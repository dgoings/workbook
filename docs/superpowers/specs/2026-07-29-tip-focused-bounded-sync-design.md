# Tip-Focused Bounded Sync Design

## Goal

Make `workbook fetch`, `workbook push`, and `workbook sync` validate and move
Workbook task refs with a constant number of Git processes instead of replaying
every task history and opening one remote connection per task.

The default synchronization trust boundary becomes the current task tip:

- owned task refs are direct, non-symbolic refs;
- each tip points directly to a commit;
- the commit has the allowed parent topology;
- the tree contains exactly canonical `operation.json` and `state.json` blobs;
- both documents use supported formats and canonical encodings;
- configured project, task, generation, and logical-clock identities agree;
- local and fetched heads are classified by Git ancestry;
- canonical updates remain compare-and-swap operations;
- publication remains non-force and subject to the remote's
  non-fast-forward checks.

Default sync deliberately does not replay historical operations or prove that
an older checkpoint equals `Apply(parent.state, operation.json)`. The explicit
validator delivered by the following task owns that exhaustive semantic audit.

## Scope

This task changes the Git synchronization implementation, its behavioral and
process-cardinality tests, documentation, and performance evidence.

It does not:

- change the immutable operation or state formats;
- change task mutation semantics;
- add reconciliation or force-push behavior;
- touch code refs;
- remove isolated tracking refs;
- implement the exhaustive `workbook validate` command;
- tune repeatedly until a wall-clock target passes.

## Baseline Evidence

The topology harness recorded every 500-task-by-20-operation remote scenario
timing out at 60 seconds under SHA-1 and SHA-256. Each sample used roughly
4,000 Git processes. The dominant causes in the current implementation are:

- full `rev-list` plus document replay for every task;
- per-task `ls-remote`;
- per-task `git push`;
- per-task post-push ref inspection;
- per-task ancestry checks and canonical ref updates.

The task is accepted when correctness holds and the real benchmark process
counts are bounded, even if a wall-clock target still misses. The new benchmark
is recorded exactly once per supported object format and is never replaced.

## Selected Architecture

Use one shared, tip-focused synchronization planner with three internal stages:

1. inspect refs and tips in batches;
2. classify head relationships and plan safe ref changes;
3. execute a bounded transport/update plan and verify local races.

Public `Fetch` and `Push` call their corresponding planner/executor. `Sync`
calls the fetch planner first and reuses its validated canonical and tracking
snapshots to plan publication without repeating validation or remote discovery.

### Planned Internal Types

Names may vary during implementation, but responsibilities remain separate:

```go
type tipReadResult struct {
    Head     TaskHead
    Snapshot core.Snapshot
    Err      error
}

type headRelationship string

const (
    headsEqual       headRelationship = "equal"
    localAhead       headRelationship = "local-ahead"
    remoteAhead      headRelationship = "remote-ahead"
    headsDiverged    headRelationship = "diverged"
)

type headPair struct {
    TaskID string
    Local  core.Snapshot
    Remote core.Snapshot
}

type fetchState struct {
    Canonical map[string]core.Snapshot
    Tracking  map[string]core.Snapshot
    Outcomes  map[string]SyncTaskResult
}

type publishCandidate struct {
    TaskID  string
    ObjectID string
    RefName string
}
```

`fetchState` is an internal reuse boundary, not a durable cache. Every public
command starts from current Git refs.

## Batched Owned-Ref Inspection

### Ref enumeration

Canonical and tracking namespaces each use one `for-each-ref`:

```text
git for-each-ref \
  --format=%(refname)%00%(objectname)%00%(symref) \
  refs/workbook/tasks/

git for-each-ref \
  --format=%(refname)%00%(objectname)%00%(symref) \
  refs/workbook/remotes/origin/tasks/
```

The parser:

- requires one flat task ID after the expected prefix;
- validates the configured task-ID format;
- rejects duplicate records and nested entries;
- rejects symbolic refs before object access;
- validates full, opaque object IDs without assuming SHA-1 width.

