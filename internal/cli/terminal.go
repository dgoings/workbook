package cli

import (
	"io"

	"golang.org/x/term"
)

// nonInteractiveWidth is the width a table is laid out for when standard output
// is not a terminal that can say how wide it is.
const nonInteractiveWidth = 100

type fileDescriptor interface {
	Fd() uintptr
}

// interactiveTerminal reports whether both ends of a conversation are
// terminals: a person typing on stdin and a screen on stdout. Either side
// being a pipe means a script or another program, and a prompt written into
// a pipe hangs the caller waiting for an answer nobody is there to give. A
// nil reader (the watcher tests pass one) is never a terminal.
func interactiveTerminal(stdin io.Reader, stdout io.Writer) bool {
	return readerIsTerminal(stdin) && writerIsTerminal(stdout)
}

func readerIsTerminal(reader io.Reader) bool {
	descriptor, ok := reader.(fileDescriptor)
	if !ok {
		return false
	}
	return term.IsTerminal(int(descriptor.Fd()))
}

func writerIsTerminal(writer io.Writer) bool {
	descriptor, ok := writer.(fileDescriptor)
	if !ok {
		return false
	}
	return term.IsTerminal(int(descriptor.Fd()))
}

func terminalWidth(output io.Writer) (int, bool) {
	if !writerIsTerminal(output) {
		return 0, false
	}
	descriptor, ok := output.(fileDescriptor)
	if !ok {
		return 0, false
	}
	width, _, err := term.GetSize(int(descriptor.Fd()))
	if err != nil || width <= 0 {
		return 0, false
	}
	return width, true
}
