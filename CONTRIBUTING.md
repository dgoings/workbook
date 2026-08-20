# Contributing to Workbook

Workbook is developed on a trunk: features merge to `main` continuously, and
releases are periodic version bumps. Pull requests are welcome — build from
source, make the full test suite pass, and describe what the change does and
why. [AGENTS.md](AGENTS.md) records the architectural invariants and testing
expectations every change is held to; they apply to human contributors just as
much as to coding agents.

## Building from source

A Go toolchain (1.26 or newer) and Git are the only requirements:

```sh
./scripts/install.sh            # builds and installs ~/.local/bin/workbook
./scripts/install.sh ~/bin wb   # or pick your own destination and name
```

## Setting up a development environment

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

### Remote agent sessions

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

## Continuous integration

`.github/workflows/ci.yml` runs on every push to `main` and every pull request
against it, on `ubuntu-24.04` and `macos-15` because Workbook publishes darwin
and linux archives. Each job verifies formatting with `gofmt -l .`, runs
`go vet ./...`, and runs `go test ./...`.

A suite that skips is the failure this workflow is built to prevent. The
embedded web board tests execute the rendered client with `node`, and the
cross-object-format tests need a Git that can create SHA-256 repositories;
without either, those tests skip and the package still reports `ok`. Four
things stop that:

- `scripts/check-ci-capabilities.sh` runs before the suite and fails, naming
  the tool, when node or SHA-256 Git support is absent.
- `WORKBOOK_TEST_REQUIRE_CAPABILITIES=1` turns a missing capability into a test
  failure. Tests report one through `internal/testenv.MissingCapability`
  instead of `t.Skip`, which skips locally and fails wherever the variable is
  set.
- A guard test in `internal/testenv` parses the module's Go sources — test
  files and the helper packages that take a `*testing.T` — and fails when a
  function probes for a tool with `exec.LookPath` and then calls a bare
  `t.Skip`. The variable above cannot see such a skip at all, and the report
  below lists it without failing the run, so nothing else makes it fatal. A
  function that skips for an unrelated reason is named in that test's
  `bareSkipExceptions` list with the reason, and an entry that stops matching
  fails as stale.
- `scripts/skipreport` reads `go test -json`, replays the readable output, and
  writes every skip and every missing-capability failure to the job summary, so
  a shrinking suite is visible rather than green.

Run the same report locally:

```sh
set -o pipefail
go test ./... -json | go run ./scripts/skipreport
```


## Releasing

Workbook is developed on a trunk. Features merge to `main` continuously, and a
release is a periodic version bump that gathers whatever has landed since the
last tag. Merging does not publish anything, so work can accumulate on `main`
until a group of it is worth releasing.

Every release is a version tag on `main`. Three things can create one, and all
three end in the same publication.

### A release pull request

The usual path. Open a pull request that adds a `CHANGELOG.md` entry describing
the release, and label it `release:patch`, `release:minor`, or `release:major`.
Merging it cuts the release.

```markdown
## v0.5.0 — 2026-08-08

### Added
- board reconcile rendering
```

The label and the changelog heading are two independent statements of the same
intent, and the release proceeds only when they agree. A changelog edit carrying
no label releases nothing, so an erroneous edit to the file is inert. A
`release:minor` or `release:major` label must be backed by an entry for the
version it implies. `release:patch` may be cut without one, for a fix that does
not warrant prose.

The check runs on the pull request itself, so a disagreement blocks the merge
rather than failing after it. It runs again against the merged commit before
tagging, because another release landing in between changes which version comes
next.

The three labels have to exist on the repository before this works:

```sh
gh label create release:patch --description "Merging cuts a patch release"
gh label create release:minor --description "Merging cuts a minor release"
gh label create release:major --description "Merging cuts a major release"
```

### The Actions button

For a patch that needs no prose, run **Cut Release** from the Actions tab, or:

```sh
gh workflow run cut-release.yml -f bump=patch
```

Pick `patch`, `minor`, or `major` and the version is computed from the newest
tag; fill in the optional exact version to override it. The same changelog rule
applies, so a `minor` or `major` cut this way still needs an entry. Releases are
cut from `main`, and a run on any other branch is refused.

### From a checkout

```sh
./scripts/cut-release.sh 0.3.0
```

It refuses to publish anything until the release is one that can be reproduced:
the version is strict `MAJOR.MINOR.PATCH` and orders after the latest release,
`HEAD` is on `main` with nothing uncommitted, `main` matches the remote, and the
tag is unused both locally and on the remote. It then runs `go test ./...`,
creates the annotated tag, and pushes only that tag.

Check a release without publishing it with `--dry-run`, which runs every check
and stops before tagging:

```sh
./scripts/cut-release.sh 0.3.0 --dry-run
```

`--skip-tests` skips the test run, and `--remote` and `--branch` override the
`origin` and `main` defaults. This path does not consult the changelog: it takes
an exact version and trusts the person typing it.

### What the workflow publishes

A version tag such as `v0.1.0` runs the release workflow. It revalidates the
strict SemVer tag, publishes the four archives and checksums to GitHub Releases,
and updates the `dgoings/homebrew-tap` formula from those generated checksums.
The protected release environment exposes a credential scoped only to that tap
repository after validation. New assets are staged in a draft, the tap update is
pushed first, and the draft is published last. A rerun verifies existing assets
byte-for-byte and never overwrites them; a failed final publication reverts the
tap update and removes only a draft created by that run.

The release notes are the `CHANGELOG.md` entry when the version has one, and
generated from the commit log when it does not.

A tag pushed by a workflow using the default `GITHUB_TOKEN` does not start
another workflow run, so the two automated paths push their tag and then call
the release workflow directly. A tag pushed from a checkout starts it through
the `push` trigger as before. Publication has one implementation and three
entrances.

### When a release fails

A tag has to exist before a release can be published against it, so both
automated paths push one and then build. A run that dies after that leaves the
tag behind, and because versions only order forward, that number cannot be
released again while the tag stands.

Two things narrow this. Both cut paths refuse to tag a commit CI has not already
verified, which removes the likeliest cause: the dispatch path checks the tip of
`main`, and the merge path checks the pull request's reviewed head, the commit
that gated the merge. And `publish-release.sh` already unwinds its own work,
reverting the tap commit and deleting only a draft that run created.

What is left is the tag. Delete it and the version is free again:

```sh
./scripts/delete-release-tag.sh 0.5.0
```

It removes the tag on the remote first and then locally, and refuses outright if
the tag has a published release, because Homebrew resolves its download URLs
through the tag and deleting a live one breaks every install that followed it.
`--dry-run` reports what it would do, `--delete-draft` also clears a draft left
behind by the failed run, and `--force` overrides the published-release refusal.

Either repair is fine: delete the tag and cut the same version again, or move on
to the next version and delete the skipped tag so it stops standing in for a
release that never existed.

The one case worth knowing about is a release whose CHANGELOG entry was already
written. Once `v0.5.0` is tagged, the newest entry naming `v0.5.0` looks exactly
like the ordinary "the last release's entry, untouched" state, and a retry would
otherwise cut `v0.5.1` with generated notes and strand that entry describing a
release nobody can download. The cut checks whether the previous tag actually
published, so it stops and names both repairs instead:

```
the newest CHANGELOG entry describes v0.5.0, whose tag exists but published no release.
  Either delete that tag and cut v0.5.0 again:
      scripts/delete-release-tag.sh 0.5.0
  or retitle the entry to the version being cut now, v0.5.1.
```

This source repository intentionally does not track an installable
`Formula/workbook.rb` with placeholder checksums. The workflow renders the real
formula directly into the tap from the built artifacts.

### Release artifacts

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

