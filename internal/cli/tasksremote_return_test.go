package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// refTips is every ref a repository holds, by name.
func refTips(t *testing.T, repository string) map[string]string {
	t.Helper()
	tips := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)")), "\n") {
		if name, tip, found := strings.Cut(line, " "); found {
			tips[name] = tip
		}
	}
	return tips
}

// requireOnlyFastForwards fails when any ref repository held in before is gone
// or has moved to a commit its old tip is not an ancestor of, except those
// named in allowed. That is what a force push or a deleted ref leaves.
func requireOnlyFastForwards(t *testing.T, repository string, before map[string]string, allowed ...string) {
	t.Helper()
	after := refTips(t, repository)
	for name, old := range before {
		if containsString(allowed, name) {
			continue
		}
		tip, found := after[name]
		if !found {
			t.Errorf("%s lost %s", repository, name)
			continue
		}
		if tip == old {
			continue
		}
		if err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", old, tip).Run(); err != nil {
			t.Errorf("%s moved %s from %s to %s, which does not descend from it", repository, name, old, tip)
		}
	}
}

// identityTipSubject is the subject of the commit a repository's identity ref
// names.
func identityTipSubject(t *testing.T, repository string) string {
	t.Helper()
	return strings.TrimSpace(cliGitOutput(t, repository, "log", "-1", "--format=%s", "refs/workbook/project"))
}

type moveBackRun struct {
	From     core.TasksRemoteTarget `json:"from"`
	To       core.TasksRemoteTarget `json:"to"`
	Remote   string                 `json:"remote"`
	Marked   string                 `json:"marked"`
	Returned string                 `json:"returned"`
	Tasks    int                    `json:"tasks"`
	Verified bool                   `json:"verified"`
}

