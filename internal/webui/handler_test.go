package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

const contentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

func TestHandlerServesBoardTasksAndHealth(t *testing.T) {
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	board := request(t, handler, http.MethodGet, "/")
	if board.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", board.Code, http.StatusOK, board.Body.String())
	}
	assertSecurityHeaders(t, board.Result())
	if got := board.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html; charset=utf-8") {
		t.Fatalf("GET / Content-Type = %q, want text/html", got)
	}
	for _, fragment := range []string{
		`data-status="backlog"`,
		`data-status="ready"`,
		`data-status="in-progress"`,
		`data-status="blocked"`,
		`data-status="done"`,
		"Ready task",
		"Task refresh failed",
		"1 of 2 prerequisites complete",
		"Waiting on dependencies",
	} {
		if !strings.Contains(board.Body.String(), fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
	for _, fragment := range []string{
		`data-status="unknown"`,
		"Unrecognized status",
		"Future status task",
	} {
		if strings.Contains(board.Body.String(), fragment) {
			t.Errorf("GET / body unexpectedly contains %q", fragment)
		}
	}

	tasksResponse := request(t, handler, http.MethodGet, "/api/tasks")
	if tasksResponse.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d; body = %s", tasksResponse.Code, http.StatusOK, tasksResponse.Body.String())
	}
	assertSecurityHeaders(t, tasksResponse.Result())
	if got := tasksResponse.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("GET /api/tasks Content-Type = %q, want application/json", got)
	}
	var document TasksDocument
	if err := json.Unmarshal(tasksResponse.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode task document: %v; body = %s", err, tasksResponse.Body.String())
	}
	if document.Format != "workbook.tasks" || document.Version != 1 {
		t.Fatalf("task document envelope = %#v, want workbook.tasks v1", document)
	}
	if !reflect.DeepEqual(document.Tasks, tasks) {
		t.Fatalf("task document tasks = %#v, want %#v", document.Tasks, tasks)
	}
	if got := document.Presentation[0]; got.DependenciesComplete != 1 ||
		got.DependenciesTotal != 2 || !got.WaitingOnDependencies {
		t.Fatalf("task presentation = %#v, want 1/2 waiting", got)
	}
	if strings.Count(board.Body.String(), "prerequisites complete") != 1 {
		t.Fatal("dependency-free cards changed their rendered content")
	}

	health := request(t, handler, http.MethodGet, "/healthz")
	if health.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d; body = %s", health.Code, http.StatusOK, health.Body.String())
	}
	assertSecurityHeaders(t, health.Result())
	if got, want := strings.TrimSpace(health.Body.String()), `{"format":"workbook.health","version":1,"status":"ok"}`; got != want {
		t.Fatalf("GET /healthz body = %q, want %q", got, want)
	}
}

