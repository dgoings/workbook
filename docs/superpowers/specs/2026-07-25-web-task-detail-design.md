# Linkable Web Task Detail Design

## Goal

Add a linkable, editable web view for one Workbook task while preserving the
existing local `workbook serve` board and Git-backed task update model.

## Scope

- Board task cards navigate to `/tasks/<full-task-id>`.
- Direct navigation and refresh at `/tasks/<full-task-id>` serve the web app.
- The detail view edits title, description, status, priority, and labels.
- Save persists through the existing core service update operation and returns
  to the board.
- Back returns to the board without writing or retaining a draft.
- A missing task displays a clear not-found view with a board link.

The POC does not add authentication, a hosted backing service, draft storage,
leave-page confirmation, task creation or deletion in the browser, or changes
to drag-and-drop status updates.

## Routes and API

`GET /` and `GET /tasks/<id>` return the same application shell. The client
uses the location to choose the board or detail view, so task URLs work after a
browser reload and can later be served by a hosted task-list application.

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

## Client behavior

Each rendered task card contains a normal task URL and preserves drag behavior.
Clicking a card, or activating it by keyboard, navigates to that URL. The app
intercepts same-origin navigation with `history.pushState`, handles browser
back/forward with `popstate`, and fetches current tasks after each route change.

The detail view shows the full task ID and a form containing the editable task
fields. Status and priority use the canonical choices already enforced by core;
labels are entered as comma-separated values and are trimmed, empty values are
removed, and repeated labels are left to core validation. Save sends only the
form values to the update endpoint. On success, the browser returns to `/` and
refreshes the board. On failure, the form remains unchanged and displays the
server error. Back performs only navigation, discarding client-only changes.

## Error handling and accessibility

The task-detail form uses explicit labels, a visible error/status region, and
native buttons. The Back control is a link to the board. Existing Content
Security Policy restrictions continue to allow only same-origin API requests.
The API remains authoritative for validation; client validation is limited to
constructing a well-formed request.

## Verification

Handler tests cover deep-link shell delivery, full-field update routing,
validation and malformed-request responses, and preservation of the status
endpoint. Rendered-page assertions cover full task URLs, the detail route
client behavior, editable controls, save/back behavior, and error/not-found
states. CLI server integration verifies that a detail-save request persists the
updated task through `workbook show`.
