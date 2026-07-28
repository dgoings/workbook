# Workbook Performance Benchmark Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a reproducible 500-task by 20-operation performance harness, capture the current implementation's bounded baseline once, and publish machine-readable and human-readable results without optimizing the product path.

**Architecture:** A development-only `workbook-bench` executable will generate valid deterministic Workbook repositories through `git fast-import`, run the real compiled `workbook` executable through cold CLI and warm HTTP scenarios, collect elapsed time and Git Trace2 process counts, and render versioned JSON plus Markdown reports. The harness owns fixture generation and measurement only; product mutation, projection, web, and sync behavior remain unchanged in this plan.

**Tech Stack:** Go 1.26+, installed Git plumbing and Trace2 event output, Workbook's existing `core` and `gitstore` packages, `net/http`, and temporary real repositories.

## Global Constraints

- The acceptance fixture has exactly 500 active tasks and exactly 20 operation commits per task, including the root create operation.
- Warm web/API Git-durable mutation target is p95 at or below 100 milliseconds.
- Cold CLI Git-durable mutation target is p95 at or below 200 milliseconds.
- Ten sequential same-task mutations and ten independent-task mutations must each complete in less than one second.
- Counts above 500 active tasks are diagnostic only.
- The baseline uses bounded timeouts and records misses or timeouts; it does not tune the implementation.
- SQLite remains disposable and Git remains the only durable source of truth.
- The harness never reads `.git/refs` directly and never assumes a Git object ID length or hash algorithm.
- The development benchmark executable is not included in release archives.
- Ordinary tests assert structure and command cardinality, not wall-clock performance.

---

## File Structure

- `internal/perf/model.go` owns versioned report types, sample summaries, target comparison, JSON encoding, and Markdown rendering.
- `internal/perf/model_test.go` tests deterministic summaries and stable JSON/Markdown contracts.
- `internal/perf/fixture.go` owns deterministic Workbook fixture generation and Git fast-import serialization.
- `internal/perf/fixture_test.go` validates small SHA-1 and supported SHA-256 fixtures through Git and Workbook readers.
- `internal/perf/command.go` owns bounded child-process execution, elapsed timing, exit classification, and Git Trace2 parsing.
- `internal/perf/command_test.go` tests success, failure, timeout, and Git-process counting.
- `internal/perf/scenarios.go` owns cold CLI, warm HTTP, burst, projection, storage, and local-remote scenario orchestration.
- `internal/perf/scenarios_test.go` tests scenario selection, mutation preconditions, server lifecycle, and report completeness with small fixtures.
- `cmd/workbook-bench/main.go` owns development-tool flags, Workbook binary selection, output files, and exit behavior.
- `cmd/workbook-bench/main_test.go` tests invocation validation and output creation.
- `docs/performance/README.md` documents reproducible benchmark commands and interpretation.
- `docs/performance/2026-07-28-baseline.json` records the generated current baseline.
- `docs/performance/2026-07-28-baseline.md` explains the same baseline for humans.
- `docs/performance/2026-07-28-baseline-sha256.json` records the optional SHA-256 baseline when the installed Git supports it.
- `docs/performance/2026-07-28-baseline-sha256.md` explains the optional SHA-256 baseline when supported.
- `README.md` links to the development benchmark documentation without claiming the targets are achieved.

### Task 1: Define the versioned report model

**Files:**
- Create: `internal/perf/model.go`
- Create: `internal/perf/model_test.go`

**Interfaces:**
- Produces: `perf.FixtureSpec`, `perf.Targets`, `perf.Sample`, `perf.Summary`, `perf.ScenarioResult`, `perf.RepositoryMetrics`, and `perf.Report`
- Produces: `func Summarize([]Sample) Summary`
- Produces: `func (Report) WriteJSON(io.Writer) error`
- Produces: `func (Report) WriteMarkdown(io.Writer) error`

- [ ] **Step 1: Write failing summary and report-contract tests**

