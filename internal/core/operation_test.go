package core

import (
	"reflect"
	"sort"
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

	state, err := Apply(nil, create)
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
	state, err = Apply(&state, update)
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
	beforeTombstone := copyTaskData(state.Task)
	state, err = Apply(&state, tombstone)
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
	assertCorrupt(t, applyError(&state, mutation))

	restore := tombstone
	restore.LogicalClock = 4
	restore.WallTime = tombstone.WallTime.Add(time.Minute)
	restore.Operations = []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C9", Type: OperationTaskRestore}}
	state, err = Apply(&state, restore)
	if err != nil {
		t.Fatalf("Apply(restore) error = %v", err)
	}
	beforeTombstone.UpdatedAt = restore.WallTime
	beforeTombstone.Dependencies = []string{}
	if got, want := state.Task, beforeTombstone; !reflect.DeepEqual(got, want) {
		t.Fatalf("Apply(restore) task = %#v, want %#v", got, want)
	}
}

func TestApplyRejectsRestoreForActiveTaskAndPayloadBearingRestore(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	restore := updatePack(2)
	restore.Operations = []Operation{{ID: operationID2, Type: OperationTaskRestore}}
	assertCorrupt(t, applyError(&created, restore))

	tombstone := updatePack(2)
	tombstone.Operations = []Operation{{ID: operationID2, Type: OperationTaskTombstone}}
	deleted, err := Apply(&created, tombstone)
	if err != nil {
		t.Fatalf("Apply(tombstone) error = %v", err)
	}
	restore = updatePack(3)
	restore.Operations = []Operation{{ID: operationID3, Type: OperationTaskRestore, Field: "title", Value: "must be ignored"}}
	assertCorrupt(t, applyError(&deleted, restore))
}

// A pack against a tombstone may carry ordinary operations after its restore,
// which is what lets a restore into a status be one change rather than two.
// The restore has to come first: everything after it applies to a task that is
// live again, and everything before it would be an edit to a tombstone.
func TestApplyAcceptsARestorePackThatCarriesMoreThanTheRestore(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	tombstone := updatePack(2)
	tombstone.Operations = []Operation{{ID: operationID2, Type: OperationTaskTombstone}}
	deleted, err := Apply(&created, tombstone)
	if err != nil {
		t.Fatalf("Apply(tombstone) error = %v", err)
	}

	restore := updatePack(3)
	restore.Operations = []Operation{
		{ID: operationID1, Type: OperationTaskRestore},
		{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "in-progress"},
		{ID: operationID3, Type: OperationFieldSet, Field: "rank", Value: "4/1"},
	}
	state, err := Apply(&deleted, restore)
	if err != nil {
		t.Fatalf("Apply(restore with a destination) error = %v", err)
	}
	if state.Task.Deleted {
		t.Fatal("Apply(restore with a destination) left the task tombstoned")
	}
	if got, want := state.Task.Status, StatusInProgress; got != want {
		t.Fatalf("Apply(restore with a destination) status = %q, want %q", got, want)
	}
	if got, want := state.Task.Rank, "4/1"; got != want {
		t.Fatalf("Apply(restore with a destination) rank = %q, want %q", got, want)
	}

	// Each refusal is pinned by message rather than only by category. A pack
	// refused for the wrong stated reason is a pack whose rule nobody can read
	// off the error, and the category alone cannot tell these apart.
	tests := map[string]struct {
		operations []Operation
		want       string
	}{
		"a restore that is not first": {
			operations: []Operation{
				{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "in-progress"},
				{ID: operationID1, Type: OperationTaskRestore},
			},
			// The parent is tombstoned, so "requires a tombstoned task" would be
			// a false account of why this is refused.
			want: "task.restore must be the first operation in its pack",
		},
		"two restores": {
			operations: []Operation{
				{ID: operationID1, Type: OperationTaskRestore},
				{ID: operationID2, Type: OperationTaskRestore},
			},
			want: "task.restore must be the first operation in its pack",
		},
		"no restore at all": {
			operations: []Operation{
				{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "in-progress"},
			},
			want: "cannot mutate a tombstoned task",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			pack := updatePack(3)
			pack.Operations = test.operations
			err := applyError(&deleted, pack)
			assertCorrupt(t, err)
			if got := err.Error(); got != test.want {
				t.Fatalf("Apply(%s) error = %q, want %q", name, got, test.want)
			}
		})
	}
}

