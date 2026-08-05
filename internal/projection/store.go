// Package projection provides a disposable SQLite read cache for Workbook task refs.
package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	_ "modernc.org/sqlite"
)

const (
	cacheFilename     = "cache.sqlite"
	cacheRecoveryHint = "run `workbook rebuild` to recreate the SQLite cache"
)

var errStaleProjectionRefresh = errors.New("projection cache changed during refresh")

type taskHeadSource interface {
	ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
	InspectTaskHead(context.Context, core.ProjectConfig, string) (gitstore.TaskHead, bool, error)
	ReadTaskHeads(context.Context, core.ProjectConfig, []gitstore.TaskHead) ([]core.Snapshot, error)
	ValidateTaskHeadAdvances(context.Context, core.ProjectConfig, []gitstore.HeadAdvance) error
	ReadTaskOperations(context.Context, core.ProjectConfig, []gitstore.TaskHistoryRequest) ([]gitstore.TaskOperationsResult, error)
}

type repositorySource struct {
	repository *gitstore.Repository
}

func (s repositorySource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return s.repository.ListTaskHeads(ctx, config)
}

func (s repositorySource) InspectTaskHead(ctx context.Context, config core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	return s.repository.InspectTaskHead(ctx, config, taskID)
}

func (s repositorySource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	return s.repository.ReadTaskHeads(ctx, config, heads)
}

func (s repositorySource) ValidateTaskHeadAdvances(ctx context.Context, config core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	return s.repository.ValidateTaskHeadAdvances(ctx, config, advances)
}

func (s repositorySource) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	return s.repository.ReadTaskOperations(ctx, config, requests)
}

// Store is a read-only task store backed by a disposable SQLite projection.
type Store struct {
	rebuildMu sync.RWMutex
	source    taskHeadSource
	config    core.ProjectConfig
	cachePath string
	db        *sql.DB
	rename    func(string, string) error
}

var _ core.TaskReader = (*Store)(nil)
var _ core.ProjectionUpdater = (*Store)(nil)
var _ core.TaskHistorySource = (*Store)(nil)

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
		return nil, cacheError("create projection cache directory", err)
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
	if err := s.lockActiveDatabase(ctx); err != nil {
		return err
	}
	defer s.rebuildMu.RUnlock()
	return s.refreshActive(ctx)
}

func (s *Store) refreshActive(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		cached, err := s.cachedTaskHeads(ctx)
		if err != nil {
			return err
		}
		heads, err := s.source.ListTaskHeads(ctx, s.config)
		if err != nil {
			return err
		}
		err = s.refreshChangedHeads(ctx, cached, heads)
		if !errors.Is(err, errStaleProjectionRefresh) {
			return err
		}
	}
	return cacheError("refresh projection cache after a concurrent update", errStaleProjectionRefresh)
}

// Rebuild recreates the projection from all currently visible Git task heads.
// It is intentionally a cache-only operation: no Git refs or objects change.
func (s *Store) Rebuild(ctx context.Context) (int, error) {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	return s.rebuildLocked(ctx)
}

func (s *Store) rebuildLocked(ctx context.Context) (int, error) {
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
	if err := s.lockActiveDatabase(ctx); err != nil {
		return nil, err
	}
	defer s.rebuildMu.RUnlock()
	if err := s.refreshActive(ctx); err != nil {
		return nil, err
	}
	return s.querySnapshots(ctx)
}

// Get returns one SQLite-projected checkpoint after inspecting its exact Git ref.
func (s *Store) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	if err := s.validateConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return core.Snapshot{}, err
	}
	defer s.rebuildMu.RUnlock()
	return s.getExactActive(ctx, taskID)
}

