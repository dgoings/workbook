# SQLite Projection Design

## Goal

Make Workbook's ordinary task queries fast and scalable without changing Git's
role as the canonical, append-only task store. SQLite is the local query source;
Git task-ref tips are the authoritative, lightweight cache-validation signal.

## Scope

This POC adds a disposable SQLite database at:

```text
<git-common-dir>/workbook/cache.sqlite
```

The database is shared by linked worktrees for the same repository, alongside
the existing private project guard. It is not tracked, synchronized, or edited
directly by users. Deleting it is safe.

Normal read paths (`list`, `show`, `board`, and `next`) validate the cache
before querying it. A new `workbook rebuild` command discards any existing
projection and atomically recreates it from all local task refs. The POC does
not add SQLite-backed full-text search, comments, implementation links, or
general SQL access.

## Read and Refresh Flow

For every ordinary read, Workbook first enumerates
`refs/workbook/tasks/*` with Git and obtains each ref's current object ID. It
does not read task objects during this check.

The cache keeps one projected row per task containing its task ID, Git tip
object ID, and the complete current checkpoint fields needed to reconstruct a
`core.Task`, plus normalized dependency and label rows. The cache also records
its schema format and the validated project ID.

If the cache's project ID and schema format match and its task-ID/tip-ID set
matches Git's enumeration, the command reads only from SQLite after the
validation step. If a task ref is new or its tip ID changed, Workbook reads and
validates that task's `state.json` using the existing Git store, replaces that
task's projected rows in one SQLite transaction, and then queries SQLite. If a
cached task no longer has a corresponding local task ref, Workbook removes its
projected rows in the same transaction.

This is SQLite-first querying, not Git-first task reconstruction: Git supplies
only ref-tip invalidation. The normal unchanged-cache path neither traverses
task histories nor reads task objects.

The first read in an initialized repository creates the projection when absent.
An invalid, unreadable, or project-mismatched cache is rebuilt rather than
trusted. A corrupt task tip remains a Git data error; Workbook must not serve a
previous cached version as if it were current.

## Atomic Rebuild and Concurrency

`workbook rebuild` enumerates and validates the current local task-ref set,
then writes a complete replacement database to a temporary file in
`<git-common-dir>/workbook`. It commits the database, closes it, and atomically
renames it over `cache.sqlite`. A failed rebuild leaves the prior cache intact.

Before replacing the cache, Workbook enumerates task refs again. If the set of
task IDs or tip IDs changed during construction, it abandons the temporary
database and retries once from the new set. If refs change again, the command
returns an operational error; a later invocation is safe and deterministic.

Incremental refreshes use SQLite transactions. Concurrent processes may each
build an equivalent projection; readers never consume an uncommitted database.
The cache is an optimization only, so a lock-contention or replacement race may
cause a command to retry or rebuild, never change canonical Git data.

## Core Boundaries

Introduce a narrow projection store behind the existing task-store boundary:

- `gitstore` continues to enumerate refs and validate/read a changed tip.
- A new SQLite projection package owns schema creation, transactions,
  cache metadata, upsert/delete of projected snapshots, and task queries.
- The command composition layer coordinates ref-tip validation with the
  projection store before constructing the core service used for read commands.
- Write commands continue to write only immutable Git operations. Their next
  read observes the changed tip and refreshes the corresponding projection.

The projection stores fields already represented by `core.Task`; it does not
invent durable task state. It may deserialize task rows back into snapshots or
provide a read-only `TaskStore` implementation, but its API must preserve the
core service's existing validation, filtering, ordering, dependency, and
next-task semantics.

Use a pure-Go SQLite driver so `./scripts/install.sh` remains a source build
without a C compiler or CGO requirement.

## Command and Error Behavior

Add:

```text
workbook rebuild [--json]
```

The command succeeds with a versioned result envelope containing the number of
active task refs projected. Human output reports the same count and cache path.
It requires an initialized Workbook repository, reads only local task refs, and
does not fetch, push, or mutate any Git ref.

Read commands retain their existing output and JSON contracts. Cache failures
are operational errors with an actionable suggestion to run `workbook rebuild`.
No command exposes arbitrary SQL or accepts a cache path override in this POC.

## Validation

Tests cover:

1. First-read creation and exact task results from SQLite-backed reads.
2. Unchanged tips: queries use the cache without reading task objects.
3. New, advanced, and removed task refs: only affected projected rows change.
4. Cache deletion, malformed databases, mismatched project IDs, and schema
   versions: safe automatic reconstruction.
5. `workbook rebuild`: temporary construction, atomic replacement, and
   unchanged prior cache after an injected rebuild failure.
6. Ref changes during rebuild: one retry, then a clear operational failure if
   the ref set changes again.
7. Existing list filters, prefix resolution, terminal/web board behavior, and
   `next` selection retain their current results through the projection.
8. Documentation and help describe `rebuild`, cache location, and the fact that
   SQLite is disposable and Git remains canonical.

## Out of Scope

- Replaying historical operation DAGs into SQLite.
- Reconciliation of divergent task histories.
- SQLite synchronization, backup, or sharing between clones.
- Full-text search, comments, implementation links, or public SQL APIs.
- Required background services, hooks, or file watchers.
