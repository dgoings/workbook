package projection

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
	"modernc.org/sqlite"
)

func TestStoreRefreshUsesSQLiteUntilATaskHeadChanges(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first, err := store.List(ctx, config)
	if err != nil || len(first) != 1 || first[0].State.Task.Title != "Initial title" {
		t.Fatalf("first projection = %#v, %v", first, err)
	}

	updateTaskTitle(t, repository, config, created.ID, "Changed title")
	second, err := store.List(ctx, config)
	if err != nil || len(second) != 1 || second[0].State.Task.Title != "Changed title" {
		t.Fatalf("incremental projection = %#v, %v", second, err)
	}

	if err := os.Remove(store.CachePath()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := store.List(ctx, config)
	if err != nil || len(rebuilt) != 1 || rebuilt[0].State.Task.Title != "Changed title" {
		t.Fatalf("recreated projection = %#v, %v", rebuilt, err)
	}
	if _, err := os.Stat(store.CachePath()); err != nil {
		t.Fatalf("Stat(recreated cache) error = %v", err)
	}
}

func TestStoreRefreshReadsOnlyAdvancedHeads(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	source := &countingHeadSource{snapshots: map[string]core.Snapshot{
		first.Head:  first,
		second.Head: second,
	}}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	source.heads = []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}, {TaskID: second.State.TaskID, ObjectID: second.Head}}
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(initial) error = %v", err)
	}
	if got, want := source.reads, 2; got != want {
		t.Fatalf("initial ReadTaskHead calls = %d, want %d", got, want)
	}

	source.reads = 0
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(unchanged) error = %v", err)
	}
	if got := source.reads; got != 0 {
		t.Fatalf("unchanged ReadTaskHead calls = %d, want 0", got)
	}

	advanced := second
	advanced.Head = "head-3"
	advanced.State.Task.Title = "Second, changed"
	source.snapshots[advanced.Head] = advanced
	source.heads[1].ObjectID = advanced.Head
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(advanced) error = %v", err)
	}
	if got, want := source.reads, 1; got != want {
		t.Fatalf("advanced ReadTaskHead calls = %d, want %d", got, want)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := snapshots[1].State.Task.Title, "Second, changed"; got != want {
		t.Fatalf("changed title = %q, want %q", got, want)
	}
}

func TestConcurrentRefreshCannotRegressProjectedHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot(first.State.TaskID, "head-2", "Second")
	third := testSnapshot(first.State.TaskID, "head-3", "Third")
	initialSource := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}},
		snapshots: map[string]core.Snapshot{first.Head: first},
	}
	store, err := openStore(ctx, initialSource, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	source := newConcurrentRefreshSource(second, third)
	store.source = source
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.Refresh(ctx)
	}()
	<-source.secondReadStarted

	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- store.Refresh(ctx)
	}()
	thirdCompleted := false
	select {
	case <-source.thirdHeadListed:
		if err := <-thirdDone; err != nil {
			t.Fatalf("Refresh(third) error = %v", err)
		}
		thirdCompleted = true
	case <-time.After(250 * time.Millisecond):
	}
	close(source.releaseSecondRead)
	if err := <-firstDone; err != nil {
		t.Fatalf("Refresh(second) error = %v", err)
	}
	if !thirdCompleted {
		err := <-thirdDone
		if err != nil {
			t.Fatalf("Refresh(third) error = %v", err)
		}
	}

	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != third.Head || snapshots[0].State.Task.Title != "Third" {
		t.Fatalf("final cache = %#v, want newest head %q", snapshots, third.Head)
	}
}

func TestStoreConcurrentReadsAndRebuilds(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Concurrent")
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	const workers = 8
	const iterations = 12
	errors := make(chan error, workers*iterations)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if (worker+iteration)%3 == 0 {
					_, err := store.Rebuild(ctx)
					errors <- err
					continue
				}
				snapshots, err := store.List(ctx, config)
				if err == nil && (len(snapshots) != 1 || snapshots[0].Head != snapshot.Head) {
					err = fmt.Errorf("List() = %#v, want head %q", snapshots, snapshot.Head)
				}
				errors <- err
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent cache operation error = %v", err)
		}
	}
}

