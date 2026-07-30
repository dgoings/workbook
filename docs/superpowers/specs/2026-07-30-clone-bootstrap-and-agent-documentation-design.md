# Clone bootstrap and agent documentation

Design for `WB-01KYD74JCGN8JM9174DN0GQXG3`, delivered as Workbook v0.2.0.

## Context

`README.md` tells a new contributor to run `./init.sh` in three places (lines 49,
68, and 586). That file has never existed. The second product priority in
`AGENTS.md` is "a small remote team onboarding with `git clone` plus one
bootstrap command," and today there is no such command: a fresh clone needs
`workbook init`, then `workbook fetch`, then manual discovery of how the tool
expects agents to behave.

The agent-facing half of the problem is worse. An agent that lands in a Workbook
project has no reliable way to learn the canonical machine values the CLI
accepts. `workbook update <id> --status "In Progress"` fails, because the
display label is not the stored value. That knowledge currently lives in
`skills/workbook/SKILL.md` in this repository only, hand-maintained, and it
duplicates the status list already defined in `internal/core/task.go:36`.

Both halves are one problem: a project's Workbook documentation is generated
from project state, so it goes stale when either the tool or the project
changes. Workbook v0.1.0 is published through `dgoings/homebrew-tap`, so future
`brew upgrade` runs will move the tool forward while every project's
documentation stays where it was. Homebrew cannot fix this automatically,
because the documentation is per-project and a developer has many projects
checked out.

The intended outcome: one documented bootstrap command, a separate explicit
command that installs and refreshes managed documentation, an honest report of
what is current, stale, or locally modified, and a v0.2.0 Homebrew release whose
caveats tell upgraders what to re-run.

## Decisions

| Decision | Choice |
|---|---|
| Staleness detection | Re-render and compare; recorded hash distinguishes stale from modified |
| Global config | `~/.config/workbook/config.json`, with an empty extensible `preferences` object |
| Bootstrap entry point | `workbook setup`, replacing `workbook init` outright |
| Bootstrap sync scope | Full `sync` (fetch + push), with `--no-sync` to opt out |
| Staleness surfacing | `workbook docs status`, `workbook setup` output, and Homebrew caveats |
| Documentation command | `workbook docs install\|update\|status\|remove` |
| Doc file creation | Refresh `AGENTS.md`/`CLAUDE.md` only if they already exist |

Two consequences are accepted deliberately:

- **`init` is removed, not aliased.** v0.1.0 is a published pre-1.0 POC with a
  small user base. An unknown-command error naming `setup` is clearer than a
  silent alias, and it keeps one bootstrap path rather than two.
- **Re-running `setup` mid-work publishes local task refs**, because setup runs
  a full sync. `--no-sync` is the escape hatch and is stated plainly in help
  text.

## Project configuration is unchanged

`.workbook/config.json` keeps exactly its current four fields. This is a
deliberate constraint, not an oversight: its decoder calls
`decoder.DisallowUnknownFields()` (`internal/gitstore/config.go:306`) and then
requires the file bytes to equal `json.Marshal(config) + "\n"` byte for byte
(`config.go:329`). Any new field would make every v0.1.0 binary fail with
`corrupt-data` and would force a `projectVersion` bump.

Every stamp therefore lives inside the artifact it describes. Nothing about
documentation state is recorded in project config, in the private project guard,
or in SQLite.

## User-global configuration

New file at `$XDG_CONFIG_HOME/workbook/config.json`, defaulting to
`~/.config/workbook/config.json`:

```json
{
  "format": "workbook.user",
  "version": 1,
  "docTargets": ["AGENTS.md", "CLAUDE.md"],
  "skillDir": ".claude/skills",
  "preferences": {}
}
```

- `docTargets` — candidate agent documentation files. A target is refreshed only
  if it already exists in the project; `setup` never creates one.
- `skillDir` — where the project-local skill is written. A relative value
  resolves against the project root; an absolute value writes to a personal
  directory shared across projects.
- `preferences` — `map[string]any`, empty in v0.2.0. This is the extensibility
  point: adding a preference later requires no format version bump and no
  migration.

Missing file means defaults, never an error. `workbook setup` writes the default
file when absent so there is something concrete to edit. The top-level
`format`/`version` pair is validated strictly; unlike project config there is no
byte-canonicality requirement, because this file is hand-edited and never
synchronized.

This introduces the first environment-variable-dependent configuration in the
codebase — `internal/gitstore/repository.go:255` only ever force-sets
`GIT_NO_REPLACE_OBJECTS`. Tests must isolate `HOME` and `XDG_CONFIG_HOME` with
`t.Setenv`.

## Managed artifacts

| Artifact | Ownership |
|---|---|
| `.workbook/guidelines.md` | Fully generated. Tracked, so it travels with the clone. |
| `AGENTS.md`, `CLAUDE.md` | User-owned. One marker-delimited block each. |
| `<skillDir>/workbook/` | Generated from the embedded copy of `skills/workbook/`. |

