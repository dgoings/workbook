package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// relicFoldPack builds a pack against the golden relic at the given clock, one
// minute later per clock step.
func relicFoldPack(clock uint64, operations ...RelicOperation) RelicOperationPack {
	wall := relicDocTime.Add(time.Duration(clock-1) * time.Minute)
	return NewRelicOperationPack(relicDocProject, relicDocRelic, relicDocGeneration, relicDocActor, clock, wall, operations)
}

// reclock returns the pack moved to the given clock, with its wall time to
// match.
func reclock(pack RelicOperationPack, clock uint64) RelicOperationPack {
	return relicFoldPack(clock, pack.Operations...)
}

func goldenRelicRecord(t *testing.T) RelicRecord {
	t.Helper()
	record, err := ApplyRelic(nil, goldenRelicPack())
	if err != nil {
		t.Fatalf("ApplyRelic(golden root): %v", err)
	}
	return record
}

func mustApplyRelic(t *testing.T, parent RelicRecord, pack RelicOperationPack) RelicRecord {
	t.Helper()
	record, err := ApplyRelic(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyRelic(clock %d): %v", pack.LogicalClock, err)
	}
	return record
}

// withoutUpdatedAt is relic data with its update time cleared, for comparing
// folds whose last pack ran at different wall times.
func withoutUpdatedAt(data RelicData) RelicData {
	data.UpdatedAt = time.Time{}
	return data
}

func requireRelicEqual(t *testing.T, label string, got, want RelicData) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Errorf("%s:\n got %s\nwant %s", label, gotJSON, wantJSON)
	}
}

func requirePartForTest(t *testing.T, data RelicData, id string) RelicPart {
	t.Helper()
	part, ok := data.Part(id)
	if !ok {
		t.Fatalf("part %s is missing", id)
	}
	return part
}

func TestApplyRelicRootHistory(t *testing.T) {
	pack := goldenRelicPack()
	record, err := ApplyRelic(nil, pack)
	if err != nil {
		t.Fatalf("ApplyRelic: %v", err)
	}
	gotState, err := EncodeDocument(record.State)
	if err != nil {
		t.Fatalf("EncodeDocument(state): %v", err)
	}
	wantState, err := EncodeDocument(goldenRelicState())
	if err != nil {
		t.Fatalf("EncodeDocument(golden state): %v", err)
	}
	if string(gotState) != string(wantState) {
		t.Errorf("state:\n got %s\nwant %s", gotState, wantState)
	}
	if !reflect.DeepEqual(record.Links, EmptyRelicLinks()) {
		t.Errorf("links = %#v, want the empty links document", record.Links)
	}
	if err := ValidateRelicCheckpoint(nil, pack, record); err != nil {
		t.Errorf("ValidateRelicCheckpoint: %v", err)
	}

	clockTwo := goldenRelicPack()
	clockTwo.LogicalClock = 2
	_, err = ApplyRelic(nil, clockTwo)
	requireCorruptData(t, "root at clock 2", err)

	noCreate := relicFoldPack(1,
		RelicOperation{ID: relicTestID(100), Type: RelicOperationTitleSet, Title: "Untitled"})
	_, err = ApplyRelic(nil, noCreate)
	requireCorruptData(t, "root without relic.create", err)

	createSecond := goldenRelicPack()
	createSecond.Operations = append([]RelicOperation{createSecond.Operations[1]}, createSecond.Operations[0])
	createSecond.Operations = append(createSecond.Operations, goldenRelicPack().Operations[2:]...)
	_, err = ApplyRelic(nil, createSecond)
	requireCorruptData(t, "root with relic.create second", err)

	withTombstone := goldenRelicPack()
	withTombstone.Operations = append(withTombstone.Operations,
		RelicOperation{ID: relicTestID(101), Type: RelicOperationTombstone})
	_, err = ApplyRelic(nil, withTombstone)
	requireCorruptData(t, "root with relic.tombstone", err)

	withRestore := goldenRelicPack()
	withRestore.Operations = append(withRestore.Operations,
		RelicOperation{ID: relicTestID(102), Type: RelicOperationRestore})
	_, err = ApplyRelic(nil, withRestore)
	requireCorruptData(t, "root with relic.restore", err)

	twoCreates := goldenRelicPack()
	twoCreates.Operations = append(twoCreates.Operations, RelicOperation{
		ID: relicTestID(103), Type: RelicOperationCreate,
		Relic: &RelicCreateData{Title: "Again", Type: "spec", CreatedAt: relicDocTime},
	})
	_, err = ApplyRelic(nil, twoCreates)
	requireCorruptData(t, "root with two relic.create", err)
}

