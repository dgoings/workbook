package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var serviceTestNow = time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)

var serviceTestConfig = ProjectConfig{
	Format:    "workbook.project",
	Version:   1,
	ProjectID: "01K0M6B8A4FTT8C39MXXYTW7D0",
	Key:       "WB",
}

func TestServiceCreateBuildsOneRootPackWithSeparateIDsAndBucketRank(t *testing.T) {
	store := newMemoryTaskStore(serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Existing", Status: StatusBacklog, Priority: PriorityMedium, Rank: "7/1",
	}))
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D2",
		"01K0M6B8A4FTT8C39MXXYTW7D3",
		"01K0M6B8A4FTT8C39MXXYTW7D4",
	}}
	service := serviceUnderTest(store, ids)

	task, err := service.Create(context.Background(), CreateInput{Title: "  Build service  "})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if got, want := task.ID, "WB-01K0M6B8A4FTT8C39MXXYTW7D2"; got != want {
		t.Fatalf("Create() task ID = %q, want %q", got, want)
	}
	if got, want := task.HistoryGeneration, "01K0M6B8A4FTT8C39MXXYTW7D3"; got != want {
		t.Fatalf("Create() history generation = %q, want %q", got, want)
	}
	if got, want := task.Head, "head-1"; got != want {
		t.Fatalf("Create() head = %q, want %q", got, want)
	}
	if got, want := task.TaskData, (TaskData{
		Title: "Build service", Status: StatusBacklog, Priority: PriorityMedium,
		Labels: []string{}, Dependencies: []string{}, Rank: "8/1",
		CreatedAt: serviceTestNow, UpdatedAt: serviceTestNow,
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Create() task data = %#v, want %#v", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
	write := store.writes[0]
	if write.parent != nil {
		t.Fatalf("Create() Write() parent = %#v, want nil", write.parent)
	}
	if got, want := write.pack.LogicalClock, uint64(1); got != want {
		t.Fatalf("Create() logical clock = %d, want %d", got, want)
	}
	if got, want := write.pack.Operations, []Operation{{
		ID: "01K0M6B8A4FTT8C39MXXYTW7D4", Type: OperationTaskCreate, Task: &task.TaskData,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Create() operations = %#v, want %#v", got, want)
	}
}

func TestServiceClassifiesIDGenerationFailureAsOperational(t *testing.T) {
	cause := errors.New("random source failed")
	store := newMemoryTaskStore()
	service := serviceUnderTest(store, IDSourceFunc(func() (string, error) {
		return "", cause
	}))

	_, err := service.Create(context.Background(), CreateInput{Title: "Task"})
	if got, want := CategoryOf(err), CategoryOperational; got != want {
		t.Fatalf("Create() category = %q, want %q; error = %v", got, want, err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Create() error = %v, want cause %v", err, cause)
	}
	if len(store.writes) != 0 {
		t.Fatalf("Create() writes = %d, want none", len(store.writes))
	}
}

func TestListFiltersTombstonesAndUsesDeterministicOrder(t *testing.T) {
	backlogMedium := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "medium", Status: StatusBacklog, Priority: PriorityMedium, Rank: "2/1", Labels: []string{"api"}})
	backlogHighLater := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "high later", Status: StatusBacklog, Priority: PriorityHigh, Rank: "2/1", Labels: []string{"ui"}})
	backlogHighFirst := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D3", TaskData{Title: "high first", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1", Labels: []string{"api"}})
	readyLow := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D4", TaskData{Title: "ready", Status: StatusReady, Priority: PriorityLow, Rank: "1/1", Labels: []string{"api"}})
	deleted := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D5", TaskData{Title: "deleted", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1", Deleted: true})
	service := serviceUnderTest(newMemoryTaskStore(readyLow, deleted, backlogMedium, backlogHighLater, backlogHighFirst), &sequenceIDSource{})

	got, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	assertTaskIDs(t, got, []string{
		backlogHighFirst.State.TaskID,
		backlogHighLater.State.TaskID,
		backlogMedium.State.TaskID,
		readyLow.State.TaskID,
	})

	all, err := service.List(context.Background(), ListFilter{All: true})
	if err != nil {
		t.Fatalf("List(All) error = %v", err)
	}
	assertTaskIDs(t, all, []string{
		backlogHighFirst.State.TaskID,
		deleted.State.TaskID,
		backlogHighLater.State.TaskID,
		backlogMedium.State.TaskID,
		readyLow.State.TaskID,
	})

	status, priority := StatusBacklog, PriorityHigh
	filtered, err := service.List(context.Background(), ListFilter{Status: &status, Priority: &priority, Label: "ui"})
	if err != nil {
		t.Fatalf("List(filtered) error = %v", err)
	}
	assertTaskIDs(t, filtered, []string{backlogHighLater.State.TaskID})
}

