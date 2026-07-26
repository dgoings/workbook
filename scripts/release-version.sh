#!/bin/sh

is_safe_release_version() {
	version=$1
	case "${version}" in
		"" | *[!0-9.]* | .* | *. | *..*)
			return 1
			;;
	esac

	major=${version%%.*}
	remainder=${version#*.}
	if [ "${remainder}" = "${version}" ]; then
		return 1
	fi
	minor=${remainder%%.*}
	patch=${remainder#*.}
	if [ "${patch}" = "${remainder}" ]; then
		return 1
	fi
	case "${patch}" in
		*.*) return 1 ;;
	esac

	for component in "${major}" "${minor}" "${patch}"; do
		case "${component}" in
			0) ;;
			0* | "") return 1 ;;
		esac
	done
	return 0
}

require_safe_release_version() {
	version=$1
	label=$2
	if ! is_safe_release_version "${version}"; then
		echo "${label}: version must be MAJOR.MINOR.PATCH without leading zeroes" >&2
		return 2
	fi
}
