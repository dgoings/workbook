package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var relicDocTime = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

var (
	relicDocProject    = relicTestID(1)
	relicDocRelic      = "WB-R-" + relicTestID(2)
	relicDocGeneration = relicTestID(3)
	relicDocCreateOp   = relicTestID(4)
	relicDocPropertyOp = relicTestID(5)
	relicDocPartA      = relicTestID(6)
	relicDocPartB      = relicTestID(7)
)

const relicDocActor = "dylan@example.com"

func goldenRelicPack() RelicOperationPack {
	return NewRelicOperationPack(relicDocProject, relicDocRelic, relicDocGeneration, relicDocActor, 1, relicDocTime,
		[]RelicOperation{
			{ID: relicDocCreateOp, Type: RelicOperationCreate, Relic: &RelicCreateData{
				Title: "Task archive compaction", Type: "spec", CreatedAt: relicDocTime,
			}},
			{ID: relicDocPropertyOp, Type: RelicOperationPropertySet, Name: "status", Value: json.RawMessage(`"approved"`)},
			{ID: relicDocPartA, Type: RelicOperationPartAdd, PartType: "section", Position: "1/1",
				Body: &RelicBodyChange{Format: "md", Bytes: 1840}},
			{ID: relicDocPartB, Type: RelicOperationPartAdd, PartType: "decision", Position: "2/1"},
		})
}

func goldenRelicState() RelicStateDocument {
	return RelicStateDocument{
		Format:       "workbook.relic-state",
		Version:      1,
		MinReader:    RelicMinReader,
		ProjectID:    relicDocProject,
		RelicID:      relicDocRelic,
		History:      History{Generation: relicDocGeneration},
		LogicalClock: 1,
		Relic: RelicData{
			Title:      "Task archive compaction",
			Type:       "spec",
			Author:     relicDocActor,
			CreatedAt:  relicDocTime,
			UpdatedAt:  relicDocTime,
			Properties: map[string]json.RawMessage{"status": json.RawMessage(`"approved"`)},
			Parts: []RelicPart{
				{ID: relicDocPartA, Type: "section", Position: "1/1",
					Body: &RelicBody{Format: "md", Version: relicDocPartA, Bytes: 1840}},
				{ID: relicDocPartB, Type: "decision", Position: "2/1"},
			},
		},
	}
}

// marshalRelicDocument is the bytes of a document without EncodeDocument's
// validation, standing in for a document some other writer stored.
func marshalRelicDocument(t *testing.T, document any) []byte {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return append(encoded, '\n')
}

func requireCorruptData(t *testing.T, label string, err error) {
	t.Helper()
	if CategoryOf(err) != CategoryCorruptData {
		t.Errorf("%s: error = %v, want corrupt data", label, err)
	}
}

// requireRelicPackRefused checks that a pack is refused when saved and when
// read back from storage.
func requireRelicPackRefused(t *testing.T, label string, pack RelicOperationPack) {
	t.Helper()
	_, err := EncodeDocument(pack)
	requireCorruptData(t, label+" (encode)", err)
	_, err = DecodeRelicOperationPack(marshalRelicDocument(t, pack))
	requireCorruptData(t, label+" (decode)", err)
}

func requireRelicStateRefused(t *testing.T, label string, state RelicStateDocument) {
	t.Helper()
	_, err := EncodeDocument(state)
	requireCorruptData(t, label+" (encode)", err)
	_, err = DecodeRelicStateDocument(marshalRelicDocument(t, state))
	requireCorruptData(t, label+" (decode)", err)
}

func requireRelicStateRoundTrips(t *testing.T, label string, state RelicStateDocument) {
	t.Helper()
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Errorf("%s: EncodeDocument: %v", label, err)
		return
	}
	decoded, err := DecodeRelicStateDocument(encoded)
	if err != nil {
		t.Errorf("%s: DecodeRelicStateDocument: %v", label, err)
		return
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Errorf("%s: decoded state differs from the encoded one", label)
	}
}

