package core

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var (
	relicLinkL1    = relicTestID(5001)
	relicLinkL2    = relicTestID(5002)
	relicLinkL3    = relicTestID(5003)
	relicLinkL4    = relicTestID(5004)
	relicLinkTask1 = "WB-" + relicTestID(5101)
	relicLinkTask2 = "WB-" + relicTestID(5102)
)

func relicLinkAdd(id, from, task, rel string) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationLinkAdd,
		Link: &RelicLinkChange{From: from, To: RelicLinkTarget{Task: task}, Rel: rel}}
}

func relicLinkRemove(id, linkID string) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationLinkRemove, LinkID: linkID}
}

// relicLinksJSON is a links.json holding the given link members exactly as
// written, for documents no Workbook writer would produce.
func relicLinksJSON(links ...string) []byte {
	return []byte(`{"format":"workbook.relic-links","version":1,"links":[` + strings.Join(links, ",") + `]}` + "\n")
}

func goldenRelicLinksJSON() []byte {
	return relicLinksJSON(
		`{"id":"`+relicLinkL1+`","from":"`+relicDocPartB+`","to":{"task":"`+relicLinkTask1+`"},"rel":"implemented-by"}`,
		`{"id":"`+relicLinkL2+`","to":{"task":"`+relicLinkTask2+`"},"rel":"relates-to"}`,
	)
}

func relicLinkIDs(links RelicLinksDocument) []string {
	ids := make([]string, 0, len(links.Links))
	for _, link := range links.Links {
		ids = append(ids, link.ID)
	}
	return ids
}

func requireRelicLinkIDs(t *testing.T, label string, links RelicLinksDocument, want ...string) {
	t.Helper()
	if got := relicLinkIDs(links); len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Errorf("%s: links = %v, want %v", label, got, want)
	}
}

func encodedRelicLinks(t *testing.T, links RelicLinksDocument) []byte {
	t.Helper()
	encoded, err := EncodeDocument(links)
	if err != nil {
		t.Fatalf("EncodeDocument(links): %v", err)
	}
	return encoded
}

func TestRelicLinksDocumentIsStrict(t *testing.T) {
	golden := goldenRelicLinksJSON()
	decoded, err := DecodeRelicLinksDocument(golden)
	if err != nil {
		t.Fatalf("DecodeRelicLinksDocument(golden): %v", err)
	}
	want := RelicLinksDocument{Format: "workbook.relic-links", Version: 1, Links: []RelicLink{
		{ID: relicLinkL1, From: relicDocPartB, To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "implemented-by"},
		{ID: relicLinkL2, To: RelicLinkTarget{Task: relicLinkTask2}, Rel: "relates-to"},
	}}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded golden = %#v, want %#v", decoded, want)
	}
	if encoded := encodedRelicLinks(t, decoded); !bytes.Equal(encoded, golden) {
		t.Fatalf("golden links re-encoded:\n got %s\nwant %s", encoded, golden)
	}
	if link, ok := decoded.Link(relicLinkL2); !ok || link.Rel != "relates-to" {
		t.Errorf("Link(L2) = %#v, %v", link, ok)
	}
	if _, ok := decoded.Link(relicLinkL3); ok {
		t.Error("Link(unknown) found a link")
	}

	link := func(id, members string) string { return `{"id":"` + id + `",` + members + `}` }
	to1 := `"to":{"task":"` + relicLinkTask1 + `"}`
	for label, document := range map[string][]byte{
		"an unsorted list": relicLinksJSON(link(relicLinkL2, to1+`,"rel":"a"`), link(relicLinkL1, to1+`,"rel":"a"`)),
		"a duplicate ID":   relicLinksJSON(link(relicLinkL1, to1+`,"rel":"a"`), link(relicLinkL1, to1+`,"rel":"b"`)),
		"a non-ULID ID":    relicLinksJSON(link("link-1", to1+`,"rel":"a"`)),
		"a lowercase ID":   relicLinksJSON(link(strings.ToLower(relicLinkL1), to1+`,"rel":"a"`)),
		"a bad from":       relicLinksJSON(link(relicLinkL1, `"from":"part-1",`+to1+`,"rel":"a"`)),
		"to empty":         relicLinksJSON(link(relicLinkL1, `"to":{},"rel":"a"`)),
		"to null":          relicLinksJSON(link(relicLinkL1, `"to":null,"rel":"a"`)),
		"no to":            relicLinksJSON(link(relicLinkL1, `"rel":"a"`)),
		"to a relic": relicLinksJSON(link(relicLinkL1,
			`"to":{"task":"`+relicLinkTask1+`","relic":"`+relicDocRelic+`"},"rel":"a"`)),
		"to a part": relicLinksJSON(link(relicLinkL1,
			`"to":{"task":"`+relicLinkTask1+`","part":"`+relicDocPartA+`"},"rel":"a"`)),
		"to a relic ID as a task":      relicLinksJSON(link(relicLinkL1, `"to":{"task":"`+relicDocRelic+`"},"rel":"a"`)),
		"to a bad task":                relicLinksJSON(link(relicLinkL1, `"to":{"task":"task-1"},"rel":"a"`)),
		"a pointer member":             relicLinksJSON(link(relicLinkL1, to1+`,"rel":"a","pointer":"x"`)),
		"a removed member":             relicLinksJSON(link(relicLinkL1, to1+`,"rel":"a","removed":true`)),
		"a rel with a capital":         relicLinksJSON(link(relicLinkL1, to1+`,"rel":"Implemented-by"`)),
		"a rel of 101 bytes":           relicLinksJSON(link(relicLinkL1, to1+`,"rel":"`+strings.Repeat("a", 101)+`"`)),
		"no rel":                       relicLinksJSON(link(relicLinkL1, to1)),
		"null links":                   []byte(`{"format":"workbook.relic-links","version":1,"links":null}` + "\n"),
		"a link that is not an object": relicLinksJSON(`1`),
	} {
		_, err := DecodeRelicLinksDocument(document)
		requireCorruptData(t, label, err)
	}

	// A rel of exactly 100 bytes is the limit, not past it.
	hundred := relicLinksJSON(link(relicLinkL1, to1+`,"rel":"`+strings.Repeat("a", 100)+`"`))
	if _, err := DecodeRelicLinksDocument(hundred); err != nil {
		t.Errorf("a rel of 100 bytes: %v", err)
	}

	// What a writer could build in memory is refused when saved too.
	for label, links := range map[string][]RelicLink{
		"unsorted":      {want.Links[1], want.Links[0]},
		"duplicate":     {want.Links[0], want.Links[0]},
		"bad rel":       {{ID: relicLinkL1, To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "Implemented-by"}},
		"no target":     {{ID: relicLinkL1, Rel: "a"}},
		"bad from":      {{ID: relicLinkL1, From: "p", To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "a"}},
		"non-ULID ID":   {{ID: "L1", To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "a"}},
		"relic as task": {{ID: relicLinkL1, To: RelicLinkTarget{Task: relicDocRelic}, Rel: "a"}},
	} {
		_, err := EncodeDocument(RelicLinksDocument{Format: "workbook.relic-links", Version: 1, Links: links})
		requireCorruptData(t, label+" (encode)", err)
	}

	if err := ValidateRelicRelation("implemented-by"); err != nil {
		t.Errorf("ValidateRelicRelation(implemented-by): %v", err)
	}
	if err := ValidateRelicRelation(strings.Repeat("a", 101)); CategoryOf(err) != CategoryValidation ||
		!strings.Contains(err.Error(), "101 bytes") {
		t.Errorf("ValidateRelicRelation(101 bytes) = %v, want a validation error naming the size", err)
	}
	if err := ValidateRelicLinkTarget(RelicLinkTarget{}); CategoryOf(err) != CategoryValidation {
		t.Errorf("ValidateRelicLinkTarget(empty) = %v, want a validation error", err)
	}

	// Every from names a part of the state, live or removed.
	state := goldenRelicState()
	if err := ValidateRelicLinks(state, decoded); err != nil {
		t.Errorf("ValidateRelicLinks(golden): %v", err)
	}
	state.Relic.Parts = state.Relic.Parts[:1]
	if err := ValidateRelicLinks(state, decoded); CategoryOf(err) != CategoryCorruptData ||
		!strings.Contains(err.Error(), relicDocPartB) {
		t.Errorf("ValidateRelicLinks(a missing part) = %v, want corrupt data naming the part", err)
	}
	state.Relic.Parts[0].Removed = true
	fromRemoved := RelicLinksDocument{Format: "workbook.relic-links", Version: 1, Links: []RelicLink{
		{ID: relicLinkL1, From: relicDocPartA, To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "a"}}}
	if err := ValidateRelicLinks(state, fromRemoved); err != nil {
		t.Errorf("ValidateRelicLinks(from a removed part): %v", err)
	}
}

