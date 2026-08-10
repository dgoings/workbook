package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// AGENTS.md names shallow and single-branch clones as bootstrap shapes that
// must work, and neither appeared anywhere in test code: --depth and
// --single-branch narrow what a clone carries and what its default refspec
// fetches, and Workbook's refs live outside refs/heads in either case. A
// bootstrap that leaned on the clone's own refspec, or on task history being
// reachable from a branch, passes every full-clone test and fails here.
func TestSetupBootstrapsNarrowedClones(t *testing.T) {
	tests := map[string]struct {
		cloneArgs []string
		assert    func(t *testing.T, clone string)
	}{
		"shallow": {
			// --depth needs the file:// transport; Git refuses to make a local
			// path clone shallow.
			cloneArgs: []string{"--depth", "1"},
			assert: func(t *testing.T, clone string) {
				if got := cliGitOutput(t, clone, "rev-parse", "--is-shallow-repository"); got != "true" {
					t.Fatalf("clone is-shallow = %q, want true", got)
				}
			},
		},
		"single-branch": {
			cloneArgs: []string{"--single-branch", "--branch", "main"},
			assert: func(t *testing.T, clone string) {
				// The clone's own refspec covers one branch and no Workbook
				// ref, so setup has to name the refs it wants itself.
				if got := cliGitOutput(t, clone, "config", "--get-all", "remote.origin.fetch"); got != "+refs/heads/main:refs/remotes/origin/main" {
					t.Fatalf("clone fetch refspec = %q, want only the single branch", got)
				}
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			origin, seed, seeded := bootstrapOrigin(t)
			clone := filepath.Join(t.TempDir(), "clone")
			args := append([]string{"clone", "--quiet"}, test.cloneArgs...)
			cliGit(t, t.TempDir(), append(args, "file://"+origin, clone)...)
			cliGit(t, clone, "config", "user.name", "Workbook Test")
			cliGit(t, clone, "config", "user.email", "workbook@example.test")
			test.assert(t, clone)

			if code, _, stderr := run(t, clone, "setup"); code != 0 {
				t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
			}
			if got := listedTaskIDs(t, clone); !sameIDs(got, seeded) {
				t.Fatalf("bootstrapped tasks = %v, want %v", got, seeded)
			}

			// Bootstrap is only half of it: the narrowed clone has to publish
			// back through the same origin it could not fetch by default.
			code, stdout, stderr := run(t, clone, "create", "Created in a narrowed clone", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
			}
			created := decodeMutationTask(t, stdout, "create")
			if !remoteHasTaskRef(t, clone, created.ID) {
				t.Fatalf("origin does not hold task %s created in the narrowed clone", created.ID)
			}

			// And an ordinary fetch has to keep seeing later shared work.
			later := cliCreateTask(t, seed, "Created after the narrowed clone")
			if code, _, stderr := run(t, seed, "push"); code != 0 {
				t.Fatalf("later push code = %d, want 0; stderr = %q", code, stderr)
			}
			if code, _, stderr := run(t, clone, "fetch"); code != 0 {
				t.Fatalf("fetch code = %d, want 0; stderr = %q", code, stderr)
			}
			want := append(append([]string(nil), seeded...), created.ID, later.ID)
			if got := listedTaskIDs(t, clone); !sameIDs(got, want) {
				t.Fatalf("tasks after fetch = %v, want %v", got, want)
			}
		})
	}
}

// A checkout that predates a project's Workbook adoption has no tracked
// configuration in its working tree, but the project already exists: its
// configuration is committed on origin's default branch. Setup must join that
// project rather than mint a second identity whose private guard then rejects
// the real configuration on every later command.
func TestSetupJoinsExistingOriginProjectFromPreWorkbookBranch(t *testing.T) {
	_, seed, stale := originAdoptedAfterClone(t)

	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, seed, "push", "--quiet", "origin", "main")
	seeded := []string{
		cliCreateTask(t, seed, "Shared before the stale checkout ran setup").ID,
		cliCreateTask(t, seed, "Also shared before the stale checkout ran setup").ID,
	}
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}

	if code, _, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got, want := projectIDOf(t, stale), projectIDOf(t, seed); got != want {
		t.Fatalf("stale checkout adopted project %q, want origin's %q", got, want)
	}
	if code, _, stderr := run(t, stale, "sync"); code != 0 {
		t.Fatalf("stale sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, stale); !sameIDs(got, seeded) {
		t.Fatalf("adopted tasks = %v, want %v", got, seeded)
	}
}

