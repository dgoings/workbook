# Local Performance Acceptance Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce acceptance-conformant local Workbook mutation evidence from a deterministic 500-task fixture with explicit cache, target, and provenance semantics.

**Architecture:** Replace the scalar-only fixture generator with deterministic task plans, then make cold and warm local scenarios independently prepare the disposable projection before measurement. Extend the report model with explicit duration policies and binary provenance while preserving synchronous Git durability and the existing remote/validation contracts.

**Tech Stack:** Go 1.26, installed Git plumbing/fast-import, SQLite projection, HTTP test servers, JSON/Markdown benchmark reports.

## Global Constraints

- The default acceptance fixture is 500 total task refs: 475 active, 25 tombstoned, and 20 operations per task.
- Acceptance requires at least 500 total tasks, at least 25 tombstones, and at least 20 operations per task; smaller runs are diagnostic.
- Every local scenario/sample has an independent fixture.
- Projection preparation occurs outside every measured local mutation and outside its Trace2 count.
- Cold single-operation p95 is inclusive at 200 milliseconds.
- Warm API-update p95 is inclusive at 100 milliseconds.
- Every ten-operation burst sample is strictly below 1,000 milliseconds.
- Local scenarios have no Git-process target.
- Git remains canonical; SQLite remains disposable; no daemon, asynchronous durability queue, persistent Git writer, or product-path optimization is introduced.
- Existing version-1 evidence is immutable; new reports use format version 2.
- Acceptance requires an exact injected source commit and records the measured Workbook binary SHA-256 checksum.
- The completion exercise selects only `cli-*` and `api-*` scenarios in SHA-1 and SHA-256 with at least 20 samples.
- A valid target miss is retained as evidence. Only a harness-construction defect authorizes a regression fix and replacement run.
- Do not run or modify remote-sync, local-bare-sync, scaling-slope, many-changed projection, validation, storage-decomposition, or peak-resource scenarios beyond compatibility changes required by shared types.

---

### Task 1: Representative deterministic fixture

**Files:**
- Modify: `internal/perf/model.go`
- Modify: `internal/perf/fixture.go`
- Modify: `internal/perf/fixture_test.go`
- Modify: `internal/perf/remote_fixture.go`
- Modify: `internal/perf/remote_fixture_test.go`
- Modify: `internal/perf/remote_scenarios.go`
- Modify: `internal/perf/remote_scenarios_test.go`
- Modify: `internal/perf/validation_scenarios.go`
- Modify: `internal/perf/validation_scenarios_test.go`
- Modify: `internal/perf/scenarios_test.go`
- Modify: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Consumes: `core.Apply`, `core.OperationPack`, `core.StateDocument`, and existing deterministic fast-import support.
- Produces:

```go
type FixtureSpec struct {
	TotalTasks       int    `json:"totalTasks"`
	ActiveTasks      int    `json:"activeTasks"`
	TombstonedTasks  int    `json:"tombstonedTasks"`
	OperationsPerTask int   `json:"operationsPerTask"`
	ObjectFormat     string `json:"objectFormat"`
}

type FixtureDependency struct {
	Dependent string
	Dependency string
}

type Fixture struct {
	Root               string
	Config             core.ProjectConfig
	TaskIDs            []string
	ActiveTaskIDs      []string
	TombstonedTaskIDs  []string
	Dependencies       []FixtureDependency
}
```

- `FixtureSpec` validation requires `ActiveTasks + TombstonedTasks == TotalTasks`, positive total/history depth, and at least two operations when tombstones are requested. Each selected local scenario separately validates the number and kind of active, tombstoned, or dependency-bearing tasks it consumes; ten-task bursts require ten active tasks.
- Remote and validation code uses `TotalTasks` for canonical-ref/task/history counts and `ActiveTasks` only for active-task selection.

- [ ] **Step 1: Write failing fixture-schema and representative-state tests**

Add table tests for invalid dimension sums and a real SHA-1 fixture test using:

```go
spec := FixtureSpec{
	TotalTasks: 10, ActiveTasks: 8, TombstonedTasks: 2,
	OperationsPerTask: 8, ObjectFormat: "sha1",
}
```

Assert literal counts, eight-commit histories, nonblank descriptions, nonempty labels, at least one direct backward dependency, at least one non-integer canonical rank, and exactly two deleted tips. Build the same fixture in two roots and assert identical task IDs, final state documents, and Git heads.

