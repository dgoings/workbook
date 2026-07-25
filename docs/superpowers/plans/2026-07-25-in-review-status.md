# In Review Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the canonical `in-review` workflow status and render it consistently in every Workbook view.

**Architecture:** `internal/core` owns an ordered immutable workflow-status definition with each durable value and human label. Core validation and service sorting consume that definition; presentation board columns consume it too. The web asset adds the new known status to its six-column client refresh model.

**Tech Stack:** Go 1.26, Go standard-library tests, embedded HTML/CSS/JavaScript.

## Global Constraints

- Durable serialized status value: `in-review`.
- Canonical order: Backlog, Ready, Blocked, In Progress, In Review, Done.
- `workbook next` selects only `Ready`; only `Done` satisfies dependencies.
- Existing task records require no migration.

---

### Task 1: Canonical core workflow statuses

**Files:**
- Modify: `internal/core/task.go:20-27,115-121`
- Modify: `internal/core/service.go:627-641`
- Modify: `internal/core/task_test.go:36-58`
- Modify: `internal/core/service_test.go:90-128`
- Modify: `internal/cli/run_test.go:380-470`

**Interfaces:**
- Produces: `StatusInReview Status = "in-review"`.
- Produces: `WorkflowStatuses() []StatusDefinition`, ordered and safe for callers to iterate without mutating core state.
- Produces: `StatusDefinition{Status Status, Label string}` with labels used by presentation.
- Consumes: `WorkflowStatuses()` in `isValidStatus` and `statusOrder`.

- [x] **Step 1: Write the failing core tests**

Extend `TestNormalizeTaskAcceptsEveryStatusAndPriority` so the test iterates the expected six statuses including `StatusInReview`, and add a service-list fixture for every workflow state. Assert list output IDs are ordered Backlog, Ready, Blocked, In Progress, In Review, Done regardless of input order. Add a `TestWorkflowStatusesReturnsCanonicalOrder` assertion. Add `TestCLIInReviewStatus`, which creates and updates tasks with `--status in-review`, then uses `list --status in-review --json` to assert both task IDs are returned.

```go
want := []StatusDefinition{
    {Status: StatusBacklog, Label: "Backlog"},
    {Status: StatusReady, Label: "Ready"},
    {Status: StatusBlocked, Label: "Blocked"},
    {Status: StatusInProgress, Label: "In Progress"},
    {Status: StatusInReview, Label: "In Review"},
    {Status: StatusDone, Label: "Done"},
}
if got := WorkflowStatuses(); !reflect.DeepEqual(got, want) { t.Fatalf(...) }
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core -run 'TestNormalizeTaskAcceptsEveryStatusAndPriority|TestWorkflowStatusesReturnsCanonicalOrder|TestServiceListOrdersWorkflowStatuses' -count=1 && go test ./internal/cli -run TestCLIInReviewStatus -count=1`

Expected: FAIL because `StatusInReview`, `StatusDefinition`, and `WorkflowStatuses` are not defined, and the CLI rejects `in-review`.

- [x] **Step 3: Write minimal implementation**

In `task.go`, add the `StatusInReview` constant and the ordered definition:

```go
type StatusDefinition struct {
    Status Status
    Label  string
}

var workflowStatuses = [...]StatusDefinition{
    {Status: StatusBacklog, Label: "Backlog"},
    {Status: StatusReady, Label: "Ready"},
    {Status: StatusBlocked, Label: "Blocked"},
    {Status: StatusInProgress, Label: "In Progress"},
    {Status: StatusInReview, Label: "In Review"},
    {Status: StatusDone, Label: "Done"},
}

func WorkflowStatuses() []StatusDefinition {
    return append([]StatusDefinition(nil), workflowStatuses[:]...)
}
```

Replace the status-validation switch with iteration over `workflowStatuses`. Replace `statusOrder` with a loop that returns the matching index; return `len(workflowStatuses)` for an unknown status.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core -run 'TestNormalizeTaskAcceptsEveryStatusAndPriority|TestWorkflowStatusesReturnsCanonicalOrder|TestServiceListOrdersWorkflowStatuses' -count=1 && go test ./internal/cli -run TestCLIInReviewStatus -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/core/task.go internal/core/task_test.go internal/core/service.go internal/core/service_test.go
git commit -m "feat: add in review task status"
```

### Task 2: Derive board views from the core workflow

**Files:**
- Modify: `internal/presentation/board.go:24-57`
- Modify: `internal/presentation/board_test.go:91-180`
- Modify: `internal/webui/assets/index.html:29,76`
- Modify: `internal/webui/handler_test.go:300-350`

**Interfaces:**
- Consumes: `core.WorkflowStatuses() []core.StatusDefinition`.
- Produces: board columns in exactly the canonical core order, including `In Review`.
- Preserves: unknown status tasks remain visible in the existing unknown section.

- [x] **Step 1: Write failing presentation and web tests**

Update `TestNewBoardPreservesInputOrderAndIncludesEmptyColumns` to include an `in-review` task and expect six columns in canonical order. Update the unknown-status test to expect six known columns. Add a handler test fixture with `StatusInReview` and assert the rendered HTML includes `In Review`, `data-status="in-review"`, and its task card.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/presentation ./internal/webui -run 'TestNewBoardPreservesInputOrderAndIncludesEmptyColumns|TestNewBoardKeepsUnknownStatusesVisible|Test.*InReview' -count=1`

Expected: FAIL because `In Review` is not present as a known board column and web refresh categorizes it as unknown.

- [x] **Step 3: Write minimal implementation**

Replace `columnDefinitions` in `board.go` with construction from `core.WorkflowStatuses()`:

```go
board := Board{Columns: make([]Column, 0, len(core.WorkflowStatuses()))}
for _, definition := range core.WorkflowStatuses() {
    board.Columns = append(board.Columns, Column{Status: definition.Status, Label: definition.Label})
}
```

In the embedded asset, change the desktop grid to `repeat(6, minmax(10.5rem, 1fr))` and add `"in-review"` to the JavaScript `statuses` set. Do not change the existing narrow responsive layout.

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/presentation ./internal/webui -run 'TestNewBoardPreservesInputOrderAndIncludesEmptyColumns|TestNewBoardKeepsUnknownStatusesVisible|Test.*InReview' -count=1`

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/presentation/board.go internal/presentation/board_test.go internal/webui/assets/index.html internal/webui/handler_test.go
git commit -m "feat: show in review tasks on boards"
```

### Task 3: Documentation and complete verification

**Files:**
- Modify: `README.md:78-105,145-160`

**Interfaces:**
- Produces: user documentation naming the six workflow states in their canonical order.

- [x] **Step 1: Document the implemented workflow**

State that task statuses are Backlog, Ready, Blocked, In Progress, In Review, and Done in that order. Clarify that `next` remains Ready-only and task dependencies require Done.

- [x] **Step 2: Run focused CLI test and complete suite**

Run: `go test ./internal/cli -run TestCLIInReviewStatus -count=1 && go test ./... -count=1 && go vet ./... && git diff --check`

Expected: all commands exit 0.

- [x] **Step 3: Commit**

```bash
git add README.md docs/superpowers/plans/2026-07-25-in-review-status.md
git commit -m "docs: describe in review workflow"
```
