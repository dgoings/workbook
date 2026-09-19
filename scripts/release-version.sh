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

# Prints the newest v* tag by release order, or nothing when there is none.
# KIND is "stable", which ignores pre-release tags, or "any". A tag whose number
# is not a release version (v2026-08-08, say) is skipped rather than an error.
# DIRECTORY names the repository; it defaults to the current directory.
newest_release_tag() {
	rv_kind=$1
	rv_directory=${2:-}
	if [ -n "${rv_directory}" ]; then
		set -- git -C "${rv_directory}" tag --list 'v[0-9]*'
	else
		set -- git tag --list 'v[0-9]*'
	fi
	"$@" | while IFS= read -r rv_tag; do
		rv_number=${rv_tag#v}
		is_safe_release_version "${rv_number}" || continue
		if [ "${rv_kind}" = stable ] && is_prerelease_version "${rv_number}"; then
			continue
		fi
		printf '%s %s\n' "$(release_version_key "${rv_number}")" "${rv_tag}"
	done | sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5n | tail -n 1 | awk '{ print $6 }'
}
