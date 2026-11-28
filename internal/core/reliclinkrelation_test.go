package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const defaultRelationsText = "implemented-by, derived-from, summarizes, decided-in, changes, supersedes, relates-to"

func TestDefaultRelationshipNames(t *testing.T) {
	want := []string{"implemented-by", "derived-from", "summarizes", "decided-in", "changes", "supersedes", "relates-to"}
	if got := DefaultRelicRelationNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultRelicRelationNames() = %v, want %v", got, want)
	}
	if len(DefaultRelicRelationships) != len(want) {
		t.Fatalf("DefaultRelicRelationships holds %d names, want %d", len(DefaultRelicRelationships), len(want))
	}
	for index, relationship := range DefaultRelicRelationships {
		if relationship.Name != want[index] {
			t.Errorf("relationship %d = %q, want %q", index, relationship.Name, want[index])
		}
		if err := ValidateRelicRelation(relationship.Name); err != nil {
			t.Errorf("%s: %v", relationship.Name, err)
		}
		if strings.TrimSpace(relationship.Meaning) == "" {
			t.Errorf("%s has no meaning", relationship.Name)
		}
	}
	// The names are handed out as a copy, so a caller cannot change the list.
	DefaultRelicRelationNames()[0] = "changed"
	if DefaultRelicRelationNames()[0] != "implemented-by" {
		t.Fatal("changing the returned names changed the defaults")
	}

	adding := mustCompileSchema(t, "transcript", `{"links":{"relationships":["reviews","relates-to"]}}`)
	if got, want := adding.Relations(), append(append([]string{}, want...), "reviews"); !reflect.DeepEqual(got, want) {
		t.Errorf("Relations() of a schema adding reviews and relates-to = %v, want %v", got, want)
	}
	if got := mustCompileSchema(t, "transcript", `{}`).Relations(); !reflect.DeepEqual(got, want) {
		t.Errorf("Relations() of a schema adding none = %v, want the defaults", got)
	}
	var none *RelicSchema
	if got := none.Relations(); !reflect.DeepEqual(got, want) {
		t.Errorf("Relations() of no schema = %v, want the defaults", got)
	}
}

func TestCheckLinkWhere(t *testing.T) {
	schema := mustCompileSchema(t, "transcript",
		`{"links":{"relationships":["reviews"],"pointer":{"type":"object","properties":{"line":{"type":"string"}}}}}`)
	link := RelicLink{ID: relicTestID(1), To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "blocks", Pointer: json.RawMessage(`{"line":1}`)}
	if got, want := problemString(schema.CheckLink("link "+link.ID, link)),
		`link `+link.ID+` relationship "blocks": the relationship must be one of `+defaultRelationsText+`, reviews (relationships)`; got != want {
		t.Errorf("an unlisted relationship = %s\nwant %s", got, want)
	}
	link.Rel = "reviews"
	if got := problemString(schema.CheckLink("link", link)); !strings.HasPrefix(got, "link pointer: at /line: ") || !strings.HasSuffix(got, "(type)") {
		t.Errorf("a listed relationship with a bad pointer = %s", got)
	}
	link.Pointer = json.RawMessage(`{"line":"1"}`)
	if problem := schema.CheckLink("link", link); problem != nil {
		t.Errorf("a fitting link = %s", problem)
	}
}

// linkDirectly stores a link on the relic as a teammate's build could have
// written it, with no check of its relationship.
func linkDirectly(store *memoryRelicStore, relicID string, link RelicLink) {
	snapshot := store.snapshots[relicID]
	links := append([]RelicLink{}, snapshot.Record.Links.Links...)
	snapshot.Record.Links.Links = append(links, link)
	store.snapshots[relicID] = snapshot
}

// newLinkRelationService is a service with the built-ins, a project
// transcript schema that adds blocks, and the link tests' tasks.
func newLinkRelationService(t *testing.T) (*memoryRelicStore, RelicService) {
	t.Helper()
	store, schemas, service := newBuiltInTestService(t)
	schemas.put(t, "transcript", `{"links":{"relationships":["blocks"]}}`)
	service.Tasks = linkTestTasks()
	return store, service
}

