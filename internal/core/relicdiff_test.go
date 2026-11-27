package core

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// diffSideBuilder builds one side of a comparison. Each part's content is given by
// its fields and body; the object IDs are derived from the content, so equal
// content has equal objects, and the content map holds every part.
type diffSideBuilder struct {
	data    RelicData
	links   []RelicLink
	fields  map[string]map[string]json.RawMessage
	bodies  map[string][]byte
	objects map[string]RelicPartObjectIDs
}

func newDiffSide(title string) *diffSideBuilder {
	return &diffSideBuilder{
		data:    RelicData{Title: title, Type: "spec", Properties: map[string]json.RawMessage{}, Parts: []RelicPart{}},
		fields:  map[string]map[string]json.RawMessage{},
		bodies:  map[string][]byte{},
		objects: map[string]RelicPartObjectIDs{},
	}
}

func (b *diffSideBuilder) property(name, value string) *diffSideBuilder {
	b.data.Properties[name] = json.RawMessage(value)
	return b
}

// part adds a part. A nil body means no body.
func (b *diffSideBuilder) part(id, position string, removed bool, fields map[string]json.RawMessage, format string, body []byte) *diffSideBuilder {
	part := RelicPart{ID: id, Type: "section", Position: position, Removed: removed}
	objects := RelicPartObjectIDs{Fields: "fields:" + canonicalFieldsKey(fields)}
	if body != nil {
		part.Body = &RelicBody{Format: format, Version: "v-" + string(body), Bytes: int64(len(body))}
		objects.Body = "blob:" + string(body)
	}
	b.data.Parts = append(b.data.Parts, part)
	b.fields[id] = fields
	b.bodies[id] = body
	b.objects[id] = objects
	return b
}

func (b *diffSideBuilder) link(id, from, task string) *diffSideBuilder {
	b.links = append(b.links, RelicLink{ID: id, From: from, To: RelicLinkTarget{Task: task}, Rel: "implements"})
	return b
}

func canonicalFieldsKey(fields map[string]json.RawMessage) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var key strings.Builder
	for _, name := range names {
		key.WriteString(name + "=" + string(fields[name]) + ";")
	}
	return key.String()
}

// side returns the built side with the content of only the given parts, or of
// every part when read is nil.
func (b *diffSideBuilder) side(read []string) RelicVersionSide {
	if err := sortRelicParts(b.data.Parts); err != nil {
		panic(err)
	}
	links := append([]RelicLink(nil), b.links...)
	sortRelicLinks(links)
	record := RelicRecord{
		State: RelicStateDocument{RelicID: relicDocRelic, Relic: b.data},
		Links: RelicLinksDocument{Links: links},
	}
	content := map[string]RelicPartContent{}
	for id := range b.objects {
		if read != nil && !containsString(read, id) {
			continue
		}
		content[id] = RelicPartContent{Fields: b.fields[id], Body: b.bodies[id], HasBody: b.bodies[id] != nil}
	}
	return RelicVersionSide{Record: &record, Objects: b.objects, Content: content}
}

// compareRead compares two sides the way a caller does: with content read
// only for the parts RelicPartsToRead lists.
func compareRead(from, to *diffSideBuilder, partID string, context int) RelicDiff {
	var fromSide, toSide RelicVersionSide
	if from != nil {
		fromSide = from.side([]string{})
	}
	toSide = to.side([]string{})
	fromRead, toRead := RelicPartsToRead(fromSide, toSide, partID)
	if from != nil {
		fromSide = from.side(append([]string{}, fromRead...))
	}
	toSide = to.side(append([]string{}, toRead...))
	return CompareRelicVersions(fromSide, toSide, partID, context)
}

func raw(value string) json.RawMessage { return json.RawMessage(value) }

