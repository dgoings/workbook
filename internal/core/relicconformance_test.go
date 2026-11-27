package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// memorySchemaStore is a RelicSchemaStore over a testSchemaSet. hidden holds
// versions this clone's listing does not show yet, and corrupt holds versions
// whose stored document cannot be read.
type memorySchemaStore struct {
	set     *testSchemaSet
	order   []RelicSchemaVersion
	hidden  map[string]bool
	corrupt map[string]bool
	reads   int
}

func newMemorySchemaStore() *memorySchemaStore {
	return &memorySchemaStore{set: newTestSchemaSet(), hidden: map[string]bool{}, corrupt: map[string]bool{}}
}

func (s *memorySchemaStore) put(t *testing.T, relicType, source string) RelicSchemaVersion {
	t.Helper()
	version, _, _ := s.set.put(t, relicType, source)
	s.order = append(s.order, version)
	return version
}

func (s *memorySchemaStore) RelicSchemaListing(context.Context, ProjectConfig) (RelicSchemaListing, error) {
	listing := RelicSchemaListing{}
	for _, version := range s.order {
		if !s.hidden[version.ID] {
			listing.Versions = append(listing.Versions, version)
		}
	}
	return listing, nil
}

func (s *memorySchemaStore) ReadRelicSchema(_ context.Context, _ ProjectConfig, version RelicSchemaVersion) (RelicSchemaDocument, *RelicSchema, error) {
	s.reads++
	if s.corrupt[version.ID] {
		return RelicSchemaDocument{}, nil, corrupt("the schema for %s version %d cannot be read", version.Type, version.Version)
	}
	schema, ok := s.set.byID[version.ID]
	if !ok {
		return RelicSchemaDocument{}, nil, corrupt("the schema for %s version %d is missing", version.Type, version.Version)
	}
	document, err := DecodeRelicSchemaDocument(s.set.stored[version.ID])
	return document, schema, err
}

// Schema versions the tests below save: spec 1 lets a relic have a status and
// an owner and asks a section for a heading; spec 2 also requires the status;
// plan 1 accepts anything.
const (
	specSchemaV1 = `{"properties":{"type":"object","properties":{"status":{"enum":["draft","approved"]},"owner":{"type":"string"}}},` +
		`"parts":{"section":{"fields":{"type":"object","required":["heading"],"properties":{"heading":{"type":"string"}}}},"note":{}}}`
	specSchemaV2 = `{"properties":{"type":"object","required":["status"],"properties":{"status":{"enum":["draft","approved"]},"owner":{"type":"string"}}},` +
		`"parts":{"section":{"fields":{"type":"object","required":["heading"],"properties":{"heading":{"type":"string"}}}},"note":{}}}`
	planSchemaV1 = `{}`
)

type schemaTestVersions struct{ spec1, spec2, plan1 RelicSchemaVersion }

func newSchemaTestService(t *testing.T) (*memoryRelicStore, *memorySchemaStore, RelicService, schemaTestVersions) {
	t.Helper()
	schemas := newMemorySchemaStore()
	versions := schemaTestVersions{
		spec1: schemas.put(t, "spec", specSchemaV1),
		spec2: schemas.put(t, "spec", specSchemaV2),
		plan1: schemas.put(t, "plan", planSchemaV1),
	}
	store := newMemoryRelicStore()
	service := newRelicTestService(store, relicServiceIDs(1, 200))
	service.Schemas = schemas
	return store, schemas, service, versions
}

// putRelicNaming stores a relic of the type, naming the schema version, with
// one section part per ID and no fields: each section lacks its heading.
func putRelicNaming(t *testing.T, store *memoryRelicStore, number int, relicType, schema string, partIDs ...string) RelicSnapshot {
	t.Helper()
	snapshot := relicSnapshotForTest(t, number, relicType, partIDs, false, false)
	snapshot.Record.State.Relic.Schema = schema
	store.put(snapshot)
	return snapshot
}

func lastWrittenPack(t *testing.T, store *memoryRelicStore) RelicOperationPack {
	t.Helper()
	if len(store.writes) == 0 {
		t.Fatal("nothing was written")
	}
	return store.writes[len(store.writes)-1].pack
}

