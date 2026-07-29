package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestFetchDiscoversAndFastForwardsTasksWithoutOverwritingLocalWork(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Shared task")

	publishTaskRefs(t, first)
	fetched, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(new task) error = %v; result = %#v", err, fetched)
	}
	assertSyncOutcome(t, fetched, task.ID, SyncCreated)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("created local tip = %q, want remote %q", got, remoteTip)
	}

	updateSyncTask(t, first, config, task.ID, "Remote update")
	publishTaskRefs(t, first)
	fetched, err = second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(update) error = %v; result = %#v", err, fetched)
	}
	assertSyncOutcome(t, fetched, task.ID, SyncFastForwarded)

	updateSyncTask(t, second, config, task.ID, "Local update")
	localTip := refValue(t, second, taskRefPrefix+task.ID)
	fetched, err = second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(local ahead) error = %v; result = %#v", err, fetched)
	}
	assertSyncOutcome(t, fetched, task.ID, SyncLocalAhead)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != localTip {
		t.Fatalf("local-ahead fetch changed tip from %q to %q", localTip, got)
	}
}

func TestFetchPreservesDivergence(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Divergent task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, task.ID, "Remote branch")
	updateSyncTask(t, second, config, task.ID, "Local branch")
	publishTaskRefs(t, first)
	localTip := refValue(t, second, taskRefPrefix+task.ID)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(diverged) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncDiverged)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != localTip {
		t.Fatalf("diverged fetch changed local tip from %q to %q", localTip, got)
	}
	tracking := remoteTaskRefPrefix + task.ID
	if got, want := refValue(t, second, tracking), refValue(t, first, taskRefPrefix+task.ID); got != want {
		t.Fatalf("tracking tip = %q, want remote tip %q", got, want)
	}
}