func TestNonTextBodiesAreReportedBySize(t *testing.T) {
	part := relicTestID(201)
	binaryA, binaryB := []byte{0xff, 0xfe, 'a'}, []byte{0x80, 'b', 'c', 'd'}
	for _, test := range []struct {
		label    string
		from, to []byte
	}{
		{"text to binary", []byte("text\n"), binaryA},
		{"binary to binary", binaryA, binaryB},
		{"binary to text", binaryB, []byte("text\n")},
	} {
		from := newDiffSide("R").part(part, "1/1", false, nil, "bin", test.from)
		to := newDiffSide("R").part(part, "1/1", false, nil, "bin", test.to)
		diff := compareRead(from, to, part, -1)
		if len(diff.Parts) != 1 || diff.Parts[0].Body == nil {
			t.Fatalf("%s: parts = %+v", test.label, diff.Parts)
		}
		body := diff.Parts[0].Body
		if body.Text || body.Hunks != nil || body.From == nil || body.To == nil ||
			body.From.Bytes != int64(len(test.from)) || body.To.Bytes != int64(len(test.to)) {
			t.Errorf("%s: body = %+v, want sizes and no text", test.label, body)
		}
	}

	from := newDiffSide("R").part(part, "1/1", false, nil, "md", []byte("same\n"))
	to := newDiffSide("R").part(part, "1/1", false, nil, "txt", []byte("same\n"))
	diff := compareRead(from, to, part, 3)
	if len(diff.Parts) != 1 || diff.Parts[0].Body == nil {
		t.Fatalf("format change: parts = %+v", diff.Parts)
	}
	body := diff.Parts[0].Body
	if body.From.Format != "md" || body.To.Format != "txt" || body.Hunks != nil || !body.Text {
		t.Errorf("format change: body = %+v, want md → txt, text, no hunks", body)
	}
	from = newDiffSide("R").part(part, "1/1", false, nil, "md", binaryA)
	to = newDiffSide("R").part(part, "1/1", false, nil, "bin", binaryA)
	if body := compareRead(from, to, part, 3).Parts[0].Body; body.Text || body.Hunks != nil {
		t.Errorf("format change of a binary body: body = %+v, want not text", body)
	}
}

