package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Untag takes the role away from the priority that carries it, and the project
// is never left without one.
//
// `default` is the only role a priority has today, and exactly one priority
// carries it, so taking it away from its holder is taking away the project's
// only default. That is refused at the authoring boundary with the message
// naming the command that fixes it — which is the same gate `status untag`
// meets when it takes the last `done` away, and is why neither verb pre-empts
// it: a peer's pack carrying the same operation still folds, because by then it
// is history.
func TestPriorityUntagWillNotLeaveTheProjectWithoutADefault(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository).Head

	code, stdout, stderr := run(t, repository, "priority", "untag", "medium", "--tag", "default", "--json")
	if code != 5 {
		t.Fatalf("priority untag medium --tag default = code %d, want 5; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("priority untag stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		"no priority is tagged default, so a new task would have no priority to land on; "+
			"tag one first: workbook priority tag <priority> --tag default")

	// Nothing was recorded, and the project still has its default.
	if after := cliPriorityList(t, repository).Head; after != before {
		t.Fatalf("configuration head moved from %q to %q on a refused untag", before, after)
	}
	list := cliPriorityList(t, repository)
	if list.Default != "medium" {
		t.Fatalf("default = %q, want medium", list.Default)
	}
	if got := list.Priorities[1]; !reflect.DeepEqual(got.Tags, []string{"default"}) {
		t.Fatalf("medium = %#v, want it still carrying default", got)
	}
}

// Untag takes one role, named by a flag. `status untag` takes its role
// positionally; the priority verbs take it as `--tag` so that `tag` and `untag`
// read as a pair, and a role typed positionally is refused rather than
// silently taken as something else.
func TestPriorityUntagRefusesARoleItCannotTake(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository).Head

	// The role typed where `status untag` would take it is a positional this
	// verb does not have, and it is refused rather than ignored.
	code, _, stderr := run(t, repository, "priority", "untag", "medium", "default")
	if code != 2 {
		t.Fatalf("priority untag medium default = code %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "priority accepts no additional positional arguments") {
		t.Fatalf("stderr = %q, want the leftover-positional refusal", stderr)
	}

	for _, test := range []struct {
		name     string
		args     []string
		code     int
		category core.Category
		message  string
	}{
		{
			name:     "no tag",
			args:     []string{"priority", "untag", "medium", "--json"},
			code:     2,
			category: core.CategoryInvocation,
			message:  "priority untag requires --tag <tag>",
		},
		{
			name:     "unknown tag",
			args:     []string{"priority", "untag", "medium", "--tag", "urgent", "--json"},
			code:     5,
			category: core.CategoryValidation,
			message:  `unsupported priority tag "urgent"; the tags are: default`,
		},
		{
			name:     "not carried",
			args:     []string{"priority", "untag", "high", "--tag", "default", "--json"},
			code:     5,
			category: core.CategoryValidation,
			message:  `priority "high" does not carry the "default" tag`,
		},
		{
			name:     "unknown priority",
			args:     []string{"priority", "untag", "urgent", "--tag", "default", "--json"},
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
		t.Fatalf("configuration head moved from %q to %q on a refused untag", before, after)
	}
}
