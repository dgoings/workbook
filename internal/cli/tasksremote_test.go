package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// tasksRemoteProject is a project whose code is on one bare repository and
// whose tasks have moved to another.
type tasksRemoteProject struct {
	// code is origin: the code, and the old copy of the tasks, now marked.
	code string
	// tasks is the bare repository the tasks moved to.
	tasks string
	// seed is the clone that chose the new remote and moved the tasks.
	seed string
	ids  []string
}

func newBareRepository(t *testing.T, objectFormat string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "repository.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", "--object-format="+objectFormat, bare)
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	return bare
}

// codeProject publishes a project with code on main and two tasks to a bare
// origin, the way a project that has always used origin for its tasks looks.
func codeProject(t *testing.T, objectFormat string) tasksRemoteProject {
	t.Helper()
	project := tasksRemoteProject{code: newBareRepository(t, objectFormat)}
	project.seed = testrepo.New(t, testrepo.WithObjectFormat(objectFormat))
	cliGit(t, project.seed, "branch", "-M", "main")
	if code, _, stderr := run(t, project.seed, "setup"); code != 0 {
		t.Fatalf("seed setup = %d; stderr = %q", code, stderr)
	}
	writeCodeFile(t, project.seed, "main.go", "package main\n\nfunc main() {}\n")
	cliGit(t, project.seed, "add", ".workbook/config.json", "main.go")
	cliGit(t, project.seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, project.seed, "remote", "add", "origin", project.code)
	cliGit(t, project.seed, "push", "--quiet", "-u", "origin", "main")
	cliGit(t, project.code, "symbolic-ref", "HEAD", "refs/heads/main")
	for _, title := range []string{"Shared before the move", "Also shared before the move"} {
		project.ids = append(project.ids, cliCreateTask(t, project.seed, title).ID)
	}
	if code, _, stderr := run(t, project.seed, "push"); code != 0 {
		t.Fatalf("seed push = %d; stderr = %q", code, stderr)
	}
	project.tasks = newBareRepository(t, objectFormat)
	return project
}

// moveTasks chooses the tasks repository, moves the tasks there, and pushes
// the committed settings, which is the documented procedure.
func (project *tasksRemoteProject) moveTasks(t *testing.T) {
	t.Helper()
	code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks, "--json")
	if code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	var written configWriteResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "config set").Data, &written); err != nil {
		t.Fatal(err)
	}
	if written.Setting != "tasks-remote" || written.Value != "tasks" || written.URL != project.tasks {
		t.Fatalf("config set result = %+v", written)
	}
	code, stdout, stderr = run(t, project.seed, "move-tasks", "--json")
	if code != 0 {
		t.Fatalf("move-tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved struct {
		From      core.TasksRemoteTarget `json:"from"`
		To        core.TasksRemoteTarget `json:"to"`
		Remote    string                 `json:"remote"`
		Marked    string                 `json:"marked"`
		Tasks     int                    `json:"tasks"`
		Identical int                    `json:"identical"`
		Carried   int                    `json:"carried"`
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if moved.From.Remote != "origin" || moved.To.Remote != "tasks" || moved.To.URL != project.tasks ||
		moved.Remote != "added" || moved.Marked != "marked" || moved.Tasks != len(project.ids) ||
		moved.Identical != len(project.ids) || moved.Carried != 0 {
		t.Fatalf("move-tasks result = %+v", moved)
	}
	cliGit(t, project.seed, "add", ".workbook/config.json")
	cliGit(t, project.seed, "commit", "--quiet", "-m", "Move the tasks to their own repository")
	cliGit(t, project.seed, "push", "--quiet", "origin", "main")
}

func movedProject(t *testing.T, objectFormat string) tasksRemoteProject {
	t.Helper()
	project := codeProject(t, objectFormat)
	project.moveTasks(t)
	return project
}

// repositoryState is every ref and every object a bare repository holds.
func repositoryState(t *testing.T, bare string) string {
	t.Helper()
	refs := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")
	objects := cliGitOutput(t, bare, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	return refs + "\n--- objects ---\n" + objects
}

func refsUnder(t *testing.T, repository, prefix string) string {
	t.Helper()
	return cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
}

func cloneCode(t *testing.T, project tasksRemoteProject) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	cliGit(t, t.TempDir(), "clone", "--quiet", project.code, clone)
	cliGit(t, clone, "config", "user.name", "Teammate")
	cliGit(t, clone, "config", "user.email", "teammate@example.test")
	return clone
}

// errorDocumentOf decodes a JSON error envelope with its tasksRemote member.
func errorDocumentOf(t *testing.T, output string) ErrorEnvelope {
	t.Helper()
	var document ErrorEnvelope
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("decode error envelope: %v; output = %q", err, output)
	}
	if document.Format != "workbook.error" || document.Version != 1 {
		t.Fatalf("error envelope = %+v", document)
	}
	return document
}

