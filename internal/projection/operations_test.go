package projection

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestStoreProjectsEveryCommitAnAdvanceCrossed(t *testing.T) {
	// Mutation caught: reading only the changed tip, which is complete for
	// current state and blind to the intermediate packs an operation table
	// needs; an advance of several commits would project one and lose the rest.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}
	// Advance Git behind the projection's back so one refresh must cross three
	// commits at once, the way a fetch that pulled several packs does.
	for index, title := range []string{"Second title", "Third title", "Fourth title"} {
		advanceTaskTitle(t, repository, config, created.ID, title, index)
	}

	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if len(history.Entries) != 4 {
		t.Fatalf("history = %d entries, want the create plus all three advances", len(history.Entries))
	}
	assertChainIsWellFormed(t, history)
	if got := titleAt(t, config, history); got != "Fourth title" {
		t.Fatalf("replayed title = %q, want %q", got, "Fourth title")
	}
	if rows := countOperationRows(t, store, created.ID); rows != 4 {
		t.Fatalf("projected operation rows = %d, want 4", rows)
	}
}

func TestStoreRebuildReprojectsFullHistories(t *testing.T) {
	// Mutation caught: rebuilding current state alone, leaving a projection that
	// can answer show but not show --history.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	for index, title := range []string{"Second title", "Third title"} {
		advanceTaskTitle(t, repository, config, created.ID, title, index)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if rows := countOperationRows(t, store, created.ID); rows != 3 {
		t.Fatalf("projected operation rows = %d, want 3 after a rebuild", rows)
	}
	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	assertChainIsWellFormed(t, history)
}

func TestStoreReplacesOperationRowsForAReconciledTask(t *testing.T) {
	// Mutation caught: upserting a reconciled chain, which strands the rows a
	// replay dropped and breaks the logical-clock chain a replay from the root
	// depends on.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	for index, title := range []string{"Local first", "Local second"} {
		advanceTaskTitle(t, repository, config, created.ID, title, index)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}
	if rows := countOperationRows(t, store, created.ID); rows != 3 {
		t.Fatalf("projected operation rows = %d, want 3 before reconciliation", rows)
	}

	// A reconciliation parks the pre-replay tip and leaves the canonical ref on
	// a head that does not descend from the projected one.
	parked := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+created.ID)
	root := gitOutput(t, repository, "rev-list", "--max-parents=0", parked)
	if _, err := repository.Git(ctx, nil, "update-ref", "refs/workbook/reconciled/"+created.ID+"/0", parked); err != nil {
		t.Fatalf("park pre-replay tip: %v", err)
	}
	if _, err := repository.Git(ctx, nil, "update-ref", "refs/workbook/tasks/"+created.ID, root); err != nil {
		t.Fatalf("roll canonical ref back: %v", err)
	}
	advanceTaskTitle(t, repository, config, created.ID, "Replayed title", 9)

	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if len(history.Entries) != 2 {
		t.Fatalf("history = %d entries, want only the reconciled chain", len(history.Entries))
	}
	assertChainIsWellFormed(t, history)
	if rows := countOperationRows(t, store, created.ID); rows != 2 {
		t.Fatalf("projected operation rows = %d, want the replaced chain alone", rows)
	}
}

func TestStoreTaskHistoryFallsBackToGitWhenNoOperationsAreProjected(t *testing.T) {
	// Mutation caught: answering with an empty history when the projection holds
	// no operations for a task, instead of reading the bounded chain from Git.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM operations`); err != nil {
		t.Fatalf("clear operation rows: %v", err)
	}

	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if len(history.Entries) != 1 || history.Entries[0].Commit != created.Head {
		t.Fatalf("history = %#v, want the task's own root read from Git", history.Entries)
	}
}

