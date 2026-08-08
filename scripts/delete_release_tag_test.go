package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Production mutation: Homebrew resolves its download URLs through the tag, so
// deleting one out from under a published release breaks installation for
// everyone the release already reached. Nothing in the recovery path is worth
// that, which is why it takes an explicit --force.
func TestDeleteReleaseTagRefusesATagWithAPublishedRelease(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin, "FAKE_PUBLISHED_TAGS=v0.5.0"),
		"0.5.0", "--repo", "dgoings/workbook")
	if err == nil {
		t.Fatalf("delete removed the tag of a published release:\n%s", output)
	}
	if !strings.Contains(output, "published release") {
		t.Errorf("output = %q, want a published-release refusal", output)
	}
	assertTagPresent(t, clone, remote, "v0.5.0")
}

func TestDeleteReleaseTagRemovesAnOrphanedTagLocallyAndOnTheRemote(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin),
		"0.5.0", "--repo", "dgoings/workbook")
	if err != nil {
		t.Fatalf("delete release tag: %v\n%s", err, output)
	}

	if got := gitOutput(t, clone, "tag", "--list", "v0.5.0"); got != "" {
		t.Errorf("local tags = %q, want v0.5.0 gone", got)
	}
	// A tag left on the remote is the one that still blocks the version, since
	// that is where the release workflow and the ordering check both read from.
	if got := gitOutput(t, remote, "tag", "--list", "v0.5.0"); got != "" {
		t.Errorf("remote tags = %q, want v0.5.0 gone", got)
	}
}

// The tag form is what a failing workflow logs, so pasting it back has to work.
func TestDeleteReleaseTagAcceptsTheTagForm(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin),
		"v0.5.0", "--repo", "dgoings/workbook")
	if err != nil {
		t.Fatalf("delete release tag: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "tag", "--list", "v0.5.0"); got != "" {
		t.Errorf("remote tags = %q, want v0.5.0 gone", got)
	}
}

func TestDeleteReleaseTagDryRunChangesNothing(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin),
		"0.5.0", "--repo", "dgoings/workbook", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, output)
	}
	assertTagPresent(t, clone, remote, "v0.5.0")
}

// Production mutation: an unanswerable question treated as "not published"
// would remove the safety check whenever gh happened to be missing.
func TestDeleteReleaseTagRefusesWhenReleaseStateCannotBeRead(t *testing.T) {
	clone, remote, _ := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone,
		environmentWithValues(os.Environ(), "PATH=/nonexistent"),
		"0.5.0", "--repo", "dgoings/workbook")
	if err == nil {
		t.Fatalf("delete ran without reading release state:\n%s", output)
	}
	if !strings.Contains(output, "--force") {
		t.Errorf("output = %q, want the override named", output)
	}
	assertTagPresent(t, clone, remote, "v0.5.0")
}

// Recovering from a run that left a draft behind should not need two tools.
func TestDeleteReleaseTagDeletesALeftoverDraftOnRequest(t *testing.T) {
	clone, _, fakeBin := newTagDeletionRepository(t)
	deleted := filepath.Join(t.TempDir(), "deleted.log")

	output, err := runDeleteReleaseTag(t, clone,
		environmentWithFakeCLI(fakeBin, "FAKE_DRAFT_TAGS=v0.5.0", "FAKE_GH_DELETED="+deleted),
		"0.5.0", "--repo", "dgoings/workbook", "--delete-draft")
	if err != nil {
		t.Fatalf("delete release tag: %v\n%s", err, output)
	}

	contents, err := os.ReadFile(deleted)
	if err != nil {
		t.Fatalf("read deleted releases: %v", err)
	}
	if !strings.Contains(string(contents), "v0.5.0") {
		t.Errorf("deleted releases = %q, want the draft removed", contents)
	}
}

// A draft is not public, so removing its tag without --delete-draft is allowed;
// the draft simply stays for the next run to reconcile against.
func TestDeleteReleaseTagRemovesATagHeldOnlyByADraft(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin, "FAKE_DRAFT_TAGS=v0.5.0"),
		"0.5.0", "--repo", "dgoings/workbook")
	if err != nil {
		t.Fatalf("delete release tag: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "tag", "--list", "v0.5.0"); got != "" {
		t.Errorf("remote tags = %q, want v0.5.0 gone", got)
	}
}

func TestDeleteReleaseTagReportsATagThatDoesNotExist(t *testing.T) {
	clone, _, fakeBin := newTagDeletionRepository(t)

	output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin),
		"9.9.9", "--repo", "dgoings/workbook")
	if err != nil {
		t.Fatalf("delete release tag: %v\n%s", err, output)
	}
	if !strings.Contains(output, "nothing to delete") {
		t.Errorf("output = %q, want a nothing-to-delete report", output)
	}
}

func TestDeleteReleaseTagRejectsUnsafeVersions(t *testing.T) {
	clone, remote, fakeBin := newTagDeletionRepository(t)

	for _, version := range []string{"0.5", "01.2.3", "1.2.3-alpha", "1.2.3/../etc"} {
		t.Run(version, func(t *testing.T) {
			output, err := runDeleteReleaseTag(t, clone, environmentWithFakeCLI(fakeBin),
				version, "--repo", "dgoings/workbook")
			if err == nil {
				t.Fatalf("delete accepted version %q:\n%s", version, output)
			}
		})
	}
	assertTagPresent(t, clone, remote, "v0.5.0")
}

func assertTagPresent(t *testing.T, clone, remote, tag string) {
	t.Helper()
	if got := gitOutput(t, clone, "tag", "--list", tag); got != tag {
		t.Errorf("local tags = %q, want %s intact", got, tag)
	}
	if got := gitOutput(t, remote, "tag", "--list", tag); got != tag {
		t.Errorf("remote tags = %q, want %s intact", got, tag)
	}
}

// newTagDeletionRepository builds a clone of a bare remote carrying an orphaned
// v0.5.0 tag, so the script runs against a repository shaped like the real one
// without touching it.
func newTagDeletionRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root, _ := renderFormulaPaths(t)
	remote := filepath.Join(t.TempDir(), "workbook.git")
	runCommand(t, "", nil, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	clone := filepath.Join(t.TempDir(), "workbook")
	runCommand(t, "", nil, "git", "clone", "--quiet", remote, clone)
	runCommand(t, clone, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, clone, nil, "git", "config", "user.email", "release-test@example.com")

	if err := os.Mkdir(filepath.Join(clone, "scripts"), 0o755); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	for _, name := range []string{"delete-release-tag.sh", "release-version.sh", "check-release-published.sh"} {
		contents, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(clone, "scripts", name), contents, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# Release fixture\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runCommand(t, clone, nil, "git", "add", ".")
	runCommand(t, clone, nil, "git", "commit", "--quiet", "-m", "init")
	runCommand(t, clone, nil, "git", "push", "--quiet", "-u", "origin", "main")
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.5.0", "--message", "Workbook v0.5.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.5.0")

	return clone, remote, newFakeReleaseCLI(t)
}

func runDeleteReleaseTag(t *testing.T, clone string, environment []string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command(filepath.Join(clone, "scripts", "delete-release-tag.sh"), args...)
	command.Dir = clone
	command.Env = environment
	output, err := command.CombinedOutput()
	return string(output), err
}
