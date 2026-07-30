# Task Detail Sidebar Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the vertically stacked task relationship editor with a shared desktop properties sidebar for New Task and existing task detail, including staged Depends On and Blocks relationships during task creation.

**Architecture:** Merge current `main` first so the implementation preserves the landed flexible-Description behavior. Refactor the embedded client into explicit editor, sidebar, and footer regions; keep existing-task relationship mutations immediate, while New Task stores both directions in client-side draft maps and persists them after the task ID exists. Preserve the existing nested mutation routes, append-only data model, stable-form refresh behavior, and explicit partial-success recovery.

**Tech Stack:** Go 1.26, embedded HTML/CSS/vanilla JavaScript, existing Node-based executable client harness, local loopback HTTP server, Git-backed Workbook task refs, and real-browser layout verification.

## Global Constraints

- Work only in `/Users/dylan.goings/source/workbook/.worktrees/wb-dependency-web-ui` on `codex/wb-dependency-web-ui`.
- Fetch and merge current `origin/main` before feature edits; preserve both the landed Description-height behavior and PR #11 dependency behavior.
- Keep one canonical durable relationship field: `TaskData.Dependencies`.
- Keep Blocks derived; do not add a durable `blocks` field.
- Keep bodyless nested `PUT`/`DELETE` dependency routes and their strict raw-path contract.
- Keep tombstoned Depends On rows removable and tombstoned Blocks rows read-only.
- Keep existing-task relationship mutations immediately Git-durable.
- New Task stages both Depends On and Blocks locally until task creation returns an ID.
- Do not imply atomicity across multiple task refs; expose partial success explicitly.
- Preserve task fields, relationship drafts, combobox queries, and valid selections through recoverable failures.
- Preserve the stable form/controller ownership and refresh-generation guards.
- Keep board dependency progress and Ready eligibility unchanged.
- Add no runtime dependency, framework, daemon, or build step.
- Follow strict TDD and capture the focused RED result before production changes in every task.

---

### Task 1: Merge Current Main and Build the Shared Layout Shell

**Files:**
- Modify: `internal/webui/assets/index.html:65-145`
- Modify: `internal/webui/assets/index.html:640-720`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Consumes: landed `.field--description` and flexible Description CSS from current `main`.
- Produces: `.task-layout`, `.task-editor`, `.task-sidebar`, `.task-properties`, `.task-actions`, and `[data-relationship-mount]`.
- Preserves: `taskForm(mode, task)`, the single semantic `<form>`, field control references, copy-ID controls, delete behavior, and submit-value construction.

- [ ] **Step 1: Merge current `origin/main` before editing**

Run:

```sh
git status --short
git fetch origin main
git merge --no-edit origin/main
```

Expected:

- the pre-merge status is clean;
- current `main` includes `WB-01KYQZ64833PGMFACTMSW4HEM1`;
- any conflicts are limited to files changed by both the Description and
  dependency work; and
- the resolution keeps `.field--description`, copy-ID behavior, dependency
  presentation, and the strict dependency route tests.

Run after resolving any conflict:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'Description|Copy|Dependency|Restore' -count=1
git diff --check
```

Expected: PASS before new feature edits.

- [ ] **Step 2: Write the failing shared-layout executable test**

Add `TestHandlerClientUsesSharedTaskSidebarLayout` to
`internal/webui/handler_test.go`. Render `/tasks/new` and an existing detail
route with `clientDOMHarness`, then require these regions:

```js
function assertSharedLayout(expectedMode) {
  const layout = findElement(main, (element) =>
    element.className.split(/\s+/).includes("task-layout"));
  const editor = findElement(layout, (element) =>
    element.className.split(/\s+/).includes("task-editor"));
  const sidebar = findElement(layout, (element) =>
    element.className.split(/\s+/).includes("task-sidebar"));
  const properties = findElement(sidebar, (element) =>
    element.className.split(/\s+/).includes("task-properties"));
  const actions = findElement(layout, (element) =>
    element.className.split(/\s+/).includes("task-actions"));
  if (!layout || !editor || !sidebar || !properties || !actions) {
    throw new Error(expectedMode + " does not use the shared task layout");
  }
  for (const id of ["task-status", "task-priority", "task-labels"]) {
    const control = findElement(properties, (element) => element.id === id);
    if (!control) throw new Error(id + " is not in Properties");
  }
  const description = findElement(editor, (element) =>
    element.id === "task-description");
  if (!description ||
      !description.parentElement.className.split(/\s+/).includes("field--description")) {
    throw new Error("Description lost its flexible editor hook");
  }
}
```

The test must navigate from New Task to existing detail in the same client
program and call `assertSharedLayout("new")` and
`assertSharedLayout("detail")`.

- [ ] **Step 3: Run the shared-layout test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientUsesSharedTaskSidebarLayout -count=1
```

