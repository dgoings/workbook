package cli

import (
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// serveMaxRequestsVariable sets `workbook serve --max-requests` for a board
// started by something that is easier to hand an environment than a command
// line, such as a container or a service definition. The option wins when
// both are given.
const serveMaxRequestsVariable = "WORKBOOK_SERVE_MAX_REQUESTS"

// boardWorkLimits reads the cap on how much work the board does at once: the
// --max-requests value when it was given, the environment variable when it was
// not, and webui.DefaultMaxRequests when neither says anything.
//
// There is no value that turns the cap off. A board shared by several people
// is the one place the cap matters, and it is also the one place a value
// copied from somewhere else would go unnoticed, so anything that is not a
// whole number of at least one is refused by name rather than read as "no
// limit" or quietly replaced with the default.
func boardWorkLimits(flagValue string, flagGiven bool, getenv func(string) string) (webui.WorkLimits, error) {
	source, value := "--max-requests", flagValue
	if !flagGiven {
		value = getenv(serveMaxRequestsVariable)
		if value == "" {
			return webui.DefaultWorkLimits(), nil
		}
		source = serveMaxRequestsVariable
	}
	requests, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || requests < 1 {
		return webui.WorkLimits{}, core.Errorf(core.CategoryInvocation,
			"%s %q is not a whole number of requests of at least 1", source, value)
	}
	return webui.WorkLimits{MaxRequests: requests}, nil
}
