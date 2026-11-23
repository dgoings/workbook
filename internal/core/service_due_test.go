package core

import (
	"context"
	"testing"
	"time"
)

// serviceTestToday is serviceTestNow's date wherever the test runs: it is the
// middle of the afternoon in UTC, which is the same calendar day in every zone
// from UTC-12 to UTC+8.
const serviceTestToday = "2026-07-23"

// datedSnapshot is a stored task that carries a due date, with the watermark a
// history that recorded one has.
func datedSnapshot(id string, task TaskData) Snapshot {
	snapshot := serviceSnapshot(id, task)
	if task.Due != "" {
		snapshot.State.MinReader = dueMinReader
	}
	return snapshot
}

// dueServiceUnderTest is serviceUnderTest reading as well as recording at
// serviceTestNow, so its today is serviceTestToday whatever the real date is.
func dueServiceUnderTest(store *memoryTaskStore, ids IDSource) Service {
	service := serviceUnderTest(store, ids)
	service.ReadNow = service.Now
	return service
}

func dueListIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for index, task := range tasks {
		ids[index] = task.ID
	}
	return ids
}

func dueFixture() *memoryTaskStore {
	return newMemoryTaskStore(
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "Yesterday", Status: StatusReady, Priority: PriorityLow, Rank: "1/1", Due: "2026-07-22"}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "Today", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1", Due: serviceTestToday}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "In three days", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Due: "2026-07-26"}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E4", TaskData{Title: "In four days", Status: StatusBacklog, Priority: PriorityMedium, Rank: "2/1", Due: "2026-07-27"}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E5", TaskData{Title: "Done late", Status: StatusDone, Priority: PriorityMedium, Rank: "1/1", Due: "2026-07-01"}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E6", TaskData{Title: "Deleted late", Status: StatusReady, Priority: PriorityMedium, Rank: "2/1", Due: "2026-07-01", Deleted: true}),
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E7", TaskData{Title: "Undated", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1"}),
	)
}

func TestServiceProjectsOverdueAndDueSoonAgainstTheReadersToday(t *testing.T) {
	service := dueServiceUnderTest(dueFixture(), &sequenceIDSource{})
	if got := service.Today(); got != serviceTestToday {
		t.Fatalf("Today() = %q, want %q", got, serviceTestToday)
	}
	tasks, err := service.List(context.Background(), ListFilter{All: true})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := map[string]DueState{
		"Yesterday": DueStateOverdue, "Today": DueStateSoon, "In three days": DueStateSoon,
		"In four days": DueStateNone, "Done late": DueStateNone, "Deleted late": DueStateNone, "Undated": DueStateNone,
	}
	for _, task := range tasks {
		if got := task.DueState(); got != want[task.Title] {
			t.Errorf("%s due state = %q, want %q", task.Title, got, want[task.Title])
		}
		if (task.ReportedDue == nil) != (task.Due == "") || (task.ReportedDue != nil && *task.ReportedDue != task.Due) {
			t.Errorf("%s reported due = %v, stored %q", task.Title, task.ReportedDue, task.Due)
		}
	}
}

func TestServiceListFiltersAndSortsByDueDate(t *testing.T) {
	service := dueServiceUnderTest(dueFixture(), &sequenceIDSource{})
	ctx := context.Background()

	overdue, err := service.List(ctx, ListFilter{Overdue: true, All: true})
	if err != nil {
		t.Fatalf("List(overdue) error = %v", err)
	}
	if got := dueListIDs(overdue); len(got) != 1 || got[0] != "WB-01K0M6B8A4FTT8C39MXXYTW7E1" {
		t.Fatalf("List(overdue) = %v, want only the task due yesterday", got)
	}

	before, err := service.List(ctx, ListFilter{DueBefore: "2026-07-26"})
	if err != nil {
		t.Fatalf("List(due before) error = %v", err)
	}
	titles := map[string]bool{}
	for _, task := range before {
		titles[task.Title] = true
	}
	if len(before) != 3 || !titles["Yesterday"] || !titles["Today"] || !titles["Done late"] {
		t.Fatalf("List(due before 2026-07-26) = %v, want the tasks due strictly before it", titles)
	}

	sorted, err := service.List(ctx, ListFilter{Sort: ListSortDue})
	if err != nil {
		t.Fatalf("List(sort due) error = %v", err)
	}
	got := make([]string, len(sorted))
	for index, task := range sorted {
		got[index] = task.Title
	}
	wantOrder := []string{"Done late", "Yesterday", "Today", "In three days", "In four days", "Undated"}
	if len(got) != len(wantOrder) {
		t.Fatalf("List(sort due) = %v, want %v", got, wantOrder)
	}
	for index := range wantOrder {
		if got[index] != wantOrder[index] {
			t.Fatalf("List(sort due) = %v, want %v", got, wantOrder)
		}
	}

	if _, err := service.List(ctx, ListFilter{DueBefore: "2026-13-01"}); CategoryOf(err) != CategoryValidation {
		t.Fatalf("List(invalid due before) error = %v, want validation", err)
	}
	if _, err := service.List(ctx, ListFilter{Sort: "priority"}); CategoryOf(err) != CategoryValidation {
		t.Fatalf("List(unknown sort) error = %v, want validation", err)
	}
}