func TestRelicLinkOperationShapes(t *testing.T) {
	add := relicLinkAdd(relicLinkL1, relicDocPartA, relicLinkTask1, "implemented-by")
	remove := relicLinkRemove(relicTestID(5010), relicLinkL1)
	wantAdd := `{"id":"` + relicLinkL1 + `","type":"link.add","link":{"from":"` + relicDocPartA +
		`","to":{"task":"` + relicLinkTask1 + `"},"rel":"implemented-by"}}`
	wantRemove := `{"id":"` + relicTestID(5010) + `","type":"link.remove","linkId":"` + relicLinkL1 + `"}`
	for label, test := range map[string]struct {
		operation RelicOperation
		want      string
	}{"link.add": {add, wantAdd}, "link.remove": {remove, wantRemove}} {
		pack := singleRelicOperationPack(test.operation)
		if pack.MinReader != 6 || relicOperationMinReader[test.operation.Type] != RelicMinReader {
			t.Errorf("%s: minReader = %d, want 6", label, pack.MinReader)
		}
		encoded, err := EncodeDocument(pack)
		if err != nil {
			t.Errorf("%s: EncodeDocument: %v", label, err)
			continue
		}
		if !strings.Contains(string(encoded), `"operations":[`+test.want+`]`) {
			t.Errorf("%s: encoded %s, want the operation %s", label, encoded, test.want)
		}
		decoded, err := DecodeRelicOperationPack(encoded)
		if err != nil || !reflect.DeepEqual(decoded, pack) {
			t.Errorf("%s: DecodeRelicOperationPack = %#v, %v", label, decoded, err)
		}
	}
	whole := relicLinkAdd(relicLinkL2, "", relicLinkTask1, "relates-to")
	if _, err := EncodeDocument(singleRelicOperationPack(whole)); err != nil {
		t.Errorf("a whole-relic link.add: %v", err)
	}

	with := func(operation RelicOperation, change func(*RelicOperation)) RelicOperation {
		if operation.Link != nil {
			link := *operation.Link
			operation.Link = &link
		}
		change(&operation)
		return operation
	}
	for label, operation := range map[string]RelicOperation{
		"link.add without link":      with(add, func(o *RelicOperation) { o.Link = nil }),
		"link.add with linkId":       with(add, func(o *RelicOperation) { o.LinkID = relicLinkL2 }),
		"link.add with part":         with(add, func(o *RelicOperation) { o.Part = relicDocPartA }),
		"link.add with a position":   with(add, func(o *RelicOperation) { o.Position = "1/1" }),
		"link.remove without linkId": with(remove, func(o *RelicOperation) { o.LinkID = "" }),
		"link.remove with link": with(remove, func(o *RelicOperation) {
			o.Link = &RelicLinkChange{To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "a"}
		}),
		"link.remove with part":            with(remove, func(o *RelicOperation) { o.Part = relicDocPartA }),
		"link.remove of a non-ULID":        with(remove, func(o *RelicOperation) { o.LinkID = "L1" }),
		"linkId on part.remove":            {ID: relicTestID(5011), Type: RelicOperationPartRemove, Part: relicDocPartA, LinkID: relicLinkL1},
		"link on title.set":                {ID: relicTestID(5012), Type: RelicOperationTitleSet, Title: "T", Link: add.Link},
		"link.add with a bad rel":          with(add, func(o *RelicOperation) { o.Link.Rel = "Implemented By" }),
		"link.add with no rel":             with(add, func(o *RelicOperation) { o.Link.Rel = "" }),
		"link.add with a bad from":         with(add, func(o *RelicOperation) { o.Link.From = "part-1" }),
		"link.add with no target":          with(add, func(o *RelicOperation) { o.Link.To = RelicLinkTarget{} }),
		"link.add to a relic ID":           with(add, func(o *RelicOperation) { o.Link.To.Task = relicDocRelic }),
		"link.add with a rel of 101 bytes": with(add, func(o *RelicOperation) { o.Link.Rel = strings.Repeat("a", 101) }),
	} {
		requireRelicPackRefused(t, label, singleRelicOperationPack(operation))
	}

	// Members a later writer might add to a link are refused by this one.
	encoded, err := EncodeDocument(singleRelicOperationPack(add))
	if err != nil {
		t.Fatal(err)
	}
	for label, change := range map[string][2]string{
		"a pointer in link": {`"rel":"implemented-by"}`, `"rel":"implemented-by","pointer":"x"}`},
		"a relic target":    {`"to":{"task":`, `"to":{"relic":"` + relicDocRelic + `","task":`},
	} {
		_, err := DecodeRelicOperationPack(bytes.Replace(encoded, []byte(change[0]), []byte(change[1]), 1))
		requireCorruptData(t, label, err)
	}
}

