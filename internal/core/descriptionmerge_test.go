package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	descriptionMergeSetID   = "01K0M6B8A4FTT8C39MXXYTW7D1"
	descriptionMergeID      = "01K0M6B8A4FTT8C39MXXYTW7D2"
	descriptionMergeLaterID = "01K0M6B8A4FTT8C39MXXYTW7D3"
	descriptionMergeTitleID = "01K0M6B8A4FTT8C39MXXYTW7D4"
	descriptionMergeAbsent  = "01K0M6B8A4FTT8C39MXXYTW7D5"
)

// describedTask is a created task whose description is three lines, the
// ancestor every merge test starts from.
func describedTask(t *testing.T) StateDocument {
	t.Helper()
	state := createdTask(t)
	next, err := Apply(&state, NewOperationPack(projectID, taskID, generationID, "developer@example.com",
		state.LogicalClock+1, updatedAt, []Operation{{ID: operationID2, Type: OperationFieldSet,
			Field: "description", Value: "one\ntwo\nthree\n"}}))
	if err != nil {
		t.Fatalf("Apply(description) error = %v", err)
	}
	return next
}

// descriptionMergeOperation is a valid description.merge of other, with the
// texts of the replay the merge tests imagine: Bob changed line three here,
// Alice changed line two on the remote.
func descriptionMergeOperation(id, other string) Operation {
	return Operation{
		ID:    id,
		Type:  OperationDescriptionMerge,
		Value: "one\nTWO\nTHREE\n",
		Merge: &DescriptionMerge{
			Other:    other,
			Ancestor: "one\ntwo\nthree\n",
			Current:  "one\nTWO\nthree\n",
			Method:   DescriptionLineMergeMethod,
		},
	}
}

// descriptionMergePack is the pack a replay writes: the local edit, then the
// merge that names it, at the generation the operations declare.
func descriptionMergePack(state StateDocument, operations ...Operation) OperationPack {
	if len(operations) == 0 {
		operations = []Operation{
			{ID: descriptionMergeSetID, Type: OperationFieldSet, Field: "description", Value: "one\ntwo\nTHREE\n"},
			descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID),
		}
	}
	return NewOperationPack(projectID, taskID, generationID, "developer@example.com",
		state.LogicalClock+1, updatedAt, operations)
}

// forgedPackBytes marshals a pack without EncodeDocument's validation, the
// way a hand-written or broken writer would store it.
func forgedPackBytes(t *testing.T, pack OperationPack) []byte {
	t.Helper()
	encoded, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

// requireCorruptMergePack checks that both the fold and the strict decode
// refuse a pack as corrupt data.
func requireCorruptMergePack(t *testing.T, label string, state StateDocument, pack OperationPack) {
	t.Helper()
	if _, err := Apply(&state, pack); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("%s: Apply = %v, want corrupt data", label, err)
	}
	if _, err := DecodeOperationPack(forgedPackBytes(t, pack)); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("%s: DecodeOperationPack = %v, want corrupt data", label, err)
	}
}

