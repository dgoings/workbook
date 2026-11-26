package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The forms these tests pin are set strict while the format is unreleased: a
// limit can be raised later and never lowered, and a form accepted now can
// never be refused later.

// relicNested returns a value of the given depth: 0 is a scalar, and each
// level wraps it in an array or, for every third level, an object.
func relicNested(depth int) string {
	var open, shut strings.Builder
	for level := 0; level < depth; level++ {
		if level%3 == 2 {
			open.WriteString(`{"k":`)
			shut.WriteString("}")
			continue
		}
		open.WriteString("[")
		shut.WriteString("]")
	}
	closing := []byte(shut.String())
	for left, right := 0, len(closing)-1; left < right; left, right = left+1, right-1 {
		closing[left], closing[right] = closing[right], closing[left]
	}
	return open.String() + "1" + string(closing)
}

func TestRelicValueDepthIsCountedInOnePass(t *testing.T) {
	for value, want := range map[string]int{
		`1`:                        0,
		`"x"`:                      0,
		`null`:                     0,
		`[1]`:                      1,
		`[]`:                       1,
		`{}`:                       1,
		`{"a":[1]}`:                2,
		`[[1],[[2]],3]`:            3,
		`{"a":{"b":{"c":[]}}}`:     4,
		`"[[[[[[[[[[[[["`:          0,
		`["]]]]\"[[[[\\",[1]]`:     2,
		`["\\\\",[["\"[[["]]]`:     3,
		` [ [ 1 ] , { "a" : 2 } ]`: 2,
	} {
		if got := relicValueDepth([]byte(value)); got != want {
			t.Errorf("relicValueDepth(%s) = %d, want %d", value, got, want)
		}
	}
	for _, depth := range []int{0, 1, 31, 32, 33, 5000} {
		if got := relicValueDepth([]byte(relicNested(depth))); got != depth {
			t.Errorf("relicValueDepth(a value nested %d deep) = %d", depth, got)
		}
	}
}

func TestARelicValueNestsAtMost32Levels(t *testing.T) {
	if MaxRelicValueDepth != 32 {
		t.Fatalf("MaxRelicValueDepth = %d, want 32", MaxRelicValueDepth)
	}
	atLimit, err := CanonicalRelicValue([]byte(relicNested(32)))
	if err != nil || string(atLimit) != relicNested(32) {
		t.Fatalf("CanonicalRelicValue(32 levels) = %q, %v", atLimit, err)
	}
	// One level past the limit, the depth a reviewer reached the encoder's own
	// failure at, past Go's own bound, and deep enough to end a recursive
	// reader: each is refused the same way, by the count alone.
	for _, depth := range []int{33, 9998, 10001, 2_000_000} {
		_, err := CanonicalRelicValue([]byte(relicNested(depth)))
		requireValidation(t, "a value nested too deep", err)
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), strconv.Itoa(depth)+" levels") || !strings.Contains(err.Error(), "the limit is 32") {
			t.Errorf("depth %d: error %q does not give the depth and the limit", depth, err)
		}
		if len(err.Error()) > 200 {
			t.Errorf("depth %d: the refusal is %d bytes long; it must not repeat the value", depth, len(err.Error()))
		}
	}

	// On read it is corrupt data, in every document that holds a value.
	deep := json.RawMessage(relicNested(33))
	state := goldenRelicState()
	state.Relic.Properties = map[string]json.RawMessage{"deep": deep}
	requireRelicStateRefused(t, "a stored property nested 33 levels", state)
	state.Relic.Properties = map[string]json.RawMessage{"deep": atLimit}
	requireRelicStateRoundTrips(t, "a stored property nested 32 levels", state)

	fields := NewRelicFields(map[string]json.RawMessage{"deep": deep})
	_, err = EncodeDocument(fields)
	requireCorruptData(t, "a stored field nested 33 levels (encode)", err)
	_, err = DecodeRelicFieldsDocument(marshalRelicDocument(t, fields))
	requireCorruptData(t, "a stored field nested 33 levels (decode)", err)

	requireRelicPackRefused(t, "property.set nested 33 levels", singleRelicOperationPack(
		RelicOperation{ID: relicTestID(40), Type: RelicOperationPropertySet, Name: "deep", Value: deep}))
	requireRelicPackRefused(t, "part.field.set nested 33 levels", singleRelicOperationPack(
		RelicOperation{ID: relicTestID(41), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "deep", Value: deep}))
}

