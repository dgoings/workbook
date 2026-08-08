package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newFakeReleaseCLI writes a gh stub whose answers come from the environment,
// so each test states the GitHub state it exercises instead of building one.
//
// FAKE_PUBLISHED_TAGS and FAKE_DRAFT_TAGS are space-delimited tag lists, and a
// tag in neither has no release at all. FAKE_CHECK_RUNS is the tab-separated
// name/status/conclusion output the real gh produces after its --jq filter.
func newFakeReleaseCLI(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	script := `#!/bin/sh
set -eu
command=${1:-}
subcommand=${2:-}

if [ "${command}" = api ]; then
	if [ "${FAKE_CHECK_RUNS_FAIL:-0}" = 1 ]; then
		echo "simulated API failure" >&2
		exit 1
	fi
	printf '%s' "${FAKE_CHECK_RUNS:-}"
	exit 0
fi

if [ "${command}" = release ] && [ "${subcommand}" = view ]; then
	tag=$3
	case " ${FAKE_PUBLISHED_TAGS:-} " in
		*" ${tag} "*)
			echo false
			exit 0
			;;
	esac
	case " ${FAKE_DRAFT_TAGS:-} " in
		*" ${tag} "*)
			echo true
			exit 0
			;;
	esac
	exit 1
fi

if [ "${command}" = release ] && [ "${subcommand}" = delete ]; then
	printf '%s\n' "$3" >> "${FAKE_GH_DELETED:-/dev/null}"
	exit 0
fi

echo "unsupported gh invocation: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return directory
}

// environmentWithFakeCLI puts the stub first on PATH so the scripts find it
// ahead of any real gh on the machine running the tests.
func environmentWithFakeCLI(fakeBin string, values ...string) []string {
	return environmentWithValues(
		os.Environ(),
		append([]string{"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")}, values...)...,
	)
}

// exitCode reports the status a script exited with. These scripts distinguish
// "the answer is no" from "the question could not be answered" by exit code, so
// a test that only checked for failure would not tell them apart.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("error %v is not an exit status", err)
	}
	return exitError.ExitCode()
}
