package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/syncloop"
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

// --- helpers ---

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
