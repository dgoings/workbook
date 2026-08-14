package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
)

// The other half of the writer-format contract, played with a real older build.
//
// Everything else about assignments is tested from inside a build that
// understands them. What nobody inside this build can answer is the question
// the marker exists for: what happens to a clone that has not been upgraded
// when a teammate assigns a task. cli/newerwriter_test.go answers it in
// general, by forging a pack from a generation that does not exist yet; this
// answers it for the generation that now does, using documents this build
// really wrote and a binary really compiled from the source that predates them.
//
// The old build is compiled out of a `git archive` of a revision whose
// SupportedFormatGeneration is still zero, which is what the assertion in
// generationZeroSource checks rather than assumes. Once this work is on the
// default branch there is no such revision among the candidates and the test
// skips saying so — it is evidence for the change that introduces the
// generation, and it retires itself when that change lands.

// generationZeroSource extracts a source tree that folds generation zero, and
// returns its path — or an empty string when no candidate revision qualifies.
func generationZeroSource(t *testing.T) string {
	t.Helper()
	root := repositoryRoot(t)
	for _, revision := range []string{"main", "origin/main"} {
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", revision+"^{commit}").Run() != nil {
			continue
		}
		directory := t.TempDir()
		archive := exec.Command("git", "-C", root, "archive", "--format=tar", revision)
		extract := exec.Command("tar", "-x", "-C", directory)
		pipe, err := archive.StdoutPipe()
		if err != nil {
			t.Fatalf("pipe git archive: %v", err)
		}
		extract.Stdin = pipe
		if err := extract.Start(); err != nil {
			t.Fatalf("start tar: %v", err)
		}
		if err := archive.Run(); err != nil {
			t.Fatalf("git archive %s: %v", revision, err)
		}
		if err := extract.Wait(); err != nil {
			t.Fatalf("extract %s: %v", revision, err)
		}
		operations, err := os.ReadFile(filepath.Join(directory, "internal", "core", "operation.go"))
		if err != nil {
			t.Fatalf("read %s operation.go: %v", revision, err)
		}
		if strings.Contains(string(operations), "const SupportedFormatGeneration = 0") {
			return directory
		}
	}
	return ""
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// buildGenerationZeroBinary compiles the older build, or reports that there is
// nothing left to compile it from.
func buildGenerationZeroBinary(t *testing.T) string {
	t.Helper()
	source := generationZeroSource(t)
	if source == "" {
		t.Skip("no revision in this checkout still folds generation zero; the assignment bump is on the default branch")
	}
	binary := filepath.Join(t.TempDir(), "workbook-generation-zero")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = source
	if len(toolchainEnvironment) == 0 {
		t.Fatal("toolchainEnvironment is empty; TestMain must record it before replacing HOME")
	}
	command.Env = toolchainEnvironment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build the generation-zero binary: %v\n%s", err, output)
	}
	return binary
}

// assignThroughTheService records an assignment in a repository the CLI made.
//
// It goes through core.Service rather than a verb because this PR ships no
// flags: the operations exist, the surface that types them does not yet. What
// reaches Git is exactly what the verb will write.
func assignThroughTheService(t *testing.T, repository, taskID, to string) {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	actor, err := repo.Actor(ctx)
	if err != nil {
		t.Fatalf("Actor() error = %v", err)
	}
	service := core.Service{
		Config: config, Reader: repo, Writer: repo,
		IDs: core.CryptoULIDSource{}, Now: time.Now, Actor: actor,
	}
	if _, err := service.AssignMutation(ctx, taskID, core.AssignInput{To: to}); err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
}

