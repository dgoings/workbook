package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// newBuiltInTestService is a service with a schema store that lists no
// project version, so spec and plan have only their built-ins.
func newBuiltInTestService(t *testing.T) (*memoryRelicStore, *memorySchemaStore, RelicService) {
	t.Helper()
	schemas := newMemorySchemaStore()
	store := newMemoryRelicStore()
	service := newRelicTestService(store, relicServiceIDs(1, 200))
	service.Schemas = schemas
	return store, schemas, service
}

func draftStatus() map[string]json.RawMessage {
	return map[string]json.RawMessage{"status": relicRawString("draft")}
}

func TestNewRelicNamesTheBuiltIn(t *testing.T) {
	store, schemas, service := newBuiltInTestService(t)
	ctx := context.Background()

	created, err := service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec", Properties: draftStatus(),
		Parts: []RelicPartInput{{Type: "section", Fields: map[string]json.RawMessage{"title": relicRawString("Goals")}}}})
	if err != nil {
		t.Fatalf("Create(spec): %v", err)
	}
	if got := lastWrittenPack(t, store).Operations[0].Relic.Schema; got != "workbook:spec:1" {
		t.Errorf("relic.create schema = %q, want workbook:spec:1", got)
	}
	if ref := created.Relic.Schema; ref == nil || *ref != (RelicSchemaRef{Type: "spec", ID: "workbook:spec:1", BuiltIn: true}) {
		t.Errorf("created relic names %+v, want the built-in", ref)
	}
	if got := created.Relic.RelicConformance; got == nil || got.State != RelicConformanceConforms {
		t.Errorf("created relic conformance = %+v, want conforms", got)
	}
	shown, err := service.Show(ctx, created.Relic.ID, RelicShowOptions{})
	if err != nil || shown.RelicConformance.State != RelicConformanceConforms {
		t.Fatalf("Show = %+v, %v; want conforms", shown.RelicConformance, err)
	}
	if schemas.reads != 0 {
		t.Errorf("judging under a built-in read %d schemas from the store, want 0", schemas.reads)
	}

	writes := len(store.writes)
	_, err = service.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec"})
	requireRelicCategory(t, "a spec without a status", err, CategoryValidation,
		`relic does not fit schema workbook:spec:1: property "status": `, "(required)")
	requireRelicWrites(t, "the refused create", store, writes)

	note, err := service.Create(ctx, RelicCreateInput{Title: "Notes", Type: "note"})
	if err != nil || note.Relic.Schema != nil {
		t.Fatalf("Create(note) = %+v, %v; want an open relic", note.Relic.Schema, err)
	}
	if got := note.Relic.RelicConformance; got == nil || got.State != RelicConformanceOpen {
		t.Errorf("note conformance = %+v, want open", got)
	}

	unchecked := service
	unchecked.Schemas = nil
	open, err := unchecked.Create(ctx, RelicCreateInput{Title: "Archive", Type: "spec"})
	if err != nil || open.Relic.Schema != nil {
		t.Fatalf("Create(spec) with no schema store = %+v, %v; want an open relic", open.Relic.Schema, err)
	}
	if got := lastWrittenPack(t, store).Operations[0].Relic.Schema; got != "" {
		t.Errorf("relic.create with no schema store names %q", got)
	}
}

