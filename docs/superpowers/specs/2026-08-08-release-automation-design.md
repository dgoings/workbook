# Release Automation Design

## Goal

Cut a release without a laptop, and make the repository carry a curated record
of what each release contained.

Today a release is one command run by hand: `./scripts/cut-release.sh 0.3.0`
validates, tags, and pushes, and the pushed tag starts
`.github/workflows/release.yml`, which builds the four archives, publishes them,
and updates the Homebrew tap. That second half is already automated and stays
exactly as it is. What is manual is deciding when to release, choosing the
version, and being at a machine with a clean checkout of `main`.

This adds two more ways to reach the same tag:

1. **A labeled release PR.** When a group of work is worth releasing, open a PR
   that adds a `CHANGELOG.md` entry and carries a `release:patch`,
   `release:minor`, or `release:major` label. Merging it cuts the release.
2. **A `workflow_dispatch` button.** For a patch that does not warrant prose,
   pick a bump kind in the Actions tab and run it from anywhere.

## Current Behavior

The version exists in exactly one place: the Git tag. `internal/release/release.go:8`
holds `Version = "dev"` as a placeholder that the linker overwrites via
`-ldflags -X main.version=` (`cmd/workbook/version.go:10`). No `VERSION` file,
no manifest, no changelog. `README.md:509` records that the tap's
`Formula/workbook.rb` is deliberately not tracked here either.

`scripts/cut-release.sh:110-147` holds the preflight gates: `HEAD` is on `main`,
the tree is clean, `main` matches the remote, the tag is unused locally and
remotely, and the version orders after the newest `v*` tag. It then runs
`go test ./...`, creates an annotated tag, and pushes only that tag.

`scripts/publish-release.sh:131-137` creates the GitHub Release as a draft with
`--generate-notes`, verifies a rerun's assets byte-for-byte, pushes the tap
commit first, and publishes the draft last, reverting the tap and deleting only
a draft it created if the run fails.

## The constraint that shapes the design

A tag pushed by a workflow using the default `GITHUB_TOKEN` does not create a
new workflow run. Both new paths push their tag from inside Actions, so neither
would start `release.yml` through its `push: tags` trigger. Only the existing
local script is unaffected, because a human pushes it with personal
credentials.

`release.yml` therefore gains a `workflow_call` trigger alongside its existing
`push` trigger, and the two cut workflows invoke the publish job directly after
pushing the tag. Three entrances, one publish job, and no new long-lived
credential — which keeps the least-privilege stance `README.md:507` describes,
where the only durable secret is scoped to the tap repository.

## Where the version comes from

The bump label and the CHANGELOG heading are two independent expressions of the
same intent, and the cut proceeds only when they agree. A CHANGELOG edit with no
label releases nothing, so an erroneous edit to the file is inert.

The previous release is the newest `v*` tag. The computed version is that tag
bumped by the label's kind. With no previous tag, the previous version is
treated as `0.0.0`.

An entry is considered **present** when the topmost `## v<version>` heading in
`CHANGELOG.md` names something other than the previous release. That
distinguishes "the author added an entry" from "the newest entry is the last
release's, untouched," without needing to inspect the diff.

| Label | Topmost CHANGELOG heading | Result |
| --- | --- | --- |
| `release:patch` | previous release (no new entry) | cut |
| `release:patch` | the computed version | cut |
| `release:patch` | any other version | fail |
| `release:minor` / `release:major` | previous release (no new entry) | fail |
| `release:minor` / `release:major` | the computed version | cut |
| `release:minor` / `release:major` | any other version | fail |
| none | anything | no release; an ordinary merge |

Two release labels on one PR is an error rather than a precedence rule.

The same matrix governs the `workflow_dispatch` path. Dispatching `patch` needs
no entry, which is the escape hatch it exists to be; dispatching `minor` or
`major` without one fails and points at the PR flow. One rule, one
implementation, whichever way the release was triggered.

## Checked twice

The agreement check runs as a pull request check on every push to a PR, so a
mismatch blocks the merge button rather than failing after the merge has landed.
It runs on every PR, not only labeled ones: a PR with no release label passes
trivially, which lets the check be marked required without leaving unlabeled PRs
pending forever.

It then runs again in the post-merge cut job, against the real state of `main`.
The pre-merge result can go stale — another release merging in between would
change the previous tag and therefore the computed version — so the second run
is what the tag is actually based on.

## Components

Every piece of release machinery in this repository is a shell script with a Go
test file beside it, and the workflows themselves are asserted in
`scripts/ci_workflow_test.go`. The new logic follows that shape: the workflows
stay thin, and everything decidable runs on a laptop without pushing a branch.

### `CHANGELOG.md` (new)

