package core

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestValidateDueDateAcceptsOnlyRealCalendarDates(t *testing.T) {
	for _, valid := range []string{"2026-12-01", "2028-02-29", "0001-01-01", "9999-12-31"} {
		if err := ValidateDueDate(valid); err != nil {
			t.Errorf("ValidateDueDate(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"", "2026-1-5", "2026-02-30", "2027-02-29", "26-12-01", "2026/12/01",
		"2026-12-01T00:00:00Z", " 2026-12-01", "tomorrow", strings.Repeat("9", 4000)} {
		err := ValidateDueDate(invalid)
		if CategoryOf(err) != CategoryValidation {
			t.Errorf("ValidateDueDate(%q) = %v, want a validation error", invalid, err)
		}
		if err != nil && len(err.Error()) > 200 {
			t.Errorf("ValidateDueDate quoted an oversized value: %d bytes", len(err.Error()))
		}
	}
}

func TestResolveDueDateTurnsEveryInputFormIntoAnAbsoluteDate(t *testing.T) {
	// Late in the evening, in a zone west of UTC, so a resolver that used the
	// UTC date instead of the writer's own would land on the next day.
	zone := time.FixedZone("UTC-5", -5*60*60)
	today := time.Date(2026, time.December, 31, 23, 30, 0, 0, zone)
	for input, want := range map[string]string{
		"2027-03-04": "2027-03-04",
		"today":      "2026-12-31",
		"Today":      "2026-12-31",
		"tomorrow":   "2027-01-01",
		"+0d":        "2026-12-31",
		"+3d":        "2027-01-03",
		"+60d":       "2027-03-01",
		" +1d ":      "2027-01-01",
		"+03d":       "2027-01-03",
		"+001d":      "2027-01-01",
		"+036500d":   "2126-12-07",
	} {
		got, err := ResolveDueDate(input, today)
		if err != nil || got != want {
			t.Errorf("ResolveDueDate(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, invalid := range []string{"", "yesterday", "fri", "+d", "+-1d", "++1d", "+ 1d", "+1 d", "+3", "3d", "+1w", "+36501d", "+999999d", "2026-13-01"} {
		if _, err := ResolveDueDate(invalid, today); CategoryOf(err) != CategoryValidation {
			t.Errorf("ResolveDueDate(%q) error = %v, want a validation error", invalid, err)
		}
	}
}

func TestDueStateOfJudgesTheBoundaryAroundToday(t *testing.T) {
	const today = "2026-11-22"
	for _, test := range []struct {
		due      string
		finished bool
		want     DueState
	}{
		{"", false, DueStateNone},
		{"2026-11-21", false, DueStateOverdue},
		{"2025-01-01", false, DueStateOverdue},
		{"2026-11-22", false, DueStateSoon},
		{"2026-11-25", false, DueStateSoon},
		{"2026-11-26", false, DueStateNone},
		{"2026-11-21", true, DueStateNone},
		{"2026-11-22", true, DueStateNone},
	} {
		if got := DueStateOf(test.due, test.finished, today); got != test.want {
			t.Errorf("DueStateOf(%q, finished=%t, %s) = %q, want %q", test.due, test.finished, today, got, test.want)
		}
	}
	// The window crosses a month and a year end the way a calendar does.
	if got := DueStateOf("2027-01-03", false, "2026-12-31"); got != DueStateSoon {
		t.Errorf("DueStateOf across a year end = %q, want due soon", got)
	}
}

func TestCompareDueSortsUndatedTasksLast(t *testing.T) {
	if CompareDue("2026-11-01", "2026-12-01") >= 0 || CompareDue("2026-12-01", "") >= 0 ||
		CompareDue("", "2026-12-01") <= 0 || CompareDue("", "") != 0 || CompareDue("2026-12-01", "2026-12-01") != 0 {
		t.Fatal("CompareDue does not order earliest first with undated tasks last")
	}
}

// dueFieldSet is a field.set of the due date at the next clock, written the way
// every mutation is, through NewOperationPack.
func dueFieldSet(state StateDocument, id, value string) OperationPack {
	return NewOperationPack(projectID, taskID, generationID, "developer@example.com",
		state.LogicalClock+1, updatedAt, []Operation{{ID: id, Type: OperationFieldSet, Field: "due", Value: value}})
}

func TestApplySetsChangesAndClearsADueDate(t *testing.T) {
	state := createdTask(t)
	if state.MinReader != 0 {
		t.Fatalf("a task created with no due date carries minReader %d, want none", state.MinReader)
	}
	steps := []struct{ id, value string }{
		{operationID2, "2026-12-01"},
		{operationID3, "2027-01-15"},
		{"01K0M6B8A4FTT8C39MXXYTW7C7", ""},
	}
	for _, step := range steps {
		pack := dueFieldSet(state, step.id, step.value)
		if pack.MinReader != dueMinReader {
			t.Fatalf("a pack recording a due date has minReader %d, want %d", pack.MinReader, dueMinReader)
		}
		next, err := Apply(&state, pack)
		if err != nil {
			t.Fatalf("Apply(due %q) error = %v", step.value, err)
		}
		if next.Task.Due != step.value {
			t.Fatalf("due = %q, want %q", next.Task.Due, step.value)
		}
		// The watermark is a running maximum: clearing the date does not hand
		// the task back to a reader that could never fold its history.
		if next.MinReader != dueMinReader {
			t.Fatalf("state minReader = %d, want %d", next.MinReader, dueMinReader)
		}
		if err := ValidateCheckpoint(&state, pack, next); err != nil {
			t.Fatalf("ValidateCheckpoint(due %q) = %v", step.value, err)
		}
		state = next
	}
	// Applying the same pack again — a duplicate delivery — computes the same
	// task: a scalar set is idempotent.
	again := dueFieldSet(state, "01K0M6B8A4FTT8C39MXXYTW7C8", "")
	replayed, err := Apply(&state, again)
	if err != nil || replayed.Task.Due != "" {
		t.Fatalf("re-applying the clear = %q, %v", replayed.Task.Due, err)
	}
}

func TestATaskWithNoDueDateEncodesAsItAlwaysHas(t *testing.T) {
	state := createdTask(t)
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if strings.Contains(string(encoded), `"due"`) || strings.Contains(string(encoded), `"minReader"`) {
		t.Fatalf("a task without a due date encodes a due member or a marker: %s", encoded)
	}
}

func TestADueDateWithoutItsMarkerIsCorrupt(t *testing.T) {
	state := createdTask(t)
	pack := dueFieldSet(state, operationID2, "2026-12-01")
	pack.MinReader = dueMinReader - 1
	assertCorrupt(t, applyError(&state, pack))

	create := createPack()
	created := *create.Operations[0].Task
	created.Due = "2026-12-01"
	create.Operations[0].Task = &created
	assertCorrupt(t, applyError(nil, create))
	create.MinReader = PackMinReader(create.Operations)
	if create.MinReader != dueMinReader {
		t.Fatalf("a create carrying a due date declares %d, want %d", create.MinReader, dueMinReader)
	}
	folded, err := Apply(nil, create)
	if err != nil || folded.Task.Due != "2026-12-01" || folded.MinReader != dueMinReader {
		t.Fatalf("Apply(create with due) = %#v, %v", folded, err)
	}

	// A stored state holding a date below the marker would reach an older
	// build as corruption, so this build will not accept one either.
	folded.MinReader = 0
	encoded, err := json.Marshal(folded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStateDocument(append(encoded, '\n')); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("DecodeStateDocument(due without marker) = %v, want corrupt data", err)
	}
}

func TestAnInvalidStoredDueDateIsCorrupt(t *testing.T) {
	state := createdTask(t)
	pack := dueFieldSet(state, operationID2, "2026-02-30")
	assertCorrupt(t, applyError(&state, pack))
}

func TestOnlyADueDateRaisesAFieldSetsGeneration(t *testing.T) {
	for _, operation := range []Operation{
		{Type: OperationFieldSet, Field: "title", Value: "x"},
		{Type: OperationFieldSet, Field: "priority", Value: "high"},
		{Type: OperationTaskCreate, Task: &TaskData{Title: "x"}},
	} {
		if got := OperationMinReader(operation); got != 0 {
			t.Errorf("OperationMinReader(%s %s) = %d, want 0", operation.Type, operation.Field, got)
		}
	}
	if got := OperationMinReader(Operation{Type: OperationFieldSet, Field: "due"}); got != dueMinReader {
		t.Errorf("a due clear declares %d, want %d", got, dueMinReader)
	}
	if got := OperationMinReader(Operation{Type: OperationHistoryCheckpoint, Task: &TaskData{Due: "2026-12-01"}}); got != dueMinReader {
		t.Errorf("a checkpoint holding a due date declares %d, want %d", got, dueMinReader)
	}
	if dueMinReader > SupportedFormatGeneration {
		t.Fatalf("this build writes generation %d and folds only %d", dueMinReader, SupportedFormatGeneration)
	}
}

func TestACheckpointCarriesTheDueDate(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := dueFieldSet(compacted, "01K0M6B8A4FTT8C39MXXYTW7C9", "2026-12-01")
	dated, err := Apply(&compacted, pack)
	if err != nil {
		t.Fatalf("Apply(due) error = %v", err)
	}
	ids = append(ids, "01K0M6B8A4FTT8C39MXXYTW7C9")
	sort.Strings(ids)
	checkpoint := checkpointPackFor(dated, ids)
	if checkpoint.MinReader != dueMinReader {
		t.Fatalf("a checkpoint of a dated task declares %d, want %d", checkpoint.MinReader, dueMinReader)
	}
	state, err := Apply(nil, checkpoint)
	if err != nil {
		t.Fatalf("Apply(checkpoint) error = %v", err)
	}
	if state.Task.Due != "2026-12-01" || state.MinReader != dueMinReader {
		t.Fatalf("checkpoint state due = %q minReader = %d", state.Task.Due, state.MinReader)
	}
}

func TestTaskJSONReportsDueAsNullWhenUnset(t *testing.T) {
	encode := func(value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	task := Task{ID: taskID, TaskData: TaskData{Title: "x"}}
	if got := encode(task); !strings.Contains(got, `"due":null`) || !strings.Contains(got, `"overdue":false`) || !strings.Contains(got, `"dueSoon":false`) {
		t.Fatalf("task JSON = %s, want due null and both flags false", got)
	}
	due := "2026-12-01"
	task.Due, task.ReportedDue, task.Overdue = due, &due, true
	detail := TaskDetail{Task: task}
	for _, got := range []string{encode(task), encode(detail)} {
		if !strings.Contains(got, `"due":"2026-12-01"`) || !strings.Contains(got, `"overdue":true`) || strings.Count(got, `"due"`) != 1 {
			t.Fatalf("task JSON = %s, want one due member holding the date", got)
		}
	}
}
