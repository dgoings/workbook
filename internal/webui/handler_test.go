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
	"github.com/dgoings/workbook/internal/testenv"
)

const contentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

func TestHandlerServesBoardTasksAndHealth(t *testing.T) {
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
		// A status this build has no column for is named rather than dropped,
		// under its own heading, matching the terminal board's UNKNOWN STATUS
		// section. See internal/presentation/parity_test.go for the decision.
		"data-unknown-list",
		"Unknown status",
		"Future status task",
	} {
		if !strings.Contains(board.Body.String(), fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
	// The unknown region is not a seventh status: nothing may treat it as a
	// status a task can be moved to.
	assertBoardStatusMarkersMatchColumns(t, board.Body.String())
	// The card is movable out of the region even so — dropping it on a column is
	// an ordinary status change — and its label is where a reader is told both
	// that the status was not recognized and that the card is the way out.
	if !strings.Contains(board.Body.String(), `aria-label="Move task Future status task out of the unrecognized status future-status"`) {
		t.Error("GET / body does not label the unknown-status card as movable out of the region")
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
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
		Create: func(context.Context, core.CreateInput) (core.MutationResult, error) {
			return result, nil
		},
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})

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
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return []core.Task{active, deleted}, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Delete: func(_ context.Context, id string, _ core.DeleteInput) (core.MutationResult, error) {
			deletedID = id
			return core.MutationResult{Task: deleted}, nil
		},
		Restore: func(_ context.Context, id string, _ core.RestoreInput) (core.MutationResult, error) {
			restoredID = id
			deleted.Deleted = false
			return core.MutationResult{Task: deleted}, nil
		},
	})

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

func TestHandlerClientNamesJSONMediaTypeOnEveryMutation(t *testing.T) {
	node := requireNode(t)
	deleted := clientPlacementTask("WB-01J00000000000000000000070", "Body-less restore", core.StatusReady, core.PriorityMedium)
	deleted.Deleted = true
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	activeDocument := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: nil, Presentation: presentationForTasks(nil),
	})
	includedDocument := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks:        []core.Task{deleted},
		Presentation: presentationForTasks([]core.Task{deleted}),
	})

	// The server rejects mutations that do not declare Content-Type:
	// application/json, because an absent media type is exactly what a
	// cross-site form POST looks like. The Restore control sends the one
	// mutation that carries no body at all when it has no head to name, so it is
	// the request most likely to lose the header and the one exercised here.
	program := clientDOMHarness("/?deleted=1", string(activeDocument)) + script + `
includedTaskResponse = ` + string(includedDocument) + `;
let asserted = false;
process.on("exit", () => {
  if (!asserted) {
    console.error("mutation media type assertions did not run");
    process.exitCode = 1;
  }
});
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await intervalCallback();
  const card = boardCard(` + strconv.Quote(deleted.ID) + `);
  if (!card) throw new Error("the Deleted column drew no card for the deleted task");
  const restore = findElement(card, (element) => hasDataKey(element, "restoreTask"));
  if (!restore || restore.hidden) throw new Error("the deleted card offers no Restore control");
  restore.eventListeners.click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  const mutations = fetchCalls.filter(({ options }) => (options.method || "GET") !== "GET");
  if (mutations.length === 0) throw new Error("restore issued no mutation request");
  for (const { url, options } of mutations) {
    const contentType = options.headers && options.headers["Content-Type"];
    if (contentType !== "application/json") {
      throw new Error(options.method + " " + url + " Content-Type = " +
        JSON.stringify(contentType) + ", want application/json");
    }
  }
  asserted = true;
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute mutation media type behavior: %v\n%s", err, output)
	}
}

func TestHandlerAddsAndRemovesTaskDependencies(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	var calls []string
	warning := core.Warning{Code: core.WarningProjectionUpdate, Message: "cache update failed"}
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			calls = append(calls, "add:"+id+":"+dependency)
			return core.MutationResult{Task: dependent, Warnings: []core.Warning{warning}}, nil
		},
		Free: func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			calls = append(calls, "remove:"+id+":"+dependency)
			return core.MutationResult{Task: dependent}, nil
		},
	})
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

func TestHandlerDependencyMutationsRequireEmptyRequestBodies(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	dependencyCalls := 0
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
		Free: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
	})
	path := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID
	tests := []struct {
		name                 string
		method               string
		body                 string
		unknownContentLength bool
		wantStatus           int
		wantCalls            int
	}{
		{name: "empty PUT", method: http.MethodPut, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "empty DELETE", method: http.MethodDelete, wantStatus: http.StatusOK, wantCalls: 2},
		{name: "PUT JSON body", method: http.MethodPut, body: `{"unexpected":true}`, wantStatus: http.StatusBadRequest, wantCalls: 2},
		{name: "DELETE JSON body", method: http.MethodDelete, body: `{"unexpected":true}`, wantStatus: http.StatusBadRequest, wantCalls: 2},
		{name: "chunked PUT body", method: http.MethodPut, body: "x", unknownContentLength: true, wantStatus: http.StatusBadRequest, wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, path, strings.NewReader(test.body))
			if test.unknownContentLength {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("%s dependency status = %d, want %d; body = %s", test.method, response.Code, test.wantStatus, response.Body.String())
			}
			if dependencyCalls != test.wantCalls {
				t.Fatalf("dependency callbacks = %d, want %d", dependencyCalls, test.wantCalls)
			}
			if test.wantStatus == http.StatusBadRequest {
				var document ErrorDocument
				if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
					t.Fatalf("decode dependency body error: %v", err)
				}
				if document.Format != "workbook.error" || document.Version != 1 ||
					document.Error.Category != core.CategoryInvocation {
					t.Fatalf("dependency body error = %#v, want workbook.error v1 invocation", document)
				}
			}
		})
	}

	unconfigured := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
	})
	response := requestJSON(t, unconfigured, http.MethodPut, path, `{"unexpected":true}`)
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode unconfigured dependency body error: %v", err)
	}
	if response.Code != http.StatusBadRequest || document.Error.Category != core.CategoryInvocation {
		t.Fatalf("unconfigured dependency body status/error = %d/%#v, want invocation before callback configuration", response.Code, document.Error)
	}
}

func TestHandlerReturnsDependencyMutationErrors(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(context.Context, string, string) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "dependency would create a cycle")
		},
	})
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
	dependencyCalls := 0
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{}, nil
		},
		Free: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{}, nil
		},
	})
	path := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID
	response := request(t, handler, http.MethodPost, path)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "PUT, DELETE" {
		t.Fatalf("POST dependency = %d Allow %q, want %d and %q", response.Code, response.Header().Get("Allow"), http.StatusMethodNotAllowed, "PUT, DELETE")
	}
	for _, malformed := range []string{
		"/api/tasks/" + dependent.ID + "/dependencies",
		"/api/tasks/" + dependent.ID + "/dependencies/",
		"/api/tasks/" + dependent.ID + "/dependencies//" + prerequisite.ID,
		"/api/tasks/" + dependent.ID + "/dependencies/../" + prerequisite.ID,
		"/api/tasks/" + dependent.ID + "//dependencies/" + prerequisite.ID,
		"/api/tasks/" + dependent.ID + "/./dependencies/" + prerequisite.ID,
		"/api/tasks/" + dependent.ID + "/segment/../dependencies/" + prerequisite.ID,
		"/./api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID,
		"//api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID,
		"/segment/../api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID,
		"/api/tasks/./dependencies/" + prerequisite.ID,
		"/api/tasks/../dependencies/" + prerequisite.ID,
		"/api/tasks/" + dependent.ID + "/dependencies/.",
		"/api/tasks/" + dependent.ID + "/dependencies/..",
		path + "/extra",
	} {
		response := requestWithRawPath(t, handler, http.MethodPut, malformed)
		if response.Code != http.StatusNotFound {
			t.Errorf("PUT %s status = %d, want %d", malformed, response.Code, http.StatusNotFound)
		}
	}
	if dependencyCalls != 0 {
		t.Fatalf("malformed dependency paths invoked %d mutation callbacks, want 0", dependencyCalls)
	}
}

func TestHandlerRejectsEncodedDependencyPathAliases(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	dependencyCalls := 0
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			dependencyCalls++
			if id != dependent.ID || dependency != prerequisite.ID {
				t.Fatalf("dependency callback IDs = %q/%q, want %q/%q", id, dependency, dependent.ID, prerequisite.ID)
			}
			return core.MutationResult{Task: dependent}, nil
		},
	})
	canonicalPath := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID
	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantCalls  int
	}{
		{
			name:       "encoded dependencies segment",
			target:     "/api/tasks/" + dependent.ID + "/%64ependencies/" + prerequisite.ID,
			wantStatus: http.StatusNotFound,
			wantCalls:  0,
		},
		{
			name:       "encoded task ID character",
			target:     "/api/tasks/" + strings.Replace(dependent.ID, "WB-", "WB%2D", 1) + "/dependencies/" + prerequisite.ID,
			wantStatus: http.StatusNotFound,
			wantCalls:  0,
		},
		{
			name:       "canonical ASCII route",
			target:     canonicalPath,
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, test.target, nil)
			if test.target != canonicalPath && request.URL.EscapedPath() == request.URL.Path {
				t.Fatalf("test request %q did not retain a real encoded path", test.target)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("PUT %s status = %d, want %d; body = %s", test.target, response.Code, test.wantStatus, response.Body.String())
			}
			if dependencyCalls != test.wantCalls {
				t.Fatalf("dependency callbacks after %s = %d, want %d", test.name, dependencyCalls, test.wantCalls)
			}
		})
	}
}

func TestHandlerServesTaskRouteShell(t *testing.T) {
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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

// The deleted tasks are a column of the board rather than a page, so the page
// they used to have is gone outright: the address is not a route, it is not a
// method question either, and nothing on the board still links to it.
func TestHandlerRemovesTheDeletedTasksRoute(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := request(t, handler, method, "/deleted")
		if response.Code != http.StatusNotFound {
			t.Errorf("%s /deleted status = %d, want %d", method, response.Code, http.StatusNotFound)
		}
		// A 405 would say the address exists under another method. It does not
		// exist at all, so the method question must not be answered for it.
		if got := response.Header().Get("Allow"); got != "" {
			t.Errorf("%s /deleted Allow = %q, want no allowed method", method, got)
		}
	}
	body := request(t, handler, http.MethodGet, "/").Body.String()
	if strings.Contains(body, `href="/deleted"`) {
		t.Error("GET / still links to the removed deleted-tasks page")
	}
}

// The header's link to the deleted tasks is now the column's switch: an anchor,
// so it can be cmd-clicked, bookmarked and walked with Back, pointing at the
// address that shows the column. It ships hidden and the board's render reveals
// it, exactly as the Descriptions setting beside it does.
func TestHandlerServesTheDeletedColumnToggleAndBoardNavigation(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	for _, path := range []string{"/", "/tasks/new"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		body := response.Body.String()
		tag := openingTag(t, body, "data-deleted-toggle")
		if !strings.HasPrefix(tag, "<a ") {
			t.Errorf("GET %s served the deleted-column toggle as %s, want an anchor", path, tag)
		}
		if !strings.Contains(tag, `href="/?deleted=1"`) {
			t.Errorf("GET %s deleted-column toggle does not name the address that shows it: %s", path, tag)
		}
		if !strings.Contains(tag, " hidden") {
			t.Errorf("GET %s served the deleted-column toggle unhidden: %s", path, tag)
		}
		if !strings.Contains(body, `href="/"`) {
			t.Errorf("GET %s does not provide header navigation to the board", path)
		}
	}
}

func TestHandlerRendersTaskAndNewTaskLinks(t *testing.T) {
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	for _, definition := range core.LegacyVocabulary().Definitions() {
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

func TestHandlerRendersTextLikeCopyableTaskIDControls(t *testing.T) {
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	if _, ok := initialCardPrefixes(body)[tasks[0].ID]; !ok {
		t.Fatalf("GET / does not retain initial actionable prefix for %q", tasks[0].ID)
	}
	for _, fragment := range []string{
		`type="button"`,
		`class="task-id-copy-group"`,
		`class="task-id-copy"`,
		`data-copy-task-id="` + tasks[0].ID + `"`,
		`aria-label="Copy full task ID ` + tasks[0].ID + `"`,
		`<code>` + presentationForTasks(tasks)[0].IDPrefix + `</code>`,
		`data-copy-status`,
		`role="status"`,
		`aria-live="polite"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
	for _, fragment := range []string{
		`.task-id-copy {`,
		`border: 0`,
		`background: transparent`,
		`cursor: pointer`,
		`padding: 0`,
		`.task-id-copy:hover { color: #2457d6; }`,
		`.task-id-copy:focus-visible`,
		`.task-id-copy-group {`,
		`position: relative`,
		`.copy-status { position: absolute`,
		`.task-route__header .copy-status { top: calc(50% + .1rem); }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("copy control styling does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `<p class="copy-status" data-copy-status`) {
		t.Error("board copy feedback is a page-level block instead of being inline with its task ID")
	}
}

func TestHandlerRequiresCanonicalStatusChoiceForUnknownTask(t *testing.T) {
	node := requireNode(t)
	tasks := boardTasks()
	unknown := tasks[2]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for unknown status: %v\n%s", err, output)
	}
}

func TestHandlerClientMarksDescriptionAsFlexibleField(t *testing.T) {
	node := requireNode(t)
	task := boardTasks()[0]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        []core.Task{task},
		Presentation: presentationForTasks([]core.Task{task}),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(() => {
  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("detail form did not render Description");
  if (!description.parentElement.className.split(/\s+/).includes("field--description")) {
    throw new Error("Description is not marked as the flexible form field");
  }
}, 0);
`
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for flexible Description: %v\n%s", err, output)
	}
}

