// Package projection provides a disposable SQLite read cache for Workbook task refs.
package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	_ "modernc.org/sqlite"
)

const cacheFilename = "cache.sqlite"

type taskHeadSource interface {
	ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
	ReadTaskHead(context.Context, core.ProjectConfig, gitstore.TaskHead) (core.Snapshot, error)
}

type repositorySource struct {
	repository *gitstore.Repository
}

func (s repositorySource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return s.repository.ListTaskHeads(ctx, config)
}

func (s repositorySource) ReadTaskHead(ctx context.Context, config core.ProjectConfig, head gitstore.TaskHead) (core.Snapshot, error) {
	return s.repository.ReadTaskHead(ctx, config, head)
}

// Store is a read-only task store backed by a disposable SQLite projection.
type Store struct {
	source    taskHeadSource
	config    core.ProjectConfig
	cachePath string
	db        *sql.DB
	rename    func(string, string) error
}

var _ core.TaskStore = (*Store)(nil)

// Open opens the repository's shared projection cache. The first read creates
// the cache from the current Git task heads when necessary.
func Open(ctx context.Context, repository *gitstore.Repository, config core.ProjectConfig) (*Store, error) {
	if repository == nil {
		return nil, core.Errorf(core.CategoryOperational, "projection repository is required")
	}
	return openStore(ctx, repositorySource{repository: repository}, config, filepath.Join(repository.CommonGitDir, "workbook", cacheFilename))
}

func openStore(ctx context.Context, source taskHeadSource, config core.ProjectConfig, cachePath string) (*Store, error) {
	if source == nil {
		return nil, core.Errorf(core.CategoryOperational, "projection task-head source is required")
	}
	if strings.TrimSpace(cachePath) == "" {
		return nil, core.Errorf(core.CategoryOperational, "projection cache path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot create projection cache directory", err)
	}
	db, err := openDatabase(ctx, cachePath)
	if err != nil {
		return nil, err
	}
	return &Store{source: source, config: config, cachePath: cachePath, db: db, rename: os.Rename}, nil
}

// CachePath returns the disposable cache location shared by repository worktrees.
func (s *Store) CachePath() string {
	return s.cachePath
}

// Refresh validates task-ref tips, applying only changed tip checkpoints to
// the cache. Git remains the canonical source for every changed task.
func (s *Store) Refresh(ctx context.Context) error {
	heads, err := s.source.ListTaskHeads(ctx, s.config)
	if err != nil {
		return err
	}
	if !s.cacheExists() || !s.metaMatches(ctx) {
		_, err := s.Rebuild(ctx)
		return err
	}
	return s.refreshChangedHeads(ctx, heads)
}

// Rebuild recreates the projection from all currently visible Git task heads.
// It is intentionally a cache-only operation: no Git refs or objects change.
func (s *Store) Rebuild(ctx context.Context) (int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		heads, err := s.source.ListTaskHeads(ctx, s.config)
		if err != nil {
			return 0, err
		}
		temporary, err := s.buildTemporaryDatabase(ctx, heads)
		if err != nil {
			return 0, err
		}
		current, err := s.source.ListTaskHeads(ctx, s.config)
		if err != nil {
			_ = os.Remove(temporary)
			return 0, err
		}
		if equalHeads(heads, current) {
			if err := s.replaceAtomically(ctx, temporary); err != nil {
				_ = os.Remove(temporary)
				return 0, err
			}
			return len(heads), nil
		}
		_ = os.Remove(temporary)
	}
	return 0, core.Errorf(core.CategoryOperational, "task refs changed during projection rebuild; retry workbook rebuild")
}

// List returns task checkpoints from SQLite after refreshing against Git ref tips.
func (s *Store) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
	if err := s.validateConfig(config); err != nil {
		return nil, err
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s.querySnapshots(ctx)
}

// Get returns one SQLite-projected checkpoint after refreshing against Git ref tips.
func (s *Store) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	if err := s.validateConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := s.Refresh(ctx); err != nil {
		return core.Snapshot{}, err
	}
	snapshot, found, err := s.querySnapshot(ctx, taskID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if !found {
		return core.Snapshot{}, core.Errorf(core.CategoryNotFound, "task %q was not found", taskID)
	}
	return snapshot, nil
}

