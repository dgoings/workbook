# Web Dependency Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show dependency readiness on web-board cards and let users inspect, add, and remove both Depends On and Blocks relationships from task detail pages.

**Architecture:** Keep `TaskData.Dependencies` as the only durable directed edge, derive Blocks by scanning known task documents, and expose nested `PUT`/`DELETE` routes that delegate to the existing core mutations. Extend the shared presentation model for card summaries, then add a mounted relationship controller to the embedded client so dependency refreshes never reconstruct or discard the task form.

**Tech Stack:** Go 1.26, `net/http`, `html/template`, embedded HTML/CSS/vanilla JavaScript, Node-based executable client tests, temporary Git repositories.

## Global Constraints

- Work only in `/Users/dylan.goings/source/workbook/.worktrees/wb-dependency-web-ui` on `codex/wb-dependency-web-ui`.
- Target `main` with the final pull request.
- Keep `TaskData.Dependencies` and `set.add`/`set.remove dependencies` as the only durable relationship representation.
- Derive `B blocks A` from `A depends on B`; do not add a durable `blocks` field.
- Preserve tombstone immutability. Deleted prerequisites are removable from active dependents; deleted blocked tasks are visible but read-only.
- Preserve the loopback-only server and `workbook.tasks`, `workbook.task-mutation`, and `workbook.error` version 1 envelopes.
- Keep task creation free of dependency editing.
- Preserve unsaved title, description, status, priority, and label values across every dependency success or failure.
- Add no runtime or JavaScript dependencies.
- Use red-green-refactor for every production behavior.

---

### Task 1: Remove an unavailable stored prerequisite through `FreeMutation`

**Files:**
- Modify: `internal/core/service.go:415-435`
- Modify: `internal/core/service_test.go:982-1003`

**Interfaces:**
- Consumes: `Service.resolveSnapshot`, `ValidateTaskID`, `hasDependency`, and `Service.writeMutation`.
- Produces: unchanged `Service.FreeMutation(context.Context, string, string) (MutationResult, error)` with exact-full-ID fallback.
- Preserves: prefix resolution and idempotent absent-edge behavior.

- [ ] **Step 1: Write the failing unavailable-prerequisite removal test**

Add this test beside the existing FreeMutation coverage:

```go
func TestServiceFreeRemovesStoredDependencyWhenReferencedTaskIsUnavailable(t *testing.T) {
	dependency := "WB-01K0M6B8A4FTT8C39MXXYTW7E2"
	dependent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
		Title: "dependent", Status: StatusReady, Priority: PriorityHigh,
		Rank: "1/1", Dependencies: []string{dependency},
	})
	store := newMemoryTaskStore(dependent)
	service := serviceUnderTest(store, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"},
	})

	result, err := service.FreeMutation(context.Background(), dependent.State.TaskID, dependency)
	if err != nil {
		t.Fatalf("FreeMutation() error = %v", err)
	}
	if got, want := result.Task.Dependencies, []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FreeMutation() dependencies = %#v, want %#v", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("FreeMutation() writes = %d, want %d", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{
		ID:    "01K0M6B8A4FTT8C39MXXYTW7E3",
		Type:  OperationSetRemove,
		Field: "dependencies",
		Value: dependency,
	}})
}
```

The production mutation that should make this test fail is restoring the
unconditional referenced-task lookup before checking the stored exact ID.

- [ ] **Step 2: Run the focused test and verify the expected red result**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run TestServiceFreeRemovesStoredDependencyWhenReferencedTaskIsUnavailable -count=1
```

Expected: FAIL with `task ... not found`, because `FreeMutation` tries to load
the missing prerequisite.

- [ ] **Step 3: Implement exact stored-ID removal before fallback resolution**

Replace the dependency-resolution portion of `FreeMutation` with:

```go
	dependencyID := dependencyOrPrefix
	if ValidateTaskID(s.Config.Key, dependencyID) != nil ||
		!hasDependency(parent.State.Task.Dependencies, dependencyID) {
		dependency, err := s.resolveSnapshot(ctx, dependencyOrPrefix)
		if err != nil {
			return MutationResult{}, err
		}
		dependencyID = dependency.State.TaskID
	}
	if !hasDependency(parent.State.Task.Dependencies, dependencyID) {
		return MutationResult{Task: Project(parent)}, nil
	}
	operations := []Operation{{
		Type: OperationSetRemove, Field: "dependencies", Value: dependencyID,
	}}
