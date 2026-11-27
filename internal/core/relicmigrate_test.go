package core

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestMigrateChecksTheWholeRelic(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	p1 := relicTestID(11)
	relic := putRelicNaming(t, store, 1, "spec", versions.spec1.ID, p1)
	relicID := relic.Record.State.RelicID

	_, err := service.Migrate(ctx, relicID, "2")
	requireRelicCategory(t, "migrating a relic that does not fit", err, CategoryValidation,
		`relic does not fit schema spec version 2: property "status"`)
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("draft")}}); err != nil {
		t.Fatalf("setting the status: %v", err)
	}
	_, err = service.Migrate(ctx, relicID, "spec@2")
	requireRelicCategory(t, "migrating while a part does not fit", err, CategoryValidation,
		"relic does not fit schema spec version 2: part "+p1+" (section)")
	if _, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditPart, Part: p1, Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("Goals")}}},
	}); err != nil {
		t.Fatalf("fixing the part: %v", err)
	}
	writes := len(store.writes)
	migrated, err := service.Migrate(ctx, relicID, "2")
	if err != nil || !migrated.Changed {
		t.Fatalf("Migrate(2) = %+v, %v", migrated, err)
	}
	pack := lastWrittenPack(t, store)
	if len(pack.Operations) != 1 || !reflect.DeepEqual(pack.Operations[0], RelicOperation{ID: pack.Operations[0].ID, Type: RelicOperationMigrate, RelicType: "spec", Schema: versions.spec2.ID}) {
		t.Fatalf("migration recorded %+v", pack.Operations)
	}
	again, err := service.Migrate(ctx, relicID, "2")
	if err != nil || again.Changed || len(store.writes) != writes+1 {
		t.Fatalf("migrating to the version the relic names = %+v, %v; %d writes", again, err, len(store.writes)-writes)
	}
	shown, err := service.Show(ctx, relicID+"#"+p1, RelicShowOptions{})
	if err != nil || shown.Schema == nil || *shown.Schema != (RelicSchemaRef{Type: "spec", ID: versions.spec2.ID, Version: 2}) ||
		shown.RelicConformance != (RelicConformance{State: RelicConformanceConforms}) {
		t.Fatalf("Show after migrating = schema %+v, conformance %+v, %v", shown.Schema, shown.RelicConformance, err)
	}

	toPlan, err := service.Migrate(ctx, relicID, "plan")
	if err != nil || toPlan.Relic.Type != "plan" || toPlan.Relic.Schema == nil || toPlan.Relic.Schema.ID != versions.plan1.ID {
		t.Fatalf("Migrate(plan) = %+v, %v", toPlan.Relic, err)
	}
	toNote, err := service.Migrate(ctx, relicID, "note")
	if err != nil || toNote.Relic.Type != "note" || toNote.Relic.Schema != nil {
		t.Fatalf("Migrate(note) = %+v, %v", toNote.Relic, err)
	}
	if pack := lastWrittenPack(t, store); pack.Operations[0].Schema != "" || pack.Operations[0].RelicType != "note" {
		t.Fatalf("migration to an open type recorded %+v", pack.Operations[0])
	}

	for _, target := range []string{"x@", "0", "", "@2", "spec@0", "spec@-1", "Not A Type", "spec@two"} {
		_, err := service.Migrate(ctx, relicID, target)
		requireRelicCategory(t, fmt.Sprintf("--to %q", target), err, CategoryInvocation)
	}
	_, err = service.Migrate(ctx, relicID, "spec@9")
	requireRelicCategory(t, "--to spec@9", err, CategoryNotFound, "type spec has no schema version 9")
	_, err = service.Migrate(ctx, relicID, "3")
	requireRelicCategory(t, "--to 3 of an open type", err, CategoryNotFound, "type note has no schema version 3")

	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatal(err)
	}
	_, err = service.Migrate(ctx, relicID, "spec")
	requireRelicCategory(t, "migrating a deleted relic", err, CategoryValidation, "is deleted; restore it first")
}