// Resolve returns a canonical full task ID for an unambiguous case-insensitive prefix.
func (s *Store) Resolve(ctx context.Context, config core.ProjectConfig, prefix string) (string, error) {
	if err := s.validateConfig(config); err != nil {
		return "", err
	}
	if strings.TrimSpace(prefix) == "" {
		return "", core.Errorf(core.CategoryValidation, "task ID prefix must not be blank")
	}
	if err := s.Refresh(ctx); err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM tasks ORDER BY task_id`)
	if err != nil {
		return "", s.databaseError("query projected task IDs", err)
	}
	defer rows.Close()

	needle := strings.ToLower(prefix)
	var matches []string
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return "", s.databaseError("read projected task ID", err)
		}
		if strings.HasPrefix(strings.ToLower(taskID), needle) {
			matches = append(matches, taskID)
		}
	}
	if err := rows.Err(); err != nil {
		return "", s.databaseError("read projected task IDs", err)
	}
	switch len(matches) {
	case 0:
		return "", core.Errorf(core.CategoryNotFound, "no task matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", core.Errorf(core.CategoryValidation, "task ID prefix %q is ambiguous", prefix)
	}
}

// Write is unsupported because projected state is derived solely from Git.
func (s *Store) Write(context.Context, core.ProjectConfig, *core.Snapshot, core.OperationPack, core.StateDocument, string) (core.Snapshot, error) {
	return core.Snapshot{}, core.Errorf(core.CategoryOperational, "SQLite projection is read-only; write task operations to Git")
}

func (s *Store) validateConfig(config core.ProjectConfig) error {
	if config != s.config {
		return core.Errorf(core.CategoryValidation, "Workbook configuration does not match projection cache")
	}
	return nil
}

func (s *Store) cacheExists() bool {
	_, err := os.Stat(s.cachePath)
	return err == nil
}

func (s *Store) metaMatches(ctx context.Context) bool {
	if s.db == nil {
		return false
	}
	values := make(map[string]string, 2)
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM projection_meta WHERE key IN ('schema_version', 'project_id')`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return false
		}
		values[key] = value
	}
	return rows.Err() == nil && values["schema_version"] == schemaVersion && values["project_id"] == s.config.ProjectID && s.requiredSchemaExists(ctx)
}

func (s *Store) requiredSchemaExists(ctx context.Context) bool {
	for _, query := range []string{
		`SELECT key, value FROM projection_meta LIMIT 0`,
		`SELECT task_id, head, project_id, history_generation, logical_clock, title, description, status, priority, rank, created_at, updated_at, deleted FROM tasks LIMIT 0`,
		`SELECT task_id, label FROM task_labels LIMIT 0`,
		`SELECT task_id, dependency_id FROM task_dependencies LIMIT 0`,
	} {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return false
		}
		if err := rows.Close(); err != nil {
			return false
		}
	}
	return true
}

func (s *Store) refreshChangedHeads(ctx context.Context, heads []gitstore.TaskHead) error {
	cached, err := s.cachedHeads(ctx)
	if err != nil {
		return err
	}
	current := make(map[string]string, len(heads))
	changed := make([]gitstore.TaskHead, 0)
	for _, head := range heads {
		current[head.TaskID] = head.ObjectID
		if cached[head.TaskID] != head.ObjectID {
			changed = append(changed, head)
		}
	}
	removed := make([]string, 0)
	for taskID := range cached {
		if _, found := current[taskID]; !found {
			removed = append(removed, taskID)
		}
	}
	if len(changed) == 0 && len(removed) == 0 {
		return nil
	}

	snapshots := make([]core.Snapshot, 0, len(changed))
	for _, head := range changed {
		snapshot, err := s.source.ReadTaskHead(ctx, s.config, head)
		if err != nil {
			return err
		}
		snapshots = append(snapshots, snapshot)
	}
	return s.applyChanges(ctx, snapshots, removed)
}

