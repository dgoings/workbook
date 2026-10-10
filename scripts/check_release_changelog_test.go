package scripts_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// The changelog states of interest, all read against a previous release of
// v0.4.1.
const (
	// The newest entry is the previous release: the author added nothing.
	changelogWithoutANewEntry = "# Changelog\n\n## v0.4.1\n\n- the previous release\n"
	// A new entry for the version a patch bump would compute.
	changelogForPatch = "# Changelog\n\n## v0.4.2\n\n- a patch note\n\n## v0.4.1\n\n- the previous release\n"
	// A new entry for the version a minor bump would compute.
	changelogForMinor = "# Changelog\n\n## v0.5.0 — 2026-08-08\n\n- a minor note\n\n## v0.4.1\n\n- the previous release\n"
	// Seeded but never written to.
	changelogWithNoEntries = "# Changelog\n\nEntries begin with the next release.\n"
)

// The label and the heading are two independent expressions of the same intent.
// Each row is one way they can agree or disagree.
func TestCheckReleaseChangelogAppliesTheAgreementMatrix(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		bump      string
		version   string
		changelog string
		wantCut   bool
	}{
		{name: "patch with no new entry", bump: "patch", version: "0.4.2", changelog: changelogWithoutANewEntry, wantCut: true},
		{name: "patch with a matching entry", bump: "patch", version: "0.4.2", changelog: changelogForPatch, wantCut: true},
		{name: "patch with an empty changelog", bump: "patch", version: "0.4.2", changelog: changelogWithNoEntries, wantCut: true},
		{name: "patch with a mismatched entry", bump: "patch", version: "0.4.2", changelog: changelogForMinor, wantCut: false},
		{name: "minor with no new entry", bump: "minor", version: "0.5.0", changelog: changelogWithoutANewEntry, wantCut: false},
		{name: "minor with a matching entry", bump: "minor", version: "0.5.0", changelog: changelogForMinor, wantCut: true},
		{name: "minor with an empty changelog", bump: "minor", version: "0.5.0", changelog: changelogWithNoEntries, wantCut: false},
		{name: "minor with a mismatched entry", bump: "minor", version: "0.5.0", changelog: changelogForPatch, wantCut: false},
		{name: "major with no new entry", bump: "major", version: "1.0.0", changelog: changelogWithoutANewEntry, wantCut: false},
		{name: "major with a mismatched entry", bump: "major", version: "1.0.0", changelog: changelogForMinor, wantCut: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeChangelog(t, testCase.changelog)
			output, err := runCheckReleaseChangelog(t,
				"--bump", testCase.bump,
				"--version", testCase.version,
				"--previous", "v0.4.1",
				"--changelog", path)
			if testCase.wantCut && err != nil {
				t.Fatalf("check refused a release it should cut: %v\n%s", err, output)
			}
			// Production mutation: accepting a disagreement cuts a release whose
			// tag and whose notes describe different versions.
			if !testCase.wantCut && err == nil {
				t.Fatalf("check accepted a disagreement:\n%s", output)
			}
		})
	}
}

// A release that dies after its tag is pushed leaves the tag behind, and the
// entry written for it then describes something nobody can download. Read as
// the ordinary "newest entry is the last release's" case, a retry cuts the next
// version, generates notes for it, and strands that entry in the file forever.
//
// Production mutation: this is the one disagreement the check would otherwise
// pass over in silence, because every other mismatch fails loudly.
func TestCheckReleaseChangelogRefusesAnEntryForATagThatNeverPublished(t *testing.T) {
	t.Parallel()
	for _, bump := range []string{"patch", "minor", "major"} {
		t.Run(bump, func(t *testing.T) {
			// v0.5.0 was tagged, the release failed, and its entry is newest.
			path := writeChangelog(t, changelogForMinor)
			version := map[string]string{"patch": "0.5.1", "minor": "0.6.0", "major": "1.0.0"}[bump]

			output, err := runCheckReleaseChangelog(t,
				"--bump", bump,
				"--version", version,
				"--previous", "v0.5.0",
				"--previous-released", "no",
				"--changelog", path)
			if err == nil {
				t.Fatalf("check cut past an orphaned entry:\n%s", output)
			}
			// Both repairs are legitimate and only the releaser knows which is
			// wanted, so the message has to offer both rather than pick one.
			if !strings.Contains(output, "delete-release-tag.sh 0.5.0") {
				t.Errorf("output = %q, want the tag deletion repair named", output)
			}
			if !strings.Contains(output, "retitle") {
				t.Errorf("output = %q, want the retitle repair named", output)
			}
		})
	}
}