Expected: FAIL because `.task-layout`, `.task-editor`, `.task-sidebar`,
`.task-properties`, and `.task-actions` do not exist.

- [ ] **Step 4: Refactor `taskForm` into explicit regions**

In `internal/webui/assets/index.html`, keep the existing controls and handlers,
but construct this form shape:

```js
const form = document.createElement("form");
form.className = "task-layout";

const editor = document.createElement("div");
editor.className = "task-editor";
editor.append(
  field("Title", title, true),
  field("Description", description, true, "field--description")
);

const sidebar = document.createElement("aside");
sidebar.className = "task-sidebar";
sidebar.setAttribute("aria-label", "Task properties and relationships");

const properties = document.createElement("section");
properties.className = "task-properties";
const propertiesHeading = document.createElement("h3");
text(propertiesHeading, "Properties");
properties.append(
  propertiesHeading,
  field("Status", status),
  field("Priority", priority),
  field("Labels", labels, true)
);

const relationshipMount = document.createElement("div");
relationshipMount.dataset.relationshipMount = "";
sidebar.append(properties, relationshipMount);

const footer = document.createElement("footer");
footer.className = "task-actions";
footer.append(result, actions);

form.append(editor, sidebar, footer);
```

Keep `form.addEventListener("submit", ...)` on this same form. Keep every
relationship action button `type="button"`.

- [ ] **Step 5: Add the wide desktop and single-column mobile CSS**

Replace the narrow route/form layout with:

```css
.task-route {
  display: flex;
  flex-direction: column;
  min-height: 100%;
  width: min(100%, 84rem);
  margin: 0 auto;
  border: 1px solid #b9c6d8;
  background: #fff;
  box-shadow: 0 12px 32px rgba(23,32,51,.08);
}
.task-layout {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: minmax(0, 1fr) 20rem;
  grid-template-rows: minmax(0, 1fr) auto;
  min-height: 0;
}
.task-editor {
  display: grid;
  grid-template-rows: auto minmax(8rem, 1fr);
  gap: 1rem;
  min-width: 0;
  min-height: 0;
  padding: 1.15rem;
}
.task-sidebar {
  min-width: 0;
  padding: 1.15rem;
  border-left: 1px solid #d5deea;
  background: #fafbfd;
}
.task-properties {
  display: grid;
  gap: .85rem;
}
.task-actions {
  display: grid;
  grid-column: 1 / -1;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: .75rem 1rem;
  padding: .85rem 1.15rem 1.15rem;
  border-top: 1px solid #d5deea;
}
@media (max-width: 760px) {
  .task-layout {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: auto auto auto;
  }
  .task-editor {
    grid-template-rows: auto auto;
  }
  .task-sidebar {
    border-top: 1px solid #d5deea;
    border-left: 0;
  }
  .task-actions {
    grid-column: 1;
  }
  .field--description textarea {
    min-height: 12rem;
  }
}
```

Preserve the landed `.field--description` internal-scroll behavior on desktop.

- [ ] **Step 6: Run the focused and package tests**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'SharedTaskSidebarLayout|MarksDescriptionAsFlexibleField|Copy|Dependency' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 7: Commit the shared shell**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: add shared task sidebar layout"
```

---

### Task 2: Compact Existing Relationships Inside the Sidebar

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Consumes: `[data-relationship-mount]` from Task 1.
- Produces: `taskRelationships(taskID, draftState)` mounted inside the sidebar.
- Produces: compact `.relationship-row--compact` rows and an overlay `.relationship-listbox`.
- Preserves: current persisted relationship derivation, mutability rules, endpoint orientation, refresh generations, and detached-controller guards.

- [ ] **Step 1: Write the failing sidebar relationship test**

Add `TestHandlerClientMountsCompactRelationshipsInSidebar`. On an existing task
detail route, assert:

```js
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

const form = findElement(main, (element) => element.tagName === "FORM");
const sameForm = form === mountedForm;
if (!sameForm) throw new Error("relationship refresh replaced the task form");
```

Also require missing/tombstoned Depends On rows to have Remove, active Blocks
rows to have Remove, and tombstoned Blocks rows to have no Remove.

- [ ] **Step 2: Run the compact-sidebar test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientMountsCompactRelationshipsInSidebar -count=1
```

