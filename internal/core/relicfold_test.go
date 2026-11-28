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

// A part.add is not among these: delivered twice it would take a part's ID a
// second time, which the fold refuses. See
// TestApplyRelicRefusesAnIDUsedAgainForAPartOrABodyVersion.
func TestApplyRelicDuplicateDeliveryChangesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup []RelicOperation
		ops   []RelicOperation
	}{
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
	if err == nil || !strings.Contains(err.Error(), "the stored relic is not what its operations produce") {
		t.Errorf("error = %v, want it to say the stored relic is not what its operations produce", err)
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
	// which is also the version of that part's first body. The fold refuses
	// it, so there is no record for it to have saved a body in.
	repeatedAdd := relicFoldPack(2, RelicOperation{ID: relicDocPartA, Type: RelicOperationPartAdd, PartType: "section",
		Position: "1/1", Body: &RelicBodyChange{Format: "md", Bytes: 1840}})
	_, err := ApplyRelic(&record, repeatedAdd)
	requireCorruptData(t, "a repeated part.add", err)

	// A repeated part.body.set carries the ID that is already the body's
	// version. Delivered again from the version it started from, the fold
	// ignores it.
	repeatedEdit := relicFoldPack(3, RelicOperation{ID: relicTestID(247), Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 1840, Base: relicDocPartA}})
	afterRepeatedEdit := mustApplyRelic(t, edited, repeatedEdit)
	if got := requirePartForTest(t, afterRepeatedEdit.State.Relic, relicDocPartA).Body.Version; got != relicTestID(247) {
		t.Fatalf("a repeated body edit folded to version %s; the test needs the version to stay", got)
	}
	if RelicBodySavedBy(&edited, repeatedEdit, afterRepeatedEdit, relicDocPartA) {
		t.Errorf("a repeated part.body.set saved no body: part A's version did not change")
	}
	// Naming the version it saved as its base, it names itself, which no pack
	// may do.
	selfBased := relicFoldPack(3, RelicOperation{ID: relicTestID(247), Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 1840, Base: relicTestID(247)}})
	if _, err := ApplyRelic(&edited, selfBased); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("ApplyRelic(a body edit based on itself) error = %v, want corrupt data", err)
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

// The limit on all of a relic's properties together is a total: each of two
// packs may set a property that is within every limit on its own, and the fold
// has to produce the relic that holds both. Refusing it would make a relic
// unreadable through two edits that were each allowed.
func TestApplyRelicFoldsPropertiesPastTheirTotal(t *testing.T) {
	large := func() json.RawMessage { return json.RawMessage(`"` + strings.Repeat("x", 40000-2) + `"`) }
	record := goldenRelicRecord(t)
	record = mustApplyRelic(t, record, relicFoldPack(2,
		RelicOperation{ID: relicTestID(50), Type: RelicOperationPropertySet, Name: "a", Value: large()}))
	record = mustApplyRelic(t, record, relicFoldPack(3,
		RelicOperation{ID: relicTestID(51), Type: RelicOperationPropertySet, Name: "b", Value: large()}))
	encoded, err := json.Marshal(record.State.Relic.Properties)
	if err != nil || len(encoded) <= MaxRelicPropertiesBytes {
		t.Fatalf("the folded properties are %d bytes, %v; want them past %d", len(encoded), err, MaxRelicPropertiesBytes)
	}
	requireRelicStateRoundTrips(t, "the folded relic", record.State)
	// And the relic can still be changed.
	record = mustApplyRelic(t, record, relicFoldPack(4,
		RelicOperation{ID: relicTestID(52), Type: RelicOperationTitleSet, Title: "Still here"}))
	if record.State.Relic.Title != "Still here" {
		t.Fatalf("title = %q", record.State.Relic.Title)
	}

	// One value past the limit for one value is not a total, and is refused.
	_, err = ApplyRelic(&record, relicFoldPack(5, RelicOperation{
		ID: relicTestID(53), Type: RelicOperationPropertySet, Name: "c",
		Value: json.RawMessage(`"` + strings.Repeat("x", MaxRelicPropertyValueBytes-1) + `"`),
	}))
	requireCorruptData(t, "one property value of 64 KiB + 1", err)
}

