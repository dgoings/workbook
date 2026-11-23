package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// removedBoardMessage is the whole refusal `workbook board` gets now that the
// terminal board is gone: the unknown-command error every other unknown name
// gets, and one sentence saying where the board went.
const removedBoardMessage = `unknown command "board". It was removed; ` +
	"`workbook serve` opens the board in a browser."

// The terminal board was removed in v0.7.0. Somebody who still types it, or a
// script that still calls it, is refused the way any unknown command is — the
// same exit code, the same usage text — and told where the board went, rather
// than being left to wonder whether the name was mistyped.
func TestRemovedBoardCommandIsRefusedWithAPointerToServe(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	for _, args := range [][]string{
		{"board"},
		{"board", "--wide"},
		{"board", "--narrow"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 2 {
			t.Fatalf("%v code = %d, want 2; stderr = %q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want nothing", args, stdout)
		}
		assertHumanError(t, stderr, "workbook: "+removedBoardMessage+"\n")
		if !strings.Contains(stderr, "Usage: workbook <command> [arguments]") {
			t.Fatalf("%v stderr = %q, want the usage every unknown command gets", args, stderr)
		}
		if strings.Contains(stderr, "  board ") {
			t.Fatalf("%v stderr = %q, want a usage that no longer lists board", args, stderr)
		}
	}

	code, stdout, stderr := run(t, repository, "board", "--json")
	if code != 2 {
		t.Fatalf("board --json code = %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("board --json stdout = %q, want nothing", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, removedBoardMessage)

	// Asking for its help is the same question, and gets the same answer.
	code, stdout, stderr = run(t, repository, "help", "board")
	if code != 2 {
		t.Fatalf("help board code = %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("help board stdout = %q, want nothing", stdout)
	}
	assertHumanError(t, stderr, "workbook: "+removedBoardMessage+"\n")

	// Any other unknown name is still refused without the pointer, so the
	// sentence is about this command rather than appended to every typo.
	code, _, stderr = run(t, repository, "boards")
	if code != 2 {
		t.Fatalf("boards code = %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stderr, "workbook: unknown command \"boards\"\n") {
		t.Fatalf("boards stderr = %q, want the plain unknown-command error", stderr)
	}
}

// Nothing that lists the command surface offers the removed command: not the
// global help, and not any shell's completion.
func TestRemovedBoardCommandIsNotOffered(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "help")
	if code != 0 {
		t.Fatalf("help code = %d; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "  board ") {
		t.Fatalf("help = %q, want no board command listed", stdout)
	}
	if strings.Contains(usage, "  board ") {
		t.Fatalf("usage = %q, want no board command listed", usage)
	}
	// Each shell names a command its own way: bash in a space-separated word
	// list, zsh as a 'name:description' entry, fish as an -a argument. The
	// descriptions still say "board", meaning the web board, so the check is
	// for the command entry rather than the word.
	for shell, pattern := range map[string]*regexp.Regexp{
		"bash": regexp.MustCompile(`'[^']*\bboard\b[^']*'`),
		"zsh":  regexp.MustCompile(`'board:`),
		"fish": regexp.MustCompile(`-a 'board'`),
	} {
		code, stdout, stderr := run(t, repository, "completion", shell)
		if code != 0 {
			t.Fatalf("completion %s code = %d; stderr = %q", shell, code, stderr)
		}
		// The pattern has to find the commands that remain, or a format change
		// would make this check pass on nothing.
		if !regexp.MustCompile(strings.ReplaceAll(pattern.String(), "board", "serve")).MatchString(stdout) {
			t.Fatalf("completion %s does not name serve the way the check expects:\n%s", shell, stdout)
		}
		if match := pattern.FindString(stdout); match != "" {
			t.Fatalf("completion %s offers board in %q", shell, match)
		}
	}
}
