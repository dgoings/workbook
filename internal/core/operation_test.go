package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	projectID    = "01K0M6B8A4FTT8C39MXXYTW7C1"
	taskID       = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"
	generationID = "01K0M6B8A4FTT8C39MXXYTW7C3"
	operationID1 = "01K0M6B8A4FTT8C39MXXYTW7C4"
	operationID2 = "01K0M6B8A4FTT8C39MXXYTW7C5"
	operationID3 = "01K0M6B8A4FTT8C39MXXYTW7C6"
)

var (
	createdAt = time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	updatedAt = time.Date(2026, time.July, 23, 12, 1, 0, 0, time.UTC)
)

func TestApplyCreateUpdateAndTombstone(t *testing.T) {
	create := createPack()

	state, err := Apply(nil, create, "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	if got, want := state.Format, "workbook.task-state"; got != want {
		t.Fatalf("Apply(create) format = %q, want %q", got, want)
	}
	if got, want := state.Version, 1; got != want {
		t.Fatalf("Apply(create) version = %d, want %d", got, want)
	}
	if got, want := state.ProjectID, projectID; got != want {
		t.Fatalf("Apply(create) project ID = %q, want %q", got, want)
	}
	if got, want := state.TaskID, taskID; got != want {
		t.Fatalf("Apply(create) task ID = %q, want %q", got, want)
	}
	if got, want := state.History.Generation, generationID; got != want {
		t.Fatalf("Apply(create) generation = %q, want %q", got, want)
	}
	if got, want := state.LogicalClock, uint64(1); got != want {
		t.Fatalf("Apply(create) logical clock = %d, want %d", got, want)
	}
	if want := create.Operations[0].Task; !reflect.DeepEqual(state.Task, *want) {
		t.Fatalf("Apply(create) task = %#v, want %#v", state.Task, *want)
	}

	update := OperationPack{
		Format:            "workbook.operation-pack",
		Version:           1,
		ProjectID:         projectID,
		TaskID:            taskID,
		HistoryGeneration: generationID,
		Actor:             Actor{ID: "developer@example.com"},
		LogicalClock:      2,
		WallTime:          updatedAt,
		Operations: []Operation{
			{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "ready"},
			{ID: operationID3, Type: OperationSetAdd, Field: "labels", Value: "git"},
		},
	}
	state, err = Apply(&state, update, "WB")
	if err != nil {
		t.Fatalf("Apply(update) error = %v", err)
	}
	if got, want := state.Task.Status, StatusReady; got != want {
		t.Fatalf("Apply(update) status = %q, want %q", got, want)
	}
	if want := []string{"git"}; !reflect.DeepEqual(state.Task.Labels, want) {
		t.Fatalf("Apply(update) labels = %#v, want %#v", state.Task.Labels, want)
	}
	if got, want := state.LogicalClock, uint64(2); got != want {
		t.Fatalf("Apply(update) logical clock = %d, want %d", got, want)
	}
	if got, want := state.Task.CreatedAt, createdAt; !got.Equal(want) {
		t.Fatalf("Apply(update) createdAt = %s, want %s", got, want)
	}

	tombstone := update
	tombstone.LogicalClock = 3
	tombstone.WallTime = updatedAt.Add(time.Minute)
	tombstone.Operations = []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C7", Type: OperationTaskTombstone}}
	state, err = Apply(&state, tombstone, "WB")
	if err != nil {
		t.Fatalf("Apply(tombstone) error = %v", err)
	}
	if !state.Task.Deleted {
		t.Fatalf("Apply(tombstone) deleted = false, want true")
	}
	if got, want := state.Task.Title, "Build Git store"; got != want {
		t.Fatalf("Apply(tombstone) preserved title = %q, want %q", got, want)
	}

	mutation := tombstone
	mutation.LogicalClock = 4
	mutation.Operations = []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C8", Type: OperationFieldSet, Field: "title", Value: "Revive task"}}
	assertCorrupt(t, applyError(&state, mutation, "WB"))
}