// A pack may not restore a task and then tombstone it again.
//
// Restoring first satisfies "opens with task.restore", so a rule that checked
// only the first operation would fold this to a tombstone with the clock
// advanced — and ValidateCheckpoint would then certify that state as the
// canonical result, making a task nothing legibly deleted look validly deleted
// to every later reader. A restore and a delete are separate intents and belong
// to separate packs.
func TestApplyRejectsARestorePackThatTombstonesAgain(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	tombstone := updatePack(2)
	tombstone.Operations = []Operation{{ID: operationID2, Type: OperationTaskTombstone}}
	deleted, err := Apply(&created, tombstone)
	if err != nil {
		t.Fatalf("Apply(tombstone) error = %v", err)
	}

	// The trailing tombstone is the shape that slips past a first-operation
	// check, because nothing between it and the restore is refused.
	retombstoned := updatePack(3)
	retombstoned.Operations = []Operation{
		{ID: operationID1, Type: OperationTaskRestore},
		{ID: operationID3, Type: OperationTaskTombstone},
	}
	state, err := Apply(&deleted, retombstoned)
	if err == nil {
		t.Fatalf("Apply(restore then tombstone) = %#v, want a refusal", state.Task)
	}
	assertCorrupt(t, err)
	if got, want := err.Error(), "task.restore must not be followed by task.tombstone"; got != want {
		t.Fatalf("Apply(restore then tombstone) error = %q, want %q", got, want)
	}
	// The state a fold would have produced is the reason this matters: it is a
	// tombstone, and ValidateCheckpoint would have called it canonical.
	if err := ValidateCheckpoint(&deleted, retombstoned, deleted); err == nil {
		t.Fatal("ValidateCheckpoint accepted a restore-then-tombstone pack")
	}

	// The same refusal wherever the tombstone sits after the restore.
	buried := updatePack(3)
	buried.Operations = []Operation{
		{ID: operationID1, Type: OperationTaskRestore},
		{ID: operationID3, Type: OperationTaskTombstone},
		{ID: operationID2, Type: OperationFieldSet, Field: "title", Value: "Deleted again"},
	}
	assertCorrupt(t, applyError(&deleted, buried))

	// And the pack this rule must not touch still folds.
	destination := updatePack(3)
	destination.Operations = []Operation{
		{ID: operationID1, Type: OperationTaskRestore},
		{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "in-progress"},
		{ID: operationID3, Type: OperationFieldSet, Field: "rank", Value: "4/1"},
	}
	restored, err := Apply(&deleted, destination)
	if err != nil {
		t.Fatalf("Apply(restore with a destination) error = %v", err)
	}
	if restored.Task.Deleted {
		t.Fatal("the restore-into pack no longer folds to a live task")
	}
}

func TestApplyRejectsInvalidHistoryAndIdentity(t *testing.T) {
	created, err := Apply(nil, createPack())
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
			_, err := Apply(test.parent, test.pack)
			assertCorrupt(t, err)
		})
	}
}

func TestApplyValidatesOperationFieldsAndValues(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	tests := []struct {
		name string
		op   Operation
	}{
		{"field set unknown field", Operation{ID: operationID2, Type: OperationFieldSet, Field: "labels", Value: "git"}},
		// "later" would now be accepted: the replay gate checks a status's
		// shape, not this build's vocabulary, because the operation was written
		// by a clone whose configuration this one may not have fetched. A value
		// that is not a status token at all is still corrupt data.
		{"field set malformed status", Operation{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "Later Maybe"}},
		// "urgent" would now be accepted too, for the same reason as "later"
		// above: the replay gate checks a priority's shape, not this build's
		// vocabulary, mirroring the status case. A value that is not a
		// priority token at all is still corrupt data.
		{"field set malformed priority", Operation{ID: operationID2, Type: OperationFieldSet, Field: "priority", Value: "Really Urgent"}},
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
			_, err := Apply(&created, pack)
			assertCorrupt(t, err)
		})
	}
}

