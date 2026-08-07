package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// AGENTS.md expects multiple worktrees and multiple processes. Worktrees are
// covered; multiple OS processes were not. Every other concurrency test runs
// goroutines inside one process, which shares this program's memory, its Git
// command construction, and — decisively — its SQLite connections. The shared
// projection at <git-common-dir>/workbook/cache.sqlite, the compare-and-swap on
// each task ref, and the config guard are all cross-process contracts that a
// single-process test cannot exercise. A multi-agent beta creates exactly this.
func TestBuiltBinariesMutateOneRepositoryConcurrently(t *testing.T) {
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)

	// Distinct tasks first: six processes writing six new refs through one
	// projection and one configuration guard.
	const writers = 6
	created := runConcurrently(t, writers, func(index int) (int, string, string) {
		return runBinary(t, binary, repository, "create", taskTitle(index), "--no-sync", "--json")
	})
	ids := make(map[string]bool, writers)
	for index, outcome := range created {
		if outcome.code != 0 {
			t.Fatalf("concurrent create %d exited %d; stdout = %q, stderr = %q", index, outcome.code, outcome.stdout, outcome.stderr)
		}
		task := decodeMutationTask(t, outcome.stdout, "create")
		if ids[task.ID] {
			t.Fatalf("two concurrent creates produced the same task ID %s", task.ID)
		}
		ids[task.ID] = true
	}
	listed := listedTaskIDs(t, repository)
	if len(listed) != writers {
		t.Fatalf("projection lists %d tasks after %d concurrent creates: %v", len(listed), writers, listed)
	}
	for _, id := range listed {
		if !ids[id] {
			t.Fatalf("projection lists task %s that no create reported", id)
		}
	}

	// Then one task, six processes: the ref compare-and-swap is the only thing
	// keeping two of them from writing over each other.
	contested := decodeMutationTask(t, mustRunBinary(t, binary, repository, "create", "Contested by processes", "--no-sync", "--json"), "create")
	// Every status differs from every other and from the one a new task starts
	// in, so a refusal is always a lost race rather than a no-op update.
	statuses := []core.Status{
		core.StatusReady, core.StatusInProgress, core.StatusInReview,
		core.StatusBlocked, core.StatusDone,
	}
	contended := runConcurrently(t, len(statuses), func(index int) (int, string, string) {
		return runBinary(t, binary, repository, "update", contested.ID, "--status", string(statuses[index]), "--no-sync", "--json")
	})
	accepted := 0
	for index, outcome := range contended {
		switch outcome.code {
		case 0:
			accepted++
		case 6:
			// stale-write is the documented, retryable answer to losing the
			// race. Any other failure means a lost update or a broken ref.
			assertJSONError(t, outcome.stderr, core.CategoryStaleWrite, "")
		default:
			t.Fatalf("contended update %d exited %d; stdout = %q, stderr = %q", index, outcome.code, outcome.stdout, outcome.stderr)
		}
	}
	if accepted == 0 {
		t.Fatalf("no contended update was accepted: %#v", contended)
	}

	// Whatever the interleaving was, the task's history has to be one
	// single-parent chain and the repository has to still validate.
	head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+contested.ID)
	for _, line := range strings.Split(gitOutput(t, repository, "rev-list", "--parents", head), "\n") {
		if fields := strings.Fields(line); len(fields) > 2 {
			t.Fatalf("contested history has a merge commit: %q", line)
		}
	}
	if got, want := len(strings.Split(gitOutput(t, repository, "rev-list", head), "\n")), accepted+1; got != want {
		t.Fatalf("contested history has %d commits, want %d for one create and %d accepted updates", got, want, accepted)
	}
	if code, stdout, stderr := runBinary(t, binary, repository, "validate", "--full", "--json"); code != 0 {
		t.Fatalf("validate --full exited %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}

	// And the disposable projection has to still be reconstructible from those
	// refs into exactly the state the writers left behind.
	before := listedTaskIDs(t, repository)
	if code, stdout, stderr := runBinary(t, binary, repository, "rebuild", "--json"); code != 0 {
		t.Fatalf("rebuild exited %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if after := listedTaskIDs(t, repository); !sameIDs(after, before) {
		t.Fatalf("rebuilt projection = %v, want %v", after, before)
	}
}

type processOutcome struct {
	code   int
	stdout string
	stderr string
}

func runConcurrently(t *testing.T, count int, invoke func(index int) (int, string, string)) []processOutcome {
	t.Helper()
	outcomes := make([]processOutcome, count)
	// A shared start gate, so the processes overlap instead of queueing behind
	// each other's startup cost.
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			code, stdout, stderr := invoke(index)
			outcomes[index] = processOutcome{code: code, stdout: stdout, stderr: stderr}
		}()
	}
	close(start)
	group.Wait()
	return outcomes
}

func taskTitle(index int) string {
	return "Concurrent process task " + string(rune('A'+index))
}

func runBinary(t *testing.T, binary, repository string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = repository
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case asExitError(err, &exit):
		return exit.ExitCode(), stdout.String(), stderr.String()
	default:
		t.Fatalf("run %v: %v", args, err)
		return -1, "", ""
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

func mustRunBinary(t *testing.T, binary, repository string, args ...string) string {
	t.Helper()
	code, stdout, stderr := runBinary(t, binary, repository, args...)
	if code != 0 {
		t.Fatalf("%v exited %d; stdout = %q, stderr = %q", args, code, stdout, stderr)
	}
	return stdout
}

// buildWorkbookBinary compiles the real command, because a test that called
// Run in six goroutines would be the single-process test this one exists to
// stop standing in for.
func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	binary := filepath.Join(t.TempDir(), "workbook")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, "./cmd/workbook")
	command.Dir = root
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/workbook: %v\n%s", err, output)
	}
	return binary
}
