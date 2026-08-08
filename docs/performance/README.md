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
operations per task, and 10 active tasks.

Both published evidence phases, `--phase acceptance` and `--phase scaling`,
require the measured `workbook version --json` result to name an exact source
commit; `unknown` is rejected before fixture construction. Provenance for
published evidence is therefore a property of the phase rather than of operator
discipline. Baseline runs are a development tool, not evidence, and retain their
reported commit even when it is `unknown`.

Each cold CLI sample rebuilds the SQLite projection with `workbook rebuild
--json` before the timed command; that rebuild and its Trace2 Git-process work
are deliberately untimed. Each warm HTTP sample starts its own server and makes
an untimed `/api/tasks` load that verifies the active-task population before the
timed mutation. Fixture construction is also outside every sample.

Local single-operation CLI scenarios use an inclusive p95 target of
200 ms; the warm `api-update` scenario uses an inclusive p95 target of 100 ms;
and each ten-operation burst sample must be strictly below 1,000 ms. The
whole-board read scenarios `cli-list` and `api-tasks` have no approved duration
target and are reported descriptively. Local scenarios have no Git-process
target. Reports use format version 2 and record the SHA-256
of the resolved measured executable in
`environment.workbookBinarySha256`, alongside its reported version and commit.

The line between a budgeted and a descriptive scenario is the shape of the work,
not reading against writing. The 200 ms local class covers a command whose cost
is bounded by one task, which is why `cli-show` carries it: reading a single task
is strictly less work than the single-task mutations the class was approved for,
on the same surface and with no round trip, so the approved envelope is a valid
upper bound rather than an invented threshold. `cli-list` and `api-tasks` answer
with the whole board, so their cost grows with the task population and the
single-operation envelope says nothing about them. No whole-board read budget has
been approved, so both are reported `not-evaluated`.

Every local `cli-*` mutation scenario passes `--no-sync`, so those budgets
measure the local mutation path alone. `cli-update-autosync` measures the same
update with automatic synchronization enabled, against a local bare origin that
already holds the fixture's task refs, and carries an inclusive p95 target of
1,000 ms. Creating that origin and publishing the starting refs is setup,
outside the measured sample. Separating the two budgets keeps a local regression
from hiding inside network variance.

### The agent hot loop

`cli-next`, `cli-show`, and `cli-update` are the three commands an agent runs
continuously: acquire work, read its context, record progress. All three are
measured, because a latency surprise in this loop is the one beta users driving
agents will meet first and most often.

`cli-next` deliberately does not pass `--no-sync`. `workbook next` fetches before
answering so two agents cannot claim the same task, and that fetch is the point
of the scenario: measuring it with `--no-sync` would report a local read no agent
ever performs. It therefore runs against a local bare origin that already holds
the fixture's task refs, exactly as `cli-update-autosync` does, and carries the
same inclusive p95 target of 1,000 ms.

Its setup does three untimed things, in this order, and each earns its place.
First it moves one active task to `ready`: `next` only ever selects a task in
that status, and the fixture's deterministic generator never leaves one there, so
without this the board would hold nothing to acquire and the scenario would
report the agent's acquire step while measuring a search that always comes up
empty. Then it publishes origin, so the measured fetch meets an
already-synchronized remote and reconciles nothing — publishing first would leave
the local task ahead and price a replay the scenario does not claim to measure.
Finally it re-settles the projection with an untimed `workbook rebuild --json`,
because the setup mutation left one head stale and refreshing a single changed
head costs tens of milliseconds at acceptance size; every other cold scenario's
projection is current when its command starts, and this keeps the sample
comparable. A setup mutation that fails aborts the scenario rather than
producing a sample.

Pricing `next` in the synchronized class rather than the local one is the
deliberate choice this scenario exists to record. Its fetch is the same broad
fetch that dominates `cli-update-autosync`, so a 200 ms local budget would
classify a command that cannot meet it by design, and the round trip would be
read as a regression instead of as the cost of not letting two agents claim the
same task. The target accounts for the fetch instead of hiding it.

`cli-show` performs no synchronization at all — `workbook show` has no
`--no-sync` flag because it never fetches — so it sits in the 200 ms local class
described above.

`api-tasks` is the read side of the board. Every warm HTTP sample already makes
an untimed `/api/tasks` load to verify the active-task population; `api-tasks`
times a second one, so the sample is a warm read against a server that has
already opened the projection and answered the same query once. The timed read
verifies the same population before it is reported, so a server that answered
with an empty board could not be recorded as a fast read.

`cli-update-watched` measures that same synchronized update with a
`workbook sync --watch` process running, and is held to the **200 ms local**
budget rather than the 1,000 ms synchronized one. That is the entire claim the
scenario exists to test: a watched mutation defers both round trips to the
watcher, so it is a local mutation and has to be measured as one. Starting the
watcher and waiting for it to complete its opening synchronization is setup. Its
interval is one hour, so it never synchronizes on its own and the sample
measures the probe and the hand-off rather than the watcher's Git work; the
harness passes `GIT_TRACE2_EVENT` only to the measured command, so the watcher's
Git processes are not attributed to the sample either. The Git-process count is
the least noisy evidence here, because it carries no network variance.

