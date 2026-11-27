package core

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func schemaPutOperation(relicType string, size int64, blob string) ConfigOperation {
	return ConfigOperation{Type: ConfigRelicSchemaPut, RelicSchema: &RelicSchemaChange{Type: relicType, Bytes: size, Blob: blob}}
}

func testBlobID(index int) string {
	return fmt.Sprintf("%040x", index)
}

func TestApplyConfigNumbersSchemaVersionsPerType(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	if parent.Config.Schemas != nil {
		t.Fatalf("a genesis has a schema listing: %+v", parent.Config.Schemas)
	}
	state := fold(t, parent,
		[]ConfigOperation{schemaPutOperation("spec", 100, testBlobID(1))},
		[]ConfigOperation{schemaPutOperation("plan", 200, testBlobID(2)), schemaPutOperation("spec", 300, testBlobID(3))},
	)
	// fold numbers the batches' operations 100 and 200 onward.
	want := []RelicSchemaVersion{
		{Type: "plan", Version: 1, ID: configOperationID(200), Bytes: 200, Blob: testBlobID(2)},
		{Type: "spec", Version: 1, ID: configOperationID(100), Bytes: 100, Blob: testBlobID(1)},
		{Type: "spec", Version: 2, ID: configOperationID(201), Bytes: 300, Blob: testBlobID(3)},
	}
	listing := state.RelicSchemas()
	if fmt.Sprint(listing.Versions) != fmt.Sprint(want) {
		t.Fatalf("listing = %+v, want %+v", listing.Versions, want)
	}
	if newest, ok := listing.Newest("spec"); !ok || newest.Version != 2 {
		t.Errorf("Newest(spec) = %+v, %t", newest, ok)
	}
	if _, ok := listing.Newest("ghost"); ok {
		t.Error("Newest(ghost) found a version")
	}
	if version, ok := listing.ByID(configOperationID(100)); !ok || version.Type != "spec" || version.Version != 1 {
		t.Errorf("ByID = %+v, %t", version, ok)
	}
	if version, ok := listing.Number("spec", 2); !ok || version.ID != configOperationID(201) {
		t.Errorf("Number(spec, 2) = %+v, %t", version, ok)
	}
	if _, ok := listing.Number("spec", 3); ok {
		t.Error("Number(spec, 3) found a version")
	}
	if got := listing.Types(); fmt.Sprint(got) != "[plan spec]" {
		t.Errorf("Types = %v", got)
	}
	encoded := mustEncode(t, state)
	decoded, err := DecodeConfigStateDocument(encoded)
	if err != nil {
		t.Fatalf("decode a checkpoint with schemas: %v", err)
	}
	if !bytes.Equal(mustEncode(t, decoded), encoded) {
		t.Error("a checkpoint with schemas does not round-trip")
	}
	if !bytes.Contains(encoded, []byte(`"schemas":{"versions":[{"type":"plan","version":1,`)) {
		t.Errorf("checkpoint = %s", encoded)
	}

	// A checkpoint with no schema keeps the bytes it was stored as.
	for _, fixture := range goldenConfigLedger {
		stored, err := DecodeConfigStateDocument([]byte(fixture.state))
		if err != nil {
			t.Fatal(err)
		}
		if stored.Config.Schemas != nil || len(stored.RelicSchemas().Versions) != 0 {
			t.Errorf("%s: schemas = %+v", fixture.name, stored.Config.Schemas)
		}
		if got := mustEncode(t, stored); string(got) != fixture.state {
			t.Errorf("%s: re-encoded to\n%s", fixture.name, got)
		}
	}
	if bytes.Contains(mustEncode(t, parent), []byte("schemas")) {
		t.Error("a checkpoint with no schema mentions schemas")
	}
}

