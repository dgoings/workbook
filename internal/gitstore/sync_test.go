package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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

func TestFetchReplaysDivergentLocalHistory(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Divergent task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	updateSyncTask(t, first, config, task.ID, "Remote branch")
	setSyncTaskPriority(t, second, config, task.ID, core.PriorityHigh)
	publishTaskRefs(t, first)
	localTip := refValue(t, second, taskRefPrefix+task.ID)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(diverged) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)
	if len(result.Conflicts) != 0 {
		t.Fatalf("Fetch(diverged) conflicts = %#v, want none", result.Conflicts)
	}

	reconciled := refValue(t, second, taskRefPrefix+task.ID)
	if reconciled == localTip || reconciled == remoteTip {
		t.Fatalf("reconciled tip = %q, want a replayed commit distinct from %q and %q", reconciled, localTip, remoteTip)
	}
	if !mergeBaseIsAncestor(t, second.Root, remoteTip, reconciled) {
		t.Fatalf("reconciled tip %q is not a descendant of the fetched tip %q", reconciled, remoteTip)
	}
	if got := parentCount(t, second, reconciled); got != 1 {
		t.Fatalf("reconciled tip parent count = %d, want 1", got)
	}

	snapshot, err := second.ReadTaskHead(context.Background(), config, TaskHead{TaskID: task.ID, ObjectID: reconciled})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.State.Task.Title, "Remote branch"; got != want {
		t.Fatalf("reconciled title = %q, want the fetched %q", got, want)
	}
	if got, want := snapshot.State.Task.Priority, core.PriorityHigh; got != want {
		t.Fatalf("reconciled priority = %q, want the replayed %q", got, want)
	}

	parked := reconciledRefPrefix + task.ID + "/0"
	if !refExists(t, second, parked) {
		t.Fatalf("fetch did not park the orphaned local tip at %s", parked)
	}
	if got := refValue(t, second, parked); got != localTip {
		t.Fatalf("parked ref = %q, want the orphaned local tip %q", got, localTip)
	}
	if got, want := refValue(t, second, remoteTaskRefPrefix+task.ID), remoteTip; got != want {
		t.Fatalf("tracking tip = %q, want remote tip %q", got, want)
	}
}

func TestFetchReportsDescriptionConflictAndStopsAtTheFetchedTip(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Described task")
	setSyncTaskDescription(t, first, config, task.ID, "Base text")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	setSyncTaskDescription(t, first, config, task.ID, "Their text")
	setSyncTaskDescription(t, second, config, task.ID, "Our text")
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)

	result, err := second.Fetch(context.Background(), config)
	if core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Fetch(description conflict) error = %v, want a conflict; result = %#v", err, result)
	}
	if core.ExitCode(err) != 8 {
		t.Fatalf("conflict exit code = %d, want 8", core.ExitCode(err))
	}
	assertSyncOutcome(t, result, task.ID, SyncConflicted)
	if len(result.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want exactly one", result.Conflicts)
	}
	conflict := result.Conflicts[0]
	if conflict.TaskID != task.ID || conflict.Type != core.ConflictDescription || conflict.Description == nil {
		t.Fatalf("conflict = %#v, want a description conflict for %s", conflict, task.ID)
	}
	want := core.DescriptionConflict{Base: "Base text", Ours: "Our text", Theirs: "Their text"}
	if *conflict.Description != want {
		t.Fatalf("description conflict = %#v, want %#v", *conflict.Description, want)
	}
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("conflicted tip = %q, want the fetched tip %q", got, remoteTip)
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