The synchronized budget is not a pure network allowance. Against a real remote,
a connection costs roughly the same whatever it carries, so the targeted push is
constant in the number of tasks a project holds. The broad fetch is not: it
enumerates and validates every task ref, and at 500 tasks that work dominates
the measured delta even against a local bare origin, where connections are
cheap. A real remote adds its connection latency on top. The 1,000 ms budget
accommodates both, and the measured delta belongs to the fetch half.

A hot loop run is an ordinary local acceptance invocation with the scenarios
named explicitly. It carries the same 20-sample minimum every `cli-*` and `api-*`
acceptance run does. Naming the two anchors alongside the three new scenarios is
what makes the run readable: `cli-update` fixes the local class and
`cli-update-autosync` fixes the synchronized one, so `cli-show` and `cli-next`
are classified against scenarios measured minutes apart on the same host rather
than against numbers from another date.

```sh
go build -buildvcs=false \
  -ldflags "-X main.version=<version> -X main.commit=<commit>" \
  -o /private/tmp/workbook-hotloop ./cmd/workbook
go build -buildvcs=false -o /private/tmp/workbook-hotloop-bench ./cmd/workbook-bench

/private/tmp/workbook-hotloop-bench \
  --workbook /private/tmp/workbook-hotloop \
  --tasks 525 \
  --tombstones 25 \
  --operations 20 \
  --samples 20 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario cli-next \
  --scenario cli-show \
  --scenario api-tasks \
  --scenario api-update \
  --scenario cli-update \
  --scenario cli-update-autosync \
  --scenario cli-list \
  --output-json docs/performance/2026-08-08-agent-hot-loop-sha1.json \
  --output-markdown docs/performance/2026-08-08-agent-hot-loop-sha1.md
```

Repeat once with `--object-format sha256`.

### 2026-08-08 agent hot loop and read-side evidence

The agent hot loop was measured once per supported Git object format with
the same frozen product and harness binaries, using 525 total tasks (500 active
and 25 tombstoned), 20 operations per task, 20 samples per scenario, a 60-second
command timeout, and the seven scenarios listed above. See the shared [build,
checksum, and host-load provenance](2026-08-08-agent-loop-and-watcher-provenance.md).

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [JSON](2026-08-08-agent-hot-loop-sha1.json), [Markdown](2026-08-08-agent-hot-loop-sha1.md) | All 140 samples completed without timeout or product failure. `cli-show` passed. `cli-next`, `api-update`, `cli-update`, and `cli-update-autosync` missed their duration targets. `cli-list` and `api-tasks` have no target. |
| SHA-256 | [JSON](2026-08-08-agent-hot-loop-sha256.json), [Markdown](2026-08-08-agent-hot-loop-sha256.md) | All 140 samples completed without timeout or product failure. `cli-next` and `cli-show` passed. `api-update`, `cli-update`, and `cli-update-autosync` missed their duration targets. `cli-list` and `api-tasks` have no target. |

**Read the outcome column with the host in mind, and prefer the ratios below.**
`cli-update` and `cli-update-autosync` are unchanged by this branch and both
passed comfortably on 2026-08-05 — 174.63 ms against 200 ms, and 706.97 ms
against 1,000 ms — yet both miss here, `cli-update` by 71% and
`cli-update-autosync` by 109%. That is the signature of a contended measuring
host, not of a regression: nothing in this branch touches either path. The
absolute values are therefore upper bounds, and every claim below rests on
scenarios compared *within* one run.

| Scenario | SHA-1 min / median / p95 | SHA-256 min / median / p95 | Target | Git processes |
| --- | ---: | ---: | ---: | ---: |
| `api-tasks` | 28.62 / 30.75 / 33.16 ms | 31.51 / 35.54 / 45.79 ms | none | 1 |
| `cli-show` | 52.70 / 70.05 / 119.20 ms | 52.15 / 63.59 / 138.71 ms | 200 ms | 3 |
| `cli-list` | 58.21 / 79.32 / 165.18 ms | 55.32 / 69.38 / 124.08 ms | none | 3 |
| `api-update` | 106.59 / 114.67 / 121.98 ms | 110.79 / 125.34 / 179.88 ms | 100 ms | 8 |
| `cli-update` (`--no-sync`) | 137.41 / 191.84 / 299.40 ms | 133.17 / 154.93 / 265.59 ms | 200 ms | 10 |
| `cli-next` | 401.19 / 652.73 / 1149.70 ms | 404.26 / 483.72 / 912.81 ms | 1,000 ms | 10 |
| `cli-update-autosync` | 602.78 / 906.65 / 1480.54 ms | 645.25 / 765.37 / 1528.82 ms | 1,000 ms | 26 |