func (s *Store) cachedHeads(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, head FROM tasks`)
	if err != nil {
		return nil, s.databaseError("query projected task heads", err)
	}
	defer rows.Close()
	heads := make(map[string]string)
	for rows.Next() {
		var taskID, head string
		if err := rows.Scan(&taskID, &head); err != nil {
			return nil, s.databaseError("read projected task head", err)
		}
		heads[taskID] = head
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read projected task heads", err)
	}
	return heads, nil
}

func replaceSnapshots(ctx context.Context, db *sql.DB, snapshots []core.Snapshot) error {
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot begin projection rebuild", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM task_labels; DELETE FROM task_dependencies; DELETE FROM tasks`); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot clear projection rebuild", err)
	}
	for _, snapshot := range snapshots {
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot commit projection rebuild", err)
	}
	return nil
}

func (s *Store) applyChanges(ctx context.Context, snapshots []core.Snapshot, removed []string) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.databaseError("begin projection refresh", err)
	}
	defer transaction.Rollback()
	for _, taskID := range removed {
		if err := deleteTask(ctx, transaction, taskID); err != nil {
			return err
		}
	}
	for _, snapshot := range snapshots {
		if err := deleteTask(ctx, transaction, snapshot.State.TaskID); err != nil {
			return err
		}
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return s.databaseError("commit projection refresh", err)
	}
	return nil
}

func deleteTask(ctx context.Context, transaction *sql.Tx, taskID string) error {
	for _, table := range []string{"task_labels", "task_dependencies", "tasks"} {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM `+table+` WHERE task_id = ?`, taskID); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot remove projected task", err)
		}
	}
	return nil
}

func upsertSnapshot(ctx context.Context, transaction *sql.Tx, snapshot core.Snapshot) error {
	state := snapshot.State
	if snapshot.Head == "" || state.TaskID == "" || state.ProjectID == "" || state.History.Generation == "" || state.LogicalClock > math.MaxInt64 {
		return core.Errorf(core.CategoryCorruptData, "cannot project invalid task snapshot")
	}
	deleted := 0
	if state.Task.Deleted {
		deleted = 1
	}
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO tasks (task_id, head, project_id, history_generation, logical_clock, title, description, status, priority, rank, created_at, updated_at, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.TaskID, snapshot.Head, state.ProjectID, state.History.Generation, int64(state.LogicalClock), state.Task.Title, state.Task.Description,
		string(state.Task.Status), string(state.Task.Priority), state.Task.Rank, formatTime(state.Task.CreatedAt), formatTime(state.Task.UpdatedAt), deleted)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot insert projected task", err)
	}
	labels := append([]string(nil), state.Task.Labels...)
	dependencies := append([]string(nil), state.Task.Dependencies...)
	sort.Strings(labels)
	sort.Strings(dependencies)
	for _, label := range labels {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO task_labels (task_id, label) VALUES (?, ?)`, state.TaskID, label); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot insert projected task label", err)
		}
	}
	for _, dependency := range dependencies {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO task_dependencies (task_id, dependency_id) VALUES (?, ?)`, state.TaskID, dependency); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot insert projected task dependency", err)
		}
	}
	return nil
}

func (s *Store) querySnapshots(ctx context.Context) ([]core.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM tasks ORDER BY task_id`)
	if err != nil {
		return nil, s.databaseError("query projected tasks", err)
	}
	defer rows.Close()
	var snapshots []core.Snapshot
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return nil, s.databaseError("read projected task ID", err)
		}
		snapshot, found, err := s.querySnapshot(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, core.Errorf(core.CategoryCorruptData, "projected task disappeared during query")
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read projected tasks", err)
	}
	return snapshots, nil
}

