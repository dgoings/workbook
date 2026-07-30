# Packed SHA-256 Repository Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the packed repository benchmark construct a same-object-format bare origin, retain repository execution failures correctly, and record focused SHA-1/SHA-256 acceptance evidence.

**Architecture:** `MeasureRepository` will delegate local-bare setup and measurement to a helper that asks Git for the fixture's actual object format, initializes the caller-owned origin with that format, and reuses the existing sync measurement function. Report normalization will give execution failures precedence over the absence of a performance target, while the benchmark command's retained-failure gate will expand only to the repository surface so expected-error remote topology behavior stays unchanged.

**Tech Stack:** Go 1.26+, Git SHA-1/SHA-256 repositories, Git porcelain sync integration tests, `workbook-bench`, JSON and Markdown acceptance reports.

## Global Constraints

- Preserve Git as the canonical durable store and SQLite as a disposable projection.
- Do not change Workbook sync refspecs, transport behavior, or push-porcelain parsing.
- Detect the source repository's actual object format with Git; do not trust ambient `init.defaultObjectFormat`.
- Keep packing, garbage collection, object-format discovery, and origin construction outside measured sync samples.
- Verify the complete remote task-ref map equals the complete local canonical task-ref map.
- A successful repository scenario without a performance target remains `not-evaluated`; timeout and product/harness execution failure take precedence.
- Return nonzero after retaining JSON and Markdown when a local CLI, warm API, or repository sample fails; do not include expected-error `remote-sync` scenarios in that gate.
- Acceptance uses exactly 500 total task refs, 475 active tasks, 25 tombstones, 20 operations per task, and one sample.
- Use one frozen Workbook binary and one frozen benchmark binary for both acceptance object formats.
- Do not run remote topology, validation, scale-slope, many-changed projection, or resource-growth acceptance scenarios.

---

### Task 1: Preserve object format in packed repository sync

**Files:**
- Modify: `internal/perf/scenarios.go:440-540`
- Modify: `internal/perf/scenarios_test.go:1256-1323`

**Interfaces:**
- Consumes: `runRepositoryGit`, `measureLocalBareSync`, `fixtureRefMap`, `fixtureRemoteRefMap`, and `supportsObjectFormat`
- Produces: `measureLocalBareSyncAgainstNewOrigin(ctx context.Context, workbookBinary, fixtureRoot, origin string, commandTimeout time.Duration, measureCommand func(context.Context, CommandSpec) Sample) ([]ScenarioResult, error)`

- [ ] **Step 1: Replace the SHA-1-only repository test with a cross-format regression**

Make `TestMeasureRepository` table-driven over literal formats:

```go
for _, objectFormat := range []string{"sha1", "sha256"} {
    t.Run(objectFormat, func(t *testing.T) {
        if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
            return
        }
        binary := buildWorkbookBinary(t)
        fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
            TotalTasks: 10, ActiveTasks: 10,
            OperationsPerTask: 2,
            ObjectFormat:      objectFormat,
        })
        if err != nil {
            t.Fatal(err)
        }
        metrics, results, err := MeasureRepository(context.Background(), binary, fixture.Root, 60*time.Second)
        if err != nil {
            t.Fatal(err)
        }
        // Retain the existing exact scenario names, successful samples,
        // and positive repository-metric assertions.
    })
}
```

Before any production edit, set the process default explicitly to SHA-1 inside the SHA-256 subtest so the regression is deterministic:

```go
t.Setenv("GIT_CONFIG_COUNT", "1")
t.Setenv("GIT_CONFIG_KEY_0", "init.defaultObjectFormat")
t.Setenv("GIT_CONFIG_VALUE_0", "sha1")
```

The production change that this test catches is removal of explicit origin object-format propagation.