`.workbook/guidelines.md` and the skill are always managed. `AGENTS.md` and
`CLAUDE.md` are conditional on already existing. A project with neither still
gets guidelines and a skill, so an agent can always find canonical values.

All three use one marker pair and one parser:

```markdown
<!-- workbook:begin generator=0.2.0 sha256=3f9a…c1 -->
## Workbook

This project tracks work with Workbook. See `.workbook/guidelines.md` for agent
workflows and the canonical machine values this project accepts.
<!-- workbook:end -->
```

`guidelines.md` is a marker pair wrapping the whole file body and nothing else.
`AGENTS.md` and `CLAUDE.md` carry a short block inside otherwise untouched
user content.

**Hash definition.** `sha256` covers the bytes strictly between the newline
terminating the begin-marker line and the first byte of the end-marker line,
encoded as lowercase hex. Marker lines are excluded, so rewriting a stamp never
changes the hash it records.

**`generator=` is diagnostic only.** It is displayed to humans reading the file
and never used as a decision input. Editing it changes no behavior; deleting the
whole marker reports as absent. There is no way to silence the tool by editing a
version number into a plausible-looking higher value.

## Reconciliation

For each artifact: parse the marker, hash the current body, render what the body
should be from current configuration.

| Condition | State | Behavior |
|---|---|---|
| No begin/end marker pair | `absent` | Install. |
| Body equals rendered output | `current` | No-op, silent. |
| Body differs, hash equals recorded | `stale` | Safe to overwrite; inputs changed. |
| Otherwise | `modified` | Report; refuse without `--force`. |

The recorded hash is not the staleness signal. Re-rendering is the staleness
signal; the hash exists only to decide whether overwriting is safe. This is what
lets one mechanism catch both staleness causes: a newer generator template, and
a project whose configuration changed under a constant binary. The second case
arrives with `WB-01KYQPKSGAMTB9TQZEV17RGY55` (per-project custom statuses).

A missing, malformed, or unparseable stamp resolves to `modified`, not `stale`.
Every ambiguous case fails toward preserving the file and reporting it.

Rendering is a template fill over already-loaded configuration, so the cost is
negligible and there is no reason to cache it.

## Generated guidelines content

`.workbook/guidelines.md` is generated from the same values the CLI validates
against, so there is no second list to maintain:

- Canonical statuses and display labels from `core.WorkflowStatuses()`
  (`internal/core/task.go:45`), stated explicitly as machine values — `in-progress`,
  not `In Progress`.
- Canonical priorities from a new `core.Priorities()`.
- Exit codes from `core.ExitCode` (`internal/core/errors.go:53`).
- The project's key and ID from `.workbook/config.json`.
- The agent task lifecycle, the `workbook.result`/`workbook.error` JSON envelope
  contract, and the rule that `fetch`/`push`/`sync` stay explicit.
- A trailing line naming `workbook docs update` as the way to regenerate.

**Supporting change:** statuses are a table (`workflowStatuses`,
`internal/core/task.go:36`) but priorities are a hardcoded `switch`
(`isValidPriority`, `task.go:143`). The generator needs one authoritative source
for both, so priorities become a `PriorityDefinition` table mirroring statuses,
with `isValidPriority` iterating it. This is a targeted change to code the
feature depends on, not general refactoring.

## Skill installation

The skill is installed only where it adds operational guidance beyond the shared
guidelines: harness triggering metadata and lifecycle discipline that a
`guidelines.md` cannot express.

To avoid a second copy, `skills/` gains an `embed.go` declaring `package skills`
with `//go:embed workbook`. Go embed paths cannot traverse upward with `..`, so
the embedding package must live in that directory; a `.go` file alongside
`skills/workbook/` does not affect harness discovery. `internal/agentdocs`
imports it. `skills/workbook/SKILL.md` remains the single canonical source.

## Command surface

```
workbook setup [--key <key>] [--no-sync] [--no-docs] [--force] [--json]
workbook docs install [--create <file>] [--force] [--json]
workbook docs update  [--force] [--json]
workbook docs status  [--json]
workbook docs remove  [--force] [--json]
```

`workbook setup` runs, in order:

1. Discover the Git repository.
2. Create or validate project identity via the existing
   `Repository.Init` (`internal/gitstore/config.go:79`).
3. Create the default user config file if absent.
4. Reconcile documentation, unless `--no-docs`.
5. `Repository.Sync`, unless `--no-sync`.
6. Print a summary and the task count.

**Sync failure handling.** No `origin` remote configured means sync is skipped
with a note and setup exits 0 — solo local use with no remote is the first
product priority in `AGENTS.md`. An `origin` that exists but fails to sync is
reported clearly and exits 1 (`operational`); identity and documentation are
already durable and setup is idempotent, so re-running after fixing the remote
costs nothing.

