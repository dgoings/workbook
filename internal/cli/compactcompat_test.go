package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// Older readers meeting a compacted task, played with real binaries.
//
// A checkpoint declares a min-reader generation above 3, the last one v0.6.1
// folds. Two older readers matter. The first is this tree with the generation
// pinned back to 3 (buildPatchedGenerationBinary): it knows everything this
// build does except that it may read a checkpoint, so it proves the marker's
// own refusal. The second is the v0.6.1 release itself, compiled from its tag,
// which predates compaction entirely: it proves the rollout section's account
// of what an un-upgraded clone does, which is fail loudly and safely rather
// than publish old history over the checkpoint.

// sharedCompactedTask publishes a Done task from first, has second fetch it,
// then compacts and publishes it from first, and returns the task and the
// checkpoint origin holds.
func sharedCompactedTask(t *testing.T) (first, second string, task core.Task, checkpoint string) {
	t.Helper()
	first, second = cliSyncRepositories(t)
	task = publishedDoneTask(t, first, "Compacted for an older reader")
	compactRun(t, second, "sync")
	compactRun(t, first, "compact", "--no-backup")
	checkpoint = remoteTaskRefValue(t, first, task.ID)
	if checkpoint == "" || checkpoint != taskRefs(t, first)[task.ID] {
		t.Fatalf("origin holds %q, want the published checkpoint %s", checkpoint, taskRefs(t, first)[task.ID])
	}
	return first, second, task, checkpoint
}

// A build that folds generation 3 fetches two compacted tasks. The one it has
// no changes to is taken as it stands, and the older build lists and shows it
// but refuses to change it, with the newer-writer message. The one it has an
// unpushed change to cannot be replayed onto a checkpoint it cannot fold, so
// its sync fails asking for an upgrade, and publishes nothing over origin's
// checkpoint.
func TestAGenerationThreeBuildRefusesCompactedTask(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a patched Workbook binary; skipped in -short mode")
	}
	t.Parallel()
	binary := buildPatchedGenerationBinary(t, 3)
	first, second := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Adopted by an older reader")
	edited := publishedDoneTask(t, first, "Edited before the compaction")
	compactRun(t, second, "sync")
	compactRun(t, second, "update", edited.ID, "--description", "Written offline", "--no-sync")
	offline := taskRefs(t, second)[edited.ID]
	compactRun(t, first, "compact", "--no-backup")
	checkpoint := remoteTaskRefValue(t, first, task.ID)
	editedCheckpoint := remoteTaskRefValue(t, first, edited.ID)

	code, stdout, stderr := runBinary(t, binary, second, "sync")
	if code == 0 || !strings.Contains(stdout+stderr, "written by a newer workbook") {
		t.Fatalf("generation-3 sync = code %d, stdout %q, stderr %q; want a failure asking for an upgrade",
			code, stdout, stderr)
	}
	if got := remoteTaskRefValue(t, second, edited.ID); got != editedCheckpoint {
		t.Fatalf("origin holds %s after the generation-3 sync, want the checkpoint %s", got, editedCheckpoint)
	}
	if got := taskRefs(t, second)[edited.ID]; got != offline {
		t.Fatalf("second clone holds %s for the edited task, want its offline change %s kept", got, offline)
	}
	if got := taskRefs(t, second)[task.ID]; got != checkpoint {
		t.Fatalf("second clone holds %s, want the adopted checkpoint %s", got, checkpoint)
	}

	code, stdout, stderr = runBinary(t, binary, second, "list", "--json")
	if code != 0 {
		t.Fatalf("generation-3 list code = %d, want 0; stderr = %q", code, stderr)
	}
	var listed []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, item := range listed {
		found = found || item.ID == task.ID
	}
	if !found {
		t.Fatalf("generation-3 list = %+v, want the compacted task %s", listed, task.ID)
	}
	if code, _, stderr := runBinary(t, binary, second, "show", task.ID, "--json"); code != 0 {
		t.Fatalf("generation-3 show code = %d, want 0; stderr = %q", code, stderr)
	}

	code, _, stderr = runBinary(t, binary, second, "update", task.ID, "--title", "Edited by an older build")
	if code == 0 || !strings.Contains(stderr, "was written by a newer workbook; upgrade workbook") {
		t.Fatalf("generation-3 update = code %d, stderr %q; want the newer-writer refusal", code, stderr)
	}
	if got := taskRefs(t, second)[task.ID]; got != checkpoint {
		t.Fatalf("second clone holds %s after the refused update, want the checkpoint %s", got, checkpoint)
	}
	// The task kept on the old history is refused the same way, rather than
	// pointed at `compact --adopt`, which could not fold the checkpoint either.
	code, _, stderr = runBinary(t, binary, second, "update", edited.ID, "--title", "Edited again", "--no-sync")
	if code != core.ExitCode(core.Errorf(core.CategoryNewerWriter, "probe")) ||
		!strings.Contains(stderr, "written by a newer workbook; upgrade workbook") {
		t.Fatalf("generation-3 update of the kept task = code %d, stderr %q; want the newer-writer refusal", code, stderr)
	}
}