func TestRelicDocumentsEncodeToGoldenBytes(t *testing.T) {
	wantPack := `{"format":"workbook.relic-operation-pack","version":1,"minReader":6,"projectId":"` + relicDocProject +
		`","relicId":"` + relicDocRelic + `","historyGeneration":"` + relicDocGeneration +
		`","actor":{"id":"dylan@example.com"},"logicalClock":1,"wallTime":"2026-11-23T10:00:00Z","operations":[` +
		`{"id":"` + relicDocCreateOp + `","type":"relic.create","relic":{"title":"Task archive compaction","type":"spec","createdAt":"2026-11-23T10:00:00Z"}},` +
		`{"id":"` + relicDocPropertyOp + `","type":"property.set","name":"status","value":"approved"},` +
		`{"id":"` + relicDocPartA + `","type":"part.add","partType":"section","position":"1/1","body":{"format":"md","bytes":1840}},` +
		`{"id":"` + relicDocPartB + `","type":"part.add","partType":"decision","position":"2/1"}]}` + "\n"
	pack := goldenRelicPack()
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(pack): %v", err)
	}
	if string(encoded) != wantPack {
		t.Fatalf("pack encoding:\n got %s\nwant %s", encoded, wantPack)
	}
	decodedPack, err := DecodeRelicOperationPack(encoded)
	if err != nil || !reflect.DeepEqual(decodedPack, pack) {
		t.Fatalf("DecodeRelicOperationPack = %#v, %v", decodedPack, err)
	}

	wantState := `{"format":"workbook.relic-state","version":1,"minReader":6,"projectId":"` + relicDocProject +
		`","relicId":"` + relicDocRelic + `","history":{"generation":"` + relicDocGeneration + `","compactedFrom":null},` +
		`"logicalClock":1,"relic":{"title":"Task archive compaction","type":"spec","schema":null,"author":"dylan@example.com",` +
		`"retired":false,"deleted":false,"createdAt":"2026-11-23T10:00:00Z","updatedAt":"2026-11-23T10:00:00Z",` +
		`"properties":{"status":"approved"},"parts":[` +
		`{"id":"` + relicDocPartA + `","type":"section","position":"1/1","body":{"format":"md","version":"` + relicDocPartA + `","bytes":1840}},` +
		`{"id":"` + relicDocPartB + `","type":"decision","position":"2/1"}]}}` + "\n"
	state := goldenRelicState()
	encoded, err = EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(state): %v", err)
	}
	if string(encoded) != wantState {
		t.Fatalf("state encoding:\n got %s\nwant %s", encoded, wantState)
	}
	decodedState, err := DecodeRelicStateDocument(encoded)
	if err != nil || !reflect.DeepEqual(decodedState, state) {
		t.Fatalf("DecodeRelicStateDocument = %#v, %v", decodedState, err)
	}

	links := EmptyRelicLinks()
	encoded, err = EncodeDocument(links)
	if err != nil || string(encoded) != `{"format":"workbook.relic-links","version":1,"links":[]}`+"\n" {
		t.Fatalf("EncodeDocument(EmptyRelicLinks()) = %q, %v", encoded, err)
	}
	decodedLinks, err := DecodeRelicLinksDocument(encoded)
	if err != nil || !reflect.DeepEqual(decodedLinks, links) {
		t.Fatalf("DecodeRelicLinksDocument = %#v, %v", decodedLinks, err)
	}

	fields := NewRelicFields(map[string]json.RawMessage{"heading": json.RawMessage(`"Goals"`)})
	encoded, err = EncodeDocument(fields)
	if err != nil || string(encoded) != `{"format":"workbook.relic-part-fields","version":1,"fields":{"heading":"Goals"}}`+"\n" {
		t.Fatalf("EncodeDocument(fields) = %q, %v", encoded, err)
	}
	decodedFields, err := DecodeRelicFieldsDocument(encoded)
	if err != nil || !reflect.DeepEqual(decodedFields, fields) {
		t.Fatalf("DecodeRelicFieldsDocument = %#v, %v", decodedFields, err)
	}

	empty, err := EncodeDocument(NewRelicFields(nil))
	if err != nil || string(empty) != `{"format":"workbook.relic-part-fields","version":1,"fields":{}}`+"\n" {
		t.Fatalf("EncodeDocument(NewRelicFields(nil)) = %q, %v", empty, err)
	}
}