// relicWithLinks folds the golden relic and a pack adding L1 (the whole
// relic, to task 1) and L2 (from part B, to task 2) at clock 2.
func relicWithLinks(t *testing.T) RelicRecord {
	t.Helper()
	return mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		relicLinkAdd(relicLinkL1, "", relicLinkTask1, "relates-to"),
		relicLinkAdd(relicLinkL2, relicDocPartB, relicLinkTask2, "implemented-by")))
}

func TestApplyRelicFoldsLinkAddAndRemove(t *testing.T) {
	root := goldenRelicRecord(t)
	linked := relicWithLinks(t)
	requireRelicLinkIDs(t, "after two adds", linked.Links, relicLinkL1, relicLinkL2)
	if got := linked.Links.Links[1]; !reflect.DeepEqual(got, RelicLink{ID: relicLinkL2, From: relicDocPartB,
		To: RelicLinkTarget{Task: relicLinkTask2}, Rel: "implemented-by"}) {
		t.Errorf("L2 = %#v", got)
	}
	unlinked := mustApplyRelic(t, linked, relicFoldPack(3, relicLinkRemove(relicTestID(5020), relicLinkL1)))
	requireRelicLinkIDs(t, "after removing L1", unlinked.Links, relicLinkL2)
	if !bytes.Equal(encodedRelicLinks(t, unlinked.Links), relicLinksJSON(
		`{"id":"`+relicLinkL2+`","from":"`+relicDocPartB+`","to":{"task":"`+relicLinkTask2+`"},"rel":"implemented-by"}`)) {
		t.Errorf("links.json = %s", encodedRelicLinks(t, unlinked.Links))
	}
	// The root's links are not changed by folding onto it.
	requireRelicLinkIDs(t, "the root", root.Links)

	// Link operations change nothing in state.json but the update time and
	// the clock.
	for label, record := range map[string]RelicRecord{"two adds": linked, "a remove": unlinked} {
		state := record.State
		state.LogicalClock = root.State.LogicalClock
		state.Relic.UpdatedAt = root.State.Relic.UpdatedAt
		got, err := EncodeDocument(state)
		if err != nil {
			t.Fatal(err)
		}
		want, err := EncodeDocument(root.State)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: state.json\n got %s\nwant %s", label, got, want)
		}
	}
	if err := ValidateRelicCheckpoint(&linked, relicFoldPack(3, relicLinkRemove(relicTestID(5020), relicLinkL1)), unlinked); err != nil {
		t.Errorf("ValidateRelicCheckpoint: %v", err)
	}
	stale := unlinked
	stale.Links = linked.Links
	if err := ValidateRelicCheckpoint(&linked, relicFoldPack(3, relicLinkRemove(relicTestID(5020), relicLinkL1)), stale); err == nil {
		t.Error("ValidateRelicCheckpoint accepted links that are not what the pack produces")
	}
}

