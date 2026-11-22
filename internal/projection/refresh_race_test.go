package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
// which is the head its own listing found. It never fails, never puts the
// older checkpoint over the newer row, and never answers a head older than
// the one it listed; the newer head the row then holds would be right too.
func TestStoreListAnswersFromGitWhenAnotherWriteMovesTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Edit 1"))
	store := stormStore(t, source)

	edits := 1
	row := "head-1"
	listedHead, writtenHead := "", ""
	edit := func() core.Snapshot {
		edits++
		snapshot := stormSnapshot(taskID, fmt.Sprintf("head-%d", edits), fmt.Sprintf("Edit %d", edits))
		source.move(snapshot)
		return snapshot
	}
	// A write lands between the read's cache scan and its ref listing, so
	// every listing finds the task changed.
	source.onList = func() { listedHead = edit().Head }
	// And another lands, with its own projection, while the read is reading
	// the checkpoint it listed.
	source.onRead = func([]gitstore.TaskHead) {
		written := edit()
		writtenHead = written.Head
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
		if len(listed) != 1 || (listed[0].Head != listedHead && listed[0].Head != writtenHead) {
			t.Fatalf("List() round %d = %#v, want the one task at the head its listing found, %s, or the one written since, %s", round, listed, listedHead, writtenHead)
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

// A slower writer can move a row forward during a read without reaching the
// ref: the cache holds head-1, the ref is already at head-3, and while the
// read is in Git the writer of head-2 records it. The read leaves head-2 to
// that writer, and answers the task at head-3, the head it listed. Answering
// the row instead would report head-2 after the write of head-3 had finished,
// an older task than the ref held for the whole of the read.
func TestStoreListAnswersTheListedHeadWhenASlowerWriterMovesTheRowBehindIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Edit 1"))
	store := stormStore(t, source)
	second := stormSnapshot(taskID, "head-2", "Edit 2")
	source.move(second)
	source.move(stormSnapshot(taskID, "head-3", "Edit 3"))

	slow := true
	source.onRead = func([]gitstore.TaskHead) {
		if !slow {
			return
		}
		slow = false
		if applied, err := store.Advance(ctx, testConfig(), "head-1", second); err != nil || !applied {
			t.Errorf("slower writer's Advance(head-1 → head-2) = %t, %v, want true, nil", applied, err)
		}
	}

	listed, err := store.List(ctx, testConfig())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if slow {
		t.Fatal("the read never reached Git, so the slower writer never ran")
	}
	if len(listed) != 1 || listed[0].Head != "head-3" || listed[0].State.Task.Title != "Edit 3" {
		t.Fatalf("List() = %#v, want the one task at head-3, \"Edit 3\", the head its ref held throughout", listed)
	}
	cached, found, err := store.querySnapshot(ctx, taskID)
	if err != nil || !found || cached.Head != "head-2" {
		t.Fatalf("cache row = %q (found %t, %v), want head-2, left to the writer that recorded it", cached.Head, found, err)
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
	requireBusyCacheWording(t, err)
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
	requireBusyCacheWording(t, err)
}

// requireBusyCacheWording holds the phrase the board tells a busy cache from a
// changed task by. Both are refused as stale-write, and the board's card says
// "The board was busy" only for a message that names the projection cache
// (cacheWasBusy in the web UI's failures.js). The board is sent the outermost
// message, which is the one checked.
func requireBusyCacheWording(t *testing.T, err error) {
	t.Helper()
	var typed *core.Error
	if !errors.As(err, &typed) || !strings.Contains(typed.Message, "the projection cache") {
		t.Fatalf("busy cache refusal %q does not name the projection cache, so the board would report it as the task changing elsewhere", err)
	}
}

// A row can also disappear while a read is in Git: a writer whose own advance
// was declined invalidates the row it found. The read still listed that
// task's ref, so the task is in its answer, from the checkpoint it read,
// rather than missing from the board or unknown to a prefix lookup.
func TestStoreListAndResolveAnswerATaskWhoseRowWasRemovedDuringTheRefresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Edit 1"))
	store := stormStore(t, source)

	edits := 1
	previous, current := "", "head-1"
	source.onList = func() {
		edits++
		previous, current = current, fmt.Sprintf("head-%d", edits)
		source.move(stormSnapshot(taskID, current, fmt.Sprintf("Edit %d", edits)))
	}
	source.onRead = func([]gitstore.TaskHead) {
		if err := store.Invalidate(ctx, testConfig(), taskID, previous, current); err != nil {
			t.Errorf("Invalidate() error = %v", err)
		}
	}

	listed, err := store.List(ctx, testConfig())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].State.TaskID != taskID || listed[0].Head != current {
		t.Fatalf("List() = %#v, want %s at the head it listed, %s", listed, taskID, current)
	}
	if _, found, err := store.querySnapshot(ctx, taskID); err != nil || found {
		t.Fatalf("cache row found %t, %v, want it left removed by the writer that removed it", found, err)
	}

	// Projected again, and removed again while a prefix lookup is in Git.
	if applied, err := store.Advance(ctx, testConfig(), "", source.snapshots[current]); err != nil || !applied {
		t.Fatalf("Advance(reproject) = %t, %v, want true, nil", applied, err)
	}
	resolved, err := store.Resolve(ctx, testConfig(), strings.ToLower(taskID[:14]))
	if err != nil || resolved != taskID {
		t.Fatalf("Resolve() = %q, %v, want %q", resolved, err, taskID)
	}
}

