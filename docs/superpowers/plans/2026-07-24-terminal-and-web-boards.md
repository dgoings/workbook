# Terminal and Web Boards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add responsive terminal list/board views and an embedded, read-only browser Kanban while preserving Workbook's existing Git-backed task and JSON contracts.

**Architecture:** `core.Service.List` remains authoritative for membership and ordering. A small pure `internal/presentation` package groups that ordered slice into five columns and computes actionable unique ID prefixes; isolated terminal and web implementations consume that same view model. The CLI owns flag parsing, terminal detection, repository/service construction, listener startup, and process-level signal context.

**Tech Stack:** Go 1.26, the standard library, `golang.org/x/term` v0.45.0 for terminal detection, embedded HTML/CSS/JavaScript, `net/http`, and `httptest`.

## Global Constraints

- Canonical task storage, refs, operation packs, checkpoints, compare-and-swap writes, and error categories must not change.
- `core.Service.List` supplies the ordered, non-tombstoned task slice; renderers preserve that order and do not independently sort it.
- Status columns are exactly `backlog`, `ready`, `in-progress`, `blocked`, and `done`, in that order.
- Human IDs use the shortest unique leading prefix in the rendered set, with at least the project key, hyphen, and eight ULID characters; JSON and HTTP task data always use full IDs.
- `workbook board --wide` and `--narrow` are mutually exclusive; without either, interactive widths at least 140 use wide mode and every other output uses narrow mode.
- Terminal output is printable ASCII with no required ANSI color or Unicode box drawing.
- Human output may truncate with ASCII `...`; JSON and HTTP data never truncate.
- `workbook serve` defaults to `127.0.0.1:7331`, embeds every asset, exposes only `GET /`, `GET /api/tasks`, and `GET /healthz`, and has no mutation path.
- Browser polling is every two seconds and preserves the last good board on errors.
- The server remains foreground-only and shuts down cleanly when its context is cancelled.
- Tests use temporary repositories, in-memory task sources, `httptest`, and ephemeral loopback listeners; they never mutate the real `refs/workbook/tasks/*` namespace.
- Every behavior follows strict red-green-refactor TDD. Before writing each test, name the production break it catches and derive expected values independently.
- The two real dogfood tasks are updated only by the controller, not by implementation or review agents.
- Run Go tests, vet, and builds with `GOCACHE=/private/tmp/workbook-go-cache` because the agent sandbox cannot write the default user cache.

---

### Task 1: Shared Board Presentation Model

**Execution:** Complete and review this foundation before creating the two parallel feature worktrees.

**Files:**
- Create: `internal/presentation/board.go`
- Create: `internal/presentation/board_test.go`

**Interfaces:**
- Consumes: ordered `[]core.Task` from `core.Service.List`.
- Produces:

```go
package presentation

type TaskView struct {
	Task     core.Task
	IDPrefix string
}

type Column struct {
	Status core.Status
	Label  string
	Tasks  []TaskView
}

type Board struct {
	Columns []Column
}

func TaskViews(tasks []core.Task) []TaskView
func NewBoard(tasks []core.Task) Board
```

- `TaskViews` preserves input order and copies each full `core.Task`.
- `NewBoard` always returns five columns, including empty ones.
- Labels are exactly `Backlog`, `Ready`, `In progress`, `Blocked`, and `Done`.

- [ ] **Step 1: Write failing unique-prefix and order tests**

Create literal task fixtures whose IDs diverge before and after the minimum
prefix. Cover:

```go
func TestTaskViewsUseShortestActionableUniquePrefixes(t *testing.T)
func TestNewBoardPreservesInputOrderAndIncludesEmptyColumns(t *testing.T)
```

The first test must assert literal prefixes:

```go
tasks := []core.Task{
	{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"},
	{ID: "WB-01ARZ3NDF0TSV4RRFFQ69G5FAW"},
	{ID: "WB-01BXZ3NDEKTSV4RRFFQ69G5FAX"},
}
want := []string{
	"WB-01ARZ3NDE",
	"WB-01ARZ3NDF",
	"WB-01BXZ3ND",
}
```

Also cover identical minimum prefixes, a single task, different project-key
lengths, and full-ID fallback. For every returned prefix, assert that exactly
one input ID has that prefix.

- [ ] **Step 2: Run the presentation tests and verify RED**

