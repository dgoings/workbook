#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/validate-release-tag.sh <tag>" >&2
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
	v*) version=${tag#v} ;;
	*)
		echo "workbook release: tag must be vMAJOR.MINOR.PATCH" >&2
		exit 2
		;;
esac
require_safe_release_version "${version}" "workbook release"
printf '%s\n' "${version}"
