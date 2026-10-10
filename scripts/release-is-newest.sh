#!/bin/sh
set -eu

# Answers whether a release tag is the newest of its sequence. Publishing
# consults it before moving anything that points at "the current release":
# GitHub's Latest badge, the Homebrew tap, the desktop cascade and the rolling
# desktop-latest release. An older tag still publishes its own release; it just
# never moves any of those backward.
#
# A stable tag is the newest when no stable tag of its sequence orders after
# it. Pre-release tags do not count against it: a candidate is not a release,
# so v0.6.1 cut while v0.7.0-rc1 is out is still the newest release. A
# pre-release is the newest when no tag of its sequence at all orders after it,
# the rule plan-desktop-release.sh already applies, so a rerun of rc1 after rc2
# is an older one.
#
# The CLI's v* tags and the desktop app's desktop-v* tags are separate
# sequences and never compared with each other. Tags are read from the current
# directory's repository, which has to carry every release tag: a shallow
# checkout that knew of no newer tag would answer "newest" for anything. The
# tag itself is required to be there, which catches a checkout with no tags at
# all.
#
# Exits 0 when the tag is the newest, 1 when a newer one exists, printing that
# tag, and 2 when the question cannot be answered.

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/release-is-newest.sh <tag>" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

tag=$1
# The validators own the grammar and exit 2 on anything malformed.
case "${tag}" in
	desktop-*)
		prefix=desktop-v
		version=$("${script_directory}/validate-desktop-release-tag.sh" "${tag}")
		;;
	*)
		prefix=v
		version=$("${script_directory}/validate-release-tag.sh" "${tag}")
		;;
esac

if ! git rev-parse --verify --quiet "refs/tags/${tag}^{commit}" >/dev/null 2>&1; then
	echo "release-is-newest: ${tag} is not a tag in this repository; ordering it needs a checkout carrying every release tag" >&2
	exit 2
fi

kind=stable
if is_prerelease_version "${version}"; then
	kind=any
fi
newest_tag=$(newest_release_tag "${kind}" "" "${prefix}")

if [ -n "${newest_tag}" ] && release_version_before "${version}" "${newest_tag#"${prefix}"}"; then
	printf '%s\n' "${newest_tag}"
	exit 1
fi
