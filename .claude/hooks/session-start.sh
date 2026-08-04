#!/usr/bin/env bash
set -euo pipefail

# SessionStart hook for Claude Code on the web.
#
# Prepares a remote session to build, test, and exercise Workbook:
#
#   * warms the Go module and build caches so tests and vet are fast;
#   * installs both CLI builds with scripts/setup-dev-env.sh, so a broken
#     working tree still leaves a usable "workbook" behind "workbook-dev";
#   * puts both install directories on PATH for the session;
#   * bootstraps this clone so task commands work.
#
# Local checkouts are left alone: developers run scripts/setup-dev-env.sh
# themselves and keep their own shell profile.
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
	exit 0
fi

repository_root=${CLAUDE_PROJECT_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)}
cd -- "${repository_root}"

# Pin the prefixes the setup script would otherwise default to, so the PATH
# entries written below are the directories it actually installs into. The
# published build is always built from source here: these containers have no
# Homebrew, and naming the method keeps the result the same if that changes.
export WORKBOOK_STABLE_PREFIX="${WORKBOOK_STABLE_PREFIX:-${HOME}/.local/share/workbook/stable}"
export WORKBOOK_DEV_PREFIX="${WORKBOOK_DEV_PREFIX:-${HOME}/.local/share/workbook/dev}"
stable_bin=${WORKBOOK_STABLE_PREFIX}/bin
dev_bin=${WORKBOOK_DEV_PREFIX}/bin

note() {
	echo "session-start: $1"
}

if ! command -v go >/dev/null 2>&1; then
	note "go was not found in PATH; skipping environment setup" >&2
	exit 0
fi

note "downloading Go modules"
go mod download

# The container image is cached once the hook finishes, so compiling the
# packages and their test binaries now makes the first "go test" in the
# session cheap instead of repeating this work per session.
note "warming the build cache"
go build ./...
go test -count=1 -run '^$' ./... >/dev/null

# Install the working-tree build first. It is the one a session actually
# iterates on, and installing it before the release build means a repository
# without a usable release tag still ends up with a CLI.
note "installing the working-tree build (workbook-dev)"
scripts/setup-dev-env.sh --dev-only --no-profile

# The release build is a fallback, so a failure here is reported and survived
# rather than failing the session.
note "installing the published release build (workbook)"
if ! scripts/setup-dev-env.sh --stable-only --stable-method source --no-profile; then
	note "the published build could not be installed; continuing with workbook-dev only" >&2
fi

# Export PATH for the session. Both directories are included even when one
# install was skipped, so a later manual install is already on PATH.
if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
	printf 'export PATH="%s:%s:${PATH}"\n' "${dev_bin}" "${stable_bin}" >> "${CLAUDE_ENV_FILE}"
fi
PATH="${dev_bin}:${stable_bin}:${PATH}"
export PATH

# Bootstrap the clone: project identity, managed agent documentation, and the
# shared task refs under refs/workbook. Managed documentation that is already
# current is left untouched, so this does not dirty the working tree.
if command -v workbook-dev >/dev/null 2>&1; then
	note "bootstrapping the clone with workbook-dev setup"
	if ! workbook-dev setup; then
		note "workbook-dev setup failed; task commands may need it run manually" >&2
	fi
fi

note "ready"
