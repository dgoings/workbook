package core

import (
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// relicMergeOperation is a part.body.merge onto base that merges other in,
// both having started from ancestor.
func relicMergeOperation(id, part, base, ancestor, other string, bytes int64) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationPartBodyMerge, Part: part,
		Body:  &RelicBodyChange{Format: "md", Bytes: bytes, Base: base},
		Merge: &RelicBodyMerge{Ancestor: ancestor, Other: other, Method: RelicLineMergeMethod}}
}

// relicStaleSaveThenMerge is the pack a replay makes of Bob's stale save of
// the golden relic's part A once Alice's save current stands: Bob's save from
// the golden body, then the merge onto current.
func relicStaleSaveThenMerge(clock uint64, save, merge, current string) RelicOperationPack {
	return relicFoldPack(clock,
		RelicOperation{ID: save, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicDocPartA}},
		relicMergeOperation(merge, relicDocPartA, current, relicDocPartA, save, 40))
}

func TestRelicMergeOperationShape(t *testing.T) {
	save, merge, current := relicTestID(9000), relicTestID(9001), relicTestID(9002)
	valid := relicStaleSaveThenMerge(1, save, merge, current)
	encoded, err := EncodeDocument(valid)
	if err != nil {
		t.Fatalf("EncodeDocument(merge pack) = %v", err)
	}
	if !strings.Contains(string(encoded), `"merge":{"ancestor":"`+relicDocPartA+`","other":"`+save+`","method":"line-3way-1"}`) {
		t.Fatalf("the merge member is not stored as the plan shows: %s", encoded)
	}
	decoded, err := DecodeRelicOperationPack(encoded)
	if err != nil {
		t.Fatalf("DecodeRelicOperationPack(merge pack) = %v", err)
	}
	if got := decoded.Operations[1].Merge; got == nil || *got != *valid.Operations[1].Merge {
		t.Fatalf("decoded merge = %#v, want %#v", got, valid.Operations[1].Merge)
	}

	edit := func(change func(*RelicOperation)) RelicOperationPack {
		pack := relicStaleSaveThenMerge(1, save, merge, current)
		operations := append([]RelicOperation(nil), pack.Operations...)
		body, mergeData := *operations[1].Body, *operations[1].Merge
		operations[1].Body, operations[1].Merge = &body, &mergeData
		change(&operations[1])
		return relicFoldPack(1, operations...)
	}
	cases := map[string]RelicOperationPack{
		"no merge":                   edit(func(operation *RelicOperation) { operation.Merge = nil }),
		"no body":                    edit(func(operation *RelicOperation) { operation.Body = nil }),
		"no body base":               edit(func(operation *RelicOperation) { operation.Body.Base = "" }),
		"no part":                    edit(func(operation *RelicOperation) { operation.Part = "" }),
		"an ancestor that is the id": edit(func(operation *RelicOperation) { operation.Merge.Ancestor = merge }),
		"an ancestor that is the base": edit(func(operation *RelicOperation) {
			operation.Merge.Ancestor = current
		}),
		"an ancestor that is no ULID":    edit(func(operation *RelicOperation) { operation.Merge.Ancestor = "01m9a" }),
		"no ancestor":                    edit(func(operation *RelicOperation) { operation.Merge.Ancestor = "" }),
		"a base that is the id":          edit(func(operation *RelicOperation) { operation.Body.Base = merge }),
		"a method of 33 bytes":           edit(func(operation *RelicOperation) { operation.Merge.Method = strings.Repeat("a", 33) }),
		"a method with an underscore":    edit(func(operation *RelicOperation) { operation.Merge.Method = "line_3way" }),
		"a method in capitals":           edit(func(operation *RelicOperation) { operation.Merge.Method = "Line-3way-1" }),
		"no method":                      edit(func(operation *RelicOperation) { operation.Merge.Method = "" }),
		"a merge with a part type":       edit(func(operation *RelicOperation) { operation.PartType = "section" }),
		"a merge naming no other save":   edit(func(operation *RelicOperation) { operation.Merge.Other = "" }),
		"a merge naming itself as other": edit(func(operation *RelicOperation) { operation.Merge.Other = merge }),
	}
	withMergeOnSet := relicStaleSaveThenMerge(1, save, merge, current)
	withMergeOnSet.Operations = append([]RelicOperation(nil), withMergeOnSet.Operations...)
	withMergeOnSet.Operations[0].Merge = &RelicBodyMerge{Ancestor: relicTestID(9003), Other: relicTestID(9004), Method: RelicLineMergeMethod}
	cases["a merge member on part.body.set"] = withMergeOnSet
	for label, pack := range cases {
		requireRelicPackRefused(t, label, pack)
	}

	// The method is a free label of 1 to 32 bytes of [a-z0-9-].
	for _, method := range []string{"a", strings.Repeat("z", 32), "line-3way-2", "0-9"} {
		pack := edit(func(operation *RelicOperation) { operation.Merge.Method = method })
		if _, err := EncodeDocument(pack); err != nil {
			t.Errorf("method %q: EncodeDocument = %v, want it accepted", method, err)
		}
	}
}

