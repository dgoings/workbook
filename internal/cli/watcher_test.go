package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/syncloop/watchertest"
)

func TestSyncWatchRejectsInvalidInvocations(t *testing.T) {
	_, second := cliSyncRepositories(t)
	for _, args := range [][]string{
		{"sync", "--interval", "5s"},
		{"sync", "--watch", "--status"},
		{"sync", "--watch", "--interval", "not-a-duration"},
		{"sync", "--watch", "--interval", "0s"},
	} {
		code, stdout, stderr := run(t, second, args...)
		if code != 2 || stdout != "" {
			t.Errorf("run(%q) = code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestSyncStatusReportsNoWatcher(t *testing.T) {
	_, second := cliSyncRepositories(t)

	code, stdout, stderr := run(t, second, "sync", "--status", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("sync --status = code %d, stderr %q", code, stderr)
	}
	var result watcherStatusResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Running {
		t.Fatalf("status reported a running watcher: %#v", result)
	}

	code, stdout, stderr = run(t, second, "sync", "--status")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "No sync watcher is running") {
		t.Fatalf("sync --status = code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestSyncStatusReportsALiveWatcher(t *testing.T) {
	_, second := cliSyncRepositories(t)
	startCLIWatcher(t, second, "1h")

	code, stdout, stderr := run(t, second, "sync", "--status", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("sync --status = code %d, stderr %q", code, stderr)
	}
	var result watcherStatusResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Running || result.PID == 0 || !result.LastSyncOK {
		t.Fatalf("status of a live watcher = %#v, want running with a successful sync", result)
	}
}

// The whole point of a watcher: another clone's work arrives without anyone
// running a command here.
func TestSyncWatchObservesAnotherClonePushWithNoLocalCommand(t *testing.T) {
	first, second := cliSyncRepositories(t)
	task := cliCreateTask(t, first, "Watched task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("push = code %d, stderr %q", code, stderr)
	}

	startCLIWatcher(t, second, "100ms")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if localHasTaskRef(t, second, task.ID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watcher never fetched %s into the watching clone", task.ID)
}

func TestSecondSyncWatchRefusesToStart(t *testing.T) {
	_, second := cliSyncRepositories(t)
	startCLIWatcher(t, second, "1h")

	code, _, stderr := run(t, second, "sync", "--watch", "--interval", "1h")
	if code != 1 || !strings.Contains(stderr, "already running") {
		t.Fatalf("second watcher = code %d, stderr %q, want an operational refusal", code, stderr)
	}
}

// A mutation with a live watcher does the local write and hands publication
// over. The watcher's interval is an hour, so origin only holds the tip if the
// nudge delivered it rather than a scheduled tick.
func TestMutationDefersToALiveWatcher(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Deferred task")
	startCLIWatcher(t, second, "1h")

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	report := decodeWatcherSync(t, stdout)
	if report.Status != syncStatusDeferred {
		t.Fatalf("sync report = %#v, want status %q", report, syncStatusDeferred)
	}
	if report.Fetch != nil || report.Push != nil {
		t.Fatalf("deferred report carried inline phases: %#v", report)
	}

	// The opening synchronization already published the create, so existence
	// proves nothing. Only the tip the update wrote does.
	waitForPublishedTip(t, second, task.ID)
}

func TestMutationFallsBackWhenTheWatcherIsGone(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Orphaned pointer")
	watchertest.StartDead(t, commonGitDir(t, second))

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	assertInlineSync(t, decodeWatcherSync(t, stdout))
	if !remoteHasTaskRef(t, second, task.ID) {
		t.Fatal("a dead watcher pointer left the change unpublished")
	}
}

func TestMutationFallsBackWhenTheWatcherStatusIsStale(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Stale watcher")
	watchertest.Start(t, commonGitDir(t, second), syncloop.Status{
		PID:        4211,
		IntervalMS: 5000,
		LastSyncAt: time.Now().Add(-time.Hour),
		LastSyncOK: true,
	})

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	assertInlineSync(t, decodeWatcherSync(t, stdout))
	if !remoteHasTaskRef(t, second, task.ID) {
		t.Fatal("a stale watcher left the change unpublished")
	}
}

// A watcher whose last synchronization failed knows origin is unreachable.
// Deferring would swallow the warning that says the work is local-only.
func TestMutationFallsBackWhenTheWatcherLastSyncFailed(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Failing watcher")
	watchertest.Start(t, commonGitDir(t, second), syncloop.Status{
		PID:        4211,
		IntervalMS: 5000,
		LastSyncAt: time.Now(),
		LastSyncOK: false,
		LastError:  "fetch failed: could not read from remote",
	})

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	assertInlineSync(t, decodeWatcherSync(t, stdout))
}

// A watcher that answers but cannot publish must not leave the caller believing
// the change was handed off.
func TestMutationPublishesInlineWhenTheNudgeIsRefused(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Refused nudge")
	recorder := watchertest.Start(t, commonGitDir(t, second), syncloop.Status{
		PID:        4211,
		IntervalMS: 5000,
		LastSyncAt: time.Now(),
		LastSyncOK: true,
	})
	recorder.RefuseNudges()

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	report := decodeWatcherSync(t, stdout)
	if report.Status != syncStatusCompleted || report.Push == nil {
		t.Fatalf("sync report = %#v, want an inline push after the refused nudge", report)
	}
	if !remoteHasTaskRef(t, second, task.ID) {
		t.Fatal("a refused nudge left the change unpublished")
	}
}

func TestDeferredMutationGatesOnAWatcherConflict(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Conflicted task")
	recorder := watchertest.Start(t, commonGitDir(t, second), syncloop.Status{
		PID:        4211,
		IntervalMS: 5000,
		LastSyncAt: time.Now(),
		LastSyncOK: true,
		Conflicts: []syncloop.ConflictEntry{{
			Conflict: core.Conflict{
				TaskID:      task.ID,
				Type:        core.ConflictDescription,
				Description: &core.DescriptionConflict{Base: "b", Ours: "o", Theirs: "t"},
			},
			Head: "head-1",
		}},
	})

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 8 {
		t.Fatalf("update against a conflicted task = code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if acknowledged := recorder.Acknowledgements(); len(acknowledged) != 1 || acknowledged[0] != task.ID {
		t.Fatalf("acknowledgements = %#v, want one for %s", acknowledged, task.ID)
	}
}

func TestDeferredMutationIgnoresAnUnrelatedWatcherConflict(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Unaffected task")
	other := cliCreateTask(t, second, "Conflicted elsewhere")
	watchertest.Start(t, commonGitDir(t, second), syncloop.Status{
		PID:        4211,
		IntervalMS: 5000,
		LastSyncAt: time.Now(),
		LastSyncOK: true,
		Conflicts: []syncloop.ConflictEntry{{
			Conflict: core.Conflict{
				TaskID:      other.ID,
				Type:        core.ConflictDescription,
				Description: &core.DescriptionConflict{},
			},
			Head: "head-1",
		}},
	})

	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if report := decodeWatcherSync(t, stdout); report.Status != syncStatusDeferred {
		t.Fatalf("sync report = %#v, want status %q", report, syncStatusDeferred)
	}
}

// The board polls its own API once a second, so hosting the loop is the whole
// change: a teammate's push reaches the browser with no client work.
func TestRunServeSurfacesAnOriginAdvanceWithoutACommand(t *testing.T) {
	first, second := cliSyncRepositories(t)
	task := cliCreateTask(t, first, "Board task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("push = code %d, stderr %q", code, stderr)
	}

	address := reserveAddress(t)
	stopServe := startServe(t, second, address)
	defer stopServe()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fetchBoardTasks(t, address), task.ID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("board never surfaced %s pushed by another clone", task.ID)
}

func TestRunServeDefersToAnExternalWatcher(t *testing.T) {
	_, second := cliSyncRepositories(t)
	startCLIWatcher(t, second, "1h")

	address := reserveAddress(t)
	output, stopServe := startServeCapturing(t, second, address)
	defer stopServe()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "external sync watcher") {
			// The board still serves; it simply runs no loop of its own.
			if !strings.Contains(fetchBoardTasks(t, address), "workbook.tasks") {
				t.Fatal("board stopped serving after deferring to an external watcher")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("board never reported deferring to the external watcher; wrote %q", output.String())
}

// A web mutation publishes by nudging, not by waiting for a scheduled tick.
// The external watcher's interval is an hour, so origin only holds the new ref
// if the mutation handed it over.
func TestWebMutationPublishesWithoutWaitingForATick(t *testing.T) {
	_, second := cliSyncRepositories(t)
	startCLIWatcher(t, second, "1h")

	address := reserveAddress(t)
	_, stopServe := startServeCapturing(t, second, address)
	defer stopServe()

	created := createTaskThroughBoard(t, address, "Nudged from the board")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if remoteTaskRefValue(t, second, created) != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("origin never received %s; the board waited for a tick instead of nudging", created)
}

// The toggle has to change what actually happens, not just what is reported.
// Inline means the response returns only after origin has the change, so a
// watcher that refuses to publish cannot hide behind a receipt.
func TestBoardInlineModePublishesBeforeResponding(t *testing.T) {
	_, second := cliSyncRepositories(t)
	startCLIWatcher(t, second, "1h")

	address := reserveAddress(t)
	_, stopServe := startServeCapturing(t, second, address)
	defer stopServe()

	if mode := boardSyncMode(t, address); mode != "deferred" {
		t.Fatalf("initial publication mode = %q, want deferred", mode)
	}
	if mode := setBoardSyncMode(t, address, "inline"); mode != "inline" {
		t.Fatalf("mode after the toggle = %q, want inline", mode)
	}

	created := createTaskThroughBoard(t, address, "Published inline")
	if remoteTaskRefValue(t, second, created) == "" {
		t.Fatal("inline mode returned before origin had the change")
	}
}

// A repository with no origin has nothing to publish to. The mutation still
// has to succeed, because the local write is the durable result.
func TestWebMutationSucceedsWithoutAnOrigin(t *testing.T) {
	repository := initializedRepository(t)
	address := reserveAddress(t)
	_, stopServe := startServeCapturing(t, repository, address)
	defer stopServe()

	created := createTaskThroughBoard(t, address, "No origin here")
	if localTaskRef(t, repository, created) == "" {
		t.Fatalf("task %s was not recorded locally", created)
	}
}

// Ctrl-C must not strand work the watcher was still holding.
func TestWatcherPublishesUnsyncedWorkOnShutdown(t *testing.T) {
	_, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Shutdown task")

	output := &watcherOutput{}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	go func() {
		finished <- Run(ctx, []string{"sync", "--watch", "--interval", "1h"}, second, output, output)
	}()
	waitForWatcherReady(t, output)

	// Record more work locally without telling the watcher, so only the final
	// synchronization can publish it.
	cliUpdateTitle(t, second, task.ID, "Recorded before shutdown")

	cancel()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("watcher exit code = %d; wrote %q", code, output.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("watcher did not stop")
	}

	if local, remote := localTaskRef(t, second, task.ID), remoteTaskRefValue(t, second, task.ID); local != remote {
		t.Fatalf("origin holds %q, local holds %q; shutdown stranded work the watcher was holding", remote, local)
	}
}

// The board's final synchronization runs alongside the HTTP drain rather than
// after it, so shutdown stays inside one budget instead of two.
func TestServeShutdownStaysWithinBudget(t *testing.T) {
	_, second := cliSyncRepositories(t)
	address := reserveAddress(t)

	ctx, cancel := context.WithCancel(context.Background())
	output := &watcherOutput{}
	finished := make(chan int, 1)
	go func() {
		finished <- Run(ctx, []string{"serve", "--addr", address}, second, output, output)
	}()
	waitForHTTP(t, "http://"+address+"/healthz")

	cancel()
	started := time.Now()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("serve exit code = %d; wrote %q", code, output.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("serve took %s to shut down, want the drain and the final sync concurrent", elapsed)
	}
}

// --- helpers ---

func reserveAddress(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}
	return address
}

func startServe(t *testing.T, repository, address string) func() {
	t.Helper()
	_, stop := startServeCapturing(t, repository, address)
	return stop
}

func startServeCapturing(t *testing.T, repository, address string) (*watcherOutput, func()) {
	t.Helper()
	output := &watcherOutput{}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	go func() {
		finished <- Run(ctx, []string{"serve", "--addr", address}, repository, output, output)
	}()
	waitForHTTP(t, "http://"+address+"/healthz")
	stopped := false
	return output, func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-finished:
		case <-time.After(20 * time.Second):
			t.Error("serve did not stop")
		}
	}
}

// createTaskThroughBoard drives the real HTTP surface the browser uses and
// returns the created task's ID.
func createTaskThroughBoard(t *testing.T, address, title string) string {
	t.Helper()
	body := strings.NewReader(`{"title":` + strconv.Quote(title) + `}`)
	response, err := http.Post("http://"+address+"/api/tasks", "application/json", body)
	if err != nil {
		t.Fatalf("create through the board: %v", err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read create response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create through the board = %d: %s", response.StatusCode, contents)
	}
	var document struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode create response: %v; body %s", err, contents)
	}
	if document.Task.ID == "" {
		t.Fatalf("create response named no task: %s", contents)
	}
	return document.Task.ID
}

func boardSyncMode(t *testing.T, address string) string {
	t.Helper()
	response, err := http.Get("http://" + address + "/api/sync")
	if err != nil {
		t.Fatalf("read the board's publication mode: %v", err)
	}
	defer response.Body.Close()
	return decodeBoardSyncMode(t, response)
}

func setBoardSyncMode(t *testing.T, address, mode string) string {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPut,
		"http://"+address+"/api/sync",
		strings.NewReader(`{"mode":`+strconv.Quote(mode)+`}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("shift the board's publication mode: %v", err)
	}
	defer response.Body.Close()
	return decodeBoardSyncMode(t, response)
}

func decodeBoardSyncMode(t *testing.T, response *http.Response) string {
	t.Helper()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("/api/sync = %d: %s", response.StatusCode, contents)
	}
	var document struct {
		Sync struct {
			Mode string `json:"mode"`
		} `json:"sync"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode sync document: %v; body %s", err, contents)
	}
	return document.Sync.Mode
}

func fetchBoardTasks(t *testing.T, address string) string {
	t.Helper()
	response, err := http.Get("http://" + address + "/api/tasks")
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read board tasks: %v", err)
	}
	return string(body)
}

func waitForWatcherReady(t *testing.T, output *watcherOutput) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), syncloop.ReadyPrefix) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("watcher never reported readiness; wrote %q", output.String())
}

