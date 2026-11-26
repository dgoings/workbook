package gitstore

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// A sync whose relic push fails on the wire after its task push went out
// keeps the push phase completed, with the task published, and says the
// relics were not published both in the phase's detail and in its error.
func TestSyncKeepsThePushPhaseWhenOnlyTheRelicPushFails(t *testing.T) {
	t.Parallel()
	_, second, config := syncRepositories(t)
	ctx := context.Background()
	task := createSyncTask(t, second, config, "Goes out")
	writeRelicRootNumbered(t, second, config, 1)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "git")
	testenv.WriteExecutable(t, path, []byte("#!/bin/sh\npush=no\nrelic=no\n"+
		"for arg in \"$@\"; do\n  case \"$arg\" in push) push=yes ;; *refs/workbook/relics/*) relic=yes ;; esac\ndone\n"+
		"if [ $push = yes ] && [ $relic = yes ]; then\n"+
		"  echo 'fatal: the remote end hung up unexpectedly' >&2\n  exit 128\nfi\n"+
		"exec '"+real+"' \"$@\"\n"))
	second.gitPath = path

	result, err := second.Sync(ctx, config)
	if err == nil || !strings.HasPrefix(err.Error(), relicPushFailedDetail+": ") || !strings.Contains(err.Error(), "hung up") {
		t.Fatalf("Sync() error = %v, want the relic push failure", err)
	}
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("Sync() error category = %q, want operational", core.CategoryOf(err))
	}
	if result.Push.Status != SyncPhaseCompleted || !strings.HasPrefix(result.Push.Detail, relicPushFailedDetail+": ") {
		t.Fatalf("push phase = %q %q, want completed with the relic failure", result.Push.Status, result.Push.Detail)
	}
	if len(result.Push.Tasks) != 1 || result.Push.Tasks[0].TaskID != task.ID || result.Push.Tasks[0].Status != SyncPublished {
		t.Fatalf("push tasks = %+v, want %s published", result.Push.Tasks, task.ID)
	}
}

// A relic push that fails on the wire in the same run as a task error that is
// not operational, corrupt data or a stale write, is named with its reason in
// the error's message, which is all a command prints for those categories,
// and only once in the whole text. The exit code stays the task error's, and
// the relic failure stays reachable in the chain.
func TestARelicPushFailureKeepsItsReasonBesideEveryTaskError(t *testing.T) {
	t.Parallel()
	refusal := core.TasksRemoteRefusal{Reason: "tasks-remote-missing"}
	relicErr := core.Wrap(core.CategoryOperational, "cannot push relic refs to origin",
		core.TasksRemoteError(core.CategoryOperational, refusal, "fatal: the remote end hung up unexpectedly"))
	for _, taskErr := range []error{
		core.Errorf(core.CategoryCorruptData, "1 local task ref(s) failed validation"),
		core.Errorf(core.CategoryCorruptData, "1 local task ref(s) record an operation twice and were not published"),
		core.Errorf(core.CategoryStaleWrite, "1 local task ref(s) changed during push"),
		core.Errorf(core.CategoryOperational, "origin rejected 1 task ref(s)"),
	} {
		result, err := relicPushFailed(SyncResult{Status: SyncPhaseCompleted}, relicErr, taskErr)
		want := taskErr.Error() + "; " + relicPushFailedDetail + ": " + relicErr.Error()
		var typed *core.Error
		if err == nil || err.Error() != want || !errors.As(err, &typed) || typed.Message != want {
			t.Fatalf("relicPushFailed(%v) = %v (message %+v), want %q as both text and message", taskErr, err, typed, want)
		}
		if strings.Count(err.Error(), "hung up") != 1 {
			t.Fatalf("relicPushFailed(%v) = %q, want the relic reason once", taskErr, err)
		}
		if core.CategoryOf(err) != core.CategoryOf(taskErr) || core.ExitCode(err) != core.ExitCode(taskErr) {
			t.Fatalf("relicPushFailed(%v) category = %q, want the task error's %q", taskErr, core.CategoryOf(err), core.CategoryOf(taskErr))
		}
		if found, ok := core.TasksRemoteRefusalOf(err); !ok || found.Reason != refusal.Reason {
			t.Fatalf("relicPushFailed(%v) refusal = %+v, want the relic failure's", taskErr, found)
		}
		if !errors.Is(err, relicErr) {
			t.Fatalf("relicPushFailed(%v) = %v, want it to wrap the relic failure", taskErr, err)
		}
		if result.Detail != relicPushFailedDetail+": "+relicErr.Error() {
			t.Fatalf("push detail = %q", result.Detail)
		}
	}
}
