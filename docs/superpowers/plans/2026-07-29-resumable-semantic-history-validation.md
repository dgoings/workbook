# Resumable Semantic History Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a foreground, resumable `workbook validate [--full] [--json]` audit that semantically verifies immutable task histories through bounded Git reads and validator-versioned disposable SQLite state.

**Architecture:** `gitstore` returns structurally validated root-to-head history records through three bounded batch commands. A new `internal/historyvalidation` package owns `validation.sqlite`, cache preparation, per-task resume transactions, and orchestration through `core.ValidateCheckpoint`. CLI and benchmark adapters depend on that package; ordinary projection and synchronization remain unchanged.

**Tech Stack:** Go 1.26, Git plumbing, `database/sql`, `modernc.org/sqlite`, Workbook's `core`, `gitstore`, `cli`, and `perf` packages.

## Global Constraints

- The command is exactly `workbook validate [--full] [--json]`.
- Default reads, fetch, push, and sync never run semantic history validation.
- Validation is foreground-only: no daemon, detached process, or background worker.
- Canonical and tracking Git refs are read-only throughout validation.
- SQLite is disposable and rebuildable from Git; it is never authoritative.
- The cache path is exactly `<git-common-dir>/workbook/validation.sqlite`.
- Validator format version starts at `1` and is independent of SQLite schema version.
- Normal validation inspects only unseen descendants after the last reachable cached valid ancestor.
- `--full` bypasses cached task results and semantic boundaries.
- Validation continues across task-local failures, caches an unchanged invalid head, reports every invalid task, and exits nonzero.
- Failure records contain exact `taskId`, full `commit`, `category`, and `message`.
- Task status values are exactly `pending`, `valid`, and `invalid`.
- JSON result fields are exactly `validatorVersion`, `full`, `taskCount`, `tasksChecked`, `commitsChecked`, `cacheHits`, `valid`, `invalid`, `pending`, `cachePath`, and `failures`.
- Acceptance uses at least 500 active tasks, exactly 20 operations per task, one sample, a 60-second timeout, and one run per supported SHA-1/SHA-256 format.
- Acceptance targets are: full audit at most 10 seconds; unchanged cached audit at most 500 milliseconds; five one-operation changes at most 1 second; every scenario fewer than 12 Git processes.
- Acceptance evidence is never tuned, rerun, or replaced. Correctness and a regular bounded-process witness are merge gates; a recorded wall-clock miss alone is not.

---

### Task 1: Bounded Git history transport

**Files:**

- Create: `internal/gitstore/history.go`
- Create: `internal/gitstore/history_test.go`
- Modify: `internal/gitstore/batch.go`
- Modify: `internal/gitstore/batch_test.go`

**Interfaces:**

- Consumes: `Repository.ListTaskHeads`, `readTaskHeadsPartial`, `parseParentGraph`, `validateCompleteParentGraph`, `core.Snapshot`, and `core.CategoryOf`.
- Produces:

```go
type TaskHistoryRequest struct {
    Head   TaskHead
    StopAt string
}

type HistoryCommit struct {
    ObjectID string
    Parents  []string
    Operation core.OperationPack
    State     core.StateDocument
}

type HistoryFailure struct {
    TaskID string
    Commit string
    Err    error
}

type TaskHistoryResult struct {
    TaskID           string
    Head             string
    BoundaryReached  bool
    Commits          []HistoryCommit
    CheckedCommits   int
    Failure          *HistoryFailure
}

func (r *Repository) ReadTaskHistories(
    ctx context.Context,
    config core.ProjectConfig,
    requests []TaskHistoryRequest,
) ([]TaskHistoryResult, error)
```

The result order must match request order. `Commits` is root/boundary-to-head
order and excludes `StopAt`. `CheckedCommits` includes the first structurally
invalid candidate commit. Per-task structural/document failures populate
`Failure`; command failure or batch framing failure returns the shared error.

- [ ] **Step 1: Write request, boundary, attribution, and command-count tests**