func TestLinkAddChecksTheRelationship(t *testing.T) {
	store, service := newLinkRelationService(t)
	ctx := context.Background()
	create := func(input RelicCreateInput) string {
		t.Helper()
		created, err := service.Create(ctx, input)
		if err != nil {
			t.Fatalf("Create(%s): %v", input.Type, err)
		}
		return created.Relic.ID
	}
	open := create(RelicCreateInput{Title: "Notes", Type: "note"})
	spec := create(RelicCreateInput{Title: "Spec", Type: "spec", Properties: draftStatus()})
	transcript := create(RelicCreateInput{Title: "Meeting", Type: "transcript"})
	link := func(relicID, rel string) error {
		_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddLink, Target: linkTestTaskID(1), Rel: rel}})
		return err
	}

	if err := link(open, "implemented-by"); err != nil {
		t.Fatalf("a listed relationship on an open relic: %v", err)
	}
	writes := len(store.writes)
	err := link(open, "blocks")
	requireRelicCategory(t, "blocks on an open relic", err, CategoryValidation)
	if want := `link relationship "blocks" is not one Workbook knows; use one of ` + defaultRelationsText +
		`, or save a schema for note whose links.relationships adds it`; err.Error() != want {
		t.Errorf("open refusal = %q\nwant %q", err, want)
	}
	requireRelicWrites(t, "blocks on an open relic", store, writes)

	err = link(spec, "blocks")
	requireRelicCategory(t, "blocks under the built-in", err, CategoryValidation)
	if want := `relic does not fit schema workbook:spec:1: link relationship "blocks": the relationship must be one of ` +
		defaultRelationsText + ` (relationships)`; err.Error() != want {
		t.Errorf("schema refusal = %q\nwant %q", err, want)
	}
	requireRelicWrites(t, "blocks under the built-in", store, writes)
	if err := link(spec, "relates-to"); err != nil {
		t.Fatalf("a listed relationship under the built-in: %v", err)
	}
	if err := link(transcript, "blocks"); err != nil {
		t.Fatalf("a relationship the project schema adds: %v", err)
	}
	if err := link(transcript, "decided-in"); err != nil {
		t.Fatalf("a default under a schema that adds names: %v", err)
	}

	// A link that already exists with an unlisted name, added again, writes
	// nothing and is not refused, under a schema or none.
	for index, relicID := range []string{open, spec} {
		linkDirectly(store, relicID, RelicLink{ID: relicTestID(900 + index), To: RelicLinkTarget{Task: linkTestTaskID(2)}, Rel: "blocks"})
		writes := len(store.writes)
		result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddLink, Target: linkTestTaskID(2), Rel: "blocks"}})
		if err != nil || result.Changed {
			t.Errorf("re-adding an existing blocks link on %s = changed %v, %v", relicID, result.Changed, err)
		}
		requireRelicWrites(t, "re-adding an existing link", store, writes)
	}
}

// relicWithADirectLink is an open note relic, with a status, linked to task 1
// by a link written directly.
func relicWithADirectLink(t *testing.T, store *memoryRelicStore, service RelicService, rel string, pointer string) (string, RelicLink) {
	t.Helper()
	created, err := service.Create(context.Background(), RelicCreateInput{Title: "Notes", Type: "note", Properties: draftStatus()})
	if err != nil {
		t.Fatalf("Create(note): %v", err)
	}
	link := RelicLink{ID: relicTestID(950), To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: rel}
	if pointer != "" {
		link.Pointer = json.RawMessage(pointer)
	}
	linkDirectly(store, created.Relic.ID, link)
	return created.Relic.ID, link
}