// The same for one part's fields, which the fold computes from the stored
// fields and the pack's field operations.
func TestApplyRelicFieldsPastTheirTotal(t *testing.T) {
	large := func() json.RawMessage { return json.RawMessage(`"` + strings.Repeat("x", 9000-2) + `"`) }
	fields := ApplyRelicFields(nil, relicDocPartA, []RelicOperation{
		{ID: relicTestID(50), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "a", Value: large()},
	})
	fields = ApplyRelicFields(fields, relicDocPartA, []RelicOperation{
		{ID: relicTestID(51), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "b", Value: large()},
	})
	encoded, err := EncodeDocument(NewRelicFields(fields))
	if err != nil || len(encoded) <= MaxRelicPartFieldsBytes {
		t.Fatalf("EncodeDocument(the folded fields) = %d bytes, %v; want them stored past %d", len(encoded), err, MaxRelicPartFieldsBytes)
	}
	if _, err := DecodeRelicFieldsDocument(encoded); err != nil {
		t.Fatalf("DecodeRelicFieldsDocument(the folded fields): %v", err)
	}
}

// A part's ID names the part and is the version of its first body, and a body
// edit's ID is the version it saves. An operation that takes one of those IDs
// again would make one name stand for two things: a body version that names
// two texts, so that a save from it overwrites text its author never saw, or
// a part added twice. The fold refuses each as corrupt data.
func TestApplyRelicRefusesAnIDUsedAgainForAPartOrABodyVersion(t *testing.T) {
	root := goldenRelicRecord(t)
	second := relicTestID(180)
	// Part A's body goes from its first version, the part's ID, to second.
	edited := mustApplyRelic(t, root, relicFoldPack(2, RelicOperation{ID: second, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))

	for name, operation := range map[string]RelicOperation{
		"a body edit that takes the part's own ID, which was its first version": {
			ID: relicDocPartA, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: second}},
		"a body edit that takes another part's ID": {
			ID: relicDocPartB, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: second}},
		"a body edit from a stale base that takes a part's ID": {
			ID: relicDocPartA, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicTestID(181)}},
		"a first body for another part that takes a part's ID": {
			ID: relicDocPartA, Type: RelicOperationPartBodySet, Part: relicDocPartB,
			Body: &RelicBodyChange{Format: "md", Bytes: 30}},
		"a part.add that takes the ID of a part the relic has": {
			ID: relicDocPartB, Type: RelicOperationPartAdd, PartType: "note", Position: "9/1"},
		"a part.add that takes the ID of a removed part": {
			ID: relicDocPartA, Type: RelicOperationPartAdd, PartType: "note", Position: "9/1",
			Body: &RelicBodyChange{Format: "md", Bytes: 30}},
		"a part.add that takes the ID of a body's current version": {
			ID: second, Type: RelicOperationPartAdd, PartType: "note", Position: "9/1"},
	} {
		parent := edited
		if strings.Contains(name, "removed") {
			parent = mustApplyRelic(t, edited, relicFoldPack(3,
				RelicOperation{ID: relicTestID(182), Type: RelicOperationPartRemove, Part: relicDocPartA}))
		}
		_, err := ApplyRelic(&parent, relicFoldPack(parent.State.LogicalClock+1, operation))
		requireCorruptData(t, name, err)
		if err != nil && !strings.Contains(err.Error(), operation.ID) {
			t.Errorf("%s: error %q does not name the ID %s", name, err, operation.ID)
		}
	}

	// IDs nothing has used fold as before.
	next := mustApplyRelic(t, edited, relicFoldPack(3,
		RelicOperation{ID: relicTestID(183), Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: second}},
		RelicOperation{ID: relicTestID(184), Type: RelicOperationPartAdd, PartType: "note", Position: "9/1"}))
	if body := requirePartForTest(t, next.State.Relic, relicDocPartA).Body; body.Version != relicTestID(183) {
		t.Errorf("part A body version = %s, want the new edit's", body.Version)
	}
	if len(next.State.Relic.Parts) != 3 {
		t.Errorf("parts = %d, want 3", len(next.State.Relic.Parts))
	}
}

