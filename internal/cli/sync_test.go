package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestRunPushAndFetchJSONAcrossClones(t *testing.T) {
	first, second := cliSyncRepositories(t)
	task := cliCreateTask(t, first, "Shared task")

	code, stdout, stderr := run(t, first, "push", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("push = code %d, stderr %q", code, stderr)
	}
	pushResult := decodeSyncResult(t, stdout, "push")
	assertCLISyncStatus(t, pushResult, task.ID, gitstore.SyncPublished)

	code, stdout, stderr = run(t, second, "fetch", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("fetch = code %d, stderr %q", code, stderr)
	}
	fetchResult := decodeSyncResult(t, stdout, "fetch")
	assertCLISyncStatus(t, fetchResult, task.ID, gitstore.SyncCreated)

	code, stdout, stderr = run(t, second, "show", task.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show fetched task = code %d, stderr %q", code, stderr)
	}
	var fetched core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Title != "Shared task" {
		t.Fatalf("fetched title = %q", fetched.Title)
	}
}

func TestRunSyncFetchesThenPushesJSONAcrossClones(t *testing.T) {
	first, second := cliSyncRepositories(t)
	firstTask := cliCreateTask(t, first, "First synced task")

	code, stdout, stderr := run(t, first, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("first sync = code %d, stderr %q", code, stderr)
	}
	firstResult := decodeSyncRunResult(t, stdout, "sync")
	assertCLISyncStatus(t, firstResult.Push, firstTask.ID, gitstore.SyncPublished)

	secondTask := cliCreateTask(t, second, "Second synced task")
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("second sync = code %d, stderr %q", code, stderr)
	}
	secondResult := decodeSyncRunResult(t, stdout, "sync")
	assertCLISyncStatus(t, secondResult.Fetch, firstTask.ID, gitstore.SyncCreated)
	assertCLISyncStatus(t, secondResult.Push, firstTask.ID, gitstore.SyncUpToDate)
	assertCLISyncStatus(t, secondResult.Push, secondTask.ID, gitstore.SyncPublished)

	code, stdout, stderr = run(t, first, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("third sync = code %d, stderr %q", code, stderr)
	}
	thirdResult := decodeSyncRunResult(t, stdout, "sync")
	assertCLISyncStatus(t, thirdResult.Fetch, secondTask.ID, gitstore.SyncCreated)
}

func TestRunSyncReplaysDivergenceAndPublishesIt(t *testing.T) {
	first, second := cliSyncRepositories(t)
	divergent := cliCreateTask(t, first, "Divergent sync task")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateTitle(t, first, divergent.ID, "Remote branch")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("remote push code = %d; stderr = %q", code, stderr)
	}
	cliUpdatePriority(t, second, divergent.ID, "high")
	unrelated := cliCreateTask(t, second, "Unrelated local task")

	code, stdout, stderr := run(t, second, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("divergent sync code = %d, want 0; stderr = %q", code, stderr)
	}
	document := assertJSONResult(t, stdout, "sync")
	if len(document.Conflict) != 0 {
		t.Fatalf("sync conflict list = %#v, want none", document.Conflict)
	}
	var result gitstore.SyncRunResult
	if err := json.Unmarshal(document.Data, &result); err != nil {
		t.Fatal(err)
	}
	assertCLISyncStatus(t, result.Fetch, divergent.ID, gitstore.SyncReconciled)
	assertCLISyncStatus(t, result.Push, divergent.ID, gitstore.SyncPublished)
	if !remoteHasTaskRef(t, second, unrelated.ID) {
		t.Fatalf("sync did not publish unrelated task %s", unrelated.ID)
	}
}