// Anyone with push access can write an arbitrary name under origin's task
// namespace, deliberately or by accident. One stray ref there must not deny
// fetch, push, and sync to every clone, so it is skipped and reported by the
// name it holds on origin, which is where a user prunes it.
func TestSyncToleratesUnrecognizedRefUnderOriginTaskNamespace(t *testing.T) {
	first, second, config := syncRepositories(t)
	shared := createSyncTask(t, first, config, "Shared task")
	publishTaskRefs(t, first)
	syncGit(t, first.Root, "push", "origin", "HEAD:"+taskRefPrefix+"EVIL")
	syncGit(t, first.Root, "push", "origin", "HEAD:"+taskRefPrefix+"team/EVIL")

	local := createSyncTask(t, second, config, "Local task")
	result, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync(foreign ref) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, shared.ID, SyncCreated)
	assertSyncOutcome(t, result.Push, local.ID, SyncPublished)
	assertIgnoredRefs(t, result.Fetch, taskRefPrefix+"EVIL", taskRefPrefix+"team/EVIL")
	if refExists(t, second, taskRefPrefix+"EVIL") {
		t.Fatalf("foreign ref reached canonical ref %s", taskRefPrefix+"EVIL")
	}
	if got, want := refValue(t, second, taskRefPrefix+shared.ID), refValue(t, first, taskRefPrefix+shared.ID); got != want {
		t.Fatalf("shared canonical tip = %q, want %q", got, want)
	}
	if got, want := remoteRefValue(t, second, taskRefPrefix+local.ID), refValue(t, second, taskRefPrefix+local.ID); got != want {
		t.Fatalf("published local tip = %q, want %q", got, want)
	}

	fetched, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(foreign ref) error = %v; result = %#v", err, fetched)
	}
	assertIgnoredRefs(t, fetched, taskRefPrefix+"EVIL", taskRefPrefix+"team/EVIL")

	// Push reads origin's namespace directly rather than the mirror, so it
	// needs the same tolerance to publish anything at all.
	pushed, err := second.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push(foreign ref) error = %v; result = %#v", err, pushed)
	}
	assertIgnoredRefs(t, pushed, taskRefPrefix+"EVIL", taskRefPrefix+"team/EVIL")

	// Pruning the refs on origin clears both the report and the local mirror.
	syncGit(t, second.Root, "push", "origin", "--delete", taskRefPrefix+"EVIL", taskRefPrefix+"team/EVIL")
	cleaned, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync(pruned) error = %v; result = %#v", err, cleaned)
	}
	if len(cleaned.Fetch.Ignored) != 0 {
		t.Fatalf("ignored refs after pruning = %#v, want none", cleaned.Fetch.Ignored)
	}
	if refExists(t, second, remoteTaskRefPrefix+"EVIL") {
		t.Fatalf("pruned foreign ref survived in the tracking namespace")
	}
}

