package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// heldPackShapes are packs a replay can meet only partly held, by the IDs
// held: a description merge counts as held only through the edit it names,
// followed within its pack, and any other operation not held leaves the pack
// unheld.
var heldPackShapes = []struct {
	name    string
	pack    []core.Operation
	held    []string
	derived bool
}{
	{"merge of a held edit", []core.Operation{heldTestSet("S"), heldTestMerge("M", "S")}, []string{"S"}, true},
	{"title and merge of a held edit", []core.Operation{heldTestSet("T"), heldTestSet("S"), heldTestMerge("M", "S")}, []string{"T", "S"}, true},
	{"second merge of a held edit", []core.Operation{heldTestSet("S"), heldTestMerge("M1", "S"), heldTestMerge("M2", "M1")}, []string{"S"}, true},
	{"merge of a held merge", []core.Operation{heldTestSet("S"), heldTestMerge("M1", "S"), heldTestMerge("M2", "M1")}, []string{"S", "M1"}, true},
	{"title not held", []core.Operation{heldTestSet("T"), heldTestSet("S"), heldTestMerge("M", "S")}, []string{"S"}, false},
	{"edit not held", []core.Operation{heldTestSet("T"), heldTestSet("S"), heldTestMerge("M", "S")}, []string{"T"}, false},
	{"merge of an operation outside the pack", []core.Operation{heldTestSet("S"), heldTestMerge("M", "X")}, []string{"S", "X"}, false},
	{"merge without its record", []core.Operation{heldTestSet("S"), {ID: "M", Type: core.OperationDescriptionMerge}}, []string{"S"}, false},
}

func heldTestSet(id string) core.Operation {
	return core.Operation{ID: id, Type: core.OperationFieldSet, Field: "description", Value: id}
}

func heldTestMerge(id, other string) core.Operation {
	return core.Operation{ID: id, Type: core.OperationDescriptionMerge, Value: id, Merge: &core.DescriptionMerge{Other: other}}
}

func TestDerivedFromHeldCountsOnlyMergesOfAHeldEdit(t *testing.T) {
	t.Parallel()
	for _, shape := range heldPackShapes {
		held := operationAccount{}
		for _, id := range shape.held {
			held[id] = struct{}{}
		}
		if got := derivedFromHeld(core.OperationPack{Operations: shape.pack}, held.has); got != shape.derived {
			t.Errorf("%s: derivedFromHeld() = %v, want %v", shape.name, got, shape.derived)
		}
	}
}

// A replay skips a partly held pack whose other operations are merges of a
// held edit, and refuses every other partly held pack before writing
// anything.
func TestReplayOfAPartlyHeldPack(t *testing.T) {
	t.Parallel()
	for _, shape := range heldPackShapes {
		held := heldOperations{}
		held.recordIDs(shape.held)
		replay := taskReplay{taskID: "WB-1", held: held}
		local := core.Snapshot{Head: "local", Operation: core.OperationPack{Operations: shape.pack}}
		done, err := replay.next(context.Background(), nil, core.StateDocument{}, local)
		if shape.derived {
			if err != nil || done || replay.skipped != 1 || replay.replayed != 0 {
				t.Errorf("%s: next() = %v, %v, skipped %d, want the pack skipped as held", shape.name, done, err, replay.skipped)
			}
			continue
		}
		if core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(err.Error(), "would record an operation twice") {
			t.Errorf("%s: next() = %v, want the refusal to record an operation twice", shape.name, err)
		}
	}
}
