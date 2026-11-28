package gitstore

import (
	"context"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// A move carried a teammate's edit S with a merge M, so the new place holds
// [field.set S, description.merge M], and the tasks moved back. At the old
// place the mover then replaced the line S changed, marked the task Done and
// compacted it: the checkpoint lists S, and not M, which only the new place
// holds. Moving the tasks forward again onto the new place's older copy
// counts that pack as held, since M is derived from S, so nothing of it is
// replayed: the later text stands, no operation is recorded after a
// checkpoint that lists it, and the copy is leased over. A teammate who read
// the new place's copy and never edited the task adopts the checkpoint with
// nothing to replay, rather than being told of an unpushed change.
func TestMoveTasksForwardOntoACarriedMergeAfterACompaction(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			moveForwardOntoACarriedMergeAfterACompaction(t, objectFormat)
		})
	}
}

func moveForwardOntoACarriedMergeAfterACompaction(t *testing.T, objectFormat string) {
	ctx := context.Background()
	fixture := newMoveFixture(t, objectFormat)
	task, _, carried := carryADescriptionStraggler(t, fixture, false)
	ref := taskRefPrefix + task
	newPlace := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}

	// A teammate on the new settings reads [S, M] there and never edits it.
	reader, readerConfig := checkoutOn(t, fixture.origin, newPlace)
	if got := refValue(t, reader, ref); got != carried {
		t.Fatalf("the reader holds %s, want the new place's tip %s", got, carried)
	}

	first, config := pullSettings(t, fixture.first, core.TasksRemoteSetting{})
	for run := range 2 {
		if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); err != nil {
			t.Fatalf("MoveTasks() back, run %d = %v", run+1, err)
		}
	}
	later := sixLines(map[int]string{2: "the mover's later line 2", 5: "the mover's line 5"})
	setSyncTaskDescription(t, first, config, task, later)
	requireNothingSetAside(t, first, config, task)
	done := core.StatusDone
	if _, err := syncService(first, config).UpdateMutation(ctx, task, core.UpdateInput{Status: &done}); err != nil {
		t.Fatal(err)
	}
	compacted := compactLocally(t, first, config, task)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() publishing the checkpoint = %v", err)
	}
	if got := syncGit(t, fixture.origin, "rev-parse", ref); got != compacted.checkpoint.Head {
		t.Fatalf("origin holds %s, want the checkpoint %s", got, compacted.checkpoint.Head)
	}

	// The reader pulls the settings that name the old place again.
	reader, readerConfig = pullSettings(t, reader, core.TasksRemoteSetting{})
	requireAdoptedWithNothingToReplay(t, reader, readerConfig, task, compacted.checkpoint.Head, later)

	first, config = pullSettings(t, first, newPlace)
	result, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
	if err != nil {
		t.Fatalf("MoveTasks() forward = %v", err)
	}
	if !result.Verified || result.Stragglers != nil || len(result.SetAside) != 0 {
		t.Fatalf("MoveTasks() forward = %+v, want it verified with nothing carried or set aside", result)
	}
	tip := syncGit(t, fixture.tasks, "rev-parse", ref)
	if tip != compacted.checkpoint.Head {
		t.Fatalf("the new place holds %s, want the checkpoint %s with nothing replayed onto it", tip, compacted.checkpoint.Head)
	}
	if got := currentSnapshotAt(t, first, config, task, tip).State.Task.Description; got != later {
		t.Fatalf("description at the new place = %q, want the later edit %q", got, later)
	}
	requireValidTaskHistory(t, first, config, task, tip)
	requireNothingRecordedAfterItsCheckpoint(t, first, config, task, tip)
	requireNothingSetAside(t, first, config, task)

	again, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
	if err != nil || !again.Verified || again.Stragglers != nil || len(again.SetAside) != 0 ||
		len(again.TaskWarnings) != 0 || len(again.Replaced) != 0 {
		t.Fatalf("MoveTasks() forward again = %+v, %v, want quiet", again, err)
	}
	if got := syncGit(t, fixture.tasks, "rev-parse", ref); got != tip {
		t.Fatalf("the new place moved to %s on the second run, want %s", got, tip)
	}

	reader, readerConfig = pullSettings(t, reader, newPlace)
	requireAdoptedWithNothingToReplay(t, reader, readerConfig, task, tip, later)
}

// requireAdoptedWithNothingToReplay synchronizes repo and requires it to end
// on head with description, with no unpushed change reported for taskID.
func requireAdoptedWithNothingToReplay(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, head, description string) {
	t.Helper()
	result, err := repo.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync() = %v; result = %+v", err, result)
	}
	for _, item := range append(result.Fetch.Tasks, result.Push.Tasks...) {
		if item.TaskID == taskID && (item.Status == SyncCompactedOnOrigin || item.Status == SyncAdoptedReplayed) {
			t.Fatalf("Sync() reports %s for %s (%s), want it adopted with nothing to replay", item.Status, taskID, item.Detail)
		}
	}
	if got := refValue(t, repo, taskRefPrefix+taskID); got != head {
		t.Fatalf("%s holds %s, want %s", taskID, got, head)
	}
	if got := currentSnapshot(t, repo, config, taskID).State.Task.Description; got != description {
		t.Fatalf("description = %q, want %q", got, description)
	}
}

// requireNothingRecordedAfterItsCheckpoint fails when a commit after the
// checkpoint at the root of head's chain records an operation that checkpoint
// lists as compacted: the double record the held rule exists to prevent.
func requireNothingRecordedAfterItsCheckpoint(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, head string) {
	t.Helper()
	chains, err := repo.completeTaskChains(context.Background(), config, []TaskHead{{TaskID: taskID, ObjectID: head}})
	if err != nil || chains[0] == nil {
		t.Fatalf("read the history of %s at %s: %v", taskID, head, err)
	}
	checkpoint, ok := chains[0][0].Operation.Checkpoint()
	if !ok {
		t.Fatalf("the history of %s at %s is not rooted at a checkpoint", taskID, head)
	}
	listed := operationIDSet(checkpoint.CompactedOperations)
	for _, commit := range chains[0][1:] {
		for _, operation := range commit.Operation.Operations {
			if _, found := listed[operation.ID]; found {
				t.Fatalf("commit %s records operation %s, which its root checkpoint lists", commit.ObjectID, operation.ID)
			}
		}
	}
}

// requireNothingSetAside fails when repo reports an edit to taskID set aside:
// the carried pack its parked tip holds is the edit and a merge derived from
// it, and the task's history records the edit.
func requireNothingSetAside(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) {
	t.Helper()
	report, err := repo.SetAsideEdits(context.Background(), config, taskHeads(t, repo))
	if err != nil {
		t.Fatalf("SetAsideEdits() = %v", err)
	}
	if edits := report.Edits[taskID]; len(edits) != 0 {
		t.Fatalf("set aside for %s = %+v, want nothing", taskID, edits)
	}
}
