// Package projection provides a disposable SQLite read cache for Workbook task refs.
package projection

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	cacheFilename     = "cache.sqlite"
	cacheRecoveryHint = "run `workbook rebuild` to recreate the SQLite cache"
)

type taskHeadSource interface {
	ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
	InspectTaskHead(context.Context, core.ProjectConfig, string) (gitstore.TaskHead, bool, error)
	ReadTaskHeads(context.Context, core.ProjectConfig, []gitstore.TaskHead) ([]core.Snapshot, error)
	ValidateTaskHeadAdvancesWithCompactions(context.Context, core.ProjectConfig, []gitstore.HeadAdvance) (map[string]struct{}, error)
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

func (s repositorySource) ValidateTaskHeadAdvancesWithCompactions(
	ctx context.Context,
	config core.ProjectConfig,
	advances []gitstore.HeadAdvance,
) (map[string]struct{}, error) {
	return s.repository.ValidateTaskHeadAdvancesWithCompactions(ctx, config, advances)
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
	// dbInfo identifies the file db is open on. Another process replacing the
	// cache renames a new file over cachePath, which leaves this handle bound
	// to the unlinked inode; SQLite then reports every later write as
	// "attempt to write a readonly database". Comparing identities is what
	// turns that into a reopen. It is nil until the cache file exists, because
	// sql.Open is lazy and creates it on first use.
	dbInfo os.FileInfo
	// closed records that Close has released db, so a later read or write
	// reports a closed cache instead of taking the recovery path a missing
	// database would take and quietly rebuilding one this handle no longer
	// owns. It is guarded by rebuildMu, like db and dbInfo.
	closed bool
	rename func(string, string) error
	// afterRecovery runs in lockActiveDatabase each time a recovery round
	// releases the exclusive lock, before the round checks the cache again.
	// It is a test seam standing for another process replacing the cache in
	// that window, and nil otherwise.
	afterRecovery func()
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
	store := &Store{source: source, config: config, cachePath: cachePath, db: db, rename: os.Rename}
	store.dbInfo, _ = store.cacheStat()
	return store, nil
}

// CachePath returns the disposable cache location shared by repository worktrees.
func (s *Store) CachePath() string {
	return s.cachePath
}

// Close releases the SQLite handle this store holds.
//
// It exists because database/sql starts a connectionOpener goroutine per
// *sql.DB and only db.Close stops it, so a process that opens a store per
// command and walks away — `workbook serve`, the sync watcher, this module's
// own test binaries — accumulates one goroutine per store for as long as it
// runs. Closing twice is harmless, and a closed store refuses further reads
// and writes with an error rather than panicking or rebuilding a cache it no
// longer holds open.
func (s *Store) Close() error {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	s.closed = true
	db := s.db
	s.db = nil
	s.dbInfo = nil
	if db == nil {
		return nil
	}
	if err := db.Close(); err != nil {
		return s.databaseError("close projection cache", err)
	}
	return nil
}

// Refresh validates task-ref tips, applying only changed tip checkpoints to
// the cache. Git remains the canonical source for every changed task.
func (s *Store) Refresh(ctx context.Context) error {
	return s.withActiveDatabase(ctx, func(ctx context.Context) error {
		_, err := s.refreshActive(ctx)
		return err
	})
}

// refreshActive brings the cache up to the task refs as they stand once it
// has listed them, in a single pass that another request or process updating
// the same cache at the same moment cannot fail.
//
// Each changed task is written with a compare-and-swap against the row this
// pass read before it listed the refs. A row somebody else moved in the
// meantime is theirs: it is left as they wrote it, and the checkpoint this
// pass read from Git for that task is returned instead, keyed by task ID, for
// the caller to answer from. That checkpoint is the task's ref as it stood
// after the caller's request began, which is all a read promises, and the
// next refresh compares the row with the ref again whichever of the two was
// newer. Nothing here is retried, so there is no bound to reach: the pass
// costs one cache read, one ref listing, and one read of the tasks that
// changed, however busy the cache is.
//
// It used to treat any moved row as a failure of the whole pass and give up
// after two, which under a board's ordinary load — every create projecting
// its own task while every concurrent read projected the same one — answered
// 500 and advised a rebuild that would have changed nothing.
func (s *Store) refreshActive(ctx context.Context) (map[string]core.Snapshot, error) {
	cached, err := s.cachedTaskHeads(ctx)
	if err != nil {
		return nil, err
	}
	heads, err := s.source.ListTaskHeads(ctx, s.config)
	if err != nil {
		return nil, err
	}
	return s.refreshChangedHeads(ctx, cached, heads)
}

// Rebuild recreates the projection from all currently visible Git task heads.
// It is intentionally a cache-only operation: no Git refs or objects change.
func (s *Store) Rebuild(ctx context.Context) (int, error) {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	if s.closed {
		return 0, errStoreClosed()
	}
	return s.rebuildLocked(ctx)
}

// rebuildLocked builds a cache from the task refs and installs it, with
// rebuildMu held exclusively.
//
// A build whose refs moved before it finished is built once more, so the cache
// it installs is usually exactly the refs. If they moved through the second
// build too, that build is installed anyway. It is a consistent projection of
// every ref as it was listed, which is what any cache is once somebody writes
// a task, and every read and write brings the cache up to the refs before it
// answers. Refusing instead would fail reads for as long as other processes
// keep writing, which is exactly when a fresh clone's first commands all
// rebuild at once. Two builds is the whole bound.
//
// Before it renames its build into place, a rebuild that finds another
// process has already installed a usable cache opens that one and discards
// its own. Renaming over it would pull a valid cache out from under every
// process that just opened it, each of which would then rebuild or redo in
// turn.
func (s *Store) rebuildLocked(ctx context.Context) (int, error) {
	const attempts = 2
	for attempt := 1; ; attempt++ {
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
		if !equalHeads(heads, current) && attempt < attempts {
			_ = os.Remove(temporary)
			continue
		}
		adopted, err := s.adoptReplacementLocked(ctx)
		if err != nil {
			_ = os.Remove(temporary)
			return 0, err
		}
		if adopted {
			_ = os.Remove(temporary)
			return len(heads), nil
		}
		if err := s.replaceAtomically(ctx, temporary); err != nil {
			_ = os.Remove(temporary)
			return 0, err
		}
		return len(heads), nil
	}
}

// adoptReplacementLocked opens the cache another process installed at the
// cache path since this store last opened it, and reports whether that cache
// is usable. A file this store is still open on, or none at all, is not a
// replacement; neither is one that is not a usable projection, which the
// caller's own build then replaces.
func (s *Store) adoptReplacementLocked(ctx context.Context) (bool, error) {
	info, ok := s.cacheStat()
	if !ok || s.boundTo(info) {
		return false, nil
	}
	if err := s.reopenDatabase(ctx); err != nil {
		return false, err
	}
	return s.cacheUsable(ctx), nil
}

// List returns task checkpoints from SQLite after refreshing against Git ref tips.
func (s *Store) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
	if err := s.validateConfig(config); err != nil {
		return nil, err
	}
	var snapshots []core.Snapshot
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		superseded, err := s.refreshActive(ctx)
		if err != nil {
			return err
		}
		listed, err := s.querySnapshots(ctx)
		if err != nil {
			return err
		}
		snapshots = answerFromGit(listed, superseded)
		return nil
	})
	return snapshots, err
}