func TestRelicServiceRefusesAValueNestedTooDeepAsValidation(t *testing.T) {
	ctx := context.Background()
	deep := json.RawMessage(relicNested(33))
	for name, mutate := range map[string]func(*RelicCreateInput){
		"property": func(input *RelicCreateInput) { input.Properties = map[string]json.RawMessage{"deep": deep} },
		"field":    func(input *RelicCreateInput) { input.Parts[0].Fields = map[string]json.RawMessage{"deep": deep} },
	} {
		store := newMemoryRelicStore()
		input := relicCreateInputForTest()
		mutate(&input)
		_, err := newRelicTestService(store, relicServiceIDs(1, 20)).Create(ctx, input)
		requireRelicCategory(t, "create with a deep "+name, err, CategoryValidation, name, "33 levels", "the limit is 32")
		if len(store.writes) != 0 {
			t.Fatalf("create with a deep %s wrote %d commits", name, len(store.writes))
		}
	}

	store := newMemoryRelicStore()
	snapshot := relicFixture(t, 1, relicFixtureParts(1, 0, -1))
	store.put(snapshot)
	relicID := snapshot.Record.State.RelicID
	part := snapshot.Record.State.Relic.Parts[0].ID
	for name, edit := range map[string]RelicEdit{
		"property": {Kind: RelicEditSetProperty, Name: "deep", Value: deep},
		"field":    {Kind: RelicEditPart, Part: part, Fields: []RelicFieldEdit{{Name: "deep", Value: deep}}},
		// The depth the commands reviewer reached "cannot encode computed
		// relic state" at, which was reported as corrupt data.
		"property at 9,998": {Kind: RelicEditSetProperty, Name: "deep", Value: json.RawMessage(relicNested(9998))},
	} {
		_, err := newRelicTestService(store, relicServiceIDs(100, 20)).Update(ctx, relicID, []RelicEdit{edit})
		requireRelicCategory(t, "update with a deep "+name, err, CategoryValidation, "levels", "the limit is 32")
		if len(store.writes) != 0 {
			t.Fatalf("update with a deep %s wrote %d commits", name, len(store.writes))
		}
	}
	atLimit := RelicEdit{Kind: RelicEditSetProperty, Name: "deep", Value: json.RawMessage(relicNested(32))}
	if _, err := newRelicTestService(store, relicServiceIDs(200, 20)).Update(ctx, relicID, []RelicEdit{atLimit}); err != nil {
		t.Fatalf("update with a value nested 32 levels: %v", err)
	}
}

func TestARelicNameIsAtMost100BytesWithNoWhiteSpaceAtItsEnds(t *testing.T) {
	if MaxRelicNameBytes != 100 {
		t.Fatalf("MaxRelicNameBytes = %d, want 100", MaxRelicNameBytes)
	}
	good := []string{
		strings.Repeat("n", 100),
		strings.Repeat("é", 50), // 100 bytes
		"reviewer name",
		"a\u00a0b", // white space inside a name is the name's own
		"a\u3000b", //
		"x",
	}
	bad := map[string]string{
		"101 bytes":                      strings.Repeat("n", 101),
		"101 bytes in 51 characters":     strings.Repeat("é", 50) + "n",
		"a leading space":                " status",
		"a trailing space":               "status ",
		"a leading no-break space":       "\u00a0status",
		"a trailing no-break space":      "status\u00a0",
		"a leading next-line":            "\u0085status",
		"a trailing em space":            "status\u2003",
		"a leading ideographic space":    "\u3000status",
		"a trailing line separator":      "status\u2028",
		"a trailing paragraph separator": "status\u2029",
		"a leading ogham space":          "\u1680status",
		"only a space":                   " ",
	}
	for _, kind := range []string{"property", "field"} {
		for _, name := range good {
			if err := ValidateRelicName(kind, name); err != nil {
				t.Errorf("%s name %q: %v", kind, name, err)
			}
		}
		for label, name := range bad {
			err := ValidateRelicName(kind, name)
			requireValidation(t, kind+" name with "+label, err)
			if err != nil && !strings.Contains(err.Error(), kind) {
				t.Errorf("%s name with %s: message %q lacks the kind", kind, label, err)
			}
		}
		long := strings.Repeat("n", 4000)
		err := ValidateRelicName(kind, long)
		requireValidation(t, kind+" name of 4000 bytes", err)
		if err != nil && (!strings.Contains(err.Error(), "4000 bytes") || !strings.Contains(err.Error(), "the limit is 100 bytes") ||
			strings.Contains(err.Error(), "nnnn")) {
			t.Errorf("%s name of 4000 bytes: message %q must give the size and the limit and not repeat the name", kind, err)
		}
	}

	// On read each is corrupt data, in every document that holds a name.
	for label, name := range bad {
		state := goldenRelicState()
		state.Relic.Properties = map[string]json.RawMessage{name: json.RawMessage(`1`)}
		requireRelicStateRefused(t, "a stored property name with "+label, state)

		fields := NewRelicFields(map[string]json.RawMessage{name: json.RawMessage(`1`)})
		_, err := EncodeDocument(fields)
		requireCorruptData(t, "a stored field name with "+label+" (encode)", err)
		_, err = DecodeRelicFieldsDocument(marshalRelicDocument(t, fields))
		requireCorruptData(t, "a stored field name with "+label+" (decode)", err)

		for _, operation := range []RelicOperation{
			{ID: relicTestID(40), Type: RelicOperationPropertySet, Name: name, Value: json.RawMessage(`1`)},
			{ID: relicTestID(41), Type: RelicOperationPropertyClear, Name: name},
			{ID: relicTestID(42), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: name, Value: json.RawMessage(`1`)},
			{ID: relicTestID(43), Type: RelicOperationPartFieldClear, Part: relicDocPartA, Name: name},
		} {
			requireRelicPackRefused(t, string(operation.Type)+" naming "+label, singleRelicOperationPack(operation))
		}
	}
	state := goldenRelicState()
	state.Relic.Properties = map[string]json.RawMessage{strings.Repeat("n", 100): json.RawMessage(`1`)}
	requireRelicStateRoundTrips(t, "a stored property name of 100 bytes", state)
}