func TestApplyRelicLinearHistory(t *testing.T) {
	partC := relicTestID(120)
	steps := []struct {
		name   string
		ops    []RelicOperation
		change func(*RelicData)
	}{
		{
			name: "title",
			ops:  []RelicOperation{{ID: relicTestID(110), Type: RelicOperationTitleSet, Title: "Archive compaction"}},
			change: func(data *RelicData) {
				data.Title = "Archive compaction"
			},
		},
		{
			name: "property set and clear",
			ops: []RelicOperation{
				{ID: relicTestID(111), Type: RelicOperationPropertySet, Name: "owner", Value: json.RawMessage(`{"team":"core"}`)},
				{ID: relicTestID(112), Type: RelicOperationPropertyClear, Name: "status"},
			},
			change: func(data *RelicData) {
				data.Properties["owner"] = json.RawMessage(`{"team":"core"}`)
				delete(data.Properties, "status")
			},
		},
		{
			name: "part add",
			ops: []RelicOperation{{ID: partC, Type: RelicOperationPartAdd, PartType: "note", Position: "3/2",
				Body: &RelicBodyChange{Format: "txt", Bytes: 12}}},
			change: func(data *RelicData) {
				data.Parts = []RelicPart{
					data.Parts[0],
					{ID: partC, Type: "note", Position: "3/2", Body: &RelicBody{Format: "txt", Version: partC, Bytes: 12}},
					data.Parts[1],
				}
			},
		},
		{
			name: "move",
			ops:  []RelicOperation{{ID: relicTestID(121), Type: RelicOperationPartMove, Part: partC, Position: "1/2"}},
			change: func(data *RelicData) {
				moved := data.Parts[1]
				moved.Position = "1/2"
				data.Parts = []RelicPart{moved, data.Parts[0], data.Parts[2]}
			},
		},
		{
			name: "remove",
			ops:  []RelicOperation{{ID: relicTestID(122), Type: RelicOperationPartRemove, Part: relicDocPartB}},
			change: func(data *RelicData) {
				data.Parts[2].Removed = true
			},
		},
		{
			name: "restore part",
			ops:  []RelicOperation{{ID: relicTestID(123), Type: RelicOperationPartRestore, Part: relicDocPartB}},
			change: func(data *RelicData) {
				data.Parts[2].Removed = false
			},
		},
		{
			name: "body set",
			ops: []RelicOperation{{ID: relicTestID(124), Type: RelicOperationPartBodySet, Part: relicDocPartA,
				Body: &RelicBodyChange{Format: "md", Bytes: 2000, Base: relicDocPartA}}},
			change: func(data *RelicData) {
				data.Parts[1].Body = &RelicBody{Format: "md", Version: relicTestID(124), Bytes: 2000}
			},
		},
	}

	record := goldenRelicRecord(t)
	for index, step := range steps {
		clock := uint64(index + 2)
		pack := relicFoldPack(clock, step.ops...)
		next := mustApplyRelic(t, record, pack)
		if next.State.LogicalClock != clock {
			t.Errorf("%s: clock = %d, want %d", step.name, next.State.LogicalClock, clock)
		}
		if !next.State.Relic.UpdatedAt.Equal(pack.WallTime) {
			t.Errorf("%s: updatedAt = %v, want %v", step.name, next.State.Relic.UpdatedAt, pack.WallTime)
		}
		want := copyRelicData(record.State.Relic)
		step.change(&want)
		want.UpdatedAt = pack.WallTime
		requireRelicEqual(t, step.name, next.State.Relic, want)
		if next.State.History != record.State.History || next.State.ProjectID != record.State.ProjectID ||
			next.State.RelicID != record.State.RelicID || next.State.MinReader != RelicMinReader {
			t.Errorf("%s: state header changed: %#v", step.name, next.State)
		}
		if err := ValidateRelicCheckpoint(&record, pack, next); err != nil {
			t.Errorf("%s: ValidateRelicCheckpoint: %v", step.name, err)
		}
		record = next
	}
}

