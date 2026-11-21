package gitstore

import (
	"context"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// checkpointNow is a late-evening compaction time west of UTC on purpose: in
// UTC it is already the next day, so a drop stamped with the local time would
// show a different date to a reader that takes the first ten characters.
var checkpointNow = time.Date(2026, time.November, 13, 22, 30, 0, 0, time.FixedZone("EST", -5*60*60))

func newCheckpointID(t *testing.T) string {
	t.Helper()
	id, err := core.CryptoULIDSource{}.New()
	if err != nil {
		t.Fatalf("new ULID: %v", err)
	}
	return id
}

func currentSnapshot(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) core.Snapshot {
	t.Helper()
	snapshot, err := repo.Get(context.Background(), config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	return snapshot
}

// checkpointInputFor builds the input a compaction run would pass for tip,
// with the summary filled from the history CompactedHistory reports.
func checkpointInputFor(t *testing.T, repo *Repository, config core.ProjectConfig, tip core.Snapshot, keep bool) CheckpointInput {
	t.Helper()
	ids, commits, first, last, err := repo.CompactedHistory(context.Background(), config, tip.Head)
	if err != nil {
		t.Fatalf("CompactedHistory() error = %v", err)
	}
	return CheckpointInput{
		TaskID:          tip.State.TaskID,
		Tip:             tip,
		RunID:           newCheckpointID(t),
		Generation:      newCheckpointID(t),
		OperationID:     newCheckpointID(t),
		Actor:           "compactor@example.test",
		Now:             checkpointNow,
		KeepAttachments: keep,
		Summary: core.CheckpointData{
			CompactedCommits:    commits,
			FirstWallTime:       first,
			LastWallTime:        last,
			CompactedOperations: ids,
		},
	}
}

func buildCheckpoint(t *testing.T, repo *Repository, config core.ProjectConfig, input CheckpointInput) core.Snapshot {
	t.Helper()
	checkpoint, err := repo.BuildCheckpoint(context.Background(), config, input)
	if err != nil {
		t.Fatalf("BuildCheckpoint() error = %v", err)
	}
	return checkpoint
}

// doneTaskWithFileAndLink is the task every attachment test compacts: Done,
// with one file and one link attached.
func doneTaskWithFileAndLink(t *testing.T, repo *Repository, config core.ProjectConfig) (core.Task, core.Attachment, core.Attachment) {
	t.Helper()
	task := createSyncTask(t, repo, config, "Compact me")
	file := attachFileToTask(t, repo, config, task.ID, "trace.log", "hello world")
	result, err := threadService(repo, config).AttachmentAddMutation(
		context.Background(), task.ID, core.AttachmentAddInput{
			Kind: core.AttachmentLink, URL: "https://example.com/spec", Label: "Spec",
		})
	if err != nil {
		t.Fatalf("AttachmentAddMutation(link) error = %v", err)
	}
	var link core.Attachment
	for _, attachment := range result.Task.Attachments {
		if attachment.Kind == core.AttachmentLink {
			link = attachment
		}
	}
	done := core.StatusDone
	if _, err := syncService(repo, config).UpdateMutation(
		context.Background(), task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatalf("UpdateMutation(done) error = %v", err)
	}
	return task, file, link
}

func findStateAttachment(t *testing.T, attachments []core.Attachment, id string) core.Attachment {
	t.Helper()
	for _, attachment := range attachments {
		if attachment.ID == id {
			return attachment
		}
	}
	t.Fatalf("attachment %s is not in %#v", id, attachments)
	return core.Attachment{}
}

func treeNames(t *testing.T, repo *Repository, commit string) []string {
	t.Helper()
	return strings.Fields(syncGit(t, repo.Root, "ls-tree", "--name-only", commit))
}

func TestBuildCheckpointDropsAttachmentsByDefault(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, file, link := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)
	input := checkpointInputFor(t, repo, config, tip, false)

	checkpoint := buildCheckpoint(t, repo, config, input)

	if got := parentCount(t, repo, checkpoint.Head); got != 0 {
		t.Fatalf("checkpoint parent count = %d, want 0", got)
	}
	if got, want := strings.Join(treeNames(t, repo, checkpoint.Head), " "), "operation.json state.json"; got != want {
		t.Fatalf("checkpoint tree = %q, want %q", got, want)
	}
	dropped := findStateAttachment(t, checkpoint.State.Task.Attachments, file.ID)
	if dropped.Dropped == nil || dropped.Dropped.RunID != input.RunID {
		t.Fatalf("file attachment drop = %#v, want run %s", dropped.Dropped, input.RunID)
	}
	// The drop is stamped in UTC, so its stored date is the date Go prints.
	if got, want := dropped.Dropped.At, checkpointNow; !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("file attachment dropped at %v, want %v in UTC", got, want)
	}
	if got, want := dropped.Dropped.At.Format("2006-01-02"), "2026-11-14"; got != want {
		t.Fatalf("file attachment drop date = %s, want the UTC date %s", got, want)
	}
	if dropped.Blob != file.Blob || dropped.Size != file.Size || dropped.Name != file.Name {
		t.Fatalf("dropped attachment lost its metadata: %#v, was %#v", dropped, file)
	}
	keptLink := findStateAttachment(t, checkpoint.State.Task.Attachments, link.ID)
	if keptLink.Dropped != nil || keptLink.URL != link.URL || keptLink.Label != link.Label {
		t.Fatalf("link attachment = %#v, want it unchanged from %#v", keptLink, link)
	}
	summary, ok := checkpoint.Operation.Checkpoint()
	if !ok || summary.Attachments != core.CheckpointAttachmentsDropped || summary.RunID != input.RunID {
		t.Fatalf("checkpoint summary = %#v, want a dropped-attachments summary for run %s", summary, input.RunID)
	}
	if checkpoint.State.Task.Status != core.StatusDone || checkpoint.State.Task.Title != task.Title {
		t.Fatalf("checkpoint task = %#v, want the Done task it replaced", checkpoint.State.Task)
	}

	read, err := repo.ReadTaskHeads(context.Background(), config,
		[]TaskHead{{TaskID: task.ID, ObjectID: checkpoint.Head}})
	if err != nil {
		t.Fatalf("ReadTaskHeads(checkpoint) error = %v", err)
	}
	if len(read) != 1 || read[0].Head != checkpoint.Head {
		t.Fatalf("ReadTaskHeads(checkpoint) = %#v", read)
	}
	// The task ref is untouched: building a checkpoint writes objects only.
	if got := refValue(t, repo, taskRefPrefix+task.ID); got != tip.Head {
		t.Fatalf("task ref = %s, want the tip %s", got, tip.Head)
	}
}

