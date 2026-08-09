# Workbook Agent Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a portable `/workbook` agent skill that selects or resolves a Workbook task, reads it through the CLI, and advances it through implementation, review, and merged completion at the correct milestones.

**Architecture:** Keep the package self-contained under `skills/workbook/`. `SKILL.md` supplies harness-neutral operational guidance and invokes the real Workbook CLI with JSON output; `agents/openai.yaml` adds optional Codex UI metadata. Safe temporary Git repositories and fresh agents provide RED/GREEN behavioral evidence without mutating the live task queue.

**Tech Stack:** Agent Skills Markdown/YAML, Workbook CLI JSON result envelopes, Git temporary repositories, Codex skill metadata, Python skill-package validator.

## Global Constraints

- Work only in `/Users/dylan.goings/source/workbook/.worktrees/workbook-agent-skill` on branch `codex/workbook-agent-skill` during execution.
- Keep the canonical package at `skills/workbook/`; the future bootstrap task may install or link it elsewhere.
- Use `workbook` CLI commands as the only task-state interface; never edit private Git refs, SQLite projections, or Workbook configuration directly.
- Use canonical status values `in-progress`, `in-review`, and `done`, never display labels.
- Do not mark a task `done` until its work is both accepted and merged.
- Do not automatically run `workbook fetch`, `workbook sync`, or `workbook push`.
- Do not mutate the live Workbook queue during behavioral evaluation.
- Run baseline agent scenarios before creating `skills/workbook/`.
- Add no wrapper scripts, permanent evaluation fixtures, or duplicated CLI reference files.

---

### Task 1: Create and behaviorally validate the portable Workbook skill

**Files:**
- Create: `skills/workbook/SKILL.md`
- Create: `skills/workbook/agents/openai.yaml`

**Interfaces:**
- Consumes: optional `/workbook` invocation argument, `workbook next --json`, `workbook show <id-or-prefix> --json`, `workbook update <id-or-prefix> --status <status> --json`, and repository-local agent instructions.
- Produces: Agent Skills package named `workbook`.
- Produces: Codex display metadata with `display_name`, `short_description`, and a `$workbook` default prompt.
- Preserves: repository-specific implementation, testing, Git, pull-request, merge, and task-ref publication policies.

- [ ] **Step 1: Create isolated RED evaluation repositories**

Create three unrelated temporary repositories so baseline agents cannot affect
the real Workbook queue or one another:

```sh
mktemp -d /private/tmp/workbook-skill-red-next.XXXXXX
mktemp -d /private/tmp/workbook-skill-red-explicit.XXXXXX
mktemp -d /private/tmp/workbook-skill-red-review.XXXXXX
```

In each returned directory, run:

```sh
git init
workbook init --key EVAL --json
```

In the no-ID repository, create one eligible task:

```sh
workbook create "Implement fixture behavior" \
  --description "Make the smallest verified source change, then prepare a pull request." \
  --status ready \
  --priority high \
  --json
```

In the explicit-ID repository, create one Ready task and retain the full ID from
the JSON result:

```sh
workbook create "Implement explicit fixture behavior" \
  --description "Make the smallest verified source change, then prepare a pull request." \
  --status ready \
  --priority medium \
  --json
```

In the review repository, create one In Review task and retain its full ID:

```sh
workbook create "Await fixture merge" \
  --description "The pull request is approved but has not merged." \
  --status in-review \
  --priority medium \
  --json
```

- [ ] **Step 2: Run RED scenarios without the skill**

Dispatch fresh agents with no conversation history and without access to the
future skill. Give each agent only its temporary repository path and one of
these prompts:

```text
The user invoked /workbook with no task ID in <red-next-repository>.
Work only far enough to identify and read the task, then describe the exact
task-tracker commands and milestones you would use through merged completion.
Do not edit source files, publish refs, or inspect directories outside this
temporary repository.
```

```text
The user invoked /workbook <explicit-task-id> in <red-explicit-repository>.
Begin the task-tracker workflow, then describe the exact commands you would run
when its pull request is ready for review and when it is accepted and merged.
Do not edit source files, publish refs, or inspect directories outside this
temporary repository.
```

```text
The user invoked /workbook <review-task-id> in <red-review-repository>.
The pull request is approved but is not merged. State the task's correct current
status, whether any Workbook update should run now, and what event permits the
next status update. Do not publish refs or inspect directories outside this
temporary repository.
```

Record the outputs outside the repository worktree. Score each output against
these observable requirements:

- no-ID selection uses `workbook next --json`;
- every selected task is loaded with `workbook show <resolved-full-id> --json`;
- implementation begins only after a successful `in-progress` update;
- review-ready work uses `in-review`;
- approved-but-unmerged work remains `in-review`;
- only accepted and merged work uses `done`;
- commands use canonical values and `--json`;
- no task-ref publication is invented.

Expected RED: at least one baseline output omits or violates an observable
requirement. Capture its exact failure or rationalization before scaffolding the
skill. If every baseline passes, stop: the proposed skill has not demonstrated
an operational benefit and its design should be reconsidered.

- [ ] **Step 3: Scaffold the skill package after observing RED**

Run the required skill initializer without optional resource directories:

```sh
python3 /Users/dylan.goings/.codex/skills/.system/skill-creator/scripts/init_skill.py \
  workbook \
  --path skills \
  --interface 'display_name=Workbook' \
  --interface 'short_description=Work a Workbook task through review and merge' \
  --interface 'default_prompt=Use $workbook to work the next eligible Workbook task through implementation, review, and merge.'
```

Expected: `skills/workbook/SKILL.md` and
`skills/workbook/agents/openai.yaml` are created. No `scripts/`, `references/`,
`assets/`, or example files are created.

- [ ] **Step 4: Replace the scaffold with the minimal skill**

Write this exact `skills/workbook/SKILL.md`, adding only narrowly targeted
clarification when the RED evidence demonstrates an otherwise uncovered
failure:

```markdown
---
name: workbook
description: Use when a repository tracks work with the Workbook CLI and the user invokes /workbook, $workbook, supplies a Workbook task ID, or asks an agent to take the next Workbook task.
---

# Working a Workbook Task

Use the `workbook` CLI as the task-state boundary. Never edit Workbook Git refs,
SQLite projections, or configuration directly.

## Select and read the task

1. If the invocation supplies a task ID or prefix, use it. Otherwise run
   `workbook next --json`.
2. Stop and report the error when Workbook is unavailable or uninitialized,
   selection fails, or no task is eligible. Do not guess.
3. Keep the resolved full ID from `data.id`, then run
   `workbook show <full-id> --json`.
4. Read the full title, description, status, dependencies, labels, and
   acceptance context before editing files.

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

## Keep publication separate

Do not automatically run `workbook fetch`, `workbook sync`, or `workbook push`.
Follow repository guidance and user authorization for shared task-ref
publication.

## Common mistakes

- Use canonical `in-progress` and `in-review`, not display labels.
- Keep using the resolved full task ID after selection.
- Check every JSON command result; do not assume a mutation succeeded.
- Do not claim that Workbook creates branches, pull requests, or merges code.
```

- [ ] **Step 5: Verify the generated Codex metadata**

Require `skills/workbook/agents/openai.yaml` to contain exactly:

```yaml
interface:
  display_name: "Workbook"
  short_description: "Work a Workbook task through review and merge"
  default_prompt: "Use $workbook to work the next eligible Workbook task through implementation, review, and merge."
```

Do not add icons, brand colors, MCP dependencies, or invocation policy.

- [ ] **Step 6: Run package validation**

Install the validator's missing YAML dependency only into a disposable
directory:

```sh
python3 -m pip install --target /private/tmp/workbook-skill-validator PyYAML
```

Run the required validator with that disposable dependency:

```sh
PYTHONPATH=/private/tmp/workbook-skill-validator \
python3 /Users/dylan.goings/.codex/skills/.system/skill-creator/scripts/quick_validate.py \
  skills/workbook
```

Expected: `Skill is valid!`

Also run:

```sh
wc -w skills/workbook/SKILL.md
git diff --check
```

Expected: the skill remains under 500 words and the diff has no whitespace
errors.

- [ ] **Step 7: Run GREEN scenarios with the skill**

Create fresh temporary repositories matching Step 1; do not reuse RED fixtures.
Dispatch fresh agents with no conversation history using the same three
scenarios, prefixed with:

```text
Use $workbook at <absolute-worktree-path>/skills/workbook/SKILL.md for this task.
```

Inspect both agent output and the corresponding temporary repository with:

```sh
workbook show <full-id> --json
```

Expected GREEN:

- no-ID and explicit Ready tasks end the simulated start phase at
  `in-progress`;
- the agents name `in-review` as the review-ready transition;
- the approved-but-unmerged task remains `in-review`;
- `done` is reserved for verified acceptance plus merge;
- all Workbook commands use JSON output and the resolved full ID; and
- no scenario runs `fetch`, `sync`, or `push`.

- [ ] **Step 8: Refactor only from observed GREEN failures**

If a GREEN agent finds a new loophole, add the smallest positive recipe or
explicit counter that addresses its exact behavior, then rerun all three GREEN
scenarios with new temporary repositories and fresh agents. Do not add
hypothetical workflow policy or duplicate `workbook --help`.

Expected: all scenarios converge on the required command sequence and milestone
gates. Re-run package validation and the word-count check after any edit.

- [ ] **Step 9: Review the final repository change**

Run:

```sh
git status --short
git diff -- skills/workbook/SKILL.md skills/workbook/agents/openai.yaml
git diff --check
```

Expected: only the two skill-package files are new, with no unrelated worktree
changes. The design and plan commits already exist on the branch base.

- [ ] **Step 10: Commit the validated skill**

```sh
git add skills/workbook/SKILL.md skills/workbook/agents/openai.yaml
git commit -m "feat: add Workbook agent skill"
```

Do not push, open a pull request, merge, update the live Workbook task queue, or
remove the worktree without explicit authorization.

---

## Later applications of this protocol

Steps 1, 2, 7, and 8 are the reusable part of this plan: any later edit to
`skills/workbook/SKILL.md` that claims to change agent behavior is validated by
running fresh agents against throwaway repositories before and after the edit,
not by asserting that its wording is present. Runs performed after the skill
shipped:

- [`docs/superpowers/evidence/2026-08-08-skill-titles-over-ids-behavior.md`](../evidence/2026-08-08-skill-titles-over-ids-behavior.md)
  — the "IDs are for commands, titles are for humans" section and the
  dependency-title resolution step.
