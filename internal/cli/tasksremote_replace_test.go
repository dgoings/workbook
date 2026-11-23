package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// replacingMoveRun is the part of move-tasks's JSON these tests read.
type replacingMoveRun struct {
	Returned string   `json:"returned"`
	Verified bool     `json:"verified"`
	Replaced []string `json:"replaced"`
	Bundles  []string `json:"bundles"`
	SetAside []struct {
		TaskID     string   `json:"taskId"`
		Title      string   `json:"title"`
		Operations []string `json:"operations"`
		ParkedRef  string   `json:"parkedRef"`
	} `json:"setAside"`
}

// operationsAccountedFor is every operation ID a task's history in
// repository holds: each commit's operations, and every operation a
// checkpoint at its root lists as compacted.
func operationsAccountedFor(t *testing.T, repository, ref string) map[string]bool {
	t.Helper()
	held := map[string]bool{}
	for _, commit := range strings.Fields(cliGitOutput(t, repository, "rev-list", ref)) {
		var pack struct {
			Operations []struct {
				ID         string `json:"id"`
				Checkpoint *struct {
					CompactedOperations []string `json:"compactedOperations"`
				} `json:"checkpoint"`
			} `json:"operations"`
		}
		if err := json.Unmarshal([]byte(cliGitOutput(t, repository, "cat-file", "blob", commit+":operation.json")), &pack); err != nil {
			t.Fatal(err)
		}
		for _, operation := range pack.Operations {
			held[operation.ID] = true
			if operation.Checkpoint != nil {
				for _, id := range operation.Checkpoint.CompactedOperations {
					held[id] = true
				}
			}
		}
	}
	return held
}

// stalePlaceWithLateEdit moves the tasks to the tasks repository, compacts
// one task there after closing it (and deleting it, when deleted is set),
// and lands a teammate's late edit to that task on origin's stale copy, as
// an edit that raced origin's mark does. It returns the project, the task,
// and origin's stale tip.
func stalePlaceWithLateEdit(t *testing.T, deleted bool) (tasksRemoteProject, string, string) {
	t.Helper()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	compactRun(t, teammate, "setup")
	project.moveTasks(t)
	task := project.ids[0]
	compactRun(t, project.seed, "update", task, "--status", "done")
	if deleted {
		compactRun(t, project.seed, "delete", task)
	}
	compactRun(t, project.seed, "compact", "--no-backup")

	straggler(t, teammate, task, "--title", "Late edit on origin's copy")
	publishStraggler(t, teammate, task)
	stale := tasksTip(t, project.code, task)
	if stale == "" || stale == localTaskRef(t, project.seed, task) {
		t.Fatalf("origin holds %s at %q, want the stale copy with the late edit", task, stale)
	}
	if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
		t.Fatalf("config unset = (%d, %q, %q)", code, stdout, stderr)
	}
	return project, task, stale
}

// A move back that replaces a stale copy of a compacted task first writes a
// verified backup bundle of it, holding the stale tip under the tracking
// name, with the compact manifest, at compact's default location, and says
// where it is in text and JSON. Every operation the replaced commits held,
// the teammate's late edit among them, is held at origin afterwards.
func TestMoveTasksBackBacksUpEveryCopyItReplaces(t *testing.T) {
	t.Parallel()
	project, task, stale := stalePlaceWithLateEdit(t, false)
	replaced := operationsAccountedFor(t, project.code, "refs/workbook/tasks/"+task)
	if len(replaced) < 2 {
		t.Fatalf("origin's stale copy holds %d operations, want the create and the late edit", len(replaced))
	}

	code, stdout, stderr := run(t, project.seed, "move-tasks", "--json")
	if code != 0 {
		t.Fatalf("move-tasks = (%d, %q, %q)", code, stdout, stderr)
	}
	var moved replacingMoveRun
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || moved.Returned != "returned" || len(moved.Replaced) != 1 || moved.Replaced[0] != task || len(moved.Bundles) != 1 {
		t.Fatalf("move-tasks = %+v, want %s replaced after one bundle", moved, task)
	}
	bundle := moved.Bundles[0]
	commonDir := strings.TrimSpace(cliGitOutput(t, project.seed, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if filepath.Dir(bundle) != filepath.Join(commonDir, "workbook-archive") {
		t.Fatalf("bundle %s is not at compact's default location", bundle)
	}
	cliGit(t, project.seed, "bundle", "verify", "--quiet", bundle)
	heads := cliGitOutput(t, project.seed, "bundle", "list-heads", bundle)
	if !strings.Contains(heads, stale+" refs/workbook/remotes/origin/tasks/"+task) ||
		!strings.Contains(heads, "refs/workbook/archive/manifest") {
		t.Fatalf("bundle heads = %q, want origin's stale tip %s and the manifest", heads, stale)
	}

	after := operationsAccountedFor(t, project.code, "refs/workbook/tasks/"+task)
	for id := range replaced {
		if !after[id] {
			t.Errorf("origin lost operation %s of the copy it replaced", id)
		}
	}
	if shown := cliShowTask(t, project.seed, task); shown.Title != "Late edit on origin's copy" {
		t.Fatalf("the late edit did not survive: %q", shown.Title)
	}
	requireEachOperationOnce(t, project.code)

	// The text output names the bundle too, and a rerun replaces nothing
	// and writes none.
	code, stdout, stderr = run(t, project.seed, "move-tasks")
	if code != 0 || strings.Contains(stdout, "Backup:") || strings.Contains(stdout, "Replaced:") {
		t.Fatalf("move-tasks again = (%d, %q, %q)", code, stdout, stderr)
	}
}

// --no-backup writes no bundle; --backup <directory> writes it there; and
// the text output says where.
func TestMoveTasksBackBackupFlags(t *testing.T) {
	t.Parallel()
	t.Run("no-backup", func(t *testing.T) {
		t.Parallel()
		project, task, _ := stalePlaceWithLateEdit(t, false)
		code, stdout, stderr := run(t, project.seed, "move-tasks", "--no-backup", "--json")
		if code != 0 {
			t.Fatalf("move-tasks --no-backup = (%d, %q, %q)", code, stdout, stderr)
		}
		var moved replacingMoveRun
		if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
			t.Fatal(err)
		}
		if len(moved.Bundles) != 0 || len(moved.Replaced) != 1 || moved.Replaced[0] != task {
			t.Fatalf("move-tasks --no-backup = %+v", moved)
		}
	})
	t.Run("backup directory", func(t *testing.T) {
		t.Parallel()
		project, _, _ := stalePlaceWithLateEdit(t, false)
		directory := t.TempDir()
		code, stdout, stderr := run(t, project.seed, "move-tasks", "--backup", directory)
		if code != 0 {
			t.Fatalf("move-tasks --backup = (%d, %q, %q)", code, stdout, stderr)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".bundle") {
			t.Fatalf("backup directory holds %v (%v), want one bundle", entries, err)
		}
		if !strings.Contains(stdout, "Backup:\t"+filepath.Join(directory, entries[0].Name())) {
			t.Fatalf("move-tasks --backup output = %q, want the bundle's path", stdout)
		}
	})
	t.Run("both", func(t *testing.T) {
		t.Parallel()
		code, _, stderr := run(t, t.TempDir(), "move-tasks", "--no-backup", "--backup", t.TempDir())
		if code != 2 || !strings.Contains(stderr, "cannot be combined") {
			t.Fatalf("move-tasks --no-backup --backup = (%d, %q)", code, stderr)
		}
	})
}