func TestApplyRelicRefusesABadLink(t *testing.T) {
	linked := relicWithLinks(t)
	second := relicTestID(5030)
	edited := mustApplyRelic(t, linked, relicFoldPack(3, RelicOperation{ID: second, Type: RelicOperationPartBodySet,
		Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))
	for label, operation := range map[string]RelicOperation{
		"a from naming no part":             relicLinkAdd(relicTestID(5031), relicTestID(5999), relicLinkTask1, "a"),
		"a link.add reusing a live link":    relicLinkAdd(relicLinkL1, "", relicLinkTask2, "b"),
		"a link.add reusing a part's ID":    relicLinkAdd(relicDocPartB, "", relicLinkTask2, "b"),
		"a link.add reusing a body version": relicLinkAdd(second, "", relicLinkTask2, "b"),
		"a part.add reusing a live link":    {ID: relicLinkL2, Type: RelicOperationPartAdd, PartType: "note", Position: "9/1"},
		"a body edit reusing a live link": {ID: relicLinkL2, Type: RelicOperationPartBodySet, Part: relicDocPartA,
			Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: second}},
	} {
		_, err := ApplyRelic(&edited, relicFoldPack(4, operation))
		requireCorruptData(t, label, err)
		if err != nil && !strings.Contains(err.Error(), operation.ID) && !strings.Contains(err.Error(), relicTestID(5999)) {
			t.Errorf("%s: error %q names neither the ID nor the part", label, err)
		}
	}

	deleted := mustApplyRelic(t, linked, relicFoldPack(3, RelicOperation{ID: relicTestID(5032), Type: RelicOperationTombstone}))
	for label, operation := range map[string]RelicOperation{
		"a link.add on a deleted relic":    relicLinkAdd(relicTestID(5033), "", relicLinkTask1, "a"),
		"a link.remove on a deleted relic": relicLinkRemove(relicTestID(5034), relicLinkL1),
	} {
		_, err := ApplyRelic(&deleted, relicFoldPack(4, operation))
		requireCorruptData(t, label, err)
		_, err = ApplyRelic(&linked, relicFoldPack(3,
			RelicOperation{ID: relicTestID(5035), Type: RelicOperationTombstone}, operation))
		requireCorruptData(t, label+" after a tombstone in its pack", err)
	}

	// A link from a removed part applies: the part is still the relic's.
	removed := mustApplyRelic(t, linked, relicFoldPack(3,
		RelicOperation{ID: relicTestID(5036), Type: RelicOperationPartRemove, Part: relicDocPartA}))
	fromRemoved := mustApplyRelic(t, removed, relicFoldPack(4, relicLinkAdd(relicTestID(5037), relicDocPartA, relicLinkTask1, "a")))
	requireRelicLinkIDs(t, "a link from a removed part", fromRemoved.Links, relicLinkL1, relicLinkL2, relicTestID(5037))
}

func TestLinkRemoveOfAnAbsentLinkChangesNothing(t *testing.T) {
	linked := relicWithLinks(t)
	unknown := mustApplyRelic(t, linked, relicFoldPack(3, relicLinkRemove(relicTestID(5040), relicTestID(5998))))
	if !reflect.DeepEqual(unknown.Links, linked.Links) {
		t.Errorf("removing an unknown link changed the links to %v", relicLinkIDs(unknown.Links))
	}
	once := mustApplyRelic(t, linked, relicFoldPack(3, relicLinkRemove(relicTestID(5041), relicLinkL1)))
	twice := mustApplyRelic(t, once, relicFoldPack(4, relicLinkRemove(relicTestID(5042), relicLinkL1)))
	requireRelicLinkIDs(t, "removed once", once.Links, relicLinkL2)
	if !reflect.DeepEqual(twice.Links, once.Links) {
		t.Errorf("removing L1 again changed the links to %v", relicLinkIDs(twice.Links))
	}
	inOnePack := mustApplyRelic(t, linked, relicFoldPack(3,
		relicLinkRemove(relicTestID(5043), relicLinkL1), relicLinkRemove(relicTestID(5044), relicLinkL1)))
	if !reflect.DeepEqual(inOnePack.Links, once.Links) {
		t.Errorf("removing L1 twice in one pack left %v", relicLinkIDs(inOnePack.Links))
	}
}

func TestLinkFoldDoesNotDependOnPackOrder(t *testing.T) {
	parent := relicWithLinks(t)
	a := relicFoldPack(3,
		relicLinkAdd(relicLinkL3, relicDocPartA, relicLinkTask1, "implemented-by"),
		relicLinkRemove(relicTestID(5050), relicLinkL1))
	b := relicFoldPack(3,
		relicLinkAdd(relicLinkL4, "", relicLinkTask1, "implemented-by"),
		relicLinkRemove(relicTestID(5051), relicLinkL1),
		relicLinkRemove(relicTestID(5052), relicLinkL2))

	ab := mustApplyRelic(t, mustApplyRelic(t, parent, a), reclock(b, 4))
	ba := mustApplyRelic(t, mustApplyRelic(t, parent, b), reclock(a, 4))
	requireRelicLinkIDs(t, "A then B", ab.Links, relicLinkL3, relicLinkL4)
	if got, want := encodedRelicLinks(t, ba.Links), encodedRelicLinks(t, ab.Links); !bytes.Equal(got, want) {
		t.Errorf("B then A links.json\n got %s\nwant %s", got, want)
	}
}

// relicRecordWithLinkCount is the golden relic with count whole-relic links,
// folded in one pack, which the fold never refuses for a count.
func relicRecordWithLinkCount(t *testing.T, count int) RelicRecord {
	t.Helper()
	operations := make([]RelicOperation, count)
	for index := range operations {
		operations[index] = relicLinkAdd(relicTestID(60000+index), "", relicLinkTask1, "relates-to")
	}
	return mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, operations...))
}