func TestApplyFieldSetRequiresCanonicalRationalRank(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	t.Run("rejects noncanonical rank", func(t *testing.T) {
		update := updatePack(2)
		update.Operations = []Operation{{ID: operationID2, Type: OperationFieldSet, Field: "rank", Value: "2/4"}}
		assertCorrupt(t, applyError(&created, update))
	})

	t.Run("accepts canonical rank", func(t *testing.T) {
		update := updatePack(2)
		update.Operations = []Operation{{ID: operationID2, Type: OperationFieldSet, Field: "rank", Value: "1/2"}}
		state, err := Apply(&created, update)
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
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}

	add := updatePack(2)
	add.Operations = []Operation{
		{ID: operationID2, Type: OperationSetAdd, Field: "labels", Value: "git"},
		{ID: operationID3, Type: OperationSetAdd, Field: "labels", Value: "git"},
	}
	state, err := Apply(&created, add)
	if err != nil {
		t.Fatalf("Apply(repeated add) error = %v", err)
	}
	if want := []string{"git"}; !reflect.DeepEqual(state.Task.Labels, want) {
		t.Fatalf("Apply(repeated add) labels = %#v, want %#v", state.Task.Labels, want)
	}

	remove := updatePack(3)
	remove.Operations = []Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C7", Type: OperationSetRemove, Field: "labels", Value: "missing"}}
	state, err = Apply(&state, remove)
	if err != nil {
		t.Fatalf("Apply(missing remove) error = %v", err)
	}
	if want := []string{"git"}; !reflect.DeepEqual(state.Task.Labels, want) {
		t.Fatalf("Apply(missing remove) labels = %#v, want %#v", state.Task.Labels, want)
	}
}

func TestApplyRejectsMutationAfterTombstoneInTheSamePack(t *testing.T) {
	created, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	pack := updatePack(2)
	pack.Operations = []Operation{
		{ID: operationID2, Type: OperationTaskTombstone},
		{ID: operationID3, Type: OperationFieldSet, Field: "title", Value: "Revive task"},
	}

	assertCorrupt(t, applyError(&created, pack))
}

func TestApplySupportsDocumentedFieldsAndSets(t *testing.T) {
	created, err := Apply(nil, createPack())
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
	state, err := Apply(&created, update)
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
			assertCorrupt(t, applyError(nil, pack))
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
			assertCorrupt(t, applyError(nil, pack))
		})
	}

	parent, err := Apply(nil, createPack())
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	duplicate := updatePack(2)
	duplicate.Operations = append(duplicate.Operations, Operation{
		ID: operationID2, Type: OperationFieldSet, Field: "priority", Value: "high",
	})
	assertCorrupt(t, applyError(&parent, duplicate))
}