// v0061Binary compiles the v0.6.1 release from its tag, or reports the tag
// missing as a capability this checkout lacks.
func v0061Binary(t *testing.T) string {
	t.Helper()
	root := repositoryRoot(t)
	if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "v0.6.1^{commit}").Run() != nil {
		testenv.MissingCapability(t, "the v0.6.1 tag is not in this checkout; fetch tags to run the older-release interop test")
	}
	source := t.TempDir()
	archive := exec.Command("git", "-C", root, "archive", "--format=tar", "v0.6.1")
	extract := exec.Command("tar", "-x", "-C", source)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe git archive: %v", err)
	}
	extract.Stdin = pipe
	if err := extract.Start(); err != nil {
		t.Fatalf("start tar: %v", err)
	}
	if err := archive.Run(); err != nil {
		t.Fatalf("git archive v0.6.1: %v", err)
	}
	if err := extract.Wait(); err != nil {
		t.Fatalf("extract v0.6.1: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "workbook")
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
		t.Fatalf("build v0.6.1: %v\n%s", err, output)
	}
	return binary
}

// The v0.6.1 release, on a clone that held a task before a teammate compacted
// it, fails safely: its sync exits non-zero naming the generation mismatch, an
// edit it records locally can never be pushed, and with its managed pre-push
// hook installed a code push from that clone fails too. Origin keeps the
// checkpoint throughout.
func TestReleasedV061FailsSafelyOnCompactedTask(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the v0.6.1 release; skipped in -short mode")
	}
	t.Parallel()
	binary := v0061Binary(t)
	_, second, task, checkpoint := sharedCompactedTask(t)

	code, stdout, stderr := runBinary(t, binary, second, "sync")
	if code == 0 || !strings.Contains(stdout+stderr, "different history generations") {
		t.Fatalf("v0.6.1 sync = code %d, stdout %q, stderr %q; want a failure naming the generation mismatch",
			code, stdout, stderr)
	}
	if got := remoteTaskRefValue(t, second, task.ID); got != checkpoint {
		t.Fatalf("origin holds %s after the v0.6.1 sync, want the checkpoint %s", got, checkpoint)
	}

	old := taskRefs(t, second)[task.ID]
	if code, _, stderr := runBinary(t, binary, second, "update", task.ID, "--title", "Edited on v0.6.1", "--no-sync"); code != 0 {
		t.Fatalf("v0.6.1 update --no-sync code = %d, want 0; stderr = %q", code, stderr)
	}
	edited := taskRefs(t, second)[task.ID]
	if edited == old || edited == checkpoint {
		t.Fatalf("local ref after the v0.6.1 edit = %s, want a new commit on the old history %s", edited, old)
	}
	if code, stdout, stderr := runBinary(t, binary, second, "push"); code == 0 {
		t.Fatalf("v0.6.1 push code = 0, want a rejection; stdout = %q; stderr = %q", stdout, stderr)
	}
	if got := remoteTaskRefValue(t, second, task.ID); got != checkpoint {
		t.Fatalf("origin holds %s after the v0.6.1 push, want the checkpoint %s", got, checkpoint)
	}

	// The managed hook runs `workbook push` from PATH, so the old binary is
	// what PATH finds first.
	if code, _, stderr := runBinary(t, binary, second, "hooks", "install"); code != 0 {
		t.Fatalf("v0.6.1 hooks install code = %d, want 0; stderr = %q", code, stderr)
	}
	bin := t.TempDir()
	name := "workbook"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.Symlink(binary, filepath.Join(bin, name)); err != nil {
		t.Fatalf("link the v0.6.1 binary onto PATH: %v", err)
	}
	cliGit(t, second, "commit", "--quiet", "--allow-empty", "-m", "Unrelated code change")
	push := exec.Command("git", "push", "origin", "HEAD")
	push.Dir = second
	push.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("git push of a code commit succeeded with the v0.6.1 hook installed; output:\n%s", output)
	}
	if got := cliGitOutput(t, second, "ls-remote", "origin", "refs/heads/main"); strings.HasPrefix(got, cliGitOutput(t, second, "rev-parse", "HEAD")) {
		t.Fatalf("origin's main = %q, want the code commit refused with the hook", got)
	}
	if got := remoteTaskRefValue(t, second, task.ID); got != checkpoint {
		t.Fatalf("origin holds %s after the code push, want the checkpoint %s", got, checkpoint)
	}
}
