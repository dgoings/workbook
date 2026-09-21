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

## The sidebar

The sidebar collapses to a narrow rail, with the chevron in its header or Cmd+B
on macOS and Ctrl+B elsewhere; the chord works from a board too, not only from
the shell. The choice is remembered across launches. The rail keeps every route
it had: the import glyph, and each project as its key with its status dot, with
the name and path on the tile's tooltip.

## Checks

```
npm run check
```

`check-styles.js` verifies every class the renderer applies has a rule, and
`check-shell.js` verifies every source file parses, every export is defined,
the renderer references only element ids that exist, and the channels the
preload invokes are the channels the main process handles. They are cheap and
each catches a mistake this project has actually made.

That one command also runs the tests, which is why it is the only one listed:
CI runs `npm run check` and nothing else for the desktop app, so anything
outside it would never run. `npm test` on its own is Node's runner over `test/`
when you want the tests without the linters — it covers the main-process
modules that can be exercised without Electron: the registry's stored state and
the PATH install's copy, block writer, profile targets and Windows registry
edit. Every case runs against a temporary directory, so no test reads or writes
your real HOME, your profiles, or the app's userData, and nothing in the suite
launches Electron.

## Building

```
npm run dist         # macOS arm64
npm run dist:mac     # macOS, arm64 + x64
npm run dist:linux   # Linux, x64 + arm64 (AppImage + deb)
npm run dist:win     # Windows, x64 + arm64 (NSIS)
```

Windows ships one installer per architecture, `Workbench-Setup-x64.exe` and
`Workbench-Setup-arm64.exe`, and no combined one: `buildUniversalInstaller` is
off, because a third installer carrying both would double the download for
everyone to spare one choice.

Each build:

1. **Staging** builds the Workbook CLI from this checkout with the repository's
   own `scripts/install.sh` rather than calling `go build` here, so the binary
   is stamped exactly as an official source install is: `-trimpath`, with
   version and commit from `git describe`. Requires Go and Git.

   There is one CLI per target, not one for all of them: `GOOS` and `GOARCH`
   reach `go build` through `install.sh`, so
   `GOOS=windows GOARCH=arm64 scripts/build-workbook.sh build/windows-arm64`
   stages that target's CLI beside the others, under `build/<goos>-<goarch>/`.
   The binary is named from `GOOS` — `workbook.exe` for Windows — and the
   banner that prints its version is skipped for a target that is not the host,
   which cannot be run to ask it.

   The Mac builds stage both architectures this way with `npm run stage:mac`,
   because they package both. `npm run stage` stages only the host's, under
   `build/`; that is the one a development run (`npm start`) uses.
2. **`electron-builder`** packages the app, and its `afterPack` hook puts the
   CLI matching that bundle's own platform and architecture into `Resources/`:
   from `build/<goos>-<goarch>/` when one was staged there, and otherwise from
   `build/`, the host-only stage. The hook does this rather than
   electron-builder's `extraResources`, which copies one named file into every
   bundle and so gave both architectures of a Mac build the host's binary.

   The hook then ad-hoc signs the macOS bundle, after the copy, since the
   resources are part of what gets signed. Signing has to happen *during*
   packaging too: signing the leftover `.app` afterwards fixes nothing, because
   the DMG and ZIP were already built from the unsigned bundle. macOS on Apple
   Silicon refuses to launch an arm64 bundle whose signature repackaging
   invalidated.

   `npm run dist:linux` and `npm run dist:win` expect to run on a host of that
   platform, which is how the release workflow runs them. Building one from a
   Mac needs its targets staged into `build/<goos>-<goarch>/` first: otherwise
   the Windows build stops on a missing `workbook.exe`, and the Linux one would
   take the host-only stage.

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
release carries the current installers at a fixed address, which is the address
the download links and the update checks below are pinned to.

`.github/workflows/desktop-release.yml` does this. A pushed `desktop-v*` tag
builds on a macOS, a Linux and a Windows runner, because each installer can
only be made on its own platform; each build stages the newest CLI release
reachable from the released commit — the one just published when a CLI release
cascaded into this, and the last CLI release when the desktop tag was cut by
hand — for every architecture its bundles cover, and stamps the package's
version from the tag. The version checked in here stays `0.0.0`: the tag says
what shipped, so nothing has to be bumped in git.

Every CLI release cascades into a desktop one, and a desktop-only release is
cut by pushing a `desktop-vX.Y.Z` tag on `main` yourself. CONTRIBUTING's
"Desktop releases" has both paths, and what the two releases each hold.

## Updating

Every release publishes on the `latest` channel, pre-release or not. JSON has
no comments, so the reason for `detectUpdateChannel: false` in `package.json`
lives here: electron-builder otherwise reads the channel out of the version, so
a `0.6.0-rc1` build would write `rc1-mac.yml` and no `latest-mac.yml` at all.
The site link and the update check both follow the newest desktop release,
which is whatever `desktop-latest` currently holds, so they would find nothing
there.

**Windows** uses electron-updater's native flow: download in the background,
install on restart.

**macOS does not.** These builds carry an ad-hoc signature rather than a
Developer ID one, and Squirrel.Mac silently refuses to install over a bundle it
cannot verify: `quitAndInstall` returns having done nothing, and the user
believes they upgraded. So the Mac path never calls it. It downloads the DMG
itself and opens it in Finder for a drag into Applications, which is what the
user did to install in the first place.

