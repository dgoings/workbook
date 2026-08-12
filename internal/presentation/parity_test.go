// Package presentation_test cross-checks the two boards Workbook renders.
//
// Parity is by construction today: the terminal renderer and the web API both
// consume internal/presentation, and each side is tested alone. Nothing fed one
// task set to both and compared what they present, so a renderer that started
// deriving a fact for itself — recomputing an ID prefix, counting dependencies
// again, formatting a priority from its own table — would agree with its own
// tests while the two boards showed a user different things about one task.
//
// The standing decision this file exists to hold: every task in the set a board
// is handed appears somewhere on that board, on both boards. A status this
// build has no column for is a presentation problem, never a reason to stop
// naming a task — Board.UnknownTasks is computed once here for exactly that,
// and a renderer that consumes Board.Columns without also consuming
// Board.UnknownTasks silently deletes tasks from the reader's view. Two Ready
// changes make that routine rather than exotic: per-project custom status
// columns, and removing the default Blocked status. Both leave one clone
// holding a status another clone does not know.
//
// So a new renderer, or a new column source, keeps the two rules
// TestBothBoardsNameATaskWhoseStatusHasNoColumn asserts: the unknown set is
// rendered, and it is rendered under its own heading rather than folded into a
// column that would misreport the task's status.
//
// The columns themselves are a project's own now, which is where the second
// standing decision lives: TestBothBoardsBuildTheProjectsOwnColumns feeds one
// vocabulary to both boards and requires the same columns, the same labels and
// the same card in the same place on each. Parity used to be free, because both
// boards read one fixed array of six statuses. It is not free any more, and a
// renderer that reintroduced a status list of its own would pass every test it
// owns while disagreeing with the other board about where a task is.
package presentation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
	"github.com/dgoings/workbook/internal/terminalui"
	"github.com/dgoings/workbook/internal/webui"
)

func parityTasks() []core.Task {
	return []core.Task{
		// The first two share every character a short prefix would cover, so
		// the prefix each board shows has to be lengthened identically.
		{
			ID: "WB-01J0000000000000000000AA",
			TaskData: core.TaskData{
				Title: "Shared prefix, first", Status: core.StatusReady, Priority: core.PriorityHigh,
				Labels: []string{"api", "docs"}, Rank: "1",
				Dependencies: []string{"WB-01J0000000000000000000CC", "WB-01J0000000000000000000DD"},
			},
		},
		{
			ID: "WB-01J0000000000000000000AB",
			TaskData: core.TaskData{
				Title: "Shared prefix, second", Status: core.StatusInProgress, Priority: core.PriorityLow,
				Labels: []string{"web"}, Rank: "2",
			},
		},
		{
			ID: "WB-01J0000000000000000000CC",
			TaskData: core.TaskData{
				Title: "Finished prerequisite", Status: core.StatusDone, Priority: core.PriorityMedium, Rank: "3",
			},
		},
		{
			ID: "WB-01J0000000000000000000DD",
			TaskData: core.TaskData{
				Title: "Unfinished prerequisite", Status: core.StatusBlocked, Priority: core.PriorityMedium, Rank: "4",
			},
		},
	}
}

