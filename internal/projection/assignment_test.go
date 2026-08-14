package projection

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// The projection is the read model a mutation resolves its parent through, so a
// snapshot served from here has to carry every assignment the checkpoint did —
// principal, label, creator and creation time alike.
//
// The hazard this guards is not a missing column in a report. A parent snapshot
// that had silently lost its assignments would fold into a checkpoint with
// everybody's assignments dropped, and that checkpoint would be published: the
// Git compare-and-swap would see nothing wrong, because the head did not move.
func TestProjectionRoundTripsAssignments(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	created := time.Date(2026, time.August, 13, 9, 30, 0, 0, time.UTC)
	assigned := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-assigned", "Assigned")
	assigned.State.Task.Assignments = []core.Assignment{
		{Principal: "dylan@example.com", Label: "impl-1", Creator: "sam@example.com", CreatedAt: created},
		{Principal: "sam@example.com", Creator: "sam@example.com", CreatedAt: created.Add(time.Minute)},
	}
	assigned.State.MinReader = 1
	unassigned := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-plain", "Plain")

	source := &staticHeadSource{
		heads: []gitstore.TaskHead{
			{TaskID: assigned.State.TaskID, ObjectID: assigned.Head},
			{TaskID: unassigned.State.TaskID, ObjectID: unassigned.Head},
		},
		snapshots: map[string]core.Snapshot{
			assigned.Head:   assigned,
			unassigned.Head: unassigned,
		},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	// The single-task path.
	got, err := store.Get(ctx, config, assigned.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertSameAssignments(t, "Get", got.State.Task.Assignments, assigned.State.Task.Assignments)

	// The whole-project path, which reads collections in bulk and joins them.
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	byID := make(map[string]core.Snapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.State.TaskID] = snapshot
	}
	assertSameAssignments(t, "List", byID[assigned.State.TaskID].State.Task.Assignments, assigned.State.Task.Assignments)

	// A task nobody assigned reads back as nil rather than as an empty slice.
	// Nil is the canonical empty list — the member is omitted from a stored
	// checkpoint — so a snapshot that spelled it differently would not compare
	// equal to the state a fold computes from the same history.
	for name, snapshot := range map[string]core.Snapshot{
		"Get":  mustGet(t, store, config, unassigned.State.TaskID),
		"List": byID[unassigned.State.TaskID],
	} {
		if snapshot.State.Task.Assignments != nil {
			t.Fatalf("%s assignments = %#v, want nil for a task nobody assigned", name, snapshot.State.Task.Assignments)
		}
	}
}

func mustGet(t *testing.T, store *Store, config core.ProjectConfig, taskID string) core.Snapshot {
	t.Helper()
	snapshot, err := store.Get(context.Background(), config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	return snapshot
}

func assertSameAssignments(t *testing.T, path string, got, want []core.Assignment) {
	t.Helper()
	if !core.SameAssignments(got, want) {
		t.Fatalf("%s assignments = %#v, want %#v", path, got, want)
	}
	// Equality by value is not enough: the ordering has to be the checkpoint's,
	// because two clones comparing checkpoints compare bytes.
	values := make([]string, len(got))
	for index, assignment := range got {
		values[index] = assignment.Value()
	}
	wanted := make([]string, len(want))
	for index, assignment := range want {
		wanted[index] = assignment.Value()
	}
	if !reflect.DeepEqual(values, wanted) {
		t.Fatalf("%s assignment order = %#v, want %#v", path, values, wanted)
	}
}
