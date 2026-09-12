package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// priorityDeleteDocument is `workbook priority delete --json`, decoded to what
// this file asserts about.
type priorityDeleteDocument struct {
	Change struct {
		Operation string   `json:"operation"`
		Priority  string   `json:"priority"`
		Into      string   `json:"into"`
		Tags      []string `json:"tags"`
		Label     *struct {
			To string `json:"to"`
		} `json:"label"`
	} `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
	Tasks      struct {
		Affected int `json:"affected"`
	} `json:"tasks"`
	Inverse struct {
		Command string `json:"command"`
		Exact   bool   `json:"exact"`
		Note    string `json:"note"`
	} `json:"inverse"`
}

func cliPriorityDelete(t *testing.T, repository string, args ...string) priorityDeleteDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append([]string{"priority", "delete"}, args...)...)
	if code != 0 || stderr != "" {
		t.Fatalf("priority delete %v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityDeleteDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority delete").Data, &document); err != nil {
		t.Fatalf("decode priority delete: %v; output = %s", err, stdout)
	}
	return document
}

func cliShowTask(t *testing.T, repository, taskID string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", taskID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show %s = code %d, stderr %q", taskID, code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &task); err != nil {
		t.Fatalf("decode show: %v; output = %s", err, stdout)
	}
	return task
}

// A removal forwards: the task filed under the removed priority is not
// rewritten, and it reads as being at the destination on every clone from the
// moment the removal folds.
func TestPriorityDeleteForwardsTheTasksItRemoves(t *testing.T) {
	repository := initializedRepository(t)
	filed := createOrderingTask(t, repository, "Filed under low", "low")

	document := cliPriorityDelete(t, repository, "low", "--into", "medium", "--no-sync", "--json")
	if document.Change.Operation != "delete" || document.Change.Priority != "low" {
		t.Fatalf("change = %#v, want a removal of low", document.Change)
	}
	if document.Change.Into != "medium" {
		t.Fatalf("into = %q, want medium", document.Change.Into)
	}
	if document.Change.Label == nil || document.Change.Label.To != "Low" {
		t.Fatalf("label = %#v, want the label the removed priority had", document.Change.Label)
	}
	if document.Tasks.Affected != 1 {
		t.Fatalf("tasks.affected = %d, want the one task filed under low", document.Tasks.Affected)
	}

	if got, want := cliPriorityNames(t, repository), []string{"high", "medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	// The task still stores `low` and reads as `medium`.
	task := cliShowTask(t, repository, filed.ID)
	if task.Priority != core.Priority("medium") {
		t.Fatalf("task priority = %q, want medium", task.Priority)
	}
	if task.StoredPriority != core.Priority("low") {
		t.Fatalf("stored priority = %q, want the low it was filed under", task.StoredPriority)
	}

	// The value a teammate may still type resolves, and says what happened.
	list := cliPriorityList(t, repository)
	if len(list.Retired) != 1 {
		t.Fatalf("retired = %#v, want the removed priority", list.Retired)
	}
	retired := list.Retired[0]
	if retired.Priority != "low" || retired.Becomes != "medium" ||
		retired.Operation != string(core.ConfigPriorityRemove) {
		t.Fatalf("retired = %#v, want low forwarded into medium by a removal", retired)
	}

	// Defining the name again is the inverse, and it says what it cannot
	// return.
	if got, want := document.Inverse.Command, "workbook priority add low --after medium --label Low"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if document.Inverse.Exact {
		t.Fatal("inverse = exact, want not exact; a removal moves tasks the add never touched")
	}
	if !strings.Contains(document.Inverse.Note, "tasks still stored under") {
		t.Fatalf("inverse note = %q, want it to say which tasks return", document.Inverse.Note)
	}
}

// Where the tasks go is never guessed, and the refusal names the priorities it
// could have been.
func TestPriorityDeleteRequiresIntoAndRefusesItself(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository).Head

	code, stdout, stderr := run(t, repository, "priority", "delete", "low", "--json")
	if code != 2 {
		t.Fatalf("priority delete without --into = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("priority delete stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		"priority delete requires --into <priority>, naming where the removed priority's tasks belong; "+
			"this project's priorities are: high, medium, low")

	code, _, stderr = run(t, repository, "priority", "delete", "low", "--into", "low", "--json")
	if code != 5 {
		t.Fatalf("priority delete into itself = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`priority delete cannot forward "low" into itself; name where its tasks belong`)

	code, _, stderr = run(t, repository, "priority", "delete", "low", "--into", "urgent", "--json")
	if code != 4 {
		t.Fatalf("priority delete into an unknown priority = code %d, want 4; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		`no priority "urgent" in this project; the priorities are: high, medium, low`)

	// Removing the priority tagged default would leave a new task with nowhere
	// to land, and the authoring boundary refuses it with the command that
	// fixes it.
	code, _, stderr = run(t, repository, "priority", "delete", "medium", "--into", "high", "--json")
	if code != 5 {
		t.Fatalf("priority delete of the default = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		"no priority is tagged default, so a new task would have no priority to land on; "+
			"tag one first: workbook priority tag <priority> --tag default")

	if after := cliPriorityList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused removal", before, after)
	}
}

// A vocabulary of none is not a state any project may reach: a task has to have
// a priority to be at, so the last one cannot be removed however the caller
// phrases it.
func TestPriorityDeleteRefusesTheLastPriority(t *testing.T) {
	repository := initializedRepository(t)
	cliPriorityDelete(t, repository, "high", "--into", "medium", "--no-sync", "--json")
	cliPriorityDelete(t, repository, "low", "--into", "medium", "--no-sync", "--json")
	if got, want := cliPriorityNames(t, repository), []string{"medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	before := cliPriorityList(t, repository).Head

	code, stdout, stderr := run(t, repository, "priority", "delete", "medium", "--into", "medium", "--json")
	if code != 5 {
		t.Fatalf("priority delete of the last priority = code %d, want 5; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("priority delete stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`priority delete cannot remove "medium"; it is this project's only priority, and every task has to be `+
			"at one; add another first: workbook priority add <priority>")

	if after := cliPriorityList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused removal", before, after)
	}
	if got, want := cliPriorityNames(t, repository), []string{"medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
}
