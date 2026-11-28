package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuiltInRelicSchemasCompile(t *testing.T) {
	versions := BuiltInRelicSchemaVersions()
	var ids []string
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	if want := []string{"workbook:spec:1", "workbook:plan:1"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("BuiltInRelicSchemaVersions() = %v, want %v", ids, want)
	}
	wantStatus := map[string][]string{
		"spec": {"draft", "approved", "superseded"},
		"plan": {"draft", "approved", "in-progress", "done"},
	}
	for _, listed := range versions {
		version, document, schema, ok := BuiltInRelicSchema(listed.ID)
		if !ok || schema == nil {
			t.Fatalf("BuiltInRelicSchema(%s) is missing", listed.ID)
		}
		if version != listed || !version.IsBuiltIn() || version.Version != 1 || version.Blob != "" {
			t.Errorf("BuiltInRelicSchema(%s) version = %+v, listed %+v", listed.ID, version, listed)
		}
		if document.Type != version.Type || schema.Type() != version.Type {
			t.Errorf("%s is a schema for %s / %s, want %s", listed.ID, document.Type, schema.Type(), version.Type)
		}
		encoded, err := EncodeDocument(document)
		if err != nil {
			t.Fatalf("EncodeDocument(%s): %v", listed.ID, err)
		}
		if version.Bytes != int64(len(encoded)) {
			t.Errorf("%s Bytes = %d, want %d", listed.ID, version.Bytes, len(encoded))
		}
		summary := schema.Summary()
		want := []RelicSchemaPropertySummary{{Name: "status", Allowed: wantStatus[version.Type]}}
		if !reflect.DeepEqual(summary.Required, want) {
			t.Errorf("%s required properties = %+v, want %+v", listed.ID, summary.Required, want)
		}
	}
	for _, id := range []string{"workbook:spec:2", relicTestID(50), "spec"} {
		if _, _, _, ok := BuiltInRelicSchema(id); ok {
			t.Errorf("BuiltInRelicSchema(%q) is found, want missing", id)
		}
	}
}

// builtInRelicSchemaPins are the SHA-256 digests of each released built-in's
// stored document. A released built-in never changes.
var builtInRelicSchemaPins = map[string]string{
	"workbook:spec:1": "fe7f2b28922690088c481a37018bcd65c66acc30239426e2a43bdc156861e6ce",
	"workbook:plan:1": "072369920d14a66211fa95a88f30a24661d03d97c7c5f12e513a436110c4c59c",
}

func TestBuiltInRelicSchemasNeverChange(t *testing.T) {
	for _, version := range BuiltInRelicSchemaVersions() {
		_, document, _, _ := BuiltInRelicSchema(version.ID)
		encoded, err := EncodeDocument(document)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		if got := hex.EncodeToString(sum[:]); got != builtInRelicSchemaPins[version.ID] {
			t.Errorf("%s stored document hashes to %s, pinned %s: a released built-in schema never changes; "+
				"add a new version beside it instead", version.ID, got, builtInRelicSchemaPins[version.ID])
		}
	}
	if len(builtInRelicSchemaPins) != len(BuiltInRelicSchemaVersions()) {
		t.Errorf("%d pins for %d built-in schemas", len(builtInRelicSchemaPins), len(BuiltInRelicSchemaVersions()))
	}
}

