package terminalui

import (
	"io"
	"strings"
	"unicode/utf8"

	"github.com/dgoings/workbook/internal/core"
)

func RenderList(w io.Writer, tasks []core.Task, width int) error {
	idWidth := len("ID")
	statusWidth := len("STATUS")
	priorityWidth := len("PRIORITY")
	// The DUE column is drawn only when some listed task has a due date, so a
	// project that never uses them reads exactly the table it always has.
	dueWidth := 0
	dues := make([]string, len(tasks))
	for index, task := range tasks {
		idWidth = max(idWidth, len(task.ID))
		statusWidth = max(statusWidth, len(task.Status))
		priorityWidth = max(priorityWidth, len(task.Priority))
		dues[index] = DueCell(task)
		if dues[index] != "" {
			dueWidth = max(dueWidth, len("DUE"), len(dues[index]))
		}
	}

	dueColumns := 0
	if dueWidth > 0 {
		dueColumns = dueWidth + 2
	}
	humanWidth := max(3, (width-idWidth-statusWidth-priorityWidth-dueColumns-8)/2)
	var output strings.Builder
	writeListRow(&output, idWidth, humanWidth, statusWidth, priorityWidth, dueWidth, "ID", "TITLE", "STATUS", "PRIORITY", "DUE", "LABELS")
	for index, task := range tasks {
		writeListRow(&output, idWidth, humanWidth, statusWidth, priorityWidth, dueWidth,
			task.ID,
			fit(core.DisplayLine(task.Title), humanWidth),
			string(task.Status),
			string(task.Priority),
			dues[index],
			fit(core.DisplayLine(strings.Join(task.Labels, ",")), humanWidth),
		)
	}
	_, err := io.WriteString(w, output.String())
	return err
}

// DueCell is a task's due date as one line of text: the date, followed by the
// word for what it means to this reader today when that is anything. The word
// is the whole marking — a terminal table has no color to lean on, and the
// mark must read the same whether or not one is drawn.
func DueCell(task core.Task) string {
	if task.Due == "" {
		return ""
	}
	if word := DueWord(task.DueState()); word != "" {
		return task.Due + " " + word
	}
	return task.Due
}

// DueWord is the word a due state is marked with wherever Workbook prints one.
func DueWord(state core.DueState) string {
	switch state {
	case core.DueStateOverdue:
		return "overdue"
	case core.DueStateSoon:
		return "due soon"
	default:
		return ""
	}
}

// StatusRow is one line of the status table: a project's status as `workbook
// status list` presents it.
//
// It is a rendering shape rather than a core type on purpose. The column order
// is a presentation decision, Tasks is a count this package never computes, and
// Position is the 1-based place a person reads rather than the rational rank
// that produces it.
type StatusRow struct {
	Position int
	Status   string
	Label    string
	Tags     string
	Tasks    string
}

// RenderStatusList writes the status table in RenderList's style: padded
// columns separated by two spaces, headings in capitals, and nothing truncated.
//
// Nothing is truncated because every column here is bounded by a ceiling that
// already keeps it typeable — a status name at 40 bytes and a label at 60 — so
// the width a task title has to be fitted into is not a constraint this table
// has. The parameter is kept for symmetry with the other renderers and because
// a future column may need it.
//
// Padding counts bytes, not display cells, which is RenderList's behavior and
// is wrong the same way: a label with non-ASCII characters pads short and its
// row's later columns sit a few cells left of the heading. It is left
// consistent rather than fixed here, because fixing it for one table would make
// two tables in the same output disagree about what a column is; the whole
// package wants one width measure, and that is a change of its own.
func RenderStatusList(w io.Writer, rows []StatusRow, _ int) error {
	positionWidth := len("#")
	statusWidth := len("STATUS")
	labelWidth := len("LABEL")
	tagsWidth := len("TAGS")
	positions := make([]string, len(rows))
	for index, row := range rows {
		positions[index] = itoa(row.Position)
		positionWidth = max(positionWidth, len(positions[index]))
		statusWidth = max(statusWidth, len(row.Status))
		labelWidth = max(labelWidth, len(row.Label))
		tagsWidth = max(tagsWidth, len(row.Tags))
	}

	var output strings.Builder
	writeStatusRow(&output, positionWidth, statusWidth, labelWidth, tagsWidth, "#", "STATUS", "LABEL", "TAGS", "TASKS")
	for index, row := range rows {
		writeStatusRow(&output, positionWidth, statusWidth, labelWidth, tagsWidth,
			positions[index], row.Status, row.Label, row.Tags, row.Tasks)
	}
	_, err := io.WriteString(w, output.String())
	return err
}

