package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// Nothing above internal/gitstore had ever run against a SHA-256 repository:
// the CLI, the SQLite projection, and history validation all reached Git
// through fixtures that did a plain `git init`. Object IDs cross every one of
// those boundaries — the projection stores a head per task, validation
// checkpoints one, and `show --compare` takes two on the command line — so a
// 40-character assumption in any of them is invisible to the SHA-1 suite.
func TestRunLifecycleInASHA256Repository(t *testing.T) {
	testrepo.RequireObjectFormat(t, testrepo.FormatSHA256)
	repository := testrepo.New(t, testrepo.WithObjectFormat(testrepo.FormatSHA256))
	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := gitOutput(t, repository, "rev-parse", "--show-object-format"); got != testrepo.FormatSHA256 {
		t.Fatalf("repository object format = %q, want %q", got, testrepo.FormatSHA256)
	}

	code, stdout, stderr := run(t, repository, "create", "SHA-256 task", "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	created := decodeMutationTask(t, stdout, "create")
	assertSHA256ObjectID(t, "created head", created.Head)
	if got := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+created.ID); got != created.Head {
		t.Fatalf("created head = %q, Git ref = %q", created.Head, got)
	}

	code, stdout, stderr = run(t, repository, "update", created.ID, "--status", "in-progress", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
	}
	updated := decodeMutationTask(t, stdout, "update")
	assertSHA256ObjectID(t, "updated head", updated.Head)
	if updated.Head == created.Head || updated.Status != core.StatusInProgress {
		t.Fatalf("updated task = %#v, want an advanced head and status in-progress", updated)
	}

	// The history entries the CLI prints are addressed by full object ID, so a
	// SHA-256 repository has to round-trip one back through --compare.
	code, stdout, stderr = run(t, repository, "show", created.ID, "--history", "--all", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show --history = (%d, %q, %q)", code, stdout, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode task detail: %v", err)
	}
	if detail.History == nil || detail.History.Total != 2 || len(detail.History.Changes) != 2 {
		t.Fatalf("history = %#v, want the create and the update", detail.History)
	}
	for _, entry := range detail.History.Changes {
		assertSHA256ObjectID(t, "history commit", entry.Commit)
	}
	if code, _, stderr := run(t, repository, "show", created.ID, "--compare", created.Head, updated.Head); code != 0 {
		t.Fatalf("show --compare code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr = run(t, repository, "delete", created.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("delete = (%d, %q, %q)", code, stdout, stderr)
	}
	if deleted := decodeMutationTask(t, stdout, "delete"); !deleted.Deleted {
		t.Fatalf("deleted task = %#v, want a tombstone", deleted)
	}
	code, stdout, stderr = run(t, repository, "restore", created.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("restore = (%d, %q, %q)", code, stdout, stderr)
	}
	restored := decodeMutationTask(t, stdout, "restore")
	if restored.Deleted {
		t.Fatalf("restored task = %#v, want an active task", restored)
	}
	assertSHA256ObjectID(t, "restored head", restored.Head)

	// The projection is rebuilt from these refs, and validation replays them,
	// so both have to accept object IDs this long.
	if code, _, stderr := run(t, repository, "rebuild"); code != 0 {
		t.Fatalf("rebuild code = %d, want 0; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, repository, "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list = (%d, %q, %q)", code, stdout, stderr)
	}
	var listed []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &listed); err != nil {
		t.Fatalf("decode listed tasks: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID || listed[0].Head != restored.Head {
		t.Fatalf("rebuilt projection = %#v, want the restored task at %q", listed, restored.Head)
	}
	if code, _, stderr := run(t, repository, "validate", "--full"); code != 0 {
		t.Fatalf("validate --full code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("cached validate code = %d, want 0; stderr = %q", code, stderr)
	}
}

func assertSHA256ObjectID(t *testing.T, name, objectID string) {
	t.Helper()
	if len(objectID) != 64 || strings.TrimLeft(objectID, "0123456789abcdef") != "" {
		t.Fatalf("%s = %q, want a 64-character SHA-256 object ID", name, objectID)
	}
}