func TestApplyRelicGivesTheSameRelicInAnyOrder(t *testing.T) {
	root := goldenRelicRecord(t)
	property := relicFoldPack(2, RelicOperation{ID: relicTestID(130), Type: RelicOperationPropertySet,
		Name: "owner", Value: json.RawMessage(`"dylan"`)})
	moveA := relicFoldPack(2, RelicOperation{ID: relicTestID(131), Type: RelicOperationPartMove,
		Part: relicDocPartA, Position: "3/1"})
	bodyB := relicFoldPack(2, RelicOperation{ID: relicTestID(132), Type: RelicOperationPartBodySet,
		Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 7}})
	packs := []RelicOperationPack{property, moveA, bodyB}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}

	var first RelicData
	for index, order := range orders {
		record := root
		for step, which := range order {
			record = mustApplyRelic(t, record, reclock(packs[which], uint64(step+2)))
		}
		got := withoutUpdatedAt(record.State.Relic)
		if index == 0 {
			first = got
			if part := requirePartForTest(t, got, relicDocPartB); part.Body == nil || part.Body.Version != relicTestID(132) {
				t.Errorf("part B body = %#v, want the edit applied", part.Body)
			}
			continue
		}
		requireRelicEqual(t, "order "+fmtInts(order), got, first)
	}

	fieldOps := []RelicOperation{
		{ID: relicTestID(133), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "heading", Value: json.RawMessage(`"Goals"`)},
		{ID: relicTestID(134), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "level", Value: json.RawMessage(`2`)},
	}
	forward := ApplyRelicFields(nil, relicDocPartA, fieldOps)
	backward := ApplyRelicFields(nil, relicDocPartA, []RelicOperation{fieldOps[1], fieldOps[0]})
	if !reflect.DeepEqual(forward, backward) || len(forward) != 2 {
		t.Errorf("ApplyRelicFields: forward %v, backward %v", forward, backward)
	}
}

func fmtInts(values []int) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func TestApplyRelicDuplicateDeliveryChangesNothing(t *testing.T) {
	partC := relicTestID(140)
	cases := []struct {
		name  string
		setup []RelicOperation
		ops   []RelicOperation
	}{
		{name: "part.add", ops: []RelicOperation{{ID: partC, Type: RelicOperationPartAdd, PartType: "note", Position: "5/1",
			Body: &RelicBodyChange{Bytes: 3}}}},
		{name: "part.body.set", ops: []RelicOperation{{ID: relicTestID(141), Type: RelicOperationPartBodySet,
			Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 5, Base: relicDocPartA}}}},
		{name: "part.move", ops: []RelicOperation{{ID: relicTestID(142), Type: RelicOperationPartMove,
			Part: relicDocPartB, Position: "1/2"}}},
		{name: "part.remove", ops: []RelicOperation{{ID: relicTestID(143), Type: RelicOperationPartRemove,
			Part: relicDocPartB}}},
		{name: "part.restore",
			setup: []RelicOperation{{ID: relicTestID(144), Type: RelicOperationPartRemove, Part: relicDocPartB}},
			ops:   []RelicOperation{{ID: relicTestID(145), Type: RelicOperationPartRestore, Part: relicDocPartB}}},
		{name: "property.set", ops: []RelicOperation{{ID: relicTestID(146), Type: RelicOperationPropertySet,
			Name: "owner", Value: json.RawMessage(`"dylan"`)}}},
		{name: "property.clear", ops: []RelicOperation{{ID: relicTestID(147), Type: RelicOperationPropertyClear,
			Name: "status"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := goldenRelicRecord(t)
			clock := uint64(2)
			if tc.setup != nil {
				record = mustApplyRelic(t, record, relicFoldPack(clock, tc.setup...))
				clock++
			}
			once := mustApplyRelic(t, record, relicFoldPack(clock, tc.ops...))
			if reflect.DeepEqual(withoutUpdatedAt(once.State.Relic), withoutUpdatedAt(record.State.Relic)) {
				t.Fatalf("the first delivery changed nothing")
			}
			twice := mustApplyRelic(t, once, relicFoldPack(clock+1, tc.ops...))
			requireRelicEqual(t, "after the duplicate", withoutUpdatedAt(twice.State.Relic), withoutUpdatedAt(once.State.Relic))
			if len(twice.State.Relic.Parts) != len(once.State.Relic.Parts) {
				t.Errorf("part count = %d, want %d", len(twice.State.Relic.Parts), len(once.State.Relic.Parts))
			}
			if twice.State.LogicalClock != clock+1 {
				t.Errorf("clock = %d, want %d", twice.State.LogicalClock, clock+1)
			}
		})
	}
}