func TestRelicServiceRefusesANameItCouldNotReadBack(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("n", 101)
	for label, name := range map[string]string{"101 bytes": long, "a trailing space": "status ", "a leading no-break space": "\u00a0status"} {
		for kind, mutate := range map[string]func(*RelicCreateInput){
			"property": func(input *RelicCreateInput) {
				input.Properties = map[string]json.RawMessage{name: json.RawMessage(`1`)}
			},
			"field": func(input *RelicCreateInput) {
				input.Parts[0].Fields = map[string]json.RawMessage{name: json.RawMessage(`1`)}
			},
		} {
			store := newMemoryRelicStore()
			input := relicCreateInputForTest()
			mutate(&input)
			_, err := newRelicTestService(store, relicServiceIDs(1, 20)).Create(ctx, input)
			requireRelicCategory(t, "create with a "+kind+" name of "+label, err, CategoryValidation, kind+" name")
			if len(store.writes) != 0 {
				t.Fatalf("create with a %s name of %s wrote %d commits", kind, label, len(store.writes))
			}
		}

		store := newMemoryRelicStore()
		snapshot := relicFixture(t, 1, relicFixtureParts(1, 0, -1))
		store.put(snapshot)
		part := snapshot.Record.State.Relic.Parts[0].ID
		for kind, edit := range map[string]RelicEdit{
			"property":       {Kind: RelicEditSetProperty, Name: name, Value: json.RawMessage(`1`)},
			"property clear": {Kind: RelicEditClearProperty, Name: name},
			"field":          {Kind: RelicEditPart, Part: part, Fields: []RelicFieldEdit{{Name: name, Value: json.RawMessage(`1`)}}},
			"field clear":    {Kind: RelicEditPart, Part: part, Fields: []RelicFieldEdit{{Name: name, Clear: true}}},
			"field of a new part": {Kind: RelicEditAddPart, PartType: "note",
				Fields: []RelicFieldEdit{{Name: name, Value: json.RawMessage(`1`)}}},
		} {
			_, err := newRelicTestService(store, relicServiceIDs(100, 20)).Update(ctx, snapshot.Record.State.RelicID, []RelicEdit{edit})
			requireRelicCategory(t, "update with a "+kind+" name of "+label, err, CategoryValidation, "name")
			if len(store.writes) != 0 {
				t.Fatalf("update with a %s name of %s wrote %d commits", kind, label, len(store.writes))
			}
			if strings.Contains(err.Error(), long) {
				t.Fatalf("update with a %s name of %s repeats the name: %q", kind, label, err)
			}
		}
	}
}

