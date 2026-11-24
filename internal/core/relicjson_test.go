package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// JSON escapes the tests below put into JSON text. Each is a backslash, a u
// and four hex digits: the two halves of the surrogate pair for U+1F600, and
// three characters that need no pair.
const (
	jsonFirstHalf   = "\\" + "ud83d"
	jsonSecondHalf  = "\\" + "ude00"
	jsonLetterA     = "\\" + "u0041"
	jsonEAcute      = "\\" + "u00e9"
	jsonReplacement = "\\" + "ufffd"
	// The same two halves with their hex digits in capitals.
	jsonFirstHalfCaps  = "\\" + "uD83D"
	jsonSecondHalfCaps = "\\" + "uDE00"
)

// requireRelicTextRefusal requires a validation error whose message holds
// every fragment and repeats nothing of the bad text: it is itself valid
// UTF-8 and holds no replacement character.
func requireRelicTextRefusal(t *testing.T, label string, err error, fragments ...string) {
	t.Helper()
	if got := CategoryOf(err); got != CategoryValidation {
		t.Errorf("%s: category = %q, want %q; error = %v", label, got, CategoryValidation, err)
		return
	}
	message := err.Error()
	for _, fragment := range fragments {
		if !strings.Contains(message, fragment) {
			t.Errorf("%s: error %q does not mention %q", label, message, fragment)
		}
	}
	if !utf8.ValidString(message) || strings.ContainsRune(message, utf8.RuneError) {
		t.Errorf("%s: message %q repeats bytes that are not text", label, message)
	}
	if len(message) > 400 {
		t.Errorf("%s: the message is %d bytes long: %q", label, len(message), message)
	}
}

func TestScanRelicJSONTextFindsHalfASurrogatePair(t *testing.T) {
	for text, want := range map[string]int{
		`"plain"`: -1,
		`"` + jsonFirstHalf + jsonSecondHalf + `"`:                  -1,
		`"` + jsonFirstHalfCaps + jsonSecondHalfCaps + `"`:          -1,
		`"` + jsonFirstHalfCaps + `"`:                               1,
		`"` + strings.ToUpper(jsonFirstHalf) + `"`:                  -1, // a capital U makes it no escape; the decoder's to refuse
		`"\` + jsonFirstHalf + `"`:                                  -1, // a backslash, then the letters
		`"` + jsonEAcute + `"`:                                      -1,
		`"` + jsonReplacement + `"`:                                 -1, // the replacement character, written on purpose
		`"` + jsonFirstHalf + `"`:                                   1,
		`"ab` + jsonSecondHalf + `"`:                                3,
		`"` + jsonFirstHalf + jsonLetterA + `"`:                     1,
		`"` + jsonFirstHalf + jsonFirstHalf + jsonSecondHalf + `"`:  1,
		`"` + jsonFirstHalf + jsonSecondHalf + jsonSecondHalf + `"`: 13,
		`{"` + jsonSecondHalf + `":1}`:                              2,
		`["ok","` + jsonFirstHalf + jsonSecondHalf + jsonFirstHalf:  19, // cut short; still half a pair
		`"` + jsonFirstHalf + `\u"`:                                 1,
		`"\u12"`:                                                    -1, // not an escape; the decoder's to refuse
	} {
		if got := scanRelicJSONText([]byte(text)).surrogate; got != want {
			t.Errorf("scanRelicJSONText(%s).surrogate = %d, want %d", text, got, want)
		}
	}
}

