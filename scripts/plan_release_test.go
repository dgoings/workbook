package scripts_test

import (
	"strings"
	"testing"
)

// The whole cut decision, exercised the way a workflow makes it: ask for a bump
// and get back a version, or get back a refusal.
func TestPlanReleaseResolvesAVersionAfterAPublishedRelease(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A pre-release is not a release, so the changelog is not consulted: the
// Unreleased heading stays where it is and the version still resolves.
func TestPlanReleaseCutsAPreReleaseWithoutAChangelogEntry(t *testing.T) {
	t.Parallel()
	fakeBin := newFakeReleaseCLI(t)
	path := writeChangelog(t, changelogWithoutANewEntry)

	output, err := runReleaseScriptWithEnvironment(t, "", "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1"),
		"--bump", "patch", "--version", "0.5.0-rc1", "--repo", "dgoings/workbook",
		"--previous", "v0.4.1", "--changelog", path)
	if err != nil {
		t.Fatalf("plan pre-release: %v\n%s", err, output)
	}
	if got := lastLine(output); got != "0.5.0-rc1" {
		t.Errorf("planned version %q, want 0.5.0-rc1", got)
	}
	if !strings.Contains(output, "needs no CHANGELOG entry") {
		t.Errorf("output = %q, want the changelog skip reported", output)
	}
}

// Production mutation: discovering the previous tag lexically, or without
// skipping pre-releases, would make the release after an rc compute from the
// rc and demand a changelog entry for a version nobody is cutting.
func TestPlanReleaseIgnoresPreReleaseTagsWhenPlanningAStableRelease(t *testing.T) {
	t.Parallel()
	fakeBin := newFakeReleaseCLI(t)
	repository := newTaggedRepository(t, "v0.4.1", "v0.5.0-rc1", "v0.5.0-rc2")
	path := writeChangelog(t, changelogForMinor)

	output, err := runReleaseScriptWithEnvironment(t, repository, "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1 v0.5.0-rc1 v0.5.0-rc2"),
		"--bump", "minor", "--repo", "dgoings/workbook", "--changelog", path)
	if err != nil {
		t.Fatalf("plan release after rcs: %v\n%s", err, output)
	}
	if got := lastLine(output); got != "0.5.0" {
		t.Errorf("planned version %q, want 0.5.0 computed from v0.4.1", got)
	}
}

// The next rc has to clear the last one, which only discovery over every tag
// can see.
func TestPlanReleaseDiscoversTheNewestPreReleaseForTheNextOne(t *testing.T) {
	t.Parallel()
	fakeBin := newFakeReleaseCLI(t)
	repository := newTaggedRepository(t, "v0.4.1", "v0.5.0-rc1", "v0.5.0-rc2")
	path := writeChangelog(t, changelogWithoutANewEntry)

	output, err := runReleaseScriptWithEnvironment(t, repository, "plan-release.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.4.1 v0.5.0-rc1 v0.5.0-rc2"),
		"--bump", "patch", "--version", "0.5.0-rc2", "--repo", "dgoings/workbook", "--changelog", path)
	if err == nil {
		t.Fatalf("plan release reused an rc number:\n%s", output)
	}
	if !strings.Contains(output, "does not come after") {
		t.Errorf("output = %q, want an ordering error against v0.5.0-rc2", output)
	}
}

// newTaggedRepository makes a repository with one commit and the given tags,
// for scripts that discover the previous release themselves.
func newTaggedRepository(t *testing.T, tags ...string) string {
	t.Helper()
	repository := t.TempDir()
	runCommand(t, repository, nil, "git", "init", "--quiet", "--initial-branch=main")
	runCommand(t, repository, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, repository, nil, "git", "config", "user.email", "release-test@example.com")
	runCommand(t, repository, nil, "git", "commit", "--quiet", "--allow-empty", "-m", "init")
	for _, tag := range tags {
		runCommand(t, repository, nil, "git", "tag", tag)
	}
	return repository
}

// A maintenance line's changelog, as maint/v0.5 carries it: the branch starts
// from v0.5.1, so nothing after that release is in it.
const changelogOnTheLine = "# Changelog\n\n## v0.5.1 — 2026-08-23\n\n- the line's previous release\n"