func TestDescriptionMergeOperationShape(t *testing.T) {
	state := describedTask(t)
	pack := descriptionMergePack(state)
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(merge pack) = %v", err)
	}
	const wantMerge = `{"id":"` + descriptionMergeID + `","type":"description.merge","value":"one\nTWO\nTHREE\n",` +
		`"merge":{"other":"` + descriptionMergeSetID + `","ancestor":"one\ntwo\nthree\n","current":"one\nTWO\nthree\n","method":"line-3way-1"}}]}`
	if !strings.Contains(string(encoded), `"version":1,"minReader":10,`) || !strings.HasSuffix(string(encoded), wantMerge+"\n") {
		t.Fatalf("the merge pack encodes as %s", encoded)
	}
	decoded, err := DecodeOperationPack(encoded)
	if err != nil {
		t.Fatalf("DecodeOperationPack(merge pack) = %v", err)
	}
	if got := decoded.Operations[1]; got.Merge == nil || *got.Merge != *pack.Operations[1].Merge ||
		got.Value != pack.Operations[1].Value || got.Type != OperationDescriptionMerge {
		t.Fatalf("the merge decoded as %#v", decoded.Operations[1])
	}
	next, err := Apply(&state, decoded)
	if err != nil {
		t.Fatalf("Apply(merge pack) = %v", err)
	}
	if next.Task.Description != "one\nTWO\nTHREE\n" {
		t.Fatalf("description = %q, want the merged text", next.Task.Description)
	}

	// An empty merged text is a text like any other.
	empty := descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID)
	empty.Value = ""
	emptyPack := descriptionMergePack(state, pack.Operations[0], empty)
	encodedEmpty, err := EncodeDocument(emptyPack)
	if err != nil {
		t.Fatalf("EncodeDocument(empty merge) = %v", err)
	}
	if _, err := DecodeOperationPack(encodedEmpty); err != nil {
		t.Fatalf("DecodeOperationPack(empty merge) = %v", err)
	}

	oversized := strings.Repeat("x", MaxDescriptionBytes+1)
	for _, test := range []struct {
		name   string
		mutate func(*Operation)
	}{
		{"no merge", func(operation *Operation) { operation.Merge = nil }},
		{"a field", func(operation *Operation) { operation.Field = "description" }},
		{"a task", func(operation *Operation) { operation.Task = &TaskData{Title: "x"} }},
		{"a comment body", func(operation *Operation) { operation.Body = "looks good" }},
		{"a comment ID", func(operation *Operation) { operation.CommentID = descriptionMergeAbsent }},
		{"an attachment ID", func(operation *Operation) { operation.AttachmentID = descriptionMergeAbsent }},
		{"a checkpoint", func(operation *Operation) { operation.Checkpoint = &CheckpointData{} }},
		{"an other that is not a ULID", func(operation *Operation) { operation.Merge.Other = "bob's edit" }},
		{"a lowercase other", func(operation *Operation) { operation.Merge.Other = strings.ToLower(descriptionMergeSetID) }},
		{"a method of 33 bytes", func(operation *Operation) { operation.Merge.Method = strings.Repeat("a", 33) }},
		{"a method with _", func(operation *Operation) { operation.Merge.Method = "line_3way" }},
		{"an empty method", func(operation *Operation) { operation.Merge.Method = "" }},
		{"an oversized ancestor", func(operation *Operation) { operation.Merge.Ancestor = oversized }},
		{"an oversized current", func(operation *Operation) { operation.Merge.Current = oversized }},
		{"an oversized value", func(operation *Operation) { operation.Value = oversized }},
	} {
		merge := descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID)
		copied := *merge.Merge
		merge.Merge = &copied
		test.mutate(&merge)
		requireCorruptMergePack(t, test.name, state, descriptionMergePack(state, pack.Operations[0], merge))
	}

	// A method label of 32 bytes is accepted.
	merge := descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID)
	merge.Merge = &DescriptionMerge{Other: descriptionMergeSetID, Method: strings.Repeat("a", 32)}
	if _, err := Apply(&state, descriptionMergePack(state, pack.Operations[0], merge)); err != nil {
		t.Errorf("a method of 32 bytes = %v, want accepted", err)
	}

	// A merge member belongs to description.merge alone.
	set := pack.Operations[0]
	set.Merge = &DescriptionMerge{Other: descriptionMergeAbsent, Method: DescriptionLineMergeMethod}
	requireCorruptMergePack(t, "a merge member on field.set", state, descriptionMergePack(state, set))
	comment := Operation{ID: descriptionMergeAbsent, Type: OperationCommentAdd, Body: "hi",
		Merge: &DescriptionMerge{Other: descriptionMergeSetID, Method: DescriptionLineMergeMethod}}
	requireCorruptMergePack(t, "a merge member on comment.add", state, descriptionMergePack(state, comment))
}

