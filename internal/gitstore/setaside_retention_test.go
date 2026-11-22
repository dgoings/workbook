package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

const setAsideAlice, setAsideContributor = "alice@client.example", "sam@client.example"

// setAsideConflict sets aside alice's description on board: the contributor
// publishes a different description from cli first, and board's sync stops on
// the conflict.
func setAsideConflict(t *testing.T, board, cli *Repository, config core.ProjectConfig, taskID, theirs, ours string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, setAsideContributor, taskID, theirs)
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, board, config, setAsideAlice, taskID, ours)
	if _, err := board.Sync(ctx, config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync() error = %v, want the description conflict", err)
	}
}

// sweepParked runs the retention sweep a board's background sync runs after
// every round.
func sweepParked(t *testing.T, repo *Repository, config core.ProjectConfig) {
	t.Helper()
	if _, err := repo.PruneParkedRefs(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}

// setAsideTexts is the description of every edit the board shows set aside
// for taskID, oldest first.
func setAsideTexts(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) []string {
	t.Helper()
	edits, err := setAsideEditsOf(repo, config, taskHeads(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, edit := range edits[taskID] {
		for _, operation := range edit.Operations {
			texts = append(texts, operation.Field+"="+operation.Value+operation.Body)
		}
	}
	return texts
}

// Every reconciliation parks the tip it replaces, including one that replays
// cleanly. Those clean tips are what the retention bound retires, oldest
// first; an edit the task lacks is never pushed out by them.
func TestSetAsideEditOutlivesCleanReconciliations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Contested")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	setAsideConflict(t, board, cli, config, task.ID, "The contributor's text", "Alice's text")

	priorities := []core.Priority{core.PriorityHigh, core.PriorityLow}
	for round := range maxParkedRefsPerTask + 2 {
		if _, err := cli.Fetch(ctx, config); err != nil {
			t.Fatal(err)
		}
		setSyncTaskPriority(t, cli, config, task.ID, priorities[round%2])
		if _, err := cli.Push(ctx, config); err != nil {
			t.Fatal(err)
		}
		updateSyncTask(t, board, config, task.ID, fmt.Sprintf("Title from the board, round %d", round))
		if result, err := board.Sync(ctx, config); err != nil {
			t.Fatalf("round %d: Sync() = %v, %#v; want a clean reconciliation", round, err, result.Fetch.Conflicts)
		}
		sweepParked(t, board, config)
		if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "description=Alice's text" {
			t.Fatalf("round %d: the board shows %q set aside, want alice's text", round, got)
		}
	}
	// One parked tip holds alice's text, and the bound keeps the newest clean
	// ones beside it.
	if got := parkedRefNames(t, board, task.ID); len(got) != 1+maxParkedRefsPerTask {
		t.Fatalf("parked refs = %v, want alice's and %d clean ones", got, maxParkedRefsPerTask)
	}
}

// However many conflicts set aside a person's edits on one task, each stays
// on show until somebody deals with it.
func TestEverySetAsideEditOnOneTaskStaysShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Contested")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	var want []string
	for round := 1; round <= 5; round++ {
		setAsideConflict(t, board, cli, config, task.ID, fmt.Sprintf("Contributor, round %d", round), fmt.Sprintf("Alice, round %d", round))
		sweepParked(t, board, config)
		// The board's own next edit of the task runs the same sweep.
		updateSyncTask(t, board, config, task.ID, fmt.Sprintf("Contested, round %d", round))
		if _, err := board.Sync(ctx, config); err != nil {
			t.Fatal(err)
		}
		want = append(want, fmt.Sprintf("description=Alice, round %d", round))
		if got := setAsideTexts(t, board, config, task.ID); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("after conflict %d the board shows %q, want %q", round, got, want)
		}
	}
}

// Adopting another clone's compaction of a task keeps a parked tip that holds
// an edit the task lacks, and the board goes on showing that edit and nothing
// of the compacted history. A parked tip with nothing left in it goes, as it
// always did.
func TestAdoptingACompactionKeepsASetAsideEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	compactor, board, config := syncRepositories(t)
	task := sharedSyncTask(t, compactor, board, config, "Deleted later")
	settled := currentSnapshot(t, board, config, task.ID)
	syncGit(t, board.Root, "update-ref", reconciledRefPrefix+task.ID+"/7", settled.Head)
	setAsideConflict(t, board, compactor, config, task.ID, "The compactor's text", "Alice's text")

	if _, err := syncService(compactor, config).DeleteMutation(ctx, task.ID, core.DeleteInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := compactor.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := compactor.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	compacted := compactLocally(t, compactor, config, task.ID)
	publishCheckpoint(t, compactor, config, compacted)

	result, err := board.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncAdopted)
	if got := refValue(t, board, taskRefPrefix+task.ID); got != compacted.checkpoint.Head {
		t.Fatalf("board holds %s, want origin's checkpoint %s", got, compacted.checkpoint.Head)
	}
	sweepParked(t, board, config)
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "description=Alice's text" {
		t.Fatalf("the board shows %q set aside on the compacted task, want alice's text alone", got)
	}
	if got := parkedRefNames(t, board, task.ID); len(got) != 1 || got[0] == reconciledRefPrefix+task.ID+"/7" {
		t.Fatalf("parked refs = %v, want only the one holding alice's text", got)
	}

	edits, err := setAsideEditsOf(board, config, taskHeads(t, board))
	if err != nil {
		t.Fatal(err)
	}
	shown := edits[task.ID][0]
	if err := board.DismissSetAside(ctx, config, task.ID, shown.ParkedRef, shown.Parked); err != nil {
		t.Fatal(err)
	}
	assertNoParkedRefs(t, board, task.ID)
}

