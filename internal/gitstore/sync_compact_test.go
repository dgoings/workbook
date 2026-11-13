package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

const (
	wantDeniedDetail = "origin refuses non-fast-forward updates to Workbook refs " +
		"(for example, a server-side hook or ruleset); the compacted task stays local until that changes"
	wantStaleDetail = "origin changed since the compaction; the next sync reconciles it"
)

// compactedSyncTask is a Done task origin holds at tip, compacted in one clone
// into checkpoint and not yet published, as `compact --no-sync` leaves it.
type compactedSyncTask struct {
	taskID     string
	tip        core.Snapshot
	checkpoint core.Snapshot
	// history is every commit the checkpoint replaced.
	history []string
}

// publishAndCompact creates a Done task in repo, publishes it with Sync so the
// tracking ref names the tip origin holds, and then compacts it locally.
func publishAndCompact(t *testing.T, repo *Repository, config core.ProjectConfig) compactedSyncTask {
	t.Helper()
	ctx := context.Background()
	task := createSyncTask(t, repo, config, "Compact and publish")
	updateSyncTask(t, repo, config, task.ID, "Compact and publish, revised")
	done := core.StatusDone
	if _, err := syncService(repo, config).UpdateMutation(ctx, task.ID, core.UpdateInput{Status: &done}); err != nil {
		t.Fatalf("UpdateMutation(done) error = %v", err)
	}
	if result, err := repo.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() before compaction error = %v; result = %#v", err, result)
	}
	// The push does not move the tracking ref; the next fetch does, as the
	// fetch compact runs first does.
	if result, err := repo.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch() before compaction error = %v; result = %#v", err, result)
	}
	tip := currentSnapshot(t, repo, config, task.ID)
	if got := remoteRefValue(t, repo, taskRefPrefix+task.ID); got != tip.Head {
		t.Fatalf("origin holds %s, want the tip %s before compaction", got, tip.Head)
	}
	if got := refValue(t, repo, remoteTaskRefPrefix+task.ID); got != tip.Head {
		t.Fatalf("tracking ref = %s, want the tip %s before compaction", got, tip.Head)
	}
	return compactLocally(t, repo, config, task.ID)
}

// compactLocally compacts taskID's current tip in repo and publishes nothing.
func compactLocally(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) compactedSyncTask {
	t.Helper()
	tip := currentSnapshot(t, repo, config, taskID)
	history := strings.Fields(syncGit(t, repo.Root, "rev-list", tip.Head))
	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))
	if err := repo.CompactTaskRef(context.Background(), config, taskID, tip.Head, checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	return compactedSyncTask{taskID: taskID, tip: tip, checkpoint: checkpoint, history: history}
}

// assertNoLease fails if any push carried a lease or forced a refspec.
func assertNoLease(t *testing.T, commands [][]string) {
	t.Helper()
	for _, command := range commands {
		if len(command) == 0 || command[0] != "push" {
			continue
		}
		for _, arg := range command {
			if strings.HasPrefix(arg, "--force") || arg == "-f" || strings.HasPrefix(arg, "+") {
				t.Fatalf("push leased or forced a ref that needed neither: %v", command)
			}
		}
	}
}

func observeCommands(repo *Repository) *[][]string {
	commands := &[][]string{}
	repo.commandObserver = func(args []string) {
		*commands = append(*commands, append([]string(nil), args...))
	}
	return commands
}