Expected: FAIL because relationships still mount after the form and rows do not
have the compact class.

- [ ] **Step 3: Mount the existing controller into the sidebar**

Change `taskForm` to pass the mount element:

```js
const relationshipController = isNew
  ? null
  : taskRelationships(task.id, null);
if (relationshipController) {
  relationshipMount.append(relationshipController.region);
  activeDependencyController = relationshipController;
  refreshRelationshipContext(
    relationshipController,
    ++relationshipRefreshGeneration
  );
}
```

Change `taskRelationships` to return the existing controller without appending
its region after the form. Keep `controller.update()` scoped to list and
candidate children so the task form remains mounted.

- [ ] **Step 4: Render compact relationship rows**

Add the compact class in `relationshipRow`:

```js
row.className = "relationship-row relationship-row--compact";
```

Use a single metadata line:

```js
const metadataParts = [];
if (relationship.task) {
  metadataParts.push(relationship.task.status, relationship.task.priority);
}
metadataParts.push(relationship.id);
text(metadata, metadataParts.join(" · "));
```

Keep full IDs, links, Deleted/Unavailable copy, Retry/Remove labels, and
`data-relationship-id`.

- [ ] **Step 5: Make combobox results an overlay**

Update CSS so closed listboxes reserve no height and open listboxes overlay the
sidebar:

```css
.relationship-editor { position: relative; }
.relationship-listbox {
  position: absolute;
  z-index: 20;
  top: calc(100% + .3rem);
  right: 0;
  left: 0;
  max-height: min(18rem, 45vh);
  overflow-y: auto;
}
.relationship-listbox[hidden] { display: none; }
.relationship-row--compact {
  grid-template-columns: minmax(0, 1fr) auto;
  gap: .22rem .45rem;
  padding: .48rem 0;
  border: 0;
  border-top: 1px solid #e1e7f0;
  border-radius: 0;
}
```

Keep visible keyboard focus and `scrollIntoView({block: "nearest"})`.

- [ ] **Step 6: Run existing relationship regressions**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'MountsCompactRelationshipsInSidebar|DependencyCombobox|DependencyMutation|DependencyFailure|DependencySnapshot|RendersDependencyRelationships' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS, including endpoint orientation, tombstone state, stale-response
guards, and unsaved-form preservation.

- [ ] **Step 7: Commit the compact relationship presentation**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: compact task relationships in sidebar"
```

---

### Task 3: Stage Both Relationship Directions on New Task

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: `newRelationshipDraftState()`.
- Produces: `taskRelationships(taskID, draftState)` where empty `taskID` means draft-only New Task mode.
- Produces: draft entries shaped as `{ id, task, error }`.
- Consumes: `latestTasks`, `latestDeletedTasks`, `relationshipCandidates`, `dependencyCombobox`, and compact rows from Tasks 1–2.
- Guarantees: no nested dependency HTTP mutation before task creation.

- [ ] **Step 1: Write the failing New Task draft test**

Add `TestHandlerClientStagesNewTaskRelationshipsWithoutMutating`. Navigate to
`/tasks/new`, then:

1. locate both comboboxes;
2. select one Depends On candidate and click Add;
3. select a different Blocks candidate and click Add;
4. require compact draft rows in both groups;
5. require selected IDs to disappear from only their own direction's candidate
   list;
6. click Remove on one draft row; and
7. require `fetchCalls` to contain no nested dependency `PUT` or `DELETE`.

Use:

```js
const dependencyMutations = fetchCalls.filter((call) =>
  /\/api\/tasks\/.+\/dependencies\/.+/.test(call.url) &&
  ["PUT", "DELETE"].includes(call.options.method));
if (dependencyMutations.length !== 0) {
  throw new Error("New Task relationship drafts wrote before task creation");
}
```

Require the task title and Description values to remain unchanged throughout.

- [ ] **Step 2: Run the draft test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientStagesNewTaskRelationshipsWithoutMutating -count=1
```

Expected: FAIL because New Task has no active relationship controller.

- [ ] **Step 3: Add explicit draft state**

Add:

```js
function newRelationshipDraftState() {
  return {
    dependsOn: new Map(),
    blocks: new Map()
  };
}

function draftEntry(task) {
  return {
    id: task.id,
    task: {
      id: task.id,
      title: task.title,
      status: task.status,
      priority: task.priority,
      deleted: false
    },
    error: ""
  };
}
```

