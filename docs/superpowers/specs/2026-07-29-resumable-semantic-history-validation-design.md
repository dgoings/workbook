# Resumable semantic history validation design

Date: 2026-07-29

Task: `WB-01KYNYT24G8R9V8N1SY3M15670`

## Summary

Workbook will add:

```text
workbook validate [--full] [--json]
```

as the explicit foreground audit of immutable task histories and their stored
`state.json` checkpoints. Ordinary reads and synchronization continue to trust
validated current tips and never wait for this audit.

The implementation has four layers:

1. `gitstore` performs bounded ref, graph, and object reads without exposing Git
   plumbing to higher layers.
2. `core.ValidateCheckpoint` remains the pure semantic oracle for applying an
   operation pack to its parent state.
3. a new `historyvalidation` package owns resumable orchestration and a
   disposable SQLite cache at
   `<git-common-dir>/workbook/validation.sqlite`;
4. the CLI and performance harness expose and verify the feature.

The command continues across task-local failures, records each task result in a
short transaction, reports every failing task, and exits nonzero when any task
is invalid. An unchanged invalid head is a cache hit and remains a nonzero
result without rereading its history.

## Goals

- Exhaustively validate every reachable operation pack and stored state
  checkpoint on demand.
- Validate only unseen descendants after the last valid cached ancestor during
  a normal run.
- Make completed task results survive interruption so a later invocation can
  resume.
- Let `--full` bypass all cached task results and semantic boundaries.
- Persist validator-versioned immutable commit IDs and task validation status
  in disposable SQLite.
- Report task and commit work, cache hits, valid/invalid/pending counts, and
  exact failures.
- Keep Git process counts constant with respect to task count and history depth.
- Never mutate canonical or tracking refs.

## Non-goals

- `sync --validate-history`
- a daemon, background worker, detached process, or implicit audit
- reconciliation of multi-parent operation histories
- making SQLite authoritative
- changing ordinary projection queries to replay history
- repairing invalid histories or rewriting refs
- remote synchronization inside `workbook validate`

## Approaches considered

### Chosen: dedicated validation cache plus bounded history reads

Keep the current task projection focused on tip-state reads. Store audit
progress in a separate disposable database and expose history traversal through
new `gitstore` APIs. This gives validation its own schema/version lifecycle,
keeps projection rebuilds independent, and avoids making sync or ordinary reads
open an audit database.

### Rejected: add validation tables to `cache.sqlite`

This makes current-state projection refresh, validation progress, and cache
replacement share one schema and lock lifecycle. Projection rebuild would
either discard audit progress unexpectedly or require special preservation
logic. It also makes a future history query depend on the current-task
projection implementation.

### Rejected: store audit state in Git

Git notes, refs, or operation commits would turn disposable derived data into
shared durable state. Validator-format upgrades and interrupted runs would
create synchronization and cleanup concerns without improving the authority of
the underlying task history.

## Command contract

### Invocation

```text
workbook validate [--full] [--json]
```

- `--full` ignores cached task results and cached semantic boundaries for this
  run. Successful and failed results from the full run replace the corresponding
  task result after that task completes.
- `--json` uses the normal result envelope with command `validate`.
- Additional positional arguments and unknown flags are invocation errors.
- The command runs in the foreground and honors context cancellation.

### Result

The result data has this stable shape:

```json
{
  "validatorVersion": 1,
  "full": false,
  "taskCount": 500,
  "tasksChecked": 5,
  "commitsChecked": 5,
  "cacheHits": 495,
  "valid": 500,
  "invalid": 0,
  "pending": 0,
  "cachePath": "/repo/.git/workbook/validation.sqlite",
  "failures": []
}
```

Definitions:

- `taskCount`: canonical task refs observed at the start of the audit.
- `tasksChecked`: tasks whose history result was not reused unchanged.
- `commitsChecked`: commits whose operation and checkpoint were semantically
  checked during this invocation. A cached boundary state is not checked again.
- `cacheHits`: unchanged task-head results reused from the current validator
  version. It counts tasks, not commits.
- `valid`, `invalid`, and `pending`: final task-status counts in the observed
  head inventory.
- `cachePath`: the shared, disposable validation database.
- `failures`: every invalid task result, sorted by task ID.

Each failure contains:

```json
{
  "taskId": "WB-...",
  "commit": "<full object ID>",
  "category": "corrupt-data",
  "message": "stored checkpoint differs from computed state"
}
```

The command writes the result even when invalid tasks make it return a
`corrupt-data` error. Human output prints the aggregate counts followed by one
line per failure with the task, full commit ID, category, and message.

An unchanged cached invalid task contributes one cache hit, one invalid task,
and its cached failure. It still causes a nonzero exit.