func TestCompareRelicVersionsWholeRelic(t *testing.T) {
	partA, partB, partC := relicTestID(211), relicTestID(212), relicTestID(213)
	linkOne, linkTwo := relicTestID(214), relicTestID(215)
	from := newDiffSide("Old title").
		property("status", `"draft"`).property("owner", `"ana"`).property("same", `1`).
		part(partA, "1/1", false, map[string]json.RawMessage{"kind": raw(`"goal"`), "keep": raw(`true`)}, "md", []byte("one\ntwo\nthree\n")).
		part(partB, "2/1", false, nil, "", nil).
		link(linkOne, partA, "WB-1")
	to := newDiffSide("New title").
		property("status", `"approved"`).property("same", `1`).
		part(partA, "1/1", false, map[string]json.RawMessage{"kind": raw(`"aim"`), "keep": raw(`true`)}, "md", []byte("one\n2\nthree\n")).
		part(partB, "3/1", true, nil, "", nil).
		part(partC, "2/1", false, map[string]json.RawMessage{"x": raw(`1`)}, "md", []byte("new\n")).
		link(linkTwo, "", "WB-2")
	diff := compareRead(from, to, "", 3)

	if diff.Relic != relicDocRelic || diff.Part != "" {
		t.Fatalf("diff names %q %q", diff.Relic, diff.Part)
	}
	wantChanges := []RelicValueChange{{Name: "title", Kind: ChangeSet, From: raw(`"Old title"`), To: raw(`"New title"`)}}
	if !reflect.DeepEqual(diff.Changes, wantChanges) {
		t.Errorf("changes = %+v", diff.Changes)
	}
	wantProperties := []RelicValueChange{
		{Name: "owner", Kind: ChangeRemoved, From: raw(`"ana"`)},
		{Name: "status", Kind: ChangeSet, From: raw(`"draft"`), To: raw(`"approved"`)},
	}
	if !reflect.DeepEqual(diff.Properties, wantProperties) {
		t.Errorf("properties = %+v", diff.Properties)
	}

	if len(diff.Parts) != 3 {
		t.Fatalf("parts = %+v, want A, C, B", diff.Parts)
	}
	a, c, b := diff.Parts[0], diff.Parts[1], diff.Parts[2]
	if a.ID != partA || a.Kind != RelicPartChanged || a.Moved != nil ||
		!reflect.DeepEqual(a.Fields, []RelicValueChange{{Name: "kind", Kind: ChangeSet, From: raw(`"goal"`), To: raw(`"aim"`)}}) {
		t.Errorf("part A = %+v", a)
	}
	if a.Body == nil || !a.Body.Text || len(a.Body.Hunks) != 1 || a.Body.Hunks[0].FromLines != 3 {
		t.Errorf("part A body = %+v", a.Body)
	}
	if c.ID != partC || c.Kind != ChangeAdded || c.Moved != nil || c.Body == nil || c.Body.From != nil ||
		c.Body.To == nil || len(c.Body.Hunks) != 1 ||
		!reflect.DeepEqual(c.Fields, []RelicValueChange{{Name: "x", Kind: ChangeAdded, To: raw(`1`)}}) {
		t.Errorf("part C = %+v", c)
	}
	if b.ID != partB || b.Kind != ChangeRemoved || b.Body != nil || b.Fields != nil ||
		!reflect.DeepEqual(b.Moved, &RelicPartMove{From: "2/1", To: "3/1", FromIndex: 1, ToIndex: -1}) {
		t.Errorf("part B = %+v, moved %+v", b, b.Moved)
	}

	wantLinks := &RelicLinksDiff{
		Added:   []RelicLink{{ID: linkTwo, To: RelicLinkTarget{Task: "WB-2"}, Rel: "implements"}},
		Removed: []RelicLink{{ID: linkOne, From: partA, To: RelicLinkTarget{Task: "WB-1"}, Rel: "implements"}},
	}
	if !reflect.DeepEqual(diff.Links, wantLinks) {
		t.Errorf("links = %+v", diff.Links)
	}

	// The same two versions compared the other way round.
	back := compareRead(to, from, "", 3)
	if len(back.Parts) != 3 || back.Parts[0].ID != partA || back.Parts[1].ID != partB || back.Parts[1].Kind != ChangeRestored ||
		back.Parts[2].ID != partC || back.Parts[2].Kind != ChangeRemoved || back.Parts[2].Body.To != nil {
		t.Errorf("reversed parts = %+v", back.Parts)
	}

	same := compareRead(from, from, "", 3)
	if same.Changes != nil || same.Properties != nil || same.Parts != nil || same.Links != nil {
		t.Errorf("equal versions = %+v", same)
	}
}

