package gitstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A build from a generation this one does not have is the thing these tests
// cannot install, so they forge its commits: a real Git commit whose
// operation.json declares a `minReader` above this build's and whose tree
// carries an entry this build has no name for.
//
// That combination is the one the writer-format contract has to survive, and
// the shape of the next format change rather than a hypothetical: attachments
// are what taught the tree to hold a third entry, and whatever comes after them
// may well add a fourth. If tree shape were judged before the marker, every
// such commit would read as corrupt data on every clone of this release — the
// v0.4.4 wall rebuilt one layer down, and frozen into a release this time.
const forgedFutureGeneration = core.SupportedFormatGeneration + 1

// forgeTaskCommit appends a commit to a task ref, optionally marking its pack
// and checkpoint with a writer-format generation and adding raw tree entries.
func forgeTaskCommit(t *testing.T, repo *Repository, taskID string, generation int, extraEntries ...string) string {
	t.Helper()
	ref := taskRefPrefix + taskID
	head := refValue(t, repo, ref)
	operation := markGeneration(syncGit(t, repo.Root, "show", head+":operation.json"), generation)
	state := markGeneration(syncGit(t, repo.Root, "show", head+":state.json"), generation)

	operationBlob := syncGitInput(t, repo.Root, []byte(operation+"\n"), "hash-object", "-w", "--stdin")
	stateBlob := syncGitInput(t, repo.Root, []byte(state+"\n"), "hash-object", "-w", "--stdin")
	entries := fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob)
	for _, entry := range extraEntries {
		entries += entry + "\n"
	}
	tree := syncGitInput(t, repo.Root, []byte(entries), "mktree")
	commit := syncGitInput(t, repo.Root, []byte("workbook: forged commit"), "commit-tree", tree, "-p", head)
	syncGit(t, repo.Root, "update-ref", ref, commit, head)
	return commit
}

// markGeneration sets a stored document's writer-format marker. A generation of
// zero leaves the document alone, which is how a same-generation forgery is
// built.
//
// An existing marker is replaced rather than preceded, because a document that
// carried two `minReader` members would decode to the last one and quietly test
// nothing: an attachment pack already carries generation one.
var storedMarkerPattern = regexp.MustCompile(`"minReader":\d+`)

func markGeneration(document string, generation int) string {
	if generation == 0 {
		return document
	}
	marker := fmt.Sprintf(`"minReader":%d`, generation)
	if storedMarkerPattern.MatchString(document) {
		return storedMarkerPattern.ReplaceAllString(document, marker)
	}
	return strings.Replace(document, `"version":1,`, `"version":1,`+marker+`,`, 1)
}

// forgedBlobEntry is a raw mktree line for a blob this build has no name for.
func forgedBlobEntry(t *testing.T, repo *Repository, name, contents string) string {
	t.Helper()
	blob := syncGitInput(t, repo.Root, []byte(contents), "hash-object", "-w", "--stdin")
	return fmt.Sprintf("100644 blob %s\t%s", blob, name)
}

// The blocker this fix exists for, stated as the reviewer proved it: a commit
// from a newer generation whose tree carries an entry this build does not
// recognize reads as newer-writer, not as corruption.
func TestATreeEntryFromANewerGenerationReadsAsNewerWriter(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task from the future")
	preview := forgedBlobEntry(t, repo, "preview-01K0M6B8A4FTT8C39MXXYTWP01", "a rendering nobody here can read")
	head := forgeTaskCommit(t, repo, task.ID, forgedFutureGeneration, preview)

	snapshot, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: head})
	if err != nil {
		t.Fatalf("ReadTaskHead() error = %v; a newer generation's tree must still read", err)
	}
	if !snapshot.Operation.RequiresNewerReader() || !snapshot.State.RequiresNewerReader() {
		t.Fatalf("forged tip does not report a newer writer: %#v", snapshot.Operation.MinReader)
	}
	if snapshot.State.Task.Title != task.Title {
		t.Fatalf("checkpoint title = %q, want the stored %q", snapshot.State.Task.Title, task.Title)
	}

	// And the whole read path agrees, because a fetch and a list read tips the
	// same way.
	if _, err := repo.List(context.Background(), config); err != nil {
		t.Fatalf("List() error = %v; one unreadable-shaped tip must not fail the read", err)
	}

	// The refusal that is owed is the mutation's, and it is newer-writer.
	title := "Renamed"
	_, err = threadService(repo, config).UpdateMutation(context.Background(), task.ID, core.UpdateInput{Title: &title})
	if got := core.CategoryOf(err); got != core.CategoryNewerWriter {
		t.Fatalf("UpdateMutation() category = %q, want %q; error = %v", got, core.CategoryNewerWriter, err)
	}
}

