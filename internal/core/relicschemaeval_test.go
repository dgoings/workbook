package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func problemString(problem *RelicSchemaProblem) string {
	if problem == nil {
		return "<nil>"
	}
	return problem.String()
}

func TestSchemaEvaluationEdgeCases(t *testing.T) {
	schema := mustCompileSchema(t, "spec", `{"properties":{"required":["r"],"properties":{
	  "i":{"type":"integer"},
	  "m":{"maximum":0.3},
	  "low":{"minimum":-2.5},
	  "s":{"maxLength":2},
	  "e":{"enum":[{"a":2,"b":1},"x"]},
	  "c":{"const":null},
	  "n":{"type":["number","null"]},
	  "list":{"items":{"type":"string"},"minItems":1,"maxItems":2},
	  "obj":{"type":"object","required":["y"],"additionalProperties":false,"properties":{"y":{"type":"boolean"}}},
	  "r":{}}}}`)
	cases := []struct {
		name, value string
		keyword     string // "" means the value fits
	}{
		{"i", `1.0`, ""},
		{"i", `10e2`, ""},
		{"i", `1.5`, "type"},
		{"i", `"1"`, "type"},
		{"m", `0.30000000000000001`, "maximum"},
		{"m", `0.3000`, ""},
		{"m", `3e-1`, ""},
		{"m", `-1e999999999`, ""},
		{"m", `1e999999999`, "maximum"},
		{"low", `-2.5`, ""},
		{"low", `-2.50001`, "minimum"},
		{"s", `"é😀"`, ""},
		{"s", `"abc"`, "maxLength"},
		{"e", `{"b":1,"a":2}`, ""},
		{"e", `{"a":2}`, "enum"},
		{"e", `"x"`, ""},
		{"c", `null`, ""},
		{"c", `0`, "const"},
		{"n", `null`, ""},
		{"n", `true`, "type"},
		{"list", `["a"]`, ""},
		{"list", `[]`, "minItems"},
		{"list", `["a","b","c"]`, "maxItems"},
		{"list", `["a",1]`, "type"},
		{"obj", `{"y":true}`, ""},
		{"obj", `{}`, "required"},
		{"obj", `{"y":true,"z":1}`, "additionalProperties"},
	}
	for _, c := range cases {
		problem := schema.CheckProperty(c.name, json.RawMessage(c.value))
		switch {
		case c.keyword == "" && problem != nil:
			t.Errorf("%s = %s: refused: %s", c.name, c.value, problem)
		case c.keyword != "" && (problem == nil || problem.Keyword != c.keyword):
			t.Errorf("%s = %s: %s, want a %s problem", c.name, c.value, problemString(problem), c.keyword)
		}
	}
	relic := RelicData{Type: "spec", Properties: map[string]json.RawMessage{"r": json.RawMessage(`null`)}}
	if problem := schema.CheckRelic(relic, nil); problem != nil {
		t.Errorf("a present null passes required: %s", problem)
	}
	if got := problemString(schema.CheckProperty("list", json.RawMessage(`["a",1]`))); got != `property "list": at /1: the value must be a string (type)` {
		t.Errorf("nested problem = %s", got)
	}
	if got := problemString(schema.CheckProperty("obj", json.RawMessage(`{}`))); got != `property "obj": at /y: the member is required (required)` {
		t.Errorf("nested required = %s", got)
	}
}

const shapedSchemaSource = `{"properties":{"required":["b","status"],"additionalProperties":false,"properties":{
    "a":{"type":"string"},"b":{"type":"string"},"status":{"enum":["draft","approved"]}}},
  "parts":{
    "section":{"fields":{"required":["heading"],"additionalProperties":false,
        "properties":{"heading":{"type":"string","maxLength":5},"level":{"type":"integer"}}},
      "body":"required","bodyFormats":["md"]},
    "note":{"body":"none"},
    "free":{"bodyFormats":["txt"]}}}`