func TestFetchKeepsInvalidRemoteTipIsolated(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Corrupt task")
	publishTaskRefs(t, first)
	badTree := syncGit(t, first.Root, "mktree")
	badCommit := syncGit(t, first.Root, "commit-tree", badTree, "-m", "invalid Workbook task")
	syncGit(t, first.Root, "push", "--force", "origin", badCommit+":"+taskRefPrefix+task.ID)

	result, err := second.Fetch(context.Background(), config)
	if err == nil {
		t.Fatalf("Fetch(invalid) error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result, task.ID, SyncInvalid)
	if refExists(t, second, taskRefPrefix+task.ID) {
		t.Fatalf("invalid remote task reached canonical ref %s", taskRefPrefix+task.ID)
	}
	if !refExists(t, second, remoteTaskRefPrefix+task.ID) {
		t.Fatalf("invalid remote task was not retained in isolated tracking ref")
	}
}

func TestFetchReconcilesValidRemoteTipWhenAnotherTrackingTipIsInvalid(t *testing.T) {
	first, second, config := syncRepositories(t)
	valid := createSyncTask(t, first, config, "Valid task")
	invalid := createSyncTask(t, first, config, "Invalid task")
	publishTaskRefs(t, first)

	badTree := syncGit(t, first.Root, "mktree")
	badCommit := syncGit(t, first.Root, "commit-tree", badTree, "-m", "invalid Workbook task")
	syncGit(t, first.Root, "push", "--force", "origin", badCommit+":"+taskRefPrefix+invalid.ID)

	result, err := second.Fetch(context.Background(), config)
	if err == nil {
		t.Fatalf("Fetch(mixed validity) error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result, invalid.ID, SyncInvalid)
	assertSyncOutcome(t, result, valid.ID, SyncCreated)
	if got, want := refValue(t, second, taskRefPrefix+valid.ID), refValue(t, first, taskRefPrefix+valid.ID); got != want {
		t.Fatalf("valid canonical tip = %q, want %q", got, want)
	}
	if refExists(t, second, taskRefPrefix+invalid.ID) {
		t.Fatalf("invalid remote task reached canonical ref %s", taskRefPrefix+invalid.ID)
	}
}

func TestFetchReconcilesValidRemoteTipWhenAnotherCanonicalTipIsInvalid(t *testing.T) {
	first, second, config := syncRepositories(t)
	invalidLocal := createSyncTask(t, second, config, "Invalid local task")
	invalidHead := refValue(t, second, taskRefPrefix+invalidLocal.ID)
	blob := syncGitInput(t, second.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, second.Root, "update-ref", taskRefPrefix+invalidLocal.ID, blob, invalidHead)

	validRemote := createSyncTask(t, first, config, "Valid remote task")
	publishTaskRefs(t, first)
	result, err := second.Fetch(context.Background(), config)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Fetch(mixed local validity) category = %q, want %q; result = %#v; error = %v", got, want, result, err)
	}
	assertSyncOutcome(t, result, invalidLocal.ID, SyncInvalid)
	assertSyncOutcome(t, result, validRemote.ID, SyncCreated)
	if got, want := refValue(t, second, taskRefPrefix+validRemote.ID), refValue(t, first, taskRefPrefix+validRemote.ID); got != want {
		t.Fatalf("valid canonical tip = %q, want %q", got, want)
	}
	if got := refValue(t, second, taskRefPrefix+invalidLocal.ID); got != blob {
		t.Fatalf("invalid canonical tip = %q, want unchanged %q", got, blob)
	}
}

func TestFetchIsolatesGenerationMismatchAndReconcilesUnrelatedRemoteTip(t *testing.T) {
	first, second, config := syncRepositories(t)
	generationTask := createSyncTask(t, first, config, "Original generation")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	localHead := refValue(t, second, taskRefPrefix+generationTask.ID)

	updateSyncTask(t, first, config, generationTask.ID, "Changed generation")
	updated, err := first.Get(context.Background(), config, generationTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	pack := updated.Operation
	state := updated.State
	pack.HistoryGeneration = "01K0M6B8A4FTT8C39MXXYTW7D9"
	state.History.Generation = pack.HistoryGeneration
	tree := syncGitInput(
		t,
		first.Root,
		[]byte(
			"100644 blob "+writeDocumentBlob(t, first, pack)+"\toperation.json\n"+
				"100644 blob "+writeDocumentBlob(t, first, state)+"\tstate.json\n",
		),
		"mktree",
	)
	parent := syncGit(t, first.Root, "rev-parse", updated.Head+"^")
	mismatched := syncGit(t, first.Root, "commit-tree", tree, "-p", parent, "-m", "change generation")
	syncGit(t, first.Root, "update-ref", taskRefPrefix+generationTask.ID, mismatched, updated.Head)

	validRemote := createSyncTask(t, first, config, "Unrelated valid task")
	publishTaskRefs(t, first)
	result, err := second.Fetch(context.Background(), config)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Fetch(generation mismatch) category = %q, want %q; result = %#v; error = %v", got, want, result, err)
	}
	assertSyncOutcome(t, result, generationTask.ID, SyncInvalid)
	assertSyncOutcome(t, result, validRemote.ID, SyncCreated)
	if got := refValue(t, second, taskRefPrefix+generationTask.ID); got != localHead {
		t.Fatalf("generation-mismatched canonical tip = %q, want unchanged %q", got, localHead)
	}
	if got, want := refValue(t, second, taskRefPrefix+validRemote.ID), refValue(t, first, taskRefPrefix+validRemote.ID); got != want {
		t.Fatalf("unrelated canonical tip = %q, want %q", got, want)
	}
}

func TestFetchFreshCheckoutUsesCompleteTwentyOperationTip(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Revision 01")
	for revision := 2; revision <= 20; revision++ {
		updateSyncTask(t, first, config, task.ID, fmt.Sprintf("Revision %02d", revision))
	}
	want, err := first.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want.State.LogicalClock != 20 {
		t.Fatalf("source logical clock = %d, want 20", want.State.LogicalClock)
	}
	publishTaskRefs(t, first)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(fresh 20-operation tip) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncCreated)
	got, err := second.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Head != want.Head || got.State.LogicalClock != 20 || got.State.Task.Title != "Revision 20" {
		t.Fatalf("fresh fetched tip = %#v, want head %q at revision 20", got, want.Head)
	}
}

func TestFetchLeavesCanonicalRefsUnchangedWhenTransactionLosesCASRace(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Shared task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, task.ID, "Remote update")
	publishTaskRefs(t, first)
	advanced := false
	second.commandObserver = func(args []string) {
		if advanced || !commandHasPrefix(args, "update-ref", "--no-deref", "--create-reflog", "-m", "workbook: fetch origin", "--stdin") {
			return
		}
		advanced = true
		updateSyncTask(t, second, config, task.ID, "Concurrent local update")
	}

	result, err := second.Fetch(context.Background(), config)
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("Fetch(CAS race) category = %q, want %q; result = %#v; error = %v", got, want, result, err)
	}
	if result.Status != SyncPhaseFailed {
		t.Fatalf("Fetch(CAS race) status = %q, want failed", result.Status)
	}
	if !advanced {
		t.Fatal("Fetch() did not attempt the canonical transaction")
	}
	for _, outcome := range result.Tasks {
		if outcome.TaskID == task.ID &&
			(outcome.Status == SyncCreated || outcome.Status == SyncFastForwarded) {
			t.Fatalf("Fetch(CAS race) reported unapplied success outcome %#v", outcome)
		}
	}
	if got := refValue(t, second, taskRefPrefix+task.ID); got == refValue(t, first, taskRefPrefix+task.ID) {
		t.Fatalf("CAS-raced canonical ref adopted remote tip %q", got)
	}
}