func TestBuiltInSchemaIDForm(t *testing.T) {
	for _, id := range []string{"workbook:spec:1", "workbook:open-question:12", "workbook:spec:2"} {
		if !IsBuiltInRelicSchemaID(id) {
			t.Errorf("IsBuiltInRelicSchemaID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"workbook:spec:0", "workbook:spec:01", "workbook:Spec:1", "workbook::1",
		"workbook:spec", "builtin:spec:1", relicTestID(50), "", "workbook:spec:1 ", "workbook:-spec:1"} {
		if IsBuiltInRelicSchemaID(id) {
			t.Errorf("IsBuiltInRelicSchemaID(%q) = true, want false", id)
		}
	}
	for n := range 200 {
		if IsBuiltInRelicSchemaID(relicTestID(n)) || IsBuiltInRelicSchemaID(configOperationID(n)) {
			t.Fatalf("a ULID is a built-in name")
		}
	}
}

func TestEffectiveListing(t *testing.T) {
	empty := RelicSchemaListing{}.WithBuiltIns()
	newest, ok := empty.Newest("spec")
	if !ok || newest.ID != "workbook:spec:1" || !newest.IsBuiltIn() {
		t.Fatalf("Newest(spec) with no project versions = %+v, %v", newest, ok)
	}
	if _, ok := (RelicSchemaListing{}).Newest("spec"); ok {
		t.Error("a listing without WithBuiltIns finds a built-in")
	}
	project := RelicSchemaVersion{Type: "spec", Version: 1, ID: relicTestID(50), Bytes: 10, Blob: testBlobID(1)}
	other := RelicSchemaVersion{Type: "transcript", Version: 1, ID: relicTestID(51), Bytes: 10, Blob: testBlobID(2)}
	listing := RelicSchemaListing{Versions: []RelicSchemaVersion{project, other}}.WithBuiltIns()
	if newest, ok := listing.Newest("spec"); !ok || newest != project {
		t.Errorf("Newest(spec) after a project version = %+v, %v; want the project's", newest, ok)
	}
	if newest, ok := listing.Newest("plan"); !ok || newest.ID != "workbook:plan:1" {
		t.Errorf("Newest(plan) = %+v, %v; want the built-in", newest, ok)
	}
	if found, ok := listing.ByID("workbook:spec:1"); !ok || found.Type != "spec" || !found.IsBuiltIn() {
		t.Errorf("ByID(workbook:spec:1) = %+v, %v", found, ok)
	}
	if found, ok := listing.ByID(relicTestID(50)); !ok || found != project {
		t.Errorf("ByID(project) = %+v, %v", found, ok)
	}
	if found, ok := listing.Number("spec", 1); !ok || found != project {
		t.Errorf("Number(spec, 1) = %+v, %v; want the project's", found, ok)
	}
	if _, ok := listing.Number("plan", 1); ok {
		t.Error("Number(plan, 1) finds a built-in")
	}
	if got, want := listing.Types(), []string{"spec", "transcript"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Types() = %v, want %v", got, want)
	}
	if got, want := listing.EffectiveTypes(), []string{"plan", "spec", "transcript"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveTypes() = %v, want %v", got, want)
	}
	if again := listing.WithBuiltIns(); !reflect.DeepEqual(again, listing) {
		t.Errorf("WithBuiltIns twice = %+v, want %+v", again, listing)
	}
	encoded, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "workbook:") {
		t.Errorf("an effective listing encodes as %s, with a built-in in it", encoded)
	}
	state := genesisState(t, testVocabulary(t))
	state.Config.Schemas = &RelicSchemaListing{Versions: []RelicSchemaVersion{project}}
	state.Config.Schemas.BuiltIn = listing.BuiltIn
	stored := mustEncode(t, state)
	if strings.Contains(string(stored), "workbook:") {
		t.Errorf("a checkpoint whose listing carries built-ins encodes with them: %s", stored)
	}
}

func TestStoredListingRefusesABuiltInName(t *testing.T) {
	state := genesisState(t, testVocabulary(t))
	state.Config.Schemas = &RelicSchemaListing{Versions: []RelicSchemaVersion{
		{Type: "spec", Version: 1, ID: "workbook:spec:1", Bytes: 10, Blob: testBlobID(1)},
	}}
	if _, err := DecodeConfigStateDocument(marshalRelicDocument(t, state)); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a checkpoint listing a built-in name = %v, want corrupt data", err)
	}
	put := ConfigOperation{ID: "workbook:spec:1", Type: ConfigRelicSchemaPut,
		RelicSchema: &RelicSchemaChange{Type: "spec", Bytes: 10, Blob: testBlobID(1)}}
	if err := validateConfigOperationDocument(put); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a schema.put whose ID is a built-in name = %v, want corrupt data", err)
	}
}

