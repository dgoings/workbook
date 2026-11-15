package projection

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
)

// compactionService writes with fresh operation IDs, because a compacted
// history has to account for every one of them and a repeated ID would hide a
// lost operation.
func compactionService(repository *gitstore.Repository, config core.ProjectConfig) core.Service {
	return core.Service{
		Config: config, Reader: repository, Writer: repository,
		Actor: "test@example.test", Now: time.Now, IDs: core.CryptoULIDSource{},
	}
}

func newULID(t *testing.T) string {
	t.Helper()
	id, err := core.CryptoULIDSource{}.New()
	if err != nil {
		t.Fatalf("new ULID: %v", err)
	}
	return id
}

// doneTaskToCompact is a Done task with a three-commit history.
func doneTaskToCompact(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) string {
	t.Helper()
	ctx := context.Background()
	service := compactionService(repository, config)
	created, err := service.CreateMutation(ctx, core.CreateInput{Title: "Compact me"})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	title := "Compacted title"
	if _, err := service.UpdateMutation(ctx, created.Task.ID, core.UpdateInput{Title: &title}); err != nil {
		t.Fatalf("UpdateMutation(title) error = %v", err)
	}
	done := core.StatusDone
	if _, err := service.UpdateMutation(ctx, created.Task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatalf("UpdateMutation(done) error = %v", err)
	}
	return created.Task.ID
}