// A stored time may carry any RFC 3339 offset: the writer records UTC, but a
// project may keep its relics in a zone of its own. The offset is kept, so a
// document reads back as the bytes it was stored in. A zero offset has one
// form, Z: Go writes +00:00 and -00:00 back as Z, so those bytes cannot be the
// document's own re-encoding, and they are refused with a reason.
func TestARelicKeepsTheOffsetOfAStoredTime(t *testing.T) {
	offset := time.FixedZone("", 5*60*60)
	elsewhere := relicDocTime.In(offset)
	if encoded, _ := json.Marshal(elsewhere); string(encoded) != `"2026-11-23T15:00:00+05:00"` {
		t.Fatalf("the test's time encodes as %s; it needs an offset", encoded)
	}

	pack := goldenRelicPack()
	pack.WallTime = elsewhere
	created := *pack.Operations[0].Relic
	created.CreatedAt = elsewhere
	pack.Operations = append([]RelicOperation(nil), pack.Operations...)
	pack.Operations[0].Relic = &created
	if _, err := EncodeDocument(pack); err != nil {
		t.Fatalf("EncodeDocument(a pack whose times have an offset): %v", err)
	}
	state := goldenRelicState()
	state.Relic.CreatedAt, state.Relic.UpdatedAt = elsewhere, elsewhere
	if _, err := EncodeDocument(state); err != nil {
		t.Fatalf("EncodeDocument(a state whose times have an offset): %v", err)
	}

	// A zero offset that is not UTC is what +00:00 and -00:00 decode to. It
	// would be written Z, so it is refused before it is written.
	zero := relicDocTime.In(time.FixedZone("", 0))
	pack = goldenRelicPack()
	pack.WallTime = zero
	_, err := EncodeDocument(pack)
	requireCorruptData(t, "a wall time with a zero offset that is not UTC", err)
	state = goldenRelicState()
	state.Relic.UpdatedAt = zero
	_, err = EncodeDocument(state)
	requireCorruptData(t, "an update time with a zero offset that is not UTC", err)

	// As bytes: the golden documents with one time rewritten.
	for label, document := range map[string]any{"pack": goldenRelicPack(), "state": goldenRelicState()} {
		encoded, err := EncodeDocument(document)
		if err != nil {
			t.Fatalf("EncodeDocument(%s): %v", label, err)
		}
		decode := func(data []byte) ([]byte, error) {
			var decoded any
			if label == "pack" {
				decoded, err = DecodeRelicOperationPack(data)
			} else {
				decoded, err = DecodeRelicStateDocument(data)
			}
			if err != nil {
				return nil, err
			}
			return EncodeDocument(decoded)
		}
		for _, other := range []string{"2026-11-23T15:00:00+05:00", "2026-11-23T05:00:00-05:00", "2026-11-23T15:45:00+05:45", "2026-11-22T21:00:00.5-13:00"} {
			forged := bytes.Replace(encoded, []byte("2026-11-23T10:00:00Z"), []byte(other), 1)
			if bytes.Equal(forged, encoded) {
				t.Fatalf("the %s holds no time to rewrite", label)
			}
			again, err := decode(forged)
			if err != nil {
				t.Errorf("a %s with a time written %s: %v", label, other, err)
				continue
			}
			if !bytes.Equal(again, forged) {
				t.Errorf("a %s with a time written %s encodes again as\n%s\nnot\n%s", label, other, again, forged)
			}
		}
		for _, other := range []string{"2026-11-23T10:00:00+00:00", "2026-11-23T10:00:00-00:00"} {
			forged := bytes.Replace(encoded, []byte("2026-11-23T10:00:00Z"), []byte(other), 1)
			_, err := decode(forged)
			requireCorruptData(t, "a "+label+" with a time written "+other, err)
			if err != nil && !strings.Contains(err.Error(), "a zero offset is written Z") {
				t.Errorf("a %s with a time written %s: error %q does not say how a zero offset is written", label, other, err)
			}
		}
	}

	// Fractions of a second are the writer's own form and stay readable.
	precise := goldenRelicState()
	precise.Relic.UpdatedAt = relicDocTime.Add(123456789 * time.Nanosecond)
	requireRelicStateRoundTrips(t, "an update time with nanoseconds", precise)
}

