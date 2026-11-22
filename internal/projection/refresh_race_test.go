package projection

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// stormSource is a repository whose task refs other writers keep moving while
// a read of this store is in the middle of using them.
//
// The hooks run inside the source call they are named after, on the reading
// goroutine, and stand for what another request or process did in that
// window. Nothing here depends on timing: every interleaving these tests need
// is forced by where a hook runs, not by how long anything takes.
type stormSource struct {
	emptyTaskOperations
	mu        sync.Mutex
	heads     map[string]string
	snapshots map[string]core.Snapshot
	onList    func()
	onInspect func(taskID string)
	onRead    func(heads []gitstore.TaskHead)
}

func newStormSource() *stormSource {
	return &stormSource{heads: map[string]string{}, snapshots: map[string]core.Snapshot{}}
}

// move points taskID's ref at a new checkpoint, as a write elsewhere would.
func (s *stormSource) move(snapshot core.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heads[snapshot.State.TaskID] = snapshot.Head
	s.snapshots[snapshot.Head] = snapshot
}

func (s *stormSource) head(taskID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heads[taskID]
}

func (s *stormSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	if s.onList != nil {
		s.onList()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	heads := make([]gitstore.TaskHead, 0, len(s.heads))
	for taskID, head := range s.heads {
		heads = append(heads, gitstore.TaskHead{TaskID: taskID, ObjectID: head})
	}
	return heads, nil
}

func (s *stormSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	if s.onInspect != nil {
		s.onInspect(taskID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	head, found := s.heads[taskID]
	return gitstore.TaskHead{TaskID: taskID, ObjectID: head}, found, nil
}

func (s *stormSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	s.mu.Lock()
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, found := s.snapshots[head.ObjectID]
		if !found {
			s.mu.Unlock()
			return nil, fmt.Errorf("storm source has no checkpoint %s", head.ObjectID)
		}
		snapshots = append(snapshots, snapshot)
	}
	s.mu.Unlock()
	if s.onRead != nil {
		s.onRead(heads)
	}
	return snapshots, nil
}

func (s *stormSource) ValidateTaskHeadAdvancesWithCompactions(_ context.Context, _ core.ProjectConfig, _ []gitstore.HeadAdvance) (map[string]struct{}, error) {
	return nil, nil
}

// stormStore opens a store over source and builds its cache from the refs as
// they stand, before any hook is installed.
func stormStore(t *testing.T, source *stormSource) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := openStoreForTest(t, ctx, source, testConfig(), filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	return store
}

// stormSnapshot is testSnapshot with the operation pack a writer's Advance
// projects alongside the checkpoint.
func stormSnapshot(taskID, head, title string) core.Snapshot {
	snapshot := testSnapshot(taskID, head, title)
	snapshot.Operation = core.OperationPack{TaskID: taskID, ProjectID: testConfig().ProjectID, LogicalClock: 1}
	return snapshot
}

func stormTaskID(index int) string {
	return fmt.Sprintf("WB-01K0M6B8A4FTT8C39MXXYTW%03d", index)
}

// Every read on a board where tasks are being filed finds new task refs, and
// each new task is being projected by the create that wrote it at the same
// moment the read projects it too. The read used to count that as the cache
// changing underneath it, try once more, find the next create doing the same,
// and answer "cannot refresh projection cache after a concurrent update; run
// `workbook rebuild`". Both writers were projecting the same checkpoint, so
// there was nothing to refuse.
func TestStoreListSucceedsWhileEachCreateProjectsItsOwnTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := newStormSource()
	source.move(stormSnapshot(stormTaskID(0), "head-0", "Before the storm"))
	store := stormStore(t, source)

	created := 0
	source.onList = func() {
		created++
		source.move(stormSnapshot(stormTaskID(created), fmt.Sprintf("head-%d", created), fmt.Sprintf("Created %d", created)))
	}
	source.onRead = func(heads []gitstore.TaskHead) {
		for _, head := range heads {
			snapshot := source.snapshots[head.ObjectID]
			if applied, err := store.Advance(ctx, testConfig(), "", snapshot); err != nil || !applied {
				t.Errorf("creator's Advance(%s) = %t, %v, want true, nil", head.TaskID, applied, err)
			}
		}
	}

	for round := 1; round <= 3; round++ {
		listed, err := store.List(ctx, testConfig())
		if err != nil {
			t.Fatalf("List() round %d error = %v", round, err)
		}
		if len(listed) != round+1 {
			t.Fatalf("List() round %d = %d tasks, want %d", round, len(listed), round+1)
		}
		for _, snapshot := range listed {
			if want := source.head(snapshot.State.TaskID); snapshot.Head != want {
				t.Fatalf("List() round %d answered %s at %s, want its ref %s", round, snapshot.State.TaskID, snapshot.Head, want)
			}
		}
	}
}

// One task edited over and over by other writers while a read is open: after
// the read lists the ref, another write moves both the ref and the row past
// what the read is about to project. The read leaves that row to the write
// that moved it and answers the task from the checkpoint it read from Git,
// which is the task as it stood after the read began. It never fails, never
// puts the older checkpoint over the newer row, and never answers a head the
// ref did not hold during the read.
func TestStoreListAnswersFromGitWhenAnotherWriteMovesTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Edit 1"))
	store := stormStore(t, source)

	edits := 1
	row := "head-1"
	held := map[string]bool{}
	edit := func() core.Snapshot {
		edits++
		snapshot := stormSnapshot(taskID, fmt.Sprintf("head-%d", edits), fmt.Sprintf("Edit %d", edits))
		source.move(snapshot)
		return snapshot
	}
	// A write lands between the read's cache scan and its ref listing, so
	// every listing finds the task changed.
	source.onList = func() { held[edit().Head] = true }
	// And another lands, with its own projection, while the read is reading
	// the checkpoint it listed.
	source.onRead = func([]gitstore.TaskHead) {
		written := edit()
		held[written.Head] = true
		if applied, err := store.Advance(ctx, testConfig(), row, written); err != nil || !applied {
			t.Errorf("writer's Advance(%s → %s) = %t, %v, want true, nil", row, written.Head, applied, err)
		}
		row = written.Head
	}

	for round := 1; round <= 3; round++ {
		listed, err := store.List(ctx, testConfig())
		if err != nil {
			t.Fatalf("List() round %d error = %v", round, err)
		}
		if len(listed) != 1 || !held[listed[0].Head] {
			t.Fatalf("List() round %d = %#v, want the one task at a head its ref held during the read", round, listed)
		}
		cached, found, err := store.querySnapshot(ctx, taskID)
		if err != nil || !found || cached.Head != row {
			t.Fatalf("cache row after round %d = %q (found %t, %v), want the newest projected head %q", round, cached.Head, found, err, row)
		}
	}

	resolved, err := store.Resolve(ctx, testConfig(), taskID[:12])
	if err != nil || resolved != taskID {
		t.Fatalf("Resolve() = %q, %v, want %q", resolved, err, taskID)
	}
}

