#!/bin/sh

# Version grammar and ordering for Workbook releases, sourced by every release
# script. A version is MAJOR.MINOR.PATCH with no leading zeroes, optionally
# followed by -rcN for a pre-release, where N is a positive integer with no
# leading zero. That is the only pre-release form: one spelling keeps the
# ordering below trivial and the tags readable beside the stable ones.
#
# These functions are sourced into scripts that own variables named version,
# major, minor and patch, and POSIX sh has no way to declare a variable local.
# Every variable here is therefore prefixed rv_, so calling a helper cannot
# overwrite a caller's state.

is_safe_release_version() {
	rv_version=$1
	case "${rv_version}" in
		*-rc*)
			rv_core=${rv_version%%-rc*}
			rv_rc=${rv_version#*-rc}
			;;
		*)
			rv_core=${rv_version}
			rv_rc=
			;;
	esac

	case "${rv_core}" in
		"" | *[!0-9.]* | .* | *. | *..*)
			return 1
			;;
	esac
	rv_major=${rv_core%%.*}
	rv_remainder=${rv_core#*.}
	if [ "${rv_remainder}" = "${rv_core}" ]; then
		return 1
	fi
	rv_minor=${rv_remainder%%.*}
	rv_patch=${rv_remainder#*.}
	if [ "${rv_patch}" = "${rv_remainder}" ]; then
		return 1
	fi
	case "${rv_patch}" in
		*.*) return 1 ;;
	esac
	for rv_component in "${rv_major}" "${rv_minor}" "${rv_patch}"; do
		case "${rv_component}" in
			0) ;;
			0* | "") return 1 ;;
		esac
	done

	# A suffix is present exactly when the core is shorter than the version;
	# then the rc number has to be a positive integer with no leading zero.
	if [ "${rv_core}" != "${rv_version}" ]; then
		case "${rv_rc}" in
			"" | *[!0-9]* | 0*) return 1 ;;
		esac
	fi
	return 0
}

require_safe_release_version() {
	rv_version=$1
	rv_label=$2
	if ! is_safe_release_version "${rv_version}"; then
		echo "${rv_label}: version must be MAJOR.MINOR.PATCH, optionally -rcN, without leading zeroes" >&2
		return 2
	fi
}

# Assumes the version already passed is_safe_release_version.
is_prerelease_version() {
	case "$1" in
		*-rc*) return 0 ;;
		*) return 1 ;;
	esac
}

# Prints "MAJOR MINOR PATCH RANK RC": five integers that sort into release
# order with sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n. RANK is 1 for a stable
# version and 0 for a pre-release, which is what puts 0.6.0 after every
# 0.6.0-rcN. Neither sort -V nor git's version sort gets that right.
release_version_key() {
	rv_version=$1
	rv_core=${rv_version%%-rc*}
	case "${rv_version}" in
		*-rc*)
			rv_rank=0
			rv_rc=${rv_version#*-rc}
			;;
		*)
			rv_rank=1
			rv_rc=0
			;;
	esac
	rv_major=${rv_core%%.*}
	rv_remainder=${rv_core#*.}
	rv_minor=${rv_remainder%%.*}
	rv_patch=${rv_remainder#*.}
	printf '%s %s %s %s %s\n' "${rv_major}" "${rv_minor}" "${rv_patch}" "${rv_rank}" "${rv_rc}"
}

# Succeeds when the first version orders strictly before the second.
release_version_before() {
	rv_first_key=$(release_version_key "$1")
	rv_second_key=$(release_version_key "$2")
	if [ "${rv_first_key}" = "${rv_second_key}" ]; then
		return 1
	fi
	rv_last_key=$(printf '%s\n%s\n' "${rv_first_key}" "${rv_second_key}" |
		sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n | tail -n 1)
	[ "${rv_last_key}" = "${rv_second_key}" ]
}

# A release line is MAJOR.MINOR, the first two components of every version on
# it, under the same no-leading-zeroes rule. A patch to an older line is cut
# from a maint/vMAJOR.MINOR branch; see CONTRIBUTING.md.
is_release_line() {
	rv_line=$1
	case "${rv_line}" in
		"" | *[!0-9.]* | .* | *. | *..*) return 1 ;;
	esac
	rv_major=${rv_line%%.*}
	rv_minor=${rv_line#*.}
	if [ "${rv_minor}" = "${rv_line}" ]; then
		return 1
	fi
	case "${rv_minor}" in
		*.*) return 1 ;;
	esac
	for rv_component in "${rv_major}" "${rv_minor}"; do
		case "${rv_component}" in
			0) ;;
			0* | "") return 1 ;;
		esac
	done
	return 0
}

