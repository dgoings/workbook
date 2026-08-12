package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/testrepo"
)

// The decoded shapes are declared here rather than reused from production so a
// change to an envelope member has to be made twice, once where it is produced
// and once where a caller reads it. That is the whole value of a machine
// interface's test: it fails when the document changes, not when the code does.
type statusChangeDocument struct {
	Operation    string            `json:"operation"`
	Status       string            `json:"status"`
	From         string            `json:"from"`
	Into         string            `json:"into"`
	Position     *positionDocument `json:"position"`
	Label        *labelDocument    `json:"label"`
	LabelDerived *bool             `json:"labelDerived"`
	Tags         []string          `json:"tags"`
	DefaultFrom  string            `json:"defaultFrom"`
}

type positionDocument struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Rank   string `json:"rank"`
	Order  int    `json:"order"`
}

type labelDocument struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type inverseDocument struct {
	Command string `json:"command"`
	Exact   bool   `json:"exact"`
	Note    string `json:"note"`
}

type vocabularyDocument struct {
	Head     string `json:"head"`
	Seeded   bool   `json:"seeded"`
	Default  string `json:"default"`
	Statuses []struct {
		Status string   `json:"status"`
		Label  string   `json:"label"`
		Tags   []string `json:"tags"`
		Order  int      `json:"order"`
		Tasks  *int     `json:"tasks"`
	} `json:"statuses"`
}

type statusMutationDocument struct {
	Change     statusChangeDocument `json:"change"`
	Vocabulary vocabularyDocument   `json:"vocabulary"`
	Tasks      struct {
		Affected       int `json:"affected"`
		ClaimableAfter int `json:"claimableAfter"`
	} `json:"tasks"`
	Inverse inverseDocument `json:"inverse"`
	Docs    *docsDocument   `json:"docs"`
}

type docsDocument struct {
	Artifacts []struct {
		Path    string `json:"path"`
		State   string `json:"state"`
		Written bool   `json:"written"`
	} `json:"artifacts"`
}