// A dropped file's bytes are not needed to build the checkpoint, so a clone
// that never had them can still compact the task.
func TestBuildCheckpointDropsAttachmentsWhoseBlobIsMissing(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, file, _ := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)
	absent := syncGitInput(t, repo.Root, []byte("bytes this clone never stored"), "hash-object", "--stdin")
	tip.State.Task.Attachments = append([]core.Attachment(nil), tip.State.Task.Attachments...)
	for index := range tip.State.Task.Attachments {
		if tip.State.Task.Attachments[index].ID == file.ID {
			tip.State.Task.Attachments[index].Blob = strings.TrimSpace(absent)
		}
	}

	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))
	if got, want := strings.Join(treeNames(t, repo, checkpoint.Head), " "), "operation.json state.json"; got != want {
		t.Fatalf("checkpoint tree = %q, want %q", got, want)
	}
}

func TestBuildCheckpointKeepsAttachments(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, file, link := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)

	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, true))

	entry := attachmentTreeName(file.ID)
	if got, want := strings.Join(treeNames(t, repo, checkpoint.Head), " "), entry+" operation.json state.json"; got != want {
		t.Fatalf("checkpoint tree = %q, want %q", got, want)
	}
	if got := syncGit(t, repo.Root, "rev-parse", checkpoint.Head+":"+entry); got != file.Blob {
		t.Fatalf("tree entry %s = %s, want the original blob %s", entry, got, file.Blob)
	}
	kept := findStateAttachment(t, checkpoint.State.Task.Attachments, file.ID)
	if kept.Dropped != nil || kept.Blob != file.Blob {
		t.Fatalf("kept attachment = %#v, want it unchanged", kept)
	}
	if findStateAttachment(t, checkpoint.State.Task.Attachments, link.ID).Dropped != nil {
		t.Fatal("link attachment was dropped")
	}
	if summary, _ := checkpoint.Operation.Checkpoint(); summary.Attachments != core.CheckpointAttachmentsKept {
		t.Fatalf("checkpoint attachments = %q, want %q", summary.Attachments, core.CheckpointAttachmentsKept)
	}
	contents, err := repo.ReadAttachment(context.Background(), config, kept.Blob)
	if err != nil {
		t.Fatalf("ReadAttachment() error = %v", err)
	}
	if string(contents) != "hello world" {
		t.Fatalf("attachment contents = %q, want the original bytes", contents)
	}
	if _, err := repo.ReadTaskHeads(context.Background(), config,
		[]TaskHead{{TaskID: task.ID, ObjectID: checkpoint.Head}}); err != nil {
		t.Fatalf("ReadTaskHeads(checkpoint) error = %v", err)
	}
	// The kept blob is reachable from the checkpoint alone, which is what lets
	// the replaced history go.
	if objects := syncGit(t, repo.Root, "rev-list", "--objects", checkpoint.Head); !strings.Contains(objects, file.Blob) {
		t.Fatal("the checkpoint does not reach the kept attachment blob")
	}
}

