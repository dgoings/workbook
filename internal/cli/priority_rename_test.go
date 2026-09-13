package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A rename never rewrites a task. The value on the ref stays what it was and
// resolves through the rename chain to the priority it now means, which is the
// whole reason renaming a priority is safe on a project with history.
func TestPriorityRenameLeavesTasksStoredUnderTheOldValueResolving(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTaskAtPriority(t, repository, "Ship the fix", "high")

	renamed := cliPriorityNamingMutation(t, repository, "priority rename",
		"priority", "rename", "high", "critical", "--no-sync", "--json")
	if renamed.Change.Operation != "rename" ||
		renamed.Change.From != "high" || renamed.Change.Priority != "critical" {
		t.Fatalf("rename change = %#v", renamed.Change)
	}
	if renamed.Change.Label == nil || renamed.Change.Label.From != "High" ||
		renamed.Change.Label.To != "Critical" {
		t.Fatalf("rename label = %#v, want the label re-derived", renamed.Change.Label)
	}
	if renamed.Change.LabelDerived == nil || !*renamed.Change.LabelDerived {
		t.Fatalf("rename labelDerived = %#v, want true", renamed.Change.LabelDerived)
	}
	if renamed.Inverse.Command != "workbook priority rename critical high --label High" ||
		!renamed.Inverse.Exact {
		t.Fatalf("rename inverse = %#v, want the exact rename back", renamed.Inverse)
	}

	if got, want := cliPriorityNames(t, repository), []string{
		"critical", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after rename = %v, want %v", got, want)
	}

	// The task itself: stored as the old value, read as the new one.
	shown := showTask(t, repository, task.ID)
	if shown.Priority != "critical" {
		t.Fatalf("task priority = %q, want critical", shown.Priority)
	}
	if shown.StoredPriority != "high" {
		t.Fatalf("task stored priority = %q, want the untouched high", shown.StoredPriority)
	}

	document := cliPriorityList(t, repository)
	if len(document.Unresolved) != 0 {
		t.Fatalf("unresolved after rename = %#v, want every task still resolving", document.Unresolved)
	}
	if got := priorityTaskCount(t, document, "critical"); got != 1 {
		t.Fatalf("tasks at critical = %d, want the renamed priority's one task", got)
	}
	if len(document.Retired) != 1 || document.Retired[0].Priority != "high" ||
		document.Retired[0].Becomes != "critical" ||
		document.Retired[0].Operation != string(core.ConfigPriorityRename) {
		t.Fatalf("retired after rename = %#v, want high forwarding to critical", document.Retired)
	}
}

// The derived-label rule, both arms, plus the override. A label nobody chose
// follows the name it came from; a label somebody chose is theirs.
func TestPriorityRenameFollowsADerivedLabelAndKeepsAChosenOne(t *testing.T) {
	repository := initializedRepository(t)

	// --label wins over both arms of the rule.
	chosen := cliPriorityNamingMutation(t, repository, "priority rename",
		"priority", "rename", "medium", "normal", "--label", "Business As Usual", "--no-sync", "--json")
	if chosen.Change.Label == nil || chosen.Change.Label.To != "Business As Usual" {
		t.Fatalf("rename label = %#v, want the label the caller chose", chosen.Change.Label)
	}
	if chosen.Change.LabelDerived == nil || *chosen.Change.LabelDerived {
		t.Fatalf("rename labelDerived = %#v, want false for a chosen label", chosen.Change.LabelDerived)
	}
	// The default tag rides along with the priority rather than being dropped
	// by the rename.
	if chosen.Vocabulary.Default != "normal" {
		t.Fatalf("default after rename = %q, want normal", chosen.Vocabulary.Default)
	}
	if len(chosen.Change.Tags) != 1 || chosen.Change.Tags[0] != "default" {
		t.Fatalf("rename tags = %#v, want the default tag reported", chosen.Change.Tags)
	}

	// That chosen label now survives the next rename untouched, and the
	// envelope says which rule applied.
	kept := cliPriorityNamingMutation(t, repository, "priority rename",
		"priority", "rename", "normal", "ordinary", "--no-sync", "--json")
	if kept.Change.Label == nil || kept.Change.Label.To != "Business As Usual" ||
		kept.Change.Label.From != "Business As Usual" {
		t.Fatalf("kept label = %#v, want the custom label kept", kept.Change.Label)
	}
	if kept.Change.LabelDerived == nil || *kept.Change.LabelDerived {
		t.Fatalf("kept labelDerived = %#v, want false", kept.Change.LabelDerived)
	}
	// A rename that changed no label records the rename alone, so its inverse
	// names no label either.
	if kept.Inverse.Command != "workbook priority rename ordinary normal" {
		t.Fatalf("kept inverse = %q, want the bare rename back", kept.Inverse.Command)
	}

	// The text surface reports which rule moved the label.
	code, stdout, stderr := run(t, repository, "priority", "rename", "low", "someday", "--no-sync")
	if code != 0 || stderr != "" {
		t.Fatalf("priority rename = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"Priority:\trename\tsomeday",
		"\tfrom:\tlow",
		"\tlabel:\tLow → Someday (derived)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("priority rename text = %q, want %q", stdout, want)
		}
	}
}

// The refusals. A value this project never had, a value it already defines, and
// the no-op rename that would otherwise author an operation the document calls
// corrupt.
func TestPriorityRenameRefusesUnknownExistingAndUnchangedValues(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "priority", "rename", "blocker", "critical", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority rename of an unknown priority = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		`no priority "blocker" in this project; the priorities are: high, medium, low`)

	code, _, stderr = run(t, repository, "priority", "rename", "high", "low", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority rename onto an existing priority = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `this project already defines priority "low"`)

	code, _, stderr = run(t, repository, "priority", "rename", "high", "high", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority rename onto itself = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "high" already has that value`)

	// A renamed value stays typeable once, and names its replacement.
	if code, _, stderr := run(t, repository, "priority", "rename", "high", "critical", "--no-sync"); code != 0 {
		t.Fatalf("priority rename = code %d, stderr %q", code, stderr)
	}
	code, _, stderr = run(t, repository, "priority", "rename", "high", "urgent", "--no-sync")
	if code == 0 {
		t.Fatalf("priority rename of a forwarded value = code 0, want a refusal")
	}
	if !strings.Contains(stderr, `it was renamed to "critical"`) {
		t.Fatalf("forwarded refusal = %q, want it to name the replacement", stderr)
	}

	if got, want := cliPriorityNames(t, repository), []string{
		"critical", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the refusals = %v, want %v", got, want)
	}
}