func builtInCreate(schema string) RelicOperation {
	return RelicOperation{ID: relicDocCreateOp, Type: RelicOperationCreate, Relic: &RelicCreateData{
		Title: "Task archive compaction", Type: "spec", Schema: schema, CreatedAt: relicDocTime,
	}}
}

func TestBuiltInSchemaNamesDeclareGeneration8(t *testing.T) {
	if SupportedFormatGeneration != 8 || RelicBuiltInSchemaMinReader != 8 {
		t.Fatalf("SupportedFormatGeneration = %d, RelicBuiltInSchemaMinReader = %d, want 8 and 8",
			SupportedFormatGeneration, RelicBuiltInSchemaMinReader)
	}
	migrate := func(schema string) RelicOperation {
		return RelicOperation{ID: relicTestID(60), Type: RelicOperationMigrate, RelicType: "spec", Schema: schema}
	}
	for label, test := range map[string]struct {
		operation RelicOperation
		want      int
	}{
		"a create naming a built-in":           {builtInCreate("workbook:spec:1"), 8},
		"a create naming an unknown built-in":  {builtInCreate("workbook:spec:9"), 8},
		"a create naming a ULID":               {builtInCreate(relicTestID(61)), 6},
		"an open create":                       {builtInCreate(""), 6},
		"a migrate naming a built-in":          {migrate("workbook:spec:1"), 8},
		"a migrate naming a ULID":              {migrate(relicTestID(61)), 6},
		"a migrate to an open type":            {RelicOperation{ID: relicTestID(60), Type: RelicOperationMigrate, RelicType: "note"}, 6},
		"a retire, as before":                  {RelicOperation{ID: relicTestID(62), Type: RelicOperationRetire}, 7},
		"a property.set beside nothing else":   {RelicOperation{ID: relicTestID(63), Type: RelicOperationPropertySet, Name: "a", Value: json.RawMessage(`1`)}, 6},
		"an unretire, as before":               {RelicOperation{ID: relicTestID(64), Type: RelicOperationUnretire}, 7},
		"a migrate naming a newer built-in":    {migrate("workbook:open-question:12"), 8},
		"a create naming a built-in of a type": {builtInCreate("workbook:plan:1"), 8},
	} {
		pack := singleRelicOperationPack(test.operation)
		if pack.MinReader != test.want {
			t.Errorf("%s: the pack declares %d, want %d", label, pack.MinReader, test.want)
		}
		if _, err := EncodeDocument(pack); err != nil {
			t.Errorf("%s: EncodeDocument = %v", label, err)
		}
	}
	checkpoint, _ := relicCheckpointForTest(RelicBoundaryRetired)
	if checkpoint.MinReader != 7 {
		t.Errorf("a checkpoint pack declares %d, want 7", checkpoint.MinReader)
	}

	// The folded state is 8, and stays 8 after a migrate to a ULID.
	created, err := ApplyRelic(nil, relicFoldPack(1, builtInCreate("workbook:spec:1")))
	if err != nil {
		t.Fatalf("ApplyRelic(create naming a built-in) = %v", err)
	}
	if created.State.MinReader != 8 || created.State.Relic.Schema != "workbook:spec:1" {
		t.Fatalf("the created state = minReader %d, schema %q", created.State.MinReader, created.State.Relic.Schema)
	}
	moved, err := ApplyRelic(&created, relicFoldPack(2, migrate(relicTestID(61))))
	if err != nil {
		t.Fatalf("ApplyRelic(migrate to a ULID) = %v", err)
	}
	if moved.State.MinReader != 8 || moved.State.Relic.Schema != relicTestID(61) {
		t.Errorf("after a migrate to a ULID = minReader %d, schema %q", moved.State.MinReader, moved.State.Relic.Schema)
	}
	plain, err := ApplyRelic(nil, relicFoldPack(1, builtInCreate(relicTestID(61))))
	if err != nil || plain.State.MinReader != 6 {
		t.Fatalf("ApplyRelic(create naming a ULID) = %d, %v; want 6", plain.State.MinReader, err)
	}
	adopted, err := ApplyRelic(&plain, relicFoldPack(2, migrate("workbook:spec:1")))
	if err != nil || adopted.State.MinReader != 8 {
		t.Fatalf("ApplyRelic(migrate to a built-in) = %d, %v; want 8", adopted.State.MinReader, err)
	}

	// A pack naming a built-in with a marker of 6 or 7 is corrupt.
	for _, marker := range []int{6, 7} {
		for label, operation := range map[string]RelicOperation{
			"create":  builtInCreate("workbook:spec:1"),
			"migrate": migrate("workbook:spec:1"),
		} {
			pack := singleRelicOperationPack(operation)
			pack.MinReader = marker
			requireRelicPackRefused(t, label+" naming a built-in at "+itoa(marker), pack)
		}
	}
	// A ULID pack at 8 is corrupt too: a pack declares exactly what it needs.
	raised := singleRelicOperationPack(builtInCreate(relicTestID(61)))
	raised.MinReader = 8
	requireRelicPackRefused(t, "a ULID create at 8", raised)
}

