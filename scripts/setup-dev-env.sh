#!/bin/sh
set -eu

# Installs both Workbook builds side by side so a broken working tree never
# leaves the environment without a usable CLI:
#
#   workbook      the published stable build, kept as a fallback
#   workbook-dev  a build of this working tree
#
# The two live in separate directories and use separate binary names, so
# neither install can shadow or overwrite the other.

usage() {
	cat <<'USAGE'
usage: scripts/setup-dev-env.sh [options]

Installs the published Workbook build as "workbook" and a build of this
working tree as "workbook-dev", in separate directories.

Options:
  --stable-only           install only the published build
  --dev-only              install only the working-tree build
  --stable-method METHOD  auto (default), brew, or source
  --stable-version TAG    release tag to build when the method is source
  --no-profile            do not add the install directories to a shell profile
  -h, --help              show this message

Environment:
  WORKBOOK_STABLE_PREFIX  published install prefix
                          (default ${HOME}/.local/share/workbook/stable)
  WORKBOOK_DEV_PREFIX     working-tree install prefix
                          (default ${HOME}/.local/share/workbook/dev)
  WORKBOOK_SETUP_PROFILE  shell profile to update instead of the detected ones
USAGE
}

fail() {
	echo "workbook setup: $1" >&2
	exit "${2:-1}"
}

install_stable=yes
install_dev=yes
stable_method=auto
stable_version=
update_profile=yes

while [ "$#" -gt 0 ]; do
	case $1 in
		--stable-only)
			install_dev=no
			;;
		--dev-only)
			install_stable=no
			;;
		--stable-method)
			[ "$#" -ge 2 ] || fail "--stable-method requires a value" 2
			stable_method=$2
			shift
			;;
		--stable-method=*)
			stable_method=${1#--stable-method=}
			;;
		--stable-version)
			[ "$#" -ge 2 ] || fail "--stable-version requires a value" 2
			stable_version=$2
			shift
			;;
		--stable-version=*)
			stable_version=${1#--stable-version=}
			;;
		--no-profile)
			update_profile=no
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			fail "unknown option: $1" 2
			;;
	esac
	shift
done

case ${stable_method} in
	auto | brew | source) ;;
	*) fail "--stable-method must be auto, brew, or source" 2 ;;
esac
if [ "${install_stable}" = no ] && [ "${install_dev}" = no ]; then
	fail "--stable-only and --dev-only cannot be combined" 2
fi
if [ -n "${stable_version}" ]; then
	case ${stable_method} in
		brew) fail "--stable-version cannot be used with --stable-method brew" 2 ;;
		# A pinned release tag only means something for a source build, so asking
		# for one selects that method rather than leaving it to detection.
		auto) stable_method=source ;;
	esac
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
repository_root=$(CDPATH= cd -- "${script_directory}/.." && pwd -P)

if [ "${install_stable}" = yes ] && [ "${stable_method}" = auto ]; then
	# The published formula declares depends_on :macos and the release archives
	# are darwin builds, so Homebrew can only serve Workbook on macOS. Everywhere
	# else the released tag is built from source to provide the same fallback.
	if [ "$(uname -s)" = Darwin ] && command -v brew >/dev/null 2>&1; then
		stable_method=brew
	else
		stable_method=source
	fi
fi

builds_from_source=no
if [ "${install_dev}" = yes ]; then
	builds_from_source=yes
elif [ "${stable_method}" = source ]; then
	builds_from_source=yes
fi

if ! command -v git >/dev/null 2>&1; then
	fail "git is required but was not found in PATH"
fi
if [ "${builds_from_source}" = yes ] && ! command -v go >/dev/null 2>&1; then
	fail "go is required to build Workbook from source but was not found in PATH"
fi
if [ "${install_stable}" = yes ] && [ "${stable_method}" = brew ] && ! command -v brew >/dev/null 2>&1; then
	fail "brew is required by --stable-method brew but was not found in PATH"
fi

absolute_directory() {
	mkdir -p -- "$1"
	CDPATH= cd -- "$1" && pwd -P
}