func TestDescriptionMergeMustNameAnEarlierDescriptionEdit(t *testing.T) {
	state := describedTask(t)
	set := Operation{ID: descriptionMergeSetID, Type: OperationFieldSet, Field: "description", Value: "one\ntwo\nTHREE\n"}
	title := Operation{ID: descriptionMergeTitleID, Type: OperationFieldSet, Field: "title", Value: "Renamed"}

	requireCorruptMergePack(t, "other names a later operation", state, descriptionMergePack(state,
		descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID), set))
	requireCorruptMergePack(t, "other names a field.set of the title", state, descriptionMergePack(state,
		set, title, descriptionMergeOperation(descriptionMergeID, descriptionMergeTitleID)))
	requireCorruptMergePack(t, "other names an operation outside the pack", state, descriptionMergePack(state,
		set, descriptionMergeOperation(descriptionMergeID, descriptionMergeAbsent)))
	requireCorruptMergePack(t, "other names the merge itself", state, descriptionMergePack(state,
		set, descriptionMergeOperation(descriptionMergeID, descriptionMergeID)))
	requireCorruptMergePack(t, "a merge with no description edit before it", state, descriptionMergePack(state,
		title, descriptionMergeOperation(descriptionMergeID, descriptionMergeTitleID)))

	// A re-replay merges again onto the first merge: [field.set, M1, M2].
	second := descriptionMergeOperation(descriptionMergeLaterID, descriptionMergeID)
	second.Value = "ONE\nTWO\nTHREE\n"
	pack := descriptionMergePack(state, set, descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID), second)
	next, err := Apply(&state, pack)
	if err != nil {
		t.Fatalf("a merge naming an earlier merge = %v, want accepted", err)
	}
	if next.Task.Description != "ONE\nTWO\nTHREE\n" {
		t.Fatalf("description = %q, want the second merge's text", next.Task.Description)
	}
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument([set, M1, M2]) = %v", err)
	}
	if _, err := DecodeOperationPack(encoded); err != nil {
		t.Fatalf("DecodeOperationPack([set, M1, M2]) = %v", err)
	}

	// Other fields of the pack may sit anywhere in it.
	withTitle := descriptionMergePack(state, set, title, descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID))
	next, err = Apply(&state, withTitle)
	if err != nil || next.Task.Title != "Renamed" || next.Task.Description != "one\nTWO\nTHREE\n" {
		t.Fatalf("a merge beside a title edit = %#v, %v", next.Task, err)
	}
}

func TestDescriptionMergeDeclaresGenerationTen(t *testing.T) {
	if DescriptionMergeMinReader != 10 {
		t.Fatalf("DescriptionMergeMinReader = %d, want 10", DescriptionMergeMinReader)
	}
	state := describedTask(t)
	pack := descriptionMergePack(state)
	if got := PackMinReader(pack.Operations); got != 10 || pack.MinReader != 10 {
		t.Fatalf("PackMinReader(merge pack) = %d, pack.MinReader = %d, want 10", got, pack.MinReader)
	}
	if got := OperationMinReader(pack.Operations[0]); got != 0 {
		t.Fatalf("the field.set of the description declares %d, want 0", got)
	}

	for _, below := range []int{0, 5, 9} {
		marked := pack
		marked.MinReader = below
		requireCorruptMergePack(t, "a merge pack marked "+itoa(below), state, marked)
	}

	merged, err := Apply(&state, pack)
	if err != nil {
		t.Fatal(err)
	}
	if merged.MinReader != 10 {
		t.Fatalf("the state after a merge is at %d, want 10", merged.MinReader)
	}
	encodedState, err := EncodeDocument(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedState), `"minReader":10,`) {
		t.Fatalf("the merged state encodes as %s", encodedState)
	}
	if _, err := DecodeStateDocument(encodedState); err != nil {
		t.Fatalf("DecodeStateDocument(merged state) = %v", err)
	}

	title := NewOperationPack(projectID, taskID, generationID, "developer@example.com", merged.LogicalClock+1, updatedAt,
		[]Operation{{ID: descriptionMergeTitleID, Type: OperationFieldSet, Field: "title", Value: "Renamed"}})
	if title.MinReader != 0 {
		t.Fatalf("a title edit declares %d, want 0", title.MinReader)
	}
	after, err := Apply(&merged, title)
	if err != nil {
		t.Fatal(err)
	}
	if after.MinReader != 10 {
		t.Fatalf("the state after a merge and a title edit is at %d, want 10", after.MinReader)
	}
}