Add integration tests that create two independent histories with temporary Git
repositories and assert literal root-to-head object ID sequences. Before each
test body, name the production mutation it catches:

```go
func TestReadTaskHistoriesReturnsOnlyUnseenDescendantsInRequestOrder(t *testing.T)
func TestReadTaskHistoriesRestartsAtRootWhenBoundaryIsUnreachable(t *testing.T)
func TestReadTaskHistoriesAttributesMalformedCheckpointAndContinuesOtherTasks(t *testing.T)
func TestReadTaskHistoriesUsesConstantBatchedGitCommands(t *testing.T)
func TestReadTaskHistoriesSupportsSHA256ObjectIDs(t *testing.T)
```

The command-count test must use 10 tasks by 4 operations and 10 tasks by 7
operations, observe repository Git commands, and expect exactly:

```text
cat-file --batch: 2
rev-list --reverse --topo-order --parents --stdin: 1
```

It must reject `cat-file -t`, `show`, `ls-tree`, and per-task `rev-list`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run 'TestReadTaskHistories' -count=1 -v
```

Expected: compile failure because `TaskHistoryRequest` and
`ReadTaskHistories` do not exist.

- [ ] **Step 3: Implement request validation and one parent-graph batch**

Validate duplicate task IDs, canonical IDs, full object IDs, and optional
`StopAt` IDs before transport. Preserve request order. Batch current-tip reads
with `readTaskHeadsPartial`; exclude a tip with a task-local failure from the
shared graph.

Construct graph input with one full head per remaining request:

```go
for _, request := range graphRequests {
    fmt.Fprintln(&input, request.Head.ObjectID)
}
output, err := r.Git(
    ctx,
    input.Bytes(),
    "rev-list", "--reverse", "--topo-order", "--parents", "--stdin",
)
```

Parse and full-width validate the graph. Walk backward from each requested head:

- stop before `StopAt` and set `BoundaryReached`;
- if `StopAt` is not reachable, continue to the root and leave
  `BoundaryReached` false;
- reject a graph record with more than one parent at that exact commit;
- reverse the collected linear IDs for root-to-head object reads.

- [ ] **Step 4: Implement one partial object batch for all unseen commits**

Refactor only enough of `readTaskHeadsPartial` to accept the flattened
task/commit sequence in one call. Reuse `validateBatchSnapshot` so every commit
checks the commit object, raw tree, canonical documents, task/project/generation
identity, and root/ordinary topology.

Map each partial result back to its request. Append valid records until the
first failure for that task, set `HistoryFailure`, and continue collecting every
other task. A failure at a candidate commit increments `CheckedCommits`.

- [ ] **Step 5: Run focused and package tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add internal/gitstore/history.go internal/gitstore/history_test.go \
  internal/gitstore/batch.go internal/gitstore/batch_test.go
git commit -m "feat: read task histories in bounded batches"
```

---

### Task 2: Validator-versioned SQLite cache

**Files:**

- Create: `internal/historyvalidation/cache.go`
- Create: `internal/historyvalidation/cache_test.go`

**Interfaces:**

- Consumes: `gitstore.TaskHead`, `core.ProjectConfig`, canonical state bytes
  from `core.EncodeDocument`, and `<repository.CommonGitDir>/workbook`.
- Produces:

```go
const ValidatorVersion = 1

type Status string

const (
    StatusPending Status = "pending"
    StatusValid   Status = "valid"
    StatusInvalid Status = "invalid"
)

type Failure struct {
    TaskID   string `json:"taskId"`
    Commit   string `json:"commit"`
    Category string `json:"category"`
    Message  string `json:"message"`
}

type CachedTask struct {
    TaskID               string
    ObservedHead         string
    ValidatorVersion     int
    Status               Status
    LastValidCommit      string
    LastValidGeneration  string
    LastValidState       []byte
    ValidatedCommitCount int
    Failure              *Failure
}

type Completion struct {
    TaskID               string
    ObservedHead         string
    Status               Status
    LastValidCommit      string
    LastValidGeneration  string
    LastValidState       []byte
    ValidatedCommitIDs   []string
    ValidatedCommitCount int
    Failure              *Failure
    Full                 bool
}

func OpenCache(ctx context.Context, commonGitDir string, config core.ProjectConfig) (*Cache, error)
func (c *Cache) Path() string
func (c *Cache) Prepare(ctx context.Context, heads []gitstore.TaskHead, full bool) (map[string]CachedTask, error)
func (c *Cache) Record(ctx context.Context, completion Completion) error
func (c *Cache) Snapshot(ctx context.Context, taskIDs []string) ([]CachedTask, error)
func (c *Cache) Close() error
```

- [ ] **Step 1: Write cache lifecycle and resume tests**

Use real temporary SQLite databases, literal rows, and no SQL mocks:

```go
func TestPrepareMarksNewChangedAndVersionMismatchedHeadsPending(t *testing.T)
func TestPrepareFullMarksEveryObservedTaskPendingWithoutDiscardingOldCompletion(t *testing.T)
func TestRecordCommitsOneTaskValidOrInvalidResultAtomically(t *testing.T)
func TestRecordRejectsStaleObservedHead(t *testing.T)
func TestUnchangedInvalidHeadRetainsExactCachedFailure(t *testing.T)
func TestCompletedTasksSurviveCancellationAndPendingTasksResume(t *testing.T)
func TestOpenCacheRebuildsMissingIncompatibleForeignAndCorruptCaches(t *testing.T)
func TestOpenCacheUsesCommonGitDirectoryAcrossWorktrees(t *testing.T)
```

The mutation witnesses are: remove the pending transition, update without
`observed_head` in the predicate, omit one valid commit insert, or reuse a cache
with the wrong project/version.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/historyvalidation \
  -run 'TestPrepare|TestRecord|TestUnchanged|TestCompleted|TestOpenCache' \
  -count=1 -v
```

Expected: package or symbol-not-found failure.

- [ ] **Step 3: Implement schema and disposable recovery**

Open exactly:

```go
filepath.Join(commonGitDir, "workbook", "validation.sqlite")
```

Use a SQLite schema with:

```sql
CREATE TABLE validation_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE task_validation (
  task_id TEXT PRIMARY KEY,
  observed_head TEXT NOT NULL,
  validator_version INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('pending','valid','invalid')),
  last_valid_commit TEXT NOT NULL,
  last_valid_generation TEXT NOT NULL,
  last_valid_state BLOB NOT NULL,
  validated_commit_count INTEGER NOT NULL,
  failure_commit TEXT NOT NULL,
  failure_category TEXT NOT NULL,
  failure_message TEXT NOT NULL
);
CREATE TABLE validated_commits (
  validator_version INTEGER NOT NULL,
  commit_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  history_generation TEXT NOT NULL,
  PRIMARY KEY (validator_version, commit_id)
);
```

Set schema version `1`, project ID metadata, a busy timeout, foreign keys on,
and immediate transactions. Missing or unusable cache state is replaced with a
fresh empty database; no Git state changes.

- [ ] **Step 4: Implement preparation and compare-checked completion**

`Prepare` executes one immediate transaction:

- upsert new heads as pending;
- preserve unchanged current-version valid/invalid rows unless `full`;
- set changed/version-mismatched/full rows pending while retaining the prior
  valid boundary fields;
- delete task-status rows absent from the current inventory.

`Record` executes one immediate transaction and first checks:

```sql
SELECT observed_head, status
FROM task_validation
WHERE task_id = ?
```

The observed head must equal `Completion.ObservedHead` and the row must still be
pending. For `Full`, delete that task/version's old `validated_commits` only
inside the completion transaction. Insert every valid immutable commit, then
update the task row to valid or invalid with exact boundary/failure fields.

- [ ] **Step 5: Run focused and package tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/historyvalidation -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add internal/historyvalidation/cache.go internal/historyvalidation/cache_test.go
git commit -m "feat: cache resumable validation progress"
```

