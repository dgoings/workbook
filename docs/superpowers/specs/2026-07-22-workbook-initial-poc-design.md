# Workbook Initial POC Design

## Summary

The initial Workbook proof of concept is a local-only, Git-native project
tracker implemented as one Go executable. It provides repository
initialization, complete task CRUD, deterministic task selection, terminal
views, and a read-only web board. The implementation is deliberately ordered
so Workbook begins tracking its own remaining work as soon as Git-backed CRUD
is usable.

Remote synchronization, distributed claims, reconciliation, and Homebrew
packaging follow the POC. Every POC operation commit stores both the immutable
operation pack and a deterministic full-state checkpoint. This preserves
operation intent for audit and future conflict resolution while allowing the
current task to be read directly from the tip commit. Dogfooded task data does
not require migration later.

## Goals

- Build a Go CLI that can be installed into a user-selected directory on
  `PATH`.
- Initialize Workbook inside an existing Git repository with `workbook init`.
- Create, read, list, update, and tombstone tasks using immutable operation
  commits under `refs/workbook/tasks/`.
- Discover canonical task membership by enumerating the exact Workbook task-ref
  prefix, without a mutable manifest or index ref.
- Store a versioned `state.json` checkpoint beside every `operation.json` so
  current-state reads do not require replaying the task's complete history.
- Provide stable machine-readable JSON alongside human-readable CLI output.
- Begin tracking the rest of the POC in Workbook immediately after CRUD works.
- Add explicit task ordering, dependencies, and deterministic next-task
  selection after the dogfooding checkpoint.
- Render tasks as a terminal table or fixed-column ASCII board.
- Serve an embedded, read-only Kanban board with `workbook serve`.
- Add a disposable SQLite projection without making SQLite authoritative.
- Keep POC histories append-only while preserving enough checkpoint and
  provenance structure for a future explicit destructive-compaction protocol.

## Non-goals

The POC does not include:

- remote fetch, push, or ref reconciliation;
- exclusive or leased claims;
- multi-parent conflict reconciliation;
- sealing, destructive history compaction, or automatic retention policies;
- multiple independent Workbook projects inside one Git repository;
- comments, attachments, implementation links, or commit trailers;
- editable web views, authentication, or non-local hosting;
- a daemon, PID-file management, or an operating-system service;
- a Homebrew formula or published release artifacts.

## Delivery strategy

The POC uses a thin Git-native vertical slice:

1. Bootstrap the Go executable, local installer, repository configuration,
   operation and state formats, and Git storage.
2. Add create, list/show, update, and tombstone deletion with text and JSON
   output.
3. Initialize Workbook in this repository and enter every remaining delivery
   item as a Workbook task.
4. Add rank/reordering, dependencies, cycle rejection, and `workbook next`.
5. Add the disposable SQLite projection and reconstruction command.
6. Add the terminal table and board renderers.
7. Add the embedded read-only web board.
8. Complete integration coverage and align the README with implemented
   behavior.

Until step 3, the implementation plan remains the execution checklist. After
step 3, Workbook is the tracker for the remainder of the POC.

## Executable and package boundaries

Workbook is a single Go executable. The design separates four responsibilities:

- **CLI:** parses commands and flags, chooses text or JSON presentation, writes
  stdout/stderr, and maps typed failures to stable exit codes.
- **Core:** owns task types, validation, use cases, task selection, and
  interfaces for canonical storage and projections. It does not execute Git or
  SQL commands.
- **Git store:** is the only package that invokes Git plumbing. It implements a
  narrow repository interface that can be exercised against temporary Git
  repositories.
- **Projection:** reads deterministic state checkpoints, validates operation
  transitions, and can reconstruct state from reachable operation packs. The
  first projector is in memory; the later SQLite implementation is an
  interchangeable disposable cache.

The web server calls the same read-only core queries as the CLI. It does not
read Git refs or SQLite tables directly.

The POC invokes the installed `git` executable instead of depending on a Git
implementation library. It discovers repositories and object IDs through Git
commands, never by reading loose files under `.git`, and never assumes a Git
object hash length or algorithm.

The module targets Go 1.26. Runtime code uses the standard library wherever it
is a good fit; durable format and repository boundaries must not expose types
from third-party packages.

## Repository initialization

`workbook init [--key WB]` must run inside an existing Git repository. It:

1. discovers the repository and common Git directory through Git;
2. verifies that Git can read and update local refs;
3. creates the private metadata directory below the common Git directory;
4. reconciles the tracked, versioned `.workbook/config.json` with the private
   identity guard at `<git-common-dir>/workbook/project.json`, creating whichever
   artifacts are needed for a new or previously initialized repository; and
5. reports the repository, project ID, project key, and current task count.

`.workbook/config.json` remains the portable tracked configuration: clones retain
the immutable project identity through that file. The common guard is private
coordination metadata and is not a replacement for tracked configuration. Because
all linked worktrees share one common Git directory, Workbook supports exactly one
project per common Git repository in the POC. Configuration loading and repository
operations reject a tracked/common identity mismatch.

For a repository initialized before the common guard existed, the first
configuration load or repeated `init` atomically backfills the guard from the
tracked configuration. Concurrent first users converge on the identity that wins
publication and reject it if it differs from their tracked identity.

For a new repository, `init` generates an immutable ULID `projectId`. The default
human-facing project key is `WB`. A supplied key must contain two to ten uppercase
ASCII letters or digits and begin with a letter. The key does not need to be
globally unique because the project ID and Git repository establish identity.

Running `init` again with matching configuration succeeds without changing
state. A different project ID, incompatible format version, or conflicting key
is an error rather than a second project.

Initialization does not create a synthetic task, push refs, install hooks, or
write a SQLite database before one is required.

## Canonical identifiers and Git storage

Task IDs have the form `<project-key>-<ULID>`, for example
`WB-01K0M6B8A4FTT8C39MXXYTW7C2`. Commands accept a full task ID or an
unambiguous case-insensitive prefix. Ambiguous prefixes are validation errors.
Operation IDs are separate ULIDs and remain independent of task IDs and Git
object IDs. Every operation and state document also carries the repository's
immutable project ID. Stored task IDs and ref suffixes always use their
canonical uppercase encoding; case-insensitive input never creates a second
case variant.

Each task owns one ref:

```text
refs/workbook/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7C2
```

That ref points to the newest operation commit for the task. Each operation
commit points to a small tree:

```text
commit
 ├── parent commit(s)
 └── tree
      ├── operation.json
      ├── state.json
      └── attachments/    optional, post-POC
```

`operation.json` records the intent and causal operation for that CLI action.
`state.json` records the complete deterministic state after applying that
operation. A create commit has no parent. Each subsequent POC mutation has
exactly one parent: the previously observed task head.

The Git store writes blobs, trees, and commits with Git plumbing, then advances
the task ref with compare-and-swap. If the ref no longer matches the expected
head, the command returns a stale-write conflict and leaves the newly written
unreachable objects harmlessly available for normal Git cleanup. It does not
silently retry an edit against state the caller did not observe.

A commit is valid only when its state checkpoint equals the deterministic
result of applying its operation pack to its parent state. The operation pack
is the authoritative record of intent and causality; the state checkpoint is
its durable materialized result. Workbook rejects a detected mismatch rather
than choosing one file as more recent.

The stored format permits multi-parent operation histories and parentless
compaction checkpoints later, but the POC creates only an append-only root and
linear descendants.

## Task discovery and ref namespace safety

Workbook owns the complete `refs/workbook/` hierarchy. The POC creates
canonical task heads only at:

```text
refs/workbook/tasks/<task-id>
```

Future synchronization may use separate tracking refs:

```text
refs/workbook/remotes/<remote-name>/tasks/<task-id>
```

Those tracking refs are not canonical local task heads and are never included
when listing tasks.

Workbook discovers task membership with one prefix-filtered Git query:

```sh
git for-each-ref \
  --format='%(refname)%00%(objectname)' \
  refs/workbook/tasks/
```

Git handles loose and packed refs behind this interface. Workbook never reads
or writes files under `.git/refs` and never assumes that a ref is loose. A
single manifest or index ref is not canonical because it would create global
write contention and could drift from the per-task refs. SQLite remains the
disposable cross-task query index.

Each discovered entry must have exactly one task-ID path component after the
prefix, pass Git ref-name validation, point to a commit, and contain matching
versioned operation and state documents. Their project ID must equal the
repository configuration, their task ID must equal the ref suffix, and its key
prefix must equal the configured project key. Workbook reports any unexpected
or malformed entry inside the owned task namespace as corrupt durable data; it
does not silently omit that entry.