func TestGenerationTenMarkers(t *testing.T) {
	if SupportedFormatGeneration != 10 || DescriptionMergeMinReader != SupportedFormatGeneration {
		t.Fatalf("SupportedFormatGeneration = %d, DescriptionMergeMinReader = %d, want 10 and 10",
			SupportedFormatGeneration, DescriptionMergeMinReader)
	}

	// A task pack marked 11 holding a member this build does not know decodes
	// leniently and every fold refuses it as written by a newer Workbook.
	parent, err := DecodeStateDocument([]byte(goldenTaskRefs[1].parent))
	if err != nil {
		t.Fatal(err)
	}
	newer := newerWriterPack(t, 11, "")
	newer = strings.Replace(newer, `"value":"in-review"`, `"value":"in-review","reason":"sentence-merge"`, 1)
	pack, err := DecodeOperationPack([]byte(newer))
	if err != nil {
		t.Fatalf("DecodeOperationPack(generation 11) = %v", err)
	}
	if !pack.RequiresNewerReader() || pack.MinReader != 11 {
		t.Fatalf("the generation-11 pack decoded as %#v", pack)
	}
	if _, err := Apply(&parent, pack); CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("Apply(generation 11) = %v, want newer writer", err)
	}
	if _, err := DecodeOperationPack([]byte(strings.Replace(newer, `"minReader":11,`, `"minReader":10,`, 1))); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("the same unknown member at generation 10 = %v, want corrupt data", err)
	}

	// Generation ten marks task packs only: a relic pack or state marked ten
	// is no relic generation, and is corrupt.
	encodedRelic, err := EncodeDocument(relicFoldPack(2, RelicOperation{ID: relicTestID(9530), Type: RelicOperationTitleSet, Title: "T"}))
	if err != nil {
		t.Fatal(err)
	}
	relicTen := strings.Replace(string(encodedRelic), `"minReader":6,`, `"minReader":10,`, 1)
	if relicTen == string(encodedRelic) {
		t.Fatal("the relic fixture substitution matched nothing")
	}
	if _, err := DecodeRelicOperationPack([]byte(relicTen)); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("a relic pack marked 10 = %v, want corrupt data", err)
	}
	if err := validateRelicMarker("relic state", 10, relicDocRelic); CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("validateRelicMarker(10) = %v, want corrupt data", err)
	}
	if err := validateRelicMarker("relic state", SupportedFormatGeneration+1, relicDocRelic); CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("validateRelicMarker(11) = %v, want newer writer", err)
	}
}

func TestDescriptionMergeFoldsTheStoredText(t *testing.T) {
	state := describedTask(t)
	pack := descriptionMergePack(state)
	first, err := Apply(&state, pack)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.Description != pack.Operations[1].Value {
		t.Fatalf("description = %q, want the merge's value %q", first.Task.Description, pack.Operations[1].Value)
	}
	again, err := Apply(&state, pack)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := EncodeDocument(first)
	if err != nil {
		t.Fatal(err)
	}
	againBytes, err := EncodeDocument(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(againBytes) {
		t.Fatalf("the same pack folded twice gave\n%s\n%s", firstBytes, againBytes)
	}

	// The fold never re-merges: stored texts that disagree with the parent
	// still fold to the stored value.
	disagreeing := descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID)
	disagreeing.Merge = &DescriptionMerge{Other: descriptionMergeSetID, Ancestor: "something else\n",
		Current: "and another\n", Method: DescriptionLineMergeMethod}
	disagreeing.Value = "the stored text\n"
	next, err := Apply(&state, descriptionMergePack(state, pack.Operations[0], disagreeing))
	if err != nil {
		t.Fatal(err)
	}
	if next.Task.Description != "the stored text\n" {
		t.Fatalf("description = %q, want the stored value", next.Task.Description)
	}
}

