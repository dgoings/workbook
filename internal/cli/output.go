package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/presentation"
	"github.com/dgoings/workbook/internal/terminalui"
)

const usage = `Usage: workbook <command> [arguments]

Commands:
  setup [options]
  create <title> [options]
  list [options]
  show <id-or-prefix> [--history [--limit <n>] [--all]] [--compare <commit> <commit>] [--get-attachment <id-or-prefix> [--out <path>]] [--json]
  update <id-or-prefix> [options]
  delete <id-or-prefix> [--json]
  restore <id-or-prefix> [--into <status>] [--json]
  move <id-or-prefix> (--before <id-or-prefix> | --after <id-or-prefix>) [--json]
  depend <id-or-prefix> <dependency-id-or-prefix> [--json]
  free <id-or-prefix> <dependency-id-or-prefix> [--json]
  next [--any] [--claim] [--limit <n>] [--no-sync] [--json]
  rebuild [--json]
  validate [--full] [--json]
  compact [--done-only [<duration>] | --deleted-only [<duration>]] [--since <duration>] [--keep-attachments] [--no-backup | --backup <path>] [--dry-run] [--no-sync] [--json]
  compact --relics [--retired-only | --deleted-only] [--dry-run] [--backup <path> | --no-backup] [--no-sync] [--json]
  compact --adopt <task> [--discard-local] [--no-backup | --backup <path>] [--json]
  version [--json]
  fetch [--remote <remote>] [--json]
  push [--remote <remote>] [--json]
  sync [--watch [--interval <duration>]] [--status] [--remote <remote>] [--json]
  move-tasks [--from <remote>] [--allow-branches] [--clean-up] [--no-backup | --backup <path>] [--json]
  reset [--dry-run] [--no-backup [--discard-unpublished] | --backup <path>] [--json]
  reset --remote <remote> [--confirm <remote>] [--dry-run] [--no-backup | --backup <path>] [--json]
  relic <command> [options]
  schema <command> [options]
  status <command> [options]
  priority <command> [options]
  key <command> [options]
  config <command> [options]
  docs <command> [options]
  hooks install [--json]
  serve [--addr 127.0.0.1:7331] [--public-url <url>] [--actor-header <header>]
  completion <shell>
`

func renderGlobalHelp(output io.Writer) {
	fmt.Fprintln(output, "Usage: workbook <command> [arguments]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	for _, name := range commandOrder {
		// A command with several forms lists each on its own line.
		for _, form := range strings.Split(commandSchemas[name].Synopsis, "\n") {
			fmt.Fprintf(output, "  %-8s %s\n", name, strings.TrimPrefix(form, "workbook "+name+" "))
		}
	}
	fmt.Fprintln(output, "  help [command]")
}

// helpProjectKey marks where a help text gives an example ID, which
// renderCommandHelpFor fills with the project's key.
const helpProjectKey = "\x00project-key\x00"

// defaultHelpProjectKey fills an example ID where no project key can be read,
// as outside a repository.
const defaultHelpProjectKey = "WB"

// helpKeyAt is the project key the help for cwd gives its example IDs in: the
// key of the project there, or defaultHelpProjectKey when there is none or it
// cannot be read. Help must work anywhere, so no failure here is reported.
func helpKeyAt(ctx context.Context, cwd string) string {
	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return defaultHelpProjectKey
	}
	config, err := repository.LoadConfig()
	if err != nil || config.Key == "" {
		return defaultHelpProjectKey
	}
	return config.Key
}

func renderCommandHelp(output io.Writer, helpTarget []string) error {
	return renderCommandHelpFor(output, helpTarget, defaultHelpProjectKey)
}

// renderCommandHelpFor renders a command's help with its example IDs in the
// project key projectKey.
func renderCommandHelpFor(output io.Writer, helpTarget []string, projectKey string) error {
	var rendered bytes.Buffer
	if err := renderCommandHelpText(&rendered, helpTarget); err != nil {
		return err
	}
	_, err := io.WriteString(output, strings.ReplaceAll(rendered.String(), helpProjectKey, projectKey))
	return err
}

