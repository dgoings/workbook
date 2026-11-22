package terminalui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestRenderListProducesCompactDeterministicTable(t *testing.T) {
	tasks := []core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "Plan storage",
			Status:   core.StatusBacklog,
			Priority: core.PriorityHigh,
			Labels:   []string{"git", "poc"},
		},
	}}

	var output bytes.Buffer
	if err := RenderList(&output, tasks, 100); err != nil {
		t.Fatalf("RenderList() error = %v", err)
	}

	const want = "ID                             TITLE                     STATUS   PRIORITY  LABELS\n" +
		"WB-01ARZ3NDEKTSV4RRFFQ69G5FAV  Plan storage              backlog  high      git,poc\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderList() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderListTruncatesOnlyHumanFields(t *testing.T) {
	tasks := []core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "A title that is deliberately too long",
			Status:   core.StatusInProgress,
			Priority: core.PriorityMedium,
			Labels:   []string{"backend", "prototype", "terminal"},
		},
	}}

	var output bytes.Buffer
	if err := RenderList(&output, tasks, 80); err != nil {
		t.Fatalf("RenderList() error = %v", err)
	}

	const want = "ID                             TITLE         STATUS       PRIORITY  LABELS\n" +
		"WB-01ARZ3NDEKTSV4RRFFQ69G5FAV  A title t...  in-progress  medium    backend,p...\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderList() =\n%q\nwant\n%q", got, want)
	}
}

func TestFitDoesNotSplitUTF8WhenTheCellIsNarrowerThanOneRune(t *testing.T) {
	if got := fit("éclair", 1); got != "" {
		t.Fatalf("fit() = %q, want no partial UTF-8 rune", got)
	}
}

func TestRenderListStripsControlCharactersFromTitlesAndLabels(t *testing.T) {
	// Mutation caught: rendering stored bytes verbatim, so an ESC sequence in
	// a title redraws the row into a forged task on a real terminal.
	tasks := []core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "benign\x1b[2K\x1b[1Gforged",
			Status:   core.StatusBacklog,
			Priority: core.PriorityHigh,
			Labels:   []string{"git\x1b[2K", "ok"},
		},
	}}

	var output bytes.Buffer
	if err := RenderList(&output, tasks, 100); err != nil {
		t.Fatalf("RenderList() error = %v", err)
	}
	got := output.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("RenderList() = %q, want no ESC bytes", got)
	}
	if !strings.Contains(got, "benign [2K [1Gforged") {
		t.Fatalf("RenderList() = %q, want the sanitized title", got)
	}
}