**`cli-next` belongs in the synchronized class, and this run says so two
independent ways.** First, its cheapest sample — the one least touched by host
load — is 401.19 ms in SHA-1 and 404.26 ms in SHA-256. A command whose *best*
observed sample is twice the 200 ms local budget cannot be classified as local,
and that single number settles the target choice without needing a quiet host.
Second, it tracks `cli-update-autosync` rather than `cli-update` at every
statistic: 63–78% of the synchronized scenario's p95 and 63–72% of its median,
where `cli-update` sits at roughly a fifth. `next` is cheaper than a
synchronized mutation, because it fetches but never pushes or replays, and it is
plainly in the same class.

**The fetch does not show up in the process count, which is why it needed
measuring.** `cli-next` spawns 10 Git processes — exactly what local
`cli-update --no-sync` spawns — while taking three to four times as long.
Elsewhere these documents lean on Git-process counts as the sturdier half of the
evidence because they carry no network variance, and this scenario is the
exception worth naming: a single broad fetch that enumerates and validates 525
task refs is one process and most of the wall clock. Counting processes would
have priced `next` as a local command. Only timing it finds the fetch.

**`cli-show` is comfortably local.** It passed the 200 ms budget in both formats
on a host where `cli-update` — the class it is being compared against — missed
in both, and it costs 36–52% of that mutation at 3 Git processes against 10.
Reading one task is strictly less work than mutating one, and the measurement
holds even under contention.

**The board's read side is the cheapest thing measured.** `api-tasks` answers the
whole 500-task collection in 33.16 ms and 45.79 ms at p95 through a single Git
process, roughly a quarter of the warm mutation on the same server in the same
run. `cli-show` and `cli-list` also sit side by side for the first time, and the
distinction the budget rules rest on is not yet visible at this size: the
whole-board read costs about 10% more than the single-task one at the median, and
the two p95 values disagree in direction between the formats. That is a statement
about 525 tasks rather than a general one — the population is simply not where a
whole-board read starts to hurt — and it is exactly why neither read carries a
target.

### 2026-08-05 sync watcher local acceptance evidence

The sync watcher branch was exercised once per supported Git object format with
the same frozen product and harness binaries, using 525 total tasks (500 active
and 25 tombstoned), 20 operations per task, 20 samples per scenario, a 60-second
command timeout, and all 15 local `cli-*` and `api-*` scenarios, which now
include `cli-update-watched`. See the shared [build and checksum
provenance](2026-08-05-local-acceptance-provenance.md).

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [JSON](2026-08-05-local-acceptance-sha1.json), [Markdown](2026-08-05-local-acceptance-sha1.md) | All samples completed without timeout or product failure. Nine scenarios passed; `api-update`, both same-task bursts, `cli-depend`, and `cli-move` missed their duration targets. `cli-list` has no target. |
| SHA-256 | [JSON](2026-08-05-local-acceptance-sha256.json), [Markdown](2026-08-05-local-acceptance-sha256.md) | All samples completed without timeout or product failure. Nine scenarios passed; the same five scenarios missed their duration targets. `cli-list` has no target. |

The miss set is exactly the one the 2026-08-02 v0.3.0 evidence recorded, so this
work introduced no new missed budget.

These numbers were measured after `WB-01KZ1JCYZCPD156TCXMRB4Z6ZB` landed and
rewrote projection refresh to walk operation chains. An earlier pair measured
before it was re-run rather than carried forward, because the older evidence
described a tree that no longer exists. The measured values moved by less than
two milliseconds, which says the refresh rewrite does not reach the timed
mutation path — each cold CLI sample rebuilds the projection untimed, and the
timed command advances a single head rather than refreshing.

The decisive result is that a watched mutation is a local mutation:

| Scenario | SHA-1 p95 | SHA-256 p95 | Target | Git processes |
| --- | ---: | ---: | ---: | ---: |
| `cli-update` (`--no-sync`) | 174.63 ms | 174.45 ms | 200 ms | 10 |
| `cli-update-watched` | 172.85 ms | 173.82 ms | 200 ms | 10 |
| `cli-update-autosync` | 706.97 ms | 711.54 ms | 1,000 ms | 26 |

Deferring to a watcher removed 534 ms in SHA-1 and 538 ms in SHA-256, and
sixteen of the twenty-six Git processes. The watched scenario came in marginally
below the unsynchronized one in both formats, by 1.78 ms and 0.63 ms. That
difference is noise and should not be read as watching being *faster* than not
synchronizing at all; the claim it supports is the weaker and more useful one,
that the difference between them is no longer measurable.

The Git-process count is the sturdier half of this evidence. Durations move with
host load, but 10 against 26 is a structural count of the work the command
performs, and it says plainly that both network round trips left the critical
path rather than merely getting faster.

### 2026-08-02 v0.3.0 local acceptance evidence