func TestSchemaPutShapesAndMarker(t *testing.T) {
	good := schemaPutOperation("spec", 812, testBlobID(1))
	good.ID = configOperationID(1)
	if err := validateConfigOperationDocument(good); err != nil {
		t.Fatalf("a good schema.put: %v", err)
	}
	bad := map[string]func(*ConfigOperation){
		"no relicSchema":   func(op *ConfigOperation) { op.RelicSchema = nil },
		"with name":        func(op *ConfigOperation) { op.Name = "todo" },
		"bytes 0":          func(op *ConfigOperation) { op.RelicSchema.Bytes = 0 },
		"bytes 65537":      func(op *ConfigOperation) { op.RelicSchema.Bytes = 65537 },
		"bad type":         func(op *ConfigOperation) { op.RelicSchema.Type = "Spec" },
		"bad blob":         func(op *ConfigOperation) { op.RelicSchema.Blob = "xyz" },
		"uppercase blob":   func(op *ConfigOperation) { op.RelicSchema.Blob = strings.ToUpper(testBlobID(10)) },
		"status op schema": func(op *ConfigOperation) { op.Type = ConfigStatusRelabel },
	}
	for label, mutate := range bad {
		operation := good
		change := *good.RelicSchema
		operation.RelicSchema = &change
		mutate(&operation)
		if err := validateConfigOperationDocument(operation); CategoryOf(err) != CategoryCorruptData {
			t.Errorf("%s: %v, want corrupt data", label, err)
		}
	}
	sha256 := good
	sha256.RelicSchema = &RelicSchemaChange{Type: "spec", Bytes: 65536, Blob: fmt.Sprintf("%064x", 7)}
	if err := validateConfigOperationDocument(sha256); err != nil {
		t.Errorf("a SHA-256 blob and 65536 bytes: %v", err)
	}

	parent := genesisState(t, testVocabulary(t))
	state := fold(t, parent, []ConfigOperation{schemaPutOperation("spec", 10, testBlobID(1))})
	reused := configPack(state.LogicalClock+1, identify(100, []ConfigOperation{schemaPutOperation("spec", 10, testBlobID(2))})...)
	if _, err := ApplyConfig(&state, reused); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a schema.put reusing a listed ID = %v, want corrupt data", err)
	}

	if got := configOperationMinReader[ConfigRelicSchemaPut]; got != 6 {
		t.Errorf("configOperationMinReader[schema.put] = %d, want 6", got)
	}
	if got := ConfigPackMinReader([]ConfigOperation{good}); got != 6 {
		t.Errorf("ConfigPackMinReader(schema.put) = %d, want 6", got)
	}
	pack, err := NewConfigOperationPack(configTestProjectID, configTestGeneration, "a@example.com", 2, configTestNow, []ConfigOperation{good})
	if err != nil || pack.MinReader != 6 {
		t.Errorf("NewConfigOperationPack minReader = %d, %v", pack.MinReader, err)
	}
	listing := &RelicSchemaListing{Versions: []RelicSchemaVersion{{Type: "spec", Version: 1, ID: configOperationID(1), Bytes: 10, Blob: testBlobID(1)}}}
	genesis := ConfigOperation{ID: configOperationID(2), Type: ConfigGenesis, Config: &ConfigData{Vocabulary: testVocabulary(t).Document(), Schemas: listing}}
	if got := ConfigPackMinReader([]ConfigOperation{genesis}); got != 6 {
		t.Errorf("ConfigPackMinReader(genesis with schemas) = %d, want 6", got)
	}
	if err := validateConfigOperationDocument(genesis); err != nil {
		t.Errorf("a genesis with a canonical listing: %v", err)
	}
	for label, versions := range map[string][]RelicSchemaVersion{
		"empty":       {},
		"unsorted":    {{Type: "spec", Version: 1, ID: configOperationID(1), Bytes: 1, Blob: testBlobID(1)}, {Type: "plan", Version: 1, ID: configOperationID(2), Bytes: 1, Blob: testBlobID(2)}},
		"gap":         {{Type: "spec", Version: 2, ID: configOperationID(1), Bytes: 1, Blob: testBlobID(1)}},
		"repeated ID": {{Type: "plan", Version: 1, ID: configOperationID(1), Bytes: 1, Blob: testBlobID(1)}, {Type: "spec", Version: 1, ID: configOperationID(1), Bytes: 1, Blob: testBlobID(2)}},
		"bad bytes":   {{Type: "spec", Version: 1, ID: configOperationID(1), Bytes: 0, Blob: testBlobID(1)}},
	} {
		broken := genesis
		broken.Config = &ConfigData{Vocabulary: genesis.Config.Vocabulary, Schemas: &RelicSchemaListing{Versions: versions}}
		if err := validateConfigOperationDocument(broken); CategoryOf(err) != CategoryCorruptData {
			t.Errorf("genesis listing %s: %v, want corrupt data", label, err)
		}
	}
}