func TestCompareRelicVersionsOfOnePart(t *testing.T) {
	partA, partB := relicTestID(221), relicTestID(222)
	from := newDiffSide("Old").property("status", `"draft"`).
		part(partA, "1/1", false, map[string]json.RawMessage{"kind": raw(`"goal"`)}, "md", []byte("a\n"))
	to := newDiffSide("New").property("status", `"done"`).
		part(partA, "1/1", false, map[string]json.RawMessage{"kind": raw(`"goal"`)}, "md", []byte("b\n")).
		part(partB, "2/1", false, map[string]json.RawMessage{"y": raw(`2`)}, "", nil).
		link(relicTestID(223), partA, "WB-1")

	diff := compareRead(from, to, partA, 3)
	if diff.Part != partA || diff.Changes != nil || diff.Properties != nil || diff.Links != nil {
		t.Fatalf("part diff carries relic members: %+v", diff)
	}
	if len(diff.Parts) != 1 || diff.Parts[0].ID != partA || diff.Parts[0].Fields != nil || diff.Parts[0].Body == nil {
		t.Fatalf("part diff = %+v", diff.Parts)
	}

	added := compareRead(from, to, partB, 3)
	if len(added.Parts) != 1 || added.Parts[0].Kind != ChangeAdded || added.Parts[0].Body != nil ||
		!reflect.DeepEqual(added.Parts[0].Fields, []RelicValueChange{{Name: "y", Kind: ChangeAdded, To: raw(`2`)}}) {
		t.Fatalf("part absent on the from side = %+v", added.Parts)
	}

	unchanged := compareRead(to, to, partA, 3)
	if unchanged.Parts != nil {
		t.Fatalf("unchanged part = %+v", unchanged.Parts)
	}

	empty := CompareRelicVersions(RelicVersionSide{}, to.side(nil), "", 3)
	if len(empty.Parts) != 2 || empty.Parts[0].Kind != ChangeAdded || empty.Parts[1].Kind != ChangeAdded {
		t.Fatalf("from the empty side, parts = %+v", empty.Parts)
	}
	if empty.Parts[0].Body == nil || empty.Parts[0].Body.From != nil || empty.Parts[0].Body.Hunks[0].FromStart != 0 {
		t.Errorf("from the empty side, body = %+v", empty.Parts[0].Body)
	}
	if !reflect.DeepEqual(empty.Changes, []RelicValueChange{{Name: "title", Kind: ChangeAdded, To: raw(`"New"`)}}) ||
		!reflect.DeepEqual(empty.Properties, []RelicValueChange{{Name: "status", Kind: ChangeAdded, To: raw(`"done"`)}}) ||
		empty.Links == nil || len(empty.Links.Added) != 1 {
		t.Errorf("from the empty side = changes %+v, properties %+v, links %+v", empty.Changes, empty.Properties, empty.Links)
	}
}

func TestRelicPartsToReadSkipsEqualObjects(t *testing.T) {
	same, fieldsOnly, bodyEdit, added, formatOnly := relicTestID(231), relicTestID(232), relicTestID(233), relicTestID(234), relicTestID(235)
	from := newDiffSide("R").
		part(same, "1/1", false, map[string]json.RawMessage{"a": raw(`1`)}, "md", []byte("x\n")).
		part(fieldsOnly, "2/1", false, map[string]json.RawMessage{"a": raw(`1`)}, "md", []byte("x\n")).
		part(bodyEdit, "3/1", false, nil, "md", []byte("x\n")).
		part(formatOnly, "5/1", false, nil, "md", []byte("x\n")).
		side(nil)
	to := newDiffSide("R").
		part(same, "9/1", false, map[string]json.RawMessage{"a": raw(`1`)}, "md", []byte("x\n")).
		part(fieldsOnly, "2/1", false, map[string]json.RawMessage{"a": raw(`2`)}, "md", []byte("x\n")).
		part(bodyEdit, "3/1", true, nil, "md", []byte("y\n")).
		part(added, "4/1", false, nil, "md", []byte("z\n")).
		part(formatOnly, "5/1", false, nil, "txt", []byte("x\n")).
		side(nil)

	fromParts, toParts := RelicPartsToRead(from, to, "")
	if want := []string{fieldsOnly, bodyEdit}; !reflect.DeepEqual(fromParts, want) {
		t.Errorf("from parts = %v, want %v", fromParts, want)
	}
	if want := []string{fieldsOnly, bodyEdit, added, formatOnly}; !reflect.DeepEqual(toParts, want) {
		t.Errorf("to parts = %v, want %v", toParts, want)
	}
	if RelicBodiesToRead(from, to, []string{fieldsOnly}) || RelicBodiesToRead(to, from, []string{fieldsOnly}) {
		t.Error("a fields-only edit needs bodies")
	}
	if !RelicBodiesToRead(from, to, []string{fieldsOnly, bodyEdit}) || !RelicBodiesToRead(to, from, []string{added}) ||
		!RelicBodiesToRead(to, from, []string{formatOnly}) {
		t.Error("a differing body object, an added body and a new format need bodies")
	}

	fromParts, toParts = RelicPartsToRead(from, to, same)
	if fromParts != nil || toParts != nil {
		t.Errorf("one unchanged part = %v, %v; want nothing read", fromParts, toParts)
	}
	fromParts, toParts = RelicPartsToRead(RelicVersionSide{}, to, added)
	if fromParts != nil || !reflect.DeepEqual(toParts, []string{added}) {
		t.Errorf("a part only on the to side = %v, %v", fromParts, toParts)
	}
	fromParts, toParts = RelicPartsToRead(to, from, added)
	if !reflect.DeepEqual(fromParts, []string{added}) || toParts != nil {
		t.Errorf("a part only on the from side = %v, %v", fromParts, toParts)
	}
}

