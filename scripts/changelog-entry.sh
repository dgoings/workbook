#!/bin/sh
set -eu

# Prints the CHANGELOG entry body for a version.
#
# The release workflow publishes this as the GitHub release notes when it
# exists. Writing notes by hand and then publishing different generated ones is
# the divergence that makes a changelog stop being worth reading.
#
# An entry heading is "## v<version>" optionally followed by anything, so a date
# may be appended without the version becoming unparseable. The body runs to the
# next "## " heading or to the end of the file.

if [ "$#" -ne 2 ]; then
	echo "usage: scripts/changelog-entry.sh <version> <changelog-path>" >&2
	exit 2
fi

version=$1
changelog=$2

if [ ! -f "${changelog}" ]; then
	echo "workbook release: no changelog at ${changelog}" >&2
	exit 1
fi

# The version reaches grep as a pattern, and its dots would otherwise match any
# character, so 0.1.0 would find an entry headed 0x1y0.
escaped_version=$(printf '%s' "${version}" | sed 's/\./\\./g')
heading_pattern="^## v${escaped_version}( .*)?$"

if ! grep -Eq "${heading_pattern}" "${changelog}"; then
	echo "workbook release: no changelog entry for v${version}" >&2
	exit 1
fi

# Command substitution strips trailing newlines, so only the blank lines between
# the heading and the first line of prose need removing.
body=$(awk -v pattern="${heading_pattern}" '
	!found && $0 ~ pattern { found = 1; next }
	found && /^## / { exit }
	found { print }
' "${changelog}" | sed -e '/./,$!d')

if [ -z "${body}" ]; then
	echo "workbook release: changelog entry for v${version} is empty" >&2
	exit 1
fi

printf '%s\n' "${body}"