func TestCheckRelicNamesTheFirstProblem(t *testing.T) {
	schema := mustCompileSchema(t, "spec", shapedSchemaSource)
	body := &RelicBody{Format: "md", Version: "v", Bytes: 1}
	relic := RelicData{
		Type: "spec",
		Properties: map[string]json.RawMessage{
			"a": json.RawMessage(`1`), "status": json.RawMessage(`"lost"`), "zzz": json.RawMessage(`1`),
		},
		Parts: []RelicPart{
			{ID: "P0", Type: "ghost", Removed: true},
			{ID: "P1", Type: "section", Body: body},
			{ID: "P2", Type: "note", Body: body},
		},
	}
	fields := map[string]map[string]json.RawMessage{
		"P0": {"anything": json.RawMessage(`1`)},
		"P1": {"level": json.RawMessage(`"x"`), "heading": json.RawMessage(`"toolong"`)},
	}
	steps := []string{
		`property "a": the value must be a string (type)`,
		`property "b": the property is required (required)`,
		`property "status": the value must be one of "draft", "approved" (enum)`,
		`property "zzz": the property is not allowed (additionalProperties)`,
		`part P1 (section) field "heading": the value must be at most 5 characters (maxLength)`,
		`part P1 (section) field "level": the value must be an integer (type)`,
		`part P2 (note) body: the part must not have a body (body)`,
	}
	fix := []func(){
		func() { relic.Properties["a"] = json.RawMessage(`"x"`) },
		func() { relic.Properties["b"] = json.RawMessage(`"x"`) },
		func() { relic.Properties["status"] = json.RawMessage(`"draft"`) },
		func() { delete(relic.Properties, "zzz") },
		func() { fields["P1"]["heading"] = json.RawMessage(`"Goals"`) },
		func() { fields["P1"]["level"] = json.RawMessage(`2`) },
		func() { relic.Parts[2].Body = nil },
	}
	for index, want := range steps {
		if got := problemString(schema.CheckRelic(relic, fields)); got != want {
			t.Errorf("step %d: %s, want %s", index, got, want)
		}
		fix[index]()
	}
	if problem := schema.CheckRelic(relic, fields); problem != nil {
		t.Errorf("the fixed relic: %s (the removed part must not be judged)", problem)
	}

	open := mustCompileSchema(t, "spec", `{}`)
	if problem := open.CheckRelic(relic, fields); problem != nil {
		t.Errorf("an empty schema refused: %s", problem)
	}
	if got := (RelicSchemaProblem{Where: "part 3 (section)", Keyword: "parts", Message: "m"}).String(); got != "part 3 (section): m (parts)" {
		t.Errorf("String = %q", got)
	}
}

