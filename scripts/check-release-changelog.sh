#!/bin/sh
set -eu

# Checks that the bump label and the CHANGELOG agree about which release is
# being cut.
#
# The label and the heading are two independent expressions of the same intent,
# and a release proceeds only when they agree. A changelog edit carrying no
# label releases nothing, so an erroneous edit to the file is inert; a label
# claiming a minor or major release must be backed by prose describing it.
#
#   label                   newest heading             result
#   release:patch           the previous release       cut, no entry needed
#   release:patch           the computed version       cut
#   release:patch           any other version          fail
#   release:minor / major   the previous release       fail, entry required
#   release:minor / major   the computed version       cut
#   release:minor / major   any other version          fail
#
# The newest heading is the highest-versioned `## vX.Y.Z` heading in the file,
# wherever it sits, not the topmost. A patch to an older line writes its entry
# on that line's maint/vX.Y branch and is then forward-ported into main's
# changelog in version order, so main carries headings for versions older than
# its own newest release; position must not turn one of those into the entry
# for the next release.
#
# An entry counts as present when the newest heading orders after the previous
# release. One that names the previous release is "the last release's,
# untouched", and one that orders before it is older still: a release that was
# cut as a patch without an entry leaves the newest heading behind it, and so
# does a forward-ported backport on a line whose minor has already moved on.
# Both are absent. That distinguishes "the author added an entry" from "nothing
# new was written" without inspecting a diff, so the same check works before a
# merge and after one. On a maintenance line the previous release is that
# line's newest, which plan-release.sh passes, or which --branch discovers.
#
# Those are not the only ways the newest entry can equal the previous tag.
# A release that failed after its tag was pushed leaves the tag behind, and the
# entry written for it then describes a release nobody can download. Read as
# "the last release's, untouched", a retry would cut the next patch, generate
# notes for it, and orphan that entry in the file forever. --previous-released
# is what tells the two apart, and an orphaned entry is refused rather than
# skipped past.

usage() {
	cat <<'USAGE'
usage: scripts/check-release-changelog.sh --bump KIND --version VERSION [options]

Options:
  --bump KIND               patch, minor, or major
  --version VERSION         the MAJOR.MINOR.PATCH version the release would carry
  --previous TAG            previous release tag (default: newest v* tag in this
                            repository, or on the line BRANCH maintains)
  --branch BRANCH           branch being cut, main (the default) or maint/vMAJOR.MINOR;
                            read only to discover the previous release
  --previous-released YESNO whether that tag published a release (default yes)
  --changelog PATH          changelog to read (default: CHANGELOG.md beside this script's repository)
  -h, --help                show this message
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-1}"
}

requested_bump=
requested_version=
previous_tag=
previous_given=no
previous_released=yes
changelog=
branch=main

while [ "$#" -gt 0 ]; do
	case $1 in
		--bump)
			[ "$#" -ge 2 ] || fail "--bump requires a value" 2
			requested_bump=$2
			shift
			;;
		--bump=*)
			requested_bump=${1#--bump=}
			;;
		--version)
			[ "$#" -ge 2 ] || fail "--version requires a value" 2
			requested_version=$2
			shift
			;;
		--version=*)
			requested_version=${1#--version=}
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
		--branch)
			[ "$#" -ge 2 ] || fail "--branch requires a value" 2
			branch=$2
			shift
			;;
		--branch=*)
			branch=${1#--branch=}
			;;
		--previous-released)
			[ "$#" -ge 2 ] || fail "--previous-released requires a value" 2
			previous_released=$2
			shift
			;;
		--previous-released=*)
			previous_released=${1#--previous-released=}
			;;
		--changelog)
			[ "$#" -ge 2 ] || fail "--changelog requires a value" 2
			changelog=$2
			shift
			;;
		--changelog=*)
			changelog=${1#--changelog=}
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

case ${requested_bump} in
	patch | minor | major) ;;
	*)
		usage >&2
		fail "--bump must be patch, minor, or major" 2
		;;
