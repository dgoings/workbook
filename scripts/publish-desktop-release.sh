#!/bin/sh
set -eu

# Publishes the desktop app's release for a desktop-vX.Y.Z tag, then points the
# rolling desktop-latest tag and release at it.
#
# Two releases carry one build because they answer two questions. The versioned
# release is the record of what shipped and is written once: someone who has
# downloaded desktop-v0.6.0 must always get the same bytes back. The rolling one
# is the address the download links and the app's updater are pinned to, so it
# is rewritten on every release and has to be refreshed even on a rerun that
# creates nothing.
#
# The rolling release is touched only after the versioned one is published, and
# never before: desktop-latest naming a release that failed to publish sends
# every download to a page that does not exist.

usage() {
	cat <<'USAGE'
usage: scripts/publish-desktop-release.sh <tag> <dist-dir> <repository> [--bundles CLI_TAG]

Publishes the desktop release for <tag> from the artifacts in <dist-dir>, then
refreshes the rolling desktop-latest tag and release to match it. Git commands
run against the current directory's repository, which the workflow has checked
out at <tag>.

Options:
  --bundles CLI_TAG  the Workbook CLI release this build bundles, named in the notes
  -h, --help         show this message
USAGE
}

fail() {
	echo "workbench release: $1" >&2
	exit "${2:-1}"
}

tag=
distribution_directory=
repository=
cli_tag=
positional=0

while [ "$#" -gt 0 ]; do
	case $1 in
		--bundles)
			[ "$#" -ge 2 ] || fail "--bundles requires a value" 2
			cli_tag=$2
			shift
			;;
		--bundles=*)
			cli_tag=${1#--bundles=}
			;;
		-h | --help)
			usage
			exit 0
			;;
		-*)
			usage >&2
			fail "unknown option: $1" 2
			;;
		*)
			positional=$((positional + 1))
			case ${positional} in
				1) tag=$1 ;;
				2) distribution_directory=$1 ;;
				3) repository=$1 ;;
				*)
					usage >&2
					fail "unexpected argument: $1" 2
					;;
			esac
			;;
	esac
	shift
done

if [ "${positional}" -ne 3 ]; then
	usage >&2
	fail "a tag, a distribution directory and a repository are required" 2
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
# shellcheck source=scripts/release-version.sh
. "${script_directory}/release-version.sh"

# The tag validator owns the grammar, so this script never has its own idea of
# what a desktop version looks like; it exits 2 on anything malformed.
version=$("${script_directory}/validate-desktop-release-tag.sh" "${tag}")
prerelease=no
if is_prerelease_version "${version}"; then
	prerelease=yes
fi

distribution_directory=$(CDPATH='' cd -- "${distribution_directory}" && pwd -P)

# Everything the build left directly in dist/ is a release asset, with one
# exception: electron-builder writes builder-debug.yml there as a log of its own
# run. Excluding it by name rather than by a pattern keeps the update manifests,
# latest-mac.yml and its siblings, which the app's updater reads and which would
# be lost to any rule broad enough to catch the debug log.
excluded_name=builder-debug.yml

asset_names=
for asset_path in "${distribution_directory}"/*; do
	[ -f "${asset_path}" ] || continue
	asset_name=${asset_path##*/}
	if [ "${asset_name}" = "${excluded_name}" ]; then
		continue
	fi
	# The asset list is a space-separated word list, as in publish-release.sh,
	# so a name with whitespace in it would split into assets that do not
	# exist and take the release down with it.
	case "${asset_name}" in
		*[[:space:]]*) fail "release asset ${asset_name} has whitespace in its name" ;;
	esac
	asset_names="${asset_names:+${asset_names} }${asset_name}"
done

# A build missing a platform is broken, not partial, and the check runs before
# anything reaches GitHub: finding out half-way through leaves a draft release
# for a build that can never ship, and the rerun then refuses to publish over
# it. The two disk images are required by exact name because those are the
# names the updater's manifests point at. The Linux and Windows installers
# carry electron-builder's own naming, which is not pinned here, so each is
# required by kind instead; a whole missing platform is the failure worth
# catching, and a renamed installer is caught by the manifest that names it.
for required_name in Workbench-arm64.dmg Workbench-x64.dmg; do
	if [ ! -f "${distribution_directory}/${required_name}" ]; then
		fail "missing release asset ${required_name}"
	fi
done
for required_pattern in '*.AppImage' '*.deb' '*.exe'; do
	pattern_matched=no
	for asset_name in ${asset_names}; do
		# The pattern names a kind of installer rather than one file, so it
		# stays unquoted here and matches as the glob it is.
		# shellcheck disable=SC2254
		case "${asset_name}" in
			${required_pattern}) pattern_matched=yes ;;
		esac
	done
	if [ "${pattern_matched}" = no ]; then
		fail "missing release asset matching ${required_pattern}"
	fi
