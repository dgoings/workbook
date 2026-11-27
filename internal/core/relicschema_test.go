package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// testSchemaSet holds compiled schema versions in memory, standing in for the
// project settings: the newest version of each type is what a new schema's
// cross-type reference pins.
type testSchemaSet struct {
	next    int
	newest  map[string]RelicSchemaVersion
	byID    map[string]*RelicSchema
	stored  map[string][]byte
	version map[string]RelicSchemaVersion
}

func newTestSchemaSet() *testSchemaSet {
	return &testSchemaSet{
		newest:  map[string]RelicSchemaVersion{},
		byID:    map[string]*RelicSchema{},
		stored:  map[string][]byte{},
		version: map[string]RelicSchemaVersion{},
	}
}

func (set *testSchemaSet) lookup(relicType string) (RelicSchemaVersion, *RelicSchema, bool, error) {
	version, ok := set.newest[relicType]
	if !ok {
		return RelicSchemaVersion{}, nil, false, nil
	}
	return version, set.byID[version.ID], true, nil
}

func (set *testSchemaSet) pinned(id string) (*RelicSchema, error) {
	schema, ok := set.byID[id]
	if !ok {
		return nil, fmt.Errorf("no schema version %s", id)
	}
	return schema, nil
}

// put compiles source as the next version of relicType and records it.
func (set *testSchemaSet) put(t *testing.T, relicType, source string) (RelicSchemaVersion, RelicSchemaDocument, *RelicSchema) {
	t.Helper()
	document, schema, err := CompileRelicSchema(relicType, []byte(source), set.lookup)
	if err != nil {
		t.Fatalf("compile %s: %v", relicType, err)
	}
	encoded, err := EncodeDocument(document)
	if err != nil {
		t.Fatalf("encode %s: %v", relicType, err)
	}
	set.next++
	number := 1
	if previous, ok := set.newest[relicType]; ok {
		number = previous.Version + 1
	}
	version := RelicSchemaVersion{
		Type: relicType, Version: number, ID: fmt.Sprintf("01M9%022d", set.next),
		Bytes: int64(len(encoded)), Blob: fmt.Sprintf("%040d", set.next),
	}
	set.newest[relicType] = version
	set.byID[version.ID] = schema
	set.stored[version.ID] = encoded
	set.version[version.ID] = version
	return version, document, schema
}

func mustCompileSchema(t *testing.T, relicType, source string) *RelicSchema {
	t.Helper()
	_, schema, err := CompileRelicSchema(relicType, []byte(source), newTestSchemaSet().lookup)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return schema
}

func requireSchemaRefusal(t *testing.T, label string, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: compiled, want a refusal", label)
		return
	}
	if CategoryOf(err) != CategoryValidation {
		t.Errorf("%s: category %q, want validation: %v", label, CategoryOf(err), err)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q lacks %q", label, err.Error(), want)
		}
	}
}

const planSchemaSource = `{"$defs":{"step":{"type":"object","required":["title"],
  "properties":{"title":{"type":"string"}}}}}`

const everyWordSchemaSource = `{"title":"Spec","description":"A design for one feature.",
 "properties":{"type":"object","required":["status"],"additionalProperties":false,
   "properties":{
     "status":{"enum":["draft","approved","superseded"]},
     "score":{"type":["number","null"],"minimum":0,"maximum":10},
     "tags":{"type":"array","items":{"type":"string","minLength":1,"maxLength":20},"minItems":0,"maxItems":5},
     "count":{"type":"integer"},
     "kind":{"const":"spec","title":"Kind","description":"Always spec."},
     "owner":{"$ref":"#/$defs/who"}}},
 "parts":{
   "section":{"description":"A heading and a body.","fields":{"type":"object","required":["heading"],
       "properties":{"heading":{"type":"string","maxLength":200}}},
     "body":"required","bodyFormats":["md"]},
   "step":{"fields":{"$ref":"plan#/$defs/step"},"body":"optional"}},
 "links":{"relationships":["blocks"],"pointer":{"type":"object"}},
 "$defs":{"who":{"type":"string","minLength":1}}}`