// schemaListingState is a checkpoint already listing count versions of each
// type, as a long-lived project's would.
func schemaListingState(t *testing.T, counts map[string]int) ConfigStateDocument {
	t.Helper()
	state := genesisState(t, testVocabulary(t))
	listing := &RelicSchemaListing{}
	next := 1000
	for _, relicType := range []string{"alpha", "beta", "gamma", "plan", "spec"} {
		for version := 1; version <= counts[relicType]; version++ {
			next++
			listing.Versions = append(listing.Versions, RelicSchemaVersion{
				Type: relicType, Version: version, ID: configOperationID(next), Bytes: 10, Blob: testBlobID(next),
			})
		}
	}
	state.Config.Schemas = listing
	if _, err := DecodeConfigStateDocument(mustEncode(t, state)); err != nil {
		t.Fatalf("listing state: %v", err)
	}
	return state
}

func TestSchemaVersionCountsAreCheckedOnlyOnAuthoring(t *testing.T) {
	if MaxRelicSchemaVersionsPerType != 500 || MaxRelicSchemaVersions != 1000 {
		t.Fatalf("limits = %d, %d", MaxRelicSchemaVersionsPerType, MaxRelicSchemaVersions)
	}
	full := schemaListingState(t, map[string]int{"spec": 500})
	one := configPack(full.LogicalClock+1, identify(1, []ConfigOperation{schemaPutOperation("spec", 10, testBlobID(1))})...)
	err := ValidateConfigAuthoring(&full, one)
	if CategoryOf(err) != CategoryValidation || err.Error() != "the schema for spec would have 501 versions; the limit is 500" {
		t.Errorf("a 501st spec version = %v", err)
	}
	other := configPack(full.LogicalClock+1, identify(1, []ConfigOperation{schemaPutOperation("plan", 10, testBlobID(1))})...)
	if err := ValidateConfigAuthoring(&full, other); err != nil {
		t.Errorf("a plan version beside 500 spec versions: %v", err)
	}

	over, err := ApplyConfig(&full, one)
	if err != nil {
		t.Fatalf("the fold refused a 501st version: %v", err)
	}
	again := configPack(over.LogicalClock+1, identify(2, []ConfigOperation{schemaPutOperation("spec", 10, testBlobID(2))})...)
	folded, err := ApplyConfig(&over, again)
	if err != nil {
		t.Fatalf("the fold refused a 502nd version: %v", err)
	}
	if newest, _ := folded.RelicSchemas().Newest("spec"); newest.Version != 502 {
		t.Errorf("newest = %d, want 502", newest.Version)
	}
	if _, err := DecodeConfigStateDocument(mustEncode(t, folded)); err != nil {
		t.Errorf("a checkpoint over the per-type count must still read: %v", err)
	}

	project := schemaListingState(t, map[string]int{"alpha": 250, "beta": 250, "plan": 250, "spec": 250})
	third := configPack(project.LogicalClock+1, identify(1, []ConfigOperation{schemaPutOperation("gamma", 10, testBlobID(1))})...)
	err = ValidateConfigAuthoring(&project, third)
	if CategoryOf(err) != CategoryValidation || err.Error() != "the project would have 1001 schema versions; the limit is 1000" {
		t.Errorf("a 1001st version = %v", err)
	}
	if _, err := ApplyConfig(&project, third); err != nil {
		t.Errorf("the fold refused a 1001st version: %v", err)
	}
}

// The schema section owns exactly one operation type, routed by its wire name
// and stamped with generation 6, as the priority and key sections' tests pin
// theirs.
func TestRelicSchemaOperationTypeIsAccountedFor(t *testing.T) {
	found := 0
	for operationType := range configOperationShapes {
		named := strings.HasPrefix(string(operationType), "schema.")
		if routed := operationType.TouchesRelicSchemas(); routed != named {
			t.Errorf("%q: TouchesRelicSchemas() = %t, want %t", operationType, routed, named)
		}
		if named {
			found++
			if got := configOperationMinReader[operationType]; got != 6 {
				t.Errorf("configOperationMinReader[%q] = %d, want 6", operationType, got)
			}
		}
	}
	if found != 1 {
		t.Errorf("configOperationShapes holds %d schema operations, want 1", found)
	}
}