done

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbench-publish.XXXXXX")
created_release=0
completed=0

rollback() {
	if [ "${created_release}" -eq 1 ]; then
		# Only a draft this run created may be deleted, and only after a fresh
		# look confirms it is still one. A publish that failed ambiguously may
		# have landed, and deleting a public release to tidy up after an error
		# is worse than leaving the error behind.
		rollback_release_is_draft=
		if rollback_release_is_draft=$(gh release view "${tag}" --repo "${repository}" --json isDraft --jq .isDraft 2>/dev/null) &&
			[ "${rollback_release_is_draft}" = true ]; then
			echo "workbench release: deleting confirmed draft ${tag}" >&2
			if gh release delete "${tag}" --repo "${repository}" --yes; then
				:
			else
				echo "workbench release: automatic draft deletion failed" >&2
			fi
		else
			echo "workbench release: preserving ${tag}; rollback could not confirm it is still a draft" >&2
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
	# The versioned release is written once. A rerun is allowed only when it is
	# the same build, so the existing release has to hold exactly these file
	# names with exactly these bytes: a different set of names means the build
	# changed shape, and different bytes mean someone would get something other
	# than what they already downloaded.
	existing_directory="${temporary_directory}/existing"
	mkdir -p -- "${existing_directory}"
	gh release download "${tag}" --repo "${repository}" --dir "${existing_directory}"

	for existing_asset in "${existing_directory}"/*; do
		# A release with no assets at all leaves the pattern unmatched; the
		# missing-asset check below reports that case in its own words.
		[ -e "${existing_asset}" ] || continue
		if [ ! -f "${existing_asset}" ]; then
			fail "existing release has an unexpected non-file asset"
		fi
		existing_name=${existing_asset##*/}
		case " ${asset_names} " in
			*" ${existing_name} "*) ;;
			*) fail "existing release has unexpected asset ${existing_name}" ;;
		esac
	done
	for asset_name in ${asset_names}; do
		if [ ! -f "${existing_directory}/${asset_name}" ]; then
			fail "existing release is missing asset ${asset_name}"
		fi
		if ! cmp -s \
			"${distribution_directory}/${asset_name}" \
			"${existing_directory}/${asset_name}"; then
			fail "${asset_name} does not match existing release asset"
		fi
	done
	echo "workbench release: ${tag} already holds this build; leaving it as it is" >&2
else
	set --
	for asset_name in ${asset_names}; do
		set -- "$@" "${distribution_directory}/${asset_name}"
	done
	# The app and the CLI version independently, so "which workbook is inside
	# this build?" has no answer on the release page unless the notes give one.
	if [ -n "${cli_tag}" ]; then
		notes="Workbench ${tag}, bundling Workbook ${cli_tag}."
	else
		notes="Workbench ${tag}."
	fi
	if [ "${prerelease}" = yes ]; then
		set -- "$@" --prerelease
	fi
	# --verify-tag refuses to invent a tag: the release names the commit that
	# was built, or it is not created. --draft holds the page back until every
	# asset has landed, so nobody downloads half a release.
	gh release create "${tag}" \
		"$@" \
		--repo "${repository}" \
		--verify-tag \
		--draft \
		--title "Workbench ${tag}" \
		--notes "${notes}"
	created_release=1
	release_is_draft=true
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

# Past this point the versioned release exists and is published, so the rolling
# release can be pointed at it. The tag moves first: the release is created
# against a tag, and a release whose tag names the previous build would serve
# the wrong source alongside the right binaries.
released_commit=$(git rev-parse "refs/tags/${tag}^{commit}")
git tag --force desktop-latest "${released_commit}"
git push --force origin refs/tags/desktop-latest

set --
for asset_name in ${asset_names}; do
	set -- "$@" "${distribution_directory}/${asset_name}"
done

if gh release view desktop-latest --repo "${repository}" >/dev/null 2>&1; then
	# --clobber replaces the previous build's assets in place. Without it the
	# upload fails on every name that already exists and the rolling release
	# keeps serving the release before this one.
	gh release upload desktop-latest "$@" --repo "${repository}" --clobber
	set -- --notes "Rolling release; currently ${tag}."
	# The flag is restated in both directions. Setting it matters for a
	# candidate; clearing it matters more, because a stable release published
	# after one would otherwise stay hidden behind "pre-release" for good.
	if [ "${prerelease}" = yes ]; then
		set -- "$@" --prerelease
	else
		set -- "$@" --prerelease=false
	fi
	gh release edit desktop-latest --repo "${repository}" "$@"
else
	if [ "${prerelease}" = yes ]; then
		set -- "$@" --prerelease
	fi
	gh release create desktop-latest \
		"$@" \
		--repo "${repository}" \
		--title "Workbench (latest)" \
		--notes "Rolling release; currently ${tag}."
fi

completed=1