func TestCheckPieces(t *testing.T) {
	schema := mustCompileSchema(t, "spec", shapedSchemaSource)
	check := func(label string, problem *RelicSchemaProblem, want string) {
		t.Helper()
		if got := problemString(problem); got != want {
			t.Errorf("%s: %s, want %s", label, got, want)
		}
	}
	check("unknown property", schema.CheckProperty("color", json.RawMessage(`"red"`)),
		`property "color": the property is not allowed (additionalProperties)`)
	check("good property", schema.CheckProperty("a", json.RawMessage(`"x"`)), "<nil>")
	check("clear required", schema.CheckPropertyClear("status"),
		`property "status": the property is required (required)`)
	check("clear optional", schema.CheckPropertyClear("a"), "<nil>")

	md := &RelicBody{Format: "md", Version: "v", Bytes: 1}
	plain := &RelicBody{Version: "v", Bytes: 1}
	heading := map[string]json.RawMessage{"heading": json.RawMessage(`"Goals"`)}
	check("unlisted part type", schema.CheckPart("part 2", RelicPart{ID: "X", Type: "ghost"}, nil),
		`part 2 (ghost): the schema allows no part of this type (parts)`)
	check("missing body", schema.CheckPart("part X", RelicPart{ID: "X", Type: "section"}, heading),
		`part X (section) body: the part must have a body (body)`)
	check("missing field", schema.CheckPart("part X", RelicPart{ID: "X", Type: "section", Body: md}, nil),
		`part X (section) field "heading": the field is required (required)`)
	check("unknown field", schema.CheckPart("part X", RelicPart{ID: "X", Type: "section", Body: md},
		map[string]json.RawMessage{"heading": json.RawMessage(`"a"`), "x": json.RawMessage(`1`)}),
		`part X (section) field "x": the field is not allowed (additionalProperties)`)
	check("body under none", schema.CheckPart("part X", RelicPart{ID: "X", Type: "note", Body: md}, nil),
		`part X (note) body: the part must not have a body (body)`)
	check("label-less body, md", schema.CheckPart("part X", RelicPart{ID: "X", Type: "section", Body: plain}, heading),
		`part X (section) body: a body with no format counts as txt, which is not one of md (bodyFormats)`)
	check("wrong label", schema.CheckPart("part X", RelicPart{ID: "X", Type: "free", Body: md}, nil),
		`part X (free) body: the body format md is not one of txt (bodyFormats)`)
	check("label-less body, txt", schema.CheckPart("part X", RelicPart{ID: "X", Type: "free", Body: plain}, nil), "<nil>")
	check("good section", schema.CheckPart("part X", RelicPart{ID: "X", Type: "section", Body: md}, heading), "<nil>")

	anyPart := mustCompileSchema(t, "spec", `{"properties":{"properties":{"a":{"type":"string"}}}}`)
	check("parts absent", anyPart.CheckPart("part X", RelicPart{ID: "X", Type: "ghost", Body: md}, nil), "<nil>")
	check("additional allowed", anyPart.CheckProperty("other", json.RawMessage(`1`)), "<nil>")
}

func TestSchemaRefsAndSiblingsBothApply(t *testing.T) {
	set := newTestSchemaSet()
	set.put(t, "plan", planSchemaSource)
	_, _, schema := set.put(t, "spec", `{"properties":{"$ref":"#/$defs/props","properties":{"b":{"type":"string"}}},
	  "parts":{"step":{"fields":{"$ref":"plan#/$defs/step"}}},
	  "$defs":{"props":{"required":["a"],"additionalProperties":false,"properties":{"a":{"type":"string"}}}}}`)
	if got := problemString(schema.CheckProperty("b", json.RawMessage(`"x"`))); got != `property "b": the property is not allowed (additionalProperties)` {
		t.Errorf("sibling and ref: %s", got)
	}
	if got := problemString(schema.CheckPropertyClear("a")); got != `property "a": the property is required (required)` {
		t.Errorf("required through a ref: %s", got)
	}
	if got := problemString(schema.CheckPart("part X", RelicPart{ID: "X", Type: "step"}, nil)); got != `part X (step) field "title": the field is required (required)` {
		t.Errorf("fields through a cross-type ref: %s", got)
	}
}

func TestRelicSchemaSummary(t *testing.T) {
	schema := mustCompileSchema(t, "spec", `{"title":"Spec","description":"A design.",
	  "properties":{"required":["status","owner"],"properties":{"status":{"enum":["draft","approved",3]}}},
	  "parts":{"section":{"description":"A heading.","fields":{"required":["heading"]},"body":"required","bodyFormats":["md"]},
	    "note":{}}}`)
	want := RelicSchemaSummary{
		Title: "Spec", Description: "A design.",
		Required: []RelicSchemaPropertySummary{{Name: "owner"}, {Name: "status", Allowed: []string{"draft", "approved", "3"}}},
		Parts: []RelicSchemaPartSummary{
			{Type: "note", Body: "optional"},
			{Type: "section", Description: "A heading.", RequiredFields: []string{"heading"}, Body: "required", BodyFormats: []string{"md"}},
		},
	}
	if got := schema.Summary(); !reflect.DeepEqual(got, want) {
		t.Errorf("summary =\n%+v\nwant\n%+v", got, want)
	}
}