func TestHandlerClientUsesSharedTaskSidebarLayout(t *testing.T) {
	node := requireNode(t)
	task := boardTasks()[0]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        []core.Task{task},
		Presentation: presentationForTasks([]core.Task{task}),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
function assertSharedLayout(expectedMode) {
  const layout = findElement(main, (element) =>
    (element.className || "").split(/\s+/).includes("task-layout"));
  const editor = layout && findElement(layout, (element) =>
    (element.className || "").split(/\s+/).includes("task-editor"));
  const sidebar = layout && findElement(layout, (element) =>
    (element.className || "").split(/\s+/).includes("task-sidebar"));
  const properties = sidebar && findElement(sidebar, (element) =>
    (element.className || "").split(/\s+/).includes("task-properties"));
  const footer = layout && findElement(layout, (element) =>
    (element.className || "").split(/\s+/).includes("task-actions"));
  const actionBar = footer && findElement(footer, (element) =>
    (element.className || "").split(/\s+/).includes("form-actions"));
  const primaryActions = actionBar && findElement(actionBar, (element) =>
    (element.className || "").split(/\s+/).includes("form-primary-actions"));
  const save = primaryActions && findElement(primaryActions, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Save");
  const back = primaryActions && findElement(primaryActions, (element) =>
    element.tagName === "A" && element.textContent === "Back");
  const danger = actionBar && findElement(actionBar, (element) =>
    (element.className || "").split(/\s+/).includes("form-danger"));
  if (!layout || !editor || !sidebar || !properties || !footer) {
    throw new Error(expectedMode + " does not use the shared task layout");
  }
  if (!actionBar || !primaryActions || !save || !back ||
      primaryActions.parentElement !== actionBar) {
    throw new Error(expectedMode + " does not left-group Save and Back");
  }
  if (expectedMode === "new") {
    if (danger) throw new Error("new task unexpectedly renders destructive actions");
  } else {
    const remove = danger && findElement(danger, (element) =>
      element.tagName === "BUTTON" && element.textContent === "Delete");
    if (!danger || !remove || danger.parentElement !== actionBar ||
        primaryActions.contains(danger) || danger.contains(primaryActions)) {
      throw new Error("detail does not separate Delete from primary actions");
    }
  }
  for (const id of ["task-status", "task-priority", "task-labels"]) {
    const control = findElement(properties, (element) => element.id === id);
    if (!control) throw new Error(id + " is not in Properties");
  }
  const description = editor && findElement(editor, (element) =>
    element.id === "task-description");
  if (!description ||
      !(description.parentElement.className || "").split(/\s+/).includes("field--description")) {
    throw new Error("Description lost its flexible editor hook");
  }
}
setTimeout(async () => {
  assertSharedLayout("new");
  const detailLink = new TestElement("a");
  detailLink.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `);
  await documentEventListeners.click({
    target: detailLink,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() { this.defaultPrevented = true; }
  });
  assertSharedLayout("detail");
}, 0);
`
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for shared task sidebar layout: %v\n%s", err, output)
	}
}

func TestHandlerClientSidebarAccessibilityAndMobileOrder(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000072", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J00000000000000000000073", "Candidate task", core.StatusDone, core.PriorityHigh)
	tasks := []core.Task{current, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	script = strings.Replace(script, "function relationshipRow(", "globalThis.relationshipRow = function relationshipRow(", 1)
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
setTimeout(() => {
  const form = findElement(main, (element) => element.tagName === "FORM");
  const editor = findElement(form, (element) =>
    element.className.split(/\s+/).includes("task-editor"));
  const sidebar = findElement(form, (element) =>
    element.className.split(/\s+/).includes("task-sidebar"));
  const actions = findElement(form, (element) =>
    element.className.split(/\s+/).includes("task-actions"));
  if (!form || !editor || !sidebar || !actions) {
    throw new Error("shared task regions did not render");
  }
  if (sidebar.getAttribute("aria-label") !==
      "Task properties and relationships") {
    throw new Error("sidebar does not identify its contents");
  }

  const groupFor = (headingText) => {
    const heading = findElement(sidebar, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const dependsGroup = groupFor("Depends On");
  const blocksGroup = groupFor("Blocks");
  const dependsInput = findElement(dependsGroup, (element) => element.tagName === "INPUT");
  const blocksInput = findElement(blocksGroup, (element) => element.tagName === "INPUT");
  if (dependsInput.attributes.role !== "combobox" ||
      blocksInput.attributes.role !== "combobox") {
    throw new Error("relationship controls lost combobox semantics");
  }
  for (const group of [dependsGroup, blocksGroup]) {
    const message = findElement(group, (element) =>
      element.className.split(/\s+/).includes("relationship-message"));
    if (!message ||
        message.attributes.role !== "status" ||
        message.attributes["aria-live"] !== "polite") {
      throw new Error("relationship group feedback is not announced politely");
    }
  }
  const formMessage = findElement(actions, (element) =>
    element.className.split(/\s+/).includes("form-status"));
  if (!formMessage ||
      formMessage.attributes.role !== "status" ||
      formMessage.attributes["aria-live"] !== "polite") {
    throw new Error("task form feedback is not announced politely");
  }

  const failedDraft = relationshipRow(
    { id: candidateTask.id, task: candidateTask, error: "not saved" },
    () => {},
    true,
    () => {}
  );
  const removeButton = failedDraft.removeButton;
  const retryButton = failedDraft.retryButton;
  if (removeButton.type !== "button" ||
      retryButton.type !== "button") {
    throw new Error("relationship actions can submit the task form");
  }
  const order = [
    editor,
    sidebar,
    actions
  ].map((element) => form.children.indexOf(element));
  if (!(order[0] < order[1] && order[1] < order[2])) {
    throw new Error("mobile DOM order does not match visual order");
  }
}, 0);
`
	program = strings.Replace(program, "candidateTask.id", strconv.Quote(candidate.ID), 1)
	program = strings.Replace(program, "candidateTask", string(candidateJSON), 1)
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for sidebar accessibility: %v\n%s", err, output)
	}
}

func TestHandlerClientClampsRelationshipListboxPlacement(t *testing.T) {
	node := requireNode(t)
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	script = strings.Replace(script, "function relationshipListboxPlacement(", "globalThis.relationshipListboxPlacement = function relationshipListboxPlacement(", 1)
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	program := clientDOMHarness("/tasks/new", string(document)) + script + `
const bounds = { top: 227, bottom: 504 };
const cases = [
  {
    label: "top scroll position",
    anchor: { top: 250, bottom: 290 },
    want: { side: "below", top: 295, bottom: 504, maxHeight: 209 }
  },
  {
    label: "middle scroll position",
    anchor: { top: 343, bottom: 386 },
    want: { side: "below", top: 391, bottom: 504, maxHeight: 113 }
  },
  {
    label: "bottom scroll position",
    anchor: { top: 440, bottom: 480 },
    want: { side: "above", top: 227, bottom: 435, maxHeight: 208 }
  }
];
for (const testCase of cases) {
  const got = relationshipListboxPlacement(testCase.anchor, bounds, 288, 5);
  if (JSON.stringify(got) !== JSON.stringify(testCase.want)) {
    throw new Error(testCase.label + " placement = " + JSON.stringify(got) +
      ", want " + JSON.stringify(testCase.want));
  }
  if (got.top < bounds.top || got.bottom > bounds.bottom) {
    throw new Error(testCase.label + " escaped the visible sidebar bounds");
  }
}
`
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute relationship listbox placement: %v\n%s", err, output)
	}
}

func TestHandlerClientStagesNewTaskRelationshipsWithoutMutating(t *testing.T) {
	node := requireNode(t)
	dependsCandidate := clientPlacementTask("WB-01J00000000000000000000072", "Depends on candidate", core.StatusDone, core.PriorityHigh)
	blocksCandidate := clientPlacementTask("WB-01J00000000000000000000073", "Blocks candidate", core.StatusBacklog, core.PriorityLow)
	tasks := []core.Task{dependsCandidate, blocksCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
let createCalls = 0;
globalThis.fetch = async (url, options = {}) => {
  fetchCalls.push({ url, options });
  if (url === "/api/tasks" && options.method === "POST") createCalls += 1;
  if ((options.method || "GET") !== "GET") {
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: taskDocument.tasks[0] }) };
  }
  return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
};
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  fetchCalls.splice(0);
  const groupFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const inputFor = (group) => findElement(group, (element) =>
    element.tagName === "INPUT" && element.attributes.role === "combobox");
  const addFor = (group) => findElement(group, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Add dependency");
  const candidateIDs = (group) => findElements(group, (element) =>
    element.attributes.role === "option").map((option) => option.dataset.candidateId);
  const resetCandidates = (group) => {
    const input = inputFor(group);
    input.value = "";
    input.eventListeners.input();
    return candidateIDs(group);
  };
  const selectAndAdd = (group, candidateID, candidateTitle) => {
    const input = inputFor(group);
    input.value = candidateTitle;
    input.eventListeners.input();
    const option = findElement(group, (element) => element.dataset.candidateId === candidateID && element.attributes.role === "option");
    if (!option || !option.eventListeners.click) throw new Error("candidate is not selectable: " + candidateID);
    option.eventListeners.click();
    const add = addFor(group);
    if (!add || add.disabled || !add.eventListeners.click) throw new Error("selected candidate did not enable Add dependency");
    return add.eventListeners.click();
  };
  const assertFormValues = () => {
    const title = findElement(main, (element) => element.id === "task-title");
    const description = findElement(main, (element) => element.id === "task-description");
    if (!title || title.value !== "Unchanged title" || !description || description.value !== "Unchanged description") {
      throw new Error("relationship drafts changed the New Task form values");
    }
  };
  const title = findElement(main, (element) => element.id === "task-title");
  const description = findElement(main, (element) => element.id === "task-description");
  title.value = "Unchanged title";
  description.value = "Unchanged description";
  const dependsGroup = groupFor("Depends On");
  const blocksGroup = groupFor("Blocks");
  const dependsInput = inputFor(dependsGroup);
  let enterPrevented = false;
  dependsInput.eventListeners.keydown({
    key: "Enter",
    preventDefault() { enterPrevented = true; }
  });
  if (!enterPrevented || createCalls !== 0) {
    throw new Error("relationship combobox Enter submitted New Task");
  }

  await selectAndAdd(dependsGroup, ` + strconv.Quote(dependsCandidate.ID) + `, ` + strconv.Quote(dependsCandidate.Title) + `);
  assertFormValues();
  const dependsRow = findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(dependsCandidate.ID) + `);
  if (!dependsRow || dependsRow.dataset.relationshipDraft === undefined ||
      !dependsRow.className.split(/\s+/).includes("relationship-row--compact") ||
      !dependsRow.textContent.includes("Not saved")) {
    throw new Error("Depends On draft did not render as a compact unsaved row");
  }
  if (resetCandidates(dependsGroup).includes(` + strconv.Quote(dependsCandidate.ID) + `) ||
      !resetCandidates(blocksGroup).includes(` + strconv.Quote(dependsCandidate.ID) + `)) {
    throw new Error("Depends On draft did not filter candidates by direction");
  }

  await selectAndAdd(blocksGroup, ` + strconv.Quote(blocksCandidate.ID) + `, ` + strconv.Quote(blocksCandidate.Title) + `);
  assertFormValues();
  const blocksRow = findElement(blocksGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(blocksCandidate.ID) + `);
  if (!blocksRow || blocksRow.dataset.relationshipDraft === undefined ||
      !blocksRow.className.split(/\s+/).includes("relationship-row--compact") ||
      !blocksRow.textContent.includes("Not saved")) {
    throw new Error("Blocks draft did not render as a compact unsaved row");
  }
  if (resetCandidates(blocksGroup).includes(` + strconv.Quote(blocksCandidate.ID) + `) ||
      !resetCandidates(dependsGroup).includes(` + strconv.Quote(blocksCandidate.ID) + `)) {
    throw new Error("Blocks draft did not filter candidates by direction");
  }

  const currentDependsRow = findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(dependsCandidate.ID) + `);
  const remove = currentDependsRow && findElement(currentDependsRow, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!remove || !remove.eventListeners.click) throw new Error("Depends On draft is not removable");
  await remove.eventListeners.click();
  assertFormValues();
  if (findElement(dependsGroup, (element) => element.dataset.relationshipId === ` + strconv.Quote(dependsCandidate.ID) + `)) {
    throw new Error("removing a Depends On draft left its row mounted");
  }
  const dependencyMutations = fetchCalls.filter((call) =>
    /\/api\/tasks\/.+\/dependencies\/.+/.test(call.url) &&
    ["PUT", "DELETE"].includes(call.options.method));
  if (dependencyMutations.length !== 0) {
    throw new Error("New Task relationship drafts wrote before task creation");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute staged New Task relationship behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientRefreshesMountedNewTaskRelationshipCandidates(t *testing.T) {
	node := requireNode(t)
	becomesDeleted := clientPlacementTask("WB-01J00000000000000000000081", "Becomes deleted", core.StatusReady, core.PriorityHigh)
	becomesRestored := clientPlacementTask("WB-01J00000000000000000000082", "Becomes restored", core.StatusBacklog, core.PriorityLow)
	becomesRestored.Deleted = true
	stableCandidate := clientPlacementTask("WB-01J00000000000000000000083", "Stable candidate", core.StatusInProgress, core.PriorityMedium)
	draftCandidate := clientPlacementTask("WB-01J00000000000000000000084", "Draft candidate", core.StatusDone, core.PriorityHigh)
	initialActive := []core.Task{becomesDeleted, stableCandidate, draftCandidate}
	restoredActive := becomesRestored
	restoredActive.Deleted = false
	deletedAfterPoll := becomesDeleted
	deletedAfterPoll.Deleted = true
	afterPollActive := []core.Task{restoredActive, stableCandidate, draftCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialActive, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
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
	deletedDocumentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}

	program := clientDOMHarness("/tasks/new", documentJSON(initialActive)) + script + `
deletedTaskResponse = ` + deletedDocumentJSON([]core.Task{becomesRestored}) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => findElement(main, (element) =>
    element.textContent === headingText).parentElement;
  const inputFor = (group) => findElement(group, (element) =>
    element.tagName === "INPUT" && element.attributes.role === "combobox");
  const addFor = (group) => findElement(group, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Add dependency");
  const selectCandidate = (group, candidateID, query) => {
    const input = inputFor(group);
    input.value = query;
    input.eventListeners.input();
    const option = findElement(group, (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option) throw new Error("candidate is not selectable: " + candidateID);
    option.eventListeners.click();
    return { input, add: addFor(group) };
  };
  const dependsGroup = groupFor("Depends On");
  const blocksGroup = groupFor("Blocks");
  const draftID = ` + strconv.Quote(draftCandidate.ID) + `;
  const deletingID = ` + strconv.Quote(becomesDeleted.ID) + `;
  const restoredID = ` + strconv.Quote(becomesRestored.ID) + `;
  const stableID = ` + strconv.Quote(stableCandidate.ID) + `;

  const draftSelection = selectCandidate(dependsGroup, draftID, ` + strconv.Quote(draftCandidate.Title) + `);
  await draftSelection.add.eventListeners.click();
  const deletingSelection = selectCandidate(dependsGroup, deletingID, ` + strconv.Quote(becomesDeleted.Title) + `);
  const stableSelection = selectCandidate(blocksGroup, stableID, ` + strconv.Quote(stableCandidate.Title) + `);
  const mountedForm = findElement(main, (element) => element.tagName === "FORM");

  let activeDocument = ` + documentJSON(afterPollActive) + `;
  let deletedDocument = ` + deletedDocumentJSON([]core.Task{deletedAfterPoll}) + `;
  let delayedDeletedResolve = null;
  let delayDeleted = false;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      return { ok: true, json: async () => activeDocument };
    }
    if (url === "/api/tasks?deleted=true") {
      if (delayDeleted) {
        delayDeleted = false;
        return {
          ok: true,
          json: async () => new Promise((resolve) => { delayedDeletedResolve = resolve; })
        };
      }
      return { ok: true, json: async () => deletedDocument };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  await intervalCallback();
  if (findElement(main, (element) => element.tagName === "FORM") !== mountedForm) {
    throw new Error("New Task polling replaced the mounted form");
  }
  const draftRow = findElement(dependsGroup, (element) =>
    element.dataset.relationshipId === draftID &&
    element.dataset.relationshipDraft !== undefined);
  if (!draftRow) throw new Error("New Task polling discarded a relationship draft");
  if (deletingSelection.input.value !== ` + strconv.Quote(becomesDeleted.Title) + ` ||
      !deletingSelection.add.disabled) {
    throw new Error("polling did not clear only the selection that became invalid");
  }
  deletingSelection.input.value = "";
  deletingSelection.input.eventListeners.input();
  const restoredOption = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === restoredID);
  const deletedOption = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === deletingID);
  if (!restoredOption || deletedOption) {
    throw new Error("New Task candidates did not reconcile delete and restore polling");
  }
  const stableSelectedOption = findElement(blocksGroup, (element) =>
    element.attributes.role === "option" &&
    element.dataset.candidateId === stableID &&
    element.attributes["aria-selected"] === "true");
  if (stableSelection.input.value !== ` + strconv.Quote(stableCandidate.Title) + ` ||
      stableSelection.add.disabled || !stableSelectedOption) {
    throw new Error("polling discarded a still-valid New Task query or selection");
  }

  delayDeleted = true;
  const stalePoll = intervalCallback();
  for (let attempt = 0; !delayedDeletedResolve && attempt < 20; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!delayedDeletedResolve) throw new Error("stale New Task refresh did not reach deleted context");
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
  const newTaskLink = new TestElement("a");
  newTaskLink.href = "/tasks/new";
  boardView.append(newTaskLink);
  documentEventListeners.click({
    target: newTaskLink,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const currentForm = findElement(main, (element) => element.tagName === "FORM");
  const currentDependsGroup = groupFor("Depends On");
  const currentSelection = selectCandidate(currentDependsGroup, restoredID, ` + strconv.Quote(becomesRestored.Title) + `);
  delayedDeletedResolve(` + deletedDocumentJSON([]core.Task{deletedAfterPoll, becomesRestored}) + `);
  await stalePoll;
  const currentSelectedOption = findElement(currentDependsGroup, (element) =>
    element.attributes.role === "option" &&
    element.dataset.candidateId === restoredID &&
    element.attributes["aria-selected"] === "true");
  if (findElement(main, (element) => element.tagName === "FORM") !== currentForm ||
      currentSelection.input.value !== ` + strconv.Quote(becomesRestored.Title) + ` ||
      currentSelection.add.disabled || !currentSelectedOption) {
    throw new Error("stale detached New Task refresh wrote into the current controller");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute mounted New Task relationship polling: %v\n%s", err, output)
	}
}

func TestHandlerClientCreatesTaskWithBothRelationshipDirections(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask("WB-01J00000000000000000000074", "Prerequisite", core.StatusDone, core.PriorityHigh)
	blockedTask := clientPlacementTask("WB-01J00000000000000000000075", "Blocked task", core.StatusBacklog, core.PriorityLow)
	lateCandidate := clientPlacementTask("WB-01J00000000000000000000080", "Late candidate", core.StatusInProgress, core.PriorityMedium)
	createdID := "WB-01J00000000000000000000076"
	created := clientPlacementTask(createdID, "Created task", core.StatusReady, core.PriorityMedium)
	created.Dependencies = []string{prerequisite.ID}
	refreshedBlockedTask := blockedTask
	refreshedBlockedTask.Dependencies = []string{createdID}
	initialTasks := []core.Task{prerequisite, blockedTask, lateCandidate}
	refreshedTasks := []core.Task{created, prerequisite, refreshedBlockedTask, lateCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
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

	program := clientDOMHarness("/tasks/new", documentJSON(initialTasks)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const selectAndAdd = (group, candidateID, candidateTitle) => {
    const input = findElement(group, (element) => element.tagName === "INPUT" && element.attributes.role === "combobox");
    input.value = candidateTitle;
    input.eventListeners.input();
    const option = findElement(group, (element) => element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option) throw new Error("candidate is not selectable: " + candidateID);
    option.eventListeners.click();
    const add = findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
    if (!add || add.disabled) throw new Error("selected candidate did not enable Add dependency");
    return add.eventListeners.click();
  };
  const prerequisiteID = ` + strconv.Quote(prerequisite.ID) + `;
  const blockedTaskID = ` + strconv.Quote(blockedTask.ID) + `;
  const createdID = ` + strconv.Quote(createdID) + `;
  const createdTask = ` + documentJSON(refreshedTasks) + `.tasks[0];
  const activeRefresh = ` + documentJSON(refreshedTasks) + `;
  const deletedRefresh = ` + string(emptyDeleted) + `;
	  const events = [];
	  let resolvePost;
	  const originalPushState = history.pushState;
	  history.pushState = (...args) => {
	    events.push("navigate");
    return originalPushState(...args);
  };
  globalThis.fetch = async (url, options = {}) => {
	    fetchCalls.push({ url, options });
	    if (url === "/api/tasks" && options.method === "POST") {
	      return {
	        ok: true,
	        json: async () => new Promise((resolve) => {
	          resolvePost = () => {
	            events.push("post");
	            resolve({
	              format: "workbook.task-mutation",
	              version: 1,
	              task: createdTask,
	              warnings: [{ code: "projection-update-failed", message: "Task creation projection needs repair." }]
	            });
	          };
	        })
	      };
    }
    if ((options.method || "GET") === "PUT") {
      const message = url.endsWith("/" + encodeURIComponent(prerequisiteID))
        ? "Depends On projection needs repair."
        : "Blocks projection needs repair.";
      return { ok: true, json: async () => { events.push("put"); return { format: "workbook.task-mutation", version: 1, task: createdTask, warnings: [{ code: "projection-update-failed", message }] }; } };
    }
    if (url === "/api/tasks") {
      return { ok: true, json: async () => { events.push("active-refresh"); return activeRefresh; } };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => { events.push("deleted-refresh"); return deletedRefresh; } };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };
  fetchCalls.splice(0);

	  await selectAndAdd(groupFor("Depends On"), prerequisiteID, ` + strconv.Quote(prerequisite.Title) + `);
	  await selectAndAdd(groupFor("Blocks"), blockedTaskID, ` + strconv.Quote(blockedTask.Title) + `);
	  const dependsGroup = groupFor("Depends On");
	  const blocksGroup = groupFor("Blocks");
	  const lateCandidateID = ` + strconv.Quote(lateCandidate.ID) + `;
	  const dependsInput = findElement(dependsGroup, (element) =>
	    element.tagName === "INPUT" && element.attributes.role === "combobox");
	  dependsInput.value = ` + strconv.Quote(lateCandidate.Title) + `;
	  dependsInput.eventListeners.input();
	  findElement(dependsGroup, (element) =>
	    element.attributes.role === "option" &&
	    element.dataset.candidateId === lateCandidateID).eventListeners.click();
	  const lateAdd = findElement(dependsGroup, (element) =>
	    element.tagName === "BUTTON" && element.textContent === "Add dependency");
	  const capturedBlocksRow = findElement(blocksGroup, (element) =>
	    element.dataset.relationshipId === blockedTaskID &&
	    element.dataset.relationshipDraft !== undefined);
	  const capturedRemove = capturedBlocksRow && findElement(capturedBlocksRow, (element) =>
	    element.tagName === "BUTTON" && element.textContent === "Remove");
	  const form = findElement(main, (element) => element.tagName === "FORM");
	  if (!form || !form.eventListeners.submit) throw new Error("New Task form did not register submit behavior");
	  const submission = form.eventListeners.submit({ preventDefault() {} });
	  for (let attempt = 0; !resolvePost && attempt < 20; attempt += 1) {
	    await new Promise((resolve) => setTimeout(resolve, 0));
	  }
	  if (!resolvePost) throw new Error("New Task POST did not reach its delayed response");
	  const blocksInput = findElement(blocksGroup, (element) =>
	    element.tagName === "INPUT" && element.attributes.role === "combobox");
	  const controlsWereLocked = dependsInput.disabled && blocksInput.disabled &&
	    lateAdd.disabled && capturedRemove && capturedRemove.disabled;
	  await lateAdd.eventListeners.click();
	  await capturedRemove.eventListeners.click();
	  const capturedDependsOnStillVisible = findElement(dependsGroup, (element) =>
	    element.dataset.relationshipId === prerequisiteID &&
	    element.dataset.relationshipDraft !== undefined);
	  const capturedBlocksStillVisible = findElement(blocksGroup, (element) =>
	    element.dataset.relationshipId === blockedTaskID &&
	    element.dataset.relationshipDraft !== undefined);
	  const lateDraft = findElement(dependsGroup, (element) =>
	    element.dataset.relationshipId === lateCandidateID &&
	    element.dataset.relationshipDraft !== undefined);
	  if (!controlsWereLocked || !capturedDependsOnStillVisible ||
	      !capturedBlocksStillVisible || lateDraft) {
	    throw new Error("New Task relationship drafts changed while task creation was in flight");
	  }
	  resolvePost();
	  await submission;

  const assertCall = (method, url, hasBody) => {
    const call = fetchCalls.find((candidate) => candidate.options.method === method && candidate.url === url);
    if (!call) throw new Error("missing " + method + " " + url);
    if (Object.prototype.hasOwnProperty.call(call.options, "body") !== hasBody) {
      throw new Error(method + " " + url + " body presence was wrong");
    }
  };
  assertCall("PUT",
    "/api/tasks/" + encodeURIComponent(createdID) +
    "/dependencies/" + encodeURIComponent(prerequisiteID),
    false);
  assertCall("PUT",
    "/api/tasks/" + encodeURIComponent(blockedTaskID) +
    "/dependencies/" + encodeURIComponent(createdID),
    false);
  const dependencyMutations = fetchCalls.filter((call) =>
    call.options.method === "PUT" && /\/api\/tasks\/.+\/dependencies\/.+/.test(call.url));
  if (dependencyMutations.length !== 2 || dependencyMutations.some((call) =>
      Object.prototype.hasOwnProperty.call(call.options, "body"))) {
    throw new Error("New Task relationship writes were not exactly two bodyless requests");
  }
  const postAt = events.indexOf("post");
  const putIndexes = events.flatMap((event, index) => event === "put" ? [index] : []);
  const activeRefreshAt = events.indexOf("active-refresh");
  const deletedRefreshAt = events.indexOf("deleted-refresh");
  const navigateAt = events.indexOf("navigate");
  if (postAt < 0 || putIndexes.length !== 2 || activeRefreshAt < 0 ||
      deletedRefreshAt < 0 || navigateAt < 0 ||
      !putIndexes.every((putAt) => postAt < putAt && putAt < activeRefreshAt) ||
      !(activeRefreshAt < deletedRefreshAt && deletedRefreshAt < navigateAt)) {
    throw new Error("New Task did not finish both relationship writes before its final refreshes and navigation");
  }
  if (fetchCalls.filter((call) => call.options.method === "POST" && call.url === "/api/tasks").length !== 1) {
    throw new Error("New Task did not issue exactly one task POST");
  }
  if (fetchCalls.filter((call) => (call.options.method || "GET") === "GET" && call.url === "/api/tasks").length !== 1) {
    throw new Error("New Task did not issue exactly one final active refresh");
  }
  if (fetchCalls.filter((call) => (call.options.method || "GET") === "GET" && call.url === "/api/tasks?deleted=true").length !== 1) {
    throw new Error("New Task did not issue exactly one final deleted refresh");
  }
	  if (historyPaths.length !== 1 || historyPaths[0] !== "/tasks/" + encodeURIComponent(createdID)) {
	    throw new Error("New Task did not navigate to the durable created task");
	  }
	  const durableDependsOn = findElement(groupFor("Depends On"), (element) =>
	    element.dataset.relationshipId === prerequisiteID &&
	    element.dataset.relationshipDraft === undefined);
	  const durableBlocks = findElement(groupFor("Blocks"), (element) =>
	    element.dataset.relationshipId === blockedTaskID &&
	    element.dataset.relationshipDraft === undefined);
	  if (!durableDependsOn || !durableBlocks ||
	      findElement(groupFor("Depends On"), (element) =>
	        element.dataset.relationshipId === lateCandidateID)) {
	    throw new Error("visible relationships did not match the captured bodyless mutations");
	  }
  const feedback = findElement(main, (element) => element.className === "form-status" &&
    element.textContent.includes("Task creation projection needs repair.") &&
    element.textContent.includes("Depends On projection needs repair.") &&
    element.textContent.includes("Blocks projection needs repair."));
  if (!feedback) throw new Error("created task detail did not receive durable mutation warnings");
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute created task relationship persistence: %v\n%s", err, output)
	}
}

func TestHandlerClientPreservesNewTaskRelationshipDraftsWhenCreateFails(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask("WB-01J00000000000000000000077", "Prerequisite", core.StatusDone, core.PriorityHigh)
	blockedTask := clientPlacementTask("WB-01J00000000000000000000078", "Blocked task", core.StatusBacklog, core.PriorityLow)
	pendingPrerequisite := clientPlacementTask("WB-01J0000000000000000000007F", "Pending prerequisite", core.StatusReady, core.PriorityMedium)
	pendingBlockedTask := clientPlacementTask("WB-01J0000000000000000000007G", "Pending blocked task", core.StatusInProgress, core.PriorityHigh)
	tasks := []core.Task{prerequisite, blockedTask, pendingPrerequisite, pendingBlockedTask}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => findElement(main, (element) =>
    element.textContent === headingText).parentElement;
  const selectAndAdd = (group, candidateID, candidateTitle) => {
    const input = findElement(group, (element) => element.attributes.role === "combobox");
    input.value = candidateTitle;
    input.eventListeners.input();
    const option = findElement(group, (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    option.eventListeners.click();
    return findElement(group, (element) =>
      element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();
  };
  const dependsGroup = groupFor("Depends On");
  const blocksGroup = groupFor("Blocks");
  await selectAndAdd(dependsGroup, ` + strconv.Quote(prerequisite.ID) + `, ` + strconv.Quote(prerequisite.Title) + `);
  await selectAndAdd(blocksGroup, ` + strconv.Quote(blockedTask.ID) + `, ` + strconv.Quote(blockedTask.Title) + `);
  const title = findElement(main, (element) => element.id === "task-title");
  const description = findElement(main, (element) => element.id === "task-description");
  const status = findElement(main, (element) => element.id === "task-status");
  const priority = findElement(main, (element) => element.id === "task-priority");
  const labels = findElement(main, (element) => element.id === "task-labels");
  const unsavedTitle = "Unsaved title";
  const unsavedDescription = "Unsaved Description";
  title.value = unsavedTitle;
  description.value = unsavedDescription;
  status.children.forEach((option) => { option.selected = option.value === "in-review"; });
  priority.children.forEach((option) => { option.selected = option.value === "high"; });
  labels.value = "review, recovery";
  labels.eventListeners.keydown({ key: "Enter", preventDefault() {} });
  const selectPending = (group, candidateID, candidateTitle) => {
    const input = findElement(group, (element) => element.attributes.role === "combobox");
    input.value = candidateTitle;
    input.eventListeners.input();
    const option = findElement(group, (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option) throw new Error("pending candidate is not selectable: " + candidateID);
    option.eventListeners.click();
    return {
      input,
      add: findElement(group, (element) =>
        element.tagName === "BUTTON" && element.textContent === "Add dependency")
    };
  };
  const pendingDepends = selectPending(
    dependsGroup,
    ` + strconv.Quote(pendingPrerequisite.ID) + `,
    ` + strconv.Quote(pendingPrerequisite.Title) + `
  );
  const pendingBlocks = selectPending(
    blocksGroup,
    ` + strconv.Quote(pendingBlockedTask.ID) + `,
    ` + strconv.Quote(pendingBlockedTask.Title) + `
  );
  let createCalls = 0;
  let dependencyCalls = 0;
  let createdLabels = null;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      createdLabels = JSON.parse(options.body).labels;
      return {
        ok: false,
        json: async () => ({
          format: "workbook.error",
          version: 1,
          error: { category: "validation", message: "title is not valid" }
        })
      };
    }
    if (/\/api\/tasks\/.+\/dependencies\/.+/.test(url)) dependencyCalls += 1;
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  const draftDependsOnRow = findElement(dependsGroup, (element) =>
    element.dataset.relationshipId === ` + strconv.Quote(prerequisite.ID) + ` &&
    element.dataset.relationshipDraft !== undefined);
  const draftBlocksRow = findElement(blocksGroup, (element) =>
    element.dataset.relationshipId === ` + strconv.Quote(blockedTask.ID) + ` &&
    element.dataset.relationshipDraft !== undefined);
  if (title.value !== unsavedTitle ||
      description.value !== unsavedDescription) {
    throw new Error("task fields were discarded after create failure");
  }
  if (JSON.stringify(createdLabels) !== '["review","recovery"]') {
    throw new Error("the create payload did not carry the label chiclets: " + JSON.stringify(createdLabels));
  }
  const chiclets = findElements(main, (element) =>
    Object.hasOwn(element.dataset, "label")).map((chiclet) => chiclet.dataset.label);
  if (status.value !== "in-review" ||
      priority.value !== "high" ||
      JSON.stringify(chiclets) !== '["review","recovery"]') {
    throw new Error("task properties were discarded after create failure: " + JSON.stringify(chiclets));
  }
  const pendingDependsOption = findElement(dependsGroup, (element) =>
    element.attributes.role === "option" &&
    element.dataset.candidateId === ` + strconv.Quote(pendingPrerequisite.ID) + ` &&
    element.attributes["aria-selected"] === "true");
  const pendingBlocksOption = findElement(blocksGroup, (element) =>
    element.attributes.role === "option" &&
    element.dataset.candidateId === ` + strconv.Quote(pendingBlockedTask.ID) + ` &&
    element.attributes["aria-selected"] === "true");
  if (pendingDepends.input.value !== ` + strconv.Quote(pendingPrerequisite.Title) + ` ||
      pendingBlocks.input.value !== ` + strconv.Quote(pendingBlockedTask.Title) + ` ||
      !pendingDependsOption || !pendingBlocksOption ||
      pendingDepends.add.disabled || pendingBlocks.add.disabled) {
    throw new Error("relationship queries or valid selections were discarded after create failure");
  }
  if (!draftDependsOnRow || !draftBlocksRow) {
    throw new Error("relationship drafts were discarded after create failure");
  }
  if (createCalls !== 1 || dependencyCalls !== 0) {
    throw new Error("create failure attempted relationship persistence");
  }
  const removeBlocks = findElement(draftBlocksRow, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Remove");
  await removeBlocks.eventListeners.click();
  if (!findElement(dependsGroup, (element) =>
      element.dataset.relationshipId === ` + strconv.Quote(prerequisite.ID) + ` &&
      element.dataset.relationshipDraft !== undefined)) {
    throw new Error("removing one preserved draft discarded the other");
  }
  const feedback = findElement(main, (element) =>
    element.attributes.role === "status" && element.textContent === "title is not valid");
  const save = findElement(main, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Save");
  if (!feedback || save.disabled) throw new Error("create failure did not restore the current form");
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute failed task creation recovery: %v\n%s", err, output)
	}
}

func TestHandlerClientRetainsFailedRelationshipDraftsAfterCreate(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask("WB-01J00000000000000000000079", "Prerequisite", core.StatusDone, core.PriorityHigh)
	blockedTask := clientPlacementTask("WB-01J0000000000000000000007A", "Blocked task", core.StatusBacklog, core.PriorityLow)
	createdID := "WB-01J0000000000000000000007B"
	created := clientPlacementTask(createdID, "Created task", core.StatusReady, core.PriorityMedium)
	created.Dependencies = []string{prerequisite.ID}
	initialTasks := []core.Task{prerequisite, blockedTask}
	afterCreate := []core.Task{created, prerequisite, blockedTask}
	afterRetryBlocked := blockedTask
	afterRetryBlocked.Dependencies = []string{createdID}
	afterRetry := []core.Task{created, prerequisite, afterRetryBlocked}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
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

	program := clientDOMHarness("/tasks/new", documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => findElement(main, (element) =>
    element.textContent === headingText).parentElement;
  const selectAndAdd = (group, candidateID, candidateTitle) => {
    const input = findElement(group, (element) => element.attributes.role === "combobox");
    input.value = candidateTitle;
    input.eventListeners.input();
    findElement(group, (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID).eventListeners.click();
    return findElement(group, (element) =>
      element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();
  };
  const prerequisiteID = ` + strconv.Quote(prerequisite.ID) + `;
  const blockedTaskID = ` + strconv.Quote(blockedTask.ID) + `;
  const createdID = ` + strconv.Quote(createdID) + `;
  const createdTask = ` + documentJSON(afterCreate) + `.tasks[0];
  let activeDocument = ` + documentJSON(afterCreate) + `;
  let blocksAttempts = 0;
  let blocksDeletes = 0;
  let activeRefreshes = 0;
  let resolveDetachedRetry;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: createdTask }) };
    }
    if (options.method === "PUT" &&
        url === "/api/tasks/" + encodeURIComponent(createdID) + "/dependencies/" + encodeURIComponent(prerequisiteID)) {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: createdTask,
          warnings: [{ code: "projection-update-failed", message: "Depends On projection needs repair." }]
        })
      };
    }
    if (options.method === "PUT" &&
        url === "/api/tasks/" + encodeURIComponent(blockedTaskID) + "/dependencies/" + encodeURIComponent(createdID)) {
      blocksAttempts += 1;
      if (blocksAttempts === 1) {
        return {
          ok: false,
          json: async () => ({
            format: "workbook.error",
            version: 1,
            error: { category: "validation", message: "dependency would create a cycle" }
          })
        };
      }
      if (blocksAttempts === 2) {
        return {
          ok: true,
          json: async () => new Promise((resolve) => {
            resolveDetachedRetry = () => {
              resolve({
                format: "workbook.task-mutation",
                version: 1,
                task: ` + documentJSON(afterRetry) + `.tasks[2]
              });
            };
          })
        };
      }
      throw new Error("unexpected additional Blocks Retry");
    }
    if (options.method === "DELETE" &&
        url === "/api/tasks/" + encodeURIComponent(blockedTaskID) + "/dependencies/" + encodeURIComponent(createdID)) {
      blocksDeletes += 1;
      activeDocument = ` + documentJSON(afterCreate) + `;
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: activeDocument.tasks[2]
        })
      };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      activeRefreshes += 1;
      return { ok: true, json: async () => activeDocument };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => deletedTaskResponse };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  await selectAndAdd(groupFor("Depends On"), prerequisiteID, ` + strconv.Quote(prerequisite.Title) + `);
  await selectAndAdd(groupFor("Blocks"), blockedTaskID, ` + strconv.Quote(blockedTask.Title) + `);
  await findElement(main, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });

  if (historyPaths.at(-1) !== "/tasks/" + encodeURIComponent(createdID)) {
    throw new Error("partial relationship success did not open created task detail");
  }
  const detailDependsGroup = groupFor("Depends On");
  const detailBlocksGroup = groupFor("Blocks");
  const durableDependsOnRow = findElement(detailDependsGroup, (element) =>
    element.dataset.relationshipId === prerequisiteID &&
    element.dataset.relationshipDraft === undefined);
  const failedBlocksRow = findElement(detailBlocksGroup, (element) =>
    element.dataset.relationshipId === blockedTaskID &&
    element.dataset.relationshipDraft !== undefined);
	  const retry = failedBlocksRow && findElement(failedBlocksRow, (element) =>
	    element.tagName === "BUTTON" && element.textContent === "Retry");
	  const remove = failedBlocksRow && findElement(failedBlocksRow, (element) =>
	    element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!durableDependsOnRow) throw new Error("durable Depends On relationship was not rendered");
  if (!failedBlocksRow ||
      !failedBlocksRow.textContent.includes("Not saved") ||
      !failedBlocksRow.textContent.includes("dependency would create a cycle")) {
    throw new Error("failed Blocks relationship was not retained with its error");
	  }
	  if (!retry || !remove) throw new Error("failed relationship draft lacks Retry or Remove");
	  if (document.activeElement !== retry) {
	    throw new Error("partial relationship recovery did not focus the first failed draft Retry");
	  }
  const feedback = findElement(main, (element) =>
    element.className === "form-status" &&
    element.textContent.includes("Task created, but some relationships were not saved.") &&
    element.textContent.includes("Depends On projection needs repair."));
  if (!feedback) throw new Error("created task did not explain its partial relationship success");

  const refreshesBeforeRetry = activeRefreshes;
  const detachedRetry = retry.eventListeners.click();
  for (let attempt = 0; !resolveDetachedRetry && attempt < 20; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!resolveDetachedRetry) {
    throw new Error("Retry did not issue the correctly oriented Blocks PUT");
  }
  const detailStatus = findElement(detailBlocksGroup, (element) =>
    element.className === "relationship-message");
  const back = findElement(main, (element) =>
    element.tagName === "A" && element.textContent === "Back");
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
	  const detachedFocus = new TestElement("button");
	  boardView.append(detachedFocus);
	  detachedFocus.focus();
	  detailStatus.textContent = "detached retry status";
	  resolveDetachedRetry();
	  await detachedRetry;
  if (retry.disabled) throw new Error("detached Retry did not release its busy state");
	  if (detailStatus.textContent !== "detached retry status") {
	    throw new Error("detached Retry wrote to its former controller");
	  }
	  if (document.activeElement !== detachedFocus) {
	    throw new Error("detached partial-recovery work stole focus from the current view");
	  }
  if (activeRefreshes !== refreshesBeforeRetry + 1) {
    throw new Error("detached Retry did not perform its one required refresh");
  }

  const detailLink = new TestElement("a");
  detailLink.href = "/tasks/" + encodeURIComponent(createdID);
  boardView.append(detailLink);
  documentEventListeners.click({
    target: detailLink,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  if (blocksAttempts !== 2) throw new Error("Retry did not issue the correctly oriented Blocks PUT");
  const recoveredDraft = findElement(groupFor("Blocks"), (element) =>
    element.dataset.relationshipId === blockedTaskID &&
    element.dataset.relationshipDraft !== undefined);
  if (!recoveredDraft ||
      !recoveredDraft.textContent.includes("dependency would create a cycle")) {
    throw new Error("detached successful Retry mutated the shared failed draft");
  }
  activeDocument = ` + documentJSON(afterRetry) + `;
  await intervalCallback();
  const retriedBlocksRow = findElement(groupFor("Blocks"), (element) =>
    element.dataset.relationshipId === blockedTaskID);
  if (!retriedBlocksRow || retriedBlocksRow.dataset.relationshipDraft !== undefined) {
    throw new Error("canonical refresh did not replace the detached draft with durable state");
  }
  const removeDurableBlock = findElement(retriedBlocksRow, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Remove");
  await removeDurableBlock.eventListeners.click();
  if (blocksDeletes !== 1 ||
      findElement(groupFor("Blocks"), (element) =>
        element.dataset.relationshipId === blockedTaskID)) {
    throw new Error("removing the durable edge revived stale detached draft state");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute partial relationship creation recovery: %v\n%s", err, output)
	}
}

func TestHandlerClientDoesNotDuplicateCreatedTaskWhenRefreshFails(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask("WB-01J0000000000000000000007C", "Prerequisite", core.StatusDone, core.PriorityHigh)
	blockedTask := clientPlacementTask("WB-01J0000000000000000000007D", "Blocked task", core.StatusBacklog, core.PriorityLow)
	createdID := "WB-01J0000000000000000000007E"
	created := clientPlacementTask(createdID, "Created task", core.StatusReady, core.PriorityMedium)
	initialTasks := []core.Task{prerequisite, blockedTask}
	refreshedTasks := []core.Task{created, prerequisite, blockedTask}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
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

	program := clientDOMHarness("/tasks/new", documentJSON(initialTasks)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => findElement(main, (element) =>
    element.textContent === headingText).parentElement;
  const selectAndAdd = (group, candidateID, candidateTitle) => {
    const input = findElement(group, (element) => element.attributes.role === "combobox");
    input.value = candidateTitle;
    input.eventListeners.input();
    findElement(group, (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID).eventListeners.click();
    return findElement(group, (element) =>
      element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();
  };
  let createCalls = 0;
  let activeRefreshCalls = 0;
  let deletedRefreshCalls = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(refreshedTasks) + `.tasks[0]
        })
      };
    }
    if (options.method === "PUT") {
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(refreshedTasks) + `.tasks[0]
        })
      };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      activeRefreshCalls += 1;
      if (activeRefreshCalls === 1) {
        return {
          ok: false,
          json: async () => ({
            format: "workbook.error",
            version: 1,
            error: { category: "internal", message: "task refresh failed" }
          })
        };
      }
      return { ok: true, json: async () => (` + documentJSON(refreshedTasks) + `) };
    }
    if (url === "/api/tasks?deleted=true") {
      deletedRefreshCalls += 1;
      if (deletedRefreshCalls === 1) {
        return {
          ok: false,
          json: async () => ({
            format: "workbook.error",
            version: 1,
            error: { category: "internal", message: "deleted task context failed" }
          })
        };
      }
      return {
        ok: true,
        json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] })
      };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  await selectAndAdd(groupFor("Depends On"), ` + strconv.Quote(prerequisite.ID) + `, ` + strconv.Quote(prerequisite.Title) + `);
  await selectAndAdd(groupFor("Blocks"), ` + strconv.Quote(blockedTask.ID) + `, ` + strconv.Quote(blockedTask.Title) + `);
  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  const retry = findElement(main, (element) =>
    element.tagName === "BUTTON" && element.type === "button" && element.textContent === "Retry");
  if (!retry) throw new Error("durable create refresh failure did not render a retry action");
  await form.eventListeners.submit({ preventDefault() {} });
  if (createCalls !== 1) {
    throw new Error("second programmatic submit created a duplicate task");
  }
  await retry.eventListeners.click();
  if (deletedRefreshCalls !== 1 || historyPaths.length !== 0 || retry.disabled) {
    throw new Error("deleted-context refresh failure did not remain recoverable");
  }
  await retry.eventListeners.click();
  if (createCalls !== 1) {
    throw new Error("refresh retry created a duplicate task");
  }
  if (deletedRefreshCalls !== 2) {
    throw new Error("deleted-context refresh retry did not recover");
  }
  // The create reported nothing, so the recovered save lands where an
  // uneventful one does: on the board, not on the task just written.
  if (historyPaths.length !== 1 || historyPaths[0] !== "/") {
    throw new Error("refresh retry did not return to the board");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute durable create refresh recovery: %v\n%s", err, output)
	}
}

func TestHandlerClientCreatedTaskRefreshDoesNotNavigateDetachedRoute(t *testing.T) {
	node := requireNode(t)
	blockedTask := clientPlacementTask("WB-01J00000000000000000000085", "Blocked task", core.StatusBacklog, core.PriorityLow)
	otherTask := clientPlacementTask("WB-01J00000000000000000000086", "Other task", core.StatusInProgress, core.PriorityMedium)
	createdID := "WB-01J00000000000000000000087"
	created := clientPlacementTask(createdID, "Created task", core.StatusReady, core.PriorityHigh)
	initialTasks := []core.Task{blockedTask, otherTask}
	refreshedTasks := []core.Task{created, blockedTask, otherTask}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
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

	program := clientDOMHarness("/tasks/new", documentJSON(initialTasks)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => findElement(main, (element) =>
    element.textContent === headingText).parentElement;
  const blocksGroup = groupFor("Blocks");
  const blocksInput = findElement(blocksGroup, (element) =>
    element.tagName === "INPUT" && element.attributes.role === "combobox");
  blocksInput.value = ` + strconv.Quote(blockedTask.Title) + `;
  blocksInput.eventListeners.input();
  const blockedOption = findElement(blocksGroup, (element) =>
    element.attributes.role === "option" &&
    element.dataset.candidateId === ` + strconv.Quote(blockedTask.ID) + `);
  if (!blockedOption) throw new Error("Blocks candidate is not selectable");
  blockedOption.eventListeners.click();
  await findElement(blocksGroup, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();

  const createdID = ` + strconv.Quote(createdID) + `;
  const blockedTaskID = ` + strconv.Quote(blockedTask.ID) + `;
  const otherTaskID = ` + strconv.Quote(otherTask.ID) + `;
  let createCalls = 0;
  let finalRefreshCalls = 0;
  let resolveFinalRefresh = null;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      return {
        ok: true,
        json: async () => ({
          format: "workbook.task-mutation",
          version: 1,
          task: ` + documentJSON(refreshedTasks) + `.tasks[0]
        })
      };
    }
    if (options.method === "PUT" &&
        url === "/api/tasks/" + encodeURIComponent(blockedTaskID) +
          "/dependencies/" + encodeURIComponent(createdID)) {
      return {
        ok: false,
        json: async () => ({
          format: "workbook.error",
          version: 1,
          error: { category: "validation", message: "dependency would create a cycle" }
        })
      };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      finalRefreshCalls += 1;
      return {
        ok: true,
        json: async () => new Promise((resolve) => {
          resolveFinalRefresh = () => resolve(` + documentJSON(refreshedTasks) + `);
        })
      };
    }
    if (url === "/api/tasks?deleted=true") {
      return {
        ok: true,
        json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] })
      };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  const originForm = findElement(main, (element) => element.tagName === "FORM");
  const submission = originForm.eventListeners.submit({ preventDefault() {} });
  for (let attempt = 0; !resolveFinalRefresh && attempt < 20; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!resolveFinalRefresh) throw new Error("created task did not reach its delayed final refresh");

  const otherLink = new TestElement("a");
  otherLink.href = "/tasks/" + encodeURIComponent(otherTaskID);
  boardView.append(otherLink);
  documentEventListeners.click({
    target: otherLink,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  const otherForm = findElement(main, (element) => element.tagName === "FORM");
  const otherTitle = findElement(otherForm, (element) => element.id === "task-title");
  otherTitle.value = "Unsaved other task title";

  resolveFinalRefresh();
  await submission;
  if (createCalls !== 1 || finalRefreshCalls !== 1) {
    throw new Error("detached final refresh changed task creation cardinality");
  }
  if (historyPaths.length !== 1 ||
      historyPaths[0] !== "/tasks/" + encodeURIComponent(otherTaskID) ||
      findElement(main, (element) => element.tagName === "FORM") !== otherForm ||
      otherTitle.value !== "Unsaved other task title") {
    throw new Error("created task final refresh navigated away from the owning current route");
  }

  const createdLink = new TestElement("a");
  createdLink.href = "/tasks/" + encodeURIComponent(createdID);
  boardView.append(createdLink);
  documentEventListeners.click({
    target: createdLink,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() {}
  });
  const staleDraft = findElement(main, (element) =>
    element.dataset.relationshipId === blockedTaskID &&
    element.dataset.relationshipDraft !== undefined);
  const staleMessage = findElement(main, (element) =>
    element.className === "form-status" &&
    element.textContent.includes("Task created, but some relationships were not saved."));
  if (staleDraft || staleMessage) {
    throw new Error("detached created task recovery leaked into a later detail route");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute detached created-task final refresh: %v\n%s", err, output)
	}
}

func TestHandlerShowsRecoverableErrorWhenInitialTaskLoadFails(t *testing.T) {
	node := requireNode(t)
	task := boardTasks()[0]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })

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
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client for initial load recovery: %v\n%s", err, output)
	}
}

func TestHandlerClientRendersDependencyRelationships(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000031", "Current task", core.StatusReady, core.PriorityMedium)
	activeDependency := clientPlacementTask("WB-01J00000000000000000000032", "Active prerequisite", core.StatusDone, core.PriorityHigh)
	activeBlocked := clientPlacementTask("WB-01J00000000000000000000033", "Active blocked task", core.StatusBacklog, core.PriorityLow)
	deletedDependency := clientPlacementTask("WB-01J00000000000000000000034", "Deleted prerequisite", core.StatusReady, core.PriorityMedium)
	deletedDependency.Deleted = true
	missingDependencyID := "WB-01J00000000000000000000036"
	current.Dependencies = []string{activeDependency.ID, deletedDependency.ID, missingDependencyID}
	activeBlocked.Dependencies = []string{current.ID}
	activeTasks := []core.Task{current, activeDependency, activeBlocked}
	deletedTasks := []core.Task{deletedDependency}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return activeTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered dependency relationships: %v\n%s", err, output)
	}
}

func TestHandlerClientMountsCompactRelationshipsInSidebar(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000037", "Current task", core.StatusReady, core.PriorityMedium)
	activeDependency := clientPlacementTask("WB-01J00000000000000000000038", "Active prerequisite", core.StatusDone, core.PriorityHigh)
	activeBlocked := clientPlacementTask("WB-01J00000000000000000000039", "Active blocked task", core.StatusBacklog, core.PriorityLow)
	deletedDependency := clientPlacementTask("WB-01J00000000000000000000040", "Deleted prerequisite", core.StatusReady, core.PriorityMedium)
	deletedDependency.Deleted = true
	deletedBlocked := clientPlacementTask("WB-01J00000000000000000000043", "Deleted blocked task", core.StatusReady, core.PriorityLow)
	deletedBlocked.Deleted = true
	missingDependencyID := "WB-01J00000000000000000000044"
	current.Dependencies = []string{activeDependency.ID, deletedDependency.ID, missingDependencyID}
	activeBlocked.Dependencies = []string{current.ID}
	deletedBlocked.Dependencies = []string{current.ID}
	activeTasks := []core.Task{current, activeDependency, activeBlocked}
	deletedTasks := []core.Task{deletedDependency, deletedBlocked}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return activeTasks, nil })

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
	refreshedCurrent := current
	refreshedCurrent.Dependencies = []string{activeDependency.ID}
	refreshedDocument, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{refreshedCurrent, activeDependency}, Presentation: presentationForTasks([]core.Task{refreshedCurrent, activeDependency})})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+current.ID, string(activeDocument)) + script + `
deletedTaskResponse = ` + string(deletedDocument) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const sidebar = findElement(main, (element) =>
    element.className.split(/\s+/).includes("task-sidebar"));
  const region = findElement(sidebar, (element) =>
    element.className.split(/\s+/).includes("task-relationships"));
  if (!region) throw new Error("Relationships are not mounted in the sidebar");

  const rows = findElements(region, (element) =>
    element.dataset.relationshipRow !== undefined);
  if (!rows.length ||
      rows.some((row) =>
        !row.className.split(/\s+/).includes("relationship-row--compact"))) {
    throw new Error("Relationships did not use compact sidebar rows");
  }

  const missing = findElement(region, (element) => element.dataset.relationshipId === ` + strconv.Quote(missingDependencyID) + `);
  const deletedDependency = findElement(region, (element) => element.dataset.relationshipId === ` + strconv.Quote(deletedDependency.ID) + `);
  const activeBlock = findElement(region, (element) => element.dataset.relationshipId === ` + strconv.Quote(activeBlocked.ID) + `);
  const deletedBlock = findElement(region, (element) => element.dataset.relationshipId === ` + strconv.Quote(deletedBlocked.ID) + `);
  const hasRemove = (row) => findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!missing || !hasRemove(missing) || !deletedDependency || !hasRemove(deletedDependency)) {
    throw new Error("missing or tombstoned Depends On rows were not removable");
  }
  if (!activeBlock || !hasRemove(activeBlock)) {
    throw new Error("active Blocks rows were not removable");
  }
  if (!deletedBlock || hasRemove(deletedBlock)) {
    throw new Error("tombstoned Blocks rows were not read-only");
  }

  const mountedForm = findElement(main, (element) => element.tagName === "FORM");
  taskResponse = ` + string(refreshedDocument) + `;
  await intervalCallback();
  const form = findElement(main, (element) => element.tagName === "FORM");
  const sameForm = form === mountedForm;
  if (!sameForm) throw new Error("relationship refresh replaced the task form");
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered compact sidebar relationships: %v\n%s", err, output)
	}
}

