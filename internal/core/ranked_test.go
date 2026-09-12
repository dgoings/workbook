package core

import (
	"fmt"
	"testing"
)

type testRanked struct {
	name string
	at   string
}

func (item testRanked) key() string  { return item.name }
func (item testRanked) rank() string { return item.at }

func rankedItems(pairs ...[2]string) []ranked[string] {
	items := make([]ranked[string], 0, len(pairs))
	for _, pair := range pairs {
		items = append(items, testRanked{name: pair[0], at: pair[1]})
	}
	return items
}

// A rational always has room between two neighbours, which is what lets two
// clones insert into the same gap without coordinating.
func TestInsertRankFindsRoomBetweenNeighbours(t *testing.T) {
	items := rankedItems([2]string{"a", "1/1"}, [2]string{"b", "2/1"})

	got, err := insertRank(items, "", "b", true, "status", "statuses")
	if err != nil {
		t.Fatalf("insertRank: %v", err)
	}
	if got != "3/2" {
		t.Errorf("insertRank between 1/1 and 2/1 = %q, want 3/2", got)
	}
}

func TestAppendRankFollowsTheHighest(t *testing.T) {
	if got := appendRank(rankedItems([2]string{"a", "1/1"}, [2]string{"b", "5/2"})); got != "7/2" {
		t.Errorf("appendRank = %q, want 7/2", got)
	}
}

func TestInsertRankRefusesAnUndefinedAnchor(t *testing.T) {
	if _, err := insertRank(rankedItems([2]string{"a", "1/1"}), "", "missing", true, "status", "statuses"); err == nil {
		t.Error("insertRank accepted an anchor the list does not define")
	}
}

// An undefined anchor and an unparseable rank are different failures — one is
// a caller mistake, the other is corrupt data — and they must keep different
// categories because a category maps to a CLI exit code. Collapsing them into
// one category is exactly the regression this test guards against.
func TestInsertRankCategorizesUndefinedAnchorAndCorruptRankDifferently(t *testing.T) {
	_, undefinedAnchorErr := insertRank(rankedItems([2]string{"a", "1/1"}), "", "missing", true, "status", "statuses")
	if CategoryOf(undefinedAnchorErr) != CategoryValidation {
		t.Errorf("insertRank(undefined anchor) category = %v, want CategoryValidation", CategoryOf(undefinedAnchorErr))
	}

	_, corruptRankErr := insertRank(rankedItems([2]string{"a", "not-a-rank"}), "", "a", true, "status", "statuses")
	if CategoryOf(corruptRankErr) != CategoryCorruptData {
		t.Errorf("insertRank(unparseable rank) category = %v, want CategoryCorruptData", CategoryOf(corruptRankErr))
	}
}

// A stored value that was renamed twice still reads into the live one.
func TestResolveForwardWalksAChain(t *testing.T) {
	forward := map[string]string{"old": "middle", "middle": "current"}
	live := func(name string) bool { return name == "current" }

	got, ok := resolveForward(forward, live, "old")
	if !ok || got != "current" {
		t.Errorf("resolveForward(old) = %q,%v; want current,true", got, ok)
	}
}

// A cycle must not hang, and must not claim a resolution.
func TestResolveForwardRefusesACycle(t *testing.T) {
	forward := map[string]string{"a": "b", "b": "a"}
	live := func(string) bool { return false }

	if got, ok := resolveForward(forward, live, "a"); ok {
		t.Errorf("resolveForward resolved a cycle to %q", got)
	}
}

func TestForwardTerminatesRejectsACycle(t *testing.T) {
	if err := forwardTerminates(map[string]string{"a": "b", "b": "a"}, "a", "status"); err == nil {
		t.Error("forwardTerminates accepted a cycle")
	}
}

// noValidation is a validate func that accepts every value, for the tests
// below that are about the sort and the self-forward check rather than about
// any particular field rule.
func noValidation(string) error { return nil }

// A canonical document sorts its forwardings by source, because the bytes are
// compared for equality by the sync path.
func TestNormalizeForwardingsSortsBySource(t *testing.T) {
	got, err := normalizeForwardings([]forwarding[string]{{"z", "a"}, {"b", "c"}}, noValidation, "priority", "alias")
	if err != nil {
		t.Fatalf("normalizeForwardings: %v", err)
	}
	if got[0].From != "b" || got[1].From != "z" {
		t.Errorf("normalizeForwardings did not sort by source: %+v", got)
	}
}

// noun and verb reproduce a caller's own self-forward wording; a duplicate
// source is deliberately not tested here — neither normalizeStatusAliases
// nor normalizeRetiredStatuses ever caught it, and normalizeVocabularyDocument
// still does downstream.
func TestNormalizeForwardingsRefusesASelfForward(t *testing.T) {
	_, err := normalizeForwardings([]forwarding[string]{{"a", "a"}}, noValidation, "priority", "alias")
	if err == nil {
		t.Fatal("normalizeForwardings accepted a source that forwards to itself")
	}
	if want := `priority "a" cannot alias itself`; err.Error() != want {
		t.Errorf("normalizeForwardings self-forward message = %q, want %q", err, want)
	}
}