// answerFromGit replaces each listed task another update moved during the
// refresh with the checkpoint the refresh read from Git for it, and adds any
// such task the cache holds no row for at all, keeping the listing's task-ID
// order. See refreshActive for why the Git checkpoint is the answer.
func answerFromGit(listed []core.Snapshot, superseded map[string]core.Snapshot) []core.Snapshot {
	if len(superseded) == 0 {
		return listed
	}
	answered := make(map[string]bool, len(superseded))
	for index := range listed {
		if snapshot, found := superseded[listed[index].State.TaskID]; found {
			listed[index] = snapshot
			answered[snapshot.State.TaskID] = true
		}
	}
	if len(answered) == len(superseded) {
		return listed
	}
	for taskID, snapshot := range superseded {
		if !answered[taskID] {
			listed = append(listed, snapshot)
		}
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].State.TaskID < listed[j].State.TaskID })
	return listed
}

// Get returns one SQLite-projected checkpoint after inspecting its exact Git ref.
func (s *Store) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	if err := s.validateConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	// Shape, not ownership. Whoever handed this ID over decided ownership —
	// the service's key set for an ID somebody typed, a ref listing for every
	// row this cache holds — and the exact-ref inspection below is what settles
	// that the task exists at all. What is worth refusing here is a name no
	// row could ever carry.
	if err := core.ValidateTaskIDShape(taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	var snapshot core.Snapshot
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		exact, err := s.getExactActive(ctx, taskID)
		if err != nil {
			return err
		}
		snapshot = exact
		return nil
	})
	return snapshot, err
}

// TaskHistory returns one task's recorded operation packs, oldest first along
// the parent chain, after refreshing that task from its exact Git ref.
func (s *Store) TaskHistory(ctx context.Context, config core.ProjectConfig, taskID string) (core.TaskHistory, error) {
	if err := s.validateConfig(config); err != nil {
		return core.TaskHistory{}, err
	}
	if err := core.ValidateTaskIDShape(taskID); err != nil {
		return core.TaskHistory{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	var history core.TaskHistory
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		snapshot, err := s.getExactActive(ctx, taskID)
		if err != nil {
			return err
		}
		// Through the head just read rather than through whatever the row now
		// names: getExactActive may have answered from Git because another
		// update moved the row, and the history has to end where the task it
		// describes does.
		projected, found, err := s.readProjectedHistory(ctx, taskID, snapshot.Head)
		if err != nil {
			return err
		}
		if !found {
			projected, err = s.gitHistory(ctx, taskID, snapshot.Head)
			if err != nil {
				return err
			}
		}
		history = projected
		return nil
	})
	return history, err
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
	if err := core.ValidateTaskIDShape(taskID); err != nil {
		return core.TaskHistory{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	var history core.TaskHistory
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		if _, err := s.getExactActive(ctx, taskID); err != nil {
			return err
		}
		projected, found, err := s.readProjectedHistory(ctx, taskID, commit)
		if err != nil {
			return err
		}
		if !found {
			projected, err = s.gitHistory(ctx, taskID, commit)
			if err != nil {
				return err
			}
		}
		history = projected
		return nil
	})
	return history, err
}