func TestBuildCheckpointRefusesAKeptAttachmentWhoseBlobIsMissing(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, file, _ := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)
	absent := strings.TrimSpace(syncGitInput(t, repo.Root, []byte("bytes this clone never stored"), "hash-object", "--stdin"))
	tip.State.Task.Attachments = append([]core.Attachment(nil), tip.State.Task.Attachments...)
	for index := range tip.State.Task.Attachments {
		if tip.State.Task.Attachments[index].ID == file.ID {
			tip.State.Task.Attachments[index].Blob = absent
		}
	}

	_, err := repo.BuildCheckpoint(context.Background(), config, checkpointInputFor(t, repo, config, tip, true))
	if got, want := core.CategoryOf(err), core.CategoryNotFound; got != want {
		t.Fatalf("BuildCheckpoint() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), task.ID) || !strings.Contains(err.Error(), file.ID) {
		t.Fatalf("BuildCheckpoint() error = %v, want it to name task %s and attachment %s", err, task.ID, file.ID)
	}
}

func TestBuildCheckpointCarriesClockAndCompactedFrom(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Compact me")
	updateSyncTask(t, repo, config, task.ID, "Compact me again")
	tip := currentSnapshot(t, repo, config, task.ID)
	input := checkpointInputFor(t, repo, config, tip, false)

	checkpoint := buildCheckpoint(t, repo, config, input)

	if got, want := checkpoint.State.LogicalClock, tip.State.LogicalClock+1; got != want {
		t.Fatalf("checkpoint logical clock = %d, want %d", got, want)
	}
	if checkpoint.State.History.CompactedFrom == nil || *checkpoint.State.History.CompactedFrom != tip.Head {
		t.Fatalf("checkpoint compactedFrom = %v, want %s", checkpoint.State.History.CompactedFrom, tip.Head)
	}
	if got, want := checkpoint.State.History.Generation, input.Generation; got != want {
		t.Fatalf("checkpoint generation = %s, want %s", got, want)
	}
	summary, _ := checkpoint.Operation.Checkpoint()
	if summary.PreviousGeneration != tip.State.History.Generation || summary.CompactedFrom != tip.Head {
		t.Fatalf("checkpoint summary = %#v, want it to name the replaced tip and generation", summary)
	}
	if got, want := checkpoint.State.MinReader, 4; got != want {
		t.Fatalf("checkpoint state minReader = %d, want %d", got, want)
	}
	if got := checkpoint.Operation.Operations[0].ID; got != input.OperationID {
		t.Fatalf("checkpoint operation ID = %s, want %s", got, input.OperationID)
	}
}

func TestCompactedHistoryListsEveryOperation(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Three commits")
	created := currentSnapshot(t, repo, config, task.ID)
	updateSyncTask(t, repo, config, task.ID, "Second title")
	second := currentSnapshot(t, repo, config, task.ID)
	updateSyncTask(t, repo, config, task.ID, "Third title")
	third := currentSnapshot(t, repo, config, task.ID)

	ids, commits, first, last, err := repo.CompactedHistory(context.Background(), config, third.Head)
	if err != nil {
		t.Fatalf("CompactedHistory() error = %v", err)
	}
	var want []string
	for _, snapshot := range []core.Snapshot{created, second, third} {
		for _, operation := range snapshot.Operation.Operations {
			want = append(want, operation.ID)
		}
	}
	sort.Strings(want)
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("CompactedHistory() ids = %v, want %v", ids, want)
	}
	if commits != 3 {
		t.Fatalf("CompactedHistory() commits = %d, want 3", commits)
	}
	if !first.Equal(created.Operation.WallTime) || !last.Equal(third.Operation.WallTime) {
		t.Fatalf("CompactedHistory() first, last = %v, %v, want %v, %v",
			first, last, created.Operation.WallTime, third.Operation.WallTime)
	}
}