func TestHandlerClientFiltersDependencyComboboxCandidates(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000041", "Current task", core.StatusReady, core.PriorityMedium)
	existingDependency := clientPlacementTask("WB-01J00000000000000000000042", "Existing prerequisite", core.StatusDone, core.PriorityHigh)
	alreadyBlocked := clientPlacementTask("WB-01J00000000000000000000043", "Already blocked", core.StatusBacklog, core.PriorityLow)
	firstCandidate := clientPlacementTask("WB-01J00000000000000000000044", "First candidate", core.StatusBacklog, core.PriorityHigh)
	secondCandidate := clientPlacementTask("WB-01J00000000000000000000045", "Second candidate", core.StatusInProgress, core.PriorityMedium)
	deletedCandidate := clientPlacementTask("WB-01J00000000000000000000046", "Deleted candidate", core.StatusReady, core.PriorityLow)
	current.Dependencies = []string{existingDependency.ID}
	alreadyBlocked.Dependencies = []string{current.ID}
	deletedCandidate.Deleted = true
	tasks := []core.Task{current, existingDependency, alreadyBlocked, firstCandidate, secondCandidate, deletedCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency combobox candidate filtering: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencySnapshotPrefersTombstones(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000047", "Current task", core.StatusReady, core.PriorityMedium)
	activeDependency := clientPlacementTask("WB-01J00000000000000000000048", "Active prerequisite copy", core.StatusDone, core.PriorityHigh)
	activeBlocked := clientPlacementTask("WB-01J00000000000000000000049", "Active blocked copy", core.StatusBacklog, core.PriorityLow)
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
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return activeTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute conflicting relationship snapshot behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyComboboxSelectionCollapseIsCoherent(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000004B", "Current task", core.StatusReady, core.PriorityMedium)
	pointerCandidate := clientPlacementTask("WB-01J0000000000000000000004C", "Pointer candidate", core.StatusDone, core.PriorityHigh)
	keyboardCandidate := clientPlacementTask("WB-01J0000000000000000000004D", "Keyboard candidate", core.StatusBacklog, core.PriorityLow)
	tasks := []core.Task{current, pointerCandidate, keyboardCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute coherent dependency combobox collapse: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyComboboxScrollsKeyboardOptionIntoView(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000004E", "Current task", core.StatusReady, core.PriorityMedium)
	first := clientPlacementTask("WB-01J0000000000000000000004F", "First candidate", core.StatusDone, core.PriorityHigh)
	second := clientPlacementTask("WB-01J0000000000000000000004G", "Second candidate", core.StatusBacklog, core.PriorityLow)
	third := clientPlacementTask("WB-01J0000000000000000000004H", "Third candidate", core.StatusBacklog, core.PriorityMedium)
	fourth := clientPlacementTask("WB-01J0000000000000000000004J", "Fourth candidate", core.StatusInProgress, core.PriorityHigh)
	tasks := []core.Task{current, first, second, third, fourth}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency keyboard option scrolling: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyComboboxDismissesOnLostFocus(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000090", "Current task", core.StatusReady, core.PriorityMedium)
	first := clientPlacementTask("WB-01J00000000000000000000091", "First candidate", core.StatusDone, core.PriorityHigh)
	second := clientPlacementTask("WB-01J00000000000000000000092", "Second candidate", core.StatusBacklog, core.PriorityLow)
	tasks := []core.Task{current, first, second}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const partsFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    const group = heading.parentElement;
    const input = findElement(group, (element) => element.tagName === "INPUT" && element.attributes.role === "combobox");
    const listbox = findElement(group, (element) => element.attributes.role === "listbox");
    const add = findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
    return { group, input, listbox, add, region: input.parentElement };
  };
  const assertOpen = (parts, label) => {
    if (parts.listbox.hidden || parts.input.attributes["aria-expanded"] !== "true") {
      throw new Error(label + " did not leave the popup open");
    }
  };
  const assertClosed = (parts, label) => {
    if (!parts.listbox.hidden || parts.input.attributes["aria-expanded"] !== "false") {
      throw new Error(label + " left the popup covering the rest of the page");
    }
  };

  const outside = findElement(main, (element) => element.id === "task-title");
  if (!outside) throw new Error("missing a field outside the relationship editor");

  ["Depends On", "Blocks"].forEach((headingText) => {
    const parts = partsFor(headingText);
    if (parts.listbox.parentElement !== parts.region) {
      throw new Error(headingText + " listbox is not mounted inside the combobox region");
    }
    if (typeof parts.region.eventListeners.focusout !== "function") {
      throw new Error(headingText + " combobox does not watch for lost focus");
    }

    parts.input.eventListeners.focus();
    parts.input.eventListeners.keydown({ key: "ArrowDown", preventDefault() {} });
    assertOpen(parts, headingText + " focus");
    if (!parts.input.attributes["aria-activedescendant"]) {
      throw new Error(headingText + " did not activate a candidate before losing focus");
    }
    parts.region.eventListeners.focusout({ relatedTarget: outside });
    assertClosed(parts, headingText + " focus moving to another field");
    if (Object.prototype.hasOwnProperty.call(parts.input.attributes, "aria-activedescendant")) {
      throw new Error(headingText + " kept an active descendant after losing focus");
    }
    if (documentEventListeners.scroll) {
      throw new Error(headingText + " kept repositioning a dismissed popup on scroll");
    }

    // A press on the still focused input fires no focus event, so the popup
    // has to reopen from the click itself or it can never be reopened.
    parts.input.eventListeners.click();
    assertOpen(parts, headingText + " clicking the already focused input");

    parts.region.eventListeners.focusout({ relatedTarget: null });
    assertClosed(parts, headingText + " focus leaving for nothing focusable");

    parts.input.eventListeners.focus();
    parts.region.eventListeners.focusout({ relatedTarget: parts.add });
    assertOpen(parts, headingText + " focus moving to Add dependency");
    parts.region.eventListeners.focusout({ relatedTarget: parts.listbox });
    assertOpen(parts, headingText + " focus moving into the popup itself");

    // Escape has to be claimed whether or not the popup is open. This is a
    // search field, so the browser's default is to clear it, and the input
    // event that fires reopens the popup: unclaimed, a second Escape summons
    // back the very popup the first one dismissed.
    parts.input.value = "Second";
    parts.input.eventListeners.input();
    assertOpen(parts, headingText + " typing a query");
    let escapePrevented = false;
    parts.input.eventListeners.keydown({ key: "Escape", preventDefault() { escapePrevented = true; } });
    assertClosed(parts, headingText + " Escape");
    if (!escapePrevented) {
      throw new Error(headingText + " left Escape to the search field's native clear, which reopens the popup");
    }
    if (parts.input.value !== "Second") {
      throw new Error(headingText + " threw the query away on the Escape that only had to close the popup");
    }
    escapePrevented = false;
    parts.input.eventListeners.keydown({ key: "Escape", preventDefault() { escapePrevented = true; } });
    if (!escapePrevented) {
      throw new Error(headingText + " left the second Escape to the native clear, whose input event reopens the popup");
    }
    if (parts.input.value !== "") {
      throw new Error(headingText + " claimed Escape without clearing the query the native default would have");
    }
    assertClosed(parts, headingText + " Escape on an already dismissed popup");
    // Nothing left to clear, so the key is nobody's business but the browser's.
    escapePrevented = false;
    parts.input.eventListeners.keydown({ key: "Escape", preventDefault() { escapePrevented = true; } });
    if (escapePrevented) {
      throw new Error(headingText + " swallowed Escape with no popup open and no query to clear");
    }

    parts.input.eventListeners.click();
    assertOpen(parts, headingText + " reopening after Escape");

    // A pointer press inside the popup must not blur the input, because blur
    // runs before click and would hide the option out from under the very
    // click that selects it.
    let prevented = false;
    parts.listbox.eventListeners.mousedown({ preventDefault() { prevented = true; } });
    if (!prevented) {
      throw new Error(headingText + " popup lets a pointer press blur the input before the click lands");
    }
    const option = findElement(parts.listbox, (element) => element.attributes.role === "option");
    if (!option) throw new Error(headingText + " popup rendered no options to select");
    const chosen = taskDocument.tasks.find((task) => task.id === option.dataset.candidateId);
    option.eventListeners.click();
    if (parts.input.value !== chosen.title || parts.add.disabled) {
      throw new Error(headingText + " pointer selection did not survive lost-focus dismissal");
    }
    assertClosed(parts, headingText + " selecting an option");
  });
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency combobox lost-focus dismissal: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationOrientationAndRefresh(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000051", "Current task", core.StatusReady, core.PriorityMedium)
	existingDependency := clientPlacementTask("WB-01J00000000000000000000052", "Existing prerequisite", core.StatusDone, core.PriorityHigh)
	existingBlocked := clientPlacementTask("WB-01J00000000000000000000053", "Existing blocked task", core.StatusBacklog, core.PriorityLow)
	dependsCandidate := clientPlacementTask("WB-01J00000000000000000000054", "New prerequisite", core.StatusBacklog, core.PriorityHigh)
	blocksCandidate := clientPlacementTask("WB-01J00000000000000000000055", "New blocked task", core.StatusInProgress, core.PriorityMedium)
	current.Dependencies = []string{existingDependency.ID}
	existingBlocked.Dependencies = []string{current.ID}
	initialTasks := []core.Task{current, existingDependency, existingBlocked, dependsCandidate, blocksCandidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency mutation orientation and refresh: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationFollowsSupersedingRefresh(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000056", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J00000000000000000000057", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute superseding dependency refresh behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationSettlesAfterControllerSupersession(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000005A", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005B", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := exec.CommandContext(commandContext, node, "-")
	command.Stdin = strings.NewReader(program)
	if output, err := command.CombinedOutput(); err != nil {
		if commandContext.Err() == context.DeadlineExceeded {
			t.Fatalf("dependency mutation did not settle after controller-only supersession")
		}
		t.Fatalf("execute controller-superseded dependency mutation: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationDoesNotWriteDetachedGroupAfterNewerPoll(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000005C", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005D", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute detached dependency mutation with newer poll: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationErrorDoesNotWriteDetachedGroup(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000005E", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J0000000000000000000005F", "Candidate task", core.StatusDone, core.PriorityHigh)
	tasks := []core.Task{current, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute detached dependency mutation error: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyMutationReportsDeletedContextFailure(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000058", "Current task", core.StatusReady, core.PriorityMedium)
	candidate := clientPlacementTask("WB-01J00000000000000000000059", "Candidate task", core.StatusDone, core.PriorityHigh)
	initialTasks := []core.Task{current, candidate}
	updatedCurrent := current
	updatedCurrent.Dependencies = []string{candidate.ID}
	updatedTasks := []core.Task{updatedCurrent, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute deleted-context dependency mutation feedback: %v\n%s", err, output)
	}
}

func TestHandlerClientDependencyFailureRecoveryAndKeyboard(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000061", "Current task", core.StatusReady, core.PriorityMedium)
	alpha := clientPlacementTask("WB-01J00000000000000000000062", "Alpha prerequisite", core.StatusDone, core.PriorityHigh)
	beta := clientPlacementTask("WB-01J00000000000000000000063", "Beta blocked task", core.StatusBacklog, core.PriorityLow)
	initialTasks := []core.Task{current, alpha, beta}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute dependency failure recovery and keyboard behavior: %v\n%s", err, output)
	}
}

func TestHandlerInterceptsOrdinarySameOriginNavigation(t *testing.T) {
	node := requireNode(t)
	task := boardTasks()[0]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })

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
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client navigation behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientCopiesFullTaskIDsAndSeparatesDrag(t *testing.T) {
	node := requireNode(t)
	tasks := boardTasks()[:1]
	task := tasks[0]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
  const taskID = ` + strconv.Quote(task.ID) + `;
  const visibleID = ` + strconv.Quote(presentationForTasks(tasks)[0].IDPrefix) + `;
  let boardCopy = boardLists.map((list) => findElement(list, (element) => element.dataset.copyTaskId === taskID)).find(Boolean);
  if (!boardCopy || boardCopy.tagName !== "BUTTON") throw new Error("board did not render a copy button for the task ID");
  let boardStatus = boardCopy.closest(".task-id-copy-group").querySelector("[data-copy-status]");
  if (!boardStatus || boardStatus.parentElement !== boardCopy.parentElement) {
    throw new Error("board copy feedback is not inline with its task ID");
  }
  if (!boardCopy.firstElementChild || boardCopy.firstElementChild.tagName !== "CODE" || boardCopy.firstElementChild.textContent !== visibleID) {
    throw new Error("board copy control did not render the actionable task ID prefix");
  }
  if (!boardCopy.attributes["aria-label"].includes(taskID)) throw new Error("board copy control lacks the full-ID accessible label");
  const clickEvent = (target) => ({
    target,
    button: 0,
    defaultPrevented: false,
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    preventDefault() { this.defaultPrevented = true; }
  });

  await documentEventListeners.click(clickEvent(boardCopy));
  if (JSON.stringify(clipboardWrites) !== JSON.stringify([taskID])) {
    throw new Error("board ID did not copy the full task ID");
  }
  if (boardStatus.attributes.role !== "status" ||
      boardStatus.attributes["aria-live"] !== "polite" ||
      boardStatus.textContent !== "Copied" ||
      boardStatus.attributes["aria-label"] !== "Copied task ID " + taskID + ".") {
    throw new Error("board copy did not render accessible success feedback");
  }
  await intervalCallback();
  boardCopy = boardLists.map((list) => findElement(list, (element) => element.dataset.copyTaskId === taskID)).find(Boolean);
  boardStatus = boardCopy.closest(".task-id-copy-group").querySelector("[data-copy-status]");
  if (boardStatus.textContent !== "Copied" ||
      boardStatus.attributes["aria-label"] !== "Copied task ID " + taskID + ".") {
    throw new Error("board refresh discarded active inline copy feedback");
  }

  clipboardWrites.splice(0);
  const dataTransfer = { effectAllowed: "", setData() {} };
  documentEventListeners.dragstart({ target: boardCopy, dataTransfer });
  documentEventListeners.dragend({ target: boardCopy });
  await documentEventListeners.click(clickEvent(boardCopy));
  if (clipboardWrites.length !== 0) throw new Error("post-drag click copied the task ID");
  await documentEventListeners.click(clickEvent(boardCopy));
  if (JSON.stringify(clipboardWrites) !== JSON.stringify([taskID])) {
    throw new Error("later intentional activation did not copy the full task ID");
  }

  const titleLink = findElement(boardCopy.closest(".task-card"), (element) => element.tagName === "A");
  await documentEventListeners.click(clickEvent(titleLink));
  const detailCopy = findElement(main, (element) => element.dataset.copyTaskId === taskID);
  const detailGroup = detailCopy && detailCopy.closest(".task-id-copy-group");
  const detailStatus = detailGroup && detailGroup.querySelector("[data-copy-status]");
  if (!detailCopy || !detailStatus || detailStatus.parentElement !== detailCopy.parentElement) {
    throw new Error("task detail did not render feedback inline with its task ID");
  }
  await documentEventListeners.click(clickEvent(detailCopy));
  if (JSON.stringify(clipboardWrites) !== JSON.stringify([taskID, taskID])) {
    throw new Error("detail ID did not copy the full task ID");
  }
  if (detailStatus.textContent !== "Copied" ||
      detailStatus.attributes["aria-label"] !== "Copied task ID " + taskID + ".") {
    throw new Error("detail copy did not render view-local success feedback");
  }

  clipboardError = new Error("denied");
  await documentEventListeners.click(clickEvent(detailCopy));
  if (detailStatus.textContent !== "Copy failed" ||
      !detailStatus.attributes["aria-label"].includes("Could not copy task ID " + taskID) ||
      clipboardWrites.length !== 2) {
    throw new Error("detail copy failure did not provide accessible feedback");
  }

  const backLink = findElement(detailCopy.closest(".task-route"), (element) => element.tagName === "A" && element.href === "/");
  await documentEventListeners.click(clickEvent(backLink));
  windowTimeouts
    .filter((timer) => !timer.canceled && timer.delay === 4000)
    .forEach((timer) => { timer.canceled = true; timer.callback(); });
  if (boardStatus.textContent || detailStatus.textContent) {
    throw new Error("copy feedback remained stale after board-detail-board timer lifecycle");
  }
}, 0);
`
	command := nodeCommand(node, program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client copy behavior: %v\n%s", err, output)
	}
}

// A task whose status matches no column is shown under its own heading rather
// than dropped, which is what the terminal board has always done. See
// internal/presentation/parity_test.go for why the two boards owe each other
// that. The region is a display, not a seventh status: it takes no drops, its
// cards do not drag, and a status the board does know pulls the card back into
// the column that owns it.
func TestHandlerClientBoardSurfacesUnknownStatuses(t *testing.T) {
	node := requireNode(t)
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: taskPresentation(tasks, core.LegacyVocabulary()),
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(documentJSON)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const card = findElement(ready, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[0].ID) + `);
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

  const inColumn = boardLists.map((list) => findElement(list, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `)).find(Boolean);
  if (inColumn) throw new Error("unknown-status task rendered in a status column, which would misreport its status");
  const stranded = findElement(boardUnknownList, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `);
  if (!stranded) throw new Error("unknown-status task did not render anywhere on the board");
  if (!stranded.textContent.includes("Future status task")) throw new Error("the unknown-status card does not name its task");
  if (boardUnknownCount.textContent !== "1") throw new Error("unknown-status count = " + boardUnknownCount.textContent + ", want 1");
  if (boardUnknownSection.dataset.visible !== "true") throw new Error("the unknown-status region stayed hidden while holding a task");
  if (stranded.draggable !== true) throw new Error("an unknown-status card offers no drag, so there is no way out of the region");
  if (stranded.getAttribute("aria-label") !== "Move task Future status task out of the unrecognized status future-status") {
    throw new Error("the unknown-status card does not say what dragging it would do: " + stranded.getAttribute("aria-label"));
  }
  // Whether the region carries data-drop-status is a fact about the server
  // template, and this harness builds its own region node, so checking it here
  // would only confirm the harness against itself. It is asserted against the
  // rendered page in assertBoardStatusMarkersMatchColumns instead.
  if (stale.dataset.visible !== "false") throw new Error("unknown-status task triggered the stale state");

  // Giving the task a status this build knows moves the very same node into that
  // column, so the recovery keeps focus, and empties the region again.
  taskResponse = {
    format: "workbook.tasks",
    version: 1,
    tasks: taskDocument.tasks.map((task) => task.id !== ` + strconv.Quote(tasks[2].ID) + ` ? task : Object.assign({}, task, { status: "done" })),
    presentation: taskDocument.presentation
  };
  await intervalCallback();
  const done = boardLists.find((list) => list.dataset.status === "done");
  if (findElement(done, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `) !== stranded) {
    throw new Error("a recognized status rebuilt the card instead of moving the one already rendered");
  }
  if (findElement(boardUnknownList, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `)) {
    throw new Error("the card stayed in the unknown-status region after its status became known");
  }
  if (stranded.getAttribute("aria-label") !== "Move task Future status task from done") {
    throw new Error("a recovered card still reads as stranded: " + stranded.getAttribute("aria-label"));
  }
  if (boardUnknownCount.textContent !== "0") throw new Error("the emptied region still counts " + boardUnknownCount.textContent);
  if (boardUnknownSection.dataset.visible !== "false") throw new Error("the emptied unknown-status region stayed visible");
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered unknown-status board region: %v\n%s", err, output)
	}
}

// Which region a card lands in and what its label says about the move are the
// same question, so they have to be answered from the same set: the columns the
// server actually rendered.
//
// This board is missing the Blocked column — what dropping the default Blocked
// status produces, and what a per-project column set produces routinely — while
// the task document still carries a Blocked task, presented against a
// vocabulary in which `blocked` is perfectly live. Everything except the
// rendered columns therefore says this task is fine, which is what makes "the
// rendered columns are the only source consulted" a checkable claim rather than
// a restatement of the code.
//
// The discriminator this actually holds is the one that has a plausible wrong
// answer left: deciding the label from the node's current parent instead of
// from the status. applyCard labels a card before card() has inserted it
// anywhere, so a parent-derived answer strands every card on a first render.
// Replacing the status test with `article.parentElement === unknownList` fails
// here with "a card in the unknown-status region claims a column it is not in",
// and fails TestHandlerClientBoardSurfacesUnknownStatuses and
// TestHandlerClientDragsACardOutOfTheUnknownStatusRegion with it.
//
// What it does not discriminate — and used to claim it did — is a client
// reading a hardcoded status list of its own. There is no such list any more:
// `statuses` and `lists` are both built from these same column nodes, so
// splicing a column out takes it out of both and the injected bug is a no-op.
//
// Every card drags, including this one. Only the label distinguishes them, so
// the label is what this asserts.
func TestHandlerClientDragsOnlyOutOfRenderedColumns(t *testing.T) {
	node := requireNode(t)
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	documentJSON, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: taskPresentation(tasks, core.LegacyVocabulary()),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Take the Blocked column out of the rendered board before the script reads
	// it, and leave the script itself alone. That is the drift: the page the
	// server emitted and the constant the client ships no longer agree.
	withoutBlockedColumn := `
const blockedColumn = boardStatuses.indexOf("blocked");
boardStatuses.splice(blockedColumn, 1);
boardLists.splice(blockedColumn, 1);
boardCounts.splice(blockedColumn, 1);
`

	program := clientDOMHarness("/", string(documentJSON)) + withoutBlockedColumn + script + `
setTimeout(async () => {
  const blockedID = ` + strconv.Quote(tasks[1].ID) + `;
  if (boardLists.some((list) => list.dataset.status === "blocked")) {
    throw new Error("the harness still renders a Blocked column, so nothing is being tested");
  }
  const inColumn = boardLists.map((list) => findElement(list, (element) => element.dataset.taskId === blockedID)).find(Boolean);
  if (inColumn) throw new Error("a task landed in a column this board does not render");
  const stranded = findElement(boardUnknownList, (element) => element.dataset.taskId === blockedID);
  if (!stranded) throw new Error("a task whose column the board does not render vanished from the board");
  if (stranded.draggable !== true) {
    throw new Error("a card in the unknown-status region offers no drag out of it");
  }
  if (stranded.getAttribute("aria-label") !== "Move task Blocked task out of the unrecognized status blocked") {
    throw new Error("a card in the unknown-status region claims a column it is not in: " + stranded.getAttribute("aria-label"));
  }
  if (boardUnknownCount.textContent !== "2") {
    throw new Error("unknown-status count = " + boardUnknownCount.textContent + ", want 2");
  }
  // The cards in the columns the board does render are unaffected.
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const readyCard = findElement(ready, (element) => element.dataset.taskId === ` + strconv.Quote(tasks[0].ID) + `);
  if (!readyCard) throw new Error("a rendered column lost its card");
  if (readyCard.draggable !== true) throw new Error("a card in a rendered column stopped being draggable");
  if (readyCard.getAttribute("aria-label") !== "Move task Ready task from ready") {
    throw new Error("a card in a rendered column stopped announcing its move: " + readyCard.getAttribute("aria-label"));
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered column-derived drag behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientPollsEverySecond(t *testing.T) {
	node := requireNode(t)
	tasks := boardTasks()[:1]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered polling behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientPlacementClampsSameColumnPointerGapsToSamePriorityPeers(t *testing.T) {
	node := requireNode(t)
	high := clientPlacementTask("WB-01J00000000000000000000011", "High", core.StatusReady, core.PriorityHigh)
	moved := clientPlacementTask("WB-01J00000000000000000000012", "Moved medium", core.StatusReady, core.PriorityMedium)
	firstMedium := clientPlacementTask("WB-01J00000000000000000000013", "First medium", core.StatusReady, core.PriorityMedium)
	low := clientPlacementTask("WB-01J00000000000000000000014", "Low", core.StatusReady, core.PriorityLow)
	tasks := []core.Task{high, moved, firstMedium, low}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered same-column placement behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientSendsAtomicClampedPlacementRequests(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000021", "Moved medium", core.StatusReady, core.PriorityMedium)
	destinationHigh := clientPlacementTask("WB-01J00000000000000000000022", "In progress high", core.StatusInProgress, core.PriorityHigh)
	destinationMedium := clientPlacementTask("WB-01J00000000000000000000023", "In progress medium", core.StatusInProgress, core.PriorityMedium)
	doneHigh := clientPlacementTask("WB-01J00000000000000000000024", "Done high", core.StatusDone, core.PriorityHigh)
	doneLow := clientPlacementTask("WB-01J00000000000000000000025", "Done low", core.StatusDone, core.PriorityLow)
	tasks := []core.Task{moved, destinationHigh, destinationMedium, doneHigh, doneLow}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered atomic placement behavior: %v\n%s", err, output)
	}
}

func TestHandlerRefreshesTasksOnEveryAPIRequest(t *testing.T) {
	first := boardTasks()
	second := append([]core.Task(nil), first...)
	second[0].Title = "Updated without restarting"
	calls := 0
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return second, nil
		},
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
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

// requireNode resolves the node binary the embedded client behavior tests
// execute. Without one the test skips as a marked missing capability, and
// fails instead when the environment requires every capability present.
func requireNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		testenv.MissingCapability(t, "node is required to execute the embedded client behavior")
	}
	return node
}

// nodeCommand feeds the program to node over stdin rather than as an argument.
// The client programs embed assets/index.html, and Linux caps one argv string
// at 128 KiB (MAX_ARG_STRLEN), a ceiling the largest programs have outgrown;
// stdin has no such limit and behaves identically on every platform CI runs.
func nodeCommand(node, program string) *exec.Cmd {
	command := exec.Command(node, "-")
	command.Stdin = strings.NewReader(program)
	return command
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

// clientDOMHarness builds the fake DOM for a board serving the statuses a
// project with no configuration ledger is using, which is what a handler built
// without a vocabulary resolver renders and what every test that is not about
// the vocabulary wants.
func clientDOMHarness(path, taskDocument string) string {
	return clientDOMHarnessWith(path, taskDocument, core.LegacyVocabulary(), "")
}

// clientDOMHarnessWith builds the fake DOM the server would have rendered for a
// project with these statuses.
//
// The columns, their labels, the default status and the ledger head are all
// attributes on the real page, and the script reads every one of them out of
// the DOM rather than holding a copy — so a harness that hard-coded six
// statuses would be testing a page the server no longer serves.
func clientDOMHarnessWith(path, taskDocument string, vocabulary core.Vocabulary, vocabularyHead string) string {
	pairs := make([][2]string, 0, len(vocabulary.Definitions()))
	for _, definition := range vocabulary.Definitions() {
		pairs = append(pairs, [2]string{string(definition.Status), definition.Label})
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		panic("webui: encode harness vocabulary: " + err.Error())
	}
	tagNames := make([]string, 0, len(core.StatusTags()))
	for _, tag := range core.StatusTags() {
		tagNames = append(tagNames, string(tag))
	}
	return `
const boardStatusTags = ` + strconv.Quote(strings.Join(tagNames, " ")) + `;
const boardStatusDefinitions = ` + string(encoded) + `;
const boardDefaultStatus = ` + strconv.Quote(string(vocabulary.Default())) + `;
const boardVocabularyHead = ` + strconv.Quote(vocabularyHead) + `;
// The attachment ceiling the server renders into the page, so the harness's
// upload control refuses what the real one refuses rather than what a number
// invented here would.
const boardAttachmentLimit = ` + strconv.Itoa(core.MaxAttachmentFileBytes) + `;
const scrollIntoViewCalls = [];
function classTokens(element) {
  return (element.className || "").split(/\s+/).filter(Boolean);
}
function writeClassTokens(element, tokens) {
  element.className = tokens.join(" ");
}
function hasClassToken(element, name) {
  return classTokens(element).includes(name);
}
class TestElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.className = "";
    this.dataset = {};
    this.attributes = {};
    this.style = {};
    this.classList = {
      contains: (name) => classTokens(this).includes(name),
      add: (...names) => { writeClassTokens(this, classTokens(this).concat(names.filter((name) => !classTokens(this).includes(name)))); },
      remove: (...names) => { writeClassTokens(this, classTokens(this).filter((token) => !names.includes(token))); },
      toggle: (name, force) => {
        const present = classTokens(this).includes(name);
        const next = force === undefined ? !present : Boolean(force);
        if (next) this.classList.add(name); else this.classList.remove(name);
        return next;
      }
    };
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
    [...this.children].forEach((child) => child.remove());
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
    // A browser blurs a focused element the moment it leaves the document, so
    // the harness has to as well: without this a destroy-and-rebuild render
    // looks like it preserved focus when it actually dropped it on the floor.
    if (globalThis.activeElement && this.contains(globalThis.activeElement)) globalThis.activeElement = null;
  }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; }
  hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attributes, name); }
  focus() { globalThis.activeElement = this; }
  get id() { return this.attributes.id || this._id || ""; }
  set id(value) { this._id = String(value); this.attributes.id = String(value); }
  addEventListener(name, listener) { this.eventListeners[name] = listener; }
  removeEventListener(name, listener) {
    if (this.eventListeners[name] === listener) delete this.eventListeners[name];
  }
  closest(selector) {
    const hasClass = hasClassToken;
    for (let element = this; element; element = element.parentElement) {
      if (selector === "a[href]" && element.tagName === "A" && element.href) return element;
      if (selector === ".task-card" && hasClass(element, "task-card")) return element;
      if (selector === ".task-route" && hasClass(element, "task-route")) return element;
      if (selector === ".task-sidebar" && hasClass(element, "task-sidebar")) return element;
      if (selector === ".task-id-copy-group" && hasClass(element, "task-id-copy-group")) return element;
      if (selector === "[data-copy-task-id]" && Object.prototype.hasOwnProperty.call(element.dataset, "copyTaskId")) return element;
      if (selector === "[data-drop-status]" && element.dataset.dropStatus) return element;
      if (selector === "[data-drop-deleted]" && element.dataset.dropDeleted) return element;
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
    if (selector === "[data-copy-status]") return findElement(this, (element) => Object.prototype.hasOwnProperty.call(element.dataset, "copyStatus"));
    if (selector.startsWith("#")) {
      const id = selector.slice(1);
      return findElement(this, (element) => element.id === id);
    }
    return null;
  }
  querySelectorAll(selector) {
    const matches = [];
    const visit = (element) => {
      for (const child of element.children || []) {
        if (selector === ".task-card" && hasClassToken(child, "task-card")) matches.push(child);
        if (selector === "[role=\"option\"]" && child.attributes.role === "option") matches.push(child);
        if (selector === "[data-relationship-row]" && Object.hasOwn(child.dataset, "relationshipRow")) matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
  getBoundingClientRect() { return this.rect || { top: 0, right: 0, bottom: 0, left: 0, width: 0 }; }
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
const boardStatuses = boardStatusDefinitions.map(([status]) => status);
// The grid the columns are tracks of. Each column is a section holding the
// list, exactly as the server renders it, because the Deleted column is
// appended to this element after the last vocabulary column and a test that
// held the lists loose could not see where it landed.
const boardElement = new TestElement("section");
const boardLists = boardStatusDefinitions.map(([status, label]) => {
  const element = new TestElement("div");
  element.dataset.status = status;
  element.dataset.statusLabel = label;
  element.dataset.dropStatus = status;
  const column = new TestElement("section");
  column.className = "column";
  column.append(element);
  boardElement.append(column);
  return element;
});
boardView.dataset.defaultStatus = boardDefaultStatus;
boardView.dataset.vocabularyHead = boardVocabularyHead;
boardView.dataset.attachmentLimit = String(boardAttachmentLimit);
// The region that holds tasks whose status matches no column. It is always in
// the document and hidden while empty, because the status that strands a task
// arrives on a poll rather than on the first paint, and a region the server
// only emits when it is already occupied could never take the first arrival.
const boardUnknownSection = new TestElement("section");
boardUnknownSection.dataset.unknownSection = "";
boardUnknownSection.dataset.visible = "false";
const boardUnknownCount = new TestElement("span");
boardUnknownCount.dataset.unknownCount = "";
const boardUnknownList = new TestElement("div");
boardUnknownList.dataset.unknownList = "";
boardUnknownSection.append(boardUnknownCount, boardUnknownList);
boardView.querySelector = (selector) => {
  if (selector === "[data-stale]") return stale;
  if (selector === "[data-board]") return boardElement;
  if (selector === "[data-unknown-section]") return boardUnknownSection;
  if (selector === "[data-unknown-count]") return boardUnknownCount;
  if (selector === "[data-unknown-list]") return boardUnknownList;
  return null;
};
const boardCounts = boardStatuses.map((status) => {
  const element = new TestElement("span");
  element.dataset.count = status;
  return element;
});
boardView.querySelectorAll = (selector) => selector === "[data-status]" ? boardLists : boardCounts;
// The create report sits outside the board view, because it has to be readable
// from whatever route the save left the user on.
const notice = new TestElement("div");
notice.hidden = true;
// The standing announcement that the project's statuses have moved on from the
// ones this page drew, and the control that acts on it. Both are part of the
// page whether or not anything has changed, for the reason the unknown-status
// region is: the change arrives on a poll.
const vocabularyNotice = new TestElement("div");
vocabularyNotice.hidden = true;
const vocabularyReload = new TestElement("button");
vocabularyNotice.append(vocabularyReload);
// The page ships this control hidden and renderRoute() reveals it on the board,
// so the harness has to start it hidden too. Starting it visible would let a
// renderRoute() that never touched it look like it had revealed it.
const descriptionToggle = new TestElement("button");
descriptionToggle.hidden = true;
// The Deleted column's switch, shipped hidden beside it and revealed by the
// board's render for the same reason. It is an anchor, because the state it
// sets is the address.
const deletedToggle = new TestElement("a");
deletedToggle.hidden = true;
deletedToggle.href = "/?deleted=1";
// The body of the statuses route, as the server renders it for a board built
// with the four vocabulary mutations: shipped hidden and outside main, mounted
// into main by the render for that route, with the list inside it drawn by the
// client from what the server answers. The roles a status may carry are an
// attribute on it for the reason the columns carry their statuses — the set is
// the server's, and the script must not hold a copy of it.
const vocabularyPanel = new TestElement("div");
vocabularyPanel.hidden = true;
vocabularyPanel.dataset.statusTags = boardStatusTags;
const vocabularyPanelStatus = new TestElement("div");
const vocabularyPanelBody = new TestElement("div");
vocabularyPanel.append(vocabularyPanelStatus, vocabularyPanelBody);
const documentEventListeners = {};
	globalThis.document = {
	  title: "",
	  get activeElement() { return globalThis.activeElement || null; },
	  querySelector(selector) {
    if (selector === "main") return main;
    if (selector === "[data-board-view]") return boardView;
    if (selector === "[data-updated]") return updated;
    if (selector === "[data-notice]") return notice;
    if (selector === "[data-vocabulary-notice]") return vocabularyNotice;
    if (selector === "[data-vocabulary-reload]") return vocabularyReload;
    if (selector === "[data-description-toggle]") return descriptionToggle;
    if (selector === "[data-deleted-toggle]") return deletedToggle;
    if (selector === "[data-vocabulary-panel]") return vocabularyPanel;
    if (selector === "[data-vocabulary-panel-status]") return vocabularyPanelStatus;
    if (selector === "[data-vocabulary-panel-body]") return vocabularyPanelBody;
    return null;
  },
  querySelectorAll() { return []; },
  createElement(tagName) { return new TestElement(tagName); },
  createDocumentFragment() { return new TestElement("fragment"); },
  addEventListener(name, listener) { documentEventListeners[name] = listener; },
  removeEventListener(name, listener) {
    if (documentEventListeners[name] === listener) delete documentEventListeners[name];
  }
};
	const initialURL = new URL("http://127.0.0.1` + path + `");
	let reloadCalls = 0;
	let intervalDelay = null;
	let intervalCallback = null;
	const clipboardWrites = [];
	let clipboardError = null;
	Object.defineProperty(globalThis, "navigator", { value: {
	  clipboard: {
	    async writeText(value) {
	      if (clipboardError) throw clipboardError;
	      clipboardWrites.push(value);
	    }
	  }
	}, configurable: true });
	const windowTimeouts = [];
	let nextWindowTimeoutID = 1;
	// What the browser remembers between visits. A test seeds it to state what
	// the reader chose last time, and reads it to state what this visit stored.
	const storedPreferences = new Map();
	const preferenceStorage = {
	  getItem(key) { return storedPreferences.has(key) ? storedPreferences.get(key) : null; },
	  setItem(key, value) { storedPreferences.set(key, String(value)); },
	  removeItem(key) { storedPreferences.delete(key); }
	};
const windowEventListeners = {};
globalThis.window = {
	  innerHeight: 900,
	  innerWidth: 1440,
	  localStorage: preferenceStorage,
	  location: { href: initialURL.href, origin: initialURL.origin, reload() { reloadCalls += 1; } },
	  // Recorded rather than discarded, so a test can walk Back and Forward the
	  // way the browser does: the client renders its route from popstate, and a
	  // harness that swallowed the listener could only ever exercise the
	  // forward direction.
	  addEventListener(name, listener) { windowEventListeners[name] = listener; },
	  removeEventListener(name, listener) {
	    if (windowEventListeners[name] === listener) delete windowEventListeners[name];
	  },
	  setInterval(callback, delay) { intervalCallback = callback; intervalDelay = delay; },
	  setTimeout(callback, delay) {
	    const timer = { id: nextWindowTimeoutID++, callback, delay, canceled: false };
	    windowTimeouts.push(timer);
	    return timer.id;
	  },
	  clearTimeout(id) {
	    const timer = windowTimeouts.find((candidate) => candidate.id === id);
	    if (timer) timer.canceled = true;
	  }
};
const historyPaths = [];
const historyReplacements = [];
globalThis.history = {
  pushState(_state, _title, path) {
    historyPaths.push(path);
    window.location.href = new URL(path, window.location.href).href;
  },
  replaceState(_state, _title, path) {
    historyReplacements.push(path);
    window.location.href = new URL(path, window.location.href).href;
  }
};
globalThis.requestAnimationFrame = (callback) => callback();
	const taskDocument = ` + taskDocument + `;
	let taskResponse = taskDocument;
	let deletedTaskResponse = { format: "workbook.tasks", version: 1, tasks: [] };
	// What GET /api/tasks?deleted=include answers: one document holding the
	// active tasks and the deleted ones together, which is what the board polls
	// for while its Deleted column is shown. Unset, it is the active document, so
	// a test that never shows the column sees exactly the board it always saw.
	let includedTaskResponse = null;
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
  if (url === "/api/tasks?deleted=true") return { ok: true, json: async () => deletedTaskResponse };
  if (url === "/api/tasks?deleted=include") return { ok: true, json: async () => includedTaskResponse || taskResponse };
  return { ok: true, json: async () => taskResponse };
};
// Walks the address bar back to an entry the reader has already been at, the
// way Back does, and hands the client the popstate it re-renders from. The
// destination is named rather than derived from the push record, because the
// tests assert on that record and popping it would take the evidence away.
function returnTo(path) {
  window.location.href = new URL(path, window.location.href).href;
  if (windowEventListeners.popstate) windowEventListeners.popstate();
}
function findElement(root, predicate) {
  if (predicate(root)) return root;
  for (const child of root.children || []) {
    const match = findElement(child, predicate);
    if (match) return match;
  }
  return null;
}
function findElements(root, predicate, matches = []) {
  if (predicate(root)) matches.push(root);
  for (const child of root.children || []) findElements(child, predicate, matches);
  return matches;
}
function hasDataKey(element, key) {
  return Object.prototype.hasOwnProperty.call(element.dataset || {}, key);
}
// The Deleted column, its list and its cards, or null while the reader has it
// hidden. It is the client's own element rather than a served one, so a test
// finds it by walking the board the way a reader's browser would.
function deletedColumn() {
  return findElement(boardElement, (element) => hasDataKey(element, "deletedColumn"));
}
function deletedList() {
  const column = deletedColumn();
  return column && findElement(column, (element) => hasDataKey(element, "deletedList"));
}
function deletedCards() {
  const list = deletedList();
  return list ? list.querySelectorAll(".task-card") : [];
}
// The card the board is drawing for a task, wherever it currently sits: a
// column, the region that holds the statuses no column matches, or the Deleted
// column when it is shown.
function boardCard(taskID) {
  const lists = boardLists.concat([boardUnknownList]);
  const removed = deletedList();
  if (removed) lists.push(removed);
  for (const list of lists) {
    const found = list.querySelectorAll(".task-card").find((item) => item.dataset.taskId === taskID);
    if (found) return found;
  }
  return null;
}
// What a card is reporting about a refused change, or "" when it is reporting
// nothing. The region is part of every card and empty on almost all of them, so
// the visibility flag is read as well as the text.
function cardFailureMessage(card) {
  const region = card && findElement(card, (element) => hasDataKey(element, "taskFailure"));
  if (!region || region.dataset.visible !== "true") return "";
  const message = findElement(region, (element) => hasDataKey(element, "taskFailureMessage"));
  return message ? message.textContent : "";
}
function cardFailureDismiss(card) {
  const region = card && findElement(card, (element) => hasDataKey(element, "taskFailure"));
  if (!region || region.dataset.visible !== "true") return null;
  return findElement(region, (element) => hasDataKey(element, "taskFailureDismiss"));
}
// The status administration panel's row for one status, and the statuses it is
// listing in the order it drew them.
function panelRow(status) {
  return findElement(vocabularyPanelBody, (element) => element.dataset.vocabularyStatus === status);
}
function panelStatuses() {
  return findElements(vocabularyPanelBody, (element) => Boolean(element.dataset.vocabularyStatus))
    .map((row) => row.dataset.vocabularyStatus);
}
// A control inside the panel, found by the name it announces or the caption it
// draws — which is how a reader finds it too.
function panelControl(root, name) {
  return findElement(root, (element) =>
    element.tagName === "BUTTON" &&
    (element.getAttribute("aria-label") === name || element.textContent === name));
}
// Everything the panel is currently saying, one entry per line.
function panelMessages() {
  return vocabularyPanelStatus.children.map((line) => line.textContent);
}
// Picks an option the way a reader does. A select reports the option that is
// selected rather than a value written at it, here as in a browser.
function chooseOption(control, value) {
  const wanted = control.children.find((option) => option.value === value);
  if (!wanted) throw new Error("no option " + JSON.stringify(value) + " in " + control.id);
  control.children.forEach((option) => { option.selected = false; });
  wanted.selected = true;
  return control;
}
`
}

func TestHandlerUpdatesTaskStatus(t *testing.T) {
	var gotID string
	var gotStatus core.Status
	updated := boardTasks()[0]
	updated.Status = core.StatusInProgress
	handler := NewHandler(Options{
		List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: unexpectedTaskCreate(t),
		Update: unexpectedTaskUpdate(t),
		UpdateStatus: func(_ context.Context, id string, status core.Status, _ string) (core.MutationResult, error) {
			gotID = id
			gotStatus = status
			return core.MutationResult{Task: updated}, nil
		},
	})

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
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("create input = %#v, want %#v", input, want)
			}
			return core.MutationResult{Task: created}, nil
		},
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})

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
	handler := NewHandler(Options{
		List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: unexpectedTaskCreate(t),
		Update: func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			if id != "WB-01J00000000000000000000001" {
				t.Fatalf("update id = %q", id)
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("update input = %#v, want %#v", input, want)
			}
			return core.MutationResult{Task: updated}, nil
		},
		UpdateStatus: unexpectedStatusUpdate(t),
	})

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
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position: func(_ context.Context, id string, input core.PlaceInput) (core.MutationResult, error) {
			gotID = id
			gotInput = input
			return core.MutationResult{Task: want}, nil
		},
	})

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
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position: func(context.Context, string, core.PlaceInput) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "placement accepts at most one anchor direction")
		},
	})

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

	handler = NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
	})
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
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
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
	handler := NewHandler(Options{
		List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: unexpectedTaskCreate(t),
		Update: unexpectedTaskUpdate(t),
		UpdateStatus: func(context.Context, string, core.Status, string) (core.MutationResult, error) {
			called = true
			return core.MutationResult{Task: boardTasks()[0]}, nil
		},
	})

	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001/status", `{"status":"ready"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/<id>/status status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !called {
		t.Fatal("status callback was not called")
	}
}

func TestHandlerRejectsWrongMethods(t *testing.T) {
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
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
	handler := NewHandler(Options{
		List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: unexpectedTaskCreate(t),
		Update: unexpectedTaskUpdate(t),
		UpdateStatus: func(context.Context, string, core.Status, string) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryValidation, "invalid task status")
		},
	})

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
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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
	if !strings.Contains(body, "taskIDCopyControl(task.id, view.idPrefix)") {
		t.Error("embedded refresh script does not render the server-provided ID prefix")
	}
}

func TestHandlerInitialCardPrefixesMatchRefreshPresentation(t *testing.T) {
	tasks := boardTasks()
	tasks[0].ID = "WB-01J0000A1111111111111111111"
	tasks[1].ID = "WB-01J0000B2222222222222222222"
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	initial := request(t, handler, http.MethodGet, "/")
	if initial.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", initial.Code, http.StatusOK)
	}
	cards := initialCardPrefixes(initial.Body.String())
	if len(cards) != len(tasks) {
		t.Fatalf("initial rendered cards = %#v, want one per task", cards)
	}
	// The unknown-status card is rendered from the same presentation as the rest,
	// so its prefix has to agree with the refresh document exactly as theirs do.
	if _, exists := cards[tasks[2].ID]; !exists {
		t.Fatalf("initial rendered cards omit unknown-status task %q", tasks[2].ID)
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
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

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
	assertBoardStatusMarkersMatchColumns(t, body)
}

// assertBoardStatusMarkersMatchColumns pins the unknown-status region to being
// a display rather than a seventh status: the two attributes that make a list
// draggable-out-of and droppable-into appear once per column and nowhere else.
//
// Naming a status here would assert nothing. The stranded fixture holds
// "future-status", so a check for the absence of `data-status="unknown"` passes
// whatever the template emits — including a template that hands the region a
// real status and makes it a genuine seventh column. Counting the markers is
// the falsifiable form: it fails for any status a regression reaches for.
func assertBoardStatusMarkersMatchColumns(t *testing.T, body string) {
	t.Helper()
	columns := len(core.LegacyVocabulary().Definitions())
	for _, marker := range []string{`data-status="`, `data-drop-status="`} {
		if got := strings.Count(body, marker); got != columns {
			t.Errorf("GET / body has %d %s markers, want %d: one per column and none on the unknown-status region", got, marker, columns)
		}
	}
}

// The client has no status list of its own any more.
//
// It used to declare one, six statuses long, and a test pinned it against
// core's built-in set so the form could not offer a status core would reject. A
// project defines its own statuses now, so a constant in the page could only be
// right for projects that never customized anything — and the ones that had
// would get a form offering columns they do not have. What the page carries
// instead is one labelled attribute per rendered column, which the script reads
// back: there is exactly one answer, and the server wrote it.
func TestHandlerRendersEachColumnWithItsProjectLabel(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(vocabulary, "9f1c2b"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
	})

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if strings.Contains(body, "const statusDefinitions = [") {
		t.Error("GET / body still declares a hard-coded client status list")
	}

	pattern := regexp.MustCompile(`data-status="([^"]*)" data-status-label="([^"]*)"`)
	matches := pattern.FindAllStringSubmatch(body, -1)
	got := make([]core.StatusDefinition, len(matches))
	for index, match := range matches {
		got[index] = core.StatusDefinition{Status: core.Status(match[1]), Label: match[2]}
	}
	want := vocabulary.Definitions()
	for index := range want {
		want[index] = core.StatusDefinition{Status: want[index].Status, Label: want[index].Label}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rendered columns = %v, want the project's %v", got, want)
	}
	// The default is a tag, not a position, so it cannot be read off the column
	// order and the server has to say which one it is.
	if !strings.Contains(body, `data-default-status="queued"`) {
		t.Errorf("GET / body does not carry the project's default status:\n%s", body)
	}
	if !strings.Contains(body, `data-vocabulary-head="9f1c2b"`) {
		t.Errorf("GET / body does not carry the vocabulary head it rendered under:\n%s", body)
	}
}

// handlerVocabulary is a project whose statuses, labels, order and default tag
// are all different from the built-in ones, so nothing about it can be
// satisfied by a fixed table. The default is deliberately not the first column.
func handlerVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Icebox", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "queued", Label: "Queued Up", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		[]core.StatusAlias{{From: "done", To: "shipped"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// staticVocabulary is a resolver that always answers the same thing, which is
// what a test that is not about a mid-session change wants.
func staticVocabulary(vocabulary core.Vocabulary, head string) VocabularyResolver {
	return func(context.Context) (VocabularyState, error) {
		return VocabularyState{Vocabulary: vocabulary, Head: head}, nil
	}
}

// A resolver that fails stops the request rather than falling back to the
// built-in statuses. Drawing six columns for a project that defines three would
// put every card in a column that does not exist and invite drops the server
// would refuse.
func TestHandlerReportsAVocabularyItCannotRead(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{}, core.Errorf(core.CategoryCorruptData, "cannot read this project's status configuration")
		},
		List: func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
	})

	for _, path := range []string{"/", "/api/tasks", "/api/vocabulary"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusInternalServerError {
			t.Errorf("GET %s status = %d, want %d; body = %s", path, response.Code, http.StatusInternalServerError, response.Body.String())
		}
	}
}

// The vocabulary route reports the project's statuses in configured order, with
// their labels, their tags, the forwarding chains and the ledger head — enough
// for a client to explain a status it is shown without deriving any of it.
func TestHandlerServesTheProjectVocabulary(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(vocabulary, "9f1c2b"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
	})

	response := request(t, handler, http.MethodGet, "/api/vocabulary")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var document VocabularyDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.vocabulary" || document.Version != 1 {
		t.Fatalf("vocabulary document = %#v, want workbook.vocabulary v1", document)
	}
	if document.Head != "9f1c2b" || document.Default != "queued" {
		t.Errorf("vocabulary head/default = %q/%q, want 9f1c2b/queued", document.Head, document.Default)
	}
	want := vocabulary.Document()
	if !reflect.DeepEqual(document.Statuses, want.Statuses) {
		t.Errorf("vocabulary statuses = %#v, want %#v", document.Statuses, want.Statuses)
	}
	if !reflect.DeepEqual(document.Aliases, want.Aliases) {
		t.Errorf("vocabulary aliases = %#v, want %#v", document.Aliases, want.Aliases)
	}
	if !reflect.DeepEqual(document.Retired, want.Retired) {
		t.Errorf("vocabulary retired = %#v, want %#v", document.Retired, want.Retired)
	}

	// A board built without a resolver reports the pre-ledger statuses and no
	// head, which is what every construction that predates this field saw. It is
	// core.LegacyVocabulary rather than the statuses a mint would write, because
	// such a caller may be drawing a project older than this build.
	plain := request(t, listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil }), http.MethodGet, "/api/vocabulary")
	var fallback VocabularyDocument
	if err := json.Unmarshal(plain.Body.Bytes(), &fallback); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, plain.Body.String())
	}
	if fallback.Head != "" || !reflect.DeepEqual(fallback.Statuses, core.LegacyVocabulary().Document().Statuses) {
		t.Errorf("vocabulary without a resolver = %#v, want the built-in statuses and no head", fallback)
	}
}

