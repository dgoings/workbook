# Footer Action Placement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore Save/Create and Back to the left side of the shared task footer while keeping Delete separate at the far right.

**Architecture:** Keep the existing full-width `task-actions` footer and make its feedback and control bar separate rows. Within the control bar, add a primary action group for Save/Create and Back; retain the existing danger group for Delete and use flex spacing to separate the groups.

**Tech Stack:** Embedded HTML/CSS/JavaScript client, Go executable-client tests, Node DOM harness

## Global Constraints

- Apply the same footer structure to New Task and existing task detail.
- Feedback occupies a full-width row above the controls.
- Save/Create and Back are left-aligned in one primary action group.
- Delete is right-aligned in its existing danger group and remains absent on New Task.
- Preserve button types, navigation behavior, form submission, deletion behavior, and accessible live-region semantics.
- Do not add assertions for README text.

---

### Task 1: Separate Primary and Destructive Footer Actions

**Files:**
- Modify: `internal/webui/handler_test.go:781-852`
- Modify: `internal/webui/assets/index.html:84-95`
- Modify: `internal/webui/assets/index.html:1003-1035`

**Interfaces:**
- Consumes: `taskForm(mode, task)`, the existing `task-actions`, `form-actions`, and `form-danger` hooks
- Produces: a `form-primary-actions` container containing Save/Create and Back, plus a full-width `form-actions` control bar that keeps `form-danger` separate

- [ ] **Step 1: Extend the executable shared-layout test**

In `TestHandlerClientUsesSharedTaskSidebarLayout`, assert that:

```javascript
const footer = findElement(layout, (element) =>
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
```

For New Task, require Save and Back inside `primaryActions` and no danger group. For detail, require Save and Back inside `primaryActions`, require Delete inside `danger`, and require `primaryActions.parentElement === danger.parentElement` while neither group contains the other.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
GOCACHE=/private/tmp/workbook-footer-actions-gocache go test ./internal/webui -run TestHandlerClientUsesSharedTaskSidebarLayout -count=1
```

Expected: FAIL because `form-primary-actions` does not exist and the current action container places Save, Back, and Delete in one group.

- [ ] **Step 3: Implement the minimal footer grouping**

In `taskForm`, create a primary group and append it before the optional danger group:

```javascript
const actions = document.createElement("div"); actions.className = "form-actions";
const primaryActions = document.createElement("div"); primaryActions.className = "form-primary-actions";
const save = document.createElement("button"); save.className = "save-button"; save.type = "submit"; text(save, "Save");
const back = document.createElement("a"); back.className = "board-link"; back.href = "/"; text(back, "Back");
primaryActions.append(save, back);
actions.append(primaryActions);
```

Keep Delete in `form-danger`. Update CSS so feedback and the action bar occupy separate footer rows, the action bar spans the footer width, and its two child groups separate:

```css
.task-actions { display: grid; grid-column: 1 / -1; gap: .75rem; padding: .85rem 1.15rem 1.15rem; border-top: 1px solid #d5deea; }
.form-actions { display: flex; align-items: center; justify-content: space-between; gap: .85rem; }
.form-primary-actions { display: flex; align-items: center; gap: .85rem; }
.form-danger { margin-left: auto; }
```

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
GOCACHE=/private/tmp/workbook-footer-actions-gocache go test ./internal/webui -run 'TestHandlerClientUsesSharedTaskSidebarLayout|TestHandlerClientSidebarAccessibilityAndMobileOrder|TestHandlerInterceptsOrdinarySameOriginNavigation' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run repository verification**

Run:

```bash
GOCACHE=/private/tmp/workbook-footer-actions-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-footer-actions-gocache go vet ./...
GOCACHE=/private/tmp/workbook-footer-actions-gocache go build -buildvcs=false -o /private/tmp/workbook-footer-actions ./cmd/workbook
git diff --check origin/main...HEAD
```

Expected: every command exits 0.

- [ ] **Step 6: Commit and publish**

```bash
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "fix: separate task footer actions"
git push origin codex/wb-dependency-web-ui
```

Confirm PR #11 points to the new head and remains mergeable against `main`.