func TestRelicLinkGrowthIsCheckedOnlyOnAdd(t *testing.T) {
	atLimit := relicRecordWithLinkCount(t, MaxRelicLinks)
	add := relicFoldPack(3, relicLinkAdd(relicTestID(70000), "", relicLinkTask2, "relates-to"))
	oneMore := mustApplyRelic(t, atLimit, add)
	err := checkRelicGrowth(relicTotalsOf(atLimit), relicTotalsOf(oneMore))
	if CategoryOf(err) != CategoryValidation ||
		err.Error() != "the relic would have 2001 links; the limit is 2000; remove a link first, or use another relic" {
		t.Errorf("one link past the limit: %v", err)
	}

	over := relicRecordWithLinkCount(t, MaxRelicLinks+5)
	encoded := encodedRelicLinks(t, over.Links)
	read, err := DecodeRelicLinksDocument(encoded)
	if err != nil || len(read.Links) != MaxRelicLinks+5 {
		t.Fatalf("reading 2,005 links = %d links, %v; want them read", len(read.Links), err)
	}
	if err := ValidateRelicLinks(over.State, read); err != nil {
		t.Fatalf("ValidateRelicLinks(2,005 links): %v", err)
	}
	removed := mustApplyRelic(t, over, relicFoldPack(3, relicLinkRemove(relicTestID(70001), relicTestID(60000))))
	if err := checkRelicGrowth(relicTotalsOf(over), relicTotalsOf(removed)); err != nil {
		t.Errorf("a remove at 2,005 links: %v", err)
	}
	added := mustApplyRelic(t, over, add)
	if err := checkRelicGrowth(relicTotalsOf(over), relicTotalsOf(added)); CategoryOf(err) != CategoryValidation ||
		err.Error() != "the relic would have 2006 links; the limit is 2000; it already had 2005, over the limit, "+
			"and only growth is refused; remove a link first, or use another relic" {
		t.Errorf("an add at 2,005 links: %v", err)
	}
	// A change that adds no link to a relic over the limit is not refused.
	retitled := mustApplyRelic(t, over, relicFoldPack(3, RelicOperation{ID: relicTestID(70002), Type: RelicOperationTitleSet, Title: "New"}))
	if err := checkRelicGrowth(relicTotalsOf(over), relicTotalsOf(retitled)); err != nil {
		t.Errorf("a title change at 2,005 links: %v", err)
	}

	warnings := RelicOverLimitWarnings(relicDocRelic, atLimit, oneMore)
	if len(warnings) != 1 || warnings[0].Code != WarningRelicOverLimit ||
		!strings.HasPrefix(warnings[0].Message, "relic "+relicDocRelic+": the relic has 2001 links; the limit is 2000") {
		t.Errorf("crossing the link limit warned %#v, want one relic-over-limit", warnings)
	}
	if warnings := RelicOverLimitWarnings(relicDocRelic, over, added); len(warnings) != 0 {
		t.Errorf("growing past the limit again warned %#v, want none", warnings)
	}
	if warnings := RelicOverLimitWarnings(relicDocRelic, relicRecordWithLinkCount(t, 3), atLimit); len(warnings) != 0 {
		t.Errorf("reaching the limit warned %#v, want none", warnings)
	}
}

func TestRelicLinkAddFromAPartRemovedElsewhereIsAnEdit(t *testing.T) {
	remove := relicFoldPack(2, RelicOperation{ID: relicTestID(5060), Type: RelicOperationPartRemove, Part: relicDocPartA})
	link := relicFoldPack(2, relicLinkAdd(relicTestID(5061), relicDocPartA, relicLinkTask1, "a"))
	whole := relicFoldPack(2, relicLinkAdd(relicTestID(5062), "", relicLinkTask1, "a"))
	result := mustApplyRelic(t, mustApplyRelic(t, goldenRelicRecord(t), remove), reclock(link, 3))
	warnings := RelicRemovedPartWarnings(relicDocRelic, []RelicOperationPack{link}, []RelicOperationPack{remove}, result)
	if len(warnings) != 1 || warnings[0].Code != WarningRelicEditOnRemovedPart ||
		!strings.Contains(warnings[0].Message, "part "+relicDocPartA) {
		t.Errorf("a link from a part removed elsewhere warned %#v, want one naming the part", warnings)
	}
	if warnings := RelicRemovedPartWarnings(relicDocRelic, []RelicOperationPack{whole}, []RelicOperationPack{remove}, result); len(warnings) != 0 {
		t.Errorf("a whole-relic link warned %#v, want none", warnings)
	}
}

// The members a stored link carries are pinned to these names; a change to
// the Go field tags would change the stored form.
func TestRelicLinkJSONMembers(t *testing.T) {
	encoded, err := json.Marshal(RelicLink{ID: "i", From: "f", To: RelicLinkTarget{Task: "t"}, Rel: "r"})
	if err != nil || string(encoded) != `{"id":"i","from":"f","to":{"task":"t"},"rel":"r"}` {
		t.Errorf("RelicLink encodes as %s, %v", encoded, err)
	}
	encoded, err = json.Marshal(RelicLinkChange{To: RelicLinkTarget{Task: "t"}, Rel: "r"})
	if err != nil || string(encoded) != `{"to":{"task":"t"},"rel":"r"}` {
		t.Errorf("RelicLinkChange encodes as %s, %v", encoded, err)
	}
}

var (
	relicLinkRelicX = "WB-R-" + relicTestID(5201)
	relicLinkRelicY = "WB-R-" + relicTestID(5202)
	relicLinkPartP1 = relicTestID(5211)
	relicLinkPartP2 = relicTestID(5212)
	relicLinkSeen   = relicTestID(5221)
)

// relicLinkToRelic is a link.add to relic, or to its part when part is not
// empty, with the seen version and pointer given.
func relicLinkToRelic(id, from, relic, part, rel string, pointer string) RelicOperation {
	change := &RelicLinkChange{From: from, To: RelicLinkTarget{Relic: relic, Part: part}, Rel: rel, Seen: relicLinkSeen}
	if pointer != "" {
		change.Pointer = json.RawMessage(pointer)
	}
	return RelicOperation{ID: id, Type: RelicOperationLinkAdd, Link: change}
}

