# Linkable Web Task Detail Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add linkable task-detail and new-task web routes that create and update every user-editable task field through Workbook's Git-backed core service.

**Architecture:** The Go handler serves the embedded application shell for /, /tasks/new, and /tasks/<id>, and exposes narrow create and full-update callbacks to core.Service. The embedded JavaScript reads the route, renders either the board or a shared task form, and retains the existing refresh and drag-and-drop behavior.

**Tech Stack:** Go standard library net/http, embedded HTML/CSS/vanilla JavaScript, Workbook internal/core.Service, and Go httptest integration tests.

## Global Constraints

- Web mutations call core.Service.Create or core.Service.Update, preserving append-only Git history.
- Keep PATCH /api/tasks/<id>/status working for drag-and-drop. Do not add draft persistence.
- Direct GET /tasks/new and GET /tasks/<full-id> serve the shell.
- Task URLs use full IDs; board display retains server-provided ID prefixes.
- JSON APIs remain versioned, reject unknown fields and multiple JSON values, and retain the existing CSP/same-origin boundary.

---

### Task 1: Add HTTP create and full-update routes

**Files:**
- Modify: internal/webui/handler.go:22-177
- Modify: internal/webui/handler_test.go:136-184, 269-354, 403-428

**Interfaces:**
- Consumes: core.CreateInput, core.UpdateInput, TaskMutationDocument, ErrorDocument.
- Produces: TaskCreator func(context.Context, core.CreateInput) (core.Task, error).
- Produces: TaskUpdater func(context.Context, string, core.UpdateInput) (core.Task, error).
- Produces: POST /api/tasks and PATCH /api/tasks/<id>.

- [ ] **Step 1: Write failing success-path handler tests**

Add a POST test that supplies all create fields and decodes a workbook.task-mutation v1 response. Add an analogous PATCH /api/tasks/<id> test for all five editable fields. The callback fixture must reject an incorrectly mapped core input, so the successful HTTP response proves the request reached the handler boundary with the expected values.

- [ ] **Step 2: Run the tests and verify the intended failure**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(CreatesTask|UpdatesAllTaskFields)' -count=1

Expected: FAIL because the callbacks and routes do not exist.

- [ ] **Step 3: Implement typed callbacks and strict request decoding**

Add createTaskRequest with Title, Description, Status, Priority, and Labels fields, plus updateTaskRequest with pointer fields for all five properties. The pointer form distinguishes an omitted field from explicit empty text or labels. Extract the existing DisallowUnknownFields and one-JSON-value checks into a shared decoder helper. Register POST /api/tasks and PATCH /api/tasks/{id}; successful responses reuse TaskMutationDocument. Keep the status endpoint separate but share its response writer.


~~~go
type TaskCreator func(context.Context, core.CreateInput) (core.Task, error)
type TaskUpdater func(context.Context, string, core.UpdateInput) (core.Task, error)

func decodeRequest(body io.Reader, value any) error {
    decoder := json.NewDecoder(body)
    decoder.DisallowUnknownFields()
    if err := decoder.Decode(value); err != nil { return err }
    if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
        if err == nil { err = errors.New("multiple JSON values") }
        return err
    }
    return nil
}
~~~

- [ ] **Step 4: Verify the success-path tests are green**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(CreatesTask|UpdatesAllTaskFields)' -count=1

Expected: PASS.

- [ ] **Step 5: Write failing validation and method-boundary tests**

Table-test malformed JSON, an unknown property, and two JSON values for both mutation routes. Each must return HTTP 400 with a workbook.error v1 invocation document. Test GET /api/tasks/<id> and PUT /api/tasks return 405 with correct Allow headers. Test PATCH /api/tasks/<id>/status still invokes only the status callback.

- [ ] **Step 6: Run validation tests and verify they fail**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(RejectsInvalidTaskMutationRequests|PreservesStatusMutationRoute|RejectsWrongMethods)' -count=1

Expected: FAIL until allowedMethod distinguishes the routes.

- [ ] **Step 7: Implement exact API method recognition**

Recognize exactly GET /api/tasks, POST /api/tasks, PATCH /api/tasks/<one path segment>, and PATCH /api/tasks/<one path segment>/status. Decode failures wrap CategoryInvocation; leave core validation and not-found errors unchanged so writeError keeps current envelope and HTTP status behavior.

- [ ] **Step 8: Verify and commit the HTTP boundary**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1

Expected: PASS.

~~~sh
git add internal/webui/handler.go internal/webui/handler_test.go
git commit -m "feat: add web task create and update routes"
~~~

### Task 2: Wire web routes to the Git-backed core service

**Files:**
- Modify: internal/cli/run.go:487-505
- Modify: internal/cli/run_test.go:860-923
- Modify: internal/webui/server_test.go

**Interfaces:**
- Consumes: expanded webui.NewHandler(list, create, update, updateStatus).
- Produces: closures that call service.Create, service.Update, and the existing status-only service.Update.
- Produces: listener integration coverage for persisted web creates and updates.

- [ ] **Step 1: Write failing listener integration tests**

Extend the existing runServe tests. POST a new task, stop the server, run workbook list --json, and locate the returned ID with its title, description, status, priority, and labels. PATCH all five editable fields on an existing task, stop the server, run workbook show <id> --json, and compare the persisted fields.

- [ ] **Step 2: Run focused tests and verify they fail**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestRunServe(CreatesTaskThroughWebRoute|UpdatesAllTaskFieldsThroughWebRoute)' -count=1

Expected: FAIL because runServe provides only a status updater.

- [ ] **Step 3: Pass complete service callbacks**