Run:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/presentation -run 'TestTaskViews|TestNewBoard' -count=1
```

Expected: FAIL because the package or exported functions do not exist.

- [ ] **Step 3: Implement the minimal view model**

Use the fixed status definition:

```go
var columnDefinitions = [...]struct {
	status core.Status
	label  string
}{
	{core.StatusBacklog, "Backlog"},
	{core.StatusReady, "Ready"},
	{core.StatusInProgress, "In progress"},
	{core.StatusBlocked, "Blocked"},
	{core.StatusDone, "Done"},
}
```

For each ID:

1. find the byte index immediately after the first `-`;
2. start at eight ULID characters beyond that index, capped at full length;
3. compare against every other canonical ASCII ID;
4. extend one byte at a time until the prefix is unique; and
5. use the full ID if necessary.

Do not mutate or sort the input slice. Group `TaskView` values by status by
walking the task views once and appending to the matching fixed column.

- [ ] **Step 4: Run focused and core tests**

Run:

```sh
gofmt -w internal/presentation/board.go internal/presentation/board_test.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/presentation -count=1
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/core -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the foundation**

```sh
git add internal/presentation/board.go internal/presentation/board_test.go
git commit -m "feat: add shared board presentation model"
```

---

### Task 2: Responsive Terminal Table and ASCII Board

**Execution:** Run in the terminal feature worktree in parallel with Task 3,
starting from the reviewed Task 1 commit.

**Required skills:** Read and follow `superpowers:test-driven-development` and
its `writing-good-tests.md` reference before editing.

**Files:**
- Create: `internal/terminalui/render.go`
- Create: `internal/terminalui/render_test.go`
- Create: `internal/cli/terminal.go`
- Create: `internal/cli/terminal_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `presentation.TaskViews`, `presentation.NewBoard`, and ordered
  `[]core.Task`.
- Produces:

```go
package terminalui

type Layout uint8

const (
	LayoutWide Layout = iota + 1
	LayoutNarrow
)

func RenderList(w io.Writer, tasks []core.Task, width int) error
func RenderBoard(w io.Writer, board presentation.Board, layout Layout, width int) error
```

CLI-only terminal detection:

```go
const wideBoardMinimum = 140
const nonInteractiveWidth = 100

type fileDescriptor interface {
	Fd() uintptr
}

func terminalWidth(output io.Writer) (int, bool)
```

`terminalWidth` type-asserts `fileDescriptor`, calls
`term.IsTerminal(int(fd))`, then `term.GetSize(int(fd))`. Any failed assertion,
nonterminal descriptor, nonpositive width, or `GetSize` error returns `(0,
false)`.

- [ ] **Step 1: Write failing pure-renderer golden tests**

Write literal expected strings for:

```go
func TestRenderListProducesCompactDeterministicTable(t *testing.T)
func TestRenderListTruncatesOnlyHumanFields(t *testing.T)
func TestRenderBoardWideGolden(t *testing.T)
func TestRenderBoardNarrowGolden(t *testing.T)
func TestRenderBoardShowsAllEmptyColumns(t *testing.T)
```

The wide board format is:

```text
+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+
| Backlog (1)              | Ready (0)                | In progress (0)          | Blocked (0)              | Done (0)                 |
+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+
| WB-01ARZ3ND [H]          |                          |                          |                          |                          |
| Plan storage             |                          |                          |                          |                          |
| git,poc                  |                          |                          |                          |                          |
+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+
```

At width 140, each content cell is 26 bytes and the overall rendered line may
be narrower than 140 because integer division discards the remainder.

The narrow format is:

```text
BACKLOG (1)
-----------
WB-01ARZ3ND [high] Plan storage
  labels: git, poc

READY (0)
---------
(empty)
```

Continue through all five sections and end with exactly one newline. Empty
repositories still show all five sections. Truncation uses `...`, never splits
the ID or priority, and applies only to title/labels.

- [ ] **Step 2: Run renderer tests and verify RED**

Run:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/terminalui -count=1
```

Expected: FAIL because the renderer package does not exist.

- [ ] **Step 3: Implement the pure renderer**

Implement small helpers in `render.go`:

```go
func fit(value string, width int) string
func pad(value string, width int) string
func priorityMarker(priority core.Priority) string
func renderWideBoard(w io.Writer, board presentation.Board, width int) error
func renderNarrowBoard(w io.Writer, board presentation.Board, width int) error
```

