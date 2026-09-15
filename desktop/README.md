# Workbench

Workbook's desktop app. Point it at a folder, pick which repositories to
import, and get one window with every project's board in a sidebar.

Workbook is repository-native: each project's tasks live in that repository's
`refs/workbook/*`, and `workbook serve` binds one board to one checkout.
Workbench does not change that. It discovers repositories, bootstraps the ones
you choose, and runs a real board per project behind a single window.

It lives in this repository so the shell and the CLI it ships are one thing:
the app is built from the same checkout as the CLI it bundles, and there is no
pinned revision to keep in step.

## Running it

```
cd desktop
npm install
npm start
```

A development run drives the CLI staged under `build/` by `npm run stage` when
there is one, and otherwise whatever `workbook` is installed on the machine
(see "Finding the workbook binary" below). `npm run dev` also opens the
shell's developer tools.

## Checks

```
npm run check
```

`check-styles.js` verifies every class the renderer applies has a rule, and
`check-shell.js` verifies every source file parses, every export is defined,
the renderer references only element ids that exist, and the channels the
preload invokes are the channels the main process handles. They are cheap and
each catches a mistake this project has actually made.

## Building

```
npm run dist         # macOS arm64
npm run dist:mac     # macOS, arm64 + x64
npm run dist:linux   # Linux, x64 + arm64 (AppImage + deb)
npm run dist:win     # Windows, x64 + arm64 (NSIS)
```

Each build:

1. **`npm run stage`** builds the Workbook CLI from this checkout and stages it
   under `build/`. It delegates to the repository's own `scripts/install.sh`
   rather than calling `go build` here, so the binary is stamped exactly as an
   official source install is: `-trimpath`, with version and commit from
   `git describe`. Requires Go and Git.
2. **`electron-builder`** packages the app with that binary in `Resources/`,
   ad-hoc signing the macOS bundle in an `afterPack` hook. That has to happen
   *during* packaging: signing the leftover `.app` afterwards fixes nothing,
   because the DMG and ZIP were already built from the unsigned bundle. macOS
   on Apple Silicon refuses to launch an arm64 bundle whose signature
   repackaging invalidated.

So installing the app installs a matching Workbook. There is no separate CLI
install step, and no dependency on what happens to be on the machine.

### Building against another revision

`scripts/build-workbook.sh` takes two overrides:

- `WORKBOOK_REPO=<path>` builds from that checkout exactly as it stands. The
  script never changes the checked-out revision of a working tree.
- `WORKBOOK_REF=<ref>` builds that ref instead of the working tree. With no
  `WORKBOOK_REPO`, this checkout is cloned into `build/workbook-src` and the ref
  is checked out there. This is how a release build names a tag.

The script also takes an output directory as its one argument, which is how
the repository's test suite keeps its builds out of the tree.

## Releasing

Desktop releases are cut from this repository under `desktop-vX.Y.Z` tags,
separately from the CLI's `vX.Y.Z` releases, and a rolling `desktop-latest`
release carries the current installers at a fixed address. The workflow that
does this is a separate task; until it lands, the checked-in version is
`0.0.0` and no release exists for the app to find.

## Updating

**Windows** uses electron-updater's native flow: download in the background,
install on restart.

**macOS does not.** These builds carry an ad-hoc signature rather than a
Developer ID one, and Squirrel.Mac silently refuses to install over a bundle it
cannot verify: `quitAndInstall` returns having done nothing, and the user
believes they upgraded. So the Mac path never calls it. It downloads the DMG
itself and opens it in Finder for a drag into Applications, which is what the
user did to install in the first place.

Checks run once on launch, and on demand from the menu, but the launch check
never opens a dialog. A native dialog is application-modal: while one is open
the app cannot quit and Cmd+Q does nothing, so an update prompt six seconds
after launch, landing behind the window or on another Space, makes the app look
hung. The automatic check marks the menu instead; dialogs are shown only in
answer to something the user asked for.

The app is unsigned by any identity and unnotarized, so Gatekeeper will need
it opened once from the Finder context menu.

## Finding the workbook binary

An explicit override first, then **the build bundled in the app**, then a
build staged under `build/`, then `PATH`, then `~/.local/bin`, `~/go/bin`,
`/opt/homebrew/bin`, `/usr/local/bin`.

The bundled build wins because the app ships it: that makes an install
self-contained, and makes the app's behavior a property of the app rather than
of the host, which is what makes a bug report reproducible. A development run
(`npm start`) has no bundled copy, so it takes the staged build next: "stage,
then start" tests the shell against the CLI from this checkout, which is what
a change to both sides needs. The rest are fallbacks for a run with no stage.

This does mean a packaged app and your terminal can drive different builds if
your installed CLI is older. The sidebar's menu names the binary in use.

## How the boards run

Each imported project gets its own `workbook serve` child process, supervised
by `src/main/supervisor.js`, and its own `WebContentsView` loading that
server's real address. That is what keeps Workbook's same-origin guard
satisfied: the Host header names the address the listener bound, and the
Origin the board sees is its own. A proxy or an iframe would break one or
both, and the board's `frame-ancestors 'none'` rules the iframe out anyway.

Views are kept warm so switching back does not reload the board or lose an
open task form; memory grows with the number of boards opened in a session.
Board servers are recorded in `running-boards.json` and any left by a run that
ended without warning, a crash or a Force Quit, are stopped at the next start,
since no in-process handler can cover a SIGKILL.

The boards draw their own dark mode, and the choice is theirs. The Dark Mode
switch on any board sets the whole window: a small preload in each board view
reports the click to the main process, which repaints the shell and tells
every other board to align by clicking its own switch. The board's rule that
choosing the scheme your system already shows means "follow the system" is
kept, so that state is reachable the way it always was, and a board opened
later starts in the current mode. The shell has no appearance control of its
own.

## Importing

**Adopt, don't re-setup.** A repository that already carries a Workbook
identity is registered from that identity and `setup` is never run: its key
cannot be changed, and `setup` also rewrites the managed agent documentation
in the checkout, which adding a repository to a list should not do to a
checkout you configured yourself. Only a repository with no identity yet is
bootstrapped, with `--no-sync`, so importing never pushes anything.

**Project keys are collected up front.** A key is minted once and cannot be
changed afterwards without deleting `refs/workbook/project` and
`refs/workbook/config` and removing `.git/workbook` and `.workbook` by hand.
The wizard suggests one per repository, validates it against Workbook's own
`^[A-Z][A-Z0-9]{1,9}$`, and lets you edit it before anything is written.

## Platform support

| | Status |
| --- | --- |
| macOS (arm64, x64) | Builds. Ad-hoc signed, not notarized. |
| Linux (x64, arm64) | Builds as AppImage and deb. |
| Windows (x64, arm64) | Builds as an NSIS installer; unsigned until the publish workflow adds Azure Trusted Signing. |

## Known gaps

- The bundled binary is built at package time and does not update afterwards;
  a newer Workbook means a newer app.
- Building requires Go and this checkout. There is no fallback to downloading
  a released CLI.
- Projects can be forgotten but not renamed from the UI.
- Workbook's CLI JSON envelope and HTTP routes are not versioned as a public
  contract, so a change to either can break this; living in the same
  repository is what keeps them in step.
