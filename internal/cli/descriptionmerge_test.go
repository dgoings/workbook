package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// mergeLines is a six-line description with the numbered lines (1-based)
// changed to say who changed them. Edits at least one line apart merge.
func mergeLines(changed map[int]string) string {
	var text strings.Builder
	for line := 1; line <= 6; line++ {
		if value, ok := changed[line]; ok {
			text.WriteString(value + "\n")
			continue
		}
		fmt.Fprintf(&text, "line %d\n", line)
	}
	return text.String()
}

var (
	mergeBaseText   = mergeLines(nil)
	mergeTheirText  = mergeLines(map[int]string{2: "their line 2"})
	mergeOurText    = mergeLines(map[int]string{5: "our line 5"})
	mergeResultText = mergeLines(map[int]string{2: "their line 2", 5: "our line 5"})
)

// pendingDescriptionMerge leaves second one synchronization away from merging
// a description: origin changed line 2 of a task, and second holds an
// unpublished change to line 5 of it.
func pendingDescriptionMerge(t *testing.T) (first, second string, task core.Task) {
	t.Helper()
	first, second = cliSyncRepositories(t)
	task = cliCreateTask(t, first, "Merged description")
	compactRun(t, first, "update", task.ID, "--description", mergeBaseText)
	compactRun(t, second, "sync")
	compactRun(t, second, "update", task.ID, "--description", mergeOurText, "--no-sync")
	compactRun(t, first, "update", task.ID, "--description", mergeTheirText)
	return first, second, task
}

func mergedDescriptionMessage(taskID, remote string) string {
	return "task " + taskID + ": the description was changed here and on " + remote +
		"; the two edits changed different lines and were merged; `workbook show " + taskID +
		" --history` shows the merge"
}