func TestApplyRelicIgnoresABodyEditFromAStaleBase(t *testing.T) {
	c1, c2, c3 := relicTestID(150), relicTestID(151), relicTestID(152)
	root := relicFoldPack(1,
		RelicOperation{ID: relicDocCreateOp, Type: RelicOperationCreate, Relic: &RelicCreateData{
			Title: "Body race", Type: "spec", CreatedAt: relicDocTime}},
		RelicOperation{ID: c1, Type: RelicOperationPartAdd, PartType: "section", Position: "1/1",
			Body: &RelicBodyChange{Format: "md", Bytes: 10}},
		RelicOperation{ID: relicDocPartB, Type: RelicOperationPartAdd, PartType: "section", Position: "2/1"},
	)
	record, err := ApplyRelic(nil, root)
	if err != nil {
		t.Fatalf("ApplyRelic(root): %v", err)
	}

	alice := mustApplyRelic(t, record, relicFoldPack(2, RelicOperation{ID: c2, Type: RelicOperationPartBodySet,
		Part: c1, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: c1}}))
	if body := requirePartForTest(t, alice.State.Relic, c1).Body; body == nil || body.Version != c2 || body.Bytes != 20 {
		t.Fatalf("after Alice: body = %#v, want version c2 and 20 bytes", body)
	}

	bob, err := ApplyRelic(&alice, relicFoldPack(3, RelicOperation{ID: c3, Type: RelicOperationPartBodySet,
		Part: c1, Body: &RelicBodyChange{Format: "txt", Bytes: 99, Base: c1}}))
	if err != nil {
		t.Fatalf("Bob's stale edit: %v", err)
	}
	body := requirePartForTest(t, bob.State.Relic, c1).Body
	if body == nil || body.Version != c2 || body.Bytes != 20 || body.Format != "md" {
		t.Errorf("after Bob: body = %#v, want Alice's body unchanged", body)
	}
	if bob.State.LogicalClock != alice.State.LogicalClock+1 {
		t.Errorf("clock = %d, want %d", bob.State.LogicalClock, alice.State.LogicalClock+1)
	}
	requireRelicEqual(t, "after Bob", withoutUpdatedAt(bob.State.Relic), withoutUpdatedAt(alice.State.Relic))

	baseOnNoBody := mustApplyRelic(t, bob, relicFoldPack(4, RelicOperation{ID: relicTestID(153),
		Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Bytes: 4, Base: c1}}))
	requireRelicEqual(t, "a base on a part with no body",
		withoutUpdatedAt(baseOnNoBody.State.Relic), withoutUpdatedAt(bob.State.Relic))

	noBaseOnBody := mustApplyRelic(t, baseOnNoBody, relicFoldPack(5, RelicOperation{ID: relicTestID(154),
		Type: RelicOperationPartBodySet, Part: c1, Body: &RelicBodyChange{Bytes: 4}}))
	requireRelicEqual(t, "no base on a part with a body",
		withoutUpdatedAt(noBaseOnBody.State.Relic), withoutUpdatedAt(bob.State.Relic))
}

