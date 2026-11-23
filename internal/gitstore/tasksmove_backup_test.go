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

// A move back stopped after it parked a late edit to a deleted, compacted
// task and before the push that replaces the copy holding it is finished by
// running it again, which parks nothing more and reports the edit once, under
// the ref it parked, with the conflict refusal; the run after that is quiet.
func TestMoveTasksBackStoppedBeforeReplacingASetAsideCopyReportsItOnce(t *testing.T) {
	t.Parallel()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	task := fixture.ids[0]
	first, config := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
	deleteSyncTask(t, first, config, task)
	compactLocally(t, first, config, task)
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	ref := taskRefPrefix + task
	updateSyncTask(t, fixture.second, fixture.stale, task, "Late edit before the move back")
	syncGit(t, fixture.second.Root, "push", "--quiet", "--no-verify", "origin", ref+":"+ref)
	stale := syncGit(t, fixture.origin, "rev-parse", ref)

	first, config = pullSettings(t, first, core.TasksRemoteSetting{})
	ctx, cancel := context.WithCancel(context.Background())
	first.leasedPushHook = cancel
	if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); err == nil {
		t.Fatal("MoveTasks() stopped before its push = nil, want the stop")
	}
	first.leasedPushHook = nil
	parked := syncGit(t, first.Root, "for-each-ref", "--format=%(refname) %(objectname)", reconciledRefPrefix+task+"/")
	if parked != reconciledRefPrefix+task+"/0 "+stale {
		t.Fatalf("parked refs after the stop = %q, want the stale copy parked once", parked)
	}
	if got := syncGit(t, fixture.origin, "rev-parse", ref); got != stale {
		t.Fatalf("origin holds %s at %s after the stop, want the copy it had", task, got)
	}

	result, err := first.MoveTasks(context.Background(), config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryConflict || !strings.Contains(err.Error(), "it was deleted and compacted at tasks") ||
		!strings.Contains(err.Error(), reconciledRefPrefix+task+"/0") {
		t.Fatalf("MoveTasks() rerun = %v, want the set-aside refusal naming the parked ref", err)
	}
	if !result.Verified || len(result.SetAside) != 1 || result.SetAside[0].ParkedRef != reconciledRefPrefix+task+"/0" ||
		len(result.Replaced) != 1 {
		t.Fatalf("MoveTasks() rerun = %+v", result)
	}
	if got := syncGit(t, first.Root, "for-each-ref", "--format=%(refname) %(objectname)", reconciledRefPrefix+task+"/"); got != parked {
		t.Fatalf("parked refs after the rerun = %q, want %q", got, parked)
	}
	if got, want := syncGit(t, fixture.origin, "rev-parse", ref), syncGit(t, first.Root, "rev-parse", ref); got != want {
		t.Fatalf("origin holds %s at %s, want the checkpoint %s", task, got, want)
	}
	result, err = first.MoveTasks(context.Background(), config, MoveTasksOptions{})
	if err != nil || len(result.SetAside) != 0 || len(result.Bundles) != 0 {
		t.Fatalf("MoveTasks() third run = %+v, %v, want quiet", result, err)
	}
}