# Prints the MAJOR.MINOR line of a version that already passed
# is_safe_release_version.
release_version_line() {
	rv_core=${1%%-rc*}
	printf '%s\n' "${rv_core%.*}"
}

# Prints the line a branch maintains: 0.5 for maint/v0.5, and nothing for any
# branch outside maint/, main among them. A maint/ name that is not a line,
# such as maint/v0.5.1, is an error rather than an ordinary branch, so a typo
# never quietly gets the trunk's rules.
release_line_of_branch() {
	case "$1" in
		maint/*)
			rv_line=${1#maint/v}
			if [ "${rv_line}" = "$1" ] || ! is_release_line "${rv_line}"; then
				echo "release-version: $1 is not a line branch; name one maint/vMAJOR.MINOR, such as maint/v0.5" >&2
				return 2
			fi
			printf '%s\n' "${rv_line}"
			;;
	esac
}

# Refuses a version that is not a stable patch to LINE, naming LABEL as the
# speaker. A line exists to ship fixes to people who cannot take the next
# minor, so a candidate, or a version on any other line, belongs on main.
# Assumes the version already passed is_safe_release_version.
require_line_release_version() {
	rv_version=$1
	rv_line=$2
	rv_label=$3
	if is_prerelease_version "${rv_version}"; then
		echo "${rv_label}: maint/v${rv_line} cuts stable patch releases only; cut ${rv_version} from main" >&2
		return 1
	fi
	if [ "$(release_version_line "${rv_version}")" != "${rv_line}" ]; then
		echo "${rv_label}: ${rv_version} is not on the ${rv_line} line; maint/v${rv_line} cuts ${rv_line}.x patch releases only" >&2
		return 1
	fi
}

# Prints the newest release tag by release order, or nothing when there is
# none. KIND is "stable", which ignores pre-release tags, or "any". A tag whose
# number is not a release version (v2026-08-08, say) is skipped rather than an
# error. DIRECTORY names the repository; it defaults to the current directory.
# PREFIX is the tag prefix, "v" for the CLI and "desktop-v" for the desktop
# app, so the two sequences never see each other's tags. LINE, when given, is
# a MAJOR.MINOR line, and only tags on it are considered.
newest_release_tag() {
	rv_kind=$1
	rv_directory=${2:-}
	rv_prefix=${3:-v}
	rv_wanted_line=${4:-}
	# A mistyped kind would otherwise fall through to "any", handing a caller
	# that asked for stable releases the pre-releases as well.
	case ${rv_kind} in
		stable | any) ;;
		*)
			echo "release-version: newest_release_tag KIND must be stable or any" >&2
			return 2
			;;
	esac
	# A malformed line would match nothing and read as "no release yet".
	if [ -n "${rv_wanted_line}" ] && ! is_release_line "${rv_wanted_line}"; then
		echo "release-version: newest_release_tag LINE must be MAJOR.MINOR" >&2
		return 2
	fi
	if [ -n "${rv_directory}" ]; then
		set -- git -C "${rv_directory}" tag --list "${rv_prefix}[0-9]*"
	else
		set -- git tag --list "${rv_prefix}[0-9]*"
	fi
	"$@" | while IFS= read -r rv_tag; do
		rv_number=${rv_tag#"${rv_prefix}"}
		is_safe_release_version "${rv_number}" || continue
		if [ "${rv_kind}" = stable ] && is_prerelease_version "${rv_number}"; then
			continue
		fi
		if [ -n "${rv_wanted_line}" ] && [ "$(release_version_line "${rv_number}")" != "${rv_wanted_line}" ]; then
			continue
		fi
		printf '%s %s\n' "$(release_version_key "${rv_number}")" "${rv_tag}"
	done | sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n | tail -n 1 | awk '{ print $6 }'
}
