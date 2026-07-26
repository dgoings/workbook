# SQLite Projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve Workbook's normal task reads from a disposable SQLite projection that incrementally follows local Git task-ref tips and can be atomically rebuilt.

**Architecture:** `gitstore.Repository` remains the canonical Git object/ref boundary and exposes only the projection-safe operations needed to enumerate task heads and read a validated changed tip. A new `internal/projection` package owns SQLite schema, cache validation, incremental refresh, atomic rebuild, and a read-only `core.TaskStore` implementation. CLI read commands use this store; all mutations continue to use the existing raw Git store.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` pure-Go driver, `database/sql`, Git CLI plumbing, and existing core/CLI integration tests.

## Global Constraints

- Git operation commits and task refs remain canonical; SQLite is never canonical.
- Cache path is `<git-common-dir>/workbook/cache.sqlite`, shared by linked worktrees, ignored, and never synchronized.
- Ordinary reads enumerate ref tips for invalidation but use SQLite task data when the tip set matches.
- Changed tips are validated through existing Git-store logic before projection.
- Mutations, fetch, push, and sync remain Git-backed.
- Rebuild writes a temporary database and renames it atomically; a failure preserves the old cache.
- Use a pure-Go driver: source installation must not require CGO.

---

### Task 1: Expose projection-safe Git task-head access

**Files:**

- Modify: `internal/gitstore/read.go`
- Modify: `internal/gitstore/read_test.go`

**Interfaces:**

- Produces `gitstore.TaskHead { TaskID string; ObjectID string }`.
- Produces `func (r *Repository) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]TaskHead, error)`.
- Produces `func (r *Repository) ReadTaskHead(ctx context.Context, config core.ProjectConfig, head TaskHead) (core.Snapshot, error)`.
- `ListTaskHeads` validates repository/config identity and returns lexical task-ID order; `ReadTaskHead` preserves the existing full tip validation.

- [ ] **Step 1: Write the failing Git-store integration test**

```go
func TestListTaskHeadsAndReadTaskHead(t *testing.T) {
    repository, config := initializedRepository(t)
    first := createTask(t, repository, config, "First")
    second := createTask(t, repository, config, "Second")

    heads, err := repository.ListTaskHeads(context.Background(), config)
    if err != nil { t.Fatalf("ListTaskHeads() error = %v", err) }
    got := []string{heads[0].TaskID, heads[1].TaskID}
    want := []string{first.ID, second.ID}
    if !reflect.DeepEqual(got, want) { t.Fatalf("heads = %v, want %v", got, want) }

    snapshot, err := repository.ReadTaskHead(context.Background(), config, heads[0])
    if err != nil { t.Fatalf("ReadTaskHead() error = %v", err) }
    if snapshot.Head != heads[0].ObjectID || snapshot.State.TaskID != heads[0].TaskID {
        t.Fatalf("snapshot = %#v, want head %q for %q", snapshot, heads[0].ObjectID, heads[0].TaskID)
    }
}
```

- [ ] **Step 2: Run it to verify RED**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -run TestListTaskHeadsAndReadTaskHead -count=1`

Expected: FAIL because `ListTaskHeads` and `ReadTaskHead` are absent.

- [ ] **Step 3: Implement the minimal access boundary**

```go
type TaskHead struct {
    TaskID   string
    ObjectID string
}

func (r *Repository) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]TaskHead, error) {
    // verify identity/config, call the existing ref enumerator, sort by task ID,
    // and translate its private ref records into TaskHead values.
}

func (r *Repository) ReadTaskHead(ctx context.Context, config core.ProjectConfig, head TaskHead) (core.Snapshot, error) {
    // verify identity/config then delegate to the existing validated readTip.
}
```

Refactor `Repository.List` to call these methods without changing its public result/error contract.

- [ ] **Step 4: Run focused and package tests**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitstore/read.go internal/gitstore/read_test.go
git commit -m "feat: expose task heads for projection"
```

### Task 2: Build the disposable SQLite projection and its read-only task store

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/projection/store.go`
- Create: `internal/projection/schema.go`
- Create: `internal/projection/store_test.go`

**Interfaces:**

- Consumes `*gitstore.Repository`, `core.ProjectConfig`, `gitstore.TaskHead`, and `core.Snapshot`.
- Produces `func Open(ctx context.Context, repository *gitstore.Repository, config core.ProjectConfig) (*Store, error)`.
- Produces `func (s *Store) Refresh(ctx context.Context) error`, `func (s *Store) Rebuild(ctx context.Context) (int, error)`, and `func (s *Store) CachePath() string`.
- `Store` implements `core.TaskStore`; its `List`, `Get`, and `Resolve` are SQLite-backed, while `Write` returns a clear operational error because the store is read-only.
- Internally, `taskHeadSource` supplies `ListTaskHeads` and `ReadTaskHead`; production uses an adapter around `*gitstore.Repository`, and tests use a counting fake without a Git process.

