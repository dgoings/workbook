package core

import "testing"

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

	got, err := insertRank(items, "", "b", true, "status")
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
	if _, err := insertRank(rankedItems([2]string{"a", "1/1"}), "", "missing", true, "status"); err == nil {
		t.Error("insertRank accepted an anchor the list does not define")
	}
}

// An undefined anchor and an unparseable rank are different failures — one is
// a caller mistake, the other is corrupt data — and they must keep different
// categories because a category maps to a CLI exit code. Collapsing them into
// one category is exactly the regression this test guards against.
func TestInsertRankCategorizesUndefinedAnchorAndCorruptRankDifferently(t *testing.T) {
	_, undefinedAnchorErr := insertRank(rankedItems([2]string{"a", "1/1"}), "", "missing", true, "status")
	if CategoryOf(undefinedAnchorErr) != CategoryValidation {
		t.Errorf("insertRank(undefined anchor) category = %v, want CategoryValidation", CategoryOf(undefinedAnchorErr))
	}

	_, corruptRankErr := insertRank(rankedItems([2]string{"a", "not-a-rank"}), "", "a", true, "status")
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
	if err := forwardTerminates(map[string]string{"a": "b", "b": "a"}, "a"); err == nil {
		t.Error("forwardTerminates accepted a cycle")
	}
}
