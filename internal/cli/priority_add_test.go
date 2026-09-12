package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The decoded mutation shapes live here rather than in priority.go's own
// vocabulary, for the reason status_test.go's do: a change to an envelope
// member has to be made twice, once where it is produced and once where a
// caller reads it.
//
// They are named for this trio of verbs — add, rename and label, the three that
// name a priority — rather than for the whole family, because the eight verb
// files are written in parallel in one package and a generically named helper
// would be declared twice.
type priorityNamingChange struct {
	Operation    string            `json:"operation"`
	Priority     string            `json:"priority"`
	From         string            `json:"from"`
	Into         string            `json:"into"`
	Position     *positionDocument `json:"position"`
	Label        *labelDocument    `json:"label"`
	LabelDerived *bool             `json:"labelDerived"`
	Tags         []string          `json:"tags"`
	DefaultFrom  string            `json:"defaultFrom"`
}

type priorityNamingMutation struct {
	Change     priorityNamingChange       `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
	Tasks      struct {
		Affected int `json:"affected"`
	} `json:"tasks"`
	Inverse inverseDocument `json:"inverse"`
	Docs    *docsDocument   `json:"docs"`
}

func cliPriorityNamingMutation(
	t *testing.T,
	repository, command string,
	args ...string,
) priorityNamingMutation {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityNamingMutation
	if err := json.Unmarshal(assertJSONResult(t, stdout, command).Data, &document); err != nil {
		t.Fatalf("decode %s result: %v; output = %s", command, err, stdout)
	}
	return document
}

// cliCreateTaskAtPriority creates a task filed under a priority the caller
// names, which is what makes a project's tasks worth re-reading after its
// priorities change.
func cliCreateTaskAtPriority(t *testing.T, repository, title, priority string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--priority", priority, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create %s = code %d, stderr %q", title, code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

// priorityTaskCount reads how many tasks `priority list` files under one
// priority, and fails rather than reporting zero for a priority the project
// does not define.
func priorityTaskCount(t *testing.T, document priorityListDocument, priority string) int {
	t.Helper()
	for _, row := range document.Priorities {
		if row.Priority != priority {
			continue
		}
		if row.Tasks == nil {
			t.Fatalf("priority %s reports no task count", priority)
		}
		return *row.Tasks
	}
	t.Fatalf("priority %s is not in %#v", priority, document.Priorities)
	return 0
}

// An added priority lands where --before says, takes the label its name
// implies, and leaves every other priority where it was.
func TestPriorityAddPlacesThePriorityAndDerivesItsLabel(t *testing.T) {
	repository := initializedRepository(t)

	added := cliPriorityNamingMutation(t, repository, "priority add",
		"priority", "add", "urgent", "--before", "high", "--no-sync", "--json")
	if added.Change.Operation != "add" || added.Change.Priority != "urgent" {
		t.Fatalf("add change = %#v", added.Change)
	}
	if added.Change.Position == nil || added.Change.Position.Before != "high" ||
		added.Change.Position.Order != 1 || added.Change.Position.Rank == "" {
		t.Fatalf("add position = %#v, want first, before high", added.Change.Position)
	}
	if added.Change.Label == nil || added.Change.Label.To != "Urgent" || added.Change.Label.From != "" {
		t.Fatalf("add label = %#v, want the derived Urgent", added.Change.Label)
	}
	if added.Change.LabelDerived != nil {
		t.Fatalf("add labelDerived = %#v, want it omitted; the rule is a rename's", added.Change.LabelDerived)
	}
	if len(added.Change.Tags) != 0 || added.Change.DefaultFrom != "" {
		t.Fatalf("add change = %#v, want no tags and no default handoff", added.Change)
	}
	if !added.Vocabulary.Seeded || added.Vocabulary.Head == "" {
		t.Fatalf("add vocabulary = %#v, want a seeded ledger with a head", added.Vocabulary)
	}
	if added.Vocabulary.Default != "medium" {
		t.Fatalf("default after add = %q, want medium untouched", added.Vocabulary.Default)
	}
	if got, want := cliPriorityNames(t, repository), []string{
		"urgent", "high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after add = %v, want %v", got, want)
	}

	// Appending is the default placement, and --label overrides the derived
	// label rather than being derived alongside it.
	appended := cliPriorityNamingMutation(t, repository, "priority add",
		"priority", "add", "someday", "--label", "Some Day", "--no-sync", "--json")
	if appended.Change.Position == nil || appended.Change.Position.Order != 5 ||
		appended.Change.Position.Before != "" || appended.Change.Position.After != "" {
		t.Fatalf("appended position = %#v, want last with no anchor", appended.Change.Position)
	}
	if appended.Change.Label == nil || appended.Change.Label.To != "Some Day" {
		t.Fatalf("appended label = %#v, want the label the caller chose", appended.Change.Label)
	}

	// --after is the mirror of --before, and both report the anchor they used.
	after := cliPriorityNamingMutation(t, repository, "priority add",
		"priority", "add", "normal", "--after", "high", "--no-sync", "--json")
	if after.Change.Position == nil || after.Change.Position.After != "high" ||
		after.Change.Position.Order != 3 {
		t.Fatalf("after position = %#v, want third, after high", after.Change.Position)
	}

	// The text surface reports the same change.
	text := initializedRepository(t)
	code, stdout, stderr := run(t, text, "priority", "add", "urgent", "--before", "high", "--no-sync")
	if code != 0 || stderr != "" {
		t.Fatalf("priority add = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"Priority:\tadd\turgent",
		"\tlabel:\tUrgent",
		"\tposition:\tbefore high (1 of 4)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("priority add text = %q, want %q", stdout, want)
		}
	}
}

// The test this whole stage was designed around. A project that has never
// configured its priorities has tasks filed under the built-in three, and the
// first `priority add` writes a priorities section that did not exist. If that
// section held only the added priority, every one of those tasks would be
// stranded at a value the project no longer defines. It does not: gitstore
// backfills the built-in three into the same commit, and this is that guarantee
// seen from the command line.
func TestPriorityAddLeavesEveryExistingTaskResolving(t *testing.T) {
	repository := preLedgerRepository(t)
	urgentWork := cliCreateTaskAtPriority(t, repository, "Ship the fix", "high")
	ordinaryWork := cliCreateTask(t, repository, "Write the notes")
	laterWork := cliCreateTaskAtPriority(t, repository, "Rename the thing", "low")

	added := cliPriorityNamingMutation(t, repository, "priority add",
		"priority", "add", "urgent", "--before", "high", "--no-sync", "--json")
	if !added.Vocabulary.Seeded {
		t.Fatalf("add vocabulary = %#v, want the project's first priority commit", added.Vocabulary)
	}

	// The built-in three are in the project's own vocabulary now, not merely
	// read as a fallback, and they kept their labels, their order and the
	// default tag.
	document := cliPriorityList(t, repository)
	if got, want := cliPriorityNames(t, repository), []string{
		"urgent", "high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the first add = %v, want %v", got, want)
	}
	if document.Default != "medium" {
		t.Fatalf("default after the first add = %q, want medium", document.Default)
	}

	// Nothing is stranded: no stored priority failed to resolve, and every task
	// is still counted under the priority it was filed at.
	if len(document.Unresolved) != 0 {
		t.Fatalf("unresolved after the first add = %#v, want every task still resolving", document.Unresolved)
	}
	for priority, want := range map[string]int{"urgent": 0, "high": 1, "medium": 1, "low": 1} {
		if got := priorityTaskCount(t, document, priority); got != want {
			t.Errorf("tasks at %s = %d, want %d", priority, got, want)
		}
	}

	// And each task reads back at the priority it was created with, through
	// `show` rather than only through the list's own census.
	for _, want := range []struct {
		task     core.Task
		priority core.Priority
	}{
		{urgentWork, core.PriorityHigh},
		{ordinaryWork, core.PriorityMedium},
		{laterWork, core.PriorityLow},
	} {
		task := showTask(t, repository, want.task.ID)
		if task.Priority != want.priority {
			t.Errorf("%s priority = %q, want %q", task.ID, task.Priority, want.priority)
		}
		if task.StoredPriority != "" {
			t.Errorf("%s stored priority = %q, want the stored value to still be the live one",
				task.ID, task.StoredPriority)
		}
	}

	// The board and the list read the same vocabulary, so a stranded task would
	// surface there too.
	if code, _, stderr := run(t, repository, "board"); code != 0 {
		t.Fatalf("board after the first add = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("validate after the first add = code %d, stderr %q", code, stderr)
	}
}

// The refusals, each naming what the caller can do about it.
func TestPriorityAddRefusesADuplicateAndTwoAnchors(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "priority", "add", "high", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority add high = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `this project already defines priority "high"`)

	code, _, stderr = run(t, repository,
		"priority", "add", "urgent", "--before", "high", "--after", "low", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority add with both anchors = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryInvocation,
		"priority add accepts --before or --after, not both")

	code, _, stderr = run(t, repository, "priority", "add", "urgent", "--before", "blocker", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority add before an unknown anchor = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		`no priority "blocker" in this project; the priorities are: high, medium, low`)

	// Nothing was recorded by any of the three.
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the refusals = %v, want %v", got, want)
	}
}
