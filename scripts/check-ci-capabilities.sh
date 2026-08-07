#!/bin/sh
# Verify that this environment can run the whole Workbook test suite.
#
# Several tests are written to skip when an optional capability is absent, so a
# machine without them reports "ok" while covering far less than it claims. CI
# runs this first: a missing capability stops the run here, naming the tool,
# instead of shrinking the suite silently.
set -eu

status=0

report_missing() {
	printf 'workbook ci: missing capability: %s\n' "$1" >&2
	status=1
}

# node executes the rendered web client, which is the only way the embedded
# board behavior tests in internal/webui run at all.
if node_version=$(node --version 2>/dev/null); then
	printf 'node %s\n' "${node_version}"
else
	report_missing 'node is not on PATH, so the embedded web client behavior tests cannot run'
fi

# Git has to create SHA-256 repositories for the cross-object-format tests in
# internal/gitstore and internal/perf. Support landed in Git 2.29.
probe=$(mktemp -d)
trap 'rm -rf "${probe}"' EXIT HUP INT TERM
if git init --quiet --object-format=sha256 "${probe}/sha256" >/dev/null 2>&1; then
	printf '%s supports --object-format=sha256\n' "$(git --version)"
else
	report_missing "$(git --version 2>/dev/null || echo git) cannot create SHA-256 repositories, so the SHA-256 tests cannot run"
fi

if [ "${status}" -ne 0 ]; then
	printf 'workbook ci: provision the reported capabilities; the suite must not run without them\n' >&2
	exit 1
fi

printf 'workbook ci: every optional test capability is present\n'
