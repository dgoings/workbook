package gitstore

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
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

// The reviewer's reproduction: a Service built for one person — the way serve
// builds one at startup and copies it per request — handed a context naming
// another. The operation and the commit both name the context's person, and
// so do `self` and the removal rule.
func TestAServiceBuiltForOnePersonRecordsTheContextsPerson(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	syncGit(t, repo.Root, "config", "user.name", "Board Server")
	syncGit(t, repo.Root, "config", "user.email", "server@example.com")
	server := actorService(repo, config, "server@example.com")
	bob := actorContext(t, "bob@example.com")

	created, err := server.CreateMutation(bob, core.CreateInput{Title: "Bob's"})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := server.UpdateMutation(bob, created.Task.ID, core.UpdateInput{
		Assignments: []core.AssignmentChange{{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := assigned.Task.Assignments; len(got) != 1 || got[0].Principal != "bob@example.com" || got[0].Creator != "bob@example.com" {
		t.Fatalf("self as bob assigned %#v, want bob as principal and creator", got)
	}
	for _, head := range []string{created.Task.Head, assigned.Task.Head} {
		author, committer := commitIdentities(t, repo.Root, head)
		if got := operationActor(t, repo.Root, head); got != "bob@example.com" ||
			author != "bob@example.com <bob@example.com>" || committer != author {
			t.Fatalf("commit %s: operation actor %q, author %q, committer %q; want bob throughout", head, got, author, committer)
		}
	}

	// The server's own identity may not withdraw what bob holds and recorded,
	// and bob may, through the same Service.
	if _, err := server.UpdateMutation(context.Background(), created.Task.ID, core.UpdateInput{
		Assignments: []core.AssignmentChange{{To: "bob@example.com", Remove: true}},
	}); err == nil {
		t.Fatal("the server's own identity withdrew bob's assignment")
	}
	if _, err := server.UpdateMutation(bob, created.Task.ID, core.UpdateInput{
		Assignments: []core.AssignmentChange{{To: "bob@example.com", Remove: true}},
	}); err != nil {
		t.Fatalf("bob withdrawing his own assignment: %v", err)
	}
}

// Where an operation naming one person meets a context naming another, the
// write is refused and nothing moves: recording either would put a wrong name
// on somebody's edit.
func TestAWriteNamingAnotherPersonThanItsContextIsRefused(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	pack := writeCreatePack()
	state := writeState(t, nil, pack)
	refsBefore := syncGit(t, repo.Root, "for-each-ref", "refs/workbook/")
	_, err := repo.Write(actorContext(t, "alice@example.com"), config, nil, pack, state, "create task")
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("Write(pack by %s, context alice) error = %v, want the mismatch refused", pack.Actor.ID, err)
	}
	if refsAfter := syncGit(t, repo.Root, "for-each-ref", "refs/workbook/"); refsAfter != refsBefore {
		t.Fatalf("a refused write moved refs:\nbefore %s\nafter  %s", refsBefore, refsAfter)
	}
}

// A replay keeps the operation's actor, so it keeps the commit's too. The
// folder here has an identity of its own, which a replay on a plain context —
// a background sync started without WORKBOOK_ACTOR — used to put on sam's edit.
func TestAReplayNamesItsOperationsActorNotTheSyncingFolder(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	syncGit(t, second.Root, "config", "user.name", "Server")
	syncGit(t, second.Root, "config", "user.email", "server@example.test")
	second = openSyncCloneAt(t, second.Root)
	pinCommitDates(t, second)
	sam := actorContext(t, "sam@example.com")

	divergent := createSyncTask(t, first, config, "Divergent task")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, divergent.ID, "Remote branch")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	high := core.PriorityHigh
	local, err := actorService(second, config, "server@example.test").UpdateMutation(
		sam, divergent.ID, core.UpdateInput{Priority: &high})
	if err != nil {
		t.Fatal(err)
	}

	result, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, divergent.ID, SyncReconciled)
	head := refValue(t, second, taskRefPrefix+divergent.ID)
	if head == local.Task.Head {
		t.Fatal("the sync did not replay sam's edit")
	}
	if got := remoteRefValue(t, second, taskRefPrefix+divergent.ID); got != head {
		t.Fatalf("origin holds %s, want the replayed commit %s", got, head)
	}
	author, committer := commitIdentities(t, second.Root, head)
	if got := operationActor(t, second.Root, head); got != "sam@example.com" ||
		author != "sam@example.com <sam@example.com>" || committer != author {
		t.Fatalf("replayed commit: operation actor %q, author %q, committer %q; want sam throughout, not the folder", got, author, committer)
	}
	// And byte for byte the commit Git writes for sam with nothing else added:
	// no date or header of the syncing process's leaks into it either.
	if rebuilt := rebuildCommitUnaided(t, second.Root, head, identityEnvironment("sam@example.com")); rebuilt != head {
		t.Fatalf("plain git commit-tree as sam rebuilt %s as %s", head, rebuilt)
	}
}

// The hosted setup: a folder with no Git identity at all, every edit recorded
// under an asserted name, and the background sync — which acts for nobody —
// replaying several people's task and configuration edits in one pass and
// publishing them.
func TestABackgroundSyncReplaysEachPersonsEditsInAFolderWithNoGitIdentity(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	removeGitIdentity(t, second.Root)
	second = openSyncCloneAt(t, second.Root)
	alice, bob := actorContext(t, "alice@example.com"), actorContext(t, "bob@example.com")

	alices := createSyncTask(t, first, config, "Alice's")
	bobs := createSyncTask(t, first, config, "Bob's")
	writeConfig(t, first, config, configOperations(addOperation("icebox", "Icebox", "7/1"))...)
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, alices.ID, "Alice's, renamed remotely")
	updateSyncTask(t, first, config, bobs.ID, "Bob's, renamed remotely")
	writeConfig(t, first, config, configOperations(relabelOperation("icebox", "Ice box"))...)
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	high := core.PriorityHigh
	if _, err := actorService(second, config, "").UpdateMutation(alice, alices.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}
	if _, err := actorService(second, config, "").UpdateMutation(bob, bobs.ID, core.UpdateInput{Priority: &high}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.WriteConfigOperation(bob, config, core.CryptoULIDSource{},
		[]core.ConfigOperation{addOperation("triage", "Triage", "1/2")}, ""); err != nil {
		t.Fatal(err)
	}

	result, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("background Sync in a folder with no identity: %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, alices.ID, SyncReconciled)
	assertSyncOutcome(t, result.Fetch, bobs.ID, SyncReconciled)
	assertConfigStatus(t, result.Fetch.Config, SyncConfigReconciled)
	for _, replayed := range []struct {
		ref, actor string
	}{
		{taskRefPrefix + alices.ID, "alice@example.com"},
		{taskRefPrefix + bobs.ID, "bob@example.com"},
		{configRef, "bob@example.com"},
	} {
		head := refValue(t, second, replayed.ref)
		if got := remoteRefValue(t, second, replayed.ref); got != head {
			t.Errorf("%s: origin holds %s, want the replayed commit %s published", replayed.ref, got, head)
		}
		author, committer := commitIdentities(t, second.Root, head)
		want := replayed.actor + " <" + replayed.actor + ">"
		if got := operationActor(t, second.Root, head); got != replayed.actor || author != want || committer != want {
			t.Errorf("%s replay: operation actor %q, author %q, committer %q; want %s throughout",
				replayed.ref, got, author, committer, replayed.actor)
		}
	}
}

// pinnedCommitDate is the date every Git process pinCommitDates wraps uses for a
// commit, unless Workbook itself names one.
const pinnedCommitDate = "1700000000 +0130"

// pinCommitDates routes repo's Git through a wrapper that supplies the commit
// dates the process environment would, so a commit is reproducible from its
// tree, parents, message and identity alone. Workbook's own environment is
// applied after the process's, so a date Workbook named would still win and
// show up as a commit the rebuild cannot reproduce.
func pinCommitDates(t *testing.T, repo *Repository) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	testenv.WriteExecutable(t, wrapper, []byte("#!/bin/sh\n"+
		": \"${GIT_AUTHOR_DATE:="+pinnedCommitDate+"}\"\n"+
		": \"${GIT_COMMITTER_DATE:="+pinnedCommitDate+"}\"\n"+
		"export GIT_AUTHOR_DATE GIT_COMMITTER_DATE\n"+
		"exec '"+git+"' \"$@\"\n"))
	repo.gitPath = wrapper
}

// identityEnvironment is the identity Git is given for an actor that is not the
// checkout's own: the email as name and email, author and committer.
func identityEnvironment(actor string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + actor, "GIT_AUTHOR_EMAIL=" + actor,
		"GIT_COMMITTER_NAME=" + actor, "GIT_COMMITTER_EMAIL=" + actor,
	}
}