func goldenRelicTargetLinksJSON() []byte {
	return relicLinksJSON(
		`{"id":"`+relicLinkL1+`","from":"`+relicDocPartB+`","to":{"task":"`+relicLinkTask1+`"},"rel":"implemented-by"}`,
		`{"id":"`+relicLinkL2+`","to":{"relic":"`+relicLinkRelicX+`"},"rel":"supersedes","seen":"`+relicLinkSeen+`"}`,
		`{"id":"`+relicLinkL3+`","from":"`+relicDocPartB+`","to":{"relic":"`+relicLinkRelicX+`","part":"`+relicLinkPartP1+
			`"},"rel":"decided-in","seen":"`+relicLinkSeen+`","pointer":{"at":"00:14:32","quote":"we agreed \u003c"}}`,
	)
}

func TestRelicLinkTargetsAreStrict(t *testing.T) {
	golden := goldenRelicTargetLinksJSON()
	decoded, err := DecodeRelicLinksDocument(golden)
	if err != nil {
		t.Fatalf("DecodeRelicLinksDocument(golden): %v", err)
	}
	want := []RelicLink{
		{ID: relicLinkL1, From: relicDocPartB, To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "implemented-by"},
		{ID: relicLinkL2, To: RelicLinkTarget{Relic: relicLinkRelicX}, Rel: "supersedes", Seen: relicLinkSeen},
		{ID: relicLinkL3, From: relicDocPartB, To: RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP1},
			Rel: "decided-in", Seen: relicLinkSeen, Pointer: json.RawMessage(`{"at":"00:14:32","quote":"we agreed \u003c"}`)},
	}
	if !reflect.DeepEqual(decoded.Links, want) {
		t.Fatalf("decoded golden = %#v, want %#v", decoded.Links, want)
	}
	if encoded := encodedRelicLinks(t, decoded); !bytes.Equal(encoded, golden) {
		t.Fatalf("golden links re-encoded:\n got %s\nwant %s", encoded, golden)
	}
	// A document holding only task links is stored exactly as before relic
	// targets existed.
	taskOnly := []byte(`{"format":"workbook.relic-links","version":1,"links":[{"id":"` + relicLinkL1 + `","from":"` +
		relicDocPartB + `","to":{"task":"` + relicLinkTask1 + `"},"rel":"implemented-by"},{"id":"` + relicLinkL2 +
		`","to":{"task":"` + relicLinkTask2 + `"},"rel":"relates-to"}]}` + "\n")
	if encoded := encodedRelicLinks(t, RelicLinksDocument{Format: "workbook.relic-links", Version: 1, Links: []RelicLink{
		{ID: relicLinkL1, From: relicDocPartB, To: RelicLinkTarget{Task: relicLinkTask1}, Rel: "implemented-by"},
		{ID: relicLinkL2, To: RelicLinkTarget{Task: relicLinkTask2}, Rel: "relates-to"},
	}}); !bytes.Equal(encoded, taskOnly) {
		t.Fatalf("task-only links encode as\n%s\nwant\n%s", encoded, taskOnly)
	}

	link := func(members string) []byte { return relicLinksJSON(`{"id":"` + relicLinkL1 + `",` + members + `}`) }
	seen := `,"seen":"` + relicLinkSeen + `"`
	for label, document := range map[string][]byte{
		"a task and a relic":     link(`"to":{"task":"` + relicLinkTask1 + `","relic":"` + relicLinkRelicX + `"},"rel":"a"` + seen),
		"a part alone":           link(`"to":{"part":"` + relicLinkPartP1 + `"},"rel":"a"` + seen),
		"a part with a task":     link(`"to":{"task":"` + relicLinkTask1 + `","part":"` + relicLinkPartP1 + `"},"rel":"a"`),
		"a bad relic ID":         link(`"to":{"relic":"WB-` + relicTestID(5201) + `"},"rel":"a"` + seen),
		"a non-canonical part":   link(`"to":{"relic":"` + relicLinkRelicX + `","part":"` + strings.ToLower(relicLinkPartP1) + `"},"rel":"a"` + seen),
		"a relic with no seen":   link(`"to":{"relic":"` + relicLinkRelicX + `"},"rel":"a"`),
		"a task with seen":       link(`"to":{"task":"` + relicLinkTask1 + `"},"rel":"a"` + seen),
		"a bad seen":             link(`"to":{"relic":"` + relicLinkRelicX + `"},"rel":"a","seen":"v1"`),
		"an unknown to member":   link(`"to":{"relic":"` + relicLinkRelicX + `","quote":"x"},"rel":"a"` + seen),
		"an unknown link member": link(`"to":{"relic":"` + relicLinkRelicX + `"},"rel":"a"` + seen + `,"changed":true`),
	} {
		_, err := DecodeRelicLinksDocument(document)
		requireCorruptData(t, label, err)
	}

	// The same targets are refused when saved and when an operation holds them.
	for label, target := range map[string]RelicLinkTarget{
		"neither":            {},
		"both":               {Task: relicLinkTask1, Relic: relicLinkRelicX},
		"a part alone":       {Part: relicLinkPartP1},
		"a part with a task": {Task: relicLinkTask1, Part: relicLinkPartP1},
		"a bad part":         {Relic: relicLinkRelicX, Part: "p1"},
	} {
		if err := ValidateRelicLinkTarget(target); CategoryOf(err) != CategoryValidation {
			t.Errorf("ValidateRelicLinkTarget(%s) = %v, want a validation error", label, err)
		}
		_, err := EncodeDocument(RelicLinksDocument{Format: "workbook.relic-links", Version: 1, Links: []RelicLink{
			{ID: relicLinkL1, To: target, Rel: "a", Seen: relicLinkSeen}}})
		requireCorruptData(t, label+" (encode)", err)
		requireRelicPackRefused(t, label+" (operation)", singleRelicOperationPack(RelicOperation{ID: relicLinkL1,
			Type: RelicOperationLinkAdd, Link: &RelicLinkChange{To: target, Rel: "a", Seen: relicLinkSeen}}))
	}
	for target, message := range map[RelicLinkTarget]string{
		{}: "a link must point at a task or a relic",
		{Task: relicLinkTask1, Relic: relicLinkRelicX}: "a link points at a task or a relic, not both",
		{Part: relicLinkPartP1}:                        "a link to a part must name its relic",
	} {
		if err := ValidateRelicLinkTarget(target); err == nil || err.Error() != message {
			t.Errorf("ValidateRelicLinkTarget(%#v) = %v, want %q", target, err, message)
		}
	}
	noSeen := relicLinkToRelic(relicLinkL1, "", relicLinkRelicX, "", "a", "")
	noSeen.Link.Seen = ""
	pack := singleRelicOperationPack(noSeen)
	requireRelicPackRefused(t, "a link.add to a relic with no seen", pack)
	if _, err := EncodeDocument(pack); err == nil || !strings.Contains(err.Error(), "a link to a relic must record the version it saw") {
		t.Errorf("a link.add to a relic with no seen: %v", err)
	}
	withSeen := relicLinkAdd(relicLinkL1, "", relicLinkTask1, "a")
	withSeen.Link.Seen = relicLinkSeen
	requireRelicPackRefused(t, "a link.add to a task with seen", singleRelicOperationPack(withSeen))

	// The stored operation names the target, seen and pointer as written.
	add := relicLinkToRelic(relicLinkL1, relicDocPartA, relicLinkRelicX, relicLinkPartP1, "decided-in", `{"at":"1"}`)
	encoded, err := EncodeDocument(singleRelicOperationPack(add))
	if err != nil {
		t.Fatalf("EncodeDocument(link.add to a part): %v", err)
	}
	wantOperation := `{"id":"` + relicLinkL1 + `","type":"link.add","link":{"from":"` + relicDocPartA + `","to":{"relic":"` +
		relicLinkRelicX + `","part":"` + relicLinkPartP1 + `"},"rel":"decided-in","seen":"` + relicLinkSeen + `","pointer":{"at":"1"}}}`
	if !strings.Contains(string(encoded), `"operations":[`+wantOperation+`]`) {
		t.Errorf("encoded %s, want the operation %s", encoded, wantOperation)
	}
	if decoded, err := DecodeRelicOperationPack(encoded); err != nil || !reflect.DeepEqual(decoded.Operations[0], add) {
		t.Errorf("DecodeRelicOperationPack = %#v, %v", decoded, err)
	}
}