// Resolve returns a canonical full task ID for an unambiguous case-insensitive prefix.
func (s *Store) Resolve(ctx context.Context, config core.ProjectConfig, prefix string) (string, error) {
	if err := s.validateConfig(config); err != nil {
		return "", err
	}
	if strings.TrimSpace(prefix) == "" {
		return "", core.Errorf(core.CategoryValidation, "task ID prefix must not be blank")
	}
	var matches []string
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		superseded, err := s.refreshActive(ctx)
		if err != nil {
			return err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM tasks ORDER BY task_id`)
		if err != nil {
			return s.databaseError("query projected task IDs", err)
		}
		defer rows.Close()

		needle := strings.ToLower(prefix)
		matches = nil
		seen := make(map[string]bool)
		for rows.Next() {
			var taskID string
			if err := rows.Scan(&taskID); err != nil {
				return s.databaseError("read projected task ID", err)
			}
			seen[taskID] = true
			if strings.HasPrefix(strings.ToLower(taskID), needle) {
				matches = append(matches, taskID)
			}
		}
		if err := rows.Err(); err != nil {
			return s.databaseError("read projected task IDs", err)
		}
		// A task whose row another update removed while the refresh ran is
		// still a task: its ref was just listed.
		for taskID := range superseded {
			if !seen[taskID] && strings.HasPrefix(strings.ToLower(taskID), needle) {
				matches = append(matches, taskID)
			}
		}
		sort.Strings(matches)
		return nil
	})
	if err != nil {
		return "", err
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

// Advance conditionally replaces one projected task checkpoint: the row moves
// to snapshot only while it still names expectedParent, the head the write
// was made on.
//
// It reports false only when the row still names expectedParent but cannot
// take snapshot, which is a history-generation change nobody proved, and the
// caller's invalidation is what clears it. A row that already names snapshot,
// or names a head another update recorded there — newer than this write, or
// older because that update had not reached it yet — reports true and is left
// as it is. It is not this write's to replace, and nothing about it is stale
// on this write's account: every read compares a row with its task's ref
// before it uses it. Reporting false there used to put a warning to run
// `workbook rebuild` on a write that had succeeded, whenever two writes to
// one task landed close together.
func (s *Store) Advance(ctx context.Context, config core.ProjectConfig, expectedParent string, snapshot core.Snapshot) (bool, error) {
	outcome, err := s.advance(ctx, config, expectedParent, snapshot)
	return outcome != advanceRefused, err
}

// Land is Advance for a caller that needs the row on snapshot itself, not
// only not stale on its own account. It moves the row exactly as Advance does
// and leaves a row another update moved as that update wrote it, but it
// reports true only when the row names snapshot afterwards.
//
// Compaction is that caller. A row left on any other head is behind a
// checkpoint whose replaced history Git is about to prune, and once it does,
// no refresh can prove that row's move to the checkpoint is a compaction.
func (s *Store) Land(ctx context.Context, config core.ProjectConfig, expectedParent string, snapshot core.Snapshot) (bool, error) {
	outcome, err := s.advance(ctx, config, expectedParent, snapshot)
	return outcome == advanceApplied, err
}

func (s *Store) advance(ctx context.Context, config core.ProjectConfig, expectedParent string, snapshot core.Snapshot) (advanceOutcome, error) {
	if err := s.validateConfig(config); err != nil {
		return advanceRefused, err
	}
	outcome := advanceRefused
	err := s.withActiveDatabase(ctx, func(ctx context.Context) error {
		var err error
		outcome, err = s.advanceActive(ctx, expectedParent, snapshot, nil, false)
		return err
	})
	if err != nil {
		return advanceRefused, err
	}
	return outcome, nil
}

// Invalidate removes one projected task only when it still names an expected head.
func (s *Store) Invalidate(ctx context.Context, config core.ProjectConfig, taskID, expectedParent, writtenHead string) error {
	if err := s.validateConfig(config); err != nil {
		return err
	}
	return s.withActiveDatabase(ctx, func(ctx context.Context) error {
		return s.invalidateActive(ctx, taskID, expectedParent, writtenHead)
	})
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
	if s.closed {
		s.rebuildMu.RUnlock()
		return errStoreClosed()
	}
	if s.cacheUsable(ctx) {
		return nil
	}
	s.rebuildMu.RUnlock()

	// A cache recovered a moment ago and unusable again already is another
	// process replacing it in between, which happens while several processes
	// that each found no cache rebuild at once. The next round reopens that
	// replacement rather than building another, because a rebuild adopts a
	// usable cache it finds in place, so the rounds settle as soon as the
	// rebuilds stop landing. Three rounds bound a machine where they do not.
	const rounds = 3
	for round := 1; ; round++ {
		s.rebuildMu.Lock()
		// Rechecked under the exclusive lock: a Close that landed in the
		// window above must not be followed by a rebuild, which would reopen
		// the cache behind the caller that closed it.
		if s.closed {
			s.rebuildMu.Unlock()
			return errStoreClosed()
		}
		if !s.cacheUsable(ctx) {
			if err := s.recoverLocked(ctx); err != nil {
				s.rebuildMu.Unlock()
				return err
			}
		}
		s.rebuildMu.Unlock()
		if s.afterRecovery != nil {
			s.afterRecovery()
		}

		s.rebuildMu.RLock()
		if s.closed {
			s.rebuildMu.RUnlock()
			return errStoreClosed()
		}
		if s.cacheUsable(ctx) {
			return nil
		}
		s.rebuildMu.RUnlock()
		if round == rounds {
			// What this request would read is in Git either way, so it is
			// sent again.
			return errCacheBusy("the projection cache changed again while it was being activated")
		}
	}
}

// errStoreClosed names the refusal a closed store answers with. It is built
// per call rather than kept in a package variable because core errors carry a
// category and a message a caller may wrap further.
func errStoreClosed() error {
	return core.Errorf(core.CategoryOperational, "projection cache is closed")
}

// recoverLocked restores a usable cache with rebuildMu held exclusively.
//
// A cache another process replaced is a complete, valid projection that this
// handle simply is not open on, so it is reopened rather than rebuilt.
// Rebuilding it would discard the work that process just did and rename yet
// another file into place, which is the failure the caller was already in.
// Anything else — a deleted, malformed, or foreign cache — still rebuilds.
func (s *Store) recoverLocked(ctx context.Context) error {
	if info, ok := s.cacheStat(); ok && !s.boundTo(info) {
		if err := s.reopenDatabase(ctx); err != nil {
			return err
		}
		if s.cacheUsable(ctx) {
			return nil
		}
	}
	_, err := s.rebuildLocked(ctx)
	return err
}

// withActiveDatabase runs body with rebuildMu read-held and the active
// database validated, redoing body when SQLite reports the cache file was
// replaced underneath the open handle. lockActiveDatabase stats the path
// before body runs, so a rebuild in another process can still rename a fresh
// cache into place mid-write, and the held handle then fails with
// SQLITE_READONLY_DBMOVED. The replacement is a complete, valid projection
// and the refused write never committed anywhere, so the recovery is to
// reopen the replacement and redo body rather than surface an operational
// error for a race the tool can absorb.
func (s *Store) withActiveDatabase(ctx context.Context, body func(context.Context) error) error {
	// Three attempts bound the pathological case of a replacement landing
	// inside every redo's window; one redo settles the race in practice.
	const attempts = 3
	for attempt := 1; ; attempt++ {
		detached := false
		err := func() error {
			if err := s.lockActiveDatabase(ctx); err != nil {
				return err
			}
			// Deferred so a panicking body cannot leave rebuildMu read-held:
			// workbook serve runs one shared store behind net/http, which
			// recovers handler panics, and a leaked read lock would wedge
			// the next Rebuild and every request queued behind it.
			defer s.rebuildMu.RUnlock()
			err := body(ctx)
			detached = err != nil && databaseDetached(err)
			return err
		}()
		if err == nil || !detached {
			return err
		}
		if attempt == attempts {
			// Another process replaced the cache inside every redo's window.
			// The replacements are valid caches and this request wrote
			// nothing to any of them, so the answer is to send it again, not
			// to rebuild a cache that is fine.
			return core.Wrap(core.CategoryStaleWrite,
				"the projection cache was replaced by another process each time this request used it; nothing was changed, try again", err)
		}
		if recoverErr := func() error {
			s.rebuildMu.Lock()
			defer s.rebuildMu.Unlock()
			// Rechecked here as in lockActiveDatabase: a Close that landed
			// between the refused write and this retry must not be followed by
			// a reopen, which would hand this store a fresh *sql.DB nobody
			// holds a closer for.
			if s.closed {
				return errStoreClosed()
			}
			return s.reopenReplacedLocked(ctx)
		}(); recoverErr != nil {
			return recoverErr
		}
	}
}

// reopenReplacedLocked trusts SQLite's moved verdict over the recorded stat.
// The stat itself can describe the replacement — the rename can win the race
// against openStore's bookkeeping — so unlike recoverLocked there is no
// boundTo gate: the handle is reopened unconditionally, and a replacement
// that is not a usable projection is rebuilt.
func (s *Store) reopenReplacedLocked(ctx context.Context) error {
	if err := s.reopenDatabase(ctx); err != nil {
		return err
	}
	if s.cacheUsable(ctx) {
		return nil
	}
	_, err := s.rebuildLocked(ctx)
	return err
}

// databaseDetached reports the SQLite verdicts that mean this handle is no
// longer operating on the live cache file, which is how another process's
// atomic cache replacement reaches the holder of the original.
func databaseDetached(err error) bool {
	var sqliteError *sqlite.Error
	if !errors.As(err, &sqliteError) {
		return false
	}
	switch sqliteError.Code() {
	case sqlite3.SQLITE_READONLY_DBMOVED:
		// SQLite's own file-identity check: the database under this
		// connection was renamed or unlinked after it was opened. The
		// recorded stat can itself have lost the race that produces this,
		// so the verdict needs no confirmation.
		return true
	case sqlite3.SQLITE_IOERR_DELETE_NOENT:
		// A rollback journal vanished between stat and unlink. Database
		// locks live on inodes while the journal path is shared, so once
		// the cache file is replaced, handles on the old and the new file
		// race each other over one journal name — and either side can be
		// the one that finds it gone. An unlink that finds no file is
		// another process having removed it, not a failing disk; a disk
		// fault reports SQLITE_IOERR_DELETE instead.
		return true
	}
	return false
}

func (s *Store) cacheUsable(ctx context.Context) bool {
	info, ok := s.cacheStat()
	return ok && s.boundTo(info) && s.metaMatches(ctx)
}

func (s *Store) cacheStat() (os.FileInfo, bool) {
	info, err := os.Stat(s.cachePath)
	if err != nil {
		return nil, false
	}
	return info, true
}

// boundTo reports whether db is open on the file now at cachePath.
func (s *Store) boundTo(info os.FileInfo) bool {
	return s.dbInfo != nil && os.SameFile(s.dbInfo, info)
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
		`SELECT task_id, head, project_id, history_generation, compacted_from, logical_clock, min_reader, title, description, status, priority, rank, created_at, updated_at, deleted, thread FROM tasks LIMIT 0`,
		`SELECT task_id, label FROM task_labels LIMIT 0`,
		`SELECT task_id, dependency_id FROM task_dependencies LIMIT 0`,
		`SELECT task_id, principal, label, creator, created_at FROM task_assignments LIMIT 0`,
		`SELECT operation_id, task_id, commit_id, chain_index, pack_index, logical_clock, history_generation, min_reader, actor, wall_time, type, field, value, task_data, payload FROM operations LIMIT 0`,
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

// getExactActive answers one task from its exact ref, projecting the ref's
// checkpoint when the row is behind it.
//
// The projection is a compare-and-swap against the row read first, so a row
// another update moved in between is left to that update, and the read is
// tried once more against the newer row. If that row moves too, the answer is
// the checkpoint the second attempt read from Git, which is the task's ref as
// it stood after this request began. Two attempts is the whole bound, and
// reaching it is an answer rather than a failure: every exit returns either a
// checkpoint or an error that has nothing to do with the race.
func (s *Store) getExactActive(ctx context.Context, taskID string) (core.Snapshot, error) {
	const attempts = 2
	for attempt := 1; ; attempt++ {
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

		var compactions map[string]struct{}
		if cachedFound {
			compactions, err = s.source.ValidateTaskHeadAdvancesWithCompactions(ctx, s.config, []gitstore.HeadAdvance{
				headAdvance(cached, head),
			})
			if err != nil {
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
		// A generation change that gets past this check is a proven compaction
		// of the row at cached.Head, which is what advanceActive is told.
		compacted := cachedFound && snapshot.State.History.Generation != cached.State.History.Generation
		if compacted {
			if err := requireCompaction(compactions, taskID); err != nil {
				return core.Snapshot{}, err
			}
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
		outcome, err := s.advanceActive(ctx, expectedParent, snapshot, &chains[0], compacted)
		if err != nil {
			return core.Snapshot{}, err
		}
		if outcome == advanceApplied || attempt == attempts {
			return snapshot, nil
		}
	}
}

func (s *Store) refreshChangedHeads(ctx context.Context, cached map[string]cachedTaskHead, heads []gitstore.TaskHead) (map[string]core.Snapshot, error) {
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
		return nil, core.Errorf(core.CategoryCorruptData, "task %q ref disappeared after it was projected", missing[0])
	}
	if len(changed) == 0 {
		return nil, nil
	}

	var compactions map[string]struct{}
	if len(advances) > 0 {
		var err error
		compactions, err = s.source.ValidateTaskHeadAdvancesWithCompactions(ctx, s.config, advances)
		if err != nil {
			return nil, err
		}
	}
	snapshots, err := s.source.ReadTaskHeads(ctx, s.config, changed)
	if err != nil {
		return nil, err
	}
	if len(snapshots) != len(changed) {
		return nil, core.Errorf(core.CategoryCorruptData, "task head batch returned %d snapshots, want %d", len(snapshots), len(changed))
	}
	for i, snapshot := range snapshots {
		head := changed[i]
		if err := validateReadSnapshot(head, snapshot); err != nil {
			return nil, err
		}
		if previous, found := cached[head.TaskID]; found &&
			snapshot.State.History.Generation != previous.historyGeneration {
			if err := requireCompaction(compactions, head.TaskID); err != nil {
				return nil, err
			}
		}
	}
	// Comparing tips is complete for current state but blind to the
	// intermediate commits an operation table needs: a fetch advancing a task
	// twelve commits yields one new tip and eleven unread packs. Walking each
	// changed task from its projected head to its new one reads exactly those.
	chains, err := s.readOperationChains(ctx, changed, cached)
	if err != nil {
		return nil, err
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
//
// A truncated read projects nothing. A stored chain that stops short of the
// head would look complete to every later reader, whereas dropping the rows
// sends the next history read to Git, which reports the boundary.
func projectChain(ctx context.Context, transaction *sql.Tx, chain gitstore.TaskOperationsResult, previousHead string) error {
	if chain.Truncated != nil {
		return deleteOperations(ctx, transaction, chain.TaskID)
	}
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

// requireCompaction allows a projected task to move to a new history
// generation only when the head-advance check accepted the move as a
// compaction: the new head is rooted at a checkpoint that accounts for every
// operation on the head the projection last saw. compactions is the set that
// check returned, so the proof is made once, in one batch, for every task an
// advance moved. Any other generation change is still reported as a rewrite.
func requireCompaction(compactions map[string]struct{}, taskID string) error {
	if _, proven := compactions[taskID]; !proven {
		return historyGenerationChanged(taskID)
	}
	return nil
}

// advanceOutcome is what a conditional advance found the row naming.
type advanceOutcome int

const (
	// advanceApplied: the row names snapshot now, by this advance or by one
	// that got there first.
	advanceApplied advanceOutcome = iota
	// advanceMoved: the row names a head another update recorded, and was
	// left as that update wrote it.
	advanceMoved
	// advanceRefused: the row still names the expected parent but cannot take
	// snapshot, because its history generation changed without a proven
	// compaction.
	advanceRefused
)

// advanceActive conditionally replaces one projected task. chain carries the
// commits a multi-commit advance crossed; a nil chain means the caller just
// wrote exactly one commit onto expectedParent and the snapshot's own pack is
// the whole advance. compacted reports that the caller proved, against the
// row at expectedParent, that the advance is a compaction, which is the only
// case in which the history generation may change.
func (s *Store) advanceActive(
	ctx context.Context,
	expectedParent string,
	snapshot core.Snapshot,
	chain *gitstore.TaskOperationsResult,
	compacted bool,
) (advanceOutcome, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return advanceRefused, s.databaseError("begin conditional projection advancement", err)
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
		return advanceRefused, s.databaseError("read conditional projected task head", err)
	}
	// None of the three early answers has written anything, so each leaves
	// by the deferred rollback rather than a commit.
	if actualHead == snapshot.Head {
		return advanceApplied, nil
	}
	if actualHead != expectedParent {
		return advanceMoved, nil
	}
	if actualHead != "" && actualHistoryGeneration != snapshot.State.History.Generation && !compacted {
		return advanceRefused, nil
	}
	if actualHead != "" {
		if err := deleteTask(ctx, transaction, snapshot.State.TaskID); err != nil {
			return advanceRefused, err
		}
	}
	if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
		return advanceRefused, err
	}
	if err := s.advanceOperations(ctx, transaction, expectedParent, snapshot, chain); err != nil {
		return advanceRefused, err
	}
	if err := transaction.Commit(); err != nil {
		return advanceRefused, s.databaseError("commit conditional projection advancement", err)
	}
	return advanceApplied, nil
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

	for _, table := range []string{"task_labels", "task_dependencies", "task_assignments", "operations"} {
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
		`DELETE FROM task_labels; DELETE FROM task_dependencies; DELETE FROM task_assignments; DELETE FROM operations; DELETE FROM tasks`,
	); err != nil {
		return cacheError("clear projection rebuild", err)
	}
	for _, snapshot := range snapshots {
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return err
		}
	}
	// A rebuild reprojects full histories, so every chain starts at its root. A
	// truncated one is left out for the same reason a refresh drops it: a
	// partial chain would read as a complete one.
	for _, chain := range chains {
		if chain.Truncated != nil {
			continue
		}
		if err := insertOperations(ctx, transaction, chain.TaskID, 0, chain.Commits); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return cacheError("commit projection rebuild", err)
	}
	return nil
}

// applyChanges writes each changed task whose row still names the head the
// refresh read before listing the refs, in one transaction. It returns the
// checkpoints of the tasks whose rows another update moved in the meantime,
// which it leaves alone; see refreshActive.
func (s *Store) applyChanges(
	ctx context.Context,
	expectedHeads map[string]string,
	snapshots []core.Snapshot,
	chains []gitstore.TaskOperationsResult,
) (map[string]core.Snapshot, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, s.databaseError("begin projection refresh", err)
	}
	defer transaction.Rollback()
	byTaskID := make(map[string]gitstore.TaskOperationsResult, len(chains))
	for _, chain := range chains {
		byTaskID[chain.TaskID] = chain
	}
	var superseded map[string]core.Snapshot
	for _, snapshot := range snapshots {
		taskID := snapshot.State.TaskID
		actualHead, err := projectedHead(ctx, transaction, taskID)
		if err != nil {
			return nil, err
		}
		// Somebody else projected exactly this checkpoint first: a create
		// recording its own task, or a concurrent read of the same refs.
		if actualHead == snapshot.Head {
			continue
		}
		if actualHead != expectedHeads[taskID] {
			if superseded == nil {
				superseded = make(map[string]core.Snapshot)
			}
			superseded[taskID] = snapshot
			continue
		}
		if err := deleteTask(ctx, transaction, taskID); err != nil {
			return nil, err
		}
		if err := upsertSnapshot(ctx, transaction, snapshot); err != nil {
			return nil, err
		}
		chain, found := byTaskID[taskID]
		if !found {
			if err := deleteOperations(ctx, transaction, taskID); err != nil {
				return nil, err
			}
			continue
		}
		if err := projectChain(ctx, transaction, chain, expectedHeads[taskID]); err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, s.databaseError("commit projection refresh", err)
	}
	return superseded, nil
}

// projectedHead returns the head one task's row names, or "" for no row.
func projectedHead(ctx context.Context, transaction *sql.Tx, taskID string) (string, error) {
	var actualHead string
	err := transaction.QueryRowContext(ctx, `SELECT head FROM tasks WHERE task_id = ?`, taskID).Scan(&actualHead)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", cacheError("read projected task head during refresh", err)
	}
	return actualHead, nil
}

func deleteTask(ctx context.Context, transaction *sql.Tx, taskID string) error {
	for _, table := range []string{"task_labels", "task_dependencies", "task_assignments", "tasks"} {
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
	thread, err := encodeThread(state.Task)
	if err != nil {
		return err
	}
	// An empty column is a history no checkpoint started: compactedFrom is
	// always a full object ID when present.
	compactedFrom := ""
	if state.History.CompactedFrom != nil {
		compactedFrom = *state.History.CompactedFrom
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO tasks (task_id, head, project_id, history_generation, compacted_from, logical_clock, min_reader, title, description, status, priority, rank, created_at, updated_at, deleted, thread)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.TaskID, snapshot.Head, state.ProjectID, state.History.Generation, compactedFrom, int64(state.LogicalClock),
		int64(state.MinReader), state.Task.Title, state.Task.Description,
		string(state.Task.Status), string(state.Task.Priority), state.Task.Rank, formatTime(state.Task.CreatedAt), formatTime(state.Task.UpdatedAt), deleted, thread)
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
	// Assignments are stored as their parts rather than as a rendered value,
	// because the creator and the creation time are what the removal rule and
	// the staleness display are computed from, and a projection that dropped
	// them would hand a mutation a parent it could not decide a removal
	// against. They are not sorted on the way in: the checkpoint they come from
	// is already canonically ordered, and every query below orders explicitly.
	for _, assignment := range state.Task.Assignments {
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO task_assignments (task_id, principal, label, creator, created_at) VALUES (?, ?, ?, ?, ?)`,
			state.TaskID, assignment.Principal, assignment.Label, assignment.Creator, formatTime(assignment.CreatedAt),
		); err != nil {
			return cacheError("insert projected task assignment", err)
		}
	}
	return nil
}

