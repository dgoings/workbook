package core

import (
	"testing"
)

// The derived-label rule is only usable as a rule if it reproduces every label
// Workbook ships. A rename asks "is this label still the one the old name
// implied?", and an answer of no for a built-in status would make every rename
// of an untouched project keep a label nobody chose.
func TestDerivedStatusLabelReproducesEveryBuiltInLabel(t *testing.T) {
	for _, definition := range builtInStatusDefinitions() {
		if got := DerivedStatusLabel(definition.Status); got != definition.Label {
			t.Errorf("DerivedStatusLabel(%q) = %q, want the shipped label %q",
				definition.Status, got, definition.Label)
		}
	}
	for status, want := range map[Status]string{
		"triage":          "Triage",
		"awaiting-review": "Awaiting Review",
		"wip-2":           "Wip 2",
		"2fa":             "2fa",
	} {
		if got := DerivedStatusLabel(status); got != want {
			t.Errorf("DerivedStatusLabel(%q) = %q, want %q", status, got, want)
		}
	}
}

func rankedVocabulary(t *testing.T, ranks ...string) Vocabulary {
	t.Helper()
	definitions := make([]StatusDefinition, 0, len(ranks))
	for index, rank := range ranks {
		definitions = append(definitions, StatusDefinition{
			Status: Status(string(rune('a' + index))),
			Label:  "Column",
			Rank:   rank,
			Tags:   []StatusTag{},
		})
	}
	definitions[0].Tags = []StatusTag{StatusTagDefault, StatusTagNext, StatusTagDone}
	vocabulary, err := NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// Appending and inserting are the two placements the status verbs need, and
// both have to leave every other status where it was: a reorder that renumbered
// its peers would make two clones inserting concurrently disagree about
// everything rather than about one position.
func TestVocabularyRanksPlaceWithoutRenumbering(t *testing.T) {
	vocabulary := rankedVocabulary(t, "1/1", "2/1", "3/1")

	if got, want := vocabulary.AppendRank(), "4/1"; got != want {
		t.Fatalf("AppendRank() = %q, want %q", got, want)
	}

	between, err := vocabulary.InsertRank("", "b", true)
	if err != nil {
		t.Fatalf("InsertRank(before b) error = %v", err)
	}
	if between != "3/2" {
		t.Fatalf("InsertRank(before b) = %q, want the midpoint 3/2", between)
	}
	after, err := vocabulary.InsertRank("", "b", false)
	if err != nil {
		t.Fatalf("InsertRank(after b) error = %v", err)
	}
	if after != "5/2" {
		t.Fatalf("InsertRank(after b) = %q, want the midpoint 5/2", after)
	}
	first, err := vocabulary.InsertRank("", "a", true)
	if err != nil {
		t.Fatalf("InsertRank(before a) error = %v", err)
	}
	if first != "1/2" {
		t.Fatalf("InsertRank(before a) = %q, want half the first rank", first)
	}
	last, err := vocabulary.InsertRank("", "c", false)
	if err != nil {
		t.Fatalf("InsertRank(after c) error = %v", err)
	}
	if last != "4/1" {
		t.Fatalf("InsertRank(after c) = %q, want the next integer", last)
	}

	// A move measures the gap without counting itself, so moving the middle
	// status one place left lands between the two it is moving among rather
	// than between itself and its neighbour.
	moved, err := vocabulary.InsertRank("b", "a", true)
	if err != nil {
		t.Fatalf("InsertRank(move b before a) error = %v", err)
	}
	if moved != "1/2" {
		t.Fatalf("InsertRank(move b before a) = %q, want half the first rank", moved)
	}

	if _, err := vocabulary.InsertRank("", "missing", false); CategoryOf(err) != CategoryValidation {
		t.Fatalf("InsertRank(unknown anchor) error = %v, want a validation refusal", err)
	}
}

// Two statuses can share a rank — two clones inserting concurrently is enough —
// and the name tiebreak decides the order. An insertion into that exhausted gap
// is refused rather than silently landing somewhere else.
func TestVocabularyInsertRankRefusesAnExhaustedGap(t *testing.T) {
	vocabulary := rankedVocabulary(t, "1/1", "2/1", "2/1")

	if _, err := vocabulary.InsertRank("a", "c", true); CategoryOf(err) != CategoryValidation {
		t.Fatalf("InsertRank(into an equal-rank gap) error = %v, want a validation refusal", err)
	}
	// The same gap is representable for a name that already sorts between the
	// two, because rank ties break by name.
	shared, err := vocabulary.InsertRank("bb", "c", true)
	if err != nil {
		t.Fatalf("InsertRank(representable equal-rank gap) error = %v", err)
	}
	if shared != "2/1" {
		t.Fatalf("InsertRank(representable equal-rank gap) = %q, want the shared rank", shared)
	}
}

// Forwarding is what lets a message say "renamed to" rather than the vaguer
// "resolves to", and it answers about the first hop because that is what
// happened to the value somebody typed.
func TestVocabularyForwardingNamesTheHopAndItsKind(t *testing.T) {
	vocabulary := customVocabulary(t)

	destination, operation, forwarded := vocabulary.Forwarding("shipped")
	if !forwarded || destination != "released" || operation != ConfigStatusRename {
		t.Fatalf("Forwarding(shipped) = %q, %q, %t; want released renamed", destination, operation, forwarded)
	}
	destination, operation, forwarded = vocabulary.Forwarding("blocked")
	if !forwarded || destination != "triage" || operation != ConfigStatusRemove {
		t.Fatalf("Forwarding(blocked) = %q, %q, %t; want triage removed", destination, operation, forwarded)
	}
	if _, _, forwarded := vocabulary.Forwarding("triage"); forwarded {
		t.Fatal("Forwarding(triage) reported a live status as forwarded")
	}
	if _, _, forwarded := vocabulary.Forwarding("nonsense"); forwarded {
		t.Fatal("Forwarding(nonsense) reported an unknown status as forwarded")
	}
}

// The three authoring validators are exported so the status verbs can refuse a
// typed word as a typo. Reaching them only through the operation document would
// report a mistyped tag as corrupt data.
func TestExportedStatusValidatorsRefuseTypedValues(t *testing.T) {
	if err := ValidateStatusToken("Not A Token"); CategoryOf(err) != CategoryValidation {
		t.Fatalf("ValidateStatusToken() error = %v, want a validation refusal", err)
	}
	if err := ValidateStatusLabel("  "); CategoryOf(err) != CategoryValidation {
		t.Fatalf("ValidateStatusLabel() error = %v, want a validation refusal", err)
	}
	if err := ValidateStatusTag("urgent"); CategoryOf(err) != CategoryValidation {
		t.Fatalf("ValidateStatusTag() error = %v, want a validation refusal", err)
	}
	for _, tag := range StatusTags() {
		if err := ValidateStatusTag(tag); err != nil {
			t.Fatalf("ValidateStatusTag(%q) error = %v, want it accepted", tag, err)
		}
	}
	if got, want := len(StatusTags()), 3; got != want {
		t.Fatalf("StatusTags() = %d tags, want %d", got, want)
	}
}
