# Workbench

A desktop shell for [Workbook](https://github.com/dgoings/workbook). Point it at
a folder, pick which repositories to import, and get one window with every
project's board in a sidebar plus a merged queue across all of them.

Workbook is repository-native: each project's tasks live in that repository's
`refs/workbook/*`, and `workbook serve` binds one board to one checkout.
Workbench does not change that. It discovers repositories, bootstraps the ones
you choose, and runs a real board per project behind a single window.

## Running it

```
npm install
npm start
```

## Building

```
npm run dist         # macOS, this machine's architecture
npm run dist:mac     # macOS, arm64 + x64
npm run dist:linux   # Linux, x64 + arm64 (AppImage + deb)
```

Each build:

1. **`npm run stage`** builds the Workbook CLI from source and stages it. It
   delegates to Workbook's own `scripts/install.sh` rather than calling
   `go build` here, so the binary is stamped exactly as an official source
   install is — `-trimpath`, with version and commit from `git describe`.
   Reimplementing that would drift from upstream. Requires Go.
2. **`electron-builder`** packages the app with that binary in `Resources/`,
   ad-hoc signing the macOS bundle in an `afterPack` hook. That has to happen
   *during* packaging: signing the leftover `.app` afterwards fixes nothing,
   because the DMG and ZIP were already built from the unsigned bundle. macOS
   on Apple Silicon refuses to launch an arm64 bundle whose signature
   repackaging invalidated.

So installing the app installs a matching Workbook. There is no separate CLI
install step, and no dependency on what happens to be on the machine.

### The pinned Workbook

The CLI version is pinned in `package.json`:

```json
"workbook": { "repository": "https://github.com/dgoings/workbook.git", "ref": "v0.5.1" }
```

CI clones that ref, so a change upstream can never silently alter a release.
Bump the pin deliberately. A local checkout is used as-is when `WORKBOOK_REPO`
points at one — this script will not change the revision of someone's working
tree — and `WORKBOOK_REF` overrides the pin.

## Releasing

Push a tag:

```
git tag v0.1.0 && git push origin v0.1.0
```

`.github/workflows/release.yml` builds macOS (arm64 + x64) and Linux
(x64 + arm64) and publishes one GitHub Release **in this repository**. Its feed
files — `latest-mac.yml`, `latest-linux.yml` — are what the in-app updater
reads, so the release is the update channel; there is no separate releases repo
to keep in step.

Each platform uploads into a draft, which is right while two jobs are writing to
one release: nobody should download a release that is half a platform. A final
job marks it published once every platform has uploaded. That step is not
cosmetic — a draft is invisible to the `releases/latest` endpoint the updater
reads, so a release left as a draft is one no running copy of the app can see.

Windows installers are signed through Azure Trusted Signing when these
repository secrets are set, and are simply unsigned when they are not:
`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`,
`AZURE_CODE_SIGNING_ACCOUNT`, `AZURE_CERTIFICATE_PROFILE`, `AZURE_ENDPOINT`.

## Updating

**Windows** uses electron-updater's native flow: download in the background,
install on restart.

**macOS does not.** These builds carry an ad-hoc signature rather than a
Developer ID one, and Squirrel.Mac silently refuses to install over a bundle it
cannot verify — `quitAndInstall` returns having done nothing, and the user
believes they upgraded. So the Mac path never calls it. It downloads the DMG
itself and opens it in Finder for a drag into Applications, which is what the
user did to install in the first place. The same approach plus2win uses, for the
same reason.

Checks run once on launch, and on demand from the menu — but the launch check
never opens a dialog. A native dialog is application-modal: while one is open
the app cannot quit and Cmd+Q does nothing, so an update prompt six seconds
after launch, landing behind the window or on another Space, makes the app look
hung. The automatic check marks the menu instead; dialogs are shown only in
answer to something the user asked for.

The app is unsigned by any identity and unnotarized, so it is for local use.
Gatekeeper will need it opened once from the Finder context menu.

## Finding the workbook binary

An explicit override first, then **the build bundled in the app**, then `PATH`,
then `~/.local/bin`, `/opt/homebrew/bin`, `/usr/local/bin`.

The bundled build wins because the app ships it: that makes an install
self-contained, and makes the app's behaviour a property of the app rather than
of the host, which is what makes a bug report reproducible. The fallbacks are
what a development run (`npm start`) uses, since there is no bundled copy there.

This does mean a packaged app and your terminal can drive different builds if
your installed CLI is older. The sidebar shows the resolved version, marks it
`· installed` when it is not the bundled one, and names the full path in its
tooltip — so the two never disagree silently.

## How it fits together

```
Electron main ─ supervises ─→ workbook serve   (repo A)  → 127.0.0.1:7331
   │                          workbook serve   (repo B)  → 127.0.0.1:auto
   │  registry.json: {path, key, name} per project
   │
   ├─ shell:  sidebar, import wizard, merged queue
   └─ WebContentsView per project → that board's real address
```

| File | Role |
| --- | --- |
| `src/main/main.js` | Window, views, IPC, theme |
| `src/main/supervisor.js` | One `workbook serve` child per project |
| `src/main/discovery.js` | Folder scan, key suggestion and validation |
| `src/main/registry.js` | Imported projects, persisted |
| `src/main/workbook.js` | Locating and driving the CLI |
| `src/main/boardtheme.js` | Per-project dark ramp derivation |
| `src/main/people.js` | People directory and identity merging |
| `src/renderer/` | Shell UI, and the board dark overlay |

## Why it is built this way

**The CLI and the HTTP API are the whole integration surface.** Every Go package
in Workbook is under `internal/`, which the language forbids other modules from
importing. There is no way to link its board handler into another program
without forking it, so Workbench drives the binary instead: `--json` for reads,
and each board's own HTTP API for everything the board does.

**A process per repository, not a proxy.** `workbook serve` binds itself to the
repository at its working directory, and the board's same-origin guard pins the
`Host` header to the address that listener actually bound. Running the real
server per project and pointing a view at its real address satisfies the guard
exactly. A reverse proxy under a path prefix would break it, and would also
break the board's absolute `/api/...` paths.

**A view per board, not an iframe.** The board sends
`frame-ancestors 'none'`, so it cannot be framed. Each board gets its own
`WebContentsView` — a top-level contents, which that directive does not apply
to. Views for projects that are not showing are parked at zero size rather than
detached, so switching back does not reload the board.

**Writes are safe alongside your terminal.** Workbook's durable writes are Git
ref compare-and-swaps, so a board mutating tasks while a coding agent runs
`workbook update` in the same repository is atomic — conflicts fail loudly
rather than corrupting. The SQLite projection re-reads ref tips on every list,
so neither side serves the other stale data.

**Import never syncs.** Bootstrapping runs `workbook setup --key <key>
--no-sync`. Adding a repository to a list should not push refs to its remote as
a side effect; sync is a per-project choice made afterwards from the board.

**An already-configured repository is adopted, not bootstrapped.** The scan
reads each repository's `.workbook/config.json`, so a checkout that already has
an identity is listed with its real key and imported by registering that
identity — `setup` is never run against it. Two reasons: its key cannot change
(`setup` with a different one is refused outright), and `setup` also rewrites
the managed agent documentation and the skill directory, which adding a
repository to a list has no business doing to a checkout you configured
yourself. Only a repository with no identity yet is bootstrapped.

**Project keys are collected up front.** A key is minted once and cannot be
changed afterwards without deleting `refs/workbook/project` and
`refs/workbook/config` and removing `.git/workbook` and `.workbook` by hand. The
wizard suggests one per repository, validates it against Workbook's own
`^[A-Z][A-Z0-9]{1,9}$`, and lets you edit it before anything is written.
Distinct keys are also what make the merged queue readable.

## Theming

Light is Workbook's palette unchanged, taken from its stylesheet: the `--wb-*`
ramp, `ui-rounded` for text and `ui-monospace` for keys and metadata, 3-4px
radii, the `#b9c6d8` hairline. Dark is this app's addition — Workbook has no
dark mode — and moves only token values, so both modes are the same design lit
differently. Auto follows the OS.

The boards are darkened too, by injecting `src/renderer/board-dark.css`. Two
things make that work:

- **Every declaration carries `!important`.** Electron's `insertCSS` injects at
  the *user* origin, which loses to the page's own author rules at equal
  specificity. Without it the sheet parses and does nothing.
- **The primary ramp is derived per project, not overridden.** Workbook lets a
  project choose its board colour. `boardtheme.js` keeps that hue and moves only
  lightness and saturation, so a project themed cyan stays cyan in dark mode
  instead of being flattened to one generic blue.

The overlay is an overlay on someone else's stylesheet and will drift when that
stylesheet changes. It is confined to colour — nothing in it moves a layout, a
size, or a radius — so the worst a drift can do is leave a light patch.

## Platform support

| | Status |
| --- | --- |
| macOS (arm64, x64) | Built and released. Ad-hoc signed, not notarized. |
| Linux (x64, arm64) | Built and released as AppImage and deb. |
| Windows (x64, arm64) | Built and released, using the ported CLI below. |

### Why the CLI is pinned to a fork

Workbook does not compile for Windows at any upstream ref: `internal/historyvalidation`
uses `flock(2)`, `internal/syncloop` reads a uid out of `syscall.Stat_t`, and a
POSIX-only process-group helper sits in a non-test package. Each is genuinely
platform-specific and each needs a build-tagged Windows counterpart.

So the pin names a fork whose branch is upstream's `main` plus that port, and
plus a relative sync indicator for the board header. Both changes are pushed as
their own branches (`feat/windows-support`, `feat/relative-sync-indicator`) and
are meant to go upstream; when they land, the pin moves back to a `dgoings` tag.
The `upstream` and `branches` keys in `package.json` record that intent so the
fork is never mistaken for a permanent divergence.

## Performance notes

Opening the queue used to cost 96 subprocesses across twelve projects. Twelve of
those were `workbook list` — the actual data — and the other 84 rebuilt the
people directory from commit history, every single time, to answer which email
addresses belong to the current user. That answer had not changed since the last
time it was asked.

The directory is cached on the set of projects plus the user's identity
mapping, so importing, forgetting, merging or renaming rebuilds it and nothing
else does. Explicit Refresh re-reads history, which is the one thing the cache
key cannot notice. Reading git identity and bareness/HEAD were also collapsed
into one invocation each.

| | before | after |
| --- | --- | --- |
| `queue:load` | 375ms, 96 subprocesses | ~200ms, 12 |
| `people:list` (warm) | 172ms, 84 subprocesses | 0ms, 0 |
| per-repository git calls | 7 | 5 |

Each open board is a full renderer process, and views are kept warm so switching
back does not reload them. That is deliberate — a reload loses scroll position
and any open task form — but it means memory grows with the number of boards
opened in a session, not the number of projects imported.

Board servers are recorded in `running-boards.json` and any left by a run that
ended without warning — a crash, a Force Quit — are stopped at the next start,
since no in-process handler can cover a SIGKILL.

## Known gaps

- The bundled binary is built at package time and does not update afterwards.
  There is no in-app download or update flow; a newer Workbook means rebuilding
  the app. Workbook publishes releases with a `checksums.json`, so a real
  updater is possible — it is just not wired up.
- Building requires Go and a Workbook checkout. There is no fallback to
  downloading a released artifact when neither is present.
- macOS builds are ad-hoc signed and not notarized, so Gatekeeper needs the
  right-click-Open dance on first launch.
- Projects can be forgotten but not renamed from the UI.
- The merged queue is read-only; writes go through each project's board.
- Workbook's CLI JSON envelope and HTTP routes are not versioned as a public
  contract, so an upstream change can break this.