// assertLeasedPush fails unless exactly one push carried the lease on the
// compacted tip and the plain refspec for the checkpoint, and no push forced
// anything unconditionally. A `+` refspec counts as unconditional: Git lets it
// override the lease.
func assertLeasedPush(t *testing.T, commands [][]string, compacted compactedSyncTask) {
	t.Helper()
	ref := taskRefPrefix + compacted.taskID
	lease := "--force-with-lease=" + ref + ":" + compacted.tip.Head
	refspec := compacted.checkpoint.Head + ":" + ref
	leased := 0
	for _, command := range commands {
		if len(command) == 0 || command[0] != "push" {
			continue
		}
		for _, arg := range command {
			if arg == "--force" || arg == "-f" || arg == "--force-with-lease" || strings.HasPrefix(arg, "+") {
				t.Fatalf("push forced unconditionally: %v", command)
			}
		}
		if slices.Contains(command, lease) && slices.Contains(command, refspec) {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased pushes = %d, want one carrying %q and %q; commands = %v", leased, lease, refspec, commands)
	}
}

func assertPublishedCheckpoint(t *testing.T, repo *Repository, compacted compactedSyncTask) {
	t.Helper()
	if got := remoteRefValue(t, repo, taskRefPrefix+compacted.taskID); got != compacted.checkpoint.Head {
		t.Fatalf("origin holds %s, want the checkpoint %s", got, compacted.checkpoint.Head)
	}
	if got := refValue(t, repo, remoteTaskRefPrefix+compacted.taskID); got != compacted.checkpoint.Head {
		t.Fatalf("tracking ref = %s, want the checkpoint %s", got, compacted.checkpoint.Head)
	}
}

// assertUnpublishedCheckpoint fails unless origin and the tracking ref still
// name the compacted tip and the local ref still names the checkpoint.
func assertUnpublishedCheckpoint(t *testing.T, repo *Repository, compacted compactedSyncTask, remote string) {
	t.Helper()
	if got := remoteRefValue(t, repo, taskRefPrefix+compacted.taskID); got != remote {
		t.Fatalf("origin holds %s, want it unchanged at %s", got, remote)
	}
	if got := refValue(t, repo, taskRefPrefix+compacted.taskID); got != compacted.checkpoint.Head {
		t.Fatalf("local ref = %s, want it still on the checkpoint %s", got, compacted.checkpoint.Head)
	}
	if got := refValue(t, repo, remoteTaskRefPrefix+compacted.taskID); got != compacted.tip.Head {
		t.Fatalf("tracking ref = %s, want it still on the compacted tip %s", got, compacted.tip.Head)
	}
}

func syncOutcome(t *testing.T, result SyncResult, taskID string) SyncTaskResult {
	t.Helper()
	for _, item := range result.Tasks {
		if item.TaskID == taskID {
			return item
		}
	}
	t.Fatalf("result has no task %s: %#v", taskID, result)
	return SyncTaskResult{}
}

func TestPushPublishesLocalCheckpointWithLease(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	compacted := publishAndCompact(t, first, config)
	commands := observeCommands(first)

	result, err := first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, compacted.taskID, SyncCompactionPublished)
	assertPublishedCheckpoint(t, first, compacted)
	assertLeasedPush(t, *commands, compacted)

	// A second push finds origin already on the checkpoint.
	result, err = first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("second Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, compacted.taskID, SyncUpToDate)
}

func TestPushTaskPublishesLocalCheckpointWithLease(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	compacted := publishAndCompact(t, first, config)
	commands := observeCommands(first)

	result, err := first.PushTask(context.Background(), config, compacted.taskID)
	if err != nil {
		t.Fatalf("PushTask() error = %v; result = %#v", err, result)
	}
	if result.Status != SyncCompactionPublished {
		t.Fatalf("PushTask() status = %q, want %q", result.Status, SyncCompactionPublished)
	}
	assertPublishedCheckpoint(t, first, compacted)
	assertLeasedPush(t, *commands, compacted)
}

// PushTasks publishes the named tasks in one push, leasing the compacted one
// and pushing an ordinary one plainly.
func TestPushTasksPublishesLocalCheckpointWithLease(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	compacted := publishAndCompact(t, first, config)
	ordinary := createSyncTask(t, first, config, "Ordinary local work")
	untouched := createSyncTask(t, first, config, "Not named")
	commands := observeCommands(first)

	result, err := first.PushTasks(context.Background(), config, []string{compacted.taskID, ordinary.ID})
	if err != nil {
		t.Fatalf("PushTasks() error = %v; result = %#v", err, result)
	}
	if result.Status != SyncPhaseCompleted || len(result.Tasks) != 2 {
		t.Fatalf("PushTasks() result = %#v, want a completed phase with two tasks", result)
	}
	assertSyncOutcome(t, result, compacted.taskID, SyncCompactionPublished)
	assertSyncOutcome(t, result, ordinary.ID, SyncPublished)
	assertPublishedCheckpoint(t, first, compacted)
	assertLeasedPush(t, *commands, compacted)
	if got := countTaskPushes(*commands); got != 1 {
		t.Fatalf("task pushes = %d, want one for both tasks; commands = %v", got, *commands)
	}
	if remoteRefExists(t, first, taskRefPrefix+untouched.ID) {
		t.Fatalf("PushTasks() published %s, which it was not given", untouched.ID)
	}
}

func TestPublishFetchedPublishesCheckpoint(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	compacted := publishAndCompact(t, first, config)
	commands := observeCommands(first)

	result, err := first.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, compacted.taskID, SyncLocalAhead)
	assertSyncOutcome(t, result.Push, compacted.taskID, SyncCompactionPublished)
	assertPublishedCheckpoint(t, first, compacted)
	assertLeasedPush(t, *commands, compacted)

	// The tracking ref moved with the push, so the next run has nothing to do.
	result, err = first.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("second Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, compacted.taskID, SyncUnchanged)
	assertSyncOutcome(t, result.Push, compacted.taskID, SyncUpToDate)
}

// refuseTaskRefUpdates installs an update hook in origin that refuses every
// change to a task ref, standing in for a host whose policy refuses the
// checkpoint. Git reports the refusal as "[remote rejected] (hook declined)".
func refuseTaskRefUpdates(t *testing.T, repo *Repository) {
	t.Helper()
	bare := syncGit(t, repo.Root, "config", "remote.origin.url")
	hooks := filepath.Join(bare, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("create %s: %v", hooks, err)
	}
	testenv.WriteExecutable(t, filepath.Join(hooks, "update"), []byte(
		"#!/bin/sh\ncase \"$1\" in refs/workbook/tasks/*) echo 'task refs are frozen' >&2; exit 1;; esac\nexit 0\n"))
}

// An origin that refuses the leased push leaves the task compacted locally and
// unpublished, through every push path, and the refusal is reported per task.
func TestLeasedPushRejectedByOriginLeavesTheCheckpointLocal(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"push", "sync", "push-task", "push-tasks"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			first, _, config := syncRepositories(t)
			compacted := publishAndCompact(t, first, config)
			refuseTaskRefUpdates(t, first)

			ctx := context.Background()
			var (
				result SyncResult
				err    error
			)
			switch path {
			case "push":
				result, err = first.Push(ctx, config)
			case "sync":
				var run SyncRunResult
				run, err = first.Sync(ctx, config)
				result = run.Push
			case "push-task":
				var item SyncTaskResult
				item, err = first.PushTask(ctx, config, compacted.taskID)
				result.Tasks = []SyncTaskResult{item}
			case "push-tasks":
				result, err = first.PushTasks(ctx, config, []string{compacted.taskID})
			}
			if got := core.CategoryOf(err); got != core.CategoryOperational {
				t.Fatalf("%s error category = %q, want %q; error = %v", path, got, core.CategoryOperational, err)
			}
			item := syncOutcome(t, result, compacted.taskID)
			want := SyncTaskResult{TaskID: compacted.taskID, Status: SyncRejected, Detail: "[remote rejected] (hook declined)"}
			if item != want {
				t.Fatalf("%s outcome = %#v, want %#v", path, item, want)
			}
			assertUnpublishedCheckpoint(t, first, compacted, compacted.tip.Head)
		})
	}
}

// The design expected a remote with receive.denyNonFastForwards (as `git init
// --bare --shared` sets) to refuse the checkpoint. Git applies that setting to
// refs/heads/ only, so origin accepts the lease on a task ref. This pins that
// behavior: the denied-detail translation is reachable only through a host
// that refuses with Git's "non-fast-forward" reason on its own terms.
func TestDenyNonFastForwardsDoesNotGuardTaskRefs(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	bare := syncGit(t, first.Root, "config", "remote.origin.url")
	syncGit(t, bare, "config", "receive.denyNonFastForwards", "true")
	compacted := publishAndCompact(t, first, config)

	result, err := first.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, compacted.taskID, SyncCompactionPublished)
	assertPublishedCheckpoint(t, first, compacted)
}

