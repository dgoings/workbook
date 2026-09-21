package core

import (
	"context"
	"strings"
	"testing"
)

// The fold asks about a task ID's shape and never about its key, so a clone
// that has not fetched the configuration ledger still reads a teammate's task
// minted under a key it has never heard of. Ownership is asked at the
// boundaries where a name enters — a ref listing, a remote listing, an ID
// somebody typed — and a KeySet answers it there.
func TestApplyFoldsAPackUnderAnyKeyAndAnyDependencyKey(t *testing.T) {
	pack := NewOperationPack(
		projectID, "NEW-01K0M6B8A4FTT8C39MXXYTW7C1", generationID, "a@example.test", 1, createdAt,
		[]Operation{{ID: operationID1, Type: OperationTaskCreate, Task: &TaskData{
			Title: "Foreign key", Status: StatusBacklog, Priority: PriorityMedium,
			Labels: []string{}, Rank: "1/1",
			Dependencies: []string{"OLD-01K0M6B8A4FTT8C39MXXYTW7C2"},
			CreatedAt:    createdAt, UpdatedAt: createdAt,
		}}},
	)

	state, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if state.TaskID != "NEW-01K0M6B8A4FTT8C39MXXYTW7C1" {
		t.Fatalf("Apply() task ID = %q, want the pack's own", state.TaskID)
	}
	want := []string{"OLD-01K0M6B8A4FTT8C39MXXYTW7C2"}
	if len(state.Task.Dependencies) != 1 || state.Task.Dependencies[0] != want[0] {
		t.Fatalf("Apply() dependencies = %#v, want %#v", state.Task.Dependencies, want)
	}

	// The same is true of a dependency added later, through set.add, which is
	// the operation a cross-key `workbook block` authors.
	added := NewOperationPack(
		projectID, state.TaskID, generationID, "a@example.test", 2, updatedAt,
		[]Operation{{
			ID: operationID2, Type: OperationSetAdd, Field: "dependencies",
			Value: "OTHER-01K0M6B8A4FTT8C39MXXYTW7C3",
		}},
	)
	next, err := Apply(&state, added)
	if err != nil {
		t.Fatalf("Apply(set.add across keys) error = %v, want nil", err)
	}
	if len(next.Task.Dependencies) != 2 {
		t.Fatalf("Apply(set.add) dependencies = %#v, want two", next.Task.Dependencies)
	}
}

// Shape is still a rule: a lowercase key, a body that is not a canonical ULID,
// and a missing separator are corrupt data whatever the project's keys are.
func TestApplyStillRefusesATaskIDThatIsNotOneAtAll(t *testing.T) {
	for name, id := range map[string]string{
		"lowercase key":     "new-01K0M6B8A4FTT8C39MXXYTW7C1",
		"lowercase body":    "NEW-01k0m6b8a4ftt8c39mxxytw7c1",
		"short body":        "NEW-01K0M6B8A4FTT8C39MXXYTW7C",
		"missing separator": "NEW01K0M6B8A4FTT8C39MXXYTW7C1",
		"no body":           "NEW-",
		"eleven-letter key": "ELEVENCHARS-01K0M6B8A4FTT8C39MXXYTW7C1",
		"empty":             "",
	} {
		t.Run(name, func(t *testing.T) {
			pack := createPack()
			pack.TaskID = id
			assertCorrupt(t, applyError(nil, pack))
		})
	}

	// A dependency is judged by the same rule, in the fold and in the pack's
	// own validation.
	pack := createPack()
	pack.Operations[0].Task.Dependencies = []string{"not-a-task-id"}
	assertCorrupt(t, applyError(nil, pack))
}

// A Service whose key set names several keys mints under the current one, and
// under any other active one the caller asks for. The founding-key fallback is
// what every caller that never read the ledger gets, so the behavior of a
// Service built the way they all build one does not change.
func TestServiceMintsUnderTheCurrentKeyOrTheOneTheCallerNames(t *testing.T) {
	keys, err := NewKeySet(KeyDocument{
		Keys:    []KeyDefinition{{Key: "WB"}, {Key: "NEW"}, {Key: "OLD", Retired: true}},
		Current: "NEW",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}

	for _, test := range []struct {
		name  string
		keys  KeySet
		input string
		want  string
	}{
		{name: "the current key", keys: keys, want: "NEW-01K0M6B8A4FTT8C39MXXYTW7D2"},
		{name: "a key the caller named", keys: keys, input: "WB", want: "WB-01K0M6B8A4FTT8C39MXXYTW7D2"},
		{name: "the founding key when nothing was read", want: "WB-01K0M6B8A4FTT8C39MXXYTW7D2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := serviceUnderTest(newMemoryTaskStore(), &sequenceIDSource{values: []string{
				"01K0M6B8A4FTT8C39MXXYTW7D2",
				"01K0M6B8A4FTT8C39MXXYTW7D3",
				"01K0M6B8A4FTT8C39MXXYTW7D4",
			}})
			service.Keys = test.keys
			result, err := service.CreateMutation(
				context.Background(), CreateInput{Title: "Minted", Key: test.input})
			if err != nil {
				t.Fatalf("CreateMutation() error = %v", err)
			}
			if got := result.Task.ID; got != test.want {
				t.Fatalf("CreateMutation() task ID = %q, want %q", got, test.want)
			}
		})
	}

	// A retired key mints nothing, and an unknown key is not this project's.
	// Both refusals name the keys a task may be minted under.
	for _, key := range []string{"OLD", "NOPE"} {
		service := serviceUnderTest(newMemoryTaskStore(), &sequenceIDSource{values: []string{
			"01K0M6B8A4FTT8C39MXXYTW7D2",
		}})
		service.Keys = keys
		_, err := service.CreateMutation(context.Background(), CreateInput{Title: "Minted", Key: key})
		if err == nil || CategoryOf(err) != CategoryValidation {
			t.Fatalf("CreateMutation(key %q) = %v, want a validation error", key, err)
		}
		if !strings.Contains(err.Error(), "WB, NEW") {
			t.Fatalf("CreateMutation(key %q) = %q, want it to name the active keys", key, err)
		}
	}
}