type statusListDocument struct {
	vocabularyDocument
	Retired []struct {
		Status    string `json:"status"`
		Becomes   string `json:"becomes"`
		Operation string `json:"operation"`
		At        string `json:"at"`
	} `json:"retired"`
	Unresolved []struct {
		Status  string   `json:"status"`
		Tasks   int      `json:"tasks"`
		TaskIDs []string `json:"taskIds"`
	} `json:"unresolved"`
	Advisories []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"advisories"`
	Migrations []struct {
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Command string `json:"command"`
		First   string `json:"first"`
	} `json:"migrations"`
}

type statusLogDocument struct {
	Showing int `json:"showing"`
	Total   int `json:"total"`
	Entries []struct {
		Commit      string           `json:"commit"`
		OperationID string           `json:"operationId"`
		WallTime    time.Time        `json:"wallTime"`
		Actor       string           `json:"actor"`
		Operation   string           `json:"operation"`
		Summary     string           `json:"summary"`
		Collapsed   int              `json:"collapsed"`
		Inverse     *inverseDocument `json:"inverse"`
	} `json:"entries"`
	Truncated *core.HistoryTruncation `json:"truncated"`
}

type statusEnvelope struct {
	resultDocument
	Sync *struct {
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
		Detail  string `json:"detail"`
	} `json:"sync"`
}

func cliStatusMutation(t *testing.T, repository, command string, args ...string) statusMutationDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v = code %d, stderr %q", args, code, stderr)
	}
	var document statusMutationDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, command).Data, &document); err != nil {
		t.Fatalf("decode %s result: %v; output = %s", command, err, stdout)
	}
	return document
}

func cliStatusList(t *testing.T, repository string) statusListDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, "status", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	var document statusListDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "status list").Data, &document); err != nil {
		t.Fatalf("decode status list: %v; output = %s", err, stdout)
	}
	return document
}

func cliStatusLog(t *testing.T, repository string, args ...string) statusLogDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append(append([]string{"status", "log"}, args...), "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("status log = code %d, stderr %q", code, stderr)
	}
	var document statusLogDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "status log").Data, &document); err != nil {
		t.Fatalf("decode status log: %v; output = %s", err, stdout)
	}
	return document
}

// cliStatusNames is the project's statuses in board order, which is what most
// assertions here are really about: a status command's whole job is to change
// this sequence and nothing else.
func cliStatusNames(t *testing.T, repository string) []string {
	t.Helper()
	names := make([]string, 0, 8)
	for _, status := range cliStatusList(t, repository).Statuses {
		names = append(names, status.Status)
	}
	return names
}

func mustRunStatus(t *testing.T, repository string, args ...string) {
	t.Helper()
	if code, _, stderr := run(t, repository, args...); code != 0 {
		t.Fatalf("%v = code %d; stderr = %q", args, code, stderr)
	}
}

// A project that has never configured its statuses reads the pre-ledger six and
// says so. That flag is the whole difference between "this project chose these"
// and "nobody has chosen anything yet", and a consumer that cannot tell them
// apart cannot decide whether to offer the setup.
//
// The six include `blocked`, which this build no longer mints a project with.
// Nothing removes it: an upgrade that dropped a column would move somebody's
// tasks without being asked. The listing says so instead, and prints the command
// that does the removal when its reader wants it.
func TestStatusListReadsTheBuiltInVocabularyOnALedgerlessProject(t *testing.T) {
	repository := preLedgerRepository(t)
	cliCreateTask(t, repository, "Alpha")
	cliCreateTask(t, repository, "Beta")

	document := cliStatusList(t, repository)
	if document.Seeded || document.Head != "" {
		t.Fatalf("list = seeded %t, head %q; want an unseeded project", document.Seeded, document.Head)
	}
	if document.Default != "backlog" {
		t.Fatalf("default = %q, want backlog", document.Default)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "blocked", "in-progress", "in-review", "done",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want the six a pre-ledger project is using %v", got, want)
	}
	first := document.Statuses[0]
	if first.Status != "backlog" || first.Label != "Backlog" || first.Order != 1 ||
		first.Tasks == nil || *first.Tasks != 2 {
		t.Fatalf("first status = %#v, want backlog holding both tasks", first)
	}
	if len(document.Retired) != 0 || len(document.Unresolved) != 0 || len(document.Advisories) != 0 {
		t.Fatalf("list = %#v, want nothing retired, unresolved, or advised", document)
	}
	if len(document.Migrations) != 1 {
		t.Fatalf("migrations = %#v, want exactly the note about `blocked`", document.Migrations)
	}
	migration := document.Migrations[0]
	if migration.Status != "blocked" ||
		migration.Command != "workbook status delete blocked --into backlog" ||
		!strings.Contains(migration.Reason, "task dependencies record what a task is waiting on") {
		t.Fatalf("migration = %#v, want blocked, a reason, and the removal command", migration)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"#  STATUS       LABEL        TAGS     TASKS",
		"1  backlog      Backlog      default  2",
		"6  done         Done         done     0",
		"No status change is recorded, so these are the statuses Workbook reads for a project that has none of its own.",
		"No longer a default:",
		"remove it when this project no longer needs it: workbook status delete blocked --into backlog",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status list text = %q, want %q", stdout, want)
		}
	}
}

// A project minted by this build gets the five statuses it ships, records them
// in a genesis rather than leaning on a fallback, and is told nothing about a
// status it does not have.
func TestStatusListReportsTheMintedVocabularyOnAFreshProject(t *testing.T) {
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Alpha")

	document := cliStatusList(t, repository)
	if !document.Seeded || document.Head == "" {
		t.Fatalf("list = seeded %t, head %q; want a project whose genesis was written", document.Seeded, document.Head)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want the five this build mints %v", got, want)
	}
	if document.Default != "backlog" {
		t.Fatalf("default = %q, want backlog", document.Default)
	}
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none for a project that never had `blocked`", document.Migrations)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, unwanted := range []string{"blocked", "No longer a default:", "No status change is recorded"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("status list text = %q, want no %q", stdout, unwanted)
		}
	}
}

// The note follows the status rather than the ledger. A project whose genesis
// was seeded from the pre-ledger vocabulary — which is what the first status
// change on an existing project writes — still defines `blocked`, and still has
// the same thing to be told about it.
func TestStatusListNotesTheDroppedDefaultOnASeededLegacyProject(t *testing.T) {
	repository := preLedgerRepository(t)
	mustRunStatus(t, repository, "status", "label", "ready", "Up Next")

	document := cliStatusList(t, repository)
	if !document.Seeded {
		t.Fatalf("list = seeded %t, want the status change to have seeded a genesis", document.Seeded)
	}
	if got, want := len(document.Statuses), 6; got != want {
		t.Fatalf("statuses = %d, want %d; the genesis is seeded from the pre-ledger vocabulary", got, want)
	}
	if len(document.Migrations) != 1 || document.Migrations[0].Status != "blocked" {
		t.Fatalf("migrations = %#v, want the note about `blocked`", document.Migrations)
	}

	// Removing it is what makes the note go away, and nothing else does.
	mustRunStatus(t, repository, "status", "delete", "blocked", "--into", "backlog")
	if got := cliStatusList(t, repository).Migrations; len(got) != 0 {
		t.Fatalf("migrations after the removal = %#v, want none", got)
	}
}

// A project whose new tasks land in `blocked` gets the note with a different
// next step rather than no note at all.
//
// `status delete` refuses to forward a status into itself and refuses to leave a
// project with nowhere for new work to land, so the removal is not a command
// that exists for them yet — but they are the readers with the most invested in
// the column and the least reason to be told nothing. The absent `command` is
// what a caller reads the difference from, and `first` carries the step that
// makes a removal expressible at all.
func TestStatusListNamesTheFirstStepWhenBlockedHoldsTheDefaultTag(t *testing.T) {
	repository := preLedgerRepository(t)
	mustRunStatus(t, repository, "status", "tag", "blocked", "--tag", "default")

	document := cliStatusList(t, repository)
	if document.Default != "blocked" {
		t.Fatalf("default = %q, want blocked", document.Default)
	}
	if len(document.Migrations) != 1 {
		t.Fatalf("migrations = %#v, want the note about `blocked`", document.Migrations)
	}
	migration := document.Migrations[0]
	if migration.Status != "blocked" {
		t.Fatalf("migration = %#v, want it to name blocked", migration)
	}
	if migration.Command != "" {
		t.Fatalf("migration command = %q, want none: no single command removes the default holder", migration.Command)
	}
	if migration.First != "workbook status tag <status> --tag default" {
		t.Fatalf("migration first = %q, want the tag handoff", migration.First)
	}
	if !strings.Contains(migration.Reason, "new tasks land in it") {
		t.Fatalf("migration reason = %q, want it to say why the removal needs a step first", migration.Reason)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout,
		"removing it starts by giving the default tag to another status: workbook status tag <status> --tag default") {
		t.Errorf("status list text = %q, want the first step", stdout)
	}
	if strings.Contains(stdout, "remove it when this project no longer needs it") {
		t.Errorf("status list text = %q, want no removal command it cannot run", stdout)
	}
}

// The migration the note recommends, run end to end on the project shape that
// needs it: a pre-ledger project holding tasks in `blocked`.
//
// Nothing here is automatic, and that is the point. The removal is a command a
// person runs; it re-files the tasks by forwarding rather than by rewriting
// them; it reports what the queue gains; and the value it retired keeps
// resolving afterwards so a teammate who names it is told what happened rather
// than that it never existed.
func TestStatusDeleteBlockedMigratesAPreLedgerProject(t *testing.T) {
	repository := preLedgerRepository(t)
	free := cliCreateTask(t, repository, "Was blocked")
	dependent := cliCreateTask(t, repository, "Was blocked and waiting")
	prerequisite := cliCreateTask(t, repository, "Prerequisite")
	mustRunStatus(t, repository, "depend", dependent.ID, prerequisite.ID, "--no-sync")
	for _, id := range []string{free.ID, dependent.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "blocked", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "blocked", "--into", "backlog", "--json")
	if removal.Tasks.Affected != 2 {
		t.Fatalf("affected = %d, want the two tasks in blocked", removal.Tasks.Affected)
	}
	// None of them becomes claimable, and that is the honest answer for the
	// recommended destination: `backlog` carries no `next` tag, so the tasks land
	// where new work lands and somebody still moves them on deliberately. Nothing
	// was claimable while they sat in `blocked` either, so the migration hands
	// nobody a queue they did not ask for.
	if removal.Tasks.ClaimableAfter != 0 {
		t.Fatalf("claimableAfter = %d, want none: backlog is not tagged next", removal.Tasks.ClaimableAfter)
	}

	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after the migration = %v, want %v", got, want)
	}
	document := cliStatusList(t, repository)
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none once `blocked` is gone", document.Migrations)
	}
	if len(document.Unresolved) != 0 {
		t.Fatalf("unresolved = %#v, want the tasks forwarded rather than stranded", document.Unresolved)
	}
	backlog := document.Statuses[0]
	if backlog.Status != "backlog" || backlog.Tasks == nil || *backlog.Tasks != 3 {
		t.Fatalf("backlog = %#v, want all three tasks resolving there", backlog)
	}

	code, board, stderr := run(t, repository, "board", "--narrow")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(board, "BLOCKED") {
		t.Errorf("the board still draws a Blocked column after the removal:\n%s", board)
	}

	// Naming the removed value afterwards is answered with what became of it and
	// when, which is the whole reason a removal leaves a forwarding pointer.
	today := time.Now().UTC().Format("2006-01-02")
	code, _, stderr = run(t, repository, "status", "label", "blocked", "Blocked Again", "--json")
	if code != 4 {
		t.Fatalf("status label blocked = code %d, want 4; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		fmt.Sprintf(`no status "blocked"; it was removed into "backlog" on %s`, today))

	// A caller supplying it to a task command is refused the same way, and the
	// task holding it stays fully editable.
	code, _, stderr = run(t, repository, "update", free.ID, "--status", "blocked", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("update --status blocked = code %d, want 5; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "update", free.ID, "--title", "Renamed", "--no-sync"); code != 0 {
		t.Fatalf("editing a task stored under the removed status = code %d; stderr = %q", code, stderr)
	}
}

// `workbook status` alone is a mistake with two likely causes, and each gets
// its own answer: a forgotten subcommand, and a caller who wanted a task's
// status and found a verb family.
func TestStatusWithoutASubcommandNamesTheSubcommandsOrTheTaskCommand(t *testing.T) {
	repository := initializedRepository(t)

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no subcommand",
			args: []string{"status"},
			want: "status takes a subcommand; the subcommands are list, add, rename, label, move, tag, untag, delete, log",
		},
		{
			name: "unknown subcommand",
			args: []string{"status", "frobnicate"},
			want: `unknown status command "frobnicate"`,
		},
		{
			name: "full task ID",
			args: []string{"status", "WB-01K0M6B8A4FTT8C39MXXYTW7D1"},
			want: "workbook status takes a subcommand; to read a task use: workbook show WB-01K0M6B8A4FTT8C39MXXYTW7D1",
		},
		{
			name: "task ID prefix",
			args: []string{"status", "WB-01K0M6"},
			want: "to read a task use: workbook show WB-01K0M6",
		},
		{
			name: "lowercase task prefix",
			args: []string{"status", "wb-01k0m6"},
			want: "to read a task use: workbook show wb-01k0m6",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("%v = code %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", test.args, stdout)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("%v stderr = %q, want %q", test.args, stderr, test.want)
			}
		})
	}

	// A word that could be a status name is a mistyped subcommand, not a task.
	code, _, stderr := run(t, repository, "status", "in-progress")
	if code != 2 || strings.Contains(stderr, "workbook show") {
		t.Fatalf("status in-progress = code %d, stderr %q; want a subcommand refusal", code, stderr)
	}

	// A caller who asked for JSON is answered in JSON even when the subcommand
	// is the part that is wrong, which needs the option scan to get past
	// arguments it cannot attribute to a subcommand it does not recognize.
	code, _, stderr = run(t, repository, "status", "frobnicate", "triage", "--json")
	if code != 2 {
		t.Fatalf("status frobnicate triage --json = code %d, want 2; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		`unknown status command "frobnicate"; the subcommands are list, add, rename, label, move, tag, untag, delete, log`)
}

// Every mutating verb, both output modes, in one project: what each one records,
// what the envelope says about it, and that the command member is the whole verb
// string a caller dispatches on.
func TestStatusVerbsRecordTheirChangeInBothModes(t *testing.T) {
	repository := initializedRepository(t)

	add := cliStatusMutation(t, repository, "status add",
		"status", "add", "triage", "--after", "backlog", "--tag", "next", "--json")
	if add.Change.Operation != "add" || add.Change.Status != "triage" {
		t.Fatalf("add change = %#v", add.Change)
	}
	if add.Change.Position == nil || add.Change.Position.After != "backlog" || add.Change.Position.Order != 2 {
		t.Fatalf("add position = %#v, want second, after backlog", add.Change.Position)
	}
	if add.Change.Label == nil || add.Change.Label.To != "Triage" {
		t.Fatalf("add label = %#v, want the derived Triage", add.Change.Label)
	}
	if len(add.Change.Tags) != 1 || add.Change.Tags[0] != "next" {
		t.Fatalf("add tags = %#v, want next", add.Change.Tags)
	}
	if !add.Vocabulary.Seeded || add.Vocabulary.Head == "" {
		t.Fatalf("add vocabulary = %#v, want a seeded ledger with a head", add.Vocabulary)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "triage", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after add = %v, want %v", got, want)
	}

	// Appending is the default placement, and it appends rather than landing
	// anywhere the ranks happen to allow.
	appended := cliStatusMutation(t, repository, "status add", "status", "add", "archived", "--json")
	if appended.Change.Position == nil || appended.Change.Position.Order != 7 ||
		appended.Change.Position.Before != "" || appended.Change.Position.After != "" {
		t.Fatalf("appended position = %#v, want last with no anchor", appended.Change.Position)
	}

	rename := cliStatusMutation(t, repository, "status rename", "status", "rename", "triage", "intake", "--json")
	if rename.Change.From != "triage" || rename.Change.Status != "intake" {
		t.Fatalf("rename change = %#v", rename.Change)
	}
	if rename.Change.Label == nil || rename.Change.Label.From != "Triage" || rename.Change.Label.To != "Intake" {
		t.Fatalf("rename label = %#v, want the label re-derived", rename.Change.Label)
	}
	if rename.Change.LabelDerived == nil || !*rename.Change.LabelDerived {
		t.Fatalf("rename labelDerived = %#v, want true", rename.Change.LabelDerived)
	}

	label := cliStatusMutation(t, repository, "status label", "status", "label", "intake", "Front Door", "--json")
	if label.Change.Label == nil || label.Change.Label.From != "Intake" || label.Change.Label.To != "Front Door" {
		t.Fatalf("label change = %#v", label.Change)
	}

	// A custom label survives the next rename, and the envelope says which rule
	// applied.
	kept := cliStatusMutation(t, repository, "status rename", "status", "rename", "intake", "inbox", "--json")
	if kept.Change.Label == nil || kept.Change.Label.To != "Front Door" {
		t.Fatalf("kept label = %#v, want the custom label kept", kept.Change.Label)
	}
	if kept.Change.LabelDerived == nil || *kept.Change.LabelDerived {
		t.Fatalf("kept labelDerived = %#v, want false", kept.Change.LabelDerived)
	}

	move := cliStatusMutation(t, repository, "status move", "status", "move", "inbox", "--before", "backlog", "--json")
	if move.Change.Position == nil || move.Change.Position.Before != "backlog" || move.Change.Position.Order != 1 {
		t.Fatalf("move position = %#v, want first, before backlog", move.Change.Position)
	}

	tag := cliStatusMutation(t, repository, "status tag", "status", "tag", "inbox", "--tag", "default", "--json")
	if len(tag.Change.Tags) != 1 || tag.Change.Tags[0] != "default" {
		t.Fatalf("tag change = %#v, want the replacement set", tag.Change)
	}
	if tag.Change.DefaultFrom != "backlog" {
		t.Fatalf("tag defaultFrom = %q, want the handoff from backlog", tag.Change.DefaultFrom)
	}
	if tag.Vocabulary.Default != "inbox" {
		t.Fatalf("default after tag = %q, want inbox", tag.Vocabulary.Default)
	}

	mustRunStatus(t, repository, "status", "tag", "in-review", "--tag", "next")
	untag := cliStatusMutation(t, repository, "status untag", "status", "untag", "in-review", "next", "--json")
	if len(untag.Change.Tags) != 0 {
		t.Fatalf("untag tags = %#v, want an empty set", untag.Change.Tags)
	}

	remove := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "archived", "--into", "done", "--json")
	if remove.Change.Into != "done" || remove.Change.Status != "archived" {
		t.Fatalf("delete change = %#v", remove.Change)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"inbox", "backlog", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after the family = %v, want %v", got, want)
	}

	// The text surface reports the same change, including the two lines the
	// design calls for by name.
	text := initializedRepository(t)
	mustRunStatus(t, text, "status", "add", "triage", "--after", "backlog")
	code, stdout, stderr := run(t, text, "status", "rename", "ready", "next-up")
	if code != 0 || stderr != "" {
		t.Fatalf("status rename = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"Status:\trename\tnext-up", "\tlabel:\tReady → Next Up (derived)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rename text = %q, want %q", stdout, want)
		}
	}
	code, stdout, stderr = run(t, text, "status", "tag", "triage", "--tag", "default")
	if code != 0 || stderr != "" {
		t.Fatalf("status tag = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdefault:\tbacklog → triage") {
		t.Fatalf("tag text = %q, want the default handoff line", stdout)
	}
}

// The guidelines state a project's statuses, so a status change that left them
// alone would ship a generated file that is wrong from the moment it is
// committed. Every mutating verb rewrites them, and the envelope says so.
func TestStatusChangesRegenerateTheGuidelines(t *testing.T) {
	repository := initializedRepository(t)

	rename := cliStatusMutation(t, repository, "status rename",
		"status", "rename", "ready", "todo", "--label", "Next Up", "--json")

	if rename.Docs == nil || len(rename.Docs.Artifacts) != 1 {
		t.Fatalf("rename docs = %#v, want the guidelines alone", rename.Docs)
	}
	artifact := rename.Docs.Artifacts[0]
	if artifact.Path != agentdocs.GuidelinesPath || artifact.State != string(agentdocs.StateStale) || !artifact.Written {
		t.Fatalf("rename docs artifact = %#v, want a stale %s rewritten", artifact, agentdocs.GuidelinesPath)
	}

	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	for _, want := range []string{
		"| 2 | `todo` | Next Up | `next` |",
		"`workbook next` claims from `todo`.",
		"New tasks land in `backlog`.",
		"A dependency is satisfied once it reaches `done`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q after the rename:\n%s", want, guidelines)
		}
	}
	if strings.Contains(guidelines, "`ready`") {
		t.Errorf("guidelines still document the old status:\n%s", guidelines)
	}
	// Regenerated means current, not merely rewritten: `workbook docs status`
	// has to agree that nothing is left to do.
	if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Errorf("docs status after a status change = code %d, %q; want everything current", code, stdout)
	}

	// A second verb, a second rewrite, and the tag legend follows the tags
	// rather than the names.
	tagged := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "done", "--json")
	if tagged.Docs == nil || !tagged.Docs.Artifacts[0].Written {
		t.Fatalf("tag docs = %#v, want the guidelines rewritten", tagged.Docs)
	}
	guidelines = readProjectFile(t, repository, agentdocs.GuidelinesPath)
	for _, want := range []string{
		"| 4 | `in-review` | In Review | `done` |",
		"A dependency is satisfied once it reaches `in-review` or `done`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q after the tag:\n%s", want, guidelines)
		}
	}

	// The text surface reports the file it rewrote, the same way setup does.
	code, stdout, stderr := run(t, repository, "status", "add", "triage", "--after", "backlog")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\twritten") {
		t.Errorf("status add text = %q, want the regenerated guidelines line", stdout)
	}
}

// A generated file somebody edited is never overwritten, and a status change is
// not the place to start. The change is already recorded and published when the
// refresh runs, so the refusal is reported beside a success rather than turned
// into a failure that would leave the ledger ahead of the exit code.
func TestStatusChangeReportsGuidelinesItWillNotOverwrite(t *testing.T) {
	repository := initializedRepository(t)
	edited := strings.Replace(
		readProjectFile(t, repository, agentdocs.GuidelinesPath),
		"# Workbook guidelines", "# Workbook guidelines\n\nOur own preamble.", 1)
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, edited)

	code, stdout, stderr := run(t, repository, "status", "add", "triage", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "status add")

	var document statusMutationDocument
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode status add: %v; output = %s", err, stdout)
	}
	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateModified) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want a modified file left alone", artifact)
	}
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningDocsRefresh {
		t.Fatalf("warnings = %#v, want one %q", envelope.Warnings, core.WarningDocsRefresh)
	}
	if !strings.Contains(envelope.Warnings[0].Message, "workbook docs update --force") {
		t.Fatalf("warning = %q, want the command that overwrites it", envelope.Warnings[0].Message)
	}

	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != edited {
		t.Fatalf("guidelines were overwritten:\n%s", got)
	}
	// The status change itself landed. A documentation refusal is not a
	// configuration refusal.
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done", "triage",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}

	// The same refusal reaches a text-mode caller on stderr, with the change on
	// stdout, and still exits 0.
	code, stdout, stderr = run(t, repository, "status", "add", "archived")
	if code != 0 {
		t.Fatalf("status add = code %d; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\tunchanged") {
		t.Errorf("status add text = %q, want the untouched guidelines reported", stdout)
	}
	if !strings.Contains(stderr, "workbook docs update --force") {
		t.Errorf("status add stderr = %q, want the warning", stderr)
	}
}

// statusVerbFixtures is one exercise of every mutating status verb, and the
// setup each needs to be a legal change on a fresh project.
//
// It is keyed by verb and checked against the family the help schema lists, so
// a verb added to the family without an entry here fails rather than quietly
// going unexercised. The docs refresh is wired per verb, which is exactly the
// shape that drifts: six verbs regenerating and a seventh silently not is
// invisible to a test that only exercises the ones somebody remembered.
var statusVerbFixtures = map[string]struct {
	setup [][]string
	args  []string
}{
	"add":    {args: []string{"status", "add", "triage", "--json"}},
	"rename": {args: []string{"status", "rename", "ready", "todo", "--json"}},
	"label":  {args: []string{"status", "label", "ready", "Next Up", "--json"}},
	"move":   {args: []string{"status", "move", "ready", "--before", "backlog", "--json"}},
	"tag":    {args: []string{"status", "tag", "in-review", "--tag", "next", "--json"}},
	"untag": {
		// Untagging the only status tagged next is refused outright, so the
		// project gets a second one first.
		setup: [][]string{{"status", "tag", "in-review", "--tag", "next"}},
		args:  []string{"status", "untag", "in-review", "next", "--json"},
	},
	"delete": {args: []string{"status", "delete", "in-review", "--into", "done", "--json"}},
}

// statusReadingVerbs are the two verbs that change nothing and so regenerate
// nothing.
var statusReadingVerbs = map[string]bool{"list": true, "log": true}

func TestEveryMutatingStatusVerbRegeneratesTheGuidelines(t *testing.T) {
	for _, verb := range statusSubcommands() {
		if statusReadingVerbs[verb] {
			continue
		}
		fixture, covered := statusVerbFixtures[verb]
		if !covered {
			t.Errorf("status %s has no fixture here, so nothing checks that it regenerates the guidelines", verb)
			continue
		}
		t.Run(verb, func(t *testing.T) {
			repository := initializedRepository(t)
			for _, command := range fixture.setup {
				mustRunStatus(t, repository, command...)
			}
			before := readProjectFile(t, repository, agentdocs.GuidelinesPath)

			document := cliStatusMutation(t, repository, "status "+verb, fixture.args...)

			if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
				t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
			}
			artifact := document.Docs.Artifacts[0]
			if artifact.Path != agentdocs.GuidelinesPath ||
				artifact.State != string(agentdocs.StateStale) || !artifact.Written {
				t.Fatalf("docs artifact = %#v, want a stale %s rewritten", artifact, agentdocs.GuidelinesPath)
			}
			if readProjectFile(t, repository, agentdocs.GuidelinesPath) == before {
				t.Fatalf("status %s reported a rewrite that changed nothing", verb)
			}
			if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
				strings.Contains(stdout, string(agentdocs.StateStale)) {
				t.Fatalf("docs status after status %s = code %d, %q; want everything current", verb, code, stdout)
			}
		})
	}
}

// A display label is authored by whoever can push, and the managed block's own
// terminator is twenty-one bytes of it. A label carrying that string used to
// truncate the block it was written into: the recorded hash covered the whole
// body and the block read back was the truncated one, so every clone reported a
// file nobody had edited as locally modified, every status change warned about
// it forever, `docs update --force` grew the file by a whole block per run, and
// `workbook setup` exited 5 in every clone — from one label.
func TestAStatusLabelCarryingTheBlockTerminatorDoesNotWedgeTheDocs(t *testing.T) {
	author, _ := cliSyncRepositories(t)
	origin := gitOutput(t, author, "remote", "get-url", "origin")
	if code, _, stderr := run(t, author, "setup"); code != 0 {
		t.Fatalf("setup = code %d; stderr = %q", code, stderr)
	}
	mustRunStatus(t, author, "status", "label", "ready", "Next <!-- workbook:end --> Up")

	// The file Workbook just wrote is one it can still read as its own.
	if code, stdout, _ := run(t, author, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateModified)) {
		t.Fatalf("docs status = code %d, %q; want nothing reported as modified", code, stdout)
	}
	guidelines := readProjectFile(t, author, agentdocs.GuidelinesPath)
	if strings.Count(guidelines, "<!-- workbook:end -->") != 1 {
		t.Fatalf("the guidelines carry %d end markers, want the block's own:\n%s",
			strings.Count(guidelines, "<!-- workbook:end -->"), guidelines)
	}
	if !strings.Contains(guidelines, "Next &lt;!-- workbook:end --> Up") {
		t.Fatalf("the label does not read as what was written:\n%s", guidelines)
	}

	// The next change refreshes it rather than refusing, with nothing to warn
	// about.
	code, stdout, stderr := run(t, author, "status", "add", "triage", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "status add")
	if len(envelope.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", envelope.Warnings)
	}
	var document statusMutationDocument
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode status add: %v", err)
	}
	if document.Docs == nil || document.Docs.Artifacts[0].State != string(agentdocs.StateStale) ||
		!document.Docs.Artifacts[0].Written {
		t.Fatalf("docs = %#v, want a stale rewrite rather than a refusal", document.Docs)
	}

	// A forced refresh settles rather than growing the file by a block.
	lines := strings.Count(readProjectFile(t, author, agentdocs.GuidelinesPath), "\n")
	for range 3 {
		if code, _, stderr := run(t, author, "docs", "update", "--force", "--no-skill"); code != 0 {
			t.Fatalf("docs update --force = code %d; stderr = %q", code, stderr)
		}
	}
	if got := strings.Count(readProjectFile(t, author, agentdocs.GuidelinesPath), "\n"); got != lines {
		t.Fatalf("three forced refreshes took the guidelines from %d lines to %d", lines, got)
	}

	// And a teammate can still bootstrap, which is the failure that reached
	// every clone rather than only the one that authored the label.
	mustRunStatus(t, author, "push")
	joining := cliClone(t, origin)
	if code, _, stderr := run(t, joining, "setup"); code != 0 {
		t.Fatalf("setup in a joining clone = code %d; stderr = %q", code, stderr)
	}
	if code, stdout, _ := run(t, joining, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateModified)) {
		t.Fatalf("docs status in a joining clone = code %d, %q; want nothing modified", code, stdout)
	}
}

// Setup installs documentation before it fetches, so that an unreachable origin
// still leaves a clone with documentation. A clone joining a project that
// configured its statuses would therefore write the built-in six and read them
// as the truth, which is the one moment a fresh clone is most likely to believe
// them.
func TestSetupWritesTheGuidelinesTheFetchDelivered(t *testing.T) {
	author, _ := cliSyncRepositories(t)
	mustRunStatus(t, author, "status", "rename", "ready", "todo", "--label", "Next Up")

	joining := cliClone(t, gitOutput(t, author, "remote", "get-url", "origin"))
	code, stdout, stderr := run(t, joining, "setup")
	if code != 0 {
		t.Fatalf("setup = code %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}

	guidelines := readProjectFile(t, joining, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, "| 2 | `todo` | Next Up | `next` |") {
		t.Fatalf("guidelines do not describe the fetched statuses:\n%s", guidelines)
	}
	if strings.Contains(guidelines, "`ready`") {
		t.Fatalf("guidelines describe the statuses this clone had before its fetch:\n%s", guidelines)
	}
	// One line per managed file, whichever pass wrote it.
	if got := strings.Count(stdout, "Docs:\t"+agentdocs.GuidelinesPath); got != 1 {
		t.Fatalf("setup reported the guidelines %d times:\n%s", got, stdout)
	}
	if code, out, _ := run(t, joining, "docs", "status"); code != 0 ||
		strings.Contains(out, string(agentdocs.StateStale)) {
		t.Fatalf("docs status after setup = code %d, %q; want everything current", code, out)
	}
}

// A project that has no guidelines file said so, with `workbook setup
// --no-docs` or `workbook docs remove`. A status change refreshes generated
// documentation and never installs it, so that decision survives.
func TestStatusChangeDoesNotInstallGuidelinesAProjectDeclined(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup --no-docs = code %d; stderr = %q", code, stderr)
	}

	document := cliStatusMutation(t, repository, "status add", "status", "add", "triage", "--json")

	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateAbsent) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want an absent file left absent", artifact)
	}
	path := filepath.Join(repository, filepath.FromSlash(agentdocs.GuidelinesPath))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the file still absent", agentdocs.GuidelinesPath, err)
	}

	// The text surface says which of the two it is. "unchanged" beside the path
	// would read as "already current", which is the opposite of the truth.
	code, stdout, stderr := run(t, repository, "status", "add", "archived")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\tnot installed") {
		t.Errorf("status add text = %q, want the guidelines reported as not installed", stdout)
	}
}