---

### Task 3: Semantic validator orchestration

**Files:**

- Create: `internal/historyvalidation/validator.go`
- Create: `internal/historyvalidation/validator_test.go`
- Modify: `internal/historyvalidation/cache.go`
- Modify: `internal/historyvalidation/cache_test.go`

**Interfaces:**

- Consumes: Task 1's `Repository.ReadTaskHistories`, Task 2's `Cache`,
  `core.ValidateCheckpoint`, and `core.EncodeDocument`.
- Produces:

```go
type Result struct {
    ValidatorVersion int       `json:"validatorVersion"`
    Full             bool      `json:"full"`
    TaskCount        int       `json:"taskCount"`
    TasksChecked     int       `json:"tasksChecked"`
    CommitsChecked   int       `json:"commitsChecked"`
    CacheHits        int       `json:"cacheHits"`
    Valid            int       `json:"valid"`
    Invalid          int       `json:"invalid"`
    Pending          int       `json:"pending"`
    CachePath        string    `json:"cachePath"`
    Failures         []Failure `json:"failures"`
}

type source interface {
    ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
    ReadTaskHistories(context.Context, core.ProjectConfig, []gitstore.TaskHistoryRequest) ([]gitstore.TaskHistoryResult, error)
}

func Open(ctx context.Context, repository *gitstore.Repository, config core.ProjectConfig) (*Validator, error)
func (v *Validator) Validate(ctx context.Context, full bool) (Result, error)
```

- [ ] **Step 1: Write semantic, cache-hit, interruption, and race tests**

Use a fake `source` only for orchestration branches and real Git integration for
the immutable-history contract:

```go
func TestValidateChecksEveryCheckpointAndCachesValidCommits(t *testing.T)
func TestValidateContinuesAllTasksAndReportsEveryFirstFailure(t *testing.T)
func TestValidateReusesUnchangedValidAndInvalidHeads(t *testing.T)
func TestValidateChangedHeadUsesReachableBoundaryAndChecksOnlyDescendants(t *testing.T)
func TestValidateUnreachableBoundaryRestartsAtRoot(t *testing.T)
func TestValidateFullBypassesCachedValidAndInvalidResults(t *testing.T)
func TestValidateCancellationPreservesCompletedTasksAndLeavesPending(t *testing.T)
func TestValidateRefRaceLeavesChangedTaskPendingAndReturnsStaleWrite(t *testing.T)
func TestValidateNeverMutatesCanonicalRefs(t *testing.T)
```

Literal expectations:

- fresh two-task, three-commit fixture: `tasksChecked=2`,
  `commitsChecked=6`, `cacheHits=0`, `valid=2`;
- unchanged rerun: `tasksChecked=0`, `commitsChecked=0`, `cacheHits=2`;
- five one-commit changes after a cached 500-task fixture:
  `tasksChecked=5`, `commitsChecked=5`, `cacheHits=495`;
- cached invalid unchanged still returns `corrupt-data`.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/historyvalidation \
  -run 'TestValidate' -count=1 -v
