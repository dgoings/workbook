package cli

import (
	"encoding/json"
	"reflect"
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
