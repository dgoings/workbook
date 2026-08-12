<!-- workbook:begin generator=dev sha256=5c9b725704e3d22913671a1562bc7f0fd4468c5ac55be0bd5aee0c289efb46c3 -->
# Workbook guidelines

Workbook tracks this project's tasks in Git refs under `refs/workbook/tasks/`.
Use the `workbook` CLI as the task-state boundary. Never edit Workbook refs,
the SQLite projection, or `.workbook/config.json` directly.

## This project

| Setting | Value |
| --- | --- |
| Project ID | `01KY8964C8TQVBKVACB45DYTNY` |
| Task ID prefix | `WB-` |

## Statuses

This project's statuses, in order. Pass the machine value, never the display
label.

| # | Machine value | Display label | Tags |
| --- | --- | --- | --- |
| 1 | `backlog` | Backlog | `default` |
| 2 | `ready` | Ready | `next` |
| 3 | `in-progress` | In Progress | none |
| 4 | `in-review` | In Review | none |
| 5 | `done` | Done | `done` |

Write `--status in-progress`, not `In Progress`.
The same applies to `in-review`.
A display label is rejected as a validation error.

A tag is a job the machine gives a status, not a description of its name:

| Tag | What it makes Workbook do |
| --- | --- |
| `default` | A task created without `--status` lands here. Exactly one status carries it. |
| `done` | A dependency sitting here is satisfied, so the work waiting on it can be claimed. |
| `next` | `workbook next` may return a task sitting here. |

A status carrying no tag is an ordinary column: work rests there and nothing
else follows from it.

These statuses belong to this project and another project's are different, so
read them here or with `workbook status list --json` rather than assuming the
ones you have seen elsewhere. This section is rewritten whenever they change.

## Canonical priorities

| Machine value | Display label |
| --- | --- |
| `low` | Low |
| `medium` | Medium |
| `high` | High |

## Task lifecycle

New tasks land in `backlog`.
`workbook next` claims from `ready`.
A dependency is satisfied once it reaches `done`.

1. Select work with `workbook next --json`, or read a known task with
   `workbook show <id> --json`. Keep the canonical full ID from `data.id`.
2. Claim it with `workbook update <id> --status <status> --json` before
   editing files, naming the status this project uses for work under way.
3. Move it along the statuses above as the work progresses, including the
   one this project uses for review, and into a status tagged `done` only
   after the work is accepted and merged.

## Machine-readable output

Every command accepts `--json` except `serve`. Success is a single compact
line: `{"format":"workbook.result","version":1,"command":...,"data":...}`.
Failure uses `"format":"workbook.error"` with an `error.category` field.
Check the result of every mutation; do not assume it succeeded.

## Exit codes

| Code | Category | What to do |
| --- | --- | --- |
| 0 | success | nothing |
| 1 | `operational` | read the message; the environment or remote is at fault |
| 2 | `invalid-invocation` | fix the command line |
| 3 | `not-initialized` | run `workbook setup` |
| 4 | `not-found` | use an existing task ID |
| 5 | `validation` | change the input; it fails the same way on every retry |
| 6 | `stale-write` | retry the identical command; it will probably succeed |
| 7 | `corrupt-data` | read the message; repair or rebuild before continuing |
| 8 | `conflict` | read the envelope's `conflict` list, change the input, then retry |

## Publication is automatic

Commands that create or update a task fetch shared task refs from `origin`,
apply the change to the refreshed tip, then publish the single ref they
changed. `workbook next` fetches before answering. A repository with no
`origin` synchronizes nothing.

Disable it for one command with `--no-sync`, for this project with
`workbook config set auto-sync false`, or for every project with
`"autoSync": false` in the user configuration's `preferences` block. A project
policy outranks a user preference; `--no-sync` outranks both.
`workbook config show` reports the resolved policy and which layer decided it.
Record a project policy with that command rather than editing
`.workbook/config.json`.

The `sync` member of a result envelope reports what happened. A `failed`
status still means the change was recorded locally and the command exits 0.
Local work that `origin` does not have is replayed onto the fetched tip and
published, so a divergent task needs no separate reconciliation step.

## Conflicts

Concurrent edits to different fields are applied silently. Exactly three
situations stop a replay and exit `8`: both sides changed the description, a
replayed dependency would close a cycle, and `origin` tombstoned a task a
local operation still edits.

They are reported in the result envelope's `conflict` list, which names each
task and a `type` of `description`, `dependency-cycle`, or `tombstone`. The
task ref stops at the last operation that replayed cleanly, everything up to
that point is published, and the remaining local operations are dropped.
Resolve one by reading the reported values and running the ordinary command
again; there is no reconcile or continue command. A conflict on one task
never blocks a command that touches a different task.

A running watcher does remember conflicts between invocations, because it
meets them with nobody present and a stopped replay leaves nothing for the
next fetch to find. It reports each one to its own terminal, gates the next
mutation of that task, and forgets it once reported or once the task moves
on, so the retry behaves exactly as it does without one.

`workbook fetch`, `workbook push`, and `workbook sync` remain available for
explicit whole-project synchronization.

## Continuous synchronization

`workbook sync --watch` runs in the foreground and keeps this clone current.
While one runs, a mutation writes locally, hands publication to it, and
reports a `sync` status of `deferred` instead of fetching and pushing
itself, which is roughly 500 ms and 16 Git processes cheaper. `workbook
serve` runs the same loop, so the board reflects other clones' work.

It is an optimization and never a requirement. With no watcher running, or
one that is stale or whose last synchronization failed, commands
synchronize inline exactly as before. `deferred` is best-effort: the local
write is durable and publication follows within milliseconds, but a watcher
killed in that window leaves the work local until `workbook push` runs.
`workbook sync --status` reports whether one is running and what it last
did.

---

This file is generated by Workbook. Edits are reported as local
modifications and preserved. Refresh it with `workbook docs update`, and
check it with `workbook docs status`.
<!-- workbook:end -->