- [ ] **Step 1: Write failing projection behavior tests**

```go
func TestStoreRefreshUsesSQLiteUntilATaskHeadChanges(t *testing.T) {
    root := testrepo.New(t)
    repository, config := initializeWorkbook(t, root)
    created := createTask(t, repository, config, "Initial title")

    store, err := projection.Open(context.Background(), repository, config)
    if err != nil { t.Fatalf("Open() error = %v", err) }
    first, err := store.List(context.Background(), config)
    if err != nil || first[0].State.Task.Title != "Initial title" {
        t.Fatalf("first projection = %#v, %v", first, err)
    }

    updateTaskTitle(t, repository, config, created.ID, "Changed title")
    second, err := store.List(context.Background(), config)
    if err != nil || second[0].State.Task.Title != "Changed title" {
        t.Fatalf("incremental projection = %#v, %v", second, err)
    }

    if err := os.Remove(store.CachePath()); err != nil { t.Fatal(err) }
    rebuilt, err := store.List(context.Background(), config)
    if err != nil || rebuilt[0].State.Task.Title != "Changed title" {
        t.Fatalf("recreated projection = %#v, %v", rebuilt, err)
    }
}
```

Add a narrow fake task-head source to assert that an unchanged second refresh makes zero `ReadTaskHead` calls, while advancing one head makes exactly one.

- [ ] **Step 2: Run it to verify RED**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/projection -run TestStoreRefreshUsesSQLiteUntilATaskHeadChanges -count=1`

Expected: FAIL because `internal/projection` and `projection.Open` do not exist.

- [ ] **Step 3: Add the pure-Go driver and schema**

Run: `go get modernc.org/sqlite`

Create schema version 1 with these normalized tables:

```sql
CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  history_generation TEXT NOT NULL, logical_clock INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL,
  priority TEXT NOT NULL, rank TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, deleted INTEGER NOT NULL
);
CREATE TABLE task_labels (
  task_id TEXT NOT NULL, label TEXT NOT NULL, PRIMARY KEY (task_id, label)
);
CREATE TABLE task_dependencies (
  task_id TEXT NOT NULL, dependency_id TEXT NOT NULL,
  PRIMARY KEY (task_id, dependency_id)
);
```

Store both `schema_version=1` and the validated `project_id` in metadata.

- [ ] **Step 4: Implement incremental refresh and task queries**

```go
func (s *Store) Refresh(ctx context.Context) error {
    heads, err := s.source.ListTaskHeads(ctx, s.config)
    if err != nil { return err }
    if !s.metaMatches(ctx) {
        _, err := s.Rebuild(ctx)
        return err
    }
    return s.refreshChangedHeads(ctx, heads)
}

func (s *Store) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
    if err := s.validateConfig(config); err != nil { return nil, err }
    if err := s.Refresh(ctx); err != nil { return nil, err }
    return s.querySnapshots(ctx)
}
```

Use a transaction per incremental refresh: delete a changed task's label/dependency rows, upsert its task row, and insert sorted collection rows. Remove rows for disappeared refs in the same transaction. Reconstruct `core.StateDocument` from SQLite, including labels/dependencies in lexical order; snapshots need only `Head` and `State` for current core read operations. Reproduce the existing case-insensitive prefix-resolution and ambiguity behavior in SQLite. `Rebuild` closes the old database handle before rename and reopens the replacement on success; if temporary construction fails, the original open handle and file remain usable.

- [ ] **Step 5: Add and satisfy cache-recovery tests**

```go
func TestStoreRebuildsMalformedOrWrongProjectDatabase(t *testing.T) {
    // Replace cache.sqlite with non-SQLite bytes, then a valid SQLite file
    // whose project_id differs. Each List must replace it and return canonical
    // tasks from local Git.
}
```

Absent, malformed, schema-incompatible, and wrong-project databases must be replaced automatically. A bad Git tip must remain a Git data error; it must not serve an old cache as current.

- [ ] **Step 6: Verify GREEN**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/projection -count=1 && GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/projection
git commit -m "feat: add SQLite task projection"
```

### Task 3: Add atomic rebuild and route normal reads through the projection

**Files:**

- Modify: `internal/cli/run.go`
- Modify: `internal/cli/terminal.go`
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run_test.go`
- Modify: `internal/cli/flags_test.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `README.md`

**Interfaces:**

- Produces `workbook rebuild [--json]` with `{ "taskCount": <number>, "cachePath": <path> }` inside the existing versioned result envelope.
- Produces `openReadService(ctx, cwd) (core.Service, error)` for read-only CLI paths.
- `openService` continues to build the raw Git-backed mutation service.

- [ ] **Step 1: Write failing CLI tests for rebuild and cache-backed readers**

```go
func TestRunRebuildProducesVersionedResult(t *testing.T) {
    repository := initializedRepository(t)
    createTaskThroughCLI(t, repository, "Projected")

    code, stdout, stderr := run(t, repository, "rebuild", "--json")
    if code != 0 || stderr != "" {
        t.Fatalf("rebuild = (%d, %q, %q)", code, stdout, stderr)
    }
    assertResult(t, stdout, "rebuild", map[string]any{"taskCount": float64(1)})
}
```