// The same shape with a released previous tag is the ordinary steady state and
// has to keep working, or every patch release after a successful one breaks.
func TestCheckReleaseChangelogAllowsAPatchAfterAPublishedRelease(t *testing.T) {
	t.Parallel()
	path := writeChangelog(t, changelogForMinor)

	output, err := runCheckReleaseChangelog(t,
		"--bump", "patch",
		"--version", "0.5.1",
		"--previous", "v0.5.0",
		"--previous-released", "yes",
		"--changelog", path)
	if err != nil {
		t.Fatalf("check refused an ordinary patch release: %v\n%s", err, output)
	}
}

func TestCheckReleaseChangelogRejectsAnUnknownPreviousReleasedValue(t *testing.T) {
	t.Parallel()
	path := writeChangelog(t, changelogForPatch)

	output, err := runCheckReleaseChangelog(t,
		"--bump", "patch", "--version", "0.4.2", "--previous", "v0.4.1",
		"--previous-released", "maybe", "--changelog", path)
	if err == nil {
		t.Fatalf("check accepted an unknown --previous-released value:\n%s", output)
	}
}

// A missing changelog carries no entries, which is the state the repository is
// in until the first release entry is written.
func TestCheckReleaseChangelogTreatsAMissingFileAsNoEntries(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "CHANGELOG.md")

	output, err := runCheckReleaseChangelog(t, "--bump", "patch", "--version", "0.4.2", "--previous", "v0.4.1", "--changelog", absent)
	if err != nil {
		t.Fatalf("patch refused a missing changelog: %v\n%s", err, output)
	}

	output, err = runCheckReleaseChangelog(t, "--bump", "minor", "--version", "0.5.0", "--previous", "v0.4.1", "--changelog", absent)
	if err == nil {
		t.Fatalf("minor accepted a missing changelog:\n%s", output)
	}
	if !strings.Contains(output, "has no entries") {
		t.Errorf("output = %q, want a no-entries error", output)
	}
}

// The message has to name both sides, because the fix differs depending on
// which one is wrong.
func TestCheckReleaseChangelogNamesBothSidesOfADisagreement(t *testing.T) {
	t.Parallel()
	path := writeChangelog(t, changelogForMinor)

	output, err := runCheckReleaseChangelog(t, "--bump", "patch", "--version", "0.4.2", "--previous", "v0.4.1", "--changelog", path)
	if err == nil {
		t.Fatalf("check accepted a disagreement:\n%s", output)
	}
	if !strings.Contains(output, "v0.4.2") || !strings.Contains(output, "v0.5.0") {
		t.Errorf("output = %q, want both the implied version and the entry version", output)
	}
}

func TestCheckReleaseChangelogRequiresABumpAndAVersion(t *testing.T) {
	t.Parallel()
	path := writeChangelog(t, changelogForPatch)

	if output, err := runCheckReleaseChangelog(t, "--version", "0.4.2", "--changelog", path); err == nil {
		t.Fatalf("check ran without a bump:\n%s", output)
	}
	if output, err := runCheckReleaseChangelog(t, "--bump", "patch", "--changelog", path); err == nil {
		t.Fatalf("check ran without a version:\n%s", output)
	}
	if output, err := runCheckReleaseChangelog(t, "--bump", "sideways", "--version", "0.4.2", "--changelog", path); err == nil {
		t.Fatalf("check accepted an unknown bump kind:\n%s", output)
	}
}

func runCheckReleaseChangelog(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runReleaseScript(t, "", "check-release-changelog.sh", "", args...)
}

// The check reads the previous release from the repository when none is
// given. A pre-release tag is not a release, so it must not become the
// previous one: a patch after v0.5.0-rc1 is 0.4.2, cut with no entry.
func TestCheckReleaseChangelogDiscoversTheNewestStableRelease(t *testing.T) {
	t.Parallel()
	repository := newTaggedRepository(t, "v0.4.1", "v0.5.0-rc1")
	path := writeChangelog(t, changelogWithoutANewEntry)

	output, err := runReleaseScript(t, repository, "check-release-changelog.sh", "",
		"--bump", "patch", "--version", "0.4.2", "--changelog", path)
	if err != nil {
		t.Fatalf("check with discovered previous: %v\n%s", err, output)
	}
	if !strings.Contains(output, "cuts v0.4.2") {
		t.Errorf("output = %q, want the patch cut against v0.4.1", output)
	}
}