func renderCommandHelpText(output io.Writer, helpTarget []string) error {
	if len(helpTarget) == 0 {
		renderGlobalHelp(output)
		return nil
	}

	metadata, exists := commandSchemas[helpTarget[0]]
	if !exists {
		return unknownCommandError(helpTarget[0])
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

	// Each further form lines up under the first, past "Usage: ".
	forms := strings.Split(metadata.Synopsis, "\n")
	fmt.Fprintf(output, "Usage: %s\n", forms[0])
	for _, form := range forms[1:] {
		fmt.Fprintf(output, "       %s\n", form)
	}
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
	// ConfigConflict lists every status or priority change whose local
	// operations could not be replayed. It is a second list rather than more
	// members on the first because a task conflict is reported against a task
	// ID and a configuration conflict against a status or a priority, and
	// merging them would give every consumer of one a member that can never be
	// populated for it.
	ConfigConflict []core.ConfigConflict `json:"configConflict,omitempty"`
	// RelicConflict lists every relic whose local changes could not be
	// replayed, a third list for the same reason: it names a relic and a part.
	RelicConflict []core.RelicConflict `json:"relicConflict,omitempty"`
	Warnings      []core.Warning       `json:"warnings,omitempty"`
	Sync          *syncReport          `json:"sync,omitempty"`
}

type ErrorBody struct {
	Category core.Category `json:"category"`
	Message  string        `json:"message"`
	// TasksRemote is present only on a refusal about the remote a project
	// shares its tasks through: which remote, why, and the command to run.
	TasksRemote *core.TasksRemoteRefusal `json:"tasksRemote,omitempty"`
	// Reset is present only on a refusal of workbook reset: why, and what it
	// would have removed or could not.
	Reset *gitstore.ResetRefusal `json:"reset,omitempty"`
}

type ErrorEnvelope struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

// nextCandidatesDocument is `next --limit`'s answer: the first tasks next would
// pick, in its order, and how many qualified in all, so a caller can show
// "and three more" without asking again. Tasks is never null.
type nextCandidatesDocument struct {
	Tasks    []core.Task `json:"tasks"`
	Eligible int         `json:"eligible"`
}

func writeResult(output io.Writer, command string, data any) {
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:  "workbook.result",
		Version: 1,
		Command: command,
		Data:    data,
	})
}

// writeResultWithWarnings writes a successful result that has something to say
// beside it.
//
// It exists because a warning used to require a mutation: writeMutationResult
// carries core.Warning, and every read wrote through writeResult, which has no
// member for one. A read that answers correctly while the answer needs
// explaining — `workbook list --status` naming a status a rename or a removal
// forwarded, so the tasks came back under a name nobody typed — had nowhere to
// put the explanation but the tasks themselves. The warnings ride the same
// envelope member mutations use, so one consumer reads both.
func writeResultWithWarnings(output io.Writer, command string, data any, warnings []core.Warning) {
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:   "workbook.result",
		Version:  1,
		Command:  command,
		Data:     data,
		Warnings: warnings,
	})
}

// writeWarnings prints what a text-mode command has to say beside its answer,
// on the channel every other warning uses so a pipeline reading stdout is
// unaffected.
func writeWarnings(stderr io.Writer, warnings []core.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", warning.Message)
	}
}

func writeSyncPhaseResult(
	output io.Writer,
	command string,
	data any,
	conflicts []core.Conflict,
	jsonMode bool,
	renderText func(io.Writer),
) {
	writeSyncPhaseResultWithConfig(output, command, data, conflicts, nil, nil, nil, jsonMode, renderText)
}

