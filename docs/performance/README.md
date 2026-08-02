# Workbook performance benchmarks

Workbook's performance harness measures representative cold CLI, warm HTTP,
burst, projection, validation, synchronization, and Git repository paths against
generated task histories. Run benchmarks from the repository root.

## Fixture dimensions, timing boundaries, and provenance

`--tasks` is the total number of canonical task refs, not the active-task
count. When `--tombstones` is omitted, a 500-or-more-task fixture contains 25
tombstoned tasks and the remaining tasks are active; therefore the default
500-task fixture is 475 active plus 25 tombstoned. Smaller diagnostic fixtures
default to one tombstone. Supplying `--tombstones 0` is allowed for diagnostics,
but not for acceptance evidence.

Acceptance requires at least 500 total tasks, 25 tombstoned tasks, 20
operations per task, and 10 active tasks. It also requires the measured
`workbook version --json` result to name an exact source commit; `unknown` is
rejected before fixture construction. Baseline runs retain their reported
commit even when it is `unknown`.

Each cold CLI sample rebuilds the SQLite projection with `workbook rebuild
--json` before the timed command; that rebuild and its Trace2 Git-process work
are deliberately untimed. Each warm HTTP sample starts its own server and makes
an untimed `/api/tasks` load that verifies the active-task population before the
timed mutation. Fixture construction is also outside every sample.

Local single-operation CLI mutation scenarios use an inclusive p95 target of
200 ms; the warm `api-update` scenario uses an inclusive p95 target of 100 ms;
and each ten-operation burst sample must be strictly below 1,000 ms. The
`cli-list` read scenario has no approved duration target and is reported
descriptively. Local scenarios have no Git-process target. Reports use format version 2 and record the SHA-256
of the resolved measured executable in
`environment.workbookBinarySha256`, alongside its reported version and commit.

Every local `cli-*` mutation scenario passes `--no-sync`, so those budgets
measure the local mutation path alone. `cli-update-autosync` measures the same
update with automatic synchronization enabled, against a local bare origin that
already holds the fixture's task refs, and carries an inclusive p95 target of
1,000 ms. Creating that origin and publishing the starting refs is setup,
outside the measured sample. Separating the two budgets keeps a local regression
from hiding inside network variance: the synchronized budget is dominated by two
connections to `origin`, and a connection costs roughly the same whatever it
carries.

### 2026-07-29 corrected local acceptance evidence

The corrected local harness was exercised once per supported Git object format
with the same reviewed product and harness binaries. Both invocations used 500
total tasks (475 active and 25 tombstoned), 20 operations per task, 20 samples
per scenario, a 60-second command timeout, and only the 12 local `cli-*` and
`api-*` scenarios. See the shared [build and checksum
provenance](2026-07-29-local-acceptance-provenance.md).

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [JSON](2026-07-29-local-acceptance-sha1.json), [Markdown](2026-07-29-local-acceptance-sha1.md) | All samples completed without timeout or product failure. Eight scenarios passed; `api-update`, both same-task bursts, and `cli-depend` missed their duration targets. |
| SHA-256 | [JSON](2026-07-29-local-acceptance-sha256.json), [Markdown](2026-07-29-local-acceptance-sha256.md) | All samples completed without timeout or product failure. Six scenarios passed; `api-update`, both same-task bursts, `cli-depend`, `cli-free`, and `cli-move` missed their duration targets. |

These valid target misses are retained as the one-shot evidence. The binaries
were not rebuilt, and neither invocation was tuned or replaced.

## Bounded baseline

The historical 2026-07-28 baseline predates report version 2 and used 500
active tasks with exactly 20 operations per task, one sample per scenario, and
a 60-second per-command timeout. It is retained as recorded; new version-2
runs use the total/active/tombstoned dimensions described above. A current
version-2 baseline invocation is:

[The current baseline evidence](2026-07-28-baseline.md) is an explicitly
hand-authored, incomplete lower-bound record. Both SHA-1 attempts aborted before
report assembly, so there is no generated JSON report or complete per-scenario
result for this baseline.

