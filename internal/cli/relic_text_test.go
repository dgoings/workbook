package cli

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dgoings/workbook/internal/core"
)

// jsonHalfPair is the JSON escape for the first half of a surrogate pair, with
// no second half: a backslash, a u and four hex digits.
const jsonHalfPair = "\\" + "ud83d"

// refusedRelicText runs a relic command that must be refused as validation
// with nothing written to standard output, and returns the message. The
// message must not repeat bytes that are not text.
func refusedRelicText(t *testing.T, repository string, args ...string) string {
	t.Helper()
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, args...)
	if !utf8.ValidString(message) || strings.ContainsRune(message, utf8.RuneError) {
		t.Fatalf("relic %s: message %q repeats bytes that are not text", strings.Join(args, " "), message)
	}
	return message
}

// A document written in Latin-1, or one that escapes half of a surrogate
// pair, was accepted with each bad sequence stored as U+FFFD: in the title, in
// values, and in a body that is "stored exactly as written". It is refused,
// and the base64 member carries the same bytes exactly.
func TestRelicCreateFromFileRefusesTextThatIsNotUTF8(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	latin1 := []byte("{\"format\":\"workbook.relic\",\"version\":1,\"title\":\"Caf\xe9\",\"type\":\"spec\"," +
		"\"properties\":{\"owner\":\"Ren\xe9e\"},\"parts\":[{\"type\":\"section\",\"fields\":{\"heading\":\"R\xe9sum\xe9\"}," +
		"\"body\":{\"format\":\"txt\",\"text\":\"na\xefve caf\xe9\\n\"}}]}")
	message := refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, latin1))
	if !strings.Contains(message, "not valid UTF-8") || !strings.Contains(message, "title") {
		t.Errorf("a Latin-1 document: message %q, want it to say the title is not valid UTF-8", message)
	}

	// The same document with only its body in Latin-1 is told where a body
	// that is not text goes.
	body := []byte("na\xefve caf\xe9\n")
	onlyBody := []byte("{\"format\":\"workbook.relic\",\"version\":1,\"title\":\"Cafe\",\"type\":\"spec\"," +
		"\"parts\":[{\"type\":\"section\",\"fields\":{},\"body\":{\"format\":\"txt\",\"text\":\"na\xefve caf\xe9\\n\"}}]}")
	message = refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, onlyBody))
	for _, want := range []string{"not valid UTF-8", "parts[0].body.text", `"base64" member`} {
		if !strings.Contains(message, want) {
			t.Errorf("a Latin-1 body: message %q does not hold %q", message, want)
		}
	}

	surrogate := []byte(`{"format":"workbook.relic","version":1,"title":"Surrogate","type":"spec","parts":[` +
		`{"type":"section","fields":{},"body":{"text":"half an emoji: ` + jsonHalfPair + `"}}]}`)
	message = refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, surrogate))
	for _, want := range []string{"surrogate pair", "parts[0].body.text", `"base64" member`} {
		if !strings.Contains(message, want) {
			t.Errorf("half a surrogate pair in a body: message %q does not hold %q", message, want)
		}
	}
	code, stdout, stderr := runWithInput(t, repository, bytes.NewReader(latin1), "relic", "create", "--from-file", "-")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "not valid UTF-8") || strings.Contains(stderr, "\xe9") {
		t.Errorf("a Latin-1 document on standard input = (%d, %q, %q), want exit 5 and none of its bytes", code, stdout, stderr)
	}
	if refs := refsUnder(t, repository, "refs/workbook/relics/"); refs != "" {
		t.Fatalf("a refused document wrote a relic:\n%s", refs)
	}

	// The bytes that are no text are a body all the same, as base64.
	asBase64 := []byte(`{"format":"workbook.relic","version":1,"title":"Cafe","type":"spec","parts":[` +
		`{"type":"section","fields":{},"body":{"format":"txt","base64":"` + base64.StdEncoding.EncodeToString(body) + `"}}]}`)
	created := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, asBase64))
	address := created.Relic.ID + "#" + created.Parts[0].ID
	code, stdout, stderr = run(t, repository, "relic", "show", address, "--body")
	if code != 0 || stderr != "" || stdout != string(body) {
		t.Fatalf("show --body of a base64 body = (%d, %q, %q), want exactly %q", code, stdout, stderr, body)
	}
	exported, err := core.DecodeRelicDocument(exportRelic(t, repository, created.Relic.ID))
	if err != nil {
		t.Fatalf("DecodeRelicDocument(export): %v", err)
	}
	if got := relicBodyBytes(t, *exported.Parts[0].Body); !bytes.Equal(got, body) || exported.Parts[0].Body.Bytes != int64(len(body)) {
		t.Fatalf("the exported body = %q (%d bytes), want %q", got, exported.Parts[0].Body.Bytes, body)
	}
}