esac
if [ -z "${requested_version}" ]; then
	usage >&2
	fail "--version is required" 2
fi
case ${previous_released} in
	yes | no) ;;
	*)
		usage >&2
		fail "--previous-released must be yes or no" 2
		;;
esac
if [ -z "${changelog}" ]; then
	repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd -P)
	changelog="${repository_root}/CHANGELOG.md"
fi
if [ "${previous_given}" = no ]; then
	# The changelog only ever describes stable releases, so only those count.
	# A line's previous release is the newest one on that line.
	line=$(release_line_of_branch "${branch}") || exit 2
	previous_tag=$(newest_release_tag stable "" v "${line}")
fi
previous_number=${previous_tag#v}
# The previous release is ordered against the headings below, which needs it
# to be a release version.
if [ -n "${previous_number}" ] && ! is_safe_release_version "${previous_number}"; then
	fail "previous release ${previous_tag} is not a release version tag" 2
fi

# A missing changelog carries no entries, which is the same state as one whose
# newest entry is the previous release. A heading that is not a release
# version, such as one with a leading zero, is not an entry.
newest_entry=
if [ -f "${changelog}" ]; then
	headings=$(awk '
		/^## v[0-9]+\.[0-9]+\.[0-9]+( .*)?$/ {
			heading = $0
			sub(/^## v/, "", heading)
			sub(/ .*$/, "", heading)
			print heading
		}
	' "${changelog}")
	newest_entry=$(printf '%s\n' "${headings}" | while IFS= read -r heading; do
		is_safe_release_version "${heading}" || continue
		printf '%s %s\n' "$(release_version_key "${heading}")" "${heading}"
	done | sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n | tail -n 1 | awk '{ print $6 }')
fi

# entry_age says where the newest entry stands against the previous release:
# after it, the same release, or before it.
entry_state=absent
entry_age=
if [ -n "${newest_entry}" ]; then
	if [ -z "${previous_number}" ] || release_version_before "${previous_number}" "${newest_entry}"; then
		entry_state=present
	elif [ "${newest_entry}" = "${previous_number}" ]; then
		entry_age=same
		if [ "${previous_released}" = no ]; then
			entry_state=orphaned
		fi
	else
		entry_age=older
	fi
fi

# The entry describes v${newest_entry}, whose tag exists but never published.
# Cutting anything else strands it, so say which of the two repairs is wanted
# rather than choosing one.
if [ "${entry_state}" = orphaned ]; then
	fail "the newest CHANGELOG entry describes v${newest_entry}, whose tag exists but published no release.
  Either delete that tag and cut v${newest_entry} again:
      scripts/delete-release-tag.sh ${newest_entry}
  or retitle the entry to the version being cut now, v${requested_version}."
fi

if [ "${entry_state}" = absent ]; then
	if [ "${requested_bump}" = patch ]; then
		echo "release:patch cuts v${requested_version} with no changelog entry"
		exit 0
	fi
	if [ -z "${newest_entry}" ]; then
		fail "release:${requested_bump} expects a CHANGELOG entry for v${requested_version}, but ${changelog} has no entries"
	fi
	if [ "${entry_age}" = older ]; then
		fail "release:${requested_bump} expects a CHANGELOG entry for v${requested_version}, but the newest entry, v${newest_entry}, is older than the previous release ${previous_tag}"
	fi
	fail "release:${requested_bump} expects a CHANGELOG entry for v${requested_version}, but the newest entry is still the previous release v${newest_entry}"
fi

if [ "${newest_entry}" != "${requested_version}" ]; then
	fail "release:${requested_bump} implies v${requested_version}, but the newest CHANGELOG entry is v${newest_entry}"
fi

echo "release:${requested_bump} cuts v${requested_version}, matching the newest CHANGELOG entry"
