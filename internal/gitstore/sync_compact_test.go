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
)

const (
	wantDeniedDetail = "origin refuses non-fast-forward updates (receive.denyNonFastForwards is set); " +
		"the compacted task stays local until that setting is changed"
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
	history := strings.Fields(syncGit(t, repo.Root, "rev-list", tip.Head))

	checkpoint := buildCheckpoint(t, repo, config, checkpointInputFor(t, repo, config, tip, false))
	if err := repo.CompactTaskRef(ctx, config, task.ID, tip.Head, checkpoint.Head); err != nil {
		t.Fatalf("CompactTaskRef() error = %v", err)
	}
	return compactedSyncTask{taskID: task.ID, tip: tip, checkpoint: checkpoint, history: history}
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
func TestLeasedPushRejectedByDenyNonFastForwards(t *testing.T) {
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

// A teammate's push after the compaction moves origin off the compacted tip, so
// the lease fails and origin keeps the teammate's work.
func TestLeasedPushStaleLease(t *testing.T) {
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

	result, err := first.Push(context.Background(), config)
	if got := core.CategoryOf(err); got != core.CategoryOperational {
		t.Fatalf("Push() error category = %q, want %q; error = %v; result = %#v", got, core.CategoryOperational, err, result)
	}
	item := syncOutcome(t, result, compacted.taskID)
	if want := (SyncTaskResult{TaskID: compacted.taskID, Status: SyncRejected, Detail: wantStaleDetail}); item != want {
		t.Fatalf("Push() outcome = %#v, want %#v", item, want)
	}
	assertUnpublishedCheckpoint(t, first, compacted, moved)
	assertLeasedPush(t, *commands, compacted)
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