```

Keep the existing tombstoned-dependent guard, operation-ID assignment, durable
write, and `"remove dependency"` subject unchanged.

- [ ] **Step 4: Verify exact-ID, tombstone, prefix, and idempotency behavior**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run 'Free|Depend' -count=1
```

Expected: PASS. Mentally mutating the exact-ID condition to always resolve must
fail the new unavailable-prerequisite test.

- [ ] **Step 5: Commit the core increment**

```sh
git add internal/core/service.go internal/core/service_test.go
git commit -m "fix: remove unavailable dependencies"
```

---

### Task 2: Add dependency progress to the shared board presentation

**Files:**
- Modify: `internal/presentation/board.go`
- Modify: `internal/presentation/board_test.go`
- Modify: `internal/webui/handler.go:36-53,249-277,430-436`
- Modify: `internal/webui/assets/index.html:1-7,42-56,137-150,379-405`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: `presentation.TaskView.DependenciesComplete int`.
- Produces: `presentation.TaskView.DependenciesTotal int`.
- Produces: `presentation.TaskView.WaitingOnDependencies bool`.
- Produces: matching `TaskPresentation` JSON fields
  `dependenciesComplete`, `dependenciesTotal`, and `waitingOnDependencies`.
- Preserves: the existing shortest unique ID prefix and canonical board order.

- [ ] **Step 1: Write the failing presentation semantics test**

Add:

```go
func TestTaskViewsSummarizeDependencyReadiness(t *testing.T) {
	done := core.Task{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{Status: core.StatusDone},
	}
	active := core.Task{
		ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW",
		TaskData: core.TaskData{Status: core.StatusInProgress},
	}
	ready := core.Task{
		ID: "WB-01CRZ3NDEKTSV4RRFFQ69G5FAX",
		TaskData: core.TaskData{
			Status: core.StatusReady,
			Dependencies: []string{
				done.ID,
				active.ID,
				"WB-01DRZ3NDEKTSV4RRFFQ69G5FAY",
			},
		},
	}
	inReview := ready
	inReview.ID = "WB-01ERZ3NDEKTSV4RRFFQ69G5FAZ"
	inReview.Status = core.StatusInReview
	withoutDependencies := core.Task{
		ID: "WB-01FRZ3NDEKTSV4RRFFQ69G5FA0",
		TaskData: core.TaskData{Status: core.StatusReady},
	}

	views := TaskViews([]core.Task{done, active, ready, inReview, withoutDependencies})
	if got := views[2]; got.DependenciesComplete != 1 ||
		got.DependenciesTotal != 3 || !got.WaitingOnDependencies {
		t.Fatalf("ready dependency summary = %#v, want 1/3 waiting", got)
	}
	if got := views[3]; got.DependenciesComplete != 1 ||
		got.DependenciesTotal != 3 || got.WaitingOnDependencies {
		t.Fatalf("in-review dependency summary = %#v, want 1/3 not waiting", got)
	}
	if got := views[4]; got.DependenciesComplete != 0 ||
		got.DependenciesTotal != 0 || got.WaitingOnDependencies {
		t.Fatalf("dependency-free summary = %#v, want zero values", got)
	}
}
```

The mutation this test catches is counting any active prerequisite instead of
only active `done` prerequisites.

- [ ] **Step 2: Run the presentation test and verify missing-field compilation failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/presentation -run TestTaskViewsSummarizeDependencyReadiness -count=1
```

Expected: compilation fails because the three TaskView fields do not exist.

- [ ] **Step 3: Implement the dependency summary once in `TaskViews`**

Extend `TaskView` and `TaskViews`:

```go
type TaskView struct {
	Task                  core.Task
	IDPrefix              string
	DependenciesComplete  int
	DependenciesTotal     int
	WaitingOnDependencies bool
}