// writeSyncPhaseResultWithConfig carries warnings for the same reason
// writeResultWithWarnings does: a synchronization can succeed while having
// something to say that its own result has no member for — that this clone now
// shares a task it claimed, above all — and the warnings ride the envelope
// member every other command already uses. Text mode leaves them to renderText,
// because they belong on standard error and this function only holds stdout.
func writeSyncPhaseResultWithConfig(
	output io.Writer,
	command string,
	data any,
	conflicts []core.Conflict,
	configConflicts []core.ConfigConflict,
	relicConflicts []core.RelicConflict,
	warnings []core.Warning,
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
			RelicConflict:  relicConflicts,
			Warnings:       warnings,
		})
		return
	}
	renderText(output)
	writeConflicts(output, conflicts)
	writeConfigConflicts(output, configConflicts)
	writeRelicConflicts(output, relicConflicts)
}

// writeConfigConflicts renders the same list the JSON envelope carries. Every
// line names the status or priority the two intents disagreed about, rather
// than a task, because that is what a person has to decide — mirroring
// ConfigConflictError's own "status %s: " / "priority %s: " choice for the
// same reason: Status and Priority are distinct types, so only one of them
// can ever be the subject a given conflict names.
//
// One member names neither: a root-vocabulary conflict is a disagreement about
// where the whole configuration started, so its line drops the column rather
// than printing an empty one.
func writeConfigConflicts(output io.Writer, conflicts []core.ConfigConflict) {
	for _, conflict := range conflicts {
		switch {
		case conflict.Status != "":
			fmt.Fprintf(output, "Config conflict:\t%s\t%s\t%s\n",
				conflict.Status, conflict.Type, core.ConfigConflictDetail(conflict))
		case conflict.Priority != "":
			fmt.Fprintf(output, "Config conflict:\t%s\t%s\t%s\n",
				conflict.Priority, conflict.Type, core.ConfigConflictDetail(conflict))
		default:
			fmt.Fprintf(output, "Config conflict:\t%s\t%s\n",
				conflict.Type, core.ConfigConflictDetail(conflict))
		}
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
	writeThreadMutationResult(stdout, stderr, command, result, sync, conflicts, jsonMode, nil)
}

// writeThreadMutationResult is writeMutationResult with the thread changes the
// pack carried, if any. They are printed under the task line and nowhere else:
// the JSON envelope's data member is the whole task, which already carries the
// comments and attachments the mutation produced.
func writeThreadMutationResult(
	stdout, stderr io.Writer,
	command string,
	result core.MutationResult,
	sync *syncReport,
	conflicts []core.Conflict,
	jsonMode bool,
	changes []core.FieldChange,
) {
	var configConflicts []core.ConfigConflict
	var relicConflicts []core.RelicConflict
	var relicWarnings []core.Warning
	if sync != nil {
		configConflicts = sync.configConflicts
		relicConflicts, relicWarnings = sync.relicConflicts, sync.relicWarnings
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        command,
			Data:           result.Task,
			Conflict:       conflicts,
			ConfigConflict: configConflicts,
			RelicConflict:  relicConflicts,
			Warnings:       withRelicWarnings(result.Warnings, relicWarnings),
			Sync:           sync,
		})
		return
	}

	writeMutation(stdout, result.Task)
	writeFieldChanges(stdout, changes)
	writeSyncReport(stdout, sync)
	writeConflicts(stdout, conflicts)
	writeConfigConflicts(stdout, configConflicts)
	writeRelicConflicts(stdout, relicConflicts)
	// Filtered as the relic warnings are: the fetch's relic defect, one of
	// these, can quote what a relic on origin holds.
	writeRelicWarnings(stderr, result.Warnings)
	writeRelicWarnings(stderr, relicWarnings)
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
	return writeThreadMutationOutcome(stdout, stderr, command, session, result, err, jsonMode, nil)
}