func TestApplyRelicEditsARemovedPart(t *testing.T) {
	removed := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		RelicOperation{ID: relicTestID(160), Type: RelicOperationPartRemove, Part: relicDocPartA}))
	edited := mustApplyRelic(t, removed, relicFoldPack(3,
		RelicOperation{ID: relicTestID(161), Type: RelicOperationPartMove, Part: relicDocPartA, Position: "9/1"},
		RelicOperation{ID: relicTestID(162), Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 3, Base: relicDocPartA}}))
	part := requirePartForTest(t, edited.State.Relic, relicDocPartA)
	if !part.Removed || part.Position != "9/1" || part.Body == nil || part.Body.Version != relicTestID(162) {
		t.Errorf("part = %#v (body %#v), want removed, at 9/1, with the new body", part, part.Body)
	}
	if edited.State.Relic.Parts[1].ID != relicDocPartA {
		t.Errorf("parts are not sorted by position after the move: %#v", edited.State.Relic.Parts)
	}
}

func TestApplyRelicUnknownPartChangesNothing(t *testing.T) {
	unknown := relicTestID(170)
	root := goldenRelicRecord(t)
	for _, operation := range []RelicOperation{
		{ID: relicTestID(171), Type: RelicOperationPartMove, Part: unknown, Position: "1/3"},
		{ID: relicTestID(172), Type: RelicOperationPartRemove, Part: unknown},
		{ID: relicTestID(173), Type: RelicOperationPartRestore, Part: unknown},
		{ID: relicTestID(174), Type: RelicOperationPartBodySet, Part: unknown, Body: &RelicBodyChange{Bytes: 1}},
	} {
		got := mustApplyRelic(t, root, relicFoldPack(2, operation))
		requireRelicEqual(t, string(operation.Type), withoutUpdatedAt(got.State.Relic), withoutUpdatedAt(root.State.Relic))
	}
}

func TestApplyRelicTombstoneRules(t *testing.T) {
	root := goldenRelicRecord(t)
	tombstone := RelicOperation{ID: relicTestID(180), Type: RelicOperationTombstone}
	deleted := mustApplyRelic(t, root, relicFoldPack(2, tombstone))
	if !deleted.State.Relic.Deleted {
		t.Fatalf("relic is not deleted after relic.tombstone")
	}
	restored := mustApplyRelic(t, deleted, relicFoldPack(3,
		RelicOperation{ID: relicTestID(181), Type: RelicOperationRestore},
		RelicOperation{ID: relicTestID(182), Type: RelicOperationTitleSet, Title: "Back again"}))
	if restored.State.Relic.Deleted || restored.State.Relic.Title != "Back again" {
		t.Errorf("after restore: deleted %v, title %q", restored.State.Relic.Deleted, restored.State.Relic.Title)
	}

	title := RelicOperation{ID: relicTestID(183), Type: RelicOperationTitleSet, Title: "Edited"}
	restore := RelicOperation{ID: relicTestID(184), Type: RelicOperationRestore}
	_, err := ApplyRelic(&deleted, relicFoldPack(3, title))
	requireCorruptData(t, "an edit to a deleted relic", err)
	_, err = ApplyRelic(&deleted, relicFoldPack(3, RelicOperation{ID: relicTestID(185), Type: RelicOperationTombstone}))
	requireCorruptData(t, "a tombstone of a deleted relic", err)
	_, err = ApplyRelic(&root, relicFoldPack(2, restore))
	requireCorruptData(t, "a restore of a live relic", err)
	_, err = ApplyRelic(&deleted, relicFoldPack(3, title, restore))
	requireCorruptData(t, "a restore that is not first", err)
	_, err = ApplyRelic(&deleted, relicFoldPack(3, restore, RelicOperation{ID: relicTestID(186), Type: RelicOperationTombstone}))
	requireCorruptData(t, "a restore followed by a tombstone", err)
	_, err = ApplyRelic(&root, relicFoldPack(2, tombstone, title))
	requireCorruptData(t, "an edit after a tombstone", err)
	_, err = ApplyRelic(&root, relicFoldPack(2, RelicOperation{ID: relicTestID(187), Type: RelicOperationCreate,
		Relic: &RelicCreateData{Title: "Again", Type: "spec", CreatedAt: relicDocTime}}))
	requireCorruptData(t, "relic.create with a parent", err)
}

