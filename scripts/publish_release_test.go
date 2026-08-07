package scripts_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishReleaseCreatesAssetsOnceAndRejectsMismatchedRerun(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	runPublishRelease(t, root, fakeBin, fakeGitHub, tap, dist, nil)
	firstRemoteHead := gitOutput(t, tap, "rev-parse", "origin/main")
	firstAsset, err := os.ReadFile(filepath.Join(fakeGitHub, "assets", "workbook_0.1.0_darwin_arm64.tar.gz"))
	if err != nil {
		t.Fatalf("read published fixture asset: %v", err)
	}

	runPublishRelease(t, root, fakeBin, fakeGitHub, tap, dist, nil)
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != firstRemoteHead {
		t.Fatalf("identical rerun changed tap head from %s to %s", firstRemoteHead, got)
	}

	if err := os.WriteFile(
		filepath.Join(dist, "workbook_0.1.0_darwin_arm64.tar.gz"),
		[]byte("different arm64 bytes\n"),
		0o600,
	); err != nil {
		t.Fatalf("tamper local asset: %v", err)
	}
	writeFixtureChecksums(t, dist, "0.1.0")
	output, err := runPublishReleaseCommand(root, fakeBin, fakeGitHub, tap, dist, nil)
	if err == nil {
		t.Fatalf("mismatched rerun succeeded; output = %q", output)
	}
	if !strings.Contains(string(output), "does not match existing release asset") {
		t.Fatalf("mismatched rerun output = %q, want asset-integrity error", output)
	}
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != firstRemoteHead {
		t.Fatalf("mismatched rerun changed tap head from %s to %s", firstRemoteHead, got)
	}
	storedAsset, err := os.ReadFile(filepath.Join(fakeGitHub, "assets", "workbook_0.1.0_darwin_arm64.tar.gz"))
	if err != nil {
		t.Fatalf("read stored release asset: %v", err)
	}
	if string(storedAsset) != string(firstAsset) {
		t.Fatal("mismatched rerun replaced the create-once release asset")
	}

	logContents, err := os.ReadFile(filepath.Join(fakeGitHub, "commands.log"))
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	if count := strings.Count(string(logContents), "release create "); count != 1 {
		t.Fatalf("release create count = %d, want exactly one; log:\n%s", count, logContents)
	}
	if strings.Contains(string(logContents), "release upload") {
		t.Fatalf("publisher attempted release upload on a rerun:\n%s", logContents)
	}
	// The formula carries no version stanza, so its version lives in the
	// release-tag path of each download URL.
	if formula := gitOutput(t, remote, "show", "main:Formula/workbook.rb"); !strings.Contains(formula, "/releases/download/v0.1.0/workbook_0.1.0_") {
		t.Fatalf("tap formula was not published from verified assets:\n%s", formula)
	}
}

func TestPublishReleaseRollsBackTapAndNewDraftWhenPublicationFails(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	initialRemoteHead := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseCommand(
		root,
		fakeBin,
		fakeGitHub,
		tap,
		dist,
		[]string{"FAKE_GH_FAIL_PUBLISH=1"},
	)
	if err == nil {
		t.Fatalf("publisher succeeded despite release publication failure; output = %q", output)
	}
	if _, statErr := os.Stat(filepath.Join(fakeGitHub, "state")); !os.IsNotExist(statErr) {
		t.Fatalf("new draft release was not deleted during rollback: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(fakeGitHub, "assets")); !os.IsNotExist(statErr) {
		t.Fatalf("new draft assets were not deleted during rollback: %v", statErr)
	}
	if _, showErr := exec.Command("git", "-C", remote, "show", "main:Formula/workbook.rb").CombinedOutput(); showErr == nil {
		t.Fatal("tap formula remains on remote after release publication rollback")
	}
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got == initialRemoteHead {
		t.Fatal("rollback did not record an append-only compensating tap commit")
	}
	logContents, readErr := os.ReadFile(filepath.Join(fakeGitHub, "commands.log"))
	if readErr != nil {
		t.Fatalf("read fake gh log: %v", readErr)
	}
	if !strings.Contains(string(logContents), "release delete v0.1.0 --repo dgoings/workbook --yes") {
		t.Fatalf("publisher did not delete its newly-created draft release:\n%s", logContents)
	}
}

func TestPublishReleaseNeverDeletesPublicReleaseAfterAmbiguousPublishFailure(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	output, err := runPublishReleaseCommand(
		root,
		fakeBin,
		fakeGitHub,
		tap,
		dist,
		[]string{"FAKE_GH_PUBLISH_THEN_FAIL=1"},
	)
	if err == nil {
		t.Fatalf("publisher reported success after ambiguous publication failure; output = %q", output)
	}
	state, readErr := os.ReadFile(filepath.Join(fakeGitHub, "state"))
	if readErr != nil {
		t.Fatalf("read release state after ambiguous publication failure: %v", readErr)
	}
	if strings.TrimSpace(string(state)) != "published" {
		t.Fatalf("release state = %q, want published release preserved", state)
	}
	if _, statErr := os.Stat(filepath.Join(fakeGitHub, "assets")); statErr != nil {
		t.Fatalf("public release assets were deleted: %v", statErr)
	}
	if _, showErr := exec.Command("git", "-C", remote, "show", "main:Formula/workbook.rb").CombinedOutput(); showErr == nil {
		t.Fatal("tap formula remains on remote after ambiguous publication rollback")
	}

	logContents, readErr := os.ReadFile(filepath.Join(fakeGitHub, "commands.log"))
	if readErr != nil {
		t.Fatalf("read fake gh log: %v", readErr)
	}
	log := string(logContents)
	if strings.Contains(log, "release delete ") {
		t.Fatalf("publisher deleted a public release after ambiguous publication:\n%s", log)
	}
	if count := strings.Count(log, "release view v0.1.0"); count < 2 {
		t.Fatalf("release state lookup count = %d, want fresh rollback confirmation; log:\n%s", count, log)
	}
}

func writeReleaseFixture(t *testing.T, version string) string {
	t.Helper()
	dist := t.TempDir()
	for _, name := range releaseArchiveNames(version) {
		contents := []byte(name + " fixture\n")
		if err := os.WriteFile(filepath.Join(dist, name), contents, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFixtureChecksums(t, dist, version)
	return dist
}

// releaseArchiveNames lists the archives scripts/release.sh publishes, in the
// order they appear in checksums.txt.
func releaseArchiveNames(version string) []string {
	names := make([]string, 0, 4)
	for _, platform := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		names = append(names, "workbook_"+version+"_"+platform+".tar.gz")
	}
	return names
}

func writeFixtureChecksums(t *testing.T, dist, version string) {
	t.Helper()
	var checksums strings.Builder
	for _, name := range releaseArchiveNames(version) {
		contents, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(contents), name)
	}
	if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(checksums.String()), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
}

func newTapRepository(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "homebrew-tap.git")
	runCommand(t, "", nil, "git", "init", "--bare", "--initial-branch=main", remote)
	// Background auto-gc spawned by receive-pack can outlive the test and race
	// t.TempDir cleanup with "directory not empty" on slow runners.
	runCommand(t, remote, nil, "git", "config", "receive.autogc", "false")
	runCommand(t, remote, nil, "git", "config", "gc.auto", "0")
	runCommand(t, remote, nil, "git", "config", "maintenance.auto", "false")
	tap := filepath.Join(t.TempDir(), "homebrew-tap")
	runCommand(t, "", nil, "git", "clone", remote, tap)
	runCommand(t, tap, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, tap, nil, "git", "config", "user.email", "release-test@example.com")
	if err := os.WriteFile(filepath.Join(tap, "README.md"), []byte("# Tap fixture\n"), 0o600); err != nil {
		t.Fatalf("write tap README: %v", err)
	}
	runCommand(t, tap, nil, "git", "add", "README.md")
	runCommand(t, tap, nil, "git", "commit", "-m", "init tap")
	runCommand(t, tap, nil, "git", "push", "-u", "origin", "main")
	return tap, remote
}

