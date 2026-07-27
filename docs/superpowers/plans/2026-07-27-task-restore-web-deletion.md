# Task Restore and Web Deletion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add append-only task restoration to the CLI and local web UI while keeping tombstoned tasks immutable except for restore.

**Architecture:** The core model gains a payload-free `task.restore` operation, and `core.Service.Restore` writes it only from a tombstoned parent checkpoint. The CLI exposes that service operation. The web handler gains narrow delete and restore callbacks/routes, and the embedded app renders an active-task Delete control and a deleted-task view.

**Tech Stack:** Go 1.26, Go standard library `net/http`, embedded HTML/CSS/vanilla JavaScript, Git-backed core service, Go `testing` and `httptest`.

## Global Constraints

- `task.restore` is append-only and payload-free.
- A tombstoned task accepts no mutation except `task.restore`; restore is invalid for an active task.
- Restore preserves all task data, history generation, and task ID; only its deleted flag, operation metadata, update time, and commit head change.
- CLI and HTTP success responses retain their existing versioned result envelopes.
- The local web POC adds neither confirmation dialogs nor permanent deletion nor draft persistence.

---

### Task 1: Make restoration a valid core operation

**Files:**

- Modify: `internal/core/operation.go`
- Modify: `internal/core/operation_test.go`
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**

- Produces: `core.OperationTaskRestore OperationType = "task.restore"`.
- Produces: `func (s Service) Restore(context.Context, string) (Task, error)`.
- Consumes: a tombstoned `StateDocument` and writes one payload-free restore operation.

- [ ] **Step 1: Write the failing core tests.** Extend `TestApplyCreateUpdateAndTombstone` with a clock-4 `task.restore` pack; assert `Deleted == false` and every task field matches the pre-tombstone state. Add invalid-operation cases for restore on an active parent and restore carrying `field`, `value`, or `task` data. Add `TestServiceRestore` for a tombstoned memory snapshot, asserting one write with only `OperationTaskRestore`, plus an active-task validation case with no write.

- [ ] **Step 2: Verify RED.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run 'TestApplyCreateUpdateAndTombstone|TestServiceRestore' -count=1`

  Expected: FAIL because `task.restore` is unsupported and `Service.Restore` does not exist.

- [ ] **Step 3: Implement the minimal transition.** Add the operation constant and validate it as payload-free. In `Apply`, let a tombstoned parent accept exactly one restore operation, copy its task, set `Deleted` false, apply normal timestamp/normalization/checkpoint handling, and retain the existing tombstone guard for every other pack. Add `Service.Restore` beside `Delete`: resolve, reject active tasks, assign one operation ID, then write the `"restore task"` mutation.

```go
func (s Service) Restore(ctx context.Context, idOrPrefix string) (Task, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil { return Task{}, err }
	if !parent.State.Task.Deleted {
		return Task{}, Errorf(CategoryValidation, "cannot restore an active task")
	}
	operations := []Operation{{Type: OperationTaskRestore}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil { return Task{}, err }
	return s.writeMutation(ctx, &parent, operations, "restore task")
}
```

- [ ] **Step 4: Verify GREEN.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -count=1`

  Expected: PASS, including the existing tests that reject ordinary tombstoned-task mutations.

- [ ] **Step 5: Commit.**

```sh
git add internal/core/operation.go internal/core/operation_test.go internal/core/service.go internal/core/service_test.go
git commit -m "feat: restore tombstoned tasks"
```

### Task 2: Expose restoration through the CLI

**Files:**

- Modify: `internal/cli/run.go`
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/run_test.go`
- Modify: `internal/cli/flags_test.go`

**Interfaces:**

- Produces: `workbook restore <id-or-prefix> [--json]`.
- Consumes: `core.Service.Restore`.
- Produces: the existing task result envelope with `command: "restore"`.

- [ ] **Step 1: Write failing CLI contract tests.** In the CRUD flow, delete a task, invoke `restore <accepted-prefix> --json`, decode the result, and assert it is active. Follow with `show` and `list` to prove it is visible again. Add an active-task restore case that returns a validation envelope. In `flags_test.go`, require `restore` in command enumeration and assert its only option is boolean `--json`.

- [ ] **Step 2: Verify RED.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -run 'TestRun.*Restore|TestCommandMetadata' -count=1`

  Expected: FAIL because the command is not recognized or described.

- [ ] **Step 3: Implement dispatch, runner, and help.** Route `case "restore"` to `runRestore`. Mirror `runDelete`: require one ID/prefix, parse `--json`, open the service, invoke `service.Restore`, and return either `writeResult(stdout, "restore", task)` or `writeMutation`. Add exact help metadata with synopsis `workbook restore <id-or-prefix> [--json]`.

- [ ] **Step 4: Verify GREEN.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli -count=1`

  Expected: PASS.

- [ ] **Step 5: Commit.**

```sh
git add internal/cli/run.go internal/cli/flags.go internal/cli/run_test.go internal/cli/flags_test.go
git commit -m "feat: add task restore command"
```

### Task 3: Add web mutation and deleted-list boundaries

**Files:**

- Modify: `internal/webui/handler.go`
- Modify: `internal/webui/handler_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**

- Produces: `TaskDeleter` and `TaskRestorer` callbacks.
- Produces: `DELETE /api/tasks/{id}`, `POST /api/tasks/{id}/restore`, and `GET /api/tasks?deleted=true`.
- Consumes: `core.Service.Delete`, `core.Service.Restore`, and `core.Service.List(ctx, core.ListFilter{All: true})`.

- [ ] **Step 1: Write failing handler and service-wiring tests.** Add DELETE and restore route tests whose callbacks assert their received ID and whose response is decoded as `TaskMutationDocument`. Test GET on delete, PATCH on restore, and nested unmatched routes for versioned 405 responses and exact Allow headers. With one active and one tombstoned fixture, assert ordinary `GET /api/tasks` returns only active data whereas `?deleted=true` returns only tombstones. In `run_test.go`, delete then restore through the server and prove `workbook show` sees `deleted: false`.

- [ ] **Step 2: Verify RED.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui ./internal/cli -run 'TestHandler(DeletesTask|RestoresTask|ListsDeletedTasks|RejectsDeleteRestoreMethods)|TestRunServe.*(Deletes|Restores)' -count=1`

  Expected: FAIL because the routes and callbacks are absent.

- [ ] **Step 3: Implement the narrow HTTP boundary.** Add `TaskDeleter` and `TaskRestorer` to the handler constructor and struct. Register the two exact mutation routes, extend `allowedMethod`, and parse the restore path before the generic task path. Let the CLI’s web lister obtain all tasks for `deleted=true` and have the handler select only tombstones; retain active-only results normally. Wire delete and restore closures directly to their core service methods, and update all test fixtures with unexpected callbacks.

```go
type TaskDeleter func(context.Context, string) (core.Task, error)
type TaskRestorer func(context.Context, string) (core.Task, error)

func (handler *handler) restoreTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" { id = taskRestorePathID(r.URL.Path) }
	task, err := handler.restore(r.Context(), id)
	if err != nil { handler.writeError(w, err); return }
	handler.writeTaskMutation(w, task)
}
```

- [ ] **Step 4: Verify GREEN.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui ./internal/cli -count=1`

  Expected: PASS.