func requireTasksRemoteRefusal(t *testing.T, stderr string, category core.Category, reason string) core.TasksRemoteRefusal {
	t.Helper()
	document := errorDocumentOf(t, stderr)
	if document.Error.Category != category || document.Error.TasksRemote == nil || document.Error.TasksRemote.Reason != reason {
		t.Fatalf("error = %+v (tasksRemote %+v), want %s with reason %s", document.Error, document.Error.TasksRemote, category, reason)
	}
	return *document.Error.TasksRemote
}

// The move leaves the new place holding every task ref the old place holds,
// commit for commit, and the old place marked. A fresh clone of the code then
// reaches the tasks with the one bootstrap command, and every way a checkout
// publishes — a mutation's own push, next --claim, and a sync watcher — goes
// to the tasks repository and adds no ref and no object to origin.
func TestTasksRemoteFreshCloneReachesTheMovedTasksAndNeverTouchesOrigin(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			testrepo.RequireObjectFormat(t, objectFormat)
			project := movedProject(t, objectFormat)
			if source, destination := refsUnder(t, project.code, "refs/workbook/tasks/"), refsUnder(t, project.tasks, "refs/workbook/tasks/"); source != destination || source == "" {
				t.Fatalf("task refs differ after the move:\norigin: %s\ntasks:  %s", source, destination)
			}
			if source, destination := refsUnder(t, project.code, "refs/workbook/config"), refsUnder(t, project.tasks, "refs/workbook/config"); source != destination {
				t.Fatalf("configuration ledgers differ:\norigin: %s\ntasks:  %s", source, destination)
			}
			before := repositoryState(t, project.code)

			clone := cloneCode(t, project)
			code, stdout, stderr := run(t, clone, "setup", "--json")
			if code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}
			var setup setupResult
			if err := json.Unmarshal(assertJSONResult(t, stdout, "setup").Data, &setup); err != nil {
				t.Fatal(err)
			}
			if setup.TasksRemote == nil || setup.TasksRemote.Name != "tasks" || setup.TasksRemote.Status != "added" ||
				setup.TasksRemote.URL != project.tasks || setup.TaskCount != len(project.ids) {
				t.Fatalf("setup result = %+v (tasksRemote %+v)", setup, setup.TasksRemote)
			}
			if got := cliGitOutput(t, clone, "remote", "get-url", "tasks"); got != project.tasks {
				t.Fatalf("tasks remote = %q, want %q", got, project.tasks)
			}
			if got := cliGitOutput(t, clone, "config", "--get-all", "remote.tasks.fetch"); got != "^refs/*" {
				t.Fatalf("tasks remote fetches %q, want nothing on a plain git fetch", got)
			}
			if got := listedTaskIDs(t, clone); !sameIDs(got, project.ids) {
				t.Fatalf("list = %v, want %v", got, project.ids)
			}

			// A mutation publishes its own ref.
			code, stdout, stderr = run(t, clone, "create", "Filed after the move", "--json")
			if code != 0 {
				t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
			}
			created := decodeMutationTask(t, stdout, "create")
			if got := cliGitOutput(t, project.tasks, "rev-parse", "refs/workbook/tasks/"+created.ID); got != created.Head {
				t.Fatalf("tasks holds %s at %q, want %s", created.ID, got, created.Head)
			}
			// next --claim publishes the claim.
			if code, _, stderr := run(t, clone, "update", created.ID, "--status", "ready"); code != 0 {
				t.Fatalf("make ready = %d; stderr = %q", code, stderr)
			}
			code, stdout, stderr = run(t, clone, "next", "--claim", "--json")
			if code != 0 {
				t.Fatalf("next --claim = (%d, %q, %q)", code, stdout, stderr)
			}
			claimed := decodeMutationTask(t, stdout, "next")
			if got := cliGitOutput(t, project.tasks, "rev-parse", "refs/workbook/tasks/"+claimed.ID); got != claimed.Head {
				t.Fatalf("tasks holds the claimed %s at %q, want %s", claimed.ID, got, claimed.Head)
			}
			// A sync watcher publishes what the commands hand it.
			output := &watcherOutput{}
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan int, 1)
			go func() {
				finished <- Run(ctx, []string{"sync", "--watch", "--interval", "1h"}, clone, nil, output, output)
			}()
			waitForWatcherReady(t, output)
			watched := cliCreateTask(t, clone, "Handed to the watcher")
			cancel()
			select {
			case code := <-finished:
				if code != 0 {
					t.Fatalf("watcher exit = %d; wrote %q", code, output.String())
				}
			case <-time.After(30 * time.Second):
				t.Fatal("watcher did not stop")
			}
			if got := cliGitOutput(t, project.tasks, "rev-parse", "refs/workbook/tasks/"+watched.ID); got != localTaskRef(t, clone, watched.ID) {
				t.Fatalf("watcher left %s unpublished at the tasks repository", watched.ID)
			}
			if code, stdout, stderr := run(t, clone, "sync", "--json"); code != 0 {
				t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
			} else {
				var synced struct {
					Remote string `json:"remote"`
				}
				if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &synced); err != nil || synced.Remote != "tasks" {
					t.Fatalf("sync remote = %q (%v), want tasks", synced.Remote, err)
				}
			}

			if after := repositoryState(t, project.code); after != before {
				t.Fatalf("origin changed after the move:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// A board served from a tasks-only folder of the tasks repository and a
// clone of the code see each other's edits, in both directions, and the folder
// never holds the code.
func TestTasksRemoteBoardInATasksOnlyFolderAndACodeCloneShareTasksBothWays(t *testing.T) {
	t.Parallel()
	project := movedProject(t, testrepo.FormatSHA1)
	before := repositoryState(t, project.code)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("clone setup = (%d, %q, %q)", code, stdout, stderr)
	}

	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.tasks); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")
	addr := startServeBoard(t, folder)

	// Board to code clone.
	body, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks", `{"title":"Filed on the board"}`)
	fromBoard := decodeServeMutation(t, body, status)
	deadline := time.Now().Add(30 * time.Second)
	for cliGitOutputAllowingMissing(t, project.tasks, "refs/workbook/tasks/"+fromBoard.ID) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("the tasks repository never received %s from the board", fromBoard.ID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _, stderr := run(t, clone, "fetch"); code != 0 {
		t.Fatalf("clone fetch = %d; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, clone); !containsID(got, fromBoard.ID) {
		t.Fatalf("clone list = %v, want the board's %s", got, fromBoard.ID)
	}

	// Code clone to board.
	code, stdout, stderr := run(t, clone, "create", "Filed in the code clone", "--json")
	if code != 0 {
		t.Fatalf("clone create = (%d, %q, %q)", code, stdout, stderr)
	}
	fromClone := decodeMutationTask(t, stdout, "create")
	if code, _, stderr := run(t, folder, "fetch"); code != 0 {
		t.Fatalf("folder fetch = %d; stderr = %q", code, stderr)
	}
	body, status = boardRequest(t, http.MethodGet, "http://"+addr+"/api/tasks", "")
	if status != http.StatusOK || !strings.Contains(string(body), fromClone.ID) {
		t.Fatalf("GET /api/tasks = %d, want the clone's %s; body = %s", status, fromClone.ID, body)
	}

	assertHoldsNoCode(t, folder, project.code)
	if after := repositoryState(t, project.code); after != before {
		t.Fatalf("origin changed:\nbefore %s\nafter  %s", before, after)
	}
}

func cliGitOutputAllowingMissing(t *testing.T, repository, ref string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", repository, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// A teammate set up before the move, holding an edit it never published, is
// refused by every task command with a message saying where the tasks went
// and what to run, records nothing more, and after pulling the settings and
// running setup publishes the kept edit to the new place. Between the pull
// and the setup, task commands say to run setup.
func TestTasksRemoteStaleCheckoutIsRefusedUntilItPullsAndSetsUp(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	unpublished := cliCreateTask(t, teammate, "Recorded before the move and never published")

	project.moveTasks(t)
	afterMove := repositoryState(t, project.code)
	localBefore := refsUnder(t, teammate, "refs/workbook/tasks/")

	code, _, stderr := run(t, teammate, "create", "Would go to the old place", "--json")
	if code != 3 {
		t.Fatalf("stale create exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.Remote != "origin" || refusal.MovedTo == nil || refusal.MovedTo.Remote != "tasks" ||
		refusal.MovedTo.URL != project.tasks || refusal.Command != "workbook setup" {
		t.Fatalf("refusal = %+v", refusal)
	}
	message := errorDocumentOf(t, stderr).Error.Message
	for _, want := range []string{project.tasks, "pull the commit that sets tasks-remote", "workbook setup", "kept"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal message %q lacks %q", message, want)
		}
	}
	if got := refsUnder(t, teammate, "refs/workbook/tasks/"); got != localBefore {
		t.Fatalf("the refused create recorded something:\nbefore %s\nafter  %s", localBefore, got)
	}
	for _, args := range [][]string{{"list", "--json"}, {"push", "--json"}, {"sync", "--json"}, {"setup", "--json"}} {
		code, _, stderr := run(t, teammate, args...)
		if code != 3 {
			t.Fatalf("stale %v exit = %d, want 3; stderr = %q", args, code, stderr)
		}
		requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	}
	// Human output says the same sentence.
	if code, _, stderr := run(t, teammate, "list"); code != 3 || !strings.Contains(stderr, "workbook: this project's tasks moved from origin to "+project.tasks) {
		t.Fatalf("stale list = (%d, %q)", code, stderr)
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("origin changed after the mark:\nbefore %s\nafter  %s", afterMove, after)
	}

	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	code, _, stderr = run(t, teammate, "list", "--json")
	if code != 3 {
		t.Fatalf("list after the pull exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal = requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMissing)
	if refusal.Remote != "tasks" || refusal.URL != project.tasks || refusal.Command != "workbook setup" {
		t.Fatalf("refusal after the pull = %+v", refusal)
	}

	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("setup after the pull = (%d, %q, %q)", code, stdout, stderr)
	} else if !strings.Contains(stdout, "Tasks remote:\ttasks\t"+project.tasks+"\t(added from .workbook/config.json)") {
		t.Fatalf("setup output lacks the tasks remote line:\n%s", stdout)
	}
	if got, want := cliGitOutput(t, project.tasks, "rev-parse", "refs/workbook/tasks/"+unpublished.ID), localTaskRef(t, teammate, unpublished.ID); got != want {
		t.Fatalf("tasks holds the kept edit at %q, want %q", got, want)
	}
	if got := listedTaskIDs(t, teammate); !sameIDs(got, append(append([]string(nil), project.ids...), unpublished.ID)) {
		t.Fatalf("list after setup = %v", got)
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("origin changed after the teammate's setup:\nbefore %s\nafter  %s", afterMove, after)
	}
}

// Settings that name a tasks repository the person who chose it never moved
// the tasks to stop every new checkout's setup with the command that finishes
// the move, rather than letting it publish to one place while teammates
// publish to the other.
func TestTasksRemoteSetupRefusesAMoveThatHasNotFinished(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set = %d; stderr = %q", code, stderr)
	}
	cliGit(t, project.seed, "add", ".workbook/config.json")
	cliGit(t, project.seed, "commit", "--quiet", "-m", "Choose a tasks repository")
	cliGit(t, project.seed, "push", "--quiet", "origin", "main")

	clone := cloneCode(t, project)
	code, _, stderr := run(t, clone, "setup", "--json")
	if code != 3 {
		t.Fatalf("setup exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteNotMoved)
	if refusal.Command != "workbook move-tasks" || refusal.Remote != "tasks" || refusal.URL != project.tasks {
		t.Fatalf("refusal = %+v", refusal)
	}
	if got := refsUnder(t, project.tasks, "refs/"); got != "" {
		t.Fatalf("the refused setup published to the tasks repository: %s", got)
	}

	// Finishing the move lets the same checkout set up.
	if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 {
		t.Fatalf("move-tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup after the move = (%d, %q, %q)", code, stdout, stderr)
	}
}

// A contributor who can reach the code but not the tasks repository is told
// so in a plain sentence by setup, which fails, and by every task command
// after it.
func TestTasksRemoteWithoutAccessSaysWhichRepositoryAndWhatAccess(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		testenv.MissingCapability(t, "file permissions do not restrict root, so a repository cannot be made unreadable")
	}
	project := movedProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if err := os.Chmod(project.tasks, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(project.tasks, 0o755) })

	code, _, stderr := run(t, clone, "setup", "--json")
	if code != 1 {
		t.Fatalf("setup exit = %d, want 1; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryOperational, core.TasksRemoteUnreachable)
	if refusal.Remote != "tasks" || refusal.URL != project.tasks || refusal.Command != "workbook setup" {
		t.Fatalf("refusal = %+v", refusal)
	}
	message := errorDocumentOf(t, stderr).Error.Message
	for _, want := range []string{project.tasks, "needs read access to that repository", "push access"} {
		if !strings.Contains(message, want) {
			t.Errorf("setup message %q lacks %q", message, want)
		}
	}

	code, _, stderr = run(t, clone, "list", "--json")
	if code != 3 {
		t.Fatalf("list exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal = requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteNotRead)
	if refusal.URL != project.tasks {
		t.Fatalf("list refusal = %+v", refusal)
	}
	if listMessage := errorDocumentOf(t, stderr).Error.Message; !strings.Contains(listMessage, project.tasks) ||
		!strings.Contains(listMessage, "needs read access to that repository") {
		t.Fatalf("list message = %q", listMessage)
	}

	if err := os.Chmod(project.tasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup once access is granted = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := listedTaskIDs(t, clone); !sameIDs(got, project.ids) {
		t.Fatalf("list = %v", got)
	}
}

// Access to the tasks repository alone is a complete board: a tasks-only
// folder sets up and serves from it with the code repository unreadable, and
// the tasks repository holds not one object of the code, so whatever opens it
// opens nothing of the code.
func TestTasksRemoteAccessToTheTasksRepositoryOpensNoCode(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		testenv.MissingCapability(t, "file permissions do not restrict root, so a repository cannot be made unreadable")
	}
	project := movedProject(t, testrepo.FormatSHA1)
	code := codeObjects(t, project.code)
	for _, object := range code {
		if err := exec.Command("git", "-C", project.tasks, "cat-file", "-e", object).Run(); err == nil {
			t.Errorf("the tasks repository holds code object %s", object)
		}
	}
	if err := os.Chmod(project.code, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(project.code, 0o755) })

	folder := t.TempDir()
	if exit, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.tasks); exit != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", exit, stdout, stderr)
	}
	if got := listedTaskIDs(t, folder); !sameIDs(got, project.ids) {
		t.Fatalf("list = %v", got)
	}
	if err := exec.Command("git", "-C", folder, "ls-remote", project.code).Run(); err == nil {
		t.Fatal("the folder could read the code repository; the fixture proves nothing")
	}
	if err := os.Chmod(project.code, 0o755); err != nil {
		t.Fatal(err)
	}
	assertHoldsNoCode(t, folder, project.code)
}

// The setting is validated before it is written, with documented JSON
// errors, and `config show` reports it only once it is set.
func TestTasksRemoteSettingIsValidated(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	original, err := os.ReadFile(filepath.Join(project.seed, ".workbook", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		args     []string
		category core.Category
		message  string
	}{
		{"invalid name", []string{"config", "set", "tasks-remote", "../tasks", "--url", project.tasks}, core.CategoryValidation, "is not a Git remote name"},
		{"origin with an address", []string{"config", "set", "tasks-remote", "origin", "--url", project.tasks}, core.CategoryValidation, "origin is the code's own remote"},
		{"no address", []string{"config", "set", "tasks-remote", "tasks"}, core.CategoryValidation, "needs --url"},
		{"password", []string{"config", "set", "tasks-remote", "tasks", "--url", "https://someone:secret@example.test/team/tasks.git"}, core.CategoryValidation, "carries a password"},
		{"https user", []string{"config", "set", "tasks-remote", "tasks", "--url", "https://ghp_token@example.test/team/tasks.git"}, core.CategoryValidation, "carries a user name"},
		{"origin's own address", []string{"config", "set", "tasks-remote", "tasks", "--url", project.code}, core.CategoryValidation, "is origin's address"},
		{"option-like address", []string{"config", "set", "tasks-remote", "tasks", "--url", "--upload-pack=x"}, core.CategoryValidation, "begins with '-'"},
		{"url for another setting", []string{"config", "set", "auto-sync", "true", "--url", project.tasks}, core.CategoryInvocation, "--url is for tasks-remote only"},
	}
	for _, test := range cases {
		code, _, stderr := run(t, project.seed, append(test.args, "--json")...)
		if code == 0 {
			t.Fatalf("%s: config set succeeded", test.name)
		}
		document := errorDocumentOf(t, stderr)
		if document.Error.Category != test.category || !strings.Contains(document.Error.Message, test.message) {
			t.Errorf("%s: error = %+v, want %s containing %q", test.name, document.Error, test.category, test.message)
		}
		if document.Error.TasksRemote != nil {
			t.Errorf("%s: a validation error carries tasksRemote %+v", test.name, document.Error.TasksRemote)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(project.seed, ".workbook", "config.json")); string(got) != string(original) {
		t.Fatalf("a refused setting changed the file:\n%s", got)
	}

	// An SSH address keeps its user name, which is not a credential.
	ssh := "git@example.test:team/tasks.git"
	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", ssh); code != 0 {
		t.Fatalf("config set with an SSH address = %d; stderr = %q", code, stderr)
	}
	contents, err := os.ReadFile(filepath.Join(project.seed, ".workbook", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"tasksRemote":{"name":"tasks","url":"`+ssh+`"}`) {
		t.Fatalf("config.json = %s", contents)
	}
	code, stdout, stderr := run(t, project.seed, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show = (%d, %q)", code, stderr)
	}
	var shown configShowResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "config show").Data, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.TasksRemote == nil || shown.TasksRemote.Name != "tasks" || shown.TasksRemote.URL != ssh || shown.TasksRemote.CheckoutURL != "" {
		t.Fatalf("config show tasksRemote = %+v", shown.TasksRemote)
	}

	// Both ways of going back to origin restore the file byte for byte.
	for _, args := range [][]string{{"config", "unset", "tasks-remote"}, {"config", "set", "tasks-remote", "origin"}} {
		if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", ssh); code != 0 {
			t.Fatalf("config set = %d; stderr = %q", code, stderr)
		}
		if code, _, stderr := run(t, project.seed, args...); code != 0 {
			t.Fatalf("%v = %d; stderr = %q", args, code, stderr)
		}
		if got, _ := os.ReadFile(filepath.Join(project.seed, ".workbook", "config.json")); string(got) != string(original) {
			t.Fatalf("%v left the file as:\n%s\nwant:\n%s", args, got, original)
		}
	}
	code, stdout, _ = run(t, project.seed, "config", "show", "--json")
	if code != 0 || strings.Contains(stdout, "tasksRemote") {
		t.Fatalf("config show after unset = (%d, %s), want no tasksRemote member", code, stdout)
	}
}

// fetch, push and sync name another remote with --remote; nothing else does.
// A remote marked as moved may be read that way and is never published to.
func TestTasksRemoteFlagReadsAMovedRemoteAndRefusesToPublishThere(t *testing.T) {
	t.Parallel()
	project := movedProject(t, testrepo.FormatSHA1)
	before := repositoryState(t, project.code)

	if code, _, stderr := run(t, project.seed, "fetch", "--remote", "nosuch", "--json"); code != 4 {
		t.Fatalf("fetch --remote nosuch exit = %d, want 4; stderr = %q", code, stderr)
	}
	// Bounded, so a watcher that starts instead of being refused fails the
	// test rather than running until the package's deadline.
	watchCtx, cancelWatch := context.WithTimeout(context.Background(), 10*time.Second)
	var watchOut, watchErr bytes.Buffer
	code := Run(watchCtx, []string{"sync", "--watch", "--remote", "origin"}, project.seed, strings.NewReader(""), &watchOut, &watchErr)
	cancelWatch()
	if code != 2 {
		t.Fatalf("sync --watch --remote exit = %d, want 2; stderr = %q", code, watchErr.String())
	}
	if code, _, stderr := run(t, project.seed, "sync", "--status", "--remote", "origin"); code != 2 {
		t.Fatalf("sync --status --remote exit = %d, want 2; stderr = %q", code, stderr)
	}
	if code, stdout, stderr := run(t, project.seed, "fetch", "--remote", "origin", "--json"); code != 0 {
		t.Fatalf("fetch --remote origin = (%d, %q, %q), want a read", code, stdout, stderr)
	} else {
		var fetched struct {
			Remote string `json:"remote"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(assertJSONResult(t, stdout, "fetch").Data, &fetched); err != nil || fetched.Remote != "origin" {
			t.Fatalf("fetch result remote = %q (%v)", fetched.Remote, err)
		}
	}
	cliCreateTask(t, project.seed, "Never for the old place")
	code, _, stderr := run(t, project.seed, "push", "--remote", "origin", "--json")
	if code != 5 {
		t.Fatalf("push --remote origin exit = %d, want 5; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryValidation, core.TasksRemoteReadOnly)
	if refusal.Remote != "origin" || refusal.Command != "workbook push" {
		t.Fatalf("refusal = %+v", refusal)
	}
	// sync --remote reads the other remote, says it is marked, and
	// publishes nothing.
	if code, stdout, stderr := run(t, project.seed, "sync", "--remote", "origin", "--json"); code != 0 {
		t.Fatalf("sync --remote origin exit = %d, want a read; stderr = %q", code, stderr)
	} else {
		var synced struct {
			Identity *struct {
				Status string `json:"status"`
			} `json:"identity"`
			Push struct {
				Status string `json:"status"`
			} `json:"push"`
		}
		if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &synced); err != nil {
			t.Fatal(err)
		}
		if synced.Identity == nil || synced.Identity.Status != "moved" || synced.Push.Status != "skipped" {
			t.Fatalf("sync --remote origin = %s, want the identity reported moved and the push skipped", stdout)
		}
	}
	if after := repositoryState(t, project.code); after != before {
		t.Fatalf("origin changed:\nbefore %s\nafter  %s", before, after)
	}
	// The ordinary commands still use the tasks repository.
	if code, _, stderr := run(t, project.seed, "push"); code != 0 {
		t.Fatalf("push = %d; stderr = %q", code, stderr)
	}
}

// A project that never chose a remote is exactly what it was: every Git
// command that talks to a remote names origin, the tracked file and every
// JSON document carry no new member, and a second remote the checkout happens
// to have — named tasks, even — is never contacted.
func TestTasksRemoteUnconfiguredProjectIsUnchanged(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	decoy := newBareRepository(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	cliGit(t, clone, "remote", "add", "tasks", decoy)
	if code, _, stderr := run(t, project.seed, "update", project.ids[0], "--status", "ready"); code != 0 {
		t.Fatalf("make ready = %d; stderr = %q", code, stderr)
	}

	commands := [][]string{
		{"setup", "--json"},
		{"create", "Unconfigured", "--json"},
		{"next", "--claim", "--json"},
		{"fetch", "--json"},
		{"push", "--json"},
		{"sync", "--json"},
		{"config", "show", "--json"},
		{"list", "--json"},
	}
	for _, args := range commands {
		code, stdout, stderr := run(t, clone, args...)
		if code != 0 {
			t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
		}
		if strings.Contains(stdout, "tasksRemote") || strings.Contains(stdout, `"remote":"tasks"`) {
			t.Errorf("%v output names the tasks remote: %s", args, stdout)
		}
		if strings.Contains(stdout, `"remote":`) && !strings.Contains(stdout, `"remote":"origin"`) {
			t.Errorf("%v output names a remote other than origin: %s", args, stdout)
		}
	}
	if got := refsUnder(t, decoy, "refs/"); got != "" {
		t.Fatalf("the decoy remote received refs: %s", got)
	}
	var namespaces []string
	for _, ref := range strings.Fields(cliGitOutput(t, clone, "for-each-ref", "--format=%(refname)", "refs/workbook/remotes/")) {
		namespaces = append(namespaces, strings.SplitN(strings.TrimPrefix(ref, "refs/workbook/remotes/"), "/", 2)[0])
	}
	sort.Strings(namespaces)
	if len(namespaces) == 0 || namespaces[0] != "origin" || namespaces[len(namespaces)-1] != "origin" {
		t.Fatalf("tracking namespaces = %v, want origin's alone", namespaces)
	}
	contents, err := os.ReadFile(filepath.Join(clone, ".workbook", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "tasksRemote") {
		t.Fatalf("config.json = %s", contents)
	}
	// An error envelope carries no new member either.
	code, _, stderr := run(t, clone, "show", "WB-NOSUCH", "--json")
	if code == 0 || strings.Contains(stderr, "tasksRemote") {
		t.Fatalf("show unknown = (%d, %q)", code, stderr)
	}
}

// A tasks-only folder serving the old place is refused once the tasks move,
// and told to set up a new folder from the new address, since its origin is
// the only remote it has and its settings are not committed anywhere. A new
// tasks-only folder set up from the old address is refused the same way.
func TestTasksRemoteTasksOnlyFolderOnTheOldPlaceIsSentToTheNewOne(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	old := t.TempDir()
	if code, stdout, stderr := run(t, old, "setup", "--tasks-only", project.code); code != 0 {
		t.Fatalf("setup --tasks-only before the move = (%d, %q, %q)", code, stdout, stderr)
	}
	project.moveTasks(t)

	want := "workbook setup --tasks-only " + project.tasks
	code, _, stderr := run(t, old, "fetch", "--json")
	if code != 3 {
		t.Fatalf("old folder fetch exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.Command != want || refusal.MovedTo == nil || refusal.MovedTo.URL != project.tasks {
		t.Fatalf("old folder refusal = %+v, want command %q", refusal, want)
	}

	fresh := t.TempDir()
	code, _, stderr = run(t, fresh, "setup", "--tasks-only", project.code, "--json")
	if code != 3 {
		t.Fatalf("setup --tasks-only from the old place exit = %d, want 3; stderr = %q", code, stderr)
	}
	refusal = requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.Command != want {
		t.Fatalf("new folder refusal = %+v, want command %q", refusal, want)
	}

	if code, stdout, stderr := run(t, t.TempDir(), "setup", "--tasks-only", project.tasks); code != 0 {
		t.Fatalf("setup --tasks-only from the new place = (%d, %q, %q)", code, stdout, stderr)
	}
}
