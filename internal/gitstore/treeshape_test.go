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

// forgeTaskCommit rewrites a task's tip in place: the same parents and the same
// two documents, optionally marked with a writer-format generation, plus
// whatever raw tree entries a test wants to add.
//
// Rewriting rather than appending is what makes these tests mean anything. An
// appended commit reuses the tip's pack, so its logical clock no longer follows
// its parent and a root create suddenly has a parent — and the reader refuses it
// for *that*, with a message about topology, while the assertion reads
// "corrupt-data" and passes. Keeping the parents makes the tree entry the only
// variable, which is the only way a test of the tree entry proves anything. The
// message assertions beside each one are the second guard.
func forgeTaskCommit(t *testing.T, repo *Repository, taskID string, generation int, extraEntries ...string) string {
	t.Helper()
	head := refValue(t, repo, taskRefPrefix+taskID)
	operationBlob := forgedDocumentBlob(t, repo, head, "operation.json", generation)
	stateBlob := forgedDocumentBlob(t, repo, head, "state.json", generation)
	entries := []string{
		fmt.Sprintf("100644 blob %s\toperation.json", operationBlob),
		fmt.Sprintf("100644 blob %s\tstate.json", stateBlob),
	}
	return forgeTaskTree(t, repo, taskID, append(entries, extraEntries...))
}

// forgeTaskTree replaces a task tip's tree with exactly the entries given,
// keeping the commit's parents, and points the ref at the result.
func forgeTaskTree(t *testing.T, repo *Repository, taskID string, entries []string) string {
	t.Helper()
	ref := taskRefPrefix + taskID
	head := refValue(t, repo, ref)
	tree := syncGitInput(t, repo.Root, []byte(strings.Join(entries, "\n")+"\n"), "mktree")

	arguments := []string{"commit-tree", tree}
	for _, parent := range strings.Fields(syncGit(t, repo.Root, "rev-list", "--parents", "-n", "1", head))[1:] {
		arguments = append(arguments, "-p", parent)
	}
	commit := syncGitInput(t, repo.Root, []byte("workbook: forged commit"), arguments...)
	syncGit(t, repo.Root, "update-ref", ref, commit, head)
	return commit
}

// forgedDocumentBlob rewrites one stored document at the given generation and
// returns the blob it hashed to.
func forgedDocumentBlob(t *testing.T, repo *Repository, commit, name string, generation int) string {
	t.Helper()
	document := markGeneration(syncGit(t, repo.Root, "show", commit+":"+name), generation)
	return syncGitInput(t, repo.Root, []byte(document+"\n"), "hash-object", "-w", "--stdin")
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
	if !strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("refusal = %q, want the name rule rather than some other corruption", err)
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
	if !strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("refusal = %q, want the name rule; this fixture's entry is named `extras`", err)
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

	head := refValue(t, repo, taskRefPrefix+task.ID)
	operationBlob := forgedDocumentBlob(t, repo, head, "operation.json", forgedFutureGeneration)
	commit := forgeTaskTree(t, repo, task.ID, []string{
		fmt.Sprintf("100644 blob %s\toperation.json", operationBlob),
	})

	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: commit})
	if err == nil {
		t.Fatal("a tree with no state.json read successfully")
	}
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("missing checkpoint category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
}