// The tasks document carries the head its columns were built from, so the poll
// can tell that the vocabulary moved without fetching it every second.
func TestHandlerTasksDocumentCarriesTheVocabularyHead(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(handlerVocabulary(t), "9f1c2b"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
	})

	response := request(t, handler, http.MethodGet, "/api/tasks")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
	}
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode tasks document: %v; body = %s", err, response.Body.String())
	}
	if document.VocabularyHead != "9f1c2b" {
		t.Errorf("tasks document vocabularyHead = %q, want 9f1c2b", document.VocabularyHead)
	}
}

// A stored status a rename replaced is drawn in the column it now means, and
// the card says so: its label names the live status rather than the token on
// disk. Only a status no chain leads out of lands in the unknown region, where
// the label names the unrecognized token instead — both cards drag, and the
// difference is which move the label describes.
func TestHandlerDrawsAStaleStatusInItsLiveColumn(t *testing.T) {
	tasks := []core.Task{
		clientPlacementTask("WB-01J00000000000000000000001", "Renamed away", core.StatusDone, core.PriorityHigh),
		clientPlacementTask("WB-01J00000000000000000000002", "Nothing forwards this", core.Status("archived"), core.PriorityLow),
	}
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(handlerVocabulary(t), "9f1c2b"),
		List:       func(context.Context) ([]core.Task, error) { return tasks, nil },
	})

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, `aria-label="Move task Renamed away from shipped"`) {
		t.Errorf("stale-status card does not announce its live column:\n%s", body)
	}
	if !strings.Contains(body, `aria-label="Move task Nothing forwards this out of the unrecognized status archived"`) {
		t.Errorf("unresolvable card does not announce itself as unrecognized:\n%s", body)
	}
	// The unknown region opens after every column, so a card rendered before it
	// is in a column and one rendered after it is not.
	unknown := strings.Index(body, "data-unknown-list")
	if stale := strings.Index(body, `data-task-id="WB-01J00000000000000000000001"`); stale < 0 || stale > unknown {
		t.Errorf("the stale-status card was rendered in the unknown region at %d, region opens at %d", stale, unknown)
	}
	if stranded := strings.Index(body, `data-task-id="WB-01J00000000000000000000002"`); stranded < unknown {
		t.Errorf("the unresolvable card was rendered in a column at %d, unknown region opens at %d", stranded, unknown)
	}
}

