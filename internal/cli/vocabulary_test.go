package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// writeTaskInAForeignStatus creates a task holding a status this project's
// ledger does not define and does not forward.
//
// It writes through the real service with an overridden vocabulary, which is
// what the situation actually is: a clone whose configuration this checkout has
// not fetched wrote a task ref naming a status it knows about. The ledger here
// is untouched, so every command that reads it afterwards sees a token that
// resolves to nothing — the one state the unknown-status region is for.
func writeTaskInAForeignStatus(t *testing.T, repository, title string, status core.Status) core.Task {
	t.Helper()
	ctx := context.Background()
	service, _, _, err := openServiceParts(ctx, repository, io.Discard)
	if err != nil {
		t.Fatalf("openServiceParts() error = %v", err)
	}
	foreign, err := core.NewVocabulary(
		[]core.StatusDefinition{{
			Status: status, Label: "Foreign", Rank: "1/1",
			Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext, core.StatusTagDone},
		}},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	service.Vocabulary = foreign
	result, err := service.CreateMutation(ctx, core.CreateInput{Title: title, Status: status, Priority: core.PriorityMedium})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	return result.Task
}

// A task holding a status that resolves to nothing is shown, and it is still
// editable. The rule is narrower than "this task is frozen": membership is
// checked against the status a caller supplies, not against the one the task
// already holds, so a title edit saves and only naming a status the project
// does not define is refused.
//
// This is pinned because the documentation said the opposite — that such a task
// could not be edited at all and that `workbook update` on one exited 7 — which
// stopped being true when the membership check moved to the mutation boundary.
// A wrong recovery instruction is worse than none: it tells a reader their work
// is unreachable when a one-line command would file it.
func TestAnUnresolvableStatusIsShownAndStillEditable(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	stranded := writeTaskInAForeignStatus(t, repository, "Written by a clone we have not fetched", "ghost")

	code, stdout, stderr := run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "UNKNOWN STATUS (1)") {
		t.Fatalf("board = %q, want the stranded task under the unknown-status heading", stdout)
	}

	// The edits that do not name a status go through.
	code, _, stderr = run(t, repository, "update", stranded.ID, "--title", "Renamed while stranded", "--no-sync")
	if code != 0 {
		t.Fatalf("update --title = code %d, want 0; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, repository, "show", stranded.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show = code %d, stderr %q", code, stderr)
	}
	var shown core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Title != "Renamed while stranded" {
		t.Fatalf("title = %q, want the edit to have saved", shown.Title)
	}
	if shown.Status != "ghost" || shown.StoredStatus != "" {
		t.Fatalf("status/stored = %q/%q, want the unresolvable token left exactly as it is", shown.Status, shown.StoredStatus)
	}

	// Naming a status the project does not define is the one refusal, and it is
	// a validation failure rather than a corruption report.
	code, _, stderr = run(t, repository, "update", stranded.ID, "--status", "phantom", "--no-sync")
	if code != 5 {
		t.Fatalf("update --status phantom = code %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `invalid task status "phantom"`) {
		t.Fatalf("update --status phantom stderr = %q, want the status named", stderr)
	}

	// And a status the project does define files the task, which is the
	// recovery the documentation now offers.
	code, _, stderr = run(t, repository, "update", stranded.ID, "--status", "ready", "--no-sync")
	if code != 0 {
		t.Fatalf("update --status ready = code %d, want 0; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "UNKNOWN STATUS") {
		t.Fatalf("board = %q, want the filed task out of the unknown-status region", stdout)
	}
}

// Two tasks stranded under one unresolvable token share a bucket, so `move`
// reorders them against each other. `place` cannot express the same reordering,
// because it names the destination status and the mutation boundary refuses a
// status the project does not define — which is that boundary doing its job,
// not a second bucketing rule. The README says so; this is what it says.
func TestMoveReordersWithinAnUnresolvableBucket(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	first := writeTaskInAForeignStatus(t, repository, "First stranded", "ghost")
	second := writeTaskInAForeignStatus(t, repository, "Second stranded", "ghost")

	code, _, stderr := run(t, repository, "move", second.ID, "--before", first.ID, "--no-sync")
	if code != 0 {
		t.Fatalf("move within the stranded bucket = code %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "UNKNOWN STATUS (2)") {
		t.Fatalf("board = %q, want both stranded tasks still in the region", stdout)
	}
	if strings.Index(stdout, "Second stranded") > strings.Index(stdout, "First stranded") {
		t.Fatalf("board = %q, want the moved task drawn first", stdout)
	}
}

// The terminal board draws the project's columns, under the project's labels,
// and puts a task still stored under a retired token in the column that token
// now means.
//
// This is the last surface that was still reading the built-in six. Everything
// else about a rename was already honest — show, list, next, create — while the
// board a person actually looks at printed a READY column for a project that no
// longer has one, with the task missing from it.
func TestBoardRendersTheProjectsOwnColumns(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	task := cliCreateTask(t, repository, "Renamed column")
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "ready", "--no-sync"); code != 0 {
		t.Fatalf("update --status ready = code %d; stderr = %q", code, stderr)
	}
	// Through the verb rather than through the ledger API, so the label the
	// board prints is the one a person renaming a column would actually get.
	if code, _, stderr := run(t, repository, "status", "rename", "ready", "todo", "--no-sync"); code != 0 {
		t.Fatalf("status rename = code %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "status", "delete", "in-review", "--into", "done", "--no-sync"); code != 0 {
		t.Fatalf("status delete = code %d; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "TODO (1)") {
		t.Fatalf("board = %q, want a TODO column holding the renamed task", stdout)
	}
	if strings.Contains(stdout, "READY (") {
		t.Fatalf("board = %q, want no column for the retired status", stdout)
	}
	// A removed status loses its column too, rather than being drawn empty.
	if strings.Contains(stdout, "IN REVIEW (") {
		t.Fatalf("board = %q, want no column for the removed status", stdout)
	}
	if strings.Contains(stdout, "UNKNOWN STATUS") {
		t.Fatalf("board = %q, want the stored token resolved rather than stranded", stdout)
	}
	if !strings.Contains(stdout, "Renamed column") {
		t.Fatalf("board = %q, want the task drawn", stdout)
	}
}

// TestUnresolvedStatusRecovery is the acceptance pair for the recovery path,
// kept in one function because the pair is the claim. A stored status this
// project cannot resolve is a *correction* — the task takes an ordinary status
// write and lands in a column — while stored data that is not a status at all
// is *corruption*, and no write reaches it. One exits 0 and appends exactly one
// operation; the other exits 7 whatever is asked of it.
//
// The two halves run against one task on purpose. Corruption is what the same
// ref becomes when something outside Workbook edits it, and asserting the fork
// on one task is what makes "the difference is the data, not the task" a
// statement rather than two unrelated fixtures.
func TestUnresolvedStatusRecovery(t *testing.T) {
	repository, _ := cliSyncRepositories(t)
	stranded := writeTaskInAForeignStatus(t, repository, "Written by a clone we have not fetched", "ghost")

	// (a) The correction. A status this project defines is an ordinary write to
	// a task whose stored status it does not, because membership is asked of
	// what a caller supplies rather than of what the task already holds.
	code, _, stderr := run(t, repository, "update", stranded.ID, "--status", "ready", "--no-sync")
	if code != 0 {
		t.Fatalf("update --status ready = code %d, want 0; stderr = %q", code, stderr)
	}
	// Exactly one operation, and it is the status: a correction must not smuggle
	// a settlement alongside itself. There is nothing to settle — the stored
	// token forwards nowhere — and a pack carrying two status writes would make
	// the history say the task moved twice.
	pack := headOperationPack(t, repository, stranded.ID)
	if len(pack.Operations) != 1 {
		t.Fatalf("appended pack = %#v, want exactly one operation", pack.Operations)
	}
	operation := pack.Operations[0]
	if operation.Type != core.OperationFieldSet || operation.Field != "status" || operation.Value != "ready" {
		t.Fatalf("appended operation = %#v, want one field.set of status to ready", operation)
	}

	code, stdout, stderr := run(t, repository, "board")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "UNKNOWN STATUS") {
		t.Fatalf("board = %q, want the corrected task filed into a column", stdout)
	}

	// (b) The residue. A stored status that is not a status token at all cannot
	// be reached by the same recovery: it fails the structural rule every read
	// applies, so the task reports corrupt data rather than an unfamiliar
	// status. Only a hand edit or a hostile ref produces it, which is why the
	// repair is a documented gap rather than a verb.
	overwriteStoredTask(t, repository, stranded.ID, `"status":"ready"`, `"status":"Not A Token"`)
	code, stdout, stderr = run(t, repository, "update", stranded.ID, "--status", "backlog", "--no-sync", "--json")
	if code != 7 {
		t.Fatalf("update on a malformed stored status = code %d, want 7; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written for a refused write", stdout)
	}
	// The refusal is about the stored document rather than about the status the
	// caller supplied, which is the whole distinction: nothing got as far as
	// asking whether `backlog` is a member.
	assertJSONError(t, stderr, core.CategoryCorruptData, "task state contains an invalid task")

	// And a corrupt field that has nothing to do with the status reads the same
	// way, so the verdict is about the data rather than about this one field.
	// Its own repository, because a tampered ref is not a state a repository
	// recovers from: the projection refuses everything downstream of it until
	// somebody rebuilds, which is the guard doing its job.
	other, _ := cliSyncRepositories(t)
	second := writeTaskInAForeignStatus(t, other, "Also written elsewhere", "ghost")
	overwriteStoredTask(t, other, second.ID, `"priority":"medium"`, `"priority":"urgent"`)
	code, _, stderr = run(t, other, "update", second.ID, "--status", "ready", "--no-sync", "--json")
	if code != 7 {
		t.Fatalf("update on a corrupt priority = code %d, want 7; stderr = %q", code, stderr)
	}
	// The message is pinned, not just the category. There is a second
	// corrupt-data failure within reach here — the projection's "current head is
	// not a descendant of its previous head" guard, which fires if
	// overwriteStoredTask ever stops discarding the cache — and it would satisfy
	// an assertion that only checked the exit code. Naming the message is what
	// keeps this leg about the tampered field.
	assertJSONError(t, stderr, core.CategoryCorruptData, "task state contains an invalid task")
}

// headOperationPack decodes the operation pack a task's newest commit carries,
// so a test can assert what a command appended rather than what the projection
// reports afterwards.
func headOperationPack(t *testing.T, repository, taskID string) core.OperationPack {
	t.Helper()
	head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+taskID)
	pack, err := core.DecodeOperationPack([]byte(gitOutput(t, repository, "show", head+":operation.json")))
	if err != nil {
		t.Fatalf("decode operation pack at %s: %v", head, err)
	}
	return pack
}

// overwriteStoredTask rewrites a task's stored checkpoint through Git, which is
// the only way to produce data no Workbook build would write.
//
// The edit is a substitution on the stored bytes rather than a re-encode of a
// decoded document, because every encoder in core validates what it is handed —
// which is the property under test. It replaces the head commit rather than
// appending one for the same reason: an append is a write, and every write goes
// through the validation this is trying to get underneath. What it reproduces is
// a ref somebody edited by hand.
func overwriteStoredTask(t *testing.T, repository, taskID, from, to string) {
	t.Helper()
	ref := "refs/workbook/tasks/" + taskID
	head := gitOutput(t, repository, "rev-parse", "--verify", ref)
	stored := gitBlob(t, repository, head+":state.json")
	if !bytes.Contains(stored, []byte(from)) {
		t.Fatalf("stored checkpoint %s does not contain %q, so the tamper would be a no-op", stored, from)
	}
	encoded := bytes.Replace(stored, []byte(from), []byte(to), 1)
	operationBlob := gitOutput(t, repository, "rev-parse", head+":operation.json")
	stateBlob := gitInput(t, repository, encoded, "hash-object", "-w", "--stdin")
	tree := gitInput(t, repository,
		[]byte("100644 blob "+operationBlob+"\toperation.json\n100644 blob "+stateBlob+"\tstate.json\n"), "mktree")
	commitArgs := []string{"commit-tree", tree}
	for _, parent := range strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", head))[1:] {
		commitArgs = append(commitArgs, "-p", parent)
	}
	gitInput(t, repository, nil, "update-ref", ref, gitInput(t, repository, nil, commitArgs...), head)
	// The projection remembers the head it last saw, and a hand-edited ref is
	// not a descendant of it — a guard that would answer every later command
	// with "run workbook rebuild" before anything read the tampered bytes. The
	// projection is disposable by construction, so discarding it puts the next
	// command in front of the refs themselves, which is where the corruption is.
	if err := os.RemoveAll(filepath.Join(repository, ".git", "workbook")); err != nil {
		t.Fatalf("discard projection cache: %v", err)
	}
}

// gitBlob reads an object's bytes exactly, where gitOutput trims them. A stored
// document ends in a newline that is part of its canonical form, so a helper
// that dropped it would tamper with more than it meant to.
func gitBlob(t *testing.T, repository, object string) []byte {
	t.Helper()
	contents, err := exec.Command("git", "-C", repository, "cat-file", "blob", object).Output()
	if err != nil {
		t.Fatalf("git cat-file blob %s: %v", object, err)
	}
	return contents
}