func TestApplyRelicFoldsMigrate(t *testing.T) {
	schemaA, schemaB := relicTestID(50), relicTestID(51)
	root := goldenRelicPack()
	root.Operations[0].Relic.Schema = schemaA
	created, err := ApplyRelic(nil, root)
	if err != nil {
		t.Fatalf("ApplyRelic(root naming a schema): %v", err)
	}
	if created.State.Relic.Schema != schemaA || created.State.Relic.Type != "spec" {
		t.Fatalf("created relic type, schema = %q, %q; want spec, %s", created.State.Relic.Type, created.State.Relic.Schema, schemaA)
	}

	toPlan := mustApplyRelic(t, created, relicFoldPack(2,
		RelicOperation{ID: relicTestID(60), Type: RelicOperationMigrate, RelicType: "plan", Schema: schemaB}))
	want := copyRelicData(created.State.Relic)
	want.Type, want.Schema = "plan", schemaB
	requireRelicEqual(t, "migrated to plan", withoutUpdatedAt(toPlan.State.Relic), withoutUpdatedAt(want))
	if toPlan.State.LogicalClock != 2 || !toPlan.State.Relic.UpdatedAt.Equal(relicDocTime.Add(time.Minute)) {
		t.Errorf("migrated clock, updatedAt = %d, %v", toPlan.State.LogicalClock, toPlan.State.Relic.UpdatedAt)
	}

	open := mustApplyRelic(t, toPlan, relicFoldPack(3,
		RelicOperation{ID: relicTestID(61), Type: RelicOperationMigrate, RelicType: "plan"}))
	want.Schema = ""
	requireRelicEqual(t, "migrated to open plan", withoutUpdatedAt(open.State.Relic), withoutUpdatedAt(want))
	if encoded, err := EncodeDocument(open.State); err != nil || !strings.Contains(string(encoded), `"type":"plan","schema":null`) {
		t.Errorf("an open migrated relic's state = %s, %v", encoded, err)
	}

	deleted := mustApplyRelic(t, open, relicFoldPack(4, RelicOperation{ID: relicTestID(62), Type: RelicOperationTombstone}))
	_, err = ApplyRelic(&deleted, relicFoldPack(5,
		RelicOperation{ID: relicTestID(63), Type: RelicOperationMigrate, RelicType: "spec", Schema: schemaA}))
	requireCorruptData(t, "migrating a deleted relic", err)
}

// stateBytesWithoutClock encodes a relic state with its clock and update time
// cleared, for comparing a state with its parent across a fold.
func stateBytesWithoutClock(t *testing.T, state RelicStateDocument) string {
	t.Helper()
	state.LogicalClock = 1
	state.Relic.UpdatedAt = relicDocTime
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(state): %v", err)
	}
	return string(encoded)
}