## Validation semantics

For every task, records are evaluated root to head.

1. The Git commit must be a full, present commit object.
2. Its tree must contain exactly regular `operation.json` and `state.json`
   blobs.
3. Both documents must decode and round-trip to their canonical encoding.
4. Project ID, task ID, history generation, and logical clock identities must
   agree with the configured project, canonical task ref, and stored state.
5. A root contains exactly one `task.create` pack and has no parent.
6. An ordinary current-format commit has exactly one parent. A multi-parent
   history is reported invalid until reconciliation semantics exist.
7. `core.ValidateCheckpoint(parent, pack, state, projectKey)` must reproduce the
   stored state exactly.
8. The final validated record must be the observed canonical head.

The first invalid commit stops semantic evaluation for that task because later
checkpoints have no valid parent state. Validation continues with every other
task. Thus the result contains every task-local failure, with one exact first
failure per invalid task.

Git transport failure, malformed batch framing, or an unreadable shared graph
is run-level operational/corrupt data because later task attribution cannot be
trusted. Already committed task results remain resumable, and all uncompleted
observed tasks remain pending.

## Bounded Git history transport

`gitstore` will expose a history-read request/result API. The repository owns
all Git commands and returns per-task structural results; it does not write
SQLite or decide cache policy.

A run uses a constant sequence:

1. enumerate canonical non-symbolic task refs once;
2. validate requested current tips in one partial `cat-file --batch`;
3. enumerate reachable commit graphs for all non-cached tasks in one
   `rev-list --reverse --topo-order --parents --stdin`;
4. read every unseen commit, tree, `operation.json`, and `state.json` in one
   `cat-file --batch`.

Normal validation omits unchanged cached task heads before steps 2-4. For a
changed task with a cached valid head, the graph may enumerate ancestry IDs,
but object/document inspection stops at that cached ancestor. Only its unseen
descendants enter the final object batch.

The graph is mapped back to each requested task from its canonical head.
Histories are expected to be task-private. A commit reused by another task
fails document identity validation for that task rather than silently sharing
semantic state.

Per-record structural and document failures remain attributed to their task and
commit. Only Git command/framing failures abort the shared batch.

The command count is independent of the number of tasks and operations. Opening
the repository may use fixed metadata commands; the validation data path uses
at most four Git commands.

## SQLite cache

### Location and authority

The cache is:

```text
<git-common-dir>/workbook/validation.sqlite
```

It is shared by linked worktrees, contains no authoritative data, and may be
deleted at any time. Missing, incompatible, or corrupt cache state is rebuilt as
an empty validation cache; the next audit reconstructs it from canonical task
refs and Git objects.

The validation database is separate from
`<git-common-dir>/workbook/cache.sqlite`. Rebuilding either database does not
replace the other.

### Schema

The schema contains:

- `validation_meta(key, value)` with schema version and project ID;
- `task_validation` with the observed head, validator version, status, cached
  valid boundary, boundary generation, boundary state bytes, validated commit
  count, and optional failure fields;
- `validated_commits` keyed by validator version and full commit ID, with task
  and history-generation identity.

Task status values are exactly `pending`, `valid`, and `invalid`.

`validatorVersion` is independent of the SQLite schema version. Any semantic
rule change that could alter validity increments the validator version.

### Preparing a run

At the start of `validate`, one SQLite transaction compares the canonical head
inventory with `task_validation`:

- new tasks are inserted as pending;
- changed heads become pending while retaining a usable last-valid boundary;
- unchanged rows from another validator version become pending;
- `--full` marks every observed task pending for the run;
- rows for task refs no longer present are removed from task status, while
  immutable commit records may remain disposable cache data.

This transaction happens before semantic reads, so interruption leaves every
uncompleted changed task visibly pending. A future validation-status query will
perform the same cheap inventory refresh; sync itself never opens or waits for
the validation cache.

### Recording progress

Each task completes in one short transaction:

- valid prefix commit IDs are inserted for the current validator version;
- the task's observed head is compare-checked;
- valid completion stores the head as the last valid boundary and stores its
  canonical state bytes;
- invalid completion stores the valid prefix boundary plus the exact failing
  task/commit/category/message;
- status changes from pending to valid or invalid only after the task result is
  durable.

If the canonical ref changes while the audit runs, the stale result is not
recorded for the new head. That task remains pending and the command returns a
stale-write error after finishing other independently recordable tasks.

The per-task transaction boundary means cancellation after task N preserves
tasks 1 through N. The next normal run reuses unchanged completed tasks and
starts with the first pending or changed task.

### Cached boundaries and invalid results

A normal run may stop semantic object inspection at a cached valid head when
that commit is an ancestor of the observed head and its task/generation
identity matches. The cached canonical boundary state becomes the parent state
for the first unseen commit.

