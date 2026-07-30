# Detail Description Height Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the shared task form fill the available detail-view height, with Description consuming the flexible space while status, priority, labels, feedback, and actions remain visible.

**Architecture:** Keep the change inside the embedded web page. Give the Description field an explicit layout hook in the existing form builder, make the task route a viewport-filling flex column, and make the form a minimum-zero grid whose Description row receives the remaining height and scrolls internally when its text overflows.

**Tech Stack:** Go 1.26, embedded HTML/CSS/vanilla JavaScript, the existing Node-based executable client harness, and real-browser layout verification.

## Global Constraints

- Apply the layout to the existing shared new/detail task form without changing task data, APIs, routing, or mutation behavior.
- Keep status, priority, labels, validation feedback, Save, Back, and Delete above the fold at ordinary desktop viewport heights.
- Let Description absorb available height and scroll internally when its content exceeds that height.
- Let the layout, rather than a user resize handle, control Description height so it cannot cover later form controls.
- Preserve the existing single-column responsive form below 560 pixels.
- Add no dependency or build step.
- Follow TDD: observe the missing Description layout hook before adding production code, then verify the computed browser layout after the change.

---

### Task 1: Give Description the flexible form row

**Files:**
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/webui/assets/index.html`

**Interfaces:**
- Consumes: the existing `taskForm(mode, task)` and `field(labelText, control, wide)` client helpers.
- Produces: a `field--description` wrapper around `#task-description`.
- Produces: a full-height `.task-route`, a minimum-zero flexible `.task-form`, and an internally scrolling Description textarea.

- [ ] **Step 1: Write the failing executable-client test**

Add `TestHandlerClientMarksDescriptionAsFlexibleField`. Render an existing task detail route through `clientDOMHarness`, find `#task-description`, and require its parent wrapper to include `field--description`.

```go
program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(() => {
  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("detail form did not render Description");
  if (!description.parentElement.className.split(/\s+/).includes("field--description")) {
    throw new Error("Description is not marked as the flexible form field");
  }
}, 0);
`
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientMarksDescriptionAsFlexibleField -count=1
```

Expected: FAIL with `Description is not marked as the flexible form field`.

- [ ] **Step 3: Add the Description layout hook**

Extend `field` with an optional modifier class and render Description with `field--description`:

```js
function field(labelText, control, wide, modifier = "") {
  const classes = ["field"];
  if (wide) classes.push("field--wide");
  if (modifier) classes.push(modifier);
  const wrapper = document.createElement("div"); wrapper.className = classes.join(" ");
  const label = document.createElement("label"); label.htmlFor = control.id; text(label, labelText);
  wrapper.append(label, control);
  return wrapper;
}
```

Pass `"field--description"` only for the Description field.

- [ ] **Step 4: Make Description consume the available height**

Update the existing CSS rules:

```css
.task-route { display: flex; flex-direction: column; min-height: 100%; max-width: 48rem; margin: 0 auto; border: 1px solid #b9c6d8; background: #fff; box-shadow: 0 12px 32px rgba(23,32,51,.08); }
.task-form { display: grid; flex: 1 1 auto; grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: auto minmax(8rem, 1fr) auto auto auto auto; gap: 1rem; min-height: 0; padding: 1.15rem; }
.field--description { grid-template-rows: auto minmax(0, 1fr); min-height: 0; }
.field--description textarea { height: 100%; min-height: 0; overflow-y: auto; resize: none; }
```

The existing 560-pixel media rule keeps one column. Its implicit final grid row continues to hold actions after the explicit Description row.

- [ ] **Step 5: Verify GREEN and the full web package**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run TestHandlerClientMarksDescriptionAsFlexibleField -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -count=1
```

Expected: all tests pass, including the executable-client test without a Node skip.

- [ ] **Step 6: Commit the tested layout**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: grow task descriptions with detail view"
```

---

### Task 2: Verify viewport behavior and finish the branch

**Files:**
- Modify: `docs/superpowers/plans/2026-07-30-detail-description-height.md`

**Interfaces:**
- Consumes: the built `workbook serve` application and story `WB-01KYQZ64833PGMFACTMSW4HEM1`.
- Produces: fresh browser, test, vet, build, and diff evidence for publication.

- [ ] **Step 1: Verify the real desktop layout**

Build and serve the branch, then open the story detail route at a 1280-by-800 viewport. Evaluate:

```js
const route = document.querySelector(".task-route");
const description = document.querySelector("#task-description");
const actions = document.querySelector(".form-actions");
const main = document.querySelector("main");
const mainStyle = getComputedStyle(main);
const mainContentHeight = main.clientHeight
  - Number(mainStyle.paddingTop.slice(0, -2))
  - Number(mainStyle.paddingBottom.slice(0, -2));
({
  routeFillsMain: Math.abs(route.getBoundingClientRect().height - mainContentHeight) <= 2,
  descriptionHasExtraHeight: description.clientHeight > 128,
  actionsAboveFold: actions.getBoundingClientRect().bottom <= window.innerHeight,
  descriptionScrollsInternally: getComputedStyle(description).overflowY === "auto",
});
```

Expected: every value is `true`. Populate Description with enough lines to overflow and require `description.scrollHeight > description.clientHeight` while actions remain above the fold.

- [ ] **Step 2: Verify the constrained-height and narrow layouts**

At 1280-by-600, require the form to grow beyond the viewport rather than overlap its controls, with `main` owning the necessary vertical overflow and every control remaining reachable. At 390-by-844, require the form to remain one column, the actions to remain reachable, and no horizontal document overflow.

- [ ] **Step 3: Run final verification**

Run:

```sh
gofmt -w internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-detail-verify ./cmd/workbook
git diff --check
git status --short
```

Expected: tests, vet, build, and diff checks succeed; status contains only the intended plan, embedded page, and handler test changes.

- [ ] **Step 4: Review, publish, and advance the story**

Request an independent review over `origin/main..HEAD`, address every Critical or Important finding, rerun final verification, commit the plan if it is not already committed, push the feature branch, and open a ready pull request against `main`. After the PR exists, update the story to `in-review` and publish the task ref.
