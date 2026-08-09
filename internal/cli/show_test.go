package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestShowWithoutHistoryFlagsIsUnchanged(t *testing.T) {
	// Mutation caught: making the change log part of ordinary show, which
	// changes what every existing consumer reads.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	run(t, repository, "update", task.ID, "--status", "ready", "--no-sync")

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "Showing") || strings.Contains(stdout, "changed") {
		t.Fatalf("plain show output = %q, want no change log", stdout)
	}

	code, stdout, stderr = run(t, repository, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show --json code = %d, want 0; stderr = %q", code, stderr)
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &members); err != nil {
		t.Fatalf("decode show data: %v", err)
	}
	if _, present := members["history"]; present {
		t.Fatalf("plain show data = %v, want the history member omitted entirely", members)
	}
	if _, present := members["comparison"]; present {
		t.Fatalf("plain show data = %v, want the comparison member omitted entirely", members)
	}
}

func TestShowHistoryListsChangesAlongTheCommitChain(t *testing.T) {
	// Mutation caught: listing one row per operation rather than per pack, or
	// losing the field-level detail under each row.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	run(t, repository, "update", task.ID, "--status", "ready", "--no-sync")
	run(t, repository, "update", task.ID, "--title", "Renamed", "--priority", "high", "--no-sync")
	run(t, repository, "update", task.ID, "--description", "Alpha beta delta.", "--no-sync")

	code, stdout, stderr := run(t, repository, "show", task.ID, "--history")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"created the task",
		"changed status",
		"changed title and priority",
		"changed description",
		"Status:\tbacklog → ready",
		"Title:\tOriginal → Renamed",
		"Priority:\tmedium → high",
		"Description:\tAlpha beta [-gamma.-]{+delta.+}",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("show --history output = %q, want %q", stdout, want)
		}
	}
	if got := strings.Count(stdout, "changed title and priority"); got != 1 {
		t.Fatalf("multi-field pack rows = %d, want one row naming both fields", got)
	}
}

func TestShowHistoryWindowsAndReportsWhatItOmitted(t *testing.T) {
	// Mutation caught: truncating silently, so a reader cannot tell a short
	// history from a windowed one.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	for _, status := range []string{"ready", "in-progress", "in-review", "done"} {
		run(t, repository, "update", task.ID, "--status", status, "--no-sync")
	}

	code, stdout, _ := run(t, repository, "show", task.ID, "--history", "--limit", "2")
	if code != 0 {
		t.Fatalf("show --history --limit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Showing 2 most recent changes out of 5.") {
		t.Fatalf("windowed output = %q, want the omitted count reported", stdout)
	}
	if strings.Contains(stdout, "backlog → ready") {
		t.Fatalf("windowed output = %q, want the oldest changes omitted", stdout)
	}

	code, stdout, _ = run(t, repository, "show", task.ID, "--history", "--all")
	if code != 0 {
		t.Fatalf("show --history --all code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Showing all 5 change(s).") || !strings.Contains(stdout, "backlog → ready") {
		t.Fatalf("--all output = %q, want every change", stdout)
	}
}

func TestShowHistoryNestsTheChangeLogInsideTheTask(t *testing.T) {
	// Mutation caught: returning the change log beside the task, which would
	// make the show envelope two shapes depending on a flag.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	run(t, repository, "update", task.ID, "--status", "ready", "--no-sync")

	code, stdout, stderr := run(t, repository, "show", task.ID, "--history", "--json")
	if code != 0 {
		t.Fatalf("show --history --json code = %d, want 0; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode show detail: %v", err)
	}
	if detail.ID != task.ID {
		t.Fatalf("detail ID = %q, want %q", detail.ID, task.ID)
	}
	if detail.History == nil || detail.History.Total != 2 || detail.History.Showing != 2 {
		t.Fatalf("history = %#v, want two changes", detail.History)
	}
	newest := detail.History.Changes[1]
	if newest.Commit != detail.Head || newest.LogicalClock != 2 {
		t.Fatalf("newest change = %#v, want the current head at clock 2", newest)
	}
	if len(newest.Fields) != 1 || newest.Fields[0].Field != "status" || newest.Fields[0].To != "ready" {
		t.Fatalf("newest fields = %#v, want the status change", newest.Fields)
	}
}

func TestShowCompareDiffsTwoCommitsInTheOrderGiven(t *testing.T) {
	// Mutation caught: sorting the two arguments, which would make a comparison
	// meaningless once a reconciliation detached ULID order from chain position.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	root := task.Head
	run(t, repository, "update", task.ID, "--status", "ready", "--no-sync")
	code, stdout, _ := run(t, repository, "update", task.ID, "--title", "Renamed", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("update code = %d, want 0", code)
	}
	head := decodeMutationTask(t, stdout, "update").Head

	forward := showComparison(t, repository, task.ID, root, head)
	if forward.From != root || forward.To != head {
		t.Fatalf("comparison endpoints = %q → %q, want %q → %q", forward.From, forward.To, root, head)
	}
	assertFieldChange(t, forward.Fields, "title", "Original", "Renamed")
	assertFieldChange(t, forward.Fields, "status", "backlog", "ready")

	reverse := showComparison(t, repository, task.ID, head, root)
	if reverse.From != head || reverse.To != root {
		t.Fatalf("reversed endpoints = %q → %q, want the caller's order preserved", reverse.From, reverse.To)
	}
	assertFieldChange(t, reverse.Fields, "title", "Renamed", "Original")
}

func TestShowCompareReportsAnAbsentCommitAsNotFound(t *testing.T) {
	// Mutation caught: reporting a retired pre-replay tip as corrupt data, which
	// tells a caller to repair a repository that is fine.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	absent := strings.Repeat("0", len(task.Head))

	code, _, stderr := run(t, repository, "show", task.ID, "--compare", absent, task.Head, "--json")
	if code != 4 {
		t.Fatalf("show --compare code = %d, want 4; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotFound, "")
	if !strings.Contains(stderr, absent) {
		t.Fatalf("error = %q, want the named commit reported", stderr)
	}
	if !strings.Contains(stderr, "pre-replay tip") {
		t.Fatalf("error = %q, want the pre-replay explanation", stderr)
	}
}

func TestShowRejectsHistoryOptionsThatWouldDoNothing(t *testing.T) {
	// Mutation caught: accepting a window flag without --history, so a caller who
	// asked for ten changes silently receives a plain task.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "limit without history", args: []string{"--limit", "3"}, message: "--limit requires --history"},
		{name: "all without history", args: []string{"--all"}, message: "--all requires --history"},
		{name: "limit with all", args: []string{"--history", "--limit", "3", "--all"}, message: "cannot use --limit with --all"},
		{name: "limit is not a number", args: []string{"--history", "--limit", "many"}, message: "--limit must be a positive whole number"},
		{name: "limit is zero", args: []string{"--history", "--limit", "0"}, message: "--limit must be a positive whole number"},
		{name: "compare needs two commits", args: []string{"--compare", "abc"}, message: "--compare requires two arguments"},
		{name: "compare rejects an inline value", args: []string{"--compare=abc", "def"}, message: "takes two separate arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := run(t, repository, append([]string{"show", task.ID}, test.args...)...)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stderr = %q", code, stderr)
			}
			assertHumanError(t, stderr, test.message)
		})
	}
}