```

Expected: compile failure because `Validator` and `Result` do not exist.

- [ ] **Step 3: Implement preparation and request planning**

`Validate` must:

1. list and sort canonical heads;
2. call `Cache.Prepare`;
3. count unchanged current-version valid/invalid rows as task cache hits;
4. create requests only for pending tasks;
5. use `LastValidCommit` as `StopAt` only when canonical boundary state decodes
   and belongs to the same cached task/generation;
6. use no boundary for `full`.

Cached invalid unchanged contributes its failure and later aggregate
`corrupt-data` error without entering `ReadTaskHistories`.

- [ ] **Step 4: Implement root-to-head semantic evaluation**

For each history result:

- select `nil` parent state unless `BoundaryReached` and its cached boundary is
  valid;
- increment `CommitsChecked` from the result's actual inspected candidates;
- call `core.ValidateCheckpoint` for each record in order;
- stop the task at the first semantic or structural failure;
- retain the last valid commit, state, generation, and total valid commit count;
- encode the last valid state canonically;
- call `Cache.Record` before moving to the next task.

Continue all task-local failures. Preserve Task 1's exact failure commit and
`core.CategoryOf(err)`. Sort final failure records by task ID.

- [ ] **Step 5: Implement final head-race reconciliation and error precedence**

After task processing, list canonical heads once more and call `Prepare` with
`full=false`. If the initial and final inventories differ, affected tasks remain
pending and the run has a stale-write error.

Build counts from `Cache.Snapshot` for the final inventory. Return precedence:

1. shared Git/SQLite/framing error;
2. `corrupt-data` when `invalid > 0`;
3. `stale-write` when a head changed;
4. success.

On shared failure, return the partial result with every uncompleted task still
pending. Context cancellation is returned without converting it to validity.

- [ ] **Step 6: Run package and integration tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/historyvalidation ./internal/gitstore -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/historyvalidation/validator.go \
  internal/historyvalidation/validator_test.go \
  internal/historyvalidation/cache.go \
  internal/historyvalidation/cache_test.go
git commit -m "feat: validate task histories resumably"
```

---

### Task 4: CLI command and user-facing documentation

**Files:**

- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/flags_test.go`
- Modify: `internal/cli/help_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Create: `internal/cli/validate_test.go`
- Modify: `README.md`

**Interfaces:**

- Consumes: `historyvalidation.Open`, `Validator.Validate`, and
  `historyvalidation.Result`.
- Produces: CLI command `validate`, normal result envelope, human output, and
  nonzero invalid/stale/operational behavior.

- [ ] **Step 1: Write help, flags, JSON, human, and failure tests**

Add tests:

```go
func TestValidateHelpDocumentsFullAndJSON(t *testing.T)
func TestValidateRejectsPositionalsAndUnknownFlags(t *testing.T)
func TestValidateJSONReportsFreshCachedAndIncrementalCounts(t *testing.T)
func TestValidateHumanOutputListsEveryFailureInTaskOrder(t *testing.T)
func TestValidateJSONWritesResultAndErrorOnInvalidHistory(t *testing.T)
func TestValidateCachedInvalidHeadStillExitsNonzeroWithoutHistoryBatch(t *testing.T)
```

The JSON test decodes the real result envelope and compares literal field
values. The human test requires:

```text
Validated 2 task(s): 4 commit(s) checked, 0 cache hit(s); 1 valid, 1 invalid, 0 pending.
Invalid WB-... at <oid> [corrupt-data]: stored checkpoint differs from computed state
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli \
  -run 'TestValidate' -count=1 -v
```

Expected: unknown-command/help-schema failures.

- [ ] **Step 3: Add command schema and dispatch**

Add:

```go
"validate": {
    Name:        "validate",
    Synopsis:    "workbook validate [--full] [--json]",
    Description: "Validate complete task histories and stored checkpoints.",
    Options: []optionMetadata{
        {Name: "full", Kind: boolFlag, Description: "bypass cached validation results"},
        {Name: "json", Kind: boolFlag, Description: "emit JSON"},
    },
},
```

Place `validate` after `rebuild` in `commandOrder`. Dispatch `case "validate"` in
`Run`.

- [ ] **Step 4: Implement `runValidate` and output**

Open the repository/config directly, then open the validation package. Always
write `historyvalidation.Result` after `Validate` returns:

```go
result, validateErr := validator.Validate(ctx, *full)
if *jsonMode {
    writeResult(stdout, "validate", result)
} else {
    writeValidationResult(stdout, result)
}
return validateErr
```

Keep the normal top-level error writer so JSON mode emits the result on stdout
and structured error on stderr with a nonzero category exit.

- [ ] **Step 5: Update README implemented behavior**

Document:

