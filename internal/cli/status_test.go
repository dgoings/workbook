package cli

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
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

// `workbook status` alone is a mistake with two likely causes, and each gets
// its own answer: a forgotten subcommand, and a caller who wanted a task's
// status and found a verb family.
func TestStatusWithoutASubcommandNamesTheSubcommandsOrTheTaskCommand(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// Arity is refused by core at the authoring boundary, and the CLI passes the
// message through untouched. Rewording it here would produce two answers to the
// same question, and this one already names the command that fixes the state.
func TestStatusArityRefusalsSurfaceCoreMessagesVerbatim(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A status change is synchronized on the same terms a task change is: the
// report says what it did, --no-sync says it deliberately did nothing, and the
// ledger reaches origin without a task ref to carry it.
func TestStatusChangesReportAndPublishSynchronization(t *testing.T) {
	t.Parallel()
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

// A chain with two kinds of hop in it has to be described by what happened, not
// by where it ended.
//
// Forwarding answers about one hop by contract, and pairing that hop's verb with
// the chain's final destination produced sentences describing a change nobody
// made: "renamed to backlog", for a value that was renamed to `sorting` and only
// reached `backlog` because somebody later removed that. Both surfaces say the
// hop, then the end of the chain, and they say it the same way.
func TestStatusChainsAreDescribedByWhatHappened(t *testing.T) {
	t.Parallel()
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

// Two sessions writing the ledger at once: the loser is refused with something
// it can act on, nothing half-written survives, and the retry the refusal asks
// for succeeds.
func TestStatusWritesUnderContentionAreRefusedAndRetryable(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
