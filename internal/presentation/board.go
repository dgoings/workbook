package presentation

import (
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

type TaskView struct {
	Task     core.Task
	IDPrefix string
}

type Column struct {
	Status core.Status
	Label  string
	Tasks  []TaskView
}

type Board struct {
	Columns []Column
}

var columnDefinitions = [...]struct {
	status core.Status
	label  string
}{
	{core.StatusBacklog, "Backlog"},
	{core.StatusReady, "Ready"},
	{core.StatusInProgress, "In progress"},
	{core.StatusBlocked, "Blocked"},
	{core.StatusDone, "Done"},
}

func TaskViews(tasks []core.Task) []TaskView {
	views := make([]TaskView, len(tasks))
	for i, task := range tasks {
		views[i] = TaskView{
			Task:     task,
			IDPrefix: shortestUniquePrefix(i, tasks),
		}
	}
	return views
}

func NewBoard(tasks []core.Task) Board {
	board := Board{Columns: make([]Column, len(columnDefinitions))}
	for i, definition := range columnDefinitions {
		board.Columns[i] = Column{Status: definition.status, Label: definition.label}
	}

	for _, task := range TaskViews(tasks) {
		for i := range board.Columns {
			if board.Columns[i].Status == task.Task.Status {
				board.Columns[i].Tasks = append(board.Columns[i].Tasks, task)
				break
			}
		}
	}
	return board
}

func shortestUniquePrefix(index int, tasks []core.Task) string {
	id := tasks[index].ID
	separatorEnd := strings.IndexByte(id, '-') + 1
	minimumEnd := min(separatorEnd+8, len(id))

	for end := minimumEnd; end <= len(id); end++ {
		prefix := id[:end]
		if isUniquePrefix(index, prefix, tasks) {
			return prefix
		}
	}
	return id
}

func isUniquePrefix(index int, prefix string, tasks []core.Task) bool {
	for otherIndex, task := range tasks {
		if otherIndex != index && strings.HasPrefix(task.ID, prefix) {
			return false
		}
	}
	return true
}