func TestStoreCommitHistoryFallsBackPerCommitForAParkedTip(t *testing.T) {
	// Mutation caught: gating the Git fallback per task. The projection is fed
	// from refs/workbook/tasks/ only, so a parked pre-replay commit has no row
	// even when its task is fully projected, and a per-task gate never fires.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	advanceTaskTitle(t, repository, config, created.ID, "Local only", 0)

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	parked := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+created.ID)
	root := gitOutput(t, repository, "rev-list", "--max-parents=0", parked)
	if _, err := repository.Git(ctx, nil, "update-ref", "refs/workbook/reconciled/"+created.ID+"/0", parked); err != nil {
		t.Fatalf("park pre-replay tip: %v", err)
	}
	if _, err := repository.Git(ctx, nil, "update-ref", "refs/workbook/tasks/"+created.ID, root); err != nil {
		t.Fatalf("roll canonical ref back: %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(after reconciliation) error = %v", err)
	}
	if rows := countOperationRows(t, store, created.ID); rows != 1 {
		t.Fatalf("projected operation rows = %d, want only the canonical root", rows)
	}

	history, err := store.CommitHistory(ctx, config, created.ID, parked)
	if err != nil {
		t.Fatalf("CommitHistory(parked) error = %v", err)
	}
	if len(history.Entries) != 2 || history.Entries[1].Commit != parked {
		t.Fatalf("parked history = %#v, want the chain ending at the parked tip", history.Entries)
	}
	if got := titleAt(t, config, history); got != "Local only" {
		t.Fatalf("parked title = %q, want %q", got, "Local only")
	}
}

func TestStoreCommitHistoryReportsARetiredTipAsNotFound(t *testing.T) {
	// Mutation caught: reporting a commit this clone no longer retains as
	// corrupt data rather than as a not-found for that argument.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	absent := fmt.Sprintf("%0*d", len(created.Head), 0)
	_, err = store.CommitHistory(ctx, config, created.ID, absent)
	if got, want := core.CategoryOf(err), core.CategoryNotFound; got != want {
		t.Fatalf("CommitHistory(absent) category = %q, want %q; error = %v", got, want, err)
	}
}

func TestStoreProjectsOperationsWrittenThroughAMutation(t *testing.T) {
	// Mutation caught: recording a mutation's new state without its operation,
	// which would leave the change log a step behind every local edit.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}
	service := core.Service{
		Config: config, Reader: store, Writer: repository, Projection: store, History: store,
		Actor: "test@example.test",
		Now:   func() time.Time { return time.Date(2026, time.July, 26, 12, 5, 0, 0, time.UTC) },
		IDs: core.IDSourceFunc(func() (string, error) {
			return "01K0M6B8A4FTT8C39MXXYTW7E1", nil
		}),
	}
	status := core.StatusReady
	if _, err := service.UpdateMutation(ctx, created.ID, core.UpdateInput{Status: &status}); err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}

	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if len(history.Entries) != 2 {
		t.Fatalf("history = %d entries, want the create and the mutation", len(history.Entries))
	}
	assertChainIsWellFormed(t, history)
	log := core.BuildChangeLog(config.Key, history, 0, true)
	if got, want := log.Changes[1].Summary, "changed status"; got != want {
		t.Fatalf("newest change = %q, want %q", got, want)
	}
}

func TestStoreProjectsNothingForATruncatedChain(t *testing.T) {
	// Mutation caught: storing the valid prefix of a truncated read, which every
	// later reader would take for a complete chain instead of being sent to Git
	// and told where the history stopped.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	advanceTaskTitle(t, repository, config, created.ID, "Second title", 0)

	store, err := openStore(ctx, truncatingSource{repository: repository}, config, t.TempDir()+"/cache.sqlite")
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if rows := countOperationRows(t, store, created.ID); rows != 0 {
		t.Fatalf("projected operation rows = %d, want none for a truncated chain", rows)
	}
}

// truncatingSource reports every operation chain as stopping at its head, the
// way an unreadable commit does.
type truncatingSource struct {
	repository *gitstore.Repository
}

func (s truncatingSource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return s.repository.ListTaskHeads(ctx, config)
}

func (s truncatingSource) InspectTaskHead(ctx context.Context, config core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	return s.repository.InspectTaskHead(ctx, config, taskID)
}

func (s truncatingSource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	return s.repository.ReadTaskHeads(ctx, config, heads)
}

func (s truncatingSource) ValidateTaskHeadAdvances(ctx context.Context, config core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	return s.repository.ValidateTaskHeadAdvances(ctx, config, advances)
}

func (s truncatingSource) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	results, err := s.repository.ReadTaskOperations(ctx, config, requests)
	if err != nil {
		return nil, err
	}
	for index := range results {
		if len(results[index].Commits) == 0 {
			continue
		}
		last := len(results[index].Commits) - 1
		results[index].Truncated = &gitstore.HistoryFailure{
			TaskID: results[index].TaskID,
			Commit: results[index].Commits[last].ObjectID,
			Err:    core.Errorf(core.CategoryCorruptData, "cannot read task commit"),
		}
		results[index].Commits = results[index].Commits[:last]
	}
	return results, nil
}