```sh
go build -o /tmp/workbook-benchmark-target ./cmd/workbook
go run ./cmd/workbook-bench \
  --workbook /tmp/workbook-benchmark-target \
  --tasks 500 \
  --tombstones 25 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --output-json docs/performance/2026-07-28-baseline.json \
  --output-markdown docs/performance/2026-07-28-baseline.md
```

Fixture construction and other setup work are excluded from scenario timings.
A baseline timeout, product-command failure, or missed reference budget is
recorded evidence rather than a reason to tune or rerun the baseline. The target
numbers in a baseline report are reference budgets, not claims that the current
implementation achieves them.

Current local acceptance evidence uses 20 samples per selected local scenario.
Acceptance may use a larger fixture, but it must use at least 500 total tasks,
25 tombstoned tasks, 20 operations per task, 10 active tasks, and 20 samples
when any local scenario is selected.

When the installed Git supports SHA-256 repositories, run the same bounded
baseline once with `--object-format sha256` and write the results to
`docs/performance/2026-07-28-baseline-sha256.json` and
`docs/performance/2026-07-28-baseline-sha256.md`.

## Projection refresh change-count family

The `projection-refresh-*` selectors measure how the disposable SQLite
projection refreshes when a known number of canonical task heads changed since
the projection was last brought current. They are descriptive: no duration or
Git-process target is attached, so a completed sample is reported
`not-evaluated`.

| Selector | Changed task heads |
| --- | ---: |
| `projection-refresh-unchanged` | 0 |
| `projection-refresh-one-changed` | 1 |
| `projection-refresh-five-changed` | 5 |
| `projection-refresh-fifty-changed` | 50 |
| `projection-refresh-five-hundred-changed` | 500 |

Each `(point, sample)` pair gets its own fixture. Every sample then runs an
untimed `workbook list --json` to settle the projection, advances exactly the
first *N* active task refs by writing Git objects directly, verifies by ref diff
that exactly *N* refs and no others changed, and only then measures one
`workbook list --json`. Fixture construction, settling, mutation, and
verification are outside the timed refresh and outside its Trace2 Git-process
count. The mutated set is the fixture's deterministic construction order, never
random. All of this is identical in SHA-1 and SHA-256 repositories.

Only active tasks are mutable, because a tombstoned task's history has ended.
The 500-changed point therefore requires at least 500 **active** tasks, which
the default 500-task acceptance fixture (475 active plus 25 tombstoned) cannot
supply. `workbook-bench` rejects that invocation before reading the measured
binary's version and before building any fixture:

```text
projection-refresh-five-hundred-changed requires 500 mutable active task heads,
but the fixture has 475 active tasks; re-run with a larger fixture, for example
--tasks 525 --tombstones 25
```

The harness never silently measures fewer heads than requested. Because an
omitted `--scenario` selects the whole registry, whole-harness runs must also
use at least 500 active tasks or select a subset.

Expect the 500-changed point to spend a noticeable amount of untimed wall-clock
time in setup: it writes 500 operation commits and 500 ref updates through
individual Git commands before the measured refresh. That cost is outside every
sample and outside the per-command timeout, which bounds only the measured
command.

### Reading the refresh slope output

When any family member runs, the JSON report gains a `projectionRefresh` block
(`workbook.projection-refresh` version 1) alongside the usual `scenarios` array,
and the Markdown report gains a "Projection refresh change-count family"
section. The block records the sample count, the measured fixture shape and Git
object format, and one entry per change-count point:

| Field | Meaning |
| --- | --- |
| `changedTaskHeads` | Exact number of task refs advanced before each measured refresh. |
| `samples` | Measured refreshes at this point. |
| `taskRefs` | Task refs the refresh had to consider. |
| `refEnumerationMedianMilliseconds` | Median untimed harness cost of enumerating every task ref and object name immediately before the measured refresh. |
| `refreshMedianMilliseconds`, `refreshP95Milliseconds` | End-to-end latency of the timed refresh. |
| `refreshMedianGitProcesses` | Median Git process starts inside the measured refresh only. |
| `projectedTaskRows` | Task rows the refreshed projection returned. |
| `projectionCacheBytes` | Size of `<common-git-dir>/workbook/cache.sqlite` after the final measured refresh at this point. |

