package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
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

	// The teammate still holds the straggler under its own commit, which the
	// new place holds rewritten onto the seed's edit. Picking up the move and
	// synchronizing, again and again, adds nothing there: the teammate's copy
	// of the operation is recognized as the one the new place already holds.
	// The seed edits the same field afterwards, so replaying the teammate's
	// copy would not be a no-op: it would record the operation a second time.
	if code, _, stderr := run(t, project.seed, "update", edited, "--description", "Rewritten at the new place after the carry"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	commits = taskCommitCount(t, project.tasks, edited)
	totals = requireEachOperationOnce(t, project.tasks)
	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup after the pull = (%d, %q, %q)", code, stdout, stderr)
	}
	for round := 0; round < 3; round++ {
		if code, stdout, stderr := run(t, teammate, "sync"); code != 0 {
			t.Fatalf("teammate sync %d = (%d, %q, %q)", round, code, stdout, stderr)
		}
	}
	if got := taskCommitCount(t, project.tasks, edited); got != commits {
		t.Fatalf("tasks holds %d commits for %s after the teammate synchronized, want %d", got, edited, commits)
	}
	if got := requireEachOperationOnce(t, project.tasks); fmt.Sprint(got) != fmt.Sprint(totals) {
		t.Fatalf("operations per task after the teammate synchronized = %v, want %v", got, totals)
	}
	if code, stdout, stderr := run(t, teammate, "validate", "--full", "--json"); code != 0 {
		t.Fatalf("teammate validate = (%d, %q, %q)", code, stdout, stderr)
	}
	if shown := cliShowTask(t, teammate, edited); shown.Description != "Rewritten at the new place after the carry" {
		t.Fatalf("teammate's task after the syncs = %+v", shown)
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
// old place moves no task ref. Neither setup nor sync looks at the old place
// for edits the new place lacks; move-tasks run again carries the edit, once,
// and the teammate's next sync brings it.
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
	} else if strings.Contains(stdout, "workbook move-tasks") {
		t.Fatalf("setup looked at the old place, which only move-tasks does:\n%s", stdout)
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
		if strings.Contains(stdout+stderr, "workbook move-tasks") {
			t.Fatalf("sync round %d looked at the old place, which only move-tasks does:\nstdout %s\nstderr %s", round, stdout, stderr)
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
	if code, stdout, stderr := run(t, teammate, "sync"); code != 0 {
		t.Fatalf("sync after the carry = (%d, %q, %q)", code, stdout, stderr)
	}
	if shown := cliShowTask(t, teammate, edited); shown.Title != "Retitled by a checkout on the old settings" {
		t.Fatalf("teammate's task after the carry = %+v, want the straggler", shown)
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

// Edits published to the old place after the move that conflict with the new
// place are reported to whoever runs move-tasks, in the clone they run it
// from, and nowhere else: setup and sync in other checkouts do not look at the
// old place and say nothing. The report names each task by title and ID, says
// which place did what, and where the text is kept; a rerun in the same clone
// is quiet, and one in another clone reports them there, once. A parked ref
// dismissed on the board is tried again by the next run, which parks it again
// and reports it once more.
func TestMoveTasksSetAsideEditsAreReportedWhereMoveTasksRuns(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	described, deleted := project.ids[0], project.ids[1]
	straggler(t, teammate, described, "--description", "The teammate's description")
	straggler(t, teammate, deleted, "--title", "Renamed by the teammate")
	project.moveTasks(t)
	if code, _, stderr := run(t, project.seed, "update", described, "--description", "The seed's description"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, project.seed, "delete", deleted); code != 0 {
		t.Fatalf("seed delete = %d; stderr = %q", code, stderr)
	}
	publishStraggler(t, teammate, described)
	publishStraggler(t, teammate, deleted)

	requireSetAsideReport := func(t *testing.T, clone string) {
		t.Helper()
		code, stdout, stderr := run(t, clone, "move-tasks")
		if code != 8 {
			t.Fatalf("move-tasks = (%d, %q, %q), want 8", code, stdout, stderr)
		}
		for _, want := range []string{
			`"Shared before the move" (` + described + `): its description was changed both at origin and at tasks`,
			`"Also shared before the move" (` + deleted + `): it was deleted at tasks, so the change to its title made at origin cannot apply to it`,
			"refs/workbook/reconciled/" + described + "/",
			"refs/workbook/reconciled/" + deleted + "/",
			"workbook serve run from this clone shows it on its task as a set-aside edit",
			"apply what you want of it by hand",
		} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("move-tasks report %q lacks %q", stderr, want)
			}
		}
		if strings.Contains(stderr, "tombstoned") {
			t.Fatalf("move-tasks report %q names the roles the wrong way round", stderr)
		}
		for round := 0; round < 2; round++ {
			if code, stdout, stderr := run(t, clone, "move-tasks"); code != 0 {
				t.Fatalf("rerun %d in the same clone = (%d, %q, %q), want quiet success", round, code, stdout, stderr)
			}
		}
	}
	requireSetAsideReport(t, project.seed)

	// Another checkout on the new settings hears nothing from setup or sync.
	other := cloneCode(t, project)
	code, stdout, stderr := run(t, other, "setup", "--json")
	if code != 0 || strings.Contains(stdout, "leftBehind") {
		t.Fatalf("other setup = (%d, %q, %q), want no left-behind report", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, other, "setup"); code != 0 || strings.Contains(stdout, "Left behind") {
		t.Fatalf("other setup text = (%d, %q, %q), want no left-behind line", code, stdout, stderr)
	}
	for round := 0; round < 2; round++ {
		code, stdout, stderr := run(t, other, "sync", "--json")
		if code != 0 || len(assertJSONResult(t, stdout, "sync").Warnings) != 0 {
			t.Fatalf("other sync %d = (%d, %q, %q), want no warning", round, code, stdout, stderr)
		}
	}
	// Run from there, move-tasks reports them there, once.
	requireSetAsideReport(t, other)

	// Dismissing the parked ref, as the board does, lets the next run try the
	// edit again: it conflicts again, is parked again and reported once more.
	parked := strings.Fields(cliGitOutput(t, project.seed, "for-each-ref", "--format=%(refname)",
		"refs/workbook/reconciled/"+described+"/"))
	if len(parked) != 1 {
		t.Fatalf("parked refs for %s = %v, want one", described, parked)
	}
	cliGit(t, project.seed, "update-ref", "-d", parked[0])
	code, _, stderr = run(t, project.seed, "move-tasks")
	if code != 8 || !strings.Contains(stderr, described) || strings.Contains(stderr, deleted) {
		t.Fatalf("move-tasks after the dismissal = (%d, %q), want %s alone reported again", code, stderr, described)
	}
	if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 {
		t.Fatalf("move-tasks after the report = (%d, %q, %q), want quiet success", code, stdout, stderr)
	}
	if shown := cliShowTask(t, project.seed, described); shown.Description != "The seed's description" {
		t.Fatalf("description = %q, want the new place's, never the set-aside one", shown.Description)
	}
	requireEachOperationOnce(t, project.tasks)
	requireFreshCloneValidates(t, project.tasks)
}

// move-tasks --clean-up deletes the relics at the place left only once the
// new place has been shown to hold every one of them. A run whose new place
// lost a relic fails its verification, naming the relic, and deletes nothing
// at the old place; once the new place keeps it, the next run carries it,
// verifies, and cleans the old place up.
func TestMoveTasksCleanUpRemovesRelicsOnlyAfterTheyAreCarried(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	relics := createThreeRelics(t, project.seed)
	if code, stdout, stderr := run(t, project.seed, "push"); code != 0 {
		t.Fatalf("push = (%d, %q, %q)", code, stdout, stderr)
	}
	published := refsUnder(t, project.code, "refs/workbook/relics/")
	if strings.Count(published, "\n")+1 != len(relics) {
		t.Fatalf("origin holds before the move:\n%s", published)
	}

	// The new place drops the last relic every time it is pushed there.
	lost := relicRef(relics[len(relics)-1])
	hook := filepath.Join(project.tasks, "hooks", "post-receive")
	testenv.WriteExecutable(t, hook, []byte("#!/bin/sh\nwhile read old new ref; do\n"+
		"  if [ \"$ref\" = \""+lost+"\" ]; then git update-ref -d \"$ref\"; fi\ndone\n"))
	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 8 || stdout != "" || !strings.Contains(stderr, "relic "+relics[len(relics)-1]+" (lacks 1 change(s))") {
		t.Fatalf("move-tasks --clean-up with a relic lost at the new place = (%d, %q, %q), want 8 naming the relic", code, stdout, stderr)
	}
	if got := refsUnder(t, project.code, "refs/workbook/relics/"); got != published {
		t.Fatalf("origin's relic refs after the stopped run:\n%s\nwant\n%s", got, published)
	}

	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 0 {
		t.Fatalf("move-tasks --clean-up = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved moveTasksSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || moved.Relics != len(relics) || moved.CleanUp == nil {
		t.Fatalf("move-tasks --clean-up = %+v, cleanUp %+v", moved.MoveTasksResult, moved.CleanUp)
	}
	if got := refsUnder(t, project.code, "refs/workbook/relics/"); got != "" {
		t.Fatalf("origin still holds relic refs after the clean-up:\n%s", got)
	}
	if got := refsUnder(t, project.tasks, "refs/workbook/relics/"); got != published {
		t.Fatalf("the new place's relic refs after the clean-up:\n%s\nwant\n%s", got, published)
	}
}

// A ref under the relic namespace at the place left that is not one of this
// project's relics is never carried, so move-tasks --clean-up fails its
// verification naming it and deletes nothing at the old place.
func TestMoveTasksCleanUpKeepsARelicRefTheMoveCannotCarry(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	relics := createThreeRelics(t, project.seed)
	if code, stdout, stderr := run(t, project.seed, "push"); code != 0 {
		t.Fatalf("push = (%d, %q, %q)", code, stdout, stderr)
	}
	_, body, _ := core.ParseRelicID(relics[0])
	stray := core.NewRelicID("OTHER", body)
	cliGit(t, project.code, "update-ref", relicRef(stray), cliGitOutput(t, project.code, "rev-parse", relicRef(relics[0])))
	// The move marks the old place, so only its task and relic refs are compared.
	held := func() string {
		return refsUnder(t, project.code, "refs/workbook/tasks/") + "\n" + refsUnder(t, project.code, "refs/workbook/relics/")
	}
	before := held()
	if !strings.Contains(before, relicRef(stray)+" ") {
		t.Fatalf("origin before the move:\n%s\nwant %s", before, stray)
	}
	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	want := "relic ref " + stray + " (not one of this project's relics, so the move cannot carry it)"
	code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 8 || stdout != "" || !strings.Contains(stderr, want) {
		t.Fatalf("move-tasks --clean-up with %s at the old place = (%d, %q, %q), want 8 naming it", stray, code, stdout, stderr)
	}
	if after := held(); after != before {
		t.Fatalf("origin's task and relic refs after the stopped run:\n%s\nwant\n%s", after, before)
	}
}
