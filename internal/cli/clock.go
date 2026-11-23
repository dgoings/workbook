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
// A compact run follows it too, cutoff included, so its checkpoints agree with
// the identifiers minted beside them. Timeouts, sync freshness, and relative
// ages stay on the real clock, because they measure this process rather than
// describe the work.
const recordedTimeVariable = "WORKBOOK_NOW"

// recordedTime reads recordedTimeVariable. Run calls it before dispatching any
// command but help and version, which record nothing, so an unreadable value
// is refused before anything is written rather than silently dating one step
// of a migration today.
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
	return recordedOr(time.Now)
}

// sessionNow is recordedNow for a task session's mutations. With no override
// it reads commandNow, the seam tests date their changes through.
func sessionNow() time.Time {
	return recordedOr(commandNow)
}

// compactionNow is recordedNow for a compact run, which measures its cutoff
// from one instant and records its checkpoints at that same instant. With no
// override it reads compactNow, the seam compaction's tests move.
func compactionNow() time.Time {
	return recordedOr(compactNow)
}

// recordedToday is the instant recordedNow names, in this machine's zone, so
// its calendar day is the writer's local date. That is what a relative due
// date such as `today` or `+3d` is resolved against. The system clock already
// reads in the local zone; a WORKBOOK_NOW value carries whatever offset it was
// written with, and 2026-03-01T02:00:00Z is still February 28 for a writer in
// New York, so it is moved into the local zone too — one rule for both clocks.
func recordedToday() time.Time {
	return recordedNow().Local()
}

func recordedOr(fallback func() time.Time) time.Time {
	// An unreadable value never gets this far; Run has already refused it.
	if at, set, err := recordedTime(); err == nil && set {
		return at
	}
	return fallback()
}
