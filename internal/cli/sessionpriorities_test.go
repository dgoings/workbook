package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// configureProjectPriority records one priority.add straight into the
// project's configuration ledger.
//
// The priority verbs are stubs while this is written, so there is no command
// line that reaches this yet; internal/gitstore's tests author the same
// operations the same way, through WriteConfigOperation, and what they produce
// is what `workbook priority add` will produce because the verb will call this
// method. Nothing here reads the session under test, so a project configured
// this way is indistinguishable from one a teammate configured and this clone
// fetched.
func configureProjectPriority(t *testing.T, repository string, name core.Priority, label, rank string) {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	operations := []core.ConfigOperation{{
		Type:         core.ConfigPriorityAdd,
		PriorityName: name,
		Label:        label,
		Rank:         rank,
		PriorityTags: []core.PriorityTag{},
	}}
	if _, err := repo.WriteConfigOperation(
		ctx, config, core.CryptoULIDSource{}, operations, "workbook: add priority "+string(name)); err != nil {
		t.Fatalf("WriteConfigOperation() error = %v", err)
	}
}

// projectWithUrgentPriority is an initialized project that has configured one
// priority the built-in three do not have, ranked ahead of `high`.
func projectWithUrgentPriority(t *testing.T) string {
	t.Helper()
	repository := initializedRepository(t)
	configureProjectPriority(t, repository, core.Priority("urgent"), "Urgent", "1/2")
	return repository
}

// A project that configured `urgent` can create a task with it.
//
// The session every command opens loads the project's statuses; until it also
// loaded the priorities, Service.Priorities was the zero value on this path and
// substituted the built-in three, so the mutation boundary refused a priority
// the project had put on file — the verbs would have written a configuration
// the rest of the tool was blind to.
func TestCreateAcceptsAPriorityOnlyTheProjectConfigured(t *testing.T) {
	repository := projectWithUrgentPriority(t)

	code, stdout, stderr := run(t, repository, "create", "Ship the release", "--priority", "urgent", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create --priority urgent = code %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &task); err != nil {
		t.Fatalf("decode create task: %v; output = %s", err, stdout)
	}
	if task.Priority != core.Priority("urgent") {
		t.Fatalf("created task priority = %q, want urgent", task.Priority)
	}
}

// The read path has the same gap as the write path, and its own service
// constructor: a filter naming a configured priority has to select the task
// stored under it rather than being refused as a priority the project does not
// have.
func TestListFindsATaskUnderAPriorityOnlyTheProjectConfigured(t *testing.T) {
	repository := projectWithUrgentPriority(t)

	code, stdout, stderr := run(t, repository, "create", "Ship the release", "--priority", "urgent", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create --priority urgent = code %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	var created core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &created); err != nil {
		t.Fatalf("decode create task: %v; output = %s", err, stdout)
	}

	code, stdout, stderr = run(t, repository, "list", "--priority", "urgent", "--json")
	if code != 0 {
		t.Fatalf("list --priority urgent = code %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	var listed []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &listed); err != nil {
		t.Fatalf("decode list: %v; output = %s", err, stdout)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list --priority urgent = %#v, want the one task created under it", listed)
	}
}

// Task 4's seam, observed from the outside.
//
// regenerateGuidelines takes the priorities so a status change documents the
// ones the project actually has. Its status call site passes the session's, so
// a session that never loaded them rewrote the generated file with the built-in
// three and dropped a project's configured priority out of it — the loss that
// parameter was added to prevent.
func TestAStatusRenameKeepsTheProjectsOwnPrioritiesInTheGuidelines(t *testing.T) {
	repository := projectWithUrgentPriority(t)

	if code, stdout, stderr := run(t, repository, "status", "rename", "ready", "todo", "--no-sync"); code != 0 {
		t.Fatalf("status rename = code %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}

	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, "| `urgent` | Urgent |") {
		t.Fatalf("guidelines dropped the project's own priority after a status rename:\n%s", guidelines)
	}
}