// TaskHistory returns one task's recorded operation packs, oldest first along
// the parent chain, after refreshing that task from its exact Git ref.
func (s *Store) TaskHistory(ctx context.Context, config core.ProjectConfig, taskID string) (core.TaskHistory, error) {
	if err := s.validateConfig(config); err != nil {
		return core.TaskHistory{}, err
	}
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.TaskHistory{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return core.TaskHistory{}, err
	}
	defer s.rebuildMu.RUnlock()

	snapshot, err := s.getExactActive(ctx, taskID)
	if err != nil {
		return core.TaskHistory{}, err
	}
	history, found, err := s.readProjectedHistory(ctx, taskID, "")
	if err != nil {
		return core.TaskHistory{}, err
	}
	if found {
		return history, nil
	}
	return s.gitHistory(ctx, taskID, snapshot.Head)
}

// CommitHistory returns the chain ending at one named commit object.
//
// The fallback to Git is gated per commit as well as per task. The projection
// is fed from refs/workbook/tasks/ only, so a parked pre-replay commit has no
// row even when its task is fully projected, and a per-task gate would never
// fire for the comparison argument that most needs it.
func (s *Store) CommitHistory(ctx context.Context, config core.ProjectConfig, taskID, commit string) (core.TaskHistory, error) {
	if err := s.validateConfig(config); err != nil {
		return core.TaskHistory{}, err
	}
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.TaskHistory{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return core.TaskHistory{}, err
	}
	defer s.rebuildMu.RUnlock()

	if _, err := s.getExactActive(ctx, taskID); err != nil {
		return core.TaskHistory{}, err
	}
	history, found, err := s.readProjectedHistory(ctx, taskID, commit)
	if err != nil {
		return core.TaskHistory{}, err
	}
	if found {
		return history, nil
	}
	return s.gitHistory(ctx, taskID, commit)
}

