# Copyable Web Task ID Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users copy a task's full ID by activating its text-like ID control on the web board or task-detail route without breaking card dragging.

**Architecture:** Keep the feature entirely inside the embedded web client. Server-rendered and refreshed board cards use the same semantic copy-button contract, a shared JavaScript helper writes full IDs through the Clipboard API, and view-local live regions announce success or failure. The existing document-level drag lifecycle temporarily suppresses copy activation caused by a completed card drag.

**Tech Stack:** Go 1.26, Go `html/template` and `httptest`, embedded HTML/CSS/vanilla JavaScript, native Clipboard and HTML drag-and-drop APIs, Node-based executable client tests.

## Global Constraints

- Work only in `/Users/dylan.goings/source/workbook/.worktrees/copy-task-id` on `codex/copy-task-id`.
- Keep task storage, core services, HTTP routes, terminal output, and deleted-task cards unchanged.
- Display the server-provided actionable prefix on board cards but always copy the full task ID.
- Use native `button` semantics without conventional button chrome: no border, fill, padding, or underline.
- Hover changes only the text color to Workbook's existing interface blue; `focus-visible` retains an explicit outline.
- A drag beginning anywhere on a board card, including its ID, must retain card placement behavior and must not copy the ID.
- Success and failure feedback must be visible and announced through a polite `role="status"` live region.
- Clipboard failure feedback must include the full ID as selectable text for manual copying.
- Write behavior-resistant tests before production changes and observe the focused test fail for the missing feature.

---

### Task 1: Add copy controls, feedback, and drag separation

**Files:**
- Modify: `internal/webui/handler_test.go:246-267`
- Modify: `internal/webui/handler_test.go:364-436`
- Modify: `internal/webui/handler_test.go:751-911`
- Modify: `internal/webui/handler_test.go:1224-1352`
- Modify: `internal/webui/assets/index.html:1-8`
- Modify: `internal/webui/assets/index.html:23-89`
- Modify: `internal/webui/assets/index.html:99-105`
- Modify: `internal/webui/assets/index.html:123-150`
- Modify: `internal/webui/assets/index.html:180-188`
- Modify: `internal/webui/assets/index.html:379-438`

**Interfaces:**
- Consumes: `TaskPresentation.IDPrefix`, `core.Task.ID`, the existing `card`, `taskForm`, `text`, `activeDrag`, and delegated document event listeners.
- Produces: `taskIDCopyControl(taskID, visibleID) HTMLButtonElement`.
- Produces: `copyTaskID(taskID, statusElement) Promise<void>`.
- Produces: `showCopyStatus(statusElement, message, kind)`.
- Produces: `[data-copy-task-id]` semantic controls and `[data-copy-status]` polite live regions.
- Preserves: `PATCH /api/tasks/{id}/position` drag placement, same-origin link navigation, and actionable board prefixes.

- [x] **Step 1: Extend the executable DOM harness for clipboard controls and timers**

In `clientDOMHarness`, add selector support needed by the production interaction:

```js
closest(selector) {
  for (let element = this; element; element = element.parentElement) {
    if (selector === "a[href]" && element.tagName === "A" && element.href) return element;
    if (selector === ".task-card" && element.className === "task-card") return element;
    if (selector === ".task-route" && element.className === "task-route") return element;
    if (selector === "[data-copy-task-id]" &&
        Object.prototype.hasOwnProperty.call(element.dataset, "copyTaskId")) return element;
    if (selector === "[data-drop-status]" && element.dataset.dropStatus) return element;
    if (selector === "[data-status]" && element.dataset.status) return element;
  }
  return null;
}
```

Make `querySelector("[data-copy-status]")` search descendants with
`findElement`. Add a board status fixture returned from
`boardView.querySelector("[data-copy-status]")`:

```js
const copyStatus = new TestElement("p");
copyStatus.dataset.copyStatus = "";

boardView.querySelector = (selector) => {
  if (selector === "[data-stale]") return stale;
  if (selector === "[data-copy-status]") return copyStatus;
  return null;
};
```

Add deterministic clipboard and window-timer fixtures:

```js
const clipboardWrites = [];
let clipboardError = null;
globalThis.navigator = {
  clipboard: {
    async writeText(value) {
      if (clipboardError) throw clipboardError;
      clipboardWrites.push(value);
    }
  }
};

const windowTimeouts = [];
let nextWindowTimeoutID = 1;
window.setTimeout = (callback, delay) => {
  const timer = { id: nextWindowTimeoutID++, callback, delay, canceled: false };
  windowTimeouts.push(timer);
  return timer.id;
};
window.clearTimeout = (id) => {
  const timer = windowTimeouts.find((candidate) => candidate.id === id);
  if (timer) timer.canceled = true;
};
```

Keep Node's global `setTimeout` unchanged because existing tests use it to wait
for the client's initial asynchronous refresh.

