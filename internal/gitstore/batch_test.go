package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// A reader that knows how large an object may be refuses a larger one from the
// size in Git's header. It allocates nothing for it and still consumes the
// record, so the object after it is read as usual.
func TestReadBatchObjectWithinSkipsAnObjectOverItsLimit(t *testing.T) {
	t.Parallel()
	treeID, blobID := strings.Repeat("a", 40), strings.Repeat("b", 40)
	tree := bytes.Repeat([]byte{'t'}, 1000)
	blob := []byte("ten bytes\n")
	if len(blob) != 10 {
		t.Fatalf("the test's blob is %d bytes", len(blob))
	}
	stream := func() *bufio.Reader {
		var records bytes.Buffer
		fmt.Fprintf(&records, "%s tree %d\n%s\n%s blob %d\n%s\n", treeID, len(tree), tree, blobID, len(blob), blob)
		return bufio.NewReader(&records)
	}

	reader := stream()
	first, err := readBatchObjectWithin(reader, 100)
	if err != nil {
		t.Fatalf("readBatchObjectWithin(tree) error = %v", err)
	}
	if first.refused == nil || first.contents != nil {
		t.Fatalf("the 1000 byte tree = refused %v with %d bytes read, want it refused and nothing read",
			first.refused, len(first.contents))
	}
	if first.objectID != treeID || first.kind != "tree" || first.size != 1000 {
		t.Fatalf("the refused tree = %s %s of %d bytes, want %s tree of 1000 bytes", first.objectID, first.kind, first.size, treeID)
	}
	if got := core.CategoryOf(first.refused); got != core.CategoryCorruptData {
		t.Fatalf("refusal category = %q, want %q; error = %v", got, core.CategoryCorruptData, first.refused)
	}
	for _, fragment := range []string{treeID, "1000 bytes", "100 bytes"} {
		if !strings.Contains(first.refused.Error(), fragment) {
			t.Fatalf("refusal %q does not mention %q", first.refused, fragment)
		}
	}
	second, err := readBatchObjectWithin(reader, 100)
	if err != nil {
		t.Fatalf("readBatchObjectWithin(blob) error = %v", err)
	}
	if second.refused != nil || second.objectID != blobID || second.kind != "blob" || !bytes.Equal(second.contents, blob) {
		t.Fatalf("the object after the refused one = %+v, want the blob's 10 bytes", second)
	}

	// An object exactly at the limit is read, and the reader with no limit of
	// its own still reads up to the ceiling for one object.
	atLimit, err := readBatchObjectWithin(stream(), 1000)
	if err != nil || atLimit.refused != nil || !bytes.Equal(atLimit.contents, tree) {
		t.Fatalf("readBatchObjectWithin(limit 1000) = refused %v, %v; want the tree read", atLimit.refused, err)
	}
	whole, err := readBatchObject(stream())
	if err != nil || whole.refused != nil || !bytes.Equal(whole.contents, tree) {
		t.Fatalf("readBatchObject() = refused %v, %v; want the tree read", whole.refused, err)
	}
}

func TestReadTaskHeadsPartialConsumesAllRecordsAfterMissingMiddleHead(t *testing.T) {
	t.Parallel()
	repository, config := writeRepository(t)
	first, _, _ := writeRoot(t, repository, config)
	missingObject := gitOutputWithInput(t, repository, []byte("missing task tip"), "hash-object", "-w", "--stdin")
	third := writeIndependentRoot(
		t,
		repository,
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Third task",
	)

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	results, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{
		{TaskID: first.Operation.TaskID, ObjectID: first.Head},
		{TaskID: "WB-01K0M6B8A4FTT8C39MXXYTW7C5", ObjectID: missingObject},
		{TaskID: third.Operation.TaskID, ObjectID: third.Head},
	})
	if err != nil {
		t.Fatalf("readTaskHeadsPartial() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Err != nil || !reflect.DeepEqual(results[0].Snapshot, first) {
		t.Fatalf("first result = %#v, want valid first snapshot", results[0])
	}
	if got, want := core.CategoryOf(results[1].Err), core.CategoryCorruptData; got != want {
		t.Fatalf("middle result category = %q, want %q; error = %v", got, want, results[1].Err)
	}
	if results[2].Err != nil || !reflect.DeepEqual(results[2].Snapshot, third) {
		t.Fatalf("third result = %#v, want valid third snapshot after missing middle head", results[2])
	}
	if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
		t.Fatalf("cat-file --batch commands = %d, want 1; commands = %v", got, commands)
	}
}

