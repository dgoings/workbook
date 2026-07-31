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

func TestRunSyncReportsDivergenceAndDoesNotPush(t *testing.T) {
	first, second := cliSyncRepositories(t)
	conflicting := cliCreateTask(t, first, "Conflicting sync task")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateTitle(t, first, conflicting.ID, "Remote branch")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("remote push code = %d; stderr = %q", code, stderr)
	}
	cliUpdateTitle(t, second, conflicting.ID, "Local branch")
	unrelated := cliCreateTask(t, second, "Unrelated local task")

	code, stdout, stderr := run(t, second, "sync", "--json")
	if code != 6 {
		t.Fatalf("divergent sync code = %d, want 6; stderr = %q", code, stderr)
	}
	result := decodeSyncRunResult(t, stdout, "sync")
	assertCLISyncStatus(t, result.Fetch, conflicting.ID, gitstore.SyncDiverged)
	if result.Push.Status != gitstore.SyncPhaseSkipped {
		t.Fatalf("divergent sync push status = %q, want %q", result.Push.Status, gitstore.SyncPhaseSkipped)
	}
	if !strings.Contains(result.Push.Detail, "push skipped") {
		t.Fatalf("divergent sync push detail = %q, want skipped detail", result.Push.Detail)
	}
	if len(result.Push.Tasks) != 0 {
		t.Fatalf("divergent sync push tasks = %#v, want none", result.Push.Tasks)
	}
	assertJSONError(t, stderr, core.CategoryStaleWrite, "1 divergent task history(s) require reconciliation before sync can push")
	if remoteHasTaskRef(t, second, unrelated.ID) {
		t.Fatalf("sync pushed unrelated task %s after divergence", unrelated.ID)
	}
}

func TestRunSyncHumanOutputReportsSkippedPushAfterDivergence(t *testing.T) {
	first, second := cliSyncRepositories(t)
	conflicting := cliCreateTask(t, first, "Human divergent sync task")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("initial fetch code = %d; stderr = %q", code, stderr)
	}

	cliUpdateTitle(t, first, conflicting.ID, "Remote branch")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("remote push code = %d; stderr = %q", code, stderr)
	}
	cliUpdateTitle(t, second, conflicting.ID, "Local branch")

	code, stdout, stderr := run(t, second, "sync")
	if code != 6 {
		t.Fatalf("divergent human sync code = %d, want 6; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Push:\nSkipped on origin: push skipped because 1 divergent task history(s) require reconciliation before sync can push\n") {
		t.Fatalf("human sync stdout = %q, want skipped push detail", stdout)
	}
	if strings.Contains(stdout, "Push:\nNo task refs on origin.") {
		t.Fatalf("human sync stdout incorrectly reports empty push phase: %q", stdout)
	}
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