func TestFetchAcceptsUpdateWhoseCheckpointDoesNotMatchItsOperation(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Original title")
	publishTaskRefs(t, first)
	updateSyncTask(t, first, config, task.ID, "Operation title")

	valid, err := first.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent := syncGit(t, first.Root, "rev-parse", valid.Head+"^")
	state := valid.State
	state.Task.Title = "Mismatched state title"
	operationBlob := syncGit(t, first.Root, "rev-parse", valid.Head+":operation.json")
	stateBlob := writeDocumentBlob(t, first, state)
	tree := syncGitInput(
		t,
		first.Root,
		[]byte("100644 blob "+operationBlob+"\toperation.json\n100644 blob "+stateBlob+"\tstate.json\n"),
		"mktree",
	)
	invalid := syncGit(t, first.Root, "commit-tree", tree, "-p", parent, "-m", "mismatched checkpoint")
	syncGit(t, first.Root, "push", "--force", "origin", invalid+":"+taskRefPrefix+task.ID)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(mismatched checkpoint) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncCreated)
	got, err := second.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Head != invalid || got.State.Task.Title != state.Task.Title {
		t.Fatalf("Fetch() snapshot = %#v, want mismatched tip %q", got, invalid)
	}
}

func TestPushPublishesAllTaskRefsAndReportsUpToDate(t *testing.T) {
	first, _, config := syncRepositories(t)
	firstTask := createSyncTask(t, first, config, "First task")
	secondTask := createSyncTask(t, first, config, "Second task")

	result, err := first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, firstTask.ID, SyncPublished)
	assertSyncOutcome(t, result, secondTask.ID, SyncPublished)

	result, err = first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push(up-to-date) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, firstTask.ID, SyncUpToDate)
	assertSyncOutcome(t, result, secondTask.ID, SyncUpToDate)
}