func TestStoreDropsIncompleteOperationRowsRatherThanLeavingAHole(t *testing.T) {
	// Mutation caught: appending onto a chain whose projected tail is not the
	// parent, which records a chain with a hole a replay cannot cross.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM operations`); err != nil {
		t.Fatalf("clear operation rows: %v", err)
	}

	advance := core.Snapshot{
		Head:      "0000000000000000000000000000000000000000",
		Operation: core.NewOperationPack(config.ProjectID, created.ID, created.HistoryGeneration, "test@example.test", 2, time.Now(), nil),
		State:     core.StateDocument{},
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer transaction.Rollback()
	if err := appendOperations(ctx, transaction, created.ID, created.Head, []gitstore.OperationCommit{{
		ObjectID: advance.Head, Parent: created.Head, Operation: advance.Operation,
	}}); err != nil {
		t.Fatalf("appendOperations() error = %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if rows := countOperationRows(t, store, created.ID); rows != 0 {
		t.Fatalf("projected operation rows = %d, want none once the tail no longer matches", rows)
	}
}

func TestStoreReportsADuplicateOperationIDAsCorruptDataNamingTheTask(t *testing.T) {
	// Mutation caught: letting the operations table's primary key reject the row
	// as an operational cache fault, which tells the user to run `workbook
	// rebuild` — the one command guaranteed to hit the same wall.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	advanceTaskTitle(t, repository, config, created.ID, "Second title", 0)

	store, err := openStore(ctx, duplicatingSource{repository: repository}, config, t.TempDir()+"/cache.sqlite")
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	_, err = store.List(ctx, config)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("List() category = %q, want corrupt-data; error = %v", category, err)
	}
	if !strings.Contains(err.Error(), created.ID) {
		t.Fatalf("List() error = %v, want the offending task named", err)
	}
	if strings.Contains(err.Error(), cacheRecoveryHint) {
		t.Fatalf("List() error = %v, want no rebuild hint for damage a rebuild repeats", err)
	}
}

func TestStoreNamesTheOtherTaskWhenAnOperationIDIsRepeatedAcrossTasks(t *testing.T) {
	// Mutation caught: blaming the task being projected for a ULID the operations
	// primary key is global over. The row already holding it can belong to a
	// different task, and telling somebody to repair the history in front of them
	// points them at a chain that repeats nothing.
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	first := createTask(t, repository, config, "First task")
	second := createTaskWithIDs(t, repository, config, "Second task",
		"01K0M6B8A4FTT8C39MXXYTW7E1", "01K0M6B8A4FTT8C39MXXYTW7E2", "01K0M6B8A4FTT8C39MXXYTW7E3")

	store, err := openStore(ctx, &crossTaskDuplicatingSource{repository: repository}, config, t.TempDir()+"/cache.sqlite")
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	_, err = store.List(ctx, config)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("List() category = %q, want corrupt-data; error = %v", category, err)
	}
	if !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), second.ID) {
		t.Fatalf("List() error = %v, want both %q and %q named", err, first.ID, second.ID)
	}
	if !strings.Contains(err.Error(), "unique across the project") {
		t.Fatalf("List() error = %v, want the project-wide key explained", err)
	}
	if strings.Contains(err.Error(), cacheRecoveryHint) {
		t.Fatalf("List() error = %v, want no rebuild hint for damage a rebuild repeats", err)
	}
}

