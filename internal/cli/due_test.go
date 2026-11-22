package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// cliDueMember decodes a command's task and reports its raw due member, so a
// test can tell null from a missing member, which core.Task cannot.
func cliDueMember(t *testing.T, stdout, command string) json.RawMessage {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(assertJSONResult(t, stdout, command).Data, &members); err != nil {
		t.Fatalf("decode %s: %v; output = %s", command, err, stdout)
	}
	due, ok := members["due"]
	if !ok {
		t.Fatalf("%s task has no due member: %s", command, stdout)
	}
	return due
}

// reportedDue is the date a decoded task object reported. Its due member
// decodes into ReportedDue, the field that wins it, so TaskData.Due stays empty
// in any task read back from JSON.
func reportedDue(task core.Task) string {
	if task.ReportedDue == nil {
		return ""
	}
	return *task.ReportedDue
}

// dueOperations reads a task's history from its operation blobs and returns
// every due value it records, oldest first, and the minimum reader each pack
// that recorded one declared.
func dueOperations(t *testing.T, repository, taskID string) ([]string, []int) {
	t.Helper()
	var values []string
	var readers []int
	ref := "refs/workbook/tasks/" + taskID
	for _, commit := range strings.Fields(cliGitOutput(t, repository, "rev-list", "--reverse", "--topo-order", ref)) {
		var pack struct {
			MinReader  int `json:"minReader"`
			Operations []struct {
				Type  string `json:"type"`
				Field string `json:"field"`
				Value string `json:"value"`
				Task  *struct {
					Due string `json:"due"`
				} `json:"task"`
			} `json:"operations"`
		}
		blob := cliGitOutput(t, repository, "cat-file", "blob", commit+":operation.json")
		if err := json.Unmarshal([]byte(blob), &pack); err != nil {
			t.Fatalf("%s at %s: %v", ref, commit, err)
		}
		for _, operation := range pack.Operations {
			switch {
			case operation.Type == "field.set" && operation.Field == "due":
				values = append(values, operation.Value)
				readers = append(readers, pack.MinReader)
			case operation.Task != nil && operation.Task.Due != "":
				values = append(values, operation.Task.Due)
				readers = append(readers, pack.MinReader)
			}
		}
	}
	return values, readers
}

func TestDueDateIsSetChangedAndClearedAsOperations(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "create", "Dated work", "--due", "2030-01-15", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create --due = (%d, %q, %q)", code, stdout, stderr)
	}
	if due := cliDueMember(t, stdout, "create"); string(due) != `"2030-01-15"` {
		t.Fatalf("created due = %s", due)
	}
	created := decodeMutationTask(t, stdout, "create")

	code, stdout, stderr = run(t, repository, "update", created.ID, "--due", "2030-02-01", "--no-sync")
	if code != 0 || !strings.Contains(stdout, "Due:\t2030-01-15 → 2030-02-01") {
		t.Fatalf("update --due = (%d, %q, %q), want the change named", code, stdout, stderr)
	}
	if shown := cliShowTask(t, repository, created.ID); reportedDue(shown) != "2030-02-01" {
		t.Fatalf("due after the change = %q", reportedDue(shown))
	}

	code, stdout, stderr = run(t, repository, "update", created.ID, "--clear-due", "--no-sync")
	if code != 0 || !strings.Contains(stdout, "Due:\t2030-02-01 → none") {
		t.Fatalf("update --clear-due = (%d, %q, %q), want the clear named", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "show", created.ID, "--json")
	if code != 0 {
		t.Fatalf("show = (%d, %q, %q)", code, stdout, stderr)
	}
	if due := cliDueMember(t, stdout, "show"); string(due) != "null" {
		t.Fatalf("due after the clear = %s, want null", due)
	}
	if text := compactRun(t, repository, "show", created.ID); !strings.Contains(text, "Due:\t\n") {
		t.Fatalf("show text after the clear = %q, want an empty Due line", text)
	}

	values, readers := dueOperations(t, repository, created.ID)
	if strings.Join(values, ",") != "2030-01-15,2030-02-01," {
		t.Fatalf("due operations = %q, want set, change and clear, one each", values)
	}
	for index, reader := range readers {
		if reader != core.SupportedFormatGeneration {
			t.Fatalf("pack %d recording a due date declares minReader %d, want %d", index, reader, core.SupportedFormatGeneration)
		}
	}

	// A task never given a date writes no marker anywhere, so a build that
	// predates due dates keeps reading it.
	plain := cliCreateTask(t, repository, "Undated work")
	cliUpdateTitle(t, repository, plain.ID, "Still undated")
	ref := "refs/workbook/tasks/" + plain.ID
	for _, commit := range strings.Fields(cliGitOutput(t, repository, "rev-list", ref)) {
		for _, file := range []string{"operation.json", "state.json"} {
			blob := cliGitOutput(t, repository, "cat-file", "blob", commit+":"+file)
			if strings.Contains(blob, `"due"`) || strings.Contains(blob, `"minReader"`) {
				t.Fatalf("an undated task's %s carries a due member or a marker: %s", file, blob)
			}
		}
	}
}

