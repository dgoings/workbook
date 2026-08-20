# Remote Sync Benchmark Topologies Design

## Goal

Split Workbook's remote synchronization benchmark into independently selectable,
deterministic topologies. Each topology must run against local repositories and
a local bare `origin`, retain JSON and Markdown reporting, verify the product's
per-task results and resulting refs, and measure Git process cardinality without
changing synchronization behavior.

## Scope

This task changes only the development benchmark harness and its documentation.
It does not optimize `fetch`, `push`, or `sync`. A slow command or timeout is
recorded as baseline evidence.

The harness covers these remote topologies:

- `sync-fresh-checkout`: a fresh checkout fetches 500 populated remote task
  refs into canonical and isolated tracking refs.
- `sync-initial-publication`: a populated local repository publishes to an
  empty bare remote.
- `sync-already-synchronized`: local, tracking, and remote task refs already
  match.
- `sync-small-changed-ref-set`: five tasks are local-ahead and five disjoint
  tasks are remote-ahead.
- `sync-divergent-tips`: one task has distinct local and remote descendants of
  the same parent.
- `sync-malformed-local-tip`: one owned local task ref points to a malformed
  current commit.
- `sync-malformed-remote-tip`: one fetched tracking ref points to a malformed
  current commit and remains isolated.

The fixture layer also creates a buried semantic-checkpoint corruption topology.
The malformed checkpoint is followed by a structurally valid descendant so
tip-only validation cannot detect the older mismatch. No sync benchmark is
registered for that fixture; the explicit validator task consumes it later.

## Scenario Selection

`workbook-bench` accepts repeatable `--scenario <name>` flags. Omitting the flag
runs the existing local scenarios plus every registered remote-sync scenario.
Supplying one or more flags runs only those scenarios and rejects unknown or
duplicate names before creating fixtures.

Existing local scenarios remain selectable by their current report names. The
new remote scenarios always require at least 500 active tasks and 20 operations
per task, even during a baseline phase. Small unit and integration tests call
the internal runner directly with smaller fixtures rather than weakening the
command contract.

Scenario execution order follows a stable registry order, independent of flag
order. This makes repeated JSON and Markdown reports easy to compare.

## Deterministic Topology Fixtures

`internal/perf/remote_fixture.go` owns remote topology creation. It builds from
the existing seeded `BuildFixture` histories, creates bare remotes with the same
Git object format, and copies repositories through local Git operations.

Each topology receives an independent root. Setup and verification are outside
the measured interval. Topology mutation helpers use explicit object IDs and
ref names; they never read `.git/refs`, assume a hash width, or touch code refs.

The five-ahead/five-behind fixture uses ten distinct sorted task IDs. The
divergence fixture writes two valid child commits from one common task head. A
malformed-tip fixture points only the selected task ref at a deterministic
commit with an invalid task tree. The buried-corruption fixture replaces an
older checkpoint with canonical JSON that does not equal
`Apply(parent.state, operation.json)`, then writes a valid descendant whose
state is derived from that stored checkpoint.

## Measured Command Evidence

The command measurement boundary gains an output-preserving variant:

```go
type CommandMeasurement struct {
    Sample Sample
    Stdout []byte
    Stderr []byte
}

func MeasureCommandOutput(context.Context, CommandSpec) CommandMeasurement
```

`MeasureCommand` remains as a compatibility wrapper returning only `Sample`.
Both functions retain the existing timeout, process-group termination, and
Trace2 Git-process counting behavior.

Remote scenario runners decode the versioned Workbook JSON result even when the
command exits nonzero with a product error. They verify the expected per-task
status set and inspect canonical, tracking, and bare-remote refs after the
command. A successful process with incorrect output or refs is a harness
verification error, not a passing sample.

Product timeouts and expected product failures remain reportable samples. A
timeout's elapsed duration is lower-bound evidence. Harness setup, malformed
JSON, or assertion failures remain fatal because they make the measurement
untrustworthy.

## Targets and Reports

Each remote `ScenarioResult` carries its own optional target:

```go
type ScenarioTarget struct {
    MaxMilliseconds float64 `json:"maxMilliseconds"`
    MaxGitProcesses int     `json:"maxGitProcesses"`
}
```

Normalization assigns an outcome of `pass`, `miss`, `timeout`, `failed`, or
`not-evaluated`. Existing local scenario reports remain compatible and use
`not-evaluated` when they have no per-scenario target.

The approved remote targets are:

| Scenario | Time | Git processes |
| --- | ---: | ---: |
| fresh checkout | 5 seconds | fewer than 20 |
| initial publication | 5 seconds | fewer than 20 |
| already synchronized | 1 second | fewer than 10 |
| small changed-ref set | 2 seconds | fewer than 20 |
| divergent or malformed tip | 2 seconds | fewer than 20 |

Because the wording is strictly "fewer than", the process limits are exclusive:
19 passes a limit of 20 and 20 misses it. Time limits are inclusive.

JSON remains `workbook.performance-report` version 1 with additive fields.
Markdown adds target and outcome columns while preserving the existing report
content. Baseline reports describe targets as reference budgets, not achieved
guarantees.

## Process-Count Scaling Proof

Ordinary tests run the same topology with different task counts and history
depths through an injected command measurer. They assert the same measured Git
process count and the same number of harness Git invocations. Integration tests
use small SHA-1 and supported SHA-256 repositories to verify real ref outcomes.
The acceptance-sized baseline supplies real Trace2 counts.

## Baseline Recording

After correctness tests pass, each remote topology is run exactly once at
500 tasks by 20 operations for SHA-1 and, when supported, SHA-256. Each command
has a 60-second timeout. The run writes date-stamped JSON and Markdown reports
under `docs/performance/`.

There are no replacement measurements and no synchronization tuning in this
task. A timeout, product failure, or target miss is committed as the observed
baseline outcome.

## Documentation

`docs/performance/README.md` documents scenario names, repeatable selection,
minimum remote workload, output interpretation, and exact baseline commands.
The repository README continues to describe targets as evidence budgets rather
than achieved guarantees.