func TestCompileAcceptsEverySupportedWord(t *testing.T) {
	set := newTestSchemaSet()
	plan, _, _ := set.put(t, "plan", planSchemaSource)
	version, document, schema := set.put(t, "spec", everyWordSchemaSource)
	if schema == nil || schema.Type() != "spec" {
		t.Fatalf("schema = %v", schema)
	}
	if document.Format != "workbook.relic-schema" || document.Version != 1 || document.Type != "spec" {
		t.Errorf("document header = %q %d %q", document.Format, document.Version, document.Type)
	}
	if len(document.References) != 1 || document.References["plan"] != plan.ID {
		t.Errorf("references = %v, want plan pinned to %s", document.References, plan.ID)
	}
	if canonical, err := CanonicalRelicValue([]byte(everyWordSchemaSource)); err != nil || !bytes.Equal(canonical, document.Schema) {
		t.Errorf("stored schema is not canonical: %s", document.Schema)
	}

	again, _, err := CompileRelicSchema("spec", document.Schema, set.lookup)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	first, _ := EncodeDocument(document)
	second, _ := EncodeDocument(again)
	if !bytes.Equal(first, second) {
		t.Errorf("recompiling the stored schema changed it:\n%s\n%s", first, second)
	}

	loaded, err := LoadRelicSchema(version, set.stored[version.ID], set.pinned)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if problem := loaded.CheckProperty("status", json.RawMessage(`"approved"`)); problem != nil {
		t.Errorf("loaded schema refused a good status: %v", problem)
	}
	if problem := loaded.CheckProperty("status", json.RawMessage(`"lost"`)); problem == nil || problem.Keyword != "enum" {
		t.Errorf("loaded schema problem = %v, want enum", problem)
	}
	decoded, err := DecodeRelicSchemaDocument(set.stored[version.ID])
	if err != nil || decoded.Type != "spec" || decoded.References["plan"] != plan.ID {
		t.Errorf("decode = %+v, %v", decoded, err)
	}
}

func TestSchemaRefusesUnsupportedWordsAtEveryDepth(t *testing.T) {
	words := map[string]string{
		"pattern": `"^a"`, "format": `"date"`, "allOf": `[]`, "anyOf": `[]`, "oneOf": `[]`,
		"not": `{}`, "$schema": `"x"`, "$id": `"x"`, "if": `{}`, "minProperties": `1`,
	}
	places := []struct{ name, template, path string }{
		{"top", `{%s}`, "/"},
		{"property", `{"properties":{"properties":{"x":{%s}}}}`, "/properties/properties/x/"},
		{"items", `{"properties":{"properties":{"x":{"items":{%s}}}}}`, "/properties/properties/x/items/"},
		{"defs", `{"$defs":{"x":{%s}}}`, "/$defs/x/"},
		{"part fields", `{"parts":{"section":{"fields":{%s}}}}`, "/parts/section/fields/"},
		{"link pointer", `{"links":{"pointer":{"properties":{"a":{%s}}}}}`, "/links/pointer/properties/a/"},
	}
	for word, value := range words {
		for _, place := range places {
			source := fmt.Sprintf(place.template, fmt.Sprintf("%q:%s", word, value))
			_, _, err := CompileRelicSchema("spec", []byte(source), newTestSchemaSet().lookup)
			requireSchemaRefusal(t, word+" at "+place.name, err,
				"schema for spec: "+place.path+word+":", `"`+word+`"`)
		}
	}
	_, _, err := CompileRelicSchema("spec", []byte(`{"color":"red"}`), newTestSchemaSet().lookup)
	requireSchemaRefusal(t, "unknown top-level member", err, "schema for spec: /color:", `"color"`)
	_, _, err = CompileRelicSchema("spec", []byte(`{"parts":{"section":{"heading":1}}}`), newTestSchemaSet().lookup)
	requireSchemaRefusal(t, "unknown part rule member", err, "/parts/section/heading:", `"heading"`)
	_, _, err = CompileRelicSchema("spec", []byte(`{"links":{"rel":[]}}`), newTestSchemaSet().lookup)
	requireSchemaRefusal(t, "unknown links member", err, "/links/rel:", `"rel"`)
}

