package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCutReleaseTagsAndPushesTheReleaseTag(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A pre-release is cut the same way as a release, by naming it, and the
// script says the tap will be left alone so the releaser is not surprised.
func TestCutReleaseTagsAPreRelease(t *testing.T) {
	t.Parallel()
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "tag", "v0.5.1")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.5.1")
	head := gitOutput(t, clone, "rev-parse", "HEAD")

	output, err := runCutRelease(clone, "0.6.0-rc1", "--skip-tests")
	if err != nil {
		t.Fatalf("cut pre-release: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "v0.6.0-rc1^{commit}"); got != head {
		t.Errorf("remote tag points at %s, want %s", got, head)
	}
	if !strings.Contains(output, "previous release  v0.5.1") {
		t.Errorf("output = %q, want the previous stable release named", output)
	}
	if !strings.Contains(output, "Homebrew tap is left alone") {
		t.Errorf("output = %q, want the tap skip announced", output)
	}
}

// The stable release after an rc ignores it, so the next patch after v0.5.1
// is still v0.5.2 while v0.6.0-rc1 exists.
func TestCutReleaseIgnoresPreReleaseTagsForAStableVersion(t *testing.T) {
	t.Parallel()
	clone, _ := newReleaseRepository(t)
	for _, tag := range []string{"v0.5.1", "v0.6.0-rc1"} {
		runCommand(t, clone, nil, "git", "tag", tag)
		runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/"+tag)
	}

	output, err := runCutRelease(clone, "0.5.2", "--skip-tests", "--dry-run")
	if err != nil {
		t.Fatalf("cut stable after rc: %v\n%s", err, output)
	}
	if !strings.Contains(output, "previous release  v0.5.1") {
		t.Errorf("output = %q, want v0.5.1 as the previous release, not the rc", output)
	}
}

// Production mutation: cutting an rc below the newest tag would publish a
// pre-release that orders before one already out.
func TestCutReleaseRefusesAStalePreRelease(t *testing.T) {
	t.Parallel()
	clone, remote := newReleaseRepository(t)
	for _, tag := range []string{"v0.5.1", "v0.6.0-rc2"} {
		runCommand(t, clone, nil, "git", "tag", tag)
		runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/"+tag)
	}
	tagsBefore := gitOutput(t, remote, "tag", "--list")

	output, err := runCutRelease(clone, "0.6.0-rc1", "--skip-tests")
	if err == nil {
		t.Fatalf("cut accepted a stale rc:\n%s", output)
	}
	if got := gitOutput(t, remote, "tag", "--list"); got != tagsBefore {
		t.Errorf("remote tags changed from %q to %q", tagsBefore, got)
	}
}

// desktop-latest is a rolling tag the desktop publisher force-moves on every
// desktop release, so a clone that fetched it once holds a stale copy as soon
// as the next desktop release ships. That copy has nothing to do with the
// release being cut.
func TestCutReleaseToleratesAMovedRollingTag(t *testing.T) {
	t.Parallel()
	clone, remote := newReleaseRepository(t)
	first := gitOutput(t, clone, "rev-parse", "HEAD")
	runCommand(t, clone, nil, "git", "tag", "desktop-latest")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/desktop-latest")
	commitAndPush(t, clone, "later.md")
	head := gitOutput(t, clone, "rev-parse", "HEAD")
	// Move the remote's desktop-latest the way the publisher does, leaving
	// this clone's copy behind.
	runCommand(t, clone, nil, "git", "push", "--quiet", "--force", "origin", "HEAD:refs/tags/desktop-latest")
	// A clone configured to fetch every tag from origin must still leave the
	// rolling tag out; dropping --no-tags lets this setting pull it back in.
	runCommand(t, clone, nil, "git", "config", "remote.origin.tagOpt", "--tags")

	// Production mutation: fetching every tag fails on the stale rolling tag
	// and stops the cut before anything is tagged.
	output, err := runCutRelease(clone, "0.1.0", "--skip-tests")
	if err != nil {
		t.Fatalf("cut release with a moved desktop-latest: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "v0.1.0^{commit}"); got != head {
		t.Errorf("remote tag points at %s, want the released commit %s", got, head)
	}
	// Leaving the rolling tag alone is the point: the cut neither needs nor
	// owns it, so it must not rewrite the clone's copy or push it back.
	if got := gitOutput(t, clone, "rev-parse", "desktop-latest"); got != first {
		t.Errorf("local desktop-latest = %s, want it left at %s", got, first)
	}
	if got := gitOutput(t, remote, "rev-parse", "desktop-latest"); got != head {
		t.Errorf("remote desktop-latest = %s, want it left at %s", got, head)
	}
}

// A release tag, unlike a rolling one, never moves once published, so a
// remote copy that disagrees with the clone's is refused by name rather than
// overwritten or reported as a failed fetch.
func TestCutReleaseRefusesAMovedReleaseTagByName(t *testing.T) {
	t.Parallel()
	clone, remote := newReleaseRepository(t)
	first := gitOutput(t, clone, "rev-parse", "HEAD")
	for _, tag := range []string{"v0.1.0", "desktop-latest"} {
		runCommand(t, clone, nil, "git", "tag", tag)
		runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/"+tag)
	}
	commitAndPush(t, clone, "later.md")
	for _, tag := range []string{"v0.1.0", "desktop-latest"} {
		runCommand(t, clone, nil, "git", "push", "--quiet", "--force", "origin", "HEAD:refs/tags/"+tag)
	}

	// Production mutation: force-fetching release tags would silently repoint
	// v0.1.0 at a different commit than the one this clone released.
	output, err := runCutRelease(clone, "0.2.0", "--skip-tests")
	if err == nil {
		t.Fatalf("cut release accepted a moved release tag:\n%s", output)
	}
	if !strings.Contains(output, "v0.1.0") {
		t.Errorf("output = %q, want the moved release tag named", output)
	}
	if strings.Contains(output, "desktop-latest") {
		t.Errorf("output = %q, want the rolling tag left out of the refusal", output)
	}
	if got := gitOutput(t, clone, "rev-parse", "v0.1.0"); got != first {
		t.Errorf("local v0.1.0 = %s, want it left at %s", got, first)
	}
	if got := gitOutput(t, remote, "tag", "--list", "v0.2.0"); got != "" {
		t.Errorf("remote carries %q, want v0.2.0 left unpublished", got)
	}
}

// The refusal compares the two copies of a release tag and cannot tell which
// one moved, so when only this clone's copy was re-created it must not send
// the developer looking for a problem on the remote.
func TestCutReleaseRefusesALocallyMovedReleaseTagWithoutBlamingTheRemote(t *testing.T) {
	t.Parallel()
	clone, remote := newReleaseRepository(t)
	first := gitOutput(t, clone, "rev-parse", "HEAD")
	runCommand(t, clone, nil, "git", "tag", "v0.1.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.1.0")
	commitAndPush(t, clone, "later.md")
	runCommand(t, clone, nil, "git", "tag", "--force", "v0.1.0", "HEAD")

	// Production mutation: wording the refusal as a tag that "moved on origin"
	// blames the remote, which here still holds the published commit.
	output, err := runCutRelease(clone, "0.2.0", "--skip-tests")
	if err == nil {
		t.Fatalf("cut release accepted a moved release tag:\n%s", output)
	}
	if !strings.Contains(output, "differ between origin and this clone: v0.1.0") {
		t.Errorf("output = %q, want v0.1.0 named as differing between the copies", output)
	}
	if strings.Contains(output, "moved on origin") {
		t.Errorf("output = %q, want the remote not blamed for a local change", output)
	}
	if got := gitOutput(t, remote, "rev-parse", "v0.1.0^{commit}"); got != first {
		t.Errorf("remote v0.1.0 = %s, want it left at %s", got, first)
	}
}

func commitAndPush(t *testing.T, clone, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(clone, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runCommand(t, clone, nil, "git", "add", name)
	runCommand(t, clone, nil, "git", "commit", "--quiet", "-m", "add "+name)
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main")
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