// A patch to an older line follows that line's newest release, not the newest
// release there is. Production mutation: discovering the previous release
// across every line would compute 0.6.1 on maint/v0.5 and publish code from
// the 0.5 line under a 0.6 number.
func TestPlanReleaseOnALineBranchPatchesThatLine(t *testing.T) {
	t.Parallel()
	fakeBin := newFakeReleaseCLI(t)
	repository := newTaggedRepository(t, "v0.5.0", "v0.5.1", "v0.6.0")
	environment := environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.5.0 v0.5.1 v0.6.0")

	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "a patch bump", args: []string{"--bump", "patch"}, want: "0.5.2"},
		{name: "an explicit version on the line", args: []string{"--bump", "patch", "--version", "0.5.2"}, want: "0.5.2"},
		// The Actions button's bump defaults to patch, but a typed version
		// overrides it, so the version is what has to be on the line.
		{name: "an explicit version beside another bump", args: []string{"--bump", "minor", "--version", "0.5.2"}, want: "0.5.2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := writeChangelog(t, changelogOnTheLine)
			args := append([]string{"--branch", "maint/v0.5", "--repo", "dgoings/workbook", "--changelog", path}, testCase.args...)
			output, err := runReleaseScriptWithEnvironment(t, repository, "plan-release.sh", "", environment, args...)
			if err != nil {
				t.Fatalf("plan release on maint/v0.5: %v\n%s", err, output)
			}
			if got := lastLine(output); got != testCase.want {
				t.Errorf("planned version %q, want %s", got, testCase.want)
			}
		})
	}

	// The trunk is unaffected by the line: main still patches the newest release.
	t.Run("main", func(t *testing.T) {
		t.Parallel()
		path := writeChangelog(t, "# Changelog\n\n## v0.6.0\n\n- the newest release\n\n## v0.5.1\n\n- older\n")
		output, err := runReleaseScriptWithEnvironment(t, repository, "plan-release.sh", "", environment,
			"--branch", "main", "--bump", "patch", "--repo", "dgoings/workbook", "--changelog", path)
		if err != nil {
			t.Fatalf("plan release on main: %v\n%s", err, output)
		}
		if got := lastLine(output); got != "0.6.1" {
			t.Errorf("planned version %q, want 0.6.1", got)
		}
	})
}

// A line exists to ship fixes to people who cannot take the next minor, so
// only a stable patch to that line may be cut on it. Each refusal says what to
// do instead rather than computing something nobody asked for.
func TestPlanReleaseOnALineBranchRefusesAnythingButAPatchToThatLine(t *testing.T) {
	t.Parallel()
	fakeBin := newFakeReleaseCLI(t)
	repository := newTaggedRepository(t, "v0.5.1", "v0.6.0")
	environment := environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.5.1 v0.6.0")

	for _, testCase := range []struct {
		name        string
		branch      string
		args        []string
		wantMessage string
	}{
		{name: "a minor bump", branch: "maint/v0.5", args: []string{"--bump", "minor"}, wantMessage: "patch releases only"},
		{name: "a major bump", branch: "maint/v0.5", args: []string{"--bump", "major"}, wantMessage: "patch releases only"},
		{name: "a version on another line", branch: "maint/v0.5", args: []string{"--bump", "patch", "--version", "0.6.1"}, wantMessage: "is not on the 0.5 line"},
		{name: "a minor version", branch: "maint/v0.5", args: []string{"--bump", "patch", "--version", "0.7.0"}, wantMessage: "is not on the 0.5 line"},
		{name: "a candidate", branch: "maint/v0.5", args: []string{"--bump", "patch", "--version", "0.5.2-rc1"}, wantMessage: "patch releases only"},
		{name: "a line with no release", branch: "maint/v0.4", args: []string{"--bump", "patch"}, wantMessage: "no v0.4.* release"},
		{name: "a malformed line", branch: "maint/v0.5.1", args: []string{"--bump", "patch"}, wantMessage: "maint/vMAJOR.MINOR"},
		{name: "a feature branch", branch: "feature", args: []string{"--bump", "patch"}, wantMessage: "cut from main or a maint/vMAJOR.MINOR line"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := writeChangelog(t, changelogOnTheLine)
			args := append([]string{"--branch", testCase.branch, "--repo", "dgoings/workbook", "--changelog", path}, testCase.args...)
			output, err := runReleaseScriptWithEnvironment(t, repository, "plan-release.sh", "", environment, args...)
			if err == nil {
				t.Fatalf("plan release on %s accepted %v:\n%s", testCase.branch, testCase.args, output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
		})
	}
}
