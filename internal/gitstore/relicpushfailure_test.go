package gitstore

import (
	"context"
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
