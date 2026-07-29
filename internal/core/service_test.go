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

	result, err := service.CreateMutation(context.Background(), CreateInput{Title: "  Build service  "})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	task := result.Task

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
	if got, want := write.reason, "workbook: create WB-01K0M6B8 Build service"; got != want {
		t.Fatalf("Create() write reason = %q, want %q", got, want)
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

	_, err := service.CreateMutation(context.Background(), CreateInput{Title: "Task"})
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

func TestServiceListOrdersWorkflowStatuses(t *testing.T) {
	backlog := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "backlog", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	ready := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{Title: "ready", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"})
	blocked := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D3", TaskData{Title: "blocked", Status: StatusBlocked, Priority: PriorityMedium, Rank: "1/1"})
	inProgress := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D4", TaskData{Title: "in progress", Status: StatusInProgress, Priority: PriorityMedium, Rank: "1/1"})
	inReview := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D5", TaskData{Title: "in review", Status: StatusInReview, Priority: PriorityMedium, Rank: "1/1"})
	done := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D6", TaskData{Title: "done", Status: StatusDone, Priority: PriorityMedium, Rank: "1/1"})
	service := serviceUnderTest(newMemoryTaskStore(done, inProgress, ready, inReview, backlog, blocked), &sequenceIDSource{})

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	assertTaskIDs(t, tasks, []string{
		backlog.State.TaskID,
		ready.State.TaskID,
		blocked.State.TaskID,
		inProgress.State.TaskID,
		inReview.State.TaskID,
		done.State.TaskID,
	})
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

	result, err := service.UpdateMutation(context.Background(), snapshot.State.TaskID[:10], UpdateInput{
		Title: &title, Description: &description, Status: &status, Priority: &priority, Labels: &labels,
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	task := result.Task
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
	if got, want := write.reason, "workbook: update WB-01K0M6B8 title New title; description updated; status backlog → ready; priority medium → high; labels -zeta; labels +beta"; got != want {
		t.Fatalf("Update() write reason = %q, want %q", got, want)
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

func TestServiceUpdateCommitSubjectNormalizesAndTruncatesTitles(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "collapses whitespace",
			title: "  New\t title\nwith  spacing ",
			want:  "workbook: update WB-01K0M6B8 title New title with spacing",
		},
		{
			name:  "truncates titles by runes",
			title: strings.Repeat("界", 73),
			want:  "workbook: update WB-01K0M6B8 title " + strings.Repeat("界", 71) + "…",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
				Title: "Old", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
			})
			store := newMemoryTaskStore(snapshot)
			service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"}})

			_, err := service.UpdateMutation(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &test.title})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got := store.writes[0].reason; got != test.want {
				t.Fatalf("Update() write reason = %q, want %q", got, test.want)
			}
		})
	}
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

	_, err := service.UpdateMutation(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title, Labels: &labels})
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

