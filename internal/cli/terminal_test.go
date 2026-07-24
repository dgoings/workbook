package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestTerminalWidthRejectsNonFileWriters(t *testing.T) {
	if width, ok := terminalWidth(&bytes.Buffer{}); ok || width != 0 {
		t.Fatalf("terminalWidth(buffer) = (%d, %t), want (0, false)", width, ok)
	}
}

func TestRunBoardDefaultsToNarrowForBufferedOutput(t *testing.T) {
	repository := initializedRepository(t)
	tasks := createTerminalTasks(t, repository)

	code, stdout, stderr := run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board code = %d, stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "BACKLOG (1)\n") {
		t.Fatalf("board output = %q, want narrow board", stdout)
	}
	assertTerminalMembershipAndOrder(t, stdout, tasks)
}

func TestRunBoardHonorsWideAndNarrowOverrides(t *testing.T) {
	repository := initializedRepository(t)
	tasks := createTerminalTasks(t, repository)

	for _, test := range []struct {
		name string
		flag string
		want string
	}{
		{name: "wide", flag: "--wide", want: "+------------------+------------------+------------------+------------------+------------------+\n"},
		{name: "narrow", flag: "--narrow", want: "BACKLOG (1)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, "board", test.flag)
			if code != 0 || stderr != "" {
				t.Fatalf("board %s code = %d, stderr = %q", test.flag, code, stderr)
			}
			if !strings.HasPrefix(stdout, test.want) {
				t.Fatalf("board %s output = %q, want prefix %q", test.flag, stdout, test.want)
			}
			assertTerminalMembershipAndOrder(t, stdout, tasks)
		})
	}
}

func TestRunBoardRejectsConflictingLayoutFlags(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "board", "--wide", "--narrow")
	if code != 2 {
		t.Fatalf("board conflicting flags code = %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("board conflicting flags stdout = %q, want empty", stdout)
	}
	assertHumanError(t, stderr, "cannot use --wide with --narrow")
}

func TestRunBoardJSONIsCompleteAndUntruncated(t *testing.T) {
	repository := initializedRepository(t)
	tasks := createTerminalTasks(t, repository)

	code, stdout, stderr := run(t, repository, "board", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("board --json code = %d, stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "board")
	var got []core.Task
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatalf("decode board tasks: %v", err)
	}
	if len(got) != len(tasks) {
		t.Fatalf("board JSON task count = %d, want %d", len(got), len(tasks))
	}
	for index, task := range tasks {
		if got[index].ID != task.ID || got[index].Title != task.Title || strings.Join(got[index].Labels, ",") != strings.Join(task.Labels, ",") {
			t.Fatalf("board JSON task[%d] = %#v, want ID/title/labels from %#v", index, got[index], task)
		}
	}
	if strings.Contains(string(result.Data), "...") {
		t.Fatalf("board JSON data was truncated: %s", result.Data)
	}
}

func TestRunListUsesResponsiveRendererWithoutChangingJSON(t *testing.T) {
	repository := initializedRepository(t)
	tasks := createTerminalTasks(t, repository)

	code, stdout, stderr := run(t, repository, "list")
	if code != 0 || stderr != "" {
		t.Fatalf("list code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "\t") || !strings.HasPrefix(stdout, "ID") {
		t.Fatalf("list output = %q, want rendered table without tabs", stdout)
	}
	for _, task := range tasks {
		if !strings.Contains(stdout, task.ID) || !strings.Contains(stdout, task.Title) {
			t.Fatalf("list output = %q, missing task %q", stdout, task.Title)
		}
	}

	code, stdout, stderr = run(t, repository, "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list --json code = %d, stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "list")
	var got []core.Task
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatalf("decode list tasks: %v", err)
	}
	if len(got) != len(tasks) {
		t.Fatalf("list JSON task count = %d, want %d", len(got), len(tasks))
	}
	for index, task := range tasks {
		if got[index].ID != task.ID || got[index].Title != task.Title {
			t.Fatalf("list JSON task[%d] = %#v, want %#v", index, got[index], task)
		}
	}
}

func createTerminalTasks(t *testing.T, repository string) []core.Task {
	t.Helper()
	var tasks []core.Task
	for _, input := range []struct {
		title    string
		status   string
		priority string
		labels   []string
	}{
		{title: "Backlog task", status: "backlog", priority: "high", labels: []string{"git", "poc"}},
		{title: "Ready task", status: "ready", priority: "medium", labels: []string{"api"}},
		{title: "Active task", status: "in-progress", priority: "low", labels: []string{"web"}},
	} {
		args := []string{"create", input.title, "--status", input.status, "--priority", input.priority}
		for _, label := range input.labels {
			args = append(args, "--label", label)
		}
		args = append(args, "--json")
		code, stdout, stderr := run(t, repository, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("create %q code = %d, stderr = %q", input.title, code, stderr)
		}
		result := assertJSONResult(t, stdout, "create")
		var task core.Task
		if err := json.Unmarshal(result.Data, &task); err != nil {
			t.Fatalf("decode created task %q: %v", input.title, err)
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func assertTerminalMembershipAndOrder(t *testing.T, output string, tasks []core.Task) {
	t.Helper()
	last := -1
	for _, task := range tasks {
		position := strings.Index(output, task.Title)
		if position < 0 {
			t.Fatalf("terminal output = %q, missing %q", output, task.Title)
		}
		if position <= last {
			t.Fatalf("terminal output orders tasks incorrectly: %q", output)
		}
		last = position
	}
}