// A state may name the tip it was compacted from, and only by a well-formed
// object ID of either hash length.
func TestApplyAcceptsOnlyWellFormedCompactedFrom(t *testing.T) {
	for name, test := range map[string]struct {
		compactedFrom string
		valid         bool
	}{
		"sha-1 object ID":         {compactedFrom: compactedTipOID, valid: true},
		"sha-256 object ID":       {compactedFrom: strings.Repeat("ab", 32), valid: true},
		"truncated object ID":     {compactedFrom: "0123456789abcdef"},
		"uppercase object ID":     {compactedFrom: strings.ToUpper(compactedTipOID)},
		"empty object ID":         {compactedFrom: ""},
		"object ID with a suffix": {compactedFrom: compactedTipOID + "0"},
	} {
		t.Run(name, func(t *testing.T) {
			parent, err := Apply(nil, createPack())
			if err != nil {
				t.Fatalf("Apply(create) error = %v", err)
			}
			compactedFrom := test.compactedFrom
			parent.History.CompactedFrom = &compactedFrom

			state, err := Apply(&parent, updatePack(2))
			if !test.valid {
				assertCorrupt(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if state.History.CompactedFrom == nil || *state.History.CompactedFrom != test.compactedFrom {
				t.Fatalf("Apply() compactedFrom = %v, want %q", state.History.CompactedFrom, test.compactedFrom)
			}
		})
	}
}

const (
	compactedTipOID        = "4f6a1d2c9b8e7a6f5e4d3c2b1a0918273645abcd"
	checkpointGenerationID = "01K0M6B8A4FTT8C39MXXYTWK01"
	checkpointRunID        = "01K0M6B8A4FTT8C39MXXYTWK02"
	checkpointOperationID  = "01K0M6B8A4FTT8C39MXXYTWK03"
	afterCheckpointOpID    = "01K0M6B8A4FTT8C39MXXYTWK04"
)

var checkpointWallTime = time.Date(2026, time.November, 10, 9, 0, 0, 0, time.UTC)

// historyToCompact folds a task through a create and every kind of edit a
// checkpoint has to carry: an assignment, a comment, a file attachment and a
// link attachment. It returns the folded state and every operation ID the
// history holds.
func historyToCompact(t *testing.T) (StateDocument, []string) {
	t.Helper()
	state := createdTask(t)
	ids := []string{operationID1}
	for _, operation := range []Operation{
		assign(assignID1, "dylan@example.com"),
		comment(commentOneID, "First note"),
		fileAttachment(attachOneID, "notes.txt"),
		linkAttachment(attachTwoID, "https://example.com/spec", "Spec"),
	} {
		pack := NewOperationPack(projectID, taskID, generationID, "developer@example.com",
			state.LogicalClock+1, updatedAt, []Operation{operation})
		next, err := Apply(&state, pack)
		if err != nil {
			t.Fatalf("Apply(%s) error = %v", operation.Type, err)
		}
		state = next
		ids = append(ids, operation.ID)
	}
	sort.Strings(ids)
	return state, ids
}

// checkpointPackFor builds the checkpoint that replaces state's history, at the
// next logical clock and in a new generation.
func checkpointPackFor(state StateDocument, compacted []string) OperationPack {
	task := state.Task
	return NewOperationPack(projectID, taskID, checkpointGenerationID, "compactor@example.com",
		state.LogicalClock+1, checkpointWallTime, []Operation{{
			ID:   checkpointOperationID,
			Type: OperationHistoryCheckpoint,
			Task: &task,
			Checkpoint: &CheckpointData{
				RunID:               checkpointRunID,
				CompactedFrom:       compactedTipOID,
				PreviousGeneration:  state.History.Generation,
				CompactedCommits:    len(compacted),
				FirstWallTime:       createdAt,
				LastWallTime:        updatedAt,
				Attachments:         CheckpointAttachmentsKept,
				CompactedOperations: append([]string(nil), compacted...),
			},
		}})
}

func TestApplyFoldsCheckpointWithoutParent(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)

	state, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply(nil, checkpoint) error = %v", err)
	}
	if !reflect.DeepEqual(state.Task, compacted.Task) {
		t.Fatalf("Apply(nil, checkpoint) task = %#v, want %#v", state.Task, compacted.Task)
	}
	if len(state.Task.Comments) != 1 || len(state.Task.Assignments) != 1 || len(state.Task.Attachments) != 2 {
		t.Fatalf("checkpoint lost the thread: %#v", state.Task)
	}
	if got, want := state.History.Generation, checkpointGenerationID; got != want {
		t.Fatalf("Apply(nil, checkpoint) generation = %q, want %q", got, want)
	}
	if state.History.CompactedFrom == nil || *state.History.CompactedFrom != compactedTipOID {
		t.Fatalf("Apply(nil, checkpoint) compactedFrom = %v, want %q", state.History.CompactedFrom, compactedTipOID)
	}
	if got, want := state.LogicalClock, compacted.LogicalClock+1; got != want {
		t.Fatalf("Apply(nil, checkpoint) logical clock = %d, want %d", got, want)
	}
	if got, want := state.MinReader, 4; got != want {
		t.Fatalf("Apply(nil, checkpoint) minReader = %d, want %d", got, want)
	}
	if got, want := pack.MinReader, 4; got != want {
		t.Fatalf("checkpoint pack minReader = %d, want %d", got, want)
	}
	if err := ValidateCheckpoint(nil, pack, state); err != nil {
		t.Fatalf("ValidateCheckpoint(nil, checkpoint) error = %v", err)
	}
}

// The compaction summary rides inside the pack; the folded state names only the
// replaced tip. The pack's accessor hands the summary back to every later
// reader.
func TestOperationPackCheckpointReturnsTheSummary(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)

	summary, ok := pack.Checkpoint()
	if !ok {
		t.Fatal("Checkpoint() ok = false for a checkpoint pack")
	}
	if summary != pack.Operations[0].Checkpoint {
		t.Fatalf("Checkpoint() = %p, want the operation's payload %p", summary, pack.Operations[0].Checkpoint)
	}
	if _, ok := createPack().Checkpoint(); ok {
		t.Fatal("Checkpoint() ok = true for a task.create pack")
	}
	if _, ok := updatePack(2).Checkpoint(); ok {
		t.Fatal("Checkpoint() ok = true for a field.set pack")
	}
	twoOperations := pack
	twoOperations.Operations = append(append([]Operation(nil), pack.Operations...), Operation{
		ID: afterCheckpointOpID, Type: OperationFieldSet, Field: "status", Value: "ready",
	})
	if _, ok := twoOperations.Checkpoint(); ok {
		t.Fatal("Checkpoint() ok = true for a pack that carries more than the checkpoint")
	}
}