func TestApplyRelicRefusesAMismatchedParent(t *testing.T) {
	root := goldenRelicRecord(t)
	title := RelicOperation{ID: relicTestID(190), Type: RelicOperationTitleSet, Title: "Edited"}
	for name, mutate := range map[string]func(*RelicOperationPack){
		"project":    func(pack *RelicOperationPack) { pack.ProjectID = relicTestID(191) },
		"relic":      func(pack *RelicOperationPack) { pack.RelicID = "WB-R-" + relicTestID(192) },
		"generation": func(pack *RelicOperationPack) { pack.HistoryGeneration = relicTestID(193) },
		"clock":      func(pack *RelicOperationPack) { pack.LogicalClock = 3 },
	} {
		pack := relicFoldPack(2, title)
		mutate(&pack)
		_, err := ApplyRelic(&root, pack)
		requireCorruptData(t, name, err)
	}
}

func TestValidateRelicCheckpointRejectsAStateThatDoesNotFollow(t *testing.T) {
	c2, c3 := relicTestID(200), relicTestID(201)
	root := goldenRelicRecord(t)
	alice := mustApplyRelic(t, root, relicFoldPack(2, RelicOperation{ID: c2, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))
	bobPack := relicFoldPack(3, RelicOperation{ID: c3, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 99, Base: relicDocPartA}})
	bob := mustApplyRelic(t, alice, bobPack)
	if err := ValidateRelicCheckpoint(&alice, bobPack, bob); err != nil {
		t.Fatalf("ValidateRelicCheckpoint(computed): %v", err)
	}

	won := RelicRecord{State: bob.State, Links: bob.Links}
	won.State.Relic = copyRelicData(bob.State.Relic)
	won.State.Relic.Parts[0].Body = &RelicBody{Format: "md", Version: c3, Bytes: 99}
	err := ValidateRelicCheckpoint(&alice, bobPack, won)
	requireCorruptData(t, "a stale edit that won", err)
	if err == nil || !strings.Contains(err.Error(), "stored checkpoint differs from computed state") {
		t.Errorf("error = %v, want it to say the stored checkpoint differs", err)
	}
}

func TestApplyRelicCarriesMinReaderForward(t *testing.T) {
	root := goldenRelicRecord(t)
	if root.State.MinReader != RelicMinReader {
		t.Errorf("root minReader = %d, want %d", root.State.MinReader, RelicMinReader)
	}
	next := mustApplyRelic(t, root, relicFoldPack(2,
		RelicOperation{ID: relicTestID(210), Type: RelicOperationTitleSet, Title: "Next"}))
	if next.State.MinReader != RelicMinReader {
		t.Errorf("child minReader = %d, want %d", next.State.MinReader, RelicMinReader)
	}
	if got := foldedRelicMinReader(&RelicRecord{State: RelicStateDocument{MinReader: 9}}, relicFoldPack(2)); got != 9 {
		t.Errorf("foldedRelicMinReader kept %d, want the parent's 9", got)
	}
}