// With the actor the checkout's own user.email, every commit Workbook writes
// is the one Git writes unaided from the checkout's configuration — the commit
// every build before WORKBOOK_ACTOR wrote. Each kind is rebuilt with a plain
// `git commit-tree` from the same tree, parents and message, the checkout's
// own identity, and the date the process environment supplies, and must come
// out with the identical object ID. So any byte the override path leaks into
// an ordinary write — a name, an email, a date, a header — fails it.
//
// The recorded clock (what WORKBOOK_NOW sets) is moved years away from the
// commit date on purpose: it dates the operations inside the trees, never the
// commits, so a commit dated from it fails here too.
func TestEveryCommitAnOrdinaryCloneWritesIsTheOneGitWritesUnaided(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	// A name and an email that differ from each other and from every actor in
	// this file, so a commit that took its identity from anywhere but the
	// checkout cannot reproduce.
	syncGit(t, second.Root, "config", "user.name", "Checkout Owner")
	syncGit(t, second.Root, "config", "user.email", "owner@example.test")
	second = openSyncCloneAt(t, second.Root)
	pinCommitDates(t, second)
	recorded := time.Date(2019, time.March, 5, 14, 0, 0, 0, time.UTC)
	second.Now = func() time.Time { return recorded }
	owner := actorService(second, config, "owner@example.test")
	owner.Now = func() time.Time { return recorded }

	commits := map[string]string{}

	// The divergence a replay needs: one task and the ledger moved on both
	// sides.
	shared := createSyncTask(t, first, config, "Shared")
	writeConfig(t, first, config, configOperations(addOperation("icebox", "Icebox", "7/1"))...)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, first, config, shared.ID, "Shared, renamed remotely")
	writeConfig(t, first, config, configOperations(relabelOperation("icebox", "Ice box"))...)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}

	created, err := owner.CreateMutation(ctx, core.CreateInput{Title: "Ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	commits["task create"] = created.Task.Head
	title := "Ordinary, renamed"
	updated, err := owner.UpdateMutation(ctx, created.Task.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	commits["task update"] = updated.Task.Head
	high := core.PriorityHigh
	local, err := owner.UpdateMutation(ctx, shared.ID, core.UpdateInput{Priority: &high})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := second.WriteConfigOperation(ctx, config, core.CryptoULIDSource{},
		[]core.ConfigOperation{addOperation("triage", "Triage", "1/2")}, "")
	if err != nil {
		t.Fatal(err)
	}
	commits["config-ledger write"] = ledger.Head

	result, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, shared.ID, SyncReconciled)
	assertConfigStatus(t, result.Fetch.Config, SyncConfigReconciled)
	commits["task replay"] = refValue(t, second, taskRefPrefix+shared.ID)
	commits["config-ledger replay"] = refValue(t, second, configRef)
	if commits["task replay"] == local.Task.Head || commits["config-ledger replay"] == ledger.Head {
		t.Fatalf("the sync replayed nothing: %#v", commits)
	}

	done := core.StatusDone
	if _, err := owner.UpdateMutation(ctx, created.Task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatal(err)
	}
	input := checkpointInputFor(t, second, config, currentSnapshot(t, second, config, created.Task.ID), true)
	input.Actor = "owner@example.test"
	checkpoint, err := second.BuildCheckpoint(ctx, config, input)
	if err != nil {
		t.Fatal(err)
	}
	commits["compaction checkpoint"] = checkpoint.Head

	tip := currentSnapshot(t, second, config, created.Task.ID)
	bundle := filepath.Join(t.TempDir(), "archive.bundle")
	if err := second.WriteArchiveBundle(ctx, config, bundle, ArchiveManifest{
		Format: "workbook.archive-manifest", Version: 1, ProjectID: config.ProjectID,
		RunID: newCheckpointID(t), CreatedAt: recorded,
		Tasks: []ArchiveTask{{
			TaskID: created.Task.ID, Title: tip.State.Task.Title, OldTip: tip.Head,
			OldGeneration: tip.State.History.Generation, NewGeneration: input.Generation, Commits: 4,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(syncGit(t, second.Root, "bundle", "list-heads", bundle), "\n") {
		if objectID, ref, found := strings.Cut(line, " "); found && ref == archiveManifestRef {
			commits["archive manifest"] = objectID
		}
	}
	if commits["archive manifest"] == "" {
		t.Fatal("the bundle carries no archive manifest")
	}

	for kind, head := range commits {
		if rebuilt := rebuildCommitUnaided(t, second.Root, head, nil); rebuilt != head {
			t.Errorf("%s: plain git commit-tree rebuilt %s as %s; Workbook's commit is not the one Git writes unaided",
				kind, head, rebuilt)
		}
		author, committer := commitIdentities(t, second.Root, head)
		if author != "Checkout Owner <owner@example.test>" || committer != author {
			t.Errorf("%s commit %s author = %q, committer = %q", kind, head, author, committer)
		}
	}
}

// rebuildCommitUnaided writes, with plain Git, the commit that has commit's
// tree, parents and message, the pinned date, and either the checkout's own
// identity (identity nil) or the one identity names, and returns its ID.
func rebuildCommitUnaided(t *testing.T, directory, commit string, identity []string) string {
	t.Helper()
	format := "%T%x00%P%x00%B"
	output, err := exec.Command("git", "-C", directory, "show", "--no-patch", "--format="+format, commit).Output()
	if err != nil {
		t.Fatalf("read commit %s: %v", commit, err)
	}
	fields := strings.SplitN(string(output), "\x00", 3)
	if len(fields) != 3 {
		t.Fatalf("commit %s fields = %q", commit, fields)
	}
	tree, parents, message := fields[0], strings.Fields(fields[1]), fields[2]
	args := []string{"-C", directory, "commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	// %B ends the message with the newline commit-tree's -m adds, and show
	// appends one more; -m gets the message back without both.
	args = append(args, "-m", strings.TrimSuffix(strings.TrimSuffix(message, "\n"), "\n"))
	command := exec.Command("git", args...)
	command.Env = append(command.Environ(), "GIT_AUTHOR_DATE="+pinnedCommitDate, "GIT_COMMITTER_DATE="+pinnedCommitDate)
	command.Env = append(command.Env, identity...)
	rebuilt, err := command.Output()
	if err != nil {
		t.Fatalf("git commit-tree: %v", err)
	}
	return strings.TrimSpace(string(rebuilt))
}