func TestSchemaRefusesBadWordValues(t *testing.T) {
	cases := []struct{ label, source, path string }{
		{"additionalProperties schema", `{"properties":{"additionalProperties":{"type":"string"}}}`, "/properties/additionalProperties:"},
		{"type date", `{"properties":{"type":"date"}}`, "/properties/type:"},
		{"type empty list", `{"properties":{"type":[]}}`, "/properties/type:"},
		{"type repeated", `{"properties":{"type":["string","string"]}}`, "/properties/type:"},
		{"required repeated", `{"properties":{"required":["a","a"]}}`, "/properties/required:"},
		{"required not strings", `{"properties":{"required":[1]}}`, "/properties/required:"},
		{"minLength negative", `{"properties":{"minLength":-1}}`, "/properties/minLength:"},
		{"minLength fraction", `{"properties":{"minLength":1.5}}`, "/properties/minLength:"},
		{"maxItems string", `{"properties":{"maxItems":"3"}}`, "/properties/maxItems:"},
		{"minimum string", `{"properties":{"minimum":"3"}}`, "/properties/minimum:"},
		{"enum empty", `{"properties":{"enum":[]}}`, "/properties/enum:"},
		{"items list", `{"properties":{"items":[{"type":"string"}]}}`, "/properties/items:"},
		{"schema not object", `{"properties":{"properties":{"x":true}}}`, "/properties/properties/x:"},
		{"title not string", `{"title":1}`, "/title:"},
		{"body sometimes", `{"parts":{"section":{"body":"sometimes"}}}`, "/parts/section/body:"},
		{"body formats label", `{"parts":{"section":{"bodyFormats":["MD"]}}}`, "/parts/section/bodyFormats"},
		{"body formats empty", `{"parts":{"section":{"bodyFormats":[]}}}`, "/parts/section/bodyFormats:"},
		{"part type name", `{"parts":{"Section":{}}}`, "/parts/Section:"},
		{"relationship name", `{"links":{"relationships":["Blocks"]}}`, "/links/relationships"},
		{"defs below top", `{"properties":{"$defs":{}}}`, "/properties/$defs:"},
		{"defs name", `{"$defs":{"a/b":{}}}`, "/$defs/a~1b:"},
		{"not an object", `[]`, "schema for spec:"},
		{"not JSON", `{"title":`, "schema for spec:"},
	}
	for _, c := range cases {
		_, _, err := CompileRelicSchema("spec", []byte(c.source), newTestSchemaSet().lookup)
		requireSchemaRefusal(t, c.label, err, c.path)
	}
}

func refChainSchema(links int) string {
	var defs []string
	for index := 1; index < links; index++ {
		defs = append(defs, fmt.Sprintf(`"d%d":{"$ref":"#/$defs/d%d"}`, index, index+1))
	}
	defs = append(defs, fmt.Sprintf(`"d%d":{"type":"string"}`, links))
	return `{"properties":{"properties":{"x":{"$ref":"#/$defs/d1"}}},"$defs":{` + strings.Join(defs, ",") + `}}`
}

