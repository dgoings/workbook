package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestOpenPreservesLeadingAndTrailingWhitespaceInRepositoryPath(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), " repository ")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	gitRun(t, repoDir, "init", "--quiet")

	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := repo.Root, gitReportedPath(t, repoDir, "rev-parse", "--show-toplevel"); got != want {
		t.Fatalf("Open().Root = %q, want byte-preserving path %q", got, want)
	}
	if got, want := repo.CommonGitDir, gitReportedPath(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != want {
		t.Fatalf("Open().CommonGitDir = %q, want %q", got, want)
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

func TestRepositoryCachesProcessStableActor(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	repo.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	first, err := repo.Actor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Git(context.Background(), nil, "config", "user.email", "changed@example.test"); err != nil {
		t.Fatal(err)
	}
	second, err := repo.Actor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != "workbook@example.test" || second != first {
		t.Fatalf("actors = %q, %q", first, second)
	}
	if got := countCommand(commands, "config", "--get", "user.email"); got != 1 {
		t.Fatalf("actor config commands = %d, want 1", got)
	}
}

func TestOpenRepositorySkipsRepeatedIdentityDiscovery(t *testing.T) {
	opened, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	var openedCommands [][]string
	opened.commandObserver = func(args []string) {
		openedCommands = append(openedCommands, append([]string(nil), args...))
	}

	if _, _, err := opened.Init(context.Background(), "WB", fixedIDs()); err != nil {
		t.Fatal(err)
	}
	if got := countCommand(openedCommands, "rev-parse", "--show-toplevel"); got != 0 {
		t.Fatalf("opened repository root discovery commands = %d, want 0", got)
	}
	if got := countCommand(openedCommands, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != 0 {
		t.Fatalf("opened repository common-directory discovery commands = %d, want 0", got)
	}

	constructed := &Repository{
		Root:         opened.Root,
		CommonGitDir: opened.CommonGitDir,
		gitPath:      opened.gitPath,
	}
	var constructedCommands [][]string
	constructed.commandObserver = func(args []string) {
		constructedCommands = append(constructedCommands, append([]string(nil), args...))
	}
	if _, _, err := constructed.Init(context.Background(), "WB", fixedIDs()); err != nil {
		t.Fatal(err)
	}
	if got := countCommand(constructedCommands, "rev-parse", "--show-toplevel"); got != 1 {
		t.Fatalf("constructed repository root discovery commands = %d, want 1", got)
	}
	if got := countCommand(constructedCommands, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != 1 {
		t.Fatalf("constructed repository common-directory discovery commands = %d, want 1", got)
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
	line, err := gitSingleLine(output)
	if err != nil {
		t.Fatalf("gitSingleLine() error = %v", err)
	}
	if got, want := filepath.Clean(line), opened.Root; got != want {
		t.Fatalf("Git() output = %q, want %q", got, want)
	}
}

func TestGitSeparatesStdoutAndStderrAndSetsReplaceProtection(t *testing.T) {
	t.Setenv("WORKBOOK_ENV_SENTINEL", "preserved")
	gitPath := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s:%s\n' "$GIT_NO_REPLACE_OBJECTS" "$WORKBOOK_ENV_SENTINEL"
printf 'warning on stderr\n' >&2
`
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake git) error = %v", err)
	}
	repo := &Repository{Root: t.TempDir(), gitPath: gitPath}

	output, err := repo.Git(context.Background(), nil, "status")
	if err != nil {
		t.Fatalf("Git() error = %v", err)
	}
	if got, want := string(output), "1:preserved\n"; got != want {
		t.Fatalf("Git() stdout = %q, want %q", got, want)
	}
}

func TestGitFailureReportsStderrWithoutContaminatingItWithStdout(t *testing.T) {
	gitPath := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf 'misleading stdout\n'
printf 'fatal stderr detail\n' >&2
exit 9
`
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake git) error = %v", err)
	}
	repo := &Repository{Root: t.TempDir(), gitPath: gitPath}

	_, err := repo.Git(context.Background(), nil, "status")
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("Git() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "fatal stderr detail") {
		t.Fatalf("Git() error = %q, want stderr detail", err)
	}
	if strings.Contains(err.Error(), "misleading stdout") {
		t.Fatalf("Git() error contains stdout: %q", err)
	}
}

func TestGitResultRetainsNonzeroStreamsAndNotifiesObserverOnce(t *testing.T) {
	gitPath := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf 'porcelain stdout\n'
printf 'transport stderr\n' >&2
exit 9
`
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake git) error = %v", err)
	}
	repo := &Repository{Root: t.TempDir(), gitPath: gitPath}
	commands := 0
	repo.commandObserver = func(args []string) {
		commands++
		if got, want := args, []string{"push", "--porcelain", "origin"}; !slices.Equal(got, want) {
			t.Fatalf("observed command = %q, want %q", got, want)
		}
	}

	result := repo.gitWithEnvResult(context.Background(), []string{"WORKBOOK_TEST_TRANSPORT=1"}, nil, "push", "--porcelain", "origin")
	if got, want := string(result.stdout), "porcelain stdout\n"; got != want {
		t.Fatalf("result stdout = %q, want %q", got, want)
	}
	if got, want := string(result.stderr), "transport stderr\n"; got != want {
		t.Fatalf("result stderr = %q, want %q", got, want)
	}
	if result.err == nil {
		t.Fatal("result error = nil, want exit error")
	}
	if commands != 1 {
		t.Fatalf("observed commands = %d, want 1", commands)
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
	line := strings.TrimSuffix(string(output), "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == string(output) {
		t.Fatalf("git %v output has no trailing newline: %q", args, output)
	}
	return filepath.Clean(line)
}

func countCommand(commands [][]string, want ...string) int {
	count := 0
	for _, got := range commands {
		if len(got) != len(want) {
			continue
		}
		matched := true
		for i := range got {
			if got[i] != want[i] {
				matched = false
				break
			}
		}
		if matched {
			count++
		}
	}
	return count
}
