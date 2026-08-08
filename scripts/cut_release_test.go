package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCutReleaseTagsAndPushesTheReleaseTag(t *testing.T) {
	clone, remote := newReleaseRepository(t)
	head := gitOutput(t, clone, "rev-parse", "HEAD")

	output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
	if err != nil {
		t.Fatalf("cut release: %v\n%s", err, output)
	}

	if got := gitOutput(t, clone, "rev-parse", "v0.1.0^{commit}"); got != head {
		t.Errorf("local tag points at %s, want the released commit %s", got, head)
	}
	// Annotated tags carry the releaser and date; a lightweight tag would drop
	// both from the published release.
	if got := gitOutput(t, clone, "cat-file", "-t", "v0.1.0"); got != "tag" {
		t.Errorf("tag object type = %q, want an annotated tag", got)
	}
	// Production mutation: creating the tag without pushing it leaves the
	// release workflow unstarted, so nothing is ever published.
	if got := gitOutput(t, remote, "rev-parse", "v0.1.0^{commit}"); got != head {
		t.Errorf("remote tag points at %s, want the released commit %s", got, head)
	}
}

func TestCutReleaseRejectsUnsafeVersions(t *testing.T) {
	// Production mutation: accepting a version the formula renderer and the
	// release workflow would later reject publishes a tag that can never build.
	for _, version := range []string{"", "0.1", "01.2.3", "1.2.03", "1.2.3-alpha", "v1.2.3", "1.2.3/../etc"} {
		t.Run(version, func(t *testing.T) {
			clone, remote := newReleaseRepository(t)
			output, err := runCutRelease(clone, version, "--skip-tests")
			if err == nil {
				t.Fatalf("cut release accepted version %q:\n%s", version, output)
			}
			assertNoTagsPublished(t, clone, remote)
		})
	}
}

func TestCutReleaseRefusesUncommittedChanges(t *testing.T) {
	clone, remote := newReleaseRepository(t)
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("edited\n"), 0o600); err != nil {
		t.Fatalf("edit working tree: %v", err)
	}

	// Production mutation: tagging a dirty tree names a commit that does not
	// contain the work the releaser is looking at.
	output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
	if err == nil {
		t.Fatalf("cut release tagged a dirty working tree:\n%s", output)
	}
	if !strings.Contains(output, "uncommitted changes") {
		t.Errorf("output = %q, want an uncommitted-changes error", output)
	}
	assertNoTagsPublished(t, clone, remote)
}

func TestCutReleaseRefusesABranchOtherThanTheTrunk(t *testing.T) {
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "checkout", "--quiet", "-b", "feature")

	// Production mutation: releasing from a feature branch ships commits that
	// never merged to the trunk.
	output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
	if err == nil {
		t.Fatalf("cut release tagged a feature branch:\n%s", output)
	}
	if !strings.Contains(output, "cut from main") {
		t.Errorf("output = %q, want a trunk-branch error", output)
	}
	assertNoTagsPublished(t, clone, remote)
}

func TestCutReleaseRefusesATagThatAlreadyExists(t *testing.T) {
	// Production mutation: reusing a tag rewrites which commit a published
	// release points at, so installed checksums stop matching their source.
	for name, publish := range map[string]func(t *testing.T, clone string){
		"locally": func(t *testing.T, clone string) {
			runCommand(t, clone, nil, "git", "tag", "v0.1.0")
		},
		"on the remote": func(t *testing.T, clone string) {
			runCommand(t, clone, nil, "git", "tag", "v0.1.0")
			runCommand(t, clone, nil, "git", "push", "origin", "refs/tags/v0.1.0")
			runCommand(t, clone, nil, "git", "tag", "--delete", "v0.1.0")
		},
	} {
		t.Run(name, func(t *testing.T) {
			clone, _ := newReleaseRepository(t)
			publish(t, clone)

			output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
			if err == nil {
				t.Fatalf("cut release reused an existing tag:\n%s", output)
			}
			if !strings.Contains(output, "already exists") {
				t.Errorf("output = %q, want an existing-tag error", output)
			}
		})
	}
}

func TestCutReleaseRefusesABranchOutOfSyncWithTheRemote(t *testing.T) {
	clone, remote := newReleaseRepository(t)
	// Land a commit on the remote that this clone has not seen, the usual
	// result of another merge landing while a release is being prepared.
	other := filepath.Join(t.TempDir(), "other")
	runCommand(t, "", nil, "git", "clone", "--quiet", remote, other)
	runCommand(t, other, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, other, nil, "git", "config", "user.email", "release-test@example.com")
	if err := os.WriteFile(filepath.Join(other, "later.md"), []byte("later\n"), 0o600); err != nil {
		t.Fatalf("write later commit: %v", err)
	}
	runCommand(t, other, nil, "git", "add", "later.md")
	runCommand(t, other, nil, "git", "commit", "--quiet", "-m", "later work")
	runCommand(t, other, nil, "git", "push", "--quiet", "origin", "main")

	// Production mutation: tagging a stale checkout silently omits work that
	// the trunk already carries.
	output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
	if err == nil {
		t.Fatalf("cut release tagged a stale branch:\n%s", output)
	}
	if !strings.Contains(output, "not in sync") {
		t.Errorf("output = %q, want an out-of-sync error", output)
	}
	assertNoTagsPublished(t, clone, remote)
}

func TestCutReleaseRefusesAVersionThatDoesNotMoveForward(t *testing.T) {
	// Production mutation: publishing a version at or below the latest release
	// leaves Homebrew serving the older archives as the newest ones.
	for _, version := range []string{"0.1.0", "0.0.9"} {
		t.Run(version, func(t *testing.T) {
			clone, remote := newReleaseRepository(t)
			runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.1.0", "-m", "Workbook v0.1.0")
			runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.1.0")

			output, err := runCutRelease(clone, version, "--skip-tests")
			if err == nil {
				t.Fatalf("cut release accepted non-advancing version %s:\n%s", version, output)
			}
			if got := gitOutput(t, remote, "tag", "--list"); got != "v0.1.0" {
				t.Errorf("remote tags = %q, want only the existing release", got)
			}
		})
	}
}

func TestCutReleaseDryRunPublishesNothing(t *testing.T) {
	clone, remote := newReleaseRepository(t)

	output, err := runCutRelease(clone, "0.1.0", "--skip-tests", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, output)
	}

	// Production mutation: a dry run that still tags or pushes releases a
	// version the operator only meant to check.
	assertNoTagsPublished(t, clone, remote)
	if !strings.Contains(output, "Dry run") {
		t.Errorf("output = %q, want the dry run reported", output)
	}
}

func assertNoTagsPublished(t *testing.T, clone, remote string) {
	t.Helper()
	if got := gitOutput(t, clone, "tag", "--list"); got != "" {
		t.Errorf("local tags = %q, want none", got)
	}
	if got := gitOutput(t, remote, "tag", "--list"); got != "" {
		t.Errorf("remote tags = %q, want none", got)
	}
}

// newReleaseRepository builds a clone of a bare remote that carries the release
// scripts, so cut-release.sh runs against a repository shaped like the real one
// without touching it.
func newReleaseRepository(t *testing.T) (string, string) {
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
	for _, name := range []string{"cut-release.sh", "release-version.sh", "resolve-release-version.sh"} {
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
	return clone, remote
}

func runCutRelease(clone string, args ...string) (string, error) {
	command := exec.Command(filepath.Join(clone, "scripts", "cut-release.sh"), args...)
	command.Dir = clone
	output, err := command.CombinedOutput()
	return string(output), err
}
