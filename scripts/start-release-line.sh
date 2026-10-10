#!/bin/sh
set -eu

# Starts a maintenance line: a maint/vMAJOR.MINOR branch for shipping patch
# releases to an older minor version once main has moved on toward the next.
#
# Workbook stays on a trunk. A line is created on demand, the first time
# someone on an older release needs a fix without taking the next minor, from
# the newest release of that line, and nothing keeps it in step with main
# afterwards: each fix lands on main first and is cherry-picked onto the line
# in a pull request. This script only creates the branch, and pushes it only
# when asked. It never creates a tag; releases on the line are cut the way
# main's are. See "Patch releases to an older line" in CONTRIBUTING.md.

usage() {
	cat <<'USAGE'
usage: scripts/start-release-line.sh <MAJOR.MINOR> [options]

Creates the maint/vMAJOR.MINOR branch at the newest stable vMAJOR.MINOR.* tag.

Options:
  --push           push the new branch to the remote as well
  --bootstrap      start a line from a release that predates release lines;
                   its first pull request then brings main's tooling onto it
  --remote NAME    remote to read and push to (default origin)
  -h, --help       show this message

It refuses when the branch already exists here or on the remote, when the line
has no stable release, and when main has nothing since that release, in which
case a patch is cut from main itself and a line would add nothing. It also
refuses, unless --bootstrap is given, a release cut before release lines
existed, whose workflows cannot release from the branch.
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-1}"
}

line=
push=no
bootstrap=no
remote=origin

while [ "$#" -gt 0 ]; do
	case $1 in
		--push)
			push=yes
			;;
		--bootstrap)
			bootstrap=yes
			;;
		--remote)
			[ "$#" -ge 2 ] || fail "--remote requires a value" 2
			remote=$2
			shift
			;;
		--remote=*)
			remote=${1#--remote=}
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
			[ -z "${line}" ] || fail "only one line may be given" 2
			line=$1
			;;
	esac
	shift
done

if [ -z "${line}" ]; then
	usage >&2
	fail "a line such as 0.5 is required" 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

if ! is_release_line "${line}"; then
	fail "a line is MAJOR.MINOR without leading zeroes, such as 0.5, not ${line}" 2
fi

branch=maint/v${line}
repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd -P)
git_command() {
	git -C "${repository_root}" "$@"
}

if ! command -v git >/dev/null 2>&1; then
	fail "git is required but was not found in PATH"
fi

# The same fetch cut-release.sh makes, for the same reasons: main and the v*
# release tags only, without force, so a stale rolling desktop-latest is left
# out and a release tag that moved is refused rather than repointed.
if ! git_command fetch --quiet --no-tags "${remote}" \
	"+refs/heads/main:refs/remotes/${remote}/main" \
	'refs/tags/v*:refs/tags/v*'; then
	fail "could not fetch main and release tags from ${remote}; a release tag that differs between ${remote} and this clone is refused, since a published release tag never moves"
fi

if git_command rev-parse --verify --quiet "refs/heads/${branch}" >/dev/null; then
	fail "${branch} already exists in this clone; a line is started once, and its fixes are cherry-picked onto it"
fi
remote_status=0
git_command ls-remote --exit-code --heads "${remote}" "refs/heads/${branch}" >/dev/null 2>&1 || remote_status=$?
case ${remote_status} in
	0) fail "${branch} already exists on ${remote}; fetch it rather than starting it again" ;;
	2) ;;
	*) fail "could not ask ${remote} whether ${branch} exists" ;;
esac

# A candidate is not a release, so only a stable tag can start a line.
release_tag=$(newest_release_tag stable "${repository_root}" v "${line}")
if [ -z "${release_tag}" ]; then
	fail "there is no v${line}.* release to start ${branch} from"
fi
release_commit=$(git_command rev-parse --verify "refs/tags/${release_tag}^{commit}")

# main with nothing since the line's newest release is still that line: a
# patch is cut from main as usual, and a branch would only be a second copy.
# Once anything lands on main, the line is worth having, even before the next
# minor ships.
main_commit=$(git_command rev-parse --verify "refs/remotes/${remote}/main^{commit}")
if [ "$(git_command rev-list --count "${release_commit}..${main_commit}")" -eq 0 ]; then
	fail "main has nothing since ${release_tag}, so there is nothing to branch; cut the patch from main"
fi

# A branch takes its workflows and release scripts from the release it starts
# at, and GitHub runs a line's pushes, dispatches and tags from that tree. A
# release cut before lines existed has none of the line support: CI does not
# run on the branch, neither cut workflow accepts it, and its release workflow
# publishes any tag as the newest, so a patch tagged there would move Latest
# and the Homebrew tap backward. start-release-line.sh arrived with that
# support, so its presence in the release's tree is the marker.
predates_lines=no
if ! git_command cat-file -e "${release_commit}:scripts/start-release-line.sh" 2>/dev/null; then
	predates_lines=yes
	if [ "${bootstrap}" = no ]; then
		fail "${release_tag} predates release lines, so ${branch} would carry workflows that cannot verify, cut or safely publish a patch, and a tag pushed onto it would move Latest and the Homebrew tap backward; pass --bootstrap to start it anyway, then bring main's tooling onto it first, as \"Patch releases to an older line\" in CONTRIBUTING.md describes"
	fi
fi

git_command branch --no-track "${branch}" "${release_commit}"
echo "Created ${branch} at ${release_tag} (${release_commit})"

if [ "${push}" = yes ]; then
	# Push only the branch. A failed push removes it here too, so a retry
	# starts from the same state rather than refusing a branch nobody has.
	if ! git_command push "${remote}" "refs/heads/${branch}:refs/heads/${branch}"; then
		git_command branch --delete --force "${branch}" >/dev/null
		fail "could not push ${branch} to ${remote}; the local branch was removed"
	fi
	echo "Pushed ${branch} to ${remote}."
else
	echo "It is local only. Push it when its first pull request is ready:"
	echo "  git push ${remote} refs/heads/${branch}"
fi
echo
if [ "${predates_lines}" = yes ]; then
	echo "${release_tag} predates release lines. Make the first pull request into ${branch}"
	echo "bring main's .github/workflows/ and scripts/ onto it, with whatever their tests"
	echo "need from main, carrying no release label; its own workflows verify it."
	echo "Do not push a tag on ${branch}, and merge no release pull request into it,"
	echo "until that has merged: the release workflow it carries now publishes any tag"
	echo "as the newest release. Once it has merged, land the fix on main, cherry-pick"
	echo "it onto ${branch} in a pull request labeled release:patch, and merge that one"
	echo "to cut the release."
else
	echo "Next: land the fix on main, cherry-pick it onto ${branch} in a pull request"
	echo "labeled release:patch, and merge that pull request to cut the release."
fi