// The same entry without a marker is exactly what it looks like: a commit this
// build's own generation cannot explain.
func TestAnUnrecognizedTreeEntryAtThisGenerationIsCorrupt(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task with a strange tree")
	preview := forgedBlobEntry(t, repo, "preview-01K0M6B8A4FTT8C39MXXYTWP01", "unexplained")
	head := forgeTaskCommit(t, repo, task.ID, 0, preview)

	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: head})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("ReadTaskHead() category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
}

// A subtree is judged by the same rule as a blob, on both sides of the marker:
// unrecognized at this generation, left alone above it.
func TestASubtreeEntryFollowsTheSameGenerationRule(t *testing.T) {
	repo, _, config := syncRepositories(t)
	sameGeneration := createSyncTask(t, repo, config, "Task with a subtree")
	newerGeneration := createSyncTask(t, repo, config, "Future task with a subtree")
	nested := syncGitInput(t, repo.Root,
		[]byte(forgedBlobEntry(t, repo, "inner.json", "{}")+"\n"), "mktree")
	subtree := fmt.Sprintf("040000 tree %s\textras", nested)

	head := forgeTaskCommit(t, repo, sameGeneration.ID, 0, subtree)
	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: sameGeneration.ID, ObjectID: head})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("same-generation subtree category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}

	head = forgeTaskCommit(t, repo, newerGeneration.ID, forgedFutureGeneration, subtree)
	if _, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: newerGeneration.ID, ObjectID: head}); err != nil {
		t.Fatalf("newer-generation subtree error = %v; a tree shape this build cannot judge must not be corruption", err)
	}
}

// What stays true at every generation: the two documents are there, and they
// are blobs. A marker does not excuse their absence, because a reader that
// cannot find the checkpoint cannot serve the task at all — which is the one
// thing the contract promises an older clone can still do.
func TestTheTwoDocumentsAreRequiredAtEveryGeneration(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task missing a document")

	ref := taskRefPrefix + task.ID
	head := refValue(t, repo, ref)
	operation := markGeneration(syncGit(t, repo.Root, "show", head+":operation.json"), forgedFutureGeneration)
	operationBlob := syncGitInput(t, repo.Root, []byte(operation+"\n"), "hash-object", "-w", "--stdin")
	tree := syncGitInput(t, repo.Root,
		[]byte(fmt.Sprintf("100644 blob %s\toperation.json\n", operationBlob)), "mktree")
	commit := syncGitInput(t, repo.Root, []byte("workbook: forged commit"), "commit-tree", tree, "-p", head)
	syncGit(t, repo.Root, "update-ref", ref, commit, head)

	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: commit})
	if err == nil {
		t.Fatal("a tree with no state.json read successfully")
	}
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("missing checkpoint category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
}

