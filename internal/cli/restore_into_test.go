package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// `restore --into` reports its destination the way every other verb reports
// what it changed: through the task in the result envelope. There is no second
// field to read and none to keep in step, because the destination is the task's
// status once the command has run.
func TestRestoreIntoReportsTheDestination(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Deleted then restored", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if code, _, stderr := run(t, repository, "delete", task.ID, "--no-sync"); code != 0 {
		t.Fatalf("delete code = %d, want 0; stderr = %q", code, stderr)
	}
	deletedCommits := taskCommitCount(t, repository, task.ID)

	code, stdout, stderr = run(t, repository, "restore", task.ID, "--into", "in-progress", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("restore --into = (%d, %q, %q)", code, stdout, stderr)
	}
	restored := decodeMutationTask(t, stdout, "restore")
	if restored.Deleted {
		t.Fatal("restore --into left the task tombstoned")
	}
	if got, want := restored.Status, core.StatusInProgress; got != want {
		t.Fatalf("restore --into status = %q, want %q", got, want)
	}

	// One pack, so one commit: the restore and the destination are the same
	// change, and a reader of the task's history sees one.
	if got, want := taskCommitCount(t, repository, task.ID), deletedCommits+1; got != want {
		t.Fatalf("restore --into added %d commits, want 1", got-deletedCommits)
	}

	// The text envelope carries the destination in the column it always carried
	// the status in, so nothing about reading a restore changed.
	code, stdout, stderr = run(t, repository, "show", task.ID)
	if code != 0 || stderr != "" {
		t.Fatalf("show = (%d, %q, %q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "in-progress") {
		t.Fatalf("show output = %q, want the destination status", stdout)
	}
}

// A destination the project does not define is refused before anything is
// written, and the task stays tombstoned. This is the status vocabulary's
// refusal, in the same words `workbook update --status` produces for the same
// name.
func TestRestoreIntoRefusesAStatusTheProjectDoesNotDefine(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Stays deleted", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if code, _, stderr := run(t, repository, "delete", task.ID, "--no-sync"); code != 0 {
		t.Fatalf("delete code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr = run(t, repository, "restore", task.ID, "--into", "shipped", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("restore --into shipped = 0, want a refusal; stdout = %q", stdout)
	}
	if stdout != "" {
		t.Fatalf("restore --into stdout = %q, want nothing written for a refused restore", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation, `invalid task status "shipped"`)
	assertTaskDeleted(t, repository, task.ID, true)

	// An empty --into is a flag the caller typed that would otherwise do
	// nothing, so it is refused rather than read as "no destination".
	code, _, stderr = run(t, repository, "restore", task.ID, "--into", "", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("restore --into \"\" = 0, want a refusal; stderr = %q", stderr)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, "restore --into requires a status")
	assertTaskDeleted(t, repository, task.ID, true)
}

func taskCommitCount(t *testing.T, repository, taskID string) int {
	t.Helper()
	output := gitOutput(t, repository, "rev-list", "--count", "refs/workbook/tasks/"+taskID)
	count, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("parse commit count %q: %v", output, err)
	}
	return count
}
