package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// bracketedEmail is a user.email Git accepts and quietly stores without its
// brackets, so whoever configured it never sees anything wrong. Workbook
// records it as given, so the operation and the commit Git wrote for it
// disagree from the first edit, and no replay can name it exactly.
const bracketedEmail = "<bob@example.test>"

// commitIdentity reads a commit's author and committer as Git prints them.
func commitIdentity(t *testing.T, repository, commit string) (author, committer string) {
	t.Helper()
	identities := gitOutput(t, repository, "show", "--no-patch", "--format=%an <%ae>%x00%cn <%ce>", commit)
	author, committer, _ = strings.Cut(identities, "\x00")
	return author, committer
}

// assertReplayedUnderTheOriginalIdentity requires replayed to re-record
// original's operation and to carry the author and committer Git recorded on
// original, the commit its edit was first made as.
func assertReplayedUnderTheOriginalIdentity(t *testing.T, repository, replayed, original string) {
	t.Helper()
	if replayed == original {
		t.Fatalf("commit %s was not replayed", original)
	}
	wantAuthor, wantCommitter := commitIdentity(t, repository, original)
	if wantAuthor != "Bob <bob@example.test>" {
		t.Fatalf("original commit %s author = %q; the test expects Git's own stripped identity", original, wantAuthor)
	}
	author, committer := commitIdentity(t, repository, replayed)
	if author != wantAuthor || committer != wantCommitter {
		t.Errorf("replayed commit %s author %q, committer %q; want the original's %q, %q",
			replayed, author, committer, wantAuthor, wantCommitter)
	}
	if got, want := operationActorOf(t, repository, replayed), operationActorOf(t, repository, original); got != want || got != bracketedEmail {
		t.Errorf("replayed operation actor = %q, original %q; want both %q", got, want, bracketedEmail)
	}
}

// operationActorOf reads the actor out of the operation blob a commit carries.
func operationActorOf(t *testing.T, repository, commit string) string {
	t.Helper()
	var pack struct {
		Actor core.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(gitOutput(t, repository, "show", commit+":operation.json")), &pack); err != nil {
		t.Fatalf("decode %s operation: %v", commit, err)
	}
	return pack.Actor.ID
}

// Bob's user.email has brackets around it. Bob comments on a Done task and
// publishes; Alice, who has not fetched the comment, compacts the task and
// syncs. The sync replays Bob's comment onto Alice's checkpoint, and the
// comment's recorded name is one Git cannot store exactly. Refusing it would
// leave the task unable to synchronize forever, since nobody can change a name
// already in history, so the replay takes the identity Git recorded on Bob's
// own commit instead, and the compaction publishes.
func TestAReplayOfANameGitCannotStoreTakesTheOriginalCommitsIdentity(t *testing.T) {
	t.Parallel()
	alice, bob := cliSyncRepositories(t)
	gitOutput(t, alice, "config", "user.name", "Alice")
	gitOutput(t, alice, "config", "user.email", "alice@example.test")
	gitOutput(t, bob, "config", "user.name", "Bob")
	gitOutput(t, bob, "config", "user.email", bracketedEmail)

	task := publishedDoneTask(t, alice, "Done and dusted")
	compactRun(t, bob, "sync")
	compactRun(t, bob, "update", task.ID, "--comment", "one more thing", "--no-sync")
	compactRun(t, bob, "push")
	bobs := remoteTaskRefValue(t, bob, task.ID)

	compactRun(t, alice, "compact", "--done-only", "--no-sync", "--no-backup")
	code, stdout, stderr := run(t, alice, "sync")
	if code != 0 {
		t.Fatalf("sync after compacting code = %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "replayed 1 operation(s) from origin onto the local checkpoint") {
		t.Errorf("sync stdout = %q, want Bob's comment replayed onto the checkpoint", stdout)
	}
	local := taskRefs(t, alice)[task.ID]
	if got := remoteTaskRefValue(t, alice, task.ID); got != local {
		t.Fatalf("origin holds %s, want the compacted tip %s published", got, local)
	}
	assertReplayedUnderTheOriginalIdentity(t, alice, local, bobs)
	if shown := showTask(t, alice, task.ID); len(shown.Comments) != 1 || shown.Comments[0].Author != bracketedEmail {
		t.Errorf("comments = %#v, want Bob's, under the name it was recorded under", shown.Comments)
	}
	// And it stays synchronized.
	if code, stdout, stderr := run(t, alice, "sync"); code != 0 {
		t.Fatalf("second sync code = %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

// Bob records an edit under the bracketed email, corrects his user.email, and
// only then syncs onto a teammate's change to the same task. The replay of his
// own edit no longer matches the checkout's email and cannot be named exactly,
// so it takes the identity of the commit he first wrote, and publishes.
func TestAReplayAfterCorrectingYourOwnEmailPublishes(t *testing.T) {
	t.Parallel()
	alice, bob := cliSyncRepositories(t)
	gitOutput(t, bob, "config", "user.name", "Bob")
	gitOutput(t, bob, "config", "user.email", bracketedEmail)

	task := cliCreateTask(t, alice, "Shared")
	compactRun(t, alice, "push")
	compactRun(t, bob, "sync")
	compactRun(t, alice, "update", task.ID, "--title", "Shared, renamed by Alice")
	compactRun(t, bob, "update", task.ID, "--priority", "high", "--no-sync")
	bobs := taskRefs(t, bob)[task.ID]

	gitOutput(t, bob, "config", "user.email", "bob@example.test")
	code, stdout, stderr := run(t, bob, "sync")
	if code != 0 {
		t.Fatalf("sync after correcting the email code = %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	local := taskRefs(t, bob)[task.ID]
	if got := remoteTaskRefValue(t, bob, task.ID); got != local {
		t.Fatalf("origin holds %s, want the replayed tip %s published", got, local)
	}
	assertReplayedUnderTheOriginalIdentity(t, bob, local, bobs)
	if shown := showTask(t, bob, task.ID); shown.Title != "Shared, renamed by Alice" || shown.Priority != core.PriorityHigh {
		t.Errorf("shown = %q at %q, want both edits", shown.Title, shown.Priority)
	}
}

// Only a replay is spared: a new edit naming such a person is still refused
// before anything is written.
func TestANewEditUnderANameGitCannotStoreIsStillRefused(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	existing := createReadyTask(t, repository, "Existing")
	refsBefore := gitOutput(t, repository, "for-each-ref", "refs/workbook/")
	code, stdout, stderr := runAs(t, repository, bracketedEmail, "update", existing.ID, "--comment", "refused", "--no-sync", "--json")
	if code != 5 || stdout != "" {
		t.Fatalf("WORKBOOK_ACTOR=%q update code = %d, stdout = %q; want 5 and nothing", bracketedEmail, code, stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	if refsAfter := gitOutput(t, repository, "for-each-ref", "refs/workbook/"); refsAfter != refsBefore {
		t.Fatalf("a refused edit moved refs:\nbefore %s\nafter  %s", refsBefore, refsAfter)
	}
}
