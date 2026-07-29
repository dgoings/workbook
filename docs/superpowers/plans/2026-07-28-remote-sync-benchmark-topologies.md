# Remote Sync Benchmark Topologies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make remote synchronization benchmarks independently selectable,
deterministic, semantically verified, and reportable at the approved
500-task-by-20-operation scale.

**Architecture:** Extend the existing performance report and measured-command
boundaries without changing product sync behavior. A remote-topology fixture
layer builds independent repositories, while a scenario registry selects and
runs the correct topology and validates Workbook JSON plus canonical, tracking,
and bare-remote refs.

**Tech Stack:** Go 1.26+, Git plumbing and local bare remotes, Git Trace2,
Workbook JSON output, `flag`, and temporary integration repositories.

## Global Constraints

- Remote scenarios require at least 500 active tasks and 20 operations per task
  through the public `workbook-bench` command.
- No product synchronization code changes in this task.
- No direct reads from `.git/refs` and no assumed Git object-ID width.
- Every measured command has a caller-provided timeout and terminates its
  process group on cancellation.
- SHA-1 and supported SHA-256 repositories must have identical semantic checks.
- Each acceptance-sized topology is measured once per supported object format.
- A timeout is retained as lower-bound evidence; no replacement run occurs.

---

### Task 1: Preserve measured command output and report scenario targets

**Files:**
- Modify: `internal/perf/command.go`
- Modify: `internal/perf/command_test.go`
- Modify: `internal/perf/model.go`
- Modify: `internal/perf/model_test.go`

**Interfaces:**
- Produces: `type CommandMeasurement struct { Sample Sample; Stdout []byte; Stderr []byte }`
- Produces: `func MeasureCommandOutput(context.Context, CommandSpec) CommandMeasurement`
- Preserves: `func MeasureCommand(context.Context, CommandSpec) Sample`
- Produces: `type ScenarioTarget struct { MaxMilliseconds float64; MaxGitProcesses int }`
- Produces: `ScenarioResult.Target *ScenarioTarget` and `ScenarioResult.Outcome string`

- [ ] **Step 1: Write failing output-preservation tests**

Add a test that runs a shell command which writes distinct stdout and stderr,
exits 7, and asserts all bytes, exit code, duration, and Trace2 count remain
available:

```go
func TestMeasureCommandOutputPreservesStreamsAndCompatibilityWrapper(t *testing.T) {
    got := MeasureCommandOutput(context.Background(), CommandSpec{
        Binary: "/bin/sh",
        Args: []string{"-c", "printf stdout; printf stderr >&2; exit 7"},
        Directory: t.TempDir(),
        Timeout: time.Second,
    })
    if string(got.Stdout) != "stdout" || string(got.Stderr) != "stderr" {
        t.Fatalf("measurement = %#v", got)
    }
    if got.Sample.ExitCode != 7 || got.Sample.TimedOut {
        t.Fatalf("sample = %#v", got.Sample)
    }
}
```

- [ ] **Step 2: Run the command test and verify the missing symbol failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run TestMeasureCommandOutputPreservesStreamsAndCompatibilityWrapper -count=1
```

Expected: compilation fails because `MeasureCommandOutput` is undefined.

- [ ] **Step 3: Implement one shared measured-command path**

Move the current command execution into `MeasureCommandOutput`. Attach
`bytes.Buffer` instances to both stdout and stderr, retain their copied bytes,
and build `Sample.Error` from stderr only for unsuccessful commands.
Implement the compatibility wrapper as:

```go
func MeasureCommand(ctx context.Context, spec CommandSpec) Sample {
    return MeasureCommandOutput(ctx, spec).Sample
}
```

- [ ] **Step 4: Run focused command tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestMeasureCommand' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing report-outcome tests**

Cover inclusive time limits, exclusive process limits, timeouts, command
failures, and scenarios without targets:

```go
func TestReportNormalizesScenarioTargetOutcomes(t *testing.T) {
    target := &ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}
    report := Report{Scenarios: []ScenarioResult{
        {Name: "pass", Target: target, Samples: []Sample{{Duration: 2 * time.Second, GitProcesses: 19}}},
        {Name: "process-miss", Target: target, Samples: []Sample{{Duration: time.Second, GitProcesses: 20}}},
        {Name: "timeout", Target: target, Samples: []Sample{{Duration: 60 * time.Second, TimedOut: true}}},
        {Name: "local", Samples: []Sample{{Duration: time.Millisecond}}},
    }}
    normalized := report.normalized()
    got := make(map[string]string, len(normalized.Scenarios))
    for _, scenario := range normalized.Scenarios {
        got[scenario.Name] = scenario.Outcome
    }
    want := map[string]string{
        "pass": "pass", "process-miss": "miss",
        "timeout": "timeout", "local": "not-evaluated",
    }
    if !reflect.DeepEqual(got, want) {
        t.Fatalf("outcomes = %#v, want %#v", got, want)
    }
}
```

- [ ] **Step 6: Run the report test and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run TestReportNormalizesScenarioTargetOutcomes -count=1
```