// A second compaction replaces a history whose root is the first checkpoint.
// Its list keeps every operation the first one compacted, and adds the first
// checkpoint's own operation and every edit since: a clone whose chain is
// rooted at the first checkpoint can then find every one of its operations in
// the second.
func TestRecompactionUnionsOperationIDs(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Compact twice")
	updateSyncTask(t, repo, config, task.ID, "Before the first compaction")
	tip := currentSnapshot(t, repo, config, task.ID)
	firstInput := checkpointInputFor(t, repo, config, tip, false)
	first := buildCheckpoint(t, repo, config, firstInput)
	syncGit(t, repo.Root, "update-ref", taskRefPrefix+task.ID, first.Head, tip.Head)

	updateSyncTask(t, repo, config, task.ID, "After the first compaction")
	edited := currentSnapshot(t, repo, config, task.ID)
	if got := parentCount(t, repo, edited.Head); got != 1 {
		t.Fatalf("edit after a checkpoint has %d parents, want 1", got)
	}

	secondInput := checkpointInputFor(t, repo, config, edited, false)
	second := buildCheckpoint(t, repo, config, secondInput)

	want := append([]string(nil), firstInput.Summary.CompactedOperations...)
	want = append(want, first.Operation.Operations[0].ID)
	for _, operation := range edited.Operation.Operations {
		want = append(want, operation.ID)
	}
	sort.Strings(want)
	summary, _ := second.Operation.Checkpoint()
	if strings.Join(summary.CompactedOperations, ",") != strings.Join(want, ",") {
		t.Fatalf("second checkpoint operations = %v, want %v", summary.CompactedOperations, want)
	}
	// The counts and the span carry on from the first checkpoint's summary,
	// which is the only record of the history it replaced; the first
	// checkpoint itself is not a change.
	if got, want := summary.CompactedCommits, firstInput.Summary.CompactedCommits+1; got != want {
		t.Fatalf("second checkpoint commits = %d, want %d", got, want)
	}
	if !summary.FirstWallTime.Equal(firstInput.Summary.FirstWallTime) ||
		!summary.LastWallTime.Equal(edited.Operation.WallTime) {
		t.Fatalf("second checkpoint span = %v..%v, want %v..%v", summary.FirstWallTime, summary.LastWallTime,
			firstInput.Summary.FirstWallTime, edited.Operation.WallTime)
	}
	if summary.PreviousGeneration != first.State.History.Generation {
		t.Fatalf("second checkpoint previous generation = %s, want %s",
			summary.PreviousGeneration, first.State.History.Generation)
	}
	if _, err := repo.ReadTaskHeads(context.Background(), config,
		[]TaskHead{{TaskID: task.ID, ObjectID: second.Head}}); err != nil {
		t.Fatalf("ReadTaskHeads(second checkpoint) error = %v", err)
	}
}

// forgeCheckpointCommit writes a parentless commit from a pack, a state and
// tree entries chosen by the test, bypassing every check BuildCheckpoint makes.
func forgeCheckpointCommit(
	t *testing.T,
	repo *Repository,
	pack core.OperationPack,
	state core.StateDocument,
	attachments ...attachmentTreeEntry,
) string {
	t.Helper()
	ctx := context.Background()
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(pack) error = %v", err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(state) error = %v", err)
	}
	operationBlob, err := repo.writeBlob(ctx, packBytes)
	if err != nil {
		t.Fatal(err)
	}
	stateBlob, err := repo.writeBlob(ctx, stateBytes)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.writeTaskTree(ctx, operationBlob, stateBlob, attachments...)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.writeCommit(ctx, tree, "", "forged checkpoint", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func assertReadCorrupt(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, head, want string) {
	t.Helper()
	_, err := repo.ReadTaskHeads(context.Background(), config, []TaskHead{{TaskID: taskID, ObjectID: head}})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("ReadTaskHeads() category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ReadTaskHeads() error = %v, want it to mention %q", err, want)
	}
}

