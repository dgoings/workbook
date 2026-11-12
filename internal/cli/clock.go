package cli

import (
	"os"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// recordedTimeVariable names the environment variable that fixes the time a
// command records, in place of the system clock. It exists to import a
// project's past: a migration replays each step at the time it really
// happened, so the history reads as it would have had Workbook been there to
// record it. Only what is written into history follows it — operation wall
// times, the timestamps inside new identifiers, and the configuration ledger.
// Timeouts, sync freshness, and relative ages stay on the real clock, because
// they measure this process rather than describe the work.
const recordedTimeVariable = "WORKBOOK_NOW"

// recordedTime reads recordedTimeVariable. Run calls it before dispatching any
// command, so an unreadable value is refused before anything is written
// rather than silently dating one step of a migration today.
func recordedTime() (time.Time, bool, error) {
	value := os.Getenv(recordedTimeVariable)
	if value == "" {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, core.Errorf(core.CategoryValidation,
			"%s=%q is not an RFC 3339 time such as 2024-03-05T14:00:00Z", recordedTimeVariable, value)
	}
	return at, true, nil
}

// recordedNow is the clock for everything a command records: the time
// recordedTimeVariable names when it is set, and the system clock otherwise.
func recordedNow() time.Time {
	// An unreadable value never gets this far; Run has already refused it.
	if at, set, err := recordedTime(); err == nil && set {
		return at
	}
	return time.Now()
}
