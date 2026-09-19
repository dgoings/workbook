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

// A release someone wrote notes for publishes those notes. Generating notes
// beside prose written for the same version puts two descriptions of one
// release on the page, and the one nobody wrote wins the reader's attention.
func TestPublishReleasePublishesTheChangelogEntryAsTheReleaseNotes(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## v0.1.0 — 2026-08-08\n\n### Added\n- the first release\n")

	output, err := runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, tap, dist, "v0.1.0", changelog, nil)
	if err != nil {
		t.Fatalf("publish release: %v\n%s", err, output)
	}

	logContents := readFakeGitHubLog(t, fakeGitHub)
	if !strings.Contains(logContents, "--notes-file") {
		t.Errorf("gh log = %q, want the changelog entry supplied as notes", logContents)
	}
	if strings.Contains(logContents, "--generate-notes") {
		t.Errorf("gh log = %q, want no generated notes alongside a written entry", logContents)
	}
	// The notes file is a body, not a release asset. Uploading it would leave
	// the release with an asset the rerun check refuses to recognise.
	if _, err := os.Stat(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, "v0.1.0"), "notes.md")); err == nil {
		t.Error("notes file was uploaded as a release asset")
	}
}

// A release with no entry has nothing to publish, so the generated notes stay.
func TestPublishReleaseGeneratesNotesWithoutAChangelogEntry(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	// An entry for a different release, which this one must not borrow.
	changelog := writeChangelog(t, "# Changelog\n\n## v0.2.0\n\n- a later release\n")

	output, err := runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, tap, dist, "v0.1.0", changelog, nil)
	if err != nil {
		t.Fatalf("publish release: %v\n%s", err, output)
	}

	logContents := readFakeGitHubLog(t, fakeGitHub)
	if !strings.Contains(logContents, "--generate-notes") {
		t.Errorf("gh log = %q, want generated notes for a release with no entry", logContents)
	}
	if strings.Contains(logContents, "--notes-file") {
		t.Errorf("gh log = %q, want no notes file for a release with no entry", logContents)
	}
}

