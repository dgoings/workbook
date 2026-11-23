package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// A task deleted and compacted at the place the tasks are leaving, which a
// teammate edited at the place they return to after they left, is resolved
// the way a forward move resolves an edit to a deleted task: the late edit is
// set aside in the mover's clone, parked and reported once by title and ID
// with exit 8, and the task ends deleted and compacted at the returned-to
// place, its older copy there backed up first. A rerun is quiet, and every
// operation the older copy held is at origin or in the parked ref.
func TestMoveTasksBackSetsAsideALateEditToADeletedCompactedTask(t *testing.T) {
	t.Parallel()
	project, task, stale := stalePlaceWithLateEdit(t, true)
	replaced := operationsAccountedFor(t, project.code, "refs/workbook/tasks/"+task)
	if len(replaced) < 2 {
		t.Fatalf("origin's stale copy holds %d operations, want the create and the late edit", len(replaced))
	}

	code, stdout, stderr := run(t, project.seed, "move-tasks", "--json")
	if code != 8 {
		t.Fatalf("move-tasks = (%d, %q, %q), want 8", code, stdout, stderr)
	}
	message := errorDocumentOf(t, stderr).Error.Message
	for _, want := range []string{
		`"Shared before the move" (` + task + `): it was deleted and compacted at tasks, so the change to its title made at origin cannot apply to it`,
		"refs/workbook/reconciled/" + task + "/",
		"workbook serve run from this clone shows it on its task as a set-aside edit",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("move-tasks report %q lacks %q", message, want)
		}
	}
	var moved replacingMoveRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || moved.Returned != "returned" || len(moved.Replaced) != 1 || moved.Replaced[0] != task ||
		len(moved.Bundles) != 1 || len(moved.SetAside) != 1 || moved.SetAside[0].TaskID != task ||
		moved.SetAside[0].ParkedRef == "" || len(moved.SetAside[0].Operations) != 1 {
		t.Fatalf("move-tasks = %+v, want the late edit set aside and the copy replaced after one bundle", moved)
	}
	if parked := strings.TrimSpace(cliGitOutput(t, project.seed, "rev-parse", moved.SetAside[0].ParkedRef)); parked != stale {
		t.Fatalf("parked %s at %s, want origin's stale tip %s", moved.SetAside[0].ParkedRef, parked, stale)
	}
	if got, want := tasksTip(t, project.code, task), localTaskRef(t, project.seed, task); got != want {
		t.Fatalf("origin holds %s at %s, want the compacted checkpoint %s", task, got, want)
	}
	after := operationsAccountedFor(t, project.code, "refs/workbook/tasks/"+task)
	parked := operationsAccountedFor(t, project.seed, moved.SetAside[0].ParkedRef)
	for id := range replaced {
		if !after[id] && !parked[id] {
			t.Errorf("operation %s of the replaced copy is neither at origin nor set aside", id)
		}
	}
	if shown := cliShowTask(t, project.seed, task); !shown.Deleted {
		t.Fatalf("%s after the move back = %+v, want it deleted", task, shown)
	}

	for round := 0; round < 2; round++ {
		code, stdout, stderr := run(t, project.seed, "move-tasks")
		if code != 0 || strings.Contains(stdout, "Backup:") || strings.Contains(stderr, task) {
			t.Fatalf("rerun %d = (%d, %q, %q), want quiet success", round, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := run(t, project.seed, "sync"); code != 0 {
		t.Fatalf("sync after the move back = (%d, %q, %q)", code, stdout, stderr)
	}
	requireEachOperationOnce(t, project.code)
}
