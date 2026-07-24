package presentation

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestTaskViewsUseShortestActionableUniquePrefixes(t *testing.T) {
	tests := []struct {
		name  string
		tasks []core.Task
		want  []string
	}{
		{
			name: "diverges at and after the minimum prefix",
			tasks: []core.Task{
				{
					ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
					TaskData: core.TaskData{
						Title:  "Preserve every task field",
						Status: core.StatusReady,
						Labels: []string{"board"},
					},
				},
				{ID: "WB-01ARZ3NDF0TSV4RRFFQ69G5FAW"},
				{ID: "WB-01BXZ3NDEKTSV4RRFFQ69G5FAX"},
			},
			want: []string{
				"WB-01ARZ3NDE",
				"WB-01ARZ3NDF",
				"WB-01BXZ3ND",
			},
		},
		{
			name: "identical minimum prefixes extend until distinct",
			tasks: []core.Task{
				{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"},
				{ID: "WB-01ARZ3NDE0TSV4RRFFQ69G5FAW"},
			},
			want: []string{
				"WB-01ARZ3NDEK",
				"WB-01ARZ3NDE0",
			},
		},
		{
			name: "a single task uses the minimum prefix",
			tasks: []core.Task{
				{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"},
			},
			want: []string{"WB-01ARZ3ND"},
		},
		{
			name: "different project key lengths retain eight ULID characters",
			tasks: []core.Task{
				{ID: "W-01ARZ3NDEKTSV4RRFFQ69G5FAV"},
				{ID: "WORKBOOK-01ARZ3NDEKTSV4RRFFQ69G5FAW"},
			},
			want: []string{
				"W-01ARZ3ND",
				"WORKBOOK-01ARZ3ND",
			},
		},
		{
			name: "final character divergence uses full IDs",
			tasks: []core.Task{
				{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"},
				{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAW"},
			},
			want: []string{
				"WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
				"WB-01ARZ3NDEKTSV4RRFFQ69G5FAW",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := append([]core.Task(nil), test.tasks...)
			got := TaskViews(test.tasks)
			if !reflect.DeepEqual(test.tasks, before) {
				t.Fatalf("TaskViews() mutated input tasks: got %#v, want %#v", test.tasks, before)
			}
			if len(got) != len(test.want) {
				t.Fatalf("TaskViews() returned %d views, want %d", len(got), len(test.want))
			}
			for i, view := range got {
				if !reflect.DeepEqual(view.Task, test.tasks[i]) {
					t.Errorf("TaskViews()[%d].Task = %#v, want %#v", i, view.Task, test.tasks[i])
				}
				if view.IDPrefix != test.want[i] {
					t.Errorf("TaskViews()[%d].IDPrefix = %q, want %q", i, view.IDPrefix, test.want[i])
				}
				assertUniquePrefix(t, test.tasks, view.IDPrefix)
			}
		})
	}
}

func TestNewBoardPreservesInputOrderAndIncludesEmptyColumns(t *testing.T) {
	tasks := []core.Task{
		{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV", TaskData: core.TaskData{Status: core.StatusDone}},
		{ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", TaskData: core.TaskData{Status: core.StatusBacklog}},
		{ID: "WB-01CRZ3NDEKTSV4RRFFQ69G5FAX", TaskData: core.TaskData{Status: core.StatusInProgress}},
		{ID: "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY", TaskData: core.TaskData{Status: core.StatusBacklog}},
	}

	got := NewBoard(tasks)
	if len(got.Columns) != 5 {
		t.Fatalf("NewBoard() returned %d columns, want 5", len(got.Columns))
	}
	want := []struct {
		status core.Status
		label  string
		ids    []string
	}{
		{core.StatusBacklog, "Backlog", []string{"WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY"}},
		{core.StatusReady, "Ready", []string{}},
		{core.StatusInProgress, "In progress", []string{"WB-01CRZ3NDEKTSV4RRFFQ69G5FAX"}},
		{core.StatusBlocked, "Blocked", []string{}},
		{core.StatusDone, "Done", []string{"WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
	}
	for i, column := range got.Columns {
		if column.Status != want[i].status {
			t.Errorf("NewBoard().Columns[%d].Status = %q, want %q", i, column.Status, want[i].status)
		}
		if column.Label != want[i].label {
			t.Errorf("NewBoard().Columns[%d].Label = %q, want %q", i, column.Label, want[i].label)
		}
		ids := make([]string, len(column.Tasks))
		for j, task := range column.Tasks {
			ids[j] = task.Task.ID
		}
		if !reflect.DeepEqual(ids, want[i].ids) {
			t.Errorf("NewBoard().Columns[%d] task IDs = %#v, want %#v", i, ids, want[i].ids)
		}
	}
}

func assertUniquePrefix(t *testing.T, tasks []core.Task, prefix string) {
	t.Helper()
	matches := 0
	for _, task := range tasks {
		if strings.HasPrefix(task.ID, prefix) {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("prefix %q matches %d task IDs, want 1", prefix, matches)
	}
}
