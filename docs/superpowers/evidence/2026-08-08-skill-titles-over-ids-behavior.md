# Behavioral evidence: "IDs are for commands, titles are for humans"

This records the RED/GREEN run prescribed by
[`docs/superpowers/plans/2026-07-30-workbook-agent-skill.md`](../plans/2026-07-30-workbook-agent-skill.md)
Steps 1, 2, 7, and 8, applied to the titles-over-IDs section of
`skills/workbook/SKILL.md`. That section shipped with a substring test only
(`TestSkillDocumentSeparatesMachineIDsFromHumanTitles`), which passes for any
phrasing, including phrasing no agent follows. This run measures the phrasing.

## Method

Every run dispatched one fresh Claude Code agent with no conversation history
into its own throwaway Git repository, one repository per run, none of them the
live Workbook queue. The harness was:

```sh
claude -p "<scenario prompt>" \
  --model sonnet \
  --safe-mode \
  --no-session-persistence \
  --output-format stream-json --verbose \
  --allowedTools "Bash(workbook:*)" "Read" "Glob" "Grep"
```

`--safe-mode` disables ambient skills, `CLAUDE.md`, plugins, and hooks, so the
skill file is the only variable between arms. Each fixture repository was built
with `git init` plus `workbook setup --key EVAL --no-sync`, and its generated
`.workbook/guidelines.md` was deleted so no second copy of the guidance could
account for a result. The `workbook` binary was built from this branch.

Three arms, twelve runs:

| Arm | Skill available to the agent |
| --- | --- |
| `red-baseline` | none — `workbook setup --no-skill`, `.claude/` removed |
| `red-currentskill` | the shipped skill, `sha256:aed7a600…f14a` (this branch's parent) |
| `green` | the revised skill, `sha256:d65dd926…4e01` |

The `red-currentskill` and `green` prompts were prefixed with `Use the Workbook
skill at <repo>/.claude/skills/workbook/SKILL.md for this task: read it first
and follow it.` Every agent read the file before acting. Prompts were otherwise
byte-identical across arms.

## Scenarios

1. **announce** — three tasks, no ID supplied. "Take the next available task …
   then write me a short report saying what you took."
2. **transition** — one `in-progress` task, ID supplied. "The pull request … is
   ready for human review. Record that in Workbook, then report the transition
   to me."
3. **error** — one `ready` task, ID supplied, with an instruction to run
   `workbook update <id> --status "In Progress" --json` verbatim and report the
   outcome. The display label is rejected with exit code 5, so the agent has to
   report a failure about a task it can name.
4. **blocked** — task "Add remote claim and lease workflow" (`blocked`)
   depending on "Define the lease renewal protocol" (`in-progress`), ID
   supplied. "Read it, then either start it or tell me why you cannot."

Scoring criterion, applied to the report the agent addressed to the human: does
the first mention of each task use its title, with the ULID absent or clearly
subordinate?

## Result

| Scenario | red-baseline | red-currentskill | green |
| --- | --- | --- | --- |
| announce | fail | pass | pass |
| transition | fail | pass | pass |
| error | fail | pass | pass |
| blocked | fail | **fail** | pass |

The shipped section already changed behavior in three of the four scenarios.
The one it did not cover is the one this evidence was collected for: a report
about a dependency.

### RED, no skill

Baseline agents mirrored the machine identifier into prose in all four
scenarios.

announce:

> **Took:** `EVAL-01KZJ9QMESCBYSFXGH3XXNVQG4` — *"Bound task field sizes and
> read limits"* (priority: high)

transition:

> **Task:** EVAL-01KZJ9RXRQQVCTFA2N2ACCVJM0 — "Migrate the limits test to the
> Options constructor"
>
> **Transition:** `in-progress` → `in-review`

error — the report never names the task at all:

> **Command run:**
> ```
> workbook update EVAL-01KZJ9TY99PAD7ZWPQZAMBVQZP --status "In Progress" --json
> ```
> … The task was **not** updated.

blocked — the agent resolved the dependency's title on its own initiative, then
threw it away in the sentence that mattered:

> **Task EVAL-01KZJ9VGA5DY0HJ4577AXBGZPX — "Add remote claim and lease
> workflow"**
> …
> - Dependency: `EVAL-01KZJ9VG53W8H1885PPQHTG2MJ` — "Define the lease renewal
>   protocol" …
>
> **Recommendation:** wait until EVAL-01KZJ9VG53W8H1885PPQHTG2MJ is completed

### RED, shipped skill

announce, transition, and error passed:

> I took **"Bound task field sizes and read limits"** (`EVAL-01KZJ9XBG5XCVQ8YPSPA3BNKFM`), priority high, and marked it `in-progress`.

> **Report:** "Migrate the limits test to the Options constructor" is now `in-review` …

> `"In Progress"` is the display label, not a valid value for `--status` …
> No update was applied: "Publish the v0.4.2 release notes" (EVAL-01KZJ9YGRSFM9D6123MNJCHMPW) is still `ready` …

blocked failed. Commands run: `Read SKILL.md`, `workbook show <task> --json`,
`workbook show <dependency> --json`, `workbook next --json`. The agent did
resolve the dependency, and still led with both ULIDs:

> I read Workbook task **EVAL-01KZJ9WBE3V1Z69FA4C7XH57W3**, "Add remote claim
> and lease workflow" … I did not start it.
>
> **Reason: it's blocked.** Its status is `blocked`, and it depends on task
> EVAL-01KZJ9WB93HMD346BQ617D7E69, "Define the lease renewal protocol" …

Two gaps produced that: the bulleted examples covered announcing and
transitioning but not reporting bad news, and the dependency bullet asked for
titles without saying where a dependency's title comes from. A run that had
skipped the second `show` would have had nothing to name the blocker with.

### Revision

`skills/workbook/SKILL.md` gained a sixth step in the read procedure —
`data.dependencies` carries bare IDs, so resolve each with `workbook show
<dependency-id> --json` and keep its `data.title`, never invent one — a bullet
stating that a blocked task, a failed command, or a task the agent will not
start is announced by title like everything else, and two entries in the
common-mistakes list.

### GREEN, revised skill

All four scenarios passed. blocked, the previously failing one, resolved the
dependency deliberately and led with both titles:

> Task found. It's status is `blocked`, with one dependency. Let me resolve
> that dependency's title before reporting.

> **Task:** "Add remote claim and lease workflow" (`EVAL-01KZJA282DQ30FSV8J8VR65BXN`) …
>
> **Why:** It depends on "Define the lease renewal protocol"
> (`EVAL-01KZJA27XB237YZ8P4WTA8KNYD`), which is still `in-progress`, not `done`.

announce, transition, and error stayed passing:

> **Taken:** "Bound task field sizes and read limits" (`EVAL-01KZJA31JV7YTXP8GA9JF4NQ7N`), priority high, no dependencies. Status moved from `ready` → `in-progress`.

> **Report:** The task "Migrate the limits test to the Options constructor" (EVAL-01KZJA3KRXJH0HKBJ03WEMKSYK) has transitioned from `in-progress` to `in-review` …

> I confirmed via `workbook show` (read-only) that the task "Publish the v0.4.2 release notes" is still at status `ready`, unchanged.

No Workbook command in any of the twelve runs touched a repository other than
its own fixture, and no run invoked `fetch`, `push`, or `sync`.

## Limits of this evidence

Twelve single-sample runs of one model at one temperature are a demonstration,
not a statistic; a rerun can differ. The `error` scenario hands the agent the
failing command, so its opening line legitimately contains an ID no matter what
the skill says, and it is scored on the rest of the report. Fixture repositories
and raw `stream-json` transcripts lived under `/private/tmp` outside the
worktree, as the plan requires, and were not preserved; the quoted reports are
verbatim excerpts from them.