func TestHandlerReturnsMutationWarningAfterDurableWrite(t *testing.T) {
	result := core.MutationResult{
		Task: core.Task{
			ID: "WB-01K0M6B8A4FTT8C39MXXYTW7D1",
			TaskData: core.TaskData{
				Title:    "Durable",
				Status:   core.StatusReady,
				Priority: core.PriorityHigh,
			},
		},
		Warnings: []core.Warning{{
			Code:    core.WarningProjectionUpdate,
			Message: "cache update failed",
		}},
	}
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return nil, nil },
		func(context.Context, core.CreateInput) (core.MutationResult, error) {
			return result, nil
		},
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
	)

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"Durable","status":"ready","priority":"high"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var document struct {
		Format   string         `json:"format"`
		Version  int            `json:"version"`
		Task     core.Task      `json:"task"`
		Warnings []core.Warning `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.task-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want workbook.task-mutation v1", document)
	}
	if !reflect.DeepEqual(document.Task, result.Task) {
		t.Fatalf("mutation task = %#v, want %#v", document.Task, result.Task)
	}
	if !reflect.DeepEqual(document.Warnings, result.Warnings) {
		t.Fatalf("mutation warnings = %#v, want %#v", document.Warnings, result.Warnings)
	}
}

func TestHandlerRendersInReviewTasks(t *testing.T) {
	tasks := boardTasks()
	tasks = append(tasks, core.Task{
		ID: "WB-01J00000000000000000000007",
		TaskData: core.TaskData{
			Title:    "Review task",
			Status:   core.StatusInReview,
			Priority: core.PriorityMedium,
		},
	})
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	for _, fragment := range []string{"In Review", `data-status="in-review"`, "Review task"} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
}

func TestHandlerDeletesRestoresAndListsTombstonedTasks(t *testing.T) {
	active := boardTasks()[0]
	deleted := boardTasks()[1]
	deleted.Deleted = true
	var deletedID, restoredID string
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return []core.Task{active, deleted}, nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t),
		func(_ context.Context, id string) (core.MutationResult, error) {
			deletedID = id
			return core.MutationResult{Task: deleted}, nil
		},
		func(_ context.Context, id string) (core.MutationResult, error) {
			restoredID = id
			deleted.Deleted = false
			return core.MutationResult{Task: deleted}, nil
		},
		nil,
		nil,
	)

	deletedResponse := request(t, handler, http.MethodGet, "/api/tasks?deleted=true")
	if deletedResponse.Code != http.StatusOK {
		t.Fatalf("GET deleted tasks status = %d, want %d", deletedResponse.Code, http.StatusOK)
	}
	var listed TasksDocument
	if err := json.Unmarshal(deletedResponse.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode deleted tasks: %v", err)
	}
	if got, want := listed.Tasks, []core.Task{deleted}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted tasks = %#v, want %#v", got, want)
	}

	deleteResponse := request(t, handler, http.MethodDelete, "/api/tasks/"+active.ID)
	if deleteResponse.Code != http.StatusOK || deletedID != active.ID {
		t.Fatalf("DELETE task status/id = %d/%q, want %d/%q", deleteResponse.Code, deletedID, http.StatusOK, active.ID)
	}
	restoreResponse := request(t, handler, http.MethodPost, "/api/tasks/"+deleted.ID+"/restore")
	if restoreResponse.Code != http.StatusOK || restoredID != deleted.ID {
		t.Fatalf("POST restore status/id = %d/%q, want %d/%q", restoreResponse.Code, restoredID, http.StatusOK, deleted.ID)
	}
}

func TestHandlerAddsAndRemovesTaskDependencies(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	var calls []string
	warning := core.Warning{Code: core.WarningProjectionUpdate, Message: "cache update failed"}
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t), nil, nil,
		func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			calls = append(calls, "add:"+id+":"+dependency)
			return core.MutationResult{Task: dependent, Warnings: []core.Warning{warning}}, nil
		},
		func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			calls = append(calls, "remove:"+id+":"+dependency)
			return core.MutationResult{Task: dependent}, nil
		},
	)
	path := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID

	add := request(t, handler, http.MethodPut, path)
	if add.Code != http.StatusOK {
		t.Fatalf("PUT dependency status = %d; body = %s", add.Code, add.Body.String())
	}
	var addDocument TaskMutationDocument
	if err := json.Unmarshal(add.Body.Bytes(), &addDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(addDocument.Warnings, []core.Warning{warning}) {
		t.Fatalf("PUT warnings = %#v, want warning", addDocument.Warnings)
	}

	remove := request(t, handler, http.MethodDelete, path)
	if remove.Code != http.StatusOK {
		t.Fatalf("DELETE dependency status = %d; body = %s", remove.Code, remove.Body.String())
	}
	wantCalls := []string{
		"add:" + dependent.ID + ":" + prerequisite.ID,
		"remove:" + dependent.ID + ":" + prerequisite.ID,
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("dependency callbacks = %#v, want %#v", calls, wantCalls)
	}
}

func TestHandlerReturnsDependencyMutationErrors(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t), nil, nil,
		func(context.Context, string, string) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "dependency would create a cycle")
		},
		nil,
	)
	response := request(t, handler, http.MethodPut, "/api/tasks/"+dependent.ID+"/dependencies/"+prerequisite.ID)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PUT dependency error status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode dependency error: %v", err)
	}
	if document.Format != "workbook.error" || document.Version != 1 ||
		document.Error.Category != core.CategoryValidation || document.Error.Message != "dependency would create a cycle" {
		t.Fatalf("dependency error document = %#v", document)
	}
}

func TestHandlerRejectsWrongDependencyMethodsAndMalformedPaths(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t), nil, nil, nil, nil,
	)
	path := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID
	response := request(t, handler, http.MethodPost, path)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "PUT, DELETE" {
		t.Fatalf("POST dependency = %d Allow %q, want %d and %q", response.Code, response.Header().Get("Allow"), http.StatusMethodNotAllowed, "PUT, DELETE")
	}
	for _, malformed := range []string{
		"/api/tasks/" + dependent.ID + "/dependencies",
		"/api/tasks/" + dependent.ID + "/dependencies/",
		path + "/extra",
	} {
		response := request(t, handler, http.MethodPut, malformed)
		if response.Code != http.StatusNotFound {
			t.Errorf("PUT %s status = %d, want %d", malformed, response.Code, http.StatusNotFound)
		}
	}
}

func TestHandlerServesTaskRouteShell(t *testing.T) {
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	for _, path := range []string{
		"/tasks/new",
		"/tasks/" + tasks[0].ID,
	} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d; body = %s", path, response.Code, http.StatusOK, response.Body.String())
			continue
		}
		assertSecurityHeaders(t, response.Result())
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html; charset=utf-8") {
			t.Errorf("GET %s Content-Type = %q, want text/html", path, got)
		}
		if !strings.Contains(response.Body.String(), "Workbook board") {
			t.Errorf("GET %s body does not contain the application shell", path)
		}

		wrongMethod := request(t, handler, http.MethodPost, path)
		if wrongMethod.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, wrongMethod.Code, http.StatusMethodNotAllowed)
		}
		if got := wrongMethod.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("POST %s Allow = %q, want %q", path, got, http.MethodGet)
		}
	}
}

func TestHandlerServesDeletedRouteAndHeaderNavigation(t *testing.T) {
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return boardTasks(), nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))
	for _, path := range []string{"/", "/deleted"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		if !strings.Contains(response.Body.String(), `href="/deleted"`) {
			t.Fatalf("GET %s does not provide header navigation to deleted tasks", path)
		}
		if !strings.Contains(response.Body.String(), `href="/"`) {
			t.Fatalf("GET %s does not provide header navigation to the board", path)
		}
	}
}

func TestHandlerRendersTaskAndNewTaskLinks(t *testing.T) {
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	for _, definition := range core.WorkflowStatuses() {
		want := `href="/tasks/new?status=` + string(definition.Status) + `"`
		if !strings.Contains(body, want) {
			t.Errorf("GET / body does not contain canonical %q New Task link %q", definition.Label, want)
		}
	}
	for _, task := range tasks[:2] {
		want := `href="/tasks/` + task.ID + `">` + task.Title + `</a>`
		if !strings.Contains(body, want) {
			t.Errorf("GET / body does not contain full-ID task link %q", want)
		}
	}
}

func TestHandlerRequiresCanonicalStatusChoiceForUnknownTask(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	tasks := boardTasks()
	unknown := tasks[2]
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+unknown.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<unknown-status-task> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:  "workbook.tasks",
		Version: 1,
		Tasks:   []core.Task{unknown},
		Presentation: []TaskPresentation{{
			TaskID:   unknown.ID,
			IDPrefix: unknown.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+unknown.ID, string(document)) + script + `
setTimeout(() => {
  const status = findElement(main, (element) => element.id === "task-status");
  if (!status) throw new Error("detail form did not render a status control");
  if (status.value !== "") throw new Error("unknown status defaulted to " + JSON.stringify(status.value) + ", want an explicit empty choice");
  if (!status.required) throw new Error("unknown status choice is not required");
  if (!status.firstElementChild || !status.firstElementChild.textContent.includes("future-status")) {
    throw new Error("unknown current status is not visible in the status control");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for unknown status: %v\n%s", err, output)
	}
}

func TestHandlerShowsRecoverableErrorWhenInitialTaskLoadFails(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	task := boardTasks()[0]
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	recoveredDocument, err := json.Marshal(TasksDocument{
		Format:  "workbook.tasks",
		Version: 1,
		Tasks:   []core.Task{task},
		Presentation: []TaskPresentation{{
			TaskID:   task.ID,
			IDPrefix: task.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+task.ID, `{}`) + script + `
setTimeout(async () => {
  const errorHeading = findElement(main, (element) => element.textContent === "Unable to load task");
  if (!errorHeading) throw new Error("initial task load failure did not replace the loading route with a visible error");
  const retry = findElement(main, (element) => element.tagName === "BUTTON" && element.textContent === "Retry");
  if (!retry || !retry.eventListeners.click) throw new Error("task load error did not render an executable Retry control");
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  if (!back || back.href !== "/") throw new Error("task load error did not retain a Back link");

  taskResponse = ` + string(recoveredDocument) + `;
  await retry.eventListeners.click();
  const title = findElement(main, (element) => element.id === "task-title");
  if (!title || title.value !== ` + strconv.Quote(task.Title) + `) {
    throw new Error("retry did not recover the task detail form");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for initial load recovery: %v\n%s", err, output)
	}
}

func TestHandlerClientRendersDependencyRelationships(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000031", "Current task", core.StatusReady, core.PriorityMedium)
	activeDependency := clientPlacementTask("WB-01J00000000000000000000032", "Active prerequisite", core.StatusDone, core.PriorityHigh)
	activeBlocked := clientPlacementTask("WB-01J00000000000000000000033", "Active blocked task", core.StatusBlocked, core.PriorityLow)
	deletedDependency := clientPlacementTask("WB-01J00000000000000000000034", "Deleted prerequisite", core.StatusReady, core.PriorityMedium)
	deletedDependency.Deleted = true
	missingDependencyID := "WB-01J00000000000000000000036"
	current.Dependencies = []string{activeDependency.ID, deletedDependency.ID, missingDependencyID}
	activeBlocked.Dependencies = []string{current.ID}
	activeTasks := []core.Task{current, activeDependency, activeBlocked}
	deletedTasks := []core.Task{deletedDependency}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return activeTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	activeDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: activeTasks, Presentation: presentationForTasks(activeTasks)})
	if err != nil {
		t.Fatal(err)
	}
	deletedDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: deletedTasks})
	if err != nil {
		t.Fatal(err)
	}
	deletedBlockedAfterPoll := activeBlocked
	deletedBlockedAfterPoll.Deleted = true
	deletedAfterPollDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{deletedDependency, deletedBlockedAfterPoll}})
	if err != nil {
		t.Fatal(err)
	}
	refreshedCurrent := current
	refreshedCurrent.Dependencies = []string{activeDependency.ID}
	refreshedDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{refreshedCurrent, activeDependency}, Presentation: presentationForTasks([]core.Task{refreshedCurrent, activeDependency})})
	if err != nil {
		t.Fatal(err)
	}
	activeAfterDeletedFetchFailure := refreshedCurrent
	activeAfterDeletedFetchFailure.Dependencies = []string{}
	deletedFetchFailureDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{activeAfterDeletedFetchFailure, activeDependency}, Presentation: presentationForTasks([]core.Task{activeAfterDeletedFetchFailure, activeDependency})})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(activeDocument)) + script + `
deletedTaskResponse = ` + string(deletedDocument) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const blocksHeading = findElement(main, (element) => element.textContent === "Blocks");
  if (!dependsHeading || !blocksHeading) throw new Error("both relationship groups did not render");

  const activeDependencyLink = findElement(main, (element) =>
    element.tagName === "A" && element.href === "/tasks/" + encodeURIComponent(` + strconv.Quote(activeDependency.ID) + `));
  if (!activeDependencyLink) throw new Error("active prerequisite was not linked");

  const unavailable = findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(missingDependencyID) + `);
  if (!unavailable || !findElement(unavailable, (element) => element.textContent === "Unavailable task")) {
    throw new Error("missing prerequisite fallback did not render");
  }

  const deletedDependency = findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(deletedDependency.ID) + `);
  if (!deletedDependency || !deletedDependency.textContent.includes("Deleted") ||
      !findElement(deletedDependency, (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("tombstoned prerequisite was not rendered removable");
  }
  const activeBlock = findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(activeBlocked.ID) + `);
  if (!activeBlock || !findElement(activeBlock, (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("active blocked task was not rendered removable");
  }

  const title = findElement(main, (element) => element.id === "task-title");
  if (!title) throw new Error("detail title field did not render");
  title.value = "Unsaved title";
  taskResponse = ` + string(refreshedDocument) + `;
  let resolveStaleDeleted;
  deletedTaskResponse = new Promise((resolve) => { resolveStaleDeleted = resolve; });
  const stalePoll = intervalCallback();
  await new Promise((resolve) => setTimeout(resolve, 0));
  deletedTaskResponse = ` + string(deletedAfterPollDocument) + `;
  await intervalCallback();
  if (title.value !== "Unsaved title") throw new Error("relationship refresh reconstructed the task form");
  if (findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(deletedDependency.ID) + `) ||
      findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(missingDependencyID) + `)) {
    throw new Error("relationship rows did not follow refreshed canonical state");
  }
  const deletedBlock = findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(activeBlocked.ID) + `);
  if (!deletedBlock || !deletedBlock.textContent.includes("Deleted") ||
      findElement(deletedBlock, (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("poll did not keep a later tombstoned blocked task read-only");
  }
  resolveStaleDeleted(` + string(deletedDocument) + `);
  await stalePoll;
  const blockAfterStaleResponse = findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(activeBlocked.ID) + `);
  if (!blockAfterStaleResponse || !blockAfterStaleResponse.textContent.includes("Deleted") ||
      findElement(blockAfterStaleResponse, (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("stale deleted response regressed the latest blocked-task state");
  }

  taskResponse = ` + string(deletedFetchFailureDocument) + `;
  deletedTaskResponse = Promise.reject(new Error("deleted task data unavailable"));
  await intervalCallback();
  if (title.value !== "Unsaved title") throw new Error("deleted-context failure reconstructed the task form");
  if (findElement(main, (element) => element.dataset.relationshipId === ` + strconv.Quote(activeDependency.ID) + `)) {
    throw new Error("deleted-context failure did not render the latest active relationship state");
  }
  const localError = findElement(main, (element) => element.textContent === "deleted task data unavailable");
  if (!localError) throw new Error("deleted-context failure did not render a relationship-local error");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered dependency relationships: %v\n%s", err, output)
	}
}

func TestHandlerClientFiltersDependencyComboboxCandidates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000041", "Current task", core.StatusReady, core.PriorityMedium)
	existingDependency := clientPlacementTask("WB-01J00000000000000000000042", "Existing prerequisite", core.StatusDone, core.PriorityHigh)
	alreadyBlocked := clientPlacementTask("WB-01J00000000000000000000043", "Already blocked", core.StatusBlocked, core.PriorityLow)
	firstCandidate := clientPlacementTask("WB-01J00000000000000000000044", "First candidate", core.StatusBacklog, core.PriorityHigh)
	secondCandidate := clientPlacementTask("WB-01J00000000000000000000045", "Second candidate", core.StatusInProgress, core.PriorityMedium)
	deletedCandidate := clientPlacementTask("WB-01J00000000000000000000046", "Deleted candidate", core.StatusReady, core.PriorityLow)
	current.Dependencies = []string{existingDependency.ID}
	alreadyBlocked.Dependencies = []string{current.ID}
	deletedCandidate.Deleted = true
	tasks := []core.Task{current, existingDependency, alreadyBlocked, firstCandidate, secondCandidate, deletedCandidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + `
setTimeout(() => {
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const blocksHeading = findElement(main, (element) => element.textContent === "Blocks");
  const dependsInput = dependsHeading && findElement(dependsHeading.parentElement, (element) => element.tagName === "INPUT");
  const blocksInput = blocksHeading && findElement(blocksHeading.parentElement, (element) => element.tagName === "INPUT");
  if (!dependsInput || dependsInput.attributes.role !== "combobox" ||
      dependsInput.attributes["aria-autocomplete"] !== "list" ||
      !dependsInput.attributes["aria-controls"]) {
    throw new Error("Depends On input does not expose the combobox contract");
  }
  if (!blocksInput || blocksInput.attributes.role !== "combobox" ||
      blocksInput.attributes["aria-autocomplete"] !== "list" ||
      !blocksInput.attributes["aria-controls"]) {
    throw new Error("Blocks input does not expose the combobox contract");
  }

  const assertOptions = (heading, wantIDs, label) => {
    const options = heading.parentElement.querySelectorAll('[role="option"]');
    const gotIDs = options.map((option) => option.dataset.candidateId);
    if (JSON.stringify(gotIDs) !== JSON.stringify(wantIDs)) {
      throw new Error(label + " candidates = " + JSON.stringify(gotIDs) + ", want " + JSON.stringify(wantIDs));
    }
    const ids = new Set();
    options.forEach((option) => {
      const task = taskDocument.tasks.find((candidate) => candidate.id === option.dataset.candidateId);
      if (!option.id || ids.has(option.id)) throw new Error(label + " option IDs are not stable and unique");
      ids.add(option.id);
      if (option.attributes.role !== "option" || option.attributes["aria-selected"] !== "false") {
        throw new Error(label + " option does not expose selection semantics");
      }
      if (!task || !option.textContent.includes(task.title) || !option.textContent.includes(task.status) ||
          !option.textContent.includes(task.priority) || !option.textContent.includes(task.id)) {
        throw new Error(label + " option does not show title, status, priority, and full ID");
      }
    });
  };

  assertOptions(dependsHeading, [
    ` + strconv.Quote(alreadyBlocked.ID) + `,
    ` + strconv.Quote(firstCandidate.ID) + `,
    ` + strconv.Quote(secondCandidate.ID) + `
  ], "Depends On");
  assertOptions(blocksHeading, [
    ` + strconv.Quote(existingDependency.ID) + `,
    ` + strconv.Quote(firstCandidate.ID) + `,
    ` + strconv.Quote(secondCandidate.ID) + `
  ], "Blocks");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency combobox candidate filtering: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencySnapshotPrefersTombstones(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000047", "Current task", core.StatusReady, core.PriorityMedium)
	activeDependency := clientPlacementTask("WB-01J00000000000000000000048", "Active prerequisite copy", core.StatusDone, core.PriorityHigh)
	activeBlocked := clientPlacementTask("WB-01J00000000000000000000049", "Active blocked copy", core.StatusBlocked, core.PriorityLow)
	activeCandidate := clientPlacementTask("WB-01J0000000000000000000004A", "Active candidate copy", core.StatusBacklog, core.PriorityMedium)
	current.Dependencies = []string{activeDependency.ID}
	activeBlocked.Dependencies = []string{current.ID}
	activeTasks := []core.Task{current, activeDependency, activeBlocked, activeCandidate}
	deletedDependency := activeDependency
	deletedDependency.Title = "Deleted prerequisite copy"
	deletedDependency.Deleted = true
	deletedBlocked := activeBlocked
	deletedBlocked.Title = "Deleted blocked copy"
	deletedBlocked.Deleted = true
	deletedCandidate := activeCandidate
	deletedCandidate.Title = "Deleted candidate copy"
	deletedCandidate.Deleted = true
	deletedTasks := []core.Task{deletedDependency, deletedBlocked, deletedCandidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return activeTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	activeDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: activeTasks, Presentation: presentationForTasks(activeTasks)})
	if err != nil {
		t.Fatal(err)
	}
	deletedDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: deletedTasks})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(activeDocument)) + script + `
deletedTaskResponse = ` + string(deletedDocument) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const blocksHeading = findElement(main, (element) => element.textContent === "Blocks");
  const dependsGroup = dependsHeading && dependsHeading.parentElement;
  const blocksGroup = blocksHeading && blocksHeading.parentElement;
  const candidateID = ` + strconv.Quote(activeCandidate.ID) + `;
  if (findElement(dependsGroup, (element) => element.attributes.role === "option" && element.dataset.candidateId === candidateID) ||
      findElement(blocksGroup, (element) => element.attributes.role === "option" && element.dataset.candidateId === candidateID)) {
    throw new Error("tombstoned ID remained an editable relationship candidate");
  }

  const dependsRows = dependsGroup.querySelectorAll("[data-relationship-row]")
    .filter((row) => row.dataset.relationshipId === ` + strconv.Quote(activeDependency.ID) + `);
  if (dependsRows.length !== 1 || !dependsRows[0].textContent.includes("Deleted") ||
      !findElement(dependsRows[0], (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("tombstoned Depends On ID was not rendered once and removable");
  }

  const blocksRows = blocksGroup.querySelectorAll("[data-relationship-row]")
    .filter((row) => row.dataset.relationshipId === ` + strconv.Quote(activeBlocked.ID) + `);
  if (blocksRows.length !== 1 || !blocksRows[0].textContent.includes("Deleted") ||
      findElement(blocksRows[0], (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
    throw new Error("tombstoned Blocks ID was not rendered once and read-only");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute conflicting relationship snapshot behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyComboboxSelectionCollapseIsCoherent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J0000000000000000000004B", "Current task", core.StatusReady, core.PriorityMedium)
	pointerCandidate := clientPlacementTask("WB-01J0000000000000000000004C", "Pointer candidate", core.StatusDone, core.PriorityHigh)
	keyboardCandidate := clientPlacementTask("WB-01J0000000000000000000004D", "Keyboard candidate", core.StatusBacklog, core.PriorityLow)
	tasks := []core.Task{current, pointerCandidate, keyboardCandidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + `
setTimeout(() => {
  const assertCollapsedSelection = (group, input, candidate, label) => {
    const listbox = findElement(group, (element) => element.attributes.role === "listbox");
    const selected = findElement(group, (element) =>
      element.attributes.role === "option" && element.attributes["aria-selected"] === "true");
    const add = findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
    if (input.value !== candidate.title) {
      throw new Error(label + " did not expose the accepted task as the combobox value");
    }
    if (input.attributes["aria-expanded"] !== "false" || !listbox.hidden ||
        Object.prototype.hasOwnProperty.call(input.attributes, "aria-activedescendant")) {
      throw new Error(label + " did not collapse without an active descendant");
    }
    if (!selected || selected.dataset.candidateId !== candidate.id || add.disabled) {
      throw new Error(label + " did not retain selection semantics and an enabled Add button");
    }
  };

  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading.parentElement;
  const dependsInput = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  dependsInput.eventListeners.focus();
  const pointerOption = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(pointerCandidate.ID) + `);
  pointerOption.eventListeners.click();
  assertCollapsedSelection(dependsGroup, dependsInput, taskDocument.tasks[1], "pointer selection from an empty query");

  const blocksHeading = findElement(main, (element) => element.textContent === "Blocks");
  const blocksGroup = blocksHeading.parentElement;
  const blocksInput = findElement(blocksGroup, (element) => element.attributes.role === "combobox");
  blocksInput.value = "Keyboard";
  blocksInput.eventListeners.input();
  blocksInput.eventListeners.keydown({ key: "ArrowDown", preventDefault() {} });
  blocksInput.eventListeners.keydown({ key: "Enter", preventDefault() {} });
  assertCollapsedSelection(blocksGroup, blocksInput, taskDocument.tasks[2], "keyboard selection");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute coherent dependency combobox collapse: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyComboboxScrollsKeyboardOptionIntoView(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J0000000000000000000004E", "Current task", core.StatusReady, core.PriorityMedium)
	first := clientPlacementTask("WB-01J0000000000000000000004F", "First candidate", core.StatusDone, core.PriorityHigh)
	second := clientPlacementTask("WB-01J0000000000000000000004G", "Second candidate", core.StatusBacklog, core.PriorityLow)
	third := clientPlacementTask("WB-01J0000000000000000000004H", "Third candidate", core.StatusBlocked, core.PriorityMedium)
	fourth := clientPlacementTask("WB-01J0000000000000000000004J", "Fourth candidate", core.StatusInProgress, core.PriorityHigh)
	tasks := []core.Task{current, first, second, third, fourth}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + `
setTimeout(() => {
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const input = findElement(dependsHeading.parentElement, (element) => element.attributes.role === "combobox");
  input.eventListeners.focus();
  for (let index = 0; index < 3; index += 1) {
    input.eventListeners.keydown({ key: "ArrowDown", preventDefault() {} });
  }
  if (scrollIntoViewCalls.length !== 3) {
    throw new Error("keyboard navigation scroll calls = " + scrollIntoViewCalls.length + ", want 3");
  }
  const last = scrollIntoViewCalls[2];
  if (last.element.dataset.candidateId !== ` + strconv.Quote(third.ID) + ` ||
      !last.options || last.options.block !== "nearest") {
    throw new Error("keyboard navigation did not scroll the third active option with block nearest");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency keyboard option scrolling: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationOrientationAndRefresh(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000051", "Current task", core.StatusReady, core.PriorityMedium)
	existingDependency := clientPlacementTask("WB-01J00000000000000000000052", "Existing prerequisite", core.StatusDone, core.PriorityHigh)
	existingBlocked := clientPlacementTask("WB-01J00000000000000000000053", "Existing blocked task", core.StatusBlocked, core.PriorityLow)
	dependsCandidate := clientPlacementTask("WB-01J00000000000000000000054", "New prerequisite", core.StatusBacklog, core.PriorityHigh)
	blocksCandidate := clientPlacementTask("WB-01J00000000000000000000055", "New blocked task", core.StatusInProgress, core.PriorityMedium)
	current.Dependencies = []string{existingDependency.ID}
	existingBlocked.Dependencies = []string{current.ID}
	initialTasks := []core.Task{current, existingDependency, existingBlocked, dependsCandidate, blocksCandidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	currentAfterAdd := current
	currentAfterAdd.Dependencies = []string{existingDependency.ID, dependsCandidate.ID}
	afterDependsAdd := []core.Task{currentAfterAdd, existingDependency, existingBlocked, dependsCandidate, blocksCandidate}
	newBlockedAfterAdd := blocksCandidate
	newBlockedAfterAdd.Dependencies = []string{current.ID}
	afterBlocksAdd := []core.Task{currentAfterAdd, existingDependency, existingBlocked, dependsCandidate, newBlockedAfterAdd}
	currentAfterRemove := currentAfterAdd
	currentAfterRemove.Dependencies = []string{dependsCandidate.ID}
	afterDependsRemove := []core.Task{currentAfterRemove, existingDependency, existingBlocked, dependsCandidate, newBlockedAfterAdd}
	existingBlockedAfterRemove := existingBlocked
	existingBlockedAfterRemove.Dependencies = []string{}
	afterBlocksRemove := []core.Task{currentAfterRemove, existingDependency, existingBlockedAfterRemove, dependsCandidate, newBlockedAfterAdd}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const selectByPointer = (group, candidateID) => {
    const option = findElement(group, (element) => element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option || !option.eventListeners.click) throw new Error("candidate option is not pointer selectable: " + candidateID);
    option.eventListeners.click();
    const add = findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
    if (!add || add.disabled || !add.eventListeners.click) throw new Error("selected candidate did not enable Add dependency");
    return add;
  };
  const mutationDocument = (task) => ({
    format: "workbook.task-mutation",
    version: 1,
    task
  });
  let nextMutation = mutationDocument(taskDocument.tasks[0]);
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return { ok: true, json: async () => nextMutation };
    }
    return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
  };
  fetchCalls.splice(0);

  let dependsGroup = groupFor("Depends On");
  nextMutation = mutationDocument(` + documentJSON(afterDependsAdd) + `.tasks[0]);
  taskResponse = ` + documentJSON(afterDependsAdd) + `;
  await selectByPointer(dependsGroup, ` + strconv.Quote(dependsCandidate.ID) + `).eventListeners.click();
  const dependsAdd = fetchCalls.find((call) =>
    call.options.method === "PUT" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(current.ID) + `) +
      "/dependencies/" + encodeURIComponent(` + strconv.Quote(dependsCandidate.ID) + `));
  if (!dependsAdd || Object.prototype.hasOwnProperty.call(dependsAdd.options, "body")) {
    throw new Error("Depends On add did not send a bodyless nested PUT");
  }

  let blocksGroup = groupFor("Blocks");
  nextMutation = mutationDocument(` + documentJSON(afterBlocksAdd) + `.tasks[4]);
  taskResponse = ` + documentJSON(afterBlocksAdd) + `;
  await selectByPointer(blocksGroup, ` + strconv.Quote(blocksCandidate.ID) + `).eventListeners.click();
  const blocksAdd = fetchCalls.find((call) =>
    call.options.method === "PUT" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(blocksCandidate.ID) + `) +
      "/dependencies/" + encodeURIComponent(` + strconv.Quote(current.ID) + `));
  if (!blocksAdd || Object.prototype.hasOwnProperty.call(blocksAdd.options, "body")) {
    throw new Error("Blocks add did not reverse the edge orientation with a bodyless request");
  }

  dependsGroup = groupFor("Depends On");
  const existingDependencyRow = findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingDependency.ID) + `);
  const dependsRemove = existingDependencyRow && findElement(existingDependencyRow, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!dependsRemove || !dependsRemove.eventListeners.click) throw new Error("active Depends On row is not removable");
  let resolveDependsRefresh;
  taskResponse = new Promise((resolve) => { resolveDependsRefresh = resolve; });
  nextMutation = mutationDocument(` + documentJSON(afterDependsRemove) + `.tasks[0]);
  const dependsRemovePromise = dependsRemove.eventListeners.click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (!findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingDependency.ID) + `)) {
    throw new Error("Depends On row changed before durable state was refreshed");
  }
  resolveDependsRefresh(` + documentJSON(afterDependsRemove) + `);
  await dependsRemovePromise;
  if (findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingDependency.ID) + `)) {
    throw new Error("Depends On row remained after refreshed state removed it");
  }
  const dependsDelete = fetchCalls.find((call) =>
    call.options.method === "DELETE" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(current.ID) + `) +
      "/dependencies/" + encodeURIComponent(` + strconv.Quote(existingDependency.ID) + `));
  if (!dependsDelete || Object.prototype.hasOwnProperty.call(dependsDelete.options, "body")) {
    throw new Error("Depends On remove did not send the mirrored bodyless nested DELETE");
  }

  blocksGroup = groupFor("Blocks");
  const existingBlockedRow = findElement(blocksGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingBlocked.ID) + `);
  const blocksRemove = existingBlockedRow && findElement(existingBlockedRow, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!blocksRemove || !blocksRemove.eventListeners.click) throw new Error("active Blocks row is not removable");
  let resolveBlocksRefresh;
  taskResponse = new Promise((resolve) => { resolveBlocksRefresh = resolve; });
  nextMutation = mutationDocument(` + documentJSON(afterBlocksRemove) + `.tasks[2]);
  const blocksRemovePromise = blocksRemove.eventListeners.click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (!findElement(blocksGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingBlocked.ID) + `)) {
    throw new Error("Blocks row changed before durable state was refreshed");
  }
  resolveBlocksRefresh(` + documentJSON(afterBlocksRemove) + `);
  await blocksRemovePromise;
  if (findElement(blocksGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(existingBlocked.ID) + `)) {
    throw new Error("Blocks row remained after refreshed state removed it");
  }
  const blocksDelete = fetchCalls.find((call) =>
    call.options.method === "DELETE" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(existingBlocked.ID) + `) +
      "/dependencies/" + encodeURIComponent(` + strconv.Quote(current.ID) + `));
  if (!blocksDelete || Object.prototype.hasOwnProperty.call(blocksDelete.options, "body")) {
    throw new Error("Blocks remove did not reverse the edge orientation with a bodyless nested DELETE");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency mutation orientation and refresh: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationFollowsSupersedingRefresh(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000056", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J00000000000000000000057", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading && dependsHeading.parentElement;
  const input = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  input.value = "Candidate";
  input.eventListeners.input();
  const option = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  option.eventListeners.click();
  const add = findElement(dependsGroup, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");

  let resolveSupersededRefresh;
  let activeGets = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(updatedTasks) + `.tasks[0]
        })
      };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => deletedTaskResponse };
    }
    activeGets += 1;
    if (activeGets === 1) {
      return {
        ok: true,
        json: async () => new Promise((resolve) => { resolveSupersededRefresh = resolve; })
      };
    }
    return { ok: true, json: async () => (` + documentJSON(updatedTasks) + `) };
  };
  fetchCalls.splice(0);

  const mutation = add.eventListeners.click();
  while (!resolveSupersededRefresh) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  await intervalCallback();
  const refreshedRow = findElement(dependsGroup, (element) =>
    element.dataset.relationshipId === ` + strconv.Quote(candidate.ID) + `);
  if (!refreshedRow) throw new Error("superseding refresh did not render the durable relationship");

  resolveSupersededRefresh(` + documentJSON(initialTasks) + `);
  await mutation;
  const falseFailure = findElement(dependsGroup, (element) =>
    element.textContent.includes("latest task state could not be refreshed"));
  if (falseFailure) throw new Error("superseded successful refresh was reported as a durable-refresh failure");
  if (input.value !== "" || !add.disabled) {
    throw new Error("superseded successful refresh did not apply PUT combobox clear semantics");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute superseding dependency refresh behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationSettlesAfterControllerSupersession(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J0000000000000000000005A", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005B", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading && dependsHeading.parentElement;
  const relationshipRegion = dependsGroup.parentElement;
  const controllerStatus = relationshipRegion.children.find((element) => element.className === "form-status");
  const input = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  input.eventListeners.focus();
  const option = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  option.eventListeners.click();
  const add = findElement(dependsGroup, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");

  let resolveDeletedContext;
  let activeGets = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(updatedTasks) + `.tasks[0]
        })
      };
    }
    if (url === "/api/tasks?deleted=true") {
      return {
        ok: true,
        json: async () => new Promise((resolve) => { resolveDeletedContext = resolve; })
      };
    }
    activeGets += 1;
    return { ok: true, json: async () => (` + documentJSON(updatedTasks) + `) };
  };
  fetchCalls.splice(0);

  const mutation = add.eventListeners.click();
  while (!resolveDeletedContext) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (activeGets !== 1) throw new Error("mutation did not start exactly one active refresh");
  controllerStatus.textContent = "detached controller remains untouched";

  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  documentEventListeners.click({
    target: back,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  if (main.firstElementChild !== boardView) throw new Error("navigation did not detach the relationship controller");

  resolveDeletedContext(` + string(emptyDeleted) + `);
  await mutation;
  if (input.disabled) throw new Error("settled mutation did not clear the initiating group busy state");
  if (controllerStatus.textContent !== "detached controller remains untouched") {
    throw new Error("superseded deleted context wrote to the detached controller");
  }
  const groupMessage = findElement(dependsGroup, (element) => element.className === "relationship-message");
  if (groupMessage.textContent !== "") {
    throw new Error("settled mutation wrote status into the detached relationship group");
  }
  if (activeGets !== 1) throw new Error("controller-only supersession started an unexpected task refresh");

  const newTask = new TestElement("a");
  newTask.href = "/tasks/new";
  boardView.append(newTask);
  documentEventListeners.click({
    target: newTask,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  if (!findElement(main, (element) => element.id === "task-title")) {
    throw new Error("client did not remain responsive after the mutation settled");
  }
}, 0);
`
	commandContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		if commandContext.Err() == context.DeadlineExceeded {
			t.Fatalf("dependency mutation did not settle after controller-only supersession")
		}
		t.Fatalf("execute controller-superseded dependency mutation: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationDoesNotWriteDetachedGroupAfterNewerPoll(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J0000000000000000000005C", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005D", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading.parentElement;
  const relationshipRegion = dependsGroup.parentElement;
  const controllerStatus = relationshipRegion.children.find((element) => element.className === "form-status");
  const groupMessage = findElement(dependsGroup, (element) => element.className === "relationship-message");
  const input = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  input.eventListeners.focus();
  const option = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  option.eventListeners.click();
  const add = findElement(dependsGroup, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");

  let resolveDeletedContext;
  let activeGets = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(updatedTasks) + `.tasks[0],
          warnings: [{ code: "projection-update-failed", message: "Detached warning must not render." }]
        })
      };
    }
    if (url === "/api/tasks?deleted=true") {
      return {
        ok: true,
        json: async () => new Promise((resolve) => { resolveDeletedContext = resolve; })
      };
    }
    activeGets += 1;
    return { ok: true, json: async () => (` + documentJSON(updatedTasks) + `) };
  };
  fetchCalls.splice(0);

  const mutation = add.eventListeners.click();
  while (!resolveDeletedContext) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  const selectedValue = input.value;
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  documentEventListeners.click({
    target: back,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  controllerStatus.textContent = "detached controller status";
  groupMessage.textContent = "detached group status";

  await intervalCallback();
  if (activeGets !== 2) throw new Error("newer polling refresh did not apply");
  resolveDeletedContext(` + string(emptyDeleted) + `);
  await mutation;

  if (input.disabled) throw new Error("detached successful mutation did not release busy state");
  if (input.value !== selectedValue) throw new Error("detached successful mutation cleared the old combobox");
  if (groupMessage.textContent !== "detached group status") {
    throw new Error("newer polling result wrote a warning into the detached group");
  }
  if (controllerStatus.textContent !== "detached controller status") {
    throw new Error("newer polling result wrote status into the detached controller");
  }
  if (main.firstElementChild !== boardView) throw new Error("newer polling result replaced the current route");

  const newTask = new TestElement("a");
  newTask.href = "/tasks/new";
  boardView.append(newTask);
  documentEventListeners.click({
    target: newTask,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  if (!findElement(main, (element) => element.id === "task-title")) {
    throw new Error("new route was not responsive after detached mutation completion");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute detached dependency mutation with newer poll: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationErrorDoesNotWriteDetachedGroup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J0000000000000000000005E", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005F", "Candidate task", core.StatusDone, core.PriorityHigh)
	tasks := []core.Task{current, candidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading.parentElement;
  const groupMessage = findElement(dependsGroup, (element) => element.className === "relationship-message");
  const input = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  input.eventListeners.focus();
  const option = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  option.eventListeners.click();
  const add = findElement(dependsGroup, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");

  let resolveMutationError;
  let activeGets = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return {
        ok: false,
        json: async () => new Promise((resolve) => { resolveMutationError = resolve; })
      };
    }
    activeGets += 1;
    return { ok: true, json: async () => taskDocument };
  };
  fetchCalls.splice(0);

  const mutation = add.eventListeners.click();
  while (!resolveMutationError) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  documentEventListeners.click({
    target: back,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  groupMessage.textContent = "detached group status";

  resolveMutationError({
    format: "workbook.error",
    version: 1,
    error: { category: "validation", message: "dependency would create a cycle" }
  });
  await mutation;

  if (activeGets !== 1) throw new Error("mutation-error recovery did not refresh global task state");
  if (input.disabled) throw new Error("detached failed mutation did not release busy state");
  if (groupMessage.textContent !== "detached group status") {
    throw new Error("mutation error wrote into the detached relationship group");
  }
  if (main.firstElementChild !== boardView) throw new Error("mutation-error recovery replaced the current route");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute detached dependency mutation error: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationReportsDeletedContextFailure(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000058", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J00000000000000000000059", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
  const dependsGroup = dependsHeading && dependsHeading.parentElement;
  const input = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  input.eventListeners.focus();
  const option = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  option.eventListeners.click();
  const add = findElement(dependsGroup, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");

  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(updatedTasks) + `.tasks[0],
          warnings: [{ code: "projection-update-failed", message: "Projection needs repair." }]
        })
      };
    }
    if (url === "/api/tasks?deleted=true") {
      return {
        ok: false,
        json: async () => ({
          format: "workbook.error",
          version: 1,
          error: { category: "internal", message: "Deleted task context is unavailable." }
        })
      };
    }
    return { ok: true, json: async () => (` + documentJSON(updatedTasks) + `) };
  };
  fetchCalls.splice(0);

  await add.eventListeners.click();
  const feedback = findElement(dependsGroup, (element) =>
    element.attributes.role === "status" &&
    element.textContent.includes("Dependency saved durably. Projection needs repair.") &&
    element.textContent.includes("Deleted task context is unavailable."));
  if (!feedback) {
    throw new Error("initiating group did not preserve the durable warning and deleted-context failure");
  }
  if (!findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(candidate.ID) + `)) {
    throw new Error("active relationship state did not render when deleted context failed");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute deleted-context dependency mutation feedback: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyFailureRecoveryAndKeyboard(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	current := clientPlacementTask("WB-01J00000000000000000000061", "Current task", core.StatusReady, core.PriorityMedium)
	alpha := clientPlacementTask("WB-01J00000000000000000000062", "Alpha prerequisite", core.StatusDone, core.PriorityHigh)
	beta := clientPlacementTask("WB-01J00000000000000000000063", "Beta blocked task", core.StatusBacklog, core.PriorityLow)
	initialTasks := []core.Task{current, alpha, beta}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return initialTasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}
	betaAfterAdd := beta
	betaAfterAdd.Dependencies = []string{current.ID}
	afterWarning := []core.Task{current, alpha, betaAfterAdd}

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const inputFor = (group) => findElement(group, (element) => element.tagName === "INPUT" && element.attributes.role === "combobox");
  const addFor = (group) => findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
  const dispatchKey = (input, key) => {
    let prevented = false;
    input.eventListeners.keydown({ key, preventDefault() { prevented = true; } });
    return prevented;
  };
  const selectedOption = (group) => findElement(group, (element) =>
    element.attributes.role === "option" && element.attributes["aria-selected"] === "true");

  let nextResponse = {
    ok: false,
    document: {
      format: "workbook.error",
      version: 1,
      error: { category: "validation", message: "dependency would create a cycle" }
    }
  };
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return { ok: nextResponse.ok, json: async () => nextResponse.document };
    }
    return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
  };
  fetchCalls.splice(0);

  const formTitle = findElement(main, (element) => element.id === "task-title");
  formTitle.value = "Unsaved task title";
  const dependsGroup = groupFor("Depends On");
  const dependsInput = inputFor(dependsGroup);
  dependsInput.value = "Alpha";
  dependsInput.eventListeners.input();
  if (!dispatchKey(dependsInput, "ArrowDown") || !dependsInput.attributes["aria-activedescendant"]) {
    throw new Error("Arrow Down did not activate a matching candidate");
  }
  if (!dispatchKey(dependsInput, "Enter")) throw new Error("Enter did not select the active candidate");
  const alphaOption = selectedOption(dependsGroup);
  const dependsAdd = addFor(dependsGroup);
  if (!alphaOption || alphaOption.dataset.candidateId !== ` + strconv.Quote(alpha.ID) + ` || dependsAdd.disabled) {
    throw new Error("keyboard selection did not select Alpha and enable Add dependency");
  }
  const alphaListbox = findElement(dependsGroup, (element) => element.attributes.role === "listbox");
  if (dependsInput.attributes["aria-expanded"] !== String(!alphaListbox.hidden)) {
    throw new Error("combobox aria-expanded state does not match the visible popup after selection");
  }

  await dependsAdd.eventListeners.click();
  const cycleMessage = findElement(dependsGroup, (element) =>
    element.attributes.role === "status" && element.textContent.includes("dependency would create a cycle"));
  if (!cycleMessage) throw new Error("cycle failure did not stay in the initiating live region");
  if (findElement(main, (element) => element.id === "task-title") !== formTitle ||
      formTitle.value !== "Unsaved task title") {
    throw new Error("dependency failure reconstructed or reset the unsaved task form");
  }
  const preservedSelection = selectedOption(dependsGroup);
  if (dependsInput.value !== ` + strconv.Quote(alpha.Title) + ` || !preservedSelection ||
      preservedSelection.dataset.candidateId !== ` + strconv.Quote(alpha.ID) + ` || dependsAdd.disabled) {
    throw new Error("dependency failure did not preserve the query and valid selection");
  }

  const blocksGroup = groupFor("Blocks");
  const blocksInput = inputFor(blocksGroup);
  blocksInput.value = "Beta";
  blocksInput.eventListeners.input();
  dispatchKey(blocksInput, "ArrowDown");
  dispatchKey(blocksInput, "Enter");
  const blocksAdd = addFor(blocksGroup);
  if (blocksAdd.disabled) throw new Error("Blocks keyboard selection did not enable Add dependency");
  nextResponse = {
    ok: true,
    document: {
      format: "workbook.task-mutation",
      version: 1,
      task: ` + documentJSON(afterWarning) + `.tasks[2],
      warnings: [{ code: "projection-update-failed", message: "The local projection needs repair." }]
    }
  };
  taskResponse = ` + documentJSON(afterWarning) + `;
  await blocksAdd.eventListeners.click();
  const warning = findElement(blocksGroup, (element) =>
    element.attributes.role === "status" &&
    element.textContent.includes("Dependency saved durably.") &&
    element.textContent.includes("The local projection needs repair."));
  if (!warning) throw new Error("durable projection warning did not stay in the initiating live region");
  await intervalCallback();
  if (warning.textContent !== "Dependency saved durably. The local projection needs repair.") {
    throw new Error("durable mutation warning did not persist through active and deleted refresh");
  }
  if (findElement(main, (element) => element.id === "task-title") !== formTitle ||
      formTitle.value !== "Unsaved task title") {
    throw new Error("successful relationship refresh reconstructed or reset the unsaved task form");
  }

  dependsInput.value = "Beta";
  dependsInput.eventListeners.input();
  dispatchKey(dependsInput, "ArrowDown");
  if (!dependsInput.attributes["aria-activedescendant"] || selectedOption(dependsGroup)) {
    throw new Error("Escape setup unexpectedly selected a candidate");
  }
  dispatchKey(dependsInput, "Escape");
  if (dependsInput.attributes["aria-expanded"] !== "false" ||
      Object.prototype.hasOwnProperty.call(dependsInput.attributes, "aria-activedescendant") ||
      selectedOption(dependsGroup)) {
    throw new Error("Escape did not close the popup and clear active state without selecting");
  }

  dependsInput.value = "no such task";
  dependsInput.eventListeners.input();
  const listbox = findElement(dependsGroup, (element) => element.attributes.role === "listbox");
  const fakeOption = listbox && findElement(listbox, (element) => element.attributes.role === "option");
  const emptyStatus = findElement(dependsGroup, (element) =>
    element.attributes.role === "status" && element.textContent === "No matching tasks.");
  if (fakeOption || !emptyStatus) {
    throw new Error("empty matches rendered a fake option instead of an announced message");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency failure recovery and keyboard behavior: %v\n%s", err, output)
	}
}

func TestHandlerInterceptsOrdinarySameOriginNavigation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	task := boardTasks()[0]
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:  "workbook.tasks",
		Version: 1,
		Tasks:   []core.Task{task},
		Presentation: []TaskPresentation{{
			TaskID:   task.ID,
			IDPrefix: task.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(() => {
  const click = documentEventListeners.click;
  if (!click) throw new Error("client did not register delegated click navigation");
  const taskLink = boardLists.map((list) => findElement(list, (element) => element.tagName === "A" && element.href === "/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `))).find(Boolean);
  if (!taskLink) throw new Error("refresh did not dynamically render a task link");

  let prevented = false;
  click({ target: taskLink, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { prevented = true; } });
  if (!prevented) throw new Error("ordinary same-origin task navigation was not intercepted");
  if (historyPaths.length !== 1 || historyPaths[0] !== taskLink.href) throw new Error("ordinary navigation did not use history.pushState");
  if (!findElement(main, (element) => element.id === "task-title")) throw new Error("ordinary navigation did not render the destination route");

  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  let backPrevented = false;
  click({ target: back, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { backPrevented = true; } });
  if (!backPrevented || historyPaths.length !== 2 || historyPaths[1] !== "/") throw new Error("Back did not use delegated history navigation");

  const newTask = new TestElement("a"); newTask.href = "/tasks/new?status=in-review"; boardView.append(newTask);
  let newTaskPrevented = false;
  click({ target: newTask, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { newTaskPrevented = true; } });
  const status = findElement(main, (element) => element.id === "task-status");
  if (!newTaskPrevented || historyPaths.length !== 3 || historyPaths[2] !== newTask.href || !status || status.value !== "in-review") {
    throw new Error("New Task did not use delegated history navigation");
  }

  let modifiedPrevented = false;
  click({ target: taskLink, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: true, shiftKey: false, altKey: false, preventDefault() { modifiedPrevented = true; } });
  if (modifiedPrevented || historyPaths.length !== 3) throw new Error("modified click was intercepted");

  const external = new TestElement("a"); external.href = "https://example.com/tasks/external";
  let externalPrevented = false;
  click({ target: external, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { externalPrevented = true; } });
  if (externalPrevented || historyPaths.length !== 3) throw new Error("external navigation was intercepted");

  const newTab = new TestElement("a"); newTab.href = "/tasks/new"; newTab.target = "_blank";
  let newTabPrevented = false;
  click({ target: newTab, button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { newTabPrevented = true; } });
  if (newTabPrevented || historyPaths.length !== 3) throw new Error("new-tab navigation was intercepted");
}, 0);
`
	command := exec.Command(node, "-e", program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client navigation behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientBoardIgnoresUnknownStatuses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: taskPresentation(tasks),
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(documentJSON)) + script + `
setTimeout(() => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const card = findElement(ready, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[0].ID) + `);
  const unknownCard = boardLists.map((list) => findElement(list, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `)).find(Boolean);
  if (!card) throw new Error("canonical task did not render when an unknown-status task was present");
  const progress = findElement(card, (element) => Object.hasOwn(element.dataset, "dependencyProgress"));
  const count = progress && findElement(progress, (element) => element.tagName === "SPAN" && element.textContent === "1 of 2 prerequisites complete");
  const waiting = progress && findElement(progress, (element) => element.tagName === "STRONG" && element.textContent === "Waiting on dependencies");
  if (!count || !waiting) {
    throw new Error("refreshed Ready card did not render dependency progress");
  }
  const dependencyFree = boardLists.map((list) => findElement(list, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[1].ID) + `)).find(Boolean);
  if (!dependencyFree || findElement(dependencyFree, (element) => Object.hasOwn(element.dataset, "dependencyProgress"))) {
    throw new Error("dependency-free refreshed card rendered dependency progress");
  }
  if (unknownCard) throw new Error("unknown-status task rendered in a canonical list");
  if (stale.dataset.visible !== "false") throw new Error("unknown-status task triggered the stale state");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered canonical board filtering: %v\n%s", err, output)
	}
}

func TestHandlerClientPollsEverySecond(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	tasks := boardTasks()[:1]
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
if (intervalDelay !== 1000) throw new Error("polling interval = " + intervalDelay + ", want 1000");
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered polling behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientPlacementClampsSameColumnPointerGapsToSamePriorityPeers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	high := clientPlacementTask("WB-01J00000000000000000000011", "High", core.StatusReady, core.PriorityHigh)
	moved := clientPlacementTask("WB-01J00000000000000000000012", "Moved medium", core.StatusReady, core.PriorityMedium)
	firstMedium := clientPlacementTask("WB-01J00000000000000000000013", "First medium", core.StatusReady, core.PriorityMedium)
	low := clientPlacementTask("WB-01J00000000000000000000014", "Low", core.StatusReady, core.PriorityLow)
	tasks := []core.Task{high, moved, firstMedium, low}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const cards = ready.querySelectorAll(".task-card");
  cards.forEach((item, index) => { item.rect = { top: index * 100, bottom: index * 100 + 80 }; });
  const moved = cards.find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  const high = cards.find((item) => item.dataset.priority === "high");
  const firstMedium = cards.find((item) => item.dataset.priority === "medium" && item !== moved);
  const low = cards.find((item) => item.dataset.priority === "low");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  if (!moved || !high || !firstMedium || !low) {
    throw new Error("client cards did not expose priority metadata required for clamped placement");
  }

  documentEventListeners.dragstart({ target: moved, dataTransfer });
  let prevented = false;
  documentEventListeners.dragover({
    target: high,
    clientY: 1,
    dataTransfer,
    preventDefault() { prevented = true; }
  });

  let markerIndex = ready.children.findIndex((item) => item.className === "drop-marker");
  let firstMediumIndex = ready.children.indexOf(firstMedium);
  if (!prevented || markerIndex !== firstMediumIndex - 1) {
    throw new Error("top-column drop did not clamp before the first medium-priority peer");
  }

  prevented = false;
  documentEventListeners.dragover({
    target: low,
    clientY: low.rect.bottom - 1,
    dataTransfer,
    preventDefault() { prevented = true; }
  });
  markerIndex = ready.children.findIndex((item) => item.className === "drop-marker");
  const lastMediumIndex = ready.children.indexOf(firstMedium);
  const lowIndex = ready.children.indexOf(low);
  if (!prevented || markerIndex !== lastMediumIndex + 1 || markerIndex !== lowIndex - 1) {
    throw new Error("bottom-column drop did not clamp after the last medium-priority peer");
  }
  await documentEventListeners.drop({
    target: low,
    clientY: low.rect.bottom - 1,
    dataTransfer,
    preventDefault() {}
  });
  const mutation = fetchCalls.find((call) => call.options.method === "PATCH");
  if (!mutation || mutation.url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `) + "/position") {
    throw new Error("same-column lower-boundary drop did not call the position endpoint");
  }
  const body = JSON.parse(mutation.options.body);
  if (body.status !== "ready" || body.after !== ` + strconv.Quote(firstMedium.ID) + ` || body.before) {
    throw new Error("same-column lower-boundary drop did not send the last same-priority peer as after");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered same-column placement behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientSendsAtomicClampedPlacementRequests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	moved := clientPlacementTask("WB-01J00000000000000000000021", "Moved medium", core.StatusReady, core.PriorityMedium)
	destinationHigh := clientPlacementTask("WB-01J00000000000000000000022", "In progress high", core.StatusInProgress, core.PriorityHigh)
	destinationMedium := clientPlacementTask("WB-01J00000000000000000000023", "In progress medium", core.StatusInProgress, core.PriorityMedium)
	doneHigh := clientPlacementTask("WB-01J00000000000000000000024", "Done high", core.StatusDone, core.PriorityHigh)
	doneLow := clientPlacementTask("WB-01J00000000000000000000025", "Done low", core.StatusDone, core.PriorityLow)
	tasks := []core.Task{moved, destinationHigh, destinationMedium, doneHigh, doneLow}
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const moved = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  const destinationHigh = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(destinationHigh.ID) + `);
  const destinationMedium = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(destinationMedium.ID) + `);
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  moved.rect = { top: 0, bottom: 80 };
  destinationHigh.rect = { top: 0, bottom: 80 };
  destinationMedium.rect = { top: 100, bottom: 180 };
  documentEventListeners.dragstart({ target: moved, dataTransfer });
  let prevented = false;
  documentEventListeners.dragover({
    target: destinationHigh,
    clientY: 1,
    dataTransfer,
    preventDefault() { prevented = true; }
  });
  const markerIndex = inProgress.children.findIndex((item) => item.className === "drop-marker");
  const destinationMediumIndex = inProgress.children.indexOf(destinationMedium);
  if (!prevented || markerIndex !== destinationMediumIndex - 1) {
    throw new Error("cross-status drop did not show the clamped medium-priority marker");
  }
  await documentEventListeners.drop({
    target: destinationHigh,
    clientY: 1,
    dataTransfer,
    preventDefault() {}
  });

  const mutation = fetchCalls.find((call) => call.options.method === "PATCH");
  if (!mutation || mutation.url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `) + "/position") {
    throw new Error("drop did not call the position endpoint");
  }
  const body = JSON.parse(mutation.options.body);
  if (body.status !== "in-progress" || body.before !== ` + strconv.Quote(destinationMedium.ID) + ` || body.after) {
    throw new Error("cross-status drop did not send the clamped medium-priority anchor");
  }

  const refreshedMoved = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  const done = boardLists.find((list) => list.dataset.status === "done");
  const refreshedDoneHigh = done.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(doneHigh.ID) + `);
  const refreshedDoneLow = done.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(doneLow.ID) + `);
  refreshedDoneHigh.rect = { top: 0, bottom: 80 };
  refreshedDoneLow.rect = { top: 100, bottom: 180 };
  documentEventListeners.dragstart({ target: refreshedMoved, dataTransfer });
  prevented = false;
  documentEventListeners.dragover({
    target: refreshedDoneHigh,
    clientY: 1,
    dataTransfer,
    preventDefault() { prevented = true; }
  });
  const doneMarkerIndex = done.children.findIndex((item) => item.className === "drop-marker");
  const doneHighIndex = done.children.indexOf(refreshedDoneHigh);
  const doneLowIndex = done.children.indexOf(refreshedDoneLow);
  if (!prevented || doneMarkerIndex !== doneHighIndex + 1 || doneMarkerIndex !== doneLowIndex - 1) {
    throw new Error("empty-priority destination did not mark the canonical priority boundary");
  }
  await documentEventListeners.drop({
    target: refreshedDoneHigh,
    clientY: 1,
    dataTransfer,
    preventDefault() {}
  });
  const mutations = fetchCalls.filter((call) => call.options.method === "PATCH");
  const noPeerMutation = mutations[1];
  if (!noPeerMutation || JSON.stringify(JSON.parse(noPeerMutation.options.body)) !== '{"status":"done"}') {
    throw new Error("empty-priority destination did not send an anchorless placement request");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered atomic placement behavior: %v\n%s", err, output)
	}
}

func TestHandlerRefreshesTasksOnEveryAPIRequest(t *testing.T) {
	first := boardTasks()
	second := append([]core.Task(nil), first...)
	second[0].Title = "Updated without restarting"
	calls := 0
	handler := NewHandler(func(context.Context) ([]core.Task, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	}, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	for _, want := range []string{"Ready task", "Updated without restarting"} {
		response := request(t, handler, http.MethodGet, "/api/tasks")
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
		}
		var document TasksDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if got := document.Tasks[0].Title; got != want {
			t.Fatalf("request %d first task title = %q, want %q", calls, got, want)
		}
	}
	if calls != 2 {
		t.Fatalf("lister calls = %d, want 2", calls)
	}
}

func renderedClientScript(t *testing.T, body string) string {
	t.Helper()
	const open = "<script>"
	const close = "</script>"
	start := strings.LastIndex(body, open)
	end := strings.LastIndex(body, close)
	if start < 0 || end <= start {
		t.Fatal("rendered page does not contain an executable client script")
	}
	return body[start+len(open) : end]
}

func clientDOMHarness(path, taskDocument string) string {
	return `
const scrollIntoViewCalls = [];
class TestElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.eventListeners = {};
    this._value = "";
    this._textContent = "";
    this.selected = false;
    this.disabled = false;
    this.required = false;
  }
  append(...children) {
    children.forEach((child) => {
      if (child.tagName === "FRAGMENT") {
        child.children.splice(0).forEach((fragmentChild) => { fragmentChild.parentElement = this; this.children.push(fragmentChild); });
        return;
      }
      child.remove();
      child.parentElement = this;
      this.children.push(child);
    });
  }
  replaceChildren(...children) {
    this.children.forEach((child) => { child.parentElement = null; });
    this.children = [];
    this.append(...children);
  }
  insertBefore(child, reference) {
    child.remove();
    const index = reference ? this.children.indexOf(reference) : -1;
    child.parentElement = this;
    if (index < 0) this.children.push(child);
    else this.children.splice(index, 0, child);
  }
  remove() {
    if (!this.parentElement) return;
    const index = this.parentElement.children.indexOf(this);
    if (index >= 0) this.parentElement.children.splice(index, 1);
    this.parentElement = null;
  }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  removeAttribute(name) { delete this.attributes[name]; }
  hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attributes, name); }
  focus() { globalThis.activeElement = this; }
  get id() { return this.attributes.id || this._id || ""; }
  set id(value) { this._id = String(value); this.attributes.id = String(value); }
  addEventListener(name, listener) { this.eventListeners[name] = listener; }
  closest(selector) {
    for (let element = this; element; element = element.parentElement) {
      if (selector === "a[href]" && element.tagName === "A" && element.href) return element;
      if (selector === ".task-card" && element.className === "task-card") return element;
      if (selector === "[data-drop-status]" && element.dataset.dropStatus) return element;
      if (selector === "[data-status]" && element.dataset.status) return element;
    }
    return null;
  }
  contains(element) {
    for (let current = element; current; current = current.parentElement) {
      if (current === this) return true;
    }
    return false;
  }
  querySelector(selector) {
    if (selector === "[data-stale]") return stale;
    return null;
  }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (element) => {
      for (const child of element.children || []) {
        if (selector === ".task-card" && child.className === "task-card") matches.push(child);
        if (selector === "[role=\"option\"]" && child.attributes.role === "option") matches.push(child);
        if (selector === "[data-relationship-row]" && Object.hasOwn(child.dataset, "relationshipRow")) matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
  getBoundingClientRect() { return this.rect || { top: 0, bottom: 0 }; }
  scrollIntoView(options) { scrollIntoViewCalls.push({ element: this, options }); }
  get firstElementChild() { return this.children[0] || null; }
  get textContent() { return this._textContent + this.children.map((child) => child.textContent).join(""); }
  set textContent(value) { this._textContent = String(value); }
  get value() {
    if (this.tagName === "SELECT") {
      const selected = this.children.find((option) => option.selected);
      return selected ? selected.value : (this.children[0] ? this.children[0].value : "");
    }
    return this._value;
  }
  set value(value) { this._value = String(value); }
}
const main = new TestElement("main");
const boardView = new TestElement("div");
const stale = new TestElement("p");
const updated = new TestElement("p");
const boardStatuses = ["backlog", "ready", "blocked", "in-progress", "in-review", "done"];
const boardLists = boardStatuses.map((status) => {
  const element = new TestElement("div");
  element.dataset.status = status;
  element.dataset.dropStatus = status;
  return element;
});
const boardCounts = boardStatuses.map((status) => {
  const element = new TestElement("span");
  element.dataset.count = status;
  return element;
});
boardView.querySelectorAll = (selector) => selector === "[data-status]" ? boardLists : boardCounts;
const documentEventListeners = {};
globalThis.document = {
  title: "",
  querySelector(selector) {
    if (selector === "main") return main;
    if (selector === "[data-board-view]") return boardView;
    if (selector === "[data-updated]") return updated;
    return null;
  },
  querySelectorAll() { return []; },
  createElement(tagName) { return new TestElement(tagName); },
  createDocumentFragment() { return new TestElement("fragment"); },
  addEventListener(name, listener) { documentEventListeners[name] = listener; }
};
const initialURL = new URL("http://127.0.0.1` + path + `");
let intervalDelay = null;
let intervalCallback = null;
globalThis.window = {
  location: { href: initialURL.href, origin: initialURL.origin },
  addEventListener() {},
  setInterval(callback, delay) { intervalCallback = callback; intervalDelay = delay; }
};
const historyPaths = [];
globalThis.history = {
  pushState(_state, _title, path) {
    historyPaths.push(path);
    window.location.href = new URL(path, window.location.href).href;
  }
};
globalThis.requestAnimationFrame = (callback) => callback();
const taskDocument = ` + taskDocument + `;
let taskResponse = taskDocument;
let deletedTaskResponse = taskDocument;
const fetchCalls = [];
globalThis.fetch = async (url, options = {}) => {
  fetchCalls.push({ url, options });
  if ((options.method || "GET") !== "GET") {
    return {
      ok: true,
      json: async () => ({
        format: "workbook.task-mutation",
        version: 1,
        task: taskDocument.tasks[0]
      })
    };
  }
  return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
};
function findElement(root, predicate) {
  if (predicate(root)) return root;
  for (const child of root.children || []) {
    const match = findElement(child, predicate);
    if (match) return match;
  }
  return null;
}
`
}

func TestHandlerUpdatesTaskStatus(t *testing.T) {
	var gotID string
	var gotStatus core.Status
	updated := boardTasks()[0]
	updated.Status = core.StatusInProgress
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		func(_ context.Context, id string, status core.Status) (core.MutationResult, error) {
			gotID = id
			gotStatus = status
			return core.MutationResult{Task: updated}, nil
		},
	)

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001/status", `{"status":"in-progress"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if gotID != "WB-01J00000000000000000000001" || gotStatus != core.StatusInProgress {
		t.Fatalf("updater saw id/status = %q/%q", gotID, gotStatus)
	}
	var document TaskMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.task-mutation" || document.Version != 1 || document.Task.Status != core.StatusInProgress {
		t.Fatalf("mutation document = %#v", document)
	}
}

func TestHandlerCreatesTask(t *testing.T) {
	created := boardTasks()[0]
	created.ID = "WB-01J00000000000000000000009"
	created.Title = "Create a detail view"
	created.Description = "Expose every editable field."
	created.Status = core.StatusInReview
	created.Priority = core.PriorityLow
	created.Labels = []string{"web", "forms"}
	want := core.CreateInput{
		Title:       created.Title,
		Description: created.Description,
		Status:      created.Status,
		Priority:    created.Priority,
		Labels:      created.Labels,
	}
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("create input = %#v, want %#v", input, want)
			}
			return core.MutationResult{Task: created}, nil
		},
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
	)

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"Create a detail view","description":"Expose every editable field.","status":"in-review","priority":"low","labels":["web","forms"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertTaskMutationDocument(t, response, created)
}

func TestHandlerUpdatesAllTaskFields(t *testing.T) {
	updated := boardTasks()[0]
	updated.Title = "Edit every task field"
	updated.Description = "Explicit empty values must remain possible."
	updated.Status = core.StatusDone
	updated.Priority = core.PriorityLow
	updated.Labels = []string{"finished"}
	want := core.UpdateInput{
		Title:       &updated.Title,
		Description: &updated.Description,
		Status:      &updated.Status,
		Priority:    &updated.Priority,
		Labels:      &updated.Labels,
	}
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			if id != "WB-01J00000000000000000000001" {
				t.Fatalf("update id = %q", id)
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("update input = %#v, want %#v", input, want)
			}
			return core.MutationResult{Task: updated}, nil
		},
		unexpectedStatusUpdate(t),
	)

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001", `{"title":"Edit every task field","description":"Explicit empty values must remain possible.","status":"done","priority":"low","labels":["finished"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/<id> status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertTaskMutationDocument(t, response, updated)
}