func TestApplyRefusesCheckpointWithParent(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)
	// The pack is otherwise a valid child of the parent, so the only rule it
	// breaks is that a checkpoint is a root.
	pack.HistoryGeneration = compacted.History.Generation

	err := applyError(&compacted, pack)
	assertCorrupt(t, err)
	if !strings.Contains(err.Error(), "history.checkpoint requires no parent") {
		t.Fatalf("Apply(parent, checkpoint) error = %v, want the root rule", err)
	}
}

func TestApplyRefusesCheckpointRootWithOtherOperations(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)
	pack.Operations = append(pack.Operations, Operation{
		ID: afterCheckpointOpID, Type: OperationFieldSet, Field: "status", Value: "ready",
	})

	assertCorrupt(t, applyError(nil, pack))
}

func TestApplyCarriesCompactedFromToChildren(t *testing.T) {
	compacted, ids := historyToCompact(t)
	root, err := Apply(nil, checkpointPackFor(compacted, ids))
	if err != nil {
		t.Fatalf("Apply(nil, checkpoint) error = %v", err)
	}
	child := NewOperationPack(projectID, taskID, checkpointGenerationID, "developer@example.com",
		root.LogicalClock+1, checkpointWallTime.Add(time.Minute), []Operation{{
			ID: afterCheckpointOpID, Type: OperationFieldSet, Field: "status", Value: "ready",
		}})
	if child.MinReader != 0 {
		t.Fatalf("an ordinary pack after a checkpoint carries minReader %d, want 0", child.MinReader)
	}

	state, err := Apply(&root, child)
	if err != nil {
		t.Fatalf("Apply(checkpoint, field.set) error = %v", err)
	}
	if state.History.CompactedFrom == nil || *state.History.CompactedFrom != compactedTipOID {
		t.Fatalf("child compactedFrom = %v, want %q", state.History.CompactedFrom, compactedTipOID)
	}
	if got, want := state.History.Generation, checkpointGenerationID; got != want {
		t.Fatalf("child generation = %q, want %q", got, want)
	}
	if got, want := state.MinReader, 4; got != want {
		t.Fatalf("child minReader = %d, want %d", got, want)
	}
	if got, want := state.Task.Status, Status("ready"); got != want {
		t.Fatalf("child status = %q, want %q", got, want)
	}
}

func TestValidateCheckpointRejectsMalformedCompactedFrom(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)
	pack.Operations[0].Checkpoint.CompactedFrom = "xyz"

	assertCorrupt(t, applyError(nil, pack))
	_, err := EncodeDocument(pack)
	assertCorrupt(t, err)
}

func TestValidateCheckpointRejectsMalformedSummary(t *testing.T) {
	for name, mutate := range map[string]func(*OperationPack){
		"run ID not a ULID": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.RunID = "run-1"
		},
		"lowercase run ID": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.RunID = strings.ToLower(checkpointRunID)
		},
		"previous generation not a ULID": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.PreviousGeneration = ""
		},
		"previous generation is the new generation": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.PreviousGeneration = pack.HistoryGeneration
		},
		"sha-1 object ID too short": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.CompactedFrom = compactedTipOID[:39]
		},
		"unknown attachments mode": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.Attachments = "maybe"
		},
		"no attachments mode": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.Attachments = ""
		},
		"no compacted operations": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.CompactedOperations = nil
		},
		"unsorted compacted operations": func(pack *OperationPack) {
			ids := pack.Operations[0].Checkpoint.CompactedOperations
			ids[0], ids[1] = ids[1], ids[0]
		},
		"duplicate compacted operations": func(pack *OperationPack) {
			ids := pack.Operations[0].Checkpoint.CompactedOperations
			ids[1] = ids[0]
		},
		"compacted operation not a ULID": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.CompactedOperations = []string{"not-a-ulid"}
		},
		"no compacted commits": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.CompactedCommits = 0
		},
		"no first wall time": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.FirstWallTime = time.Time{}
		},
		"no last wall time": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.LastWallTime = time.Time{}
		},
		"zero became-done time": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.BecameDoneAt = &time.Time{}
		},
		"zero became-deleted time": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint.BecameDeletedAt = &time.Time{}
		},
		"noncanonical task": func(pack *OperationPack) {
			pack.Operations[0].Task.Labels = []string{"poc", "git"}
		},
		"task without createdAt": func(pack *OperationPack) {
			pack.Operations[0].Task.CreatedAt = time.Time{}
		},
		"field payload": func(pack *OperationPack) {
			pack.Operations[0].Field = "title"
		},
		"value payload": func(pack *OperationPack) {
			pack.Operations[0].Value = "x"
		},
		"comment payload": func(pack *OperationPack) {
			pack.Operations[0].Body = "x"
		},
	} {
		t.Run(name, func(t *testing.T) {
			compacted, ids := historyToCompact(t)
			pack := checkpointPackFor(compacted, ids)
			mutate(&pack)
			assertCorrupt(t, applyError(nil, pack))
		})
	}
}