// An edit counts as settled only when a state the task has had since the edit
// was set aside carries it. Origin's own earlier states never count: here the
// contributor wrote alice's very text and then changed it again before alice's
// edit reached the conflict.
func TestSetAsideEditMatchingAnEarlierValueOnOriginIsShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Shipping")
	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Ship sometime")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, setAsideContributor, task.ID, "Ship on Friday")
	setAsideConflict(t, board, cli, config, task.ID, "Ship on Monday", "Ship on Friday")

	if snapshot := currentSnapshot(t, board, config, task.ID); snapshot.State.Task.Description != "Ship on Monday" {
		t.Fatalf("description = %q, want the contributor's last", snapshot.State.Task.Description)
	}
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "description=Ship on Friday" {
		t.Fatalf("the board shows %q set aside, want alice's Friday", got)
	}
}

// When the parked tip shows where origin stood as the edit was set aside —
// an earlier edit of alice's replayed onto it — a person saving the set-aside
// text again settles it for good, and a later change to the field does not
// bring it back.
func TestSetAsideEditSavedAgainStaysSettled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Shipping")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, setAsideContributor, task.ID, "Ship on Monday")
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	high := core.PriorityHigh
	if _, err := actorService(board, config, setAsideAlice).UpdateMutation(actorContext(t, setAsideAlice), task.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Ship on Friday")
	if _, err := board.Sync(ctx, config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync() error = %v, want the description conflict", err)
	}
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "description=Ship on Friday" {
		t.Fatalf("the board shows %q set aside, want alice's Friday", got)
	}

	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Ship on Friday")
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 0 {
		t.Fatalf("the board shows %q set aside once alice saved it again, want nothing", got)
	}
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, setAsideContributor, task.ID, "Ship on Sunday")
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 0 {
		t.Fatalf("the board shows %q set aside after a later change, want nothing", got)
	}
}

// A set-aside comment settles when the task holds the same text again from the
// same person, which a copy back in gives a new ID. The same text from
// somebody else does not settle it.
func TestSetAsideCommentSettlesWhenItsAuthorPostsItAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const bob = "bob@client.example"
	board, cli, config := syncRepositories(t)
	task := createSyncTask(t, board, config, "Discussed")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, cli, config, setAsideContributor, task.ID, "The contributor's text")
	if _, err := cli.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Alice's text")
	comment := func(actor, body string) {
		t.Helper()
		if _, err := actorService(board, config, actor).CommentAddMutation(actorContext(t, actor), task.ID, core.CommentAddInput{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	comment(setAsideAlice, "Looks right to me")
	if _, err := board.Sync(ctx, config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync() error = %v, want the description conflict", err)
	}
	want := []string{"description=Alice's text", "=Looks right to me"}
	if got := setAsideTexts(t, board, config, task.ID); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the board shows %q set aside, want %q", got, want)
	}

	comment(bob, "Looks right to me")
	if got := setAsideTexts(t, board, config, task.ID); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("after bob posted the text the board shows %q, want %q still", got, want)
	}
	comment(setAsideAlice, "Looks right to me")
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("after alice posted the text again the board shows %q, want the description alone", got)
	}
}

