package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
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
	var refused []int
	for index, outcome := range created {
		if outcome.code != 0 {
			assertRefusedUnderContention(t, outcome, "concurrent create "+taskTitle(index))
			refused = append(refused, index)
			continue
		}
		task := decodeMutationTask(t, outcome.stdout, "create")
		if ids[task.ID] {
			t.Fatalf("two concurrent creates produced the same task ID %s", task.ID)
		}
		ids[task.ID] = true
	}
	// A refused create must have recorded nothing. CreateMutation reads the
	// projection to rank the new task before it writes any Git object, so a
	// refusal on that read leaves the repository with exactly the refs the
	// accepted creates wrote. Retrying a create that had half-written would
	// duplicate a task instead, which is why this is checked before retrying.
	if got := taskRefCount(t, repository); got != len(ids) {
		t.Fatalf("%d task refs exist after %d accepted and %d refused creates, want %d",
			got, len(ids), len(refused), len(ids))
	}
	// Retrying is what the refusal told the caller to do, so it has to work.
	for _, index := range refused {
		task := decodeMutationTask(t, mustRunBinary(t, binary, repository, "create", taskTitle(index), "--no-sync", "--json"), "create")
		if ids[task.ID] {
			t.Fatalf("retried create produced the existing task ID %s", task.ID)
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

	// Then one task and one process per status a task can be moved to: the ref
	// compare-and-swap is the only thing keeping two of them from writing over
	// each other.
	contested := decodeMutationTask(t, mustRunBinary(t, binary, repository, "create", "Contested by processes", "--no-sync", "--json"), "create")
	// Every status differs from every other and from the one a new task starts
	// in, so a refusal is always a lost race rather than a no-op update.
	statuses := []core.Status{
		core.StatusReady, core.StatusInProgress, core.StatusInReview, core.StatusDone,
	}
	contended := runConcurrently(t, len(statuses), func(index int) (int, string, string) {
		return runBinary(t, binary, repository, "update", contested.ID, "--status", string(statuses[index]), "--no-sync", "--json")
	})
	accepted := 0
	for index, outcome := range contended {
		if outcome.code == 0 {
			accepted++
			continue
		}
		// Losing the race is allowed; losing an update is not. A refusal has to
		// be one of the retryable answers, and it has to have written nothing,
		// which the commit count below proves.
		assertRefusedUnderContention(t, outcome, "contended update "+string(statuses[index]))
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

// Project identity is published with a ref transaction whose create verb is
// exactly-once, and goroutines inside one process share the Repository that
// memoizes the result. Only separate processes exercise the real race: several
// binaries bootstrapping the same repository at the same moment, each willing to
// mint, must converge on exactly one project.
func TestBuiltBinariesBootstrapOneRepositoryConcurrently(t *testing.T) {
	binary := buildWorkbookBinary(t)
	repository := testrepo.New(t)

	const bootstrappers = 4
	outcomes := runConcurrently(t, bootstrappers, func(int) (int, string, string) {
		return runBinary(t, binary, repository, "setup", "--no-sync", "--json")
	})

	minted := 0
	var projectID string
	for index, outcome := range outcomes {
		if outcome.code != 0 {
			t.Fatalf("concurrent setup %d exited %d; stdout = %q, stderr = %q",
				index, outcome.code, outcome.stdout, outcome.stderr)
		}
		var result struct {
			ProjectID string `json:"projectId"`
			Identity  struct {
				Source    string `json:"source"`
				Minted    bool   `json:"minted"`
				Published bool   `json:"published"`
			} `json:"identity"`
		}
		if err := json.Unmarshal(assertJSONResult(t, outcome.stdout, "setup").Data, &result); err != nil {
			t.Fatalf("decode concurrent setup %d: %v; stdout = %q", index, err, outcome.stdout)
		}
		if projectID == "" {
			projectID = result.ProjectID
		} else if result.ProjectID != projectID {
			t.Fatalf("concurrent setup %d joined project %q, want the single project %q",
				index, result.ProjectID, projectID)
		}
		if result.Identity.Minted {
			minted++
		}
	}
	if minted != 1 {
		t.Fatalf("%d of %d concurrent bootstraps minted a project, want exactly one", minted, bootstrappers)
	}

	// One ref, one document, and every advisory record agreeing with it.
	refs := gitOutput(t, repository, "for-each-ref", "--format=%(refname)", "refs/workbook/project")
	if refs != "refs/workbook/project" {
		t.Fatalf("identity refs = %q, want exactly refs/workbook/project", refs)
	}
	document := gitOutput(t, repository, "cat-file", "blob", "refs/workbook/project:project.json")
	if !strings.Contains(document, projectID) {
		t.Fatalf("identity document = %q, want project %s", document, projectID)
	}
	for _, path := range []string{
		filepath.Join(repository, ".workbook", "config.json"),
		filepath.Join(repository, ".git", "workbook", "project.json"),
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(contents), projectID) {
			t.Fatalf("%s = %q, want project %s", path, contents, projectID)
		}
	}
	if code, stdout, stderr := runBinary(t, binary, repository, "list", "--json"); code != 0 {
		t.Fatalf("list after concurrent bootstrap exited %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

type processOutcome struct {
	code   int
	stdout string
	stderr string
}

// projectionContentionMessages are the four ways the shared SQLite projection
// reports that another process moved task refs, or replaced the cache file,
// while this one was using it: the rebuild that lost twice
// (internal/projection/store.go:177), the recovery that could not activate a
// cache afterwards (:391), the incremental refresh that kept finding the
// cache changed underneath it (:142), and the write redo that kept finding
// the cache file replaced underneath its handle (:446).
var projectionContentionMessages = []string{
	"task refs changed during projection rebuild",
	"activate projection cache after rebuild",
	"refresh projection cache after a concurrent update",
	"redo a projection write after the cache was replaced",
}

// assertRefusedUnderContention accepts the ways a process is allowed to lose
// this race and rejects every other failure. All of them leave the repository
// untouched and all of them are answered by running the command again:
//
//   - exit 6, stale-write: the task ref moved between this process's read and
//     its compare-and-swap. This is the designed answer.
//   - exit 1, operational, with one of projectionContentionMessages: a mutation
//     could not read the disposable projection it ranks against, because enough
//     other processes were writing refs. This is a rough edge rather than a
//     designed answer. The category blames the environment for something that
//     is neither the environment's nor the caller's fault; the advice names
//     `workbook rebuild` when the command that failed was `create`; and the
//     four separate wordings are one phenomenon. It is pinned here rather than
//     tolerated silently, so that smoothing it has to change this test, and a
//     fifth wording fails rather than passing quietly.
func assertRefusedUnderContention(t *testing.T, outcome processOutcome, what string) {
	t.Helper()
	if outcome.stdout != "" {
		t.Fatalf("%s failed but wrote to stdout: %q", what, outcome.stdout)
	}
	switch outcome.code {
	case 6:
		assertJSONError(t, outcome.stderr, core.CategoryStaleWrite, "")
	case 1:
		assertJSONError(t, outcome.stderr, core.CategoryOperational, "")
		if !containsAny(outcome.stderr, projectionContentionMessages) {
			t.Fatalf("%s failed operationally for a reason that is not projection contention: %q",
				what, outcome.stderr)
		}
	default:
		t.Fatalf("%s exited %d, want 0, 6, or 1 for projection contention; stderr = %q",
			what, outcome.code, outcome.stderr)
	}
}

func containsAny(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

// taskRefCount counts the task refs actually present, which is what a mutation
// either did or did not write, independently of the disposable projection.
func taskRefCount(t *testing.T, repository string) int {
	t.Helper()
	listing := gitOutput(t, repository, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/")
	if listing == "" {
		return 0
	}
	return len(strings.Split(listing, "\n"))
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
	// Not os.Environ(): TestMain has already pointed HOME at a temporary
	// directory, and the module and build caches would move with it. See
	// toolchainEnvironment.
	if len(toolchainEnvironment) == 0 {
		t.Fatal("toolchainEnvironment is empty; TestMain must record it before replacing HOME")
	}
	command.Env = toolchainEnvironment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/workbook: %v\n%s", err, output)
	}
	return binary
}