// scanAssignment reads one projected assignment row.
func scanAssignment(scanner rowScanner) (string, core.Assignment, error) {
	var (
		taskID     string
		assignment core.Assignment
		created    string
	)
	if err := scanner.Scan(&taskID, &assignment.Principal, &assignment.Label, &assignment.Creator, &created); err != nil {
		return "", core.Assignment{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return "", core.Assignment{}, core.Wrap(core.CategoryCorruptData, "projected task assignment has an invalid creation time", err)
	}
	assignment.CreatedAt = createdAt
	return taskID, assignment, nil
}

func (s *Store) querySnapshots(ctx context.Context) ([]core.Snapshot, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, s.databaseError("begin projected task query", err)
	}
	defer transaction.Rollback()

	rows, err := transaction.QueryContext(ctx, `
		SELECT task_id, head, project_id, history_generation, compacted_from, logical_clock, min_reader, title, description, status, priority, rank, created_at, updated_at, deleted, thread
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

	assignmentRows, err := transaction.QueryContext(ctx,
		`SELECT task_id, principal, label, creator, created_at FROM task_assignments ORDER BY task_id, principal, label`)
	if err != nil {
		return nil, s.databaseError("query projected task assignments", err)
	}
	for assignmentRows.Next() {
		taskID, assignment, err := scanAssignment(assignmentRows)
		if err != nil {
			_ = assignmentRows.Close()
			if core.CategoryOf(err) == core.CategoryCorruptData {
				return nil, err
			}
			return nil, s.databaseError("read projected task assignment", err)
		}
		index, found := byTaskID[taskID]
		if !found {
			_ = assignmentRows.Close()
			return nil, core.Errorf(core.CategoryCorruptData, "projected assignment references missing task %q", taskID)
		}
		snapshots[index].State.Task.Assignments = append(snapshots[index].State.Task.Assignments, assignment)
	}
	if err := assignmentRows.Err(); err != nil {
		_ = assignmentRows.Close()
		return nil, s.databaseError("read projected task assignments", err)
	}
	if err := assignmentRows.Close(); err != nil {
		return nil, s.databaseError("close projected task assignment query", err)
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
		snapshot      core.Snapshot
		state         core.StateDocument
		compactedFrom string
		status        string
		priority      string
		created       string
		updated       string
		deleted       int
		clock         int64
		minReader     int64
		thread        string
	)
	if err := scanner.Scan(
		&state.TaskID, &snapshot.Head, &state.ProjectID, &state.History.Generation, &compactedFrom, &clock, &minReader,
		&state.Task.Title, &state.Task.Description,
		&status, &priority, &state.Task.Rank, &created, &updated, &deleted, &thread,
	); err != nil {
		return core.Snapshot{}, err
	}
	if clock < 0 || minReader < 0 || minReader > math.MaxInt32 || (deleted != 0 && deleted != 1) {
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
	// The watermark is restored rather than recomputed. It is what tells every
	// reader of this snapshot whether the task can be folded at all, and a
	// projection that silently answered zero would let a mutation build on a
	// history this build cannot replay.
	state.MinReader = int(minReader)
	if compactedFrom != "" {
		state.History.CompactedFrom = &compactedFrom
	}
	state.Task.Status = core.Status(status)
	state.Task.Priority = core.Priority(priority)
	state.Task.Labels = []string{}
	state.Task.Dependencies = []string{}
	state.Task.CreatedAt = createdAt
	state.Task.UpdatedAt = updatedAt
	state.Task.Deleted = deleted == 1
	if err := decodeThread(&state.Task, thread); err != nil {
		return core.Snapshot{}, err
	}
	snapshot.State = state
	return snapshot, nil
}

// projectedThread is how a task's comments and attachments are cached.
//
// They are one JSON column on the task row rather than two tables, unlike
// labels and dependencies. The difference is not a shortcut: a label is
// queried — `list --label` filters on it in SQL — while a thread is only ever
// read back whole, with the task it belongs to. The projection stores an
// operation's task payload the same way and for the same reason.
//
// A task with neither is stored as the empty string rather than as `{}`, so
// that the overwhelmingly common row stays the width it was.
type projectedThread struct {
	Comments    []core.Comment    `json:"comments,omitempty"`
	Attachments []core.Attachment `json:"attachments,omitempty"`
}

func encodeThread(task core.TaskData) (string, error) {
	if len(task.Comments) == 0 && len(task.Attachments) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(projectedThread{Comments: task.Comments, Attachments: task.Attachments})
	if err != nil {
		return "", core.Wrap(core.CategoryCorruptData, "cannot project task comments and attachments", err)
	}
	return string(encoded), nil
}

func decodeThread(task *core.TaskData, encoded string) error {
	if encoded == "" {
		return nil
	}
	var thread projectedThread
	if err := json.Unmarshal([]byte(encoded), &thread); err != nil {
		return core.Wrap(core.CategoryCorruptData, "projected task has invalid comments or attachments", err)
	}
	task.Comments = thread.Comments
	task.Attachments = thread.Attachments
	return nil
}

func (s *Store) querySnapshot(ctx context.Context, taskID string) (core.Snapshot, bool, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.Snapshot{}, false, s.databaseError("begin projected task query", err)
	}
	defer transaction.Rollback()

	snapshot, err := scanSnapshotScalars(transaction.QueryRowContext(ctx, `
			SELECT task_id, head, project_id, history_generation, compacted_from, logical_clock, min_reader, title, description, status, priority, rank, created_at, updated_at, deleted, thread
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
	assignments, err := s.queryAssignments(ctx, transaction, taskID)
	if err != nil {
		return core.Snapshot{}, false, err
	}
	snapshot.State.Task.Labels = labels
	snapshot.State.Task.Dependencies = dependencies
	snapshot.State.Task.Assignments = assignments
	if err := transaction.Commit(); err != nil {
		return core.Snapshot{}, false, s.databaseError("commit projected task query", err)
	}
	return snapshot, true, nil
}

// queryAssignments reads one task's assignments, and leaves the slice nil when
// there are none. Nil is the canonical empty assignment list — the member is
// omitted from a stored checkpoint rather than written as `[]` — so a snapshot
// served from this cache has to spell emptiness the same way the checkpoint
// does or every comparison against a folded state would disagree about a task
// nobody assigned.
func (s *Store) queryAssignments(ctx context.Context, transaction *sql.Tx, taskID string) ([]core.Assignment, error) {
	rows, err := transaction.QueryContext(ctx,
		`SELECT task_id, principal, label, creator, created_at FROM task_assignments WHERE task_id = ? ORDER BY principal, label`,
		taskID)
	if err != nil {
		return nil, s.databaseError("query projected task assignments", err)
	}
	defer rows.Close()
	var assignments []core.Assignment
	for rows.Next() {
		_, assignment, err := scanAssignment(rows)
		if err != nil {
			if core.CategoryOf(err) == core.CategoryCorruptData {
				return nil, err
			}
			return nil, s.databaseError("read projected task assignment", err)
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read projected task assignments", err)
	}
	return assignments, nil
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
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	db, err := openDatabase(ctx, s.cachePath)
	if err != nil {
		return err
	}
	s.db = db
	s.dbInfo, _ = s.cacheStat()
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
	dsn := &url.URL{Scheme: "file", Path: sqliteURIPath(path)}
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

// sqliteURIPath spells a filesystem path the way a SQLite file: URI wants it.
// A Windows path has backslashes and starts with a drive, so left as it is the
// URI comes out as file://C:%5CUsers%5C… and SQLite reads the drive as a host.
// SQLite's form is file:///C:/Users/…: forward slashes, and a leading slash
// before the drive. VolumeName is empty on every other platform, so a Unix
// path, relative or absolute, is left exactly as it was.
func sqliteURIPath(path string) string {
	if filepath.VolumeName(path) == "" {
		return path
	}
	return "/" + filepath.ToSlash(path)
}

func (s *Store) databaseError(action string, err error) error {
	return cacheError(action, err)
}

// cacheError reports a failure of the cache itself, which a rebuild repairs,
// with the advice to run one. The exception is SQLite reporting the cache
// locked: that is another connection holding it past the busy timeout, the
// cache is fine, and a rebuild would only queue behind the same lock, so it is
// the retryable answer instead.
func cacheError(action string, err error) error {
	if databaseBusy(err) {
		return core.Wrap(core.CategoryStaleWrite,
			fmt.Sprintf("cannot %s: the projection cache stayed locked by another request or process; nothing was changed, try again", action), err)
	}
	return core.Wrap(core.CategoryOperational, fmt.Sprintf("cannot %s; %s", action, cacheRecoveryHint), err)
}

// errCacheBusy reports a race over the cache that a bounded retry inside this
// package did not outlast. Nothing was written, and the identical request is
// expected to succeed, which is what the stale-write category promises.
func errCacheBusy(what string) error {
	return core.Errorf(core.CategoryStaleWrite, "%s; nothing was changed, try again", what)
}

// databaseBusy reports SQLite's verdicts for a lock another connection held
// past the busy timeout.
func databaseBusy(err error) bool {
	var sqliteError *sqlite.Error
	if !errors.As(err, &sqliteError) {
		return false
	}
	switch sqliteError.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}

func formatTime(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}
