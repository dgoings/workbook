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

	got, err := insertRank(items, "", "b", true)
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
	if _, err := insertRank(rankedItems([2]string{"a", "1/1"}), "", "missing", true); err == nil {
		t.Error("insertRank accepted an anchor the list does not define")
	}
}