Use byte-width ASCII formatting; task IDs and current validated task fields are
ASCII-safe for layout, while non-ASCII title bytes may be truncated only at a
valid UTF-8 boundary using `utf8.DecodeRuneInString`. Return writer errors
instead of discarding them. Use `tabwriter` only for the list table if its
spacing remains deterministic in the golden tests.

- [ ] **Step 4: Run renderer tests and verify GREEN**

Run:

```sh
gofmt -w internal/terminalui/render.go internal/terminalui/render_test.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/terminalui -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing terminal-detection and CLI board tests**

Add tests for:

```go
func TestTerminalWidthRejectsNonFileWriters(t *testing.T)
func TestRunBoardDefaultsToNarrowForBufferedOutput(t *testing.T)
func TestRunBoardHonorsWideAndNarrowOverrides(t *testing.T)
func TestRunBoardRejectsConflictingLayoutFlags(t *testing.T)
func TestRunBoardJSONIsCompleteAndUntruncated(t *testing.T)
func TestRunListUsesResponsiveRendererWithoutChangingJSON(t *testing.T)
```

The CLI tests must initialize a real temporary repository, create tasks across
statuses, and assert visible membership/order rather than merely checking a
header. The JSON test decodes a `workbook.result` envelope whose command is
`board`, compares full IDs/titles/labels, and proves no `...` entered task data.

- [ ] **Step 6: Run CLI tests and verify RED**

Run:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/cli -run 'TestTerminalWidth|TestRunBoard|TestRunListUsesResponsive' -count=1
```

Expected: FAIL because `board`, layout flags, and terminal detection are absent.

- [ ] **Step 7: Add terminal dependency and CLI integration**