// The service writes UTC whatever zone its clock answers in, so no relic it
// writes is one a reader refuses.
func TestRelicServiceRecordsUTCFromAClockInAnotherZone(t *testing.T) {
	ctx := context.Background()
	local := relicServiceTestTime.In(time.FixedZone("", -5*60*60))
	store := newMemoryRelicStore()
	service := newRelicTestService(store, relicServiceIDs(1, 40))
	service.Now = func() time.Time { return local }

	created, err := service.Create(ctx, relicCreateInputForTest())
	if err != nil {
		t.Fatalf("Create() with a clock in another zone: %v", err)
	}
	if _, err := service.Update(ctx, created.Relic.ID, []RelicEdit{{Kind: RelicEditTitle, Title: "Another title"}}); err != nil {
		t.Fatalf("Update() with a clock in another zone: %v", err)
	}
	if len(store.writes) != 2 {
		t.Fatalf("the store holds %d writes, want 2", len(store.writes))
	}
	for index, write := range store.writes {
		for label, document := range map[string]any{"pack": write.pack, "state": write.record.State} {
			encoded, err := EncodeDocument(document)
			if err != nil {
				t.Fatalf("write %d: EncodeDocument(%s): %v", index, label, err)
			}
			if bytes.Contains(encoded, []byte("-05:00")) || !bytes.Contains(encoded, []byte(`Z"`)) {
				t.Fatalf("write %d: the %s holds a time that is not UTC: %s", index, label, encoded)
			}
		}
		if !write.pack.WallTime.Equal(local) {
			t.Fatalf("write %d: wall time %s is not the instant the clock gave, %s", index, write.pack.WallTime, local)
		}
	}
}