The namespace is isolation by convention, not a security boundary. Normal
branch, tag, checkout, and commit commands do not target it. Git maintenance
may pack its refs or objects without changing their values. However, any
process with repository write access can deliberately update or delete a
Workbook ref, and broad mirror or custom-refspec operations can include the
namespace.

### Local ref protection

Workbook constructs ref names only from validated identifiers and confirms the
complete name with `git check-ref-format`. It advances a task through Git's
compare-and-swap form of `update-ref`, supplying the exact previously observed
object ID. Creation supplies an empty expected old value so it fails if the ref
already exists.

Every Workbook ref update requests a reflog with `--create-reflog` and records a
descriptive reason. Custom refs do not otherwise receive reflogs under the
usual Git configuration. Reflogs provide a time-limited local recovery aid,
not canonical history or protection against a writer that can also delete the
reflog.

Once the SQLite projection exists, it records each last-seen task head. If a ref
in the same history generation moves backward or sideways from that head,
Workbook reports an external rewrite and stops instead of silently accepting
it. Compare-and-swap prevents races among cooperating Workbook processes; no
local design can prevent a different process with equivalent repository access
from intentionally bypassing Workbook.

### Future remote protection

Remote synchronization must never fetch directly over
`refs/workbook/tasks/*`. It fetches remote task refs into the isolated
`refs/workbook/remotes/<remote-name>/tasks/*` tracking namespace, validates
their objects, reconciles them with canonical local heads, and then advances
local task refs with compare-and-swap.

Pushes name one fully qualified task ref at a time and require the remote ref to
equal an explicit expected object ID. Workbook must not use broad wildcard
pushes, unconditional force, direct-fetch mappings over canonical refs, or
mirror mode. Fetch pruning may affect only the remote-tracking namespace
selected by Workbook's exact refspec; it must never prune canonical local task
refs.

Where the Git server is controllable, an optional receive hook may reject
Workbook ref deletion, non-fast-forward updates, and malformed operation
commits. Hooks improve defense in depth but are never required for client
correctness.

### Namespace scalability

A synthetic local benchmark on Git 2.50.1 created 10,000 task refs pointing to
one commit. One prefix enumeration took approximately 169 ms while the refs
were loose and 18 ms after `git pack-refs --all`; loose refs occupied
approximately 39 MiB of filesystem blocks and the packed-ref file approximately
716 KiB.

The result is directional and does not include reading 10,000 task-state blobs,
but it supports using prefix enumeration for the POC's expected thousands of
tasks. Workbook should retain a repeatable benchmark and revisit ref storage or
an additional derived accelerator before claiming support for hundreds of
thousands of task refs.

## Operation packs

Every mutation stores one JSON document with this envelope:

```json
{
  "format": "workbook.operation-pack",
  "version": 1,
  "projectId": "01K0M65GBZ8F5ZQX0VC1J8H3TP",
  "taskId": "WB-01K0M6B8A4FTT8C39MXXYTW7C2",
  "historyGeneration": "01K0M6F4TDAJ4MZ0FB3X8W6Q9K",
  "actor": {
    "id": "developer@example.com"
  },
  "logicalClock": 3,
  "wallTime": "2026-07-22T14:35:00-04:00",
  "operations": []
}
```

One CLI mutation creates one pack so all field changes requested by that
invocation are applied together. The POC uses these operation types:

- `task.create` for the complete initial task value;
- `field.set` for title, description, status, priority, and rank;
- `set.add` and `set.remove` for labels and dependencies; and
- `task.tombstone` for deletion.

The pack actor defaults to the email in the repository's Git configuration.
Actor and Git commit identity are attribution, not verified identity.
`projectId` must match the immutable repository configuration.
`historyGeneration` is a ULID established by `task.create` and copied unchanged
by every ordinary descendant. It gives future clients an explicit boundary for
detecting destructive compaction instead of inferring one from missing parents.
`logicalClock` must advance from the maximum clock in the task's causal
history. `wallTime` is display metadata and never determines conflict or
selection semantics.

Unknown pack formats, versions, destructive operation types, or malformed
values encountered during writes or full-history validation fail safely.
Workbook preserves the Git objects and does not advance any ref in response to
unreadable state.

## Durable state checkpoints

Every operation tree contains a second versioned document:

```json
{
  "format": "workbook.task-state",
  "version": 1,
  "projectId": "01K0M65GBZ8F5ZQX0VC1J8H3TP",
  "taskId": "WB-01K0M6B8A4FTT8C39MXXYTW7C2",
  "history": {
    "generation": "01K0M6F4TDAJ4MZ0FB3X8W6Q9K",
    "compactedFrom": null
  },
  "logicalClock": 3,
  "task": {
    "title": "Build Git storage",
    "description": "Persist operation and state objects.",
    "status": "in-progress",
    "priority": "high",
    "labels": ["git", "poc"],
    "rank": "1/1",
    "dependencies": [],
    "createdAt": "2026-07-22T14:00:00-04:00",
    "updatedAt": "2026-07-22T14:35:00-04:00",
    "deleted": false
  }
}
```

Workbook serializes state deterministically: UTF-8 JSON, stable struct field
order, no insignificant whitespace, one trailing line feed, canonical rational
ranks, and lexicographically sorted set values. This produces stable object
IDs for identical states and gives Git's packer similar blobs to delta-compress.

For a root commit, `state.json` must equal the result of its `task.create`
operation. For a linear commit, it must equal
`Apply(parent.state, operation.json)`. A future multi-parent commit must define
and validate an equivalent deterministic merge rule before Workbook accepts
that commit shape. Ordinary operations must carry the same history generation
as their parent state.

The state document does not contain the Git commit ID because that would create
a content-addressing cycle. Workbook adds the ref's current commit ID as the
projected task's `head` at read time.

Locally created commits are validated before the task ref advances. Normal
reads may load and schema-validate the tip checkpoint directly. Full history
verification replays operation packs and compares each computed state with its
checkpoint; the POC exercises this path in tests, while a user-facing
repository-verification command remains post-POC.

### Storage implications

The hybrid format creates four Git objects per update without attachments: one
commit, one tree, one operation blob, and one state blob. The advancing logical
clock normally gives each checkpoint a distinct object ID, while similar
snapshots can be delta-compressed when Git packs the repository.

A synthetic local benchmark on Git 2.50.1 created 2,000 linear updates to a
mostly stable 2 KiB task and ran `git gc --prune=now`:

| Format | Packed objects | Packed size |
| --- | ---: | ---: |
| Operation only | 6,000 | 646 KiB |
| State only | 6,000 | 618 KiB |
| Operation and state | 8,000 | 811 KiB |

The result is directional rather than a capacity guarantee, but it supports
the design assumption that object count and history traversal deserve at least
as much attention as snapshot bytes. Attachments, large descriptions, signing,
repository packing policy, and task churn can materially change the result.
Workbook should retain a repeatable benchmark before making production-scale
storage claims.

### Decision rationale

An operation-only tree minimizes objects but makes a cold current-state read
depend on history replay. A state-only tree gives direct reads with the same
three-object shape, but it loses explicit operation identity, atomic user
intent, and the inputs needed for domain-aware reconciliation. Periodic
checkpoints reduce snapshot-object count but add checkpoint-discovery and
bounded-replay logic to every uncached read.

The POC therefore stores operation and state on every commit. It accepts one
additional blob per update in exchange for the simplest direct-read path while
retaining an auditable operation DAG. SQLite remains valuable for cross-task
queries, indexing, and filtering; `state.json` improves per-task reads and cache
reconstruction rather than replacing SQLite.

## Projected task model

A POC task projects to:

```text
id
projectId
title
description
status
priority
labels
rank
dependencies
createdAt
updatedAt
deleted
historyGeneration
head
```

Title is required and nonblank. Description is a Markdown string. Status is
one of `backlog`, `ready`, `in-progress`, `blocked`, or `done`. Priority is one
of `low`, `medium`, or `high`. Labels and dependencies are sets. New tasks
default to `backlog`, `medium`, no labels, and no dependencies.

`projectId` must match the repository configuration and is immutable.
`createdAt` and `updatedAt` are display projections from operation wall times;
they do not participate in semantic ordering. `head` is the Git object ID from
which the projection was built and is not serialized inside `state.json`.
`historyGeneration` comes from the state document's history metadata.

Tombstoned tasks retain their full projected history. They are omitted from
normal lists and boards, included by `list --all`, and available through direct
`show`. Tombstoned tasks cannot be updated in the POC.

## CLI surface

The core CRUD surface is flat:

```text
workbook init [--key WB]
workbook create "Title" [field flags] [--json]
workbook list [filter flags] [--all] [--json]
workbook show <id> [--json]
workbook update <id> [field flags] [--json]
workbook delete <id> [--json]
```