func TestHandlerPositionsTask(t *testing.T) {
	want := boardTasks()[0]
	want.Status = core.StatusInProgress
	want.Rank = "3/1"
	var gotID string
	var gotInput core.PlaceInput
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
		func(_ context.Context, id string, input core.PlaceInput) (core.MutationResult, error) {
			gotID = id
			gotInput = input
			return core.MutationResult{Task: want}, nil
		},
		nil,
		nil,
		nil,
		nil,
	)

	response := requestJSON(
		t,
		handler,
		http.MethodPatch,
		"/api/tasks/"+want.ID+"/position",
		`{"status":"in-progress","before":"WB-01J00000000000000000000002"}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH position status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if gotID != want.ID || gotInput.Status != core.StatusInProgress ||
		gotInput.Before != "WB-01J00000000000000000000002" || gotInput.After != "" {
		t.Fatalf("position callback = %q/%#v", gotID, gotInput)
	}
	assertTaskMutationDocument(t, response, want)

	response = requestJSON(
		t,
		handler,
		http.MethodPatch,
		"/api/tasks/"+want.ID+"/position",
		`{"status":"ready","after":"WB-01J00000000000000000000003"}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH position after status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if gotID != want.ID || gotInput.Status != core.StatusReady ||
		gotInput.Before != "" || gotInput.After != "WB-01J00000000000000000000003" {
		t.Fatalf("position after callback = %q/%#v", gotID, gotInput)
	}
	assertTaskMutationDocument(t, response, want)
}

