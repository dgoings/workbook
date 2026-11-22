package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func describeSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, actor, taskID, description string) core.MutationResult {
	t.Helper()
	result, err := actorService(repo, config, actor).UpdateMutation(actorContext(t, actor), taskID, core.UpdateInput{Description: &description})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// An edit a fetch sets aside on a description conflict is reported from the
// parked tip alone: who made it, when, and the text, to a process that did not
// park it. A clean reconciliation parks a tip too and reports nothing, and so
// does a task that already carries the set-aside text again.
func TestSetAsideEditsReportsWhatAConflictParked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const alice, contributor = "alice@client.example", "sam@client.example"
	board, cli, config := syncRepositories(t)
	contested := createSyncTask(t, board, config, "Contested")
	quiet := createSyncTask(t, board, config, "Quiet")
	describeSyncTask(t, board, config, alice, contested.ID, "Base text")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}

	describeSyncTask(t, cli, config, contributor, contested.ID, "The contributor's text")
	updateSyncTask(t, cli, config, quiet.ID, "Quiet, renamed by the contributor")
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	edit := describeSyncTask(t, board, config, alice, contested.ID, "Alice's text")
	// An edit made after the conflicting one is dropped with it, since replay
	// stops at the conflict, and is reported with it.
	high := core.PriorityHigh
	later, err := actorService(board, config, alice).UpdateMutation(actorContext(t, alice), contested.ID, core.UpdateInput{Priority: &high})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncService(board, config).UpdateMutation(ctx, quiet.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}
	result, err := board.Sync(ctx, config)
	if core.CategoryOf(err) != core.CategoryConflict || len(result.Fetch.Conflicts) != 1 {
		t.Fatalf("Sync() = %v, %#v; want the one description conflict", err, result.Fetch.Conflicts)
	}
	if got := strings.Fields(syncGit(t, board.Root, "for-each-ref", "--format=%(refname)", reconciledRefPrefix+quiet.ID+"/")); len(got) != 1 {
		t.Fatalf("the clean reconciliation parked %v, want one tip", got)
	}

	// A fresh handle, as a restarted server opens one: nothing is remembered.
	reopened := openSyncCloneAt(t, board.Root)
	edits, err := reopened.SetAsideEdits(ctx, config, taskHeads(t, reopened))
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || len(edits[contested.ID]) != 2 {
		t.Fatalf("set-aside edits = %#v, want two, on %s only", edits, contested.ID)
	}
	if after := edits[contested.ID][1]; after.Commit != later.Task.Head || after.Operations[0].Field != "priority" || after.Parked != edits[contested.ID][0].Parked {
		t.Fatalf("second set-aside edit = %#v, want alice's later priority from the same parked tip", after)
	}
	got := edits[contested.ID][0]
	if got.Actor != alice || got.Commit != edit.Task.Head || got.WallTime.IsZero() ||
		!strings.HasPrefix(got.ParkedRef, reconciledRefPrefix+contested.ID+"/") ||
		got.Parked != refValue(t, board, got.ParkedRef) {
		t.Fatalf("set-aside edit = %#v, want alice's commit %s at its parked ref", got, edit.Task.Head)
	}
	if len(got.Operations) != 1 || got.Operations[0].Field != "description" || got.Operations[0].Value != "Alice's text" {
		t.Fatalf("set-aside operations = %#v, want alice's description", got.Operations)
	}
	snapshot, err := reopened.Get(ctx, config, contested.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Task.Description != "The contributor's text" {
		t.Fatalf("description = %q, want the contributor's, which reached origin first", snapshot.State.Task.Description)
	}

	// Copying the text back in is an ordinary edit on top, and the task then
	// carries what was set aside.
	describeSyncTask(t, reopened, config, alice, contested.ID, "Alice's text")
	if edits, err := reopened.SetAsideEdits(ctx, config, taskHeads(t, reopened)); err != nil || len(edits[contested.ID]) != 1 || edits[contested.ID][0].Operations[0].Field != "priority" {
		t.Fatalf("set-aside edits after the text was copied back = %#v, %v; want the priority alone", edits, err)
	}
	if _, err := actorService(reopened, config, alice).UpdateMutation(actorContext(t, alice), contested.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}
	if edits, err := reopened.SetAsideEdits(ctx, config, taskHeads(t, reopened)); err != nil || len(edits) != 0 {
		t.Fatalf("set-aside edits after the text was copied back = %#v, %v; want none", edits, err)
	}
	// Settled for good: a later change to the field is an ordinary edit, and
	// the set-aside text does not come back.
	describeSyncTask(t, reopened, config, contributor, contested.ID, "Something else again")
	if edits, err := reopened.SetAsideEdits(ctx, config, taskHeads(t, reopened)); err != nil || len(edits) != 0 {
		t.Fatalf("set-aside edits once the text changed again = %#v, %v; want none", edits, err)
	}
}