// compactTask replaces taskID's history with a checkpoint and moves its ref
// there, with the primitives a compaction run uses, and returns the tip it
// replaced and the checkpoint.
func compactTask(
	t *testing.T,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	taskID string,
) (core.Snapshot, core.Snapshot) {
	t.Helper()
	ctx := context.Background()
	tip, err := repository.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	ids, commits, first, last, err := repository.CompactedHistory(ctx, config, tip.Head)
	if err != nil {
		t.Fatalf("CompactedHistory() error = %v", err)
	}
	checkpoint, err := repository.BuildCheckpoint(ctx, config, gitstore.CheckpointInput{
		TaskID:      taskID,
		Tip:         tip,
		RunID:       newULID(t),
		Generation:  newULID(t),
		OperationID: newULID(t),
		Actor:       "compactor@example.test",
		Now:         time.Now(),
		Summary: core.CheckpointData{
			CompactedCommits:    commits,
			FirstWallTime:       first,
			LastWallTime:        last,
			CompactedOperations: ids,
		},
	})
	if err != nil {
		t.Fatalf("BuildCheckpoint() error = %v", err)
	}
	if err := repository.CompactTaskRef(ctx, config, taskID, tip.Head, checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	return tip, checkpoint
}

func projectedOperationIDs(t *testing.T, store *Store, taskID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT operation_id FROM operations WHERE task_id = ? ORDER BY chain_index, pack_index`, taskID)
	if err != nil {
		t.Fatalf("query projected operations: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan projected operation: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read projected operations: %v", err)
	}
	return ids
}

func assertProjectedCheckpoint(t *testing.T, store *Store, got, tip, checkpoint core.Snapshot) {
	t.Helper()
	if got.Head != checkpoint.Head {
		t.Fatalf("projected head = %s, want the checkpoint %s", got.Head, checkpoint.Head)
	}
	if got.State.History.Generation != checkpoint.State.History.Generation {
		t.Fatalf("projected generation = %s, want the checkpoint's %s",
			got.State.History.Generation, checkpoint.State.History.Generation)
	}
	if !reflect.DeepEqual(got.State.Task, tip.State.Task) {
		t.Fatalf("projected task = %#v, want the compacted tip's %#v", got.State.Task, tip.State.Task)
	}
	want := []string{checkpoint.Operation.Operations[0].ID}
	if ids := projectedOperationIDs(t, store, tip.State.TaskID); !reflect.DeepEqual(ids, want) {
		t.Fatalf("projected operations = %v, want only the checkpoint's %v", ids, want)
	}
}

// A projection that last saw the old tip refreshes onto the checkpoint: the
// generation changed, but the checkpoint accounts for every operation it drops.
func TestStoreRefreshAcceptsCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	taskID := doneTaskToCompact(t, repository, config)
	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(before compaction) error = %v", err)
	}

	tip, checkpoint := compactTask(t, repository, config, taskID)

	listed, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List(after compaction) error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List() returned %d tasks, want 1", len(listed))
	}
	assertProjectedCheckpoint(t, store, listed[0], tip, checkpoint)
}

// A bulk compaction moves many heads at once, and one refresh takes them all,
// with the proof made by the head-advance check alone.
func TestStoreRefreshAcceptsSeveralCompactions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	taskIDs := []string{
		doneTaskToCompact(t, repository, config),
		doneTaskToCompact(t, repository, config),
		doneTaskToCompact(t, repository, config),
	}
	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(before compaction) error = %v", err)
	}
	tips := make(map[string]core.Snapshot, len(taskIDs))
	checkpoints := make(map[string]core.Snapshot, len(taskIDs))
	for _, taskID := range taskIDs {
		tips[taskID], checkpoints[taskID] = compactTask(t, repository, config, taskID)
	}

	listed, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List(after compaction) error = %v", err)
	}
	if len(listed) != len(taskIDs) {
		t.Fatalf("List() returned %d tasks, want %d", len(listed), len(taskIDs))
	}
	for _, snapshot := range listed {
		taskID := snapshot.State.TaskID
		assertProjectedCheckpoint(t, store, snapshot, tips[taskID], checkpoints[taskID])
	}
}

// The exact read takes its own path to the same guard, and its conditional
// advance has to accept the generation change too.
func TestStoreGetAcceptsCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	taskID := doneTaskToCompact(t, repository, config)
	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Get(ctx, config, taskID); err != nil {
		t.Fatalf("Get(before compaction) error = %v", err)
	}

	tip, checkpoint := compactTask(t, repository, config, taskID)

	got, err := store.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get(after compaction) error = %v", err)
	}
	assertProjectedCheckpoint(t, store, got, tip, checkpoint)
}

// Deleting the cache and rebuilding it, and auditing every history in full,
// both start from the checkpoint, which is a root.
func TestRebuildAndValidateAfterCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	taskID := doneTaskToCompact(t, repository, config)
	tip, checkpoint := compactTask(t, repository, config, taskID)

	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	got, err := store.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertProjectedCheckpoint(t, store, got, tip, checkpoint)

	validator, err := historyvalidation.Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("historyvalidation.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	result, err := validator.Validate(ctx, true)
	if err != nil {
		t.Fatalf("Validate(full) error = %v", err)
	}
	if result.Invalid != 0 || len(result.Failures) != 0 || result.Valid != 1 {
		t.Fatalf("Validate(full) = valid %d invalid %d failures %#v, want one valid task and no problems",
			result.Valid, result.Invalid, result.Failures)
	}
}

// A snapshot read from the projection carries the checkpoint's compactedFrom,
// as the state in Git does. A mutation folds its parent's history metadata into
// the state it writes, so an edit built on a projection that dropped it would
// write a state.json that replaying the history from Git does not compute, and
// `validate --full` would call the task corrupt.
func TestProjectionKeepsCompactedFromForTheEditAfterACheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, config := initializeWorkbook(t)
	taskID := doneTaskToCompact(t, repository, config)
	tip, _ := compactTask(t, repository, config, taskID)

	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got, err := store.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if from := got.State.History.CompactedFrom; from == nil || *from != tip.Head {
		t.Fatalf("projected compactedFrom = %v, want the replaced tip %s", from, tip.Head)
	}

	service := compactionService(repository, config)
	service.Reader = store
	service.Projection = store
	reopened := core.StatusReady
	if _, err := service.UpdateMutation(ctx, taskID, core.UpdateInput{Status: &reopened}); err != nil {
		t.Fatalf("UpdateMutation(ready) error = %v", err)
	}
	edited, err := repository.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get(after edit) error = %v", err)
	}
	if from := edited.State.History.CompactedFrom; from == nil || *from != tip.Head {
		t.Fatalf("compactedFrom after the edit = %v, want it carried from the checkpoint %s", from, tip.Head)
	}
	listed, err := store.List(ctx, config)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %v, %v; want the one task", listed, err)
	}
	if from := listed[0].State.History.CompactedFrom; from == nil || *from != tip.Head {
		t.Fatalf("listed compactedFrom = %v, want %s", from, tip.Head)
	}

	validator, err := historyvalidation.Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("historyvalidation.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	result, err := validator.Validate(ctx, true)
	if err != nil {
		t.Fatalf("Validate(full) error = %v", err)
	}
	if result.Invalid != 0 || len(result.Failures) != 0 || result.Valid != 1 {
		t.Fatalf("Validate(full) = valid %d invalid %d failures %#v, want one valid task and no problems",
			result.Valid, result.Invalid, result.Failures)
	}
}
