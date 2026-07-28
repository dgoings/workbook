# Optimize Git-Durable Local Mutations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Workbook create and mutation commands use a bounded synchronous Git path while planning from validated SQLite tip state and preserving Git as the success boundary.

**Architecture:** A configured `gitstore.Repository` caches process-stable repository metadata, inspects exact refs directly, and batches changed-tip object reads. The core mutation service receives separate projection-backed reader, Git-backed canonical writer, and projection-update boundaries; after a successful Git compare-and-swap it conditionally advances SQLite or returns a nonfatal structured cache warning. Full IDs avoid namespace enumeration, while create, prefix resolution, move, and dependency-cycle checks refresh the global head set and read Git objects only for unknown or changed heads.

**Tech Stack:** Go 1.26+, installed Git plumbing (`for-each-ref`, `cat-file --batch`, `hash-object`, `mktree`, `commit-tree`, and `update-ref`), SQLite through `modernc.org/sqlite`, Workbook's existing `core`, `gitstore`, `projection`, `cli`, `webui`, and `perf` packages.

## Global Constraints

- A successful CLI command or HTTP mutation means the canonical task ref has advanced synchronously in Git.
- SQLite remains a disposable projection; direct SQL writes to canonical task state remain unsupported.
- The current task ref tip's complete `state.json` is sufficient for ordinary mutation planning; ordinary mutations never replay all historical operations.
- A warm exact-ID mutation performs one exact-ref inspection followed by two blob writes, one tree write, one commit write, and one compare-and-swap ref update.
- Full IDs do not enumerate `refs/workbook/tasks/`; prefixes refresh the global head set before resolving ambiguity.
- Create refreshes the global head set before calculating rank from SQLite.
- Global and cross-task refreshes batch changed-tip object reads; Git command count does not grow once per unchanged task.
- Backward, sideways, missing, nested, or symbolic task-ref movement is reported as conflict or corruption rather than accepted through the cache.
- A projected snapshot advances only when the cached head equals the expected parent or already equals the new head; an older request never overwrites a newer snapshot.
- Ordinary reads and independent per-task projection updates are not serialized by one process-wide mutex; full rebuild and database replacement retain an exclusive path.
- If Git succeeds and projection advancement fails, the mutation remains successful, the affected cache row is conditionally invalidated, human CLI output warns on standard error, and JSON and HTTP output contain a structured warning.
- Git refs are accessed through Git commands, never `.git/refs` files, and no code assumes SHA-1 length; SHA-256 remains supported when installed Git supports it.
- Reflog messages retain the existing exact `workbook:` prefix and display-text sanitization behavior.
- Ordinary tests assert behavior and Git-command cardinality, not elapsed-time thresholds.
- The post-implementation benchmark is run once. A miss or timeout is reported as evidence and does not trigger automatic tuning or a replacement run.
- Durable SQLite outboxes, asynchronous Git reconciliation, required daemons, Git libraries, and persistent Git writer processes remain deferred.

---

## File Structure

- `internal/gitstore/repository.go` owns repository discovery, the Git command runner, command observation for tests, and cached identity, configuration, and actor metadata.
- `internal/gitstore/repository_test.go` proves process-stable metadata is read once and command observation does not change Git behavior.
- `internal/gitstore/read.go` owns task-ref enumeration, exact task-ref inspection, movement validation, and public read orchestration.
- `internal/gitstore/batch.go` owns `git cat-file --batch` request/response parsing, commit/tree/blob decoding, and multi-head snapshot validation without hash-length assumptions.
- `internal/gitstore/read_test.go` covers exact refs, symbolic/nested refs, batched reads, changed-head ancestry, corrupt objects, SHA-1, and supported SHA-256.
- `internal/gitstore/write.go` retains the defensive standalone `Write` path and adds the bounded `WriteValidated` path used after an observed validated projection read.
- `internal/gitstore/write_test.go` proves defensive writes still reject forged parents, validated writes retain CAS safety, and the validated write uses five Git commands.
- `internal/projection/store.go` owns exact-ID refresh, global changed-head refresh, concurrent reads, conditional projection advancement, invalidation, and exclusive rebuild.
- `internal/projection/store_test.go` proves exact reads do not enumerate, unchanged heads avoid object reads, changed heads batch, independent updates do not regress, and invalidation is conditional.
- `internal/core/store.go` owns separate `TaskReader`, `CanonicalTaskWriter`, and `ProjectionUpdater` interfaces plus mutation warning/result types.
- `internal/core/service.go` plans mutations through `TaskReader`, writes through `CanonicalTaskWriter`, and treats projection failures as nonfatal warnings.
- `internal/core/service_test.go` covers direct full-ID resolution, prefix/global refresh behavior through reader calls, split-boundary orchestration, and Git-success/projection-failure semantics.
- `internal/cli/run.go` composes one repository and one projection into read and mutation services and renders mutation warnings.
- `internal/cli/output.go` adds optional warnings to JSON result envelopes and human warning rendering on standard error.
- `internal/cli/run_test.go` covers split-store composition plus human and JSON cache-warning output without changing successful exit status.
- `internal/webui/handler.go` carries `core.MutationResult` through mutation callbacks and adds optional warnings to `workbook.task-mutation`.
- `internal/webui/handler_test.go` proves HTTP 200 with a durable task and structured cache warning.
- `README.md` documents synchronous Git durability, targeted projection refresh, and nonfatal projection warnings without claiming target latency has been achieved.

