package terminalui

import (
	"io"
	"strings"
	"unicode/utf8"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

type Layout uint8

const (
	LayoutWide Layout = iota + 1
	LayoutNarrow
)

func RenderList(w io.Writer, tasks []core.Task, width int) error {
	idWidth := len("ID")
	statusWidth := len("STATUS")
	priorityWidth := len("PRIORITY")
	for _, task := range tasks {
		idWidth = max(idWidth, len(task.ID))
		statusWidth = max(statusWidth, len(task.Status))
		priorityWidth = max(priorityWidth, len(task.Priority))
	}

	humanWidth := max(3, (width-idWidth-statusWidth-priorityWidth-8)/2)
	var output strings.Builder
	writeListRow(&output, idWidth, humanWidth, statusWidth, priorityWidth, "ID", "TITLE", "STATUS", "PRIORITY", "LABELS")
	for _, task := range tasks {
		writeListRow(&output, idWidth, humanWidth, statusWidth, priorityWidth,
			task.ID,
			fit(core.DisplayLine(task.Title), humanWidth),
			string(task.Status),
			string(task.Priority),
			fit(core.DisplayLine(strings.Join(task.Labels, ",")), humanWidth),
		)
	}
	_, err := io.WriteString(w, output.String())
	return err
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

func RenderBoard(w io.Writer, board presentation.Board, layout Layout, width int) error {
	switch layout {
	case LayoutWide:
		return renderWideBoard(w, board, width)
	case LayoutNarrow:
		return renderNarrowBoard(w, board, width)
	default:
		return renderNarrowBoard(w, board, width)
	}
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

func priorityMarker(priority core.Priority) string {
	switch priority {
	case core.PriorityHigh:
		return "H"
	case core.PriorityMedium:
		return "M"
	case core.PriorityLow:
		return "L"
	default:
		return "?"
	}
}

func renderWideBoard(w io.Writer, board presentation.Board, width int) error {
	columns := canonicalColumns(board)
	columnCount := len(columns)
	// A board with no columns has no grid to draw, and a grid renderer that
	// divided the width among zero cells crashed rather than saying so.
	//
	// This is reachable from data, which is why it is guarded here rather than
	// prevented upstream. A configuration checkpoint is decoded and trusted —
	// only a genesis is re-folded — and nothing in the state document requires
	// a live status, so a ledger tip written by a hand edit, a corruption, or a
	// build that allows an empty vocabulary reaches this renderer with zero
	// columns and its tasks stranded in UnknownTasks. The read path is total
	// over ledger data by doctrine (see core.Vocabulary.Resolve), and a
	// renderer is part of the read path.
	//
	// The narrow layout is the answer rather than an empty frame: in this state
	// every task the board holds is in the unknown-status section, which is
	// exactly what the narrow renderer prints, and a bordered box with nothing
	// in it would only hide it.
	if columnCount == 0 {
		return renderNarrowBoard(w, board, width)
	}
	cellWidth := max(3, (width-(columnCount+1))/columnCount)
	contentWidth := max(1, cellWidth-2)

	border := "+" + strings.Repeat(strings.Repeat("-", cellWidth)+"+", columnCount)
	var output strings.Builder
	output.WriteString(border)
	output.WriteByte('\n')
	writeWideRow(&output, columns, contentWidth, true, func(column presentation.Column, _ int) string {
		return column.Label + " (" + itoa(len(column.Tasks)) + ")"
	})
	output.WriteString(border)
	output.WriteByte('\n')

	columnLines := make([][]string, len(columns))
	rows := 1
	for index, column := range columns {
		for _, task := range column.Tasks {
			columnLines[index] = append(columnLines[index], wideCardLines(task, contentWidth)...)
		}
		rows = max(rows, len(columnLines[index]))
	}
	for row := 0; row < rows; row++ {
		writeWideRow(&output, columns, contentWidth, false, func(_ presentation.Column, index int) string {
			if row >= len(columnLines[index]) {
				return ""
			}
			return columnLines[index][row]
		})
	}
	output.WriteString(border)
	output.WriteByte('\n')
	if len(board.UnknownTasks) > 0 {
		output.WriteByte('\n')
		writeUnknownSection(&output, board.UnknownTasks, width)
		output.WriteByte('\n')
	}
	_, err := io.WriteString(w, output.String())
	return err
}

func renderNarrowBoard(w io.Writer, board presentation.Board, width int) error {
	columns := canonicalColumns(board)
	sections := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		sections = append(sections, narrowSection(strings.ToUpper(column.Label), column.Tasks, width))
	}
	if len(board.UnknownTasks) > 0 {
		var output strings.Builder
		writeUnknownSection(&output, board.UnknownTasks, width)
		sections = append(sections, output.String())
	}
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func writeListRow(output *strings.Builder, idWidth, titleWidth, statusWidth, priorityWidth int, id, title, status, priority, labels string) {
	output.WriteString(pad(id, idWidth))
	output.WriteString("  ")
	output.WriteString(pad(title, titleWidth))
	output.WriteString("  ")
	output.WriteString(pad(status, statusWidth))
	output.WriteString("  ")
	output.WriteString(pad(priority, priorityWidth))
	output.WriteString("  ")
	output.WriteString(labels)
	output.WriteByte('\n')
}

func writeWideRow(output *strings.Builder, columns []presentation.Column, contentWidth int, truncate bool, value func(presentation.Column, int) string) {
	for index, column := range columns {
		cell := value(column, index)
		if truncate {
			cell = fit(cell, contentWidth)
		}
		output.WriteString("| ")
		output.WriteString(pad(cell, contentWidth))
		output.WriteByte(' ')
	}
	output.WriteString("|\n")
}

func wideCardLines(task presentation.TaskView, width int) []string {
	lines := wrapWideMetadata(task.IDPrefix, " ["+priorityMarker(task.Task.Priority)+"]", width)
	lines = append(
		lines,
		fit(core.DisplayLine(task.Task.Title), width),
		fit(core.DisplayLine(strings.Join(task.Task.Labels, ",")), width),
	)
	return lines
}

func wrapWideMetadata(prefix, marker string, width int) []string {
	width = max(1, width)
	lines := splitASCII(prefix, width)
	if len(lines) == 0 {
		return splitASCII(marker, width)
	}
	last := len(lines) - 1
	if len(lines[last])+len(marker) <= width {
		lines[last] += marker
		return lines
	}
	return append(lines, splitASCII(marker, width)...)
}

func splitASCII(value string, width int) []string {
	if value == "" {
		return nil
	}
	lines := make([]string, 0, (len(value)+width-1)/width)
	for len(value) > width {
		lines = append(lines, value[:width])
		value = value[width:]
	}
	return append(lines, value)
}

func narrowSection(label string, tasks []presentation.TaskView, width int) string {
	heading := label + " (" + itoa(len(tasks)) + ")"
	var output strings.Builder
	output.WriteString(heading)
	output.WriteByte('\n')
	output.WriteString(strings.Repeat("-", len(heading)))
	output.WriteByte('\n')
	if len(tasks) == 0 {
		output.WriteString("(empty)")
		return output.String()
	}
	for index, task := range tasks {
		if index > 0 {
			output.WriteByte('\n')
		}
		writeNarrowTask(&output, task, width)
	}
	return output.String()
}

// unknownStatusNote says what puts a task in this section, because the answer
// changed and the old one is the one a reader would assume.
//
// It used to hold any status outside the six this build shipped, so "a newer
// Workbook wrote it" was the usual explanation. A project defines its own
// statuses now, and a stored status that a rename or a removal forwards to a
// live one is drawn in that live column. What is left here is a status that
// forwards nowhere, which no upgrade will fix.
//
// The claim is absolute because this renderer can afford it: a board is built
// and printed inside one command, from the statuses that same command read, so
// a task in this section really did fail to resolve against the vocabulary the
// columns came from. The web board cannot say that much — its columns are as
// old as the page and its tasks are as new as the last poll — so its copy names
// the other case as well.
const unknownStatusNote = "(no status this project defines, and no rename or removal leads to one)"

func writeUnknownSection(output *strings.Builder, tasks []presentation.TaskView, width int) {
	heading := "UNKNOWN STATUS (" + itoa(len(tasks)) + ")"
	output.WriteString(heading)
	output.WriteByte('\n')
	output.WriteString(strings.Repeat("-", len(heading)))
	output.WriteByte('\n')
	output.WriteString(fit(unknownStatusNote, max(1, width)))
	output.WriteByte('\n')
	for index, task := range tasks {
		if index > 0 {
			output.WriteByte('\n')
		}
		writeNarrowTask(output, task, width)
	}
}

func writeNarrowTask(output *strings.Builder, task presentation.TaskView, width int) {
	prefix := task.IDPrefix + " [" + string(task.Task.Priority) + "] "
	output.WriteString(prefix)
	output.WriteString(fit(core.DisplayLine(task.Task.Title), max(1, width-len(prefix))))
	if len(task.Task.Labels) > 0 {
		output.WriteString("\n  labels: ")
		output.WriteString(fit(core.DisplayLine(strings.Join(task.Task.Labels, ", ")), max(1, width-len("  labels: "))))
	}
}

func canonicalColumns(board presentation.Board) []presentation.Column {
	return board.Columns
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
