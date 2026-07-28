# Workbook Performance Architecture Design

## Goal

Make Workbook mutations responsive while preserving the existing Git-durable,
local-first contract. A successful CLI command or HTTP mutation still means the
canonical task ref has advanced. SQLite remains a disposable projection, and no
background daemon or pending-write queue becomes required.

The design first implements a streamlined synchronous path, then runs one
explicit acceptance evaluation. Missing a performance target triggers a new
architectural decision; it does not authorize an open-ended optimization loop.

## Performance Envelope

The interactive acceptance fixture contains:

- 500 active tasks;
- 20 historical operations per task; and
- the current full task fields, labels, dependencies, ranks, and tombstones.

The target budgets are:

- warm web/API Git-durable mutation p95 at or below 100 milliseconds;
- cold CLI Git-durable mutation p95 at or below 200 milliseconds; and
- ten rapid sequential same-task mutations and ten independent-task mutations
  each completing in less than one second.

Counts above 500 active tasks are diagnostic stress data, not interactive
release gates.

Remote wall-clock time depends on the network and Git host. Remote-sync
evaluation therefore reports network invocation count, local processing time,
localhost bare-remote time, and representative real-remote time separately.

## Observed Baseline

A directional local trace of the current implementation showed one update in a
roughly 30-task repository taking about four seconds and starting 256 Git
processes. The update resolved one full task ID by reconstructing and validating
every current task through Git before writing the selected task.

A separate directional plumbing experiment wrote 20 two-blob commit trees and
advanced a ref with compare-and-swap in 1.77 seconds, or about 89 milliseconds
per mutation, using straightforward Git subprocesses. This is not an acceptance
result, but it is sufficient evidence to try the synchronous design before
weakening the durability contract.

## Architectural Decision

Workbook will optimize synchronous Git durability first.

The core mutation service receives separate boundaries for:

- a projection-backed current-state reader; and
- a Git-backed canonical writer.

The current combined `TaskStore` shape may be split or composed behind an
adapter, but SQLite must not implement canonical task writes. The projection
may accept a newly Git-validated snapshot as a cache update after the canonical
write succeeds.

The design explicitly defers:

- a durable SQLite outbox;
- asynchronous Git reconciliation;
- a required daemon;
- a Git library;
- a persistent Git object-writing process; and
- automatic performance tuning after the acceptance evaluation.

Those options require a new decision supported by benchmark evidence.

## Current State and Historical Audit

Each task operation commit contains:

- `operation.json`, which records the change intent; and
- `state.json`, which records the complete resulting task checkpoint.

The task ref tip is therefore sufficient for ordinary current-state work.
Create, update, delete, restore, move, depend, free, list, board, show, and
`next` must not replay the complete task history.

SQLite records the validated tip object ID and its complete projected task
state. When the exact Git ref still equals that head, mutation planning uses
the projected state directly. When it differs, Workbook reads and validates
only the new tip or the unseen suffix after a known validated ancestor.

History is a separate audit and indexing concern:

- initial bootstrap, cache loss, or an explicit verification command may
  validate a complete history;
- subsequent validation begins at the last validated head;
- future read-only SQLite history tables may expose operations, actors, and
  timelines; and
- history queries never become a prerequisite for current-state mutations.

Backward, sideways, malformed, or generation-changing ref movement remains an
explicit conflict or corruption condition. A cache must never make an external
ref rewrite appear valid.

## Local Mutation Flow

An ordinary exact-task mutation follows this sequence:

1. Resolve a full task ID directly. Do not enumerate or reconstruct all tasks.
2. Read the projected task and its validated head from SQLite.
3. Inspect the exact Git ref once, including its object ID and symbolic-ref
   status.
4. If the ref differs from the projected head, validate the unseen Git state
   and refresh the affected projection before planning the mutation.
5. Validate the requested transition and construct `operation.json` and the
   complete resulting `state.json` in memory.
6. Write the two blobs, tree, and commit.
7. Advance the exact task ref with compare-and-swap against the observed parent
   head.
8. Upsert the resulting validated snapshot into SQLite.

Creation uses SQLite to calculate the next rank after refreshing the global ref
head set. Prefix resolution must likewise refresh the head set so an unseen ref
cannot create hidden ambiguity. Cross-task operations may query all necessary
projected rows, but read Git objects only for unknown or changed heads.

Repository discovery, common-directory identity, configuration, actor metadata,
and other process-stable information are established once when opening a
service. Internal store calls do not repeatedly run repository discovery.

The first target implementation continues to use installed Git plumbing. A
warm exact-task mutation should require roughly one exact-ref inspection plus
the existing object, tree, commit, and CAS writes, rather than hundreds of Git
processes. Cache refreshes involving multiple changed heads use batched Git
object reads instead of separate `cat-file`, `show`, and `ls-tree` processes
per task.

## Projection Consistency and Failure Semantics

Git remains the success boundary.

Projection updates use short per-task transactions and may run concurrently for
different tasks. A projected snapshot advances only when the cached head equals
the expected parent or already equals the new head. An older completed request
must not overwrite a newer projected snapshot.

The projection implementation should permit concurrent reads and independent
per-task updates instead of serializing all traffic through one process-wide
mutex. Full cache rebuild and replacement retain a separate exclusive path.

