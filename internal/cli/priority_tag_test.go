package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// priorityTagDocument is `workbook priority tag --json`, decoded to what this
// file asserts about.
type priorityTagDocument struct {
	Change struct {
		Operation   string   `json:"operation"`
		Priority    string   `json:"priority"`
		Tags        []string `json:"tags"`
		DefaultFrom string   `json:"defaultFrom"`
	} `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
	Inverse    struct {
		Command string `json:"command"`
		Exact   bool   `json:"exact"`
	} `json:"inverse"`
}

func cliPriorityTag(t *testing.T, repository string, args ...string) priorityTagDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append([]string{"priority", "tag"}, args...)...)
	if code != 0 || stderr != "" {
		t.Fatalf("priority tag %v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityTagDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority tag").Data, &document); err != nil {
		t.Fatalf("decode priority tag: %v; output = %s", err, stdout)
	}
	return document
}

// Giving the default tag takes it from whichever priority held it, and does so
// in one recorded operation: a priority carries one role, so there is no set to
// reconcile and the fold transfers the tag inside the single `priority.tag`.
func TestPriorityTagMovesTheDefaultInOneOperation(t *testing.T) {
	repository := initializedRepository(t)

	document := cliPriorityTag(t, repository, "high", "--tag", "default", "--no-sync", "--json")
	if document.Change.Operation != "tag" || document.Change.Priority != "high" {
		t.Fatalf("change = %#v, want a tag of high", document.Change)
	}
	if got, want := document.Change.Tags, []string{"default"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want the whole set after the change %v", got, want)
	}
	if document.Change.DefaultFrom != "medium" {
		t.Fatalf("defaultFrom = %q, want medium, which gave the tag up", document.Change.DefaultFrom)
	}
	if document.Vocabulary.Default != "high" {
		t.Fatalf("default = %q, want high", document.Vocabulary.Default)
	}

	// One priority carries it, and the one that gave it up carries nothing.
	list := cliPriorityList(t, repository)
	if list.Default != "high" {
		t.Fatalf("list default = %q, want high", list.Default)
	}
	if got := list.Priorities[0]; got.Priority != "high" || !reflect.DeepEqual(got.Tags, []string{"default"}) {
		t.Fatalf("first priority = %#v, want high carrying default", got)
	}
	if got := list.Priorities[1]; got.Priority != "medium" || len(got.Tags) != 0 {
		t.Fatalf("second priority = %#v, want medium carrying nothing", got)
	}

	// The transfer is one operation, not an untag followed by a tag: the log
	// counts the priority operations in the commit beyond the first, and there
	// are none.
	log := cliPriorityLog(t, repository)
	if log.Total != 1 || len(log.Entries) != 1 {
		t.Fatalf("log = %#v, want one recorded change", log)
	}
	entry := log.Entries[0]
	if entry.Operation != string(core.ConfigPriorityTag) || entry.Collapsed != 0 {
		t.Fatalf("entry = %#v, want one priority.tag and nothing collapsed with it", entry)
	}

	// The tag is what decides where a task with no priority named lands.
	if got := cliCreateTask(t, repository, "Untriaged").Priority; got != core.Priority("high") {
		t.Fatalf("created task priority = %q, want high, which now carries the default tag", got)
	}

	// The inverse gives the tag back to the priority that held it.
	if got, want := document.Inverse.Command, "workbook priority tag medium --tag default"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if !document.Inverse.Exact {
		t.Fatal("inverse = not exact, want exact; giving the tag back restores the whole change")
	}
}

// Every way of naming a role the command cannot give is refused, and the
// refusals name what exists rather than sending somebody to the help.
func TestPriorityTagRefusesARoleItCannotGive(t *testing.T) {
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
			name:     "no tag",
			args:     []string{"priority", "tag", "high", "--json"},
			code:     2,
			category: core.CategoryInvocation,
			message:  "priority tag requires --tag <tag>",
		},
		{
			name:     "unknown tag",
			args:     []string{"priority", "tag", "high", "--tag", "urgent", "--json"},
			code:     5,
			category: core.CategoryValidation,
			message:  `unsupported priority tag "urgent"; the tags are: default`,
		},
		{
			name:     "already carried",
			args:     []string{"priority", "tag", "medium", "--tag", "default", "--json"},
			code:     5,
			category: core.CategoryValidation,
			message:  `priority "medium" already carries the "default" tag`,
		},
		{
			name:     "unknown priority",
			args:     []string{"priority", "tag", "urgent", "--tag", "default", "--json"},
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
		t.Fatalf("configuration head moved from %q to %q on a refused tag", before, after)
	}
}
