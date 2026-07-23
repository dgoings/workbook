# Workbook CRUD and Dogfooding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the first working Workbook vertical slice: installable Go CLI, repository initialization, Git-backed task CRUD, human and JSON output, and Workbook tasks that track the remaining POC.

**Architecture:** `cmd/workbook` delegates parsing and presentation to `internal/cli`, which calls an application service in `internal/core`. Core owns identifiers, validation, operation application, deterministic checkpoints, and CRUD use cases behind a narrow `TaskStore` interface. `internal/gitstore` is the only package that invokes Git; it stores one `operation.json` and one `state.json` in each task commit and advances `refs/workbook/tasks/<task-id>` with compare-and-swap.

**Tech Stack:** Go 1.26, Go standard library, `github.com/oklog/ulid/v2` v2.1.1, Git 2.50 or newer for integration tests.

## Global Constraints

- Work only on the isolated `feat/initial-poc` branch; do not implement on `main`.
- The POC is local-only: do not fetch, push, install hooks, add a daemon, or change the current code branch.
- Workbook supports exactly one project per Git repository.
- Project keys contain two to ten uppercase ASCII letters or digits and begin with a letter; the default is `WB`.
- Task IDs are canonical uppercase `<project-key>-<26-character-ULID>` strings; command lookup accepts an unambiguous case-insensitive prefix.
- Canonical task refs exist only at `refs/workbook/tasks/<task-id>`; enumerate them with `git for-each-ref`, never by reading `.git/refs`.
- Every accepted task commit has exactly `operation.json` and `state.json`; ordinary POC mutations have one parent and create has none.
- Durable JSON uses versioned structs, UTF-8, stable struct field order, no insignificant whitespace, sorted label/dependency sets, and one trailing line feed.
- `operation.json` is authoritative intent; `state.json` must equal deterministic application of the pack to its parent state.
- Task refs advance with `git update-ref --create-reflog` and an exact expected old object ID; stale writes fail without retry.
- Git object IDs are opaque strings; never assume SHA-1 length.
- Deletion is a tombstone operation; normal CRUD never deletes a task ref.
- Core code does not execute Git commands; the Git package does not own CLI presentation.
- All production behavior is written test-first and each task ends with a focused commit.
- The verification gate is `gofmt -w`, `go vet ./...`, and `go test ./...`.

---

### Task 1: Go module, domain identifiers, values, and typed errors

**Files:**
- Create: `go.mod`
- Create: `internal/core/id.go`
- Create: `internal/core/id_test.go`
- Create: `internal/core/task.go`
- Create: `internal/core/task_test.go`
- Create: `internal/core/errors.go`
- Create: `internal/core/errors_test.go`

**Interfaces:**
- Consumes: no application code.
- Produces: `core.IDSource`, `core.CryptoULIDSource`, `core.ProjectConfig`, `core.TaskData`, `core.Task`, `core.Status`, `core.Priority`, `core.ValidateProjectKey`, `core.ValidateTaskID`, `core.NormalizeTask`, `core.Category`, `core.Error`, and `core.ExitCode`.

- [ ] **Step 1: Create the module and add the pinned ULID dependency**

Create `go.mod` with:

```go
module github.com/dgoings/workbook

go 1.26

require github.com/oklog/ulid/v2 v2.1.1
```

Run: `go mod download`

Expected: exit 0 and a generated `go.sum`.

- [ ] **Step 2: Write failing identifier and value tests**

Add table-driven tests that require:

```go
func TestValidateProjectKey(t *testing.T) {
    valid := []string{"WB", "A1", "WORKBOOK10"}
    invalid := []string{"", "A", "1A", "wb", "WORKBOOK123"}
    // Every valid key returns nil; every invalid key returns CategoryValidation.
}

func TestValidateTaskID(t *testing.T) {
    const id = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"
    // The exact canonical ID is accepted.
    // Lowercase, the wrong key, a missing separator, and an invalid ULID fail.
}

func TestNormalizeTaskSortsSetsAndValidatesValues(t *testing.T) {
    task := TaskData{
        Title: "  Build Git store  ",
        Status: StatusReady,
        Priority: PriorityHigh,
        Labels: []string{"poc", "git", "git"},
        Rank: "2/1",
        Dependencies: []string{
            "WB-01K0M6B8A4FTT8C39MXXYTW7C3",
            "WB-01K0M6B8A4FTT8C39MXXYTW7C3",
        },
    }
    got, err := NormalizeTask("WB", task)
    // got.Title == "Build Git store"
    // got.Labels == []string{"git", "poc"}
    // got.Dependencies contains one canonical ID.
}
```

