package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// priorityColorMutationDocument decodes a `priority color` envelope's data
// member. Declared here rather than reused from production for the reason
// priorityListDocument's comment gives in priority_list_test.go: a change to
// what the envelope carries has to be made twice, once where it is produced
// and once where a caller reads it.
type priorityColorMutationDocument struct {
	Change struct {
		Operation string `json:"operation"`
		Priority  string `json:"priority"`
		Color     *struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"color"`
	} `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
}

func cliPriorityColorMutation(t *testing.T, repository string, args ...string) priorityColorMutationDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityColorMutationDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority color").Data, &document); err != nil {
		t.Fatalf("decode priority color result: %v; output = %s", err, stdout)
	}
	return document
}

// priorityColorOf reads the stored color for one priority from `priority
// list`, so a refusal can be checked against the ledger rather than against
// the refused command's own say-so.
func priorityColorOf(t *testing.T, repository string, priority string) string {
	t.Helper()
	for _, entry := range cliPriorityList(t, repository).Priorities {
		if entry.Priority == priority {
			return entry.Color
		}
	}
	t.Fatalf("no priority %q in %v", priority, cliPriorityList(t, repository))
	return ""
}

func TestPriorityColorSetsAndClearsTheStoredInk(t *testing.T) {
	repository := initializedRepository(t)

	set := cliPriorityColorMutation(t, repository, "priority", "color", "high", "#b42318", "--json")
	if set.Change.Operation != "color" || set.Change.Priority != "high" {
		t.Fatalf("color change = %#v", set.Change)
	}
	if set.Change.Color == nil || set.Change.Color.From != "" || set.Change.Color.To != "#b42318" {
		t.Fatalf("color change.color = %#v, want empty from, #b42318 to", set.Change.Color)
	}
	if got := priorityColorOf(t, repository, "high"); got != "#b42318" {
		t.Fatalf("stored color = %q, want #b42318", got)
	}

	cleared := cliPriorityColorMutation(t, repository, "priority", "color", "high", "--json")
	if cleared.Change.Color == nil || cleared.Change.Color.From != "#b42318" || cleared.Change.Color.To != "" {
		t.Fatalf("cleared change.color = %#v, want #b42318 -> empty", cleared.Change.Color)
	}
	if got := priorityColorOf(t, repository, "high"); got != "" {
		t.Fatalf("stored color after clear = %q, want empty (derived)", got)
	}

	// The text surface names the field, the way status.go's mutations do.
	code, stdout, stderr := run(t, repository, "priority", "color", "high", "#0f62fe")
	if code != 0 || stderr != "" {
		t.Fatalf("priority color = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tcolor:\t#0f62fe\n") {
		t.Fatalf("color text = %q, want the color line", stdout)
	}
}

// An uppercase value is normalized to lowercase before it is stored, not
// refused. Confirmed against core.ValidateThemeColor in internal/core/limits.go
// (it lowercases the value it accepts) and against the canonical-storage check
// in internal/core/priority.go's normalizePriorityDocument (definition.Color
// must already equal ValidateThemeColor's return, or the checkpoint is corrupt
// data) and the identical check on priority.recolor's own Value in
// internal/core/configop.go.
func TestPriorityColorNormalizesAnUppercaseValue(t *testing.T) {
	repository := initializedRepository(t)

	mutation := cliPriorityColorMutation(t, repository, "priority", "color", "medium", "#B42318", "--json")
	if mutation.Change.Color == nil || mutation.Change.Color.To != "#b42318" {
		t.Fatalf("normalized color = %#v, want #b42318", mutation.Change.Color)
	}
	if got := priorityColorOf(t, repository, "medium"); got != "#b42318" {
		t.Fatalf("stored color = %q, want lowercase #b42318", got)
	}
}

// A malformed value is refused before anything is written: the ledger has to
// still read as though the command never ran.
func TestPriorityColorRefusesAMalformedValueBeforeWritingAnything(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository)

	code, stdout, stderr := run(t, repository, "priority", "color", "high", "crimson")
	if code == 0 {
		t.Fatalf("priority color crimson = code 0, want a refusal")
	}
	if stdout != "" {
		t.Fatalf("priority color crimson stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "hexadecimal") {
		t.Fatalf("priority color crimson stderr = %q, want it to name the color rule", stderr)
	}

	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused color", before.Head, after.Head)
	}
	if got := priorityColorOf(t, repository, "high"); got != "" {
		t.Fatalf("stored color after refusal = %q, want untouched", got)
	}
}

// The same refusal applies to the JSON surface, and still writes nothing.
func TestPriorityColorRefusesAMalformedValueInJSONMode(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository)

	code, stdout, stderr := run(t, repository, "priority", "color", "high", "#zzzzzz", "--json")
	if code == 0 {
		t.Fatalf("priority color #zzzzzz --json = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	if stdout != "" {
		t.Fatalf("priority color #zzzzzz --json stdout = %q, want nothing written", stdout)
	}

	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused color", before.Head, after.Head)
	}
}
