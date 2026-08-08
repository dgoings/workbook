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

// A missing changelog carries no entries, which is the state the repository is
// in until the first release entry is written.
func TestCheckReleaseChangelogTreatsAMissingFileAsNoEntries(t *testing.T) {
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