Also test all five statuses, all three priorities, blank titles, malformed ranks, and `ExitCode` mappings: invocation 2, not initialized 3, not found 4, validation 5, stale write 6, corrupt/unsupported data 7, and unexpected failures 1.

Run: `go test ./internal/core -run 'TestValidate|TestNormalize|TestExitCode'`

Expected: FAIL because the `core` types and functions do not exist.

- [ ] **Step 3: Implement the domain types and validation**

Use these public shapes:

```go
type IDSource interface {
    New() (string, error)
}

type IDSourceFunc func() (string, error)

func (f IDSourceFunc) New() (string, error) {
    return f()
}

type CryptoULIDSource struct {
    Now     func() time.Time
    Entropy io.Reader
}

func (s CryptoULIDSource) New() (string, error)

type ProjectConfig struct {
    Format    string `json:"format"`
    Version   int    `json:"version"`
    ProjectID string `json:"projectId"`
    Key       string `json:"key"`
}

type Status string
const (
    StatusBacklog    Status = "backlog"
    StatusReady      Status = "ready"
    StatusInProgress Status = "in-progress"
    StatusBlocked    Status = "blocked"
    StatusDone       Status = "done"
)

type Priority string
const (
    PriorityLow    Priority = "low"
    PriorityMedium Priority = "medium"
    PriorityHigh   Priority = "high"
)

type TaskData struct {
    Title        string    `json:"title"`
    Description  string    `json:"description"`
    Status       Status    `json:"status"`
    Priority     Priority  `json:"priority"`
    Labels       []string  `json:"labels"`
    Rank         string    `json:"rank"`
    Dependencies []string  `json:"dependencies"`
    CreatedAt    time.Time `json:"createdAt"`
    UpdatedAt    time.Time `json:"updatedAt"`
    Deleted      bool      `json:"deleted"`
}

type Task struct {
    ID                string   `json:"id"`
    ProjectID         string   `json:"projectId"`
    TaskData
    HistoryGeneration string   `json:"historyGeneration"`
    Head              string   `json:"head"`
}
```

`CryptoULIDSource.New` calls `ulid.New(ulid.Timestamp(now.UTC()), entropy)` with `time.Now` and `crypto/rand.Reader` defaults. `ValidateProjectKey` uses `^[A-Z][A-Z0-9]{1,9}$`. `ValidateTaskID` checks the exact key prefix and `ulid.ParseStrict`. `NormalizeTask` trims the title, validates enum/rank values, rejects empty labels, validates dependencies, and returns sorted de-duplicated copies. The first-slice rank grammar is canonical positive integer rationals `^[1-9][0-9]*/1$`; the ordering plan will extend the implementation before `move`.

Implement typed errors with:

```go
type Category string
const (
    CategoryInvocation     Category = "invalid-invocation"
    CategoryNotInitialized Category = "not-initialized"
    CategoryNotFound       Category = "not-found"
    CategoryValidation     Category = "validation"
    CategoryStaleWrite     Category = "stale-write"
    CategoryCorruptData    Category = "corrupt-data"
)

type Error struct {
    Category Category
    Message  string
    Cause    error
}

func (e *Error) Error() string
func (e *Error) Unwrap() error
func Errorf(category Category, format string, args ...any) error
func Wrap(category Category, message string, cause error) error
func CategoryOf(err error) Category
func ExitCode(err error) int
```

- [ ] **Step 4: Verify the task**

Run:

```sh
gofmt -w internal/core
go test ./internal/core
go vet ./internal/core
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```sh
git add go.mod go.sum internal/core
git commit -m "feat: define workbook task domain"
```

---

### Task 2: Durable operation packs and deterministic state application

**Files:**
- Create: `internal/core/operation.go`
- Create: `internal/core/operation_test.go`
- Create: `internal/core/encoding.go`
- Create: `internal/core/encoding_test.go`

**Interfaces:**
- Consumes: `ProjectConfig`, `TaskData`, `Task`, enums, validators, and `IDSource` from Task 1.
- Produces: `core.Actor`, `core.Operation`, `core.OperationPack`, `core.History`, `core.StateDocument`, `core.EncodeDocument`, `core.Apply`, and `core.ValidateCheckpoint`.

- [ ] **Step 1: Write failing transition tests**

Use fixed IDs and times to test:

```go
func TestApplyCreateUpdateAndTombstone(t *testing.T) {
    create := OperationPack{
        Format: "workbook.operation-pack", Version: 1,
        ProjectID: projectID, TaskID: taskID,
        HistoryGeneration: generationID,
        Actor: Actor{ID: "developer@example.com"},
        LogicalClock: 1, WallTime: createdAt,
        Operations: []Operation{{
            ID: operationID1, Type: OperationTaskCreate,
            Task: &TaskData{
                Title: "Build Git store", Description: "",
                Status: StatusBacklog, Priority: PriorityMedium,
                Labels: []string{}, Rank: "1/1", Dependencies: []string{},
                CreatedAt: createdAt, UpdatedAt: createdAt, Deleted: false,
            },
        }},
    }
    state, err := Apply(nil, create, "WB")
    // Assert root fields, generation, clock, and task equality.

    update := OperationPack{
        Format: "workbook.operation-pack", Version: 1,
        ProjectID: projectID, TaskID: taskID,
        HistoryGeneration: generationID,
        Actor: Actor{ID: "developer@example.com"},
        LogicalClock: 2, WallTime: updatedAt,
        Operations: []Operation{
            {ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "ready"},
            {ID: operationID3, Type: OperationSetAdd, Field: "labels", Value: "git"},
        },
    }
    state, err = Apply(&state, update, "WB")
    // Assert ready, label git, logical clock 2, and unchanged createdAt.
}
```

Add separate tests proving:

- create rejects a parent;
- an ordinary update requires one parent and `parent.LogicalClock + 1`;
- project ID, task ID, and generation mismatches fail as corrupt data;
- `field.set` supports only title, description, status, priority, and rank;
- `set.add`/`set.remove` support only labels and dependencies;
- repeated adds and missing removes are idempotent;
- tombstone sets `Deleted`, preserves the task, and prevents further mutation;
- unknown formats, versions, operation types, and malformed values fail;
- `ValidateCheckpoint` rejects a byte-different computed state.

Run: `go test ./internal/core -run 'TestApply|TestValidateCheckpoint'`

Expected: FAIL because operation application does not exist.

- [ ] **Step 2: Implement versioned operations and application**

Use these durable structs and constants:

```go
type Actor struct {
    ID string `json:"id"`
}

type OperationType string
const (
    OperationTaskCreate    OperationType = "task.create"
    OperationFieldSet      OperationType = "field.set"
    OperationSetAdd        OperationType = "set.add"
    OperationSetRemove     OperationType = "set.remove"
    OperationTaskTombstone OperationType = "task.tombstone"
)

type Operation struct {
    ID    string        `json:"id"`
    Type  OperationType `json:"type"`
    Field string        `json:"field,omitempty"`
    Value string        `json:"value,omitempty"`
    Task  *TaskData     `json:"task,omitempty"`
}

type OperationPack struct {
    Format             string      `json:"format"`
    Version            int         `json:"version"`
    ProjectID          string      `json:"projectId"`
    TaskID             string      `json:"taskId"`
    HistoryGeneration  string      `json:"historyGeneration"`
    Actor               Actor       `json:"actor"`
    LogicalClock        uint64      `json:"logicalClock"`
    WallTime            time.Time   `json:"wallTime"`
    Operations          []Operation `json:"operations"`
}

type History struct {
    Generation    string  `json:"generation"`
    CompactedFrom *string `json:"compactedFrom"`
}

type StateDocument struct {
    Format       string   `json:"format"`
    Version      int      `json:"version"`
    ProjectID    string   `json:"projectId"`
    TaskID       string   `json:"taskId"`
    History      History  `json:"history"`
    LogicalClock uint64   `json:"logicalClock"`
    Task         TaskData `json:"task"`
}
```

`Apply(parent *StateDocument, pack OperationPack, projectKey string) (StateDocument, error)` validates the envelope before applying operations in slice order to a value copy. It sets `UpdatedAt` from `pack.WallTime`, normalizes the final task, and never compares wall time for ordering. `ValidateCheckpoint(parent, pack, stored, key)` calls `Apply`, canonical-encodes both states, and returns corrupt-data when bytes differ.

- [ ] **Step 3: Write the failing deterministic encoding test**

```go
func TestEncodeDocumentUsesCanonicalJSON(t *testing.T) {
    state := StateDocument{
        Format: "workbook.task-state", Version: 1,
        ProjectID: projectID, TaskID: taskID,
        History: History{Generation: generationID, CompactedFrom: nil},
        LogicalClock: 1,
        Task: TaskData{
            Title: "Build Git store", Description: "",
            Status: StatusBacklog, Priority: PriorityMedium,
            Labels: []string{"poc", "git"}, Rank: "1/1",
            Dependencies: []string{}, CreatedAt: createdAt,
            UpdatedAt: createdAt, Deleted: false,
        },
    }
    got, err := EncodeDocument(state)
    // Assert one compact line, one trailing LF, labels sorted as ["git","poc"],
    // and byte-for-byte equality across repeated calls.
}
```

Run: `go test ./internal/core -run TestEncodeDocumentUsesCanonicalJSON`

Expected: FAIL because `EncodeDocument` does not exist.

- [ ] **Step 4: Implement deterministic encoding and verify**

`EncodeDocument(value any) ([]byte, error)` copies and normalizes known durable document types, rejects unsupported input types, calls `json.Marshal`, and appends `'\n'`. Add strict `DecodeOperationPack` and `DecodeStateDocument` helpers using `json.Decoder.DisallowUnknownFields`, requiring EOF after one JSON value and validating the format/version.

Run:

```sh
gofmt -w internal/core
go test ./internal/core
go vet ./internal/core
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```sh
git add internal/core
git commit -m "feat: apply durable task operations"
```