Expected: compilation fails because target fields are undefined.

- [ ] **Step 7: Implement target normalization and Markdown columns**

Add the target type and additive JSON fields. Determine the normalized outcome
from the first sample because every remote topology has exactly one measured
sample. Add `Target time`, `Target Git processes`, and `Outcome` columns to
Markdown. Render process targets with `< N`.

- [ ] **Step 8: Run model and command package tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestReport|TestSummarize|TestMeasureCommand' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit measured evidence and target reporting**

```sh
git add internal/perf/command.go internal/perf/command_test.go internal/perf/model.go internal/perf/model_test.go
git commit -m "feat: report benchmark target outcomes"
```

### Task 2: Add repeatable scenario selection

**Files:**
- Create: `internal/perf/registry.go`
- Create: `internal/perf/registry_test.go`
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Produces: `func ScenarioNames() []string`
- Produces: `func ResolveScenarios([]string) ([]string, error)`
- Produces: repeatable `--scenario <name>` command option
- Preserves: omitted selector runs the complete registry

- [ ] **Step 1: Write registry tests for stable order and rejection**

Test omitted selection, caller order independence, duplicate rejection, and an
unknown scenario error that lists valid names. Include every existing local
scenario and every new remote scenario in the expected registry.

- [ ] **Step 2: Run the registry tests and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestResolveScenarios|TestScenarioNames' -count=1
```

Expected: compilation fails because the registry functions do not exist.

- [ ] **Step 3: Implement the immutable registry**

Define one ordered `[]string`, return defensive copies, and resolve a requested
set by iterating the registry rather than the flag order. Reject duplicates
before resolution.

- [ ] **Step 4: Write failing CLI option tests**

Add tests that parse repeated selectors, reject duplicates and unknown names,
and reject a remote selector below 500 tasks or 20 operations. Change the
small end-to-end test to select one existing local scenario.

- [ ] **Step 5: Run CLI tests and verify the missing flag behavior**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench -run 'TestRunResolves|TestValidateOptions|TestScenario' -count=1
```

Expected: tests fail because `--scenario` is not registered.

- [ ] **Step 6: Implement repeatable flag parsing and selected run routing**

Add a small `stringListFlag` implementing `flag.Value`. Resolve selectors
during option validation and store the stable names on `options`. Split
`runBenchmark` so local scenario groups run only when selected; preserve a
single final report.

- [ ] **Step 7: Run registry and CLI tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -run 'TestResolveScenarios|TestScenario|TestRunResolves|TestValidateOptions' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit scenario selection**

```sh
git add internal/perf/registry.go internal/perf/registry_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "feat: select benchmark scenarios"
```

### Task 3: Build deterministic remote topology fixtures

**Files:**
- Create: `internal/perf/remote_fixture.go`
- Create: `internal/perf/remote_fixture_test.go`
- Modify: `internal/perf/fixture.go`

