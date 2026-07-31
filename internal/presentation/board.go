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
