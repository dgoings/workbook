package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// The tasks-only marker belongs to one folder. The same setting arriving from
// the user's global Git configuration or from the environment must not make
// an ordinary clone of the code a tasks-only folder: plain setup must not
// rewrite its fetch refspec, and the docs and hooks commands must still work.
//
// Not parallel: Git reads both sources from this process's environment, and
// the commands under test run in this process.
func TestTasksOnlyMarkerOutsideTheFolderLeavesACodeCloneAlone(t *testing.T) {
	sources := map[string]func(t *testing.T){
		"global configuration": func(t *testing.T) {
			original, err := os.ReadFile(os.Getenv("GIT_CONFIG_GLOBAL"))
			if err != nil {
				t.Fatal(err)
			}
			global := filepath.Join(t.TempDir(), "gitconfig")
			contents := string(original) + "[workbook]\n\ttasksOnly = true\n"
			if err := os.WriteFile(global, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", global)
		},
		"environment": func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "workbook.tasksOnly")
			t.Setenv("GIT_CONFIG_VALUE_0", "true")
		},
	}
	for name, supply := range sources {
		t.Run(name, func(t *testing.T) {
			origin, _, _ := tasksOnlyOriginWithCode(t)
			clone := filepath.Join(t.TempDir(), "clone")
			cliGit(t, t.TempDir(), "clone", "--quiet", origin, clone)
			refspec := cliGitOutput(t, clone, "config", "--get-all", "remote.origin.fetch")

			supply(t)
			if got := cliGitOutput(t, clone, "config", "--type=bool", "--get", "workbook.tasksOnly"); got != "true" {
				t.Fatalf("Git reads workbook.tasksOnly = %q in the clone; the fixture supplies no marker", got)
			}

			code, stdout, stderr := run(t, clone, "setup", "--json")
			if code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}
			if strings.Contains(stdout, `"tasksOnly"`) {
				t.Fatalf("setup in a code clone reported a tasks-only run: %s", stdout)
			}
			if got := cliGitOutput(t, clone, "config", "--get-all", "remote.origin.fetch"); got != refspec {
				t.Fatalf("setup rewrote the clone's fetch refspec from %q to %q", refspec, got)
			}
			if output, err := exec.Command("git", "-C", clone, "config", "--local", "--get-all", "remote.origin.tagopt").Output(); err == nil {
				t.Fatalf("setup wrote remote.origin.tagOpt = %q in the clone", output)
			}
			if _, err := os.Stat(filepath.Join(clone, ".workbook", "guidelines.md")); err != nil {
				t.Fatalf("setup in a code clone skipped the agent documentation: %v", err)
			}
			for _, args := range [][]string{
				{"docs", "update"},
				{"docs", "status"},
				{"hooks", "install"},
			} {
				if code, stdout, stderr := run(t, clone, args...); code != 0 {
					t.Fatalf("%v in a code clone = (%d, %q, %q)", args, code, stdout, stderr)
				}
			}
		})
	}
}

// tasksOnlySHA256Origin is a bare SHA-256 origin holding one task and its
// project identity.
func tasksOnlySHA256Origin(t *testing.T) string {
	t.Helper()
	testrepo.RequireObjectFormat(t, "sha256")
	seed := testrepo.New(t, testrepo.WithObjectFormat("sha256"))
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("seed setup = %d; stderr = %q", code, stderr)
	}
	cliCreateTask(t, seed, "A SHA-256 task")
	bare := filepath.Join(t.TempDir(), "origin.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", "--object-format=sha256", bare)
	cliGit(t, bare, "config", "receive.autogc", "false")
	cliGit(t, bare, "config", "gc.auto", "0")
	cliGit(t, seed, "remote", "add", "origin", bare)
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push = %d; stderr = %q", code, stderr)
	}
	return bare
}