// A file somebody wrote at the guidelines path themselves carries no managed
// block, and a status change appending one would install documentation the
// project never asked for — the same decision as having no file at all, made a
// different way.
func TestStatusChangeLeavesAGuidelinesFileWithNoManagedBlockAlone(t *testing.T) {
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "docs", "remove", "--no-skill"); code != 0 {
		t.Fatalf("docs remove = code %d; stderr = %q", code, stderr)
	}
	handWritten := "# Our own notes\n\nWorkbook wrote none of this.\n"
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, handWritten)

	document := cliStatusMutation(t, repository, "status add", "status", "add", "triage", "--json")

	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateAbsent) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want a blockless file left alone", artifact)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != handWritten {
		t.Fatalf("the hand-written file gained a managed block:\n%s", got)
	}
}

// --no-docs is the escape hatch, and it is the same word `workbook setup`
// already uses for the same decision.
func TestStatusChangeSkipsTheGuidelinesOnRequest(t *testing.T) {
	repository := initializedRepository(t)
	before := readProjectFile(t, repository, agentdocs.GuidelinesPath)

	document := cliStatusMutation(t, repository, "status add",
		"status", "add", "triage", "--no-docs", "--json")
	if document.Docs != nil {
		t.Fatalf("docs = %#v, want nothing reported for a skipped refresh", document.Docs)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != before {
		t.Fatalf("guidelines were regenerated despite --no-docs:\n%s", got)
	}

	code, stdout, stderr := run(t, repository, "status", "add", "archived", "--no-docs")
	if code != 0 || stderr != "" {
		t.Fatalf("status add --no-docs = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\tskipped") {
		t.Errorf("status add --no-docs text = %q, want the skipped line", stdout)
	}
	// The skipped refresh leaves work behind, and `workbook docs` is where it is
	// found. Nothing else has to notice.
	if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
		!strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Errorf("docs status = code %d, %q; want the guidelines reported stale", code, stdout)
	}
}

// The tag set is a replacement, exactly as `update --label` is, and --clear-tags
// is how an empty set is spelled. Both together is a contradiction rather than a
// precedence rule to remember.
func TestStatusTagReplacesTheWholeSet(t *testing.T) {
	repository := initializedRepository(t)

	replaced := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "next", "--tag", "done", "--json")
	if got := replaced.Change.Tags; len(got) != 2 || got[0] != "done" || got[1] != "next" {
		t.Fatalf("tags = %#v, want the canonical done,next", got)
	}

	narrowed := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "next", "--json")
	if got := narrowed.Change.Tags; len(got) != 1 || got[0] != "next" {
		t.Fatalf("tags = %#v, want the tag left out taken away", got)
	}

	cleared := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--clear-tags", "--json")
	if len(cleared.Change.Tags) != 0 {
		t.Fatalf("tags = %#v, want an empty set", cleared.Change.Tags)
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "both",
			args: []string{"status", "tag", "ready", "--tag", "next", "--clear-tags"},
			want: "cannot use --tag with --clear-tags",
		},
		{
			name: "neither",
			args: []string{"status", "tag", "ready"},
			want: "status tag requires --tag or --clear-tags",
		},
		{
			name: "add with both anchors",
			args: []string{"status", "add", "triage", "--before", "ready", "--after", "backlog"},
			want: "status add accepts --before or --after, not both",
		},
		{
			name: "move with neither anchor",
			args: []string{"status", "move", "ready"},
			want: "status move requires exactly one of --before or --after",
		},
		{
			name: "move with both anchors",
			args: []string{"status", "move", "ready", "--before", "done", "--after", "backlog"},
			want: "status move requires exactly one of --before or --after",
		},
		{
			name: "log windowing",
			args: []string{"status", "log", "--limit", "2", "--all"},
			want: "cannot use --limit with --all",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("%v = code %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("%v stderr = %q, want %q", test.args, stderr, test.want)
			}
		})
	}
}