func TestCreateNamesTheNewestVersionAndChecksTheWhole(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()

	_, err := service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec"})
	requireRelicCategory(t, "a create missing a required property", err, CategoryValidation)
	if want := `relic does not fit schema spec version 2: property "status": ` + missingRequiredMessage(t) + ` (required)`; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	_, err = service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec",
		Properties: map[string]json.RawMessage{"status": relicRawString("draft")},
		Parts:      []RelicPartInput{{Type: "note"}, {Type: "section"}}})
	requireRelicCategory(t, "a create whose second part lacks a field", err, CategoryValidation,
		"relic does not fit schema spec version 2: part 2 (section)", "(required)")
	_, err = service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec",
		Properties: map[string]json.RawMessage{"status": relicRawString("draft")},
		Parts:      []RelicPartInput{{Type: "decision"}}})
	requireRelicCategory(t, "a create with a part type the schema leaves out", err, CategoryValidation,
		"part 1 (decision): the schema allows no part of this type (parts)")
	requireRelicWrites(t, "refused creates", store, 0)

	created, err := service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec",
		Properties: map[string]json.RawMessage{"status": relicRawString("draft")},
		Parts:      []RelicPartInput{{Type: "section", Fields: map[string]json.RawMessage{"heading": relicRawString("Goals")}}}})
	if err != nil {
		t.Fatalf("Create(fits): %v", err)
	}
	if got := lastWrittenPack(t, store).Operations[0].Relic.Schema; got != versions.spec2.ID {
		t.Errorf("relic.create schema = %q, want spec version 2 %s", got, versions.spec2.ID)
	}
	if ref := created.Relic.Schema; ref == nil || *ref != (RelicSchemaRef{Type: "spec", ID: versions.spec2.ID, Version: 2}) {
		t.Errorf("created relic names %+v, want spec version 2", ref)
	}
}

// missingRequiredMessage is the message the schema checker gives for a
// required property that is absent, read from the checker itself so this test
// pins the refusal's frame and not the checker's own wording.
func missingRequiredMessage(t *testing.T) string {
	t.Helper()
	problem := mustCompileSchema(t, "spec", specSchemaV2).CheckRelic(RelicData{Properties: map[string]json.RawMessage{}}, nil)
	if problem == nil || problem.Where != `property "status"` || problem.Keyword != "required" {
		t.Fatalf("CheckRelic(no status) = %+v", problem)
	}
	return problem.Message
}

func TestCreateUnderAnOpenTypeIsUnchecked(t *testing.T) {
	store, _, service, _ := newSchemaTestService(t)
	input := relicCreateInputForTest()
	input.Type = "note"
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create(open type): %v", err)
	}
	if created.Relic.Schema != nil || lastWrittenPack(t, store).Operations[0].Relic.Schema != "" {
		t.Errorf("an open type's relic names %+v", created.Relic.Schema)
	}
	encoded, _ := json.Marshal(created.Relic)
	if !strings.Contains(string(encoded), `"schema":null`) {
		t.Errorf("summary = %s, want \"schema\":null", encoded)
	}
}

func TestUpdateChecksOnlyThePieceSaved(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	p1, p2 := relicTestID(11), relicTestID(12)
	relic := putRelicNaming(t, store, 1, "spec", versions.spec2.ID, p1, p2)
	relicID := relic.Record.State.RelicID

	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("draft")}}); err != nil {
		t.Fatalf("setting a valid property on a relic that breaks a rule elsewhere: %v", err)
	}
	_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("wip")}})
	requireRelicCategory(t, "a property outside its enum", err, CategoryValidation,
		`relic does not fit schema spec version 2: property "status": `, "(enum)")
	_, err = service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditTitle, Title: "Renamed"},
		{Kind: RelicEditPart, Part: p1, Fields: []RelicFieldEdit{{Name: "other", Value: json.RawMessage(`1`)}}},
	})
	requireRelicCategory(t, "editing the broken part without fixing it", err, CategoryValidation,
		"relic does not fit schema spec version 2: part "+p1+" (section)", "(required)")
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditClearProperty, Name: "status"}})
	requireRelicCategory(t, "clearing a required property", err, CategoryValidation,
		`relic does not fit schema spec version 2: property "status": the property is required (required)`)
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}})
	requireRelicCategory(t, "adding a part that lacks a field", err, CategoryValidation, "(section)", "(required)")
	requireRelicWrites(t, "refused updates", store, 1)

	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: p1, After: p2}}); err != nil {
		t.Fatalf("moving the broken part: %v", err)
	}
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: p1}}); err != nil {
		t.Fatalf("removing the broken part: %v", err)
	}
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRestorePart, Part: p1}})
	requireRelicCategory(t, "restoring the broken part", err, CategoryValidation, "part "+p1+" (section)")
	if _, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditPart, Part: p2, Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("Goals")}}},
	}); err != nil {
		t.Fatalf("fixing the other part: %v", err)
	}
}