func TestDescriptionMergeIDIsDeterministic(t *testing.T) {
	wall := time.Date(2026, 11, 28, 9, 30, 15, 123_456_789, time.UTC)
	other, ancestor, current, method := descriptionMergeSetID, "one\ntwo\n", "one\nTWO\n", DescriptionLineMergeMethod
	id := DescriptionMergeOperationID(taskID, other, ancestor, current, method, wall)
	if again := DescriptionMergeOperationID(taskID, other, ancestor, current, method, wall); again != id {
		t.Fatalf("the same inputs gave %s and %s", id, again)
	}
	if zoned := DescriptionMergeOperationID(taskID, other, ancestor, current, method, wall.In(time.FixedZone("x", 3600))); zoned != id {
		t.Fatalf("the same instant in another zone gave %s, want %s", zoned, id)
	}
	if err := validateCanonicalULID("merge ID", id); err != nil {
		t.Fatalf("DescriptionMergeOperationID = %q: %v", id, err)
	}
	parsed := ulid.MustParseStrict(id)
	if parsed.Time() != uint64(wall.UnixMilli()) {
		t.Fatalf("the ID's time is %d, want the wall time %d", parsed.Time(), wall.UnixMilli())
	}

	seen := map[string]string{id: "the original"}
	for _, test := range []struct {
		name                                   string
		task, other, ancestor, current, method string
	}{
		{"another task", "WB-01K0M6B8A4FTT8C39MXXYTW7C9", other, ancestor, current, method},
		{"another other", taskID, descriptionMergeAbsent, ancestor, current, method},
		{"another ancestor", taskID, other, "one\n", current, method},
		{"another current", taskID, other, ancestor, "ONE\nTWO\n", method},
		{"another method", taskID, other, ancestor, current, "line-3way-2"},
		{"a NUL moved from the ancestor to the current", taskID, other, "a\x00", "b", method},
		{"a NUL moved to the start of the current", taskID, other, "a", "\x00b", method},
		{"ancestor and current swapped", taskID, other, current, ancestor, method},
		// The fields are separated, so moving a byte across a boundary is a
		// different input; and the texts are hashed first, so a NUL inside a
		// text cannot pose as a separator.
		{"a byte moved from the other to the task", taskID + other[:1], other[1:], ancestor, current, method},
	} {
		changed := DescriptionMergeOperationID(test.task, test.other, test.ancestor, test.current, test.method, wall)
		if previous, duplicate := seen[changed]; duplicate {
			t.Errorf("%s gave the same ID as %s: %s", test.name, previous, changed)
		}
		seen[changed] = test.name
		if ulid.MustParseStrict(changed).Time() != uint64(wall.UnixMilli()) {
			t.Errorf("%s: the ID's time is not the wall time", test.name)
		}
	}
	if later := DescriptionMergeOperationID(taskID, other, ancestor, current, method, wall.Add(time.Millisecond)); later == id {
		t.Fatal("another wall time gave the same ID")
	} else if parsedLater := ulid.MustParseStrict(later); string(parsedLater.Entropy()) != string(parsed.Entropy()) {
		t.Fatal("the random bits depend on the wall time")
	}
}

