package gitstore

import (
	"context"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A replayed pack earns a commit only when it changes what an operator can
// see, and a due date is something they can see. Here the fetching clone's
// only change is a date, made while the other clone renamed the task: the
// replay has to record it on top of the rename. A comparison that left the
// date out would call the replayed state the fetched one and drop the pack,
// losing the date without a word.
func TestFetchReplaysADueDateThatIsTheOnlyLocalChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Renamed upstream, dated here")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}

	renamed := "Renamed upstream"
	if _, err := syncService(first, config).UpdateMutation(ctx, task.ID, core.UpdateInput{Title: &renamed}); err != nil {
		t.Fatal(err)
	}
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)
	due := "2031-05-06"
	if _, err := syncService(second, config).UpdateMutation(ctx, task.ID, core.UpdateInput{Due: &due}); err != nil {
		t.Fatal(err)
	}

	result, err := second.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(dated locally) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)
	snapshot, err := second.Get(ctx, config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head == remoteTip {
		t.Fatalf("reconciled tip is the fetched tip %s: the replayed date earned no commit", remoteTip)
	}
	if snapshot.State.Task.Due != due || snapshot.State.Task.Title != renamed {
		t.Fatalf("reconciled task = (%q, due %q), want (%q, due %q)", snapshot.State.Task.Title, snapshot.State.Task.Due, renamed, due)
	}
	if !mergeBaseIsAncestor(t, second.Root, remoteTip, snapshot.Head) {
		t.Fatalf("reconciled tip %s does not descend from the fetched tip %s", snapshot.Head, remoteTip)
	}
}