// validRelicOperations is one valid operation of each type this story writes.
func validRelicOperations() []RelicOperation {
	part := relicDocPartA
	return []RelicOperation{
		{ID: relicTestID(100), Type: RelicOperationCreate, Relic: &RelicCreateData{Title: "T", Type: "spec", CreatedAt: relicDocTime}},
		{ID: relicTestID(101), Type: RelicOperationTitleSet, Title: "New title"},
		{ID: relicTestID(102), Type: RelicOperationTombstone},
		{ID: relicTestID(103), Type: RelicOperationRestore},
		{ID: relicTestID(104), Type: RelicOperationPropertySet, Name: "status", Value: json.RawMessage(`{"a":[1,2.50]}`)},
		{ID: relicTestID(105), Type: RelicOperationPropertyClear, Name: "status"},
		{ID: relicTestID(106), Type: RelicOperationPartAdd, PartType: "section", Position: "3/2"},
		{ID: relicTestID(107), Type: RelicOperationPartFieldSet, Part: part, Name: "heading", Value: json.RawMessage(`"Goals"`)},
		{ID: relicTestID(108), Type: RelicOperationPartFieldClear, Part: part, Name: "heading"},
		{ID: relicTestID(109), Type: RelicOperationPartBodySet, Part: part, Body: &RelicBodyChange{Format: "md", Bytes: 10, Base: relicDocPartA}},
		{ID: relicTestID(110), Type: RelicOperationPartMove, Part: part, Position: "5/1"},
		{ID: relicTestID(111), Type: RelicOperationPartRemove, Part: part},
		{ID: relicTestID(112), Type: RelicOperationPartRestore, Part: part},
		{ID: relicTestID(113), Type: RelicOperationLinkAdd, Link: &RelicLinkChange{From: part,
			To: RelicLinkTarget{Task: "WB-" + relicTestID(114)}, Rel: "implemented-by"}},
		{ID: relicTestID(115), Type: RelicOperationLinkRemove, LinkID: relicTestID(113)},
	}
}

func singleRelicOperationPack(operation RelicOperation) RelicOperationPack {
	return NewRelicOperationPack(relicDocProject, relicDocRelic, relicDocGeneration, relicDocActor, 1, relicDocTime,
		[]RelicOperation{operation})
}

func TestRelicOperationMembersAreExact(t *testing.T) {
	shapes := validRelicOperations()
	if len(shapes) != 15 {
		t.Fatalf("%d shapes, want one for each of the 15 types", len(shapes))
	}
	valid := append(shapes,
		RelicOperation{ID: relicTestID(120), Type: RelicOperationPartAdd, PartType: "section", Position: "1/1",
			Body: &RelicBodyChange{Format: "md", Bytes: 0}},
		RelicOperation{ID: relicTestID(121), Type: RelicOperationPartAdd, PartType: "section", Position: "1/1",
			Body: &RelicBodyChange{Bytes: 7}},
		RelicOperation{ID: relicTestID(122), Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Bytes: 3}},
		RelicOperation{ID: relicTestID(123), Type: RelicOperationPropertySet, Name: "n", Value: json.RawMessage(`null`)},
	)
	for _, operation := range valid {
		pack := singleRelicOperationPack(operation)
		encoded, err := EncodeDocument(pack)
		if err != nil {
			t.Errorf("%s: EncodeDocument: %v", operation.Type, err)
			continue
		}
		decoded, err := DecodeRelicOperationPack(encoded)
		if err != nil || !reflect.DeepEqual(decoded, pack) {
			t.Errorf("%s: DecodeRelicOperationPack = %#v, %v", operation.Type, decoded, err)
		}
	}

	byType := make(map[RelicOperationType]RelicOperation, len(shapes))
	for _, operation := range shapes {
		byType[operation.Type] = operation
	}
	with := func(operationType RelicOperationType, change func(*RelicOperation)) RelicOperation {
		operation := byType[operationType]
		if operation.Body != nil {
			body := *operation.Body
			operation.Body = &body
		}
		change(&operation)
		return operation
	}
	refused := map[string]RelicOperation{
		"title on property.set": with(RelicOperationPropertySet, func(o *RelicOperation) { o.Title = "x" }),
		"value on property.clear": with(RelicOperationPropertyClear, func(o *RelicOperation) {
			o.Value = json.RawMessage(`1`)
		}),
		"part on part.add": with(RelicOperationPartAdd, func(o *RelicOperation) { o.Part = relicDocPartB }),
		"base in part.add's body": with(RelicOperationPartAdd, func(o *RelicOperation) {
			o.Body = &RelicBodyChange{Format: "md", Bytes: 1, Base: relicDocPartA}
		}),
		"position on part.remove":  with(RelicOperationPartRemove, func(o *RelicOperation) { o.Position = "1/1" }),
		"relic on title.set":       with(RelicOperationTitleSet, func(o *RelicOperation) { o.Relic = byType[RelicOperationCreate].Relic }),
		"name on relic.tombstone":  with(RelicOperationTombstone, func(o *RelicOperation) { o.Name = "status" }),
		"no relic on relic.create": with(RelicOperationCreate, func(o *RelicOperation) { o.Relic = nil }),
		"no title on title.set":    with(RelicOperationTitleSet, func(o *RelicOperation) { o.Title = "" }),
		"no value on property.set": with(RelicOperationPropertySet, func(o *RelicOperation) { o.Value = nil }),
		"no name on property.set":  with(RelicOperationPropertySet, func(o *RelicOperation) { o.Name = "" }),
		"no position on part.add":  with(RelicOperationPartAdd, func(o *RelicOperation) { o.Position = "" }),
		"no part type on part.add": with(RelicOperationPartAdd, func(o *RelicOperation) { o.PartType = "" }),
		"no part on field.set":     with(RelicOperationPartFieldSet, func(o *RelicOperation) { o.Part = "" }),
		"no name on field.clear":   with(RelicOperationPartFieldClear, func(o *RelicOperation) { o.Name = "" }),
		"no body on body.set":      with(RelicOperationPartBodySet, func(o *RelicOperation) { o.Body = nil }),
		"no position on part.move": with(RelicOperationPartMove, func(o *RelicOperation) { o.Position = "" }),
		"no part on part.restore":  with(RelicOperationPartRestore, func(o *RelicOperation) { o.Part = "" }),
		"unknown type":             {ID: relicTestID(130), Type: "relic.retire"},
		"untrimmed title":          with(RelicOperationTitleSet, func(o *RelicOperation) { o.Title = " New title" }),
		"non-canonical value": with(RelicOperationPropertySet, func(o *RelicOperation) {
			o.Value = json.RawMessage(`{"b":1,"a":2}`)
		}),
		"invalid property name": with(RelicOperationPropertySet, func(o *RelicOperation) { o.Name = "a=b" }),
		"part not a ULID":       with(RelicOperationPartRemove, func(o *RelicOperation) { o.Part = "part-1" }),
		"unreduced position":    with(RelicOperationPartMove, func(o *RelicOperation) { o.Position = "2/2" }),
		"negative body size": with(RelicOperationPartBodySet, func(o *RelicOperation) {
			o.Body.Bytes = -1
		}),
		"oversized body": with(RelicOperationPartBodySet, func(o *RelicOperation) {
			o.Body.Bytes = MaxRelicBodyBytes + 1
		}),
		"base not a ULID": with(RelicOperationPartBodySet, func(o *RelicOperation) { o.Body.Base = "v1" }),
		"bad part type":   with(RelicOperationPartAdd, func(o *RelicOperation) { o.PartType = "Section" }),
	}
	for label, operation := range refused {
		pack := singleRelicOperationPack(operation)
		// An unknown type declares no generation of its own; mark the pack as
		// this build would, so the type is what gets refused.
		pack.MinReader = RelicMinReader
		requireRelicPackRefused(t, label, pack)
	}
}

