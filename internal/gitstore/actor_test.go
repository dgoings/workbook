package gitstore

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// actorContext is a context asserting who is acting, the way the command line
// builds one from WORKBOOK_ACTOR and a long-running caller builds one per
// request.
func actorContext(t *testing.T, actor string) context.Context {
	t.Helper()
	ctx, err := core.WithActor(context.Background(), actor)
	if err != nil {
		t.Fatalf("WithActor(%q): %v", actor, err)
	}
	return ctx
}

// removeGitIdentity leaves a checkout with no Git identity at all. useConfigOnly
// is what makes that true on every machine: without it Git guesses an address
// from the login and host name, and on a machine whose host name has a dot in
// it the guess succeeds.
func removeGitIdentity(t *testing.T, directory string) {
	t.Helper()
	syncGit(t, directory, "config", "--unset", "user.email")
	syncGit(t, directory, "config", "--unset", "user.name")
	syncGit(t, directory, "config", "user.useConfigOnly", "true")
}

// commitIdentities reads the author and committer of a commit as Git prints
// them, name and email.
func commitIdentities(t *testing.T, directory, commit string) (author, committer string) {
	t.Helper()
	output := syncGit(t, directory, "show", "--no-patch", "--format=%an <%ae>%x00%cn <%ce>", commit)
	author, committer, found := strings.Cut(output, "\x00")
	if !found {
		t.Fatalf("show %s = %q", commit, output)
	}
	return author, committer
}

// operationActor reads the actor out of the operation blob a commit carries,
// which is the authoritative record, rather than out of the commit.
func operationActor(t *testing.T, directory, commit string) string {
	t.Helper()
	var pack struct {
		Actor core.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(syncGit(t, directory, "show", commit+":operation.json")), &pack); err != nil {
		t.Fatalf("decode %s operation: %v", commit, err)
	}
	return pack.Actor.ID
}

func actorService(repo *Repository, config core.ProjectConfig, actor string) core.Service {
	service := syncService(repo, config)
	service.Actor = actor
	return service
}

func TestActorComesFromTheContextBeforeTheCheckout(t *testing.T) {
	t.Parallel()
	repo, _ := writeRepository(t)
	var commands [][]string
	repo.commandObserver = func(args []string) { commands = append(commands, args) }

	got, err := repo.Actor(actorContext(t, "alice@example.com"))
	if err != nil || got != "alice@example.com" {
		t.Fatalf("Actor(alice) = %q, %v", got, err)
	}
	if len(commands) != 0 {
		t.Fatalf("an asserted actor asked Git %v; it must not need the checkout's identity", commands)
	}

	got, err = repo.Actor(context.Background())
	if err != nil || got != "workbook@example.test" {
		t.Fatalf("Actor(plain) = %q, %v; want the checkout's user.email", got, err)
	}
	// And the checkout's answer, once memoized, does not leak into a context
	// that names somebody.
	if got, _ := repo.Actor(actorContext(t, "bob@example.com")); got != "bob@example.com" {
		t.Fatalf("Actor(bob) after a plain read = %q", got)
	}
}