// A malformed field in a later pair and a self-forward in an earlier one are
// both real problems in the same document; the earlier pair's problem is
// reported, because normalizeForwardings validates and self-checks one pair
// at a time rather than validating every pair before self-checking any of
// them.
func TestNormalizeForwardingsInterleavesValidationWithTheSelfForwardCheck(t *testing.T) {
	refuseB := func(value string) error {
		if value == "b" {
			return Errorf(CategoryValidation, "value %q is refused", value)
		}
		return nil
	}
	_, err := normalizeForwardings([]forwarding[string]{{"a", "a"}, {"b", "c"}}, refuseB, "priority", "alias")
	if want := `priority "a" cannot alias itself`; err == nil || err.Error() != want {
		t.Errorf("normalizeForwardings error = %v, want the earlier pair's self-forward: %q", err, want)
	}
}

// forwardingsGrew refuses a pack only when it is what pushes the list both
// over its ceiling and past what the parent already had.
func TestForwardingsGrewRefusesOverTheCeilingAndGrown(t *testing.T) {
	before := []forwarding[string]{{"a", "b"}}
	after := []forwarding[string]{{"a", "b"}, {"c", "d"}}
	if err := forwardingsGrew(before, after, 1, "priority", "alias", "old"); err == nil {
		t.Error("forwardingsGrew accepted a pack that pushed the list over its ceiling")
	}
}

// A project already over its ceiling — two clones each adding one
// concurrently is enough — must still be able to fold further packs that do
// not make the list any bigger; this is the only way back under it.
func TestForwardingsGrewAllowsOverTheCeilingWhenTheSizeDidNotGrow(t *testing.T) {
	before := []forwarding[string]{{"a", "b"}, {"c", "d"}}
	after := []forwarding[string]{{"a", "b"}, {"c", "d"}}
	if err := forwardingsGrew(before, after, 1, "priority", "alias", "old"); err != nil {
		t.Errorf("forwardingsGrew refused a same-size pack over the ceiling: %v", err)
	}
}

func TestForwardingsGrewAllowsAtOrUnderTheCeiling(t *testing.T) {
	before := []forwarding[string]{{"a", "b"}}
	after := []forwarding[string]{{"a", "b"}, {"c", "d"}}
	if err := forwardingsGrew(before, after, 2, "priority", "alias", "old"); err != nil {
		t.Errorf("forwardingsGrew refused a pack that only reached the ceiling: %v", err)
	}
}

// validateVocabularyGrowth's two forwarding-ceiling messages are quoted in
// this stage's own design notes as byte-identical to what the pre-extraction
// code produced. Nothing else pins the literal text, and message drift is
// exactly this task's risk, so this asserts both exactly rather than just
// checking that an error came back.
func TestValidateVocabularyGrowthMessagesAreByteIdenticalToTheOriginal(t *testing.T) {
	aliases := make([]StatusAlias, MaxStatusAliasCount+1)
	for index := range aliases {
		aliases[index] = StatusAlias{From: Status(fmt.Sprintf("a%d", index)), To: "todo"}
	}
	err := validateVocabularyGrowth(VocabularyDocument{}, VocabularyDocument{Aliases: aliases})
	wantAlias := fmt.Sprintf(
		"the project has recorded %d status renames and must not exceed %d; "+
			"nothing can drop a rename yet, because a clone that has not fetched it "+
			"still needs it to read tasks stored under the old name",
		MaxStatusAliasCount+1, MaxStatusAliasCount,
	)
	if err == nil || err.Error() != wantAlias {
		t.Errorf("alias growth message = %q, want %q", err, wantAlias)
	}

	retired := make([]RetiredStatus, MaxStatusRetiredCount+1)
	for index := range retired {
		retired[index] = RetiredStatus{Status: Status(fmt.Sprintf("r%d", index)), Destination: "todo"}
	}
	err = validateVocabularyGrowth(VocabularyDocument{}, VocabularyDocument{Retired: retired})
	wantRetired := fmt.Sprintf(
		"the project has recorded %d status removals and must not exceed %d; "+
			"nothing can drop a removal yet, because a clone that has not fetched it "+
			"still needs it to read tasks stored under the removed name",
		MaxStatusRetiredCount+1, MaxStatusRetiredCount,
	)
	if err == nil || err.Error() != wantRetired {
		t.Errorf("retired growth message = %q, want %q", err, wantRetired)
	}
}

// The golden fixtures each carry exactly one alias and one retirement (the
// review that caught this checked), so they never exercise the comparator
// that decides canonical order among several. This does, with both lists
// supplied scrambled.
func TestNormalizeVocabularyDocumentSortsAliasesAndRetiredByStatus(t *testing.T) {
	document := VocabularyDocument{
		Statuses: []StatusDefinition{
			{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext, StatusTagDone}},
		},
		Aliases: []StatusAlias{
			{From: "zeta", To: "todo"},
			{From: "alpha", To: "todo"},
		},
		Retired: []RetiredStatus{
			{Status: "yankee", Destination: "todo"},
			{Status: "bravo", Destination: "todo"},
		},
	}
	normalized, err := normalizeVocabularyDocument(document)
	if err != nil {
		t.Fatalf("normalizeVocabularyDocument: %v", err)
	}
	if len(normalized.Aliases) != 2 || normalized.Aliases[0].From != "alpha" || normalized.Aliases[1].From != "zeta" {
		t.Errorf("aliases not sorted by source: %+v", normalized.Aliases)
	}
	if len(normalized.Retired) != 2 || normalized.Retired[0].Status != "bravo" || normalized.Retired[1].Status != "yankee" {
		t.Errorf("retired not sorted by source: %+v", normalized.Retired)
	}
}
