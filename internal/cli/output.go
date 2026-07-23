package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

const usage = `Usage: workbook <command> [arguments]

Commands:
  init [--key WB]
  create <title> [options]
  list [options]
  show <id-or-prefix> [--json]
  update <id-or-prefix> [options]
  delete <id-or-prefix> [--json]
`

type ResultEnvelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Command string `json:"command"`
	Data    any    `json:"data"`
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
		return typed.Message
	}
	return err.Error()
}

func writeMutation(output io.Writer, task core.Task) {
	fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Priority, task.Title)
}

func writeList(output io.Writer, tasks []core.Task) {
	fmt.Fprintln(output, "ID\tTITLE\tSTATUS\tPRIORITY\tLABELS")
	for _, task := range tasks {
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n",
			task.ID,
			task.Title,
			task.Status,
			task.Priority,
			strings.Join(task.Labels, ","),
		)
	}
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
