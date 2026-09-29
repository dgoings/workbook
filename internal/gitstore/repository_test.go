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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	_, err := Open(context.Background(), t.TempDir())
	if got, want := core.CategoryOf(err), core.CategoryNotInitialized; got != want {
		t.Fatalf("Open() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "cannot find Git repository") {
		t.Fatalf("Open() error = %q, want it to contain %q", err, "cannot find Git repository")
	}
}

// gitRevParseArgs is the single combined rev-parse invocation Open and
// verifyIdentity make to learn both the repository root and the common Git
// directory in one process.
var gitRevParseArgs = []string{"rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir"}

// installGitInvocationCounter puts a shim named "git" ahead of the real one
// on PATH that appends one argument per line to a counter file, followed by a
// blank line marking the end of the invocation, then execs the real git it
// resolved before installing itself, so the command still succeeds. One
// argument per line, rather than a single space-joined line, survives an
// argument (such as a -C directory) that itself contains a space — a plain
// space-joined line would let strings.Fields split that argument in two. It
// returns a function that reads back the recorded invocations.
func installGitInvocationCounter(t *testing.T) func() [][]string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git) error = %v", err)
	}
	shimDir := t.TempDir()
	counterFile := filepath.Join(shimDir, "invocations.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + shellQuote(counterFile) + "\nprintf '\\n' >> " + shellQuote(counterFile) + "\nexec " + shellQuote(realGit) + " \"$@\"\n"
	shimPath := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(shim git) error = %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() [][]string {
		t.Helper()
		data, err := os.ReadFile(counterFile)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("ReadFile(counter) error = %v", err)
		}
		if len(data) == 0 {
			return nil
		}
		var invocations [][]string
		for _, block := range strings.Split(string(data), "\n\n") {
			if block == "" {
				continue
			}
			invocations = append(invocations, strings.Split(block, "\n"))
		}
		return invocations
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gitRevParseArgsOf strips the leading "-C <directory>" every invocation
// carries (runGitWithEnvResult always sets it) so an observed command line
// can be compared against the git arguments alone.
func gitRevParseArgsOf(invocation []string) []string {
	if len(invocation) >= 2 && invocation[0] == "-C" {
		return invocation[2:]
	}
	return invocation
}

func TestOpenFromLinkedWorktreeUsesReportedPaths(t *testing.T) {
	t.Parallel()
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

func TestOpenSpawnsOneGitProcessForRootAndCommonDir(t *testing.T) {
	repoDir := testrepo.New(t)
	wantRoot := gitReportedPath(t, repoDir, "rev-parse", "--show-toplevel")
	wantCommonDir := gitReportedPath(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")

	invocations := installGitInvocationCounter(t)

	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got := invocations()
	if len(got) != 1 {
		t.Fatalf("git invocations = %d, want 1; log = %v", len(got), got)
	}
	if !slices.Equal(gitRevParseArgsOf(got[0]), gitRevParseArgs) {
		t.Fatalf("git invocation args = %q, want %q with a -C prefix", got[0], gitRevParseArgs)
	}
	if repo.Root != wantRoot {
		t.Fatalf("Open().Root = %q, want %q", repo.Root, wantRoot)
	}
	if repo.CommonGitDir != wantCommonDir {
		t.Fatalf("Open().CommonGitDir = %q, want %q", repo.CommonGitDir, wantCommonDir)
	}
}

func TestOpenFromLinkedWorktreeSpawnsOneGitProcess(t *testing.T) {
	repoDir := testrepo.New(t)
	gitRun(t, repoDir, "commit", "--allow-empty", "--quiet", "-m", "initial")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repoDir, "worktree", "add", "--detach", "--quiet", linkedDir, "HEAD")
	t.Cleanup(func() { gitRun(t, repoDir, "worktree", "remove", "--force", linkedDir) })

	wantRoot := gitReportedPath(t, linkedDir, "rev-parse", "--show-toplevel")
	wantCommonDir := gitReportedPath(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if got, want := wantCommonDir, gitReportedPath(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != want {
		t.Fatalf("linked worktree common dir = %q, want the main repository's .git %q", got, want)
	}

	invocations := installGitInvocationCounter(t)

	repo, err := Open(context.Background(), linkedDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got := invocations()
	if len(got) != 1 {
		t.Fatalf("git invocations = %d, want 1; log = %v", len(got), got)
	}
	if !slices.Equal(gitRevParseArgsOf(got[0]), gitRevParseArgs) {
		t.Fatalf("git invocation args = %q, want %q with a -C prefix", got[0], gitRevParseArgs)
	}
	if repo.Root != wantRoot {
		t.Fatalf("Open().Root = %q, want %q", repo.Root, wantRoot)
	}
	if repo.CommonGitDir != wantCommonDir {
		t.Fatalf("Open().CommonGitDir = %q, want %q", repo.CommonGitDir, wantCommonDir)
	}
}

func TestActorReturnsRepositoryEmail(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if got := countCommand(openedCommands, gitRevParseArgs...); got != 0 {
		t.Fatalf("opened repository root/common-directory discovery commands = %d, want 0", got)
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
	if got := countCommand(constructedCommands, gitRevParseArgs...); got != 1 {
		t.Fatalf("constructed repository root/common-directory discovery commands = %d, want 1", got)
	}
}

func TestGitUsesResolvedPathForValidConstructedRepository(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