func TestPublishReleaseCreatesAssetsOnceAndRejectsMismatchedRerun(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	runPublishRelease(t, root, fakeBin, fakeGitHub, tap, dist, nil)
	firstRemoteHead := gitOutput(t, tap, "rev-parse", "origin/main")
	firstAsset, err := os.ReadFile(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, "v0.1.0"), "workbook_0.1.0_darwin_arm64.tar.gz"))
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
	storedAsset, err := os.ReadFile(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, "v0.1.0"), "workbook_0.1.0_darwin_arm64.tar.gz"))
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
	if _, statErr := os.Stat(fakeReleaseStatePath(fakeGitHub, "v0.1.0")); !os.IsNotExist(statErr) {
		t.Fatalf("new draft release was not deleted during rollback: %v", statErr)
	}
	if _, statErr := os.Stat(fakeReleaseAssetsPath(fakeGitHub, "v0.1.0")); !os.IsNotExist(statErr) {
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
	state, readErr := os.ReadFile(fakeReleaseStatePath(fakeGitHub, "v0.1.0"))
	if readErr != nil {
		t.Fatalf("read release state after ambiguous publication failure: %v", readErr)
	}
	if strings.TrimSpace(string(state)) != "published" {
		t.Fatalf("release state = %q, want published release preserved", state)
	}
	if _, statErr := os.Stat(fakeReleaseAssetsPath(fakeGitHub, "v0.1.0")); statErr != nil {
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

// A pre-release is flagged as one on the release page, takes its notes from the
// Unreleased section, and leaves the tap alone: brew upgrade must never serve an
// rc to someone who installed a release.
func TestPublishReleasePublishesAPreReleaseWithoutTouchingTheTap(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## Unreleased\n\n- a candidate\n\n## v0.5.1\n\n- the last release\n")
	tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, tap, dist, "v0.6.0-rc1", changelog, nil)
	if err != nil {
		t.Fatalf("publish pre-release: %v\n%s", err, output)
	}

	logContents := readFakeGitHubLog(t, fakeGitHub)
	if !strings.Contains(logContents, "--prerelease") {
		t.Errorf("gh log = %q, want the release created as a pre-release", logContents)
	}
	if !strings.Contains(logContents, "--notes-file") {
		t.Errorf("gh log = %q, want the Unreleased section supplied as notes", logContents)
	}
	if strings.Contains(logContents, "--generate-notes") {
		t.Errorf("gh log = %q, want no generated notes alongside the Unreleased section", logContents)
	}
	// Publishing the draft must not drop the flag: a candidate listed as a
	// release is one brew and every reader takes for a finished version.
	editLine := ""
	for _, line := range strings.Split(logContents, "\n") {
		if strings.HasPrefix(line, "release edit ") {
			editLine = line
		}
	}
	if !strings.Contains(editLine, "--prerelease") {
		t.Errorf("gh edit line = %q, want the pre-release flag restated when the draft is published", editLine)
	}
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != tapHeadBefore {
		t.Errorf("tap head moved from %s to %s for a pre-release", tapHeadBefore, got)
	}
	if got := gitOutput(t, tap, "rev-parse", "HEAD"); got != tapHeadBefore {
		t.Errorf("tap checkout moved from %s to %s for a pre-release", tapHeadBefore, got)
	}
}

// With nothing under Unreleased there is nothing to publish as notes, so the
// generated ones stay, as for a stable release with no entry.
func TestPublishReleaseGeneratesPreReleaseNotesWithoutAnUnreleasedSection(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## v0.5.1\n\n- the last release\n")

	output, err := runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, tap, dist, "v0.6.0-rc1", changelog, nil)
	if err != nil {
		t.Fatalf("publish pre-release: %v\n%s", err, output)
	}

	logContents := readFakeGitHubLog(t, fakeGitHub)
	if !strings.Contains(logContents, "--generate-notes") {
		t.Errorf("gh log = %q, want generated notes", logContents)
	}
	if !strings.Contains(logContents, "--prerelease") {
		t.Errorf("gh log = %q, want the release flagged as a pre-release", logContents)
	}
}

// The workflow checks the tap out only for a stable release, so a pre-release
// run is handed a tap path that does not exist. It has to publish anyway.
func TestPublishReleasePublishesAPreReleaseWithoutATapCheckout(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## Unreleased\n\n- a candidate\n\n## v0.5.1\n\n- the last release\n")
	absentTap := filepath.Join(t.TempDir(), "homebrew-tap")

	output, err := runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, absentTap, dist, "v0.6.0-rc1", changelog, nil)
	if err != nil {
		t.Fatalf("publish pre-release without a tap checkout: %v\n%s", err, output)
	}

	if logContents := readFakeGitHubLog(t, fakeGitHub); !strings.Contains(logContents, "--prerelease") {
		t.Errorf("gh log = %q, want the release created as a pre-release", logContents)
	}
	if _, statErr := os.Stat(absentTap); !os.IsNotExist(statErr) {
		t.Errorf("pre-release created the tap directory it was told to leave alone: %v", statErr)
	}
}

// A stable release does need the tap, so a missing checkout is a broken run and
// has to stop before anything is published rather than after.
func TestPublishReleaseFailsFastWhenAStableReleaseHasNoTapCheckout(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	absentTap := filepath.Join(t.TempDir(), "homebrew-tap")

	output, err := runPublishReleaseCommand(root, fakeBin, fakeGitHub, absentTap, dist, nil)
	if err == nil {
		t.Fatalf("stable release published without a tap checkout; output = %q", output)
	}
	if _, statErr := os.Stat(filepath.Join(fakeGitHub, "commands.log")); !os.IsNotExist(statErr) {
		t.Errorf("stable release reached gh before failing on the missing tap: %v", statErr)
	}
}