// The conflict list is the whole non-interactive contract: it is on the result
// envelope, it is a list, and the exit code alone says the caller must act.
func TestRunSyncReportsConflictListAndExitsEight(t *testing.T) {
	first, second := cliSyncRepositories(t)
	conflicting := cliCreateTask(t, first, "Conflicting sync task")
	cliUpdateDescription(t, first, conflicting.ID, "Base text")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateDescription(t, first, conflicting.ID, "Their text")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("remote push code = %d; stderr = %q", code, stderr)
	}
	cliUpdateDescription(t, second, conflicting.ID, "Our text")
	unrelated := cliCreateTask(t, second, "Unrelated local task")

	code, stdout, stderr := run(t, second, "sync", "--json")
	if code != 8 {
		t.Fatalf("conflicting sync code = %d, want 8; stderr = %q", code, stderr)
	}
	document := assertJSONResult(t, stdout, "sync")
	if len(document.Conflict) != 1 {
		t.Fatalf("sync conflict list = %#v, want exactly one entry", document.Conflict)
	}
	conflict := document.Conflict[0]
	if conflict.TaskID != conflicting.ID || conflict.Type != core.ConflictDescription {
		t.Fatalf("conflict = %#v, want a description conflict for %s", conflict, conflicting.ID)
	}
	want := core.DescriptionConflict{Base: "Base text", Ours: "Our text", Theirs: "Their text"}
	if conflict.Description == nil || *conflict.Description != want {
		t.Fatalf("description conflict = %#v, want %#v", conflict.Description, want)
	}
	var result gitstore.SyncRunResult
	if err := json.Unmarshal(document.Data, &result); err != nil {
		t.Fatal(err)
	}
	assertCLISyncStatus(t, result.Fetch, conflicting.ID, gitstore.SyncConflicted)
	assertJSONError(t, stderr, core.CategoryConflict, "")
	if !remoteHasTaskRef(t, second, unrelated.ID) {
		t.Fatalf("one task's conflict stopped unrelated task %s from publishing", unrelated.ID)
	}
}

