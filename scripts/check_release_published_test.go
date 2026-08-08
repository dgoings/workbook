package scripts_test

import (
	"strings"
	"testing"
)

// A tag alone does not mean a release shipped, and the difference is what the
// changelog check and the tag deletion tool both key on.
func TestCheckReleasePublishedDistinguishesReleaseStates(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		environment   []string
		wantPublished bool
	}{
		{
			name:          "published release",
			environment:   []string{"FAKE_PUBLISHED_TAGS=v0.4.1 v0.5.0"},
			wantPublished: true,
		},
		{
			// publish-release.sh stages assets in a draft and publishes it last,
			// so a surviving draft is a failed run rather than a release.
			name:          "draft left behind by a failed run",
			environment:   []string{"FAKE_DRAFT_TAGS=v0.5.0"},
			wantPublished: false,
		},
		{
			name:          "tag with no release at all",
			environment:   nil,
			wantPublished: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fakeBin := newFakeReleaseCLI(t)
			output, err := runReleaseScriptWithEnvironment(t, "", "check-release-published.sh", "",
				environmentWithFakeCLI(fakeBin, testCase.environment...),
				"v0.5.0", "--repo", "dgoings/workbook")

			if testCase.wantPublished && err != nil {
				t.Fatalf("check reported an unpublished release: %v\n%s", err, output)
			}
			if !testCase.wantPublished && err == nil {
				t.Fatalf("check reported a published release:\n%s", output)
			}
		})
	}
}

// Production mutation: an unanswerable question reported as "not published"
// would let delete-release-tag.sh remove the tag of a live release whenever the
// GitHub CLI is merely absent.
func TestCheckReleasePublishedSeparatesUnknownFromUnpublished(t *testing.T) {
	output, err := runReleaseScriptWithEnvironment(t, "", "check-release-published.sh", "",
		environmentWithValues(nil, "PATH=/nonexistent", "GITHUB_REPOSITORY=dgoings/workbook"),
		"v0.5.0")
	if err == nil {
		t.Fatalf("check answered without gh:\n%s", output)
	}
	if code := exitCode(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 to mark the question unanswered rather than answered no", code)
	}
	if !strings.Contains(output, "gh is required") {
		t.Errorf("output = %q, want a missing-gh error", output)
	}
}

func TestCheckReleasePublishedRequiresATagAndRepository(t *testing.T) {
	fakeBin := newFakeReleaseCLI(t)
	if output, err := runReleaseScriptWithEnvironment(t, "", "check-release-published.sh", "",
		environmentWithFakeCLI(fakeBin), "--repo", "dgoings/workbook"); err == nil {
		t.Fatalf("check ran without a tag:\n%s", output)
	}
	if output, err := runReleaseScriptWithEnvironment(t, "", "check-release-published.sh", "",
		environmentWithValues(nil, "PATH="+fakeBin), "v0.5.0"); err == nil {
		t.Fatalf("check ran without a repository:\n%s", output)
	}
}