// An unknown tag is a typo, not corrupt data, and the refusal names the three
// tags that exist rather than sending somebody to the help.
func TestStatusRefusesAnUnknownTagAsAValidationFailure(t *testing.T) {
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{"status", "add", "triage", "--tag", "urgent", "--json"},
		{"status", "tag", "ready", "--tag", "urgent", "--json"},
		{"status", "untag", "ready", "urgent", "--json"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 5 {
			t.Fatalf("%v = code %d, want 5; stderr = %q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation,
			`unsupported status tag "urgent"; the tags are: default, done, next`)
	}
}

// Arity is refused by core at the authoring boundary, and the CLI passes the
// message through untouched. Rewording it here would produce two answers to the
// same question, and this one already names the command that fixes the state.
func TestStatusArityRefusalsSurfaceCoreMessagesVerbatim(t *testing.T) {
	repository := preLedgerRepository(t)

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "last done tag",
			args: []string{"status", "untag", "done", "done", "--json"},
			want: "no status is tagged done, so no dependency could ever be satisfied; " +
				"tag another status first: workbook status tag <status> --tag done",
		},
		{
			name: "last next tag",
			args: []string{"status", "tag", "ready", "--clear-tags", "--json"},
			want: "no status is tagged next, so `workbook next` would never return a task; " +
				"tag another status first: workbook status tag <status> --tag next",
		},
		{
			name: "removing the default holder",
			args: []string{"status", "delete", "backlog", "--into", "ready", "--json"},
			want: "no status is tagged default, so a new task would have nowhere to land; " +
				"tag another status first: workbook status tag <status> --tag default",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 5 {
				t.Fatalf("%v = code %d, want 5; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", test.args, stdout)
			}
			assertJSONError(t, stderr, core.CategoryValidation, test.want)
		})
	}

	// Nothing was recorded by any of them. The fixture is a pre-ledger project
	// because a mint writes a genesis, and "seeded" would be true here before the
	// first refusal ever ran.
	if cliStatusList(t, repository).Seeded {
		t.Fatal("a refused status command seeded the configuration ledger")
	}
}