func TestRunSyncHumanOutputReportsConflictDetail(t *testing.T) {
	first, second := cliSyncRepositories(t)
	conflicting := cliCreateTask(t, first, "Human conflicting sync task")
	cliUpdateDescription(t, first, conflicting.ID, "Base text")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateDescription(t, first, conflicting.ID, "Their text")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("remote push code = %d; stderr = %q", code, stderr)
	}
	cliUpdateDescription(t, second, conflicting.ID, "Our text")

	code, stdout, stderr := run(t, second, "sync")
	if code != 8 {
		t.Fatalf("conflicting human sync code = %d, want 8; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"Conflict:\t" + conflicting.ID + "\tdescription",
		"\tbase:\tBase text\n",
		"\tours:\tOur text\n",
		"\ttheirs:\tTheir text\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human sync stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

// A ref under origin's task namespace that Workbook does not recognize is the
// one poisoning anyone with push access can do by accident. Synchronization
// keeps working for every well-formed task, and every command that talks to
// origin names the stray ref so somebody prunes it.
func TestRunSyncToleratesAndReportsUnrecognizedRemoteTaskRef(t *testing.T) {
	first, second := cliSyncRepositories(t)
	shared := cliCreateTask(t, first, "Shared task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("initial push code = %d; stderr = %q", code, stderr)
	}
	cliGit(t, first, "push", "origin", "HEAD:refs/workbook/tasks/EVIL")

	local := cliCreateTask(t, second, "Local task")
	code, stdout, stderr := run(t, second, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned sync code = %d, want 0; stderr = %q", code, stderr)
	}
	result := decodeSyncRunResult(t, stdout, "sync")
	assertCLISyncStatus(t, result.Fetch, shared.ID, gitstore.SyncCreated)
	assertCLISyncStatus(t, result.Push, local.ID, gitstore.SyncPublished)
	assertCLIIgnoredRef(t, result.Fetch, "refs/workbook/tasks/EVIL")

	code, stdout, stderr = run(t, second, "fetch", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned fetch code = %d, want 0; stderr = %q", code, stderr)
	}
	assertCLIIgnoredRef(t, decodeSyncResult(t, stdout, "fetch"), "refs/workbook/tasks/EVIL")

	code, stdout, stderr = run(t, second, "push", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned push code = %d, want 0; stderr = %q", code, stderr)
	}
	assertCLIIgnoredRef(t, decodeSyncResult(t, stdout, "push"), "refs/workbook/tasks/EVIL")

	// An ordinary mutation synchronizes inline, so it was breaking too.
	code, stdout, stderr = run(t, second, "update", local.ID, "--priority", "high", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned update code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, `"status":"completed"`) {
		t.Fatalf("poisoned update sync report = %q, want a completed synchronization", stdout)
	}

	code, stdout, stderr = run(t, second, "sync")
	if code != 0 || stderr != "" {
		t.Fatalf("human sync code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"Ignored:\trefs/workbook/tasks/EVIL\t",
		"prune with: git push origin --delete <ref>",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human sync stdout = %q, want it to contain %q", stdout, want)
		}
	}

	cliGit(t, second, "push", "origin", "--delete", "refs/workbook/tasks/EVIL")
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pruned sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if pruned := decodeSyncRunResult(t, stdout, "sync"); len(pruned.Fetch.Ignored) != 0 {
		t.Fatalf("ignored refs after pruning = %#v, want none", pruned.Fetch.Ignored)
	}
}

func assertCLIIgnoredRef(t *testing.T, result gitstore.SyncResult, ref string) {
	t.Helper()
	if len(result.Ignored) != 1 {
		t.Fatalf("ignored refs = %#v, want exactly one for %s", result.Ignored, ref)
	}
	if got := result.Ignored[0]; got.Ref != ref || strings.TrimSpace(got.Reason) == "" {
		t.Fatalf("ignored ref = %#v, want %q with a reason", got, ref)
	}
}

// A tip origin holds under a well-formed task name that is not a Workbook
// history is isolated per task: the fetch still runs to completion and advances
// every other ref. The mutation that follows must publish on the back of it,
// because refusing denies every clone publication over one task no command
// touched -- the same repository-wide denial a stray ref name caused, reached
// through the object instead of the name.
func TestRunMutationPublishesWhenAnotherRemoteTaskTipIsMalformed(t *testing.T) {
	first, second := cliSyncRepositories(t)
	shared := cliCreateTask(t, first, "Shared task")
	poisoned := cliCreateTask(t, first, "Poisoned task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("initial push code = %d; stderr = %q", code, stderr)
	}
	tree := cliGitOutput(t, first, "mktree")
	commit := cliGitOutput(t, first, "commit-tree", tree, "-m", "not a Workbook task")
	cliGit(t, first, "push", "--force", "origin", commit+":refs/workbook/tasks/"+poisoned.ID)

	code, stdout, stderr := run(t, second, "create", "After poison", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned create code = %d, want 0; stderr = %q", code, stderr)
	}
	document := assertJSONResult(t, stdout, "create")
	var created core.Task
	if err := json.Unmarshal(document.Data, &created); err != nil {
		t.Fatal(err)
	}
	report := decodeMutationSync(t, stdout)
	if report.Status != syncStatusCompleted || report.Fetch == nil || report.Push == nil {
		t.Fatalf("sync report = %#v, want a %q inline synchronization", report, syncStatusCompleted)
	}
	assertCLISyncStatus(t, *report.Fetch, shared.ID, gitstore.SyncCreated)
	assertCLISyncStatus(t, *report.Fetch, poisoned.ID, gitstore.SyncInvalid)
	if report.Push.Status != gitstore.SyncPublished {
		t.Fatalf("push phase = %#v, want %q", report.Push, gitstore.SyncPublished)
	}
	if !remoteHasTaskRef(t, second, created.ID) {
		t.Fatalf("one malformed remote tip left task %s unpublished; output = %q", created.ID, stdout)
	}

	// Publication succeeding is not a reason to go quiet: the caller is told
	// which refs origin holds that this fetch could not validate, because
	// nothing else in a mutation's output names them.
	if len(document.Warnings) != 1 ||
		document.Warnings[0].Code != core.WarningAutoSync ||
		!strings.Contains(document.Warnings[0].Message, "failed validation") {
		t.Fatalf("warnings = %#v, want one %s naming the refs that failed validation",
			document.Warnings, core.WarningAutoSync)
	}

	// The explicit command reports the same repository state the same way. It
	// still exits nonzero over the ref it could not validate, but it has
	// nothing left to publish, which is what the inline gate had stopped being
	// able to agree with.
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 7 {
		t.Fatalf("poisoned sync code = %d, want 7; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryCorruptData, "")
	assertCLISyncStatus(t, decodeSyncRunResult(t, stdout, "sync").Push, created.ID, gitstore.SyncUpToDate)
}

// mutationSyncReport decodes only the sync members these tests assert on, so an
// unrelated addition to the envelope does not break them.
type mutationSyncReport struct {
	Status string                   `json:"status"`
	Detail string                   `json:"detail"`
	Fetch  *gitstore.SyncResult     `json:"fetch"`
	Push   *gitstore.SyncTaskResult `json:"push"`
}

func decodeMutationSync(t *testing.T, output string) mutationSyncReport {
	t.Helper()
	var envelope struct {
		Sync *mutationSyncReport `json:"sync"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode sync envelope: %v; output = %q", err, output)
	}
	if envelope.Sync == nil {
		t.Fatalf("result carried no sync member; output = %q", output)
	}
	return *envelope.Sync
}

func TestRunSyncJSONReportsFailedFetchWhenOriginIsMissing(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "sync", "--json")
	if code != 1 {
		t.Fatalf("missing-origin sync code = %d, want 1; stderr = %q", code, stderr)
	}
	result := decodeSyncRunResult(t, stdout, "sync")
	if result.Fetch.Status != gitstore.SyncPhaseFailed {
		t.Fatalf("missing-origin fetch status = %q, want %q", result.Fetch.Status, gitstore.SyncPhaseFailed)
	}
	if !strings.Contains(result.Fetch.Detail, "fetch failed before completion") {
		t.Fatalf("missing-origin fetch detail = %q, want failed detail", result.Fetch.Detail)
	}
	if result.Push.Status != gitstore.SyncPhaseSkipped {
		t.Fatalf("missing-origin push status = %q, want %q", result.Push.Status, gitstore.SyncPhaseSkipped)
	}
	assertJSONError(t, stderr, core.CategoryOperational, "")
}

func TestRunPushReportsPartialRejectionAndNonzeroJSONError(t *testing.T) {
	first, second := cliSyncRepositories(t)
	conflicting := cliCreateTask(t, first, "Conflicting task")
	unrelated := cliCreateTask(t, first, "Unrelated task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("initial push code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateTitle(t, second, conflicting.ID, "Remote update")
	if code, _, stderr := run(t, second, "push"); code != 0 {
		t.Fatalf("remote update push code = %d; stderr = %q", code, stderr)
	}
	cliUpdateTitle(t, first, conflicting.ID, "Rejected local update")
	cliUpdateTitle(t, first, unrelated.ID, "Accepted unrelated update")

	code, stdout, stderr := run(t, first, "push", "--json")
	if code != 1 {
		t.Fatalf("partial push code = %d, want 1; stderr = %q", code, stderr)
	}
	result := decodeSyncResult(t, stdout, "push")
	assertCLISyncStatus(t, result, conflicting.ID, gitstore.SyncRejected)
	assertCLISyncStatus(t, result, unrelated.ID, gitstore.SyncPublished)
	assertJSONError(t, stderr, core.CategoryOperational, "1 task ref(s) were rejected by origin")
}

func TestRunHooksInstallIsIdempotentAndRefusesUnmanagedHook(t *testing.T) {
	first, _ := cliSyncRepositories(t)

	code, stdout, stderr := run(t, first, "hooks", "install", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("hooks install = code %d, stderr %q", code, stderr)
	}
	var installed gitstore.HookInstallResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "hooks install").Data, &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Status != gitstore.HookInstalled {
		t.Fatalf("hook status = %q, want installed", installed.Status)
	}

	code, stdout, stderr = run(t, first, "hooks", "install", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("second hooks install = code %d, stderr %q", code, stderr)
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "hooks install").Data, &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Status != gitstore.HookUnchanged {
		t.Fatalf("second hook status = %q, want unchanged", installed.Status)
	}

	if err := os.WriteFile(installed.Path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run(t, first, "hooks", "install", "--json")
	if code != 1 || stdout != "" {
		t.Fatalf("unmanaged hook install = code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryOperational, "")
	if !strings.Contains(stderr, "workbook push") {
		t.Fatalf("unmanaged hook error lacks chaining guidance: %q", stderr)
	}
}

func TestRunSyncCommandsRejectUnexpectedArguments(t *testing.T) {
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{"fetch", "extra"},
		{"push", "extra"},
		{"sync", "extra"},
		{"hooks"},
		{"hooks", "unknown"},
		{"hooks", "install", "extra"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "Usage: workbook") {
			t.Errorf("run(%q) = code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func cliSyncRepositories(t *testing.T) (string, string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	// Background auto-gc spawned by receive-pack can outlive the test and race
	// t.TempDir cleanup with "directory not empty" on slow runners.
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	cliGit(t, bare, "config", "maintenance.auto", "false")

	seed := testrepo.New(t)
	cliGit(t, seed, "branch", "-M", "main")
	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("setup code = %d; stderr = %q", code, stderr)
	}
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, seed, "remote", "add", "origin", bare)
	cliGit(t, seed, "push", "--quiet", "-u", "origin", "main")
	cliGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return cliClone(t, bare), cliClone(t, bare)
}

func cliClone(t *testing.T, bare string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clone")
	cliGit(t, t.TempDir(), "clone", "--quiet", bare, path)
	cliGit(t, path, "config", "user.name", "Workbook Test")
	cliGit(t, path, "config", "user.email", "workbook@example.test")
	return path
}

func cliCreateTask(t *testing.T, repository, title string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func cliUpdateTitle(t *testing.T, repository, taskID, title string) {
	t.Helper()
	if code, _, stderr := run(t, repository, "update", taskID, "--title", title, "--no-sync"); code != 0 {
		t.Fatalf("update %s = code %d; stderr = %q", taskID, code, stderr)
	}
}

func cliUpdateDescription(t *testing.T, repository, taskID, description string) {
	t.Helper()
	if code, _, stderr := run(t, repository, "update", taskID, "--description", description, "--no-sync"); code != 0 {
		t.Fatalf("update %s = code %d; stderr = %q", taskID, code, stderr)
	}
}

func cliUpdatePriority(t *testing.T, repository, taskID, priority string) {
	t.Helper()
	if code, _, stderr := run(t, repository, "update", taskID, "--priority", priority, "--no-sync"); code != 0 {
		t.Fatalf("update %s = code %d; stderr = %q", taskID, code, stderr)
	}
}

func decodeSyncResult(t *testing.T, output, command string) gitstore.SyncResult {
	t.Helper()
	var result gitstore.SyncResult
	if err := json.Unmarshal(assertJSONResult(t, output, command).Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeSyncRunResult(t *testing.T, output, command string) gitstore.SyncRunResult {
	t.Helper()
	var result gitstore.SyncRunResult
	if err := json.Unmarshal(assertJSONResult(t, output, command).Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertCLISyncStatus(t *testing.T, result gitstore.SyncResult, taskID string, want gitstore.SyncStatus) {
	t.Helper()
	for _, item := range result.Tasks {
		if item.TaskID == taskID {
			if item.Status != want {
				t.Fatalf("task %s status = %q, want %q", taskID, item.Status, want)
			}
			return
		}
	}
	t.Fatalf("result missing task %s: %#v", taskID, result)
}

func remoteHasTaskRef(t *testing.T, repository, taskID string) bool {
	t.Helper()
	command := exec.Command("git", "-C", repository, "ls-remote", "--refs", "origin", "refs/workbook/tasks/"+taskID)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote task ref: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output)) != ""
}

func cliGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func cliGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
