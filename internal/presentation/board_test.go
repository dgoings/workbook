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

func TestTaskViewsSummarizeDependencyReadiness(t *testing.T) {
	done := core.Task{
		ID:       "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TaskData: core.TaskData{Status: core.StatusDone},
	}
	active := core.Task{
		ID:       "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW",
		TaskData: core.TaskData{Status: core.StatusInProgress},
	}
	ready := core.Task{
		ID: "WB-01CRZ3NDEKTSV4RRFFQ69G5FAX",
		TaskData: core.TaskData{
			Status: core.StatusReady,
			Dependencies: []string{
				done.ID,
				active.ID,
				"WB-01DRZ3NDEKTSV4RRFFQ69G5FAY",
			},
		},
	}
	inReview := ready
	inReview.ID = "WB-01ERZ3NDEKTSV4RRFFQ69G5FAZ"
	inReview.Status = core.StatusInReview
	withoutDependencies := core.Task{
		ID:       "WB-01FRZ3NDEKTSV4RRFFQ69G5FA0",
		TaskData: core.TaskData{Status: core.StatusReady},
	}
	tombstonedDone := core.Task{
		ID:       "WB-01GRZ3NDEKTSV4RRFFQ69G5FA1",
		TaskData: core.TaskData{Status: core.StatusDone, Deleted: true},
	}
	readyDependingOnTombstonedDone := core.Task{
		ID: "WB-01HRZ3NDEKTSV4RRFFQ69G5FA2",
		TaskData: core.TaskData{
			Status:       core.StatusReady,
			Dependencies: []string{tombstonedDone.ID},
		},
	}

	views := TaskViews([]core.Task{done, active, ready, inReview, withoutDependencies, tombstonedDone, readyDependingOnTombstonedDone})
	if got := views[2]; got.DependenciesComplete != 1 ||
		got.DependenciesTotal != 3 || !got.WaitingOnDependencies {
		t.Fatalf("ready dependency summary = %#v, want 1/3 waiting", got)
	}
	if got := views[3]; got.DependenciesComplete != 1 ||
		got.DependenciesTotal != 3 || got.WaitingOnDependencies {
		t.Fatalf("in-review dependency summary = %#v, want 1/3 not waiting", got)
	}
	if got := views[4]; got.DependenciesComplete != 0 ||
		got.DependenciesTotal != 0 || got.WaitingOnDependencies {
		t.Fatalf("dependency-free summary = %#v, want zero values", got)
	}
	if got := views[6]; got.DependenciesComplete != 0 ||
		got.DependenciesTotal != 1 || !got.WaitingOnDependencies {
		t.Fatalf("tombstoned Done dependency summary = %#v, want 0/1 waiting", got)
	}
}

func TestNewBoardPreservesInputOrderAndIncludesEmptyColumns(t *testing.T) {
	tasks := []core.Task{
		{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV", TaskData: core.TaskData{Status: core.StatusDone}},
		{ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", TaskData: core.TaskData{Status: core.StatusBacklog}},
		{ID: "WB-01CRZ3NDEKTSV4RRFFQ69G5FAX", TaskData: core.TaskData{Status: core.StatusInProgress}},
		{ID: "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY", TaskData: core.TaskData{Status: core.StatusBacklog}},
		{ID: "WB-01ERZ3NDEKTSV4RRFFQ69G5FAZ", TaskData: core.TaskData{Status: core.StatusInReview}},
	}

	got := NewBoard(tasks)
	if len(got.Columns) != 6 {
		t.Fatalf("NewBoard() returned %d columns, want 6", len(got.Columns))
	}
	want := []struct {
		status core.Status
		label  string
		ids    []string
	}{
		{core.StatusBacklog, "Backlog", []string{"WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY"}},
		{core.StatusReady, "Ready", []string{}},
		{core.StatusBlocked, "Blocked", []string{}},
		{core.StatusInProgress, "In Progress", []string{"WB-01CRZ3NDEKTSV4RRFFQ69G5FAX"}},
		{core.StatusInReview, "In Review", []string{"WB-01ERZ3NDEKTSV4RRFFQ69G5FAZ"}},
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

func TestNewBoardKeepsUnknownStatusesVisible(t *testing.T) {
	tasks := []core.Task{
		{ID: "WB-01ARZ3NDEKTSV4RRFFQ69G5FAV", TaskData: core.TaskData{Status: core.StatusReady}},
		{ID: "WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", TaskData: core.TaskData{Status: "future"}},
		{ID: "WB-01CRZ3NDEKTSV4RRFFQ69G5FAX", TaskData: core.TaskData{Status: core.StatusDone}},
		{ID: "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY", TaskData: core.TaskData{Status: "archived"}},
	}

	got := NewBoard(tasks)
	if len(got.Columns) != 6 {
		t.Fatalf("NewBoard() returned %d columns, want 6", len(got.Columns))
	}
	for i, want := range []struct {
		status core.Status
		label  string
		ids    []string
	}{
		{core.StatusBacklog, "Backlog", []string{}},
		{core.StatusReady, "Ready", []string{"WB-01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
		{core.StatusBlocked, "Blocked", []string{}},
		{core.StatusInProgress, "In Progress", []string{}},
		{core.StatusInReview, "In Review", []string{}},
		{core.StatusDone, "Done", []string{"WB-01CRZ3NDEKTSV4RRFFQ69G5FAX"}},
	} {
		column := got.Columns[i]
		if column.Status != want.status || column.Label != want.label {
			t.Errorf("NewBoard().Columns[%d] = {%q, %q}, want {%q, %q}", i, column.Status, column.Label, want.status, want.label)
		}
		ids := make([]string, len(column.Tasks))
		for j, task := range column.Tasks {
			ids[j] = task.Task.ID
		}
		if !reflect.DeepEqual(ids, want.ids) {
			t.Errorf("NewBoard().Columns[%d] task IDs = %#v, want %#v", i, ids, want.ids)
		}
	}

	unknownIDs := make([]string, len(got.UnknownTasks))
	for i, task := range got.UnknownTasks {
		unknownIDs[i] = task.Task.ID
	}
	if want := []string{"WB-01BRZ3NDEKTSV4RRFFQ69G5FAW", "WB-01DRZ3NDEKTSV4RRFFQ69G5FAY"}; !reflect.DeepEqual(unknownIDs, want) {
		t.Errorf("NewBoard().UnknownTasks IDs = %#v, want %#v", unknownIDs, want)
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