func TestPushUsesOneBoundedPublication(t *testing.T) {
	repository, _, config := syncRepositories(t)
	for i := 0; i < 25; i++ {
		createSyncTask(t, repository, config, fmt.Sprintf("Task %02d", i))
	}

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	result, err := repository.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", taskRefPrefix); got != 2 {
		t.Fatalf("canonical ref enumerations = %d, want planning plus final snapshot; commands = %v", got, commands)
	}
	if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
		t.Fatalf("tip batches = %d, want 1; commands = %v", got, commands)
	}
	if got := countCommand(commands, "ls-remote", "--refs", "origin", taskRefPrefix+"*"); got != 1 {
		t.Fatalf("wildcard remote probes = %d, want 1; commands = %v", got, commands)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "ls-remote" && (len(command) != 4 || command[3] != taskRefPrefix+"*") {
			t.Fatalf("Push() ran a per-task remote probe: %v", command)
		}
	}
	pushes := 0
	for _, command := range commands {
		if len(command) == 0 || command[0] != "push" {
			continue
		}
		pushes++
		if strings.Contains(strings.Join(command, " "), "--atomic") || strings.Contains(strings.Join(command, " "), "--force") {
			t.Fatalf("Push() command must be non-atomic and non-force: %v", command)
		}
		if len(command) != 3+25 {
			t.Fatalf("Push() args = %v, want 25 explicit destinations", command)
		}
		for _, refspec := range command[3:] {
			if !strings.Contains(refspec, ":"+taskRefPrefix) || strings.Contains(refspec, "*") {
				t.Fatalf("Push() refspec = %q, want one explicit task destination", refspec)
			}
		}
	}
	if pushes != 1 {
		t.Fatalf("push commands = %d, want 1; commands = %v", pushes, commands)
	}
}

func TestPushRejectsNonFastForwardButPublishesUnrelatedTasks(t *testing.T) {
	first, second, config := syncRepositories(t)
	conflicting := createSyncTask(t, first, config, "Conflicting task")
	unrelated := createSyncTask(t, first, config, "Unrelated task")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, second, config, conflicting.ID, "Remote winner")
	if _, err := second.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	remoteConflict := refValue(t, second, taskRefPrefix+conflicting.ID)

	updateSyncTask(t, first, config, conflicting.ID, "Rejected local branch")
	updateSyncTask(t, first, config, unrelated.ID, "Published local update")
	localUnrelated := refValue(t, first, taskRefPrefix+unrelated.ID)

	result, err := first.Push(context.Background(), config)
	if err == nil {
		t.Fatalf("Push(partial rejection) error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result, conflicting.ID, SyncRejected)
	assertSyncOutcome(t, result, unrelated.ID, SyncPublished)

	if got := remoteRefValue(t, first, taskRefPrefix+conflicting.ID); got != remoteConflict {
		t.Fatalf("rejected remote ref = %q, want unchanged %q", got, remoteConflict)
	}
	if got := remoteRefValue(t, first, taskRefPrefix+unrelated.ID); got != localUnrelated {
		t.Fatalf("unrelated remote ref = %q, want published %q", got, localUnrelated)
	}
}

func TestPushBypassesManagedHookRecursion(t *testing.T) {
	first, _, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Hook recursion task")
	if _, err := first.InstallHooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "unexpected-hook-call")
	fakeWorkbook := filepath.Join(bin, "workbook")
	script := "#!/bin/sh\nprintf called > \"$WORKBOOK_TEST_LOG\"\nexit 19\n"
	if err := os.WriteFile(fakeWorkbook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKBOOK_TEST_LOG", logPath)

	result, err := first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncPublished)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("managed hook recursively invoked workbook; stat error = %v", err)
	}
}

