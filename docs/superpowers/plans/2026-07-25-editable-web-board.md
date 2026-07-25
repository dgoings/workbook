# Editable Web Board Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the local Workbook web board change task status by dragging cards between workflow columns.

**Architecture:** `internal/webui` owns the HTTP mutation route and embedded browser behavior, but it does not mutate task storage directly. The CLI `serve` command wires the handler to `core.Service.Update`, so drag-and-drop produces the same immutable operation commits as `workbook update --status`. The UI stays loopback-only, compact, and work-focused.

**Tech Stack:** Go 1.26, Go standard library `net/http`, embedded HTML/CSS/JavaScript, native HTML drag-and-drop APIs.

## Global Constraints

- Keep `workbook serve` bound to the requested local listener; do not add hosting, authentication, a daemon, or non-local defaults.
- Preserve Workbook's versioned JSON success and error envelopes.
- Mutations must use the same core service path as CLI task updates.
- Unknown or unrecognized statuses are view-only drop targets; only canonical workflow statuses accept status changes.
- Dragging a task to its current status is a no-op in the browser and must not call the mutation endpoint.
- Dragging between statuses does not change rank in this slice.
- Maintain the existing two-second `/api/tasks` polling behavior.
- Follow TDD: write failing tests before production code.

---

### Task 1: Add a status mutation HTTP route

**Files:**
- Modify: `internal/webui/handler.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/webui/server_test.go`
- Modify: `internal/cli/run.go`

**Interfaces:**
- Consumes: `core.Task`, `core.Status`, `core.UpdateInput`, and `core.Service.Update(context.Context, string, core.UpdateInput) (core.Task, error)`.
- Produces: `type TaskStatusUpdater func(context.Context, string, core.Status) (core.Task, error)` and `func NewHandler(list TaskLister, updateStatus TaskStatusUpdater) http.Handler`.

- [ ] **Step 1: Write a failing HTTP mutation test**

Add `TestHandlerUpdatesTaskStatus` to `internal/webui/handler_test.go`:

```go
func TestHandlerUpdatesTaskStatus(t *testing.T) {
	var gotID string
	var gotStatus core.Status
	updated := boardTasks()[0]
	updated.Status = core.StatusInProgress
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		func(_ context.Context, id string, status core.Status) (core.Task, error) {
			gotID = id
			gotStatus = status
			return updated, nil
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
```

Add `requestJSON` beside `request` so tests can send JSON bodies.

- [ ] **Step 2: Verify the test fails**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerUpdatesTaskStatus -count=1`

Expected: FAIL because `NewHandler` does not accept an updater and `TaskMutationDocument` does not exist.

- [ ] **Step 3: Implement the route and document shape**

In `internal/webui/handler.go`, add:

```go
type TaskStatusUpdater func(context.Context, string, core.Status) (core.Task, error)

type TaskMutationDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Task    core.Task `json:"task"`
}

