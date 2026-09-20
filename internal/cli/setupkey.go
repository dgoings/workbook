package cli

import (
	"bufio"
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

// promptProjectKey asks for the project key a new project will mint under,
// offering suggested as the answer Enter gives. An answer is trimmed and
// uppercased before the grammar sees it, because a key is uppercase by
// definition and typing "myapp" for MYAPP is what anyone would do. An answer
// the grammar still refuses is explained and asked again.
//
// End of input always takes the suggestion, whatever came before it on that
// last line: an empty final line and an invalid final line are treated alike,
// because a closed stream means the person has stopped typing, and the
// suggestion is always a valid key. An invalid final line is still explained
// before the suggestion is returned, so the person sees why their last answer
// did not count.
func promptProjectKey(stdin io.Reader, stdout io.Writer, suggested string) (string, error) {
	reader := bufio.NewReader(stdin)
	for attempt := 0; attempt < projectKeyAttempts; attempt++ {
		fmt.Fprintf(stdout, "Project key [%s]: ", suggested)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", core.Wrap(core.CategoryOperational, "cannot read the project key", err)
		}
		atEOF := errors.Is(err, io.EOF)
		answer := strings.ToUpper(strings.TrimSpace(line))
		if answer == "" {
			if atEOF {
				// A closed stream with nothing typed is an Enter that will never
				// arrive; the newline the terminal did not get is written so
				// the report starts on its own line.
				fmt.Fprintln(stdout)
			}
			return suggested, nil
		}
		if validationErr := core.ValidateProjectKey(answer); validationErr != nil {
			fmt.Fprintf(stdout, "%s\n", validationErr)
			if atEOF {
				return suggested, nil
			}
			continue
		}
		return answer, nil
	}
	return "", core.Errorf(core.CategoryInvocation,
		"no usable project key was given; rerun workbook setup --key <key> with a key matching %s", core.ProjectKeyPattern())
}
