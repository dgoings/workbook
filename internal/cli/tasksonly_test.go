package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// tasksOnlyReport is the part of a setup result a tasks-only run adds.
type tasksOnlyReport struct {
	Repository string                     `json:"repository"`
	TaskCount  int                        `json:"taskCount"`
	Docs       json.RawMessage            `json:"docs"`
	Sync       struct{ Status string }    `json:"sync"`
	TasksOnly  *tasksOnlySetupReportShape `json:"tasksOnly"`
}

type tasksOnlySetupReportShape struct {
	Remote      string   `json:"remote"`
	Initialized bool     `json:"initialized"`
	Changed     []string `json:"changed"`
	Actor       string   `json:"actor"`
	Unavailable []string `json:"unavailable"`
}

func decodeTasksOnlySetup(t *testing.T, stdout string) tasksOnlyReport {
	t.Helper()
	var report tasksOnlyReport
	if err := json.Unmarshal(assertJSONResult(t, stdout, "setup").Data, &report); err != nil {
		t.Fatalf("decode setup result: %v; stdout = %q", err, stdout)
	}
	if report.TasksOnly == nil {
		t.Fatalf("setup result has no tasksOnly section: %s", stdout)
	}
	return report
}

// tasksOnlyOriginWithCode is a bare origin holding a project's code on two
// branches and a tag as well as its Workbook refs: the remote a team's server
// is pointed at. The code carries a marker no Workbook object could contain,
// so a test can ask whether any of it reached a folder.
func tasksOnlyOriginWithCode(t *testing.T) (string, string, []string) {
	t.Helper()
	origin, seed, seeded := bootstrapOrigin(t)
	cliGit(t, seed, "switch", "--quiet", "-c", "feature")
	writeCodeFile(t, seed, "feature.go", "package feature // feature branch code only\n")
	cliGit(t, seed, "add", "feature.go")
	cliGit(t, seed, "commit", "--quiet", "-m", "Add feature code")
	cliGit(t, seed, "tag", "v1.0.0")
	cliGit(t, seed, "push", "--quiet", "origin", "feature", "v1.0.0")
	cliGit(t, seed, "switch", "--quiet", "main")
	if got := cliGitOutput(t, origin, "for-each-ref", "--format=%(refname)", "refs/workbook/project"); got != "refs/workbook/project" {
		t.Fatalf("origin identity ref listing = %q; the fixture needs a published project identity", got)
	}
	return origin, seed, seeded
}

