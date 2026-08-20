#!/bin/sh
set -eu

# Cuts a Workbook release by tagging a commit on the default branch.
#
# Releases are periodic version bumps over trunk-based development: features
# merge to main continuously, and a release gathers whatever has landed since
# the last tag. This script only creates and pushes the tag. Pushing it starts
# .github/workflows/release.yml, which builds the archives, publishes them to
# GitHub Releases, and updates the Homebrew tap.

usage() {
	cat <<'USAGE'
usage: scripts/cut-release.sh <version> [options]

Tags the current default-branch commit as vMAJOR.MINOR.PATCH and pushes the
tag, which starts the release workflow.

Options:
  --dry-run        report every check and stop before tagging or pushing
  --skip-tests     do not run "go test ./..." before tagging
  --remote NAME    remote to publish to (default origin)
  --branch NAME    branch releases must be cut from (default main)
  -h, --help       show this message

The release workflow builds and publishes the archives and the Homebrew
formula, so nothing else has to be run by hand after this succeeds.
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-1}"
}

version=
dry_run=no
skip_tests=no
remote=origin
branch=main

while [ "$#" -gt 0 ]; do
	case $1 in
		--dry-run)
			dry_run=yes
			;;
		--skip-tests)
			skip_tests=yes
			;;
		--remote)
			[ "$#" -ge 2 ] || fail "--remote requires a value" 2
			remote=$2
			shift
			;;
		--remote=*)
			remote=${1#--remote=}
			;;
		--branch)
			[ "$#" -ge 2 ] || fail "--branch requires a value" 2
			branch=$2
			shift
			;;
		--branch=*)
			branch=${1#--branch=}
			;;
		-h | --help)
			usage
			exit 0
			;;
		-*)
			usage >&2
			fail "unknown option: $1" 2
			;;
		*)
			[ -z "${version}" ] || fail "only one version may be given" 2
			version=$1
			;;
	esac
	shift
done

if [ -z "${version}" ]; then
	usage >&2
	fail "a release version is required" 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"
# Reject the version before anything reaches the remote, using the same rule
# the workflow and the formula renderer apply.
require_safe_release_version "${version}" "workbook release"

tag=v${version}
repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd -P)
git_command() {
	git -C "${repository_root}" "$@"
}

if ! command -v git >/dev/null 2>&1; then
	fail "git is required but was not found in PATH"
fi
if [ "${skip_tests}" = no ] && ! command -v go >/dev/null 2>&1; then
	fail "go is required to test before releasing; pass --skip-tests to skip"
fi

# A release must describe a commit that reviewers can see, so refuse to tag
# uncommitted work or a branch other than the trunk.
current_branch=$(git_command symbolic-ref --quiet --short HEAD 2>/dev/null || echo "")
if [ "${current_branch}" != "${branch}" ]; then
	fail "releases are cut from ${branch}, but HEAD is ${current_branch:-detached}"
fi
if [ -n "$(git_command status --porcelain)" ]; then
	fail "the working tree has uncommitted changes"
fi

if ! git_command fetch --quiet --tags "${remote}" "${branch}"; then
	fail "could not fetch ${branch} and tags from ${remote}"
fi

# Tags are immutable once the workflow has published against them, so a name
# already in use has to be resolved by choosing a new version.
if git_command rev-parse --verify --quiet "refs/tags/${tag}" >/dev/null; then
	fail "tag ${tag} already exists locally"
fi
if git_command ls-remote --exit-code --tags "${remote}" "refs/tags/${tag}" >/dev/null 2>&1; then
	fail "tag ${tag} already exists on ${remote}"
fi

local_head=$(git_command rev-parse HEAD)
remote_head=$(git_command rev-parse "refs/remotes/${remote}/${branch}")
if [ "${local_head}" != "${remote_head}" ]; then
	fail "${branch} is not in sync with ${remote}/${branch}; pull or push first"
fi

# Version numbers only ever move forward, so catch a bump that would order
# below a release that already shipped. Resolving through the shared script
# rather than repeating the comparison here keeps this path and the two cut
# workflows from disagreeing about which version follows which. It reports its
# own refusal, so there is nothing to add to it.
previous_tag=$(git_command tag --list 'v[0-9]*' --sort=-v:refname | head -n 1)
if ! "${script_directory}/resolve-release-version.sh" \
	--version "${version}" --previous "${previous_tag}" >/dev/null; then
	exit 1
fi

echo "Releasing ${tag} from ${branch} at ${local_head}"
if [ -n "${previous_tag}" ]; then
	echo "  previous release  ${previous_tag}"
	echo "  changes since     $(git_command rev-list --count "${previous_tag}..HEAD") commits"
else
	echo "  previous release  none"
fi

if [ "${skip_tests}" = no ]; then
	echo "Running tests..."
	if [ "${dry_run}" = yes ]; then
		echo "  dry run: would run go test ./..."
	else
		(cd -- "${repository_root}" && go test ./...)
	fi
fi

if [ "${dry_run}" = yes ]; then
	echo
	echo "Dry run: every check passed. Without --dry-run this would run:"
	echo "  git tag --annotate ${tag} --message 'Workbook ${tag}' ${local_head}"
	echo "  git push ${remote} refs/tags/${tag}"
	exit 0
fi

git_command tag --annotate "${tag}" --message "Workbook ${tag}" "${local_head}"
# Push only the tag. Anything else in the working repository stays local, so a
# failed push leaves nothing published to unwind.
if ! git_command push "${remote}" "refs/tags/${tag}"; then
	git_command tag --delete "${tag}" >/dev/null
	fail "could not push ${tag} to ${remote}; the local tag was removed"
fi

echo
echo "Pushed ${tag}. The release workflow now builds and publishes:"
echo "  - the four platform archives and checksums.txt on the GitHub release"
echo "  - the Homebrew formula in the tap"
# Derive the Actions URL from the remote rather than hardcoding the owner, so
# a fork's operator is pointed at their own workflow runs.
remote_url=$(git_command remote get-url "${remote}" 2>/dev/null || echo "")
case ${remote_url} in
	git@github.com:*)
		repository_path=${remote_url#git@github.com:}
		;;
	https://github.com/*)
		repository_path=${remote_url#https://github.com/}
		;;
	*)
		repository_path=
		;;
esac
repository_path=${repository_path%.git}
if [ -n "${repository_path}" ]; then
	echo "Watch it at https://github.com/${repository_path}/actions"
fi