```go
func TestSummarizeUsesNearestRankP95AndRetainsTimeouts(t *testing.T) {
	samples := []Sample{
		{Duration: 10 * time.Millisecond, GitProcesses: 2},
		{Duration: 20 * time.Millisecond, GitProcesses: 3},
		{Duration: 30 * time.Millisecond, GitProcesses: 4},
		{Duration: 40 * time.Millisecond, GitProcesses: 5},
		{TimedOut: true, Error: "timed out after 1s"},
	}
	got := Summarize(samples)
	if got.Completed != 4 || got.TimedOut != 1 {
		t.Fatalf("summary counts = %#v", got)
	}
	if got.P95Milliseconds != 40 || got.P95GitProcesses != 5 {
		t.Fatalf("summary p95 = %#v", got)
	}
}

func TestReportWritesVersionedJSONAndMarkdown(t *testing.T) {
	report := Report{
		Format: "workbook.performance-report", Version: 1, Phase: "baseline",
		Fixture: FixtureSpec{ActiveTasks: 500, OperationsPerTask: 20, ObjectFormat: "sha1"},
		Targets: Targets{WarmP95Milliseconds: 100, ColdP95Milliseconds: 200, BurstMilliseconds: 1000},
		Scenarios: []ScenarioResult{{Name: "cli-update", Surface: "cold-cli", Samples: []Sample{{Duration: 25 * time.Millisecond}}}},
	}
	var jsonOutput, markdownOutput bytes.Buffer
	if err := report.WriteJSON(&jsonOutput); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteMarkdown(&markdownOutput); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(jsonOutput.Bytes(), []byte(`"format":"workbook.performance-report"`)) {
		t.Fatalf("JSON = %s", jsonOutput.Bytes())
	}
	if !strings.Contains(markdownOutput.String(), "| cli-update | cold-cli |") {
		t.Fatalf("Markdown = %s", markdownOutput.String())
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm the package is absent**

Run: `go test ./internal/perf -run 'TestSummarize|TestReport' -count=1`

Expected: FAIL because `internal/perf` and its report types do not exist.

- [ ] **Step 3: Implement exact report types and nearest-rank summaries**

```go
const (
	ReportFormat  = "workbook.performance-report"
	ReportVersion = 1
)

type FixtureSpec struct {
	ActiveTasks        int    `json:"activeTasks"`
	OperationsPerTask  int    `json:"operationsPerTask"`
	ObjectFormat       string `json:"objectFormat"`
}

type Targets struct {
	WarmP95Milliseconds float64 `json:"warmP95Milliseconds"`
	ColdP95Milliseconds float64 `json:"coldP95Milliseconds"`
	BurstMilliseconds   float64 `json:"burstMilliseconds"`
}

type Sample struct {
	Duration     time.Duration `json:"-"`
	Milliseconds float64       `json:"milliseconds"`
	GitProcesses int           `json:"gitProcesses"`
	ExitCode     int           `json:"exitCode"`
	TimedOut     bool          `json:"timedOut"`
	Error        string        `json:"error,omitempty"`
}

type Summary struct {
	Completed            int     `json:"completed"`
	TimedOut             int     `json:"timedOut"`
	MinMilliseconds      float64 `json:"minMilliseconds"`
	MedianMilliseconds   float64 `json:"medianMilliseconds"`
	P95Milliseconds      float64 `json:"p95Milliseconds"`
	P95GitProcesses      int     `json:"p95GitProcesses"`
}

type ScenarioResult struct {
	Name    string   `json:"name"`
	Surface string   `json:"surface"`
	Samples []Sample `json:"samples"`
	Summary Summary  `json:"summary"`
}

type RepositoryMetrics struct {
	LooseRefEnumerationMilliseconds  float64 `json:"looseRefEnumerationMilliseconds"`
	PackedRefEnumerationMilliseconds float64 `json:"packedRefEnumerationMilliseconds"`
	LooseObjects                     int64   `json:"looseObjects"`
	LooseObjectBytes                 int64   `json:"looseObjectBytes"`
	PackedObjects                    int64   `json:"packedObjects"`
	PackBytes                        int64   `json:"packBytes"`
}

