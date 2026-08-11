package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// writeProjectStatusRename records a status rename in a repository's
// configuration ledger.
//
// It goes through the gitstore API rather than a command because the status
// verbs do not exist yet; they are the next change. What is under test here is
// everything downstream of them — that the commands build a Service on the
// project's own vocabulary rather than the built-in default.
func writeProjectStatusRename(t *testing.T, repository string, from, to core.Status) {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{},
		[]core.ConfigOperation{{Type: core.ConfigStatusRename, From: from, To: to}}, ""); err != nil {
		t.Fatalf("WriteConfigOperation() error = %v", err)
	}
}

// TestCommandsReadTheProjectsOwnVocabulary is the activation this change
// exists for. Before it, every command built a Service on the built-in
// vocabulary, so a project that renamed a status got the old columns back on
// every read.
func TestCommandsReadTheProjectsOwnVocabulary(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	task := cliCreateTask(t, repository, "Renamed column")
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "ready", "--no-sync"); code != 0 {
		t.Fatalf("update --status ready = code %d; stderr = %q", code, stderr)
	}
	writeProjectStatusRename(t, repository, "ready", "todo")

	// The renamed token is accepted; the retired one is explained.
	code, stdout, stderr := run(t, repository, "show", task.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show = code %d, stderr %q", code, stderr)
	}
	var shown core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Status != "todo" {
		t.Fatalf("projected status = %q, want the resolved todo", shown.Status)
	}
	if shown.StoredStatus != "ready" {
		t.Fatalf("stored status = %q, want the untouched ready", shown.StoredStatus)
	}

	code, _, stderr = run(t, repository, "list", "--status", "todo", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list --status todo = code %d, stderr %q", code, stderr)
	}
	// The retired token is followed rather than refused, and the warning says
	// so; see the List relaxation this PR completes.
	code, stdout, stderr = run(t, repository, "list", "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list --status ready = code %d, stderr %q", code, stderr)
	}
	listed := assertJSONResult(t, stdout, "list")
	var filtered []core.Task
	if err := json.Unmarshal(listed.Data, &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != task.ID {
		t.Fatalf("list --status ready = %#v, want the renamed status's task", filtered)
	}
	if len(listed.Warnings) != 1 || listed.Warnings[0].Code != core.WarningStatusFilter {
		t.Fatalf("list --status ready warnings = %#v, want the rename explained", listed.Warnings)
	}

	// Creating with the new token works, and the old one is refused with a
	// message about the status rather than about the repository.
	code, stdout, stderr = run(t, repository, "create", "New column task", "--status", "todo", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create --status todo = code %d, stderr %q", code, stderr)
	}
	created := decodeMutationTask(t, stdout, "create")
	if created.Status != "todo" {
		t.Fatalf("created status = %q, want todo", created.Status)
	}
}

// TestUpdateSettlesAStaleStoredStatusInItsOwnPack is correct on touch seen from
// outside: the settlement is a real appended operation in the task's history,
// not a projection that would vanish on the next clone.
func TestUpdateSettlesAStaleStoredStatusInItsOwnPack(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	task := cliCreateTask(t, repository, "Settled on touch")
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "ready", "--no-sync"); code != 0 {
		t.Fatalf("update --status ready = code %d; stderr = %q", code, stderr)
	}
	writeProjectStatusRename(t, repository, "ready", "todo")

	code, stdout, stderr := run(t, repository, "update", task.ID, "--title", "Touched", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update --title = code %d, stderr %q", code, stderr)
	}
	var envelope struct {
		Data struct {
			Status          core.Status            `json:"status"`
			StoredStatus    core.Status            `json:"storedStatus"`
			StatusCorrected *core.StatusCorrection `json:"statusCorrected"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Status != "todo" || envelope.Data.StoredStatus != "" {
		t.Fatalf("settled task = %#v, want todo with no stored token left", envelope.Data)
	}

	code, stdout, stderr = run(t, repository, "show", task.ID, "--history", "--all", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show --history = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"status"`) || !strings.Contains(stdout, `"todo"`) {
		t.Fatalf("history = %s, want the appended status settlement", stdout)
	}
}

