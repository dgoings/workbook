package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// memoryRelicLinkIndex answers reverse lookups from a memory store's tips, as
// the Git form of the index does, and counts the lookups.
type memoryRelicLinkIndex struct {
	store   *memoryRelicStore
	targets []RelicLinkTarget
	unread  []string
}

func (m *memoryRelicLinkIndex) RelicLinksTo(_ context.Context, _ ProjectConfig, target RelicLinkTarget) (RelicLinkLookup, error) {
	m.targets = append(m.targets, target)
	lookup := RelicLinkLookup{Unread: m.unread}
	for _, snapshot := range m.store.snapshots {
		lookup.Sources = append(lookup.Sources, RelicLinkSourcesOf(snapshot, target)...)
	}
	SortRelicLinkSources(lookup.Sources)
	return lookup, nil
}

// incomingLinkRelics is relicLinkTestRelics with Z beside Y, a links index
// over the store, and these links to X: Y's part goals to X#P0 with a
// pointer, Z's whole relic to X, and Z's whole relic to X#P1.
func incomingLinkRelics(t *testing.T) (store *memoryRelicStore, index *memoryRelicLinkIndex, service RelicService, y, goals, z, x string, xParts []string) {
	t.Helper()
	store, service, y, goals, _, x, xParts = relicLinkTestRelics(t)
	ctx := context.Background()
	created, err := service.Create(ctx, RelicCreateInput{Title: "Decisions", Type: "decision-log"})
	if err != nil {
		t.Fatal(err)
	}
	z = created.Relic.ID
	if _, err := service.Update(ctx, y, []RelicEdit{{Kind: RelicEditAddLink, Target: x + "#" + xParts[0], From: goals,
		Rel: "decided-in", Pointer: json.RawMessage(`{"at":"00:14:32"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, z, []RelicEdit{
		{Kind: RelicEditAddLink, Target: x, Rel: "changes"},
		{Kind: RelicEditAddLink, Target: x + "#" + xParts[1], Rel: "relates-to"},
	}); err != nil {
		t.Fatal(err)
	}
	index = &memoryRelicLinkIndex{store: store}
	service.Links = index
	return store, index, service, y, goals, z, x, xParts
}

func incomingAddresses(links []RelicIncomingLink) []string {
	addresses := []string{}
	for _, link := range links {
		address := link.Relic
		if link.From != "" {
			address += "#" + link.From
		}
		target := "relic"
		if link.ToPart != "" {
			target = link.ToPart
		}
		addresses = append(addresses, address+" "+link.Rel+" "+target)
	}
	return addresses
}

func TestShowListsIncomingRelicLinks(t *testing.T) {
	store, index, service, y, goals, z, x, xParts := incomingLinkRelics(t)
	ctx := context.Background()
	show := func(address string, options RelicShowOptions) RelicDocument {
		t.Helper()
		before := len(index.targets)
		document, err := service.Show(ctx, address, options)
		if err != nil {
			t.Fatalf("Show(%s): %v", address, err)
		}
		if lookups := len(index.targets) - before; lookups != 1 {
			t.Fatalf("Show(%s) ran %d reverse lookups, want one", address, lookups)
		}
		return document
	}
	whole := show(x, RelicShowOptions{})
	want := []string{y + "#" + goals + " decided-in " + xParts[0], z + " changes relic", z + " relates-to " + xParts[1]}
	if got := incomingAddresses(whole.LinkedFrom); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("linkedFrom of X = %q, want %q", got, want)
	}
	first := whole.LinkedFrom[0]
	yLink := storedLinks(store, y)[0]
	if first.Title != store.snapshots[y].Record.State.Relic.Title || first.Type != "spec" || first.FromType != "section" ||
		first.Link != yLink.ID || first.Seen != yLink.Seen || string(first.Pointer) != `{"at":"00:14:32"}` {
		t.Fatalf("Y's incoming link = %+v", first)
	}
	encoded, err := json.Marshal(whole)
	if err != nil || !strings.Contains(string(encoded), `"linkedFrom":[{"relic":"`+y+`"`) {
		t.Fatalf("Show JSON = %s, %v", encoded, err)
	}

	if got := incomingAddresses(show(x+"#"+xParts[0], RelicShowOptions{}).LinkedFrom); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("linkedFrom of X#P0 = %q, want Y's one link", got)
	}
	if index.targets[len(index.targets)-1] != (RelicLinkTarget{Relic: x, Part: xParts[0]}) {
		t.Fatalf("Show(X#P0) looked up %+v", index.targets[len(index.targets)-1])
	}

	// A removed part's incoming links are listed only with its part.
	if _, err := service.Update(ctx, x, []RelicEdit{{Kind: RelicEditRemovePart, Part: xParts[1]}}); err != nil {
		t.Fatal(err)
	}
	if got := incomingAddresses(show(x, RelicShowOptions{}).LinkedFrom); len(got) != 2 {
		t.Fatalf("linkedFrom of X after removing P1 = %q, want two", got)
	}
	if got := incomingAddresses(show(x, RelicShowOptions{Removed: true}).LinkedFrom); len(got) != 3 {
		t.Fatalf("linkedFrom of X --removed = %q, want three", got)
	}

	// Links from a removed part and from a deleted relic are not live.
	if _, err := service.Update(ctx, y, []RelicEdit{{Kind: RelicEditRemovePart, Part: goals}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, z); err != nil {
		t.Fatal(err)
	}
	if got := show(x, RelicShowOptions{Removed: true}).LinkedFrom; len(got) != 0 {
		t.Fatalf("linkedFrom of X after removing Y's part and deleting Z = %+v, want none", got)
	}

	// Export never lists incoming links and asks the index nothing.
	before := len(index.targets)
	exported, err := service.Export(ctx, x)
	if err != nil || exported.LinkedFrom != nil || len(index.targets) != before {
		t.Fatalf("Export = %+v, %v after %d lookups", exported.LinkedFrom, err, len(index.targets)-before)
	}
	// A service with no index leaves them out.
	service.Links = nil
	if document, err := service.Show(ctx, x, RelicShowOptions{}); err != nil || document.LinkedFrom != nil {
		t.Fatalf("Show with no index = %+v, %v", document.LinkedFrom, err)
	}
}

func TestShowWarnsOfUnreadIncomingRelics(t *testing.T) {
	_, index, service, _, _, _, x, _ := incomingLinkRelics(t)
	unread := serviceTestConfig.Key + "-R-" + relicTestID(7301)
	index.unread = []string{unread}
	document, err := service.Show(context.Background(), x, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Warnings) != 1 || document.Warnings[0] != RelicLinksUnreadWarning(unread) {
		t.Fatalf("Show warnings = %#v", document.Warnings)
	}
}

func TestRelicCreateFromADocumentWithLinkedFromIsRefused(t *testing.T) {
	document := RelicDocument{Format: RelicDocumentFormat, Version: documentVersion, Title: "T", Type: "spec",
		Links: []RelicDocumentLink{}, LinkedFrom: []RelicIncomingLink{{Relic: "WB-R-X", Link: "L", Rel: "r"}}}
	_, err := RelicCreateInputFromDocument(document)
	requireRelicCategory(t, "create from a document with linkedFrom", err, CategoryValidation,
		"linkedFrom lists other relics' links and is not part of a relic; remove it")
}

func TestLinkedToARelicOrPart(t *testing.T) {
	_, _, service, y, goals, z, x, xParts := incomingLinkRelics(t)
	ctx := context.Background()
	ids := func(summaries []RelicSummary) []string {
		got := []string{}
		for _, summary := range summaries {
			got = append(got, summary.ID)
		}
		return got
	}
	summaries, warnings, err := service.LinkedTo(ctx, strings.ToLower(x), RelicListFilter{})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("LinkedTo(x in lower case) = %v, %v", warnings, err)
	}
	if got := ids(summaries); strings.Join(got, " ") != y+" "+z {
		t.Fatalf("LinkedTo(X) = %q, want Y and Z", got)
	}
	if matches := summaries[0].LinkedBy; len(matches) != 1 || matches[0].Part != goals || matches[0].ToPart != xParts[0] ||
		string(matches[0].Pointer) != `{"at":"00:14:32"}` {
		t.Fatalf("Y's matches = %+v", matches)
	}
	summaries, _, err = service.LinkedTo(ctx, x+"#"+strings.ToLower(xParts[1]), RelicListFilter{})
	if err != nil || strings.Join(ids(summaries), " ") != z || len(summaries[0].LinkedBy) != 1 {
		t.Fatalf("LinkedTo(X#P1 prefix) = %+v, %v", summaries, err)
	}

	// A full ID this clone lacks is looked up as given, and so is a full part
	// ID under it; a prefix must resolve.
	absent := serviceTestConfig.Key + "-R-" + relicTestID(7401)
	for _, address := range []string{absent, absent + "#" + relicTestID(7402)} {
		if summaries, warnings, err := service.LinkedTo(ctx, address, RelicListFilter{}); err != nil || len(summaries) != 0 || len(warnings) != 0 {
			t.Fatalf("LinkedTo(%s) = %+v, %v, %v; want nothing and no error", address, summaries, warnings, err)
		}
	}
	_, _, err = service.LinkedTo(ctx, absent[:len(absent)-3], RelicListFilter{})
	requireRelicCategory(t, "a prefix this clone lacks", err, CategoryNotFound)
	_, _, err = service.LinkedTo(ctx, "OTHER-R-"+relicTestID(7403), RelicListFilter{})
	requireRelicCategory(t, "another project's relic", err, CategoryValidation, "in another project")

	// A deleted relic's links are listed only with IncludeDeleted.
	if _, err := service.Delete(ctx, z); err != nil {
		t.Fatal(err)
	}
	if summaries, _, _ := service.LinkedTo(ctx, x, RelicListFilter{}); strings.Join(ids(summaries), " ") != y {
		t.Fatalf("LinkedTo(X) after deleting Z = %q", ids(summaries))
	}
	if summaries, _, _ := service.LinkedTo(ctx, x, RelicListFilter{IncludeDeleted: true}); len(summaries) != 2 {
		t.Fatalf("LinkedTo(X, all) after deleting Z = %q", ids(summaries))
	}
}

// F3: repeating a link that exists changes nothing even after its target was
// removed or deleted; a new link to it is still refused.
func TestRepeatedLinkToARemovedOrDeletedTargetChangesNothing(t *testing.T) {
	store, _, service, y, goals, z, x, xParts := incomingLinkRelics(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, x, []RelicEdit{{Kind: RelicEditAddLink, Target: z, Rel: "relates-to"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, x, []RelicEdit{{Kind: RelicEditRemovePart, Part: xParts[0]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, z); err != nil {
		t.Fatal(err)
	}
	writes := len(store.writes)
	for label, test := range map[string]struct {
		relic string
		edit  RelicEdit
	}{
		"a link to a removed part": {y, RelicEdit{Kind: RelicEditAddLink, Target: x + "#" + xParts[0], From: goals,
			Rel: "decided-in", Pointer: json.RawMessage(`{"at":"00:14:32"}`)}},
		"a link to a deleted relic": {x, RelicEdit{Kind: RelicEditAddLink, Target: z, Rel: "relates-to"}},
	} {
		result, err := service.Update(ctx, test.relic, []RelicEdit{test.edit})
		if err != nil || result.Changed {
			t.Fatalf("%s again: changed = %v, err = %v", label, result.Changed, err)
		}
		requireRelicWrites(t, label, store, writes)
	}
	_, err := service.Update(ctx, y, []RelicEdit{{Kind: RelicEditAddLink, Target: x + "#" + xParts[0], Rel: "other"}})
	requireRelicCategory(t, "a new link to a removed part", err, CategoryValidation, "is removed; restore it first")
	_, err = service.Update(ctx, y, []RelicEdit{{Kind: RelicEditAddLink, Target: z, Rel: "other"}})
	requireRelicCategory(t, "a new link to a deleted relic", err, CategoryValidation, "is deleted; restore it first")
}

// failingGetReader fails to read one relic.
type failingGetReader struct {
	RelicReader
	relicID string
}

func (r *failingGetReader) GetRelic(ctx context.Context, config ProjectConfig, relicID string) (RelicSnapshot, error) {
	if relicID == r.relicID {
		return RelicSnapshot{}, Errorf(CategoryCorruptData, "relic %s cannot be read", relicID)
	}
	return r.RelicReader.GetRelic(ctx, config, relicID)
}

// F7: a target relic that cannot be read is marked missing, with exactly one
// warning naming it; a retired target is retired and not removed.
func TestShowMarksUnreadAndRetiredRelicTargets(t *testing.T) {
	store, _, service, y, _, z, x, xParts := incomingLinkRelics(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, y, []RelicEdit{
		{Kind: RelicEditAddLink, Target: x + "#" + xParts[1], Rel: "relates-to"},
		{Kind: RelicEditAddLink, Target: z, Rel: "relates-to"},
	}); err != nil {
		t.Fatal(err)
	}
	service.Reader = &failingGetReader{RelicReader: store, relicID: x}
	document, err := service.Show(ctx, y, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range document.Links {
		want := RelicLinkTargetMissing
		if link.To.Relic == z {
			want = ""
		}
		if link.TargetState != want || link.TargetRemoved != (want != "") {
			t.Errorf("link to %s = %q, removed %v; want %q", link.To.Address(), link.TargetState, link.TargetRemoved, want)
		}
	}
	if len(document.Warnings) != 1 || document.Warnings[0] != RelicLinksUnreadWarning(x) {
		t.Fatalf("Show warnings = %#v, want one naming X", document.Warnings)
	}

	service.Reader = store
	snapshot := store.snapshots[z]
	snapshot.Record.State.Relic.Retired = true
	store.snapshots[z] = snapshot
	document, err = service.Show(ctx, y, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range document.Links {
		if link.To.Relic == z && (link.TargetState != RelicLinkTargetRetired || link.TargetRemoved) {
			t.Fatalf("the link to a retired relic = %+v", link)
		}
	}
}

// F7: Export reads no relic but the one exported, even when it links to
// relics.
func TestExportOfARelicWithRelicLinksReadsNoTarget(t *testing.T) {
	store, _, service, y, _, _, x, _ := incomingLinkRelics(t)
	reader := &countingRelicReader{RelicReader: &failingGetReader{RelicReader: store, relicID: x}, gets: map[string]int{}}
	service.Reader = reader
	exported, err := service.Export(context.Background(), y)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Links) != 1 || exported.Links[0].TargetState != "" || len(exported.Warnings) != 0 {
		t.Fatalf("Export = links %+v, warnings %+v", exported.Links, exported.Warnings)
	}
	if len(reader.gets) != 1 || reader.gets[y] != 1 {
		t.Fatalf("Export read %v, want only Y", reader.gets)
	}
}

// F8: showing a relic that links to itself reads it once.
func TestShowOfASelfLinkedRelicReadsItOnce(t *testing.T) {
	store, _, service, y, goals, _, _, _ := incomingLinkRelics(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, y, []RelicEdit{{Kind: RelicEditAddLink, Target: y + "#" + goals, Rel: "summarizes"}}); err != nil {
		t.Fatal(err)
	}
	reader := &countingRelicReader{RelicReader: store, gets: map[string]int{}}
	service.Reader = reader
	document, err := service.Show(ctx, y, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reader.gets[y] != 1 {
		t.Fatalf("Show of a self-linked relic read it %d times, want once", reader.gets[y])
	}
	var self int
	for _, link := range document.Links {
		if link.To.Relic == y {
			self++
			if link.TargetState != "" {
				t.Fatalf("the self-link = %+v", link)
			}
		}
	}
	if self != 1 {
		t.Fatalf("Show listed %d self-links, want 1", self)
	}
}

// F4: a failed relic listing names relics, not tasks.
func TestRelicTargetsUnreadWarningNamesRelics(t *testing.T) {
	store, _, service, y, _, _, x, _ := incomingLinkRelics(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, y, []RelicEdit{{Kind: RelicEditAddLink, Target: x, Rel: "relates-to"}}); err != nil {
		t.Fatal(err)
	}
	service.Reader = &failingHeadsReader{RelicReader: store}
	service.Links = nil
	document, err := service.Show(ctx, y, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Warnings) != 1 || !strings.Contains(document.Warnings[0].Message, "the relics cannot be listed") ||
		strings.Contains(document.Warnings[0].Message, "tasks") {
		t.Fatalf("Show warnings = %#v", document.Warnings)
	}
}