// writeThreadMutationOutcome is writeMutationOutcome for a mutation that also
// changed the task's thread.
//
// The extra lines name what the pack did to the comments and attachments,
// because the task line above them cannot: a comment is invisible in a task's
// ID, status, priority and title, so an update that only commented would
// otherwise print a line identical to the one it printed before. They are
// rendered by writeFieldChanges, which is what `show --history` renders a
// change with, so the confirmation and the log entry read the same way.
//
// Nothing is added to the JSON envelope: its data member is the whole task,
// which already carries the thread the mutation produced.
func writeThreadMutationOutcome(
	stdout, stderr io.Writer,
	command string,
	session *taskSession,
	result core.MutationResult,
	err error,
	jsonMode bool,
	changes []core.FieldChange,
) error {
	if err != nil {
		if core.CategoryOf(err) != core.CategoryConflict {
			return err
		}
		// The warnings are said here too: a merge the stopping fetch made is not
		// made again by a later one.
		writeSyncPhaseResultWithConfig(stdout, command, nil, session.conflicts, nil, session.relicConflicts,
			session.relicWarnings, jsonMode, func(io.Writer) {})
		if !jsonMode {
			writeRelicWarnings(stderr, session.relicWarnings)
		}
		return err
	}
	writeThreadMutationResult(stdout, stderr, command, result, &session.report, session.conflicts, jsonMode, changes)
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

func writeSyncedResult(
	output io.Writer,
	command string,
	data any,
	sync *syncReport,
	conflicts []core.Conflict,
	warnings []core.Warning,
) {
	// The configuration conflicts the command's fetch met ride the envelope's
	// own member, as they do for every mutation (writeThreadMutationResult).
	var configConflicts []core.ConfigConflict
	var relicConflicts []core.RelicConflict
	var relicWarnings []core.Warning
	if sync != nil {
		configConflicts = sync.configConflicts
		relicConflicts, relicWarnings = sync.relicConflicts, sync.relicWarnings
	}
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:         "workbook.result",
		Version:        1,
		Command:        command,
		Data:           data,
		Conflict:       conflicts,
		ConfigConflict: configConflicts,
		RelicConflict:  relicConflicts,
		Warnings:       withRelicWarnings(warnings, relicWarnings),
		Sync:           sync,
	})
}

func writeError(output io.Writer, err error, jsonMode bool) {
	if jsonMode {
		_ = json.NewEncoder(output).Encode(ErrorEnvelope{
			Format:  "workbook.error",
			Version: 1,
			Error: ErrorBody{
				Category:    core.CategoryOf(err),
				Message:     publicErrorMessage(err),
				TasksRemote: tasksRemoteRefusal(err),
				Reset:       resetRefusalOf(err),
			},
		})
		return
	}

	fmt.Fprintf(output, "workbook: %s\n", publicErrorMessage(err))
	if core.CategoryOf(err) == core.CategoryInvocation {
		fmt.Fprint(output, usage)
	}
}

func resetRefusalOf(err error) *gitstore.ResetRefusal {
	var refusal *gitstore.ResetRefusalError
	if !errors.As(err, &refusal) {
		return nil
	}
	copied := refusal.Refusal
	return &copied
}

func tasksRemoteRefusal(err error) *core.TasksRemoteRefusal {
	refusal, found := core.TasksRemoteRefusalOf(err)
	if !found {
		return nil
	}
	return refusal
}

func publicErrorMessage(err error) string {
	// A refusal about the tasks remote is a complete sentence written for the
	// person reading it, so it is said as written however deep the command
	// that met it wrapped it.
	for current := err; current != nil; current = errors.Unwrap(current) {
		if typed, ok := current.(*core.Error); ok && typed.TasksRemote != nil {
			if typed.Category == core.CategoryOperational {
				return typed.Error()
			}
			return typed.Message
		}
	}
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
	}
	for _, task := range result.Tasks {
		fmt.Fprintf(output, "%s\t%s", task.TaskID, task.Status)
		if task.Detail != "" {
			fmt.Fprintf(output, "\t%s", task.Detail)
		}
		fmt.Fprintln(output)
	}
	// One line per relic the phase changed or could not change, in the form
	// of the task lines. A relic's detail can quote what origin holds, so every
	// column passes through the control-byte filter.
	for _, relic := range result.Relics {
		fmt.Fprintf(output, "%s\t%s", singleLine(relic.RelicID), singleLine(string(relic.Status)))
		if relic.Detail != "" {
			fmt.Fprintf(output, "\t%s", singleLine(relic.Detail))
		}
		fmt.Fprintln(output)
	}
}