### Task 1: Cache process-stable repository metadata

**Files:**
- Modify: `internal/gitstore/repository.go`
- Modify: `internal/gitstore/config.go`
- Modify: `internal/gitstore/repository_test.go`
- Modify: `internal/gitstore/config_test.go`

**Interfaces:**
- Produces: `type gitCommandObserver func([]string)`
- Produces: `func (r *Repository) observeGitCommand(args []string)`
- Preserves: `func Open(context.Context, string) (*Repository, error)`
- Preserves: `func (r *Repository) LoadConfig() (core.ProjectConfig, error)`
- Preserves: `func (r *Repository) Actor(context.Context) (string, error)`
- Guarantees: repositories returned by `Open` do not rerun identity discovery; the first successful configuration and actor reads are cached for that `Repository`.

- [ ] **Step 1: Write failing metadata-cache and observer tests**

Add tests that count only commands run through the repository and assert the consumer-visible cached values:

```go
func TestRepositoryCachesProcessStableActor(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	repo.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	first, err := repo.Actor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Git(context.Background(), nil, "config", "user.email", "changed@example.test"); err != nil {
		t.Fatal(err)
	}
	second, err := repo.Actor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != "workbook@example.test" || second != first {
		t.Fatalf("actors = %q, %q", first, second)
	}
	if got := countCommand(commands, "config", "--get", "user.email"); got != 1 {
		t.Fatalf("actor config commands = %d, want 1", got)
	}
}
```

Add a configuration test that calls `LoadConfig` twice, changes the tracked file between calls, and proves the already-open repository retains its first validated configuration. Keep separate tests proving a newly opened repository detects tracked/guard mismatch.

- [ ] **Step 2: Run the focused tests and verify the new cache assertions fail**

Run: `go test ./internal/gitstore -run 'TestRepositoryCachesProcessStable|TestLoadConfigCaches' -count=1`

Expected: FAIL because `commandObserver` and the repository-level metadata caches do not exist and repeated actor/configuration reads are not cached.

- [ ] **Step 3: Add thread-safe successful-value caches**

Extend `Repository` with mutex-protected successful configuration state, a `sync.Once` actor result, an `identityVerified` flag set by `Open`, and the test observer:

```go
type Repository struct {
	Root         string
	CommonGitDir string
	gitPath      string

	metadataMu       sync.RWMutex
	identityVerified bool
	configLoaded     bool
	config           core.ProjectConfig
	actorOnce        sync.Once
	actor            string
	actorErr         error
	commandObserver  gitCommandObserver
}
```

`LoadConfig` must cache only a successful, guard-validated value. `Init` must remember the successful returned configuration on every successful return path. `Actor` must cache both its value and error for the repository session. `gitWithEnv` must call `observeGitCommand` with a defensive copy immediately before executing Git. `verifyIdentity` must return immediately for an already verified repository and remember a successful verification for a constructed repository.

- [ ] **Step 4: Run repository and configuration tests**

Run: `go test ./internal/gitstore -run 'Test(Open|Repository|Actor|LoadConfig|Init)' -count=1`

Expected: PASS, including invalid constructed repositories, linked worktrees, and conflicting project guards.

- [ ] **Step 5: Commit**

```bash
git add internal/gitstore/repository.go internal/gitstore/config.go internal/gitstore/repository_test.go internal/gitstore/config_test.go
git commit -m "perf: cache repository session metadata"
```

### Task 2: Inspect exact refs and batch current-tip reads

**Files:**
- Create: `internal/gitstore/batch.go`
- Modify: `internal/gitstore/read.go`
- Modify: `internal/gitstore/read_test.go`