// When origin holds Workbook task refs but its default branch never gained the
// tracked configuration, setup cannot know which project the tasks belong to.
// Minting a fresh identity would wedge the repository behind a guard mismatch
// the moment the real configuration arrives, so setup must refuse and leave no
// identity behind.
func TestSetupRefusesToMintWhenOriginHasTasksButNoCommittedConfig(t *testing.T) {
	_, seed, stale := originAdoptedAfterClone(t)

	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	cliCreateTask(t, seed, "Published without committing the configuration")
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}

	code, _, stderr := run(t, stale, "setup")
	if code == 0 {
		t.Fatal("stale setup code = 0, want a refusal to mint a second project")
	}
	if !strings.Contains(stderr, ".workbook/config.json") {
		t.Fatalf("stale setup stderr %q does not tell the user what origin is missing", stderr)
	}
	if _, err := os.Stat(filepath.Join(stale, ".workbook", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("refused setup left a tracked configuration behind (stat error = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(stale, ".git", "workbook", "project.json")); !os.IsNotExist(err) {
		t.Fatalf("refused setup published a project guard (stat error = %v)", err)
	}
}

// --no-sync keeps bootstrap fully local, so it must keep working when origin
// cannot even be reached — it is the escape hatch the origin probe's failure
// message recommends.
func TestSetupNoSyncSkipsOriginProbe(t *testing.T) {
	repo := testrepo.New(t)
	cliGit(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if code, _, stderr := run(t, repo, "setup", "--no-sync"); code != 0 {
		t.Fatalf("setup --no-sync code = %d, want 0; stderr = %q", code, stderr)
	}
}

func projectIDOf(t *testing.T, repository string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repository, ".workbook", "config.json"))
	if err != nil {
		t.Fatalf("read tracked configuration: %v", err)
	}
	var config core.ProjectConfig
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatalf("decode tracked configuration: %v", err)
	}
	return config.ProjectID
}

// originAdoptedAfterClone builds a bare origin, a seed clone that will adopt
// Workbook, and a second clone taken before that adoption whose checkout
// therefore lacks the tracked configuration.
func originAdoptedAfterClone(t *testing.T) (string, string, string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	cliGit(t, bare, "config", "maintenance.auto", "false")

	seed := testrepo.New(t)
	cliGit(t, seed, "branch", "-M", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, seed, "add", "README.md")
	cliGit(t, seed, "commit", "--quiet", "-m", "Before Workbook")
	cliGit(t, seed, "remote", "add", "origin", bare)
	cliGit(t, seed, "push", "--quiet", "-u", "origin", "main")
	cliGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	stale := filepath.Join(t.TempDir(), "stale")
	cliGit(t, t.TempDir(), "clone", "--quiet", bare, stale)
	cliGit(t, stale, "config", "user.name", "Workbook Test")
	cliGit(t, stale, "config", "user.email", "workbook@example.test")
	return bare, seed, stale
}

func listedTaskIDs(t *testing.T, repository string) []string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list = (%d, %q, %q)", code, stdout, stderr)
	}
	var tasks []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &tasks); err != nil {
		t.Fatalf("decode listed tasks: %v", err)
	}
	ids := make([]string, len(tasks))
	for index, task := range tasks {
		ids[index] = task.ID
	}
	sort.Strings(ids)
	return ids
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	for index := range got {
		if got[index] != sorted[index] {
			return false
		}
	}
	return true
}

// bootstrapOrigin publishes a Workbook project and two task refs to a bare
// origin whose branch history is deep enough for --depth 1 to leave something
// out. It returns the origin, the seed clone that published to it, and the
// task IDs origin already holds.
func bootstrapOrigin(t *testing.T) (string, string, []string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	// Background auto-gc spawned by receive-pack can outlive the test and race
	// t.TempDir cleanup with "directory not empty" on slow runners.
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	cliGit(t, bare, "config", "maintenance.auto", "false")

	seed := testrepo.New(t)
	cliGit(t, seed, "branch", "-M", "main")
	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, seed, "add", "README.md")
	cliGit(t, seed, "commit", "--quiet", "-m", "Add a second commit so a depth-1 clone is truncated")
	cliGit(t, seed, "remote", "add", "origin", bare)
	cliGit(t, seed, "push", "--quiet", "-u", "origin", "main")
	cliGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	ids := []string{
		cliCreateTask(t, seed, "Shared before the narrowed clone").ID,
		cliCreateTask(t, seed, "Also shared before the narrowed clone").ID,
	}
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}
	return bare, seed, ids
}