Seeded with a header only. Releases before this change have generated notes on
the GitHub Releases page, and inventing prose for them after the fact would be
fabrication. Entries begin with the next release.

```markdown
## v0.5.0 — 2026-08-08

### Added
- ...
```

The date is optional and unvalidated. A heading matches
`^## v([0-9]+\.[0-9]+\.[0-9]+)( .*)?$`; an entry body is everything up to the
next `## ` heading or end of file.

### `scripts/resolve-release-version.sh` (new)

```
usage: scripts/resolve-release-version.sh --bump KIND [--version VERSION] [--previous TAG]
```

Prints the resolved bare `MAJOR.MINOR.PATCH`. A non-empty `--version` wins over
`--bump`, so the caller passes both unconditionally and the precedence lives in
tested code rather than in YAML. `--previous` defaults to the newest `v*` tag.
Validates through the existing `is_safe_release_version` in
`scripts/release-version.sh` and rejects a version that does not order strictly
after the previous release.

### `scripts/release-bump-label.sh` (new)

Reads label names and prints the bump kind. No release label prints nothing and
exits 0, which is how the PR check distinguishes an ordinary PR. Two or more
release labels exit non-zero.

### `scripts/changelog-entry.sh` (new)

```
usage: scripts/changelog-entry.sh <version> <changelog-path>
```

Prints the entry body for a version, or exits 1 when absent. Used both to detect
presence and to supply the release notes.

### `scripts/check-release-changelog.sh` (new)

Applies the matrix above given a bump kind, the computed version, the previous
tag, and a changelog path. Failures name both sides of the disagreement, so the
message says what the label implies and what the file says.

### `scripts/cut-release.sh` (modified)

Drops its own ordering arithmetic at lines 141-147 in favor of
`resolve-release-version.sh`, so the local path and the automated paths cannot
diverge on what version follows what. Its other preflight gates are unchanged.

### `scripts/publish-release.sh` (modified)

When `CHANGELOG.md` has an entry for the version, the GitHub Release body is
that entry via `--notes-file`; otherwise `--generate-notes` as today. Writing
notes by hand and then publishing different generated ones is the divergence
that makes a changelog stop being trusted. Takes an optional fifth argument for
the changelog path, defaulting to the repository root, so the existing
four-argument callers and tests keep working.

### `.github/workflows/release.yml` (modified)

Gains `workflow_call` with a required `tag` input beside its `push` trigger. The
tag is `inputs.tag` when called and `github.ref_name` when triggered by a push,
and the concurrency group keys on the tag rather than `github.ref` so a called
run does not group by the caller's branch.

### `.github/workflows/cut-release.yml` (new)

`workflow_dispatch` with a `bump` choice defaulting to `patch` and an optional
`version` string. Resolves the version, applies the changelog matrix, pushes an
annotated tag as `github-actions[bot]`, then calls the publish job.

### `.github/workflows/release-pr.yml` (new)

One file, two jobs. `validate` runs on `opened`, `synchronize`, `reopened`,
`labeled`, and `unlabeled` with read-only permissions. `cut` runs on `closed`,
guarded on the PR being merged, carrying exactly one release label, and
originating from this repository rather than a fork. `cut` pushes the tag and a
third job calls the publish workflow.

## Error handling

A failed cut leaves nothing behind: validation happens before the tag is
created, and the tag is the last thing the cut job does.

A failed publish is already handled by `publish-release.sh`'s rollback, which
reverts the tap commit and deletes only a draft that run created. What it leaves
is the tag itself. Because `release.yml` runs `go test ./...` before it builds
anything, a broken commit fails there, and the version number is burned until
the tag is deleted. That is the same exposure the current local flow has and is
documented rather than engineered around; gating the cut on a green CI run for
the exact SHA is a plausible later addition, not part of this change.

Release labels must exist on the repository before the PR path works. Creating
`release:patch`, `release:minor`, and `release:major` is a one-time setup step.

## Testing

A Go test file beside each new script, matching `scripts/cut_release_test.go`
and `scripts/render_homebrew_formula_test.go`: version resolution across bump
kinds, explicit-version precedence, the no-previous-tag case, rejection of
versions that do not move forward, every row of the changelog matrix, entry
extraction including the last entry in a file and a version that is absent, and
label selection with zero, one, and two release labels.

Workflow assertions extend `scripts/ci_workflow_test.go`'s approach: that
`release.yml` carries both triggers and keys concurrency on the tag, that the
cut workflows call the publish workflow rather than relying on the tag push,
that the PR cut job is guarded on merged and same-repository, and that actions
stay pinned to SHAs.

`scripts/publish_release_test.go` gains coverage for notes selection: an entry
present yields `--notes-file`, absent yields `--generate-notes`.
