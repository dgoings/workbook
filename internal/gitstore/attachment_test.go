package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func threadService(repo *Repository, config core.ProjectConfig) core.Service {
	service := syncService(repo, config)
	service.Blobs = repo
	return service
}

func commentOnTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, body string) core.MutationResult {
	t.Helper()
	result, err := threadService(repo, config).CommentAddMutation(
		context.Background(), taskID, core.CommentAddInput{Body: body})
	if err != nil {
		t.Fatalf("CommentAddMutation() error = %v", err)
	}
	return result
}

func attachFileToTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, name, contents string) core.Attachment {
	t.Helper()
	result, err := threadService(repo, config).AttachmentAddMutation(
		context.Background(), taskID, core.AttachmentAddInput{
			Kind: core.AttachmentFile, Name: name, Content: []byte(contents),
		})
	if err != nil {
		t.Fatalf("AttachmentAddMutation() error = %v", err)
	}
	for _, attachment := range result.Task.Attachments {
		if attachment.Name == name {
			return attachment
		}
	}
	t.Fatalf("attachment %q is not in the resulting task: %#v", name, result.Task.Attachments)
	return core.Attachment{}
}

// The blob an attachment names is in the tree of the commit that added it, and
// the checkpoint names the blob.
func TestAnAttachedFileLivesInItsOwnCommitTree(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task with a file")
	attachment := attachFileToTask(t, repo, config, task.ID, "trace.log", "hello world")

	head := refValue(t, repo, taskRefPrefix+task.ID)
	entries := syncGit(t, repo.Root, "ls-tree", "--name-only", head)
	entryName := attachmentTreeName(attachment.ID)
	if !strings.Contains(entries, entryName) {
		t.Fatalf("commit tree = %q, want an entry named %q", entries, entryName)
	}
	if got := syncGit(t, repo.Root, "rev-parse", head+":"+entryName); got != attachment.Blob {
		t.Fatalf("tree entry object = %q, want the checkpoint's blob %q", got, attachment.Blob)
	}

	// And the read is one object read by the ID the checkpoint recorded.
	contents, err := repo.ReadAttachment(context.Background(), config, attachment.Blob)
	if err != nil {
		t.Fatalf("ReadAttachment() error = %v", err)
	}
	if string(contents) != "hello world" {
		t.Fatalf("attachment contents = %q", contents)
	}
	if attachment.Size != int64(len("hello world")) || attachment.Media != "text/plain" {
		t.Fatalf("attachment = %#v", attachment)
	}
}

// HARD INVARIANT. An attachment blob is reachable through its own task's ref
// history and through nothing else.
//
// This is what the future compaction verb is being kept honest for: it will
// offer to strip a task's attachments by rewriting that task's history, and it
// may only do that if no other task's history depends on the objects it drops.
// So two tasks attaching identical bytes each carry the blob in their own
// commit — Git stores one object because Git is content-addressed, but neither
// task reaches it through the other, and dropping either task's refs leaves the
// other's attachment readable.
func TestAnAttachmentIsReachableOnlyThroughItsOwnTaskHistory(t *testing.T) {
	repo, _, config := syncRepositories(t)
	first := createSyncTask(t, repo, config, "First task")
	second := createSyncTask(t, repo, config, "Second task")
	const shared = "identical bytes"
	firstAttachment := attachFileToTask(t, repo, config, first.ID, "a.txt", shared)
	secondAttachment := attachFileToTask(t, repo, config, second.ID, "b.txt", shared)

	// Content addressing means one object, and that is not what the invariant
	// is about: the question is which histories reach it.
	if firstAttachment.Blob != secondAttachment.Blob {
		t.Fatalf("identical bytes hashed to %q and %q", firstAttachment.Blob, secondAttachment.Blob)
	}
	if firstAttachment.ID == secondAttachment.ID {
		t.Fatal("two attachments share an identifier")
	}

	for _, subject := range []struct {
		taskID string
		blob   string
	}{{first.ID, firstAttachment.Blob}, {second.ID, secondAttachment.Blob}} {
		objects := syncGit(t, repo.Root, "rev-list", "--objects", taskRefPrefix+subject.taskID)
		if !strings.Contains(objects, subject.blob) {
			t.Fatalf("task %s does not reach its own attachment blob %q", subject.taskID, subject.blob)
		}
	}

	// No ref outside the task refs holds attachments: there is no shared store
	// to become a dependency between tasks.
	refs := syncGit(t, repo.Root, "for-each-ref", "--format=%(refname)", "refs/workbook/")
	for _, ref := range strings.Fields(refs) {
		if strings.HasPrefix(ref, taskRefPrefix) || ref == "refs/workbook/config" ||
			strings.HasPrefix(ref, "refs/workbook/project") {
			continue
		}
		t.Fatalf("an unexpected Workbook ref exists and might hold attachments: %q", ref)
	}

	// The load-bearing half: with every other task's ref gone, the surviving
	// task's attachment is still reachable from its own ref alone.
	syncGit(t, repo.Root, "update-ref", "-d", taskRefPrefix+second.ID)
	objects := syncGit(t, repo.Root, "rev-list", "--objects", taskRefPrefix+first.ID)
	if !strings.Contains(objects, firstAttachment.Blob) {
		t.Fatal("the surviving task stopped reaching its attachment once the other task's ref was deleted")
	}
}