---

### Task 3: Repository discovery and idempotent Workbook initialization

**Files:**
- Create: `internal/gitstore/repository.go`
- Create: `internal/gitstore/repository_test.go`
- Create: `internal/gitstore/config.go`
- Create: `internal/gitstore/config_test.go`
- Create: `internal/testrepo/repository.go`

**Interfaces:**
- Consumes: `core.ProjectConfig`, `core.IDSource`, typed errors, and document encoding.
- Produces: `gitstore.Repository`, `gitstore.Open`, `(*Repository).Init`, `(*Repository).LoadConfig`, `(*Repository).Actor`, and `testrepo.New`.

- [ ] **Step 1: Write failing real-Git initialization tests**

Create `testrepo.New(t)` to run `git init`, set `user.name=Workbook Test` and `user.email=workbook@example.test`, and return the path. Tests must cover:

```go
func TestInitCreatesTrackedConfigAndPrivateCache(t *testing.T) {
    repoDir := testrepo.New(t)
    repo, err := Open(context.Background(), repoDir)
    fixed := core.IDSourceFunc(func() (string, error) {
        return "01K0M65GBZ8F5ZQX0VC1J8H3TP", nil
    })
    config, created, err := repo.Init(context.Background(), "WB", fixed)
    // Assert created, exact config fields, mode 0644, one trailing LF,
    // <repo>/.git/workbook exists, and no refs/workbook/tasks entries.
}
```

Add tests for:

- opening from a nested working-tree directory;
- calling init twice with the same key returns `created=false` and identical bytes;
- a conflicting key fails with validation and does not rewrite the file;
- malformed/foreign format, version, project ID, or key fails as corrupt data;
- init outside Git fails as not initialized;
- `Actor` returns the repository's `user.email`.

Run: `go test ./internal/gitstore -run 'TestOpen|TestInit|TestActor'`

Expected: FAIL because repository initialization does not exist.

- [ ] **Step 2: Implement Git command execution and repository discovery**

Use:

```go
type Repository struct {
    Root         string
    CommonGitDir string
    gitPath      string
}

func Open(ctx context.Context, startDir string) (*Repository, error)
func (r *Repository) Git(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
func (r *Repository) Actor(ctx context.Context) (string, error)
```

`Open` resolves `git` with `exec.LookPath`, runs `git -C <start> rev-parse --show-toplevel` and `git -C <start> rev-parse --path-format=absolute --git-common-dir`, cleans both returned paths, and never changes process working directory. `Git` always invokes `git -C r.Root`.

- [ ] **Step 3: Implement deterministic configuration**

Use:

```go
const configPath = ".workbook/config.json"

func (r *Repository) Init(ctx context.Context, key string, ids core.IDSource) (core.ProjectConfig, bool, error)
func (r *Repository) LoadConfig() (core.ProjectConfig, error)
```

New configuration is:

```go
core.ProjectConfig{
    Format: "workbook.project",
    Version: 1,
    ProjectID: generatedULID,
    Key: key,
}
```

Write it via a temporary file in `.workbook`, `Sync`, close, chmod 0644, and `os.Rename`; create `<CommonGitDir>/workbook` with mode 0755. Existing valid configuration is returned unchanged only when its key matches. Strictly decode and validate existing bytes before any write.

- [ ] **Step 4: Verify the task**

Run:

```sh
gofmt -w internal/gitstore internal/testrepo
go test ./internal/gitstore
go vet ./internal/gitstore ./internal/testrepo
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```sh
git add internal/gitstore internal/testrepo
git commit -m "feat: initialize workbook repositories"
```

---

### Task 4: Git task object writes, compare-and-swap, and reflogs

**Files:**
- Create: `internal/core/store.go`
- Create: `internal/gitstore/write.go`
- Create: `internal/gitstore/write_test.go`

**Interfaces:**
- Consumes: opened repository, project config, operation/state documents, canonical encoding, and checkpoint validation.
- Produces: `core.Snapshot`, `core.TaskStore`, `(*Repository).Write`, and Git-backed atomic ref creation/update.

- [ ] **Step 1: Define the storage boundary and write failing integration tests**

Add:

```go
type Snapshot struct {
    Head      string
    Operation OperationPack
    State     StateDocument
}

type TaskStore interface {
    List(context.Context, ProjectConfig) ([]Snapshot, error)
    Get(context.Context, ProjectConfig, string) (Snapshot, error)
    Resolve(context.Context, ProjectConfig, string) (string, error)
    Write(context.Context, ProjectConfig, *Snapshot, OperationPack, StateDocument, string) (Snapshot, error)
}
```

The parent snapshot is `nil` only for create. For an update, its `Head` is the
exact expected old ref value and its `State` is the input to checkpoint
validation.

Write real-Git tests proving that `Repository.Write`:

- creates a root commit with no parents and exactly two regular blobs;
- stores bytes equal to `core.EncodeDocument(pack)` and `core.EncodeDocument(state)`;
- updates the canonical task ref to the returned opaque head;
- creates a reflog whose message begins `workbook:`;
- appends a one-parent commit when supplied the current head;
- rejects a stale expected head with `CategoryStaleWrite`;
- leaves the canonical ref unchanged after rejection;
- validates operation/state equality before writing any objects or refs.

Run: `go test ./internal/gitstore -run TestWrite`

Expected: FAIL because `Write` does not exist.

- [ ] **Step 2: Implement Git object plumbing**

Implement:

```go
func (r *Repository) Write(
    ctx context.Context,
    config core.ProjectConfig,
    parent *core.Snapshot,
    pack core.OperationPack,
    state core.StateDocument,
    reason string,
) (core.Snapshot, error)
```

The method:

1. validates project/task/ref identity and `ValidateCheckpoint`;
2. calls `git check-ref-format refs/workbook/tasks/<task-id>`;
3. writes each document with `git hash-object -w --stdin`;
4. creates a tree with `git mktree` input containing sorted `operation.json` and `state.json` entries;
5. creates a commit with `git commit-tree <tree> [-p <parent.Head>] -m <reason>`;
6. calls `git update-ref --create-reflog -m "workbook: <reason>" <ref> <newHead> <parent.Head>`;
7. maps an update-ref old-value mismatch to `CategoryStaleWrite`.

Pass the empty string as the expected old value for creation; do not manufacture
a zero OID. For updates, call `ValidateCheckpoint(&parent.State, ...)` and use
the same parent head for the commit parent and ref compare-and-swap. Return the
supplied documents with the new head only after the ref update succeeds.

- [ ] **Step 3: Verify the task**

Run:

```sh
gofmt -w internal/core internal/gitstore
go test ./internal/gitstore -run TestWrite
go test ./internal/core ./internal/gitstore
go vet ./internal/core ./internal/gitstore
```

Expected: all commands exit 0.

- [ ] **Step 4: Commit**

```sh
git add internal/core/store.go internal/gitstore/write.go internal/gitstore/write_test.go
git commit -m "feat: persist task operation commits"
```

---

### Task 5: Validated task discovery, direct reads, and prefix resolution

**Files:**
- Create: `internal/gitstore/read.go`
- Create: `internal/gitstore/read_test.go`

**Interfaces:**
- Consumes: `core.TaskStore`, task documents, repository Git runner, and project configuration.
- Produces: `(*Repository).List`, `(*Repository).Get`, `(*Repository).Resolve`, strict tip validation, and packed-ref-safe discovery.

- [ ] **Step 1: Write failing read/discovery integration tests**

Use documents written through Task 4 and prove:

- `Get` reads the tip `operation.json` and `state.json` without replaying parents;
- `List` returns every canonical task sorted by full ID;
- `Resolve` accepts full IDs and unambiguous case-insensitive prefixes;
- no match returns not-found and multiple matches return validation;
- `git pack-refs --all` does not change discovery results;
- nested refs below a task ID, invalid task IDs, non-commit targets, extra/missing tree entries, malformed JSON, operation/state identity mismatch, foreign project IDs, wrong project-key prefixes, history-generation mismatch, and logical-clock mismatch all fail as corrupt data;
- refs under `refs/workbook/remotes/` are never listed.

Run: `go test ./internal/gitstore -run 'TestGet|TestList|TestResolve'`

Expected: FAIL because read methods do not exist.

- [ ] **Step 2: Implement exact-prefix enumeration and strict tip reads**

`List` runs:

```text
git for-each-ref --format=%(refname)%00%(objectname) refs/workbook/tasks/
```

Parse each newline-terminated record around its NUL separator. Require exactly one path component after the prefix and validate every discovered entry; do not silently skip owned malformed refs.

`Get` verifies the object is a commit with `git cat-file -e <oid>^{commit}`, requires exactly `operation.json` and `state.json` from `git ls-tree`, reads them with `git show <oid>:<name>`, strictly decodes both, and verifies project ID, task ID, generation, clock, and ref suffix agreement. It does not traverse parent commits.

`Resolve` lowercases only for comparison, returns the stored canonical full ID, and obtains candidates from validated `List`.

- [ ] **Step 3: Verify the task**

Run:

```sh
gofmt -w internal/gitstore
go test ./internal/gitstore
go vet ./internal/gitstore
```

Expected: all commands exit 0.

- [ ] **Step 4: Commit**

```sh
git add internal/gitstore/read.go internal/gitstore/read_test.go
git commit -m "feat: discover and read workbook tasks"
```

---

### Task 6: Core CRUD service and deterministic list filtering

**Files:**
- Create: `internal/core/service.go`
- Create: `internal/core/service_test.go`

**Interfaces:**
- Consumes: `TaskStore`, `IDSource`, clock function, domain validators, operation application, and project configuration.
- Produces: `core.Service`, `core.CreateInput`, `core.UpdateInput`, `core.ListFilter`, and `Create`, `List`, `Show`, `Update`, `Delete`.

- [ ] **Step 1: Write failing service tests with an in-memory store**

Define the service API:

```go
type Service struct {
    Config ProjectConfig
    Store  TaskStore
    IDs    IDSource
    Now    func() time.Time
    Actor  string
}