The production mutations these tests catch are: omitting a representative field, emitting a forward/cyclic dependency, writing an operation after tombstone, losing deterministic IDs/timestamps, or reporting the wrong population.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestFixtureSpec|TestBuildFixtureCreatesRepresentativeDeterministicHistories' -count=1
```

Expected: compilation or assertion failure because `FixtureSpec` and `Fixture` do not expose the new dimensions/populations and generated tips remain scalar-only.

- [ ] **Step 3: Implement deterministic task plans and explicit populations**

Precompute every task ID and generation before history writing. Build one plan per task with literal stable distributions:

```go
type fixtureTaskPlan struct {
	TaskID       string
	Generation   string
	Initial      core.TaskData
	Operations   []core.Operation
	Tombstoned   bool
}
```

Use stable label values from `benchmark`, `git`, `projection`, and `workflow`; dependencies may reference only earlier active plans. Use reduced fractional ranks such as `1/2`, `3/2`, and integer ranks. Ensure each plan has exactly `OperationsPerTask` packs, with a tombstone as the final pack only for the last `TombstonedTasks` plans.

Populate `TaskIDs`, `ActiveTaskIDs`, `TombstonedTaskIDs`, and literal direct `FixtureDependency` pairs from final states. Update shared remote/validation helpers and tests to use explicit total/active/tombstone dimensions without changing their behavior.

- [ ] **Step 4: Run fixture, remote, and validation tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestFixture|TestBuildFixture|TestRemote|TestValidation' -count=1 -timeout=180s
```

Expected: PASS.

- [ ] **Step 5: Format, inspect diagnostics, and commit**

Run:

```sh
gofmt -w internal/perf/model.go internal/perf/fixture.go internal/perf/fixture_test.go internal/perf/remote_fixture.go internal/perf/remote_fixture_test.go internal/perf/remote_scenarios.go internal/perf/remote_scenarios_test.go internal/perf/validation_scenarios.go internal/perf/validation_scenarios_test.go internal/perf/scenarios_test.go cmd/workbook-bench/main_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1 -timeout=180s
git diff --check
```

Commit:

```sh
git add internal/perf cmd/workbook-bench/main_test.go
git commit -m "test: make performance fixture representative"
```

---

### Task 2: Executable duration-target semantics

**Files:**
- Modify: `internal/perf/model.go`
- Modify: `internal/perf/model_test.go`
- Modify: `internal/perf/scenarios.go`
- Modify: `internal/perf/remote_scenarios.go`
- Modify: `internal/perf/validation_scenarios.go`
- Modify: `internal/perf/remote_scenarios_test.go`
- Modify: `internal/perf/validation_scenarios_test.go`
- Modify: `internal/perf/registry_test.go`

**Interfaces:**
- Consumes: `Summarize`, `ScenarioResult`, and all existing remote/validation target definitions.
- Produces:

```go
type DurationStatistic string
const (
	DurationP95         DurationStatistic = "p95"
	DurationEverySample DurationStatistic = "every-sample"
)

type DurationComparison string
const (
	DurationAtMost   DurationComparison = "at-most"
	DurationLessThan DurationComparison = "less-than"
)

type ScenarioTarget struct {
	DurationStatistic DurationStatistic  `json:"durationStatistic"`
	DurationComparison DurationComparison `json:"durationComparison"`
	MaxMilliseconds   float64            `json:"maxMilliseconds"`
	MaxGitProcesses   int                `json:"maxGitProcesses,omitempty"`
}
```

- A zero `MaxGitProcesses` means no process target.
- Report version becomes `2`.
- Existing remote/validation targets explicitly use `every-sample` plus `at-most`; their positive process limits remain exclusive.

- [ ] **Step 1: Write failing policy-boundary tests**

Add literal samples proving:

- p95 values of exactly 100 and 200 milliseconds pass;
- one sample above a p95 limit does not miss when nearest-rank p95 remains within it;
- a burst sample of exactly 1,000 milliseconds misses;
- a burst sample below 1,000 milliseconds passes;
- zero process limit does not cause a miss;
- timeout beats failure, failure beats miss, and miss beats pass;
- JSON and Markdown show `p95 <= 200.00 ms`, `p95 <= 100.00 ms`, and `each < 1000.00 ms`.

The production mutations these tests catch are using maximum instead of p95, treating strict burst comparison as inclusive, or accidentally inventing a zero-process budget.

