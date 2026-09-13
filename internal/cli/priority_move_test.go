package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// priorityMoveDocument is `workbook priority move --json`, decoded to what this
// file asserts about. It is declared here rather than reused from production
// for the reason priority_list_test.go's shapes are: a change to an envelope
// member has to be made twice, once where it is produced and once where a
// caller reads it.
type priorityMoveDocument struct {
	Change struct {
		Operation string   `json:"operation"`
		Priority  string   `json:"priority"`
		Tags      []string `json:"tags"`
		Label     *struct {
			To string `json:"to"`
		} `json:"label"`
		Position *struct {
			Before string `json:"before"`
			After  string `json:"after"`
			Rank   string `json:"rank"`
			Order  int    `json:"order"`
		} `json:"position"`
	} `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
	Inverse    struct {
		Command string `json:"command"`
		Exact   bool   `json:"exact"`
	} `json:"inverse"`
}

func cliPriorityMove(t *testing.T, repository string, args ...string) priorityMoveDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append([]string{"priority", "move"}, args...)...)
	if code != 0 || stderr != "" {
		t.Fatalf("priority move %v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityMoveDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority move").Data, &document); err != nil {
		t.Fatalf("decode priority move: %v; output = %s", err, stdout)
	}
	return document
}

// cliNextTaskID is the task `workbook next` hands out, which is where a
// priority's position is observable from outside this command group: among the
// claimable tasks, Next prefers the one at the most urgent priority.
func cliNextTaskID(t *testing.T, repository string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "next", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("next = code %d, stderr %q", code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "next").Data, &task); err != nil {
		t.Fatalf("decode next: %v; output = %s", err, stdout)
	}
	return task.ID
}

// Moving a priority reorders the vocabulary, and everything that reads an order
// reads the new one: `workbook next` hands out the task at the priority that is
// now the most urgent, without either task being touched.
func TestPriorityMoveReordersAndTheBoardSortFollows(t *testing.T) {
	repository := initializedRepository(t)
	least := createOrderingTask(t, repository, "Least urgent work", "low")
	most := createOrderingTask(t, repository, "Most urgent work", "high")
	if got := cliNextTaskID(t, repository); got != most.ID {
		t.Fatalf("next = %q, want the task at high %q", got, most.ID)
	}

	document := cliPriorityMove(t, repository, "low", "--before", "high", "--no-sync", "--json")
	if document.Change.Operation != "move" || document.Change.Priority != "low" {
		t.Fatalf("change = %#v, want a move of low", document.Change)
	}
	position := document.Change.Position
	if position == nil || position.Before != "high" || position.After != "" {
		t.Fatalf("position = %#v, want it placed before high", position)
	}
	if position.Rank == "" || position.Order != 1 {
		t.Fatalf("position = %#v, want a rank and the first place", position)
	}
	if document.Change.Label == nil || document.Change.Label.To != "Low" {
		t.Fatalf("label = %#v, want the label a move leaves alone", document.Change.Label)
	}
	if len(document.Change.Tags) != 0 {
		t.Fatalf("tags = %#v, want the empty set low carries", document.Change.Tags)
	}
	if document.Vocabulary.Default != "medium" {
		t.Fatalf("default = %q, want medium; a move gives no tag away", document.Vocabulary.Default)
	}

	if got, want := cliPriorityNames(t, repository), []string{
		"low", "high", "medium",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	if got := cliNextTaskID(t, repository); got != least.ID {
		t.Fatalf("next = %q, want the task at low %q now that low is the most urgent", got, least.ID)
	}

	// The inverse names the neighbour the priority left, and running it puts
	// the vocabulary back exactly as it was.
	if got, want := document.Inverse.Command, "workbook priority move low --after medium"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if !document.Inverse.Exact {
		t.Fatal("inverse = not exact, want exact; one move undoes one move")
	}
	cliPriorityMove(t, repository, "low", "--after", "medium", "--no-sync", "--json")
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the inverse = %v, want %v", got, want)
	}
}

// A move says where it goes, and every way of failing to say it is refused
// before anything is recorded.
func TestPriorityMoveRefusesAnAnchorItCannotUse(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository).Head

	for _, test := range []struct {
		name     string
		args     []string
		code     int
		category core.Category
		message  string
	}{
		{
			name:     "neither anchor",
			args:     []string{"priority", "move", "low", "--json"},
			code:     2,
			category: core.CategoryInvocation,
			message:  "priority move requires exactly one of --before or --after",
		},
		{
			name:     "both anchors",
			args:     []string{"priority", "move", "low", "--before", "high", "--after", "medium", "--json"},
			code:     2,
			category: core.CategoryInvocation,
			message:  "priority move requires exactly one of --before or --after",
		},
		{
			name:     "itself",
			args:     []string{"priority", "move", "low", "--before", "low", "--json"},
			code:     5,
			category: core.CategoryValidation,
			message:  `cannot move priority "low" relative to itself`,
		},
		{
			name:     "unknown priority",
			args:     []string{"priority", "move", "urgent", "--before", "high", "--json"},
			code:     4,
			category: core.CategoryNotFound,
			message:  `no priority "urgent" in this project; the priorities are: high, medium, low`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != test.code {
				t.Fatalf("%v = code %d, want %d; stderr = %q", test.args, code, test.code, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", test.args, stdout)
			}
			assertJSONError(t, stderr, test.category, test.message)
		})
	}

	if after := cliPriorityList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused move", before, after)
	}
}

// A move to the position the priority already holds is refused, from both
// sides, and nothing is recorded.
//
// This is the refusal that matters most in this family. A priority write on a
// project that has never configured its priorities backfills the built-in
// three and records a ConfigPriorityReorder, which requires a generation-three
// reader — so a move that reorders nobody would buy a permanent compatibility
// marker with a change of nothing. docs/reference.md says it plainly: a command
// that changes nothing should not be what costs a team its compatibility.
func TestPriorityMoveRefusesThePositionThePriorityAlreadyHolds(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository).Head

	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "already directly after the anchor",
			args:    []string{"priority", "move", "medium", "--after", "high", "--no-sync", "--json"},
			message: `priority "medium" is already directly after "high"`,
		},
		{
			name:    "already directly before the anchor",
			args:    []string{"priority", "move", "medium", "--before", "low", "--no-sync", "--json"},
			message: `priority "medium" is already directly before "low"`,
		},
		{
			name:    "the first priority, already before the second",
			args:    []string{"priority", "move", "high", "--before", "medium", "--no-sync", "--json"},
			message: `priority "high" is already directly before "medium"`,
		},
		{
			name:    "the last priority, already after the one above it",
			args:    []string{"priority", "move", "low", "--after", "medium", "--no-sync", "--json"},
			message: `priority "low" is already directly after "medium"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 5 {
				t.Fatalf("%v = code %d, want 5; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want nothing written", test.args, stdout)
			}
			assertJSONError(t, stderr, core.CategoryValidation, test.message)
		})
	}

	if after := cliPriorityList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a move that would change nothing", before, after)
	}
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the refusals = %v, want %v", got, want)
	}
}