func TestServiceListOrdersRanksAsExactRationals(t *testing.T) {
	twoThirds := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "two thirds", Status: StatusBacklog, Priority: PriorityHigh, Rank: "2/3"})
	nineTenths := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "nine tenths", Status: StatusBacklog, Priority: PriorityHigh, Rank: "9/10"})
	service := serviceUnderTest(newMemoryTaskStore(nineTenths, twoThirds), &sequenceIDSource{})

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	assertTaskIDs(t, tasks, []string{twoThirds.State.TaskID, nineTenths.State.TaskID})
}

func TestServiceNextSelectsReadyTaskByPriorityExactRankAndID(t *testing.T) {
	medium := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "medium", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"})
	highLater := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "high later", Status: StatusReady, Priority: PriorityHigh, Rank: "9/10"})
	highFirstByID := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D3", TaskData{Title: "high first", Status: StatusReady, Priority: PriorityHigh, Rank: "2/3"})
	highSecondByID := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D4", TaskData{Title: "high second", Status: StatusReady, Priority: PriorityHigh, Rank: "2/3"})
	backlog := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D5", TaskData{Title: "backlog", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1"})
	deleted := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D6", TaskData{Title: "deleted", Status: StatusReady, Priority: PriorityHigh, Rank: "1/2", Deleted: true})
	lowEarlier := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D7", TaskData{Title: "low earlier", Status: StatusReady, Priority: PriorityLow, Rank: "1/2"})
	store := newMemoryTaskStore(medium, highLater, highSecondByID, deleted, highFirstByID, backlog, lowEarlier)
	service := serviceUnderTest(store, &sequenceIDSource{})

	task, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got, want := task.ID, highFirstByID.State.TaskID; got != want {
		t.Fatalf("Next() ID = %q, want %q", got, want)
	}
	if got, want := store.listCalls, 1; got != want {
		t.Fatalf("Next() List() calls = %d, want %d", got, want)
	}
}

func TestServiceNextPrefersMediumPriorityOverLowPriority(t *testing.T) {
	medium := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "medium", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"})
	low := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "low", Status: StatusReady, Priority: PriorityLow, Rank: "1/2"})
	service := serviceUnderTest(newMemoryTaskStore(low, medium), &sequenceIDSource{})

	task, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got, want := task.ID, medium.State.TaskID; got != want {
		t.Fatalf("Next() ID = %q, want %q", got, want)
	}
}

func TestServiceNextRequiresEveryDependencyToBeActiveAndDone(t *testing.T) {
	done := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "done", Status: StatusDone, Priority: PriorityLow, Rank: "1/1"})
	notDone := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "not done", Status: StatusInProgress, Priority: PriorityLow, Rank: "1/1"})
	tombstoned := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D3", TaskData{Title: "tombstoned", Status: StatusDone, Priority: PriorityLow, Rank: "1/1", Deleted: true})
	eligible := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D4", TaskData{Title: "eligible", Status: StatusReady, Priority: PriorityLow, Rank: "4/1", Dependencies: []string{done.State.TaskID}})
	missing := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D5", TaskData{Title: "missing", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1", Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D9"}})
	blocked := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "blocked", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1", Dependencies: []string{notDone.State.TaskID}})
	deletedDependency := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "deleted dependency", Status: StatusReady, Priority: PriorityHigh, Rank: "3/1", Dependencies: []string{tombstoned.State.TaskID}})
	service := serviceUnderTest(newMemoryTaskStore(done, notDone, tombstoned, eligible, missing, blocked, deletedDependency), &sequenceIDSource{})

	task, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got, want := task.ID, eligible.State.TaskID; got != want {
		t.Fatalf("Next() ID = %q, want %q", got, want)
	}
}