func TestReadTaskHeadsPartialRejectsMalformedBatchFramingForAllHeads(t *testing.T) {
	t.Parallel()
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)
	fakeGit := filepath.Join(t.TempDir(), "git")
	testenv.WriteExecutable(t, fakeGit, []byte("#!/bin/sh\nprintf 'not a Git batch header\\n'\n"))
	repository.gitPath = fakeGit

	_, err := repository.readTaskHeadsPartial(context.Background(), config, []TaskHead{{
		TaskID:   snapshot.Operation.TaskID,
		ObjectID: snapshot.Head,
	}})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("readTaskHeadsPartial() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestReadTaskHeadsPartialReadsRepeatedTaskHistoryCommitsInOneBatch(t *testing.T) {
	t.Parallel()
	// Mutation caught: rejecting repeated task IDs when batching commits from one history.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 2300, 3)
	heads := []TaskHead{
		{TaskID: history[0].Operation.TaskID, ObjectID: history[0].Head},
		{TaskID: history[1].Operation.TaskID, ObjectID: history[1].Head},
		{TaskID: history[2].Operation.TaskID, ObjectID: history[2].Head},
	}

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	results, err := repository.readTaskHeadsPartial(context.Background(), config, heads)
	if err != nil {
		t.Fatalf("readTaskHeadsPartial() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for i := range history {
		if results[i].Err != nil || !reflect.DeepEqual(results[i].Snapshot, history[i]) {
			t.Fatalf("results[%d] = %#v, want history snapshot %#v", i, results[i], history[i])
		}
	}
	if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
		t.Fatalf("cat-file --batch commands = %d, want 1; commands = %v", got, commands)
	}
}

// compactedAdvance is one task compacted in place: the tip a projection last
// saw, and the checkpoint built to replace it.
type compactedAdvance struct {
	repo       *Repository
	config     core.ProjectConfig
	taskID     string
	tip        core.Snapshot
	checkpoint core.Snapshot
}

// newCompactedAdvance builds a checkpoint for a two-commit task. edit, when
// set, changes the input first, which is how a test forges a bad summary.
func newCompactedAdvance(t *testing.T, edit func(*CheckpointInput)) compactedAdvance {
	t.Helper()
	repo, config := writeRepository(t)
	task := createSyncTask(t, repo, config, "Compact me")
	updateSyncTask(t, repo, config, task.ID, "Compacted title")
	tip := currentSnapshot(t, repo, config, task.ID)
	input := checkpointInputFor(t, repo, config, tip, false)
	if edit != nil {
		edit(&input)
	}
	checkpoint := buildCheckpoint(t, repo, config, input)
	return compactedAdvance{repo: repo, config: config, taskID: task.ID, tip: tip, checkpoint: checkpoint}
}

// replayOnto writes local's pack as a single-parent commit on parent, keeping
// its operation IDs, actor and wall time, the way an adoption replays an
// unpushed change onto a checkpoint.
func replayOnto(t *testing.T, repo *Repository, parent, local core.Snapshot) core.Snapshot {
	t.Helper()
	pack := local.Operation
	pack.HistoryGeneration = parent.State.History.Generation
	pack.LogicalClock = parent.State.LogicalClock + 1
	state, err := core.Apply(&parent.State, pack)
	if err != nil {
		t.Fatalf("Apply(replayed pack) error = %v", err)
	}
	head, err := repo.writeTaskObjects(context.Background(), parent.Head, pack, state, "replay", "")
	if err != nil {
		t.Fatalf("writeTaskObjects(replay) error = %v", err)
	}
	return core.Snapshot{Head: head, Operation: pack, State: state}
}

