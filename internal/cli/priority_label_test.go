package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A label is what a person reads; the machine value is what a task stores.
// Changing the one changes nothing about the other, which is what makes
// relabelling the cheapest priority change there is.
func TestPriorityLabelChangesOnlyTheLabel(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTaskAtPriority(t, repository, "Ship the fix", "high")

	relabelled := cliPriorityNamingMutation(t, repository, "priority label",
		"priority", "label", "high", "Drop Everything", "--no-sync", "--json")
	if relabelled.Change.Operation != "label" || relabelled.Change.Priority != "high" {
		t.Fatalf("label change = %#v", relabelled.Change)
	}
	if relabelled.Change.Label == nil || relabelled.Change.Label.From != "High" ||
		relabelled.Change.Label.To != "Drop Everything" {
		t.Fatalf("label change = %#v, want High → Drop Everything", relabelled.Change.Label)
	}
	if relabelled.Change.Position != nil || relabelled.Change.From != "" {
		t.Fatalf("label change = %#v, want no position and no rename", relabelled.Change)
	}
	if relabelled.Change.LabelDerived != nil {
		t.Fatalf("label labelDerived = %#v, want it omitted; the rule is a rename's",
			relabelled.Change.LabelDerived)
	}
	if relabelled.Inverse.Command != "workbook priority label high High" || !relabelled.Inverse.Exact {
		t.Fatalf("label inverse = %#v, want the exact label back", relabelled.Inverse)
	}

	// The machine value, the order, the default and the task are all where they
	// were.
	document := cliPriorityList(t, repository)
	if got, want := cliPriorityNames(t, repository), []string{
		"high", "medium", "low",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after label = %v, want %v", got, want)
	}
	if document.Default != "medium" {
		t.Fatalf("default after label = %q, want medium", document.Default)
	}
	if document.Priorities[0].Label != "Drop Everything" {
		t.Fatalf("first priority = %#v, want the new label", document.Priorities[0])
	}
	if len(document.Retired) != 0 || len(document.Unresolved) != 0 {
		t.Fatalf("list after label = %#v, want nothing retired or unresolved", document)
	}
	if got := priorityTaskCount(t, document, "high"); got != 1 {
		t.Fatalf("tasks at high = %d, want the one task still filed there", got)
	}
	shown := showTask(t, repository, task.ID)
	if shown.Priority != "high" || shown.StoredPriority != "" {
		t.Fatalf("task priority = %q (stored %q), want an untouched high", shown.Priority, shown.StoredPriority)
	}

	// A label a person chose is theirs, so the next rename keeps it rather than
	// re-deriving one from the new name.
	renamed := cliPriorityNamingMutation(t, repository, "priority rename",
		"priority", "rename", "high", "critical", "--no-sync", "--json")
	if renamed.Change.Label == nil || renamed.Change.Label.To != "Drop Everything" {
		t.Fatalf("renamed label = %#v, want the chosen label kept", renamed.Change.Label)
	}
	if renamed.Change.LabelDerived == nil || *renamed.Change.LabelDerived {
		t.Fatalf("renamed labelDerived = %#v, want false", renamed.Change.LabelDerived)
	}

	// The text surface reports the move, with no "(derived)" or "(kept)" note,
	// because neither rule is what changed this label.
	code, stdout, stderr := run(t, repository, "priority", "label", "low", "Whenever", "--no-sync")
	if code != 0 || stderr != "" {
		t.Fatalf("priority label = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"Priority:\tlabel\tlow", "\tlabel:\tLow → Whenever"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("priority label text = %q, want %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "(derived)") || strings.Contains(stdout, "(kept)") {
		t.Errorf("priority label text = %q, want no derived-label note", stdout)
	}
}

// The refusals: a priority this project does not have, and the label it already
// carries.
func TestPriorityLabelRefusesUnknownPrioritiesAndTheLabelItAlreadyHas(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "priority", "label", "blocker", "Blocker", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority label of an unknown priority = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryNotFound,
		`no priority "blocker" in this project; the priorities are: high, medium, low`)

	code, _, stderr = run(t, repository, "priority", "label", "high", "High", "--no-sync", "--json")
	if code == 0 {
		t.Fatalf("priority label with the label it already has = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "high" already has that label`)

	// Nothing was recorded by either.
	document := cliPriorityList(t, repository)
	if document.Seeded && document.Priorities[0].Label != "High" {
		t.Fatalf("first priority = %#v, want an untouched High", document.Priorities[0])
	}
}
