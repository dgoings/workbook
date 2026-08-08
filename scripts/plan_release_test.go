package scripts_test

import (
	"strings"
	"testing"
)

// The whole cut decision, exercised the way a workflow makes it: ask for a bump
// and get back a version, or get back a refusal.
func TestPlanReleaseResolvesAVersionAfterAPublishedRelease(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	path := writeChangelog(t, changelogForMinor)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1"),
		"--bump", "minor", "--repo", "dgoings/workbook",
		"--previous", "v0.4.1", "--changelog", path)
	if err != nil {
		t.Fatalf("plan release: %v\n%s", err, output)
	}
	// Diagnostics go to stderr so a caller can capture the version directly.
	if got := lastLine(output); got != "0.5.0" {
		t.Errorf("planned version %q, want 0.5.0", got)
	}
}

// The failure this whole path exists to catch, reached the way a releaser would
// actually reach it: retrying a release whose tag survived its failure.
func TestPlanReleaseRefusesARetryThatWouldStrandTheEntry(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	path := writeChangelog(t, changelogForMinor)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		// v0.5.0 was tagged but published nothing.
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1"),
		"--bump", "patch", "--repo", "dgoings/workbook",
		"--previous", "v0.5.0", "--changelog", path)
	if err == nil {
		t.Fatalf("plan release cut past an orphaned entry:\n%s", output)
	}
	if !strings.Contains(output, "published no release") {
		t.Errorf("output = %q, want the unpublished previous release reported", output)
	}
}

// A draft is the debris of a failed publication, not a release.
func TestPlanReleaseTreatsASurvivingDraftAsUnpublished(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	path := writeChangelog(t, changelogForMinor)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_DRAFT_TAGS=v0.5.0"),
		"--bump", "patch", "--repo", "dgoings/workbook",
		"--previous", "v0.5.0", "--changelog", path)
	if err == nil {
		t.Fatalf("plan release treated a draft as a published release:\n%s", output)
	}
}

// Production mutation: applying the bump kind that was chosen rather than the
// distance actually travelled would let "patch" plus an exact 1.0.0 skip the
// changelog entry a major release has to carry.
func TestPlanReleaseHoldsAnExplicitVersionToTheDistanceItTravels(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	// An entry for 0.5.0 only, so a major release has nothing describing it.
	path := writeChangelog(t, changelogForMinor)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1"),
		"--bump", "patch", "--version", "1.0.0", "--repo", "dgoings/workbook",
		"--previous", "v0.4.1", "--changelog", path)
	if err == nil {
		t.Fatalf("plan release let a patch bump carry a major version past the changelog:\n%s", output)
	}
	if !strings.Contains(output, "release:major") {
		t.Errorf("output = %q, want the release judged as a major one", output)
	}
}

// A bare patch is the escape hatch, and it has to stay open.
func TestPlanReleaseCutsAPatchWithNoEntry(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	path := writeChangelog(t, changelogWithoutANewEntry)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1"),
		"--bump", "patch", "--repo", "dgoings/workbook",
		"--previous", "v0.4.1", "--changelog", path)
	if err != nil {
		t.Fatalf("plan release: %v\n%s", err, output)
	}
	if got := lastLine(output); got != "0.4.2" {
		t.Errorf("planned version %q, want 0.4.2", got)
	}
}

func TestPlanReleaseRequiresABumpOrAVersion(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin), "--repo", "dgoings/workbook", "--previous", "v0.4.1")
	if err == nil {
		t.Fatalf("plan release ran without a bump or a version:\n%s", output)
	}
}

func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