func TestServiceDeleteTombstonesTaskAndRestoreIsTheOnlyAllowedMutation(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "Delete me", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3"}})

	deleteResult, err := service.DeleteMutation(context.Background(), snapshot.State.TaskID)
	if err != nil {
		t.Fatalf("DeleteMutation() error = %v", err)
	}
	task := deleteResult.Task
	if !task.Deleted {
		t.Fatal("Delete() task is not tombstoned")
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7D2", Type: OperationTaskTombstone}})

	title := "cannot update"
	_, err = service.UpdateMutation(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Update(tombstone) error category = %q, want %q (error: %v)", got, want, err)
	}
	_, err = service.DeleteMutation(context.Background(), snapshot.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Delete(tombstone) error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls after tombstone = %d, want %d", got, want)
	}

	restoreResult, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID)
	if err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	task = restoreResult.Task
	if task.Deleted {
		t.Fatal("Restore() task is still tombstoned")
	}
	assertOperations(t, store.writes[1].pack.Operations, []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7D3", Type: OperationTaskRestore}})

	_, err = service.RestoreMutation(context.Background(), snapshot.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Restore(active) error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 2; got != want {
		t.Fatalf("Write() calls after active restore = %d, want %d", got, want)
	}
}

func TestServicePropagatesStaleWriteWithoutRetry(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{Title: "Race", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	store := newMemoryTaskStore(snapshot)
	store.writeErr = Errorf(CategoryStaleWrite, "task ref changed concurrently")
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"}})
	title := "Race resolved"

	_, err := service.UpdateMutation(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title})
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

			_, err := service.CreateMutation(context.Background(), CreateInput{Title: "Reject invalid IDs"})
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

			_, err := service.UpdateMutation(context.Background(), snapshot.State.TaskID, UpdateInput{Title: &title, Description: &description})
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

	_, err := service.DeleteMutation(context.Background(), snapshot.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Delete() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
}

func TestServicePlaceMovesAcrossStatusAndRankInOneWrite(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "9/1",
	})
	previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
		Title: "previous", Status: StatusInProgress, Priority: PriorityMedium, Rank: "2/1",
	})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
		Title: "anchor", Status: StatusInProgress, Priority: PriorityMedium, Rank: "4/1",
	})
	store := newMemoryTaskStore(moved, previous, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E4",
		"01K0M6B8A4FTT8C39MXXYTW7E5",
	}})

	result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{
		Status: StatusInProgress,
		Before: anchor.State.TaskID,
	})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if got, want := result.Task.Status, StatusInProgress; got != want {
		t.Fatalf("PlaceMutation() status = %q, want %q", got, want)
	}
	if got, want := result.Task.Rank, "3/1"; got != want {
		t.Fatalf("PlaceMutation() rank = %q, want %q", got, want)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
	}
	if got, want := store.writes[0].parent.State.TaskID, moved.State.TaskID; got != want {
		t.Fatalf("PlaceMutation() wrote task %q, want %q", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E4", Type: OperationFieldSet, Field: "status", Value: "in-progress"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E5", Type: OperationFieldSet, Field: "rank", Value: "3/1"},
	})
}

func TestServicePlaceWithoutAnchorMovesIntoEmptyPriorityBucket(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "7/1",
	})
	otherPriority := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "high", Status: StatusDone, Priority: PriorityHigh, Rank: "1/1",
	})
	store := newMemoryTaskStore(moved, otherPriority)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7F3",
	}})

	result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{Status: StatusDone})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if result.Task.Status != StatusDone || result.Task.Rank != "7/1" {
		t.Fatalf("PlaceMutation() task = %#v, want done with unchanged rank", result.Task)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{{
		ID: "01K0M6B8A4FTT8C39MXXYTW7F3", Type: OperationFieldSet, Field: "status", Value: "done",
	}})
}

func TestServicePlacePlacesSameStatusTaskAtBucketBoundaries(t *testing.T) {
	first := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "first", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1",
	})
	last := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "last", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1",
	})
	before := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F3", TaskData{
		Title: "before", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1",
	})
	after := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F4", TaskData{
		Title: "after", Status: StatusReady, Priority: PriorityHigh, Rank: "10/1",
	})
	store := newMemoryTaskStore(first, last, before, after)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7F5",
		"01K0M6B8A4FTT8C39MXXYTW7F6",
	}})

	beforeResult, err := service.PlaceMutation(context.Background(), before.State.TaskID, PlaceInput{
		Status: StatusReady,
		Before: first.State.TaskID,
	})
	if err != nil {
		t.Fatalf("PlaceMutation(before boundary) error = %v", err)
	}
	if got, want := beforeResult.Task.Rank, "1/1"; got != want {
		t.Fatalf("PlaceMutation(before boundary) rank = %q, want %q", got, want)
	}

	afterResult, err := service.PlaceMutation(context.Background(), after.State.TaskID, PlaceInput{
		Status: StatusReady,
		After:  last.State.TaskID,
	})
	if err != nil {
		t.Fatalf("PlaceMutation(after boundary) error = %v", err)
	}
	if got, want := afterResult.Task.Rank, "5/1"; got != want {
		t.Fatalf("PlaceMutation(after boundary) rank = %q, want %q", got, want)
	}
}

