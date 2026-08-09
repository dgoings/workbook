# Sync Watcher Design

## Goal

Make automatic synchronization cheap enough that nobody wants to turn it off.

An optional foreground process, `workbook sync --watch`, keeps this clone's task
refs and projection current. While one is running, a mutation writes locally and
hands publication to it, so the command costs what a local mutation costs. With
no watcher running, every command behaves exactly as it does today.

This reverses two rules the project currently states.

The first is `AGENTS.md`'s "Avoid adding a required background daemon. A daemon
may optimize IDE usage, but every operation must work through the CLI." Half of
that survives and is strengthened below: no operation may require the watcher.
The prohibition on shipping one does not survive, and the file changes with this
design.

The second is generated into `.workbook/guidelines.md` and every agent's
context by `internal/agentdocs/render.go`: "there is no reconcile or continue
command, and no conflict state is kept between invocations." The watcher keeps
exactly that state, for the reason developed under *Conflicts* below. That
sentence is as load-bearing as the `AGENTS.md` line and is reversed with it.

## Measured Cost

The design rests on measurements rather than estimates.
`docs/performance/2026-08-02-local-acceptance-sha1.md`, at v0.3.0, 525 tasks,
20 samples:

| Scenario | p95 | git processes |
| --- | ---: | ---: |
| `cli-update` (`--no-sync`) | 179.76 ms | 9 |
| `cli-update-autosync` | 695.90 ms | 25 |

Automatic synchronization costs 516 ms and 16 git processes per mutation. The
2026-07-31 measurements explain why it cannot be trimmed in place: roughly
200 ms of an HTTPS round trip buys *a connection*, not the refs carried on it,
and this design needs two of them — `fetchBefore` and `publish` in
`internal/cli/autosync.go`.

An agent pays that three times for one task: `next`, `update --status
in-progress`, `update --status in-review`.

The cost is not removable from a mutation. It is removable from a mutation's
*critical path*, which is what this design does.

## Why This Is Safe Now And Was Not Before

Deferring the fetch means a mutation applies to a tip that may be seconds stale.
Before replay-based reconciliation landed, that was a hazard worth 516 ms to
avoid: a stale tip produced a divergent history, and divergence had no automatic
resolution.

Reconciliation changed the arithmetic. A clone that diverged from `origin` now
replays its own unpublished operations onto the fetched tip and parks the tip it
replaced. Applying to a slightly stale tip is no longer a case to prevent. It is
a case the fetch path already handles, and the watcher's next tick handles it
without anybody present.

## The Loop

A new `internal/syncloop` package owns one loop, hosted by two commands. Each
tick:

1. `Repository.Sync`, which is `fetch()` followed by `publishFetched()` reusing
   the fetched tips, so no `ls-remote`.
2. `Repository.PruneParkedRefs`, new, described under *Adjacent Defects*.
3. `projection.Store.Refresh`, but only when the sync reported changed refs.

A quiet tick is one `git fetch` and nothing else. `publishFetched` builds a
refspec only for tasks whose canonical head differs from tracking and guards the
push behind a non-empty set, so a clone with nothing to publish issues no push
at all. This matters because it is the steady state.

The refresh is gated on the sync reporting changed refs. `refreshChangedHeads`
already returns early when nothing changed, so this is a smaller saving than it
first appears: it avoids one `git for-each-ref` and two SQLite round trips on a
quiet tick, not the walk itself. It is still worth having, because a quiet tick
is the steady state and a watcher runs one every interval forever.

The walk is what makes the gate worth stating rather than assuming.
`WB-01KZ1JCYZCPD156TCXMRB4Z6ZB` landed the operations table, so refresh now
reads every intermediate commit a changed task gained rather than just its tip,
and scales with operation count rather than task count. The gate does not avoid
that cost when refs did change — nothing can, since that is the work — but it
keeps the loop from paying anything for it while idle.

| Knob | Default | Purpose |
| --- | --- | --- |
| `--interval` | 5s | scheduled tick spacing |
| quiet period | 250 ms | nudge coalescing and collision damping |
| staleness threshold | `max(3×interval, 30s)` | when the CLI stops trusting a watcher |
| shutdown budget | 5s | bounds the final sync |

### Coalescing is load-bearing

Ten mutations must not produce ten serialized 500 ms syncs. A one-deep pending
flag plus the quiet period collapses a burst into a single follow-up sync;
`cli-burst-independent-10` and `cli-burst-same-task-10` are the scenarios that
would otherwise expose this.

