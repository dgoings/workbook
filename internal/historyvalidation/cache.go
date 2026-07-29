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
	"strings"
	"syscall"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	_ "modernc.org/sqlite"
)

const (
	ValidatorVersion           = 1
	schemaVersion              = "1"
	cacheFilename              = "validation.sqlite"
	initializationLockFilename = cacheFilename + ".lock"
)

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
	Failure              *Failure
	Full                 bool
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
	}
	for _, commitID := range completion.ValidatedCommitIDs {
		if strings.TrimSpace(commitID) == "" {
			return core.Errorf(core.CategoryValidation, "validated commit ID must not be blank")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO validated_commits (
				validator_version, commit_id, task_id, history_generation
			) VALUES (?, ?, ?, ?)
		`, ValidatorVersion, commitID, completion.TaskID, completion.LastValidGeneration); err != nil {
			return cacheError("record validated commit", err)
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

func acquireInitializationLock(ctx context.Context, path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, cacheError("open validation cache initialization lock", err)
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = lock.Close()
			return nil, cacheError("acquire validation cache initialization lock", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = lock.Close()
			return nil, cacheError("acquire validation cache initialization lock", ctx.Err())
		case <-timer.C:
		}
	}
}

func releaseInitializationLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
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
		VALUES ('schema_version', ?), ('project_id', ?)
	`, schemaVersion, projectID); err != nil {
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
	values := make(map[string]string, 2)
	rows, err := db.QueryContext(ctx, `
		SELECT key, value
		FROM validation_meta
		WHERE key IN ('schema_version', 'project_id')
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
	if values["schema_version"] != schemaVersion || values["project_id"] != projectID {
		return false
	}
	for _, query := range []string{
		`SELECT key, value FROM validation_meta LIMIT 0`,
		`SELECT ` + taskColumns + ` FROM task_validation LIMIT 0`,
		`SELECT validator_version, commit_id, task_id, history_generation FROM validated_commits LIMIT 0`,
	} {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return false
		}
		if err := rows.Close(); err != nil {
			return false
		}
	}
	return true
}

func cacheError(action string, err error) error {
	return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot %s validation cache", action), err)
}