// withRelicWarnings is warnings followed by relicWarnings, what relicReport
// took from a fetch, for one envelope member, leaving both lists as they were.
func withRelicWarnings(warnings, relicWarnings []core.Warning) []core.Warning {
	if len(relicWarnings) == 0 {
		return warnings
	}
	return append(append([]core.Warning(nil), warnings...), relicWarnings...)
}

// Every ignored ref carries one of these verdicts in its own line, so the
// footer that offers a deletion command and the footer that warns against one
// each name the verdict they apply to. A reader never has to decide which of
// several listed names a command was meant for, and never has to infer it from
// a validator's message.
const (
	ignoredRefRemovable = gitstore.IgnoredRefRemovable
	ignoredRefPlausible = gitstore.IgnoredRefPlausible
)

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

// writeIgnoredRefs names every skipped ref, whether any project's ID format
// could produce that name, why it was skipped, and — only when some name no
// project can own is listed — the command that removes one. Synchronization
// succeeded despite these refs, so the report is the only thing standing
// between a poisoned namespace and nobody noticing.
//
// It takes the refs rather than a phase because the same report is written for
// one phase, for a whole run, and for what a watcher last observed.
//
// It offers no advice to adopt such a name under a key of this project's. A
// task ref under a key this project does not have is a ref from another project
// identity — a split, a re-initialization, another project sharing the origin —
// and its documents name that project, which the tip check refuses whatever
// this project's key set says. Naming `workbook key add` would have pointed at
// a command that cannot make the ref readable.
//
// It is the only place Workbook suggests deleting anything from a shared
// remote, and shared task history is append-only. A name this build does not
// recognize can still be a task written by a newer version or under a second
// project's key, so every line says which of the two it is, a warning stands in
// for the command on the ones that may be history, and even the command that is
// offered is phrased as a decision the reader makes about a specific ref rather
// than a step to take.
func writeIgnoredRefs(output io.Writer, remote string, refs []gitstore.IgnoredRef) {
	if len(refs) == 0 {
		return
	}
	removable := 0
	for _, ignored := range refs {
		verdict := ignored.Verdict()
		if !ignored.PlausibleTask {
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
	writeShowThread(output, detail.Task, detail.Comments, detail.RemovedComments)
	writeShowRelics(output, detail.Relics)
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
	if compacted := log.Compacted; compacted != nil {
		now := time.Now()
		fmt.Fprintf(output, "%d earlier changes from %s to %s were compacted on %s.\n",
			compacted.Changes, compactedDay(compacted.From, now), compactedDay(compacted.To, now),
			compactedDay(compacted.On, now))
	}
	writeHistoryTruncation(output, log.Truncated)
}

// compactedDay renders one date of the compacted-history line in local time,
// with its year only when that is not the current one.
func compactedDay(day, now time.Time) string {
	day = day.Local()
	if day.Year() != now.Year() {
		return day.Format("Jan 2, 2006")
	}
	return day.Format("Jan 2")
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
		// A due date set where there was none, or cleared, has an empty side
		// that is a real value rather than one the log did not recover, so it
		// is named instead of falling into the edited form below.
		if change.Kind == core.ChangeSet && change.Field == "due" {
			fmt.Fprintf(output, "\t%s:\t%s → %s\n", label, dueOrNone(change.From), dueOrNone(change.To))
			continue
		}
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
				if change.Merge != nil {
					fmt.Fprintf(output, "\t\tmerged two edits that changed different lines (%s)\n",
						singleLine(change.Merge.Method))
				}
				continue
			}
			if change.From == "" {
				// A replacement whose previous value is not known has nothing
				// to put on the left of the arrow, and a line beginning "→"
				// reads as a renderer that lost something rather than as the
				// change it describes. The change log's account of a comment
				// edit is the case: it summarizes one commit and deliberately
				// does not replay the thread to recover what the comment said
				// before, so what it can report is the new body and the fact
				// that it replaced one.
				fmt.Fprintf(output, "\t%s:\t%s\t(edited)\n", label, singleLine(change.To))
				continue
			}
			fmt.Fprintf(output, "\t%s:\t%s → %s\n", label, singleLine(change.From), singleLine(change.To))
		}
	}
}