A scheduled tick is also skipped while a nudged synchronization is pending, so
the timer does not follow a burst with a second redundant round trip.

That is a narrower guarantee than it may look, and the narrower reading is the
correct one. A watcher's fetch transaction can still collide with a CLI's write
compare-and-swap, and the loser still gets `stale-write`, exit 6. No nudge-based
suppression can prevent that, because the collision window opens *before* the
nudge exists — the watcher has no way to know a write is coming. What the rule
does prevent is a tick landing on the heels of a burst while more mutations are
still arriving. The residual collision keeps its existing documented recovery:
run the identical command again.

A lock was considered and rejected. Held across the watcher's ~500 ms network
sync it would stall the CLI, which defeats the entire design; held only around
the ref transaction it would have to reach inside `Repository.Fetch`. The
watcher retrying on its next tick costs the user nothing, because the user is
not waiting on it.

### Status must not share a lock with the sync

The status handler serves from an `atomic.Pointer` published by the sync
goroutine. If it read through a mutex the sync goroutine held across `git
fetch`, a watcher wedged on a hung network call would block every probe for the
full network timeout, and every mutation would pay it before falling back. This
gets its own test.

## Rendezvous

A Unix domain socket, because a successful `net.Dial` is simultaneously the
liveness test, the nudge channel, and the status channel. The alternative —
lock file, pid file, and a signal — establishes liveness but carries no data, so
it needs a state file anyway, and `SIGUSR1`'s default disposition is *terminate*,
so signalling a recycled pid can kill an unrelated process.

The socket path is **published, not derived**. Deriving it is wrong twice over.
`sun_path` is 104 bytes on darwin, and `Repository.CommonGitDir` is
`filepath.Clean`ed but not guaranteed absolute — `git rev-parse
--git-common-dir` returns `.git` from a repository root — so a hash of it
differs between two processes with different working directories. Deriving from
`$TMPDIR` is worse: it is per-user *and* per-bootstrap-namespace on macOS, so a
watcher started from launchd, `sudo`, or a different SSH session lands where the
CLI never looks. The CLI would then fall back to the inline path forever, and
the entire performance win would vanish with no diagnostic. A silent failure
mode is a worse trade than a tail risk.

So the watcher binds a short path in a directory only this user can write to,
and publishes the absolute result in `<common-git-dir>/workbook/watcher.json` —
beside `cache.sqlite` and `validation.sqlite`, and shared across linked
worktrees for the same reason those are. The candidates are `/tmp/workbook-<uid>/`
first, created `0700`, then `os.TempDir()`, then `<common-git-dir>/workbook/`,
and the first one that yields a path under 100 bytes and passes the directory
check wins.

`os.TempDir()` must not come first, which it originally did. It is the per-user
`$TMPDIR` on darwin, but plain world-writable `/tmp` on Linux, and the socket
name is `sha256(abs(common-git-dir))[:8]` — derived, so guessable. Another local
user who guesses the repository path can bind `/tmp/wb-<hash>.sock` before the
watcher does, and since a watcher dials before binding and reads anything that
answers as a live watcher, `sync --watch` and `serve` would refuse to start with
*a Workbook watcher already owns this repository*, permanently: a sticky `/tmp`
also refuses this process the unlink. That is a durable denial of the whole
optimization behind a misleading error, not a takeover — the rendezvous is the
pointer file, which the squatter cannot write.

So every candidate directory is rejected unless it is a real directory, not a
symlink, owned by the caller, and writable by neither group nor other; the
per-user directory is additionally required to be exactly `0700`. The socket
itself is created under a `0177` umask and chmodded `0600` afterwards. The chmod
alone was not enough: under the usual `umask 022` the interim mode denies
connect, but under `umask 0` the socket was briefly world-connectable, and
anything that connects can read `status` or drop a recorded conflict with `ack`.

The CLI reads one file. When no watcher is running that is a single `ENOENT`,
which is what keeps the unwatched path honestly unchanged.

A stale pointer file after `SIGKILL` is harmless: the dial fails and the CLI
falls back. Before binding, a watcher dials the recorded socket and unlinks only
if nothing answers.

Three request types, newline-delimited JSON. Both ends bound the line they read
as well as holding a five-second deadline. The deadline bounds how long a peer
may take, not how much it may send, so without a bound a peer that never writes
a newline grows the other end's buffer until the process dies. A request is a
command and a task ID, so 64 KiB; a response has to clear the largest honest
status, which is a description conflict per task carrying three descriptions of
`core.MaxDescriptionBytes` each, so twenty of those plus room for the JSON
framing around them — sized to the descriptions alone, the bound refuses the
twentieth.

