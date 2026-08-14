package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// newerWriterNamedLimit bounds how many task IDs one advisory spells out.
//
// The advisory exists to be acted on, and the action is the same however many
// tasks are involved: upgrade. Naming three and counting the rest keeps the
// line readable on a project where somebody upgraded and then wrote to
// everything, without hiding which tasks to look at on the ordinary project
// where it is one or two.
const newerWriterNamedLimit = 3

// newerWriterWarnings reports tasks in this answer whose history was written by
// a newer Workbook.
//
// It is a warning rather than a refusal because the read succeeded and the
// values shown are the ones their author wrote — every read serves a task from
// its stored checkpoint, so a checkpoint a newer build wrote is still that
// build's account of the task. What the reader cannot see in the answer is the
// consequence, so the message says it: this build will refuse to change these
// tasks until it is upgraded.
func newerWriterWarnings(tasks []core.Task) []core.Warning {
	var affected []string
	for _, task := range tasks {
		if task.NewerWriter {
			affected = append(affected, task.ID)
		}
	}
	if len(affected) == 0 {
		return nil
	}
	sort.Strings(affected)
	subject, pronoun := "they", "them"
	if len(affected) == 1 {
		subject, pronoun = "it", "it"
	}
	return []core.Warning{{
		Code: core.WarningNewerWriter,
		Message: fmt.Sprintf(
			"%s written by a newer workbook; %s %s shown from the stored checkpoint, and changing %s "+
				"is refused until workbook is upgraded",
			describeNewerWriterTasks(affected),
			subject, pluralIs(len(affected)), pronoun,
		),
	}}
}

// newerWriterTaskWarnings is the single-task form, for the surfaces that answer
// about one task rather than a list.
func newerWriterTaskWarnings(task core.Task) []core.Warning {
	return newerWriterWarnings([]core.Task{task})
}

// describeNewerWriterTasks names the tasks and agrees with itself, because this
// message is read by people rather than parsed.
func describeNewerWriterTasks(ids []string) string {
	switch {
	case len(ids) == 1:
		return "task " + ids[0] + " was"
	case len(ids) <= newerWriterNamedLimit:
		return "tasks " + strings.Join(ids, ", ") + " were"
	default:
		return fmt.Sprintf("%d tasks, including %s, were",
			len(ids), strings.Join(ids[:newerWriterNamedLimit], ", "))
	}
}

func pluralIs(count int) string {
	if count == 1 {
		return "is"
	}
	return "are"
}
