package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Renaming a status onto a name a rename or a removal retired is allowed, and
// says loudly what it does to history.
//
// The name is not free. Every task still stored under it — and every task stored
// under a name that forwards through it — resolved to wherever the retirement
// sent it, and the rename makes the name live again, so all of them land in the
// renamed column instead. Nothing in the rename's own arguments says so, which
// is why the warning names the retired name, where its history used to resolve,
// where it resolves now, and how many of this clone's active tasks moved.

// reresolutionDocument is the `reresolved` member of a status mutation, decoded
// on its own for the reason statusChangeDocument is.
type reresolutionDocument struct {
	Status     string `json:"status"`
	RetiredBy  string `json:"retiredBy"`
	Via        string `json:"via"`
	Previously string `json:"previously"`
	Now        string `json:"now"`
	Tasks      int    `json:"tasks"`
}

// retiredChainRepository builds a project where `review3` was renamed to
// `sorting` and `sorting` was then removed into `backlog`, with `staging`
// removed into `review3` before that, so a stored `staging` reaches `backlog`
// only by way of `review3`.
//
// It returns the tasks the rename onto `review3` has to move — one stored under
// the retired name, one stored under a name that forwards through it — and the
// two it must leave alone: one that was always in `backlog`, and one a later
// write settled into `backlog`, whose stored value no longer names `review3`.
func retiredChainRepository(t *testing.T) (repository string, moved, unmoved []string) {
	t.Helper()
	repository = initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "staging", "--after", "backlog")
	mustRunStatus(t, repository, "status", "add", "review3", "--after", "staging")

	carried := cliCreateTask(t, repository, "Carried through a removal")
	stored := cliCreateTask(t, repository, "Stored under the retired name")
	settled := cliCreateTask(t, repository, "Settled after the retirement")
	untouched := cliCreateTask(t, repository, "Always in backlog")
	mustRunStatus(t, repository, "update", carried.ID, "--status", "staging", "--no-sync")
	mustRunStatus(t, repository, "update", stored.ID, "--status", "review3", "--no-sync")
	mustRunStatus(t, repository, "update", settled.ID, "--status", "review3", "--no-sync")

	mustRunStatus(t, repository, "status", "delete", "staging", "--into", "review3")
	mustRunStatus(t, repository, "status", "rename", "review3", "sorting")
	mustRunStatus(t, repository, "status", "delete", "sorting", "--into", "backlog")
	// Any write settles a stored status into the one it resolves to, so this
	// task's ref now holds `backlog` and no configuration change can find it.
	mustRunStatus(t, repository, "update", settled.ID, "--title", "Touched", "--no-sync")

	return repository, []string{carried.ID, stored.ID}, []string{settled.ID, untouched.ID}
}

func TestStatusRenameOntoARetiredNameReportsWhatReResolves(t *testing.T) {
	t.Parallel()
	repository, moved, unmoved := retiredChainRepository(t)
	for _, id := range append(append([]string{}, moved...), unmoved...) {
		if got := taskStatus(t, repository, id); got != "backlog" {
			t.Fatalf("before the rename task %s is in %q, want every task resolving to backlog", id, got)
		}
	}

	code, stdout, stderr := run(t, repository, "status", "rename", "ready", "review3", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status rename = code %d, stderr %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "status rename")
	var document struct {
		statusMutationDocument
		Reresolved *reresolutionDocument `json:"reresolved"`
	}
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode status rename: %v; output = %s", err, stdout)
	}
	if document.Change.From != "ready" || document.Change.Status != "review3" {
		t.Fatalf("change = %#v, want ready renamed to review3", document.Change)
	}

	want := reresolutionDocument{
		Status: "review3", RetiredBy: "status.rename", Via: "sorting",
		Previously: "backlog", Now: "review3", Tasks: 2,
	}
	if document.Reresolved == nil || *document.Reresolved != want {
		t.Fatalf("reresolved = %#v, want %#v", document.Reresolved, want)
	}
	message := `renaming "ready" onto "review3" re-points a retired name: "review3" was renamed to ` +
		`"sorting", which resolved to "backlog"; 2 active tasks stored under "review3" or under a name ` +
		`that forwards through it now resolve to "review3" (formerly "ready") instead of "backlog"`
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningStatusRenameReresolves ||
		envelope.Warnings[0].Message != message {
		t.Fatalf("warnings = %#v, want one %s warning saying %q",
			envelope.Warnings, core.WarningStatusRenameReresolves, message)
	}

	// The count is checked against the tasks rather than against itself.
	for _, id := range moved {
		if got := taskStatus(t, repository, id); got != "review3" {
			t.Fatalf("task %s is in %q after the rename, want the re-resolved review3", id, got)
		}
	}
	for _, id := range unmoved {
		if got := taskStatus(t, repository, id); got != "backlog" {
			t.Fatalf("task %s is in %q after the rename, want it left in backlog", id, got)
		}
	}

	// Renaming back cannot restore the retirement: the name the rename gives
	// up forwards to the renamed status, not to where the retirement sent it.
	if document.Inverse.Exact {
		t.Fatalf("inverse = %#v, want it marked inexact", document.Inverse)
	}
	note := `renaming back leaves "review3" forwarding to "ready", so the tasks this rename re-pointed ` +
		`stay there rather than returning to "backlog"`
	if document.Inverse.Note != note {
		t.Fatalf("inverse note = %q, want %q", document.Inverse.Note, note)
	}
	// The log reaches the same answer from the ledger alone.
	if entry := newestStatusLogEntry(t, repository); entry.Inverse == nil ||
		entry.Inverse.Exact || entry.Inverse.Note != note {
		t.Fatalf("log inverse = %#v, want the verb's inexact inverse and note", entry.Inverse)
	}
}