// assertIgnoredRefs requires exactly the named refs, each reported under the
// namespace it occupies on origin and each carrying a reason a user can act on.
func assertIgnoredRefs(t *testing.T, result SyncResult, refs ...string) {
	t.Helper()
	got := make([]string, 0, len(result.Ignored))
	for _, ignored := range result.Ignored {
		got = append(got, ignored.Ref)
		if !strings.HasPrefix(ignored.Ref, taskRefPrefix) {
			t.Fatalf("ignored ref = %q, want it named under %q as origin holds it", ignored.Ref, taskRefPrefix)
		}
		if strings.TrimSpace(ignored.Reason) == "" {
			t.Fatalf("ignored ref %q has no reason", ignored.Ref)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), refs...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ignored refs = %#v, want %#v", got, want)
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

func TestSyncReplaysDivergentHistoryAndPublishesIt(t *testing.T) {
	first, second, config := syncRepositories(t)
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
	setSyncTaskPriority(t, second, config, divergent.ID, core.PriorityHigh)
	unrelated := createSyncTask(t, second, config, "Unrelated local task")

	result, err := second.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync(diverged) error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, divergent.ID, SyncReconciled)
	assertSyncOutcome(t, result.Push, divergent.ID, SyncPublished)
	assertSyncOutcome(t, result.Push, unrelated.ID, SyncPublished)
	if got, want := remoteRefValue(t, second, taskRefPrefix+divergent.ID), refValue(t, second, taskRefPrefix+divergent.ID); got != want {
		t.Fatalf("published divergent tip = %q, want the reconciled local tip %q", got, want)
	}
}

// A partial replay is ordinary history the clone already holds, so sync
// publishes exactly what push would. Stopping at the conflict decides how far
// the ref advances; it does not decide whether that advance is shareable.
func TestSyncPublishesEveryReplayedOperationBeforeAConflict(t *testing.T) {
	first, second, config := syncRepositories(t)
	conflicting := createSyncTask(t, first, config, "Conflicting task")
	setSyncTaskDescription(t, first, config, conflicting.ID, "Base text")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// Origin gains one operation this clone has not seen.
	setSyncTaskDescription(t, first, config, conflicting.ID, "Their text")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	fetchedTip := refValue(t, first, taskRefPrefix+conflicting.ID)

	// Locally, a status change replays cleanly and the description change after
	// it does not.
	setSyncTaskStatus(t, second, config, conflicting.ID, core.StatusReady)
	setSyncTaskDescription(t, second, config, conflicting.ID, "Our text")
	unrelated := createSyncTask(t, second, config, "Unrelated local task")

	result, err := second.Sync(context.Background(), config)
	if core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync(conflict) error = %v, want a conflict; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, conflicting.ID, SyncConflicted)
	if len(result.Fetch.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want exactly one", result.Fetch.Conflicts)
	}
	assertSyncOutcome(t, result.Push, conflicting.ID, SyncPublished)
	assertSyncOutcome(t, result.Push, unrelated.ID, SyncPublished)

	canonical := refValue(t, second, taskRefPrefix+conflicting.ID)
	if canonical == fetchedTip {
		t.Fatalf("conflicted tip = %q, want the replayed status change on top of %q", canonical, fetchedTip)
	}
	if !mergeBaseIsAncestor(t, second.Root, fetchedTip, canonical) {
		t.Fatalf("conflicted tip %q is not a descendant of the fetched tip %q", canonical, fetchedTip)
	}
	if got := remoteRefValue(t, second, taskRefPrefix+conflicting.ID); got != canonical {
		t.Fatalf("published tip = %q, want the partially replayed local tip %q", got, canonical)
	}

	snapshot, err := second.Get(context.Background(), config, conflicting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.State.Task.Status, core.StatusReady; got != want {
		t.Fatalf("replayed status = %q, want %q", got, want)
	}
	if got, want := snapshot.State.Task.Description, "Their text"; got != want {
		t.Fatalf("description = %q, want the fetched %q with the conflicting local edit dropped", got, want)
	}
	if !remoteRefExists(t, second, taskRefPrefix+unrelated.ID) {
		t.Fatalf("sync did not publish unrelated task %s alongside a conflict", unrelated.ID)
	}
}

// Sync and push must agree about the same refs, so a partial replay that sync
// publishes is already up to date when push runs next.
func TestPushAgreesWithSyncAfterAPartialReplay(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Contested task")
	setSyncTaskDescription(t, first, config, task.ID, "Base text")
	if _, err := first.Sync(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	setSyncTaskDescription(t, first, config, task.ID, "Their text")
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	setSyncTaskStatus(t, second, config, task.ID, core.StatusReady)
	setSyncTaskDescription(t, second, config, task.ID, "Our text")

	if _, err := second.Sync(context.Background(), config); core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Sync(conflict) error = %v, want a conflict", err)
	}
	pushed, err := second.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() after a conflicted sync error = %v; result = %#v", err, pushed)
	}
	assertSyncOutcome(t, pushed, task.ID, SyncUpToDate)
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

func setSyncTaskDescription(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, description string) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Description: &description}); err != nil {
		t.Fatal(err)
	}
}

func setSyncTaskPriority(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string, priority core.Priority) {
	t.Helper()
	service := syncService(repo, config)
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Priority: &priority}); err != nil {
		t.Fatal(err)
	}
}

