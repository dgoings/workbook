// Package presentation_test cross-checks the two boards Workbook renders.
//
// Parity is by construction today: the terminal renderer and the web API both
// consume internal/presentation, and each side is tested alone. Nothing fed one
// task set to both and compared what they present, so a renderer that started
// deriving a fact for itself — recomputing an ID prefix, counting dependencies
// again, formatting a priority from its own table — would agree with its own
// tests while the two boards showed a user different things about one task.
package presentation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	board := presentation.NewBoard(tasks)
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

// The two boards genuinely disagree about a task whose status is not canonical:
// the terminal prints an UNKNOWN STATUS section, and the web board hands the
// client a task no column claims, which its column loop drops. That divergence
// is being removed under a separate ticket that has not landed, so this asserts
// today's behavior; when the divergence goes, this test is the one that says so.
func TestBoardsDivergeOnUnknownStatusesUntilThatIsCorrected(t *testing.T) {
	tasks := append(parityTasks(), core.Task{
		ID: "WB-01J0000000000000000000EE",
		TaskData: core.TaskData{
			Title: "Status from a newer Workbook", Status: core.Status("archived"),
			Priority: core.PriorityMedium, Rank: "5",
		},
	})
	board := presentation.NewBoard(tasks)

	if len(board.UnknownTasks) != 1 || board.UnknownTasks[0].Task.ID != "WB-01J0000000000000000000EE" {
		t.Fatalf("unknown-status tasks = %#v, want exactly the archived task", board.UnknownTasks)
	}
	for _, column := range board.Columns {
		for _, view := range column.Tasks {
			if view.Task.ID == "WB-01J0000000000000000000EE" {
				t.Fatalf("archived task landed in canonical column %q", column.Label)
			}
		}
	}

	narrow := renderTerminalBoard(t, board, terminalui.LayoutNarrow)
	if !strings.Contains(narrow, "UNKNOWN STATUS (1)") {
		t.Fatalf("terminal board did not name the unknown status:\n%s", narrow)
	}
	assertRenders(t, "narrow", narrow, "Status from a newer Workbook")

	// The web side carries it in both arrays with the same derived prefix, so
	// the divergence is the client's column loop rather than a difference in
	// the facts the two boards were given.
	document := webBoardDocument(t, tasks)
	var served *webui.TaskPresentation
	for index, entry := range document.Presentation {
		if entry.TaskID == "WB-01J0000000000000000000EE" {
			served = &document.Presentation[index]
		}
	}
	if served == nil {
		t.Fatalf("web presentation omits the unknown-status task: %#v", document.Presentation)
	}
	if served.IDPrefix != board.UnknownTasks[0].IDPrefix {
		t.Fatalf("unknown-status prefix: terminal %q, web %q", board.UnknownTasks[0].IDPrefix, served.IDPrefix)
	}
}

func webBoardDocument(t *testing.T, tasks []core.Task) webui.TasksDocument {
	t.Helper()
	handler := webui.NewHandler(webui.Options{
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var document webui.TasksDocument
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode tasks document: %v; body = %s", err, recorder.Body.String())
	}
	return document
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