func TestRelicLinkPointerIsStrict(t *testing.T) {
	stored := func(pointer string) []byte {
		return relicLinksJSON(`{"id":"` + relicLinkL1 + `","to":{"relic":"` + relicLinkRelicX + `"},"rel":"a","seen":"` +
			relicLinkSeen + `","pointer":` + pointer + `}`)
	}
	deep := strings.Repeat(`{"a":`, 33) + `1` + strings.Repeat(`}`, 33)
	for label, pointer := range map[string]string{
		"an array":         `[1]`,
		"a string":         `"x"`,
		"null":             `null`,
		"a number":         `1`,
		"unsorted members": `{"b":1,"a":2}`,
		"spaced":           `{"a": 1}`,
		"a bare <":         `{"a":"<"}`,
		"513 bytes":        `{"a":"` + strings.Repeat("x", 505) + `"}`,
		"nested 33 deep":   deep,
	} {
		_, err := DecodeRelicLinksDocument(stored(pointer))
		requireCorruptData(t, label, err)
		pack := singleRelicOperationPack(relicLinkToRelic(relicLinkL1, "", relicLinkRelicX, "", "a", pointer))
		if label == "spaced" || label == "a bare <" {
			// json.Marshal compacts and escapes a raw value, so only a
			// writer handing these over as they are can be refused.
			_, err := EncodeDocument(pack)
			requireCorruptData(t, label+" (operation)", err)
			continue
		}
		requireRelicPackRefused(t, label+" (operation)", pack)
	}
	limit := `{"a":"` + strings.Repeat("x", 504) + `"}`
	if len(limit) != 512 {
		t.Fatalf("the limit pointer is %d bytes", len(limit))
	}
	if _, err := DecodeRelicLinksDocument(stored(limit)); err != nil {
		t.Errorf("a 512-byte pointer: %v", err)
	}
	// A pointer is allowed on a task target too.
	task := relicLinksJSON(`{"id":"` + relicLinkL1 + `","to":{"task":"` + relicLinkTask1 + `"},"rel":"a","pointer":{"at":"1"}}`)
	if _, err := DecodeRelicLinksDocument(task); err != nil {
		t.Errorf("a pointer on a task link: %v", err)
	}

	canonical, err := CanonicalRelicLinkPointer([]byte(`{"b":1, "a":"<"}`))
	if err != nil || string(canonical) != `{"a":"\u003c","b":1}` {
		t.Errorf("CanonicalRelicLinkPointer = %s, %v", canonical, err)
	}
	for label, test := range map[string]struct{ raw, message string }{
		"an array":  {`[1]`, "a link pointer must be a JSON object"},
		"null":      {`null`, "a link pointer must be a JSON object"},
		"513 bytes": {`{"a":"` + strings.Repeat("x", 505) + `"}`, "the link pointer is 513 bytes; the limit is 512 bytes"},
		"not JSON":  {`{`, "the value is not valid JSON"},
	} {
		_, err := CanonicalRelicLinkPointer([]byte(test.raw))
		if CategoryOf(err) != CategoryValidation || !strings.Contains(err.Error(), test.message) {
			t.Errorf("CanonicalRelicLinkPointer(%s) = %v, want a validation error %q", label, err, test.message)
		}
	}
	// Escapes count: the size is of the canonical bytes.
	escaped := `{"a":"` + strings.Repeat("<", 85) + `"}`
	if _, err := CanonicalRelicLinkPointer([]byte(escaped)); err == nil || !strings.Contains(err.Error(), "512 bytes") {
		t.Errorf("a pointer whose escapes take it past 512 bytes: %v", err)
	}
}

