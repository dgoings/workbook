# Web Drag Reordering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let web-board drag and drop atomically change a task's status and place it at the pointer-indicated position, clamped to the task's existing priority group.

**Architecture:** Add a core `PlaceMutation` that writes status and rank changes in one immutable operation pack, expose it through a strict position endpoint, and have the embedded client translate pointer gaps into same-priority anchors. Preserve the existing `move` CLI contract and reuse its exact-rational `movedRank` helper.

**Tech Stack:** Go 1.26, `net/http`, embedded HTML/CSS/JavaScript, Node-based executable client tests, temporary Git repositories.

## Global Constraints

- Work on the existing local branch `codex/web-drag-reordering`; do not create a worktree.
- Target `main` with the final pull request.
- Keep task priority unchanged during every drag.
- Clamp drops above or below the dragged task's priority group to the nearest group boundary.
- Apply the same placement behavior to same-status and cross-status drops.
- Persist a cross-status status-and-rank change in one operation pack on only the moved task's ref.
- Keep `workbook move` and `PATCH /api/tasks/<id>/status` backward compatible.
- Keep SQLite disposable and Git task operations canonical.
- Add no runtime or JavaScript dependencies.

---

### Task 1: Add atomic core placement

**Files:**
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**
- Consumes: `Service.resolveSnapshot`, `movedRank`, `Service.assignOperationIDs`, and `Service.writeMutation`.
- Produces: `PlaceInput{Status Status, Before string, After string}`.
- Produces: `Service.PlaceMutation(context.Context, string, PlaceInput) (MutationResult, error)`.

- [ ] **Step 1: Write the failing atomic cross-status placement test**

Add a service test that proves status and rank are written together and only the
moved task is the parent of the write:

```go
func TestServicePlaceMovesAcrossStatusAndRankInOneWrite(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "9/1",
	})
	previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
		Title: "previous", Status: StatusInProgress, Priority: PriorityMedium, Rank: "2/1",
	})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
		Title: "anchor", Status: StatusInProgress, Priority: PriorityMedium, Rank: "4/1",
	})
	store := newMemoryTaskStore(moved, previous, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E4",
		"01K0M6B8A4FTT8C39MXXYTW7E5",
	}})

	result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{
		Status: StatusInProgress,
		Before: anchor.State.TaskID,
	})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if got, want := result.Task.Status, StatusInProgress; got != want {
		t.Fatalf("PlaceMutation() status = %q, want %q", got, want)
	}
	if got, want := result.Task.Rank, "3/1"; got != want {
		t.Fatalf("PlaceMutation() rank = %q, want %q", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
	}
	if got, want := store.writes[0].parent.State.TaskID, moved.State.TaskID; got != want {
		t.Fatalf("PlaceMutation() wrote task %q, want %q", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E4", Type: OperationFieldSet, Field: "status", Value: "in-progress"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E5", Type: OperationFieldSet, Field: "rank", Value: "3/1"},
	})
}
```