func TestServiceNextReturnsNilWhenNoTaskIsEligible(t *testing.T) {
	done := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "done", Status: StatusDone, Priority: PriorityHigh, Rank: "1/1"})
	blocked := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "blocked", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1", Dependencies: []string{done.State.TaskID, "WB-01K0M6B8A4FTT8C39MXXYTW7D9"}})
	service := serviceUnderTest(newMemoryTaskStore(done, blocked), &sequenceIDSource{})

	task, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if task != nil {
		t.Fatalf("Next() = %#v, want nil", task)
	}
}

func TestNextRankAppendsAfterMaximumRationalRank(t *testing.T) {
	first := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "first", Status: StatusBacklog, Priority: PriorityHigh, Rank: "7/2"})
	second := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "second", Status: StatusBacklog, Priority: PriorityHigh, Rank: "9/2"})

	got, err := nextRank([]Snapshot{first, second}, StatusBacklog, PriorityHigh)
	if err != nil {
		t.Fatalf("nextRank() error = %v", err)
	}
	if want := "11/2"; got != want {
		t.Fatalf("nextRank() = %q, want %q", got, want)
	}
}

func TestServiceShowResolvesPrefixAndIncludesTombstone(t *testing.T) {
	deleted := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "gone", Status: StatusDone, Priority: PriorityLow, Rank: "1/1", Deleted: true})
	service := serviceUnderTest(newMemoryTaskStore(deleted), &sequenceIDSource{})

	task, err := service.Show(context.Background(), "wb-01k0m6b8")
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if got, want := task.ID, deleted.State.TaskID; got != want {
		t.Fatalf("Show() ID = %q, want %q", got, want)
	}
	if !task.Deleted {
		t.Fatal("Show() deleted = false, want true")
	}
}

func TestServiceUpdateBuildsOneDeterministicPackFromNormalizedValues(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Description: "old", Status: StatusBacklog, Priority: PriorityMedium,
		Rank: "1/1", Labels: []string{"alpha", "zeta"},
	})
	store := newMemoryTaskStore(snapshot)
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3",
		"01K0M6B8A4FTT8C39MXXYTW7D4", "01K0M6B8A4FTT8C39MXXYTW7D5",
		"01K0M6B8A4FTT8C39MXXYTW7D6", "01K0M6B8A4FTT8C39MXXYTW7D7",
		"01K0M6B8A4FTT8C39MXXYTW7D8",
	}}
	service := serviceUnderTest(store, ids)
	title, description := "  New title  ", "new"
	status, priority := StatusReady, PriorityHigh
	labels := []string{"beta", "alpha", "beta"}

	task, err := service.Update(context.Background(), snapshot.State.TaskID[:10], UpdateInput{
		Title: &title, Description: &description, Status: &status, Priority: &priority, Labels: &labels,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, want := task.Head, "head-1"; got != want {
		t.Fatalf("Update() head = %q, want %q", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
	write := store.writes[0]
	if got, want := write.parent.Head, snapshot.Head; got != want {
		t.Fatalf("Update() observed parent head = %q, want %q", got, want)
	}
	if got, want := write.pack.LogicalClock, uint64(2); got != want {
		t.Fatalf("Update() logical clock = %d, want %d", got, want)
	}
	assertOperations(t, write.pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D2", Type: OperationFieldSet, Field: "description", Value: "new"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D3", Type: OperationFieldSet, Field: "priority", Value: "high"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D4", Type: OperationFieldSet, Field: "status", Value: "ready"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D5", Type: OperationFieldSet, Field: "title", Value: "New title"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D6", Type: OperationSetRemove, Field: "labels", Value: "zeta"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7D7", Type: OperationSetAdd, Field: "labels", Value: "beta"},
	})
}

func TestServiceUpdateRejectsNormalizedNoopWithoutWriting(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Labels: []string{"alpha", "beta"},
	})
	store := newMemoryTaskStore(snapshot)
	ids := &sequenceIDSource{}
	service := serviceUnderTest(store, ids)
	title := "  Title  "
	labels := []string{"beta", "alpha", "alpha"}

	_, err := service.Update(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title, Labels: &labels})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Update() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
	if got, want := ids.calls, 0; got != want {
		t.Fatalf("IDSource calls = %d, want %d", got, want)
	}
}