type CreateInput struct {
    Title       string
    Description string
    Status      Status
    Priority    Priority
    Labels      []string
}

type UpdateInput struct {
    Title       *string
    Description *string
    Status      *Status
    Priority    *Priority
    Labels      *[]string
}

type ListFilter struct {
    Status   *Status
    Priority *Priority
    Label    string
    All      bool
}
```

Tests must prove:

- create generates separate task, history-generation, and operation ULIDs;
- create defaults to backlog/medium/empty sets and appends rank after the highest task in the same status/priority bucket;
- create writes one `task.create` pack at clock 1 and returns the new head;
- list omits tombstones unless `All`, filters by status/priority/label, and sorts by status sequence, priority high/medium/low, numeric rank, then ID;
- show resolves an unambiguous prefix and returns tombstoned tasks;
- update emits only changed `field.set`, `set.add`, and `set.remove` operations in deterministic field/value order;
- a no-op update returns validation without writing;
- update uses the observed head and increments the logical clock once per CLI mutation;
- delete emits one tombstone and returns the tombstoned task;
- updating/deleting an already tombstoned task fails validation;
- store stale-write errors propagate without retry.

Run: `go test ./internal/core -run 'TestService|TestList'`

Expected: FAIL because `Service` does not exist.

- [ ] **Step 2: Implement CRUD pack construction and projection**

Implement:

```go
func (s Service) Create(ctx context.Context, input CreateInput) (Task, error)
func (s Service) List(ctx context.Context, filter ListFilter) ([]Task, error)
func (s Service) Show(ctx context.Context, idOrPrefix string) (Task, error)
func (s Service) Update(ctx context.Context, idOrPrefix string, input UpdateInput) (Task, error)
func (s Service) Delete(ctx context.Context, idOrPrefix string) (Task, error)
```

Convert snapshots to projected tasks with:

```go
func Project(snapshot Snapshot) Task {
    return Task{
        ID: snapshot.State.TaskID,
        ProjectID: snapshot.State.ProjectID,
        TaskData: snapshot.State.Task,
        HistoryGeneration: snapshot.State.History.Generation,
        Head: snapshot.Head,
    }
}
```

Build all requested update operations before one call to `Apply` and one call to `Store.Write`. Sort scalar operations by field name; sort label removes and adds lexicographically. Ranks in this slice are sequential `<max+1>/1` values inside the create task's status/priority bucket.

- [ ] **Step 3: Verify the task**

Run:

```sh
gofmt -w internal/core
go test ./internal/core
go vet ./internal/core
```

Expected: all commands exit 0.

- [ ] **Step 4: Commit**

```sh
git add internal/core/service.go internal/core/service_test.go
git commit -m "feat: add git-backed task CRUD"
```

---

### Task 7: CLI, versioned JSON envelopes, installer, and end-to-end CRUD

**Files:**
- Create: `cmd/workbook/main.go`
- Create: `internal/cli/run.go`
- Create: `internal/cli/run_test.go`
- Create: `internal/cli/flags.go`
- Create: `internal/cli/output.go`
- Create: `scripts/install.sh`
- Create: `scripts/install_test.go`

**Interfaces:**
- Consumes: repository open/init/config/actor, `core.Service`, all CRUD inputs, typed errors, and projections.
- Produces: runnable `workbook` with `init`, `create`, `list`, `show`, `update`, and `delete`; stable JSON success/error envelopes; local installer.

- [ ] **Step 1: Write failing CLI integration tests**

Use:

```go
func Run(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) int
```

Exercise a real temporary Git repository and cover:

```text
workbook init [--key WB]
workbook create "Title" [--description text] [--status value] [--priority value] [--label value ...] [--json]
workbook list [--status value] [--priority value] [--label value] [--all] [--json]
workbook show <id-or-prefix> [--json]
workbook update <id-or-prefix> [--title value] [--description value] [--status value] [--priority value] [--label value ...] [--json]
workbook delete <id-or-prefix> [--json]
```

Tests must prove:

- no command and unknown commands return invalid-invocation exit 2;
- commands other than init before configuration return not-initialized exit 3;
- init is idempotent and prints repository, project ID, key, and task count;
- successful mutations print the resulting task;
- human list shows ID, title, status, priority, and labels;
- JSON success is one compact line with:

```json
{"format":"workbook.result","version":1,"command":"create","data":{}}
```

- JSON failure is one compact line on stderr with:

```json
{"format":"workbook.error","version":1,"error":{"category":"validation","message":"title is required"}}
```

- JSON mode never truncates or writes human prose;
- all stable category exit codes are honored;
- the full init/create/list/show/update/delete/list-all lifecycle succeeds;
- repeated `--label` replaces the complete label set on update, including an explicit empty set from `--clear-labels`;
- positional title/ID must be first after the subcommand, matching the documented syntax.

Run: `go test ./internal/cli -run TestRun`

Expected: FAIL because the CLI package does not exist.

- [ ] **Step 2: Implement command parsing and application wiring**

`Run` parses the subcommand without a third-party CLI framework. Each command gets a fresh `flag.FlagSet` with `ContinueOnError`; a custom repeated-string value handles labels. For create/update/show/delete, remove and validate the required first positional argument before parsing remaining flags.

Open the repository from `cwd`; `init` calls `Repository.Init`. Other commands call `LoadConfig`, `Actor`, construct `core.Service` with `core.CryptoULIDSource{}` and `time.Now`, then invoke one use case.

Use:

```go
type ResultEnvelope struct {
    Format  string `json:"format"`
    Version int    `json:"version"`
    Command string `json:"command"`
    Data    any    `json:"data"`
}