func TaskViews(tasks []core.Task) []TaskView {
	active := make(map[string]core.Task, len(tasks))
	for _, task := range tasks {
		if !task.Deleted {
			active[task.ID] = task
		}
	}
	views := make([]TaskView, len(tasks))
	for i, task := range tasks {
		complete := 0
		for _, dependencyID := range task.Dependencies {
			if dependency, ok := active[dependencyID]; ok &&
				dependency.Status == core.StatusDone {
				complete++
			}
		}
		total := len(task.Dependencies)
		views[i] = TaskView{
			Task:                  task,
			IDPrefix:              shortestUniquePrefix(i, tasks),
			DependenciesComplete:  complete,
			DependenciesTotal:     total,
			WaitingOnDependencies: task.Status == core.StatusReady && complete < total,
		}
	}
	return views
}
```

Because `complete < total` is false for `0 < 0`, Ready tasks without
dependencies remain eligible.

- [ ] **Step 4: Add failing server-rendered and JSON presentation assertions**

In `TestHandlerServesBoardTasksAndHealth`, assert the Ready task renders:

```go
for _, fragment := range []string{
	"0 of 1 prerequisites complete",
	"Waiting on dependencies",
} {
	if !strings.Contains(board.Body.String(), fragment) {
		t.Errorf("GET / body does not contain %q", fragment)
	}
}
```

After decoding `TasksDocument`, assert:

```go
if got := document.Presentation[0]; got.DependenciesComplete != 0 ||
	got.DependenciesTotal != 1 || !got.WaitingOnDependencies {
	t.Fatalf("task presentation = %#v, want 0/1 waiting", got)
}
if strings.Count(board.Body.String(), "prerequisites complete") != 1 {
	t.Fatal("dependency-free cards changed their rendered content")
}
```

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerServesBoardTasksAndHealth -count=1
```

Expected: FAIL because neither HTML nor JSON exposes the summary.

- [ ] **Step 5: Render the same presentation in HTML and refreshed cards**

Extend the web document type:

```go
type TaskPresentation struct {
	TaskID                string `json:"taskId"`
	IDPrefix              string `json:"idPrefix"`
	DependenciesComplete  int    `json:"dependenciesComplete"`
	DependenciesTotal     int    `json:"dependenciesTotal"`
	WaitingOnDependencies bool   `json:"waitingOnDependencies"`
}
```

Copy the fields from each shared view in `taskPresentation`:

```go
result[index] = TaskPresentation{
	TaskID:                view.Task.ID,
	IDPrefix:              view.IDPrefix,
	DependenciesComplete:  view.DependenciesComplete,
	DependenciesTotal:     view.DependenciesTotal,
	WaitingOnDependencies: view.WaitingOnDependencies,
}
```

In the `task` template, after labels, add:

```html
{{ if .DependenciesTotal }}
<div class="dependency-progress" data-dependency-progress>
  <span>{{ .DependenciesComplete }} of {{ .DependenciesTotal }} prerequisites complete</span>
  {{ if .WaitingOnDependencies }}<strong>Waiting on dependencies</strong>{{ end }}
</div>
{{ end }}
```

Add restrained styles using existing colors:

```css
.dependency-progress { display: grid; gap: .18rem; margin-top: .65rem; color: #4e5d73; font-size: .72rem; }
.dependency-progress strong { color: #9c2f25; font-size: .72rem; }
```

Change the client presentation map to retain each full presentation object:

```js
const presentation = new Map(document.presentation.map((view) => [view.taskId, view]));
```

Change `card(task, idPrefix)` to `card(task, view)`, use `view.idPrefix`, and
append the same progress text only when `view.dependenciesTotal > 0`:

```js
if (view.dependenciesTotal > 0) {
  const progress = document.createElement("div");
  progress.className = "dependency-progress";
  progress.dataset.dependencyProgress = "";
  const count = document.createElement("span");
  text(count, view.dependenciesComplete + " of " + view.dependenciesTotal + " prerequisites complete");
  progress.append(count);
  if (view.waitingOnDependencies) {
    const waiting = document.createElement("strong");
    text(waiting, "Waiting on dependencies");
    progress.append(waiting);
  }
  article.append(progress);
}
```

- [ ] **Step 6: Verify presentation and refreshed-card behavior**

Extend an existing executable board test to assert the dynamic Ready card has
both strings and a dependency-free card has no `dataDependencyProgress`
element. Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/presentation ./internal/webui -run 'Dependency|ServesBoardTasksAndHealth|ClientBoard' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the board-presentation increment**

```sh
git add internal/presentation/board.go internal/presentation/board_test.go internal/webui/handler.go internal/webui/handler_test.go internal/webui/assets/index.html
git commit -m "feat: show web dependency progress"
```

---

### Task 3: Expose Git-durable dependency relationship endpoints

**Files:**
- Modify: `internal/webui/handler.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/cli/run.go:679-712`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Produces: `TaskDependencyAdder func(context.Context, string, string) (core.MutationResult, error)`.
- Produces: `TaskDependencyRemover func(context.Context, string, string) (core.MutationResult, error)`.
- Produces: `PUT /api/tasks/{id}/dependencies/{dependency}`.
- Produces: `DELETE /api/tasks/{id}/dependencies/{dependency}`.
- Preserves: `NewHandler` as the simple constructor for tests that do not need
  full mutation wiring.

- [ ] **Step 1: Find every constructor reference before changing its signature**

Run:

```sh
rg -n 'NewHandlerWithTaskMutations|newHandler\(' --glob '*.go'
```

Confirm the only production call is `internal/cli/run.go` and update every test
call in the same commit.

- [ ] **Step 2: Write failing handler callback and document tests**

Add:

```go
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
```

Add a callback that returns
`core.Errorf(core.CategoryValidation, "dependency would create a cycle")` and
assert `400`, `workbook.error` version 1, category `validation`, and the exact
message.

- [ ] **Step 3: Run the handler tests and verify the missing-constructor failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'AddsAndRemovesTaskDependencies|Dependency.*Error' -count=1
```

Expected: compilation fails because dependency callbacks are not accepted.

- [ ] **Step 4: Implement callbacks, routes, and method detection**

Add callback types and handler fields:

```go
type TaskDependencyAdder func(context.Context, string, string) (core.MutationResult, error)
type TaskDependencyRemover func(context.Context, string, string) (core.MutationResult, error)
```

Extend `NewHandlerWithTaskMutations` and `newHandler` with `depend` and `free`
callbacks. Register:

```go
handler.mux.HandleFunc("PUT /api/tasks/{id}/dependencies/{dependency}", handler.addTaskDependency)
handler.mux.HandleFunc("DELETE /api/tasks/{id}/dependencies/{dependency}", handler.removeTaskDependency)
```

Implement both handlers with operational nil-callback errors and the shared
mutation writer:

```go
func (handler *handler) addTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if handler.depend == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency addition is not configured"))
		return
	}
	result, err := handler.depend(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}
```

Add a strict `taskDependencyPathIDs` helper that accepts exactly:

```text
/api/tasks/<non-empty>/dependencies/<non-empty>
```

and no extra slash. Check it before ordinary task paths in `allowedMethod`, and
return `http.MethodPut + ", " + http.MethodDelete`.

Use this parser:

```go
func taskDependencyPathIDs(path string) (string, string, bool) {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 3 || parts[0] == "" ||
		parts[1] != "dependencies" || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}
```

Implement the remove handler explicitly:

```go
func (handler *handler) removeTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if handler.free == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency removal is not configured"))
		return
	}
	result, err := handler.free(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}
```

- [ ] **Step 5: Add wrong-method and malformed-path coverage**

Assert `POST` on a valid dependency path returns `405` with
`Allow: PUT, DELETE`. Assert these paths remain `404`:

```text
/api/tasks/<id>/dependencies
/api/tasks/<id>/dependencies/
/api/tasks/<id>/dependencies/<dependency>/extra
```

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'Dependencies|UnknownRoutesAndMutationMethods' -count=1
```

Expected: PASS.

- [ ] **Step 6: Write the failing real-repository server integration test**

Add `TestRunServeMutatesDependenciesThroughWebRoutes` beside the existing
status and position server tests. Create two tasks, start `runServe`, record both
task ref heads, send nested `PUT`, and assert:

```go
if got, want := mutation.Task.Dependencies, []string{prerequisite.ID}; !reflect.DeepEqual(got, want) {
	t.Fatalf("PUT dependencies = %#v, want %#v", got, want)
}
```

After `PUT`, verify only the dependent ref advanced and its `operation.json`
contains exactly:

```go
core.Operation{Type: core.OperationSetAdd, Field: "dependencies", Value: prerequisite.ID}
```

Send nested `DELETE`, verify the dependent ref advances again, and assert its
new operation is `OperationSetRemove` with the same field and value. Confirm the
prerequisite ref never advances.

- [ ] **Step 7: Wire the core service and verify durable integration**

Pass these callbacks from `runServe`:

```go
func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
	return service.DependMutation(requestContext, id, dependency)
},
func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
	return service.FreeMutation(requestContext, id, dependency)
},
```

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestRunServeMutatesDependenciesThroughWebRoutes -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui ./internal/cli -run 'Dependenc|RunServeMutatesDependencies' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the HTTP increment**

```sh
git add internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: expose web dependency mutations"
```

---

### Task 4: Render Depends On and Blocks relationship state

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: mounted `dependencyController(taskID)` client component.
- Consumes: active `workbook.tasks` document and deleted
  `GET /api/tasks?deleted=true` document.
- Preserves: the mounted task form and every input element.

- [ ] **Step 1: Extend the executable DOM harness for relationship behavior**

Add element support required by real client code:

```js
focus() { globalThis.activeElement = this; }
get id() { return this.attributes.id || this._id || ""; }
set id(value) { this._id = String(value); this.attributes.id = String(value); }
removeAttribute(name) { delete this.attributes[name]; }
```