func assertAdvanceRefusedAsRewrite(t *testing.T, repo *Repository, config core.ProjectConfig, advance HeadAdvance) {
	t.Helper()
	err := repo.ValidateTaskHeadAdvances(context.Background(), config, []HeadAdvance{advance})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("ValidateTaskHeadAdvances() category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
	if !strings.Contains(err.Error(), "is not a descendant of its previous head") {
		t.Fatalf("ValidateTaskHeadAdvances() error = %v, want the non-descendant refusal", err)
	}
}

func TestValidateHeadAdvanceAcceptsCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	if err := fixture.repo.CompactTaskRef(ctx, fixture.config, fixture.taskID,
		fixture.tip.Head, fixture.checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	current := TaskHead{TaskID: fixture.taskID, ObjectID: fixture.checkpoint.Head}

	accounted, err := fixture.repo.CompactionAccountsFor(ctx, fixture.config, fixture.tip, current)
	if err != nil || !accounted {
		t.Fatalf("CompactionAccountsFor() = %t, %v, want true, nil", accounted, err)
	}
	if err := fixture.repo.ValidateTaskHeadAdvances(ctx, fixture.config, []HeadAdvance{{
		Previous: fixture.tip, Current: current,
	}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(tip to checkpoint) error = %v, want nil", err)
	}
}

// A clone that had an unpushed change when the remote compacted replays it
// onto the checkpoint. That change is not in the checkpoint's list, but it is
// on the new head's chain, so nothing the old head held is lost.
func TestValidateHeadAdvanceAcceptsCheckpointWithReplayedLocalOps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	repo, config, taskID := fixture.repo, fixture.config, fixture.taskID
	updateSyncTask(t, repo, config, taskID, "Unpushed local title")
	local := currentSnapshot(t, repo, config, taskID)

	// Moving straight to the checkpoint would lose the local change.
	checkpointHead := TaskHead{TaskID: taskID, ObjectID: fixture.checkpoint.Head}
	if accounted, err := repo.CompactionAccountsFor(ctx, config, local, checkpointHead); err != nil || accounted {
		t.Fatalf("CompactionAccountsFor(local to bare checkpoint) = %t, %v, want false, nil", accounted, err)
	}
	assertAdvanceRefusedAsRewrite(t, repo, config, HeadAdvance{Previous: local, Current: checkpointHead})

	replayed := replayOnto(t, repo, fixture.checkpoint, local)
	current := TaskHead{TaskID: taskID, ObjectID: replayed.Head}
	accounted, err := repo.CompactionAccountsFor(ctx, config, local, current)
	if err != nil || !accounted {
		t.Fatalf("CompactionAccountsFor(local to replayed) = %t, %v, want true, nil", accounted, err)
	}
	if err := repo.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{Previous: local, Current: current}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(local to replayed) error = %v, want nil", err)
	}
}

// A second compaction lists the first checkpoint's own operation, so a head
// rooted at the first checkpoint is accounted for by the second.
func TestValidateHeadAdvanceAcceptsRecompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	repo, config, taskID := fixture.repo, fixture.config, fixture.taskID
	if err := repo.CompactTaskRef(ctx, config, taskID, fixture.tip.Head, fixture.checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	updateSyncTask(t, repo, config, taskID, "After the first compaction")
	edited := currentSnapshot(t, repo, config, taskID)
	second := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, edited, false))

	if err := repo.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{
		Previous: edited, Current: TaskHead{TaskID: taskID, ObjectID: second.Head},
	}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(recompaction) error = %v, want nil", err)
	}
}

func TestValidateHeadAdvanceRejectsCheckpointMissingAnOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var omitted string
	fixture := newCompactedAdvance(t, func(input *CheckpointInput) {
		ids := input.Summary.CompactedOperations
		omitted = ids[len(ids)-1]
		input.Summary.CompactedOperations = ids[:len(ids)-1]
	})
	summary, _ := fixture.checkpoint.Operation.Checkpoint()
	if slices.Contains(summary.CompactedOperations, omitted) {
		t.Fatalf("forged checkpoint still lists %s", omitted)
	}
	current := TaskHead{TaskID: fixture.taskID, ObjectID: fixture.checkpoint.Head}

	accounted, err := fixture.repo.CompactionAccountsFor(ctx, fixture.config, fixture.tip, current)
	if err != nil || accounted {
		t.Fatalf("CompactionAccountsFor() = %t, %v, want false, nil", accounted, err)
	}
	assertAdvanceRefusedAsRewrite(t, fixture.repo, fixture.config, HeadAdvance{Previous: fixture.tip, Current: current})
}

