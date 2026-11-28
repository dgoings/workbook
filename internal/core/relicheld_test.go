package core

import (
	"strings"
	"testing"
)

// relicMergeOp is a part.body.merge of other onto base.
func relicMergeOp(id, part, base, ancestor, other string) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationPartBodyMerge, Part: part,
		Body:  &RelicBodyChange{Format: "md", Bytes: 40, Base: base},
		Merge: &RelicBodyMerge{Ancestor: ancestor, Other: other, Method: RelicLineMergeMethod}}
}

// relicHeldShapes are the partly held packs of the held rule: the pack, the
// operations the other history holds, and whether it counts as held whole.
func relicHeldShapes() []struct {
	name string
	pack []RelicOperation
	held []string
	want bool
} {
	save, title := relicTestID(500), relicTestID(501)
	m1, m2, theirs := relicTestID(502), relicTestID(503), relicTestID(504)
	saveOp := relicSaveOp(save, relicDocPartA, relicDocPartA, 30)
	titleOp := RelicOperation{ID: title, Type: RelicOperationTitleSet, Title: "Retitled"}
	merge := relicMergeOp(m1, relicDocPartA, theirs, relicDocPartA, save)
	again := relicMergeOp(m2, relicDocPartA, relicTestID(505), save, m1)
	outside := relicMergeOp(m1, relicDocPartA, theirs, relicDocPartA, relicTestID(506))
	unrecorded := merge
	unrecorded.Merge = nil
	return []struct {
		name string
		pack []RelicOperation
		held []string
		want bool
	}{
		{"a merge of a held save", []RelicOperation{saveOp, merge}, []string{save}, true},
		{"a re-replayed merge of a held save", []RelicOperation{saveOp, merge, again}, []string{save}, true},
		{"a held title beside it", []RelicOperation{titleOp, saveOp, merge}, []string{title, save}, true},
		{"a title not held", []RelicOperation{titleOp, saveOp, merge}, []string{save}, false},
		{"the save not held", []RelicOperation{titleOp, saveOp, merge}, []string{title}, false},
		{"other outside the pack", []RelicOperation{saveOp, outside}, []string{save}, false},
		{"a merge without its record", []RelicOperation{saveOp, unrecorded}, []string{save}, false},
	}
}

func TestRelicPackHeldByCountsOnlyMergesOfAHeldSave(t *testing.T) {
	for _, shape := range relicHeldShapes() {
		held := map[string]bool{}
		for _, id := range shape.held {
			held[id] = true
		}
		pack := relicFoldPack(2, shape.pack...)
		if got := RelicPackHeldBy(pack, func(id string) bool { return held[id] }); got != shape.want {
			t.Errorf("%s: RelicPackHeldBy() = %v, want %v", shape.name, got, shape.want)
		}
	}
}

// Replay skips a partly held pack the held rule counts as held whole, and
// refuses every other one as before.
func TestPlanRelicReplayOfAPartlyHeldPack(t *testing.T) {
	parent := goldenRelicRecord(t)
	for _, shape := range relicHeldShapes() {
		held := map[string]RelicOperation{}
		for _, operation := range shape.pack {
			for _, id := range shape.held {
				if operation.ID == id {
					held[id] = operation
				}
			}
		}
		step, err := PlanRelicReplay(parent, relicFoldPack(2, shape.pack...), RelicReplayInput{Held: held})
		if shape.want {
			if err != nil || !step.Skip {
				t.Errorf("%s: step = %#v, %v; want a skip", shape.name, step, err)
			}
			continue
		}
		requireReplayRefused(t, shape.name, step, err)
		if err != nil && !strings.Contains(err.Error(), "a pack is written whole") {
			t.Errorf("%s: error %q, want the partly held refusal", shape.name, err)
		}
	}
}