func TestDueDateRelativeFormsAreStoredAsTheDateTheyMeant(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	before := time.Now()
	code, stdout, stderr := run(t, repository, "create", "In three days", "--due", "+3d", "--no-sync")
	after := time.Now()
	if code != 0 {
		t.Fatalf("create --due +3d = (%d, %q, %q)", code, stdout, stderr)
	}
	// The command may straddle midnight; either side of it is the right date.
	early, late := core.LocalDate(before.AddDate(0, 0, 3)), core.LocalDate(after.AddDate(0, 0, 3))
	if !strings.Contains(stdout, "Due:\tnone → "+early) && !strings.Contains(stdout, "Due:\tnone → "+late) {
		t.Fatalf("create confirmation = %q, want the resolved date %s", stdout, late)
	}
	tasks := listJSON(t, repository)
	if len(tasks) != 1 || (reportedDue(tasks[0]) != early && reportedDue(tasks[0]) != late) {
		t.Fatalf("listed = %+v, want one task due %s", tasks, late)
	}
	values, _ := dueOperations(t, repository, tasks[0].ID)
	if len(values) != 1 || values[0] != reportedDue(tasks[0]) {
		t.Fatalf("stored due = %q, want only the absolute date %s", values, reportedDue(tasks[0]))
	}

	if code, _, stderr := run(t, repository, "update", tasks[0].ID, "--due", "today", "--no-sync"); code != 0 {
		t.Fatalf("update --due today = %d; stderr = %q", code, stderr)
	}
	if shown := reportedDue(cliShowTask(t, repository, tasks[0].ID)); shown != core.LocalDate(time.Now()) && shown != core.LocalDate(after) {
		t.Fatalf("due today = %q", shown)
	}
}

func listJSON(t *testing.T, repository string, args ...string) []core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, append(append([]string{"list"}, args...), "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("list %v = (%d, %q, %q)", args, code, stdout, stderr)
	}
	var tasks []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &tasks); err != nil {
		t.Fatalf("decode list: %v; output = %s", err, stdout)
	}
	return tasks
}