- bounded current-tip validation versus explicit semantic history validation;
- cache location, validator version, pending/valid/invalid statuses, and
  disposable authority;
- normal cache hits and reachable boundary behavior;
- `--full`, interruption, exact failures, head races, and no ref mutation;
- the three acceptance scenarios and exact targets.

Do not add `sync --validate-history`.

- [ ] **Step 6: Run CLI and documentation checks GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/cli/flags.go internal/cli/flags_test.go \
  internal/cli/help_test.go internal/cli/run.go internal/cli/run_test.go \
  internal/cli/validate_test.go README.md
git commit -m "feat: expose semantic history validation"
```

---

### Task 5: Validation performance topologies

**Files:**

- Modify: `internal/perf/registry.go`
- Modify: `internal/perf/registry_test.go`
- Create: `internal/perf/validation_scenarios.go`
- Create: `internal/perf/validation_scenarios_test.go`
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`
- Modify: `docs/performance/README.md`

**Interfaces:**

- Consumes: `BuildFixture`, `MeasureCommandOutput`, scenario report types, and
  the real `workbook validate --json` envelope.
- Produces: selectors `validate-full-history`,
  `validate-cached-unchanged`, and `validate-five-changed`.

- [ ] **Step 1: Write registry, setup-isolation, oracle, and target tests**

Add tests:

```go
func TestValidationScenariosUseIndependentFixturesAndCommands(t *testing.T)
func TestValidationScenarioSetupIsExcludedFromMeasurement(t *testing.T)
func TestValidationScenarioOracleRejectsWrongCounts(t *testing.T)
func TestValidationScenarioTargetsUseExclusiveProcessLimit(t *testing.T)
func TestValidationScenarioProcessCountDoesNotScaleWithHistoryDepth(t *testing.T)
func TestBenchmarkMainRunsOnlySelectedValidationScenarios(t *testing.T)
```

The oracle literals are:

```text
full:       tasksChecked=tasks, commitsChecked=tasks*operations, cacheHits=0
cached:     tasksChecked=0, commitsChecked=0, cacheHits=tasks
fiveChange: tasksChecked=5, commitsChecked=5, cacheHits=tasks-5
all:        valid=tasks, invalid=0, pending=0, failures=[]
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench \
  -run 'Validation|SelectedValidation' -count=1 -v
```

Expected: missing selector and scenario runner failures.

- [ ] **Step 3: Implement independent validation scenario fixtures**

Each sample/scenario builds an independent fixture. Setup commands run through
the product binary outside `MeasureCommandOutput`:

- full: no setup validation;
- cached: one successful `validate --json`;
- five changed: one successful `validate --json`, then five successful
  `update <task> --description <literal> --json` commands.

Measure exactly:

```text
validate --full --json
validate --json
validate --json
```

Parse stdout into the normal result envelope and verify the literal contract
before accepting the sample.

- [ ] **Step 4: Register targets and benchmark dispatch**

Append stable registry entries:

```go
"validate-full-history",
"validate-cached-unchanged",
"validate-five-changed",
```

Targets:

```go
ScenarioTarget{MaxMilliseconds: 10000, MaxGitProcesses: 12}
ScenarioTarget{MaxMilliseconds: 500, MaxGitProcesses: 12}
ScenarioTarget{MaxMilliseconds: 1000, MaxGitProcesses: 12}
```

Add `RunValidationScenarios` dispatch in `cmd/workbook-bench/main.go`, selecting
only requested `validate-` names. Require at least 500 tasks and 20 operations
for selected validation scenarios even in baseline mode.

- [ ] **Step 5: Add a regular bounded-process witness**

Build the product binary once in the test and measure 10 tasks by 4 operations
for all three paths. Require each sample's `GitProcesses < 12`. Separately run
10 tasks by 7 operations and assert the same process counts, proving depth does
not scale the process count. This is a diagnostic regression, not acceptance
evidence.

