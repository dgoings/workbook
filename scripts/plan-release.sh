#!/bin/sh
set -eu

# Decides which version a cut would release, and refuses when it should not
# happen at all.
#
# Both cut workflows ask the same four questions in the same order: which
# release came last, whether that release actually published, which version
# follows it, and whether the changelog agrees. Answering them here keeps the
# workflows to a single call each and keeps the decision testable without
# GitHub.
#
# Prints the resolved version on standard output and everything else on standard
# error, so a caller can capture the version directly.

usage() {
	cat <<'USAGE'
usage: scripts/plan-release.sh --bump KIND [options]

Prints the MAJOR.MINOR.PATCH version a release would carry.

Options:
  --bump KIND        patch, minor, or major
  --version VERSION  explicit version; overrides --bump when non-empty
  --repo REPOSITORY  owner/name to query for release state (default: $GITHUB_REPOSITORY)
  --previous TAG     previous release tag (default: newest v* tag in this repository)
  --changelog PATH   changelog to read (default: CHANGELOG.md in this repository)
  -h, --help         show this message
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-1}"
}

requested_bump=
requested_version=
repository=${GITHUB_REPOSITORY:-}
previous_tag=
previous_given=no
changelog=

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
		--repo)
			[ "$#" -ge 2 ] || fail "--repo requires a value" 2
			repository=$2
			shift
			;;
		--repo=*)
			repository=${1#--repo=}
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

if [ -z "${requested_bump}" ] && [ -z "${requested_version}" ]; then
	usage >&2
	fail "either --bump or --version is required" 2
fi
if [ "${previous_given}" = no ]; then
	previous_tag=$(git tag --list 'v[0-9]*' --sort=-v:refname | head -n 1)
fi

# A previous release that never published leaves its changelog entry describing
# something nobody can download. The changelog check needs to know, so that it
# refuses rather than quietly cutting past it.
previous_released=yes
if [ -n "${previous_tag}" ]; then
	if ! "${script_directory}/check-release-published.sh" "${previous_tag}" --repo "${repository}" >/dev/null; then
		previous_released=no
		echo "workbook release: ${previous_tag} published no release" >&2
	fi
fi

version=$("${script_directory}/resolve-release-version.sh" \
	--bump "${requested_bump}" \
	--version "${requested_version}" \
	--previous "${previous_tag}")

# An explicit version's changelog requirement follows the distance it actually
# travels, not the bump kind it was typed beside. Choosing "patch" and typing
# 1.0.0 would otherwise skip the entry a major release has to carry.
effective_bump=${requested_bump:-patch}
if [ -n "${requested_version}" ]; then
	if [ -n "${previous_tag}" ]; then
		previous_number=${previous_tag#v}
	else
		previous_number=0.0.0
	fi
	previous_major=${previous_number%%.*}
	previous_minor=${previous_number#*.}
	previous_minor=${previous_minor%%.*}
	version_major=${version%%.*}
	version_minor=${version#*.}
	version_minor=${version_minor%%.*}
	if [ "${version_major}" != "${previous_major}" ]; then
		effective_bump='major'
	elif [ "${version_minor}" != "${previous_minor}" ]; then
		effective_bump='minor'
	else
		effective_bump='patch'
	fi
fi

set -- \
	--bump "${effective_bump}" \
	--version "${version}" \
	--previous "${previous_tag}" \
	--previous-released "${previous_released}"
if [ -n "${changelog}" ]; then
	set -- "$@" --changelog "${changelog}"
fi
"${script_directory}/check-release-changelog.sh" "$@" >&2

echo "${version}"