func TestServiceDeleteTombstonesTaskAndRejectsFurtherMutations(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "Delete me", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"}})

	task, err := service.Delete(context.Background(), snapshot.State.TaskID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !task.Deleted {
		t.Fatal("Delete() task is not tombstoned")
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7D2", Type: OperationTaskTombstone}})

	title := "cannot update"
	_, err = service.Update(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Update(tombstone) error category = %q, want %q (error: %v)", got, want, err)
	}
	_, err = service.Delete(context.Background(), snapshot.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Delete(tombstone) error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls after tombstone = %d, want %d", got, want)
	}
}

func TestServicePropagatesStaleWriteWithoutRetry(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "Race", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	store := newMemoryTaskStore(snapshot)
	store.writeErr = Errorf(CategoryStaleWrite, "task ref changed concurrently")
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"}})
	title := "Race resolved"

	_, err := service.Update(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title})
	if got, want := CategoryOf(err), CategoryStaleWrite; got != want {
		t.Fatalf("Update() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
}

func TestServiceCreateRejectsInvalidOrDuplicateGeneratedIDsBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
	}{
		{
			name: "malformed operation ID",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3", "not-a-ulid"},
		},
		{
			name: "noncanonical history generation",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D2", "01k0m6b8a4ftt8c39mxxytw7d3", "01K0M6B8A4FTT8C39MXXYTW7D4"},
		},
		{
			name: "duplicate task and history IDs",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D4"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryTaskStore()
			service := serviceUnderTest(store, &sequenceIDSource{values: test.ids})

			_, err := service.Create(context.Background(), CreateInput{Title: "Reject invalid IDs"})
			if got, want := CategoryOf(err), CategoryValidation; got != want {
				t.Fatalf("Create() error category = %q, want %q (error: %v)", got, want, err)
			}
			if got, want := len(store.writes), 0; got != want {
				t.Fatalf("Write() calls = %d, want %d", got, want)
			}
		})
	}
}

func TestServiceUpdateRejectsInvalidOrCollidingOperationIDsBeforeWrite(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Description: "old", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	tests := []struct {
		name string
		ids  []string
	}{
		{
			name: "malformed operation ID",
			ids:  []string{"not-a-ulid", "01K0M6B8A4FTT8C39MXXYTW7D2"},
		},
		{
			name: "noncanonical operation ID",
			ids:  []string{"01k0m6b8a4ftt8c39mxxytw7d2", "01K0M6B8A4FTT8C39MXXYTW7D3"},
		},
		{
			name: "duplicate operations",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D2"},
		},
		{
			name: "operation duplicates task ULID suffix",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D1", "01K0M6B8A4FTT8C39MXXYTW7D2"},
		},
		{
			name: "operation duplicates history generation",
			ids:  []string{"01K0M6B8A4FTT8C39MXXYTW7D9", "01K0M6B8A4FTT8C39MXXYTW7D2"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryTaskStore(snapshot)
			service := serviceUnderTest(store, &sequenceIDSource{values: test.ids})
			title, description := "New title", "new"

			_, err := service.Update(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title, Description: &description})
			if got, want := CategoryOf(err), CategoryValidation; got != want {
				t.Fatalf("Update() error category = %q, want %q (error: %v)", got, want, err)
			}
			if got, want := len(store.writes), 0; got != want {
				t.Fatalf("Write() calls = %d, want %d", got, want)
			}
		})
	}
}