Release `v0.3.0` was exercised once per supported Git object format with the
same frozen product and harness binaries. Both invocations used 500 total tasks
(475 active and 25 tombstoned), 20 operations per task, 20 samples per scenario,
a 60-second command timeout, and all 14 local `cli-*` and `api-*` scenarios,
which now include `cli-list` and `cli-update-autosync`. See the shared [build and
checksum provenance](2026-08-02-local-acceptance-provenance.md).

| Format | Evidence | Outcome |
| --- | --- | --- |
| SHA-1 | [JSON](2026-08-02-local-acceptance-sha1.json), [Markdown](2026-08-02-local-acceptance-sha1.md) | All samples completed without timeout or product failure. Eight scenarios passed; `api-update`, both same-task bursts, `cli-depend`, and `cli-move` missed their duration targets. `cli-list` has no target. |
| SHA-256 | [JSON](2026-08-02-local-acceptance-sha256.json), [Markdown](2026-08-02-local-acceptance-sha256.md) | All samples completed without timeout or product failure. Eight scenarios passed; the same five scenarios missed their duration targets. `cli-list` has no target. |

The automatic synchronization budgets both held:

| Scenario | SHA-1 p95 | SHA-256 p95 | Target | Git processes |
| --- | ---: | ---: | ---: | ---: |
| `cli-update` (`--no-sync`) | 179.76 ms | 170.93 ms | 200 ms | 9 |
| `cli-update-autosync` | 695.90 ms | 745.53 ms | 1,000 ms | 25 |

Synchronizing a mutation therefore cost 516 ms in SHA-1 and 575 ms in SHA-256
against a local bare origin, well inside its budget but above the roughly 420 ms
the design projected from connection latency alone. The difference is the broad
fetch: enumerating and validating 500 task refs is real work that a cheap local
connection does not hide, and it grows with the project. The targeted push is
the half that stays constant.

`cli-move` missed at 227.90 ms and 219.61 ms, having passed at 199.18 ms in the
2026-07-29 evidence, and `cli-update` rose from 153.74 ms to 179.76 ms while
still passing. Both scenarios pass `--no-sync` and perform no synchronization,
so neither delta is explained by this release's behaviour. The report
`environment` block records no CPU model or host load, so these one-shot runs
cannot be attributed to a code change rather than the host. Per the acceptance
rules the misses are recorded rather than retried or tuned.

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

## Proposed `api-update` target: 150 ms, pending sign-off

**Status: proposed, not applied.** The harness still evaluates `api-update`
against 100 ms and still reports it as a `miss`. Nothing in this section changes
a target; changing one needs the project owner's sign-off, and this is the
evidence that decision should be made on.

`api-update` has missed its 100 ms inclusive p95 target in every acceptance set
recorded since 2026-07-29 — eight independent measurements across four dates and
both object formats, with no measurement on either side of the line:

| Date | Format | p95 | Median | Outcome | In derivation |
| --- | --- | ---: | ---: | --- | --- |
| 2026-07-29 | SHA-1 | 134.16 ms | 97.39 ms | miss | yes |
| 2026-07-29 | SHA-256 | 102.85 ms | 98.54 ms | miss | yes |
| 2026-08-02 | SHA-1 | 114.06 ms | 107.25 ms | miss | yes |
| 2026-08-02 | SHA-256 | 108.91 ms | 105.82 ms | miss | yes |
| 2026-08-05 | SHA-1 | 121.37 ms | 119.31 ms | miss | yes |
| 2026-08-05 | SHA-256 | 121.49 ms | 119.27 ms | miss | yes |
| 2026-08-08 | SHA-1 | 121.98 ms | 114.67 ms | miss | no — contended host |
| 2026-08-08 | SHA-256 | 179.88 ms | 125.34 ms | miss | no — contended host |

Eight for eight is the important number. A target missed occasionally describes
a noisy scenario; a target missed by every measurement ever taken of it describes
a budget that was never achievable on this workload, and continuing to report it
as a defect trains readers to skip the miss list.

The proposal is derived from the six quiet-host measurements only. The
2026-08-08 pair is discussed as corroboration at the end of this section, along
with the reason one of its two points argues against the number proposed here.

The proposal is **p95 ≤ 150 ms**, and it is bounded from both directions rather
than picked for comfort:

- **Below, by the measurements.** The worst of the six p95 values is 134.16 ms,
  and the other five sit between 102.85 ms and 121.49 ms. A target under about
  135 ms would keep failing on evidence that shows no defect. 150 ms clears the
  worst observed value by 12%, which is roughly the spread the six points
  already show between quiet hosts.
- **Above, by the approved local budget.** `api-update` performs the same
  single-task mutation as `cli-update --no-sync`, which is approved at 200 ms
  and has measured 153.74–179.76 ms across the same six runs. The warm HTTP path
  does that mutation without paying for a process start or for opening the
  projection, so it must stay meaningfully cheaper than the cold CLI path. Any
  target at or above 200 ms would permit a regression that erased the warm
  path's entire advantage and still report a pass. 150 ms is 25% under the cold
  budget, so it still fails that regression.

