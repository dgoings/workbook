package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestRunListFindNarrowsByText(t *testing.T) {
	repository := initializedRepository(t)
	audit := createOrderingTask(t, repository, "Audit card abilities", "high")
	perf := createOrderingTask(t, repository, "Performance audit", "low")
	createOrderingTask(t, repository, "Trip templates", "medium")

	code, stdout, stderr := run(t, repository, "list", "--find", "AUDIT", "--json")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "list")
	var tasks []core.Task
	if err := json.Unmarshal(result.Data, &tasks); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	if got, want := strings.Join(ids, ","), audit.ID+","+perf.ID; got != want {
		t.Fatalf("list --find = %q, want %q", got, want)
	}

	code, stdout, stderr = run(t, repository, "list", "--find", "audit", "--priority", "low", "--json")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	if err := json.Unmarshal(result.Data, &tasks); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != perf.ID {
		t.Fatalf("list --find audit --priority low = %v, want only %s", tasks, perf.ID)
	}

	code, stdout, stderr = run(t, repository, "list", "--find", "nothing-here", "--json")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	if got := string(result.Data); got != "[]" {
		t.Fatalf("list --find with no match data = %s, want []", got)
	}
}

func TestRunListFindRefusesAnEmptyQuery(t *testing.T) {
	repository := initializedRepository(t)
	for _, value := range []string{"", "   "} {
		code, stdout, stderr := run(t, repository, "list", "--find", value, "--json")
		if code != 2 || stdout != "" {
			t.Fatalf("list --find %q code/stdout = %d/%q, want 2/empty; stderr = %q", value, code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "list --find needs at least one word")
	}
}