// A compacted relic whose state names a built-in keeps 8: its checkpoint
// state declares it, and so does every state after it.
func TestCheckpointOfABuiltInRelicDeclaresGeneration8(t *testing.T) {
	pack, stored := relicCheckpointForTest(RelicBoundaryRetired)
	stored.State.Relic.Schema = "workbook:spec:1"
	stored.State.MinReader = 8
	if err := ValidateRelicCheckpointRoot(pack, stored); err != nil {
		t.Fatalf("ValidateRelicCheckpointRoot(a built-in relic at 8) = %v", err)
	}
	plain, plainStored := relicCheckpointForTest(RelicBoundaryRetired)
	if err := ValidateRelicCheckpointRoot(plain, plainStored); err != nil {
		t.Fatalf("ValidateRelicCheckpointRoot(an open relic at 7) = %v", err)
	}
	// A checkpoint keeps the watermark of the tip it replaced, so a relic that
	// once named a built-in may stay at 8 after it moved to a project version.
	raisedPlain := plainStored
	raisedPlain.State.MinReader = 8
	if err := ValidateRelicCheckpointRoot(plain, raisedPlain); err != nil {
		t.Errorf("a checkpoint of an open relic at 8 = %v, want it accepted", err)
	}
	lowPack := plain
	lowPack.MinReader = 6
	if err := ValidateRelicCheckpointRoot(lowPack, plainStored); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a checkpoint pack at 6 = %v, want corrupt data", err)
	}
	lowState := plainStored
	lowState.State.MinReader = 6
	if err := ValidateRelicCheckpointRoot(plain, lowState); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a checkpoint state at 6 = %v, want corrupt data", err)
	}
	low := stored
	low.State.MinReader = 7
	if err := ValidateRelicCheckpointRoot(pack, low); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a checkpoint of a built-in relic at 7 = %v, want corrupt data", err)
	}
	unretire := NewRelicOperationPack(relicDocProject, relicDocRelic, relicCheckpointGeneration, relicDocActor, 5,
		relicCheckpointWall.Add(time.Minute), []RelicOperation{{ID: relicTestID(7300), Type: RelicOperationUnretire}})
	after, err := ApplyRelic(&stored, unretire)
	if err != nil {
		t.Fatalf("ApplyRelic(unretire after the checkpoint) = %v", err)
	}
	if after.State.MinReader != 8 {
		t.Errorf("the state after the checkpoint declares %d, want 8", after.State.MinReader)
	}
}