type updateStatusRequest struct {
	Status core.Status `json:"status"`
}
```

Store the updater on `handler`. Register:

```go
handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
```

Implement `updateTaskStatus` to decode exactly one JSON object, reject blank/extra/malformed status through existing core validation by calling the updater, and return `TaskMutationDocument{Format: "workbook.task-mutation", Version: 1, Task: task}`.

- [ ] **Step 4: Preserve method and path behavior**

Update `isKnownPath` and `serveHTTP` so `/api/tasks/<id>/status` allows only `PATCH`, while `/`, `/api/tasks`, and `/healthz` still allow only `GET`. Add tests proving `GET /api/tasks/<id>/status` and `POST /api/tasks/<id>/status` return `405` with `Allow: PATCH`.

- [ ] **Step 5: Wire the real service**

In `internal/cli/run.go`, change `runServe` to call:

```go
handler := webui.NewHandler(
	func(requestContext context.Context) ([]core.Task, error) {
		return service.List(requestContext, core.ListFilter{})
	},
	func(requestContext context.Context, id string, status core.Status) (core.Task, error) {
		return service.Update(requestContext, id, core.UpdateInput{Status: &status})
	},
)
```

Update webui tests that construct `NewHandler` to pass a helper updater that returns a validation error if unexpectedly called.

- [ ] **Step 6: Verify Task 1**

Run:

```sh
gofmt -w internal/webui internal/cli
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run Serve -count=1
```

Expected: all commands exit 0.

---

### Task 2: Add drag-and-drop status changes to the embedded board

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Consumes: `PATCH /api/tasks/{id}/status` with request body `{"status":"ready"}`.
- Produces: draggable cards, status-column drop zones, optimistic refresh after successful mutation, and visible failure feedback through the existing stale/status banner.

- [ ] **Step 1: Write failing asset contract tests**

Add assertions to `TestHandlerServesBoardTasksAndHealth` or a new `TestHandlerServesDragAndDropBoardControls` requiring the rendered HTML/script to contain these exact fragments:

```go
for _, fragment := range []string{
	`draggable="true"`,
	`aria-label="Move task`,
	`data-drop-status="{{ .Status }}"`,
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
```

- [ ] **Step 2: Verify the asset test fails**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerServesDragAndDropBoardControls -count=1`

Expected: FAIL because cards are not draggable and no mutation script exists.

- [ ] **Step 3: Make cards draggable and columns droppable**

In the `task` template, add `draggable="true"` and an `aria-label` that includes the task title and current status. Add `data-drop-status="{{ .Status }}"` to canonical status `.task-list` elements. Do not add `data-drop-status` to the unrecognized-status list.

- [ ] **Step 4: Add focused CSS states**

Add restrained styles for:

```css
.task-card.is-dragging { opacity: .5; }
.task-list.can-drop { outline: 2px dashed #2457d6; outline-offset: -4px; background: rgba(36,87,214,.08); }
.task-list.cannot-drop { outline: 2px dashed #b45309; outline-offset: -4px; background: rgba(180,83,9,.08); }
```

Keep the current board layout and palette.

- [ ] **Step 5: Implement drag events and mutation fetch**

In the embedded script:

```js
function setDropState(list, allowed) { ... }
async function updateStatus(taskId, status) { ... }
```

On `dragstart`, write the task ID and source status to `dataTransfer`. On `dragover`, allow drops only onto canonical status lists where target status differs from source status. On `drop`, call `PATCH /api/tasks/${encodeURIComponent(taskId)}/status` with JSON body and then call `refresh()` on success. On failure, show the existing stale banner text as `Task update failed. Showing the last successful board.`

Ensure `card()` creates the same draggable attributes for refreshed cards.

- [ ] **Step 6: Verify Task 2**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
```

Expected: all commands exit 0.

---

### Task 3: Document and verify the editable slice

**Files:**
- Modify: `README.md`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: implemented `workbook serve` drag-and-drop status mutation.
- Produces: README language that says the web board supports drag-and-drop status changes but not general editing, auth, or non-local hosting.

- [ ] **Step 1: Write or update a README contract test**

Update the README contract test in `internal/cli/run_test.go` so it requires the implemented command documentation to mention drag-and-drop status changes and the route `PATCH /api/tasks/<id>/status`.

- [ ] **Step 2: Verify the README test fails**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run README -count=1`

Expected: FAIL because README still says the board is read-only.

- [ ] **Step 3: Update README**

Change the status summary and web board section to say `workbook serve` remains loopback-only and read-mostly, but now supports drag-and-drop status changes through `PATCH /api/tasks/<id>/status`. State that title, description, labels, priority editing, authentication, and non-local hosting remain future work.

- [ ] **Step 4: Run final verification**

Run:

```sh
gofmt -w internal/webui internal/cli
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit**

```sh
git add README.md docs/superpowers/plans/2026-07-25-editable-web-board.md internal/cli/run.go internal/cli/run_test.go internal/webui/handler.go internal/webui/handler_test.go internal/webui/server_test.go internal/webui/assets/index.html
git commit -m "feat: support editable web board status changes"
```