// Once origin holds the checkpoint, an edit on top of it is an ordinary
// fast-forward: no lease, and nothing stale about it.
func TestPublishedCheckpointExtendedLocallyPushesAsAFastForward(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"push-task", "push"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, _, config := syncRepositories(t)
			compacted := publishAndCompact(t, first, config)
			if _, err := first.Push(ctx, config); err != nil {
				t.Fatalf("Push() of the checkpoint error = %v", err)
			}
			assertPublishedCheckpoint(t, first, compacted)
			updateSyncTask(t, first, config, compacted.taskID, "Edited after publication")
			extended := refValue(t, first, taskRefPrefix+compacted.taskID)
			if parent := syncGit(t, first.Root, "rev-parse", extended+"^"); parent != compacted.checkpoint.Head {
				t.Fatalf("edit's parent = %s, want the checkpoint %s", parent, compacted.checkpoint.Head)
			}
			commands := observeCommands(first)

			var (
				item SyncTaskResult
				err  error
			)
			if path == "push-task" {
				item, err = first.PushTask(ctx, config, compacted.taskID)
			} else {
				var result SyncResult
				result, err = first.Push(ctx, config)
				item = syncOutcome(t, result, compacted.taskID)
			}
			if err != nil {
				t.Fatalf("%s error = %v", path, err)
			}
			if want := (SyncTaskResult{TaskID: compacted.taskID, Status: SyncPublished}); item != want {
				t.Fatalf("%s outcome = %#v, want %#v", path, item, want)
			}
			if got := remoteRefValue(t, first, taskRefPrefix+compacted.taskID); got != extended {
				t.Fatalf("origin holds %s, want the new tip %s", got, extended)
			}
			assertNoLease(t, *commands)
		})
	}
}

// A task origin never held has nothing to lease against, so its checkpoint is
// published as an ordinary create.
func TestCompactedTaskOriginNeverHeldIsCreatedAtTheCheckpoint(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"push-task", "push"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, _, config := syncRepositories(t)
			task := createSyncTask(t, first, config, "Never published")
			done := core.StatusDone
			if _, err := syncService(first, config).UpdateMutation(ctx, task.ID, core.UpdateInput{Status: &done}); err != nil {
				t.Fatalf("UpdateMutation(done) error = %v", err)
			}
			compacted := compactLocally(t, first, config, task.ID)
			if remoteRefExists(t, first, taskRefPrefix+task.ID) || refExists(t, first, remoteTaskRefPrefix+task.ID) {
				t.Fatalf("origin or the tracking namespace already holds %s", task.ID)
			}
			commands := observeCommands(first)

			var (
				item SyncTaskResult
				err  error
			)
			if path == "push-task" {
				item, err = first.PushTask(ctx, config, task.ID)
			} else {
				var result SyncResult
				result, err = first.Push(ctx, config)
				item = syncOutcome(t, result, task.ID)
			}
			if err != nil {
				t.Fatalf("%s error = %v", path, err)
			}
			if want := (SyncTaskResult{TaskID: task.ID, Status: SyncPublished}); item != want {
				t.Fatalf("%s outcome = %#v, want %#v", path, item, want)
			}
			if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != compacted.checkpoint.Head {
				t.Fatalf("origin holds %s, want the checkpoint %s", got, compacted.checkpoint.Head)
			}
			assertNoLease(t, *commands)
		})
	}
}

// A teammate's push after the compaction moves origin off the compacted tip.
// PushTask leases against the tip the tracking ref last saw, so the lease fails
// as stale and origin keeps the teammate's work. Push lists origin and sees a
// tip this clone has never fetched, which it cannot prove the checkpoint
// accounts for, so it pushes plainly and Git refuses the non-fast-forward.
func TestLeasedPushStaleLease(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"push-task", "push"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			first, second, config := syncRepositories(t)
			compacted := publishAndCompact(t, first, config)
			if _, err := second.Sync(context.Background(), config); err != nil {
				t.Fatalf("second Sync() error = %v", err)
			}
			updateSyncTask(t, second, config, compacted.taskID, "Edited after the compaction")
			if _, err := second.Push(context.Background(), config); err != nil {
				t.Fatalf("second Push() error = %v", err)
			}
			moved := refValue(t, second, taskRefPrefix+compacted.taskID)
			if got := remoteRefValue(t, first, taskRefPrefix+compacted.taskID); got != moved || moved == compacted.tip.Head {
				t.Fatalf("origin holds %s, want the teammate's edit %s on top of %s", got, moved, compacted.tip.Head)
			}
			commands := observeCommands(first)

			var (
				item SyncTaskResult
				err  error
			)
			if path == "push-task" {
				item, err = first.PushTask(context.Background(), config, compacted.taskID)
			} else {
				var result SyncResult
				result, err = first.Push(context.Background(), config)
				item = syncOutcome(t, result, compacted.taskID)
			}
			if got := core.CategoryOf(err); got != core.CategoryOperational {
				t.Fatalf("%s error category = %q, want %q; error = %v", path, got, core.CategoryOperational, err)
			}
			assertUnpublishedCheckpoint(t, first, compacted, moved)
			if path == "push-task" {
				if want := (SyncTaskResult{TaskID: compacted.taskID, Status: SyncRejected, Detail: wantStaleDetail}); item != want {
					t.Fatalf("%s outcome = %#v, want %#v", path, item, want)
				}
				assertLeasedPush(t, *commands, compacted)
				return
			}
			if item.Status != SyncRejected || item.Detail == wantStaleDetail {
				t.Fatalf("%s outcome = %#v, want an ordinary rejection", path, item)
			}
			assertNoLease(t, *commands)
		})
	}
}

// Once the checkpoint is published and the tracking ref follows it, nothing in
// the compacting clone reaches the old history, so gc prunes it.
func TestCompactionGCRemovesHistoryAfterPublish(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	compacted := publishAndCompact(t, first, config)
	if _, err := first.Push(context.Background(), config); err != nil {
		t.Fatalf("Push() error = %v", err)
	}

	syncGit(t, first.Root, "gc", "--prune=now", "--quiet")

	if len(compacted.history) < 3 {
		t.Fatalf("compacted history = %v, want at least three commits", compacted.history)
	}
	for _, object := range compacted.history {
		if err := exec.Command("git", "-C", first.Root, "cat-file", "-e", object).Run(); err == nil {
			t.Fatalf("commit %s of the compacted history survived gc --prune=now after publication", object)
		}
	}
	if err := exec.Command("git", "-C", first.Root, "cat-file", "-e", compacted.checkpoint.Head).Run(); err != nil {
		t.Fatalf("checkpoint %s did not survive gc: %v", compacted.checkpoint.Head, err)
	}
}