Tracking refs receive the same ownership checks as canonical refs. Direct
inspection of `.git/refs` is forbidden.

### Partial tip batch

The existing `ReadTaskHeads` contract remains strict and all-or-nothing for
normal repository reads. Its implementation is refactored over a private
partial reader:

```go
func (r *Repository) readTaskHeadsPartial(
    context.Context,
    core.ProjectConfig,
    []TaskHead,
) ([]tipReadResult, error)
```

All requested commits, trees, operation blobs, and state blobs are submitted to
one `git cat-file --batch` process. The reader consumes all four response
records for one head before validating that head, so one missing or malformed
tip cannot desynchronize later results.

Per-head object absence or Workbook validation failure becomes that head's
`Err`. A malformed Git batch protocol is fatal because later attribution is no
longer trustworthy.

Tip validation retains:

- direct commit and exact tree/blob shape;
- canonical operation and state JSON;
- supported durable format versions;
- configured project and task identity;
- matching operation/state generation and logical clock;
- root-create and ordinary-parent topology.

It intentionally omits non-root checkpoint replay against the parent state.

## Batched Ancestry Classification

All unequal valid local/tracking pairs are classified with at most one:

```text
git rev-list --parents --stdin
```

The input contains every unequal local and tracking head. The parsed parent
graph is used to test reachability in both directions:

- remote reaches local: local is behind and may fast-forward;
- local reaches remote: local is ahead;
- neither reaches the other: diverged;
- equal object IDs: unchanged without invoking `rev-list`.

Object IDs are validated as full repository IDs before graph traversal.
Operation and state generation identities must be internally consistent at
each tip. A descendant that changes generation relative to its local ancestor
is not accepted as an ordinary fast-forward.

The graph walk traverses commit ancestry but does not read or apply historical
Workbook documents. This is ancestry validation, not semantic history
validation.

## Fetch

Fetch uses this bounded sequence:

1. verify repository identity and configuration;
2. run exactly one isolated fetch:

   ```text
   git fetch --no-tags --prune --no-auto-maintenance origin \
     +refs/workbook/tasks/*:refs/workbook/remotes/origin/tasks/*
   ```

   Pruning is scoped by the explicit refspec to Workbook's isolated tracking
   namespace so a remotely missing task ref does not masquerade as an
   up-to-date task. Auto-maintenance is disabled for this explicit task-ref
   transfer; normal repository maintenance remains the user's or Git's
   separate concern.

3. enumerate canonical and tracking refs once each;
4. read all canonical and tracking tips in one partial object batch;
5. classify every unequal valid pair in one ancestry graph;
6. collect missing-local creates and remote-ahead fast-forwards;
7. apply all canonical changes in one compare-and-swap transaction.

### Batched canonical CAS

Canonical updates use:

```text
git update-ref --no-deref --create-reflog \
  -m "workbook: fetch origin" --stdin
```

The stdin transaction includes `option no-deref`, exact expected old object IDs
for updates, and create-only records for missing refs. A CAS failure aborts the
transaction and returns a stale-write failure. Workbook never force-updates a
canonical ref or reports a ref as changed after an aborted transaction.

The task-ID validator and owned-ref parser establish safe ref names before the
transaction, avoiding a per-task `check-ref-format` process.

### Fetch outcomes

- missing canonical + valid tracking: `created`;
- tracking descendant of canonical: `fast-forwarded`;
- equal: `unchanged`;
- canonical descendant of tracking: `local-ahead`;
- unrelated valid tips: `diverged`;
- malformed tracking tip: `invalid`, left isolated;
- malformed canonical tip: local corruption, never overwritten.

Valid unrelated tasks may still be created or fast-forwarded when another
tracking tip is invalid. Fetch returns nonzero after recording all invalid
outcomes.

Fresh checkout state is exposed from the validated complete tip checkpoint.
No historical replay is required before `Get`, `List`, or projection refresh.

## Push

Push uses this bounded sequence:

1. enumerate canonical refs once;
2. validate all canonical tips in one partial batch;
3. enumerate all remote task heads once:

   ```text
   git ls-remote --refs origin refs/workbook/tasks/*
   ```

4. mark exact matches `up-to-date`;
5. submit all other valid candidates in one explicit-OID, non-atomic push;
6. enumerate canonical refs once after planning/publication to detect local
   advancement.

Invalid local tips are reported as `invalid` and omitted from publication while
valid independent candidates remain publishable.

### One non-atomic push

When candidates exist, Workbook runs exactly:

```text
WORKBOOK_PRE_PUSH_ACTIVE=1 git push --porcelain origin \
  <observed-oid>:refs/workbook/tasks/<task-id> ...
```

Constraints:

- no force marker or `--force`;
- no deletion refspec;
- no `--atomic`;
- no wildcard source;
- no code ref;
- one destination per validated candidate;
- explicit observed object IDs prevent an unobserved local advance from being
  published.

Non-atomic publication preserves partial success: the remote may reject a
stale/divergent task while accepting independent refs in the same connection.

### Result-preserving Git execution

The repository gains a private Git result API that preserves stdout, stderr,
and the execution error. Existing `Git` callers keep their current behavior.

This is required because `git push --porcelain` returns useful per-ref stdout
when one ref is rejected and the process exits nonzero.

### Strict porcelain parser

The parser validates one status record for every submitted destination and
rejects duplicate, missing, malformed, or unexpected task refs.

Allowed flags:

- `*` or normal fast-forward success: `published`;
- `=`: `up-to-date`;
- `!`: `rejected` with Git's summary/reason as detail.

Force and deletion forms are protocol violations because Workbook never
requested them. A nonzero command with a complete set containing rejections is
a valid partial-result transport. A transport failure without complete
accounting fails the phase rather than fabricating outcomes.

### Local race verification

After push planning, one final canonical `for-each-ref` snapshot compares every
reported local head with its observed object ID.

A successfully published or up-to-date task whose local head advanced becomes
`local-changed`; the remote contains the validated observed head and the user is
told to run push again. Rejected tasks retain `rejected` precedence.

The final snapshot is also performed when no push command is needed, preventing
an already-synchronized race from being reported as stable.

## Sync

`Sync` performs Fetch first and reuses its validated `fetchState`.

It stops before publication on:

- fetch transport or protocol failure;
- malformed tracking or canonical data;
- any divergent task;
- CAS failure.

Otherwise it constructs a push result for every current canonical task:

- canonical with no tracking ref: publish candidate;
- canonical equal to tracking: `up-to-date`;
- local-ahead canonical: publish candidate;
- canonical advanced from tracking during fetch: `up-to-date`.

When there are no candidates, Sync runs no `git push` command. The push phase is
`completed` with exact per-task `up-to-date` outcomes, not `skipped`, because no
prerequisite failed.

When candidates exist, Sync executes the same one-push/final-snapshot path
without `ls-remote`, tip revalidation, or another ancestry walk.

Tracking refs remain the fetched pre-push remote tips. A successful push does
not rewrite them; the next fetch remains authoritative.

## Process Budgets

The intended measured Git-process shapes, excluding fixture setup, are:

| Scenario | Bounded shape |
| --- | --- |
| fresh checkout | open/config discovery + one fetch + two ref reads + one tip batch + one CAS |
| initial publication | one fetch + two ref reads + one tip batch + one push + one final ref read |
| already synchronized | one fetch + two ref reads + one tip batch + one final ref read; zero push |
| small changed set | synchronized shape + one ancestry graph + one CAS + one push |
| divergent tips | one fetch + two ref reads + one tip batch + one ancestry graph; zero push |
| malformed local tip | one local ref read + one tip batch + one remote-head read + one final ref read |
| malformed remote tip | one fetch + two ref reads + one tip batch; zero push |

No command count depends on task count or operations per task. Payload size and
ancestry traversal work may scale, but Git process cardinality does not.

## Error Precedence and Compatibility