func writeStatusRow(output *strings.Builder, positionWidth, statusWidth, labelWidth, tagsWidth int, position, status, label, tags, tasks string) {
	output.WriteString(pad(position, positionWidth))
	output.WriteString("  ")
	output.WriteString(pad(status, statusWidth))
	output.WriteString("  ")
	output.WriteString(pad(label, labelWidth))
	output.WriteString("  ")
	output.WriteString(pad(tags, tagsWidth))
	output.WriteString("  ")
	output.WriteString(tasks)
	output.WriteByte('\n')
}

// PriorityRow is one line of the priority table, StatusRow's counterpart for
// `workbook priority list`.
//
// It is a separate shape rather than StatusRow reused with a different heading,
// because a priority carries a color and a status does not: sharing the row
// would mean either a column statuses always leave blank or a heading that
// lies about one of the two tables.
type PriorityRow struct {
	Position int
	Priority string
	Label    string
	Tags     string
	// Color is the priority's stored ink, blank when nothing is stored and the
	// board derives one from the position. Blank is the honest rendering of
	// that: printing a derived value here would make an automatic color look
	// like a recorded decision.
	Color string
	Tasks string
}

// RenderPriorityList writes the priority table in RenderStatusList's style, and
// inherits every trade that function's comment describes, including the
// byte-counted padding.
func RenderPriorityList(w io.Writer, rows []PriorityRow, _ int) error {
	positionWidth := len("#")
	priorityWidth := len("PRIORITY")
	labelWidth := len("LABEL")
	tagsWidth := len("TAGS")
	colorWidth := len("COLOR")
	positions := make([]string, len(rows))
	for index, row := range rows {
		positions[index] = itoa(row.Position)
		positionWidth = max(positionWidth, len(positions[index]))
		priorityWidth = max(priorityWidth, len(row.Priority))
		labelWidth = max(labelWidth, len(row.Label))
		tagsWidth = max(tagsWidth, len(row.Tags))
		colorWidth = max(colorWidth, len(row.Color))
	}

	var output strings.Builder
	writePriorityRow(&output, positionWidth, priorityWidth, labelWidth, tagsWidth, colorWidth,
		"#", "PRIORITY", "LABEL", "TAGS", "COLOR", "TASKS")
	for index, row := range rows {
		writePriorityRow(&output, positionWidth, priorityWidth, labelWidth, tagsWidth, colorWidth,
			positions[index], row.Priority, row.Label, row.Tags, row.Color, row.Tasks)
	}
	_, err := io.WriteString(w, output.String())
	return err
}

func writePriorityRow(
	output *strings.Builder,
	positionWidth, priorityWidth, labelWidth, tagsWidth, colorWidth int,
	position, priority, label, tags, color, tasks string,
) {
	output.WriteString(pad(position, positionWidth))
	output.WriteString("  ")
	output.WriteString(pad(priority, priorityWidth))
	output.WriteString("  ")
	output.WriteString(pad(label, labelWidth))
	output.WriteString("  ")
	output.WriteString(pad(tags, tagsWidth))
	output.WriteString("  ")
	output.WriteString(pad(color, colorWidth))
	output.WriteString("  ")
	output.WriteString(tasks)
	output.WriteByte('\n')
}

func fit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		end := 0
		for end < len(value) {
			_, size := utf8.DecodeRuneInString(value[end:])
			if end+size > width {
				break
			}
			end += size
		}
		return value[:end]
	}

	limit := width - 3
	end := 0
	for end < len(value) && end < limit {
		_, size := utf8.DecodeRuneInString(value[end:])
		if end+size > limit {
			break
		}
		end += size
	}
	return value[:end] + "..."
}

func pad(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-len(value)))
}

func writeListRow(output *strings.Builder, idWidth, titleWidth, statusWidth, priorityWidth, dueWidth int, id, title, status, priority, due, labels string) {
	output.WriteString(pad(id, idWidth))
	output.WriteString("  ")
	output.WriteString(pad(title, titleWidth))
	output.WriteString("  ")
	output.WriteString(pad(status, statusWidth))
	output.WriteString("  ")
	output.WriteString(pad(priority, priorityWidth))
	output.WriteString("  ")
	if dueWidth > 0 {
		output.WriteString(pad(due, dueWidth))
		output.WriteString("  ")
	}
	output.WriteString(labels)
	output.WriteByte('\n')
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