**Interfaces:**
- Produces: `type RemoteTopology string`
- Produces: `type RemoteFixture struct { LocalRoot string; PeerRoot string; OriginRoot string; Config core.ProjectConfig; TaskIDs []string; Expected map[string]ExpectedRefs }`
- Produces: `func BuildRemoteFixture(context.Context, string, FixtureSpec, RemoteTopology) (RemoteFixture, error)`
- Produces: `RemoteBuriedCheckpointCorruption` for the later validator task

- [ ] **Step 1: Write small SHA-1 topology integration tests**

For each topology, build 10 tasks by 4 operations and inspect refs only through
`git for-each-ref` and `git ls-remote`. Assert:

- fresh checkout has no canonical task refs before measurement;
- initial publication has an empty remote;
- synchronized refs match in all namespaces;
- changed-set uses five local and five remote tasks;
- divergent children share one parent;
- malformed tips point to commits with invalid trees;
- buried corruption is not at the tip.

- [ ] **Step 2: Run topology fixture tests and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run TestBuildRemoteFixture -count=1
```

Expected: compilation fails because `BuildRemoteFixture` is undefined.

- [ ] **Step 3: Extract deterministic operation/commit helpers**

Expose package-private helpers from `fixture.go` that can append one valid
operation commit from an explicit parent and can encode deliberately mismatched
stored state without weakening production validation.

- [ ] **Step 4: Implement remote repository construction**

Create the code/config branch needed for local clones, initialize a same-format
bare origin, and use explicit refspecs for setup. Build every topology in its
own directory. Use sorted task IDs for the selected changed tasks.

- [ ] **Step 5: Implement malformed and buried corruption writers**

Use `git hash-object`, `mktree`, `commit-tree`, and `update-ref` against explicit
validated paths and object IDs. Never force a product sync operation; force is
allowed only while constructing the private temporary bare fixture.

- [ ] **Step 6: Add supported SHA-256 subtests**

Probe `git init --object-format=sha256`; skip only when Git reports that the
format is unsupported. Run the same topology assertions otherwise.

- [ ] **Step 7: Run fixture package tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestBuildFixture|TestBuildRemoteFixture' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit topology fixtures**

```sh
git add internal/perf/fixture.go internal/perf/remote_fixture.go internal/perf/remote_fixture_test.go
git commit -m "feat: build remote benchmark topologies"
```

### Task 4: Run and semantically verify remote scenarios

**Files:**
- Create: `internal/perf/remote_scenarios.go`
- Create: `internal/perf/remote_scenarios_test.go`
- Modify: `cmd/workbook-bench/main.go`
- Modify: `cmd/workbook-bench/main_test.go`

**Interfaces:**
- Produces: `func RunRemoteScenarios(context.Context, RunSpec, string, []string) ([]ScenarioResult, error)`
- Consumes: `MeasureCommandOutput`, `BuildRemoteFixture`, and the scenario registry
- Verifies: versioned `fetch`, `push`, and `sync` result envelopes plus refs

- [ ] **Step 1: Write failing result-decoding tests**

Provide real JSON documents for successful fetch/push/sync and nonzero
divergence/corruption outcomes. Assert exact task statuses and reject a wrong
format, version, command, duplicate task result, missing expected task, and
unexpected task result.

- [ ] **Step 2: Run decoding tests and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run TestDecodeRemoteScenarioResult -count=1
```

Expected: compilation fails because the decoder is undefined.

- [ ] **Step 3: Implement strict result decoding**

Decode the existing `workbook.result` envelope and command-specific data into
`gitstore.SyncResult` or `gitstore.SyncRunResult`. Decode stderr's
`workbook.error` envelope for expected nonzero outcomes. Compare sorted exact
`taskId/status` pairs.

- [ ] **Step 4: Write failing scenario tests with injected measurement**

For each topology, inject a measurer that runs the real small-fixture Workbook
command and then returns a fixed Git-process count. Assert command choice,
target, output verification, and post-command refs. Add a scaling test that
returns the same count for 10x4 and 25x7 fixtures.

