package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
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

// A project that has never configured its statuses reads the built-in six and
// says so. That flag is the whole difference between "this project chose these"
// and "nobody has chosen anything yet", and a consumer that cannot tell them
// apart cannot decide whether to offer the setup.
func TestStatusListReadsTheBuiltInVocabularyOnALedgerlessProject(t *testing.T) {
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Alpha")
	cliCreateTask(t, repository, "Beta")

	document := cliStatusList(t, repository)
	if document.Seeded || document.Head != "" {
		t.Fatalf("list = seeded %t, head %q; want an unseeded project", document.Seeded, document.Head)
	}
	if document.Default != "backlog" {
		t.Fatalf("default = %q, want backlog", document.Default)
	}
	if got := len(document.Statuses); got != 6 {
		t.Fatalf("statuses = %d, want the six built-ins", got)
	}
	first := document.Statuses[0]
	if first.Status != "backlog" || first.Label != "Backlog" || first.Order != 1 ||
		first.Tasks == nil || *first.Tasks != 2 {
		t.Fatalf("first status = %#v, want backlog holding both tasks", first)
	}
	if len(document.Retired) != 0 || len(document.Unresolved) != 0 || len(document.Advisories) != 0 {
		t.Fatalf("list = %#v, want nothing retired, unresolved, or advised", document)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"#  STATUS       LABEL        TAGS     TASKS",
		"1  backlog      Backlog      default  2",
		"6  done         Done         done     0",
		"No status change is recorded yet",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status list text = %q, want %q", stdout, want)
		}
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
		"backlog", "triage", "ready", "blocked", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after add = %v, want %v", got, want)
	}

	// Appending is the default placement, and it appends rather than landing
	// anywhere the ranks happen to allow.
	appended := cliStatusMutation(t, repository, "status add", "status", "add", "archived", "--json")
	if appended.Change.Position == nil || appended.Change.Position.Order != 8 ||
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

	mustRunStatus(t, repository, "status", "tag", "blocked", "--tag", "next")
	untag := cliStatusMutation(t, repository, "status untag", "status", "untag", "blocked", "next", "--json")
	if len(untag.Change.Tags) != 0 {
		t.Fatalf("untag tags = %#v, want an empty set", untag.Change.Tags)
	}

	remove := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "archived", "--into", "done", "--json")
	if remove.Change.Into != "done" || remove.Change.Status != "archived" {
		t.Fatalf("delete change = %#v", remove.Change)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"inbox", "backlog", "ready", "blocked", "in-progress", "in-review", "done",
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
	repository := initializedRepository(t)

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

	// Nothing was recorded by any of them.
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
			args: []string{"status", "delete", "blocked", "--into", "triage", "--json"},
			want: fmt.Sprintf(`no status "triage"; it was removed into "backlog" on %s`, today),
		},
		{
			name: "never defined",
			args: []string{"status", "tag", "shipped", "--tag", "done", "--json"},
			want: `no status "shipped" in this project; the statuses are: ` +
				"backlog, queued, blocked, in-progress, in-review, done",
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

	code, stdout, stderr := run(t, repository, "status", "delete", "blocked", "--json")
	if code != 2 {
		t.Fatalf("status delete without --into = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("status delete stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		"status delete requires --into <status>, naming where the removed status's tasks belong; "+
			"this project's statuses are: backlog, ready, blocked, in-progress, in-review, done")

	code, _, stderr = run(t, repository, "status", "delete", "blocked", "--into", "blocked", "--json")
	if code != 5 {
		t.Fatalf("status delete into itself = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`status delete cannot forward "blocked" into itself; name where its tasks belong`)

	if cliStatusList(t, repository).Seeded {
		t.Fatal("a refused removal seeded the configuration ledger")
	}
}

// What a removal costs is counted before it happens, in the terms the person
// running it is deciding about: how many tasks move, and how many of those
// become claimable where they land.
func TestStatusDeleteCountsAffectedAndClaimableTasks(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")

	first := cliCreateTask(t, repository, "Alpha")
	second := cliCreateTask(t, repository, "Beta")
	third := cliCreateTask(t, repository, "Gamma")
	blocker := cliCreateTask(t, repository, "Blocker")
	for _, id := range []string{first.ID, second.ID, third.ID} {
		mustRunStatus(t, repository, "update", id, "--status", "triage", "--no-sync")
	}
	mustRunStatus(t, repository, "depend", third.ID, blocker.ID, "--no-sync")

	// A destination that is not tagged next moves the tasks and makes none of
	// them claimable.
	parked := initializedRepositoryLike(t, repository)
	_ = parked

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "ready", "--json")
	if removal.Tasks.Affected != 3 {
		t.Fatalf("affected = %d, want the three tasks in triage", removal.Tasks.Affected)
	}
	if removal.Tasks.ClaimableAfter != 2 {
		t.Fatalf("claimableAfter = %d, want the two with no dependencies", removal.Tasks.ClaimableAfter)
	}

	// The tasks were not rewritten; they resolve into the destination.
	document := cliStatusList(t, repository)
	for _, status := range document.Statuses {
		if status.Status != "ready" {
			continue
		}
		if status.Tasks == nil || *status.Tasks != 3 {
			t.Fatalf("ready holds %#v tasks, want the three forwarded ones", status.Tasks)
		}
	}
	if len(document.Retired) != 1 || document.Retired[0].Status != "triage" ||
		document.Retired[0].Becomes != "ready" || document.Retired[0].Operation != "status.remove" {
		t.Fatalf("retired = %#v, want triage forwarded to ready", document.Retired)
	}
	if document.Retired[0].At == "" {
		t.Fatal("retired entry carries no date, want the ledger's wall time")
	}
}

// initializedRepositoryLike keeps a second project available for a comparison a
// test wants to make without disturbing the first.
func initializedRepositoryLike(t *testing.T, _ string) string {
	t.Helper()
	return initializedRepository(t)
}

// A destination outside `next` leaves nothing claimable, which is the other half
// of the count and the reason it is reported separately from the total.
func TestStatusDeleteReportsNoClaimableTasksForAParkedDestination(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	task := cliCreateTask(t, repository, "Alpha")
	mustRunStatus(t, repository, "update", task.ID, "--status", "triage", "--no-sync")

	removal := cliStatusMutation(t, repository, "status delete",
		"status", "delete", "triage", "--into", "blocked", "--json")
	if removal.Tasks.Affected != 1 || removal.Tasks.ClaimableAfter != 0 {
		t.Fatalf("tasks = %#v, want one affected and none claimable", removal.Tasks)
	}
}

// The inverse matrix, verified by running it. Every inverse marked exact has to
// return the vocabulary to the document the change found; the ones marked
// inexact say what they will not restore.
func TestStatusInverseMatrixRestoresWhatItClaims(t *testing.T) {
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
			name:    "rename",
			command: []string{"status", "rename", "ready", "queued", "--json"},
			verb:    "status rename",
			inverse: "workbook status rename queued ready --label Ready",
			exact:   true,
		},
		{
			name:    "rename with a custom label",
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
			inverse: "workbook status label ready Ready",
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
			// The last status carrying a tag cannot be untagged, so the
			// project has to carry a second one first; that is arity, and it
			// is refused by core rather than here.
			name:    "untag",
			setup:   [][]string{{"status", "tag", "blocked", "--tag", "next"}},
			command: []string{"status", "untag", "blocked", "next", "--json"},
			verb:    "status untag",
			inverse: "workbook status tag blocked --tag next",
			exact:   true,
		},
		{
			name:    "delete",
			setup:   [][]string{{"status", "add", "triage", "--after", "backlog", "--label", "Triage"}},
			command: []string{"status", "delete", "triage", "--into", "backlog", "--json"},
			verb:    "status delete",
			inverse: "workbook status add triage --after backlog --label Triage",
			note:    `tasks that resolved into "backlog" are not moved back`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := initializedRepository(t)
			for _, setup := range test.setup {
				mustRunStatus(t, repository, setup...)
			}
			before := cliStatusList(t, repository)

			result := cliStatusMutation(t, repository, test.verb, test.command...)
			if result.Inverse.Command != test.inverse {
				t.Fatalf("inverse = %q, want %q", result.Inverse.Command, test.inverse)
			}
			if result.Inverse.Exact != test.exact {
				t.Fatalf("inverse exact = %t, want %t", result.Inverse.Exact, test.exact)
			}
			if result.Inverse.Note != test.note {
				t.Fatalf("inverse note = %q, want %q", result.Inverse.Note, test.note)
			}

			mustRunStatus(t, repository, strings.Fields(strings.TrimPrefix(result.Inverse.Command, "workbook "))...)
			after := cliStatusList(t, repository)
			if !test.exact {
				return
			}
			if !equalStatusDocuments(before, after) {
				t.Fatalf("running an exact inverse left %#v, want the state it found %#v",
					after.Statuses, before.Statuses)
			}
		})
	}
}