type ErrorBody struct {
    Category core.Category `json:"category"`
    Message  string        `json:"message"`
}

type ErrorEnvelope struct {
    Format  string    `json:"format"`
    Version int       `json:"version"`
    Error   ErrorBody `json:"error"`
}
```

Human mutation output is one tab-separated row `ID STATUS PRIORITY TITLE`; human list has a header and adds comma-joined labels. `show` prints labeled full fields without truncation. All errors go to stderr. `cmd/workbook/main.go` only calls `os.Exit(cli.Run(...))`.

- [ ] **Step 3: Write the failing installer test**

The Go test runs `scripts/install.sh <temp-bin-dir>`, then executes `<temp-bin-dir>/workbook` without arguments and asserts exit 2 plus usage text. It also sets a restricted PATH in separate subtests to prove missing `go` and missing `git` produce actionable failures.

Run: `go test ./scripts -run TestInstall`

Expected: FAIL because the installer does not exist.

- [ ] **Step 4: Implement the installer**

Create executable `scripts/install.sh` that:

1. uses `set -eu`;
2. checks `command -v go` and `command -v git`;
3. accepts zero or one destination argument, defaulting to `${HOME}/.local/bin`;
4. creates the destination;
5. runs `go build -trimpath -o "${destination}/workbook" ./cmd/workbook` from the repository root;
6. prints the installed path;
7. prints `export PATH="<destination>:$PATH"` when the destination is not already a PATH component.

- [ ] **Step 5: Verify the complete vertical slice**

Run:

```sh
chmod +x scripts/install.sh
gofmt -w cmd internal scripts
go test ./...
go vet ./...
go build ./cmd/workbook
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit**