Result envelopes and existing status strings remain version 1 and additive
behavior is avoided.

Aggregate push errors retain this precedence:

1. local corrupt-data outcomes;
2. remote rejections;
3. local-changed races;
4. incomplete transport/protocol failure where per-ref outcomes cannot be
   trusted.

Fetch retains isolated invalid tracking refs and reports exact per-task
outcomes. Sync retains the current fetch-then-push envelope and skipped-push
behavior on failed/divergent fetches.

The managed pre-push hook recursion guard remains
`WORKBOOK_PRE_PUSH_ACTIVE=1`. Workbook does not bypass unrelated hooks.

## Testing Strategy

### Unit and parser tests

- generic owned-ref parser rejects symbolic, nested, duplicate, wrong-prefix,
  and malformed object-ID records;
- partial cat-file reader consumes later heads after one invalid tip and fails
  closed on malformed batch framing;
- ancestry graph classifies equal, both fast-forward directions, divergence,
  and generation mismatch in SHA-1 and SHA-256 repositories;
- update-ref transaction input uses exact refs, expected old IDs, create-only
  records, no-deref, and no force;
- porcelain parsing accepts creation, fast-forward, up-to-date, and rejection,
  and rejects force/deletion, duplicate, missing, unexpected, and malformed
  records;
- result-preserving Git execution retains stdout and stderr on nonzero exit
  without changing the public `Git` error contract.

### Behavioral integration tests

- fresh checkout exposes the correct multi-operation tip state;
- five local-ahead and five remote-ahead tasks reach exact canonical,
  tracking, and remote refs;
- common-parent divergence remains isolated and stops Sync publication;
- one malformed local tip is omitted while valid tasks publish;
- one malformed remote tip remains tracking-only while valid remote tasks may
  advance;
- a buried checkpoint mismatch is accepted by default tip-focused sync;
- non-fast-forward rejection and unrelated publication share one push;
- a canonical race triggers CAS failure without overwrite;
- a local advance during publication reports `local-changed`;
- already synchronized Sync returns completed/up-to-date with zero push;
- code HEAD and code refs are unchanged;
- SHA-1 and supported SHA-256 have identical semantic outcomes.

### Process-cardinality tests

Command observers and Trace2 evidence prove:

- one fetch;
- at most one wildcard `ls-remote`;
- zero per-task `ls-remote`;
- one object batch;
- at most one ancestry graph;
- at most one canonical update transaction;
- zero or one non-atomic push;
- one final canonical snapshot;
- the same transport/plumbing command counts for 10x4 and 25x7 fixtures.

Tests must fail under mutations that restore per-task replay, per-task remote
probes, per-task push, unconditional synchronized publication, atomic push, or
ref-name rather than explicit-OID publication.

## One-Shot Acceptance Evidence

After all correctness and process-cardinality tests pass:

1. build the measured Workbook binary once;
2. run all seven remote topology selectors once at 500 tasks by 20 operations,
   one sample, and a 60-second command timeout under SHA-1;
3. probe SHA-256 support and run the identical selector set once when
   supported;
4. write new date-stamped JSON and Markdown reports;
5. verify exact scenario/sample/environment/target/outcome completeness;
6. compare results with approved targets without replacing measurements.

A timeout or wall-clock miss is committed as observed. Correctness and bounded
process counts are sufficient for merge under the user's approved decision.

## Alternatives Considered

### Keep exhaustive replay but batch Git reads

Rejected. It still applies every historical operation during routine sync and
does not establish the intended tip-focused trust boundary.

### Push each task independently

Rejected. It preserves partial success but creates task-count-dependent remote
connections and caused the observed process explosion.

### Use one atomic push

Rejected. One stale task would block unrelated publication, violating required
partial success.

### Fetch directly into canonical refs

Rejected. Malformed or divergent remote data must remain isolated until
validated and CAS-applied.

### Add a coordination service

Rejected. Git already supplies fetch isolation, ancestry, CAS, and
non-fast-forward safety within the repository-native architecture.