func TestCanonicalRelicValueRefusesTextThatIsNotUTF8(t *testing.T) {
	for label, raw := range map[string]string{
		"a Latin-1 byte":             "\"Ren\xe9e\"",
		"a Latin-1 byte in a name":   "{\"caf\xe9\":1}",
		"a cut-off sequence":         "\"\xe2\x82\"",
		"a byte outside any string":  "[1,\xff2]",
		"a nested Latin-1 byte":      "{\"a\":[\"na\xefve\"]}",
		"an overlong encoding":       "\"\xc0\xaf\"",
		"a surrogate written as raw": "\"\xed\xa0\xbd\"",
	} {
		_, err := CanonicalRelicValue([]byte(raw))
		requireRelicTextRefusal(t, label, err, "not valid UTF-8")
	}
	for label, raw := range map[string]string{
		"the first half alone":           `"` + jsonFirstHalf + `"`,
		"the second half alone":          `"` + jsonSecondHalf + `"`,
		"a first half and a letter":      `"` + jsonFirstHalf + jsonLetterA + `"`,
		"a first half at the end":        `"half an emoji: ` + jsonFirstHalf + `"`,
		"half a pair in a member name":   `{"` + jsonFirstHalf + `":1}`,
		"half a pair deep in the value":  `{"a":[{"b":"` + jsonSecondHalf + `"}]}`,
		"two first halves and a second":  `"` + jsonFirstHalf + jsonFirstHalf + jsonSecondHalf + `"`,
		"a pair and a second half alone": `"` + jsonFirstHalf + jsonSecondHalf + jsonSecondHalf + `"`,
	} {
		_, err := CanonicalRelicValue([]byte(raw))
		requireRelicTextRefusal(t, label, err, "surrogate pair")
	}
	for raw, want := range map[string]string{
		`"` + jsonFirstHalf + jsonSecondHalf + `"`: "\"\U0001F600\"",
		`"\` + jsonFirstHalf + `"`:                 `"\` + jsonFirstHalf + `"`,
		`"` + jsonReplacement + `"`:                "\"\xef\xbf\xbd\"",
		"\"\xef\xbf\xbd\"":                         "\"\xef\xbf\xbd\"",
		`"Ren` + jsonEAcute + `e"`:                 "\"Ren\xc3\xa9e\"",
	} {
		got, err := CanonicalRelicValue([]byte(raw))
		if err != nil || string(got) != want {
			t.Errorf("CanonicalRelicValue(%s) = %s, %v; want %s", raw, got, err, want)
		}
	}
}

func TestCanonicalRelicValueRefusesARepeatedMember(t *testing.T) {
	for raw, fragments := range map[string][]string{
		`{"a":1,"a":2}`:                    {`member "a" twice`},
		`{"a":1,"b":2,"a":1}`:              {`member "a" twice`},
		`{"A":1,"` + jsonLetterA + `":2}`:  {`member "A" twice`},
		`{"x":[{"b":1,"b":2}]}`:            {`member "b" twice`, `x[0]`},
		`[{"a":1},{"a":1,"a":1}]`:          {`member "a" twice`, `[1]`},
		`{"outer":{"in ner":{"":1,"":2}}}`: {`member "" twice`, `outer["in ner"]`},
		`{"` + strings.Repeat("n", 500) + `":1,"` + strings.Repeat("n", 500) + `":2}`: {"a member whose name is 500 bytes long"},
	} {
		_, err := CanonicalRelicValue([]byte(raw))
		requireRelicTextRefusal(t, raw, err, fragments...)
	}
	for raw, want := range map[string]string{
		`{"a":{"a":1}}`:               `{"a":{"a":1}}`,
		`[{"a":1},{"a":2}]`:           `[{"a":1},{"a":2}]`,
		`{"a":1,"A":2}`:               `{"A":2,"a":1}`,
		`{"a":[{"a":1}],"b":{"a":2}}`: `{"a":[{"a":1}],"b":{"a":2}}`,
		`["{\"a\":1,\"a\":2}"]`:       `["{\"a\":1,\"a\":2}"]`,
	} {
		got, err := CanonicalRelicValue([]byte(raw))
		if err != nil || string(got) != want {
			t.Errorf("CanonicalRelicValue(%s) = %s, %v; want %s", raw, got, err, want)
		}
	}

	// A member repeated far down is located without a message that grows with
	// the depth.
	deep := strings.Repeat(`{"level":`, MaxRelicValueDepth-1) + `{"a":1,"a":2}` + strings.Repeat("}", MaxRelicValueDepth-1)
	_, err := CanonicalRelicValue([]byte(deep))
	requireRelicTextRefusal(t, "a member repeated 32 levels down", err, `member "a" twice`)
}