// The bytes a stored value has are what json.Marshal writes for it, and a
// stored relic is canonical only while they stay that. This test holds one
// value with everything json.Marshal treats specially against bytes written
// out here by hand.
//
// If this test fails after a Go upgrade, do not change the expected bytes.
// The encoder's output has changed, which means every relic already stored
// with one of these characters would stop being canonical and be refused as
// corrupt. Pin the encoder: write these forms with code this repository owns,
// producing exactly the bytes below.
func TestRelicValueEncodingIsPinnedToTheseBytes(t *testing.T) {
	// Every C0 control character and DEL, as the escapes a person would type.
	var controls strings.Builder
	for c := 0; c < 0x20; c++ {
		controls.WriteString(`\u00` + string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&0xf]))
	}
	controls.WriteString(`\u007f`)
	input := `{"é":"non-ASCII key","a":"ASCII key","controls":"` + controls.String() + `",` +
		`"separators":"` + "\u2028\u2029" + `","html":"<>&","numbers":[1.0,1E2,-0],"Z":1,"_":2,"aa":3}`

	const want = `{"Z":1,"_":2,"a":"ASCII key","aa":3,` +
		`"controls":"\u0000\u0001\u0002\u0003\u0004\u0005\u0006\u0007\b\t\n\u000b\f\r\u000e\u000f` +
		`\u0010\u0011\u0012\u0013\u0014\u0015\u0016\u0017\u0018\u0019\u001a\u001b\u001c\u001d\u001e\u001f` + "\x7f" + `",` +
		`"html":"\u003c\u003e\u0026","numbers":[1.0,1E2,-0],"separators":"\u2028\u2029","é":"non-ASCII key"}`

	canonical, err := CanonicalRelicValue([]byte(input))
	if err != nil {
		t.Fatalf("CanonicalRelicValue: %v", err)
	}
	if string(canonical) != want {
		t.Fatalf("the canonical value is\n%q\nwant\n%q\nSee the comment on this test before changing either.", canonical, want)
	}

	// The same bytes inside a stored document, which json.Marshal passes
	// through its own escaping a second time.
	state := goldenRelicState()
	state.Relic.Properties = map[string]json.RawMessage{"golden": canonical}
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"properties":{"golden":`+want+`},`)) {
		t.Fatalf("the stored state does not hold the value in these bytes:\n%q\nSee the comment on this test.", encoded)
	}
	fields, err := EncodeDocument(NewRelicFields(map[string]json.RawMessage{"golden": canonical}))
	if err != nil {
		t.Fatalf("EncodeDocument(fields): %v", err)
	}
	if want := `{"format":"workbook.relic-part-fields","version":1,"fields":{"golden":` + want + `}}` + "\n"; string(fields) != want {
		t.Fatalf("the stored fields are\n%q\nwant\n%q\nSee the comment on this test.", fields, want)
	}
	decoded, err := DecodeRelicStateDocument(encoded)
	if err != nil || string(decoded.Relic.Properties["golden"]) != want {
		t.Fatalf("the stored state reads back as %q, %v", decoded.Relic.Properties["golden"], err)
	}
}

// A name holds no control character, by Unicode's category Cc: U+0000 to
// U+001F, U+007F, and U+0080 to U+009F. The last range was accepted inside a
// name, where no terminal shows it and two names could differ by it unseen.
func TestARelicNameHoldsNoControlCharacter(t *testing.T) {
	bad := map[string]string{
		"U+0000 inside":     "sta\x00tus",
		"U+001F inside":     "sta\x1ftus",
		"U+007F inside":     "sta\x7ftus",
		"U+0080 inside":     "sta\xc2\x80tus",
		"U+0085 inside":     "sta\xc2\x85tus",
		"U+009B inside":     "sta\xc2\x9btus",
		"U+009F inside":     "sta\xc2\x9ftus",
		"U+0080 at the end": "status\xc2\x80",
	}
	good := map[string]string{
		"U+00A0, the first character after the range": "sta\xc2\xa0tus",
	}
	for _, kind := range []string{"property", "field"} {
		for label, name := range good {
			if err := ValidateRelicName(kind, name); err != nil {
				t.Errorf("%s name with %s: %v", kind, label, err)
			}
		}
		for label, name := range bad {
			err := ValidateRelicName(kind, name)
			requireValidation(t, kind+" name with "+label, err)
			if err != nil && !strings.Contains(err.Error(), "control characters") {
				t.Errorf("%s name with %s: message %q, want it to say a name holds no control characters", kind, label, err)
			}
		}
	}

	// On read each is corrupt data.
	for label, name := range bad {
		state := goldenRelicState()
		state.Relic.Properties = map[string]json.RawMessage{name: json.RawMessage(`1`)}
		requireRelicStateRefused(t, "a stored property name with "+label, state)

		fields := NewRelicFields(map[string]json.RawMessage{name: json.RawMessage(`1`)})
		_, err := DecodeRelicFieldsDocument(marshalRelicDocument(t, fields))
		requireCorruptData(t, "a stored field name with "+label, err)
	}
	for label, name := range good {
		state := goldenRelicState()
		state.Relic.Properties = map[string]json.RawMessage{name: json.RawMessage(`1`)}
		requireRelicStateRoundTrips(t, "a stored property name with "+label, state)
	}
}

// A name holds no format character, by Unicode's category Cf. None of them is
// seen on a terminal, so two names that differ by one look the same: a
// zero-width space or a byte-order mark at an end, a right-to-left override
// that reverses what follows, a soft hyphen.
func TestARelicNameHoldsNoFormatCharacter(t *testing.T) {
	bad := map[string]string{
		"U+200B at the end":     "status\u200b",
		"U+FEFF at the start":   "\ufeffstatus",
		"U+202E inside":         "sta\u202etus",
		"U+00AD inside":         "sta\u00adtus",
		"U+2060 inside":         "sta\u2060tus",
		"U+E0001, a tag":        "status\U000e0001",
		"U+0600, Arabic":        "\u0600status",
		"U+200D, a zero joiner": "sta\u200dtus",
	}
	for _, kind := range []string{"property", "field"} {
		for label, name := range bad {
			err := ValidateRelicName(kind, name)
			requireValidation(t, kind+" name with "+label, err)
			if err != nil && !strings.Contains(err.Error(), "format characters") {
				t.Errorf("%s name with %s: message %q, want it to say a name holds no format characters", kind, label, err)
			}
		}
		// Letters outside ASCII and visible marks are kept.
		for _, name := range []string{"stätus", "статус", "状態", "status·2"} {
			if err := ValidateRelicName(kind, name); err != nil {
				t.Errorf("%s name %q: %v", kind, name, err)
			}
		}
	}

	// On read each is corrupt data, in the state and in a part's fields.
	for label, name := range bad {
		state := goldenRelicState()
		state.Relic.Properties = map[string]json.RawMessage{name: json.RawMessage(`1`)}
		requireRelicStateRefused(t, "a stored property name with "+label, state)

		fields := NewRelicFields(map[string]json.RawMessage{name: json.RawMessage(`1`)})
		_, err := DecodeRelicFieldsDocument(marshalRelicDocument(t, fields))
		requireCorruptData(t, "a stored field name with "+label, err)
	}
}
