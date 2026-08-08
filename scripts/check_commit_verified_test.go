package scripts_test

import (
	"strings"
	"testing"
)

// checkRuns renders the tab-separated name/status/conclusion rows the real gh
// produces after its --jq filter.
func checkRuns(rows ...[3]string) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, strings.Join(row[:], "\t"))
	}
	return strings.Join(lines, "\n")
}

func TestCheckCommitVerifiedAcceptsOnlyACompletelyGreenCommit(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		runs         string
		wantVerified bool
	}{
		{
			name: "every platform succeeded",
			runs: checkRuns(
				[3]string{"Verify on ubuntu-24.04", "completed", "success"},
				[3]string{"Verify on macos-15", "completed", "success"}),
			wantVerified: true,
		},
		{
			name: "one platform failed",
			runs: checkRuns(
				[3]string{"Verify on ubuntu-24.04", "completed", "success"},
				[3]string{"Verify on macos-15", "completed", "failure"}),
			wantVerified: false,
		},
		{
			// Tagging while the suite is still running would let a failure land
			// after the version number was already spent.
			name: "still running",
			runs: checkRuns(
				[3]string{"Verify on ubuntu-24.04", "in_progress", ""},
				[3]string{"Verify on macos-15", "completed", "success"}),
			wantVerified: false,
		},
		{
			name: "cancelled",
			runs: checkRuns(
				[3]string{"Verify on ubuntu-24.04", "completed", "cancelled"},
				[3]string{"Verify on macos-15", "completed", "success"}),
			wantVerified: false,
		},
		{
			// A commit CI never ran is exactly the commit worth refusing, and
			// finding no matching runs must not read as nothing-to-object-to.
			name:         "CI never ran for this commit",
			runs:         checkRuns([3]string{"some unrelated check", "completed", "success"}),
			wantVerified: false,
		},
		{
			name:         "no check runs at all",
			runs:         "",
			wantVerified: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fakeBin := newFakeReleaseCLI(t)
			output, err := runReleaseScriptWithEnvironment(t, "", "check-commit-verified.sh", "",
				environmentWithFakeCLI(fakeBin, "FAKE_CHECK_RUNS="+testCase.runs),
				"abc123", "--repo", "dgoings/workbook")

			if testCase.wantVerified && err != nil {
				t.Fatalf("check refused a green commit: %v\n%s", err, output)
			}
			if !testCase.wantVerified && err == nil {
				t.Fatalf("check verified a commit it should refuse:\n%s", output)
			}
		})
	}
}

// The gate matches check run names by prefix rather than counting them, so a
// platform added to the CI matrix is required here without anyone remembering
// to update this.
func TestCheckCommitVerifiedRequiresPlatformsAddedToTheMatrix(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	runs := checkRuns(
		[3]string{"Verify on ubuntu-24.04", "completed", "success"},
		[3]string{"Verify on macos-15", "completed", "success"},
		[3]string{"Verify on windows-2025", "completed", "failure"})

	output, err := runReleaseScriptWithEnvironment(t, "", "check-commit-verified.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_CHECK_RUNS="+runs),
		"abc123", "--repo", "dgoings/workbook")
	if err == nil {
		t.Fatalf("check ignored a platform it had not been told about:\n%s", output)
	}
	if !strings.Contains(output, "windows-2025") {
		t.Errorf("output = %q, want the failing platform named", output)
	}
}

// Production mutation: an unreachable API reported as "not verified" is a
// nuisance, but reported as "verified" it silently removes the gate.
func TestCheckCommitVerifiedTreatsAnUnreadableAPIAsAnError(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)

	output, err := runReleaseScriptWithEnvironment(t, "", "check-commit-verified.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_CHECK_RUNS_FAIL=1"),
		"abc123", "--repo", "dgoings/workbook")
	if err == nil {
		t.Fatalf("check verified a commit it could not read:\n%s", output)
	}
	if code := exitCode(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 for an unanswered question", code)
	}
}

// Check run names carry spaces, so splitting on whitespace rather than tabs
// would truncate every name to "Verify" and match nothing.
func TestCheckCommitVerifiedReadsNamesContainingSpaces(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	runs := checkRuns([3]string{"Verify on ubuntu-24.04", "completed", "success"})

	output, err := runReleaseScriptWithEnvironment(t, "", "check-commit-verified.sh", "",
		environmentWithFakeCLI(fakeBin, "FAKE_CHECK_RUNS="+runs),
		"abc123", "--repo", "dgoings/workbook")
	if err != nil {
		t.Fatalf("check refused a green commit: %v\n%s", err, output)
	}
	if !strings.Contains(output, "1 CI check run") {
		t.Errorf("output = %q, want one matched check run", output)
	}
}