func TestPushRejectsLocallyCorruptHistoryBeforePublishing(t *testing.T) {
	first, _, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Valid root")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	remoteRoot := remoteRefValue(t, first, taskRefPrefix+task.ID)

	valid := refValue(t, first, taskRefPrefix+task.ID)
	invalid := syncGitInput(t, first.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, first.Root, "update-ref", taskRefPrefix+task.ID, invalid, valid)

	result, err := first.Push(context.Background(), config)
	if err == nil {
		t.Fatalf("Push(corrupt local history) error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result, task.ID, SyncInvalid)
	if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != remoteRoot {
		t.Fatalf("corrupt history changed remote from %q to %q", remoteRoot, got)
	}
}

func TestPushOmitsInvalidTaskButPublishesIndependentValidTask(t *testing.T) {
	repository, _, config := syncRepositories(t)
	invalid := createSyncTask(t, repository, config, "Invalid task")
	valid := createSyncTask(t, repository, config, "Valid task")
	invalidHead := refValue(t, repository, taskRefPrefix+invalid.ID)
	blob := syncGitInput(t, repository.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, repository.Root, "update-ref", taskRefPrefix+invalid.ID, blob, invalidHead)

	result, err := repository.Push(context.Background(), config)
	if err == nil {
		t.Fatalf("Push() error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result, invalid.ID, SyncInvalid)
	assertSyncOutcome(t, result, valid.ID, SyncPublished)
	if got, want := remoteRefValue(t, repository, taskRefPrefix+valid.ID), refValue(t, repository, taskRefPrefix+valid.ID); got != want {
		t.Fatalf("valid remote head = %q, want %q", got, want)
	}
}

func TestPushLocalCorruptionPrecedesRemoteTransportFailure(t *testing.T) {
	repository, _, config := syncRepositories(t)
	task := createSyncTask(t, repository, config, "Invalid before transport")
	validHead := refValue(t, repository, taskRefPrefix+task.ID)
	blob := syncGitInput(t, repository.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, repository.Root, "update-ref", taskRefPrefix+task.ID, blob, validHead)
	syncGit(t, repository.Root, "remote", "remove", "origin")

	result, err := repository.Push(context.Background(), config)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Push() category = %q, want %q; result = %#v; error = %v", got, want, result, err)
	}
	if result.Status != SyncPhaseFailed {
		t.Fatalf("Push() status = %q, want %q", result.Status, SyncPhaseFailed)
	}
	assertSyncOutcome(t, result, task.ID, SyncInvalid)
}

func TestPushReportsLocalChangedWhenHeadAdvancesDuringPublication(t *testing.T) {
	repository, _, config := syncRepositories(t)
	task := createSyncTask(t, repository, config, "Race task")
	advanced := false
	repository.commandObserver = func(args []string) {
		if advanced || len(args) == 0 || args[0] != "push" {
			return
		}
		advanced = true
		updateSyncTask(t, repository, config, task.ID, "Advanced during push")
	}

	result, err := repository.Push(context.Background(), config)
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("Push() category = %q, want %q; result = %#v; error = %v", got, want, result, err)
	}
	assertSyncOutcome(t, result, task.ID, SyncLocalChanged)
}

func TestSyncReusesFetchedTipsWithoutRepeatedInspection(t *testing.T) {
	commandCount := 0
	synchronizedCommandCount := 0
	for _, fixture := range []struct {
		tasks      int
		operations int
	}{{tasks: 10, operations: 4}, {tasks: 25, operations: 7}} {
		t.Run(fmt.Sprintf("%d tasks x %d operations", fixture.tasks, fixture.operations), func(t *testing.T) {
			repository, _, config := syncRepositories(t)
			for i := 0; i < fixture.tasks; i++ {
				task := createSyncTask(t, repository, config, fmt.Sprintf("Task %02d", i))
				for operation := 1; operation < fixture.operations; operation++ {
					updateSyncTask(t, repository, config, task.ID, fmt.Sprintf("Task %02d revision %02d", i, operation))
				}
			}

			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			result, err := repository.Sync(context.Background(), config)
			if err != nil {
				t.Fatalf("Sync() error = %v; result = %#v", err, result)
			}
			if got := countCommand(commands, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "origin", "+"+taskRefPrefix+"*:"+remoteTaskRefPrefix+"*"); got != 1 {
				t.Fatalf("fetch commands = %d, want 1; commands = %v", got, commands)
			}
			if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
				t.Fatalf("tip batches = %d, want 1; commands = %v", got, commands)
			}
			if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", taskRefPrefix); got != 2 {
				t.Fatalf("canonical ref enumerations = %d, want fetch planning plus final snapshot; commands = %v", got, commands)
			}
			if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", remoteTaskRefPrefix); got != 1 {
				t.Fatalf("tracking ref enumerations = %d, want 1; commands = %v", got, commands)
			}
			if got := countCommand(commands, "ls-remote", "--refs", "origin", taskRefPrefix+"*"); got != 0 {
				t.Fatalf("ls-remote commands = %d, want none when Sync reuses fetched tracking heads; commands = %v", got, commands)
			}
			if got := countCommand(commands, "merge-base", "--is-ancestor"); got != 0 {
				t.Fatalf("merge-base commands = %d, want none; commands = %v", got, commands)
			}
			if got := countCommandPrefix(commands, "push", "--porcelain", "origin"); got != 1 {
				t.Fatalf("push commands = %d, want one; commands = %v", got, commands)
			}
			if got := countCommand(commands, "rev-list", "--parents", "--stdin"); got > 1 {
				t.Fatalf("graph classifications = %d, want at most 1; commands = %v", got, commands)
			}
			if got := countCommandPrefix(commands, "update-ref", "--no-deref", "--create-reflog", "-m", "workbook: fetch origin", "--stdin"); got > 1 {
				t.Fatalf("canonical transactions = %d, want at most 1; commands = %v", got, commands)
			}
			if commandCount == 0 {
				commandCount = len(commands)
			} else if len(commands) != commandCount {
				t.Fatalf("initial sync command count = %d, want invariant %d; commands = %v", len(commands), commandCount, commands)
			}

			commands = nil
			result, err = repository.Sync(context.Background(), config)
			if err != nil {
				t.Fatalf("Sync(already synchronized) error = %v; result = %#v", err, result)
			}
			if result.Push.Status != SyncPhaseCompleted {
				t.Fatalf("synchronized Push status = %q, want completed; result = %#v", result.Push.Status, result)
			}
			if got := countCommandPrefix(commands, "push", "--porcelain", "origin"); got != 0 {
				t.Fatalf("synchronized push commands = %d, want none; commands = %v", got, commands)
			}
			for _, command := range commands {
				if commandHasPrefix(command, "merge-base", "--is-ancestor") ||
					commandHasPrefix(command, "rev-list", "--reverse") ||
					commandHasPrefix(command, "cat-file", "-t") ||
					(len(command) > 0 && command[0] == "show") {
					t.Fatalf("synchronized Sync used per-task inspection command %v", command)
				}
				if len(command) > 0 && command[0] == "update-ref" &&
					!commandHasPrefix(command, "update-ref", "--no-deref", "--create-reflog", "-m", "workbook: fetch origin", "--stdin") {
					t.Fatalf("synchronized Sync used per-task update command %v", command)
				}
			}
			if synchronizedCommandCount == 0 {
				synchronizedCommandCount = len(commands)
			} else if len(commands) != synchronizedCommandCount {
				t.Fatalf("synchronized command count = %d, want invariant %d; commands = %v", len(commands), synchronizedCommandCount, commands)
			}
			for _, task := range result.Push.Tasks {
				if task.Status != SyncUpToDate {
					t.Fatalf("synchronized task outcome = %#v, want up-to-date", task)
				}
			}
		})
	}
}

