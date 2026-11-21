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

// droppedDefaultStatuses reports the statuses this project defines that Workbook
// has stopped shipping, with the command that removes each one.
//
// The test is provenance rather than membership, because the vocabulary alone
// cannot tell the `blocked` a project never migrated away from apart from one
// somebody added back on purpose — and telling the second reader about a
// decision they already made, on every listing, forever, with nothing to
// suppress it, is nagging rather than explaining. The ledger can tell them
// apart: a `status.add` after the genesis is recorded history, and a `blocked`
// the genesis itself carried is an inheritance. See blockedTracesToADecision
// for what the walk counts and what it does when the answer is out of reach.
//
// Provenance is answered first, ahead of every shape the note can take. A
// project that inherited the status and one that chose it are different readers
// before they are different configurations, and the ordering costs nothing: the
// membership gate above already keeps the walk off every listing that does not
// define `blocked` at all, so answering provenance first adds it to no listing
// that would otherwise have skipped it.
//
// A project whose new tasks land in the dropped status gets the same note with
// a different next step rather than no note at all. `status delete` refuses to
// forward a status into itself and refuses to leave a project with nowhere for
// new work to land, so the removal is not a command that exists yet for them —
// but they are the readers with the most invested in the column and the least
// reason to be told nothing. Naming the tag handoff is the whole difference
// between "you have a status Workbook no longer ships" and silence.
//
// That is the one shape with no escape — no removal to run, no window to fall
// out of — which is exactly why it sits below the provenance test rather than
// above it. A project that added the column and made it the place new work
// lands built a workflow deliberately, and advice on dismantling it is not
// something to repeat on every listing forever; `status delete` explains the
// handoff at the moment somebody actually tries the removal.
func droppedDefaultStatuses(vocabulary core.Vocabulary, ledger configLedgerWindow) []statusMigrationView {
	if !vocabulary.Has(core.StatusBlocked) {
		return nil
	}
	if blockedTracesToADecision(ledger) {
		return nil
	}
	reason := "task dependencies record what a task is waiting on, so `blocked` is no longer " +
		"one of the statuses Workbook gives a new project"
	if vocabulary.Default() == core.StatusBlocked {
		return []statusMigrationView{{
			Status: core.StatusBlocked,
			Reason: reason + ", and this project's new tasks land in it",
			// Written out rather than built by statusCommand: `<status>` is a
			// placeholder for a person to replace, and quoting it the way a real
			// argument is quoted would make it read as a status called
			// "<status>". This is the same spelling the arity refusals use.
			First: "workbook status tag <status> --tag " + string(core.StatusTagDefault),
		}}
	}
	return []statusMigrationView{{
		Status: core.StatusBlocked,
		Reason: reason,
		Command: statusCommand("delete", string(core.StatusBlocked),
			"--into", string(vocabulary.Default())),
	}}
}

// blockedTracesToADecision reports whether the `blocked` this project defines
// was put there by somebody rather than inherited.
//
// The walk is over the whole read window in order, keeping the last operation
// that established the name, because a status can be established and retired
// more than once: a project that removed `blocked` and later added it back
// decided twice, and the second decision is the one the reader is living with.
// The arms that reset are restatements of the caller's invariant rather than
// answers this reaches; see the note on the rename case.
//
// An answer this cannot reach is inherited. The window is bounded (see
// maxDatedConfigCommits), so an add older than it looks exactly like no add at
// all, and the two directions cost differently: showing the note to somebody who
// has already decided costs them a sentence they can act on once, while hiding
// it from a project that never migrated withholds the only place the migration
// is explained. So the conservative direction is to nag, and absence of evidence
// is never read as evidence of a decision. A project with no ledger — unseeded,
// on the legacy fallback — reaches this with an empty window and gets the note
// for the same reason.
func blockedTracesToADecision(ledger configLedgerWindow) bool {
	decided := false
	for _, commit := range ledger.Commits {
		for _, operation := range commit.Pack.Operations {
			switch operation.Type {
			case core.ConfigGenesis:
				decided = false
			case core.ConfigStatusAdd:
				if operation.Name == core.StatusBlocked {
					decided = true
				}
			case core.ConfigStatusRename:
				// Renaming a column onto the name is as deliberate as adding it.
				//
				// The three arms that reset — a genesis, a rename away, a removal —
				// are defensive restatements of an invariant the caller's membership
				// gate already enforces rather than decisions made here. This runs
				// only for a project whose `blocked` is live, and a live one is
				// always established by the newest add or rename onto the name that
				// the window holds, so nothing after such an operation can reset it
				// and no reset can survive to be returned. They are written out
				// because the invariant lives in another function and a walk that
				// silently depended on it would be wrong the day it moved — but the
				// provenance table does not cover them, because no input can reach
				// them and change the answer.
				switch core.StatusBlocked {
				case operation.To:
					decided = true
				case operation.From:
					decided = false
				}
			case core.ConfigStatusRemove:
				if operation.Status == core.StatusBlocked {
					decided = false
				}
			}
		}
	}
	return decided
}