func TestServiceDeleteRejectsInvalidGeneratedIDBeforeWrite(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "Delete", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"not-a-ulid"}})

	_, err := service.Delete(context.Background(), snapshot.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Delete() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
}

func TestServiceMovePlacesTaskBetweenAnchorAndNeighborWithoutWritingAnotherTask(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1"})
	previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "previous", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	store := newMemoryTaskStore(moved, previous, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E4"}})

	task, err := service.Move(context.Background(), moved.State.TaskID, MoveInput{Before: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := task.Rank, "3/1"; got != want {
		t.Fatalf("Move() rank = %q, want %q", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
	if got, want := store.writes[0].parent.State.TaskID, moved.State.TaskID; got != want {
		t.Fatalf("Move() wrote task = %q, want %q", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7E4", Type: OperationFieldSet, Field: "rank", Value: "3/1"}})
}

func TestServiceMoveReturnsExistingTaskWhenEquivalentPlacementKeepsRank(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "3/1"})
	previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "previous", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	store := newMemoryTaskStore(moved, previous, anchor)
	ids := &sequenceIDSource{}
	service := serviceUnderTest(store, ids)

	task, err := service.Move(context.Background(), moved.State.TaskID, MoveInput{Before: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := task, Project(moved); !reflect.DeepEqual(got, want) {
		t.Fatalf("Move() task = %#v, want existing %#v", got, want)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Move() Write() calls = %d, want %d", got, want)
	}
	if got, want := ids.calls, 0; got != want {
		t.Fatalf("Move() ID requests = %d, want %d", got, want)
	}
}

func TestServiceMovePlacesTaskAtBucketBoundaries(t *testing.T) {
	first := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "first", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	last := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "last", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	before := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "before", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1"})
	after := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E4", TaskData{Title: "after", Status: StatusReady, Priority: PriorityHigh, Rank: "10/1"})
	store := newMemoryTaskStore(first, last, before, after)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E5", "01K0M6B8A4FTT8C39MXXYTW7E6"}})

	gotBefore, err := service.Move(context.Background(), before.State.TaskID, MoveInput{Before: first.State.TaskID})
	if err != nil {
		t.Fatalf("Move(before boundary) error = %v", err)
	}
	if got, want := gotBefore.Rank, "1/1"; got != want {
		t.Fatalf("Move(before boundary) rank = %q, want %q", got, want)
	}
	gotAfter, err := service.Move(context.Background(), after.State.TaskID, MoveInput{After: last.State.TaskID})
	if err != nil {
		t.Fatalf("Move(after boundary) error = %v", err)
	}
	if got, want := gotAfter.Rank, "5/1"; got != want {
		t.Fatalf("Move(after boundary) rank = %q, want %q", got, want)
	}
}

func TestServiceMovePlacesTaskAfterAnchorBeforeFollowingRank(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	following := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "following", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	service := serviceUnderTest(newMemoryTaskStore(moved, anchor, following), &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E4"}})

	task, err := service.Move(context.Background(), moved.State.TaskID, MoveInput{After: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := task.Rank, "3/1"; got != want {
		t.Fatalf("Move() rank = %q, want %q", got, want)
	}
}

func TestServiceMoveAfterFractionalLastRankUsesNextInteger(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1"})
	last := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "last", Status: StatusReady, Priority: PriorityHigh, Rank: "3/2"})
	service := serviceUnderTest(newMemoryTaskStore(moved, last), &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"}})

	task, err := service.Move(context.Background(), moved.State.TaskID, MoveInput{After: last.State.TaskID})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := task.Rank, "2/1"; got != want {
		t.Fatalf("Move() rank = %q, want %q", got, want)
	}
}

func TestServiceMoveRejectsSelfAndCrossBucketAnchorsWithoutWriting(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	crossBucket := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "other", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1"})
	store := newMemoryTaskStore(moved, crossBucket)
	service := serviceUnderTest(store, &sequenceIDSource{})

	for _, input := range []MoveInput{{}, {Before: moved.State.TaskID}, {After: crossBucket.State.TaskID}, {Before: crossBucket.State.TaskID, After: moved.State.TaskID}} {
		_, err := service.Move(context.Background(), moved.State.TaskID, input)
		if got, want := CategoryOf(err), CategoryValidation; got != want {
			t.Fatalf("Move(%#v) category = %q, want %q (error: %v)", input, got, want, err)
		}
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
}

func TestServiceDependAddsEdgeToDependentTask(t *testing.T) {
	dependent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "dependent", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	dependency := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "dependency", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	store := newMemoryTaskStore(dependent, dependency)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"}})

	task, err := service.Depend(context.Background(), dependent.State.TaskID, dependency.State.TaskID)
	if err != nil {
		t.Fatalf("Depend() error = %v", err)
	}
	if got, want := task.Dependencies, []string{dependency.State.TaskID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Depend() dependencies = %#v, want %#v", got, want)
	}
	if got, want := store.writes[0].parent.State.TaskID, dependent.State.TaskID; got != want {
		t.Fatalf("Depend() wrote task = %q, want %q", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7E3", Type: OperationSetAdd, Field: "dependencies", Value: dependency.State.TaskID}})
}

func TestServiceDependRejectsMissingTombstonedAndSelfEndpointsWithoutWriting(t *testing.T) {
	active := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "active", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	tombstoned := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "tombstoned", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1", Deleted: true})
	store := newMemoryTaskStore(active, tombstoned)
	service := serviceUnderTest(store, &sequenceIDSource{})

	for _, dependency := range []string{"WB-01K0M6B8A4FTT8C39MXXYTW7E3", tombstoned.State.TaskID, active.State.TaskID} {
		_, err := service.Depend(context.Background(), active.State.TaskID, dependency)
		if got := CategoryOf(err); got != CategoryNotFound && got != CategoryValidation {
			t.Fatalf("Depend(%q) category = %q, want not-found or validation (error: %v)", dependency, got, err)
		}
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Depend() Write() calls = %d, want %d", got, want)
	}
}

func TestServiceFreeRemovesExistingDependencyAndIsIdempotentWhenAbsent(t *testing.T) {
	dependency := "WB-01K0M6B8A4FTT8C39MXXYTW7E2"
	dependent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "dependent", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1", Dependencies: []string{dependency}})
	store := newMemoryTaskStore(dependent, serviceSnapshot(dependency, TaskData{Title: "dependency", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1", Deleted: true}))
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"}})

	task, err := service.Free(context.Background(), dependent.State.TaskID, dependency)
	if err != nil {
		t.Fatalf("Free() error = %v", err)
	}
	if got, want := task.Dependencies, []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Free() dependencies = %#v, want %#v", got, want)
	}
	_, err = service.Free(context.Background(), dependent.State.TaskID, dependency)
	if err != nil {
		t.Fatalf("Free(absent) error = %v", err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Free(absent) Write() calls = %d, want %d", got, want)
	}
}

func TestServiceDependRejectsCycleInActiveGraphWithoutWriting(t *testing.T) {
	a := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "a", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1", Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7E2"}})
	b := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "b", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1", Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7E3"}})
	c := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "c", Status: StatusReady, Priority: PriorityHigh, Rank: "3/1"})
	store := newMemoryTaskStore(a, b, c)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, err := service.Depend(context.Background(), c.State.TaskID, a.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Depend(cycle) category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Depend(cycle) Write() calls = %d, want %d", got, want)
	}
}

func TestServiceDependRejectsExistingReachableCycleWithoutWriting(t *testing.T) {
	a := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "a", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	b := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "b", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1", Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7E3"}})
	c := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "c", Status: StatusReady, Priority: PriorityHigh, Rank: "3/1", Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7E2"}})
	store := newMemoryTaskStore(a, b, c)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, err := service.Depend(context.Background(), a.State.TaskID, b.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Depend(existing cycle) category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Depend(existing cycle) Write() calls = %d, want %d", got, want)
	}
}

func serviceUnderTest(store TaskStore, ids IDSource) Service {
	return Service{Config: serviceTestConfig, Store: store, IDs: ids, Now: func() time.Time { return serviceTestNow }, Actor: "developer@example.com"}
}

func serviceSnapshot(id string, task TaskData) Snapshot {
	if task.Labels == nil {
		task.Labels = []string{}
	}
	if task.Dependencies == nil {
		task.Dependencies = []string{}
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = serviceTestNow.Add(-time.Minute)
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	state := StateDocument{
		Format: stateDocumentFormat, Version: documentVersion, ProjectID: serviceTestConfig.ProjectID, TaskID: id,
		History: History{Generation: "01K0M6B8A4FTT8C39MXXYTW7D9"}, LogicalClock: 1, Task: task,
	}
	return Snapshot{Head: "existing-" + id, State: state, Operation: OperationPack{TaskID: id}}
}

type sequenceIDSource struct {
	values []string
	calls  int
}

func (s *sequenceIDSource) New() (string, error) {
	if s.calls >= len(s.values) {
		return "", fmt.Errorf("unexpected ID request %d", s.calls+1)
	}
	id := s.values[s.calls]
	s.calls++
	return id, nil
}

type memoryTaskStore struct {
	snapshots map[string]Snapshot
	writes    []memoryWrite
	writeErr  error
	listCalls int
}

type memoryWrite struct {
	parent *Snapshot
	pack   OperationPack
	state  StateDocument
}

func newMemoryTaskStore(snapshots ...Snapshot) *memoryTaskStore {
	store := &memoryTaskStore{snapshots: make(map[string]Snapshot, len(snapshots))}
	for _, snapshot := range snapshots {
		store.snapshots[snapshot.State.TaskID] = snapshot
	}
	return store
}

func (s *memoryTaskStore) List(_ context.Context, _ ProjectConfig) ([]Snapshot, error) {
	s.listCalls++
	ids := make([]string, 0, len(s.snapshots))
	for id := range s.snapshots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	snapshots := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		snapshots = append(snapshots, s.snapshots[id])
	}
	return snapshots, nil
}

func (s *memoryTaskStore) Get(_ context.Context, _ ProjectConfig, id string) (Snapshot, error) {
	snapshot, ok := s.snapshots[id]
	if !ok {
		return Snapshot{}, Errorf(CategoryNotFound, "task %q was not found", id)
	}
	return snapshot, nil
}

func (s *memoryTaskStore) Resolve(ctx context.Context, config ProjectConfig, prefix string) (string, error) {
	snapshots, err := s.List(ctx, config)
	if err != nil {
		return "", err
	}
	needle := strings.ToLower(prefix)
	matches := make([]string, 0, 1)
	for _, snapshot := range snapshots {
		if strings.HasPrefix(strings.ToLower(snapshot.State.TaskID), needle) {
			matches = append(matches, snapshot.State.TaskID)
		}
	}
	switch len(matches) {
	case 0:
		return "", Errorf(CategoryNotFound, "no task matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", Errorf(CategoryValidation, "task ID prefix %q is ambiguous", prefix)
	}
}

func (s *memoryTaskStore) Write(_ context.Context, _ ProjectConfig, parent *Snapshot, pack OperationPack, state StateDocument, _ string) (Snapshot, error) {
	s.writes = append(s.writes, memoryWrite{parent: parent, pack: pack, state: state})
	if s.writeErr != nil {
		return Snapshot{}, s.writeErr
	}
	snapshot := Snapshot{Head: fmt.Sprintf("head-%d", len(s.writes)), Operation: pack, State: state}
	s.snapshots[state.TaskID] = snapshot
	return snapshot, nil
}

func assertTaskIDs(t *testing.T, tasks []Task, want []string) {
	t.Helper()
	got := make([]string, len(tasks))
	for i, task := range tasks {
		got[i] = task.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
}

func assertOperations(t *testing.T, got, want []Operation) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %#v, want %#v", got, want)
	}
}