func initialCardPrefixes(body string) map[string]string {
	pattern := regexp.MustCompile(`(?s)<article class="task-card" tabindex="0" data-task-id="([^"]+)" data-priority="[^"]+" data-id-prefix="([^"]+)"[^>]*>\s*<div class="task-card__meta"><span class="task-id-copy-group"><button type="button" class="task-id-copy" data-copy-task-id="([^"]+)" aria-label="Copy full task ID [^"]+"><code>([^<]+)</code></button><span class="copy-status"`)
	cards := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(body, -1) {
		if match[1] == match[3] && match[2] == match[4] {
			cards[match[1]] = match[2]
		}
	}
	return cards
}

func TestHandlerRejectsUnknownRoutesAndMutationMethods(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

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

// The board sends the tip it rendered so a change proposed against a stale
// view is reported rather than silently overwriting whatever arrived since.
// The card lands in its new column before the write completes, and the poll
// that fires while the request is still open must not drag it back. Reverting
// for the length of every round trip is the flicker this queue removes.
func TestHandlerClientRendersAPlacementBeforeItsResponseAndSurvivesAPoll(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000031", "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	settled := moved
	settled.Status = core.StatusInProgress
	settled.Head = "head-2"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := json.Marshal(settled)
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const card = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  card.rect = { top: 0, bottom: 80 };

  let releaseMutation;
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseMutation = () => resolve({
          ok: true,
          json: async () => ({ format: "workbook.task-mutation", version: 1, task: ` + string(confirmed) + ` })
        });
      });
    }
    return boardFetch(url, options);
  };

  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({
    target: inProgress, clientY: 1, dataTransfer, preventDefault() {}
  });

  await Promise.resolve();
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the card did not move before the response arrived");
  }

  // A poll lands while the write is still open. It replaces the whole model,
  // and the pending intent has to survive that.
  await intervalCallback();
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("a poll reverted the optimistic card while the write was open");
  }

  releaseMutation();
  await pending;
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the confirmed task did not stay in its new column");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute optimistic placement behavior: %v\n%s", err, output)
	}
}