// A hand-run `git init` in the other object format can never fetch from
// origin, so it is refused before any setting is written, with advice that
// works, rather than adopted into a folder whose every synchronization fails.
func TestSetupTasksOnlyRefusesAFolderInTheOtherObjectFormat(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		origin func(t *testing.T) string
		folder string
		want   string
	}{
		"SHA-256 folder, SHA-1 origin": {
			origin: func(t *testing.T) string {
				testrepo.RequireObjectFormat(t, "sha256")
				origin, _, _ := tasksOnlyOriginWithCode(t)
				return origin
			},
			folder: "sha256",
			want:   "uses sha256 object IDs and ",
		},
		"SHA-1 folder, SHA-256 origin": {
			origin: tasksOnlySHA256Origin,
			folder: "sha1",
			want:   "uses sha1 object IDs and ",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			origin := test.origin(t)
			folder := t.TempDir()
			cliGit(t, folder, "init", "--quiet", "--object-format="+test.folder)
			before := cliGitOutput(t, folder, "config", "--local", "--list")

			for attempt := 0; attempt < 2; attempt++ {
				code, _, stderr := run(t, folder, "setup", "--tasks-only", origin, "--json")
				if code != 5 {
					t.Fatalf("attempt %d code = %d, want 5; stderr = %q", attempt, code, stderr)
				}
				var document errorDocument
				if err := json.Unmarshal([]byte(stderr), &document); err != nil {
					t.Fatalf("decode error: %v; stderr = %q", err, stderr)
				}
				message := document.Error.Message
				if document.Error.Category != core.CategoryValidation ||
					!strings.Contains(message, test.want) ||
					!strings.Contains(message, "Git cannot fetch between the two") ||
					!strings.Contains(message, "and run this again, or run workbook setup --tasks-only "+origin+" in a new, empty folder") {
					t.Fatalf("attempt %d error = %+v", attempt, document.Error)
				}
				if after := cliGitOutput(t, folder, "config", "--local", "--list"); after != before {
					t.Fatalf("attempt %d changed the folder's settings:\nbefore:\n%s\nafter:\n%s", attempt, before, after)
				}
			}

			// The advice works.
			if err := os.RemoveAll(filepath.Join(folder, ".git")); err != nil {
				t.Fatal(err)
			}
			if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
				t.Fatalf("setup after removing .git = (%d, %q, %q)", code, stdout, stderr)
			}
		})
	}
}

// The URL comparison is exact, so another spelling of the same remote is
// refused, and the refusal says how to change the address. Changing it by hand
// and running again then works.
func TestSetupTasksOnlyAddressChangeIsMadeByHand(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	respelled := origin + "/"

	code, _, stderr := run(t, folder, "setup", "--tasks-only", respelled)
	if code != 5 {
		t.Fatalf("respelled setup code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"is a tasks-only folder for " + origin + ", not " + respelled,
		"if " + respelled + " is the same repository under another spelling, or the repository has moved, run git config remote.origin.url " + respelled + " in this folder and run this again",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("refusal lacks %q: %q", want, stderr)
		}
	}
	if got := cliGitOutput(t, folder, "config", "--get", "remote.origin.url"); got != origin {
		t.Fatalf("refused setup changed the URL to %q", got)
	}

	cliGit(t, folder, "config", "remote.origin.url", respelled)
	code, stdout, stderr := run(t, folder, "setup", "--tasks-only", respelled, "--json")
	if code != 0 {
		t.Fatalf("setup after changing the URL = (%d, %q, %q)", code, stdout, stderr)
	}
	report := decodeTasksOnlySetup(t, stdout)
	if report.TasksOnly.Remote != respelled || len(report.TasksOnly.Changed) != 0 || report.Sync.Status != "completed" {
		t.Fatalf("tasksOnly = %+v, sync = %q; want the new URL kept, nothing changed and a completed sync", report.TasksOnly, report.Sync.Status)
	}
	if code, _, stderr := run(t, folder, "setup"); code != 0 {
		t.Fatalf("plain setup after changing the URL = %d; stderr = %q", code, stderr)
	}
}

