package cli

import (
	"context"
	"io"

	"golang.org/x/term"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
	"github.com/dgoings/workbook/internal/terminalui"
)

const (
	// wideBoardMinimum is the terminal width the wide board wanted while every
	// project had exactly wideBoardMinimumColumns statuses. It is kept as the
	// calibration point rather than as the threshold: a project defines its own
	// statuses now, and a fixed number would give a three-column board the same
	// cell width a six-column one gets at 280.
	wideBoardMinimum = 140
	// wideBoardMinimumColumns is how many columns wideBoardMinimum was chosen
	// for, so the ratio between them is the per-column budget and the pair
	// reproduce the historical threshold exactly.
	wideBoardMinimumColumns = 6
	nonInteractiveWidth     = 100
)

// wideBoardMinimumFor returns the terminal width a board of this many columns
// needs before the wide layout is worth choosing.
//
// The budget is per column, rounded up, so at six columns it is exactly the 140
// this has always used and at any other count it asks for the same room per
// column rather than the same room in total.
//
// A board with no columns is charged the whole historical width rather than
// nothing. That state is reachable from a corrupt or foreign ledger tip and
// renders as sections either way — renderWideBoard has no grid to draw and says
// so by deferring to the narrow layout — so the number only has to be a width
// no terminal is silently under, never zero.
func wideBoardMinimumFor(columns int) int {
	if columns <= 0 {
		return wideBoardMinimum
	}
	return (wideBoardMinimum*columns + wideBoardMinimumColumns - 1) / wideBoardMinimumColumns
}

type fileDescriptor interface {
	Fd() uintptr
}

func terminalWidth(output io.Writer) (int, bool) {
	descriptor, ok := output.(fileDescriptor)
	if !ok {
		return 0, false
	}
	fd := int(descriptor.Fd())
	if !term.IsTerminal(fd) {
		return 0, false
	}
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
}

func runBoard(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("board")
	wide := flags.Bool("wide", false, "render a wide board")
	narrow := flags.Bool("narrow", false, "render a narrow board")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *wide && *narrow {
		return core.Errorf(core.CategoryInvocation, "cannot use --wide with --narrow")
	}

	service, err := openReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	tasks, err := service.List(ctx, core.ListFilter{})
	if err != nil {
		return err
	}
	warnings := newerWriterWarnings(tasks)
	if *jsonMode {
		writeResultWithWarnings(stdout, "board", tasks, warnings)
		return nil
	}

	// The project's own columns, and the width they need. A project with three
	// statuses gets a wide board on a terminal that could never have fitted six.
	board := presentation.NewBoard(tasks, service.Vocabulary)
	width, measured := terminalWidth(stdout)
	if !measured {
		width = nonInteractiveWidth
	}
	layout := terminalui.LayoutNarrow
	if *wide || (!*narrow && measured && width >= wideBoardMinimumFor(len(board.Columns))) {
		layout = terminalui.LayoutWide
	}
	if err := terminalui.RenderBoard(stdout, board, layout, width); err != nil {
		return core.Wrap(core.CategoryOperational, "render board", err)
	}
	writeWarnings(stderr, warnings)
	return nil
}