func TestServicePlaceWithoutAnchorReturnsExistingSoleBucketTask(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "7/1",
	})
	store := newMemoryTaskStore(moved)
	ids := &sequenceIDSource{}
	service := serviceUnderTest(store, ids)

	result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{Status: StatusReady})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if got, want := result.Task, Project(moved); !reflect.DeepEqual(got, want) {
		t.Fatalf("PlaceMutation() task = %#v, want existing %#v", got, want)
	}
	if got, want := ids.calls, 0; got != want {
		t.Fatalf("PlaceMutation() ID requests = %d, want %d", got, want)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
	}
}

func TestServicePlaceRejectsBothAnchorDirectionsWithoutWriting(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1",
	})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "anchor", Status: StatusReady, Priority: PriorityMedium, Rank: "2/1",
	})
	store := newMemoryTaskStore(moved, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{
		Status: StatusReady,
		Before: anchor.State.TaskID,
		After:  anchor.State.TaskID,
	})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("PlaceMutation() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
	}
}

func TestServicePlaceRejectsAnchorWithDifferentPriorityWithoutWriting(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "moved", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1",
	})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1",
	})
	store := newMemoryTaskStore(moved, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, err := service.PlaceMutation(context.Background(), moved.State.TaskID, PlaceInput{
		Status: StatusReady,
		Before: anchor.State.TaskID,
	})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("PlaceMutation() error category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
	}
}

func TestServicePlaceRejectsUnrepresentableTiedRankGapsWithoutWriting(t *testing.T) {
	tests := []struct {
		name  string
		moved Snapshot
		left  Snapshot
		right Snapshot
		input PlaceInput
	}{
		{
			name: "before anchor",
			moved: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E4", TaskData{
				Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1",
			}),
			left: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
				Title: "left", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			}),
			right: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
				Title: "anchor", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			}),
			input: PlaceInput{Status: StatusInProgress, Before: "WB-01K0M6B8A4FTT8C39MXXYTW7E3"},
		},
		{
			name: "after anchor",
			moved: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
				Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1",
			}),
			left: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
				Title: "anchor", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			}),
			right: serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
				Title: "right", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			}),
			input: PlaceInput{Status: StatusInProgress, After: "WB-01K0M6B8A4FTT8C39MXXYTW7E2"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryTaskStore(test.moved, test.left, test.right)
			ids := &sequenceIDSource{values: []string{
				"01K0M6B8A4FTT8C39MXXYTW7E5",
				"01K0M6B8A4FTT8C39MXXYTW7E6",
			}}
			service := serviceUnderTest(store, ids)

			_, err := service.PlaceMutation(context.Background(), test.moved.State.TaskID, test.input)
			if got, want := CategoryOf(err), CategoryStaleWrite; got != want {
				t.Errorf("PlaceMutation() error category = %q, want %q (error: %v)", got, want, err)
			}
			if got, want := len(store.writes), 0; got != want {
				t.Errorf("PlaceMutation() writes = %d, want %d", got, want)
			}
			if got, want := ids.calls, 0; got != want {
				t.Errorf("PlaceMutation() ID requests = %d, want %d", got, want)
			}
		})
	}
}

