// Package historyvalidation stores resumable semantic-history validation state.
package historyvalidation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	_ "modernc.org/sqlite"
)

const (
	ValidatorVersion = 1
	// schemaVersion 2 adds validatedCommitsByTaskIndex. Version 3 adds the
	// validated_operations table, which carries the operation ULIDs already
	// recorded for each task so a run can tell that a commit repeats one. The
	// cache is disposable, so a bump simply rebuilds it.
	schemaVersion              = "3"
	cacheFilename              = "validation.sqlite"
	initializationLockFilename = cacheFilename + ".lock"

	// validatedCommitsByTaskIndex serves Record's per-task DELETE. The primary
	// key leads with validator_version, whose only usable prefix matches every
	// row, so without this index each full-run task scans the whole table and a
	// full run costs O(tasks^2 x depth) row visits.
	validatedCommitsByTaskIndex = "validated_commits_by_task"
)

// readerGeneration is the writer-format generation this build can fold, and it
// is recorded in the cache because a verdict is not a property of the history
// alone — it is a property of the history and of the build that read it.
//
// Without this, a cache row survives the upgrade that makes it wrong in the
// direction that matters most. A generation-zero build folds a history
// containing a generation-one pack, records newer-writer, and exits 9; the user
// upgrades to a build that folds generation one; and `workbook validate` keeps
// reporting the same failure from cache, with a cache hit, while every mutation
// against the same task now succeeds. The row is keyed on the task head and the
// validator version, and neither of those moved.
//
// A downgrade is the same bug pointed the other way: a `valid` verdict recorded
// by a newer build says the whole chain folded, which an older build cannot
// claim about a chain it would refuse.
//
// Recording it in the metadata rather than per row follows the schema version's
// precedent, and says the same thing: the rules this cache was computed under
// are not this build's rules, so it is rebuilt. That costs one full
// revalidation on a release that changes the generation, which is rare by
// construction and cheaper than being wrong.
//
// It is a variable rather than a constant only so a test can play the upgrade.
var readerGeneration = core.SupportedFormatGeneration

type Status string

const (
	StatusPending Status = "pending"
	StatusValid   Status = "valid"
	StatusInvalid Status = "invalid"
)

type Failure struct {
	TaskID   string `json:"taskId"`
	Commit   string `json:"commit"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

type CachedTask struct {
	TaskID               string
	ObservedHead         string
	ValidatorVersion     int
	Status               Status
	LastValidCommit      string
	LastValidGeneration  string
	LastValidState       []byte
	ValidatedCommitCount int
	Failure              *Failure
}

type Completion struct {
	TaskID               string
	ObservedHead         string
	Status               Status
	LastValidCommit      string
	LastValidGeneration  string
	LastValidState       []byte
	ValidatedCommitIDs   []string
	ValidatedCommitCount int
	// ValidatedOperations is every operation ULID this run read for the task,
	// each paired with the commit that recorded it. Uniqueness is a property of
	// the whole project rather than of any one commit, so the set outlives the
	// run: a later run resuming at a cached boundary, or skipping the task
	// entirely on a cache hit, still has to be able to tell that a commit it
	// does read repeats one of these.
	//
	// It carries only what this run read, exactly like ValidatedCommitIDs, and
	// Full decides whether the recorded set replaces the stored one or extends
	// it.
	ValidatedOperations []ValidatedOperation
	Failure             *Failure
	Full                bool
}

// ValidatedOperation names one operation ULID a validated task history
// recorded, and the commit of that history that recorded it.
type ValidatedOperation struct {
	TaskID      string
	OperationID string
	CommitID    string
}

type Cache struct {
	path string
	db   *sql.DB
}

const schema = `
CREATE TABLE validation_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE task_validation (
  task_id TEXT PRIMARY KEY,
  observed_head TEXT NOT NULL,
  validator_version INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('pending','valid','invalid')),
  last_valid_commit TEXT NOT NULL,
  last_valid_generation TEXT NOT NULL,
  last_valid_state BLOB NOT NULL,
  validated_commit_count INTEGER NOT NULL,
  failure_commit TEXT NOT NULL,
  failure_category TEXT NOT NULL,
  failure_message TEXT NOT NULL
);
CREATE TABLE validated_commits (
  validator_version INTEGER NOT NULL,
  commit_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  history_generation TEXT NOT NULL,
  PRIMARY KEY (validator_version, commit_id)
);
CREATE INDEX ` + validatedCommitsByTaskIndex + ` ON validated_commits (validator_version, task_id);
CREATE TABLE validated_operations (
  validator_version INTEGER NOT NULL,
  task_id TEXT NOT NULL,
  operation_id TEXT NOT NULL,
  commit_id TEXT NOT NULL,
  PRIMARY KEY (validator_version, task_id, operation_id)
);
`

const taskColumns = `
	task_id, observed_head, validator_version, status,
	last_valid_commit, last_valid_generation, last_valid_state,
	validated_commit_count, failure_commit, failure_category, failure_message