func countCommandPrefix(commands [][]string, want ...string) int {
	count := 0
	for _, got := range commands {
		if len(got) < len(want) {
			continue
		}
		matched := true
		for index := range want {
			if got[index] != want[index] {
				matched = false
				break
			}
		}
		if matched {
			count++
		}
	}
	return count
}

func commandHasPrefix(got []string, want ...string) bool {
	if len(got) < len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestSyncFetchesThenPushesWorkbookTaskRefs(t *testing.T) {
	first, second, config := syncRepositories(t)
	firstTask := createSyncTask(t, first, config, "First shared task")

	firstResult, err := first.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync(first) error = %v; result = %#v", err, firstResult)
	}
	assertSyncOutcome(t, firstResult.Push, firstTask.ID, SyncPublished)

	secondTask := createSyncTask(t, second, config, "Second shared task")
	secondResult, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync(second) error = %v; result = %#v", err, secondResult)
	}
	assertSyncOutcome(t, secondResult.Fetch, firstTask.ID, SyncCreated)
	assertSyncOutcome(t, secondResult.Push, firstTask.ID, SyncUpToDate)
	assertSyncOutcome(t, secondResult.Push, secondTask.ID, SyncPublished)
	if got, want := remoteRefValue(t, second, taskRefPrefix+secondTask.ID), refValue(t, second, taskRefPrefix+secondTask.ID); got != want {
		t.Fatalf("second task remote tip = %q, want local tip %q", got, want)
	}
}