func TestMigrateJudgesLinks(t *testing.T) {
	store, schemas, service := newBuiltInTestService(t)
	service.Tasks = linkTestTasks()
	ctx := context.Background()

	relicID, link := relicWithADirectLink(t, store, service, "blocks", "")
	writes := len(store.writes)
	_, err := service.Migrate(ctx, relicID, "workbook:spec:1")
	requireRelicCategory(t, "migrating a relic with a blocks link", err, CategoryValidation)
	if want := `relic does not fit schema workbook:spec:1: link ` + link.ID + ` relationship "blocks": the relationship must be one of ` +
		defaultRelationsText + ` (relationships)`; err.Error() != want {
		t.Errorf("migrate refusal = %q\nwant %q", err, want)
	}
	requireRelicWrites(t, "the refused migrate", store, writes)
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemoveLink, Link: link.ID}}); err != nil {
		t.Fatalf("unlinking: %v", err)
	}
	if migrated, err := service.Migrate(ctx, relicID, "workbook:spec:1"); err != nil || !migrated.Changed {
		t.Fatalf("migrating after the unlink = %+v, %v", migrated, err)
	}

	schemas.put(t, "transcript", `{"links":{"pointer":{"type":"object","properties":{"line":{"type":"string"}}}}}`)
	other, pointed := relicWithADirectLink(t, store, service, "relates-to", `{"line":1}`)
	_, err = service.Migrate(ctx, other, "transcript")
	requireRelicCategory(t, "migrating a relic whose link pointer breaks the rule", err, CategoryValidation,
		"relic does not fit schema transcript version 1: link "+pointed.ID+" pointer: at /line: ", "(type)")
}

func TestMigrateToTheNamedVersionReportsItsLinks(t *testing.T) {
	store, _, service := newBuiltInTestService(t)
	ctx := context.Background()
	created, err := service.Create(ctx, RelicCreateInput{Title: "Spec", Type: "spec", Properties: draftStatus()})
	if err != nil {
		t.Fatal(err)
	}
	link := RelicLink{ID: relicTestID(960), To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "blocks"}
	linkDirectly(store, created.Relic.ID, link)
	result, err := service.Migrate(ctx, created.Relic.ID, "workbook:spec:1")
	if err != nil || result.Changed {
		t.Fatalf("migrating to the version named = %+v, %v", result, err)
	}
	if got := result.Relic.RelicConformance; got == nil || got.State != RelicConformanceNotConforming ||
		!strings.HasPrefix(got.Problem, "link "+link.ID+` relationship "blocks": `) {
		t.Fatalf("conformance = %+v, want not-conforming naming the link", got)
	}
}

func TestConformanceJudgesLinks(t *testing.T) {
	store, _, service := newBuiltInTestService(t)
	service.Tasks = linkTestTasks()
	ctx := context.Background()
	part := relicTestID(970)
	snapshot := putRelicNaming(t, store, 1, "spec", "workbook:spec:1", part)
	relicID := snapshot.Record.State.RelicID
	link := RelicLink{ID: relicTestID(971), To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "blocks"}
	linkDirectly(store, relicID, link)

	judged := func(label, want string) {
		t.Helper()
		shown, err := service.Show(ctx, relicID, RelicShowOptions{})
		if err != nil {
			t.Fatalf("%s: Show: %v", label, err)
		}
		if shown.RelicConformance.State != RelicConformanceNotConforming || !strings.HasPrefix(shown.RelicConformance.Problem, want) {
			t.Fatalf("%s: Show conformance = %+v, want not-conforming at %q", label, shown.RelicConformance, want)
		}
		listed, err := service.List(ctx, RelicListFilter{})
		if err != nil || len(listed) != 1 || listed[0].RelicConformance == nil || *listed[0].RelicConformance != shown.RelicConformance {
			t.Fatalf("%s: List = %+v, %v; want %+v", label, listed, err, shown.RelicConformance)
		}
	}
	// Properties first, then parts, then links.
	judged("no status", `property "status": `)
	snapshot = store.snapshots[relicID]
	snapshot.Record.State.Relic.Properties = draftStatus()
	store.snapshots[relicID] = snapshot
	judged("a section with no title", "part "+part+" (section)")
	store.content[relicID][part] = RelicPartContent{Fields: map[string]json.RawMessage{"title": relicRawString("Goals")}}
	judged("a blocks link", "link "+link.ID+` relationship "blocks": the relationship must be one of `)

	// An open relic with the same link is open.
	open := putRelicNaming(t, store, 2, "note", "")
	linkDirectly(store, open.Record.State.RelicID, link)
	shown, err := service.Show(ctx, open.Record.State.RelicID, RelicShowOptions{})
	if err != nil || shown.RelicConformance.State != RelicConformanceOpen {
		t.Fatalf("an open relic with a blocks link = %+v, %v; want open", shown.RelicConformance, err)
	}
}
