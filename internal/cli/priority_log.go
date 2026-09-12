package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityLog lists the recorded priority changes, oldest first.
//
// It mirrors runStatusLog's flags and refusals exactly, and departs from it in
// one place: the window. `status log` bounds the ledger read itself, because
// every commit it delivers is an entry. A priority change is a subsequence of
// the same ledger — a project can record forty status changes between two
// priority ones — so a bound on commits read is not a bound on changes shown,
// and a window of ten commits on such a project would show nothing while
// claiming to be the ten most recent changes. So the read is the whole ledger
// and the bound is applied to what it found, which is what makes --limit mean
// "this many priority changes" rather than "whatever was in the last N
// commits".
func runPriorityLog(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("priority", "log")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// The window is decided before anything is read. --limit is checked for
	// having been given at all rather than for being non-empty: `--limit=` sets
	// it to the empty string, which would otherwise slip past the conflict
	// check and let --all win silently.
	window := core.DefaultChangeLimit
	limited := false
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == "limit" {
			limited = true
		}
	})
	if limited {
		if *all {
			return core.Errorf(core.CategoryInvocation, "cannot use --limit with --all")
		}
		parsed, err := strconv.Atoi(*limit)
		if err != nil || parsed < 1 {
			return core.Errorf(core.CategoryInvocation, "priority log --limit must be a positive whole number")
		}
		window = parsed
	}
	if *all {
		window = 0
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	ledger, err := readConfigLedgerWindow(ctx, repository, config, 0)
	if err != nil {
		return err
	}
	result := buildPriorityLog(ledger, window)
	if *jsonMode {
		writeResult(stdout, "priority log", result)
		return nil
	}
	writePriorityLog(stdout, result, ledger.Found)
	return nil
}

// buildPriorityLog renders a read of the ledger as the priority change log.
//
// One entry is one commit that changed a priority. A commit that changed only
// statuses is not this log's news and is skipped rather than shown with an
// empty summary: the two sections share a ledger and do not share a history a
// reader is asking about.
//
// Total is the number of priority commits the ledger holds, not the ledger's
// length, so "showing 10 of 12" counts the things this log lists.
func buildPriorityLog(ledger configLedgerWindow, window int) priorityLogResult {
	entries := make([]priorityLogEntry, 0, len(ledger.Commits))
	for _, commit := range ledger.Commits {
		// Every member below is read from the authored operations rather than
		// from the recorded pack, because an entry is an account of what
		// somebody ran. The one commit where the two differ is a project's first
		// priority change, which gitstore records together with the built-in
		// three it had been using all along; see authoredPriorityOperations.
		authored := authoredPriorityOperations(commit.Before, commit.Pack.Operations)
		primary, found := subjectPriorityOperation(authored)
		if !found {
			continue
		}
		entries = append(entries, priorityLogEntry{
			Commit:      commit.Commit,
			OperationID: primary.ID,
			WallTime:    commit.Pack.WallTime,
			Actor:       commit.Pack.Actor.ID,
			Operation:   primary.Type,
			Summary:     priorityPackSummary(authored),
			// The backfill is left out of this count as well as out of the
			// summary, and that is the honest number rather than a convenient
			// one: "+3 more priority change(s)" would claim the commit changed
			// three priorities it did not. The project's priorities were the
			// built-in three before the commit — every reader substituted them —
			// and they are the built-in three plus the authored change after it.
			// What the backfill changed is where they are written down, which is
			// not news about a priority.
			Collapsed: priorityOperationCount(authored) - 1,
			Inverse:   priorityPackInverse(commit.Before, commit.Pack.Operations),
		})
	}
	total := len(entries)
	if window > 0 && total > window {
		entries = entries[total-window:]
	}
	return priorityLogResult{
		Total:     total,
		Showing:   len(entries),
		Entries:   entries,
		Truncated: ledger.Truncation,
	}
}

// writePriorityLog renders the log the way `status log` renders its own: oldest
// first, wall times as attribution only, and the window's size stated before
// its contents.
func writePriorityLog(output io.Writer, result priorityLogResult, found bool) {
	if !found {
		fmt.Fprintln(output, "No priority change is recorded; this project has not configured its priorities.")
		return
	}
	if result.Showing < result.Total {
		fmt.Fprintf(output, "Showing %d most recent changes out of %d.\n", result.Showing, result.Total)
	} else {
		fmt.Fprintf(output, "Showing all %d change(s).\n", result.Total)
	}
	for _, entry := range result.Entries {
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\n",
			entry.Commit,
			entry.WallTime.Format(time.RFC3339),
			singleLine(entry.Actor),
			singleLine(entry.Summary),
		)
		if entry.Inverse == nil {
			continue
		}
		exactness := "\t(not exact)"
		if entry.Inverse.Exact {
			exactness = ""
		}
		fmt.Fprintf(output, "\tinverse:\t%s%s\n", singleLine(entry.Inverse.Command), exactness)
		if entry.Inverse.Note != "" {
			fmt.Fprintf(output, "\tnote:\t%s\n", singleLine(entry.Inverse.Note))
		}
	}
	writeHistoryTruncation(output, result.Truncated)
}
