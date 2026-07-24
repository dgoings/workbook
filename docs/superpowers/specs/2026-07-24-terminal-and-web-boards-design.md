# Workbook Terminal and Web Boards Design

## Summary

This increment makes Workbook useful for day-to-day human task review while
preserving the existing local-first, Git-native architecture. It adds:

- responsive human-readable terminal output;
- a five-column ASCII board through `workbook board`; and
- an embedded, read-only browser board through `workbook serve`.

The terminal and browser views consume the existing ordered results from
`core.Service.List`. They do not introduce a second task store, projection, or
ordering rule. Machine-readable output continues to expose full task data
without presentation truncation.

The two features are implemented concurrently in isolated worktrees, then
integrated and reviewed together. Terminal work merges first; the web branch is
rebased or conflict-resolved against that result.

## Goals

- Make the current dogfooded tasks easy to scan from an interactive terminal.
- Provide a board layout that remains readable in narrow terminals and pipes.
- Make every displayed abbreviated ID usable as an unambiguous CLI prefix for
  the tasks in that view.
- Provide a polished local browser board without requiring Node.js, external
  assets, a daemon, or a hosted service.
- Keep terminal, JSON, API, and browser task ordering consistent.
- Preserve the existing stable CLI error categories and JSON conventions.
- Keep the web surface strictly read-only.

## Non-goals

- task editing, dragging, or status changes in the browser;
- authentication or non-local hosting;
- a background daemon, PID files, or service management;
- WebSockets or server-sent events;
- remote ref synchronization or distributed claims;
- dependency-aware `workbook next`, reordering commands, or SQLite;
- configurable statuses, colors, columns, or polling intervals in this
  increment.

## Shared task and ordering contract

Both features call `core.Service.List` with the normal non-tombstone filter.
That service remains the only authority for task selection and ordering:

1. status: `backlog`, `ready`, `in-progress`, `blocked`, `done`;
2. priority: `high`, `medium`, `low`;
3. numeric rank; and
4. full task ID.

Renderers group the already ordered slice by status without independently
sorting it. Tombstoned tasks do not appear.

Human views abbreviate task IDs to the shortest unambiguous leading prefix
within the rendered task set, with a minimum of the project key, separator, and
eight ULID characters. If no shorter prefix is unique, the full ID is used.
This is deliberately a prefix rather than a suffix so it can be pasted into
`workbook show`, `workbook update`, or `workbook delete`.

JSON output and the HTTP API always return full IDs and complete task values.

## Terminal commands and layout

The CLI adds:

```text
workbook board [--wide | --narrow] [--json]
```

`--wide` and `--narrow` are mutually exclusive. They affect human output only.
`--json` returns the standard versioned `workbook.result` envelope with command
`board` and the complete ordered task array as its data.

Without an explicit layout:

1. if stdout is an interactive terminal, Workbook reads its width;
2. widths of 140 columns or more use the horizontal layout;
3. smaller widths use the stacked layout; and
4. noninteractive or unmeasurable output uses the stacked layout.

Tests and callers that need deterministic formatting use the explicit flags.
Terminal-size detection lives at the CLI boundary; pure renderer functions take
an explicit width/layout and write to an `io.Writer`.

### List table

Human `workbook list` remains the compact table command and keeps the existing
status, priority, and label filters. Its rendering moves behind the terminal
renderer so column sizing and title truncation are deterministic.

The table shows:

- actionable abbreviated ID;
- title;
- status;
- priority; and
- comma-separated labels.

Wide output allocates remaining space to the title and labels. Narrow output
preserves ID, status, and priority, truncates title and labels with ASCII `...`,
and never emits malformed columns. Redirected output uses the deterministic
narrow table. Existing JSON behavior is unchanged and never truncates.

### ASCII board

Wide mode renders five horizontal columns in the fixed status order. Narrow
mode renders five vertically stacked status sections in that same order. Empty
columns remain visible so the workflow shape is clear.

Each compact card shows:

- actionable abbreviated ID;
- priority;
- title; and
- labels when present.

The renderer uses printable ASCII separators and no mandatory ANSI color,
Unicode box drawing, or terminal-control sequences. Long titles and labels are
truncated only in human output. A card never splits an ID or priority value.

## Read-only web server

The CLI adds:

```text
workbook serve [--addr 127.0.0.1:7331]
```

The address defaults to loopback port 7331. The command runs in the foreground,
logs the effective listening address to stderr, and exits cleanly when its
context is cancelled by an interrupt. Users may background it with normal shell
process control:

```fish
workbook serve &
```

The implementation uses Go's HTTP server and embeds its HTML template, CSS, and
JavaScript into the executable. It requires no runtime asset directory and no
Node.js installation.

### Routes

The server exposes exactly:

```text
GET /             browser board
GET /api/tasks    current task data
GET /healthz      process health
```

Unknown paths return 404. Non-GET methods return 405 with an `Allow: GET`
header. Responses set explicit content types, disable MIME sniffing, and use a
content security policy that permits only the embedded page resources and
same-origin API calls.

`GET /healthz` returns a small versioned JSON health document without reading
task refs:

```json
{
  "format": "workbook.health",
  "version": 1,
  "status": "ok"
}
```

`GET /api/tasks` performs a fresh `core.Service.List` call on every request and
returns:

```json
{
  "format": "workbook.tasks",
  "version": 1,
  "tasks": []
}
```