func TestReadRejectsCheckpointWithWrongState(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Compact me")
	updateSyncTask(t, repo, config, task.ID, "Compact me again")
	tip := currentSnapshot(t, repo, config, task.ID)
	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))

	forged := checkpoint.State
	forged.Task.Title = "Not what the checkpoint folds to"
	head := forgeCheckpointCommit(t, repo, checkpoint.Operation, forged)

	assertReadCorrupt(t, repo, config, task.ID, head, "stored checkpoint differs from computed state")
}

// A checkpoint is a root, and a root has no parent.
func TestReadRejectsCheckpointWithAParent(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Compact me")
	tip := currentSnapshot(t, repo, config, task.ID)
	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))
	tree := syncGit(t, repo.Root, "rev-parse", checkpoint.Head+"^{tree}")
	head := syncGit(t, repo.Root, "commit-tree", tree, "-p", tip.Head, "-m", "checkpoint with a parent")

	assertReadCorrupt(t, repo, config, task.ID, head, "history.checkpoint tip must have no parents")
}

func TestReadRejectsKeptAttachmentMissingFromTree(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, _, _ := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)
	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, true))

	head := forgeCheckpointCommit(t, repo, checkpoint.Operation, checkpoint.State)

	assertReadCorrupt(t, repo, config, task.ID, head, "does not carry the blob")
}

// A dropped file's bytes must not ride along: dropping them is the point, and
// a tree that kept them would hold a blob the state says is gone.
func TestReadRejectsCheckpointCarryingADroppedAttachment(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task, file, _ := doneTaskWithFileAndLink(t, repo, config)
	tip := currentSnapshot(t, repo, config, task.ID)
	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))

	head := forgeCheckpointCommit(t, repo, checkpoint.Operation, checkpoint.State,
		attachmentTreeEntry{name: attachmentTreeName(file.ID), objectID: file.Blob})

	assertReadCorrupt(t, repo, config, task.ID, head, "dropped")
}

// A chain that ends at a parentless commit that is not a root is a history this
// clone holds only part of: a shallow boundary, or damage. Summarizing it would
// write a checkpoint that silently forgets the rest, so it is refused.
func TestCompactedHistoryRefusesAChainWhoseRootIsNotARoot(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Partial history")
	updateSyncTask(t, repo, config, task.ID, "Second title")
	middle := currentSnapshot(t, repo, config, task.ID)
	updateSyncTask(t, repo, config, task.ID, "Third title")
	tip := currentSnapshot(t, repo, config, task.ID)

	orphan := syncGit(t, repo.Root, "commit-tree", middle.Head+"^{tree}", "-m", "parentless ordinary commit")
	head := syncGit(t, repo.Root, "commit-tree", tip.Head+"^{tree}", "-p", orphan, "-m", "tip on a cut chain")

	_, _, _, _, err := repo.CompactedHistory(context.Background(), config, head)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("CompactedHistory() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), task.ID) || !strings.Contains(err.Error(), orphan) {
		t.Fatalf("CompactedHistory() error = %v, want it to name task %s and commit %s", err, task.ID, orphan)
	}
}

// compactionFixture is a Done task with a three-commit history in a clone that
// has no origin, plus two parked refs for it: one at the task's first commit
// and one at a sibling of its second commit, which nothing but that parked ref
// reaches. It carries the tip it compacts from and the checkpoint built for
// that tip.
type compactionFixture struct {
	repo       *Repository
	config     core.ProjectConfig
	taskID     string
	tip        core.Snapshot
	checkpoint core.Snapshot
	parked     []string
	sibling    string
}

func newCompactionFixture(t *testing.T) compactionFixture {
	t.Helper()
	repo, config := writeRepository(t)
	task := createSyncTask(t, repo, config, "Compact me")
	created := currentSnapshot(t, repo, config, task.ID)
	updateSyncTask(t, repo, config, task.ID, "Second title")
	second := currentSnapshot(t, repo, config, task.ID)
	done := core.StatusDone
	if _, err := syncService(repo, config).UpdateMutation(
		context.Background(), task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatalf("UpdateMutation(done) error = %v", err)
	}
	tip := currentSnapshot(t, repo, config, task.ID)

	sibling := syncGit(t, repo.Root, "commit-tree", second.Head+"^{tree}", "-p", created.Head, "-m", "parked sibling")
	parked := []string{
		reconciledRefPrefix + task.ID + "/0",
		reconciledRefPrefix + task.ID + "/1",
	}
	syncGit(t, repo.Root, "update-ref", parked[0], created.Head)
	syncGit(t, repo.Root, "update-ref", parked[1], sibling)

	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))
	return compactionFixture{
		repo: repo, config: config, taskID: task.ID,
		tip: tip, checkpoint: checkpoint, parked: parked, sibling: sibling,
	}
}