**Interfaces:**
- Consumes: repository metadata and command observation from Task 1.
- Produces: `func (r *Repository) InspectTaskHead(context.Context, core.ProjectConfig, string) (TaskHead, bool, error)`
- Produces: `func (r *Repository) ReadTaskHeads(context.Context, core.ProjectConfig, []TaskHead) ([]core.Snapshot, error)`
- Produces: `type HeadAdvance struct { Previous core.Snapshot; Current TaskHead }`
- Produces: `func (r *Repository) ValidateTaskHeadAdvances(context.Context, core.ProjectConfig, []HeadAdvance) error`
- Preserves: `ListTaskHeads`, `ReadTaskHead`, `List`, `Get`, and `Resolve`.
- Guarantees: one `for-each-ref` command lists all heads, one `for-each-ref` command inspects one exact ref, and one `cat-file --batch` command reads any number of requested tips.

- [ ] **Step 1: Write failing exact-ref and batch-cardinality tests**

Add a test that creates two valid tasks, resets the observer, and verifies:

```go
heads, err := repository.ListTaskHeads(ctx, config)
if err != nil {
	t.Fatal(err)
}
snapshots, err := repository.ReadTaskHeads(ctx, config, heads)
if err != nil {
	t.Fatal(err)
}
if len(snapshots) != 2 {
	t.Fatalf("snapshots = %d, want 2", len(snapshots))
}
if got := countCommand(commands, "for-each-ref"); got != 1 {
	t.Fatalf("for-each-ref commands = %d, want 1", got)
}
if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
	t.Fatalf("cat-file --batch commands = %d, want 1", got)
}
```

Add separate tests proving `InspectTaskHead` rejects a symbolic exact ref, rejects nested entries under the exact name, returns `(TaskHead{}, false, nil)` for an absent valid ID, and does not enumerate or read unrelated task objects. Exercise the same batch reader in SHA-1 and SHA-256 repositories when SHA-256 initialization is supported.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/gitstore -run 'Test(InspectTaskHead|ReadTaskHeads|ListTaskHeads)' -count=1`

Expected: FAIL because the exact inspection and batched read APIs do not exist.

- [ ] **Step 3: Implement exact ref records with symbolic status**

Change the ref format to:

```go
const taskRefFormat = "%(refname)%00%(objectname)%00%(symref)"
```

Use a shared parser that validates the namespace, exact task ID, nonempty object ID, absence of nested entries, and blank `%(symref)`. `InspectTaskHead` must validate the full task ID before running Git and pass exactly `refs/workbook/tasks/<taskID>` to `for-each-ref`.

- [ ] **Step 4: Implement the `cat-file --batch` codec**

For every requested head, write these four revision expressions to one `git cat-file --batch` process:

```text
<head>
<head>^{tree}
<head>:operation.json
<head>:state.json
```

Parse each response as `<oid> <type> <size>\n<raw-bytes>\n`. Validate that the first object is a commit, the second is a tree, and the last two are blobs. Parse raw tree entries as `<mode> <name NUL><raw object id>` using `len(head.ObjectID)/2` bytes for the object ID so SHA-1 and SHA-256 both work. Require exactly regular-blob entries named `operation.json` and `state.json`. Reuse canonical document decoding, topology validation from the raw commit bytes, identity validation, and root checkpoint validation. Return snapshots in request order.

- [ ] **Step 5: Add bounded advancement validation**

`ValidateTaskHeadAdvances` must reject duplicate task IDs, mismatched task IDs, missing current heads, and any current head that is not a descendant of its previous validated head. Supply all new heads and exclusions to one `git rev-list --parents --stdin` invocation, parse the returned parent graph, and walk from each current head until its expected previous head is reached. It must not run `merge-base` once per task. The projection compares the batch-read current snapshots with each previous snapshot and rejects history-generation changes before caching them.

Add a test with two independently advanced tasks that observes exactly one `rev-list --parents --stdin` command, plus backward and sideways movement tests expecting `core.CategoryCorruptData`.

- [ ] **Step 6: Delegate existing read methods to the bounded primitives**

`ReadTaskHead` calls `ReadTaskHeads` with one head. `List` calls `ListTaskHeads` once and `ReadTaskHeads` once. `Get` calls `InspectTaskHead` once and `ReadTaskHeads` once. `Resolve` retains full namespace behavior because prefix ambiguity requires a global refresh.

- [ ] **Step 7: Run Git read tests**

Run: `go test ./internal/gitstore -run 'Test(List|Get|Resolve|Inspect|ReadTask|ValidateTaskHead)' -count=1`

Expected: PASS for valid, corrupt, symbolic, nested, SHA-1, and supported SHA-256 cases.

- [ ] **Step 8: Commit**

```bash
git add internal/gitstore/batch.go internal/gitstore/read.go internal/gitstore/read_test.go
git commit -m "perf: batch task tip reads"
```

### Task 3: Add exact and conditional projection paths

**Files:**
- Modify: `internal/projection/store.go`
- Modify: `internal/projection/store_test.go`

**Interfaces:**
- Consumes: `InspectTaskHead`, `ReadTaskHeads`, and `ValidateTaskHeadAdvances` from Task 2.
- Produces: `func (s *Store) Advance(context.Context, core.ProjectConfig, string, core.Snapshot) (bool, error)`
- Produces: `func (s *Store) Invalidate(context.Context, core.ProjectConfig, string, string, string) error`
- Preserves: `Refresh`, `Rebuild`, `List`, `Get`, `Resolve`, `CachePath`.
- Preserves temporarily: the unsupported projection `Write` method and `core.TaskStore` assertion so the repository compiles until all service constructors migrate in Task 6.
- Guarantees: warm exact `Get` performs one exact-head inspection and zero task-object reads; global refresh reads all changed tips in one batch.

- [ ] **Step 1: Replace source fakes with the exact/batch source contract and add failing behavior tests**

Change `taskHeadSource` to:

```go
type taskHeadSource interface {
	ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
	InspectTaskHead(context.Context, core.ProjectConfig, string) (gitstore.TaskHead, bool, error)
	ReadTaskHeads(context.Context, core.ProjectConfig, []gitstore.TaskHead) ([]core.Snapshot, error)
	ValidateTaskHeadAdvances(context.Context, core.ProjectConfig, []gitstore.HeadAdvance) error
}
```

Add tests that warm a cache and then assert:

```go
snapshot, err := store.Get(ctx, config, taskID)
if err != nil {
	t.Fatal(err)
}
if snapshot.Head != expected.Head {
	t.Fatalf("head = %q, want %q", snapshot.Head, expected.Head)
}
if source.listCalls != 0 || source.inspectCalls != 1 || source.batchReadCalls != 0 {
	t.Fatalf("source calls = list %d inspect %d batch %d", source.listCalls, source.inspectCalls, source.batchReadCalls)
}
```

Add changed-exact-head, absent-exact-head, changed-global-heads, conditional advance, already-advanced no-op, newer-head no-regression, and conditional invalidation tests.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/projection -run 'TestStore(Get|Refresh|Advance|Invalidate)' -count=1`

