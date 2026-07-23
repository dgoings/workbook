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
if [ "$#" -gt 1 ]; then
	echo "usage: scripts/install.sh [destination]" >&2
	exit 2
fi

if [ "$#" -eq 1 ]; then
	destination=$1
else
	destination=${HOME}/.local/bin
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
repository_root=$(CDPATH= cd -- "${script_directory}/.." && pwd)

mkdir -p -- "${destination}"
(
	cd -- "${repository_root}"
	go build -trimpath -o "${destination}/workbook" ./cmd/workbook
)

echo "Installed Workbook at ${destination}/workbook"
case ":${PATH}:" in
	*:"${destination}":*) ;;
	*) echo "export PATH=\"${destination}:\$PATH\"" ;;
esac