func TestApplyRelicRefusesANewerParentOrPack(t *testing.T) {
	root := goldenRelicRecord(t)
	title := RelicOperation{ID: relicTestID(220), Type: RelicOperationTitleSet, Title: "Next"}

	newerParent := root
	newerParent.State.MinReader = SupportedFormatGeneration + 1
	// The gate runs first, so it wins even over a pack that is also invalid.
	invalid := relicFoldPack(9, title)
	invalid.ProjectID = "not a project"
	if _, err := ApplyRelic(&newerParent, invalid); CategoryOf(err) != CategoryNewerWriter {
		t.Errorf("newer parent: error = %v, want newer-writer", err)
	}

	newerPack := relicFoldPack(2, title)
	newerPack.MinReader = SupportedFormatGeneration + 1
	if _, err := ApplyRelic(&root, newerPack); CategoryOf(err) != CategoryNewerWriter {
		t.Errorf("newer pack: error = %v, want newer-writer", err)
	}
	newerRoot := goldenRelicPack()
	newerRoot.MinReader = SupportedFormatGeneration + 1
	if _, err := ApplyRelic(nil, newerRoot); CategoryOf(err) != CategoryNewerWriter {
		t.Errorf("newer root pack: error = %v, want newer-writer", err)
	}
}

func TestApplyRelicFields(t *testing.T) {
	before := map[string]json.RawMessage{"heading": json.RawMessage(`"Goals"`), "level": json.RawMessage(`1`)}
	other := relicTestID(230)
	got := ApplyRelicFields(before, relicDocPartA, []RelicOperation{
		{ID: relicTestID(231), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "owner", Value: json.RawMessage(`"dylan"`)},
		{ID: relicTestID(232), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "heading", Value: json.RawMessage(`"Aims"`)},
		{ID: relicTestID(233), Type: RelicOperationPartFieldClear, Part: relicDocPartA, Name: "level"},
		{ID: relicTestID(234), Type: RelicOperationPartFieldClear, Part: relicDocPartA, Name: "absent"},
		{ID: relicTestID(235), Type: RelicOperationPartFieldSet, Part: other, Name: "heading", Value: json.RawMessage(`"Other"`)},
		{ID: relicTestID(236), Type: RelicOperationPartFieldClear, Part: other, Name: "owner"},
		{ID: relicTestID(237), Type: RelicOperationPropertySet, Name: "status", Value: json.RawMessage(`"x"`)},
	})
	want := map[string]json.RawMessage{"heading": json.RawMessage(`"Aims"`), "owner": json.RawMessage(`"dylan"`)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	wantBefore := map[string]json.RawMessage{"heading": json.RawMessage(`"Goals"`), "level": json.RawMessage(`1`)}
	if !reflect.DeepEqual(before, wantBefore) {
		t.Errorf("input map was mutated: %v", before)
	}
	if empty := ApplyRelicFields(nil, relicDocPartA, nil); empty == nil || len(empty) != 0 {
		t.Errorf("ApplyRelicFields(nil) = %#v, want an empty map", empty)
	}
}

func TestRelicBodySavedByAndPartsNamedBy(t *testing.T) {
	pack := goldenRelicPack()
	record := goldenRelicRecord(t)
	// A new relic has no parent, so every body it lists arrived with it.
	if !RelicBodySavedBy(nil, pack, record, relicDocPartA) {
		t.Errorf("part A's body was saved by the root pack")
	}
	if RelicBodySavedBy(nil, pack, record, relicDocPartB) {
		t.Errorf("part B has no body")
	}
	if RelicBodySavedBy(nil, pack, record, relicTestID(239)) {
		t.Errorf("the relic has no such part")
	}
	edit := relicFoldPack(2, RelicOperation{ID: relicTestID(240), Type: RelicOperationTitleSet, Title: "Next"})
	next := mustApplyRelic(t, record, edit)
	if RelicBodySavedBy(&record, edit, next, relicDocPartA) {
		t.Errorf("part A's body was not saved by a title edit")
	}

	// A part new in this commit.
	partC := relicTestID(245)
	add := relicFoldPack(2, RelicOperation{ID: partC, Type: RelicOperationPartAdd, PartType: "section", Position: "3/1",
		Body: &RelicBodyChange{Format: "md", Bytes: 12}})
	added := mustApplyRelic(t, record, add)
	if !RelicBodySavedBy(&record, add, added, partC) {
		t.Errorf("part C's body was saved by the pack that added it")
	}
	if RelicBodySavedBy(&record, add, added, relicDocPartA) {
		t.Errorf("part A's body was not saved by a pack that adds part C")
	}

	// A body version that changed: a first body for a part that had none, and
	// a new version of a body.
	firstBody := relicFoldPack(2, RelicOperation{ID: relicTestID(246), Type: RelicOperationPartBodySet, Part: relicDocPartB,
		Body: &RelicBodyChange{Format: "md", Bytes: 7}})
	if !RelicBodySavedBy(&record, firstBody, mustApplyRelic(t, record, firstBody), relicDocPartB) {
		t.Errorf("part B's first body was saved by the pack that set it")
	}
	bodyEdit := relicFoldPack(2, RelicOperation{ID: relicTestID(247), Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 1840, Base: relicDocPartA}})
	edited := mustApplyRelic(t, record, bodyEdit)
	if got := requirePartForTest(t, edited.State.Relic, relicDocPartA).Body.Version; got != relicTestID(247) {
		t.Fatalf("the body edit folded to version %s; the test needs it to apply", got)
	}
	if !RelicBodySavedBy(&record, bodyEdit, edited, relicDocPartA) {
		t.Errorf("part A's body was saved by the edit that gave it a new version")
	}

	// A repeated part.add carries the ID of a part the relic already has,
	// which is also the version of that part's first body. The fold ignores
	// it, so it saved nothing.
	repeatedAdd := relicFoldPack(2, RelicOperation{ID: relicDocPartA, Type: RelicOperationPartAdd, PartType: "section",
		Position: "1/1", Body: &RelicBodyChange{Format: "md", Bytes: 1840}})
	afterRepeatedAdd := mustApplyRelic(t, record, repeatedAdd)
	requireRelicEqual(t, "a repeated part.add", withoutUpdatedAt(afterRepeatedAdd.State.Relic), withoutUpdatedAt(record.State.Relic))
	if RelicBodySavedBy(&record, repeatedAdd, afterRepeatedAdd, relicDocPartA) {
		t.Errorf("a repeated part.add saved no body: part A's version did not change")
	}

	// A repeated part.body.set carries the ID that is already the body's
	// version, whether it names the current version as its base or an older one.
	for name, base := range map[string]string{"from the current version": relicTestID(247), "from an older version": relicDocPartA} {
		repeatedEdit := relicFoldPack(3, RelicOperation{ID: relicTestID(247), Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 1840, Base: base}})
		afterRepeatedEdit := mustApplyRelic(t, edited, repeatedEdit)
		if got := requirePartForTest(t, afterRepeatedEdit.State.Relic, relicDocPartA).Body.Version; got != relicTestID(247) {
			t.Fatalf("a repeated body edit %s folded to version %s; the test needs the version to stay", name, got)
		}
		if RelicBodySavedBy(&edited, repeatedEdit, afterRepeatedEdit, relicDocPartA) {
			t.Errorf("a repeated part.body.set %s saved no body: part A's version did not change", name)
		}
	}

	if got, want := RelicPartsNamedBy(pack), []string{relicDocPartA, relicDocPartB}; !reflect.DeepEqual(got, want) {
		t.Errorf("RelicPartsNamedBy(root) = %v, want %v", got, want)
	}
	mixed := relicFoldPack(2,
		RelicOperation{ID: relicTestID(241), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "1/2"},
		RelicOperation{ID: relicTestID(242), Type: RelicOperationPartFieldSet, Part: relicDocPartB, Name: "a", Value: json.RawMessage(`1`)},
		RelicOperation{ID: relicTestID(243), Type: RelicOperationPartFieldClear, Part: relicDocPartA, Name: "a"},
		RelicOperation{ID: relicTestID(244), Type: RelicOperationPropertySet, Name: "a", Value: json.RawMessage(`1`)},
	)
	if got, want := RelicPartsNamedBy(mixed), []string{relicDocPartA, relicDocPartB}; !reflect.DeepEqual(got, want) {
		t.Errorf("RelicPartsNamedBy(mixed) = %v, want %v", got, want)
	}
	if got := RelicPartsNamedBy(edit); got == nil || len(got) != 0 {
		t.Errorf("RelicPartsNamedBy(title edit) = %#v, want an empty list", got)
	}
}
