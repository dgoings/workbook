# Changelog

A release pull request adds an entry here before the release is cut, and the
bump label it carries has to agree with the version this file names. See
[Releasing](README.md#releasing) for how the two are checked against each
other.

Patch releases may be cut without an entry, so this file records the releases
worth describing rather than every release. Every release, described here or
not, has notes on the
[GitHub Releases page](https://github.com/dgoings/workbook/releases).

Releases through v0.4.1 predate this file.

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
