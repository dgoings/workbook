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

func TestRunOrderingCommandsUseInitializedRepository(t *testing.T) {
	repository := testrepo.New(t)
	code, stdout, stderr := run(t, repository, "next", "--json")
	if code != 3 || stdout != "" {
		t.Fatalf("next code/stdout = %d/%q, want 3/empty; stderr = %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotInitialized, "")
}
