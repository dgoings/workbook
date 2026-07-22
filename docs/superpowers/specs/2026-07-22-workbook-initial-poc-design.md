# Workbook Initial POC Design

## Summary

The initial Workbook proof of concept is a local-only, Git-native project
tracker implemented as one Go executable. It provides repository
initialization, complete task CRUD, deterministic task selection, terminal
views, and a read-only web board. The implementation is deliberately ordered
so Workbook begins tracking its own remaining work as soon as Git-backed CRUD
is usable.

Remote synchronization, distributed claims, reconciliation, and Homebrew
packaging follow the POC. The POC nevertheless writes the canonical immutable
operation history described by the repository architecture, so dogfooded task
data does not require migration later.

## Goals

- Build a Go CLI that can be installed into a user-selected directory on
  `PATH`.
- Initialize Workbook inside an existing Git repository with `workbook init`.
- Create, read, list, update, and tombstone tasks using immutable operation
  commits under `refs/workbook/tasks/`.
- Provide stable machine-readable JSON alongside human-readable CLI output.
- Begin tracking the rest of the POC in Workbook immediately after CRUD works.
- Add explicit task ordering, dependencies, and deterministic next-task
  selection after the dogfooding checkpoint.
- Render tasks as a terminal table or fixed-column ASCII board.
- Serve an embedded, read-only Kanban board with `workbook serve`.
- Add a disposable SQLite projection without making SQLite authoritative.

## Non-goals

The POC does not include:

- remote fetch, push, or ref reconciliation;
- exclusive or leased claims;
- multi-parent conflict reconciliation;
- comments, attachments, implementation links, or commit trailers;
- editable web views, authentication, or non-local hosting;
- a daemon, PID-file management, or an operating-system service;
- a Homebrew formula or published release artifacts.

## Delivery strategy

The POC uses a thin Git-native vertical slice:

1. Bootstrap the Go executable, local installer, repository configuration,
   operation format, and Git storage.
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
- **Projection:** reconstructs task state from reachable operation packs. The
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
3. writes a tracked, versioned `.workbook/config.json` containing the format
   identifier, configuration version, and project key;
4. creates the private cache directory below the common Git directory; and
5. reports the repository, project key, and current task count.

The default project key is `WB`. A supplied key must contain two to ten
uppercase ASCII letters or digits and begin with a letter. Running `init`
again with matching configuration succeeds without changing state. An
incompatible existing configuration is an error.

Initialization does not create a synthetic task, push refs, install hooks, or
write a SQLite database before one is required.

## Canonical identifiers and Git storage

Task IDs have the form `<project-key>-<ULID>`, for example
`WB-01K0M6B8A4FTT8C39MXXYTW7C2`. Commands accept a full task ID or an
unambiguous case-insensitive prefix. Ambiguous prefixes are validation errors.
Operation IDs are separate ULIDs and remain independent of task IDs and Git
object IDs.

Each task owns one ref:

```text
refs/workbook/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7C2
```

That ref points to the newest operation commit for the task. Each operation
commit points to a small tree containing only `operation.json` for that CLI
action. A create commit has no parent. Each subsequent POC mutation has exactly
one parent: the previously observed task head.

The Git store writes blobs, trees, and commits with Git plumbing, then advances
the task ref with compare-and-swap. If the ref no longer matches the expected
head, the command returns a stale-write conflict and leaves the newly written
unreachable objects harmlessly available for normal Git cleanup. It does not
silently retry an edit against state the caller did not observe.

The stored format permits multi-parent operation histories later, but the POC
does not create or reconcile them.

## Operation packs

Every mutation stores one JSON document with this envelope:

```json
{
  "format": "workbook.operation-pack",
  "version": 1,
  "taskId": "WB-01K0M6B8A4FTT8C39MXXYTW7C2",
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
`logicalClock` must advance from the maximum clock in the task's causal
history. `wallTime` is display metadata and never determines conflict or
selection semantics.

Unknown pack formats, versions, destructive operation types, or malformed
values fail projection safely. Workbook preserves the Git objects and does not
advance any ref in response to unreadable state.

## Projected task model

A POC task projects to:

```text
id
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
head
```

Title is required and nonblank. Description is a Markdown string. Status is
one of `backlog`, `ready`, `in-progress`, `blocked`, or `done`. Priority is one
of `low`, `medium`, or `high`. Labels and dependencies are sets. New tasks
default to `backlog`, `medium`, no labels, and no dependencies.

`createdAt` and `updatedAt` are display projections from operation wall times;
they do not participate in semantic ordering. `head` is the Git object ID from
which the projection was built.

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

The initial in-memory projector enumerates `refs/workbook/tasks/`, reads every
reachable operation pack, validates it, and produces deterministic task values.
For the linear POC history, application order follows Git ancestry rather than
wall-clock time.

The later SQLite projection lives at
`<git-common-dir>/workbook/cache.sqlite`. It stores normalized projected tasks,
labels, dependencies, and the Git head used for each task projection. On read,
Workbook compares current refs with cached heads and rebuilds changed tasks.
The core receives the same task values whether the in-memory or SQLite
projector is active.

`workbook rebuild` deletes no canonical data. It constructs a replacement
database from Workbook refs and atomically replaces the prior cache only after
successful projection. Direct SQL writes are unsupported and never flow back
to Git.

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
IDs and ranks, invalid project keys, dependency cycles, mutation of tombstoned
tasks, and reads of unsupported durable formats. Git command failures retain
their diagnostic cause but are mapped to stable Workbook error categories.

Commands write task refs only after all requested operations validate. A
failed command cannot partially update a task. Workbook never pushes code or
Workbook refs, installs hooks, or changes the current branch during the POC.

## Testing strategy

Unit tests cover operation validation, deterministic projection, tombstones,
rank generation and comparison, dependency cycles, and next-task selection.

Git integration tests use temporary repositories for root and linear task
histories, task-ref discovery, compare-and-swap success and rejection,
unreachable objects after rejected updates, cache deletion and reconstruction,
and both SHA-1 and SHA-256 repositories when supported by the installed Git.

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
- Workbook's remaining POC work is represented and selected as Workbook tasks;
- deleting the SQLite cache and rebuilding it produces identical task JSON;
- local compare-and-swap prevents silent concurrent overwrites;
- ASCII list and board output and the read-only browser board present the same
  tasks with matching priority, rank, and ID order inside each status; and
- README examples clearly distinguish implemented local behavior from proposed
  remote synchronization and Homebrew distribution.