// runStatusList reads the project's statuses without touching the network.
//
// It fetches nothing on purpose. Listing statuses is a read, and a read that
// synchronized would make `workbook status list` slower and less predictable
// than `workbook list` for no gain: the ledger this clone holds is what every
// other command in this shell is already using.
func runStatusList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("status", "list")
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
	service, releaseProjection, err := statusReadService(ctx, repository, config, state.Vocabulary)
	if err != nil {
		return err
	}
	defer releaseProjection()
	tasks, err := service.List(ctx, core.ListFilter{})
	if err != nil {
		return err
	}
	counts, unresolved := statusTaskCensus(state.Vocabulary, tasks)

	// Two of this listing's answers are read off the ledger — when a value was
	// retired, and whether the `blocked` a project still defines was chosen —
	// so it is read once, ahead of both. A project that has none skips the read
	// entirely: it has nothing retired to date, and nothing recorded to have
	// decided anything.
	var ledger configLedgerWindow
	if state.Seeded {
		ledger, err = readConfigLedgerWindow(ctx, repository, config, maxDatedConfigCommits)
		if err != nil {
			return err
		}
	}

	document := state.Vocabulary.Document()
	result := statusListResult{
		Head:       state.Head,
		Seeded:     state.Seeded,
		Default:    state.Vocabulary.Default(),
		Statuses:   statusViews(state.Vocabulary, counts),
		Retired:    retiredStatusViews(document, forwardingTimes(ledger)),
		Unresolved: unresolved,
		Advisories: historyvalidation.StatusCeilingAdvisories(document),
		Migrations: droppedDefaultStatuses(state.Vocabulary, ledger),
	}

	if *jsonMode {
		writeResult(stdout, "status list", result)
		return nil
	}
	return writeStatusList(stdout, result)
}

// writeStatusList renders the status table and the two note blocks under it.
//
// Retired and unresolved values are notes rather than rows because they are not
// columns: putting them in the table would make a board's worth of statuses
// indistinguishable from the values that merely still resolve into one.
func writeStatusList(output io.Writer, result statusListResult) error {
	rows := make([]terminalui.StatusRow, 0, len(result.Statuses))
	for _, status := range result.Statuses {
		tasks := ""
		if status.Tasks != nil {
			tasks = strconv.Itoa(*status.Tasks)
		}
		rows = append(rows, terminalui.StatusRow{
			Position: status.Order,
			Status:   string(status.Status),
			Label:    singleLine(status.Label),
			Tags:     statusTagsLine(status.Tags),
			Tasks:    tasks,
		})
	}
	width, measured := terminalWidth(output)
	if !measured {
		width = nonInteractiveWidth
	}
	if err := terminalui.RenderStatusList(output, rows, width); err != nil {
		return core.Wrap(core.CategoryOperational, "render status list", err)
	}
	if !result.Seeded {
		// Not "the statuses this project started with": a project can reach the
		// fallback by never having recorded anything and by having lost the
		// ledger that recorded something, and only one of those started here.
		// What is true in both is that nothing is recorded now.
		fmt.Fprintf(output, "\tNo status change is recorded, so these are the statuses Workbook reads for a project that has none of its own.\n")
	}
	for _, migration := range result.Migrations {
		fmt.Fprintf(output, "\tNo longer a default:\t%s\t%s\n", migration.Status, migration.Reason)
		if migration.Command != "" {
			fmt.Fprintf(output, "\t\tremove it when this project no longer needs it: %s\n", migration.Command)
			continue
		}
		fmt.Fprintf(output, "\t\tremoving it starts by giving the default tag to another status: %s\n", migration.First)
	}
	for _, retired := range result.Retired {
		fmt.Fprintf(output, "\tRetired:\t%s → %s\t%s%s\n",
			retired.Status, retired.Becomes, retirementVerb(retired.Operation), retiredOnClause(retired.At))
	}
	for _, unresolved := range result.Unresolved {
		fmt.Fprintf(output, "\tUnresolved:\t%s\t%d task(s)\t%s\n",
			unresolved.Status, unresolved.Tasks, unresolvedTaskIDsLine(unresolved))
		fmt.Fprintf(output, "\t\tcorrect with: workbook update <task> --status <status>, or define it again: %s\n",
			statusCommand("add", string(unresolved.Status)))
	}
	for _, advisory := range result.Advisories {
		fmt.Fprintf(output, "\tAdvisory:\t%s\t%s\n", advisory.Code, advisory.Message)
	}
	return nil
}

// unresolvedTaskIDsLine renders the tasks stranded under one value, saying so
// when it is showing only the first few.
//
// A sample that did not announce itself would read as the whole set, and a
// person who filed every ID they were given would think they were finished.
func unresolvedTaskIDsLine(unresolved unresolvedStatusView) string {
	ids := strings.Join(unresolved.TaskIDs, ", ")
	if len(unresolved.TaskIDs) >= unresolved.Tasks {
		return ids
	}
	return fmt.Sprintf("%s (first %d of %d)", ids, len(unresolved.TaskIDs), unresolved.Tasks)
}

// retirementVerb names how a value stopped being live, for a column rather
// than for a sentence: the arrow beside it already says where it went.
func retirementVerb(operation core.ConfigOperationType) string {
	if operation == core.ConfigStatusRemove {
		return "removed"
	}
	return "renamed"
}