func TestStateNamingABuiltInBelowGeneration8IsCorrupt(t *testing.T) {
	state := goldenRelicState()
	state.Relic.Schema = "workbook:spec:1"
	state.MinReader = 7
	requireRelicStateRefused(t, "a built-in at 7", state)
	state.MinReader = 6
	requireRelicStateRefused(t, "a built-in at 6", state)
	for _, schema := range []string{"workbook:spec:1", "workbook:spec:9"} {
		state.Relic.Schema = schema
		state.MinReader = 8
		encoded, err := EncodeDocument(state)
		if err != nil {
			t.Fatalf("EncodeDocument(%s at 8) = %v", schema, err)
		}
		decoded, err := DecodeRelicStateDocument(encoded)
		if err != nil || decoded.Relic.Schema != schema || decoded.MinReader != 8 {
			t.Errorf("DecodeRelicStateDocument(%s at 8) = %q %d, %v", schema, decoded.Relic.Schema, decoded.MinReader, err)
		}
	}
	// A state at 8 that names a ULID or nothing is still valid: a relic that
	// once named a built-in keeps the watermark.
	for _, schema := range []string{relicTestID(50), ""} {
		state.Relic.Schema = schema
		if _, err := EncodeDocument(state); err != nil {
			t.Errorf("EncodeDocument(schema %q at 8) = %v", schema, err)
		}
	}
}

func TestRelicSchemaRefForBuiltIns(t *testing.T) {
	project := RelicSchemaVersion{Type: "spec", Version: 1, ID: relicTestID(50), Bytes: 10, Blob: testBlobID(1)}
	for label, listing := range map[string]RelicSchemaListing{
		"bare":      {Versions: []RelicSchemaVersion{project}},
		"effective": RelicSchemaListing{Versions: []RelicSchemaVersion{project}}.WithBuiltIns(),
	} {
		for _, test := range []struct {
			relic RelicData
			want  RelicSchemaRef
			text  string
		}{
			{RelicData{Type: "spec", Schema: "workbook:spec:1"}, RelicSchemaRef{Type: "spec", ID: "workbook:spec:1", BuiltIn: true},
				`{"type":"spec","id":"workbook:spec:1","builtIn":true}`},
			{RelicData{Type: "spec", Schema: "workbook:spec:2"}, RelicSchemaRef{Type: "spec", ID: "workbook:spec:2", BuiltIn: true},
				`{"type":"spec","id":"workbook:spec:2","builtIn":true}`},
			{RelicData{Type: "spec", Schema: relicTestID(50)}, RelicSchemaRef{Type: "spec", ID: relicTestID(50), Version: 1},
				`{"type":"spec","id":"` + relicTestID(50) + `","version":1}`},
		} {
			ref := ResolveRelicSchemaRef(test.relic, listing)
			if ref == nil || *ref != test.want {
				t.Errorf("%s: ResolveRelicSchemaRef(%s) = %+v, want %+v", label, test.relic.Schema, ref, test.want)
				continue
			}
			encoded, err := json.Marshal(ref)
			if err != nil || string(encoded) != test.text {
				t.Errorf("%s: %s encodes as %s, %v; want %s", label, test.relic.Schema, encoded, err, test.text)
			}
		}
	}
	builtIn, _, _, _ := BuiltInRelicSchema("workbook:spec:1")
	if builtIn.Name() != "workbook:spec:1" || project.Name() != "spec version 1" || project.IsBuiltIn() {
		t.Errorf("Name() = %q and %q", builtIn.Name(), project.Name())
	}

	document := `{"format":"workbook.relic","version":1,"title":"A spec","type":"spec",` +
		`"schema":{"type":"spec","id":"workbook:spec:1","builtIn":true},"properties":{"status":"draft"},"parts":[],"links":[]}`
	decoded, err := DecodeRelicDocument([]byte(document))
	if err != nil || decoded.Schema == nil || !decoded.Schema.BuiltIn {
		t.Fatalf("DecodeRelicDocument(builtIn) = %+v, %v", decoded.Schema, err)
	}
	input, err := RelicCreateInputFromDocument(decoded)
	if err != nil || input.Schema != "workbook:spec:1" {
		t.Errorf("RelicCreateInputFromDocument(built-in) = %q, %v", input.Schema, err)
	}
	unknown := strings.Replace(document, "workbook:spec:1", "workbook:spec:2", 1)
	decoded, _ = DecodeRelicDocument([]byte(unknown))
	if input, err := RelicCreateInputFromDocument(decoded); err != nil || input.Schema != "workbook:spec:2" {
		t.Errorf("RelicCreateInputFromDocument(unknown built-in) = %q, %v", input.Schema, err)
	}
	for _, bad := range []string{"spec", "workbook:spec", "workbook:spec:01"} {
		decoded, _ = DecodeRelicDocument([]byte(strings.Replace(document, "workbook:spec:1", bad, 1)))
		if _, err := RelicCreateInputFromDocument(decoded); CategoryOf(err) != CategoryValidation {
			t.Errorf("RelicCreateInputFromDocument(%q) = %v, want validation", bad, err)
		}
	}
}

