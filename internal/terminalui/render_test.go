package terminalui

import (
	"bytes"
	"strings"
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

// The board goldens in this file render core.LegacyVocabulary rather than the
// statuses a mint writes, and deliberately. They pin how a fixed terminal width
// is divided among columns and where an identifier wraps when it will not fit,
// so the interesting fixture is the wider set — six columns, which is what every
// project with no configuration ledger draws. Rendering five would relax the
// widths these cases exist to hold.
func TestRenderBoardWideGolden(t *testing.T) {
	board := presentation.NewBoard([]core.Task{
		{
			ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
			TaskData: core.TaskData{
				Title:    "Plan storage",
				Status:   core.StatusBacklog,
				Priority: core.PriorityHigh,
				Labels:   []string{"git", "poc"},
			},
		},
		{
			ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW",
			TaskData: core.TaskData{
				Title:    "Ship release",
				Status:   core.StatusDone,
				Priority: core.PriorityLow,
				Labels:   []string{"release"},
			},
		},
	}, core.LegacyVocabulary())

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutWide, 140); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	const want = "+----------------------+----------------------+----------------------+----------------------+----------------------+----------------------+\n" +
		"| Backlog (1)          | Ready (0)            | Blocked (0)          | In Progress (0)      | In Review (0)        | Done (1)             |\n" +
		"+----------------------+----------------------+----------------------+----------------------+----------------------+----------------------+\n" +
		"| WB-01ARZ3ND [H]      |                      |                      |                      |                      | WB-01BRZ3ND [L]      |\n" +
		"| Plan storage         |                      |                      |                      |                      | Ship release         |\n" +
		"| git,poc              |                      |                      |                      |                      | release              |\n" +
		"+----------------------+----------------------+----------------------+----------------------+----------------------+----------------------+\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderBoard() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderBoardWidePreservesLongUniquePrefixes(t *testing.T) {
	board := presentation.NewBoard([]core.Task{
		{
			ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
			TaskData: core.TaskData{
				Title:    "First",
				Status:   core.StatusBacklog,
				Priority: core.PriorityHigh,
			},
		},
		{
			ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAW",
			TaskData: core.TaskData{
				Title:    "Second",
				Status:   core.StatusBacklog,
				Priority: core.PriorityMedium,
			},
		},
	}, core.LegacyVocabulary())

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutWide, 100); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	last := -1
	for _, want := range []string{
		"WB-01ARZ3NDEK", "TSV4RRFFQ69G5", "FAV [H]",
		"WB-01ARZ3NDEK", "TSV4RRFFQ69G5", "FAW [M]",
	} {
		position := strings.Index(output.String()[last+1:], want)
		if position < 0 {
			t.Fatalf("wide board = %q, missing actionable metadata %q", output.String(), want)
		}
		last += position + len(want)
	}
}

func TestRenderBoardNarrowGolden(t *testing.T) {
	board := presentation.NewBoard([]core.Task{
		{
			ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
			TaskData: core.TaskData{
				Title:    "Plan storage",
				Status:   core.StatusBacklog,
				Priority: core.PriorityHigh,
				Labels:   []string{"git", "poc"},
			},
		},
		{
			ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW",
			TaskData: core.TaskData{
				Title:    "Ship release",
				Status:   core.StatusDone,
				Priority: core.PriorityLow,
				Labels:   []string{"release"},
			},
		},
	}, core.LegacyVocabulary())

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
		"BLOCKED (0)\n" +
		"-----------\n" +
		"(empty)\n\n" +
		"IN PROGRESS (0)\n" +
		"---------------\n" +
		"(empty)\n\n" +
		"IN REVIEW (0)\n" +
		"-------------\n" +
		"(empty)\n\n" +
		"DONE (1)\n" +
		"--------\n" +
		"WB-01BRZ3ND [low] Ship release\n" +
		"  labels: release\n"
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
	}}, core.LegacyVocabulary())

	var output bytes.Buffer
	if err := RenderBoard(&output, board, LayoutNarrow, 100); err != nil {
		t.Fatalf("RenderBoard() error = %v", err)
	}

	const want = "BACKLOG (0)\n-----------\n(empty)\n\n" +
		"READY (0)\n---------\n(empty)\n\n" +
		"BLOCKED (0)\n-----------\n(empty)\n\n" +
		"IN PROGRESS (0)\n---------------\n(empty)\n\n" +
		"IN REVIEW (0)\n-------------\n(empty)\n\n" +
		"DONE (0)\n--------\n(empty)\n\n" +
		"UNKNOWN STATUS (1)\n------------------\n" +
		"(no status this project defines, and no rename or removal leads to one)\n" +
		"WB-01ARZ3ND [low] Archived task\n"
	if got := output.String(); got != want {
		t.Fatalf("RenderBoard() =\n%q\nwant\n%q", got, want)
	}
}