Create one draft state per mounted New Task form:

```js
const relationshipDrafts = isNew
  ? newRelationshipDraftState()
  : null;
```

Do not store it globally until durable creation succeeds with partial failures.

- [ ] **Step 4: Extend candidate filtering for draft mode**

Change the signature:

```js
function relationshipCandidates(taskID, direction, draftState = null)
```

Use the reconciled active/deleted snapshot. Build exclusions:

```js
const excluded = new Set();
if (taskID) excluded.add(taskID);
if (taskID && direction === "depends-on") {
  (current.dependencies || []).forEach((id) => excluded.add(id));
}
if (taskID && direction === "blocks") {
  activeTasks.forEach((task) => {
    if ((task.dependencies || []).includes(taskID)) excluded.add(task.id);
  });
}
if (draftState) {
  const draftMap = direction === "depends-on"
    ? draftState.dependsOn
    : draftState.blocks;
  draftMap.forEach((_entry, id) => excluded.add(id));
}
```

Return active candidates in canonical `latestTasks` order. Keep search
case-insensitive for title and full ID.

- [ ] **Step 5: Make the relationship controller draft-aware**

Change the signature:

```js
function taskRelationships(taskID, draftState = null)
```

In New Task mode (`taskID === ""`):

- render persisted relationship arrays as empty;
- overlay entries from `draftState.dependsOn` and `draftState.blocks`;
- Add stores `draftEntry(selectedTask)` in the correct map;
- Remove deletes the entry from the correct map;
- call `controller.update()` after either local change; and
- never call `mutateRelationship`.

Give draft rows:

```js
row.dataset.relationshipDraft = "";
```

Reuse the compact row renderer, but show `Not saved` when `entry.error` is
empty and the row is a creation draft.

- [ ] **Step 6: Prevent combobox Enter from submitting New Task**

In the combobox key handler, prevent default for Enter whether or not an active
option exists:

```js
if (event.key === "Enter") {
  event.preventDefault();
  if (state.activeIndex >= 0) {
    selectCandidate(options[state.activeIndex]);
  }
  return;
}
```

Add an assertion to the draft test:

```js
dependsInput.dispatch("keydown", {
  key: "Enter",
  preventDefault() { enterPrevented = true; }
});
if (!enterPrevented || createCalls !== 0) {
  throw new Error("relationship combobox Enter submitted New Task");
}
```

- [ ] **Step 7: Run focused and package tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'StagesNewTaskRelationships|DependencyCombobox|SharedTaskSidebarLayout' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 8: Commit New Task draft editing**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: stage relationships on new tasks"
```

---

### Task 4: Persist New Task Drafts After Creation

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: `draftRelationshipOperations(taskID, draftState)`.
- Produces: `persistRelationshipDrafts(taskID, draftState)`.
- Consumes: `mutateTask`, `currentRefreshResult`, `refresh`, and draft entry maps.
- Returns: `{ failures, warnings }`, where `failures` is a fresh relationship draft state containing only failed entries.

- [ ] **Step 1: Write the failing complete-success test**

Add `TestHandlerClientCreatesTaskWithBothRelationshipDirections`. Stage one
entry in each group, submit the form, and make the fetch harness return:

1. a successful `POST /api/tasks` mutation document whose task ID is
   `createdID`;
2. a successful bodyless Depends On PUT;
3. a successful bodyless Blocks PUT;
4. one refreshed `/api/tasks` document; and
5. one refreshed `/api/tasks?deleted=true` document.

Assert:

```js
assertCall("PUT",
  "/api/tasks/" + encodeURIComponent(createdID) +
  "/dependencies/" + encodeURIComponent(prerequisiteID),
  false);
assertCall("PUT",
  "/api/tasks/" + encodeURIComponent(blockedTaskID) +
  "/dependencies/" + encodeURIComponent(createdID),
  false);
```

Require exactly one task `POST`, no dependency request body, one final active
refresh, one final deleted refresh, and navigation to `/tasks/<createdID>` only
after both PUTs and refreshes resolve.

- [ ] **Step 2: Run the creation test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientCreatesTaskWithBothRelationshipDirections -count=1
```

Expected: FAIL because the current create submit ignores draft relationships
and navigates after only the task mutation.

- [ ] **Step 3: Build deterministic operations**

Add:

```js
function draftRelationshipOperations(taskID, draftState) {
  const operations = [];
  draftState.dependsOn.forEach((entry) => {
    operations.push({
      direction: "dependsOn",
      dependentID: taskID,
      prerequisiteID: entry.id,
      entry
    });
  });
  draftState.blocks.forEach((entry) => {
    operations.push({
      direction: "blocks",
      dependentID: entry.id,
      prerequisiteID: taskID,
      entry
    });
  });
  return operations;
}
```

Map iteration preserves the canonical order in which candidates were added.

- [ ] **Step 4: Persist every staged edge and collect outcomes**

Add:

```js
async function persistRelationshipDrafts(taskID, draftState) {
  const failures = newRelationshipDraftState();
  const warnings = [];
  for (const operation of draftRelationshipOperations(taskID, draftState)) {
    const path = "/api/tasks/" +
      encodeURIComponent(operation.dependentID) +
      "/dependencies/" +
      encodeURIComponent(operation.prerequisiteID);
    try {
      const document = await mutateTask("PUT", path);
      (document.warnings || []).forEach((warning) =>
        warnings.push(warning.message));
    } catch (error) {
      const failed = {
        ...operation.entry,
        error: error instanceof Error
          ? error.message
          : "Dependency update failed."
      };
      failures[operation.direction].set(operation.entry.id, failed);
    }
  }
  return { failures, warnings };
}
```

Attempt every operation even after one fails. Do not refresh between edges.

- [ ] **Step 5: Split New Task submit from existing-task submit**

After `POST /api/tasks` succeeds:

```js
const taskID = document.task.id;
const relationshipOutcome = await persistRelationshipDrafts(
  taskID,
  relationshipDrafts
);
const refreshed = await currentRefreshResult(refresh());
```

Navigate only after the winning refresh returns `status: "applied"`. Existing
task PATCH behavior remains unchanged.

Clear the New Task draft maps only after task creation and all edge attempts
have completed.

- [ ] **Step 6: Preserve durable warnings on complete success**

Combine task-create warnings and relationship warnings into the feedback that
will be shown after navigation:

```js
const messages = [
  ...mutationWarnings(document),
  ...relationshipOutcome.warnings
];
```

Store them in a client-only `pendingTaskMessages` map keyed by task ID. The
detail form consumes and deletes its entry after rendering it in the form
feedback live region.

- [ ] **Step 7: Run creation and regression tests**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'CreatesTaskWithBothRelationshipDirections|DependencyMutation|Create|SharedTaskSidebarLayout' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 8: Commit durable New Task relationships**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: persist new task relationships"
```

---

### Task 5: Recover from Create, Relationship, and Refresh Failures

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`

**Interfaces:**
- Produces: `pendingRelationshipDrafts`, a `Map<taskID, draftState>`.
- Produces: `createdTaskRecovery`, shaped as `{ taskID, failures, warnings }` for refresh retry without another POST.
- Consumes: `persistRelationshipDrafts`, `currentRefreshResult`, controller ownership guards, and compact draft rows.
- Guarantees: task creation is never repeated after a durable create response.

- [ ] **Step 1: Write the failing task-create failure test**

Add `TestHandlerClientPreservesNewTaskRelationshipDraftsWhenCreateFails`.
Stage both directions, populate title and Description, return a version-1
validation error from `POST /api/tasks`, and require:

```js
if (title.value !== unsavedTitle ||
    description.value !== unsavedDescription) {
  throw new Error("task fields were discarded after create failure");
}
if (!draftDependsOnRow || !draftBlocksRow) {
  throw new Error("relationship drafts were discarded after create failure");
}
if (createCalls !== 1 || dependencyCalls !== 0) {
  throw new Error("create failure attempted relationship persistence");
}
```

- [ ] **Step 2: Write the failing partial-success test**

Add `TestHandlerClientRetainsFailedRelationshipDraftsAfterCreate`. Return:

- successful task creation;
- successful Depends On PUT with a warning;
- failed Blocks PUT with `dependency would create a cycle`;
- successful active/deleted refresh.

Require navigation to detail, a durable Depends On row, an unsaved Blocks draft
row with the error, and Retry/Remove buttons. Require the form feedback to say
the task was created but one relationship was not saved.

- [ ] **Step 3: Write the failing refresh-recovery test**

Add `TestHandlerClientDoesNotDuplicateCreatedTaskWhenRefreshFails`. Return
successful task creation and relationship mutations, then fail the active task
refresh. Click the displayed retry action and return a successful refresh.
Require:

```js
if (createCalls !== 1) {
  throw new Error("refresh retry created a duplicate task");
}
if (historyPaths.at(-1) !== "/tasks/" + createdID) {
  throw new Error("refresh retry did not open the created task");
}
```

- [ ] **Step 4: Run the failure tests and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'PreservesNewTaskRelationshipDraftsWhenCreateFails|RetainsFailedRelationshipDraftsAfterCreate|DoesNotDuplicateCreatedTaskWhenRefreshFails' -count=1
```

Expected: FAIL because there is no failed-draft transfer, retry action, or
created-task recovery state.

- [ ] **Step 5: Transfer only failed drafts to detail**

Add:

```js
const pendingRelationshipDrafts = new Map();
const pendingTaskMessages = new Map();
let createdTaskRecovery = null;
```

After edge attempts:

```js
if (hasRelationshipDrafts(relationshipOutcome.failures)) {
  pendingRelationshipDrafts.set(taskID, relationshipOutcome.failures);
  pendingTaskMessages.set(taskID, [
    "Task created, but some relationships were not saved.",
    ...relationshipOutcome.warnings
  ]);
}
```

When rendering existing detail:

```js
const failedDrafts = pendingRelationshipDrafts.get(task.id) || null;
const controller = taskRelationships(task.id, failedDrafts);
```

Render a failed draft row with:

- `Not saved`;
- the exact error;
- Retry; and
- Remove.

Retry calls the correctly oriented nested PUT for that one draft. On success,
delete only that draft entry, refresh once, and update the existing controller.
Remove deletes only the client draft entry.

- [ ] **Step 6: Preserve New Task state on POST failure**

In the New Task submit catch, do not rebuild or navigate. Re-enable the current
form and call:

```js
text(result, error instanceof Error
  ? error.message
  : "Task creation failed.");
```

Do not clear `relationshipDrafts`, input values, combobox queries, or valid
selections.

- [ ] **Step 7: Add refresh retry without another create**

When durable create succeeds but the winning refresh is not applied:

```js
createdTaskRecovery = {
  taskID,
  failures: relationshipOutcome.failures,
  warnings: relationshipOutcome.warnings
};
showCreatedTaskRefreshFailure(result, async () => {
  const retried = await currentRefreshResult(refresh());
  if (retried.status !== "applied") return;
  applyCreatedTaskRecovery(createdTaskRecovery);
  const recoveredID = createdTaskRecovery.taskID;
  createdTaskRecovery = null;
  navigate("/tasks/" + encodeURIComponent(recoveredID));
});
```

Disable Create after durable creation. The retry control must be
`type="button"` and must never call `POST /api/tasks`.

- [ ] **Step 8: Guard failed-draft writes by controller ownership**

Before any post-await failed-draft row, message, Retry, or Remove update, require:

```js
if (!ownsDependencyController(controller)) return;
```

Keep unconditional busy cleanup in `finally`. Add navigation during Retry to
the existing detached-controller test matrix.

- [ ] **Step 9: Run failure and race regressions**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'PreservesNewTaskRelationshipDrafts|RetainsFailedRelationshipDrafts|DoesNotDuplicateCreatedTask|DependencyMutation|ControllerSupersession|Detached' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 10: Commit explicit recovery behavior**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "fix: recover partial new task relationships"
```

---

### Task 6: Verify Accessibility and Responsive Layout in a Real Browser

**Files:**
- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`
- Modify: `docs/superpowers/plans/2026-07-30-task-detail-sidebar-layout.md`

**Interfaces:**
- Consumes: complete shared sidebar and New Task draft behavior.
- Produces: computed-layout and screenshot evidence at desktop and mobile sizes.
- Preserves: keyboard focus, live regions, no horizontal overflow, Description internal scrolling, and action reachability.

- [ ] **Step 1: Add executable accessibility assertions**

Add `TestHandlerClientSidebarAccessibilityAndMobileOrder`. Require:

```js
if (sidebar.getAttribute("aria-label") !==
    "Task properties and relationships") {
  throw new Error("sidebar does not identify its contents");
}
if (dependsInput.attributes.role !== "combobox" ||
    blocksInput.attributes.role !== "combobox") {
  throw new Error("relationship controls lost combobox semantics");
}
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
```

Require each group message to retain `role="status"` and `aria-live="polite"`.

- [ ] **Step 2: Run the accessibility test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientSidebarAccessibilityAndMobileOrder -count=1
```

