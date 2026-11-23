package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// operationsAt is every operation ID the history at ref in a bare
// repository holds, counting the operations a checkpoint lists as compacted.
func operationsAt(t *testing.T, bare, ref string) map[string]bool {
	t.Helper()
	held := map[string]bool{}
	for _, commit := range strings.Fields(syncGit(t, bare, "rev-list", ref)) {
		pack, err := core.DecodeOperationPack([]byte(syncGit(t, bare, "cat-file", "blob", commit+":operation.json")))
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range pack.Operations {
			held[operation.ID] = true
			if operation.Checkpoint != nil {
				for _, id := range operation.Checkpoint.CompactedOperations {
					held[id] = true
				}
			}
		}
	}
	return held
}

// A teammate who publishes to the stale copy between the move's backup and
// its leased push is never pushed over: the lease fails, the move reads the
// place again, writes a second bundle holding the new tip, and replaces only
// that. Both bundles verify, and every operation either tip held, both of the
// teammate's edits among them, is at the returned-to place afterwards.
func TestMoveTasksBackRebundlesATipThatMovesBeforeItsPush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	task := fixture.ids[0]
	teammate := fixture.second
	first, config := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
	compactLocally(t, first, config, task)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}

	ref := taskRefPrefix + task
	publishLate := func(title string) string {
		updateSyncTask(t, teammate, fixture.stale, task, title)
		syncGit(t, teammate.Root, "push", "--quiet", "--no-verify", "origin", ref+":"+ref)
		return syncGit(t, fixture.origin, "rev-parse", ref)
	}
	firstTip := publishLate("Late edit before the move back")

	first, config = pullSettings(t, first, core.TasksRemoteSetting{})
	var secondTip string
	first.leasedPushHook = func() {
		if secondTip == "" {
			secondTip = publishLate("Late edit between the backup and the push")
		}
	}
	result, err := first.MoveTasks(ctx, config, MoveTasksOptions{RunID: "01MOVEBACKRUN0000000000000"})
	first.leasedPushHook = nil
	if err != nil {
		t.Fatalf("MoveTasks() back = %v", err)
	}
	if !result.Verified || len(result.Bundles) != 2 || len(result.Replaced) != 1 || result.Replaced[0] != task {
		t.Fatalf("MoveTasks() back = %+v, want one task replaced after two bundles", result)
	}
	for index, tip := range []string{firstTip, secondTip} {
		bundle := result.Bundles[index]
		syncGit(t, first.Root, "bundle", "verify", "--quiet", bundle)
		heads := syncGit(t, first.Root, "bundle", "list-heads", bundle)
		if !strings.Contains(heads, tip+" "+remoteTaskRefPrefix+task) {
			t.Fatalf("bundle %d heads = %q, want the tip %s it was written for", index+1, heads, tip)
		}
	}

	after := operationsAt(t, fixture.origin, ref)
	for _, tip := range []string{firstTip, secondTip} {
		before := operationsAt(t, first.Root, tip)
		if len(before) < 2 {
			t.Fatalf("the copy at %s holds %d operations, want the create and a late edit at least", tip, len(before))
		}
		for id := range before {
			if !after[id] {
				t.Errorf("origin lost operation %s of the copy at %s", id, tip)
			}
		}
	}
	if snapshot := currentSnapshot(t, first, config, task); snapshot.State.Task.Title != "Late edit between the backup and the push" {
		t.Fatalf("title = %q, want the last late edit", snapshot.State.Task.Title)
	}
	if got, want := syncGit(t, fixture.origin, "rev-parse", ref), syncGit(t, first.Root, "rev-parse", ref); got != want {
		t.Fatalf("origin holds %s at %s, want this clone's %s", task, got, want)
	}
}