// The exemption around validateAttachmentBlobs is deliberate and load-bearing,
// and nothing else in the suite would notice if it disappeared.
//
// A newer generation may attach something in a way this build cannot see — a
// blob under a name it does not know, or no blob at all — and judging that would
// turn "written by a newer workbook" back into "corrupt" through a rule about
// data this build did not decode. The same pack at this generation is checked,
// which is the test beside this one.
func TestTheAttachmentBlobCheckIsSkippedForANewerGeneration(t *testing.T) {
	repo, _, config := syncRepositories(t)
	task := createSyncTask(t, repo, config, "Task with an attachment")
	attachment := attachFileToTask(t, repo, config, task.ID, "trace.log", "hello world")

	ref := taskRefPrefix + task.ID
	head := refValue(t, repo, ref)
	parent := syncGit(t, repo.Root, "rev-parse", head+"^")
	operationBlob := syncGit(t, repo.Root, "rev-parse", head+":operation.json")
	stateBlob := syncGit(t, repo.Root, "rev-parse", head+":state.json")

	// The same commit with its attachment blob stripped out of the tree: corrupt
	// at this generation, because this build understands exactly what the pack
	// asked for and can see that it is not there.
	stripped := syncGitInput(t, repo.Root, []byte(fmt.Sprintf(
		"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob)), "mktree")
	sameGeneration := syncGitInput(t, repo.Root, []byte("workbook: forged commit"),
		"commit-tree", stripped, "-p", parent)
	syncGit(t, repo.Root, "update-ref", ref, sameGeneration, head)
	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: sameGeneration})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("same-generation missing blob category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
	if !strings.Contains(err.Error(), attachment.ID) {
		t.Fatalf("refusal = %q, want it to name the attachment", err)
	}

	// The same tree under a newer marker is not this build's to judge.
	markedOperation := markGeneration(syncGit(t, repo.Root, "show", head+":operation.json"), forgedFutureGeneration)
	markedState := markGeneration(syncGit(t, repo.Root, "show", head+":state.json"), forgedFutureGeneration)
	markedOperationBlob := syncGitInput(t, repo.Root, []byte(markedOperation+"\n"), "hash-object", "-w", "--stdin")
	markedStateBlob := syncGitInput(t, repo.Root, []byte(markedState+"\n"), "hash-object", "-w", "--stdin")
	markedTree := syncGitInput(t, repo.Root, []byte(fmt.Sprintf(
		"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		markedOperationBlob, markedStateBlob)), "mktree")
	newerGeneration := syncGitInput(t, repo.Root, []byte("workbook: forged commit"),
		"commit-tree", markedTree, "-p", parent)
	syncGit(t, repo.Root, "update-ref", ref, newerGeneration, sameGeneration)
	if _, err := repo.ReadTaskHead(context.Background(), config,
		TaskHead{TaskID: task.ID, ObjectID: newerGeneration}); err != nil {
		t.Fatalf("newer-generation missing blob error = %v; this build must not judge a tree it cannot read", err)
	}
}

// The configuration ledger inherited the task tree's parser, and with it both
// halves of the bug: the wall in front of a future generation, and an allowance
// for attachment entries in a document where an attachment means nothing.
func TestTheConfigurationLedgerJudgesItsTreeAfterTheMarker(t *testing.T) {
	repo, config := writeRepository(t)
	const ref = configRef
	if _, err := repo.MintConfigLedger(context.Background(), config, core.CryptoULIDSource{}); err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}

	// An entry the ledger has no name for, at this generation, is corruption —
	// including an attachment entry, which the shared parser used to accept.
	head := refValue(t, repo, ref)
	stray := forgedBlobEntry(t, repo, "attachment-01K0M6B8A4FTT8C39MXXYTWA01", "not a thing a ledger holds")
	forged := forgeConfigCommit(t, repo, ref, head, 0, stray)
	// A fresh handle, because a process that already decoded this ledger serves
	// its own memory rather than reading the ref again.
	_, err := reopened(t, repo).LoadVocabulary(context.Background())
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("same-generation ledger entry category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}

	// The same entry under a newer marker reads, because the ledger's whole
	// newer-writer contract is that a status still resolves while the ledger is
	// unfoldable.
	syncGit(t, repo.Root, "update-ref", ref, head, forged)
	forgeConfigCommit(t, repo, ref, head, forgedFutureGeneration, stray)
	if _, err := reopened(t, repo).LoadVocabulary(context.Background()); err != nil {
		t.Fatalf("newer-generation ledger entry error = %v; the ledger must read past a tree it cannot judge", err)
	}
}

func forgeConfigCommit(t *testing.T, repo *Repository, ref, head string, generation int, extraEntries ...string) string {
	t.Helper()
	operation := markGeneration(syncGit(t, repo.Root, "show", head+":operation.json"), generation)
	state := markGeneration(syncGit(t, repo.Root, "show", head+":state.json"), generation)
	operationBlob := syncGitInput(t, repo.Root, []byte(operation+"\n"), "hash-object", "-w", "--stdin")
	stateBlob := syncGitInput(t, repo.Root, []byte(state+"\n"), "hash-object", "-w", "--stdin")
	entries := fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob)
	for _, entry := range extraEntries {
		entries += entry + "\n"
	}
	tree := syncGitInput(t, repo.Root, []byte(entries), "mktree")
	commit := syncGitInput(t, repo.Root, []byte("workbook: forged configuration commit"),
		"commit-tree", tree, "-p", head)
	syncGit(t, repo.Root, "update-ref", ref, commit, head)
	return commit
}

// reopened is the process that has not read this ledger before.
func reopened(t *testing.T, repo *Repository) *Repository {
	t.Helper()
	fresh, err := Open(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return fresh
}