The watcher bounds what it serializes below what the client accepts, so a
healthy watcher can never reach the client's limit. If it could, the failure
would sustain itself: the client refuses the status, so it never trusts the
watcher, so it never acknowledges a conflict, so the set that made the answer
too large never drains, and the watcher answers every dial while `sync
--status` reports it as not running. Past the budget the conflict list is
served as a prefix, in task-ID order, and drains the ordinary way.

```
{"status":{}}                             -> Status
{"nudge":{"taskId":"WB-…"}}               -> receipt
{"ack":{"taskId":"WB-…","head":"<oid>"}}  -> receipt
```

```json
{"format":"workbook.watcher-status","version":1,"pid":4211,"intervalMs":5000,
 "lastSyncAt":"…","lastSyncOk":true,"lastSyncError":"",
 "conflicts":[{"taskId":"WB-…","type":"description","head":"<oid>"}]}
```

Both formats are versioned from their first release.

## The Deferred Mutation

The change is confined to `internal/cli/autosync.go`. `openTaskSession` probes
for a watcher against a hard 50 ms deadline. When it defers, `fetchBefore` and
`publish` become no-ops and `mutate` sends the nudge after a successful write.
`syncReport.Status` gains `deferred` beside `completed`, `skipped`, and
`failed`.

Deferral requires all of: the policy is enabled; the pointer file resolves and
the status decodes within 50 ms; `lastSyncAt` is within the staleness threshold;
`lastSyncOk`; and the target task carries no conflict entry.

Anything else takes today's inline path. The watcher's last sync having failed
is deliberately in that list. If `origin` is unreachable the watcher knows, and
the CLI must go inline so the existing `core.WarningAutoSync` — "the change was
recorded locally, but …" — still reaches the user. Swallowing that to save
500 ms would trade the one guarantee the local-first design exists to state.

The nudge is request/response, waiting up to 50 ms for *receipt* rather than
completion. A watcher that died a moment ago is caught at the instant of
writing, and the CLI falls back to an inline `PushTask` there. Without this,
`deferred` would be a promise the CLI has no basis to make.

## Publication Semantics

This is the guarantee that changes, and it is stated plainly because
`AGENTS.md` requires distinguishing guarantees from best-effort behavior.

Today a mutation returns after `origin` has accepted the ref. Under deferral it
returns after the local write, and publication follows within milliseconds. The
window is bounded by the receipt handshake rather than by the interval, but it
is not zero: a watcher killed between receipt and push leaves the work local.

`workbook sync --status` reports what is outstanding, `workbook push` publishes
it, and the watcher's own shutdown runs a final sync so an ordinary Ctrl-C or
`kill` never strands work. The durable record is unchanged in every case — the
local commit is made before anything is attempted.

## Conflicts

This is the sharp edge of the design.

`mutate` fetches and then gates on `conflictFor(target)`. The gate exists
because if a replay hit a conflict on *this* task, local history dropped
operations, and mutating on top would silently build on a history missing the
caller's earlier work. Deferral removes the fetch, so the gate is dead.

Worse, the conflict does not merely go unreported — it evaporates. A stopped
replay leaves the ref truncated, so the *next* tick finds nothing divergent and
reports an empty list. Parked refs cannot substitute as a marker either, since
every divergent reconciliation parks a tip whether or not it conflicted.

Three parts:

**The watcher reports conflicts to its own stderr the moment they occur**, in
the existing `writeConflicts` format. The decision to ship a foreground process
is what makes this possible: it has a terminal, and that terminal is the channel
by which a human finds out. Without it a conflict occurring between two commands
has no reader at all.

**The status carries the conflict set**, each entry stamped with the task's head
via the existing `Repository.InspectTaskHead`, which needs no `core.Conflict`
format change. The deferred path seeds `session.conflicts` with the whole set
and lets the existing `conflictFor` fire unchanged, so output is byte-identical
to today and `writeMutationOutcome` renders it with no new code. Seeding the
whole set rather than only the target matches today's semantics, where a fetch's
entire conflict list is reported and `next` deliberately surfaces unrelated
ones.

**Two independent clearing mechanisms**, because today's gate is one-shot by
construction — exit 8, then the identical retry succeeds — and bare set
membership would re-fire forever and wedge the task:

- *Acknowledgement.* Reporting a conflict to a human sends `ack` and removes
  that entry, reproducing the one-shot semantics with no dependence on a clock.
- *Expiry by head-move.* Each tick drops any entry whose task head no longer
  matches the recorded one. It is free, since the watcher already enumerates
  refs, and it retires conflicts on tasks nobody returns to.

Because head-move expiry exists, the CLI needs only task-ID membership, so the
deferred path adds no git call and the change stays inside one file.

A fail-closed rule was drafted here and then removed. `conflictFor` returns "no
conflict" when a prefix will not resolve, which reads like failing open, but
`Store.Resolve` queries the projected task table with no filter on `deleted`, so
it resolves tombstoned tasks like any other. Resolution therefore fails only
when a prefix genuinely matches nothing or is ambiguous, and in both cases the
mutation itself fails identically on the same lookup. The gate cannot be slipped
past, so guarding it would have added a branch defending nothing.

**Residual risk, stated rather than hidden.** A conflict that occurs and is
head-move-expired before any command runs, or that occurs while the watcher is
later restarted, leaves nobody notified through the CLI. The dropped text
remains at `refs/workbook/reconciled/<task-id>/<n>` and the watcher's stderr
line is the record. This is strictly weaker than inline mode, and it is the
price of deferring the fetch.

## `workbook next`

`next` fetches today so two agents do not claim the same task. Deferring widens
that window from *now* to *up to one interval*.

It defers anyway. `next` opens without a writer and claims nothing; it reports a
recommendation, and the mutation that acts on it carries its own gate. Recording
this as a decision rather than letting it fall out of the implementation is the
point of the paragraph.

## Command Surface

```
workbook sync [--watch [--interval <duration>]] [--status] [--json]
```

No new noun. `--watch` is the existing sync command on a loop, which is
literally what it is, and framing it that way keeps the optimization from
reading as a new subsystem. `--interval` is a string option parsed with
`time.ParseDuration`, so the existing two-kind flag schema is untouched.

`--status` reports whether a watcher is live and what it last did, and exits 0
when none is.

`workbook sync --watch` ignores a disabled auto-sync policy, exactly as
`workbook sync` already does. Running it is explicit intent.

## The Web Board

`serve` performs no synchronization today, so the board is a purely local view.
Hosting the loop inside it means the browser's existing 1 Hz poll of
`/api/tasks` surfaces teammates' changes with no JavaScript change, no new
endpoint, and no server-sent events.

Whoever binds the socket owns the loop. A `serve` that finds a live external
watcher starts normally, runs no loop, says so on stderr, and retries binding
each tick, so a watcher's death is picked up within one interval, `serve` never
fails to start, and nothing ever double-fetches.

`WB-01KYM85R7X6FSFR5ER7ZDJ8T3N` owns the rest: web *mutations* participating in
synchronization, the optimistic queue, and the UI toggle. It should depend on
this story, because once the loop exists the right implementation of "bring the
board onto auto-sync" is to nudge the in-process loop after a web mutation
rather than to fetch and push inline in an HTTP handler — which is materially
cheaper, and removes the latency the optimistic queue exists to hide.

## Adjacent Defects

A long-lived process makes three latent bugs routine. All are fixed here.

**Parked refs grow without bound.** Pruning runs only inside a mutation's ref
transaction, so `maxParkedRefsPerTask` holds only for tasks under active local
mutation. This is already true for hand-run `fetch` and `sync`, which made the
retention bound softer than `WB-01KZ1JCYZCPD156TCXMRB4Z6ZB` assumed when it
described parked reachability as bounded by `maxParkedRefsPerTask`; a watcher
that reconciles but never mutates would grow the namespace forever. Sweeping on
every tick restores that bound as written, and stays inside what the history
view already tolerates, since it treats a parked commit that no longer resolves
as an ordinary not-found. A
new `Repository.PruneParkedRefs` enumerates the namespace once, groups by task,
and deletes past the retained rank in one `update-ref --stdin` — two git
processes, and only when parked refs exist. Running it *after* the fetch does
not contradict the existing reasoning that pruning during a fetch would delete
recoverable work in the same command that orphaned it, because retention counted
from the post-fetch state always keeps the ref the fetch just created.