If the boundary is not reachable, the task is validated from its current root;
old immutable commit rows remain harmless cache entries.

If a cached invalid commit remains reachable after the head advances, the task
remains invalid with that immutable failure. If it is not reachable, the new
history is checked from its root. An unchanged invalid observed head is reused
without Git history reads.

`--full` never uses a cached boundary or cached invalid result, but it replaces a
task's current-version result only after the new task audit finishes.

## Concurrency and interruption

- Validation never advances, deletes, or creates Git refs.
- The head inventory is captured before the run.
- Every task cache write compare-checks the observed head stored during
  preparation.
- A ref changed during the run produces a pending task and stale-write failure,
  never a false valid result.
- SQLite uses a busy timeout and short immediate transactions.
- Context cancellation stops new semantic work. Completed task transactions
  survive; current and remaining tasks stay pending.
- Two validators may read Git concurrently, but compare-checked task writes
  prevent an older result from replacing a newer observed-head row.
- Wall-clock timestamps are not used for semantic ordering or cache validity.

## Performance scenarios

The benchmark registry adds:

| Selector | Setup outside measurement | Measured command | Target |
| --- | --- | --- | --- |
| `validate-full-history` | fresh 500-by-20 fixture | `workbook validate --full --json` | at most 10 seconds; fewer than 12 Git processes |
| `validate-cached-unchanged` | run one successful validation | `workbook validate --json` | at most 500 milliseconds; fewer than 12 Git processes |
| `validate-five-changed` | run one successful validation, then append one operation to five tasks | `workbook validate --json` | at most 1 second; fewer than 12 Git processes |

Every scenario uses an independent fixture per sample. Setup is not measured.
The harness parses the JSON result and verifies exact task/commit/cache/status
counts before accepting a sample.

Before acceptance, diagnostic tests may use smaller fixtures. Acceptance builds
the product binary once, then runs the three selected scenarios exactly once
for SHA-1 and exactly once for SHA-256 when supported, using:

- at least 500 active tasks;
- exactly 20 operations per task for the requested acceptance workload;
- one sample;
- a 60-second command timeout.

Timeouts, command failures, time misses, and process misses are recorded and are
not replaced by tuning or reruns. Correctness and the exclusive fewer-than-12
process requirement are merge gates. Under the user's preapproved decision,
recorded wall-clock misses alone do not block merge.

## Testing

### Core and Git transport

- root and linear histories validate in root-to-head order;
- the unseen sequence stops at a reachable cached boundary;
- an unrelated boundary triggers validation from the current root;
- malformed commit, tree, operation, state, identity, topology, and missing
  object errors identify the exact task and commit;
- one invalid task does not hide another valid or invalid task;
- SHA-1 and SHA-256 full object IDs work;
- command-observer tests prove constant batched Git command counts.

### Cache and resume

- fresh heads become pending before semantic work;
- valid and invalid completion records exact status and commit IDs;
- unchanged valid and invalid heads are cache hits;
- changed heads retain only a reachable valid boundary;
- validator-version changes and `--full` bypass task results;
- cancellation preserves completed tasks and leaves the rest pending;
- head races cannot record a stale valid result;
- cache deletion, schema mismatch, project mismatch, and corruption rebuild to
  an empty disposable cache;
- linked worktrees resolve the same cache path.

### CLI

- help and flag schema expose `validate`, `--full`, and `--json`;
- JSON success and failure result shapes are exact;
- human output includes aggregate counts and exact failure lines;
- multiple invalid tasks are all reported in task-ID order;
- unchanged cached invalid results still exit nonzero;
- validation never changes canonical refs.

### Performance harness

- selectors are registered and stable;
- each topology has an independent fixture;
- setup validation and five updates are excluded from measurement;
- result verification rejects wrong counts even when the command exits zero;
- targets use inclusive time and exclusive process semantics;
- ordinary diagnostic tests prove process counts do not scale with fixture
  depth.

## Documentation

`README.md` will distinguish:

- bounded current-tip validation used by ordinary reads and sync;
- exhaustive `workbook validate`;
- validator-versioned disposable cache status;
- normal versus `--full` behavior;
- failure, pending, interruption, and ref-race semantics;
- exact performance evidence and any target misses.

No implemented section will advertise `sync --validate-history`.

## Self-review

- No placeholder or deferred implementation requirement remains.
- The command, JSON fields, statuses, failure precedence, cache authority,
  resume boundary, race handling, and acceptance protocol are explicit.
- The design does not make SQLite canonical or mutate Git refs.
- The audit is foreground-only and separate from default synchronization.
- Git process count is bounded independently of history depth.