func TestProjectSchemaTakesOver(t *testing.T) {
	_, schemas, service := newBuiltInTestService(t)
	ctx := context.Background()
	older, err := service.Create(ctx, RelicCreateInput{Title: "Older", Type: "spec", Properties: draftStatus()})
	if err != nil {
		t.Fatalf("Create(older): %v", err)
	}
	own := schemas.put(t, "spec", `{"properties":{"type":"object","properties":{"status":{"type":"string"}}}}`)

	newer, err := service.Create(ctx, RelicCreateInput{Title: "Newer", Type: "spec"})
	if err != nil {
		t.Fatalf("Create(newer): %v", err)
	}
	if ref := newer.Relic.Schema; ref == nil || *ref != (RelicSchemaRef{Type: "spec", ID: own.ID, Version: 1}) {
		t.Errorf("a new spec names %+v, want the project's spec version 1", ref)
	}

	_, err = service.Update(ctx, older.Relic.ID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("wip")}})
	requireRelicCategory(t, "an edit the built-in refuses", err, CategoryValidation,
		`relic does not fit schema workbook:spec:1: property "status": `, "(enum)")
	if _, err := service.Update(ctx, older.Relic.ID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("approved")}}); err != nil {
		t.Fatalf("an edit the built-in allows: %v", err)
	}

	behind, err := service.List(ctx, RelicListFilter{Behind: true})
	if err != nil {
		t.Fatalf("List(behind): %v", err)
	}
	if len(behind) != 1 || behind[0].ID != older.Relic.ID {
		t.Fatalf("behind = %+v, want only the relic on the built-in", behind)
	}
	if got := behind[0].RelicConformance; got == nil || got.State != RelicConformanceConforms {
		t.Errorf("the relic on the built-in is %+v, want conforms", got)
	}

	migrated, err := service.Migrate(ctx, older.Relic.ID, "spec")
	if err != nil || !migrated.Changed {
		t.Fatalf("Migrate(spec) = %+v, %v", migrated, err)
	}
	if ref := migrated.Relic.Schema; ref == nil || ref.ID != own.ID || ref.BuiltIn {
		t.Errorf("migrated relic names %+v, want spec version 1", ref)
	}
	if behind, err := service.List(ctx, RelicListFilter{Behind: true}); err != nil || len(behind) != 0 {
		t.Errorf("behind after the migration = %+v, %v", behind, err)
	}
}