// Two intents on one task go out one at a time, and the second carries the head
// the first returned. Without that serialization there is no single head the
// client could name while its own writes are in flight.
func TestHandlerClientSendsOneTasksIntentsSeriallyThreadingTheHead(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000041", "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const done = boardLists.find((list) => list.dataset.status === "done");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  let open = 0;
  let maxOpen = 0;
  const heads = [];
  let nextHead = 2;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      open += 1;
      maxOpen = Math.max(maxOpen, open);
      heads.push(JSON.parse(options.body).expectedHead);
      await new Promise((resolve) => setTimeout(resolve, 0));
      open -= 1;
      const head = "head-" + nextHead++;
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1,
        task: Object.assign({}, ` + string(mustJSON(t, moved)) + `, { head })
      }) };
    }
    return { ok: true, json: async () => (` + string(document) + `) };
  };

  const first = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  first.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: first, dataTransfer });
  const firstDrop = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });

  const second = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  second.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: second, dataTransfer });
  const secondDrop = documentEventListeners.drop({ target: done, clientY: 1, dataTransfer, preventDefault() {} });

  await Promise.all([firstDrop, secondDrop]);
  if (maxOpen !== 1) {
    throw new Error("one task's intents overlapped; max concurrent writes was " + maxOpen);
  }
  if (heads.length !== 2 || heads[0] !== "head-1" || heads[1] !== "head-2") {
    throw new Error("intents did not thread the confirmed head: " + JSON.stringify(heads));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute serial intent behavior: %v\n%s", err, output)
	}
}

