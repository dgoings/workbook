# Workbook command reference

Every command the `workbook` CLI implements, with its flags, output contract,
and exit codes. For installation and a quick start, see the
[README](../README.md); for architecture and the Git storage model, see
[architecture.md](architecture.md).


The current CLI implements these local commands. Commands marked `--json` support
both human-readable output and a versioned machine-readable result envelope:

```text
workbook setup [--key <key>] [--no-docs] [--no-sync] [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook setup --tasks-only <url> [--key <key>] [--no-docs] [--no-skill] [--json]
workbook create <title> [--description <text>] [--status <status>] [--priority <priority>] [--due <date>] [--key <key>] [--label <label>] [--no-sync] [--json]
workbook list [--status <status>] [--priority <priority>] [--key <key>] [--label <label>] [--find <text>] [--overdue] [--due-before <date>] [--sort due] [--all] [--json]
workbook show <task> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--get-attachment <attachment> [--out <path>]] [--json]
workbook update <task> [--title <title>] [--description <text>] [--status <status>] [--priority <priority>] [--due <date> | --clear-due] [--label <label>] [--clear-labels] [--comment <body>] [--edit-comment <comment>] [--remove-comment <comment>] [--attach-file <path>] [--attach-url <url>] [--attach-label <text>] [--remove-attachment <attachment>] [--assign <who>] [--unassign <who>] [--force] [--no-sync] [--json]
workbook delete <task> [--no-sync] [--json]
workbook restore <task> [--into <status>] [--no-sync] [--json]
workbook move <task> (--before <task> | --after <task>) [--no-sync] [--json]
workbook depend <task> <dependency> [--no-sync] [--json]
workbook free <task> <dependency> [--no-sync] [--json]
workbook next [--any] [--claim] [--limit <n>] [--no-sync] [--json]
workbook rebuild [--json]
workbook validate [--full] [--json]
workbook compact [--done-only [<duration>] | --deleted-only [<duration>]] [--since <duration>] [--keep-attachments] [--no-backup | --backup <path>] [--dry-run] [--no-sync] [--json]
workbook compact --adopt <task> [--discard-local] [--no-backup | --backup <path>] [--json]
workbook version [--json]
workbook fetch [--remote <remote>] [--json]
workbook push [--remote <remote>] [--json]
workbook sync [--watch [--interval <duration>]] [--status] [--remote <remote>] [--json]
workbook move-tasks [--from <remote>] [--allow-branches] [--clean-up] [--no-backup | --backup <path>] [--json]
workbook reset [--dry-run] [--no-backup [--discard-unpublished] | --backup <path>] [--json]
workbook reset --remote <remote> [--confirm <remote>] [--dry-run] [--no-backup | --backup <path>] [--json]
workbook relic create [<title>] [--type <type>] [--from-file <file>] [--no-sync] [--json]
workbook relic list [--type <type>] [--all] [--linked-to <task>] [--json]
workbook relic show <relic>[#<part>] [--body] [--removed] [--json]
workbook relic update <relic> [--title <title>] [--set <name>=<value>]... [--set-json <name>=<json>]... [--clear <name>]... [--no-sync] [--json]
workbook relic update <relic> --add-part --part-type <type> [--field <name>=<value>]... [--field-json <name>=<json>]... [--body-file <file> [--body-format <label>]] [--before <part> | --after <part>] [--no-sync]
workbook relic update <relic> --part <part> [--field <name>=<value>]... [--field-json <name>=<json>]... [--clear-field <name>]... [--body-file <file> [--body-format <label>] [--base <version>]] [--no-sync]
workbook relic update <relic> --move-part <part> (--before <part> | --after <part>) [--no-sync]
workbook relic update <relic> [--remove-part <part>]... [--restore-part <part>]... [--no-sync]
workbook relic update <relic> [--link <task> [--from <part>] --rel <rel>]... [--unlink <link>]... [--no-sync]
workbook relic delete <relic> [--no-sync] [--json]
workbook relic restore <relic> [--no-sync] [--json]
workbook relic export <relic> [--json]
workbook status list [--json]
workbook status add <status> [--label <label>] [--before <status> | --after <status>] [--tag <tag>]... [--no-sync] [--no-docs] [--json]
workbook status rename <status> <new-status> [--label <label>] [--no-sync] [--no-docs] [--json]
workbook status label <status> <display-label> [--no-sync] [--no-docs] [--json]
workbook status move <status> (--before <status> | --after <status>) [--no-sync] [--no-docs] [--json]
workbook status tag <status> ([--tag <tag>]... | --clear-tags) [--no-sync] [--no-docs] [--json]
workbook status untag <status> <tag> [--no-sync] [--no-docs] [--json]
workbook status delete <status> --into <status> [--no-sync] [--no-docs] [--json]
workbook status log [--limit <n>] [--all] [--json]
workbook priority list [--json]
workbook priority add <priority> [--label <label>] [--before <priority> | --after <priority>] [--no-sync] [--no-docs] [--json]
workbook priority rename <priority> <new-priority> [--label <label>] [--no-sync] [--no-docs] [--json]
workbook priority label <priority> <display-label> [--no-sync] [--no-docs] [--json]
workbook priority move <priority> (--before <priority> | --after <priority>) [--no-sync] [--no-docs] [--json]
workbook priority tag <priority> --tag <tag> [--no-sync] [--no-docs] [--json]
workbook priority delete <priority> --into <priority> [--no-sync] [--no-docs] [--json]
workbook priority color <priority> [<color>] [--no-sync] [--no-docs] [--json]
workbook priority log [--limit <n>] [--all] [--json]
workbook key list [--no-counts] [--json]
workbook key add <key> [--current] [--no-sync] [--no-docs] [--json]
workbook key current <key> [--no-sync] [--no-docs] [--json]
workbook key retire <key> [--no-sync] [--no-docs] [--json]
workbook key log [--limit <n>] [--all] [--json]
workbook config show [--json]
workbook config set <setting> <value> [--url <address>] [--no-sync] [--json]
workbook config unset <setting> [--no-sync] [--json]
workbook docs install [--create <file>] [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook docs update [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook docs status [--skill-dir <dir>] [--no-skill] [--json]
workbook docs remove [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook hooks install [--json]
workbook serve [--addr <address>] [--public-url <url>] [--actor-header <header>]
workbook completion <shell>
workbook help [command]
```

`workbook list --find <text>` keeps the tasks whose title or description
contains every whitespace-separated word of `<text>`, case-insensitively, or,
for a word shaped like the start of a task ID (a project key, a dash, and more
characters), whose ID begins with it; it combines with the other `list` filters,
and an empty `--find` is refused. `--overdue`, `--due-before` and `--sort due`
are described under [Due dates](#due-dates).

`workbook setup` is the single bootstrap path for a fresh clone. It creates or
validates the tracked `.workbook/config.json` holding the project ID and key,
writes the user-global configuration file when it is missing, installs or
refreshes managed agent documentation, and synchronizes shared task refs with
`origin`, or with the remote the project's settings name for its tasks (see
[A tasks remote other than origin](#a-tasks-remote-other-than-origin)), which
setup adds to the checkout first. It skips synchronization when no such remote
is configured, so a solo local project needs no remote. Use `--no-sync` to bootstrap without
exchanging refs and `--no-docs` to create project identity alone.

The project key, the prefix a task ID carries, is asked for only when setup
creates a new project; it is that project's founding key, and a project may add
more later. A clone joining a project that already exists, in the working tree
or on `origin`, takes that project's founding key, and `--key` is then a claim
that has to agree with it. For a new project on a terminal, setup prompts
`Project key [WORK]:` with a key derived from the repository's directory name,
and an answer is trimmed and uppercased before the grammar
`^[A-Z][A-Z0-9]{1,9}$` is checked. Enter accepts the suggestion, an answer the
grammar refuses is explained and asked again up to five times, and Ctrl-D or
Ctrl-C ends setup without creating anything. Setup asks only when stdin and
stdout are both terminals and `--json` was not passed; otherwise, including
`workbook setup > setup.log` from a terminal, it takes the derived key without
asking, so scripts and the desktop app get a key that names the project. Pass
`--key` to choose without being asked. The key setup reports is the project's
*current* key, which is the founding key until `workbook key current` moves it;
see [Project keys](#project-keys).

The derived key is the initials of the directory name's words when it has
more than one (`my-app` becomes `MA`, `workbook-desktop-shell` becomes `WDS`,
`MyAppService` becomes `MAS`), or the first four letters of the one word there
is (`workbook` becomes `WORK`), uppercased. A result shorter than two
characters is padded with `WB`, one that does not start with a letter gets `W`
in front (`2024-planning` becomes `W2P`), and a name with no ASCII letters or
digits gives `WB`; accented and non-Latin characters are treated as word
breaks rather than kept (`café` becomes `CAF`). A long or unusual directory
name, and any script or CI job, is a reason to pass `--key` rather than
inherit a key derived from a directory. A project that inherits one it does not
want is not stuck with it — `workbook key add` and `workbook key current` move
where new tasks are minted — but the task IDs already minted keep the prefix
they were given, because a task ID is a permanent name.

A checkout whose working tree has no `.workbook/config.json` does not
necessarily mean the project is new: a branch cut before the project adopted
Workbook looks exactly like that. Before minting a fresh identity, setup
therefore asks `origin`. A configuration committed on origin's default branch
is adopted into the working tree, so running setup from a pre-Workbook branch
joins the existing project instead of splitting the repository into two. When
origin holds Workbook task refs but no committed configuration names their
project, setup stops rather than guessing; and when origin cannot be reached,
setup reports that rather than minting blind — `--no-sync` remains the
deliberate way to bootstrap without consulting origin.

The Workbook skill is installed under the directory named by the user-global
`skillDir` setting, `.claude/skills` by default. Because that setting applies to
every project on a machine, `--skill-dir <dir>` overrides it for one project and
`--no-skill` leaves the skill alone while still managing the guidelines. These
flags are a stopgap until per-project configuration lands.

### Tasks-only folders

A server that only shows and edits tasks, such as one running `workbook serve`
for a team, has no use for the project's code. `workbook setup --tasks-only
<url>`, run in an empty folder, makes that folder hold the project's Workbook
refs and nothing else:

```sh
mkdir board && cd board
workbook setup --tasks-only git@example.com:team/project.git
git config user.email board@example.com   # the author edits are recorded under
workbook serve
```

`<url>` is anything `git` accepts as a remote: an SSH or HTTPS URL, or a path.
The remote has to hold a Workbook project already, published with its project
identity `refs/workbook/project`; any clone running a Workbook that publishes
it (`workbook setup` or `workbook sync`) has done so. A tasks-only setup joins
that project and never creates one.

The folder is an ordinary Git folder, not a bare one, with nothing checked out.
It has no branch and no tag, and none of the code's objects. Setup writes
`.workbook/config.json` into it as it does in a clone, uncommitted, and installs
no agent documentation or skill. `list`, `show`, `create`, `update`, `serve`
and the other task commands work there as they do in a clone, and
changes publish to the remote the same way. The folder gets the project's
relics too, and the relic commands work in it; see
[Sharing relics](#sharing-relics).

What makes the folder tasks-only is recorded in its own Git settings, never in
the shared `.workbook/config.json`:

```ini
[workbook]
	tasksOnly = true
[remote "origin"]
	tagOpt = --no-tags
	fetch = ^refs/*
	url = git@example.com:team/project.git
```

The fetch refspec is a negative one that matches every ref, so a plain `git
fetch`, `git fetch --all`, `git remote update` or `git pull` in the folder
downloads nothing, not even branches pushed after setup ran. Workbook's own
fetches name the refs they want on the command line and are not narrowed by it.
Git's default setting would fetch every branch, and leaving the refspec out
would fetch the default branch into `FETCH_HEAD`, which `git pull` then checks
out. A Git command that names a branch, such as `git fetch origin main`, still
downloads it, because it asks for it explicitly. `git fetch --refetch origin`
does not finish in the folder and has to be interrupted; it downloads nothing.
Git does the same in any repository whose only fetch refspec is negative. The negative refspec needs Git 2.29 or
later, which is the version Workbook needs anyway (see
[Installation](../README.md#installation)).

Setup accepts three folders:

- an empty folder outside any Git repository, which it makes a Git folder;
- a folder it already set up for the same URL. Running the command again
  there, or running a plain `workbook setup`, changes nothing and reports
  success. If somebody has changed one of the settings above, such as putting
  the fetch refspec back to Git's default, setup restores it and names it;
- a Git folder with nothing in it at all (no ref, no remote, and no file beside
  `.git`), which is what a hand-run `git init` or an interrupted first run
  leaves. Setup adopts it.

It refuses, before changing anything, a folder that holds files, an existing
repository it did not set up (a clone of the code, for one), an empty folder
inside another repository, a Git folder whose object format (SHA-1 or SHA-256)
differs from the remote's, since Git cannot fetch between the two, and a
tasks-only folder set up for a different URL. Each refusal exits `5`, the same
on every retry.

The URL is compared exactly as written, so another spelling of the same remote
(`../project.git` for `/srv/git/project.git`, a trailing `/`, a `file://`
prefix) is refused like a different remote. To give a tasks-only folder a new
address, because the remote moved or you prefer another spelling, change it in
the folder and run setup again:

```sh
git config remote.origin.url git@example.com:team/moved.git
workbook setup --tasks-only git@example.com:team/moved.git
```

After `workbook move-tasks`, a tasks-only folder on the old place is refused
with the `tasks-moved` reason, and the refusal names the one command that fixes
it: `workbook setup --tasks-only <new address>`, run in that folder, where the
new address is the place the tasks are now, however many moves ago the folder
was set up. Setup accepts that address without the `git config` step, because
the markers name it: it reads the folder's own place (fetching the folder's
origin identity ref first, when it can) and follows each marker by the address
it records, accepting any address on the way, so a folder can also be pointed
at a place it was on before and the tasks have come back to. It points `origin`
at the new address, reads the tasks from there and publishes the edits saved in
the folder that the old place refused. Nothing is lost and the command is safe
to run again. Any address the markers do not name is still refused as a
different remote, and the `git config` step above is the way to insist on one. The result's `tasksOnly` object gains `movedFrom`, the old
address, and the text report's `Mode:` line says the folder was repointed. A
board served from the folder has to be stopped before and started after; see
[hosting](hosting.md#updating-and-retiring).

A plain `workbook setup` in a tasks-only folder that has no remote URL yet, as
a first run interrupted before its last step leaves, refuses with exit `5` and
names `workbook setup --tasks-only <url>`, which finishes it.

Only the folder's own Git configuration (`git config --local`) counts. A
`workbook.tasksOnly` set in your global or system Git settings, or supplied
through `GIT_CONFIG_*` environment variables or `git -c`, is ignored, so it
cannot turn an ordinary clone into a tasks-only folder.

Setup asks the remote before it creates anything, so a wrong URL, an
unreachable remote or a remote with no Workbook project leaves the empty folder
empty. An unreachable remote exits `1`. A remote with no project, or one whose
tasks predate the published project identity, exits `5` and names the clone
command that fixes it. A clone finds an older project by reading
`.workbook/config.json` from the remote's default branch, which downloads the
code, so a tasks-only folder never does that.

A new folder's object format follows the remote's, so a SHA-256 project gets a
SHA-256 folder.

`--no-sync` is refused, because the folder can only join a project the remote
already has. `--skill-dir` and `--force` are refused because the folder installs
no agent documentation. `--no-docs` and `--no-skill` are accepted and change
nothing, since that is already what a tasks-only setup does. Each refusal exits
`2`. `--key` is a claim, as in a clone: it has to agree with the project's
founding key. Setup checks it against the remote before it creates or changes
anything, so a wrong key leaves the folder as it was.

Setup records nothing in task history, so it needs no author and succeeds in a
folder with no `user.email`. Every command that edits a task does need one. The
report says which author edits will be recorded under, or that none is
configured; set one with `git config user.email <address>` in the folder before
editing.

With `--json`, the setup result carries a `tasksOnly` object, which a setup in
a clone of the code leaves out. `docs` is absent from a tasks-only result
because no documentation is installed:

```json
"tasksOnly": {
  "remote": "git@example.com:team/project.git",
  "initialized": true,
  "changed": ["workbook.tasksOnly", "remote.origin.tagOpt", "remote.origin.fetch", "remote.origin.url"],
  "actor": "",
  "unavailable": ["docs", "hooks"]
}
```

`initialized` is true only for the run that made the folder a Git folder.
`movedFrom`, present only on the run that repointed a folder after a move, is
the address it used before.
`changed` names each Git setting the run wrote, and is `[]` on a rerun that
changed nothing. `actor` is the address edits will be recorded under, or `""`.
`unavailable` lists the commands that refuse in this folder. The text report
says the same in `Mode:`, `Remote:`, `Author:` and `Unavailable:` lines. Its
`Mode:` line also tells a setting the folder lacked, which it says it `set`,
from one somebody had changed, which it says it `restored`. With no address to
record edits under, the `Author:` line reads `none configured` and says how to
give one: `git config user.email <address>` in the folder, or `WORKBOOK_ACTOR`,
which is enough on its own.

#### What is unavailable without the code

Anything Workbook derives from code history reports that it is unavailable in a
tasks-only folder, rather than answering from history the folder does not hold.
Each refusal is a `validation` error (exit `5`) whose message says that the
command is unavailable in a tasks-only folder:

| Command | Why |
| --- | --- |
| `workbook docs install`, `update`, `status`, `remove` | Agent documentation and the skill are written for people working in the code. |
| `workbook hooks install` | The managed pre-push hook runs when somebody pushes code, and nobody pushes code from this folder. |

Commands that change statuses, priorities or keys still work. They refresh the
generated guidelines only where those exist, and a tasks-only folder has none.
Implementation links, branch inclusion and landed state are
[proposed](architecture.md#relationship-to-code-branches) and not yet
implemented. When they are, they will report themselves unavailable here in the
same way.

## What a task may hold

A task ref is shared history. Every clone that fetches, synchronizes, or runs an
auto-syncing mutation reads every task tip into memory, so an unbounded field is
not one collaborator's problem but the whole team's. Workbook therefore bounds
what a task document may contain and how much of one it will read back:

| Limit | Value | Applies to |
| --- | --- | --- |
| Title | 500 bytes | The title after surrounding whitespace is trimmed. |
| Description | 65,536 bytes (64 KiB) | The description as stored. |
| Label | 100 bytes | Each individual label. |
| Labels per task | 50 | Distinct labels, counted after duplicates are dropped. |
| Rank | 4,096 bytes | The `numerator/denominator` ordering key. |
| Dependencies per task | 100 | Distinct dependencies, counted after duplicates are dropped. |
| Comment body | 16,384 bytes (16 KiB) | One comment, after surrounding whitespace is trimmed. |
| Comments per task | 500 | Live comments; a removed one no longer counts. |
| Attachments per task | 50 | Live attachments, files and links together. |
| Attached file | 1,048,576 bytes (1 MiB) | One attached file. |
| Attached files per task | 10,485,760 bytes (10 MiB) | What one task's live attached files add up to. |
| Attachment URL | 2,048 bytes | One link attachment's URL. |
| Git object | 4,194,304 bytes (4 MiB) | Any single object Workbook reads: a commit, a task tree, or a stored document. |
| Web request body | 1,048,576 bytes (1 MiB) | One request to `workbook serve`, except the attachment upload below. |
| Web attachment upload body | 1,463,640 bytes | One `POST /api/tasks/<id>/attachments`, which carries an attached file base64-encoded inside JSON. Base64 costs four bytes for every three, so the largest attachment needs four thirds of its own ceiling plus the envelope around it. It bounds the encoding, not the attachment: a file over 1 MiB is still refused, against the same number and in the same sentence core answers with, before anything is staged. |

Lengths are counted in bytes rather than characters, because bytes are what a
reader has to allocate. A field over its limit never reaches storage: the CLI
answers with a `validation` error (exit `5`) naming the size and the ceiling,
and the board answers `400` carrying the same document. The same check runs
when a stored document is read back, where
an over-limit field describes data that is already written rather than input
that can be corrected, and is reported as `corrupt-data` (exit `7`).

The limits are set far above ordinary use: a title is a headline, a description
is prose and short code fences, and labels are vocabulary. A description that
approaches 64 KiB is a document, and a document belongs in a relic linked to
the task (see [Relics](#relics)).

The rank limit bounds work rather than storage. A rank is an exact rational, it
is parsed every time a task is read and again on every comparison that orders a
board, and converting a long digit string costs more than linear time — so an
unbounded rank is a cheaper denial of service than an unbounded description,
even though it is far smaller. Ordinary ranks are a few bytes: placing a task
between two neighbours halves the gap, which adds about one byte for every three
placements into the same shrinking gap.

The object ceiling is checked against the size Git reports before the object is
read, so an object claiming to be a gigabyte costs a comparison rather than a
gigabyte. It sits roughly fifty times above the largest task document the field
limits above allow, so it never fires on anything Workbook produced; it exists
so that an object hand-built and pushed by a collaborator cannot exhaust memory
in every clone that fetches it. An object over it is reported as `corrupt-data`
(exit `7`) against the one task that holds it: the record is skipped rather than
read, so a single oversized object marks that task unreadable instead of
stopping every command that reads the project.

Objects are also read one at a time rather than by buffering a whole batch of
Git's output, so reading a project costs one object at a time instead of every
task tip at once.

Treat these numbers as part of the storage format. Raising one is a compatible
change — an older clone rejects a document a newer one accepted. Lowering one is
not, because a task already stored at the old size stops reading.

The comment and attachment ceilings are asked of what a task would hold after a
change, and only of growth: a task already over one — which two people
commenting on the same afternoon can produce without either doing anything they
were not allowed to do — can still have things removed from it. A file over the
per-file ceiling is refused before it is read, and the refusal suggests a link,
which stores nothing. Only a regular file may be attached: a ceiling read off a
stat bounds only a file whose size a stat describes, and a device or a named
pipe reports no size and then reads without end.

## Due dates

A task may have a due date. It is a calendar date with no time of day:

```sh
workbook create "Ship the beta" --due 2026-12-01
workbook update WB-01K0M6B8 --due +3d       # three days from today
workbook update WB-01K0M6B8 --due tomorrow
workbook update WB-01K0M6B8 --clear-due
```

`--due` takes a date as `YYYY-MM-DD`, `today`, `tomorrow`, or `+Nd` for N days
from today, where N is decimal digits from 0 to 36500 and leading zeros are
allowed (`+07d` is `+7d`). The relative forms are worked out against this
machine's local date when the command runs, and only the date they come to is
recorded: the confirmation prints it, and the task holds `2026-12-04`, never
`+3d`. A migration that sets `WORKBOOK_NOW` to record a project's past resolves
them against the local date of that recorded instant instead, because the date
is written into the history: `WORKBOOK_NOW=2026-03-01T02:00:00Z` with
`--due today` records `2026-02-28` on a machine in New York, where that instant
is still February 28. Whether a task is overdue is still judged against the
real today.
Anything else — `2026-02-30`, `next friday`, `+1w`, an empty value — is a
`validation` error (exit `5`) and records nothing. `--due` and `--clear-due`
together are an `invalid-invocation` (exit `2`). Setting, changing and clearing
a date are each one operation in the task's history, shown by
`workbook show --history` like any other field change.

**Overdue and due soon.** A task is *overdue* when its date is before today and
*due soon* when its date is today or one of the next three days. A task in a
status tagged `done`, or a deleted task, is never either. "Today" is the local
date of whoever is looking: two people in different timezones can see the same
task as overdue and not overdue for up to a day around midnight. That is a
difference in what they are shown; both hold the same date.

**Finding them.**

```sh
workbook list --overdue                  # only tasks that are overdue
workbook list --due-before 2026-12-01    # due strictly before this date
workbook list --due-before +7d           # due within the coming week
workbook list --sort due                 # earliest first, undated tasks last
```

`--due-before` takes the same forms as `--due` and includes finished tasks,
since a date before a cutoff is a fact about the task rather than a judgment.
All three combine with the other `list` filters. `--sort due` orders by date,
with tasks of the same date, and every undated task after them, in the board's
usual order; it is the only `--sort` order. `workbook next` does not look at due
dates: what it offers next is decided as it always was.

**How they read.** `workbook show` prints a `Due:` line, empty for a task with
none. `workbook list` adds a `DUE` column when any listed task has a date. Both
put the word beside the date — `2026-11-20 overdue`, `2026-11-24 due soon` —
because the terminal output carries no color. The web board's cards and task
page show the same words, with color repeating them rather than standing in for
them, and the task page sets the date with a date field (empty it to clear the
date). The board judges each date in the browser, against the reader's own
local date, and judges again when that date changes while the page is open, on
its next poll; the page as the server first sends it shows a card's date with
no word beside it, so a reader with scripts off sees the date alone. The
desktop app's Next view marks a dated task the same way.

**In JSON**, every task object carries three members:

| Member | Value |
| --- | --- |
| `due` | The date as `"YYYY-MM-DD"`, or `null` when the task has none. Always present. |
| `overdue` | `true` when the task is overdue on the local date of the machine that answered. Always present. |
| `dueSoon` | `true` when the task is due soon on the local date of the machine that answered. Always present. |

`overdue` and `dueSoon` are worked out when the command runs and never stored,
so the same task can answer differently tomorrow, or on a machine in another
timezone. The tasks `workbook serve` sends its board carry them too, judged on
the serving machine's local date; the board does not use them. A caller in
another zone than the machine that answered should judge `due` itself.

**Synchronization.** Two clones that set different dates on one task reconcile
the way they do for any other one-value field: the clone that synchronizes last
has its date applied on top, and no conflict is reported. Two clones that set
the same date are not in conflict either. Compaction keeps a task's date.

**Older Workbook versions.** A task that has ever had a due date is written in a
format releases before this one cannot fully read. Such a release still lists,
shows and synchronizes the task, without its date, marks it as written by a
newer Workbook, and refuses to change it (exit `9`, `newer-writer`). A task that
never had a date is unaffected. Everyone on a team upgrades together; see
[Mixed versions and forks](#mixed-versions-and-forks), and
[Due dates](architecture.md#due-dates) in the architecture notes for the format.

## Comments and attachments

Comments and attachments are things a task holds, so they ride `workbook
update` rather than becoming verbs of their own:

```sh
workbook update WB-01K --comment "shipped behind the flag"
workbook update WB-01K --edit-comment 01K7… --comment "shipped, flag removed"
workbook update WB-01K --remove-comment 01K7…
workbook update WB-01K --attach-file ./profile.png
workbook update WB-01K --attach-url https://example.com/pr/42 --attach-label "The pull request"
workbook update WB-01K --remove-attachment 01K8…
```

The body of an edit travels in `--comment` because a flag carries one value, so
`--comment` alone adds a comment and `--comment` beside `--edit-comment` is the
new body. That is the whole ambiguity in the set, and it is settled by refusing
the two invocations that would otherwise be guesses: `--edit-comment` without
`--comment` has no new text, and `--attach-label` without `--attach-url` has
nothing to label. Every other combination is two intents and composes.

Composition is the point of putting them on `update`. One invocation is one
operation pack, one commit, and one entry in `workbook show --history`:

```sh
workbook update WB-01K --status done --comment "shipped" --attach-url https://example.com/pr/42
```

records the status change, the comment and the link together, or records
nothing at all.

`--attach-url` takes an `http` or `https` address and nothing else, and refuses
one that holds a control or bidi-control character: U+202E RIGHT-TO-LEFT
OVERRIDE would draw `https://evil.example/<U+202E>fdp.exe` as
`https://evil.example/exe.pdf`. The web board refuses the same addresses. A link
stored before this rule, or by another clone, is kept, and the command line and
the board draw its control characters and direction overrides as spaces.

Only the person who wrote a comment may change its text. The author is the
name the comment was recorded under, and the person editing is the one
[`WORKBOOK_ACTOR`](#who-an-edit-is-recorded-under) names, or else the
checkout's `user.email`; both are asserted names, compared exactly, and
nothing verifies either. Anybody else's `--edit-comment` is refused with
category `validation` (exit `5`), naming the author, and nothing is recorded —
including the rest of the same `update`, since one invocation records all of
its changes or none. Anybody may remove any comment, and the thread then says
who did:

```text
Comments:
	01K7…A1	bob@example.com	2026-11-21T09:00:00Z	(removed 2026-11-21T10:30:00Z by alice@example.com)
	01K7…B2	bob@example.com	2026-11-21T09:05:00Z
		Still here.
```

A removed comment keeps its place in the thread as that one line — whose it
was, when it was written, and who removed it when — and its text is not
printed. Removing a comment takes it out of the thread, and its text remains in
the task's history, which anyone who can open the board or fetch the repository
can read; `show --history` shows it. A
history can also hold an edit somebody other than the author made, before this
rule existed or from a clone running a build without it. Such an edit stands as
recorded, on every clone, and the thread names who made it:
`(edited 2026-11-21T11:00:00Z by alice@example.com)`. An edit by the author
prints as it always has, `(edited …)`.

Who removed a comment and who made an edit come from the operations that
recorded them, read on every `show`. A task that was
[compacted](#task-archive-compaction) keeps only its checkpoint, which holds
the live thread and not the operations that changed it, so for anything done
before the compaction:

- a removed comment is gone from the thread entirely, with no line for it;
- an edited comment still says when it changed but not who changed it, and
  says so: `(edited 2026-11-21T11:00:00Z, editor not recorded)`. It never reads
  as its author's own words, because it may not be.

An edit or a removal made after the compaction is on the task's chain again,
and names its maker as above.

A comment or attachment identifier may be typed as any unambiguous prefix of
the one `workbook show` prints, the way a task ID may be. `show` prints them
whole rather than shortened, because identifiers minted moments apart share a
long prefix and a display that could not be typed back would be worse than a
long one.

`workbook show` renders the thread and the attachment list under the task, and
`--json` carries both in the task document. In `show --json` each entry of
`comments` also carries `editable`, always present, which says whether the
person running the command may edit it, and `editedBy` when the history names
who made the edit `editedAt` records. When the comment was edited and the
history no longer records who made the edit, because it came before the task
was compacted, the entry carries `"editorNotRecorded": true` and no `editedBy`;
the member is absent otherwise. Removed comments are listed under
`removedComments`, in identifier order, as `id`, `author`, `createdAt`,
`removedBy` and `removedAt`, and never with a body; the member is absent when
nothing was removed. One attached file's bytes come back
with `workbook show <task> --get-attachment <attachment>`, on standard output or
into the file `--out` names:

```sh
workbook show WB-01K --get-attachment 01K8… --out ./profile.png
```

Because that output is bytes rather than a rendered task, no other `show`
option may be given beside it — including `--json`, which a caller reading
bytes cannot use anyway. A refusal there is plain text and the exit code
carries the category: a link holds no bytes and is refused with the URL it
points at, and an attachment nobody holds is `not-found`.

A file whose bytes [compaction](#task-archive-compaction) dropped keeps its
row, so a reader still learns it existed. `show` lists it with
`(removed by compaction on <date>)` after its name, its `--json` entry carries a
`dropped` member with the run ID and time, and `--get-attachment` refuses it as
`not-found` (exit `4`) with
`attachment "<name>" was removed when the task was compacted on <date>`.

Removing a comment or an attachment hides it. The bytes of a removed file stay
in the commit that added them, because no command changes a commit; space comes
back only after `workbook compact` moves the task to a checkpoint that does not
carry them and Git prunes the old commits.

## Relics

A relic is a record for material that is longer or more structured than a
task: a spec, a plan, a decision record. It has a title, a type, properties
and an ordered list of parts. A part has a type, fields and, optionally, a
body. A property or a field is a named JSON value. A body is a file's bytes,
stored exactly as given, with an optional format label such as `md`.

The examples in this section are one session, in the order they appear, run
by `alice@example.com` in a project whose key is `WB`, in a clone of a remote
that a second person, `bob@example.com`, clones later. Each command is shown
as it was typed and each output as it was printed. Every command that changes
a relic synchronizes with the remote, as a task command does, and ends with a
`Sync:` line; see [Sharing relics](#sharing-relics). The session starts with a
file `goals.md` that holds two lines of text, and with these three commands:

```sh
workbook relic create "Task archive compaction" --type spec
workbook relic update WB-R-01M87F0S --set status=draft --add-part --part-type section --field heading=Goals --body-file goals.md --add-part --part-type decision --field "heading=Keep a backup bundle" --field-json accepted=true
workbook relic show WB-R-01M87F0S
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction
Author:	alice@example.com
Updated:	2026-11-24T19:02:46Z
Properties:
  status: draft
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 102 bytes, version 01M87F0T8ZACFWH3F27EK4CYVH
  heading: Goals
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
```

**Relics are shared with the tasks.** A relic command that changes a relic, `workbook fetch`,
`push`, `sync`, `setup` and `move-tasks` carry relic refs to and from the
tasks remote, and a fresh clone gets every relic from `workbook setup`. The
synchronization a task command runs carries none; see
[Sharing relics](#sharing-relics). No task command returns a relic:
`workbook list` and `next` answer as they did before relics existed. There
are no schemas, no history or diff commands, no retiring, no compaction and no
web page for relics yet.

**Upgrade every Workbook on the machine before creating relics.** A Workbook
from before relics does not know the refs that hold them. Its `workbook reset
--no-backup` deletes the relic refs with the rest, writes no bundle, and does
not say that it held relics, because it does not count them as work that
exists only here. Its plain `workbook reset` writes them into its bundle, but
the restore command it prints does not fetch them back. A relic the tasks
remote already holds comes back with the next `workbook sync` of this build.
A change to a relic that was not yet published, because it was made with
`--no-sync`, offline, or with `auto-sync` off, can be lost.

Each relic is one ref, `refs/workbook/relics/<relic-id>`, holding a chain of
immutable commits, as a task ref does. No relic command deletes a ref or
changes a commit. See [Relic refs](architecture.md#relic-refs) for the stored
format.

### Relic IDs and part addresses

A relic ID is `<KEY>-R-<ULID>`, where the key is the project's current key
when the relic is created: `WB-R-01M87F0SQYHP3HCR4QEN3RTVEX`. Its grammar is
`^[A-Z][A-Z0-9]{1,9}-R-[0-7][0-9A-HJKMNP-TV-Z]{25}$`. A relic ID is never a
task ID. A task command given a relic ID, and a relic command given a task ID,
both answer `not-found` (exit `4`).

A part ID is an uppercase ULID, `01M87F0T8ZACFWH3F27EK4CYVH`. It is the ID of
the operation that added the part, and it never changes.

A part address is `<relic-id>#<part-id>`:
`WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH`.

Any unambiguous prefix of a relic ID or of a part ID works wherever one is
taken, on either side of the `#`, and letter case is ignored. A prefix that
matches nothing is `not-found` (exit `4`). A prefix that matches more than one
is `validation` (exit `5`), and the message says how many it matches and to
use more characters. The first ten characters of a ULID are its time, so IDs
made by one command usually share them. The two parts added above do, and ten
characters do not tell them apart:

```sh
workbook relic show WB-R-01M87F0S#01M87F0T8Z
```

```text
workbook: part prefix "01M87F0T8Z" matches 2 parts of WB-R-01M87F0SQYHP3HCR4QEN3RTVEX; use more characters
```

Quote an address in a shell that gives `#` a meaning inside a word, as zsh
does with `EXTENDED_GLOB` set.

### Creating a relic

The session's first command printed one line:

```sh
workbook relic create "Task archive compaction" --type spec
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction
Sync:	completed
```

The output is the relic's ID, type and title, separated by tabs. The relic
starts with no properties and no parts. The title and `--type` are both
required. The title must be valid UTF-8, is trimmed, must not be blank, and is
at most 500 bytes.

A relic type and a part type are lowercase names:
`^[a-z0-9]+(-[a-z0-9]+)*$`, at most 100 bytes. That is lowercase ASCII letters
and digits, in words joined by single hyphens, such as `spec`, `plan` or
`decision-record`. Workbook gives no type a meaning yet. Any other name is
refused with exit `5`:

```sh
workbook relic create "Too loud" --type "Design Doc"
```

```text
workbook: the relic type "Design Doc" must be lowercase letters and digits, with single hyphens between words
```

`relic create --from-file <file>` creates a relic with its parts from one
document instead. See [The relic document](#the-relic-document).

### Changing a relic

`workbook relic update <relic>` makes every change to a relic. One command
takes any number of changes:

| Change | Options |
| --- | --- |
| Set the title | `--title <title>` |
| Set a property | `--set <name>=<value>` for text, `--set-json <name>=<json>` for any JSON value |
| Remove a property | `--clear <name>` |
| Add a part | `--add-part --part-type <type>`, then any of `--field`, `--field-json`, `--body-file` with `--body-format`, and `--before <part>` or `--after <part>` |
| Change a part | `--part <part>`, then at least one of `--field`, `--field-json`, `--clear-field <name>`, and `--body-file` with `--body-format` and `--base <version>` |
| Move a part | `--move-part <part>`, then `--before <part>` or `--after <part>` |
| Remove or restore a part | `--remove-part <part>`, `--restore-part <part>` |

The session's second command set a property and added two parts, the first
with a body:

```sh
workbook relic update WB-R-01M87F0S --set status=draft --add-part --part-type section --field heading=Goals --body-file goals.md --add-part --part-type decision --field "heading=Keep a backup bundle" --field-json accepted=true
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F0T8ZACFWH3F27EK4CYVH
part	01M87F0T8ZDXPZV8WNS006CBKV	decision	-
Sync:	completed
```

After the relic's line, the output has one `part` line for each part the
command added or edited: the part's ID, its type, and its body version, or `-`
when it has no body.

**Groups.** `--add-part`, `--part`, `--move-part`, `--remove-part` and
`--restore-part` each start a group, and the part options typed after one
belong to it until the next group starts. `--title`, `--set`, `--set-json` and
`--clear` always act on the relic, wherever they are typed, even between a
group's options. `--part` takes a part ID, a prefix, or a whole part address
of the relic being changed. Every other part option (`--move-part`,
`--remove-part`, `--restore-part`, `--before` and `--after`) takes a part ID
or a prefix, not an address.

**All or nothing.** The changes apply in the order given, and each one sees
the ones before it. They are recorded together as one commit, or, when any one
of them is refused, none is recorded.

**One name, once.** One command may name a given property once, and a given
field of a given part once. Naming one twice is refused with exit `5` and
nothing is written, whatever the combination: a set and a clear, two sets or
two clears, `--set` with `--set-json`, or `--field` with `--field-json` or
`--clear-field`.

```sh
workbook relic update WB-R-01M87F0S --set status=approved --clear status
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZD --field accepted=yes --field-json accepted=true
```

```text
workbook: this update sets and clears property "status"; name a property once in one update
workbook: this update sets field "accepted" of part 01M87F0T8ZDXPZV8WNS006CBKV twice; name a field once for each part in one update
```

**Names.** A property name or a field name is 1 to 100 bytes of valid UTF-8.
It must not contain `=`, a control character or a format character, and it
must not begin or end with white space. A control character is one in
Unicode's category Cc: U+0000 to U+001F, U+007F, and U+0080 to U+009F. A
format character is one in category Cf, which nothing shows: U+200B ZERO WIDTH
SPACE, U+FEFF ZERO WIDTH NO-BREAK SPACE, U+00AD SOFT HYPHEN and the direction
marks and overrides such as U+202E among them. White space is a character with
Unicode's `White_Space` property, which includes the no-break space. White
space inside a name is allowed unless it is a control character, as a tab is:
`review status` is a name. Names are compared byte for byte. Letter case
matters, so `Status` and `status` are two properties. Workbook does not
normalize Unicode, so one accented letter written two ways makes two names. A
name that breaks a rule is refused with exit `5`, and a name over 100 bytes is
reported by its size and not repeated.

**Values.** `--set` and `--field` store their value as text, a JSON string.
`--set-json` and `--field-json` store any one JSON value: a number, `true`,
`null`, a list, an object. The name ends at the first `=`, so a value may
contain more of them. A value is refused with exit `5` when it is not valid
UTF-8, when it escapes half of a surrogate pair, when an object in it names a
member twice, or when it nests lists and objects more than 32 levels deep. A
number or a string is at depth 0, and `[1]` is at depth 1. Nothing is replaced
or dropped to make a value fit: the value stored is the value given, or the
command is refused.

A value is stored in one canonical form: object members are sorted by name, a
number keeps the text it was written with, and inside a string `<`, `>` and
`&` are written as the six-byte escapes `\u003c`, `\u003e` and
`\u0026`. Those escapes mean the same characters. `relic show` prints a
string value's characters. It prints any other value as it is stored, on one
line, so a string inside a list or an object keeps its escapes, and `null`
prints as `null`. `relic export` and `--json` carry every value as it is
stored. A value read back and stored again does not change.

```sh
workbook relic update WB-R-01M87F0S --title "Task archive compaction, second draft" --set "owner=Alice <alice@example.com>" --set-json 'reviewers=["Alice <alice@example.com>","bob@example.com"]' --set-json approved=null
workbook relic show WB-R-01M87F0S
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:02:48Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: draft
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 102 bytes, version 01M87F0T8ZACFWH3F27EK4CYVH
  heading: Goals
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
```

**No change.** A change that would leave the relic as it is records nothing.
When no change in a command records anything, the command exits `0`, prints
`No change: <relic-id>`, and reports `"changed": false` in JSON, so a repeated
command is safe. Saving a body is the exception: it always records a new body
version, even when the bytes are the same.

```sh
workbook relic update WB-R-01M87F0S --set status=draft
```

```text
No change: WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
Sync:	completed
```

**Bodies.** `--body-file <file>` saves the file's bytes as the part's body.
Only a regular file is read. Standard input is not: `--body-file -` is refused
with exit `2`. A file over 1 MiB is refused with exit `5` before it is read.

A body has a format label, or none:

- A part's first body takes its label from the file's name: the text after
  its last `.`, lowercased. `goals.md` and `Notes.MD` both give `md`, and a
  name with no `.` gives no label. That holds for `--add-part`, and for
  `--part` when the part has no body yet. An ending that cannot be a label is
  refused with exit `5`, and the message says to pass `--body-format`.
- A save over a body keeps the label the body has, whatever the file is
  called.
- `--body-format <label>` sets the label in either case, and an empty one,
  `--body-format ""`, removes it.

A label is at most 16 bytes of `^[a-z0-9][a-z0-9.+-]*$`: lowercase ASCII
letters, digits, `.`, `+` and `-`, starting with a letter or a digit. A label
given with `--body-format` is not lowercased, so `--body-format MD` is refused
with exit `5`. Workbook gives no label a meaning yet. `relic show` prints a
body with no label as `txt`; in JSON such a body has no `format` member.
Saving over a body that exists needs `--base`; see
[Body versions](#body-versions).

**Refusals.** A command line that is put together wrongly is
`invalid-invocation` (exit `2`), and nothing is read or judged before it is
refused: a part option before any group, an option its group does not take,
`--add-part` without `--part-type`, `--part` with no change after it,
`--move-part` without `--before` or `--after`, `--body-format` or `--base`
without `--body-file` in the same group, or no change at all. A deleted relic
cannot be changed until it is restored (exit `5`).

### Listing relics

The session creates a second relic, so there are two to list:

```sh
workbook relic create "Release checklist" --type plan
workbook relic list
```

```text
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	2 parts	Task archive compaction, second draft
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist
```

Each line of `relic list` is the relic's ID, its type, how many live parts it
has, and its title, separated by tabs. Relics are listed in ID order. Under
one project key that is the order they were created in, to the millisecond. A
project that has had more than one key lists its relics key by key, in the
keys' own order, so a relic created under the key `AB` comes before every
`WB` relic, whenever it was created. `--type <type>` keeps the relics of
exactly that type. Deleted relics are left out unless `--all` is given, which
marks each deleted relic with one more column, `deleted`; see
[Deleting and restoring a relic](#deleting-and-restoring-a-relic).

`relic list --json` carries a list of relics, each with `id`, `head`, `title`,
`type`, `author`, `retired`, `deleted`, `createdAt`, `updatedAt` and `parts`,
the number of live parts. `head` is the commit the relic's ref points at.

`relic list` reads every relic. One relic whose stored data is damaged fails
the whole listing, with exit `7` and a message that names that relic; see
[A damaged relic](#a-damaged-relic).

### Showing a relic

`workbook relic show <relic>` prints the relic, its properties, and each live
part with its fields, as above. A part's line is `#` and its ID, its type, and
its body's format, size and version, or `no body`. Properties and fields are
printed in name order, a string as its characters and any other value as it is
stored. Times are UTC.

`--body` adds each body's text under its part. `--removed` adds removed parts,
each marked `removed`. Positions are not printed; `--json` carries them.

A part address shows the relic's own lines and that one part, removed or not:

```sh
workbook relic show WB-R-01M87F0S#01M87F0T8ZA
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:02:48Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: draft
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 102 bytes, version 01M87F0T8ZACFWH3F27EK4CYVH
  heading: Goals
```

A part address with `--body`, and without `--json`, writes only that part's
body. A part with no body is `not-found` (exit `4`) there.

```sh
workbook relic show WB-R-01M87F0S#01M87F0T8ZA --body
```

```text
Finished tasks keep every commit they ever had.
Compaction replaces that history with one checkpoint.
```

Where that body goes decides how it is written:

- **Into a file or a pipe**, the body is written byte for byte, so it can be
  redirected and saved again.
- **On a terminal**, the body is printed for reading with its layout kept:
  spaces, tabs, indentation and blank lines stay as written, so nested lists
  and code blocks keep their shape. A Windows line end becomes one line break,
  and every other control character, a lone carriage return included, becomes
  a space, as do the bidi controls described under
  [Text-mode output safety](#text-mode-output-safety), so a body cannot move
  the cursor, color the screen or retitle the window. The full `relic show
  --body` prints each text body the same way, under an indent of four spaces. A body that is not valid UTF-8 is not written
  to a terminal at all: one line says so, gives its size, and says to send
  the output to a file or a pipe.

Every other line of text output passes through the same filter, so `--json`
is the interface for exact values. `relic show --json` carries
[the relic document](#the-relic-document), with a body's content only under
`--body`. A body that is not valid UTF-8 is not printed in a whole relic's
text output, which says so in its place, and JSON carries it as `base64`.

### A damaged relic

`relic show`, `relic export`, `relic update`, `relic delete` and
`relic restore` each check the relic's newest commit against its parent before
they answer. A newest commit whose stored relic or whose bodies do not follow
from its operations is `corrupt-data` (exit `7`). `relic list` reads each
relic's newest commit without that check, and refuses one that is not the
bytes Workbook writes.

Every message about one relic's stored data begins `relic <relic-id>: ` and
says the cause once. From the five commands above it ends with a pointer to
`workbook validate`, which checks the whole history and names the commit.
Workbook writes no such relic; the one below had its newest `state.json`
rewritten with Git alone, after the session:

```sh
workbook relic show AB-R-01M87F4VC3D1ZDFRBP3M6YCB7Y
```

```text
workbook: relic AB-R-01M87F4VC3D1ZDFRBP3M6YCB7Y: relic state is not canonical; run "workbook validate" for details
```

A relic whose newest commit, or that commit's parent, names an object Git does
not have is reported the same way. These commands do not look further back
than the parent; `workbook validate` does.

### Body versions

Every saved body has a version: the ID of the operation that saved it. The
first body of a part that was added with one has the part's own ID as its
version. `relic show` prints the version, and `relic update` prints the new
one after a save.

To save over a body, say which version the edit started from, with `--base
<version>`. The save is recorded only when that is the part's current body
version. The version is written out in full: a prefix is not accepted here.
Letter case is ignored, as it is for every ID. The session adds a third line
to `goals.md` and saves it over the body it showed above:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md --base 01M87F0T8ZACFWH3F27EK4CYVH
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F0ZKD0X1MC06E1JCG5QBY
Sync:	completed
```

The body is still labeled `md`: a save over a body keeps its label.

A save from any other version is refused as `stale-write` (exit `6`), and
nothing is written. Repeating the same command, with the base it has just
replaced:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md --base 01M87F0T8ZACFWH3F27EK4CYVH
```

```text
workbook: part 01M87F0T8ZACFWH3F27EK4CYVH body is at version 01M87F0ZKD0X1MC06E1JCG5QBY, not 01M87F0T8ZACFWH3F27EK4CYVH; read it again with "workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body" and save from that version
```

With `--json` the same refusal is the error envelope:

```json
{"format":"workbook.error","version":1,"error":{"category":"stale-write","message":"part 01M87F0T8ZACFWH3F27EK4CYVH body is at version 01M87F0ZKD0X1MC06E1JCG5QBY, not 01M87F0T8ZACFWH3F27EK4CYVH; read it again with \"workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body\" and save from that version"}}
```

`--base` is required whenever the part has a body, and the refusal names the
version to pass:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md
```

```text
workbook: part 01M87F0T8ZACFWH3F27EK4CYVH body is at version 01M87F0ZKD0X1MC06E1JCG5QBY; pass --base 01M87F0ZKD0X1MC06E1JCG5QBY to save over it
```

`--base` given for a part that has no body is refused as well:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZD --body-file goals.md --base 01M87F0ZKD0X1MC06E1JCG5QBY
```

```text
workbook: part 01M87F0T8ZDXPZV8WNS006CBKV has no body, so there is no version 01M87F0ZKD0X1MC06E1JCG5QBY to start from
```

**These three refusals are exit `6`, and none of them is got past by running
the same command again**: a base that is not the current version, no base
when the part has a body, and a base when the part has none. Each fails the
same way until the command changes. Read the part again, with `relic show
<relic>#<part>` for its version and `--body` for its text, carry the change
over to that text, and save with the new version as the base. Fields and
properties have no versions and take no base.

**One exit `6` is got past by running the same command again**: two commands
changing one relic at the same moment. Each started from the same newest
commit, exactly one is recorded, and the other is refused with nothing
written. Workbook does not retry it. The refusal names the relic and says
what to do:

```text
workbook: relic ref changed concurrently: another change to relic <relic-id> was saved first, so this one was not; run the command again
```

The refused command succeeds when it is run again, unless it saves a body the
first command also saved, which is then a save from a stale base. Both kinds
of refusal are category `stale-write` and carry no other member in JSON, so a
script tells them apart by the message: only the one a retry gets past begins
`relic ref changed concurrently`.

### Part order

Parts are kept in order by position. A position is an exact fraction written
`numerator/denominator` in lowest terms, such as `3/2`, as a task's rank is.
Workbook chooses positions; no option takes one. Text output lists parts in
order without printing positions, and JSON carries each part's `position`.

- A relic created from a document places its parts in document order, at
  `1/1`, `2/1` and so on.
- `--add-part` with no `--before` or `--after` puts the part after every part
  the relic holds, removed ones included.
- `--add-part` or `--move-part` with `--before <part>` or `--after <part>`
  puts the part directly beside that part, at the fraction with the smallest
  denominator in the gap. It looks at live parts only.

```sh
workbook relic update WB-R-01M87F0S --add-part --part-type section --field heading=Non-goals --before 01M87F0T8ZD --json
```

```json
{"format":"workbook.result","version":1,"command":"relic update","data":{"relic":{"id":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","head":"c5ed20941945d1b370927f125911e5909890c80c","title":"Task archive compaction, second draft","type":"spec","author":"alice@example.com","retired":false,"deleted":false,"createdAt":"2026-11-24T19:02:45.75869Z","updatedAt":"2026-11-24T19:02:54.052091Z","parts":3},"parts":[{"id":"01M87F11V42NP3MEYSJ017ZDDA","type":"section","position":"3/2","fields":{"heading":"Non-goals"}}],"changed":true},"sync":{"enabled":true,"source":"default","status":"completed","fetch":{"remote":"origin","status":"completed","tasks":[]},"pushRelic":{"relicId":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","status":"published"}}}
```

The new part went between `1/1` and `2/1`, at `3/2`, so it is now the second
of three.

`--move-part` needs `--before` or `--after`; without one it is
`invalid-invocation` (exit `2`). Both together, after `--move-part` or after
`--add-part`, are `invalid-invocation` too (exit `2`). A part cannot be placed beside itself, and the part named by `--before` or
`--after` must be live. Moving a part to where it already is changes nothing.

Because a placement looks at live parts only, a new or moved part can be given
the position a removed part holds. If that part is restored later, the two
share a position and are ordered by part ID. Moving either one beside the
other gives them separate positions again. Until then, a third part can be
placed between the two only when its ID sorts between theirs; otherwise the
command is refused with exit `5` and says to move one of them first.

A position is at most 256 bytes. Placing a part into the same shrinking gap
again and again makes positions longer, and a placement that would need a
longer one is refused with exit `5`, saying to place the part somewhere else
or to move a neighboring part first.

### Removing and restoring parts

```sh
workbook relic update WB-R-01M87F0S --remove-part 01M87F11V42
workbook relic show WB-R-01M87F0S --removed
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:02:54Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: draft
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 146 bytes, version 01M87F0ZKD0X1MC06E1JCG5QBY
  heading: Goals
#01M87F11V42NP3MEYSJ017ZDDA	section	no body	removed
  heading: Non-goals
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
```

`--remove-part` marks a part removed. The part keeps its ID, fields, body and
position, and no stored commit changes, so removing a part frees no space.
A removed part is left out of `relic show` and `relic export`. `relic show
--removed` lists it where its position puts it, and its own part address shows
it. It cannot be edited or moved, and no part can be placed beside it, until
it is restored:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F11V42 --field heading=Later
```

```text
workbook: part 01M87F11V42NP3MEYSJ017ZDDA was removed; restore it first with --restore-part 01M87F11V42NP3MEYSJ017ZDDA
```

`--restore-part` brings it back at the position it had. Removing a removed
part, or restoring a live one, changes nothing.

```sh
workbook relic update WB-R-01M87F0S --restore-part 01M87F11V42
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
```

### Deleting and restoring a relic

```sh
workbook relic delete WB-R-01M87F0Y
workbook relic list --all
```

```text
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	3 parts	Task archive compaction, second draft
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist	deleted
```

`relic delete` marks the relic deleted and prints the relic's line, as `relic
create` does. Its ref and its whole history stay. A deleted relic is left out
of `relic list` unless `--all` is given. It can still be shown and exported,
and `relic show` prints `Deleted: yes`. It cannot be changed until `relic
restore` brings it back:

```sh
workbook relic update WB-R-01M87F0Y --title "Edited while deleted"
workbook relic restore WB-R-01M87F0Y
```

```text
workbook: relic WB-R-01M87F0YHZ1852KPS55KCDPXG2 is deleted; restore it first with "workbook relic restore WB-R-01M87F0YHZ1852KPS55KCDPXG2"
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
```

Deleting a deleted relic, or restoring a live one, changes nothing and prints
`No change: <relic-id>`.

### The relic document

`workbook relic export <relic>` writes the whole relic as one JSON document of
format `workbook.relic`, with its live parts in order and their bodies:

```sh
workbook relic export WB-R-01M87F0S
```

```json
{
  "format": "workbook.relic",
  "version": 1,
  "id": "WB-R-01M87F0SQYHP3HCR4QEN3RTVEX",
  "head": "3e680ce8bdf534b0a12ddb4002e693c3894a9369",
  "title": "Task archive compaction, second draft",
  "type": "spec",
  "schema": null,
  "author": "alice@example.com",
  "retired": false,
  "deleted": false,
  "createdAt": "2026-11-24T19:02:45.75869Z",
  "updatedAt": "2026-11-24T19:02:56.218696Z",
  "properties": {
    "approved": null,
    "owner": "Alice \u003calice@example.com\u003e",
    "reviewers": [
      "Alice \u003calice@example.com\u003e",
      "bob@example.com"
    ],
    "status": "draft"
  },
  "parts": [
    {
      "id": "01M87F0T8ZACFWH3F27EK4CYVH",
      "type": "section",
      "position": "1/1",
      "fields": {
        "heading": "Goals"
      },
      "body": {
        "format": "md",
        "version": "01M87F0ZKD0X1MC06E1JCG5QBY",
        "bytes": 146,
        "text": "Finished tasks keep every commit they ever had.\nCompaction replaces that history with one checkpoint.\nThe old history is saved to a bundle first.\n"
      }
    },
    {
      "id": "01M87F11V42NP3MEYSJ017ZDDA",
      "type": "section",
      "position": "3/2",
      "fields": {
        "heading": "Non-goals"
      }
    },
    {
      "id": "01M87F0T8ZDXPZV8WNS006CBKV",
      "type": "decision",
      "position": "2/1",
      "fields": {
        "accepted": true,
        "heading": "Keep a backup bundle"
      }
    }
  ],
  "links": []
}
```

Without `--json` the output is the document alone, indented, and nothing else.
In it a string holding `<`, `>` or `&`, such as a title or a body's text, has
those characters as they are written. A property or field value is written as
it is stored, so `<`, `>` and `&` inside one keep their six-byte escapes, as in
the `owner` property above. With `--json` the document is the `data` of the
usual result envelope, on one line, where every string writes `<`, `>` and `&`
as `\u003c`, `\u003e` and `\u0026`, as all JSON output does. `relic show --json` writes the same document, for the parts it was asked
for, with body content only under `--body`.

| Member | Value |
| --- | --- |
| `format`, `version` | `"workbook.relic"` and `1`. |
| `id`, `head` | The relic's ID, and the commit its ref points at. |
| `title`, `type` | As stored. |
| `schema` | Always `null`. |
| `author` | Who created the relic. |
| `retired` | Always `false`. |
| `deleted` | Whether the relic is deleted. |
| `createdAt`, `updatedAt` | RFC 3339 times, in the offset they are stored with. Workbook records UTC, written with `Z`; a relic written by another tool may carry another offset, which is kept. `updatedAt` is the time of the newest commit. The `Updated:` line of `relic show` text gives it in UTC. |
| `properties` | An object of the relic's properties. |
| `parts` | The parts, in order. Each has `id`, `type`, `position`, `fields`, `removed` only when it is `true`, and `body` when the part has one. |
| `parts[].body` | `format` when the body has a label, `version`, `bytes`, and the content as `text`, or as `base64` when the bytes are not valid UTF-8. An empty body is `"text": ""`. |
| `links` | The relic's live links, ordered by ID: each with `id`, `from` when it is from a part, `to` as `{"task": "<task-id>"}`, and `rel`. `relic show` lists only the links of the parts it shows, and adds `targetRemoved: true` to a link whose task is deleted or not in this clone. See [Links to tasks](#links-to-tasks). |

`workbook relic create --from-file <file>` creates a relic from such a
document, and `-` reads it from standard input:

```sh
workbook relic export WB-R-01M87F0S | workbook relic create --from-file - --type plan
```

```text
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	Task archive compaction, second draft
part	01M87F17QTQAFWG6DJCPQSCWZQ	section	01M87F17QTQAFWG6DJCPQSCWZQ
part	01M87F17QTVCY2H4X9Z600ZMG6	section	-
part	01M87F17QTTXNR7433BG90Z5M2	decision	-
Sync:	completed
```

The new relic is a new record. It gets a new ID, its parts get new IDs, and
the first body of each part gets a new version. The whole relic is recorded as
one commit.

- **Read:** `format` and `version`, which must be `workbook.relic` and `1`;
  `title`, `type` and `properties`; and each part's `type`, `fields`, and
  body `format` with exactly one of `text` and `base64`.
- **Ignored:** `id`, `head`, `author`, `retired`, `deleted`, `createdAt` and
  `updatedAt`; each part's `id`, `position` and `removed`; and each body's
  `version` and `bytes`. Parts are placed in the order the document lists
  them, and every one is live.
- **Refused** (exit `5`), with nothing created:
  - a document of more than 67,108,864 bytes (64 MiB), from a file or from
    standard input, which is not read past that size;
  - text that is not valid UTF-8, anywhere in the document, or a `\u` escape
    for half of a surrogate pair. Nothing is replaced: a title, a name, a
    value or a body's `text` is stored as it was written, or the document is
    refused. The message says where, as in `title` or `parts[0].body.text`
    with parts counted from 0, and does not repeat the bytes. A body that is
    not UTF-8 text goes in the body's `base64` member;
  - an object that names a member twice, at any depth: the document, a part,
    a body, `properties`, one part's `fields`, or an object inside a value;
  - a member the format does not have. Member names are matched exactly, so
    `"Title"` is not `"title"`;
  - a `schema` that is not `null`, and a `links` list that is not empty: the
    new relic's parts get new IDs, so add each link afterwards with
    `relic update --link`;
  - a body with both `text` and `base64`, or with neither.

A title given first on the command line, or `--type`, replaces the document's.
Both may be left out when the document has them. A document with no `type`,
given no `--type`, is refused with exit `5` and told to add one or to pass
`--type`. Every value is checked as `relic update` checks it, and a document
that fails any check creates nothing. A file that does not exist is
`not-found` (exit `4`).

The file `--from-file` reads is the document, not a command's JSON result.
`relic export --json` writes a result, and passing that file is refused with a
message that says so:

```sh
workbook relic export WB-R-01M87F0S --json > result.json
workbook relic create --from-file result.json
```

```text
workbook: this is the JSON result of a command, not a relic document: it has "command" and "data" members; "workbook relic export <relic>" without --json writes the document, and the document is the result's "data" member
```

### What a relic may hold

| Limit | Value | Measured as | Checked |
| --- | --- | --- | --- |
| Title | 500 bytes | The title after surrounding whitespace is trimmed. | When saved and when read |
| Relic type, part type | 100 bytes each | The name. | When saved and when read |
| Property name, field name | 100 bytes each | The name. | When saved and when read |
| Body format label | 16 bytes | The label. | When saved and when read |
| Part position | 256 bytes | The `numerator/denominator` text. | When saved and when read |
| One property value | 65,536 bytes (64 KiB) | The value as stored JSON. | When saved and when read |
| One field value | 16,384 bytes (16 KiB) | The value as stored JSON. | When saved and when read |
| Nesting inside one value | 32 levels | Lists and objects inside one another. | When saved and when read |
| Body | 1,048,576 bytes (1 MiB) | One part's body. | When saved and when read |
| Properties together | 65,536 bytes (64 KiB) | The relic's properties object as stored JSON. | Only when a change makes the total larger |
| Fields of one part together | 16,384 bytes (16 KiB) | The part's stored fields document, with the few bytes that wrap it. | Only when a change makes the total larger |
| Live parts | 2,000 | Parts that are not removed. | Only when a change adds or restores a part |
| Parts | 4,000 | Parts, counting removed ones. | Only when a change adds a part |
| Live bodies | 16,777,216 bytes (16 MiB) | The bodies of the live parts, added together. | Only when a change makes the total larger |
| Link relationship | 100 bytes | The `rel`, lowercase words joined by single hyphens. | When saved and when read |
| Live links | 2,000 | Links that are not removed. | Only when a change adds a link |
| Git object | 4,194,304 bytes (4 MiB) | Any one stored object. | Never stored larger; never read larger |

A value's size is the size of its stored form, with its quotes and its
escapes. Each `<`, `>` or `&` in a string is stored as a six-byte escape and
counts six bytes, so text of 11,000 `<` characters is a 66,002-byte value and
is refused as one property value.

Sizes are measured on what the whole command would leave stored, not after
each change in it. A command that goes over a limit is refused as `validation`
(exit `5`), with the size and the limit in the message, and writes nothing.
Two 40,000-byte properties set in one command, each within the limit for one
value, are refused together:

```text
workbook: the relic's properties are 80167 bytes; the limit is 65536 bytes
```

The first nine limits, and the link relationship, are also checked when what
was stored is read back. A
stored value over one was not written by these commands, and its relic is
reported as `corrupt-data` (exit `7`).

The two totals for properties and fields, the three counts and the total for
bodies are different: they are never checked when a relic is read, and they
refuse only growth. A change is refused for one only when it leaves the total
over its limit and larger than it was. A relic that is already over one can
still be read and edited, and brought back under: clearing a property,
setting a smaller value, removing a part or removing a link is always saved.

`relic create --from-file` also reads at most 67,108,864 bytes (64 MiB) of
document. That is a limit on one command's input, not on what is stored. The
export of a relic of ordinary text at every stored limit fits under it. A text
body is a JSON string in the document, though, where a control character other
than a tab or a line break takes six bytes, so a relic whose bodies are mostly
such characters can export to more, and `--from-file` refuses that document
with exit `5`.

Treat the numbers in the table as part of the storage format, as the task
limits are. One can be raised later. None can be lowered, because a relic
already stored at the old size would stop reading.

### Relics in other commands

**`workbook validate`** checks every commit of every relic, from the commit
that created it to its newest, and reports the result on a line of its own.
After the commands above:

```text
Validated 3 relic(s): 11 commit(s) checked; 3 valid, 0 invalid.
```

It also refuses a relic in which one operation ID is recorded by two commits
of that relic's history, which no command that reads one relic looks for, and
it reports a relic whose history names an object Git does not have as that
relic's failure, with every other relic and every task still reported. A
failure is one line that names the relic, the first commit that failed, and
the cause. For the damaged relic under [A damaged relic](#a-damaged-relic):

```text
Invalid AB-R-01M87F4VC3D1ZDFRBP3M6YCB7Y at 5a6cb7552a43b43efd4b7da6df92244e8d81a0b1 [corrupt-data]: relic state is not canonical
```

See [Semantic history validation](#semantic-history-validation) for what is
checked and for the `relics` member of the JSON result.

**`workbook reset`** measures a relic as it measures a task. A relic whose
every commit the tasks remote holds is published, and only a change it lacks
is work that exists only here, listed on an `Unpublished:` line as `relic <id>
"<title>"` with how many operations it holds. A parked relic tip, which holds
the text a [relic conflict](#a-relic-conflict) did not apply, is listed on a
`Set aside:` line. A reset writes the relic refs and the parked relic tips
into its backup bundle before it removes them, and the `Restore:` line it
prints fetches `'refs/workbook/relics/*:refs/workbook/relics/*'` back from
that bundle, and the parked tips as well when there were any. A relic ref
that is damaged but still names a commit is kept the same way, because a sync
never publishes a relic it cannot read. `--no-backup` is refused with exit `5`
while such work exists, as it is for a task; see
[Work that exists only here](#work-that-exists-only-here).
`workbook reset --remote` deletes the relic refs at a remote with the rest of
its Workbook refs, after writing them to its bundle.

**Task commands** do not return relics. `workbook list` and `next` read no
relic, `workbook show <task>` lists the relics that link to the task (see
[Links to tasks](#links-to-tasks)), and `workbook compact` leaves relic refs alone. `workbook fetch`,
`push`, `sync` and `move-tasks` carry them; see [Sharing relics](#sharing-relics).
The synchronization a task command runs before and after its change fetches
and pushes no relic ref, so it meets no relic conflict. The `Relics:` section
of `workbook show <task>` lists the relics this checkout holds, so it is as
fresh as the last command here that fetched relics: a relic command that
changes a relic, `workbook fetch`, `workbook sync` (including a running
`sync --watch`), `workbook setup` or `workbook move-tasks`.
A task command handed to a running watcher reports the relic conflicts the
watcher recorded, with the same `relicConflict` member a relic command uses.

**A newer Workbook.** The relic formats carry the same `minReader` marker task
histories do; see [Mixed Workbook versions](architecture.md#mixed-workbook-versions).
A relic whose newest commit was written by a newer Workbook than this one can
still be shown, as far as this build can read it. `relic update`,
`relic delete` and `relic restore` refuse it as `newer-writer` (exit `9`),
naming the relic and saying to upgrade, and write nothing. `workbook validate`
counts it under `newerWriter` rather than as invalid.

### Sharing relics

Relics travel with the tasks, to and from the tasks remote: `origin`, unless
the project's settings name another (see
[A tasks remote other than origin](#a-tasks-remote-other-than-origin)).
`workbook fetch`, `push` and `sync` carry relic refs as they carry task refs,
and so do a relic command that changes a relic, a running `workbook sync --watch`, the board's
own loop under `workbook serve`, the pre-push hook, `workbook setup` and
`workbook move-tasks`. The synchronization a task command such as `create`,
`update` or `next` runs before and after its change does not: it fetches and
pushes the task refs and the project's settings only, so its cost does not
grow with the number of relics. A relic another clone published arrives here
with the next relic command that changes a relic, `workbook fetch` or `workbook sync`, and a relic
recorded here with `--no-sync` goes out with the next `workbook push`,
`workbook sync` or relic command that changes it. A fresh clone has every
relic once `workbook setup` has run there:

```sh
workbook relic list
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	3 parts	Task archive compaction, second draft
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	3 parts	Task archive compaction, second draft
```

That is bob's clone. Each relic is fetched to a tracking ref beside the
task tracking refs, `refs/workbook/remotes/origin/relics/<relic-id>`, and from
there to the relic's own ref:

```sh
git for-each-ref refs/workbook/relics/ refs/workbook/remotes/origin/relics/
```

```text
3e680ce8bdf534b0a12ddb4002e693c3894a9369 commit	refs/workbook/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
00fc4f61eb62c288c79d3c9cef8ba71d3416340a commit	refs/workbook/relics/WB-R-01M87F0YHZ1852KPS55KCDPXG2
a112b0c1b8059dfafa88aefc65ad45d447da7426 commit	refs/workbook/relics/WB-R-01M87F17QTWKQKT6MZWX54HFY8
3e680ce8bdf534b0a12ddb4002e693c3894a9369 commit	refs/workbook/remotes/origin/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
00fc4f61eb62c288c79d3c9cef8ba71d3416340a commit	refs/workbook/remotes/origin/relics/WB-R-01M87F0YHZ1852KPS55KCDPXG2
a112b0c1b8059dfafa88aefc65ad45d447da7426 commit	refs/workbook/remotes/origin/relics/WB-R-01M87F17QTWKQKT6MZWX54HFY8
```

`relic create`, `relic update`, `relic delete` and `relic restore` synchronize
as a task command does, and their fetch carries relics as well as tasks. Each
fetches first, records its change, and pushes the one relic ref it moved, and its output ends with the `Sync:` line every
synchronizing command prints; with `--json` the result carries it as `sync`.
`--no-sync`, or the setting `auto-sync` turned off, skips both: the change is
recorded in this checkout only, no `Sync:` line is printed, and the next
`workbook sync`, `push`, or relic command that changes the relic publishes it. While a
watcher is running, a change is handed to it to publish, as a task change is;
see [Continuous synchronization](#continuous-synchronization). `relic list`,
`relic show` and `relic export` never contact the remote: they read what this
checkout holds.

```sh
workbook relic update WB-R-01M87F0S --set reviewed=true --no-sync
workbook push
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	published
```

A fetch, a push and a sync list one line for each relic they changed or could
not change, after the task lines, in the same form: the relic's ID, its
status and, when there is one, a detail. A relic that did not change is not
listed. A fetch reports `created`, `fast-forwarded`, `reconciled`,
`conflicted`, `needs-upgrade` or `invalid`, and a push reports `published`,
`rejected` or `invalid`. Here alice fetches what bob just pushed:

```sh
workbook fetch
```

```text
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	fast-forwarded
```

**What a sync reads.** A relic whose ref and tracking ref name the same commit
has not changed, and nothing of it is read. For a changed relic, a sync reads
the commit, its operations, its state and the sizes of its parts, and checks
the fetched tip as `relic show` checks a relic's newest commit. It never reads
a body's text. It reads a part's fields only when both sides changed fields of
that part. The only command that reads a body during a sync is the one
reporting a [body conflict](#a-relic-conflict), which reads at most the three
texts the report carries.

**What a sync refuses.** A relic whose tip at the tasks remote fails that check
is reported `invalid` with the reason, and its local ref is not moved. Every
other relic and every task is still synchronized, and the command exits `7`
after everything else has finished. A local relic ref this build cannot read
is reported `invalid` and is neither moved nor pushed. A relic written by a
newer Workbook is taken as it is when this checkout has no change of its own
to replay onto it, and is reported `needs-upgrade`, exit `9`, when it has. A
ref under `refs/workbook/relics/` at the tasks remote whose name is not one of
this project's relic IDs is skipped and reported as an ignored ref, as a
stray name under the task namespace is; see
[Explicit task sharing](#explicit-task-sharing). Its line calls it what it
calls such a task name:

```sh
workbook fetch
```

```text
Ignored:	refs/workbook/relics/not-a-relic	may be another Workbook's task	relic ID "not-a-relic" must be <KEY>-R-<ULID>
	kept on origin; Workbook deletes no ref there.
	deleting a "may be another Workbook's task" ref would destroy a newer Workbook's or another project's history.
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
```

A relic whose documents name another project is skipped the same way.

**What a push does.** Relic refs are pushed in a `git push` of their own, after
the task refs and only when a relic changed. A relic ref is never pushed with
force, deleted at the remote, or moved locally except by compare-and-swap. A
push the remote refuses because it holds newer commits is reported
`rejected`, and the next sync replays this checkout's changes onto them and
pushes again. A relic the last fetch withheld is not pushed at all, because
the remote would refuse it and the next sync would withhold it again rather
than replay onto it. A relic command whose own fetch reported its relic
`invalid` or `needs-upgrade` records the change, skips the push, and reports
`sync.status` `failed` with a detail saying the relic was not pushed and why;
the command still exits `0` with that warning. A `workbook push` compares each
relic with the tip the last fetch left for it: a relic that is only behind
that tip has nothing to publish and is not listed, and one that diverged from
a tip that fails its check is reported `invalid`, says it was not pushed, and
makes the push exit `7`.

`workbook fetch --remote <remote>`, which fills another remote's tracking refs
for inspection, does not fetch relics.

### When two edits meet

Two clones can change one relic before either has the other's change. The
clone that synchronizes second replays its own changes onto what the first one
published: each of its commits is written again on top of the fetched tip,
with the same operations, operation IDs, actor and wall time, and only its
logical clock rewritten. No merge commit is written. Bob set a property with
`--no-sync`, alice set another and published it, and bob then synchronized:

```sh
workbook relic update WB-R-01M87F0S --set reviewer=bob@example.com --no-sync
workbook relic update WB-R-01M87F0S --set status=review
workbook sync
workbook relic show WB-R-01M87F0S | sed -n '/^Properties:/,/^#/p'
git log --format='%h %an %s' -3 refs/workbook/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	reconciled	relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: replayed 1 local change(s); 0 already on origin or changing nothing
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	published
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewed: yes
  reviewer: bob@example.com
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: review
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 146 bytes, version 01M87F0ZKD0X1MC06E1JCG5QBY
a61eb71 Bob replay relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
bd840f6 Alice update relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
5654d72 Bob update relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
```

The first command is bob's and the second alice's. The relic holds both
properties, and bob's change is the commit `replay relic` on top of alice's.
What the replay does depends on what both sides changed. Alice synchronizes
first and bob second:

| What both sides changed | Relic status in bob's clone | Stored result | Reported | Exit |
| --- | --- | --- | --- | --- |
| Different parts, different fields of one part, or different properties | `reconciled` | both changes, even when together they pass a total | nothing, or the warning `relic-over-limit` when a total or a count is passed | `0` |
| The same field or property, to different values | `reconciled` | bob's value; both operations in the history | nothing | `0` |
| The title, or the position of the same part | `reconciled` | bob's value; both operations in the history | nothing | `0` |
| The same body, to the same bytes, when that was all bob changed, or only changes origin already holds | `fast-forwarded` | alice's tip | nothing | `0` |
| The same body, to different bytes | `conflicted` | alice's body; bob's old tip parked | a `relicConflict` entry of type `body` naming the part, with the base, bob's and alice's texts | `8` |
| Alice removed a part, bob edited it | `reconciled` | the part stays removed and holds bob's edit | the warning `relic-edit-on-removed-part` naming the relic and the part | `0` |
| Alice deleted the relic, bob edited it | `conflicted` | deleted; bob's old tip parked | a `relicConflict` entry of type `tombstone` | `8` |
| Alice edited the relic, bob deleted it | `reconciled` | deleted, holding alice's edit | nothing | `0` |

These are guarantees. No stored value is ever half of one side's: every body,
property, field and title is one side's whole value or the result of applying
whole operations in order, and no conflict marker is written anywhere. Each
operation is recorded once, whatever the order the clones synchronize in, and
a sync repeated, retried after it was interrupted, or run on the same commits
in two clones, ends in the same state.

**A deletion that synchronizes second applies without a word.** When alice
edits a relic and bob deletes it, bob's sync reports the relic `reconciled`
and nothing more, and the relic is deleted in both clones. This is what
happens to a task too. Alice's edit is not lost: it is in the relic's history,
and `relic restore` brings the relic back with it. Only the opposite order, a
deletion that reached the remote first, is a conflict. Here bob deleted the
third relic with `--no-sync`, alice changed it, and bob synchronized:

```sh
workbook relic delete WB-R-01M87F17 --no-sync
workbook relic update WB-R-01M87F17 --set status=approved
workbook sync
workbook relic list --all
workbook relic show WB-R-01M87F17 | sed -n '/^Properties:/,/^#/p'
```

```text
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	Task archive compaction, second draft
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	Task archive compaction, second draft
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F17QTWKQKT6MZWX54HFY8	reconciled	relic WB-R-01M87F17QTWKQKT6MZWX54HFY8: replayed 1 local change(s); 0 already on origin or changing nothing
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
WB-R-01M87F17QTWKQKT6MZWX54HFY8	published
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	3 parts	Task archive compaction, second draft
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	3 parts	Task archive compaction, second draft	deleted
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: approved
#01M87F17QTQAFWG6DJCPQSCWZQ	section	md, 146 bytes, version 01M87F17QTQAFWG6DJCPQSCWZQ
```

**Totals can be passed together.** Each clone checks the totals and counts of
[What a relic may hold](#what-a-relic-may-hold) when it saves, but two changes
that each fit can pass a total together. The replay keeps both and warns with
`relic-over-limit`, giving the size or the count and the limit. The next change
that makes that total larger is refused, as any change past a total is, and a
change that makes it smaller is saved. Bob and alice each set a property of
40,000 bytes on the second relic, bob first and with `--no-sync`, and bob
then synchronized and changed it twice more:

```sh
workbook relic update WB-R-01M87F0Y --set notes="$(head -c 40000 /dev/zero | tr '\0' b)" --no-sync
workbook relic update WB-R-01M87F0Y --set summary="$(head -c 40000 /dev/zero | tr '\0' a)"
workbook sync
workbook relic update WB-R-01M87F0Y --set more=x
workbook relic update WB-R-01M87F0Y --clear notes
```

```text
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0YHZ1852KPS55KCDPXG2	reconciled	relic WB-R-01M87F0YHZ1852KPS55KCDPXG2: replayed 1 local change(s); 0 already on origin or changing nothing
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
WB-R-01M87F0YHZ1852KPS55KCDPXG2	published
workbook: warning: relic WB-R-01M87F0YHZ1852KPS55KCDPXG2: the relic's properties are 80025 bytes; the limit is 65536 bytes; both changes are kept, and the next change that makes it larger is refused; clear or shorten a property
workbook: the relic's properties are 80036 bytes; the limit is 65536 bytes
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
```

**An edit to a removed part is kept and hidden.** When one side removed a part
and the other edited it, the part stays removed, holds the edit, and the sync
warns with `relic-edit-on-removed-part`. Bob changed a field of the third part
with `--no-sync`, alice removed that part, and bob synchronized:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F11V42 --field heading="Non-goals and limits" --no-sync
workbook relic update WB-R-01M87F0S --remove-part 01M87F11V42
workbook sync
workbook relic show WB-R-01M87F0S#01M87F11V42
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F11V42NP3MEYSJ017ZDDA	section	-
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	reconciled	relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: replayed 1 local change(s); 0 already on origin or changing nothing
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	published
workbook: warning: relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F11V42NP3MEYSJ017ZDDA was edited here and removed in another clone; it stays removed and holds the edit; `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F11V42NP3MEYSJ017ZDDA` shows the part with the edit, and `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --restore-part 01M87F11V42NP3MEYSJ017ZDDA` restores it for everyone
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:03:20Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewed: yes
  reviewer: bob@example.com
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: review
#01M87F11V42NP3MEYSJ017ZDDA	section	no body	removed
  heading: Non-goals and limits
```

A warning from a sync is printed by the command that met it, or, for a sync a
watcher ran, on the watcher's terminal; it is not kept for a later command.

**Two saves of one body usually meet at `--base`.** With synchronization on, a
command fetches before it saves, so the second of two people saving the same
body is refused as a save from a stale base (exit `6`), as under
[Body versions](#body-versions), and nothing reaches the remote. A body
conflict comes from a save that was already recorded in this checkout without
being published, with `--no-sync`, offline, or with `auto-sync` off, meeting
another save of the same body.

### A relic conflict

A conflict stops the replay of that relic. Its ref moves to the remote's tip,
plus whatever of this checkout's changes replayed before the one that could
not, and the old local tip is kept at
`refs/workbook/reconciled-relics/<relic-id>/<n>`, counting from 1, in the same
compare-and-swap. That parked tip is never pushed and never removed
automatically: it holds the only copy of the text that was not applied, until
`workbook reset` writes it to a bundle and removes it. The command reports the
conflict once, exits `8`, and every other relic and task is still
synchronized. Bob and alice each saved a new third line over the same version
of the first part's body, bob first and with `--no-sync`, and bob then
synchronized:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md --base 01M87F0ZKD0X1MC06E1JCG5QBY --no-sync
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md --base 01M87F0ZKD0X1MC06E1JCG5QBY
workbook sync
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F205BTS8BJX4REAA4KMWJ
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F211MQKDWR4N1Q504HW4X
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	conflicted	relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F0T8ZACFWH3F27EK4CYVH: the body was saved here and on origin from the same version; the text from origin stands; the text saved here is kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1; run `git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1:parts/01M87F0T8ZACFWH3F27EK4CYVH/body > mine.md` to write it, `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body > current.md` to write the text that stands, combine the two in mine.md, then run `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F211MQKDWR4N1Q504HW4X`
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
Relic conflict:	WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	body	relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F0T8ZACFWH3F27EK4CYVH: the body was saved here and on origin from the same version; the text from origin stands; the text saved here is kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1; run `git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1:parts/01M87F0T8ZACFWH3F27EK4CYVH/body > mine.md` to write it, `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body > current.md` to write the text that stands, combine the two in mine.md, then run `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F211MQKDWR4N1Q504HW4X`
workbook: relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F0T8ZACFWH3F27EK4CYVH: the body was saved here and on origin from the same version; the text from origin stands; the text saved here is kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1; run `git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1:parts/01M87F0T8ZACFWH3F27EK4CYVH/body > mine.md` to write it, `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body > current.md` to write the text that stands, combine the two in mine.md, then run `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F211MQKDWR4N1Q504HW4X`
```

The text that stands is alice's, the one the remote held first. Text output
names the versions and the commands that write each text to a file, and never
prints the text itself. The next sync does not report the conflict again:

```sh
workbook sync
git for-each-ref refs/workbook/reconciled-relics/
```

```text
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
735d9a70c9284f3e7443555e6e24bc42e111e70a commit	refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1
```

**Getting the text back.** Three commands bring the text that was not applied
back and save it, where `<parked-ref>` and `<theirs-version>` are the
report's `parkedRef` and `body.theirs.version`, both of which the conflict's
line spells out:

1. `git cat-file blob <parked-ref>:parts/<part-id>/body > mine.md` writes the
   text that was not applied. Reading a relic at an older commit is a Git
   command for now.
2. `workbook relic show <relic>#<part> --body > current.md` writes the text
   that stands.
3. After combining the two by hand in `mine.md`, `workbook relic update
   <relic> --part <part> --body-file mine.md --base <theirs-version>` saves it.

```sh
git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1:parts/01M87F0T8ZACFWH3F27EK4CYVH/body > mine.md
workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body > current.md
diff current.md mine.md
```

```text
3c3
< The old history is saved to a bundle under .git first.
---
> The old history is saved to a bundle first, and the bundle is kept.
```

Bob combined the two third lines in `mine.md` and saved it:

```sh
workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F211MQKDWR4N1Q504HW4X
workbook relic show WB-R-01M87F0S#01M87F0T8ZA --body
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F244F21JP9K0HBRMFJX04
Sync:	completed
Finished tasks keep every commit they ever had.
Compaction replaces that history with one checkpoint.
The old history is saved to a bundle under .git first, and the bundle is kept.
```

To keep the text that stands instead, do nothing: the relic already holds it.

**Other changes that were not applied.** The replay stops at the command that
saved the body, so the rest of that command and every later local command are
not applied either: a `--title` or `--set` given with the `--body-file`, for
example. When there are any, the conflict's line ends by naming them, with
their operation type and ID, up to five, and the report lists them all in
`unapplied`. Each entry names the local commit that holds it, and
`git cat-file blob <commit>:operation.json` shows exactly what it changed. They
are kept at the parked ref. Make them again after saving the body. Had bob
also set a property with `--no-sync` before he synchronized, his line would
end like this, where `<operation-id>` and `<commit>` are that change's:

```text
; 1 other local change(s) were not applied either: property.set <operation-id>; they are kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1; `git cat-file blob <commit>:operation.json` shows the first, and the report's `unapplied` list under --json gives the commit of each; make them again after saving the body
```

**The report in JSON.** With `--json`, the result envelope carries the
conflicts in `relicConflict`, a list beside `conflict` and `configConflict`,
and the relic's line in the fetch carries the same detail text mode prints.
Bob and alice made the same two saves again, this time each adding a fourth
line, and bob synchronized with `--json`. The result went to `conflict.json`,
and the error envelope bob's command wrote to standard error is the line
that follows it. The result envelope in the file holds the conflict:

```sh
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file mine.md --base 01M87F244F21JP9K0HBRMFJX04 --no-sync
workbook relic update WB-R-01M87F0S --part 01M87F0T8ZA --body-file goals.md --base 01M87F244F21JP9K0HBRMFJX04
workbook sync --json > conflict.json
jq .relicConflict conflict.json
jq -c .data.fetch.relics conflict.json
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F273245GW2Y9NJSWHEMWQ
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
part	01M87F0T8ZACFWH3F27EK4CYVH	section	01M87F283AZ3GZ7ZW9GZKM2RK9
Sync:	completed
{"format":"workbook.error","version":1,"error":{"category":"conflict","message":"relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F0T8ZACFWH3F27EK4CYVH: the body was saved here and on origin from the same version; the text from origin stands; the text saved here is kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/2; run `git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/2:parts/01M87F0T8ZACFWH3F27EK4CYVH/body \u003e mine.md` to write it, `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body \u003e current.md` to write the text that stands, combine the two in mine.md, then run `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F283AZ3GZ7ZW9GZKM2RK9`"}}
[
  {
    "relicId": "WB-R-01M87F0SQYHP3HCR4QEN3RTVEX",
    "type": "body",
    "part": "01M87F0T8ZACFWH3F27EK4CYVH",
    "body": {
      "base": {
        "version": "01M87F244F21JP9K0HBRMFJX04",
        "format": "md",
        "bytes": 181,
        "object": "d7236f6a859ec647e42393432fe28f3e6d72c2e1",
        "text": "Finished tasks keep every commit they ever had.\nCompaction replaces that history with one checkpoint.\nThe old history is saved to a bundle under .git first, and the bundle is kept.\n"
      },
      "ours": {
        "version": "01M87F273245GW2Y9NJSWHEMWQ",
        "format": "md",
        "bytes": 201,
        "object": "8d2d80b3f13ec98d2ad0f516697cfe783b3c208f",
        "text": "Finished tasks keep every commit they ever had.\nCompaction replaces that history with one checkpoint.\nThe old history is saved to a bundle under .git first, and the bundle is kept.\nBob, a fourth line.\n"
      },
      "theirs": {
        "version": "01M87F283AZ3GZ7ZW9GZKM2RK9",
        "format": "md",
        "bytes": 203,
        "object": "ebf87e493debb69e0f4432b1196a964a220a41b2",
        "text": "Finished tasks keep every commit they ever had.\nCompaction replaces that history with one checkpoint.\nThe old history is saved to a bundle under .git first, and the bundle is kept.\nAlice, a fourth line.\n"
      }
    },
    "parkedRef": "refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/2",
    "unreplayed": 1,
    "remote": "origin"
  }
]
[{"relicId":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","status":"conflicted","detail":"relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX: part 01M87F0T8ZACFWH3F27EK4CYVH: the body was saved here and on origin from the same version; the text from origin stands; the text saved here is kept at refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/2; run `git cat-file blob refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/2:parts/01M87F0T8ZACFWH3F27EK4CYVH/body > mine.md` to write it, `workbook relic show WB-R-01M87F0SQYHP3HCR4QEN3RTVEX#01M87F0T8ZACFWH3F27EK4CYVH --body > current.md` to write the text that stands, combine the two in mine.md, then run `workbook relic update WB-R-01M87F0SQYHP3HCR4QEN3RTVEX --part 01M87F0T8ZACFWH3F27EK4CYVH --body-file mine.md --base 01M87F283AZ3GZ7ZW9GZKM2RK9`"}]
```

| Member | Value |
| --- | --- |
| `relicId`, `type` | The relic, and `body` or `tombstone`. |
| `part` | For `body`, the part whose body both sides saved. |
| `body.base` | The part's body at the commit both histories share. Absent when the part had no body there. |
| `body.ours` | The body at the parked local tip: the text that was not applied. |
| `body.theirs` | The body at the remote's tip: the text that stands. Its `version` is the `--base` to save from. |
| `body.*` | Each version has `version`, `format` when it has a label, `bytes`, `object` (the blob that holds it), and the content as `text`, or as `base64` when it is not UTF-8 text. |
| `tombstone` | For `tombstone`, the local operation the deletion stopped: `operationId` and `operation`. |
| `parkedRef` | Where the old local tip is kept. |
| `unreplayed` | How many of this checkout's commits did not replay. |
| `unapplied` | The other local operations that did not apply, oldest first: every one of the commits that did not replay except the one the conflict names, and except any the remote already holds. Each has `operationId`, `operation` (its type) and `commit` (the local commit whose `operation.json` holds it). Absent when there are none. |
| `remote` | The remote the conflict was met with. |

A body is at most 1 MiB, so one body conflict's report can reach about 4 MiB
once its three texts are escaped as JSON.

**A deleted relic.** When the remote holds a deletion and this checkout holds
an edit to the relic, the relic is deleted here as well, and the edit is
parked. The detail says to restore the relic and make the change again. It
names the first operation that could not apply; when the relic's other local
changes hold more, the line names them and the report lists them in
`unapplied`, as for a body. Make them again after the restore. Bob
changed the second relic with `--no-sync`, alice deleted it, and bob
synchronized, restored it and made his change again:

```sh
workbook relic update WB-R-01M87F0Y --set owner=bob@example.com --no-sync
workbook relic delete WB-R-01M87F0Y
workbook sync
workbook relic restore WB-R-01M87F0Y
workbook relic update WB-R-01M87F0Y --set owner=bob@example.com
```

```text
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
Fetch:
WB-01M87F19RB1PVEE87F7PS1D1FJ	unchanged
WB-R-01M87F0YHZ1852KPS55KCDPXG2	conflicted	relic WB-R-01M87F0YHZ1852KPS55KCDPXG2: origin deleted this relic, so the local property.set operation 01M87F2AHMB15362SBP9MWDKYV cannot apply; the local changes are kept at refs/workbook/reconciled-relics/WB-R-01M87F0YHZ1852KPS55KCDPXG2/1; to keep them, run `workbook relic restore WB-R-01M87F0YHZ1852KPS55KCDPXG2` and make the changes again
Push:
WB-01M87F19RB1PVEE87F7PS1D1FJ	up-to-date
Relic conflict:	WB-R-01M87F0YHZ1852KPS55KCDPXG2	tombstone	relic WB-R-01M87F0YHZ1852KPS55KCDPXG2: origin deleted this relic, so the local property.set operation 01M87F2AHMB15362SBP9MWDKYV cannot apply; the local changes are kept at refs/workbook/reconciled-relics/WB-R-01M87F0YHZ1852KPS55KCDPXG2/1; to keep them, run `workbook relic restore WB-R-01M87F0YHZ1852KPS55KCDPXG2` and make the changes again
workbook: relic WB-R-01M87F0YHZ1852KPS55KCDPXG2: origin deleted this relic, so the local property.set operation 01M87F2AHMB15362SBP9MWDKYV cannot apply; the local changes are kept at refs/workbook/reconciled-relics/WB-R-01M87F0YHZ1852KPS55KCDPXG2/1; to keep them, run `workbook relic restore WB-R-01M87F0YHZ1852KPS55KCDPXG2` and make the changes again
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
```

**A conflict met by a relic command.** `relic update`, `relic delete` and
`relic restore` fetch before they change anything. When that fetch parks a
conflict on the relic the command names, the command records nothing, reports
the conflict as above, and exits `8`; run it again to make the change on top
of what now stands. A conflict that fetch meets on another relic is reported
with the command's own output, and the command makes its change and exits `0`.
With a watcher running, a conflict the watcher met is reported by the next
command that hands it a change, with the three texts read from this checkout.

### The relic index

`relic list`, `relic show` and `relic export` answer from the relics in the
SQLite cache at `<git-common-dir>/workbook/cache.sqlite`, the same disposable
cache task reads use, and never from a copy of a body: a body is read from
Git by its object ID when it is shown or exported. Before answering, a relic
read brings the cache up to the relic refs, reading from Git only the relics
that changed since, so a change made in another clone, worktree or process is
seen without a rebuild. `relic create`, `update`, `delete` and `restore` read
the relic from Git, as they always did. See
[SQLite projection](architecture.md#sqlite-projection) for what the cache
holds.

`workbook rebuild` indexes every relic into the new cache beside the tasks
and counts them. Alice, in her clone, after the session above:

```sh
workbook rebuild --json | jq -c '.data | {taskCount, relicCount}'
workbook rebuild | sed "s|$PWD/||"
```

```text
{"taskCount":1,"relicCount":3}
Rebuilt 1 task(s) and 3 relic(s) at .git/workbook/cache.sqlite.
```

With `--json` the result has `relicCount` beside `taskCount` and `cachePath`.
The cache can be deleted at any time. The next relic read builds it again from
the refs and answers as before:

```sh
rm .git/workbook/cache.sqlite
workbook relic list
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	2 parts	Task archive compaction, second draft
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist
WB-R-01M87F17QTWKQKT6MZWX54HFY8	plan	3 parts	Task archive compaction, second draft
```

A task command that finds the cache missing rebuilds the task rows only, and
the relic rows wait for the first relic read. A relic the cache cannot hold,
one that is damaged or was written by a newer Workbook, is read from Git on
every read, with the same answer and the same error as before the index
existed. A relic read never fails because of the cache: if the cache cannot
be built or read, as when a damaged task ref stops the rebuild, relic reads
answer from Git.

### Links to tasks

A relic, or one of its parts, can link to a task. A link has its own ID, the
part it is from or none for the whole relic, the task it points at, and a
relationship, `rel`, such as `implemented-by`. It is stored once, on the
relic: the task's history does not change. Alice made a second task and
linked the relic to the first and its decision part to the second, in one
command:

```sh
workbook create "Bundle the old history"
workbook relic update WB-R-01M87F0S --link WB-01M87F19RB1 --rel implemented-by --link WB-01M87F2JJT7 --from 01M87F0T8ZD --rel implemented-by
workbook relic show WB-R-01M87F0S
```

```text
WB-01M87F2JJT7ERPBSGFX8XTZ4NW	backlog	medium	Bundle the old history
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:03:45Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewed: yes
  reviewer: bob@example.com
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: review
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 203 bytes, version 01M87F283AZ3GZ7ZW9GZKM2RK9
  heading: Goals
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
Links:
	01M87F2KNMJKQA2AYXXF2ZQ5C4	relic	implemented-by	WB-01M87F19RB1PVEE87F7PS1D1FJ
	01M87F2KR522KBJ4CWWSRHRY4G	01M87F0T8ZDXPZV8WNS006CBKV	implemented-by	WB-01M87F2JJT7ERPBSGFX8XTZ4NW
```

`--link <task>` starts a link and needs `--rel <rel>`; `--from <part>`, given
after `--link`, makes the link from that part rather than the whole relic.
Each `--link` is its own group, so one update adds several links. A task is named by
any unambiguous prefix of its ID, as a task command takes it. `relic show`
lists the links under `Links:`, one per line: the link's ID, `relic` or the
part it is from, the relationship and the task. A whole-relic `relic show`
lists every link, and `relic show <relic>#<part>` only that part's.

A `rel` is 1 to 100 bytes of lowercase letters and digits, with single hyphens
between words. Linking what is already linked, the same part or the whole
relic to the same task with the same relationship, records nothing and says
`No change`, so a retried command is safe. The same task under another
relationship is a second link:

```sh
workbook relic update WB-R-01M87F0S --link WB-01M87F19RB1 --rel implemented-by
workbook relic update WB-R-01M87F0Y --link WB-01M87F19RB1 --rel relates-to
```

```text
No change: WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
Sync:	completed
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	Release checklist
Sync:	completed
```

**From the task's side.** `relic list --linked-to <task>` lists the relics
that link to a task, each with the links that do, and `workbook show <task>`
ends with a `Relics:` section that lists them, with `relics` in its JSON. A
task with no links shows no section and no member. Both answer from the
[relic index](#the-relic-index):

```sh
workbook relic list --linked-to WB-01M87F19RB1
workbook relic list --linked-to WB-01M87F19RB1 --json
workbook show WB-01M87F19RB1
workbook show WB-01M87F19RB1 --json | jq -c '.data.relics'
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	2 parts	Task archive compaction, second draft
	01M87F2KNMJKQA2AYXXF2ZQ5C4	relic	implemented-by
WB-R-01M87F0YHZ1852KPS55KCDPXG2	plan	0 parts	Release checklist
	01M87F2PV71CCDX34JPK969NYV	relic	relates-to
{"format":"workbook.result","version":1,"command":"relic list","data":[{"id":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","head":"9838201adc215ebdae1780fef049d03763273df7","title":"Task archive compaction, second draft","type":"spec","author":"alice@example.com","retired":false,"deleted":false,"createdAt":"2026-11-24T19:02:45.75869Z","updatedAt":"2026-11-24T19:03:45.157964Z","parts":2,"linkedBy":[{"link":"01M87F2KNMJKQA2AYXXF2ZQ5C4","rel":"implemented-by"}]},{"id":"WB-R-01M87F0YHZ1852KPS55KCDPXG2","head":"2582439f3202d7b0e539ae782f0b83facf090182","title":"Release checklist","type":"plan","author":"alice@example.com","retired":false,"deleted":false,"createdAt":"2026-11-24T19:02:50.687794Z","updatedAt":"2026-11-24T19:03:48.327344Z","parts":0,"linkedBy":[{"link":"01M87F2PV71CCDX34JPK969NYV","rel":"relates-to"}]}]}
ID:	WB-01M87F19RB1PVEE87F7PS1D1FJ
Project ID:	01M87F0RVXDCDBV34NDN8Z3Y5E
Title:	Write the compaction command
Description:	
Status:	backlog
Priority:	medium
Due:	
Labels:	
Assignments:	
Rank:	1/1
Dependencies:	
Created At:	2026-11-24T14:03:02.155093-05:00
Updated At:	2026-11-24T14:03:02.155093-05:00
Deleted:	false
History Generation:	01M87F19RB8WNZJ5EV6SZHWGDY
Head:	f9b8437ee3bd5b22ee166240ca45a4e3f62e403b

Relics:
	WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	implemented-by	Task archive compaction, second draft
	WB-R-01M87F0YHZ1852KPS55KCDPXG2	relates-to	Release checklist
[{"relic":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","title":"Task archive compaction, second draft","type":"spec","link":"01M87F2KNMJKQA2AYXXF2ZQ5C4","rel":"implemented-by"},{"relic":"WB-R-01M87F0YHZ1852KPS55KCDPXG2","title":"Release checklist","type":"plan","link":"01M87F2PV71CCDX34JPK969NYV","rel":"relates-to"}]
```

In `relic list --json`, each relic carries `linkedBy`, the links to the task
as `link`, `part` and `partType` for a link from a part, and `rel`. Each entry
of the task's `relics` has `relic`, `title`, `type`, `part` and `partType` for
a link from a part, `link` and `rel`. A deleted relic is listed by
`--linked-to` only with `--all`, and neither lists a link from a removed part:
a link from a part is hidden wherever the part is, and comes back with it.
`workbook show` never fails because of relics. A relic it cannot read, one
that is damaged or written by a newer Workbook, is left out with a
`relic-links-unread` warning that names it.

**In the relic's own form.** `relic show --json` lists the links of the parts
it shows in `links`, each with `id`, `from` for a link from a part, `to` with
the task, and `rel`. The relic's commit stores the live links in
`links.json`, ordered by ID, and the commit's operations record the change:

```sh
workbook relic show WB-R-01M87F0S#01M87F0T8ZD
workbook relic show WB-R-01M87F0S --json | jq -c '.data.links'
git cat-file blob refs/workbook/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX:links.json
git cat-file blob 9838201adc215ebdae1780fef049d03763273df7:operation.json | jq -c .operations
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:03:45Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewed: yes
  reviewer: bob@example.com
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: review
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
Links:
	01M87F2KR522KBJ4CWWSRHRY4G	01M87F0T8ZDXPZV8WNS006CBKV	implemented-by	WB-01M87F2JJT7ERPBSGFX8XTZ4NW
[{"id":"01M87F2KNMJKQA2AYXXF2ZQ5C4","to":{"task":"WB-01M87F19RB1PVEE87F7PS1D1FJ"},"rel":"implemented-by"},{"id":"01M87F2KR522KBJ4CWWSRHRY4G","from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by"}]
{"format":"workbook.relic-links","version":1,"links":[{"id":"01M87F2KNMJKQA2AYXXF2ZQ5C4","to":{"task":"WB-01M87F19RB1PVEE87F7PS1D1FJ"},"rel":"implemented-by"},{"id":"01M87F2KR522KBJ4CWWSRHRY4G","from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by"}]}
[{"id":"01M87F2KNMJKQA2AYXXF2ZQ5C4","type":"link.add","link":{"to":{"task":"WB-01M87F19RB1PVEE87F7PS1D1FJ"},"rel":"implemented-by"}},{"id":"01M87F2KR522KBJ4CWWSRHRY4G","type":"link.add","link":{"from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by"}}]
```

**Refused** (nothing is written): a `rel` that is not a lowercase word list
(exit `5`); a relic ID, since a relic links only to tasks so far (exit `5`); a
task that does not exist (exit `4`); `--link` with no `--rel`, and `--from`
before any `--link` (exit `2`); a `--from` part that does not exist (exit `4`)
or is removed (exit `5`); and a deleted task (exit `5`), shown further on:

```sh
workbook relic update WB-R-01M87F0S --link WB-01M87F19RB1 --rel "Implemented By"
workbook relic update WB-R-01M87F0S --link WB-R-01M87F0YHZ1852KPS55KCDPXG2 --rel relates-to
workbook relic update WB-R-01M87F0S --link WB-7ZZZZ --rel relates-to
workbook relic update WB-R-01M87F0S --link WB-01M87F19RB1
workbook relic update WB-R-01M87F0S --from 01M87F0T8ZD --rel implemented-by
```

```text
workbook: the link relationship "Implemented By" must be lowercase letters and digits, with single hyphens between words
workbook: a relic links only to tasks so far; "WB-R-01M87F0YHZ1852KPS55KCDPXG2" is not a task
workbook: no task matches "WB-7ZZZZ"
workbook: relic update --link requires --rel <rel>, such as implemented-by
workbook: relic update --from applies to one link; give it after --link
```

The two invocation errors are followed by the command's usage, left out here.
A relic holds at most 2,000 live links, checked only when a change adds one; a
relic over the limit, which two clones' links together can make, can still be
read and changed, and any link can be removed.

**A task that is removed later.** A link stays when its task is deleted.
`relic show` marks it `(removed)`, and `targetRemoved: true` in JSON; the
same holds for a task this clone does not have. The deleted task cannot be
linked again until it is restored, and `--linked-to` still finds the relic:

```sh
workbook delete WB-01M87F2JJT7
workbook relic show WB-R-01M87F0S
workbook relic show WB-R-01M87F0S --json | jq -c '.data.links'
workbook relic update WB-R-01M87F0Y --link WB-01M87F2JJT7 --rel relates-to
workbook relic list --linked-to WB-01M87F2JJT7
```

```text
WB-01M87F2JJT7ERPBSGFX8XTZ4NW	backlog	medium	Bundle the old history
Sync:	completed
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Author:	alice@example.com
Updated:	2026-11-24T19:03:45Z
Properties:
  approved: null
  owner: Alice <alice@example.com>
  reviewed: yes
  reviewer: bob@example.com
  reviewers: ["Alice \u003calice@example.com\u003e","bob@example.com"]
  status: review
#01M87F0T8ZACFWH3F27EK4CYVH	section	md, 203 bytes, version 01M87F283AZ3GZ7ZW9GZKM2RK9
  heading: Goals
#01M87F0T8ZDXPZV8WNS006CBKV	decision	no body
  accepted: true
  heading: Keep a backup bundle
Links:
	01M87F2KNMJKQA2AYXXF2ZQ5C4	relic	implemented-by	WB-01M87F19RB1PVEE87F7PS1D1FJ
	01M87F2KR522KBJ4CWWSRHRY4G	01M87F0T8ZDXPZV8WNS006CBKV	implemented-by	WB-01M87F2JJT7ERPBSGFX8XTZ4NW (removed)
[{"id":"01M87F2KNMJKQA2AYXXF2ZQ5C4","to":{"task":"WB-01M87F19RB1PVEE87F7PS1D1FJ"},"rel":"implemented-by"},{"id":"01M87F2KR522KBJ4CWWSRHRY4G","from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by","targetRemoved":true}]
workbook: task WB-01M87F2JJT7ERPBSGFX8XTZ4NW is deleted; restore it first
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	2 parts	Task archive compaction, second draft
	01M87F2KR522KBJ4CWWSRHRY4G	01M87F0T8ZDXPZV8WNS006CBKV	implemented-by
```

When the task list cannot be read, `relic show` still answers: the links are
listed without `targetRemoved`, and a `relic-link-targets-unread` warning says
that links to removed tasks are not marked.

**Removing a link.** `--unlink <link>` removes one link, named by its ID or an
unambiguous prefix of it, by recording a `link.remove` that names the ID. The
link is no longer in `links.json`; its `link.add` stays in the history.
`relic update --json` reports the relic as any update does, with no link
member, so read a new link's ID from `relic show`. A full-length link ID the
relic does not hold records nothing, so a retry after the link is gone is
safe, and so is a mistyped full-length ID, which is not reported. A shorter
prefix that matches no link is `not-found` (exit `4`), and one that matches
two is refused (exit `5`). Linking the same task again
makes a new link with a new ID:

```sh
workbook relic update WB-R-01M87F0S --unlink 01M87F2KNMJ
git cat-file blob refs/workbook/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX:operation.json | jq -c .operations
git cat-file blob refs/workbook/relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX:links.json
workbook relic update WB-R-01M87F0S --link WB-01M87F19RB1 --rel implemented-by --json | jq -c .data
workbook relic update WB-R-01M87F0S --unlink 01M87F2KNMJKQA2AYXXF2ZQ5C4
workbook relic update WB-R-01M87F0S --unlink 01M0000
```

```text
WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	spec	Task archive compaction, second draft
Sync:	completed
[{"id":"01M87F2Z6246AY8E00388DH9SS","type":"link.remove","linkId":"01M87F2KNMJKQA2AYXXF2ZQ5C4"}]
{"format":"workbook.relic-links","version":1,"links":[{"id":"01M87F2KR522KBJ4CWWSRHRY4G","from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by"}]}
{"relic":{"id":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","head":"ecab03238f99a74eb0366e7dbd69e889028f623a","title":"Task archive compaction, second draft","type":"spec","author":"alice@example.com","retired":false,"deleted":false,"createdAt":"2026-11-24T19:02:45.75869Z","updatedAt":"2026-11-24T19:03:58.380464Z","parts":2},"parts":[],"changed":true}
No change: WB-R-01M87F0SQYHP3HCR4QEN3RTVEX
Sync:	completed
workbook: relic WB-R-01M87F0SQYHP3HCR4QEN3RTVEX has no link "01M0000"
```

**Export and `--from-file`.** `relic export` lists every stored link, as
`links.json` holds them. `relic create --from-file` refuses a document whose
`links` is not empty (exit `5`), since its parts get new IDs:

```sh
workbook relic export WB-R-01M87F0S | jq -c .links
workbook relic export WB-R-01M87F0S | workbook relic create --from-file - --type plan
```

```text
[{"id":"01M87F2KR522KBJ4CWWSRHRY4G","from":"01M87F0T8ZDXPZV8WNS006CBKV","to":{"task":"WB-01M87F2JJT7ERPBSGFX8XTZ4NW"},"rel":"implemented-by"},{"id":"01M87F30NCBXBZXB7160GW3MB7","to":{"task":"WB-01M87F19RB1PVEE87F7PS1D1FJ"},"rel":"implemented-by"}]
```

The second command's message says that this version cannot create a relic
with links, and to empty the document's `"links"` list, create the relic, then
add each link with `relic update <relic> --link <task> --rel <rel>`.

**Links are shared with the relic.** A link travels in the relic's commits,
so the next sync brings it to every clone, and two clones' link changes are
combined as any two relic changes are: every link either clone adds is kept,
two clones adding the same link at once keep two links with two IDs, and a
removal removes only the link it names. Bob, in his clone, after alice's
changes:

```sh
workbook sync > /dev/null; workbook show WB-01M87F19RB1 | tail -3
```

```text
Relics:
	WB-R-01M87F0SQYHP3HCR4QEN3RTVEX	implemented-by	Task archive compaction, second draft
	WB-R-01M87F0YHZ1852KPS55KCDPXG2	relates-to	Release checklist
```

### Relic JSON results

Every relic command takes `--json` and uses the result and error envelopes
described below. The `command` member is `relic create`, `relic list`,
`relic show`, `relic update`, `relic delete`, `relic restore` or
`relic export`.

`relic create`, `relic update`, `relic delete` and `relic restore` report the
same three members, as in the `--add-part` result under
[Part order](#part-order):

| Member | Value |
| --- | --- |
| `relic` | The relic after the command, with the members `relic list --json` gives. |
| `parts` | The parts the command added or edited, in the order it first changed them, each with `id`, `type`, `position`, `fields`, and `body` without its content. A part that was only moved, removed or restored is not listed. Always present, and empty when there is none. |
| `changed` | `false` when the command recorded nothing. |

Those four commands also report, in the envelope's `sync` member, the fetch
they ran and the push of the one relic they changed, as `sync.pushRelic`:

```json
{"enabled":true,"source":"default","status":"completed","fetch":{"remote":"origin","status":"completed","tasks":[{"taskId":"WB-01M87F19RB1PVEE87F7PS1D1FJ","status":"unchanged"}]},"pushRelic":{"relicId":"WB-R-01M87F0SQYHP3HCR4QEN3RTVEX","status":"published"}}
```

`pushRelic` has `relicId`, `status` (`published`, `rejected`, `invalid` or
`needs-upgrade`)
and, when it did not publish, `detail`. It is absent with `--no-sync`, when
the command changed nothing, when the fetch did not complete, and when a
running watcher was handed the change to publish. `relic list`, `relic show` and `relic export` contact no remote and
report no `sync`.

`workbook fetch`, `workbook push` and `workbook sync` list each relic they
changed or could not change in a `relics` list on the phase that met it:
`data.relics` for `fetch` and `push`, and `data.fetch.relics` and
`data.push.relics` for `sync`. Each entry has `relicId`, `status` and, when
there is more to say, `detail`. A fetch reports `created`, `fast-forwarded`,
`reconciled`, `conflicted`, `needs-upgrade` or `invalid`; a push reports
`published`, `rejected` or `invalid`. A relic that did not change is not
listed, so a phase that changed no relic gives the JSON it gave before relics
were shared. A relic that could not be replayed is also listed in the
envelope's `relicConflict`, as under [A relic conflict](#a-relic-conflict),
and a relic over a limit after a merge gives a `relic-over-limit` warning in
`warnings`.

### Relics for agents

The generated guidelines, `.workbook/guidelines.md`, have a `Relics` section,
and the Workbook skill the same steps; `workbook setup` installs both and
`workbook docs update` refreshes them. They teach an agent to find the relics
linked to a task, read one part, and save an edit naming the version it read,
with these commands, in this order:

```sh
workbook show <task> --json
workbook relic list --linked-to <task> --json
workbook relic show <relic>#<part> --body --json
workbook relic update <relic> --part <part> --body-file <file> --base <version> --json
```

The links are the task's `data.relics`, as under
[Links to tasks](#links-to-tasks), and the part's body and its version are
`data.parts[0].body.text` and `data.parts[0].body.version`, as under
[The relic document](#the-relic-document). The skill tells an agent to read
each linked part before editing files, as part of the task's context.

They also say what to do when a save meets another edit of the same body. A
save from a version that is no longer current is refused with exit `6`
(`stale-write`), as under [Body versions](#body-versions), and repeating it
fails the same way. A sync that meets another clone's save reports a
[relic conflict](#a-relic-conflict), exit `8`. Either way the agent reads the
part again and, when its intent is clear, applies its change to the current
text and saves from the current version. Otherwise it shows the user both
versions and asks. The exit-code table in the guidelines says that a relic
save refused for its `--base` needs a fresh read first, where most exit `6`
refusals succeed when retried.

The guidelines describe relic IDs under the project's current key, as
`WB-R-<ULID>` and `WB-<ULID>` here. They name only commands and options that
exist: a test holds every `workbook` command in the guidelines and the skill
to the command schemas, and runs the four commands above in order against a
temporary repository.

## Machine-readable output and exit codes

With `--json`, success is a single compact line carrying a versioned envelope,
and failure is a single compact line on standard error:

```text
{"format":"workbook.result","version":1,"command":"create","data":{...}}
{"format":"workbook.error","version":1,"error":{"category":"validation","message":"..."}}
```

A refusal about the remote a project shares its tasks through adds a
`tasksRemote` member to `error`; see
[Setup and the refusals](#setup-and-the-refusals). No other error carries it.
A refusal from `workbook reset` adds a `reset`
member instead; see [The reset's JSON result](#the-resets-json-result).

The exit code names the error's category, so a caller can decide what to do
without parsing a message:

| Code | Category | What it means |
| --- | --- | --- |
| 0 | — | The command succeeded. |
| 1 | `operational` | The environment or `origin` is at fault: an unreachable remote, a Git command that failed, a port already bound. |
| 2 | `invalid-invocation` | The command line is wrong. Fix the arguments. |
| 3 | `not-initialized` | This repository has no Workbook project. Run `workbook setup`. |
| 4 | `not-found` | No such task, relic, part, commit, or setting. |
| 5 | `validation` | The input is well-formed but not allowed, such as an unknown status or restoring an active task. It fails the same way on every retry. |
| 6 | `stale-write` | The task or relic ref moved between read and write, so the compare-and-swap was refused, or a bounded wait on the shared SQLite cache ran out while another process held or replaced it. Nothing was written. Retrying the identical command usually succeeds. A relic body saved from the wrong `--base` is exit `6` as well, and retrying that one does not. |
| 7 | `corrupt-data` | Stored data could not be read as Workbook wrote it. Read the message; repair or rebuild before continuing. |
| 8 | `conflict` | Reconciliation stopped on a decision Workbook will not make, for a task, a status or priority, or a relic. See [Reconciling divergent histories](#reconciling-divergent-histories) and [A relic conflict](#a-relic-conflict). |
| 9 | `newer-writer` | A newer Workbook wrote this history and this build cannot fold it. Nothing is damaged; upgrade Workbook. See [Mixed Workbook versions](architecture.md#mixed-workbook-versions). |
| 10 | `assigned` | The task is assigned to somebody else, you have no assignment on it, and you did not pass `--force`. Nothing was recorded. Pick another task. See [Assignments](#assignments). |

Exit `10` is what `workbook update <id> --assign self` answers when a specific
task turns out to be somebody else's, and it is a category of its own rather
than a reuse of `5` for that reason: "somebody else is on this task" is the only
refusal a caller can act on by itself, while a validation refusal means it typed
something wrong and a not-found means the task is gone. It is also a promise
about state — nothing was written, so an agent that meets it holds no assignment
and owes no cleanup. An assignment that lands beside somebody else's, whether
forced or through a lost push race, exits `0` with an `assignment-shared`
warning instead. `workbook next --claim` does not produce exit `10`; see
[Claiming work as an agent](#claiming-work-as-an-agent).

Exit `6` is what a concurrent writer sees: two processes mutating one task, a
push whose remote ref changed underneath it, and a web save proposed against a
tip the task has moved past all surface as `stale-write` rather than as silent
overwriting. So do the three bounded waits on the shared SQLite cache, when
another process outlasts them, for a read as much as for a write: the cache
locked past its five-second busy timeout, the cache file replaced by another
process during each of three attempts at one command, and the cache replaced by
another process again each time it was recovered, three rounds running. A read
is never refused because somebody else updated the cache's rows at the same
moment; see [Simultaneous requests](#simultaneous-requests).

Two commands changing one relic at the same moment are a concurrent writer
too. One is recorded and the other is refused with exit `6`, with a message
that begins `relic ref changed concurrently` and says to run the command
again. Running it again gets past that refusal.

Three relic refusals are exit `6` and are not got past by a retry of the
identical command. All three are about `--base` when a body is saved with
`relic update --part <part> --body-file <file>`: a `--base` that is not the
part's current body version, no `--base` when the part has a body, and a
`--base` when the part has none. Each fails the same way until the command
changes; see [Body versions](#body-versions).

Reporting a conflict and failing over one are separate things. `workbook next`
exits `0` while listing conflicts in its envelope, because a caller asking which
task to pick up next is not the caller who has to resolve them; a mutation that
could not replay past one exits `8`. A relic conflict follows the same rule.
`workbook fetch` and `sync` exit `8` when they park one. A relic command exits
`8` when its fetch parks one on the relic it names, and exits `0`, listing the
conflict in `relicConflict`, when the conflict is on another relic.

## User-global configuration

Settings that describe a developer rather than a project live in
`$XDG_CONFIG_HOME/workbook/config.json`, defaulting to
`~/.config/workbook/config.json`. `workbook setup` writes it with defaults when
it is missing, and a missing file always means defaults rather than an error:

```json
{
  "format": "workbook.user",
  "version": 1,
  "docTargets": ["AGENTS.md", "CLAUDE.md"],
  "skillDir": ".claude/skills",
  "preferences": {}
}
```

`docTargets` names the agent documentation files Workbook manages. A target is
refreshed only when the project already contains it; Workbook never creates one
on its own, so listing a file here is safe. An entry must name a file inside the
project, given as a path relative to it — an escaping value such as
`../../.bashrc`, an absolute path, and a directory listed by mistake such as
`docs` or `.claude` are all refused, with the error naming the offending value
and where it came from: the user configuration file, by its path, or
`--skill-dir`. A relative `skillDir` follows the same escape rule, but an
absolute one remains honored, as the documented way to keep one personal copy of
the skill across projects. `preferences` is reserved for future settings and is
deliberately untyped, so adding one later needs no format version bump.

Project identity and task data stay in the repository. Nothing in this file
affects what Workbook records.
Create, update, delete, and restore append immutable task commits under
`refs/workbook/tasks/`; delete records a tombstone instead of removing the ref.
Creation and ordinary updates write descriptive task-operation commit subjects
suitable for `git log`, while canonical data remains in the operation and state
blobs.
Tombstoned tasks reject every mutation except `workbook restore`, which records
an explicit append-only restore operation. A restore may name where the task
comes back with `--into <status>`; that destination is recorded in the same
operation pack as the restore, so it is one change rather than two. Without it,
the task returns to the status it was deleted from. `--into` names a column and
not a position, so the task keeps the position it was deleted with; the web
route below also accepts a neighbour to land beside, and computes a new position
when one is named.
List and show read the current task checkpoint from each task ref's tip. A new
project's statuses are Backlog, Ready, In Progress, In Review, and Done, in that
order; a project may change them with `workbook status` (see
[Project statuses](#project-statuses)). `move` orders a task inside its
status-and-priority bucket with an exact rational rank; it changes only that
task. `depend` adds a prerequisite edge and rejects cycles; `free` removes one
prerequisite edge and is idempotent. `next` chooses the first task in a status
tagged `next` whose dependencies are all active and in a status tagged `done`,
sorting by priority, rank, and task ID; it reports no eligible task when none
qualify. It skips the tasks somebody else is assigned to — see
[Assignments](#assignments) — and `--any` offers the whole eligible set instead.
`--limit <n>` offers the first `n` eligible tasks in that same order; its JSON
`data` is `{"tasks": [...], "eligible": N}`, where `eligible` counts every task
that qualified, and its text output is a `list` header and one row per task.
`--limit` cannot be combined with `--claim`, which takes one task. The web board
uses the same core task order and presents an actionable, unambiguous task-ID
prefix with each card's priority, title, labels, and assignees. Normal `list`,
`show`, and `next` reads use the local SQLite projection.
Implementation links remain future work.

## Who an edit is recorded under

Every edit Workbook records names the person who made it. By default that is
the Git `user.email` of the checkout the command runs in, read once per
command. Setting `WORKBOOK_ACTOR` names somebody else:

```sh
WORKBOOK_ACTOR=sam@example.com workbook update WB-01K0 --comment "on it" --assign self
```

There is no command-line flag for this. The variable is meant for hosting, where
one checkout acts for many signed-in people, and not for everyday use on your
own machine.

With the variable set, the command treats the given name as the person for
everything Workbook decides by identity:

- every operation it records carries the name, so `workbook show --history`
  shows it, and comments and attachments are authored by it;
- `--assign self` assigns to it, and an assignment records it as its creator;
- `workbook next` skips tasks held by anybody other than it, and `next --claim`
  claims for it;
- only that name, or whoever recorded an assignment, may remove the assignment;
- only that name may edit a comment recorded under it, and `show --json`
  answers `editable` for it;
- `workbook serve` records the board's edits under it, and the board's empty
  **Assign someone** field assigns to it.

The Git commit behind each edit carries the name as both its author and its
committer, in place of the checkout's `user.name` and `user.email` and any
`GIT_AUTHOR_*` or `GIT_COMMITTER_*` variables. That holds even when the name is
the checkout's own `user.email`: setting the variable names the person, so the
commit does not take its identity from anywhere else. Workbook is given an email
address and nothing else, so the address is the commit's display name as well:
`sam@example.com <sam@example.com>`. A folder with no Git identity configured
works when the variable is set. The commit's date is Git's, as it always is;
`WORKBOOK_NOW` dates the operation, not the commit.

A commit always names the person its edit is recorded under, whichever process
writes it. That matters for synchronization, which sometimes writes commits of
its own: when somebody else has published changes to the same task or to the
project's configuration, a sync replays this checkout's unpublished edits onto
theirs as new commits. Each replayed commit names the person its edit was
recorded under, not the person running the sync. A replay has no
`WORKBOOK_ACTOR` of its own, whatever the syncing command was started with: a
replayed edit recorded under the checkout's own `user.email` is committed
exactly as any commit from the checkout is, and any other name is the commit's
author and committer as above. So
a `workbook sync --watch` or `workbook serve` started without the variable
replays and publishes everybody's edits under their own names, several people's
in one pass, and needs no Git identity of its own to do it. One limit remains
for a watcher started separately: a replay of an edit recorded under the
checkout's own `user.email` takes its display name, and any `GIT_AUTHOR_*` or
`GIT_COMMITTER_*` variables, from the process that writes the replay, which for
a deferred sync is the watcher rather than the command that recorded the edit.
A replay is never refused because of the form of a name already in history.
When a replayed edit's name is not the checkout's `user.email` and Git could
not store it exactly, such as a `user.email` somebody configured with angle
brackets around it, which Git quietly drops, the replayed commit takes its
author and committer from the commit being replayed: the identity Git recorded
when the edit was made. A commit some other tool wrote can record an identity
Git will not write again: an empty name, a name made only of the characters Git
strips, or no email at all. The replay then takes the recorded email as its
name, or the name `unknown` when the email will not do either, and a commit
missing its author or committer line takes the other one. So no other person's
name is attached, and the task or the configuration still synchronizes. Only
a new edit is refused for such a name, and only when `WORKBOOK_ACTOR` gives it,
as below.

The name must be an email address, by the same rule `--assign` applies to a
person, with no `/label` part. It also must be valid UTF-8, must not contain
`<` or `>`, and must not begin or end with any of `` , : ; " ' \ ``, because
the operation or the commit would otherwise record somebody else. The value is
used exactly as given and is never trimmed. A variable that is set but empty,
or set to anything else, is refused with exit `5` and a validation error that
names the variable, before the command reads or writes anything. It never falls
back to the checkout's name. A command name that does not exist is reported as
unknown first. `workbook version`, `workbook completion` and help are the only
commands that do not check the variable.

To count as the same person as a checkout's `user.email`, or as an assignment's
principal or creator, the name must match it exactly, including case:
`Sam@example.com` and `sam@example.com` are two different people, as they are
for `user.email`.

With the variable unset, a command's own edits are recorded and committed
exactly as they were before the variable existed. The one difference is in
replays: a replayed edit that was recorded under a name other than this
checkout's `user.email`, such as a teammate's change replayed while adopting a
compaction, is now committed under that name rather than this checkout's.

The name is asserted, exactly as `user.email` is. Workbook does not verify it
and keeps no list of who may use it: whoever can set the variable can already
push under any name they like.

## Assignments

An assignment says who is responsible for a task. It is a convergent collection
exactly like labels: two clones that assign the same task concurrently both
survive, because pairing and independent spikes are working modes rather than
disagreements. There are no leases, no renewals, and no expiry — nothing a clock
takes away — and an assignment never blocks a write.

```sh
workbook update WB-01K0 --assign self                 # this repository's user.email, or WORKBOOK_ACTOR
workbook update WB-01K0 --assign self/impl-1          # one agent of that identity
workbook update WB-01K0 --assign sam@example.com      # somebody else
workbook update WB-01K0 --unassign self/impl-1        # give it back
workbook update WB-01K0 --status in-progress --assign self   # one commit
```

The principal is the asserted Git identity, which is already stamped on every
operation: there is no roster and no verification protocol, because verifying
identity would import the central authority Workbook deliberately lacks. An
agent qualifies its principal with a free-form label after a slash, so
`dylan@example.com/impl-1` and `dylan@example.com/impl-2` are two agents of one
person. Authority keys on the address; the label only says which agent.

One `--assign` or one `--unassign` per invocation, never both. Everything else
in the same invocation rides in the same operation pack, so `--status
in-progress --assign self` is one commit, one history entry, and one refusal:
either the status moved and the assignment landed or neither did.

**Removal is strict.** An assignment may be withdrawn only by the identity it
names — any agent of that address, so an orchestrator can sweep up after its own
fleet, including agents that crashed — or by whoever recorded it, so a mistaken
tag of a teammate is undoable by its author. Anybody else is refused with exit
`5` and told who to ask. The rule is enforced twice: at the mutation boundary,
and in the fold, which treats a foreign removal as a recorded no-op. That is
what makes it durable rather than advisory, and it is why two agents forcing
their way on to one task terminate immediately in the both-assigned state
instead of removing each other's claims forever.

`workbook show` prints each assignment with the identity, when it was recorded,
and how long ago that was. Staleness is displayed and never enforced: "assigned
9 days ago" on a task nobody has touched is a fact for people to settle between
themselves. The web board shows a compact chip — the local part of the address
plus the agent label — beside each card's labels.

On `workbook serve`'s board the chip row is drawn only on a card somebody holds,
so an unassigned board is exactly the board it has always been, and it is capped
at three chips with a `+N more` beside them: a task may hold fifty assignments
and a card is one box in one column. A long identity is truncated to its column
and never widens it. The task's own page carries the whole list under
**Assignments** — the full value and the time it was recorded — in the page's
sidebar, below Dependencies and above Attachments. It does not repeat the
"assigned 3 days ago" phrasing `workbook show` prints; the recorded time is the
fact, and `workbook show` is where the phrase is worded.
It follows the poll, so an assignment made from the CLI in another terminal
appears on an open page within a second without disturbing anything being typed
into it.

The section also makes assignments. Type an identity into **Assign someone** and
press Assign, which sits on the field's line; leave the field blank and the
assignment is `serve`'s own identity, which is this checkout's `user.email`, or
the name `WORKBOOK_ACTOR` gave `serve` when it started —
the same one `--assign self` resolves from the same directory, and the one the
empty field shows as its placeholder. Add a `/label` to name one agent, exactly
as on the command line. Assigning from the board is additive and never takes the
claim gate: the section in front of you already names everybody who holds the
task, so nothing is refused for being shared, and a warning names the others
when your assignment lands beside one. That is the one place this surface
differs from the command line, and it is worth knowing if your habits come from
there: plain `workbook update --assign` refuses a task somebody else holds, and
`--force` is what assigns you beside them. The board always behaves the way
`--force` does, and offers no way to ask for the gate.

The New Task form offers the same control in the same place, and stages what it
is given rather than sending it: a task has to exist before anything can be
assigned to it. Once the create answers with the task's ID, each staged
assignment is sent through the assignment route above, in the order it was
staged, exactly as the task page's control would send it — so a blank one is
`serve`'s own identity there too. An assignment the server refuses after the
create is handled the way a refused dependency is: the task exists, so the
board opens its page, says some assignments were not saved, and draws each one
in the Assignments section with the reason, a Retry, and a red × to remove it.
**Create more** re-arms a form with nothing staged.

Each row carries a red × at its right end, named **Unassign** followed by the
value (for example "Unassign sam@example.com/impl-1"), where this checkout may
withdraw it — its own assignments, and the ones it recorded against somebody
else. A row without the control is somebody else's to release, under the same
rule `workbook update --unassign` enforces; the button is drawn from the
server's answer, so a control
this page offers is one the write would accept.

A board with no identity to record an assignment against draws none of this: an
assignment records its creator, so there is nothing to write one as. That board
is the read-only section — no form, no control, and no section at all on a task
nobody holds — and both assignment addresses answer it with an operational
error rather than staging a write nothing could be attributed to. `workbook
serve` never reaches that state by accident: a checkout with no configured
`user.email` fails to start at all unless `WORKBOOK_ACTOR` names the person (see
[Who an edit is recorded under](#who-an-edit-is-recorded-under)), because every
command opens the service through the same identity this one would have
assigned as.

### Claiming work as an agent

There are two ways to take work up, and they report differently because they ask
different questions.

```sh
workbook next --claim --json
# exit 0, data non-null → the task is yours, assigned and published
# exit 0, data null     → nothing to claim; a `next-held-by-others` warning
#                         distinguishes "held" from "there is no work"

workbook update <id> --assign self --json
# exit 0  → recorded
# exit 10 → somebody else holds that task and you do not; nothing was recorded
```

`next --claim` fetches, picks the task `next` would pick, appends the
assignment, and pushes, in one synchronous command. Doing it in two commands
would leave a window in which a second agent selects the same task, so the claim
is one stroke or nothing.

**`next --claim` does not report exit `10`.** Its selection has already excluded
every task another identity holds and this one does not, so there is nothing
left for the claim to refuse. An empty board and a fully-claimed board are both
`data: null`, and the `next-held-by-others` warning is what tells them apart.
Exit `10` is what naming a task directly produces — `update <id> --assign self`
— which is the branch a fleet member takes when it was told to work on a
specific task rather than to ask for one.

Sharing is never an error, and it arises two ways.

- `--force` records your assignment beside the existing one, which is how two
  agents are deliberately paired on one task.
- A claim can lose the push race: the local write stands, `origin` refuses the
  push because somebody else's claim landed first, and the next fetch replays
  yours onto their tip. Both claims survive — the removal rule guarantees
  neither can erase the other — which is the spike the design treats as a
  meaningful outcome.

Either way you are told, by an `assignment-shared` warning naming who else holds
the task, and the command exits `0` because the assignment was recorded. The
command that claimed emits it when its own fetch already saw the other claim.
When the other claim arrives later, the fetch that replays yours onto their tip
emits it — `workbook sync`, `workbook fetch`, or whichever ordinary command
synchronized. It is said by the synchronization that did the replaying and not
repeated afterwards, and a synchronization that reconciles nothing does not go
looking.

`workbook next` with nothing to offer says so, and says which kind of nothing it
is: a board where every eligible task is already somebody else's answers with a
`next-held-by-others` warning rather than leaving a caller to conclude that the
work has run out.

The skip is about other *identities*, and only about tasks you have no part in.
A task you hold is still offered to you however many people share it: a task you
claimed and have not moved on is still the one you should be working on, and two
agents deliberately paired on one task would otherwise both be told there is
nothing to do the moment the pairing succeeded. `next --claim` on a task your
identity already holds writes nothing.

Two agents of one identity — `dylan@example.com/impl-1` and `/impl-2` — are
therefore both offered the same task; what separates them is the status. Taking
work up moves it out of the `next` column, and that is one command:

```sh
workbook update WB-01K0 --status in-progress --assign self
```

## Project statuses

A project's statuses are configuration, not a constant. `workbook status list`
shows them with the tasks in each; `add`, `rename`, `label`, `move`, `tag`,
`untag`, and `delete` change them; and `workbook status log` lists what has been
changed, oldest first, with the command that reverses each entry. There is no
undo verb: an inverse that is sometimes exact and sometimes not is worth less
than a printed command whose limits are stated beside it.

A status has a machine value — the lowercase token typed as `--status
in-progress` — a display label, a position, and any of three tags. `default` is
where a new task lands, `next` is what `workbook next` may return, and `done` is
what satisfies a dependency. Exactly one status carries `default`, so giving it
to another status takes it away in the same operation; at least one must carry
each of `next` and `done`, and a command that would leave none is refused with
the command that fixes it. A status with no tag is an ordinary column: work
rests there and nothing else in Workbook follows from it.

A project created before this release also defines `Blocked`, which used to be
one of the defaults. Task dependencies record what a task is waiting on, so the
column no longer earns its place, and new projects do not get it. Nothing is
migrated automatically: a status with tasks in it is not a release's to remove.
`workbook status list` names it and prints the removal, and the recommended one
is `workbook status delete blocked --into backlog` — the tasks re-file to
`backlog` by forwarding, and anything still stored under `blocked` keeps
resolving there. A project that wants the column keeps it, or adds one back with
`workbook status add`.

`workbook status list` is how a project's statuses are discovered rather than
assumed. It shows each status's position, machine value, display label, tags,
and the tasks resolving to it, followed by the values that are no longer live,
the stored values that resolve to nothing, and any advisory about the
configuration itself. Nothing else has to be read to know what `--status`
accepts here.

Changing a status never rewrites a task. History is append-only and other clones
may be offline for weeks, so a rename or a removal leaves a forwarding pointer:
tasks stored under the old value keep resolving to the right column everywhere,
and each one settles the next time something writes to it. `workbook status
delete` therefore requires `--into`, naming where the removed status's tasks
belong, and reports how many tasks that moves.

Forwarding is followed to the end of the chain, not one hop. A task stored under
`ready`, where `ready` was renamed to `sorting` and `sorting` was later removed
into `backlog`, is read into `backlog`: it is drawn in that column, it orders
against the tasks stored under `backlog`, and the next write to it records
`backlog` for good. That settlement is the only thing that ever rewrites a
stored status, and it happens on any write to the task — a title edit, a move, a
dependency change — not only on a status change.

A retired name can be taken again, and taking it with `workbook status rename`
moves history. The name stops forwarding the moment it is live, so every task
still stored under it — and every task stored under a name whose chain runs
through it — resolves to the renamed status instead of to wherever the
retirement had sent it. The rename is allowed, because the forwarding decays as
tasks are written to and an old column name is a reasonable thing to want back,
but it is never silent: the result carries a `status-rename-reresolves` warning
naming the retired name, what retired it, the live status its history used to
resolve to, the status it resolves to now, and how many active tasks moved. The
JSON result carries the same account in a `reresolved` member:

```
$ workbook status delete review --into backlog
$ workbook status rename ready review
Status:	rename	review
	from:	ready
	label:	Ready → Review (derived)
	tags:	next
	inverse:	workbook status rename review ready --label Ready	(not exact)
	note:	renaming back leaves "review" forwarding to "ready", so the tasks this rename re-pointed stay there rather than returning to "backlog"
	docs:	.workbook/guidelines.md	written
Sync:	skipped	no origin remote configured
workbook: warning: renaming "ready" onto "review" re-points a retired name: "review" was removed into "backlog"; 1 active task stored under "review" or under a name that forwards through it now resolves to "review" (formerly "ready") instead of "backlog"
```

```json
"reresolved": {"status": "review", "retiredBy": "status.remove", "via": "backlog",
               "previously": "backlog", "now": "review", "tasks": 1}
```

`previously` is the end of the chain and `via` its first hop, so the two differ
when the retired name was itself renamed or removed again later; `previously` is
omitted when the chain led to no live status at all. The count is exact for the
tasks this clone holds: it reads each active task's stored value, so a task a
later write already settled elsewhere is rightly not in it, and a task on a
teammate's clone that has not been published yet cannot be. A count of zero is
still warned, because those unpublished values move too. The printed inverse —
renaming back — is marked not exact, since the name it gives up forwards to the
renamed status rather than to where the retirement sent it. Renaming onto a
retired name whose history already resolves to the status being renamed — the
inverse of an earlier rename, or the name of a status removed into this one —
moves nothing and is not warned. `workbook status add` of a retired name also
stops it forwarding, and is not warned either: that is exactly what a removal's
printed inverse relies on to bring the removed status's tasks back.

Reading and writing treat a stale value differently, and deliberately.
Everything that reads a *stored* status resolves it. Everything that *writes* a
status a caller supplies requires a live member, without forwarding: `workbook
update <id> --status ready` is refused with `validation` (exit `5`) once `ready`
is no longer a status this project defines. A status a caller supplies to
*filter* by is resolved first and refused only if it resolves to nothing, so
`workbook list --status ready` still lists the renamed column's tasks. The
`workbook status` verbs answer the same value with what became of it — which
rename or removal retired it, on what date, and which live status it resolves
to now — rather than with a bare "not found". A task holding a status no chain
leads out of is still fully editable;
only supplying that status again is refused. See
[Statuses and priorities a project does not define](#statuses-and-priorities-a-project-does-not-define) for
how to file such a task, and
[Which exit code a status produces](#which-exit-code-a-status-produces) for why
membership is exit `5` while a malformed stored token is exit `7`.

Everywhere includes ordering. A task's status-and-priority bucket is its
*resolved* status, so two tasks stored under different tokens that forward to
one status share a bucket: they are drawn in one column, they rank against each
other, and `move` and `place` each accept the other's task as an anchor whenever
the bucket resolves to a status this project defines. Anything narrower would
let a `move` be refused against the card sitting directly above it on the board.

The two verbs part company for a bucket that resolves to nothing — tasks
stranded under a status no chain leads out of. `move` reorders them against each
other, because it names an anchor rather than a status; `place` cannot be asked
to, because it names the destination status and a status the project does not
define is refused at the mutation boundary. That is the boundary working as
intended: `place` is how a caller supplies a status, and supplying one that does
not exist is the mistake it exists to catch.

The configuration lives in its own synchronized ref, `refs/workbook/config`,
seeded the first time anybody changes a status and published wherever a task is
published. A project that has never changed a status has no ledger at all, which
`workbook status list --json` reports as `"seeded": false`. One ledger carries
everything a project configures — its statuses, its priorities, its
[board display settings](#board-display-settings) and its
[keys](#project-keys) — so all four are recorded the same way, reversed by the
command every result prints, and synchronized with everyone who fetches.

Reading with a status this project does not have is refused the same way
supplying one is: `workbook list --status <value>` exits `5`. An empty table
cannot be told apart from an empty column, so answering with one would throw
away the only interesting fact there was — that this checkout has never heard
of that name. If you expected the status because a teammate mentioned it, you
are out of sync rather than looking at an empty column, so the refusal says so
rather than reporting bad input: it names the value, lists the statuses this
project does define, and names the fix, which is to fetch.

```
$ workbook list --status shipped
workbook: no status "shipped" in this project; the statuses are: backlog, ready, in-progress, in-review, done; fetch if a teammate added it
```

Supplying the same value to a task — `workbook update <id> --status shipped` —
is refused as `invalid task status "shipped"` instead, because there the caller
is choosing a value and the value is the news. A value a rename or a removal
still forwards is neither case: it resolves, the tasks come back, and a warning
says what the name now means. `--priority` answers an unknown priority the same
way, naming this project's priorities.

`.workbook/guidelines.md` is a generated rendering of this configuration, not a
document to maintain. Its status table lists each status's position, machine
value, display label, and tags, with a legend saying what each tag makes
Workbook do, and the lifecycle prose under it names this project's own statuses
because it is read off the tags rather than written out. Every mutating
`workbook status` verb rewrites it, so the file an agent reads and the ledger it
describes cannot drift; `--no-docs` skips that for one command, and `workbook
docs update` catches up afterwards. A project with no managed guidelines block —
no file at all, or a file somebody wrote at that path themselves — is left
alone, because refreshing what a project has is not the same as installing what
it declined; `workbook docs install` is what creates the block.

A display label is authored by whoever can push to the configuration ref, so the
generated file treats one as untrusted text: control runes and newlines are
collapsed, a pipe is escaped so it cannot add a column to the table, a code span
is fenced long enough to survive the backticks the label carries, and the
managed block's own markers are neutralized in the body as well as by the block
format itself. A label carrying `<!-- workbook:end -->` would otherwise truncate
the block it was written into, and every clone would report a file nobody had
edited as locally modified — permanently.

Hand-edits are refused rather than lost. The managed block records a hash of
what Workbook wrote, so a file that still matches is rewritten in place and a
file somebody changed is left exactly as it is: the status change still lands
and is still published, and the result envelope's `docs` member reports the file
as `modified` and unwritten alongside a `docs-refresh-incomplete` warning naming
`workbook docs update --force`. Edit the project's statuses to change what that
file says; there is nothing to write in it by hand.

## Project priorities

A project's priorities are configuration too, and the whole of the section
above applies to them. `workbook priority list` shows them with the tasks at
each; `add`, `rename`, `label`, `move`, `tag`, `delete` and `color` change
them; and `workbook priority log` lists what has been changed, oldest first,
with the command that reverses each entry. There is no undo verb here either,
and for the same reason.

A priority has a machine value — the lowercase token typed as `--priority
high` — a display label, a position, one optional tag, and an optional color.
Position is urgency and nothing else: the most urgent priority is first
everywhere it is listed or drawn, and `--before` and `--after` are how a
priority is placed against the others rather than an index anybody counts.
`default` is the only tag, and it is where a task created without `--priority`
lands. Exactly one priority carries it, so giving it to another priority takes
it away in the same operation, and a command that would leave none is refused
with the command that fixes it — including `workbook priority delete` on the
priority that holds it.

`high`, `medium` and `low` are not the priorities there are. They are the ones
a project that has configured none is using, `medium` carrying `default`, and a
project that never runs a `workbook priority` verb keeps exactly those three
with its stored history unchanged to the byte.

What the first change writes depends on when the project was created. A project
this release created records its priorities in its configuration from its first
commit, so there is nothing to backfill. A project created before this release
has no priorities section at all, and the first `workbook priority` verb run
against it writes the built-in three into the same commit, ahead of the change
itself — the section then states what the project was already using rather than
a starting point this release invented, and the change on top of it is the one
somebody made. That commit is also what marks the configuration as needing a
v0.6.0 clone to change; see
[Mixed versions and forks](#mixed-versions-and-forks).

A color is optional. `workbook priority color <priority> "#rrggbb"` records one
and the same command with no value clears it, returning that priority to a
color the board derives from its position. Derived means derived from the
position a priority holds *now*, so adding or removing one restyles the others
that carry no color of their own — they are spread across the board's
palette, and a list of four is spread differently from a list of three. A priority with
a recorded color keeps it whatever moves around it, which is the other reason
to record one. There is no stored default to go
back to, which is why clearing is what it is: nothing was written, so nothing
is written back. Recording the color a priority already has, or clearing one it
does not have, is refused rather than recorded — a priority operation against a
pre-0.6.0 project is never free, and a command that changes nothing should not
be what costs a team its compatibility with an older clone.

`workbook priority delete` requires `--into`, naming the priority the removed
one's tasks belong at, and reports how many tasks that moves. It is never
guessed: where a task's urgency goes is a judgment, not an inference from
adjacent positions. Removing the last remaining priority is refused
outright — every task is at one — and so is removing the one tagged `default`,
until another carries the tag.

Changing a priority never rewrites a task, exactly as changing a status does
not. A rename or a removal leaves a forwarding pointer, a task stored under the
old value keeps resolving to the right priority in every clone, and each one
settles the next time anything writes to it. Supplying a retired priority to a
task is refused; filtering by one resolves and returns the tasks with a
`priority-filter-forwarded` warning saying what the name now means. A priority
that resolves to nothing refuses the filter, and names this project's
priorities in doing so, exactly as a status does under
[Project statuses](#project-statuses).

`workbook priority list` is how a project's priorities are discovered rather
than assumed, the way `workbook status list` is for its columns: it shows each
priority's position, machine value, display label, tag, color and task count.
`.workbook/guidelines.md` carries the same table, and every mutating priority
verb rewrites it; `--no-docs` skips that for one command and `workbook docs
update` catches up afterwards.

The web board's **Priorities** section does all of this from the browser —
adding, renaming, relabeling, recoloring, the `default` role, reordering and
removal — against this same ledger and with these same refusals, the way its
**Statuses** section administers the columns; see
[Board columns](#board-columns).

## Project keys

A task ID is `<KEY>-<ULID>`, and the key is the prefix — the `WB` in
`WB-01K0M6B8A4FTT8C39MXXYTW7C1`. A project may have more than one, so IDs in one
project can carry different prefixes and every one of them is that project's. A
repository made of several subprojects can therefore give each one its own
prefix, and a project that has outgrown the key it was created with can mint
under a new one without renaming a thing.

The keys are configuration, in the same ledger the statuses and the priorities
live in. `workbook key list` shows them in the order they were added, with each
one's state and the tasks under it; `add`, `current` and `retire` change them;
and `workbook key log` lists what has been changed, oldest first, with the
command that reverses each entry, exactly as `workbook status log` does.

Counting the tasks under each key is the one part of `workbook key list` that
reads every task, through the local projection, so it grows with the project.
`workbook key list --no-counts` leaves the counts out and never opens the
projection: the keys, their states and the current key come from the
configuration ledger alone. Its JSON is the same document with each key's
`tasks` member omitted, as the mutating key commands omit it, and its table has
no TASKS column. It is for a caller that asks often and needs only the current
key; the desktop app reads every project's key this way.

A key is **active** or **retired**, and exactly one active key is **current**.
The current key is where a new task is minted; any active key is one a create
may name. A retired key mints nothing, and that is the whole of what retiring
does: its tasks are untouched, they stay this project's, and they keep
resolving, listing, ordering and synchronizing as they always did. Nothing here
renames or deletes a key, and nothing rewrites a task ID. A task ID is a
permanent name, so a key that has ever minted one stays valid forever — which is
why there is no removal verb, and why retirement is what "we do not file new
work under that prefix any more" means.

`workbook key add <KEY>` adds an active key, last in the order and not current;
`--current` makes it current in the same change. A key this project already has
as active is refused, naming the command that was meant, and a key that does not
match `^[A-Z][A-Z0-9]{1,9}$` is refused with the grammar:

```
$ workbook key add NEW
workbook: project key "NEW" is already active; to mint new tasks under it: workbook key current NEW
$ workbook key add ops
workbook: project key "ops" must match ^[A-Z][A-Z0-9]{1,9}$
```

Adding a **retired** key back is how it is reactivated: it becomes active again
in the place it already had rather than as a new entry at the end, so the order
a project reads is still the order it added things in. Sixteen **active** keys
is the ceiling, and the seventeenth is refused with the retirement that would
make room rather than recorded. Retired keys are not counted: a key is never
deleted, so a ceiling on the whole list would be one a project could reach and
never come back under, and retiring one really does make room for the add that
was refused.

`workbook key current <KEY>` moves where new tasks are minted. The key that held
it gives it up in the same operation, so exactly one key is ever current. A
retired key is refused naming the command that brings it back, an unknown key is
refused naming every key this project has, and the key that is already current
is refused rather than recorded — a configuration change that would change
nothing is not written here either.

```
$ workbook key current WB
workbook: project key "WB" is retired; bring it back first: workbook key add WB
```

`workbook key retire <KEY>` stops minting under an active key. The current key is
refused, because new tasks would have nowhere to go, and so is the last active
key, because a project has to keep one; each refusal names the command that
comes first:

```
$ workbook key retire NEW
workbook: project key "NEW" is this project's current key, so new tasks would have nowhere to go; make another key current first: workbook key current <key>
$ workbook key retire WB
workbook: project key "WB" is this project's only active key, and a project must keep one to mint new tasks under; add another first: workbook key add <key>
```

Every one of the three prints the command that reverses it. An addition reverses
with `workbook key retire`, a retirement with `workbook key add`, and
`workbook key current` with the key that was current before.

An addition's reversal is marked **not exact**, because a key is never deleted:
retiring it stops it minting and leaves it on the list, which is not the state
the addition found.

```
	inverse:	workbook key retire THIRD	(not exact)
	note:	THIRD stays on this project's list as a retired key; a key is never deleted
```

`workbook key add --current` is inexact for a second reason — no single command
undoes both halves — and says which half it undoes rather than printing a
command that only half works:

```
	inverse:	workbook key current NEW	(not exact)
	note:	then `workbook key retire THIRD` if it should not stay
```

Each of the three synchronizes with `origin` unless `--no-sync` says not to, and
each regenerates `.workbook/guidelines.md`, whose "This project" table names the
key a new task is minted under; `--no-docs` skips that for one command and
`workbook docs update` catches up afterwards, exactly as for a status change.

`workbook create --key <KEY>` mints one task under another active key instead of
the current one. A retired or unknown key is refused, naming the keys a task may
be minted under. `workbook list --key <KEY>` lists the tasks whose IDs carry one
key: a retired key is accepted there, because its tasks still exist, and an
unknown one is refused naming this project's keys and saying that fetching is
what fixes a name a teammate has and this clone does not. Both flags name a key
that already exists, so both accept it in any case — `--key api` means `API` —
and the refusal names the key it looked for. `workbook key add` does not: it
records the name a project reads from then on, so it is held to the grammar.

```
$ workbook create "Under retired" --key WB
workbook: project key "WB" is retired, so no new task is minted under it; the active keys are: NEW
$ workbook list --key ZZ
workbook: no project key "ZZ" in this project; its keys are: WB (retired), NEW; fetch if a teammate added it
```

The ledger records only what somebody changed. A project that has never run a
`workbook key` verb has no key section at all — the founding key in its identity
ref is its one key, active and current — and its stored history does not change
by a byte. Its first key change writes the founding key ahead of itself in the
same commit, so the recorded order begins with the key this project's existing
task IDs already carry rather than with something this release invented; a
project's first priority change backfills the built-in priorities the same way
and for the same reason.

Which task IDs belong to this project is decided by the key set, so a ref under
a key the project does not have is another project's as far as this clone is
concerned. Adding the key does not make it this project's: the ref's documents
name the project that wrote them, and that is what every read compares against.
The ignored-ref report under [Explicit task sharing](#explicit-task-sharing)
names such a ref, offers nothing to run against it, and says the same thing
about a ref under a key this project *does* have whose documents name another
project.

Recording a key raises what the configuration history requires of a reader, the
way recording a display setting or a priority does. `key.add`, `key.current` and
`key.retire` carry minimum reader generation 3, the generation this release
introduces, so a teammate running a released Workbook reads the ledger as newer
than it can read: every read and every task synchronization still works, a
configuration change is refused with the message that says to upgrade rather
than one that says the project is corrupt, and — because that clone drops a key
section it has no field for and decides what a task ref is from the key in the
identity record alone — tasks minted under a key added since it last upgraded
are reported as another project's refs rather than as this project's tasks.
Everyone on the team upgrades together; see
[Mixed versions and forks](#mixed-versions-and-forks).

The web board's **Keys** section administers the same key set from the
browser — add a key, make one current, retire one, reactivate one — against
this same ledger and with these same refusals, the way its **Statuses** section
administers the columns; see [Board columns](#board-columns). Its new-task
form offers the key to mint under when there is more than one to choose from;
see [Local web board](#local-web-board).

## Board display settings

A project can say what its board is called and what color it draws itself in.
`workbook config set project-name Atlas`, `workbook config set primary-color
"#1a7f4b"` and `workbook config set text-color "#101820"` record the three, and
`workbook config unset <setting>` clears one. `workbook config show` reports all
three with their source — `configured`, or `default` when nobody has decided.

They are project configuration in the same sense the statuses are: recorded in
the shared configuration history at `refs/workbook/config`, synchronized with
everyone who fetches, and reversed by the exact command every result and every
`workbook status log` entry prints. `--no-sync` records one without talking to
`origin`, and two clones that named the project differently get a
`display-setting` conflict naming both values rather than a silent winner. A
change to a different setting, or the same value chosen twice, converges without
a conflict — the three values are independent.

The quotes around a color are the shell's, not Workbook's: `#` starts a comment
in every POSIX shell, so an unquoted color reaches the command as a missing
argument.

A color is six hexadecimal digits behind a hash, in either case; it is stored
lowercase, because the configuration checkpoint is compared byte for byte and a
color with two spellings would be two configurations. A project name is
trimmed, must not be blank, and must not exceed 100 bytes. Clearing a setting is
how a project has none: there is no stored default, and a board with nothing
configured renders exactly as it did before this existed.

Any color that is six hexadecimal digits is accepted, including one that makes
the board hard to read: a very pale primary or text color draws the web board's
links, hairlines and prose — including the text you type into the Board settings
form itself — close to invisible against its white surfaces. There is no
contrast floor, because which colors a project wants are the project's business.
The way back is always open from the command line, which does not use these
colors at all: `workbook config unset primary-color` or `workbook config unset
text-color` restores this board's own palette, and a page reloaded afterwards is
legible again. On the web board itself the Board settings form's field labels
keep a fixed color rather than the project's, so the form can still be found and
emptied — but a pale primary draws every filled button on the page, Save and Add
status among them, white on white, and the command line is the reliable way out
of that one.

A command that would change nothing is refused rather than recorded: `config
unset` of a setting nobody has configured, and `config set` to the value already
stored, both exit 5 and leave the configuration history exactly where it was.
The board's own **Board settings** form on `/config` upholds the same rule from
the other side: it sends all three values at once and the server records only
the ones that moved, so a Save you have not edited writes nothing and answers
with the configuration as it already stands.

Recording any display setting raises what the configuration history requires of
a reader. A clone running v0.5.0 or earlier keeps reading the project and keeps
synchronizing every task, and reports that the configuration was written by a
newer Workbook when it is asked to change a status — with the upgrade message
rather than a corruption report. On such a clone `workbook validate --full`
exits 9 rather than replaying a ledger it cannot fold, and a `workbook sync`
exits 9 with the configuration reported as `needs-upgrade` once its own
configuration history has diverged from origin's — while the tasks in that same
run still synchronize and every read still works. A project that never
configures a display setting is unaffected: the marker is per operation, a
refused no-op records none, and a ledger carrying no display operation is
exactly the ledger those versions already fold.

## Task change history

`workbook show` gains two opt-in views. Plain `workbook show` is unchanged, and
in `--json` each view's member is omitted entirely unless its flag is given, so
existing consumers see the same shape they always did.

```sh
workbook show WB-01K0M6B8A4FTT8C39MXXYTW7C1 --history
workbook show WB-01K0M6B8A4FTT8C39MXXYTW7C1 --history --limit 25
workbook show WB-01K0M6B8A4FTT8C39MXXYTW7C1 --history --all
workbook show WB-01K0M6B8A4FTT8C39MXXYTW7C1 --compare <commit> <commit>
```

`--history` lists one row per operation pack rather than one per operation,
because actor, wall time, and logical clock are all pack-level. A pack touching
several fields renders as one row naming them, "changed title and status". The
default shows the ten most recent changes and says what it left out — "Showing
10 most recent changes out of 200" — while `--limit <n>` and `--all` widen the
window.

Each field renders in its own terms. Title, status, and priority read as old to
new. Rank reads as "Reordered" with no values, because a rank is opaque and its
literal value tells a reader nothing. Description is the only field needing a
real diff, so Workbook computes a word-level diff and marks it the way
`git diff --word-diff` does: `Alpha beta [-gamma-]{+delta+}`. In `--json` that
diff is a list of `{kind, text}` spans, and concatenating the equal and delete
spans reproduces the old text exactly while the equal and insert spans reproduce
the new one.

Entries are ordered by the parent chain, not by wall time, and wall time is
printed as attribution only. Replay preserves an operation's wall time while
rewriting its logical clock, so after a reconciliation the two orders
legitimately disagree. Workbook shows what the chain says and lets the
timestamps read out of order rather than reordering them; that disagreement is
the visible fingerprint of replayed work.

`--compare` diffs the two commits in the order given, the way `git diff` does,
and never sorts them: operation ULIDs sort by authoring time and no longer track
chain position once a task has been reconciled. Both arguments are full Git
commit object IDs, exactly as `--history` prints them.

Addressing entries by commit object ID works across a reconciliation because
reconciliation parks the pre-replay tip under
`refs/workbook/reconciled/<task-id>/<n>` rather than discarding it, so an object
an earlier listing named usually stays reachable. That reachability is bounded,
not permanent: of the parked tips whose edits the task already carries,
Workbook keeps the three newest per task and retires the rest inside a later
mutation's ref transaction, after which those pre-replay chains are
collectable. A parked tip that holds an edit the task lacks is kept until
somebody dismisses it on the board. A named object that no longer resolves is an
ordinary not-found for that argument, exit `4`, naming the commit rather than
reporting corruption.

A [compacted](#task-archive-compaction) task has no step-by-step history before
its checkpoint. `--history` lists the changes made since the checkpoint, then
one line taken from the checkpoint's summary, with dates in local time and the
year shown only when it is not the current one:

```text
12 earlier changes from Jul 3 to Sep 19 were compacted on Nov 10.
```

In `--json` the same facts are the `history.compacted` member, `{"changes",
"from", "to", "on"}`, present only for a compacted task. The checkpoint itself is
not a change: it is not among `changes` and is not counted in `total`.

Both views are served from the SQLite projection's `operations` table, which
stores operations alone and reconstructs any state by replaying from the root;
`--compare` replays both endpoints in full. Where the projection holds no
operations for a task, or for a commit it does not hold — every parked
pre-replay tip, since the projection is fed from `refs/workbook/tasks/` only —
Workbook falls back to a bounded Git read so `show` still answers.

## Recording past work

`WORKBOOK_NOW` fixes the time a command records, in place of the system clock.
It exists to import a project's past: a migration runs each step with the time
that step really happened, so the history reads as it would have had Workbook
been there to record it.

```sh
WORKBOOK_NOW=2024-03-04T09:00:00Z workbook setup --key WB
WORKBOOK_NOW=2024-03-05T14:00:00-05:00 workbook create "Ship the importer"
WORKBOOK_NOW=2024-03-09T16:30:00Z workbook update WB-01HR --status done
```

The value is an RFC 3339 time. A task operation records it as given, offset
included; a configuration pack records the same instant in UTC. It
moves everything written into history: each operation pack's wall time, and with
it a task's `createdAt` and `updatedAt`, a comment's or assignment's time, and
every `--history` and `status log` row; the time encoded in every identifier the
command mints, from the project and task IDs to each operation's; and the
configuration packs `setup` and the `status`, `priority`, `key`, and `config`
commands write, whether typed or made through the board `workbook serve` runs.
A value that does not parse is refused with exit `5` before the command reads or
writes anything; only `help` and `version`, which record nothing, still answer.

`workbook compact` runs at that time as well: its cutoff is measured from it,
so `--done-only 30d` selects what had been done for thirty days by then, and the
checkpoints and the run ID are dated with it.

Identifiers minted at the same instant share their first ten characters, the
part that encodes the time. Tasks imported with one timestamp between them,
such as a tracker export that kept only the day, therefore need a longer prefix
than usual to be told apart, or the full ID.

It does not move the dates on the Git commits that carry those operations,
which are Git's to stamp. Workbook's Git commands inherit the environment, so
setting `GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` beside it dates those too.
Nor does it touch what measures the running process rather than the work: sync
watcher freshness, timeouts, and relative ages such as how long ago a task was
assigned stay on the real clock.

Nothing checks the order of the times given. Wall time is attribution only, so a
step recorded earlier than the change before it is stored as given, and
`--history` still lists it in chain order, as described above.

## Local mutation durability

Existing-task mutations plan from the validated current state in the SQLite
projection. With a full task ID, Workbook inspects that task's exact Git ref
before planning; when the ref has advanced, Workbook validates and refreshes
that projected task from its current Git tip. Operations that need cross-task
information, such as prefix resolution, task ordering, or dependency checks,
refresh the relevant global projection first.

After planning, a state-changing mutation writes the immutable operation and
complete current state as Git objects, then synchronously compare-and-swaps the
canonical `refs/workbook/tasks/<task-id>` ref. The CLI or HTTP response reports
that state change as successful only after the Git ref advances. Workbook then
conditionally advances SQLite from the parent it observed to the new Git head,
so an older request cannot overwrite a newer projection row.

Idempotent no-ops can also return successfully without advancing Git or SQLite:
for example, removing a dependency that is already absent, adding a dependency
that is already present, or moving a task when its calculated rank is unchanged.
These responses return the already-observed task state rather than recording a
new operation.

SQLite remains a disposable read projection, not a second durability boundary.
If a successful mutation includes a `projection-update-failed` warning, the Git
mutation succeeded but the local projection could not be advanced. Subsequent
reads may repair the affected row; `workbook rebuild` recreates the projection
from the canonical Git refs when recovery is needed and reports its task count,
its relic count and the cache path; see [The relic index](#the-relic-index).

## Explicit task sharing

`workbook fetch` downloads `refs/workbook/tasks/*` from `origin` into
`refs/workbook/remotes/origin/tasks/*`, and `refs/workbook/project` into
`refs/workbook/remotes/origin/project`, in one `git fetch`. The project identity
is settled before any task is inspected: a clone and an origin that name
different projects stop the run rather than replay one project's operations into
another's history. `fetch` never publishes the identity ref — downloading is not
the moment to write to `origin` — while `sync` publishes it once, in the run that
migrates a project to v0.5.0. It validates the current tracking and
canonical tips' IDs, documents, and safe ancestry relationship before touching
the corresponding local task ref. A missing local task is created, and a behind
local task is fast-forwarded in one compare-and-swap transaction. Local-ahead
tasks are left alone; a divergent task has its local-only operations replayed
onto the fetched tip in the same transaction, and a task a teammate compacted is
adopted (see [Task archive compaction](#task-archive-compaction)). Invalid fetched data remains
isolated to its own task and causes a nonzero exit; valid unrelated tracking
refs can still reconcile in that run, and because the fetch phase ran to
completion the publication that follows it in a `sync` or an automatically
synchronizing mutation still happens. Stale refs are pruned only from Workbook's
isolated tracking namespace, allowing `sync` to republish an intact canonical
task ref if its remote counterpart was removed externally.

The same `git fetch` downloads `refs/workbook/relics/*` into
`refs/workbook/remotes/origin/relics/*`. After every task ref has moved, each
relic that is missing or behind locally is created or fast-forwarded, and a
relic changed on both sides has its local commits replayed onto the fetched
tip, in a compare-and-swap transaction of its own. `push` and `sync` publish
the relic refs in a second `git push` after the task push, without force and
without deletion. A relic that cannot be replayed is a
[relic conflict](#a-relic-conflict): its local tip is parked, `fetch` and
`sync` exit `8`, and every other relic and every task still synchronizes. A
name under `refs/workbook/relics/` on `origin` that is not one of this
project's relic IDs is skipped and reported on an `Ignored:` line like a task
ref's. Text output and JSON list each relic a phase changed; see
[Sharing relics](#sharing-relics) and [Relic JSON results](#relic-json-results).

`refs/workbook/tasks/*` on `origin` is writable by everyone with push access and
is not covered by branch protection on typical hosts, so a name Workbook does
not recognize there is not treated as corruption. `fetch`, `push`, and `sync`
skip such a ref and complete for every well-formed task, then report it: an
`ignoredRefs` entry in JSON and an `Ignored:` line in human output, named as
`origin` holds it. Hearing about it does not depend on running one of those
three in the foreground: `workbook setup` names what its bootstrap
synchronization skipped, a mutation that synchronizes inline names it beneath
its `Sync:` line, and a watcher names each newly skipped ref on its terminal and
carries the current set in `workbook sync --status`. A mutation that defers to a
running watcher does not repeat the report; the watcher's own two surfaces carry
it. One stray ref would otherwise deny the whole synchronization path to every
clone. The local canonical namespace keeps rejecting an unrecognized name
outright, because only this tool writes it. Anything that is not a name — Git's
own record framing, object IDs, symbolic refs, or the same task returned twice —
stays fatal in both namespaces.

Workbook never deletes a ref on `origin`, and being unreadable to this build is
not evidence that a ref is junk: a newer version's task ID format and a second
project's key sharing the namespace both land in the same report while naming
real append-only history. Each entry therefore carries a `plausibleTask`
boolean, true when the name still fits some Workbook's task ID — one of
[this project's keys](#project-keys) with an ID format this build predates, or
any valid project key followed by a ULID-shaped body, including a ref nested
under either or a peeled name pointing at one. Human output states that verdict
on each ref's own line, as `no project's task` or `may be another Workbook's
task`, so a mixed report never leaves a reader matching advice to a name by
guesswork. Only a name that fits neither is offered for removal, with
`git push origin --delete <ref>`; an entry that may be another project's or
another version's task is reported as kept, together with what deleting it would
cost. Judge such a ref yourself before removing it.

Which names are this project's is decided by its whole key set, active and
retired keys alike, rather than by the key in its identity record. A ref under a
key this project does *not* have — the ID itself, or the peeled `…^{}` form of
it — is reported with the key it carries and with the keys this project has:

```
Ignored:	refs/workbook/tasks/OPS-01M32R58CBNDXTAAQBCQFSXYAZ	may be another Workbook's task	task ID "OPS-01M32R58CBNDXTAAQBCQFSXYAZ" carries project key "OPS", which this project does not have; its keys are: WB (retired), NEW
```

No command is offered for such a ref, and `workbook key add OPS` is not one: the
ref carries another project's task documents, which every read refuses whatever
this project's keys say, so adding the key would change the report's wording and
nothing else. A second key on one origin is a second project identity — a split,
a re-initialization, or another project pushing into the same remote — and the
report's job is to name it and leave it alone.

Adding a key a stranger's tasks are minted under is therefore a mistake the
report absorbs rather than one that breaks synchronization. A fetched ref whose
name this project's keys do cover, but whose documents carry another project ID,
is reported the same way, with that mismatch as its reason, and the fetch
completes:

```
Ignored:	refs/workbook/tasks/QQ-01M32X6CZRQNKG4QYJVHM3162K	may be another Workbook's task	task ID "QQ-01M32X6CZRQNKG4QYJVHM3162K" carries this project's key, but its documents carry project ID 01M32X6BJ6T19THJJVDBA407HG rather than this project's 01M32X6A5MF3JQF2TZ2S5VFC28, so the ref belongs to another project sharing this origin
```

`workbook key retire QQ` — or leaving the key where it is — costs nothing but
the line, because a key is never deleted and its refs are read whatever its
state. The local canonical namespace is stricter: only Workbook writes there, so
documents naming another project under `refs/workbook/tasks/*` are corruption
and are refused as such.

`workbook push` publishes validated local `refs/workbook/tasks/*` refs to
`origin` without deletion, and without force except for the leased push that
publishes a [compacted task](#publishing-a-compaction). One bounded, non-atomic publication retains
per-ref outcomes, so an unrelated task can publish even when another task is
rejected as non-fast-forward. Invalid local tips are omitted. The command
reports every outcome and exits nonzero if any ref is invalid, rejected, or
changes during publication.

`workbook hooks install` opts the clone into automatic task publication during
an ordinary `git push origin`. The managed pre-push hook is recursion-safe and
blocks the code push when Workbook task publication fails. Installation is
idempotent for Workbook-managed hooks. An existing non-Workbook pre-push hook is
never overwritten; Workbook instead prints manual chaining guidance. Hooks are
optional convenience only and are not required for correctness. The hook
watches for a push to `origin`, the code's remote, whatever the tasks remote is;
the `workbook push` it runs publishes to the project's tasks remote.

A refusal about where the tasks live never blocks the code push. When the hook
runs it, `workbook push` reports the `tasks-moved`, `remote-missing`,
`not-read`, `unreachable`, `move-not-done` and `published-after-move` refusals
(see [Setup and the refusals](#setup-and-the-refusals)) on standard error with
the command to run, and exits 0, so a checkout that has not picked up a move, or
has no access to a separate tasks repository, still pushes its code. The tasks
stay recorded in the checkout. Every other failure, such as a task ref that does
not validate, still stops the push. Installed hooks need no reinstall for this:
`workbook push` recognizes the variable the hook sets.

Everything else in this section says `origin` because that is where a project
shares its tasks unless its settings name another remote; with one named, read
that remote for `origin` throughout. See
[A tasks remote other than origin](#a-tasks-remote-other-than-origin).

`workbook sync` runs the full sequence against `origin`: fetch Workbook task
refs into the isolated tracking namespace, validate and fast-forward/create
compatible local task refs, replay any divergent local history onto the fetched
tip, then publish the resulting local tips. Reconciliation is per task and
publication covers every canonical tip, so one task that needs a decision leaves
every other task fetched, replayed, and published, and publishes its own
replayed prefix too. The same holds for a task whose tip on `origin` failed
validation: the push phase is gated on the fetch phase having completed, not on
whether it reported failures, so a single malformed tip is reported and exits
nonzero while every other task is still published. The command does not replay every buried
checkpoint during ordinary synchronization; that is reserved for the explicit
`workbook validate` audit. The command never fetches or pushes code branches and
does not create a hidden tasks branch.

## A tasks remote other than origin

A project shares its tasks through `origin` unless its settings name another
Git remote. Keeping the tasks in a repository of their own, one that holds
tasks and nothing else, means a key or a person given access to it is given
nothing of the code. That is the reason to do it: a [hosted board](hosting.md)
needs push access to wherever the tasks live.

The choice is recorded in the tracked `.workbook/config.json`, so it travels
with the code like every other project setting, as an optional `tasksRemote`
member of the version 2 document:

```json
{"format":"workbook.project","version":2,"projectId":"01M…","key":"WB","tasksRemote":{"name":"tasks","url":"git@github.com:team/tasks.git"}}
```

`name` is the Git remote every checkout uses for the tasks, and `url` is the
address `workbook setup` adds it from. Each checkout keeps its own address for
the remote: if a checkout already has a remote of that name, setup leaves its
address alone and says so, which is how a person uses SSH where the file says
HTTPS. Setup writes nothing to the committed file.

A project that never chose has no such member, and its file, its JSON output
and every ref it writes, locally and on `origin`, are what they were before the
setting existed. Its Git commands differ only by checks that read, and these
are all of them:

- every command's first local listing of identity refs also names
  `refs/workbook/remotes/origin/project`, origin's tracking identity ref, which
  the checkout already holds, so a marker published to `origin` is noticed
  without asking `origin` anything. A command that hands its change to a
  watcher runs nothing else new: it decides from that same listing;
- a publication of task refs, by a command, a watcher round or a board, reads
  the histories it is about to publish once more, three local Git processes,
  to refuse one that records an operation twice;
- after `origin` accepts task refs, that publication lists `origin`'s identity
  ref, one ref, to report an edit that landed just as `origin` was marked as
  moved;
- a board save published inline lists `origin`'s identity ref before its push,
  one ref, every time; the release before asked once, at the board's first
  publication, and trusted that answer for as long as the board ran, which
  would let a board running across a move go on publishing to the old place;
- a board save handed to a watcher first lists origin's tracking identity ref
  locally once more, one local Git process, because the board's last listing
  can be older than the watcher's last fetch.

One difference is not a check. A board hands its saves to a watcher, its own
background loop unless a `workbook sync --watch` is already running, and in
the release before that hand-off was never accepted, so the board published
every save itself before answering. It is accepted now. A save's response
returns before the edit is on `origin`, which holds it a few seconds later; if
the watcher's last round failed the board publishes inline as before, and if
an outside watcher dies the board's own loop publishes instead. A save that meets a teammate's conflicting edit no longer carries an
`auto-sync-incomplete` warning on its response, because the conflict is found
by the round that follows: the outcome is the same, the teammate's text kept
and the board's shown on the task page as a set-aside edit.

A test records the Git commands of a scripted session, including one command
handed to a watcher, against a build from before the setting and fails on any
difference beyond the first three; the board's two are covered by their own
tests. Measured against a local remote, an inline `workbook update` goes from
two connections to the remote to three (the listing after the push), and an
inline board save from one to three (the listings before and after). A command
handed to a watcher opens none, as before; a board save handed to one goes
from one connection, its own push, to none; and the watcher's round goes from
two to three.

A project whose tasks moved to their own repository and came back to `origin`
costs the same connections as one that never moved them: measured the same
way, an inline `workbook update` makes three (fetch, push, the listing after
it), an inline board save three (the listing before, the push, the listing
after), an update handed to a watcher none, the watcher's round three, and a
board save handed to a watcher none itself, that watcher's round three, in
both kinds of project. A board's own loop runs the same round, on a timer that
adds a fetch whenever it falls due, which is why the test hands the board's
save to a watcher that waits an hour between scheduled rounds. `origin`
lists the return recorded on its identity ref rather than the identity commit
itself; a checkout that has read that return to be live, in this command or
by an earlier fetch into its tracking ref, takes it as agreement without
fetching it again. What it does cost is local: reading the return's commits
adds a few Git processes to a command (21 rather than 17 for a handed update,
42 rather than 38 inline, when measured). A test measures every one of these,
logs the processes and fails only on a difference in connections.
Comparing operations by content as well as ID, below, runs no Git command.

### Choosing the remote and moving the tasks

The move is one procedure, run once by one person from a clone of the code:

```sh
# 1. An empty repository for the tasks, created on your host.
# 2. Record it, then move the tasks there:
workbook config set tasks-remote tasks --url git@github.com:team/tasks.git
workbook move-tasks
# 3. Publish the setting with the code:
git add .workbook/config.json && git commit -m "Keep tasks in their own repository" && git push
# 4. Everyone else, after pulling that commit:
workbook setup
```

The order of steps 2 and 3 is the one to follow: run `move-tasks`, then commit
and push the setting, so a teammate's setup after pulling works straight away.
Committing and pushing the setting first is safe too, for a first move and for
a move back. A teammate who pulls it before `move-tasks` has run is refused,
exit 3, publishes nothing to either place, and keeps every edit not yet
published: for a first move, setup says the move was never finished
(`move-not-done`); for a move back, the place the setting names is still
marked. When no move recorded in the markers ever took the tasks to that
place, every command and setup there say that the change of `tasks-remote` is
committed and its move has not been run, with `command` `workbook move-tasks`,
since pulling and running setup cannot help. When some move did, as for a move
back to a tasks repository the tasks went to before, the teammate's settings
cannot be told apart from stale ones, so they are told to pull and run setup,
`command` `workbook setup`, with a sentence saying that whoever changed
`tasks-remote` runs `workbook move-tasks` first. Once `move-tasks` has run,
their setup works with nothing more to pull.

`config set tasks-remote <name> --url <address>` refuses a name that is not a
single-component Git remote name (letters, digits, `.`, `_` and `-`), `origin`
with an address, a missing `--url`, an address that begins with `-` or holds
white space, an address carrying a password, any user name in an `http` or
`https` address (which is how tokens are usually passed), and origin's own
address, spelled with or without a trailing `/`, a `.git` suffix or a `file://`
scheme, as a relative path, or through a symbolic link. That last check is a
first line only: the spelling of an address cannot prove two repositories
differ, so `move-tasks` and `setup` decide it by what the repositories hold (see
below). An SSH user name such as `git@` is kept. `config set tasks-remote
origin` with no address and `config unset tasks-remote` both go back to sharing
through `origin` by removing the member. The command changes only the file:
until setup runs, this checkout's task commands say to run it. Its `Next:` line
says what is left to do, from the settings it replaced: before a move, only to
commit the file, since the tasks never left `origin` and the tasks repository
keeps the copy it holds; after one, to run `workbook move-tasks`, which moves
them back, then to commit and push the file, and that committing it first is
safe too.

### Moving the tasks back

The tasks can move back: to `origin`, or to any repository they
left, with the same two commands that moved them away:

```sh
workbook config unset tasks-remote         # back to origin, or:
workbook config set tasks-remote tasks --url git@github.com:team/tasks.git
workbook move-tasks
git add .workbook/config.json && git commit -m "Move the tasks back" && git push
# Everyone else, after pulling that commit:
workbook setup
```

A place the tasks left keeps the copies of their refs it held when they left,
and a move back starts from those. They are brought up to date by the same
synchronizations a first move makes, so each operation made at the newer place
since arrives once: a task nobody edited there is already identical, one edited
there is fast-forwarded, one an earlier move carried by replay is carried by
operation identity, and one the returned-to place no longer holds, because
somebody removed it, is published afresh. A task compacted at the newer place in
between arrives as its checkpoint, which replaces the older history the
returned-to place kept for it the way any [compaction](#publishing-a-compaction) is
published: a non-fast-forward push of that one task ref that succeeds only while
the place still holds exactly the tip this run read there. An edit a teammate
published to that older copy after the tasks left is replayed onto the
checkpoint first, so it arrives with the rest. One that cannot apply, because
the task was deleted and compacted, is set aside the way a move sets aside an
old place's edit to a task deleted at the new place: the copy holding it is
parked in this clone under `refs/workbook/reconciled/<task>/<n>`, the run
replaces it with the checkpoint and exits 8 naming the task by title and ID
and saying it was deleted and compacted at the place left, so the change made
at the returned-to place cannot apply, and the task stays deleted and
compacted. A run stopped before that replacement reports it when run again,
under the ref it parked; after it, no run meets it again. With `--no-backup`,
no bundle holds the replaced copy, so a set-aside edit survives only in the
mover's parked ref and in the clone of whoever made it. No other ref moves
backwards, and none is deleted. Removing the old copies is not part of a move
unless `--clean-up` asks for it (see [Cleaning up after a move](#cleaning-up-after-a-move)),
and a move works whether they are there, stale, compacted or gone.

Before the first such push in a run, `move-tasks` writes a Git bundle of every
tip it is about to replace, as [`compact`](#the-backup-bundle) does: at
`<git-common-dir>/workbook-archive/<run-id>.bundle` by default, elsewhere with
`--backup <path>`, and not at all with `--no-backup`; the two cannot be
combined, and a path that already exists is refused. The bundle holds each
replaced tip under the name this clone tracks it by,
`refs/workbook/remotes/<remote>/tasks/<id>` (not `refs/workbook/tasks/<id>`,
so fetching from it never overwrites a live task), and the same manifest, with
`move-tasks` first among its flags. It is verified before the push. The push is
leased on exactly the tip that was bundled: if the place changed since, nothing
is pushed over it, and the run reads the place again, writes another bundle
(the run ID with `-2`, `-3` appended, or beside the `--backup` file) and tries
again, up to three times. Each path is printed as a `Backup:` line, also when
the run then stops with an error, and the tasks it replaced as a `Replaced:`
line. A run that replaces nothing writes no bundle.

The place the tasks come back to records it with one commit on top of its
marker, a fast-forward (see [The moved marker](#the-moved-marker)). The place
they leave is marked as any old place is. Moves can follow each other any number
of times in any order (`origin` to `tasks`, back to `origin`, then to a third
repository, and so on), and each place's identity ref says by its tip alone
whether the tasks are there or where to look next.

`workbook move-tasks [--from <remote>] [--allow-branches]` moves the tasks
from the old place to the remote the settings name, adding that remote to this
checkout first, or to `origin` when the settings name none and `origin` is a
place the tasks left; with neither, it is refused, exit 5, saying the settings
name no tasks remote to move to. The old place is the remote `--from` names or,
without it, the place that holds the tasks now, found by following markers by
the remote names they record. For a new place the tasks never left, the walk
starts at `origin`: after a second move it is the place the first move went to,
which is the remote `config set` names in the command it prints. For a place
they left, it starts at the place they went when they left it, so it ends at
the place they are in now or, on a run finishing a move back, at the place
already marked as moved to the new one. A marker naming a remote this checkout
does not have is refused, exit 5, saying to add it with `git remote add <name>
<url>`, which the message spells out, and run the command again, or to name
this checkout's own remote for that address with `--from`; a walk
that comes back to a place it passed is refused, exit 8, saying the markers form
a circle, that the move between the places on it is unfinished, and to finish
that move first with `workbook move-tasks` from a clone whose settings name
where it was taking the tasks. An old place, named or found, that is already
marked as moved somewhere other than the new place is refused, exit 8, before
anything is written to the new place. Before it writes anything it lists both
remotes, and refuses a new place that is the same repository as the old one,
with the `same-repository` reason, exit 5, however its address is spelled. It
tells the two apart by what they hold: it writes a ref named by a fresh ULID
under `refs/workbook/probes/` to the new place, looks for it at the old place,
and removes it. A separate repository cannot show that ref, so a fresh tasks
repository and one filled beforehand are both accepted, and the same repository
shows it through any spelling, link or case of its address.

That namespace is the command's own. A run removes, with its own probe, every
probe an earlier run left at the new place, which a stopped run leaves; those
are the ones whose names sort before its own. A new place that refuses to delete
refs, as a host's rule against deletions does, is refused, exit 1, saying which
probe could not be removed and that deleting refs under `refs/workbook/probes/`
must be allowed there or the probe removed by hand. A run that then finds that
probe still there stops before writing another, so at most one is ever left.

Then it refuses a new place that holds branches, exit 5, unless
`--allow-branches` says that is meant or the new place is the code's own
repository, because a repository for tasks holds only Workbook refs. The code's
own repository is the remote named `origin`, and only in a checkout whose
`HEAD` is one of the branches `origin` lists, or an ancestor or descendant of
one this checkout holds: settings can never name `origin` as a tasks remote,
so no other repository can pass for it, and what the destination's own refs
say, a moved marker included, never excuses the check, since a mirror of a
marked `origin` carries that marker too. Never give it for a mirror, a repository something updates
from the code repository, such as a host's mirroring feature or a job running
`git push --mirror`: that update would delete or overwrite the tasks' refs at
each run, because they are not in the code repository's copy. The refusal and
the command's help say the same. Then:

1. reads the old place into its tracking refs, and nothing else, except that
   the old place's relics are folded into this checkout's relic refs at once,
   replaying any local relic change onto them as a fetch does;
2. synchronizes with the new place, carries onto this checkout's tasks every
   operation the old place holds that they lack, and synchronizes again, which
   publishes every task, the configuration ledger and the identity ref there.
   A new place the tasks left earlier is still marked then, and every other
   checkout is still refused there; this run alone treats it as live, so it is
   brought up to date before anybody uses it;
3. marks the old place;
4. reads the old place again, for edits published there before the mark or
   since;
5. carries and publishes those the same way;
6. for a new place the tasks left earlier, records their return there (see
   [The moved marker](#the-moved-marker)), after which checkouts whose
   settings name it use it again;
7. lists both places afresh and requires the new place to hold, task by task,
   every operation ID the old place holds, or to have it set aside in this
   checkout. A difference is reported by naming each task and the operations
   it lacks. The same holds for relics: every relic the old place holds must
   be at the new place at the same commit, or with every operation held there
   under other commits or in a relic tip parked in this checkout. A relic that
   is not is named as `relic <id> (lacks <n> change(s))`, counting commits.

Carrying is by operation identity. A task the new place holds at the old
place's commit is `identical`; one whose every operation it holds under other
commits, because it was edited there after the copy or because an edit was
carried onto its history, is `carried`. An operation the new place lacks is
applied once on top of its history by the ordinary replay, which skips every
pack that history already records, so a rerun never writes an operation twice.
One that conflicts with the new place's history is set aside instead: the old
place's history for that task is parked in this checkout under
`refs/workbook/reconciled/<task>/<n>`, and the run finishes and exits 8. Its
message names each task by title and ID and says why its edit cannot apply:
that its description was changed at both places, that it was deleted at the
new place so the old place's change to one of its fields cannot apply, or that
the old place's dependency would close a cycle at the new place. It says the
text is kept in this clone under the parked ref, that `workbook serve` run from
this clone shows it on its task as a set-aside edit, and that what is wanted of
it has to be applied by hand. A later run in this clone lists it in `setAside`
without a `parkedRef`, exits 0 and does not try it again. Dismissing the
set-aside edit on the board deletes the parked ref, so the next run tries the
edit again, meets the same conflict, parks and reports it once more, and is
quiet after that.

Relics move with the tasks: `move-tasks` fetches the old place's relics
itself, and the two synchronizations with the new place publish them there.
A relic change that cannot be replayed is a
[relic conflict](#a-relic-conflict): its local tip is parked under
`refs/workbook/reconciled-relics/<relic>/<n>`, the run finishes and is
verified, and it exits 8 with the conflict's detail. `--clean-up` then
deletes nothing, and its `Cleaned up:` line names the
`workbook reset --remote <remote>` to run once the conflict is dealt with. A ref
under `refs/workbook/relics/` or `refs/workbook/tasks/` at the old place that
is not one of this project's relics or tasks cannot be carried, so the run is
not verified: it exits 8, and `--clean-up` deletes nothing. Running the command
again cannot change that, so the error names each such ref and the command that
removes it at the old place:

```text
workbook: origin holds a ref the move cannot carry, because it is not one of this project's tasks or relics: refs/workbook/relics/not-a-relic; the move stays unverified while it is there, so remove it there, after keeping a copy if another project needs it, with git push origin --delete refs/workbook/relics/not-a-relic, then run workbook move-tasks again
```

Look at what such a ref holds first: on a remote shared with another project it
may be that project's task or relic.

A conflicting edit that reaches the old place late is reported only to whoever
runs `workbook move-tasks`, in the clone they run it from: setup and sync read
no task history from the old place. Sync does not contact it at all. Setup
lists its identity and task refs and fetches its identity commit, to refuse a
move that was never finished, and looks no further. A teammate's clone
learns of it only by running `workbook move-tasks` there, which reports it the
same way.

Every step is safe to repeat: an interrupted run, wherever it stopped, is
finished by running the command again, a run whose marker is already published
reports `"marked":"already"`, one whose return is already recorded reports
`"returned":"already"`, and a run at any later time carries whatever reached
the old place since. The old place is marked before the new place's return is
recorded, so no interruption leaves two places live at once: stopped between
the two, both places are marked, each naming the other, and every checkout,
on either generation of the settings or freshly set up, is refused at both
with the circle refusal (`command` `workbook move-tasks`) and publishes
nothing anywhere. Running `workbook move-tasks` again finishes the move from
any checkout: it moves the tasks to the place that checkout's settings name,
following the markers to find them, and asks for `git remote add <name> <url>`
first when a marker names a remote the checkout lacks. Edits recorded but not
published in the meantime stay in each checkout and go to the live place at its
next publication.

A move is a coordinated team action: one person runs `workbook move-tasks` at
a time. Two runs at the same moment can leave two places live until
`workbook move-tasks` is run again.

```json
{"format":"workbook.result","version":1,"command":"move-tasks","data":{"from":{"remote":"origin","url":"git@github.com:team/code.git"},"to":{"remote":"tasks","url":"git@github.com:team/tasks.git"},"remote":"added","marked":"already","tasks":42,"identical":41,"carried":1,"stragglers":{"operations":1,"tasks":["WB-01M…"],"actors":["sam@example.com"]},"config":"unchanged","copy":{…},"recopy":{…},"verified":true}}
```

`remote` is `added` or `kept`, as setup reports it. `returned`, present only
for a move to a place the tasks left earlier, is `returned` when this run
recorded their return there and `already` when an earlier run had. `replaced`,
present only when there were some, names the tasks whose older copy there this
run replaced with their checkpoint, and `bundles` the backups it wrote first.
`stragglers`, present only
when there were some, counts the operations this run carried from a place that
was already marked, names their tasks and who made them. The count covers only
the run that reports it: operations an earlier run carried before it was
stopped are at the new place already, so the run that finishes the move does
not count them, and nothing records them. `relics`, `relicsIdentical` and
`relicsCarried`, present only when the old place holds relics, count its relic
refs as `tasks`, `identical` and `carried` count task refs, with a relic whose
missing operations are in a parked relic tip of this checkout counted as
carried; text output prints them on a `Relics:` line. `setAside`, present only when there
were some, lists each set-aside task with its `title`, its `operations` and,
when this run parked them, its `parkedRef`. `config` is `unchanged`, `carried`
when this run took the old place's configuration ledger, or `not-carried` when
it holds changes this checkout's ledger has diverged from, which a move does not
combine; make those changes again with `workbook config set`. `copy` and
`recopy` are the two synchronizations, in the shape `workbook sync --json`
reports, and `verified` says step 7 passed. A run that stops reports only its
error; the old place keeps every ref it had, and nothing is deleted anywhere.

What is guaranteed and what is not:

- No history is lost. The old place keeps every ref it held; the new place ends
  holding every operation they hold, each once. A teammate's edit recorded but
  not yet published stays in their checkout's own refs and is published to the
  new place by their next `workbook setup`.
- Before step 3, a checkout still on the old settings goes on publishing to the
  old place, and steps 4 and 5 carry that across. After step 3, such a checkout
  is refused: every command that would publish asks the old place's identity
  ref first and stops with the `tasks-moved` refusal.
- The mark cannot be made atomic with other people's edits: plain Git has no
  compare-and-swap across two refs. A checkout on the old settings whose
  publication was already under way when the mark landed, between its look at
  the identity ref and its push, still lands its edit on the old place. Every
  publication lists the identity ref again after the push, so that command
  exits 3 with the `published-after-move` refusal, names the tasks, and keeps
  the edit, which its next setup publishes to the new place. If that listing
  fails, the command cannot tell, and says so: it succeeds with a
  `publication-unconfirmed` warning, in text and in JSON, naming the remote and
  the tasks and giving Git's reason with the credentials of configured
  addresses removed. A board save published inline carries the same warning.
  A watcher round, which publishes what commands and board saves hand to it,
  has no command to carry one, so it writes the warning to its terminal (a
  board's server log) instead. An edit published
  in that instant by a clone that then disappears, such as an ephemeral agent,
  sits at the old place until somebody runs `workbook move-tasks` again. So:
  announce the move before making it, and run `workbook move-tasks` once more
  after everyone has pulled the setting. Nothing else looks for edits at the
  old place: `workbook sync` on the new settings never contacts it, and
  `workbook setup` only lists its refs to check the move was finished.
- As optional hardening, a host that can make refs outside branches and tags
  read-only can close the window on the old repository by refusing pushes to
  `refs/workbook/tasks/*` there after the move. GitHub does not document a way
  to: its rulesets "target branches or tags in a repository", and its push
  rulesets restrict file extensions, path lengths, paths and sizes, not ref
  names (see GitHub's
  [About rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets)).
  Do not count on a ruleset protecting a ref outside branches and tags without
  trying a push against it. A self-hosted Git server can do it with a
  `pre-receive` or `update` hook.
- If the move is never finished (stopped before step 3 and not run again), the
  old place is unmarked and checkouts on the old settings keep using it. A new
  checkout's setup refuses that state instead of joining one half of it; see
  `move-not-done` below.
- A board that was already running when the old place was marked keeps saving
  edits locally, and each save's publication fails with the moved refusal as a
  `tasks-moved` warning beside the saved change, in either publication mode.
  It learns of the move from this checkout's copy of the remote's identity ref,
  `refs/workbook/remotes/<remote>/project`, which every fetch updates, the
  board's and a watcher's own rounds included: before handing a save to a
  watcher the board reads that copy locally, with no network; when that copy
  carries a marker, it asks the remote once before refusing, because the tasks
  may have moved back there since. So a board, and a
  command handed to a watcher, learns of a move within one interval of the
  synchronization that fetches it, or at once when another command here fetched
  it first; a save handed over before then is refused by the watcher, which
  logs the refusal once. A hosted board shows the warning in its own words,
  without the server's paths, and the board's background synchronization logs
  the refusal once. Restart it from a folder set up for the new place. A new
  `workbook serve` refuses to start.
- A checkout on any earlier generation of the settings is refused and told the
  place the tasks are now. Its refusal follows the markers from its own tasks
  remote, through this checkout's remote of each name when it has one and at
  the address the marker records otherwise (always by address in a tasks-only
  folder), up to 32 places; that costs one listing per place, and only on the
  way to a refusal. A walk that cannot read a place names the last place a
  marker named. One that meets a place twice says the markers lead back to a
  place already passed, which a move back stopped between marking the place it
  leaves and recording the return leaves, and names `workbook move-tasks`,
  which finishes it. A checkout whose settings name a
  place the tasks came back to is not refused: before refusing, it asks that
  remote again, and finding it live, uses it and publishes the edits it kept.
- A checkout whose committed settings name a marked place, where an earlier
  committed version named the place the tasks are now, is either on stale
  settings or on a change of `tasks-remote` committed before its move ran.
  Workbook tells it to run `workbook move-tasks` only when that is certain: the
  place the tasks are is unmarked, and no marker recorded at the place the
  settings name, at the place the tasks are, or at any place those markers lead
  to names the place the settings name as where the tasks went. Otherwise it is
  told to pull and run setup, and the message adds that whoever changed the
  settings runs `workbook move-tasks` first. The checks read each of those
  places once, and only on the way to a refusal.

### Setup and the refusals

`workbook setup` in a project whose settings name a tasks remote adds the
remote when the checkout lacks it (`git remote add --no-tags`, with a fetch
refspec of `^refs/*` so a plain `git fetch` downloads nothing from it), lists
its Workbook refs, and then synchronizes with it. Its result gains a
`tasksRemote` member, absent for a project that never chose:

```json
"tasksRemote":{"name":"tasks","url":"git@github.com:team/tasks.git","settingsUrl":"git@github.com:team/tasks.git","status":"added"}
```

`status` is `added` when this run added the remote from the settings and `kept`
when the checkout already had one of that name; `url` is the address the
checkout uses, with any credential removed. Text output prints it as a
`Tasks remote:` line.

Every command that opens the project, except `setup`, `config` and
`move-tasks`, refuses a checkout that has to run setup first. Each refusal
is a plain sentence and carries a `tasksRemote` member in the JSON error
envelope, saying which remote, why, and the command that gets out of it:

```json
{"format":"workbook.error","version":1,"error":{"category":"not-initialized","message":"…","tasksRemote":{"reason":"tasks-moved","remote":"origin","movedTo":{"remote":"tasks","url":"git@github.com:team/tasks.git"},"command":"workbook setup"}}}
```

| `reason` | Exit | When |
| --- | --- | --- |
| `remote-missing` | 3 | The settings name a remote this checkout does not have: what an ordinary pull of the settings change leaves. `url` is the address setup will add. |
| `not-read` | 3 | The checkout has the remote and has never read the tasks from it: what a setup without access leaves. The message names the address and the access needed. |
| `unreachable` | 1 | Setup could not list the tasks remote. The message says which address, that seeing the tasks needs read access to it and changing them needs push access, and Git's own reason. |
| `tasks-moved` | 3 | This checkout's tasks remote is marked as moved, so its settings predate the move. `movedTo` names the new place. The message says to pull the commit that sets `tasks-remote` and run setup, and that unpublished edits are kept; `command` is `workbook setup`. When the settings committed at `HEAD` name the marked place, an earlier committed version named the place the tasks are now, that place is unmarked, and no marker recorded at either place or at any place their markers lead to names the marked place as where the tasks went, the change of `tasks-remote` was committed before its move ran: the message says so, and that pulling and running setup cannot finish it, and `command` is `workbook move-tasks`. Any other case keeps `command` `workbook setup`. When the markers lead in a circle, because a move was stopped between marking the old place and recording the return, the message says so and `command` is `workbook move-tasks` too. In a [tasks-only folder](#tasks-only-folders) it gives one command instead, `workbook setup --tasks-only <new address>`, which repoints that folder at the new place and keeps its unpublished edits; `command` holds it. Exit 5, category `validation`, when the marked remote was named with `--remote` and the command would publish to it. |
| `move-not-done` | 3 | Setup found the place the tasks came from still holding task refs with no marker: the move was never run or never finished. That place is `origin` for a first move and, for a move back, the place the markers lead to from the newest departure recorded at the place the settings name, which is `origin` when they name no tasks remote. Setup follows those markers whether or not a tasks remote is named, and costs nothing more when `origin` lists the identity commit this checkout already holds. A place along them that cannot be read is refused, exit 3 with this reason, naming it and saying to run setup again or, if that repository no longer exists, and you know the move finished, `workbook setup --no-sync` and then `workbook sync`, which skip that check; a marker naming a remote this checkout lacks says to add it with `git remote add`. `command` is `workbook move-tasks`. |
| `published-after-move` | 3 | The remote accepted this command's task refs and, listed again straight after, was marked as moved: the edit went to a place the tasks have left. `tasks` names them; the edit is kept in this checkout, and the message says to pull the setting and run setup, and that `workbook move-tasks` run again carries it. |
| `same-repository` | 5 | `move-tasks` or `setup` found the tasks remote to be the same repository as the place the tasks are in, whatever its address says. `command` is the `config set` to correct it. |
| `read-only-remote` | 5 | `push`, or a publication, named with `--remote` a remote other than the project's tasks remote. `command` is `workbook push`. |

The member's fields are `reason`, `remote`, and, where they apply, `url`,
`movedTo` (`remote` and `url`), `command` and `tasks`. An error that is not about the
tasks remote has no such member.

### Naming another remote for one run

`workbook fetch`, `push` and `sync` take `--remote <remote>`, which names a Git
remote this checkout has for that one run instead of the project's tasks remote.
No other command takes it, and a watcher (`sync --watch`) always uses the
project's tasks remote, so `sync --remote` refuses `--watch` and `--status`. A
run that names a remote skips the setup check above, because naming a remote is
how a checkout reads one it has not set up.

A remote other than the project's tasks remote is only read. `fetch --remote`
and `sync --remote` update its tracking refs under
`refs/workbook/remotes/<remote>/` and nothing else: no task ref of this checkout
moves, nothing is replayed or parked, the identity and configuration refs stay
where they are, and each task is reported `tracked` when the remote holds it at
another commit. The identity is compared, so a remote holding another project is
still refused, and a marked one is reported with `"status":"moved"` in `sync`'s
`identity` member. `sync --remote` reports its push phase `skipped`, and `push
--remote` is refused with the `read-only-remote` reason, exit 5.
`workbook move-tasks` is the one command that carries tasks from one remote to
another. Folding another remote in was what made alternating between the old
and the new place write the same operations again on every round trip.

Each remote's copy of the Workbook namespace is mirrored under its own
`refs/workbook/remotes/<remote>/` (`tasks/*`, `project`, `config`), and a fetch
prunes only the namespace of the remote it fetched from.

### The moved marker

The old place is marked on its `refs/workbook/project`, by one commit on top of
the identity commit. It keeps the identity's `project.json` byte for byte and
adds `moved.json`, a versioned document saying where the tasks went:

```json
{"format":"workbook.tasks-moved","version":1,"projectId":"01M…","remote":"tasks","url":"git@github.com:team/tasks.git"}
```

Publishing it is an ordinary fast-forward of that one ref, and its inputs are
fixed, so a repeated move pushes the same commit. Workbook reads it and never
adopts it: the local `refs/workbook/project` stays on the identity commit, and
a local identity ref moved onto a marker by hand is refused as corrupt-data,
with the `git update-ref` that puts it back.

A place the tasks come back to records the return with one more commit, on top
of its marker, whose tree is the identity commit's own tree and nothing else:
there is no new document, and its message, `workbook: tasks returned`, is for
people only. Publishing it is a fast-forward too. A place that is left again
gets a new marker on top of that, so its identity ref is a single line of
commits that starts at the identity commit and alternates, marker, return,
marker, and so on. Workbook reads each place by the shape of its tip: the
identity commit or a return means the tasks are there, a marker means they
went where it says. Any other shape is refused as corrupt-data: two markers or
two returns in a row, a return directly on the identity commit, a merge, a
commit whose tree differs from what its place in the line requires, or a
marker for another project. A line longer than 512 commits is refused too.
See [architecture.md](architecture.md#a-tasks-remote-other-than-origin) for the
object graph.

### Older Workbook versions

A Workbook release from before the setting (v0.6.1 and earlier) refuses a
`.workbook/config.json` that carries `tasksRemote`, as it refuses any member it
does not know: every command exits 7 with `cannot decode Workbook
configuration`. It never quietly uses `origin`. Upgrade every checkout before
committing the setting.

Such a release in a checkout still on the old settings, meeting a marked
`origin`, refuses the marker as an invalid identity: `fetch`, `sync` and `push`
exit 7 with `the Workbook project identity commit must have no parents`, and an
edit is recorded locally with a warning that its fetch failed and is never
published. It does not say where the tasks went, but it writes nothing to the
old place. Measured with Homebrew 0.6.0 and a build of the v0.6.1 tag.

After a move back to `origin`, such a release in a checkout whose settings never
named a tasks remote meets the same refusal on the return commit: `list` still
shows what it holds, `fetch` and `sync` exit 7 with the same message, and an
edit is recorded locally and never published. Measured with Homebrew 0.6.0.

A development build from after the setting and before moves back meets a
return commit, or a marker on top of one, and stops: `fetch`, `sync`, `create`
and `update` exit 7 with `a project identity commit with a parent must be a
tasks-moved marker holding exactly project.json and moved.json` and record
nothing, and after that first refusal `list` exits 7 too; a fresh clone's
`setup`, and `setup --tasks-only`, exit 7 the same way. In a checkout whose
settings name the place the tasks left, it gives the ordinary `tasks-moved`
refusal, exit 3, naming the place they went back to. It writes nothing to
either place. Neither kind of build can say that an upgrade is what is needed,
which is one more reason a team upgrades together before moving its tasks
back.

A release from before relics (v0.6.1 and earlier) works in a clone of a
remote that holds them and leaves them alone. Its fetch asks only for task,
identity and configuration refs, so its `setup` and `sync` bring no relic into
the clone, its `push` and `sync` publish no relic, and the relics at the
remote keep their tips. Its `list`, `show` and `update` work as before. A
clone of this build that also holds relics and is used with such a release
keeps its relic refs: that fetch does not prune them. The test suite plays the v0.6.1 release against a remote with
relics to hold it to this. Such a release has no `move-tasks`, so it cannot
move relics anywhere; its reset is described under
[Relics](#relics).

## Continuous synchronization

`workbook sync --watch` runs the same sequence on a loop in the foreground,
default every five seconds, until it is interrupted. It is an optimization and
never a requirement: every command works unchanged with none running.
`--interval <duration>` changes the period. It takes anything Go's
`time.ParseDuration` accepts, such as `30s` or `2m`, plus a leading whole number
of days, where a day is 24 hours (`1d`, `1d12h`), and it must be positive.
`workbook compact` cutoffs use the same format.

While a watcher is running, a mutation writes locally, asks the watcher to
publish, and returns, reporting a `sync` status of `deferred` rather than the
usual `completed`. That removes both network round trips from the command's
critical path — roughly 500 ms and 16 Git processes per mutation, measured
with the benchmark harness. Publication follows within
milliseconds rather than at the next scheduled tick, because the command hands
the change over rather than waiting for the timer.

`deferred` is best-effort, and deliberately not a guarantee. The local write is
durable before anything is attempted, but a watcher killed between accepting the
change and publishing it leaves the work local until `workbook push` or the next
watcher runs. A command falls back to synchronizing inline whenever no watcher
answers, its socket is dead, it has not synchronized recently, or its last
synchronization failed — that last case matters, because a watcher that cannot
reach `origin` knows it, and deferring would swallow the warning that says the
work is local-only.

The watcher writes to standard error as it goes: a line for each conflict it
meets, and `workbook: background sync failed: <message>` when a round fails,
where the message is Git's own with the credentials of configured addresses
removed, as [the hosted board's exposure list](#what-anyone-who-can-open-the-board-can-do-and-see)
details. A token in an address's path or query string, and anything a
credential helper, a hook or the remote prints, are not removed. A push
`origin` refuses is reported as `<n> task ref(s) were rejected by origin:`
followed by up to three task IDs, each with Git's porcelain reason such as
`[rejected] (fetch first)` or `[remote rejected] (hook declined)`, and `; and
<n> more` for the rest; what a refusing hook printed is not included. A failure that
repeats is written once, and again only when its message changes; the first
round that succeeds after it writes `workbook: background sync is working
again`. `workbook serve` runs the same loop and writes the same lines.

A watcher shares relics as a foreground `workbook sync` does, and a relic
command hands it its change the way a task command does. A relic conflict it
meets is written as `workbook: conflict on relic <relic-id>: ...`, with the
detail a foreground sync prints and never any body text. The next command
that hands the watcher a change reports that conflict in its own output and
`relicConflict`, reading the three texts of a body conflict from this
checkout, since the watcher keeps none. The watcher's status document is
version 2, the first that shares relics. A command finds a watcher that
serves version 1, an older Workbook's, not worth trusting and synchronizes
inline instead, so a relic change is never handed to a watcher that would not
push it.

`workbook sync --status` reports whether a watcher is running, what it last did,
any task conflicts it is holding, and the refs its last synchronization skipped under
`origin`'s task namespace. It does not list the relic conflicts the watcher
holds; those reach a person through the watcher's terminal and the next
command that hands it a change. That last set is what the watcher observed rather
than an accumulating memory: the tracking namespace is pruned against `origin`
on every fetch, so a name `origin` no longer holds leaves the report on the next
synchronization. `workbook serve` runs the same loop, so the board reflects
other clones' work without anyone running a command; an external watcher already
running keeps ownership and the board runs no second loop.

Reconciliation is what makes this safe. A mutation applied to a tip a few
seconds stale is no longer a case worth a network round trip to prevent; it is
one the fetch path already handles.

## Reconciling divergent histories

When `origin` holds operations this clone does not, and this clone holds
operations `origin` does not, the fetch replays the local ones onto the fetched
tip. The canonical task ref moves to the last replayed commit, the orphaned
local tip is parked at `refs/workbook/reconciled/<task-id>/<n>` in the same
compare-and-swap transaction, and every replayed pack keeps its actor, wall
time, and operation IDs, rewriting only its logical clock. Replayed commits have
exactly one parent; Workbook writes no merge commits, never rebases a shared ref,
and never force-pushes one unconditionally.

The command line reports a conflict once, on the run that meets it, and has no
command that lists or removes parked tips. `workbook serve` shows every edit a
parked tip holds that the task lacks, until somebody copies it back or
dismisses it; see [Edits the board's sync set aside](#edits-the-boards-sync-set-aside).

Most concurrent edits touch different fields, commute, and replay silently, so a
mutation that loses a push race and a fetch that finds week-old local work both
republish without asking anything. A replayed operation whose value `origin`
already holds records no commit at all. Every other concurrent field change is
last-syncer-wins.

Each conflict carries a `remote` member naming the remote it was met with
when that is not `origin`, and its wording names that remote too; for a
project that shares its tasks through `origin`, both are as they were.

Before writing a replayed pack, a replay checks it against the operations the
fetched history already records, by ID and by content. A pack whose operations
are all recorded with the same content is skipped. One that reuses a recorded
operation ID for different content is not that operation: the task is refused
as `corrupt-data`, exit 7, naming the task, the operation ID and both commits,
and nothing is changed, so neither value is taken silently. `workbook
move-tasks` makes the same comparison when it carries and when it verifies.

Exactly three situations stop a task's replay:

| Type | Situation | Reported detail |
| --- | --- | --- |
| `description` | both sides changed the description | `base`, `ours`, `theirs` |
| `dependency-cycle` | a replayed dependency closes a cycle against the fetched graph | the edge and the closing path |
| `tombstone` | `origin` tombstoned a task a local operation still edits | the blocked operation |

A conflict aborts replay for its own task only, leaving that ref at the fetched
tip or at the furthest operation replayed before the conflict arose. Whatever
did replay is ordinary history and is published like any other advance, so
`sync` and `push` always agree about the same refs; only the operations from the
conflict onward are dropped. Conflicts are reported in the result envelope's
`conflict` list — a list, because one fetch can stop on several tasks — and the
command exits `8`. Workbook never writes a conflict marker or an unresolved
value into a commit.

Resolution is a plain retry of the ordinary command against the now
fast-forwarded ref. There is no reconcile command, no continue command, no
discard flow, and no conflict state kept between invocations. The one exception
is a task a compaction left stuck, which only
[`workbook compact --adopt`](#when-a-compaction-cannot-be-combined) settles. The contract is
complete without a terminal, so JSON output, hooks, agents, and the web board
all consume the same list.

Relics are reconciled the same way, by replaying local commits onto the
fetched tip and parking the old local tip, under
`refs/workbook/reconciled-relics/<relic-id>/<n>`. Two situations stop a
relic's replay, `body` and `tombstone`, and they are reported in the
envelope's `relicConflict` list rather than in `conflict`; see
[When two edits meet](#when-two-edits-meet) and
[A relic conflict](#a-relic-conflict). A parked relic tip is never pushed and
never retired: only `workbook reset` removes one, after writing it to its
bundle.

Parked refs stay local; they are never pushed, because every enumeration that
builds a push refspec is scoped to `refs/workbook/tasks/`. Most parked refs hold
nothing the task lacks, since every reconciliation parks its old tip, clean or
not. The next successful mutation of a task retires those settled parked refs
beyond the three newest inside the same `update-ref` transaction, and so does the
background sync of `workbook serve` and `workbook sync --watch` after each
round. A parked ref holding an
edit the task lacks is never retired this way, however many accumulate; it
goes only when somebody dismisses it on the board, or when a manual
`workbook compact` or `workbook compact --adopt <id> --discard-local` drops it,
after writing it to the backup bundle unless told `--no-backup`. Fetches and reads
never prune them: a fetch must not delete recoverable work in the command that
orphaned it.

## Task archive compaction

Every change to a task is a commit, and every commit carries a full copy of the
task's state, so a task's history only grows. `workbook compact` replaces the
history of Done and Deleted tasks with one new parentless *checkpoint* commit
each, holding the task's current state, after saving the old history to a local
backup bundle. Title, description, status, labels, priority, rank,
dependencies, comments and assignments survive unchanged; only the step-by-step
history goes. Workbook never compacts on its own, and there is no confirmation
prompt: `--dry-run` is the way to look first.

```sh
workbook compact --dry-run
workbook compact
workbook compact --done-only 30d
workbook compact --deleted-only --keep-attachments
workbook compact --since 90d --backup ~/workbook-backups
```

No existing commit is changed. The run adds the checkpoint and moves the task's
ref to it; the old commits stay intact but are no longer reachable from any ref.
[Compaction](architecture.md#compaction) in the architecture document shows the
object graph before and after.

A compacted task stays fully editable. The next change is an ordinary commit on
top of the checkpoint, reopening a Done task works as it does for any task, and
a compacted task that picked up new changes can be compacted again: the new
checkpoint replaces the old checkpoint and those changes.

### Upgrade every clone first

Compaction ships in v0.7.0. Before anyone on a team compacts, every clone of the
project has to run v0.7.0 or later. Compaction never runs on its own, so a team
has time to upgrade first. Every compaction run, dry run included, says so.
The text output has this line near its end, and `--json` carries the same text
as `data.notice`:

```text
Every clone must run Workbook 0.7.0 or later; clones on v0.6.1 cannot sync compacted tasks.
```

`--adopt` does not repeat it.

A clone still on v0.6.1 that already holds a task someone compacted fails
safely, but not cleanly:

- Every `workbook sync` on that clone exits non-zero, reporting the task as
  invalid with `tracking and canonical tips use different history generations`,
  as if the data were corrupt.
- Its local ref stays on the old history, so it keeps accepting edits to that
  task, and none of them can be pushed: `workbook push` is rejected, and
  `origin` keeps the checkpoint.
- With the managed pre-push hook installed, every `git push` of code from that
  clone fails as well, until its owner upgrades.

No old history is pushed back over the checkpoint, so nothing on `origin` is
lost.

### Choosing tasks

With no selection flags, a run compacts every Done task and every Deleted task.

- A task is Done when its status carries the `done` tag under the project's
  current [statuses](#project-statuses), not only when the status is literally
  `done`. A Deleted task counts as Deleted whatever its status. Tasks in any
  other status are never compacted.
- `--done-only` compacts only Done tasks, and `--deleted-only` only Deleted
  ones. The two cannot be combined.
- `--since <duration>` keeps only tasks that became Done or Deleted longer ago
  than `<duration>`. `--done-only <duration>` and `--deleted-only <duration>`
  take the same cutoff directly: `--done-only 30d` means `--done-only --since
  30d`. Giving a cutoff both ways is an error, even when the two match.
- A duration is anything Go's `time.ParseDuration` accepts, such as `36h`, plus
  a leading whole number of days, where a day is 24 hours: `30d`, `1d12h`. It
  must be positive. `sync --interval` takes the same format.
- The value after `--done-only` or `--deleted-only` is optional. The next
  argument is taken as the cutoff when it is a duration or starts with a digit,
  so `--done-only --json` leaves `--json` alone, while `--done-only 30` is
  refused as `compact --done-only must be a duration such as 30d` and
  `--done-only 0d` as `compact --done-only must be positive`.
  `--done-only=30d` always names the value.

"Became Done" is when the task's current unbroken stretch in done-tagged
statuses began. Moving between two done-tagged statuses does not reset it, and
nor does a comment or any other edit. If no change to the task started the
stretch, because its status became done-tagged through the project's
configuration, the task's most recent status change is used, and failing that
its creation time. "Became Deleted" is the time of the most recent delete. Both
are wall times, compared only against the cutoff.

A task of the selected kinds, past the cutoff, can still be left alone. A run
lists each one under `skipped` with one of these reasons:

| Reason | Why |
| --- | --- |
| `already compacted` | Its tip is already a checkpoint with nothing after it, so there is nothing to save. |
| `has changes not yet on origin` | Sync is on, `origin` exists, and the local tip is not the tip `origin` holds. Compaction always starts from a tip `origin` already has. |
| `written by a newer workbook` | This build cannot fold its history. Reported whatever the cutoff. |
| `history cannot be read in full` | Its history could not be replayed to the root. Reported whatever the cutoff. |
| `changed during the run` | Its ref moved between selection and compaction, so it was not touched. |

With `--no-sync`, or with no `origin`, the local tip is compacted as it is.

### What a run does and reports

1. With sync on, it fetches first, as every mutation does.
2. It selects the tasks and computes the summary. `--dry-run` stops here: it
   compacts nothing, writes no bundle and pushes nothing. With sync on, the
   fetch in step 1 still runs, so a dry run can still move task refs or adopt
   a teammate's compaction, as any fetch does.
3. It writes and verifies the backup bundle, unless `--no-backup`. If writing or
   verifying fails, the run changes nothing and exits with the error.
4. For each task it builds the checkpoint and compare-and-swaps the task ref
   from the tip it read. The same transaction deletes the task's parked refs
   under `refs/workbook/reconciled/<id>/`, and the run then expires the task's
   reflog.
5. It moves the SQLite projection onto the checkpoints.
6. With sync on, it publishes the checkpoints, or hands them to a running
   watcher, as every mutation publishes what it changed. With `--no-sync` they
   stay local until a later `workbook sync`, `workbook push` or watcher
   publishes them.

Tasks are independent. A run that stops part way leaves some tasks compacted
and the bundle already complete, and running the same command again finishes
the rest. If every ref moved but the projection could not follow, the run still
succeeds with a `projection-update-failed` warning saying to run
`workbook rebuild` before `git gc`.

The text output is one line per task, then the totals, any skipped tasks, the
sync line, and the hints. Each task is named by the shortest prefix of its ID
that no other task in the project shares, at least eight characters of the
ULID, so `workbook show` accepts it. Skipped tasks are listed with their reason
under `Skipped:`, except those already compacted, which a later run finds
again every time: they are counted on one line, such as
`Skipped: 12 already compacted`. `--json` lists every one.

```text
WB-01KYD75B  Finished work  done  6 commits  1 attachment dropped (412.0 KiB)
WB-01KZ3M2Q  Abandoned work  deleted  2 commits  no attachments
Compacted 2 tasks (1 done, 1 deleted), replacing 8 commits.
Attachments: 1 dropped (412.0 KiB), 0 kept (0 bytes).
Sync:	completed
Estimated space saved: 431.2 KiB (applies once Git cleans up unreachable objects)
Backup: /path/to/repo/.git/workbook-archive/01KZ8Q4V3T9S2R1N0M7K6J5H4G.bundle
```

followed by the upgrade line and, for a real run, a hint that `git gc` reclaims
the space locally and that `origin` cleans up on its own schedule. A dry run
says `Would compact` and prints no backup or `git gc` line.

With `--json`, `data` is the summary, beside the usual `sync`, `conflict` and
`warnings` members:

```json
{
  "dryRun": false,
  "runId": "01KZ8Q4V3T9S2R1N0M7K6J5H4G",
  "tasks": [
    {
      "id": "WB-01KYD75B1R6CW2ZGCC0YEQ7Q28",
      "title": "Finished work",
      "kind": "done",
      "commits": 6,
      "attachments": {"dropped": 1, "kept": 0, "droppedBytes": 421888, "keptBytes": 0},
      "estimatedSavedBytes": 436120
    }
  ],
  "skipped": [{"taskId": "WB-01KZ…", "title": "Waiting on review", "reason": "has changes not yet on origin"}],
  "totals": {
    "done": 1, "deleted": 0, "commits": 6,
    "attachmentsDropped": 1, "attachmentsKept": 0,
    "droppedBytes": 421888, "keptBytes": 0, "estimatedSavedBytes": 436120
  },
  "bundle": "/path/to/repo/.git/workbook-archive/01KZ8Q4V3T9S2R1N0M7K6J5H4G.bundle",
  "notice": "Every clone must run Workbook 0.7.0 or later; clones on v0.6.1 cannot sync compacted tasks."
}
```

`kind` is `done` or `deleted`. `tasks` and `skipped` are always lists, empty
when there is nothing in them. `bundle` is omitted on a dry run, with
`--no-backup`, and when nothing was compacted. `notice` is the upgrade line,
present on every run. The estimate is the on-disk size of the objects only that task's
refs reach, less the checkpoint and any attachments kept. It is an estimate in
both directions, and it applies only once Git prunes; see
[Reclaiming space](#reclaiming-space).

### Attachments

File attachments on compacted tasks are dropped by default. Each one stays on
the task as an entry with its name, size, media type and blob ID, marked
`dropped` with the run ID and time:

- `workbook show` lists it as `notes.md (removed by compaction on 2026-11-10)`.
- `workbook show --get-attachment` refuses it as `not-found` (exit `4`) with
  `attachment "notes.md" was removed when the task was compacted on 2026-11-10`.
- The board, which the desktop app embeds, lists it with
  `Removed by compaction on 2026-11-10` beside its size and no download link.
- The board's attachment route answers `410 Gone` with the same message.

`--keep-attachments` copies every live file's bytes into the checkpoint
instead, so those attachments keep working. It is one choice for the whole run.
A later run with `--keep-attachments` does not bring back bytes an earlier run
dropped. Link attachments store no bytes, cost the repository nothing, and are
always kept unchanged. A file removed before compaction is reclaimed either
way, because nothing refers to it any more.

Dropping attachments is not final while a backup bundle exists: the bundle
holds the old history, and the old history holds the bytes. To remove something
on purpose, such as a file attached by mistake, compact with `--no-backup`, and
delete any earlier bundle that holds it. `origin` may also keep the old objects
for a while; see [Reclaiming space](#reclaiming-space).

### The backup bundle

Every real run that compacts anything writes a Git bundle of the history it is
about to make unreachable, unless `--no-backup` is given. By default it is written to
`<git-common-dir>/workbook-archive/<run-id>.bundle`: inside `.git`, so it is
never committed, outside the disposable caches under `.git/workbook`, and shared
by every worktree. `--backup <path>` writes it elsewhere: an existing directory
gets the default file name, and any other path is used as the file name.
`--no-backup` and `--backup` cannot be combined. Workbook never overwrites a
file; a bundle path that already exists is refused.

The bundle holds each compacted task's old tip under `refs/workbook/tasks/<id>`,
the task's parked refs under their own names, and a manifest commit at
`refs/workbook/archive/manifest` whose `manifest.json` names the format
(`workbook.archive-manifest`, version `1`), the project, the run, its time, the
flags given, and for each task its ID, title, old tip, old and new generation,
and commit count. It needs no other objects, and it is checked with
`git bundle verify` before any ref moves. `git bundle list-heads <file>` shows
what one holds.

Workbook does not read bundles back. Restoring a compaction after it is
published is not supported; the bundle is a manual recovery copy. Workbook
never deletes or rotates bundles either. Keeping them, moving them off the
machine, or deleting them is up to you. A bundle written on a machine that is
thrown away, such as a disposable sandbox, is lost with it. Once Git prunes the
replaced commits, the bundle is this clone's only copy of the old history.

### Publishing a compaction

Compaction publishes to, adopts from and leases against the project's tasks
remote, which is `origin` unless the project
[chose another](#a-tasks-remote-other-than-origin). Where this section and the messages it
quotes say `origin`, a project with a tasks remote reads that remote's name:
`"<title>" was compacted on tasks; …` for a remote named `tasks`. The status
values, such as `compacted-on-origin`, are machine values and keep their
names.

A checkpoint does not build on the tip it replaced, so Git sees publishing it
as a forced update. Workbook publishes it with a lease instead of a force-push:
the push uses a plain refspec with
`--force-with-lease=refs/workbook/tasks/<id>:<expected>`, where `<expected>` is
the tip `origin` held when this clone last fetched, and only once this clone has
shown that the checkpoint accounts for every operation in it. `origin` accepts
the push only while it still points at that tip. The task's sync status is then
`compaction-published`, and the clone moves its own tracking ref to the
checkpoint.

`workbook sync`, `workbook push` and a running watcher publish a local
checkpoint the same way, so one compacted with `--no-sync` goes out with the
next synchronization.

When `origin` refuses the push, the task stays compacted locally and
unpublished:

- **`origin` moved on.** The lease is stale, and the task's detail reads
  `origin changed since the compaction; the next sync reconciles it`. The next
  fetch replays what `origin` added onto the local checkpoint, and the push
  after it leases against `origin`'s new tip.
- **`origin` refuses the checkpoint itself.** A host can refuse
  non-fast-forward updates to Workbook refs with a server-side hook or ruleset,
  and a hook can decline the push for reasons of its own. Every refusal other
  than a stale lease gives the same detail, with Git's reason in it:

  ```text
  origin refused the compacted checkpoint (<git reason>); the compacted task stays local. Run "workbook compact --adopt <id> --discard-local" to undo the compaction on this clone.
  ```

  The reason is what Git reported, such as `non-fast-forward` or
  `[remote rejected] (hook declined)`. Every later push is refused the same way
  until the host allows it or the compaction is undone; see
  [Undoing a compaction origin will not take](#undoing-a-compaction-origin-will-not-take).
  Git's own `receive.denyNonFastForwards` covers only branches
  (`refs/heads/*`), so it does not block this push, and there is no need to
  change it.

`compact` itself still exits `0` when the push is refused, with an
`auto-sync-incomplete` warning that starts
`the tasks were compacted locally, but` and names each refused task with its
detail. `workbook push` and `workbook sync` report the refusal as a `rejected`
task and exit non-zero.

If the fetch before the run failed, the run compacts from this clone's last view
of `origin`, skips publishing, and warns that the compacted tasks stay local
(unpublished) until the next successful sync.

### Other clones adopt a compaction

Any fetch, whether from `workbook fetch`, `workbook sync`, a mutation's own
synchronization or a watcher, adopts a teammate's compaction automatically:

| Status | What happened |
| --- | --- |
| `adopted` | Origin compacted the task and this clone had nothing it lacked. The local ref moved to origin's checkpoint. |
| `adopted-replayed` | Origin compacted the task and this clone had unpushed changes. They were replayed onto the checkpoint, keeping their actor, wall time and operation IDs, and are published like any other local change. The detail reads `replayed <n> local operation(s) onto origin's checkpoint`. |
| `reconciled` | This clone compacted the task and origin then gained changes on the old history. Those were replayed onto the local checkpoint, whose push then leases against origin's tip. The detail reads `replayed <n> operation(s) from origin onto the local checkpoint`. |

Which local changes are unpushed is decided by operation ID: those neither in
the checkpoint's list of compacted operations nor on `origin` since the
checkpoint. When two clones compact the same task, `origin`'s checkpoint wins,
and the second clone adopts it the same way, replaying any of its operations
`origin`'s checkpoint lacks.

When origin's changes are replayed onto a local checkpoint and one clashes with
a local change, `origin`'s version wins, as in an ordinary reconcile: the
conflict is reported, the task is `conflicted`, and the local tip from before
the sync is parked under `refs/workbook/reconciled/<id>/<n>` so its text can be
recovered.

**Adopting drops some local data without a backup.** An adopted task's reflog
is deleted, and so are those of its parked refs under
`refs/workbook/reconciled/<id>/` whose edits the adopted checkpoint already
carries; neither is written to any bundle, since keeping them would keep the
old history alive. A parked ref that still holds an edit the task lacks — text
an earlier clash pushed aside that nobody copied back or dismissed — is kept,
and the board goes on showing it: its operations are not among the
checkpoint's compacted ones, so they are still the task's to recover. Its old
history stays reachable through it until it is dismissed. Unpushed changes are
replayed, not dropped.

### When a compaction cannot be combined

Two situations leave a task that no synchronization settles by itself. Either
way the task's ref holds history `origin` will not accept, so the task stays
unpublished. Commands find these tasks from local refs, so what follows holds
with `--no-sync` and beside a watcher too.

**Compacted on origin, with changes that will not replay.** A teammate
compacted the task, and this clone's unpushed changes to it cannot be replayed
onto their checkpoint. The local ref stays on the old history and the task's
status is `compacted-on-origin`, with this detail:

```text
"<title>" was compacted on origin; you have <n> unpushed changes on the old history. Run "workbook compact --adopt <id>" to retry, or add --discard-local to take origin's version after saving yours to a bundle.
```

With a single change it reads `you have 1 unpushed change on the old history`.

A task this clone deleted without pushing, when `origin` had already deleted
and compacted it, is reported this way too. `compact --adopt <id>
--discard-local` loses nothing there, because both sides deleted it.

It is that task's state, not a failed synchronization. `workbook sync` and
`workbook fetch` still exit `0` when it is the only issue, and a watcher keeps
working. A mutation of another task succeeds with an `auto-sync-incomplete`
warning that names the task: `the change synchronized, but origin compacted
tasks this clone keeps on the old history (<ids>); run "workbook sync" for
details`.

**Changed on origin after you compacted it.** This clone compacted and deleted a
task, and `origin` then changed it on the old history, its own deletion
included. Nothing applies to a deleted task, so the change cannot be replayed
onto the checkpoint. It is reported as a conflict, the task is `conflicted`, and
the checkpoint stays unpublished:

```text
"<title>" changed on origin after you compacted it, and the two can't be combined. Run "workbook compact --adopt <id> --discard-local" to take origin's version after saving yours to a bundle; then compact it again.
```

In either state, a command that would change the task, including one from the
board, is refused with that detail as a `conflict` (exit `8`; the board answers
`409`), and `workbook next` and `next --claim` skip the task.

`workbook compact --adopt <task>` is the way out:

```sh
workbook compact --adopt WB-01K…
workbook compact --adopt WB-01K… --discard-local
workbook compact --adopt WB-01K… --discard-local --no-backup
```

- It always fetches first, whatever the `auto-sync` setting and whether a
  watcher is running, because it settles against `origin`'s current tip. It
  needs an `origin`, refusing a clone without one as `validation` (exit `5`)
  with `compact --adopt needs an origin remote to fetch from`, and it fails if
  that fetch fails. It cannot be combined
  with `--done-only`, `--deleted-only`, `--since`, `--keep-attachments`,
  `--dry-run` or `--no-sync`.
- Without `--discard-local` it retries the replay. If the fetch or the retry
  settles the task, the result is `adopted` or `adopted-replayed` (or, for a
  task this clone compacted, `reconciled` or `conflicted`), and the task is
  published. If the replay still fails, it exits `8` with the same detail.
- `--discard-local` takes `origin`'s version. It first saves the local history,
  the old tip and the task's parked refs, to a bundle at
  `<git-common-dir>/workbook-archive/<run-id>-<task-id>-local.bundle`, or where
  `--backup` says. Then it moves the task ref to `origin`'s tip and drops the
  task's parked refs and reflog. The status is `discarded-local`, and there is
  nothing to publish. `--no-backup` skips the bundle. `--no-backup` and
  `--backup` need `--discard-local` when used with `--adopt`.
- A task in neither state, and with no unpublished compaction (see below), is
  refused as `validation` (exit `5`), `task <id> has no compaction to adopt`.
  The command was typed correctly, so these refusals print no usage text. A checkpoint this build cannot read is
  refused as `newer-writer` (exit `9`) before anything is written; only an
  upgrade settles it.

For a task this clone compacted, `--discard-local` returns it to `origin`'s
uncompacted history. Compact it again afterwards if you still want it compacted.

#### Undoing a compaction origin will not take

`compact --adopt <id> --discard-local` also undoes a compaction this clone made
that is not published yet, whether `origin` refused the checkpoint or the run
used `--no-sync`. It saves the compacted history to
`<run-id>-<task-id>-local.bundle` as above, unless `--no-backup`; moves the task
ref back to the tip `origin` holds with a compare-and-swap; drops the task's
parked refs; and moves the SQLite projection onto that tip. The status is
`discarded-local`, and nothing is pushed. Without `--discard-local`, such a task
is refused as `validation` (exit `5`), with no usage text:

```text
task <id> has a local compaction that is not published; add --discard-local to undo it after saving it to a bundle
```

The text output is a result line, such as
`Took origin's version of <id>; the local history is saved in <path>.` or
`Adopted origin's compaction of <id>.`, then the `Sync:` line and any
conflicts and warnings. With `--json`, `data` is:

```json
{"taskId": "WB-01K…", "status": "discarded-local", "detail": "took origin's version; the local history is saved in /path/to/repo/.git/workbook-archive/01KZ…-WB-01K…-local.bundle", "head": "<object ID>", "bundle": "/path/to/repo/.git/workbook-archive/01KZ…-WB-01K…-local.bundle"}
```

`head` is the tip the task's ref holds afterwards. `detail` is omitted when
there is nothing to add, and `bundle` is present only when `--discard-local`
wrote one.

A fetch that meets a checkpoint this build cannot fold, while this clone has
unpushed changes to the task, reports the task as `needs-upgrade` and exits `9`
instead, as for any newer-writer history.

### Reclaiming space

Compaction makes the old history unreachable. It does not delete it.

- **Locally**, the space comes back when `git gc` prunes unreachable objects.
  By default Git prunes only objects older than two weeks (`gc.pruneExpire`);
  `git gc --prune=now` prunes them at once. The run removes the task's reflog
  entries and parked refs, and the remote-tracking ref moves once the push
  lands, so nothing Workbook keeps holds the old history. A backup bundle is a
  separate file and holds nothing in the repository; once Git prunes the
  replaced commits, the bundle is this clone's only copy of them.
- **On `origin`**, the host decides. Hosts such as GitHub keep unreachable
  objects for an unspecified time, and Workbook cannot make them prune.
- **In other clones**, the space comes back when they adopt the compaction and
  then run `git gc`. The fetch that adopts it moves the SQLite projection onto
  the checkpoint while the old history is still there. If that update fails,
  the command warns `projection-update-failed`, saying to run
  `workbook rebuild` before `git gc`. If Git prunes the old history before the
  projection moves, the next read is refused as `corrupt-data` (exit `7`), and
  the message says to run `workbook rebuild`, which reads the task refs
  afresh.

## Resetting Workbook data

`workbook reset` removes Workbook's data from one checkout, and `workbook reset
--remote <remote>` removes it from one Git remote. Both are run by hand. They are
for starting a checkout over, for removing the copies a [move](#a-tasks-remote-other-than-origin)
left behind, and for [leaving Workbook](#leaving-workbook). Neither touches a ref
outside `refs/workbook/`, a branch, a tag or a tracked file. Both write a backup
bundle first, unless `--no-backup` is given where it is allowed, and print the
Git command that brings its refs back.

These commands change what everyone sees. Workbook does not detect a teammate
running Workbook against the same remote at the same moment, so agree when to
run them, as for any change the whole project shares.

### Resetting a checkout

```sh
workbook reset [--dry-run] [--no-backup [--discard-unpublished] | --backup <path>] [--json]
```

A reset of a checkout first makes every check that can refuse it: the bundle's
path (see [The reset's backup bundle](#the-resets-backup-bundle)) and, with
`--no-backup`, the work that exists only here (see below). A refused reset
changes nothing, and leaves the watcher running. Then it does three things, in
this order:

1. It stops this checkout's watcher (`workbook sync --watch`), if one is
   running. It asks the process the watcher reports itself as to stop. That is
   an interrupt where the platform has one, so the watcher's last
   synchronization runs and publishes what it held, and a kill otherwise. It
   then waits up to 20 seconds for the watcher to go. A watcher that cannot be
   found, cannot be stopped, or does not stop in time is refused, exit 1, reason
   `watcher-running`, saying to stop it with Ctrl-C in its terminal and run
   the reset again. Nothing is removed then. The watcher's last
   synchronization can change what exists only here, so a reset with
   `--no-backup` checks that work again once the watcher has stopped, and a
   refusal then says the watcher was stopped.
2. It writes the backup bundle (see below) and then removes every ref under
   `refs/workbook/` in one transaction. That covers the task refs, the project
   identity, the settings ledger, the parked refs of edits a reconciliation
   set aside, and the tracking refs of every remote.
3. It removes the caches: everything under `<git-common-dir>/workbook/`, such
   as the SQLite projection, the identity guard and the watcher's address, and
   the temporary files Workbook's own writes leave under `.workbook/`. The only
   such file is `.workbook/.config-*.tmp`, which an interrupted write of the
   settings leaves. Every other file under `.workbook/` stays: tracked files,
   `.workbook/config.json` and `.workbook/guidelines.md` whether tracked or
   not, and untracked or ignored files Workbook did not write, which a `Left:`
   line counts.

A dry run lists exactly the refs and the files a real run would then remove,
on its `Would remove:` and `Caches:` lines and in its JSON.

It does not remove the backup bundles under `<git-common-dir>/workbook-archive/`,
the managed pre-push hook, the agent documentation or the skill. Run `workbook
setup` and then `workbook sync` afterwards to fetch the project again, as a fresh
clone does. In a [tasks-only folder](#tasks-only-folders), the next step is
`workbook setup --tasks-only <url>`. A checkout whose `.workbook/config.json` is
gone is told no next step.

A damaged ref is removed anyway and listed on a `Damaged:` line with the
reason. That is a ref whose object is missing or cannot be read, one that
names something other than a commit, a Workbook commit with no `operation.json`,
or a symbolic ref. This is how a checkout whose Workbook data is damaged is
rebuilt to match a fresh clone. A damaged ref is not in the bundle, with one
exception: a damaged relic ref that still resolves to a commit. Git can bundle
a commit whatever its tree holds, and a sync never publishes a relic it
cannot read, so nothing else holds that one, and that ref
is written to the bundle with its history, counts as
[work that exists only here](#work-that-exists-only-here), and comes back with
the `Restore:` command. The `Damaged:` line ends one of three ways:

```text
Damaged:	refs/workbook/relics/WB-R-01M9A000000000000000000001	(it names a commit that is not a Workbook operation commit; it is a commit, so the bundle keeps it like any other ref)
Damaged:	refs/workbook/relics/WB-R-01M9A000000000000000000002	(it names a blob, not a commit; nothing of it can be kept)
```

The first is the relic ref the bundle keeps, and the second is any ref it
cannot keep. With `--no-backup` there is no bundle, so the first kind is
listed with its reason alone and neither ending.

#### Work that exists only here

Before it removes anything, a reset finds the work this checkout holds that the
tasks remote does not have:

- every commit of a task or the settings ledger that the remote's matching ref
  does not reach, listed on an `Unpublished:` line per ref with the task's title
  and how many operations it holds, or `never published` when the remote has no
  such ref;
- every parked ref, listed on a `Set aside:` line.

A relic counts the same way: a relic whose every commit the remote's relic ref
reaches is published, and one with commits it lacks is listed as
`relic <id> "<title>"` with how many operations it holds, or `never published`,
with `relicId` in the JSON in place of `taskId`. A change made with
`--no-sync` and not yet synchronized:

```text
Unpublished:	relic WB-R-01M87F0YHZ1852KPS55KCDPXG2 "Release checklist", 1 operation
```

A damaged relic ref that resolves to a commit is listed as well, as
`relic <id>` with no title when its newest commit cannot be read as a relic.
A parked relic tip, which holds a change a
[relic conflict](#a-relic-conflict) did not apply, is listed on a `Set aside:`
line, as a parked task tip is:

```text
Set aside:	refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1
```

It measures this against the remote as it is now. It lists the remote's Workbook
refs and fetches the objects of those this checkout lacks without moving any ref,
so a dry run changes nothing. A remote that was wiped holds nothing, so after
one every task here is listed.

When that check cannot be made, an `Unchecked:` line says why, and what is
listed comes from this checkout's tracking refs for the remote, which are what
it held at the last fetch. Either the remote could not be read, or it was read
and its history could not be fetched into this checkout, which a damaged ref
here causes; the line then says the remote was read and names the damaged
refs. Tracking refs can be wrong both ways. They name work published since the
last fetch, and when the remote lost refs since, as a reset of it makes it,
they reach commits it no longer holds and so hide work that exists only here.

The bundle keeps that work, so a plain reset goes ahead with it. `--no-backup`
writes no bundle, so a reset with `--no-backup` of a checkout that holds such
work is refused, exit 5, reason `unpublished`. The refusal names each task and
relic, marks a damaged ref `damaged`, and ends with what can be done. When
everything listed is work sync can publish, that is `Run workbook sync to
publish it, or reset without --no-backup to keep it in a bundle, or add
--discard-unpublished to lose it.` A parked tip, task or relic, and a damaged
ref are never published, so only the bundle keeps them: when the work is only
of that kind the refusal says `Nothing publishes set-aside edits or a damaged
ref, so workbook sync cannot help`, and when it is mixed it says sync publishes
only the rest. `--discard-unpublished` is the only
way to lose such work on purpose, and it needs `--no-backup`. While the check cannot be made,
`--no-backup` is refused, exit 5, reason `unchecked`, with or without
`--discard-unpublished`: a bundle is cheap, and the tracking refs cannot say
what would be lost. A local-only project is such a checkout: it has no Git
remote by the tasks remote's name, `origin` by default, so there is nothing to
check against and its reset cannot skip the bundle.

#### Restoring a checkout

The `Restore:` line is a `git fetch` from the bundle of the refs that hold work:
the task refs, plus the parked refs, the relic refs, the parked relic
tips (`'refs/workbook/reconciled-relics/*:refs/workbook/reconciled-relics/*'`)
and the settings ledger when there were any.

```sh
git fetch .git/workbook-archive/01M….bundle 'refs/workbook/tasks/*:refs/workbook/tasks/*' refs/workbook/config:refs/workbook/config
workbook setup
workbook sync
```

Run it before `workbook setup`. A fetch that would move an existing ref
anywhere but forward is refused by Git, so it never overwrites a ref that setup
or a sync made since. The identity and the tracking refs are in the bundle too,
and setup and sync make them afresh, so the command leaves them out.

### Resetting a remote

```sh
workbook reset --remote <remote> [--confirm <remote>] [--dry-run] [--no-backup | --backup <path>] [--json]
```

`--remote` names a Git remote of this checkout. The reset fetches that remote's
Workbook refs into this checkout's tracking refs for it, and drops the tracking
refs it no longer has. A dry run does that fetch too, and changes nothing else.
The reset then writes the backup bundle and pushes a deletion of each ref. Each
deletion is leased on the commit just fetched, so a ref someone changed since is
not deleted, and the push runs without the pre-push hook. Afterwards it fetches
again, so this checkout stops tracking what is gone.

What it removes depends on what the remote is:

- **A place the tasks moved away from**, whose `refs/workbook/project` holds
  the [moved marker](#the-moved-marker): every Workbook ref but that one. The
  marker stays, listed on a `Kept:` line, so a checkout still on the old
  settings keeps being refused and sent to where the tasks went. Its tasks
  live elsewhere, so no confirmation is needed and `--no-backup` is allowed.
- **The place the tasks live**: every Workbook ref there, which removes the
  project's tasks for everyone. It is refused, exit 5, reason `confirm`, unless
  `--confirm` repeats the remote's name. The refusal says how many tasks would
  go and prints the command to run. `--no-backup` is refused there, exit 5,
  reason `backup-required`. A bundle is always written.

A remote that holds no Workbook ref has nothing to remove, and the reset
succeeds.

A checkout that still holds the tasks publishes them back to a wiped place on
its next synchronization, or when its watcher next runs. Workbook does not
prevent that. Run `workbook reset` in each such checkout, or `workbook reset
--remote` again after that checkout has published. A reset run from a checkout
that still holds task refs says so on its `Next:` line. A place the tasks moved
away from keeps its marker, and that marker refuses such a publication there.

A host that refuses to delete refs, as a branch-protection or push rule can, is
refused, exit 1, reason `deletion-refused`. The refusal names the host's reason
and the lines it sent, and lists every Workbook ref the remote still holds.
Deletions the host allowed are not undone. Once the host allows deleting refs
under `refs/workbook/`, running the same command again finishes the job. That
run's bundle holds only the refs still left, so the refusal names the first
run's bundle, which holds every ref, and says to keep it.

A reset of a remote does not stop this checkout's watcher.

#### Restoring a remote

The bundle holds the remote's refs under the names this checkout tracked them
by, `refs/workbook/remotes/<remote>/…`, so fetching from it never overwrites a
live ref. Its `Restore:` line rebuilds them in a new, empty repository, from
which they can be pushed back:

```sh
git init --bare restored.git && cd restored.git
git fetch /path/to/.git/workbook-archive/01M….bundle 'refs/workbook/remotes/origin/*:refs/workbook/*'
git push <remote address> 'refs/workbook/*:refs/workbook/*'
```

The new repository must use the same object format as the bundle.

### The reset's backup bundle

A reset writes its bundle as [`compact`](#the-backup-bundle) does. It goes to
`<git-common-dir>/workbook-archive/<run-id>.bundle` by default, or to
`--backup <path>`: a directory gets the default file name, and any other path is
the file name. A relative path is taken from the directory the command runs in.
A path that already exists is refused, exit 5. A path inside a folder a reset of
this checkout removes files from, `.workbook/` or `<git-common-dir>/workbook/`,
is refused, exit 2, once symbolic links are resolved, for a file or a
directory, and for `reset --remote` and `move-tasks --clean-up` too. The
default place is outside both, in a linked worktree and a tasks-only folder as
well. Both refusals come before anything is stopped, written or removed, and a
dry run makes them too. `--no-backup` and `--backup` cannot be combined. The bundle holds each ref under its own name, and
the same manifest, at `refs/workbook/archive/manifest`, with `reset` first among
its flags and one entry per task. It is verified before any ref is removed, and
a ref that moved since the reset read it stops the run with nothing removed. A
reset that has nothing to keep writes no bundle. A damaged ref is left out of
it, except a damaged relic ref that resolves to a commit, which is written to
it like any other ref. A `refs/workbook/archive/manifest` that an interrupted
backup left is left out too: it is not data and is removed with the rest. Before reporting the
bundle, the reset checks that it is still there, and fails naming the path if
it is not. Workbook does not read bundles back, delete them or rotate them.

A reset stopped part way, before or during its bundle, after the refs went at a
checkout, or partway through the deletions at a remote, is finished by running
it again once. Each run writes a bundle of what is left, if anything is. A run
killed while writing its bundle can leave a `<bundle>.tmp` file beside where
the bundle was going. That file is not a backup, the next run does not read it,
and it can be deleted.

### The reset's JSON result

The result's `data` is the same for both forms:

```json
{"dryRun":false,"scope":"remote","remote":"tasks","url":"git@github.com:team/tasks.git",
 "refs":[{"ref":"refs/workbook/tasks/WB-01M…","objectId":"…"}],"kept":[],"tasks":2,
 "unpublished":[],"setAside":[],"caches":[],"left":0,
 "bundle":"/…/.git/workbook-archive/01M….bundle",
 "restore":"git fetch … 'refs/workbook/remotes/tasks/*:refs/workbook/*'","next":["workbook reset"]}
```

`scope` is `checkout` or `remote`. `refs` lists what was removed, or with
`dryRun` what would be. A damaged ref carries `damaged` with the reason, and
`"bundleable": true` as well when it is a relic ref the bundle keeps.
`movedTo` names where the tasks went, for a place they left. `unpublished` has,
for each ref, `ref`, `taskId` and `title` for a task, `new` when the remote has
no such ref, `commits`, and `operations`, the IDs of the operations those
commits hold. `unchecked` is why the tasks remote could not be checked, and
`watcher` is `{"pid":…,"stopped":true}` when a watcher was found. `caches` lists
each file and folder removed besides refs, or with `dryRun` each one that would
be, and `left` counts the untracked files under `.workbook/` that Workbook did
not write and left alone.

A refusal adds a `reset` member to the error: `reason` (`unpublished`,
`unchecked`, `confirm`, `backup-required`, `deletion-refused` or
`watcher-running`), and,
as they apply, `remote`, `command` (the command to run), `tasks`,
`unpublished`, `setAside`, `remaining` (the refs a host would not delete) and
`bundle` (the bundle a refused run had already written).

### Cleaning up after a move

`workbook move-tasks --clean-up` resets the place the tasks left once the move
has finished, as `workbook reset --remote <that place>` does: every Workbook ref
there but the moved marker goes, after its own bundle is written. With
`--backup <file>`, the clean-up's bundle goes to that file. When the move itself
wrote a bundle there, as a move back that replaces older copies does, the
clean-up's goes to the same name with `-clean-up` before the extension instead.
`--no-backup` applies to both. A move that stops with an error cleans up
nothing. A move that finished but set edits aside, exit 8, cleans up nothing
either: its output says so on a `Cleaned up:` line, and its JSON names the
command to run once the edits are dealt with as `cleanUpNext`, `workbook reset
--remote <place>`. A clean-up that fails after a finished move says the move is
complete and to run `workbook reset --remote <place>` to finish. The JSON
result carries the clean-up as a `cleanUp` member shaped as above. The tasks can still move back to a place cleaned up this way:
a move back works whether the old copies are there or gone.

### Leaving Workbook

To stop using Workbook in a project, everyone first stops running it: no task
commands, no `workbook sync --watch`, no board. Then, with `tasks` standing for
the project's tasks remote (`origin` if its settings name none):

```sh
# 1. In one checkout: remove the managed documentation and the settings, stage
#    what docs remove changed, and push without the pre-push hook, which would
#    publish the tasks again.
workbook docs remove
git rm -r -q .workbook
git rm -r -q --ignore-unmatch .claude/skills/workbook   # the skill, where it is committed
git add AGENTS.md                            # and any other file docs remove wrote, such as CLAUDE.md
git commit -m "Stop tracking tasks with Workbook"
git push --no-verify

# 2. From that checkout: wipe the place the tasks live, then each place they
#    moved away from, and remove that place's marker.
workbook reset --remote tasks --confirm tasks
workbook reset --remote origin              # only if the tasks moved away from it
git push --no-verify origin :refs/workbook/project

# 3. In every checkout and tasks-only folder, after pulling step 1:
workbook reset
hook=$(git rev-parse --git-path hooks/pre-push)
if [ "$(sed -n 2p "$hook" 2>/dev/null)" = '# workbook-managed-pre-push v1' ]; then rm "$hook"; fi
rm -rf .claude/skills/workbook               # the skill, where it is not committed

# 4. Check that nothing is left; each line should print nothing.
git ls-remote origin 'refs/workbook/*'
git ls-remote tasks 'refs/workbook/*'        # only if there is a tasks remote
git for-each-ref refs/workbook/
ls "$(git rev-parse --git-common-dir)/workbook" 2>/dev/null
git ls-files .workbook '*workbook/SKILL.md'
git grep -l 'workbook:begin' -- AGENTS.md CLAUDE.md

# 5. Then, in every checkout:
git remote remove tasks                      # only if setup added it for the tasks
```

What each step removes:

1. `workbook docs remove` removes the Workbook section from each documentation
   file it manages, such as `AGENTS.md`, and removes `.workbook/guidelines.md`
   and this checkout's skill. It
   leaves a file you edited inside its managed block unless given `--force`.
   `git rm` removes `.workbook/config.json` and a committed skill, and `git add`
   stages the edited `AGENTS.md`, so that every other checkout loses them when
   it pulls. Run `docs remove` before any reset, while this checkout can still
   read the project.
2. Step 2 removes every Workbook ref on each remote, and `git push :ref` removes
   the marker. This cannot be undone except from the bundles the resets print.
3. Each `workbook reset` stops that checkout's watcher, writes a bundle of
   everything it held, and removes its refs and caches. It lists the tasks as
   unpublished, because the remotes no longer hold them, and keeps them in the
   bundle. The hook is removed only when it is Workbook's own. Use your
   `skillDir` in place of `.claude/skills` if you set one.
4. A remote that holds Workbook refs again was published to by a checkout that
   still held tasks. Reset that checkout, then run step 2 again. The last two
   checks find a skill or a Workbook section a checkout still tracks. Step 1
   was not pulled there, or it left a file out.
5. The remote is removed last, because step 4 reads it.

The bundles stay in each checkout's `<git-common-dir>/workbook-archive/` until
you delete them. Deleting them is the last step that cannot be undone:

```sh
rm -r "$(git rev-parse --git-common-dir)/workbook-archive"
```

The user-global configuration, `$XDG_CONFIG_HOME/workbook/` or
`~/.config/workbook/`, belongs to each person, not to the project, and
the procedure leaves it alone.

## Semantic history validation

Normal `list`, `show`, `fetch`, `push`, and `sync` use bounded current-tip
checks. To audit semantic history, run the foreground command:

```sh
workbook validate [--full] [--json]
```

`validate` reads canonical task histories and verifies every stored checkpoint
against the operation sequence that produced it. A compacted task's history
starts at its `history.checkpoint` commit, which `validate` treats as the root:
its `state.json` must be what that operation folds to with no prior state, and
the discarded history is never needed. It stores resumable,
non-authoritative progress in the disposable shared cache at
`<git-common-dir>/workbook/validation.sqlite`; linked worktrees therefore share
the same audit cache. Validator version `1` records each observed head as
`pending`, `valid`, or `invalid`. Deleting this cache is safe: a later validation
recreates it from canonical Git data.

For an unchanged task head, normal validation reuses its cached valid or invalid
result without rereading that history. When a head changes, it validates only
unseen descendants after the last reachable cached valid checkpoint; if that
boundary is no longer reachable, validation restarts at the root. `--full`
bypasses cached results and semantic boundaries for every current head.

Validation records completed task results independently, so an interrupted run
leaves unfinished tasks pending and the next invocation resumes them. It reports
the exact task ID, full commit ID, category, and message for every invalid task.
An invalid result exits nonzero even when served from an unchanged cache. If
canonical heads change before the final inventory check, affected results remain
pending and the command exits nonzero. Validation never mutates canonical or
tracking Git refs.

Validation also checks that no operation ULID is recorded twice, whether within
one task's chain or across two of them. Checkpoint comparison cannot see that on
its own — repeating a ULID changes no projected state, so every checkpoint still
folds — while the disposable SQLite projection keys its operation rows on
exactly that ULID, with no task in the key. A repeat is therefore unprojectable
by any clone, and `workbook rebuild` reads the same duplicate and stops in the
same place, so reporting it `corrupt-data` is what keeps `validate` from
vouching for a repository nothing else can read. The check spans the whole
project: the chain being read, the prefix a resumed run took from cache, and the
tasks a run answered from cache without reading them. Every prefix a run takes
from cache is loaded before the first chain is read, so the verdict does not
depend on the order task IDs happen to sort in. It names the repeated operation,
the commit that first recorded it, and, when the two are different tasks, the
task that already owns the ULID.

`validate` also checks every relic history under `refs/workbook/relics/`. Each
relic is read from its root to its tip, and every commit must hold exactly the
state, links, part fields and body blobs that its operations produce from its
parent commit. An operation ID is used once in a relic's history: a relic in
which two commits record the same operation ID is invalid, and the failure
names the ID and both commits. That check is made within one relic's history
only. Nothing is compared across relics, and the commands that read one relic
do not walk its history, so only `validate` makes it. A relic whose history
names an object Git does not have, such as a commit, the `parts` directory or
one part's directory, is that relic's failure, and every other relic and every
task is still checked and reported. Relic results are not cached: every run,
`--full` or not,
checks every relic commit again, and none of them is written to
`validation.sqlite`. The JSON result reports them in a `relics` member, which is
left out when the repository holds no relic ref, so a repository without relics
gets the same output it got before relics existed:

```json
"relics": {
  "relicCount": 2,
  "commitsChecked": 5,
  "valid": 1,
  "invalid": 1,
  "failures": [
    {"relicId": "WB-R-01M9A...", "commit": "<full commit ID>",
     "category": "corrupt-data", "message": "..."}
  ]
}
```

`relicCount` is the number of relic refs. `commitsChecked` counts the relic
commits that passed, summed over every relic. `valid` and `invalid` count
relics. `newerWriter` counts relics whose history was written by a newer
Workbook and could not be checked. It is left out when zero, and these relics
are not counted as invalid. `failures` lists, by relic ID, the first commit of
each relic that failed or could not be checked, with its category and message.
A `message` stands alone: it begins `relic <relic-id>: `, and a message about
an object missing from Git goes on with `commit <commit>: `.
Text output adds `Validated N relic(s): N commit(s) checked; N valid, N
invalid.` and one `Invalid <relic-id> at <commit> [<category>]: <cause>` line
per failure, where the cause is the message without that opening, since the
line has just named the relic and the commit. A failure that could not be
checked starts with `Newer` instead of `Invalid`. An invalid relic exits `7` (`corrupt-data`), but only after any
invalid task, which is reported first. A relic written by a newer Workbook exits
`9` (`newer-writer`) only when nothing else is wrong.

An incremental run holds the project's recorded operation ULIDs in memory for
the length of the audit — a few megabytes at the representative 500-task,
20-operation size, and linear in the project's operation count above it.
`validate --full` re-reads every chain from its root, takes nothing from the
cache, and holds none of them.

Validation cost is measured by the benchmark harness's `validate-full-history`,
`validate-cached-unchanged`, and `validate-five-changed` scenarios against the
representative 500-task, 20-operation fixture; run `scripts/benchmark.sh` and
compare reports to see how audits behave over time.

## Board columns

The board `workbook serve` opens in a browser draws one column per status the
project defines, in the order `workbook status list` reports and under the
labels it shows. The task-ID prefixes on its cards are accepted anywhere a task
ID is accepted.

Workbook 0.7 removed the terminal board, the `board` command. Running it now is
the usage error any unknown command gets, with a sentence pointing at `workbook
serve`. A script that read `board --json` reads `workbook list --json` instead:
both returned the same task array in the same order.

The columns are resolved per request, so a status change another clone made
reaches a running `workbook serve` on the next poll. It does
not rebuild the columns under an open page — that would destroy the card nodes
holding open forms, staged changes and unread refusals — and instead shows a
notice offering a reload.

The board says which checkout it is serving and what the project calls itself:
the header's eyebrow reads `Repository: <the worktree's directory name>`, and
the heading, the browser tab and every route's title are the project's own name
where it has recorded one — see
[Board display settings](#board-display-settings). A project that has
recorded a primary or a text color is drawn in it: the server derives the whole
family that color implies — the darker steps a filled control takes, the pale
surfaces, the hairlines, the focus rings — and serves it as a stylesheet
override, so a board named and colored per project is distinguishable from
another one in a strip of browser tabs. The semantic colors do not move: the
danger red, the warning amber and the priority inks mean what they mean
whatever accent a project picks. A priority's ink is nonetheless the project's
to choose: it is recorded against the priority itself rather than derived from
the accent, by `workbook priority color` or by the configuration page's
Priorities section, and a priority that has none is drawn in a color the board
derives from its position — see [Project priorities](#project-priorities). On a
card that ink is drawn straight onto the card, and is given a background of its
own only where the card cannot carry it. The board measures each priority's
color against the card it sits on, in each scheme separately: a color that
clears the 4.5:1 contrast bar there is left as a bare word, and one that does
not — a pale yellow is 1.4:1 on a white card — is drawn on a chip deep enough
behind it to clear 7:1, which is also far enough from the card to read as a
badge rather than a wash. So a board's priorities do not all look alike, and
that is the intent: the same yellow is a filled pill in light mode and a bare
word in dark, because that is what each of those cards can carry.

The `Config` link in the board's header goes to `/config`, where each section
administers one part of what a project configures. **Statuses** administers the
board's columns: add one, rename, relabel
or retag one, remove one into the column its tasks belong in, and reorder them
by dragging a row or with the Up and Down controls beside it. **Priorities**
administers the project's priorities the same way, most urgent first: add one
above or below the ones already there, rename, relabel or recolor one, give one
the `default` role, remove one into the priority its tasks belong at, and
reorder them by dragging a row or with the Up and Down controls beside it. The
role is given rather than taken, so the priority that already holds it shows a
checked box that cannot be cleared — another priority taking it is how it moves.
A row's color is the `#rrggbb` its edit form holds, with a swatch beside the
field as a way into it; emptying that field clears the color and returns the
priority to the one the board derives from its position, which is why the field
rather than the swatch is the control — a swatch has no empty value to pick.
**Keys** administers the project's [task-ID keys](#project-keys) in the order
they were added: each row shows the key, whether it is active or retired, a
badge on the one that is current, and the controls that key can have —
**Make current** and **Retire** on an active key, **Reactivate** on a retired
one — with a form below to add a key and a box to mint new tasks under it at
once. There is no reorder, no rename and no removal, because a key has no rank,
is never renamed, and is a permanent name; retiring one is what the page offers
instead, and the current row carries no control at all, since the way it stops
being current is another key becoming so. Every refusal is the `workbook key`
verb's own sentence, the grammar a name must match included, which the page
does not spell out for itself.
**Board settings** is the project's name and its two colors, as three fields and
one Save; an empty field is a setting cleared, and a save records only the
settings that actually changed — a Save you have not edited records nothing at
all, which matters because a display setting is what marks a project's
configuration as needing Workbook 0.6 or newer. Every section writes the same
ledger the command line writes, so `workbook status`, `workbook priority`,
`workbook key` and `workbook config set` see exactly what the page records.

It is a page rather than a drawer over the board, so its forms have room, and it
is a route like any other here — a bookmark, a reload and a middle-click all
land on it, and `Back` returns to the board. It is `workbook status`, `workbook
priority`, `workbook key` and `workbook config` reached from the browser — the
same rules, the same refusals, in the same words — and everything it refuses is
refused by the vocabulary or by the configuration rather than by the page, so an
unknown tag, a name that is already taken, a color that is not six hexadecimal
digits, or a color a priority already has reads exactly as it does in the
terminal. A removal reports how many tasks it moved and how many of those
`workbook next` can claim where they landed, and any warning a change carries,
such as generated guidelines the server did not rewrite, is shown rather than
swallowed. A rename onto a retired name is one of those: the page shows the
same `status-rename-reresolves` sentence the terminal prints, naming the
retired name, where its history used to resolve, where it resolves now, and how
many active tasks moved. A board served without the four vocabulary mutations has no such
page: no link, and `/config` is a 404; one served without the display writer
has the page and not its Board settings section; and one served without all six
priority mutations — the addition, the rename-and-relabel, the recolor, the
`default` role, the move and the removal — has the page and not its Priorities
section. One served without both key mutations — the addition and the edit that
makes a key current, retires it or reactivates it — has the page and not its
Keys section, and still mints tasks under the project's current key.

The route was `/statuses` before it held more than statuses, and nothing
forwards the old address: a bookmark to it now lands on this board's
`Page not found`.

A few things about it are worth knowing before you use it on a busy board.
A status change is not queued the way a task change is: the page waits for any
card change still in flight — including one you started on the board a moment
before walking here — sends, and re-draws itself from the answer, because
rolling a status change back would repaint every column rather than one card.
It composes each change against the head it read, so a change made from a stale
page is refused rather than applied over somebody else's — and the refusal is
final: the page shows the statuses as they now stand and asks you to look
again, because two people renaming the same column mean two different things.
A priority row's Save, unlike a status row's, is not one write. It sends one
request per operation, in order — the color, then the rename or relabel, then
the `default` role — each composed against the head the one before it answered
with, because no route does two of them at once. The color goes first because
it is the only value in the form whose shape the writer checks, so sending it
first means a mistyped hex code is refused before a rename is recorded. What
that ordering cannot prevent is somebody else writing between two of the
requests. Nothing is rolled back — there is no write that would undo a recorded
rename, and a board authoring a change nobody asked for is worse than one that
says what happened — so the row reports both halves in one line: what landed,
in the words that operation would have reported on its own, followed by the
refusal in the writer's. The row is redrawn from the priorities as they now
stand, so re-opening its form offers the operations that did not land and not
the ones that did, and pressing Save again sends only those.
And the board you walk back to keeps the columns and the priorities you left it
with — their order, and which ones there are — and the name it was opened with;
the notice above it offers the reload that redraws them, exactly as it does for
a change another clone made. Colors are the exception: a color you set here — a
priority's ink, or the project's accent and text colors — is drawn on the board
the moment it is saved, because a configuration answer carries the stylesheet
the server composed and the page can swap it without rebuilding a single card.
That covers a priority nobody colored, too: its ink is derived from its
position, so adding one here redraws the ones carrying no color of their own.
A color another clone set is not, for the same reason its columns are not: that
change reaches this page as a moved head and nothing else. The notice follows
the same division rather than the head: it is raised when the statuses or the
priorities an answer carries differ from the ones the page is drawing — which
there are, their order, or their labels — and stays down when only a color
moved, whoever moved it. Every section of the
page shares one ledger and one tip, so none of them can be changed while another
is changing, and a save of the board's settings, or of a priority, is as much a
reason for a status change to be refused as another status change would be.

## Statuses and priorities a project does not define

A task can hold a status that resolves to nothing this project defines: not a
live status, and not one any recorded rename or removal forwards to a live one.
That is what a clone whose configuration this checkout has not fetched produces
on its own. The web board shows that task rather than dropping it, under a
heading that says the status was not recognized: an "Unknown status" region
below its columns. On the command line, `workbook list` shows the task with the
status it stores, and `workbook status list` counts it among the unresolved. A
task that is invisible reads as a task that was
deleted, which is a worse report than a task that is merely unsorted.

A stored status that a rename or a removal does forward is not in this region.
It is drawn in the column that status now means, and settled to the live token
the next time anything writes to the task; see
[Project statuses](#project-statuses).

The region is a display, not an extra status. Nothing drops into it, because
there is no status such a drop would name. Its cards drag *out*, though, and on
the web board that is the recovery: dropping one on a column sends the same
status write every other drag sends, naming a status the project does define.

Such a task is not frozen, and the rule is narrower than it sounds: membership
is checked against the status a caller *supplies*, not against the one the task
already holds. `workbook update <id> --title …` succeeds, and so do `move`,
`depend` and the board's own save; only naming a status this project does not
define is refused. So the ways to file one of these tasks are to give it a
status that exists — `workbook update <id> --status <one of yours>`, or a drag
out of the web board's region — or to fetch the configuration the clone that
wrote the status was using, after which it resolves on its own.

A priority can resolve to nothing in the same way, and `workbook priority list`
reports such tasks under `Unresolved`. A priority has no column to be left out
of, so the web board marks the card instead: its priority chip still shows the
token, drawn with a question mark and a dashed outline in the same warning
colors as the "Unknown status" region, which no priority is drawn in. Hovering
it says the priority is not one of this project's, and the card's accessible
name says so too. The mark follows the task on every poll, so filing the task
under a priority that exists clears it without a reload. It is judged against
the priorities the page was loaded with, as the columns are: a configuration
change raises the reload notice rather than redrawing the board.

In the task's form, a status or priority select holding a value this project
does not define shows a disabled placeholder rather than one of the project's
values, so an unrelated save never changes it. Both selects word it the same
way and lead with the value, as in `Current: critical (not in this project)`,
so a narrow field cuts off the explanation rather than the value.

### Which exit code a status produces

Two different questions are asked about a status, and they are different kinds
of failure:

| Question | Where it is asked | Failure |
| --- | --- | --- |
| Is this status one the project defines? | Wherever a caller supplies one, to write or to filter by | `validation`, exit `5` |
| Is this string a status token at all? | Every read, over a value already stored | `corrupt-data`, exit `7` |

Membership is a decision, so it is asked only where somebody is still making
one: `create`, `update`, `place`, the board's saves, and `list --status`. It is
deliberately not asked on the way in from a ref, because a status this clone has
not heard of is an ordinary thing for a teammate to have written, and refusing
to read their task would report a broken repository over a stale configuration.

Shape is not a decision. A status is lowercase letters and digits separated by
single hyphens (`core.ValidateStatusToken`), and every build that has ever
written one wrote one of those — so a stored value that fails the rule is not an
unfamiliar status, it is data Workbook did not write. Replay says so with
`corrupt-data`, which is why `workbook status add "Not A Token"` is exit `5`
(you typed it, and you can type something else) while a ref *holding* that value
is exit `7`.

The residue is a task whose stored status fails the shape rule, and it is not a
one-task problem. Every command that reads the project reads every task ref, so
one such task stops the whole project: `list`, `next`, `show` and
`update` of a *healthy* sibling, `status list`, and `rebuild` all exit `7`, and
`workbook serve` answers `/` with a 500 carrying the same error document instead
of a board. `workbook validate` is the only verb that still answers — it exits
`7` too, but it prints the report first, naming the task, the commit and the
rule that was broken:

```text
Invalid WB-01J… at 6c91b62… [corrupt-data]: task state contains an invalid task: status "Not A Token" must be lowercase letters and digits separated by single hyphens
```

That is the corruption doctrine working as intended — Workbook does not serve a
project it cannot read correctly, and `rebuild` refuses for the same reason it
refuses any unreadable ref — but it means the recovery is not a Workbook
command. The ref has to be repaired outside Workbook, with the commit `validate`
names as the starting point.

Only a hand-edited or hostile ref produces one: no Workbook build writes a status
it would then refuse to read, and every path that could — the status verbs, the
mutation boundary, replay — checks the shape first. A repair verb is deferred on
that likelihood alone, not on the cost of hitting it, which is a whole project.
If anybody does hit it, that judgment should be revisited rather than defended.

The web board reads the unknown/known split from one place — `presentation.Board`
separates `Columns` from `UnknownTasks` — so a renderer that consumes only
`Columns` silently deletes tasks from the reader's view.
`internal/presentation/parity_test.go` holds the web board to that model for
one task set.

## Text-mode output safety

Task titles, descriptions, and labels are authored by whoever can push to the
shared refs, so text-mode rendering treats them as untrusted. Every control rune
becomes a space and whitespace runs collapse, so a stored value renders on one
line and an escape sequence in a title cannot redraw the row into a task that
does not exist. Unicode's bidi controls — the overrides, embeddings, isolates,
and directional marks — are replaced the same way, because U+202E in a comment
body makes a terminal draw the rest of the line backwards and an attachment
named `gpj.exe` then reads as `exe.jpg`. Format runes that carry meaning rather
than direction, such as the zero-width joiner inside an emoji sequence, print as
they were written. The same rule already shaped Git commit subjects and now
applies to `list`, `show`, its history and comparison output, conflict
detail, and the line a mutation prints.

The web board is less strict about one part of this: it keeps the three
directional marks and replaces only the overrides, embeddings and isolates. See [Local web board](#local-web-board) for its rule and
the reason.

The description `workbook show` prints is the one place where structure is worth
keeping — most descriptions are several paragraphs, and collapsing them makes
the main human-facing read command hard to read — so it is sanitized a line at a
time and keeps its line breaks. Every line after the first is indented with a
tab:

```text
Title:	Preserve description structure
Description:	The first line sits beside the field name.
	Later lines are indented, blank lines stay blank, and
	Status: done is description text, not a field.
Status:	in-progress
```

The indent is what replaces the collapse, and the guarantee it buys covers the
field block: `show` writes its own fields at column zero, so no description line
can be read as one, and a description that ends with a newline still leaves no
blank line before the next field. It says nothing about the detail sections.
`--history`, `--compare`, and the conflict detail `workbook next` prints all
indent their own structured lines with the same tab, so a description line can
render exactly like one of theirs. Those sections are fenced by the column-zero
header each of them opens with, which a description cannot forge either.

Line breaks are all that survives. Every line is still collapsed on its own, so
leading indentation is dropped and interior whitespace runs become one space:
nested list items, indented code blocks, and aligned tables lose their shape.

This is a rendering rule, not a validation rule. Stored task data keeps the
bytes it was written with, and JSON output reproduces them exactly, because
`encoding/json` escapes every byte below `0x20`. A consumer that wants the
authored text, or one that would rather not reassemble a description from its
printed lines, should read `--json`.

## Local web board

`workbook serve` starts a foreground, loopback-only board at
`http://127.0.0.1:7331` by default. That port is a preference rather than a
requirement: when it is already taken — a second project's board on the same
machine is an expected setup — serve binds a free port instead and prints the
address it chose, so the second board simply starts. The move is never silent:
serve names the collision first, as in

```text
127.0.0.1:7331 is in use; serving on http://127.0.0.1:53321 instead. If you did not start another board, check what is holding that address.
Workbook board: http://127.0.0.1:53321
```

because a process squatting the default port to serve a look-alike board looks
exactly like a second project's board from the outside, and only the person who
started this one can tell the two apart. Only an address already in use is
treated this way; any other bind failure, such as permission denied on a
privileged port, still fails with an operational error. An address given with
`--addr` is a contract and never moves: serve fails rather than silently
listening somewhere the user did not ask for. In fish, run it in the foreground
with:

```fish
workbook serve
```

Or run it in the background with:

```fish
workbook serve &
```

The embedded page and its API expose these routes:

```text
GET /                         board HTML; `?deleted=1` shows the Deleted column;
                              `?q=`, `?priority=`, `?label=` and `?key=` filter
                              the columns
GET /tasks/new                new-task shell; client-rendered form
GET /tasks/<id>               linkable task-detail shell; client-rendered form
GET /api/tasks                versioned task JSON: the active tasks, or
                              `?deleted=true` for the deleted ones alone, or
                              `?deleted=include` for both in one document;
                              each answer carries an `ETag`, and a request whose
                              `If-None-Match` names it is answered
                              `304 Not Modified` with no body
GET /api/vocabulary           versioned status vocabulary JSON: the project's
                              statuses in order, their labels and tags, the
                              forwarding chains, the configuration ledger head
                              they were read from, a `priorities` member carrying
                              the project's priorities, their labels, tags and
                              colors, a `keys` member carrying the project's
                              keys in add order with each one's state and which
                              is current, and — for a project that has recorded
                              any — a `display` member carrying its name and
                              colors at that same head
POST /api/vocabulary/statuses            define a status, optionally placed
                                         before or after an existing one
PATCH /api/vocabulary/statuses/<status>  rename, relabel and retag a status,
                                         in any combination
DELETE /api/vocabulary/statuses/<status> remove a status, naming in the body
                                         where its tasks belong
PUT /api/vocabulary/order                set the whole column order at once
POST /api/vocabulary/priorities          define a priority, optionally placed
                                         before or after an existing one
PATCH /api/vocabulary/priorities/<priority>
                                         rename and relabel a priority
DELETE /api/vocabulary/priorities/<priority>
                                         remove a priority, naming in the body
                                         where its tasks belong
PATCH /api/vocabulary/priorities/<priority>/position
                                         move a priority before or after another
PATCH /api/vocabulary/priorities/<priority>/default
                                         give a priority the `default` role
PATCH /api/vocabulary/priorities/<priority>/color
                                         record the color a priority is drawn
                                         in, or clear it with an empty value
POST /api/vocabulary/keys                add a project key, optionally making
                                         it the one new tasks are minted under
PATCH /api/vocabulary/keys/<key>         make a key current, retire it, or
                                         reactivate a retired one; the body
                                         names exactly one of the three
PATCH /api/display                       record the project's name and colors;
                                         the body states all three, an empty
                                         value clears a setting, and only what
                                         changed is recorded
GET /api/tasks/<id>            the task as `workbook show --json` gives it, for
                              the request's person, without the `relics` that
                              link to it
GET /api/tasks/<id>/history   versioned change log and status lifecycle JSON
POST /api/tasks               create a task
PATCH /api/tasks/<id>         update task fields
PATCH /api/tasks/<id>/status  drag-and-drop status changes
PATCH /api/tasks/<id>/position  atomically change status and board position
DELETE /api/tasks/<id>        tombstone a task, optionally against a task tip
POST /api/tasks/<id>/restore  restore a tombstoned task, optionally into a
                              status and position
PUT /api/tasks/<id>/dependencies/<dependency>     add a prerequisite
DELETE /api/tasks/<id>/dependencies/<dependency>  remove a prerequisite
POST /api/tasks/<id>/comments             add a comment
PATCH /api/tasks/<id>/comments/<cid>      edit a comment's body
DELETE /api/tasks/<id>/comments/<cid>     remove a comment
POST /api/tasks/<id>/attachments          attach a file or a link
GET /api/tasks/<id>/attachments/<aid>     download an attached file's bytes
DELETE /api/tasks/<id>/attachments/<aid>  remove an attachment
POST /api/tasks/<id>/assignments          assign, naming the assignee in the
                                          body as `to`; an empty value is the
                                          request's person
DELETE /api/tasks/<id>/assignments        withdraw, naming the assignment in
                                          the body as `from`
DELETE /api/tasks/<id>/set-aside          dismiss edits the board's sync set
                                          aside, naming the `parkedRef` and
                                          `parked` commit that were shown
GET /api/sync                 versioned publication-mode JSON, reporting the
                              mode, whether a watcher answers, and a `reason` of
                              `no-watcher` or `no-origin` when none does
PUT /api/sync                 change this board's publication mode
GET /healthz                  versioned health JSON
GET /manifest.webmanifest     web manifest naming the board for a home-screen
                              shortcut; see On a phone
GET /icons/<name>.png         the home-screen icons: `apple-touch-icon.png`
                              (180 pixels), `icon-192.png` and `icon-512.png`
```

The delete and restore routes take an optional JSON body, and a request with no
body at all is the bare verb. Delete accepts `expectedHead`; restore accepts
`status`, `before`, `after`, and `expectedHead`, which are the members the
position route uses and mean the same things — a restore naming `before` or
`after` lands beside that task and takes a new position, while one naming only
`status` keeps the position it was deleted with. A body that is present is held to
the same rules every other body is: an unknown member, a trailing value, or
malformed JSON is refused rather than partly read.

The five thread mutations are the `workbook update` comment and attachment flags
reached over HTTP: they run the same planners and record the same operations, so
a change either surface refuses is refused by both, in the words the planner
answers with. They also refuse two things only a request can be wrong about — a
`content` member that is not base64, and a body that mixes the two kinds — which
have no flag to be the counterpart of. Each takes an optional `expectedHead` and
answers with the whole task, thread and attachment list included, the way every
other task mutation does; the two removals also accept no body at all, which is
the bare verb. An edit of somebody else's comment is refused with a `400` and
category `validation`, and nothing is recorded.

Whether a comment is yours is a question about the reader, so the board answers
it on the server for the person each request acts for — the person the
`--actor-header` header names, when `serve` honors one, or else the person
`WORKBOOK_ACTOR` named when `serve` started, or else the serving checkout's
`user.email` — and never from anything the page decides. The three comment
routes answer with an `editableComments` array beside the task: the
identifiers of its comments that person may edit, `[]` when there are none.
The poll's `presentation` carries the same `editableComments` for each task,
leaving it out when there are none. `GET /api/tasks/<id>` answers
`{"format":"workbook.task","version":1,"task":…}`, where `task` is what
`workbook show --json` prints for that person, without a change log: each
comment with `editable`, `editedBy` and `editorNotRecorded`, and
`removedComments`. A board built
without the edit route answers every comment as not editable.

The two assignment mutations are `workbook update --assign` and `--unassign`
reached over HTTP, through the same planners and the same refusals. Both take an
optional `expectedHead` and answer with the whole task; the answer also carries
an `assignments` array — the section as the page draws it, with each row's
recording time, the `value` a withdrawal names it by, and whether this checkout
may withdraw it — because the last two are not fields of the task and neither is
a client's to compose. The rows carry no staleness phrasing; the page words it
from the recording time, exactly as it does for a polled row. Neither takes the claim
gate, and that is the one place the two surfaces diverge: plain `workbook update
--assign` refuses a task somebody else holds with the `assigned` category, and
`--force` is what clears that gate, while assigning from a board is always
additive — it behaves as `--assign --force` does, never as bare `--assign`. An
assignment that lands beside somebody else's answers 200 with an
`assignment-shared` warning. The board offers no way to make a gated claim: the
section a reader is looking at already names everybody who holds the task, so
there is no unseen holder for a gate to protect them from.

Both verbs address the collection rather than one assignment, because an
assignment's name carries a slash — `sam@example.com/impl-1` is one value with a
separator core owns — and a path segment holding it is a request this board
refuses everywhere else. The withdrawal also accepts no body at all, which is
the bare verb the comment removals accept: it releases whatever this checkout's
own identity holds, the same value `UnassignInput.From` reads an empty string
as. A board built without either mutation, or built without an identity to
record one against, answers these two addresses with an operational error and
renders a page that offers neither. The route asks the same question the page
does, so a board that draws no control accepts no write.

An upload carries the file base64-encoded in a JSON member rather than as a
multipart part, and that is the same-origin guard's doing rather than a
preference: `multipart/form-data` is one of the three media types a cross-site
HTML form can send without a preflight, so a multipart route would be the one
address on this board a drive-by page could post to. The body is
`{"kind":"file","name":…,"content":<base64>}`, or
`{"kind":"link","url":…,"label":…}`; a body that mixes the two is refused rather
than read as one of them. `media` is accepted and the board's own upload never
sends it: a media type is written into shared history, and a browser's guess for
the same bytes differs from machine to machine, so the file name decides it
through the table Workbook keeps. A file over the ceiling is refused before
anything is staged, so an upload refused for its size leaves no Git object
behind. That is a claim about the size rule alone, and deliberately: the
refusals that come after staging — a name past its own ceiling, most of all —
write the blob first and then decline the attachment, which is the mutation
boundary's existing order rather than anything this route decides.

`GET /api/tasks/<id>/attachments/<aid>` is the one route that answers with
somebody else's bytes, and its headers are the whole of its security:

- Only an allow-list of media types is served inline — `image/gif`,
  `image/jpeg`, `image/png`, `image/webp`, which is exactly the set of image
  types the extension table can produce. It is a list rather than a test on the
  type: `image/svg+xml` passes any "starts with `image/`" check, and an SVG is
  markup that can carry script, so an SVG served inline would be stored
  cross-site scripting on the board's own origin.
- Everything else is served as `application/octet-stream` with
  `Content-Disposition: attachment`, so an attached HTML file downloads rather
  than rendering. The file name inside that header is formatted rather than
  concatenated, because an attachment name is text somebody typed.
- The file name in that header is not quite the stored one: each control
  character and each direction override, embedding or isolate (U+202A to
  U+202E and U+2066 to U+2069) is sent as `_`, in the plain `filename=` form
  and in the RFC 2231 `filename*=` form alike. So `invoice<U+202E>fdp.exe`
  downloads as `invoice_fdp.exe` in every browser and file manager, rather
  than relying on each to clean the name itself. The stored name is unchanged.
- The board draws a file's name with its control characters and direction
  overrides, embeddings and isolates as spaces. A U+202E RIGHT-TO-LEFT
  OVERRIDE would otherwise draw `invoice<U+202E>fdp.exe` as `invoiceexe.pdf` for
  a reader deciding whether to open it. A drawn file name also reads the
  blank-looking fillers U+3164, U+115F, U+1160, U+FFA0, U+2800 and U+180E, and
  every Unicode space separator, as a space, and collapses each run of spaces
  to one, so `invoice.pdf`, seventy-eight U+3164 and `.exe` draws as
  `invoice.pdf .exe` rather than pushing the extension out of sight. That
  applies to file names in the attachment list, in references and in the
  history. A link attachment's label, its address and the address's tooltip
  are drawn with the same control and direction characters as spaces. The
  stored name is unchanged.
- Everything else the board draws from text somebody typed — titles,
  descriptions, comments, labels, assignees, status and priority labels, the
  project's name, the history, tooltips, accessible names, an image's `alt` and
  every route's browser tab title — is drawn with its direction overrides,
  embeddings and isolates as spaces, on the page the server renders and in what
  the page's script draws. Line breaks are kept.
- The board keeps the three directional marks, U+200E LEFT-TO-RIGHT MARK,
  U+200F RIGHT-TO-LEFT MARK and U+061C ARABIC LETTER MARK, where the command
  line's `show` and `list` print them as spaces. A mark is an invisible letter
  with a direction: Hebrew and Arabic writers use it to put an exclamation mark
  or a digit on the right side of a word. It cannot reverse a run of letters,
  so a file name's extension reads as stored, but marks placed between groups
  of digits can swap them: a title stored as `10.99` with marks around the
  point reads `99.10`. Read an amount or a date that matters from `workbook
  show`, which prints the marks as spaces.
- A one-line field shown before anything is edited — a task's Title field, and
  the configuration page's project name and status and priority label fields —
  holds the cleaned text. A save treats a field that still holds exactly that
  as unchanged and sends the stored text back, or nothing, so saving some other
  change never rewrites the stored title or name to its cleaned form. An editor
  that opens only when asked, such as the description and comment editors,
  shows the stored text exactly.
- `X-Content-Type-Options: nosniff` is set on both paths, so no browser may
  decide for itself that an octet-stream is HTML.
- The response carries `Content-Security-Policy: default-src 'none'; sandbox`
  rather than the page's own policy, which permits the board's inline script.

That allow-list is rendered into the board page as an attribute, and the
markdown renderer reads it from there rather than holding a copy: an attachment
reference becomes an `<img>` only for a type this route would hand back as
pixels, and every other attachment becomes a link to the same route. Two copies
of the set is how a type could come to be drawn inline on a page that the route
would only ever serve as a download.

A link holds no bytes: the route refuses one and names the URL, so a client that
followed the wrong address is told where the thing is. A file whose bytes
[compaction](#task-archive-compaction) dropped is answered `410 Gone`, with the
same `attachment "<name>" was removed when the task was compacted on <date>`
message the command line gives. Any other attachment whose blob this clone does
not hold is answered `404`. So is an identifier the addressed task does not
carry: an attachment is read out of that task's own live list, so naming another
task's attachment, or a comment, reaches nothing.

The vocabulary routes are `workbook status`, `workbook priority` and
`workbook key` reached over HTTP: they run the same planners, so they refuse
what the verbs refuse, in the same words, and record the same operations into
the same configuration ledger. Three things follow from being a server rather
than a command, and each is deliberate:

- Every one of them requires an `expectedHead` member naming the vocabulary head
  the change was composed against — the `head` that `GET /api/vocabulary`
  reports, which is the empty string for a project whose ledger has never been
  seeded. A head that no longer matches is answered `409` with a `stale-write`
  document carrying the current vocabulary, and nothing is merged or rebased: a
  status is somebody's decision about how the project works, and applying two of
  them would invent a third that neither author chose. A key change is composed
  against the same head and refused the same way.
- Each answers with the whole vocabulary document, in the same shape
  `GET /api/vocabulary` serves, including the head the next change must name. A
  status removal also reports how many tasks it moved and how many of those
  become claimable where they land, which is what `workbook status delete`
  reports; a priority removal reports the tasks it moved, claimability being a
  property of a status rather than of a priority. A key change reports no tasks
  at all, because no key change moves one. Keys also have two routes where the
  statuses have four: a key is added and edited, never removed and never
  reordered, so there is no `DELETE` for one and no order to `PUT`.
- None of them writes the generated `.workbook/guidelines.md`. The board is a
  long-running server that may be answering while somebody rebases the checkout
  it lives in, so it records the change and reports that the file is stale; the
  next `workbook status`, `workbook priority` or `workbook key` verb, or
  `workbook docs update`, rewrites it.

The board answers only its own pages. It has no accounts and no tokens, so three
checks stand in for them and every route is subject to all three:

- The `Host` header must name the address the listener bound — except on a
  wildcard bind, which has no one address to name and pins only the port, as
  described below — and its port in every case. Where the host is pinned, a page
  that rebinds its own DNS name to the board's address reaches the port with a
  foreign `Host` and is refused, so it never gains same-origin access. A loopback
  bind is named by any loopback host — `localhost`, `127.0.0.1`, `[::1]` —
  because they all mean this machine. A bind to one address, such as
  `--addr 192.168.1.5:7331`, is named by that address alone: a DNS name is
  refused even when it resolves there, because a name that resolves to the board
  is exactly what a rebinding page sends. That covers a name given to `--addr`
  itself, which binds the address it resolves to and is then pinned to that
  address, so reach such a board at the address `serve` prints rather than by
  the name.
- An `Origin` header, when a browser sends one, must name the board itself. A
  cross-site request that carries one is refused whatever its method.
  `--public-url` adds the public address to what names the board, for both
  this check and the `Host` check; see
  [Behind a sign-in proxy](#behind-a-sign-in-proxy).
- `POST`, `PUT`, `PATCH`, and `DELETE` must send `Content-Type: application/json`,
  with or without a body. The three media types a cross-site HTML form can send
  need no preflight, so refusing them is what keeps a drive-by page from creating
  tasks that publish to `origin` and are later read as agent instructions.

A refused request is answered with a versioned `workbook.error` document and
never reaches task storage: `403` for a foreign `Host` or `Origin`, and `415`
for a mutation that does not declare JSON.

Every request body is bounded before any route sees it, and a body over the
bound is answered `400` naming the ceiling it ran into. That ceiling is 1 MiB
everywhere but the attachment upload, which is bounded at the encoded size of
the largest attachment plus its envelope, for the reason the limits table above
gives. The bound is well above the largest task the field limits above allow,
and it applies to routes that ignore their body as well, so no route can read an
unbounded one by forgetting to ask for a limit. The server also bounds how long a connection may hold a header
incomplete, a request body unfinished, or a keep-alive idle, so a client that
opens a connection and stops talking is closed rather than holding a goroutine
until `serve` exits. There is no response deadline: a mutation publishing inline
waits on `origin`, and that has no honest bound.

The board never asks a client to wait, but a proxy in front of it may: past a
limit it can answer `429 Too Many Requests` or `503 Service Unavailable` with a
`Retry-After` header. When the page gets one of those for a change that names
the head it was composed against, it keeps the change drawn as made, shows a
notice, and sends the same request again after the wait the header names, in
seconds or as an HTTP date. Each resend waits twice as long as the one before,
no wait is longer than a minute, and after three resends the last answer is
reported. These are the changes made on the board and on a task's page —
moves, edits, deletes (a card dropped on the Deleted column and the Delete
button on a task's page alike), restores, comments, assignments and
attachments — and the configuration page's changes. A resend of one of them
cannot apply twice: if the first attempt did reach the board, the head has
moved, and the resend is refused as a stale write. The sync switch names no
head and is resent too, because it sets a mode rather than adding anything:
the same mode set twice is the mode set once. A create, and any other change
that names no head (the relationships, assignments and attachments a New Task
form writes after its create, and a dependency added or removed in the
sidebar), is sent once and its answer reported, because nothing would stop a
resend from applying a second time. Reads are never resent this way; the poll
asks again on its next tick, as it always does.

A stale refusal of a resend does not by itself say the change failed: the
first attempt may be what moved the head. A proxy that times out a slow board
can pass the change on and still answer `429`:

```text
page  -- PATCH, expectedHead: h1 -->  proxy  -- PATCH, expectedHead: h1 -->  board   (recorded; head h1 -> h2)
page  <-- 429, Retry-After: 1 ------  proxy
page  -- PATCH, expectedHead: h1 -->  proxy  -- PATCH, expectedHead: h1 -->  board   (stale: head is h2)
page  <-- 409 stale-write ----------  proxy  <-----------------------------  board
page  -- GET /api/tasks (or /api/vocabulary) ------------------------------> board   (is the change in h2?)
```

So before reporting a stale refusal of a change it sent more than once, the
page reads the task or the configuration again and compares it with what it
sent. A change that is there is treated as saved: the field it was typed in
clears, its form closes or returns to the board, and no failure is shown. A
change that is not there is reported as an ordinary stale refusal. When the
board cannot be read, the page says the change may already have been saved and
to check before making it again, and never says it was not saved; on a task's
page, making the same change again from that panel
reads the board first and sends it only if it is still not there. This check is
best-effort, not a proof: it compares values, so a change somebody else made to
the same values meanwhile is taken for the reader's own. A comment or an
attachment counts only when it is the reader's own, by the name the board
serves, and an added status or priority only when it carries the label, tags
and placement the reader gave it. A file is matched by its name, its size and
who attached it, not by its bytes, so the same person attaching a different
file of the same name and size from somewhere else at that moment is taken for
this one; and on a board serving no identity a new comment or attachment by
anybody that looks the same counts as the reader's.

A waiting change exists only in that browser tab. The notice says so, a task
form's save says so beside Save as well, and the browser asks before the tab is
closed or the page is left while one waits. A save from a task's form that lands after the reader typed more keeps the form
open with what they typed, says it is not saved yet, and sends only that on
the next Save. A `429` or `503` without `Retry-After`, or with one the page
cannot read, is reported as any other refusal, and a refusal whose body is not
JSON, such as a proxy's HTML error page, is reported in a sentence of the
page's own that names the status, on the board, a task's page and the
configuration page alike. The line beside Save that says a save is held is set
in the board's pending color, the held-change notice's own, not its error
color.

Binding `--addr` to anything other than a loopback address makes the board
reachable by whoever shares the network, and those checks cannot tell a
teammate from a stranger. `workbook serve` prints a warning naming the address
and the missing authentication when it does.

A wildcard bind — `--addr 0.0.0.0:7331` or `--addr [::]:7331` — is the one case
the `Host` check cannot close, and it is worse than network reach. Such a
listener answers every address this machine has, under every name that resolves
to one of them, so there is no host to pin: the `Host` check falls back to the
port alone, and the `Origin` check falls back to requiring the header to repeat
the authority the browser addressed, which a rebound name satisfies by matching
itself. Any page on the web can therefore point its own DNS name at this
machine and hold same-origin read *and write* on the board through the browser
of whoever opens it — including writes that publish to `origin` and are later
read as agent instructions — without ever being on the network. `workbook
serve` says so in the warning it prints for a wildcard bind. Bind the one
address you mean instead, and the `Host` is pinned to it.

### On a phone

A screen 560 CSS pixels wide or narrower gets a layout of its own: every phone
held upright, the largest included, is under 450, the narrowest tablet held
upright is 600, and the Workbench app never gives the board less than 740. Above
560 the board is laid out exactly as it was before this layout existed.

At that width the board shows one column at a time. A row of the columns'
names, each with its task count, sits under the header and stays at the top of
the screen while the cards scroll; the header and the filters scroll away with
the page. Tapping a name shows that column and slides the row to bring the name
into view. There is no swiping between columns. A phone opens the board on the
column `workbook next` draws from — the status the project tagged `next`,
whatever it is called — and after that on the column it was last looking at,
from the first frame it draws, with that column's name slid into view in the
row. That column is remembered in the browser's own storage on that phone,
never sent anywhere, and kept for each project apart: under the key the
project's first tasks were minted with and the project's name, so two boards a
phone opens at the same address, as every local `workbook serve` is, each keep
their own. A project that is renamed, or two projects that share both their
first key and their name, are the limits of that: the renamed one opens once on
its `next` column, and the two share one remembered column. A phone whose
browser keeps no storage, or whose remembered column no longer exists, opens on
the `next` column instead, and so does one that was looking at a status since
renamed, because the page knows a status only by its name.

Cards cannot be dragged at that width, so a finger on a card scrolls the page.
A task's status is changed from its own page, which a tap on the card opens.
The task page, a new task, comments, assignments and the change log are laid
out for the width: no page scrolls sideways, a wide code block scrolls inside
its own box, and every control is at least 44 CSS pixels each way. Links inside
the text of a description or a comment are the exception: they keep the height
of the line they sit in, about 18 pixels, because they are read rather than
aimed at. Every field is set in at least 16 pixels, so a phone's browser does
not zoom the page in when one takes focus. The configuration page, where the
project's statuses, priorities and keys are changed, is not laid out for a
phone. The board's notices, such as a change held because a proxy asked the
page to wait, are stacked in one box held at the bottom of the screen while
there is page below it. The box is never taller than a third of the screen
and scrolls inside itself past that, and a control brought into view by focus
is kept clear of it.

Saving the board to a phone's home screen gives the shortcut the Workbench icon
and the project's name. The page links a web manifest whose `display` is
`browser`, which asks for the board to open in an ordinary browser tab, and an
Apple touch icon, and it has no `apple-mobile-web-app-capable` tag. What the
shortcut then does is the phone's to decide:

- Chrome on Android offers two things under "Add to home screen". "Create
  shortcut" opens the board in a Chrome tab, signed in the way Chrome is, and
  is the one to choose. "Install app" is offered for any page, whatever its
  manifest says, and opens the board in a window of its own.
- On an iPhone or iPad from iOS and iPadOS 26, every site added to the Home
  Screen opens as a web app of its own unless "Open as Web App" is switched
  off in the Add to Home Screen sheet, where it starts on. Nothing a page says
  changes that. A web app has its own window and its own website data: it
  starts with the cookies Safari had when it was added, and keeps its own after
  that, so a sign-in that expires has to be done again inside it, and signing
  in again in Safari does not sign it in. Switch "Open as Web App" off when
  adding the board, and the shortcut opens the board in the browser instead.

None of this has been tried on a real phone. The Android behavior follows from
Chrome's install rules and the manifest's own definition of `browser`, and the
iPhone behavior from Apple's description of iOS 26; the board itself was
checked only in Chrome with a phone's screen, touch input and user agent.

The page's content policy carries `manifest-src
'self'` for this: a manifest falls under that directive alone, which falls back
to `default-src 'none'`, so without it the browser refuses the board's own
file. The icons are kept by a browser for a day (`Cache-Control:
max-age=86400`); the manifest names the project, which can be renamed, so a
browser checks it again each time (`no-cache`).

### Simultaneous requests

Every request to the board reads the task refs and brings the shared SQLite
cache up to them before it answers, so requests that arrive together, a
command-line user in the same repository, a board in another worktree, and the
board's own background synchronization all update one cache at the same moment.
This is how each request is answered while that happens:

- A read is never refused because another update moved its rows. Each task
  is written to the cache with a compare-and-swap against the row the read saw
  first; a row somebody else moved in the meantime is left as they wrote it,
  and that task is answered from the checkpoint the read took from its Git
  ref, which is the task as it stood after the request arrived. The bounded
  waits in the third item apply to a read as well, and a read that outlasts
  one is answered as that item says.
- A write reads the task from its ref the same way and records its change with
  a compare-and-swap on that ref. A write that loses the race to another change
  of the same task is answered `409` with a `stale-write` document, as is one
  whose `expectedHead` the task has moved past. Nothing was written; read the
  task again and send the change against its new head. A write is never
  recorded twice, and never on a head other than the one it was composed
  against.
- A bounded wait that runs out is answered `409` `stale-write` too, with
  "try again" in its message, whether the request was a read or a write: the
  cache locked by another process past the five-second busy timeout, the cache
  file replaced by another process during each of three attempts at one
  request, or replaced again each time it was recovered, three rounds running.
  Nothing was written, and the identical request is the right thing to send
  next. The message names the projection cache, which no refusal of a changed
  task does, and a card refused this way says the board was busy rather than
  that the task changed elsewhere. Each wait has a fixed number of attempts or
  a fixed time, so a request under sustained load still gets an answer, though
  that answer can be this refusal.

A `500` naming `workbook rebuild` means SQLite could not read or write the
cache itself — a disk error, or a page it reports malformed — which a rebuild
repairs. A cache that is missing, from another Workbook version, or for another
project is rebuilt without being asked.

### Edits the board's sync set aside

When the board's own background sync fetches a task that changed on `origin`
while the board had unpublished edits to it, it replays those edits onto
`origin`'s history. A replay that meets a conflict — a description both sides
rewrote, an edit to a task `origin` deleted, a dependency that would close a
cycle — stops there: `origin`'s side is kept, and the edit, with every later
unpublished edit to that task, is left out. This is the same reconciliation
`workbook sync` runs, and as there the old local tip is kept under
`refs/workbook/reconciled/<task>/<n>` in the serving checkout and is never
pushed. The CLI reports such a conflict once, on the run that meets it, and
has no command to list or dismiss what was left out.

The board shows it to everyone until it is dealt with. On every listing it
compares each parked tip with the tip of the task it is listing and reports
each parked commit that is not settled. A parked commit is settled when one of
these holds, and in no other case:

- Every operation in it is in the task's history under the same operation ID:
  it was replayed after all, or it is among the operations a compaction
  checkpoint lists as compacted.
- The task's current state carries it: applying its operations to that state
  changes nothing. Text a person copied back in and saved settles this way.
- A state the task reached after the edit was set aside carried it, and the
  board knows where `origin` stood when it was set aside. It knows when the
  same reconciliation replayed some of the parked edits before the conflict:
  the first replayed commit's parent is that point. A replay that stopped on
  its first parked edit leaves nothing that says where `origin` stood, so then
  only the current state counts, and an edit that was copied back and then
  changed again is shown again. A state from before the edit was set aside
  never counts, so an edit that restores a value `origin` held only earlier is
  shown.
- A `comment.add` counts as carried when the task has a comment, added after
  the histories parted, by the same actor with the same body. The same text
  from another actor does not count. Once the task has been compacted since,
  nothing says which comments were added after the histories parted, and no
  comment counts as carried this way: it is shown until dismissed.

Dismissing is the way to clear anything else. Nothing new is recorded and no
format changes: the answer is read from the parked refs, so it survives a
restart and needs no record of its own. A board built without the reader shows
nothing.

The board keeps, in memory, one verdict per parked ref, keyed by the commit the
ref names and the task tip it was judged against, and reuses its listing of
parked refs for up to ten seconds while no task head changed and it moved no
parked ref itself. A verdict that every
later state settles too — all its operations replayed or compacted, or carried
by a state after a known parking point — is kept across tip changes. So a
listing reads only the parked refs that are new or whose task moved, in a
fixed number of Git processes however many there are, and the first listing
after a restart computes every verdict the same way, without waiting on
anything else.

A parked ref the board cannot read is skipped and hides nothing else: one
whose commit is missing from the clone or is not a commit, one whose
`operation.json` does not parse, one whose history shares no root with the
task's, or one holding another task's commits. `workbook serve` writes
`workbook serve: skipped parked ref <ref> (commit <commit>) of <task>, which
cannot be shown as set-aside edits: <reason>` to standard error the first time
it meets each one, and never prunes it.

The poll's `presentation` carries, for a task with set-aside edits, a
`setAside` array, oldest first, and leaves it out otherwise:

```json
"setAside": [
  {
    "parkedRef": "refs/workbook/reconciled/WB-01K…/0",
    "parked": "<the commit the parked ref holds>",
    "commit": "<the commit that recorded this edit>",
    "actor": "alice@example.com",
    "wallTime": "2026-11-21T14:03:00Z",
    "operations": [
      {"id": "01K…", "type": "field.set", "field": "description", "value": "…"}
    ]
  }
]
```

`operations` are the edit's operations as `operation.json` records them. The
board's card says how many edits are set aside, and the task page shows each
one — who, when, the field and the value, drawn through the same cleaning as
every other text — with a button to copy a description or comment and one to
dismiss.

`DELETE /api/tasks/<id>/set-aside` takes `{"parkedRef": …, "parked": …}` from
an edit it was shown and deletes that parked ref, which removes every edit it
holds. It deletes only while the ref still points at `parked`: a ref that moved
is refused with `409` and category `stale-write`, one that is gone with `404`,
and a ref outside `refs/workbook/reconciled/<id>/` with `400`. A dismissal that
races another change to the same ref answers by what that change did, even
when it lands between the check and the delete: `404` when the ref was
deleted, such as by a second dismissal, and `409` when it was moved. It answers
`{"format":"workbook.set-aside-dismissal","version":1,"taskId":…,"parkedRef":…}`
and writes `workbook serve: <person> dismissed the set-aside edits of <task>
held at <ref> (commit <commit>)` to the server's standard error; nothing is
recorded in the repository. The commits stay in the object store until Git
collects them. A parked ref holding a set-aside edit is never pruned by
itself, so it stays until dismissed, or until a manual `workbook compact` or
`workbook compact --adopt <id> --discard-local` in the serving checkout drops
it, after writing it to the backup bundle unless told `--no-backup`.

### Behind a sign-in proxy

Two options let one board serve a team from behind a sign-in proxy such as
Cloudflare Access. [Hosting the board](hosting.md) walks through a complete
setup; this section is what the options do.

```sh
workbook serve --addr 127.0.0.1:7331 \
  --public-url https://tasks.example.com \
  --actor-header Cf-Access-Authenticated-User-Email
```

Each option can be set by an environment variable instead, for a service
manager that sets a process's environment more easily than its arguments:
`WORKBOOK_SERVE_PUBLIC_URL` and `WORKBOOK_SERVE_ACTOR_HEADER`. A flag given on
the command line wins over its variable. A value that is set, by either, and
unusable stops `serve` before it listens, with exit `2`, an invalid-invocation
error naming the flag or variable, and a sentence saying what is accepted. A
blank value is refused the same way rather than read as the option being off.
With neither option and neither variable, `serve` behaves exactly as it did
before the options existed, byte for byte.

`--public-url <url>` is the address people open the board at. It must be an
`https://` address such as `https://tasks.example.com`, or `http://` only for a
host that names this machine (`localhost` or a loopback address), with no path
beyond `/`, no query and no user name. The board then answers requests whose
`Host` names the public address as well as those naming its own bind address,
and no other host; an `Origin`, when sent, must name one or the other. The
other two checks above apply unchanged. A browser at the public address is the
board's own page, and a page on any other site is refused at both addresses.
`serve` prints `Public address: <url>` after the board's own address.

`--public-url` also makes the board *hosted*, and a hosted board keeps the
server's own words out of what it answers:

- An error whose words are the server's — an operational failure, which
  carries the failing Git command and Git's output, and corrupt data, which
  names the objects and files it could not read — is answered with
  `The server could not finish this request. Its log has the detail under
  reference <id>.` and a `reference` member in the error document beside the
  message. The status code and category are unchanged. Every other refusal is
  Workbook's own sentence about the request, such as a title that is too long
  or a task that changed elsewhere, and is answered as it always is.
- A warning whose words are the server's says what happened to the change
  instead, with a reference: `auto-sync-incomplete` reads `The change was
  recorded on the server, but publishing it to the shared repository did not
  finish. The server's log has the detail under reference <id>.`, and
  `projection-update-failed` and `docs-refresh-incomplete` read the same way
  for the server's index of tasks and the generated guidelines.
- The detail goes to `serve`'s standard error as one line,
  `workbook serve: reference <id>: <detail>`, so whoever runs the server finds
  it from the reference a person reports. The reference is twelve random
  hexadecimal digits.
- Every answer carries `Cache-Control: private, no-store`, refusals included,
  so a task page, the task data or an attachment is never kept by a cache
  between the proxy and the person or left on a shared machine's disk. The
  board's styles and script are inside the page, which carries the same
  header. That costs a page download on each navigation, which is small. The
  one exception is the home-screen icons, which are the same for every board
  and say nothing about the project: they carry `Cache-Control: private,
  max-age=86400`, so the phone's browser keeps them for a day and a shared
  cache keeps nothing. The manifest names the project and is not kept.
  The page's own once-a-second poll still sends `If-None-Match` and is still
  answered `304 Not Modified` when nothing changed, because the page sends that
  header itself rather than relying on the browser's cache.

`--actor-header <header>` names the request header the proxy sets to the
signed-in person's email address, and each request then acts for that person:
its edits are recorded under the name, its commits are authored by it, and the
board's answers about which assignments are yours, which you may withdraw and
which comments you may edit are answered for it. The header's name is matched
without regard to case, as HTTP matches header names; its value is used exactly
as sent, case included, and is held to the rule `WORKBOOK_ACTOR` is held to.
That makes the name the proxy sends the name in history, so a person's proxy
name must equal the `user.email` their own clone records, exactly and in the
same case, for the board and their clone to agree that a comment or an
assignment is theirs. `serve` prints `Each request's person: the <header>
header`.

With the header honored, a request must name exactly one person:

- A request with no value, or an empty one, is refused `401` with category
  `invalid-invocation` and `This board takes each person's name from the
  <header> header its sign-in proxy sets, and this request came without one;
  open the board through the sign-in proxy.` That includes reads and the page
  itself, and the home-screen manifest and icons, which a phone's browser asks
  for through the proxy as it does the page. `GET /healthz` is the one
  exception, because a hosting platform calls it directly rather than through
  the proxy, and it reads and changes nothing.
- A header sent more than once, or one whose value carries a comma, is refused
  `400` with category `validation`, `The <header> header was sent more than
  once; it must name exactly one person.` or `… carries several values; …`. A
  proxy that appended its value to one the visitor sent, rather than replacing
  it, would otherwise let the visitor choose their name.
- A value that is not a usable person — not an email address, one with a
  `/label`, one with `<` or `>` — is refused `400` with category `validation`
  and the reason, and is never recorded or replaced by another name.

Each refusal is a `workbook.error` document, except that a browser opening a
page (a path outside `/api/` whose `Accept` names `text/html`) is answered the
same sentence as plain text, which is what a browser draws. Nothing refused is
recorded.

Without `--actor-header`, a request carrying such a header is treated like any
other: every edit is recorded under the person `serve` was started as.

`WORKBOOK_ACTOR` still governs what no request makes. With the header honored,
each request's person is the header's, even when `WORKBOOK_ACTOR` is set, and a
request without one is refused rather than given the variable's name. The
background sync `serve` runs makes no edits of its own: it publishes and
replays the edits people made, each under the name it was recorded under, so
`WORKBOOK_ACTOR` names nobody in it either. `serve` still needs the checkout's
`user.email` or `WORKBOOK_ACTOR` to start, as it always has.

The board does not check that a request came through the proxy. Proxies have
no common way to prove it, and this version does not verify the signed token
Cloudflare Access sends. Whoever reaches the board's port directly can send the
header and act as anyone, so it must be reachable only by the proxy: bind
`127.0.0.1` with the proxy on the same machine. When the header is honored and
the bind is not a loopback address, `serve` prints a warning to standard error
naming the address. It does not stop the board, because a deployment may close
the port by other means, such as a firewall, which `serve` cannot see.

### What anyone who can open the board can do and see

The board has no roles. Whoever can open it — on a person's own machine, that
person; behind a sign-in proxy, everyone the proxy lets in — can do everything
below, through the page or by calling the same routes directly. This list is
the whole of it, so a team deciding whom to let in can read what they are
granting.

Without `--actor-header`, every write is recorded under the person `serve`
was started as — the serving checkout's `user.email`, or `WORKBOOK_ACTOR` —
whoever made it, and that address is shown in the page (the assignment control
names it). With `--actor-header`, each write is recorded under the person the
proxy named for that request, and the page names them instead. Either way the
board has no roles, so nothing below is limited to the person who wrote
something first, with the exceptions noted for comments and assignments.

Read:

- `GET /`, `/tasks/new`, `/tasks/<id>`, `/config` and `GET /api/tasks`: every
  task, active and deleted — titles, descriptions, labels, statuses,
  priorities, dependencies, comments with their authors, attachments with who
  added them, and assignments with who holds them and who made them.
- `GET /api/tasks/<id>/history`: every recorded change to a task, each with the
  email address of whoever recorded it and the old and new values. Text that was
  edited or removed — an old title, a rewritten comment — stays readable here.
- `GET /api/tasks/<id>/attachments/<aid>`: the bytes of any attached file still
  live on its task.
- `GET /api/vocabulary`: the project's statuses, priorities, keys and display
  settings, with the configuration head.
- `GET /api/sync`: whether an `origin` is configured and whether a sync watcher
  answers. It does not give `origin`'s address.
- `GET /healthz`: that the board is up.
- `GET /manifest.webmanifest`: the project's display name. The icons it names
  are the same for every board.

Write, all of it published to `origin` like any other change:

- Create tasks, and change any task's title, description, status, priority,
  due date, labels, position and dependencies (`POST /api/tasks`, `PATCH
  /api/tasks/<id>`, `/status`, `/position`, the dependency routes).
- Delete any task and restore any deleted one (`DELETE /api/tasks/<id>`,
  `POST /api/tasks/<id>/restore`). Deleting records a tombstone; nothing is
  erased.
- Add a comment, edit your own, and remove anyone's. An edit of somebody
  else's comment is refused. Without `--actor-header` every comment is the
  serving checkout's, so whoever opens the board may edit any comment written
  there. A removed or edited comment's earlier text stays readable in the
  task's history.
- Attach files and links to any task, and remove anyone's attachment. Removing
  an attachment does not give back the space its file took.
- Assign anyone to any task by email address, and withdraw any assignment that
  names the request's person or was recorded by them — the serving checkout's
  identity, without `--actor-header`. An assignment
  somebody else recorded for somebody else is refused, as it is on the command
  line.
- Add, rename, relabel, retag, reorder and remove statuses and priorities,
  record priority colors, add keys and change which is current or retired, and
  rename and recolor the project (the `/api/vocabulary` and `/api/display`
  routes). Removing a status or priority moves its tasks where the request says.
- Dismiss the edits the board's sync set aside on any task (`DELETE
  /api/tasks/<id>/set-aside`), which deletes the serving checkout's only copy
  of them. Reading them is part of `GET /api/tasks`.
- Switch the board between publishing inline and handing changes to a watcher
  (`PUT /api/sync`). The setting is the server's, so it changes what happens to
  everyone's next change.

What the board reveals about the machine serving it:

- The serving checkout's folder: its last path element is shown in the page
  header as `Repository: <name>`. The rest of the path is not shown, except
  inside an error message as described below.
- The serving checkout's `user.email`, in the page and on every change it
  records, without `--actor-header`. With it, the page names the reader, and
  the serving checkout's address appears only on what it records itself.
- `origin`'s address: not shown by any route, but a change whose publication
  fails is answered with a warning carrying the failing Git command, its exit
  status and Git's message, and that message usually names `origin`'s full URL
  or path, with the credential removed as described next. For example, a task
  created while `origin` is unreachable is answered `200` with the warning
  `the change was recorded locally, but publishing it failed: cannot ask origin
  which Workbook project it holds before publishing: git ls-remote --refs origin
  refs/workbook/project failed: exit status 128: fatal: unable to access
  'http://127.0.0.1:1/team/secret-path/repo.git/': Failed to connect …`, and
  one whose `origin` is a missing folder names that folder's path on the
  server. Task, status, priority, key and display changes all answer this way.
  An operational error carries the failing command and Git's own words in the
  same way, which can include paths on the server. A hosted board — one started
  with `--public-url` — answers neither: it gives a short sentence and a
  reference instead and writes the detail to its log, as
  [Behind a sign-in proxy](#behind-a-sign-in-proxy) describes, so the server's
  folder, `origin`'s address and Git's output reach only whoever can read that
  log.
- `origin`'s credential: the user name and password in a remote's address are
  removed. When a Git command fails and its error output contains an `@`,
  Workbook looks up the addresses Git is configured with — each
  `remote.<name>.url` and `pushurl`, each `url.<base>.insteadOf` and
  `pushInsteadOf`, and the addresses those rewrites produce — and removes each
  address's credential, by its exact text, from Git's error output and from the
  command the error names. A failure whose output holds no `@`, and a
  successful command, cost no extra Git process. Outside its address, a
  password of eight characters or more is also removed wherever it appears on
  its own, as `***`. A user name of that length is removed on its own only when
  its address has no password, or only a placeholder for one (an empty
  password, one shorter than eight characters, or `x-oauth-basic`), because a
  token is often sent as the user name (`https://<token>@host/…`); beside a
  real password, a user name such as
  `workbook` is removed only inside its address, so `refs/workbook/tasks/*` and
  a path segment `/workbook/` read as written. A shorter user name or password,
  such as the user name `git`, is removed only inside its address. Any other
  `scheme://user@` in Git's output loses its user part too.
- What is not removed: anything a credential helper, a Git hook or the remote
  itself prints (a helper that writes `refreshing token <token>` to its error
  output is reported as written), and a token placed in an address's path or
  query string, such as `https://git.example/repo.git?access_token=<token>`. A
  deployment should give the server its credential by a means that keeps it out
  of the address where it can: a credential helper that prints nothing, or an
  SSH key, rather than a token in `origin`'s URL.

One limit a hosted board does not have yet, so whoever deploys one should know
it: nothing bounds how many tasks, attachments or requests one person may send
beyond the per-request and per-task ceilings above, so one person can fill the
server's disk and `origin` and slow the board for everyone. Which person behind
the proxy made a change is recorded when `--actor-header` is given.

Drag a task card within a column to reorder it or into another of the
project's status columns to change status and position together. Workbook keeps the task's priority
unchanged and clamps drops outside that priority group to the nearest group
boundary, so dropping at the top or bottom of a column still has a clear result.
The placement creates one normal Workbook operation commit on the moved task and
returns a versioned JSON task-mutation document. The older status-only endpoint
remains available for compatible clients.

The board renders a change before it is durable and reconciles it afterwards.
Each mutation becomes an intent held in a per-task queue: the card moves at
once, and the intent is sent with the task tip the board rendered. One task's
intents are sent one at a time, because each carries the head the one before it
returned, while independent tasks publish concurrently, so a slow write to one
card never stalls the rest of the board. A confirmed response replaces the
projected task with what Git recorded. A refused one drops only the intent that
failed and keeps the ones queued behind it, because those were separate
decisions; a `stale-write` refusal re-bases the queue on the tip the server now
holds so the intents behind it retry against current truth rather than failing
identically. Optimism is confined to the display: HTTP success still means the
operation commit exists in Git.

A refusal is reported on the card it concerns, in wording that distinguishes a
task someone else changed from an ordinary failure, and the report stays there
until a **Dismiss** button on the card clears it or a later write to that task
is accepted. It survives every poll: the board is current again after one, but
the conflict still happened, and a report cleared a second after it appeared is
one nobody on a shared board ever reads. The stale banner keeps its own single
subject — a refresh that failed — because *this board is behind* is a condition
a successful poll ends and *your change was refused* is an event only a person
can acknowledge. If the card itself goes, which is what happens when the change
was refused because another clone deleted the task, the report moves to the
notice above the board naming the task it was about.

A task's detail form can be open when its board change is refused, in which case
the form is showing the value the server just refused. It is corrected where it
stands rather than rebuilt: fields you have not edited follow the version the
server holds, everything you have typed stays exactly as typed, a save already
in flight keeps reporting into the form, and a message says which change was not
applied. Saving afterwards sends only your own edits, against the version that
now exists. If the task has left the board entirely, the form and your unsaved
text stay put and say so. If the refusal's refresh could not read the board at
all, those untouched fields are the last successful poll's rather than the
server's, and the message says the board could not be refreshed instead of
claiming otherwise.

The board header keeps its two route links — Board and Statuses — on the left
beside the title, and its settings on the right, so a link is in the same place
on every route even though the settings a route offers are not. Each setting is
a switch: words that say what the next flip does, then a track whose knob and
`aria-checked` say what is true now. All three answer the mouse, Space and
Enter.

The publishing switch says what the *next* mutation will do with `origin`, and
flipping it changes that for this server. On — the default — hands publication
to a watcher: a mutation returns as soon as the write is durable and a running
`workbook sync --watch` publishes it just behind the response, so the switch
offers **Push** as the way back. Off means the board attempts the push itself
and answers afterwards, so the switch offers **Publish** as the way to hand that
job away again. Neither mode makes publication a condition of success. As on the CLI, an unreachable `origin` is a warning and not a
failure, so an inline mutation whose push failed still answers `200` with the
change recorded locally and an `auto-sync-incomplete` warning naming the error;
a response says the operation commit exists in Git, never that `origin` has it.
The indicator is read from `GET /api/sync` rather than assumed, because it
reports what the next mutation will really do: a board set to defer publishes
inline when no watcher answers, and a repository with no `origin` publishes
nothing at all. In either case the switch has nothing to set, so it marks itself
`aria-disabled`, says in its title why it cannot be flipped, and changes no mode.
The two cases are not the same news, and the document's `reason` tells them
apart so the switch can read each in its own words. With an `origin` configured
and nobody answering — none running, none reachable, or one whose last
synchronization failed — the reason is `no-watcher`, both modes publish inline,
and the switch reads **No watcher**, because starting one brings it back. With
no `origin` the reason is `no-origin`, nothing is published in either mode, and
the switch reads **No origin**, because no watcher can help until a remote is
added. Activating it in either state reads `GET /api/sync` again rather than
writing, so a watcher started — or an `origin` added — after the page was opened
brings the switch back to life without a reload; a read that fails changes
nothing and leaves the switch where it was. The mode is a preference held in
memory for the life of the server, not a project setting, which is why it is
separate from `workbook config set auto-sync`.

The executable embeds its HTML, CSS, and JavaScript, and the page polls
`/api/tasks` every second. Each answer carries an `ETag`, and the page sends it
back as `If-None-Match`; a poll that finds nothing changed is answered
`304 Not Modified` with no body, counts as a sync, and redraws nothing. The
document carries nothing derived from the clock — an assignment carries the
time it was recorded, never a phrase such as "assigned 5 minutes ago" — so a
board nobody is changing is answered 304 on every poll, however recent its
assignments. Each
poll that does bring a new document reconciles the board by task ID instead of
rebuilding it: a card whose data did not change keeps its element, a changed one
is updated in place, and only added, removed, and reordered cards are touched.
Keyboard focus therefore stays on the card that had it, a drag that outlasts a
poll stays intact, and a scrolled column stays where the reader left it. The six
canonical columns share the available width on large screens but never go below
a readable minimum: a window too narrow to give all six that much scrolls
horizontally instead of squeezing them, and each column keeps a dense task list
vertically scrollable within the viewport. Web cards show the actionable
task-ID prefix, priority, title, and labels, plus the report of a refused change
described above while one is standing; each title links to its full-ID
task-detail URL, where the complete description remains available. Every status
column has a New Task link that preselects that column's status.

A description is the one card field with no length a column can rely on, so the
board hides it and a **Show Descriptions** switch in the header puts up to six
clamped lines of it back on every card. The choice says how one reader wants this board
drawn rather than anything about the project, so the browser remembers it and
nothing is published to `origin`; two people reading the same board can read it
differently, and a browser that refuses storage simply gets the default. The
text stays in the card either way and only the stylesheet decides whether it is
shown, which keeps the setting out of the renderer: turning it on reveals what
the page already holds rather than rebuilding cards or waiting for the next
poll, and the reconciling poll has no preference to reset. The toggle
accompanies the board alone; task pages draw no cards for it to act on and so
do not offer it.

A filter row sits under the header. Its search box narrows the columns to the
cards whose title or description contains every word typed, case-insensitively,
or whose task ID begins with a word shaped like one — a key, a dash, and more
characters — so a pasted ID finds its card and a stray word never matches the
inside of an ID; that is the same rule `workbook list --find` uses. Its
Priority, Labels and Key choosers each offer a list of ticks with the count of
cards each would leave; ticks within a chooser are alternatives, and the search
and the choosers narrow one another. The Labels menu has a box of its own that
narrows its rows as you type, since a project may carry many labels; what is
typed there is not part of the address. The Key chooser appears only once a
project has more than one key, retired keys included because their tasks still
exist, and a board already open learns of a second key when the configuration
page is visited or on its next load; a key added from the CLI does not reveal
the chooser on a board left open, because a key change moves no column and the
board is never asked to reload for one. Every control writes the address —
`/?q=audit&priority=high&label=web&key=WB` — so a filtered board is shareable,
bookmarkable, and walked by Back and Forward, and typing in the search replaces
the current entry rather than filling history with keystrokes. A column's count
reads `3 / 12` while a filter hides cards, and a column the filters empty says
**No matches.** rather than looking empty. Dragging is unchanged while
filtered: a card dropped between two visible cards is placed between them,
whatever hidden cards lie in that stretch.

Deleted tasks are a column of the board rather than a page of their own. The
row's **Show Deleted** switch is a link to `/?deleted=1`, so that state is
shareable, bookmarkable, and walked by Back and Forward like the filters beside
it; showing it appends a muted **Deleted** column after the last status column
and hiding it takes that section away, and neither touches another column or
another card. The column is fed by the same one-second poll as every other
column, which asks `/api/tasks?deleted=include` for as long as the board is set
to show it — opening a task's page does not stop the asking, so the column is
already current when you come back to it — and orders its cards
most-recently-deleted first. Each card carries a **Restore** button that
returns the task to the status it was deleted from. Dragging a deleted card
onto a status column restores it into that column at the position it was
dropped, and dragging a live card onto the Deleted column deletes it; both are
queued optimistic changes like every other board drag, with the same head
check, the same rollback, and the same refusal report on the card. A deleted
task has no detail page — a tombstone cannot be edited — so its card offers
Restore instead of a link.

Cards with prerequisites show completed versus total dependency progress.
Ready cards whose prerequisites are not all active and Done also say
`Waiting on dependencies`. Task forms list a task's edges in one
**Dependencies** section, seen from both ends of the same directed edge: a row
tagged **Depends On** is one of the current task's prerequisites, and a row
tagged **Blocks** is a live task that depends on the current task. Depends On
rows come first, then Blocks. One combobox searches eligible active tasks, and
an **Add as** toggle (a radio group, so the arrow keys move it) chooses whether
the task picked is added as Depends On or Blocks; it starts on Depends On and
stays where it was put after an add. The combobox leaves out the tasks the
chosen kind already lists, the current task itself, and every task whose edge
would close a cycle through the current task: under Depends On, any live task that
already depends on the current task, directly or through other live tasks;
under Blocks, any live task the current task already depends on, directly or
through others. Staged and unsaved rows count as edges for this, which is what
the New Task form has. A task left out this way is offered again once the
toggle moves to the other kind or the row that closed the path is removed. The
highlighted option in the open list stays on its task across the board's
once-a-second refresh, and the list stays where it was scrolled. The server
remains the authority: if the stored dependencies already hold a cycle, it
refuses any edge that leads into it, and the combobox still offers those tasks.
Adding and removing use
the nested Git-durable mutation routes above: a Depends On edge is written to
the current task and a Blocks edge to the other task.

Task forms use a wide main column and a compact sidebar holding, in order,
Properties (status, priority, due date and labels), Dependencies, Assignments,
and Attachments; the New Task form and a task's own page lay it out alike.
Every section is spaced the same way, a rule between each, and every
dependency, assignment and attachment is drawn as the same bordered card with
a red × at the right end of its first line that removes it. Each × is a
keyboard-reachable button named for what it removes — "Remove dependency on
<title>", "Remove blocked task <title>", "Unassign <who>", "Remove <name>" —
and that name is also its tooltip. A dependency row shows the other task's
title, its kind, and its status and priority; the task's ID is the title's
tooltip rather than a line of the row. The dependency picker's box and its
**Add** button share a line, as the assignment box and **Assign** do. The
New Task header is a compact band reading "New task"; both pages put a ×
(Close) at the right of the header that leaves the way Back does, and a New
Task form's footer has Save with **Create more** beside it and Cancel at the
far right. A New Task form on a project with more
than one active [key](#project-keys) also offers the key to mint under, standing
at the current one; a project with a single key draws no such field, and a form
whose reader took the default sends the create every client sent before keys
could be chosen. New Task stages both Depends On and Blocks entries without
writing task refs; relationship mutations run after the task receives its
durable ID. If
only some edges succeed, successful relationships remain durable while failed
relationships remain available to retry or remove.
In a window at least 761px wide and 701px tall, both forms are held to the
height the window has left under the app header: the description and the Save
row stay on the screen, and a sidebar longer than that scrolls inside the form.
A task's page has its comments and change history below the form, and the page
scrolls down to them. A shorter window scrolls the whole page instead, sidebar
included. On narrow screens, the task editor, the sidebar's sections, and the
actions stack in that order.

Saving a new task returns to the board, and a **Create more** toggle on the New
Task form re-arms a clean form filed under the same column instead, so a run of
tasks can be entered without a round trip; the re-armed form takes the caret,
because the save it followed destroyed the control the user was on. Either way
the saved task's own form is left behind.

A save that staged no relationships lands immediately rather than waiting for
Git. The board draws the task from what was typed and hands it the caret while
the create is still open, the same way a dragged card lands before its placement
is durable. The stand-in card offers no ID to copy, no detail link, and no drag,
because none of those exist until the server answers, and it is retired by the
refresh that brings the task itself rather than by the response, so a poll that
left first cannot take it away again. A save that staged Depends On or Blocks
still waits: those writes need the ID the server assigns, and the sidebar that
reports and retries them is destroyed by an instant landing. That create still
opens the task's detail page when it has a warning or an edge it could not
write, because that is the only place its retry actions exist.

What an instant save turns out to have to say is reported in a notice above the
board rather than by moving the user again, who is by then reading the board or
typing the next task. A warning names the task and links to it. A refused create
names what was refused and offers **Restore draft**, which reopens the New Task
form holding every value that was typed and the reason the server gave. The same
notice carries a board refusal whose card has left the board. Reports stack and
are cleared by a click rather than by the next poll, because one of them can be
holding the only copy of a task that was never saved.

Labels are a set, and the form edits them as one chiclet per label rather than
as a line of commas. The input holds only the label being typed; Enter or a
comma commits it and clears the input, each chiclet carries a named remove
control, and Backspace in an empty input removes the last one. A label left
half-typed is still sent when the form is saved, and the payload the API sees
is the same array of strings it always was.

Missing prerequisite IDs remain visible and removable. Tombstoned
prerequisites are also removable because the active dependent owns that edge.
A deleted task that depends on the current one is not listed as Blocks. Its
edge is stored on the deleted task's own record, which a tombstone does not let
anyone change, and it has no effect while that task is deleted, because
readiness never reads a deleted task's dependencies. Restoring the task lists it
again, and from there its Remove control drops the edge as it would for any
other.
Dependency warnings and failures are reported on the Dependencies section's
own message line, and dependency refreshes leave unsaved task-form fields
mounted.

Task detail pages show that task's change history by default, unlike
`workbook show`, where it is opt in behind `--history`. The view leads with a
status lifecycle lane rather than another flat row type: status is the most
common change by a wide margin, and a lane reading Backlog to Ready to In
Progress to In Review to Done tells a task's life in a way a chronological list
cannot. Each stop names the change that entered that status and marks where the
task stands now. No operation records the status a task was created in, because
a create pack carries the whole task rather than a field change, so the lane
opens at the earliest status the log can name; a task whose status never changed
shows its current status as the only stop, without attribution.

Every other field change reads as one ordinary row per operation pack beside the
lane, newest first. That is the same parent chain `--history` prints, read from
the current tip backwards rather than sorted by wall time, so a timestamp that
reads out of order after a reconciliation stays where the chain puts it. A pack
that changed only status is not repeated as a row, and the row count says how
many the lane absorbed. Selecting a row expands it in place into the
field-level comparison the server already computed for that pack, description
word diff included, and names the commit object so the same two points can be
compared from the CLI. Expanding needs no second request, no comparison route,
and no range syntax, so it keeps working with the board offline. A history read
that fails leaves the task form usable and offers a retry, and a history the
server could only read in part says where it stopped.

Click a card's shortened task ID to copy its full ID. The ID remains part of the
card's drag target, so dragging moves the task while a click copies. The task
detail route provides the same action on its full ID. Copy feedback appears
inline beside the ID without shifting the board or task form;
polite live announcements identify the full task ID.

The shared new-task and detail form creates or edits title, description, status,
priority, and labels through the versioned APIs, and a create may name the key
as well. Saving returns to the board and refreshes it. A failed save leaves the
entered values in place and shows the server error in the form; Back returns to
the board without mutating a task.

An edit to an existing task sends only the fields that form changed, together
with the task tip it rendered. A change someone else made to a field you did not
touch therefore survives your save, and a save proposed against a tip the task
has moved past is refused rather than applied: the form keeps your edits, says
the task changed elsewhere, and saving again applies the same fields to the
version the server now holds. Saving a form you changed nothing in sends no
request at all and simply returns to the board.
Active task details also provide Delete; a successful deletion returns to the
board with its Deleted column shown, where the task now is and where the
Restore that undoes it sits on the card.

A task's page also carries what is attached to it, in its sidebar, and what
has been said about it, below the form and above the change history. The attachment
list shows both kinds in one list — a file's name links to its download, a
link's label (or its URL, when it has no label) to its destination — with one
line saying what kind it is, a file's size, the day it was added and who added
it. The line's tooltip adds a file's media type and the exact time, and a
link's URL is its name's tooltip rather than a second copy in that line. Under
the list are a **Choose file** button, which opens the browser's own file
picker (the real file input stays the focusable control, so the keyboard,
screen readers, multiple selection, drop and paste all work as before), and a
form for adding a link: the URL with **Attach** beside it, then the optional
label on its own line. Enter in either box attaches. Each row has the same red
× the other sidebar rows have. A
chosen file is listed by name with a × that takes it back out before anything
is sent, and the chooser gives way to an **Add file** button until the list is
empty again; the New Task form's staged list works the same way, its buttons
reading **Choose files** and **Add**. An empty
list draws only its heading and controls. The
thread is drawn oldest first, each comment carrying its author, when it was
written, and whether it has been edited since — and by whom, when that was not
its author, or that the history no longer records who, for an edit made before
the task was compacted — with a Remove on each one, an Edit only on your own,
and a box for adding another. A removed comment stays in its place as one quieter line naming
whose it was and who removed it when, without its text. The page reads those
lines from `GET /api/tasks/<id>`, once when it opens and again whenever the
task changes. Comment bodies render as markdown, in the
subset documented below.

None of these is drawn optimistically the way a board drag is. A comment's
identifier, author and timestamps are minted by the operation that records it,
so there is nothing for a client to paint in advance; each change is sent with
the tip the page read, and the thread the answer carries is what is drawn. A
refusal is reported in the section it was made in and keeps the text you typed;
a tip that has moved on is re-based, so saving again applies your change to the
version the server holds. The one exception is an edit whose comment somebody
removed while you were editing it: no save can succeed, so the form says it
was removed and by whom, drops its Save, and keeps what you typed in the box
for you to copy. A file too large to attach is refused before it is
read or sent, naming the ceiling, and the server enforces the same one. The
board's cards say nothing about either section: a card is an identifier, a
title, and its labels.

## Markdown in descriptions and comments

Task descriptions and comment bodies render as markdown in the browser. The
command line prints them exactly as they were written, and what is stored is
always the text somebody typed — the formatting is a rendering, not a
conversion.

A task's page opens with its description rendered and an **Edit** beside the
caption; Edit swaps the rendering for the textarea it always was, and Done puts
the rendering back. Saving is unchanged: the same field, the same "send only
what you changed", the same tip the page rendered against.

**What renders.** Headings (`#` through `######`, drawn as `h3` down to `h6` so
a heading in a description never outranks the page's own), `**bold**` and
`*italic*` and `` `inline code` ``, fenced code blocks (`` ``` `` or `~~~`),
unordered lists (`-`, `*`, `+`), ordered lists (`1.`, `1)`), blockquotes (`>`,
nested up to six deep), links (`[text](https://…)`), and images that name an
attachment of the same task. A single newline is a line break, because a
description written in a textarea means the breaks it was typed with.

**Images are attachments, and only attachments.** `![alt](attachment:<id>)`
resolves against the task's own attachment list — by full identifier or by any
prefix that names exactly one file, the same way the command line resolves one —
and draws the attachment through `GET /api/tasks/<id>/attachments/<aid>`. An
attachment the download route serves inline (`image/gif`, `image/jpeg`,
`image/png`, `image/webp`) is drawn as an image; anything else it holds — an
SVG, a PDF, a text file — is drawn as a **link** to the same route, which
answers those with `Content-Disposition: attachment` and downloads them rather
than rendering them. That link always reads as the file's real name, drawn as
above; the reference's own text, when it says something the name does not,
follows the link as a caption in parentheses. So
`![invoice.pdf](attachment:<id>)` naming a file called `invoice<U+202E>fdp.exe`
draws as `invoice fdp.exe (invoice.pdf)`, never as a link reading
`invoice.pdf`. An image's alternative text follows the same rule — the file's
real name, then the author's text in parentheses — because a browser draws it
in place of a file that does not decode as an image. A reference that resolves
to nothing, to more than one attachment, or to a link attachment is drawn as
the text it is. Every other
image target — an external URL, a `data:` URI, an address on this board — is
drawn as text as well: a board that fetched images named in task text would
report every reader of every card to whoever wrote the task.

The page's own content policy is the second lock on that door and the reason
the first one can be trusted: it carries `img-src 'self'`, so the browser
refuses any image address outside this server whatever the renderer builds. It
is also load-bearing in the ordinary direction — without the directive, images
fall back to `default-src 'none'` and a browser blocks every attachment picture
on the page.

**Links** are drawn as links only for `http` and `https`, and carry
`rel="noopener noreferrer nofollow"` in a new tab, exactly as an attached link
does. A target with any other scheme is drawn as its words with no address
attached.

**Board cards** render the inline half of the subset — bold, italic, inline
code — and nothing that makes a block. A card's description is clipped to six
lines inside a box that must never be wider than its column, so a heading, a
bullet or a fence on a card is drawn as the characters it is, line breaks run
together as they always have, and a link is drawn as its words. A card with a
formatted description is exactly as tall as the same card was before any of
this existed.

**What deliberately does not render.** Raw HTML (markup in a task is text about
markup — the renderer builds nodes and has no HTML path at all), tables,
footnotes, bare URLs, setext headings, thematic breaks, indented code blocks,
reference links, and `***three delimiters***`. Nested lists render flat. An
ordered list always counts from one. A link's caption may hold no bracket, so
`[a] b](https://…)` is text rather than a link captioned "a] b" — a caption is
the words between one pair of brackets, never a run reaching across somebody
else's. Anything the parser does not recognize, and anything it recognizes but
cannot complete — an unclosed `**`, a fence with no end, a link whose target
holds a space — is drawn exactly as it was typed.

**There are no backslash escapes, and that is the one exception to the rule
above.** A backslash is an ordinary character: it does not suppress the marker
that follows it. `\*text\*` renders as *text* in italics with both backslashes
still in it, which is not what somebody writing it meant. It is the only input
this renderer draws as something other than what was typed, it is written down
here rather than discovered, and it is what to fix first if the subset grows.

The web experience is still local-first and intentionally narrow in scope.
Authentication, hosted deployment, browser deletion, draft persistence, and
broader collaboration remain future work. A request using the wrong method for
a known route receives `405` with the route's allowed method.

Development performance is measured with the reproducible, bounded
`workbook-bench` harness. One command builds the working tree and runs the
whole scenario registry, writing a dated JSON and Markdown report pair under
the gitignored `bench-reports/` directory and printing the Markdown path:

```sh
scripts/benchmark.sh
```

Any `workbook-bench` flag passes through, so a quick diagnostic run looks like
`scripts/benchmark.sh --scenario cli-list --tasks 50 --operations 5`, and
`scripts/benchmark.sh --scaling` runs the task-count and history-depth scaling
matrix instead of the single-fixture scenarios. Reports are descriptive: they
carry no thresholds and no pass or fail classification. Each scenario reports
its measured latencies and Git process counts with a `completed`, `failed`, or
`timeout` outcome, and reports include the SHA-256 and source commit of the
measured binary; comparing a run's report against an earlier one is how a
baseline shift is noticed.

In the benchmark CLI, `--tasks` counts total refs; omitted `--tombstones`
produces 25 tombstones at 500 or more total tasks and one in smaller
diagnostics, while an explicit zero is diagnostic-only. Remote synchronization
benchmarks select one or more of seven named topologies with repeatable
`--scenario` flags and require at least 500 total tasks with 20 operations per
task, so a per-command number always describes a board someone actually runs.
Cold CLI rebuilds and each warm HTTP sample's preparatory task-list load are
untimed setup; the `api-tasks` read scenario times a second, warm load of its
own.

The three commands an agent runs continuously — `next` to acquire work, `show`
to read its context, `update` to record progress — are all measured. `cli-next`
includes the fetch `next` performs before answering so two agents cannot claim
the same task; that round trip is priced rather than hidden. The
`watch-steady-state` selector observes a live `workbook sync --watch` with
nothing pending and reports its CPU, peak resident memory, and per-tick cost
descriptively; it is measured in its own invocation.

## Project identity across worktrees

A project's canonical identity lives in a Workbook ref, not in branch content:

```text
refs/workbook/project
 └── commit (no parents, fixed author/committer/date)
      └── tree
           └── project.json   {"format":"workbook.project-identity","version":1,"projectId":…,"key":…}
```

The document holds only the project ID and the project key, both immutable after
mint. That key is the project's *founding* key: it is the implied first entry of
the project's key set and is current until the configuration ledger says
otherwise, and which task IDs belong to this project is decided by that set
rather than by this document alone. See [Project keys](#project-keys) for how
the set is changed and for what a key the ledger has added means for a clone
that cannot read it.

The ref holds exactly one root commit and that commit's tree holds exactly one
entry; a tip with parents or a second entry is rejected rather than
interpreted. `refs/workbook/project` is a leaf name and must stay one — Git's
directory/file rule means no ref may ever be created under it, so a future family
of project documents belongs in a sibling namespace.

The commit is deterministic: fixed author and committer (`Workbook
<workbook@invalid>`), the Unix epoch in UTC, a fixed message, and no parent. Two
clones that adopt the same identity therefore build the *same* commit object, so
a second publication is "everything up-to-date" rather than a competing history
that origin must reject.

`.workbook/config.json` stays committed and readable, and keeps deciding the
mutable preferences it owns (the document version and `autoSync`). Its `projectId`
and `key` are now advisory copies: they are what a v0.4.x teammate reads, and what
a v0.5.0 clone migrates from, but the ref decides. Nothing rewrites or strips
them; slimming the tracked document is a deliberate, later upgrade.

Identity is resolved once per opened repository, in this order:

1. **`refs/workbook/project`** — canonical; it wins outright.
2. **`.workbook/config.json`** — its identity is adopted and published to the ref
   once. This is the self-healing migration for every project created before
   v0.5.0.
3. **`<git-common-dir>/workbook/project.json`**, the private guard — adopted and
   published the same way when no branch in the checkout carries the tracked file.
4. **Nothing** — the repository is not initialized. Only `workbook setup` mints a
   new identity; no ordinary command can invent a project by being run in the
   wrong directory.

Because the ref lives in the common Git directory, every linked worktree shares
one identity, and a checkout of a branch that predates Workbook adoption is no
longer mistaken for a new project.

The private guard is still written in v0.5.0 for downgrade safety, since a v0.4.x
binary sharing the repository reads it. What changed is the verdict on
disagreement: when the ref and the guard disagree, the guard is **repaired** from
the ref and the command proceeds. The older wedge — the error naming both files
and telling you to delete the guard — remains reachable only while no identity ref
exists yet. Deleting the guard entirely waits until no supported version reads it.

One disagreement is still fatal: a tracked `.workbook/config.json` whose
`projectId` or `key` differs from the ref's. That means two records claim
different projects for the same working tree, and continuing would write one
project's operations into another's history. The error names the ref, the tracked
file, the guard, both identities, and the way out. What is *not* fatal is
reported instead, as a single `workbook: warning:` line on stderr: a checkout
whose branch carries no advisory copy at all, and a private guard that had to be
repaired. A tracked document written by an older version keeps working untouched;
`workbook setup` is where its version is reported and, deliberately, upgraded.

The same rule governs publication. No path publishes a task ref to `origin`
without first establishing that `origin` is this project: `workbook push`, and
the targeted push every automatically synchronizing mutation makes, both check
first. Where `origin` has no identity ref they publish one before the task refs,
so task refs on a remote imply an identity beside them — *unless* a server-side
policy accepts task refs and refuses `refs/workbook/project` in particular, or
the remote is read-only. Workbook then publishes the tasks anyway, because
nothing on that remote claims any project and refusing would take a working
repository away from someone who cannot change its policy; every publishing
command says so in the same breath, as a `workbook: warning:` line and an
`identity` member carrying `"unpublished": true` and the remote's own words.
Where `origin` holds a *different* project they refuse, and nothing is written.
The check costs nothing on the mutation path, which has already fetched and
compared the two refs in the same command.

`workbook setup` consults `origin` before minting, and asks for the identity ref
first. If origin publishes one, setup fetches and adopts it — no branch checkout,
no committed configuration needed anywhere. The older probe (a configuration
committed on origin's default branch) remains the fallback for projects that have
not published the ref yet. Setup reports which path ran, so joining an existing
project and creating a new one never look alike.

### Mixed versions and forks

Upgrade order does not matter. A v0.4.x client's fetch refspec and `ls-remote`
patterns name only `refs/workbook/tasks/*`, so publishing `refs/workbook/project`
is invisible to a teammate who has not upgraded, and the tracked document keeps
its identity fields for them to read. Nothing breaks until a project deliberately
upgrades its tracked document, and by then every clone should be on v0.5.0.

Forks copy branches, not Workbook refs — but a fork of a project that committed
`.workbook/config.json` copies that file, and its advisory identity is exactly
what the migration chain adopts. `workbook setup` in such a fork therefore
**inherits the upstream project's identity** and publishes it as the fork's own
canonical ref. Task history does not come with it: refs stay behind, so the fork
starts empty under an identity it shares with upstream.

Pick deliberately:

- **To carry the tracker over**, push the refs — history and identity together:

  ```sh
  git push fork 'refs/workbook/*:refs/workbook/*'
  ```

- **To start a fresh, independent project in the fork**, remove the inherited
  advisory copy before bootstrapping, so nothing is left to adopt:

  ```sh
  git rm .workbook/config.json && workbook setup --no-sync --key WB
  ```

  Setup then mints a new project ID and publishes a new identity ref. A fork
  whose `origin` still resolves to the upstream repository joins upstream
  instead, whatever its files say — the published ref wins.

Because the identity ref rides the same `git fetch` as the task refs, none of
this costs an extra network round trip. `workbook sync` reports what the identity
stage did under an `identity` member — `created`, `published`, `adopted`, or
`mismatched` — and omits the member entirely when this clone and `origin` already
agree and there is nothing else to note. The remaining status, `matched`, appears
only alongside such a note: refs `origin` holds under the identity ref's name,
which this version skips and lists under `ignoredRefs` rather than refusing to
run. A `mismatched` identity stops the run before any task is fetched. A
`moved` identity appears only when `--remote` names a remote marked as the
place the tasks moved away from: the run reads it and publishes nothing there;
see [The moved marker](#the-moved-marker).

