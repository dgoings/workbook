package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The migration the note recommends, run end to end on the project shape that
// needs it: a pre-ledger project holding tasks in `blocked`.
//
// Nothing here is automatic, and that is the point. The removal is a command a
// person runs; it re-files the tasks by forwarding rather than by rewriting
// them; it reports what the queue gains; and the value it retired keeps
// resolving afterwards so a teammate who names it is told what happened rather
// than that it never existed.
func TestStatusDeleteBlockedMigratesAPreLedgerProject(t *testing.T) {
	t.Parallel()
	repository := preLedgerRepository(t)
	free := cliCreateTask(t, repository, "Was blocked")
	dependent := cliCreateTask(t, repository, "Was blocked and waiting")
	prerequisite := cliCreateTask(t, repository, "Prerequisite")
	mustRunStatus(t, repository, "depend", dependent.ID, prerequisite.ID, "--no-sync")
	for _, id := range []string{free.ID, dependent.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "blocked", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "blocked", "--into", "backlog", "--json")
	if removal.Tasks.Affected != 2 {
		t.Fatalf("affected = %d, want the two tasks in blocked", removal.Tasks.Affected)
	}
	// None of them becomes claimable, and that is the honest answer for the
	// recommended destination: `backlog` carries no `next` tag, so the tasks land
	// where new work lands and somebody still moves them on deliberately. Nothing
	// was claimable while they sat in `blocked` either, so the migration hands
	// nobody a queue they did not ask for.
	if removal.Tasks.ClaimableAfter != 0 {
		t.Fatalf("claimableAfter = %d, want none: backlog is not tagged next", removal.Tasks.ClaimableAfter)
	}

	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after the migration = %v, want %v", got, want)
	}
	document := cliStatusList(t, repository)
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none once `blocked` is gone", document.Migrations)
	}
	if len(document.Unresolved) != 0 {
		t.Fatalf("unresolved = %#v, want the tasks forwarded rather than stranded", document.Unresolved)
	}
	backlog := document.Statuses[0]
	if backlog.Status != "backlog" || backlog.Tasks == nil || *backlog.Tasks != 3 {
		t.Fatalf("backlog = %#v, want all three tasks resolving there", backlog)
	}

	code, board, stderr := run(t, repository, "board", "--narrow")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(board, "BLOCKED") {
		t.Errorf("the board still draws a Blocked column after the removal:\n%s", board)
	}

	// Naming the removed value afterwards is answered with what became of it and
	// when, which is the whole reason a removal leaves a forwarding pointer.
	today := time.Now().UTC().Format("2006-01-02")
	code, _, stderr = run(t, repository, "status", "label", "blocked", "Blocked Again", "--json")
	if code != 4 {
		t.Fatalf("status label blocked = code %d, want 4; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		fmt.Sprintf(`no status "blocked"; it was removed into "backlog" on %s`, today))

	// A caller supplying it to a task command is refused the same way, and the
	// task holding it stays fully editable.
	code, _, stderr = run(t, repository, "update", free.ID, "--status", "blocked", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("update --status blocked = code %d, want 5; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "update", free.ID, "--title", "Renamed", "--no-sync"); code != 0 {
		t.Fatalf("editing a task stored under the removed status = code %d; stderr = %q", code, stderr)
	}
}

// --into is required, never prompted for, and the refusal carries the answer:
// agents run this command, and a prompt would hang one.
func TestStatusDeleteRequiresIntoAndRefusesItself(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "status", "delete", "in-review", "--json")
	if code != 2 {
		t.Fatalf("status delete without --into = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("status delete stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		"status delete requires --into <status>, naming where the removed status's tasks belong; "+
			"this project's statuses are: backlog, ready, in-progress, in-review, done")

	code, _, stderr = run(t, repository, "status", "delete", "in-review", "--into", "in-review", "--json")
	if code != 5 {
		t.Fatalf("status delete into itself = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`status delete cannot forward "in-review" into itself; name where its tasks belong`)

	// Nothing the refusals touched was recorded. The head is the genesis this
	// project was minted with, and it has not moved.
	before := cliStatusList(t, repository).Head
	if code, _, _ := run(t, repository, "status", "delete", "in-review", "--json"); code == 0 {
		t.Fatal("status delete without --into succeeded")
	}
	if after := cliStatusList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused removal", before, after)
	}
}

// What a removal costs is counted before it happens, in the terms the person
// running it is deciding about: how many tasks move, and how many of those
// become claimable where they land.
//
// Claimable means what `workbook next` means. A task whose only dependency is
// finished is claimable, and counting only the tasks with no dependencies at all
// reported a queue growing by one where `workbook next` would hand out two.
func TestStatusDeleteCountsAffectedAndClaimableTasks(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")

	free := cliCreateTask(t, repository, "No dependencies")
	satisfied := cliCreateTask(t, repository, "Waiting on finished work")
	waiting := cliCreateTask(t, repository, "Waiting on unfinished work")
	finished := cliCreateTask(t, repository, "Finished")
	unfinished := cliCreateTask(t, repository, "Unfinished")

	mustRunStatus(t, repository, "depend", satisfied.ID, finished.ID, "--no-sync")
	mustRunStatus(t, repository, "depend", waiting.ID, unfinished.ID, "--no-sync")
	mustRunStatus(t, repository, "update", finished.ID, "--status", "done", "--no-sync")
	for _, id := range []string{free.ID, satisfied.ID, waiting.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "triage", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "ready", "--json")
	if removal.Tasks.Affected != 3 {
		t.Fatalf("affected = %d, want the three tasks in triage", removal.Tasks.Affected)
	}
	if removal.Tasks.ClaimableAfter != 2 {
		t.Fatalf("claimableAfter = %d, want the dependency-free task and the satisfied one",
			removal.Tasks.ClaimableAfter)
	}

	// The count is a prediction about `workbook next`, so it is checked against
	// `workbook next` rather than against itself: draining the queue has to
	// hand out exactly the tasks that were counted.
	claimed := make(map[string]bool)
	for range removal.Tasks.ClaimableAfter {
		code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("next = code %d, stderr %q", code, stderr)
		}
		var task core.Task
		if err := json.Unmarshal(assertJSONResult(t, stdout, "next").Data, &task); err != nil {
			t.Fatal(err)
		}
		if claimed[task.ID] {
			t.Fatalf("next offered %s twice", task.ID)
		}
		claimed[task.ID] = true
		// Taking it out of the queue is what lets the next call answer about a
		// different task.
		mustRunStatus(t, repository, "update", task.ID, "--status", "in-progress", "--no-sync")
	}
	if !claimed[free.ID] || !claimed[satisfied.ID] {
		t.Fatalf("next handed out %#v, want the two tasks the count promised", claimed)
	}
	code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("next = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"data":null`) {
		t.Fatalf("next after the counted tasks = %s, want no eligible task", stdout)
	}

	// The tasks were not rewritten; they resolve into the destination.
	document := cliStatusList(t, repository)
	if len(document.Retired) != 1 || document.Retired[0].Status != "triage" ||
		document.Retired[0].Becomes != "ready" || document.Retired[0].Operation != "status.remove" {
		t.Fatalf("retired = %#v, want triage forwarded to ready", document.Retired)
	}
	if document.Retired[0].At == "" {
		t.Fatal("retired entry carries no date, want the ledger's wall time")
	}
}

// A destination outside `next` leaves nothing claimable, which is the other half
// of the count and the reason it is reported separately from the total.
func TestStatusDeleteReportsNoClaimableTasksForAParkedDestination(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	task := cliCreateTask(t, repository, "Alpha")
	mustRunStatus(t, repository, "update", task.ID, "--status", "triage", "--no-sync")

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "in-progress", "--json")
	if removal.Tasks.Affected != 1 || removal.Tasks.ClaimableAfter != 0 {
		t.Fatalf("tasks = %#v, want one affected and none claimable", removal.Tasks)
	}
}

// The note on a removal's inverse says which tasks come back, and both halves
// of that are checked against real tasks.
//
// Defining the name again drops the forwarding pointer, so a task still stored
// under the old value reads as being in that column again — the note used to
// claim the opposite. What does not come back is a task some later write
// settled, because correct-on-touch rewrote its stored value to the destination
// and no configuration change can find it again.
func TestStatusDeleteInverseReturnsStoredTasksAndLeavesSettledOnes(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	stored := cliCreateTask(t, repository, "Never touched again")
	settled := cliCreateTask(t, repository, "Touched after the removal")
	for _, id := range []string{stored.ID, settled.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "triage", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "backlog", "--json")
	want := `tasks still stored under "triage" return to it, because defining the name again drops the ` +
		`forwarding pointer; tasks a later write settled into "backlog" stay there`
	if removal.Inverse.Note != want {
		t.Fatalf("note = %q, want %q", removal.Inverse.Note, want)
	}

	// One task is written to after the removal, which settles its stored status
	// into the destination. The other is left alone.
	mustRunStatus(t, repository, "update", settled.ID, "--title", "Touched", "--no-sync")

	mustRunStatus(t, repository, strings.Fields(strings.TrimPrefix(removal.Inverse.Command, "workbook "))...)

	if got := taskStatus(t, repository, stored.ID); got != "triage" {
		t.Fatalf("the untouched task is in %q, want it back in triage as the note says", got)
	}
	if got := taskStatus(t, repository, settled.ID); got != "backlog" {
		t.Fatalf("the settled task is in %q, want it left in backlog as the note says", got)
	}
}