What the number prices is a Git object write, not HTTP framing. The warm server
holds the projection open and the fixture repository has no origin, so the sample
synchronizes nothing; the handful of Git processes each sample records — six on
2026-07-29, seven on 2026-08-05, eight on 2026-08-08 — are that local write. The
six quiet-host runs agree it costs roughly 100–135 ms at p95 on a 525-task board,
and the count creeping upward is worth watching on its own. The original 100 ms
budget assumed a warm server would bring a mutation into double digits, and the
measurements say the write floor alone sits at about that value before any
variance is added.

Re-approving rather than optimizing is a deliberate choice and worth stating as
one. The alternative — making the write cheaper — is real work with its own
design questions, and nothing in the evidence suggests a user-visible problem at
120 ms in a browser form save. Recording the achievable number is the honest
move before beta; if the write is later made cheaper, the target can come back
down against evidence rather than against an aspiration.

Two limits on this proposal:

- It is a single fixture point. 150 ms describes a 525-task board and says
  nothing about how the warm write grows with the population. Those fixture
  points should be coordinated with the field-size bounding work so both
  re-measure against the same board.
- The 2026-08-08 agent hot loop evidence re-measured `api-update` on a
  contended host and is **not** part of this derivation. It is cited only as
  corroboration that the miss is structural — eight for eight now, across four
  dates. A loaded host cannot make a command faster, so those two points confirm
  the floor without moving it.

  One of them is worth surfacing rather than burying, because it argues against
  the proposal: the 2026-08-08 SHA-256 `api-update` p95 is **179.88 ms**, above
  the 150 ms being proposed. It is excluded because `cli-update` missed its own
  long-standing budget by 52% in that same run, which prices the host rather
  than the product — but a reader who thinks a target should hold on a developer
  machine running several agents at once should know that 150 ms would not have
  held in that run, and that a target which does hold there is a larger number
  answering a different question. That is a call for the project owner, and it
  is the reason this section proposes rather than decides.

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

### 2026-08-05 re-baseline

The change history view moved projection refresh and rebuild from scaling with
task count to scaling with operation count, because the projection now
materializes each task's recorded operations rather than its current state
alone. The family was re-measured on the same 525-total by 20 fixture in both
object formats; see the [re-baseline
provenance](2026-08-05-projection-refresh-provenance.md).

| Changed heads | 2026-07-30 SHA-1 (ms) | 2026-08-05 SHA-1 (ms) | Git processes, before → after |
| ---: | ---: | ---: | --- |
| 0 | 67.54 | 72.31 | 3 → 3 |
| 1 | 99.17 | 144.49 | 5 → 8 |
| 5 | 108.10 | 153.18 | 5 → 8 |
| 50 | 137.55 | 187.98 | 5 → 8 |
| 500 | 399.53 | 585.20 | 5 → 8 |

An unchanged refresh is the control and did not move. Any changed refresh now
also walks each changed task from the head the projection holds to its new one,
which costs three more Git processes in total — not per task — and reads that
task's previously unread operation packs. The marginal cost rose from 0.6640 to
1.0258 ms per changed head, and the disposable cache grew from 372,736 to
4,726,784 bytes at 20 operations per task. These remain descriptive
measurements; the family still has no pass threshold.

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

An omitted `--scenario` also selects `watch-steady-state`, which spends two
62.5-second windows per sample. At the 20 samples local acceptance requires that
is over forty minutes of wall clock on its own, so run the watcher family in its
own invocation with a small `--samples` value, as its section below describes,
rather than letting a whole-registry run drag it along.

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
| `sync-divergent-tips` | `sync` with one task whose local and remote tips diverge, replayed and published | none yet; reported as `not-evaluated` |
| `sync-malformed-local-tip` | `push` with one malformed owned local task ref | at most 2 seconds; fewer than 20 Git processes |
| `sync-malformed-remote-tip` | `fetch` with one malformed fetched tracking ref | at most 2 seconds; fewer than 20 Git processes |

The remote report target is evaluated per scenario. `pass` means every sample
completed within the inclusive time limit and below the exclusive Git-process
limit; `miss` means a completed sample exceeded either budget; `timeout` means
at least one sample reached its command timeout; and `failed` means a command
sample failed. Scenarios with no target are `not-evaluated`, which is where
`sync-divergent-tips` sits: reconciliation replays local history rather than
refusing to publish it, so the scenario measures work no recorded run has priced
yet and gets a budget from observed evidence rather than a guess. Expected
product errors in the malformed topologies remain measured samples when their
result and ref invariants are correct, but their nonzero command samples still
produce `failed` evidence. A timeout's elapsed duration is a lower bound:
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

Prefer within-run slopes when comparing one revision to another. A slope is a
ratio between two points measured minutes apart on one host, so a uniformly
faster or slower machine largely cancels out of it, while comparing a raw
millisecond figure across runs does not survive a different host or a different
background load. The environment block records the OS, architecture, and tool
versions, but not the CPU model or the host's load, so a cross-run millisecond
comparison is only as good as the operator's account of the machine.

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