// The same storm on an exact read, the one every edit makes for its parent:
// each inspection finds the ref moved, and each projection finds the row moved
// again by somebody else. The read tries the newer row once and then answers
// from the checkpoint it read, rather than failing the edit before it starts.
func TestStoreGetAnswersFromGitWhenEveryProjectionLosesTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Edit 1"))
	store := stormStore(t, source)

	edits := 1
	row := "head-1"
	held := map[string]bool{}
	edit := func() core.Snapshot {
		edits++
		snapshot := stormSnapshot(taskID, fmt.Sprintf("head-%d", edits), fmt.Sprintf("Edit %d", edits))
		source.move(snapshot)
		held[snapshot.Head] = true
		return snapshot
	}
	source.onInspect = func(string) { edit() }
	source.onRead = func([]gitstore.TaskHead) {
		written := edit()
		if applied, err := store.Advance(ctx, testConfig(), row, written); err != nil || !applied {
			t.Errorf("writer's Advance(%s → %s) = %t, %v, want true, nil", row, written.Head, applied, err)
		}
		row = written.Head
	}

	got, err := store.Get(ctx, testConfig(), taskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !held[got.Head] || got.State.TaskID != taskID {
		t.Fatalf("Get() = %s at %s, want the task at a head its ref held during the read", got.State.TaskID, got.Head)
	}
	cached, found, err := store.querySnapshot(ctx, taskID)
	if err != nil || !found || cached.Head != row {
		t.Fatalf("cache row = %q (found %t, %v), want the newest projected head %q", cached.Head, found, err, row)
	}
}

