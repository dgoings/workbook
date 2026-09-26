package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestRunMoveDependFreeAndNext(t *testing.T) {
	repository := initializedRepository(t)
	first := createOrderingTask(t, repository, "First", "high")
	second := createOrderingTask(t, repository, "Second", "high")
	third := createOrderingTask(t, repository, "Third", "medium")

	t.Run("move emits the mutated task", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "move", second.ID, "--before", first.ID, "--json")
		if code != 0 {
			t.Fatalf("move code = %d, want 0; stderr = %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "move")
		var moved core.Task
		if err := json.Unmarshal(result.Data, &moved); err != nil {
			t.Fatalf("decode moved task: %v", err)
		}
		if moved.ID != second.ID || moved.Rank != "1/2" {
			t.Fatalf("moved = %#v, want %s with rank 1/2", moved, second.ID)
		}
	})

	t.Run("move requires exactly one anchor", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "move", first.ID, "--json")
		if code != 2 || stdout != "" {
			t.Fatalf("move missing anchor code/stdout = %d/%q, want 2/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "move requires exactly one of --before or --after")

		code, stdout, stderr = run(t, repository, "move", first.ID, "--before", second.ID, "--after", third.ID, "--json")
		if code != 2 || stdout != "" {
			t.Fatalf("move conflicting anchors code/stdout = %d/%q, want 2/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, "move requires exactly one of --before or --after")
	})

	t.Run("depend and free mutate dependency set", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "depend", third.ID, first.ID, "--json")
		if code != 0 {
			t.Fatalf("depend code = %d, want 0; stderr = %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "depend")
		var dependent core.Task
		if err := json.Unmarshal(result.Data, &dependent); err != nil {
			t.Fatalf("decode dependent task: %v", err)
		}
		if got := strings.Join(dependent.Dependencies, ","); got != first.ID {
			t.Fatalf("dependencies = %q, want %q", got, first.ID)
		}

		code, stdout, stderr = run(t, repository, "free", third.ID, first.ID)
		if code != 0 {
			t.Fatalf("free code = %d, want 0; stderr = %q", code, stderr)
		}
		if !strings.HasPrefix(stdout, third.ID+"\t") {
			t.Fatalf("free stdout = %q, want mutation row", stdout)
		}
		code, stdout, stderr = run(t, repository, "show", third.ID, "--json")
		if code != 0 {
			t.Fatalf("show after free code = %d, want 0; stderr = %q", code, stderr)
		}
		result = assertJSONResult(t, stdout, "show")
		if err := json.Unmarshal(result.Data, &dependent); err != nil {
			t.Fatalf("decode task after free: %v", err)
		}
		if len(dependent.Dependencies) != 0 {
			t.Fatalf("persisted dependencies after free = %q, want empty", dependent.Dependencies)
		}
	})

	t.Run("depend rejects cycles", func(t *testing.T) {
		code, _, stderr := run(t, repository, "depend", first.ID, second.ID, "--json")
		if code != 0 {
			t.Fatalf("first depend code = %d, want 0; stderr = %q", code, stderr)
		}
		code, stdout, stderr := run(t, repository, "depend", second.ID, first.ID, "--json")
		if code != 5 || stdout != "" {
			t.Fatalf("cycle code/stdout = %d/%q, want 5/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "dependency would create a cycle")
	})

	t.Run("next renders task and JSON no-result is null", func(t *testing.T) {
		code, _, stderr := run(t, repository, "free", first.ID, second.ID)
		if code != 0 {
			t.Fatalf("free before next code = %d, want 0; stderr = %q", code, stderr)
		}
		code, stdout, stderr := run(t, repository, "next")
		if code != 0 {
			t.Fatalf("next code = %d, want 0; stderr = %q", code, stderr)
		}
		if !strings.Contains(stdout, "ID:\t"+second.ID) {
			t.Fatalf("next stdout = %q, want first task details", stdout)
		}

		for _, task := range []core.Task{first, second, third} {
			code, _, stderr = run(t, repository, "update", task.ID, "--status", "backlog")
			if code != 0 {
				t.Fatalf("backlog %s code = %d, want 0; stderr = %q", task.ID, code, stderr)
			}
		}
		code, stdout, stderr = run(t, repository, "next", "--json")
		if code != 0 {
			t.Fatalf("empty next code = %d, want 0; stderr = %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "next")
		if string(result.Data) != "null" {
			t.Fatalf("next JSON data = %s, want null", result.Data)
		}
	})
}

func TestRunOrderingCommandsExposeCoreTargetErrors(t *testing.T) {
	repository := initializedRepository(t)
	first := createOrderingTask(t, repository, "First", "high")
	second := createOrderingTask(t, repository, "Second", "high")

	t.Run("move missing anchor", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "move", first.ID, "--before", "WB-NOPE", "--json")
		if code != 4 || stdout != "" {
			t.Fatalf("missing move anchor code/stdout = %d/%q, want 4/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryNotFound, "")
	})

	t.Run("depend tombstoned dependency", func(t *testing.T) {
		code, _, stderr := run(t, repository, "delete", second.ID)
		if code != 0 {
			t.Fatalf("delete dependency code = %d, want 0; stderr = %q", code, stderr)
		}
		code, stdout, stderr := run(t, repository, "depend", first.ID, second.ID, "--json")
		if code != 5 || stdout != "" {
			t.Fatalf("tombstoned dependency code/stdout = %d/%q, want 5/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "cannot add a dependency involving a tombstoned task")
	})
}

// TestRunFreeToleratesTombstonedDependencyButDependRefusesOne exercises the
// stated rule end to end through the CLI verbs rather than through core
// directly: once a dependency task is deleted, `free` must still be able to
// remove it from a dependent — whether it's named by its full ID or by a
// prefix — while `depend` must go on refusing to attach it to anything else.
//
// The rule is asymmetric by design: removal has to tolerate a tombstone or a
// task that once depended on something now-deleted could never be freed of
// it, leaving it permanently (and wrongly) blocked; addition has to refuse a
// tombstone or a brand-new dependency edge could point at a task that will
// never become ready. Asymmetric rules like this are exactly what an
// incautious refactor "tidies" into a symmetric one — reject both endpoints
// everywhere, say — so this test drives both halves against the same deleted
// task in one place to make that regression visible immediately.
func TestRunFreeToleratesTombstonedDependencyButDependRefusesOne(t *testing.T) {
	repository := initializedRepository(t)
	dependency := createOrderingTask(t, repository, "Dependency", "high")
	byFullID := createOrderingTask(t, repository, "Freed by full ID", "high")
	byPrefix := createOrderingTask(t, repository, "Freed by prefix", "high")
	other := createOrderingTask(t, repository, "Blocked from depending", "high")

	// Both dependents pick up the dependency while it is still alive; only
	// `dependency` itself is deleted afterward, so this reaches free and
	// depend against the same tombstoned task the rule is about.
	for _, dependent := range []core.Task{byFullID, byPrefix} {
		code, _, stderr := run(t, repository, "depend", dependent.ID, dependency.ID, "--json")
		if code != 0 {
			t.Fatalf("depend %s code = %d, want 0; stderr = %q", dependent.ID, code, stderr)
		}
	}
	code, _, stderr := run(t, repository, "delete", dependency.ID, "--json")
	if code != 0 {
		t.Fatalf("delete dependency code = %d, want 0; stderr = %q", code, stderr)
	}

	t.Run("free removes a tombstoned dependency named by its full ID", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "free", byFullID.ID, dependency.ID, "--json")
		if code != 0 {
			t.Fatalf("free code = %d, want 0; stderr = %q", code, stderr)
		}
		task := decodeMutationTask(t, stdout, "free")
		if len(task.Dependencies) != 0 {
			t.Fatalf("dependencies after free = %q, want empty", task.Dependencies)
		}
	})

	t.Run("free removes a tombstoned dependency named by a prefix", func(t *testing.T) {
		// Naming the dependency by a prefix instead of its full canonical ID
		// forces free through Resolve rather than the exact-canonical-ID fast
		// path Free takes when the argument is already a literal member of
		// the dependency set. Resolve is the branch where a future "skip
		// deleted tasks" filter could plausibly land (to mirror the
		// depend-side guard below) and would silently break this half of the
		// rule without ever failing the full-ID case above.
		//
		// Long enough to reach past the ULID's timestamp into its random
		// suffix. A ULID spends its first ten characters on the millisecond it
		// was minted in, so a shorter prefix would ask only that no two of
		// this test's four tasks were created in the same 32ms bucket — true
		// today, because every create writes Git objects and refs, but true by
		// accident rather than by construction.
		prefix := dependency.ID[:16]
		code, stdout, stderr := run(t, repository, "free", byPrefix.ID, prefix, "--json")
		if code != 0 {
			t.Fatalf("free(prefix) code = %d, want 0; stderr = %q", code, stderr)
		}
		task := decodeMutationTask(t, stdout, "free")
		if len(task.Dependencies) != 0 {
			t.Fatalf("dependencies after free(prefix) = %q, want empty", task.Dependencies)
		}
	})

	t.Run("depend still refuses to add the same tombstoned task", func(t *testing.T) {
		// Run against the very same task the two subtests above just proved
		// removable, so this isn't just "depend rejects some other deleted
		// task" — it confirms freeing a tombstoned dependency never
		// reclassifies it as addable again.
		code, stdout, stderr := run(t, repository, "depend", other.ID, dependency.ID, "--json")
		if code != 5 || stdout != "" {
			t.Fatalf("depend(tombstoned) code/stdout = %d/%q, want 5/empty; stderr = %q", code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "cannot add a dependency involving a tombstoned task")
	})
}

func createOrderingTask(t *testing.T, repository, title, priority string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--status", "ready", "--priority", priority, "--json")
	if code != 0 {
		t.Fatalf("create %q code = %d, want 0; stderr = %q", title, code, stderr)
	}
	result := assertJSONResult(t, stdout, "create")
	var task core.Task
	if err := json.Unmarshal(result.Data, &task); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	return task
}

func TestRunOrderingCommandsRequireTwoIDs(t *testing.T) {
	repository := initializedRepository(t)
	for _, command := range []string{"depend", "free"} {
		t.Run(command, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, command, "WB-ONE", "--json")
			if code != 2 || stdout != "" {
				t.Fatalf("%s code/stdout = %d/%q, want 2/empty; stderr = %q", command, code, stdout, stderr)
			}
			assertJSONError(t, stderr, core.CategoryInvocation, "dependency task ID must be the first argument after "+command)
		})
	}
}

func TestRunNextNoEligibleHumanMessage(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "next")
	if code != 0 {
		t.Fatalf("next code = %d, want 0; stderr = %q", code, stderr)
	}
	if stdout != "No eligible task.\n" {
		t.Fatalf("next stdout = %q, want empty-state message", stdout)
	}
}

func TestRunNextLimitOffersTheEligibleSetInOrder(t *testing.T) {
	repository := initializedRepository(t)
	low := createOrderingTask(t, repository, "Low", "low")
	high := createOrderingTask(t, repository, "High", "high")
	medium := createOrderingTask(t, repository, "Medium", "medium")

	decode := func(t *testing.T, stdout string) (ids []string, eligible int) {
		t.Helper()
		result := assertJSONResult(t, stdout, "next")
		var document struct {
			Tasks    []core.Task `json:"tasks"`
			Eligible int         `json:"eligible"`
		}
		if err := json.Unmarshal(result.Data, &document); err != nil {
			t.Fatalf("decode next --limit document: %v", err)
		}
		for _, task := range document.Tasks {
			ids = append(ids, task.ID)
		}
		return ids, document.Eligible
	}

	t.Run("limit below the eligible count truncates and reports the whole count", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "next", "--limit", "2", "--json")
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
		}
		ids, eligible := decode(t, stdout)
		if got, want := strings.Join(ids, ","), high.ID+","+medium.ID; got != want {
			t.Fatalf("tasks = %q, want %q", got, want)
		}
		if eligible != 3 {
			t.Fatalf("eligible = %d, want 3", eligible)
		}
	})

	t.Run("limit 1 keeps the document shape", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "next", "--limit", "1", "--json")
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
		}
		ids, eligible := decode(t, stdout)
		if got, want := strings.Join(ids, ","), high.ID; got != want {
			t.Fatalf("tasks = %q, want %q", got, want)
		}
		if eligible != 3 {
			t.Fatalf("eligible = %d, want 3", eligible)
		}
	})

	t.Run("limit above the eligible count offers everything", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "next", "--limit", "10", "--json")
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
		}
		ids, eligible := decode(t, stdout)
		if got, want := strings.Join(ids, ","), high.ID+","+medium.ID+","+low.ID; got != want {
			t.Fatalf("tasks = %q, want %q", got, want)
		}
		if eligible != 3 {
			t.Fatalf("eligible = %d, want 3", eligible)
		}
	})

	t.Run("text output is one list line per task", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "next", "--limit", "2")
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
		}
		var want strings.Builder
		if err := writeList(&want, []core.Task{high, medium}); err != nil {
			t.Fatalf("writeList: %v", err)
		}
		if stdout != want.String() {
			t.Fatalf("stdout = %q, want %q", stdout, want.String())
		}
	})

	t.Run("plain next still returns one task", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "next", "--json")
		if code != 0 {
			t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "next")
		var task core.Task
		if err := json.Unmarshal(result.Data, &task); err != nil {
			t.Fatalf("plain next data is no longer one task: %v", err)
		}
		if task.ID != high.ID {
			t.Fatalf("plain next = %s, want %s", task.ID, high.ID)
		}
	})
}

