package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestRunListFindNarrowsByText(t *testing.T) {
	t.Parallel()
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

// A pasted task ID is a search, and `list --find` answers it with the one task
// it names. A fragment from the middle of a ULID names nothing: an ID is matched
// from its beginning only, so a word that happens to occur inside one never
// drags a task into an answer.
func TestRunListFindNarrowsByTaskIDPrefix(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	first := createOrderingTask(t, repository, "Audit card abilities", "high")
	createOrderingTask(t, repository, "Performance audit", "low")
	createOrderingTask(t, repository, "Trip templates", "medium")

	listIDs := func(find string) []string {
		t.Helper()
		code, stdout, stderr := run(t, repository, "list", "--find", find, "--json")
		if code != 0 {
			t.Fatalf("list --find %q code = %d, want 0; stderr = %q", find, code, stderr)
		}
		result := assertJSONResult(t, stdout, "list")
		var tasks []core.Task
		if err := json.Unmarshal(result.Data, &tasks); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		ids := make([]string, 0, len(tasks))
		for _, task := range tasks {
			ids = append(ids, task.ID)
		}
		return ids
	}

	// The key, the dash and enough of the ULID to be nobody else's. Sixteen
	// characters rather than twelve: the first ten of a ULID are its timestamp,
	// and three tasks created in the same millisecond share them, so a shorter
	// prefix could pass while matching nothing but the clock.
	prefix := first.ID[:16]
	if got := listIDs(prefix); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("list --find %q = %v, want only %s", prefix, got, first.ID)
	}
	// A pasted ID arrives however it was pasted, and the whole of one is a prefix
	// of itself.
	if got := listIDs(strings.ToLower(first.ID)); len(got) != 1 || got[0] != first.ID {
		t.Fatalf("list --find the whole ID lower-cased = %v, want only %s", got, first.ID)
	}
	// Eight characters out of the middle of the same ULID.
	middle := first.ID[8:16]
	if got := listIDs(middle); len(got) != 0 {
		t.Fatalf("list --find %q = %v, want nothing: an ID matches only from its beginning", middle, got)
	}
	// The key alone is a word, not a prefix: it appears in no title here, so it
	// finds nothing rather than everything.
	if got := listIDs("WB-"); len(got) != 0 {
		t.Fatalf("list --find \"WB-\" = %v, want nothing: a key and a dash name no ID", got)
	}
}

func TestRunListFindRefusesAnEmptyQuery(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, value := range []string{"", "   "} {
		code, stdout, stderr := run(t, repository, "list", "--find", value, "--json")
		if code != 2 || stdout != "" {
			t.Fatalf("list --find %q code/stdout = %d/%q, want 2/empty; stderr = %q", value, code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "list --find needs at least one word")
	}
}