// Resolve returns a canonical full task ID for an unambiguous case-insensitive prefix.
func (s *Store) Resolve(ctx context.Context, config core.ProjectConfig, prefix string) (string, error) {
	if err := s.validateConfig(config); err != nil {
		return "", err
	}
	if strings.TrimSpace(prefix) == "" {
		return "", core.Errorf(core.CategoryValidation, "task ID prefix must not be blank")
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return "", err
	}
	defer s.rebuildMu.RUnlock()
	if err := s.refreshActive(ctx); err != nil {
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

// Advance conditionally replaces one projected task checkpoint.
func (s *Store) Advance(ctx context.Context, config core.ProjectConfig, expectedParent string, snapshot core.Snapshot) (bool, error) {
	if err := s.validateConfig(config); err != nil {
		return false, err
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return false, err
	}
	defer s.rebuildMu.RUnlock()
	return s.advanceActive(ctx, expectedParent, snapshot, nil)
}

// Invalidate removes one projected task only when it still names an expected head.
func (s *Store) Invalidate(ctx context.Context, config core.ProjectConfig, taskID, expectedParent, writtenHead string) error {
	if err := s.validateConfig(config); err != nil {
		return err
	}
	if err := s.lockActiveDatabase(ctx); err != nil {
		return err
	}
	defer s.rebuildMu.RUnlock()
	return s.invalidateActive(ctx, taskID, expectedParent, writtenHead)
}

func (s *Store) validateConfig(config core.ProjectConfig) error {
	if config != s.config {
		return core.Errorf(core.CategoryValidation, "Workbook configuration does not match projection cache")
	}
	return nil
}

// lockActiveDatabase returns with rebuildMu held for reading. If the active
// database is missing or incompatible, it drops the read lock before taking
// the exclusive rebuild lock and rechecks the cache there.
func (s *Store) lockActiveDatabase(ctx context.Context) error {
	s.rebuildMu.RLock()
	if s.cacheUsable(ctx) {
		return nil
	}
	s.rebuildMu.RUnlock()

	s.rebuildMu.Lock()
	if !s.cacheUsable(ctx) {
		if _, err := s.rebuildLocked(ctx); err != nil {
			s.rebuildMu.Unlock()
			return err
		}
	}
	s.rebuildMu.Unlock()

	s.rebuildMu.RLock()
	if s.cacheUsable(ctx) {
		return nil
	}
	s.rebuildMu.RUnlock()
	return cacheError("activate projection cache after rebuild", errors.New("projection cache is unavailable"))
}

func (s *Store) cacheUsable(ctx context.Context) bool {
	return s.cacheExists() && s.metaMatches(ctx)
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
		`SELECT operation_id, task_id, commit_id, chain_index, pack_index, logical_clock, history_generation, actor, wall_time, type, field, value, task_data FROM operations LIMIT 0`,
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

func (s *Store) getExactActive(ctx context.Context, taskID string) (core.Snapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		cached, cachedFound, err := s.querySnapshot(ctx, taskID)
		if err != nil {
			return core.Snapshot{}, err
		}
		head, headFound, err := s.source.InspectTaskHead(ctx, s.config, taskID)
		if err != nil {
			return core.Snapshot{}, err
		}
		if !headFound {
			if !cachedFound {
				return core.Snapshot{}, core.Errorf(core.CategoryNotFound, "task %q was not found", taskID)
			}
			return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task %q ref disappeared after it was projected", taskID)
		}
		if cachedFound && cached.Head == head.ObjectID {
			return cached, nil
		}

		if cachedFound {
			if err := s.source.ValidateTaskHeadAdvances(ctx, s.config, []gitstore.HeadAdvance{
				headAdvance(cached, head),
			}); err != nil {
				return core.Snapshot{}, err
			}
		}
		snapshots, err := s.source.ReadTaskHeads(ctx, s.config, []gitstore.TaskHead{head})
		if err != nil {
			return core.Snapshot{}, err
		}
		if len(snapshots) != 1 {
			return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "exact task read returned %d snapshots, want 1", len(snapshots))
		}
		snapshot := snapshots[0]
		if err := validateReadSnapshot(head, snapshot); err != nil {
			return core.Snapshot{}, err
		}
		if cachedFound && snapshot.State.History.Generation != cached.State.History.Generation {
			return core.Snapshot{}, historyGenerationChanged(taskID)
		}
		expectedParent := ""
		if cachedFound {
			expectedParent = cached.Head
		}
		// One exact read can cross many commits, so the operation rows come
		// from the same head-to-head walk the broad refresh uses rather than
		// from the tip alone.
		chains, err := s.readOperationChains(ctx, []gitstore.TaskHead{head}, map[string]cachedTaskHead{
			taskID: {head: expectedParent},
		})
		if err != nil {
			return core.Snapshot{}, err
		}
		applied, err := s.advanceActive(ctx, expectedParent, snapshot, &chains[0])
		if err != nil {
			return core.Snapshot{}, err
		}
		if applied {
			return snapshot, nil
		}
	}
	return core.Snapshot{}, cacheError("refresh exact projected task after a concurrent update", errStaleProjectionRefresh)
}

func (s *Store) refreshChangedHeads(ctx context.Context, cached map[string]cachedTaskHead, heads []gitstore.TaskHead) error {
	expectedHeads := make(map[string]string, len(cached))
	for taskID, previous := range cached {
		expectedHeads[taskID] = previous.head
	}
	current := make(map[string]string, len(heads))
	changed := make([]gitstore.TaskHead, 0)
	advances := make([]gitstore.HeadAdvance, 0)
	for _, head := range heads {
		current[head.TaskID] = head.ObjectID
		previous, found := cached[head.TaskID]
		if !found || previous.head != head.ObjectID {
			changed = append(changed, head)
		}
		if found && previous.head != head.ObjectID {
			advances = append(advances, projectedHeadAdvance(head.TaskID, previous, head))
		}
	}
	missing := make([]string, 0)
	for taskID := range cached {
		if _, found := current[taskID]; !found {
			missing = append(missing, taskID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return core.Errorf(core.CategoryCorruptData, "task %q ref disappeared after it was projected", missing[0])
	}
	if len(changed) == 0 {
		return nil
	}

	if len(advances) > 0 {
		if err := s.source.ValidateTaskHeadAdvances(ctx, s.config, advances); err != nil {
			return err
		}
	}
	snapshots, err := s.source.ReadTaskHeads(ctx, s.config, changed)
	if err != nil {
		return err
	}
	if len(snapshots) != len(changed) {
		return core.Errorf(core.CategoryCorruptData, "task head batch returned %d snapshots, want %d", len(snapshots), len(changed))
	}
	for i, snapshot := range snapshots {
		head := changed[i]
		if err := validateReadSnapshot(head, snapshot); err != nil {
			return err
		}
		if previous, found := cached[head.TaskID]; found &&
			snapshot.State.History.Generation != previous.historyGeneration {
			return historyGenerationChanged(head.TaskID)
		}
	}
	// Comparing tips is complete for current state but blind to the
	// intermediate commits an operation table needs: a fetch advancing a task
	// twelve commits yields one new tip and eleven unread packs. Walking each
	// changed task from its projected head to its new one reads exactly those.
	chains, err := s.readOperationChains(ctx, changed, cached)
	if err != nil {
		return err
	}
	return s.applyChanges(ctx, expectedHeads, snapshots, chains)
}

// readOperationChains walks every changed task from the head the projection
// already holds to its current one.
func (s *Store) readOperationChains(
	ctx context.Context,
	changed []gitstore.TaskHead,
	cached map[string]cachedTaskHead,
) ([]gitstore.TaskOperationsResult, error) {
	requests := make([]gitstore.TaskHistoryRequest, 0, len(changed))
	for _, head := range changed {
		request := gitstore.TaskHistoryRequest{Head: head}
		if previous, found := cached[head.TaskID]; found {
			request.StopAt = previous.head
		}
		requests = append(requests, request)
	}
	chains, err := s.source.ReadTaskOperations(ctx, s.config, requests)
	if err != nil {
		return nil, err
	}
	if len(chains) != len(requests) {
		return nil, core.Errorf(core.CategoryCorruptData, "task operation read returned %d chains, want %d", len(chains), len(requests))
	}
	return chains, nil
}

// projectChain records one task's newly read commits. A new head that does not
// descend from the projected one restarts the read at the root and reports
// BoundaryReached false, which is the reconciliation signal: replay may have
// orphaned rows this task still owns, so they are deleted before the returned
// chain is projected rather than upserted into.
func projectChain(ctx context.Context, transaction *sql.Tx, chain gitstore.TaskOperationsResult, previousHead string) error {
	if !chain.BoundaryReached {
		return replaceOperations(ctx, transaction, chain.TaskID, chain.Commits)
	}
	return appendOperations(ctx, transaction, chain.TaskID, previousHead, chain.Commits)
}

type cachedTaskHead struct {
	head              string
	historyGeneration string
}

func (s *Store) cachedTaskHeads(ctx context.Context) (map[string]cachedTaskHead, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, head, history_generation FROM tasks`)
	if err != nil {
		return nil, s.databaseError("query projected task heads", err)
	}
	defer rows.Close()
	heads := make(map[string]cachedTaskHead)
	for rows.Next() {
		var taskID string
		var head cachedTaskHead
		if err := rows.Scan(&taskID, &head.head, &head.historyGeneration); err != nil {
			return nil, s.databaseError("read projected task head", err)
		}
		heads[taskID] = head
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read projected task heads", err)
	}
	return heads, nil
}

func headAdvance(previous core.Snapshot, current gitstore.TaskHead) gitstore.HeadAdvance {
	previous.Operation.TaskID = previous.State.TaskID
	return gitstore.HeadAdvance{Previous: previous, Current: current}
}

func projectedHeadAdvance(taskID string, previous cachedTaskHead, current gitstore.TaskHead) gitstore.HeadAdvance {
	return gitstore.HeadAdvance{
		Previous: core.Snapshot{
			Head:      previous.head,
			Operation: core.OperationPack{TaskID: taskID},
			State: core.StateDocument{
				TaskID:  taskID,
				History: core.History{Generation: previous.historyGeneration},
			},
		},
		Current: current,
	}
}

func validateReadSnapshot(head gitstore.TaskHead, snapshot core.Snapshot) error {
	if snapshot.Head != head.ObjectID || snapshot.State.TaskID != head.TaskID {
		return core.Errorf(core.CategoryCorruptData, "task head batch returned a snapshot for the wrong task or object")
	}
	return nil
}

func historyGenerationChanged(taskID string) error {
	return core.Errorf(core.CategoryCorruptData, "task %q history generation changed across a projected head advance", taskID)
}

// advanceActive conditionally replaces one projected task. chain carries the
// commits a multi-commit advance crossed; a nil chain means the caller just
// wrote exactly one commit onto expectedParent and the snapshot's own pack is
// the whole advance.
func (s *Store) advanceActive(
	ctx context.Context,
	expectedParent string,
	snapshot core.Snapshot,
	chain *gitstore.TaskOperationsResult,
) (bool, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, s.databaseError("begin conditional projection advancement", err)
	}
	defer transaction.Rollback()

	var actualHead, actualHistoryGeneration string
	err = transaction.QueryRowContext(
		ctx,
		`SELECT head, history_generation FROM tasks WHERE task_id = ?`,
		snapshot.State.TaskID,
	).Scan(&actualHead, &actualHistoryGeneration)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		actualHead = ""
		actualHistoryGeneration = ""
	case err != nil:
		return false, s.databaseError("read conditional projected task head", err)
	}
	if actualHead == snapshot.Head {
		if err := transaction.Commit(); err != nil {
			return false, s.databaseError("commit conditional projection advancement", err)
		}
		return true, nil
	}
	if actualHead != expectedParent {
		if err := transaction.Commit(); err != nil {
			return false, s.databaseError("commit conditional projection advancement", err)
		}
		return false, nil
	}
	if actualHead != "" && actualHistoryGeneration != snapshot.State.History.Generation {
		if err := transaction.Commit(); err != nil {
			return false, s.databaseError("commit conditional projection advancement", err)
		}
		return false, nil
	}
	if actualHead != "" {
		if err := deleteTask(ctx, transaction, snapshot.State.TaskID); err != nil {
			return false, err
		}
	}
	if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
		return false, err
	}
	if err := s.advanceOperations(ctx, transaction, expectedParent, snapshot, chain); err != nil {
		return false, err
	}
	if err := transaction.Commit(); err != nil {
		return false, s.databaseError("commit conditional projection advancement", err)
	}
	return true, nil
}

func (s *Store) advanceOperations(
	ctx context.Context,
	transaction *sql.Tx,
	expectedParent string,
	snapshot core.Snapshot,
	chain *gitstore.TaskOperationsResult,
) error {
	if chain != nil {
		return projectChain(ctx, transaction, *chain, expectedParent)
	}
	return appendOperations(ctx, transaction, snapshot.State.TaskID, expectedParent, []gitstore.OperationCommit{{
		ObjectID:  snapshot.Head,
		Parent:    expectedParent,
		Operation: snapshot.Operation,
	}})
}

func (s *Store) invalidateActive(ctx context.Context, taskID, expectedParent, writtenHead string) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.databaseError("begin conditional projection invalidation", err)
	}
	defer transaction.Rollback()

	for _, table := range []string{"task_labels", "task_dependencies", "operations"} {
		if _, err := transaction.ExecContext(
			ctx,
			`DELETE FROM `+table+` WHERE task_id = ? AND EXISTS (
				SELECT 1 FROM tasks WHERE task_id = ? AND head IN (?, ?)
			)`,
			taskID,
			taskID,
			expectedParent,
			writtenHead,
		); err != nil {
			return s.databaseError("invalidate projected task collection", err)
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		`DELETE FROM tasks WHERE task_id = ? AND head IN (?, ?)`,
		taskID,
		expectedParent,
		writtenHead,
	); err != nil {
		return s.databaseError("invalidate projected task", err)
	}
	if err := transaction.Commit(); err != nil {
		return s.databaseError("commit conditional projection invalidation", err)
	}
	return nil
}

func replaceSnapshots(
	ctx context.Context,
	db *sql.DB,
	snapshots []core.Snapshot,
	chains []gitstore.TaskOperationsResult,
) error {
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cacheError("begin projection rebuild", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(
		ctx,
		`DELETE FROM task_labels; DELETE FROM task_dependencies; DELETE FROM operations; DELETE FROM tasks`,
	); err != nil {
		return cacheError("clear projection rebuild", err)
	}
	for _, snapshot := range snapshots {
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
	}
	// A rebuild reprojects full histories, so every chain starts at its root.
	for _, chain := range chains {
		if err := insertOperations(ctx, transaction, chain.TaskID, 0, chain.Commits); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return cacheError("commit projection rebuild", err)
	}
	return nil
}

func (s *Store) applyChanges(
	ctx context.Context,
	expectedHeads map[string]string,
	snapshots []core.Snapshot,
	chains []gitstore.TaskOperationsResult,
) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.databaseError("begin projection refresh", err)
	}
	defer transaction.Rollback()
	for _, snapshot := range snapshots {
		matches, err := cachedHeadMatches(ctx, transaction, snapshot.State.TaskID, expectedHeads[snapshot.State.TaskID])
		if err != nil {
			return err
		}
		if !matches {
			return errStaleProjectionRefresh
		}
	}
	byTaskID := make(map[string]gitstore.TaskOperationsResult, len(chains))
	for _, chain := range chains {
		byTaskID[chain.TaskID] = chain
	}
	for _, snapshot := range snapshots {
		taskID := snapshot.State.TaskID
		if err := deleteTask(ctx, transaction, taskID); err != nil {
			return err
		}
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
		chain, found := byTaskID[taskID]
		if !found {
			if err := deleteOperations(ctx, transaction, taskID); err != nil {
				return err
			}
			continue
		}
		if err := projectChain(ctx, transaction, chain, expectedHeads[taskID]); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return s.databaseError("commit projection refresh", err)
	}
	return nil
}

func cachedHeadMatches(ctx context.Context, transaction *sql.Tx, taskID, expectedHead string) (bool, error) {
	var actualHead string
	err := transaction.QueryRowContext(ctx, `SELECT head FROM tasks WHERE task_id = ?`, taskID).Scan(&actualHead)
	if errors.Is(err, sql.ErrNoRows) {
		return expectedHead == "", nil
	}
	if err != nil {
		return false, cacheError("read projected task head during refresh", err)
	}
	return actualHead == expectedHead, nil
}

func deleteTask(ctx context.Context, transaction *sql.Tx, taskID string) error {
	for _, table := range []string{"task_labels", "task_dependencies", "tasks"} {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM `+table+` WHERE task_id = ?`, taskID); err != nil {
			return cacheError("remove projected task", err)
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
		return cacheError("insert projected task", err)
	}
	labels := append([]string(nil), state.Task.Labels...)
	dependencies := append([]string(nil), state.Task.Dependencies...)
	sort.Strings(labels)
	sort.Strings(dependencies)
	for _, label := range labels {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO task_labels (task_id, label) VALUES (?, ?)`, state.TaskID, label); err != nil {
			return cacheError("insert projected task label", err)
		}
	}
	for _, dependency := range dependencies {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO task_dependencies (task_id, dependency_id) VALUES (?, ?)`, state.TaskID, dependency); err != nil {
			return cacheError("insert projected task dependency", err)
		}
	}
	return nil
}

func (s *Store) querySnapshots(ctx context.Context) ([]core.Snapshot, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, s.databaseError("begin projected task query", err)
	}
	defer transaction.Rollback()

	rows, err := transaction.QueryContext(ctx, `
		SELECT task_id, head, project_id, history_generation, logical_clock, title, description, status, priority, rank, created_at, updated_at, deleted
		FROM tasks ORDER BY task_id`)
	if err != nil {
		return nil, s.databaseError("query projected tasks", err)
	}
	var snapshots []core.Snapshot
	byTaskID := make(map[string]int)
	for rows.Next() {
		snapshot, err := scanSnapshotScalars(rows)
		if err != nil {
			_ = rows.Close()
			if core.CategoryOf(err) == core.CategoryCorruptData {
				return nil, err
			}
			return nil, s.databaseError("read projected task", err)
		}
		if _, found := byTaskID[snapshot.State.TaskID]; found {
			_ = rows.Close()
			return nil, core.Errorf(core.CategoryCorruptData, "projected task %q appears more than once", snapshot.State.TaskID)
		}
		byTaskID[snapshot.State.TaskID] = len(snapshots)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, s.databaseError("read projected tasks", err)
	}
	if err := rows.Close(); err != nil {
		return nil, s.databaseError("close projected task query", err)
	}

	labelRows, err := transaction.QueryContext(ctx, `SELECT task_id, label FROM task_labels ORDER BY task_id, label`)
	if err != nil {
		return nil, s.databaseError("query projected task labels", err)
	}
	for labelRows.Next() {
		var taskID, label string
		if err := labelRows.Scan(&taskID, &label); err != nil {
			_ = labelRows.Close()
			return nil, s.databaseError("read projected task label", err)
		}
		index, found := byTaskID[taskID]
		if !found {
			_ = labelRows.Close()
			return nil, core.Errorf(core.CategoryCorruptData, "projected label references missing task %q", taskID)
		}
		snapshots[index].State.Task.Labels = append(snapshots[index].State.Task.Labels, label)
	}
	if err := labelRows.Err(); err != nil {
		_ = labelRows.Close()
		return nil, s.databaseError("read projected task labels", err)
	}
	if err := labelRows.Close(); err != nil {
		return nil, s.databaseError("close projected task label query", err)
	}

	dependencyRows, err := transaction.QueryContext(ctx, `SELECT task_id, dependency_id FROM task_dependencies ORDER BY task_id, dependency_id`)
	if err != nil {
		return nil, s.databaseError("query projected task dependencies", err)
	}
	for dependencyRows.Next() {
		var taskID, dependencyID string
		if err := dependencyRows.Scan(&taskID, &dependencyID); err != nil {
			_ = dependencyRows.Close()
			return nil, s.databaseError("read projected task dependency", err)
		}
		index, found := byTaskID[taskID]
		if !found {
			_ = dependencyRows.Close()
			return nil, core.Errorf(core.CategoryCorruptData, "projected dependency references missing task %q", taskID)
		}
		snapshots[index].State.Task.Dependencies = append(snapshots[index].State.Task.Dependencies, dependencyID)
	}
	if err := dependencyRows.Err(); err != nil {
		_ = dependencyRows.Close()
		return nil, s.databaseError("read projected task dependencies", err)
	}
	if err := dependencyRows.Close(); err != nil {
		return nil, s.databaseError("close projected task dependency query", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, s.databaseError("commit projected task query", err)
	}
	return snapshots, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanSnapshotScalars(scanner rowScanner) (core.Snapshot, error) {
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
	if err := scanner.Scan(
		&state.TaskID, &snapshot.Head, &state.ProjectID, &state.History.Generation, &clock, &state.Task.Title, &state.Task.Description,
		&status, &priority, &state.Task.Rank, &created, &updated, &deleted,
	); err != nil {
		return core.Snapshot{}, err
	}
	if clock < 0 || (deleted != 0 && deleted != 1) {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "projected task %q has invalid scalar values", state.TaskID)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "projected task has invalid creation time", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "projected task has invalid update time", err)
	}
	state.Format = "workbook.task-state"
	state.Version = 1
	state.LogicalClock = uint64(clock)
	state.Task.Status = core.Status(status)
	state.Task.Priority = core.Priority(priority)
	state.Task.Labels = []string{}
	state.Task.Dependencies = []string{}
	state.Task.CreatedAt = createdAt
	state.Task.UpdatedAt = updatedAt
	state.Task.Deleted = deleted == 1
	snapshot.State = state
	return snapshot, nil
}