// A recognized name is a name this build also knows the shape of.
//
// This is the regression the round-2 review found. Moving the mode check out of
// the parser and onto the two documents alone left an entry *named*
// `attachment-<ULID>` accepted whatever it was stored as — a subtree, an
// executable, a symlink. Nothing follows such an entry, so nothing was unsafe;
// what was lost is that `validate` blessed a commit every earlier build called
// corrupt, silently. The name and the mode are one rule and are checked
// together, inside the marker gate, so a newer generation is still not judged.
func TestARecognizedAttachmentEntryMustBeARegularBlob(t *testing.T) {
	const name = "attachment-01K0M6B8A4FTT8C39MXXYTWA01"
	for _, mode := range []struct {
		name   string
		mode   string
		kind   string
		nested bool
	}{
		{"a subtree", "040000", "tree", true},
		{"an executable", "100755", "blob", false},
		{"a symlink", "120000", "blob", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			// A repository per case, and both tasks created before anything is
			// forged: a forged tip is unreadable, and every mutation reads every
			// tip, so a fixture built after one would fail on the forgery rather
			// than on what it came to test.
			repo, _, config := syncRepositories(t)
			task := createSyncTask(t, repo, config, "Task with "+mode.name)
			future := createSyncTask(t, repo, config, "Future task with "+mode.name)
			object := syncGitInput(t, repo.Root, []byte("attached bytes"), "hash-object", "-w", "--stdin")
			if mode.nested {
				object = syncGitInput(t, repo.Root,
					[]byte(forgedBlobEntry(t, repo, "inner.json", "{}")+"\n"), "mktree")
			}
			entry := fmt.Sprintf("%s %s %s\t%s", mode.mode, mode.kind, object, name)

			head := forgeTaskCommit(t, repo, task.ID, 0, entry)
			_, err := repo.ReadTaskHead(context.Background(), config,
				TaskHead{TaskID: task.ID, ObjectID: head})
			if got := core.CategoryOf(err); got != core.CategoryCorruptData {
				t.Fatalf("ReadTaskHead() category = %q, want %q; error = %v",
					got, core.CategoryCorruptData, err)
			}
			if !strings.Contains(err.Error(), "not a regular blob") {
				t.Fatalf("refusal = %q, want the mode rule", err)
			}

			// The same entry under a newer marker is still not this build's to
			// judge, which is what keeps the fix from re-erecting the wall.
			head = forgeTaskCommit(t, repo, future.ID, forgedFutureGeneration, entry)
			if _, err := repo.ReadTaskHead(context.Background(), config,
				TaskHead{TaskID: future.ID, ObjectID: head}); err != nil {
				t.Fatalf("newer-generation error = %v; a tree this build cannot judge must not be corruption", err)
			}
		})
	}
}

// The other half of the structural floor: the two documents must be regular
// blobs, at every generation.
//
// An executable is the precise probe. A subtree or a symlink under those names
// fails earlier, on "task documents are not blobs", because the object Git hands
// back for `<commit>:state.json` is then not a blob at all; a 100755 entry
// pointing at a real blob passes every other check and reaches this one alone.
func TestTheTwoDocumentsMustBeRegularBlobsAtEveryGeneration(t *testing.T) {
	for _, generation := range []int{0, forgedFutureGeneration} {
		t.Run(fmt.Sprintf("generation %d", generation), func(t *testing.T) {
			repo, _, config := syncRepositories(t)
			task := createSyncTask(t, repo, config, "Task with an executable checkpoint")
			head := refValue(t, repo, taskRefPrefix+task.ID)
			operationBlob := forgedDocumentBlob(t, repo, head, "operation.json", generation)
			stateBlob := forgedDocumentBlob(t, repo, head, "state.json", generation)
			commit := forgeTaskTree(t, repo, task.ID, []string{
				fmt.Sprintf("100644 blob %s\toperation.json", operationBlob),
				fmt.Sprintf("100755 blob %s\tstate.json", stateBlob),
			})

			_, err := repo.ReadTaskHead(context.Background(), config,
				TaskHead{TaskID: task.ID, ObjectID: commit})
			if got := core.CategoryOf(err); got != core.CategoryCorruptData {
				t.Fatalf("ReadTaskHead() category = %q, want %q; error = %v",
					got, core.CategoryCorruptData, err)
			}
			if !strings.Contains(err.Error(), "not a regular blob") {
				t.Fatalf("refusal = %q, want the mode rule", err)
			}
		})
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

	head := refValue(t, repo, taskRefPrefix+task.ID)
	operationBlob := syncGit(t, repo.Root, "rev-parse", head+":operation.json")
	stateBlob := syncGit(t, repo.Root, "rev-parse", head+":state.json")

	// The same commit with its attachment blob stripped out of the tree: corrupt
	// at this generation, because this build understands exactly what the pack
	// asked for and can see that it is not there.
	sameGeneration := forgeTaskTree(t, repo, task.ID, []string{
		fmt.Sprintf("100644 blob %s\toperation.json", operationBlob),
		fmt.Sprintf("100644 blob %s\tstate.json", stateBlob),
	})
	_, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: sameGeneration})
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("same-generation missing blob category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
	}
	if !strings.Contains(err.Error(), attachment.ID) {
		t.Fatalf("refusal = %q, want it to name the attachment", err)
	}

	// The same tree under a newer marker is not this build's to judge.
	markedOperationBlob := forgedDocumentBlob(t, repo, head, "operation.json", forgedFutureGeneration)
	markedStateBlob := forgedDocumentBlob(t, repo, head, "state.json", forgedFutureGeneration)
	newerGeneration := forgeTaskTree(t, repo, task.ID, []string{
		fmt.Sprintf("100644 blob %s\toperation.json", markedOperationBlob),
		fmt.Sprintf("100644 blob %s\tstate.json", markedStateBlob),
	})
	if _, err := repo.ReadTaskHead(context.Background(), config,
		TaskHead{TaskID: task.ID, ObjectID: newerGeneration}); err != nil {
		t.Fatalf("newer-generation missing blob error = %v; this build must not judge a tree it cannot read", err)
	}
}