// A commit that names a blob it does not carry is corruption, which is the read
// path's half of the invariant.
func TestATaskCommitThatDoesNotCarryItsAttachmentIsCorrupt(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task with a file")
	attachment := attachFileToTask(t, repo, config, task.ID, "trace.log", "hello world")

	head := refValue(t, repo, taskRefPrefix+task.ID)
	operationBlob := syncGit(t, repo.Root, "rev-parse", head+":operation.json")
	stateBlob := syncGit(t, repo.Root, "rev-parse", head+":state.json")
	parent := syncGit(t, repo.Root, "rev-parse", head+"^")
	tree := syncGitInput(t, repo.Root, []byte(
		"100644 blob "+operationBlob+"\toperation.json\n"+
			"100644 blob "+stateBlob+"\tstate.json\n"), "mktree")
	stripped := syncGitInput(t, repo.Root, []byte("workbook: attachment without its blob"),
		"commit-tree", tree, "-p", parent)
	syncGit(t, repo.Root, "update-ref", taskRefPrefix+task.ID, stripped, head)

	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: stripped})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("ReadTaskHead() category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
	if !strings.Contains(err.Error(), attachment.ID) {
		t.Fatalf("refusal = %q, want it to name the attachment %s", err, attachment.ID)
	}
}

// Comments and attachments cross a real remote, in both directions, and the
// bytes arrive with them.
func TestCommentsAndAttachmentsSynchronizeInBothDirections(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Shared task")
	commentOnTask(t, first, config, task.ID, "from the first clone")
	attachment := attachFileToTask(t, first, config, task.ID, "trace.log", "first clone bytes")
	publishTaskRefs(t, first)

	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	fetched := readSyncTask(t, second, config, task.ID)
	if len(fetched.Comments) != 1 || fetched.Comments[0].Body != "from the first clone" {
		t.Fatalf("fetched comments = %#v", fetched.Comments)
	}
	if len(fetched.Attachments) != 1 || fetched.Attachments[0].Blob != attachment.Blob {
		t.Fatalf("fetched attachments = %#v", fetched.Attachments)
	}
	// The blob travelled with the ref, so the other clone reads it without
	// asking origin for anything.
	contents, err := second.ReadAttachment(context.Background(), config, attachment.Blob)
	if err != nil {
		t.Fatalf("ReadAttachment() on the second clone error = %v", err)
	}
	if string(contents) != "first clone bytes" {
		t.Fatalf("fetched attachment contents = %q", contents)
	}

	// And back the other way.
	commentOnTask(t, second, config, task.ID, "from the second clone")
	back := attachFileToTask(t, second, config, task.ID, "reply.log", "second clone bytes")
	publishTaskRefs(t, second)
	if _, err := first.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(back) error = %v", err)
	}
	returned := readSyncTask(t, first, config, task.ID)
	if len(returned.Comments) != 2 {
		t.Fatalf("comments after the round trip = %#v", returned.Comments)
	}
	contents, err = first.ReadAttachment(context.Background(), config, back.Blob)
	if err != nil {
		t.Fatalf("ReadAttachment() on the first clone error = %v", err)
	}
	if string(contents) != "second clone bytes" {
		t.Fatalf("returned attachment contents = %q", contents)
	}
}

