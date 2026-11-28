<!-- workbook:begin generator=v0.7.0-205-g5d620ee2 sha256=57ea93047476ce64e844340e94e2212f75114554c6fee3c176cf699960da0c7f -->
# Workbook guidelines

Workbook tracks this project's tasks in Git refs under `refs/workbook/tasks/`
and its relics under `refs/workbook/relics/`.
Use the `workbook` CLI as the task-state boundary. Never edit Workbook refs,
the SQLite projection, or `.workbook/config.json` directly.

## This project

| Setting | Value |
| --- | --- |
| Project ID | `01KY8964C8TQVBKVACB45DYTNY` |
| Task ID prefix | `WB-` |

A task ID is `<KEY>-<ULID>`. A project may have more than one key, so IDs in
one project can carry different prefixes; all of them are this project's.
`workbook create --key <KEY>` mints a task under another of this project's
active keys; without it, a task is minted under the current key.

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

## Priorities

This project's priorities, most urgent first. Pass the machine value, never
the display label.

| # | Machine value | Display label | Tags |
| --- | --- | --- | --- |
| 1 | `critical` | Critical | none |
| 2 | `high` | High | `default` |
| 3 | `medium` | Medium | none |
| 4 | `low` | Low | none |

| Tag | What it makes Workbook do |
| --- | --- |
| `default` | A task created without `--priority` lands here. Exactly one priority carries it. |

A priority carrying no tag is an ordinary level of urgency: its position in
the table is the whole of what it means.

These priorities belong to this project and another project's are different,
so read them here or with `workbook priority list --json` rather than assuming
the ones you have seen elsewhere. This section is rewritten whenever they
change.

## Task lifecycle

New tasks land in `backlog`.
`workbook next` claims from `ready`.
A dependency is satisfied once it reaches `done`.

1. Select work with `workbook next --claim --json`, which picks the next
   eligible task and assigns it to you in one command, or read a known task
   with `workbook show <id> --json`. Keep the full ID from `data.id`.
2. Take it up with `workbook update <id> --status <status> --assign self
   --json` before editing files, naming the status this project uses for
   work under way. Both changes are recorded as one, so neither lands
   without the other.
3. Move it along the statuses above as the work progresses, including the
   one this project uses for review, and into a status tagged `done` only
   after the work is accepted and merged.
4. Give it back with `workbook update <id> --unassign self` if you stop
   without finishing.

## Assignments say who is responsible

An assignment is `self`, an email address, or either followed by `/label`
naming one agent of that identity — `--assign self/impl-1`. It blocks
nothing and expires never, and only the identity it names or whoever
recorded it may take it away.

`workbook next` skips tasks another identity is assigned to and you are
not, so a fleet does not hand two agents the same work; `--any` offers the
whole eligible set. A task you already hold is still offered to you, and
claiming it again records nothing.

`workbook update <id> --assign self` exits 10 when somebody else holds
that task and you do not, and records nothing: pick another task, or pass
`--force` to work on it alongside them deliberately. `workbook next
--claim` never exits 10 — it has already skipped what somebody else holds
— and answers a fully claimed board with a null `data` and a
`next-held-by-others` warning instead.

An `assignment-shared` warning means you hold the task together with
somebody else, either because you forced it or because two claims raced
and both were kept. The work was recorded; decide with the other party
rather than removing their assignment, which Workbook refuses anyway.

## Relics

A relic holds a spec, a plan, a decision record or similar material that is
longer or more structured than a task. It is made of typed parts, each with
fields and an optional body. A relic ID is `WB-R-<ULID>` and a task ID
`WB-<ULID>`. A part is addressed as `<relic>#<part>`, and any unambiguous
prefix of either ID works. Parts added by one command start alike, so a
part prefix may need 16 characters or more. `workbook next`, `workbook list`
and the board `workbook serve` runs never return relics.

Read the relics linked to a task, one part at a time, and save an edit
naming the version you read:

```sh
workbook show <task> --json
workbook relic list --linked-to <task> --json
workbook relic show <relic>#<part> --body --json
workbook relic update <relic> --part <part> --body-file <file> --base <version> --json
```

`workbook show` lists the task's links in `data.relics`: each entry names
the `relic`, the `part` it links from (absent for a link from the whole
relic) and the `rel`, such as `implemented-by`. `workbook relic list
--linked-to` lists the same relics, without retired ones unless `--retired`
is given. `workbook relic show` with a part and
`--body` carries the body at `data.parts[0].body.text`, or at
`data.parts[0].body.base64` when it is not text, and its version at
`data.parts[0].body.version`.

`workbook relic retire <relic>` marks a relic that is no longer in use, and
`workbook relic unretire <relic>` takes the mark off; do either only when
asked. A retired relic can still be read and changed. `workbook relic list`
leaves it out, and its entry in `data.relics` has `retired` set to `true`.

To see what links to a part, run `workbook relic list --linked-to
<relic>#<part> --json`; `workbook relic show` of that part lists each link
in `data.linkedFrom`.

Task commands and the relic read commands (`workbook relic list`,
`workbook relic show`, `workbook relic export`) do not fetch relics, so
they show the relics this checkout holds. Run `workbook sync` or
`workbook fetch` first to get the latest relics.

To save, write the whole new body to a file and pass the version you read
as `--base`. Two edits to the same body are never merged.

