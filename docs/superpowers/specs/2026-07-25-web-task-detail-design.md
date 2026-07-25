# Linkable Web Task Detail Design

## Goal

Add linkable, editable web task views for creating and updating Workbook tasks
while preserving the existing local `workbook serve` board and Git-backed task
operation model.

## Scope

- Board task cards navigate to `/tasks/<full-task-id>`.
- Each board column has a New Task button that navigates to `/tasks/new` with
  that column's status pre-filled.
- Direct navigation and refresh at `/tasks/<full-task-id>` serve the web app.
- `/tasks/new` renders the same form for a new task, with a valid optional
  status query parameter pre-filled.
- The shared form edits title, description, status, priority, and labels.
- Save creates or updates through the existing core service operations and
  returns to the board.
- Back returns to the board without writing or retaining a draft.
- A missing task displays a clear not-found view with a board link.

The POC does not add authentication, a hosted backing service, draft storage,
leave-page confirmation, task deletion in the browser, or changes to
drag-and-drop status updates.

## Routes and API

`GET /`, `GET /tasks/new`, and `GET /tasks/<id>` return the same application
shell. The client uses the location to choose the board, new-task, or detail
view, so task URLs work after a browser reload and can later be served by a
hosted task-list application.

`GET /api/tasks` remains the source for board and detail reads. The detail view
finds its full task ID in that document; unknown IDs render the not-found view.

`PATCH /api/tasks/<id>` accepts a versioned JSON request with optional
`title`, `description`, `status`, `priority`, and `labels` fields. It maps to
`core.UpdateInput` and returns the existing `workbook.task-mutation` v1
envelope. The endpoint rejects malformed JSON, unknown fields, multiple JSON
values, and core validation errors using the existing versioned error document.

The existing `PATCH /api/tasks/<id>/status` endpoint stays available for board
drag-and-drop. Both mutation routes use the same underlying service update
operation, so they produce the same append-only Git task history.

`POST /api/tasks` accepts a versioned JSON request containing `title`,
`description`, `status`, `priority`, and `labels`. It maps to `core.CreateInput`
and returns `workbook.task-mutation` v1. It applies the same strict JSON and
versioned-error rules as the update route. The handler's create callback uses
the same service instance and ID/time sources as the CLI, so web-created tasks
are ordinary append-only task root commits.

## Client behavior

Each rendered task card contains a normal task URL and preserves drag behavior.
Each recognized status column includes a New Task link to `/tasks/new?status=`
with its canonical status value. Clicking a card, activating it by keyboard,
or following a New Task link navigates to that URL. The app intercepts
same-origin navigation with `history.pushState`, handles browser back/forward
with `popstate`, and fetches current tasks after each route change.

The task detail view shows the full task ID and a form containing the editable
task fields. The new-task view uses that same form with empty values and a
status selected from its query parameter when it is one of the canonical
statuses; an absent or invalid parameter selects Backlog. Status and priority
use the canonical choices already enforced by core; labels are entered as
comma-separated values and are trimmed, empty values are removed, and repeated
labels are left to core validation. Save sends the form values to
`POST /api/tasks` for a new task or `PATCH /api/tasks/<id>` for an existing task. On
success, the browser returns to `/` and refreshes the board. On failure, the
form remains unchanged and displays the server error. Back performs only
navigation, discarding client-only changes.

## Error handling and accessibility

The task-detail form uses explicit labels, a visible error/status region, and
native buttons. The Back control is a link to the board. Existing Content
Security Policy restrictions continue to allow only same-origin API requests.
The API remains authoritative for validation; client validation is limited to
constructing a well-formed request.

## Verification

Handler tests cover deep-link shell delivery, new-task shell delivery,
full-field update and create routing, validation and malformed-request
responses, and preservation of the status endpoint. Rendered-page assertions
cover full task URLs, status-prefilled New Task links, the board/detail/new
client routes, editable controls, save/back behavior, and error/not-found
states. CLI server integration verifies that detail-save and new-task-save
requests persist through `workbook show` and `workbook list`.