func moveTasksJSON(t *testing.T, seed string) moveBackRun {
	t.Helper()
	code, stdout, stderr := run(t, seed, "move-tasks", "--json")
	if code != 0 {
		t.Fatalf("move-tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved moveBackRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	return moved
}

// The tasks go back to origin with the same two commands that moved them
// away: config unset tasks-remote, then move-tasks. Origin ends holding every
// operation once, edits made at the tasks repository since included, and
// records the return on top of its marker; the tasks repository is marked as
// moved to origin. Neither place loses a ref or has one moved backwards. A
// checkout still on the first settings simply works again and publishes the
// edit it kept; one on the tasks repository's settings is refused, told
// origin, and publishes its kept edit after it pulls and sets up; and a
// tasks-only folder that followed the tasks to the tasks repository is
// repointed at origin, a place it was on before, by the command its refusal
// prints.
func TestTasksRemoteMovesBackToOriginWithTheTwoCommands(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	onOrigin := cloneCode(t, project)
	if code, stdout, stderr := run(t, onOrigin, "setup"); code != 0 {
		t.Fatalf("setup on origin = (%d, %q, %q)", code, stdout, stderr)
	}
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.code); code != 0 {
		t.Fatalf("setup --tasks-only on origin = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")
	project.moveTasks(t)
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.tasks); code != 0 {
		t.Fatalf("repoint the folder at tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	onTasks := cloneCode(t, project)
	if code, stdout, stderr := run(t, onTasks, "setup"); code != 0 {
		t.Fatalf("setup on tasks = (%d, %q, %q)", code, stdout, stderr)
	}

	// Work at the tasks repository after the first move, and an edit the
	// first-settings checkout keeps while it is refused.
	if code, _, stderr := run(t, project.seed, "update", project.ids[0], "--title", "Edited at the tasks repository"); code != 0 {
		t.Fatalf("update at tasks = %d; stderr = %q", code, stderr)
	}
	added := cliCreateTask(t, project.seed, "Created at the tasks repository")
	if code, stdout, stderr := run(t, project.seed, "sync"); code != 0 {
		t.Fatalf("sync at tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, _, stderr := run(t, onOrigin, "update", project.ids[1], "--title", "Kept by the first settings", "--no-sync"); code != 0 {
		t.Fatalf("update on the first settings = %d; stderr = %q", code, stderr)
	}
	// The first-settings checkout learns of the move: its fetch is refused,
	// and its copy of origin's identity ref now carries the marker.
	if code, _, stderr := run(t, onOrigin, "fetch", "--json"); code != 3 {
		t.Fatalf("fetch on the first settings after the move = %d, want 3; stderr = %q", code, stderr)
	}
	if got := strings.TrimSpace(cliGitOutput(t, onOrigin, "log", "-1", "--format=%s", "refs/workbook/remotes/origin/project")); got != "workbook: tasks moved" {
		t.Fatalf("the first settings' copy of origin's identity ref = %q, want the marker", got)
	}
	codeBefore, tasksBefore := refTips(t, project.code), refTips(t, project.tasks)

	code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote")
	if code != 0 || !strings.Contains(stdout, "run workbook move-tasks to move the tasks back from tasks to origin and mark tasks") {
		t.Fatalf("config unset tasks-remote = (%d, %q, %q), want the move back named", code, stdout, stderr)
	}
	moved := moveTasksJSON(t, project.seed)
	if moved.From.Remote != "tasks" || moved.To.Remote != "origin" || moved.To.URL != project.code ||
		moved.Returned != "returned" || moved.Marked != "marked" || moved.Tasks != len(project.ids)+1 || !moved.Verified {
		t.Fatalf("move-tasks back = %+v", moved)
	}
	again := moveTasksJSON(t, project.seed)
	if again.Returned != "already" || again.Marked != "already" || !again.Verified {
		t.Fatalf("move-tasks back again = %+v", again)
	}
	cliGit(t, project.seed, "add", ".workbook/config.json")
	cliGit(t, project.seed, "commit", "--quiet", "-m", "Keep the tasks with the code again")
	cliGit(t, project.seed, "push", "--quiet", "origin", "main")

	if got := identityTipSubject(t, project.code); got != "workbook: tasks returned" {
		t.Fatalf("origin's identity tip = %q", got)
	}
	if got := identityTipSubject(t, project.tasks); got != "workbook: tasks moved" {
		t.Fatalf("tasks' identity tip = %q", got)
	}
	if got, want := refsUnder(t, project.code, "refs/workbook/tasks/"), refsUnder(t, project.tasks, "refs/workbook/tasks/"); got != want {
		t.Fatalf("origin and tasks hold different task refs:\n%s\n%s", got, want)
	}
	requireOnlyFastForwards(t, project.code, codeBefore)
	requireOnlyFastForwards(t, project.tasks, tasksBefore)
	if shown := cliShowTask(t, project.seed, project.ids[0]); shown.Title != "Edited at the tasks repository" {
		t.Fatalf("seed shows %q after the move back", shown.Title)
	}

	// The first settings name origin again: that checkout works and
	// publishes what it kept.
	if code, stdout, stderr := run(t, onOrigin, "sync"); code != 0 {
		t.Fatalf("sync on the first settings after the move back = (%d, %q, %q)", code, stdout, stderr)
	}
	if got, want := cliGitOutput(t, project.code, "rev-parse", "refs/workbook/tasks/"+project.ids[1]), localTaskRef(t, onOrigin, project.ids[1]); strings.TrimSpace(got) != want {
		t.Fatalf("origin holds %s at %s, want the kept edit %s", project.ids[1], got, want)
	}
	if shown := cliShowTask(t, onOrigin, added.ID); shown.ID != added.ID {
		t.Fatalf("the first settings do not see %s", added.ID)
	}

	// The tasks repository's settings are refused and told origin.
	code, _, stderr = run(t, onTasks, "update", project.ids[0], "--title", "Kept on the tasks settings", "--json")
	if code != 3 {
		t.Fatalf("update on the tasks settings = %d, want 3; stderr = %q", code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.Remote != "tasks" || refusal.MovedTo == nil || refusal.MovedTo.Remote != "origin" ||
		refusal.MovedTo.URL != project.code || refusal.Command != "workbook setup" {
		t.Fatalf("refusal on the tasks settings = %+v", refusal)
	}
	if code, _, stderr := run(t, onTasks, "update", project.ids[0], "--title", "Kept on the tasks settings", "--no-sync"); code != 3 {
		t.Fatalf("a no-sync update on the tasks settings = %d, want 3; stderr = %q", code, stderr)
	}
	cliGit(t, onTasks, "pull", "--quiet", "--ff-only", "origin", "main")
	if code, stdout, stderr := run(t, onTasks, "setup"); code != 0 {
		t.Fatalf("setup on the tasks settings after the pull = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, _, stderr := run(t, onTasks, "update", project.ids[0], "--title", "Edited after pulling"); code != 0 {
		t.Fatalf("update after the pull = %d; stderr = %q", code, stderr)
	}
	if got, want := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/workbook/tasks/"+project.ids[0])), localTaskRef(t, onTasks, project.ids[0]); got != want {
		t.Fatalf("origin holds %s at %s, want %s", project.ids[0], got, want)
	}

	// The tasks-only folder, on the tasks repository, is sent back to origin.
	saved := cliCreateTask(t, folder, "Saved in the folder before it was refused")
	want := "workbook setup --tasks-only " + project.code
	code, _, stderr = run(t, folder, "fetch", "--json")
	if code != 3 {
		t.Fatalf("folder fetch after the move back = %d, want 3; stderr = %q", code, stderr)
	}
	if refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved); refusal.Command != want {
		t.Fatalf("folder refusal = %+v, want %q", refusal, want)
	}
	code, stdout, stderr = run(t, folder, "setup", "--tasks-only", project.code, "--json")
	if code != 0 {
		t.Fatalf("%s = (%d, %q, %q)", want, code, stdout, stderr)
	}
	var setup struct {
		TasksOnly struct {
			Remote    string `json:"remote"`
			MovedFrom string `json:"movedFrom"`
		} `json:"tasksOnly"`
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "setup").Data, &setup); err != nil {
		t.Fatal(err)
	}
	if setup.TasksOnly.Remote != project.code || setup.TasksOnly.MovedFrom != project.tasks {
		t.Fatalf("folder setup = %+v", setup.TasksOnly)
	}
	if cliGitOutputAllowingMissing(t, project.code, "refs/workbook/tasks/"+saved.ID) == "" {
		t.Fatalf("origin lacks %s, saved in the folder", saved.ID)
	}
	if code, stdout, stderr := run(t, project.seed, "sync"); code != 0 {
		t.Fatalf("seed sync = (%d, %q, %q)", code, stdout, stderr)
	}
	requireEachOperationOnce(t, project.code)
	requireFreshCloneValidates(t, project.code)
	if code, stdout, stderr := run(t, project.seed, "validate", "--full"); code != 0 {
		t.Fatalf("validate --full in the seed = (%d, %q, %q)", code, stdout, stderr)
	}
}

// A task compacted at the tasks repository while the tasks were there
// reaches origin as that checkpoint, which replaces the older history origin
// kept for it the way any compaction's publication does: conditionally on
// origin still holding exactly the tip the checkpoint accounts for. Every
// other ref only moves forward.
func TestTasksRemoteMoveBackCarriesATaskCompactedInBetween(t *testing.T) {
	t.Parallel()
	project := movedProject(t, testrepo.FormatSHA1)
	task := project.ids[0]
	compactRun(t, project.seed, "update", task, "--status", "done")
	compactRun(t, project.seed, "update", task, "--description", "Closed at the tasks repository")
	compactRun(t, project.seed, "compact", "--no-backup")
	checkpoint := localTaskRef(t, project.seed, task)
	if got := tasksTip(t, project.tasks, task); got != checkpoint {
		t.Fatalf("tasks holds %s at %s, want the checkpoint %s", task, got, checkpoint)
	}
	codeBefore := refTips(t, project.code)

	if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
		t.Fatalf("config unset = (%d, %q, %q)", code, stdout, stderr)
	}
	moved := moveTasksJSON(t, project.seed)
	if moved.Returned != "returned" || !moved.Verified {
		t.Fatalf("move-tasks back = %+v", moved)
	}
	if got := tasksTip(t, project.code, task); got != checkpoint {
		t.Fatalf("origin holds %s at %s, want the checkpoint %s", task, got, checkpoint)
	}
	requireOnlyFastForwards(t, project.code, codeBefore, "refs/workbook/tasks/"+task)
	requireEachOperationOnce(t, project.code)
	requireFreshCloneValidates(t, project.code)
}

// An edit that reaches the tasks repository after it was marked as moved back
// to origin, as an edit racing the mark does, is carried to origin by the
// next move-tasks, once.
func TestTasksRemoteLateEditAtTheLeftPlaceIsCarriedBack(t *testing.T) {
	t.Parallel()
	project := movedProject(t, testrepo.FormatSHA1)
	onTasks := cloneCode(t, project)
	if code, stdout, stderr := run(t, onTasks, "setup"); code != 0 {
		t.Fatalf("setup on tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
		t.Fatalf("config unset = (%d, %q, %q)", code, stdout, stderr)
	}
	moveTasksJSON(t, project.seed)

	straggler(t, onTasks, project.ids[1], "--title", "Raced the mark")
	ref := "refs/workbook/tasks/" + project.ids[1]
	cliGit(t, onTasks, "push", "--quiet", "--no-verify", "tasks", ref+":"+ref)

	code, stdout, stderr := run(t, project.seed, "move-tasks", "--json")
	if code != 0 {
		t.Fatalf("move-tasks again = (%d, %q, %q)", code, stdout, stderr)
	}
	var again moveRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &again); err != nil {
		t.Fatal(err)
	}
	if again.Stragglers == nil || again.Stragglers.Operations != 1 || !containsString(again.Stragglers.Tasks, project.ids[1]) {
		t.Fatalf("move-tasks again = %+v, want the late edit carried", again)
	}
	if shown := cliShowTask(t, project.seed, project.ids[1]); shown.Title != "Raced the mark" {
		t.Fatalf("seed shows %q", shown.Title)
	}
	requireEachOperationOnce(t, project.code)
	if code, stdout, stderr := run(t, project.seed, "move-tasks", "--json"); code != 0 || strings.Contains(stdout, "stragglers") {
		t.Fatalf("a third move-tasks = (%d, %q, %q), want nothing more carried", code, stdout, stderr)
	}
}

// No help, refusal or setting output calls a move one-way any more.
func TestTasksRemoteMoveIsNotDescribedAsOneWay(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, t.TempDir(), "move-tasks", "--help")
	if code != 0 || strings.Contains(strings.ToLower(stdout+stderr), "one-way") || !strings.Contains(stdout, "back to origin") {
		t.Fatalf("move-tasks --help = (%d, %q, %q)", code, stdout, stderr)
	}
}