// hookedRepositorySource reads a real repository and runs onRead after each
// checkpoint read, numbered from one.
type hookedRepositorySource struct {
	repositorySource
	reads  int
	onRead func(read int)
}

func (s *hookedRepositorySource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	snapshots, err := s.repositorySource.ReadTaskHeads(ctx, config, heads)
	s.reads++
	if err == nil && s.onRead != nil {
		s.onRead(s.reads)
	}
	return snapshots, err
}

// A task's history read answers the task from its ref, and when both of its
// attempts to project that ref find the row moved by a slower writer, it
// answers from Git with the row left behind the ref. The history then has to
// end where that answer does, not where the lagging row's operations end.
func TestStoreTaskHistoryEndsAtTheHeadItReadWhenTheRowLags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	created := createTask(t, repository, config, "History raced")
	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	heads := []string{created.Head}
	for index, id := range []string{"01K0M6B8A4FTT8C39MXXYTW7E1", "01K0M6B8A4FTT8C39MXXYTW7E2", "01K0M6B8A4FTT8C39MXXYTW7E3"} {
		title := fmt.Sprintf("History raced, edit %d", index+1)
		service := core.Service{Config: config, Reader: repository, Writer: repository, Actor: "test@example.test",
			Now: func() time.Time { return time.Date(2026, time.July, 26, 12, 1+index, 0, 0, time.UTC) },
			IDs: core.IDSourceFunc(func() (string, error) { return id, nil })}
		result, err := service.UpdateMutation(ctx, created.ID, core.UpdateInput{Title: &title})
		if err != nil {
			t.Fatalf("UpdateMutation(%d) error = %v", index+1, err)
		}
		heads = append(heads, result.Task.Head)
	}
	snapshots, err := repository.ReadTaskHeads(ctx, config, []gitstore.TaskHead{
		{TaskID: created.ID, ObjectID: heads[1]}, {TaskID: created.ID, ObjectID: heads[2]},
	})
	if err != nil {
		t.Fatalf("ReadTaskHeads() error = %v", err)
	}

	// The cache holds the creation; the ref is three edits on. While each of
	// the read's two attempts is in Git, the writer of the next older edit
	// records it, so the row moves to the second edit and stops there.
	store.source = &hookedRepositorySource{
		repositorySource: repositorySource{repository: repository},
		onRead: func(read int) {
			if read > 2 {
				return
			}
			if applied, err := store.Advance(ctx, config, heads[read-1], snapshots[read-1]); err != nil || !applied {
				t.Errorf("slow writer's Advance(%d) = %t, %v, want true, nil", read, applied, err)
			}
		},
	}
	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if cached, _, _ := store.querySnapshot(ctx, created.ID); cached.Head != heads[2] {
		t.Fatalf("cache row = %s, want it left at the slow writer's %s", cached.Head, heads[2])
	}
	if len(history.Entries) != len(heads) || history.Entries[len(history.Entries)-1].Commit != heads[3] {
		t.Fatalf("TaskHistory() = %d entries ending at %s, want %d ending at the ref's %s",
			len(history.Entries), history.Entries[len(history.Entries)-1].Commit, len(heads), heads[3])
	}
}