func TestServicePlaceRepresentsTiedRankGapWhenMovedIDSortsBetweenAnchors(t *testing.T) {
	tests := []struct {
		name  string
		input PlaceInput
	}{
		{
			name:  "before anchor",
			input: PlaceInput{Status: StatusInProgress, Before: "WB-01K0M6B8A4FTT8C39MXXYTW7E4"},
		},
		{
			name:  "after anchor",
			input: PlaceInput{Status: StatusInProgress, After: "WB-01K0M6B8A4FTT8C39MXXYTW7E2"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
				Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1",
			})
			left := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
				Title: "left", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			})
			right := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E4", TaskData{
				Title: "right", Status: StatusInProgress, Priority: PriorityHigh, Rank: "2/1",
			})
			store := newMemoryTaskStore(moved, left, right)
			service := serviceUnderTest(store, &sequenceIDSource{values: []string{
				"01K0M6B8A4FTT8C39MXXYTW7E5",
				"01K0M6B8A4FTT8C39MXXYTW7E6",
			}})

			result, err := service.PlaceMutation(context.Background(), moved.State.TaskID, test.input)
			if err != nil {
				t.Fatalf("PlaceMutation() error = %v", err)
			}
			if got, want := result.Task.Rank, "2/1"; got != want {
				t.Fatalf("PlaceMutation() rank = %q, want %q", got, want)
			}
			if got, want := len(store.writes), 1; got != want {
				t.Fatalf("PlaceMutation() writes = %d, want %d", got, want)
			}
			if got, want := store.writes[0].parent.State.TaskID, moved.State.TaskID; got != want {
				t.Fatalf("PlaceMutation() wrote task %q, want %q", got, want)
			}
			assertOperations(t, store.writes[0].pack.Operations, []Operation{
				{ID: "01K0M6B8A4FTT8C39MXXYTW7E5", Type: OperationFieldSet, Field: "status", Value: "in-progress"},
				{ID: "01K0M6B8A4FTT8C39MXXYTW7E6", Type: OperationFieldSet, Field: "rank", Value: "2/1"},
			})
		})
	}
}

