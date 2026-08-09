# Workbook Agent Skill Design

## Goal

Add a small, portable agent skill for completing work tracked by Workbook. A
user invokes the skill as `/workbook` with an optional task ID. The skill makes
Workbook's CLI the authoritative interface for selecting, reading, and updating
the task while the active agent harness continues to own code editing, testing,
Git, and pull-request operations.

The canonical skill lives at `skills/workbook/` in this repository. The future
clone-bootstrap workflow tracked by `WB-01KYD74JCGN8JM9174DN0GQXG3` may install
or refresh that package in a harness-specific project or personal skill
directory without maintaining a second copy of the workflow.

## Package

The package contains:

- `SKILL.md`, using the portable Agent Skills format; and
- `agents/openai.yaml`, providing Codex-facing display metadata while remaining
  ignorable by other harnesses.

The workflow is concise enough to remain self-contained. It needs no wrapper
script, reference file, or duplicated CLI schema. Agents invoke `workbook`
directly and use its JSON output so the CLI remains the behavioral boundary.

## Invocation and Task Selection

When the invocation includes a task ID or unambiguous task-ID prefix, the skill
uses that value. With no explicit task, it runs:

```sh
workbook next --json
```

The agent takes the selected ID from the successful result envelope and then
loads the authoritative task context with:

```sh
workbook show <task-id> --json
```

The agent reads the title, description, status, dependencies, labels, and other
acceptance context before changing status or implementation files. It keeps the
resolved full task ID for every later command.

If there is no eligible next task, the supplied ID is invalid or ambiguous,
Workbook is unavailable or uninitialized, or either command fails, the agent
reports that result and stops rather than guessing or editing task storage
directly.

## Dependency Titles

Human-facing prose names tasks by title, including the tasks a blocked task
waits on. `data.dependencies` in a result envelope is a list of canonical task
IDs and nothing else, so the skill instructs the agent to resolve each entry
with:

```sh
workbook show <dependency-id> --json
```

and to take the blocker's name from that result's `data.title`. A dependency
has no title until it is read, and an agent that skips the resolution step
either invents one or falls back to the ULID.

The alternative of carrying dependency titles in the `show` envelope was
considered and rejected. `show` serializes the same `Task` shape that `create`,
`update`, `list`, `next`, `board`, and the web API return, so a new member
either changes every one of those envelopes or makes one command's rendering of
a task differ from the rest — a worse deal for machine consumers than one extra
read. A title is also mutable display metadata owned by the dependency's own
task ref; copying it into a second task's envelope creates a stale-able
duplicate of a value that has a canonical home, and forces a shape decision for
tombstoned or unresolvable dependencies. The extra round trip is one cheap
projection read per dependency, paid only when an agent is about to describe
one.

## Status Lifecycle

Before implementation begins, the agent records active work with:

```sh
workbook update <task-id> --status in-progress --json
```

The durable CLI value is `in-progress`, not the display label `In Progress`.
An already in-progress task may be resumed without manufacturing another
operation. An in-review task remains in review when the invocation is only
checking acceptance or merge state; it returns to `in-progress` only when
further implementation is required. A blocked, deleted, or completed task is
not silently reopened.

The agent then follows the repository's own instructions and available
harnesses for planning, implementation, verification, commits, worktrees,
branches, and pull requests. The skill does not replace those workflows or
claim that Workbook commands perform them.

When the implementation is verified and its pull request is genuinely ready
for human review, the agent runs:

```sh
workbook update <task-id> --status in-review --json
```

The durable CLI value is `in-review`, not `In Review`. If review requests
further implementation, the agent returns the task to `in-progress` before
making those changes and moves it to `in-review` again only when the revised
pull request is ready.

The agent runs:

```sh
workbook update <task-id> --status done --json
```

only after the work has been accepted and the pull request has merged. Opening
a pull request, receiving approval without merge, passing CI, or finishing a
local implementation is insufficient. If the harness cannot verify acceptance
and merge, it leaves the task in `in-review` and reports what remains.

After each update, the agent checks the command result instead of assuming the
status changed.

## Publication Boundary

The skill does not automatically run `workbook fetch`, `workbook sync`, or
`workbook push`. Those commands affect shared task refs and remain governed by
the repository's instructions and the user's authorization. The status
lifecycle is expressed through local, Git-durable `workbook update` operations;
publication is a separate concern.

The skill also does not edit Workbook's private Git refs, SQLite projection, or
configuration files directly. On CLI failure it preserves the current task
state, surfaces the error, and uses documented Workbook recovery commands only
when the error calls for them.

## Portability

`SKILL.md` names the skill `workbook` so compatible harnesses can expose it as
their normal explicit skill invocation, including `/workbook` where slash
commands are supported. Instructions refer to capabilities rather than
Codex-specific tool names: inspect files, follow repository guidance, run
commands, verify work, create or inspect the pull request, and confirm merge.

The optional task ID is treated as invocation input rather than being embedded
in a harness-specific script. A personal installation may copy or link the same
`skills/workbook/` directory into `~/.agents/skills/workbook/`.

## Verification

Creation follows a skill-oriented RED/GREEN validation:

1. Exercise fresh agents without the skill against safe temporary Workbook
   repositories and record whether they select or read the wrong task, omit
   lifecycle updates, use display labels, or mark work done prematurely.
2. Add the minimum workflow guidance that addresses observed failures.
3. Repeat explicit-ID and no-ID scenarios with the skill available.
4. Verify review-revision and unmerged-pull-request scenarios do not advance
   prematurely to `done`.
5. Run the skill package validator and inspect `agents/openai.yaml` against the
   final `SKILL.md`.

No validation scenario mutates the live Workbook task queue or publishes task
refs.