func TestBothBoardsPresentTheSameFactsAboutOneTaskSet(t *testing.T) {
	tasks := parityTasks()
	board := presentation.NewBoard(tasks, core.Vocabulary{})
	document := webBoardDocument(t, tasks)

	if len(document.Presentation) != len(tasks) {
		t.Fatalf("web presentation covers %d tasks, want %d", len(document.Presentation), len(tasks))
	}
	web := make(map[string]webui.TaskPresentation, len(document.Presentation))
	for _, entry := range document.Presentation {
		web[entry.TaskID] = entry
	}
	webTasks := make(map[string]core.Task, len(document.Tasks))
	for _, task := range document.Tasks {
		webTasks[task.ID] = task
	}

	wide := renderTerminalBoard(t, board, terminalui.LayoutWide)
	narrow := renderTerminalBoard(t, board, terminalui.LayoutNarrow)

	seen := 0
	for _, column := range board.Columns {
		for _, view := range column.Tasks {
			seen++
			entry, ok := web[view.Task.ID]
			if !ok {
				t.Fatalf("web board omits task %s that the terminal board places in %q", view.Task.ID, column.Label)
			}
			// The derived facts have to be the same numbers, not merely both
			// plausible: an ID prefix computed twice is the classic drift.
			if entry.IDPrefix != view.IDPrefix {
				t.Errorf("task %s prefix: terminal %q, web %q", view.Task.ID, view.IDPrefix, entry.IDPrefix)
			}
			if entry.DependenciesComplete != view.DependenciesComplete || entry.DependenciesTotal != view.DependenciesTotal {
				t.Errorf("task %s dependency summary: terminal %d/%d, web %d/%d",
					view.Task.ID, view.DependenciesComplete, view.DependenciesTotal,
					entry.DependenciesComplete, entry.DependenciesTotal)
			}
			if entry.WaitingOnDependencies != view.WaitingOnDependencies {
				t.Errorf("task %s waiting-on-dependencies: terminal %t, web %t",
					view.Task.ID, view.WaitingOnDependencies, entry.WaitingOnDependencies)
			}

			// And the task fields both surfaces show have to come out equal.
			served, ok := webTasks[view.Task.ID]
			if !ok {
				t.Fatalf("web board lists no task %s", view.Task.ID)
			}
			if served.Title != view.Task.Title || served.Priority != view.Task.Priority ||
				served.Status != view.Task.Status || strings.Join(served.Labels, ",") != strings.Join(view.Task.Labels, ",") {
				t.Errorf("task %s: terminal shows %#v, web serves %#v", view.Task.ID, view.Task, served)
			}

			// The terminal has to actually print what it was handed. Both
			// layouts, because they format a card differently: the wide board
			// abbreviates priority to one letter and the narrow board spells it.
			assertRenders(t, "wide", wide, view.IDPrefix, view.Task.Title, wideMarker(view.Task.Priority))
			assertRenders(t, "narrow", narrow, view.IDPrefix, view.Task.Title, "["+string(view.Task.Priority)+"]")
			if len(view.Task.Labels) > 0 {
				assertRenders(t, "wide", wide, strings.Join(view.Task.Labels, ","))
				assertRenders(t, "narrow", narrow, strings.Join(view.Task.Labels, ", "))
			}
		}
	}
	if seen != len(tasks) {
		t.Fatalf("terminal board placed %d of %d tasks in canonical columns", seen, len(tasks))
	}
	if len(board.UnknownTasks) != 0 {
		t.Fatalf("canonical tasks landed outside the columns: %#v", board.UnknownTasks)
	}
}