func TestRefusalNamesABuiltIn(t *testing.T) {
	builtIn, _, _, _ := BuiltInRelicSchema("workbook:spec:1")
	problem := RelicSchemaProblem{Where: `property "status"`, Keyword: "required", Message: "the property is required"}
	if got, want := RelicSchemaRefusal(builtIn, problem).Error(),
		`relic does not fit schema workbook:spec:1: property "status": the property is required (required)`; got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
	project := RelicSchemaVersion{Type: "spec", Version: 3, ID: relicTestID(50)}
	if got, want := RelicSchemaRefusal(project, problem).Error(),
		`relic does not fit schema spec version 3: property "status": the property is required (required)`; got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
}

// checkShapeDocument judges a relic document's properties and parts under a
// built-in schema, as a relic created from it would be judged.
func checkShapeDocument(t *testing.T, path, schemaID string, edit func(*RelicDocument)) *RelicSchemaProblem {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := DecodeRelicDocument(data)
	if err != nil {
		t.Fatalf("DecodeRelicDocument(%s) = %v", path, err)
	}
	if edit != nil {
		edit(&document)
	}
	input, err := RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatalf("RelicCreateInputFromDocument(%s) = %v", path, err)
	}
	_, _, schema, ok := BuiltInRelicSchema(schemaID)
	if !ok {
		t.Fatalf("no built-in %s", schemaID)
	}
	if schema.Type() != input.Type {
		t.Fatalf("%s is a %s, not a %s", path, input.Type, schema.Type())
	}
	relic := RelicData{Type: input.Type, Schema: schemaID, Properties: input.Properties}
	fields := map[string]map[string]json.RawMessage{}
	for index, part := range input.Parts {
		id := relicTestID(9000 + index)
		stored := RelicPart{ID: id, Type: part.Type, Position: itoa(index+1) + "/1"}
		if part.Body != nil {
			stored.Body = &RelicBody{Format: part.Body.Format, Version: id, Bytes: int64(len(part.Body.Content))}
		}
		relic.Parts = append(relic.Parts, stored)
		fields[id] = part.Fields
	}
	return schema.CheckRelic(relic, fields, nil)
}

func TestDesignRelicShapeFitsTheDefaultSpec(t *testing.T) {
	if problem := checkShapeDocument(t, "testdata/design-relic-shape.json", "workbook:spec:1", nil); problem != nil {
		t.Fatalf("the design relic's shape does not fit workbook:spec:1: %s", problem)
	}
	problem := checkShapeDocument(t, "testdata/design-relic-shape.json", "workbook:spec:1", func(document *RelicDocument) {
		delete(document.Properties, "status")
	})
	if problem == nil || problem.Keyword != "required" || problem.Where != `property "status"` {
		t.Errorf("the shape without status = %v, want status required", problem)
	}
}

func TestPlanShapeFitsTheDefaultPlan(t *testing.T) {
	if problem := checkShapeDocument(t, "testdata/plan-relic-shape.json", "workbook:plan:1", nil); problem != nil {
		t.Fatalf("this plan's shape does not fit workbook:plan:1: %s", problem)
	}
	problem := checkShapeDocument(t, "testdata/plan-relic-shape.json", "workbook:plan:1", func(document *RelicDocument) {
		for index, part := range document.Parts {
			if part.Type == "step" {
				document.Parts[index].Fields["needs"] = json.RawMessage(`"Task 1"`)
				return
			}
		}
	})
	if problem == nil || problem.Keyword != "type" {
		t.Errorf("a step whose needs is not a list = %v, want a (type) refusal", problem)
	}
}