// Two clones compacted the same tip, and this one, already on its own
// checkpoint, takes the remote one instead. Its checkpoint was never a change:
// it is accounted for because everything it compacted is.
func TestValidateHeadAdvanceAcceptsConcurrentCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	repo, config, taskID := fixture.repo, fixture.config, fixture.taskID
	ours := fixture.checkpoint
	theirs := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, fixture.tip, false))
	if ours.State.History.Generation == theirs.State.History.Generation {
		t.Fatalf("both checkpoints opened generation %s", ours.State.History.Generation)
	}
	theirHead := TaskHead{TaskID: taskID, ObjectID: theirs.Head}

	if err := repo.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{Previous: ours, Current: theirHead}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(our checkpoint to theirs) error = %v, want nil", err)
	}

	// A change written on our checkpoint is lost by taking theirs bare, and
	// kept by replaying it onto theirs.
	if err := repo.CompactTaskRef(ctx, config, taskID, fixture.tip.Head, ours.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	updateSyncTask(t, repo, config, taskID, "Edited after our compaction")
	local := currentSnapshot(t, repo, config, taskID)
	assertAdvanceRefusedAsRewrite(t, repo, config, HeadAdvance{Previous: local, Current: theirHead})
	replayed := replayOnto(t, repo, theirs, local)
	if err := repo.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{
		Previous: local, Current: TaskHead{TaskID: taskID, ObjectID: replayed.Head},
	}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances(our edited checkpoint to theirs replayed) error = %v, want nil", err)
	}
}

// Our checkpoint summarized an operation the remote checkpoint neither lists
// nor carries, so taking theirs would lose it.
func TestValidateHeadAdvanceRejectsConcurrentCompactionThatLosesAnOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var extra string
	fixture := newCompactedAdvance(t, func(input *CheckpointInput) {
		extra = newCheckpointID(t)
		ids := append(append([]string(nil), input.Summary.CompactedOperations...), extra)
		sort.Strings(ids)
		input.Summary.CompactedOperations = ids
	})
	repo, config, taskID := fixture.repo, fixture.config, fixture.taskID
	theirs := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, fixture.tip, false))
	summary, _ := theirs.Operation.Checkpoint()
	if slices.Contains(summary.CompactedOperations, extra) {
		t.Fatalf("their checkpoint lists %s", extra)
	}
	current := TaskHead{TaskID: taskID, ObjectID: theirs.Head}

	accounted, err := repo.CompactionAccountsFor(ctx, config, fixture.checkpoint, current)
	if err != nil || accounted {
		t.Fatalf("CompactionAccountsFor(our checkpoint to theirs) = %t, %v, want false, nil", accounted, err)
	}
	assertAdvanceRefusedAsRewrite(t, repo, config, HeadAdvance{Previous: fixture.checkpoint, Current: current})
}

