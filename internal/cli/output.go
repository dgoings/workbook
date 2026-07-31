package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
  show <id-or-prefix> [--json]
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
  sync [--json]
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
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Command  string         `json:"command"`
	Data     any            `json:"data"`
	Warnings []core.Warning `json:"warnings,omitempty"`
	Sync     *syncReport    `json:"sync,omitempty"`
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

func writeMutationResult(
	stdout, stderr io.Writer,
	command string,
	result core.MutationResult,
	sync *syncReport,
	jsonMode bool,
) {
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:   "workbook.result",
			Version:  1,
			Command:  command,
			Data:     result.Task,
			Warnings: result.Warnings,
			Sync:     sync,
		})
		return
	}

	writeMutation(stdout, result.Task)
	writeSyncReport(stdout, sync)
	for _, warning := range result.Warnings {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", warning.Message)
	}
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

func writeSyncedResult(output io.Writer, command string, data any, sync *syncReport) {
	_ = json.NewEncoder(output).Encode(ResultEnvelope{
		Format:  "workbook.result",
		Version: 1,
		Command: command,
		Data:    data,
		Sync:    sync,
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
