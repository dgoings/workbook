package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// divergedKeyProject is a project whose mover recorded a configuration change
// that never reached origin, while a teammate, an ephemeral agent's clone,
// added a key there and created a task and a relic under it before
// disappearing: the two configuration ledgers have diverged, and the key
// exists only in origin's.
func divergedKeyProject(t *testing.T, teammateChange []string) (tasksRemoteProject, string, string) {
	t.Helper()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	mustRunKey(t, project.seed, "key", "add", "DD", "--no-sync")
	mustRunKey(t, teammate, teammateChange...)
	var task, relic string
	if teammateChange[0] == "key" {
		task = cliCreateTask(t, teammate, "Created under the teammate's key").ID
		relic = mustCreateRelic(t, teammate, "Written under the teammate's key", "--type", "spec").Relic.ID
		if !strings.HasPrefix(task, "CC-") || !strings.HasPrefix(relic, "CC-R-") {
			t.Fatalf("teammate's task %s and relic %s, want both under CC", task, relic)
		}
	}
	if code, stdout, stderr := run(t, teammate, "push"); code != 0 {
		t.Fatalf("teammate push = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	return project, task, relic
}

// A key the old place's own configuration has is this project's key, though
// this checkout has not seen it because the two configurations diverged. The
// task and relic under it are this project's refs that the new place lacks,
// so the move stays unverified with exit 8, names the key and says how to get
// it here, and --clean-up deletes nothing at the old place, its configuration
// ref included. Following the advice carries both, and the next run verifies
// and cleans up.
func TestMoveTasksStaysUnverifiedForAKeyOnlyTheOldPlacesConfigurationHas(t *testing.T) {
	t.Parallel()
	project, task, relic := divergedKeyProject(t, []string{"key", "add", "CC", "--current"})
	refs := []string{"refs/workbook/tasks/" + task, relicRef(relic), "refs/workbook/config"}
	before := workbookRefTips(t, project.code)
	for _, ref := range refs {
		if before[ref] == "" {
			t.Fatalf("origin does not hold %s before the move: %v", ref, before)
		}
	}
	advice := "run workbook key add CC here, then workbook move-tasks again"
	for _, args := range [][]string{
		{"move-tasks", "--json"},
		{"move-tasks"},
		{"move-tasks", "--clean-up", "--json"},
		{"move-tasks", "--clean-up"},
	} {
		code, stdout, stderr := run(t, project.seed, args...)
		if code != 8 {
			t.Fatalf("%v = (%d, %q, %q), want exit 8", args, code, stdout, stderr)
		}
		for _, want := range []string{task + " (absent)", "relic " + relic, "project key CC", advice} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%v stderr = %q, want it to contain %q", args, stderr, want)
			}
		}
		held := workbookRefTips(t, project.code)
		for _, ref := range refs {
			if held[ref] != before[ref] {
				t.Fatalf("origin holds %s at %q after %v, want %q", ref, held[ref], args, before[ref])
			}
		}
		if got := refsUnder(t, project.tasks, "refs/workbook/tasks/"+task); got != "" {
			t.Fatalf("the new place holds %s after %v", task, args)
		}
	}

	mustRunKey(t, project.seed, "key", "add", "CC")
	code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 0 {
		t.Fatalf("move-tasks --clean-up after key add CC = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved moveTasksSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || moved.Tasks != len(project.ids)+1 || moved.Relics != 1 || moved.CleanUp == nil ||
		moved.CleanUp.Tasks != len(project.ids)+1 {
		t.Fatalf("move-tasks --clean-up = %+v, cleanUp %+v; want %d tasks and 1 relic", moved.MoveTasksResult, moved.CleanUp, len(project.ids)+1)
	}
	carried := workbookRefTips(t, project.tasks)
	for _, ref := range refs[:2] {
		if carried[ref] != before[ref] {
			t.Fatalf("the new place holds %s at %q, want %q", ref, carried[ref], before[ref])
		}
	}
	if got := workbookRefTips(t, project.code); len(got) != 1 || got["refs/workbook/project"] == "" {
		t.Fatalf("origin holds after the clean-up: %v, want only its marker", got)
	}
	if shown := cliShowTask(t, project.seed, task); shown.Title != "Created under the teammate's key" {
		t.Fatalf("the carried task = %+v", shown)
	}
}

// A diverged configuration with no key this checkout lacks leaves nothing
// unowned: the move verifies, reports the configuration not carried, and
// --clean-up finishes, as before.
func TestMoveTasksVerifiesADivergedConfigurationWithNoNewKey(t *testing.T) {
	t.Parallel()
	project, _, _ := divergedKeyProject(t, []string{"status", "add", "icebox"})
	code, stdout, stderr := run(t, project.seed, "move-tasks")
	if code != 0 || !strings.Contains(stdout, "Config:\tnot carried") {
		t.Fatalf("move-tasks = (%d, %q, %q), want exit 0 and the configuration not carried", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 0 {
		t.Fatalf("move-tasks --clean-up = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved moveTasksSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || moved.Config != "not-carried" || moved.Tasks != len(project.ids) || moved.CleanUp == nil ||
		!reflect.DeepEqual(moved.CleanUp.Kept, []string{"refs/workbook/project"}) {
		t.Fatalf("move-tasks --clean-up = %+v, cleanUp %+v", moved.MoveTasksResult, moved.CleanUp)
	}
}