func TestRelicPackEnvelopeRules(t *testing.T) {
	cases := map[string]func(*RelicOperationPack){
		"wrong format":            func(p *RelicOperationPack) { p.Format = "workbook.operation-pack" },
		"version 2":               func(p *RelicOperationPack) { p.Version = 2 },
		"minReader 5":             func(p *RelicOperationPack) { p.MinReader = 5 },
		"no minReader":            func(p *RelicOperationPack) { p.MinReader = 0 },
		"blank actor":             func(p *RelicOperationPack) { p.Actor.ID = "  " },
		"clock 0":                 func(p *RelicOperationPack) { p.LogicalClock = 0 },
		"zero wall time":          func(p *RelicOperationPack) { p.WallTime = time.Time{} },
		"no operations":           func(p *RelicOperationPack) { p.Operations = nil },
		"duplicate operation ID":  func(p *RelicOperationPack) { p.Operations[1].ID = p.Operations[0].ID },
		"non-ULID operation ID":   func(p *RelicOperationPack) { p.Operations[1].ID = strings.ToLower(p.Operations[1].ID) },
		"relic ID of a task":      func(p *RelicOperationPack) { p.RelicID = "WB-" + relicTestID(2) },
		"project ID not a ULID":   func(p *RelicOperationPack) { p.ProjectID = "project" },
		"generation not a ULID":   func(p *RelicOperationPack) { p.HistoryGeneration = "" },
		"oversized created title": func(p *RelicOperationPack) { p.Operations[0].Relic.Title = strings.Repeat("a", 501) },
	}
	for label, change := range cases {
		pack := goldenRelicPack()
		create := *pack.Operations[0].Relic
		pack.Operations[0].Relic = &create
		change(&pack)
		requireRelicPackRefused(t, label, pack)
	}
}

