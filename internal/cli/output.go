package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/terminalui"
)

const usage = `Usage: workbook <command> [arguments]

Commands:
  setup [options]
  create <title> [options]
  list [options]
  board [--wide | --narrow] [--json]
  show <id-or-prefix> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--json]
  update <id-or-prefix> [options]
  delete <id-or-prefix> [--json]
  restore <id-or-prefix> [--json]
  move <id-or-prefix> (--before <id-or-prefix> | --after <id-or-prefix>) [--json]
  depend <id-or-prefix> <dependency-id-or-prefix> [--json]
  free <id-or-prefix> <dependency-id-or-prefix> [--json]
  next [--json]
  rebuild [--json]
  validate [--full] [--json]
  version [--json]
  fetch [--json]
  push [--json]
  sync [--watch [--interval <duration>]] [--status] [--json]
  config <command> [options]
  docs <command> [options]
  hooks install [--json]
  serve [--addr 127.0.0.1:7331]
`

func renderGlobalHelp(output io.Writer) {
	fmt.Fprintln(output, "Usage: workbook <command> [arguments]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	for _, name := range commandOrder {
		metadata := commandSchemas[name]
		fmt.Fprintf(output, "  %-8s %s\n", name, strings.TrimPrefix(metadata.Synopsis, "workbook "+name+" "))
	}
	fmt.Fprintln(output, "  help [command]")
}

func renderCommandHelp(output io.Writer, helpTarget []string) error {
	if len(helpTarget) == 0 {
		renderGlobalHelp(output)
		return nil
	}

	metadata, exists := commandSchemas[helpTarget[0]]
	if !exists {
		return core.Errorf(core.CategoryInvocation, "unknown command %q", helpTarget[0])
	}
	if len(helpTarget) > 2 {
		return core.Errorf(core.CategoryInvocation, "unknown %s command %q", metadata.Name, helpTarget[2])
	}
	if len(helpTarget) == 2 {
		var subcommandExists bool
		metadata, subcommandExists = metadata.Subcommands[helpTarget[1]]
		if !subcommandExists {
			return core.Errorf(core.CategoryInvocation, "unknown %s command %q", helpTarget[0], helpTarget[1])
		}
	}

	fmt.Fprintf(output, "Usage: %s\n", metadata.Synopsis)
	if metadata.Description != "" {
		fmt.Fprintf(output, "\n%s\n", metadata.Description)
	}
	if len(metadata.Subcommands) > 0 {
		fmt.Fprintln(output, "\nCommands:")
		for _, name := range metadata.SubcommandOrder {
			subcommand, exists := metadata.Subcommands[name]
			if exists {
				fmt.Fprintf(output, "  %-8s %s\n", name, subcommand.Description)
			}
		}
	}
	fmt.Fprintln(output, "\nOptions:")
	for _, option := range metadata.Options {
		name := "--" + option.Name
		if option.Value != "" {
			name += " " + option.Value
		}
		fmt.Fprintf(output, "  %-24s %s\n", name, option.Description)
	}
	fmt.Fprintf(output, "  %-24s %s\n", "-h, --help", "show help")
	return nil
}

type ResultEnvelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Command string `json:"command"`
	Data    any    `json:"data"`
	// Conflict lists every task whose local operations could not be replayed.
	// It is a list because one fetch reconciles every task at once and can stop
	// on several of them, and it lives on the envelope so one command reports
	// one list whatever mix of phases produced it.
	Conflict []core.Conflict `json:"conflict,omitempty"`
	// ConfigConflict lists every status change whose local operations could not
	// be replayed. It is a second list rather than more members on the first
	// because a task conflict is reported against a task ID and a
	// configuration conflict against a status, and merging them would give
	// every consumer of one a member that can never be populated for it.
	ConfigConflict []core.ConfigConflict `json:"configConflict,omitempty"`
	Warnings       []core.Warning        `json:"warnings,omitempty"`
	Sync           *syncReport           `json:"sync,omitempty"`
}

type ErrorBody struct {
	Category core.Category `json:"category"`
	Message  string        `json:"message"`
}

type ErrorEnvelope struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

func writeResult(output io.Writer, command string, data any) {
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:  "workbook.result",
		Version: 1,
		Command: command,
		Data:    data,
	})
}

func writeSyncPhaseResult(
	output io.Writer,
	command string,
	data any,
	conflicts []core.Conflict,
	jsonMode bool,
	renderText func(io.Writer),
) {
	writeSyncPhaseResultWithConfig(output, command, data, conflicts, nil, jsonMode, renderText)
}

