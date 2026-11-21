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

func runStatusLog(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("status", "log")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// The window is decided before anything is read, because it is what bounds
	// the read. --limit is checked for having been given at all rather than for
	// being non-empty: `--limit=` sets it to the empty string, which used to
	// slip past the conflict check and let --all win silently.
	window := core.DefaultChangeLimit
	limited := false
	var offending error
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
			return core.Errorf(core.CategoryInvocation, "status log --limit must be a positive whole number")
		}
		window = parsed
	}
	if offending != nil {
		return offending
	}
	if *all {
		window = 0
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	ledger, err := readConfigLedgerWindow(ctx, repository, config, window)
	if err != nil {
		return err
	}
	result := buildStatusLog(ledger)
	if *jsonMode {
		writeResult(stdout, "status log", result)
		return nil
	}
	writeStatusLog(stdout, result, ledger.Found)
	return nil
}

// buildStatusLog renders a read window as the change log.
//
// One entry is one commit, which is the same unit `show --history` counts: a
// Change there is one operation pack, whatever the pack contains. That matters
// here beyond symmetry, because a commit is also one command — `workbook status
// tag` records a transfer and the tags it dropped in one — and the inverse worth
// printing is the one that undoes the command somebody ran rather than a
// fragment of it. It is also what makes the total exact under a bounded read:
// counting commits costs the walk, counting operations would cost reading every
// pack.
func buildStatusLog(ledger configLedgerWindow) statusLogResult {
	entries := make([]statusLogEntry, 0, len(ledger.Commits))
	for _, commit := range ledger.Commits {
		// The authored operations, not the recorded pack. Two sections record a
		// backfill ahead of a project's first change to them — the founding key
		// and the built-in priorities — and an entry built from operations[0]
		// names, summarizes, counts and identifies that prefix while the
		// inverse beside it, which already strips, describes the change
		// somebody ran. One entry then says two different things about one
		// commit. Both strippings are idempotent and neither can fire on the
		// other's prefix, since each is identified by its own first operation.
		operations := authoredKeyOperations(commit.Before.keys, commit.Pack.Operations)
		operations = authoredPriorityOperations(commit.Before, operations)
		if len(operations) == 0 {
			continue
		}
		primary := operations[0]
		entries = append(entries, statusLogEntry{
			Commit:      commit.Commit,
			OperationID: primary.ID,
			WallTime:    commit.Pack.WallTime,
			Actor:       commit.Pack.Actor.ID,
			Operation:   primary.Type,
			Summary:     configPackSummary(operations),
			Collapsed:   len(operations) - 1,
			Inverse:     statusPackInverse(commit.Before, operations),
		})
	}
	return statusLogResult{
		Total:     ledger.Total,
		Showing:   len(entries),
		Entries:   entries,
		Truncated: ledger.Truncation,
	}
}

// writeStatusLog renders the ledger the way `show --history` renders a task's
// chain: oldest first, wall times as attribution only, and the window's size
// stated before its contents.
func writeStatusLog(output io.Writer, result statusLogResult, found bool) {
	if !found {
		fmt.Fprintln(output, "No status change is recorded; this project has not configured its statuses.")
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