func (s *Store) querySnapshot(ctx context.Context, taskID string) (core.Snapshot, bool, error) {
	var (
		snapshot core.Snapshot
		state    core.StateDocument
		status   string
		priority string
		created  string
		updated  string
		deleted  int
		clock    int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT head, project_id, history_generation, logical_clock, title, description, status, priority, rank, created_at, updated_at, deleted
		FROM tasks WHERE task_id = ?`, taskID).Scan(
		&snapshot.Head, &state.ProjectID, &state.History.Generation, &clock, &state.Task.Title, &state.Task.Description,
		&status, &priority, &state.Task.Rank, &created, &updated, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Snapshot{}, false, nil
	}
	if err != nil {
		return core.Snapshot{}, false, s.databaseError("query projected task", err)
	}
	if clock < 0 || (deleted != 0 && deleted != 1) {
		return core.Snapshot{}, false, core.Errorf(core.CategoryCorruptData, "projected task %q has invalid scalar values", taskID)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return core.Snapshot{}, false, core.Wrap(core.CategoryCorruptData, "projected task has invalid creation time", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return core.Snapshot{}, false, core.Wrap(core.CategoryCorruptData, "projected task has invalid update time", err)
	}
	labels, err := s.queryStrings(ctx, `SELECT label FROM task_labels WHERE task_id = ? ORDER BY label`, taskID)
	if err != nil {
		return core.Snapshot{}, false, err
	}
	dependencies, err := s.queryStrings(ctx, `SELECT dependency_id FROM task_dependencies WHERE task_id = ? ORDER BY dependency_id`, taskID)
	if err != nil {
		return core.Snapshot{}, false, err
	}
	state.Format = "workbook.task-state"
	state.Version = 1
	state.TaskID = taskID
	state.LogicalClock = uint64(clock)
	state.Task.Status = core.Status(status)
	state.Task.Priority = core.Priority(priority)
	state.Task.Labels = labels
	state.Task.Dependencies = dependencies
	state.Task.CreatedAt = createdAt
	state.Task.UpdatedAt = updatedAt
	state.Task.Deleted = deleted == 1
	snapshot.State = state
	return snapshot, true, nil
}

func (s *Store) queryStrings(ctx context.Context, query, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, s.databaseError("query projected task collection", err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, s.databaseError("read projected task collection", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read projected task collection", err)
	}
	return values, nil
}

func (s *Store) buildTemporaryDatabase(ctx context.Context, heads []gitstore.TaskHead) (string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(s.cachePath), "."+filepath.Base(s.cachePath)+"-*.tmp")
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot create temporary projection cache", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", core.Wrap(core.CategoryOperational, "cannot close temporary projection cache", err)
	}

	db, err := openDatabase(ctx, temporaryPath)
	if err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	defer func() {
		_ = db.Close()
	}()
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = os.Remove(temporaryPath)
		return "", core.Wrap(core.CategoryOperational, "cannot create temporary projection schema", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, schemaVersion, s.config.ProjectID); err != nil {
		_ = os.Remove(temporaryPath)
		return "", core.Wrap(core.CategoryOperational, "cannot initialize temporary projection metadata", err)
	}
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, err := s.source.ReadTaskHead(ctx, s.config, head)
		if err != nil {
			_ = os.Remove(temporaryPath)
			return "", err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := replaceSnapshots(ctx, db, snapshots); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return temporaryPath, nil
}

func (s *Store) replaceAtomically(ctx context.Context, temporary string) error {
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return s.databaseError("close projection cache", err)
		}
		s.db = nil
	}
	rename := s.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(temporary, s.cachePath); err != nil {
		if reopenErr := s.reopenDatabase(ctx); reopenErr != nil {
			return core.Wrap(core.CategoryOperational, "cannot replace projection cache and cannot reopen previous cache", errors.Join(err, reopenErr))
		}
		return core.Wrap(core.CategoryOperational, "cannot replace projection cache", err)
	}
	return s.reopenDatabase(ctx)
}

func (s *Store) reopenDatabase(ctx context.Context) error {
	db, err := openDatabase(ctx, s.cachePath)
	if err != nil {
		return err
	}
	s.db = db
	return nil
}

func equalHeads(left, right []gitstore.TaskHead) bool {
	if len(left) != len(right) {
		return false
	}
	byTaskID := make(map[string]string, len(left))
	for _, head := range left {
		byTaskID[head.TaskID] = head.ObjectID
	}
	for _, head := range right {
		if byTaskID[head.TaskID] != head.ObjectID {
			return false
		}
	}
	return true
}

func openDatabase(_ context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot open projection cache", err)
	}
	return db, nil
}

func (s *Store) databaseError(action string, err error) error {
	return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot %s", action), err)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