func TestRelicMergeMustNameAnEarlierSaveOfThePart(t *testing.T) {
	save, merge, current := relicTestID(9010), relicTestID(9011), relicTestID(9012)
	otherPartSave := relicTestID(9013)
	added := relicTestID(9014)
	saveOf := func(id, part, format string) RelicOperation {
		return RelicOperation{ID: id, Type: RelicOperationPartBodySet, Part: part,
			Body: &RelicBodyChange{Format: format, Bytes: 30, Base: relicTestID(9015)}}
	}
	mergeNaming := func(other string) RelicOperation {
		return relicMergeOperation(merge, relicDocPartA, current, relicDocPartA, other, 40)
	}
	cases := map[string][]RelicOperation{
		"a later save":        {mergeNaming(save), saveOf(save, relicDocPartA, "md")},
		"another part's save": {saveOf(otherPartSave, relicDocPartB, "md"), mergeNaming(otherPartSave)},
		"a part.add": {RelicOperation{ID: added, Type: RelicOperationPartAdd, PartType: "section", Position: "3/1",
			Body: &RelicBodyChange{Format: "md", Bytes: 3}}, mergeNaming(added)},
		"a save with another format": {saveOf(save, relicDocPartA, "txt"), mergeNaming(save)},
		"a save outside the pack":    {mergeNaming(relicTestID(9016))},
	}
	for label, operations := range cases {
		requireRelicPackRefused(t, label, relicFoldPack(2, operations...))
	}

	// An earlier merge of the same part counts as a save it may merge in.
	first, second := relicTestID(9017), relicTestID(9018)
	chained := relicFoldPack(2,
		saveOf(save, relicDocPartA, "md"),
		relicMergeOperation(first, relicDocPartA, current, relicDocPartA, save, 40),
		relicMergeOperation(second, relicDocPartA, relicTestID(9019), current, first, 50))
	if _, err := EncodeDocument(chained); err != nil {
		t.Fatalf("a merge naming an earlier merge of the part: EncodeDocument = %v", err)
	}
}