func TestRetireShapesAndFold(t *testing.T) {
	retire := RelicOperation{ID: relicTestID(7300), Type: RelicOperationRetire}
	unretire := RelicOperation{ID: relicTestID(7301), Type: RelicOperationUnretire}
	for _, operation := range []RelicOperation{retire, unretire} {
		pack := singleRelicOperationPack(operation)
		encoded, err := EncodeDocument(pack)
		if err != nil {
			t.Fatalf("EncodeDocument(%s) = %v", operation.Type, err)
		}
		want := `"operations":[{"id":"` + operation.ID + `","type":"` + string(operation.Type) + `"}]}`
		if !strings.Contains(string(encoded), want) {
			t.Errorf("%s encodes as %s, want it to hold %s", operation.Type, encoded, want)
		}
		decoded, err := DecodeRelicOperationPack(encoded)
		if err != nil || !reflect.DeepEqual(decoded, pack) {
			t.Errorf("DecodeRelicOperationPack(%s) = %#v, %v", operation.Type, decoded, err)
		}
		for label, change := range map[string]func(*RelicOperation){
			"title":      func(o *RelicOperation) { o.Title = "Retired" },
			"name":       func(o *RelicOperation) { o.Name = "status" },
			"part":       func(o *RelicOperation) { o.Part = relicDocPartA },
			"checkpoint": func(o *RelicOperation) { o.Checkpoint = &RelicCheckpointData{RunID: relicTestID(7302)} },
		} {
			changed := operation
			change(&changed)
			refused := singleRelicOperationPack(changed)
			requireRelicPackRefused(t, string(operation.Type)+" with a "+label, refused)
		}
	}

	golden := goldenRelicRecord(t)
	retired := mustApplyRelic(t, golden, relicFoldPack(2, retire))
	if !retired.State.Relic.Retired {
		t.Fatalf("a retire left the relic live")
	}
	again := mustApplyRelic(t, retired, relicFoldPack(3, RelicOperation{ID: relicTestID(7303), Type: RelicOperationRetire}))
	if stateBytesWithoutClock(t, again.State) != stateBytesWithoutClock(t, retired.State) {
		t.Errorf("retiring a retired relic changed more than its clock and update time")
	}
	if again.State.LogicalClock != 3 || !again.State.Relic.UpdatedAt.Equal(relicDocTime.Add(2*time.Minute)) {
		t.Errorf("the repeated retire: clock %d, updatedAt %s", again.State.LogicalClock, again.State.Relic.UpdatedAt)
	}
	live := mustApplyRelic(t, again, relicFoldPack(4, unretire))
	if live.State.Relic.Retired {
		t.Fatalf("an unretire left the relic retired")
	}
	liveAgain := mustApplyRelic(t, live, relicFoldPack(5, RelicOperation{ID: relicTestID(7304), Type: RelicOperationUnretire}))
	if stateBytesWithoutClock(t, liveAgain.State) != stateBytesWithoutClock(t, live.State) {
		t.Errorf("unretiring a live relic changed more than its clock and update time")
	}
	requireRelicEqual(t, "an unretired relic", withoutUpdatedAt(live.State.Relic), withoutUpdatedAt(golden.State.Relic))

	deleted := mustApplyRelic(t, golden, relicFoldPack(2, RelicOperation{ID: relicTestID(7305), Type: RelicOperationTombstone}))
	for _, operation := range []RelicOperation{retire, unretire} {
		_, err := ApplyRelic(&deleted, relicFoldPack(3, operation))
		requireCorruptData(t, string(operation.Type)+" on a deleted relic", err)
	}
	_, err := ApplyRelic(&golden, relicFoldPack(2, retire, RelicOperation{ID: relicTestID(7306), Type: RelicOperationTombstone},
		RelicOperation{ID: relicTestID(7307), Type: RelicOperationUnretire}))
	requireCorruptData(t, "an unretire after a tombstone in one pack", err)

	// A retired relic is deleted and restored as any other, and keeps its mark.
	gone := mustApplyRelic(t, retired, relicFoldPack(3, RelicOperation{ID: relicTestID(7308), Type: RelicOperationTombstone}))
	back := mustApplyRelic(t, gone, relicFoldPack(4, RelicOperation{ID: relicTestID(7309), Type: RelicOperationRestore}))
	if !back.State.Relic.Retired || back.State.Relic.Deleted {
		t.Errorf("a retired relic deleted and restored: retired %v, deleted %v", back.State.Relic.Retired, back.State.Relic.Deleted)
	}

	// A checkpoint never applies onto a parent, nor beside other operations.
	checkpoint, _ := relicCheckpointForTest(RelicBoundaryRetired)
	child := relicFoldPack(2, checkpoint.Operations[0])
	_, err = ApplyRelic(&golden, child)
	requireCorruptData(t, "a checkpoint with a parent", err)
	withCreate := goldenRelicPack()
	withCreate.Operations = append(withCreate.Operations, checkpoint.Operations[0])
	withCreate = NewRelicOperationPack(withCreate.ProjectID, withCreate.RelicID, withCreate.HistoryGeneration,
		withCreate.Actor.ID, 1, withCreate.WallTime, withCreate.Operations)
	_, err = ApplyRelic(nil, withCreate)
	requireCorruptData(t, "a create root with a checkpoint", err)
}