func TestCheckpointRequiresSummary(t *testing.T) {
	for name, mutate := range map[string]func(*OperationPack){
		"no summary": func(pack *OperationPack) {
			pack.Operations[0].Checkpoint = nil
		},
		"no task": func(pack *OperationPack) {
			pack.Operations[0].Task = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			compacted, ids := historyToCompact(t)
			pack := checkpointPackFor(compacted, ids)
			mutate(&pack)
			assertCorrupt(t, applyError(nil, pack))
		})
	}
}

// A compaction summary belongs to a checkpoint and to nothing else; an
// ordinary operation carrying one is a document nobody wrote on purpose.
func TestCheckpointSummaryOnAnotherOperationIsCorrupt(t *testing.T) {
	parent := createdTask(t)
	pack := updatePack(2)
	pack.Operations[0].Checkpoint = &CheckpointData{RunID: checkpointRunID}

	assertCorrupt(t, applyError(&parent, pack))
	_, err := EncodeDocument(pack)
	assertCorrupt(t, err)

	create := createPack()
	create.Operations[0].Checkpoint = &CheckpointData{RunID: checkpointRunID}
	assertCorrupt(t, applyError(nil, create))
}

// A checkpoint round-trips through the strict decoder this build reads its own
// documents with, so every member it writes is one it declares.
func TestCheckpointDocumentsRoundTripStrictly(t *testing.T) {
	compacted, ids := historyToCompact(t)
	pack := checkpointPackFor(compacted, ids)
	done := updatedAt.Add(time.Hour)
	pack.Operations[0].Checkpoint.BecameDoneAt = &done
	state, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply(nil, checkpoint) error = %v", err)
	}

	encodedPack, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(checkpoint pack) error = %v", err)
	}
	decodedPack, err := DecodeOperationPack(encodedPack)
	if err != nil {
		t.Fatalf("DecodeOperationPack(checkpoint) error = %v", err)
	}
	if !reflect.DeepEqual(decodedPack, pack) {
		t.Fatalf("decoded checkpoint pack = %#v, want %#v", decodedPack, pack)
	}

	encodedState, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(checkpoint state) error = %v", err)
	}
	decodedState, err := DecodeStateDocument(encodedState)
	if err != nil {
		t.Fatalf("DecodeStateDocument(checkpoint) error = %v", err)
	}
	if err := ValidateCheckpoint(nil, decodedPack, decodedState); err != nil {
		t.Fatalf("ValidateCheckpoint(decoded) error = %v", err)
	}
}

// A compacted task can be deleted; its checkpoint carries the tombstone.
func TestApplyFoldsCheckpointOfADeletedTask(t *testing.T) {
	compacted, ids := historyToCompact(t)
	tombstone := NewOperationPack(projectID, taskID, generationID, "developer@example.com",
		compacted.LogicalClock+1, updatedAt, []Operation{{ID: afterCheckpointOpID, Type: OperationTaskTombstone}})
	deleted, err := Apply(&compacted, tombstone)
	if err != nil {
		t.Fatalf("Apply(tombstone) error = %v", err)
	}
	ids = append(ids, afterCheckpointOpID)
	sort.Strings(ids)
	pack := checkpointPackFor(deleted, ids)
	gone := updatedAt
	pack.Operations[0].Checkpoint.BecameDeletedAt = &gone

	state, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply(nil, checkpoint of deleted task) error = %v", err)
	}
	if !state.Task.Deleted {
		t.Fatal("checkpoint of a deleted task folded to a live task")
	}
}

func TestValidateCheckpointRejectsByteDifferentState(t *testing.T) {
	pack := createPack()
	stored, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	stored.Task.Title = "Different"

	assertCorrupt(t, ValidateCheckpoint(nil, pack, stored))
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

func applyError(parent *StateDocument, pack OperationPack) error {
	_, err := Apply(parent, pack)
	return err
}

func assertCorrupt(t *testing.T, err error) {
	t.Helper()
	if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("error category = %q, want %q (error: %v)", got, CategoryCorruptData, err)
	}
}