// dueOrNone is one side of a due-date change: the date, or "none".
func dueOrNone(due string) string {
	if due == "" {
		return "none"
	}
	return due
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

// writeShow prints a task the way `show` does, with its live thread and no
// trace of removals: the callers that print a bare task have read no history
// to derive one from.
func writeShow(output io.Writer, task core.Task) {
	writeShowThread(output, task, core.CommentTrace{}.Thread(task.Comments, ""), nil)
}

// writeShowThread prints a task with the thread as one reader is shown it:
// each live comment with whoever edited it when that was not its author, and
// a line for each removed one saying who removed it.
func writeShowThread(output io.Writer, task core.Task, comments []core.ThreadComment, removed []core.RemovedComment) {
	fmt.Fprintf(output, "ID:\t%s\n", task.ID)
	fmt.Fprintf(output, "Project ID:\t%s\n", task.ProjectID)
	fmt.Fprintf(output, "Title:\t%s\n", singleLine(task.Title))
	writeDescription(output, task.Description)
	fmt.Fprintf(output, "Status:\t%s\n", task.Status)
	fmt.Fprintf(output, "Priority:\t%s\n", task.Priority)
	fmt.Fprintf(output, "Due:\t%s\n", terminalui.DueCell(task))
	fmt.Fprintf(output, "Labels:\t%s\n", singleLine(strings.Join(task.Labels, ",")))
	writeAssignments(output, task.Assignments, time.Now())
	fmt.Fprintf(output, "Rank:\t%s\n", task.Rank)
	fmt.Fprintf(output, "Dependencies:\t%s\n", strings.Join(task.Dependencies, ","))
	fmt.Fprintf(output, "Created At:\t%s\n", task.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Updated At:\t%s\n", task.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(output, "Deleted:\t%t\n", task.Deleted)
	fmt.Fprintf(output, "History Generation:\t%s\n", task.HistoryGeneration)
	fmt.Fprintf(output, "Head:\t%s\n", task.Head)
	writeComments(output, comments, removed)
	writeAttachments(output, task.Attachments)
}

// writeComments prints the thread after the field block, and prints nothing at
// all for a task nobody has commented on — so what `show` prints for every task
// that existed before comments did is byte-for-byte what it printed before.
//
// The section is fenced the way writeDescription describes: its header sits at
// column zero, which no comment can reach, each comment's attribution line
// carries one tab, and the body carries two. A body line claiming to be another
// comment's attribution therefore lands a tab too deep, and singleLine has
// already dropped whatever indentation it was written with, so it cannot climb
// back out.
//
// Identifiers are printed whole rather than shortened. They are what the
// --edit-comment and --remove-comment flags take, and those flags accept a
// prefix, so a reader who wants a short form can take one — while a display
// that shortened them would sometimes print two comments under one string. A
// ULID opens with ten characters of millisecond timestamp and is random after
// that, so anything shorter than eleven characters is shared outright by two
// items minted in the same millisecond, which one pack's are.
//
// A removed comment keeps its place in the thread — identifier order is
// creation order, live or not — as an attribution line with no body, ending
// in who removed it and when. Its text is not printed: removing a comment
// hides what it said from the thread, and `show --history` is where the
// history still holds it.
func writeComments(output io.Writer, comments []core.ThreadComment, removed []core.RemovedComment) {
	if len(comments) == 0 && len(removed) == 0 {
		return
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Comments:")
	next := 0
	for _, comment := range comments {
		for ; next < len(removed) && removed[next].ID < comment.ID; next++ {
			writeRemovedComment(output, removed[next])
		}
		fmt.Fprintf(output, "\t%s\t%s\t%s%s\n",
			comment.ID,
			singleLine(comment.Author),
			comment.CreatedAt.Format(time.RFC3339),
			editedMarker(comment),
		)
		for _, line := range descriptionLines(comment.Body) {
			if line == "" {
				fmt.Fprintln(output)
				continue
			}
			fmt.Fprintf(output, "\t\t%s\n", line)
		}
	}
	for ; next < len(removed); next++ {
		writeRemovedComment(output, removed[next])
	}
}

func writeRemovedComment(output io.Writer, removal core.RemovedComment) {
	fmt.Fprintf(output, "\t%s\t%s\t%s\t(removed %s by %s)\n",
		removal.ID,
		singleLine(removal.Author),
		removal.CreatedAt.Format(time.RFC3339),
		removal.RemovedAt.Format(time.RFC3339),
		singleLine(removal.RemovedBy),
	)
}

// editedMarker states that a comment no longer reads as it was written, and
// when it changed. The presence of the time is the flag, exactly as it is in
// the stored document, so there is no second spelling of the same fact.
//
// It names the editor only when that was somebody other than the author. Only
// the author may edit a comment now, but a history can hold an edit made
// before that rule or by a build without it, and a comment shown under its
// author's name over somebody else's words has to say so. When the history no
// longer records who made the edit — it came before the task was compacted —
// the marker says that instead, for the same reason.
func editedMarker(comment core.ThreadComment) string {
	if !comment.Edited() {
		return ""
	}
	if comment.EditorNotRecorded {
		return "\t(edited " + comment.EditedAt.Format(time.RFC3339) + ", editor not recorded)"
	}
	if comment.EditedByOther() {
		return "\t(edited " + comment.EditedAt.Format(time.RFC3339) + " by " + singleLine(comment.EditedBy) + ")"
	}
	return "\t(edited " + comment.EditedAt.Format(time.RFC3339) + ")"
}

// writeAttachments lists what is attached, of both kinds, in one list — because
// a reader asking what is attached to a task is asking one question.
//
// Each row names the identifier, the display text, the kind, and then the one
// value that differs by kind: a file's size in bytes, which is what a download
// will cost, and a link's URL, which is the link. A link with no label leaves
// the display column empty rather than repeating its URL into it.
func writeAttachments(output io.Writer, attachments []core.Attachment) {
	if len(attachments) == 0 {
		return
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Attachments:")
	for _, attachment := range attachments {
		detail := singleLine(attachment.URL)
		name := singleLine(attachment.Label)
		if attachment.Kind == core.AttachmentFile {
			detail = fmt.Sprintf("%d bytes", attachment.Size)
			name = singleLine(attachment.Name)
			// A dropped file keeps its row, so a reader learns it existed, and
			// says why there is nothing to fetch.
			if attachment.IsDropped() {
				name += " (removed by compaction on " + attachment.Dropped.At.Format("2006-01-02") + ")"
			}
		}
		fmt.Fprintf(output, "\t%s\t%s\t%s\t%s\n", attachment.ID, name, attachment.Kind, detail)
	}
}

// writeAssignments prints who is responsible for a task, one line each.
//
// Four columns, because each answers a different question a reader has: the
// value as somebody would type it into --unassign, the exact time it was
// recorded, and how long ago that was. The last is the whole of what this
// design means by staleness — nothing expires, and an assignment three weeks
// old is a fact for people to settle between themselves rather than something a
// clock takes away.
//
// The field is printed even when nobody holds the task, the way Labels is, so a
// caller reading text output can rely on the field existing. Continuation lines
// are indented with a tab for the reason writeDescription's are: every field
// this block prints begins at column zero, so an indented line cannot be read
// as one — and an assignment value cannot contain a tab or a newline at all,
// because the fold refuses one that does.
func writeAssignments(output io.Writer, assignments []core.Assignment, now time.Time) {
	if len(assignments) == 0 {
		fmt.Fprint(output, "Assignments:\t\n")
		return
	}
	for index, assignment := range assignments {
		field := "Assignments:"
		if index > 0 {
			field = ""
		}
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\n",
			field,
			singleLine(assignment.Value()),
			assignment.CreatedAt.Format(time.RFC3339),
			presentation.AssignedAgo(assignment, now),
		)
	}
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