func writeSyncPhaseResultWithConfig(
	output io.Writer,
	command string,
	data any,
	conflicts []core.Conflict,
	configConflicts []core.ConfigConflict,
	jsonMode bool,
	renderText func(io.Writer),
) {
	if jsonMode {
		_ = json.NewEncoder(output).Encode(ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        command,
			Data:           data,
			Conflict:       conflicts,
			ConfigConflict: configConflicts,
		})
		return
	}
	renderText(output)
	writeConflicts(output, conflicts)
	writeConfigConflicts(output, configConflicts)
}

// writeConfigConflicts renders the same list the JSON envelope carries. Every
// line names the status rather than a task, because that is what the two
// intents disagreed about and what a person has to decide.
func writeConfigConflicts(output io.Writer, conflicts []core.ConfigConflict) {
	for _, conflict := range conflicts {
		fmt.Fprintf(output, "Config conflict:\t%s\t%s\t%s\n",
			conflict.Status, conflict.Type, core.ConfigConflictDetail(conflict))
		if conflict.Ours != "" || conflict.Theirs != "" {
			fmt.Fprintf(output, "\tours:\t%s\n", singleLine(conflict.Ours))
			fmt.Fprintf(output, "\ttheirs:\t%s\n", singleLine(conflict.Theirs))
		}
	}
}

// writeConfigWarning states what a command could not settle about the project
// configuration, on the same channel every other warning uses. It is the
// configuration ledger's half of the reporting the identity ref established: a
// push that origin accepts while refusing the ledger beside it exits zero, and
// that is exactly the state that leaves teammates rendering columns nothing
// explains.
func writeConfigWarning(stderr io.Writer, result *gitstore.SyncConfigResult) {
	if result == nil {
		return
	}
	if detail, found := result.Warning(); found {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", detail)
	}
}

func writeMutationResult(
	stdout, stderr io.Writer,
	command string,
	result core.MutationResult,
	sync *syncReport,
	conflicts []core.Conflict,
	jsonMode bool,
) {
	var configConflicts []core.ConfigConflict
	if sync != nil {
		configConflicts = sync.configConflicts
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        command,
			Data:           result.Task,
			Conflict:       conflicts,
			ConfigConflict: configConflicts,
			Warnings:       result.Warnings,
			Sync:           sync,
		})
		return
	}

	writeMutation(stdout, result.Task)
	writeSyncReport(stdout, sync)
	writeConflicts(stdout, conflicts)
	writeConfigConflicts(stdout, configConflicts)
	for _, warning := range result.Warnings {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", warning.Message)
	}
	if sync != nil {
		writeIdentityWarning(stderr, sync.Identity)
		writeConfigWarning(stderr, sync.Config)
	}
}

// writeMutationOutcome reports what the command did, including when a conflict
// stopped it before it did anything. A conflicted mutation still writes a
// result envelope because the conflict list is the only place the caller can
// read the values it needs in order to retry.
func writeMutationOutcome(
	stdout, stderr io.Writer,
	command string,
	session *taskSession,
	result core.MutationResult,
	err error,
	jsonMode bool,
) error {
	if err != nil {
		if core.CategoryOf(err) != core.CategoryConflict {
			return err
		}
		writeSyncPhaseResult(stdout, command, nil, session.conflicts, jsonMode, func(io.Writer) {})
		return err
	}
	writeMutationResult(stdout, stderr, command, result, &session.report, session.conflicts, jsonMode)
	return nil
}

// writeConflicts renders the same list the JSON envelope carries. The three
// values a description conflict reports are printed in full because retyping
// the wanted text is the whole resolution.
func writeConflicts(output io.Writer, conflicts []core.Conflict) {
	for _, conflict := range conflicts {
		fmt.Fprintf(output, "Conflict:\t%s\t%s\t%s\n", conflict.TaskID, conflict.Type, core.ConflictDetail(conflict))
		switch {
		case conflict.Description != nil:
			fmt.Fprintf(output, "\tbase:\t%s\n", singleLine(conflict.Description.Base))
			fmt.Fprintf(output, "\tours:\t%s\n", singleLine(conflict.Description.Ours))
			fmt.Fprintf(output, "\ttheirs:\t%s\n", singleLine(conflict.Description.Theirs))
		case conflict.Dependency != nil:
			fmt.Fprintf(output, "\tedge:\t%s → %s\n", conflict.Dependency.From, conflict.Dependency.To)
			fmt.Fprintf(output, "\tpath:\t%s\n", strings.Join(conflict.Dependency.Path, " → "))
		case conflict.Tombstone != nil:
			fmt.Fprintf(output, "\tblocked:\t%s\t%s\n", conflict.Tombstone.OperationID, conflict.Tombstone.Operation)
		}
	}
}

