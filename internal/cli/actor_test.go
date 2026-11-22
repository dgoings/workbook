package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// runWithActorEnvironment runs a command with an environment that holds only
// what env says, so a test about WORKBOOK_ACTOR can run beside every other test
// in the package without touching the process environment they all read. A nil
// env is an environment with the variable unset.
func runWithActorEnvironment(t *testing.T, cwd string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	lookup := func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
	var stdout, stderr bytes.Buffer
	code := runWithEnvironment(context.Background(), lookup, args, cwd, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// runAs runs a command as the person actor names.
func runAs(t *testing.T, cwd, actor string, args ...string) (int, string, string) {
	t.Helper()
	return runWithActorEnvironment(t, cwd, map[string]string{core.ActorVariable: actor}, args...)
}

// mustRunAs runs a mutation as actor and returns the task it answered with.
func mustRunAs(t *testing.T, cwd, actor string, args ...string) core.Task {
	t.Helper()
	code, stdout, stderr := runAs(t, cwd, actor, append(args, "--no-sync", "--json")...)
	if code != 0 {
		t.Fatalf("%s as %s code = %d, want 0; stderr = %q", strings.Join(args, " "), actor, code, stderr)
	}
	return decodeMutationTask(t, stdout, args[0])
}

// withoutGitIdentity strips a checkout of every Git identity it has. Setting
// useConfigOnly is what makes "none" true everywhere: without it Git guesses an
// address from the login and host name, and on a machine whose host name has a
// dot in it the guess succeeds.
func withoutGitIdentity(t *testing.T, repository string) {
	t.Helper()
	gitOutput(t, repository, "config", "--unset", "user.email")
	gitOutput(t, repository, "config", "--unset", "user.name")
	gitOutput(t, repository, "config", "user.useConfigOnly", "true")
}

// taskCommits lists the commits on a task's ref, newest first, each as the
// author and committer Git prints and the actor its operation blob records.
type taskCommit struct {
	commit, author, committer, actor string
}

func taskCommits(t *testing.T, repository, taskID string) []taskCommit {
	t.Helper()
	ref := "refs/workbook/tasks/" + taskID
	var commits []taskCommit
	for _, commit := range strings.Fields(gitOutput(t, repository, "rev-list", ref)) {
		identities := gitOutput(t, repository, "show", "--no-patch", "--format=%an <%ae>%x00%cn <%ce>", commit)
		author, committer, _ := strings.Cut(identities, "\x00")
		var pack struct {
			Actor core.Actor `json:"actor"`
		}
		if err := json.Unmarshal([]byte(gitOutput(t, repository, "show", commit+":operation.json")), &pack); err != nil {
			t.Fatalf("decode %s operation: %v", commit, err)
		}
		commits = append(commits, taskCommit{commit: commit, author: author, committer: committer, actor: pack.Actor.ID})
	}
	return commits
}

// recordEveryKindOfEdit makes, as actor, each edit the story names — a create,
// an update, a comment, an attachment and an assignment — and returns the task.
func recordEveryKindOfEdit(t *testing.T, repository, actor string) core.Task {
	t.Helper()
	attachment := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(attachment, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := mustRunAs(t, repository, actor, "create", "Hosted task", "--status", "ready")
	mustRunAs(t, repository, actor, "update", created.ID, "--title", "Hosted task, renamed")
	mustRunAs(t, repository, actor, "update", created.ID, "--comment", "picking this up")
	mustRunAs(t, repository, actor, "update", created.ID, "--attach-file", attachment)
	mustRunAs(t, repository, actor, "update", created.ID, "--attach-url", "https://example.com/pr/1")
	return mustRunAs(t, repository, actor, "update", created.ID, "--assign", "self")
}

// assertRecordedUnder requires every trace of a task's edits to name actor: the
// change log `show --history` prints, the comment and attachment authors, the
// assignment's principal and creator, and the operation and commit behind each
// change.
func assertRecordedUnder(t *testing.T, repository string, task core.Task, actor string) {
	t.Helper()
	code, stdout, stderr := runAs(t, repository, actor, "show", task.ID, "--history", "--json")
	if code != 0 {
		t.Fatalf("show --history code = %d; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if detail.History == nil || detail.History.Total != 6 {
		t.Fatalf("history = %#v, want the create and five updates", detail.History)
	}
	for _, change := range detail.History.Changes {
		if change.Actor != actor {
			t.Errorf("history change %s actor = %q, want %q", change.Commit, change.Actor, actor)
		}
	}
	// The plain-text history names the person too, for the reader who never
	// asks for JSON.
	code, stdout, stderr = runAs(t, repository, actor, "show", task.ID, "--history")
	if code != 0 || strings.Count(stdout, actor) < 6 {
		t.Errorf("show --history code = %d names %s %d times, want every change; stdout = %q, stderr = %q",
			code, actor, strings.Count(stdout, actor), stdout, stderr)
	}

	if len(task.Comments) != 1 || task.Comments[0].Author != actor {
		t.Errorf("comments = %#v, want one by %s", task.Comments, actor)
	}
	if len(task.Attachments) != 2 {
		t.Errorf("attachments = %#v, want the file and the link", task.Attachments)
	}
	for _, attachment := range task.Attachments {
		if attachment.Author != actor {
			t.Errorf("attachment %s author = %q, want %q", attachment.ID, attachment.Author, actor)
		}
	}
	if len(task.Assignments) != 1 || task.Assignments[0].Principal != actor || task.Assignments[0].Creator != actor {
		t.Errorf("assignments = %#v, want one held and recorded by %s", task.Assignments, actor)
	}

	want := actor + " <" + actor + ">"
	for _, commit := range taskCommits(t, repository, task.ID) {
		if commit.actor != actor || commit.author != want || commit.committer != want {
			t.Errorf("commit %s: actor %q, author %q, committer %q; want %s throughout",
				commit.commit, commit.actor, commit.author, commit.committer, actor)
		}
	}
}

func TestWorkbookActorRecordsEveryEditUnderTheGivenName(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := recordEveryKindOfEdit(t, repository, "alice@example.com")
	assertRecordedUnder(t, repository, task, "alice@example.com")

	// The checkout's own identity is left as it was: the variable names the
	// person for one command, it does not reconfigure the folder.
	if got := gitOutput(t, repository, "config", "user.email"); got != assignTestActor {
		t.Fatalf("user.email = %q after edits under WORKBOOK_ACTOR, want it untouched", got)
	}
}

func TestWorkbookActorWorksInAFolderWithNoGitIdentity(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	withoutGitIdentity(t, repository)

	// The folder really has none: without the variable a write is refused.
	if code, _, stderr := runWithActorEnvironment(t, repository, nil, "create", "Nobody's", "--no-sync"); code == 0 {
		t.Fatalf("create in a folder with no user.email succeeded without WORKBOOK_ACTOR; stderr = %q", stderr)
	}

	task := recordEveryKindOfEdit(t, repository, "alice@example.com")
	assertRecordedUnder(t, repository, task, "alice@example.com")
	code, stdout, stderr := runAs(t, repository, "alice@example.com", "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next code = %d; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != task.ID {
		t.Fatalf("next = %#v, want alice's own task %s", got, task.ID)
	}
	code, _, stderr = runAs(t, repository, "alice@example.com", "update", task.ID, "--unassign", "alice@example.com", "--no-sync")
	if code != 0 {
		t.Fatalf("unassign code = %d; stderr = %q", code, stderr)
	}
}

// Everything Workbook decides by identity takes the given name as the person:
// what `self` means, which tasks `next` treats as somebody else's, and who may
// withdraw an assignment.
func TestWorkbookActorIsThePersonForSelfNextAndRemoval(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	const alice, bob = "alice@example.com", "bob@example.com"
	bobs := mustRunAs(t, repository, alice, "create", "Bob's", "--status", "ready")
	alices := mustRunAs(t, repository, alice, "create", "Alice's", "--status", "ready")

	held := mustRunAs(t, repository, bob, "update", bobs.ID, "--assign", "self/impl-1")
	assertAssignments(t, held, bob+"/impl-1")
	if held.Assignments[0].Creator != bob {
		t.Fatalf("creator = %q, want %q", held.Assignments[0].Creator, bob)
	}

	// next, as alice, passes over the task bob holds, and as bob does not.
	code, stdout, stderr := runAs(t, repository, alice, "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next as alice code = %d; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != alices.ID {
		t.Fatalf("next as alice = %#v, want %s, the task bob does not hold", got, alices.ID)
	}
	code, stdout, stderr = runAs(t, repository, bob, "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next as bob code = %d; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != bobs.ID {
		t.Fatalf("next as bob = %#v, want %s, which bob holds himself", got, bobs.ID)
	}
	// And a claim records the claimant.
	code, stdout, stderr = runAs(t, repository, alice, "next", "--claim", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next --claim as alice code = %d; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != alices.ID {
		t.Fatalf("next --claim as alice = %#v, want %s", got, alices.ID)
	} else {
		assertAssignments(t, *got, alice)
	}

	// Removal: alice is neither bob nor the one who recorded bob's assignment,
	// so she is refused, and so is the checkout's own identity.
	code, _, stderr = runAs(t, repository, alice, "update", bobs.ID, "--unassign", bob+"/impl-1", "--no-sync", "--json")
	if code != 5 || !strings.Contains(stderr, "may be removed only by") {
		t.Fatalf("alice removing bob's assignment code = %d, stderr = %q; want the removal rule's refusal", code, stderr)
	}
	code, _, stderr = runWithActorEnvironment(t, repository, nil, "update", bobs.ID, "--unassign", bob+"/impl-1", "--no-sync", "--json")
	if code != 5 || !strings.Contains(stderr, "may be removed only by") {
		t.Fatalf("the checkout removing bob's assignment code = %d, stderr = %q; want the removal rule's refusal", code, stderr)
	}
	released := mustRunAs(t, repository, bob, "update", bobs.ID, "--unassign", "self/impl-1")
	assertAssignments(t, released)

	// The creator half of the rule: alice tags sam, and alice — not sam's
	// absent self, and not bob — may take it back.
	tagged := mustRunAs(t, repository, alice, "update", bobs.ID, "--assign", "sam@example.com")
	assertAssignments(t, tagged, "sam@example.com")
	code, _, stderr = runAs(t, repository, bob, "update", bobs.ID, "--unassign", "sam@example.com", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("bob removing alice's tag of sam code = %d, want 5; stderr = %q", code, stderr)
	}
	untagged := mustRunAs(t, repository, alice, "update", bobs.ID, "--unassign", "sam@example.com")
	assertAssignments(t, untagged)
}

func TestWorkbookActorRefusesAnEmptyOrUnusableValue(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	existing := createReadyTask(t, repository, "Existing")
	refsBefore := gitOutput(t, repository, "for-each-ref", "refs/workbook/")

	for _, value := range []string{"", " ", "alice", "alice@example", "alice@example.com/impl-1", "alice@example.com ", "a<b@example.com", "s\xffam@example.com"} {
		for _, args := range [][]string{
			{"create", "Refused", "--no-sync", "--json"},
			{"update", existing.ID, "--title", "Refused", "--no-sync", "--json"},
			{"update", existing.ID, "--assign", "self", "--no-sync", "--json"},
			{"next", "--no-sync", "--json"},
			{"list", "--json"},
		} {
			code, stdout, stderr := runAs(t, repository, value, args...)
			if code != 5 {
				t.Errorf("WORKBOOK_ACTOR=%q %s code = %d, want 5; stdout = %q, stderr = %q", value, args[0], code, stdout, stderr)
				continue
			}
			assertJSONError(t, stderr, core.CategoryValidation, "")
			if !strings.Contains(stderr, core.ActorVariable) {
				t.Errorf("WORKBOOK_ACTOR=%q %s stderr = %q, want it to name the variable", value, args[0], stderr)
			}
			if stdout != "" {
				t.Errorf("WORKBOOK_ACTOR=%q %s wrote %q to stdout", value, args[0], stdout)
			}
		}
	}
	if refsAfter := gitOutput(t, repository, "for-each-ref", "refs/workbook/"); refsAfter != refsBefore {
		t.Fatalf("a refused WORKBOOK_ACTOR moved refs:\nbefore %s\nafter  %s", refsBefore, refsAfter)
	}

	// The plain-text refusal says the same, for the person at a terminal.
	// Both say why, not only that: the reason is what somebody needs to fix the
	// value.
	code, _, stderr := runAs(t, repository, "", "create", "Refused", "--no-sync")
	if code != 5 || !strings.Contains(stderr, "WORKBOOK_ACTOR is set but unusable: the acting person must not be blank") {
		t.Fatalf("plain refusal code = %d, stderr = %q", code, stderr)
	}
	code, _, stderr = runAs(t, repository, "alice@example.com/impl-1", "create", "Refused", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("labelled refusal code = %d, stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`WORKBOOK_ACTOR is set but unusable: the acting person "alice@example.com/impl-1" must be an email address with no /label`)
}

// version and completion open no repository and record nothing, so a bad value
// is not theirs to refuse; a desktop app probing its binary should not fail on
// a variable meant for the commands that write.
func TestWorkbookActorDoesNotGateCommandsThatRecordNothing(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"completion", "bash"}} {
		if code, _, stderr := runAs(t, t.TempDir(), "", args...); code != 0 {
			t.Errorf("%s with an empty WORKBOOK_ACTOR code = %d; stderr = %q", args[0], code, stderr)
		}
	}
}

// With the variable unset, every edit is the one this build wrote before the
// variable existed: recorded under the checkout's user.email, and committed
// under the checkout's own Git name and email. The gitstore test
// TestEveryCommitAnOrdinaryCloneWritesIsTheOneGitWritesUnaided proves, for
// every kind of commit Workbook writes, that such a commit is byte for byte
// the one plain Git writes; this checks only that the command line's edits
// reach that path with the checkout's identity, by running the same edits that,
// with the variable set, are recorded under somebody else.
func TestUnsetWorkbookActorWritesWhatItWroteBefore(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	gitOutput(t, repository, "config", "user.name", "Checkout Owner")
	gitOutput(t, repository, "config", "user.email", "owner@example.test")

	attachment := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(attachment, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unset := func(args ...string) core.Task {
		t.Helper()
		code, stdout, stderr := runWithActorEnvironment(t, repository, nil, append(args, "--no-sync", "--json")...)
		if code != 0 {
			t.Fatalf("%s code = %d; stderr = %q", strings.Join(args, " "), code, stderr)
		}
		return decodeMutationTask(t, stdout, args[0])
	}
	created := unset("create", "Ordinary", "--status", "ready")
	unset("update", created.ID, "--title", "Ordinary, renamed")
	unset("update", created.ID, "--comment", "noted")
	unset("update", created.ID, "--attach-file", attachment)
	task := unset("update", created.ID, "--assign", "self")

	commits := taskCommits(t, repository, task.ID)
	if len(commits) != 5 {
		t.Fatalf("commits = %d, want 5", len(commits))
	}
	for _, commit := range commits {
		if commit.actor != "owner@example.test" ||
			commit.author != "Checkout Owner <owner@example.test>" ||
			commit.committer != "Checkout Owner <owner@example.test>" {
			t.Errorf("commit %s: actor %q, author %q, committer %q; want the checkout's own identity throughout",
				commit.commit, commit.actor, commit.author, commit.committer)
		}
	}
	if task.Comments[0].Author != "owner@example.test" || task.Attachments[0].Author != "owner@example.test" {
		t.Errorf("comment author %q, attachment author %q; want the checkout's user.email",
			task.Comments[0].Author, task.Attachments[0].Author)
	}
	assertAssignments(t, task, "owner@example.test")
}

// The board records its edits under the person serve was started as. A
// request's context belongs to the HTTP server rather than to the command, so
// this is the path that could keep the operation's person and lose the
// commit's: the service each route builds was handed the actor when serve
// opened, and only the request's context reaches the commit writer. A folder
// with no Git identity of its own is the case that shows it, because there a
// commit with nobody to write it as is refused outright.
func TestServeRecordsBoardEditsUnderWorkbookActor(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	withoutGitIdentity(t, repository)
	const alice = "alice@example.com"

	parent, err := core.WithActor(context.Background(), alice)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(parent)
	reserved := reserveListener(t)
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServeWith(ctx, reserved.listen, []string{"--addr", reserved.addr}, repository, &serveStdout, &serveStderr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runServeWith() error = %v; stderr = %q", err, serveStderr.String())
		}
	})
	waitForHTTP(t, "http://"+reserved.addr+"/healthz")
	base := "http://" + reserved.addr

	body, status := boardRequest(t, http.MethodPost, base+"/api/tasks",
		`{"title":"Filed on the board","description":"","status":"ready","priority":"medium","labels":[]}`)
	created := decodeServeMutation(t, body, status)
	body, status = boardRequest(t, http.MethodPost, base+"/api/tasks/"+created.ID+"/assignments",
		`{"to":"","expectedHead":"`+created.Head+`"}`)
	assigned, _ := decodeServeAssignment(t, body, status)
	if len(assigned.Assignments) != 1 || assigned.Assignments[0].Principal != alice || assigned.Assignments[0].Creator != alice {
		t.Fatalf("the board assigned %#v, want alice as principal and creator", assigned.Assignments)
	}

	want := alice + " <" + alice + ">"
	commits := taskCommits(t, repository, created.ID)
	if len(commits) != 2 {
		t.Fatalf("commits = %d, want the create and the assignment", len(commits))
	}
	for _, commit := range commits {
		if commit.actor != alice || commit.author != want || commit.committer != want {
			t.Errorf("commit %s: actor %q, author %q, committer %q; want alice throughout",
				commit.commit, commit.actor, commit.author, commit.committer)
		}
	}
}

// A value that is not valid UTF-8 would be recorded as somebody else: the
// operation's JSON turns each bad byte into U+FFFD. It is refused as an
// assignee exactly as it is refused as the acting person, and nothing moves.
func TestAssignRefusesAPersonThatIsNotValidUTF8(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	existing := createReadyTask(t, repository, "Existing")
	refsBefore := gitOutput(t, repository, "for-each-ref", "refs/workbook/")
	code, stdout, stderr := runWithActorEnvironment(t, repository, nil,
		"update", existing.ID, "--assign", "s\xffam@example.com", "--no-sync", "--json")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "valid UTF-8") {
		t.Fatalf("--assign with invalid UTF-8 = (%d, %q, %q), want the validation refusal", code, stdout, stderr)
	}
	if refsAfter := gitOutput(t, repository, "for-each-ref", "refs/workbook/"); refsAfter != refsBefore {
		t.Fatalf("a refused assignment moved refs:\nbefore %s\nafter  %s", refsBefore, refsAfter)
	}
}

// A mistyped command is the first thing wrong with it, so it is what is
// reported, whatever the environment it would have run under.
func TestAnUnknownCommandIsReportedBeforeAnUnusableWorkbookActor(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	code, _, stderr := runAs(t, repository, "not an address", "udpate", "--json")
	if code != 2 {
		t.Fatalf("udpate with a bad WORKBOOK_ACTOR code = %d, want 2; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, `unknown command "udpate"`)
	if strings.Contains(stderr, core.ActorVariable) {
		t.Fatalf("stderr = %q; an unknown command must not be refused over WORKBOOK_ACTOR", stderr)
	}
}

// boardRequestAs makes one board request as person, the name a deployment that
// signs people in would put on the request; see withRequestActor. It returns
// its failure rather than failing the test, so concurrent requests can make it.
func boardRequestAs(method, url, body, person string) ([]byte, int, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(testActorHeader, person)
	response, err := boardTestClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s as %s: %w", method, url, person, err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	return contents, response.StatusCode, err
}

const testActorHeader = "X-Workbook-Test-Actor"

// servedReply is one board answer a concurrent request brought back.
type servedReply struct {
	body   []byte
	status int
	err    error
}

// concurrently runs one request per person at the same moment and returns each
// answer, in the order of people.
func concurrently(people []string, request func(person string) servedReply) []servedReply {
	replies := make([]servedReply, len(people))
	start := make(chan struct{})
	var group sync.WaitGroup
	for index, person := range people {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			replies[index] = request(person)
		}()
	}
	close(start)
	group.Wait()
	return replies
}

func (reply servedReply) ok(t *testing.T, what string) servedReply {
	t.Helper()
	if reply.err != nil {
		t.Fatalf("%s: %v", what, reply.err)
	}
	return reply
}

// One server, two people at once. The server's folder has an identity of its
// own, and every request names somebody else: each request's operation, the
// commit behind it, the assignee `self` resolves to, and the board's answer to
// which assignments that person may remove are that request's person, never
// the server's and never the other request's. This is the whole of what the
// next story needs from this one: a name on each request's context, with
// nothing else to change.
func TestServeRecordsEachConcurrentRequestUnderItsOwnPerson(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	gitOutput(t, repository, "config", "user.name", "Board Server")
	gitOutput(t, repository, "config", "user.email", "server@example.test")
	people := []string{"alice@example.com", "bob@example.com"}

	ctx, cancel := context.WithCancel(withRequestActor(context.Background(), func(request *http.Request) (string, error) {
		return request.Header.Get(testActorHeader), nil
	}))
	reserved := reserveListener(t)
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServeWith(ctx, reserved.listen, []string{"--addr", reserved.addr}, repository, &serveStdout, &serveStderr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runServeWith() error = %v; stderr = %q", err, serveStderr.String())
		}
	})
	waitForHTTP(t, "http://"+reserved.addr+"/healthz")
	base := "http://" + reserved.addr

	// Each person files a task and assigns it to `self`, both at once.
	created := map[string]core.Task{}
	for index, reply := range concurrently(people, func(person string) servedReply {
		body, status, err := boardRequestAs(http.MethodPost, base+"/api/tasks",
			`{"title":"Filed by `+person+`","description":"","status":"ready","priority":"medium","labels":[]}`, person)
		return servedReply{body, status, err}
	}) {
		reply.ok(t, "create as "+people[index])
		created[people[index]] = decodeServeMutation(t, reply.body, reply.status)
	}
	assigned := map[string]core.Task{}
	for index, reply := range concurrently(people, func(person string) servedReply {
		task := created[person]
		body, status, err := boardRequestAs(http.MethodPost, base+"/api/tasks/"+task.ID+"/assignments",
			`{"to":"","expectedHead":"`+task.Head+`"}`, person)
		return servedReply{body, status, err}
	}) {
		person := people[index]
		reply.ok(t, "assign self as "+person)
		task, views := decodeServeAssignment(t, reply.body, reply.status)
		if len(task.Assignments) != 1 || task.Assignments[0].Principal != person || task.Assignments[0].Creator != person {
			t.Fatalf("self as %s assigned %#v, want %s as principal and creator", person, task.Assignments, person)
		}
		if len(views) != 1 || !views[0].Removable {
			t.Fatalf("the answer to %s's own assignment = %#v, want it removable by %s", person, views, person)
		}
		assigned[person] = task
	}

	// The board's per-request answers: each person reads the same board, and
	// sees their own assignment as theirs to remove and the other's as not.
	for index, reply := range concurrently(people, func(person string) servedReply {
		body, status, err := boardRequestAs(http.MethodGet, base+"/api/tasks", "", person)
		return servedReply{body, status, err}
	}) {
		person := people[index]
		reply.ok(t, "list as "+person)
		if reply.status != http.StatusOK {
			t.Fatalf("list as %s = %d; body = %s", person, reply.status, reply.body)
		}
		var document struct {
			Presentation []struct {
				TaskID      string             `json:"taskId"`
				Assignments []servedAssignment `json:"assignments"`
			} `json:"presentation"`
		}
		if err := json.Unmarshal(reply.body, &document); err != nil {
			t.Fatalf("decode the board as %s: %v", person, err)
		}
		seen := 0
		for _, row := range document.Presentation {
			for _, owner := range people {
				if row.TaskID != created[owner].ID {
					continue
				}
				seen++
				if len(row.Assignments) != 1 || row.Assignments[0].Principal != owner || row.Assignments[0].Removable != (owner == person) {
					t.Errorf("as %s, %s's task shows %#v; want %s's assignment, removable only by %s", person, owner, row.Assignments, owner, owner)
				}
			}
		}
		if seen != len(people) {
			t.Fatalf("as %s the board shows %d of the %d tasks", person, seen, len(people))
		}
	}

	// And the service agrees: each is refused the other's assignment, at once,
	// and each may withdraw their own.
	other := map[string]string{people[0]: people[1], people[1]: people[0]}
	for index, reply := range concurrently(people, func(person string) servedReply {
		owner := other[person]
		body, status, err := boardRequestAs(http.MethodDelete, base+"/api/tasks/"+assigned[owner].ID+"/assignments",
			`{"from":"`+owner+`","expectedHead":"`+assigned[owner].Head+`"}`, person)
		return servedReply{body, status, err}
	}) {
		reply.ok(t, "remove the other's as "+people[index])
		if reply.status != http.StatusBadRequest || !strings.Contains(string(reply.body), "may be removed only by") {
			t.Fatalf("%s removing %s's assignment = %d; body = %s; want the removal rule's refusal",
				people[index], other[people[index]], reply.status, reply.body)
		}
	}
	for index, reply := range concurrently(people, func(person string) servedReply {
		body, status, err := boardRequestAs(http.MethodDelete, base+"/api/tasks/"+assigned[person].ID+"/assignments",
			`{"from":"`+person+`","expectedHead":"`+assigned[person].Head+`"}`, person)
		return servedReply{body, status, err}
	}) {
		reply.ok(t, "remove own as "+people[index])
		if withdrawn, _ := decodeServeAssignment(t, reply.body, reply.status); len(withdrawn.Assignments) != 0 {
			t.Fatalf("%s withdrawing their own left %#v", people[index], withdrawn.Assignments)
		}
	}

	for _, person := range people {
		want := person + " <" + person + ">"
		commits := taskCommits(t, repository, created[person].ID)
		if len(commits) != 3 {
			t.Fatalf("%s's task has %d commits, want the create, the assignment and the withdrawal", person, len(commits))
		}
		for _, commit := range commits {
			if commit.actor != person || commit.author != want || commit.committer != want {
				t.Errorf("%s's commit %s: actor %q, author %q, committer %q; want %s throughout",
					person, commit.commit, commit.actor, commit.author, commit.committer, person)
			}
		}
	}

	// A name that is not a usable actor is refused before any route runs, and
	// never replaced by the server's own.
	body, status, err := boardRequestAs(http.MethodPost, base+"/api/tasks",
		`{"title":"Nobody's","description":"","status":"ready","priority":"medium","labels":[]}`, "not an address")
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("a request as an unusable name = %d, %v; body = %s; want 400", status, err, body)
	}
}