func newFakeGitHubCLI(t *testing.T) (string, string) {
	t.Helper()
	fakeBin := t.TempDir()
	stateRoot := t.TempDir()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "${FAKE_GH_ROOT}/commands.log"
if [ "$1" != release ]; then
	echo "unsupported gh command: $*" >&2
	exit 2
fi
command=$2
shift 2
case "${command}" in
	view)
		if [ ! -f "${FAKE_GH_ROOT}/state" ]; then
			exit 1
		fi
		if [ "$(cat "${FAKE_GH_ROOT}/state")" = draft ]; then
			echo true
		else
			echo false
		fi
		;;
	download)
		shift
		destination=
		while [ "$#" -gt 0 ]; do
			case "$1" in
				--dir)
					destination=$2
					shift 2
					;;
				*)
					shift
					;;
			esac
		done
		mkdir -p "${destination}"
		cp "${FAKE_GH_ROOT}"/assets/* "${destination}/"
		;;
	create)
		shift
		mkdir -p "${FAKE_GH_ROOT}/assets"
		for argument in "$@"; do
			if [ -f "${argument}" ]; then
				cp "${argument}" "${FAKE_GH_ROOT}/assets/"
			fi
		done
		echo draft > "${FAKE_GH_ROOT}/state"
		;;
	edit)
		if [ "${FAKE_GH_PUBLISH_THEN_FAIL:-0}" = 1 ]; then
			echo published > "${FAKE_GH_ROOT}/state"
			echo "simulated ambiguous publish failure" >&2
			exit 1
		fi
		if [ "${FAKE_GH_FAIL_PUBLISH:-0}" = 1 ]; then
			echo "simulated publish failure" >&2
			exit 1
		fi
		echo published > "${FAKE_GH_ROOT}/state"
		;;
	delete)
		rm -rf "${FAKE_GH_ROOT}/assets"
		rm -f "${FAKE_GH_ROOT}/state"
		;;
	*)
		echo "unsupported gh release command: ${command}" >&2
		exit 2
		;;
esac
`
	path := filepath.Join(fakeBin, "gh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return fakeBin, stateRoot
}

func runPublishRelease(t *testing.T, root, fakeBin, fakeGitHub, tap, dist string, extraEnvironment []string) {
	t.Helper()
	if output, err := runPublishReleaseCommand(root, fakeBin, fakeGitHub, tap, dist, extraEnvironment); err != nil {
		t.Fatalf("publish release: %v\n%s", err, output)
	}
}

func runPublishReleaseCommand(root, fakeBin, fakeGitHub, tap, dist string, extraEnvironment []string) ([]byte, error) {
	command := exec.Command(
		filepath.Join(root, "scripts", "publish-release.sh"),
		"v0.1.0",
		dist,
		tap,
		"dgoings/workbook",
	)
	command.Dir = root
	command.Env = environmentWithValues(
		os.Environ(),
		append([]string{
			"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"FAKE_GH_ROOT=" + fakeGitHub,
		}, extraEnvironment...)...,
	)
	return command.CombinedOutput()
}

func environmentWithValues(base []string, values ...string) []string {
	replacements := make(map[string]string, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		replacements[key] = value
	}
	environment := make([]string, 0, len(base)+len(values))
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if _, replaced := replacements[key]; !replaced {
			environment = append(environment, value)
		}
	}
	return append(environment, values...)
}

func runCommand(t *testing.T, directory string, environment []string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