- [ ] **Step 2: Run the regression and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run '^TestMeasureRepository/sha256$' -count=1 -v -timeout=120s
```

Expected: FAIL in `sync-initial-local-bare` with the retained product error containing:

```text
Git returned 0 push outcomes, want 10
```

- [ ] **Step 3: Extract same-format origin setup**

Replace the inline origin initialization, remote addition, and call to `measureLocalBareSync` in `MeasureRepository` with:

```go
syncResults, err := measureLocalBareSyncAgainstNewOrigin(
    ctx,
    workbookBinary,
    fixtureRoot,
    filepath.Join(originRoot, "origin.git"),
    commandTimeout,
    MeasureCommand,
)
```

Implement the helper with this behavior:

```go
func measureLocalBareSyncAgainstNewOrigin(
    ctx context.Context,
    workbookBinary string,
    fixtureRoot string,
    origin string,
    commandTimeout time.Duration,
    measureCommand func(context.Context, CommandSpec) Sample,
) ([]ScenarioResult, error) {
    output, _, err := runRepositoryGit(
        ctx, commandTimeout, fixtureRoot, "rev-parse", "--show-object-format",
    )
    if err != nil {
        return nil, err
    }
    objectFormat := strings.TrimSuffix(string(output), "\n")
    if objectFormat == "" || strings.ContainsAny(objectFormat, "\r\n\t ") {
        return nil, fmt.Errorf("Git returned invalid repository object format %q", objectFormat)
    }
    if _, _, err := runRepositoryGit(
        ctx, commandTimeout, "", "init", "--bare", "--quiet",
        "--object-format="+objectFormat, origin,
    ); err != nil {
        return nil, err
    }
    if _, _, err := runRepositoryGit(
        ctx, commandTimeout, fixtureRoot, "remote", "add", "origin", origin,
    ); err != nil {
        return nil, err
    }
    return measureLocalBareSync(
        ctx, workbookBinary, fixtureRoot, commandTimeout, measureCommand,
    )
}
```

Use argument-vector execution exactly as shown; do not invoke a shell.

- [ ] **Step 4: Add exact-ref integration coverage**

Add `TestMeasureLocalBareSyncAgainstNewOriginPreservesPackedRefs` beside `TestMeasureRepository`. For each supported literal object format:

1. Build a 10-task, two-operation fixture.
2. Run `git pack-refs --all` and `git gc` with `runRepositoryGit`.
3. Call `measureLocalBareSyncAgainstNewOrigin` with an origin under `t.TempDir()` and real `MeasureCommand`.
4. Assert the two result names are exactly `sync-initial-local-bare` and `sync-unchanged-local-bare`.
5. Assert every sample has `ExitCode == 0`, `TimedOut == false`, and `Error == ""`.
6. Assert:

```go
canonical := fixtureRefMap(t, fixture.Root, "refs/workbook/tasks/")
remote := fixtureRemoteRefMap(t, origin)
if !reflect.DeepEqual(remote, canonical) {
    t.Fatalf("remote task refs = %#v, want exact canonical refs %#v", remote, canonical)
}
```

The production mutation this assertion catches is an incomplete or wrong destination refspec that still returns an overall successful command.

- [ ] **Step 5: Verify GREEN and surrounding repository behavior**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestMeasureRepository|TestMeasureLocalBareSyncAgainstNewOriginPreservesPackedRefs|TestMeasureRepositoryRunsUnchangedSyncOnlyAfterInitialCompletes' -count=1 -v -timeout=180s
```

Expected: PASS in both supported object formats; exact refs match.

- [ ] **Step 6: Format and commit**

Run:

```sh
gofmt -w internal/perf/scenarios.go internal/perf/scenarios_test.go
git diff --check
git add internal/perf/scenarios.go internal/perf/scenarios_test.go
git commit -m "fix: preserve packed repository object format"
```

### Task 2: Surface repository execution failures

**Files:**
- Modify: `internal/perf/model.go:204-227`
- Modify: `internal/perf/model_test.go:192-272`
- Modify: `cmd/workbook-bench/main.go:73-126`
- Modify: `cmd/workbook-bench/main_test.go:91-190`

**Interfaces:**
- Consumes: `ScenarioResult.Surface`, normalized scenario outcomes, and retained `Sample` execution fields
- Produces: `hasFailedRetainedMeasurement(report perf.Report) bool`

- [ ] **Step 1: Add failing no-target outcome cases**

Extend `TestReportNormalizesScenarioTargetOutcomes` with these literal scenarios:

```go
{
    Name:    "repository-success",
    Surface: "repository",
    Samples: []Sample{{Duration: time.Millisecond}},
},
{
    Name:    "repository-failure",
    Surface: "repository",
    Samples: []Sample{{Duration: time.Millisecond, ExitCode: 1, Error: "sync failed"}},
},
{
    Name:    "repository-timeout",
    Surface: "repository",
    Samples: []Sample{{Duration: time.Second, TimedOut: true, Error: "timed out"}},
},
```

Add exact expected outcomes:

```go
"repository-success": "not-evaluated",
"repository-failure": "failed",
"repository-timeout": "timeout",
```

The production mutation this catches is returning `not-evaluated` before inspecting execution status.

- [ ] **Step 2: Add a retained repository failure command test**

Generalize the table in `TestRunRetainsLocalMeasurementOutcomesAndReturnsExecutionStatus` to include scenario name, surface, and optional target. Add:

```go
{
    name:         "repository failure exits one after reports",
    scenarioName: "sync-initial-local-bare",
    surface:      "repository",
    sample:       perf.Sample{Duration: time.Millisecond, ExitCode: 1, Error: "sync failed"},
    wantExitCode: failureExitCode,
    wantOutcome:  "failed",
},
```

Keep the valid targeted local miss case expecting zero. In every table row, continue asserting that both retained reports exist and contain the exact normalized outcome.

The production mutation this catches is limiting the post-report failure gate to `cli-` and `api-` scenario names.

- [ ] **Step 3: Run both focused tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run '^TestReportNormalizesScenarioTargetOutcomes$' -count=1 -v
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench -run '^TestRunRetainsLocalMeasurementOutcomesAndReturnsExecutionStatus$' -count=1 -v
```

Expected:

- model test FAIL because repository failure/timeout normalize to `not-evaluated`;
- command test FAIL because the retained repository failure exits zero.

- [ ] **Step 4: Give execution status precedence over no target**

Refactor `scenarioOutcome` so it:

1. returns `failed` for an empty sample set only when a target exists;
2. scans samples before checking the target;
3. returns `timeout` if any sample timed out;
4. otherwise returns `failed` if any sample has a nonzero exit or error;
5. returns `not-evaluated` when `Target == nil`;
6. evaluates miss/pass only for targeted, successfully executed samples.

Preserve the existing order-resistant timeout-over-failure and failure-over-miss behavior.

- [ ] **Step 5: Expand only the retained repository failure gate**

Rename `hasFailedLocalMeasurement` to `hasFailedRetainedMeasurement`. Select scenarios by surface:

```go
switch scenario.Surface {
case "cold-cli", "warm-http", "repository":
default:
    continue
}
```

For selected surfaces, retain the existing condition:

```go
if sample.TimedOut || sample.ExitCode != 0 || sample.Error != "" {
    return true
}
```

Update the stderr line to:

```text
workbook-bench: measurement failed; see retained reports
```

Do not include `remote-sync` because its correctness topologies intentionally retain expected nonzero product commands.

- [ ] **Step 6: Verify GREEN and command semantics**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf -run 'TestReportNormalizesScenarioTargetOutcomes|TestScenarioOutcomeDurationPolicies' -count=1 -v
GOCACHE=/private/tmp/workbook-gocache go test ./cmd/workbook-bench -run 'TestRunRetainsLocalMeasurementOutcomesAndReturnsExecutionStatus|TestRunRejects' -count=1 -v
```

Expected: PASS; reports are written before the nonzero return, repository failures are `failed`, successful untargeted repository samples are `not-evaluated`, and valid local misses return zero.

- [ ] **Step 7: Format and commit**

Run:

```sh
gofmt -w internal/perf/model.go internal/perf/model_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git diff --check
git add internal/perf/model.go internal/perf/model_test.go cmd/workbook-bench/main.go cmd/workbook-bench/main_test.go
git commit -m "fix: fail retained repository measurements"
```

### Task 3: Verify and record focused acceptance evidence

**Files:**
- Create: `docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.json`
- Create: `docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.md`
- Create: `docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.json`
- Create: `docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.md`
- Create: `docs/performance/2026-07-30-packed-repository-sync-acceptance-provenance.md`
- Modify: `docs/performance/README.md`

**Interfaces:**
- Consumes: the committed Task 1 and Task 2 source tree
- Produces: one frozen product binary, one frozen harness binary, and focused functional acceptance evidence in both object formats