type Environment struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	GitVersion      string `json:"gitVersion"`
	GoVersion       string `json:"goVersion"`
	WorkbookVersion string `json:"workbookVersion"`
	WorkbookCommit  string `json:"workbookCommit"`
}

type Report struct {
	Format      string             `json:"format"`
	Version     int                `json:"version"`
	Phase       string             `json:"phase"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Environment Environment        `json:"environment"`
	Fixture     FixtureSpec        `json:"fixture"`
	Targets     Targets            `json:"targets"`
	Scenarios   []ScenarioResult   `json:"scenarios"`
	Repository  RepositoryMetrics  `json:"repository"`
}
```

Normalize `Milliseconds` from `Duration` before encoding. Sort scenarios by name in both output formats. Use nearest-rank percentile index `ceil(0.95*n)-1` over successful samples, and retain timeout counts separately. Baseline Markdown labels targets as reference budgets, not achieved guarantees.

- [ ] **Step 4: Run focused tests**

Run: `go test ./internal/perf -run 'TestSummarize|TestReport' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the report model**

```bash
git add internal/perf/model.go internal/perf/model_test.go
git commit -m "feat: add performance report model"
```

### Task 2: Generate deterministic valid Workbook fixtures

**Files:**
- Create: `internal/perf/fixture.go`
- Create: `internal/perf/fixture_test.go`

**Interfaces:**
- Consumes: `perf.FixtureSpec`
- Consumes: `core.OperationPack`, `core.Apply`, `core.EncodeDocument`, and `gitstore.Repository.Init`
- Produces: `type Fixture struct { Root string; Config core.ProjectConfig; TaskIDs []string }`
- Produces: `func BuildFixture(context.Context, string, FixtureSpec) (Fixture, error)`

- [ ] **Step 1: Write failing small-fixture integration tests**

```go
func TestBuildFixtureCreatesCompleteTipStatesWithoutReplay(t *testing.T) {
	spec := FixtureSpec{ActiveTasks: 3, OperationsPerTask: 4, ObjectFormat: "sha1"}
	fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.TaskIDs) != 3 {
		t.Fatalf("task IDs = %d, want 3", len(fixture.TaskIDs))
	}
	repository, err := gitstore.Open(context.Background(), fixture.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range fixture.TaskIDs {
		snapshot, err := repository.Get(context.Background(), fixture.Config, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State.LogicalClock != 4 || snapshot.State.Task.Title == "" {
			t.Fatalf("tip = %#v", snapshot.State)
		}
		output := runGit(t, fixture.Root, "rev-list", "--count", snapshot.Head)
		if strings.TrimSpace(output) != "4" {
			t.Fatalf("history count = %q, want 4", output)
		}
	}
}
```

Add a SHA-256 subtest that first probes `git init --object-format=sha256`; call `t.Skip` only when Git reports that the format is unsupported.

- [ ] **Step 2: Run the fixture tests and confirm failure**

Run: `go test ./internal/perf -run TestBuildFixture -count=1`

Expected: FAIL because `BuildFixture` does not exist.

- [ ] **Step 3: Implement deterministic configuration and operation generation**

Use a fixed UTC origin, a seeded `math/rand.Rand` as ULID entropy, and monotonically increasing millisecond timestamps. Generate one project ID, then unique task, history-generation, and operation IDs.

For every task, generate exactly `OperationsPerTask` commits:

```go
rootPack := core.OperationPack{
	Format: "workbook.operation-pack", Version: 1,
	ProjectID: config.ProjectID, TaskID: taskID,
	HistoryGeneration: generation,
	Actor: core.Actor{ID: "benchmark@example.invalid"},
	LogicalClock: 1, WallTime: timestamp,
	Operations: []core.Operation{{
		ID: operationID, Type: core.OperationTaskCreate,
		Task: &core.TaskData{
			Title: fmt.Sprintf("Benchmark task %04d", taskIndex),
			Status: core.StatusBacklog, Priority: core.PriorityMedium,
			Rank: fmt.Sprintf("%d/1", taskIndex+1),
			CreatedAt: timestamp, UpdatedAt: timestamp,
		},
	}},
}
```

For commits 2 through 20, alternate valid `field.set` operations over `description`, `status`, and `priority`, while ensuring the twentieth resulting state is active. Apply every pack with `core.Apply`, encode both documents with `core.EncodeDocument`, and fail fixture creation on any validation error.

- [ ] **Step 4: Implement one-pass Git fast-import**

Initialize the destination with the requested hash format, configure deterministic user identity and `core.logAllRefUpdates=always`, open it through `gitstore`, and initialize Workbook with the deterministic project ID.

Serialize every commit to one `git fast-import --quiet` process:

```go
func writeImportedCommit(
	writer io.Writer,
	ref string,
	mark int,
	parentMark int,
	timestamp time.Time,
	message string,
	operation []byte,
	state []byte,
) error {
	fmt.Fprintf(writer, "commit %s\nmark :%d\n", ref, mark)
	fmt.Fprintf(writer, "author Workbook Benchmark <benchmark@example.invalid> %d +0000\n", timestamp.Unix())
	fmt.Fprintf(writer, "committer Workbook Benchmark <benchmark@example.invalid> %d +0000\n", timestamp.Unix())
	fmt.Fprintf(writer, "data %d\n%s\n", len(message), message)
	if parentMark != 0 {
		fmt.Fprintf(writer, "from :%d\n", parentMark)
	}
	fmt.Fprintln(writer, "deleteall")
	fmt.Fprintln(writer, "M 100644 inline operation.json")
	fmt.Fprintf(writer, "data %d\n", len(operation))
	writer.Write(operation)
	fmt.Fprintln(writer, "M 100644 inline state.json")
	fmt.Fprintf(writer, "data %d\n", len(state))
	writer.Write(state)
	fmt.Fprintln(writer)
	return nil
}
```

Close fast-import input, wait for successful completion, then enumerate `refs/workbook/tasks/` through `git for-each-ref` and require exactly `ActiveTasks` refs.

- [ ] **Step 5: Run focused and package tests**

Run: `go test ./internal/perf -run TestBuildFixture -count=1`

Expected: PASS for SHA-1 and PASS or an explicit unsupported-format skip for SHA-256.

Run: `go test ./internal/perf -count=1`

Expected: PASS.

- [ ] **Step 6: Commit fixture generation**

```bash
git add internal/perf/fixture.go internal/perf/fixture_test.go
git commit -m "feat: generate performance fixtures"
```

### Task 3: Measure bounded commands and Git process counts

**Files:**
- Create: `internal/perf/command.go`
- Create: `internal/perf/command_test.go`

**Interfaces:**
- Consumes: `perf.Sample`
- Produces: `type CommandSpec struct { Binary string; Args []string; Directory string; Environment []string; Timeout time.Duration }`
- Produces: `func MeasureCommand(context.Context, CommandSpec) Sample`
- Produces: `type TraceCursor struct` with `func OpenTraceCursor(string) (*TraceCursor, error)` and `func (*TraceCursor) CountNewGitProcesses() (int, error)`

- [ ] **Step 1: Write failing command and Trace2 tests**

```go
func TestMeasureCommandCountsGitProcesses(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: gitPath, Args: []string{"--version"}, Directory: t.TempDir(),
		Timeout: 5 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.GitProcesses != 1 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandRecordsTimeout(t *testing.T) {
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{"-c", "while :; do :; done"},
		Directory: t.TempDir(), Timeout: 20 * time.Millisecond,
	})
	if !sample.TimedOut || sample.ExitCode == 0 {
		t.Fatalf("sample = %#v", sample)
	}
}
```

- [ ] **Step 2: Run focused tests and confirm failure**

Run: `go test ./internal/perf -run 'TestMeasureCommand|TestTraceCursor' -count=1`

Expected: FAIL because command measurement does not exist.

- [ ] **Step 3: Implement bounded execution and trace parsing**

Create a fresh absolute Trace2 event file for each cold command. Add
`GIT_TRACE2_EVENT=<absolute-path>` to the child environment without dropping
the caller environment. Measure only `command.Run`, not fixture setup.

Parse each JSON line into:

```go
type traceEvent struct {
	Event string   `json:"event"`
	Argv  []string `json:"argv"`
}
```

Count events where `Event == "start"` and `Argv` is nonempty. Preserve the
child's exit code, classify deadline cancellation as `TimedOut`, and retain a
single-line stderr summary only for failure reports. `TraceCursor` stores a byte
offset so a long-lived server trace can be counted per HTTP request.

- [ ] **Step 4: Run focused tests**

Run: `go test ./internal/perf -run 'TestMeasureCommand|TestTraceCursor' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit measurement support**

```bash
git add internal/perf/command.go internal/perf/command_test.go
git commit -m "feat: measure Workbook command performance"
```

### Task 4: Run cold CLI mutation scenarios

**Files:**
- Create: `internal/perf/scenarios.go`
- Create: `internal/perf/scenarios_test.go`

**Interfaces:**
- Consumes: `perf.BuildFixture`, `perf.MeasureCommand`, and the real `workbook` executable
- Produces: `type RunSpec struct { WorkbookBinary string; Fixture FixtureSpec; Samples int; CommandTimeout time.Duration }`
- Produces: `func RunColdCLI(context.Context, RunSpec, string) ([]ScenarioResult, error)`

- [ ] **Step 1: Write failing scenario-selection and completeness tests**

Use a 40-task, four-operation fixture with one sample and assert the exact
scenario names:

```go
want := []string{
	"cli-create",
	"cli-delete",
	"cli-depend",
	"cli-free",
	"cli-move",
	"cli-restore",
	"cli-update",
	"cli-burst-independent-10",
	"cli-burst-same-task-10",
}
```

Build the test Workbook binary into `t.TempDir()`. Require every single
mutation scenario to contain one sample, both burst scenarios to contain one
aggregate sample, and every successful sample to record at least one Git
process.

- [ ] **Step 2: Run the focused scenario test and confirm failure**

Run: `go test ./internal/perf -run TestRunColdCLI -count=1`

Expected: FAIL because `RunColdCLI` does not exist.

- [ ] **Step 3: Implement deterministic scenario task allocation**

Allocate disjoint fixture task IDs to each scenario. Execute commands with full
canonical IDs and `--json`. Pair delete followed by restore, and depend followed
by free, so their prerequisites are created by a separately measured scenario.
Alternate status values for repeated updates so no request is a no-op.

For each sample index, use these command forms:

```text
workbook create "Benchmark created task N" --status ready --priority high --json
workbook update <task> --status ready --json
workbook delete <task> --json
workbook restore <same-task> --json
workbook move <task> --before <anchor> --json
workbook depend <task> <dependency> --json
workbook free <same-task> <same-dependency> --json
```

Run the same-task burst as ten sequential status changes and measure the entire
loop. Run the independent burst as ten concurrently started CLI updates against
ten different tasks and measure until every process exits. Record one aggregate
sample containing total duration, summed Git-process count, and failure detail
when any member fails or times out.

- [ ] **Step 4: Run the cold CLI scenario tests**

Run: `go test ./internal/perf -run TestRunColdCLI -count=1`

Expected: PASS.

- [ ] **Step 5: Commit cold CLI scenarios**

```bash
git add internal/perf/scenarios.go internal/perf/scenarios_test.go
git commit -m "feat: benchmark cold CLI mutations"
```

### Task 5: Add warm HTTP and repository scenarios

**Files:**
- Modify: `internal/perf/scenarios.go`
- Modify: `internal/perf/scenarios_test.go`

**Interfaces:**
- Produces: `func RunWarmHTTP(context.Context, RunSpec, string) ([]ScenarioResult, error)`
- Produces: `func MeasureRepository(context.Context, string, string, time.Duration) (RepositoryMetrics, []ScenarioResult, error)`

- [ ] **Step 1: Write failing warm-server lifecycle tests**

Start the compiled Workbook binary with:

```text
workbook serve --addr 127.0.0.1:0
```

Wait for the exact stderr prefix `Workbook board: http://`, confirm `/healthz`
returns HTTP 200, then assert one measured status request returns a versioned
`workbook.task-mutation` document. Cancel the server with `os.Interrupt` and
require a bounded clean exit.

Assert exact warm scenario names:

```go
[]string{
	"api-update",
	"api-burst-independent-10",
	"api-burst-same-task-10",
}
```

- [ ] **Step 2: Run warm tests and confirm failure**

Run: `go test ./internal/perf -run 'TestRunWarmHTTP|TestMeasureRepository' -count=1`

Expected: FAIL because warm and repository scenarios are absent.

- [ ] **Step 3: Implement warm HTTP measurement**

Give the long-lived server one Trace2 file. After startup and health readiness,
capture the trace cursor offset immediately before each request and count only
new Git start events after the response.

Send status bodies using the current API:

```json
{"status":"ready"}
```

Alternate `ready` and `in-progress` for sequential samples. Use ten distinct
tasks for the independent burst. Require HTTP 200 and the exact versioned
mutation response before recording a sample as successful.

- [ ] **Step 4: Implement projection, ref, object, and local-remote metrics**

Measure these operations without reading `.git/refs`:

- `workbook rebuild --json`;
- unchanged `workbook list --json`;
- one changed task followed by `workbook list --json`;
- `git for-each-ref --format=%(refname)%00%(objectname) refs/workbook/tasks/`
  before and after `git pack-refs --all`;
- `git count-objects -v` before and after `git gc`;
- initial `workbook sync --json` against a temporary local bare `origin`; and
- unchanged `workbook sync --json` against the same origin.

Use scenario names `projection-rebuild`, `projection-refresh-unchanged`,
`projection-refresh-one-changed`, `sync-initial-local-bare`, and
`sync-unchanged-local-bare`. Apply the configured command timeout to sync; a
timeout is a valid baseline result rather than a harness failure.

Parse numeric `count`, `size`, `in-pack`, and `size-pack` fields from
`git count-objects -v`. Convert KiB fields to bytes. Record prefix-enumeration
duration before and after packing refs without inspecting their file layout.

- [ ] **Step 5: Run focused and complete performance-package tests**

Run: `go test ./internal/perf -run 'TestRunWarmHTTP|TestMeasureRepository' -count=1`

Expected: PASS.

Run: `go test ./internal/perf -count=1`

Expected: PASS.

- [ ] **Step 6: Commit warm and repository scenarios**

```bash
git add internal/perf/scenarios.go internal/perf/scenarios_test.go
git commit -m "feat: benchmark warm API and repository paths"
```

### Task 6: Add the development benchmark executable

**Files:**
- Create: `cmd/workbook-bench/main.go`
- Create: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Consumes: all `internal/perf` interfaces from Tasks 1 through 5
- Produces: `go run ./cmd/workbook-bench`

- [ ] **Step 1: Write failing flag and output tests**

Exercise a small fixture:

```go
exitCode := run(context.Background(), []string{
	"--workbook", workbookBinary,
	"--tasks", "3",
	"--operations", "4",
	"--samples", "1",
	"--timeout", "5s",
	"--object-format", "sha1",
	"--output-json", jsonPath,
	"--output-markdown", markdownPath,
}, &stdout, &stderr)
```

Require exit zero, both output files, report format/version, and the complete
scenario set. Add invocation-error cases for missing Workbook binary, tasks
below 3, operations below 2, samples below 1, nonpositive timeout, unsupported
object-format text, and identical output paths.

- [ ] **Step 2: Run command tests and confirm failure**

Run: `go test ./cmd/workbook-bench -count=1`

Expected: FAIL because the command does not exist.

- [ ] **Step 3: Implement exact development-tool flags**

Support:

```text
--workbook <path>
--tasks <count>                 default 500
--operations <count>            default 20
--samples <count>               default 1
--timeout <duration>            default 60s
--object-format <sha1|sha256>   default sha1
--output-json <path>            required
--output-markdown <path>        required
--phase <baseline|acceptance>   default baseline
```

Create a temporary fixture root, run cold CLI, warm HTTP, and repository
scenarios, fill environment metadata from `runtime`, `git --version`, `go
version`, and `workbook version --json`, then write both outputs through
temporary sibling files followed by atomic rename.

Return nonzero for fixture, harness, or output failures. A measured child
timeout or nonzero product command remains report data and does not make the
harness command fail.

- [ ] **Step 4: Run command and complete tests**

Run: `go test ./cmd/workbook-bench -count=1`

Expected: PASS.

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit the benchmark executable**

```bash
git add cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "feat: add Workbook benchmark harness"
```

### Task 7: Document and capture the bounded baseline

**Files:**
- Create: `docs/performance/README.md`
- Create: `docs/performance/2026-07-28-baseline.json`
- Create: `docs/performance/2026-07-28-baseline.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: `go run ./cmd/workbook-bench`
- Produces: reproducible baseline artifacts for `WB-01KYD75VPPVW6SGH28X1ME9CZ5`

- [ ] **Step 1: Write the benchmark documentation**

Document:

```sh
go build -o /tmp/workbook-benchmark-target ./cmd/workbook
go run ./cmd/workbook-bench \
  --workbook /tmp/workbook-benchmark-target \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --output-json docs/performance/2026-07-28-baseline.json \
  --output-markdown docs/performance/2026-07-28-baseline.md
```

Explain that baseline timeouts are recorded evidence, target numbers are not
claims, setup time is excluded, and the final acceptance task will run multiple
samples after all target paths are implemented.

- [ ] **Step 2: Add README discoverability**

Add one short development-performance paragraph linking
`docs/performance/README.md`. Preserve the existing statement that SQLite is
disposable and Git is canonical.

- [ ] **Step 3: Run the SHA-1 baseline exactly once**

Build the current Workbook binary from the implementation commit, then run the
documented command once. Do not modify product code in response to results.

Expected: both report files are produced. Scenarios may pass, miss, fail, or
time out; each outcome must be represented explicitly.

- [ ] **Step 4: Probe and run SHA-256 when supported**

Run:

```sh
sha_probe=$(mktemp -d /tmp/workbook-sha256-probe.XXXXXX)
git init --quiet --object-format=sha256 "$sha_probe"
```

When supported, run a second benchmark with
`--object-format sha256`, writing
`docs/performance/2026-07-28-baseline-sha256.json` and
`docs/performance/2026-07-28-baseline-sha256.md`. When unsupported, record the
probe error in the SHA-1 Markdown report and create no empty SHA-256 artifacts.

- [ ] **Step 5: Verify artifacts and the full repository**

Run: `jq -e '.format == "workbook.performance-report" and .version == 1 and .fixture.activeTasks == 500 and .fixture.operationsPerTask == 20' docs/performance/2026-07-28-baseline.json`

Expected: exit zero.

Run: `gofmt -w internal/perf/*.go cmd/workbook-bench/*.go`

Run: `go test ./...`

Run: `go vet ./...`

Run: `git diff --check`

Expected: every command succeeds. Inspect the Markdown report and require every
scenario to show a concrete result or timeout, never a missing measurement.

- [ ] **Step 6: Commit documentation and baseline artifacts**

```bash
git add README.md docs/performance
git commit -m "docs: record Workbook performance baseline"
```

- [ ] **Step 7: Mark the Workbook benchmark task complete**

Run:

```sh
workbook update WB-01KYD75VPPVW6SGH28X1ME9CZ5 \
  --status done \
  --json
```

Expected: the task becomes Done and retains its dependency on the completed
SQLite projection task.

## Final Review Checkpoint

- Confirm the benchmark executable is absent from `scripts/release.sh` archives.
- Confirm no SQLite database, temporary repository, Git trace, or compiled
  binary is tracked.
- Confirm fixture task tips contain complete `state.json` documents and exactly
  20 commits each.
- Confirm the baseline was run once after implementation, with no product
  optimization loop.
- Confirm `git status --short` is clean except for intentional Workbook private
  ref updates.