// singleLine renders untrusted task text for one text-mode line. It shares
// core.DisplayLine so an ESC sequence cannot redraw the row and a newline
// cannot forge a structured field line; JSON output stays byte-exact.
func singleLine(value string) string {
	return core.DisplayLine(value)
}

// writeSyncReport prints one line only when synchronization was attempted.
// A command the user deliberately kept local should not gain output saying so.
//
// A fetch that skipped a ref under origin's task namespace names it here too.
// Synchronization succeeded despite that ref, so this line is otherwise the
// whole account a mutation gives of what it saw on origin, and the JSON
// envelope was the only place the report reached anyone.
func writeSyncReport(output io.Writer, sync *syncReport) {
	if sync == nil || !sync.Enabled {
		return
	}
	fmt.Fprintf(output, "Sync:\t%s", sync.Status)
	if sync.Detail != "" {
		fmt.Fprintf(output, "\t%s", sync.Detail)
	}
	fmt.Fprintln(output)
	if sync.Fetch != nil {
		writeIgnoredRefs(output, sync.Fetch.Remote, sync.Fetch.Ignored)
	}
}

func writeSyncedResult(output io.Writer, command string, data any, sync *syncReport, conflicts []core.Conflict) {
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:   "workbook.result",
		Version:  1,
		Command:  command,
		Data:     data,
		Conflict: conflicts,
		Sync:     sync,
	})
}

func writeError(output io.Writer, err error, jsonMode bool) {
	if jsonMode {
		_ = json.NewEncoder(output).Encode(ErrorEnvelope{
			Format:  "workbook.error",
			Version: 1,
			Error: ErrorBody{
				Category: core.CategoryOf(err),
				Message:  publicErrorMessage(err),
			},
		})
		return
	}

	fmt.Fprintf(output, "workbook: %s\n", publicErrorMessage(err))
	if core.CategoryOf(err) == core.CategoryInvocation {
		fmt.Fprint(output, usage)
	}
}

func publicErrorMessage(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		if core.CategoryOf(err) == core.CategoryOperational {
			return err.Error()
		}
		return typed.Message
	}
	return err.Error()
}

func writeMutation(output io.Writer, task core.Task) {
	fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Priority, singleLine(task.Title))
}

func writeSyncResult(output io.Writer, result gitstore.SyncResult) {
	if result.Status == gitstore.SyncPhaseFailed {
		fmt.Fprintf(output, "Failed on %s", result.Remote)
		if result.Detail != "" {
			fmt.Fprintf(output, ": %s", result.Detail)
		}
		fmt.Fprintln(output)
		// A phase that observed origin's namespace before failing later still
		// knows which refs it skipped, and they may be why it is being read.
		writeIgnoredRefs(output, result.Remote, result.Ignored)
		return
	}
	if result.Status == gitstore.SyncPhaseSkipped {
		fmt.Fprintf(output, "Skipped on %s", result.Remote)
		if result.Detail != "" {
			fmt.Fprintf(output, ": %s", result.Detail)
		}
		fmt.Fprintln(output)
		return
	}
	writeIgnoredRefs(output, result.Remote, result.Ignored)
	if len(result.Tasks) == 0 {
		fmt.Fprintf(output, "No task refs on %s.\n", result.Remote)
		return
	}
	for _, task := range result.Tasks {
		fmt.Fprintf(output, "%s\t%s", task.TaskID, task.Status)
		if task.Detail != "" {
			fmt.Fprintf(output, "\t%s", task.Detail)
		}
		fmt.Fprintln(output)
	}
}

// Every ignored ref carries one of these verdicts in its own line, so the
// footer that offers a deletion command and the footer that warns against one
// each name the verdict they apply to. A reader never has to decide which of
// several listed names a command was meant for, and never has to infer it from
// a validator's message.
const (
	ignoredRefRemovable = "no project's task"
	ignoredRefPlausible = "may be another Workbook's task"
)