// In text mode the same sentence is a warning on standard error, where every
// other warning a status verb raises is written, and the change is still
// recorded: the rename is allowed, and the warning is the whole of the cost.
func TestStatusRenameOntoARetiredNameWarnsInText(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "review3", "--after", "backlog")
	task := cliCreateTask(t, repository, "Stored under the retired name")
	mustRunStatus(t, repository, "update", task.ID, "--status", "review3", "--no-sync")
	mustRunStatus(t, repository, "status", "delete", "review3", "--into", "backlog")

	code, stdout, stderr := run(t, repository, "status", "rename", "ready", "review3")
	if code != 0 {
		t.Fatalf("status rename = code %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "Status:\trename\treview3\n") {
		t.Fatalf("stdout = %q, want the rename reported", stdout)
	}
	want := `workbook: warning: renaming "ready" onto "review3" re-points a retired name: "review3" was ` +
		`removed into "backlog"; 1 active task stored under "review3" or under a name that forwards ` +
		`through it now resolves to "review3" (formerly "ready") instead of "backlog"` + "\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if got := taskStatus(t, repository, task.ID); got != "review3" {
		t.Fatalf("task is in %q, want the re-resolved review3", got)
	}
}

// A retired name whose history already resolves to the status being renamed
// is not re-pointed by renaming that status onto it: every task that followed
// the chain was already in this column and stays in it. Renaming back is the
// everyday case — it is the inverse every rename prints — and warning about it
// would teach people to ignore the warning.
func TestStatusRenameOntoANameThatAlreadyResolvesHereIsQuiet(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Follows the column")
	mustRunStatus(t, repository, "update", task.ID, "--status", "in-review", "--no-sync")
	mustRunStatus(t, repository, "status", "rename", "in-review", "qa")
	mustRunStatus(t, repository, "status", "add", "parking", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "parking", "--into", "ready")

	for _, test := range []struct {
		name     string
		from, to string
	}{
		{name: "renaming back", from: "qa", to: "in-review"},
		{name: "taking the name a removal sent here", from: "ready", to: "parking"},
	} {
		code, stdout, stderr := run(t, repository, "status", "rename", test.from, test.to, "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("%s: status rename = code %d, stderr %q", test.name, code, stderr)
		}
		envelope := assertJSONResult(t, stdout, "status rename")
		if len(envelope.Warnings) != 0 {
			t.Fatalf("%s: warnings = %#v, want none", test.name, envelope.Warnings)
		}
		if strings.Contains(string(envelope.Data), `"reresolved"`) {
			t.Fatalf("%s: data = %s, want no reresolved member", test.name, envelope.Data)
		}
		var document statusMutationDocument
		if err := json.Unmarshal(envelope.Data, &document); err != nil {
			t.Fatal(err)
		}
		if !document.Inverse.Exact {
			t.Fatalf("%s: inverse = %#v, want an exact inverse", test.name, document.Inverse)
		}
	}
	if got := taskStatus(t, repository, task.ID); got != "in-review" {
		t.Fatalf("task is in %q, want it to have followed its column back to in-review", got)
	}
}

// The board's status form inherits the rename, so it inherits the warning: the
// answer carries the verb's own sentence for the page to show.
func TestBoardRenameOntoARetiredNameCarriesTheWarning(t *testing.T) {
	t.Parallel()
	repository, moved, _ := retiredChainRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	document := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/ready",
		`{"name":"review3","expectedHead":`+quoteJSON(before.Head)+`}`)
	message := `renaming "ready" onto "review3" re-points a retired name: "review3" was renamed to ` +
		`"sorting", which resolved to "backlog"; 2 active tasks stored under "review3" or under a name ` +
		`that forwards through it now resolve to "review3" (formerly "ready") instead of "backlog"`
	found := false
	for _, warning := range document.Warnings {
		if warning.Code == core.WarningStatusRenameReresolves {
			if warning.Message != message {
				t.Fatalf("warning = %q, want the verb's sentence %q", warning.Message, message)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %#v, want a %s warning", document.Warnings, core.WarningStatusRenameReresolves)
	}
	for _, id := range moved {
		if got := taskStatus(t, repository, id); got != "review3" {
			t.Fatalf("task %s is in %q after the board's rename, want review3", id, got)
		}
	}
}

// A retired name whose chain ends nowhere live re-resolves tasks that were in
// no column at all, and the sentence says that rather than naming an empty
// status.
func TestReresolutionSentenceNamesAnUnresolvedHistory(t *testing.T) {
	t.Parallel()
	reresolution := statusReresolution{
		Status: "review3", RetiredBy: core.ConfigStatusRemove, Via: "gone", Now: "review3", Tasks: 0,
	}
	want := `renaming "ready" onto "review3" re-points a retired name: "review3" was removed into "gone", ` +
		`which resolved to no status; no active task here is stored under "review3" or under a ` +
		`name that forwards through it, but any such value elsewhere now resolves to "review3" ` +
		`(formerly "ready") instead of no status`
	if got := reresolution.message("ready"); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