- [ ] **Step 2: Run the focused test and verify the missing API failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run TestServicePlaceMovesAcrossStatusAndRankInOneWrite -count=1
```

Expected: compilation fails because `PlaceInput` and `PlaceMutation` do not
exist.

- [ ] **Step 3: Add boundary, empty-bucket, no-op, and representative validation tests**

Add focused tests with these exact assertions:

```go
func TestServicePlaceWithoutAnchorMovesIntoEmptyPriorityBucket(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "7/1",
	})
	otherPriority := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "high", Status: StatusDone, Priority: PriorityHigh, Rank: "1/1",
	})
	store := newMemoryTaskStore(moved, otherPriority)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7F3",
	}})

	result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{Status: StatusDone})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if result.Task.Status != StatusDone || result.Task.Rank != "7/1" {
		t.Fatalf("PlaceMutation() task = %#v, want done with unchanged rank", result.Task)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{
		ID: "01K0M6B8A4FTT8C39MXXYTW7F3", Type: OperationFieldSet, Field: "status", Value: "done",
	}})
}
```

Also add:

- a same-status before-first assertion that produces rank `1/1`;
- a same-status after-last assertion that produces rank `5/1`;
- a same-status, anchorless, sole-priority-task request that returns the
  existing task without requesting an operation ID or writing;
- a request with both `Before` and `After` that returns
  `CategoryValidation`; and
- one anchor whose priority does not match the moved task that returns
  `CategoryValidation` without writing.

Do not duplicate the existing compare-and-swap and projection-warning
permutations for placement. The atomic test above proves placement enters the
shared `writeMutation` path; the existing write-path tests remain the oracle for
those generic behaviors.

- [ ] **Step 4: Implement `PlaceInput` and `PlaceMutation`**

Add the input beside `MoveInput`:

```go
type PlaceInput struct {
	Status Status
	Before string
	After  string
}
```

Implement the mutation with this control flow:

```go
func (s Service) PlaceMutation(ctx context.Context, idOrPrefix string, input PlaceInput) (MutationResult, error) {
	if !isValidStatus(input.Status) {
		return MutationResult{}, Errorf(CategoryValidation, "invalid task status %q", input.Status)
	}
	if input.Before != "" && input.After != "" {
		return MutationResult{}, Errorf(CategoryValidation, "placement accepts at most one anchor direction")
	}

	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot place a tombstoned task")
	}

	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}

	anchorInput := input.Before
	if anchorInput == "" {
		anchorInput = input.After
	}
	rank := parent.State.Task.Rank
	if anchorInput == "" {
		for _, snapshot := range snapshots {
			task := snapshot.State.Task
			if snapshot.State.TaskID != parent.State.TaskID &&
				!task.Deleted &&
				task.Status == input.Status &&
				task.Priority == parent.State.Task.Priority {
				return MutationResult{}, Errorf(CategoryValidation, "placement requires an anchor when the destination bucket is not empty")
			}
		}
	} else {
		anchor, resolveErr := s.resolveSnapshot(ctx, anchorInput)
		if resolveErr != nil {
			return MutationResult{}, resolveErr
		}
		if anchor.State.Task.Deleted ||
			anchor.State.TaskID == parent.State.TaskID ||
			anchor.State.Task.Status != input.Status ||
			anchor.State.Task.Priority != parent.State.Task.Priority {
			return MutationResult{}, Errorf(CategoryValidation, "placement anchor must be an active different task in the destination status and priority bucket")
		}
		rank, err = movedRank(snapshots, parent.State.TaskID, anchor.State.Task, input.Before != "")
		if err != nil {
			return MutationResult{}, err
		}
	}

	operations := make([]Operation, 0, 2)
	if parent.State.Task.Status != input.Status {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "status", Value: string(input.Status)})
	}
	if parent.State.Task.Rank != rank {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "rank", Value: rank})
	}
	if len(operations) == 0 {
		return MutationResult{Task: Project(parent)}, nil
	}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "place task")
}
```

- [ ] **Step 5: Run focused and complete core tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run 'Place|Move' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -count=1
```

Expected: both commands pass.

- [ ] **Step 6: Commit the core placement increment**

```sh
git add internal/core/service.go internal/core/service_test.go
git commit -m "feat: add atomic task placement"
```

---

### Task 2: Expose placement through the local web API

**Files:**
- Modify: `internal/webui/handler.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/cli/run.go`

**Interfaces:**
- Consumes: `core.PlaceInput` and `Service.PlaceMutation`.
- Produces: `TaskPositionUpdater`.
- Produces: `PATCH /api/tasks/{id}/position` with required `status` and optional `before` or `after`.
- Preserves: `PATCH /api/tasks/{id}/status`.

- [ ] **Step 1: Write the failing handler routing and callback test**

Add a handler test that captures the exact placement input:

```go
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
}
```