- [ ] **Step 6: Run performance and benchmark packages GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/perf/registry.go internal/perf/registry_test.go \
  internal/perf/validation_scenarios.go \
  internal/perf/validation_scenarios_test.go \
  cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go \
  docs/performance/README.md
git commit -m "feat: benchmark semantic history validation"
```

---

### Task 6: One-shot acceptance evidence and final verification

**Files:**

- Create on successful run:
  `docs/performance/2026-07-29-history-validation-sha1.json`
- Create on successful run:
  `docs/performance/2026-07-29-history-validation-sha1.md`
- Create when SHA-256 is supported and the run succeeds:
  `docs/performance/2026-07-29-history-validation-sha256.json`
- Create when SHA-256 is supported and the run succeeds:
  `docs/performance/2026-07-29-history-validation-sha256.md`
- Create instead of a missing report when a one-shot command aborts:
  `docs/performance/2026-07-29-history-validation-<format>-attempt.md`
- Modify: `docs/performance/README.md`

**Interfaces:**

- Consumes: Task 5 selectors and the final product binary.
- Produces: immutable one-shot evidence, no rerun.

- [ ] **Step 1: Run pre-acceptance diagnostics**

Before building the measured binary, run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Run a non-acceptance 10-by-4 harness command for all three selectors and inspect
its JSON counts. Fix any failure before acceptance.

- [ ] **Step 2: Build the measured product binary once**

Run exactly once:

```sh
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false \
  -o /private/tmp/workbook-history-validation-acceptance ./cmd/workbook
```

Do not rebuild between object formats.

- [ ] **Step 3: Run SHA-1 acceptance exactly once**

Run exactly once:

```sh
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-history-validation-acceptance \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario validate-full-history \
  --scenario validate-cached-unchanged \
  --scenario validate-five-changed \
  --output-json docs/performance/2026-07-29-history-validation-sha1.json \
  --output-markdown docs/performance/2026-07-29-history-validation-sha1.md
```

If it aborts before trustworthy report assembly, do not rerun. Remove no
evidence; write the format-specific attempt note with the exact stage and
failure.

- [ ] **Step 4: Probe and run SHA-256 acceptance exactly once when supported**

Probe `git init --object-format=sha256` in a fresh temporary directory. If
supported, run the same command exactly once with:

```text
--object-format sha256
--output-json docs/performance/2026-07-29-history-validation-sha256.json
--output-markdown docs/performance/2026-07-29-history-validation-sha256.md
```

If unsupported, record that fact without substituting another SHA-1 run.

- [ ] **Step 5: Record evidence without replacement**

Update `docs/performance/README.md` with:

- the exact elapsed time and Git process count for each generated scenario;
- pass/miss/timeout/failure outcomes against inclusive time and exclusive
  process targets;
- any missing report and why it is missing;
- an explicit statement that no acceptance run was retried or replaced.

If a process target misses, fix the bounded product path and prove `<12` with
the regular diagnostic test, but do not rerun acceptance. A wall-clock miss is
recorded and does not by itself block merge under the approved decision.

- [ ] **Step 6: Commit evidence**

Stage only the generated reports/attempt notes and performance README:

```sh
git add docs/performance/README.md \
  docs/performance/2026-07-29-history-validation-*
git commit -m "docs: record history validation evidence"
```

- [ ] **Step 7: Run final verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Expected: tests and vet pass, no whitespace errors, and the worktree is clean.

## Plan self-review

- Every design requirement maps to a task.
- Every production slice begins with a named failing test and observed RED.
- Types and JSON names are consistent across Git, cache, validator, CLI, and
  benchmark tasks.
- Cache hits count unchanged task-head results in every layer.
- Structural failures and semantic failures both retain exact task and commit.
- Per-task transactions provide interruption resume without per-task Git
  processes.
- Final head reconciliation prevents a completed command from claiming stale
  validity.
- Acceptance commands are exact and state their non-rerun rule.
- No placeholders, unimplemented convenience flags, daemon, ref mutation, or
  authoritative SQLite behavior are included.