`slope.millisecondsPerChangedHead` is the plain difference quotient between the
lowest and highest measured points, and `slope.description` names every measured
point. Read both as a description of the samples that were taken, not as a
budget: this family has no pass threshold, and a steep slope is evidence to
record rather than a failure.

Repository-surface scenarios now honor `--samples`. `projection-rebuild` repeats
its independent rebuild, and each local-bare sync sample receives its own fresh
empty bare origin so it measures the same initial-publication and
already-synchronized topology every time. The harness also clears any fetched
tracking ref before each sync sample, so that starting topology does not depend
on the measured product still pruning stale tracking refs itself.

## Remote synchronization topologies

`workbook-bench` accepts repeatable `--scenario <name>` selectors. With one or
more selectors, it runs only the selected scenarios; unknown and duplicate
names are rejected before fixture construction. Omitting `--scenario` includes
the existing local scenarios and every registered remote synchronization
scenario. Selected scenarios run in the harness's stable registry order, not
the order of the flags.

Every remote scenario requires at least 500 total tasks and 20 operations per
task, even for a baseline run. The seven remote selectors are:

Synchronization measurements exercise the bounded default path: one isolated
tracking fetch, current-tip validation, ancestry classification, and ref
publication. They deliberately do not include a replay of every buried
checkpoint; the planned explicit validation audit is a separate future path.

| Selector | Workbook command and topology | Reference target |
| --- | --- | --- |
| `sync-fresh-checkout` | `fetch` from 500 populated remote task refs into a fresh checkout | at most 5 seconds; fewer than 20 Git processes |
| `sync-initial-publication` | `push` a populated local repository to an empty bare remote | at most 5 seconds; fewer than 20 Git processes |
| `sync-already-synchronized` | `sync` when local, tracking, and remote refs match | at most 1 second; fewer than 10 Git processes |
| `sync-small-changed-ref-set` | `sync` with five local-ahead and five disjoint remote-ahead tasks | at most 2 seconds; fewer than 20 Git processes |
| `sync-divergent-tips` | `sync` with one task whose local and remote tips diverge | at most 2 seconds; fewer than 20 Git processes |
| `sync-malformed-local-tip` | `push` with one malformed owned local task ref | at most 2 seconds; fewer than 20 Git processes |
| `sync-malformed-remote-tip` | `fetch` with one malformed fetched tracking ref | at most 2 seconds; fewer than 20 Git processes |

The remote report target is evaluated per scenario. `pass` means every sample
completed within the inclusive time limit and below the exclusive Git-process
limit; `miss` means a completed sample exceeded either budget; `timeout` means
at least one sample reached its command timeout; and `failed` means a command
sample failed. Scenarios with no target are `not-evaluated`. Expected product
errors in the divergent and malformed topologies remain measured samples when
their result and ref invariants are correct, but their nonzero command samples
still produce `failed` evidence. A timeout's elapsed duration is a lower bound:
it shows the command ran for at least that long, not its final latency. Harness
setup, report encoding, JSON decoding, or ref-verification failures are fatal
because they make the measurement untrustworthy.

These targets are reference budgets, not achieved-performance guarantees. A
timeout, product failure, or target miss is evidence to record, not a reason to
tune or replace the one-shot baseline.

## Semantic history validation topologies

The validation selectors each create an independent fixture, and all cache
seeding and five-task updates occur before the measured command. They require
at least 500 total tasks and 20 operations per task, including baseline mode.

| Selector | Measured command | Reference target |
| --- | --- | --- |
| `validate-full-history` | `validate --full --json` | at most 10 seconds; fewer than 12 Git processes |
| `validate-cached-unchanged` | `validate --json` after a successful cache seed | at most 500 milliseconds; fewer than 12 Git processes |
| `validate-five-changed` | `validate --json` after a cache seed and five one-operation updates | at most 1 second; fewer than 12 Git processes |

Each measured result must exactly report valid task and empty-failure totals,
with full, cached, and five-changed counts respectively. The Git-process limit
is exclusive: twelve processes is a miss.

