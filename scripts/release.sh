#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: scripts/release.sh <version> <output-dir>" >&2
	exit 2
fi

version=$1
output_directory=$2
if [ -z "${version}" ]; then
	echo "workbook release: version must not be empty" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
repository_root=$(CDPATH= cd -- "${script_directory}/.." && pwd)
commit=$(git -C "${repository_root}" rev-parse HEAD)

mkdir -p -- "${output_directory}"
output_directory=$(CDPATH= cd -- "${output_directory}" && pwd -P)
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbook-release.XXXXXX")
trap 'rm -rf -- "${temporary_directory}"' EXIT HUP INT TERM

for architecture in amd64 arm64; do
	archive_name="workbook_${version}_darwin_${architecture}.tar.gz"
	binary_path="${temporary_directory}/workbook"
	(
		cd -- "${repository_root}"
		CGO_ENABLED=0 GOOS=darwin GOARCH="${architecture}" go build -buildvcs=false -trimpath \
			-ldflags "-X main.version=${version} -X main.commit=${commit}" \
			-o "${binary_path}" ./cmd/workbook
	)
	touch -t 197001010000 "${binary_path}"
	COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 --uname root --gname root -cf - -C "${temporary_directory}" workbook | gzip -n > "${output_directory}/${archive_name}"
done

(
	cd -- "${output_directory}"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 workbook_"${version}"_darwin_amd64.tar.gz workbook_"${version}"_darwin_arm64.tar.gz
	else
		sha256sum workbook_"${version}"_darwin_amd64.tar.gz workbook_"${version}"_darwin_arm64.tar.gz
	fi
) > "${output_directory}/checksums.txt"