// A value that is no longer live is explained rather than reported missing: the
// chain says where it went, and the ledger says when.
func TestStatusNamesTheChainForARetiredValue(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "triage", "--into", "backlog")
	today := time.Now().UTC().Format("2006-01-02")

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "renamed",
			args: []string{"status", "label", "ready", "Ready Again", "--json"},
			want: fmt.Sprintf(`no status "ready"; it was renamed to "queued" on %s`, today),
		},
		{
			name: "removed",
			args: []string{"status", "move", "triage", "--after", "backlog", "--json"},
			want: fmt.Sprintf(`no status "triage"; it was removed into "backlog" on %s`, today),
		},
		{
			name: "removal destination",
			args: []string{"status", "delete", "in-review", "--into", "triage", "--json"},
			want: fmt.Sprintf(`no status "triage"; it was removed into "backlog" on %s`, today),
		},
		{
			name: "never defined",
			args: []string{"status", "tag", "shipped", "--tag", "done", "--json"},
			want: `no status "shipped" in this project; the statuses are: ` +
				"backlog, queued, in-progress, in-review, done",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := run(t, repository, test.args...)
			if code != 4 {
				t.Fatalf("%v = code %d, want 4; stderr = %q", test.args, code, stderr)
			}
			assertJSONError(t, stderr, core.CategoryNotFound, test.want)
		})
	}
}

// --into is required, never prompted for, and the refusal carries the answer:
// agents run this command, and a prompt would hang one.
func TestStatusDeleteRequiresIntoAndRefusesItself(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "status", "delete", "in-review", "--json")
	if code != 2 {
		t.Fatalf("status delete without --into = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("status delete stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		"status delete requires --into <status>, naming where the removed status's tasks belong; "+
			"this project's statuses are: backlog, ready, in-progress, in-review, done")

	code, _, stderr = run(t, repository, "status", "delete", "in-review", "--into", "in-review", "--json")
	if code != 5 {
		t.Fatalf("status delete into itself = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`status delete cannot forward "in-review" into itself; name where its tasks belong`)

	// Nothing the refusals touched was recorded. The head is the genesis this
	// project was minted with, and it has not moved.
	before := cliStatusList(t, repository).Head
	if code, _, _ := run(t, repository, "status", "delete", "in-review", "--json"); code == 0 {
		t.Fatal("status delete without --into succeeded")
	}
	if after := cliStatusList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused removal", before, after)
	}
}

// What a removal costs is counted before it happens, in the terms the person
// running it is deciding about: how many tasks move, and how many of those
// become claimable where they land.
//
// Claimable means what `workbook next` means. A task whose only dependency is
// finished is claimable, and counting only the tasks with no dependencies at all
// reported a queue growing by one where `workbook next` would hand out two.
func TestStatusDeleteCountsAffectedAndClaimableTasks(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")

	free := cliCreateTask(t, repository, "No dependencies")
	satisfied := cliCreateTask(t, repository, "Waiting on finished work")
	waiting := cliCreateTask(t, repository, "Waiting on unfinished work")
	finished := cliCreateTask(t, repository, "Finished")
	unfinished := cliCreateTask(t, repository, "Unfinished")

	mustRunStatus(t, repository, "depend", satisfied.ID, finished.ID, "--no-sync")
	mustRunStatus(t, repository, "depend", waiting.ID, unfinished.ID, "--no-sync")
	mustRunStatus(t, repository, "update", finished.ID, "--status", "done", "--no-sync")
	for _, id := range []string{free.ID, satisfied.ID, waiting.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "triage", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "ready", "--json")
	if removal.Tasks.Affected != 3 {
		t.Fatalf("affected = %d, want the three tasks in triage", removal.Tasks.Affected)
	}
	if removal.Tasks.ClaimableAfter != 2 {
		t.Fatalf("claimableAfter = %d, want the dependency-free task and the satisfied one",
			removal.Tasks.ClaimableAfter)
	}

	// The count is a prediction about `workbook next`, so it is checked against
	// `workbook next` rather than against itself: draining the queue has to
	// hand out exactly the tasks that were counted.
	claimed := make(map[string]bool)
	for range removal.Tasks.ClaimableAfter {
		code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("next = code %d, stderr %q", code, stderr)
		}
		var task core.Task
		if err := json.Unmarshal(assertJSONResult(t, stdout, "next").Data, &task); err != nil {
			t.Fatal(err)
		}
		if claimed[task.ID] {
			t.Fatalf("next offered %s twice", task.ID)
		}
		claimed[task.ID] = true
		// Taking it out of the queue is what lets the next call answer about a
		// different task.
		mustRunStatus(t, repository, "update", task.ID, "--status", "in-progress", "--no-sync")
	}
	if !claimed[free.ID] || !claimed[satisfied.ID] {
		t.Fatalf("next handed out %#v, want the two tasks the count promised", claimed)
	}
	code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("next = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"data":null`) {
		t.Fatalf("next after the counted tasks = %s, want no eligible task", stdout)
	}

	// The tasks were not rewritten; they resolve into the destination.
	document := cliStatusList(t, repository)
	if len(document.Retired) != 1 || document.Retired[0].Status != "triage" ||
		document.Retired[0].Becomes != "ready" || document.Retired[0].Operation != "status.remove" {
		t.Fatalf("retired = %#v, want triage forwarded to ready", document.Retired)
	}
	if document.Retired[0].At == "" {
		t.Fatal("retired entry carries no date, want the ledger's wall time")
	}
}

// A destination outside `next` leaves nothing claimable, which is the other half
// of the count and the reason it is reported separately from the total.
func TestStatusDeleteReportsNoClaimableTasksForAParkedDestination(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	task := cliCreateTask(t, repository, "Alpha")
	mustRunStatus(t, repository, "update", task.ID, "--status", "triage", "--no-sync")

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "in-progress", "--json")
	if removal.Tasks.Affected != 1 || removal.Tasks.ClaimableAfter != 0 {
		t.Fatalf("tasks = %#v, want one affected and none claimable", removal.Tasks)
	}
}

// The inverse matrix, verified by running it — and by running the one the log
// emits, through a shell, exactly as printed.
//
// Three things are being checked at once, and each of them failed at some point
// on this branch. The command the log prints has to be the command the verb
// printed, because they are one computation and a second one drifts. It has to
// survive a shell, because a label with a space in it is one argument only if
// the quoting is right, and a test that split on whitespace could not have seen
// that. And exact:true has to mean the vocabulary comes back, which is the only
// claim a reader has no way to check for themselves.
func TestStatusInverseMatrixRoundTripsThroughAShell(t *testing.T) {
	binary := buildWorkbookBinary(t)

	for _, test := range []struct {
		name    string
		setup   [][]string
		command []string
		verb    string
		inverse string
		exact   bool
		note    string
	}{
		{
			name:    "add",
			command: []string{"status", "add", "triage", "--after", "backlog", "--json"},
			verb:    "status add",
			inverse: "workbook status delete triage --into backlog",
			note:    `tasks created in "triage" since are forwarded to "backlog"`,
		},
		{
			name:    "add taking the default",
			command: []string{"status", "add", "intake", "--tag", "default", "--json"},
			verb:    "status add",
			inverse: "workbook status tag backlog --tag default",
			note: `that returns the default tag to "backlog", which "intake" holds; ` +
				"workbook status delete intake --into backlog then removes the status, " +
				"forwarding the tasks created in it since",
		},
		{
			name:    "rename",
			command: []string{"status", "rename", "ready", "queued", "--json"},
			verb:    "status rename",
			inverse: "workbook status rename queued ready --label Ready",
			exact:   true,
		},
		{
			name:    "rename with an explicit label",
			command: []string{"status", "rename", "ready", "queued", "--label", "On Deck", "--json"},
			verb:    "status rename",
			inverse: `workbook status rename queued ready --label Ready`,
			exact:   true,
		},
		{
			name:    "rename keeping a custom label",
			setup:   [][]string{{"status", "label", "ready", "On Deck"}},
			command: []string{"status", "rename", "ready", "queued", "--json"},
			verb:    "status rename",
			inverse: "workbook status rename queued ready",
			exact:   true,
		},
		{
			name:    "label",
			command: []string{"status", "label", "ready", "On Deck", "--json"},
			verb:    "status label",
			inverse: `workbook status label ready Ready`,
			exact:   true,
		},
		{
			name:    "label with a space",
			setup:   [][]string{{"status", "label", "ready", "Next Up"}},
			command: []string{"status", "label", "ready", "On Deck", "--json"},
			verb:    "status label",
			inverse: `workbook status label ready "Next Up"`,
			exact:   true,
		},
		{
			name:    "move",
			command: []string{"status", "move", "done", "--before", "backlog", "--json"},
			verb:    "status move",
			inverse: "workbook status move done --after in-review",
			exact:   true,
		},
		{
			name:    "move from the first position",
			command: []string{"status", "move", "backlog", "--after", "done", "--json"},
			verb:    "status move",
			inverse: "workbook status move backlog --before ready",
			exact:   true,
		},
		{
			name:    "tag",
			command: []string{"status", "tag", "in-review", "--tag", "done", "--json"},
			verb:    "status tag",
			inverse: "workbook status tag in-review --clear-tags",
			exact:   true,
		},
		{
			name:    "tag taking the default",
			command: []string{"status", "tag", "in-review", "--tag", "default", "--json"},
			verb:    "status tag",
			inverse: "workbook status tag backlog --tag default",
			note: `that returns the default tag to "backlog"; ` +
				`workbook status tag in-review --clear-tags restores "in-review"'s own tags`,
		},
		{
			name:    "untag",
			setup:   [][]string{{"status", "tag", "in-progress", "--tag", "next"}},
			command: []string{"status", "untag", "in-progress", "next", "--json"},
			verb:    "status untag",
			inverse: "workbook status tag in-progress --tag next",
			exact:   true,
		},
		{
			name:    "delete",
			setup:   [][]string{{"status", "add", "triage", "--after", "backlog", "--label", "Triage"}},
			command: []string{"status", "delete", "triage", "--into", "backlog", "--json"},
			verb:    "status delete",
			inverse: "workbook status add triage --after backlog --label Triage",
			note: `tasks still stored under "triage" return to it, because defining the name again ` +
				`drops the forwarding pointer; tasks a later write settled into "backlog" stay there`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := initializedRepository(t)
			for _, setup := range test.setup {
				mustRunStatus(t, repository, setup...)
			}
			before := cliStatusList(t, repository)

			result := cliStatusMutation(t, repository, test.verb, test.command...)
			assertInverse(t, "the command's", result.Inverse, test.inverse, test.exact, test.note)

			// The log reads the same change out of the ledger and has to reach
			// the same answer, because it is the same computation.
			logged := cliStatusLog(t, repository, "--limit", "1").Entries
			if len(logged) != 1 || logged[0].Inverse == nil {
				t.Fatalf("log = %#v, want the change with an inverse", logged)
			}
			assertInverse(t, "the log's", *logged[0].Inverse, test.inverse, test.exact, test.note)

			// Executed as printed, by a shell, against the built binary.
			runInverseThroughShell(t, binary, repository, logged[0].Inverse.Command)
			after := cliStatusList(t, repository)
			if !test.exact {
				return
			}
			if !equalStatusDocuments(before, after) {
				t.Fatalf("an exact inverse left %#v, want the state it found %#v",
					after.Statuses, before.Statuses)
			}
		})
	}
}