Extend `querySelectorAll` to recognize `[role="option"]` and
`[data-relationship-row]`, and make the fetch stub return a separate
`deletedTaskResponse` for `/api/tasks?deleted=true`.

- [ ] **Step 2: Write a failing executable relationship-rendering test**

Construct:

- current task depends on one active task, one tombstoned task, and one missing
  full ID;
- one active task depends on current, so current Blocks it; and
- one tombstoned task depends on current.

Execute the rendered client on `/tasks/<current>`, then assert:

```js
const dependsHeading = findElement(main, (element) => element.textContent === "Depends On");
const blocksHeading = findElement(main, (element) => element.textContent === "Blocks");
if (!dependsHeading || !blocksHeading) throw new Error("both relationship groups did not render");

const activeDependencyLink = findElement(main, (element) =>
  element.tagName === "A" && element.href === "/tasks/" + encodeURIComponent(activeDependencyID));
if (!activeDependencyLink) throw new Error("active prerequisite was not linked");

const unavailable = findElement(main, (element) =>
  element.dataset.relationshipId === missingDependencyID);
if (!unavailable || !findElement(unavailable, (element) =>
    element.textContent === "Unavailable task")) {
  throw new Error("missing prerequisite fallback did not render");
}

const deletedBlock = findElement(main, (element) =>
  element.dataset.relationshipId === deletedBlockedID);
if (!deletedBlock || !deletedBlock.textContent.includes("Deleted") ||
    findElement(deletedBlock, (element) => element.tagName === "BUTTON" && element.textContent === "Remove")) {
  throw new Error("deleted blocked task was not rendered read-only");
}
```

Also capture `const title = findElement(...task-title)` and set
`title.value = "Unsaved title"` for the later mounted-form assertion.