Two depths give a ratio, not a shape. Evidence that argues about how a resource
grows with history needs more than two points, so a growth claim should be
measured with something like `--storage-operations 20,50,100,200` and read as a
curve rather than as a single before-and-after pair.

The scaling matrix and this measurement do not share a fixture shape at their
nominally similar points. A `500`-active scaling point is 525 total refs, while
`--tasks 500 --tombstones 25` is 500 total with 475 active. A slope from one
family and a byte count from the other therefore describe different
repositories; say which, or pass `--tasks 525` and accept that the result no
longer compares to earlier 500-total evidence.

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

So does a measured command whose result does not match its literal oracle. A
peak-memory number is only evidence about a command that did the work it names,
so `rebuild` must report the fixture's exact task count and `validate --full`
must match the same complete-audit oracle the scaling path asserts: every task
checked, every commit checked, no cache hits, and no failures. Both are checked
on the measured stdout, not on the exit code alone.

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

Three caveats matter when reading these numbers:

- Peak resident memory from `wait4` is a maximum, not a sum. It is the largest
  resident set observed for the measured process or any descendant it reaped, so
  a command that runs several `git` processes concurrently reports the largest
  single peak, not the concurrent total. Read it as a lower bound on whole-tree
  peak memory. It also means a measured peak can belong to a `git` child rather
  than to Workbook; attributing one to the product requires measuring the same
  Git work on its own.
- Peak resident memory is not run-deterministic. The fixtures are deterministic,
  built from seeded identifiers and fixed timestamps, so a rerun measures the
  same repository; allocator behavior, page-cache state, and host load still move
  the byte count. Deterministic evidence here means a deterministic fixture plus
  recorded provenance, not a reproducible byte count.
- Darwin does not maintain `ru_inblock` or `ru_oublock`. On darwin those fields
  are zero and `blockIoCountersSupported` is false; a zero there is not evidence
  that no I/O happened. Use `majorPageFaults` and `repositoryBytesDelta`
  instead.

Deliberately not measured: unreachable and dangling objects, non-Workbook refs
and working-tree files including `.workbook/config.json`, reflog and
`packed-refs` bytes, delta chain depth, concurrent whole-process-tree peak
memory, cold-start page-cache behavior, and any latency budget.

### 2026-08-07 bounded full validation evidence

The 2026-07-30 evidence identified full history validation as the only measured
path that did not scale, superlinear on both axes and the only one whose peak
resident memory grew with history. Both families were re-measured after that was
fixed. See the [build, environment, and measurement
provenance](2026-08-07-bounded-validation-provenance.md), which records the
frozen binary checksums, the shared-host caveat, and why the conclusions rest on
within-run slopes and same-host pairs.

| Family | Evidence | Outcome |
| --- | --- | --- |
| Scaling matrix | [SHA-1 JSON](2026-08-07-scaling-slopes-sha1.json), [Markdown](2026-08-07-scaling-slopes-sha1.md); [SHA-256 JSON](2026-08-07-scaling-slopes-sha256.json), [Markdown](2026-08-07-scaling-slopes-sha256.md) | `validate-full-history` within-run slopes fall below 1.0 on every axis pair in both formats: task-count 1.10 → 0.66 and 1.56 → 0.86 in SHA-1. This comparison is cross-run against 2026-07-30; the intended same-host baseline run did not complete, which the provenance record states. No other scenario regressed. Git processes stay at 7. |
| Storage and peak resources | [SHA-1 JSON](2026-08-07-storage-resources-sha1.json), [Markdown](2026-08-07-storage-resources-sha1.md); [SHA-256 JSON](2026-08-07-storage-resources-sha256.json), [Markdown](2026-08-07-storage-resources-sha256.md); baselines [SHA-1](2026-08-07-storage-resources-baseline-sha1.json), [SHA-256](2026-08-07-storage-resources-baseline-sha256.json) | Four depths rather than two. Peak resident memory above the measured `git cat-file --batch` floor no longer grows with depth: 58.8 MB → 694.8 MB across a 10x depth increase before, 5.7 MB → 0.1 MB after. Full validation elapsed time falls 5.2x to 18.1x. |

Both families were measured with one frozen `workbook-bench` against two product
binaries, the change and its `main` parent, so the product under test is the
only variable within each pair.

## Sync watcher steady state

`workbook sync --watch` is the one Workbook process a beta user leaves running
for hours, and until now nothing measured it. `cli-update-watched` runs a watcher
with a one-hour interval precisely so it never synchronizes during the sample:
that scenario measures the mutation hand-off and deliberately not the daemon.

The `watch-steady-state` selector measures the daemon itself. It observes a live
`workbook sync --watch` against an already-synchronized origin with nothing
pending, which is the state such a process spends nearly all of its life in, and
reports CPU, peak resident memory, Git processes, and durable bytes over a
bounded window.