func TestServiceMovePlacesTaskBetweenAnchorAndNeighborWithoutWritingAnotherTask(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1"})
	previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "previous", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	store := newMemoryTaskStore(moved, previous, anchor)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E4"}})

	result, err := service.MoveMutation(context.Background(), moved.State.TaskID, MoveInput{Before: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation() error = %v", err)
	}
	task := result.Task
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

	result, err := service.MoveMutation(context.Background(), moved.State.TaskID, MoveInput{Before: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation() error = %v", err)
	}
	task := result.Task
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

	beforeResult, err := service.MoveMutation(context.Background(), before.State.TaskID, MoveInput{Before: first.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation(before boundary) error = %v", err)
	}
	gotBefore := beforeResult.Task
	if got, want := gotBefore.Rank, "1/1"; got != want {
		t.Fatalf("Move(before boundary) rank = %q, want %q", got, want)
	}
	afterResult, err := service.MoveMutation(context.Background(), after.State.TaskID, MoveInput{After: last.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation(after boundary) error = %v", err)
	}
	gotAfter := afterResult.Task
	if got, want := gotAfter.Rank, "5/1"; got != want {
		t.Fatalf("Move(after boundary) rank = %q, want %q", got, want)
	}
}

func TestServiceMovePlacesTaskAfterAnchorBeforeFollowingRank(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1"})
	following := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{Title: "following", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1"})
	service := serviceUnderTest(newMemoryTaskStore(moved, anchor, following), &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E4"}})

	result, err := service.MoveMutation(context.Background(), moved.State.TaskID, MoveInput{After: anchor.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation() error = %v", err)
	}
	task := result.Task
	if got, want := task.Rank, "3/1"; got != want {
		t.Fatalf("Move() rank = %q, want %q", got, want)
	}
}

func TestServiceMoveAfterFractionalLastRankUsesNextInteger(t *testing.T) {
	moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{Title: "moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1"})
	last := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{Title: "last", Status: StatusReady, Priority: PriorityHigh, Rank: "3/2"})
	service := serviceUnderTest(newMemoryTaskStore(moved, last), &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"}})

	result, err := service.MoveMutation(context.Background(), moved.State.TaskID, MoveInput{After: last.State.TaskID})
	if err != nil {
		t.Fatalf("MoveMutation() error = %v", err)
	}
	task := result.Task
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
		_, err := service.MoveMutation(context.Background(), moved.State.TaskID, input)
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

	result, err := service.DependMutation(context.Background(), dependent.State.TaskID, dependency.State.TaskID)
	if err != nil {
		t.Fatalf("DependMutation() error = %v", err)
	}
	task := result.Task
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
		_, err := service.DependMutation(context.Background(), active.State.TaskID, dependency)
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

	result, err := service.FreeMutation(context.Background(), dependent.State.TaskID, dependency)
	if err != nil {
		t.Fatalf("FreeMutation() error = %v", err)
	}
	task := result.Task
	if got, want := task.Dependencies, []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Free() dependencies = %#v, want %#v", got, want)
	}
	_, err = service.FreeMutation(context.Background(), dependent.State.TaskID, dependency)
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

	_, err := service.DependMutation(context.Background(), c.State.TaskID, a.State.TaskID)
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

	_, err := service.DependMutation(context.Background(), a.State.TaskID, b.State.TaskID)
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("Depend(existing cycle) category = %q, want %q (error: %v)", got, want, err)
	}
	if got, want := len(store.writes), 0; got != want {
		t.Fatalf("Depend(existing cycle) Write() calls = %d, want %d", got, want)
	}
}

func TestServiceFullIDUpdateReadsParentDirectlyAndAdvancesFromItsHead(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	reader := newTaskReaderSpy(parent)
	writer := &canonicalWriterSpy{}
	projection := &projectionUpdaterSpy{}
	service := splitServiceUnderTest(reader, writer, projection, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})
	title := "New title"

	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if got, want := result.Task.Head, "written-head"; got != want {
		t.Fatalf("UpdateMutation() head = %q, want %q", got, want)
	}
	if got := reader.resolveInputs; len(got) != 0 {
		t.Fatalf("Resolve() inputs = %#v, want none for a full task ID", got)
	}
	if got, want := reader.getIDs, []string{parent.State.TaskID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() IDs = %#v, want %#v", got, want)
	}
	if got, want := len(writer.calls), 1; got != want {
		t.Fatalf("WriteValidated() calls = %d, want %d", got, want)
	}
	if got, want := writer.calls[0].parent, &parent; !reflect.DeepEqual(got, want) {
		t.Fatalf("WriteValidated() parent = %#v, want %#v", got, want)
	}
	if got, want := len(projection.advanceCalls), 1; got != want {
		t.Fatalf("Advance() calls = %d, want %d", got, want)
	}
	if got, want := projection.advanceCalls[0].expectedParent, parent.Head; got != want {
		t.Fatalf("Advance() expected parent = %q, want %q", got, want)
	}
	if got, want := projection.advanceCalls[0].snapshot.Head, "written-head"; got != want {
		t.Fatalf("Advance() written head = %q, want %q", got, want)
	}
}

func TestServicePrefixUpdateResolvesThenReadsCanonicalTask(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	reader := newTaskReaderSpy(parent)
	reader.resolveID = parent.State.TaskID
	writer := &canonicalWriterSpy{}
	service := splitServiceUnderTest(reader, writer, nil, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})
	prefix := parent.State.TaskID[:10]
	title := "New title"

	result, err := service.UpdateMutation(context.Background(), prefix, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if got, want := result.Task.ID, parent.State.TaskID; got != want {
		t.Fatalf("UpdateMutation() task ID = %q, want %q", got, want)
	}
	if got, want := reader.resolveInputs, []string{prefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve() inputs = %#v, want %#v", got, want)
	}
	if got, want := reader.getIDs, []string{parent.State.TaskID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() IDs = %#v, want %#v", got, want)
	}
}

func TestServiceMutationBoundariesKeepRequiredGlobalListReads(t *testing.T) {
	t.Run("create rank", func(t *testing.T) {
		existing := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
			Title: "Existing", Status: StatusBacklog, Priority: PriorityMedium, Rank: "7/1",
		})
		reader := newTaskReaderSpy(existing)
		projection := &projectionUpdaterSpy{}
		service := splitServiceUnderTest(reader, &canonicalWriterSpy{}, projection, &sequenceIDSource{values: []string{
			"01K0M6B8A4FTT8C39MXXYTW7D2",
			"01K0M6B8A4FTT8C39MXXYTW7D3",
			"01K0M6B8A4FTT8C39MXXYTW7D4",
		}})

		result, err := service.CreateMutation(context.Background(), CreateInput{Title: "New task"})
		if err != nil {
			t.Fatalf("CreateMutation() error = %v", err)
		}
		if got, want := result.Task.Rank, "8/1"; got != want {
			t.Fatalf("CreateMutation() rank = %q, want %q", got, want)
		}
		if got, want := reader.listCalls, 1; got != want {
			t.Fatalf("CreateMutation() List() calls = %d, want %d", got, want)
		}
		if got, want := len(projection.advanceCalls), 1; got != want {
			t.Fatalf("CreateMutation() Advance() calls = %d, want %d", got, want)
		}
		if got := projection.advanceCalls[0].expectedParent; got != "" {
			t.Fatalf("CreateMutation() Advance() expected parent = %q, want empty root parent", got)
		}
	})

	t.Run("move rank", func(t *testing.T) {
		moved := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
			Title: "Moved", Status: StatusReady, Priority: PriorityHigh, Rank: "9/1",
		})
		previous := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
			Title: "Previous", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1",
		})
		anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
			Title: "Anchor", Status: StatusReady, Priority: PriorityHigh, Rank: "4/1",
		})
		reader := newTaskReaderSpy(moved, previous, anchor)
		service := splitServiceUnderTest(reader, &canonicalWriterSpy{}, nil, &sequenceIDSource{
			values: []string{"01K0M6B8A4FTT8C39MXXYTW7E4"},
		})

		result, err := service.MoveMutation(context.Background(), moved.State.TaskID, MoveInput{Before: anchor.State.TaskID})
		if err != nil {
			t.Fatalf("MoveMutation() error = %v", err)
		}
		if got, want := result.Task.Rank, "3/1"; got != want {
			t.Fatalf("MoveMutation() rank = %q, want %q", got, want)
		}
		if got, want := reader.listCalls, 1; got != want {
			t.Fatalf("MoveMutation() List() calls = %d, want %d", got, want)
		}
	})

	t.Run("dependency cycle", func(t *testing.T) {
		dependent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E1", TaskData{
			Title: "Dependent", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1",
		})
		dependency := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
			Title: "Dependency", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1",
		})
		reader := newTaskReaderSpy(dependent, dependency)
		service := splitServiceUnderTest(reader, &canonicalWriterSpy{}, nil, &sequenceIDSource{
			values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3"},
		})

		result, err := service.DependMutation(context.Background(), dependent.State.TaskID, dependency.State.TaskID)
		if err != nil {
			t.Fatalf("DependMutation() error = %v", err)
		}
		if got, want := result.Task.Dependencies, []string{dependency.State.TaskID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("DependMutation() dependencies = %#v, want %#v", got, want)
		}
		if got, want := reader.listCalls, 1; got != want {
			t.Fatalf("DependMutation() List() calls = %d, want %d", got, want)
		}
	})
}