func TestAnInvalidDueDateIsAValidationErrorAndWritesNothing(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	existing := cliCreateTask(t, repository, "Existing")
	head := cliGitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+existing.ID)
	for _, args := range [][]string{
		{"create", "Bad date", "--due", "2026-02-30", "--no-sync", "--json"},
		{"create", "Bad date", "--due", "next week", "--no-sync", "--json"},
		{"create", "Bad date", "--due", "", "--no-sync", "--json"},
		{"update", existing.ID, "--due", "2026-13-01", "--no-sync", "--json"},
		{"update", existing.ID, "--due", "+1w", "--no-sync", "--json"},
		{"list", "--due-before", "someday", "--json"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 5 {
			t.Fatalf("%v = (%d, %q, %q), want exit 5", args, code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "")
	}
	if refs := taskRefs(t, repository); len(refs) != 1 {
		t.Fatalf("task refs = %v, want only the existing task", refs)
	}
	if got := cliGitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+existing.ID); got != head {
		t.Fatalf("an invalid date moved %s from %s to %s", existing.ID, head, got)
	}

	code, stdout, stderr := run(t, repository, "update", existing.ID, "--due", "2030-01-01", "--clear-due", "--json")
	if code != 2 {
		t.Fatalf("--due with --clear-due = (%d, %q, %q), want exit 2", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, "cannot use --due with --clear-due")
}

func TestListFiltersOverdueAndDueBeforeAndSortsByDue(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	create := func(title string, args ...string) core.Task {
		t.Helper()
		code, stdout, stderr := run(t, repository, append([]string{"create", title, "--no-sync", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("create %s = (%d, %q, %q)", title, code, stdout, stderr)
		}
		return decodeMutationTask(t, stdout, "create")
	}
	late := create("Past due", "--due", "2020-01-01")
	soon := create("Soon", "--due", "today")
	later := create("Later", "--due", "+30d")
	undated := create("Undated")
	finished := create("Finished late", "--due", "2019-06-01")
	compactRun(t, repository, "update", finished.ID, "--status", "done", "--no-sync")

	overdue := listJSON(t, repository, "--overdue")
	if len(overdue) != 1 || overdue[0].ID != late.ID || !overdue[0].Overdue {
		t.Fatalf("list --overdue = %+v, want only %s, flagged", overdue, late.ID)
	}
	before := listJSON(t, repository, "--due-before", "2021-01-01")
	ids := map[string]bool{}
	for _, task := range before {
		ids[task.ID] = true
	}
	if len(before) != 2 || !ids[late.ID] || !ids[finished.ID] {
		t.Fatalf("list --due-before 2021-01-01 = %+v, want the two tasks due earlier", before)
	}

	sorted := listJSON(t, repository, "--sort", "due")
	var order []string
	for _, task := range sorted {
		order = append(order, task.ID)
	}
	want := []string{finished.ID, late.ID, soon.ID, later.ID, undated.ID}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("list --sort due = %v, want %v", order, want)
	}
	for _, task := range sorted {
		switch task.ID {
		case soon.ID:
			if !task.DueSoon || task.Overdue {
				t.Fatalf("today's task = %+v, want due soon", task)
			}
		case finished.ID, later.ID, undated.ID:
			if task.DueSoon || task.Overdue {
				t.Fatalf("%s = %+v, want neither flag", task.Title, task)
			}
		}
	}

	text := compactRun(t, repository, "list", "--sort", "due")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if !strings.Contains(lines[0], "DUE") {
		t.Fatalf("list header = %q, want a DUE column", lines[0])
	}
	for _, line := range lines[1:] {
		switch {
		case strings.Contains(line, "Past due"):
			if !strings.Contains(line, "2020-01-01 overdue") {
				t.Fatalf("overdue row = %q, want the word overdue beside the date", line)
			}
		case strings.Contains(line, "Soon"):
			if !strings.Contains(line, "due soon") {
				t.Fatalf("due-soon row = %q, want the words due soon", line)
			}
		case strings.Contains(line, "Finished"):
			if strings.Contains(line, "overdue") || strings.Contains(line, "due soon") {
				t.Fatalf("done row = %q, want no due state", line)
			}
		}
	}

	code, stdout, stderr := run(t, repository, "list", "--sort", "", "--json")
	if code != 2 {
		t.Fatalf("list --sort '' = (%d, %q, %q), want exit 2", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "list", "--sort", "priority", "--json")
	if code != 5 {
		t.Fatalf("list --sort priority = (%d, %q, %q), want exit 5", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "unknown list order \"priority\"; the orders are: due")
}

func TestAListOfUndatedTasksHasNoDueColumn(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Undated")
	if text := compactRun(t, repository, "list"); strings.Contains(text, "DUE") {
		t.Fatalf("list of undated tasks = %q, want no DUE column", text)
	}
}

// Concurrent due-date edits follow the rule every scalar field does: the clone
// that synchronizes last replays its edit onto the other's, so its date stands,
// and no conflict is raised. The same date written from both is no conflict
// either. Synchronizing again, and again, delivers nothing twice.
func TestConcurrentDueEditsFromTwoClonesFollowTheScalarRule(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	code, stdout, stderr := run(t, first, "create", "Shared deadline", "--due", "2030-03-01", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("first sync = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "sync"); code != 0 {
		t.Fatalf("second sync = %d; stderr = %q", code, stderr)
	}

	// Different dates: the second clone synchronizes last, so its date stands.
	if code, _, stderr := run(t, first, "update", task.ID, "--due", "2030-04-01", "--no-sync"); code != 0 {
		t.Fatalf("first update = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("first push = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "update", task.ID, "--due", "2030-05-01", "--no-sync"); code != 0 {
		t.Fatalf("second update = %d; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 {
		t.Fatalf("second sync over a concurrent due edit = (%d, %q, %q), want 0", code, stdout, stderr)
	}
	if document := assertJSONResult(t, stdout, "sync"); len(document.Conflict) != 0 {
		t.Fatalf("conflicts = %#v, want none", document.Conflict)
	}
	if code, _, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("first sync after = %d; stderr = %q", code, stderr)
	}
	for _, clone := range []string{first, second} {
		if shown := cliShowTask(t, clone, task.ID); reportedDue(shown) != "2030-05-01" {
			t.Fatalf("due = %q, want the last syncer's 2030-05-01", reportedDue(shown))
		}
	}

	// The same date from both clones.
	for _, clone := range []string{first, second} {
		if code, _, stderr := run(t, clone, "update", task.ID, "--due", "2030-06-15", "--no-sync"); code != 0 {
			t.Fatalf("update to the same date = %d; stderr = %q", code, stderr)
		}
	}
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("first push of the same date = %d; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 {
		t.Fatalf("sync of the same date = (%d, %q, %q), want 0", code, stdout, stderr)
	}
	if document := assertJSONResult(t, stdout, "sync"); len(document.Conflict) != 0 {
		t.Fatalf("conflicts = %#v, want none for the same date", document.Conflict)
	}

	// Duplicate delivery: every further round is a no-op.
	for round := 0; round < 3; round++ {
		for _, clone := range []string{first, second} {
			if code, _, stderr := run(t, clone, "sync"); code != 0 {
				t.Fatalf("round %d sync = %d; stderr = %q", round, code, stderr)
			}
		}
	}
	requireEachOperationOnce(t, first)
	requireEachOperationOnce(t, second)
	if a, b := cliGitOutput(t, first, "rev-parse", "refs/workbook/tasks/"+task.ID),
		cliGitOutput(t, second, "rev-parse", "refs/workbook/tasks/"+task.ID); a != b {
		t.Fatalf("clones disagree on the head: %s and %s", a, b)
	}
	for _, clone := range []string{first, second} {
		if shown := cliShowTask(t, clone, task.ID); reportedDue(shown) != "2030-06-15" {
			t.Fatalf("due = %q, want 2030-06-15", reportedDue(shown))
		}
	}
}

func TestCompactionAndARebuildKeepTheDueDate(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Shipped", "--due", "2030-07-04", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	compactRun(t, repository, "update", task.ID, "--due", "2030-07-05", "--no-sync")
	compactRun(t, repository, "update", task.ID, "--status", "done", "--no-sync")

	summary, _ := compactJSON(t, repository, "--no-sync", "--no-backup")
	if got := compactedTaskIDs(summary); len(got) != 1 || got[0] != task.ID {
		t.Fatalf("compacted = %v, want %s", got, task.ID)
	}
	assertParentless(t, repository, task.ID)
	values, readers := dueOperations(t, repository, task.ID)
	if len(values) != 1 || values[0] != "2030-07-05" || readers[0] != core.SupportedFormatGeneration {
		t.Fatalf("checkpoint due = %q at minReader %v, want 2030-07-05 at %d", values, readers, core.SupportedFormatGeneration)
	}
	if shown := cliShowTask(t, repository, task.ID); reportedDue(shown) != "2030-07-05" || shown.Overdue || shown.DueSoon {
		t.Fatalf("compacted task = %+v, want its date and no due state", shown)
	}

	// The cache goes; the date comes back from the refs alone.
	cache := filepath.Join(repository, ".git", "workbook")
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("read the cache directory: %v", err)
	}
	removed := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "cache.sqlite") {
			if err := os.Remove(filepath.Join(cache, entry.Name())); err != nil {
				t.Fatal(err)
			}
			removed++
		}
	}
	if removed == 0 {
		t.Fatalf("no projection cache under %s: %v", cache, entries)
	}
	if shown := cliShowTask(t, repository, task.ID); reportedDue(shown) != "2030-07-05" {
		t.Fatalf("due after deleting the cache = %q", reportedDue(shown))
	}
	compactRun(t, repository, "rebuild")
	if shown := cliShowTask(t, repository, task.ID); reportedDue(shown) != "2030-07-05" {
		t.Fatalf("due after a rebuild = %q", reportedDue(shown))
	}
	if code, stdout, stderr := run(t, repository, "validate", "--full", "--json"); code != 0 {
		t.Fatalf("validate after compaction = (%d, %q, %q)", code, stdout, stderr)
	}
}

// A due-date edit that reached the old place after the tasks moved is carried
// once, compared by content like any other operation.
func TestMoveTasksCarriesADueDateStragglerOnce(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	edited := project.ids[0]
	straggler(t, teammate, edited, "--due", "2031-05-06")
	project.moveTasks(t)
	// The new place edits the task first, so the teammate's commit cannot be
	// taken as it is: its operation is rewritten onto the seed's edit, and
	// every later run has to recognize it by content under another commit.
	if code, _, stderr := run(t, project.seed, "update", edited, "--title", "Retitled at the new place"); code != 0 {
		t.Fatalf("seed update = %d; stderr = %q", code, stderr)
	}
	publishStraggler(t, teammate, edited)

	first := moveTasksAgain(t, project, 0)
	if first.Stragglers == nil || first.Stragglers.Operations != 1 || first.Carried != 1 {
		t.Fatalf("first rerun = %+v, want the teammate's due edit carried", first)
	}
	totals := requireEachOperationOnce(t, project.tasks)
	for round := 0; round < 2; round++ {
		if again := moveTasksAgain(t, project, 0); again.Stragglers != nil || len(again.SetAside) != 0 {
			t.Fatalf("rerun %d = %+v, want nothing carried again", round, again)
		}
	}
	if got := requireEachOperationOnce(t, project.tasks); len(got) != len(totals) || got[edited] != totals[edited] {
		t.Fatalf("operations after the reruns = %v, want %v", got, totals)
	}
	commits := taskCommitCount(t, project.tasks, edited)
	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup after the pull = (%d, %q, %q)", code, stdout, stderr)
	}
	for round := 0; round < 2; round++ {
		if code, stdout, stderr := run(t, teammate, "sync"); code != 0 {
			t.Fatalf("teammate sync %d = (%d, %q, %q)", round, code, stdout, stderr)
		}
	}
	if got := taskCommitCount(t, project.tasks, edited); got != commits {
		t.Fatalf("tasks holds %d commits for %s after the teammate synchronized, want %d", got, edited, commits)
	}
	if shown := cliShowTask(t, teammate, edited); reportedDue(shown) != "2031-05-06" || shown.Title != "Retitled at the new place" {
		t.Fatalf("teammate's task = %+v, want both edits", shown)
	}
	if shown := cliShowTask(t, project.seed, edited); reportedDue(shown) != "2031-05-06" {
		t.Fatalf("due at the new place = %q, want the carried 2031-05-06", reportedDue(shown))
	}
	requireFreshCloneValidates(t, project.tasks)
}

// A migration recording its past with WORKBOOK_NOW resolves a relative date
// against the day it is recording, because that date is written into history.
// Whether the task is overdue is a reading of the real today, so the task the
// migration dated in 2001 reads as overdue now.
func TestWorkbookNowMovesWhatARelativeDueDateMeansButNotOverdue(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := testrepo.New(t)
	const recordedAt = "2001-02-03T12:00:00Z"
	mustRunBinaryAt(t, binary, repository, recordedAt, "setup", "--key", "WB", "--no-sync")
	var created struct {
		Data struct {
			ID      string  `json:"id"`
			Due     *string `json:"due"`
			Overdue bool    `json:"overdue"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinaryAt(t, binary, repository, recordedAt, "create", "Imported deadline", "--due", "+1d", "--no-sync", "--json"), &created)
	want := core.LocalDate(time.Date(2001, time.February, 4, 12, 0, 0, 0, time.UTC))
	if created.Data.Due == nil || *created.Data.Due != want {
		t.Fatalf("due = %v, want %s: the day after the recorded day", created.Data.Due, want)
	}
	if !created.Data.Overdue {
		t.Fatal("a task due in 2001 is not overdue today")
	}
}