// A status neither board has a column for is the one case where "render the
// columns" and "render every task" come apart, and it is the case a beta
// produces on its own: two clones on different Workbook versions, one holding a
// status the other has never heard of. Both boards name the task, under a
// heading that says the status was not recognized, rather than one of them
// quietly dropping it — a task that is invisible reads as a task that was
// deleted, which is a worse lie than a task that is merely unsorted.
func TestBothBoardsNameATaskWhoseStatusHasNoColumn(t *testing.T) {
	const strandedID = "WB-01J0000000000000000000EE"
	tasks := append(parityTasks(), core.Task{
		ID: strandedID,
		TaskData: core.TaskData{
			Title: "Status from a newer Workbook", Status: core.Status("archived"),
			Priority: core.PriorityMedium, Rank: "5",
		},
	})
	board := presentation.NewBoard(tasks, core.Vocabulary{})

	if len(board.UnknownTasks) != 1 || board.UnknownTasks[0].Task.ID != strandedID {
		t.Fatalf("unknown-status tasks = %#v, want exactly the archived task", board.UnknownTasks)
	}
	for _, column := range board.Columns {
		for _, view := range column.Tasks {
			if view.Task.ID == strandedID {
				t.Fatalf("archived task landed in canonical column %q", column.Label)
			}
		}
	}
	prefix := board.UnknownTasks[0].IDPrefix

	for _, layout := range []struct {
		name   string
		layout terminalui.Layout
	}{{"wide", terminalui.LayoutWide}, {"narrow", terminalui.LayoutNarrow}} {
		rendered := renderTerminalBoard(t, board, layout.layout)
		if !strings.Contains(rendered, "UNKNOWN STATUS (1)") {
			t.Errorf("terminal %s board did not name the unknown status:\n%s", layout.name, rendered)
		}
		assertRenders(t, layout.name, rendered, prefix, "Status from a newer Workbook")
	}

	// The web side carries it in both arrays with the same derived prefix, so a
	// client that renders the document has the same facts the terminal printed.
	document := webBoardDocument(t, tasks)
	var served *webui.TaskPresentation
	for index, entry := range document.Presentation {
		if entry.TaskID == strandedID {
			served = &document.Presentation[index]
		}
	}
	if served == nil {
		t.Fatalf("web presentation omits the unknown-status task: %#v", document.Presentation)
	}
	if served.IDPrefix != prefix {
		t.Fatalf("unknown-status prefix: terminal %q, web %q", prefix, served.IDPrefix)
	}

	// And the page the server renders before any script runs shows it too, in a
	// region of its own rather than inside one of the status columns.
	page := webBoardPage(t, tasks)
	region := strings.Index(page, "data-unknown-list")
	if region < 0 {
		t.Fatalf("web board page has no unknown-status region:\n%s", page)
	}
	card := strings.Index(page, `data-task-id="`+strandedID+`"`)
	if card < 0 {
		t.Fatalf("web board page omits the unknown-status task %s", strandedID)
	}
	if card < region {
		t.Fatalf("web board page rendered %s inside a status column, at %d before the unknown region at %d", strandedID, card, region)
	}
	if !strings.Contains(page, `data-id-prefix="`+prefix+`"`) {
		t.Errorf("web board page does not show the terminal's ID prefix %q", prefix)
	}
	if !strings.Contains(page, "Unknown status") {
		t.Errorf("web board page does not label the unknown-status region:\n%s", page)
	}
}