func TestUpdateIsCheckedUnderTheNamedVersion(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	onOne := putRelicNaming(t, store, 1, "spec", versions.spec1.ID)
	onTwo := putRelicNaming(t, store, 2, "spec", versions.spec2.ID)
	for _, relic := range []RelicSnapshot{onOne, onTwo} {
		if _, err := service.Update(ctx, relic.Record.State.RelicID, []RelicEdit{
			{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("draft")},
		}); err != nil {
			t.Fatalf("setting the status: %v", err)
		}
	}
	if _, err := service.Update(ctx, onOne.Record.State.RelicID, []RelicEdit{{Kind: RelicEditClearProperty, Name: "status"}}); err != nil {
		t.Errorf("a relic on version 1 cannot clear a property only version 2 requires: %v", err)
	}
	_, err := service.Update(ctx, onTwo.Record.State.RelicID, []RelicEdit{{Kind: RelicEditClearProperty, Name: "status"}})
	requireRelicCategory(t, "a relic on version 2 clearing its status", err, CategoryValidation, "spec version 2", "(required)")
}

func TestUpdateOfAnUnknownVersionIsRefused(t *testing.T) {
	store, schemas, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	schemas.hidden[versions.spec2.ID] = true
	relic := putRelicNaming(t, store, 1, "spec", versions.spec2.ID)
	relicID := relic.Record.State.RelicID

	_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "Renamed"}})
	requireRelicCategory(t, "updating a relic naming an unsynced version", err, CategoryStaleWrite)
	if want := "relic " + relicID + " names a schema version for spec this clone has not synced; run `workbook sync` and try again"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}

	shown, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown.RelicConformance.State != RelicConformanceUnknownSchema || shown.Schema == nil || shown.Schema.Version != 0 || shown.Schema.ID != versions.spec2.ID {
		t.Errorf("shown schema %+v, conformance %+v; want an unknown version", shown.Schema, shown.RelicConformance)
	}

	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := service.Restore(ctx, relicID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	migrated, err := service.Migrate(ctx, relicID, "1")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if ref := migrated.Relic.Schema; ref == nil || ref.ID != versions.spec1.ID || ref.Version != 1 {
		t.Errorf("migrated relic names %+v, want spec version 1", ref)
	}
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "Renamed"}}); err != nil {
		t.Errorf("updating after the migration: %v", err)
	}
}

