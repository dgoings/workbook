package gitstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A replayed set.add that closes a cycle against the fetched graph is the one
// dependency case reconciliation refuses. Recording it would leave both tasks
// permanently ineligible for selection with nothing to point at.
func TestFetchReportsDependencyCycleConflictAgainstFetchedGraph(t *testing.T) {
	first, second, config := syncRepositories(t)
	blocked := createSyncTask(t, first, config, "Blocked task")
	blocker := createSyncTask(t, first, config, "Blocker task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// Origin gains blocked -> blocker, which this clone has not seen.
	dependSyncTask(t, first, config, blocked.ID, blocker.ID)
	updateSyncTask(t, first, config, blocker.ID, "Blocker branch")
	publishTaskRefs(t, first)

	// Locally, blocker gains the opposite edge, so replaying it closes a cycle.
	dependSyncTask(t, second, config, blocker.ID, blocked.ID)

	result, err := second.Fetch(context.Background(), config)
	if core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Fetch(cycle) error = %v, want a conflict; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, blocker.ID, SyncConflicted)
	if len(result.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want exactly one", result.Conflicts)
	}
	conflict := result.Conflicts[0]
	if conflict.TaskID != blocker.ID || conflict.Type != core.ConflictDependencyCycle || conflict.Dependency == nil {
		t.Fatalf("conflict = %#v, want a dependency cycle for %s", conflict, blocker.ID)
	}
	if conflict.Dependency.From != blocker.ID || conflict.Dependency.To != blocked.ID {
		t.Fatalf("conflict edge = %s -> %s, want %s -> %s",
			conflict.Dependency.From, conflict.Dependency.To, blocker.ID, blocked.ID)
	}
	wantPath := []string{blocked.ID, blocker.ID}
	if !sameStrings(conflict.Dependency.Path, wantPath) {
		t.Fatalf("closing path = %v, want %v", conflict.Dependency.Path, wantPath)
	}

	snapshot, err := second.Get(context.Background(), config, blocker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.Task.Dependencies) != 0 {
		t.Fatalf("conflicted task dependencies = %v, want the unresolved edge left unrecorded", snapshot.State.Task.Dependencies)
	}
}

func TestFetchReportsTombstoneConflictWithBlockedOperation(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Doomed task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	deleteSyncTask(t, first, config, task.ID)
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)
	updateSyncTask(t, second, config, task.ID, "Local branch")
	localPack, err := second.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, fetchErr := second.Fetch(context.Background(), config)
	if core.CategoryOf(fetchErr) != core.CategoryConflict {
		t.Fatalf("Fetch(tombstone) error = %v, want a conflict; result = %#v", fetchErr, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncConflicted)
	if len(result.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want exactly one", result.Conflicts)
	}
	conflict := result.Conflicts[0]
	if conflict.Type != core.ConflictTombstone || conflict.Tombstone == nil {
		t.Fatalf("conflict = %#v, want a tombstone conflict", conflict)
	}
	blocked := localPack.Operation.Operations[0]
	if conflict.Tombstone.OperationID != blocked.ID || conflict.Tombstone.Operation != blocked.Type {
		t.Fatalf("blocked operation = %#v, want %s %s", conflict.Tombstone, blocked.ID, blocked.Type)
	}
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("conflicted tip = %q, want the fetched tombstone %q", got, remoteTip)
	}
}

// A replayed operation whose value the fetched history already holds says
// nothing new, so it earns no commit rather than an empty edit.
func TestFetchRecordsNoCommitWhenReplayedValueMatchesUpstream(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Agreed task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	setSyncTaskStatus(t, first, config, task.ID, core.StatusReady)
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)
	setSyncTaskStatus(t, second, config, task.ID, core.StatusReady)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(agreeing) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("reconciled tip = %q, want the fetched tip %q with no replayed commit", got, remoteTip)
	}
}

// Parked tips are retired by the next mutation of their task, not by the fetch
// that created them: a fetch must not delete recoverable work in the same
// command that orphaned it.
func TestMutationPrunesParkedRefsAndFetchDoesNot(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Parked task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}

	head := refValue(t, second, taskRefPrefix+task.ID)
	for index := range 5 {
		syncGit(t, second.Root, "update-ref", fmt.Sprintf("%s%s/%d", reconciledRefPrefix, task.ID, index), head)
	}

	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	for index := range 5 {
		if !refExists(t, second, fmt.Sprintf("%s%s/%d", reconciledRefPrefix, task.ID, index)) {
			t.Fatalf("fetch pruned parked ref %d", index)
		}
	}

	updateSyncTask(t, second, config, task.ID, "Prune trigger")
	for index := range 5 {
		name := fmt.Sprintf("%s%s/%d", reconciledRefPrefix, task.ID, index)
		want := index >= 5-maxParkedRefsPerTask
		if got := refExists(t, second, name); got != want {
			t.Fatalf("parked ref %d present = %t, want %t", index, got, want)
		}
	}
}

// Parked refs live outside the task namespace, so nothing that enumerates task
// refs — including the refspec a push builds — can carry them to origin.
func TestParkedRefsAreNeverPublished(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Unpublished parking")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, task.ID, "Remote branch")
	setSyncTaskPriority(t, second, config, task.ID, core.PriorityHigh)
	publishTaskRefs(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	parked := reconciledRefPrefix + task.ID + "/0"
	if !refExists(t, second, parked) {
		t.Fatalf("reconciliation did not park the orphaned tip at %s", parked)
	}

	if _, err := second.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	if got := syncGit(t, second.Root, "ls-remote", "--refs", "origin", reconciledRefPrefix+"*"); got != "" {
		t.Fatalf("origin holds parked refs: %q", got)
	}
}

// The projection's descendant guard has to keep rejecting a ref rolled
// backwards while accepting the one thing that legitimately does so.
func TestValidateTaskHeadAdvancesAcceptsOnlyParkedNonDescendants(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Advancing task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	root, err := second.Get(ctx, config, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, task.ID, "Remote branch")
	setSyncTaskPriority(t, second, config, task.ID, core.PriorityHigh)
	publishTaskRefs(t, first)
	previous, err := second.Get(ctx, config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	current := TaskHead{TaskID: task.ID, ObjectID: refValue(t, second, taskRefPrefix+task.ID)}

	if err := second.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{Previous: previous, Current: current}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(parked previous) error = %v, want nil", err)
	}

	// The root is an ancestor of both tips and was never parked, so moving a
	// projection forward from an unparked, unreachable tip must still fail.
	syncGit(t, second.Root, "update-ref", "-d", reconciledRefPrefix+task.ID+"/0")
	err = second.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{Previous: previous, Current: current}})
	if core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("ValidateTaskHeadAdvances(unparked previous) error = %v, want corrupt data", err)
	}
	if err := second.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{
		Previous: root, Current: current,
	}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(ancestor previous) error = %v, want nil", err)
	}
}

func dependSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, dependencyID string) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.DependMutation(context.Background(), taskID, dependencyID); err != nil {
		t.Fatal(err)
	}
}

func deleteSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.DeleteMutation(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
}

func setSyncTaskStatus(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string, status core.Status) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
}