func TestUnknownBuiltInIsNewerWriter(t *testing.T) {
	store, _, service := newBuiltInTestService(t)
	ctx := context.Background()
	relic := putRelicNaming(t, store, 1, "spec", "workbook:spec:9")
	relicID := relic.Record.State.RelicID

	shown, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown.RelicConformance.State != RelicConformanceUnknownSchema ||
		shown.Schema == nil || *shown.Schema != (RelicSchemaRef{Type: "spec", ID: "workbook:spec:9", BuiltIn: true}) {
		t.Errorf("shown schema %+v, conformance %+v; want an unknown built-in", shown.Schema, shown.RelicConformance)
	}
	listed, err := service.List(ctx, RelicListFilter{})
	if err != nil || len(listed) != 1 || listed[0].Behind || listed[0].RelicConformance.State != RelicConformanceUnknownSchema {
		t.Fatalf("List = %+v, %v; want one unknown-schema relic, not behind", listed, err)
	}

	writes := len(store.writes)
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "Renamed"}})
	requireRelicCategory(t, "updating a relic naming an unknown built-in", err, CategoryNewerWriter)
	if want := "relic " + relicID + " names the built-in schema workbook:spec:9, which this Workbook does not have; upgrade Workbook and try again"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	requireRelicWrites(t, "the refused update", store, writes)

	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := service.Restore(ctx, relicID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	migrated, err := service.Migrate(ctx, relicID, "note")
	if err != nil || migrated.Relic.Type != "note" || migrated.Relic.Schema != nil {
		t.Fatalf("Migrate(note) = %+v, %v", migrated.Relic, err)
	}

	decoded, err := DecodeRelicDocument([]byte(`{"format":"workbook.relic","version":1,"title":"A","type":"spec",` +
		`"schema":{"type":"spec","id":"workbook:spec:9","builtIn":true},"properties":{"status":"draft"},"parts":[],"links":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	input, err := RelicCreateInputFromDocument(decoded)
	if err != nil {
		t.Fatal(err)
	}
	writes = len(store.writes)
	_, err = service.Create(ctx, input)
	requireRelicCategory(t, "creating from a document naming an unknown built-in", err, CategoryNewerWriter)
	if want := "the document names the built-in schema workbook:spec:9, which this Workbook does not have; upgrade Workbook and try again"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	requireRelicWrites(t, "the refused create", store, writes)
}

func TestCreateFromADocumentNamingABuiltIn(t *testing.T) {
	store, _, service := newBuiltInTestService(t)
	ctx := context.Background()
	document := func(schema string) RelicCreateInput {
		t.Helper()
		decoded, err := DecodeRelicDocument([]byte(`{"format":"workbook.relic","version":1,"title":"A","type":"spec",` +
			`"schema":` + schema + `,"properties":{"status":"draft"},"parts":[],"links":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		input, err := RelicCreateInputFromDocument(decoded)
		if err != nil {
			t.Fatal(err)
		}
		return input
	}
	created, err := service.Create(ctx, document(`{"type":"spec","id":"workbook:spec:1","builtIn":true}`))
	if err != nil || created.Relic.Schema == nil || created.Relic.Schema.ID != "workbook:spec:1" {
		t.Fatalf("Create(workbook:spec:1) = %+v, %v", created.Relic.Schema, err)
	}
	writes := len(store.writes)
	_, err = service.Create(ctx, document(`{"type":"plan","id":"workbook:plan:1","builtIn":true}`))
	requireRelicCategory(t, "another type's built-in", err, CategoryValidation)
	if want := "schema workbook:plan:1 is a schema for plan, not for spec"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	requireRelicWrites(t, "the refused create", store, writes)
}

func TestMigrateTargets(t *testing.T) {
	store, _, service := newBuiltInTestService(t)
	ctx := context.Background()
	openSpec := func(number int, partIDs ...string) string {
		t.Helper()
		relicID := putRelicNaming(t, store, number, "spec", "", partIDs...).Record.State.RelicID
		if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("draft")}}); err != nil {
			t.Fatalf("setting the status: %v", err)
		}
		return relicID
	}

	byName := openSpec(1)
	migrated, err := service.Migrate(ctx, byName, "workbook:spec:1")
	if err != nil || !migrated.Changed || migrated.Relic.Schema == nil ||
		*migrated.Relic.Schema != (RelicSchemaRef{Type: "spec", ID: "workbook:spec:1", BuiltIn: true}) {
		t.Fatalf("Migrate(workbook:spec:1) = %+v, %v", migrated, err)
	}
	if got := migrated.Relic.RelicConformance; got == nil || got.State != RelicConformanceConforms {
		t.Errorf("conformance after the migration = %+v", got)
	}
	if pack := lastWrittenPack(t, store); pack.Operations[0].Schema != "workbook:spec:1" || pack.Operations[0].RelicType != "spec" {
		t.Errorf("migration recorded %+v", pack.Operations[0])
	}

	byType := openSpec(2)
	migrated, err = service.Migrate(ctx, byType, "spec")
	if err != nil || migrated.Relic.Schema == nil || migrated.Relic.Schema.ID != "workbook:spec:1" {
		t.Fatalf("Migrate(spec) = %+v, %v", migrated.Relic.Schema, err)
	}

	numbered := openSpec(3)
	_, err = service.Migrate(ctx, numbered, "spec@1")
	requireRelicCategory(t, "--to spec@1 with no project version", err, CategoryNotFound)
	if want := "type spec has no schema version 1 of this project's own; its built-in schema is workbook:spec:1"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}
	_, err = service.Migrate(ctx, numbered, "1")
	requireRelicCategory(t, "--to 1 with no project version", err, CategoryNotFound, "of this project's own")
	_, err = service.Migrate(ctx, numbered, "workbook:spec:9")
	requireRelicCategory(t, "--to an unknown built-in", err, CategoryNotFound)
	if want := "this Workbook has no built-in schema workbook:spec:9"; err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err, want)
	}

	toPlan, err := service.Migrate(ctx, numbered, "workbook:plan:1")
	if err != nil || toPlan.Relic.Type != "plan" || toPlan.Relic.Schema == nil || toPlan.Relic.Schema.ID != "workbook:plan:1" {
		t.Fatalf("Migrate(workbook:plan:1) = %+v, %v", toPlan.Relic, err)
	}

	untitled := openSpec(4, relicTestID(41))
	writes := len(store.writes)
	_, err = service.Migrate(ctx, untitled, "workbook:spec:1")
	requireRelicCategory(t, "a relic whose section has no title", err, CategoryValidation,
		"relic does not fit schema workbook:spec:1: part "+relicTestID(41)+" (section)", "(required)")
	requireRelicWrites(t, "the refused migration", store, writes)

	again, err := service.Migrate(ctx, byName, "workbook:spec:1")
	if err != nil || again.Changed {
		t.Fatalf("migrating to the built-in named = %+v, %v", again, err)
	}
}

