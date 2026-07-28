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
		`data-status="unknown"`,
		"Ready task",
		"Future status task",
		"Task refresh failed",
	} {
		if !strings.Contains(board.Body.String(), fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
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
		func(_ context.Context, id string) (core.MutationResult, error) {
			deletedID = id
			return core.MutationResult{Task: deleted}, nil
		},
		func(_ context.Context, id string) (core.MutationResult, error) {
			restoredID = id
			deleted.Deleted = false
			return core.MutationResult{Task: deleted}, nil
		},
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
	for _, task := range tasks {
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
class TestElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.classList = { add() {}, remove() {}, toggle() {} };
    this.eventListeners = {};
    this._value = "";
    this.textContent = "";
    this.selected = false;
    this.disabled = false;
    this.required = false;
  }
  append(...children) { children.forEach((child) => { child.parentElement = this; this.children.push(child); }); }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attributes, name); }
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
  querySelectorAll() { return []; }
  get firstElementChild() { return this.children[0] || null; }
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
const boardStatuses = ["backlog", "ready", "blocked", "in-progress", "in-review", "done", "unknown"];
const boardLists = boardStatuses.map((status) => {
  const element = new TestElement("div");
  element.dataset.status = status;
  if (status !== "unknown") element.dataset.dropStatus = status;
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
globalThis.window = {
  location: { href: initialURL.href, origin: initialURL.origin },
  addEventListener() {},
  setInterval() {}
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
globalThis.fetch = async () => ({ ok: true, json: async () => taskResponse });
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
	if !strings.Contains(body, "text(id, idPrefix)") {
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
	if len(cards) != len(tasks) {
		t.Fatalf("initial rendered cards = %#v, want one task identity and prefix for each of %#v", cards, tasks)
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
		`data-drop-status="ready"`,
		`PATCH`,
		`/api/tasks/`,
		`dragstart`,
		`dragover`,
		`drop`,
		`setDropState`,
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
	pattern := regexp.MustCompile(`(?s)<article class="task-card" tabindex="0" data-task-id="([^"]+)" data-id-prefix="([^"]+)"[^>]*>\s*<div class="task-card__meta"><code>([^<]+)</code>`)
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
				Dependencies: []string{"WB-01J00000000000000000000002"},
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
	}
}
