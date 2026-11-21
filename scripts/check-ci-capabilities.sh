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

# Chrome or Chromium measures the board's layout in a real browser, for the
# internal/webui tests a fake DOM cannot answer. They look for it in this
# order: WORKBOOK_TEST_CHROME, then these names on PATH, then the macOS
# application bundles.
find_chrome() {
	if [ -n "${WORKBOOK_TEST_CHROME:-}" ]; then
		if [ -x "${WORKBOOK_TEST_CHROME}" ]; then
			printf '%s\n' "${WORKBOOK_TEST_CHROME}"
			return 0
		fi
		return 1
	fi
	for candidate in google-chrome google-chrome-stable chromium chromium-browser chrome; do
		if found=$(command -v "${candidate}" 2>/dev/null); then
			printf '%s\n' "${found}"
			return 0
		fi
	done
	if [ "$(uname -s)" = Darwin ]; then
		for candidate in \
			'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
			'/Applications/Chromium.app/Contents/MacOS/Chromium'; do
			if [ -x "${candidate}" ]; then
				printf '%s\n' "${candidate}"
				return 0
			fi
		done
	fi
	return 1
}
if chrome_path=$(find_chrome); then
	printf 'chrome %s\n' "${chrome_path}"
elif [ -n "${WORKBOOK_TEST_CHROME:-}" ]; then
	report_missing "WORKBOOK_TEST_CHROME names ${WORKBOOK_TEST_CHROME}, which is not an executable, so the browser layout tests cannot run"
else
	report_missing 'Chrome or Chromium is not on PATH, so the browser layout tests cannot run (set WORKBOOK_TEST_CHROME to its path)'
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