func TestSyncRepublishesCanonicalTaskAfterRemoteRefDeletion(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Restore remotely deleted task ref")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	refName := taskRefPrefix + task.ID
	syncGit(t, second.Root, "push", "origin", ":"+refName)
	if remoteRefExists(t, second, refName) {
		t.Fatalf("remote task ref %s still exists after deletion", refName)
	}

	result, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Push, task.ID, SyncPublished)
	if got, want := remoteRefValue(t, second, refName), refValue(t, second, refName); got != want {
		t.Fatalf("republished remote head = %q, want canonical %q", got, want)
	}
}

func TestSyncStopsBeforePushWhenFetchedHistoryDiverges(t *testing.T) {
	first, second, config := syncRepositories(t)
	conflicting := createSyncTask(t, first, config, "Conflicting task")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, conflicting.ID, "Remote branch")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, second, config, conflicting.ID, "Local branch")
	unrelated := createSyncTask(t, second, config, "Unrelated local task")

	result, err := second.Sync(context.Background(), config)
	if err == nil {
		t.Fatalf("Sync(diverged) error = nil; result = %#v", result)
	}
	assertSyncOutcome(t, result.Fetch, conflicting.ID, SyncDiverged)
	if result.Push.Status != SyncPhaseSkipped {
		t.Fatalf("divergent sync push status = %q, want %q", result.Push.Status, SyncPhaseSkipped)
	}
	if !strings.Contains(result.Push.Detail, "push skipped") {
		t.Fatalf("divergent sync push detail = %q, want skipped detail", result.Push.Detail)
	}
	if len(result.Push.Tasks) != 0 {
		t.Fatalf("Sync(diverged) pushed tasks = %#v, want none", result.Push.Tasks)
	}
	if remoteRefExists(t, second, taskRefPrefix+unrelated.ID) {
		t.Fatalf("sync published unrelated task %s after detecting divergence", unrelated.ID)
	}
}

func TestSyncReportsFailedFetchAndSkipsPushWhenOriginIsMissing(t *testing.T) {
	ctx := context.Background()
	path := testrepo.New(t)
	repo, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := repo.Init(ctx, "WB", core.CryptoULIDSource{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := repo.Sync(ctx, config)
	if err == nil {
		t.Fatalf("Sync(missing origin) error = nil; result = %#v", result)
	}
	if result.Fetch.Status != SyncPhaseFailed {
		t.Fatalf("missing-origin fetch status = %q, want %q", result.Fetch.Status, SyncPhaseFailed)
	}
	if !strings.Contains(result.Fetch.Detail, "fetch failed before completion") {
		t.Fatalf("missing-origin fetch detail = %q, want failed detail", result.Fetch.Detail)
	}
	if result.Push.Status != SyncPhaseSkipped {
		t.Fatalf("missing-origin push status = %q, want %q", result.Push.Status, SyncPhaseSkipped)
	}
	if len(result.Push.Tasks) != 0 {
		t.Fatalf("missing-origin push tasks = %#v, want none", result.Push.Tasks)
	}
}

func TestTaskOperationCommitsStayOutsideCheckedOutBranchHistory(t *testing.T) {
	first, _, config := syncRepositories(t)
	mainBefore := refValue(t, first, "HEAD")
	task := createSyncTask(t, first, config, "Branch-independent task")
	updateSyncTask(t, first, config, task.ID, "Still branch-independent")
	taskHead := refValue(t, first, taskRefPrefix+task.ID)
	mainAfter := refValue(t, first, "HEAD")

	if mainAfter != mainBefore {
		t.Fatalf("code branch HEAD moved from %q to %q", mainBefore, mainAfter)
	}
	if mergeBaseIsAncestor(t, first.Root, taskHead, mainAfter) {
		t.Fatalf("task commit %s is reachable from checked-out branch HEAD %s", taskHead, mainAfter)
	}
	if mergeBaseIsAncestor(t, first.Root, mainAfter, taskHead) {
		t.Fatalf("checked-out branch HEAD %s is reachable from task history %s", mainAfter, taskHead)
	}
}

func syncRepositories(t *testing.T) (*Repository, *Repository, core.ProjectConfig) {
	t.Helper()
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)

	seedPath := testrepo.New(t)
	syncGit(t, seedPath, "branch", "-M", "main")
	seed, err := Open(ctx, seedPath)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := seed.Init(ctx, "WB", core.CryptoULIDSource{})
	if err != nil {
		t.Fatal(err)
	}
	syncGit(t, seedPath, "add", ".workbook/config.json")
	syncGit(t, seedPath, "commit", "--quiet", "-m", "Initialize Workbook")
	syncGit(t, seedPath, "remote", "add", "origin", bare)
	syncGit(t, seedPath, "push", "--quiet", "-u", "origin", "main")
	syncGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	first := openSyncClone(t, bare)
	second := openSyncClone(t, bare)
	for _, repo := range []*Repository{first, second} {
		loaded, err := repo.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if loaded != config {
			t.Fatalf("clone config = %#v, want %#v", loaded, config)
		}
	}
	return first, second, config
}

func openSyncClone(t *testing.T, bare string) *Repository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clone")
	syncGit(t, t.TempDir(), "clone", "--quiet", bare, path)
	syncGit(t, path, "config", "user.name", "Workbook Test")
	syncGit(t, path, "config", "user.email", "workbook@example.test")
	repo, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func publishTaskRefs(t *testing.T, repo *Repository) {
	t.Helper()
	syncGit(t, repo.Root, "push", "origin", taskRefPrefix+"*:"+taskRefPrefix+"*")
}

func createSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, title string) core.Task {
	t.Helper()
	service := syncService(repo, config)
	result, err := service.CreateMutation(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return result.Task
}

func updateSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, title string) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
}