// A body edit's base is the version it started from, and its own ID is the
// version it saves, so the two are never the same operation. One that names
// itself could relabel a body without giving it a new version.
func TestRelicBodyEditCannotNameItselfAsItsBase(t *testing.T) {
	edit := relicTestID(8)
	pack := NewRelicOperationPack(relicDocProject, relicDocRelic, relicDocGeneration, relicDocActor, 2, relicDocTime,
		[]RelicOperation{{ID: edit, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "txt", Bytes: 1840, Base: edit}}})
	requireRelicPackRefused(t, "a body edit based on itself", pack)

	// The same edit from another version is an ordinary pack.
	pack.Operations[0].Body.Base = relicDocPartA
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(an edit from another version) = %v", err)
	}
	if _, err := DecodeRelicOperationPack(encoded); err != nil {
		t.Fatalf("DecodeRelicOperationPack(an edit from another version) = %v", err)
	}
}

func TestRelicStateRefusesOversizedSingleValuesOnSaveAndOnRead(t *testing.T) {
	// value is one stored value of exactly size bytes, quotes included.
	value := func(size int) json.RawMessage {
		return json.RawMessage(`"` + strings.Repeat("x", size-2) + `"`)
	}
	cases := []struct {
		label string
		set   func(*RelicStateDocument, int)
		limit int
	}{
		{"title", func(s *RelicStateDocument, n int) { s.Relic.Title = strings.Repeat("a", n) }, MaxRelicTitleBytes},
		{"type", func(s *RelicStateDocument, n int) { s.Relic.Type = strings.Repeat("a", n) }, MaxRelicTypeBytes},
		{"part type", func(s *RelicStateDocument, n int) { s.Relic.Parts[0].Type = strings.Repeat("a", n) }, MaxRelicPartTypeBytes},
		{"format label", func(s *RelicStateDocument, n int) { s.Relic.Parts[0].Body.Format = strings.Repeat("a", n) }, MaxRelicBodyFormatBytes},
		{"position", func(s *RelicStateDocument, n int) {
			s.Relic.Parts[0].Position = "1/1" + strings.Repeat("0", n-3)
		}, MaxRelicPartPositionBytes},
		{"one property value", func(s *RelicStateDocument, n int) {
			s.Relic.Properties = map[string]json.RawMessage{"k": value(n)}
		}, MaxRelicPropertyValueBytes},
		{"body bytes", func(s *RelicStateDocument, n int) { s.Relic.Parts[0].Body.Bytes = int64(n) }, MaxRelicBodyBytes},
	}
	for _, testCase := range cases {
		atLimit := goldenRelicState()
		testCase.set(&atLimit, testCase.limit)
		requireRelicStateRoundTrips(t, testCase.label+" at the limit", atLimit)

		over := goldenRelicState()
		testCase.set(&over, testCase.limit+1)
		requireRelicStateRefused(t, testCase.label+" over the limit", over)
	}

	// The same for an operation that carries a value, which is where a stored
	// value first appears.
	for _, kind := range []struct {
		label     string
		operation func(json.RawMessage) RelicOperation
		limit     int
	}{
		{"property.set value", func(v json.RawMessage) RelicOperation {
			return RelicOperation{ID: relicTestID(40), Type: RelicOperationPropertySet, Name: "k", Value: v}
		}, MaxRelicPropertyValueBytes},
		{"part.field.set value", func(v json.RawMessage) RelicOperation {
			return RelicOperation{ID: relicTestID(40), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "k", Value: v}
		}, MaxRelicFieldValueBytes},
	} {
		atLimit := singleRelicOperationPack(kind.operation(value(kind.limit)))
		encoded, err := EncodeDocument(atLimit)
		if err != nil {
			t.Errorf("%s at the limit: EncodeDocument: %v", kind.label, err)
		} else if _, err := DecodeRelicOperationPack(encoded); err != nil {
			t.Errorf("%s at the limit: DecodeRelicOperationPack: %v", kind.label, err)
		}
		requireRelicPackRefused(t, kind.label+" over the limit", singleRelicOperationPack(kind.operation(value(kind.limit+1))))
	}

	atLimit := NewRelicFields(map[string]json.RawMessage{"heading": value(MaxRelicFieldValueBytes)})
	encoded, err := EncodeDocument(atLimit)
	if err != nil {
		t.Fatalf("EncodeDocument(a field value at the limit): %v", err)
	}
	if _, err := DecodeRelicFieldsDocument(encoded); err != nil {
		t.Fatalf("DecodeRelicFieldsDocument(a field value at the limit): %v", err)
	}
	over := NewRelicFields(map[string]json.RawMessage{"heading": value(MaxRelicFieldValueBytes + 1)})
	_, err = EncodeDocument(over)
	requireCorruptData(t, "one field value over the limit (encode)", err)
	_, err = DecodeRelicFieldsDocument(marshalRelicDocument(t, over))
	requireCorruptData(t, "one field value over the limit (decode)", err)
}