// The same history, audited by the build that wrote it: valid, not
// newer-writer. `validate` folds every chain from its root, so this is the one
// command that proves an assignment history replays rather than merely reads —
// and it is the assertion that would fail first if the fold's removal rule ever
// stopped being a pure function of the history.
func TestThisBuildValidatesAnAssignmentHistory(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Assigned task")
	assignThroughTheService(t, repository, task.ID, "dylan@example.com/impl-1")
	assignThroughTheService(t, repository, task.ID, "sam@example.com")

	code, stdout, stderr := run(t, repository, "validate", "--full", "--json")
	if code != 0 {
		t.Fatalf("validate code = %d, want 0; stderr = %q", code, stderr)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode validate: %v", err)
	}
	if report.Invalid != 0 || report.NewerWriter != 0 {
		t.Fatalf("validate reported %d invalid and %d newer-writer, want none of either",
			report.Invalid, report.NewerWriter)
	}

	// And `show --history` renders the operations rather than skipping them.
	code, stdout, stderr = run(t, repository, "show", task.ID, "--history")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, wanted := range []string{"dylan@example.com/impl-1", "sam@example.com"} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("show --history output does not mention %q:\n%s", wanted, stdout)
		}
	}
}

func TestAGenerationZeroBuildTreatsAnAssignedTaskAsANewerWritersWork(t *testing.T) {
	binary := buildGenerationZeroBinary(t)
	repository := initializedRepository(t)
	assigned := cliCreateTask(t, repository, "Assigned task")
	untouched := cliCreateTask(t, repository, "Ordinary task")
	assignThroughTheService(t, repository, assigned.ID, "dylan@example.com/impl-1")

	// The marker really is on the documents the old build will read.
	head := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+assigned.ID)
	for _, name := range []string{"operation.json", "state.json"} {
		if document := gitOutput(t, repository, "show", head+":"+name); !strings.Contains(document, `"minReader":1`) {
			t.Fatalf("%s carries no generation-one marker: %s", name, document)
		}
	}

	// Reads succeed and serve the task from its checkpoint, with an advisory
	// rather than a failure. This is the promise that keeps an un-upgraded
	// teammate working.
	code, stdout, stderr := runBinary(t, binary, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "list")
	var listed []core.Task
	if err := json.Unmarshal(envelope.Data, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, task := range listed {
		if task.ID != assigned.ID {
			continue
		}
		found = true
		if !task.NewerWriter {
			t.Fatal("the assigned task does not report a newer writer")
		}
		if task.Title != assigned.Title {
			t.Fatalf("title = %q, want the checkpoint's %q", task.Title, assigned.Title)
		}
	}
	if !found {
		t.Fatalf("the assigned task is missing from the old build's list: %q", stdout)
	}
	assertNewerWriterWarning(t, envelope.Warnings, assigned.ID)

	// Mutating it is refused with the upgrade signal, not with corrupt data.
	code, _, stderr = runBinary(t, binary, repository, "update", assigned.ID, "--title", "Renamed", "--no-sync", "--json")
	if code != 9 {
		t.Fatalf("update code = %d, want 9 (newer-writer); stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	// The category alone would let the refusal say anything at all. What an
	// un-upgraded reader actually needs is the instruction: which task, who
	// wrote it, and what to do about it.
	for _, wanted := range []string{assigned.ID, "newer workbook", "upgrade workbook"} {
		if !strings.Contains(stderr, wanted) {
			t.Fatalf("refusal = %q, want it to contain %q", stderr, wanted)
		}
	}
	for _, forbidden := range []string{"corrupt", "damaged", "invalid history"} {
		if strings.Contains(strings.ToLower(stderr), forbidden) {
			t.Fatalf("refusal = %q, want no claim that the repository is %s", stderr, forbidden)
		}
	}

	// And the scope is exactly one task. A generation is declared per operation
	// type, so a task nobody assigned is still fully the old build's to change —
	// which is what makes the marker worth carrying instead of a version bump.
	if code, _, stderr := runBinary(t, binary, repository, "update", untouched.ID, "--title", "Renamed", "--no-sync"); code != 0 {
		t.Fatalf("update of the unassigned task code = %d, want 0; stderr = %q", code, stderr)
	}

	// validate reports the same way: an audit it could not perform, named as an
	// upgrade rather than as damage, and scoped to the one task.
	code, stdout, stderr = runBinary(t, binary, repository, "validate", "--json")
	if code != 9 {
		t.Fatalf("validate code = %d, want 9 (newer-writer); stderr = %q", code, stderr)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode validate: %v", err)
	}
	if report.NewerWriter != 1 || report.Invalid != 0 {
		t.Fatalf("validate reported %d newer-writer and %d invalid, want 1 and 0", report.NewerWriter, report.Invalid)
	}
}

// Synchronization is the property that has to hold hardest, because a clone
// that cannot fetch is a clone that can never receive the upgrade's data.
//
// The hard case is divergence: the un-upgraded clone has its own unpublished
// change to the very task somebody has since assigned, so accepting origin's
// tip would mean replaying a local pack onto a history this build cannot fold.
// It refuses that one task, by name, and everything else still moves — its
// unrelated work publishes, its local commit survives, and running again
// changes nothing rather than compounding.
func TestAGenerationZeroBuildStillSynchronizesAProjectWithAssignments(t *testing.T) {
	binary := buildGenerationZeroBinary(t)
	upgraded, old := cliSyncRepositories(t)

	assigned := cliCreateTask(t, upgraded, "Assigned upstream")
	if code, _, stderr := run(t, upgraded, "sync"); code != 0 {
		t.Fatalf("upgraded sync code = %d; stderr = %q", code, stderr)
	}
	// The old clone knows the task while it is still ordinary, and edits it.
	if code, _, stderr := runBinary(t, binary, old, "sync"); code != 0 {
		t.Fatalf("old clone first sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := runBinary(t, binary, old, "update", assigned.ID, "--title", "Mine locally", "--no-sync"); code != 0 {
		t.Fatalf("old clone update code = %d; stderr = %q", code, stderr)
	}
	localTip := gitOutput(t, old, "rev-parse", "refs/workbook/tasks/"+assigned.ID)
	mine := cliCreateTask(t, old, "Mine")

	// Now the task is assigned upstream, and the old clone tries to catch up.
	assignThroughTheService(t, upgraded, assigned.ID, "dylan@example.com/impl-1")
	cliGit(t, upgraded, "push", "--quiet", "origin", "refs/workbook/tasks/"+assigned.ID)
	remoteTip := gitOutput(t, upgraded, "rev-parse", "refs/workbook/tasks/"+assigned.ID)

	code, _, stderr := runBinary(t, binary, old, "sync", "--json")
	if code != 9 {
		t.Fatalf("old sync code = %d, want 9 (newer-writer); stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	for _, wanted := range []string{"newer workbook", "upgrade workbook"} {
		if !strings.Contains(stderr, wanted) {
			t.Fatalf("sync refusal = %q, want it to contain %q", stderr, wanted)
		}
	}

	// Nothing was lost and nothing wedged: the local commit is still the task's
	// head, origin's tip is tracked, and the unrelated task published.
	if got := gitOutput(t, old, "rev-parse", "refs/workbook/tasks/"+assigned.ID); got != localTip {
		t.Fatalf("assigned task ref = %q, want the preserved local tip %q", got, localTip)
	}
	if got := gitOutput(t, old, "rev-parse", "refs/workbook/remotes/origin/tasks/"+assigned.ID); got != remoteTip {
		t.Fatalf("tracking ref = %q, want origin's tip %q; the fetch must advance", got, remoteTip)
	}
	if got := gitOutput(t, upgraded, "ls-remote", "origin", "refs/workbook/tasks/"+mine.ID); !strings.Contains(got, mine.ID) {
		t.Fatalf("origin does not hold the old clone's own task: %q", got)
	}

	// Running again behaves identically rather than compounding.
	if code, _, _ := runBinary(t, binary, old, "sync", "--json"); code != 9 {
		t.Fatalf("second old sync code = %d, want 9", code)
	}
	if got := gitOutput(t, old, "rev-parse", "refs/workbook/tasks/"+assigned.ID); got != localTip {
		t.Fatalf("assigned task ref moved on the second run: %q, want %q", got, localTip)
	}
}