// parityVocabulary is a project that shares no status name with the built-in
// six, renamed one of its own, and removed another into a third. Nothing in it
// can be produced by a fixed table, which is the point: a renderer that still
// held one would fail every assertion below rather than a subtle one.
func parityVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "inbox", Label: "Inbox", Rank: "1/1", Tags: []core.StatusTag{core.StatusTagDefault}},
			{Status: "doing", Label: "Doing", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagNext}},
			{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		[]core.StatusAlias{{From: "in-progress", To: "doing"}},
		[]core.RetiredStatus{{Status: "blocked", Destination: "inbox"}},
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

func customVocabularyTasks() []core.Task {
	return []core.Task{
		{
			ID: "WB-01J0000000000000000000A1",
			TaskData: core.TaskData{
				Title: "Filed", Status: core.Status("inbox"), Priority: core.PriorityHigh, Rank: "1",
			},
		},
		{
			// Stored under the name a rename replaced. It belongs in Doing, and
			// a board that matched stored tokens would strand it instead.
			ID: "WB-01J0000000000000000000B2",
			TaskData: core.TaskData{
				Title: "Stored under the old name", Status: core.StatusInProgress, Priority: core.PriorityMedium, Rank: "2",
			},
		},
		{
			// Stored under a status the project removed into Inbox.
			ID: "WB-01J0000000000000000000C3",
			TaskData: core.TaskData{
				Title: "Stored under a removed name", Status: core.StatusBlocked, Priority: core.PriorityLow, Rank: "3",
			},
		},
		{
			ID: "WB-01J0000000000000000000D4",
			TaskData: core.TaskData{
				Title: "Finished", Status: core.Status("shipped"), Priority: core.PriorityMedium, Rank: "4",
			},
		},
		{
			// No chain leads out of this one, so it is the only task with no
			// column to be drawn in.
			ID: "WB-01J0000000000000000000E5",
			TaskData: core.TaskData{
				Title: "Nothing forwards this", Status: core.Status("archived"), Priority: core.PriorityLow, Rank: "5",
			},
		},
	}
}

// The columns are the project's, on both boards, and a task lands in the same
// one on both.
//
// This is the parity rule the fixed six-status array used to satisfy for free.
// Now that each board builds its columns from a vocabulary, "both boards agree"
// has to be asserted against a vocabulary neither of them could have guessed —
// including the two cases resolution exists for, a stored status a rename
// replaced and one a removal forwarded, which belong in a live column rather
// than in the unknown region.
func TestBothBoardsBuildTheProjectsOwnColumns(t *testing.T) {
	tasks := customVocabularyTasks()
	vocabulary := parityVocabulary(t)
	board := presentation.NewBoard(tasks, vocabulary)

	wantColumns := []struct{ status, label string }{
		{"inbox", "Inbox"},
		{"doing", "Doing"},
		{"shipped", "Shipped"},
	}
	if len(board.Columns) != len(wantColumns) {
		t.Fatalf("terminal board has %d columns, want %d: %#v", len(board.Columns), len(wantColumns), board.Columns)
	}
	terminalPlacement := make(map[string]string, len(tasks))
	for index, column := range board.Columns {
		if string(column.Status) != wantColumns[index].status || column.Label != wantColumns[index].label {
			t.Fatalf("terminal column %d = {%q, %q}, want {%q, %q}",
				index, column.Status, column.Label, wantColumns[index].status, wantColumns[index].label)
		}
		for _, view := range column.Tasks {
			terminalPlacement[view.Task.ID] = string(column.Status)
		}
	}
	for _, view := range board.UnknownTasks {
		terminalPlacement[view.Task.ID] = unknownRegion
	}
	want := map[string]string{
		"WB-01J0000000000000000000A1": "inbox",
		"WB-01J0000000000000000000B2": "doing",
		"WB-01J0000000000000000000C3": "inbox",
		"WB-01J0000000000000000000D4": "shipped",
		"WB-01J0000000000000000000E5": unknownRegion,
	}
	if !reflect.DeepEqual(terminalPlacement, want) {
		t.Fatalf("terminal placement = %#v, want %#v", terminalPlacement, want)
	}

	// The labels have to reach the printed board too, in both layouts: a column
	// that carries the project's label and prints a derived one is the same
	// drift by another route.
	for name, layout := range map[string]terminalui.Layout{"wide": terminalui.LayoutWide, "narrow": terminalui.LayoutNarrow} {
		rendered := renderTerminalBoard(t, board, layout)
		for _, column := range wantColumns {
			heading := column.label
			if layout == terminalui.LayoutNarrow {
				heading = strings.ToUpper(heading)
			}
			assertRenders(t, name, rendered, heading)
		}
		for _, absent := range []string{"BACKLOG", "IN REVIEW", "Backlog", "In Review"} {
			if strings.Contains(rendered, absent) {
				t.Errorf("terminal %s board prints the built-in column %q for a project that does not define it:\n%s", name, absent, rendered)
			}
		}
	}

	// And the page the server renders before any script runs has to agree,
	// column for column and card for card.
	page := webBoardResponse(t, tasks, vocabulary, "/").Body.String()
	gotColumns := webColumns(page)
	if len(gotColumns) != len(wantColumns) {
		t.Fatalf("web board has %d columns, want %d: %#v", len(gotColumns), len(wantColumns), gotColumns)
	}
	for index, column := range gotColumns {
		if column.status != wantColumns[index].status || column.label != wantColumns[index].label {
			t.Fatalf("web column %d = {%q, %q}, want {%q, %q}",
				index, column.status, column.label, wantColumns[index].status, wantColumns[index].label)
		}
	}
	if got := webPlacement(t, page); !reflect.DeepEqual(got, terminalPlacement) {
		t.Fatalf("web placement = %#v, want the terminal board's %#v", got, terminalPlacement)
	}

	// The default-tagged status is the server's answer, not a constant: it is
	// what /tasks/new falls back to when no status is named.
	if !strings.Contains(page, `data-default-status="inbox"`) {
		t.Errorf("web board page does not carry the project's default status:\n%s", page)
	}
}

// unknownRegion is the placement key for the region that is not a column, so
// the two boards can be compared with one map rather than two shapes.
const unknownRegion = "(unknown)"

type webColumn struct{ status, label string }

// webColumns reads the columns the page rendered, in document order.
func webColumns(page string) []webColumn {
	pattern := regexp.MustCompile(`data-status="([^"]*)" data-status-label="([^"]*)"`)
	matches := pattern.FindAllStringSubmatch(page, -1)
	columns := make([]webColumn, len(matches))
	for index, match := range matches {
		columns[index] = webColumn{status: match[1], label: match[2]}
	}
	return columns
}

// webPlacement reads which region each card was rendered inside, by position:
// a card belongs to the last column opened before it, and to the unknown region
// once that has opened. It is deliberately positional rather than structural,
// because position is what a reader sees.
func webPlacement(t *testing.T, page string) map[string]string {
	t.Helper()
	type region struct {
		start int
		name  string
	}
	regions := make([]region, 0, 8)
	for _, match := range regexp.MustCompile(`data-status="([^"]*)"`).FindAllStringSubmatchIndex(page, -1) {
		regions = append(regions, region{start: match[0], name: page[match[2]:match[3]]})
	}
	unknown := strings.Index(page, "data-unknown-list")
	if unknown < 0 {
		t.Fatalf("web board page has no unknown-status region:\n%s", page)
	}
	regions = append(regions, region{start: unknown, name: unknownRegion})

	placement := make(map[string]string)
	for _, match := range regexp.MustCompile(`data-task-id="([^"]*)"`).FindAllStringSubmatchIndex(page, -1) {
		id := page[match[2]:match[3]]
		if _, seen := placement[id]; seen {
			// The copy control repeats the ID inside the card it already placed.
			continue
		}
		for _, candidate := range regions {
			if candidate.start < match[0] {
				placement[id] = candidate.name
			}
		}
	}
	return placement
}

func webBoardDocument(t *testing.T, tasks []core.Task) webui.TasksDocument {
	t.Helper()
	recorder := webBoardResponse(t, tasks, core.Vocabulary{}, "/api/tasks")
	var document webui.TasksDocument
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode tasks document: %v; body = %s", err, recorder.Body.String())
	}
	return document
}