func TestRelicStateNeverChecksCountsOrTotals(t *testing.T) {
	state := goldenRelicState()
	parts := make([]RelicPart, 0, MaxRelicParts+1)
	for index := 0; index < MaxRelicParts+1; index++ {
		id := relicTestID(1000 + index)
		part := RelicPart{ID: id, Type: "section", Position: itoa(index+1) + "/1"}
		if index > MaxRelicLiveParts {
			part.Removed = true
		}
		if index < 17 {
			part.Body = &RelicBody{Version: id, Bytes: MaxRelicBodyBytes}
		}
		parts = append(parts, part)
	}
	state.Relic.Parts = parts
	if live := len(state.Relic.LiveParts()); live != MaxRelicLiveParts+1 {
		t.Fatalf("%d live parts, want %d", live, MaxRelicLiveParts+1)
	}
	// All of a relic's properties together, and all of one part's fields
	// together, are totals too: each is a sum over values that separate
	// operations set. Two values that are each within the limit for one value
	// take each total one byte past its limit here.
	filler := func(size int) json.RawMessage { return json.RawMessage(`"` + strings.Repeat("x", size-2) + `"`) }
	state.Relic.Properties = map[string]json.RawMessage{"a": filler(40000), "b": filler(40000)}
	padding, err := json.Marshal(state.Relic.Properties)
	if err != nil {
		t.Fatal(err)
	}
	state.Relic.Properties["b"] = filler(40000 - (len(padding) - (MaxRelicPropertiesBytes + 1)))
	if encoded, _ := json.Marshal(state.Relic.Properties); len(encoded) != MaxRelicPropertiesBytes+1 {
		t.Fatalf("the properties encode to %d bytes, want %d", len(encoded), MaxRelicPropertiesBytes+1)
	}
	requireRelicStateRoundTrips(t, "a relic over every count and total", state)

	fields := NewRelicFields(map[string]json.RawMessage{"a": filler(9000), "b": filler(9000)})
	stored := marshalRelicDocument(t, fields)
	fields.Fields["b"] = filler(9000 - (len(stored) - (MaxRelicPartFieldsBytes + 1)))
	stored, err = EncodeDocument(fields)
	if err != nil || len(stored) != MaxRelicPartFieldsBytes+1 {
		t.Fatalf("EncodeDocument(fields one byte past their total) = %d bytes, %v", len(stored), err)
	}
	decoded, err := DecodeRelicFieldsDocument(stored)
	if err != nil || !reflect.DeepEqual(decoded, fields) {
		t.Fatalf("DecodeRelicFieldsDocument(fields one byte past their total) = %v", err)
	}
}

func TestRelicStateCanonicalForm(t *testing.T) {
	cases := map[string]func(*RelicStateDocument){
		"parts out of order": func(s *RelicStateDocument) {
			s.Relic.Parts[0], s.Relic.Parts[1] = s.Relic.Parts[1], s.Relic.Parts[0]
		},
		"parts sharing a position out of ID order": func(s *RelicStateDocument) {
			s.Relic.Parts[0].Position = "2/1"
			s.Relic.Parts[0], s.Relic.Parts[1] = s.Relic.Parts[1], s.Relic.Parts[0]
		},
		"duplicate part ID":     func(s *RelicStateDocument) { s.Relic.Parts[1].ID = s.Relic.Parts[0].ID },
		"null properties":       func(s *RelicStateDocument) { s.Relic.Properties = nil },
		"null parts":            func(s *RelicStateDocument) { s.Relic.Parts = nil },
		"non-canonical value":   func(s *RelicStateDocument) { s.Relic.Properties["status"] = json.RawMessage(`{"b":1,"a":2}`) },
		"invalid property name": func(s *RelicStateDocument) { s.Relic.Properties["a\x01"] = json.RawMessage(`1`) },
		"retired":               func(s *RelicStateDocument) { s.Relic.Retired = true },
		"a schema":              func(s *RelicStateDocument) { s.Relic.Schema = json.RawMessage(`{}`) },
		"compacted": func(s *RelicStateDocument) {
			from := relicTestID(9)
			s.History.CompactedFrom = &from
		},
		"wrong format":        func(s *RelicStateDocument) { s.Format = "workbook.task-state" },
		"version 2":           func(s *RelicStateDocument) { s.Version = 2 },
		"minReader 5":         func(s *RelicStateDocument) { s.MinReader = 5 },
		"relic ID of a task":  func(s *RelicStateDocument) { s.RelicID = "WB-" + relicTestID(2) },
		"clock 0":             func(s *RelicStateDocument) { s.LogicalClock = 0 },
		"blank author":        func(s *RelicStateDocument) { s.Relic.Author = "" },
		"untrimmed title":     func(s *RelicStateDocument) { s.Relic.Title += " " },
		"zero creation time":  func(s *RelicStateDocument) { s.Relic.CreatedAt = time.Time{} },
		"zero update time":    func(s *RelicStateDocument) { s.Relic.UpdatedAt = time.Time{} },
		"body version blank":  func(s *RelicStateDocument) { s.Relic.Parts[0].Body.Version = "" },
		"negative body bytes": func(s *RelicStateDocument) { s.Relic.Parts[0].Body.Bytes = -1 },
		"part ID not a ULID":  func(s *RelicStateDocument) { s.Relic.Parts[1].ID = "B" },
	}
	for label, change := range cases {
		state := goldenRelicState()
		state.Relic.Parts = append([]RelicPart(nil), state.Relic.Parts...)
		body := *state.Relic.Parts[0].Body
		state.Relic.Parts[0].Body = &body
		state.Relic.Properties = map[string]json.RawMessage{"status": json.RawMessage(`"approved"`)}
		change(&state)
		requireRelicStateRefused(t, label, state)
	}

	removed := goldenRelicState()
	removed.Relic.Deleted = true
	removed.Relic.Parts[1].Removed = true
	removed.Relic.Properties = map[string]json.RawMessage{}
	requireRelicStateRoundTrips(t, "a deleted relic with a removed part", removed)
}

