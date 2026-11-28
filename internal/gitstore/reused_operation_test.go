package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// forgeReusedOperation writes, on top of onto's tip of taskID, a commit whose
// pack copies mine's operation IDs with its first value replaced: another
// change under the same operation ID. It points onto's task ref at the commit
// and returns it.
func forgeReusedOperation(t *testing.T, onto *Repository, config core.ProjectConfig, taskID string, mine core.Snapshot, value string) string {
	t.Helper()
	ctx := context.Background()
	tip := currentSnapshot(t, onto, config, taskID)
	forged := mine.Operation
	forged.Operations = append([]core.Operation(nil), mine.Operation.Operations...)
	forged.Operations[0].Value = value
	forged.LogicalClock = tip.State.LogicalClock + 1
	forged.HistoryGeneration = tip.Operation.HistoryGeneration
	state, err := core.Apply(&tip.State, forged)
	if err != nil {
		t.Fatal(err)
	}
	head, err := onto.writeTaskObjects(ctx, tip.Head, forged, state, "workbook: forged", "")
	if err != nil {
		t.Fatal(err)
	}
	syncGit(t, onto.Root, "update-ref", taskRefPrefix+taskID, head, tip.Head)
	return head
}

// An operation ID names one operation. A remote holding a commit that reuses
// the ID of this checkout's unpublished edit with other content is corrupt,
// and synchronizing with it is refused, naming the task, the operation and
// both commits, with this checkout's task ref and its edit left as they were.
// Skipping the edit because its ID is already there would take the other
// value and say nothing.
func TestSyncRefusesAnOperationIDReusedWithOtherContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Shared")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, task.ID, "Mine")
	mine := currentSnapshot(t, first, config, task.ID)
	forged := forgeReusedOperation(t, second, config, task.ID, mine, "Forged")
	syncGit(t, second.Root, "push", "--quiet", "origin", forged+":"+taskRefPrefix+task.ID)

	result, err := first.Sync(ctx, config)
	if core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("Sync error = %v (category %q), want corrupt-data", err, core.CategoryOf(err))
	}
	detail := ""
	for _, item := range result.Fetch.Tasks {
		if item.TaskID == task.ID {
			if item.Status != SyncInvalid {
				t.Fatalf("task outcome = %+v, want invalid", item)
			}
			detail = item.Detail
		}
	}
	for _, want := range []string{task.ID, mine.Operation.Operations[0].ID, mine.Head, forged, "different content"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("task outcome detail = %q, want it to name %s", detail, want)
		}
	}
	after := currentSnapshot(t, first, config, task.ID)
	if after.Head != mine.Head || after.State.Task.Title != "Mine" {
		t.Fatalf("task after refusal = %s %q, want %s %q", after.Head, after.State.Task.Title, mine.Head, "Mine")
	}
	if parked := parkedRefNames(t, first, task.ID); len(parked) != 0 {
		t.Fatalf("parked refs after refusal = %v, want none", parked)
	}
	if got := syncGit(t, first.Root, "ls-remote", "origin", taskRefPrefix+task.ID); !strings.HasPrefix(got, forged) {
		t.Fatalf("origin's task ref = %q, want the forged commit left as it was", got)
	}
}

// The move carries by operation identity, so a commit at the old place that
// reuses the ID of this checkout's edit with other content must not count as
// held: the run is refused as corrupt, naming the task, the operation and both
// commits, and this checkout's edit stays as it was.
func TestMoveTasksRefusesAnOperationIDReusedWithOtherContentAtTheOldPlace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	taskID := fixture.ids[0]
	updateSyncTask(t, fixture.first, fixture.config, taskID, "Mine")
	mine := currentSnapshot(t, fixture.first, fixture.config, taskID)
	forged := forgeReusedOperation(t, fixture.second, fixture.stale, taskID, mine, "Forged")
	syncGit(t, fixture.second.Root, "push", "--quiet", "origin", forged+":"+taskRefPrefix+taskID)

	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryCorruptData || result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v (category %q), want a corrupt-data refusal", result, err, core.CategoryOf(err))
	}
	for _, want := range []string{taskID, mine.Operation.Operations[0].ID, mine.Head, forged, "different content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("MoveTasks() error = %q, want it to name %s", err, want)
		}
	}
	after := currentSnapshot(t, fixture.first, fixture.config, taskID)
	if after.Head != mine.Head || after.State.Task.Title != "Mine" {
		t.Fatalf("task after refusal = %s %q, want %s %q", after.Head, after.State.Task.Title, mine.Head, "Mine")
	}
	if parked := parkedRefNames(t, fixture.first, taskID); len(parked) != 0 {
		t.Fatalf("parked refs after refusal = %v, want none", parked)
	}
}

