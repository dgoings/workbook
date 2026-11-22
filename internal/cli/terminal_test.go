package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestTerminalWidthRejectsNonFileWriters(t *testing.T) {
	t.Parallel()
	if width, ok := terminalWidth(&bytes.Buffer{}); ok || width != 0 {
		t.Fatalf("terminalWidth(buffer) = (%d, %t), want (0, false)", width, ok)
	}
}

func TestRunListUsesResponsiveRendererWithoutChangingJSON(t *testing.T) {
	t.Parallel()
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