// A backport's entry is written on its line and forward-ported to main's
// changelog afterwards, in version order. The check reads the highest-versioned
// heading wherever it sits, and treats one that does not clear the previous
// release as no entry at all, so an older entry can never stand in for the
// next release or block it.
func TestCheckReleaseChangelogReadsTheHighestVersionedHeading(t *testing.T) {
	t.Parallel()
	const (
		// The forward-ported v0.5.2 entry, in version order below v0.6.0.
		forwardPorted = "# Changelog\n\n## v0.6.0 — 2026-10-08\n\n- the minor\n\n## v0.5.2 — 2026-10-12\n\n- the backport\n\n## v0.5.1\n\n- older\n"
		// The same entry pasted at the top by mistake. Production mutation:
		// reading the topmost heading would call v0.5.2 the entry being cut.
		forwardPortedOnTop = "# Changelog\n\n## v0.5.2 — 2026-10-12\n\n- the backport\n\n## v0.6.0 — 2026-10-08\n\n- the minor\n\n## v0.5.1\n\n- older\n"
		// The next minor's entry sits below a forward-ported one.
		nextMinorBelowABackport = "# Changelog\n\n## v0.5.2 — 2026-10-12\n\n- the backport\n\n## v0.7.0 — 2026-11-01\n\n- the next minor\n\n## v0.6.0\n\n- the minor\n"
		// v0.6.1 was cut as a patch with no entry, so the newest heading is
		// older than the previous release.
		patchedWithoutAnEntry = "# Changelog\n\n## v0.6.0 — 2026-10-08\n\n- the minor\n"
	)
	for _, testCase := range []struct {
		name      string
		bump      string
		version   string
		previous  string
		changelog string
		wantCut   bool
		wantText  string
	}{
		{name: "a forward-ported entry does not block a patch", bump: "patch", version: "0.6.1", previous: "v0.6.0", changelog: forwardPorted, wantCut: true, wantText: "with no changelog entry"},
		{name: "a forward-ported entry on top does not block a patch", bump: "patch", version: "0.6.1", previous: "v0.6.0", changelog: forwardPortedOnTop, wantCut: true, wantText: "with no changelog entry"},
		{name: "a forward-ported entry is not a minor's entry", bump: "minor", version: "0.7.0", previous: "v0.6.0", changelog: forwardPortedOnTop, wantCut: false, wantText: "newest entry is still the previous release v0.6.0"},
		{name: "the highest heading is the entry", bump: "minor", version: "0.7.0", previous: "v0.6.0", changelog: nextMinorBelowABackport, wantCut: true, wantText: "matching the newest CHANGELOG entry"},
		{name: "a heading below the previous release is no entry", bump: "patch", version: "0.6.2", previous: "v0.6.1", changelog: patchedWithoutAnEntry, wantCut: true, wantText: "with no changelog entry"},
		{name: "a minor after an entry-less patch still needs one", bump: "minor", version: "0.7.0", previous: "v0.6.1", changelog: patchedWithoutAnEntry, wantCut: false, wantText: "is older than the previous release v0.6.1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := writeChangelog(t, testCase.changelog)
			output, err := runCheckReleaseChangelog(t,
				"--bump", testCase.bump,
				"--version", testCase.version,
				"--previous", testCase.previous,
				"--changelog", path)
			if testCase.wantCut && err != nil {
				t.Fatalf("check refused a release it should cut: %v\n%s", err, output)
			}
			if !testCase.wantCut && err == nil {
				t.Fatalf("check accepted a release it should refuse:\n%s", output)
			}
			if !strings.Contains(output, testCase.wantText) {
				t.Errorf("output = %q, want %q", output, testCase.wantText)
			}
		})
	}
}

// On a maintenance line the baseline is that line's previous release. An
// entry for v0.5.2 on maint/v0.5 is the entry being cut there, not an older
// one, so it has to match the version; measured against main's v0.6.0 it would
// read as absent and let v0.5.3 ship with v0.5.2's prose unpublished.
func TestCheckReleaseChangelogOnALineComparesAgainstThatLine(t *testing.T) {
	t.Parallel()
	repository := newTaggedRepository(t, "v0.5.1", "v0.6.0")
	path := writeChangelog(t, "# Changelog\n\n## v0.5.2 — 2026-10-12\n\n- the backport\n\n## v0.5.1\n\n- the line's previous release\n")

	output, err := runReleaseScript(t, repository, "check-release-changelog.sh", "",
		"--branch", "maint/v0.5", "--bump", "patch", "--version", "0.5.2", "--changelog", path)
	if err != nil {
		t.Fatalf("check on maint/v0.5: %v\n%s", err, output)
	}
	if !strings.Contains(output, "matching the newest CHANGELOG entry") {
		t.Errorf("output = %q, want the v0.5.2 entry matched", output)
	}

	output, err = runReleaseScript(t, repository, "check-release-changelog.sh", "",
		"--branch", "maint/v0.5", "--bump", "patch", "--version", "0.5.3", "--changelog", path)
	if err == nil {
		t.Fatalf("check on maint/v0.5 cut v0.5.3 past the v0.5.2 entry:\n%s", output)
	}
	if !strings.Contains(output, "newest CHANGELOG entry is v0.5.2") {
		t.Errorf("output = %q, want the disagreement named", output)
	}
}
