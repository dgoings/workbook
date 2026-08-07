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
	Warnings []core.Warning  `json:"warnings,omitempty"`
	Sync     *syncReport     `json:"sync,omitempty"`
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
	if jsonMode {
		_ = json.NewEncoder(output).Encode(ResultEnvelope{
			Format:   "workbook.result",
			Version:  1,
			Command:  command,
			Data:     data,
			Conflict: conflicts,
		})
		return
	}
	renderText(output)
	writeConflicts(output, conflicts)
}

func writeMutationResult(
	stdout, stderr io.Writer,
	command string,
	result core.MutationResult,
	sync *syncReport,
	conflicts []core.Conflict,
	jsonMode bool,
) {
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:   "workbook.result",
			Version:  1,
			Command:  command,
			Data:     result.Task,
			Conflict: conflicts,
			Warnings: result.Warnings,
			Sync:     sync,
		})
		return
	}

	writeMutation(stdout, result.Task)
	writeSyncReport(stdout, sync)
	writeConflicts(stdout, conflicts)
	for _, warning := range result.Warnings {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", warning.Message)
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

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// writeSyncReport prints one line only when synchronization was attempted.
// A command the user deliberately kept local should not gain output saying so.
func writeSyncReport(output io.Writer, sync *syncReport) {
	if sync == nil || !sync.Enabled {
		return
	}
	fmt.Fprintf(output, "Sync:\t%s", sync.Status)
	if sync.Detail != "" {
		fmt.Fprintf(output, "\t%s", sync.Detail)
	}
	fmt.Fprintln(output)
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
	fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Priority, task.Title)
}

func writeSyncResult(output io.Writer, result gitstore.SyncResult) {
	// Ignored refs are reported before the phase outcome because they are true
	// whatever the phase did, and because the reader needs the prune command.
	// The remote is this tool's own constant; only the ref name came from
	// origin, so only it is quoted.
	for _, ignored := range result.Ignored {
		fmt.Fprintf(output, "Ignored %s on %s: %s. Prune it with: git push %s %s\n",
			ignored.Ref, result.Remote, ignored.Reason, result.Remote, shellWord(":"+ignored.Ref))
	}
	if result.Status == gitstore.SyncPhaseFailed {
		fmt.Fprintf(output, "Failed on %s", result.Remote)
		if result.Detail != "" {
			fmt.Fprintf(output, ": %s", result.Detail)
		}
		fmt.Fprintln(output)
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

// shellWord renders a value as one POSIX shell word so that a command this
// tool invites the user to paste stays one command with one argument.
//
// A ref name under origin's task namespace is chosen by whoever pushed it.
// Git's own refname rules ban control characters and spaces but allow ';',
// '$', '`', '&', '|', and parentheses, so interpolating such a name unquoted
// would both run whatever it says and prune the wrong ref. Single quotes
// suppress every expansion, and an embedded quote is closed, escaped, and
// reopened. Quoting is unconditional: deciding a name looks harmless is the
// same judgment this function exists to avoid.
func shellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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
			change.Actor,
			change.Summary,
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
	fmt.Fprintf(output, "Title:\t%s\n", task.Title)
	fmt.Fprintf(output, "Description:\t%s\n", task.Description)
	fmt.Fprintf(output, "Status:\t%s\n", task.Status)
	fmt.Fprintf(output, "Priority:\t%s\n", task.Priority)
	fmt.Fprintf(output, "Labels:\t%s\n", strings.Join(task.Labels, ","))
	fmt.Fprintf(output, "Rank:\t%s\n", task.Rank)
	fmt.Fprintf(output, "Dependencies:\t%s\n", strings.Join(task.Dependencies, ","))
	fmt.Fprintf(output, "Created At:\t%s\n", task.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Updated At:\t%s\n", task.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Deleted:\t%t\n", task.Deleted)
	fmt.Fprintf(output, "History Generation:\t%s\n", task.HistoryGeneration)
	fmt.Fprintf(output, "Head:\t%s\n", task.Head)
}
