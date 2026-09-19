#!/bin/sh
set -eu

# Decides which desktop tag a CLI release cascades into, and refuses when the
# desktop sequence has moved ahead of the CLI's number.
#
# The two sequences start at the same number and stay equal until a
# desktop-only release is cut. Once that happens the CLI's number may fall at
# or below the newest desktop tag, and guessing a bump kind on the app's behalf
# would publish a version nobody chose. So this script takes the CLI's number
# when it is newer and otherwise says how to cut by hand.
#
# The grammar and the ordering are release-version.sh's; nothing here is the
# desktop app's own idea of what a version is.

usage() {
	cat <<'USAGE'
usage: scripts/plan-desktop-release.sh --cli-version VERSION [--previous TAG]

Prints the desktop tag (desktop-vVERSION) a CLI release cascades into.

Options:
  --cli-version VERSION  the CLI version just released, such as 0.6.0 or 0.6.0-rc1
  --previous TAG         previous desktop tag (default: newest desktop-v* tag in this repository)
  -h, --help             show this message
USAGE
}

fail() {
	echo "workbench release: $1" >&2
	exit "${2:-1}"
}

cli_version=
previous_tag=
previous_given=no

while [ "$#" -gt 0 ]; do
	case $1 in
		--cli-version)
			[ "$#" -ge 2 ] || fail "--cli-version requires a value" 2
			cli_version=$2
			shift
			;;
		--cli-version=*)
			cli_version=${1#--cli-version=}
			;;
		--previous)
			[ "$#" -ge 2 ] || fail "--previous requires a value" 2
			previous_tag=$2
			previous_given=yes
			shift
			;;
		--previous=*)
			previous_tag=${1#--previous=}
			previous_given=yes
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			fail "unknown option: $1" 2
			;;
	esac
	shift
done

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

if [ -z "${cli_version}" ]; then
	usage >&2
	fail "--cli-version is required" 2
fi
# Hold the CLI's number to the same grammar the desktop tag will carry, so a
# version this prints can never be one validate-desktop-release-tag.sh rejects.
require_safe_release_version "${cli_version}" "workbench release"

# Discovering the previous desktop release is a convenience for interactive
# use; the workflow passes the tag it looked up. The kind is "any" because a
# desktop pre-release still has to be cleared: unlike a CLI bump, nothing here
# computes a number from the previous tag, so an rc is never invisible.
if [ "${previous_given}" = no ]; then
	previous_tag=$(newest_release_tag any "" desktop-v)
fi

# No desktop release yet means nothing to order against, and the first
# companion release simply takes the CLI's number.
if [ -n "${previous_tag}" ]; then
	previous_number=${previous_tag#desktop-v}
	if ! is_safe_release_version "${previous_number}"; then
		fail "previous desktop release ${previous_tag} is not a desktop release version tag"
	fi
	if ! release_version_before "${previous_number}" "${cli_version}"; then
		fail "the newest desktop release ${previous_tag} does not order before the CLI's ${cli_version}; cut the desktop release by hand with a chosen version:
  git tag --annotate desktop-vX.Y.Z --message 'Workbench desktop-vX.Y.Z' && git push origin refs/tags/desktop-vX.Y.Z"
	fi
fi

echo "desktop-v${cli_version}"