func TestCreateFromFileSchemaMember(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	document := func(schema string) RelicCreateInput {
		t.Helper()
		decoded, err := DecodeRelicDocument([]byte(`{"format":"workbook.relic","version":1,"title":"Archive","type":"spec",` +
			`"schema":` + schema + `,"properties":{"status":"draft"},"parts":[],"links":[]}`))
		if err != nil {
			t.Fatalf("DecodeRelicDocument(%s): %v", schema, err)
		}
		input, err := RelicCreateInputFromDocument(decoded)
		if err != nil {
			t.Fatalf("RelicCreateInputFromDocument(%s): %v", schema, err)
		}
		return input
	}
	ref := func(version RelicSchemaVersion) string {
		return fmt.Sprintf(`{"type":%q,"id":%q,"version":%d}`, version.Type, version.ID, version.Version)
	}

	for label, test := range map[string]struct {
		schema string
		want   RelicSchemaVersion
	}{
		"null":              {"null", versions.spec2},
		"a known older one": {ref(versions.spec1), versions.spec1},
		"the newest by ID":  {`{"type":"spec","id":"` + versions.spec2.ID + `"}`, versions.spec2},
	} {
		created, err := service.Create(ctx, document(test.schema))
		if err != nil {
			t.Fatalf("%s: Create: %v", label, err)
		}
		if got := created.Relic.Schema; got == nil || got.ID != test.want.ID || got.Version != test.want.Version {
			t.Errorf("%s: created relic names %+v, want %+v", label, got, test.want)
		}
	}
	writes := len(store.writes)
	_, err := service.Create(ctx, document(`{"type":"spec","id":"`+relicTestID(999)+`"}`))
	requireRelicCategory(t, "an unknown version", err, CategoryStaleWrite,
		"the document names schema version "+relicTestID(999), "workbook sync")
	_, err = service.Create(ctx, document(ref(versions.plan1)))
	requireRelicCategory(t, "another type's version", err, CategoryValidation, versions.plan1.ID, "plan")
	requireRelicWrites(t, "refused creates", store, writes)

	decoded, err := DecodeRelicDocument([]byte(`{"format":"workbook.relic","version":1,"title":"A","type":"spec",` +
		`"schema":{"type":"spec","id":"spec"},"properties":{},"parts":[],"links":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RelicCreateInputFromDocument(decoded)
	requireRelicCategory(t, "a schema ID that is not an ID", err, CategoryValidation, "schema")
}

func TestJudgeRelicStates(t *testing.T) {
	schemas := newMemorySchemaStore()
	spec := schemas.put(t, "spec", specSchemaV2)
	broken := schemas.put(t, "spec", specSchemaV1)
	schemas.corrupt[broken.ID] = true
	listing, _ := schemas.RelicSchemaListing(context.Background(), ProjectConfig{})
	load := func(version RelicSchemaVersion) (*RelicSchema, error) {
		_, schema, err := schemas.ReadRelicSchema(context.Background(), ProjectConfig{}, version)
		return schema, err
	}
	part := RelicPart{ID: relicTestID(11), Type: "section", Position: "1/1"}
	relic := func(schema string, properties map[string]json.RawMessage, parts ...RelicPart) RelicData {
		if properties == nil {
			properties = map[string]json.RawMessage{}
		}
		return RelicData{Type: "spec", Schema: schema, Properties: properties, Parts: append([]RelicPart{}, parts...)}
	}
	heading := map[string]map[string]json.RawMessage{part.ID: {"heading": relicRawString("Goals")}}
	draft := map[string]json.RawMessage{"status": relicRawString("draft")}
	removed := part
	removed.Removed = true

	for label, test := range map[string]struct {
		relic  RelicData
		fields map[string]map[string]json.RawMessage
		want   RelicConformance
	}{
		"open":                         {relic("", nil, part), nil, RelicConformance{State: RelicConformanceOpen}},
		"conforms":                     {relic(spec.ID, draft, part), heading, RelicConformance{State: RelicConformanceConforms}},
		"a removed part is not judged": {relic(spec.ID, draft, removed), nil, RelicConformance{State: RelicConformanceConforms}},
		"properties are judged first": {relic(spec.ID, nil, part), nil, RelicConformance{State: RelicConformanceNotConforming,
			Problem: `property "status": ` + missingRequiredMessage(t) + ` (required)`}},
		"then parts":     {relic(spec.ID, draft, part), nil, RelicConformance{State: RelicConformanceNotConforming}},
		"unknown":        {relic(relicTestID(999), nil, part), nil, RelicConformance{State: RelicConformanceUnknownSchema}},
		"a corrupt blob": {relic(broken.ID, nil, part), nil, RelicConformance{State: RelicConformanceUnknownSchema}},
	} {
		got, err := JudgeRelic(test.relic, test.fields, listing, load)
		if err != nil {
			t.Fatalf("%s: JudgeRelic: %v", label, err)
		}
		if label == "then parts" {
			if got.State != RelicConformanceNotConforming || !strings.HasPrefix(got.Problem, "part "+part.ID+" (section)") {
				t.Errorf("%s: JudgeRelic = %+v", label, got)
			}
			continue
		}
		if got != test.want {
			t.Errorf("%s: JudgeRelic = %+v, want %+v", label, got, test.want)
		}
	}

	failing := func(RelicSchemaVersion) (*RelicSchema, error) {
		return nil, Errorf(CategoryOperational, "git is not answering")
	}
	if _, err := JudgeRelic(relic(spec.ID, draft), nil, listing, failing); CategoryOf(err) != CategoryOperational {
		t.Errorf("JudgeRelic with a failing store = %v, want the store's error", err)
	}

	for label, conformance := range map[string]RelicConformance{
		`"conformance":"open"}`:                               {State: RelicConformanceOpen},
		`"conformance":"not-conforming","schemaProblem":"x"}`: {State: RelicConformanceNotConforming, Problem: "x"},
	} {
		if encoded, _ := json.Marshal(conformance); string(encoded) != "{"+label {
			t.Errorf("conformance encodes as %s, want {%s", encoded, label)
		}
	}
}
