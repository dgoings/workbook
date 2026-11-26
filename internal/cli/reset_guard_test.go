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

	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// startResetWatcher starts `workbook sync --watch` in clone as a separate
// process and waits until it answers. The returned channel closes when it
// exits.
func startResetWatcher(t *testing.T, binary, clone string) (*exec.Cmd, chan struct{}) {
	t.Helper()
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
	deadline := time.Now().Add(time.Minute)
	for !watcherAnswers(filepath.Join(clone, ".git")) {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never answered; output = %q", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return watcher, exited
}

func watcherAnswers(commonDir string) bool {
	client, err := syncloop.Dial(commonDir, time.Second)
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
}

// A --backup inside .workbook/ or the Git directory's workbook/ folder, the
// two folders a reset of this checkout removes files from, is refused with
// exit 2 before anything is written or removed, for a file, a folder, a
// relative path and a path through a symbolic link, and for reset --remote
// and move-tasks --clean-up, which share the bundle's path. Nothing is
// written at the path, and the work only this checkout holds is still there.
func TestResetRefusesABackupInsideAFolderItRemovesFilesFrom(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	cliCreateTaskNoSync(t, clone, "Here only")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(clone, ".workbook"), link); err != nil {
		t.Fatal(err)
	}
	before, originBefore := refTips(t, clone), refTips(t, project.code)
	listing := func() string {
		var names []string
		for _, folder := range []string{filepath.Join(clone, ".workbook"), filepath.Join(clone, ".git", "workbook")} {
			_ = filepath.Walk(folder, func(path string, _ os.FileInfo, _ error) error {
				names = append(names, path)
				return nil
			})
		}
		sort.Strings(names)
		return strings.Join(names, "\n")
	}
	filesBefore := listing()
	for _, args := range [][]string{
		{"reset", "--backup", ".workbook/mybackup.bundle"},
		{"reset", "--backup", ".workbook"},
		{"reset", "--backup", ".git/workbook"},
		{"reset", "--backup", ".git/workbook/new/mybackup.bundle"},
		{"reset", "--backup", filepath.Join(link, "mybackup.bundle")},
		{"reset", "--dry-run", "--backup", ".workbook"},
		{"reset", "--remote", "origin", "--confirm", "origin", "--backup", filepath.Join(clone, ".workbook")},
		{"move-tasks", "--clean-up", "--backup", filepath.Join(clone, ".git", "workbook")},
	} {
		code, stdout, stderr := run(t, clone, append(args, "--json")...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "which workbook reset removes files from") {
			t.Errorf("%v = (%d, %q, %q), want exit 2 naming the folder", args, code, stdout, stderr)
		}
	}
	requireSameRefs(t, "the checkout's refs", refTips(t, clone), before)
	requireSameRefs(t, "origin's refs", refTips(t, project.code), originBefore)
	if got := listing(); got != filesBefore {
		t.Fatalf("the folders changed:\n%s\nwant\n%s", got, filesBefore)
	}
}

func cliCreateTaskNoSync(t *testing.T, repository, title string) {
	t.Helper()
	if code, _, stderr := run(t, repository, "create", title, "--no-sync"); code != 0 {
		t.Fatalf("create %q = %d; stderr = %q", title, code, stderr)
	}
}