func TestHandlerValidatesPositionRequests(t *testing.T) {
	const taskID = "WB-01J00000000000000000000001"
	handler := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
		func(context.Context, string, core.PlaceInput) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "placement accepts at most one anchor direction")
		},
		nil,
		nil,
		nil,
		nil,
	)

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+taskID+"/position",
		`{"status":"ready","before":"WB-A","after":"WB-B"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous position status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode ambiguous position error: %v", err)
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryValidation {
		t.Fatalf("ambiguous position error document = %#v, want workbook.error v1 validation", document)
	}

	handler = NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t),
		nil,
		nil,
		nil,
		nil,
	)
	response = requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+taskID+"/position",
		`{"status":"ready","rank":"1/1"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown position field status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode position error: %v", err)
	}
	if document.Error.Category != core.CategoryInvocation {
		t.Fatalf("unknown position field category = %q, want %q", document.Error.Category, core.CategoryInvocation)
	}
}

func TestHandlerRejectsInvalidTaskMutationRequests(t *testing.T) {
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
	)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create malformed JSON", method: http.MethodPost, path: "/api/tasks", body: `{"title":`},
		{name: "create unknown property", method: http.MethodPost, path: "/api/tasks", body: `{"title":"task","unexpected":true}`},
		{name: "create multiple JSON values", method: http.MethodPost, path: "/api/tasks", body: `{"title":"task"} {}`},
		{name: "update malformed JSON", method: http.MethodPatch, path: "/api/tasks/WB-01J00000000000000000000001", body: `{"title":`},
		{name: "update unknown property", method: http.MethodPatch, path: "/api/tasks/WB-01J00000000000000000000001", body: `{"title":"task","unexpected":true}`},
		{name: "update multiple JSON values", method: http.MethodPatch, path: "/api/tasks/WB-01J00000000000000000000001", body: `{"title":"task"} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, test.method, test.path, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want %d; body = %s", test.method, test.path, response.Code, http.StatusBadRequest, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v", err)
			}
			if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryInvocation {
				t.Fatalf("error document = %#v, want workbook.error v1 invocation", document)
			}
		})
	}
}

func TestHandlerPreservesStatusMutationRoute(t *testing.T) {
	called := false
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		func(context.Context, string, core.Status) (core.MutationResult, error) {
			called = true
			return core.MutationResult{Task: boardTasks()[0]}, nil
		},
	)

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001/status", `{"status":"ready"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/<id>/status status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !called {
		t.Fatal("status callback was not called")
	}
}

func TestHandlerRejectsWrongMethods(t *testing.T) {
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
	)
	for _, test := range []struct {
		method    string
		path      string
		wantAllow string
	}{
		{method: http.MethodGet, path: "/api/tasks/WB-01J00000000000000000000001", wantAllow: http.MethodPatch + ", " + http.MethodDelete},
		{method: http.MethodGet, path: "/api/tasks/WB-01J00000000000000000000001/position", wantAllow: http.MethodPatch},
		{method: http.MethodPut, path: "/api/tasks", wantAllow: http.MethodGet + ", " + http.MethodPost},
	} {
		response := request(t, handler, test.method, test.path)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, response.Code, http.StatusMethodNotAllowed)
		}
		if got := response.Header().Get("Allow"); got != test.wantAllow {
			t.Errorf("%s %s Allow = %q, want %q", test.method, test.path, got, test.wantAllow)
		}
	}
}