func assertInverse(t *testing.T, whose string, got inverseDocument, command string, exact bool, note string) {
	t.Helper()
	if got.Command != command {
		t.Fatalf("%s inverse = %q, want %q", whose, got.Command, command)
	}
	if got.Exact != exact {
		t.Fatalf("%s inverse exact = %t, want %t", whose, got.Exact, exact)
	}
	if got.Note != note {
		t.Fatalf("%s inverse note = %q, want %q", whose, got.Note, note)
	}
}

// runInverseThroughShell runs a printed inverse the way a person would: paste
// it into a shell. Splitting it on whitespace instead would test a command
// nobody runs and would never notice a quoting failure.
func runInverseThroughShell(t *testing.T, binary, repository, command string) {
	t.Helper()
	if !strings.HasPrefix(command, "workbook ") {
		t.Fatalf("inverse %q does not begin with the command name", command)
	}
	script := shellQuote(binary) + strings.TrimPrefix(command, "workbook")
	shell := exec.Command("/bin/sh", "-c", script)
	shell.Dir = repository
	output, err := shell.CombinedOutput()
	if err != nil {
		t.Fatalf("running the printed inverse failed: %v\n$ %s\n%s", err, script, output)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// The log is the same window `show --history` renders, over a different
// history: oldest first, the ten most recent by default, and every entry
// carrying the command that reverses it.
//
// One entry is one commit, which is the unit `show --history` counts and the
// unit a person recognizes: one command they ran. It is also what lets the
// total stay exact while the read is bounded by the window.
func TestStatusLogMirrorsShowHistoryWindowing(t *testing.T) {
	// The ledgerless report needs a project with no ledger, and a mint writes
	// one, so the fixture is the pre-ledger shape a real upgrade lands on.
	repository := preLedgerRepository(t)
	if code, stdout, stderr := run(t, repository, "status", "log"); code != 0 || stderr != "" {
		t.Fatalf("status log on a ledgerless project = code %d, stderr %q", code, stderr)
	} else if !strings.Contains(stdout, "No status change is recorded") {
		t.Fatalf("status log = %q, want the ledgerless report", stdout)
	}

	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	for index := range 11 {
		mustRunStatus(t, repository, "status", "label", "triage", fmt.Sprintf("Triage %d", index))
	}

	document := cliStatusLog(t, repository)
	if document.Total != 13 {
		t.Fatalf("total = %d, want the genesis, the add, and eleven relabels", document.Total)
	}
	if document.Showing != 10 {
		t.Fatalf("showing = %d, want the default window of ten", document.Showing)
	}
	if document.Entries[0].Summary != `labelled status triage "Triage 1"` {
		t.Fatalf("first entry = %#v, want the oldest change in the window", document.Entries[0])
	}
	last := document.Entries[len(document.Entries)-1]
	if last.Summary != `labelled status triage "Triage 10"` {
		t.Fatalf("last entry = %#v, want the newest change", last)
	}
	if last.Inverse == nil || last.Inverse.Command != `workbook status label triage "Triage 9"` || !last.Inverse.Exact {
		t.Fatalf("last inverse = %#v, want the label it replaced", last.Inverse)
	}
	if last.Commit == "" || last.OperationID == "" || last.Actor == "" || last.WallTime.IsZero() {
		t.Fatalf("entry = %#v, want commit, operation ID, actor and wall time", last)
	}

	windowed := cliStatusLog(t, repository, "--limit", "2")
	if windowed.Showing != 2 || windowed.Total != 13 {
		t.Fatalf("limited log = %d of %d, want 2 of 13", windowed.Showing, windowed.Total)
	}
	// The window bounds the read, and the oldest entry in it still carries an
	// inverse — which is only possible if the commit before the window was read
	// for its checkpoint.
	if windowed.Entries[0].Inverse == nil {
		t.Fatalf("windowed log = %#v, want the oldest entry's inverse read against its parent", windowed.Entries[0])
	}
	if windowed.Entries[0].Inverse.Command != `workbook status label triage "Triage 8"` {
		t.Fatalf("windowed inverse = %q, want the label the change replaced", windowed.Entries[0].Inverse.Command)
	}

	every := cliStatusLog(t, repository, "--all")
	if every.Showing != 13 {
		t.Fatalf("--all showing = %d, want every change", every.Showing)
	}
	if every.Entries[0].Operation != "config.genesis" || every.Entries[0].Inverse != nil {
		t.Fatalf("first entry = %#v, want the genesis with no inverse", every.Entries[0])
	}

	code, stdout, stderr := run(t, repository, "status", "log", "--limit", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("status log text = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "Showing 3 most recent changes out of 13.") {
		t.Fatalf("status log text = %q, want the window header", stdout)
	}
	if !strings.Contains(stdout, "\tinverse:\tworkbook status label triage") {
		t.Fatalf("status log text = %q, want an inverse line", stdout)
	}

	// `--limit=` sets the flag to an empty value, which used to slip past the
	// conflict check and let --all win in silence.
	for _, args := range [][]string{
		{"status", "log", "--limit=", "--all"},
		{"status", "log", "--limit", "2", "--all"},
	} {
		code, _, stderr := run(t, repository, args...)
		if code != 2 || !strings.Contains(stderr, "cannot use --limit with --all") {
			t.Fatalf("%v = code %d, stderr %q; want the conflict refused", args, code, stderr)
		}
	}
}

// One command is one entry, however many operations it recorded. A rename that
// also moves the label writes two operations into one commit, and the entry
// reports the command — with the inverse that undoes all of it.
func TestStatusLogReportsOneEntryPerRecordedCommand(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")

	document := cliStatusLog(t, repository, "--all")
	if document.Total != 2 {
		t.Fatalf("total = %d, want the genesis and the rename", document.Total)
	}
	rename := document.Entries[1]
	if rename.Operation != "status.rename" {
		t.Fatalf("entry operation = %q, want the rename", rename.Operation)
	}
	if rename.Summary != "renamed status ready to queued (+1 more change(s) in this commit)" {
		t.Fatalf("summary = %q, want the command with the rest of its commit counted", rename.Summary)
	}
	// The same count, reachable without parsing the sentence that states it.
	if rename.Collapsed != 1 {
		t.Fatalf("collapsed = %d, want the relabel this commit also recorded", rename.Collapsed)
	}
	if document.Entries[0].Collapsed != 0 {
		t.Fatalf("genesis collapsed = %d, want nothing beyond the seeding itself", document.Entries[0].Collapsed)
	}
	if rename.Inverse == nil || rename.Inverse.Command != "workbook status rename queued ready --label Ready" {
		t.Fatalf("inverse = %#v, want the rename and the label it moved", rename.Inverse)
	}
	if rename.OperationID == "" {
		t.Fatal("entry carries no operation ID")
	}
}

// An ordinary rename survives a teammate publishing something else, which is the
// coupling gitstore's replay fixtures can only assume.
//
// `workbook status rename` records two operations whenever the display label
// follows the machine value — the rename, then a relabel of the token the rename
// just created — and that is the default for every derived label. Nothing on the
// replay path may read the second operation as an edit to a status nobody
// defines, and nothing but this test notices if the verb stops emitting the shape
// gitstore's fixtures reproduce by hand.
func TestStatusRenameReplaysAfterATeammatePublishes(t *testing.T) {
	first, second := cliSyncRepositories(t)
	mustRunStatus(t, first, "status", "add", "triage", "--after", "backlog")
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = code %d; stderr = %q", code, stderr)
	}

	// The teammate renames the column offline. Its label was derived, so the
	// rename carries a relabel of the new value with it.
	rename := cliStatusMutation(t, second, "status rename",
		"status", "rename", "triage", "intake", "--no-sync", "--json")
	if rename.Change.Label == nil || rename.Change.Label.To != "Intake" {
		t.Fatalf("rename label = %#v, want the derived Intake that makes this a two-operation pack",
			rename.Change.Label)
	}

	// Somebody else publishes anything at all, so the rename has to replay.
	mustRunStatus(t, first, "status", "label", "done", "Delivered")

	code, stdout, stderr := run(t, second, "fetch")
	if code != 0 {
		t.Fatalf("fetch after a rename = code %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "Config conflict") || strings.Contains(stdout, "Config conflict") {
		t.Fatalf("an ordinary rename conflicted:\n%s\n%s", stdout, stderr)
	}

	document := cliStatusList(t, second)
	names := make([]string, 0, len(document.Statuses))
	label := ""
	for _, status := range document.Statuses {
		names = append(names, status.Status)
		if status.Status == "intake" {
			label = status.Label
		}
	}
	if !equalStrings(names, []string{"backlog", "intake", "ready", "blocked", "in-progress", "in-review", "done"}) {
		t.Fatalf("statuses = %v, want the rename replayed in place", names)
	}
	if label != "Intake" {
		t.Fatalf("intake label = %q, want the relabel from the same pack to have landed", label)
	}
	// The forwarding pointer survives the replay, so a task stored under the old
	// value still reads into the new column.
	if len(document.Retired) != 1 || document.Retired[0].Status != "triage" ||
		document.Retired[0].Becomes != "intake" || document.Retired[0].Operation != "status.rename" {
		t.Fatalf("retired = %#v, want triage forwarding to intake", document.Retired)
	}
	// And the teammate's own change is still there, so the replay landed on top
	// of it rather than instead of it.
	for _, status := range document.Statuses {
		if status.Status == "done" && status.Label != "Delivered" {
			t.Fatalf("done label = %q, want the fetched change preserved", status.Label)
		}
	}
}

// A status change is synchronized on the same terms a task change is: the
// report says what it did, --no-sync says it deliberately did nothing, and the
// ledger reaches origin without a task ref to carry it.
func TestStatusChangesReportAndPublishSynchronization(t *testing.T) {
	first, second := cliSyncRepositories(t)

	deliberate := cliStatusMutation(t, first, "status add", "status", "add", "triage", "--no-sync", "--json")
	if deliberate.Vocabulary.Head == "" {
		t.Fatal("a --no-sync status change recorded nothing")
	}
	code, stdout, stderr := run(t, first, "status", "add", "wip", "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status add --no-sync = code %d, stderr %q", code, stderr)
	}
	var envelope statusEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Sync == nil || envelope.Sync.Enabled ||
		envelope.Sync.Status != syncStatusSkipped ||
		envelope.Sync.Detail != "automatic synchronization is disabled" {
		t.Fatalf("sync = %#v, want a deliberate skip", envelope.Sync)
	}

	// Without --no-sync the same command fetches first and publishes the ledger.
	code, stdout, stderr = run(t, first, "status", "rename", "ready", "queued", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status rename = code %d, stderr %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Sync == nil || !envelope.Sync.Enabled || envelope.Sync.Status != syncStatusCompleted {
		t.Fatalf("sync = %#v, want a completed synchronization", envelope.Sync)
	}

	// The teammate sees every change, including the two made before the ledger
	// was ever published, because publication sends the whole ref.
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = code %d; stderr = %q", code, stderr)
	}
	// The six rather than the five: this fixture's clones join through a plain
	// `git clone` of a bare origin that carries no configuration ledger, so both
	// of them are pre-ledger projects and the first status change seeds the
	// vocabulary such a project is using. Publication is what is under test here,
	// and it carries whatever the ledger holds.
	names := cliStatusNames(t, second)
	if !equalStrings(names, []string{
		"backlog", "queued", "blocked", "in-progress", "in-review", "done", "triage", "wip",
	}) {
		t.Fatalf("teammate statuses = %v, want the published vocabulary", names)
	}
	if !cliStatusList(t, second).Seeded {
		t.Fatal("the teammate's ledger is not seeded after fetching one")
	}
}

// The list names the tasks nothing can place, and the command that places them.
// A stored status that resolves nowhere is invisible to every count — it is in
// no column — so the census that produces the counts is what has to find it.
func TestStatusListReportsUnresolvedStoredStatuses(t *testing.T) {
	repository := initializedRepository(t)
	kept := cliCreateTask(t, repository, "In a real column")
	stranded := writeTaskInAnUndefinedStatus(t, repository, "shipped", "Written by a newer clone")

	document := cliStatusList(t, repository)
	if len(document.Unresolved) != 1 {
		t.Fatalf("unresolved = %#v, want the one stranded status", document.Unresolved)
	}
	entry := document.Unresolved[0]
	if entry.Status != "shipped" || entry.Tasks != 1 || len(entry.TaskIDs) != 1 || entry.TaskIDs[0] != stranded {
		t.Fatalf("unresolved entry = %#v, want the stranded task", entry)
	}
	for _, status := range document.Statuses {
		if status.Tasks == nil {
			t.Fatalf("status %q reports no task count", status.Status)
		}
		if status.Status == "backlog" && *status.Tasks != 1 {
			t.Fatalf("backlog holds %d tasks, want only %s", *status.Tasks, kept.ID)
		}
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"\tUnresolved:\tshipped\t1 task(s)\t" + stranded,
		"correct with: workbook update <task> --status <status>",
		"workbook status add shipped",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status list text = %q, want %q", stdout, want)
		}
	}
}

// A folded configuration may sit over a size ceiling without anybody having
// been refused anything: two clones adding statuses concurrently is enough. The
// list says so where a person is already looking at what they would remove.
func TestStatusListReportsTheOverCeilingAdvisory(t *testing.T) {
	first, second := cliSyncRepositories(t)

	// One clone fills the project to the ceiling and publishes it.
	additions := make([]core.ConfigOperation, 0, core.MaxStatusCount-6)
	for index := range core.MaxStatusCount - 6 {
		name := core.Status(fmt.Sprintf("extra-%02d", index))
		additions = append(additions, core.ConfigOperation{
			Type:  core.ConfigStatusAdd,
			Name:  name,
			Label: core.DerivedStatusLabel(name),
			Rank:  fmt.Sprintf("%d/1", 100+index),
		})
	}
	writeConfigOperations(t, first, additions)
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("push = code %d; stderr = %q", code, stderr)
	}

	// The other clone adds one status without having seen any of that, which is
	// a change neither author could have been refused.
	mustRunStatus(t, second, "status", "add", "triage", "--no-sync")
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = code %d; stderr = %q", code, stderr)
	}

	document := cliStatusList(t, second)
	if got, want := len(document.Statuses), core.MaxStatusCount+1; got != want {
		t.Fatalf("statuses = %d, want %d after the reconcile", got, want)
	}
	if len(document.Advisories) != 1 || document.Advisories[0].Code != "status-ceiling-exceeded" {
		t.Fatalf("advisories = %#v, want the ceiling advisory", document.Advisories)
	}
	if !strings.Contains(document.Advisories[0].Message, "removing one brings it back under") {
		t.Fatalf("advisory = %q, want the way back named", document.Advisories[0].Message)
	}

	code, stdout, stderr := run(t, second, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tAdvisory:\tstatus-ceiling-exceeded\t") {
		t.Fatalf("status list text = %q, want the advisory line", stdout)
	}

	// Growth is refused while over the ceiling, and the refusal names the verb
	// that brings it back under.
	code, _, stderr = run(t, second, "status", "add", "another", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("status add over the ceiling = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		fmt.Sprintf("the project would define %d statuses and must not exceed %d; "+
			"remove one first: workbook status delete <status> --into <status>",
			core.MaxStatusCount+2, core.MaxStatusCount))
}

