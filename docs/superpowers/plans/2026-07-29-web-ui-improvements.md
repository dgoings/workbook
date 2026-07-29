# Responsive Web Board Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the local web board fill wide screens, scroll horizontally on narrow screens, keep dense columns independently scrollable, clamp descriptions to six lines, omit the unrecognized-status board section, and poll every second.

**Architecture:** Keep the change inside the existing embedded `internal/webui` page and its handler tests. CSS owns viewport sizing, track sizing, card clamping, and nested scrolling; the client renderer owns canonical-status filtering and the polling cadence. The HTTP API and task/detail recovery behavior remain unchanged.

**Tech Stack:** Go 1.26, Go `html/template` and `httptest`, embedded HTML/CSS/JavaScript, the existing Node-based executable client harness, and browser verification against the real local server.

## Global Constraints

- Keep the six canonical workflow columns in their existing order.
- Preserve complete task descriptions in API data and detail forms; clamp only board-card presentation.
- Keep unknown-status tasks available through the API and direct detail route while omitting them from the board.
- Preserve drag-and-drop, routing, mutation, stale-data, and task-ordering behavior.
- Use CSS and existing browser APIs only; add no dependency or build step.
- Poll `/api/tasks` every 1,000 milliseconds after the existing immediate startup refresh.
- Work in the existing `codex/web-ui-improvements` checkout, not a worktree.
- Follow TDD: each behavior test must fail for the intended production defect before implementation.

---

### Task 1: Render only canonical statuses and poll every second

**Files:**
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/webui/assets/index.html`

**Interfaces:**
- Consumes: `core.WorkflowStatuses()`, the existing `TasksDocument` API shape, and `clientDOMHarness(path, taskDocument string)`.
- Produces: a six-list board that ignores noncanonical tasks without breaking refresh, plus `window.setInterval(refresh, 1000)`.

- [ ] **Step 1: Write the failing server-rendered board assertion**

In `TestHandlerServesBoardTasksAndHealth`, keep the canonical status and task
assertions, then explicitly reject the unknown board list and its fixture task:

```go
for _, fragment := range []string{
	`data-status="backlog"`,
	`data-status="ready"`,
	`data-status="in-progress"`,
	`data-status="blocked"`,
	`data-status="done"`,
	"Ready task",
	"Task refresh failed",
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
```

In `TestHandlerRendersTaskAndNewTaskLinks`, limit server-rendered task-link
expectations to the two canonical fixture tasks:

```go
for _, task := range tasks[:2] {
	want := `href="/tasks/` + task.ID + `">` + task.Title + `</a>`
	if !strings.Contains(body, want) {
		t.Errorf("GET / body does not contain full-ID task link %q", want)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandlerServesBoardTasksAndHealth|TestHandlerRendersTaskAndNewTaskLinks' -count=1
```

Expected: FAIL because the server-rendered page still contains the
`data-status="unknown"` section and `Future status task`.

- [ ] **Step 3: Remove the server-rendered unknown-status section**

Delete this section from `internal/webui/assets/index.html`:

```html
<section class="unknown" aria-label="Unrecognized task statuses">
  <header class="column__header"><h2 class="column__heading">Unrecognized status <span class="count" data-count="unknown">{{ len .Board.UnknownTasks }}</span></h2><code class="ref-label">refs/workbook/status/unrecognized</code></header>
  <div class="task-list" data-status="unknown">{{ range .Board.UnknownTasks }}{{ template "task" . }}{{ end }}</div>
</section>
```

Remove the now-unused `.unknown` and `.unknown .column__header` CSS rules.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run the command from Step 2 again.

Expected: PASS. `GET /api/tasks` must still return all three fixture tasks,
including the unknown-status task.

- [ ] **Step 5: Write failing executable-client coverage**

Change `clientDOMHarness` so its board fixtures expose only canonical lists and
capture the polling interval:

```js
const boardStatuses = ["backlog", "ready", "blocked", "in-progress", "in-review", "done"];
const boardLists = boardStatuses.map((status) => {
  const element = new TestElement("div");
  element.dataset.status = status;
  element.dataset.dropStatus = status;
  return element;
});
let intervalDelay = null;
globalThis.window = {
  location: { href: initialURL.href, origin: initialURL.origin },
  addEventListener() {},
  setInterval(_callback, delay) { intervalDelay = delay; }
};
```

Add `TestHandlerClientBoardIgnoresUnknownStatuses`. Render a document containing
the canonical ready task and the unknown-status task, execute the embedded
client, and assert after the initial promise turn that the ready card rendered
without a stale error:

```go
func TestHandlerClientBoardIgnoresUnknownStatuses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	tasks := boardTasks()
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))
	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	presentation := presentationForTasks(tasks)
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentation})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(() => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const card = findElement(ready, (element) => element.dataset.taskId === "` + tasks[0].ID + `");
  if (!card) throw new Error("canonical task did not render when an unknown-status task was present");
  if (stale.dataset.visible !== "false") throw new Error("unknown-status task triggered the stale state");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rendered canonical board filtering: %v\n%s", err, output)
	}
}
```

Add a small test helper beside `boardTasks`:

```go
func presentationForTasks(tasks []core.Task) []TaskPresentation {
	presentation := make([]TaskPresentation, len(tasks))
	for i, task := range tasks {
		presentation[i] = TaskPresentation{TaskID: task.ID, IDPrefix: task.ID}
	}
	return presentation
}
```

Add `TestHandlerClientPollsEverySecond` using one canonical task:

```go
func TestHandlerClientPollsEverySecond(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	tasks := boardTasks()[:1]
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return tasks, nil }, unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))
	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
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
```

- [ ] **Step 6: Run the executable-client tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandlerClientBoardIgnoresUnknownStatuses|TestHandlerClientPollsEverySecond' -count=1
```