Checks run once on launch and log what they find. There is no update action in
the shell yet: when one returns it will live in the native application menu
rather than on the shell page. The menu also reaches a user who is looking at a
board rather than at the sidebar.

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
your installed CLI is older. The version line in the sidebar names the build in
use on its tooltip: bundled or installed, and the path it was found at.

## Putting workbook on your PATH

On launch the app copies its bundled `workbook` into a directory it owns and
appends that directory to your PATH, so the CLI the app ships is also the CLI
you can type. The copy is kept current by content — size, then a sha256 of both
files, never the mtime, which travels with a download and says nothing — so an
app update replaces it and an unchanged launch writes nothing at all.

Where the copy lives:

| Platform | Directory |
| --- | --- |
| macOS | `~/Library/Application Support/Workbench/bin` |
| Windows | `%LOCALAPPDATA%\Workbench\bin`, or `~\AppData\Local\Workbench\bin` when `LOCALAPPDATA` is unset |
| Linux | `$XDG_DATA_HOME/workbench/bin`, or `~/.local/share/workbench/bin` |

On macOS and Linux the directory is added by a marked block written into every
one of `~/.bashrc`, `~/.zshrc` and `~/.profile` that exists — and into
`~/.profile`, created, if none of them does. The markers are the app's own, not
the ones `scripts/setup-dev-env.sh` writes, so a profile can carry both blocks
and neither will delete the other:

```sh
# >>> workbench app PATH >>>
case ":${PATH}:" in
	*':/Users/you/Library/Application Support/Workbench/bin:'*) ;;
	*) PATH="${PATH}:"'/Users/you/Library/Application Support/Workbench/bin' ;;
esac
export PATH
# <<< workbench app PATH <<<
```

The directory is in *single* quotes, and `${PATH}` alone is left expanding.
Double quotes would still run a `$(…)` or a backtick inside the directory name
the moment the profile was sourced; single quotes make it one literal word.

fish is not POSIX — `PATH="${PATH}:x"` is a syntax error there — so
`~/.config/fish/config.fish` gets the same thing in fish's own syntax:

```fish
# >>> workbench app PATH >>>
if not contains '/Users/you/Library/Application Support/Workbench/bin' $PATH
    set -gx PATH $PATH '/Users/you/Library/Application Support/Workbench/bin'
end
# <<< workbench app PATH <<<
```

That file is only written when fish is indicated — `~/.config/fish` already
exists, or `$SHELL` ends in `fish`. The app does not invent a fish
configuration for someone who does not use fish. On Windows there are no
profiles: the user PATH in `HKCU\Environment` is edited instead, with `reg add`
rather than `setx`, which truncates a PATH longer than 1024 characters.

A profile that is a symlink is followed, and the real file behind it is the one
edited. That is very often a dotfiles repository — `~/.zshrc` and
`~/.config/fish/config.fish` are symlinks into one on this project's own
machine — so the block can turn up as an uncommitted change in a repository
you track, rather than in your home directory. The startup log names the
resolved path for each file it wrote, so it says which file to go and look at.

**A shell already running does not see any of this.** On macOS and Linux, open
a new terminal once. **On Windows a new terminal is not enough:** `reg add`
changes the stored user PATH but cannot broadcast the `WM_SETTINGCHANGE` that
tells running processes to re-read it, so Explorer keeps handing every terminal
it launches the environment it cached — sign out and back in, or restart
Explorer. The app says which of the two you need, once — on the first launch
that puts the directory on your PATH without a single failure. A launch that
managed some of your profiles and not others stays quiet and logs what it could
not write, since a notice about PATH would be false for the shell it missed and
there is no second one to correct it.

The directory is *appended*, not prepended, which is the one difference from
`setup-dev-env.sh`. An existing `workbook` earlier on your PATH — a Homebrew
install, a `go install` build in `~/go/bin` — therefore keeps winning, and this
never silently takes over a CLI you manage yourself.

To undo it: delete the marked block from each profile it is in (on Windows,
remove the directory from your user PATH), and delete the directory. Nothing
else refers to it — but the next launch of a packaged app puts both back, since
undoing it by hand looks exactly like a machine that has never had it. To undo
it and keep it undone, set `WORKBENCH_SKIP_PATH_SETUP` as well.

A development run does nothing at all: `npm start` has no bundled binary under
`process.resourcesPath` to copy, so the install skips, and your own profiles are
left alone. `WORKBENCH_SKIP_PATH_SETUP=1` turns the whole thing off explicitly,
for a packaged app as well — but it is read from *the app's own* environment,
and a macOS app launched from the Finder or the Dock inherits nothing from your
shell rc: exporting it in `~/.zshrc` has no effect on the app you double-click.
Launch the app from a terminal that has it set, or set it for the login session
with `launchctl setenv WORKBENCH_SKIP_PATH_SETUP 1`.

Failures never block startup. A read-only `.zshrc` costs you that one profile,
not the binary copy and not the window: every step's failure is collected, the
rest still runs, and what went wrong is logged.

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
| Windows (x64, arm64) | Builds one NSIS installer per architecture, and no combined one; unsigned until the publish workflow adds Azure Trusted Signing. |

## Known gaps

- The bundled binary is built at package time and does not update afterwards;
  a newer Workbook means a newer app.
- Building requires Go and this checkout. There is no fallback to downloading
  a released CLI.
- Projects can be forgotten but not renamed from the UI.
- Workbook's CLI JSON envelope and HTTP routes are not versioned as a public
  contract, so a change to either can break this; living in the same
  repository is what keeps them in step.
