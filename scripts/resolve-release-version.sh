#!/bin/sh
set -eu

# Resolves the version a release should carry, from a bump kind applied to the
# previous release or from an explicit version that overrides it.
#
# Every path that cuts a release resolves through here: the local
# cut-release.sh, the workflow_dispatch button, and the merged release pull
# request. The arithmetic deciding which version follows which therefore exists
# once, and the three cannot drift apart.
#
# release-version.sh's helpers assign version, major, minor, and patch without
# declaring them local, which POSIX sh has no way to do. Every variable here is
# named so that sourcing and calling them cannot overwrite it.

usage() {
	cat <<'USAGE'
usage: scripts/resolve-release-version.sh [--bump KIND] [--version VERSION] [--previous TAG]

Prints the resolved MAJOR.MINOR.PATCH version.

Options:
  --bump KIND       patch, minor, or major, applied to the previous release
  --version VER     explicit version; overrides --bump when non-empty
  --previous TAG    previous release tag (default: newest v* tag in this repository)
  -h, --help        show this message

At least one of --bump and --version must be non-empty. Callers that accept
both from a user pass both unconditionally and let --version win here, so the
precedence is decided in one tested place rather than at each call site.
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

if [ -z "${requested_bump}" ] && [ -z "${requested_version}" ]; then
	usage >&2
	fail "either --bump or --version is required" 2
fi
if [ -z "${requested_version}" ]; then
	case ${requested_bump} in
		patch | minor | major) ;;
		*) fail "--bump must be patch, minor, or major" 2 ;;
	esac
fi

# Discovering the previous release is a convenience for interactive use. Every
# workflow passes it explicitly, which keeps the resolution a pure function of
# its arguments and lets the tests exercise it without building a repository.
if [ "${previous_given}" = no ]; then
	previous_tag=$(git tag --list 'v[0-9]*' --sort=-v:refname | head -n 1)
fi

# No release yet orders everything after 0.0.0, so a first bump lands on 0.0.1,
# 0.1.0, or 1.0.0 depending on the kind asked for.
if [ -z "${previous_tag}" ]; then
	previous_number=0.0.0
else
	previous_number=${previous_tag#v}
	if ! is_safe_release_version "${previous_number}"; then
		fail "previous release ${previous_tag} is not a MAJOR.MINOR.PATCH tag"
	fi
fi

if [ -n "${requested_version}" ]; then
	resolved=${requested_version}
else
	previous_major=${previous_number%%.*}
	previous_tail=${previous_number#*.}
	previous_minor=${previous_tail%%.*}
	previous_patch=${previous_tail#*.}
	case ${requested_bump} in
		patch) resolved="${previous_major}.${previous_minor}.$((previous_patch + 1))" ;;
		minor) resolved="${previous_major}.$((previous_minor + 1)).0" ;;
		major) resolved="$((previous_major + 1)).0.0" ;;
	esac
fi

# Apply the same rule the release workflow and the formula renderer apply, so a
# version this prints can never be one they would later reject.
require_safe_release_version "${resolved}" "workbook release"

# Version numbers only ever move forward, so catch a version that would order
# below or equal to a release that already shipped.
ordered_last=$(printf '%s\n%s\n' "${previous_number}" "${resolved}" | sort -V | tail -n 1)
if [ "${ordered_last}" != "${resolved}" ] || [ "${previous_number}" = "${resolved}" ]; then
	if [ -z "${previous_tag}" ]; then
		fail "version ${resolved} does not come after 0.0.0"
	fi
	fail "version ${resolved} does not come after the latest release ${previous_tag}"
fi

echo "${resolved}"
