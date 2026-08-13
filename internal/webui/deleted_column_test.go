package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The deleted tasks are a column of the board rather than a page of their own.
// What that costs the server is one more answer to the question /api/tasks
// already asks — which tasks — and what it costs the page is a stylesheet that
// mutes the column without sizing it. Both are asserted here against the served
// artifact; what the client does with them is deleted_column_client_test.go's
// subject.

// deletedColumnTasks is one active task and two deleted ones, tombstoned a
// minute apart so a test can state the order the column draws them in.
func deletedColumnTasks() []core.Task {
	stamp := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	active := core.Task{
		ID:        "WB-01J00000000000000000000101",
		ProjectID: "01J00000000000000000000000",
		TaskData: core.TaskData{
			Title:     "Still here",
			Status:    core.StatusReady,
			Priority:  core.PriorityMedium,
			Rank:      "1/1",
			Labels:    []string{},
			CreatedAt: stamp,
			UpdatedAt: stamp,
		},
		Head: "head-active",
	}
	older := active
	older.ID = "WB-01J00000000000000000000102"
	older.Title = "Deleted earlier"
	older.Deleted = true
	older.UpdatedAt = stamp.Add(time.Minute)
	older.Head = "head-older"
	newer := active
	newer.ID = "WB-01J00000000000000000000103"
	newer.Title = "Deleted later"
	newer.Deleted = true
	newer.UpdatedAt = stamp.Add(2 * time.Minute)
	newer.Head = "head-newer"
	return []core.Task{active, older, newer}
}

func decodeTasksDocument(t *testing.T, handler http.Handler, path string) TasksDocument {
	t.Helper()
	response := request(t, handler, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d; body = %s", path, response.Code, http.StatusOK, response.Body.String())
	}
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode %s: %v; body = %s", path, err, response.Body.String())
	}
	return document
}

func documentTaskIDs(document TasksDocument) []string {
	ids := make([]string, len(document.Tasks))
	for index, task := range document.Tasks {
		ids[index] = task.ID
	}
	return ids
}

// The poll gains a third answer and loses none. `include` is the whole board in
// one document, because the column is fed by the same poll as every other
// column and two reads could only disagree about the moment they took.
func TestHandlerServesActiveAndDeletedTasksTogether(t *testing.T) {
	tasks := deletedColumnTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	document := decodeTasksDocument(t, handler, "/api/tasks?deleted=include")
	got := documentTaskIDs(document)
	want := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("include-mode tasks = %v, want %v", got, want)
	}
	deletedFlags := map[string]bool{}
	for _, task := range document.Tasks {
		deletedFlags[task.ID] = task.Deleted
	}
	if deletedFlags[tasks[0].ID] || !deletedFlags[tasks[1].ID] || !deletedFlags[tasks[2].ID] {
		t.Errorf("include-mode tasks do not carry the flag that tells them apart: %v", deletedFlags)
	}
	// The client refuses a document whose presentation does not cover its tasks,
	// so a mode that served views for only half of them would be unusable.
	if len(document.Presentation) != len(document.Tasks) {
		t.Fatalf("include-mode presentation covers %d of %d tasks", len(document.Presentation), len(document.Tasks))
	}
	covered := map[string]bool{}
	for _, view := range document.Presentation {
		covered[view.TaskID] = true
	}
	for _, task := range document.Tasks {
		if !covered[task.ID] {
			t.Errorf("include-mode presentation names no view for %s", task.ID)
		}
	}
}

// The relationship picker still asks `deleted=true` and the board still asks
// for nothing, so neither answer may move under them.
func TestHandlerLeavesTheExistingDeletedFilterUnchanged(t *testing.T) {
	tasks := deletedColumnTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	for path, want := range map[string][]string{
		"/api/tasks":               {tasks[0].ID},
		"/api/tasks?deleted=true":  {tasks[1].ID, tasks[2].ID},
		"/api/tasks?deleted=1":     {tasks[0].ID},
		"/api/tasks?deleted=":      {tasks[0].ID},
		"/api/tasks?deleted=false": {tasks[0].ID},
	} {
		got := documentTaskIDs(decodeTasksDocument(t, handler, path))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("GET %s tasks = %v, want %v", path, got, want)
		}
	}
}

// The column is a track of the board grid and takes no sizing of its own, which
// is what lets it join a project with five statuses and a project with twelve
// without a rule that could be wrong for either. What it does carry is colour,
// and every one of those rules is equal in specificity to the .column rule it
// mutes — so this pins the order they are written in, which is the only thing
// deciding which of them wins.
func TestHandlerStylesTheDeletedColumnAfterTheRulesItOverrides(t *testing.T) {
	body := boardPage(t)

	for _, selector := range []string{
		".column--deleted",
		".column--deleted .column__header",
		".column--deleted .task-card",
		".column__empty",
		".restore-button",
	} {
		if cssRule(t, body, selector) == "" {
			t.Errorf("the page has no %s rule", selector)
		}
	}
	base := strings.Index(body, ".column { ")
	muted := strings.Index(body, ".column--deleted { ")
	if base < 0 || muted < 0 {
		t.Fatalf("the page does not carry both the column rule and its muted variant: %d/%d", base, muted)
	}
	if muted < base {
		t.Error("the muted column rule is written before the .column rule it overrides, where it loses on order")
	}
	// No width, no minimum, no maximum: the column takes the board's track
	// sizing exactly as every other column does, so there is no sizing rule here
	// that could lose to one written above it.
	rule := cssRule(t, body, ".column--deleted")
	for _, sized := range []string{"width", "min-width", "max-width", "flex", "grid-column"} {
		if strings.Contains(rule, sized+":") {
			t.Errorf("the deleted column sizes itself with %s, which the board's track flow already decides: %s", sized, rule)
		}
	}
	// The empty state is drawn or not drawn by the hidden attribute alone, so a
	// display in its own rule would silently outrank it.
	if empty := cssRule(t, body, ".column__empty"); strings.Contains(empty, "display:") {
		t.Errorf("the empty state declares a display, which would outrank the hidden attribute: %s", empty)
	}
	if hidden := cssRule(t, body, ".column__empty[hidden]"); !strings.Contains(hidden, "display: none") {
		t.Errorf("the empty state is not hidden by its attribute: %s", hidden)
	}
	// The stylesheet the deleted-tasks page used is gone with the page.
	for _, stale := range []string{".deleted-list", ".deleted-card"} {
		if strings.Contains(body, stale) {
			t.Errorf("the page still carries the removed deleted-page rule %q", stale)
		}
	}
}
