package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// gitPushThroughHook runs a real `git push origin main` in clone with the
// built workbook first on PATH, so the installed hook runs the real command
// under the system's sh.
func gitPushThroughHook(t *testing.T, binary, clone string, branch ...string) (int, string) {
	t.Helper()
	target := "main"
	if len(branch) > 0 {
		target = branch[0]
	}
	command := exec.Command("git", "-C", clone, "push", "origin", target)
	command.Env = append(os.Environ(), "PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), string(output)
		}
		t.Fatal(err)
	}
	return 0, string(output)
}

func commitCode(t *testing.T, clone, name string) string {
	t.Helper()
	writeCodeFile(t, clone, name, "package main\n")
	cliGit(t, clone, "add", name)
	cliGit(t, clone, "commit", "--quiet", "-m", "Add "+name)
	return strings.TrimSpace(cliGitOutput(t, clone, "rev-parse", "HEAD"))
}

// The managed pre-push hook never stops a code push because of where the
// tasks live: in a checkout that has not picked up a move, and in one with
// no access to the tasks repository, the code reaches origin and the hook
// says why the tasks were not published and what to run. A failure that is
// not about where the tasks live still stops it, as before.
func TestPrePushHookLetsCodePushesThroughTasksRemoteRefusals(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	project := codeProject(t, testrepo.FormatSHA1)
	stale := cloneCode(t, project)
	if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, _, stderr := run(t, stale, "hooks", "install"); code != 0 {
		t.Fatalf("hooks install = %d; stderr = %q", code, stderr)
	}
	project.moveTasks(t)

	t.Run("stale after the move", func(t *testing.T) {
		// Not pulled yet: the checkout still reads origin, which is marked.
		cliGit(t, stale, "checkout", "--quiet", "-b", "stale-side")
		head := commitCode(t, stale, "stale_side.go")
		code, output := gitPushThroughHook(t, binary, stale, "stale-side")
		if code != 0 {
			t.Fatalf("git push = %d, want the code pushed; output:\n%s", code, output)
		}
		if !strings.Contains(output, "the code push goes ahead") || !strings.Contains(output, project.tasks) {
			t.Fatalf("hook output lacks the moved notice:\n%s", output)
		}
		if got := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/heads/stale-side")); got != head {
			t.Fatalf("origin stale-side = %s, want %s", got, head)
		}
		cliGit(t, stale, "checkout", "--quiet", "main")
	})

	t.Run("pulled and not set up", func(t *testing.T) {
		cliGit(t, stale, "pull", "--quiet", "--ff-only", "origin", "main")
		head := commitCode(t, stale, "after_pull.go")
		code, output := gitPushThroughHook(t, binary, stale)
		if code != 0 {
			t.Fatalf("git push = %d, want the code pushed; output:\n%s", code, output)
		}
		if !strings.Contains(output, "the code push goes ahead") || !strings.Contains(output, "workbook setup") {
			t.Fatalf("hook output lacks the notice and the command:\n%s", output)
		}
		if got := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/heads/main")); got != head {
			t.Fatalf("origin main = %s, want %s", got, head)
		}
	})

	t.Run("no access to the tasks repository", func(t *testing.T) {
		if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
			t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
		}
		cliGit(t, stale, "remote", "set-url", "tasks", filepath.Join(t.TempDir(), "no-access.git"))
		if code, _, stderr := run(t, stale, "update", project.ids[0], "--title", "Kept here", "--no-sync"); code != 0 {
			t.Fatalf("update = %d; stderr = %q", code, stderr)
		}
		head := commitCode(t, stale, "no_access.go")
		code, output := gitPushThroughHook(t, binary, stale)
		if code != 0 {
			t.Fatalf("git push = %d, want the code pushed; output:\n%s", code, output)
		}
		if !strings.Contains(output, "no-access.git") || !strings.Contains(output, "the code push goes ahead") {
			t.Fatalf("hook output lacks the repository and the notice:\n%s", output)
		}
		if got := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/heads/main")); got != head {
			t.Fatalf("origin main = %s, want %s", got, head)
		}
		cliGit(t, stale, "remote", "set-url", "tasks", project.tasks)
	})

	t.Run("other failures still stop it", func(t *testing.T) {
		// A task ref this checkout cannot read is not about where the tasks
		// live, and the hook stops the push as it always has.
		task := cliCreateTask(t, stale, "Broken before the push")
		blobFile := filepath.Join(t.TempDir(), "blob")
		if err := os.WriteFile(blobFile, []byte("not a task commit"), 0o644); err != nil {
			t.Fatal(err)
		}
		blob := strings.TrimSpace(cliGitOutput(t, stale, "hash-object", "-w", blobFile))
		cliGit(t, stale, "update-ref", "refs/workbook/tasks/"+task.ID, blob)
		before := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/heads/main"))
		commitCode(t, stale, "blocked.go")
		code, output := gitPushThroughHook(t, binary, stale)
		if code == 0 {
			t.Fatalf("git push with an unreadable task ref succeeded; output:\n%s", output)
		}
		if strings.Contains(output, "the code push goes ahead") {
			t.Fatalf("hook let an unrelated failure through:\n%s", output)
		}
		if got := strings.TrimSpace(cliGitOutput(t, project.code, "rev-parse", "refs/heads/main")); got != before {
			t.Fatalf("origin main moved to %s despite the hook", got)
		}
	})
}