// Under .workbook/ a reset removes only the temporary file Workbook's own
// write of the settings leaves; a hand-made file, an ignored one, symbolic
// links and a file of that name in a subfolder stay, and are counted. The dry
// run lists exactly the refs and the files the real run then removes.
func TestResetDryRunListsExactlyWhatTheResetRemoves(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	clone, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(clone, ".workbook")
	if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("/.workbook/*.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(folder, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := []string{"draft.md", "ignored.local", "notes/.config-1.tmp", "link-to-draft", "dangling"}
	for _, name := range []string{"draft.md", "ignored.local", "notes/.config-1.tmp"} {
		if err := os.WriteFile(filepath.Join(folder, filepath.FromSlash(name)), []byte("theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("draft.md", filepath.Join(folder, "link-to-draft")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(folder, "dangling")); err != nil {
		t.Fatal(err)
	}
	workbooks := filepath.Join(folder, ".config-123456.tmp")
	if err := os.WriteFile(workbooks, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := run(t, clone, "reset", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "Left:\t5 untracked files under .workbook/ that Workbook did not write") {
		t.Fatalf("dry run text = (%d, %q)", code, stdout)
	}
	dry := resetJSON(t, clone, "--dry-run")
	if !containsString(dry.Caches, workbooks) || !containsString(dry.Caches, filepath.Join(clone, ".git", "workbook", "project.json")) ||
		dry.Left != len(theirs) {
		t.Fatalf("dry run caches = %v, left %d", dry.Caches, dry.Left)
	}
	summary := resetJSON(t, clone)
	sort.Strings(dry.Caches)
	sort.Strings(summary.Caches)
	if !reflect.DeepEqual(summary.Caches, dry.Caches) || !reflect.DeepEqual(summary.Refs, dry.Refs) || summary.Left != dry.Left {
		t.Fatalf("the reset removed refs %v and files %v (left %d);\nthe dry run said refs %v and files %v (left %d)",
			summary.Refs, summary.Caches, summary.Left, dry.Refs, dry.Caches, dry.Left)
	}
	for _, name := range theirs {
		if _, err := os.Lstat(filepath.Join(folder, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if _, err := os.Lstat(workbooks); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Workbook's own temporary file is still there: %v", err)
	}
}

// Every check that can refuse a reset runs before this checkout's watcher is
// stopped: a refused reset leaves the watcher running.
func TestResetRefusesBeforeStoppingTheWatcher(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	_, exited := startResetWatcher(t, binary, clone)
	commonDir := filepath.Join(clone, ".git")

	existing := filepath.Join(t.TempDir(), "existing.bundle")
	if err := os.WriteFile(existing, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, clone, "update-ref", "refs/workbook/reconciled/"+project.ids[0]+"/1", localTaskRef(t, clone, project.ids[0]))
	for _, refusal := range []struct {
		args []string
		code int
	}{
		{[]string{"--backup", existing}, 5},
		{[]string{"--backup", ".workbook"}, 2},
		{[]string{"--no-backup"}, 5},
	} {
		if code, stdout, stderr := run(t, clone, append([]string{"reset", "--json"}, refusal.args...)...); code != refusal.code {
			t.Fatalf("reset %v = (%d, %q, %q), want %d", refusal.args, code, stdout, stderr, refusal.code)
		}
		select {
		case <-exited:
			t.Fatalf("reset %v was refused and stopped the watcher", refusal.args)
		default:
		}
		if !watcherAnswers(commonDir) {
			t.Fatalf("reset %v was refused and the watcher no longer answers", refusal.args)
		}
	}
}

// With its tasks remote unreachable a checkout cannot tell what only it
// holds: its tracking refs say what the remote held at the last fetch, and a
// remote wiped since then makes them hide work. A reset without a bundle is
// then refused, --discard-unpublished or not; with the bundle it goes ahead.
func TestResetWithoutABackupIsRefusedWhenTheRemoteCannotBeChecked(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	resetJSON(t, project.seed, "--remote", "origin", "--confirm", "origin")
	cliGit(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable.git"))
	before := refTips(t, clone)

	for _, args := range [][]string{{"--no-backup"}, {"--no-backup", "--discard-unpublished"}} {
		message, refusal := resetRefusalJSON(t, clone, 5, args...)
		if refusal.Reason != gitstore.ResetUnchecked || refusal.Command != "workbook reset" ||
			!strings.Contains(message, "origin could not be checked") {
			t.Fatalf("reset %v = %q, %+v", args, message, refusal)
		}
	}
	requireSameRefs(t, "the checkout's refs after the refusals", refTips(t, clone), before)
	summary := resetJSON(t, clone)
	if summary.Unchecked == "" || summary.Bundle == "" {
		t.Fatalf("reset with the remote unreachable = %+v", summary)
	}
	for _, id := range project.ids {
		if got := bundleHolds(t, clone, summary.Bundle, "refs/workbook/tasks/"+id); got != before["refs/workbook/tasks/"+id] {
			t.Fatalf("the bundle holds %s at %q, want %s", id, got, before["refs/workbook/tasks/"+id])
		}
	}
}

func bundleHolds(t *testing.T, directory, bundle, ref string) string {
	t.Helper()
	for _, line := range strings.Split(cliGitOutput(t, directory, "bundle", "list-heads", bundle), "\n") {
		if objectID, name, found := strings.Cut(line, " "); found && name == ref {
			return objectID
		}
	}
	return ""
}

// A checkout whose own ref names an object it does not have cannot take the
// remote's history in, and the reset says that is what failed, naming the
// damaged ref, rather than blaming a remote it read.
func TestResetSaysWhenTheCheckoutRatherThanTheRemoteFailed(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	// A ref to an object only another repository holds, borrowed through
	// its alternates for long enough to set the ref.
	elsewhere := t.TempDir()
	cliGit(t, elsewhere, "init", "--quiet")
	missing := cliGitOutputInput(t, elsewhere, "only elsewhere\n", "hash-object", "-w", "--stdin")
	alternates := filepath.Join(clone, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(elsewhere, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	damaged := "refs/workbook/tasks/" + project.ids[0]
	cliGit(t, clone, "update-ref", damaged, missing)
	if err := os.Remove(alternates); err != nil {
		t.Fatal(err)
	}

	dry := resetJSON(t, clone, "--dry-run")
	if !strings.HasPrefix(dry.Unchecked, "origin was read, but its history could not be fetched into this checkout") ||
		!strings.Contains(dry.Unchecked, "damaged refs ("+damaged+")") {
		t.Fatalf("unchecked = %q, want it to say origin was read and name %s", dry.Unchecked, damaged)
	}
}

// A move that finished but set edits aside, exit 8, cleans nothing up under
// --clean-up: the place left keeps every ref, and the result names the
// command that cleans it up once the edits are dealt with.
func TestMoveTasksCleanUpWaitsWhenTheMoveSetEditsAside(t *testing.T) {
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
	before := refsUnder(t, project.code, "refs/workbook/")

	code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up", "--json")
	if code != 8 {
		t.Fatalf("move-tasks --clean-up with a conflicting straggler = (%d, %q, %q), want 8", code, stdout, stderr)
	}
	var moved moveTasksSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "move-tasks").Data, &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Verified || len(moved.SetAside) != 1 || moved.CleanUp != nil || moved.CleanUpNext != "workbook move-tasks --clean-up" {
		t.Fatalf("move-tasks --clean-up = %+v, cleanUp %+v, next %q", moved.MoveTasksResult, moved.CleanUp, moved.CleanUpNext)
	}
	if got := refsUnder(t, project.code, "refs/workbook/"); got != before {
		t.Fatalf("origin was cleaned up after a move that set edits aside:\n%s\nwant\n%s", got, before)
	}

	// The command it names finishes the job once the edits are looked at: a
	// later run reports them set aside without stopping, and cleans up.
	code, stdout, stderr = run(t, project.seed, "move-tasks", "--clean-up")
	if code != 0 || !strings.Contains(stdout, "parked earlier in this checkout") || !strings.Contains(stdout, "Cleaned up:\torigin") {
		t.Fatalf("the named move-tasks --clean-up = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	if got := refsUnder(t, project.code, "refs/workbook/"); !strings.HasPrefix(got, "refs/workbook/project ") || strings.Contains(got, "\n") {
		t.Fatalf("origin holds after the named clean-up:\n%s\nwant only its marker", got)
	}
}

// A clean-up a host stopped by refusing deletions names, as the command that
// finishes it, move-tasks --clean-up run again, not plain reset --remote,
// which would delete another project's refs the clean-up leaves. Run once the
// host allows deletions, it finishes and leaves them.
func TestMoveTasksCleanUpRefusedByTheHostNamesTheCleanUpToRunAgain(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	others := otherProject(t, project, "ZZ")
	hook := filepath.Join(project.code, "hooks", "update")
	testenv.WriteExecutable(t, hook, []byte("#!/bin/sh\n"+
		"case \"$3\" in *[!0]*) exit 0;; esac\necho \"deleting refs is not allowed here\"\nexit 1\n"))
	if code, stdout, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set tasks-remote = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, args := range [][]string{{"move-tasks", "--clean-up"}, {"move-tasks", "--clean-up", "--json"}} {
		code, stdout, stderr := run(t, project.seed, args...)
		if code != 1 {
			t.Fatalf("%v with deletions refused = (%d, %q, %q), want exit 1", args, code, stdout, stderr)
		}
		for _, want := range []string{
			"the move is complete, but cleaning up origin did not finish",
			"run workbook move-tasks --clean-up again to finish",
		} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%v stderr = %q, want it to contain %q", args, stderr, want)
			}
		}
		if strings.Contains(stderr, "reset --remote") {
			t.Fatalf("%v stderr = %q, names reset --remote", args, stderr)
		}
		if args[len(args)-1] == "--json" && !strings.Contains(stderr, `"command":"workbook move-tasks --clean-up"`) {
			t.Fatalf("%v stderr = %q, want the refusal's command to be the clean-up", args, stderr)
		}
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run(t, project.seed, "move-tasks", "--clean-up"); code != 0 {
		t.Fatalf("move-tasks --clean-up once deletions are allowed = (%d, %q, %q)", code, stdout, stderr)
	}
	held := workbookRefTips(t, project.code)
	if len(held) != len(others)+1 || held["refs/workbook/project"] == "" {
		t.Fatalf("origin holds after the clean-up: %v, want its marker and %v", held, others)
	}
	for _, ref := range others {
		if held[ref] == "" {
			t.Fatalf("the clean-up deleted %s: %v", ref, held)
		}
	}
}