`

func OpenCache(ctx context.Context, commonGitDir string, config core.ProjectConfig) (*Cache, error) {
	if strings.TrimSpace(commonGitDir) == "" {
		return nil, core.Errorf(core.CategoryOperational, "validation cache requires a Git common directory")
	}
	if strings.TrimSpace(config.ProjectID) == "" {
		return nil, core.Errorf(core.CategoryValidation, "validation cache requires a project ID")
	}
	path := filepath.Join(commonGitDir, "workbook", cacheFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, cacheError("create validation cache directory", err)
	}

	db, usable, err := openUsableDatabase(ctx, path, config.ProjectID)
	if err != nil {
		return nil, err
	}
	if usable {
		return &Cache{path: path, db: db}, nil
	}

	lock, err := acquireInitializationLock(ctx, filepath.Join(filepath.Dir(path), initializationLockFilename))
	if err != nil {
		return nil, err
	}
	defer releaseInitializationLock(lock)

	db, usable, err = openUsableDatabase(ctx, path, config.ProjectID)
	if err != nil {
		return nil, err
	}
	if usable {
		return &Cache{path: path, db: db}, nil
	}

	db, err = rebuildDatabase(ctx, path, config.ProjectID)
	if err != nil {
		return nil, err
	}
	return &Cache{path: path, db: db}, nil
}

func (c *Cache) Path() string {
	return c.path
}

func (c *Cache) Prepare(
	ctx context.Context,
	heads []gitstore.TaskHead,
	full bool,
) (map[string]CachedTask, error) {
	if c == nil || c.db == nil {
		return nil, core.Errorf(core.CategoryOperational, "validation cache is closed")
	}
	observed := make(map[string]string, len(heads))
	for _, head := range heads {
		if strings.TrimSpace(head.TaskID) == "" || strings.TrimSpace(head.ObjectID) == "" {
			return nil, core.Errorf(core.CategoryValidation, "validation task head requires a task ID and object ID")
		}
		if _, duplicate := observed[head.TaskID]; duplicate {
			return nil, core.Errorf(core.CategoryValidation, "duplicate validation task ID %q", head.TaskID)
		}
		observed[head.TaskID] = head.ObjectID
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, cacheError("begin validation cache preparation", err)
	}
	defer tx.Rollback()

	for _, head := range heads {
		cached, found, err := queryCachedTask(ctx, tx, head.TaskID)
		if err != nil {
			return nil, cacheError("read validation cache preparation state", err)
		}
		if !found {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO task_validation (
					task_id, observed_head, validator_version, status,
					last_valid_commit, last_valid_generation, last_valid_state,
					validated_commit_count, failure_commit, failure_category, failure_message
				) VALUES (?, ?, ?, ?, '', '', x'', 0, '', '', '')
			`, head.TaskID, head.ObjectID, ValidatorVersion, StatusPending); err != nil {
				return nil, cacheError("insert pending validation task", err)
			}
			continue
		}
		if !full &&
			cached.ObservedHead == head.ObjectID &&
			cached.ValidatorVersion == ValidatorVersion {
			continue
		}
		if cached.ValidatorVersion != ValidatorVersion {
			if _, err := tx.ExecContext(ctx, `
				UPDATE task_validation
				SET observed_head = ?,
				    validator_version = ?,
				    status = ?,
				    last_valid_commit = '',
				    last_valid_generation = '',
				    last_valid_state = x'',
				    validated_commit_count = 0,
				    failure_commit = '',
				    failure_category = '',
				    failure_message = ''
				WHERE task_id = ?
			`, head.ObjectID, ValidatorVersion, StatusPending, head.TaskID); err != nil {
				return nil, cacheError("invalidate prior-version validation task", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_validation
			SET observed_head = ?,
			    validator_version = ?,
			    status = ?,
			    failure_commit = '',
			    failure_category = '',
			    failure_message = ''
			WHERE task_id = ?
		`, head.ObjectID, ValidatorVersion, StatusPending, head.TaskID); err != nil {
			return nil, cacheError("mark validation task pending", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM task_validation`)
	if err != nil {
		return nil, cacheError("list cached validation tasks", err)
	}
	var absent []string
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			_ = rows.Close()
			return nil, cacheError("read cached validation task ID", err)
		}
		if _, exists := observed[taskID]; !exists {
			absent = append(absent, taskID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, cacheError("read cached validation task IDs", err)
	}
	if err := rows.Close(); err != nil {
		return nil, cacheError("close cached validation task rows", err)
	}
	for _, taskID := range absent {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_validation WHERE task_id = ?`, taskID); err != nil {
			return nil, cacheError("remove absent validation task", err)
		}
		// This task's operation set is consulted on behalf of every other task,
		// so a ref that is gone takes its ULIDs with it rather than leaving them
		// to be reported against a chain that still exists.
		if _, err := tx.ExecContext(ctx, `DELETE FROM validated_operations WHERE task_id = ?`, taskID); err != nil {
			return nil, cacheError("remove absent validation task operations", err)
		}
	}

	prepared := make(map[string]CachedTask, len(heads))
	for _, head := range heads {
		cached, found, err := queryCachedTask(ctx, tx, head.TaskID)
		if err != nil {
			return nil, cacheError("read prepared validation task", err)
		}
		if !found {
			return nil, core.Errorf(core.CategoryOperational, "prepared validation task %q disappeared", head.TaskID)
		}
		prepared[head.TaskID] = cached
	}
	if err := tx.Commit(); err != nil {
		return nil, cacheError("commit validation cache preparation", err)
	}
	return prepared, nil
}

func (c *Cache) Record(ctx context.Context, completion Completion) error {
	if c == nil || c.db == nil {
		return core.Errorf(core.CategoryOperational, "validation cache is closed")
	}
	if err := validateCompletion(completion); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return cacheError("begin validation completion", err)
	}
	defer tx.Rollback()

	var observedHead string
	var status Status
	err = tx.QueryRowContext(ctx, `
		SELECT observed_head, status
		FROM task_validation
		WHERE task_id = ?
	`, completion.TaskID).Scan(&observedHead, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Errorf(core.CategoryStaleWrite, "validation task %q is not prepared", completion.TaskID)
	}
	if err != nil {
		return cacheError("read prepared validation task", err)
	}
	if observedHead != completion.ObservedHead {
		return core.Errorf(
			core.CategoryStaleWrite,
			"validation task %q observed head changed from %q to %q",
			completion.TaskID,
			completion.ObservedHead,
			observedHead,
		)
	}
	if status != StatusPending {
		return core.Errorf(
			core.CategoryStaleWrite,
			"validation task %q is %q instead of pending",
			completion.TaskID,
			status,
		)
	}

	if completion.Full {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM validated_commits
			WHERE validator_version = ? AND task_id = ?
		`, ValidatorVersion, completion.TaskID); err != nil {
			return cacheError("replace full validation commit set", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM validated_operations
			WHERE validator_version = ? AND task_id = ?
		`, ValidatorVersion, completion.TaskID); err != nil {
			return cacheError("replace full validation operation set", err)
		}
	}
	if len(completion.ValidatedOperations) > 0 {
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO validated_operations (
				validator_version, task_id, operation_id, commit_id
			) VALUES (?, ?, ?, ?)
		`)
		if err != nil {
			return cacheError("prepare validated operation insert", err)
		}
		defer insert.Close()
		for _, operation := range completion.ValidatedOperations {
			if _, err := insert.ExecContext(ctx, ValidatorVersion, completion.TaskID, operation.OperationID, operation.CommitID); err != nil {
				return cacheError("record validated operation", err)
			}
		}
	}
	// One prepared insert serves the whole task. A deep history otherwise pays
	// SQLite's parse and plan cost once per commit for an identical statement.
	if len(completion.ValidatedCommitIDs) > 0 {
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO validated_commits (
				validator_version, commit_id, task_id, history_generation
			) VALUES (?, ?, ?, ?)
		`)
		if err != nil {
			return cacheError("prepare validated commit insert", err)
		}
		defer insert.Close()
		for _, commitID := range completion.ValidatedCommitIDs {
			if strings.TrimSpace(commitID) == "" {
				return core.Errorf(core.CategoryValidation, "validated commit ID must not be blank")
			}
			if _, err := insert.ExecContext(ctx, ValidatorVersion, commitID, completion.TaskID, completion.LastValidGeneration); err != nil {
				return cacheError("record validated commit", err)
			}
		}
	}

	failureCommit, failureCategory, failureMessage := "", "", ""
	if completion.Failure != nil {
		failureCommit = completion.Failure.Commit
		failureCategory = completion.Failure.Category
		failureMessage = completion.Failure.Message
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE task_validation
		SET validator_version = ?,
		    status = ?,
		    last_valid_commit = ?,
		    last_valid_generation = ?,
		    last_valid_state = ?,
		    validated_commit_count = ?,
		    failure_commit = ?,
		    failure_category = ?,
		    failure_message = ?
		WHERE task_id = ? AND observed_head = ? AND status = ?
	`,
		ValidatorVersion,
		completion.Status,
		completion.LastValidCommit,
		completion.LastValidGeneration,
		completion.LastValidState,
		completion.ValidatedCommitCount,
		failureCommit,
		failureCategory,
		failureMessage,
		completion.TaskID,
		completion.ObservedHead,
		StatusPending,
	)
	if err != nil {
		return cacheError("record validation completion", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return cacheError("confirm validation completion", err)
	}
	if updated != 1 {
		return core.Errorf(core.CategoryStaleWrite, "validation task %q changed while recording completion", completion.TaskID)
	}
	if err := tx.Commit(); err != nil {
		return cacheError("commit validation completion", err)
	}
	return nil
}

// ValidatedOperations returns every operation ULID this validator version has
// recorded, for every task the cache holds, each with the task and commit that
// recorded it.
//
// It is read whole rather than task by task because the property it defends is
// project-wide: the projection keys its operation rows on the ULID alone, so a
// run checking a single task still has to see the ULIDs of the tasks it is
// about to skip.
//
// The caller turns the result into a map that stays resident for the whole
// fold, which is the one whole-corpus resident set in a code path that
// otherwise streams a commit at a time. The rows are one short string triple
// per operation — roughly 250 bytes each once mapped, so a few megabytes at the
// 500-task, 20-operation size the benchmark fixture uses, and linear in the
// project's operation count above it. `validate --full` takes nothing from the
// cache and does not call this at all.
func (c *Cache) ValidatedOperations(ctx context.Context) ([]ValidatedOperation, error) {
	if c == nil || c.db == nil {
		return nil, core.Errorf(core.CategoryOperational, "validation cache is closed")
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT task_id, operation_id, commit_id
		FROM validated_operations
		WHERE validator_version = ?
		ORDER BY task_id, operation_id
	`, ValidatorVersion)
	if err != nil {
		return nil, cacheError("read validated operations", err)
	}
	defer rows.Close()
	var operations []ValidatedOperation
	for rows.Next() {
		var operation ValidatedOperation
		if err := rows.Scan(&operation.TaskID, &operation.OperationID, &operation.CommitID); err != nil {
			return nil, cacheError("read validated operation row", err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, cacheError("read validated operations", err)
	}
	return operations, nil
}

func (c *Cache) Snapshot(ctx context.Context, taskIDs []string) ([]CachedTask, error) {
	if c == nil || c.db == nil {
		return nil, core.Errorf(core.CategoryOperational, "validation cache is closed")
	}
	if len(taskIDs) == 0 {
		rows, err := c.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM task_validation ORDER BY task_id`)
		if err != nil {
			return nil, cacheError("query validation snapshot", err)
		}
		defer rows.Close()
		result := []CachedTask{}
		for rows.Next() {
			cached, err := scanCachedTask(rows)
			if err != nil {
				return nil, cacheError("read validation snapshot", err)
			}
			result = append(result, cached)
		}
		if err := rows.Err(); err != nil {
			return nil, cacheError("read validation snapshot", err)
		}
		return result, nil
	}

	result := make([]CachedTask, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		cached, found, err := queryCachedTask(ctx, c.db, taskID)
		if err != nil {
			return nil, cacheError("read validation snapshot", err)
		}
		if found {
			result = append(result, cached)
		}
	}
	return result, nil
}

func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}