- [ ] **Step 2: Run the model tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestReport.*Target|TestScenarioOutcome.*Duration' -count=1
```

Expected: compilation/assertion failure because the policy fields and local targets do not exist.

- [ ] **Step 3: Implement policy evaluation and local target assignment**

Evaluate timeout/failure first. For `p95`, compare `Summary.P95Milliseconds`; for `every-sample`, compare every successful sample. Apply inclusive `>` for `at-most` and strict `>=` for `less-than`. Check Git processes only when `MaxGitProcesses > 0`.

Assign:

```go
var coldSingleTarget = ScenarioTarget{
	DurationStatistic: DurationP95,
	DurationComparison: DurationAtMost,
	MaxMilliseconds: 200,
}
var warmUpdateTarget = ScenarioTarget{
	DurationStatistic: DurationP95,
	DurationComparison: DurationAtMost,
	MaxMilliseconds: 100,
}
var burstTarget = ScenarioTarget{
	DurationStatistic: DurationEverySample,
	DurationComparison: DurationLessThan,
	MaxMilliseconds: 1000,
}
```

Attach targets to every applicable local scenario and explicitly annotate existing remote/validation targets.

- [ ] **Step 4: Run target/report compatibility tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestReport|TestScenario|TestRemote|TestValidation' -count=1
```

Expected: PASS.

- [ ] **Step 5: Format and commit**

Run:

```sh
gofmt -w internal/perf/model.go internal/perf/model_test.go internal/perf/scenarios.go internal/perf/remote_scenarios.go internal/perf/validation_scenarios.go internal/perf/remote_scenarios_test.go internal/perf/validation_scenarios_test.go internal/perf/registry_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -count=1 -timeout=180s
git diff --check
```

Commit:

```sh
git add internal/perf
git commit -m "feat: encode local performance target policies"
```

---

### Task 3: Cold CLI isolation and untimed projection preparation

**Files:**
- Modify: `internal/perf/scenarios.go`
- Modify: `internal/perf/scenarios_test.go`
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Changes `RunColdCLI` to:

```go
func RunColdCLI(
	ctx context.Context,
	spec RunSpec,
	fixtureRoot string,
	selected []string,
) ([]ScenarioResult, error)
```

- Adds `prepareProjection(context.Context, CommandSpec, int) error`, which runs `workbook rebuild --json` with `MeasureCommandOutput`, discards its timing/process evidence, and validates `workbook.result` version 1, command `rebuild`, and exact `taskCount == FixtureSpec.TotalTasks`.
- Cold scenario definitions select active tasks, fixture tombstones, or literal dependency pairs from `Fixture`; no paired scenario shares a root.

- [ ] **Step 1: Write failing selection, ordering, and isolation tests**

Dependency-injected tests record these literal events per sample:

```text
build cli-delete
prepare cli-delete
measure cli-delete
build cli-restore
prepare cli-restore
measure cli-restore
```

Assert one unique root per selected scenario/sample, exactly one preparation before each measurement, restore uses `TombstonedTaskIDs[0]`, free uses an existing `FixtureDependency`, and selecting only `cli-update` builds/measures nothing else.

Add a real rebuild-envelope test that fails on the wrong format, command, or total count.

- [ ] **Step 2: Run focused cold tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -run 'TestRunColdCLI.*(Select|Prepare|Isolat)|TestPrepareProjection' -count=1
```

Expected: compilation/assertion failure because cold selection and projection preparation are absent and delete/restore plus depend/free share fixtures.

- [ ] **Step 3: Implement declarative cold scenario lifecycle**

Define cold scenario records containing name, target, task allocation, and command/burst measurement callback. For each selected definition and sample:

1. build a fresh fixture;
2. allocate only active/tombstoned/dependency IDs appropriate to that scenario;
3. prepare projection and validate the total count;
4. run exactly one measured command or burst; and
5. retain the measured sample unchanged.

Update `runBenchmark` to pass only selected `cli-*` names rather than run the full family and filter afterward.

- [ ] **Step 4: Run cold integration tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -run 'TestRunColdCLI|TestPrepareProjection|TestRunResolvesRelativeWorkbookBinary' -count=1 -timeout=180s
```

Expected: PASS.

- [ ] **Step 5: Format and commit**

Run:

```sh
gofmt -w internal/perf/scenarios.go internal/perf/scenarios_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1 -timeout=180s
git diff --check
```

Commit:

```sh
git add internal/perf/scenarios.go internal/perf/scenarios_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "fix: isolate seeded cold performance samples"
```

---

### Task 4: Warm HTTP projection preparation and exact selection

**Files:**
- Modify: `internal/perf/scenarios.go`
- Modify: `internal/perf/scenarios_test.go`
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Changes `RunWarmHTTP` to:

```go
func RunWarmHTTP(
	ctx context.Context,
	spec RunSpec,
	fixtureRoot string,
	selected []string,
) ([]ScenarioResult, error)
```

- Extends `warmScenarioServer` with:

```go
prepareProjection(ctx context.Context, activeTasks int, timeout time.Duration) error
```

- The real server method performs untimed `GET /api/tasks`, consumes the body, and validates `workbook.tasks` version 1 with exactly `FixtureSpec.ActiveTasks` tasks before any Trace2 cursor used for measurement is opened.

- [ ] **Step 1: Write failing warm setup-order and response tests**

Record one `prepare` event before each warm `measure` event and assert selecting only `api-update` starts exactly one server per sample. Add real HTTP tests with literal `workbook.tasks` envelopes for correct count, wrong count, malformed JSON, wrong format/version, and non-200 status.

The production mutations these tests catch are treating `/healthz` as projection preparation, opening the measured cursor too early, or running unselected warm scenarios.

- [ ] **Step 2: Run focused warm tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -run 'TestRunWarmHTTP.*(Select|Prepare|Order)|TestWarm.*PrepareProjection' -count=1
```

Expected: compilation/assertion failure because warm servers do not expose projection preparation and selectors are filtered only after running.

- [ ] **Step 3: Implement warm preparation and selected lifecycle**

After `/healthz` succeeds, perform and validate the untimed task load. Only then call `measureStatus`, `measureIndependentBurst`, or `measureSameTaskBurst`. Preserve one fixture/server per selected scenario/sample and close the server on setup or measurement failure.

Update `runBenchmark` to pass selected `api-*` names directly.

- [ ] **Step 4: Run warm integration tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -run 'TestRunWarmHTTP|TestWarm|TestRunResolvesRelativeWorkbookBinary' -count=1 -timeout=180s
```

Expected: PASS.

- [ ] **Step 5: Format and commit**

Run:

```sh
gofmt -w internal/perf/scenarios.go internal/perf/scenarios_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1 -timeout=180s
git diff --check
```

Commit:

```sh
git add internal/perf/scenarios.go internal/perf/scenarios_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "fix: prepare warm projection before measurement"
```

---

### Task 5: CLI dimensions, provenance, and documentation

**Files:**
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`
- Modify: `internal/perf/model.go`
- Modify: `internal/perf/model_test.go`
- Modify: `docs/performance/README.md`
- Modify: `README.md`

**Interfaces:**
- Adds `options.tombstones int` and `--tombstones`.
- `--tasks` means total task refs.
- Omitted tombstones normalize to 25 when total tasks are at least 500 and one for smaller diagnostics; explicit zero remains diagnostic-only.
- `Environment` adds:

```go
WorkbookBinarySHA256 string `json:"workbookBinarySha256"`
```

- `benchmarkEnvironment` streams the measured binary through `crypto/sha256` and `hex.EncodeToString`.
- Acceptance rejects `WorkbookCommit == "unknown"` before fixture construction; baseline retains it.

- [ ] **Step 1: Write failing dimension/default/provenance tests**

Add option tables proving:

- default 500 tasks resolves to 475 active and 25 tombstoned;
- a ten-task diagnostic defaults to nine active and one tombstoned;
- explicit zero tombstones remains valid only outside acceptance;
- acceptance rejects 499 total, 24 tombstones, 19 operations, fewer than ten active, or an unknown measured commit;
- report JSON contains the hand-computed SHA-256 of a deterministic temporary executable.

The production mutations these tests catch are treating `--tasks` as active, silently accepting an undersized representative fixture, hashing the wrong path, or inferring provenance from the harness checkout.

- [ ] **Step 2: Run CLI/model tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench ./internal/perf -run 'TestValidateOptions.*(Tombstone|Acceptance)|TestBenchmarkEnvironment.*SHA256|TestReportWritesVersioned' -count=1
```

Expected: compilation/assertion failure because the option, checksum field, and acceptance provenance guard do not exist.

- [ ] **Step 3: Implement CLI normalization and checksum provenance**

Normalize tombstones after parsing but before scenario dispatch. Construct all three explicit fixture counts. Compute the checksum from the resolved measured binary path. Reject unknown acceptance provenance immediately after environment collection and before `BuildFixture`.

Update README examples and performance documentation with the exact total/active/tombstone semantics, untimed cold rebuild, untimed warm task load, target-policy text, report version 2, checksum field, and acceptance commit requirement.

