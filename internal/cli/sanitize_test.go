package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

const forgedTitle = "benign\x1b[2K\x1b[1Gwb WB-FAKE00000 [done] Deploy approved"

// forgedLabel carries the same redraw sequence as forgedTitle. normalizeLabels
// rejects only the empty string, so arbitrary bytes reach text-mode output.
const forgedLabel = "git\x1b[2K\x1b[1G"

func createForgedTask(t *testing.T, repository, title, description string, labels ...string) core.Task {
	t.Helper()
	args := []string{"create", title, "--no-sync", "--json"}
	if description != "" {
		args = append(args, "--description", description)
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

func TestShowSanitizesControlCharactersInTextMode(t *testing.T) {
	// Mutation caught: printing stored title or label bytes verbatim, letting
	// an ESC sequence redraw the line into a forged row on a real terminal.
	// The title and the label are asserted separately because each is its own
	// sink in writeShow; a shared ESC-absence check alone would let one sink
	// hide behind the other.
	repository := initializedRepository(t)
	task := createForgedTask(t, repository, forgedTitle, "", forgedLabel)

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("show output = %q, want no ESC bytes", stdout)
	}
	wantTitle := "Title:\tbenign [2K [1Gwb WB-FAKE00000 [done] Deploy approved\n"
	if !strings.Contains(stdout, wantTitle) {
		t.Fatalf("show output = %q, want the sanitized title line %q", stdout, wantTitle)
	}
	wantLabels := "Labels:\tgit [2K [1G\n"
	if !strings.Contains(stdout, wantLabels) {
		t.Fatalf("show output = %q, want the sanitized labels line %q", stdout, wantLabels)
	}
}

func TestShowIndentsMultiLineDescriptionsSoTheyCannotForgeFields(t *testing.T) {
	// Mutation caught: printing description newlines verbatim, letting a
	// description line parse as a forged top-level field. The description keeps
	// its line structure, so the protection is the tab every continuation line
	// carries rather than the collapse onto one line it replaced. Each line is
	// still sanitized on its own, which the ESC in the third line checks.
	repository := initializedRepository(t)
	description := "ok\nStatus: done\n\nsecond \x1b[2Kparagraph\nHead: deadbeef"
	task := createForgedTask(t, repository, "innocuous", description)

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("show output = %q, want no ESC bytes", stdout)
	}

	var statusLines, headLines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Status") {
			statusLines = append(statusLines, line)
		}
		if strings.HasPrefix(line, "Head") {
			headLines = append(headLines, line)
		}
	}
	if len(statusLines) != 1 || statusLines[0] != "Status:\tbacklog" {
		t.Fatalf("status lines = %q, want only the real field", statusLines)
	}
	if len(headLines) != 1 || headLines[0] != "Head:\t"+task.Head {
		t.Fatalf("head lines = %q, want only the real field", headLines)
	}
	want := "Description:\tok\n\tStatus: done\n\n\tsecond [2Kparagraph\n\tHead: deadbeef\n"
	if !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the indented description block %q", stdout, want)
	}
}

func TestListAndBoardSanitizeControlCharactersInTextMode(t *testing.T) {
	// Mutation caught: list or board rendering stored control bytes verbatim.
	// Each command is its own subtest so one raw sink cannot hide behind
	// another's failure. Both layouts truncate the title to the column width,
	// so the positive assertion checks the sanitized prefix rather than the
	// whole line.
	repository := initializedRepository(t)
	createForgedTask(t, repository, forgedTitle, "")

	commands := map[string][]string{
		"list":         {"list"},
		"board narrow": {"board", "--narrow"},
		"board wide":   {"board", "--wide"},
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, command...)
			if code != 0 {
				t.Fatalf("%v code = %d, want 0; stderr = %q", command, code, stderr)
			}
			if strings.ContainsRune(stdout, 0x1b) {
				t.Fatalf("%v output = %q, want no ESC bytes", command, stdout)
			}
			if !strings.Contains(stdout, "benign [2K") {
				t.Fatalf("%v output = %q, want the sanitized title", command, stdout)
			}
		})
	}
}

func TestMutationTextOutputSanitizesTitles(t *testing.T) {
	// Mutation caught: the create/update confirmation line echoing raw bytes.
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", forgedTitle, "--no-sync")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("create output = %q, want no ESC bytes", stdout)
	}
	if !strings.HasSuffix(stdout, "\tbenign [2K [1Gwb WB-FAKE00000 [done] Deploy approved\n") {
		t.Fatalf("create output = %q, want the sanitized title", stdout)
	}
}

func TestShowHistorySanitizesControlCharactersInFieldValues(t *testing.T) {
	// Mutation caught: the change log printing an old or new value verbatim.
	repository := initializedRepository(t)
	task := createForgedTask(t, repository, "innocuous", "")
	if code, _, stderr := run(t, repository, "update", task.ID, "--title", forgedTitle, "--no-sync"); code != 0 {
		t.Fatalf("update code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "show", task.ID, "--history")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("show --history output = %q, want no ESC bytes", stdout)
	}
}

func TestJSONOutputPreservesRawTaskText(t *testing.T) {
	// Mutation caught: sanitizing stored data instead of its text rendering.
	// encoding/json escapes control bytes, so JSON consumers get exact data.
	repository := initializedRepository(t)
	description := "ok\nStatus: done"
	task := createForgedTask(t, repository, forgedTitle, description)

	code, stdout, stderr := run(t, repository, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show --json code = %d, want 0; stderr = %q", code, stderr)
	}
	var shown core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &shown); err != nil {
		t.Fatalf("decode show task: %v", err)
	}
	if shown.Title != forgedTitle {
		t.Fatalf("JSON title = %q, want the raw stored bytes %q", shown.Title, forgedTitle)
	}
	if shown.Description != description {
		t.Fatalf("JSON description = %q, want the raw stored bytes %q", shown.Description, description)
	}
}