Expected: FAIL on any missing region label, button type, live region, or DOM
order exposed by Tasks 1–5.

- [ ] **Step 3: Make the minimum accessibility corrections**

Fix only the failed assertions. Do not change copy or introduce new interaction
patterns beyond:

- semantic section headings;
- the sidebar label;
- explicit `type="button"`;
- polite group/form feedback; and
- DOM order matching the mobile stack.

- [ ] **Step 4: Build and serve the current branch**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-sidebar-verify ./cmd/workbook
/private/tmp/workbook-sidebar-verify serve
```

Use the temporary loopback URL printed by the server. Open both:

- `/tasks/new`
- `/tasks/WB-01KYQMZMKMD9RWNH6PDTMZ97MC`

- [ ] **Step 5: Verify wide desktop layout**

At 1440×900 and 1280×800, evaluate:

```js
const route = document.querySelector(".task-route");
const layout = document.querySelector(".task-layout");
const editor = document.querySelector(".task-editor");
const sidebar = document.querySelector(".task-sidebar");
const description = document.querySelector("#task-description");
const actions = document.querySelector(".task-actions");
const columns = getComputedStyle(layout).gridTemplateColumns
  .split(" ")
  .map((value) => Number.parseFloat(value));
({
  routeIsWide: route.getBoundingClientRect().width >= 1050,
  mainIsWider: columns[0] > columns[1] * 2,
  sidebarIsDesktopWidth:
    sidebar.getBoundingClientRect().width >= 288 &&
    sidebar.getBoundingClientRect().width <= 352,
  descriptionHasExtraHeight: description.clientHeight > 220,
  descriptionScrollsInternally:
    getComputedStyle(description).overflowY === "auto",
  actionsAboveFold:
    actions.getBoundingClientRect().bottom <= window.innerHeight,
});
```

Expected: every value is `true` for New Task and existing detail.

Populate enough Description text and relationships to overflow. Require the
Description height to remain unchanged when relationship rows grow.

Capture screenshots:

- `/private/tmp/workbook-sidebar-new-1440.png`
- `/private/tmp/workbook-sidebar-detail-1440.png`

- [ ] **Step 6: Verify constrained desktop**

At 1280×600, require:

- no overlap among editor, sidebar, and footer;
- every action is reachable through the page's vertical scroll;
- Description remains at least 128px tall; and
- an open relationship listbox stays within the viewport.

Capture:

```text
/private/tmp/workbook-sidebar-detail-1280x600.png
```

- [ ] **Step 7: Verify mobile layout**

At 390×844 and 360×640, evaluate:

```js
({
  oneColumn:
    getComputedStyle(layout).gridTemplateColumns.split(" ").length === 1,
  noHorizontalOverflow:
    document.documentElement.scrollWidth <= window.innerWidth,
  editorBeforeSidebar:
    editor.getBoundingClientRect().top < sidebar.getBoundingClientRect().top,
  sidebarBeforeActions:
    sidebar.getBoundingClientRect().top < actions.getBoundingClientRect().top,
  descriptionMinimum:
    description.getBoundingClientRect().height >= 192,
});
```

Expected: every value is `true`. Open each combobox and require the listbox
right edge to remain within `window.innerWidth`.

Capture:

```text
/private/tmp/workbook-sidebar-new-390.png
/private/tmp/workbook-sidebar-detail-390.png
```

- [ ] **Step 8: Critique and apply only evidence-backed CSS fixes**

Review screenshots for:

- sidebar visual weight;
- compact row density;
- long full-ID wrapping;
- footer separation;
- Description prominence;
- focus visibility; and
- excessive decoration.

Adjust CSS only when a computed check or screenshot shows a specific defect.
Rerun Steps 5–7 after each correction.

- [ ] **Step 9: Run accessibility and web regressions**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'SidebarAccessibility|SharedTaskSidebarLayout|Description|Dependency|Copy|Restore' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 10: Record visual evidence and commit refinements**

Append the exact viewport results and screenshot paths to this plan under the
task checklist. Then:

```sh
git add docs/superpowers/plans/2026-07-30-task-detail-sidebar-layout.md internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "fix: refine responsive task sidebar"
```

---

### Task 7: Document, Verify, Review, and Update the Pull Request

**Files:**
- Modify: `README.md`
- Modify: `internal/cli/run_test.go`
- Modify: `docs/superpowers/plans/2026-07-30-task-detail-sidebar-layout.md`

**Interfaces:**
- Documents: shared sidebar layout, mobile stacking, New Task relationship staging, endpoint orientation, and partial success.
- Preserves: implemented-command policy, local-first limitations, and existing dependency route documentation.

- [ ] **Step 1: Add failing README contract assertions**

Extend `TestREADMEImplementedCommands` with:

```go
for _, required := range []string{
    "wide main column and a compact Properties sidebar",
    "New Task stages both Depends On and Blocks",
    "relationship mutations run after the task receives its durable ID",
    "successful relationships remain durable",
    "failed relationships remain available to retry or remove",
    "On narrow screens, the task editor, Properties, Relationships, and actions stack in that order",
} {
    if !strings.Contains(readme, required) {
        t.Errorf("README task sidebar documentation is missing %q", required)
    }
}
```

- [ ] **Step 2: Run the README test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestREADME -count=1
```

