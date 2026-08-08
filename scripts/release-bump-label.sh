#!/bin/sh
set -eu

# Reads pull request label names on standard input, one per line, and prints the
# release bump kind they select.
#
# Printing nothing is how an ordinary pull request is told apart from one that
# cuts a release: the pull request check runs on every pull request so that it
# can be marked required, and a pull request carrying no release label passes it
# without validating anything.
#
# Two release labels are an error rather than a precedence rule. A pull request
# labelled both minor and patch expresses two intents, and guessing which one
# was meant would cut a release the author did not ask for.

bump=
matched_label=

while IFS= read -r label || [ -n "${label}" ]; do
	case ${label} in
		release:patch) kind='patch' ;;
		release:minor) kind='minor' ;;
		release:major) kind='major' ;;
		*) continue ;;
	esac
	if [ -n "${bump}" ]; then
		echo "workbook release: pull request carries both ${matched_label} and ${label}" >&2
		exit 1
	fi
	bump=${kind}
	matched_label=${label}
done

if [ -n "${bump}" ]; then
	echo "${bump}"
fi