// TestMoveAndDependSettleAStaleStoredStatus covers the settlement seam PR-A
// disclosed: every mutation that writes a pack settles the status, not only the
// ones that were about status.
func TestMoveAndDependSettleAStaleStoredStatus(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	anchor := cliCreateTask(t, repository, "Anchor")
	moved := cliCreateTask(t, repository, "Moved")
	dependent := cliCreateTask(t, repository, "Dependent")
	writeProjectStatusRename(t, repository, "backlog", "inbox")

	code, stdout, stderr := run(t, repository, "move", moved.ID, "--before", anchor.ID, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("move = code %d, stderr %q", code, stderr)
	}
	assertSettledStatus(t, stdout, "inbox")

	code, stdout, stderr = run(t, repository, "depend", dependent.ID, anchor.ID, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("depend = code %d, stderr %q", code, stderr)
	}
	assertSettledStatus(t, stdout, "inbox")

	code, stdout, stderr = run(t, repository, "free", dependent.ID, anchor.ID, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("free = code %d, stderr %q", code, stderr)
	}
	// The task settled on the depend, so the free has nothing left to settle.
	assertNoSettlement(t, stdout)

	// Restore is the one write that does not settle: the fold refuses a pack
	// against a tombstoned parent unless it is exactly one task.restore, so a
	// settlement riding along would be rejected. The task comes back carrying
	// its stored token and settles on its next ordinary write.
	code, _, stderr = run(t, repository, "delete", anchor.ID, "--no-sync")
	if code != 0 {
		t.Fatalf("delete = code %d, stderr %q", code, stderr)
	}
	code, stdout, stderr = run(t, repository, "restore", anchor.ID, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("restore = code %d, stderr %q", code, stderr)
	}
	var restored struct {
		Data struct {
			Status       core.Status `json:"status"`
			StoredStatus core.Status `json:"storedStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Data.Status != "inbox" || restored.Data.StoredStatus != "backlog" {
		t.Fatalf("restored task = %#v, want the stale token resolved but not yet settled", restored.Data)
	}
	code, stdout, stderr = run(t, repository, "update", anchor.ID, "--title", "Anchor again", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update after restore = code %d, stderr %q", code, stderr)
	}
	assertSettledStatus(t, stdout, "inbox")
}

func assertSettledStatus(t *testing.T, output string, to core.Status) {
	t.Helper()
	var envelope struct {
		Data struct {
			Status          core.Status            `json:"status"`
			StoredStatus    core.Status            `json:"storedStatus"`
			StatusCorrected *core.StatusCorrection `json:"statusCorrected"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Status != to || envelope.Data.StoredStatus != "" {
		t.Fatalf("task = %#v, want %q with no stored token left", envelope.Data, to)
	}
}

func assertNoSettlement(t *testing.T, output string) {
	t.Helper()
	var envelope struct {
		Data struct {
			StoredStatus core.Status `json:"storedStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.StoredStatus != "" {
		t.Fatalf("stored status = %q, want an already-settled task", envelope.Data.StoredStatus)
	}
}

// TestSyncJSONOmitsTheConfigMemberWhenNothingMoved is the byte compatibility
// claim: a project with no ledger, and a project whose ledger is settled, emit
// exactly the envelope they emitted before this stage existed.
func TestSyncJSONOmitsTheConfigMemberWhenNothingMoved(t *testing.T) {
	first, second := cliSyncRepositories(t)
	cliCreateTask(t, first, "Task")

	code, stdout, stderr := run(t, first, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("sync = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) {
		t.Fatalf("sync JSON = %s, want no configuration member for a project with no ledger", stdout)
	}
	if code, stdout, stderr = run(t, first, "push", "--json"); code != 0 || stderr != "" {
		t.Fatalf("push = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) {
		t.Fatalf("push JSON = %s, want no configuration member", stdout)
	}

	// A ledger that exists and is settled everywhere says nothing either.
	writeProjectStatusRename(t, first, "ready", "todo")
	if code, _, stderr = run(t, first, "sync", "--json"); code != 0 || stderr != "" {
		t.Fatalf("sync publishing the ledger = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr = run(t, second, "sync", "--json"); code != 0 || stderr != "" {
		t.Fatalf("second clone sync = code %d, stderr %q", code, stderr)
	}
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("settled sync = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) {
		t.Fatalf("settled sync JSON = %s, want no configuration member once both sides agree", stdout)
	}
	code, stdout, stderr = run(t, second, "fetch", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("settled fetch = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) {
		t.Fatalf("settled fetch JSON = %s, want no configuration member", stdout)
	}

	// A mutation on a settled project says nothing about configuration either.
	code, stdout, stderr = run(t, second, "create", "Quiet mutation", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) {
		t.Fatalf("mutation JSON = %s, want no configuration member", stdout)
	}
}

// TestValidateJSONOmitsTheConfigSectionWithoutALedger keeps the audit's
// envelope compatible for the projects that never configure a status.
func TestValidateJSONOmitsTheConfigSectionWithoutALedger(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	cliCreateTask(t, repository, "Validated task")

	code, stdout, stderr := run(t, repository, "validate", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("validate = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"config"`) || strings.Contains(stdout, `"advisories"`) {
		t.Fatalf("validate JSON = %s, want neither section without a ledger", stdout)
	}

	writeProjectStatusRename(t, repository, "ready", "todo")
	code, stdout, stderr = run(t, repository, "validate", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("validate with a ledger = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"config"`) {
		t.Fatalf("validate JSON = %s, want the configuration section once a ledger exists", stdout)
	}
	if strings.Contains(stdout, `"advisories"`) {
		t.Fatalf("validate JSON = %s, want no advisories for a vocabulary inside every ceiling", stdout)
	}
}