Add `golang.org/x/term v0.45.0` using:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go get golang.org/x/term@v0.45.0
```

If module download is blocked by the sandbox or network policy, request the
needed command approval; do not silently substitute another version.

Extend `commandSchemas`:

```go
"board": {
	flags: map[string]flagKind{
		"wide":   boolFlag,
		"narrow": boolFlag,
		"json":   boolFlag,
	},
},
```

Add `board [--wide | --narrow] [--json]` to usage and dispatch it from `Run`.
Implement:

```go
func runBoard(ctx context.Context, args []string, cwd string, stdout io.Writer) error
```

Behavior:

1. parse `wide`, `narrow`, and `json`;
2. reject both layout flags with
   `cannot use --wide with --narrow`;
3. call the existing service with `core.ListFilter{}`;
4. emit full tasks through `writeResult(stdout, "board", tasks)` in JSON mode;
5. otherwise build `presentation.NewBoard(tasks)`;
6. explicit flags win;
7. absent flags use wide only when measured width is at least 140;
8. absent/unmeasurable width uses narrow with width 100; and
9. propagate renderer writer errors as `operational`.

Replace `writeList`'s body with `terminalui.RenderList`, using measured width
when available and 100 otherwise. Preserve `list --json` unchanged.

- [ ] **Step 8: Run focused and full terminal-track verification**

Run:

```sh
gofmt -w internal/cli/*.go internal/terminalui/*.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/terminalui ./internal/cli -count=1
env GOCACHE=/private/tmp/workbook-go-cache go test ./... -count=1
env GOCACHE=/private/tmp/workbook-go-cache go vet ./...
env GOCACHE=/private/tmp/workbook-go-cache go build -o /tmp/workbook-terminal ./cmd/workbook
```

Expected: all commands exit 0 with no warnings.

- [ ] **Step 9: Commit the terminal feature**

```sh
git add go.mod go.sum internal/terminalui internal/cli
git commit -m "feat: render responsive terminal boards"
```

The implementer writes its report with red/green command evidence, golden-output
decisions, commit IDs, and any integration concerns.

---

### Task 3: Embedded Read-only Web Kanban

**Execution:** Run in the web feature worktree in parallel with Task 2, starting
from the reviewed Task 1 commit.

**Required skills:** Read and follow `superpowers:test-driven-development`,
`writing-good-tests.md`, and `frontend-design:frontend-design` before editing.

**Files:**
- Create: `internal/webui/handler.go`
- Create: `internal/webui/handler_test.go`
- Create: `internal/webui/server.go`
- Create: `internal/webui/server_test.go`
- Create: `internal/webui/assets/index.html`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run_test.go`
- Modify: `cmd/workbook/main.go`

**Interfaces:**
- Consumes: `presentation.NewBoard`, `core.Task`, existing typed core errors, and
  the list operation injected by CLI.
- Produces:

```go
package webui

type TaskLister func(context.Context) ([]core.Task, error)

type TasksDocument struct {
	Format  string      `json:"format"`
	Version int         `json:"version"`
	Tasks   []core.Task `json:"tasks"`
}

type HealthDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type ErrorBody struct {
	Category core.Category `json:"category"`
	Message  string        `json:"message"`
}

type ErrorDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

func NewHandler(list TaskLister) http.Handler
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error
```

CLI construction:

```go
func runServe(
	ctx context.Context,
	args []string,
	cwd string,
	stdout io.Writer,
	stderr io.Writer,
) error
```

`runServe` writes no normal stdout. It opens the listener, logs
`Workbook board: http://<effective-address>\n` to stderr, and calls
`webui.Serve`.

- [ ] **Step 1: Write failing HTTP route and envelope tests**

Use an in-memory `TaskLister` returning ordered literal tasks and test:

```go
func TestHandlerServesBoardTasksAndHealth(t *testing.T)
func TestHandlerRefreshesTasksOnEveryAPIRequest(t *testing.T)
func TestHandlerRejectsUnknownRoutesAndMutationMethods(t *testing.T)
func TestHandlerMapsTaskErrorsToVersionedErrorDocuments(t *testing.T)
func TestHandlerEscapesHostileTaskContent(t *testing.T)
```

Assert exact health JSON:

```json
{"format":"workbook.health","version":1,"status":"ok"}
```

Assert exact task envelope fields and full task values. HTTP status mapping:

```text
invalid-invocation, validation -> 400
not-found                     -> 404
not-initialized, stale-write  -> 409
corrupt-data, operational     -> 500
```

Assert `Content-Type`, `X-Content-Type-Options: nosniff`, CSP, `Allow: GET`,
and that hostile values such as `<img src=x onerror=alert(1)>` never appear as
executable raw markup.

- [ ] **Step 2: Run handler tests and verify RED**

Run:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/webui -run TestHandler -count=1
```

Expected: FAIL because the web package does not exist.

- [ ] **Step 3: Implement the handler and embedded page**

Use a dedicated `http.ServeMux` with Go 1.22 method-aware patterns:
`GET /{$}`, `GET /api/tasks`, and `GET /healthz`. The `{$}` anchor keeps the
root handler from swallowing unknown subpaths, while the method patterns
produce `405 Method Not Allowed` and `Allow: GET` for known paths.

Set this exact CSP on every response:

```text
default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'
```

When producing `ErrorDocument`, expose `err.Error()` for operational failures
so a local user sees the listener or repository cause. For every other typed
error, expose the stable public `TypedError.Message` and do not leak wrapped
implementation details.

The page's design direction is a **repository workbench**, not a generic SaaS
dashboard:

- canvas `#e9eef5` ("checkout");
- cards `#ffffff` ("paper");
- ink `#172033` ("graphite");
- line `#b9c6d8` ("index");
- primary `#2457d6` ("ref blue");
- warning `#b45309` and blocked `#b42318`;
- title/body stack: `ui-rounded, "Avenir Next", system-ui, sans-serif`;
- IDs/data stack: `ui-monospace, "SFMono-Regular", Menlo, monospace`;
- square-to-6px radii, tight vertical rhythm, and no gradients;
- signature element: each column header shows a quiet Git-like ref label
  `refs/workbook/status/<status>` on a thin blue rail;
- one 120ms opacity transition when refreshed, disabled by
  `prefers-reduced-motion`.

The embedded page must:

1. server-render the initial `presentation.Board`;
2. show all five columns and counts;
3. use `data-status` containers for safe DOM replacement;
4. poll `/api/tasks` with `cache: "no-store"` every 2000ms;
5. validate `format === "workbook.tasks"` and `version === 1`;
6. build task elements with `document.createElement` and `textContent`;
7. keep the old DOM when polling fails;
8. expose a stale banner containing `Task refresh failed`;
9. show a local last-updated time after success; and
10. stack columns below 900px.

Use `html/template` for initial rendering and `//go:embed assets/index.html`.
Keep all CSS and JavaScript inline so no additional asset route exists.

- [ ] **Step 4: Run handler tests and verify GREEN**

Run:

```sh
gofmt -w internal/webui/handler.go internal/webui/handler_test.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/webui -run TestHandler -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing server lifecycle and CLI tests**

Add:

```go
func TestServeStopsCleanlyWhenContextIsCancelled(t *testing.T)
func TestServeReturnsUnexpectedHTTPFailure(t *testing.T)
func TestRunServeRejectsInvalidArguments(t *testing.T)
func TestRunServeReportsListenerFailureAsOperational(t *testing.T)
```

For lifecycle coverage, create `net.Listen("tcp", "127.0.0.1:0")`, run `Serve`
in a goroutine, make a real health request, cancel the context, and require the
goroutine to return nil within two seconds. Do not use arbitrary sleeps; wait
for the health request before cancellation.

CLI argument cases:

- default address is `127.0.0.1:7331`;
- `--addr 127.0.0.1:0` is accepted;
- extra positionals and `--json` are rejected as invalid invocation; and
- an already occupied address returns `operational` with the listener cause.

- [ ] **Step 6: Run lifecycle and CLI tests and verify RED**

Run:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/webui ./internal/cli -run 'TestServe|TestRunServe' -count=1
```

Expected: FAIL because `Serve`, CLI dispatch, and signal context are absent.

- [ ] **Step 7: Implement graceful server and CLI integration**

`Serve` uses `http.Server{Handler: handler}` and:

1. starts `server.Serve(listener)` in a buffered result channel;
2. returns nil for `http.ErrServerClosed`;
3. on `ctx.Done()`, calls `Shutdown` with a fresh five-second timeout;
4. waits for the serve goroutine; and
5. returns shutdown or unexpected serve errors.

Add to `commandSchemas`:

```go
"serve": {
	flags: map[string]flagKind{
		"addr": stringFlag,
	},
},
```

Add `serve [--addr 127.0.0.1:7331]` to usage and dispatch it with both stdout
and stderr. `runServe`:

1. parses only `--addr`;
2. builds the read service;
3. defines a lister calling `service.List(ctx, core.ListFilter{})`;
4. creates `webui.NewHandler`;
5. opens `net.Listen("tcp", addr)`;
6. wraps listener failures with `CategoryOperational`;
7. logs the effective address to stderr; and
8. wraps unexpected server failures with `CategoryOperational`.

Change `cmd/workbook/main.go` to create a signal-aware context:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
os.Exit(cli.Run(ctx, os.Args[1:], ".", os.Stdout, os.Stderr))
```

- [ ] **Step 8: Critique and verify the rendered page**

Build a temporary binary and start it on an ephemeral loopback port. Capture a
browser screenshot if browser tooling is available; otherwise render the page
through `httptest` and inspect its DOM structure. Check:

- the ref-rail signature is specific to Workbook rather than generic Kanban;
- five columns fit without unreadably narrow cards on a typical laptop;
- IDs and priority are scannable before descriptions;
- hostile/long content cannot break layout;
- mobile stacking and keyboard focus are visible; and
- reduced-motion styling exists.

Remove any decorative element that does not improve task scanning. Record this
critique and any adjustment in the task report.

- [ ] **Step 9: Run focused and full web-track verification**

Run:

```sh
gofmt -w cmd/workbook/main.go internal/cli/*.go internal/webui/*.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/webui ./internal/cli -count=1
env GOCACHE=/private/tmp/workbook-go-cache go test ./... -count=1
env GOCACHE=/private/tmp/workbook-go-cache go vet ./...
env GOCACHE=/private/tmp/workbook-go-cache go build -o /tmp/workbook-web ./cmd/workbook
```

Expected: all commands exit 0 with no warnings.

- [ ] **Step 10: Commit the web feature**

```sh
git add cmd/workbook/main.go internal/webui internal/cli
git commit -m "feat: serve read-only web task board"
```

The implementer writes its report with red/green command evidence, HTTP route
coverage, visual self-critique, commit IDs, and integration concerns.

---

### Task 4: Integrate Both Tracks, Document Behavior, and Dogfood

**Execution:** Complete after both parallel task reviews pass.

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-22-workbook-initial-poc-design.md`
- Modify: `internal/cli/run.go` as required by conflict resolution
- Modify: `internal/cli/flags.go` as required by conflict resolution
- Modify: `internal/cli/output.go` as required by conflict resolution
- Modify: `internal/cli/run_test.go` as required by conflict resolution
- Test: all repository test packages

**Interfaces:**
- Consumes: reviewed Task 2 and Task 3 commits.
- Produces: one integration branch containing both commands and accurate current
  documentation.

- [ ] **Step 1: Merge terminal, then integrate web**

Fast-forward or cherry-pick the reviewed terminal commits first. Rebase or
cherry-pick the reviewed web commits afterward.

Resolve shared files by retaining both complete command schemas, dispatch cases,
usage lines, JSON-intent behavior, and test sets. Never resolve by choosing one
side wholesale.

- [ ] **Step 2: Run focused CLI tests after conflict resolution**

Run:

```sh
gofmt -w cmd/workbook/*.go internal/cli/*.go
env GOCACHE=/private/tmp/workbook-go-cache go test ./internal/cli -run 'TestRunBoard|TestRunServe|TestRunJSONIntent|TestRunCRUD' -count=1
```

Expected: PASS. If a conflict resolution breaks behavior, add a failing
integration regression before changing production code.

- [ ] **Step 3: Update current-behavior documentation**

Update README's status and command sections to list terminal and web boards as
implemented:

```text
workbook board [--wide | --narrow] [--json]
workbook serve [--addr 127.0.0.1:7331]
```

Document:

- automatic 140-column wide selection and stacked noninteractive output;
- the card fields and actionable ID prefixes;
- foreground/background fish examples;
- exact web routes and read-only guarantee;
- two-second polling and embedded assets; and
- that ordering/dependencies, SQLite, remote sync, and Homebrew remain proposed.

Update the original POC spec's environment note that incorrectly says Go is not
installed and mark terminal/web delivery steps as implemented without claiming
the remaining roadmap is complete.

- [ ] **Step 4: Verify real dogfood presentation without mutating tasks**

Build:

```sh
env GOCACHE=/private/tmp/workbook-go-cache go build -o /tmp/workbook-boards ./cmd/workbook
```

Run from the Workbook repository:

```sh
/tmp/workbook-boards list
/tmp/workbook-boards board --wide
/tmp/workbook-boards board --narrow
/tmp/workbook-boards board --json
```

Expected: the same five current tasks appear in core order; human prefixes are
actionable; JSON retains full IDs and descriptions.

Start the server through an integration test or ephemeral listener and probe:

```text
GET /             -> 200 text/html
GET /api/tasks    -> 200 application/json
GET /healthz      -> 200 application/json
POST /api/tasks   -> 405 with Allow: GET
```

- [ ] **Step 5: Run the complete repository gate**

Run:

```sh
gofmt -w cmd/workbook/*.go internal/cli/*.go internal/presentation/*.go internal/terminalui/*.go internal/webui/*.go
env GOCACHE=/private/tmp/workbook-go-cache go test -count=1 ./...
env GOCACHE=/private/tmp/workbook-go-cache go vet ./...
env GOCACHE=/private/tmp/workbook-go-cache go build -o /tmp/workbook-boards-final ./cmd/workbook
git diff --check
```

Expected: all commands exit 0 with no warnings.

- [ ] **Step 6: Commit integration documentation or fixes**

```sh
git add README.md docs/superpowers/specs/2026-07-22-workbook-initial-poc-design.md cmd internal go.mod go.sum
git commit -m "docs: document terminal and web boards"
```

If conflict resolution required production changes, commit those separately as
`fix: integrate board commands` before the documentation commit.

- [ ] **Step 7: Complete task-scoped and whole-branch review**

Review Task 2 and Task 3 against their exact briefs before integration. After
Task 4, generate one whole-branch review package from the implementation base
through integration HEAD. The final reviewer checks:

- spec compliance and no unimplemented README claims;
- presentation parity across terminal, API, and initial browser HTML;
- CLI flag/JSON-intent integration;
- HTTP method/path safety and escaped content;
- graceful shutdown and listener errors;
- no Git storage/ref regressions; and
- meaningful TDD coverage rather than source-text assertions.

- [ ] **Step 8: Update dogfood task state after final verification**

Only the controller performs these real repository mutations:

```sh
workbook update WB-01KY899M21C4JD7X4H2K9TSZ42 --status done --json
workbook update WB-01KY899RKNBG69PQZ2PR970MQC --status done --json
```

Before implementation dispatch, the controller similarly moves both tasks to
`in-progress`. Record the old and new ref heads and verify each new tree still
contains exactly `operation.json` and `state.json`.

- [ ] **Step 9: Refresh the user's installed binary**

After the reviewed integration branch is merged locally, run:

```fish
./scripts/install.sh
command -v workbook
workbook board
```

Expected: installation reports `$HOME/.local/bin/workbook`, fish resolves that
binary, and `workbook board` displays the completed tasks.
