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
  --branch BRANCH    branch being cut: main (the default) or a maint/vMAJOR.MINOR
                     line, where only a stable patch to that line is cut
  --repo REPOSITORY  owner/name to query for release state (default: $GITHUB_REPOSITORY)
  --previous TAG     previous release tag (default: newest v* tag in this
                     repository, or on the line for a maint/vMAJOR.MINOR branch)
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
branch=main
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
		--branch)
			[ "$#" -ge 2 ] || fail "--branch requires a value" 2
			branch=$2
			shift
			;;
		--branch=*)
			branch=${1#--branch=}
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
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

if [ -z "${requested_bump}" ] && [ -z "${requested_version}" ]; then
	usage >&2
	fail "either --bump or --version is required" 2
fi

# A maint/vMAJOR.MINOR branch is a line: it ships stable patches to that one
# minor version, for people who cannot take the next one, and its previous
# release is the line's newest rather than the newest there is. Every other
# release is cut from main.
line=
case ${branch} in
	main) ;;
	maint/*) line=$(release_line_of_branch "${branch}") || exit 2 ;;
	*) fail "releases are cut from main or a maint/vMAJOR.MINOR line branch, not ${branch}" 2 ;;
esac
if [ -n "${line}" ]; then
	# An explicit version overrides the bump, so the version is what has to be
	# on the line; an unsafe one is left to the resolver to report.
	if [ -n "${requested_version}" ]; then
		if is_safe_release_version "${requested_version}"; then
			require_line_release_version "${requested_version}" "${line}" "workbook release" || exit 1
		fi
	elif [ "${requested_bump}" != patch ]; then
		fail "${branch} cuts patch releases only; cut a ${requested_bump} release from main"
	fi
	if [ "${previous_given}" = no ]; then
		previous_tag=$(newest_release_tag stable "" v "${line}")
		if [ -z "${previous_tag}" ]; then
			fail "${branch} has no v${line}.* release to patch; a line starts from a stable release of it"
		fi
	fi
fi

if [ "${previous_given}" = no ] && [ -z "${line}" ]; then
	# A stable release follows the newest stable release and never sees a
	# pre-release tag; a pre-release has to clear every tag there is.
	if [ -n "${requested_version}" ] && is_safe_release_version "${requested_version}" && is_prerelease_version "${requested_version}"; then
		previous_tag=$(newest_release_tag any)
	else
		previous_tag=$(newest_release_tag stable)
	fi
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

# Once a line has its own branch, main has moved past it: the line cuts that
# minor's patches, candidates included, and main cutting one as well would
# interleave two histories under one number, so the line's next patch would
# become the newest release without what main's had shipped. The branch is read
# from origin's remote-tracking refs, which both cut workflows' full-depth
# checkouts carry.
if [ -z "${line}" ]; then
	version_line=$(release_version_line "${version}")
	if git rev-parse --verify --quiet "refs/remotes/origin/maint/v${version_line}^{commit}" >/dev/null 2>&1; then
		fail "main has moved past ${version_line}, which maint/v${version_line} now patches; cut a minor or major release from main, or cut v${version} on maint/v${version_line}"
	fi
fi

# A pre-release is not a release. The changelog keeps its Unreleased heading
# until the stable version is cut, so there is no entry to agree with and the
# check is not run.
if is_prerelease_version "${version}"; then
	echo "workbook release: pre-release v${version} needs no CHANGELOG entry" >&2
	echo "${version}"
	exit 0
fi

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