func TestRunNextLimitWithNothingEligible(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "next", "--limit", "3", "--json")
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "next")
	if got, want := string(result.Data), `{"tasks":[],"eligible":0}`; got != want {
		t.Fatalf("data = %s, want %s", got, want)
	}
	code, stdout, stderr = run(t, repository, "next", "--limit", "3")
	if code != 0 || stdout != "No eligible task.\n" {
		t.Fatalf("text code/stdout = %d/%q, want 0/empty-state message; stderr = %q", code, stdout, stderr)
	}
}

func TestRunNextLimitRefusals(t *testing.T) {
	repository := initializedRepository(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"zero", []string{"next", "--limit", "0", "--json"}, "next --limit must be at least 1"},
		{"negative", []string{"next", "--limit", "-2", "--json"}, "next --limit must be at least 1"},
		{"not a number", []string{"next", "--limit", "two", "--json"}, "next --limit must be at least 1"},
		{"with claim", []string{"next", "--limit", "2", "--claim", "--no-sync", "--json"}, "next accepts --limit or --claim, not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, tc.args...)
			if code != 2 || stdout != "" {
				t.Fatalf("code/stdout = %d/%q, want 2/empty; stderr = %q", code, stdout, stderr)
			}
			assertJSONError(t, stderr, core.CategoryInvocation, tc.want)
		})
	}
}

func TestRunOrderingCommandsUseInitializedRepository(t *testing.T) {
	repository := testrepo.New(t)
	code, stdout, stderr := run(t, repository, "next", "--json")
	if code != 3 || stdout != "" {
		t.Fatalf("next code/stdout = %d/%q, want 3/empty; stderr = %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotInitialized, "")
}