// The forced flag is accepted only for a ref the caller leased, and a leased
// ref's rejection is translated into what the user has to know.
func TestPushRejectsPlusFlagForOrdinaryRefs(t *testing.T) {
	t.Parallel()
	const (
		leasedRef = "refs/workbook/tasks/task-leased"
		plainRef  = "refs/workbook/tasks/task-plain"
	)
	expected := map[string]string{leasedRef: "task-leased", plainRef: "task-plain"}
	forced := map[string]bool{leasedRef: true}

	if _, err := parsePushPorcelain([]byte(
		"=\tabc:"+leasedRef+"\t[up to date]\n"+
			"+\tdef:"+plainRef+"\tabc...def (forced update)\n",
	), expected, forced, nil); err == nil {
		t.Fatalf("parsePushPorcelain() accepted a forced update of a ref it did not lease")
	}

	tests := []struct {
		name   string
		output string
		want   map[string]SyncTaskResult
	}{
		{
			name: "leased update accepted",
			output: "+\tabc:" + leasedRef + "\t111...abc (forced update)\n" +
				" \tdef:" + plainRef + "\t222..def\n",
			want: map[string]SyncTaskResult{
				"task-leased": {TaskID: "task-leased", Status: SyncCompactionPublished},
				"task-plain":  {TaskID: "task-plain", Status: SyncPublished},
			},
		},
		{
			name: "stale lease",
			output: "!\tabc:" + leasedRef + "\t[rejected] (stale info)\n" +
				"!\tdef:" + plainRef + "\t[rejected] (non-fast-forward)\n",
			want: map[string]SyncTaskResult{
				"task-leased": {TaskID: "task-leased", Status: SyncRejected, Detail: wantStaleDetail},
				"task-plain":  {TaskID: "task-plain", Status: SyncRejected, Detail: "[rejected] (non-fast-forward)"},
			},
		},
		{
			name: "origin refuses non-fast-forward",
			output: "!\tabc:" + leasedRef + "\t[remote rejected] (non-fast-forward)\n" +
				"=\tdef:" + plainRef + "\t[up to date]\n",
			want: map[string]SyncTaskResult{
				"task-leased": {TaskID: "task-leased", Status: SyncRejected, Detail: wantDeniedDetail},
				"task-plain":  {TaskID: "task-plain", Status: SyncUpToDate},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePushPorcelain([]byte(test.output), expected, forced, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("outcomes = %#v, want %#v", got, test.want)
			}
		})
	}
}

// sharedSyncTask creates a task in first and synchronizes both clones, so each
// holds the same tip and the same tracking ref for it.
func sharedSyncTask(t *testing.T, first, second *Repository, config core.ProjectConfig, title string) core.Task {
	t.Helper()
	ctx := context.Background()
	task := createSyncTask(t, first, config, title)
	updateSyncTask(t, first, config, task.ID, title+", revised")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	if result, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v; result = %#v", err, result)
	}
	if got, want := refValue(t, second, taskRefPrefix+task.ID), refValue(t, first, taskRefPrefix+task.ID); got != want {
		t.Fatalf("second clone holds %s, want the shared tip %s", got, want)
	}
	return task
}

// publishCheckpoint pushes a checkpoint repo compacted and requires origin to
// take it with a lease.
func publishCheckpoint(t *testing.T, repo *Repository, config core.ProjectConfig, compacted compactedSyncTask) {
	t.Helper()
	result, err := repo.Push(context.Background(), config)
	if err != nil {
		t.Fatalf("Push() of the checkpoint error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, compacted.taskID, SyncCompactionPublished)
	if got := remoteRefValue(t, repo, taskRefPrefix+compacted.taskID); got != compacted.checkpoint.Head {
		t.Fatalf("origin holds %s, want the checkpoint %s", got, compacted.checkpoint.Head)
	}
}

func operationIDs(snapshot core.Snapshot) []string {
	ids := make([]string, 0, len(snapshot.Operation.Operations))
	for _, operation := range snapshot.Operation.Operations {
		ids = append(ids, operation.ID)
	}
	return ids
}

func assertNoParkedRefs(t *testing.T, repo *Repository, taskID string) {
	t.Helper()
	if names := parkedRefNames(t, repo, taskID); len(names) != 0 {
		t.Fatalf("parked refs for %s = %v, want none", taskID, names)
	}
}

// assertUnreferenced fails if any ref or the task ref's reflog still reaches
// commit.
func assertUnreferenced(t *testing.T, repo *Repository, taskID, commit string) {
	t.Helper()
	if refs := syncGit(t, repo.Root, "for-each-ref", "--contains", commit, "--format=%(refname)"); refs != "" {
		t.Fatalf("refs still reach %s: %s", commit, refs)
	}
	if slices.Contains(taskReflogObjects(t, repo, taskID), commit) {
		t.Fatalf("task %s reflog still names %s", taskID, commit)
	}
}

func pushOutcome(result SyncResult, taskID string) (SyncTaskResult, bool) {
	for _, item := range result.Tasks {
		if item.TaskID == taskID {
			return item, true
		}
	}
	return SyncTaskResult{}, false
}

// A clone with nothing unpushed moves straight to origin's checkpoint and keeps
// nothing of the old history: no parked ref and no reflog entry.
func TestFetchAdoptsCheckpointWithNothingUnpushed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Adopt me")
	old := currentSnapshot(t, second, config, task.ID)
	// A parked tip from an earlier reconciliation, which adoption must drop.
	syncGit(t, second.Root, "update-ref", reconciledRefPrefix+task.ID+"/0", old.Head)
	if !slices.Contains(taskReflogObjects(t, second, task.ID), old.Head) {
		t.Fatalf("second clone's reflog does not name %s before adoption; the check below would prove nothing", old.Head)
	}
	compacted := compactLocally(t, first, config, task.ID)
	publishCheckpoint(t, first, config, compacted)

	result, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	if item := syncOutcome(t, result.Fetch, task.ID); item.Status != SyncAdopted {
		t.Fatalf("fetch outcome = %#v, want %q", item, SyncAdopted)
	}
	assertSyncOutcome(t, result.Push, task.ID, SyncUpToDate)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != compacted.checkpoint.Head {
		t.Fatalf("second clone holds %s, want origin's checkpoint %s", got, compacted.checkpoint.Head)
	}
	assertNoParkedRefs(t, second, task.ID)
	reflog := taskReflogObjects(t, second, task.ID)
	for _, object := range compacted.history {
		if slices.Contains(reflog, object) {
			t.Fatalf("task reflog %v still names %s of the old history", reflog, object)
		}
	}
}

// Unpushed local changes are replayed onto origin's checkpoint as ordinary
// commits in its generation, keeping their operation IDs, and publish as a
// fast-forward.
func TestFetchAdoptsCheckpointAndReplaysLocalOnlyOperations(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			var first, second *Repository
			var config core.ProjectConfig
			if objectFormat == testrepo.FormatSHA1 {
				first, second, config = syncRepositories(t)
			} else {
				first, second, config = syncRepositoriesWithObjectFormat(t, objectFormat)
			}
			testFetchAdoptsCheckpointAndReplays(t, first, second, config)
		})
	}
}

