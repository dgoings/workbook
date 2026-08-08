#!/bin/sh
set -eu

# Removes a release tag that never published anything.
#
# A release that dies after its tag is pushed leaves the tag behind, and because
# versions only order forward that number can never be released again. There are
# two ways out and this script serves both: delete the tag and cut the same
# version once the cause is fixed, or move on to the next version and delete the
# skipped tag so it stops standing in for a release that does not exist.
#
# The one thing it will not do by default is pull a tag out from under a
# published release. Homebrew resolves its download URLs through the tag, so
# deleting a live one breaks installation for everyone the release already
# reached.

usage() {
	cat <<'USAGE'
usage: scripts/delete-release-tag.sh <version> [options]

Deletes vMAJOR.MINOR.PATCH locally and on the remote.

Options:
  --repo REPOSITORY  owner/name to query for release state (default: $GITHUB_REPOSITORY,
                     otherwise read from the remote's URL)
  --remote NAME      remote to delete from (default origin)
  --delete-draft     also delete a draft release left behind for this tag
  --force            delete even when the tag has a published release
  --dry-run          report what would happen and change nothing
  -h, --help         show this message
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-1}"
}

version=
repository=${GITHUB_REPOSITORY:-}
remote=origin
delete_draft=no
force=no
dry_run=no

while [ "$#" -gt 0 ]; do
	case $1 in
		--repo)
			[ "$#" -ge 2 ] || fail "--repo requires a value" 2
			repository=$2
			shift
			;;
		--repo=*)
			repository=${1#--repo=}
			;;
		--remote)
			[ "$#" -ge 2 ] || fail "--remote requires a value" 2
			remote=$2
			shift
			;;
		--remote=*)
			remote=${1#--remote=}
			;;
		--delete-draft)
			delete_draft=yes
			;;
		--force)
			force=yes
			;;
		--dry-run)
			dry_run=yes
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
# Accept the tag form too, since that is what the failing workflow logged.
version=${version#v}

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"
require_safe_release_version "${version}" "workbook release"

tag=v${version}
repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd -P)
git_command() {
	git -C "${repository_root}" "$@"
}

if [ -z "${repository}" ]; then
	remote_url=$(git_command remote get-url "${remote}" 2>/dev/null || echo "")
	case ${remote_url} in
		*github.com[:/]*)
			repository=${remote_url#*github.com}
			repository=${repository#:}
			repository=${repository#/}
			repository=${repository%.git}
			;;
	esac
fi

# Refusing is only meaningful if the release state was actually read. Treat an
# unanswerable question as a reason to stop, not as permission to continue.
if [ "${force}" = no ]; then
	if [ -z "${repository}" ]; then
		fail "could not determine the repository to check for a published release; pass --repo or --force"
	fi
	published_status=0
	"${script_directory}/check-release-published.sh" "${tag}" --repo "${repository}" >/dev/null || published_status=$?
	case ${published_status} in
		0)
			fail "${tag} has a published release; deleting its tag would break every install that resolves through it. Pass --force only if you are certain."
			;;
		1) ;;
		*)
			fail "could not read release state for ${tag}; pass --force to delete without checking"
			;;
	esac
fi

local_exists=no
remote_exists=no
if git_command rev-parse --verify --quiet "refs/tags/${tag}" >/dev/null; then
	local_exists=yes
fi
if git_command ls-remote --exit-code --tags "${remote}" "refs/tags/${tag}" >/dev/null 2>&1; then
	remote_exists=yes
fi

if [ "${local_exists}" = no ] && [ "${remote_exists}" = no ]; then
	echo "${tag} does not exist locally or on ${remote}; nothing to delete"
	exit 0
fi

describe() {
	if [ "$2" = yes ]; then
		printf '  %-10s present\n' "$1"
	else
		printf '  %-10s absent\n' "$1"
	fi
}

echo "Deleting ${tag}"
describe local "${local_exists}"
describe "${remote}" "${remote_exists}"

if [ "${dry_run}" = yes ]; then
	echo
	echo "Dry run. Without --dry-run this would run:"
	if [ "${remote_exists}" = yes ]; then
		echo "  git push ${remote} :refs/tags/${tag}"
	fi
	if [ "${local_exists}" = yes ]; then
		echo "  git tag --delete ${tag}"
	fi
	if [ "${delete_draft}" = yes ]; then
		echo "  gh release delete ${tag} --repo ${repository} --yes"
	fi
	exit 0
fi

# The remote goes first. A failure there leaves the local tag as the record of
# what still needs removing, where deleting locally first would hide it.
if [ "${remote_exists}" = yes ]; then
	git_command push "${remote}" ":refs/tags/${tag}"
fi
if [ "${local_exists}" = yes ]; then
	git_command tag --delete "${tag}" >/dev/null
fi

if [ "${delete_draft}" = yes ]; then
	if [ -z "${repository}" ]; then
		fail "could not determine the repository to delete a draft from; pass --repo"
	fi
	if gh release view "${tag}" --repo "${repository}" >/dev/null 2>&1; then
		gh release delete "${tag}" --repo "${repository}" --yes
		echo "  deleted the release page left behind for ${tag}"
	fi
fi

echo
echo "${tag} is gone. Cut v${version} again once the cause is fixed, or cut the"
echo "next version and leave this one unused."
