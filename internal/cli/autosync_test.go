package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// autoSyncEnvelope decodes only the members these tests assert on, so an
// unrelated addition to the result envelope does not break them.
type autoSyncEnvelope struct {
	Sync *struct {
		Enabled bool   `json:"enabled"`
		Source  string `json:"source"`
		Status  string `json:"status"`
		Detail  string `json:"detail"`
		Push    *struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		} `json:"push"`
	} `json:"sync"`
}

func decodeAutoSync(t *testing.T, output string) autoSyncEnvelope {
	t.Helper()
	var envelope autoSyncEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode sync envelope: %v; output = %q", err, output)
	}
	return envelope
}

func remoteTaskRef(t *testing.T, repository, taskID string) string {
	t.Helper()
	command := exec.Command("git", "-C", repository, "ls-remote", "--refs", "origin", "refs/workbook/tasks/"+taskID)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestCreatePublishesTheNewTaskRefByDefault(t *testing.T) {
	repository, _ := cliSyncRepositories(t)

	code, stdout, stderr := run(t, repository, "create", "Automatically published", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if remoteTaskRef(t, repository, task.ID) == "" {
		t.Fatal("task ref was not published to origin")
	}

	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil {
		t.Fatal("result envelope has no sync member")
	}
	if !envelope.Sync.Enabled || envelope.Sync.Source != "default" {
		t.Fatalf("sync = %+v, want enabled by default", envelope.Sync)
	}
	if envelope.Sync.Push == nil || envelope.Sync.Push.Status != "published" {
		t.Fatalf("push outcome = %+v, want published", envelope.Sync.Push)
	}
}

func TestNoSyncFlagLeavesTheTaskUnpublished(t *testing.T) {
	repository, _ := cliSyncRepositories(t)

	code, stdout, stderr := run(t, repository, "create", "Deliberately local", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if got := remoteTaskRef(t, repository, task.ID); got != "" {
		t.Fatalf("remote ref = %q, want absent", got)
	}
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil || envelope.Sync.Enabled || envelope.Sync.Source != "flag" {
		t.Fatalf("sync = %+v, want disabled by flag", envelope.Sync)
	}
}

func TestUpdatePublishesOnlyTheMutatedRef(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	first := cliCreateTask(t, repository, "First")
	code, _, stderr := run(t, repository, "create", "Second", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "update", first.ID, "--status", "ready", "--json")
	if code != 0 {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil || envelope.Sync.Push == nil || envelope.Sync.Push.TaskID != first.ID {
		t.Fatalf("push outcome = %+v, want %s", envelope.Sync, first.ID)
	}
}

func TestMutationWithoutOriginReportsSkippedAndSucceeds(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "create", "Solo local task", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil || envelope.Sync.Status != "skipped" {
		t.Fatalf("sync = %+v, want skipped without an origin", envelope.Sync)
	}
}

func TestMutationWarnsButSucceedsWhenOriginIsUnreachable(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	cliGit(t, repository, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	code, stdout, stderr := run(t, repository, "create", "Written while offline", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, want 0; stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil || envelope.Sync.Status != "failed" {
		t.Fatalf("sync = %+v, want failed", envelope.Sync)
	}

	code, _, stderr = run(t, repository, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show = code %d, stderr %q; local write must survive", code, stderr)
	}
}

func TestDivergentTaskFailsWithStaleWriteAfterWritingLocally(t *testing.T) {
	first, second := cliSyncRepositories(t)
	code, stdout, stderr := run(t, first, "create", "Contested", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")

	if code, _, stderr := run(t, second, "fetch", "--json"); code != 0 {
		t.Fatalf("fetch = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "update", task.ID, "--title", "Remote winner", "--json"); code != 0 {
		t.Fatalf("remote update = code %d, stderr %q", code, stderr)
	}
	remoteHead := remoteTaskRef(t, second, task.ID)

	if code, _, _ := run(t, first, "update", task.ID, "--title", "Local loser", "--no-sync", "--json"); code != 0 {
		t.Fatalf("seeding local divergence = code %d", code)
	}

	code, stdout, _ = run(t, first, "update", task.ID, "--priority", "high", "--json")
	if code != 6 {
		t.Fatalf("update = code %d, want 6 (stale-write); stdout = %q", code, stdout)
	}
	if got := remoteTaskRef(t, second, task.ID); got != remoteHead {
		t.Fatalf("remote ref = %q, want unchanged %q", got, remoteHead)
	}

	code, _, stderr = run(t, first, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show = code %d, stderr %q; local write must survive divergence", code, stderr)
	}
}

func TestNextFetchesWithoutPublishing(t *testing.T) {
	first, second := cliSyncRepositories(t)
	task := cliCreateTask(t, first, "Ready for pickup")
	if code, _, stderr := run(t, first, "update", task.ID, "--status", "ready", "--json"); code != 0 {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}

	code, _, stderr := run(t, second, "create", "Local only", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := run(t, second, "next", "--json")
	if code != 0 {
		t.Fatalf("next = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, task.ID) {
		t.Fatalf("next did not fetch the remote ready task; stdout = %q", stdout)
	}
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync != nil && envelope.Sync.Push != nil {
		t.Fatalf("next published %+v, want fetch only", envelope.Sync.Push)
	}
}