func commonGitDir(t *testing.T, repository string) string {
	t.Helper()
	return filepath.Join(repository, ".git")
}

// watcherSyncReport decodes only the envelope members these tests assert on,
// so an unrelated addition does not break them.
type watcherSyncReport struct {
	Status string          `json:"status"`
	Detail string          `json:"detail"`
	Fetch  json.RawMessage `json:"fetch"`
	Push   json.RawMessage `json:"push"`
}

func decodeWatcherSync(t *testing.T, output string) watcherSyncReport {
	t.Helper()
	var envelope struct {
		Sync *watcherSyncReport `json:"sync"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode sync envelope: %v; output = %q", err, output)
	}
	if envelope.Sync == nil {
		t.Fatalf("result carried no sync member; output = %q", output)
	}
	return *envelope.Sync
}

func assertInlineSync(t *testing.T, report watcherSyncReport) {
	t.Helper()
	if report.Status != syncStatusCompleted {
		t.Fatalf("sync report = %#v, want an inline %q", report, syncStatusCompleted)
	}
	if report.Fetch == nil || report.Push == nil {
		t.Fatalf("sync report = %#v, want both inline phases", report)
	}
}

// startCLIWatcher runs `workbook sync --watch` in the background and returns
// once it is listening. The watcher is stopped and its exit checked at cleanup.
func startCLIWatcher(t *testing.T, repository, interval string) *watcherOutput {
	t.Helper()
	output := &watcherOutput{}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	go func() {
		finished <- Run(ctx, []string{"sync", "--watch", "--interval", interval}, repository, output, output)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-finished:
			if code != 0 {
				t.Errorf("watcher exit code = %d; wrote %q", code, output.String())
			}
		case <-time.After(15 * time.Second):
			t.Error("watcher did not stop after cancellation")
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), syncloop.ReadyPrefix) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(output.String(), syncloop.ReadyPrefix) {
		t.Fatalf("watcher never reported readiness; wrote %q", output.String())
	}

	// Readiness means bound and listening, which is deliberately not the same
	// as trustworthy: until the opening synchronization lands, a mutation
	// correctly refuses to defer. Tests want the settled state.
	for time.Now().Before(deadline) {
		if watcherStatus(t, repository).LastSyncOK {
			return output
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("watcher never completed a synchronization; wrote %q", output.String())
	return nil
}

func watcherStatus(t *testing.T, repository string) watcherStatusResult {
	t.Helper()
	code, stdout, stderr := run(t, repository, "sync", "--status", "--json")
	if code != 0 {
		t.Fatalf("sync --status = code %d, stderr %q", code, stderr)
	}
	var result watcherStatusResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// waitForPublishedTip waits until origin holds exactly the tip this clone does,
// which is the only assertion that distinguishes work the nudge published from
// work an earlier synchronization already carried.
func waitForPublishedTip(t *testing.T, repository, taskID string) {
	t.Helper()
	local := localTaskRef(t, repository, taskID)
	if local == "" {
		t.Fatalf("no local ref for %s", taskID)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if remoteTaskRefValue(t, repository, taskID) == local {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("origin never reached the local tip %s for %s", local, taskID)
}

func localTaskRef(t *testing.T, repository, taskID string) string {
	t.Helper()
	command := exec.Command("git", "-C", repository, "rev-parse", "--verify", "--quiet", "refs/workbook/tasks/"+taskID)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func remoteTaskRefValue(t *testing.T, repository, taskID string) string {
	t.Helper()
	fields := strings.Fields(remoteTaskRef(t, repository, taskID))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func localHasTaskRef(t *testing.T, repository, taskID string) bool {
	t.Helper()
	command := exec.Command("git", "-C", repository, "rev-parse", "--verify", "--quiet", "refs/workbook/tasks/"+taskID)
	output, err := command.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// watcherOutput collects a background command's writes. The command writes from
// its own goroutine while the test reads, so a plain bytes.Buffer would race.
type watcherOutput struct {
	mu       sync.Mutex
	contents bytes.Buffer
}

func (w *watcherOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.contents.Write(p)
}

func (w *watcherOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.contents.String()
}