func (s *Store) querySnapshot(ctx context.Context, taskID string) (core.Snapshot, bool, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.Snapshot{}, false, s.databaseError("begin projected task query", err)
	}
	defer transaction.Rollback()

	snapshot, err := scanSnapshotScalars(transaction.QueryRowContext(ctx, `
			SELECT task_id, head, project_id, history_generation, logical_clock, title, description, status, priority, rank, created_at, updated_at, deleted
			FROM tasks WHERE task_id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := transaction.Commit(); err != nil {
			return core.Snapshot{}, false, s.databaseError("commit projected task query", err)
		}
		return core.Snapshot{}, false, nil
	}
	if err != nil {
		if core.CategoryOf(err) == core.CategoryCorruptData {
			return core.Snapshot{}, false, err
		}
		return core.Snapshot{}, false, s.databaseError("query projected task", err)
	}
	labels, err := s.queryStrings(ctx, transaction, `SELECT label FROM task_labels WHERE task_id = ? ORDER BY label`, taskID)
	if err != nil {
		return core.Snapshot{}, false, err
	}
	dependencies, err := s.queryStrings(ctx, transaction, `SELECT dependency_id FROM task_dependencies WHERE task_id = ? ORDER BY dependency_id`, taskID)
	if err != nil {
		return core.Snapshot{}, false, err
	}
	snapshot.State.Task.Labels = labels
	snapshot.State.Task.Dependencies = dependencies
	if err := transaction.Commit(); err != nil {
		return core.Snapshot{}, false, s.databaseError("commit projected task query", err)
	}
	return snapshot, true, nil
}

func (s *Store) queryStrings(ctx context.Context, transaction *sql.Tx, query, taskID string) ([]string, error) {
	rows, err := transaction.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, s.databaseError("query projected task collection", err)
	}
	defer rows.Close()
	values := []string{}
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
		return "", cacheError("create temporary projection cache", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", cacheError("close temporary projection cache", err)
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
		return "", cacheError("create temporary projection schema", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, schemaVersion, s.config.ProjectID); err != nil {
		_ = os.Remove(temporaryPath)
		return "", cacheError("initialize temporary projection metadata", err)
	}
	var snapshots []core.Snapshot
	var chains []gitstore.TaskOperationsResult
	if len(heads) > 0 {
		snapshots, err = s.source.ReadTaskHeads(ctx, s.config, heads)
		if err != nil {
			_ = os.Remove(temporaryPath)
			return "", err
		}
		if len(snapshots) != len(heads) {
			_ = os.Remove(temporaryPath)
			return "", core.Errorf(core.CategoryCorruptData, "projection rebuild batch returned %d snapshots, want %d", len(snapshots), len(heads))
		}
		for i, snapshot := range snapshots {
			if err := validateReadSnapshot(heads[i], snapshot); err != nil {
				_ = os.Remove(temporaryPath)
				return "", err
			}
		}
		chains, err = s.readOperationChains(ctx, heads, nil)
		if err != nil {
			_ = os.Remove(temporaryPath)
			return "", err
		}
	}
	if err := replaceSnapshots(ctx, db, snapshots, chains); err != nil {
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
			return cacheError("replace projection cache and reopen previous cache", errors.Join(err, reopenErr))
		}
		return cacheError("replace projection cache", err)
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
	dsn := &url.URL{Scheme: "file", Path: path}
	query := dsn.Query()
	query.Set("_pragma", "busy_timeout(5000)")
	query.Set("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, cacheError("open projection cache", err)
	}
	return db, nil
}

func (s *Store) databaseError(action string, err error) error {
	return cacheError(action, err)
}

func cacheError(action string, err error) error {
	return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot %s; %s", action, cacheRecoveryHint), err)
}

func formatTime(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}

func parseProjectedTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, core.Wrap(core.CategoryCorruptData, "projected operation has an invalid wall time", err)
	}
	return parsed, nil
}
