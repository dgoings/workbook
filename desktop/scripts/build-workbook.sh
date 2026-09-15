#!/bin/sh
# Build the Workbook CLI from source and stage it for bundling.
#
# Workbench ships its own Workbook rather than requiring one to be installed, so
# this runs as part of every packaged build. It delegates to Workbook's own
# scripts/install.sh instead of invoking `go build` here, so the binary is
# stamped the way an official source install is: -trimpath, with version and
# commit derived from the checkout by `git describe`. Reimplementing that would
# drift from upstream the first time they changed it.
#
# The ref is pinned in package.json under "workbook", so a change upstream can
# never silently alter a Workbench release. Bump it deliberately.
#
#   WORKBOOK_REPO  use this checkout as-is instead of cloning (local development)
#   WORKBOOK_REF   override the pinned ref
#
# With no WORKBOOK_REPO — which is the case in CI — the pinned ref is cloned
# into build/workbook-src. A local checkout is used exactly as it stands,
# because reaching into someone's working tree to change its checked-out
# revision is not this script's business.

set -eu

script_directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd)

if ! command -v go >/dev/null 2>&1; then
	echo "build-workbook: go is required to build Workbook from source." >&2
	echo "  macOS: brew install go" >&2
	exit 1
fi
if ! command -v node >/dev/null 2>&1; then
	echo "build-workbook: node is required to read the pinned ref." >&2
	exit 1
fi

# Read the pin with node's working directory set and a relative path, never an
# absolute one embedded in the expression. Under Git Bash the shell's paths are
# POSIX (/d/a/...) while node is a native Windows binary that cannot resolve
# them; MSYS rewrites arguments that look like paths, but not a path inside a
# JavaScript string.
pinned_url=$(cd -- "${project_root}" && node -p "require('./package.json').workbook.repository")
pinned_ref=${WORKBOOK_REF:-$(cd -- "${project_root}" && node -p "require('./package.json').workbook.ref")}

if [ -n "${WORKBOOK_REPO:-}" ]; then
	repo=${WORKBOOK_REPO}
	if [ ! -d "${repo}/.git" ]; then
		echo "build-workbook: WORKBOOK_REPO is not a git checkout: ${repo}" >&2
		exit 1
	fi
	echo "build-workbook: using local checkout ${repo} (ref pin ${pinned_ref} not applied)"
else
	repo="${project_root}/build/workbook-src"
	if [ ! -d "${repo}/.git" ]; then
		echo "build-workbook: cloning ${pinned_url}"
		mkdir -p -- "${project_root}/build"
		git clone --quiet "${pinned_url}" "${repo}"
	fi
	echo "build-workbook: checking out ${pinned_ref}"
	# A pin may be a tag or an exact commit. Fetching the ref by name covers a
	# tag or branch; the bare fetch that follows covers a commit that no ref
	# points at any more, which a `--tags` fetch alone would never retrieve.
	git -C "${repo}" fetch --quiet --tags origin || true
	git -C "${repo}" fetch --quiet origin "${pinned_ref}" 2>/dev/null || true
	git -C "${repo}" checkout --quiet --detach "${pinned_ref}"
fi

# `go build -o <name>` writes exactly the name it is given — it does not append
# .exe on Windows — and Workbench looks for workbook.exe there, so the name is
# decided here rather than left to the toolchain.
case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) binary_name=workbook.exe ;;
	*) binary_name=workbook ;;
esac

mkdir -p -- "${project_root}/build"
echo "build-workbook: building from $(git -C "${repo}" rev-parse --short HEAD) as ${binary_name}"
"${repo}/scripts/install.sh" "${project_root}/build" "${binary_name}"

# The MIT licence travels with the binary: the app redistributes it.
cp -- "${repo}/LICENSE" "${project_root}/build/WORKBOOK-LICENSE"

echo "build-workbook: staged $("${project_root}/build/${binary_name}" version)"