// requireMergedDescriptionWarning requires a JSON result's warnings to hold the
// description-merged warning for taskID exactly once.
func requireMergedDescriptionWarning(t *testing.T, stdout, taskID, remote string) {
	t.Helper()
	var envelope struct {
		Warnings []core.Warning `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode envelope: %v; output = %q", err, stdout)
	}
	var merged []core.Warning
	for _, warning := range envelope.Warnings {
		if warning.Code == core.WarningDescriptionMerged {
			merged = append(merged, warning)
		}
	}
	want := core.Warning{Code: core.WarningDescriptionMerged, Message: mergedDescriptionMessage(taskID, remote)}
	if len(merged) != 1 || merged[0] != want {
		t.Fatalf("warnings = %+v, want %+v once", envelope.Warnings, want)
	}
}

func requireMergedDescriptionLine(t *testing.T, output, prefix, taskID, remote string) {
	t.Helper()
	line := prefix + mergedDescriptionMessage(taskID, remote) + "\n"
	if got := strings.Count(output, line); got != 1 {
		t.Fatalf("output = %q, want %q once, got %d", output, line, got)
	}
}

func TestSyncReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	t.Run("json", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		code, stdout, stderr := run(t, second, "sync", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("sync --json = (%d, %q, %q)", code, stdout, stderr)
		}
		requireMergedDescriptionWarning(t, stdout, task.ID, "origin")
		if got := showTask(t, second, task.ID).Description; got != mergeResultText {
			t.Fatalf("description = %q, want %q", got, mergeResultText)
		}
		// The merge was made once, so it is reported once.
		if again := compactRun(t, second, "sync", "--json"); strings.Contains(again, core.WarningDescriptionMerged) {
			t.Fatalf("second sync = %q, want no description-merged warning", again)
		}
	})
	t.Run("text", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		code, stdout, stderr := run(t, second, "sync")
		if code != 0 {
			t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
		}
		requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task.ID, "origin")
		if strings.Contains(stdout, "were merged") {
			t.Fatalf("stdout = %q, want the warning on stderr only", stdout)
		}
	})
}

func TestFetchReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	t.Run("json", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		requireMergedDescriptionWarning(t, compactRun(t, second, "fetch", "--json"), task.ID, "origin")
	})
	t.Run("text", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		_, _, stderr := run(t, second, "fetch")
		requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task.ID, "origin")
	})
}

// Setup reports what its synchronization has to say as lines of its report.
func TestSetupReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	t.Run("json", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		requireMergedDescriptionWarning(t, compactRun(t, second, "setup", "--json"), task.ID, "origin")
	})
	t.Run("text", func(t *testing.T) {
		t.Parallel()
		_, second, task := pendingDescriptionMerge(t)
		requireMergedDescriptionLine(t, compactRun(t, second, "setup"), "Warning:\t", task.ID, "origin")
	})
}

// A task command's own synchronization reports a merge of any task, not only
// of the one the command names.
func TestTaskCommandSyncReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"update":       {"update", "OTHER", "--title", "Renamed"},
		"create":       {"create", "Another task"},
		"next":         {"next"},
		"next limit":   {"next", "--limit", "2"},
		"next claim":   {"next", "--claim"},
		"relic create": {"relic", "create", "Made here", "--type", "paper"},
		"status add":   {"status", "add", "parked"},
		"compact":      {"compact"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			other := cliCreateTask(t, first, "Other task")
			task := cliCreateTask(t, first, "Merged description")
			compactRun(t, first, "update", task.ID, "--description", mergeBaseText)
			compactRun(t, first, "push")
			compactRun(t, second, "sync")
			compactRun(t, second, "update", task.ID, "--description", mergeOurText, "--no-sync")
			compactRun(t, first, "update", task.ID, "--description", mergeTheirText)
			command := slices.Clone(args)
			if index := slices.Index(command, "OTHER"); index >= 0 {
				command[index] = other.ID
			}
			code, stdout, stderr := run(t, second, append(command, "--json")...)
			if code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", command, code, stdout, stderr)
			}
			requireMergedDescriptionWarning(t, stdout, task.ID, "origin")
		})
	}
	for name, args := range map[string][]string{
		"update text": {"update", "--title", "Renamed"},
		"next text":   {"next"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second, task := pendingDescriptionMerge(t)
			command := slices.Clone(args)
			if command[0] == "update" {
				other := cliCreateTask(t, first, "Other task")
				compactRun(t, first, "push")
				command = []string{"update", other.ID, "--title", "Renamed"}
			}
			code, stdout, stderr := run(t, second, command...)
			if code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", command, code, stdout, stderr)
			}
			requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task.ID, "origin")
		})
	}
}

// A command stopped by its own task's conflict still says which other
// description its fetch merged: no later fetch makes that merge again.
func TestConflictedCommandReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			stopped := cliCreateTask(t, first, "Conflicting task")
			task := cliCreateTask(t, first, "Merged description")
			compactRun(t, first, "update", task.ID, "--description", mergeBaseText)
			compactRun(t, first, "push")
			compactRun(t, second, "sync")
			compactRun(t, second, "update", stopped.ID, "--description", "Our text", "--no-sync")
			compactRun(t, second, "update", task.ID, "--description", mergeOurText, "--no-sync")
			compactRun(t, first, "update", stopped.ID, "--description", "Their text")
			compactRun(t, first, "update", task.ID, "--description", mergeTheirText)
			args := []string{"update", stopped.ID, "--title", "Renamed"}
			if jsonMode {
				args = append(args, "--json")
			}
			code, stdout, stderr := run(t, second, args...)
			if code != 8 {
				t.Fatalf("%v = (%d, %q, %q), want 8", args, code, stdout, stderr)
			}
			if jsonMode {
				requireMergedDescriptionWarning(t, stdout, task.ID, "origin")
				return
			}
			requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task.ID, "origin")
		})
	}
}

// compact --adopt fetches first, and that fetch's adoption replay can merge.
func TestCompactAdoptReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			task := publishedDoneTask(t, first, "Adopted and merged")
			compactRun(t, first, "update", task.ID, "--description", mergeBaseText)
			compactRun(t, second, "sync")
			compactRun(t, second, "update", task.ID, "--description", mergeOurText, "--no-sync")
			compactRun(t, first, "update", task.ID, "--description", mergeTheirText)
			compactRun(t, first, "compact", "--no-backup")
			args := []string{"compact", "--adopt", task.ID}
			if jsonMode {
				args = append(args, "--json")
			}
			code, stdout, stderr := run(t, second, args...)
			if code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
			}
			if jsonMode {
				requireMergedDescriptionWarning(t, stdout, task.ID, "origin")
			} else {
				requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task.ID, "origin")
			}
			if got := showTask(t, second, task.ID).Description; got != mergeResultText {
				t.Fatalf("description = %q, want %q", got, mergeResultText)
			}
		})
	}
}

// A teammate's edit published to the old place is merged into this checkout's
// own while move-tasks carries it across.
func TestMoveTasksReportsAMergedDescription(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			t.Parallel()
			project := codeProject(t, testrepo.FormatSHA1)
			task := project.ids[0]
			compactRun(t, project.seed, "update", task, "--description", mergeBaseText)
			teammate := cloneCode(t, project)
			compactRun(t, teammate, "setup")
			compactRun(t, project.seed, "update", task, "--description", mergeOurText, "--no-sync")
			compactRun(t, teammate, "update", task, "--description", mergeTheirText)
			compactRun(t, project.seed, "config", "set", "tasks-remote", "tasks", "--url", project.tasks)
			args := []string{"move-tasks"}
			if jsonMode {
				args = append(args, "--json")
			}
			code, stdout, stderr := run(t, project.seed, args...)
			if code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
			}
			if jsonMode {
				requireMergedDescriptionWarning(t, stdout, task, "origin")
			} else {
				requireMergedDescriptionLine(t, stderr, "workbook: warning: ", task, "origin")
			}
			if got := cliShowTask(t, project.seed, task).Description; got != mergeResultText {
				t.Fatalf("description = %q, want %q", got, mergeResultText)
			}
		})
	}
}

// mergedTask is a task second holds after merging its description.
func mergedTask(t *testing.T) (second string, task core.Task) {
	t.Helper()
	_, second, task = pendingDescriptionMerge(t)
	compactRun(t, second, "sync")
	return second, task
}

func TestShowHistoryMarksAMergedDescription(t *testing.T) {
	t.Parallel()
	second, task := mergedTask(t)

	var detail struct {
		History struct {
			Changes []struct {
				Summary string `json:"summary"`
				Fields  []struct {
					Field string            `json:"field"`
					Kind  string            `json:"kind"`
					From  string            `json:"from"`
					To    string            `json:"to"`
					Merge map[string]string `json:"merge"`
				} `json:"fields"`
			} `json:"changes"`
		} `json:"history"`
	}
	stdout := compactRun(t, second, "show", task.ID, "--history", "--json")
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode history: %v; output = %q", err, stdout)
	}
	history := detail.History
	if len(history.Changes) < 2 {
		t.Fatalf("history = %q, want the changes", stdout)
	}
	last := history.Changes[len(history.Changes)-1]
	if last.Summary != "changed description" || len(last.Fields) != 1 {
		t.Fatalf("last change = %+v, want one description change", last)
	}
	field := last.Fields[0]
	if field.Field != "description" || field.Kind != "set" || field.From != mergeTheirText || field.To != mergeResultText {
		t.Fatalf("field = %+v, want the description set from origin's text to the merged one", field)
	}
	merge := field.Merge
	if len(merge) != 5 || merge["operation"] == "" || merge["method"] != "line-3way-1" ||
		merge["ancestor"] != mergeBaseText || merge["other"] != mergeOurText || merge["current"] != mergeTheirText {
		t.Fatalf("merge = %#v, want operation, method, ancestor, other and current", merge)
	}
	// Origin's own edit before it is an ordinary change.
	if earlier := history.Changes[len(history.Changes)-2]; earlier.Fields[0].Merge != nil {
		t.Fatalf("earlier change = %+v, want no merge member", earlier)
	}

	text := compactRun(t, second, "show", task.ID, "--history")
	lines := strings.Split(text, "\n")
	const marked = "\t\tmerged two edits that changed different lines (line-3way-1)"
	index := slices.Index(lines, marked)
	if index < 1 || !strings.HasPrefix(lines[index-1], "\tDescription:\t") || strings.Count(text, marked) != 1 {
		t.Fatalf("history = %q, want %q once, after the description line", text, marked)
	}
}

// show answers the merged text, with the members every task has.
func TestShowOfAMergedTaskIsUnchanged(t *testing.T) {
	t.Parallel()
	second, task := mergedTask(t)
	plain := cliCreateTask(t, second, "Never merged")
	compactRun(t, second, "update", plain.ID, "--description", mergeBaseText)

	members := func(id string) (map[string]any, string) {
		stdout := compactRun(t, second, "show", id, "--json")
		var data map[string]any
		if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &data); err != nil {
			t.Fatalf("decode show: %v; output = %q", err, stdout)
		}
		return data, stdout
	}
	merged, stdout := members(task.ID)
	ordinary, _ := members(plain.ID)
	if merged["description"] != mergeResultText {
		t.Fatalf("description = %q, want %q", merged["description"], mergeResultText)
	}
	for name := range merged {
		if _, known := ordinary[name]; !known {
			t.Fatalf("show of a merged task has a %q member an ordinary task lacks: %q", name, stdout)
		}
	}
	if strings.Contains(stdout, `"warnings"`) {
		t.Fatalf("show = %q, want no warning", stdout)
	}
}