func TestShowCompareKeepsJSONModeAfterItsTwoValues(t *testing.T) {
	// Mutation caught: a pair option that consumes one argument, leaving the
	// second commit to be mistaken for the end of the flags and the error
	// envelope rendered as prose to a machine caller.
	repository := initializedRepository(t)
	task := createShowTask(t, repository)
	absent := strings.Repeat("0", len(task.Head))

	_, _, stderr := run(t, repository, "show", task.ID, "--compare", absent, task.Head, "--json")
	assertJSONError(t, stderr, core.CategoryNotFound, "")
}

func TestShowRendersTheDescriptionAsItWasWritten(t *testing.T) {
	// Mutation caught: reprinting the description as one collapsed line, or
	// turning its trailing newline into a blank line that reads as the end of
	// the field. writeShow is called directly because the shape of the block
	// between "Description:" and the next field is the whole assertion.
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{name: "empty", description: "", want: "Description:\t\n"},
		{name: "one line", description: "one line", want: "Description:\tone line\n"},
		{name: "paragraphs", description: "first\n\nsecond", want: "Description:\tfirst\n\n\tsecond\n"},
		{name: "trailing newlines", description: "first\n\n", want: "Description:\tfirst\n"},
		{name: "carriage returns", description: "first\r\nsecond", want: "Description:\tfirst\n\tsecond\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			writeShow(&output, core.Task{TaskData: core.TaskData{Description: test.description}})
			rendered := output.String()
			start := strings.Index(rendered, "Description:")
			end := strings.Index(rendered, "Status:")
			if start < 0 || end < start {
				t.Fatalf("show output = %q, want a description field followed by a status field", rendered)
			}
			if got := rendered[start:end]; got != test.want {
				t.Fatalf("description block = %q, want %q", got, test.want)
			}
		})
	}
}

func createShowTask(t *testing.T, repository string) core.Task {
	t.Helper()
	code, stdout, stderr := run(
		t, repository,
		"create", "Original", "--description", "Alpha beta gamma.", "--no-sync", "--json",
	)
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

func showComparison(t *testing.T, repository, taskID, from, to string) core.Comparison {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", taskID, "--compare", from, to, "--json")
	if code != 0 {
		t.Fatalf("show --compare code = %d, want 0; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode show detail: %v", err)
	}
	if detail.Comparison == nil {
		t.Fatal("comparison = nil, want a field-level diff")
	}
	return *detail.Comparison
}

func assertFieldChange(t *testing.T, fields []core.FieldChange, field, from, to string) {
	t.Helper()
	for _, change := range fields {
		if change.Field == field {
			if change.From != from || change.To != to {
				t.Fatalf("%s change = %q → %q, want %q → %q", field, change.From, change.To, from, to)
			}
			return
		}
	}
	t.Fatalf("comparison = %#v, want a %s entry", fields, field)
}