### Remote synchronization commands and evidence

Build the measured binary once, then run each object format at most once. These
commands select only the seven remote scenarios and write separate reports;
they do not replace the incomplete whole-harness record above.

```sh
go build -buildvcs=false -o /private/tmp/workbook-sync-baseline ./cmd/workbook

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

When `git init --object-format=sha256` is supported, run the following once;
otherwise record that SHA-256 is unsupported and do not substitute a SHA-1 run.

```sh
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-sync-baseline \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha256 \
  --phase baseline \
  --scenario sync-fresh-checkout \
  --scenario sync-initial-publication \
  --scenario sync-already-synchronized \
  --scenario sync-small-changed-ref-set \
  --scenario sync-divergent-tips \
  --scenario sync-malformed-local-tip \
  --scenario sync-malformed-remote-tip \
  --output-json docs/performance/2026-07-28-sync-baseline-sha256.json \
  --output-markdown docs/performance/2026-07-28-sync-baseline-sha256.md
```

### 2026-07-29 tip-focused evidence

The tip-focused implementation was exercised once per supported object format
with the same 500-by-20 fixture, one sample, and 60-second timeout:

| Format | Evidence | Result |
| --- | --- | --- |
| SHA-1 | [attempt record](2026-07-29-sync-tip-focused-sha1-attempt.md) | The fail-fast harness found an incorrect changed-set oracle before report assembly; no rerun or substitute report was made. |
| SHA-256 | [JSON](2026-07-29-sync-tip-focused-sha256.json), [Markdown](2026-07-29-sync-tip-focused-sha256.md) | All seven topology contracts verified. Four success scenarios completed in 313.51–1047.08 ms; three expected product-error scenarios completed in 226.62–322.68 ms. |

Compared with the [SHA-1 baseline](2026-07-28-sync-baseline-sha1.md) and
[SHA-256 baseline](2026-07-28-sync-baseline-sha256.md), which timed out with
3,997–4,082 Git processes per topology, the SHA-256 tip-focused run used 8–20
Git processes and did not time out. Fresh checkout and initial publication met
both targets. Synchronized and small-changed-set timing met their budgets, but
their observed process counts of 11 and 20 missed the exclusive `<10` and `<20`
limits.

The evidence was not replaced after those misses. A subsequent bounded-shape
test, using 10 tasks by 4 operations and the same Trace2 counter, verifies the
two affected product paths at 9 and 18 Git processes after removing a redundant
object-width probe and fetch auto-maintenance. That test demonstrates the
constant process shape now meets the approved exclusive limits; it is not a
replacement 500-by-20 acceptance sample.

### 2026-07-30 packed repository sync acceptance evidence

The corrected packed-repository acceptance was run once in each supported object
format, using one frozen product binary and one frozen harness binary. The
sync-only dispatcher skipped all projection measurements and mutations. Its real
regression verifies that every canonical task ref retains its requested commit
count immediately before and after each sync; the existing integration test
verifies the remote refs exactly.

Both the initial and unchanged local-bare sync scenarios completed in SHA-1 and
SHA-256. The reports have no repository latency or Git-process budget because
this focused functional acceptance introduced neither. No unrelated
future-story acceptance family was rerun.

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [JSON](2026-07-30-packed-repository-sync-acceptance-sha1.json), [Markdown](2026-07-30-packed-repository-sync-acceptance-sha1.md) | Both selected scenarios completed once without timeout or product error. |
| SHA-256 | [JSON](2026-07-30-packed-repository-sync-acceptance-sha256.json), [Markdown](2026-07-30-packed-repository-sync-acceptance-sha256.md) | Both selected scenarios completed once without timeout or product error. |

See the shared [build and checksum provenance](2026-07-30-packed-repository-sync-acceptance-provenance.md).

### 2026-07-29 semantic history validation evidence

The measured product binary was built once. Each supported object format then
received one historical acceptance invocation with 500 active tasks, exactly 20
operations per task, one sample, and a 60-second command timeout. This
version-1 evidence remains unchanged; new version-2 acceptance evidence uses
the dimensions and provenance rules above.

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [attempt record](2026-07-29-history-validation-sha1-attempt.md) | `go run` could not read the sandboxed default Go build cache, so the harness aborted before fixture construction or report assembly. No retry or replacement was made. |
| SHA-256 | [JSON](2026-07-29-history-validation-sha256.json), [Markdown](2026-07-29-history-validation-sha256.md) | All three contracts passed: full 2,185.65 ms / 7 Git processes; cached unchanged 116.71 ms / 4; five changed 166.03 ms / 7. |

The SHA-256 full audit met its inclusive 10-second time target and exclusive
`<12` process target. Cached unchanged met 500 milliseconds and `<12`; five
changed met 1 second and `<12`. None timed out or failed. The SHA-256 invocation
used the same measured product binary as the SHA-1 attempt and set only the
writable Go build-cache location for the harness compilation; setup and harness
compilation are outside every measured sample.

Neither acceptance invocation was retried, tuned, or replaced.

After these invocations, commit `c26f9a4` fixed reuse of a cached boundary from
an older validator version. That path is not exercised by the fresh,
current-version acceptance fixtures above, and the measured binary was not
rebuilt or the evidence rerun after the fix.

## Task-count and history-depth scaling matrix

`--phase scaling` measures a matrix of fixture points instead of one fixture, so
the task-count axis and the history-depth axis can be read independently rather
than inferred from the single 500-by-20 acceptance point.

A matrix point names an *active* task population and a history depth. The
harness derives the rest of the representative fixture from it, using one
tombstoned task per twenty active tasks — the same ratio as the default 25-in-500
acceptance fixture — and a total ref count that is their sum:

| Point | Active | Tombstoned | Total refs | History depth |
| --- | ---: | ---: | ---: | ---: |
| `active-100-depth-20` | 100 | 5 | 105 | 20 |
| `active-500-depth-20` | 500 | 25 | 525 | 20 |
| `active-500-depth-100` | 500 | 25 | 525 | 100 |
| `active-1000-depth-20` | 1000 | 50 | 1050 | 20 |

The three depth-20 points form the task-count axis; the two 500-active points
form the history-depth axis. Every point uses the corrected representative
fixture generator, and every point records its realized shape and Git object
format in the report.

Nine scenarios are measured at each point: `cli-create`, `cli-depend`,
`cli-list`, `cli-move`, `cli-update`, `sync-already-synchronized`,
`sync-small-changed-ref-set`, `validate-full-history`, and
`validate-cached-unchanged`. `cli-list` is a cold read scenario; like every
other cold scenario it runs an untimed `workbook rebuild --json` before the
timed `workbook list --json`, and it has no duration target because no read-path
budget has been approved.

Run the default matrix with:

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

Repeat once with `--object-format sha256` when the installed Git supports
SHA-256 repositories. Add repeatable `--scaling-point <active>x<depth>`
selectors to measure a reduced matrix, for example `--scaling-point 20x4`, for a
fast diagnostic run.

The scaling phase owns its own fixture, so it rejects `--tasks`, `--tombstones`,
`--operations`, and `--scenario`. It also relaxes the single-run rule that
remote and validation scenarios need at least 500 tasks and 20 operations,
because the 100-active point is deliberately smaller. That relaxation applies
only to `--phase scaling`; `--phase baseline` and `--phase acceptance` keep every
existing minimum.

### Reading the scaling slope output

Scaling reports use the separate `workbook.performance-scaling-report` format,
version 1. They carry no duration or Git-process budget: a scaling point is a
description, not a classification, so every scenario reports
`not-evaluated` unless its command actually failed or timed out. The existing
acceptance targets are unchanged and are not attached to scaling points.

Each slope row compares one scenario metric between two consecutive points on
one axis — `task-count` holds history depth constant, `history-depth` holds the
active population constant. A row reports both measured values, both dimensions,
the dimension ratio, the value ratio, and

```text
logLogSlope = ln(value ratio) / ln(dimension ratio)
```

Read a slope near 0 as "this metric did not move with the dimension", near 1 as
"it moved proportionally", and above 1 as "it moved faster than the dimension".
Metrics are `medianMilliseconds`, `p95Milliseconds`, and `p95GitProcesses`.

A slope is reported as undefined, with a note and a zero value, when either
dimension is nonpositive, the two dimensions are identical, or either measured
value is nonpositive. Undefined rows render as `-` in Markdown rather than as an
infinity or a `NaN`. A scenario measured at only one point of a pair produces no
row at all.

Slopes are descriptive evidence. A wide slope is a reason to open a narrow,
separately justified optimization story, not a failing benchmark.
## Storage and peak resource growth

`workbook-bench --storage-resources` measures durable Git storage by object
class, disposable cache size, and the peak resident memory and I/O of the
projection rebuild and full validation commands. It replaces the scenario
families for that invocation and cannot be combined with `--scenario`; the
scenario fixtures mutate their own repositories and would change the storage
being measured.

`--storage-operations` takes a comma-separated list of operations-per-task
depths and defaults to `20,100`. The list is deduplicated and measured in
ascending order, and each depth gets its own freshly built fixture. `--tasks`
and `--tombstones` keep their usual meaning and apply to every depth. Under
`--phase acceptance` every depth must be at least 20 operations per task, in
addition to the existing acceptance fixture minimums.

```sh
go build -buildvcs=false -o /private/tmp/workbook-storage ./cmd/workbook
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-storage \
  --tasks 500 \
  --tombstones 25 \
  --timeout 120s \
  --object-format sha1 \
  --phase baseline \
  --storage-resources \
  --storage-operations 20,100 \
  --output-json /private/tmp/storage-sha1.json \
  --output-markdown /private/tmp/storage-sha1.md
