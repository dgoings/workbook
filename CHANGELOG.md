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