Expected: FAIL because exact inspection, batch reads, conditional advancement, and invalidation are not implemented.

- [ ] **Step 3: Separate rebuild exclusion from ordinary traffic**

Replace `mu sync.Mutex` with `rebuildMu sync.RWMutex`. `Rebuild` and atomic database replacement take the write lock. `Refresh`, `List`, `Get`, `Resolve`, `Advance`, and `Invalidate` take the read lock only while using the active database. A cache-missing or schema-mismatch check must release the read lock, acquire the write lock, recheck, and rebuild once; it must never attempt an RWMutex lock upgrade.

- [ ] **Step 4: Implement exact `Get`**

After ensuring the cache exists, query the projected row, inspect only the exact Git ref, and:

- return `CategoryNotFound` when neither Git nor SQLite contains the task;
- return the cached snapshot when the Git object ID matches;
- reject a disappeared ref when SQLite still contains it;
- validate advancement and batch-read the one changed tip when the IDs differ;
- conditionally apply the changed snapshot using the cached head observed before Git validation; and
- retry the exact operation once on `errStaleProjectionRefresh`.

Do not call `ListTaskHeads` from `Get`.

- [ ] **Step 5: Batch global changed-head refresh**

In `refreshChangedHeads`, build all `gitstore.HeadAdvance` values for changed cached tasks, validate them in one call, read all changed/unknown tips in one `ReadTaskHeads` call, reject any batch-read snapshot whose history generation differs from its cached predecessor, and apply the accepted snapshots in a short transaction. Unchanged heads produce neither advancement validation nor object reads.

- [ ] **Step 6: Implement conditional `Advance` and `Invalidate`**

`Advance(ctx, config, expectedParent, snapshot)` uses one short transaction. It returns `(false, nil)` when the cached row is already newer or otherwise differs from both `expectedParent` and `snapshot.Head`, `(true, nil)` after replacing the expected row, and `(true, nil)` when it already equals `snapshot.Head`.

`Invalidate(ctx, config, taskID, expectedParent, writtenHead)` deletes labels, dependencies, and task only when the current cached head equals either supplied head. It preserves any third/newer head. Both methods use `validateConfig` and wrap SQLite errors with the existing rebuild hint.