func parentCount(t *testing.T, repo *Repository, objectID string) int {
	t.Helper()
	output := syncGit(t, repo.Root, "rev-list", "--parents", "-n", "1", objectID)
	return len(strings.Fields(output)) - 1
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

func TestPushTaskPublishesOnlyTheNamedRef(t *testing.T) {
	repository, _, config := syncRepositories(t)
	target := createSyncTask(t, repository, config, "Targeted task")
	untouched := createSyncTask(t, repository, config, "Untouched task")

	result, err := repository.PushTask(context.Background(), config, target.ID)
	if err != nil {
		t.Fatalf("PushTask() error = %v; result = %#v", err, result)
	}
	if result.Status != SyncPublished {
		t.Fatalf("status = %q, want %q", result.Status, SyncPublished)
	}
	if got, want := remoteRefValue(t, repository, taskRefPrefix+target.ID), refValue(t, repository, taskRefPrefix+target.ID); got != want {
		t.Fatalf("published remote head = %q, want %q", got, want)
	}
	if remoteRefExists(t, repository, taskRefPrefix+untouched.ID) {
		t.Fatal("unrelated remote ref exists, want absent")
	}
}

func TestPushTaskReportsUpToDateWhenRemoteAlreadyMatches(t *testing.T) {
	repository, _, config := syncRepositories(t)
	task := createSyncTask(t, repository, config, "Repeated task")
	if _, err := repository.PushTask(context.Background(), config, task.ID); err != nil {
		t.Fatal(err)
	}

	result, err := repository.PushTask(context.Background(), config, task.ID)
	if err != nil {
		t.Fatalf("PushTask(up-to-date) error = %v; result = %#v", err, result)
	}
	if result.Status != SyncUpToDate {
		t.Fatalf("status = %q, want %q", result.Status, SyncUpToDate)
	}
}

func TestPushTaskReportsRejectionWhenRemoteAdvanced(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Contested task")
	if _, err := first.PushTask(context.Background(), config, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	updateSyncTask(t, second, config, task.ID, "Remote winner")
	if _, err := second.PushTask(context.Background(), config, task.ID); err != nil {
		t.Fatal(err)
	}
	remoteHead := refValue(t, second, taskRefPrefix+task.ID)

	updateSyncTask(t, first, config, task.ID, "Local loser")
	result, err := first.PushTask(context.Background(), config, task.ID)
	if err == nil {
		t.Fatalf("PushTask(rejected) error = nil; result = %#v", result)
	}
	if result.Status != SyncRejected {
		t.Fatalf("status = %q, want %q", result.Status, SyncRejected)
	}
	if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != remoteHead {
		t.Fatalf("remote head = %q, want unchanged %q", got, remoteHead)
	}
}

func TestPushTaskListsNoRemoteRefsAndPublishesOnce(t *testing.T) {
	repository, _, config := syncRepositories(t)
	task := createSyncTask(t, repository, config, "Bounded task")
	for i := 0; i < 10; i++ {
		createSyncTask(t, repository, config, fmt.Sprintf("Unrelated %02d", i))
	}

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	if _, err := repository.PushTask(context.Background(), config, task.ID); err != nil {
		t.Fatal(err)
	}
	if got := countCommandPrefix(commands, "ls-remote"); got != 0 {
		t.Fatalf("ls-remote invocations = %d, want 0; commands = %v", got, commands)
	}
	if got := countCommandPrefix(commands, "push"); got != 1 {
		t.Fatalf("push invocations = %d, want 1; commands = %v", got, commands)
	}
}

func TestPushTaskIgnoresUnrelatedMalformedLocalRef(t *testing.T) {
	repository, _, config := syncRepositories(t)
	target := createSyncTask(t, repository, config, "Valid target")
	broken := createSyncTask(t, repository, config, "Malformed neighbour")
	brokenHead := refValue(t, repository, taskRefPrefix+broken.ID)
	blob := syncGitInput(t, repository.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, repository.Root, "update-ref", taskRefPrefix+broken.ID, blob, brokenHead)

	result, err := repository.PushTask(context.Background(), config, target.ID)
	if err != nil {
		t.Fatalf("PushTask() error = %v; result = %#v", err, result)
	}
	if result.Status != SyncPublished {
		t.Fatalf("status = %q, want %q", result.Status, SyncPublished)
	}
}

func TestPushTaskRejectsMalformedTargetBeforePublishing(t *testing.T) {
	repository, _, config := syncRepositories(t)
	task := createSyncTask(t, repository, config, "Malformed target")
	head := refValue(t, repository, taskRefPrefix+task.ID)
	blob := syncGitInput(t, repository.Root, []byte("not a task commit"), "hash-object", "-w", "--stdin")
	syncGit(t, repository.Root, "update-ref", taskRefPrefix+task.ID, blob, head)

	result, err := repository.PushTask(context.Background(), config, task.ID)
	if err == nil {
		t.Fatalf("PushTask(malformed) error = nil; result = %#v", result)
	}
	if result.Status != SyncInvalid {
		t.Fatalf("status = %q, want %q", result.Status, SyncInvalid)
	}
	if remoteRefExists(t, repository, taskRefPrefix+task.ID) {
		t.Fatal("remote ref exists, want absent after refusing to publish a malformed tip")
	}
}