func TestPlanDescriptionReplayTable(t *testing.T) {
	described := func(text string) TaskData { return TaskData{Title: "Task", Description: text} }
	set := Operation{ID: descriptionMergeSetID, Type: OperationFieldSet, Field: "description", Value: "B"}
	title := Operation{ID: descriptionMergeTitleID, Type: OperationFieldSet, Field: "title", Value: "Renamed"}
	merge := descriptionMergeOperation(descriptionMergeID, descriptionMergeSetID)
	pack := func(operations ...Operation) OperationPack {
		return OperationPack{TaskID: taskID, Operations: operations}
	}
	for _, row := range []struct {
		name                       string
		sourceParent, source, onto string
		pack                       OperationPack
		want                       DescriptionReplay
	}{
		{name: "origin left the text alone", sourceParent: "A", source: "B", onto: "A", pack: pack(set)},
		{name: "both sides hold the same text", sourceParent: "A", source: "B", onto: "B", pack: pack(set)},
		{name: "no description operation", sourceParent: "A", source: "A", onto: "C", pack: pack(title)},
		{
			name: "both sides changed it", sourceParent: "A", source: "B", onto: "C", pack: pack(title, set),
			want: DescriptionReplay{Merge: true, Ancestor: "A", Ours: "B", Theirs: "C"},
		},
		{
			name: "a pack merged before", sourceParent: "A", source: "M", onto: "C", pack: pack(set, merge),
			want: DescriptionReplay{Merge: true, Ancestor: "A", Ours: "M", Theirs: "C"},
		},
		{
			name: "a pack holding only a merge", sourceParent: "A", source: "M", onto: "C", pack: pack(merge),
			want: DescriptionReplay{Merge: true, Ancestor: "A", Ours: "M", Theirs: "C"},
		},
		{name: "a merged pack origin already matches", sourceParent: "A", source: "M", onto: "M", pack: pack(set, merge)},
	} {
		got := PlanDescriptionReplay(described(row.sourceParent), described(row.source), described(row.onto), row.pack)
		if got != row.want {
			t.Errorf("%s: PlanDescriptionReplay = %+v, want %+v", row.name, got, row.want)
		}
	}
}

func TestMergeDescriptionRefusals(t *testing.T) {
	merged, refusal := MergeDescription("one\ntwo\nthree\nfour\n", "one\nTWO\nthree\nfour\n", "one\ntwo\nthree\nFOUR\n")
	if refusal != "" || merged != "one\nTWO\nthree\nFOUR\n" {
		t.Fatalf("clean merge = %q, %q; want the two edits combined", merged, refusal)
	}
	// An empty ancestor is ordinary text: two additions meet at line 0.
	if merged, refusal := MergeDescription("", "first\n", "second\n"); refusal != DescriptionMergeOverlap || merged != "" {
		t.Errorf("two additions to an empty description = %q, %q; want an overlap", merged, refusal)
	}

	many := strings.Repeat("x\n", 3000)
	long := strings.Repeat("a", MaxDescriptionBytes/2+16)
	for _, row := range []struct {
		name                     string
		ancestor, current, other string
		want                     DescriptionMergeRefusal
	}{
		{name: "same line", ancestor: "one\ntwo\n", current: "one\nTWO\n", other: "one\n2\n", want: DescriptionMergeOverlap},
		{name: "touching lines", ancestor: "one\ntwo\n", current: "ONE\ntwo\n", other: "one\nTWO\n", want: DescriptionMergeOverlap},
		{name: "too many changes", ancestor: many, current: strings.Repeat("y\n", 3000), other: many + "z\n", want: DescriptionMergeTooManyChanges},
		{name: "too large", ancestor: "a\nb\nc\n", current: long + "\nb\nc\n", other: "a\nb\n" + long + "\n", want: DescriptionMergeTooLarge},
		{name: "ancestor not text", ancestor: "one\n\xff\nthree\n", current: "ONE\n\xff\nthree\n", other: "one\n\xff\nTHREE\n", want: DescriptionMergeNotText},
		{name: "current not text", ancestor: "one\ntwo\nthree\n", current: "\xff\ntwo\nthree\n", other: "one\ntwo\nTHREE\n", want: DescriptionMergeNotText},
		{name: "other not text", ancestor: "one\ntwo\nthree\n", current: "ONE\ntwo\nthree\n", other: "one\ntwo\n\xff\n", want: DescriptionMergeNotText},
	} {
		merged, refusal := MergeDescription(row.ancestor, row.current, row.other)
		if refusal != row.want || merged != "" {
			t.Errorf("%s: MergeDescription = %d bytes, %q; want no text and %q", row.name, len(merged), refusal, row.want)
		}
	}
	for _, refusal := range []DescriptionMergeRefusal{
		DescriptionMergeOverlap, DescriptionMergeTooManyChanges, DescriptionMergeTooLarge, DescriptionMergeNotText,
	} {
		if refusal == "" {
			t.Errorf("a refusal is empty")
		}
	}
	if string(DescriptionMergeOverlap) != "overlap" || string(DescriptionMergeTooManyChanges) != "too-many-changes" ||
		string(DescriptionMergeTooLarge) != "too-large" || string(DescriptionMergeNotText) != "not-text" {
		t.Errorf("refusal values = %q, %q, %q, %q", DescriptionMergeOverlap, DescriptionMergeTooManyChanges,
			DescriptionMergeTooLarge, DescriptionMergeNotText)
	}
}

