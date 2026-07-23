package core

import (
	"reflect"
	"testing"
)

func TestNormalizeTaskSortsSetsAndValidatesValues(t *testing.T) {
	task := TaskData{
		Title:    "  Build Git store  ",
		Status:   StatusReady,
		Priority: PriorityHigh,
		Labels:   []string{"poc", "git", "git"},
		Rank:     "2/1",
		Dependencies: []string{
			"WB-01K0M6B8A4FTT8C39MXXYTW7C3",
			"WB-01K0M6B8A4FTT8C39MXXYTW7C3",
		},
	}

	got, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if got.Title != "Build Git store" {
		t.Fatalf("NormalizeTask() title = %q, want %q", got.Title, "Build Git store")
	}
	if want := []string{"git", "poc"}; !reflect.DeepEqual(got.Labels, want) {
		t.Fatalf("NormalizeTask() labels = %#v, want %#v", got.Labels, want)
	}
	if want := []string{"WB-01K0M6B8A4FTT8C39MXXYTW7C3"}; !reflect.DeepEqual(got.Dependencies, want) {
		t.Fatalf("NormalizeTask() dependencies = %#v, want %#v", got.Dependencies, want)
	}
}

func TestNormalizeTaskAcceptsEveryStatusAndPriority(t *testing.T) {
	statuses := []Status{StatusBacklog, StatusReady, StatusInProgress, StatusBlocked, StatusDone}
	priorities := []Priority{PriorityLow, PriorityMedium, PriorityHigh}

	for _, status := range statuses {
		for _, priority := range priorities {
			t.Run(string(status)+"/"+string(priority), func(t *testing.T) {
				_, err := NormalizeTask("WB", TaskData{
					Title:    "Task",
					Status:   status,
					Priority: priority,
					Rank:     "1/1",
				})
				if err != nil {
					t.Fatalf("NormalizeTask() error = %v", err)
				}
			})
		}
	}
}

func TestNormalizeTaskRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		task TaskData
	}{
		{
			name: "blank title",
			task: TaskData{Title: " \t", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"},
		},
		{
			name: "unknown status",
			task: TaskData{Title: "Task", Status: "unknown", Priority: PriorityMedium, Rank: "1/1"},
		},
		{
			name: "unknown priority",
			task: TaskData{Title: "Task", Status: StatusReady, Priority: "urgent", Rank: "1/1"},
		},
		{
			name: "empty label",
			task: TaskData{Title: "Task", Status: StatusReady, Priority: PriorityMedium, Labels: []string{""}, Rank: "1/1"},
		},
		{
			name: "invalid dependency",
			task: TaskData{Title: "Task", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1", Dependencies: []string{"WB-not-a-ulid"}},
		},
	}

	for _, rank := range []string{"", "0/1", "2/2", "1", "1/01", " 1/1"} {
		tests = append(tests, struct {
			name string
			task TaskData
		}{
			name: "rank/" + rank,
			task: TaskData{Title: "Task", Status: StatusReady, Priority: PriorityMedium, Rank: rank},
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CategoryOf(mustNormalize(test.task)); got != CategoryValidation {
				t.Fatalf("NormalizeTask() category = %q, want %q", got, CategoryValidation)
			}
		})
	}
}

func mustNormalize(task TaskData) error {
	_, err := NormalizeTask("WB", task)
	return err
}