// writeIgnoredRefs names every skipped ref, whether any project's ID format
// could produce that name, why it was skipped, and — only when some name no
// project can own is listed — the command that removes one. Synchronization
// succeeded despite these refs, so the report is the only thing standing
// between a poisoned namespace and nobody noticing.
//
// It takes the refs rather than a phase because the same report is written for
// one phase, for a whole run, and for what a watcher last observed.
//
// It is also the only place Workbook suggests deleting anything from a shared
// remote, and shared task history is append-only. A name this build does not
// recognize can still be a task written by a newer version or under a second
// project's key, so every line says which of the two it is, a warning stands in
// for the command on the ones that may be history, and even the command that is
// offered is phrased as a decision the reader makes about a specific ref rather
// than a step to take.
// writeIdentityWarning states what a command could not settle about the
// project identity, on the same channel every other warning uses.
//
// It is deliberately not conditional on the command having failed. A push that
// origin accepts while refusing the identity ref beside it exits zero, and that
// is exactly the state that leaves a later clone with task refs it cannot
// attribute to a project — so it has to be said out loud.
func writeIdentityWarning(stderr io.Writer, identity *gitstore.SyncIdentityResult) {
	if identity == nil {
		return
	}
	if detail, found := identity.Warning(); found {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", detail)
	}
}

func writeIgnoredRefs(output io.Writer, remote string, refs []gitstore.IgnoredRef) {
	if len(refs) == 0 {
		return
	}
	removable := 0
	for _, ignored := range refs {
		verdict := ignoredRefPlausible
		if !ignored.PlausibleTask {
			verdict = ignoredRefRemovable
			removable++
		}
		fmt.Fprintf(output, "Ignored:\t%s\t%s\t%s\n", ignored.Ref, verdict, ignored.Reason)
	}
	fmt.Fprintf(output, "\tkept on %s; Workbook deletes no ref there.\n", remote)
	if removable < len(refs) {
		fmt.Fprintf(output, "\tdeleting a %q ref would destroy a newer Workbook's or another project's history.\n",
			ignoredRefPlausible)
	}
	if removable > 0 {
		fmt.Fprintf(output, "\tremove a %q ref with: git push %s --delete <ref>\n",
			ignoredRefRemovable, remote)
	}
}

func writeSyncRunResult(output io.Writer, result gitstore.SyncRunResult) {
	fmt.Fprintln(output, "Fetch:")
	writeSyncResult(output, result.Fetch)
	fmt.Fprintln(output, "Push:")
	writeSyncResult(output, result.Push)
}

func writeList(output io.Writer, tasks []core.Task) error {
	width, measured := terminalWidth(output)
	if !measured {
		width = nonInteractiveWidth
	}
	return terminalui.RenderList(output, tasks, width)
}

func writeShowDetail(output io.Writer, detail core.TaskDetail) {
	writeShow(output, detail.Task)
	if detail.History != nil {
		writeChangeLog(output, *detail.History)
	}
	if detail.Comparison != nil {
		writeComparison(output, *detail.Comparison)
	}
}

// writeChangeLog prints the chain oldest first and its wall times as
// attribution only. After a reconciliation those timestamps legitimately read
// out of order; that disagreement is the visible fingerprint of replayed work,
// so it is shown rather than sorted away.
func writeChangeLog(output io.Writer, log core.ChangeLog) {
	fmt.Fprintln(output)
	if log.Showing < log.Total {
		fmt.Fprintf(output, "Showing %d most recent changes out of %d.\n", log.Showing, log.Total)
	} else {
		fmt.Fprintf(output, "Showing all %d change(s).\n", log.Total)
	}
	for _, change := range log.Changes {
		fmt.Fprintf(
			output,
			"%s\t%s\t%s\t%s\n",
			change.Commit,
			change.WallTime.Format(time.RFC3339),
			singleLine(change.Actor),
			singleLine(change.Summary),
		)
		writeFieldChanges(output, change.Fields)
	}
	writeHistoryTruncation(output, log.Truncated)
}

func writeComparison(output io.Writer, comparison core.Comparison) {
	fmt.Fprintln(output)
	fmt.Fprintf(output, "Comparing:\t%s\t→\t%s\n", comparison.From, comparison.To)
	if len(comparison.Fields) == 0 {
		fmt.Fprintln(output, "\tThese two points hold the same task state.")
		return
	}
	writeFieldChanges(output, comparison.Fields)
}

