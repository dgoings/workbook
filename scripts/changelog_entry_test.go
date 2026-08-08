package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleChangelog = `# Changelog

## v0.5.0 — 2026-08-08

### Added
- board reconcile rendering

### Fixed
- bounded full validation

## v0.4.1

### Fixed
- the previous release
`

func TestChangelogEntryPrintsTheBodyUpToTheNextHeading(t *testing.T) {
	path := writeChangelog(t, sampleChangelog)

	output, err := runChangelogEntry(t, "0.5.0", path)
	if err != nil {
		t.Fatalf("changelog entry: %v\n%s", err, output)
	}

	want := "### Added\n- board reconcile rendering\n\n### Fixed\n- bounded full validation\n"
	if output != want {
		t.Errorf("entry = %q, want %q", output, want)
	}
}

// The newest entry is the common case, but the oldest one has no following
// heading to stop at, and running past the end would publish nothing.
func TestChangelogEntryPrintsTheLastEntryInTheFile(t *testing.T) {
	path := writeChangelog(t, sampleChangelog)

	output, err := runChangelogEntry(t, "0.4.1", path)
	if err != nil {
		t.Fatalf("changelog entry: %v\n%s", err, output)
	}

	want := "### Fixed\n- the previous release\n"
	if output != want {
		t.Errorf("entry = %q, want %q", output, want)
	}
}

// A heading may carry a date, and the release notes should not include it or
// stop matching because of it.
func TestChangelogEntryMatchesAHeadingWithOrWithoutADate(t *testing.T) {
	path := writeChangelog(t, "# Changelog\n\n## v1.2.3\n\n- undated\n")

	output, err := runChangelogEntry(t, "1.2.3", path)
	if err != nil {
		t.Fatalf("changelog entry: %v\n%s", err, output)
	}
	if output != "- undated\n" {
		t.Errorf("entry = %q, want the undated body", output)
	}
}

// Production mutation: exiting zero for a version with no entry would publish
// an empty release body in place of the generated notes.
func TestChangelogEntryReportsAnAbsentVersion(t *testing.T) {
	path := writeChangelog(t, sampleChangelog)

	output, err := runChangelogEntry(t, "9.9.9", path)
	if err == nil {
		t.Fatalf("changelog entry found an absent version:\n%s", output)
	}
	if !strings.Contains(output, "no changelog entry for v9.9.9") {
		t.Errorf("output = %q, want an absent-entry error", output)
	}
}

// The version reaches grep as a pattern. Unescaped, 0.5.0 would match a heading
// reading v0x5y0 and publish the wrong release's notes.
func TestChangelogEntryTreatsDotsLiterally(t *testing.T) {
	path := writeChangelog(t, "# Changelog\n\n## v0x5y0\n\n- decoy\n")

	output, err := runChangelogEntry(t, "0.5.0", path)
	if err == nil {
		t.Fatalf("changelog entry matched a dot as a wildcard:\n%s", output)
	}
}

// A heading is matched literally and has to end where the version ends, or
// v0.5.0 would take the body written for v0.5.01.
func TestChangelogEntryDoesNotMatchALongerVersion(t *testing.T) {
	path := writeChangelog(t, "# Changelog\n\n## v0.5.01\n\n- longer version\n\n## v0.5.0\n\n- exact\n")

	output, err := runChangelogEntry(t, "0.5.0", path)
	if err != nil {
		t.Fatalf("changelog entry: %v\n%s", err, output)
	}
	if output != "- exact\n" {
		t.Errorf("entry = %q, want the body of the exact version", output)
	}
}

// gawk processes escape sequences when it assigns a -v variable and warns on
// stdout's neighbour while doing it, so a pattern passed in that way both
// polluted the extracted body and quietly lost its escaping. Nothing the script
// hands awk may contain a backslash.
func TestChangelogEntryEmitsNothingButTheBody(t *testing.T) {
	path := writeChangelog(t, sampleChangelog)

	output, err := runChangelogEntry(t, "0.5.0", path)
	if err != nil {
		t.Fatalf("changelog entry: %v\n%s", err, output)
	}
	if strings.Contains(output, "warning") || strings.Contains(output, "awk") {
		t.Errorf("entry = %q, want no tool diagnostics mixed into the release notes", output)
	}
}

func TestChangelogEntryRejectsAnEmptyBody(t *testing.T) {
	path := writeChangelog(t, "# Changelog\n\n## v0.5.0\n\n## v0.4.1\n\n- old\n")

	output, err := runChangelogEntry(t, "0.5.0", path)
	if err == nil {
		t.Fatalf("changelog entry accepted an empty body:\n%s", output)
	}
	if !strings.Contains(output, "is empty") {
		t.Errorf("output = %q, want an empty-entry error", output)
	}
}

func TestChangelogEntryReportsAMissingChangelog(t *testing.T) {
	output, err := runChangelogEntry(t, "0.5.0", filepath.Join(t.TempDir(), "absent.md"))
	if err == nil {
		t.Fatalf("changelog entry read a missing file:\n%s", output)
	}
	if !strings.Contains(output, "no changelog at") {
		t.Errorf("output = %q, want a missing-changelog error", output)
	}
}

func runChangelogEntry(t *testing.T, version, path string) (string, error) {
	t.Helper()
	return runReleaseScript(t, "", "changelog-entry.sh", "", version, path)
}

func writeChangelog(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write changelog: %v", err)
	}
	return path
}