If Git advances successfully but the SQLite upsert fails:

- the mutation remains successful;
- Workbook invalidates the affected cache entry;
- human CLI output reports a nonfatal cache warning on standard error;
- JSON and HTTP results expose an optional structured warning; and
- a later read refreshes the task from Git or reports the existing actionable
  rebuild error.

Workbook must not report the overall mutation as failed after Git durability,
because retrying an apparently failed request could duplicate user intent.

## Optimistic Web Mutations

The browser separates confirmed server state from pending user intents.

For a drag operation:

1. Capture the confirmed task and Git head.
2. Add a pending status-change intent.
3. Recompute and render the board immediately.
4. Send the durable mutation with the expected head.
5. On success, replace the confirmed task with the returned task and remove the
   pending intent.
6. On failure, remove the failed intent, recompute the optimistic board, and
   display the error.

Pending intents for one task are persisted sequentially. Independent tasks may
be persisted concurrently. Rendering confirmed state plus remaining intents
prevents an earlier failure from rolling back a later user action.

The web API includes the client-observed head in mutation requests. A stale
head returns HTTP 409. Workbook does not silently reinterpret a user's intent
against task state the client did not observe.

The HTTP response remains synchronous with Git durability. Optimistic rendering
improves perceived latency without changing the server success contract.

## Remote Synchronization

Remote synchronization retains isolated tracking refs, validation, fast-forward
rules, compare-and-swap local updates, non-force pushes, and per-task outcome
reporting.

The optimized sequence is:

1. Snapshot local task heads.
2. Fetch the Workbook ref namespace once into the isolated remote-tracking
   namespace.
3. Compare fetched heads with cached validated-history heads.
4. Validate only unseen descendants after known ancestors, batching Git object
   reads. A fresh or cache-deleted clone performs the full one-time validation.
5. Reconcile compatible fetched heads into canonical local refs using
   compare-and-swap.
6. Compare the resulting local heads with the freshly fetched remote heads.
7. Push all new or advanced task refs in one non-atomic Git push and parse its
   per-ref porcelain results.
8. Enumerate local heads once more to identify refs that advanced during the
   push.

An unchanged sync performs one fetch and no push. A changed sync performs one
fetch and at most one push. It does not run `ls-remote` or open a push
connection once per task.

The batched push remains non-atomic so one rejected ref does not prevent the
remote from accepting unrelated valid refs. A remote change after the fetch
still causes a normal non-fast-forward rejection. Workbook never force-pushes
or fetches directly over canonical local task refs.

## Concurrency

Same-task mutations remain causally ordered and protected by the task ref's
compare-and-swap. Multiple web intents from one client are queued per task.
Requests from different clients may race; at most one writer advances a given
observed head, and stale writers receive a conflict.

Independent task writes may run concurrently. Git owns object and ref locking;
Workbook does not introduce a repository-wide mutation lock. SQLite uses short
transactions and conditional head advancement so independent task projection
updates do not block one another unnecessarily.

## Benchmark and Validation Strategy

The benchmark harness creates a reproducible 500-by-20 repository and measures:

- create, update, delete, restore, move, depend, and free;
- warm web/API and cold CLI mutations;
- ten sequential same-task mutations;
- ten independent-task mutations;
- unchanged, one-changed, and many-changed projection refreshes;
- Git process counts and elapsed time;
- loose and packed ref behavior;
- operation and state object growth;
- projection rebuild time;
- incremental and first-time history validation;
- unchanged and changed remote synchronization against a local bare remote; and
- SHA-1 and SHA-256 repositories when supported.

Ordinary correctness tests do not assert wall-clock thresholds. They enforce
bounded Git-command cardinality, prove exact-ID paths do not enumerate or replay
all tasks, verify changed-head-only refresh, and cover cache failure and
concurrency behavior.

Web tests cover optimistic success, rollback, queued same-task intents,
independent-task requests, and stale expected heads. Sync tests cover one fetch,
zero-or-one pushes, incremental validation, partial batched-push outcomes,
concurrent local advancement, and a deleted validation cache.

Delivery captures the current baseline, implements the complete target path,
then runs the agreed acceptance benchmark once. The final report lists every
measurement and target as pass or miss. A miss stops the performance work and
creates a separate decision about deeper Git plumbing or eventual persistence.

## Task Decomposition and Ordering

The work is delivered in this order:

1. Rewrite `WB-01KYD75VPPVW6SGH28X1ME9CZ5` as the performance benchmark
   harness and baseline task.
2. Add a task to optimize the Git-durable local mutation path.
3. Add a task to implement optimistic web mutations.
4. Rewrite `WB-01KYFCM3QVJWA98C7D6FS4Q2H0` as the batched sync and
   incremental-validation task.
5. Add a final performance acceptance and reporting task.

Dependencies encode the same sequence where work is actually blocked. The
benchmark harness precedes both local mutation and sync work. Optimistic web
behavior follows the local mutation path. The final evaluation follows all
three implementation tasks.

The final evaluation does not contain an optimization loop. If the target path
misses a budget, its only follow-up is a new evidence-backed architectural
decision.

## Documentation

`README.md` continues to describe Git as canonical and SQLite as disposable.
After evaluation, documentation may report measured results and supported
scale. It must not present target numbers as achieved until the acceptance
report confirms them.