`--no-docs` skips the entire documentation reconcile, including the skill, and
covers the identity-only case that the removed `init` served. "Documentation"
throughout this design means all three managed artifacts.

`docs install` and `docs update` share one reconcile implementation. `install`
additionally accepts `--create <file>` to add a doc target that does not yet
exist, which is the explicit affordance for a project that wants a `CLAUDE.md`
Workbook did not create. `docs status` always exits 0 with state in its output;
it reports, it does not gate. `docs remove` strips managed blocks while
preserving surrounding user content, deletes `guidelines.md` and the installed
skill, and refuses to remove `modified` artifacts without `--force`.

**Wiring.** Adding a command requires four coordinated edits, enforced at
runtime by `validateSchema` (`internal/cli/flags.go:327`): the dispatch switch
(`run.go:59`), `commandSchemas` (`flags.go:90`), `commandOrder` (`flags.go:263`),
and the hand-maintained `usage` constant (`output.go:15`). Additionally,
`renderCommandHelp` hardcodes `[]string{"install"}` as the subcommand list
(`output.go:78`) because `hooks` was the only command with subcommands; this
must become schema-driven or `docs` subcommands will not appear in help.

## Release

The Homebrew formula gains a `caveats` block naming `workbook setup`, so every
`brew install` and `brew upgrade` prints the per-project action Homebrew cannot
take itself.

**Risk being addressed.** `internal/release.RenderFormula`
(`internal/release/formula.go:17`) has no production caller — the live path is
`scripts/publish-release.sh:136` invoking `scripts/render-homebrew-formula.sh`.
Both contain the same formula template, and no test compares them, so editing
one silently diverges. Before adding caveats, add a parity test that runs the
shell script and asserts its output equals `release.RenderFormula` byte for
byte. Collapsing the shell renderer onto the Go one — the pattern `release.sh`
already uses for `internal/release/archivecmd` — is the better fix and is
deliberately left as follow-up work.

Release itself is tagging `v0.2.0` and letting
`.github/workflows/release.yml` build, publish, and update the tap. No release
tooling changes are needed beyond the caveats block.

## Documentation updates

`README.md` is a tested contract — `TestREADMEImplementedCommands` and its
siblings (`internal/cli/run_test.go:863`) parse its code fences and fail when an
unimplemented command is presented as real. Required edits:

- Replace `./init.sh` with `workbook setup` at lines 49, 68, and 586.
- Document `setup` and `docs`; remove `init`.
- Correct the status paragraph at line 14, which still claims no public package
  has been published. v0.1.0 has been live since 2026-07-27.
- Move `brew install dgoings/tap/workbook` out of "Proposed post-POC commands"
  (line 394) into implemented installation instructions.
- Replace the "A future bootstrap command or `init.sh` should:" list at line 586
  with a description of what `workbook setup` does.

## Testing

Following existing conventions: same-package tests, stdlib only, `got`/`want`
failure messages, in-process `cli.Run` rather than an executed binary.

**Unit — `internal/agentdocs`:** marker parsing including malformed and
unterminated markers; hash boundary correctness; all four reconciliation states;
user content outside markers preserved byte for byte across install, update, and
remove; idempotence across repeated updates; rendered output containing canonical
machine values and not display labels.

**Unit — `internal/userconfig`:** defaults when absent; strict `format`/`version`
rejection; round-trip with unknown keys inside `preferences`; `XDG_CONFIG_HOME`
honored and `HOME` fallback, isolated with `t.Setenv`.

**Unit — `internal/core`:** `Priorities()` returns a defensive copy and
`isValidPriority` agrees with it.

**CLI:** `setup` on a fresh repository, on an already-initialized repository, and
with each of `--no-sync`, `--no-docs`, `--force`; `docs` subcommand help coverage
through the existing `commandOrder` loop in `help_test.go:33`; JSON envelopes via
`assertJSONResult`; exit codes asserted numerically; `init` now returning an
invocation error.

**Integration:** `setup` against a bare remote using the `syncRepositories(t)`
pattern (`internal/gitstore/sync_test.go:772`), covering both the fresh-clone
fetch and the no-`origin` skip path.

**Release:** the new shell-versus-Go formula parity test, and an assertion that
the rendered formula contains the caveats text.

**Migration note.** `initializedRepository` (`internal/cli/run_test.go:1488`) is
the single choke point for the `init` → `setup` rename across the existing
suite.

## Out of scope

- Per-project custom status columns (`WB-01KYQPKSGAMTB9TQZEV17RGY55`). This
  design generates from `core.WorkflowStatuses()` so that work can supply a
  different source without a second status list.
- Multiple task remotes (`WB-01KYD742HDHMJ8QGJ72NJ3YTNT`).
- Collapsing `render-homebrew-formula.sh` onto `release.RenderFormula`.
- Linux release artifacts, signing, and notarization.
- Runtime staleness warnings on ordinary commands, notification throttling, and
  any user-facing preference for suppressing them.
- A `workbook status` or `workbook doctor` command.