- [ ] **Step 3: Run the executable test and verify the missing-section failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientRendersDependencyRelationships -count=1
```

Expected: FAIL because the detail page has no relationship groups.

- [ ] **Step 4: Implement deleted-task loading and relationship derivation**

Add client state:

```js
let latestDeletedTasks = [];
let deletedTasksLoaded = false;
let activeDependencyController = null;
```

Add strict deleted-document loading:

```js
async function refreshDeletedTasks() {
  const response = await fetch("/api/tasks?deleted=true", { cache: "no-store" });
  const document = await response.json();
  if (!response.ok || document.format !== "workbook.tasks" ||
      document.version !== 1 || !Array.isArray(document.tasks)) {
    throw new Error(document.error && document.error.message ||
      "Deleted task data could not be loaded.");
  }
  latestDeletedTasks = document.tasks;
  deletedTasksLoaded = true;
}
```

Implement pure relationship derivation:

```js
function dependencyState(taskID) {
  const current = latestTasks.find((task) => task.id === taskID);
  if (!current) return null;
  const known = new Map([...latestTasks, ...latestDeletedTasks].map((task) => [task.id, task]));
  const dependsOn = (current.dependencies || []).map((id) => ({
    id, task: known.get(id) || null, removable: true
  }));
  const blocks = [...latestTasks, ...latestDeletedTasks]
    .filter((task) => task.id !== taskID && (task.dependencies || []).includes(taskID))
    .map((task) => ({ id: task.id, task, removable: !task.deleted }));
  return { current, dependsOn, blocks };
}
```

- [ ] **Step 5: Mount a relationship controller without reconstructing the form**

Have `taskForm("detail", task)` append a relationship region after the existing
form and set `activeDependencyController` to an object with stable group,
status, row-list, and combobox elements plus an `update()` method.

Render each row with this behavior:

```js
function relationshipRow(relationship, onRemove) {
  const row = document.createElement("article");
  row.className = "relationship-row";
  row.dataset.relationshipRow = "";
  row.dataset.relationshipId = relationship.id;

  const heading = document.createElement("h4");
  if (relationship.task && !relationship.task.deleted) {
    const link = document.createElement("a");
    link.href = "/tasks/" + encodeURIComponent(relationship.id);
    text(link, relationship.task.title);
    heading.append(link);
  } else {
    text(heading, relationship.task ? relationship.task.title : "Unavailable task");
  }

  const metadata = document.createElement("p");
  metadata.className = "relationship-row__metadata";
  if (relationship.task) {
    text(metadata, relationship.task.status + " · " +
      relationship.task.priority + " · " + relationship.id);
  } else {
    text(metadata, relationship.id);
  }
  row.append(heading, metadata);

  if (relationship.task && relationship.task.deleted) {
    const deleted = document.createElement("strong");
    deleted.className = "relationship-state";
    text(deleted, "Deleted");
    row.append(deleted);
  }
  if (relationship.removable) {
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "relationship-remove";
    text(remove, "Remove");
    remove.addEventListener("click", () => onRemove(relationship));
    row.append(remove);
  } else {
    const explanation = document.createElement("p");
    explanation.className = "relationship-row__note";
    text(explanation, "Read-only because deleted tasks cannot be changed.");
    row.append(explanation);
  }
  return row;
}
```

`update()` calls `dependencyState(task.id)` and replaces only the row-list
children with `relationshipRow` results. Add these styles without changing the
existing form controls:

```css
.task-relationships { display: grid; gap: 1rem; padding: 0 1.15rem 1.15rem; }
.relationship-group { border-top: 1px solid #d5deea; padding-top: 1rem; }
.relationship-list { display: grid; gap: .55rem; margin: .75rem 0; }
.relationship-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: .35rem .75rem; border: 1px solid #d5deea; border-radius: 4px; padding: .7rem; }
.relationship-row h4, .relationship-row p { margin: 0; }
.relationship-row__metadata, .relationship-row__note { color: #56647a; font-size: .75rem; overflow-wrap: anywhere; }
.relationship-remove { align-self: center; grid-column: 2; grid-row: 1 / span 2; }
.relationship-state { color: #9c2f25; font-size: .75rem; }
```

On first mount, render active state immediately, then call
`refreshDeletedTasks().then(controller.update)` and show a relationship-local
error if deleted context fails. When `refresh()` succeeds, call
`activeDependencyController.update()` without calling `renderRoute`.
Return `true` from the successful `refresh()` path and `false` from its catch
path so relationship mutations can distinguish a durable write from a completed
canonical client refresh.

When leaving the detail route, clear `activeDependencyController`.

- [ ] **Step 6: Verify inverse derivation, deleted state, and mounted form**

Extend the executable test to replace the active task document, call
`await refresh()`, and assert:

```js
if (title.value !== "Unsaved title") {
  throw new Error("relationship refresh reconstructed the task form");
}
if (findElement(main, (element) =>
    element.dataset.relationshipId === removedDependencyID)) {
  throw new Error("relationship rows did not follow refreshed canonical state");
}
```

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'ClientRendersDependencyRelationships|InitialTaskLoad|Navigation' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit relationship rendering**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: render task relationships"
```

---

### Task 5: Add accessible two-direction combobox editing

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: reusable `dependencyCombobox(config)` controller.
- Consumes: `latestTasks` in canonical order.
- Produces: correctly oriented nested `PUT` and `DELETE` requests.
- Preserves: search query, selected option, and task-form inputs on failure.

- [ ] **Step 1: Write failing candidate-filtering and ARIA tests**

In an executable client test, locate each group input and assert:

```js
if (dependsInput.attributes.role !== "combobox" ||
    dependsInput.attributes["aria-autocomplete"] !== "list" ||
    !dependsInput.attributes["aria-controls"]) {
  throw new Error("Depends On input does not expose the combobox contract");
}
```

For Depends On, assert options exclude current, existing direct dependencies,
and deleted tasks. For Blocks, assert options exclude current, tasks already
blocked by current, and deleted tasks. Assert each remaining option includes its
title, status, priority, and full ID.

The production mutations these assertions catch are filtering from the wrong
edge direction and exposing a tombstoned candidate.

- [ ] **Step 2: Run the filtering test and verify the missing-combobox failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientFiltersDependencyComboboxCandidates -count=1
```

Expected: FAIL because no combobox controls exist.

- [ ] **Step 3: Implement stable candidate derivation**

Add:

```js
function relationshipCandidates(taskID, direction) {
  const current = latestTasks.find((task) => task.id === taskID);
  if (!current) return [];
  const excluded = new Set([taskID]);
  if (direction === "depends-on") {
    (current.dependencies || []).forEach((id) => excluded.add(id));
  } else {
    latestTasks.forEach((task) => {
      if ((task.dependencies || []).includes(taskID)) excluded.add(task.id);
    });
  }
  return latestTasks.filter((task) => !task.deleted && !excluded.has(task.id));
}
```

Do not sort this result again; `latestTasks` already carries canonical task
order.

- [ ] **Step 4: Implement the ARIA combobox/listbox controller**

Create native input, listbox, and button elements. The controller owns:

```js
const state = { query: "", selectedID: "", activeIndex: -1, candidates: [] };
```

Construct the accessible controls with:

```js
const input = document.createElement("input");
input.type = "search";
input.id = config.id + "-input";
input.setAttribute("role", "combobox");
input.setAttribute("aria-autocomplete", "list");
input.setAttribute("aria-controls", config.id + "-listbox");
input.setAttribute("aria-expanded", "false");

const listbox = document.createElement("div");
listbox.id = config.id + "-listbox";
listbox.setAttribute("role", "listbox");

const empty = document.createElement("p");
empty.setAttribute("role", "status");
empty.setAttribute("aria-live", "polite");

const add = document.createElement("button");
add.type = "button";
add.disabled = true;
text(add, "Add dependency");
```

Filter with:

```js
function matchingCandidates() {
  const query = state.query.trim().toLocaleLowerCase();
  if (!query) return state.candidates;
  return state.candidates.filter((task) =>
    task.title.toLocaleLowerCase().includes(query) ||
    task.id.toLocaleLowerCase().includes(query));
}
```

Render each real option with `role="option"`, a stable ID derived from the
combobox ID plus candidate index, `aria-selected`, and visible title, status,
priority, and full ID. Keep the Add button disabled until `selectedID` matches a
current candidate. `setCandidates` preserves the query and selected ID when the
selected candidate remains eligible; if it no longer exists, it clears only the
selection and leaves the query intact.

Handle:

```js
input.addEventListener("keydown", (event) => {
  const options = matchingCandidates();
  if (event.key === "ArrowDown") {
    event.preventDefault();
    state.activeIndex = Math.min(state.activeIndex + 1, options.length - 1);
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    state.activeIndex = Math.max(state.activeIndex - 1, 0);
  } else if (event.key === "Enter" && state.activeIndex >= 0) {
    event.preventDefault();
    selectCandidate(options[state.activeIndex]);
  } else if (event.key === "Escape") {
    state.activeIndex = -1;
    input.setAttribute("aria-expanded", "false");
  } else {
    return;
  }
  renderOptions();
});
```

Input and pointer events call the same `selectCandidate`. Empty matches render
an announced message outside the listbox, not a fake option.

- [ ] **Step 5: Write failing request-orientation and durable-refresh tests**

Drive both Add buttons and assert:

```js
const dependsAdd = fetchCalls.find((call) =>
  call.options.method === "PUT" &&
  call.url === "/api/tasks/" + encodeURIComponent(currentID) +
    "/dependencies/" + encodeURIComponent(prerequisiteID));
if (!dependsAdd || Object.prototype.hasOwnProperty.call(dependsAdd.options, "body")) {
  throw new Error("Depends On add did not send a bodyless nested PUT");
}

const blocksAdd = fetchCalls.find((call) =>
  call.options.method === "PUT" &&
  call.url === "/api/tasks/" + encodeURIComponent(blockedTaskID) +
    "/dependencies/" + encodeURIComponent(currentID));
if (!blocksAdd) throw new Error("Blocks add did not reverse the edge orientation");
```

Drive active Depends On and Blocks Remove buttons and assert the same paths with
`DELETE`. Have the fetch stub return an updated mutation document followed by
an updated `GET /api/tasks` document. Assert the old relationship remains until
the GET resolves, then disappears.

- [ ] **Step 6: Return the full mutation document and preserve bodyless requests**

Change `mutateTask` to:

```js
async function mutateTask(method, path, values) {
  const options = { method };
  if (values !== undefined) {
    options.headers = { "Content-Type": "application/json" };
    options.body = JSON.stringify(values);
  }
  const response = await fetch(path, options);
  const document = await response.json();
  if (!response.ok || document.format !== "workbook.task-mutation" ||
      document.version !== 1 || !document.task) {
    throw new Error(document.error && document.error.message || "Task update failed.");
  }
  return document;
}
```

Existing callers ignore the return value, so returning the full document is
backward compatible inside the client.

Implement relationship actions:

```js
async function mutateRelationship(method, dependentID, prerequisiteID, controller, group) {
  const path = "/api/tasks/" + encodeURIComponent(dependentID) +
    "/dependencies/" + encodeURIComponent(prerequisiteID);
  group.setBusy(true);
  group.setMessage("");
  try {
    const document = await mutateTask(method, path);
    const messages = [];
    const warnings = Array.isArray(document.warnings) ? document.warnings : [];
    if (warnings.length) {
      messages.push("Dependency saved durably. " +
        warnings.map((warning) => warning.message).join(" "));
    }
    const refreshed = await refresh();
    if (!refreshed) {
      messages.push("The dependency was saved durably, but the latest task state could not be refreshed.");
      group.setMessage(messages.join(" "));
      return;
    }
    try {
      await refreshDeletedTasks();
      controller.update();
    } catch (error) {
      messages.push(error instanceof Error ? error.message :
        "Deleted task data could not be refreshed.");
    }
    if (method === "PUT") group.combobox.clear();
    group.setMessage(messages.join(" "));
  } catch (error) {
    await refresh();
    group.setMessage(error instanceof Error ? error.message : "Dependency update failed.");
  } finally {
    group.setBusy(false);
  }
}
```

Depends On calls it with `(currentID, selectedID)`; Blocks calls it with
`(selectedID, currentID)`.

- [ ] **Step 7: Write and pass failure-recovery, warning, and keyboard tests**

The executable test must:

- type a query and select with Arrow Down plus Enter;
- return `workbook.error` version 1 with
  `dependency would create a cycle`;
- assert the error is visible in the initiating live region;
- assert query, selected candidate, and unsaved task title remain unchanged;
- return a successful mutation with a projection warning;
- assert the warning text remains visible after active/deleted refresh; and
- assert Escape closes the popup and clears `aria-activedescendant` without
  selecting a candidate.

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'DependencyCombobox|DependencyMutation|DependencyFailure' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
```

Expected: PASS with Node-backed client behavior executed.

- [ ] **Step 8: Commit editable dependency controls**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: edit web task relationships"
```

---

### Task 6: Document and verify the completed feature

**Files:**
- Modify: `README.md:304-349`
- Modify: `internal/cli/run_test.go:917-926`

**Interfaces:**
- Documents: dependency progress, two-direction detail editing, nested routes,
  deleted/unavailable behavior, and durable warning semantics.
- Preserves: implemented-command policy and local-first limitations.

- [ ] **Step 1: Write the failing README contract assertions**

Extend the existing web documentation test with these literals:

```go
for _, required := range []string{
	"PUT /api/tasks/<id>/dependencies/<dependency>",
	"DELETE /api/tasks/<id>/dependencies/<dependency>",
	"Depends On",
	"Blocks",
	"Waiting on dependencies",
	"deleted blocked tasks remain read-only",
} {
	if !strings.Contains(readme, required) {
		t.Errorf("README web dependency documentation is missing %q", required)
	}
}
```

- [ ] **Step 2: Run the README test and verify missing-documentation failure**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestREADME -count=1
```

Expected: FAIL on the new required web-dependency text.

- [ ] **Step 3: Update the local web board documentation**

Add these route lines:

```text
PUT /api/tasks/<id>/dependencies/<dependency>     add a prerequisite
DELETE /api/tasks/<id>/dependencies/<dependency>  remove a prerequisite
```

Add this behavior paragraph:

```markdown
Cards with prerequisites show completed versus total dependency progress.
Ready cards whose prerequisites are not all active and Done also say
`Waiting on dependencies`. Task detail pages derive two views from the same
directed edge: **Depends On** lists the current task's prerequisites, while
**Blocks** lists tasks that depend on the current task. Each group searches
eligible active tasks through an integrated combobox and uses the nested
Git-durable mutation routes above.

Missing prerequisite IDs remain visible and removable. Tombstoned
prerequisites are also removable because the active dependent owns that edge;
deleted blocked tasks remain read-only because tombstones cannot be changed.
Dependency warnings and failures stay beside the initiating group, and
dependency refreshes leave unsaved task-form fields mounted.
```

- [ ] **Step 4: Format and run focused verification**

Run:

```sh
gofmt -w internal/core/service.go internal/core/service_test.go internal/presentation/board.go internal/presentation/board_test.go internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go internal/cli/run_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core ./internal/presentation ./internal/webui ./internal/cli -count=1
```

Expected: PASS.

- [ ] **Step 5: Run complete fresh verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-wb-dependencies ./cmd/workbook
git diff --check
git status --short
```

Expected:

- every Go package passes;
- `go vet` exits 0 with no diagnostics;
- the production executable builds;
- `git diff --check` exits 0; and
- status lists only intended feature files before the documentation commit.

- [ ] **Step 6: Commit documentation**

```sh
git add README.md internal/cli/run_test.go
git commit -m "docs: describe web dependency management"
```

- [ ] **Step 7: Review the completed branch against the approved spec**

Compare `origin/main...HEAD` to
`docs/superpowers/specs/2026-07-29-web-dependency-management-design.md`.
Confirm every scope bullet and test category has a corresponding implementation
or test. Run the required code-review skill and resolve every Critical or
Important finding before publishing.