func TestSchemaRefusesOutsideAndLoopingRefs(t *testing.T) {
	set := newTestSchemaSet()
	set.put(t, "plan", planSchemaSource)
	set.put(t, "spec", `{"$defs":{"a":{"type":"string"}}}`)
	refAt := func(ref string) string {
		return fmt.Sprintf(`{"properties":{"properties":{"x":{"$ref":%q}}},"$defs":{"a":{"type":"string"}}}`, ref)
	}
	for _, ref := range []string{
		"https://x/s.json", "http://x/s.json", "file:a.json", "other.json#/$defs/a", "a.json",
		"#/properties/a", "#/$defs/missing", "#/$defs/a/properties", "#", "spec#/$defs/a",
		"ghost#/$defs/a", "plan#/$defs/missing", "plan#/$defs/step/properties/title",
	} {
		_, _, err := CompileRelicSchema("spec", []byte(refAt(ref)), set.lookup)
		requireSchemaRefusal(t, ref, err, "schema for spec: /properties/properties/x/$ref:")
	}
	_, _, err := CompileRelicSchema("spec", []byte(refAt("ghost#/$defs/a")), set.lookup)
	requireSchemaRefusal(t, "ghost message", err, "ghost")

	loop := `{"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"type":"object","properties":{"x":{"$ref":"#/$defs/a"}}}}}`
	_, _, err = CompileRelicSchema("spec", []byte(loop), set.lookup)
	requireSchemaRefusal(t, "loop", err, "loop", "#/$defs/a", "#/$defs/b")
	_, _, err = CompileRelicSchema("spec", []byte(`{"$defs":{"a":{"items":{"$ref":"#/$defs/a"}}}}`), set.lookup)
	requireSchemaRefusal(t, "self loop", err, "loop", "#/$defs/a")

	if _, _, err := CompileRelicSchema("spec", []byte(refChainSchema(MaxRelicSchemaRefDepth)), set.lookup); err != nil {
		t.Errorf("a chain of %d refs: %v", MaxRelicSchemaRefDepth, err)
	}
	_, _, err = CompileRelicSchema("spec", []byte(refChainSchema(MaxRelicSchemaRefDepth+1)), set.lookup)
	requireSchemaRefusal(t, "a chain of 33 refs", err, "33", "32")

	// A ref into another type counts the refs that type's definition follows.
	deep := newTestSchemaSet()
	deep.put(t, "plan", strings.Replace(refChainSchema(MaxRelicSchemaRefDepth+1), `"properties":{"properties":{"x":{"$ref":"#/$defs/d1"}}},`, "", 1))
	_, _, err = CompileRelicSchema("spec", []byte(`{"properties":{"$ref":"plan#/$defs/d1"}}`), deep.lookup)
	requireSchemaRefusal(t, "a chain of 33 refs across types", err, "33", "32")
}

func TestSchemaSizeLimit(t *testing.T) {
	if MaxRelicSchemaBytes != 65536 {
		t.Fatalf("MaxRelicSchemaBytes = %d, want 65536", MaxRelicSchemaBytes)
	}
	withDescription := func(size int) string {
		return `{"description":"` + strings.Repeat("a", size) + `","properties":{"required":["status"]}}`
	}
	document, _, err := CompileRelicSchema("spec", []byte(withDescription(0)), nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	empty, _ := EncodeDocument(document)
	fill := MaxRelicSchemaBytes - len(empty)

	document, _, err = CompileRelicSchema("spec", []byte(withDescription(fill)), nil)
	if err != nil {
		t.Fatalf("a %d-byte document: %v", MaxRelicSchemaBytes, err)
	}
	stored, _ := EncodeDocument(document)
	if len(stored) != MaxRelicSchemaBytes {
		t.Fatalf("stored = %d bytes, want %d", len(stored), MaxRelicSchemaBytes)
	}
	version := RelicSchemaVersion{Type: "spec", Version: 1, ID: "01M90000000000000000000001", Bytes: int64(len(stored))}
	if _, err := LoadRelicSchema(version, stored, nil); err != nil {
		t.Errorf("load a %d-byte document: %v", MaxRelicSchemaBytes, err)
	}

	_, _, err = CompileRelicSchema("spec", []byte(withDescription(fill+1)), nil)
	requireSchemaRefusal(t, "a 65537-byte document", err, "65537", "65536")

	over := bytes.Replace(stored, []byte(`"description":"`), []byte(`"description":"a`), 1)
	_, err = LoadRelicSchema(version, over, nil)
	if CategoryOf(err) != CategoryCorruptData || !strings.Contains(err.Error(), "spec version 1") {
		t.Errorf("load a 65537-byte document = %v, want corrupt data naming spec version 1", err)
	}
	if _, err := DecodeRelicSchemaDocument(over); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("decode a 65537-byte document = %v, want corrupt data", err)
	}
}

