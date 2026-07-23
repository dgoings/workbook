package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestOpenFromNestedWorkingTree(t *testing.T) {
	repoDir := testrepo.New(t)
	nestedDir := filepath.Join(repoDir, "a", "deep", "directory")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	before, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() before Open = %v", err)
	}
	repo, err := Open(context.Background(), nestedDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() after Open = %v", err)
	}

	if got, want := repo.Root, gitReportedPath(t, repoDir, "rev-parse", "--show-toplevel"); got != want {
		t.Fatalf("Open().Root = %q, want %q", got, want)
	}
	if got, want := repo.CommonGitDir, gitReportedPath(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != want {
		t.Fatalf("Open().CommonGitDir = %q, want %q", got, want)
	}
	if after != before {
		t.Fatalf("Open() changed process cwd from %q to %q", before, after)
	}
}

func TestOpenOutsideGitIsNotInitialized(t *testing.T) {
	_, err := Open(context.Background(), t.TempDir())
	if got, want := core.CategoryOf(err), core.CategoryNotInitialized; got != want {
		t.Fatalf("Open() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestOpenFromLinkedWorktreeUsesReportedPaths(t *testing.T) {
	repoDir := testrepo.New(t)
	gitRun(t, repoDir, "commit", "--allow-empty", "--quiet", "-m", "initial")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repoDir, "worktree", "add", "--detach", "--quiet", linkedDir, "HEAD")
	t.Cleanup(func() { gitRun(t, repoDir, "worktree", "remove", "--force", linkedDir) })

	repo, err := Open(context.Background(), linkedDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := repo.Root, gitReportedPath(t, linkedDir, "rev-parse", "--show-toplevel"); got != want {
		t.Fatalf("Open().Root = %q, want %q", got, want)
	}
	if got, want := repo.CommonGitDir, gitReportedPath(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != want {
		t.Fatalf("Open().CommonGitDir = %q, want %q", got, want)
	}
}

func TestActorReturnsRepositoryEmail(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	actor, err := repo.Actor(context.Background())
	if err != nil {
		t.Fatalf("Actor() error = %v", err)
	}
	if got, want := actor, "workbook@example.test"; got != want {
		t.Fatalf("Actor() = %q, want %q", got, want)
	}
}

func TestGitUsesResolvedPathForValidConstructedRepository(t *testing.T) {
	opened, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	repo := &Repository{Root: opened.Root, CommonGitDir: opened.CommonGitDir}

	output, err := repo.Git(context.Background(), nil, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("Git() error = %v", err)
	}
	if got, want := filepath.Clean(strings.TrimSpace(string(output))), opened.Root; got != want {
		t.Fatalf("Git() output = %q, want %q", got, want)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitReportedPath(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return filepath.Clean(strings.TrimSpace(string(output)))
}
