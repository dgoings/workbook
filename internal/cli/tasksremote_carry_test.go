package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// moveRun is the part of a move-tasks result these tests read.
type moveRun struct {
	Marked     string `json:"marked"`
	Tasks      int    `json:"tasks"`
	Identical  int    `json:"identical"`
	Carried    int    `json:"carried"`
	Verified   bool   `json:"verified"`
	Stragglers *struct {
		Operations int      `json:"operations"`
		Tasks      []string `json:"tasks"`
		Actors     []string `json:"actors"`
	} `json:"stragglers"`
	SetAside []struct {
		TaskID     string   `json:"taskId"`
		Operations []string `json:"operations"`
		ParkedRef  string   `json:"parkedRef"`
	} `json:"setAside"`
}

// moveTasksAgain runs move-tasks in the seed and decodes its result, which it
// prints on success and on a finished run that set operations aside.
func moveTasksAgain(t *testing.T, project tasksRemoteProject, wantCode int) moveRun {
	t.Helper()
	code, stdout, stderr := run(t, project.seed, "move-tasks", "--json")
	if code != wantCode {
		t.Fatalf("move-tasks exit = %d, want %d; stdout = %q; stderr = %q", code, wantCode, stdout, stderr)
	}
	var moved moveRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified {
		t.Fatalf("move-tasks result = %+v, want verified", moved)
	}
	return moved
}

