# Many-Changed Projection Refresh Design

Date: 2026-07-30

## Context

`WB-01KYQQJP9HPT9ZFW4X1T5MV91Y` was created because the 2026-07-29 repository
evaluation measured only two projection refresh shapes: an unchanged refresh and
a refresh after one changed task. Neither establishes how the disposable SQLite
projection behaves when many canonical task heads changed between refreshes,
which is the shape a fetching agent or a returning teammate actually meets.

The previous harness also had two defects for this question. Repository
scenarios were hardcoded to a single sample because `MeasureRepository` was
called without the `--samples` value, and the one-changed setup used
`workbook update`, which advances the projection row for the task it mutates.
The projection was therefore already current when the following `list --json`
ran, so the measured "one changed" refresh saw zero stale task heads.

That defective scenario never produced committed evidence. `docs/performance/`
contains no generated report with any `projection-refresh-*` result before
2026-07-30; the only earlier mention is a preparation timeout recorded in
`2026-07-28-baseline.md`, an attempt that aborted before writing a report. The
2026-07-30 reports are therefore the first published refresh measurements rather
than a replacement for a superseded number.

## Goals

- Measure projection refresh at 0, 1, 5, 50, and 500 changed task heads.
- Record end-to-end refresh latency, Git process count, ref-enumeration work,
  and SQLite projection work at each point.
- Keep fixture construction, projection settling, and head mutation outside the
  timed refresh and outside its Git-process count.
- Make repository-family scenarios honor `--samples`.
- Keep scenario semantics identical in SHA-1 and SHA-256 repositories.
- Describe the measured slope without inventing a pass threshold.

## Non-goals

- Change refresh behavior or any product code. Only `internal/perf/`,
  `cmd/workbook-bench/`, and documentation change.
- Attach a duration or Git-process target to this family. These scenarios stay
  descriptive; their reported outcome remains `not-evaluated` when they complete.
- Add a scaling or matrix mode, a `cli-list` scenario, or storage and
  peak-resource accounting. Those belong to separate stories.
- Optimize refresh, ref enumeration, packing, or projection storage.

## Scenario family and change-count semantics

| Selector | Changed task heads |
| --- | ---: |
| `projection-refresh-unchanged` | 0 |
| `projection-refresh-one-changed` | 1 |
| `projection-refresh-five-changed` | 5 |
| `projection-refresh-fifty-changed` | 50 |
| `projection-refresh-five-hundred-changed` | 500 |

The two pre-existing names keep their existing meaning, so earlier evidence and
the registry stay coherent. The three new names follow the same spelled-out
style already used by `projection-refresh-one-changed` and
`validate-five-changed`.

"Changed task head" means one canonical `refs/workbook/tasks/<id>` ref that
advanced to a new commit after the projection was last brought current, and
before the measured refresh started. The mutated set is a deterministic function
of the fixture: the first *N* entries of `Fixture.ActiveTaskIDs`, which is the
fixture's stable construction order. Nothing is random.

Only active tasks are mutable. A tombstoned task's history has ended, so
appending an operation to it would not be a valid task history. The 500-changed
point therefore needs at least 500 **active** tasks.

## How setup is excluded from the measurement

Each sample runs, in order:

1. **Build the fixture.** Every `(point, sample)` pair receives its own
   deterministic fixture, matching the existing validation-scenario pattern.
2. **Settle the projection.** One untimed `workbook list --json` brings the
   disposable cache to a known state that names every current task head. This
   command runs through `runValidationSetupCommand`, which deliberately does not
   set `GIT_TRACE2_EVENT` and does not produce a sample.
3. **Snapshot task refs.** An untimed `git for-each-ref` records every task ref
   and object name.
4. **Mutate exactly N heads.** The harness appends one deterministic, valid
   operation commit per selected task by writing Git objects directly
   (`hash-object`, `mktree`, `commit-tree`, `update-ref`). No product command
   runs, so the product never gets the chance to advance the projection rows for
   the tasks it is supposed to find stale. This is the defect the old
   `workbook update` setup had.
5. **Verify cardinality.** A second untimed `git for-each-ref` is compared with
   the snapshot. The task ref population must be unchanged, the number of
   differing refs must equal *N* exactly, and every intended task ID must be in
   the changed set. Any deviation is a fatal harness error; the harness never
   silently measures a different change count.