// The log is the same window `show --history` renders, over a different
// history: oldest first, the ten most recent by default, and every entry
// carrying the command that reverses it.
func TestStatusLogMirrorsShowHistoryWindowing(t *testing.T) {
	repository := initializedRepository(t)
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
}

// A rename writes two operations in one commit, and each is its own entry with
// its own inverse. The relabel's inverse has to name the status by the value it
// carries now while reading the label from before the whole commit.
func TestStatusLogSeparatesTheOperationsOfOneCommit(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")

	document := cliStatusLog(t, repository, "--all")
	if document.Total != 3 {
		t.Fatalf("total = %d, want the genesis, the rename and its relabel", document.Total)
	}
	rename, relabel := document.Entries[1], document.Entries[2]
	if rename.Commit != relabel.Commit {
		t.Fatalf("rename and relabel commits = %q, %q; want one commit", rename.Commit, relabel.Commit)
	}
	if rename.OperationID == relabel.OperationID {
		t.Fatalf("both entries carry operation ID %q, want one each", rename.OperationID)
	}
	if rename.Inverse == nil || rename.Inverse.Command != "workbook status rename queued ready" {
		t.Fatalf("rename inverse = %#v", rename.Inverse)
	}
	if relabel.Inverse == nil || relabel.Inverse.Command != "workbook status label queued Ready" {
		t.Fatalf("relabel inverse = %#v, want the label from before the commit", relabel.Inverse)
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
			result.Warnings[0].Message != `no status "ready" in this project's vocabulary; it was renamed to "queued", and that is what was listed` {
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
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{}, operations, ""); err != nil {
		t.Fatalf("WriteConfigOperation() error = %v", err)
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