One window cannot separate the cost of a scheduled tick from the cost of merely
having the process alive, because a watcher synchronizes once when it starts and
once when it is interrupted whatever its interval is. So each observation runs
two windows of identical wall-clock length against the same fixture:

| Window | Interval | What it prices |
| --- | --- | --- |
| `idle-control` | 1 hour | The fixed costs: process startup, the opening synchronization, the shutdown synchronization, and simply being alive for the window. No tick is scheduled inside it. |
| `steady-interval` | 5 seconds — the product's own default | The same fixed costs plus every scheduled tick. |

The observation window is 62.5 seconds, twelve steady intervals plus a
half-interval margin, so the scheduled tick count is unambiguous even when a tick
runs long. The control runs first in every observation, so a steady window is
never the first thing a cold page cache sees. Subtracting the control's medians
from the steady window's leaves the marginal cost of the ticks and nothing else,
reported under `perSynchronization`.

Ticks are counted from Trace2 `cmd_name` events rather than from raw process
starts. `Repository.Sync` runs exactly one `git fetch` per call, so counting
fetches counts synchronizations without the harness assuming how many other Git
processes a tick spawns. A run whose steady window observed no more
synchronizations than its control is refused rather than reported: it measured
nothing about ticking, and dividing by that difference would invent evidence.

Everything before a window opens is setup — binding the socket, printing the
readiness line, and completing the opening synchronization — and the Trace2
cursor opens at the moment the clock starts. Both windows pay for exactly one
`workbook sync --status` probe, taken as the window closes to confirm the watcher
was still running and still trustworthy when it was measured.

The family is descriptive. It has no duration, memory, or Git-process target, it
contributes no row to the scenario table, and its results appear in their own
report block instead. Every watcher tick reads each canonical and tracking tip,
so the harness rejects a watcher run below 500 tasks and 20 operations per task:
a per-tick number measured on a tiny fixture would describe no board anyone runs.

Because each observation costs two 62.5-second windows, the family is measured in
its own invocation with a small `--samples` value rather than the 20 that local
acceptance requires. Twenty observations would be well over an hour of wall clock
per object format and would not sharpen a median.

```sh
go build -buildvcs=false -o /private/tmp/workbook-watcher ./cmd/workbook
go build -buildvcs=false -o /private/tmp/workbook-watcher-bench ./cmd/workbook-bench

/private/tmp/workbook-watcher-bench \
  --workbook /private/tmp/workbook-watcher \
  --tasks 525 \
  --tombstones 25 \
  --operations 20 \
  --samples 3 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario watch-steady-state \
  --output-json docs/performance/2026-08-08-watcher-steady-state-sha1.json \
  --output-markdown docs/performance/2026-08-08-watcher-steady-state-sha1.md
```

Repeat once with `--object-format sha256`.

### 2026-08-08 sync watcher steady-state evidence

The family was observed once per supported Git object format with the same frozen
product and harness binaries as the hot loop pair, using 525 total tasks (500
active and 25 tombstoned), 20 operations per task, three observations, and a
60-second command timeout. See the shared [build, checksum, and host-load
provenance](2026-08-08-agent-loop-and-watcher-provenance.md).

| Format | Evidence |
| --- | --- |
| SHA-1 | [JSON](2026-08-08-watcher-steady-state-sha1.json), [Markdown](2026-08-08-watcher-steady-state-sha1.md) |
| SHA-256 | [JSON](2026-08-08-watcher-steady-state-sha256.json), [Markdown](2026-08-08-watcher-steady-state-sha256.md) |

| Window | Format | Synchronizations | Git processes | CPU (% of one core) | Peak resident | Major faults | Repository bytes |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `idle-control` | SHA-1 | 1 | 9 | 0.90–1.28% | 32.6–33.4 MB | 148 | −95 |
| `steady-interval` | SHA-1 | 13 | 117 | 6.78–7.64% | 33.5–34.9 MB | 952 | −95 |
| `idle-control` | SHA-256 | 1 | 9 | 0.91–1.40% | 31.2–33.7 MB | 144 | −95 |
| `steady-interval` | SHA-256 | 13 | 117 | 6.16–6.71% | 34.1–34.6 MB | 924 | −94 to −95 |

| Derived | SHA-1 | SHA-256 |
| --- | ---: | ---: |
| CPU per synchronization | 338.54 ms | 300.79 ms |
| Git processes per synchronization | 9.00 | 9.00 |
| Repository bytes per synchronization | 0 | 0 |
| Peak resident delta, steady vs control | 1,654,784 B | 1,851,392 B |

**A tick is exactly nine Git processes, and the count is not an estimate.** All
six steady windows recorded 13 synchronizations and 117 Git processes, and all
six control windows recorded 1 and 9, in both object formats without a single
outlier. 117 is 9 × 13. The subtraction that derives the per-tick cost is
therefore reading a structural constant rather than smoothing noise, and it is
the sturdiest number in this section: process counts carry no host-load variance.