func TestHandlerMapsStatusUpdateErrorsToVersionedErrorDocuments(t *testing.T) {
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t),
		unexpectedTaskUpdate(t),
		func(context.Context, string, core.Status) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "invalid task status")
		},
	)

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001/status", `{"status":"future-status"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryValidation || document.Error.Message != "invalid task status" {
		t.Fatalf("error document = %#v", document)
	}
}

func TestHandlerProvidesActionablePrefixesForRefresh(t *testing.T) {
	tasks := boardTasks()
	tasks[0].ID = "WB-01J0000A1111111111111111111"
	tasks[1].ID = "WB-01J0000B2222222222222222222"
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/api/tasks")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
	}
	var document struct {
		Format       string      `json:"format"`
		Version      int         `json:"version"`
		Tasks        []core.Task `json:"tasks"`
		Presentation []struct {
			TaskID   string `json:"taskId"`
			IDPrefix string `json:"idPrefix"`
		} `json:"presentation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode task document: %v", err)
	}
	if !reflect.DeepEqual(document.Tasks, tasks) {
		t.Errorf("task document tasks = %#v, want full values %#v", document.Tasks, tasks)
	}
	prefixes := make(map[string]string, len(document.Presentation))
	for _, view := range document.Presentation {
		prefixes[view.TaskID] = view.IDPrefix
	}
	if got, want := prefixes[tasks[0].ID], "WB-01J0000A"; got != want {
		t.Errorf("first ID prefix = %q, want %q", got, want)
	}
	if got, want := prefixes[tasks[1].ID], "WB-01J0000B"; got != want {
		t.Errorf("second ID prefix = %q, want %q", got, want)
	}
	if len(prefixes) != len(tasks) {
		t.Errorf("presentation prefix count = %d, want %d", len(prefixes), len(tasks))
	}

	page := request(t, handler, http.MethodGet, "/")
	body := page.Body.String()
	if !strings.Contains(body, "document.presentation") {
		t.Error("embedded refresh script does not read server presentation data")
	}
	if !strings.Contains(body, "text(id, view.idPrefix)") {
		t.Error("embedded refresh script does not render the server-provided ID prefix")
	}
	if strings.Contains(body, "text(id, task.id)") {
		t.Error("embedded refresh script renders full task IDs instead of server-provided prefixes")
	}
}

func TestHandlerInitialCardPrefixesMatchRefreshPresentation(t *testing.T) {
	tasks := boardTasks()
	tasks[0].ID = "WB-01J0000A1111111111111111111"
	tasks[1].ID = "WB-01J0000B2222222222222222222"
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	initial := request(t, handler, http.MethodGet, "/")
	if initial.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", initial.Code, http.StatusOK)
	}
	cards := initialCardPrefixes(initial.Body.String())
	if len(cards) != 3 {
		t.Fatalf("initial rendered cards = %#v, want the three canonical-status tasks", cards)
	}
	if _, exists := cards[tasks[2].ID]; exists {
		t.Fatalf("initial rendered cards include unknown-status task %q", tasks[2].ID)
	}

	refreshed := request(t, handler, http.MethodGet, "/api/tasks")
	if refreshed.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", refreshed.Code, http.StatusOK)
	}
	var document TasksDocument
	if err := json.Unmarshal(refreshed.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode refreshed task document: %v", err)
	}
	prefixes := make(map[string]string, len(document.Presentation))
	for _, view := range document.Presentation {
		prefixes[view.TaskID] = view.IDPrefix
	}
	for taskID, initialPrefix := range cards {
		if got := prefixes[taskID]; got != initialPrefix {
			t.Errorf("refresh presentation prefix for %q = %q, want initial rendered prefix %q", taskID, got, initialPrefix)
		}
	}
}

func TestHandlerServesDragAndDropBoardControls(t *testing.T) {
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return boardTasks(), nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	for _, fragment := range []string{
		`draggable="true"`,
		`aria-label="Move task Ready task from ready"`,
		`data-priority="high"`,
		`data-drop-status="ready"`,
		`PATCH`,
		`/api/tasks/`,
		`/position`,
		`dragstart`,
		`dragover`,
		`drop`,
		`drop-marker`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `data-drop-status="unknown"`) {
		t.Error("unknown status list must not be a status drop target")
	}
}

func initialCardPrefixes(body string) map[string]string {
	pattern := regexp.MustCompile(`(?s)<article class="task-card" tabindex="0" data-task-id="([^"]+)" data-priority="[^"]+" data-id-prefix="([^"]+)"[^>]*>\s*<div class="task-card__meta"><code>([^<]+)</code>`)
	cards := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(body, -1) {
		if match[2] == match[3] {
			cards[match[1]] = match[2]
		}
	}
	return cards
}