func testFetchAdoptsCheckpointAndReplays(t *testing.T, first, second *Repository, config core.ProjectConfig) {
	t.Helper()
	ctx := context.Background()
	task := sharedSyncTask(t, first, second, config, "Adopt and replay")
	setSyncTaskDescription(t, second, config, task.ID, "Written offline")
	local := currentSnapshot(t, second, config, task.ID)
	compacted := compactLocally(t, first, config, task.ID)
	publishCheckpoint(t, first, config, compacted)
	commands := observeCommands(second)

	result, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	if item := syncOutcome(t, result.Fetch, task.ID); item.Status != SyncAdoptedReplayed {
		t.Fatalf("fetch outcome = %#v, want %q", item, SyncAdoptedReplayed)
	}
	adopted := currentSnapshot(t, second, config, task.ID)
	if parent := syncGit(t, second.Root, "rev-parse", adopted.Head+"^"); parent != compacted.checkpoint.Head {
		t.Fatalf("replayed commit's parent = %s, want origin's checkpoint %s", parent, compacted.checkpoint.Head)
	}
	if adopted.State.Task.Description != "Written offline" {
		t.Fatalf("description = %q, want the offline edit", adopted.State.Task.Description)
	}
	if got, want := operationIDs(adopted), operationIDs(local); !slices.Equal(got, want) {
		t.Fatalf("replayed operation IDs = %v, want the original %v", got, want)
	}
	if got, want := adopted.Operation.HistoryGeneration, compacted.checkpoint.Operation.HistoryGeneration; got != want {
		t.Fatalf("replayed generation = %s, want the checkpoint's %s", got, want)
	}
	if adopted.State.LogicalClock != compacted.checkpoint.State.LogicalClock+1 {
		t.Fatalf("replayed clock = %d, want one past the checkpoint's %d",
			adopted.State.LogicalClock, compacted.checkpoint.State.LogicalClock)
	}
	assertNoParkedRefs(t, second, task.ID)
	assertUnreferenced(t, second, task.ID, local.Head)

	// The push phase of the same run published it as an ordinary fast-forward.
	assertSyncOutcome(t, result.Push, task.ID, SyncPublished)
	assertNoLease(t, *commands)
	if got := remoteRefValue(t, second, taskRefPrefix+task.ID); got != adopted.Head {
		t.Fatalf("origin holds %s, want the replayed tip %s", got, adopted.Head)
	}
	fetched, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, fetched)
	}
	assertSyncOutcome(t, fetched.Fetch, task.ID, SyncFastForwarded)
	if got := refValue(t, first, taskRefPrefix+task.ID); got != adopted.Head {
		t.Fatalf("first clone holds %s, want the replayed tip %s", got, adopted.Head)
	}
}

// A local-only operation whose effect origin's checkpoint already has is still
// replayed as a commit. Dropping it as a no-op, as an ordinary reconcile does,
// would leave its ID on neither the checkpoint's list nor the new chain, and
// every guard on the move would then read it as lost.
func TestFetchAdoptionRecordsALocalOperationTheCheckpointAlreadyHas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Agreed")
	updateSyncTask(t, second, config, task.ID, "Agreed title")
	local := currentSnapshot(t, second, config, task.ID)
	updateSyncTask(t, first, config, task.ID, "Agreed title")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	compacted := compactLocally(t, first, config, task.ID)
	publishCheckpoint(t, first, config, compacted)

	result, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncAdoptedReplayed)
	adopted := currentSnapshot(t, second, config, task.ID)
	if parent := syncGit(t, second.Root, "rev-parse", adopted.Head+"^"); parent != compacted.checkpoint.Head {
		t.Fatalf("replayed commit's parent = %s, want origin's checkpoint %s", parent, compacted.checkpoint.Head)
	}
	if got, want := operationIDs(adopted), operationIDs(local); !slices.Equal(got, want) {
		t.Fatalf("replayed operation IDs = %v, want the original %v", got, want)
	}
	if err := second.ValidateTaskHeadAdvances(ctx, config, []HeadAdvance{{
		Previous: local,
		Current:  TaskHead{TaskID: task.ID, ObjectID: adopted.Head},
	}}); err != nil {
		t.Fatalf("ValidateTaskHeadAdvances() from the old tip error = %v", err)
	}
}