- [x] **Step 2: Write the failing server-rendered copy-control test**

Add `TestHandlerRendersTextLikeCopyableTaskIDControls`. Render `/`, use
`initialCardPrefixes` to retain the initial-prefix contract, and assert that the
first active task produces these exact observable fragments:

```go
for _, fragment := range []string{
    `type="button"`,
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
```

Also assert source-level visual constraints that are meaningful for this
embedded artifact:

```go
for _, fragment := range []string{
    `.task-id-copy {`,
    `border: 0`,
    `background: transparent`,
    `cursor: pointer`,
    `padding: 0`,
    `.task-id-copy:hover { color: #2457d6; }`,
    `.task-id-copy:focus-visible`,
} {
    if !strings.Contains(body, fragment) {
        t.Errorf("copy control styling does not contain %q", fragment)
    }
}
```

- [x] **Step 3: Write the failing executable interaction test**

Add `TestHandlerClientCopiesFullTaskIDsAndSeparatesDrag`. Render one Ready task,
build a `TasksDocument` with its actionable presentation prefix, and execute the
client using `clientDOMHarness`.

After initial refresh, find the dynamic board control by
`element.dataset.copyTaskId === task.ID`. Assert it is a `BUTTON`, its nested
`CODE` contains the prefix, and its accessible label contains the full ID.
Invoke the delegated click listener and prove the full ID—not the prefix—was
written:

```js
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
if (copyStatus.attributes.role !== "status" ||
    copyStatus.attributes["aria-live"] !== "polite" ||
    copyStatus.textContent !== "Copied task ID " + taskID + ".") {
  throw new Error("board copy did not render accessible success feedback");
}
```

Prove drag separation by clearing `clipboardWrites`, firing `dragstart` and
`dragend` from the copy control, then immediately invoking click. The write
array must remain empty. The suppressed click clears the one-shot guard; click
again as a later intentional activation and require one full-ID write.

Navigate to the task detail route through the existing task-title link, find its
full-ID copy control, and require another full-ID write plus a view-local
`[data-copy-status]` success message. Then set
`clipboardError = new Error("denied")`, click again, and require feedback that
contains both `"Could not copy task ID"` and the full task ID while the write
count remains unchanged.

- [x] **Step 4: Run the focused tests and verify the intended RED state**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui \
  -run 'TestHandler(RendersTextLikeCopyableTaskIDControls|ClientCopiesFullTaskIDsAndSeparatesDrag)' \
  -count=1
```

Expected: FAIL because the rendered IDs are still plain `code` elements, the
client does not register copy controls, and no Clipboard API interaction or
copy-status region exists. Confirm the failure is about those missing
behaviors, not a harness syntax error.

- [x] **Step 5: Render a text-like semantic copy control on initial and refreshed cards**

Replace the initial task-card ID with:

```html
<button type="button"
        class="task-id-copy"
        data-copy-task-id="{{ .Task.ID }}"
        aria-label="Copy full task ID {{ .Task.ID }}"><code>{{ .IDPrefix }}</code></button>
```

Add a shared client renderer and use it from `card`:

```js
function taskIDCopyControl(taskID, visibleID) {
  const control = document.createElement("button");
  control.type = "button";
  control.className = "task-id-copy";
  control.dataset.copyTaskId = taskID;
  control.setAttribute("aria-label", "Copy full task ID " + taskID);
  const id = document.createElement("code");
  text(id, visibleID);
  control.append(id);
  return control;
}
```

`card(task, idPrefix)` appends
`taskIDCopyControl(task.id, idPrefix)` before the unchanged priority badge.
Update `TestHandlerProvidesActionablePrefixesForRefresh` to look for
`taskIDCopyControl(task.id, idPrefix)` instead of the old direct code rendering.

Update `initialCardPrefixes` so its regular expression captures the article's
task ID and prefix, the button's full task ID, and the nested code's visible
prefix. Add an entry only when both ID values match and both prefix values
match.

- [x] **Step 6: Add text-like styles and view-local live regions**

Add these style contracts:

```css
.task-id-copy {
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
  padding: 0;
}
.task-id-copy:hover { color: #2457d6; }
.task-id-copy:focus-visible { outline: 3px solid #2457d6; outline-offset: 2px; }
.copy-status {
  margin: 0 0 .75rem;
  padding: .45rem .65rem;
  border-left: 3px solid #2457d6;
  background: #eef4ff;
  color: #173f9e;
  font-size: .78rem;
}
.copy-status:empty { display: none; }
.copy-status[data-kind="error"] {
  border-left-color: #b42318;
  background: #fff1f0;
  color: #8f1d1d;
}
```

Change `.board-view` to three rows—`auto auto minmax(0, 1fr)`—and insert this
between the existing refresh status and the board:

```html
<p class="copy-status" data-copy-status role="status" aria-live="polite"></p>
```

In detail mode, append `taskIDCopyControl(task.id, task.id)` and another
copy-status paragraph to the task-route header. Do not add either control to the
new-task route.

- [x] **Step 7: Implement clipboard feedback and drag suppression**

Capture the board live region with:

```js
const boardCopyStatus = boardView.querySelector("[data-copy-status]");
let copyFeedbackTimer = null;
let copySuppressionTimer = null;
let copySuppressedTaskID = null;
```

Add:

```js
function showCopyStatus(status, message, kind) {
  if (copyFeedbackTimer !== null) window.clearTimeout(copyFeedbackTimer);
  status.dataset.kind = kind;
  text(status, message);
  copyFeedbackTimer = window.setTimeout(() => {
    text(status, "");
    delete status.dataset.kind;
    copyFeedbackTimer = null;
  }, 4000);
}

async function copyTaskID(taskID, status) {
  try {
    if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
      throw new Error("clipboard unavailable");
    }
    await navigator.clipboard.writeText(taskID);
    showCopyStatus(status, "Copied task ID " + taskID + ".", "success");
  } catch (_) {
    showCopyStatus(
      status,
      "Could not copy task ID " + taskID + ". Select this ID and copy it manually.",
      "error"
    );
  }
}
```

Make the delegated click listener `async`. Before anchor recognition, recognize
the closest `[data-copy-task-id]` and prevent default. If its ID matches
`copySuppressedTaskID`, clear that one-shot guard and its timer, then return
without copying. Otherwise choose the nearest task-route copy-status region or
`boardCopyStatus`, await `copyTaskID`, and return.

At drag start, set `copySuppressedTaskID = item.dataset.taskId`. At drag end,
retain the existing cleanup and schedule a 250 ms fallback that clears only the
same suppressed ID. This catches a browser-generated click immediately after
the drag while ensuring the guard expires when the browser emits no click:

```js
const suppressedTaskID = copySuppressedTaskID;
if (copySuppressionTimer !== null) window.clearTimeout(copySuppressionTimer);
copySuppressionTimer = window.setTimeout(() => {
  if (copySuppressedTaskID === suppressedTaskID) copySuppressedTaskID = null;
  copySuppressionTimer = null;
}, 250);
```

- [x] **Step 8: Run focused and package tests to verify GREEN**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui \
  -run 'TestHandler(RendersTextLikeCopyableTaskIDControls|ClientCopiesFullTaskIDsAndSeparatesDrag|InterceptsOrdinarySameOriginNavigation|ClientSendsAtomicClampedPlacementRequests|ProvidesActionablePrefixesForRefresh|InitialCardPrefixesMatchRefreshPresentation)' \
  -count=1
```

Expected: PASS.

Then run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: all commands exit 0 and `gofmt` leaves the Go test syntactically
clean.

- [x] **Step 9: Commit the tested client behavior**

Inspect `git diff` and stage only:

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: copy web task IDs"
```

---

### Task 2: Document and verify the completed behavior

**Files:**
- Modify: `README.md:329-341`
- Modify: `docs/superpowers/plans/2026-07-29-copy-task-id.md`

**Interfaces:**
- Consumes: the tested board and detail copy interaction from Task 1.
- Produces: user-facing local-web documentation and final repository evidence.

- [x] **Step 1: Document task-ID copying in the local web board section**

After the paragraph describing card contents and detail links, add:

```markdown
Click a card's shortened task ID to copy its full ID. The ID remains part of the
card's drag target, so dragging moves the task while a click copies. The task
detail route provides the same action on its full ID. Successful copies show an
accessible confirmation; clipboard failures keep the full ID visible for manual
copying.
```

- [x] **Step 2: Mark completed plan checkboxes**

Change each executed plan checkbox from `- [ ]` to `- [x]` only after its
command or behavior has been observed. Leave any unexecuted item unchecked and
report it rather than claiming completion.

- [x] **Step 3: Run final verification**

Run fresh commands from the feature worktree:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Expected: tests and vet exit 0, `git diff --check` emits no errors, and status
shows only the intended README and plan updates after Task 1's commit.

- [x] **Step 4: Commit documentation and the execution record**

Inspect the diff, then run:

```sh
git add README.md docs/superpowers/plans/2026-07-29-copy-task-id.md
git commit -m "docs: describe task ID copying"
```

- [x] **Step 5: Verify the branch before publication**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
git log --oneline origin/main..HEAD
```

Expected: all checks exit 0, the worktree is clean, and the log contains the
design, implementation, and documentation commits only.

- [x] **Step 6: Publish for review**

After completing the verification-before-completion gate:

```sh
git push -u origin codex/copy-task-id
```

Open a draft pull request against `main` with a body covering the interaction,
accessibility feedback, drag/click separation, and exact verification commands.
Only after the PR exists, update
`WB-01KYQXDDQXSE8WRW7XFKXZQ2DP` to `in-review` and publish the Workbook task
ref with `workbook push --json`.