func TestFoldIgnoresChangesToStubs(t *testing.T) {
	_, root := relicCheckpointForTest(RelicBoundaryRetired)
	stub := relicDocPartB
	pack := func(operations ...RelicOperation) RelicOperationPack {
		return NewRelicOperationPack(relicDocProject, relicDocRelic, relicCheckpointGeneration, relicDocActor, 5,
			relicCheckpointWall.Add(time.Minute), operations)
	}
	cases := map[string]RelicOperation{
		"field set": {ID: relicTestID(7400), Type: RelicOperationPartFieldSet, Part: stub, Name: "heading",
			Value: json.RawMessage(`"Back"`)},
		"field clear": {ID: relicTestID(7401), Type: RelicOperationPartFieldClear, Part: stub, Name: "heading"},
		"body set": {ID: relicTestID(7402), Type: RelicOperationPartBodySet, Part: stub,
			Body: &RelicBodyChange{Format: "md", Bytes: 4}},
		"move":    {ID: relicTestID(7403), Type: RelicOperationPartMove, Part: stub, Position: "1/2"},
		"remove":  {ID: relicTestID(7404), Type: RelicOperationPartRemove, Part: stub},
		"restore": {ID: relicTestID(7405), Type: RelicOperationPartRestore, Part: stub},
	}
	want := stateBytesWithoutClock(t, root.State)
	for label, operation := range cases {
		record, err := ApplyRelic(&root, pack(operation))
		if err != nil {
			t.Errorf("%s on a stub: ApplyRelic = %v", label, err)
			continue
		}
		if got := stateBytesWithoutClock(t, record.State); got != want {
			t.Errorf("%s on a stub changed the relic:\n got %s\nwant %s", label, got, want)
		}
		if record.State.MinReader != RelicCheckpointMinReader || record.State.History.CompactedFrom == nil ||
			record.State.Relic.Compacted == nil {
			t.Errorf("%s on a stub: the child lost the checkpoint's generation or compaction", label)
		}
	}

	_, err := ApplyRelic(&root, pack(RelicOperation{ID: stub, Type: RelicOperationPartAdd, PartType: "note", Position: "3/1"}))
	requireCorruptData(t, "a part.add reusing a stub's ID", err)

	// Live parts of a compacted relic still change as usual.
	edited, err := ApplyRelic(&root, pack(RelicOperation{ID: relicTestID(7406), Type: RelicOperationPartMove,
		Part: relicDocPartA, Position: "3/1"}))
	if err != nil || requirePartForTest(t, edited.State.Relic, relicDocPartA).Position != "3/1" {
		t.Fatalf("a move of a live part of a compacted relic = %v", err)
	}

	// Restoring a compacted deleted relic leaves every part a stub.
	_, deletedRoot := relicCheckpointForTest(RelicBoundaryDeleted)
	restored, err := ApplyRelic(&deletedRoot, pack(RelicOperation{ID: relicTestID(7407), Type: RelicOperationRestore},
		RelicOperation{ID: relicTestID(7408), Type: RelicOperationPartRestore, Part: relicDocPartA}))
	if err != nil {
		t.Fatalf("restoring a compacted deleted relic = %v", err)
	}
	if restored.State.Relic.Deleted || len(restored.State.Relic.LiveParts()) != 0 {
		t.Fatalf("the restored relic: deleted %v, %d live parts; want false and 0",
			restored.State.Relic.Deleted, len(restored.State.Relic.LiveParts()))
	}
	for _, part := range restored.State.Relic.Parts {
		if part.Dropped == nil || !part.Removed {
			t.Errorf("part %s of the restored relic is no longer a stub", part.ID)
		}
	}
}

// aliceSavedPartA is the golden relic after Alice saved part A from its first
// body, as version current.
func aliceSavedPartA(t *testing.T, current string) RelicRecord {
	t.Helper()
	return mustApplyRelic(t, goldenRelicRecord(t), aliceSavePack(current))
}

func aliceSavePack(current string) RelicOperationPack {
	return relicFoldPack(2, RelicOperation{ID: current, Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}})
}