// A move whose copy to the new place stops because a task's replay was
// refused says which task and why, since the command prints no fetch result
// when it fails: here the new place holds a commit reusing the ID of this
// checkout's unpublished edit with other content.
func TestMoveTasksNamesTheTaskItCouldNotCopyAndWhy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	taskID := fixture.ids[0]
	updateSyncTask(t, fixture.first, fixture.config, taskID, "Mine")
	mine := currentSnapshot(t, fixture.first, fixture.config, taskID)
	forged := forgeReusedOperation(t, fixture.second, fixture.stale, taskID, mine, "Forged")
	syncGit(t, fixture.second.Root, "push", "--quiet", fixture.tasks, forged+":"+taskRefPrefix+taskID)

	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryCorruptData || result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v (category %q), want a corrupt-data refusal", result, err, core.CategoryOf(err))
	}
	for _, want := range []string{"cannot copy the tasks to tasks", "could not be replayed: " + taskID + ": ", mine.Operation.Operations[0].ID, "different content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("MoveTasks() error = %q, want it to contain %q", err, want)
		}
	}
}

// Verification compares content too: a new place that, by the time the run
// verifies it, holds a commit reusing one of the old place's operation IDs for
// other content fails the run as corrupt rather than reporting it verified.
func TestMoveTasksVerificationRefusesAnOperationIDReusedWithOtherContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	taskID := fixture.ids[0]
	updateSyncTask(t, fixture.second, fixture.stale, taskID, "Theirs")
	if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
		t.Fatal(err)
	}
	theirs := currentSnapshot(t, fixture.second, fixture.stale, taskID)
	var forged string
	fixture.first.moveStepHook = func(at string) error {
		if at != "verify" {
			return nil
		}
		// A forgery on the new place: the teammate's operation, with another
		// value, on top of the commit before it.
		syncGit(t, fixture.second.Root, "update-ref", taskRefPrefix+taskID, theirs.Head+"^")
		forged = forgeReusedOperation(t, fixture.second, fixture.stale, taskID, theirs, "Forged")
		syncGit(t, fixture.second.Root, "push", "--quiet", "--force", fixture.tasks, forged+":"+taskRefPrefix+taskID)
		return nil
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryCorruptData || result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v (category %q), want a corrupt-data refusal", result, err, core.CategoryOf(err))
	}
	for _, want := range []string{taskID, theirs.Operation.Operations[0].ID, theirs.Head, forged, "different content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("MoveTasks() error = %q, want it to name %s", err, want)
		}
	}
}

// A conflict met synchronizing with a tasks remote other than origin names
// that remote, in its Remote member and in its wording.
func TestConflictWithATasksRemoteNamesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.second.SetProjectTasksRemote(ctx, fixture.config.TasksRemote); err != nil {
		t.Fatal(err)
	}
	teammate, err := Open(ctx, fixture.second.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teammate.EnsureTasksRemote(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := teammate.Sync(ctx, fixture.config); err != nil {
		t.Fatal(err)
	}
	taskID := fixture.ids[0]
	setSyncTaskDescription(t, fixture.first, fixture.config, taskID, "First's description")
	if _, err := fixture.first.Sync(ctx, fixture.config); err != nil {
		t.Fatal(err)
	}
	setSyncTaskDescription(t, teammate, fixture.config, taskID, "The teammate's description")
	result, err := teammate.Sync(ctx, fixture.config)
	if core.CategoryOf(err) != core.CategoryConflict || len(result.Fetch.Conflicts) != 1 {
		t.Fatalf("Sync() = %+v, %v, want one conflict", result.Fetch.Conflicts, err)
	}
	conflict := result.Fetch.Conflicts[0]
	if conflict.Remote != "tasks" || core.ConflictDetail(conflict) != "the description changed here and on tasks; reapply the wanted text" {
		t.Fatalf("conflict = %+v (%q), want it to name tasks", conflict, core.ConflictDetail(conflict))
	}
}
