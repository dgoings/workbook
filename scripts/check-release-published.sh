#!/bin/sh
set -eu

# Reports whether a release tag actually published a release.
#
# A tag alone does not mean a release shipped. The cut workflows push the tag
# before the release workflow tests, builds, and publishes, so a tag can outlive
# a run that failed on its way to the release page. Telling the two apart is what
# lets the changelog check notice that its newest entry describes a release
# nobody can download, and what stops delete-release-tag.sh from pulling a tag
# out from under a live one.
#
# A draft counts as unpublished. publish-release.sh stages assets in a draft and
# publishes it last, and it deliberately preserves a draft it cannot confirm it
# created, so a surviving draft is the debris of a failed run rather than a
# release.

usage() {
	cat <<'USAGE'
usage: scripts/check-release-published.sh <tag> [--repo REPOSITORY]

Exits 0 when the tag has a published release, 1 when it does not, and 2 when
the question could not be answered.

Options:
  --repo REPOSITORY  owner/name to query (default: $GITHUB_REPOSITORY)
  -h, --help         show this message
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-2}"
}

tag=
repository=${GITHUB_REPOSITORY:-}

while [ "$#" -gt 0 ]; do
	case $1 in
		--repo)
			[ "$#" -ge 2 ] || fail "--repo requires a value"
			repository=$2
			shift
			;;
		--repo=*)
			repository=${1#--repo=}
			;;
		-h | --help)
			usage
			exit 0
			;;
		-*)
			usage >&2
			fail "unknown option: $1"
			;;
		*)
			[ -z "${tag}" ] || fail "only one tag may be given"
			tag=$1
			;;
	esac
	shift
done

if [ -z "${tag}" ]; then
	usage >&2
	fail "a release tag is required"
fi
if [ -z "${repository}" ]; then
	fail "a repository is required; pass --repo or set GITHUB_REPOSITORY"
fi
if ! command -v gh >/dev/null 2>&1; then
	fail "gh is required to read release state but was not found in PATH"
fi

# A tag with no release at all answers the question without an error: nothing
# was published. Any other failure is an unanswered question, not a "no", and
# has to stay distinguishable from one.
if ! draft=$(gh release view "${tag}" --repo "${repository}" --json isDraft --jq .isDraft 2>/dev/null); then
	echo "no release published for ${tag}"
	exit 1
fi

if [ "${draft}" = true ]; then
	echo "release for ${tag} is still a draft"
	exit 1
fi

echo "release for ${tag} is published"
