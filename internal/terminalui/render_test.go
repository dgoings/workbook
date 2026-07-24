package terminalui

import (
	"bytes"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
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

func TestRenderBoardWideGolden(t *testing.T) {
	board := presentation.NewBoard([]core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "Plan storage",
			Status:   core.StatusBacklog,
			Priority: core.PriorityHigh,
			Labels:   []string{"git", "poc"},
		},
	}})

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutWide, 140); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	const want = "+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+\n" +
		"| Backlog (1)              | Ready (0)                | In progress (0)          | Blocked (0)              | Done (0)                 |\n" +
		"+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+\n" +
		"| WB-01ARZ3ND [H]          |                          |                          |                          |                          |\n" +
		"| Plan storage             |                          |                          |                          |                          |\n" +
		"| git,poc                  |                          |                          |                          |                          |\n" +
		"+--------------------------+--------------------------+--------------------------+--------------------------+--------------------------+\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderBoard() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderBoardNarrowGolden(t *testing.T) {
	board := presentation.NewBoard([]core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "Plan storage",
			Status:   core.StatusBacklog,
			Priority: core.PriorityHigh,
			Labels:   []string{"git", "poc"},
		},
	}})

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutNarrow, 100); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	const want = "BACKLOG (1)\n" +
		"-----------\n" +
		"WB-01ARZ3ND [high] Plan storage\n" +
		"  labels: git, poc\n\n" +
		"READY (0)\n" +
		"---------\n" +
		"(empty)\n\n" +
		"IN PROGRESS (0)\n" +
		"---------------\n" +
		"(empty)\n\n" +
		"BLOCKED (0)\n" +
		"-----------\n" +
		"(empty)\n\n" +
		"DONE (0)\n" +
		"--------\n" +
		"(empty)\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderBoard() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderBoardShowsAllEmptyColumns(t *testing.T) {
	board := presentation.NewBoard([]core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "Archived task",
			Status:   core.Status("archived"),
			Priority: core.PriorityLow,
		},
	}})

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutNarrow, 100); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	const want = "BACKLOG (0)\n-----------\n(empty)\n\n" +
		"READY (0)\n---------\n(empty)\n\n" +
		"IN PROGRESS (0)\n---------------\n(empty)\n\n" +
		"BLOCKED (0)\n-----------\n(empty)\n\n" +
		"DONE (0)\n--------\n(empty)\n\n" +
		"UNKNOWN STATUS (1)\n------------------\n" +
		"WB-01ARZ3ND [low] Archived task\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderBoard() =\n%q\nwant\n%q", got, want)
	}
}