Add tests that `workbook help rebuild` is valid and that `list`, `show`, `board`, `next`, and web `GET /api/tasks` observe an advanced Git tip through the projection without changing their output contracts.

- [ ] **Step 2: Run it to verify RED**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestRunRebuildProducesVersionedResult|TestCommandHelp' -count=1`

Expected: FAIL with unknown command `rebuild`.

- [ ] **Step 3: Implement read-service composition and rebuild command metadata**

```go
type rebuildResult struct {
    TaskCount int    `json:"taskCount"`
    CachePath string `json:"cachePath"`
}

func openReadService(ctx context.Context, cwd string) (core.Service, error) {
    repository, config, err := openRepository(ctx, cwd)
    if err != nil { return core.Service{}, err }
    store, err := projection.Open(ctx, repository, config)
    if err != nil { return core.Service{}, err }
    return core.Service{Config: config, Store: store, IDs: core.CryptoULIDSource{}, Now: time.Now}, nil
}
```

Add `rebuild` to the dispatch switch, metadata map, and ordered help list. Route `runList`, `runShow`, `runNext`, and `runBoard` through `openReadService`. In `runServe`, construct a cached read service solely for list closures and retain a separate raw Git service for create/update closures. Human rebuild output is `Rebuilt <count> task(s) at <path>.`.

- [ ] **Step 4: Write failing atomic rebuild tests**

```go
func TestRebuildLeavesPreviousDatabaseWhenReplacementFails(t *testing.T) {
    // Build a known-good cache, inject failure before rename, then verify a
    // normal read still has the prior valid projected data.
}

func TestRebuildRetriesOnceWhenHeadsChangeDuringBuild(t *testing.T) {
    // Advance a task head after the first enumeration. The installed database
    // must contain the second head. Advance again and require an error.
}
```

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/projection -run 'TestRebuildLeavesPreviousDatabaseWhenReplacementFails|TestRebuildRetriesOnceWhenHeadsChangeDuringBuild' -count=1`

Expected: FAIL until rebuild uses a temporary database, validates heads again, retries once, and atomically replaces the cache.

- [ ] **Step 5: Implement atomic replacement and verify GREEN**

```go
func (s *Store) Rebuild(ctx context.Context) (int, error) {
    for attempt := 0; attempt < 2; attempt++ {
        heads, err := s.source.ListTaskHeads(ctx, s.config)
        if err != nil { return 0, err }
        temporary, err := s.buildTemporaryDatabase(ctx, heads)
        if err != nil { return 0, err }
        current, err := s.source.ListTaskHeads(ctx, s.config)
        if err != nil { os.Remove(temporary); return 0, err }
        if equalHeads(heads, current) {
            return len(heads), s.replaceAtomically(temporary)
        }
        os.Remove(temporary)
    }
    return 0, core.Errorf(core.CategoryOperational, "task refs changed during projection rebuild; retry workbook rebuild")
}
```

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli ./internal/webui ./internal/projection -count=1 && GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Document the implemented cache behavior**

Update README status, architecture table, command list, cache location, automatic validation/refresh, explicit rebuild, and disposal guarantee. Do not claim full-text search or arbitrary SQL support.

- [ ] **Step 7: Commit**

```bash
git add README.md internal/cli internal/webui
git commit -m "feat: query tasks from SQLite projection"
```

### Task 4: Verify the completed feature and update task state

**Files:**

- Modify: only files corrected by verification.
- Test: all Go packages and source installation script.

- [ ] **Step 1: Run static checks**

```bash
gofmt -w internal/gitstore/read.go internal/gitstore/read_test.go internal/projection/*.go internal/cli/*.go
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Expected: no remaining formatter change, no vet diagnostics, and no whitespace errors.

- [ ] **Step 2: Run the full test suite**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1`

Expected: PASS.

- [ ] **Step 3: Verify a source-installed executable**

```bash
destination=$(mktemp -d)
./scripts/install.sh "$destination"
"$destination/workbook" rebuild --json
"$destination/workbook" board --narrow
```

Expected: source installation succeeds without CGO, rebuild returns versioned JSON, and board renders tasks.

- [ ] **Step 4: Review conformance**

Confirm Git remains canonical; unchanged reads only enumerate tips; changed tips are validated; writes stay Git-backed; and a failed rebuild preserves the prior cache.

- [ ] **Step 5: Mark the task In Review after all verification passes**

Run: `GOCACHE=/private/tmp/workbook-gocache go run ./cmd/workbook update WB-01KY899D7TM3PSDFM2EH08V14X --status in-review --json`

Expected: the Workbook result envelope reports status `in-review`. If verification discovers a defect, fix it with a focused test-first commit, repeat Steps 1-4, then run this status update.