Expected: FAIL. The current renderer routes unknown statuses into a missing
`unknown` list, and the current polling delay is 2,000 milliseconds.

- [ ] **Step 7: Filter unknown statuses and change the polling delay**

Replace the client grouping logic with:

```js
function render(tasks, presentation) {
  const grouped = new Map([...lists.keys()].map((status) => [status, []]));
  tasks.forEach((task) => {
    const items = grouped.get(task.status);
    if (items) items.push(task);
  });
  grouped.forEach((items, status) => {
    const list = lists.get(status); list.classList.add("is-refreshing");
    const fragment = document.createDocumentFragment(); items.forEach((task) => fragment.append(card(task, presentation.get(task.id))));
    list.replaceChildren(fragment);
    requestAnimationFrame(() => requestAnimationFrame(() => list.classList.remove("is-refreshing")));
    text(counts.get(status), String(items.length));
  });
}
```

Change the scheduler to:

```js
window.setInterval(refresh, 1000);
```

- [ ] **Step 8: Verify Task 1**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
```

Expected: PASS with no skipped executable-client tests when Node is available.

- [ ] **Step 9: Commit Task 1**

```sh
git add internal/webui/handler_test.go internal/webui/assets/index.html
git commit -m "feat: simplify web board refresh"
```

---

### Task 2: Add viewport-aware board sizing and card clamping

**Files:**
- Modify: `internal/webui/assets/index.html`

**Interfaces:**
- Consumes: the existing `.app-header`, `main`, `.board-view`, `.board`, `.column`, `.task-list`, and `.task-card p` elements.
- Produces: a viewport-height application shell, full-width responsive board tracks, horizontal narrow-screen scrolling, independent column-list scrolling, and six-line description excerpts.

- [ ] **Step 1: Establish the failing browser baseline**

Build and start the current feature branch before changing layout CSS:

```sh
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-web-ui-red ./cmd/workbook
/private/tmp/workbook-web-ui-red serve --addr 127.0.0.1:7342
```

At a narrow 390-by-844 viewport, inspect the real board and record that the
current 900-pixel media rule stacks the columns, so:

```js
document.querySelector(".board").scrollWidth === document.querySelector(".board").clientWidth
```

is `true`, contrary to the required horizontal-scrolling board.

At a viewport with a long card description, record that:

```js
getComputedStyle(document.querySelector(".task-card p")).webkitLineClamp
```

is not `"6"`. In a densely populated column, record that the page rather than
the column list owns the vertical overflow. These observations are the RED
layout baseline.

- [ ] **Step 2: Implement the viewport-height shell and responsive columns**

Update the page CSS with these layout rules while retaining the existing
palette, typography, focus states, and form styling:

```css
html, body { height: 100%; }
body {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
  margin: 0;
  min-width: 20rem;
  background: #e9eef5;
}
.app-header {
  display: flex;
  flex: 0 0 auto;
  align-items: end;
  justify-content: space-between;
  gap: 1.25rem;
  padding: 1.25rem;
  border-bottom: 1px solid #b9c6d8;
  background: #fff;
}
main {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
  padding: 1.25rem;
}
.board-view {
  display: grid;
  grid-template-rows: auto minmax(0, 1fr);
  height: 100%;
  min-height: 0;
}
.board {
  display: grid;
  grid-template-columns: repeat(6, minmax(12rem, 1fr));
  gap: .8rem;
  min-height: 0;
  overflow-x: auto;
  overflow-y: hidden;
  padding-bottom: .25rem;
}
.column {
  display: grid;
  grid-template-rows: auto minmax(0, 1fr);
  min-width: 12rem;
  min-height: 0;
  border: 1px solid #b9c6d8;
  background: rgba(255,255,255,.36);
}
.task-list {
  display: grid;
  align-content: start;
  gap: .55rem;
  min-height: 0;
  overflow-y: auto;
  padding: .55rem;
}
```

Replace the narrow breakpoint's stacked-board rules with:

```css
@media (max-width: 900px) {
  .app-header { align-items: start; flex-direction: column; }
  .updated { text-align: left; }
  .board { grid-template-columns: repeat(6, minmax(min(18rem, calc(100vw - 2.5rem)), 1fr)); }
  .column { min-width: min(18rem, calc(100vw - 2.5rem)); }
}
```

- [ ] **Step 3: Clamp board descriptions to six lines**

Extend `.task-card p` without changing the full description value:

```css
.task-card p {
  display: -webkit-box;
  max-height: calc(1.36em * 6);
  overflow: hidden;
  margin: 0;
  color: #56647a;
  font-size: .8rem;
  line-height: 1.36;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 6;
}
```

- [ ] **Step 4: Verify the browser behavior is GREEN**

Build the changed branch to `/private/tmp/workbook-web-ui-green`, serve it on
`127.0.0.1:7343`, and inspect the same task data.

At 390-by-844, confirm:

```js
const board = document.querySelector(".board");
board.scrollWidth > board.clientWidth;
```

At 1600-by-900, confirm all six columns share the available main width without
the former 92-rem outer gutter cap. Confirm:

```js
getComputedStyle(document.querySelector(".task-card p")).webkitLineClamp === "6"
```

For a dense column, confirm its `.task-list` has
`scrollHeight > clientHeight`, `overflowY === "auto"`, and the document itself
does not grow when that column grows.

- [ ] **Step 5: Run focused regression verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: both commands exit 0.

- [ ] **Step 6: Commit Task 2**

```sh
git add internal/webui/assets/index.html
git commit -m "feat: make web board viewport responsive"
```

---

### Task 3: Document and verify the completed web-board behavior

**Files:**
- Modify: `README.md`
- Include: `docs/superpowers/plans/2026-07-29-web-ui-improvements.md`

**Interfaces:**
- Consumes: the completed canonical-only, one-second, responsive board behavior.
- Produces: current user-facing documentation and fresh whole-branch verification evidence.

- [ ] **Step 1: Update the local web board documentation**

Replace the README's two-second polling statement with one-second polling.
Describe that wide screens share the available width across all six columns,
narrow screens scroll the board horizontally, dense columns scroll vertically,
and card descriptions show at most six lines while detail routes retain the
full text. Do not claim that API task descriptions are truncated.

- [ ] **Step 2: Verify documentation consistency**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestREADME' -count=1
rg -n 'every two seconds|Unrecognized status' README.md internal/webui/assets/index.html
```

Expected: README tests pass and the search returns no stale implemented-board
language or unrecognized-status board markup.

- [ ] **Step 3: Run fresh whole-branch verification**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false ./cmd/workbook
git diff --check
git status --short
```

Expected: tests, vet, build, and diff checks exit 0. Status lists only the
intended README, plan, test, and embedded-page changes not already committed.

- [ ] **Step 4: Commit the documentation**

```sh
git add README.md docs/superpowers/plans/2026-07-29-web-ui-improvements.md
git commit -m "docs: explain responsive web board"
```

- [ ] **Step 5: Review the final branch against the approved spec**

Compare `git diff main...HEAD` with
`docs/superpowers/specs/2026-07-29-web-ui-improvements-design.md`. Confirm every
scope item has implementation or verification evidence, no API/storage behavior
changed, and no unrelated file entered the branch.

- [ ] **Step 6: Publish for review**

After a clean final verification, push `codex/web-ui-improvements`, open a
pull request against `main`, then run:

```sh
workbook update WB-01KYN6FSYH6E2XSJM1BEXF8XRN --status in-review --json
workbook push --json
```

The task moves to `in-review` only after GitHub reports that the pull request
was created successfully.
