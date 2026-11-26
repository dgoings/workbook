package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// holdTaskPushes makes the bare repository hold the first push that updates a
// task ref, after its client has looked at the identity ref and before the
// ref moves, until release is called. That is the window plain Git cannot
// close: the move can mark the repository while the push waits, and the
// push still lands, because nothing it updates changed.
func holdTaskPushes(t *testing.T, bare string) (held <-chan struct{}, release func()) {
	t.Helper()
	return holdPushesTo(t, bare, "refs/workbook/tasks/*")
}

// holdPushesTo is holdTaskPushes for the first push that updates a ref
// matching pattern, a shell case pattern.
func holdPushesTo(t *testing.T, bare, pattern string) (held <-chan struct{}, release func()) {
	t.Helper()
	signals := t.TempDir()
	holding := filepath.ToSlash(filepath.Join(signals, "holding"))
	released := filepath.ToSlash(filepath.Join(signals, "released"))
	hook := "#!/bin/sh\nheld=no\nwhile read old new ref; do\n" +
		"  case \"$ref\" in " + pattern + ") held=yes ;; esac\ndone\n" +
		"if [ \"$held\" = yes ] && [ ! -f '" + released + "' ]; then\n" +
		"  : > '" + holding + "'\n" +
		"  while [ ! -f '" + released + "' ]; do sleep 1; done\nfi\nexit 0\n"
	testenv.WriteExecutable(t, filepath.Join(bare, "hooks", "pre-receive"), []byte(hook))
	signal := make(chan struct{})
	go func() {
		defer close(signal)
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.FromSlash(holding)); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return signal, func() {
		if err := testenv.WriteFile(filepath.FromSlash(released), nil, 0o644); err != nil {
			t.Error(err)
		}
	}
}

// An edit whose publication to the old place was under way when the move
// marked it lands there, and the command that made it is told: it exits with
// the published-after-move refusal, naming the task, and keeps the edit. The
// next move-tasks carries the edit once, and the new place validates.
func TestTasksRemoteEditPublishedAroundTheMarkIsReportedAndCarried(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	edited := project.ids[0]
	held, release := holdTaskPushes(t, project.code)

	type outcome struct {
		code           int
		stdout, stderr string
	}
	finished := make(chan outcome, 1)
	go func() {
		code, stdout, stderr := run(t, teammate, "update", edited, "--title", "Edited as the tasks moved", "--json")
		finished <- outcome{code, stdout, stderr}
	}()
	select {
	case <-held:
	case result := <-finished:
		release()
		t.Fatalf("the teammate's update finished before its push was held: %+v", result)
	}
	// The whole move runs while the teammate's push waits.
	project.moveTasks(t)
	release()
	var result outcome
	select {
	case result = <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("the teammate's update never finished")
	}

	if result.code != 3 {
		t.Fatalf("teammate update = (%d, %q, %q), want 3", result.code, result.stdout, result.stderr)
	}
	refusal := requireTasksRemoteRefusal(t, result.stderr, core.CategoryNotInitialized, core.TasksRemotePublishedAfterMove)
	if refusal.Remote != "origin" || refusal.MovedTo == nil || refusal.MovedTo.URL != project.tasks ||
		len(refusal.Tasks) != 1 || refusal.Tasks[0] != edited || refusal.Command != "workbook setup" {
		t.Fatalf("refusal = %+v", refusal)
	}
	message := errorDocumentOf(t, result.stderr).Error.Message
	for _, want := range []string{"kept in this checkout", "workbook move-tasks", edited} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal message %q lacks %q", message, want)
		}
	}
	// The edit reached the old place, and the teammate still holds it.
	teammateHead := localTaskRef(t, teammate, edited)
	if got := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/workbook/tasks/"+edited)); got != teammateHead {
		t.Fatalf("origin holds %s at %s, want the teammate's %s", edited, got, teammateHead)
	}

	moved := moveTasksAgain(t, project, 0)
	if moved.Stragglers == nil || moved.Stragglers.Operations != 1 || strings.Join(moved.Stragglers.Tasks, ",") != edited {
		t.Fatalf("move-tasks after the race = %+v, want the one edit carried", moved.Stragglers)
	}
	if shown := cliShowTask(t, project.seed, edited); shown.Title != "Edited as the tasks moved" {
		t.Fatalf("task after the carry = %+v", shown)
	}
	requireFreshCloneValidates(t, project.tasks)

	// Once the teammate pulls the settings and sets up, their checkout and the
	// new place agree, and nothing is carried twice.
	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup after the pull = (%d, %q, %q)", code, stdout, stderr)
	}
	requireFreshCloneValidates(t, project.tasks)
}