stable_prefix=${WORKBOOK_STABLE_PREFIX:-${HOME}/.local/share/workbook/stable}
dev_prefix=${WORKBOOK_DEV_PREFIX:-${HOME}/.local/share/workbook/dev}
stable_bin=
stable_binary=
stable_description=
dev_bin=
dev_binary=

install_stable_with_brew() {
	if brew list --formula --versions workbook >/dev/null 2>&1; then
		# A failed upgrade still leaves the previously installed formula in place,
		# which is enough for a fallback, so report it and keep going.
		if ! brew upgrade dgoings/tap/workbook; then
			echo "workbook setup: brew upgrade failed; keeping the installed formula" >&2
		fi
	else
		brew install dgoings/tap/workbook
	fi

	if brew_prefix=$(brew --prefix dgoings/tap/workbook 2>/dev/null) && [ -x "${brew_prefix}/bin/workbook" ]; then
		stable_binary=${brew_prefix}/bin/workbook
	elif linked_binary=$(command -v workbook 2>/dev/null); then
		stable_binary=${linked_binary}
	else
		fail "Homebrew did not provide a workbook binary"
	fi
	stable_bin=${stable_binary%/*}
	stable_description="Homebrew formula dgoings/tap/workbook"
}

install_stable_from_source() {
	stable_bin=$(absolute_directory "${stable_prefix}/bin")

	version=${stable_version}
	if [ -z "${version}" ]; then
		if ! git -C "${repository_root}" fetch --tags --quiet origin 2>/dev/null; then
			echo "workbook setup: could not fetch tags from origin; using local tags" >&2
		fi
		version=$(git -C "${repository_root}" tag --list 'v[0-9]*' --sort=-v:refname | head -n 1)
	fi
	if [ -z "${version}" ]; then
		fail "no release tag was found; pass --stable-version to choose one"
	fi
	if ! git -C "${repository_root}" rev-parse --verify --quiet "${version}^{commit}" >/dev/null; then
		fail "release tag ${version} is not available in this clone"
	fi

	# Build the release in a detached worktree so the published build cannot pick
	# up uncommitted work and the working tree is left untouched.
	worktree_parent=$(mktemp -d "${TMPDIR:-/tmp}/workbook-stable.XXXXXX")
	worktree=${worktree_parent}/workbook
	trap 'git -C "${repository_root}" worktree remove --force "${worktree}" >/dev/null 2>&1 || true; rm -rf -- "${worktree_parent}"' EXIT HUP INT TERM
	git -C "${repository_root}" worktree add --detach --quiet "${worktree}" "${version}"
	# Only the destination is passed so that older release tags, whose installer
	# predates the binary-name argument, can still be built.
	"${worktree}/scripts/install.sh" "${stable_bin}" >/dev/null
	git -C "${repository_root}" worktree remove --force "${worktree}"
	rm -rf -- "${worktree_parent}"
	trap - EXIT HUP INT TERM

	stable_binary=${stable_bin}/workbook
	stable_description="source build of ${version}"
}

if [ "${install_stable}" = yes ]; then
	echo "Installing the published Workbook build (${stable_method})..."
	case ${stable_method} in
		brew) install_stable_with_brew ;;
		source) install_stable_from_source ;;
	esac
fi

if [ "${install_dev}" = yes ]; then
	echo "Installing the working-tree Workbook build..."
	dev_bin=$(absolute_directory "${dev_prefix}/bin")
	if [ "${dev_bin}" = "${stable_bin}" ]; then
		fail "the published and working-tree builds must use different directories"
	fi
	"${repository_root}/scripts/install.sh" "${dev_bin}" workbook-dev >/dev/null
	dev_binary=${dev_bin}/workbook-dev
fi

# Collect the directories that belong on PATH. A skipped build still
# contributes its directory when an earlier run already installed there, so
# rebuilding one side does not drop the other from the shell profile.
existing_bin_directory() {
	if [ -d "$1/bin" ]; then
		CDPATH= cd -- "$1/bin" && pwd -P
	fi
}

set --
if [ -n "${stable_bin}" ]; then
	set -- "$@" "${stable_bin}"
else
	retained=$(existing_bin_directory "${stable_prefix}")
	if [ -n "${retained}" ]; then
		set -- "$@" "${retained}"
	fi
fi
if [ -n "${dev_bin}" ]; then
	set -- "$@" "${dev_bin}"
else
	retained=$(existing_bin_directory "${dev_prefix}")
	if [ -n "${retained}" ]; then
		set -- "$@" "${retained}"
	fi
fi

path_block_begin='# >>> workbook development environment >>>'
path_block_end='# <<< workbook development environment <<<'

write_path_block() {
	profile=$1
	shift
	case ${profile} in
		*/*) profile_directory=${profile%/*} ;;
		*) profile_directory=. ;;
	esac
	temporary_profile=$(mktemp "${profile_directory}/.workbook-profile.XXXXXX")
	if [ -f "${profile}" ]; then
		# Drop any previous block so repeated runs replace it rather than
		# stacking duplicate PATH entries.
		awk -v begin="${path_block_begin}" -v end="${path_block_end}" '
			$0 == begin { skipping = 1 }
			skipping != 1 { print }
			$0 == end { skipping = 0 }
		' "${profile}" > "${temporary_profile}"
	fi
	{
		echo "${path_block_begin}"
		for directory in "$@"; do
			echo "case \":\${PATH}:\" in"
			echo "	*\":${directory}:\"*) ;;"
			echo "	*) PATH=\"${directory}:\${PATH}\" ;;"
			echo "esac"
		done
		echo "export PATH"
		echo "${path_block_end}"
	} >> "${temporary_profile}"
	# Copy rather than move so the profile keeps its existing mode and owner.
	cat -- "${temporary_profile}" > "${profile}"
	rm -f -- "${temporary_profile}"
	echo "Updated PATH in ${profile}"
}

if [ "${update_profile}" = yes ]; then
	if [ -n "${WORKBOOK_SETUP_PROFILE:-}" ]; then
		write_path_block "${WORKBOOK_SETUP_PROFILE}" "$@"
	else
		updated_a_profile=no
		for candidate in "${HOME}/.bashrc" "${HOME}/.zshrc" "${HOME}/.profile"; do
			if [ -f "${candidate}" ]; then
				write_path_block "${candidate}" "$@"
				updated_a_profile=yes
			fi
		done
		if [ "${updated_a_profile}" = no ]; then
			write_path_block "${HOME}/.profile" "$@"
		fi
	fi
fi

report() {
	label=$1
	binary=$2
	description=$3
	if [ ! -x "${binary}" ]; then
		fail "the ${label} build is missing at ${binary}"
	fi
	if ! reported=$("${binary}" version 2>&1); then
		fail "the ${label} build at ${binary} does not run: ${reported}"
	fi
	printf '  %-12s %s\n' "${label}" "${binary}"
	printf '  %-12s %s (%s)\n' '' "$(echo "${reported}" | head -n 1)" "${description}"
}

echo
echo "Workbook is installed:"
if [ "${install_stable}" = yes ]; then
	report workbook "${stable_binary}" "${stable_description}"
fi
if [ "${install_dev}" = yes ]; then
	report workbook-dev "${dev_binary}" "build of the current working tree"
fi

missing_from_path=
for directory in "$@"; do
	case ":${PATH}:" in
		*:"${directory}":*) ;;
		*) missing_from_path="${missing_from_path:+${missing_from_path}:}${directory}" ;;
	esac
done
if [ -n "${missing_from_path}" ]; then
	echo
	echo "Add the install directories to the current shell with:"
	echo "  export PATH=\"${missing_from_path}:\$PATH\""
fi

if [ "${install_dev}" = yes ]; then
	echo
	echo "Bootstrap this clone with 'workbook-dev setup' before using task commands."
	echo "Rebuild after changing the working tree by rerunning with --dev-only."
fi