func TestAppendDescriptionMergeFoldsAtTheNextClock(t *testing.T) {
	ancestor, current := "one\ntwo\nthree\n", "one\nTWO\nthree\n"
	base := describedTask(t)
	// The replay parent: origin changed line two.
	parent, err := Apply(&base, NewOperationPack(projectID, taskID, generationID, "teammate@example.com",
		base.LogicalClock+1, updatedAt, []Operation{{ID: descriptionMergeAbsent, Type: OperationFieldSet,
			Field: "description", Value: current}}))
	if err != nil {
		t.Fatal(err)
	}
	wall := updatedAt.Add(time.Minute)
	local := NewOperationPack(projectID, taskID, generationID, "developer@example.com", base.LogicalClock+1, wall,
		[]Operation{
			{ID: descriptionMergeSetID, Type: OperationFieldSet, Field: "description", Value: "one\ntwo\nTHREE\n"},
			{ID: descriptionMergeTitleID, Type: OperationFieldSet, Field: "title", Value: "Renamed"},
		})
	if local.MinReader != 0 {
		t.Fatalf("local pack minReader = %d, want 0 before the merge", local.MinReader)
	}

	pack, state, err := AppendDescriptionMerge(parent, local, ancestor, current, "one\nTWO\nTHREE\n")
	if err != nil {
		t.Fatalf("AppendDescriptionMerge() error = %v", err)
	}
	if pack.MinReader != DescriptionMergeMinReader || state.MinReader != DescriptionMergeMinReader {
		t.Errorf("minReader = pack %d, state %d; want %d", pack.MinReader, state.MinReader, DescriptionMergeMinReader)
	}
	if pack.LogicalClock != parent.LogicalClock+1 || state.LogicalClock != parent.LogicalClock+1 {
		t.Errorf("clock = pack %d, state %d; want %d", pack.LogicalClock, state.LogicalClock, parent.LogicalClock+1)
	}
	if len(pack.Operations) != 3 || len(local.Operations) != 2 {
		t.Fatalf("operations = %d (input left with %d), want 3 and the input untouched", len(pack.Operations), len(local.Operations))
	}
	want := Operation{
		ID:    DescriptionMergeOperationID(taskID, descriptionMergeSetID, ancestor, current, DescriptionLineMergeMethod, wall),
		Type:  OperationDescriptionMerge,
		Value: "one\nTWO\nTHREE\n",
		Merge: &DescriptionMerge{Other: descriptionMergeSetID, Ancestor: ancestor, Current: current, Method: DescriptionLineMergeMethod},
	}
	got := pack.Operations[2]
	if got.ID != want.ID || got.Type != want.Type || got.Value != want.Value || got.Merge == nil || *got.Merge != *want.Merge {
		t.Fatalf("merge = %+v %+v, want %+v %+v", got, got.Merge, want, want.Merge)
	}
	if state.Task.Description != "one\nTWO\nTHREE\n" || state.Task.Title != "Renamed" {
		t.Errorf("state = %q / %q, want the merged text and the title", state.Task.Title, state.Task.Description)
	}
	if _, err := EncodeDocument(pack); err != nil {
		t.Errorf("EncodeDocument(merged pack) error = %v", err)
	}

	// A pack merged before gains a second merge naming the first.
	again, err := Apply(&parent, NewOperationPack(projectID, taskID, generationID, "carol@example.com",
		parent.LogicalClock+1, updatedAt, []Operation{{ID: descriptionMergeLaterID, Type: OperationFieldSet,
			Field: "description", Value: "ONE\nTWO\nthree\n"}}))
	if err != nil {
		t.Fatal(err)
	}
	repacked, restate, err := AppendDescriptionMerge(again, pack, current, "ONE\nTWO\nthree\n", "ONE\nTWO\nTHREE\n")
	if err != nil {
		t.Fatalf("AppendDescriptionMerge(merged pack) error = %v", err)
	}
	if len(repacked.Operations) != 4 || repacked.Operations[3].Merge.Other != got.ID {
		t.Fatalf("second merge = %+v, want a fourth operation naming %s", repacked.Operations, got.ID)
	}
	if restate.Task.Description != "ONE\nTWO\nTHREE\n" || restate.LogicalClock != again.LogicalClock+1 {
		t.Errorf("state = %q at %d, want the second merge's text at %d", restate.Task.Description,
			restate.LogicalClock, again.LogicalClock+1)
	}

	if _, _, err := AppendDescriptionMerge(parent, NewOperationPack(projectID, taskID, generationID, "developer@example.com",
		parent.LogicalClock+1, wall, []Operation{{ID: descriptionMergeTitleID, Type: OperationFieldSet,
			Field: "title", Value: "Renamed"}}), ancestor, current, "x\n"); err == nil {
		t.Errorf("AppendDescriptionMerge(pack without a description edit) error = nil")
	}
}