func TestServiceProjectionFailureReturnsDurableMutationWarning(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	title := "New title"
	written := parent
	written.Head = "durable-written-head"
	written.State.Task.Title = title
	written.State.LogicalClock = 2
	reader := newTaskReaderSpy(parent)
	writer := &canonicalWriterSpy{written: written}
	projection := &projectionUpdaterSpy{advanceErr: errors.New("disk full")}
	service := splitServiceUnderTest(reader, writer, projection, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})

	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if got, want := result.Task.Head, written.Head; got != want {
		t.Fatalf("task head = %q, want durable Git head %q", got, want)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningProjectionUpdate {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if !strings.Contains(result.Warnings[0].Message, "run `workbook rebuild`") ||
		!strings.Contains(result.Warnings[0].Message, "disk full") {
		t.Fatalf("warning message = %q, want rebuild guidance and projection error", result.Warnings[0].Message)
	}
	if got, want := len(projection.invalidateCalls), 1; got != want {
		t.Fatalf("Invalidate() calls = %d, want %d", got, want)
	}
	invalidation := projection.invalidateCalls[0]
	if invalidation.taskID != parent.State.TaskID ||
		invalidation.expectedParent != parent.Head ||
		invalidation.writtenHead != written.Head {
		t.Fatalf("invalidation = %#v", invalidation)
	}
}

func TestServiceProjectionFailureInvalidatesWhenConditionalAdvanceIsDeclined(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	title := "New title"
	written := parent
	written.Head = "durable-written-head"
	written.State.Task.Title = title
	written.State.LogicalClock = 2
	advanced := false
	projection := &projectionUpdaterSpy{advanceResult: &advanced}
	service := splitServiceUnderTest(newTaskReaderSpy(parent), &canonicalWriterSpy{written: written}, projection, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})

	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if got, want := result.Task.Head, written.Head; got != want {
		t.Fatalf("task head = %q, want durable Git head %q", got, want)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningProjectionUpdate {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if got := result.Warnings[0].Message; !strings.Contains(got, "run `workbook rebuild`") {
		t.Fatalf("warning message = %q, want actionable rebuild guidance", got)
	}
	if got, want := len(projection.invalidateCalls), 1; got != want {
		t.Fatalf("Invalidate() calls = %d, want %d", got, want)
	}
	invalidation := projection.invalidateCalls[0]
	if invalidation.taskID != parent.State.TaskID ||
		invalidation.expectedParent != parent.Head ||
		invalidation.writtenHead != written.Head {
		t.Fatalf("invalidation = %#v", invalidation)
	}
}

func TestServiceProjectionFailureAppendsInvalidationFailureToOneWarning(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	title := "New title"
	writer := &canonicalWriterSpy{}
	projection := &projectionUpdaterSpy{
		advanceErr:    errors.New("disk full"),
		invalidateErr: errors.New("database locked"),
	}
	service := splitServiceUnderTest(newTaskReaderSpy(parent), writer, projection, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})

	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if got, want := len(result.Warnings), 1; got != want {
		t.Fatalf("warnings = %#v, want %d", result.Warnings, want)
	}
	if got := result.Warnings[0].Message; !strings.Contains(got, "; cache invalidation also failed: database locked") {
		t.Fatalf("warning message = %q, want invalidation failure detail", got)
	}
}

func TestServiceProjectionFailureDoesNotAdvanceAfterGitWriteFailure(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Old title", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	title := "New title"
	writeErr := errors.New("reference changed")
	writer := &canonicalWriterSpy{err: writeErr}
	projection := &projectionUpdaterSpy{}
	service := splitServiceUnderTest(newTaskReaderSpy(parent), writer, projection, &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7D2"},
	})

	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if !errors.Is(err, writeErr) {
		t.Fatalf("UpdateMutation() error = %v, want %v", err, writeErr)
	}
	if got, want := result, (MutationResult{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateMutation() result = %#v, want %#v", got, want)
	}
	if got := len(projection.advanceCalls); got != 0 {
		t.Fatalf("Advance() calls = %d, want none", got)
	}
	if got := len(projection.invalidateCalls); got != 0 {
		t.Fatalf("Invalidate() calls = %d, want none", got)
	}
}

func serviceUnderTest(store *memoryTaskStore, ids IDSource) Service {
	return Service{
		Config: serviceTestConfig,
		Reader: store,
		Writer: store,
		IDs:    ids,
		Now:    func() time.Time { return serviceTestNow },
		Actor:  "developer@example.com",
	}
}

func splitServiceUnderTest(reader TaskReader, writer CanonicalTaskWriter, projection ProjectionUpdater, ids IDSource) Service {
	return Service{
		Config: serviceTestConfig, Reader: reader, Writer: writer, Projection: projection,
		IDs: ids, Now: func() time.Time { return serviceTestNow }, Actor: "developer@example.com",
	}
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
	reason string
}

type taskReaderSpy struct {
	snapshots     map[string]Snapshot
	resolveID     string
	listCalls     int
	getIDs        []string
	resolveInputs []string
}

func newTaskReaderSpy(snapshots ...Snapshot) *taskReaderSpy {
	reader := &taskReaderSpy{snapshots: make(map[string]Snapshot, len(snapshots))}
	for _, snapshot := range snapshots {
		reader.snapshots[snapshot.State.TaskID] = snapshot
	}
	return reader
}

func (s *taskReaderSpy) List(_ context.Context, _ ProjectConfig) ([]Snapshot, error) {
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

func (s *taskReaderSpy) Get(_ context.Context, _ ProjectConfig, id string) (Snapshot, error) {
	s.getIDs = append(s.getIDs, id)
	snapshot, ok := s.snapshots[id]
	if !ok {
		return Snapshot{}, Errorf(CategoryNotFound, "task %q was not found", id)
	}
	return snapshot, nil
}

func (s *taskReaderSpy) Resolve(_ context.Context, _ ProjectConfig, input string) (string, error) {
	s.resolveInputs = append(s.resolveInputs, input)
	if s.resolveID == "" {
		return "", Errorf(CategoryNotFound, "no task matches %q", input)
	}
	return s.resolveID, nil
}

type canonicalWrite struct {
	parent *Snapshot
	pack   OperationPack
	state  StateDocument
	reason string
}

type canonicalWriterSpy struct {
	calls   []canonicalWrite
	written Snapshot
	err     error
}

func (s *canonicalWriterSpy) WriteValidated(
	_ context.Context,
	_ ProjectConfig,
	parent *Snapshot,
	pack OperationPack,
	state StateDocument,
	reason string,
) (Snapshot, error) {
	s.calls = append(s.calls, canonicalWrite{parent: parent, pack: pack, state: state, reason: reason})
	if s.err != nil {
		return Snapshot{}, s.err
	}
	if s.written.Head != "" {
		return s.written, nil
	}
	return Snapshot{Head: "written-head", Operation: pack, State: state}, nil
}

type projectionAdvanceCall struct {
	expectedParent string
	snapshot       Snapshot
}

type projectionInvalidateCall struct {
	taskID         string
	expectedParent string
	writtenHead    string
}

type projectionUpdaterSpy struct {
	advanceCalls    []projectionAdvanceCall
	invalidateCalls []projectionInvalidateCall
	advanceResult   *bool
	advanceErr      error
	invalidateErr   error
}

func (s *projectionUpdaterSpy) Advance(_ context.Context, _ ProjectConfig, expectedParent string, snapshot Snapshot) (bool, error) {
	s.advanceCalls = append(s.advanceCalls, projectionAdvanceCall{expectedParent: expectedParent, snapshot: snapshot})
	if s.advanceResult != nil {
		return *s.advanceResult, s.advanceErr
	}
	return s.advanceErr == nil, s.advanceErr
}

func (s *projectionUpdaterSpy) Invalidate(_ context.Context, _ ProjectConfig, taskID, expectedParent, writtenHead string) error {
	s.invalidateCalls = append(s.invalidateCalls, projectionInvalidateCall{
		taskID: taskID, expectedParent: expectedParent, writtenHead: writtenHead,
	})
	return s.invalidateErr
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

func (s *memoryTaskStore) WriteValidated(
	_ context.Context,
	_ ProjectConfig,
	parent *Snapshot,
	pack OperationPack,
	state StateDocument,
	reason string,
) (Snapshot, error) {
	s.writes = append(s.writes, memoryWrite{parent: parent, pack: pack, state: state, reason: reason})
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