// A failed intent is dropped while the intents queued behind it survive: those
// were separate decisions, and discarding a later change because an earlier
// one was refused is the clobbering the queue exists to avoid.
func TestHandlerClientRollsBackAFailedIntentAndLeavesALaterOneStanding(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000051", "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	confirmed := moved
	confirmed.Status = core.StatusDone
	confirmed.Head = "head-2"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const done = boardLists.find((list) => list.dataset.status === "done");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  const heads = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      heads.push(JSON.parse(options.body).expectedHead);
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (heads.length === 1) {
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "validation", message: "that placement is not legal" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const first = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  first.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: first, dataTransfer });
  const firstDrop = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });

  const second = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  second.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: second, dataTransfer });
  const secondDrop = documentEventListeners.drop({ target: done, clientY: 1, dataTransfer, preventDefault() {} });

  await Promise.all([firstDrop, secondDrop]);
  if (heads.length !== 2 || heads[1] !== "head-1") {
    throw new Error("the later intent was not sent against the unmoved head: " + JSON.stringify(heads));
  }
  if (inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the failed intent did not roll back");
  }
  if (!done.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the later intent did not stay standing after the earlier failure");
  }
  const reported = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
  if (!reported.includes("Task update failed")) {
    throw new Error("the failure was not reported on the card: " + JSON.stringify(reported));
  }
  if (stale.dataset.visible === "true") {
    throw new Error("the refusal was written to the banner every successful poll clears");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute failed intent rollback behavior: %v\n%s", err, output)
	}
}

// A stale write rolls the intent back, forces a refresh, and re-bases the
// queue's head from that refresh so the intents behind it retry against
// current truth instead of failing identically.
func TestHandlerClientStaleWriteRollsBackRefreshesAndRebasesTheQueue(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000052", "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	confirmed := elsewhere
	confirmed.Status = core.StatusDone
	confirmed.Head = "head-3"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	refreshed := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{elsewhere}, Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const done = boardLists.find((list) => list.dataset.status === "done");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  const heads = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      heads.push(JSON.parse(options.body).expectedHead);
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (heads.length === 1) {
        taskResponse = ` + string(refreshed) + `;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const first = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  first.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: first, dataTransfer });
  const firstDrop = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });

  const second = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  second.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: second, dataTransfer });
  const secondDrop = documentEventListeners.drop({ target: done, clientY: 1, dataTransfer, preventDefault() {} });

  await Promise.all([firstDrop, secondDrop]);
  if (heads.length !== 2 || heads[1] !== "head-2") {
    throw new Error("the queue did not re-base on the refreshed head: " + JSON.stringify(heads));
  }
  const firstMutation = fetchCalls.findIndex((call) => (call.options.method || "GET") !== "GET");
  const refreshedAfterConflict = fetchCalls.slice(firstMutation + 1).some((call) =>
    (call.options.method || "GET") === "GET" && call.url === "/api/tasks");
  if (!refreshedAfterConflict) {
    throw new Error("the stale write did not force a refresh");
  }
  if (inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the conflicted intent did not roll back");
  }
  if (!done.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the intent behind the conflict did not stay standing");
  }
  const reported = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
  if (!reported.includes("changed elsewhere")) {
    throw new Error("the conflict was not reported as such on the card: " + JSON.stringify(reported));
  }
  if (stale.dataset.visible === "true") {
    throw new Error("the conflict was written to the banner every successful poll clears");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute stale-write re-base behavior: %v\n%s", err, output)
	}
}

// A pending intent can outlive the board view that queued it. If it fails
// while the detail form for its task is open, the form must stop showing the
// optimistic value the server refused, because it reads as saved state.
//
// What is at stake there is display accuracy rather than safety: a save from
// this form sends only the fields that differ from the ones it rendered
// (changedTaskValues), so an untouched control could never have carried the
// refused value into one. That is why the value is corrected where it stands
// instead of by a re-render, which would buy the accuracy with every keystroke
// typed into the form since it opened. detail_withdrawal_test.go covers what
// the correction keeps; this covers the path from the board drag to the form.
func TestHandlerClientReflectsAFailedPendingIntentInAnOpenDetailForm(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J00000000000000000000053", "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	elsewhere := moved
	elsewhere.Description = "Rewritten elsewhere."
	elsewhere.Head = "head-2"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{elsewhere}, Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  let releaseMutation;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseMutation = () => {
          taskResponse = ` + string(truth) + `;
          resolve({ ok: false, json: async () => ({
            format: "workbook.error", version: 1,
            error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
          }) });
        };
      });
    }
    return boardFetch(url, options);
  };

  const card = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const optimistic = findElement(main, (element) => element.id === "task-status");
  if (!optimistic || optimistic.value !== "in-progress") {
    throw new Error("the open form does not project the pending intent: " + JSON.stringify(optimistic && optimistic.value));
  }

  releaseMutation();
  await pending;
  const status = findElement(main, (element) => element.id === "task-status");
  if (!status || status.value !== "ready") {
    throw new Error("the form kept the refused optimistic status: " + JSON.stringify(status && status.value));
  }
  const message = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!message || !message.textContent.includes("changed elsewhere")) {
    throw new Error("the failure was not reported in the open form: " + JSON.stringify(message && message.textContent));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute open detail form rollback behavior: %v\n%s", err, output)
	}
}

// The detail form names the head it rendered and sends only the fields the
// user changed, so an unrelated concurrent edit is neither silently
// overwritten nor re-asserted away. A save that changes nothing is not sent
// at all, because the server refuses an empty update.
func TestHandlerClientDetailFormSendsOnlyChangedFieldsWithTheObservedHead(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask("WB-01J00000000000000000000054", "Detail task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "Original."
	task.Labels = []string{"docs", "web"}
	confirmed := task
	confirmed.Description = "Rewritten for the test."
	confirmed.Head = "head-2"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const boardFetch = globalThis.fetch;
  const bodies = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      bodies.push({ url, body: JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("the detail form did not render");
  description.value = "Rewritten for the test.";
  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("a one-field save sent " + bodies.length + " mutations");
  }
  if (bodies[0].url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `)) {
    throw new Error("the save went to " + bodies[0].url);
  }
  if (JSON.stringify(bodies[0].body) !== '{"description":"Rewritten for the test.","expectedHead":"head-1"}') {
    throw new Error("the save did not send only the changed field with the observed head: " + JSON.stringify(bodies[0].body));
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("a successful save did not return to the board");
  }

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const untouched = findElement(main, (element) => element.tagName === "FORM");
  await untouched.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("a no-change save reached the server");
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("a no-change save did not return to the board");
  }

  const reopen = async () => {
    await documentEventListeners.click({
      target: link, button: 0, defaultPrevented: false,
      metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
      preventDefault() {}
    });
    return findElement(main, (element) => element.tagName === "FORM");
  };

  // Labels are a set server-side, so reordering or repeating one is not a
  // change and must not be sent: the server would find no operations in it
  // and refuse the update outright.
  const chiclets = () => findElements(main, (element) =>
    Object.hasOwn(element.dataset, "label")).map((chiclet) => chiclet.dataset.label);
  const addLabel = (value) => {
    const input = findElement(main, (element) => element.id === "task-labels");
    input.value = value;
    input.eventListeners.keydown({ key: "Enter", preventDefault() {} });
  };
  const reordered = await reopen();
  if (JSON.stringify(chiclets()) !== '["docs","web"]') {
    throw new Error("the labels field did not render the stored set: " + JSON.stringify(chiclets()));
  }
  findElement(main, (element) => element.dataset.removeLabel === "docs").eventListeners.click();
  addLabel("docs");
  addLabel("web");
  if (JSON.stringify(chiclets()) !== '["web","docs"]') {
    throw new Error("re-adding a removed label did not reorder the set: " + JSON.stringify(chiclets()));
  }
  await reordered.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("a reordered-labels save reached the server: " + JSON.stringify(bodies[1]));
  }

  const relabeled = await reopen();
  addLabel("api");
  await relabeled.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 2) {
    throw new Error("an added label was not saved");
  }
  if (JSON.stringify(bodies[1].body) !== '{"labels":["docs","web","api"],"expectedHead":"head-1"}') {
    throw new Error("the added label was not sent as entered with the observed head: " + JSON.stringify(bodies[1].body));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute changed-field save behavior: %v\n%s", err, output)
	}
}

// Labels are a set, and the form edits them as one chiclet per label rather
// than as a line of commas: the input holds only the label being typed, Enter
// and comma commit it, every chiclet carries its own remove control, and the
// payload the server sees is the same array of strings it always was.
func TestHandlerClientTaskFormEditsLabelsAsRemovableChiclets(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask("WB-01J0000000000000000000005A", "Detail task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Labels = []string{"docs", "web"}
	saved := task
	saved.Head = "head-2"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s status = %d, want %d", task.ID, response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const boardFetch = globalThis.fetch;
  const bodies = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      bodies.push({ url, body: JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, saved)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const chips = () => findElements(main, (element) =>
    Object.hasOwn(element.dataset, "label")).map((chip) => chip.dataset.label);
  const input = findElement(main, (element) => element.id === "task-labels");
  if (!input || input.tagName !== "INPUT") {
    throw new Error("the labels field is not a single-label input");
  }
  if (input.value !== "") {
    throw new Error("the labels input rendered stored labels as text: " + JSON.stringify(input.value));
  }
  if (JSON.stringify(chips()) !== '["docs","web"]') {
    throw new Error("stored labels did not render as chiclets: " + JSON.stringify(chips()));
  }
  const caption = findElement(main, (element) =>
    element.tagName === "LABEL" && element.textContent === "Labels");
  if (!caption || caption.htmlFor !== "task-labels") {
    throw new Error("the Labels caption does not focus the label input");
  }

  // Enter adds what was typed and clears the input, and must not submit the
  // form on its way there.
  let prevented = false;
  input.value = "api";
  input.eventListeners.keydown({ key: "Enter", preventDefault() { prevented = true; } });
  if (!prevented) throw new Error("Enter in the labels input was left to submit the form");
  if (input.value !== "") throw new Error("adding a label did not clear the input");
  if (JSON.stringify(chips()) !== '["docs","web","api"]') {
    throw new Error("Enter did not add a chiclet: " + JSON.stringify(chips()));
  }

  // A label already on the task is not a second chiclet.
  input.value = "api";
  input.eventListeners.keydown({ key: "Enter", preventDefault() {} });
  if (JSON.stringify(chips()) !== '["docs","web","api"]') {
    throw new Error("a repeated label added a duplicate chiclet: " + JSON.stringify(chips()));
  }

  // A comma is a separator too, so pasted or typed lists still work.
  input.value = "cli,";
  input.eventListeners.input();
  if (input.value !== "") throw new Error("a comma-terminated label was left in the input");
  if (JSON.stringify(chips()) !== '["docs","web","api","cli"]') {
    throw new Error("a comma did not separate labels: " + JSON.stringify(chips()));
  }

  // A pointer press on a remove control blurs the input before the click
  // lands, and the blur commits whatever was typed. The commit has to
  // reconcile the chiclets rather than rebuild them: the browser delivers the
  // click to the button the press started on only while that button is still
  // in the document, and a Shift+Tab into the list is lost the same way.
  const removeDocs = findElement(main, (element) => element.dataset.removeLabel === "docs");
  input.value = "sdk";
  input.eventListeners.blur();
  if (JSON.stringify(chips()) !== '["docs","web","api","cli","sdk"]') {
    throw new Error("leaving the field did not commit the typed label: " + JSON.stringify(chips()));
  }
  if (!main.contains(removeDocs)) {
    throw new Error("committing on blur detached a chiclet that was already on screen");
  }
  findElement(main, (element) => element.dataset.removeLabel === "sdk").eventListeners.click();
  if (JSON.stringify(chips()) !== '["docs","web","api","cli"]') {
    throw new Error("the chiclet committed on blur did not remove: " + JSON.stringify(chips()));
  }
  if (!main.contains(removeDocs)) {
    throw new Error("removing one chiclet detached its surviving siblings");
  }

  // An IME delivers the Enter that confirms a candidate as a keydown with
  // isComposing set. Consuming it commits the unconverted reading and leaves
  // the label the user is actually typing permanently unreachable.
  input.value = "どきゅ";
  let composingPrevented = false;
  input.eventListeners.keydown({ key: "Enter", isComposing: true, preventDefault() { composingPrevented = true; } });
  if (composingPrevented || input.value !== "どきゅ") {
    throw new Error("Enter mid-composition was consumed as a label commit");
  }
  if (JSON.stringify(chips()) !== '["docs","web","api","cli"]') {
    throw new Error("an unconverted IME reading became a chiclet: " + JSON.stringify(chips()));
  }
  input.value = "";
  input.eventListeners.keydown({ key: "Backspace", isComposing: true, preventDefault() {} });
  if (JSON.stringify(chips()) !== '["docs","web","api","cli"]') {
    throw new Error("Backspace mid-composition deleted a chiclet: " + JSON.stringify(chips()));
  }

  const removeWeb = findElement(main, (element) => element.dataset.removeLabel === "web");
  if (!removeWeb || removeWeb.tagName !== "BUTTON" ||
      !(removeWeb.attributes["aria-label"] || "").includes("web")) {
    throw new Error("a chiclet does not offer a named remove control");
  }
  removeWeb.eventListeners.click();
  if (JSON.stringify(chips()) !== '["docs","api","cli"]') {
    throw new Error("removing a chiclet removed the wrong labels: " + JSON.stringify(chips()));
  }
  if (globalThis.activeElement !== input) {
    throw new Error("removing a chiclet dropped keyboard focus");
  }

  // Backspace in an empty input reaches back into the chiclets.
  input.eventListeners.keydown({ key: "Backspace", preventDefault() {} });
  if (JSON.stringify(chips()) !== '["docs","api"]') {
    throw new Error("Backspace did not remove the last chiclet: " + JSON.stringify(chips()));
  }

  // Text left in the input is a label the user typed and meant, so saving
  // carries it rather than dropping it.
  input.value = "typed";
  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("the labels save sent " + bodies.length + " mutations");
  }
  if (JSON.stringify(bodies[0].body) !== '{"labels":["docs","api","typed"],"expectedHead":"head-1"}') {
    throw new Error("the saved payload is not the edited label set: " + JSON.stringify(bodies[0].body));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute label chiclet editing: %v\n%s", err, output)
	}
}

// A save whose head is stale is refused rather than clobbering. The refusal
// keeps the user's edits, re-bases the form's head from the forced refresh,
// and a deliberate re-save applies only the changed fields to the latest
// version — the teammate's concurrent edit to an untouched field survives.
func TestHandlerClientDetailFormRefusesAStaleSaveAndRetriesAgainstTheRefreshedHead(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask("WB-01J00000000000000000000055", "Detail task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "Original."
	elsewhere := task
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	confirmed := elsewhere
	confirmed.Description = "Rewritten while stale."
	confirmed.Head = "head-3"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{elsewhere}, Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const boardFetch = globalThis.fetch;
  const bodies = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      bodies.push(JSON.parse(options.body));
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (bodies.length === 1) {
        taskResponse = ` + string(truth) + `;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("the detail form did not render");
  description.value = "Rewritten while stale.";
  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  if (JSON.stringify(bodies[0]) !== '{"description":"Rewritten while stale.","expectedHead":"head-1"}') {
    throw new Error("the stale save did not carry the rendered head: " + JSON.stringify(bodies[0]));
  }
  if (historyPaths.length !== 0) {
    throw new Error("a refused save navigated away");
  }
  if (description.value !== "Rewritten while stale.") {
    throw new Error("the refusal discarded the user's edits");
  }
  const result = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!result || !result.textContent.includes("changed elsewhere")) {
    throw new Error("the refusal was not reported in the form: " + JSON.stringify(result && result.textContent));
  }
  const save = findElement(main, (element) => element.tagName === "BUTTON" && element.textContent === "Save");
  if (save.disabled) throw new Error("the refusal left Save disabled");

  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 2) {
    throw new Error("the retry did not reach the server; sent " + bodies.length + " mutations");
  }
  if (JSON.stringify(bodies[1]) !== '{"description":"Rewritten while stale.","expectedHead":"head-2"}') {
    throw new Error("the retry did not re-base on the refreshed head or re-asserted untouched fields: " + JSON.stringify(bodies[1]));
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("the accepted retry did not return to the board");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute stale save refusal behavior: %v\n%s", err, output)
	}
}

// A "Depends On" edge is stored on the dependent task, which is the task the
// form is open on, so editing one from the form's own sidebar moves the head
// the form proposes. The form has to adopt the head that write returned, or the
// next Save is refused as a conflict with a change nobody else made. A "Blocks"
// edge is stored on the other task and must not move it, which is read from a
// save the server refuses after that edge is written and before the removal
// below: the head is a closure variable, and that removal moves it again for a
// reason of its own, so the final save's body cannot tell a held guard from a
// broken one.
func TestHandlerClientDetailFormAdoptsTheHeadItsOwnDependencyEditMoved(t *testing.T) {
	node := requireNode(t)
	current := clientPlacementTask("WB-01J00000000000000000000056", "Detail task", core.StatusReady, core.PriorityMedium)
	current.Head = "head-1"
	current.Description = "Original."
	prerequisite := clientPlacementTask("WB-01J00000000000000000000057", "Prerequisite", core.StatusDone, core.PriorityHigh)
	prerequisite.Head = "prerequisite-head-1"
	blocked := clientPlacementTask("WB-01J00000000000000000000058", "Blocked task", core.StatusBacklog, core.PriorityLow)
	blocked.Head = "blocked-head-1"

	afterDependsAdd := current
	afterDependsAdd.Dependencies = []string{prerequisite.ID}
	afterDependsAdd.Head = "head-2"
	blockedAfterAdd := blocked
	blockedAfterAdd.Dependencies = []string{current.ID}
	blockedAfterAdd.Head = "blocked-head-2"
	afterDependsRemove := afterDependsAdd
	afterDependsRemove.Dependencies = []string{}
	afterDependsRemove.Head = "head-3"
	saved := afterDependsRemove
	saved.Description = "Rewritten after the dependency edits."
	saved.Head = "head-4"

	initialTasks := []core.Task{current, prerequisite, blocked}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initialTasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		return string(mustJSON(t, TasksDocument{
			Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
		}))
	}
	emptyDeleted := documentJSON([]core.Task{})

	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initialTasks)) + script + `
deletedTaskResponse = ` + emptyDeleted + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const groupFor = (headingText) => {
    const heading = findElement(main, (element) => element.textContent === headingText);
    if (!heading) throw new Error("missing " + headingText + " group");
    return heading.parentElement;
  };
  const addCandidate = (group, candidateID) => {
    const option = findElement(group, (element) => element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option || !option.eventListeners.click) throw new Error("candidate option is not selectable: " + candidateID);
    option.eventListeners.click();
    const add = findElement(group, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency");
    if (!add || add.disabled) throw new Error("selected candidate did not enable Add dependency");
    return add.eventListeners.click();
  };
  const bodies = [];
  let nextMutation = null;
  let nextRefusal = null;
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (options.body !== undefined) bodies.push(JSON.parse(options.body));
      if (nextRefusal) {
        const refusal = nextRefusal;
        nextRefusal = null;
        return { ok: false, json: async () => refusal };
      }
      return { ok: true, json: async () => nextMutation };
    }
    return boardFetch(url, options);
  };
  const detailForm = () => {
    const form = findElement(main, (element) => element.tagName === "FORM");
    if (!form) throw new Error("the detail form is gone");
    return form;
  };

  nextMutation = { format: "workbook.task-mutation", version: 1, task: ` + documentJSON([]core.Task{afterDependsAdd}) + `.tasks[0] };
  taskResponse = ` + documentJSON([]core.Task{afterDependsAdd, prerequisite, blocked}) + `;
  await addCandidate(groupFor("Depends On"), ` + strconv.Quote(prerequisite.ID) + `);

  nextMutation = { format: "workbook.task-mutation", version: 1, task: ` + documentJSON([]core.Task{blockedAfterAdd}) + `.tasks[0] };
  taskResponse = ` + documentJSON([]core.Task{afterDependsAdd, prerequisite, blockedAfterAdd}) + `;
  await addCandidate(groupFor("Blocks"), ` + strconv.Quote(blocked.ID) + `);
  // Dependency writes are bodyless, so they never reach "bodies". Nothing below
  // would notice an add that silently stopped writing the edge, and the head it
  // must not move is "head-2" whether the edge was written or not, so pin the
  // premise here: without this the guard assertion is vacuous.
  const blocksAdd = fetchCalls.find((call) =>
    call.options.method === "PUT" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(blocked.ID) + `) +
      "/dependencies/" + encodeURIComponent(` + strconv.Quote(current.ID) + `));
  if (!blocksAdd || Object.prototype.hasOwnProperty.call(blocksAdd.options, "body")) {
    throw new Error("the \"Blocks\" add did not write the mirrored edge, so the head below proves nothing");
  }

  // The head the form proposes lives in a closure, so the only way to read it is
  // to make the form send it. A save the server refuses sends the head and
  // changes nothing else, which is what this needs: it observes the head while
  // the "Blocks" write is the newest one. Asserting only the final save cannot
  // see a wrong adoption here, because the "Depends On" removal below moves the
  // head again for a legitimate reason and overwrites it either way.
  const title = findElement(main, (element) => element.id === "task-title");
  if (!title) throw new Error("the detail form did not render a title field");
  const renderedTitle = title.value;
  title.value = "Probed while the Blocks edge is the newest write.";
  nextRefusal = {
    format: "workbook.error", version: 1,
    error: { category: "validation", message: "Probe refused." }
  };
  await detailForm().eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("the probing save sent " + bodies.length + " bodies");
  }
  if (bodies[0].expectedHead !== "head-2") {
    throw new Error("the \"Blocks\" edge is stored on the other task and must not move this form's head: " + JSON.stringify(bodies[0]));
  }
  if (historyPaths.length !== 0) {
    throw new Error("the probing save was applied rather than refused");
  }
  title.value = renderedTitle;

  const row = findElement(groupFor("Depends On"), (element) => element.dataset.relationshipId === ` + strconv.Quote(prerequisite.ID) + `);
  const remove = row && findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!remove) throw new Error("the added prerequisite is not removable");
  nextMutation = { format: "workbook.task-mutation", version: 1, task: ` + documentJSON([]core.Task{afterDependsRemove}) + `.tasks[0] };
  taskResponse = ` + documentJSON([]core.Task{afterDependsRemove, prerequisite, blockedAfterAdd}) + `;
  await remove.eventListeners.click();

  const description = findElement(main, (element) => element.id === "task-description");
  if (!description || description.value !== "Original.") {
    throw new Error("the relationship edits re-rendered the form: " + JSON.stringify(description && description.value));
  }
  description.value = "Rewritten after the dependency edits.";
  nextMutation = { format: "workbook.task-mutation", version: 1, task: ` + documentJSON([]core.Task{saved}) + `.tasks[0] };
  await detailForm().eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 2) {
    throw new Error("the save sent " + (bodies.length - 1) + " bodies");
  }
  if (JSON.stringify(bodies[1]) !== '{"description":"Rewritten after the dependency edits.","expectedHead":"head-3"}') {
    throw new Error("the save did not carry the head this form's own dependency edits moved to: " + JSON.stringify(bodies[1]));
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("the save was refused rather than applied");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute dependency head adoption behavior: %v\n%s", err, output)
	}
}