func TestApplyRejectsInvalidHistoryAndIdentity(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	tests := []struct {
		name   string
		parent *StateDocument
		pack   OperationPack
	}{
		{
			name:   "create with parent",
			parent: &created,
			pack:   createPack(),
		},
		{
			name:   "update without parent",
			parent: nil,
			pack:   updatePack(2),
		},
		{
			name:   "clock does not advance once",
			parent: &created,
			pack:   updatePack(3),
		},
		{
			name:   "project mismatch",
			parent: &created,
			pack:   func() OperationPack { p := updatePack(2); p.ProjectID = "01K0M6B8A4FTT8C39MXXYTW7C9"; return p }(),
		},
		{
			name:   "task mismatch",
			parent: &created,
			pack:   func() OperationPack { p := updatePack(2); p.TaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C9"; return p }(),
		},
		{
			name:   "generation mismatch",
			parent: &created,
			pack:   func() OperationPack { p := updatePack(2); p.HistoryGeneration = "01K0M6B8A4FTT8C39MXXYTW7CA"; return p }(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Apply(test.parent, test.pack, "WB")
			assertCorrupt(t, err)
		})
	}
}

func TestApplyValidatesOperationFieldsAndValues(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	tests := []struct {
		name string
		op   Operation
	}{
		{"field set unknown field", Operation{ID: operationID2, Type: OperationFieldSet, Field: "labels", Value: "git"}},
		{"field set invalid status", Operation{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "later"}},
		{"field set invalid priority", Operation{ID: operationID2, Type: OperationFieldSet, Field: "priority", Value: "urgent"}},
		{"field set blank title", Operation{ID: operationID2, Type: OperationFieldSet, Field: "title", Value: "  "}},
		{"field set malformed rank", Operation{ID: operationID2, Type: OperationFieldSet, Field: "rank", Value: "2/2"}},
		{"set add unknown field", Operation{ID: operationID2, Type: OperationSetAdd, Field: "title", Value: "x"}},
		{"set add empty label", Operation{ID: operationID2, Type: OperationSetAdd, Field: "labels", Value: ""}},
		{"set add malformed dependency", Operation{ID: operationID2, Type: OperationSetAdd, Field: "dependencies", Value: "WB-not-a-ulid"}},
		{"set remove unknown field", Operation{ID: operationID2, Type: OperationSetRemove, Field: "status", Value: "ready"}},
		{"unknown operation type", Operation{ID: operationID2, Type: "future.operation"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pack := updatePack(2)
			pack.Operations = []Operation{test.op}
			_, err := Apply(&created, pack, "WB")
			assertCorrupt(t, err)
		})
	}
}

func TestApplyFieldSetRequiresCanonicalRationalRank(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	t.Run("rejects noncanonical rank", func(t *testing.T) {
		update := updatePack(2)
		update.Operations = []Operation{{ID: operationID2, Type: OperationFieldSet, Field: "rank", Value: "2/4"}}
		assertCorrupt(t, applyError(&created, update, "WB"))
	})

	t.Run("accepts canonical rank", func(t *testing.T) {
		update := updatePack(2)
		update.Operations = []Operation{{ID: operationID2, Type: OperationFieldSet, Field: "rank", Value: "1/2"}}
		state, err := Apply(&created, update, "WB")
		if err != nil {
			t.Fatalf("Apply(field.set rank) error = %v", err)
		}
		if want := "1/2"; state.Task.Rank != want {
			t.Fatalf("Apply(field.set rank) = %q, want %q", state.Task.Rank, want)
		}
	})
}

func TestValidateFieldSetOperationRequiresCanonicalRationalRank(t *testing.T) {
	if err := validateFieldSetOperation(Operation{Type: OperationFieldSet, Field: "rank", Value: "2/4"}); err == nil {
		t.Fatal("validateFieldSetOperation() accepted noncanonical rank")
	}
	if err := validateFieldSetOperation(Operation{Type: OperationFieldSet, Field: "rank", Value: "1/2"}); err != nil {
		t.Fatalf("validateFieldSetOperation() error = %v", err)
	}
}

func TestApplySetOperationsAreIdempotent(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	add := updatePack(2)
	add.Operations = []Operation{
		{ID: operationID2, Type: OperationSetAdd, Field: "labels", Value: "git"},
		{ID: operationID3, Type: OperationSetAdd, Field: "labels", Value: "git"},
	}
	state, err := Apply(&created, add, "WB")
	if err != nil {
		t.Fatalf("Apply(repeated add) error = %v", err)
	}
	if want := []string{"git"}; !reflect.DeepEqual(state.Task.Labels, want) {
		t.Fatalf("Apply(repeated add) labels = %#v, want %#v", state.Task.Labels, want)
	}

	remove := updatePack(3)
	remove.Operations = []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C7", Type: OperationSetRemove, Field: "labels", Value: "missing"}}
	state, err = Apply(&state, remove, "WB")
	if err != nil {
		t.Fatalf("Apply(missing remove) error = %v", err)
	}
	if want := []string{"git"}; !reflect.DeepEqual(state.Task.Labels, want) {
		t.Fatalf("Apply(missing remove) labels = %#v, want %#v", state.Task.Labels, want)
	}
}

func TestApplyRejectsMutationAfterTombstoneInTheSamePack(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	pack := updatePack(2)
	pack.Operations = []Operation{
		{ID: operationID2, Type: OperationTaskTombstone},
		{ID: operationID3, Type: OperationFieldSet, Field: "title", Value: "Revive task"},
	}

	assertCorrupt(t, applyError(&created, pack, "WB"))
}

func TestApplySupportsDocumentedFieldsAndSets(t *testing.T) {
	created, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	update := updatePack(2)
	update.Operations = []Operation{
		{ID: operationID2, Type: OperationFieldSet, Field: "title", Value: "  Ship Git store  "},
		{ID: operationID3, Type: OperationFieldSet, Field: "description", Value: "Durable operation history."},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7C7", Type: OperationFieldSet, Field: "status", Value: "ready"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7C8", Type: OperationFieldSet, Field: "priority", Value: "high"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7C9", Type: OperationFieldSet, Field: "rank", Value: "2/1"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7CA", Type: OperationSetAdd, Field: "labels", Value: "git"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7CB", Type: OperationSetAdd, Field: "dependencies", Value: "WB-01K0M6B8A4FTT8C39MXXYTW7CC"},
	}
	state, err := Apply(&created, update, "WB")
	if err != nil {
		t.Fatalf("Apply(supported operations) error = %v", err)
	}
	if got, want := state.Task.Title, "Ship Git store"; got != want {
		t.Fatalf("Apply(supported operations) title = %q, want %q", got, want)
	}
	if got, want := state.Task.Description, "Durable operation history."; got != want {
		t.Fatalf("Apply(supported operations) description = %q, want %q", got, want)
	}
	if got, want := state.Task.Priority, PriorityHigh; got != want {
		t.Fatalf("Apply(supported operations) priority = %q, want %q", got, want)
	}
	if got, want := state.Task.Rank, "2/1"; got != want {
		t.Fatalf("Apply(supported operations) rank = %q, want %q", got, want)
	}
	if want := []string{"WB-01K0M6B8A4FTT8C39MXXYTW7CC"}; !reflect.DeepEqual(state.Task.Dependencies, want) {
		t.Fatalf("Apply(supported operations) dependencies = %#v, want %#v", state.Task.Dependencies, want)
	}
}

func TestApplyRejectsUnknownPackFormatsAndVersions(t *testing.T) {
	for name, mutate := range map[string]func(*OperationPack){
		"format":  func(pack *OperationPack) { pack.Format = "workbook.future" },
		"version": func(pack *OperationPack) { pack.Version = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			pack := createPack()
			mutate(&pack)
			assertCorrupt(t, applyError(nil, pack, "WB"))
		})
	}
}