func TestApplyRelicFoldsRelicLinks(t *testing.T) {
	root := goldenRelicRecord(t)
	self := RelicOperation{ID: relicLinkL3, Type: RelicOperationLinkAdd, Link: &RelicLinkChange{From: relicDocPartA,
		To: RelicLinkTarget{Relic: relicDocRelic, Part: relicDocPartB}, Rel: "relates-to", Seen: relicLinkSeen}}
	linked := mustApplyRelic(t, root, relicFoldPack(2,
		relicLinkToRelic(relicLinkL1, "", relicLinkRelicX, "", "supersedes", ""),
		relicLinkToRelic(relicLinkL2, relicDocPartB, relicLinkRelicX, relicLinkPartP1, "decided-in", `{"at":"00:14:32","quote":"q"}`),
		self))
	requireRelicLinkIDs(t, "after three adds", linked.Links, relicLinkL1, relicLinkL2, relicLinkL3)
	unlinked := mustApplyRelic(t, linked, relicFoldPack(3, relicLinkRemove(relicTestID(5230), relicLinkL1)))
	want := relicLinksJSON(
		`{"id":"`+relicLinkL2+`","from":"`+relicDocPartB+`","to":{"relic":"`+relicLinkRelicX+`","part":"`+relicLinkPartP1+
			`"},"rel":"decided-in","seen":"`+relicLinkSeen+`","pointer":{"at":"00:14:32","quote":"q"}}`,
		`{"id":"`+relicLinkL3+`","from":"`+relicDocPartA+`","to":{"relic":"`+relicDocRelic+`","part":"`+relicDocPartB+
			`"},"rel":"relates-to","seen":"`+relicLinkSeen+`"}`,
	)
	if got := encodedRelicLinks(t, unlinked.Links); !bytes.Equal(got, want) {
		t.Errorf("links.json\n got %s\nwant %s", got, want)
	}
	if err := ValidateRelicCheckpoint(&linked, relicFoldPack(3, relicLinkRemove(relicTestID(5230), relicLinkL1)), unlinked); err != nil {
		t.Errorf("ValidateRelicCheckpoint: %v", err)
	}
	// A link to a part the target relic may not have, or to a relic this
	// clone lacks, folds the same: the fold reads no target.
	missing := mustApplyRelic(t, root, relicFoldPack(2,
		relicLinkToRelic(relicLinkL4, "", relicLinkRelicY, relicTestID(5999), "a", "")))
	requireRelicLinkIDs(t, "a link to an unknown part", missing.Links, relicLinkL4)
}

func TestRelicLinkSourcesCoverPartsOfAWholeRelicTarget(t *testing.T) {
	record := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		relicLinkToRelic(relicLinkL1, "", relicLinkRelicX, "", "a", ""),
		relicLinkToRelic(relicLinkL2, relicDocPartA, relicLinkRelicX, relicLinkPartP1, "a", `{"at":"1"}`),
		relicLinkToRelic(relicLinkL3, "", relicLinkRelicX, relicLinkPartP2, "a", ""),
		relicLinkToRelic(relicLinkL4, "", relicLinkRelicY, relicLinkPartP1, "a", "")))
	snapshot := RelicSnapshot{Record: record}
	ids := func(sources []RelicLinkSource) []string {
		got := []string{}
		for _, source := range sources {
			got = append(got, source.Link.ID)
		}
		return got
	}
	for label, test := range map[string]struct {
		target RelicLinkTarget
		want   []string
	}{
		"X":    {RelicLinkTarget{Relic: relicLinkRelicX}, []string{relicLinkL1, relicLinkL2, relicLinkL3}},
		"X#P1": {RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP1}, []string{relicLinkL2}},
		"Y":    {RelicLinkTarget{Relic: relicLinkRelicY}, []string{relicLinkL4}},
		"Y#P2": {RelicLinkTarget{Relic: relicLinkRelicY, Part: relicLinkPartP2}, []string{}},
		"task": {RelicLinkTarget{Task: relicLinkTask1}, []string{}},
	} {
		if got := ids(RelicLinkSourcesOf(snapshot, test.target)); !reflect.DeepEqual(got, test.want) {
			t.Errorf("RelicLinkSourcesOf(%s) = %v, want %v", label, got, test.want)
		}
	}
	if sources := RelicLinkSourcesOf(snapshot, RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP1}); len(sources) != 1 ||
		string(sources[0].Link.Pointer) != `{"at":"1"}` || sources[0].Link.From != relicDocPartA {
		t.Errorf("the X#P1 source = %#v", sources)
	}

	for label, test := range map[string]struct {
		target RelicLinkTarget
		want   string
	}{
		"task": {RelicLinkTarget{Task: relicLinkTask1}, relicLinkTask1},
		"X":    {RelicLinkTarget{Relic: relicLinkRelicX}, relicLinkRelicX},
		"X#P1": {RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP1}, relicLinkRelicX + "#" + relicLinkPartP1},
	} {
		if got := test.target.Address(); got != test.want {
			t.Errorf("%s.Address() = %q, want %q", label, got, test.want)
		}
	}
	whole, part := RelicLinkTarget{Relic: relicLinkRelicX}, RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP1}
	if !whole.Covers(whole) || !whole.Covers(part) || part.Covers(whole) || !part.Covers(part) ||
		part.Covers(RelicLinkTarget{Relic: relicLinkRelicX, Part: relicLinkPartP2}) ||
		whole.Covers(RelicLinkTarget{Relic: relicLinkRelicY}) || whole.Covers(RelicLinkTarget{Task: relicLinkTask1}) {
		t.Error("Covers does not cover exactly the relic and its parts")
	}
}