func TestRelicValuesKeepJSONMarshalHTMLEscaping(t *testing.T) {
	canonical, err := CanonicalRelicValue([]byte(`{"note":"a<b>&c"}`))
	if err != nil {
		t.Fatalf("CanonicalRelicValue: %v", err)
	}
	state := goldenRelicState()
	state.Relic.Properties["note"] = canonical
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument: %v", err)
	}
	decoded, err := DecodeRelicStateDocument(encoded)
	if err != nil {
		t.Fatalf("DecodeRelicStateDocument: %v", err)
	}
	if string(decoded.Relic.Properties["note"]) != string(canonical) {
		t.Fatalf("decoded value %s, want %s", decoded.Relic.Properties["note"], canonical)
	}
	again, err := EncodeDocument(decoded)
	if err != nil || string(again) != string(encoded) {
		t.Fatalf("re-encoding the decoded state = %q, %v", again, err)
	}

	bare := strings.Replace(string(encoded), `\u003c`, `<`, 1)
	if bare == string(encoded) {
		t.Fatal("the encoded state holds no escaped '<'")
	}
	_, err = DecodeRelicStateDocument([]byte(bare))
	requireCorruptData(t, "a value stored without json.Marshal's escaping", err)

	fields := NewRelicFields(map[string]json.RawMessage{"heading": canonical})
	encoded, err = EncodeDocument(fields)
	if err != nil {
		t.Fatalf("EncodeDocument(fields): %v", err)
	}
	if decodedFields, err := DecodeRelicFieldsDocument(encoded); err != nil ||
		string(decodedFields.Fields["heading"]) != string(canonical) {
		t.Fatalf("DecodeRelicFieldsDocument = %#v, %v", decodedFields, err)
	}
}

func TestRelicFieldsCanonicalForm(t *testing.T) {
	cases := map[string]RelicFieldsDocument{
		"null fields":         {Format: "workbook.relic-part-fields", Version: 1},
		"wrong format":        {Format: "workbook.relic-links", Version: 1, Fields: map[string]json.RawMessage{}},
		"version 2":           {Format: "workbook.relic-part-fields", Version: 2, Fields: map[string]json.RawMessage{}},
		"non-canonical value": NewRelicFields(map[string]json.RawMessage{"a": json.RawMessage(`{"b":1,"a":2}`)}),
		"invalid name":        NewRelicFields(map[string]json.RawMessage{"": json.RawMessage(`1`)}),
	}
	for label, fields := range cases {
		_, err := EncodeDocument(fields)
		requireCorruptData(t, label+" (encode)", err)
		_, err = DecodeRelicFieldsDocument(marshalRelicDocument(t, fields))
		requireCorruptData(t, label+" (decode)", err)
	}
}