// fanOutDefs is $defs d1…d32 where each di refers to d(i+1) both as its own
// $ref and as the rule for member "a". Checking a value nested under "a"
// without remembering what was already judged visits about C(32+L, L) nodes
// for L levels.
func fanOutDefs(prefix string) string {
	var defs []string
	for index := 1; index < 32; index++ {
		next := fmt.Sprintf("%s#/$defs/d%d", prefix, index+1)
		defs = append(defs, fmt.Sprintf(`"d%d":{"$ref":%q,"properties":{"a":{"$ref":%q}}}`, index, next, next))
	}
	defs = append(defs, `"d32":{"type":"object"}`)
	return `"$defs":{` + strings.Join(defs, ",") + `}`
}

// nestedUnderA is levels objects nested under "a", with leaf at the bottom.
func nestedUnderA(levels int, leaf string) string {
	return strings.Repeat(`{"a":`, levels) + leaf + strings.Repeat(`}`, levels)
}

func requireQuickCheck(t *testing.T, label string, check func() *RelicSchemaProblem, want string) {
	t.Helper()
	start := time.Now()
	got := problemString(check())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("%s took %s", label, elapsed)
	}
	if got != want {
		t.Errorf("%s: %s, want %s", label, got, want)
	}
}

func TestSchemaCheckWithFanningRefsIsQuick(t *testing.T) {
	schema := mustCompileSchema(t, "spec", `{"properties":{"$ref":"#/$defs/d1"},`+fanOutDefs("")+`}`)
	for _, levels := range []int{20, MaxRelicValueDepth} {
		value := json.RawMessage(nestedUnderA(levels-1, `{}`))
		requireQuickCheck(t, fmt.Sprintf("property, %d levels, fits", levels),
			func() *RelicSchemaProblem { return schema.CheckProperty("a", value) }, "<nil>")
		relic := RelicData{Type: "spec", Properties: map[string]json.RawMessage{"a": value}}
		requireQuickCheck(t, fmt.Sprintf("relic, %d levels, fits", levels),
			func() *RelicSchemaProblem { return schema.CheckRelic(relic, nil) }, "<nil>")

		// d32, the last rule, judges the value 31 levels below the
		// properties; the property itself is the first of them.
		bad := json.RawMessage(nestedUnderA(levels-2, `1`))
		want := `property "a": at ` + strings.Repeat("/a", levels-2) + `: the value must be an object (type)`
		requireQuickCheck(t, fmt.Sprintf("property, %d levels, does not fit", levels),
			func() *RelicSchemaProblem { return schema.CheckProperty("a", bad) }, want)
		relic = RelicData{Type: "spec", Properties: map[string]json.RawMessage{"a": bad}}
		requireQuickCheck(t, fmt.Sprintf("relic, %d levels, does not fit", levels),
			func() *RelicSchemaProblem { return schema.CheckRelic(relic, nil) }, want)
	}
}

func TestSchemaCheckWithFanningCrossTypeRefsIsQuick(t *testing.T) {
	// Types t32…t1, each with one definition d that refers to the next
	// type's d as its own $ref and as the rule for member "a".
	set := newTestSchemaSet()
	set.put(t, "t32", `{"$defs":{"d":{"type":"object"}}}`)
	for index := 31; index >= 1; index-- {
		next := fmt.Sprintf("t%d#/$defs/d", index+1)
		set.put(t, fmt.Sprintf("t%d", index), fmt.Sprintf(`{"$defs":{"d":{"$ref":%q,"properties":{"a":{"$ref":%q}}}}}`, next, next))
	}
	_, _, schema := set.put(t, "spec", `{"properties":{"$ref":"t1#/$defs/d"},
	  "parts":{"step":{"fields":{"$ref":"t1#/$defs/d"}}}}`)
	levels := MaxRelicValueDepth
	value := json.RawMessage(nestedUnderA(levels-1, `{}`))
	requireQuickCheck(t, "property fits",
		func() *RelicSchemaProblem { return schema.CheckProperty("a", value) }, "<nil>")
	fields := map[string]json.RawMessage{"a": value}
	requireQuickCheck(t, "part fits",
		func() *RelicSchemaProblem {
			return schema.CheckPart("part X", RelicPart{ID: "X", Type: "step"}, fields)
		}, "<nil>")
	bad := map[string]json.RawMessage{"a": json.RawMessage(nestedUnderA(levels-2, `"x"`))}
	requireQuickCheck(t, "part does not fit",
		func() *RelicSchemaProblem { return schema.CheckPart("part X", RelicPart{ID: "X", Type: "step"}, bad) },
		`part X (step) field "a": at `+strings.Repeat("/a", levels-2)+`: the value must be an object (type)`)
}