func TestListUsesBoundedSQLQueriesForManyTasks(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	taskIDs := []string{
		"WB-01K0M6B8A4FTT8C39MXXYTW7D1",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D2",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D3",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D4",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D5",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D6",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D7",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D8",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D9",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DA",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DB",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DC",
	}
	source := &staticHeadSource{snapshots: make(map[string]core.Snapshot, len(taskIDs))}
	for index, taskID := range taskIDs {
		head := fmt.Sprintf("head-%02d", index)
		snapshot := testSnapshot(taskID, head, fmt.Sprintf("Task %02d", index))
		snapshot.State.Task.Labels = []string{"zeta", "alpha"}
		snapshot.State.Task.Dependencies = []string{taskIDs[1], taskIDs[0]}
		source.heads = append(source.heads, gitstore.TaskHead{TaskID: taskID, ObjectID: head})
		source.snapshots[head] = snapshot
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	var queries atomic.Int64
	driverName := fmt.Sprintf("workbook-counting-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, &queryCountingDriver{inner: &sqlite.Driver{}, queries: &queries})
	if err := store.db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store.db, err = sql.Open(driverName, store.cachePath)
	if err != nil {
		t.Fatalf("Open(counting driver) error = %v", err)
	}

	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != len(taskIDs) {
		t.Fatalf("List() count = %d, want %d", len(snapshots), len(taskIDs))
	}
	if got, want := snapshots[0].State.Task.Labels, []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if got, want := snapshots[0].State.Task.Dependencies, []string{taskIDs[0], taskIDs[1]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if got := queries.Load(); got > 10 {
		t.Fatalf("unchanged List() SQL queries = %d for %d tasks, want at most 10", got, len(taskIDs))
	}
}

func TestProjectionPreservesCanonicalTimestampOffsets(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	offset := time.FixedZone("canonical-offset", -(4*60+30)*60)
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Offset")
	snapshot.State.Task.CreatedAt = time.Date(2026, time.July, 26, 12, 34, 56, 789, offset)
	snapshot.State.Task.UpdatedAt = time.Date(2026, time.July, 26, 13, 45, 1, 987, offset)
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	got, err := store.Get(ctx, config, snapshot.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State.Task.CreatedAt.Format(time.RFC3339Nano) != snapshot.State.Task.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("createdAt = %q, want preserved %q", got.State.Task.CreatedAt.Format(time.RFC3339Nano), snapshot.State.Task.CreatedAt.Format(time.RFC3339Nano))
	}
	if got.State.Task.UpdatedAt.Format(time.RFC3339Nano) != snapshot.State.Task.UpdatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("updatedAt = %q, want preserved %q", got.State.Task.UpdatedAt.Format(time.RFC3339Nano), snapshot.State.Task.UpdatedAt.Format(time.RFC3339Nano))
	}
}

func TestProjectionCacheErrorsSuggestRebuildWithoutMaskingGitCorruption(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Cache error")
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE task_labels`); err != nil {
		t.Fatalf("DROP TABLE error = %v", err)
	}
	if _, err := store.querySnapshots(ctx); core.CategoryOf(err) != core.CategoryOperational || !strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("cache error = %v (%q), want operational error with rebuild guidance", err, core.CategoryOf(err))
	}

	canonicalError := core.Errorf(core.CategoryCorruptData, "canonical Git checkpoint is corrupt")
	corruptSource := &staticHeadSource{
		heads:   []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		readErr: canonicalError,
	}
	corruptStore, err := openStore(ctx, corruptSource, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore(corrupt source) error = %v", err)
	}
	err = corruptStore.Refresh(ctx)
	if core.CategoryOf(err) != core.CategoryCorruptData || !errors.Is(err, canonicalError) {
		t.Fatalf("Refresh(corrupt Git) error = %v (%q), want original corrupt-data error", err, core.CategoryOf(err))
	}
	if strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("Refresh(corrupt Git) error = %v, must not suggest rebuilding cache", err)
	}
}

func TestStoreRebuildsMalformedOrWrongProjectDatabase(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}

	for _, contents := range [][]byte{
		[]byte("not a sqlite database"),
		validProjectionDatabase(t, "2", config.ProjectID),
		validProjectionDatabase(t, schemaVersion, config.ProjectID+"X"),
	} {
		if err := os.WriteFile(store.CachePath(), contents, 0o600); err != nil {
			t.Fatalf("WriteFile(cache) error = %v", err)
		}
		snapshots, err := store.List(ctx, config)
		if err != nil {
			t.Fatalf("List(recovery) error = %v", err)
		}
		if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
			t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
		}
	}
}

func TestOpenRebuildsMalformedCacheFromCanonicalGit(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := os.WriteFile(cachePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("WriteFile(cache) error = %v", err)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
		t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
	}
}

func TestStoreRebuildsMetadataMatchingDatabaseMissingRequiredTable(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := writeIncompleteProjectionDatabase(cachePath, config.ProjectID); err != nil {
		t.Fatalf("writeIncompleteProjectionDatabase() error = %v", err)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
		t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
	}
}

func TestRebuildLeavesPreviousDatabaseWhenReplacementFails(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	replacement := testSnapshot(previous.State.TaskID, "head-2", "Replacement")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous, replacement.Head: replacement},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	source.heads[0].ObjectID = replacement.Head
	store.rename = func(string, string) error { return errors.New("rename failed") }
	if _, err := store.Rebuild(ctx); err == nil {
		t.Fatal("Rebuild(replacement) error = nil, want failure")
	}

	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() after failed rebuild error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != previous.Head || snapshots[0].State.Task.Title != "Previous" {
		t.Fatalf("cache after failed rebuild = %#v, want previous projected snapshot", snapshots)
	}
}

func TestRebuildRetriesOnceWhenHeadsChangeDuringBuild(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot(first.State.TaskID, "head-2", "Second")
	source := &headSequenceSource{
		headSets: [][]gitstore.TaskHead{
			{{TaskID: first.State.TaskID, ObjectID: first.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		},
		snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	count, err := store.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Rebuild() count = %d, want 1", count)
	}
	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != second.Head || snapshots[0].State.Task.Title != "Second" {
		t.Fatalf("installed cache = %#v, want second task head", snapshots)
	}

	third := testSnapshot(first.State.TaskID, "head-3", "Third")
	source.headSets = [][]gitstore.TaskHead{
		{{TaskID: first.State.TaskID, ObjectID: first.Head}},
		{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		{{TaskID: third.State.TaskID, ObjectID: third.Head}},
	}
	source.index = 0
	source.snapshots[third.Head] = third
	if _, err := store.Rebuild(ctx); core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("Rebuild() category = %q, want operational; error = %v", core.CategoryOf(err), err)
	}
}

func TestStoreQueriesAndRejectsWrites(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	first.State.Task.Labels = []string{"alpha", "zeta"}
	first.State.Task.Dependencies = []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D2"}
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	source := &countingHeadSource{heads: []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}, {TaskID: second.State.TaskID, ObjectID: second.Head}}, snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second}}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	got, err := store.Get(ctx, config, first.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(got.State.Task.Labels, []string{"alpha", "zeta"}) || !reflect.DeepEqual(got.State.Task.Dependencies, []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D2"}) {
		t.Fatalf("Get() collections = %#v", got.State.Task)
	}
	resolved, err := store.Resolve(ctx, config, "wb-01k0m6b8a4ftt8c39mxxytw7d1")
	if err != nil || resolved != first.State.TaskID {
		t.Fatalf("Resolve(case-insensitive) = %q, %v", resolved, err)
	}
	_, err = store.Resolve(ctx, config, "WB-")
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Resolve(ambiguous) category = %q, want validation; error = %v", got, err)
	}
	_, err = store.Get(ctx, config, "WB-01K0M6B8A4FTT8C39MXXYTW7D3")
	if got := core.CategoryOf(err); got != core.CategoryNotFound {
		t.Fatalf("Get(missing) category = %q, want not-found; error = %v", got, err)
	}
	_, err = store.Write(ctx, config, nil, core.OperationPack{}, core.StateDocument{}, "write")
	if got := core.CategoryOf(err); got != core.CategoryOperational {
		t.Fatalf("Write() category = %q, want operational; error = %v", got, err)
	}
}

type countingHeadSource struct {
	heads     []gitstore.TaskHead
	snapshots map[string]core.Snapshot
	reads     int
	err       error
}

type headSequenceSource struct {
	headSets  [][]gitstore.TaskHead
	snapshots map[string]core.Snapshot
	index     int
}

type concurrentRefreshSource struct {
	mu                sync.Mutex
	snapshots         map[string]core.Snapshot
	headCalls         int
	secondReadStarted chan struct{}
	releaseSecondRead chan struct{}
	thirdHeadListed   chan struct{}
}

type staticHeadSource struct {
	heads     []gitstore.TaskHead
	snapshots map[string]core.Snapshot
	readErr   error
}

type queryCountingDriver struct {
	inner   driver.Driver
	queries *atomic.Int64
}

type queryCountingConn struct {
	driver.Conn
	queries *atomic.Int64
}

var countingDriverSequence atomic.Int64

func newConcurrentRefreshSource(second, third core.Snapshot) *concurrentRefreshSource {
	return &concurrentRefreshSource{
		snapshots:         map[string]core.Snapshot{second.Head: second, third.Head: third},
		secondReadStarted: make(chan struct{}),
		releaseSecondRead: make(chan struct{}),
		thirdHeadListed:   make(chan struct{}),
	}
}

func (s *concurrentRefreshSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headCalls++
	head := s.snapshots["head-2"]
	if s.headCalls > 1 {
		head = s.snapshots["head-3"]
		if s.headCalls == 2 {
			close(s.thirdHeadListed)
		}
	}
	return []gitstore.TaskHead{{TaskID: head.State.TaskID, ObjectID: head.Head}}, nil
}

func (s *concurrentRefreshSource) ReadTaskHead(_ context.Context, _ core.ProjectConfig, head gitstore.TaskHead) (core.Snapshot, error) {
	if head.ObjectID == "head-2" {
		close(s.secondReadStarted)
		<-s.releaseSecondRead
	}
	return s.snapshots[head.ObjectID], nil
}

func (s *staticHeadSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return append([]gitstore.TaskHead(nil), s.heads...), nil
}

func (s *staticHeadSource) ReadTaskHead(_ context.Context, _ core.ProjectConfig, head gitstore.TaskHead) (core.Snapshot, error) {
	if s.readErr != nil {
		return core.Snapshot{}, s.readErr
	}
	snapshot, found := s.snapshots[head.ObjectID]
	if !found {
		return core.Snapshot{}, errors.New("missing test snapshot")
	}
	return snapshot, nil
}

func (d *queryCountingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &queryCountingConn{Conn: connection, queries: d.queries}, nil
}

func (c *queryCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *queryCountingConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (s *headSequenceSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	if len(s.headSets) == 0 {
		return nil, nil
	}
	index := s.index
	if index >= len(s.headSets) {
		index = len(s.headSets) - 1
	}
	s.index++
	return append([]gitstore.TaskHead(nil), s.headSets[index]...), nil
}

func (s *headSequenceSource) ReadTaskHead(_ context.Context, _ core.ProjectConfig, head gitstore.TaskHead) (core.Snapshot, error) {
	snapshot, found := s.snapshots[head.ObjectID]
	if !found {
		return core.Snapshot{}, errors.New("missing test snapshot")
	}
	return snapshot, nil
}

func (s *countingHeadSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return append([]gitstore.TaskHead(nil), s.heads...), s.err
}

func (s *countingHeadSource) ReadTaskHead(_ context.Context, _ core.ProjectConfig, head gitstore.TaskHead) (core.Snapshot, error) {
	s.reads++
	snapshot, found := s.snapshots[head.ObjectID]
	if !found {
		return core.Snapshot{}, errors.New("missing test snapshot")
	}
	return snapshot, nil
}

func testConfig() core.ProjectConfig {
	return core.ProjectConfig{Format: "workbook.project", Version: 1, ProjectID: "01K0M6B8A4FTT8C39MXXYTW7D0", Key: "WB"}
}

func testSnapshot(taskID, head, title string) core.Snapshot {
	createdAt := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	return core.Snapshot{
		Head: head,
		State: core.StateDocument{
			Format: "workbook.task-state", Version: 1, ProjectID: testConfig().ProjectID, TaskID: taskID,
			History: core.History{Generation: "01K0M6B8A4FTT8C39MXXYTW7D9"}, LogicalClock: 1,
			Task: core.TaskData{Title: title, Status: core.StatusBacklog, Priority: core.PriorityMedium, Rank: "1/1", CreatedAt: createdAt, UpdatedAt: createdAt},
		},
	}
}

func initializeWorkbook(t *testing.T, root string) (*gitstore.Repository, core.ProjectConfig) {
	t.Helper()
	repository, err := gitstore.Open(context.Background(), root)
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, _, err := repository.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		return testConfig().ProjectID, nil
	}))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return repository, config
}

func createTask(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, title string) core.Task {
	t.Helper()
	ids := []string{"01K0M6B8A4FTT8C39MXXYTW7D1", "01K0M6B8A4FTT8C39MXXYTW7D9", "01K0M6B8A4FTT8C39MXXYTW7D8"}
	index := 0
	service := core.Service{Config: config, Store: repository, Actor: "test@example.test", Now: func() time.Time { return time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC) }, IDs: core.IDSourceFunc(func() (string, error) {
		value := ids[index]
		index++
		return value, nil
	})}
	task, err := service.Create(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return task
}

func updateTaskTitle(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, taskID, title string) {
	t.Helper()
	service := core.Service{Config: config, Store: repository, Actor: "test@example.test", Now: func() time.Time { return time.Date(2026, time.July, 26, 12, 1, 0, 0, time.UTC) }, IDs: core.IDSourceFunc(func() (string, error) {
		return "01K0M6B8A4FTT8C39MXXYTW7D7", nil
	})}
	if _, err := service.Update(context.Background(), taskID, core.UpdateInput{Title: &title}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
}

func validProjectionDatabase(t *testing.T, version, projectID string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projection.sqlite")
	if err := writeDatabaseMetadata(path, version, projectID); err != nil {
		t.Fatalf("writeDatabaseMetadata() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(foreign cache) error = %v", err)
	}
	return contents
}

func writeDatabaseMetadata(path, version, projectID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, version, projectID)
	return err
}

func writeIncompleteProjectionDatabase(path, projectID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, schemaVersion, projectID)
	return err
}
