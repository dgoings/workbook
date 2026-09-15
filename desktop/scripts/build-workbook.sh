#!/bin/sh
# Build the Workbook CLI and stage it for bundling.
#
# The desktop app ships its own Workbook rather than requiring one to be
# installed, so this runs as part of every packaged build. It delegates to the
# repository's own scripts/install.sh instead of invoking `go build` here, so the
# binary is stamped the way an official source install is: -trimpath, with
# version and commit derived from the checkout by `git describe`. Reimplementing
# that would drift from the CLI's own install the first time it changed.
#
# In-tree, the source is the checkout this script sits in: desktop/.. There is
# no pinned revision to bump, because the shell and the CLI it ships are one
# repository.
#
#   build-workbook.sh [output-dir]   stage into output-dir (default: desktop/build)
#
#   WORKBOOK_REPO  use this checkout as it stands instead of the enclosing one.
#                  Its checked-out revision is never changed: reaching into
#                  someone's working tree is not this script's business.
#   WORKBOOK_REF   build this ref instead of the working tree. With no
#                  WORKBOOK_REPO, the enclosing checkout is cloned into
#                  <output-dir>/workbook-src and the ref is checked out there.
#                  This is how a release build names a tag.

set -eu

script_directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd)
repository_root=$(CDPATH='' cd -- "${project_root}/.." && pwd)

if [ "$#" -gt 1 ]; then
	echo "usage: scripts/build-workbook.sh [output-dir]" >&2
	exit 2
fi
if [ "$#" -eq 1 ]; then
	output_directory=$1
else
	output_directory="${project_root}/build"
fi

if ! command -v go >/dev/null 2>&1; then
	echo "build-workbook: go is required to build Workbook from source." >&2
	echo "  macOS: brew install go" >&2
	exit 1
fi
if ! command -v git >/dev/null 2>&1; then
	echo "build-workbook: git is required to stamp the build." >&2
	exit 1
fi

mkdir -p -- "${output_directory}"
output_directory=$(CDPATH='' cd -- "${output_directory}" && pwd -P)

if [ -n "${WORKBOOK_REPO:-}" ]; then
	repo=${WORKBOOK_REPO}
	if ! git -C "${repo}" rev-parse --git-dir >/dev/null 2>&1; then
		echo "build-workbook: WORKBOOK_REPO is not a git checkout: ${repo}" >&2
		exit 1
	fi
	if [ -n "${WORKBOOK_REF:-}" ]; then
		echo "build-workbook: using local checkout ${repo} as it stands (WORKBOOK_REF ${WORKBOOK_REF} not applied)" >&2
	else
		echo "build-workbook: using local checkout ${repo}" >&2
	fi
elif [ -n "${WORKBOOK_REF:-}" ]; then
	repo="${output_directory}/workbook-src"
	if [ ! -d "${repo}/.git" ]; then
		echo "build-workbook: cloning ${repository_root}"
		git clone --quiet "${repository_root}" "${repo}"
	fi
	echo "build-workbook: checking out ${WORKBOOK_REF}"
	# A ref may be a tag, a branch, or a commit the clone already has. Fetching
	# by name covers a tag or branch created since the clone was made; the
	# checkout that follows covers a commit no ref points at.
	git -C "${repo}" fetch --quiet --tags origin || true
	git -C "${repo}" fetch --quiet origin "${WORKBOOK_REF}" 2>/dev/null || true
	git -C "${repo}" checkout --quiet --detach "${WORKBOOK_REF}"
else
	repo=${repository_root}
fi

# `go build -o <name>` writes exactly the name it is given, and does not append
# .exe on Windows, so the name is decided here rather than left to the toolchain.
case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) binary_name=workbook.exe ;;
	*) binary_name=workbook ;;
esac

echo "build-workbook: building from $(git -C "${repo}" rev-parse --short HEAD) as ${binary_name}"
"${repo}/scripts/install.sh" "${output_directory}" "${binary_name}"

# The MIT license travels with the binary: the app redistributes it.
cp -- "${repo}/LICENSE" "${output_directory}/WORKBOOK-LICENSE"

echo "build-workbook: staged $("${output_directory}/${binary_name}" version)"