// Dismissing removes the parked tip a person was shown and nothing else: a
// stale or foreign name is refused, and the edits stop being reported.
func TestDismissSetAsideRemovesOnlyTheTipThatWasShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const alice, contributor = "alice@client.example", "sam@client.example"
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Contested")
	other := createSyncTask(t, board, config, "Other")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, contributor, task.ID, "The contributor's text")
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, board, config, alice, task.ID, "Alice's text")
	if _, err := board.Sync(ctx, config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync() error = %v, want the conflict", err)
	}
	edits, err := board.SetAsideEdits(ctx, config, taskHeads(t, board))
	if err != nil || len(edits[task.ID]) != 1 {
		t.Fatalf("set-aside edits = %#v, %v", edits, err)
	}
	shown := edits[task.ID][0]

	for _, refused := range []struct {
		name, taskID, ref, parked string
		want                      core.Category
	}{
		{"another task's ref", other.ID, shown.ParkedRef, shown.Parked, core.CategoryValidation},
		{"a ref outside the namespace", task.ID, taskRefPrefix + task.ID, shown.Parked, core.CategoryValidation},
		{"a tip that moved", task.ID, shown.ParkedRef, shown.Commit + "0", core.CategoryStaleWrite},
		{"a ref that is not there", task.ID, reconciledRefPrefix + task.ID + "/9", shown.Parked, core.CategoryNotFound},
	} {
		if err := board.DismissSetAside(ctx, config, refused.taskID, refused.ref, refused.parked); core.CategoryOf(err) != refused.want {
			t.Errorf("%s: DismissSetAside() error = %v (%s), want %s", refused.name, err, core.CategoryOf(err), refused.want)
		}
	}
	if got := refValue(t, board, shown.ParkedRef); got != shown.Parked {
		t.Fatalf("a refused dismissal moved %s to %q", shown.ParkedRef, got)
	}

	if err := board.DismissSetAside(ctx, config, task.ID, shown.ParkedRef, shown.Parked); err != nil {
		t.Fatal(err)
	}
	if edits, err := board.SetAsideEdits(ctx, config, taskHeads(t, board)); err != nil || len(edits) != 0 {
		t.Fatalf("set-aside edits after dismissal = %#v, %v; want none", edits, err)
	}
	if got := strings.TrimSpace(syncGit(t, board.Root, "for-each-ref", reconciledRefPrefix+task.ID+"/")); got != "" {
		t.Fatalf("parked refs after dismissal = %q, want none", got)
	}
	if err := board.DismissSetAside(ctx, config, task.ID, shown.ParkedRef, shown.Parked); core.CategoryOf(err) != core.CategoryNotFound {
		t.Fatalf("a second dismissal error = %v, want not-found", err)
	}
}

// taskHeads is every task's current tip, as a board that had just refreshed
// its projection would pass them.
func taskHeads(t *testing.T, repo *Repository) map[string]string {
	t.Helper()
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(syncGit(t, repo.Root, "for-each-ref", "--format=%(refname) %(objectname)", taskRefPrefix)), "\n") {
		name, objectID, found := strings.Cut(line, " ")
		if found {
			heads[strings.TrimPrefix(name, taskRefPrefix)] = objectID
		}
	}
	return heads
}
