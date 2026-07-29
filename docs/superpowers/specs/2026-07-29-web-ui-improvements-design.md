# Responsive Web Board Improvements Design

## Goal

Make the existing local web board easier to scan across phone, laptop, and
large desktop viewports without changing Workbook's task model, API, routing,
or dependency footprint.

## Scope

- Keep the six canonical workflow columns visible in their existing order.
- Let the board use the available width on large screens.
- Keep a readable column width and scroll the board horizontally on smaller
  screens instead of stacking the columns.
- Constrain the board to the available viewport height and scroll each
  column's task list independently beneath its header.
- Clamp task-card descriptions to six lines while preserving the complete
  description in task data and on the detail route.
- Remove the board section for unrecognized statuses.
- Refresh task data every second instead of every two seconds.

This increment does not change the canonical statuses, task validation, API
documents, detail forms, drag-and-drop semantics, task ordering, or storage.
It adds no JavaScript or CSS dependencies.

## Layout

The application shell becomes a viewport-height flex column. The existing
header keeps its natural height, while `main` takes the remaining height and
retains overflow behavior for non-board routes.

The board view fills the available `main` content area. Its canonical columns
remain a six-track CSS grid:

- above the existing 900-pixel responsive breakpoint, each track has a
  readable minimum width and shares all remaining width equally;
- at or below 900 pixels, each track is sized for one readable card column and
  the grid scrolls horizontally; and
- the board is no longer capped by centered 92-rem content gutters, so wide
  screens produce wider columns rather than wider outer margins.

Each column is a two-row grid containing its existing header followed by a
minimum-zero task-list row. The task list scrolls vertically when its cards
exceed the available viewport height. Column headers remain visible without
requiring sticky positioning, and one long column does not lengthen the page
or the other columns.

The application keeps the existing narrow-screen header and form adjustments.
Task forms and the deleted-task view continue to use `main` scrolling when
their content is taller than the viewport.

## Card descriptions

Board card descriptions use a six-line CSS clamp with overflow hidden. This is
presentation-only: server responses, client task values, form fields, and task
detail routes retain the full description. Cards without descriptions retain
their current compact spacing.

## Canonical-status board

The server-rendered unrecognized-status section is removed. The client board
renderer builds groups only for the six canonical status lists and skips tasks
whose status is not canonical, so a forward-version or corrupt task cannot
break polling.

Unknown-status tasks remain in the versioned API response and can still be
opened directly by full task ID. The existing detail form continues to require
an explicit canonical status before such a task can be saved. This preserves
the defensive recovery path without dedicating board space to a state the
current CLI rejects.

## Refresh behavior

The client keeps its existing immediate refresh during startup and schedules
subsequent refreshes at a 1,000-millisecond interval. Successful refreshes
continue to update the visible timestamp; failures continue to leave the last
successful board visible with the stale-data message.

## Testing and verification

Changes follow test-driven development:

- a rendered-handler test proves that the board exposes only canonical status
  lists even when the task source includes an unknown status;
- the executable client harness captures the registered interval and proves
  that refresh polling uses 1,000 milliseconds;
- executable client coverage proves that an unknown-status task is ignored
  without breaking canonical list rendering;
- focused `internal/webui` tests cover the complete embedded page behavior;
  and
- the full Go test suite, `go vet`, a production build, and `git diff --check`
  verify the branch before publication.

Browser verification uses the real local server at narrow and wide viewport
sizes. It confirms horizontal board scrolling on a narrow viewport, full-width
column expansion on a wide viewport, a six-line description clamp, and
independent vertical scrolling in a densely populated column.