func TestHandlerRejectsUnknownRoutesAndMutationMethods(t *testing.T) {
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return boardTasks(), nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	unknown := request(t, handler, http.MethodGet, "/missing")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("GET /missing status = %d, want %d", unknown.Code, http.StatusNotFound)
	}
	assertSecurityHeaders(t, unknown.Result())

	for _, path := range []string{"/", "/healthz"} {
		response := request(t, handler, http.MethodPost, path)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
		}
		if got := response.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("POST %s Allow = %q, want %q", path, got, http.MethodGet)
		}
		assertSecurityHeaders(t, response.Result())
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := request(t, handler, method, "/api/tasks/WB-01J00000000000000000000001/status")
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/tasks/<id>/status status = %d, want %d", method, response.Code, http.StatusMethodNotAllowed)
		}
		if got := response.Header().Get("Allow"); got != http.MethodPatch {
			t.Errorf("%s /api/tasks/<id>/status Allow = %q, want %q", method, got, http.MethodPatch)
		}
		assertSecurityHeaders(t, response.Result())
	}
}

func TestHandlerMapsTaskErrorsToVersionedErrorDocuments(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "invalid invocation", err: core.Errorf(core.CategoryInvocation, "bad arguments"), wantStatus: http.StatusBadRequest, wantBody: "bad arguments"},
		{name: "validation", err: core.Errorf(core.CategoryValidation, "invalid task"), wantStatus: http.StatusBadRequest, wantBody: "invalid task"},
		{name: "not found", err: core.Errorf(core.CategoryNotFound, "task missing"), wantStatus: http.StatusNotFound, wantBody: "task missing"},
		{name: "not initialized", err: core.Errorf(core.CategoryNotInitialized, "initialize first"), wantStatus: http.StatusConflict, wantBody: "initialize first"},
		{name: "stale write", err: core.Errorf(core.CategoryStaleWrite, "stale task"), wantStatus: http.StatusConflict, wantBody: "stale task"},
		{name: "corrupt data", err: core.Errorf(core.CategoryCorruptData, "bad checkpoint"), wantStatus: http.StatusInternalServerError, wantBody: "bad checkpoint"},
		{name: "operational includes cause", err: core.Wrap(core.CategoryOperational, "list tasks", errors.New("permission denied")), wantStatus: http.StatusInternalServerError, wantBody: "list tasks: permission denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(func(context.Context) ([]core.Task, error) { return nil, test.err }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))
			response := request(t, handler, http.MethodGet, "/api/tasks")
			if response.Code != test.wantStatus {
				t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, test.wantStatus)
			}
			assertSecurityHeaders(t, response.Result())
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.error" || document.Version != 1 {
				t.Fatalf("error envelope = %#v, want workbook.error v1", document)
			}
			if document.Error.Category != core.CategoryOf(test.err) || document.Error.Message != test.wantBody {
				t.Fatalf("error body = %#v, want category %q and message %q", document.Error, core.CategoryOf(test.err), test.wantBody)
			}
		})
	}
}