func TestDescriptionMergeBoundsArePinned(t *testing.T) {
	want := LineMergeLimits{EditLines: maxRelicDiffEditLines, Work: maxLineMergeWork, Bytes: MaxDescriptionBytes}
	if DescriptionLineMergeLimits != want {
		t.Errorf("DescriptionLineMergeLimits = %+v, want %+v", DescriptionLineMergeLimits, want)
	}
	if DescriptionLineMergeLimits.EditLines != 2000 || DescriptionLineMergeLimits.Work != 1<<26 ||
		DescriptionLineMergeLimits.Bytes != 64<<10 {
		t.Errorf("DescriptionLineMergeLimits = %+v, want 2,000 edit lines, 1<<26 work and 64 KiB: "+
			"each may be raised, never lowered", DescriptionLineMergeLimits)
	}
}

func TestDescriptionMergedWarning(t *testing.T) {
	got := DescriptionMergedWarning("WB-1", "")
	want := Warning{
		Code: "description-merged",
		Message: "task WB-1: the description was changed here and on origin; the two edits changed different lines " +
			"and were merged; `workbook show WB-1 --history` shows the merge",
	}
	if got != want || WarningDescriptionMerged != "description-merged" {
		t.Errorf("DescriptionMergedWarning = %+v, want %+v", got, want)
	}
	if got := DescriptionMergedWarning("WB-1", "upstream").Message; !strings.Contains(got, " on upstream;") {
		t.Errorf("warning for remote upstream = %q, want it named", got)
	}
}