// tasksOnlyOriginWithoutCode is a bare origin holding only Workbook refs: no
// branch and no tag, the shape the hand-built check on 0.6.0 ran against.
func tasksOnlyOriginWithoutCode(t *testing.T) (string, []string) {
	t.Helper()
	withCode, _, seeded := bootstrapOrigin(t)
	bare := filepath.Join(t.TempDir(), "tasks.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	cliGit(t, withCode, "push", "--quiet", bare, "refs/workbook/*:refs/workbook/*")
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags"); got != "" {
		t.Fatalf("Workbook-only origin holds %q", got)
	}
	return bare, seeded
}

func writeCodeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// codeObjects lists every object origin's branches and tags reach that its
// Workbook refs do not, which is to say the project's code.
func codeObjects(t *testing.T, origin string) []string {
	t.Helper()
	listing := cliGitOutput(t, origin, "rev-list", "--objects", "--branches", "--tags", "--not", "--glob=refs/workbook/*")
	var objects []string
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			objects = append(objects, fields[0])
		}
	}
	if len(objects) == 0 {
		t.Fatal("origin holds no code objects; the fixture proves nothing")
	}
	return objects
}

// assertHoldsNoCode fails if folder has any ref outside Workbook's namespace,
// anything checked out, or any object reachable from origin's branches.
func assertHoldsNoCode(t *testing.T, folder, origin string) {
	t.Helper()
	for _, ref := range strings.Fields(cliGitOutput(t, folder, "for-each-ref", "--format=%(refname)")) {
		if !strings.HasPrefix(ref, "refs/workbook/") {
			t.Errorf("tasks-only folder holds ref %s", ref)
		}
	}
	if err := exec.Command("git", "-C", folder, "rev-parse", "--verify", "--quiet", "HEAD").Run(); err == nil {
		t.Errorf("tasks-only folder has a HEAD commit; nothing should be checked out")
	}
	if got := cliGitOutput(t, folder, "rev-parse", "--is-bare-repository"); got != "false" {
		t.Errorf("tasks-only folder is-bare = %q, want an ordinary non-bare folder", got)
	}
	for _, object := range codeObjects(t, origin) {
		if err := exec.Command("git", "-C", folder, "cat-file", "-e", object).Run(); err == nil {
			t.Errorf("tasks-only folder holds code object %s", object)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, ".git", "FETCH_HEAD")); err == nil {
		contents, _ := os.ReadFile(filepath.Join(folder, ".git", "FETCH_HEAD"))
		for _, line := range strings.Split(string(contents), "\n") {
			if strings.Contains(line, "branch") || strings.Contains(line, "HEAD") {
				t.Errorf("FETCH_HEAD names code: %q", line)
			}
		}
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" && entry.Name() != ".workbook" {
			t.Errorf("tasks-only folder working tree holds %s", entry.Name())
		}
	}
}

// The heart of the story. A server's folder is pointed at the remote that also
// holds the project's code, and it ends up with every task and none of the
// code — not from setup, and not from a plain `git fetch` or `git pull`
// somebody runs in it afterwards, even once origin has new code to offer.
func TestSetupTasksOnlyHoldsNoBranchesOrCodeFromAnOriginWithCode(t *testing.T) {
	t.Parallel()
	origin, seed, seeded := tasksOnlyOriginWithCode(t)
	folder := filepath.Join(t.TempDir(), "board")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin, "--json")
	if code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	report := decodeTasksOnlySetup(t, stdout)
	if !report.TasksOnly.Initialized || report.TasksOnly.Remote != origin {
		t.Fatalf("tasksOnly = %+v, want initialized against %s", report.TasksOnly, origin)
	}
	if report.Docs != nil {
		t.Fatalf("tasks-only setup reported docs %s; it installs none", report.Docs)
	}
	if report.Sync.Status != "completed" || report.TaskCount != len(seeded) {
		t.Fatalf("sync = %q with %d tasks, want completed with %d", report.Sync.Status, report.TaskCount, len(seeded))
	}
	if got := listedTaskIDs(t, folder); !sameIDs(got, seeded) {
		t.Fatalf("tasks-only folder lists %v, want %v", got, seeded)
	}
	assertHoldsNoCode(t, folder, origin)
	if got := cliGitOutput(t, folder, "config", "--local", "--get", "workbook.tasksOnly"); got != "true" {
		t.Fatalf("workbook.tasksOnly = %q, want the marker in the folder's own Git settings", got)
	}
	if _, err := os.Stat(filepath.Join(folder, "AGENTS.md")); err == nil {
		t.Fatal("tasks-only setup wrote AGENTS.md")
	}
	if _, err := os.Stat(filepath.Join(folder, ".workbook", "guidelines.md")); err == nil {
		t.Fatal("tasks-only setup wrote the guidelines")
	}

	// Origin moves on: new code on the default branch and a new branch.
	writeCodeFile(t, seed, "later.go", "package later // pushed after the board was set up\n")
	cliGit(t, seed, "add", "later.go")
	cliGit(t, seed, "commit", "--quiet", "-m", "Add later code")
	cliGit(t, seed, "push", "--quiet", "origin", "main", "main:refs/heads/later")

	// Each of these is what somebody might type in the folder. Their exit
	// codes are not the point; `git pull` has no branch to merge into and
	// says so. What they download is.
	for _, args := range [][]string{
		{"fetch"},
		{"fetch", "origin"},
		{"fetch", "--all"},
		{"remote", "update"},
		{"pull"},
		{"pull", "origin"},
	} {
		command := exec.Command("git", append([]string{"-C", folder}, args...)...)
		command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, _ := command.CombinedOutput()
		t.Logf("git %s: %s", strings.Join(args, " "), output)
	}
	assertHoldsNoCode(t, folder, origin)

	// Workbook's own synchronization still sees everything it should.
	later := cliCreateTask(t, seed, "Shared after the board was set up")
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, folder, "fetch"); code != 0 {
		t.Fatalf("fetch in tasks-only folder = %d; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, folder); !sameIDs(got, append(append([]string(nil), seeded...), later.ID)) {
		t.Fatalf("tasks after fetch = %v", got)
	}
	assertHoldsNoCode(t, folder, origin)
}

// Every command the story names works in the folder and its changes reach
// origin, against both shapes of remote. Setup itself records nothing, so it
// succeeds in a folder with no author configured and says what edits need.
func TestSetupTasksOnlySupportsEverydayCommandsAndPublishes(t *testing.T) {
	t.Parallel()
	tests := map[string]func(t *testing.T) (string, []string){
		"origin with code": func(t *testing.T) (string, []string) {
			origin, _, seeded := tasksOnlyOriginWithCode(t)
			return origin, seeded
		},
		"origin without code": tasksOnlyOriginWithoutCode,
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			origin, seeded := build(t)
			folder := t.TempDir()

			code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin)
			if code != 0 {
				t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
			}
			for _, want := range []string{
				"Mode:\ttasks-only\t(created; branches and code are never fetched)",
				"Remote:\t" + origin,
				"Docs:\tskipped\t(tasks-only folder)",
				"Author:\tnone configured",
				"Unavailable:\tdocs, hooks",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("setup output lacks %q:\n%s", want, stdout)
				}
			}

			// Without an author, an edit is refused rather than recorded under
			// nobody, and nothing is written.
			if code, _, _ := run(t, folder, "create", "Recorded by nobody"); code == 0 {
				t.Fatal("create with no author configured succeeded")
			}
			cliGit(t, folder, "config", "user.name", "Board Server")
			cliGit(t, folder, "config", "user.email", "board@example.test")

			code, stdout, stderr = run(t, folder, "create", "Filed on the board server", "--json")
			if code != 0 {
				t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
			}
			created := decodeMutationTask(t, stdout, "create")
			if !remoteHasTaskRef(t, folder, created.ID) {
				t.Fatalf("origin does not hold task %s created in the tasks-only folder", created.ID)
			}
			code, stdout, stderr = run(t, folder, "update", seeded[0], "--status", "in-progress", "--comment", "Picked up from the board server", "--json")
			if code != 0 {
				t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
			}
			updated := decodeMutationTask(t, stdout, "update")
			if got := cliGitOutput(t, origin, "rev-parse", "refs/workbook/tasks/"+seeded[0]); got != updated.Head {
				t.Fatalf("origin's %s = %s, want the published update %s", seeded[0], got, updated.Head)
			}
			code, stdout, stderr = run(t, folder, "show", seeded[0], "--json")
			if code != 0 || !strings.Contains(stdout, "Picked up from the board server") {
				t.Fatalf("show = (%d, %q, %q)", code, stdout, stderr)
			}
			if got := listedTaskIDs(t, folder); !sameIDs(got, append(append([]string(nil), seeded...), created.ID)) {
				t.Fatalf("list = %v", got)
			}

			addr := startServeBoard(t, folder)
			body, status := boardRequest(t, http.MethodGet, "http://"+addr+"/api/tasks", "")
			if status != http.StatusOK || !strings.Contains(string(body), created.ID) {
				t.Fatalf("GET /api/tasks = %d; body = %s", status, body)
			}
			body, status = boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks", `{"title":"Filed through the board"}`)
			boardTask := decodeServeMutation(t, body, status)
			deadline := time.Now().Add(30 * time.Second)
			for !remoteHasTaskRef(t, folder, boardTask.ID) {
				if time.Now().After(deadline) {
					t.Fatalf("origin never received task %s created on the board", boardTask.ID)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// tasksOnlyFolderState captures everything a rerun could change: every ref,
// the folder's own Git settings, and every file in the working tree.
func tasksOnlyFolderState(t *testing.T, folder string) string {
	t.Helper()
	var state []string
	state = append(state, cliGitOutput(t, folder, "for-each-ref", "--format=%(refname) %(objectname)"))
	state = append(state, cliGitOutput(t, folder, "config", "--local", "--list"))
	var files []string
	err := filepath.WalkDir(folder, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(folder, path)
			files = append(files, relative+"\n"+string(contents))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return strings.Join(append(state, files...), "\n---\n")
}

// Running the command again in a folder it set up changes nothing and reports
// success, and so does the plain `workbook setup` people already know.
func TestSetupTasksOnlyRerunChangesNothing(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("first setup = (%d, %q, %q)", code, stdout, stderr)
	}
	before := tasksOnlyFolderState(t, folder)

	for _, args := range [][]string{
		{"setup", "--tasks-only", origin, "--json"},
		{"setup", "--json"},
	} {
		code, stdout, stderr := run(t, folder, args...)
		if code != 0 {
			t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
		}
		report := decodeTasksOnlySetup(t, stdout)
		if report.TasksOnly.Initialized || len(report.TasksOnly.Changed) != 0 || report.TasksOnly.Changed == nil {
			t.Fatalf("%v tasksOnly = %+v, want nothing initialized and an empty changed list", args, report.TasksOnly)
		}
		if report.Sync.Status != "completed" {
			t.Fatalf("%v sync = %q, want completed", args, report.Sync.Status)
		}
		if after := tasksOnlyFolderState(t, folder); after != before {
			t.Fatalf("%v changed the folder:\nbefore:\n%s\nafter:\n%s", args, before, after)
		}
	}
	code, stdout, _ := run(t, folder, "setup", "--tasks-only", origin)
	if code != 0 || !strings.Contains(stdout, "Mode:\ttasks-only\t(already set up; nothing changed)") {
		t.Fatalf("rerun text = (%d, %q)", code, stdout)
	}
}

// A setting somebody changed by hand — most likely the fetch refspec, put back
// to Git's default — is restored by the next setup, so the folder cannot be
// left able to fetch code.
func TestSetupTasksOnlyRestoresAChangedFetchSetting(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")

	code, stdout, stderr := run(t, folder, "setup", "--json")
	if code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	report := decodeTasksOnlySetup(t, stdout)
	if strings.Join(report.TasksOnly.Changed, ",") != "remote.origin.fetch" {
		t.Fatalf("changed = %v, want the restored fetch refspec", report.TasksOnly.Changed)
	}
	if got := cliGitOutput(t, folder, "config", "--get-all", "remote.origin.fetch"); got != "^refs/*" {
		t.Fatalf("fetch refspec = %q after setup", got)
	}
	cliGit(t, folder, "fetch")
	assertHoldsNoCode(t, folder, origin)
}

// With origin out of reach the command cannot finish, but it still puts back
// the settings that keep code out, so the folder is safe by the time the
// network returns.
func TestSetupTasksOnlyRestoresAChangedFetchSettingWhenOriginIsUnreachable(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	cliGit(t, folder, "config", "--unset", "remote.origin.tagOpt")
	away := origin + ".away"
	if err := os.Rename(origin, away); err != nil {
		t.Fatal(err)
	}

	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code == 0 {
		t.Fatalf("setup with origin unreachable = (0, %q, %q)", stdout, stderr)
	}
	if got := cliGitOutput(t, folder, "config", "--get-all", "remote.origin.fetch"); got != "^refs/*" {
		t.Fatalf("fetch refspec = %q after setup with origin unreachable", got)
	}
	if got := cliGitOutput(t, folder, "config", "--get", "remote.origin.tagOpt"); got != "--no-tags" {
		t.Fatalf("tagOpt = %q after setup with origin unreachable", got)
	}

	if err := os.Rename(away, origin); err != nil {
		t.Fatal(err)
	}
	cliGit(t, folder, "fetch")
	assertHoldsNoCode(t, folder, origin)
}

// A folder setup did not make is refused before anything in it changes.
func TestSetupTasksOnlyRefusesFoldersItDidNotSetUp(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	other, _, _ := tasksOnlyOriginWithCode(t)

	t.Run("folder holding files", func(t *testing.T) {
		t.Parallel()
		folder := t.TempDir()
		writeCodeFile(t, folder, "notes.txt", "mine\n")
		code, _, stderr := run(t, folder, "setup", "--tasks-only", origin, "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "workbook setup --tasks-only needs an empty folder, and "+folder+" already holds files; run it in a new, empty folder")
		if _, err := os.Stat(filepath.Join(folder, ".git")); err == nil {
			t.Fatal("refused setup created a Git folder")
		}
	})

	t.Run("clone of the code", func(t *testing.T) {
		t.Parallel()
		clone := filepath.Join(t.TempDir(), "clone")
		cliGit(t, t.TempDir(), "clone", "--quiet", origin, clone)
		before := cliGitOutput(t, clone, "config", "--local", "--list")
		code, _, stderr := run(t, clone, "setup", "--tasks-only", origin)
		if code != 5 || !strings.Contains(stderr, "already a Git repository that workbook setup --tasks-only did not set up") {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		if after := cliGitOutput(t, clone, "config", "--local", "--list"); after != before {
			t.Fatalf("refused setup changed the clone's settings:\n%s\n%s", before, after)
		}
	})

	t.Run("empty folder inside a repository", func(t *testing.T) {
		t.Parallel()
		parent := testrepo.New(t)
		folder := filepath.Join(parent, "board")
		if err := os.Mkdir(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := run(t, folder, "setup", "--tasks-only", origin)
		if code != 5 || !strings.Contains(stderr, "is inside the Git repository at") {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(folder, ".git")); err == nil {
			t.Fatal("refused setup created a nested Git folder")
		}
	})

	t.Run("tasks-only folder for another remote", func(t *testing.T) {
		t.Parallel()
		folder := t.TempDir()
		if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
			t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
		}
		code, _, stderr := run(t, folder, "setup", "--tasks-only", other)
		if code != 5 || !strings.Contains(stderr, "is a tasks-only folder for "+origin) {
			t.Fatalf("code = %d, stderr = %q", code, stderr)
		}
		if got := cliGitOutput(t, folder, "config", "--get", "remote.origin.url"); got != origin {
			t.Fatalf("origin = %q after a refused rerun", got)
		}
	})

	t.Run("hand-initialized empty Git folder is adopted", func(t *testing.T) {
		t.Parallel()
		folder := t.TempDir()
		cliGit(t, folder, "init", "--quiet")
		code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin, "--json")
		if code != 0 {
			t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
		}
		report := decodeTasksOnlySetup(t, stdout)
		if report.TasksOnly.Initialized || len(report.TasksOnly.Changed) == 0 {
			t.Fatalf("tasksOnly = %+v, want an adopted folder with its settings written", report.TasksOnly)
		}
		assertHoldsNoCode(t, folder, origin)
	})
}

// A remote this command cannot join leaves an empty folder empty: a typo in
// the URL must not leave a Git folder behind to be refused on the retry.
func TestSetupTasksOnlyAsksOriginBeforeCreatingAnything(t *testing.T) {
	t.Parallel()
	_, seed, _ := originAdoptedAfterClone(t)
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("seed setup = %d; stderr = %q", code, stderr)
	}
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, seed, "push", "--quiet", "origin", "main")
	plain := filepath.Join(t.TempDir(), "plain.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", plain)

	tests := map[string]struct {
		url      string
		category core.Category
		message  string
	}{
		"unreachable": {
			url:      filepath.Join(t.TempDir(), "missing.git"),
			category: core.CategoryOperational,
			message:  "cannot ask",
		},
		"no Workbook project": {
			url:      plain,
			category: core.CategoryValidation,
			message:  "has no Workbook project to join",
		},
		// A project whose configuration is committed on its default branch but
		// which has published no identity ref is found, in a clone, by reading
		// that branch. A tasks-only folder must not, because reading it is
		// downloading the code.
		"configuration only on the default branch": {
			url:      cliGitOutput(t, seed, "remote", "get-url", "origin"),
			category: core.CategoryValidation,
			message:  "has no Workbook project to join",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			folder := t.TempDir()
			code, _, stderr := run(t, folder, "setup", "--tasks-only", test.url, "--json")
			if code == 0 {
				t.Fatal("setup succeeded")
			}
			var document errorDocument
			if err := json.Unmarshal([]byte(stderr), &document); err != nil {
				t.Fatalf("decode error: %v; stderr = %q", err, stderr)
			}
			if document.Error.Category != test.category || !strings.Contains(document.Error.Message, test.message) {
				t.Fatalf("error = %+v, want %s containing %q", document.Error, test.category, test.message)
			}
			entries, err := os.ReadDir(folder)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("refused setup left %d entries in the folder", len(entries))
			}
		})
	}
}

// A tasks-only folder whose origin has task refs but no identity ref — one
// adopted before this run asked — never falls back to reading the default
// branch, which a clone does to find such a project.
func TestTasksOnlySetupNeverFetchesTheDefaultBranchToFindTheProject(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	// Strip the folder back to a marked folder with no identity and no
	// configuration, and origin back to a project that never published its
	// identity. A clone in this state would read origin's default branch.
	for _, ref := range strings.Fields(cliGitOutput(t, folder, "for-each-ref", "--format=%(refname)")) {
		cliGit(t, folder, "update-ref", "-d", ref)
	}
	if err := os.RemoveAll(filepath.Join(folder, ".workbook")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(folder, ".git", "workbook")); err != nil {
		t.Fatal(err)
	}
	cliGit(t, origin, "update-ref", "-d", "refs/workbook/project")

	code, _, stderr := run(t, folder, "setup", "--json")
	if code != 5 || !strings.Contains(stderr, "has not published refs/workbook/project") {
		t.Fatalf("setup = %d; stderr = %q", code, stderr)
	}
	assertHoldsNoCode(t, folder, origin)
}

// The commands whose work needs the project's code say so in a tasks-only
// folder rather than writing documentation or a hook nobody there will use.
func TestTasksOnlyFolderRefusesCodeCommands(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"docs", "install"},
		{"docs", "update"},
		{"docs", "status"},
		{"docs", "remove"},
		{"hooks", "install"},
	} {
		code, _, stderr := run(t, folder, append(args, "--json")...)
		if code != 5 {
			t.Fatalf("%v code = %d, want 5; stderr = %q", args, code, stderr)
		}
		var document errorDocument
		if err := json.Unmarshal([]byte(stderr), &document); err != nil {
			t.Fatalf("decode %v error: %v", args, err)
		}
		if document.Error.Category != core.CategoryValidation ||
			!strings.Contains(document.Error.Message, "is unavailable in a tasks-only folder") {
			t.Fatalf("%v error = %+v", args, document.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, ".git", "hooks", "pre-push")); err == nil {
		t.Fatal("hooks install wrote a pre-push hook in a tasks-only folder")
	}
	if _, err := os.Stat(filepath.Join(folder, "AGENTS.md")); err == nil {
		t.Fatal("docs install wrote AGENTS.md in a tasks-only folder")
	}
}

// The flags that mean nothing in a tasks-only folder are refused as an
// invocation error before the folder is touched.
func TestSetupTasksOnlyRefusesContradictoryFlags(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--no-sync"},
		{"--skill-dir", "skills"},
		{"--force"},
	} {
		folder := t.TempDir()
		code, _, stderr := run(t, folder, append([]string{"setup", "--tasks-only", "/nowhere.git"}, args...)...)
		if code != 2 {
			t.Fatalf("%v code = %d, want 2; stderr = %q", args, code, stderr)
		}
		if entries, _ := os.ReadDir(folder); len(entries) != 0 {
			t.Fatalf("%v left the folder with %d entries", args, len(entries))
		}
	}
	code, _, stderr := run(t, t.TempDir(), "setup", "--tasks-only", "")
	if code != 2 || !strings.Contains(stderr, "--tasks-only needs the URL") {
		t.Fatalf("empty URL = %d; stderr = %q", code, stderr)
	}
}

// A SHA-256 origin gets a SHA-256 folder: the object format is chosen from
// what origin advertises, because a folder in the other format cannot fetch
// from it at all.
func TestSetupTasksOnlyMatchesOriginsObjectFormat(t *testing.T) {
	t.Parallel()
	testrepo.RequireObjectFormat(t, "sha256")
	seed := testrepo.New(t, testrepo.WithObjectFormat("sha256"))
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("seed setup = %d; stderr = %q", code, stderr)
	}
	cliCreateTask(t, seed, "A SHA-256 task")
	bare := filepath.Join(t.TempDir(), "origin.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", "--object-format=sha256", bare)
	cliGit(t, seed, "remote", "add", "origin", bare)
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push = %d; stderr = %q", code, stderr)
	}

	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", bare); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := cliGitOutput(t, folder, "rev-parse", "--show-object-format"); got != "sha256" {
		t.Fatalf("folder object format = %q, want sha256", got)
	}
	if got := listedTaskIDs(t, folder); len(got) != 1 {
		t.Fatalf("tasks = %v, want the one SHA-256 task", got)
	}
}
