package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/term"

	"github.com/dgoings/workbook/internal/core"
)

// projectKeyAttempts bounds how many times the prompt asks again after an
// answer the grammar refuses. A person who cannot produce a key in that many
// tries is better served by the message than by an unbounded loop, and a
// stream that keeps supplying garbage must not keep the command alive.
const projectKeyAttempts = 5

// interactiveTerminal reports whether both ends of a conversation are
// terminals: a person typing on stdin and a screen on stdout. Either side
// being a pipe means a script or another program, and a prompt written into
// a pipe hangs the caller waiting for an answer nobody is there to give.
func interactiveTerminal(stdin io.Reader, stdout io.Writer) bool {
	return isTerminal(stdin) && isTerminal(stdout)
}

func isTerminal(stream any) bool {
	descriptor, ok := stream.(fileDescriptor)
	if !ok {
		return false
	}
	return term.IsTerminal(int(descriptor.Fd()))
}

// promptProjectKey asks for the project key a new project will mint under,
// offering suggested as the answer Enter gives. An answer is trimmed and
// uppercased before the grammar sees it, because a key is uppercase by
// definition and typing "myapp" for MYAPP is what anyone would do. An answer
// the grammar still refuses is explained and asked again, and end of input
// takes the suggestion, so a person who closes the stream gets the default
// rather than a failure.
func promptProjectKey(stdin io.Reader, stdout io.Writer, suggested string) (string, error) {
	reader := bufio.NewReader(stdin)
	for attempt := 0; attempt < projectKeyAttempts; attempt++ {
		fmt.Fprintf(stdout, "Project key [%s]: ", suggested)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", core.Wrap(core.CategoryOperational, "cannot read the project key", err)
		}
		answer := strings.ToUpper(strings.TrimSpace(line))
		if answer == "" {
			if errors.Is(err, io.EOF) {
				// A closed stream with nothing typed is an Enter that will never
				// arrive; the newline the terminal did not get is written so
				// the report starts on its own line.
				fmt.Fprintln(stdout)
			}
			return suggested, nil
		}
		if validationErr := core.ValidateProjectKey(answer); validationErr != nil {
			fmt.Fprintf(stdout, "%s\n", validationErr)
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		return answer, nil
	}
	return "", core.Errorf(core.CategoryInvocation,
		"no usable project key was given; rerun workbook setup --key <key> with a key matching %s", core.ProjectKeyPattern())
}