- [ ] **Step 2: Run the handler test and verify the missing route failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerPositionsTask -count=1
```

Expected: compilation fails because the handler constructor and position
callback do not exist.

- [ ] **Step 3: Add strict request, method, and error-document coverage**

Extend handler tests to prove:

```go
response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+taskID+"/position",
	`{"status":"ready","before":"WB-A","after":"WB-B"}`)
if response.Code != http.StatusBadRequest {
	t.Fatalf("ambiguous position status = %d, want %d", response.Code, http.StatusBadRequest)
}
```

The callback for that request returns
`core.Errorf(core.CategoryValidation, "placement accepts at most one anchor direction")`;
assert a `workbook.error` version 1 document with category `validation`.

Add `/api/tasks/<id>/position` to the wrong-method table with `GET` producing
`405` and `Allow: PATCH`. Keep `TestHandlerPreservesStatusMutationRoute`
unchanged except for constructor adjustments required by the new callback.

Send one strict-decoding request with an unknown `rank` field:

```go
response = requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+taskID+"/position",
	`{"status":"ready","rank":"1/1"}`)
if response.Code != http.StatusBadRequest {
	t.Fatalf("unknown position field status = %d, want %d", response.Code, http.StatusBadRequest)
}
var document ErrorDocument
if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
	t.Fatalf("decode position error: %v", err)
}
if document.Error.Category != core.CategoryInvocation {
	t.Fatalf("unknown position field category = %q, want %q", document.Error.Category, core.CategoryInvocation)
}
```

- [ ] **Step 4: Implement the position handler and route**

Add:

```go
type TaskPositionUpdater func(context.Context, string, core.PlaceInput) (core.MutationResult, error)

type positionTaskRequest struct {
	Status core.Status `json:"status"`
	Before string      `json:"before"`
	After  string      `json:"after"`
}
```

Store the callback on `handler`, pass it through `newHandler`, add it between
`updateStatus` and `delete` in `NewHandlerWithTaskMutations`, and register:

```go
handler.mux.HandleFunc("PATCH /api/tasks/{id}/position", handler.positionTask)
```

Add `taskPositionPathID` using the same prefix/suffix validation as
`taskStatusPathID`, and check it before the generic task path in
`allowedMethod`.

Implement:

```go
func (handler *handler) positionTask(writer http.ResponseWriter, request *http.Request) {
	if handler.position == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task positioning is not configured"))
		return
	}
	var body positionTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode task position", err))
		return
	}
	result, err := handler.position(
		request.Context(),
		request.PathValue("id"),
		core.PlaceInput{Status: body.Status, Before: body.Before, After: body.After},
	)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}
```

- [ ] **Step 5: Wire `workbook serve` directly to core placement**

Update the `NewHandlerWithTaskMutations` call in `runServe` with:

```go
func(requestContext context.Context, id string, input core.PlaceInput) (core.MutationResult, error) {
	return service.PlaceMutation(requestContext, id, input)
},
```

Update the one existing handler-test call to `NewHandlerWithTaskMutations` by
adding an `unexpectedTaskPosition(t)` helper:

```go
func unexpectedTaskPosition(t *testing.T) TaskPositionUpdater {
	t.Helper()
	return func(context.Context, string, core.PlaceInput) (core.MutationResult, error) {
		t.Fatal("unexpected task position")
		return core.MutationResult{}, nil
	}
}
```

- [ ] **Step 6: Run focused handler and CLI compilation tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'Position|WrongMethods|PreservesStatus' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestRunServe -count=1
```

Expected: both commands pass.

- [ ] **Step 7: Commit the web API increment**

```sh
git add internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go
git commit -m "feat: expose atomic web placement"
```

---

### Task 3: Clamp pointer gaps and render the insertion marker

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Consumes: rendered task ID, status, priority, and board order.
- Produces: `{status, before}` or `{status, after}` placement requests.
- Produces: an anchorless `{status}` request when the destination has no same-priority peer.

- [ ] **Step 1: Extend the executable DOM harness for placement behavior**

Enhance `TestElement` with DOM operations used by the insertion marker:

```javascript
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
querySelectorAll(selector) {
  const matches = [];
  const visit = (element) => {
    for (const child of element.children || []) {
      if (selector === ".task-card" && child.className === "task-card") matches.push(child);
      visit(child);
    }
  };
  visit(this);
  return matches;
}
getBoundingClientRect() {
  return this.rect || { top: 0, bottom: 0 };
}
```

Change the fetch stub to retain mutation calls while preserving the existing
task-document response:

```javascript
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
  return { ok: true, json: async () => taskResponse };
};
```

- [ ] **Step 2: Write the failing same-column clamping test**

Create an executable client test with one ready column containing high,
medium, medium, and low tasks. After initial refresh:

```javascript
const ready = boardLists.find((list) => list.dataset.status === "ready");
const cards = ready.querySelectorAll(".task-card");
cards.forEach((item, index) => { item.rect = { top: index * 100, bottom: index * 100 + 80 }; });
const moved = cards.find((item) => item.dataset.taskId === movedTaskID);
const high = cards.find((item) => item.dataset.priority === "high");
const firstMedium = cards.find((item) => item.dataset.priority === "medium" && item !== moved);
const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

documentEventListeners.dragstart({ target: moved, dataTransfer });
let prevented = false;
documentEventListeners.dragover({
  target: high,
  clientY: 1,
  dataTransfer,
  preventDefault() { prevented = true; }
});

const markerIndex = ready.children.findIndex((item) => item.className === "drop-marker");
const firstMediumIndex = ready.children.indexOf(firstMedium);
if (!prevented || markerIndex !== firstMediumIndex - 1) {
  throw new Error("top-column drop did not clamp before the first medium-priority peer");
}
```

Repeat the dragover at the low-priority card's bottom edge and assert the marker
is immediately after the last medium-priority peer and before the low-priority
card.

- [ ] **Step 3: Write the failing atomic cross-status request test**

Use the executable harness to drag a medium Ready task above a high-priority
In Progress card when that column also has a medium task. Drop and assert:

```javascript
const mutation = fetchCalls.find((call) => call.options.method === "PATCH");
if (!mutation || mutation.url !== "/api/tasks/" + encodeURIComponent(movedTaskID) + "/position") {
  throw new Error("drop did not call the position endpoint");
}
const body = JSON.parse(mutation.options.body);
if (body.status !== "in-progress" || body.before !== destinationMediumID || body.after) {
  throw new Error("cross-status drop did not send the clamped medium-priority anchor");
}
```

Add one destination with high and low tasks but no medium peer. Assert its
marker appears between those priority groups and its request body is exactly:

```json
{"status":"done"}
```

- [ ] **Step 4: Add task priority data and insertion-marker styling**

Render priority on both server and client cards:

```html
<article class="task-card" tabindex="0" data-task-id="{{ .Task.ID }}" data-priority="{{ .Task.Priority }}" data-id-prefix="{{ .IDPrefix }}" draggable="true" aria-label="Move task {{ .Task.Title }} from {{ .Task.Status }}">
```

```javascript
article.dataset.priority = task.priority;
```

Replace the whole-list allowed/disallowed outlines with:

```css
.drop-marker {
  height: 0;
  margin: -.1rem 0;
  border-top: 3px solid #2457d6;
  border-radius: 3px;
  pointer-events: none;
}
```

- [ ] **Step 5: Implement priority-clamped placement calculation**

Add:

```javascript
const priorityOrder = new Map([["high", 0], ["medium", 1], ["low", 2]]);
const dropMarker = document.createElement("div");
dropMarker.className = "drop-marker";
dropMarker.setAttribute("aria-hidden", "true");

function placementFor(list, clientY) {
  const cards = [...list.querySelectorAll(".task-card")]
    .filter((item) => item.dataset.taskId !== activeDrag.taskId);
  let intended = cards.findIndex((item) => {
    const bounds = item.getBoundingClientRect();
    return clientY < (bounds.top + bounds.bottom) / 2;
  });
  if (intended < 0) intended = cards.length;

  const peers = cards.filter((item) => item.dataset.priority === activeDrag.priority);
  if (peers.length === 0) {
    const draggedOrder = priorityOrder.get(activeDrag.priority);
    let index = cards.findIndex((item) => priorityOrder.get(item.dataset.priority) > draggedOrder);
    if (index < 0) index = cards.length;
    return { status: list.dataset.dropStatus, reference: cards[index] || null };
  }

  const first = cards.indexOf(peers[0]);
  const last = cards.indexOf(peers[peers.length - 1]);
  const index = Math.max(first, Math.min(intended, last + 1));
  if (index <= first) {
    return {
      status: list.dataset.dropStatus,
      before: peers[0].dataset.taskId,
      reference: peers[0]
    };
  }
  if (index > last) {
    return {
      status: list.dataset.dropStatus,
      after: peers[peers.length - 1].dataset.taskId,
      reference: cards[index] || null
    };
  }
  return {
    status: list.dataset.dropStatus,
    before: cards[index].dataset.taskId,
    reference: cards[index]
  };
}

function showPlacement(list, placement) {
  dropMarker.remove();
  list.insertBefore(dropMarker, placement.reference);
}

function clearPlacement() {
  dropMarker.remove();
}
```

On drag start, capture:

```javascript
activeDrag = {
  taskId: item.dataset.taskId,
  priority: item.dataset.priority,
  sourceStatus: source.dataset.status
};
```

- [ ] **Step 6: Replace status-only drop behavior with placement requests**

Allow same-column dragover, compute and display placement, and use:

```javascript
document.addEventListener("dragover", (event) => {
  const target = dropTarget(event);
  if (!target || !activeDrag || !statuses.has(target.dataset.dropStatus)) return;
  const placement = placementFor(target, event.clientY);
  showPlacement(target, placement);
  event.preventDefault();
  event.dataTransfer.dropEffect = "move";
});

document.addEventListener("drop", async (event) => {
  const target = dropTarget(event);
  if (!target || !activeDrag || !statuses.has(target.dataset.dropStatus)) return;
  event.preventDefault();
  const taskId = activeDrag.taskId;
  const sourceStatus = activeDrag.sourceStatus;
  const placement = placementFor(target, event.clientY);
  clearPlacement();
  if (placement.status === sourceStatus && !placement.before && !placement.after) return;

  const body = { status: placement.status };
  if (placement.before) body.before = placement.before;
  if (placement.after) body.after = placement.after;
  try {
    await mutateTask("PATCH", "/api/tasks/" + encodeURIComponent(taskId) + "/position", body);
    await refresh();
  } catch (_) {
    await refresh();
    text(stale, "Task placement failed. Showing the latest board.");
    stale.dataset.visible = "true";
  }
});
```

Update `dragend` and `dragleave` to clear the marker. Remove `canDropOn`,
`setDropState`, and the `can-drop`/`cannot-drop` classes.

- [ ] **Step 7: Run the focused executable client and web suite**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'Drag|Drop|Position|Placement' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
```

Expected: both commands pass with Node available; the tests skip only when Node
is genuinely absent.

- [ ] **Step 8: Commit the client placement increment**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: reorder web tasks by drag position"
```

---

### Task 4: Prove Git persistence and align documentation