func TestApplyRejectsMalformedDurableIdentifiersAndDuplicateOperations(t *testing.T) {
	for name, mutate := range map[string]func(*OperationPack){
		"invalid project ID": func(pack *OperationPack) {
			pack.ProjectID = "not-a-ulid"
		},
		"noncanonical project ID": func(pack *OperationPack) {
			pack.ProjectID = strings.ToLower(projectID)
		},
		"invalid history generation": func(pack *OperationPack) {
			pack.HistoryGeneration = "not-a-ulid"
		},
		"noncanonical history generation": func(pack *OperationPack) {
			pack.HistoryGeneration = strings.ToLower(generationID)
		},
		"invalid operation ID": func(pack *OperationPack) {
			pack.Operations[0].ID = "not-a-ulid"
		},
		"noncanonical operation ID": func(pack *OperationPack) {
			pack.Operations[0].ID = strings.ToLower(operationID1)
		},
		"noncanonical create task": func(pack *OperationPack) {
			pack.Operations[0].Task.Labels = []string{"poc", "git"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			pack := createPack()
			mutate(&pack)
			assertCorrupt(t, applyError(nil, pack, "WB"))
		})
	}

	parent, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	duplicate := updatePack(2)
	duplicate.Operations = append(duplicate.Operations, Operation{
		ID: operationID2, Type: OperationFieldSet, Field: "priority", Value: "high",
	})
	assertCorrupt(t, applyError(&parent, duplicate, "WB"))
}

func TestApplyRejectsUnsupportedCompactionMetadata(t *testing.T) {
	parent, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	compactedFrom := "0123456789abcdef"
	parent.History.CompactedFrom = &compactedFrom

	assertCorrupt(t, applyError(&parent, updatePack(2), "WB"))
}

func TestValidateCheckpointRejectsByteDifferentState(t *testing.T) {
	pack := createPack()
	stored, err := Apply(nil, pack, "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	stored.Task.Title = "Different"

	assertCorrupt(t, ValidateCheckpoint(nil, pack, stored, "WB"))
}

func createPack() OperationPack {
	return OperationPack{
		Format:            "workbook.operation-pack",
		Version:           1,
		ProjectID:         projectID,
		TaskID:            taskID,
		HistoryGeneration: generationID,
		Actor:             Actor{ID: "developer@example.com"},
		LogicalClock:      1,
		WallTime:          createdAt,
		Operations: []Operation{{
			ID: operationID1, Type: OperationTaskCreate,
			Task: &TaskData{
				Title: "Build Git store", Description: "",
				Status: StatusBacklog, Priority: PriorityMedium,
				Labels: []string{}, Rank: "1/1", Dependencies: []string{},
				CreatedAt: createdAt, UpdatedAt: createdAt, Deleted: false,
			},
		}},
	}
}

func updatePack(clock uint64) OperationPack {
	return OperationPack{
		Format:            "workbook.operation-pack",
		Version:           1,
		ProjectID:         projectID,
		TaskID:            taskID,
		HistoryGeneration: generationID,
		Actor:             Actor{ID: "developer@example.com"},
		LogicalClock:      clock,
		WallTime:          updatedAt,
		Operations:        []Operation{{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "ready"}},
	}
}

func applyError(parent *StateDocument, pack OperationPack, projectKey string) error {
	_, err := Apply(parent, pack, projectKey)
	return err
}

func assertCorrupt(t *testing.T, err error) {
	t.Helper()
	if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("error category = %q, want %q (error: %v)", got, CategoryCorruptData, err)
	}
}