// relicDocumentWith returns the test document with old replaced by new.
func relicDocumentWith(t *testing.T, old, new string) []byte {
	t.Helper()
	text := relicDocumentForTest()
	if !strings.Contains(text, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return []byte(strings.Replace(text, old, new, 1))
}

func TestDecodeRelicDocumentRefusesTextThatIsNotUTF8(t *testing.T) {
	const bodyAdvice = `"base64" member`
	for label, test := range map[string]struct {
		old, new  string
		fragments []string
	}{
		"a Latin-1 title":          {`"title":"Task archive compaction"`, "\"title\":\"Caf\xe9\"", []string{"not valid UTF-8", "title"}},
		"a Latin-1 type":           {`"type":"spec"`, "\"type\":\"sp\xe9c\"", []string{"not valid UTF-8", "type"}},
		"a Latin-1 property":       {`"status":"approved"`, "\"owner\":\"Ren\xe9e\"", []string{"not valid UTF-8", "properties.owner"}},
		"a Latin-1 property name":  {`"status":"approved"`, "\"caf\xe9\":1", []string{"not valid UTF-8", "member name", "properties"}},
		"a Latin-1 field":          {`"heading":"Goals"`, "\"heading\":\"R\xe9sum\xe9\"", []string{"not valid UTF-8", "parts[0].fields.heading"}},
		"a Latin-1 body":           {`"text":"# Goals\n"`, "\"text\":\"na\xefve caf\xe9\\n\"", []string{"not valid UTF-8", "parts[0].body.text", bodyAdvice}},
		"a Latin-1 format label":   {`"format":"md"`, "\"format\":\"m\xe9\"", []string{"not valid UTF-8", "parts[0].body.format"}},
		"half a pair in the title": {`"title":"Task archive compaction"`, `"title":"` + jsonFirstHalf + `"`, []string{"surrogate pair", "title"}},
		"half a pair in a body":    {`"text":"# Goals\n"`, `"text":"half an emoji: ` + jsonFirstHalf + `"`, []string{"surrogate pair", "parts[0].body.text", bodyAdvice}},
		"half a pair in a field":   {`"heading":"Goals"`, `"heading":["` + jsonSecondHalf + `"]`, []string{"surrogate pair", "parts[0].fields.heading[0]"}},
	} {
		_, err := DecodeRelicDocument(relicDocumentWith(t, test.old, test.new))
		requireRelicTextRefusal(t, label, err, test.fragments...)
		if err != nil && !strings.Contains(label, "body") && strings.Contains(err.Error(), "base64") {
			t.Errorf("%s: message %q sends text that is no body to base64", label, err.Error())
		}
	}

	// Text that is no JSON at all and not UTF-8 either is still refused for
	// its bytes.
	_, err := DecodeRelicDocument([]byte("\xff\xfe{"))
	requireRelicTextRefusal(t, "bytes before the document", err, "not valid UTF-8")

	// Both halves of a pair are one character, and are read.
	document, err := DecodeRelicDocument(relicDocumentWith(t, `"text":"# Goals\n"`, `"text":"`+jsonFirstHalf+jsonSecondHalf+`"`))
	if err != nil || document.Parts[0].Body.Text == nil || *document.Parts[0].Body.Text != "\U0001F600" {
		t.Errorf("a document with both halves of a pair = %+v, %v", document.Parts, err)
	}
}

func TestDecodeRelicDocumentRefusesARepeatedMember(t *testing.T) {
	for label, test := range map[string]struct {
		old, new  string
		fragments []string
	}{
		"the title twice":     {`"type":"spec"`, `"type":"spec","title":"Final"`, []string{`member "title" twice`}},
		"the parts twice":     {`"links":[]`, `"parts":[{"type":"section","fields":{}}],"links":[]`, []string{`member "parts" twice`}},
		"a property twice":    {`"status":"approved"`, `"status":"approved","status":"draft"`, []string{`member "status" twice`, "properties"}},
		"a part's type twice": {`"type":"blob"`, `"type":"blob","type":"note"`, []string{`member "type" twice`, "parts[1]"}},
		"a part's fields twice": {`{"type":"note","fields":{}}`, `{"type":"note","fields":{},"fields":{"a":1}}`,
			[]string{`member "fields" twice`, "parts[2]"}},
		"a field twice":       {`"heading":"Goals"`, `"heading":"Goals","heading":"Summary"`, []string{`member "heading" twice`, "parts[0].fields"}},
		"a body's text twice": {`"text":"# Goals\n"`, `"text":"# Goals\n","text":"other"`, []string{`member "text" twice`, "parts[0].body"}},
		"a body twice": {`"body":{"bytes":3,"base64":"//4A"}`, `"body":{"bytes":3,"base64":"//4A"},"body":{"text":"x"}`,
			[]string{`member "body" twice`, "parts[1]"}},
		"inside a property": {`"status":"approved"`, `"status":{"a":1,"a":2}`, []string{`member "a" twice`, "properties.status"}},
		"inside a field":    {`"heading":"Goals"`, `"heading":[{"k":1,"k":1}]`, []string{`member "k" twice`, "parts[0].fields.heading[0]"}},
		"inside the links":  {`"links":[]`, `"links":[{"to":1,"to":2}]`, []string{`member "to" twice`, "links[0]"}},
	} {
		_, err := DecodeRelicDocument(relicDocumentWith(t, test.old, test.new))
		requireRelicTextRefusal(t, label, err, test.fragments...)
	}
}

func TestDecodeRelicDocumentMatchesMemberNamesExactly(t *testing.T) {
	for label, test := range map[string]struct {
		old, new  string
		fragments []string
	}{
		"Title":          {`"title":`, `"Title":`, []string{`"Title"`, `"title"`}},
		"PARTS":          {`"parts":`, `"PARTS":`, []string{`"PARTS"`, `"parts"`}},
		"a part's Type":  {`"type":"blob"`, `"Type":"blob"`, []string{`"Type"`, `"type"`, "parts[1]"}},
		"a body's TEXT":  {`"text":"# Goals\n"`, `"TEXT":"# Goals\n"`, []string{`"TEXT"`, `"text"`, "parts[0].body"}},
		"an extra":       {`"links":[]`, `"links":[],"extra":1`, []string{`"extra"`}},
		"a part's extra": {`{"type":"note","fields":{}}`, `{"type":"note","fields":{},"extra":1}`, []string{`"extra"`, "parts[2]"}},
		"a body's extra": {`"bytes":3,`, `"bytes":3,"encoding":"x",`, []string{`"encoding"`, "parts[1].body"}},
		"a long name":    {`"links":[]`, `"links":[],"` + strings.Repeat("x", 300) + `":1`, []string{"300 bytes long"}},
	} {
		_, err := DecodeRelicDocument(relicDocumentWith(t, test.old, test.new))
		requireRelicTextRefusal(t, label, err, test.fragments...)
	}
	// Names inside properties, fields and values are the writer's own.
	document, err := DecodeRelicDocument(relicDocumentWith(t, `"status":"approved"`, `"Title":"x","title":{"Type":1,"type":2}`))
	if err != nil || len(document.Properties) != 2 {
		t.Errorf("properties named Title and title = %v, %v; want both read", document.Properties, err)
	}
}

func TestDecodeRelicDocumentNamesAResultEnvelope(t *testing.T) {
	envelope := `{"format":"workbook.result","version":1,"command":"relic export","data":` + relicDocumentForTest() + `}`
	_, err := DecodeRelicDocument([]byte(envelope))
	requireRelicTextRefusal(t, "a result envelope", err, "result", "relic export", "without --json", `"data"`)

	// One of the two members alone is only a member the document does not have.
	_, err = DecodeRelicDocument(relicDocumentWith(t, `"links":[]`, `"links":[],"command":"x"`))
	requireRelicTextRefusal(t, "a document with a command member", err, `"command"`)
	if err != nil && strings.Contains(err.Error(), "without --json") {
		t.Errorf("a document with only a command member is called a result: %q", err.Error())
	}
}