// createTaskWithIDs creates a task from an explicit ID sequence, so a test that
// needs two tasks in one repository can give the second one identifiers the
// first has not already claimed.
func createTaskWithIDs(
	t *testing.T,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	title string,
	ids ...string,
) core.Task {
	t.Helper()
	index := 0
	service := core.Service{
		Config: config, Reader: repository, Writer: repository,
		Actor: "test@example.test",
		Now:   func() time.Time { return time.Date(2026, time.July, 26, 12, 2, 0, 0, time.UTC) },
		IDs: core.IDSourceFunc(func() (string, error) {
			if index >= len(ids) {
				return "", fmt.Errorf("createTaskWithIDs ran out of identifiers after %d", index)
			}
			value := ids[index]
			index++
			return value, nil
		}),
	}
	result, err := service.CreateMutation(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	return result.Task
}

// crossTaskDuplicatingSource stamps one task's operation ULID onto another
// task's operation, which the operations table refuses exactly as it refuses a
// repeat inside one chain: its key carries no task.
type crossTaskDuplicatingSource struct {
	repository *gitstore.Repository
	borrowed   string
}

func (s *crossTaskDuplicatingSource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return s.repository.ListTaskHeads(ctx, config)
}

func (s *crossTaskDuplicatingSource) InspectTaskHead(ctx context.Context, config core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	return s.repository.InspectTaskHead(ctx, config, taskID)
}

func (s *crossTaskDuplicatingSource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	return s.repository.ReadTaskHeads(ctx, config, heads)
}

func (s *crossTaskDuplicatingSource) ValidateTaskHeadAdvances(ctx context.Context, config core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	return s.repository.ValidateTaskHeadAdvances(ctx, config, advances)
}

func (s *crossTaskDuplicatingSource) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	results, err := s.repository.ReadTaskOperations(ctx, config, requests)
	if err != nil {
		return nil, err
	}
	for index := range results {
		for _, commit := range results[index].Commits {
			if len(commit.Operation.Operations) == 0 {
				continue
			}
			if s.borrowed == "" {
				s.borrowed = commit.Operation.Operations[0].ID
				continue
			}
			if index == 0 {
				continue
			}
			commit.Operation.Operations[0].ID = s.borrowed
			return results, nil
		}
	}
	return results, nil
}

// duplicatingSource repeats one operation ULID later in the same chain, the
// shape a hand-edited or hostile ref can carry and no Workbook build writes.
type duplicatingSource struct {
	repository *gitstore.Repository
}

func (s duplicatingSource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return s.repository.ListTaskHeads(ctx, config)
}

func (s duplicatingSource) InspectTaskHead(ctx context.Context, config core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	return s.repository.InspectTaskHead(ctx, config, taskID)
}

func (s duplicatingSource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	return s.repository.ReadTaskHeads(ctx, config, heads)
}

func (s duplicatingSource) ValidateTaskHeadAdvances(ctx context.Context, config core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	return s.repository.ValidateTaskHeadAdvances(ctx, config, advances)
}

func (s duplicatingSource) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	results, err := s.repository.ReadTaskOperations(ctx, config, requests)
	if err != nil {
		return nil, err
	}
	for index := range results {
		commits := results[index].Commits
		if len(commits) < 2 {
			continue
		}
		first := commits[0].Operation.Operations
		last := commits[len(commits)-1].Operation.Operations
		if len(first) == 0 || len(last) == 0 {
			continue
		}
		last[0].ID = first[0].ID
	}
	return results, nil
}

func advanceTaskTitle(
	t *testing.T,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	taskID, title string,
	sequence int,
) {
	t.Helper()
	service := core.Service{
		Config: config, Reader: repository, Writer: repository, Actor: "test@example.test",
		Now: func() time.Time {
			return time.Date(2026, time.July, 26, 12, 10+sequence, 0, 0, time.UTC)
		},
		IDs: core.IDSourceFunc(func() (string, error) {
			return fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05X", 0x900+sequence), nil
		}),
	}
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Title: &title}); err != nil {
		t.Fatalf("UpdateMutation(%q) error = %v", title, err)
	}
}

func assertChainIsWellFormed(t *testing.T, history core.TaskHistory) {
	t.Helper()
	for index, entry := range history.Entries {
		if entry.Operation.LogicalClock != uint64(index+1) {
			t.Fatalf("entry %d clock = %d, want %d", index, entry.Operation.LogicalClock, index+1)
		}
		wantParent := ""
		if index > 0 {
			wantParent = history.Entries[index-1].Commit
		}
		if entry.Parent != wantParent {
			t.Fatalf("entry %d parent = %q, want %q", index, entry.Parent, wantParent)
		}
	}
}

func titleAt(t *testing.T, config core.ProjectConfig, history core.TaskHistory) string {
	t.Helper()
	task, err := core.StateAt(config.Key, history)
	if err != nil {
		t.Fatalf("StateAt() error = %v", err)
	}
	return task.Title
}

func countOperationRows(t *testing.T, store *Store, taskID string) int {
	t.Helper()
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM operations WHERE task_id = ?`, taskID).Scan(&rows); err != nil {
		t.Fatalf("count operation rows: %v", err)
	}
	return rows
}

func gitOutput(t *testing.T, repository *gitstore.Repository, args ...string) string {
	t.Helper()
	output, err := repository.Git(context.Background(), nil, args...)
	if err != nil {
		t.Fatalf("Git(%v) error = %v", args, err)
	}
	return trimNewline(string(output))
}

func trimNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