// The list filter accepts a value this project does not have, answers with the
// tasks it selects, and says what it did — on both surfaces. Refusing it would
// fail a caller whose clone is merely behind; saying nothing would leave an
// empty table indistinguishable from an empty column.
func TestListStatusFilterWarnsWithoutFailing(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Alpha")
	mustRunStatus(t, repository, "update", task.ID, "--status", "ready", "--no-sync")
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")

	t.Run("unknown status in JSON", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "typoo", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("list --status typoo = code %d, stderr %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "list")
		var tasks []core.Task
		if err := json.Unmarshal(result.Data, &tasks); err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 0 {
			t.Fatalf("list --status typoo returned %d tasks, want none", len(tasks))
		}
		if len(result.Warnings) != 1 || result.Warnings[0].Code != core.WarningStatusFilter ||
			result.Warnings[0].Message != `no status "typoo" in this project's vocabulary` {
			t.Fatalf("warnings = %#v, want the missing status named", result.Warnings)
		}
	})

	t.Run("unknown status in text", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "typoo")
		if code != 0 {
			t.Fatalf("list --status typoo = code %d; stderr = %q", code, stderr)
		}
		if strings.Contains(stdout, task.ID) {
			t.Fatalf("list --status typoo listed %q", stdout)
		}
		if stderr != "workbook: warning: no status \"typoo\" in this project's vocabulary\n" {
			t.Fatalf("stderr = %q, want one warning line", stderr)
		}
	})

	t.Run("resolvable status is followed", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "ready", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("list --status ready = code %d, stderr %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "list")
		var tasks []core.Task
		if err := json.Unmarshal(result.Data, &tasks); err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 || tasks[0].ID != task.ID {
			t.Fatalf("list --status ready returned %#v, want the renamed status's task", tasks)
		}
		if len(result.Warnings) != 1 ||
			result.Warnings[0].Message != `no status "ready" in this project's vocabulary; it was renamed to "queued", and "queued" is what was listed` {
			t.Fatalf("warnings = %#v, want the rename explained", result.Warnings)
		}
	})

	t.Run("a live status warns about nothing", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "queued", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("list --status queued = code %d, stderr %q", code, stderr)
		}
		if len(assertJSONResult(t, stdout, "list").Warnings) != 0 {
			t.Fatalf("list --status queued warned about a live status: %q", stdout)
		}
	})
}

// A chain with two kinds of hop in it has to be described by what happened, not
// by where it ended.
//
// Forwarding answers about one hop by contract, and pairing that hop's verb with
// the chain's final destination produced sentences describing a change nobody
// made: "renamed to backlog", for a value that was renamed to `sorting` and only
// reached `backlog` because somebody later removed that. Both surfaces say the
// hop, then the end of the chain, and they say it the same way.
func TestStatusChainsAreDescribedByWhatHappened(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Carried along")
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	mustRunStatus(t, repository, "update", task.ID, "--status", "triage", "--no-sync")

	// A rename, then a removal of the new name.
	mustRunStatus(t, repository, "status", "rename", "triage", "sorting")
	mustRunStatus(t, repository, "status", "delete", "sorting", "--into", "backlog")

	// A removal, then a rename of the destination.
	mustRunStatus(t, repository, "status", "add", "parking", "--after", "backlog")
	mustRunStatus(t, repository, "status", "add", "holding", "--after", "parking")
	mustRunStatus(t, repository, "status", "delete", "parking", "--into", "holding")
	mustRunStatus(t, repository, "status", "rename", "holding", "storage")

	today := time.Now().UTC().Format("2006-01-02")
	for _, test := range []struct {
		name    string
		status  string
		refusal string
		warning string
	}{
		{
			name:   "renamed then removed",
			status: "triage",
			refusal: fmt.Sprintf(
				`no status "triage"; it was renamed to "sorting" on %s, which now resolves to "backlog"`, today),
			warning: `no status "triage" in this project's vocabulary; it was renamed to "sorting", ` +
				`which now resolves to "backlog", and "backlog" is what was listed`,
		},
		{
			name:   "removed then renamed",
			status: "parking",
			refusal: fmt.Sprintf(
				`no status "parking"; it was removed into "holding" on %s, which now resolves to "storage"`, today),
			warning: `no status "parking" in this project's vocabulary; it was removed into "holding", ` +
				`which now resolves to "storage", and "storage" is what was listed`,
		},
		{
			name:    "one hop only",
			status:  "sorting",
			refusal: fmt.Sprintf(`no status "sorting"; it was removed into "backlog" on %s`, today),
			warning: `no status "sorting" in this project's vocabulary; it was removed into "backlog", ` +
				`and "backlog" is what was listed`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _, stderr := run(t, repository, "status", "label", test.status, "Renamed", "--json")
			if code != 4 {
				t.Fatalf("status label %s = code %d, want 4; stderr = %q", test.status, code, stderr)
			}
			assertJSONError(t, stderr, core.CategoryNotFound, test.refusal)

			code, stdout, stderr := run(t, repository, "list", "--status", test.status, "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("list --status %s = code %d, stderr %q", test.status, code, stderr)
			}
			warnings := assertJSONResult(t, stdout, "list").Warnings
			if len(warnings) != 1 || warnings[0].Message != test.warning {
				t.Fatalf("warnings = %#v, want %q", warnings, test.warning)
			}
		})
	}

	// The task itself is where the chain leads, which is the thing all this
	// describes.
	listed := cliStatusList(t, repository)
	for _, status := range listed.Statuses {
		if status.Status == "backlog" && (status.Tasks == nil || *status.Tasks != 1) {
			t.Fatalf("backlog holds %#v tasks, want the task carried through both hops", status.Tasks)
		}
	}
}