```sh
git add cmd internal/cli scripts
git commit -m "feat: expose workbook CRUD CLI"
```

---

### Task 8: Dogfood Workbook and align current documentation

**Files:**
- Create: `.workbook/config.json` through the built CLI.
- Modify: `README.md`
- Test: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: the runnable CLI and canonical Git storage from Tasks 1–7.
- Produces: one initialized Workbook project in this repository, durable refs for the remaining POC work, and README examples that distinguish implemented local behavior from proposed remote behavior.

- [ ] **Step 1: Add a failing regression test for the README's implemented command block**

Add a test that reads `README.md` from the repository root and requires a section headed `## Implemented POC commands` containing exactly the six implemented command names and a separate `## Proposed post-POC commands` section containing remote claim/sync examples.

Run: `go test ./internal/cli -run TestREADMEImplementedCommands`

Expected: FAIL because README still says all formats and commands are unimplemented.

- [ ] **Step 2: Build a scratch executable and initialize this repository**

Run:

```sh
mkdir -p .superpowers/bin
go build -o .superpowers/bin/workbook ./cmd/workbook
.superpowers/bin/workbook init --key WB --json
```

Expected: JSON success with a new immutable project ID, key `WB`, and task count 0. The tracked `.workbook/config.json` is created in this worktree.

- [ ] **Step 3: Create the remaining POC work as Workbook tasks**

Run these commands in order:

```sh
.superpowers/bin/workbook create "Add task ordering, dependencies, and next selection" --description "Implement exact rational ranks, move/depend/undepend, cycle rejection, and deterministic workbook next." --status ready --priority high --label poc --label ordering --json
.superpowers/bin/workbook create "Add disposable SQLite projection" --description "Build cache.sqlite from task tip checkpoints, refresh changed heads, and support atomic workbook rebuild." --status ready --priority high --label poc --label sqlite --json
.superpowers/bin/workbook create "Render terminal task table and ASCII board" --description "Add consistent wide and narrow terminal views using core task ordering without truncating JSON." --status ready --priority medium --label poc --label cli --json
.superpowers/bin/workbook create "Serve the read-only web Kanban board" --description "Embed assets, expose GET /, /api/tasks, and /healthz, poll task state, and shut down gracefully." --status ready --priority medium --label poc --label web --json
.superpowers/bin/workbook create "Complete POC acceptance coverage and documentation" --description "Cover history replay, cache reconstruction, SHA variants, renderer parity, HTTP routes, installer behavior, and final README alignment." --status backlog --priority medium --label poc --label testing --json
```

Expected: five successful JSON envelopes with five distinct task IDs and heads.

- [ ] **Step 4: Verify dogfooded state through the public CLI**

Run:

```sh
.superpowers/bin/workbook list --json
git for-each-ref --format='%(refname) %(objectname)' refs/workbook/tasks/
```

Expected: the list contains exactly five nondeleted tasks; Git prints exactly five canonical task refs.

For every printed ref, run:

```sh
git ls-tree --name-only <object-id>
```

Expected: exactly `operation.json` and `state.json`.

- [ ] **Step 5: Update README to current behavior**

Change the status note to say local initialization and CRUD are implemented in the POC branch while ordering, SQLite, terminal board, web board, and remote synchronization remain proposed. Add:

```text
## Implemented POC commands

workbook init
workbook create
workbook list
workbook show
workbook update
workbook delete

## Proposed post-POC commands
```

Keep remote claim, fetch/push/sync, conflict reconciliation, and Homebrew examples clearly labeled proposed. Update the storage diagram and prose so every operation commit contains both `operation.json` and `state.json`, and normal current-state reads use the tip checkpoint.

- [ ] **Step 6: Verify the slice**

Run:

```sh
gofmt -w cmd internal scripts
go test ./...
go vet ./...
go build ./cmd/workbook
git diff --check
.superpowers/bin/workbook list --json
```

Expected: all commands exit 0 and the final command returns exactly five tasks.

- [ ] **Step 7: Commit**

```sh
git add .workbook/config.json README.md internal/cli/run_test.go
git commit -m "docs: dogfood workbook CRUD"
```

Do not add `.superpowers/`, compiled binaries, task refs, or Git object files to the commit.
