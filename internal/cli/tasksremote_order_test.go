package cli

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// Committing the change of tasks-remote before running move-tasks is safe:
// a teammate who pulls it before the move ran is refused at the place it
// names, still marked, and told to have move-tasks run, in text and JSON,
// from every command and from setup, since pulling and running setup cannot
// help. Nothing is published anywhere meanwhile, and once the move is run the
// teammate's setup works with nothing more pulled.
func TestTasksRemoteSettingsCommittedBeforeTheMoveSayToRunMoveTasks(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	project.moveTasks(t)
	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	compactRun(t, teammate, "setup")

	if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
		t.Fatalf("config unset = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, project.seed, "add", ".workbook/config.json")
	cliGit(t, project.seed, "commit", "--quiet", "-m", "Move the tasks back")
	cliGit(t, project.seed, "push", "--quiet", "origin", "main")
	cliGit(t, teammate, "pull", "--quiet", "--ff-only", "origin", "main")
	codeBefore, tasksBefore := repositoryState(t, project.code), repositoryState(t, project.tasks)

	for _, args := range [][]string{
		{"update", project.ids[0], "--title", "Edited before the move ran", "--json"},
		{"setup", "--json"},
	} {
		code, _, stderr := run(t, teammate, args...)
		if code != 3 {
			t.Fatalf("%v = %d, want 3; stderr = %q", args, code, stderr)
		}
		refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
		if refusal.Remote != "origin" || refusal.MovedTo == nil || refusal.MovedTo.Remote != "tasks" ||
			refusal.Command != "workbook move-tasks" {
			t.Fatalf("%v refusal = %+v, want workbook move-tasks", args, refusal)
		}
	}
	code, _, stderr := run(t, teammate, "update", project.ids[0], "--title", "Edited before the move ran")
	if code != 3 {
		t.Fatalf("update = %d, want 3; stderr = %q", code, stderr)
	}
	for _, want := range []string{"the move it is for has not been run", "Pulling and running setup cannot finish it", "runs workbook move-tasks"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("refusal %q lacks %q", stderr, want)
		}
	}
	if got := repositoryState(t, project.code); got != codeBefore {
		t.Fatalf("origin changed before the move ran:\nbefore %s\nafter  %s", codeBefore, got)
	}
	if got := repositoryState(t, project.tasks); got != tasksBefore {
		t.Fatalf("tasks changed before the move ran:\nbefore %s\nafter  %s", tasksBefore, got)
	}

	if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 ||
		!strings.Contains(stdout, "Returned:\torigin\t(the tasks are back at a place they left") {
		t.Fatalf("move-tasks = (%d, %q, %q), want it to say origin was returned to", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 ||
		!strings.Contains(stdout, "Returned:\torigin\t(already recorded by an earlier run)") {
		t.Fatalf("move-tasks again = (%d, %q, %q), want it to say the return was recorded already", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("setup after the move = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, teammate, "update", project.ids[0], "--title", "Edited after the move"); code != 0 {
		t.Fatalf("update after the move = (%d, %q, %q)", code, stdout, stderr)
	}
	if got, want := tasksTip(t, project.code, project.ids[0]), localTaskRef(t, teammate, project.ids[0]); got != want {
		t.Fatalf("origin holds %s at %s, want the teammate's %s", project.ids[0], got, want)
	}
}

// After a route through three places and back, origin to tasks to third to
// origin, each move run before its settings were committed and pushed, a
// checkout or a fresh clone left on any older generation of the settings is
// told to pull and run setup, never to run move-tasks: the place its settings
// name is one the tasks were moved to, so its settings may be stale, and
// running move-tasks there would undo the finished move.
func TestTasksRemoteOlderGenerationsAfterAThreePlaceRouteAreToldToSetUp(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	third := newBareRepository(t, testrepo.FormatSHA1)
	generations := map[string]string{"origin": strings.TrimSpace(cliGitOutput(t, project.seed, "rev-parse", "HEAD"))}
	checkouts := map[string]string{}
	for _, step := range []struct{ name, url string }{{"tasks", project.tasks}, {"third", third}, {"origin", ""}} {
		args := []string{"config", "unset", "tasks-remote"}
		if step.url != "" {
			args = []string{"config", "set", "tasks-remote", step.name, "--url", step.url}
		}
		if code, stdout, stderr := run(t, project.seed, args...); code != 0 {
			t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
		}
		if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 {
			t.Fatalf("move-tasks to %s = (%d, %q, %q)", step.name, code, stdout, stderr)
		}
		cliGit(t, project.seed, "add", ".workbook/config.json")
		cliGit(t, project.seed, "commit", "--quiet", "-m", "Move the tasks to "+step.name)
		cliGit(t, project.seed, "push", "--quiet", "origin", "main")
		if step.url == "" {
			break
		}
		generations[step.name] = strings.TrimSpace(cliGitOutput(t, project.seed, "rev-parse", "HEAD"))
		checkout := cloneCode(t, project)
		compactRun(t, checkout, "setup")
		checkouts[step.name] = checkout
	}

	for _, name := range []string{"tasks", "third"} {
		code, _, stderr := run(t, checkouts[name], "update", project.ids[0], "--title", "Edited on "+name+"'s settings", "--json")
		requireToldToSetUp(t, "checkout on "+name+"'s settings", code, stderr, name, project.code)
		code, _, stderr = run(t, checkouts[name], "update", project.ids[0], "--title", "Edited on "+name+"'s settings")
		if code != 3 || !strings.Contains(stderr, "then run workbook setup") || strings.Contains(stderr, "has not been run") {
			t.Fatalf("checkout on %s's settings = (%d, %q), want pull and run setup", name, code, stderr)
		}
	}
	for _, name := range []string{"origin", "tasks", "third"} {
		fresh := cloneCode(t, project)
		cliGit(t, fresh, "reset", "--quiet", "--hard", generations[name])
		code, stdout, stderr := run(t, fresh, "setup", "--json")
		if name == "origin" {
			// Origin's own settings name the place the tasks are now.
			if code != 0 {
				t.Fatalf("fresh clone on origin's first settings: setup = (%d, %q, %q)", code, stdout, stderr)
			}
			continue
		}
		requireToldToSetUp(t, "fresh clone on "+name+"'s settings", code, stderr, name, project.code)
	}
}

// requireToldToSetUp requires the moved refusal at remote, naming origin as
// where the tasks are and workbook setup as the command.
func requireToldToSetUp(t *testing.T, who string, code int, stderr, remote, origin string) {
	t.Helper()
	if code != 3 {
		t.Fatalf("%s = %d, want 3; stderr = %q", who, code, stderr)
	}
	refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
	if refusal.Remote != remote || refusal.MovedTo == nil || refusal.MovedTo.URL != origin ||
		refusal.Command != "workbook setup" {
		t.Fatalf("%s refusal = %+v, want moved to origin and workbook setup", who, refusal)
	}
}