// A key that is not origin's founding key is refused before the folder is
// touched, so the retry with the right key, or with none, runs in the same
// folder.
func TestSetupTasksOnlyChecksTheKeyBeforeCreatingAnything(t *testing.T) {
	t.Parallel()
	origin, seed, _ := tasksOnlyOriginWithCode(t)
	contents, err := os.ReadFile(filepath.Join(seed, ".workbook", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(contents, &config); err != nil || config.Key == "" || config.Key == "NOPE" {
		t.Fatalf("seed config key = %q (%v)", config.Key, err)
	}

	t.Run("empty folder", func(t *testing.T) {
		t.Parallel()
		folder := t.TempDir()
		code, _, stderr := run(t, folder, "setup", "--tasks-only", origin, "--key", "NOPE", "--json")
		if code != 5 || !strings.Contains(stderr, "its founding key is \\\""+config.Key+"\\\"") {
			t.Fatalf("setup --key NOPE = %d; stderr = %q", code, stderr)
		}
		if entries, _ := os.ReadDir(folder); len(entries) != 0 {
			t.Fatalf("refused setup left %d entries in the folder", len(entries))
		}
		if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin, "--key", config.Key); code != 0 {
			t.Fatalf("setup with the founding key = (%d, %q, %q)", code, stdout, stderr)
		}
	})

	t.Run("hand-initialized folder", func(t *testing.T) {
		t.Parallel()
		folder := t.TempDir()
		cliGit(t, folder, "init", "--quiet")
		before := cliGitOutput(t, folder, "config", "--local", "--list")
		code, _, stderr := run(t, folder, "setup", "--tasks-only", origin, "--key", "NOPE")
		if code != 5 || !strings.Contains(stderr, "founding key") {
			t.Fatalf("setup --key NOPE = %d; stderr = %q", code, stderr)
		}
		if after := cliGitOutput(t, folder, "config", "--local", "--list"); after != before {
			t.Fatalf("refused setup changed the folder's settings:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if refs := cliGitOutput(t, folder, "for-each-ref"); refs != "" {
			t.Fatalf("refused setup left refs: %s", refs)
		}
	})
}

// The Mode line says "set" for a setting the folder did not have and
// "restored" only for one somebody changed.
func TestSetupTasksOnlyModeLineTellsSetFromRestored(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)

	adopted := t.TempDir()
	cliGit(t, adopted, "init", "--quiet")
	code, stdout, stderr := run(t, adopted, "setup", "--tasks-only", origin)
	want := "Mode:\ttasks-only\t(set workbook.tasksOnly, remote.origin.tagOpt, remote.origin.fetch, remote.origin.url)\n"
	if code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("adopting setup = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}

	cliGit(t, adopted, "config", "--unset", "remote.origin.tagOpt")
	cliGit(t, adopted, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	code, stdout, stderr = run(t, adopted, "setup")
	want = "Mode:\ttasks-only\t(set remote.origin.tagOpt; restored remote.origin.fetch)\n"
	if code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("repairing setup = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
}

// A marked folder that never got its remote URL — what a first run
// interrupted before its last write leaves — is told which command finishes
// it, and that command does.
func TestSetupInATasksOnlyFolderWithoutARemoteNamesTheCommand(t *testing.T) {
	t.Parallel()
	origin, _, _ := tasksOnlyOriginWithCode(t)
	folder := t.TempDir()
	cliGit(t, folder, "init", "--quiet")
	cliGit(t, folder, "config", "workbook.tasksOnly", "true")
	cliGit(t, folder, "config", "remote.origin.tagOpt", "--no-tags")
	cliGit(t, folder, "config", "remote.origin.fetch", "^refs/*")
	before := cliGitOutput(t, folder, "config", "--local", "--list")

	code, _, stderr := run(t, folder, "setup", "--json")
	if code != 5 {
		t.Fatalf("setup code = %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		cliGitOutput(t, folder, "rev-parse", "--show-toplevel")+" is a tasks-only folder with no remote yet; run workbook setup --tasks-only <url> here with the URL of the project's remote")
	if after := cliGitOutput(t, folder, "config", "--local", "--list"); after != before {
		t.Fatalf("refused setup changed the folder's settings:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin)
	if code != 0 || !strings.Contains(stdout, "Mode:\ttasks-only\t(set remote.origin.url)\n") {
		t.Fatalf("finishing setup = (%d, %q, %q)", code, stdout, stderr)
	}
}