// A replay that conflicts leaves the local ref on the old history and reports
// how to adopt by hand. Title is last-syncer-wins under the scalar rules, so
// the conflict is made with the description, the field the replay three-way
// compares.
func TestFetchAdoptionFallsBackOnConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Contested")
	setSyncTaskDescription(t, second, config, task.ID, "Our text")
	setSyncTaskDescription(t, first, config, task.ID, "Their text")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	compacted := compactLocally(t, first, config, task.ID)
	publishCheckpoint(t, first, config, compacted)
	local := refValue(t, second, taskRefPrefix+task.ID)
	title := currentSnapshot(t, second, config, task.ID).State.Task.Title
	want := SyncTaskResult{
		TaskID: task.ID,
		Status: SyncCompactedOnOrigin,
		Detail: `"` + title + `" was compacted on origin; you have 1 unpushed changes on the old history. ` +
			`Run "workbook compact --adopt ` + task.ID + `" to retry, ` +
			`or add --discard-local to take origin's version after saving yours to a bundle.`,
	}

	for attempt := 1; attempt <= 2; attempt++ {
		result, err := second.Sync(ctx, config)
		if got := core.CategoryOf(err); got != core.CategoryOperational {
			t.Fatalf("Sync() #%d error category = %q, want %q; error = %v; result = %#v",
				attempt, got, core.CategoryOperational, err, result)
		}
		if result.Fetch.Status != SyncPhaseCompleted {
			t.Fatalf("Sync() #%d fetch = %#v, want a completed phase", attempt, result.Fetch)
		}
		if item := syncOutcome(t, result.Fetch, task.ID); item != want {
			t.Fatalf("Sync() #%d fetch outcome = %#v, want %#v", attempt, item, want)
		}
		if item, found := pushOutcome(result.Push, task.ID); found {
			t.Fatalf("Sync() #%d pushed the task anyway: %#v", attempt, item)
		}
		if got := refValue(t, second, taskRefPrefix+task.ID); got != local {
			t.Fatalf("Sync() #%d moved the local ref to %s, want it left at %s", attempt, got, local)
		}
		if got := remoteRefValue(t, second, taskRefPrefix+task.ID); got != compacted.checkpoint.Head {
			t.Fatalf("Sync() #%d left origin at %s, want the checkpoint %s", attempt, got, compacted.checkpoint.Head)
		}
		assertNoParkedRefs(t, second, task.ID)
	}
}

// A clone that compacted without publishing, while a teammate added a change to
// the old history, replays that change onto its checkpoint and then leases
// against the teammate's tip.
func TestFetchReconcilesRemoteEditAfterLocalCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Edited after compaction")
	compacted := compactLocally(t, first, config, task.ID)
	updateSyncTask(t, second, config, task.ID, "The teammate's title")
	if result, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v; result = %#v", err, result)
	}
	teammate := currentSnapshot(t, second, config, task.ID)
	commands := observeCommands(first)

	result, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncReconciled)
	reconciled := currentSnapshot(t, first, config, task.ID)
	if parent := syncGit(t, first.Root, "rev-parse", reconciled.Head+"^"); parent != compacted.checkpoint.Head {
		t.Fatalf("replayed commit's parent = %s, want the local checkpoint %s", parent, compacted.checkpoint.Head)
	}
	if reconciled.State.Task.Title != "The teammate's title" {
		t.Fatalf("title = %q, want the teammate's edit", reconciled.State.Task.Title)
	}
	if got, want := operationIDs(reconciled), operationIDs(teammate); !slices.Equal(got, want) {
		t.Fatalf("replayed operation IDs = %v, want the teammate's %v", got, want)
	}
	assertNoParkedRefs(t, first, task.ID)

	assertSyncOutcome(t, result.Push, task.ID, SyncCompactionPublished)
	assertLeasedPush(t, *commands, compactedSyncTask{taskID: task.ID, tip: teammate, checkpoint: reconciled})
	if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != reconciled.Head {
		t.Fatalf("origin holds %s, want the reconciled tip %s", got, reconciled.Head)
	}
	if got := refValue(t, first, remoteTaskRefPrefix+task.ID); got != reconciled.Head {
		t.Fatalf("tracking ref = %s, want the reconciled tip %s", got, reconciled.Head)
	}
}

// Origin's change to the old history conflicts with an edit this clone made on
// its checkpoint. The conflict is reported from this clone's side, the ref
// stays where it is, and nothing is pushed: origin's tip is not accounted for,
// so the checkpoint may not be leased over it.
func TestFetchReportsConflictReplayingOriginOntoLocalCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Contested after compaction")
	setSyncTaskDescription(t, first, config, task.ID, "Base text")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	if result, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v; result = %#v", err, result)
	}
	compacted := compactLocally(t, first, config, task.ID)
	setSyncTaskDescription(t, first, config, task.ID, "Our text")
	local := refValue(t, first, taskRefPrefix+task.ID)
	setSyncTaskDescription(t, second, config, task.ID, "Their text")
	if result, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v; result = %#v", err, result)
	}
	theirs := refValue(t, second, taskRefPrefix+task.ID)
	commands := observeCommands(first)

	result, err := first.Sync(ctx, config)
	if got := core.CategoryOf(err); got != core.CategoryConflict {
		t.Fatalf("Sync() error category = %q, want %q; error = %v; result = %#v", got, core.CategoryConflict, err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncConflicted)
	if len(result.Fetch.Conflicts) != 1 || result.Fetch.Conflicts[0].Description == nil {
		t.Fatalf("conflicts = %#v, want one description conflict", result.Fetch.Conflicts)
	}
	want := core.DescriptionConflict{Base: "Base text", Ours: "Our text", Theirs: "Their text"}
	if got := *result.Fetch.Conflicts[0].Description; got != want {
		t.Fatalf("description conflict = %#v, want %#v", got, want)
	}
	if got := refValue(t, first, taskRefPrefix+task.ID); got != local {
		t.Fatalf("local ref = %s, want it left at %s", got, local)
	}
	if parent := syncGit(t, first.Root, "rev-parse", local+"^"); parent != compacted.checkpoint.Head {
		t.Fatalf("local edit's parent = %s, want the checkpoint %s", parent, compacted.checkpoint.Head)
	}
	if item, found := pushOutcome(result.Push, task.ID); found {
		t.Fatalf("push phase published the task anyway: %#v", item)
	}
	if got := countPushesTargeting(*commands, taskRefPrefix+task.ID); got != 0 {
		t.Fatalf("pushes naming the task ref = %d, want none; commands = %v", got, *commands)
	}
	if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != theirs {
		t.Fatalf("origin holds %s, want the teammate's tip %s", got, theirs)
	}
}