func TestLoadRelicSchemaRefusesBadDocuments(t *testing.T) {
	set := newTestSchemaSet()
	version, _, _ := set.put(t, "spec", `{"properties":{"required":["status"]}}`)
	stored := set.stored[version.ID]
	cases := map[string][]byte{
		"type mismatch":    bytes.Replace(stored, []byte(`"type":"spec"`), []byte(`"type":"plan"`), 1),
		"format":           bytes.Replace(stored, []byte(`workbook.relic-schema`), []byte(`workbook.relic`), 1),
		"version":          bytes.Replace(stored, []byte(`"version":1`), []byte(`"version":2`), 1),
		"unknown member":   bytes.Replace(stored, []byte(`{"format"`), []byte(`{"extra":1,"format"`), 1),
		"bad schema":       bytes.Replace(stored, []byte(`"required"`), []byte(`"pattern"`), 1),
		"unused reference": bytes.Replace(stored, []byte(`"type":"spec",`), []byte(`"type":"spec","references":{"plan":"01M90000000000000000000009"},`), 1),
		"not JSON":         []byte(`{`),
	}
	for label, data := range cases {
		_, err := LoadRelicSchema(version, data, set.pinned)
		if CategoryOf(err) != CategoryCorruptData || !strings.Contains(err.Error(), "spec version 1") {
			t.Errorf("%s: %v, want corrupt data naming spec version 1", label, err)
		}
	}
}

func TestPinnedReferenceKeepsItsMeaning(t *testing.T) {
	set := newTestSchemaSet()
	plan1, _, _ := set.put(t, "plan", `{"$defs":{"step":{"type":"string"}}}`)
	spec, document, _ := set.put(t, "spec", `{"properties":{"properties":{"next":{"$ref":"plan#/$defs/step"}}}}`)
	if document.References["plan"] != plan1.ID {
		t.Fatalf("references = %v", document.References)
	}
	plan2, _, _ := set.put(t, "plan", `{"$defs":{"step":{"type":"number"}}}`)
	if set.newest["plan"].ID != plan2.ID {
		t.Fatal("plan v2 is not the newest")
	}

	var asked []string
	loaded, err := LoadRelicSchema(spec, set.stored[spec.ID], func(id string) (*RelicSchema, error) {
		asked = append(asked, id)
		return set.pinned(id)
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(asked) != 1 || asked[0] != plan1.ID {
		t.Errorf("pinned asked for %v, want only plan v1 %s", asked, plan1.ID)
	}
	if problem := loaded.CheckProperty("next", json.RawMessage(`"write it"`)); problem != nil {
		t.Errorf("plan v1's rule refused a string: %v", problem)
	}
	if problem := loaded.CheckProperty("next", json.RawMessage(`3`)); problem == nil || problem.Keyword != "type" {
		t.Errorf("plan v1's rule = %v, want a type problem for a number", problem)
	}

	wrong := func(string) (*RelicSchema, error) { return set.byID[set.newest["spec"].ID], nil }
	if _, err := LoadRelicSchema(spec, set.stored[spec.ID], wrong); CategoryOf(err) != CategoryCorruptData {
		t.Errorf("a pin resolving to another type = %v, want corrupt data", err)
	}
}

func TestRelicSchemaRefusalWording(t *testing.T) {
	version := RelicSchemaVersion{Type: "spec", Version: 3}
	err := RelicSchemaRefusal(version, RelicSchemaProblem{Where: `property "status"`, Keyword: "enum", Message: `the value must be one of "draft", "approved"`})
	want := `relic does not fit schema spec version 3: property "status": the value must be one of "draft", "approved" (enum)`
	if err == nil || err.Error() != want || CategoryOf(err) != CategoryValidation {
		t.Errorf("refusal = %v, want %q as validation", err, want)
	}
}
