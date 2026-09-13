package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/terminalui"
)

// runPriorityList reads the project's priorities without touching the network.
//
// It fetches nothing on purpose, exactly as `status list` does not: listing
// priorities is a read, and a read that synchronized would make this slower and
// less predictable than `workbook list` for no gain — the ledger this clone
// holds is what every other command in this shell is already using.
func runPriorityList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("priority", "list")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	service, err := priorityReadService(ctx, repository, config, state.Vocabulary, state.Priorities)
	if err != nil {
		return err
	}
	tasks, err := service.List(ctx, core.ListFilter{})
	if err != nil {
		return err
	}
	counts, unresolved := priorityTaskCensus(state.Priorities, tasks)

	// The one answer this listing reads off the ledger is when a value was
	// retired, so a project with nothing retired skips the read entirely.
	var ledger configLedgerWindow
	if state.Seeded && len(state.Priorities.Document().Aliases)+len(state.Priorities.Document().Retired) > 0 {
		ledger, err = readConfigLedgerWindow(ctx, repository, config, maxDatedConfigCommits)
		if err != nil {
			return err
		}
	}

	// EffectiveDocument rather than Document: this is a reading, so the project
	// that has configured nothing is described by the built-in three it is
	// actually using rather than by an empty section. Nothing here is headed
	// for a checkpoint; see PriorityVocabulary.Document for why the two must
	// not be confused.
	document := state.Priorities.EffectiveDocument()
	result := priorityListResult{
		Head:       state.Head,
		Seeded:     state.Seeded,
		Default:    state.Priorities.Default(),
		Priorities: priorityViews(state.Priorities, counts),
		Retired:    retiredPriorityViews(document, priorityForwardingTimes(ledger)),
		Unresolved: unresolved,
		Advisories: historyvalidation.PriorityCeilingAdvisories(document),
	}

	if *jsonMode {
		writeResult(stdout, "priority list", result)
		return nil
	}
	return writePriorityList(stdout, result)
}

// writePriorityList renders the priority table and the note blocks under it.
//
// Retired and unresolved values are notes rather than rows because they are not
// priorities: putting them in the table would make a project's actual
// priorities indistinguishable from the values that merely still resolve into
// one.
func writePriorityList(output io.Writer, result priorityListResult) error {
	rows := make([]terminalui.PriorityRow, 0, len(result.Priorities))
	for _, priority := range result.Priorities {
		tasks := ""
		if priority.Tasks != nil {
			tasks = strconv.Itoa(*priority.Tasks)
		}
		rows = append(rows, terminalui.PriorityRow{
			Position: priority.Order,
			Priority: string(priority.Priority),
			Label:    singleLine(priority.Label),
			Tags:     priorityTagsLine(priority.Tags),
			Color:    priority.Color,
			Tasks:    tasks,
		})
	}
	width, measured := terminalWidth(output)
	if !measured {
		width = nonInteractiveWidth
	}
	if err := terminalui.RenderPriorityList(output, rows, width); err != nil {
		return core.Wrap(core.CategoryOperational, "render priority list", err)
	}
	if !result.Seeded {
		// Not "the priorities this project started with": a project can reach
		// the fallback by never having recorded anything and by having lost the
		// ledger that recorded something, and only one of those started here.
		// What is true in both is that nothing is recorded now.
		fmt.Fprintf(output, "\tNo priority change is recorded, so these are the priorities Workbook reads for a project that has none of its own.\n")
	}
	for _, retired := range result.Retired {
		fmt.Fprintf(output, "\tRetired:\t%s → %s\t%s%s\n",
			retired.Priority, retired.Becomes,
			priorityRetirementVerb(retired.Operation), retiredOnClause(retired.At))
	}
	for _, unresolved := range result.Unresolved {
		fmt.Fprintf(output, "\tUnresolved:\t%s\t%d task(s)\t%s\n",
			unresolved.Priority, unresolved.Tasks, unresolvedPriorityTaskIDsLine(unresolved))
		fmt.Fprintf(output, "\t\tcorrect with: workbook update <task> --priority <priority>, or define it again: %s\n",
			priorityCommand("add", string(unresolved.Priority)))
	}
	for _, advisory := range result.Advisories {
		fmt.Fprintf(output, "\tAdvisory:\t%s\t%s\n", advisory.Code, advisory.Message)
	}
	return nil
}

// unresolvedPriorityTaskIDsLine renders the tasks stranded under one value,
// saying so when it is showing only the first few. A sample that did not
// announce itself would read as the whole set, and a person who filed every ID
// they were given would think they were finished.
func unresolvedPriorityTaskIDsLine(unresolved unresolvedPriorityView) string {
	ids := strings.Join(unresolved.TaskIDs, ", ")
	if len(unresolved.TaskIDs) >= unresolved.Tasks {
		return ids
	}
	return fmt.Sprintf("%s (first %d of %d)", ids, len(unresolved.TaskIDs), unresolved.Tasks)
}

// priorityRetirementVerb names how a value stopped being live, for a column
// rather than for a sentence: the arrow beside it already says where it went.
func priorityRetirementVerb(operation core.ConfigOperationType) string {
	if operation == core.ConfigPriorityRemove {
		return "removed"
	}
	return "renamed"
}