// A write redone because another process replaced the cache file under it is
// bounded, and when every redo loses to another replacement the answer is to
// send the request again: each replacement was a valid cache, and nothing was
// written. It used to be a 500 that advised a rebuild, which is the very thing
// that kept winning the race.
func TestStoreWriteRedoGivesUpRetryablyWhenTheCacheKeepsBeingReplaced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := newStormSource()
	source.move(stormSnapshot(stormTaskID(1), "head-1", "Replaced"))
	store := stormStore(t, source)
	pristine, err := os.ReadFile(store.cachePath)
	if err != nil {
		t.Fatal(err)
	}

	bodies := 0
	err = store.withActiveDatabase(ctx, func(ctx context.Context) error {
		bodies++
		// Pin the connection lockActiveDatabase just validated, replace the
		// file under it the way another process's rebuild does, and write.
		connection, err := store.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer connection.Close()
		replacement := filepath.Join(filepath.Dir(store.cachePath), fmt.Sprintf("replacement-%d.sqlite", bodies))
		if err := os.WriteFile(replacement, pristine, 0o600); err != nil {
			return err
		}
		if err := os.Rename(replacement, store.cachePath); err != nil {
			return err
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO projection_meta (key, value) VALUES (?, 'x')`, fmt.Sprintf("probe-%d", bodies))
		return err
	})
	if bodies != 3 {
		t.Fatalf("body ran %d times, want the bound of 3; error = %v", bodies, err)
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("withActiveDatabase() = %v (%q), want a stale-write refusal", err, core.CategoryOf(err))
	}
	if strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("withActiveDatabase() = %v, must not advise a rebuild", err)
	}
}

// SQLite reporting the cache locked past its busy timeout is another
// connection holding it, not a broken cache: the answer is the retryable one,
// without the rebuild advice. A cache that is really damaged still gets that
// advice; TestProjectionCacheErrorsSuggestRebuildWithoutMaskingGitCorruption
// holds that half.
func TestCacheErrorCallsALockedCacheRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := newStormSource()
	source.move(stormSnapshot(stormTaskID(1), "head-1", "Locked"))
	store := stormStore(t, source)

	holder, err := sql.Open("sqlite", "file:"+store.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	held, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if _, err := held.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = held.ExecContext(ctx, `ROLLBACK`) })

	// No busy timeout, so the lock is reported at once instead of after the
	// five seconds the store itself waits.
	impatient, err := sql.Open("sqlite", "file:"+store.cachePath+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = impatient.Close() })
	_, lockErr := impatient.ExecContext(ctx, `SELECT count(*) FROM tasks`)
	if lockErr == nil {
		t.Fatal("reading a cache another connection holds exclusively succeeded, want SQLITE_BUSY")
	}

	err = cacheError("query projected tasks", lockErr)
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("cacheError(locked) = %v (%q), want a stale-write refusal", err, core.CategoryOf(err))
	}
	if strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("cacheError(locked) = %v, must not advise a rebuild", err)
	}
}