Create and update accept explicit flags for description, status, priority, and
repeated labels. List supports status, priority, and label filters. A successful
mutation prints the resulting task. A successful delete prints the tombstoned
task, so scripts can record exactly what changed.

After the dogfooding checkpoint, the CLI adds:

```text
workbook move <id> (--before <id> | --after <id>) [--json]
workbook depend <id> --on <id> [--json]
workbook undepend <id> --on <id> [--json]
workbook next [--json]
workbook rebuild [--json]
workbook board [--json]
workbook serve [--addr 127.0.0.1:7331]
```

`--json` produces a versioned JSON success envelope on stdout. Failures use a
versioned JSON error envelope when JSON was requested, remain on stderr for
human output, and map to stable categories and exit codes: invalid invocation,
not initialized, not found, validation failure, stale-write conflict, and
corrupt or unsupported stored data.

## Ordering, dependencies, and next-task selection

Rank is an exact rational number serialized as a canonical string. A new task
is appended after the last task in its status and priority bucket. Moving a
task computes a rational value between its new neighbors, so the command
updates only the moved task's ref and does not require cross-ref atomicity or
rank renumbering. A before/after target must have the same status and priority;
the caller updates those fields separately when moving between buckets.

Dependencies are directed edges stored on the dependent task. Adding a
dependency rejects missing tasks, tombstoned tasks, self-dependency, and any
edge that would create a cycle. Removing a missing edge is idempotent.

`workbook next` considers only nondeleted `ready` tasks whose dependencies are
all `done`. It sorts candidates by:

1. priority: `high`, then `medium`, then `low`;
2. rank: lowest first; and
3. full task ID as a deterministic tie-breaker.

Terminal and web boards use the same ordering within each status column. When
no task is eligible, `next` succeeds with an empty result rather than treating
an empty queue as an operational failure.

## Projection and SQLite cache

The initial in-memory projector enumerates `refs/workbook/tasks/` and reads each
tip commit's `state.json`, so ordinary current-state queries do not walk task
history. It schema-validates the checkpoint and tip operation pack, verifies
that their project ID, task ID, history generation, and logical clock agree,
and adds the tip Git object ID as `head`.

The later SQLite projection lives at
`<git-common-dir>/workbook/cache.sqlite`. It stores normalized projected tasks,
labels, dependencies, and the Git head used for each task projection. On read,
Workbook compares current refs with cached heads and rebuilds changed tasks.
The core receives the same task values whether the in-memory or SQLite
projector is active.

`workbook rebuild` deletes no canonical data. It constructs a replacement
database from the state checkpoint at each Workbook ref and atomically replaces
the prior cache only after successful projection. Direct SQL writes are
unsupported and never flow back to Git.

A separate full-history validation path traverses commits in causal order,
applies their operations, and checks every stored state checkpoint. This path
detects hand-authored, corrupted, or unsupported transitions without putting
full replay on the normal read path.

## Future destructive compaction

The POC remains strictly append-only. A `done` task is not sealed and may be
reopened, so status alone must never trigger retention or deletion behavior.
Adding `state.json` does not make old commits unreachable: every ordinary state
checkpoint still has the previous task head as a parent.

The durable format leaves room for a future explicit compaction protocol:

1. A task enters a distinct sealed or archived lifecycle under an explicit
   repository retention policy.
2. Workbook optionally exports the old task ref to an archival bundle.
3. Workbook creates a new parentless checkpoint commit containing the current
   `state.json` and a versioned `history.checkpoint` operation. The checkpoint
   operation carries the complete task value so it can independently project
   the state after the discarded operations are gone.
4. The checkpoint establishes a new history-generation ULID and records the
   prior tip object ID as `compactedFrom`. That object ID remains a
   content-addressed provenance pointer to the discarded history even after its
   objects are unavailable.
5. A generation-aware remote compare-and-swap replaces the old task ref with
   the checkpoint. Clients based on an older generation must rebootstrap and
   must not merge or republish the discarded ancestry.
6. Old objects become removable only after no refs or reflogs retain them and
   each object store's retention and garbage-collection policy permits it.

This is intentionally a non-fast-forward, destructive protocol rather than an
ordinary operation. It requires explicit user intent, remote coordination,
generation-aware clients, recovery/export behavior, and tests against stale
clones before it can be implemented. Git hosts may retain unreachable objects
for an unspecified period, so compaction cannot promise immediate physical
space recovery.