// Two clones compact the same tip. The second to synchronize takes the first's
// checkpoint, and its own is left unreferenced.
func TestTwoClonesCompactSameTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := sharedSyncTask(t, first, second, config, "Compacted twice")
	winner := compactLocally(t, first, config, task.ID)
	loser := compactLocally(t, second, config, task.ID)
	if winner.tip.Head != loser.tip.Head {
		t.Fatalf("clones compacted %s and %s, want the same tip", winner.tip.Head, loser.tip.Head)
	}
	publishCheckpoint(t, first, config, winner)

	result, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncAdopted)
	assertSyncOutcome(t, result.Push, task.ID, SyncUpToDate)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != winner.checkpoint.Head {
		t.Fatalf("second clone holds %s, want the first clone's checkpoint %s", got, winner.checkpoint.Head)
	}
	assertUnreferenced(t, second, task.ID, loser.checkpoint.Head)
	assertNoParkedRefs(t, second, task.ID)
}

// Two clones compact different tips of the same task. Whichever compacted the
// later tip, the one that synchronizes second ends on origin's checkpoint with
// every operation it held: summarized there, or replayed on top.
func TestTwoClonesCompactDifferentTips(t *testing.T) {
	t.Parallel()
	t.Run("origin compacted the later tip", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		first, second, config := syncRepositories(t)
		task := sharedSyncTask(t, first, second, config, "Compacted at two tips")
		updateSyncTask(t, first, config, task.ID, "Later title")
		if result, err := first.Push(ctx, config); err != nil {
			t.Fatalf("first Push() error = %v; result = %#v", err, result)
		}
		earlier := compactLocally(t, second, config, task.ID)
		later := compactLocally(t, first, config, task.ID)
		publishCheckpoint(t, first, config, later)

		result, err := second.Sync(ctx, config)
		if err != nil {
			t.Fatalf("Sync() error = %v; result = %#v", err, result)
		}
		assertSyncOutcome(t, result.Fetch, task.ID, SyncAdopted)
		if got := refValue(t, second, taskRefPrefix+task.ID); got != later.checkpoint.Head {
			t.Fatalf("second clone holds %s, want origin's checkpoint %s", got, later.checkpoint.Head)
		}
		assertUnreferenced(t, second, task.ID, earlier.checkpoint.Head)
		assertNoParkedRefs(t, second, task.ID)
	})
	t.Run("this clone compacted the later tip", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		first, second, config := syncRepositories(t)
		task := sharedSyncTask(t, first, second, config, "Compacted at two tips")
		updateSyncTask(t, second, config, task.ID, "Unpushed title")
		unpushed := currentSnapshot(t, second, config, task.ID)
		later := compactLocally(t, second, config, task.ID)
		earlier := compactLocally(t, first, config, task.ID)
		publishCheckpoint(t, first, config, earlier)

		result, err := second.Sync(ctx, config)
		if err != nil {
			t.Fatalf("Sync() error = %v; result = %#v", err, result)
		}
		assertSyncOutcome(t, result.Fetch, task.ID, SyncAdoptedReplayed)
		adopted := currentSnapshot(t, second, config, task.ID)
		if parent := syncGit(t, second.Root, "rev-parse", adopted.Head+"^"); parent != earlier.checkpoint.Head {
			t.Fatalf("replayed commit's parent = %s, want origin's checkpoint %s", parent, earlier.checkpoint.Head)
		}
		if adopted.State.Task.Title != "Unpushed title" {
			t.Fatalf("title = %q, want the change only this clone's checkpoint held", adopted.State.Task.Title)
		}
		if got, want := operationIDs(adopted), operationIDs(unpushed); !slices.Equal(got, want) {
			t.Fatalf("replayed operation IDs = %v, want the original %v", got, want)
		}
		assertUnreferenced(t, second, task.ID, later.checkpoint.Head)
		assertNoParkedRefs(t, second, task.ID)
		assertSyncOutcome(t, result.Push, task.ID, SyncPublished)
	})
}

// forgeUnrelatedRoot writes, in repo, a task.create root for created's task
// that shares no operation with the history origin held: its own operation ID
// and its own generation.
func forgeUnrelatedRoot(t *testing.T, repo *Repository, created core.Snapshot) string {
	t.Helper()
	pack := created.Operation
	pack.Operations = append([]core.Operation(nil), pack.Operations...)
	pack.Operations[0].ID = newCheckpointID(t)
	pack.HistoryGeneration = newCheckpointID(t)
	state := created.State
	state.History.Generation = pack.HistoryGeneration
	tree := syncGitInput(t, repo.Root, []byte(
		"100644 blob "+writeDocumentBlob(t, repo, pack)+"\toperation.json\n"+
			"100644 blob "+writeDocumentBlob(t, repo, state)+"\tstate.json\n",
	), "mktree")
	return syncGit(t, repo.Root, "commit-tree", tree, "-m", "unrelated history")
}

// A local checkpoint whose task origin now holds an unrelated history is left
// unpublished and reported, never pushed over it.
func TestUnrelatedRemoteHistoryIsNotPublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Rewritten elsewhere")
	created := currentSnapshot(t, first, config, task.ID)
	updateSyncTask(t, first, config, task.ID, "Rewritten elsewhere, revised")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v; result = %#v", err, result)
	}
	compacted := compactLocally(t, first, config, task.ID)
	unrelated := forgeUnrelatedRoot(t, second, created)
	syncGit(t, second.Root, "push", "--force", "origin", unrelated+":"+taskRefPrefix+task.ID)
	commands := observeCommands(first)

	result, err := first.Sync(ctx, config)
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("Sync() error category = %q, want %q; error = %v; result = %#v", got, core.CategoryCorruptData, err, result)
	}
	want := SyncTaskResult{
		TaskID: task.ID,
		Status: SyncInvalid,
		Detail: "origin has an unrelated history for this task; it was not published",
	}
	if item := syncOutcome(t, result.Fetch, task.ID); item != want {
		t.Fatalf("fetch outcome = %#v, want %#v", item, want)
	}
	if item, found := pushOutcome(result.Push, task.ID); found {
		t.Fatalf("push phase published the task anyway: %#v", item)
	}
	if got := countPushesTargeting(*commands, taskRefPrefix+task.ID); got != 0 {
		t.Fatalf("pushes naming the task ref = %d, want none; commands = %v", got, *commands)
	}
	if got := remoteRefValue(t, first, taskRefPrefix+task.ID); got != unrelated {
		t.Fatalf("origin holds %s, want the unrelated history %s left alone", got, unrelated)
	}
	if got := refValue(t, first, taskRefPrefix+task.ID); got != compacted.checkpoint.Head {
		t.Fatalf("local ref = %s, want the checkpoint %s", got, compacted.checkpoint.Head)
	}
}

