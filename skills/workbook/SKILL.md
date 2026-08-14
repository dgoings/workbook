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
2. Otherwise run `workbook next --claim --json`, which picks the next eligible
   task and assigns it to you in one command; keep its full `data.id`, then run
   `workbook show <full-id> --json`. A `null` `data` means there was nothing to
   claim — read the warnings, which say whether the eligible work is all held by
   somebody else or there is simply none.
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

## Statuses belong to the project

A project's statuses are its own configuration, not a fixed list. Workbook gives
a new project `backlog`, `ready`, `in-progress`, `in-review`, and `done`, and any
project may rename, reorder, add, or remove them. Older projects define
`blocked` as well.

Read this project's before naming one. The Statuses table in
`.workbook/guidelines.md` is a generated rendering of them, and `workbook status
list --json` answers the same question from the CLI. Pass the machine value —
the lowercase token — never the display label. Naming a status this project does
not define is refused with exit code 5, so check rather than guess.

Three tags carry the machine meaning wherever the names differ: a task created
without `--status` lands in the status tagged `default`, `workbook next` returns
tasks from a status tagged `next`, and a dependency is satisfied once it reaches
a status tagged `done`. The status names used below are Workbook's defaults;
where this project uses different ones, use the status it has for that step.

## Follow the lifecycle

Before implementation, move the task into the status this project uses for work
under way, and put your name on it in the same command:

```sh
workbook update <full-id> --status in-progress --assign self --json
```

Both changes are recorded as one, so the task never ends up started by nobody or
assigned but not started. A task claimed through `workbook next --claim` is
already assigned; `--assign self` again records nothing and is not an error.

Check the result before editing. Resume a task already in that status without
inventing an operation. Do not silently reopen a deleted task, one with unmet
prerequisites, or one already in a `done`-tagged status. Leave a task in the project's review status
when only checking acceptance or merge; return it to the in-progress status
before making requested implementation changes.

Follow the repository's instructions for planning, worktrees, implementation,
tests, commits, branches, pull requests, and merge verification.

| Milestone | Required Workbook action |
| --- | --- |
| Pull request is verified and ready for human review | Move it to the project's review status: `workbook update <full-id> --status in-review --json` |
| Review requires implementation changes | Move it back to the in-progress status before editing; return it to the review status when ready |
| Work is accepted and the pull request is merged | Move it to a `done`-tagged status: `workbook update <full-id> --status done --json` |
| You stop without finishing | Give the task back: `workbook update <full-id> --unassign self --json` |

Approval, passing CI, opening a pull request, or finishing locally is not a
merge. If acceptance and merge cannot both be verified, leave the task in the
review status and report what remains.

## Assignments say who is responsible

An assignment names `self`, an email address, or either followed by `/label` for
one agent of that identity — `--assign self/impl-1`. It blocks nothing and never
expires, and only the identity it names or whoever recorded it may remove it, so
never try to take somebody else's assignment away; Workbook refuses it with exit
code 5.

`workbook next` skips tasks another identity is assigned to and you are not, so
two agents are not handed the same work. A task you already hold is still
offered to you, and claiming it again records nothing.

Two outcomes matter when taking a specific task up:

- **Exit code 10** from `workbook update <full-id> --assign self` means somebody
  else holds that task and you do not. Nothing was recorded and you hold
  nothing. Ask for another task, or — only when the human asked for deliberate
  pairing — pass `--force` to work on it alongside them.
- An **`assignment-shared` warning** in the result envelope means you hold the
  task together with somebody else, because you forced it or because two claims
  raced and both were kept. Your work was recorded. Say so in your report and
  let the humans decide; do not remove the other assignment.

`workbook next --claim` never exits 10: it has already skipped what somebody
else holds.

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

- Use this project's own machine values, read from `.workbook/guidelines.md` or
  `workbook status list`, not the display labels and not the six Workbook ships
  with.
- Keep using the resolved full task ID after selection.
- Lead with task titles, not raw IDs, when reporting to a human, including when
  reporting a blocker or a failure.
- Resolve a dependency ID with `workbook show` before naming the blocker.
- Check every JSON command result; do not assume a mutation succeeded.
- Do not claim that Workbook creates branches, pull requests, or merges code.