- [ ] **Step 5: Run scenario tests and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestRunRemote|TestRemoteScenarioProcessCount' -count=1
```

Expected: compilation fails because the runner is undefined.

- [ ] **Step 6: Implement scenario preparation, measurement, and verification**

Map each name to a topology, Workbook command, expected output, ref invariant,
and approved target. Build only selected scenarios. Treat setup/JSON/ref
assertion errors as fatal; retain product timeouts and expected nonzero product
outcomes as samples.

- [ ] **Step 7: Wire selected remote scenarios into the command**

Call `RunRemoteScenarios` once with the selected remote names and append its
results to selected local results. Ensure a remote-only invocation does not
build local mutation or projection fixtures.

- [ ] **Step 8: Run all benchmark harness tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit remote scenario execution**

```sh
git add internal/perf/remote_scenarios.go internal/perf/remote_scenarios_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "feat: benchmark remote sync topologies"
```

### Task 5: Document and record the one-shot baseline

**Files:**
- Modify: `docs/performance/README.md`
- Modify: `README.md`
- Create: `docs/performance/2026-07-28-sync-baseline-sha1.json`
- Create: `docs/performance/2026-07-28-sync-baseline-sha1.md`
- Create when supported: `docs/performance/2026-07-28-sync-baseline-sha256.json`
- Create when supported: `docs/performance/2026-07-28-sync-baseline-sha256.md`

**Interfaces:**
- Documents: repeatable selectors, scenario names, workload minimum, and
  lower-bound timeout interpretation
- Records: one sample for every remote topology in each supported object format

- [ ] **Step 1: Update documentation tests first**

Extend existing README assertions to require `--scenario`,
`sync-fresh-checkout`, the 500-by-20 remote minimum, and the statement that
timeouts are lower-bound evidence.

- [ ] **Step 2: Run documentation tests and verify failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench ./internal/cli -run 'README|Help' -count=1
```

Expected: at least one new documentation assertion fails.

- [ ] **Step 3: Update performance and repository documentation**

Add exact commands for remote-only SHA-1 and SHA-256 baselines and explain
per-scenario target outcomes. Keep the existing incomplete whole-harness
baseline record intact.

- [ ] **Step 4: Run documentation and full correctness checks**

Run:

```sh
gofmt -w cmd/workbook-bench/*.go internal/perf/*.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Expected: every command succeeds.

- [ ] **Step 5: Build the measured Workbook binary**

Run:

```sh
go build -buildvcs=false -o /private/tmp/workbook-sync-baseline ./cmd/workbook
```

Expected: `/private/tmp/workbook-sync-baseline` is created successfully.

- [ ] **Step 6: Run the one authorized SHA-1 baseline**

Run one command selecting all seven remote scenarios with:

```sh
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-sync-baseline \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --scenario sync-fresh-checkout \
  --scenario sync-initial-publication \
  --scenario sync-already-synchronized \
  --scenario sync-small-changed-ref-set \
  --scenario sync-divergent-tips \
  --scenario sync-malformed-local-tip \
  --scenario sync-malformed-remote-tip \
  --output-json docs/performance/2026-07-28-sync-baseline-sha1.json \
  --output-markdown docs/performance/2026-07-28-sync-baseline-sha1.md
```

Expected: a report is written even when product scenarios time out or miss.

- [ ] **Step 7: Probe SHA-256 and run it once when supported**

Use a temporary bare repository to probe `git init --object-format=sha256`.
When supported, run the identical selector set with `--object-format sha256`
and the SHA-256 output paths. Do not replace either measurement.

- [ ] **Step 8: Verify generated report completeness**

Decode each JSON report, require seven exact scenario names, one sample per
scenario, nonempty environment metadata, targets, outcomes, and matching
Markdown scenario rows.

- [ ] **Step 9: Commit documentation and baseline evidence**

```sh
git add README.md docs/performance
git commit -m "docs: record remote sync baseline"
```

- [ ] **Step 10: Run final branch verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Expected: tests and vet pass, no whitespace errors exist, and only intentional
committed files are present.