func TestRelicMergeFoldsLikeASaveFromTheCurrentVersion(t *testing.T) {
	save, merge, current := relicTestID(9100), relicTestID(9101), relicTestID(9102)
	alice := aliceSavedPartA(t, current)

	// Applied when body.base is the part's current version.
	merged := mustApplyRelic(t, alice, relicStaleSaveThenMerge(3, save, merge, current))
	body := requirePartForTest(t, merged.State.Relic, relicDocPartA).Body
	if body == nil || body.Version != merge || body.Bytes != 40 || body.Format != "md" {
		t.Fatalf("after the merge: body = %#v, want version %s with 40 bytes", body, merge)
	}

	// Ignored when body.base is stale: a version other than Alice's.
	stale, err := ApplyRelic(&alice, relicFoldPack(3,
		RelicOperation{ID: save, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicDocPartA}},
		relicMergeOperation(merge, relicDocPartA, relicTestID(9109), relicDocPartA, save, 40)))
	if err != nil {
		t.Fatalf("a stale merge: %v", err)
	}
	requireRelicEqual(t, "a merge from a stale version", withoutUpdatedAt(stale.State.Relic), withoutUpdatedAt(alice.State.Relic))

	// Delivered twice, it changes nothing the second time: the save and the
	// merge are both stale against the merge's own version.
	again := mustApplyRelic(t, merged, relicStaleSaveThenMerge(4, save, merge, current))
	requireRelicEqual(t, "the merge delivered twice", withoutUpdatedAt(again.State.Relic), withoutUpdatedAt(merged.State.Relic))

	// Applied on a removed part, which stays removed.
	removed := mustApplyRelic(t, alice, relicFoldPack(3,
		RelicOperation{ID: relicTestID(9103), Type: RelicOperationPartRemove, Part: relicDocPartA}))
	mergedRemoved := mustApplyRelic(t, removed, relicStaleSaveThenMerge(4, save, merge, current))
	part := requirePartForTest(t, mergedRemoved.State.Relic, relicDocPartA)
	if !part.Removed || part.Body == nil || part.Body.Version != merge {
		t.Fatalf("a merge on a removed part: part = %#v (body %#v), want removed with version %s", part, part.Body, merge)
	}

	// Ignored on a part the relic does not have.
	unknown := relicTestID(9104)
	missing := mustApplyRelic(t, alice, relicFoldPack(3,
		RelicOperation{ID: save, Type: RelicOperationPartBodySet, Part: unknown, Body: &RelicBodyChange{Format: "md", Bytes: 3, Base: relicTestID(9105)}},
		relicMergeOperation(merge, unknown, relicTestID(9106), relicTestID(9105), save, 4)))
	requireRelicEqual(t, "a merge on a part the relic does not have", withoutUpdatedAt(missing.State.Relic), withoutUpdatedAt(alice.State.Relic))

	// Ignored on a stub, even from the version the stub had.
	_, root := relicCheckpointForTest(RelicBoundaryRetired)
	stub := relicDocPartB
	stubPack := NewRelicOperationPack(relicDocProject, relicDocRelic, relicCheckpointGeneration, relicDocActor, 5,
		relicCheckpointWall.Add(time.Minute), []RelicOperation{
			{ID: save, Type: RelicOperationPartBodySet, Part: stub, Body: &RelicBodyChange{Format: "md", Bytes: 3, Base: relicTestID(9107)}},
			relicMergeOperation(merge, stub, relicTestID(9108), relicTestID(9107), save, 4),
		})
	onStub, err := ApplyRelic(&root, stubPack)
	if err != nil {
		t.Fatalf("a merge on a stub: %v", err)
	}
	if got, want := stateBytesWithoutClock(t, onStub.State), stateBytesWithoutClock(t, withMinReader(root.State, RelicMergeMinReader)); got != want {
		t.Fatalf("a merge on a stub changed the relic:\n got %s\nwant %s", got, want)
	}
}

func TestStaleSaveThenMergeInOnePack(t *testing.T) {
	save, merge, current := relicTestID(9110), relicTestID(9111), relicTestID(9112)
	alice := aliceSavedPartA(t, current)
	pack := relicStaleSaveThenMerge(3, save, merge, current)

	// The save alone, cut from the pack, is stale and changes nothing.
	cut := relicFoldPack(3, pack.Operations[0])
	saved := mustApplyRelic(t, alice, cut)
	if body := requirePartForTest(t, saved.State.Relic, relicDocPartA).Body; body.Version != current {
		t.Fatalf("the stale save alone gave version %s, want %s", body.Version, current)
	}

	merged := mustApplyRelic(t, alice, pack)
	body := requirePartForTest(t, merged.State.Relic, relicDocPartA).Body
	if body.Version != merge || body.Bytes != 40 {
		t.Fatalf("after the pack: body = %#v, want the merge %s", body, merge)
	}
	if !RelicBodySavedBy(&alice, pack, merged, relicDocPartA) {
		t.Fatal("RelicBodySavedBy says the merge pack saved no body")
	}
	applied := RelicBodySavesApplied(historyChain(goldenRelicPack(), aliceSavePack(current), pack))
	if applied[save] || !applied[merge] {
		t.Fatalf("applied = %v, want the save not applied and the merge applied", applied)
	}
}

func withMinReader(state RelicStateDocument, minReader int) RelicStateDocument {
	state.MinReader = minReader
	return state
}
