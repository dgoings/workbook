# Local Performance Acceptance Semantics Design

## Goal

Make Workbook's local mutation benchmark produce acceptance-conformant evidence
without changing the synchronous Git-durable product path. The harness must
measure mutations against a representative deterministic fixture, keep
projection bootstrap outside mutation timing, isolate every scenario sample,
and classify local targets without manual arithmetic.

This design amends the original performance envelope to use at least 500 total
task refs, including at least 25 tombstoned tasks, rather than 500 active tasks.
The default acceptance fixture is therefore 500 total tasks, 475 active tasks,
25 tombstoned tasks, and 20 operations per task. This is an order-of-magnitude
architectural workload, not a fine-grained supported-capacity limit.

## Non-goals

- Do not optimize the product mutation path.
- Do not add an asynchronous durability boundary, background daemon, or
  persistent Git writer.
- Do not introduce a local Git-process-count budget.
- Do not rerun or replace the 2026-07-29 recorded evidence.
- Do not add many-changed projection, scaling-slope, or resource-growth
  measurements; separate follow-up stories own those changes.

## Fixture Contract

### Reported dimensions

`FixtureSpec` records these values explicitly:

- total task refs;
- active task refs;
- tombstoned task refs;
- operations per task; and
- Git object format.

The active and tombstoned counts must be non-negative, must sum to the total,
and must leave at least ten active tasks for the local scenarios. A fixture with
tombstones requires at least two operations per tombstoned task: creation and a
final tombstone.

The CLI keeps `--tasks` as the total task-ref count and adds `--tombstones`.
When `--tombstones` is omitted, it resolves to 25 for workloads of at least 500
tasks and one for smaller diagnostic workloads. An explicit zero remains valid
for a diagnostic baseline. Acceptance requires at least 500 total tasks, 25
tombstones, and 20 operations per task.

Because the durable fixture schema changes meaning, new reports use performance
report version 2. Historical version-1 evidence remains unchanged.

### Deterministic task plans

The fixture builder precomputes all task IDs before writing history. It then
creates one immutable task plan per ID. Plans use the existing fixed timestamp
origin and seeded ULID source and define:

- a nonblank title and realistically sized description;
- varied canonical status and priority values;
- at least one label selected from a small stable label vocabulary;
- a canonical rational rank, including non-integer fractions;
- zero or more acyclic dependencies on earlier active task IDs;
- a stable sequence of scalar, label, dependency, and rank operations; and
- whether the final operation is a tombstone.

The first 475 default plans remain active. The final 25 are tombstoned by their
last operation. No operation follows a tombstone. Every plan contains exactly
the requested operation count, and every stored `state.json` remains the
canonical result of applying its operation pack to its parent.

Dependencies always point backward to active task plans, so the graph is
acyclic and no active scenario target depends on a tombstoned task. The fixture
returns all task IDs, active task IDs, tombstoned task IDs, and stable direct
dependency pairs so scenario allocation never relies on undocumented indexes.

### Mutation witnesses

Fixture tests compare two independently built repositories and require equal
task IDs, equal final state documents, equal task heads, and equal history
lengths. They also assert the exact active/tombstoned counts and representative
descriptions, labels, dependencies, fractional ranks, and tombstone state.
Removing any representative dimension or changing the deterministic plan must
fail a test.

## Local Scenario Lifecycle

### Independent fixtures

Each selected local scenario and sample receives its own fixture directory.
Delete and restore never share a repository. Depend and free never share a
repository. Restore selects a fixture-provided tombstoned task; free selects a
fixture-provided direct dependency edge. Depend selects an active pair whose new
edge is valid and not already direct.

Cold and warm runners receive the selected scenario names and construct only
those scenarios. Selecting one local scenario must not build or mutate fixtures
for the rest of its family.

### Cold CLI projection boundary

After fixture construction and before opening the Trace2 cursor or mutation
timer, the harness runs:

```text
workbook rebuild --json
```

This setup command is not a benchmark sample. The harness validates its
versioned result and exact total task count. Setup failure aborts the scenario;
it is never folded into mutation latency or Git-process counts.

