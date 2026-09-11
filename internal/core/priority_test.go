package core

import (
	"encoding/json"
	"strings"
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

// The zero value means "this caller did not configure one", and reads as the
// built-in set — which is what lets every construction predating per-project
// priorities keep its behavior without being edited.
func TestZeroPriorityVocabularyReadsAsBuiltIn(t *testing.T) {
	var vocabulary PriorityVocabulary

	if !vocabulary.IsZero() {
		t.Fatal("the zero value does not report itself as unconfigured")
	}
	if got := vocabulary.Default(); got != PriorityMedium {
		t.Errorf("zero vocabulary Default() = %q, want medium", got)
	}
	if vocabulary.Order(PriorityHigh) >= vocabulary.Order(PriorityLow) {
		t.Error("high does not sort ahead of low in the built-in order")
	}
}

// A renamed priority keeps resolving, which is what stops a rename from
// rewriting task history.
func TestPriorityVocabularyResolvesARename(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary(
		[]PriorityDefinition{{Priority: "critical", Label: "Critical", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}}},
		[]PriorityAlias{{From: PriorityHigh, To: "critical"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	got, ok := vocabulary.Resolve(PriorityHigh)
	if !ok || got != "critical" {
		t.Errorf("Resolve(high) = %q,%v; want critical,true", got, ok)
	}
}

// Arity: exactly one default, at least one priority.
func TestPriorityVocabularyValidateRefusesTwoDefaults(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary([]PriorityDefinition{
		{Priority: "a", Label: "A", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}},
		{Priority: "b", Label: "B", Rank: "2/1", Tags: []PriorityTag{PriorityTagDefault}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	if err := vocabulary.Validate(); err == nil {
		t.Error("Validate accepted two default priorities")
	}
}

func TestPriorityVocabularyValidateRefusesAnEmptySet(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	if err := vocabulary.Validate(); err == nil {
		t.Error("Validate accepted a project with no priorities")
	}
}

// rankedPriorityVocabulary builds a vocabulary whose priorities are named
// a, b, c... at the given ranks, the priority-flavoured twin of
// vocabulary_authoring_test.go's rankedVocabulary.
func rankedPriorityVocabulary(t *testing.T, ranks ...string) PriorityVocabulary {
	t.Helper()
	definitions := make([]PriorityDefinition, 0, len(ranks))
	for index, rank := range ranks {
		definitions = append(definitions, PriorityDefinition{
			Priority: Priority(string(rune('a' + index))),
			Label:    "Column",
			Rank:     rank,
			Tags:     []PriorityTag{},
		})
	}
	definitions[0].Tags = []PriorityTag{PriorityTagDefault}
	vocabulary, err := NewPriorityVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// insertRank's exhausted-gap message is shared with Vocabulary.InsertRank
// (ranked.go), and it used to hardcode "statuses" regardless of which caller
// asked. This is the priority side of Ruling 9: the message must name
// priorities, not statuses, mirroring
// TestVocabularyInsertRankRefusesAnExhaustedGap's setup.
func TestPriorityVocabularyInsertRankRefusesAnExhaustedGapNamingPriorities(t *testing.T) {
	vocabulary := rankedPriorityVocabulary(t, "1/1", "2/1", "2/1")

	_, err := vocabulary.InsertRank("a", "c", true)
	if CategoryOf(err) != CategoryValidation {
		t.Fatalf("InsertRank(into an equal-rank gap) error = %v, want a validation refusal", err)
	}
	if !strings.Contains(err.Error(), "priorities") {
		t.Errorf("InsertRank error = %q, want it to name priorities", err)
	}
	if strings.Contains(err.Error(), "statuses") {
		t.Errorf("InsertRank error = %q, wrongly names statuses", err)
	}
}

// forwardTerminates's cycle message is shared with normalizeVocabularyDocument
// and used to hardcode "status" regardless of which caller asked. This is the
// priority side of Ruling 9, mirroring TestNewVocabularyRejectsAForwardingCycle's
// setup.
func TestNewPriorityVocabularyRejectsAForwardingCycleNamingPriorities(t *testing.T) {
	_, err := NewPriorityVocabulary(
		[]PriorityDefinition{{Priority: "todo", Label: "Todo", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}}},
		[]PriorityAlias{{From: "a", To: "b"}, {From: "b", To: "a"}},
		nil,
	)
	if err == nil {
		t.Fatal("NewPriorityVocabulary() error = nil, want a cycle rejection")
	}
	if !strings.Contains(err.Error(), "priority") {
		t.Errorf("cycle error = %q, want it to name a priority", err)
	}
	if strings.Contains(err.Error(), "status") {
		t.Errorf("cycle error = %q, wrongly names a status", err)
	}
}
