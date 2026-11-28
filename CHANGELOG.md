# Changelog

A release pull request adds an entry here before the release is cut, and the
bump label it carries has to agree with the version this file names. See
[Releasing](CONTRIBUTING.md#releasing) for how the two are checked against each
other.

Patch releases may be cut without an entry, so this file records the releases
worth describing rather than every release. Every release, described here or
not, has notes on the
[GitHub Releases page](https://github.com/dgoings/workbook/releases).

Releases through v0.4.1 predate this file.

An `## Unreleased` heading collects what has landed since the last release, so
work that needs prose can write it while the reasoning is fresh rather than
reconstruct it in the release pull request. Retitle that heading to the version
being cut — `## v0.6.0 — 2026-09-30` — in the pull request that carries the
bump label. The heading is deliberately not a version until then: the release check
reads the highest-versioned `## vX.Y.Z` heading, wherever it sits, and refuses a
label that disagrees with it, so an early version heading would block every
patch release until that version was cut. A patch to an older line is written
here on its `maint/vX.Y` branch and forward-ported to `main` afterwards, in
version order rather than at the top; a heading no newer than the previous
release counts as no entry, so it never blocks the next release. See
[Patch releases to an older line](CONTRIBUTING.md#patch-releases-to-an-older-line).

## Unreleased

Workbook has a second kind of record, the relic, for specs, plans, decisions
and similar material. A relic has a title, a type, properties and an ordered
list of parts, and a part has a type, fields and an optional body. Relics are
shared through the tasks remote as tasks are, can link to tasks, and are taught
to agents by the generated guidelines and the skill. The web board shows relics
and cannot change them. A relic can be retired, and `workbook compact --relics`
compacts retired and deleted relics. Upgrade
every Workbook on a machine before creating one; see Compatibility across
versions below.

### Added
- **Task description edits on different lines merge automatically.** When a
  synchronization replays a description edit onto another clone's edit and the
  two changed different lines, Workbook stores the combined text once, as a
  recorded `description.merge` operation, with a `description-merged` warning.
  Other clones read the stored text. Changes to the same or adjacent lines, and
  descriptions that cannot be merged safely, stay a `description` conflict,
  unchanged. `show --history` marks a merge, with a `merge` member in JSON. The
  generated guidelines and the skill say what an agent does; run `workbook docs
  update` to refresh them. See [Merged
  descriptions](docs/reference.md#merged-descriptions) and [the stored
  format](docs/architecture.md#description-merges).
- **Body edits on different lines merge automatically.** When two saves of one
  part's body started from the same version and changed different lines,
  Workbook stores the combined text once, as a recorded merge, in a sync or in
  `relic update --base <older version>`, with a `relic-body-merged` warning.
  Other clones read the stored text. Changes to the same or adjacent lines, and
  bodies that cannot be merged safely, stay a `body` conflict. `relic show
  --history`, `relic diff` and the board mark a merge. See [Merged
  bodies](docs/reference.md#merged-bodies) and [the stored
  format](docs/architecture.md#merged-bodies).
- **Relics on the web board.** `workbook serve`, and the desktop app that loads
  it, show a list of relics filtered by type, retired and deleted; a page for
  each relic with its parts, links both ways, history and diffs; and a
  **Relics** section on the page of a task that relics link to. All of it is read
  only, and whoever can open the board can read every relic, deleted and retired
  ones included, with older body texts and authors' email addresses. **Open in
  new tab** shows one body by itself under a sandbox content policy. The same
  data is at new `GET` routes under `/api/relics` and at
  `/api/tasks/<id>/relics`, as versioned JSON. See
  [Relics on the board](docs/reference.md#relics-on-the-board).
- **Built-in schemas for specs and plans.** Every project, new or upgraded, has
  `workbook:spec:1` and `workbook:plan:1` with no setup. A new `spec` or `plan`
  relic names the built-in and is checked against it: both require a `status`
  property, and every part needs a `title`. A project that saves its own schema
  for a type takes over for new relics, and relics already on the built-in keep
  it. `schema list` and `schema show` show built-ins, with `builtIn` in JSON, and
  `relic migrate --to` takes a built-in's name. Existing relics stay open until
  `relic migrate <relic> --to spec` adopts one. See
  [Built-in schemas](docs/reference.md#built-in-schemas).
- **Relationship names.** Workbook knows `implemented-by`, `derived-from`,
  `summarizes`, `decided-in`, `changes`, `supersedes` and `relates-to`, and a
  schema's `links.relationships` adds names. The generated guidelines list them
  and show how to write a spec and a plan. Run `workbook docs update` to
  refresh them. See
  [Relationship names](docs/reference.md#relationship-names).
- **Properties when creating a relic.** `relic create --set <name>=<value>` and
  `--set-json <name>=<json>` set properties, as on `relic update`; they cannot be
  combined with `--from-file`.
- **Retiring a relic.** `workbook relic retire <relic>` and `relic unretire`
  record a reversible mark. `relic list --retired` and `--all` show retired
  relics, and `workbook show <task>` lists the links of a retired relic marked
  `(retired)`. A retired relic can still be edited and linked. See
  [Retiring a relic](docs/reference.md#retiring-a-relic).
- **Compacting relics.** `workbook compact --relics [--retired-only |
  --deleted-only]` replaces the history of retired and deleted relics with one
  parentless checkpoint each, after writing a verified backup bundle, and
  publishes each with a leased push. Removed parts, and every part of a deleted
  relic, stay as stubs with their IDs and dates, so links still resolve; their
  content is only in the bundle. Other clones adopt the checkpoint at their next
  fetch, replaying their own changes onto it or parking one that names a dropped
  part, as a `dropped` relic conflict. A compaction not yet published is dropped,
  with a `relic-compaction-dropped` warning, when origin changed the relic
  first, and restoring a relic compacted while deleted warns
  `relic-content-dropped`. `move-tasks` carries compacted relics. The cache moves
  to schema version 13, which is rebuilt once. See
  [Compacting relics](docs/reference.md#compacting-relics) and
  [Compacted relics](docs/architecture.md#compacted-relics).
- **Relic schemas.** `workbook schema put <type> <file|->` saves a versioned
  schema for a relic type in the project's settings, and `schema list` and
  `schema show` read them. A schema is a file of properties, part types and
  links whose rules use a closed list of JSON Schema words. `relic create`,
  `update` and `migrate` hold a relic to the version it names and refuse a save
  that breaks it, with exit 5; no read or sync ever refuses one, and a relic
  naming a version this checkout lacks cannot be updated until it syncs (exit
  6). Two clones that save a version at once both keep theirs, and the one
  renumbered is reported as `relic-schema-renumbered`. A version is at most
  64 KiB, a type has at most 500 versions and a project 1,000. A schema guides
  honest tools and is not a security boundary. The generated guidelines list
  each type with a schema. See [Relic schemas](docs/reference.md#relic-schemas)
  and [the stored format](docs/architecture.md#relic-schemas).
- **Moving a relic to another schema version.** `workbook relic migrate <relic>
  --to <target>` moves a relic to a version of its type, or to another type,
  if the whole relic fits, and `relic diff` shows a type change. It synchronizes
  as `relic update` does.
- **Relics behind and not fitting.** `relic list --behind` lists the relics
  behind their type's newest version. In JSON, `relic list`, `show`, `export`,
  `create` and `migrate` report `conformance` (`open`, `conforms`,
  `not-conforming` with `schemaProblem`, or `unknown-schema`), a relic's
  `schema` names its version, and `relic list` adds `behind`.
- **Relic history and diffs.** `relic show <relic>[#<part>] --history` lists the
  changes with author and time, a part's body versions, and a stale body save
  marked as not applied. `relic diff <relic>[#<part>] [<version> [<version>]]`
  compares two versions, line by line for a text body and field by field, in
  text or `--json`. A version is a change ID, so one noted before a sync still
  works, and nothing new is stored. See
  [History and diffs](docs/reference.md#history-and-diffs).
- **Relics.** `workbook relic create`, `list`, `show`, `update`, `delete`,
  `restore` and `export` work with them, and `relic create --from-file` creates
  one from the document `relic export` writes. Each relic is one ref under
  `refs/workbook/relics/`, a chain of immutable commits in new formats that
  declare writer-format generation 6. See [Relics](docs/reference.md#relics)
  and [Relic refs](docs/architecture.md#relic-refs).
- **A save over a part's body names the version it started from.** `--base
  <version>` is required, and a save from an older version never overwrites the
  newer text: it is merged or refused. The body keeps its format label
  unless `--body-format` gives another. `relic show <relic>#<part> --body`
  writes the body byte for byte into a file or a pipe, and on a terminal prints
  it with its indentation and line breaks kept and its control characters
  replaced.
- **A relic's limits are part of its stored format.** A body is at most 1 MiB,
  one property value 64 KiB and one field value 16 KiB, measured on the stored
  JSON, where each `<`, `>` or `&` in a string counts six bytes. A value nests
  lists and objects at most 32 levels deep. A property or field name is 1 to
  100 bytes of valid UTF-8 with no `=`, no control character, no format
  character (such as U+200B or U+202E) and no white space at either end, and
  names are compared byte for byte. A relic's properties together, one part's
  fields together, its part counts and its live bodies together are totals: a
  change is refused only when it leaves a total over its limit and larger than
  it was, and a total is never checked when a relic is read. Text that is not
  valid UTF-8 and an object that names a member twice are refused, never
  repaired, and `relic create --from-file` reads at most 64 MiB.
- **Relics are shared with the tasks.** `workbook fetch`, `push`, `sync`,
  `setup`, `move-tasks` and `sync --watch` carry relic refs to and from the
  tasks remote, through tracking refs under
  `refs/workbook/remotes/<remote>/relics/`. `relic create`, `update`, `delete`
  and `restore` fetch first, push the one relic they changed, and print a
  `Sync:` line; `--no-sync` skips both, and the read commands contact no
  remote. The synchronization a task command runs before and after its change
  carries no relic ref, so its cost does not grow with the number of relics.
- **A relic changed in two clones is combined** by replaying one clone's
  commits onto the other's, operation by operation. Two saves over the same
  body version that cannot be merged, and an edit to a relic the remote has
  deleted, stop the replay:
  the remote's version stands, the local tip is parked under
  `refs/workbook/reconciled-relics/`, and the command reports a relic conflict,
  exit 8, in text and in a new `relicConflict` envelope member that carries all
  three bodies. A deletion that synchronizes second applies with no report. Two
  changes that each fit a total but pass it together are both kept, with a
  `relic-over-limit` warning, and an edit to a part removed elsewhere gives
  `relic-edit-on-removed-part`. `workbook sync --status` lists the relic
  conflicts a running watcher holds, as `relicConflicts` in JSON, without body
  text.
- **Links between relics and parts.** `relic update --link <task|relic[#part]>`
  now also links a relic, or one of its parts, to another relic or to one part
  of one, with an optional `--pointer`: a JSON object of at most 512 bytes, such
  as a quote or a time, which a schema's `links.pointer` can constrain. A link to
  a relic records `seen`, the target's version when it was made, so `relic diff
  <target> <seen>` shows what changed since. `relic show` lists links coming in
  (`linkedFrom` in JSON), `relic list --linked-to <task|relic[#part]>` answers
  what links to something, from one index query, and a link whose target is
  deleted, removed or not in this clone is marked (`targetState`, on task links
  too) and fails no command. Relic links are stored in the existing
  `links.json`, at writer-format generation 6, and the cache moves to schema
  version 12, which is rebuilt once. See [Links](docs/reference.md#links) and
  [Relic refs](docs/architecture.md#relic-refs).
- **A relic, or one of its parts, can link to a task.** `relic update --link
  <task> --rel <rel>`, with `--from <part>` for a link from a part, adds a
  link, and `--unlink <link>` removes one by its ID. `rel` is up to 100 bytes
  of lowercase words joined by hyphens, such as `implemented-by`, and a relic
  holds at most 2,000 live links, checked only when one is added. A link is
  stored once, on the relic, in `links.json` and as `link.add` and
  `link.remove` operations; the task's history does not change. Linking what is
  already linked and unlinking a full ID already gone record nothing, so
  retries are safe, and every link two clones add survives their sync. A
  new link to a deleted task or a missing task is refused, and
  `relic create --from-file` still refuses a document with links.
- **Links show from both ends.** `relic show` lists links under `Links:`, and
  marks a link whose task was deleted since `(removed)`, `targetRemoved` in
  JSON. `relic list --linked-to <task>` lists the relics that link to a task,
  with `linkedBy` in JSON, and `workbook show <task>` ends with a `Relics:`
  section, `relics` in JSON, listing the linked relics this checkout holds; a
  relic it cannot read gives a `relic-links-unread` warning instead of a
  failure. On the web board, `/api/tasks/<id>` does not list links;
  `/api/tasks/<id>/relics` does.
- **Relic reads are served from the SQLite cache.** `relic list`, `relic show`
  and `relic export` answer from four new relic tables and a `relic_links`
  table, refreshing only the relics whose ref changed since, so a change made
  in another clone or worktree is seen without a rebuild. The cache holds each
  body's object ID, never its bytes; a body is read from Git when it is shown.
  A relic the cache cannot hold, damaged or written by a newer Workbook, is
  read from Git as before, and a relic read answers from Git when the cache
  cannot be built at all. Task commands other than `workbook show` read no
  relic, and the automatic rebuild a missing cache triggers leaves the relic
  tables to the first relic read.
- **Relic refs are packed after a run that moved one.** Such a run ends by
  packing the relic refs, their tracking refs and parked relic tips with `git
  pack-refs --include`, and no other ref, so a task command's fetch no longer
  reads a file per relic. A run that moved none runs nothing for it, and a task
  command, `push` and the relic reads never pack. A failed pack, a Git without
  `--include` and the reftable backend change nothing about the command's
  output or exit code. A bare origin you host yourself keeps its refs loose
  until `git gc` or `git pack-refs --all` runs there; see [A self-hosted origin
  keeps its refs
  loose](docs/hosting.md#a-self-hosted-origin-keeps-its-refs-loose).
- **`workbook validate` and `workbook reset` cover relics.** `validate` checks
  every commit of every relic history, and refuses a relic in which two commits
  record the same operation ID. `reset` measures a relic as it measures a task:
  a change the tasks remote lacks is work that exists only in the checkout, and
  a parked relic tip is set aside. It writes relic refs and parked relic tips
  to its bundle, a damaged relic ref that still resolves to a commit included,
  and `--no-backup` is refused while such work exists unless
  `--discard-unpublished` is given. In a checkout with no tasks remote to check
  against, which a local-only project is, `--no-backup` is refused with
  `--discard-unpublished` or without it.
- **The generated guidelines and the Workbook skill teach agents to use
  relics.** `.workbook/guidelines.md` gains a `Relics` section: what a relic
  is, its ID under the project's current key, the four commands that find the
  relics linked to a task, read one part and save an edit with `--base`, and
  what to do when that save is merged, is refused as `stale-write` (exit 6), or
  cannot be merged and is reported as a relic conflict (exit 8). Its exit-code table says a
  relic save refused for its `--base` needs a fresh read first. The skill tells
  an agent to read each linked part before editing files. Run `workbook docs
  update` to refresh both. See [Relics for
  agents](docs/reference.md#relics-for-agents).
- **Relics are measured.** `scripts/benchmark.sh --relics` runs a relic
  benchmark family on 1,000 relics of 20 parts, a 2,000-part relic and bodies
  at the 1 MiB and 16 MiB limits, and compares every task command without and
  with that set; `--relic-scale smoke` runs a small set. On the measured run
  every budget was met: the first relic read on a fresh clone took 1,389 ms, a
  reverse lookup from a task 121 ms, and every task command kept its P95 count
  of Git processes, with a median within 10 % of its time without relics. Three
  outcomes follow. The relic limits are confirmed as starting limits: a later
  release may raise one and will lower none. Bodies are not cached; a body read
  is one more Git process. Part positions are not renumbered; a placement that
  would need a position over 256 bytes is refused with exit 5, which in the
  benchmark only repeated placement into one narrowing gap reached. A checkout
  whose objects are loose files, as Workbook leaves them until a `git gc` or
  another Git command's automatic maintenance packs them, makes a synchronizing
  command slower, with tasks or relics; `git gc` removes the cost. See [Relic
  benchmarks](docs/architecture.md#relic-benchmarks).

### Changed
- **Two description edits in a row no longer conflict** when the other clone
  changed only something else, such as the status. A replay now compares each
  local edit with the text it started from, in its own parent commit, rather than
  with the last text the two clones shared.
- **`relic update --base <older version>` merges, or exits 8,** where it was
  refused with exit 6. It merges when the file and the newer body changed
  different lines. Otherwise it records nothing and reports a relic conflict
  with the three texts. Exit 6 remains for a `--base` that is not a version of
  the part, for no `--base` when the part has a body, and for a `--base` when it
  has none. A save repeated from the version it replaced now records nothing and
  exits 0.
- **A `body` relic conflict says why.** It carries a `reason`, and for
  `overlap` the lines in `overlaps`. Its `base` is the text the save started
  from, and a sync's line also names the command that writes it to a file.
- The board's header has a **Relics** link between **Board** and **Config**.
- **New `spec` and `plan` relics are checked.** `relic create <title> --type spec`
  alone is now refused, exit 5, because the built-in requires `status`; add `--set
  status=draft`. `relic list --behind` lists every earlier `spec` and `plan`
  relic that names no schema.
- **A new link's `--rel` is checked.** It must be one of the relationship names,
  or one the relic's schema adds, or `relic update --link` refuses it with exit
  5. Links already held under other names stay readable.
- **`relic migrate` and `conformance` judge links** as well as properties and
  parts: each link's relationship, then its pointer, including links from removed
  parts. A relic under a schema that holds another relationship name reads
  `not-conforming`.
- **The cache moves to schema version 15**, which is rebuilt once, because stored
  judgments now cover links and built-ins, and a stored task operation now keeps
  a description merge.
- **A move concerns this project's refs only.** A task or relic ref at the old
  place whose name is not one of this project's task or relic IDs, under any
  key it has had, is another project's or no project's, and `workbook
  move-tasks` and its `--clean-up` leave it there without a word, where it used
  to leave the move unverified; `reset --remote` still deletes it. A key the
  old place's configuration has, when it has diverged from this checkout's,
  counts as this project's too: a task or relic under it leaves the move
  unverified, exit 8, naming the key and the `workbook key add` that lets the
  next run carry it, and `--clean-up` deletes nothing meanwhile.
- **`workbook rebuild` indexes relics too.** Its text line now reads `Rebuilt
  <n> task(s) and <m> relic(s) at <path>.`, and its JSON gains `relicCount`.
  The cache's schema is version 11, so the first command run by this build
  replaces an older cache.
- **A relic push that fails on the wire after the task push** now leaves the
  push phase `completed`, says so in its `detail`, and names the task errors of
  the same run before it instead of hiding them.
- **The watcher's status document is now version 2.** A command does not hand a
  change to an older watcher.
- **`relic list` leaves retired relics out**, as does `relic list --linked-to`,
  unless `--retired` or `--all` is given.
- **A compacted relic's history and diffs start at its checkpoint.** `relic show
  --history` and `relic diff` say so, with a `compacted` member in JSON, and
  refuse a version the compaction dropped as `not-found`.

### Compatibility across versions
- **A task with a merged description needs a build with this change to be
  changed.** A merge is written at writer-format generation 10. v0.5.0 through
  v0.7.0, and any build from before merged descriptions, show the task with the
  merged text and refuse to change it (exit 9, `newer-writer`), until they are
  upgraded. One that holds its own unpublished change to the task reports
  `needs-upgrade` and moves nothing, and publishes the change after the upgrade,
  so nothing is lost. A task that never had a merge is written as before, and
  compacting a merged task makes it changeable again by a build that reads
  compacted tasks, v0.7.0 included.
- **A merged relic needs a build with this change to be changed.** A merge is
  written at writer-format generation 9. A build from before merging shows the
  relic from its stored state, refuses `relic update` and `relic diff` as
  `newer-writer`, and reports a clone that holds its own change to the relic as
  `needs-upgrade`, moving nothing. v0.7.0 and earlier never fetch relics.
- **Upgrade every Workbook on the machine before creating relics.** A Workbook
  from before relics does not know the refs that hold them. Its `workbook reset
  --no-backup` deletes the relic refs without writing a bundle, and the restore
  command its plain `workbook reset` prints does not fetch them back from the
  bundle it wrote. A relic the tasks remote holds comes back with the next
  sync; a change not yet published can be lost.
- **A retired or compacted relic needs a build with this change to be
  changed.** Both are written at writer-format generation 7. A build from before
  retiring and compaction reads them and refuses to change them as
  `newer-writer`; v0.7.0 and earlier never fetch relics.
- **A project's first relic schema makes its settings read-only to an older
  Workbook.** `schema put` marks the settings history as writer-format
  generation 6. A release from before relics, v0.6.1 and v0.7.0 among them,
  still reads the settings, tasks and statuses, and is told to upgrade when it
  tries to change a status, a priority, a key or another setting kept there.
  Upgrade every clone before saving a schema.
- **A relic written under a built-in schema needs a build with this change to be
  changed.** It is written at writer-format generation 8. A build from before the
  built-ins reads it, refuses to change it as `newer-writer` and never pushes
  over it; such a build creates `spec` and `plan` relics as open
  ones. v0.7.0 and earlier never fetch relics.
- **Releases can disagree about `--behind`.** A later release can add a newer
  built-in, and a relic on the older one is behind only for whoever runs it.
- **A Workbook from before relics works beside them otherwise.** v0.6.1 and
  v0.7.0 read, change and synchronize tasks in a checkout that holds relics,
  and leave the relics at a remote alone. A task with a merged description is the
  exception to changing them, as above.

## v0.7.0 — 2026-11-23

A project can now keep its tasks in a repository of their own, finished tasks
can be compacted, and the web board can be hosted for a team behind a sign-in
proxy and used on a phone. Tasks gain due dates, and `workbook board` is gone.
Compacting, setting a due date and naming a tasks remote each need every clone
upgraded first; see Compatibility across versions below.

### Added
- **Tasks can live in a repository of their own**, so that access to them,
  such as a hosted board's deploy key, is not access to the code. `workbook
  config set tasks-remote <name> --url <address>` names the remote, and
  `workbook move-tasks` copies every task there and leaves a marker that sends
  checkouts on older settings to the new place. It is safe to run again, carries
  an edit that reached the old place late exactly once, and can move the tasks
  back. See [A tasks remote other than origin][tasks-remote] and
  [Keep the tasks in their own repository][own-repository] (#219, #223).
- **`workbook compact`** replaces the history of Done and Deleted tasks with one
  checkpoint commit each, after saving the old history to a backup bundle. It
  runs only by hand. Other clones adopt a compaction on their next fetch and
  replay their unpushed edits onto it, and `show --history` says how many
  changes were compacted. Durations such as `sync --interval` now accept days
  (`30d`). See [Task archive compaction][compaction] (#198).
- **`workbook reset`** removes Workbook's refs and caches from a checkout, or
  from a remote with `--remote`, after writing a backup bundle, so a damaged
  checkout can be set up again from scratch. `workbook move-tasks --clean-up`
  resets the place a finished move left behind. See
  [Resetting Workbook data][reset] and [Leaving Workbook][leaving] (#224).
- **Tasks can have a due date.** `create --due` and `update --due` take
  `YYYY-MM-DD`, `today`, `tomorrow` or `+Nd`, and `list --overdue`,
  `--due-before` and `--sort due` find dated tasks. `show`, `list`, the board
  and the desktop app's Next view mark a task overdue or due soon. See
  [Due dates][due-dates] (#222).
- **`workbook serve` can run behind a sign-in proxy.** `--public-url` names
  the address people open the board at, and `--actor-header` names the header
  carrying the signed-in person's email, so each person's edits are recorded
  under their own name. The new [hosting guide][hosting] sets one up with
  Cloudflare Tunnel and Cloudflare Access (#215).
- **`workbook setup --tasks-only <url>`** sets up a folder that holds a
  project's tasks and none of its code, for a server that only hosts the board
  (#207).
- **The web board works on a phone.** At 560 CSS pixels wide or narrower it
  shows one column at a time under a row of column tabs, and the task page and
  forms fit the screen. It has not yet been tried on a real phone (#217, #220).
- **The board shows the edits its own sync set aside.** A card says "1 edit
  set aside", and the task page shows each edit with buttons to copy or dismiss
  it. Until now only the server's terminal reported them. See
  [Edits the board's sync set aside][set-aside] (#215).
- **`WORKBOOK_ACTOR`** records edits under a given email address in place of
  the checkout's `user.email`, as a server acting for many people needs (#208).
- **`WORKBOOK_NOW`** makes a command record a given time in place of the
  clock, so a project's past can be imported with each step dated when it
  happened. See [Recording past work][past-work] (#197).
- **Workbench can remove a project** from its sidebar, leaving the repository
  untouched, and marks a project whose repository is gone instead of failing
  to start its board (#202).

### Changed
- **`workbook board` is removed.** Use `workbook serve` or the Workbench app.
  A script that read `workbook board --json` can read `workbook list --json`,
  which returns the same tasks in the same order (#221).
- **Only a comment's author can edit it**, from the command line or the board.
  Anybody can still remove a comment, and the thread now says who removed one
  and who edited one when it was not its author (#213).
- **A commit names the person its edit is recorded under**, including when a
  sync replays a teammate's edit, so a shared watcher or board publishes each
  person's edits under their own name. `--assign` refuses a name that is not
  valid UTF-8 (#208).
- **The board hands its saves to a running watcher**, as it was meant to. A
  save returns without waiting for the network and reaches the remote a few
  seconds later (#219).
- **The board keeps a change a proxy asked it to send later.** A `429` or
  `503` carrying `Retry-After` is retried up to three times instead of being
  lost, and an error page that is not JSON is reported in a plain sentence
  (#214).
- **The board's task forms were reworked.** The New Task form can assign, a
  task's page uses the same sidebar layout, Depends On and Blocks are one
  Dependencies section whose picker leaves out tasks that would close a cycle,
  and the sidebar's rows, buttons and keyboard focus are tidier (#203, #204,
  #205, #206).
- **Push errors say more.** A push Git refuses outright reports Git's own
  message rather than `Git returned 0 push outcomes, want 1`, and a push refused
  for some task refs names up to three of them with Git's reason. A script that
  matches the whole old message must match its start instead (#215).
- **A failed background sync is logged.** `workbook serve` and `workbook sync
  --watch` write a line to standard error when a round fails and when it
  recovers; they used to write nothing (#215).
- **Git 2.29 or later is required**, and the README says so (#207).
- **The desktop app runs on Electron 44.11.5** (#192).
- **The SQLite projection is rebuilt once after upgrading.** Nothing needs to
  be done, and no task data lives in the cache (#198).
- **The format generation is now 4**, for compacted tasks only. A task nobody
  has compacted is unchanged, byte for byte (#198).

### Fixed
- **A remote's password no longer appears in error messages.** Workbook
  removes the credentials of the addresses Git is configured with from Git's
  error output, on the command line and the board, where a failed publication
  could show them to whoever made the change (#209).
- **Typed text can no longer disguise itself on the board.** Bidirectional
  override characters in titles, comments, file names and elsewhere are drawn
  as spaces, so a file name cannot make an `.exe` read as a `.pdf`. See
  [What anyone who can open the board can do and see][board-exposure] (#209).
- **A parked ref that holds an edit the task lacks is no longer pruned**, and
  listing tasks no longer slows as parked refs accumulate (#215).
- **Sync no longer stops on a commit with an identity Git will not write
  again**, such as one another tool wrote with an empty name (#215).
- **Simultaneous requests to the board no longer fail with a rebuild error**,
  and `workbook rebuild` no longer refuses while tasks are being written (#216).

### Compatibility across versions
- **Upgrade every clone before anyone compacts.** A v0.6.1 clone that meets a
  compacted task reports it as invalid on every sync, and with the pre-push
  hook its code pushes fail too, until it upgrades. Nothing on the remote is
  lost. Every compaction run says so (#198).
- **Upgrade every clone before anyone sets a due date.** Older releases show
  such a task without its date and refuse to change it (exit 9). Tasks without
  a date are unaffected (#222).
- **Upgrade every clone before committing a tasks remote.** Older releases
  refuse a `.workbook/config.json` that names one (exit 7) (#219).
- **An older clone can still edit somebody else's comment.** Every clone shows
  the edit, and this release names who made it, so it no longer reads as the
  author's own words (#213).

[tasks-remote]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#a-tasks-remote-other-than-origin
[own-repository]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/hosting.md#keep-the-tasks-in-their-own-repository
[compaction]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#task-archive-compaction
[reset]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#resetting-workbook-data
[leaving]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#leaving-workbook
[due-dates]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#due-dates
[hosting]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/hosting.md
[set-aside]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#edits-the-boards-sync-set-aside
[past-work]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#recording-past-work
[board-exposure]: https://github.com/dgoings/workbook/blob/v0.7.0/docs/reference.md#what-anyone-who-can-open-the-board-can-do-and-see

## v0.6.1 — 2026-10-28

Fixes and polish on top of v0.6.0, most of them in the desktop app. An idle
board stops resending every task once a second, the sidebar shows each
project's current key, and a status rename that brings a retired name back to
life now says what it moved. Nothing here changes a stored format, and a
v0.6.0 clone works alongside a v0.6.1 one.

### Added
- **`workbook key list --no-counts`** prints the key listing without counting
  the tasks under each key, so it never opens the projection. In JSON each
  key's `tasks` member is left out, as the mutating key commands already leave
  it out; in text the TASKS column is dropped. The desktop app uses it to read
  every project's current key (#179).

### Changed
- **Renaming a status onto a retired name warns.** The rename still succeeds,
  but it moves the history the retired name was forwarding elsewhere, so the
  result now carries a `status-rename-reresolves` warning and, in JSON, a
  `reresolved` member naming the retired name, where its tasks used to resolve,
  where they resolve now and how many active tasks moved. The board's `/config`
  Statuses page shows the same warning (#189).
- **The board polls conditionally.** `GET /api/tasks` answers with an `ETag`
  and a matching `If-None-Match` gets `304 Not Modified` with no body. An idle
  board used to download, parse and redraw the whole document every second;
  on this repository that was 437 KB a second. Now it receives headers only
  (#181). To keep that true, the "assigned 5 minutes ago" wording is no longer
  in the document: each assignment row in `/api/tasks` and in the assignment
  mutation answers has lost its `ago` member, and the page words the row from
  `createdAt` on its own tick by the same rule `workbook show` uses (#182).
- **The desktop sidebar, rail, menu and Next view show each project's current
  key**, not the key it was imported under, so they agree with `workbook key
  list` after the key changes (#178).
- A folded category can be unfolded from the collapsed rail, and both the
  rail's and the sidebar's fold buttons are 24px click targets (#170, #174,
  #176).
- The window drags by the top row of the Import and Next views and of a board
  that is starting or has failed, not only by the sidebar head (#188).

### Fixed
- **A failed board start shows its own message.** A board's view no longer
  stays laid over the shell's "Could not start this board" text, whether the
  start failed over a working board or you switched away during a slow start
  (#183). The message no longer carries Electron's "Error invoking remote
  method" prefix, and a start you have already clicked away from no longer
  overwrites the status line of the board you are opening now (#193).
- Hover tooltips in the desktop sidebar and Next view show reliably on macOS
  instead of rarely after the first hover, working around
  electron/electron#49843 (#180, #185).
- A task's Blocks list no longer shows deleted tasks, whose edge cannot be
  changed and has no effect while they are deleted (#190).
- The task form's placeholder for a status or priority the project does not
  define leads with the value, `Current: critical (not in this project)`, so a
  narrow select cuts off the explanation rather than the value, and a card
  holding such a priority marks its chip (#171).

### Release tooling
- A patch to an older minor line can be cut from a `maint/vX.Y` branch, which
  `scripts/start-release-line.sh` creates (#175).
- Publishing never moves the Latest badge, the Homebrew tap or the rolling
  `desktop-latest` release backward to an older version (#172), and generated
  release notes start at the line's previous release (#177).
- `scripts/cut-release.sh` fetches only `main` and the release tags, so a
  moved `desktop-latest` tag no longer blocks a cut (#168).

## v0.6.0 — 2026-10-08

Priorities become a per-project vocabulary, the way statuses already are, a
project may have more than one task-ID key, `workbook setup` stops assuming
a project key, and the desktop app grows keyboard shortcuts for switching
projects and driving the board it is showing. Workbook and Workbench also run on
Windows now, where until this release nothing could be imported and no board
would start. Three of the changes below affect scripts; all three are described
under Changed.

### Added
- **Shell completions for bash, zsh and fish**, printed by `workbook
  completion <shell>`. The scripts are generated from the same command schema
  `workbook help` is rendered from, so they complete every command, subcommand
  and option the version that printed them accepts, and no hand-written list
  can fall behind the CLI. They complete names only: a task ID, a status or a
  title is left alone rather than guessed at, and none of the three shells
  falls back to offering file names. The Homebrew formula generates and
  installs all three during `brew install`; a source build writes the one line
  for its shell, which the README's Installation section gives.
- **The desktop app's sidebar can be reordered and grouped.** Drag a project
  to move it, and group projects under named categories that fold away: the
  `+` beside Projects makes one and opens its name for typing, a double-click
  renames it, and removing one puts its projects back at the top level where it
  stood. Cmd+1–9, Previous/Next Project and the Next view all follow the
  sidebar's order top to bottom, counting projects in a folded category, so
  folding one never renumbers anything. The order is kept with the project
  list; a list from an older build opens in the order it was imported.
- **The desktop app has keyboard shortcuts**, all in the application menu:
  Cmd+1–9 and Cmd+Option+Up/Down for projects, Cmd+0 for Next, Cmd+Shift+I
  for Import, Cmd+Shift+D to toggle dark mode, Cmd+B for the sidebar, and
  Cmd+N, Cmd+F, Cmd+,, Cmd+[, Cmd+] and Cmd+R on the active board. Ctrl on
  Windows and Linux.
- **A project may have more than one task-ID key.** `workbook key` gains
  `list`, `add`, `current`, `retire` and `log`; `workbook create --key` mints
  under another active key and `workbook list --key` shows one key's tasks.
  Keys are project configuration, recorded in the same synchronized history as
  the statuses, and the web board's configuration page administers them beside
  the statuses and the priorities, with the new-task form offering the key to
  mint under once there is more than one. Existing task IDs never change, a
  retired key's tasks stay this project's, and nothing renames or deletes a
  key. A ref on `origin` under a key this project does not have is reported as
  another project's rather than as junk to delete, and a fetched ref whose key
  this project has but whose documents name another project is reported the
  same way instead of failing the synchronization, so adding a key by mistake
  is undone by retiring it. A project that adds a key records a configuration
  a Workbook older than this release cannot fold: it reads the ledger as newer
  than it can read and sees tasks under the new key as another project's refs,
  so everyone on the team upgrades together.
- **A project defines its own priorities.** `high`, `medium` and `low` stop
  being the only ones there are: `workbook priority` gains `list`, `add`,
  `rename`, `label`, `move`, `tag`, `delete`, `color` and `log`, the same verbs
  `workbook status` has, and every mutating one prints the command that reverses
  it. A project that changes nothing keeps the three it has always had, and its
  stored history does not change by a byte.
- **The web board manages priorities too**, on its configuration page, beside
  the statuses it already managed: define one, rename or relabel it, reorder by
  dragging a row or with its Up and Down controls, move the `default` role, and
  remove one by naming where its tasks belong. A priority row also carries the
  color below.
- A priority carries a color, set with `workbook priority color <priority>
  #rrggbb` and cleared by omitting the value, which returns it to one derived
  from its position. The board offers the same thing as a swatch beside a
  `#rrggbb` field, and a color chosen there redraws the board at once rather
  than on the next reload — as does the project's own accent or text color,
  which used to need one.
- Every priority is drawn on the board in its own color, where only the
  built-in three ever were. A priority with no color of its own gets one the
  board derives from its position, which is what the documentation has
  described for some time without anything implementing it.
- `workbook priority delete` requires `--into`, naming where the removed
  priority's tasks belong. It is never guessed, and removing the last remaining
  priority is refused outright — every task has to be at one.
- A priority filter naming a priority that was renamed or removed away now says
  what the name resolves to, instead of quietly listing a different priority's
  tasks. It carries a `priority-filter-forwarded` warning beside the results,
  mirroring what a status filter has always done.
- `.workbook/guidelines.md` documents the priorities a project actually uses.
  Its "Canonical priorities" table always printed the built-in three, so the one
  document an agent reads to learn a project's vocabulary described a different
  project. That table is now headed "Priorities" and carries what the statuses
  table above it carries — each priority's position, machine value, display
  label and tag, with a legend saying that a task created without `--priority`
  lands on the tagged one.
- **`workbook next --limit <n>`** offers the first `n` eligible tasks in
  `next`'s order, with a count of every task that qualified, so a caller can
  show what comes after the first choice. Plain `next` is unchanged.
- **The desktop app puts `workbook` on your PATH.** On launch Workbench copies
  the CLI it bundles into a directory of its own — `~/Library/Application
  Support/Workbench/bin` on macOS, `%LOCALAPPDATA%\Workbench\bin` on Windows,
  `~/.local/share/workbench/bin` on Linux — and appends that directory to your
  PATH. On macOS and Linux that is a marked block written into each of
  `~/.bashrc`, `~/.zshrc` and `~/.profile` that exists, or into `~/.profile`,
  created, if none of them does, plus `~/.config/fish/config.fish` in fish's
  own syntax when fish is indicated; on Windows it is the user PATH in the
  registry instead. The app says once what it takes to see the change: a new
  terminal, or on Windows signing out and back in, since the stored PATH cannot
  be broadcast to running processes. The directory is appended rather than
  prepended, so an existing Homebrew or `go install` build earlier on your PATH
  keeps winning; `WORKBENCH_SKIP_PATH_SETUP` turns the whole thing off, and a
  development run does nothing because it has no bundled binary to copy.
- **The desktop app gains a Next view.** With two or more projects imported,
  a Next entry above the project list shows what each project would hand out
  next, as `workbook next` decides it, with a number for how many per project;
  a row opens that task on its board. It refreshes every five seconds.
- **The web board gains a filter row.** A search over title, description or a
  task-ID prefix and Priority, Labels and Key choosers narrow the columns, every
  control writes the address so a filtered board is a link, and columns report
  `visible / total`; the Labels menu has a search box of its own. A pasted task
  ID finds that one card, since an ID is matched from its beginning — a key, a
  dash and more characters — and never from the middle.
  `workbook list --find <text>` applies the same search rule, title,
  description or a task-ID prefix alike, from the CLI.
- **The desktop app asks for whatever Git cannot supply before opening a board
  that needs it.** A checkout with no `user.email` has nobody to record changes
  as, and what the app showed was Git's own `git config --get user.email
  failed: exit status 1`. In place of the board it now shows a form, writes
  what you give it with `git config` — for every repository (`--global`) or
  only this one (`--local`) — so it is the identity your own commits carry, and
  then opens the board you asked for. Usually it asks for the address alone,
  because Git makes a name up from your computer account when none is
  configured. On a machine where that gives nothing — a Linux account with no
  full name, for instance — the form asks for the name too, since Git refuses
  to commit without one. It asks for exactly what Git reports it cannot supply,
  and marks the rest optional. A fresh install of Git has no identity at all,
  which is why this was the first thing a new user met.

### Changed
- **`workbook setup` asks for the project key only when it creates a
  project.** A clone of an existing project no longer needs `--key` to join
  it; the flag is now a claim that has to agree with the project's key. A new
  project on a terminal is prompted, with a key derived from the directory
  name (`my-app` becomes `MA`) as the default; setup asks only when stdin
  and stdout are both terminals and `--json` was not passed, and otherwise
  takes that derived key silently. The old silent `WB` survives only as the
  fallback for a directory name that yields no key, so a script that never
  passed `--key` now gets a key that names its project.
- **Every project created by this release records its priorities, and so
  requires a v0.6.0 or newer clone to change its configuration at all** — not
  only its priorities, because the configuration ledger carries one version
  requirement for the whole document rather than one per section. Older clones
  keep fetching, keep reading, and keep publishing their own tasks; what they
  cannot do is rename a status, change a display setting, or touch a priority
  until they upgrade. A teammate on an older build sees a message saying to
  upgrade rather than one saying the project is corrupt.
- **`workbook list --status <name>` now exits `5` for a status this project
  does not define**, where it returned an empty list at exit `0` with a
  warning. An empty table cannot be told apart from an empty column, so the
  old answer threw away the one fact worth having: this checkout has never
  heard of that name, which usually means it has not fetched. A name that a
  rename or a removal still forwards is unaffected — it resolves, the tasks
  come back, and a warning says what the name now means. `--priority` has
  always answered an unknown priority this way. Both refusals now say which
  statuses — or priorities — this project does define, and that fetching is
  what fixes a name a teammate has and this clone does not; supplying a value
  to a task is still answered with the shorter `invalid task status "…"`,
  because there the value is the news rather than the clone.
- **The priorities table in `.workbook/guidelines.md` is now most urgent
  first**, where it read Low, Medium, High. Every existing project's copy
  flips to High, Medium, Low the first time anything regenerates it — a
  priority verb, a status verb, `workbook docs update`, or `workbook setup` —
  so expect a diff in a generated file nobody edited. It is the order
  `workbook priority list` and both boards have always used, and the order the
  statuses table beside it uses; the two now agree.
- **The warning code `status-filter-unresolved` is now
  `status-filter-forwarded`.** It was minted when that warning also covered the
  case the refusal above has taken away, so its name described the one thing it
  no longer reports.
- **The desktop app starts every imported project's board server at launch**
  and on import, rather than when a board is first opened, so each project's
  synchronization loop runs for as long as the app does.
- **The desktop app's macOS releases are signed with a Developer ID and
  notarized**, so they open like any other downloaded app, without the trip to
  the Finder context menu, and they update in place: the update downloads in
  the background and installs on restart, as it does on Windows, rather than
  arriving as a disk image to drag into Applications. A build made without a
  signing identity is still ad-hoc signed, and still updates by disk image,
  because macOS will not install an update over a bundle it cannot verify. The
  first signed release still updates the old way, since it is installed by the
  unsigned copy before it.
- **Show Deleted moved from the header into the board's new filter row.** Its
  address, `/?deleted=1`, is unchanged, so existing links keep working.
- **Every command opens its repository with one `git rev-parse` process
  instead of two.** The root and the common directory were two questions in
  two processes; rev-parse answers both in one, about twenty-five
  milliseconds saved per command.
- `workbook serve` parses the board page once per process instead of per
  handler.

### Fixed
- **Saving the board's settings after changing a status or a priority no longer
  erases the project's name and colors.** A vocabulary change answered with
  only half the configuration, so the settings form redrew empty and the next
  save wrote those blanks into the ledger — a project could lose its name to
  somebody reordering a column. Both kinds of change now answer with the whole
  configuration they wrote.
- **A task could be filed at a priority the board could not offer.** The task
  form listed `high`, `medium` and `low` whatever a project had defined, so a
  project that added one could not put a task at it from the board, drag
  placement used the wrong order, and a new task landed on `medium` rather than
  the project's own default.
- A task stored at a priority the project no longer defines is no longer
  silently moved. The form had no way to show such a value, so it fell to the
  first one in the list and saving reassigned the task; it now says what the
  task is at and leaves it alone until somebody chooses.
- A `docTargets` entry that didn't name a file inside the project used to be
  joined onto the project root with no check. `docTargets: ["AGENTS.md",
  "docs"]` — an easy entry to list by mistake — wrote the guidelines, the skill
  and `AGENTS.md`, then failed on `docs` itself with the operating system's
  generic "is a directory." A value that escaped the project, such as
  `../../.bashrc`, fared no better, writing without ever failing at all. An
  absolute path was different: `filepath.Join(root, "/etc/motd")` is
  `<root>/etc/motd`, which usually doesn't exist, so the entry was silently
  skipped, and on a project where it did exist, `workbook docs` wrote there
  while reporting the absolute path as the file it touched. All three mistakes
  are now refused up front, before anything is written, naming the offending
  value and where it came from. A relative `skillDir` follows the same escape
  rule; an absolute `skillDir` still works, since keeping one personal copy of
  the skill across projects is a documented, separate feature.
- **Moving a priority to the position it already holds is refused** rather than
  recorded. On a project that had never configured its priorities, that empty
  change wrote a configuration section and required every teammate to
  upgrade — for nothing. `workbook status` has always refused the same thing.
- **Ctrl+C on `workbook serve` now exits at once, and exits cleanly.** With a
  browser tab open on the board it used to sit for five seconds and then print
  `serve board: context deadline exceeded` and exit non-zero, with nothing
  actually wrong: browsers open spare connections they never send a request on,
  and the shutdown was waiting for requests that were never coming. It now drops
  those connections immediately and gives only a request already under way a
  couple of seconds to answer. A change made on the board is recorded locally
  before it is published, so quitting in the middle of a publish leaves the
  project where a failed publish leaves it — recorded, and picked up by the next
  sync.
- **A deleted dependency no longer wrecks the layout of the board's task
  panel.** A relationship row is a two-column grid, but only its Remove button
  said which column it belonged in; every other part landed wherever the order
  of appending put it. A row with no Remove button — a Blocks row whose task is
  deleted — pushed its metadata line and its explanation into the second
  column, which is sized to its own content, and that squeezed the title's
  column to nothing: the title wrapped one character per line into a wall of
  text hundreds of pixels tall, with the badge overlapping the text beside it.
  Each part of the row now sits inside one of two items that name their own
  columns, so nothing appended later can move them. A draft row carrying an
  error was broken the same way and is fixed with it.
- A dependency that cannot be removed now says why and what to do about it. The
  note read "Read-only because deleted tasks cannot be changed", which named
  neither whose record was in the way nor the way out; it now names the deleted
  task the relationship is stored on and points at the Restore control in the
  board's Deleted column. Removing a deleted task from a live task's
  dependencies has always worked — it is only that mirror relationship, stored
  on the deleted task itself, that Workbook will not touch — and the rule is now
  covered by tests, including when the deleted dependency is named by a prefix
  rather than its full ID.
- **Closing the desktop app's window and reopening it from the dock no longer
  leaves the new window half broken.** Closing the window does not quit
  Workbench on macOS, and the window it built next inherited every board view
  belonging to the window that was gone. Cmd+B was the visible casualty:
  collapsing the sidebar moves each board to the new width, and moving a view
  whose window has been destroyed fails, so the chord did nothing at all. A
  closed window now lets go of its boards and its own chrome, and the window
  that replaces it starts with nothing borrowed — including from a board that
  was still starting up as the window went. The board servers are left running,
  so reopening a board afterwards is as quick as opening one whose server is
  already up, which is the part of the wait worth saving.
- **A Dark Mode choice Workbench could not store no longer turns up later
  anyway.** A board's switch is the whole window's: the board that was clicked
  changes at once, and Workbench stores the choice and passes it to the shell
  and to every other open board. When that write failed — a full disk, a
  preferences directory that is not writable — the clicked board went dark and
  everything around it quietly stayed behind, and the refused choice was still
  held in memory, so the next save of anything at all, a sidebar collapse or an
  import, wrote it out: the rest of the window adopted, at some unrelated
  moment, a mode that had been asked for once and denied. A refused write is now
  rolled back, and the failure is reported instead of going unhandled in the
  app's main process.
- **A Workbook built from a Windows clone can set up a project.** Git for
  Windows installs with `core.autocrlf=true`, so a default clone of this
  repository carried CRLF copies of the skill and the board page that are
  compiled into the binary, and a binary built there refused every project with
  "embedded skill is missing YAML frontmatter". A `.gitattributes` now checks
  text out with LF whatever a clone's own setting says.
- **Importing a project on Windows no longer fails with "Access is denied".**
  Workbook flushes the directory holding its private cache, so that the rename
  publishing the project guard survives a crash, and Windows cannot flush a
  directory handle at all. NTFS journals that rename itself, so the flush is
  skipped there.
- **A Windows clone of a Workbook project is no longer refused as not
  canonical.** The tracked `.workbook/config.json` is compared byte for byte
  with its canonical encoding, and a Windows checkout writes it with CRLF, so
  every clone was turned away. Those line endings are folded back before the
  comparison, which undoes Git's conversion and nothing else: any other
  difference is still refused.
- **A board on Windows can open its cache.** The cache is opened through a
  `file:` URI built from its path, and a path beginning `C:\` produced a URI
  whose host was the drive letter — "invalid uri authority". A drive is now
  spelled the way SQLite reads it.
- **`workbook docs update` no longer leaves a duplicate managed block behind on
  a Windows clone.** Workbook finds the block it manages in `AGENTS.md`,
  `CLAUDE.md` and the skill by its marker lines, and a CRLF checkout spells
  those lines differently, so the block went unrecognized and another was
  appended below it — once per fresh checkout, since the copy was written with
  LF and the next run found that one instead, which is also the one `workbook
  docs remove` took out rather than the block the file started with. The markers
  are recognized either way now, and a block written into a file that uses CRLF
  is written with CRLF, whichever convention the block being replaced happened
  to carry. A file that already collected a duplicate is cleaned up by the next
  `workbook docs update`, which leaves one block where the first one was and
  keeps whatever was written between them; `workbook docs remove` takes them all
  out at once.
- **`workbook-bench` no longer kills a fixture build with the per-command
  timeout.** Fixture construction gets its own bound, as storage benchmarks
  already had, so a fixture — an init, a synthetic history, and for the remote
  topologies two clones and dozens of plumbing calls — is no longer expected to
  finish inside the budget for one measured command, and a busy machine can no
  longer make a healthy fixture build die of that deadline. Every benchmark that
  builds a fixture gets the new bound, the scaling matrix included, and all of
  them take it from one number. What the benchmark reports as a timeout is
  unchanged. The bare origins the harness publishes into also carry the
  fixture's own isolation settings now, instead of inheriting the operator's
  global `core.hooksPath`, where a company hooks directory could reject or
  charge for the very push being measured.
- **`workbook-bench` no longer records a failed sample when the machine is too
  busy to drain a finished command's output within a tenth of a second.** The
  harness bounded that drain at 100ms, which on a loaded machine is a stopwatch
  on the Go scheduler rather than a guard: a command that wrote nothing and
  exited cleanly came back with no duration, an exit code of -1 and
  `exec: WaitDelay expired before I/O complete`. The bound is now five seconds,
  the same number the object-batch streaming already uses, which still catches a
  command that exits leaving a descendant holding its pipe open.
- **Every command now closes its projection database.** The SQLite read cache
  was opened per command and never closed, and `database/sql` keeps a
  connection-opener goroutine alive for each open database, so a process that
  runs command after command collected one goroutine per command: a long-lived
  `workbook serve` or `workbook sync --watch` grew for as long as it ran, the
  desktop app with it, since it runs one `workbook serve` per imported project,
  and one run of the command-line test suite left roughly thirteen hundred
  goroutines behind. A one-shot command is unaffected, because it was already
  giving the handle back by exiting.
- **The desktop app's window moves by its whole top row.** Workbench hides
  the native title bar on macOS and Windows, which leaves the window movable
  only by a region the app marks as draggable, and since the shell's first
  build that was the sidebar head alone, and on macOS not even that. Now the
  top row of the window is one bar, as in Slack: the sidebar head, with the
  Workbench wordmark and the collapse chevron in the same row as the window
  controls, and beside it the board's own header, laid out from the window's
  top edge with its repository eyebrow on the wordmark's line. The window
  drags from anywhere along that row, in the expanded sidebar and the
  collapsed rail alike; the chevron, the version line and the header's links
  and switches still click rather than drag. On macOS the traffic lights are
  pinned to the row and the wordmark starts to their right; in the rail they
  take the row, and the chevron sits under them. On Windows the header keeps
  its switches clear of the window controls. The line between the sidebar and
  the board stops at the bottom of the board's header, so the head and the
  header read as one band. A board opened in a browser draws no such line and
  is no drag region. Linux keeps its native title bar.

## v0.5.1 — 2026-08-23

Five stories from adversarially reviewed pull requests: the web board learns
to edit assignments and accept pasted images, and three defects found in
earlier reviews are closed.

### Added
- Assignments can be added and withdrawn from the web board, through the same
  claim semantics the CLI uses (#121).
- An image in the clipboard pastes straight onto the attachment controls, with
  a generated filename and the same ceilings as drag-and-drop (#124).

### Fixed
- With no sync watcher answering, the board's Publishing switch shows a
  stalled state naming why, instead of silently flipping server configuration
  it cannot apply (#123).
- Terminal output neutralizes Unicode bidi-control format characters, so a
  hostile title or comment can no longer visually reorder a rendered line
  (RTLO spoofing); benign format characters like emoji ZWJ pass through
  (#122).
- `workbook validate` now refuses a history containing duplicate operation
  ULIDs as corrupt-data, and the projection reports the offending task instead
  of crashing with a raw SQLite constraint error (#120).

## v0.5.0 — 2026-08-18

A project now owns its statuses, tasks carry comments, attachments and
assignments, deleted tasks come back through the board, and project identity
no longer depends on what a branch happens to contain — from thirty reviewed
pull requests.

### Project identity

- Project identity moves out of branch content into `refs/workbook/project`.
  A checkout taken before a project adopted Workbook carried no
  `.workbook/config.json`, so bootstrap minted a second identity and the
  private guard then rejected the real configuration on every command; refs
  live in the common Git directory, so nothing about a branch can strand
  identity anymore. The ref is written deterministically, so two clones
  adopting the same project converge on the same commit, and a disagreeing
  private guard is now repaired from the ref instead of wedging the
  repository (#81).

### Per-project statuses

- The status vocabulary becomes a modeled document, and status validity moves
  to the mutation boundary. A status stored in history is never rewritten,
  only resolved through the vocabulary on read — so every task ref already
  written stays byte-identical, a property captured in a golden test against
  the previous release before any change landed (#82).
- The vocabulary lives in `refs/workbook/config`, a configuration ledger with
  a Git history of its own, synchronized and reconciled the way task refs
  are: a rename made in one clone arrives in the next as an operation, not a
  file merge, so concurrent changes converge or surface as domain conflicts
  instead of conflicting a JSON file (#83).
- `workbook status` gives a person and an agent the vocabulary: `list`,
  `add`, `rename`, `label`, `move`, `tag`, `untag`, `delete --into`, and
  `log`, whose every entry prints the exact inverse command that would undo
  it. Deleting a status requires a destination, so no task is ever stranded
  by a vocabulary change (#84).
- Both boards build their columns from the project's own vocabulary. A
  project that renamed `ready` to `todo` used to get a READY column with the
  task missing from it; a stored status that a rename or removal forwards is
  now drawn in the column it now means, and only a status that forwards
  nowhere lands in the unknown region. The terminal's wide-layout threshold
  becomes a per-column budget, so a three-status project reaches its wide
  layout on a terminal that could never have fitted six (#85).
- The generated guidelines describe the statuses the project actually has:
  the table gains position and tag columns with a legend saying what each tag
  makes the machine do, and the lifecycle prose is read off the tags rather
  than off hardcoded names (#86). A display label carrying an HTML comment
  opener can no longer swallow the rest of the generated document, and the
  migration note about a retired default column stops nagging a project that
  deliberately added the column back (#90).
- A new project no longer gets a Blocked column. Tasks have dependencies, and
  a column duplicating that claim is a second answer to the same question.
  Nothing is taken from existing projects: a project that already has
  `blocked` keeps it until a person runs
  `workbook status delete blocked --into <status>` — this repository ran
  exactly that migration (#87, #88).
- The statuses can be administered from the browser at `/statuses`: add,
  rename, relabel, retag, reorder by drag or by buttons, and delete into a
  chosen destination, with each change reporting how many tasks moved and how
  many became claimable. Every mutation names the columns the caller actually
  saw and is refused as stale rather than merged when they have changed; the
  board's columns are never rebuilt live under a reader — a standing notice
  offers the reload. The first cut opened as a half-height sliver over the
  board and became a page of its own before release (#92, #93, #94). The
  reorder answers a drag on `dragenter` as well as `dragover`, so a browser
  engine that switches events when content moves under the cursor still finds
  a panel that answers, and the drop mark survives the leave-event churn a
  cursor crossing child elements produces (#110).
- A task stranded on a status that resolves nowhere can be dragged back onto
  the board: the web unknown-status region's cards are draggable out into any
  column, `status list` bounds its unresolved listing to the first ten IDs
  while keeping the count exact, and the contract is written down — an
  unrecognized status is a mutation refusal, never a corruption claim (#89).
- The web board's columns were sized by a hardcoded six-track grid, so a
  five-column board sat frozen at minimum width until the viewport reached
  about 1640px while a phantom sixth track absorbed the free space. The board
  now creates exactly one track per rendered column, growth begins as soon as
  the columns can use the space, and columns stop growing at 26rem so a wide
  monitor no longer stretches every card across the page (#91).

### Deleted tasks

- Deleted tasks are a hideable "Deleted" column at the end of the board
  instead of a separate page. The header link toggles `/?deleted=1`, so the
  state is bookmarkable and Back works; the column lists tombstones
  newest-first with a Restore button on each card, and dragging works both
  ways — a deleted card dropped on a status column is a restore naming its
  destination and position, and a live card dropped on the Deleted column is
  a delete. The `/deleted` page is gone (#96).
- `workbook restore` gained `--into <status>`, so a deleted task can come
  back into a chosen column instead of the one it died holding, recorded as
  one history entry; the web restore accepts the same choice (#95).

### Comments, attachments and assignments

- Tasks carry a comment thread and an attachment list — comments that can be
  added, edited and removed, attachments that are an uploaded file's bytes or
  a link. Concurrent edits converge, and a task with neither stores exactly
  the bytes it always did. Ceilings are asked as growth — 16 KiB per comment,
  500 comments, 50 attachments, 1 MiB per file, 10 MiB of live files per
  task — so a task carried over a limit by a teammate's change can still be
  edited and, above all, shrunk back under it (#99).
- `workbook update` composes them into single commits: `--comment`,
  `--edit-comment`, `--remove-comment`, `--attach-file`, `--attach-url` with
  `--attach-label`, and `--remove-attachment`, alongside `--status` and the
  rest; `workbook show` prints the thread and the list, and
  `show --get-attachment` writes a file's bytes out. Comment and attachment
  IDs take any unambiguous prefix, the same contract task IDs have (#100).
- Tasks can be assigned. An assignment names a principal with an optional
  agent label and records who assigned it and when; two clones assigning the
  same task both survive as a legible both-assigned state, and an assignment
  can only be removed by the person it names or whoever recorded it — a
  removal by anyone else is recorded and changes nothing, on every clone
  (#98). `--assign self` claims in one atomic commit with a status change,
  `workbook next` skips tasks held only by others and says so when everything
  eligible is held, `next --claim` picks and assigns in one step, and
  assigning over somebody else's hold is refused — exit 10, naming the holder
  and `--force`, which records the assignment beside theirs (#101).
- The web task page gained the comment thread and the attachment list —
  add, edit, remove, upload, link, download. These panels are deliberately
  not optimistic: identity and attribution come from the recorded operation,
  so each panel disables, sends, and draws the answer. Only GIF, JPEG, PNG
  and WebP are served inline; every other type, including every spelling of
  SVG, downloads as an opaque attachment (#102). The board and task page also
  show who holds a task — assignee chips on held cards, an Assignments
  section on the task page — derived through the same functions the terminal
  prints, so the surfaces cannot drift (#104).
- The New Task form stages attachments before the task exists. Files and
  links are checked against the same ceilings the server enforces before
  anything uploads; a create whose attachments partially fail holds the form
  with per-row reasons and a Retry attachments button bound to the task it
  created, and the panel refuses changes while a run is walking it (#106).
- Both attachment surfaces accept drag and drop, through the same pre-checks
  as picked files. A refused file says why while the drag is still over the
  zone — a refused drop is cancelled by the browser and never delivered, so
  the drop handler was never the place to say it; a dropped folder is named
  rather than staged and failed; and a file dropped near the board can no
  longer move an unrelated card, nor navigate the page away and destroy
  staged work (#109).

### Markdown

- Task descriptions and comment bodies render as markdown in the browser:
  headings, emphasis, code, lists, blockquotes, http and https links, and
  images. The description opens rendered with an Edit control that swaps the
  textarea in. Images are written `![alt](attachment:<id>)` and resolve only
  against the task's own attachments — every other image target, including
  external URLs, is drawn as text, so a board never reports its readers to
  whoever wrote a task. Anything outside the subset is drawn as the
  characters that were typed (#103).

### Web board drag and scroll

- A board column taller than its viewport could only be reordered a few
  places per gesture. A column now scrolls while a card is held near its
  edge, ramping with depth, and keeps scrolling at full speed when the card
  is pushed past its end; the drop line is recomputed as the cards move, so
  the card lands where the line shows. This round also fixed drops being
  silently lost after a column had autoscrolled — the browser stops sending
  `dragover` when content churns under a still cursor, which cancelled the
  drop entirely (#105).
- A card dragged toward a column off the side of the window carries the
  board with it, both axes at once from one held cursor, with the edge zones
  measured from the window the reader can actually reach — which matters
  under large default fonts and narrow windows, where the document itself
  scrolls sideways (#108).
- The board's header holds its shape: Board and Statuses sit by the heading
  as links, the display controls are labeled toggle switches with pinned
  widths, and the updated-time no longer shoves them around as it changes
  (#107).

### Compatibility across versions

- History written by a newer Workbook is refused with an upgrade signal
  instead of a corruption claim. Reads still serve the task from its stored
  checkpoint with an advisory, mutations of that one task are refused with
  the new `newer-writer` category and exit 9 saying to upgrade, and fetch,
  sync and push keep working — nothing wedges, other tasks are unaffected,
  and unpublished local work publishes itself on the first sync after
  upgrading (#97). Assignments, comments and attachments are the first
  writers to raise the format generation, so this boundary is now guarded in
  both directions going forward. A clone still on v0.4.4 predates the signal
  and reports a task carrying them as corrupt-data — which an upgrade, not a
  repair, resolves; tasks without them stay byte-identical everywhere.

## v0.4.4 — 2026-08-10

Joining an existing project no longer forks it, the board tells the truth about
what it could and could not do, and the measurement harness stops leaking the
processes it prices — from fourteen reviewed pull requests.

### Setup and project identity

- `workbook setup` asks `origin` before minting a project identity. A checkout
  cut before the project adopted Workbook carries no tracked configuration, and
  setup used to read that as a brand-new project — splitting the repository
  into two identities and wedging it behind a guard mismatch that no
  working-tree cleanup could reach. Setup now adopts the configuration
  committed on origin's default branch, stops rather than guessing when origin
  holds task refs but no committed configuration, and treats an unreachable
  origin as an error instead of minting blind; `--no-sync` remains the
  deliberate local-only bootstrap. The guard-mismatch error names both files,
  both identities, and the recovery, which is safe because the projection
  validates its stored project ID and rebuilds itself (#66).

### Web board

- A refused write is no longer re-based onto a head the server just refused.
  When the forced refresh after a stale-write refusal cannot read the board,
  the queue stops and reports instead of sending every queued intent to the
  same refusal; when only the relationship context was superseded but the
  board read landed, the queue re-bases and proceeds. The outage message
  appears only when the read genuinely failed (#70).
- A detail save whose form was left before it landed reports its outcome
  instead of being swallowed. The refusal is named in the notice and staged on
  the task's own form, the server's reason lands last, and a save that lands
  while detached no longer yanks the reader back to the board — which used to
  silently destroy a New Task draft being typed (#73).
- An editor whose task was deleted elsewhere is told so. The refused-save
  message used to invite a doomed retry against a version the server no longer
  holds, and the failure notice linked to a task page that no longer exists;
  both now state the deletion, and the edits stay in the form to copy out
  (#76).
- The withdrawal that corrects an open form after a refused board change no
  longer claims the unedited fields show the server's version when the forced
  refresh could not read the board — the form and the card are now written
  against the same fact (#78).
- A new task drafted in a form the reader left before the create landed is no
  longer lost in silence. A refused detached create reports into the notice
  with a **Restore draft** button that brings back every typed field — saying
  plainly that staged relationships are not restored — and a create that lands
  while detached leaves the reader on the route they chose (#79).

### Command line and sync reporting

- `workbook config unset --json` reports `"command":"config unset"` instead of
  claiming to be `config set`, and a test holds the two verbs apart so they
  cannot drift back together (#67).
- An ignored ref under origin's task namespace now reaches the surfaces where
  a user actually meets one: `workbook setup` names the skipped refs beneath
  its `Sync:` line, an inline mutation's report carries them, and the sync
  watcher announces each newly skipped ref once on its terminal and carries
  the set in `workbook sync --status`, text and JSON alike. The report keeps
  its restraint: removal advice is offered only for names no project's ID
  format could have produced (#69).

### Performance harness and targets

- The streaming history read's memory bound is pinned by a test. A rewrite
  that buffers the whole corpus before delivering it — the exact regression
  the streaming work exists to prevent — used to leave the suite green and
  was caught only by an off-repo bench run; it now fails an allocation
  ceiling asserted on settled live heap (#68).
- The local-bare sync scenarios' tracking-ref reset was filed for deletion as
  defensive dead code and turned out to be load-bearing: each sample's second
  measured sync populates the tracking namespace, and leaving it uncleared
  inflates the initial-sync measurement by about a quarter at 500 tasks. The
  reset is kept, pinned by a test that fails without it, and documented with
  the measurement (#71).
- The measurement harness reliably reaps the processes it spawns. The helper
  that priced commands used to signal their process group only on
  cancellation, so a command that exited normally could leave a busy-loop
  descendant spinning at a whole core for days — observed once for a week.
  Every measured group is now killed after its sample is stamped, at the
  shared helper and at every remaining `Setpgid` exec site, and the tests
  reap their own helpers even if the test binary dies mid-run (#72, #77).
- Read-path scenarios carry approved duration targets: `cli-list` joins
  `cli-show` on the 200 ms local budget, fetch-first reads like `cli-next`
  carry the 1,000 ms synchronized budget as recorded policy, and the stale
  100 ms warm `api-update` figure in the README now reads 150 ms everywhere
  it speaks in the present tense (#74, #75).

## v0.4.3 — 2026-08-08

Reports you can actually read — on the board, on the terminal, and about an
ignored ref — plus a hardened watcher socket and a stricter board `Host` check,
from thirteen reviewed pull requests.

### Web board

- A refused change is now reported on the card it concerns and stays there until
  you dismiss it, change that card again, or the board stops carrying the card.
  The one-second poll used to erase the banner a refused drag wrote before
  anyone could read it; when the card leaves the board the report moves to the
  notice above it and names the task (#55).
- A refused board change no longer discards what you have typed into an open
  detail form. Untouched fields follow the task the board holds, edits in
  progress stay as typed with the caret where it was, and a save in flight still
  reports into the form (#61).
- A card leaving a column no longer disturbs the cards below it. Departed cards
  are swept before the columns are reconciled, so another clone moving one card
  out of a long column no longer re-inserts — and blurs — every card under it
  (#57).
- The board and the task form are more compact: columns hold a minimum width and
  the board scrolls sideways instead of squeezing titles over four lines, column
  headers drop the `refs/workbook/status/…` line, card titles are no longer
  underlined, the Depends On and Blocks groups and the form footer are shorter,
  **Create more** sits directly above **Save**, and the Labels caption is aligned
  with its input (#50).

### Command line

- `workbook show` keeps a description's line structure instead of collapsing it
  to one line. Later lines are indented by a tab, which preserves the guarantee
  the collapse provided: no description line can be read as one of `show`'s own
  fields (#54).
- `workbook serve` says why the board moved when the default `127.0.0.1:7331` is
  taken, naming the collision on its own line above the board banner instead of
  falling back silently. An explicit `--addr` still fails rather than moving
  (#53).
- `fetch`, `push`, and `sync` no longer end an ignored-ref report with blanket
  `git push origin --delete` advice. Each ignored ref is classified on its own
  line as `no project's task` or `may be another Workbook's task`; every ref is
  reported as kept on `origin`, and the removal command is offered only for names
  no project's ID format could have produced (#62).

### Hardening

- The sync watcher's Unix socket is bound in a private per-user directory rather
  than world-writable `/tmp`. Candidate directories are refused unless they are
  real directories owned by the caller and writable by nobody else, the socket is
  created `0600` with no world-connectable window, both ends of the watcher
  protocol bound the line they read, and the watcher bounds the status it serves.
  Exclusivity survives a change of socket path, so an older watcher still keeps
  ownership across the upgrade (#51).
- The board's `Host` check now pins the bound address on an explicit non-loopback
  bind — `--addr 192.168.1.5:7331` is named by that address alone — and the
  `Origin` check is measured against the bound address too, so a page that
  rebinds its own DNS name to a LAN board no longer holds same-origin read and
  write on it. A wildcard bind is the one case with no host to pin; the exposure
  warning now names drive-by access from off the network, and the README says so
  where the claim is made (#58).

### Agent skill and tests

- The Workbook agent skill says where a dependency's title comes from: resolve
  each `data.dependencies` entry with `workbook show <id> --json`, never invent a
  title, and report a dependency this clone cannot read by ID instead of
  abandoning the task. Bad news — a blocked task, a failed command — is announced
  by title like everything else. The behavioral run behind the change is recorded
  under `docs/superpowers/evidence/` (#52).
- A capability probe that skips without the marker is now detected structurally
  rather than by convention, so coverage cannot quietly disappear on a machine
  missing a tool while CI reports success (#59).
- The board's card-signature fast path and the detail form's Blocks-edge
  orientation guard are covered by tests that fail when the line they protect is
  deleted; both previously left the package green (#56, #60).

## v0.4.2 — 2026-08-08

Quality and hardening for the web board, plus safety limits on task storage,
from nine reviewed pull requests.

### Web board

- Saving a new task returns you to the board, and a **Create more** toggle
  keeps a clean form open for the next task instead (#41).
- New tasks land on the board optimistically: the card renders immediately
  while the save is still in flight, and a refused save offers the draft back
  instead of losing it (#46).
- Card descriptions are hidden by default to keep columns scannable; a board
  setting restores the previous behavior (#38).
- Labels are edited as removable chiclets instead of one comma-separated text
  field (#40).
- Tasks whose status matches no column appear in an explicit unknown-status
  section, matching the terminal board, instead of being hidden (#42).
- The dependency search popup closes when it loses focus (#47).

### Limits and hardening

- Task fields are bounded — title 500 bytes, description 64 KiB, labels
  100 bytes each and 50 per task — and Workbook refuses to read a Git object
  over 4 MiB, so one oversized task can no longer exhaust memory in every
  clone. Web request bodies are capped at 1 MiB and the board server gains
  connection timeouts. The limits are documented in the README (#44).
- The web handler is built from a named options struct, and serve-level tests
  fail if the delete/restore or depend/free wirings are ever swapped (#43).

### Performance and tooling

- The benchmark harness measures the agent hot loop (`workbook next`,
  `workbook show`), warm board reads, and the sync watcher's steady-state CPU
  and memory; a replacement `api-update` p95 target is proposed with evidence
  under `docs/performance/` (#45).
- `go test -short ./...` is the supported fast suite for local iteration and
  parallel agents; the release and installer build tests share the ambient Go
  build cache instead of cold-compiling everything per test (#39).