func TestRelicLinksEnvelopeRules(t *testing.T) {
	cases := map[string]RelicLinksDocument{
		"an empty link": {Format: "workbook.relic-links", Version: 1, Links: []RelicLink{{}}},
		"null links":    {Format: "workbook.relic-links", Version: 1},
		"wrong format":  {Format: "workbook.relic-part-fields", Version: 1, Links: []RelicLink{}},
		"version 2":     {Format: "workbook.relic-links", Version: 2, Links: []RelicLink{}},
	}
	for label, links := range cases {
		_, err := EncodeDocument(links)
		requireCorruptData(t, label+" (encode)", err)
		_, err = DecodeRelicLinksDocument(marshalRelicDocument(t, links))
		requireCorruptData(t, label+" (decode)", err)
	}
}

func TestRelicDocumentsFromANewerWriter(t *testing.T) {
	encodedPack, err := EncodeDocument(goldenRelicPack())
	if err != nil {
		t.Fatal(err)
	}
	newerPack := strings.Replace(string(encodedPack), `"minReader":6,`, `"minReader":7,`, 1)
	newerPack = strings.Replace(newerPack, `]}`+"\n",
		`,{"id":"`+relicTestID(8)+`","type":"relic.retire","reason":"superseded"}]}`+"\n", 1)
	pack, err := DecodeRelicOperationPack([]byte(newerPack))
	if err != nil {
		t.Fatalf("DecodeRelicOperationPack(newer) = %v", err)
	}
	if !pack.RequiresNewerReader() || pack.MinReader != 7 || len(pack.Operations) != 5 ||
		pack.Operations[4].Type != "relic.retire" {
		t.Fatalf("newer pack decoded as %#v", pack)
	}
	_, err = EncodeDocument(pack)
	if CategoryOf(err) != CategoryNewerWriter ||
		err.Error() != "relic "+relicDocRelic+" was written by a newer workbook; upgrade workbook to change it" {
		t.Fatalf("EncodeDocument(newer pack) = %v, want the newer-writer refusal", err)
	}
	_, err = DecodeRelicOperationPack([]byte(strings.Replace(newerPack, `"minReader":7,`, "", 1)))
	requireCorruptData(t, "the newer pack without its marker", err)

	encodedState, err := EncodeDocument(goldenRelicState())
	if err != nil {
		t.Fatal(err)
	}
	newerState := strings.Replace(string(encodedState), `"minReader":6,`, `"minReader":7,`, 1)
	newerState = strings.Replace(newerState, `"retired":false,`, `"retired":true,"retiredBy":"x",`, 1)
	state, err := DecodeRelicStateDocument([]byte(newerState))
	if err != nil {
		t.Fatalf("DecodeRelicStateDocument(newer) = %v", err)
	}
	if !state.RequiresNewerReader() || !state.Relic.Retired || state.Relic.Title != "Task archive compaction" ||
		len(state.Relic.Parts) != 2 {
		t.Fatalf("newer state decoded as %#v", state)
	}
	_, err = EncodeDocument(state)
	if CategoryOf(err) != CategoryNewerWriter {
		t.Fatalf("EncodeDocument(newer state) = %v, want newer writer", err)
	}
	_, err = DecodeRelicStateDocument([]byte(strings.Replace(newerState, `"minReader":7,`, "", 1)))
	requireCorruptData(t, "the newer state without its marker", err)

	// A newer state whose lists are null still reads as a total value.
	nullLists := strings.Replace(newerState, `"properties":{"status":"approved"}`, `"properties":null`, 1)
	state, err = DecodeRelicStateDocument([]byte(nullLists))
	if err != nil || state.Relic.Properties == nil {
		t.Fatalf("DecodeRelicStateDocument(newer, null properties) = %#v, %v", state.Relic.Properties, err)
	}
}

func TestSupportedFormatGenerationIsSix(t *testing.T) {
	if SupportedFormatGeneration != 6 || RelicMinReader != 6 {
		t.Fatalf("SupportedFormatGeneration = %d, RelicMinReader = %d, want 6 and 6",
			SupportedFormatGeneration, RelicMinReader)
	}
	for _, operation := range validRelicOperations() {
		if got := singleRelicOperationPack(operation).MinReader; got != 6 {
			t.Errorf("a %s pack declares minReader %d, want 6", operation.Type, got)
		}
	}
}

func TestRelicDataParts(t *testing.T) {
	data := goldenRelicState().Relic
	data.Parts[0].Removed = true
	live := data.LiveParts()
	if len(live) != 1 || live[0].ID != relicDocPartB {
		t.Fatalf("LiveParts() = %#v", live)
	}
	if part, ok := data.Part(relicDocPartA); !ok || !part.Removed {
		t.Fatalf("Part(removed) = %#v, %v", part, ok)
	}
	if _, ok := data.Part(relicTestID(99)); ok {
		t.Fatal("Part(unknown) found a part")
	}
}