func parkedRefNames(t *testing.T, repo *Repository, taskID string) []string {
	t.Helper()
	return strings.Fields(syncGit(t, repo.Root, "for-each-ref", "--format=%(refname)", reconciledRefPrefix+taskID+"/"))
}

func taskReflogObjects(t *testing.T, repo *Repository, taskID string) []string {
	t.Helper()
	return strings.Fields(syncGit(t, repo.Root, "reflog", "show", "--format=%H", taskRefPrefix+taskID))
}

func TestCompactTaskRefMovesRefAndDropsParkedRefs(t *testing.T) {
	t.Parallel()
	fixture := newCompactionFixture(t)
	repo, taskID := fixture.repo, fixture.taskID
	if got := parkedRefNames(t, repo, taskID); len(got) != 2 {
		t.Fatalf("parked refs before compaction = %v, want two", got)
	}
	if !slices.Contains(taskReflogObjects(t, repo, taskID), fixture.tip.Head) {
		t.Fatalf("task reflog before compaction does not name the tip %s", fixture.tip.Head)
	}

	if err := repo.CompactTaskRef(context.Background(), fixture.config, taskID,
		fixture.tip.Head, fixture.checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}

	if got := refValue(t, repo, taskRefPrefix+taskID); got != fixture.checkpoint.Head {
		t.Fatalf("task ref = %s, want the checkpoint %s", got, fixture.checkpoint.Head)
	}
	if got := parkedRefNames(t, repo, taskID); len(got) != 0 {
		t.Fatalf("parked refs after compaction = %v, want none", got)
	}
	reflog := syncGit(t, repo.Root, "reflog", "show", "--format=%H %gs", taskRefPrefix+taskID)
	if strings.Contains(reflog, fixture.tip.Head) {
		t.Fatalf("task reflog still names the old tip %s:\n%s", fixture.tip.Head, reflog)
	}
}

func TestCompactTaskRefRefusesMovedRef(t *testing.T) {
	t.Parallel()
	fixture := newCompactionFixture(t)
	repo, taskID := fixture.repo, fixture.taskID
	stale := syncGit(t, repo.Root, "rev-parse", fixture.tip.Head+"^")
	reflogBefore := taskReflogObjects(t, repo, taskID)

	err := repo.CompactTaskRef(context.Background(), fixture.config, taskID, stale, fixture.checkpoint.Head)
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("CompactTaskRef(stale) category = %q, want %q; error = %v", got, want, err)
	}
	if got := refValue(t, repo, taskRefPrefix+taskID); got != fixture.tip.Head {
		t.Fatalf("task ref = %s, want it unchanged at %s", got, fixture.tip.Head)
	}
	if got := parkedRefNames(t, repo, taskID); strings.Join(got, " ") != strings.Join(fixture.parked, " ") {
		t.Fatalf("parked refs = %v, want them intact as %v", got, fixture.parked)
	}
	if got := taskReflogObjects(t, repo, taskID); strings.Join(got, " ") != strings.Join(reflogBefore, " ") {
		t.Fatalf("task reflog = %v, want it unchanged from %v", got, reflogBefore)
	}
}