// A board with no columns renders rather than crashing, in both layouts.
//
// It is reachable from data, not from a mistake in this package: a
// configuration checkpoint is decoded and trusted, and nothing in the state
// document requires a live status, so a ledger tip written by a hand edit, a
// corruption or a foreign build arrives here with an empty status set and a
// forwarding entry. That vocabulary is not the zero value, so the board's
// substitution of the built-in statuses does not fire and the column count is
// genuinely zero. The wide grid used to divide the terminal width among zero
// cells and panic — through the real `workbook board --wide`, on a repository
// whose only fault was in a ref.
//
// Whatever the board is holding is in the unknown-status section in that state,
// so both layouts have to print it: a renderer that swallowed the task would
// turn a broken ledger into missing work.
func TestRenderBoardWithNoColumnsRendersRatherThanCrashing(t *testing.T) {
	// Not the zero vocabulary: it forwards a status and defines none, which is
	// what makes the column count zero instead of six.
	vocabulary, err := core.NewVocabulary(nil, nil, []core.RetiredStatus{{Status: "ghost", Destination: "gone"}})
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	if vocabulary.IsZero() {
		t.Fatal("the fixture is the zero vocabulary, so the board substitutes the built-in statuses and proves nothing")
	}
	board := presentation.NewBoard([]core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "Stranded by a ledger with no statuses",
			Status:   core.Status("ghost"),
			Priority: core.PriorityLow,
		},
	}}, vocabulary)
	if len(board.Columns) != 0 {
		t.Fatalf("board has %d columns, want none", len(board.Columns))
	}

	for name, layout := range map[string]Layout{"wide": LayoutWide, "narrow": LayoutNarrow} {
		var output bytes.Buffer
		if err := RenderBoard(&output, board, layout, 140); err != nil {
			t.Fatalf("RenderBoard(%s) error = %v", name, err)
		}
		got := output.String()
		if !strings.Contains(got, "UNKNOWN STATUS (1)") {
			t.Errorf("RenderBoard(%s) = %q, want the unknown-status section", name, got)
		}
		if !strings.Contains(got, "Stranded by a ledger with no statuses") {
			t.Errorf("RenderBoard(%s) = %q, want the stranded task named", name, got)
		}
		// No grid, rather than an empty one drawn around nothing.
		if strings.Contains(got, "+--") {
			t.Errorf("RenderBoard(%s) = %q, want no column grid for a board with no columns", name, got)
		}
	}

	// And with nothing at all to show, it still returns rather than dividing by
	// a column count of zero.
	var empty bytes.Buffer
	if err := RenderBoard(&empty, presentation.NewBoard(nil, vocabulary), LayoutWide, 140); err != nil {
		t.Fatalf("RenderBoard(empty) error = %v", err)
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

func TestRenderBoardStripsControlCharactersFromCards(t *testing.T) {
	// Mutation caught: card lines rendering stored bytes verbatim in either
	// layout.
	board := presentation.NewBoard([]core.Task{{
		ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{
			Title:    "benign\x1b[2K\x1b[1Gforged",
			Status:   core.StatusBacklog,
			Priority: core.PriorityHigh,
			Labels:   []string{"git\x1b[2K"},
		},
	}}, core.LegacyVocabulary())

	for name, layout := range map[string]Layout{"narrow": LayoutNarrow, "wide": LayoutWide} {
		var output bytes.Buffer
		if err := RenderBoard(&output, board, layout, 140); err != nil {
			t.Fatalf("RenderBoard(%s) error = %v", name, err)
		}
		got := output.String()
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("RenderBoard(%s) = %q, want no ESC bytes", name, got)
		}
		if !strings.Contains(got, "benign [2K [1Gforged") {
			t.Fatalf("RenderBoard(%s) = %q, want the sanitized title", name, got)
		}
	}
}
