---
name: workbook
description: Use when a repository tracks work with the Workbook CLI and the user invokes /workbook, $workbook, supplies a Workbook task ID, or asks an agent to take the next Workbook task.
---

# Working a Workbook Task

Use the `workbook` CLI as the task-state boundary. Never edit Workbook Git refs,
SQLite projections, or configuration directly.

## Select and read the task

1. If the invocation supplies a task ID or prefix, run
   `workbook show <supplied-id-or-prefix> --json`, then keep the canonical full
   ID from `data.id`.
2. Otherwise run `workbook next --json`, keep its full `data.id`, then run
   `workbook show <full-id> --json`.
3. Stop and report the error when Workbook is unavailable or uninitialized,
   selection or reading fails, or no task is eligible. Do not guess.
4. Use the resolved full ID for every later Workbook command.
5. Read the full title, description, status, dependencies, labels, and
   acceptance context before editing files.
6. `data.dependencies` carries bare IDs. Before saying anything about a
   dependency or a blocker, run `workbook show <dependency-id> --json` for each
   entry and keep its `data.title`; never invent one. Exit code 4 there is not
   the step 3 stop: this clone cannot resolve that one dependency, so name it
   by ID, say it is unresolved, and keep working rather than stopping.

## IDs are for commands, titles are for humans

The full task ID is the machine interface; titles are for humans. Keep using
the resolved full ID for every Workbook CLI invocation, but build prose —
progress reports, completion summaries, questions, and error reports — around
the task title.

- Announce a selected task by title: `Taking "Add remote claim and lease
  workflow".`, not `Taking WB-01KYD730XZ9S88N1GGGSSG2CJ5.`
- Report lifecycle transitions the same way: `"Add remote claim and lease
  workflow" is ready for review.`
- Bad news is not an exception. A blocked task, a failed command, or a task you
  will not start is still announced by title: `"Add remote claim and lease
  workflow" is blocked by "Define the lease renewal protocol".`
- Describe dependencies and blockers by the titles of the tasks involved,
  resolved through step 6 above.
- Mention an ID only when it adds something: disambiguating similarly titled
  tasks, or giving a human a command to run themselves.

## Follow the lifecycle

Before implementation, run:

```sh
workbook update <full-id> --status in-progress --json
```

Check the result before editing. Resume an already `in-progress` task without
inventing an operation. Do not silently reopen a blocked, deleted, or `done`
task. Leave an `in-review` task there when only checking acceptance or merge;
return it to `in-progress` before making requested implementation changes.

Follow the repository's instructions for planning, worktrees, implementation,
tests, commits, branches, pull requests, and merge verification.

| Milestone | Required Workbook action |
| --- | --- |
| Pull request is verified and ready for human review | `workbook update <full-id> --status in-review --json` |
| Review requires implementation changes | Move to `in-progress` before editing; return to `in-review` when ready |
| Work is accepted and the pull request is merged | `workbook update <full-id> --status done --json` |

Approval, passing CI, opening a pull request, or finishing locally is not a
merge. If acceptance and merge cannot both be verified, leave the task
`in-review` and report what remains.

## Publication is automatic

Task commands publish their own work. `create`, `update`, and the other
mutations fetch shared task refs, apply the change to the refreshed tip, and
push the single ref they changed; `next` fetches before answering. No extra
command is needed.

Pass `--no-sync` when a change must stay local. Record a project-wide policy
with `workbook config set auto-sync <true|false>`, never by editing
`.workbook/config.json`. Read the `sync` member of the
result envelope to confirm what happened: `status` is `completed`, `skipped`, or
`failed`. A `failed` status still means the change was recorded locally, and the
command exits 0. Exit code 6 means the task diverged from `origin` and was not
published; report that rather than retrying the mutation.

Run `workbook fetch`, `workbook sync`, or `workbook push` only when explicitly
asked, or to reconcile after exit code 6.

## Common mistakes

- Use canonical `in-progress` and `in-review`, not display labels.
- Keep using the resolved full task ID after selection.
- Lead with task titles, not raw IDs, when reporting to a human, including when
  reporting a blocker or a failure.
- Resolve a dependency ID with `workbook show` before naming the blocker.
- Check every JSON command result; do not assume a mutation succeeded.
- Do not claim that Workbook creates branches, pull requests, or merges code.
