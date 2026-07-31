# Scaling Slope Measurement Design

## Goal

Measure how Workbook's local, synchronization, and validation paths respond to
two workload dimensions independently, instead of inferring both from the single
500-total-by-20-operation acceptance point.

The harness measures four fixture points: 100, 500, and 1,000 active tasks at
history depth 20, plus 500 active tasks at history depth 100. It records latency,
Git process count, realized fixture shape, and Git object format for every
measured scenario at every point, and derives descriptive slopes that separate
the task-count axis from the history-depth axis.

This story changes only the benchmark harness and its documentation. It does not
change product behavior, and it does not create, move, or relax any existing
performance target.

## Non-goals

- Do not change the product mutation, synchronization, projection, or validation
  paths.
- Do not attach the local acceptance duration targets, or any new pass/fail
  threshold, to a scaling point.
- Do not change the `--phase baseline` or `--phase acceptance` guards.
- Do not add many-changed projection, peak-resource, or storage-decomposition
  measurements; separate stories own those.
- Do not commit report files under `docs/performance/` from this branch. The
  SHA-1 and SHA-256 evidence is generated after all three in-flight performance
  stories are integrated so every report shares one source identity.

## Fixture-point mapping and rationale

The story names *active* tasks; `FixtureSpec` records total, active, and
tombstoned refs. `ScalingPointSpec` therefore names only the two story
dimensions and derives the realized fixture:

```text
tombstoned = max(1, active / 20)
total      = active + tombstoned
```

One tombstone per twenty active tasks is exactly the ratio of the default
acceptance fixture (25 tombstoned in 500 total). Holding the *ratio* constant
rather than the *count* keeps two properties that matter for slope reading:

- the task-count axis scales the whole repository exactly 1x, 5x, and 10x, so a
  ratio of measured values can be compared against a ratio of dimensions
  without correcting for a fixed tombstone overhead; and
- every point keeps a representative tombstone population, so the corrected
  fixture generator (titles, descriptions, labels, canonical statuses and
  priorities, fractional ranks, backward dependency edges, and a final tombstone
  operation) produces the same *kind* of repository at every point.

The realized matrix is:

| Point | Active | Tombstoned | Total refs | History depth |
| --- | ---: | ---: | ---: | ---: |
| `active-100-depth-20` | 100 | 5 | 105 | 20 |
| `active-500-depth-20` | 500 | 25 | 525 | 20 |
| `active-500-depth-100` | 500 | 25 | 525 | 100 |
| `active-1000-depth-20` | 1000 | 50 | 1050 | 20 |

The task-count axis is the three depth-20 points; the history-depth axis is the
two 500-active points. The `active-500-depth-20` point is shared by both axes,
which is what makes them separable from one run.

The minimum measurable point is ten active tasks, because the
`sync-small-changed-ref-set` topology needs ten active tasks to build its five
local-ahead and five remote-ahead sets. Depth must be at least two so a
tombstoned task can be created and then tombstoned. Both are rejected before any
fixture is built.

Nothing infers the realized shape after the fact. `BuildFixture` already fails
unless the repository contains exactly `TotalTasks` canonical refs, and the
untimed `workbook rebuild --json` setup already fails unless the projection
reports exactly `TotalTasks` tasks. The report records the same `FixtureSpec`
that produced the fixture, per point, including the object format.

## Measured scenarios

Nine scenarios are measured at every point, in the harness's stable registry
order:

| Scenario | Surface | Why |
| --- | --- | --- |
| `cli-create` | cold CLI | write path that adds a ref |
| `cli-depend` | cold CLI | write path that reads and writes related tasks |
| `cli-list` | cold CLI | read path across the active population |
| `cli-move` | cold CLI | rank write path |
| `cli-update` | cold CLI | projection-warm exact update |
| `sync-already-synchronized` | remote sync | no-change synchronization |
| `sync-small-changed-ref-set` | remote sync | small-change synchronization |
| `validate-full-history` | history validation | full audit, no cache |
| `validate-cached-unchanged` | history validation | cached validation |

`cli-list` is new. It follows the existing cold-CLI pattern exactly: an
independent fixture per scenario and sample, an untimed `workbook rebuild
--json` that validates the versioned result and exact task count, and only then
the timed `workbook list --json`. It deliberately carries **no** duration
target. No read-path budget has been approved, and borrowing the 200 ms
single-mutation budget would publish a classification nobody agreed to. It is
therefore reported descriptively, and its outcome is `not-evaluated` unless the
command actually failed or timed out.

The matrix reuses `RunColdCLI`, `RunRemoteScenarios`, and
`RunValidationScenarios` rather than reimplementing measurement, so every
existing timing boundary is inherited unchanged: fixture construction,
projection seeding, remote topology construction, and validation cache seeding
all remain outside the timed command and outside its Trace2 process count.

