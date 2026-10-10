#!/bin/sh
set -eu

# Refuses a CLI release tag that does not sit on a branch releases are
# published from. The tag has to be reachable from a supported branch on the
# remote, and no higher stable release may already be part of its history: an
# older version tagged on newer code would publish the newer code under the
# older number.
#
# The remote's branches are read from the current directory's repository, so
# the checkout has to carry them along with every tag; the release workflow's
# full-depth checkout does. Prints the branch the tag is on.
#
# Exits 0 when the tag is on a supported branch, 1 when it is not, and 2 when
# the question cannot be answered.

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/validate-release-branch.sh <tag>" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

remote=origin
tag=$1
version=$("${script_directory}/validate-release-tag.sh" "${tag}")

if ! commit=$(git rev-parse --verify --quiet "refs/tags/${tag}^{commit}" 2>/dev/null); then
	echo "workbook release: ${tag} is not a tag in this repository" >&2
	exit 2
fi

# Prints the supported branch COMMIT is on, or fails when it is on none. Only
# main is supported today. A maintenance line for an older minor version is a
# second branch this would answer with, chosen from VERSION, so the rest of
# this script holds for one unchanged.
supported_branch_for() {
	sb_commit=$1
	# The version is unused until a branch other than main is supported.
	# shellcheck disable=SC2034
	sb_version=$2
	sb_main="refs/remotes/${remote}/main"
	if ! git rev-parse --verify --quiet "${sb_main}^{commit}" >/dev/null 2>&1; then
		echo "workbook release: ${sb_main} is missing; fetch the remote's branches before validating a release" >&2
		return 2
	fi
	# --is-ancestor answers no with 1 and reports an error with anything else,
	# which must not read as either answer.
	sb_status=0
	git merge-base --is-ancestor "${sb_commit}" "${sb_main}" || sb_status=$?
	case ${sb_status} in
		0) printf '%s\n' main ;;
		1) return 1 ;;
		*) return 2 ;;
	esac
}

branch_status=0
branch=$(supported_branch_for "${commit}" "${version}") || branch_status=$?
case ${branch_status} in
	0) ;;
	1)
		echo "workbook release: ${tag} is not on a branch releases are published from; tag a commit on ${remote}/main" >&2
		exit 1
		;;
	*) exit 2 ;;
esac

# Candidates are not releases, so one in the history does not make this an
# older version on newer code. The newest higher one is named, as it is the
# release the tag would be published behind.
higher_tag=$(git tag --merged "${commit}" --list 'v[0-9]*' | while IFS= read -r merged_tag; do
	merged_version=${merged_tag#v}
	is_safe_release_version "${merged_version}" || continue
	if is_prerelease_version "${merged_version}"; then
		continue
	fi
	if release_version_before "${version}" "${merged_version}"; then
		printf '%s %s\n' "$(release_version_key "${merged_version}")" "${merged_tag}"
	fi
done | sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n | tail -n 1 | awk '{ print $6 }')

if [ -n "${higher_tag}" ]; then
	echo "workbook release: ${tag} is on a commit that already contains ${higher_tag}; an older version cannot be released from newer code" >&2
	exit 1
fi

printf '%s\n' "${branch}"
