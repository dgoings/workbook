#!/bin/sh
set -eu

if ! command -v go >/dev/null 2>&1; then
	echo "workbook installer: go is required but was not found in PATH" >&2
	exit 1
fi
if ! command -v git >/dev/null 2>&1; then
	echo "workbook installer: git is required but was not found in PATH" >&2
	exit 1
fi
if [ "$#" -gt 2 ]; then
	echo "usage: scripts/install.sh [destination] [name]" >&2
	exit 2
fi

if [ "$#" -ge 1 ]; then
	destination=$1
else
	destination=${HOME}/.local/bin
fi

# A second name lets a source build sit beside a released install in the same
# directory instead of shadowing it.
if [ "$#" -eq 2 ]; then
	name=$2
else
	name=workbook
fi
case ${name} in
	"" | */* | .*)
		echo "workbook installer: binary name must not be empty, contain a path separator, or begin with a dot" >&2
		exit 2
		;;
esac

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
repository_root=$(CDPATH= cd -- "${script_directory}/.." && pwd)

# Stamp the build so a source install reports which commit it came from. A
# leading "v" distinguishes these from released artifacts, which report a bare
# MAJOR.MINOR.PATCH, and "-dirty" marks a build from a modified tree.
version=$(git -C "${repository_root}" describe --tags --always --dirty 2>/dev/null || echo dev)
commit=$(git -C "${repository_root}" rev-parse HEAD 2>/dev/null || echo unknown)

mkdir -p -- "${destination}"
destination=$(CDPATH= cd -- "${destination}" && pwd -P)
(
	cd -- "${repository_root}"
	go build -trimpath \
		-ldflags "-X main.version=${version} -X main.commit=${commit}" \
		-o "${destination}/${name}" ./cmd/workbook
)

echo "Installed Workbook at ${destination}/${name}"
case ":${PATH}:" in
	*:"${destination}":*) ;;
	*) echo "export PATH=\"${destination}:\$PATH\"" ;;
esac