6. **Measure the refresh.** One `workbook list --json` runs through
   `MeasureCommandOutput`, which creates a fresh Trace2 event file for that
   command alone. Its elapsed time and Git process count therefore describe the
   refresh only.
7. **Verify and record projection work.** The measured result must be a
   `workbook.result` v1 `list` envelope whose data array holds exactly the
   fixture's active-task count. The disposable cache at
   `<common-git-dir>/workbook/cache.sqlite` is then sized.

Steps 1-5 and 7 are all outside the timed window. Steps 1-5 additionally run
outside the measured Trace2 file, so no setup Git process can be counted as
refresh work.

## Failure semantics

These scenarios have no target, so a completed sample is `not-evaluated`. A
timed-out sample is `timeout` and a nonzero-exit or errored sample is `failed`;
both are retained in the report, and `workbook-bench` already returns nonzero
after writing the reports when a repository-surface sample failed.

Harness problems remain fatal, because they make the measurement untrustworthy:
an unmet mutable-head requirement, a failed settle command, a ref-diff
cardinality mismatch, a changed task ref population, or a successful refresh
whose result envelope or task-row count does not match the fixture.

## New report fields

The JSON report gains an optional, versioned `projectionRefresh` block
(`workbook.projection-refresh` version 1) that is present only when the family
was measured. Per-scenario latency, Git processes, and outcome stay in the
existing `scenarios` array; the block adds the dimensions that array cannot
express.

Block fields:

- `format`, `version` — durable identity of the block.
- `samples` — the requested sample count actually used at every point.
- `fixture` — the measured `FixtureSpec`: total, active, and tombstoned tasks,
  operations per task, and Git object format.
- `points[]` — one entry per measured change-count point, in change-count order.
- `slope` — the descriptive summary.

Each `points[]` entry:

| Field | Meaning |
| --- | --- |
| `scenario` | Registered scenario name. |
| `changedTaskHeads` | Exact number of task refs advanced before each measured refresh. Verified by ref diff. |
| `samples` | Measured refreshes at this point. |
| `taskRefs` | Number of `refs/workbook/tasks/*` refs the refresh had to consider. |
| `refEnumerationMedianMilliseconds` | Median untimed harness cost of enumerating every task ref and object name immediately before the measured refresh. Harness-measured ref-enumeration work, not a product-internal counter. |
| `refreshMedianMilliseconds` | Median end-to-end latency of the timed `list --json`. |
| `refreshP95Milliseconds` | Nearest-rank p95 of the same samples. |
| `refreshMedianGitProcesses` | Median Trace2 process starts inside the measured refresh only. |
| `projectedTaskRows` | Task rows the refreshed projection returned; equals the fixture's active-task count. |
| `projectionCacheBytes` | Size of `<common-git-dir>/workbook/cache.sqlite` after the final measured refresh at this point. |

`slope` carries `baselineMilliseconds`, `maxChangedTaskHeads`,
`maxChangedMilliseconds`, `millisecondsPerChangedHead`, and a generated
`description`. `millisecondsPerChangedHead` is the plain difference quotient
between the lowest and highest measured change-count points. The description
names every measured point and states explicitly that the values describe the
measured samples and that the family has no pass threshold. No target is
attached to any of these scenarios, so a completed sample stays `not-evaluated`.

The generated Markdown renders the same block as a "Projection refresh
change-count family" section: a fixture-and-sample-count sentence, one table row
per point with every field above, and the slope sentence.

## Sample-count handling

`MeasureRepository` and `MeasurePackedRepositorySync` now take the requested
sample count, and `cmd/workbook-bench` passes `--samples` to them. Every
repository-surface scenario allocates that many samples:

- `projection-rebuild` repeats `workbook rebuild --json`, which is independent by
  construction because it rebuilds the cache from scratch.
- `sync-initial-local-bare` and `sync-unchanged-local-bare` receive their own
  fresh empty bare origin per sample, so each sample measures the same
  initial-publication and already-synchronized topology rather than degenerating
  into a second unchanged sync. The harness also clears any fetched tracking ref
  first, so the starting topology does not depend on the measured product still
  pruning stale tracking refs during its own fetch; against the current
  implementation that clear is a no-op. An integration test asserts each
  sample's origin ends up holding exactly the canonical task refs. All of this
  reset work is plain Git work outside both measured samples.
- The projection refresh family builds a fresh fixture per `(point, sample)` and
  applies the settle, mutate, and verify sequence above to each one.

