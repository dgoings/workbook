#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: scripts/publish-release.sh <tag> <dist-dir> <tap-dir> <repository>" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac

tag=$1
distribution_directory=$2
tap_directory=$3
repository=$4
version=$("${script_directory}/validate-release-tag.sh" "${tag}")

distribution_directory=$(CDPATH='' cd -- "${distribution_directory}" && pwd -P)
tap_directory=$(CDPATH='' cd -- "${tap_directory}" && pwd -P)

arm64_archive="workbook_${version}_darwin_arm64.tar.gz"
amd64_archive="workbook_${version}_darwin_amd64.tar.gz"
asset_names="${amd64_archive} ${arm64_archive} checksums.txt"
for asset_name in ${asset_names}; do
	if [ ! -f "${distribution_directory}/${asset_name}" ]; then
		echo "workbook release: missing release asset ${asset_name}" >&2
		exit 1
	fi
done

(
	cd -- "${distribution_directory}"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c checksums.txt
	else
		sha256sum -c checksums.txt
	fi
) >/dev/null

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbook-publish.XXXXXX")
created_release=0
tap_pushed=0
tap_commit=
completed=0

rollback() {
	if [ "${tap_pushed}" -eq 1 ]; then
		echo "workbook release: reverting tap commit ${tap_commit}" >&2
		if git -C "${tap_directory}" revert --no-edit "${tap_commit}" &&
			git -C "${tap_directory}" push origin HEAD; then
			:
		else
			echo "workbook release: automatic tap rollback failed" >&2
		fi
	fi
	if [ "${created_release}" -eq 1 ]; then
		rollback_release_is_draft=
		if rollback_release_is_draft=$(gh release view "${tag}" --repo "${repository}" --json isDraft --jq .isDraft 2>/dev/null) &&
			[ "${rollback_release_is_draft}" = true ]; then
			echo "workbook release: deleting confirmed draft ${tag}" >&2
			if gh release delete "${tag}" --repo "${repository}" --yes; then
				:
			else
				echo "workbook release: automatic draft deletion failed" >&2
			fi
		else
			echo "workbook release: preserving ${tag}; rollback could not confirm it is still a draft" >&2
		fi
	fi
}

finish() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "${completed}" -ne 1 ]; then
		rollback
	fi
	rm -rf -- "${temporary_directory}"
	exit "${status}"
}
trap finish EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

release_is_draft=
if release_is_draft=$(gh release view "${tag}" --repo "${repository}" --json isDraft --jq .isDraft 2>/dev/null); then
	existing_directory="${temporary_directory}/existing"
	mkdir -p -- "${existing_directory}"
	gh release download "${tag}" --repo "${repository}" --dir "${existing_directory}"

	asset_count=0
	for existing_asset in "${existing_directory}"/*; do
		if [ ! -f "${existing_asset}" ]; then
			echo "workbook release: existing release has an unexpected non-file asset" >&2
			exit 1
		fi
		existing_name=${existing_asset##*/}
		case " ${asset_names} " in
			*" ${existing_name} "*) ;;
			*)
				echo "workbook release: existing release has unexpected asset ${existing_name}" >&2
				exit 1
				;;
		esac
		asset_count=$((asset_count + 1))
	done
	if [ "${asset_count}" -ne 3 ]; then
		echo "workbook release: existing release must contain exactly three assets" >&2
		exit 1
	fi
	for asset_name in ${asset_names}; do
		if ! cmp -s \
			"${distribution_directory}/${asset_name}" \
			"${existing_directory}/${asset_name}"; then
			echo "workbook release: ${asset_name} does not match existing release asset" >&2
			exit 1
		fi
	done
else
	gh release create "${tag}" \
		"${distribution_directory}/${arm64_archive}" \
		"${distribution_directory}/${amd64_archive}" \
		"${distribution_directory}/checksums.txt" \
		--repo "${repository}" \
		--verify-tag \
		--draft \
		--generate-notes \
		--title "Workbook ${tag}"
	created_release=1
	release_is_draft=true
fi

"${script_directory}/render-homebrew-formula.sh" \
	"${version}" \
	"${distribution_directory}/checksums.txt" \
	"${tap_directory}/Formula/workbook.rb" \
	"${repository}"

git -C "${tap_directory}" config user.name "github-actions[bot]"
git -C "${tap_directory}" config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git -C "${tap_directory}" add Formula/workbook.rb
if ! git -C "${tap_directory}" diff --cached --quiet; then
	git -C "${tap_directory}" commit -m "workbook ${version}"
	tap_commit=$(git -C "${tap_directory}" rev-parse HEAD)
	git -C "${tap_directory}" push origin HEAD
	tap_pushed=1
fi

if [ "${release_is_draft}" = true ]; then
	gh release edit "${tag}" --repo "${repository}" --draft=false
fi

completed=1
