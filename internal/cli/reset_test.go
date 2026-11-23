package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// refsOutsideWorkbook is every ref a repository holds outside refs/workbook/,
// by name: what no reset may ever change.
func refsOutsideWorkbook(t *testing.T, repository string) map[string]string {
	t.Helper()
	outside := map[string]string{}
	for name, tip := range refTips(t, repository) {
		if !strings.HasPrefix(name, "refs/workbook/") {
			outside[name] = tip
		}
	}
	return outside
}

func requireSameRefs(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s changed:\ngot  %v\nwant %v", label, got, want)
	}
}

func resetJSON(t *testing.T, cwd string, args ...string) resetSummary {
	t.Helper()
	code, stdout, stderr := run(t, cwd, append([]string{"reset", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("reset %v = (%d, %q, %q)", args, code, stdout, stderr)
	}
	var summary resetSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "reset").Data, &summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

// resetRefusalJSON runs a reset that must be refused with exit code want and
// returns the reset member of its error envelope.
func resetRefusalJSON(t *testing.T, cwd string, want int, args ...string) (string, gitstore.ResetRefusal) {
	t.Helper()
	code, stdout, stderr := run(t, cwd, append([]string{"reset", "--json"}, args...)...)
	if code != want || stdout != "" {
		t.Fatalf("reset %v = (%d, %q, %q), want exit %d", args, code, stdout, stderr, want)
	}
	var document struct {
		Error struct {
			Message string                 `json:"message"`
			Reset   *gitstore.ResetRefusal `json:"reset"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &document); err != nil || document.Error.Reset == nil {
		t.Fatalf("reset %v error = %q, want a reset refusal", args, stderr)
	}
	return document.Error.Message, *document.Error.Reset
}

// A checkout whose Workbook state is damaged three ways — a corrupt
// projection, a local task ref that names something other than a commit, and
// tracking refs that say origin holds what it does not — is reset, set up and
// synchronized, and then holds exactly the refs a fresh clone does. Its
// tracked settings and every ref outside refs/workbook/ are left alone, here
// and at origin.
func TestResetRebuildsADamagedCheckoutToMatchAFreshClone(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			testrepo.RequireObjectFormat(t, objectFormat)
			project := codeProject(t, objectFormat)
			clone := cloneCode(t, project)
			if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}
			commonDir := filepath.Join(clone, ".git")
			if err := os.WriteFile(filepath.Join(commonDir, "workbook", "cache.sqlite"), []byte("not a database"), 0o644); err != nil {
				t.Fatal(err)
			}
			blob := cliGitOutputInput(t, clone, "not a commit\n", "hash-object", "-w", "--stdin")
			cliGit(t, clone, "update-ref", "refs/workbook/tasks/"+project.ids[0], blob)
			cliGit(t, clone, "update-ref", "refs/workbook/remotes/origin/tasks/"+project.ids[1], localTaskRef(t, project.seed, project.ids[0]))
			cliGit(t, clone, "update-ref", "refs/workbook/remotes/origin/tasks/WB-01M00000000000000000000000",
				localTaskRef(t, project.seed, project.ids[1]))
			if err := os.WriteFile(filepath.Join(clone, ".workbook", "stray.cache"), []byte("untracked"), 0o644); err != nil {
				t.Fatal(err)
			}
			if code, _, _ := run(t, clone, "list"); code == 0 {
				t.Fatal("list succeeded on the damaged checkout; the damage did not take")
			}
			outside, originBefore := refsOutsideWorkbook(t, clone), refTips(t, project.code)
			trackedBefore, statusBefore := cliGitOutput(t, clone, "ls-files", "-s", ".workbook"), cliGitOutput(t, clone, "status", "--porcelain")

			summary := resetJSON(t, clone)
			damaged := map[string]string{}
			for _, ref := range summary.Refs {
				if ref.Damaged != "" {
					damaged[ref.Ref] = ref.Damaged
				}
			}
			if !reflect.DeepEqual(damaged, map[string]string{"refs/workbook/tasks/" + project.ids[0]: "it names a blob, not a commit"}) {
				t.Fatalf("damaged refs = %v", damaged)
			}
			if summary.Bundle == "" || summary.Restore == "" {
				t.Fatalf("reset wrote no bundle: %+v", summary)
			}
			if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
				t.Fatalf("refs left after the reset:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(commonDir, "workbook")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the cache directory is still there: %v", err)
			}
			if _, err := os.Stat(filepath.Join(clone, ".workbook", "stray.cache")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the untracked file under .workbook is still there: %v", err)
			}
			if got := cliGitOutput(t, clone, "ls-files", "-s", ".workbook"); got != trackedBefore {
				t.Fatalf("tracked files under .workbook changed:\n%s\nwant\n%s", got, trackedBefore)
			}
			statusBefore = strings.TrimSpace(strings.Replace(statusBefore, "?? .workbook/stray.cache", "", 1))
			if status := cliGitOutput(t, clone, "status", "--porcelain"); status != statusBefore {
				t.Fatalf("the working tree changed:\n%s\nwant\n%s", status, statusBefore)
			}

			for _, command := range [][]string{{"setup"}, {"sync"}} {
				if code, stdout, stderr := run(t, clone, command...); code != 0 {
					t.Fatalf("%v after the reset = (%d, %q, %q)", command, code, stdout, stderr)
				}
			}
			fresh := cloneCode(t, project)
			if code, stdout, stderr := run(t, fresh, "setup"); code != 0 {
				t.Fatalf("fresh setup = (%d, %q, %q)", code, stdout, stderr)
			}
			if got, want := refsUnder(t, clone, "refs/workbook/"), refsUnder(t, fresh, "refs/workbook/"); got != want || want == "" {
				t.Fatalf("the reset checkout holds\n%s\nwant what a fresh clone holds\n%s", got, want)
			}
			requireSameRefs(t, "the checkout's refs outside refs/workbook/", refsOutsideWorkbook(t, clone), outside)
			requireSameRefs(t, "origin's refs", refTips(t, project.code), originBefore)
		})
	}
}

func cliGitOutputInput(t *testing.T, directory, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// A checkout holding an edit and a task its tasks remote lacks, and a parked
// set-aside edit, is refused under --no-backup with all three listed and
// nothing changed. Reset with its bundle, it lists them again, and fetching
// the bundle back before setting up brings every one of those commits back,
// so the next sync publishes them.
func TestResetListsUnpublishedWorkAndItsBundleBringsItBack(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			testrepo.RequireObjectFormat(t, objectFormat)
			project := codeProject(t, objectFormat)
			clone := cloneCode(t, project)
			if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}
			// A task published by the clone's own create, which no fetch has
			// recorded in its tracking refs yet, is not unpublished.
			if code, _, stderr := run(t, clone, "create", "Published by its own create"); code != 0 {
				t.Fatalf("create = %d; stderr = %q", code, stderr)
			}
			if code, _, stderr := run(t, clone, "update", project.ids[0], "--title", "Edited here only", "--no-sync"); code != 0 {
				t.Fatalf("update = %d; stderr = %q", code, stderr)
			}
			added := cliCreateTask(t, clone, "Created here only")
			parked := "refs/workbook/reconciled/" + project.ids[1] + "/1"
			cliGit(t, clone, "update-ref", parked, localTaskRef(t, clone, project.ids[1]))
			kept := map[string]string{
				"refs/workbook/tasks/" + project.ids[0]: localTaskRef(t, clone, project.ids[0]),
				"refs/workbook/tasks/" + added.ID:       added.Head,
				parked:                                  localTaskRef(t, clone, project.ids[1]),
			}
			before, originBefore, outsideBefore := refTips(t, clone), refTips(t, project.code), refsOutsideWorkbook(t, clone)

			message, refusal := resetRefusalJSON(t, clone, 5, "--no-backup")
			if refusal.Reason != gitstore.ResetUnpublished || !reflect.DeepEqual(refusal.SetAside, []string{parked}) ||
				!strings.Contains(message, "Edited here only") || !strings.Contains(message, "Created here only") {
				t.Fatalf("refusal = %q, %+v", message, refusal)
			}
			unpublished := map[string]bool{}
			for _, item := range refusal.Unpublished {
				if len(item.Operations) == 0 {
					t.Fatalf("unpublished %s lists no operations", item.Ref)
				}
				unpublished[item.TaskID] = item.New
			}
			if !reflect.DeepEqual(unpublished, map[string]bool{project.ids[0]: false, added.ID: true}) {
				t.Fatalf("unpublished = %+v", refusal.Unpublished)
			}
			if dry := resetJSON(t, clone, "--dry-run"); !dry.DryRun || len(dry.Unpublished) != 2 || dry.Bundle != "" {
				t.Fatalf("dry run = %+v", dry)
			}
			requireSameRefs(t, "the checkout's refs after the refusal and the dry run", refTips(t, clone), before)

			summary := resetJSON(t, clone)
			if len(summary.Unpublished) != 2 || summary.Bundle == "" || !strings.Contains(summary.Restore, summary.Bundle) {
				t.Fatalf("reset = %+v", summary)
			}
			if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
				t.Fatalf("refs left after the reset:\n%s", got)
			}

			restore := exec.Command("sh", "-c", summary.Restore)
			restore.Dir = clone
			if output, err := restore.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", summary.Restore, err, output)
			}
			for name, tip := range kept {
				if got := cliGitOutput(t, clone, "rev-parse", name); got != tip {
					t.Fatalf("restored %s = %s, want %s", name, got, tip)
				}
			}
			for _, command := range [][]string{{"setup"}, {"sync"}} {
				if code, stdout, stderr := run(t, clone, command...); code != 0 {
					t.Fatalf("%v after the restore = (%d, %q, %q)", command, code, stdout, stderr)
				}
			}
			for _, taskID := range []string{project.ids[0], added.ID} {
				if got, want := remoteTaskRefValue(t, clone, taskID), kept["refs/workbook/tasks/"+taskID]; got != want {
					t.Fatalf("origin holds %s at %q, want the restored %s", taskID, got, want)
				}
			}
			for name, tip := range originBefore {
				if strings.HasPrefix(name, "refs/workbook/") {
					continue
				}
				if got := refTips(t, project.code)[name]; got != tip {
					t.Fatalf("origin's %s moved from %s to %s", name, tip, got)
				}
			}
			requireSameRefs(t, "the checkout's refs outside refs/workbook/", refsOutsideWorkbook(t, clone), outsideBefore)
		})
	}
}

// After a move, move-tasks --clean-up run again finishes the move it already
// made and then removes every Workbook ref at the place left but its marker,
// writing a bundle of them first. The new place is untouched, nothing outside
// refs/workbook/ moves at either, a checkout still on the old settings is
// refused and told where the tasks went, and the tasks can move back onto the
// cleaned place.
func TestMoveTasksCleanUpLeavesOnlyTheMarkerAndTheTasksCanComeBack(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			testrepo.RequireObjectFormat(t, objectFormat)
			project := codeProject(t, objectFormat)
			stale := cloneCode(t, project)
			if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
				t.Fatalf("setup on the old settings = (%d, %q, %q)", code, stdout, stderr)
			}
			project.moveTasks(t)
			marker := cliGitOutput(t, project.code, "rev-parse", "refs/workbook/project")
			tasksBefore, codeOutside := refTips(t, project.tasks), refsOutsideWorkbook(t, project.code)
			seedOutside := refsOutsideWorkbook(t, project.seed)

			code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up", "--json")
			if code != 0 {
				t.Fatalf("move-tasks --clean-up = (%d, %q, %q)", code, stdout, stderr)
			}
			var moved moveTasksSummary
			if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
				t.Fatal(err)
			}
			cleaned := moved.CleanUp
			if !moved.Verified || cleaned == nil || cleaned.Remote != "origin" || cleaned.Tasks != len(project.ids) ||
				!reflect.DeepEqual(cleaned.Kept, []string{"refs/workbook/project"}) || cleaned.MovedTo == nil ||
				cleaned.MovedTo.URL != project.tasks || cleaned.Bundle == "" {
				t.Fatalf("move-tasks --clean-up = %+v, cleanUp %+v", moved.MoveTasksResult, cleaned)
			}
			if _, err := os.Stat(cleaned.Bundle); err != nil {
				t.Fatalf("clean-up bundle: %v", err)
			}
			if got := refsUnder(t, project.code, "refs/workbook/"); got != "refs/workbook/project "+marker {
				t.Fatalf("origin holds after the clean-up:\n%s", got)
			}
			requireSameRefs(t, "the tasks repository", refTips(t, project.tasks), tasksBefore)
			requireSameRefs(t, "origin's refs outside refs/workbook/", refsOutsideWorkbook(t, project.code), codeOutside)
			requireSameRefs(t, "the seed's refs outside refs/workbook/", refsOutsideWorkbook(t, project.seed), seedOutside)

			code, _, stderr = run(t, stale, "fetch", "--json")
			if code != 3 {
				t.Fatalf("fetch on the old settings after the clean-up = %d, want 3; stderr = %q", code, stderr)
			}
			refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryNotInitialized, core.TasksRemoteMoved)
			if refusal.MovedTo == nil || refusal.MovedTo.URL != project.tasks {
				t.Fatalf("refusal on the old settings = %+v", refusal)
			}
			if code, _, stderr := run(t, stale, "update", project.ids[0], "--title", "Kept on the old settings"); code != 3 {
				t.Fatalf("update on the old settings = %d, want 3; stderr = %q", code, stderr)
			}
			if got := refsUnder(t, project.code, "refs/workbook/"); got != "refs/workbook/project "+marker {
				t.Fatalf("the old settings published to origin:\n%s", got)
			}

			// Back onto the place that was cleaned up.
			if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
				t.Fatalf("config unset tasks-remote = (%d, %q, %q)", code, stdout, stderr)
			}
			back := moveTasksJSON(t, project.seed)
			if back.From.Remote != "tasks" || back.To.Remote != "origin" || back.Returned != "returned" ||
				back.Tasks != len(project.ids) || !back.Verified {
				t.Fatalf("move-tasks back onto the cleaned place = %+v", back)
			}
			if got, want := refsUnder(t, project.code, "refs/workbook/tasks/"), refsUnder(t, project.tasks, "refs/workbook/tasks/"); got != want || got == "" {
				t.Fatalf("origin and tasks hold different task refs after the move back:\n%s\n%s", got, want)
			}
			if got := identityTipSubject(t, project.code); got != "workbook: tasks returned" {
				t.Fatalf("origin's identity tip after the move back = %q", got)
			}
			requireSameRefs(t, "origin's refs outside refs/workbook/", refsOutsideWorkbook(t, project.code), codeOutside)
		})
	}
}

// Wiping the remote the tasks live on is refused without --confirm naming it,
// with the number of tasks and the command, and with --no-backup; the dry run
// and both refusals change nothing. Confirmed, it removes every Workbook ref
// there, nothing else, and leaves a bundle from which a new empty repository
// is given exactly the refs the remote held.
func TestResetRemoteWipesTheLiveTasksOnlyWhenConfirmed(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			testrepo.RequireObjectFormat(t, objectFormat)
			project := codeProject(t, objectFormat)
			originBefore, seedOutside := refTips(t, project.code), refsOutsideWorkbook(t, project.seed)
			workbookBefore := refsUnder(t, project.code, "refs/workbook/")

			message, refusal := resetRefusalJSON(t, project.seed, 5, "--remote", "origin")
			if refusal.Reason != gitstore.ResetConfirm || refusal.Tasks != len(project.ids) ||
				refusal.Command != "workbook reset --remote origin --confirm origin" ||
				!strings.Contains(message, "removes all 2 of them") || !strings.Contains(message, refusal.Command) {
				t.Fatalf("refusal = %q, %+v", message, refusal)
			}
			if _, refusal := resetRefusalJSON(t, project.seed, 5, "--remote", "origin", "--confirm", "tasks"); refusal.Reason != gitstore.ResetConfirm {
				t.Fatalf("a confirmation naming another remote = %+v", refusal)
			}
			if _, refusal := resetRefusalJSON(t, project.seed, 5, "--remote", "origin", "--confirm", "origin", "--no-backup"); refusal.Reason != gitstore.ResetBackupRequired {
				t.Fatalf("--no-backup on the live remote = %+v", refusal)
			}
			if dry := resetJSON(t, project.seed, "--remote", "origin", "--dry-run"); !dry.DryRun || dry.Tasks != len(project.ids) || dry.MovedTo != nil {
				t.Fatalf("dry run = %+v", dry)
			}
			requireSameRefs(t, "origin after the refusals and the dry run", refTips(t, project.code), originBefore)

			summary := resetJSON(t, project.seed, "--remote", "origin", "--confirm", "origin")
			if summary.Scope != "remote" || summary.Tasks != len(project.ids) || summary.Bundle == "" ||
				!reflect.DeepEqual(summary.Next, []string{"workbook reset"}) {
				t.Fatalf("reset --remote = %+v", summary)
			}
			if got := refsUnder(t, project.code, "refs/workbook/"); got != "" {
				t.Fatalf("origin still holds:\n%s", got)
			}
			if got := refsUnder(t, project.seed, "refs/workbook/remotes/origin/"); got != "" {
				t.Fatalf("the seed still tracks:\n%s", got)
			}
			outside := map[string]string{}
			for name, tip := range originBefore {
				if !strings.HasPrefix(name, "refs/workbook/") {
					outside[name] = tip
				}
			}
			requireSameRefs(t, "origin's refs outside refs/workbook/", refsOutsideWorkbook(t, project.code), outside)
			requireSameRefs(t, "the seed's refs outside refs/workbook/", refsOutsideWorkbook(t, project.seed), seedOutside)

			restored := newBareRepository(t, objectFormat)
			restore := exec.Command("sh", "-c", summary.Restore)
			restore.Dir = restored
			if output, err := restore.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", summary.Restore, err, output)
			}
			if got := refsUnder(t, restored, "refs/workbook/"); got != workbookBefore {
				t.Fatalf("the restored repository holds\n%s\nwant\n%s", got, workbookBefore)
			}
			if got := refsOutsideWorkbook(t, restored); len(got) != 0 {
				t.Fatalf("the restored repository holds refs outside refs/workbook/: %v", got)
			}
		})
	}
}

// refuseDeletions installs an update hook in a bare repository that refuses
// to delete any ref whose name starts with prefix, as a host's rule would.
func refuseDeletions(t *testing.T, bare, prefix string) {
	t.Helper()
	testenv.WriteExecutable(t, filepath.Join(bare, "hooks", "update"), []byte("#!/bin/sh\n"+
		"case \"$1\" in "+prefix+"*) ;; *) exit 0 ;; esac\n"+
		"case \"$3\" in *[!0]*) exit 0 ;; esac\n"+
		"echo \"rule: Workbook refs here may not be deleted\" >&2\n"+
		"exit 1\n"))
}

// A host that refuses to delete refs gets one refusal, exit 1, naming its rule
// and every ref it still holds. Deletions it allowed are made and not undone;
// once the rule is lifted, running the reset again finishes the job.
func TestResetRemoteNamesAHostRuleAgainstDeletionAndFinishesWhenItIsLifted(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, prefix string
		remaining    func(project tasksRemoteProject) []string
	}{
		{"every ref", "refs/workbook/", func(project tasksRemoteProject) []string {
			names := []string{"refs/workbook/config", "refs/workbook/project"}
			for _, id := range project.ids {
				names = append(names, "refs/workbook/tasks/"+id)
			}
			sort.Strings(names)
			return names
		}},
		{"the identity ref", "refs/workbook/project", func(tasksRemoteProject) []string {
			return []string{"refs/workbook/project"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project := codeProject(t, testrepo.FormatSHA1)
			outside := refsOutsideWorkbook(t, project.code)
			refuseDeletions(t, project.code, test.prefix)

			message, refusal := resetRefusalJSON(t, project.seed, 1, "--remote", "origin", "--confirm", "origin")
			want := test.remaining(project)
			if refusal.Reason != gitstore.ResetDeletionRefused || !reflect.DeepEqual(refusal.Remaining, want) ||
				refusal.Bundle == "" || refusal.Command != "workbook reset --remote origin --confirm origin" ||
				!strings.Contains(message, "rule: Workbook refs here may not be deleted") ||
				!strings.Contains(message, "hook declined") {
				t.Fatalf("refusal = %q, %+v; want remaining %v", message, refusal, want)
			}
			var held []string
			for name := range refTips(t, project.code) {
				if strings.HasPrefix(name, "refs/workbook/") {
					held = append(held, name)
				}
			}
			sort.Strings(held)
			if !reflect.DeepEqual(held, want) {
				t.Fatalf("origin holds %v, want %v", held, want)
			}

			if err := os.Remove(filepath.Join(project.code, "hooks", "update")); err != nil {
				t.Fatal(err)
			}
			resetJSON(t, project.seed, "--remote", "origin", "--confirm", "origin")
			if got := refsUnder(t, project.code, "refs/workbook/"); got != "" {
				t.Fatalf("origin still holds after the rule was lifted:\n%s", got)
			}
			requireSameRefs(t, "origin's refs outside refs/workbook/", refsOutsideWorkbook(t, project.code), outside)
		})
	}
}

// The reset stops this checkout's watcher, a separate process, and waits for
// it to exit before removing anything, so the watcher can neither publish
// over the reset nor write a cache after it.
func TestResetStopsThisCheckoutsWatcherFirst(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	watcher := exec.Command(binary, "sync", "--watch", "--interval", "1h")
	watcher.Dir = clone
	output := &lockedWriter{}
	watcher.Stdout, watcher.Stderr = output, output
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = watcher.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = watcher.Process.Kill()
		<-exited
	})
	commonDir := filepath.Join(clone, ".git")
	deadline := time.Now().Add(time.Minute)
	for {
		client, err := syncloop.Dial(commonDir, time.Second)
		if err == nil {
			_ = client.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never answered; output = %q", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if dry := resetJSON(t, clone, "--dry-run"); dry.Watcher == nil || dry.Watcher.PID != watcher.Process.Pid || dry.Watcher.Stopped {
		t.Fatalf("dry run watcher = %+v, want process %d reported running", dry.Watcher, watcher.Process.Pid)
	}
	summary := resetJSON(t, clone)
	if summary.Watcher == nil || summary.Watcher.PID != watcher.Process.Pid || !summary.Watcher.Stopped {
		t.Fatalf("reset watcher = %+v, want process %d stopped", summary.Watcher, watcher.Process.Pid)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the watcher process did not exit")
	}
	if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
		t.Fatalf("refs left after the reset:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(commonDir, "workbook")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cache directory is still there: %v", err)
	}
}

// The flags that make no sense together are refused before anything is read.
func TestResetRefusesFlagsThatCannotBeCombined(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{"--confirm", "origin"},
		{"--discard-unpublished"},
		{"--remote", "origin", "--no-backup", "--discard-unpublished"},
		{"--no-backup", "--backup", "elsewhere"},
		{"--backup", ""},
		{"--remote", ""},
	} {
		if code, _, stderr := run(t, repository, append([]string{"reset"}, args...)...); code != 2 {
			t.Errorf("reset %v = %d, want 2; stderr = %q", args, code, stderr)
		}
	}
}

// A move that stops before it is verified cleans nothing up, whatever
// --clean-up says: the place the tasks are in keeps every ref. Once the move
// has run, reset --remote on the place left needs no confirmation, because
// its marker says the tasks are elsewhere, and it keeps that marker.
func TestMoveTasksCleanUpWaitsForAFinishedMove(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	missing := filepath.Join(t.TempDir(), "missing.git")
	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", missing); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	before := refTips(t, project.code)
	if code, stdout, _ := run(t, project.seed, "move-tasks", "--clean-up", "--json"); code == 0 {
		t.Fatalf("move-tasks to a repository that does not exist succeeded: %q", stdout)
	}
	requireSameRefs(t, "origin after a move that stopped early", refTips(t, project.code), before)

	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	if hasGitRemote(t, project.seed, "tasks") {
		cliGit(t, project.seed, "remote", "set-url", "tasks", project.tasks)
	}
	moveTasksJSON(t, project.seed)
	marker := cliGitOutput(t, project.code, "rev-parse", "refs/workbook/project")
	dry := resetJSON(t, project.seed, "--remote", "origin", "--dry-run")
	if dry.MovedTo == nil || dry.MovedTo.Remote != "tasks" || !reflect.DeepEqual(dry.Kept, []string{"refs/workbook/project"}) ||
		dry.Tasks != len(project.ids) {
		t.Fatalf("dry run on the place left = %+v", dry)
	}
	resetJSON(t, project.seed, "--remote", "origin")
	if got := refsUnder(t, project.code, "refs/workbook/"); got != "refs/workbook/project "+marker {
		t.Fatalf("origin holds after its reset:\n%s", got)
	}
}

func hasGitRemote(t *testing.T, repository, name string) bool {
	t.Helper()
	return exec.Command("git", "-C", repository, "remote", "get-url", name).Run() == nil
}