func TestRelicDiffJSONMembers(t *testing.T) {
	part := relicTestID(241)
	from := newDiffSide("R").part(part, "1/1", false, map[string]json.RawMessage{"a": raw(`1`)}, "md", []byte("a\nb"))
	to := newDiffSide("S").property("p", `true`).
		part(part, "2/1", false, map[string]json.RawMessage{"a": raw(`2`)}, "md", []byte("a\nc")).
		link(relicTestID(242), "", "WB-1")
	diff := compareRead(from, to, "", 3)
	diff.From = &RelicDiffPoint{Version: part, Commit: "c1", Actor: "ana@example.com"}
	diff.To = RelicDiffPoint{Version: relicTestID(243), Commit: "c2", Actor: "bo@example.com"}
	encoded, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "diff", top, "relic", "from", "to", "changes", "properties", "parts", "links")
	var point map[string]json.RawMessage
	_ = json.Unmarshal(top["to"], &point)
	requireMembers(t, "point", point, "version", "commit", "actor", "wallTime")
	var changes []map[string]json.RawMessage
	_ = json.Unmarshal(top["changes"], &changes)
	requireMembers(t, "change", changes[0], "name", "kind", "from", "to")

	var parts []map[string]json.RawMessage
	_ = json.Unmarshal(top["parts"], &parts)
	requireMembers(t, "part", parts[0], "id", "type", "kind", "moved", "fields", "body")
	var moved map[string]json.RawMessage
	_ = json.Unmarshal(parts[0]["moved"], &moved)
	requireMembers(t, "moved", moved, "from", "to", "fromIndex", "toIndex")
	var body map[string]json.RawMessage
	_ = json.Unmarshal(parts[0]["body"], &body)
	requireMembers(t, "body", body, "from", "to", "text", "hunks")
	var hunks []map[string]json.RawMessage
	_ = json.Unmarshal(body["hunks"], &hunks)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %s", body["hunks"])
	}
	requireMembers(t, "hunk", hunks[0], "fromStart", "fromLines", "toStart", "toLines", "lines")
	var lines []map[string]json.RawMessage
	_ = json.Unmarshal(hunks[0]["lines"], &lines)
	requireMembers(t, "equal line", lines[0], "kind", "text", "from", "to")
	requireMembers(t, "deleted line", lines[1], "kind", "text", "from", "noNewline")
	if _, ok := lines[1]["to"]; ok {
		t.Errorf("deleted line carries a to number: %v", keysOf(lines[1]))
	}
	if string(lines[1]["noNewline"]) != "true" || string(lines[1]["kind"]) != `"delete"` {
		t.Errorf("deleted line = %v", lines[1])
	}
	var links map[string]json.RawMessage
	_ = json.Unmarshal(top["links"], &links)
	requireMembers(t, "links", links, "added")

	// The side before the relic is null, and so is a missing body.
	diff.From = nil
	diff.Parts[0].Body.From = nil
	encoded, _ = json.Marshal(diff)
	if !strings.Contains(string(encoded), `"from":null`) || !strings.Contains(string(encoded), `"body":{"from":null`) {
		t.Errorf("empty side JSON %s lacks \"from\":null", encoded)
	}
	encoded, _ = json.Marshal(RelicDiff{Relic: relicDocRelic, Part: part})
	if strings.Contains(string(encoded), "parts") || strings.Contains(string(encoded), "changes") || !strings.Contains(string(encoded), `"part":`) {
		t.Errorf("equal part diff JSON = %s", encoded)
	}
}