// The refusal is positional, so every move that does reorder somebody still
// goes through: to the front of the list, to the back of it, and between two
// priorities in the middle.
//
// The last of the three is the one a rank comparison would get wrong. A rank is
// a reduced rational chosen between whatever pair the priority lands between,
// so a move can produce a rank numerically different from the one the priority
// held while reordering nobody — it is the neighbours, not the arithmetic, that
// decide whether anything moved.
func TestPriorityMoveStillMovesAtBothEndsAndInTheMiddle(t *testing.T) {
	repository := initializedRepository(t)

	// To the back: high leaves the front and becomes the least urgent.
	cliPriorityMove(t, repository, "high", "--after", "low", "--no-sync", "--json")
	if got, want := cliPriorityNames(t, repository), []string{
		"medium", "low", "high",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after a move to the back = %v, want %v", got, want)
	}

	// To the front: high comes all the way back.
	cliPriorityMove(t, repository, "high", "--before", "medium", "--no-sync", "--json")
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after a move to the front = %v, want %v", got, want)
	}

	// Into the middle: low takes the place between the two it was below.
	cliPriorityMove(t, repository, "low", "--after", "high", "--no-sync", "--json")
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "low", "medium",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after a move into the middle = %v, want %v", got, want)
	}
}

// The harm, asserted where it actually lands: a project whose ledger predates
// the priorities section, asked for a move that moves nothing.
//
// legacyDisplayConfiguredRepository (see priority_color_test.go) forges the
// real shape — a genesis with no priorities section and a minReader of 2 — and
// the point of reading the stored marker rather than only the exit code is that
// a regression which refused on the surface but wrote anyway would still park
// every teammate on a build that can read generation three.
func TestPriorityMoveNoOpRefusalDoesNotBumpAnUnconfiguredProjectsLedger(t *testing.T) {
	repository := legacyDisplayConfiguredRepository(t)

	before := cliGitOutput(t, repository, "rev-parse", configLedgerRefName)
	beforeState := cliGitOutput(t, repository, "show", before+":state.json")
	if !strings.Contains(beforeState, `"minReader":2`) {
		t.Fatalf("forged ledger state.json = %s, want minReader 2 before the refusal", beforeState)
	}
	if strings.Contains(beforeState, "priorities") {
		t.Fatalf("forged ledger state.json = %s, want no priorities section before the refusal", beforeState)
	}

	// Nothing below stops at the first failure. A build that records the move
	// fails the exit-code check first, and the useful half of this test is what
	// comes after it: the stamp itself, printed, so a reader sees what the
	// no-op cost rather than only that it was allowed.
	code, stdout, stderr := run(t, repository, "priority", "move", "medium", "--after", "high", "--no-sync", "--json")
	if code == 0 {
		t.Errorf("priority move to the position it already holds = code 0, want a refusal")
	} else {
		assertJSONError(t, stderr, core.CategoryValidation, `priority "medium" is already directly after "high"`)
	}
	if stdout != "" {
		t.Errorf("priority move no-op stdout = %q, want nothing written", stdout)
	}

	after := cliGitOutput(t, repository, "rev-parse", configLedgerRefName)
	if after != before {
		t.Errorf("configuration ledger head moved from %q to %q on a refused no-op move", before, after)
	}
	afterState := cliGitOutput(t, repository, "show", after+":state.json")
	if !strings.Contains(afterState, `"minReader":2`) {
		t.Errorf("ledger state.json after the refusal = %s, want minReader still 2, not bumped to 3", afterState)
	}
	if strings.Contains(afterState, "priorities") {
		t.Errorf("ledger state.json after the refusal = %s, want no priorities section backfilled in", afterState)
	}
}