- [ ] **Step 7: Run projection tests, including concurrency**

Run: `go test ./internal/projection -count=1`

Expected: PASS, including `TestConcurrentRefreshCannotRegressProjectedHead` and new concurrent independent-task advancement coverage.

Run: `go test -race ./internal/projection -run 'Test(StoreGet|StoreRefresh|StoreAdvance|Concurrent)' -count=1`

Expected: PASS with no data races.

- [ ] **Step 8: Commit**

```bash
git add internal/projection/store.go internal/projection/store_test.go
git commit -m "perf: target projection refreshes"
```

### Task 4: Add the bounded canonical Git writer

**Files:**
- Modify: `internal/gitstore/write.go`
- Modify: `internal/gitstore/write_test.go`

**Interfaces:**
- Consumes: cached repository metadata from Task 1 and validated observed snapshots supplied by the projection flow.
- Produces: `func (r *Repository) WriteValidated(context.Context, core.ProjectConfig, *core.Snapshot, core.OperationPack, core.StateDocument, string) (core.Snapshot, error)`
- Preserves: defensive `Write`, which validates a caller-provided parent against Git before delegating to the common object/CAS writer.
- Guarantees: `WriteValidated` performs exactly five Git commands after its parent was observed: two `hash-object`, one `mktree`, one `commit-tree`, and one `update-ref`.

- [ ] **Step 1: Write failing command-cardinality and stale-CAS tests**

Add a successful update test that resets `commandObserver` after obtaining a validated parent and asserts:

```go
written, err := repo.WriteValidated(ctx, config, &parent, pack, state, "update task")
if err != nil {
	t.Fatal(err)
}
if written.Head == parent.Head {
	t.Fatal("validated write did not advance the task")
}
if got := len(commands); got != 5 {
	t.Fatalf("Git commands = %d, want 5: %#v", got, commands)
}
assertCommandSequence(t, commands, []string{
	"hash-object -w --stdin",
	"hash-object -w --stdin",
	"mktree",
	"commit-tree",
	"update-ref",
})
```

Add a test that advances the exact ref after observing the parent and before `WriteValidated`; expect `core.CategoryStaleWrite` and prove the concurrent head remains installed. Retain the existing forged-parent test against defensive `Write`.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/gitstore -run 'TestWriteValidated|TestWriteValidatesAgainstStateStoredAtParentHead' -count=1`

Expected: FAIL because `WriteValidated` does not exist.

- [ ] **Step 3: Extract pure validation and common object/CAS writing**

`WriteValidated` must validate configuration equality through the cached `LoadConfig`, document identity, task ID/ref construction, nonblank canonical parent head, and `core.ValidateCheckpoint` against the supplied validated parent. It must not call `verifyIdentity`, `check-ref-format`, `rev-parse`, `for-each-ref`, `symbolic-ref`, `ls-tree`, `show`, or `cat-file`.

The common writer retains the exact existing canonical document encoding, two blob writes, deterministic two-entry tree, commit subject, sanitized/prefixed reflog reason, and `update-ref --no-deref --create-reflog` compare-and-swap. On update-ref failure it may run diagnostic Git commands only on the failure path to distinguish stale-write and symbolic-ref errors.

- [ ] **Step 4: Keep defensive `Write` for untrusted direct callers**

Defensive `Write` obtains the exact current head, batch-reads and validates the stored parent, compares it to `parent.Head`, validates the checkpoint against stored state, and then delegates to the common writer. It must continue to reject forged parent state, malformed roots, noncanonical object IDs, symbolic refs, and concurrent changes before ref advancement.

- [ ] **Step 5: Run all write tests**

Run: `go test ./internal/gitstore -run 'TestWrite' -count=1`

Expected: PASS with the existing corruption and reflog contracts unchanged and the new five-command bound satisfied.

- [ ] **Step 6: Commit**

```bash
git add internal/gitstore/write.go internal/gitstore/write_test.go
git commit -m "perf: add bounded canonical writer"
```

### Task 5: Split core mutation boundaries and preserve Git-success semantics

**Files:**
- Modify: `internal/core/store.go`
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**
- Consumes: `WriteValidated` from Task 4 and `Advance`/`Invalidate` from Task 3.
- Produces:

```go
type TaskReader interface {
	List(context.Context, ProjectConfig) ([]Snapshot, error)
	Get(context.Context, ProjectConfig, string) (Snapshot, error)
	Resolve(context.Context, ProjectConfig, string) (string, error)
}