func validateCompletion(completion Completion) error {
	if strings.TrimSpace(completion.TaskID) == "" || strings.TrimSpace(completion.ObservedHead) == "" {
		return core.Errorf(core.CategoryValidation, "validation completion requires a task ID and observed head")
	}
	if completion.ValidatedCommitCount < 0 {
		return core.Errorf(core.CategoryValidation, "validated commit count must not be negative")
	}
	// The stored row is read back on behalf of every other task, so an entry
	// that names no operation, no recording commit, or a task other than the one
	// being recorded would misattribute a later report rather than merely be
	// useless.
	for _, operation := range completion.ValidatedOperations {
		if strings.TrimSpace(operation.OperationID) == "" {
			return core.Errorf(core.CategoryValidation, "validated operation ID must not be blank")
		}
		if strings.TrimSpace(operation.CommitID) == "" {
			return core.Errorf(core.CategoryValidation, "validated operation %q must name the commit that recorded it", operation.OperationID)
		}
		if operation.TaskID != completion.TaskID {
			return core.Errorf(
				core.CategoryValidation,
				"validated operation %q belongs to task %q, not %q",
				operation.OperationID, operation.TaskID, completion.TaskID,
			)
		}
	}
	switch completion.Status {
	case StatusValid:
		if completion.Failure != nil {
			return core.Errorf(core.CategoryValidation, "valid validation completion must not contain a failure")
		}
	case StatusInvalid:
		if completion.Failure == nil {
			return core.Errorf(core.CategoryValidation, "invalid validation completion requires a failure")
		}
		if completion.Failure.TaskID != completion.TaskID {
			return core.Errorf(core.CategoryValidation, "validation failure task ID does not match completion")
		}
		if strings.TrimSpace(completion.Failure.Commit) == "" ||
			strings.TrimSpace(completion.Failure.Category) == "" ||
			strings.TrimSpace(completion.Failure.Message) == "" {
			return core.Errorf(core.CategoryValidation, "validation failure requires commit, category, and message")
		}
	default:
		return core.Errorf(core.CategoryValidation, "validation completion status must be valid or invalid")
	}
	return nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryCachedTask(ctx context.Context, queryer queryRower, taskID string) (CachedTask, bool, error) {
	row := queryer.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task_validation WHERE task_id = ?`, taskID)
	cached, err := scanCachedTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedTask{}, false, nil
	}
	return cached, err == nil, err
}

type scanner interface {
	Scan(...any) error
}

func scanCachedTask(row scanner) (CachedTask, error) {
	var cached CachedTask
	var failureCommit, failureCategory, failureMessage string
	if err := row.Scan(
		&cached.TaskID,
		&cached.ObservedHead,
		&cached.ValidatorVersion,
		&cached.Status,
		&cached.LastValidCommit,
		&cached.LastValidGeneration,
		&cached.LastValidState,
		&cached.ValidatedCommitCount,
		&failureCommit,
		&failureCategory,
		&failureMessage,
	); err != nil {
		return CachedTask{}, err
	}
	if failureCommit != "" || failureCategory != "" || failureMessage != "" {
		cached.Failure = &Failure{
			TaskID:   cached.TaskID,
			Commit:   failureCommit,
			Category: failureCategory,
			Message:  failureMessage,
		}
	}
	return cached, nil
}

func openDatabase(path string) (*sql.DB, error) {
	dsn := &url.URL{Scheme: "file", Path: path}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	// A full validation commits one transaction per task, which under the
	// default rollback journal and synchronous FULL costs about two fsyncs per
	// task. This cache is disposable and is rebuilt from Git whenever it is
	// unusable, so durability across a host crash buys nothing that a rebuild
	// does not already provide.
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, cacheError("open validation cache", err)
	}
	return db, nil
}

func openUsableDatabase(ctx context.Context, path, projectID string) (*sql.DB, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, cacheError("inspect validation cache", err)
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, false, err
	}
	if databaseUsable(ctx, db, projectID) {
		return db, true, nil
	}
	_ = db.Close()
	if err := ctx.Err(); err != nil {
		return nil, false, cacheError("inspect validation cache", err)
	}
	return nil, false, nil
}

func rebuildDatabase(ctx context.Context, path, projectID string) (*sql.DB, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+cacheFilename+"-*.tmp")
	if err != nil {
		return nil, cacheError("create temporary validation cache", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, cacheError("close temporary validation cache", err)
	}
	cleanup := func() {
		_ = os.Remove(temporaryPath)
		_ = os.Remove(temporaryPath + "-wal")
		_ = os.Remove(temporaryPath + "-shm")
	}

	db, err := openDatabase(temporaryPath)
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		cleanup()
		return nil, cacheError("create validation cache schema", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO validation_meta (key, value)
		VALUES ('schema_version', ?), ('project_id', ?), ('reader_generation', ?)
	`, schemaVersion, projectID, strconv.Itoa(readerGeneration)); err != nil {
		_ = db.Close()
		cleanup()
		return nil, cacheError("initialize validation cache metadata", err)
	}
	if err := db.Close(); err != nil {
		cleanup()
		return nil, cacheError("close initialized validation cache", err)
	}

	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	if err := os.Rename(temporaryPath, path); err != nil {
		cleanup()
		return nil, cacheError("replace validation cache", err)
	}
	db, err = openDatabase(path)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func databaseUsable(ctx context.Context, db *sql.DB, projectID string) bool {
	if db == nil {
		return false
	}
	var quickCheck string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		return false
	}
	values := make(map[string]string, 3)
	rows, err := db.QueryContext(ctx, `
		SELECT key, value
		FROM validation_meta
		WHERE key IN ('schema_version', 'project_id', 'reader_generation')
	`)
	if err != nil {
		return false
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			return false
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false
	}
	if err := rows.Close(); err != nil {
		return false
	}
	// A cache written by a build that folds a different set of generations is
	// discarded rather than read. See readerGeneration for what a surviving row
	// would claim.
	if values["schema_version"] != schemaVersion ||
		values["project_id"] != projectID ||
		values["reader_generation"] != strconv.Itoa(readerGeneration) {
		return false
	}
	for _, query := range []string{
		`SELECT key, value FROM validation_meta LIMIT 0`,
		`SELECT ` + taskColumns + ` FROM task_validation LIMIT 0`,
		`SELECT validator_version, commit_id, task_id, history_generation FROM validated_commits LIMIT 0`,
		`SELECT validator_version, task_id, operation_id, commit_id FROM validated_operations LIMIT 0`,
	} {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return false
		}
		if err := rows.Close(); err != nil {
			return false
		}
	}
	// The per-task index is what keeps a full run linear in task count, so a
	// file that lost it is rebuilt rather than used slowly.
	var indexName string
	if err := db.QueryRowContext(ctx, `
		SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?
	`, validatedCommitsByTaskIndex).Scan(&indexName); err != nil || indexName != validatedCommitsByTaskIndex {
		return false
	}
	return true
}

func cacheError(action string, err error) error {
	return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot %s validation cache", action), err)
}
