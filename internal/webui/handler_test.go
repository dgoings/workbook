package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dop251/goja"
)

const contentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

func TestHandlerServesBoardTasksAndHealth(t *testing.T) {
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	})

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

func TestHandlerProvidesActionablePrefixesForRefresh(t *testing.T) {
	tasks := boardTasks()
	tasks[0].ID = "WB-01J0000A1111111111111111111"
	tasks[1].ID = "WB-01J0000B2222222222222222222"
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil })

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

func TestEmbeddedRefreshRendersServerProvidedPrefix(t *testing.T) {
	tasks := boardTasks()
	tasks[0].ID = "WB-01J0000A1111111111111111111"
	tasks[1].ID = "WB-01J0000B2222222222222222222"
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/api/tasks")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
	}

	runtime := goja.New()
	if err := runtime.Set("__apiDocumentJSON", response.Body.String()); err != nil {
		t.Fatalf("set API document: %v", err)
	}
	if _, err := runtime.RunString(refreshHarness); err != nil {
		t.Fatalf("initialize refresh harness: %v", err)
	}
	if _, err := runtime.RunString(embeddedRefreshScript(t)); err != nil {
		t.Fatalf("run embedded refresh script: %v", err)
	}
	if _, err := runtime.RunString("scheduledRefresh()"); err != nil {
		t.Fatalf("run scheduled refresh: %v", err)
	}

	value, err := runtime.RunString(`JSON.stringify({
		taskID: listsByStatus.ready.children[0].dataset.taskId,
		idPrefix: listsByStatus.ready.children[0].dataset.idPrefix,
		visibleID: listsByStatus.ready.children[0].children[0].children[0].textContent
	})`)
	if err != nil {
		t.Fatalf("inspect refreshed card: %v", err)
	}
	var card struct {
		TaskID    string `json:"taskID"`
		IDPrefix  string `json:"idPrefix"`
		VisibleID string `json:"visibleID"`
	}
	if err := json.Unmarshal([]byte(value.String()), &card); err != nil {
		t.Fatalf("decode refreshed card: %v", err)
	}
	if got, want := card.TaskID, tasks[0].ID; got != want {
		t.Errorf("refreshed card task ID = %q, want %q", got, want)
	}
	if got, want := card.IDPrefix, "WB-01J0000A"; got != want {
		t.Errorf("refreshed card data prefix = %q, want %q", got, want)
	}
	if got, want := card.VisibleID, "WB-01J0000A"; got != want {
		t.Errorf("refreshed card visible ID = %q, want server-provided prefix %q", got, want)
	}
	if card.VisibleID == tasks[0].ID {
		t.Errorf("refreshed card rendered full task ID %q instead of its actionable prefix", card.VisibleID)
	}
}

func embeddedRefreshScript(t *testing.T) string {
	t.Helper()
	asset, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("read embedded asset: %v", err)
	}
	const opening = "<script>"
	start := strings.Index(string(asset), opening)
	end := strings.LastIndex(string(asset), "</script>")
	if start < 0 || end < start {
		t.Fatal("embedded asset has no inline refresh script")
	}
	return string(asset)[start+len(opening) : end]
}

const refreshHarness = `
var scheduledRefresh;
function element() {
  return {
    dataset: {}, children: [], className: "", tabIndex: 0, textContent: "",
    classList: { add: function() {}, remove: function() {} },
    append: function() { for (var i = 0; i < arguments.length; i++) this.children.push(arguments[i]); },
    replaceChildren: function(fragment) { this.children = fragment.children.slice(); }
  };
}
var listsByStatus = {};
var countsByStatus = {};
["backlog", "ready", "in-progress", "blocked", "done", "unknown"].forEach(function(status) {
  listsByStatus[status] = element(); listsByStatus[status].dataset.status = status;
  countsByStatus[status] = element(); countsByStatus[status].dataset.count = status;
});
var staleElement = element();
var updatedElement = element();
var document = {
  createElement: function() { return element(); },
  createDocumentFragment: function() { return element(); },
  querySelectorAll: function(selector) {
    if (selector === "[data-status]") return Object.keys(listsByStatus).map(function(status) { return listsByStatus[status]; });
    if (selector === "[data-count]") return Object.keys(countsByStatus).map(function(status) { return countsByStatus[status]; });
    return [];
  },
  querySelector: function(selector) {
    if (selector === "[data-stale]") return staleElement;
    if (selector === "[data-updated]") return updatedElement;
    return null;
  }
};
var window = { setInterval: function(callback) { scheduledRefresh = callback; return 1; } };
function requestAnimationFrame(callback) { callback(); }
function fetch() {
  return Promise.resolve({
    ok: true,
    json: function() { return Promise.resolve(JSON.parse(__apiDocumentJSON)); }
  });
}
`

func initialCardPrefixes(body string) map[string]string {
	pattern := regexp.MustCompile(`(?s)<article class="task-card" tabindex="0" data-task-id="([^"]+)" data-id-prefix="([^"]+)">\s*<div class="task-card__meta"><code>([^<]+)</code>`)
	cards := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(body, -1) {
		if match[2] == match[3] {
			cards[match[1]] = match[2]
		}
	}
	return cards
}

func TestHandlerRejectsUnknownRoutesAndMutationMethods(t *testing.T) {
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	unknown := request(t, handler, http.MethodGet, "/missing")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("GET /missing status = %d, want %d", unknown.Code, http.StatusNotFound)
	}
	assertSecurityHeaders(t, unknown.Result())

	for _, path := range []string{"/", "/api/tasks", "/healthz"} {
		response := request(t, handler, http.MethodPost, path)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
		}
		if got := response.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("POST %s Allow = %q, want %q", path, got, http.MethodGet)
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
			handler := NewHandler(func(context.Context) ([]core.Task, error) { return nil, test.err })
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
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil })

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
