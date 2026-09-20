package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// projectKeyAttempts bounds how many times the prompt asks again after an
// answer the grammar refuses. A person who cannot produce a key in that many
// tries is better served by the message than by an unbounded loop, and a
// stream that keeps supplying garbage must not keep the command alive.
const projectKeyAttempts = 5

// promptAnswer is one line read from the person answering the prompt, with
// whatever the read ended on. It travels over a channel because the read
// itself cannot be canceled: only a separate goroutine lets the prompt stop
// waiting for a line that will never come.
type promptAnswer struct {
	line string
	err  error
}

// promptProjectKey asks for the project key a new project will mint under,
// offering suggested as the answer Enter gives. An answer is trimmed and
// uppercased before the grammar sees it, because a key is uppercase by
// definition and typing "myapp" for MYAPP is what anyone would do. An answer
// the grammar still refuses is explained and asked again.
//
// The prompt is reached only when both stdin and stdout are terminals, so end
// of input here is a person pressing Ctrl-D rather than a stream running out:
// it cancels setup instead of accepting the suggestion. Nothing is created,
// and the message says how to get the suggested key deliberately. An invalid
// final line is still explained before that, so the person sees why their last
// answer did not count.
//
// Ctrl-C is the other way out, and it is why the read runs on its own
// goroutine: a read in flight on the terminal cannot be interrupted, so the
// prompt selects between the line and the canceled context and returns as
// soon as either arrives. The goroutine blocked on the terminal is left to the
// process exit that follows.
func promptProjectKey(ctx context.Context, stdin io.Reader, stdout io.Writer, suggested string) (string, error) {
	answers := make(chan promptAnswer)
	go func() {
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadString('\n')
			select {
			case answers <- promptAnswer{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for attempt := 0; attempt < projectKeyAttempts; attempt++ {
		fmt.Fprintf(stdout, "Project key [%s]: ", suggested)
		var received promptAnswer
		select {
		case received = <-answers:
		case <-ctx.Done():
			return "", core.Errorf(core.CategoryOperational, "setup interrupted; nothing was created")
		}
		if received.err != nil && !errors.Is(received.err, io.EOF) {
			return "", core.Wrap(core.CategoryOperational, "cannot read the project key", received.err)
		}
		atEOF := errors.Is(received.err, io.EOF)
		answer := strings.ToUpper(strings.TrimSpace(received.line))
		if answer != "" {
			if validationErr := core.ValidateProjectKey(answer); validationErr != nil {
				fmt.Fprintf(stdout, "%s\n", validationErr)
				if atEOF {
					return "", endOfInputError()
				}
				continue
			}
			return answer, nil
		}
		if atEOF {
			// Ctrl-D with nothing typed: the newline the terminal did not get
			// is written so the refusal starts on its own line.
			fmt.Fprintln(stdout)
			return "", endOfInputError()
		}
		return suggested, nil
	}
	return "", core.Errorf(core.CategoryValidation,
		"no usable project key was given; rerun workbook setup --key <key> with a key matching %s", core.ProjectKeyPattern())
}

// endOfInputError is what Ctrl-D at the prompt means: the person stopped
// before naming a key, so nothing is created and the message says both ways to
// finish the job on the next run.
func endOfInputError() error {
	return core.Errorf(core.CategoryValidation,
		"no project key was given; nothing was created. Rerun workbook setup and press Enter to accept the suggested key, or pass --key <key>")
}