func TestSchemaCheckPastItsBudgetIsTooCostly(t *testing.T) {
	schema := mustCompileSchema(t, "spec", `{"properties":{"$ref":"#/$defs/d1"},`+fanOutDefs("")+`}`)
	decoded, ok := decodeSchemaInstance([]byte(nestedUnderA(10, `{}`)))
	if !ok {
		t.Fatal("decode")
	}
	check := newSchemaCheck(10)
	fault := check.value(schema.properties, map[string]any{"a": decoded}, "property", nil)
	if got := problemString(propertyProblem(fault)); got != "properties: the schema is too costly to check against this value" {
		t.Errorf("past the budget: %s", got)
	}
	if got := problemString(propertyProblem(newSchemaCheck(MaxRelicSchemaCheckSteps).value(schema.properties, map[string]any{"a": decoded}, "property", nil))); got != "<nil>" {
		t.Errorf("within the budget: %s", got)
	}
}

func TestSchemaEnumAndConstCompareNumbersByValue(t *testing.T) {
	schema := mustCompileSchema(t, "spec", `{"properties":{"properties":{
	  "one":{"const":1},
	  "e":{"enum":[100,"a"]},
	  "obj":{"const":{"n":[1,{"m":2.5}],"s":"x"}},
	  "tenth":{"const":0.3},
	  "zero":{"const":0},
	  "huge":{"const":1e99999999999999}}}}`)
	cases := []struct {
		name, value string
		keyword     string
	}{
		{"one", `1`, ""},
		{"one", `1.0`, ""},
		{"one", `1e0`, ""},
		{"one", `10E-1`, ""},
		{"one", `2`, "const"},
		{"one", `"1"`, "const"},
		{"one", `true`, "const"},
		{"e", `1e2`, ""},
		{"e", `100.000`, ""},
		{"e", `"a"`, ""},
		{"e", `"100"`, "enum"},
		{"e", `101`, "enum"},
		{"obj", `{"s":"x","n":[1.0,{"m":25e-1}]}`, ""},
		{"obj", `{"s":"x","n":[{"m":2.5},1]}`, "const"},
		{"obj", `{"s":"x","n":[1,{"m":2.5}],"t":1}`, "const"},
		{"obj", `{"s":"x","n":[1,{"m":2.51}]}`, "const"},
		{"tenth", `0.30000000000000001`, "const"},
		{"tenth", `3e-1`, ""},
		{"zero", `-0`, ""},
		{"zero", `0.0e5`, ""},
		{"huge", `1e99999999999999`, ""},
		{"huge", `1e99999999999998`, "const"},
	}
	for _, c := range cases {
		problem := schema.CheckProperty(c.name, json.RawMessage(c.value))
		switch {
		case c.keyword == "" && problem != nil:
			t.Errorf("%s = %s: refused: %s", c.name, c.value, problem)
		case c.keyword != "" && (problem == nil || problem.Keyword != c.keyword):
			t.Errorf("%s = %s: %s, want a %s problem", c.name, c.value, problemString(problem), c.keyword)
		}
	}
	if got := problemString(schema.CheckProperty("e", json.RawMessage(`7`))); got != `property "e": the value must be one of 100, "a" (enum)` {
		t.Errorf("enum message = %s", got)
	}
	if got := problemString(schema.CheckProperty("one", json.RawMessage(`2`))); got != `property "one": the value must be 1 (const)` {
		t.Errorf("const message = %s", got)
	}
}