A save whose `--base` is no longer the current version exits 6
(`stale-write`) and writes nothing; repeating the identical command fails
the same way. A sync that meets another clone's different edit of the same
body exits 8 and reports it in the result envelope's `relicConflict` list:
an entry of `type` `body` whose `body` carries `base`, `ours` and `theirs`,
with your text kept at `parkedRef`. Other local changes the sync did not
apply are listed in `unapplied`; make them again after saving the body.
When a relic change exits 8 this way, it recorded nothing; run it again
after you resolve the body.

On either, read the part again. When your intent is clear, apply your
change to the current text and save with `--base` set to the current
version. Otherwise show the user both versions and ask; never save over
text you have not read.

To see what changed since the version you passed as `--base`, run
`workbook relic diff <relic>#<part> <version> --json` with that version.

For other edits (the title, a property or field, or adding, moving,
removing or restoring a part), run `workbook relic update --help`; for
export, see `workbook relic --help`.

### Link relationships

A link's `--rel` must be one of these, or one the relic's schema adds in
`links.relationships`; a link made earlier with another name stays readable.

| Name | Read as |
| --- | --- |
| `implemented-by` | This part of a spec or plan is built by that task. |
| `derived-from` | This was drawn from that source. |
| `summarizes` | This is a summary of that. |
| `decided-in` | This decision was made in that meeting or transcript turn. |
| `changes` | This decision changes that part of a spec. |
| `supersedes` | This relic replaces that one. |
| `relates-to` | A general connection with no stronger meaning. |

### Writing a spec or a plan

To write a spec:

```sh
workbook relic create "<title>" --type spec --set status=draft --json
workbook relic update <relic> --add-part --part-type section --field "title=<title>" --body-file <file.md> --json
workbook relic update <relic> --add-part --part-type decision --field "title=<question>" --field "chosen=<choice>" --field status=proposed --body-file <file.md> --json
workbook relic update <relic> --add-part --part-type open-question --field "title=<question>" --body-file <file.md> --json
workbook relic update <relic> --link <task> --from <part> --rel implemented-by --json
```

Every part needs `title`; bodies are optional Markdown. Set `status` (`draft`,
`approved` or `superseded`) with `workbook relic update <relic> --set
status=approved`. A relic created before the defaults names no schema, and
`workbook relic migrate <relic> --to spec` adopts the default.

To write a plan:

```sh
workbook relic create "<title>" --type plan --set status=draft --json
workbook relic update <relic> --add-part --part-type section --field "title=<title>" --body-file <file.md> --json
workbook relic update <relic> --add-part --part-type step --field "title=<title>" --field-json 'needs=["<title>"]' --body-file <file.md> --json
workbook relic update <relic> --link <task> --from <part> --rel implemented-by --json
```

Every part needs `title`; bodies are optional Markdown. A step's `needs` lists
the titles of the steps it needs first; leave it out when there are none. Set
`status` (`draft`, `approved`, `in-progress` or `done`) with `workbook relic
update <relic> --set status=approved`. A relic created before the defaults names
no schema, and `workbook relic migrate <relic> --to plan` adopts the default.

### This project's relic types

A relic save is checked against the schema version the relic names, and a
refusal names the rule it broke. `workbook relic migrate <relic> --to <target>`
moves a relic to a newer version, and `workbook relic list --behind` lists the
relics behind. Each type below shows its newest version;
`workbook schema show <type> --json` gives the full rules.

- `plan`, built-in `workbook:plan:1`
  - Title: Plan
  - Description: How a spec gets built: titled sections of context and the steps that build it. Link each step to the task that builds it with implemented-by.
  - Required properties: `status` (one of `draft`, `approved`, `in-progress`, `done`)
  - Part `section`: required fields `title`; body optional
  - Part `step`: required fields `title`; body optional
- `spec`, built-in `workbook:spec:1`
  - Title: Spec
  - Description: A design for one feature or change: titled sections, the decisions it makes and the questions it leaves open.
  - Required properties: `status` (one of `draft`, `approved`, `superseded`)
  - Part `decision`: required fields `title`; body optional
  - Part `open-question`: required fields `title`; body optional
  - Part `section`: required fields `title`; body optional

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
| 4 | `not-found` | use an existing task or relic ID |
| 5 | `validation` | change the input; it fails the same way on every retry |
| 6 | `stale-write` | retry the identical command; it will probably succeed. A relic save refused for its `--base` needs a fresh read first |
| 7 | `corrupt-data` | read the message; repair or rebuild before continuing |
| 8 | `conflict` | read the envelope's `conflict` list (`relicConflict` for a relic), change the input, then retry |
| 10 | `assigned` | somebody else holds that task; pick another one, or `--force` to share it |

## Publication is automatic

Commands that create or update a task fetch shared task refs from the
project's tasks remote (`origin` unless configured), apply the change to the
refreshed tip, then publish the single ref they changed. `workbook next`
fetches before answering. A repository with no such remote synchronizes
nothing.

Disable it for one command with `--no-sync`, for this project with
`workbook config set auto-sync false`, or for every project with
`"autoSync": false` in the user configuration's `preferences` block. A project
policy outranks a user preference; `--no-sync` outranks both.
`workbook config show` reports the resolved policy and which layer decided it.
Record a project policy with that command rather than editing
`.workbook/config.json`.

The `sync` member of a result envelope reports what happened. A `failed`
status still means the change was recorded locally and the command exits 0.
Local work that the tasks remote does not have is replayed onto the fetched tip and
published, so a divergent task needs no separate reconciliation step.

## Conflicts

Concurrent edits to different fields are applied silently. Exactly three
situations stop a replay and exit `8`: both sides changed the description, a
replayed dependency would close a cycle, and the tasks remote tombstoned a task a
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
