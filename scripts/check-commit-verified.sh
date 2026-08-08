#!/bin/sh
set -eu

# Reports whether CI verified a commit, before a release tag is created for it.
#
# The cut workflows push a tag and only then does the release workflow test and
# build. A commit that fails its tests therefore burns a version number: the tag
# exists, nothing is published, and the version can never be reused because
# releases only order forward. Checking that CI already passed for the exact
# commit closes most of that window, because a failing test suite is much the
# likeliest way for a release to die after its tag exists.
#
# The check is on named check runs rather than on a count, so adding a platform
# to the CI matrix automatically makes that platform required here too.

usage() {
	cat <<'USAGE'
usage: scripts/check-commit-verified.sh <commit> [options]

Exits 0 when every CI check run for the commit whose name begins with the
prefix has completed successfully, and 1 when any is missing, pending, or
unsuccessful.

Options:
  --repo REPOSITORY  owner/name to query (default: $GITHUB_REPOSITORY)
  --prefix PREFIX    check run name prefix to require (default "Verify on ")
  -h, --help         show this message
USAGE
}

fail() {
	echo "workbook release: $1" >&2
	exit "${2:-2}"
}

commit=
repository=${GITHUB_REPOSITORY:-}
# ci.yml names its matrix job "Verify on ${{ matrix.os }}", so this prefix
# matches every platform it verifies and nothing else.
prefix='Verify on '

while [ "$#" -gt 0 ]; do
	case $1 in
		--repo)
			[ "$#" -ge 2 ] || fail "--repo requires a value"
			repository=$2
			shift
			;;
		--repo=*)
			repository=${1#--repo=}
			;;
		--prefix)
			[ "$#" -ge 2 ] || fail "--prefix requires a value"
			prefix=$2
			shift
			;;
		--prefix=*)
			prefix=${1#--prefix=}
			;;
		-h | --help)
			usage
			exit 0
			;;
		-*)
			usage >&2
			fail "unknown option: $1"
			;;
		*)
			[ -z "${commit}" ] || fail "only one commit may be given"
			commit=$1
			;;
	esac
	shift
done

if [ -z "${commit}" ]; then
	usage >&2
	fail "a commit is required"
fi
if [ -z "${repository}" ]; then
	fail "a repository is required; pass --repo or set GITHUB_REPOSITORY"
fi
if ! command -v gh >/dev/null 2>&1; then
	fail "gh is required to read check runs but was not found in PATH"
fi

if ! runs=$(gh api "repos/${repository}/commits/${commit}/check-runs" --paginate \
	--jq '.check_runs[] | [.name, .status, .conclusion] | @tsv' 2>/dev/null); then
	fail "could not read check runs for ${commit} in ${repository}"
fi

# Reading from a file rather than a pipe keeps the loop in this shell, so the
# counts it accumulates survive it.
runs_file=$(mktemp "${TMPDIR:-/tmp}/workbook-check-runs.XXXXXX")
trap 'rm -f -- "${runs_file}"' EXIT HUP INT TERM
printf '%s\n' "${runs}" > "${runs_file}"

matched=0
unverified=0
tab=$(printf '\t')
while IFS="${tab}" read -r name status conclusion; do
	[ -n "${name}" ] || continue
	# Quoting the prefix keeps any character in it literal; the trailing * is
	# the only pattern.
	case ${name} in
		"${prefix}"*) ;;
		*) continue ;;
	esac
	matched=$((matched + 1))
	if [ "${status}" != completed ]; then
		echo "workbook release: ${name} is ${status} for ${commit}; wait for it to finish" >&2
		unverified=$((unverified + 1))
		continue
	fi
	if [ "${conclusion}" != success ]; then
		echo "workbook release: ${name} concluded ${conclusion} for ${commit}" >&2
		unverified=$((unverified + 1))
	fi
done < "${runs_file}"

if [ "${matched}" -eq 0 ]; then
	echo "workbook release: no check run named \"${prefix}...\" has run for ${commit}" >&2
	exit 1
fi
if [ "${unverified}" -ne 0 ]; then
	exit 1
fi

echo "${matched} CI check run(s) verified ${commit}"