func TestHandlerReportsAndShiftsThePublicationMode(t *testing.T) {
	state := SyncState{Mode: SyncModeDeferred, Watcher: true}
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		SyncState:    func(context.Context) SyncState { return state },
		SetSyncMode: func(_ context.Context, mode string) (SyncState, error) {
			if mode != SyncModeInline && mode != SyncModeDeferred {
				return SyncState{}, core.Errorf(core.CategoryValidation, "bad mode")
			}
			state = SyncState{Mode: mode, Watcher: true}
			return state, nil
		},
	})

	response := request(t, handler, http.MethodGet, "/api/sync")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/sync = %d, want 200", response.Code)
	}
	var document SyncDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode sync document: %v; body %s", err, response.Body.String())
	}
	if document.Format != "workbook.sync" || document.Version != 1 || document.Sync.Mode != SyncModeDeferred {
		t.Fatalf("sync document = %#v, want a deferred workbook.sync v1", document)
	}

	response = requestJSON(t, handler, http.MethodPut, "/api/sync", `{"mode":"inline"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT /api/sync = %d, want 200", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Sync.Mode != SyncModeInline {
		t.Fatalf("mode after the toggle = %q, want %q", document.Sync.Mode, SyncModeInline)
	}

	response = requestJSON(t, handler, http.MethodPut, "/api/sync", `{"mode":"sideways"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/sync with an unknown mode = %d, want 400", response.Code)
	}
}

// A board with no sync control configured must still serve, because leaving
// those two options nil is what most callers do.
func TestHandlerReportsSyncControlIsNotConfigured(t *testing.T) {
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
	if response := request(t, handler, http.MethodGet, "/api/sync"); response.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/sync without sync control = %d, want 500", response.Code)
	}
}

func TestHandlerForwardsTheExpectedHeadOnEveryRequestThatCarriesIt(t *testing.T) {
	updated := boardTasks()[0]

	t.Run("status", func(t *testing.T) {
		var got string
		handler := NewHandler(Options{
			List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
			Create: unexpectedTaskCreate(t),
			Update: unexpectedTaskUpdate(t),
			UpdateStatus: func(_ context.Context, _ string, _ core.Status, expectedHead string) (core.MutationResult, error) {
				got = expectedHead
				return core.MutationResult{Task: updated}, nil
			},
		})
		requestJSON(t, handler, http.MethodPatch,
			"/api/tasks/WB-01J00000000000000000000001/status",
			`{"status":"in-progress","expectedHead":"head-from-the-board"}`)
		if got != "head-from-the-board" {
			t.Fatalf("status updater expectedHead = %q, want the request's", got)
		}
	})

	t.Run("update", func(t *testing.T) {
		var got core.UpdateInput
		handler := NewHandler(Options{
			List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
			Create: unexpectedTaskCreate(t),
			Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
				got = input
				return core.MutationResult{Task: updated}, nil
			},
			UpdateStatus: unexpectedStatusUpdate(t),
		})
		requestJSON(t, handler, http.MethodPatch,
			"/api/tasks/WB-01J00000000000000000000001",
			`{"title":"Renamed","expectedHead":"head-from-the-board"}`)
		if got.ExpectedHead != "head-from-the-board" {
			t.Fatalf("update input expectedHead = %q, want the request's", got.ExpectedHead)
		}
	})

	t.Run("position", func(t *testing.T) {
		var got core.PlaceInput
		handler := NewHandler(Options{
			List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
			Create:       unexpectedTaskCreate(t),
			Update:       unexpectedTaskUpdate(t),
			UpdateStatus: unexpectedStatusUpdate(t),
			Position: func(_ context.Context, _ string, input core.PlaceInput) (core.MutationResult, error) {
				got = input
				return core.MutationResult{Task: updated}, nil
			},
		})
		requestJSON(t, handler, http.MethodPatch,
			"/api/tasks/WB-01J00000000000000000000001/position",
			`{"status":"ready","expectedHead":"head-from-the-board"}`)
		if got.ExpectedHead != "head-from-the-board" {
			t.Fatalf("place input expectedHead = %q, want the request's", got.ExpectedHead)
		}
	})

	t.Run("omitted stays empty", func(t *testing.T) {
		var got core.UpdateInput
		handler := NewHandler(Options{
			List:   func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
			Create: unexpectedTaskCreate(t),
			Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
				got = input
				return core.MutationResult{Task: updated}, nil
			},
			UpdateStatus: unexpectedStatusUpdate(t),
		})
		requestJSON(t, handler, http.MethodPatch,
			"/api/tasks/WB-01J00000000000000000000001", `{"title":"Renamed"}`)
		if got.ExpectedHead != "" {
			t.Fatalf("update input expectedHead = %q, want empty when omitted", got.ExpectedHead)
		}
	})
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
		{name: "conflict", err: core.Errorf(core.CategoryConflict, "conflicting task"), wantStatus: http.StatusConflict, wantBody: "conflicting task"},
		{name: "corrupt data", err: core.Errorf(core.CategoryCorruptData, "bad checkpoint"), wantStatus: http.StatusInternalServerError, wantBody: "bad checkpoint"},
		{name: "operational includes cause", err: core.Wrap(core.CategoryOperational, "list tasks", errors.New("permission denied")), wantStatus: http.StatusInternalServerError, wantBody: "list tasks: permission denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, test.err })
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
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

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

func TestHandlerClientRendersTaskHistoryLaneRowsAndComparisons(t *testing.T) {
	node := requireNode(t)
	detail := historyDetail()
	task := detail.Task
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	tasks, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        []core.Task{task},
		Presentation: presentationForTasks([]core.Task{task}),
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := json.Marshal(TaskHistoryDocument{
		Format:    "workbook.task-history",
		Version:   1,
		TaskID:    task.ID,
		Lifecycle: lifecycleStages(*detail.History, task.Status, core.LegacyVocabulary()),
		History:   *detail.History,
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/"+task.ID, string(tasks)) + script + `
const historyDocument = ` + string(history) + `;
const historyURL = "/api/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `) + "/history";
let historyReads = 0;
globalThis.fetch = async (url, options = {}) => {
  fetchCalls.push({ url, options });
  if (url === historyURL) {
    historyReads += 1;
    if (historyReads === 1) {
      return { ok: false, json: async () => ({
        format: "workbook.error",
        version: 1,
        error: { category: "operational", message: "history read failed" }
      }) };
    }
    return { ok: true, json: async () => historyDocument };
  }
  return { ok: true, json: async () => taskResponse };
};
const laneStops = () => {
  const lane = findElement(main, (element) => Object.hasOwn(element.dataset, "lifecycle"));
  return lane ? lane.children : [];
};
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  // A failed history read leaves the detail form usable and offers a retry.
  const failure = findElement(main, (element) =>
    (element.className || "").split(/\s+/).includes("history-state") &&
    element.dataset.kind === "error");
  if (!failure || failure.textContent !== "history read failed") {
    throw new Error("failed history read did not report the server message");
  }
  if (!findElement(main, (element) => element.id === "task-title")) {
    throw new Error("failed history read removed the task form");
  }
  const retry = findElement(main, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Retry history");
  if (!retry || retry.type !== "button") {
    throw new Error("failed history read offers no retry that cannot submit the form");
  }

  await retry.eventListeners.click();
  await new Promise((resolve) => setTimeout(resolve, 0));

  const stops = laneStops().map((stop) => [stop.dataset.status, stop.dataset.current || ""]);
  const wantStops = [["backlog", ""], ["ready", ""], ["in-progress", "true"]];
  if (JSON.stringify(stops) !== JSON.stringify(wantStops)) {
    throw new Error("lifecycle lane = " + JSON.stringify(stops) + ", want " + JSON.stringify(wantStops));
  }
  const current = laneStops()[2];
  if (current.getAttribute("aria-current") !== "step") {
    throw new Error("the current lifecycle stop is not announced as the current step");
  }

  const rows = findElements(main, (element) => Object.hasOwn(element.dataset, "changeRow"));
  // The chain records aaa before ccc; the rows read newest first.
  const rowCommits = rows.map((row) => row.dataset.changeRow);
  if (JSON.stringify(rowCommits) !== JSON.stringify(["ccc", "aaa"])) {
    throw new Error("change rows = " + JSON.stringify(rowCommits) + ", want the packs that changed more than status, newest first");
  }
  const summary = findElement(main, (element) => Object.hasOwn(element.dataset, "changeSummary"));
  if (!summary || summary.textContent !==
      "4 changes recorded. 2 of them changed only status and read as the lifecycle above.") {
    throw new Error("change summary = " + JSON.stringify(summary && summary.textContent));
  }

  // Drilling into a row expands it in place into the comparison the server
  // already computed, description word diff included.
  const descriptionRow = rows[0];
  const toggle = descriptionRow.children[0];
  const comparison = descriptionRow.children[1];
  if (toggle.getAttribute("aria-expanded") !== "false" || comparison.hidden !== true) {
    throw new Error("change rows do not start collapsed");
  }
  toggle.eventListeners.click();
  if (toggle.getAttribute("aria-expanded") !== "true" || comparison.hidden !== false) {
    throw new Error("clicking a change row did not expand its comparison");
  }
  if (!findElement(comparison, (element) => element.textContent === "ccc")) {
    throw new Error("the expanded comparison does not name its commit");
  }
  const removed = findElements(comparison, (element) => element.tagName === "DEL").map((element) => element.textContent);
  const added = findElements(comparison, (element) => element.tagName === "INS").map((element) => element.textContent);
  if (!removed.includes("board") || !added.includes("history")) {
    throw new Error("the expanded comparison lost the word-level description diff: " +
      JSON.stringify({ removed, added }));
  }
  toggle.eventListeners.click();
  if (toggle.getAttribute("aria-expanded") !== "false" || comparison.hidden !== true) {
    throw new Error("clicking an expanded change row did not collapse it");
  }
}, 0);
`
	command := nodeCommand(node, program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered client for task history: %v\n%s", err, output)
	}
}

func TestHandlerServesTaskHistoryWithItsLifecycleLane(t *testing.T) {
	detail := historyDetail()
	var gotID string
	handler := historyHandler(t, func(_ context.Context, id string) (core.TaskDetail, error) {
		gotID = id
		return detail, nil
	})

	response := request(t, handler, http.MethodGet, "/api/tasks/"+detail.ID+"/history")
	if response.Code != http.StatusOK {
		t.Fatalf("GET history status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertSecurityHeaders(t, response.Result())
	if gotID != detail.ID {
		t.Fatalf("history reader saw id = %q, want %q", gotID, detail.ID)
	}
	var document TaskHistoryDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode history document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.task-history" || document.Version != 1 || document.TaskID != detail.ID {
		t.Fatalf("history document envelope = %#v, want workbook.task-history v1", document)
	}

	lane := make([]string, 0, len(document.Lifecycle))
	for _, stage := range document.Lifecycle {
		lane = append(lane, string(stage.Status))
	}
	if !reflect.DeepEqual(lane, []string{"backlog", "ready", "in-progress"}) {
		t.Fatalf("lifecycle lane = %v, want backlog, ready, in-progress", lane)
	}
	if !document.Lifecycle[len(document.Lifecycle)-1].Current {
		t.Fatal("lifecycle lane does not mark the task's current status")
	}
	if document.Lifecycle[0].Commit != "aaa" || document.Lifecycle[0].WallTime == nil {
		t.Fatalf("opening lifecycle stage = %#v, want the creating change's attribution", document.Lifecycle[0])
	}

	// The rows keep the whole chain and the diff spans the CLI already renders,
	// so the client needs no second request to compare one change.
	if document.History.Total != len(detail.History.Changes) ||
		len(document.History.Changes) != len(detail.History.Changes) {
		t.Fatalf("history document changes = %#v, want the whole chain", document.History)
	}
	description := document.History.Changes[2].Fields[0]
	if description.Field != "description" || len(description.Diff) == 0 {
		t.Fatalf("description change = %#v, want the server-computed word diff", description)
	}
}

func TestHandlerRejectsUnconfiguredAndMistypedTaskHistoryRequests(t *testing.T) {
	unconfigured := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
	response := request(t, unconfigured, http.MethodGet, "/api/tasks/WB-01J00000000000000000000001/history")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unconfigured history status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	assertErrorDocument(t, response, core.CategoryOperational, "task history is not configured")

	missing := historyHandler(t, func(context.Context, string) (core.TaskDetail, error) {
		return core.TaskDetail{}, core.Errorf(core.CategoryNotFound, "no task matches %q", "WB-nope")
	})
	notFound := request(t, missing, http.MethodGet, "/api/tasks/WB-nope/history")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("missing task history status = %d, want %d", notFound.Code, http.StatusNotFound)
	}
	assertErrorDocument(t, notFound, core.CategoryNotFound, `no task matches "WB-nope"`)

	configured := historyHandler(t, func(context.Context, string) (core.TaskDetail, error) {
		t.Fatal("history reader ran for a request using the wrong method")
		return core.TaskDetail{}, nil
	})
	wrongMethod := requestJSON(t, configured, http.MethodPost, "/api/tasks/WB-01J00000000000000000000001/history", "{}")
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST history status = %d, want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}
	if got := wrongMethod.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("POST history Allow = %q, want %q", got, http.MethodGet)
	}
}

func assertErrorDocument(t *testing.T, response *httptest.ResponseRecorder, category core.Category, message string) {
	t.Helper()
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.error" || document.Version != 1 ||
		document.Error.Category != category || document.Error.Message != message {
		t.Fatalf("error document = %#v, want %s: %s", document, category, message)
	}
}

func historyHandler(t *testing.T, read TaskHistoryReader) http.Handler {
	t.Helper()
	return NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		History:      read,
	})
}

// historyDetail is one task whose chain creates it, moves it twice, and
// rewrites its description, so a lane, an ordinary row, and a word diff all
// have something to render.
func historyDetail() core.TaskDetail {
	task := boardTasks()[0]
	task.Status = core.StatusInProgress
	stamp := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	log := core.ChangeLog{
		Total:   4,
		Showing: 4,
		Changes: []core.Change{
			{
				Commit: "aaa", Actor: "dylan", WallTime: stamp, LogicalClock: 1,
				Summary: "created the task",
				Fields:  []core.FieldChange{{Field: "task", Kind: core.ChangeCreated, To: task.Title}},
			},
			{
				Commit: "bbb", Actor: "dylan", WallTime: stamp.Add(time.Hour), LogicalClock: 2,
				Summary: "changed status",
				Fields:  []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "backlog", To: "ready"}},
			},
			{
				Commit: "ccc", Actor: "agent", WallTime: stamp.Add(2 * time.Hour), LogicalClock: 3,
				Summary: "changed description",
				Fields: []core.FieldChange{{
					Field: "description", Kind: core.ChangeSet,
					From: "Build the board surface.",
					To:   "Build the history surface.",
					Diff: core.WordDiff("Build the board surface.", "Build the history surface."),
				}},
			},
			{
				Commit: "ddd", Actor: "agent", WallTime: stamp.Add(3 * time.Hour), LogicalClock: 4,
				Summary: "changed status",
				Fields:  []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "ready", To: "in-progress"}},
			},
		},
	}
	return core.TaskDetail{Task: task, History: &log}
}

func request(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func requestWithRawPath(t *testing.T, handler http.Handler, method, rawPath string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/", nil)
	request.URL.Path = rawPath
	request.RequestURI = rawPath
	handler.ServeHTTP(response, request)
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

// listHandler builds the read-only board most of these tests want: it renders
// and lists what the given lister returns, and fails the test if any mutation
// route is reached.
func listHandler(t *testing.T, list TaskLister) http.Handler {
	t.Helper()
	return NewHandler(Options{
		List:         list,
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
}

func unexpectedStatusUpdate(t *testing.T) TaskStatusUpdater {
	t.Helper()
	return func(context.Context, string, core.Status, string) (core.MutationResult, error) {
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
				// `blocked` is a status this build does not mint a project with
				// and every pre-ledger project still defines. That is exactly
				// what this fixture wants: the board these tests drive is built
				// without a vocabulary resolver, so it renders the pre-ledger
				// columns, and one of the cases below removes this column from
				// the rendered page to check what a card does when its column is
				// not drawn.
				Status:    core.StatusBlocked,
				Priority:  core.PriorityMedium,
				Labels:    []string{"decision"},
				Rank:      "2/1",
				CreatedAt: stamp,
				UpdatedAt: stamp,
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