## Report schema

Scaling evidence is a separate versioned format, `workbook.performance-scaling-report`
version 1. It is not `workbook.performance-report`, because that format carries
one fixture and one target classification per scenario, and scaling evidence
carries several fixtures and no classification.

```json
{
  "format": "workbook.performance-scaling-report",
  "version": 1,
  "phase": "scaling",
  "generatedAt": "...",
  "environment": { "os": "...", "arch": "...", "gitVersion": "...",
                   "goVersion": "...", "workbookVersion": "...",
                   "workbookCommit": "...", "workbookBinarySha256": "..." },
  "objectFormat": "sha1",
  "samples": 20,
  "points": [
    {
      "name": "active-100-depth-20",
      "spec": { "activeTasks": 100, "operationsPerTask": 20 },
      "fixture": { "totalTasks": 105, "activeTasks": 100,
                   "tombstonedTasks": 5, "operationsPerTask": 20,
                   "objectFormat": "sha1" },
      "scenarios": [ { "name": "cli-create", "surface": "cold-cli",
                       "outcome": "not-evaluated",
                       "samples": [ { "milliseconds": 0, "gitProcesses": 0,
                                      "exitCode": 0, "timedOut": false } ],
                       "summary": { "completed": 0, "timedOut": 0,
                                    "minMilliseconds": 0,
                                    "medianMilliseconds": 0,
                                    "p95Milliseconds": 0,
                                    "p95GitProcesses": 0 } } ]
    }
  ],
  "slopes": [
    {
      "axis": "task-count",
      "scenario": "cli-create",
      "metric": "medianMilliseconds",
      "fromPoint": "active-100-depth-20",
      "toPoint": "active-500-depth-20",
      "fromDimension": 100,
      "toDimension": 500,
      "fromValue": 0,
      "toValue": 0,
      "dimensionRatio": 5,
      "valueRatio": 0,
      "logLogSlope": 0,
      "defined": false,
      "note": "nonpositive measured value"
    }
  ]
}
```

`ScenarioResult`, `Sample`, and `Summary` are the existing harness types, so
sample and summary field names are unchanged. The scaling report always writes
`target: null`: a scaling point is descriptive evidence, never a classified
budget. `outcome` retains only facts the harness observed directly —
`not-evaluated`, `failed`, or `timeout`.

The generated Markdown renders the same content: a matrix-point table with the
realized fixture shape and object format, one measurement table per point, and
the slope table.

### Determinism

`WriteJSON` and `WriteMarkdown` both normalize before writing. Normalization
recomputes each point's name from its spec, recomputes every summary from the
retained samples, strips targets, sorts points by ascending active tasks then
ascending depth, sorts each point's scenarios into the scaling scenario registry
order, and recomputes the slope list from the normalized points. Two runs with
the same measurements therefore produce byte-identical reports apart from
`generatedAt` and the measured values themselves. A caller cannot inject a slope
list the points do not support.

## Slope semantics

A slope compares one scenario metric between two **consecutive** points on one
axis:

- a **task-count** segment joins two points that share a history depth; and
- a **history-depth** segment joins two points that share an active population.

Grouping is generic, so a reduced matrix produces exactly the segments its points
support and nothing else. Segments are emitted in axis order (task-count, then
history-depth), then ascending group key, then ascending dimension, then the
scenario order recorded at the earlier point, then the fixed metric order:
`medianMilliseconds`, `p95Milliseconds`, `p95GitProcesses`.

Each record carries both raw values, both dimensions, the dimension ratio, the
value ratio, and a log-log slope:

```text
logLogSlope = ln(toValue / fromValue) / ln(toDimension / fromDimension)
```

A slope near 0 describes a metric that did not move with the dimension; near 1,
one that moved proportionally; above 1, one that moved faster than the
dimension. These are descriptions of what was measured. They are not budgets,
and the harness never classifies them.

Degenerate inputs are reported, never divided:

| Condition | Result |
| --- | --- |
| fewer than two points on an axis | no segment |
| scenario measured at only one of the two points | no record |
| nonpositive dimension | `defined: false`, note `nonpositive dimension` |
| identical dimensions | `defined: false`, note `identical dimensions` |
| nonpositive measured value at either end | `defined: false`, note `nonpositive measured value` |

An undefined slope always reports `logLogSlope: 0` and never `NaN` or `Inf`, so
the JSON stays decodable and the Markdown renders `-`.

Values come from `Summarize` over the retained samples, so a scenario whose
samples all failed contributes a zero median and an honest undefined slope
rather than a fabricated ratio.

## Invocation and option validation

`--phase scaling` selects the matrix. It is a distinct phase rather than a
modifier on `baseline` or `acceptance` so that:

- the acceptance fixture, tombstone, operation, active-task, sample, and commit
  guards are untouched and still apply only to `--phase acceptance`; and
- the single-run guard that remote and validation scenarios need at least 500
  tasks and 20 operations still applies to every `baseline` and `acceptance`
  invocation.

The 100-active point is 105 total refs, below that single-run minimum, so the
minimum is relaxed **only** inside the scaling branch. Tests cover both
directions: the scaling phase accepts the small point, while a baseline
invocation selecting `sync-already-synchronized` or `validate-cached-unchanged`
at 105 tasks is still rejected with its existing message, and an acceptance
invocation at 105 tasks is still rejected with its existing message.

The scaling phase rejects the flags the matrix already owns — `--tasks`,
`--tombstones`, `--operations`, and `--scenario` — rather than silently ignoring
them. `--scaling-point <active>x<depth>` is repeatable and replaces the default
matrix; it is rejected outside the scaling phase, rejected when malformed,
rejected when duplicated, and rejected when the point is unmeasurable. A run
whose measured samples failed or timed out still writes both reports and then
exits nonzero, matching the existing single-run behavior.

## Deliberately not measured

- **No new product targets.** Scaling points carry no duration or Git-process
  budget, and the existing local, remote, and validation targets are unchanged.
- **No `cli-delete`, `cli-free`, `cli-restore`, burst, or warm HTTP scenarios.**
  The story names a specific set; adding more would multiply an already long run
  without answering the question asked.
- **No `projection-*` scenarios, repository storage metrics, or peak-resource
  accounting.** Those belong to the concurrent many-changed-projection and
  resource-growth stories.
- **No SHA-1 versus SHA-256 slope.** Object format is recorded per point and per
  report, and each format is measured in its own run; the two runs are compared
  by a reader, not by an invented cross-format slope.
- **No regression gate.** This story produces a described measurement. A narrow
  optimization story is created only if a measured target miss or a measured
  slope warrants it.

## Verification

Implementation proceeded test-first. Focused tests cover:

- default matrix enumeration, ordering, and defensive copying;
- point naming and the active/tombstoned/total fixture mapping, including a
  reduced smoke-sized point, plus rejection of unmeasurable points;
- the scaling scenario set and its agreement with the harness registry order;
- the new `cli-list` scenario: untimed `rebuild --json` before the timed `list
  --json`, and the absence of an approved duration target;
- delegation of every point to the existing cold, remote, and validation runners
  with the correct run spec, scenario subset, and unique fixture roots;
- rejection of incomplete scenario coverage and of unmeasurable matrix specs;
- axis separation, ratios, and log-log slopes for a known synthetic matrix;
- degenerate slope inputs (single point, disjoint scenarios, zero baseline, zero
  result, identical dimensions);
- deterministic JSON and Markdown serialization from shuffled input, including
  stripped targets and recomputed slopes;
- relaxed-versus-strict option validation in both directions; and
- an end-to-end two-point scaling run against a real Workbook binary.

Full verification runs `gofmt -l .`, `go build ./...`, `go vet ./...`, and
`go test ./...`.

## Evidence exercise

The SHA-1 and SHA-256 matrix evidence is **not** generated on this branch. It is
generated once, after the three concurrent performance stories are integrated,
from one product binary and one harness binary built from that integrated
commit, so every report shares one source identity. Record the commit and both
binary checksums with the reports.

```sh
go build -buildvcs=false -o /private/tmp/workbook-scaling-target ./cmd/workbook
go build -buildvcs=false -o /private/tmp/workbook-scaling-bench ./cmd/workbook-bench

/private/tmp/workbook-scaling-bench \
  --workbook /private/tmp/workbook-scaling-target \
  --phase scaling \
  --samples 20 \
  --timeout 120s \
  --object-format sha1 \
  --output-json docs/performance/2026-07-30-scaling-slopes-sha1.json \
  --output-markdown docs/performance/2026-07-30-scaling-slopes-sha1.md
```

When `git init --object-format=sha256` is supported, run the same matrix once
more; otherwise record that SHA-256 is unsupported and do not substitute a
SHA-1 run.

```sh
/private/tmp/workbook-scaling-bench \
  --workbook /private/tmp/workbook-scaling-target \
  --phase scaling \
  --samples 20 \
  --timeout 120s \
  --object-format sha256 \
  --output-json docs/performance/2026-07-30-scaling-slopes-sha256.json \
  --output-markdown docs/performance/2026-07-30-scaling-slopes-sha256.md
```

Both invocations omit `--scaling-point`, so both measure the default story
matrix. A measured target miss elsewhere, a slow point, or a wide slope is
recorded evidence, not a reason to tune the product or rerun the matrix. Create
a narrow optimization story only when a measured target miss or a measured slope
warrants it.
