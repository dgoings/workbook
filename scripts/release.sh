#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: scripts/release.sh <version> <output-dir>" >&2
	exit 2
fi

version=$1
output_directory=$2

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"
require_safe_release_version "${version}" "workbook release"

repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd)
commit=$(git -C "${repository_root}" rev-parse HEAD)

SOURCE_DATE_EPOCH=0
TZ=UTC
LC_ALL=C
export SOURCE_DATE_EPOCH TZ LC_ALL

mkdir -p -- "${output_directory}"
output_directory=$(CDPATH='' cd -- "${output_directory}" && pwd -P)
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbook-release.XXXXXX")
trap 'rm -rf -- "${temporary_directory}"' EXIT HUP INT TERM

(
	cd -- "${repository_root}"
	CGO_ENABLED=0 go build -buildvcs=false -trimpath \
		-o "${temporary_directory}/archive-tool" ./internal/release/archivecmd
)

for architecture in amd64 arm64; do
	archive_name="workbook_${version}_darwin_${architecture}.tar.gz"
	binary_path="${temporary_directory}/workbook"
	(
		cd -- "${repository_root}"
		CGO_ENABLED=0 GOOS=darwin GOARCH="${architecture}" go build -buildvcs=false -trimpath \
			-ldflags "-X main.version=${version} -X main.commit=${commit}" \
			-o "${binary_path}" ./cmd/workbook
	)
	"${temporary_directory}/archive-tool" \
		"${binary_path}" \
		"${output_directory}/${archive_name}"
done

(
	cd -- "${output_directory}"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 workbook_"${version}"_darwin_amd64.tar.gz workbook_"${version}"_darwin_arm64.tar.gz
	else
		sha256sum workbook_"${version}"_darwin_amd64.tar.gz workbook_"${version}"_darwin_arm64.tar.gz
	fi
) > "${output_directory}/checksums.txt"
