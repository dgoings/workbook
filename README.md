# Workbook

Workbook is a lightweight, repository-native project tracker for humans and coding agents.

The goal is to keep structured project state close to the code without requiring
a hosted issue tracker. The current CLI stores task operations durably in Git
objects and refs and explicitly synchronizes them through `origin`. A disposable
SQLite materialized view accelerates normal task reads while Git remains canonical.

> **Status:** initial collaborative POC. Repository initialization, local task
> CRUD, task ordering and dependencies, terminal and web boards, web
> drag-and-drop status changes, explicit origin-only task fetch/push/sync, and a
> disposable SQLite task projection are implemented, along with clone bootstrap
> through `workbook setup` and managed agent documentation through `workbook
> docs`. Conflict reconciliation remains proposed. Workbook is published for
> macOS and Linux through the `dgoings/homebrew-tap` Homebrew tap.

## Why Workbook?

`TODO.md` files travel with a repository and are easy for agents to read, but they lack structure, validation, dependency modeling, useful queries, and safe concurrent updates. Hosted trackers provide that rigor, but introduce another account, API, network dependency, and source of context that can drift away from the code.

Workbook aims for a middle ground:

- project state travels through the same Git remote as the code;
- developers can clone a repository and bootstrap with one command;
- agents can discover, claim, and update work through a small CLI with JSON output;
- task history is append-only, inspectable, and mergeable;
- no hosted service or MCP server is required for the default workflow;
- SQLite provides efficient local task reads and filtering without becoming a
  second source of truth.

## Target workflows

### Solo development

A developer keeps task state in the repository's Git object database and queries it locally. A remote is optional, although pushing Workbook refs provides backup and portability across clones.

### Small-team workflow

Team members explicitly share task refs through the repository's `origin` remote.
Workbook fetches only its private task namespace, validates current task tips
and their safe ancestry relationships in isolated tracking refs, and leaves the
checked-out code branch untouched. Exhaustive checkpoint replay belongs to the
separate validation audit rather than ordinary synchronization.

After cloning the repository, bootstrap Workbook with one command. It creates or
validates project identity, installs managed agent documentation, and exchanges
shared task refs with `origin`:

```sh
git clone <repository>
cd <repository>
workbook setup
```

Task changes synchronize themselves. A command that creates or updates a task
fetches shared task refs from `origin`, applies its change to the refreshed tip,
and publishes the single ref it changed. `workbook next` fetches before
answering so two agents do not claim the same task. A repository with no
`origin` is a local-only project and synchronizes nothing.

Turn it off for one command with `--no-sync`, for one project with
`workbook config set auto-sync false`, or for every project with
`"autoSync": false` in the `preferences` block of the user configuration.
`workbook config show` reports the resolved policy and which layer decided it,
and `workbook config unset auto-sync` returns the project to the user setting. A tracked project policy outranks
a personal preference, so a team can require synchronization in a repository;
`--no-sync` always wins over both.

