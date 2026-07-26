package projection

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
	_ "modernc.org/sqlite"
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