type CanonicalTaskWriter interface {
	WriteValidated(context.Context, ProjectConfig, *Snapshot, OperationPack, StateDocument, string) (Snapshot, error)
}

type ProjectionUpdater interface {
	Advance(context.Context, ProjectConfig, string, Snapshot) (bool, error)
	Invalidate(context.Context, ProjectConfig, string, string, string) error
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type MutationResult struct {
	Task     Task      `json:"task"`
	Warnings []Warning `json:"warnings,omitempty"`
}
```

- Produces: `const WarningProjectionUpdate = "projection-update-failed"`
- Produces: `CreateMutation`, `UpdateMutation`, `DeleteMutation`, `RestoreMutation`, `MoveMutation`, `DependMutation`, and `FreeMutation`, each returning `(MutationResult, error)`.
- Preserves: existing task-returning mutation methods as compatibility wrappers over the result methods until all internal callers are migrated.

- [ ] **Step 1: Write failing split-boundary and direct-ID tests**

Create reader, writer, and projection spies. Add a full-ID update test asserting `Resolve` is never called, `Get` is called once, `WriteValidated` receives that exact parent, and `Advance` receives `expectedParent == parent.Head`.

Add a prefix update test asserting one `Resolve` and one `Get`. Add create and move/depend tests proving their existing `List` calls remain, because rank and cycle calculations require globally refreshed projected rows.

- [ ] **Step 2: Write the failing nonfatal projection test**

Use a writer that returns a durable `written` snapshot, a projection whose `Advance` returns `errors.New("disk full")`, and an `Invalidate` spy. Assert:

```go
result, err := service.UpdateMutation(ctx, taskID, UpdateInput{Title: &title})
if err != nil {
	t.Fatalf("UpdateMutation() error = %v", err)
}
if result.Task.Head != written.Head {
	t.Fatalf("task head = %q, want durable Git head %q", result.Task.Head, written.Head)
}
if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningProjectionUpdate {
	t.Fatalf("warnings = %#v", result.Warnings)
}
if invalidator.taskID != taskID || invalidator.expectedParent != parent.Head || invalidator.writtenHead != written.Head {
	t.Fatalf("invalidation = %#v", invalidator)
}
```

Also test that a Git writer error returns no successful result and never calls projection advancement.

- [ ] **Step 3: Run focused tests and verify they fail**

Run: `go test ./internal/core -run 'TestService(FullID|Prefix|MutationBoundaries|ProjectionFailure)' -count=1`

Expected: FAIL because the split interfaces and mutation result methods do not exist.

- [ ] **Step 4: Split the service dependencies**

Change `Service` to hold:

```go
type Service struct {
	Config     ProjectConfig
	Reader     TaskReader
	Writer     CanonicalTaskWriter
	Projection ProjectionUpdater
	Store      TaskStore // compatibility fallback removed in Task 6
	IDs        IDSource
	Now        func() time.Time
	Actor      string
}
```

Add private `taskReader` and `canonicalWriter` accessors that prefer the split fields and fall back to `Store` during this task. All read methods and mutation planning use the selected reader. Full-ID resolution first calls `ValidateTaskID`; a valid canonical full ID goes directly to `Reader.Get`, while any other input uses `Reader.Resolve` followed by `Reader.Get`. The fallback writer requires `Store` to implement `CanonicalTaskWriter`; update the core memory store accordingly.

- [ ] **Step 5: Implement mutation results and conditional projection advancement**

Each result-returning mutation plans exactly as the current task-returning method does, then calls `Writer.WriteValidated`. After Git success, call `Projection.Advance` when configured. If advancement fails, call `Projection.Invalidate` with task ID, expected parent head (blank for create), and written head, then return the durable task plus one warning:

```go
Warning{
	Code: WarningProjectionUpdate,
	Message: "Git mutation succeeded, but the SQLite cache could not be updated; run `workbook rebuild` if the warning persists: " + err.Error(),
}
```

If invalidation also fails, append `"; cache invalidation also failed: " + invalidateErr.Error()` to the same warning message. Never return a mutation error after `WriteValidated` succeeds.

Compatibility wrappers call the result method and return `result.Task, err`; no production CLI or HTTP caller remains on these wrappers after Task 6.

- [ ] **Step 6: Update core service tests and memory stores**

Change `memoryTaskStore` into explicit reader, writer, and projection roles or implement the three interfaces on it. Existing behavior tests must keep their assertions for normalization, operations, ranks, dependencies, tombstones, restore, commit reasons, generated IDs, and no-op behavior.

- [ ] **Step 7: Run all core tests**

Run: `go test ./internal/core -count=1`

Expected: PASS with the old domain behavior and the new split-boundary/warning behavior.

- [ ] **Step 8: Commit**

```bash
git add internal/core/store.go internal/core/service.go internal/core/service_test.go
git commit -m "perf: split mutation read and write paths"
```

### Task 6: Compose the optimized CLI and HTTP mutation path

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run_test.go`
- Modify: `internal/webui/handler.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/core/store.go`
- Modify: `internal/core/service.go`
- Modify: `internal/projection/store.go`
- Modify: affected `internal/gitstore/*_test.go` and `internal/projection/store_test.go` service constructors

**Interfaces:**
- Consumes: `core.MutationResult` and the split service boundaries from Task 5.
- Produces: `ResultEnvelope.Warnings []core.Warning` with `json:"warnings,omitempty"`.
- Produces: `TaskMutationDocument.Warnings []core.Warning` with `json:"warnings,omitempty"`.
- Changes web mutation callback types to return `(core.MutationResult, error)`.
- Guarantees: human CLI warning goes to stderr with exit code 0; JSON warning is structured on stdout with empty stderr; HTTP warning is structured with status 200.

- [ ] **Step 1: Write failing CLI warning rendering tests**

Add output tests for:

```go
result := core.MutationResult{
	Task: core.Task{ID: taskID, Title: "Durable", Status: core.StatusReady, Priority: core.PriorityHigh},
	Warnings: []core.Warning{{Code: core.WarningProjectionUpdate, Message: "cache update failed"}},
}
```

Human mode must retain the existing one-line task output on stdout and write `workbook: warning: cache update failed\n` to stderr. JSON mode must produce `workbook.result` with `data` equal to the task, a top-level `warnings` array, empty stderr, and successful exit status.

- [ ] **Step 2: Write failing HTTP warning test**

Construct a handler mutation callback returning the same `MutationResult`, send a mutation request, and assert HTTP 200 plus:

```json
{
  "format": "workbook.task-mutation",
  "version": 1,
  "task": {"id": "WB-01K0M6B8A4FTT8C39MXXYTW7D1"},
  "warnings": [
    {"code": "projection-update-failed", "message": "cache update failed"}
  ]
}
```

Existing success responses without warnings must omit `warnings`.

- [ ] **Step 3: Run focused CLI and web tests and verify they fail**

Run: `go test ./internal/cli ./internal/webui -run 'Test.*Warning' -count=1`

Expected: FAIL because mutation results and structured warnings are not wired through these surfaces.

- [ ] **Step 4: Compose one mutation service**

Replace `openService` with an optimized constructor that calls `openRepository` once, reads actor once, opens one `projection.Store`, and returns:

```go
core.Service{
	Config:     config,
	Reader:     store,
	Writer:     repository,
	Projection: store,
	IDs:        core.CryptoULIDSource{},
	Now:        time.Now,
	Actor:      actor,
}
```

`openReadService` uses `Reader: store`. `runServe` must construct the repository/config/projection once and reuse one service for both reads and mutations rather than opening the repository twice.

After every constructor has migrated, remove `Service.Store`, its fallback accessors, the combined `TaskStore` interface, the projection's unsupported `Write` method, and its `core.TaskStore` assertion. Keep only the three explicit interfaces.

- [ ] **Step 5: Render CLI mutation results**

Pass `stderr` into mutation command runners. Human mode calls existing `writeMutation(stdout, result.Task)` and writes each warning to stderr. JSON mode calls a warning-aware result writer that keeps `data` as the task and places warnings at envelope top level. Errors remain on stderr and keep existing exit codes.

- [ ] **Step 6: Render HTTP mutation results**

Update callback types, handlers, and `writeTaskMutation` to carry `core.MutationResult`. The response status stays 200 because Git already succeeded. Existing error-category status mapping remains unchanged.

- [ ] **Step 7: Update service constructors in tests**

All direct `core.Service` construction in `gitstore`, `projection`, and CLI tests must supply explicit `Reader`, `Writer`, and optional `Projection` values. Tests that intentionally exercise defensive `Repository.Write` continue to call it directly.

- [ ] **Step 8: Run CLI, web, projection, and Git-store tests**

Run: `go test ./internal/cli ./internal/webui ./internal/projection ./internal/gitstore -count=1`

Expected: PASS with existing command output and HTTP contracts unchanged when no warnings are present.

- [ ] **Step 9: Commit**

```bash
git add internal/core/store.go internal/core/service.go internal/cli/run.go internal/cli/output.go internal/cli/run_test.go internal/webui/handler.go internal/webui/handler_test.go internal/gitstore internal/projection/store.go internal/projection/store_test.go
git commit -m "perf: wire projection-backed mutations"
```

### Task 7: Verify cardinality, document behavior, and run one evaluation

**Files:**
- Modify: `internal/cli/run_test.go`
- Modify: `README.md`
- No committed benchmark output; the single evaluation writes under `/tmp`.

**Interfaces:**
- Consumes: the complete optimized local path from Tasks 1-6 and the existing `workbook-bench` executable.
- Produces: integration evidence that exact-ID mutation uses the split service and warnings preserve success.
- Documents: Git success boundary, disposable projection, targeted refresh, structured warning behavior, and unverified target status.

- [ ] **Step 1: Add a failing composition regression test**

Extend the service-construction test to assert the mutation service has:

```go
if _, ok := service.Reader.(*projection.Store); !ok {
	t.Fatalf("Reader = %T, want *projection.Store", service.Reader)
}
if _, ok := service.Writer.(*gitstore.Repository); !ok {
	t.Fatalf("Writer = %T, want *gitstore.Repository", service.Writer)
}
if service.Projection != service.Reader {
	t.Fatalf("Projection = %T, want the opened reader instance", service.Projection)
}
```

The test must also create then update a full task ID, delete it, and restore it through CLI entry points, proving each command succeeds and the ref head changes once per mutation.

- [ ] **Step 2: Run the regression test and verify it fails before the final test update**

Run: `go test ./internal/cli -run 'TestOpen.*Service|TestRunExactMutationPath' -count=1`

Expected: FAIL until the constructor assertions and exact create/update/delete/restore integration sequence match the optimized composition.

- [ ] **Step 3: Complete the integration assertions**

Use real temporary Git repositories and the real projection. Derive expected heads with `git rev-parse refs/workbook/tasks/<taskID>` after each CLI command; do not read loose ref files. Keep latency out of the test.

- [ ] **Step 4: Update README**

Document that mutations read validated current state from SQLite, inspect the exact Git ref before planning, synchronously CAS the canonical Git ref, and then conditionally advance SQLite. State that a projection-update warning means the Git mutation succeeded and `workbook rebuild` is the recovery command. Retain the explicit statement that SQLite can be deleted and rebuilt, and do not claim the 100 ms, 200 ms, or one-second budgets were achieved.

- [ ] **Step 5: Run formatting, diagnostics, focused race tests, and the full suite**

Run:

```bash
gofmt -w internal/core/store.go internal/core/service.go internal/core/service_test.go internal/gitstore/repository.go internal/gitstore/config.go internal/gitstore/read.go internal/gitstore/batch.go internal/gitstore/write.go internal/gitstore/repository_test.go internal/gitstore/config_test.go internal/gitstore/read_test.go internal/gitstore/write_test.go internal/projection/store.go internal/projection/store_test.go internal/cli/run.go internal/cli/output.go internal/cli/run_test.go internal/webui/handler.go internal/webui/handler_test.go
go test -race ./internal/core ./internal/gitstore ./internal/projection ./internal/cli ./internal/webui -count=1
go test ./... -count=1
```

Expected: all commands PASS with no race reports. Check available editor/LSP diagnostics for every edited Go file and require zero errors.

- [ ] **Step 6: Commit the verified code and documentation**

```bash
git add README.md internal/cli/run_test.go
git commit -m "docs: explain optimized mutation durability"
```

- [ ] **Step 7: Run the post-implementation benchmark exactly once**

Build the branch binary, then run the existing minimum-size harness once with one sample and the same bounded command timeout as the baseline. Write only temporary evidence:

```bash
go build -o /tmp/workbook-local-mutation-target ./cmd/workbook
go run ./cmd/workbook-bench \
  --workbook /tmp/workbook-local-mutation-target \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --output-json /tmp/workbook-local-mutation-evaluation.json \
  --output-markdown /tmp/workbook-local-mutation-evaluation.md
```

Do not rerun, tune, or correct the implementation based on this command in the same delivery. If it completes, report every local mutation scenario's duration, Git process count, and target result, explicitly labeling one-sample p95 values as directional. If it times out or aborts, report the elapsed lower bound, failing setup/scenario, and error exactly.

- [ ] **Step 8: Record final branch evidence**

Run:

```bash
git status --short
git log --oneline --decorate "$(git merge-base main HEAD)..HEAD"
```

Expected: clean worktree and focused commits for metadata caching, batched reads, targeted projection refresh, bounded writes, split service orchestration, surface wiring, and documentation.
