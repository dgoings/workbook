# Web Task Deletion and Restore Design

## Goal

Let a user tombstone a task from its local web detail view, browse tombstoned
tasks, and explicitly restore one without weakening the rule that a tombstoned
task is otherwise immutable.

## Scope

- Add `workbook restore <id-or-prefix> [--json]`.
- Add an append-only `task.restore` operation that is valid only after a task
  has been tombstoned.
- Keep updates, moves, dependency changes, and repeated deletes rejected for
  tombstoned tasks.
- Add a `DELETE /api/tasks/<id>` endpoint backed by the existing core delete
  operation and a `POST /api/tasks/<id>/restore` endpoint backed by restore.
- Add a `/deleted` web route that lists only tombstoned tasks and provides a
  Restore control for each.
- Add a Delete control to the existing active-task detail form.

The POC does not add a confirmation dialog, permanent browser deletion,
selective history rewrites, or edits to a task while it remains tombstoned.

## Operation and Core Service

`task.restore` is a versioned, payload-free operation. Applying it requires a
previous valid state whose task is tombstoned and produces an identical task
state except `deleted` becomes `false`. It is invalid for an active task. The
operation remains part of the immutable task history, so delete/restore cycles
are inspectable and synchronization continues to use ordinary fast-forward
Git commits.

`core.Service.Restore` resolves the ID prefix, reads the task tip, rejects an
active task with a validation error, and writes the one-operation mutation.
The existing tombstone validation remains in force for every other service
mutation path. The CLI adds `restore` with the same output contract as delete.

## Web Routes and Behavior

The handler serves the application shell at `GET /deleted` and allows it only
for GET. `GET /api/tasks?deleted=true` returns only tombstoned tasks; the
existing unfiltered endpoint continues to return active tasks. This keeps the
browser data boundary explicit and lets the deleted view avoid receiving active
tasks.

`DELETE /api/tasks/<id>` invokes the existing delete callback. `POST
/api/tasks/<id>/restore` invokes a new restore callback. Both successful
responses use the existing versioned task-mutation document. The handler keeps
strict JSON and method behavior: these payload-free routes reject request
bodies with multiple or malformed JSON values only where a body is accepted;
method mismatches return the existing versioned 405 response with an accurate
Allow header.

The active task form includes a native Delete button. A successful deletion
navigates to `/deleted` and refreshes that list. `/deleted` displays a clear
empty state, a Board link, and one Restore button per tombstoned task. A
successful restore navigates to `/tasks/<full-id>` and refreshes the active
task detail view. Failures preserve the current route and show the server error
in the existing visible status area.

## Verification

Core tests prove `task.restore` is accepted only after a tombstone, preserves
the task data, and does not permit other mutations while tombstoned. CLI tests
prove restore accepts full IDs and prefixes and emits the normal result
envelope. Handler and server integration tests prove deletion and restoration
persist through the Git-backed service, reject wrong methods, and keep the
normal active-task API result isolated from the deleted list. Rendered-page
tests exercise the Delete control, deleted route, restore navigation, empty
state, and failure messages.