// The key filter keeps the tasks whose ID carries one key, retired keys
// included, and refuses a key this project does not have rather than answering
// with an empty list.
func TestServiceListFiltersByKeyAndRefusesAnUnknownOne(t *testing.T) {
	keys, err := NewKeySet(KeyDocument{
		Keys:    []KeyDefinition{{Key: "WB"}, {Key: "OLD", Retired: true}},
		Current: "WB",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
			Title: "Ours", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
		}),
		serviceSnapshot("OLD-01K0M6B8A4FTT8C39MXXYTW7D2", TaskData{
			Title: "Retired key", Status: StatusBacklog, Priority: PriorityMedium, Rank: "2/1",
		}),
	)
	service := serviceUnderTest(store, &sequenceIDSource{})
	service.Keys = keys

	for key, want := range map[string]string{
		"WB":  "WB-01K0M6B8A4FTT8C39MXXYTW7D1",
		"OLD": "OLD-01K0M6B8A4FTT8C39MXXYTW7D2",
	} {
		tasks, err := service.List(context.Background(), ListFilter{Key: key})
		if err != nil {
			t.Fatalf("List(key %q) error = %v", key, err)
		}
		if len(tasks) != 1 || tasks[0].ID != want {
			t.Fatalf("List(key %q) = %#v, want only %q", key, tasks, want)
		}
	}
	if tasks, err := service.List(context.Background(), ListFilter{}); err != nil || len(tasks) != 2 {
		t.Fatalf("List() = (%d tasks, %v), want both", len(tasks), err)
	}

	_, err = service.List(context.Background(), ListFilter{Key: "NOPE"})
	if err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("List(unknown key) = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), "OLD (retired)") {
		t.Fatalf("List(unknown key) = %q, want it to name every key this project has", err)
	}
}

// An ID under any of the project's keys is read exactly rather than resolved as
// a prefix, and the mutation minted beside it reserves the task's ULID body.
//
// The reservation is what keeps an operation ID from colliding with the task's
// own identifier. It used to be produced by trimming this project's key off the
// ID, which under a second key trimmed nothing and reserved the whole name —
// reserving a string no generated ULID can equal, which is to say nothing.
func TestServiceReadsAndReservesAnIDUnderASecondKey(t *testing.T) {
	keys, err := NewKeySet(KeyDocument{
		Keys:    []KeyDefinition{{Key: "WB"}, {Key: "NEW"}},
		Current: "NEW",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	const id = "NEW-01K0M6B8A4FTT8C39MXXYTW7D1"
	const body = "01K0M6B8A4FTT8C39MXXYTW7D1"
	title := "Renamed"

	store := newMemoryTaskStore(serviceSnapshot(id, TaskData{
		Title: "Second key", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	}))
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D5",
	}})
	service.Keys = keys

	result, err := service.UpdateMutation(context.Background(), id, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation(%q) error = %v", id, err)
	}
	if got := result.Task.ID; got != id {
		t.Fatalf("UpdateMutation() task ID = %q, want %q", got, id)
	}
	if store.listCalls != 0 {
		t.Fatalf("Reader.List calls = %d, want none: an owned ID is read exactly, not resolved", store.listCalls)
	}

	// The task's own ULID body is reserved, so an ID source that returns it is
	// refused rather than minting an operation that shares the task's name.
	reserving := newMemoryTaskStore(serviceSnapshot(id, TaskData{
		Title: "Second key", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	}))
	service = serviceUnderTest(reserving, &sequenceIDSource{values: []string{body}})
	service.Keys = keys
	_, err = service.UpdateMutation(context.Background(), id, UpdateInput{Title: &title})
	if err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("UpdateMutation() = %v, want a validation error for the task's own ULID", err)
	}
	if !strings.Contains(err.Error(), body) {
		t.Fatalf("UpdateMutation() = %q, want it to name the reserved ULID %q", err, body)
	}
}
