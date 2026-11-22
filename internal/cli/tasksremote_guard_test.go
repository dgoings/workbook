package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// forceTasksRemoteAddress writes a tasks-remote setting naming address,
// whatever config set would say about it, so the checks that do not depend on
// how an address is spelled are the ones tried.
func forceTasksRemoteAddress(t *testing.T, project tasksRemoteProject, address string) {
	t.Helper()
	placeholder := filepath.Join(t.TempDir(), "placeholder.git")
	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", placeholder); code != 0 {
		t.Fatalf("config set tasks-remote = %d; stderr = %q", code, stderr)
	}
	path := filepath.Join(project.seed, ".workbook", "config.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	quoted := func(value string) string {
		return strings.ReplaceAll(value, `\`, `\\`)
	}
	rewritten := strings.Replace(string(contents), quoted(placeholder), quoted(address), 1)
	if rewritten == string(contents) {
		t.Fatalf("config.json does not name %s:\n%s", placeholder, contents)
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The code repository's own address, spelled in any of the ways the
// reviewer found config set accepting, is never the tasks repository:
// config set refuses the spellings it can recognize, and whatever it lets
// through, move-tasks refuses before marking anything and setup refuses in a
// fresh clone, both by what the two repositories hold.
func TestTasksRemoteSameRepositoryIsRefusedHoweverItIsSpelled(t *testing.T) {
	t.Parallel()
	for _, spelling := range []struct {
		name string
		// address spells project.code; caught says config set's first-line
		// check recognizes it.
		address func(t *testing.T, project tasksRemoteProject) string
		caught  bool
	}{
		{"trailing slash", func(_ *testing.T, p tasksRemoteProject) string { return p.code + "/" }, true},
		{"without .git", func(_ *testing.T, p tasksRemoteProject) string { return strings.TrimSuffix(p.code, ".git") }, true},
		{"file scheme", func(_ *testing.T, p tasksRemoteProject) string { return "file://" + p.code }, true},
		{"relative path", func(t *testing.T, p tasksRemoteProject) string {
			relative, err := filepath.Rel(p.seed, p.code)
			if err != nil {
				t.Fatal(err)
			}
			return relative
		}, true},
		{"symbolic link", func(t *testing.T, p tasksRemoteProject) string {
			link := filepath.Join(t.TempDir(), "link.git")
			if err := os.Symlink(p.code, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, true},
		{"upper case", func(_ *testing.T, p tasksRemoteProject) string {
			return filepath.Join(filepath.Dir(p.code), strings.ToUpper(filepath.Base(p.code)))
		}, false},
	} {
		t.Run(spelling.name, func(t *testing.T) {
			t.Parallel()
			project := codeProject(t, testrepo.FormatSHA1)
			address := spelling.address(t, project)
			before := repositoryState(t, project.code)
			if _, err := os.Stat(address); spelling.name == "upper case" && err != nil {
				// A file system that tells case apart holds no such
				// repository: the address names another, missing place, and
				// the move stops at it having changed nothing.
				forceTasksRemoteAddress(t, project, address)
				if code, stdout, stderr := run(t, project.seed, "move-tasks"); code == 0 {
					t.Fatalf("move-tasks to a missing repository = (%d, %q, %q)", code, stdout, stderr)
				}
				if after := repositoryState(t, project.code); after != before {
					t.Fatalf("the failed move changed the code repository:\nbefore %s\nafter  %s", before, after)
				}
				return
			}

			code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", address, "--json")
			if spelling.caught {
				if code != 5 || !strings.Contains(stderr, "origin's address") {
					t.Fatalf("config set %s = (%d, %q), want the first-line refusal", address, code, stderr)
				}
			} else if code != 0 {
				t.Fatalf("config set %s = (%d, %q)", address, code, stderr)
			}
			forceTasksRemoteAddress(t, project, address)

			for _, args := range [][]string{{"move-tasks", "--json"}, {"move-tasks", "--allow-branches", "--json"}} {
				code, stdout, stderr := run(t, project.seed, args...)
				if code != 5 {
					t.Fatalf("%v = (%d, %q, %q), want 5", args, code, stdout, stderr)
				}
				refusal := requireTasksRemoteRefusal(t, stderr, core.CategoryValidation, core.TasksRemoteSameRepository)
				if refusal.Remote != "tasks" {
					t.Fatalf("refusal = %+v", refusal)
				}
			}
			if after := repositoryState(t, project.code); after != before {
				t.Fatalf("the refused move changed the code repository:\nbefore %s\nafter  %s", before, after)
			}
			if remotes := cliGitOutput(t, project.seed, "remote"); strings.Contains(remotes, "tasks") {
				t.Fatalf("the refused move left the remote it added: %s", remotes)
			}

			// A teammate who pulls the setting is refused by setup too.
			cliGit(t, project.seed, "add", ".workbook/config.json")
			cliGit(t, project.seed, "commit", "--quiet", "-m", "Choose a tasks remote")
			cliGit(t, project.seed, "push", "--quiet", "--no-verify", "origin", "main")
			before = repositoryState(t, project.code)
			teammate := cloneCode(t, project)
			if filepath.IsAbs(address) || strings.Contains(address, "://") {
				code, stdout, stderr := run(t, teammate, "setup", "--json")
				if code != 5 {
					t.Fatalf("teammate setup = (%d, %q, %q), want 5", code, stdout, stderr)
				}
				requireTasksRemoteRefusal(t, stderr, core.CategoryValidation, core.TasksRemoteSameRepository)
				if after := repositoryState(t, project.code); after != before {
					t.Fatalf("the refused setup changed the code repository:\nbefore %s\nafter  %s", before, after)
				}
			}
		})
	}
}

// A destination that holds branches is refused unless the person says it is
// meant, and a legitimate tasks repository already filled with this
// project's tasks is not taken for the code repository.
func TestMoveTasksRefusesBranchesUnlessAllowedAndAcceptsAPrefilledCopy(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	cliGit(t, project.seed, "push", "--quiet", "--no-verify", project.tasks, "main:refs/heads/main")
	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set = %d; stderr = %q", code, stderr)
	}
	before := repositoryState(t, project.code)
	code, stdout, stderr := run(t, project.seed, "move-tasks")
	if code != 5 || !strings.Contains(stderr, "holds branches (refs/heads/main)") || !strings.Contains(stderr, "--allow-branches") {
		t.Fatalf("move-tasks into a repository with branches = (%d, %q, %q)", code, stdout, stderr)
	}
	// A mirror of the code repository holds branches too, and the flag must
	// never be given for one; the refusal and the help both say so.
	if !strings.Contains(stderr, "Never pass it for a mirror") {
		t.Fatalf("branches refusal = %q, want the mirror warning", stderr)
	}
	if code, help, helpErr := run(t, project.seed, "move-tasks", "--help"); code != 0 || !strings.Contains(help, "never a mirror of the code repository") ||
		!strings.Contains(help, "Never give it for a mirror") {
		t.Fatalf("move-tasks --help = (%d, %q, %q), want the mirror warning", code, help, helpErr)
	}
	if after := repositoryState(t, project.code); after != before {
		t.Fatalf("the refused move changed origin:\nbefore %s\nafter  %s", before, after)
	}
	if got := refsUnder(t, project.tasks, "refs/workbook/"); got != "" {
		t.Fatalf("the refused move wrote to the tasks repository: %s", got)
	}
	// The same repository, its branches gone and the project's tasks copied
	// in beforehand by hand, is a separate repository and is accepted.
	cliGit(t, project.tasks, "update-ref", "-d", "refs/heads/main")
	cliGit(t, project.seed, "push", "--quiet", "--no-verify", project.tasks, "refs/workbook/*:refs/workbook/*")
	moved := moveTasksAgain(t, project, 0)
	if moved.Marked != "marked" || moved.Identical != len(project.ids) {
		t.Fatalf("move-tasks into a prefilled copy = %+v", moved)
	}
	if got := refsUnder(t, project.tasks, "refs/workbook/probes/"); got != "" {
		t.Fatalf("the move left its probe behind: %s", got)
	}
}

// The refs move-tasks writes under refs/workbook/probes/ to tell the two
// repositories apart are its own: a run removes those an earlier, stopped run
// left behind along with its own. A destination that refuses to delete refs
// is refused once, saying so, and a run after that refusal writes no second
// probe; once deleting is allowed, the next run clears the one left.
func TestMoveTasksClearsLeftoverProbesAndRefusesOnceWhereDeletingIsForbidden(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	if code, _, stderr := run(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks); code != 0 {
		t.Fatalf("config set = %d; stderr = %q", code, stderr)
	}
	head := cliGitOutput(t, project.seed, "rev-parse", "refs/workbook/project")
	probes := func() []string {
		return strings.Fields(cliGitOutput(t, project.tasks, "for-each-ref", "--format=%(refname)", "refs/workbook/probes/"))
	}

	// The first run's own probe cannot be removed, and it says so; the second
	// finds that probe still there and stops before writing another.
	// A hosting service's rule against deleting refs, as a hook that refuses
	// every deletion while the flag file exists.
	deny := filepath.ToSlash(filepath.Join(t.TempDir(), "deny-deletes"))
	testenv.WriteExecutable(t, filepath.Join(project.tasks, "hooks", "pre-receive"), []byte("#!/bin/sh\n"+
		"[ -f '"+deny+"' ] || exit 0\nwhile read old new ref; do\n"+
		"  case \"$new\" in *[!0]*) ;; *) echo \"deletion prohibited: $ref\" >&2; exit 1 ;; esac\ndone\nexit 0\n"))
	if err := os.WriteFile(deny, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var left []string
	for round := 0; round < 2; round++ {
		code, stdout, stderr := run(t, project.seed, "move-tasks")
		if code != 1 || !strings.Contains(stderr, "refuses to delete refs") || !strings.Contains(stderr, "refs/workbook/probes/") {
			t.Fatalf("move-tasks round %d into a repository that refuses deletions = (%d, %q, %q)", round, code, stdout, stderr)
		}
		got := probes()
		if round == 0 {
			left = got
		}
		if len(got) != 1 || got[0] != left[0] {
			t.Fatalf("probes after round %d = %v, want the first run's alone", round, got)
		}
		if got := cliGitOutputAllowingMissing(t, project.code, "refs/workbook/project"); got != head {
			t.Fatalf("the refused move marked origin: %s", got)
		}
	}

	// Once deleting is allowed, the next run removes that probe and any other
	// an earlier run left, here two with names drawn long ago.
	for _, name := range []string{"01AAAAAAAAAAAAAAAAAAAAAAAA", "01BBBBBBBBBBBBBBBBBBBBBBBB"} {
		cliGit(t, project.seed, "push", "--quiet", "--no-verify", project.tasks, head+":refs/workbook/probes/"+name)
	}
	if err := os.Remove(deny); err != nil {
		t.Fatal(err)
	}
	moved := moveTasksAgain(t, project, 0)
	if moved.Marked != "marked" {
		t.Fatalf("move-tasks once deleting is allowed = %+v", moved)
	}
	if got := refsUnder(t, project.tasks, "refs/workbook/probes/"); got != "" {
		t.Fatalf("the move left probes behind: %s", got)
	}
}
