#!/bin/sh
set -eu

# The desktop app carries its own tag sequence, desktop-vX.Y.Z, beside the CLI's
# vX.Y.Z. Accepting a CLI tag here would let release.yml's v* trigger and the
# desktop workflow publish the same tag twice, so the prefix is required and
# the grammar after it is the shared one.

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/validate-desktop-release-tag.sh <tag>" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

tag=$1
case "${tag}" in
	desktop-v*) version=${tag#desktop-v} ;;
	*)
		echo "workbench release: tag must be desktop-vMAJOR.MINOR.PATCH, optionally -rcN" >&2
		exit 2
		;;
esac
require_safe_release_version "${version}" "workbench release"
printf '%s\n' "${version}"