**Files:**
- Modify: `internal/cli/run_test.go`
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-07-29-web-drag-reordering.md`

**Interfaces:**
- Consumes: the running `workbook serve` position endpoint.
- Produces: real-repository evidence that one placement commit changes only the moved task ref.
- Produces: current route and drag behavior documentation.

- [ ] **Step 1: Write the failing real-repository server test**

Create two medium-priority tasks through the CLI: one Ready task to move and one
In Progress anchor. Capture both heads, start `runServe`, and send:

```go
request, err := http.NewRequest(
	http.MethodPatch,
	"http://"+addr+"/api/tasks/"+moved.ID+"/position",
	strings.NewReader(`{"status":"in-progress","before":"`+anchor.ID+`"}`),
)
if err != nil {
	t.Fatal(err)
}
request.Header.Set("Content-Type", "application/json")
response, err := http.DefaultClient.Do(request)
if err != nil {
	t.Fatalf("PATCH position: %v", err)
}
body, readErr := io.ReadAll(response.Body)
response.Body.Close()
if readErr != nil {
	t.Fatal(readErr)
}
if response.StatusCode != http.StatusOK {
	t.Fatalf("PATCH position = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
}
```

After stopping the server, assert:

```go
movedHeadAfter := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+moved.ID)
anchorHeadAfter := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+anchor.ID)
if movedHeadAfter == movedHeadBefore {
	t.Fatal("position did not advance the moved task ref")
}
if anchorHeadAfter != anchorHeadBefore {
	t.Fatal("position advanced the anchor task ref")
}
parents := strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", movedHeadAfter))
if len(parents) != 2 || parents[1] != movedHeadBefore {
	t.Fatalf("position commit parents = %#v, want sole parent %q", parents, movedHeadBefore)
}
var pack core.OperationPack
if err := json.Unmarshal([]byte(gitOutput(t, repository, "show", movedHeadAfter+":operation.json")), &pack); err != nil {
	t.Fatalf("decode placement operation pack: %v", err)
}
if len(pack.Operations) != 2 ||
	pack.Operations[0].Field != "status" || pack.Operations[0].Value != "in-progress" ||
	pack.Operations[1].Field != "rank" {
	t.Fatalf("placement operations = %#v", pack.Operations)
}
```

Also decode the returned mutation task and assert its status is In Progress and
its rank sorts before the anchor's rank.

- [ ] **Step 2: Run the focused server integration test**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestRunServePositionsTaskThroughWebRoute -count=1
```

Expected before production wiring is complete: the position route is missing or
does not persist both fields. Expected after Tasks 1-3: pass.

- [ ] **Step 3: Update README routes and drag behavior**

Add the position route:

```text
PATCH /api/tasks/<id>/position  atomically change status and board position
```

Replace the status-only drag paragraph with:

```markdown
Drag a task card within a column to reorder it or into another canonical status
column to change status and position together. Workbook keeps the task's priority
unchanged and clamps drops outside that priority group to the nearest group
boundary, so dropping at the top or bottom of a column still has a clear result.
The placement creates one normal Workbook operation commit on the moved task and
returns a versioned JSON task-mutation document. The older status-only endpoint
remains available for compatible clients.
```

- [ ] **Step 4: Run formatting and focused package verification**

Run:

```sh
gofmt -w internal/core/service.go internal/core/service_test.go internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go internal/cli/run_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core ./internal/webui ./internal/cli -count=1
```

Expected: formatting changes no semantics and all focused packages pass.

- [ ] **Step 5: Run complete verification**

Run each command and inspect its exit status:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-web-drag ./cmd/workbook
git diff --check
git status --short
```

Then start `/private/tmp/workbook-web-drag serve` against a temporary repository
and verify in a browser at narrow and wide viewport sizes:

- same-column movement renders and follows the insertion marker;
- top and bottom drops clamp to the dragged task's priority group;
- cross-column movement changes status and placement together; and
- a rejected placement refreshes the board and shows the visible error state.

- [ ] **Step 6: Mark plan steps complete and commit integration and docs**

```sh
git add internal/cli/run_test.go README.md docs/superpowers/plans/2026-07-29-web-drag-reordering.md
git commit -m "test: verify durable web placement"
```

- [ ] **Step 7: Review the complete branch before publication**

Run:

```sh
git log --oneline origin/main..HEAD
git diff --stat origin/main...HEAD
git diff --check origin/main...HEAD
git status -sb
```

Read the complete `origin/main...HEAD` diff and confirm every approved design
requirement maps to implementation or verification evidence before requesting
code review.