// One handle, two people. This is the seam a server acting for several
// signed-in people relies on: nothing about who acted is held on the
// Repository, so each request's context decides its own operations and its own
// commits.
func TestOneRepositoryRecordsEachContextsOwnPerson(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)

	alice := actorContext(t, "alice@example.com")
	created, err := actorService(repo, config, "alice@example.com").CreateMutation(alice, core.CreateInput{Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	bob := actorContext(t, "bob@example.com")
	title := "Shared, renamed"
	updated, err := actorService(repo, config, "bob@example.com").UpdateMutation(bob, created.Task.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		head, actor string
	}{
		{created.Task.Head, "alice@example.com"},
		{updated.Task.Head, "bob@example.com"},
	} {
		author, committer := commitIdentities(t, repo.Root, step.head)
		want := step.actor + " <" + step.actor + ">"
		if author != want || committer != want {
			t.Errorf("commit %s author = %q, committer = %q, want both %q", step.head, author, committer, want)
		}
		if got := operationActor(t, repo.Root, step.head); got != step.actor {
			t.Errorf("operation at %s actor = %q, want %q", step.head, got, step.actor)
		}
	}
}

// The given name replaces the checkout's whole Git identity, not only its
// email: a server whose own user.name is set must not sign somebody else's edit
// with it.
func TestAnAssertedActorReplacesTheCheckoutsGitName(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	syncGit(t, repo.Root, "config", "user.name", "Board Server")
	syncGit(t, repo.Root, "config", "user.email", "server@example.test")

	ctx := actorContext(t, "alice@example.com")
	created, err := actorService(repo, config, "alice@example.com").CreateMutation(ctx, core.CreateInput{Title: "Hers"})
	if err != nil {
		t.Fatal(err)
	}
	author, committer := commitIdentities(t, repo.Root, created.Task.Head)
	if author != "alice@example.com <alice@example.com>" || committer != author {
		t.Fatalf("author = %q, committer = %q; want alice as both, with no trace of the server", author, committer)
	}
}

func TestAnAssertedActorWritesInAFolderWithNoGitIdentity(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	removeGitIdentity(t, repo.Root)

	// The folder really has none: the checkout's own answer is refused.
	reopened, err := Open(context.Background(), repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Actor(context.Background()); err == nil {
		t.Fatal("a folder with no user.email answered Actor without one")
	}

	ctx := actorContext(t, "alice@example.com")
	created, err := actorService(repo, config, "alice@example.com").CreateMutation(ctx, core.CreateInput{Title: "Hosted"})
	if err != nil {
		t.Fatalf("create under an asserted actor in a folder with no identity: %v", err)
	}
	author, _ := commitIdentities(t, repo.Root, created.Task.Head)
	if author != "alice@example.com <alice@example.com>" {
		t.Fatalf("author = %q", author)
	}

	// The configuration ledger asks the repository who is acting itself, so it
	// is the other half of "every write": a status added from such a folder is
	// recorded under the given name too.
	result, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{},
		[]core.ConfigOperation{addOperation("icebox", "Icebox", "7/1")}, "")
	if err != nil {
		t.Fatalf("configuration write under an asserted actor in a folder with no identity: %v", err)
	}
	head := refValue(t, repo, configRef)
	author, committer := commitIdentities(t, repo.Root, head)
	if author != "alice@example.com <alice@example.com>" || committer != author {
		t.Fatalf("configuration commit author = %q, committer = %q", author, committer)
	}
	if got := operationActor(t, repo.Root, head); got != "alice@example.com" {
		t.Fatalf("configuration operation actor = %q, want alice; result = %#v", got, result)
	}
}

// Synchronization writes commits of its own — the replay of a local edit onto
// history somebody else published — and the background sync of a server folder
// with no Git identity must still be able to.
func TestSyncReplaysUnderAnAssertedActorInAFolderWithNoGitIdentity(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	removeGitIdentity(t, second.Root)
	alice := actorContext(t, "alice@example.com")

	divergent := createSyncTask(t, first, config, "Divergent task")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(alice, config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, divergent.ID, "Remote branch")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	high := core.PriorityHigh
	if _, err := actorService(second, config, "alice@example.com").UpdateMutation(
		alice, divergent.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}

	result, err := second.Sync(alice, config)
	if err != nil {
		t.Fatalf("Sync under an asserted actor in a folder with no identity: %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, divergent.ID, SyncReconciled)
	head := refValue(t, second, taskRefPrefix+divergent.ID)
	author, committer := commitIdentities(t, second.Root, head)
	if author != "alice@example.com <alice@example.com>" || committer != author {
		t.Fatalf("replayed commit author = %q, committer = %q", author, committer)
	}
}

// With no actor on the context, the commit behind an edit is the one Git
// writes unaided from the checkout's own configuration — the commit every
// build before WORKBOOK_ACTOR wrote. This rebuilds the commit with a plain
// `git commit-tree` from the same tree, parent, message and timestamps and
// requires the identical object ID, so any byte the override path could leak
// into an ordinary write — a name, an email, a header — fails it.
func TestAPlainContextWritesTheCommitGitWritesUnaided(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	// A name and an email that differ from each other and from every actor
	// above, so a commit that took its identity from anywhere but the checkout
	// cannot reproduce.
	syncGit(t, repo.Root, "config", "user.name", "Checkout Owner")
	syncGit(t, repo.Root, "config", "user.email", "owner@example.test")
	service := syncService(repo, config)
	service.Actor = "owner@example.test"

	created, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "Ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Ordinary, renamed"
	updated, err := service.UpdateMutation(context.Background(), created.Task.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range []string{created.Task.Head, updated.Task.Head} {
		if rebuilt := rebuildCommitUnaided(t, repo.Root, head); rebuilt != head {
			t.Fatalf("plain git commit-tree rebuilt %s as %s; Workbook's commit is not the one Git writes unaided", head, rebuilt)
		}
		author, committer := commitIdentities(t, repo.Root, head)
		if author != "Checkout Owner <owner@example.test>" || committer != author {
			t.Fatalf("commit %s author = %q, committer = %q", head, author, committer)
		}
	}
}

// rebuildCommitUnaided writes, with plain Git and no identity of its own, the
// commit that has commit's tree, parents, message and timestamps, and returns
// its ID.
func rebuildCommitUnaided(t *testing.T, directory, commit string) string {
	t.Helper()
	format := "%T%x00%P%x00%ad%x00%cd%x00%B"
	output, err := exec.Command("git", "-C", directory, "show", "--no-patch", "--date=raw", "--format="+format, commit).Output()
	if err != nil {
		t.Fatalf("read commit %s: %v", commit, err)
	}
	fields := strings.SplitN(string(output), "\x00", 5)
	if len(fields) != 5 {
		t.Fatalf("commit %s fields = %q", commit, fields)
	}
	tree, parents, authorDate, committerDate, message := fields[0], strings.Fields(fields[1]), fields[2], fields[3], fields[4]
	args := []string{"-C", directory, "commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	// %B ends the message with the newline commit-tree's -m adds, and show
	// appends one more; -m gets the message back without both.
	args = append(args, "-m", strings.TrimSuffix(strings.TrimSuffix(message, "\n"), "\n"))
	command := exec.Command("git", args...)
	command.Env = append(command.Environ(), "GIT_AUTHOR_DATE="+authorDate, "GIT_COMMITTER_DATE="+committerDate)
	rebuilt, err := command.Output()
	if err != nil {
		t.Fatalf("git commit-tree: %v", err)
	}
	return strings.TrimSpace(string(rebuilt))
}