func TestRelicMergeDeclaresGenerationNine(t *testing.T) {
	save, merge, current := relicTestID(9020), relicTestID(9021), relicTestID(9022)
	record := goldenRelicRecord(t)
	alice := mustApplyRelic(t, record, relicFoldPack(2, RelicOperation{ID: current, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))
	if alice.State.MinReader != RelicMinReader {
		t.Fatalf("an ordinary save declares %d, want %d", alice.State.MinReader, RelicMinReader)
	}

	pack := relicStaleSaveThenMerge(3, save, merge, current)
	if pack.MinReader != 9 || RelicMergeMinReader != 9 {
		t.Fatalf("a merge pack declares %d (RelicMergeMinReader %d), want 9", pack.MinReader, RelicMergeMinReader)
	}
	atEight := pack
	atEight.MinReader = 8
	requireRelicPackRefused(t, "a merge pack marked 8", atEight)
	atSix := pack
	atSix.MinReader = 6
	requireRelicPackRefused(t, "a merge pack marked 6", atSix)

	merged := mustApplyRelic(t, alice, pack)
	if merged.State.MinReader != 9 {
		t.Fatalf("the state folded from a merge declares %d, want 9", merged.State.MinReader)
	}
	encoded, err := EncodeDocument(merged.State)
	if err != nil || !strings.Contains(string(encoded), `"minReader":9,`) {
		t.Fatalf("EncodeDocument(merged state) = %s, %v", encoded, err)
	}
	next := mustApplyRelic(t, merged, relicFoldPack(4, RelicOperation{ID: relicTestID(9023), Type: RelicOperationTitleSet, Title: "After"}))
	if next.State.MinReader != 9 {
		t.Fatalf("a title edit after a merge declares %d, want 9", next.State.MinReader)
	}
	if title := relicFoldPack(4, RelicOperation{ID: relicTestID(9023), Type: RelicOperationTitleSet, Title: "After"}); title.MinReader != RelicMinReader {
		t.Fatalf("the title pack itself declares %d, want %d", title.MinReader, RelicMinReader)
	}

	// A merge beside a retire or a built-in schema still declares the largest.
	mixed := relicFoldPack(3, append(append([]RelicOperation(nil), pack.Operations...),
		RelicOperation{ID: relicTestID(9024), Type: RelicOperationRetire})...)
	if mixed.MinReader != 9 {
		t.Fatalf("a merge and a retire declare %d, want 9", mixed.MinReader)
	}
	migrate := relicFoldPack(3, append(append([]RelicOperation(nil), pack.Operations...),
		RelicOperation{ID: relicTestID(9025), Type: RelicOperationMigrate, RelicType: "spec", Schema: "workbook:spec:1"})...)
	if migrate.MinReader != 9 {
		t.Fatalf("a merge and a built-in migrate declare %d, want 9", migrate.MinReader)
	}
}

func TestRelicGenerationNineMarkers(t *testing.T) {
	if RelicMergeMinReader != 9 || SupportedFormatGeneration < RelicMergeMinReader {
		t.Fatalf("SupportedFormatGeneration = %d, RelicMergeMinReader = %d, want at least 9 and 9",
			SupportedFormatGeneration, RelicMergeMinReader)
	}
	for _, generation := range []int{RelicMinReader, RelicCheckpointMinReader, RelicBuiltInSchemaMinReader, RelicMergeMinReader} {
		if err := validateRelicMarker("relic state", generation, relicDocRelic); err != nil {
			t.Errorf("validateRelicMarker(%d) = %v, want accepted", generation, err)
		}
	}
	for _, generation := range []int{0, 5} {
		if err := validateRelicMarker("relic state", generation, relicDocRelic); CategoryOf(err) != CategoryCorruptData {
			t.Errorf("validateRelicMarker(%d) = %v, want corrupt data", generation, err)
		}
	}
	if err := validateRelicMarker("relic state", SupportedFormatGeneration+1, relicDocRelic); CategoryOf(err) != CategoryNewerWriter {
		t.Errorf("validateRelicMarker(%d) = %v, want newer writer", SupportedFormatGeneration+1, err)
	}

	// A state at nine round-trips.
	state := goldenRelicState()
	state.MinReader = 9
	requireRelicStateRoundTrips(t, "a state at generation 9", state)

	// A pack marked 11 holding an operation this build does not know decodes
	// leniently and every fold refuses it as written by a newer Workbook.
	encodedPack, err := EncodeDocument(relicFoldPack(2, RelicOperation{ID: relicTestID(9030), Type: RelicOperationTitleSet, Title: "T"}))
	if err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(string(encodedPack), `"minReader":6,`, `"minReader":11,`, 1)
	newer = strings.Replace(newer, `]}`+"\n", `,{"id":"`+relicTestID(9031)+`","type":"part.body.rebase","part":"x"}]}`+"\n", 1)
	pack, err := DecodeRelicOperationPack([]byte(newer))
	if err != nil {
		t.Fatalf("DecodeRelicOperationPack(generation 11) = %v", err)
	}
	if !pack.RequiresNewerReader() || pack.MinReader != 11 || len(pack.Operations) != 2 {
		t.Fatalf("the generation-11 pack decoded as %#v", pack)
	}
	record := goldenRelicRecord(t)
	if _, err := ApplyRelic(&record, pack); CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("ApplyRelic(generation 11) = %v, want newer writer", err)
	}
	if _, err := DecodeRelicOperationPack([]byte(strings.Replace(newer, `"minReader":11,`, `"minReader":9,`, 1))); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("the same unknown operation at generation 9 = %v, want corrupt data", err)
	}
}

