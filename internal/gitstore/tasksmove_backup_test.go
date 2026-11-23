package gitstore

import (
	"context"
	"fmt"
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

// A checkout that synchronizes with a place the tasks came back to keeps its
// own identity ref on the identity commit at the bottom of that place's
// history, never on the return recorded above it, whether it set up there
// afresh or already held the identity commit.
func TestSyncWithAMovedAndBackRemoteKeepsTheIdentityRefAtTheRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	first, config := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
	first, config = pullSettings(t, first, core.TasksRemoteSetting{})
	if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); err != nil {
		t.Fatalf("MoveTasks() back = %v", err)
	}
	tip := syncGit(t, fixture.origin, "rev-parse", identityRef)
	root := syncGit(t, fixture.origin, "rev-list", "--max-parents=0", identityRef)
	if tip == root {
		t.Fatalf("origin's identity ref is at its root %s, want a return above it", root)
	}

	fresh, freshConfig := checkoutOn(t, fixture.origin, core.TasksRemoteSetting{})
	if _, err := fresh.Sync(ctx, freshConfig); err != nil {
		t.Fatalf("Sync() in a fresh checkout = %v", err)
	}
	for name, repo := range map[string]*Repository{"the fresh checkout": fresh, "the mover": first} {
		if _, err := repo.Sync(ctx, config); err != nil {
			t.Fatalf("Sync() in %s = %v", name, err)
		}
		if got := syncGit(t, repo.Root, "rev-parse", identityRef); got != root {
			t.Fatalf("%s's identity ref = %s, want the identity commit %s, not the return %s", name, got, root, tip)
		}
	}
}

// An identity history of exactly identityChainLimit commits above the
// identity commit is read, and one commit more is refused with the limit's
// own message, not some other failure further on.
func TestIdentityChainLimitReadsTheLongestChainAndRefusesOneMore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, _, config := syncRepositories(t)
	local, found, err := repo.readIdentityRef(ctx, identityRef)
	if err != nil || !found {
		t.Fatalf("readIdentityRef() = %v, %v", found, err)
	}
	marker, err := repo.writeTasksMovedCommit(ctx, local, TasksMoved{
		Format: tasksMovedFormat, Version: tasksMovedVersion, ProjectID: config.ProjectID,
		Remote: "tasks", URL: "https://example.test/tasks.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	markerTree := syncGit(t, repo.Root, "rev-parse", marker+"^{tree}")
	returnTree := syncGit(t, repo.Root, "rev-parse", local.Head+"^{tree}")
	tips := []string{local.Head, marker}
	for len(tips) <= identityChainLimit+1 {
		tree, message := returnTree, tasksReturnedCommitMessage
		if len(tips)%2 == 1 {
			tree, message = markerTree, tasksMovedCommitMessage
		}
		commit, err := repo.writeIdentityChainCommit(ctx, tree, tips[len(tips)-1], message)
		if err != nil {
			t.Fatal(err)
		}
		tips = append(tips, commit)
	}

	longest := remoteIdentityRefFor("longest")
	syncGit(t, repo.Root, "update-ref", longest, tips[identityChainLimit])
	record, found, err := repo.readIdentityRef(ctx, longest)
	if err != nil || !found || record.Root != local.Head || record.Moved != nil {
		t.Fatalf("readIdentityRef() of %d commits above the identity commit = %+v, %v, %v, want a live place",
			identityChainLimit, record, found, err)
	}
	tooLong := remoteIdentityRefFor("toolong")
	syncGit(t, repo.Root, "update-ref", tooLong, tips[identityChainLimit+1])
	_, _, err = repo.readIdentityRef(ctx, tooLong)
	want := fmt.Sprintf("carries more than %d moves and returns above the project's identity commit", identityChainLimit)
	if core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(fmt.Sprint(err), want) {
		t.Fatalf("readIdentityRef() of %d commits above the identity commit = %v, want %q", identityChainLimit+1, err, want)
	}
}