// The recording clock is not the reading clock: a migration that records
// changes in the past still judges overdue against the real today.
func TestServiceJudgesDueStatesByTheReadingClockNotTheRecordingOne(t *testing.T) {
	service := serviceUnderTest(dueFixture(), &sequenceIDSource{})
	service.Now = func() time.Time { return time.Date(2001, time.January, 1, 12, 0, 0, 0, time.UTC) }
	if got, want := service.Today(), LocalDate(time.Now()); got != want {
		t.Fatalf("Today() with no reading clock = %q, want the system's %q", got, want)
	}
	tasks, err := service.List(context.Background(), ListFilter{Overdue: true})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	// Against 2001 nothing in the fixture would be overdue; against the real
	// today every live dated task not yet done is.
	if len(tasks) != 4 {
		t.Fatalf("List(overdue) = %v, want the four live, unfinished dated tasks", dueListIDs(tasks))
	}
}

func TestServiceNeverFlagsAFinishedTaskThroughShow(t *testing.T) {
	// Show projects through the same path List does, so a done task long past
	// its date reads as finished there too.
	service := dueServiceUnderTest(newMemoryTaskStore(
		datedSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "Shipped", Status: StatusDone, Priority: PriorityLow, Rank: "1/1", Due: "2026-07-01"}),
	), &sequenceIDSource{})
	task, err := service.Show(context.Background(), "WB-01K0M6B8A4FTT8C39MXXYTW7E1")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if task.Overdue || task.DueSoon {
		t.Fatalf("a done task is flagged: overdue %t, due soon %t", task.Overdue, task.DueSoon)
	}
}

func TestServiceCreateAndUpdateRecordTheDueDateAsOperations(t *testing.T) {
	store := newMemoryTaskStore()
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3", "01K0M6B8A4FTT8C39MXXYTW7D4",
		"01K0M6B8A4FTT8C39MXXYTW7D5", "01K0M6B8A4FTT8C39MXXYTW7D6",
	}}
	service := serviceUnderTest(store, ids)
	ctx := context.Background()

	created, err := service.CreateMutation(ctx, CreateInput{Title: "Dated", Due: "2026-08-01"})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	create := store.writes[0]
	if create.pack.MinReader != dueMinReader || create.state.MinReader != dueMinReader || create.pack.Operations[0].Task.Due != "2026-08-01" {
		t.Fatalf("create pack = %#v", create.pack)
	}
	if created.Task.Due != "2026-08-01" {
		t.Fatalf("created due = %q", created.Task.Due)
	}

	changed := "2026-09-01"
	if _, err := service.UpdateMutation(ctx, created.Task.ID, UpdateInput{Due: &changed}); err != nil {
		t.Fatalf("UpdateMutation(change) error = %v", err)
	}
	update := store.writes[1]
	assertOperations(t, update.pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D5", Type: OperationFieldSet, Field: "due", Value: "2026-09-01"},
	})
	if update.reason != "workbook: update WB-01K0M6B8 due 2026-09-01" {
		t.Fatalf("change reason = %q", update.reason)
	}

	cleared := ""
	if _, err := service.UpdateMutation(ctx, created.Task.ID, UpdateInput{Due: &cleared}); err != nil {
		t.Fatalf("UpdateMutation(clear) error = %v", err)
	}
	clear := store.writes[2]
	if len(clear.pack.Operations) != 1 || clear.pack.Operations[0].Field != "due" || clear.pack.Operations[0].Value != "" {
		t.Fatalf("clear operations = %#v", clear.pack.Operations)
	}
	if clear.reason != "workbook: update WB-01K0M6B8 due cleared" || clear.state.Task.Due != "" {
		t.Fatalf("clear reason = %q, due = %q", clear.reason, clear.state.Task.Due)
	}

	// Clearing a date that is already gone changes nothing, and says so.
	if _, err := service.UpdateMutation(ctx, created.Task.ID, UpdateInput{Due: &cleared}); CategoryOf(err) != CategoryValidation {
		t.Fatalf("UpdateMutation(clear again) error = %v, want validation", err)
	}
}

func TestServiceRefusesAnInvalidDueDateAsValidation(t *testing.T) {
	store := newMemoryTaskStore(serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Existing", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	}))
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3", "01K0M6B8A4FTT8C39MXXYTW7D4",
	}})
	ctx := context.Background()
	if _, err := service.CreateMutation(ctx, CreateInput{Title: "Bad", Due: "2026-02-30"}); CategoryOf(err) != CategoryValidation {
		t.Fatalf("CreateMutation(invalid due) error = %v, want validation", err)
	}
	bad := "next week"
	if _, err := service.UpdateMutation(ctx, "WB-01K0M6B8A4FTT8C39MXXYTW7D1", UpdateInput{Due: &bad}); CategoryOf(err) != CategoryValidation {
		t.Fatalf("UpdateMutation(invalid due) error = %v, want validation", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("an invalid due date wrote %d packs", len(store.writes))
	}
}