// Production mutation: flagging every release as a pre-release would hide each
// stable one from brew and from anyone reading the releases page.
func TestPublishReleaseDoesNotFlagAStableReleaseAsAPreRelease(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	runPublishRelease(t, root, fakeBin, fakeGitHub, tap, dist, nil)

	if logContents := readFakeGitHubLog(t, fakeGitHub); strings.Contains(logContents, "--prerelease") {
		t.Errorf("gh log = %q, want no pre-release flag on a stable release", logContents)
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
# Every gh release subcommand names its release as the third argument, and the
# desktop publisher touches two of them in one run. Keying state by that name
# keeps the versioned release and the rolling one apart; a caller that only
# ever uses one name sees the fake it always did, one directory down.
name=${3:-}
if [ -z "${name}" ]; then
	echo "fake gh: release ${command} needs a release name" >&2
	exit 2
fi
release_root="${FAKE_GH_ROOT}/${name}"
shift 3
case "${command}" in
	view)
		if [ ! -f "${release_root}/state" ]; then
			exit 1
		fi
		if [ "$(cat "${release_root}/state")" = draft ]; then
			echo true
		else
			echo false
		fi
		;;
	download)
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
		cp "${release_root}"/assets/* "${destination}/"
		;;
	create | upload)
		mkdir -p "${release_root}/assets"
		created_state=published
		while [ "$#" -gt 0 ]; do
			# gh reads the notes body out of this file rather than uploading it.
			# Copying it would add an asset the rerun check would reject.
			if [ "$1" = --notes-file ] && [ "$#" -ge 2 ]; then
				shift 2
				continue
			fi
			if [ "$1" = --draft ]; then
				created_state=draft
			fi
			if [ -f "$1" ]; then
				cp "$1" "${release_root}/assets/"
			fi
			shift
		done
		if [ "${command}" = create ]; then
			echo "${created_state}" > "${release_root}/state"
		fi
		;;
	edit)
		if [ "${FAKE_GH_PUBLISH_THEN_FAIL:-0}" = 1 ]; then
			echo published > "${release_root}/state"
			echo "simulated ambiguous publish failure" >&2
			exit 1
		fi
		if [ "${FAKE_GH_FAIL_PUBLISH:-0}" = 1 ]; then
			echo "simulated publish failure" >&2
			exit 1
		fi
		echo published > "${release_root}/state"
		;;
	delete)
		rm -rf "${release_root}"
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

// The fake gh keys each release's state by name, so a test names the release it
// is inspecting rather than reaching for one well-known directory.
func fakeReleaseStatePath(fakeGitHub, name string) string {
	return filepath.Join(fakeGitHub, name, "state")
}

func fakeReleaseAssetsPath(fakeGitHub, name string) string {
	return filepath.Join(fakeGitHub, name, "assets")
}

func readFakeGitHubLog(t *testing.T, fakeGitHub string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(fakeGitHub, "commands.log"))
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	return string(contents)
}

func runPublishRelease(t *testing.T, root, fakeBin, fakeGitHub, tap, dist string, extraEnvironment []string) {
	t.Helper()
	if output, err := runPublishReleaseCommand(root, fakeBin, fakeGitHub, tap, dist, extraEnvironment); err != nil {
		t.Fatalf("publish release: %v\n%s", err, output)
	}
}

func runPublishReleaseCommand(root, fakeBin, fakeGitHub, tap, dist string, extraEnvironment []string) ([]byte, error) {
	// Pointing at a changelog that does not exist keeps these cases on the
	// generated-notes path regardless of what the repository's own CHANGELOG.md
	// happens to contain. Notes selection is covered on its own below.
	return runPublishReleaseWithChangelog(
		root, fakeBin, fakeGitHub, tap, dist, "v0.1.0",
		filepath.Join(root, "scripts", "testdata-absent-changelog.md"),
		extraEnvironment,
	)
}

// The tag carries the version, so it has to match the fixture the caller built.
func runPublishReleaseWithChangelog(root, fakeBin, fakeGitHub, tap, dist, tag, changelog string, extraEnvironment []string) ([]byte, error) {
	command := exec.Command(
		filepath.Join(root, "scripts", "publish-release.sh"),
		tag,
		dist,
		tap,
		"dgoings/workbook",
		changelog,
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