Expected: FAIL on each new sidebar/draft behavior statement.

- [ ] **Step 3: Update README behavior documentation**

Add a concise web-board paragraph containing the exact asserted text:

```markdown
Task forms use a wide main column and a compact Properties sidebar for status,
priority, labels, Depends On, and Blocks. New Task stages both Depends On and
Blocks without writing task refs; relationship mutations run after the task
receives its durable ID. If only some edges succeed, successful relationships
remain durable while failed relationships remain available to retry or remove.
On narrow screens, the task editor, Properties, Relationships, and actions
stack in that order.
```

Remove or revise the older statement that dependency editing during task
creation is out of scope.

- [ ] **Step 4: Run focused documentation and feature tests**

Run:

```sh
gofmt -w internal/cli/run_test.go internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run TestREADME -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core ./internal/presentation ./internal/webui ./internal/cli -count=1
```

Expected: PASS.

- [ ] **Step 5: Merge any newer `origin/main` before final verification**

Run:

```sh
git fetch origin main
git rev-list --left-right --count origin/main...HEAD
```

If the left count is nonzero:

```sh
git merge --no-edit origin/main
```

Resolve by preserving the sidebar feature plus all current-main behavior. Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui ./internal/cli -count=1
git diff --check
```

Expected: PASS with no unmerged files.

- [ ] **Step 6: Run complete fresh verification**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-task-sidebar ./cmd/workbook
git diff --check
git status --short
```

Expected:

- every package passes;
- vet has no diagnostics;
- the production binary builds;
- both diff checks are clean; and
- the worktree contains only intended committed feature work.

- [ ] **Step 7: Commit documentation**

```sh
git add README.md internal/cli/run_test.go docs/superpowers/plans/2026-07-30-task-detail-sidebar-layout.md
git commit -m "docs: describe task sidebar relationships"
```

- [ ] **Step 8: Request independent task and whole-branch review**

Review `origin/main...HEAD` against:

- `docs/superpowers/specs/2026-07-30-task-detail-sidebar-layout-design.md`
- `docs/superpowers/specs/2026-07-29-web-dependency-management-design.md`

Require explicit assessment of:

- Description-height compatibility;
- New Task draft directions and no pre-create mutation;
- complete success and partial failure;
- retry without duplicate task creation;
- existing task relationship regressions;
- mobile DOM/visual order;
- accessibility and live-region behavior; and
- real-browser screenshot evidence.

Resolve every Critical or Important finding and rerun affected plus complete
verification.

- [ ] **Step 9: Align the Workbook task description and status**

Update `WB-01KYQMZMKMD9RWNH6PDTMZ97MC` so its acceptance criteria no longer say
New Task and reverse views are out of scope. Record:

- shared task sidebar;
- both relationship directions on New Task;
- post-create nested mutations; and
- partial-success retry behavior.

When the PR update is fully verified:

```sh
workbook update WB-01KYQMZMKMD9RWNH6PDTMZ97MC --status in-review --json
git push origin refs/workbook/tasks/WB-01KYQMZMKMD9RWNH6PDTMZ97MC:refs/workbook/tasks/WB-01KYQMZMKMD9RWNH6PDTMZ97MC
```

- [ ] **Step 10: Push the branch and verify PR #11**

Run:

```sh
git push origin codex/wb-dependency-web-ui
gh pr view 11 --json url,state,isDraft,baseRefName,headRefName,mergeable,statusCheckRollup
git status --short
```

Expected:

- PR #11 remains open and ready against `main`;
- GitHub reports the branch mergeable after recalculation;
- the remote branch matches local HEAD; and
- the worktree is clean and retained for review feedback.

---