// A comment written while offline is replayed onto the fetched tip rather than
// dropped, and the replayed commit carries the attachment blobs its pack names.
func TestDivergentCommentsAndAttachmentsAreReplayed(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Divergent thread")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// Origin moves.
	commentOnTask(t, first, config, task.ID, "upstream remark")
	publishTaskRefs(t, first)

	// The second clone commented and attached while it could not reach origin.
	local := commentOnTask(t, second, config, task.ID, "local remark")
	localComment := local.Task.Comments[len(local.Task.Comments)-1]
	localAttachment := attachFileToTask(t, second, config, task.ID, "local.log", "local bytes")

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(diverged) error = %v", err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v; concurrent comments must not be a conflict", result.Conflicts)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)

	reconciled := readSyncTask(t, second, config, task.ID)
	bodies := map[string]bool{}
	for _, comment := range reconciled.Comments {
		bodies[comment.Body] = true
	}
	if !bodies["upstream remark"] || !bodies["local remark"] {
		t.Fatalf("reconciled thread = %#v, want both remarks", reconciled.Comments)
	}
	if len(reconciled.Comments) != 2 {
		t.Fatalf("reconciled thread = %#v, want exactly two comments", reconciled.Comments)
	}
	// The local comment kept its identity through the replay, which is what
	// makes a later edit of it mean the same thing on both clones.
	if index := indexOfComment(reconciled.Comments, localComment.ID); index < 0 {
		t.Fatalf("the replayed comment lost its identifier: %#v", reconciled.Comments)
	}
	// The replayed attachment commit carries the blob, so the invariant holds
	// on a commit no clone authored directly.
	contents, err := second.ReadAttachment(context.Background(), config, localAttachment.Blob)
	if err != nil {
		t.Fatalf("ReadAttachment() after replay error = %v", err)
	}
	if string(contents) != "local bytes" {
		t.Fatalf("replayed attachment contents = %q", contents)
	}
	head := refValue(t, second, taskRefPrefix+task.ID)
	objects := syncGit(t, second.Root, "rev-list", "--objects", head)
	if !strings.Contains(objects, localAttachment.Blob) {
		t.Fatal("the reconciled history does not reach the replayed attachment's blob")
	}

	// Publishing the replayed history and fetching it back leaves the other
	// clone with the same thread.
	publishTaskRefs(t, second)
	if _, err := first.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(published replay) error = %v", err)
	}
	converged := readSyncTask(t, first, config, task.ID)
	if len(converged.Comments) != len(reconciled.Comments) {
		t.Fatalf("converged thread = %#v, want %#v", converged.Comments, reconciled.Comments)
	}
	for index := range converged.Comments {
		if converged.Comments[index].ID != reconciled.Comments[index].ID ||
			converged.Comments[index].Body != reconciled.Comments[index].Body {
			t.Fatalf("clones disagree about the thread:\n%#v\n%#v", converged.Comments, reconciled.Comments)
		}
	}
}

// A replayed comment must not be mistaken for a pack that changed nothing.
func TestAReplayedCommentIsNotSkippedAsANoOp(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Comment only")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, task.ID, "Retitled upstream")
	publishTaskRefs(t, first)
	commentOnTask(t, second, config, task.ID, "only a comment")

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)
	reconciled := readSyncTask(t, second, config, task.ID)
	if len(reconciled.Comments) != 1 {
		t.Fatalf("reconciled thread = %#v, want the local comment replayed", reconciled.Comments)
	}
	if reconciled.Title != "Retitled upstream" {
		t.Fatalf("reconciled title = %q, want the fetched one", reconciled.Title)
	}
}

func indexOfComment(comments []core.Comment, id string) int {
	for index, comment := range comments {
		if comment.ID == id {
			return index
		}
	}
	return -1
}

func readSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) core.TaskData {
	t.Helper()
	snapshot, err := repo.Get(context.Background(), config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	return snapshot.State.Task
}