// operationCounts reads every task history in a repository straight from its
// operation blobs and counts each operation ID, task by task.
func operationCounts(t *testing.T, repository string) map[string]map[string]int {
	t.Helper()
	counts := make(map[string]map[string]int)
	refs := strings.Fields(cliGitOutput(t, repository, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/"))
	for _, ref := range refs {
		taskID := strings.TrimPrefix(ref, "refs/workbook/tasks/")
		counts[taskID] = make(map[string]int)
		for _, commit := range strings.Fields(cliGitOutput(t, repository, "rev-list", ref)) {
			var pack struct {
				Operations []struct {
					ID string `json:"id"`
				} `json:"operations"`
			}
			blob := cliGitOutput(t, repository, "cat-file", "blob", commit+":operation.json")
			if err := json.Unmarshal([]byte(blob), &pack); err != nil {
				t.Fatalf("%s at %s: %v", ref, commit, err)
			}
			for _, operation := range pack.Operations {
				counts[taskID][operation.ID]++
			}
		}
	}
	return counts
}

// requireEachOperationOnce fails when any task history in repository records
// an operation ID more than once, and returns how many operations each holds.
func requireEachOperationOnce(t *testing.T, repository string) map[string]int {
	t.Helper()
	totals := make(map[string]int)
	for taskID, operations := range operationCounts(t, repository) {
		for id, count := range operations {
			if count != 1 {
				t.Errorf("%s records operation %s %d times", taskID, id, count)
			}
		}
		totals[taskID] = len(operations)
	}
	return totals
}

// requireFreshCloneValidates sets up a tasks-only folder from the tasks
// repository, which is what a newcomer gets, and validates every history.
func requireFreshCloneValidates(t *testing.T, tasks string) {
	t.Helper()
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", tasks); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, folder, "validate", "--full", "--json"); code != 0 {
		t.Fatalf("validate in a fresh tasks-only folder = (%d, %q, %q)", code, stdout, stderr)
	}
	requireEachOperationOnce(t, folder)
}

// straggler records an edit in a checkout that set up before the move and
// publishes it to the old place after the move with plain Git, as an edit
// that raced the mark does (see TestTasksRemoteEditPublishedAroundTheMark for
// the race itself).
func straggler(t *testing.T, teammate, taskID string, args ...string) {
	t.Helper()
	if code, _, stderr := run(t, teammate, append(append([]string{"update", taskID}, args...), "--no-sync")...); code != 0 {
		t.Fatalf("teammate update = %d; stderr = %q", code, stderr)
	}
}

func publishStraggler(t *testing.T, teammate, taskID string) {
	t.Helper()
	ref := "refs/workbook/tasks/" + taskID
	cliGit(t, teammate, "push", "--quiet", "--no-verify", "origin", ref+":"+ref)
}

// (a) An edit published to the old place after the move, on a task also
// edited at the new place, is carried once, and every later run finds it
// carried: the operation count, the commit count and every task stay put.
func TestMoveTasksCarriesAStragglerOnceHoweverOftenItRuns(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	edited := project.ids[0]
	straggler(t, teammate, edited, "--description", "Written by the teammate before they heard about the move")
	project.moveTasks(t)

	// The seed edits the same task at the new place, another field.
	if code, _, stderr := run(t, project.seed, "update", edited, "--title", "Retitled at the new place"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	publishStraggler(t, teammate, edited)

	first := moveTasksAgain(t, project, 0)
	if first.Stragglers == nil || first.Stragglers.Operations != 1 ||
		strings.Join(first.Stragglers.Tasks, ",") != edited || len(first.Stragglers.Actors) != 1 {
		t.Fatalf("first rerun stragglers = %+v, want the teammate's one operation on %s", first.Stragglers, edited)
	}
	if first.Marked != "already" || first.Carried != 1 || first.Identical != len(project.ids)-1 {
		t.Fatalf("first rerun = %+v", first)
	}
	commits := taskCommitCount(t, project.tasks, edited)
	totals := requireEachOperationOnce(t, project.tasks)
	for round := 0; round < 3; round++ {
		again := moveTasksAgain(t, project, 0)
		if again.Stragglers != nil || len(again.SetAside) != 0 || again.Carried != 1 {
			t.Fatalf("rerun %d = %+v, want nothing carried again", round, again)
		}
	}
	if got := taskCommitCount(t, project.tasks, edited); got != commits {
		t.Fatalf("tasks holds %d commits for %s after the reruns, want %d", got, edited, commits)
	}
	if got := requireEachOperationOnce(t, project.tasks); fmt.Sprint(got) != fmt.Sprint(totals) {
		t.Fatalf("operations per task after the reruns = %v, want %v", got, totals)
	}
	// Both edits are in the task.
	shown := cliShowTask(t, project.seed, edited)
	if shown.Title != "Retitled at the new place" || !strings.Contains(shown.Description, "before they heard") {
		t.Fatalf("task after the carry = %+v", shown)
	}
	requireFreshCloneValidates(t, project.tasks)
}

// A straggler that conflicts with the new place's history is set aside once,
// named, with exit 8; a rerun reports it set aside and does not try it again.
func TestMoveTasksSetsAsideAConflictingStragglerOnce(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	edited := project.ids[0]
	straggler(t, teammate, edited, "--description", "The teammate's description")
	project.moveTasks(t)
	if code, _, stderr := run(t, project.seed, "update", edited, "--description", "The seed's description"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	publishStraggler(t, teammate, edited)

	code, stdout, stderr := run(t, project.seed, "move-tasks", "--json")
	if code != 8 {
		t.Fatalf("move-tasks with a conflicting straggler = (%d, %q, %q), want 8", code, stdout, stderr)
	}
	if !strings.Contains(stderr, edited) {
		t.Fatalf("conflict report %q does not name %s", stderr, edited)
	}
	var first moveRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.SetAside) != 1 || first.SetAside[0].TaskID != edited || len(first.SetAside[0].Operations) != 1 ||
		first.SetAside[0].ParkedRef == "" {
		t.Fatalf("set aside = %+v", first.SetAside)
	}
	totals := requireEachOperationOnce(t, project.tasks)
	commits := taskCommitCount(t, project.tasks, edited)
	for round := 0; round < 2; round++ {
		again := moveTasksAgain(t, project, 0)
		if len(again.SetAside) != 1 || again.SetAside[0].ParkedRef != "" || again.Stragglers != nil {
			t.Fatalf("rerun %d = %+v, want the operation reported set aside earlier and nothing carried", round, again)
		}
	}
	if got := taskCommitCount(t, project.tasks, edited); got != commits {
		t.Fatalf("tasks holds %d commits for %s after the reruns, want %d", got, edited, commits)
	}
	if got := requireEachOperationOnce(t, project.tasks); fmt.Sprint(got) != fmt.Sprint(totals) {
		t.Fatalf("operations per task after the reruns = %v, want %v", got, totals)
	}
	if shown := cliShowTask(t, project.seed, edited); shown.Description != "The seed's description" {
		t.Fatalf("description after the conflict = %q, want the new place's", shown.Description)
	}
	requireFreshCloneValidates(t, project.tasks)
}

// (b) A set-up teammate who reads the old place with fetch --remote and then
// synchronizes, over and over, changes nothing at the new place: reading the
// old place moves no task ref. Their sync says the old place holds an edit
// the new place lacks and names the command that carries it, and that command
// carries it once.
func TestTasksRemoteReadingTheOldPlaceNeverWritesTheNewOne(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	stale := cloneCode(t, project)
	if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup = (%d, %q, %q)", code, stdout, stderr)
	}
	edited := project.ids[1]
	straggler(t, stale, edited, "--title", "Retitled by a checkout on the old settings")
	project.moveTasks(t)
	if code, _, stderr := run(t, project.seed, "update", edited, "--description", "Described at the new place"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	publishStraggler(t, stale, edited)

	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	} else if !strings.Contains(stdout, "workbook move-tasks") {
		t.Fatalf("setup did not say the old place holds an edit the new place lacks:\n%s", stdout)
	}
	before := repositoryState(t, project.tasks)
	localBefore := refsUnder(t, teammate, "refs/workbook/tasks/")
	for round := 0; round < 3; round++ {
		if code, stdout, stderr := run(t, teammate, "fetch", "--remote", "origin", "--json"); code != 0 {
			t.Fatalf("fetch --remote origin round %d = (%d, %q, %q)", round, code, stdout, stderr)
		}
		if got := refsUnder(t, teammate, "refs/workbook/tasks/"); got != localBefore {
			t.Fatalf("fetch --remote origin moved a task ref:\nbefore %s\nafter  %s", localBefore, got)
		}
		if got := refsUnder(t, teammate, "refs/workbook/reconciled/"); got != "" {
			t.Fatalf("fetch --remote origin parked something: %s", got)
		}
		code, stdout, stderr := run(t, teammate, "sync")
		if code != 0 {
			t.Fatalf("sync round %d = (%d, %q, %q)", round, code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "workbook move-tasks") || !strings.Contains(stdout+stderr, edited) {
			t.Fatalf("sync round %d did not report the straggler on %s:\nstdout %s\nstderr %s", round, edited, stdout, stderr)
		}
	}
	if after := repositoryState(t, project.tasks); after != before {
		t.Fatalf("the new place changed while the old one was only read:\nbefore %s\nafter  %s", before, after)
	}
	requireFreshCloneValidates(t, project.tasks)

	moved := moveTasksAgain(t, project, 0)
	if moved.Stragglers == nil || moved.Stragglers.Operations != 1 {
		t.Fatalf("move-tasks = %+v, want the one straggler carried", moved)
	}
	if code, stdout, stderr := run(t, teammate, "sync"); code != 0 || strings.Contains(stdout+stderr, "workbook move-tasks") {
		t.Fatalf("sync after the carry = (%d, %q, %q), want no straggler notice", code, stdout, stderr)
	}
	requireFreshCloneValidates(t, project.tasks)
}

// (c) A tasks repository that already held an older copy of the project,
// whose history for a task diverged from origin's, receives each operation
// once, and keeps doing so on every rerun.
func TestMoveTasksIntoADivergedEarlierCopyAddsEachOperationOnce(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	// An earlier copy, made by hand and then edited there by somebody else.
	cliGit(t, project.seed, "push", "--quiet", "--no-verify", project.tasks, "refs/workbook/*:refs/workbook/*")
	elsewhere := t.TempDir()
	if code, stdout, stderr := run(t, elsewhere, "setup", "--tasks-only", project.tasks); code != 0 {
		t.Fatalf("setup --tasks-only on the earlier copy = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, elsewhere, "config", "user.name", "Somebody Else")
	cliGit(t, elsewhere, "config", "user.email", "elsewhere@example.test")
	for _, id := range project.ids {
		if code, _, stderr := run(t, elsewhere, "update", id, "--label", "from-the-earlier-copy"); code != 0 {
			t.Fatalf("edit at the earlier copy = %d; stderr = %q", code, stderr)
		}
	}
	// Meanwhile origin moves on.
	for _, id := range project.ids {
		if code, _, stderr := run(t, project.seed, "update", id, "--status", "ready"); code != 0 {
			t.Fatalf("edit at origin = %d; stderr = %q", code, stderr)
		}
	}
	want := make(map[string]int)
	for taskID, operations := range operationCounts(t, project.code) {
		want[taskID] = len(operations)
	}
	for taskID, operations := range operationCounts(t, project.tasks) {
		for id := range operations {
			if _, found := operationCounts(t, project.code)[taskID][id]; !found {
				want[taskID]++
			}
		}
	}

	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = %d; stderr = %q", code, stderr)
	}
	if moved := moveTasksAgain(t, project, 0); moved.Marked != "marked" || moved.Carried != len(project.ids) {
		t.Fatalf("move-tasks = %+v, want every task carried by operation", moved)
	}
	if got := requireEachOperationOnce(t, project.tasks); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("operations per task at the new place = %v, want %v (the union)", got, want)
	}
	commits := make(map[string]int)
	for _, id := range project.ids {
		commits[id] = taskCommitCount(t, project.tasks, id)
	}
	for round := 0; round < 2; round++ {
		again := moveTasksAgain(t, project, 0)
		if again.Stragglers != nil || again.Carried != len(project.ids) {
			t.Fatalf("rerun %d = %+v, want every task carried and nothing new", round, again)
		}
	}
	for _, id := range project.ids {
		if got := taskCommitCount(t, project.tasks, id); got != commits[id] {
			t.Fatalf("tasks holds %d commits for %s after the reruns, want %d", got, id, commits[id])
		}
		shown := cliShowTask(t, project.seed, id)
		if string(shown.Status) != "ready" || !containsString(shown.Labels, "from-the-earlier-copy") {
			t.Fatalf("task %s after the move = %+v, want both places' edits", id, shown)
		}
	}
	requireFreshCloneValidates(t, project.tasks)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
