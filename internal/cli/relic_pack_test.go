package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

// The tests in this file pin, at the command line, when Workbook packs the
// relic namespaces (gitstore.Repository.PackMovedRelicRefs): after a run that
// moved a relic ref, never after a task command or a relic read, and with no
// trace in a command's result when Git refuses or fails the pack.

// packRefsLines is the pack-refs command lines a traced run started.
func packRefsLines(cost gitCost) []string {
	var lines []string
	for _, line := range cost.lines {
		if strings.Contains(line, "pack-refs") {
			lines = append(lines, line)
		}
	}
	return lines
}

// looseRelicRefFiles counts the loose ref files under refs/workbook/relics/ in
// a clone. Only a test looks at a repository's files.
func looseRelicRefFiles(t *testing.T, clone string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(clone, ".git", "refs", "workbook", "relics"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestASyncThatBringsRelicsPacksThem(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	first, second := cliSyncRepositories(t)
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		mustCreateRelic(t, first, title, "--type", "paper")
	}
	cliGit(t, second, "branch", "stays-loose")

	cost, _ := tracedCommand(t, binary, second, nil, "sync", "--json")
	packs := packRefsLines(cost)
	if len(packs) != 1 {
		t.Fatalf("sync ran %d pack-refs, want 1:\n%s", len(packs), strings.Join(cost.lines, "\n"))
	}
	if strings.Contains(packs[0], "--all") || !strings.Contains(packs[0], "--include") {
		t.Fatalf("the pack is %q, want one limited by --include", packs[0])
	}
	if got := looseRelicRefFiles(t, second); got != 0 {
		t.Fatalf("%d relic refs are loose after the sync", got)
	}
	if _, err := os.Stat(filepath.Join(second, ".git", "refs", "heads", "stays-loose")); err != nil {
		t.Fatalf("the loose branch was packed: %v", err)
	}

	// Nothing moved this time, so nothing is packed.
	cost, _ = tracedCommand(t, binary, second, nil, "sync", "--json")
	if packs := packRefsLines(cost); len(packs) != 0 {
		t.Fatalf("a sync that moved no relic ran %q", packs)
	}
}

// A task command and a relic read command run no pack-refs, with relics
// loose in the checkout. A relic change does.
func TestTaskCommandsAndRelicReadsRunNoPack(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	first, second := cliSyncRepositories(t)
	mustCreateRelic(t, first, "On origin", "--type", "paper")
	relicID := mustCreateRelic(t, second, "Here", "--type", "paper", "--no-sync").Relic.ID
	task := cliCreateTask(t, second, "A task")
	for _, args := range [][]string{
		{"create", "Traced", "--json"},
		{"update", task.ID, "--title", "Traced update", "--json"},
		{"next", "--json"},
		{"list", "--json"},
		{"show", task.ID, "--json"},
		{"relic", "list", "--json"},
		{"relic", "show", relicID, "--json"},
	} {
		cost, _ := tracedCommand(t, binary, second, nil, args...)
		if packs := packRefsLines(cost); len(packs) != 0 {
			t.Errorf("%v ran %q", args, packs)
		}
	}
	cost, _ := tracedCommand(t, binary, second, nil, "relic", "update", relicID, "--title", "Changed", "--json")
	if packs := packRefsLines(cost); len(packs) != 1 {
		t.Fatalf("relic update ran %d pack-refs, want 1:\n%s", len(packs), strings.Join(cost.lines, "\n"))
	}
}

// With a Git that refuses pack-refs --include, or one whose pack fails, a
// sync prints and exits exactly as it does with a pack that works.
func TestARefusedOrFailedPackLeavesTheCommandAsItWas(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for name, packRefs := range map[string]string{
		"refused": "echo \"error: unknown option \\`include'\" >&2; exit 129",
		"failed":  "echo \"fatal: Unable to create 'packed-refs.lock': File exists.\" >&2; exit 128",
	} {
		name, packRefs := name, packRefs
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			for _, title := range []string{"One", "Two", "Three"} {
				mustCreateRelic(t, first, title, "--type", "paper")
			}
			third := filepath.Join(t.TempDir(), "clone")
			if err := os.MkdirAll(third, 0o755); err != nil {
				t.Fatal(err)
			}
			copyTree(t, second, third)

			fake := t.TempDir()
			testenv.WriteExecutable(t, filepath.Join(fake, "git"), []byte(
				"#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = pack-refs ]; then\n    "+packRefs+
					"\n  fi\ndone\nexec '"+real+"' \"$@\"\n"))
			runSync := func(clone string, env ...string) (int, string, string) {
				command := exec.Command(binary, "sync", "--json")
				command.Dir = clone
				command.Env = append(os.Environ(), env...)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				code := 0
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else if err != nil {
					t.Fatal(err)
				}
				return code, stdout.String(), stderr.String()
			}
			wantCode, wantStdout, wantStderr := runSync(second)
			code, stdout, stderr := runSync(third, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
			if code != wantCode || stdout != wantStdout || stderr != wantStderr {
				t.Fatalf("with a %s pack sync = (%d, %q, %q), want (%d, %q, %q)",
					name, code, stdout, stderr, wantCode, wantStdout, wantStderr)
			}
			if got := looseRelicRefFiles(t, second); got != 0 {
				t.Fatalf("the working pack left %d relic refs loose", got)
			}
			if got := looseRelicRefFiles(t, third); got != 3 {
				t.Fatalf("the %s pack left %d relic refs loose, want 3: the fake was not used", name, got)
			}
		})
	}
}
