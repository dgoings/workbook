package core

import (
	"encoding/json"
	"testing"
)

// The built-in set is today's three, most urgent first, with medium carrying
// the default tag the service used to hardcode.
func TestBuiltInPrioritiesAreTodaysThree(t *testing.T) {
	definitions := builtInPriorityDefinitions()

	var names []Priority
	for _, definition := range definitions {
		names = append(names, definition.Priority)
	}
	want := []Priority{PriorityHigh, PriorityMedium, PriorityLow}
	if len(names) != len(want) {
		t.Fatalf("built-in priorities = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("built-in priorities = %v, want %v (most urgent first)", names, want)
		}
	}
	for _, definition := range definitions {
		if definition.Priority == PriorityMedium && !definition.HasTag(PriorityTagDefault) {
			t.Error("medium does not carry the default tag, so a new task has no priority to land on")
		}
	}
}

// Color is omitted when unset, so a definition that chose no color encodes to
// the same bytes it would have before the field existed.
func TestPriorityDefinitionOmitsAnUnsetColor(t *testing.T) {
	encoded, err := json.Marshal(PriorityDefinition{Priority: PriorityHigh, Label: "High", Rank: "1/1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); got != `{"priority":"high","label":"High","rank":"1/1","tags":null}` {
		t.Errorf("encoded = %s; an unset color must not appear", got)
	}
}