func TestRelicMergeIDIsDeterministic(t *testing.T) {
	wall := time.Date(2026, 11, 27, 9, 30, 15, 123_456_789, time.UTC)
	relic, part, other, base := relicDocRelic, relicDocPartA, relicTestID(9040), relicTestID(9041)
	id := RelicMergeOperationID(relic, part, other, base, RelicLineMergeMethod, wall)
	if again := RelicMergeOperationID(relic, part, other, base, RelicLineMergeMethod, wall); again != id {
		t.Fatalf("the same inputs gave %s and %s", id, again)
	}
	// The same instant in another zone is the same wall time.
	if zoned := RelicMergeOperationID(relic, part, other, base, RelicLineMergeMethod, wall.In(time.FixedZone("x", 3600))); zoned != id {
		t.Fatalf("the same instant in another zone gave %s, want %s", zoned, id)
	}
	if err := validateCanonicalULID("merge ID", id); err != nil {
		t.Fatalf("RelicMergeOperationID = %q: %v", id, err)
	}
	parsed := ulid.MustParseStrict(id)
	if parsed.Time() != uint64(wall.UnixMilli()) {
		t.Fatalf("the ID's time is %d, want the wall time %d", parsed.Time(), wall.UnixMilli())
	}

	changed := map[string]string{
		"relic":  RelicMergeOperationID("WB-R-"+relicTestID(9042), part, other, base, RelicLineMergeMethod, wall),
		"part":   RelicMergeOperationID(relic, relicDocPartB, other, base, RelicLineMergeMethod, wall),
		"other":  RelicMergeOperationID(relic, part, relicTestID(9043), base, RelicLineMergeMethod, wall),
		"base":   RelicMergeOperationID(relic, part, other, relicTestID(9044), RelicLineMergeMethod, wall),
		"method": RelicMergeOperationID(relic, part, other, base, "line-3way-2", wall),
		"time":   RelicMergeOperationID(relic, part, other, base, RelicLineMergeMethod, wall.Add(time.Millisecond)),
		// The fields are separated, so moving a byte across a boundary is a
		// different input.
		"boundary": RelicMergeOperationID(relic, part, other+base[:1], base[1:], RelicLineMergeMethod, wall),
	}
	seen := map[string]string{id: "the original"}
	for label, changedID := range changed {
		if previous, clash := seen[changedID]; clash {
			t.Errorf("changing the %s gave %s, the ID of %s", label, changedID, previous)
		}
		seen[changedID] = label
	}
	// Only the time field changes with the wall time; the random bits do not.
	later := ulid.MustParseStrict(changed["time"])
	if later.Entropy() == nil || string(later.Entropy()) != string(parsed.Entropy()) {
		t.Errorf("the random bits changed with the wall time: %x and %x", parsed.Entropy(), later.Entropy())
	}
}

func TestRelicMergeTakingAPartIDIsRefused(t *testing.T) {
	save, current := relicTestID(9050), relicTestID(9051)
	alice := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, RelicOperation{ID: current, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))
	_, err := ApplyRelic(&alice, relicStaleSaveThenMerge(3, save, relicDocPartB, current))
	requireCorruptData(t, "a merge taking a part's ID", err)
	if err != nil && !strings.Contains(err.Error(), "part.body.merge") {
		t.Errorf("the refusal %q does not name part.body.merge", err)
	}
	_, err = ApplyRelic(&alice, relicStaleSaveThenMerge(3, save, relicDocPartA, current))
	requireCorruptData(t, "a merge taking the ID of the part it merges", err)
}