```

Per fixture depth the harness builds the fixture, packs refs with `git pack-refs
--all`, packs objects with `git gc --quiet --prune=now`, classifies durable
objects, measures `workbook rebuild --json` and `workbook validate --full
--json`, and then sizes the two SQLite caches. Fixture construction and packing
are outside every measured command. A failing or timed-out measured command
aborts the run instead of producing a report.

These results are descriptive. They have no target, no budget, and no
pass/fail outcome. A storage-only report runs no scenarios, so its `scenarios`
array is empty and its `targets` object is zero-valued; neither describes the
storage measurement. The report's top-level `fixture` records the shallowest
measured depth, and complete per-depth fixture metadata lives in
`storageResources.depths[].fixture`.

### Reading every storage and resource field

All fields live under the report's optional `storageResources` object. Runs
that do not measure storage omit the key.

| Field | Meaning |
| --- | --- |
| `platform` | `GOOS/GOARCH` of the host that produced the measurement. |
| `maxResidentRawUnit` | Unit of `ru_maxrss` on that platform: `bytes` on darwin, `kilobytes` on Linux and the BSDs. |
| `blockIoCountersSupported` | Whether the kernel maintains `ru_inblock` and `ru_oublock`. False on darwin. |
| `objectSizeSemantics` | Self-describing sentence distinguishing raw from on-disk bytes. |
| `repositoryState` | How the repository was packed before measuring. |
| `depths[]` | One entry per operations-per-task depth, ascending. |

Per depth, `fixture` repeats the complete fixture specification: `totalTasks`,
`activeTasks`, `tombstonedTasks`, `operationsPerTask`, and `objectFormat`.

Durable Git storage lives under `depths[].git`:

| Field | Meaning |
| --- | --- |
| `objectFormat` | Object format read back from the measured repository. |
| `refPrefix`, `workbookRefs`, `taskRefs` | Root ref namespace of the object walk and the ref populations found in it. |
| `reachableObjects` | Objects reachable from those refs, each counted once. |
| `classifiedObjects`, `unclassifiedObjects` | Completeness of the accounting. `unclassifiedObjects` is always zero; a non-zero value means the classification missed a case. |
| `classes[]` | Per class: `class`, `objects`, `rawBytes`, `diskBytes`. Classes are `operation-blob`, `state-blob`, `other-blob`, `tree`, `commit`, `annotated-tag`. |
| `reachableRawBytes`, `reachableDiskBytes` | Sums of the class byte totals. |
| `packs`, `packedObjects`, `looseObjects` | Git's own counts from `count-objects -v`. After packing, `packedObjects` should equal `reachableObjects` and `looseObjects` should be zero. |
| `packFileBytes`, `packIndexBytes`, `packAuxiliaryBytes` | Exact summed sizes of `*.pack`, `*.idx`, and the remaining files in `objects/pack`. |
| `looseObjectFileBytes` | Exact summed size of loose object files. |
| `objectDirectoryBytes` | Every regular file under `objects`, including `objects/info` artifacts. |

`rawBytes` is `%(objectsize)`: uncompressed Git object content. `diskBytes` is
`%(objectsize:disk)`: the stored representation including delta base chain and
object header, excluding per-pack index and header overhead. `diskBytes` is
normally smaller for JSON documents but can exceed `rawBytes` for very small
objects such as the fixture's two-entry trees, where framing outweighs
compression. That is real Git behavior, not a reporting error.

Object counts are stored-object counts, not logical document counts. Git stores
byte-identical blobs once, so two identical operation documents contribute one
object; that is the storage the repository actually pays for.

Disposable cache bytes live under `depths[].disposableCache`. `projectionPath`
and `validationPath` are repository-relative
(`.git/workbook/cache.sqlite` and `.git/workbook/validation.sqlite`).
`projectionBytes` and `validationBytes` are the database files;
`projectionSidecarBytes` and `validationSidecarBytes` sum any `-wal`, `-shm`,
and `-journal` companions; `totalBytes` is the sum of all four. Every byte here
can be deleted and rebuilt from Workbook refs.

Resource measurements live under `depths[].resources`, always in the order
`projection-rebuild` then `full-validation`:

| Field | Meaning |
| --- | --- |
| `command`, `argv` | Measurement name and the product arguments that were run. |
| `milliseconds`, `userMilliseconds`, `systemMilliseconds` | Elapsed, user, and system time. Context only; no target applies. |
| `exitCode`, `timedOut`, `error` | Command outcome. A failure aborts the run before a report is written. |
| `maxResidentRaw`, `maxResidentRawUnit` | `ru_maxrss` exactly as the kernel reported it, plus its unit. |
| `maxResidentBytes` | `maxResidentRaw` normalized to bytes. |
| `blockInputOperations`, `blockOutputOperations`, `blockIoCountersSupported` | `ru_inblock` and `ru_oublock`, and whether the platform maintains them. Forced to zero where unsupported. |
| `minorPageFaults`, `majorPageFaults` | `ru_minflt` and `ru_majflt`. Populated on darwin, where they are the usable I/O pressure signal. |
| `voluntaryContextSwitches`, `involuntaryContextSwitches` | `ru_nvcsw` and `ru_nivcsw`. |
| `repositoryBytesDelta` | Change in total on-disk bytes under the repository root across the command, sampled outside the timing window. A durable-write lower bound, not a syscall counter. |

Two platform caveats matter when reading these numbers:

- Peak resident memory from `wait4` is a maximum, not a sum. It is the largest
  resident set observed for the measured process or any descendant it reaped, so
  a command that runs several `git` processes concurrently reports the largest
  single peak, not the concurrent total. Read it as a lower bound on whole-tree
  peak memory.
- Darwin does not maintain `ru_inblock` or `ru_oublock`. On darwin those fields
  are zero and `blockIoCountersSupported` is false; a zero there is not evidence
  that no I/O happened. Use `majorPageFaults` and `repositoryBytesDelta`
  instead.

Deliberately not measured: unreachable and dangling objects, non-Workbook refs
and working-tree files including `.workbook/config.json`, reflog and
`packed-refs` bytes, delta chain depth, concurrent whole-process-tree peak
memory, cold-start page-cache behavior, and any latency budget.

## Reading the reports

A completed harness run produces a versioned, machine-readable JSON report and a
compact generated Markdown view of the same scenarios. Each scenario then
records a concrete result, timeout, or product-command failure. Harness and
output failures remain fatal and may prevent report creation, as happened for
the current hand-authored lower-bound evidence.