An unreachable `origin` is a warning, not a failure: the change is recorded
locally and the command still succeeds. Local work that `origin` does not have
is replayed onto the fetched tip and published, so a task whose history diverged
needs no separate reconciliation step. The three concurrent situations Workbook
will not decide are reported instead, and exit `8`; see
[Reconciling divergent histories](#reconciling-divergent-histories).

`workbook fetch`, `workbook push`, and `workbook sync` remain available for
explicit whole-project synchronization, and teams that want publication tied to
ordinary code pushes can still opt in per clone with `workbook hooks install`.

Recording a project policy requires a version 2 project configuration. A
repository created by an earlier Workbook still has version 1, which keeps
working as it stands; `workbook setup` upgrades it and reports that it did.
Commit the result, and note that Workbook versions older than the upgrade
cannot read a version 2 configuration.

### Proposed ephemeral coding-agent workflow

A future ephemeral-agent workflow would clone the repository, bootstrap Workbook,
claim a task using a remote compare-and-swap, read the relevant context, implement
the work, and record the resulting commit before the environment is discarded.

The proposed command flow is:

```sh
git clone <repository>
cd <repository>
workbook setup

workbook claim TASK-123 --remote-required --json
workbook show TASK-123 --json

# Implement and commit the work.

workbook finish TASK-123 --commit HEAD --push --json
```

## Installation

Workbook is published for macOS and Linux, on both arm64 and amd64, through a
Homebrew tap:

```sh
brew install dgoings/tap/workbook
```

Workbook generates per-project agent documentation, so an upgrade cannot refresh
every checkout on its own. After installing or upgrading, run `workbook setup` in
each project that uses Workbook, and `workbook docs status` to check whether a
project is current.

### Building from source

Building from source requires Go 1.26 or newer and Git on `PATH`:

```sh
./scripts/install.sh [destination] [name]
```

The destination defaults to `$HOME/.local/bin` and is created when needed. The
name defaults to `workbook`; pass another to keep a source build beside a
released install rather than shadowing it:

```sh
./scripts/install.sh ~/.local/bin workbook-dev
```

The script prints the installed path and, when necessary, the `PATH` export
needed to run it.

Source builds are stamped from `git describe`, so `workbook version` reports the
commit they came from rather than `dev (unknown)`. A source build reports a
leading `v`, for example `v0.2.0-3-g86281c9`, and gains a `-dirty` suffix when
built from a modified tree. A released artifact reports a bare `0.2.0`, so the
two are always distinguishable. Stamping also lets a source build satisfy the
benchmark harness, which rejects an unknown commit.

### Setting up a development environment

Working on Workbook with Workbook needs a build that survives a broken working
tree. `scripts/setup-dev-env.sh` installs both builds into separate directories
under separate names, so neither can shadow or overwrite the other:

```sh
./scripts/setup-dev-env.sh
```

| Build | Name | Default location | Source |
| --- | --- | --- | --- |
| published | `workbook` | Homebrew prefix, or `$HOME/.local/share/workbook/stable/bin` | the tap, or the newest release tag |
| working tree | `workbook-dev` | `$HOME/.local/share/workbook/dev/bin` | the current checkout |

The published build comes from `brew install dgoings/tap/workbook` wherever
Homebrew is installed. Without Homebrew the script builds the newest release tag
instead, in a detached worktree that leaves the checkout untouched. Both routes
produce a `workbook` to fall back on when `workbook-dev` breaks; a source-built
fallback reports a leading `v`, as any source build does.

The script adds both directories to the detected shell profiles inside a marked
block that later runs replace rather than duplicate, and prints the `PATH`
export needed by the current shell. It ends by reporting the resolved path and
reported version of each build.

Useful options:

```sh
./scripts/setup-dev-env.sh --dev-only                  # rebuild the working tree alone
./scripts/setup-dev-env.sh --stable-method source      # skip Homebrew entirely
./scripts/setup-dev-env.sh --stable-version v0.2.0     # pin the fallback release
./scripts/setup-dev-env.sh --no-profile                # leave shell profiles alone
```

`WORKBOOK_STABLE_PREFIX`, `WORKBOOK_DEV_PREFIX`, and `WORKBOOK_SETUP_PROFILE`
override the install prefixes and the profile that is updated. Run
`workbook-dev setup` afterwards to bootstrap the clone.

#### Remote agent sessions

`.claude/hooks/session-start.sh` runs the same setup for Claude Code on the web.
It is registered as a `SessionStart` hook in `.claude/settings.json` and does
nothing outside a remote session, so local checkouts keep their own shell
profile and install locations. In a remote session it warms the Go module and
build caches, installs `workbook-dev` and then `workbook`, adds both install
directories to `PATH` for the session, and runs `workbook-dev setup` to
bootstrap the clone. The working-tree build is installed first, and a failure to
build the published release is reported rather than fatal, so a session always
ends up with a CLI.

Use help to discover commands and their options:

```text
workbook --help
workbook create --help
workbook help create
workbook version
```

Help output is human-readable. Help itself has no JSON form.

## Implemented POC commands

The current CLI implements these local commands. Commands marked `--json` support
both human-readable output and a versioned machine-readable result envelope:

```text
workbook setup [--key <key>] [--no-docs] [--no-sync] [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook create
workbook list
workbook board [--wide | --narrow] [--json]
workbook show <task> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--json]
workbook update
workbook delete
workbook restore
workbook move <task> (--before <task> | --after <task>) [--json]
workbook depend <task> <dependency> [--json]
workbook free <task> <dependency> [--json]
workbook next [--json]
workbook rebuild [--json]
workbook validate [--full] [--json]
workbook version [--json]
workbook fetch [--json]
workbook push [--json]
workbook sync [--watch [--interval <duration>]] [--status] [--json]
workbook config show [--json]
workbook config set <setting> <value> [--json]
workbook config unset <setting> [--json]
workbook docs install [--create <file>] [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook docs update [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook docs status [--skill-dir <dir>] [--no-skill] [--json]
workbook docs remove [--skill-dir <dir>] [--no-skill] [--force] [--json]
workbook hooks install [--json]
workbook serve [--addr 127.0.0.1:7331]
workbook help [command]
```

`workbook setup` is the single bootstrap path for a fresh clone. It creates or
validates the tracked `.workbook/config.json` holding the project ID and key,
writes the user-global configuration file when it is missing, installs or
refreshes managed agent documentation, and synchronizes shared task refs with
`origin`. It skips synchronization when no `origin` remote is configured, so a
solo local project needs no remote. Use `--no-sync` to bootstrap without
exchanging refs and `--no-docs` to create project identity alone.

The Workbook skill is installed under the directory named by the user-global
`skillDir` setting, `.claude/skills` by default. Because that setting applies to
every project on a machine, `--skill-dir <dir>` overrides it for one project and
`--no-skill` leaves the skill alone while still managing the guidelines. These
flags are a stopgap until per-project configuration lands.

### User-global configuration

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
on its own, so listing a file here is safe. `preferences` is reserved for future
settings and is deliberately untyped, so adding one later needs no format
version bump.

Project identity and task data stay in the repository. Nothing in this file
affects what Workbook records.
Create, update, delete, and restore append immutable task commits under
`refs/workbook/tasks/`; delete records a tombstone instead of removing the ref.
Creation and ordinary updates write descriptive task-operation commit subjects
suitable for `git log`, while canonical data remains in the operation and state
blobs.
Tombstoned tasks reject every mutation except `workbook restore`, which records
an explicit append-only restore operation.
List and show read the current task checkpoint from each task ref's tip. Task
statuses follow this canonical order: Backlog, Ready, Blocked, In Progress, In
Review, and Done. `move` orders a task inside its status-and-priority bucket with
an exact rational rank; it changes only that task. `depend` adds a prerequisite
edge and rejects cycles; `free` removes one prerequisite edge and is idempotent.
`next` chooses the first Ready task whose dependencies are all active and Done,
sorting by priority, rank, and task ID; it reports no eligible task when none
qualify. `board` uses the same core task order and presents an actionable,
unambiguous task-ID prefix with each card's priority, title, and labels. Its JSON
output retains full task IDs, descriptions, and the rest of the task data. Normal
`list`, `show`, `board`, and `next` reads use the local SQLite projection.
Claims and implementation links remain future work.

### Task change history

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
not permanent: Workbook keeps at most three parked tips per task and retires the
excess inside a later mutation's ref transaction, after which the oldest
pre-replay chain is collectable. A named object that no longer resolves is an
ordinary not-found for that argument, exit `4`, naming the commit rather than
reporting corruption.

Both views are served from the SQLite projection's `operations` table, which
stores operations alone and reconstructs any state by replaying from the root;
`--compare` replays both endpoints in full. Where the projection holds no
operations for a task, or for a commit it does not hold — every parked
pre-replay tip, since the projection is fed from `refs/workbook/tasks/` only —
Workbook falls back to a bounded Git read so `show` still answers.

### Local mutation durability

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
from the canonical Git refs when recovery is needed and reports its task count
and cache path.

### Releasing

Workbook is developed on a trunk. Features merge to `main` continuously, and a
release is a periodic version bump that gathers whatever has landed since the
last tag. Merging does not publish anything, so work can accumulate on `main`
until a group of it is worth releasing.

Cutting a release is one command:

```sh
./scripts/cut-release.sh 0.3.0
```

It refuses to publish anything until the release is one that can be reproduced:
the version is strict `MAJOR.MINOR.PATCH` and orders after the latest release,
`HEAD` is on `main` with nothing uncommitted, `main` matches the remote, and the
tag is unused both locally and on the remote. It then runs `go test ./...`,
creates the annotated tag, and pushes only that tag. Pushing the tag is what
starts the release; nothing else needs to be run by hand.

Check a release without publishing it with `--dry-run`, which runs every check
and stops before tagging:

```sh
./scripts/cut-release.sh 0.3.0 --dry-run
```

`--skip-tests` skips the test run, and `--remote` and `--branch` override the
`origin` and `main` defaults.

#### What the workflow publishes

Pushing a version tag such as `v0.1.0` runs the release workflow. It revalidates
the strict SemVer tag, publishes the four archives and checksums to GitHub
Releases, and updates the `dgoings/homebrew-tap` formula from those generated
checksums. The protected release environment exposes a credential scoped only to
that tap repository after validation. New assets are staged in a draft, the tap
update is pushed first, and the draft is published last. A rerun verifies
existing assets byte-for-byte and never overwrites them; a failed final
publication reverts the tap update and removes only a draft created by that run.

This source repository intentionally does not track an installable
`Formula/workbook.rb` with placeholder checksums. The workflow renders the real
formula directly into the tap from the built artifacts.

#### Release artifacts

`scripts/release.sh <version> <output-dir>` creates macOS and Linux archives for
Apple Silicon and Intel plus a sorted `checksums.txt` file. The four archives
match the platform blocks in the published Homebrew formula, which serves
`darwin` and `linux` on `arm64` and `amd64`. Each archive contains only
the `workbook` executable. The script cross-compiles with the requested version
and the current Git commit injected into `workbook version`; source builds
report `dev` and `unknown` instead. Release versions must use the exact
`MAJOR.MINOR.PATCH` form without leading zeroes.

The rendered formula declares no `version`. Homebrew derives one from the URL of
whichever platform block it selects, by matching
`github.com/.+/releases/download/v<version>/`, and a `version` that agrees with
what Homebrew already scans fails `brew audit` as redundant. Both the host and
the release-tag segment are part of that rule, so the download URLs are what
make the published version correct: served from another host, or without the
tag segment, Homebrew falls back to filename heuristics that read
`workbook_0.3.0_linux_amd64.tar.gz` as version `64`. Keep the version in the
release-tag path when changing where archives are published.

### Explicit task sharing

`workbook fetch` downloads only `refs/workbook/tasks/*` from `origin` into
`refs/workbook/remotes/origin/tasks/*`. It validates the current tracking and
canonical tips' IDs, documents, and safe ancestry relationship before touching
the corresponding local task ref. A missing local task is created, and a behind
local task is fast-forwarded in one compare-and-swap transaction. Local-ahead
tasks are left alone; a divergent task has its local-only operations replayed
onto the fetched tip in the same transaction. Invalid fetched data remains
isolated and causes a nonzero exit; valid unrelated tracking refs can still
reconcile in that run. Stale refs are pruned only from Workbook's
isolated tracking namespace, allowing `sync` to republish an intact canonical
task ref if its remote counterpart was removed externally.

`workbook push` publishes validated local `refs/workbook/tasks/*` refs to
`origin` without force or deletion. One bounded, non-atomic publication retains
per-ref outcomes, so an unrelated task can publish even when another task is
rejected as non-fast-forward. Invalid local tips are omitted. The command
reports every outcome and exits nonzero if any ref is invalid, rejected, or
changes during publication.

`workbook hooks install` opts the clone into automatic task publication during
an ordinary `git push origin`. The managed pre-push hook is recursion-safe and
blocks the code push when Workbook task publication fails. Installation is
idempotent for Workbook-managed hooks. An existing non-Workbook pre-push hook is
never overwritten; Workbook instead prints manual chaining guidance. Hooks are
optional convenience only and are not required for correctness.

The collaborative POC supports only the remote named `origin`. Multiple named
remotes remain future work.

`workbook sync` runs the full sequence against `origin`: fetch Workbook task
refs into the isolated tracking namespace, validate and fast-forward/create
compatible local task refs, replay any divergent local history onto the fetched
tip, then publish the resulting local tips. Reconciliation is per task and
publication covers every canonical tip, so one task that needs a decision leaves
every other task fetched, replayed, and published, and publishes its own
replayed prefix too. The command does not replay every buried
checkpoint during ordinary synchronization; that is reserved for the explicit
`workbook validate` audit. The command never fetches or pushes code branches and
does not create a hidden tasks branch.

### Continuous synchronization

`workbook sync --watch` runs the same sequence on a loop in the foreground,
default every five seconds, until it is interrupted. It is an optimization and
never a requirement: every command works unchanged with none running.

While a watcher is running, a mutation writes locally, asks the watcher to
publish, and returns, reporting a `sync` status of `deferred` rather than the
usual `completed`. That removes both network round trips from the command's
critical path — roughly 500 ms and 16 Git processes per mutation, measured in
[`docs/performance/`](docs/performance/). Publication follows within
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

`workbook sync --status` reports whether a watcher is running, what it last did,
and any conflicts it is holding. `workbook serve` runs the same loop, so the
board reflects other clones' work without anyone running a command; an external
watcher already running keeps ownership and the board runs no second loop.

Reconciliation is what makes this safe. A mutation applied to a tip a few
seconds stale is no longer a case worth a network round trip to prevent; it is
one the fetch path already handles.

### Reconciling divergent histories

When `origin` holds operations this clone does not, and this clone holds
operations `origin` does not, the fetch replays the local ones onto the fetched
tip. The canonical task ref moves to the last replayed commit, the orphaned
local tip is parked at `refs/workbook/reconciled/<task-id>/<n>` in the same
compare-and-swap transaction, and every replayed pack keeps its actor, wall
time, and operation IDs, rewriting only its logical clock. Replayed commits have
exactly one parent; Workbook writes no merge commits and never rebases or
force-pushes a shared ref.

Most concurrent edits touch different fields, commute, and replay silently, so a
mutation that loses a push race and a fetch that finds week-old local work both
republish without asking anything. A replayed operation whose value `origin`
already holds records no commit at all. Every other concurrent field change is
last-syncer-wins.

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
discard flow, and no conflict state kept between invocations. The contract is
complete without a terminal, so JSON output, hooks, agents, and the web board
all consume the same list.

Parked refs stay local; they are never pushed, because every enumeration that
builds a push refspec is scoped to `refs/workbook/tasks/`. The next successful
mutation of a task retires that task's oldest parked refs inside the same
`update-ref` transaction, keeping the most recent few. Fetches and reads never
prune them: a fetch must not delete recoverable work in the command that
orphaned it.

### Semantic history validation

Normal `list`, `show`, `fetch`, `push`, and `sync` use bounded current-tip
checks. To audit semantic history, run the foreground command:

```sh
workbook validate [--full] [--json]
```

`validate` reads canonical task histories and verifies every stored checkpoint
against the operation sequence that produced it. It stores resumable,
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

The following acceptance scenarios are **planned targets**, not recorded
performance evidence: with 500 total task refs (475 active and 25 tombstoned)
and 20 operations per task, a full audit targets at most 10 seconds; an
unchanged cached audit targets at most 500 milliseconds; and five one-operation
changes target at most 1 second. Each scenario also targets fewer than 12 Git
processes.

### Terminal board

`workbook board` automatically chooses its six-column wide layout on an
interactive terminal at least 140 columns wide. It uses vertically stacked status
sections for narrow or noninteractive output; `--wide` and `--narrow` force the
respective layouts. The task-ID prefixes in human output are accepted anywhere a
task ID is accepted.

### Local web board

`workbook serve` starts a foreground, loopback-only board at
`http://127.0.0.1:7331` by default. In fish, run it in the foreground with:

```fish
workbook serve
```

Or run it in the background with:

```fish
workbook serve &
```

The embedded page and its API expose these routes:

```text
GET /                         board HTML
GET /tasks/new                new-task shell; client-rendered form
GET /tasks/<id>               linkable task-detail shell; client-rendered form
GET /deleted                  deleted-task shell; client-rendered list
GET /api/tasks                versioned task JSON
GET /api/tasks/<id>/history   versioned change log and status lifecycle JSON
POST /api/tasks               create a task
PATCH /api/tasks/<id>         update task fields
PATCH /api/tasks/<id>/status  drag-and-drop status changes
PATCH /api/tasks/<id>/position  atomically change status and board position
DELETE /api/tasks/<id>        tombstone a task
POST /api/tasks/<id>/restore  restore a tombstoned task
PUT /api/tasks/<id>/dependencies/<dependency>     add a prerequisite
DELETE /api/tasks/<id>/dependencies/<dependency>  remove a prerequisite
GET /healthz                  versioned health JSON
```

Drag a task card within a column to reorder it or into another canonical status
column to change status and position together. Workbook keeps the task's priority
unchanged and clamps drops outside that priority group to the nearest group
boundary, so dropping at the top or bottom of a column still has a clear result.
The placement creates one normal Workbook operation commit on the moved task and
returns a versioned JSON task-mutation document. The older status-only endpoint
remains available for compatible clients.

The executable embeds its HTML, CSS, and JavaScript, and the page polls
`/api/tasks` every second. The six canonical columns share the available width
on large screens, scroll horizontally on narrow screens, and keep dense task
lists vertically scrollable within the viewport. Web cards show the actionable
task-ID prefix, priority, title, up to six lines of an optional description, and
labels; each title links to its full-ID task-detail URL, where the complete
description remains available. Every status column has a New Task link that
preselects that column's canonical status.

Cards with prerequisites show completed versus total dependency progress.
Ready cards whose prerequisites are not all active and Done also say
`Waiting on dependencies`. Task detail pages derive two views from the same
directed edge: **Depends On** lists the current task's prerequisites, while
**Blocks** lists tasks that depend on the current task. Each group searches
eligible active tasks through an integrated combobox and uses the nested
Git-durable mutation routes above.

Task forms use a wide main column and a compact Properties sidebar for status,
priority, labels, Depends On, and Blocks. New Task stages both Depends On and
Blocks without writing task refs; relationship mutations run after the task
receives its durable ID. If only some edges succeed, successful relationships
remain durable while failed relationships remain available to retry or remove.
On narrow screens, the task editor, Properties, Relationships, and actions
stack in that order.

Missing prerequisite IDs remain visible and removable. Tombstoned
prerequisites are also removable because the active dependent owns that edge;
deleted blocked tasks remain read-only because tombstones cannot be changed.
Dependency warnings and failures stay beside the initiating group, and
dependency refreshes leave unsaved task-form fields mounted.

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
priority, and labels through the versioned APIs. Saving returns to the board and
refreshes it. A failed save leaves the entered values in place and shows the
server error in the form; Back returns to the board without mutating a task.
Active task details also provide Delete; successful deletion opens `/deleted`.
That route lists tombstoned tasks and restores a selected task through the
explicit restore operation.

The web experience is still local-first and intentionally narrow in scope.
Authentication, hosted deployment, browser deletion, draft persistence, and
broader collaboration remain future work. A request using the wrong method for
a known route receives `405` with the route's allowed method.

Development performance is measured with the reproducible, bounded harness
documented in [`docs/performance/README.md`](docs/performance/README.md).
Remote synchronization benchmarks select one or more of seven named topologies
with repeatable `--scenario` flags and use at least 500 total tasks with 20
operations per task. In the benchmark CLI, `--tasks` counts total refs; omitted
`--tombstones` produces 25 tombstones at 500 or more total tasks and one in
smaller diagnostics, while an explicit zero is diagnostic-only. Cold CLI
rebuilds and warm HTTP task-list loads are untimed setup. Local CLI p95 targets
are 200 ms, warm-update p95 is 100 ms, and every burst must be below 1 second;
local scenarios have no Git-process target. Version-2 reports include the
SHA-256 of the resolved measured binary and acceptance rejects an `unknown`
measured commit before it builds a fixture. Reports evaluate each topology as
`pass`, `miss`, `timeout`, or `failed` against a time and Git-process reference
budget; `not-evaluated` means that scenario has no target. Baseline budgets and
outcomes are evidence, not achieved-performance guarantees; in particular, a
timeout is only lower-bound elapsed-time evidence.

### Project identity across worktrees

`.workbook/config.json` remains the portable tracked configuration that carries a
Workbook project's identity across clones. Workbook also records that identity in
a private guard at `<git-common-dir>/workbook/project.json`. The guard is private
coordination metadata shared by every worktree attached to the same common Git
directory; it does not replace the tracked configuration or travel with a clone.

The current POC permits one Workbook project per common Git repository, including
all linked worktrees. The first successful configuration load for each opened
repository compares the portable configuration with the common guard, then caches
that validated configuration for the repository session. Reopening the repository
observes later tracked or guard changes and rejects use when the tracked and common
identities do not match. For repositories initialized before the guard was
introduced, the first configuration load or repeated `workbook setup` atomically
backfills the missing guard from `.workbook/config.json`. Concurrent first users
must either publish that same identity or observe and validate the identity another
user published.

## Proposed post-POC commands

The following examples describe future coordination and are not implemented:

```sh
workbook claim TASK-123 --remote-required --json
```

Remote compare-and-swap claims, automatic conflict reconciliation, multiple
remote selection, and a combined `workbook finish --commit HEAD --push` flow
remain design proposals.

## Architecture

Workbook separates its data model, transport, and query engine:

```mermaid
flowchart TD
    CLI["CLI / IDE / agent"] --> Core["Workbook core"]
    Core --> Ops["Immutable task operations + tip checkpoints"]
    Ops --> Refs["Tool-private Git refs"]
    Refs --> Remote["Explicit origin synchronization"]
    Ops --> SQLite["Disposable SQLite projection"]
```

| Layer | Responsibility |
| --- | --- |
| Operation model | Defines task history, current tip checkpoints, causality, and validation |
| Git refs and objects | Store task operations durably and explicitly synchronize task refs with `origin` |
| SQLite projection | Materializes current task state and recorded operations for local reads; Git remains canonical |
| CLI/core library | Currently owns initialization, local validation, CRUD, and user-facing output |
| Optional adapters (proposed) | IDE integrations, local API, MCP, or a coordination relay |

Workbook adds only tracked bootstrap configuration to the working tree. Task state
does not live on the currently checked-out code branch.

## Git storage model

Each task owns a custom Git ref:

```text
refs/workbook/tasks/TASK-123
refs/workbook/tasks/TASK-456
```

The ref points to a Git **commit object**. That commit is the current head of the task's operation DAG:

```text
ref
 └── commit
      ├── parent commit(s)
      └── tree
           ├── operation.json blob
           └── state.json blob
```

- The root commit has no parents and contains `task.create`.
- A normal edit commit has one parent.
- A future reconciliation or resolution commit can have multiple parents.
- Immutable `operation.json` packs are the authoritative record of task intent,
  and Git parent ancestry is the authoritative record of causality.
- Every current POC commit tree contains exactly the edit's versioned operation
  pack in `operation.json` and a deterministic, durable task materialization in
  `state.json`.
- The durable model requires each `state.json` checkpoint to match the result of
  applying that commit's operation pack to its parent state, or to no parent
  state for a root commit.
- Ordinary `list`, `show`, `fetch`, and `sync` validate and use current tip
  checkpoints without replaying the complete history. This bounded default does
  not change the authority of immutable operations or Git ancestry.
- Reading a task's operation history — for the projection's `operations` table
  and for `workbook show --history` — reads the operation packs alone and does
  not revalidate their documents, tree shape, or stored checkpoints. The write
  path already prevents this clone from recording an invalid commit, and setup,
  sync, and fetch validate what arrives from elsewhere, so revalidating a whole
  history on every read buys nothing and makes a read scale with history depth.
  An unreadable commit truncates the read softly: the valid prefix is returned
  and the boundary commit is named.
- `workbook validate` explicitly replays history and reconstructs checkpoints;
  it is not part of ordinary current-tip reads or synchronization, and it
  remains the path that audits stored checkpoints.

Using one ref per task avoids a single global state-branch bottleneck: local
commands working on different tasks update different refs. Future concurrent edits
to the same task can form branches in that task's operation DAG and will require
the proposed domain reconciliation rules.

### Operation pack

Each task mutation creates one versioned operation pack. A pack can hold multiple
operations that should be applied atomically:

```json
{
  "format": "workbook.operation-pack",
  "version": 1,
  "projectId": "01K0M65GBZ8F5ZQX0VC1J8H3TP",
  "taskId": "WB-01K0M6B8A4FTT8C39MXXYTW7C1",
  "historyGeneration": "01K0M6B8A4FTT8C39MXXYTW7C2",
  "actor": {
    "id": "agent-7"
  },
  "logicalClock": 2,
  "wallTime": "2026-07-22T14:35:00-04:00",
  "operations": [
    {
      "id": "01K0M6B8A4FTT8C39MXXYTW7C3",
      "type": "field.set",
      "field": "status",
      "value": "ready"
    }
  ]
}
```

Operation IDs are globally unique and independent of Git object IDs. Git commit ancestry provides causal ordering; a logical clock assists validation and deterministic presentation. Wall-clock time is for display only and must not decide semantic conflicts.

The current POC format supports task creation, scalar and label updates, and
tombstones. Later CLI and format work is expected to add:

- dependency commands using the existing `set.add` and `set.remove` operation
  semantics;
- `comment.add`;
- `claim.acquire`, `claim.release`, and heartbeat/lease operations;
- `implementation.link` for associating work with code commits.

Historical operation commits are immutable. Tasks are tombstoned rather than
deleting their refs. Shared task histories are never rebased or force-pushed. A
clone that diverged from `origin` replays its own unpublished operations onto
the fetched tip and parks the tip it replaced, which changes only local refs and
appends to the shared history.

## Concurrency and synchronization

Explicit synchronization, a combined fetch-then-push sync command, and
replay-based reconciliation of divergent task histories are implemented. The
model is:

1. Fetch the relevant Workbook ref.
2. Replay any local-only operation packs onto the fetched tip.
3. Validate the requested transition against the projected task.
4. Write an operation pack and Git commit.
5. Atomically advance the local task ref.
6. Push that task ref.
7. If the remote changed concurrently, the next fetch replays onto its new tip.

Most operations converge because they touch different fields. Meaningful
contradictions are surfaced rather than silently hidden, through the three
conflicts described in
[Reconciling divergent histories](#reconciling-divergent-histories). Concurrent
`done` and `blocked` status changes are not among them: status is
last-syncer-wins, and multi-value status conflicts remain proposed.

Exclusive claims are different. When `--remote-required` is used, a claim succeeds only after the remote accepts a fast-forward update based on the task ref that was fetched. If another agent claimed the task first, Workbook refetches and reports that the claim failed instead of merging two exclusive claims. Offline claims, if supported, must be visibly tentative.

Workbook does not rely on Git hooks for correctness. The optional managed
pre-push hook publishes task refs as a convenience, but fresh clones, alternate
Git clients, and remote PR merges cannot be assumed to execute it.

## Relationship to code branches

Implementation links and landed-state reporting are proposed; the current POC
does not implement them. The intended model keeps workflow state and code
availability as separate facts.

A task operation can record an implementation commit, while Workbook computes whether that implementation is reachable from the current branch or the configured target branch. This supports states such as:

- implemented on a feature branch but not landed;
- landed on `main`;
- done globally but absent from the currently checked-out historical branch.

Commit trailers provide a stable link across squash merges and cherry-picks:

```text
Task: TASK-123
```

Facts Git can derive, such as whether work has landed on `main`, should not be duplicated as mutable tracker state.

## SQLite projection

SQLite is a local, disposable read cache, never the canonical source of truth.
The shared cache is stored at `<git-common-dir>/workbook/cache.sqlite`, so linked
worktrees use the same projection. It materializes current task state, including
labels and dependencies; direct SQL writes and arbitrary SQL query support are not
part of the Workbook interface.

Before normal task reads (`list`, `show`, `board`, `next`, and web `GET
/api/tasks`), Workbook validates the cache against current Git task heads and
refreshes changed checkpoints. Create, update, and other mutations continue to
write Git operations directly. Run `workbook rebuild` to explicitly recreate the
cache; it builds a temporary database, checks task heads again, retries once if
they changed during the build, and atomically installs a stable result. With
`--json`, the normal result envelope contains `taskCount` and `cachePath`.

An `operations` table alongside the task tables materializes each task's recorded
operations for `workbook show --history`, `--compare`, and web
`GET /api/tasks/<id>/history`. Its rows are keyed on
the operation ULID rather than the commit object ID, because replay preserves
operation ULIDs while rewriting logical clocks and therefore every downstream
object ID; a surviving row changes only its ordering and object-ID columns. Rows
hold operations alone, with no per-row state checkpoint: any state is
reconstructed by replaying from the root.

Because comparing tips is complete for current state but blind to intermediate
commits — a fetch advancing a task twelve commits yields one new tip and eleven
unread packs — refresh walks each changed task from the head the projection
already holds to its new one, and a rebuild reprojects full histories. A new head
that does not descend from the projected one is the reconciliation signal: replay
drops operations three ways, so upserting alone would strand rows and break the
logical-clock chain a root-first replay depends on. Workbook deletes that task's
operation rows and reprojects the returned chain instead. Refresh and rebuild
therefore scale with operation count rather than task count; see the
[projection refresh evidence](docs/performance/README.md#projection-refresh-change-count-family).

The cache can be deleted at any time and rebuilt entirely from Workbook refs.
Semantic validation results live in a separate `validation.sqlite` beside it, so
a validator-version bump does not force a projection rebuild and a long
validation pass does not hold the projection's writer lock against mutations.

## Bootstrap and portability

A normal Git clone does not fetch arbitrary custom ref namespaces, so bootstrap
stays explicit. Installing Workbook is a separate step: `brew install
dgoings/tap/workbook`, or `./scripts/install.sh` to build from source.

`workbook setup` then performs the repository half of the bootstrap:

1. detect the repository and validate Git identity;
2. create or validate project configuration and the private common-directory guard;
3. write the user-global configuration file when it is missing;
4. install or refresh managed agent documentation and the project-local Workbook skill;
5. explicitly fetch and publish `refs/workbook/tasks/*` through `origin`, or report
   that synchronization was skipped when no remote is configured;
6. report the resulting task count.

Setup deliberately does not install Git hooks. Hooks remain opt-in through
`workbook hooks install`, because they must never be required for correctness.

The default shared backend uses custom refs because they avoid branch switching and per-repository head contention. A conventional `workbook/state` branch may be offered as a compatibility fallback for Git hosts that reject custom refs. A future optional relay may provide strict leases, heartbeats, notifications, or high-concurrency agent coordination while retaining the same operation model.

## Design principles

- **Repository-native:** use the existing Git repository and remote.
- **Local-first:** remain useful offline; synchronize explicitly and transparently.
- **CLI-first:** every capability is available without a GUI or long-running server. A watcher makes synchronization cheaper; it never becomes required.
- **Agent-friendly:** stable semantic commands and machine-readable output minimize context and token use.
- **Human-inspectable:** operation payloads use versioned, inspectable formats.
- **Derived when possible:** do not duplicate facts already represented by Git.
- **No hidden rewrites:** shared history is append-only and never force-pushed.
- **Progressive complexity:** no service is required for the default workflow; stronger coordination can be optional.

## Non-goals

At least initially, Workbook is not intended to provide:

- a hosted Jira or Linear replacement;
- organization-wide portfolio planning across many repositories;
- real-time collaborative rich-text editing;
- strict distributed leases without an online coordination authority;
- a required MCP server or IDE integration;
- field-level permissions beyond the repository's Git access controls.

## POC roadmap

The POC now has versioned operation and state documents, local Git object/ref
CRUD, structured CLI output, repository initialization, terminal and web boards,
web drag-and-drop status changes, task ordering and dependencies, and explicit
origin-only task sharing including fetch, push, sync, and optional pre-push hook
installation. Remaining POC work is:

1. Complete replay, reconstruction, Git hash, renderer, HTTP, installer, and
   documentation acceptance coverage.

Remote claims, conflict reconciliation, multiple-remote support, packaged
distribution, and optional adapters follow this collaborative POC.

## Open questions

- Distribution format beyond source builds.
- Exact status workflow and customization model.
- Per-field CRDT and conflict-resolution semantics.
- Actor identity and optional operation signing.
- Attachment limits and storage policy.
- Snapshotting and compaction without rewriting shared history.
- Git-host compatibility and fallback behavior.
- Lease expiration and abandoned-agent recovery.
- Atomic publication of code refs and Workbook refs.

## Related work

- [git-bug](https://github.com/git-bug/git-bug) stores distributed issue operations in Git objects under per-entity refs.
- [Fossil](https://fossil-scm.org/) synchronizes ticket-change artifacts and reconstructs SQL tables as projections.
- [Beads](https://github.com/gastownhall/beads) explores dependency-aware task memory for coding agents.
- [Automerge](https://automerge.org/) and [Yjs](https://yjs.dev/) provide general-purpose CRDT models for local-first collaboration.

Workbook's intended distinction is a small, repository-adjacent tracker designed equally for humans, small development teams, and ephemeral coding agents.