// A checkpoint of a tip origin never fully held (compact --no-sync after an
// unpushed edit) is leased against the older tip origin does hold, which it
// accounts for.
func TestCheckpointLeasesAgainstOriginBehindCompactedFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Origin behind")
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	behind := currentSnapshot(t, first, config, task.ID)
	updateSyncTask(t, first, config, task.ID, "Unpushed before compaction")
	compacted := compactLocally(t, first, config, task.ID)
	commands := observeCommands(first)

	result, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, task.ID, SyncLocalAhead)
	assertSyncOutcome(t, result.Push, task.ID, SyncCompactionPublished)
	assertLeasedPush(t, *commands, compactedSyncTask{taskID: task.ID, tip: behind, checkpoint: compacted.checkpoint})
	assertPublishedCheckpoint(t, first, compacted)
}

// A clone that compacts again a task whose checkpoint origin already holds,
// after an edit it has not pushed, publishes the new checkpoint over the old
// one. Origin's tip begins at a checkpoint too, but it is the generation this
// clone's checkpoint replaced, so it is not one to adopt.
func TestRecompactionOverOriginsCheckpointPublishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	earlier := publishAndCompact(t, first, config)
	if result, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() of the first checkpoint error = %v; result = %#v", err, result)
	}
	assertPublishedCheckpoint(t, first, earlier)
	updateSyncTask(t, first, config, earlier.taskID, "Edited, then compacted again")
	later := compactLocally(t, first, config, earlier.taskID)
	commands := observeCommands(first)

	result, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result.Fetch, earlier.taskID, SyncLocalAhead)
	assertSyncOutcome(t, result.Push, earlier.taskID, SyncCompactionPublished)
	assertLeasedPush(t, *commands, compactedSyncTask{
		taskID: earlier.taskID, tip: earlier.checkpoint, checkpoint: later.checkpoint,
	})
	if got := remoteRefValue(t, first, taskRefPrefix+earlier.taskID); got != later.checkpoint.Head {
		t.Fatalf("origin holds %s, want the new checkpoint %s", got, later.checkpoint.Head)
	}
}

// When origin's tip is in the local generation but not on local history, a
// teammate extended the published checkpoint: an ordinary non-fast-forward,
// with no lease and nothing said about a stale compaction, whether or not this
// clone holds the teammate's commit.
func TestPushOverSameGenerationTipIsAnOrdinaryRejection(t *testing.T) {
	t.Parallel()
	for _, holds := range []bool{false, true} {
		name := "tip absent"
		if holds {
			name = "tip present"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, second, config := syncRepositories(t)
			task := sharedSyncTask(t, first, second, config, "Extended checkpoint")
			compacted := compactLocally(t, first, config, task.ID)
			publishCheckpoint(t, first, config, compacted)
			updateSyncTask(t, first, config, task.ID, "First clone's edit")
			if _, err := first.Push(ctx, config); err != nil {
				t.Fatalf("first Push() error = %v", err)
			}
			if _, err := second.Sync(ctx, config); err != nil {
				t.Fatalf("second Sync() error = %v", err)
			}
			updateSyncTask(t, second, config, task.ID, "Second clone's edit")
			if _, err := second.Push(ctx, config); err != nil {
				t.Fatalf("second Push() error = %v", err)
			}
			updateSyncTask(t, first, config, task.ID, "First clone's second edit")
			if holds {
				syncGit(t, first.Root, "fetch", "origin", taskRefPrefix+task.ID)
			}
			commands := observeCommands(first)

			result, err := first.Push(ctx, config)
			if got := core.CategoryOf(err); got != core.CategoryOperational {
				t.Fatalf("Push() error category = %q, want %q; error = %v", got, core.CategoryOperational, err)
			}
			item := syncOutcome(t, result, task.ID)
			if item.Status != SyncRejected || item.Detail == wantStaleDetail {
				t.Fatalf("Push() outcome = %#v, want an ordinary rejection", item)
			}
			assertNoLease(t, *commands)
		})
	}
}

// advanceTrackingRef moves the tracking ref only from the tip the lease was
// taken against, or creates it; a ref a concurrent fetch moved is left alone.
func TestAdvanceTrackingRefIsACompareAndSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Tracked")
	one := refValue(t, first, taskRefPrefix+task.ID)
	updateSyncTask(t, first, config, task.ID, "Tracked, two")
	two := refValue(t, first, taskRefPrefix+task.ID)
	updateSyncTask(t, first, config, task.ID, "Tracked, three")
	three := refValue(t, first, taskRefPrefix+task.ID)
	ref := remoteTaskRefPrefix + task.ID

	if err := first.advanceTrackingRef(ctx, task.ID, one, two); err != nil {
		t.Fatalf("advanceTrackingRef() create error = %v", err)
	}
	if got := refValue(t, first, ref); got != two {
		t.Fatalf("created tracking ref = %s, want %s", got, two)
	}
	if err := first.advanceTrackingRef(ctx, task.ID, one, three); err != nil {
		t.Fatalf("advanceTrackingRef() from a moved ref error = %v", err)
	}
	if got := refValue(t, first, ref); got != two {
		t.Fatalf("tracking ref = %s, want it left at %s, which the lease did not expect", got, two)
	}
	if err := first.advanceTrackingRef(ctx, task.ID, two, three); err != nil {
		t.Fatalf("advanceTrackingRef() error = %v", err)
	}
	if got := refValue(t, first, ref); got != three {
		t.Fatalf("tracking ref = %s, want %s", got, three)
	}
}