// The configuration ledger inherited the task tree's parser, and with it both
// halves of the bug: the wall in front of a future generation, and an allowance
// for attachment entries in a document where an attachment means nothing.
//
// The forgery rewrites the ledger tip's tree in place, keeping its parents. The
// first version of this test appended a commit to a freshly minted, one-commit
// ledger, which made the forged commit a genesis with a parent — corrupt with
// or without the stray entry, so it passed no matter what the entry rule did.
// Keeping the parents makes the tree entry the only variable, and the message
// assertions say which rule answered.
func TestTheConfigurationLedgerJudgesItsTreeAfterTheMarker(t *testing.T) {
	// An entry the ledger has no name for, at this generation, is corruption —
	// including an attachment entry, which the shared parser used to accept.
	t.Run("this generation", func(t *testing.T) {
		repo := mintedLedgerRepository(t)
		stray := forgedBlobEntry(t, repo, "attachment-01K0M6B8A4FTT8C39MXXYTWA01", "not a thing a ledger holds")
		forgeConfigTree(t, repo, 0, stray)
		// A fresh handle, because a process that already decoded this ledger
		// serves its own memory rather than reading the ref again.
		_, err := reopened(t, repo).LoadVocabulary(context.Background())
		if got := core.CategoryOf(err); got != core.CategoryCorruptData {
			t.Fatalf("category = %q, want %q; error = %v", got, core.CategoryCorruptData, err)
		}
		if !strings.Contains(err.Error(), "unexpected entry") {
			t.Fatalf("refusal = %q, want the entry rule rather than some other corruption", err)
		}
	})

	// The same entry under a newer marker reads, because the ledger's whole
	// newer-writer contract is that a status still resolves while the ledger is
	// unfoldable.
	t.Run("a newer generation", func(t *testing.T) {
		repo := mintedLedgerRepository(t)
		stray := forgedBlobEntry(t, repo, "attachment-01K0M6B8A4FTT8C39MXXYTWA01", "not a thing a ledger holds")
		forgeConfigTree(t, repo, forgedFutureGeneration, stray)
		if _, err := reopened(t, repo).LoadVocabulary(context.Background()); err != nil {
			t.Fatalf("error = %v; the ledger must read past a tree it cannot judge", err)
		}
	})

	// And an untouched ledger reads, which is what says the forgery above is
	// what the refusal is about.
	t.Run("untouched", func(t *testing.T) {
		repo := mintedLedgerRepository(t)
		if _, err := reopened(t, repo).LoadVocabulary(context.Background()); err != nil {
			t.Fatalf("error = %v; an unforged ledger must read", err)
		}
	})
}

func mintedLedgerRepository(t *testing.T) *Repository {
	t.Helper()
	repo, config := writeRepository(t)
	if _, err := repo.MintConfigLedger(context.Background(), config, core.CryptoULIDSource{}); err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}
	return repo
}

// forgeConfigTree rewrites the ledger tip's tree in place, keeping its parents
// — none, for the genesis this fixture mints — so that the entries are the only
// thing the reader can be answering about.
func forgeConfigTree(t *testing.T, repo *Repository, generation int, extraEntries ...string) string {
	t.Helper()
	head := refValue(t, repo, configRef)
	entries := []string{
		fmt.Sprintf("100644 blob %s\t%s", forgedDocumentBlob(t, repo, head, configOperationPath, generation),
			configOperationPath),
		fmt.Sprintf("100644 blob %s\t%s", forgedDocumentBlob(t, repo, head, configStatePath, generation),
			configStatePath),
	}
	tree := syncGitInput(t, repo.Root,
		[]byte(strings.Join(append(entries, extraEntries...), "\n")+"\n"), "mktree")

	arguments := []string{"commit-tree", tree}
	for _, parent := range strings.Fields(syncGit(t, repo.Root, "rev-list", "--parents", "-n", "1", head))[1:] {
		arguments = append(arguments, "-p", parent)
	}
	commit := syncGitInput(t, repo.Root, []byte("workbook: forged configuration commit"), arguments...)
	syncGit(t, repo.Root, "update-ref", configRef, commit, head)
	return commit
}

func reopened(t *testing.T, repo *Repository) *Repository {
	t.Helper()
	fresh, err := Open(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return fresh
}