// A parked ref that cannot be read as one of its task's set-aside edits is
// skipped and named, and every other task's set-aside edit is still reported.
func TestUnreadableParkedRefHidesNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	board, cli, config := syncRepositories(t)
	contested := createSyncTask(t, board, config, "Contested")
	victim := createSyncTask(t, board, config, "Victim")
	other := createSyncTask(t, board, config, "Other")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	setAsideConflict(t, board, cli, config, contested.ID, "The contributor's text", "Alice's text")

	head := refValue(t, board, taskRefPrefix+victim.ID)
	blob := func(contents string) string {
		return strings.TrimSpace(syncGitInput(t, board.Root, []byte(contents), "hash-object", "-w", "--stdin"))
	}
	tree := func(entries string) string {
		return strings.TrimSpace(syncGitInput(t, board.Root, []byte(entries), "mktree"))
	}
	state := strings.TrimSpace(syncGit(t, board.Root, "rev-parse", head+":state.json"))
	malformed := strings.TrimSpace(syncGit(t, board.Root, "commit-tree",
		tree(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", blob("{"), state)), "-p", head, "-m", "malformed"))
	unrelated := strings.TrimSpace(syncGit(t, board.Root, "commit-tree",
		tree(fmt.Sprintf("100644 blob %s\tREADME\n", blob("not a task\n"))), "-m", "unrelated root"))
	missing := strings.TrimSpace(syncGit(t, board.Root, "commit-tree",
		tree(fmt.Sprintf("100644 blob %s\tREADME\n", blob("soon gone\n"))), "-p", head, "-m", "to be lost"))

	bad := map[string]string{
		reconciledRefPrefix + victim.ID + "/1": malformed,
		reconciledRefPrefix + victim.ID + "/2": unrelated,
		reconciledRefPrefix + victim.ID + "/3": refValue(t, board, taskRefPrefix+other.ID),
		reconciledRefPrefix + victim.ID + "/4": missing,
	}
	for name, commit := range bad {
		syncGit(t, board.Root, "update-ref", name, commit)
	}
	gitDir := strings.TrimSpace(syncGit(t, board.Root, "rev-parse", "--absolute-git-dir"))
	if err := os.Remove(filepath.Join(gitDir, "objects", missing[:2], missing[2:])); err != nil {
		t.Fatalf("remove the commit object to lose it: %v", err)
	}

	report, err := openSyncCloneAt(t, board.Root).SetAsideEdits(ctx, config, taskHeads(t, board))
	if err != nil {
		t.Fatalf("SetAsideEdits() error = %v, want the readable edits", err)
	}
	if len(report.Edits) != 1 || len(report.Edits[contested.ID]) != 1 || report.Edits[contested.ID][0].Operations[0].Value != "Alice's text" {
		t.Fatalf("set-aside edits = %#v, want alice's text on %s alone", report.Edits, contested.ID)
	}
	if len(report.Unreadable) != len(bad) {
		t.Fatalf("unreadable = %#v, want the %d bad refs", report.Unreadable, len(bad))
	}
	for _, skipped := range report.Unreadable {
		if bad[skipped.Ref] != skipped.Commit || skipped.TaskID != victim.ID || skipped.Reason == "" {
			t.Errorf("unreadable entry %#v does not name its ref, commit and reason", skipped)
		}
		t.Logf("%s: %s", skipped.Ref, skipped.Reason)
	}
	// None of them is retired automatically: nobody can say what they hold.
	updateSyncTask(t, board, config, victim.ID, "Victim, renamed")
	sweepParked(t, board, config)
	if got := parkedRefNames(t, board, victim.ID); len(got) != len(bad) {
		t.Fatalf("parked refs after the sweep = %v, want all %d kept", got, len(bad))
	}
}

// After the board adopts another clone's compaction nothing says which
// comments were there before the two sides parted. A set-aside comment whose
// text its author also wrote long before the conflict is then still shown,
// and its parked ref kept: nobody copied it back.
func TestAdoptingACompactionKeepsASetAsideCommentItsAuthorWroteBefore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	compactor, board, config := syncRepositories(t)
	task := sharedSyncTask(t, compactor, board, config, "Old comment")
	comment := func(body string) {
		t.Helper()
		if _, err := actorService(board, config, setAsideAlice).CommentAddMutation(actorContext(t, setAsideAlice), task.ID, core.CommentAddInput{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	comment("Done")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := compactor.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, compactor, config, setAsideContributor, task.ID, "Compactor text")
	if _, err := compactor.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Alice text")
	comment("Done")
	if _, err := board.Sync(ctx, config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync() error = %v, want a conflict", err)
	}
	// Alice deals with the description alone.
	describeSyncTask(t, board, config, setAsideAlice, task.ID, "Alice text")
	if _, err := board.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "=Done" {
		t.Fatalf("before the compaction the board shows %q set aside, want the comment alone", got)
	}

	done := core.Status("done")
	if _, err := compactor.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := syncService(compactor, config).UpdateMutation(ctx, task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatal(err)
	}
	if _, err := compactor.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	compacted := compactLocally(t, compactor, config, task.ID)
	publishCheckpoint(t, compactor, config, compacted)
	if result, err := board.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	sweepParked(t, board, config)
	if got := setAsideTexts(t, board, config, task.ID); len(got) != 1 || got[0] != "=Done" {
		t.Fatalf("after adopting the compaction the board shows %q set aside, want the comment nobody copied back", got)
	}
	if got := parkedRefNames(t, board, task.ID); len(got) != 1 {
		t.Fatalf("parked refs = %v, want the one holding the comment", got)
	}
}