// A create in a checkout on the old settings with a watcher running is
// refused by the command itself, not only in the watcher's log, once the
// watcher's round after the move has brought the marker into this checkout's
// copy of origin's identity ref: within one interval, and without the command
// asking origin anything. Handing the change to that watcher would have
// exited 0.
func TestTasksRemoteStaleCreateHandedToAWatcherIsRefusedByTheCommand(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	stale := cloneCode(t, project)
	if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup = (%d, %q, %q)", code, stdout, stderr)
	}
	output := &watcherOutput{}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	go func() {
		finished <- Run(ctx, []string{"sync", "--watch", "--interval", "1s"}, stale, nil, output, output)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(20 * time.Second):
			t.Error("watcher did not stop")
		}
	}()
	waitForWatcherReady(t, output)

	project.moveTasks(t)
	afterMove := repositoryState(t, project.code)
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(output.String(), "tasks moved from origin to "+project.tasks) {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never reported the move; it wrote:\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if count := strings.Count(output.String(), "tasks moved from origin to "+project.tasks); count != 1 {
		t.Fatalf("the watcher logged the move %d times, want once:\n%s", count, output.String())
	}
	localBefore := refsUnder(t, stale, "refs/workbook/tasks/")
	code, stdout, stderr := run(t, stale, "create", "Handed to a watcher that predates the move", "--json")
	if code != 3 {
		t.Fatalf("stale create with a watcher = (%d, %q, %q), want 3", code, stdout, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.MovedTo == nil || refusal.MovedTo.URL != project.tasks {
		t.Fatalf("refusal = %+v", refusal)
	}
	if got := refsUnder(t, stale, "refs/workbook/tasks/"); got != localBefore {
		t.Fatalf("the refused create recorded something:\nbefore %s\nafter  %s", localBefore, got)
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("origin changed:\nbefore %s\nafter  %s", afterMove, after)
	}
}

// A push the remote accepted is a success even when listing the remote again
// afterwards fails, but the command says it could not confirm the remote is
// still where the tasks live, with Git's reason, in JSON and in text. The
// remote's hook takes it out of reach as soon as it has accepted a task ref.
func TestTasksRemotePublicationThatCannotBeCheckedAfterwardsSaysSo(t *testing.T) {
	t.Parallel()
	_, clone := cliSyncRepositories(t)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	bare := strings.TrimSpace(cliGitOutput(t, clone, "remote", "get-url", "origin"))
	gone := bare + ".gone"
	hook := "#!/bin/sh\nmoved=no\nwhile read old new ref; do\n" +
		"  case \"$ref\" in refs/workbook/tasks/*) moved=yes ;; esac\ndone\n" +
		"if [ \"$moved\" = yes ]; then mv '" + filepath.ToSlash(bare) + "' '" + filepath.ToSlash(gone) + "'; fi\nexit 0\n"
	testenv.WriteExecutable(t, filepath.Join(bare, "hooks", "post-receive"), []byte(hook))

	for _, jsonMode := range []bool{true, false} {
		args := []string{"create", "Accepted, then out of reach"}
		if jsonMode {
			args = append(args, "--json")
		}
		code, stdout, stderr := run(t, clone, args...)
		if code != 0 {
			t.Fatalf("%v = (%d, %q, %q), want success: the push succeeded", args, code, stdout, stderr)
		}
		want := "could not confirm that origin is still where this project's tasks live"
		if jsonMode {
			warnings := assertJSONResult(t, stdout, "create").Warnings
			if len(warnings) != 1 || warnings[0].Code != core.WarningPublicationUnconfirmed ||
				!strings.Contains(warnings[0].Message, want) || !strings.Contains(warnings[0].Message, "origin.git") {
				t.Fatalf("warnings = %+v, want the unconfirmed publication with Git's reason", warnings)
			}
		} else if !strings.Contains(stderr, want) {
			t.Fatalf("text stderr = %q, want %q", stderr, want)
		}
		if refs := cliGitOutput(t, gone, "for-each-ref", "refs/workbook/tasks/"); strings.TrimSpace(refs) == "" {
			t.Fatalf("the remote holds no task refs after the push: %q", refs)
		}
		if err := os.Rename(gone, bare); err != nil {
			t.Fatal(err)
		}
	}
}