func webBoardPage(t *testing.T, tasks []core.Task) string {
	t.Helper()
	return webBoardResponse(t, tasks, core.Vocabulary{}, "/").Body.String()
}

// webBoardResponse serves one request from a board built on the given
// vocabulary. The zero vocabulary is a board built without a resolver at all,
// which is how every caller that predates per-project columns builds one.
func webBoardResponse(t *testing.T, tasks []core.Task, vocabulary core.Vocabulary, path string) *httptest.ResponseRecorder {
	t.Helper()
	options := webui.Options{
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
	}
	if !vocabulary.IsZero() {
		options.Vocabulary = func(context.Context) (webui.VocabularyState, error) {
			return webui.VocabularyState{Vocabulary: vocabulary, Head: "0123456789abcdef0123456789abcdef01234567"}, nil
		}
	}
	handler := webui.NewHandler(options)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d; body = %s", path, recorder.Code, http.StatusOK, recorder.Body.String())
	}
	return recorder
}

func renderTerminalBoard(t *testing.T, board presentation.Board, layout terminalui.Layout) string {
	t.Helper()
	var output strings.Builder
	// Wide enough that nothing under test is truncated, so a missing string is
	// a missing fact rather than an elision.
	if err := terminalui.RenderBoard(&output, board, layout, 400); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}
	return output.String()
}

func assertRenders(t *testing.T, layout, rendered string, wanted ...string) {
	t.Helper()
	for _, want := range wanted {
		if !strings.Contains(rendered, want) {
			t.Errorf("%s board does not show %q", layout, want)
		}
	}
}

func wideMarker(priority core.Priority) string {
	switch priority {
	case core.PriorityHigh:
		return "[H]"
	case core.PriorityMedium:
		return "[M]"
	case core.PriorityLow:
		return "[L]"
	default:
		return "[?]"
	}
}
