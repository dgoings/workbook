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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## v0.1.0 — 2026-08-08\n\n### Added\n- the first release\n")

	output, err := runPublishReleaseWithChangelog(t, root, fakeBin, fakeGitHub, tap, dist, "v0.1.0", changelog, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	// An entry for a different release, which this one must not borrow.
	changelog := writeChangelog(t, "# Changelog\n\n## v0.2.0\n\n- a later release\n")

	output, err := runPublishReleaseWithChangelog(t, root, fakeBin, fakeGitHub, tap, dist, "v0.1.0", changelog, nil)
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
	t.Parallel()
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
	output, err := runPublishReleaseCommand(t, root, fakeBin, fakeGitHub, tap, dist, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	initialRemoteHead := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseCommand(
		t,
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	output, err := runPublishReleaseCommand(
		t,
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## Unreleased\n\n- a candidate\n\n## v0.5.1\n\n- the last release\n")
	tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseWithChangelog(t, root, fakeBin, fakeGitHub, tap, dist, "v0.6.0-rc1", changelog, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## v0.5.1\n\n- the last release\n")

	output, err := runPublishReleaseWithChangelog(t, root, fakeBin, fakeGitHub, tap, dist, "v0.6.0-rc1", changelog, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0-rc1")
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	changelog := writeChangelog(t, "# Changelog\n\n## Unreleased\n\n- a candidate\n\n## v0.5.1\n\n- the last release\n")
	absentTap := filepath.Join(t.TempDir(), "homebrew-tap")

	output, err := runPublishReleaseWithChangelog(t, root, fakeBin, fakeGitHub, absentTap, dist, "v0.6.0-rc1", changelog, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	absentTap := filepath.Join(t.TempDir(), "homebrew-tap")

	output, err := runPublishReleaseCommand(t, root, fakeBin, fakeGitHub, absentTap, dist, nil)
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
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.1.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	runPublishRelease(t, root, fakeBin, fakeGitHub, tap, dist, nil)

	if logContents := readFakeGitHubLog(t, fakeGitHub); strings.Contains(logContents, "--prerelease") {
		t.Errorf("gh log = %q, want no pre-release flag on a stable release", logContents)
	}
}

// The newest release publishes exactly as it always has: the same gh calls,
// in the same order, with the same flags, and a tap commit. GitHub marks a
// newly published release Latest unless told otherwise, so nothing here says
// --latest, and the rule for older releases must not leak into this path.
func TestPublishReleaseMakesTheSameCallsForTheNewestRelease(t *testing.T) {
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	source := newTaggedRepository(t, "v0.5.1", "v0.6.0-rc1", "v0.6.0", "desktop-v0.9.0")
	tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, "v0.6.0", absentChangelog(root), nil)
	if err != nil {
		t.Fatalf("publish newest release: %v\n%s", err, output)
	}

	assets := releaseAssetArguments(t, dist, "0.6.0")
	want := strings.Join([]string{
		"release view v0.6.0 --repo dgoings/workbook --json isDraft --jq .isDraft",
		"release create v0.6.0 " + assets + " --generate-notes --repo dgoings/workbook --verify-tag --draft --title Workbook v0.6.0",
		"release edit v0.6.0 --repo dgoings/workbook --draft=false",
	}, "\n") + "\n"
	if got := readFakeGitHubLog(t, fakeGitHub); got != want {
		t.Errorf("gh calls =\n%s\nwant\n%s", got, want)
	}
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got == tapHeadBefore {
		t.Error("the newest release made no tap commit")
	}
	if formula := gitOutput(t, remote, "show", "main:Formula/workbook.rb"); !strings.Contains(formula, "/releases/download/v0.6.0/workbook_0.6.0_") {
		t.Errorf("tap formula does not serve v0.6.0:\n%s", formula)
	}
}

// A pre-release keeps its own rules whatever the tags around it say: it is
// flagged as one, GitHub never makes one Latest, and it never touches the tap.
// One cut below a newer stable release makes exactly the calls one cut above it
// does, so the newest-release rule never reaches it.
func TestPublishReleaseMakesTheSameCallsForAPreReleaseWhateverTheTags(t *testing.T) {
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	for _, testCase := range []struct {
		name string
		tags []string
	}{
		{name: "above every release", tags: []string{"v0.5.1", "v0.6.0-rc1"}},
		{name: "below a newer release", tags: []string{"v0.6.1", "v0.6.0-rc1"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dist := writeReleaseFixture(t, "0.6.0-rc1")
			tap, _ := newTapRepository(t)
			fakeBin, fakeGitHub := newFakeGitHubCLI(t)
			source := newTaggedRepository(t, testCase.tags...)
			tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

			output, err := runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, "v0.6.0-rc1", absentChangelog(root), nil)
			if err != nil {
				t.Fatalf("publish pre-release: %v\n%s", err, output)
			}

			assets := releaseAssetArguments(t, dist, "0.6.0-rc1")
			want := strings.Join([]string{
				"release view v0.6.0-rc1 --repo dgoings/workbook --json isDraft --jq .isDraft",
				"release create v0.6.0-rc1 " + assets + " --generate-notes --prerelease --repo dgoings/workbook --verify-tag --draft --title Workbook v0.6.0-rc1",
				"release edit v0.6.0-rc1 --repo dgoings/workbook --draft=false --prerelease",
			}, "\n") + "\n"
			if got := readFakeGitHubLog(t, fakeGitHub); got != want {
				t.Errorf("gh calls =\n%s\nwant\n%s", got, want)
			}
			if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != tapHeadBefore {
				t.Errorf("tap head moved from %s to %s for a pre-release", tapHeadBefore, got)
			}
		})
	}
}

// A patch to a line main has moved past is a quiet release: it is on the
// Releases page, but Latest and brew stay on the newest release. GitHub makes a
// newly published release Latest by default, so the older one has to say
// otherwise when its draft is published.
func TestPublishReleasePublishesAnOlderReleaseQuietly(t *testing.T) {
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.5.2")
	tap, remote := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	source := newTaggedRepository(t, "v0.5.1", "v0.6.0", "v0.5.2")
	tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, "v0.5.2", absentChangelog(root), nil)
	if err != nil {
		t.Fatalf("publish older release: %v\n%s", err, output)
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	if got, want := fakeGitHubLine(t, log, "release edit v0.5.2"), "release edit v0.5.2 --repo dgoings/workbook --draft=false --latest=false"; got != want {
		t.Errorf("edit line = %q, want %q", got, want)
	}
	// The versioned release itself is published as usual.
	if state, readErr := os.ReadFile(fakeReleaseStatePath(fakeGitHub, "v0.5.2")); readErr != nil || strings.TrimSpace(string(state)) != "published" {
		t.Errorf("release state = %q (%v), want published", state, readErr)
	}
	// Production mutation: refreshing the tap regardless would hand brew
	// upgrade a downgrade.
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != tapHeadBefore {
		t.Errorf("tap head moved from %s to %s for an older release", tapHeadBefore, got)
	}
	if got := gitOutput(t, tap, "rev-parse", "HEAD"); got != tapHeadBefore {
		t.Errorf("tap checkout moved from %s to %s for an older release", tapHeadBefore, got)
	}
	if _, showErr := exec.Command("git", "-C", remote, "show", "main:Formula/workbook.rb").CombinedOutput(); showErr == nil {
		t.Error("an older release wrote a formula to the tap")
	}
	if !strings.Contains(string(output), "v0.5.2 is older than v0.6.0") {
		t.Errorf("output = %q, want it to say why Latest and the tap were left alone", output)
	}
}

// The tap is checked too, because it can be ahead of the tags this run sees: a
// formula already serving a higher version stays as it is, while one serving
// the same or a lower version is refreshed as it always was.
func TestPublishReleaseNeverMovesTheTapFormulaBackward(t *testing.T) {
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	for _, testCase := range []struct {
		formulaVersion string
		wantRefresh    bool
	}{
		{formulaVersion: "0.7.0", wantRefresh: false},
		{formulaVersion: "0.6.10", wantRefresh: false},
		{formulaVersion: "0.6.0", wantRefresh: true},
		{formulaVersion: "0.6.1", wantRefresh: true},
	} {
		t.Run(testCase.formulaVersion, func(t *testing.T) {
			t.Parallel()
			dist := writeReleaseFixture(t, "0.6.1")
			tap, remote := newTapRepository(t)
			writeTapFormula(t, tap, testCase.formulaVersion)
			fakeBin, fakeGitHub := newFakeGitHubCLI(t)
			source := newTaggedRepository(t, "v0.6.0", "v0.6.1")
			tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

			output, err := runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, "v0.6.1", absentChangelog(root), nil)
			if err != nil {
				t.Fatalf("publish release: %v\n%s", err, output)
			}

			formula := gitOutput(t, remote, "show", "main:Formula/workbook.rb")
			// The fixture formula carries no checksums, so a rendered one is told
			// apart from it even when both name the same version.
			refreshed := strings.Contains(formula, "/releases/download/v0.6.1/workbook_0.6.1_") && strings.Contains(formula, "sha256 ")
			if refreshed != testCase.wantRefresh {
				t.Errorf("tap formula refreshed = %t, want %t; formula:\n%s", refreshed, testCase.wantRefresh, formula)
			}
			if !testCase.wantRefresh {
				if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != tapHeadBefore {
					t.Errorf("tap head moved from %s to %s past a newer formula", tapHeadBefore, got)
				}
				if !strings.Contains(string(output), "already serves "+testCase.formulaVersion) {
					t.Errorf("output = %q, want it to say the tap is ahead", output)
				}
			}
			// The tags say this is the newest release, so it is still Latest:
			// the publish call is the one it always was.
			if got, want := fakeGitHubLine(t, readFakeGitHubLog(t, fakeGitHub), "release edit v0.6.1"), "release edit v0.6.1 --repo dgoings/workbook --draft=false"; got != want {
				t.Errorf("edit line = %q, want %q", got, want)
			}
		})
	}
}

// A run that cannot tell whether its release is the newest must not guess, and
// must find that out before anything reaches GitHub or the tap.
func TestPublishReleaseStopsBeforePublishingWhenTheTagIsNotInTheCheckout(t *testing.T) {
	t.Parallel()
	root, _ := renderFormulaPaths(t)
	dist := writeReleaseFixture(t, "0.6.0")
	tap, _ := newTapRepository(t)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	source := newTaggedRepository(t, "v0.5.1")
	tapHeadBefore := gitOutput(t, tap, "rev-parse", "origin/main")

	output, err := runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, "v0.6.0", absentChangelog(root), nil)
	if err == nil {
		t.Fatalf("published a release whose tag the checkout does not carry:\n%s", output)
	}
	if !strings.Contains(string(output), "is not a tag in this repository") {
		t.Errorf("output = %q, want the missing tag named", output)
	}
	if _, statErr := os.Stat(filepath.Join(fakeGitHub, "commands.log")); !os.IsNotExist(statErr) {
		t.Errorf("publisher reached gh before failing: %v", statErr)
	}
	if got := gitOutput(t, tap, "rev-parse", "origin/main"); got != tapHeadBefore {
		t.Errorf("tap head moved from %s to %s", tapHeadBefore, got)
	}
}

// releaseAssetArguments is the asset list publish-release.sh hands gh release
// create, in its order, under the resolved path it uses.
func releaseAssetArguments(t *testing.T, dist, version string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dist)
	if err != nil {
		t.Fatalf("resolve %s: %v", dist, err)
	}
	arguments := make([]string, 0, 5)
	for _, name := range append(releaseArchiveNames(version), "checksums.txt") {
		arguments = append(arguments, filepath.Join(resolved, name))
	}
	return strings.Join(arguments, " ")
}

// writeTapFormula commits a formula serving version to the tap and its remote.
// Only the download URLs matter: they are where the version lives.
func writeTapFormula(t *testing.T, tap, version string) {
	t.Helper()
	formula := "class Workbook < Formula\n" +
		"  url \"https://github.com/dgoings/workbook/releases/download/v" + version + "/workbook_" + version + "_darwin_arm64.tar.gz\"\n" +
		"  url \"https://github.com/dgoings/workbook/releases/download/v" + version + "/workbook_" + version + "_linux_amd64.tar.gz\"\n" +
		"end\n"
	if err := os.MkdirAll(filepath.Join(tap, "Formula"), 0o755); err != nil {
		t.Fatalf("create Formula directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tap, "Formula", "workbook.rb"), []byte(formula), 0o600); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	runCommand(t, tap, nil, "git", "add", "Formula/workbook.rb")
	runCommand(t, tap, nil, "git", "commit", "--quiet", "-m", "workbook "+version)
	runCommand(t, tap, nil, "git", "push", "--quiet", "origin", "main")
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
# The desktop publisher touches two releases in one run, so a test that wants
# one operation on one of them to fail names both: FAKE_GH_FAIL_RELEASE is the
# release and FAKE_GH_FAIL_OP the subcommand. The blunt switches below stay as
# they are, because they say "fail whatever is published next", which is what
# their callers mean.
if [ "${FAKE_GH_FAIL_RELEASE:-}" = "${name}" ] && [ "${FAKE_GH_FAIL_OP:-}" = "${command}" ]; then
	echo "simulated ${command} failure for ${name}" >&2
	exit 1
fi
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
	if output, err := runPublishReleaseCommand(t, root, fakeBin, fakeGitHub, tap, dist, extraEnvironment); err != nil {
		t.Fatalf("publish release: %v\n%s", err, output)
	}
}

func runPublishReleaseCommand(t *testing.T, root, fakeBin, fakeGitHub, tap, dist string, extraEnvironment []string) ([]byte, error) {
	t.Helper()
	// Pointing at a changelog that does not exist keeps these cases on the
	// generated-notes path regardless of what the repository's own CHANGELOG.md
	// happens to contain. Notes selection is covered on its own below.
	return runPublishReleaseWithChangelog(
		t, root, fakeBin, fakeGitHub, tap, dist, "v0.1.0",
		absentChangelog(root),
		extraEnvironment,
	)
}

// The tag carries the version, so it has to match the fixture the caller built.
// The release is published from a repository holding that tag alone, so it is
// the newest release there, whatever tags this checkout happens to carry.
func runPublishReleaseWithChangelog(t *testing.T, root, fakeBin, fakeGitHub, tap, dist, tag, changelog string, extraEnvironment []string) ([]byte, error) {
	t.Helper()
	source := newTaggedRepository(t, tag)
	return runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, tag, changelog, extraEnvironment)
}

func absentChangelog(root string) string {
	return filepath.Join(root, "scripts", "testdata-absent-changelog.md")
}

// runPublishReleaseFrom runs the publisher in source, standing in for the
// workflow's checkout of the tag, whose tag list decides whether this is the
// newest release.
func runPublishReleaseFrom(source, root, fakeBin, fakeGitHub, tap, dist, tag, changelog string, extraEnvironment []string) ([]byte, error) {
	command := exec.Command(
		filepath.Join(root, "scripts", "publish-release.sh"),
		tag,
		dist,
		tap,
		"dgoings/workbook",
		changelog,
	)
	command.Dir = source
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