// A task ref can exist without a reflog, for one written by a Git that logged
// nothing, and a retried cleanup finds nothing left to expire. Neither is an
// error.
func TestExpireTaskReflogWithoutReflogIsANoOp(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	task := createSyncTask(t, repo, config, "No reflog")
	head := refValue(t, repo, taskRefPrefix+task.ID)
	// Deleting the ref deletes its reflog; recreating it with logging off
	// leaves it without one.
	syncGit(t, repo.Root, "update-ref", "-d", taskRefPrefix+task.ID)
	syncGit(t, repo.Root, "-c", "core.logAllRefUpdates=false", "update-ref", taskRefPrefix+task.ID, head)
	if exists := exec.Command("git", "-C", repo.Root, "reflog", "exists", taskRefPrefix+task.ID).Run(); exists == nil {
		t.Fatalf("fixture task ref still has a reflog")
	}

	if err := repo.ExpireTaskReflog(context.Background(), task.ID); err != nil {
		t.Fatalf("ExpireTaskReflog() without a reflog error = %v", err)
	}
	if got := refValue(t, repo, taskRefPrefix+task.ID); got != head {
		t.Fatalf("task ref = %s, want it unchanged at %s", got, head)
	}
}

// Once the ref, its parked refs and its reflog let go of the old history, Git
// can prune it. The clone has no origin, so no remote-tracking ref holds it
// either.
func TestCompactionFreesObjectsAfterGC(t *testing.T) {
	t.Parallel()
	fixture := newCompactionFixture(t)
	repo, taskID := fixture.repo, fixture.taskID
	if remotes := syncGit(t, repo.Root, "remote"); remotes != "" {
		t.Fatalf("fixture remotes = %q, want none", remotes)
	}
	if refExists(t, repo, remoteTaskRefPrefix+taskID) {
		t.Fatalf("fixture has a tracking ref for %s", taskID)
	}
	before := reachableObjects(t, repo.Root)

	if err := repo.CompactTaskRef(context.Background(), fixture.config, taskID,
		fixture.tip.Head, fixture.checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	syncGit(t, repo.Root, "gc", "--prune=now", "--quiet")

	for _, object := range []string{fixture.tip.Head, fixture.sibling} {
		command := exec.Command("git", "-C", repo.Root, "cat-file", "-e", object)
		if err := command.Run(); err == nil {
			t.Fatalf("object %s survived gc --prune=now after compaction", object)
		}
	}
	if after := reachableObjects(t, repo.Root); after >= before {
		t.Fatalf("reachable objects after compaction = %d, want fewer than %d", after, before)
	}
}

// The estimate sums the on-disk size of the objects only the task's refs
// reach, less the tip's state.json. Git 2.31's `rev-list --disk-usage` is the
// oracle here; the estimate itself must not need it.
func TestCompactionReclaimableBytesCountsOnlyTheTaskHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newCompactionFixture(t)
	repo, taskID := fixture.repo, fixture.taskID
	exclusions := []string{
		"--not",
		"--exclude=" + taskRefPrefix + taskID,
		"--exclude=" + remoteTaskRefPrefix + taskID,
		"--exclude=" + reconciledRefPrefix + taskID + "/*",
		"--all",
	}
	oracle := func() int64 {
		t.Helper()
		args := append([]string{"-C", repo.Root, "rev-list", "--objects", "--disk-usage", fixture.tip.Head}, fixture.parked...)
		output, err := exec.Command("git", append(args, exclusions...)...).Output()
		if err != nil {
			testenv.MissingCapability(t, "Git cannot report rev-list --disk-usage (needs 2.31): %v", err)
		}
		usage, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
		if err != nil {
			t.Fatalf("parse disk usage %q: %v", output, err)
		}
		state, err := strconv.ParseInt(syncGit(t, repo.Root, "cat-file", "-s", fixture.tip.Head+":state.json"), 10, 64)
		if err != nil {
			t.Fatalf("parse state size: %v", err)
		}
		return usage - state
	}

	got, err := repo.CompactionReclaimableBytes(ctx, fixture.config, taskID, fixture.tip.Head)
	if err != nil {
		t.Fatalf("CompactionReclaimableBytes() error = %v", err)
	}
	want := oracle()
	if got != want || got <= 0 {
		t.Fatalf("estimate = %d, want the oracle's positive %d", got, want)
	}

	// Another ref reaching the parked sibling keeps its objects, so they no
	// longer count.
	syncGit(t, repo.Root, "update-ref", "refs/heads/keeps-sibling", fixture.sibling)
	shared, err := repo.CompactionReclaimableBytes(ctx, fixture.config, taskID, fixture.tip.Head)
	if err != nil {
		t.Fatalf("CompactionReclaimableBytes(shared) error = %v", err)
	}
	if want := oracle(); shared != want || shared >= got {
		t.Fatalf("estimate with the sibling shared = %d, want the oracle's %d, below %d", shared, want, got)
	}
}
