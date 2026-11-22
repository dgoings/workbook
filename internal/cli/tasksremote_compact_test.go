package cli

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

func tasksTip(t *testing.T, bare, taskID string) string {
	t.Helper()
	return strings.TrimSpace(cliGitOutputAllowingMissing(t, bare, "refs/workbook/tasks/"+taskID))
}

// Compaction runs against the project's tasks remote: a compaction from a
// code clone and one from a tasks-only folder are published there and nowhere
// else, a teammate whose unpushed edit conflicts with one is told so under
// the remote's name, and compact --adopt then takes the checkpoint from there.
func TestTasksRemoteCompactionAndAdoptionUseTheTasksRemote(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	project.moveTasks(t)
	teammate := cloneCode(t, project)
	compactRun(t, teammate, "setup")
	folder := t.TempDir()
	compactRun(t, folder, "setup", "--tasks-only", project.tasks)
	cliGit(t, folder, "config", "user.name", "Folder")
	cliGit(t, folder, "config", "user.email", "folder@example.test")
	afterMove := repositoryState(t, project.code)

	task := project.ids[0]
	compactRun(t, project.seed, "update", task, "--status", "done")
	compactRun(t, teammate, "sync")
	compactRun(t, teammate, "update", task, "--description", "Our text", "--no-sync")
	compactRun(t, project.seed, "update", task, "--description", "Their text")
	compactRun(t, project.seed, "compact", "--no-backup")
	checkpoint := localTaskRef(t, project.seed, task)
	if got := tasksTip(t, project.tasks, task); got != checkpoint {
		t.Fatalf("tasks remote holds %s at %s, want the checkpoint %s", task, got, checkpoint)
	}
	if parents := strings.TrimSpace(cliGitOutput(t, project.tasks, "rev-list", "--parents", "-n", "1", checkpoint)); parents != checkpoint {
		t.Fatalf("checkpoint %s has parents: %s", checkpoint, parents)
	}

	code, stdout, stderr := run(t, teammate, "sync")
	if code != 0 {
		t.Fatalf("teammate sync = (%d, %q, %q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, string(gitstore.SyncCompactedOnOrigin)) {
		t.Fatalf("teammate sync = %q, want the task kept on the old history", stdout)
	}
	if strings.Contains(stdout, "origin;") || strings.Contains(stdout, "origin's") ||
		!strings.Contains(stdout, "was compacted on tasks;") || !strings.Contains(stdout, "take tasks's version") {
		t.Fatalf("teammate sync = %q, want the tasks remote named rather than origin", stdout)
	}

	compactRun(t, project.seed, "update", task, "--description", "Our text")
	result, envelope := adoptJSON(t, teammate, task)
	if result.Status != gitstore.SyncAdoptedReplayed {
		t.Fatalf("adopt = %+v", result)
	}
	if result.Detail != "replayed 1 local operation(s) onto tasks's checkpoint" {
		t.Fatalf("adopt detail = %q, want the tasks remote named", result.Detail)
	}
	if code, stdout, stderr := run(t, teammate, "compact", "--adopt", task); code == 0 || strings.Contains(stdout+stderr, "origin") {
		t.Fatalf("a second adopt = (%d, %q, %q), want a refusal that does not name origin", code, stdout, stderr)
	}
	if envelope.Sync.Status != syncStatusCompleted {
		t.Fatalf("adopt sync = %+v", envelope.Sync)
	}
	if got := tasksTip(t, project.tasks, task); got != localTaskRef(t, teammate, task) {
		t.Fatalf("tasks remote holds %s, want the adopted tip %s", got, localTaskRef(t, teammate, task))
	}

	// A tasks-only folder compacts too, and the code clone adopts it on sync.
	other := project.ids[1]
	compactRun(t, folder, "sync")
	compactRun(t, folder, "update", other, "--status", "done")
	compactRun(t, folder, "compact", "--no-backup")
	folded := localTaskRef(t, folder, other)
	if got := tasksTip(t, project.tasks, other); got != folded {
		t.Fatalf("tasks remote holds %s at %s, want the folder's checkpoint %s", other, got, folded)
	}
	compactRun(t, project.seed, "sync")
	if got := localTaskRef(t, project.seed, other); got != folded {
		t.Fatalf("seed holds %s at %s after sync, want the checkpoint %s", other, got, folded)
	}
	if shown := cliShowTask(t, project.seed, other); shown.Status != core.StatusDone {
		t.Fatalf("seed shows %+v", shown)
	}
	requireFreshCloneValidates(t, project.tasks)
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("compaction touched origin:\nbefore %s\nafter  %s", afterMove, after)
	}
}