The `tasks` array contains the complete existing `core.Task` JSON shape in core
ordering. Storage and validation errors return the existing versioned
`workbook.error` shape with an appropriate HTTP status and do not leak internal
filesystem paths for non-operational errors.

HTTP error status mapping is deterministic:

- invalid invocation or validation: 400;
- not found: 404;
- not initialized: 409;
- stale write: 409; and
- corrupt data or operational failure: 500.

If the initial task read for `GET /` fails, the server returns the browser page
with a visible load-error state and a 200 response so polling can recover
without requiring a reload. API failures still use the error envelope and
non-200 status.

### Browser board

`GET /` returns a server-rendered initial board so the page is useful before
JavaScript completes. Embedded JavaScript polls `/api/tasks` every two seconds
and replaces the board only after a successful, validated response.

The page uses a restrained, high-density Kanban layout:

- five status columns on wide screens;
- stacked status sections on narrow screens;
- a visible task count per column;
- cards with actionable ID prefix, priority badge, title, labels, and a
  two-line description excerpt;
- system fonts and local CSS only; and
- clear empty, loading, stale/error, and last-updated states.

Polling errors show a nonblocking stale-data banner while leaving the last
successful board visible. All task strings are escaped by the server template
and inserted by browser code with text APIs rather than raw HTML.

There are no mutation routes, forms, drag handlers, or editable controls.

## Component boundaries

A small shared `internal/presentation` package accepts the ordered
`[]core.Task` and produces a board view model with the five fixed columns,
task counts, and shortest unique ID prefixes. It preserves input order and has
no CLI, HTTP, terminal, or Git dependency. This foundation is committed before
the two feature worktrees branch so both implementations consume the same
presentation contract.

The terminal feature owns a package such as `internal/terminalui` containing
pure list and board rendering. CLI parsing and TTY detection remain under
`internal/cli`.

The web feature owns a package such as `internal/webui` containing:

- the embedded assets;
- the HTTP handler;
- versioned API response types; and
- a server runner that accepts a context, address, task-list function, and log
  writer.

The web package depends on core task values but not on Git plumbing or CLI flag
types. The CLI constructs the existing service and injects its list operation.
This permits handler tests to use real HTTP requests with deterministic
in-memory task sources.

Both feature branches may touch CLI dispatch, usage text, CLI integration
tests, and README status documentation. Those overlaps are expected integration
points rather than reasons to share a working directory.

## Error handling

- Unknown commands and invalid flags remain `invalid-invocation`.
- `board --wide --narrow` is an invalid invocation.
- Board and API task-loading failures preserve the existing typed error
  category.
- `serve` startup or listener failures are `operational` and include an
  actionable cause.
- A polling failure does not erase previously rendered tasks.
- Graceful shutdown treats context cancellation as successful termination;
  unrelated server failures remain operational errors.

## Testing

Every production behavior follows a failing-test, minimal-implementation,
passing-test cycle.

Terminal coverage includes:

- golden wide and narrow board output;
- empty columns and empty repositories;
- deterministic truncation and actionable unique prefixes;
- automatic noninteractive narrow selection;
- explicit layout overrides and mutual exclusion;
- list filters continuing to affect human and JSON output; and
- JSON remaining complete and untruncated.

HTTP coverage uses `httptest` and includes:

- all three successful GET routes and content types;
- exact versioned task/health envelopes;
- a changed task source appearing on the next API request;
- unknown-route 404 and non-GET 405 behavior;
- task-source error mapping;
- escaped hostile task content;
- embedded page bootstrap data and same-origin polling contract; and
- context-driven graceful shutdown plus listener failure.

Integration verification includes:

- `gofmt` on changed Go files;
- `go test -count=1 ./...`;
- `go vet ./...`;
- a clean production build;
- `workbook list`, `workbook board --wide`, and `workbook board --narrow`
  against this repository's dogfooded tasks;
- starting `workbook serve` on an ephemeral loopback port and probing all three
  routes; and
- confirming existing task refs are unchanged except for intentional status
  updates to the two implementation tasks.

## Parallel delivery and dogfooding

After the implementation plan is approved:

1. mark both Workbook tasks `in-progress`;
2. implement and review the small shared presentation view model on the
   integration branch;
3. create one isolated terminal worktree and one isolated web worktree from
   that same foundation commit;
4. dispatch one implementation agent per worktree;
5. require TDD, focused commits, a self-review report, and task-scoped review
   for each;
6. merge the terminal branch into the integration branch;
7. rebase or cherry-pick the web work, resolving only shared CLI/test/doc
   integration points;
8. run the whole-branch review and verification gate; and
9. mark each Workbook task `done` only after its integrated behavior passes.

The web task does not depend on the terminal task semantically. Integration
order is only a practical choice for resolving their small shared CLI surface.

## Acceptance criteria

- `workbook list` remains compatible and is readable in wide, narrow, and
  redirected human output.
- `workbook board` renders all five statuses horizontally or vertically with
  the same tasks and within-status order returned by core.
- Every abbreviated human ID uniquely resolves within the rendered set.
- Board JSON contains full, untruncated task data.
- `workbook serve` starts on the requested local address and shuts down
  gracefully.
- The browser board works without Node.js or external assets and refreshes task
  data every two seconds.
- Only the three documented GET routes exist; no mutation path is available.
- Terminal and browser boards present identical task membership and ordering.
- Existing CRUD, Git storage, JSON error contracts, and dogfooded task refs
  continue to pass their tests.
