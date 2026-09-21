package core

import "testing"

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