// A bulk compaction moves many heads at once, and the guard proves them all in
// a fixed number of Git processes rather than a few per task. In a mixed batch
// only the task whose checkpoint loses an operation is refused.
func TestValidateHeadAdvancesProvesSeveralCompactionsInOneBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, config := writeRepository(t)
	compact := func(title string, edit func(*CheckpointInput)) HeadAdvance {
		t.Helper()
		task := createSyncTask(t, repo, config, title)
		updateSyncTask(t, repo, config, task.ID, title+" edited")
		tip := currentSnapshot(t, repo, config, task.ID)
		input := checkpointInputFor(t, repo, config, tip, false)
		if edit != nil {
			edit(&input)
		}
		checkpoint := buildCheckpoint(t, repo, config, input)
		return HeadAdvance{Previous: tip, Current: TaskHead{TaskID: task.ID, ObjectID: checkpoint.Head}}
	}
	advances := []HeadAdvance{compact("First", nil), compact("Second", nil), compact("Third", nil)}
	lossy := compact("Loses an operation", func(input *CheckpointInput) {
		ids := input.Summary.CompactedOperations
		input.Summary.CompactedOperations = ids[:len(ids)-1]
	})

	// The first call fills per-repository caches, so the counts compared
	// below are both warm.
	if _, err := repo.ValidateTaskHeadAdvancesWithCompactions(ctx, config, advances[:1]); err != nil {
		t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(warm-up) error = %v", err)
	}
	var commands [][]string
	repo.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	if _, err := repo.ValidateTaskHeadAdvancesWithCompactions(ctx, config, advances[:1]); err != nil {
		t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(one) error = %v", err)
	}
	forOne := len(commands)
	commands = nil
	compactions, err := repo.ValidateTaskHeadAdvancesWithCompactions(ctx, config, advances)
	if err != nil {
		t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(three) error = %v", err)
	}
	forThree := len(commands)
	repo.commandObserver = nil
	if forThree != forOne {
		t.Fatalf("Git commands for three compactions = %d, for one = %d, want the same; commands = %v",
			forThree, forOne, commands)
	}
	if len(compactions) != len(advances) {
		t.Fatalf("compactions = %v, want all %d tasks", compactions, len(advances))
	}
	for _, advance := range advances {
		if _, found := compactions[advance.Current.TaskID]; !found {
			t.Fatalf("compactions = %v, missing %s", compactions, advance.Current.TaskID)
		}
	}

	mixed := []HeadAdvance{advances[0], lossy, advances[1], advances[2]}
	_, err = repo.ValidateTaskHeadAdvancesWithCompactions(ctx, config, mixed)
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(mixed) category = %q, want %q; error = %v",
			got, core.CategoryCorruptData, err)
	}
	want := `task "` + lossy.Current.TaskID + `" current head is not a descendant of its previous head`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(mixed) error = %v, want it to contain %q", err, want)
	}
	for _, advance := range advances {
		if strings.Contains(err.Error(), advance.Current.TaskID) {
			t.Fatalf("ValidateTaskHeadAdvancesWithCompactions(mixed) error = %v names accounted task %s",
				err, advance.Current.TaskID)
		}
	}
}

// A parentless commit that is not a checkpoint accounts for nothing, even
// when it is the same task and carries every operation's effect.
func TestValidateHeadAdvanceRejectsUnrelatedRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	repo, config := fixture.repo, fixture.config
	orphan := syncGit(t, repo.Root, "commit-tree", fixture.tip.Head+"^{tree}", "-m", "parentless rewrite")
	current := TaskHead{TaskID: fixture.taskID, ObjectID: orphan}

	accounted, err := repo.CompactionAccountsFor(ctx, config, fixture.tip, current)
	if err != nil || accounted {
		t.Fatalf("CompactionAccountsFor(orphan) = %t, %v, want false, nil", accounted, err)
	}
	assertAdvanceRefusedAsRewrite(t, repo, config, HeadAdvance{Previous: fixture.tip, Current: current})
}

// After gc a clone may no longer hold the history a projection last saw.
// Nothing can then be proven about it, which is a refusal, not a failure.
func TestCompactionAccountsForPreviousMissingLocally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactedAdvance(t, nil)
	repo, config := fixture.repo, fixture.config
	if err := repo.CompactTaskRef(ctx, config, fixture.taskID, fixture.tip.Head, fixture.checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	syncGit(t, repo.Root, "gc", "--prune=now", "--quiet")
	if err := exec.Command("git", "-C", repo.Root, "cat-file", "-e", fixture.tip.Head).Run(); err == nil {
		t.Fatalf("old tip %s survived gc --prune=now", fixture.tip.Head)
	}

	accounted, err := repo.CompactionAccountsFor(ctx, config, fixture.tip,
		TaskHead{TaskID: fixture.taskID, ObjectID: fixture.checkpoint.Head})
	if err != nil || accounted {
		t.Fatalf("CompactionAccountsFor(pruned previous) = %t, %v, want false, nil", accounted, err)
	}
}