// The first commands in a fresh clone find no cache and each rebuild one, while
// other commands are filing tasks. A rebuild whose refs moved through both of
// its builds used to refuse with "task refs kept changing", which failed the
// read that only needed a cache to answer from. The second build is installed
// instead, and the read brings it up to the refs like any cache that is behind.
func TestStoreListAnswersWhileRefsMoveThroughEveryRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := newStormSource()
	source.move(stormSnapshot(stormTaskID(1), "head-1-1", "Task 1"))
	// No Rebuild first: the read finds no cache at all.
	store, err := openStoreForTest(t, ctx, source, testConfig(), filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	// Every listing finds one more task filed and one task edited since the
	// last, so no build ever matches the refs it is checked against.
	listings := 0
	source.onList = func() {
		listings++
		source.move(stormSnapshot(stormTaskID(1), fmt.Sprintf("head-1-%d", listings+1), "Task 1"))
		taskID := stormTaskID(listings + 1)
		source.move(stormSnapshot(taskID, fmt.Sprintf("head-%d-1", listings+1), fmt.Sprintf("Task %d", listings+1)))
	}

	listed, err := store.List(ctx, testConfig())
	if err != nil {
		t.Fatalf("List() while the refs kept moving error = %v", err)
	}
	if listings < 5 {
		t.Fatalf("the refs were listed %d times, want both builds bracketed and a refresh after them", listings)
	}
	// The read answers every task its last listing found, at the head it
	// found, which is every ref as it stands now: the hook moves the refs
	// before each listing is taken, never after.
	source.onList = nil
	if len(listed) != listings+1 {
		t.Fatalf("List() answered %d tasks, want the %d its last listing found", len(listed), listings+1)
	}
	for _, snapshot := range listed {
		if want := source.head(snapshot.State.TaskID); snapshot.Head != want {
			t.Fatalf("List() answered %s at %s, want the head its last listing found, %s", snapshot.State.TaskID, snapshot.Head, want)
		}
	}
	// And the cache it left behind serves the next read with nothing moving.
	again, err := store.List(ctx, testConfig())
	if err != nil || len(again) != len(listed) {
		t.Fatalf("List() once the refs settled = %d tasks, %v, want %d", len(again), err, len(listed))
	}
}

// A cache another process replaces again right after each time this store
// recovers it is bounded too: three rounds of opening the replacement, and
// then the request is refused as one to send again, with nothing written.
// Each replacement is a valid cache, so the refusal does not advise a
// rebuild; it used to be a 500 that did.
func TestStoreActivationGivesUpRetryablyWhenTheCacheIsReplacedAfterEveryRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	taskID := stormTaskID(1)
	source := newStormSource()
	source.move(stormSnapshot(taskID, "head-1", "Before"))
	store := stormStore(t, source)
	pristine, err := os.ReadFile(store.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	replacements := 0
	replace := func() {
		replacements++
		replacement := filepath.Join(filepath.Dir(store.cachePath), fmt.Sprintf("replacement-%d.sqlite", replacements))
		if err := os.WriteFile(replacement, pristine, 0o600); err != nil {
			t.Errorf("write replacement cache: %v", err)
			return
		}
		if err := os.Rename(replacement, store.cachePath); err != nil {
			t.Errorf("install replacement cache: %v", err)
		}
	}

	// Replaced once before the write arrives, and again after every recovery.
	replace()
	recoveries := 0
	store.afterRecovery = func() {
		recoveries++
		replace()
	}
	written := stormSnapshot(taskID, "head-2", "After")
	advanced, err := store.Advance(ctx, testConfig(), "head-1", written)
	store.afterRecovery = nil

	if recoveries != 3 {
		t.Fatalf("recovered %d times, want the bound of 3; Advance() = %t, %v", recoveries, advanced, err)
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("Advance() = %t, %v (%q), want a stale-write refusal", advanced, err, core.CategoryOf(err))
	}
	for _, want := range []string{"changed again while it was being activated", "nothing was changed, try again"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Advance() error = %q, want it to say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("Advance() error = %q, must not advise a rebuild", err)
	}
	requireBusyCacheWording(t, err)

	// Nothing was written: the cache now in place, which the next request
	// opens, still holds the task as it was. It is read directly, because a
	// read through the store would bring the row up to the ref either way.
	reader, err := sql.Open("sqlite", "file:"+store.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var head string
	if err := reader.QueryRowContext(ctx, `SELECT head FROM tasks WHERE task_id = ?`, taskID).Scan(&head); err != nil {
		t.Fatalf("read the cache in place: %v", err)
	}
	if head != "head-1" {
		t.Fatalf("cache in place holds %s, want head-1, untouched by the refused write", head)
	}
}