The existing `projection-rebuild` repository scenario remains the separate
measured bootstrap path. Local mutation rows therefore describe a seeded
projection, while bootstrap remains visible rather than disappearing from the
report.

### Warm HTTP projection boundary

Server startup continues to wait for `/healthz`. Before timing a mutation, the
harness then performs an untimed `GET /api/tasks`, consumes the full response,
and validates its versioned envelope and exact active-task count. The measured
Trace2 cursor is opened only after that request completes.

Every warm scenario/sample owns one server and one fixture. Server health alone
does not count as projection preparation.

### Setup-order witnesses

Dependency-injected runner tests record fixture construction, projection setup,
measurement, and cleanup events. They require projection setup exactly once
before every measured local sample. Tests also require unique fixture roots for
delete, restore, depend, and free. Removing either setup call or reintroducing a
shared pair must fail before any wall-clock assertion is involved.

## Target Semantics

`ScenarioTarget` describes the duration statistic and comparison explicitly
rather than treating every target as an inclusive per-sample maximum.

Supported duration policies are:

- nearest-rank p95 at or below a limit; and
- every successful sample strictly below a limit.

Local target assignment is:

| Scenarios | Policy |
| --- | --- |
| `cli-create`, `cli-delete`, `cli-depend`, `cli-free`, `cli-move`, `cli-restore`, `cli-update` | nearest-rank p95 at or below 200 ms |
| `api-update` | nearest-rank p95 at or below 100 ms |
| `cli-burst-independent-10`, `cli-burst-same-task-10`, `api-burst-independent-10`, `api-burst-same-task-10` | every sample strictly below 1,000 ms |

Remote and validation targets retain their existing inclusive time and exclusive
Git-process semantics. A zero or absent Git-process limit means the scenario has
no process budget and renders as `-` in Markdown.

Timeout has highest outcome precedence, followed by product/harness failure,
then target miss, then pass. P95 policies compare the completed-sample
nearest-rank p95. Every-sample policies inspect each successful sample. Exact
100 ms and 200 ms values pass their inclusive p95 targets; exact 1,000 ms burst
samples miss the strict target.

Both JSON and Markdown include the statistic and comparison, so consumers never
need to infer policy from the scenario name. This story does not change process
exit behavior solely because a target misses; reports remain the durable
acceptance evidence, and actual harness or product failures still exit nonzero.

## Provenance

The harness computes the measured Workbook binary's SHA-256 digest before
fixture construction and records it in the report environment alongside the
binary-reported version and commit.

A baseline diagnostic may report the normal source-build commit value
`unknown`. An acceptance invocation rejects `unknown` before fixture
construction, requiring the measured binary to be built with the exact source
commit injected through the existing release linker variable. Tests use a
deterministic temporary binary to prove the reported digest is exact and prove
that acceptance rejects unverifiable provenance.

The harness does not infer a source commit from its current directory because
the measured binary may have been built from another checkout.

## Documentation and Compatibility

The performance README documents:

- `--tasks` as total task refs;
- the `--tombstones` option and acceptance minimum;
- projection preparation outside mutation timing;
- `projection-rebuild` as bootstrap evidence;
- the three local duration policies; and
- required commit/checksum provenance for acceptance.

Existing version-1 JSON and Markdown evidence is not regenerated. Tests and
examples that construct diagnostic fixtures specify dimensions that leave
enough active tasks for their selected scenarios.

## Verification

Implementation proceeds test-first. Focused tests cover:

- deterministic representative SHA-1 and SHA-256 fixture states;
- exact 500/475/25 acceptance dimensions and 20-commit histories;
- selector-specific fixture construction;
- unique paired-scenario roots;
- cold rebuild before measurement;
- warm task load before measurement;
- inclusive p95 and strict every-sample boundary behavior;
- timeout, failure, and miss precedence;
- JSON and Markdown target-policy rendering;
- exact binary SHA-256 provenance;
- rejection of unknown acceptance commits; and
- unchanged remote and validation target behavior.

The final verification runs `go test ./...`, `go vet ./...`, formatting, diff
checks, and an independent review. No acceptance benchmark is rerun as part of
this story; the corrected harness enables the next explicit one-shot run.
