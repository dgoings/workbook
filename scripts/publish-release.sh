#!/bin/sh
set -eu

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
	echo "usage: scripts/publish-release.sh <tag> <dist-dir> <tap-dir> <repository> [changelog]" >&2
	exit 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

tag=$1
distribution_directory=$2
tap_directory=$3
repository=$4
# The changelog is optional so the four-argument form keeps working. A release
# with no entry falls back to generated notes.
changelog=${5:-${script_directory}/../CHANGELOG.md}
version=$("${script_directory}/validate-release-tag.sh" "${tag}")
# A pre-release is published as one and stops there: the tap serves brew upgrade
# to everyone who installed a release, and a candidate must never reach them.
prerelease=no
if is_prerelease_version "${version}"; then
	prerelease=yes
fi

distribution_directory=$(CDPATH='' cd -- "${distribution_directory}" && pwd -P)
# Only a stable release reads or writes the tap, and the workflow checks it out
# only for one, so a pre-release is handed a path that need not exist. Resolving
# it here would fail the run over a repository it never touches.
if [ "${prerelease}" = no ]; then
	tap_directory=$(CDPATH='' cd -- "${tap_directory}" && pwd -P)
fi

# Must match the platforms scripts/release.sh builds and the formula serves.
archive_names=
for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
	archive_names="${archive_names:+${archive_names} }workbook_${version}_${platform}.tar.gz"
done
asset_names="${archive_names} checksums.txt"
expected_asset_count=5
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
	if [ "${asset_count}" -ne "${expected_asset_count}" ]; then
		echo "workbook release: existing release must contain exactly ${expected_asset_count} assets" >&2
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
	set --
	for asset_name in ${asset_names}; do
		set -- "$@" "${distribution_directory}/${asset_name}"
	done
	# A hand-written entry is the release's notes. Publishing generated notes
	# beside prose someone wrote for this version is the divergence that makes a
	# changelog stop being worth reading; a release with no entry has nothing to
	# diverge from and keeps the generated ones. A pre-release has no entry of
	# its own and publishes the Unreleased section instead, the changes it is a
	# candidate release of.
	notes_file="${temporary_directory}/notes.md"
	if [ "${prerelease}" = yes ]; then
		notes_entry=unreleased
	else
		notes_entry=${version}
	fi
	if "${script_directory}/changelog-entry.sh" "${notes_entry}" "${changelog}" > "${notes_file}" 2>/dev/null; then
		set -- "$@" --notes-file "${notes_file}"
	else
		set -- "$@" --generate-notes
	fi
	if [ "${prerelease}" = yes ]; then
		set -- "$@" --prerelease
	fi
	gh release create "${tag}" \
		"$@" \
		--repo "${repository}" \
		--verify-tag \
		--draft \
		--title "Workbook ${tag}"
	created_release=1
	release_is_draft=true
fi

if [ "${prerelease}" = no ]; then
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
else
	echo "workbook release: pre-release ${tag}; leaving the Homebrew tap alone" >&2
fi

if [ "${release_is_draft}" = true ]; then
	set -- --draft=false
	# gh patches only the flags it is given, so restate the pre-release one here
	# rather than trust that publishing the draft leaves it alone.
	if [ "${prerelease}" = yes ]; then
		set -- "$@" --prerelease
	fi
	gh release edit "${tag}" --repo "${repository}" "$@"
fi

completed=1