func TestBehindByID(t *testing.T) {
	schemas := newMemorySchemaStore()
	empty := RelicSchemaListing{}.WithBuiltIns()
	own1 := schemas.put(t, "spec", `{}`)
	own2 := schemas.put(t, "spec", `{"properties":{"type":"object"}}`)
	takenOver := RelicSchemaListing{Versions: []RelicSchemaVersion{own1, own2}}.WithBuiltIns()
	takenOverOnce := RelicSchemaListing{Versions: []RelicSchemaVersion{own1}}.WithBuiltIns()

	for label, test := range map[string]struct {
		relic   RelicData
		listing RelicSchemaListing
		want    bool
	}{
		"an open spec":                        {RelicData{Type: "spec"}, empty, true},
		"an open note":                        {RelicData{Type: "note"}, empty, false},
		"on the built-in, no project version": {RelicData{Type: "spec", Schema: "workbook:spec:1"}, empty, false},
		"on the built-in after a takeover":    {RelicData{Type: "spec", Schema: "workbook:spec:1"}, takenOver, true},
		"on the built-in, project version 1":  {RelicData{Type: "spec", Schema: "workbook:spec:1"}, takenOverOnce, true},
		"on project version 1 of 2":           {RelicData{Type: "spec", Schema: own1.ID}, takenOver, true},
		"on project version 2 of 2":           {RelicData{Type: "spec", Schema: own2.ID}, takenOver, false},
		"on an unknown project version":       {RelicData{Type: "spec", Schema: relicTestID(999)}, takenOver, false},
		"on an unknown built-in":              {RelicData{Type: "spec", Schema: "workbook:spec:9"}, empty, false},
		"with no schema store":                {RelicData{Type: "spec"}, RelicSchemaListing{}, false},
	} {
		if got := RelicBehind(test.relic, test.listing); got != test.want {
			t.Errorf("%s: RelicBehind = %t, want %t", label, got, test.want)
		}
	}
}

func TestReadRelicSchemaVersion(t *testing.T) {
	schemas := newMemorySchemaStore()
	own := schemas.put(t, "spec", `{}`)
	builtIn, _, wantSchema, _ := BuiltInRelicSchema("workbook:plan:1")
	ctx := context.Background()

	document, schema, err := ReadRelicSchemaVersion(ctx, schemas, serviceTestConfig, builtIn)
	if err != nil || schema != wantSchema || document.Type != "plan" || schemas.reads != 0 {
		t.Fatalf("ReadRelicSchemaVersion(built-in) = %v, %v; %d store reads", document.Type, err, schemas.reads)
	}
	if _, _, err := ReadRelicSchemaVersion(ctx, schemas, serviceTestConfig, own); err != nil || schemas.reads != 1 {
		t.Fatalf("ReadRelicSchemaVersion(project) = %v; %d store reads", err, schemas.reads)
	}
	_, _, err = ReadRelicSchemaVersion(ctx, schemas, serviceTestConfig, RelicSchemaVersion{Type: "spec", Version: 9, ID: "workbook:spec:9"})
	requireRelicCategory(t, "an unknown built-in", err, CategoryNewerWriter, "workbook:spec:9")
}

func TestAReferenceCannotNameABuiltIn(t *testing.T) {
	_, _, err := CompileRelicSchema("note", []byte(`{"properties":{"type":"object","properties":{"x":{"$ref":"spec#/$defs/x"}}}}`),
		func(string) (RelicSchemaVersion, *RelicSchema, bool, error) {
			return RelicSchemaVersion{}, nil, false, nil
		})
	if err == nil || !strings.Contains(err.Error(),
		`the reference "spec#/$defs/x" names type spec, which has no schema of this project's own; a reference cannot name a built-in schema`) {
		t.Fatalf("CompileRelicSchema = %v, want the built-in refusal", err)
	}
	_, _, err = CompileRelicSchema("note", []byte(`{"properties":{"type":"object","properties":{"x":{"$ref":"other#/$defs/x"}}}}`),
		func(string) (RelicSchemaVersion, *RelicSchema, bool, error) {
			return RelicSchemaVersion{}, nil, false, nil
		})
	if err == nil || !strings.Contains(err.Error(), `the reference "other#/$defs/x" names type other, which has no schema`) ||
		strings.Contains(err.Error(), "built-in") {
		t.Fatalf("CompileRelicSchema(other) = %v, want the plain refusal", err)
	}
}
