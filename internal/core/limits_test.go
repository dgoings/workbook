package core

import (
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeTaskRejectsOversizedFields(t *testing.T) {
	// Production mutation: accepting arbitrary bytes in a task document lets one
	// collaborator publish a task every other clone must read into memory.
	tests := []struct {
		name string
		task TaskData
	}{
		{
			name: "title over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Title = strings.Repeat("t", MaxTitleBytes+1)
			}),
		},
		{
			name: "title over the ceiling only after trimming is applied",
			task: sizedTask(func(task *TaskData) {
				task.Title = "  " + strings.Repeat("t", MaxTitleBytes+1) + "  "
			}),
		},
		{
			name: "description over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Description = strings.Repeat("d", MaxDescriptionBytes+1)
			}),
		},
		{
			name: "one label over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Labels = []string{"ok", strings.Repeat("l", MaxLabelBytes+1)}
			}),
		},
		{
			name: "more labels than the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Labels = distinctLabels(MaxLabelCount + 1)
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeTask("WB", test.task)
			if got, want := CategoryOf(err), CategoryValidation; got != want {
				t.Fatalf("NormalizeTask() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestNormalizeTaskAcceptsFieldsExactlyAtTheCeiling(t *testing.T) {
	// Production mutation: an off-by-one in a ceiling silently narrows what a
	// team may already have stored, which a patch release must not do.
	task := sizedTask(func(task *TaskData) {
		task.Title = strings.Repeat("t", MaxTitleBytes)
		task.Description = strings.Repeat("d", MaxDescriptionBytes)
		task.Labels = append(distinctLabels(MaxLabelCount-1), strings.Repeat("l", MaxLabelBytes))
	})

	normalized, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if len(normalized.Title) != MaxTitleBytes {
		t.Fatalf("NormalizeTask() title bytes = %d, want %d", len(normalized.Title), MaxTitleBytes)
	}
	if len(normalized.Description) != MaxDescriptionBytes {
		t.Fatalf("NormalizeTask() description bytes = %d, want %d", len(normalized.Description), MaxDescriptionBytes)
	}
	if len(normalized.Labels) != MaxLabelCount {
		t.Fatalf("NormalizeTask() labels = %d, want %d", len(normalized.Labels), MaxLabelCount)
	}
}

// TestNormalizeTaskCountsLabelsAfterDeduplication keeps the count ceiling
// describing the stored document rather than the caller's raw argument list.
func TestNormalizeTaskCountsLabelsAfterDeduplication(t *testing.T) {
	labels := make([]string, 0, MaxLabelCount*2)
	for range MaxLabelCount * 2 {
		labels = append(labels, "release")
	}
	task := sizedTask(func(task *TaskData) { task.Labels = labels })

	normalized, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if len(normalized.Labels) != 1 {
		t.Fatalf("NormalizeTask() labels = %#v, want one deduplicated label", normalized.Labels)
	}
}

func sizedTask(apply func(*TaskData)) TaskData {
	task := TaskData{Title: "Task", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"}
	apply(&task)
	return task
}

func distinctLabels(count int) []string {
	labels := make([]string, 0, count)
	for index := range count {
		labels = append(labels, "label-"+strconv.Itoa(index))
	}
	return labels
}
