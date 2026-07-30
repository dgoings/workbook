#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: scripts/render-homebrew-formula.sh <version> <checksums-file> <output-file> <repository>" >&2
	exit 2
fi

version=$1
checksums_file=$2
output_file=$3
repository=$4

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"
require_safe_release_version "${version}" "workbook formula"

if [ ! -f "${checksums_file}" ]; then
	echo "workbook formula: checksums file does not exist: ${checksums_file}" >&2
	exit 2
fi
case "${repository}" in
	*/*/* | /* | */ | "")
		echo "workbook formula: repository must be an owner/name pair" >&2
		exit 2
		;;
esac

checksum_for() {
	archive_name=$1
	awk -v archive_name="${archive_name}" '
		$2 == archive_name { count++; checksum = $1 }
		END {
			if (count != 1 || checksum !~ /^[[:xdigit:]]{64}$/) {
				exit 1
			}
			print tolower(checksum)
		}
	' "${checksums_file}"
}

arm64_archive="workbook_${version}_darwin_arm64.tar.gz"
amd64_archive="workbook_${version}_darwin_amd64.tar.gz"
if ! arm64_checksum=$(checksum_for "${arm64_archive}"); then
	echo "workbook formula: expected exactly one SHA-256 checksum for ${arm64_archive}" >&2
	exit 1
fi
if ! amd64_checksum=$(checksum_for "${amd64_archive}"); then
	echo "workbook formula: expected exactly one SHA-256 checksum for ${amd64_archive}" >&2
	exit 1
fi

repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd -P)

output_directory=$(dirname -- "${output_file}")
mkdir -p -- "${output_directory}"
# The formula template lives in internal/release so that the Go renderer and
# this script cannot drift apart. Render through it rather than duplicating it.
build_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbook-formula.XXXXXX")
temporary_file=$(mktemp "${output_directory}/.workbook-formula.XXXXXX")
trap 'rm -rf -- "${build_directory}"; rm -f -- "${temporary_file}"' EXIT HUP INT TERM

(
	cd -- "${repository_root}"
	CGO_ENABLED=0 go build -buildvcs=false -trimpath \
		-o "${build_directory}/formula-tool" ./internal/release/formulacmd
)

"${build_directory}/formula-tool" \
	"${version}" \
	"${arm64_checksum}" \
	"${amd64_checksum}" \
	"${repository}" > "${temporary_file}"

chmod 0644 "${temporary_file}"
mv -- "${temporary_file}" "${output_file}"