// The note on a removal's inverse says which tasks come back, and both halves
// of that are checked against real tasks.
//
// Defining the name again drops the forwarding pointer, so a task still stored
// under the old value reads as being in that column again — the note used to
// claim the opposite. What does not come back is a task some later write
// settled, because correct-on-touch rewrote its stored value to the destination
// and no configuration change can find it again.
func TestStatusDeleteInverseReturnsStoredTasksAndLeavesSettledOnes(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	stored := cliCreateTask(t, repository, "Never touched again")
	settled := cliCreateTask(t, repository, "Touched after the removal")
	for _, id := range []string{stored.ID, settled.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "triage", "--no-sync")
	}

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "backlog", "--json")
	want := `tasks still stored under "triage" return to it, because defining the name again drops the ` +
		`forwarding pointer; tasks a later write settled into "backlog" stay there`
	if removal.Inverse.Note != want {
		t.Fatalf("note = %q, want %q", removal.Inverse.Note, want)
	}

	// One task is written to after the removal, which settles its stored status
	// into the destination. The other is left alone.
	mustRunStatus(t, repository, "update", settled.ID, "--title", "Touched", "--no-sync")

	mustRunStatus(t, repository, strings.Fields(strings.TrimPrefix(removal.Inverse.Command, "workbook "))...)

	if got := taskStatus(t, repository, stored.ID); got != "triage" {
		t.Fatalf("the untouched task is in %q, want it back in triage as the note says", got)
	}
	if got := taskStatus(t, repository, settled.ID); got != "backlog" {
		t.Fatalf("the settled task is in %q, want it left in backlog as the note says", got)
	}
}

func taskStatus(t *testing.T, repository, id string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show %s = code %d, stderr %q", id, code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &task); err != nil {
		t.Fatal(err)
	}
	return string(task.Status)
}

// The listing dates a retirement from the newest commits and says nothing for
// one older than that, rather than reading a history that only grows to answer
// a courtesy.
func TestStatusListDatesRecentRetirementsAndDegradesBeyondItsBound(t *testing.T) {
	repository := initializedRepository(t)
	// One early retirement, then enough commits to push it past the bound.
	mustRunStatus(t, repository, "status", "add", "ancient", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "ancient", "--into", "backlog")
	packs := make([][]core.ConfigOperation, 0, maxDatedConfigCommits)
	for index := range maxDatedConfigCommits {
		packs = append(packs, []core.ConfigOperation{{
			Type:   core.ConfigStatusRelabel,
			Status: "backlog",
			Label:  fmt.Sprintf("Backlog %d", index),
		}})
	}
	writeConfigCommits(t, repository, packs)
	mustRunStatus(t, repository, "status", "add", "recent", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "recent", "--into", "backlog")

	document := cliStatusList(t, repository)
	dates := make(map[string]string, len(document.Retired))
	for _, retired := range document.Retired {
		dates[retired.Status] = retired.At
	}
	if len(dates) != 2 {
		t.Fatalf("retired = %#v, want both removals reported", document.Retired)
	}
	if dates["recent"] == "" {
		t.Fatal("the recent removal carries no date, want one from inside the bound")
	}
	if dates["ancient"] != "" {
		t.Fatalf("the ancient removal carries the date %q, want none from beyond the bound", dates["ancient"])
	}

	// Both are still reported, with the undated one simply missing its clause.
	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tRetired:\tancient → backlog\tremoved\n") {
		t.Fatalf("status list text = %q, want the undated retirement with no clause", stdout)
	}
	if !strings.Contains(stdout, "\tRetired:\trecent → backlog\tremoved on ") {
		t.Fatalf("status list text = %q, want the dated retirement", stdout)
	}
}

// Two sessions writing the ledger at once: the loser is refused with something
// it can act on, nothing half-written survives, and the retry the refusal asks
// for succeeds.
func TestStatusWritesUnderContentionAreRefusedAndRetryable(t *testing.T) {
	repository := initializedRepository(t)
	const writers = 4

	type outcome struct {
		code   int
		stderr string
		name   string
	}
	outcomes := make([]outcome, writers)
	var wait sync.WaitGroup
	for index := range writers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			name := fmt.Sprintf("racer-%d", index)
			code, _, stderr := run(t, repository, "status", "add", name, "--no-sync", "--json")
			outcomes[index] = outcome{code: code, stderr: stderr, name: name}
		}(index)
	}
	wait.Wait()

	accepted := 0
	for _, result := range outcomes {
		switch result.code {
		case 0:
			accepted++
		case 6:
			assertJSONError(t, result.stderr, core.CategoryStaleWrite,
				"another process changed this project's statuses while this command was writing; "+
					"nothing was recorded, so run it again")
		default:
			t.Fatalf("concurrent status add exited %d, want 0 or 6; stderr = %q", result.code, result.stderr)
		}
	}
	if accepted == 0 {
		t.Fatalf("no concurrent status add succeeded; outcomes = %#v", outcomes)
	}

	// Whatever the interleaving, the ledger holds exactly the statuses whose
	// commands reported success, and the refused ones still work.
	present := make(map[string]bool)
	for _, name := range cliStatusNames(t, repository) {
		present[name] = true
	}
	for _, result := range outcomes {
		if result.code == 0 && !present[result.name] {
			t.Fatalf("status %q reported success but is not defined", result.name)
		}
		if result.code == 6 && present[result.name] {
			t.Fatalf("status %q was refused but is defined", result.name)
		}
		if result.code == 6 {
			mustRunStatus(t, repository, "status", "add", result.name, "--no-sync")
		}
	}
}

// The advice a lost compare-and-swap carries is the difference between an error
// a caller retries and one it reports. Provoking the race is the concurrency
// test above; this pins what the caller is told when it happens.
func TestStatusStaleWriteAdvisesTheRetry(t *testing.T) {
	err := statusWriteError(core.Wrap(core.CategoryStaleWrite,
		"the configuration ledger changed concurrently", nil))
	if got := core.ExitCode(err); got != 6 {
		t.Fatalf("exit code = %d, want 6", got)
	}
	if got, want := publicErrorMessage(err),
		"another process changed this project's statuses while this command was writing; "+
			"nothing was recorded, so run it again"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	// Every other failure is passed through untouched.
	validation := core.Errorf(core.CategoryValidation, "no status is tagged done")
	if statusWriteError(validation) != validation {
		t.Fatal("statusWriteError rewrote a failure that was not a lost race")
	}
}

func TestStatusHelpDocumentsTheFamily(t *testing.T) {
	output := assertHelpOutput(t, []string{"help", "status"}, "Usage: workbook status <command> [options]")
	for _, want := range []string{"list", "add", "rename", "label", "move", "tag", "untag", "delete", "log"} {
		if !strings.Contains(output, "  "+want) {
			t.Errorf("status help = %q, want subcommand %q", output, want)
		}
	}
	for _, test := range []struct {
		target []string
		want   string
	}{
		{target: []string{"help", "status", "add"}, want: "workbook status add <status>"},
		{target: []string{"status", "delete", "-h"}, want: "workbook status delete <status> --into <status>"},
		{target: []string{"status", "log", "--help"}, want: "workbook status log [--limit <n>] [--all] [--json]"},
	} {
		assertHelpOutput(t, test.target, test.want)
	}
}

// writeTaskInAnUndefinedStatus records a task whose stored status this project
// cannot resolve.
//
// It goes through the core service against a vocabulary of its own rather than
// through a command, because no command can produce this state: the mutation
// boundary refuses a status the project does not define, and every rename and
// removal leaves a forwarding pointer behind. What it reproduces is a task
// written by a clone whose configuration this one has never seen — the state
// the unresolved report exists for.
func writeTaskInAnUndefinedStatus(t *testing.T, repository, status, title string) string {
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
	store, err := projection.Open(ctx, repo, config)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := repo.Actor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{{
		Status: core.Status(status),
		Label:  core.DerivedStatusLabel(core.Status(status)),
		Rank:   "1/1",
		Tags:   []core.StatusTag{core.StatusTagDefault, core.StatusTagNext, core.StatusTagDone},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := core.Service{
		Config:     config,
		Vocabulary: vocabulary,
		Reader:     store,
		Writer:     repo,
		Projection: store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
		Actor:      actor,
	}
	result, err := service.CreateMutation(ctx, core.CreateInput{Title: title, Status: core.Status(status)})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	return result.Task.ID
}

// writeConfigOperations records a batch of configuration operations directly,
// for a state no single command produces.
func writeConfigOperations(t *testing.T, repository string, operations []core.ConfigOperation) {
	t.Helper()
	writeConfigCommits(t, repository, [][]core.ConfigOperation{operations})
}

// writeConfigCommits records one commit per batch through one opened
// repository, for a test that needs a ledger longer than it needs commands.
func writeConfigCommits(t *testing.T, repository string, packs [][]core.ConfigOperation) {
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
	for _, operations := range packs {
		if _, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{}, operations, ""); err != nil {
			t.Fatalf("WriteConfigOperation() error = %v", err)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStatusDocuments(left, right statusListDocument) bool {
	if left.Default != right.Default || len(left.Statuses) != len(right.Statuses) {
		return false
	}
	for index := range left.Statuses {
		first, second := left.Statuses[index], right.Statuses[index]
		if first.Status != second.Status || first.Label != second.Label || first.Order != second.Order {
			return false
		}
		if !equalStrings(first.Tags, second.Tags) {
			return false
		}
	}
	return true
}