**A watcher at the product's default interval costs roughly 7% of one core,
continuously.** The control window shows what merely being alive costs — about
1% of a core for startup and the two bracketing synchronizations — so the
scheduled ticks are the rest. At a 5-second interval that is about 300–340 ms of
CPU every 5 seconds, or roughly four minutes of CPU per hour of wall clock on a
525-task board. That is the number to weigh before telling a beta user to leave
one running all day, and it is a real cost rather than a rounding error.

**Nothing durable is written, and the peak does not obviously grow.** The
repository shrinks by 94 to 95 bytes in every window, control and steady alike,
which makes it a fixed effect of the process lifetime rather than something ticks
do; per-synchronization durable bytes are 0. Peak resident memory
sits near 33 MB and the steady window's peak is under 2 MB above the control's.
Per the caveats below that delta is a difference between two maxima and not a
measured allocation, so read it as "twelve ticks did not obviously grow the
peak" — which is the useful claim for a process left running for hours.

**Ticks are not free of I/O.** Major page faults rise from 148 to 952 in SHA-1
and from 144 to 924 in SHA-256 — 65 to 67 per tick. On darwin this is the usable
I/O-pressure signal, and it is where the per-tip reads show up.

One thing this evidence does **not** establish is the shape of that cost against
board size. It is a single 525-task fixture point, measured at one interval. The
concern that motivated the family — that every tick reads each canonical and
tracking tip through a fully buffering Git helper, so per-tick work should grow
with the task population — is consistent with 9 processes and 300–340 ms per tick
at this size, but consistency is not evidence of a slope. Establishing one needs
the same observation repeated at two or more populations.

### Reading the watcher block

The JSON report gains a `watcherSteadyState` object
(`workbook.watcher-steady-state` version 1) and the Markdown report gains a "Sync
watcher steady state" section. Runs that did not observe a watcher omit both.

The block records `platform`, `maxResidentRawUnit`, the measured `fixture`, the
number of `observations`, the `windowMilliseconds` each one ran for, the `idle`
and `steady` window arrays, and the derived `perSynchronization` cost.

Per window:

| Field | Meaning |
| --- | --- |
| `name`, `intervalMilliseconds` | Which window this is and the `--interval` it ran with. |
| `observedMilliseconds` | Measured wall clock of the window, which may exceed the requested length. |
| `synchronizations` | `git fetch` invocations recorded inside the window, one per completed synchronization. It includes the shutdown synchronization that follows the interrupt, because that work is inside the observed process lifetime; the control counts the same one and subtracts it. |
| `gitProcesses` | Trace2 process starts inside the window. |
| `userMilliseconds`, `systemMilliseconds`, `cpuMilliseconds` | Kernel-reported CPU time for the watcher and every descendant it reaped. |
| `cpuPercentOfOneCore` | `cpuMilliseconds` over `observedMilliseconds`, as a percentage of a single core. |
| `maxResidentRaw`, `maxResidentRawUnit`, `maxResidentBytes` | `ru_maxrss` as the kernel reported it, its unit, and the normalized byte value. This is a whole-lifetime peak, so it also covers the untimed startup synchronization before the window. |
| `minorPageFaults`, `majorPageFaults` | `ru_minflt` and `ru_majflt`. On darwin these are the usable I/O pressure signal. |
| `voluntaryContextSwitches`, `involuntaryContextSwitches` | `ru_nvcsw` and `ru_nivcsw`. |
| `repositoryBytesDelta` | Change in on-disk bytes under the repository root across the window, sampled outside the CPU accounting. It answers whether an idle daemon writes durable bytes every tick. |

`perSynchronization` reports the marginal `synchronizations` the steady window
added, the `cpuMillisecondsPerSynchronization`,
`gitProcessesPerSynchronization`, and `repositoryBytesPerSynchronization`
derived from them, a `maxResidentByteDelta` between the two windows, and a
`description` naming both intervals and the window length.

Three caveats carry over from the storage measurements and matter here too. Peak
resident memory from `wait4` is a maximum rather than a sum, so a watcher running
several `git` processes concurrently reports the largest single peak and the
number may belong to a `git` child rather than to Workbook. It is not
run-deterministic: the fixture is, but allocator behavior, page-cache state, and
host load move the byte count. And `maxResidentByteDelta` is a difference between
two such peaks, so read a small value as "the steady window did not obviously
grow the peak", not as a measured allocation.

One more limit is worth stating plainly. This family measures a single fixture
point, so it prices what a tick costs on a 525-task board and says nothing about
how that cost grows with the board. Every tick reads each canonical and tracking
tip through a fully buffering Git helper, so per-tick allocation is expected to
scale with the total task count even when nothing changed; establishing that
shape needs the same observation repeated at two or more task populations, which
is a separate measurement rather than a reading of this one.

## Reading the reports

A completed harness run produces a versioned, machine-readable JSON report and a
compact generated Markdown view of the same scenarios. Each scenario then
records a concrete result, timeout, or product-command failure. Harness and
output failures remain fatal and may prevent report creation, as happened for
the current hand-authored lower-bound evidence.