func writeFieldChanges(output io.Writer, fields []core.FieldChange) {
	for _, change := range fields {
		label := core.FieldLabel(change.Field)
		switch change.Kind {
		case core.ChangeReordered:
			fmt.Fprintf(output, "\t%s:\tReordered\n", label)
		case core.ChangeAdded:
			fmt.Fprintf(output, "\t%s:\t+%s\n", label, singleLine(change.To))
		case core.ChangeRemoved:
			fmt.Fprintf(output, "\t%s:\t-%s\n", label, singleLine(change.From))
		case core.ChangeCreated:
			fmt.Fprintf(output, "\t%s:\tCreated\t%s\n", label, singleLine(change.To))
		case core.ChangeDeleted:
			fmt.Fprintf(output, "\t%s:\tDeleted\n", label)
		case core.ChangeRestored:
			fmt.Fprintf(output, "\t%s:\tRestored\n", label)
		default:
			if len(change.Diff) > 0 {
				fmt.Fprintf(output, "\t%s:\t%s\n", label, renderWordDiff(change.Diff))
				continue
			}
			fmt.Fprintf(output, "\t%s:\t%s → %s\n", label, singleLine(change.From), singleLine(change.To))
		}
	}
}

// renderWordDiff marks removed and added runs the way `git diff --word-diff`
// does, so the surrounding prose stays readable and the change is copyable.
func renderWordDiff(spans []core.DiffSpan) string {
	var rendered strings.Builder
	for _, span := range spans {
		switch span.Kind {
		case core.DiffDelete:
			rendered.WriteString("[-" + span.Text + "-]")
		case core.DiffInsert:
			rendered.WriteString("{+" + span.Text + "+}")
		default:
			rendered.WriteString(span.Text)
		}
	}
	return singleLine(rendered.String())
}

func writeHistoryTruncation(output io.Writer, truncation *core.HistoryTruncation) {
	if truncation == nil {
		return
	}
	fmt.Fprintf(output, "Truncated at %s: %s\n", truncation.Commit, truncation.Message)
}

func writeShow(output io.Writer, task core.Task) {
	fmt.Fprintf(output, "ID:\t%s\n", task.ID)
	fmt.Fprintf(output, "Project ID:\t%s\n", task.ProjectID)
	fmt.Fprintf(output, "Title:\t%s\n", singleLine(task.Title))
	writeDescription(output, task.Description)
	fmt.Fprintf(output, "Status:\t%s\n", task.Status)
	fmt.Fprintf(output, "Priority:\t%s\n", task.Priority)
	fmt.Fprintf(output, "Labels:\t%s\n", singleLine(strings.Join(task.Labels, ",")))
	fmt.Fprintf(output, "Rank:\t%s\n", task.Rank)
	fmt.Fprintf(output, "Dependencies:\t%s\n", strings.Join(task.Dependencies, ","))
	fmt.Fprintf(output, "Created At:\t%s\n", task.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Updated At:\t%s\n", task.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Deleted:\t%t\n", task.Deleted)
	fmt.Fprintf(output, "History Generation:\t%s\n", task.HistoryGeneration)
	fmt.Fprintf(output, "Head:\t%s\n", task.Head)
}

// writeDescription prints a description over as many lines as it was written
// with. Every line is sanitized on its own by singleLine, so no line can carry
// an escape sequence, and every line after the first is indented with a tab.
// That indent is what keeps a description out of writeShow's field block: every
// field it prints begins at column zero, so "Status: done" written into a
// description renders as "\tStatus: done" and reads as description text.
//
// The indent buys nothing against the detail sections. writeFieldChanges and
// writeConflicts mark their structured lines with the same tab, and once a
// terminal expands the tabs a description line reading "Status: backlog → done"
// is indistinguishable from a real change-log line. What fences those sections
// off is the column-zero header each one opens with, which a description cannot
// reach for the same reason it cannot forge a field.
//
// Only the line breaks survive. singleLine drops each line's leading
// indentation, so nested lists and indented code blocks still flatten; --json
// is the interface for a description whose indentation carries meaning.
func writeDescription(output io.Writer, description string) {
	lines := descriptionLines(description)
	fmt.Fprintf(output, "Description:\t%s\n", lines[0])
	for _, line := range lines[1:] {
		// A blank line separates paragraphs. It is printed empty rather than as
		// a lone tab because trailing whitespace serves nobody, and an empty
		// line cannot be read as a field either.
		if line == "" {
			fmt.Fprintln(output)
			continue
		}
		fmt.Fprintf(output, "\t%s\n", line)
	}
}

// descriptionLines sanitizes a description into the lines to print, always at
// least one. Trailing blank lines are dropped: a description that ends with a
// newline should not push a blank line between it and the next field.
func descriptionLines(description string) []string {
	lines := strings.Split(description, "\n")
	for i, line := range lines {
		lines[i] = singleLine(line)
	}
	for len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