func syncService(repo *Repository, config core.ProjectConfig) core.Service {
	return core.Service{
		Config: config,
		Reader: repo,
		Writer: repo,
		IDs:    core.CryptoULIDSource{},
		Now:    time.Now,
		Actor:  "workbook@example.test",
	}
}

func assertSyncOutcome(t *testing.T, result SyncResult, taskID string, want SyncStatus) {
	t.Helper()
	for _, item := range result.Tasks {
		if item.TaskID == taskID {
			if item.Status != want {
				t.Fatalf("task %s status = %q, want %q; result = %#v", taskID, item.Status, want, result)
			}
			return
		}
	}
	t.Fatalf("result has no task %s: %#v", taskID, result)
}

func refValue(t *testing.T, repo *Repository, ref string) string {
	t.Helper()
	return syncGit(t, repo.Root, "rev-parse", "--verify", ref)
}

func refExists(t *testing.T, repo *Repository, ref string) bool {
	t.Helper()
	command := exec.Command("git", "-C", repo.Root, "show-ref", "--verify", "--quiet", ref)
	err := command.Run()
	if err == nil {
		return true
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git show-ref %s: %v", ref, err)
	return false
}

func remoteRefValue(t *testing.T, repo *Repository, ref string) string {
	t.Helper()
	output := syncGit(t, repo.Root, "ls-remote", "--refs", "origin", ref)
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != ref {
		t.Fatalf("git ls-remote %s output = %q", ref, output)
	}
	return fields[0]
}

func remoteRefExists(t *testing.T, repo *Repository, ref string) bool {
	t.Helper()
	output := syncGit(t, repo.Root, "ls-remote", "--refs", "origin", ref)
	return strings.TrimSpace(output) != ""
}

func mergeBaseIsAncestor(t *testing.T, directory, ancestor, descendant string) bool {
	t.Helper()
	command := exec.Command("git", "-C", directory, "merge-base", "--is-ancestor", ancestor, descendant)
	err := command.Run()
	if err == nil {
		return true
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git merge-base --is-ancestor %s %s: %v", ancestor, descendant, err)
	return false
}

func syncGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return stringTrimLine(output)
}

func syncGitInput(t *testing.T, directory string, input []byte, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return stringTrimLine(output)
}

func stringTrimLine(output []byte) string {
	result := string(output)
	for len(result) > 0 && (result[len(result)-1] == '\n' || result[len(result)-1] == '\r') {
		result = result[:len(result)-1]
	}
	return result
}