Pass closures to NewHandler that respectively call service.Create(ctx, input), service.Update(ctx, id, input), and service.Update(ctx, id, core.UpdateInput with Status set). Update all direct NewHandler test call sites with create and update callbacks that fail their test if unexpectedly used.


~~~go
handler := webui.NewHandler(
    func(ctx context.Context) ([]core.Task, error) { return service.List(ctx, core.ListFilter{}) },
    func(ctx context.Context, input core.CreateInput) (core.Task, error) { return service.Create(ctx, input) },
    func(ctx context.Context, id string, input core.UpdateInput) (core.Task, error) { return service.Update(ctx, id, input) },
    func(ctx context.Context, id string, status core.Status) (core.Task, error) {
        return service.Update(ctx, id, core.UpdateInput{Status: &status})
    },
)
~~~

- [ ] **Step 4: Verify persistence and commit**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestRunServe(CreatesTaskThroughWebRoute|UpdatesAllTaskFieldsThroughWebRoute|UpdatesTaskStatusThroughWebRoute)' -count=1

Expected: PASS.

~~~sh
git add internal/cli/run.go internal/cli/run_test.go internal/webui/server_test.go internal/webui/handler_test.go
git commit -m "feat: persist web task form mutations"
~~~

### Task 3: Build linkable board, detail, and new-task views

**Files:**
- Modify: internal/webui/handler.go:76-132
- Modify: internal/webui/assets/index.html:1-236
- Modify: internal/webui/handler_test.go:18-102, 186-268

**Interfaces:**
- Consumes: GET /api/tasks, POST /api/tasks, PATCH /api/tasks/<id>, and canonical WorkflowStatuses.
- Produces: browser routes /, /tasks/new?status=<canonical-status>, and /tasks/<full-id>.
- Produces: an accessible form with title, description, status, priority, labels, Save, and Back controls.

- [ ] **Step 1: Write failing server-rendered route tests**

Request /tasks/new and /tasks/<known-full-id>; assert a 200 HTML shell with the existing security headers. On the initial board page, assert every canonical column, including in-review, renders a real New Task link with its canonical status query value and every rendered task title has a real full-ID URL. These are observable server-rendered navigation contracts, not source-text assertions about client JavaScript.


- [ ] **Step 2: Run focused route tests and verify they fail**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(ServesTaskRouteShell|RendersTaskAndNewTaskLinks)' -count=1

Expected: FAIL because only GET / exists and cards are not links.

- [ ] **Step 3: Serve the shell for app task routes**

Register GET /tasks/new before GET /tasks/{id}; both execute the current template. Update allowedMethod so task app routes are GET-only while API-like paths cannot fall through to the shell.

- [ ] **Step 4: Implement the minimal client route renderer**

Add focused helpers named route, navigate, taskForm, and mutateTask. Render card titles as anchors to full-ID URLs while preserving draggable card wrappers. Put a New Task anchor at the top of every canonical status column. Default missing or invalid status on the new route to backlog. Detail mode locates exactly one task by full ID or shows not found.

The shared form sends title, description, status, priority, and labels. Labels are comma split, trimmed, and empty values removed. New mode posts to /api/tasks; detail mode patches /api/tasks/<id>. Successful save navigates to / and refreshes. Failed save leaves inputs unchanged and renders the server message in an in-form role=status area. Back is a board link and never mutates data.


~~~js
function navigate(path) {
  history.pushState({}, "", path);
  renderRoute();
}
window.addEventListener("popstate", renderRoute);

async function mutateTask(method, path, values) {
  const response = await fetch(path, {
    method, headers: { "Content-Type": "application/json" },
    body: JSON.stringify(values),
  });
  const document = await response.json();
  if (!response.ok || document.format !== "workbook.task-mutation" || document.version !== 1) {
    throw new Error(document.error && document.error.message || "Task update failed.");
  }
  return document.task;
}
~~~


- [ ] **Step 5: Verify route tests and perform the focused browser check**

Run: GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(ServesTaskRouteShell|RendersTaskAndNewTaskLinks)' -count=1

Expected: PASS.

Start workbook serve against a disposable test repository and manually verify: open a full task URL directly; create a task from an In Review column button and confirm the prefill; edit all fields and save; use Back without saving; and open an unknown task URL. Record the exact commands and outcomes in the implementation report. This is a focused behavior check for the embedded browser code; it does not add a browser-test framework to the POC.

- [ ] **Step 6: Commit the client view**

~~~sh
git add internal/webui/handler.go internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: add linkable web task forms"
~~~

### Task 4: Align README and run final checks

**Files:**
- Modify: README.md:170-202
- Create: docs/superpowers/plans/2026-07-25-web-task-detail.md

**Interfaces:**
- Consumes: completed route contract and POC scope.
- Produces: README documentation that distinguishes implemented local web task forms from future hosted collaboration.

- [ ] **Step 1: Document the finished local web experience**

Update the route table and behavior explanation with linkable task views, new-task creation, full-field editing, column-specific status prefill, save failure/discard behavior, and versioned APIs. Retain the out-of-scope boundaries: authentication, hosted deployment, draft persistence, browser deletion, and broader collaboration.

- [ ] **Step 2: Verify the complete repository**

Run:

~~~sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
gofmt -w internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go internal/cli/run_test.go
git diff --check
~~~

Expected: all commands succeed and gofmt leaves no diff.

- [ ] **Step 3: Commit documentation**

~~~sh
git add README.md docs/superpowers/plans/2026-07-25-web-task-detail.md
git commit -m "docs: describe web task forms"
~~~