- [ ] **Step 1: Run focused and full verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/perf ./cmd/workbook-bench -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1 -timeout=300s
GOCACHE=/private/tmp/workbook-gocache go vet ./...
gofmt -l .
git diff --check
```

Expected: every command exits zero and `gofmt -l .` prints nothing.

- [ ] **Step 2: Freeze source identity and build binaries once**

Record:

```sh
git rev-parse HEAD
go version
git --version
uname -m
```

Build from the recorded literal commit:

```sh
WB_PACKED_SOURCE_COMMIT=$(git rev-parse HEAD)
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -ldflags "-X main.commit=$WB_PACKED_SOURCE_COMMIT" -o /private/tmp/workbook-packed-repository-acceptance ./cmd/workbook
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-bench-packed-repository-acceptance ./cmd/workbook-bench
shasum -a 256 /private/tmp/workbook-packed-repository-acceptance /private/tmp/workbook-bench-packed-repository-acceptance
```

Verify `WB_PACKED_SOURCE_COMMIT` is the exact 40-character output recorded above;
do not rebuild either binary between formats.

- [ ] **Step 3: Run SHA-1 acceptance once**

Run exactly:

```sh
/private/tmp/workbook-bench-packed-repository-acceptance \
  --workbook /private/tmp/workbook-packed-repository-acceptance \
  --phase acceptance \
  --tasks 500 \
  --tombstones 25 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --scenario sync-initial-local-bare \
  --scenario sync-unchanged-local-bare \
  --output-json docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.json \
  --output-markdown docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.md
```

Expected: exit zero; both scenarios have one completed sample, no timeout, no error, and `not-evaluated` outcome because no performance target exists.

- [ ] **Step 4: Run SHA-256 acceptance once**

Use the identical binaries, task population, history depth, sample count, and timeout, changing only:

```text
--object-format sha256
--output-json docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.json
--output-markdown docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.md
```

Expected: the same functional completion contract as SHA-1. Do not retry, tune, or replace either invocation.

- [ ] **Step 5: Write provenance and README summary**

In `2026-07-30-packed-repository-sync-acceptance-provenance.md`, record:

- the exact source commit;
- the two exact binary paths and SHA-256 checksums;
- Git, Go, OS architecture, and date;
- the exact two build commands and checksum command;
- that neither binary was rebuilt between formats;
- that each acceptance invocation ran once;
- that the reports selected only the two packed repository sync scenarios;
- that 500 total refs means 475 active and 25 tombstoned.

Add a focused section to `docs/performance/README.md` linking all five artifacts and stating:

- initial and unchanged packed local-bare sync completed in both formats;
- remote refs were verified exactly by the automated integration test;
- no repository latency or process budget was introduced;
- unrelated future-story acceptance families were not rerun.

- [ ] **Step 6: Validate retained evidence**

For each JSON report, verify with `jq`:

```sh
jq '{phase, fixture, scenarios: [.scenarios[] | {name, outcome, completed: .summary.completed, timedOut: .summary.timedOut, exitCode: .samples[0].exitCode, error: .samples[0].error}]}' <report>
```

Expected literals:

- `phase`: `acceptance`
- `fixture.totalTasks`: `500`
- `fixture.activeTasks`: `475`
- `fixture.tombstonedTasks`: `25`
- `fixture.operationsPerTask`: `20`
- exactly two scenario names
- every outcome: `not-evaluated`
- every completed: `1`
- every timedOut: `0`
- every exitCode: `0`
- every error: absent or empty
- identical non-`unknown` source commit and product checksum across formats

Verify the Markdown rows match those JSON values and run:

```sh
git diff --check
git status --short
```

- [ ] **Step 7: Commit evidence**

Run:

```sh
git add \
  docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.json \
  docs/performance/2026-07-30-packed-repository-sync-acceptance-sha1.md \
  docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.json \
  docs/performance/2026-07-30-packed-repository-sync-acceptance-sha256.md \
  docs/performance/2026-07-30-packed-repository-sync-acceptance-provenance.md \
  docs/performance/README.md
git commit -m "docs: record packed repository sync acceptance"
```