- [ ] **Step 4: Run benchmark CLI and documentation-adjacent tests GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench ./internal/perf -count=1 -timeout=180s
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Format and commit**

Run:

```sh
gofmt -w cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go internal/perf/model.go internal/perf/model_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Commit:

```sh
git add cmd/workbook-bench internal/perf/model.go internal/perf/model_test.go docs/performance/README.md README.md
git commit -m "feat: make local acceptance evidence self-describing"
```

---

### Task 6: Independent review and local acceptance exercise

**Files:**
- Create after valid runs: `docs/performance/2026-07-29-local-acceptance-sha1.json`
- Create after valid runs: `docs/performance/2026-07-29-local-acceptance-sha1.md`
- Create after valid runs: `docs/performance/2026-07-29-local-acceptance-sha256.json`
- Create after valid runs: `docs/performance/2026-07-29-local-acceptance-sha256.md`
- Create after valid runs: `docs/performance/2026-07-29-local-acceptance-provenance.md`
- Create only after an invalid attempt: `docs/performance/2026-07-29-local-acceptance-<format>-attempt.md`
- Modify: `docs/performance/README.md`
- Modify only for a discovered harness defect: the smallest production/test files covering that defect.

**Interfaces:**
- Consumes the reviewed final Workbook and `workbook-bench` binaries.
- Produces two valid local-only reports with one source commit and recorded checksums.

- [ ] **Step 1: Run full verification and obtain independent whole-branch review**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Resolve every load-bearing review finding before building measured binaries.

- [ ] **Step 2: Build both final binaries once and record identities**

Run:

```sh
git rev-parse HEAD
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -ldflags "-X main.commit=$(git rev-parse HEAD)" -o /private/tmp/workbook-local-acceptance ./cmd/workbook
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-bench-local-acceptance ./cmd/workbook-bench
shasum -a 256 /private/tmp/workbook-local-acceptance /private/tmp/workbook-bench-local-acceptance
```

Write the exact source commit, product checksum, harness checksum, and build
commands to `docs/performance/2026-07-29-local-acceptance-provenance.md`. Do not
rebuild between formats.

- [ ] **Step 3: Run the SHA-1 local-only acceptance invocation**

Run one invocation containing each `cli-*` and `api-*` selector, and no other selector:

```sh
/private/tmp/workbook-bench-local-acceptance \
  --workbook /private/tmp/workbook-local-acceptance \
  --tasks 500 --tombstones 25 --operations 20 --samples 20 \
  --timeout 60s --phase acceptance --object-format sha1 \
  --scenario cli-create --scenario cli-delete --scenario cli-depend \
  --scenario cli-free --scenario cli-move --scenario cli-restore \
  --scenario cli-update --scenario cli-burst-independent-10 \
  --scenario cli-burst-same-task-10 --scenario api-update \
  --scenario api-burst-independent-10 --scenario api-burst-same-task-10 \
  --output-json docs/performance/2026-07-29-local-acceptance-sha1.json \
  --output-markdown docs/performance/2026-07-29-local-acceptance-sha1.md
```

- [ ] **Step 4: Run the SHA-256 local-only acceptance invocation**

Repeat the exact selector set and dimensions with:

```sh
--object-format sha256
--output-json docs/performance/2026-07-29-local-acceptance-sha256.json
--output-markdown docs/performance/2026-07-29-local-acceptance-sha256.md
```

- [ ] **Step 5: Classify any non-valid attempt before changing code**

If a run emits a valid report whose only unfavorable outcome is `miss`, retain it and continue. If the run exposes fixture, isolation, setup, target, or provenance breakage, write an attempt record before editing. Add one failing regression test, watch it fail, implement the smallest harness correction, rerun focused/full verification and independent review, rebuild both binaries, and repeat both format invocations. Do not change product behavior to chase a target.

- [ ] **Step 6: Validate and commit final evidence**

Verify both JSON reports contain:

```text
format workbook.performance-report
version 2
phase acceptance
totalTasks 500
activeTasks 475
tombstonedTasks 25
operationsPerTask 20
12 selected local scenarios
20 samples per scenario
non-unknown workbookCommit
64-character workbookBinarySha256
```

Confirm no remote, repository, validation, scale, resource, or storage scenario appears. Link the reports from `docs/performance/README.md`.
Verify the provenance document matches the product checksum embedded in both
JSON reports and contains the separately computed harness checksum.

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench ./internal/perf -count=1 -timeout=180s
git diff --check
git status --short
```

Commit:

```sh
git add docs/performance
git commit -m "docs: record corrected local acceptance evidence"
```