- [ ] **Step 5: Commit.**

```sh
git add internal/webui/handler.go internal/webui/handler_test.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: add web task deletion routes"
```

### Task 4: Render deletion and restoration and document it

**Files:**

- Modify: `internal/webui/assets/index.html`
- Modify: `internal/webui/handler_test.go`
- Modify: `README.md`

**Interfaces:**

- Consumes: the deleted-list and mutation APIs from Task 3.
- Produces: `GET /deleted` client route, a detail-form Delete button, and Restore buttons for tombstoned task cards.

- [ ] **Step 1: Write failing rendered-route tests.** Extend the embedded-script harness to navigate to an active detail view and assert a Delete button. Simulate successful DELETE and assert navigation to `/deleted`; render the deleted route from a tombstone fixture, click Restore, and assert POST to the full-ID restore URL then navigation to `/tasks/<full-id>`. Add an empty view assertion with a Board link and failures that preserve the route and show the server error in its status region.

- [ ] **Step 2: Verify RED.**

  Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/webui -run 'TestHandler(Client.*(Delete|Deleted|Restore)|RendersDeletedRoute)' -count=1`

  Expected: FAIL because the deleted route and controls are absent.

- [ ] **Step 3: Implement the client behavior.** Serve the shell at `GET /deleted`, extend `route` with a `deleted` view, add a native `type="button"` Delete control in detail mode, and call the existing mutation helper with DELETE before navigating to `/deleted`. Add a `deletedView` helper that fetches `/api/tasks?deleted=true`, renders IDs/titles/Restore buttons, and after a successful `POST /api/tasks/<encoded-id>/restore` navigates to that task detail. Reuse the visible status error behavior; do not add a modal.

```js
async function restoreTask(task, result) {
  try {
    await mutateTask("POST", "/api/tasks/" + encodeURIComponent(task.id) + "/restore");
    navigate("/tasks/" + encodeURIComponent(task.id));
  } catch (error) {
    text(result, error.message);
  }
}
```

- [ ] **Step 4: Document and verify all behavior.** Update README’s implemented command list, web route table, and local web-board description for `restore`, `/deleted`, DELETE, POST restore, and the tombstone rule. Then run:

```sh
gofmt -w internal/core/operation.go internal/core/operation_test.go internal/core/service.go internal/core/service_test.go internal/cli/run.go internal/cli/run_test.go internal/cli/flags.go internal/cli/flags_test.go internal/webui/handler.go internal/webui/handler_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
git diff --check
```

  Expected: formatting is clean, the complete suite passes, and whitespace validation succeeds.

- [ ] **Step 5: Commit.**

```sh
git add internal/webui/assets/index.html internal/webui/handler_test.go README.md docs/superpowers/plans/2026-07-27-task-restore-web-deletion.md
git commit -m "feat: restore deleted tasks from web"
```