## Terminal views

Human `workbook list` output is a compact table showing task ID, title, status,
priority, and labels. `workbook board` groups tasks into the five fixed status
columns. Wide terminals receive a horizontal board; narrow or noninteractive
terminals receive vertically stacked status sections. Long titles are
truncated only in human output.

Both renderers consume projected tasks from core queries. List output sorts by
the fixed status sequence, then uses the same priority, rank, and ID ordering
that the board uses within each status column. JSON output bypasses terminal
formatting and never truncates data.

## Read-only web board

`workbook serve` starts Go's HTTP server and embeds all HTML, CSS, and
JavaScript in the executable. The POC requires no Node runtime or separate
static asset installation. It exposes only:

```text
GET /             Kanban board
GET /api/tasks    Current projected tasks
GET /healthz      Process health
```

The page polls the task endpoint periodically and renders the five fixed
columns with the same ordering used by the CLI. No mutation endpoint exists.
The server binds to `127.0.0.1:7331` by default, logs to stderr, and handles
interrupt-driven graceful shutdown. It remains a foreground process and can be
backgrounded using normal shell process control such as `workbook serve &`.

## Local installation

The repository contains a small installation helper that builds the Go
executable and places it in a caller-selected directory, defaulting to a
`~/.local/bin` directory. The helper checks that Go and Git are available and
prints the PATH change required when the destination is not already
discoverable.

Go is not currently installed in the development environment, so installing a
supported Go toolchain is an explicit prerequisite in the implementation plan.
Homebrew distribution is deferred, but the single-executable layout and
embedded web assets keep that future packaging path straightforward.

## Validation and failure behavior

Core validation rejects blank titles, unknown statuses or priorities, malformed
IDs and ranks, invalid project keys, project-ID mismatches, dependency cycles,
mutation of tombstoned tasks, malformed owned refs, and reads of unsupported
durable formats. Git command failures retain their diagnostic cause but are
mapped to stable Workbook error categories.

Commands write task refs only after all requested operations validate. A
failed command cannot partially update a task. Workbook never pushes code or
Workbook refs, installs hooks, or changes the current branch during the POC.

## Testing strategy

Unit tests cover operation validation, deterministic state serialization,
operation-to-state transition validation, history-generation mismatch,
project-ID mismatch, tombstones, rank generation and comparison, dependency
cycles, and next-task selection.

Git integration tests use temporary repositories for root and linear task
histories, direct tip-state reads, full replay equivalence, mismatch rejection,
loose and packed task-ref discovery, malformed and foreign-project ref
rejection, reflog creation, compare-and-swap success and rejection, external
rewrite detection after projection, unreachable objects after rejected
updates, cache deletion and reconstruction, and both SHA-1 and SHA-256
repositories when supported by the installed Git.

CLI tests cover every command's human and JSON modes, filters, unambiguous ID
prefixes, stable error payloads, and exit codes. Golden tests cover wide and
narrow terminal rendering. HTTP tests cover all three GET routes, task refresh,
graceful shutdown, and rejection of mutation methods and paths.

Every implementation task follows a failing-test, minimal-implementation,
passing-test cycle and ends with a focused commit. The repository-wide
verification gate is formatting, `go vet ./...`, and `go test ./...`.

## POC acceptance criteria

The POC is complete when:

- the local installer places a runnable `workbook` executable in a selected
  PATH directory;
- a fresh temporary Git repository can initialize and complete the full task
  create, list/show, update, and tombstone lifecycle;
- initialization creates one immutable project ID and rejects a conflicting
  second Workbook project in the repository;
- task discovery returns the same validated tasks with loose or packed refs and
  rejects malformed or foreign-project entries in the owned namespace;
- every accepted task commit contains matching versioned operation and state
  documents;
- Workbook's remaining POC work is represented and selected as Workbook tasks;
- deleting the SQLite cache and rebuilding it produces identical task JSON;
- direct tip-state reads and complete operation replay produce identical task
  state;
- local compare-and-swap prevents silent concurrent overwrites;
- Workbook-created task refs have recovery reflogs;
- ASCII list and board output and the read-only browser board present the same
  tasks with matching priority, rank, and ID order inside each status; and
- README examples clearly distinguish implemented local behavior from proposed
  remote synchronization and Homebrew distribution.