// --set-json and --field-json stored a replacement character for a byte that
// is not UTF-8, and then answered "No change" when given the same bad byte
// again. Both are refused, as --set and --field refuse it.
func TestRelicValueOptionsRefuseTextThatIsNotUTF8(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Values", "--type", "spec")
	id := created.Relic.ID
	// What the bad byte used to be stored as, so a repeat of the old behavior
	// would report no change.
	added := mustUpdateRelic(t, repository, id, "--set", "owner=Ren\xef\xbf\xbde",
		"--add-part", "--part-type", "note", "--field", "who=Ren\xef\xbf\xbde")
	part, head := added.Parts[0].ID, added.Relic.Head

	for label, args := range map[string][]string{
		"--set-json":                     {"update", id, "--set-json", "owner=\"Ren\xe9e\""},
		"--set":                          {"update", id, "--set", "owner=Ren\xe9e"},
		"--field-json":                   {"update", id, "--part", part, "--field-json", "who=\"Ren\xe9e\""},
		"--field":                        {"update", id, "--part", part, "--field", "who=Ren\xe9e"},
		"--field-json on a new part":     {"update", id, "--add-part", "--part-type", "note", "--field-json", "who=[\"Ren\xe9e\"]"},
		"--set-json, nested in a member": {"update", id, "--set-json", "owner={\"name\":\"Ren\xe9e\"}"},
	} {
		message := refusedRelicText(t, repository, args...)
		if !strings.Contains(message, "valid UTF-8") {
			t.Errorf("%s with a Latin-1 byte: message %q, want it to say the value is not valid UTF-8", label, message)
		}
	}
	for label, args := range map[string][]string{
		"--set-json":   {"update", id, "--set-json", `owner="` + jsonHalfPair + `"`},
		"--field-json": {"update", id, "--part", part, "--field-json", `who={"a":["` + jsonHalfPair + `"]}`},
	} {
		message := refusedRelicText(t, repository, args...)
		if !strings.Contains(message, "surrogate pair") {
			t.Errorf("%s with half a surrogate pair: message %q, want it to say so", label, message)
		}
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused value moved the relic from %s to %s", head, now)
	}
}

// A document that named a member twice was read as a mixture: a second parts
// list decoded on top of the first, and member names matched without regard
// to letter case. Inside a value the last copy won. Each is refused.
func TestRelicRefusesAMemberNamedTwice(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	const duplicated = `{"format":"workbook.relic","version":1,"title":"Draft","type":"spec",
 "parts":[{"type":"decision","fields":{"chosen":"by hand","status":"accepted"},"body":{"format":"md","text":"Reasoning."}},
          {"type":"section","fields":{"heading":"Goals"}}],
 "Title":"Final",
 "parts":[{"type":"section","fields":{"heading":"Summary"}}]}`
	message := refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, []byte(duplicated)))
	if !strings.Contains(message, `"Title"`) || !strings.Contains(message, `written "title"`) {
		t.Errorf("a document with a Title member: message %q, want it to refuse the member and give its spelling", message)
	}
	twice := strings.Replace(duplicated, "\n \"Title\":\"Final\",", "", 1)
	message = refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, []byte(twice)))
	if !strings.Contains(message, `member "parts" twice`) {
		t.Errorf("a document with two parts members: message %q, want it to name the member", message)
	}

	for label, test := range map[string]struct{ old, new, member, where string }{
		"a property":         {`"weight":3`, `"weight":3,"weight":4`, "weight", "properties"},
		"a field":            {`"outcome":"accepted"`, `"outcome":"accepted","outcome":"rejected"`, "outcome", "parts[2].fields"},
		"inside a property":  {`"weight":3`, `"weight":{"a":1,"a":2}`, "a", "properties.weight"},
		"a part's body text": {`"text":"# Goals\n\nKeep it small.\n"`, `"text":"one","text":"two"`, "text", "parts[0].body"},
	} {
		document := bytes.Replace(relicTestDocument(t), []byte(test.old), []byte(test.new), 1)
		message := refusedRelicText(t, repository, "create", "--from-file", writeRelicDocumentFile(t, document))
		if !strings.Contains(message, `member "`+test.member+`" twice`) || !strings.Contains(message, test.where) {
			t.Errorf("a document naming %s twice: message %q, want member %q in %s", label, message, test.member, test.where)
		}
	}
	if refs := refsUnder(t, repository, "refs/workbook/relics/"); refs != "" {
		t.Fatalf("a refused document wrote a relic:\n%s", refs)
	}

	created := mustCreateRelic(t, repository, "Values", "--type", "spec")
	id := created.Relic.ID
	added := mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "note")
	part, head := added.Parts[0].ID, added.Relic.Head
	for label, args := range map[string][]string{
		"--set-json":   {"update", id, "--set-json", `dup={"a":1,"a":2}`},
		"--field-json": {"update", id, "--part", part, "--field-json", `dup=[{"a":1,"a":2}]`},
	} {
		message := refusedRelicText(t, repository, args...)
		if !strings.Contains(message, `member "a" twice`) || !strings.Contains(message, "dup") {
			t.Errorf("%s: message %q, want it to name the option's property and the repeated member", label, message)
		}
	}
	if now := relicHeadOf(t, repository, id); now != head {
		t.Fatalf("a refused value moved the relic from %s to %s", head, now)
	}
}