## Fixture requirement for the 500-changed point

The default acceptance fixture of 500 total tasks is 475 active plus 25
tombstoned, which cannot supply 500 mutable active heads. Rather than measuring
475 and calling it 500, the harness rejects the invocation.

`workbook-bench` validates this in `validateOptions`, before the measured
binary's environment is read and before any fixture is built, and the perf
runner re-checks it before it builds anything. The message names the scenario,
the requirement, the actual active-task population, and a concrete fix:

```text
projection-refresh-five-hundred-changed requires 500 mutable active task heads,
but the fixture has 475 active tasks; re-run with a larger fixture, for example
--tasks 525 --tombstones 25
```

Because an omitted `--scenario` selects the entire registry, whole-harness runs
must now also use a fixture with at least 500 active tasks, or select a subset.

## What is deliberately not measured

- No product-internal ref-enumeration or SQLite counter is added. Recording one
  would require changing product code, which this story forbids. The reported
  ref-enumeration figure is the harness's own equivalent enumeration.
- No per-row or per-statement SQLite timing. `projectionCacheBytes` and
  `projectedTaskRows` are the stable, machine-readable projection-work proxies.
- No pass/fail budget, and no claim about what the slope *should* be.
- No change-count point between 50 and 500, and none above 500.
- Cross-sample history depth is not held constant: mutating the same first *N*
  active tasks in each sample adds one operation to those tasks. The measured
  refresh reads tip commits, and each sample uses its own fresh fixture, so this
  does not vary within a point.

## Verification

- `gofmt -l .`, `go build ./...`, `go vet ./...`.
- `go test ./internal/perf/ ./cmd/workbook-bench/`.
- Integration tests over SHA-1 and SHA-256 real repositories prove identical
  scenario semantics, that every point's sample count matches the request, that
  the measured Git-process count reflects only the refresh, and that the
  reported task refs, projected rows, and cache size are real.
- A dependency-injected test proves the call order is settle, mutate, measure,
  once per sample, and that the refs are already mutated when the timed command
  starts.
- A deliberately-off mutator proves the harness fails with
  `setup changed 4 task heads, want exactly 5` instead of measuring the wrong
  cardinality.
- A serialization test pins the exact JSON bytes of the new block.
- Command tests prove the fixture shortfall is rejected before any work and that
  the JSON and Markdown reports carry every point, the sample count, the fixture
  shape, the object format, and the slope description.

## Acceptance evidence

No report files are committed by this story. Final SHA-1 and SHA-256 evidence is
generated after the three concurrent performance stories are integrated, so
every report shares one source identity. Build one product binary and one
harness binary from that commit, then run each object format once:

```sh
go build -buildvcs=false -o /private/tmp/workbook-projection-refresh ./cmd/workbook
go build -buildvcs=false -o /private/tmp/workbook-bench-projection-refresh ./cmd/workbook-bench

/private/tmp/workbook-bench-projection-refresh \
  --workbook /private/tmp/workbook-projection-refresh \
  --tasks 525 \
  --tombstones 25 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario projection-refresh-unchanged \
  --scenario projection-refresh-one-changed \
  --scenario projection-refresh-five-changed \
  --scenario projection-refresh-fifty-changed \
  --scenario projection-refresh-five-hundred-changed \
  --output-json docs/performance/2026-07-30-projection-refresh-sha1.json \
  --output-markdown docs/performance/2026-07-30-projection-refresh-sha1.md

/private/tmp/workbook-bench-projection-refresh \
  --workbook /private/tmp/workbook-projection-refresh \
  --tasks 525 \
  --tombstones 25 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha256 \
  --phase acceptance \
  --scenario projection-refresh-unchanged \
  --scenario projection-refresh-one-changed \
  --scenario projection-refresh-five-changed \
  --scenario projection-refresh-fifty-changed \
  --scenario projection-refresh-five-hundred-changed \
  --output-json docs/performance/2026-07-30-projection-refresh-sha256.json \
  --output-markdown docs/performance/2026-07-30-projection-refresh-sha256.md
```

The fixture is 525 total tasks — 500 active and 25 tombstoned — which satisfies
both the acceptance minimums and the 500-changed point's mutable-head
requirement. If `git init --object-format=sha256` is unsupported on the
measuring host, record that and do not substitute a second SHA-1 run. Each
invocation runs once; a slow point is evidence to record, not a reason to tune
or rerun.
