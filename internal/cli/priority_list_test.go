package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The decoded shapes are declared here rather than reused from production for
// the reason status_test.go's are: a change to an envelope member has to be
// made twice, once where it is produced and once where a caller reads it.
type priorityVocabularyDocument struct {
	Head       string `json:"head"`
	Seeded     bool   `json:"seeded"`
	Default    string `json:"default"`
	Priorities []struct {
		Priority string   `json:"priority"`
		Label    string   `json:"label"`
		Tags     []string `json:"tags"`
		Color    string   `json:"color"`
		Order    int      `json:"order"`
		Tasks    *int     `json:"tasks"`
	} `json:"priorities"`
}

type priorityListDocument struct {
	priorityVocabularyDocument
	Retired []struct {
		Priority  string `json:"priority"`
		Becomes   string `json:"becomes"`
		Operation string `json:"operation"`
		At        string `json:"at"`
	} `json:"retired"`
	Unresolved []struct {
		Priority string   `json:"priority"`
		Tasks    int      `json:"tasks"`
		TaskIDs  []string `json:"taskIds"`
	} `json:"unresolved"`
	Advisories []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"advisories"`
}

func cliPriorityList(t *testing.T, repository string) priorityListDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, "priority", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("priority list = code %d, stderr %q", code, stderr)
	}
	var document priorityListDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority list").Data, &document); err != nil {
		t.Fatalf("decode priority list: %v; output = %s", err, stdout)
	}
	return document
}

// cliPriorityNames is the project's priorities in board order, most urgent
// first, which is what most assertions about this command group are really
// about.
func cliPriorityNames(t *testing.T, repository string) []string {
	t.Helper()
	names := make([]string, 0, 4)
	for _, priority := range cliPriorityList(t, repository).Priorities {
		names = append(names, priority.Priority)
	}
	return names
}

// A project that has never configured its priorities reads the built-in three
// and says so. The flag is the whole difference between "this project chose
// these" and "nobody has chosen anything yet".
func TestPriorityListReadsTheBuiltInVocabularyOnALedgerlessProject(t *testing.T) {
	repository := preLedgerRepository(t)
	cliCreateTask(t, repository, "Alpha")
	cliCreateTask(t, repository, "Beta")

	document := cliPriorityList(t, repository)
	if document.Seeded || document.Head != "" {
		t.Fatalf("list = seeded %t, head %q; want an unseeded project", document.Seeded, document.Head)
	}
	if document.Default != "medium" {
		t.Fatalf("default = %q, want medium", document.Default)
	}
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want the built-in three %v", got, want)
	}
	first := document.Priorities[0]
	if first.Priority != "high" || first.Label != "High" || first.Order != 1 ||
		first.Tasks == nil || *first.Tasks != 0 {
		t.Fatalf("first priority = %#v, want high holding no tasks", first)
	}
	// Both tasks were created without a priority, so both land on the default.
	middle := document.Priorities[1]
	if middle.Priority != "medium" || middle.Label != "Medium" || middle.Order != 2 ||
		middle.Tasks == nil || *middle.Tasks != 2 {
		t.Fatalf("second priority = %#v, want medium holding both tasks", middle)
	}
	if len(document.Retired) != 0 || len(document.Unresolved) != 0 || len(document.Advisories) != 0 {
		t.Fatalf("list = %#v, want nothing retired, unresolved, or advised", document)
	}

	code, stdout, stderr := run(t, repository, "priority", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("priority list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"#  PRIORITY  LABEL   TAGS     COLOR  TASKS",
		"1  high      High             ",
		"2  medium    Medium  default         2",
		"3  low       Low              ",
		"No priority change is recorded, so these are the priorities Workbook reads for a project that has none of its own.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("priority list text = %q, want %q", stdout, want)
		}
	}
}

// A project minted by this build records the built-in three in its genesis
// rather than leaning on a fallback, so the same three come back seeded.
func TestPriorityListReportsTheMintedVocabularyOnAFreshProject(t *testing.T) {
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Alpha")

	document := cliPriorityList(t, repository)
	if !document.Seeded || document.Head == "" {
		t.Fatalf("list = seeded %t, head %q; want a project whose genesis was written", document.Seeded, document.Head)
	}
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want the built-in three %v", got, want)
	}
	if document.Default != "medium" {
		t.Fatalf("default = %q, want medium", document.Default)
	}

	code, stdout, stderr := run(t, repository, "priority", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("priority list = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "No priority change is recorded") {
		t.Errorf("priority list text = %q, want no unseeded note for a minted project", stdout)
	}
}

// A bare `workbook priority` names its verbs rather than failing silently, and
// the refusal is derived from the help schema so all nine are always listed.
func TestPriorityWithoutASubcommandNamesEveryVerb(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "priority")
	if code != 2 {
		t.Fatalf("priority = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("priority stdout = %q, want empty", stdout)
	}
	for _, want := range []string{
		"priority takes a subcommand",
		"list, add, rename, label, move, tag, delete, color, log",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("priority stderr = %q, want %q", stderr, want)
		}
	}
}

// `untag` is not one of this group's verbs, and is refused the way any other
// word would be.
//
// A priority carries exactly one role and exactly one priority must carry it,
// so taking the role away has no outcome the vocabulary permits: taking it from
// a priority that does not hold it is a typo, and taking it from the one that
// does would leave a new task nowhere to land. Shipping a verb whose every call
// refuses is worse than not shipping it, so the group has nine. The durable
// `priority.untag` operation is untouched — a peer or a later build can still
// author one, and this build still folds, validates and describes it.
func TestPriorityDoesNotAcceptUntag(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "priority", "untag", "medium", "--tag", "default")
	if code != 2 {
		t.Fatalf("priority untag = code %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("priority untag stdout = %q, want empty", stdout)
	}
	for _, want := range []string{
		`unknown priority command "untag"`,
		"the subcommands are list, add, rename, label, move, tag, delete, color, log",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("priority untag stderr = %q, want %q", stderr, want)
		}
	}

	// The help schema does not offer it either, so no rendered usage and no
	// generated document can advertise a verb the dispatch refuses.
	if _, known := commandMetadataFor([]string{"priority", "untag"}); known {
		t.Error("the priority help schema still declares untag")
	}
	for _, verb := range prioritySubcommands() {
		if verb == "untag" {
			t.Error("prioritySubcommands still lists untag")
		}
	}
}

// `workbook priority WB-01J...` is not a typo, it is a caller reaching for a
// task's priority and finding a verb family. Naming `workbook show` turns a
// dead end into the command they wanted.
func TestPriorityWithATaskReferenceNamesShow(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Alpha")
	code, _, stderr := run(t, repository, "priority", task.ID)
	if code != 2 {
		t.Fatalf("priority %s = code %d, want 2", task.ID, code)
	}
	if !strings.Contains(stderr, "to read a task use: workbook show "+task.ID) {
		t.Errorf("priority stderr = %q, want the show advice", stderr)
	}
}

// Every verb this group declares is dispatched.
//
// The second half feeds each mutating verb two placeholder arguments it cannot
// accept. What it asserts is not the refusal but its KIND: anything other than
// "unknown priority command" means the dispatch switch named the verb and the
// verb itself did the refusing. A verb dropped from the switch while its schema
// entry survives would still pass the --help half above, and this is what
// catches it.
func TestPriorityDispatchesEveryDeclaredVerb(t *testing.T) {
	repository := initializedRepository(t)
	for _, verb := range prioritySubcommands() {
		code, stdout, stderr := run(t, repository, "priority", verb, "--help")
		if code != 0 || !strings.Contains(stdout, "Usage: workbook priority "+verb) {
			t.Errorf("priority %s --help = code %d, stdout %q, stderr %q", verb, code, stdout, stderr)
		}
	}
	for _, verb := range []string{"add", "rename", "label", "move", "tag", "delete", "color"} {
		code, _, stderr := run(t, repository, "priority", verb, "placeholder", "placeholder")
		if code == 0 {
			t.Errorf("priority %s = code 0, want a refusal for two placeholder arguments", verb)
		}
		if strings.Contains(stderr, "unknown priority command") {
			t.Errorf("priority %s = %q, want the verb dispatched rather than unknown", verb, stderr)
		}
	}
}
