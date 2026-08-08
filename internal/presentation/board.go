package presentation

import (
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

type TaskView struct {
	Task                  core.Task
	IDPrefix              string
	DependenciesComplete  int
	DependenciesTotal     int
	WaitingOnDependencies bool
}

type Column struct {
	Status core.Status
	Label  string
	Tasks  []TaskView
}

// Board is the split every renderer works from. Columns holds the tasks this
// build has a status column for; UnknownTasks holds the rest, and the two
// together are always the whole set the board was handed.
//
// A renderer must consume both. Rendering Columns alone silently deletes tasks
// from the reader's view, and a task that is invisible reads as a task that was
// deleted rather than one that is merely unsorted. Both renderers therefore
// give UnknownTasks its own labeled region — the terminal an UNKNOWN STATUS
// section, the web board an "Unknown status" area below the columns — rather
// than folding it into a column that would misreport the status. Keeping it out
// of the columns matters as much as showing it: the region is a display, not a
// seventh status, so it takes no drops and its cards do not drag.
//
// This is routine rather than exotic, because two clones on different Workbook
// versions produce it on their own. internal/presentation/parity_test.go holds
// the decision and asserts it against both renderers at once.
type Board struct {
	Columns      []Column
	UnknownTasks []TaskView
}

func TaskViews(tasks []core.Task) []TaskView {
	active := make(map[string]core.Task, len(tasks))
	for _, task := range tasks {
		if !task.Deleted {
			active[task.ID] = task
		}
	}
	views := make([]TaskView, len(tasks))
	for i, task := range tasks {
		complete := 0
		for _, dependencyID := range task.Dependencies {
			if dependency, ok := active[dependencyID]; ok &&
				dependency.Status == core.StatusDone {
				complete++
			}
		}
		total := len(task.Dependencies)
		views[i] = TaskView{
			Task:                  task,
			IDPrefix:              shortestUniquePrefix(i, tasks),
			DependenciesComplete:  complete,
			DependenciesTotal:     total,
			WaitingOnDependencies: task.Status == core.StatusReady && complete < total,
		}
	}
	return views
}

func NewBoard(tasks []core.Task) Board {
	definitions := core.WorkflowStatuses()
	board := Board{Columns: make([]Column, 0, len(definitions))}
	for _, definition := range definitions {
		board.Columns = append(board.Columns, Column{Status: definition.Status, Label: definition.Label})
	}

	for _, task := range TaskViews(tasks) {
		matched := false
		for i := range board.Columns {
			if board.Columns[i].Status == task.Task.Status {
				board.Columns[i].Tasks = append(board.Columns[i].Tasks, task)
				matched = true
				break
			}
		}
		if !matched {
			board.UnknownTasks = append(board.UnknownTasks, task)
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