**A rebuilt projection cache wedges every process holding the old one.**
`cacheUsable` stats the *path* and queries the *old handle*, so after another
process renames a new cache into place the old inode still answers with valid
metadata and the check reports usable forever. The failure is not the silent
staleness it looks like: SQLite notices the file was replaced and reports every
subsequent write as `attempt to write a readonly database`, with a hint to run
`workbook rebuild` — the command that caused it. This already breaks
`workbook rebuild` during `workbook serve`. The fix compares `os.FileInfo` with
`os.SameFile` and splits recovery: a different inode means reopen, while a
missing file or a metadata mismatch means rebuild as today. There is already an
`os.Stat` on that path, so it costs nothing.

Reproducing it needs a handle with a live connection. `sql.Open` is lazy, so a
process that has not queried yet rebinds to the new file by accident on its next
query and the defect hides — which is why a long-running command is where it
surfaces.

**`SIGTERM` is unhandled.** Only `os.Interrupt` is trapped, so a backgrounded
watcher killed with plain `kill` dies without its final sync.

Per-tick configuration reload is deliberately *not* added. The loop reads only
the project key and ID, which are effectively immutable, and a watcher already
ignores the auto-sync policy by design. The tracked configuration file is
stat-ed per tick and reopened only when it changes.

## Performance Regression Protection

A new `cli-update-watched` scenario measures a mutation with a live watcher and
is held to `coldSingleTarget`, the 200 ms **local** budget, not the 1,000 ms
auto-sync budget. The entire claim of this design is that a watched mutation is
a local mutation, so it must be measured against the local budget or the
scenario proves nothing.

The fixture starts `sync --watch --interval 1h` so the watcher never
self-ticks, isolating the probe and nudge from the watcher's own git work.
Trace2 attribution is already correct, since the harness passes
`GIT_TRACE2_EVENT` only to the measured process.

The expected result is a p95 indistinguishable from `cli-update`'s, and **the
same git-process count as an unsynchronized mutation rather than sixteen more**.
The process count is the cleanest evidence, because it carries no network
variance.

`cli-update` and `cli-update-autosync` keep their existing budgets and meanings.

## Testing

Unit coverage, against a fake syncer with no Git:

- status answers while a sync is blocked, which is the atomic-snapshot invariant;
- a burst of nudges coalesces into exactly one follow-up sync;
- a scheduled tick is suppressed inside the quiet period;
- a conflict entry expires when the task head moves, and an acknowledgement
  removes exactly one entry;
- the final sync runs on cancellation against a fresh context;
- the loop is a quiet no-op with no `origin` configured;
- pointer-file round-trip, fast "no watcher" on both a missing file and a dead
  socket, a second watcher reporting the repository already owned, and socket
  path fallback past the length ceiling.

Integration coverage, using temporary local bare remotes rather than mocks:

- a watcher in one clone observes another clone's push with no command run in
  the watching clone;
- a mutation defers, and `origin` holds the new tip shortly after, which is what
  proves the nudge rather than the timer published it;
- the four fallbacks — no watcher, dead socket, stale status, failed last sync —
  each take the inline path, and the failed-last-sync case still emits the
  auto-sync warning;
- a watcher-reported conflict on the target exits 8 with output identical to
  today, records the acknowledgement, and the identical retry then succeeds;
- a conflict on an unrelated task never blocks the mutation;
- a watcher that answers but refuses to publish is caught by the receipt
  handshake and the change is pushed inline instead;
- a watcher publishes unsynced work on shutdown;
- `serve` surfaces an `origin` advance with no command run, and defers to an
  external watcher when one already owns the socket;
- `serve` shutdown stays within its budget with the final sync running
  concurrently with the HTTP drain rather than after it;
- the existing synchronization suite passes untouched, which is the real
  regression guard for the unwatched path.

## Out of Scope

**No daemonization.** The watcher is a foreground process. No fork, no `setsid`,
no supervision, no pid file, no auto-spawn, and no log file — it writes to the
stderr it was given, like every other command. Process lifetime belongs to the
shell, an editor, or a supervisor the user already runs, and `serve` for anyone
who wants one process instead of two.

**No auto-start from the CLI.** A mutation that finds no watcher takes the
inline path and says nothing about it. Starting a background process as a side
effect of an unrelated command would make the optimization a policy.

**No new web surface.** Hosting the loop inside `serve` is in scope; endpoints,
client changes, and the auto-sync toggle belong to
`WB-01KYM85R7X6FSFR5ER7ZDJ8T3N`.

**No multi-repository watcher.** One watcher serves one repository, keyed by its
common Git directory, and linked worktrees share it because they share that
directory.

**No change to conflict resolution.** The watcher reports and remembers
conflicts; resolving one is still running the ordinary command again.
