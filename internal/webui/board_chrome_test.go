package webui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board's own chrome — what a column header carries and how the stylesheet
// sizes and decorates it. None of it is drawn by the client, so it is asserted
// against the served page rather than through the Node harness: the page is the
// only artifact that exists, and a fake DOM with no layout engine could not read
// a rule out of it anyway.

// boardPage renders the board with the standard task set and returns its HTML.
func boardPage(t *testing.T) string {
	t.Helper()
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// A column header names a status a reader recognizes. The Git ref that status
// is stored under is an implementation detail of the tool, not something the
// reader can act on, and printing six copies of it cost a whole row of every
// header for no decision anyone makes from the board.
func TestHandlerBoardColumnsOmitWorkbookRefPaths(t *testing.T) {
	body := boardPage(t)
	for _, definition := range core.WorkflowStatuses() {
		if want := "refs/workbook/status/" + string(definition.Status); strings.Contains(body, want) {
			t.Errorf("GET / body still prints the ref path %q in a column header", want)
		}
	}
	if strings.Contains(body, "ref-label") {
		t.Error("GET / body still carries the ref-path element or its styling")
	}
	// The header is otherwise unchanged: the label, the count, and the link that
	// files a new task under this column all remain.
	for _, fragment := range []string{
		`class="column__header"`,
		`class="count" data-count="ready"`,
		`href="/tasks/new?status=ready"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
}

// Six columns of underlined titles read as a page of rules. The underline is
// carrying nothing here — a card is one link and the card itself takes focus —
// so the title is plain text that turns blue under the pointer.
func TestHandlerBoardCardTitlesAreNotUnderlined(t *testing.T) {
	body := boardPage(t)
	for _, fragment := range []string{
		`.task-card h3 a { color: #172033; text-decoration: none; }`,
		`.task-card h3 a:hover { color: #2457d6; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("card title styling does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `.task-card h3 a { color: #172033; text-decoration-color`) {
		t.Error("card titles still draw an underline")
	}
}
