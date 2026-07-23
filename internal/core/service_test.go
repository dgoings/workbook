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