func TestHandlerEscapesHostileTaskContent(t *testing.T) {
	tasks := boardTasks()
	tasks[0].Title = `<img src=x onerror=alert(1)>`
	tasks[0].Description = `<script>alert("pwned")</script>`
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, hostile := range []string{`<img src=x onerror=alert(1)>`, `<script>alert("pwned")</script>`} {
		if strings.Contains(body, hostile) {
			t.Errorf("GET / contains executable hostile markup %q", hostile)
		}
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("GET / did not preserve hostile title as escaped text: %s", body)
	}
}

func request(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func requestJSON(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}

func unexpectedStatusUpdate(t *testing.T) TaskStatusUpdater {
	t.Helper()
	return func(context.Context, string, core.Status) (core.MutationResult, error) {
		t.Fatal("unexpected status update")
		return core.MutationResult{}, nil
	}
}

func unexpectedTaskPosition(t *testing.T) TaskPositionUpdater {
	t.Helper()
	return func(context.Context, string, core.PlaceInput) (core.MutationResult, error) {
		t.Fatal("unexpected task position")
		return core.MutationResult{}, nil
	}
}

func unexpectedTaskCreate(t *testing.T) TaskCreator {
	t.Helper()
	return func(context.Context, core.CreateInput) (core.MutationResult, error) {
		t.Fatal("unexpected task create")
		return core.MutationResult{}, nil
	}
}

func unexpectedTaskUpdate(t *testing.T) TaskUpdater {
	t.Helper()
	return func(context.Context, string, core.UpdateInput) (core.MutationResult, error) {
		t.Fatal("unexpected task update")
		return core.MutationResult{}, nil
	}
}

func assertTaskMutationDocument(t *testing.T, response *httptest.ResponseRecorder, want core.Task) {
	t.Helper()
	var document TaskMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.task-mutation" || document.Version != 1 || !reflect.DeepEqual(document.Task, want) {
		t.Fatalf("mutation document = %#v, want workbook.task-mutation v1 with task %#v", document, want)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode mutation document fields: %v; body = %s", err, response.Body.String())
	}
	if _, exists := fields["warnings"]; exists {
		t.Fatalf("mutation document includes warnings without a warning: %s", response.Body.String())
	}
}

func assertSecurityHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := response.Header.Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, contentSecurityPolicy)
	}
}

func clientPlacementTask(id, title string, status core.Status, priority core.Priority) core.Task {
	return core.Task{
		ID: id,
		TaskData: core.TaskData{
			Title:    title,
			Status:   status,
			Priority: priority,
			Rank:     "1/1",
			Labels:   []string{},
		},
	}
}

func boardTasks() []core.Task {
	stamp := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	return []core.Task{
		{
			ID:        "WB-01J00000000000000000000001",
			ProjectID: "01J00000000000000000000000",
			TaskData: core.TaskData{
				Title:        "Ready task",
				Description:  "Build the board surface.",
				Status:       core.StatusReady,
				Priority:     core.PriorityHigh,
				Labels:       []string{"ui", "web"},
				Rank:         "1/1",
				Dependencies: []string{"WB-01J00000000000000000000002", "WB-01J00000000000000000000006"},
				CreatedAt:    stamp,
				UpdatedAt:    stamp.Add(time.Minute),
			},
			HistoryGeneration: "01J00000000000000000000003",
			Head:              "abcdef0123456789",
		},
		{
			ID:        "WB-01J00000000000000000000002",
			ProjectID: "01J00000000000000000000000",
			TaskData: core.TaskData{
				Title:       "Blocked task",
				Description: "Await a dependent decision.",
				Status:      core.StatusBlocked,
				Priority:    core.PriorityMedium,
				Labels:      []string{"decision"},
				Rank:        "2/1",
				CreatedAt:   stamp,
				UpdatedAt:   stamp,
			},
			HistoryGeneration: "01J00000000000000000000004",
			Head:              "0123456789abcdef",
		},
		{
			ID:        "WB-01J00000000000000000000005",
			ProjectID: "01J00000000000000000000000",
			TaskData: core.TaskData{
				Title:       "Future status task",
				Description: "Keep forward-compatible status values visible.",
				Status:      core.Status("future-status"),
				Priority:    core.PriorityLow,
				Rank:        "3/1",
				CreatedAt:   stamp,
				UpdatedAt:   stamp,
			},
			HistoryGeneration: "01J00000000000000000000006",
			Head:              "fedcba9876543210",
		},
		{
			ID:        "WB-01J00000000000000000000006",
			ProjectID: "01J00000000000000000000000",
			TaskData: core.TaskData{
				Title:       "Completed prerequisite",
				Description: "Finish the prerequisite work.",
				Status:      core.StatusDone,
				Priority:    core.PriorityLow,
				Labels:      []string{"completed"},
				Rank:        "4/1",
				CreatedAt:   stamp,
				UpdatedAt:   stamp,
			},
			HistoryGeneration: "01J00000000000000000000007",
			Head:              "1234567890abcdef",
		},
	}
}

func presentationForTasks(tasks []core.Task) []TaskPresentation {
	presentation := make([]TaskPresentation, len(tasks))
	for i, task := range tasks {
		presentation[i] = TaskPresentation{TaskID: task.ID, IDPrefix: task.ID}
	}
	return presentation
}
