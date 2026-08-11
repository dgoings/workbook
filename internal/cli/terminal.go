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
	wideBoardMinimum    = 140
	nonInteractiveWidth = 100
)

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
	if *jsonMode {
		writeResult(stdout, "board", tasks)
		return nil
	}

	width, measured := terminalWidth(stdout)
	if !measured {
		width = nonInteractiveWidth
	}
	layout := terminalui.LayoutNarrow
	if *wide || (!*narrow && measured && width >= wideBoardMinimum) {
		layout = terminalui.LayoutWide
	}
	if err := terminalui.RenderBoard(stdout, presentation.NewBoard(tasks), layout, width); err != nil {
		return core.Wrap(core.CategoryOperational, "render board", err)
	}
	return nil
}
