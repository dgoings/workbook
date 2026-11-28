package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

var relicIDGrammar = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-R-[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// relicRepositoryFor returns a set-up project under objectFormat, with the key
// WB.
func relicRepositoryFor(t *testing.T, objectFormat string) string {
	t.Helper()
	if objectFormat == testrepo.FormatSHA1 {
		return initializedRepository(t)
	}
	testrepo.RequireObjectFormat(t, objectFormat)
	repository := testrepo.New(t, testrepo.WithObjectFormat(objectFormat))
	if code, _, stderr := run(t, repository, "setup", "--key", "WB"); code != 0 {
		t.Fatalf("setup code = %d; stderr = %q", code, stderr)
	}
	return repository
}

// mustRelic runs a relic command with --json, requires it to succeed quietly,
// and decodes its data into target.
func mustRelic(t *testing.T, repository string, target any, args ...string) {
	t.Helper()
	mustRelicWithInput(t, repository, "", target, args...)
}

func mustRelicWithInput(t *testing.T, repository, input string, target any, args ...string) {
	t.Helper()
	full := append([]string{"relic"}, append(args, "--json")...)
	code, stdout, stderr := runWithInput(t, repository, strings.NewReader(input), full...)
	if code != 0 || stderr != "" {
		t.Fatalf("%s = (%d, %q, %q)", strings.Join(full, " "), code, stdout, stderr)
	}
	result := assertJSONResult(t, stdout, "relic "+args[0])
	if target != nil {
		if err := json.Unmarshal(result.Data, target); err != nil {
			t.Fatalf("decode %s: %v; data = %s", strings.Join(full, " "), err, result.Data)
		}
	}
}

func mustCreateRelic(t *testing.T, repository string, args ...string) core.RelicMutationResult {
	t.Helper()
	var created core.RelicMutationResult
	mustRelic(t, repository, &created, append([]string{"create"}, args...)...)
	return created
}

// relicErrorMessage runs a relic command with --json, requires exit code
// want, and returns the error's message.
func relicErrorMessage(t *testing.T, repository string, want int, category core.Category, args ...string) string {
	t.Helper()
	full := append([]string{"relic"}, append(args, "--json")...)
	code, stdout, stderr := run(t, repository, full...)
	if code != want || stdout != "" {
		t.Fatalf("%s = (%d, %q, %q), want exit %d", strings.Join(full, " "), code, stdout, stderr, want)
	}
	assertJSONError(t, stderr, category, "")
	var document errorDocument
	if err := json.Unmarshal([]byte(stderr), &document); err != nil {
		t.Fatal(err)
	}
	return document.Error.Message
}

func TestRelicCreateShowListDeleteRestoreJSON(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			created := mustCreateRelic(t, repository, "Task archive compaction", "--type", "spec")
			id := created.Relic.ID
			if !relicIDGrammar.MatchString(id) || !strings.HasPrefix(id, "WB-R-") {
				t.Fatalf("created ID = %q, want a relic ID under WB", id)
			}
			if !created.Changed || len(created.Parts) != 0 {
				t.Fatalf("created = %+v, want a change with no parts", created)
			}
			ref := "refs/workbook/relics/" + id
			if head := gitOutput(t, repository, "rev-parse", "--verify", ref); head != created.Relic.Head {
				t.Fatalf("%s = %q, result head = %q", ref, head, created.Relic.Head)
			}

			var shown map[string]json.RawMessage
			mustRelic(t, repository, &shown, "show", id)
			author := gitOutput(t, repository, "config", "user.email")
			for member, want := range map[string]string{
				"title": `"Task archive compaction"`, "type": `"spec"`, "schema": "null", "retired": "false",
				"deleted": "false", "properties": "{}", "parts": "[]", "links": "[]",
				"author": mustJSON(t, author), "id": mustJSON(t, id),
			} {
				if got := string(shown[member]); got != want {
					t.Errorf("show %s = %s, want %s", member, got, want)
				}
			}

			var listed []core.RelicSummary
			mustRelic(t, repository, &listed, "list")
			if len(listed) != 1 || listed[0].ID != id || listed[0].Deleted {
				t.Fatalf("list = %+v, want the one live relic", listed)
			}

			var deleted core.RelicMutationResult
			mustRelic(t, repository, &deleted, "delete", id)
			if !deleted.Changed || !deleted.Relic.Deleted {
				t.Fatalf("delete = %+v", deleted)
			}
			mustRelic(t, repository, &listed, "list")
			if len(listed) != 0 {
				t.Fatalf("list after delete = %+v, want none", listed)
			}
			mustRelic(t, repository, &listed, "list", "--all")
			if len(listed) != 1 || !listed[0].Deleted {
				t.Fatalf("list --all after delete = %+v, want the deleted relic", listed)
			}
			if head := gitOutput(t, repository, "rev-parse", "--verify", ref); head != deleted.Relic.Head {
				t.Fatalf("%s after delete = %q, want %q", ref, head, deleted.Relic.Head)
			}

			var restored core.RelicMutationResult
			mustRelic(t, repository, &restored, "restore", id)
			if !restored.Changed || restored.Relic.Deleted {
				t.Fatalf("restore = %+v", restored)
			}
			mustRelic(t, repository, &listed, "list")
			if len(listed) != 1 || listed[0].ID != id || listed[0].Deleted {
				t.Fatalf("list after restore = %+v", listed)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// relicTestDocument is a whole relic with two properties and three parts: one
// with a markdown body, one with a body that is not UTF-8, one with none.
func relicTestDocument(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"format":"workbook.relic","version":1,"title":"Archive plan","type":"spec","schema":null,` +
		`"retired":false,"deleted":false,"properties":{"status":"approved","weight":3},"parts":[` +
		`{"type":"section","fields":{"heading":"Goals"},"body":{"format":"md","bytes":0,"text":"# Goals\n\nKeep it small.\n"}},` +
		`{"type":"figure","fields":{},"body":{"format":"bin","bytes":0,"base64":"` +
		base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0xfe}) + `"}},` +
		`{"type":"decision","fields":{"outcome":"accepted","votes":[1,2]}}` +
		`],"links":[]}`)
}

func writeRelicDocumentFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relic.json")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exportRelic(t *testing.T, repository, id string) []byte {
	t.Helper()
	code, stdout, stderr := run(t, repository, "relic", "export", id)
	if code != 0 || stderr != "" {
		t.Fatalf("relic export = (%d, %q, %q)", code, stdout, stderr)
	}
	return []byte(stdout)
}

func TestRelicCreateFromFileAndExportRoundTrip(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	path := writeRelicDocumentFile(t, relicTestDocument(t))
	first := mustCreateRelic(t, repository, "--from-file", path)
	if len(first.Parts) != 3 {
		t.Fatalf("created parts = %+v, want three", first.Parts)
	}

	exported := exportRelic(t, repository, first.Relic.ID)
	if !bytes.HasPrefix(exported, []byte("{\n  \"format\": \"workbook.relic\"")) || !bytes.HasSuffix(exported, []byte("}\n")) {
		t.Fatalf("export = %q, want the bare document indented by two spaces", exported)
	}
	document, err := core.DecodeRelicDocument(exported)
	if err != nil {
		t.Fatalf("DecodeRelicDocument(export): %v", err)
	}

	second := mustCreateRelicWithInput(t, repository, string(exported), "--from-file", "-")
	again, err := core.DecodeRelicDocument(exportRelic(t, repository, second.Relic.ID))
	if err != nil {
		t.Fatalf("DecodeRelicDocument(second export): %v", err)
	}
	assertSameRelicContent(t, document, again)
	if document.Title != "Archive plan" || document.Type != "spec" || len(document.Properties) != 2 {
		t.Fatalf("export = %+v, want the file's title, type and properties", document)
	}
	if body := document.Parts[1].Body; body == nil || body.Base64 == nil ||
		*body.Base64 != base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0xfe}) {
		t.Fatalf("exported binary body = %+v", body)
	}

	other := mustCreateRelic(t, repository, "Other", "--type", "plan", "--from-file", path)
	if other.Relic.Title != "Other" || other.Relic.Type != "plan" || len(other.Parts) != 3 {
		t.Fatalf("create with a title and --type over a file = %+v", other.Relic)
	}

	// The export with --json is the same document inside the envelope.
	var enveloped core.RelicDocument
	mustRelic(t, repository, &enveloped, "export", first.Relic.ID)
	assertSameRelicContent(t, document, enveloped)
	if enveloped.Format != core.RelicDocumentFormat || enveloped.Version != 1 {
		t.Fatalf("export --json format = %q version %d", enveloped.Format, enveloped.Version)
	}
}

// relic export writes '<', '>' and '&' in strings as themselves, not as the
// six-byte escapes json.Marshal uses, so a Markdown or HTML body reads as it
// was written and is no larger than its text. A stored property or field
// value is written as it is stored, in its canonical form. The document still
// reads back as the same relic.
func TestRelicExportWritesHTMLCharactersAsThemselves(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	body := "# <Title> & more\n\n<details>a -> b && c</details>\n"
	id := mustCreateRelic(t, repository, "Tags <and> & ampersands", "--type", "spec").Relic.ID
	mustUpdateRelic(t, repository, id, "--set", "owner=Alice <alice@example.com>",
		"--add-part", "--part-type", "note", "--body-file", writeRelicBodyFile(t, "body.md", body))

	exported := exportRelic(t, repository, id)
	for _, want := range []string{`"title": "Tags <and> & ampersands"`, `"text": "# <Title> & more\n\n<details>a -> b && c</details>\n"`,
		`"owner": "Alice \u003calice@example.com\u003e"`} {
		if !bytes.Contains(exported, []byte(want)) {
			t.Fatalf("export = %s, want it to hold %s", exported, want)
		}
	}
	if count := bytes.Count(exported, []byte(`\u003c`)); count != 1 {
		t.Fatalf("export holds %d escapes of '<', want only the stored property's: %s", count, exported)
	}

	document, err := core.DecodeRelicDocument(exported)
	if err != nil {
		t.Fatalf("DecodeRelicDocument(export): %v", err)
	}
	second := mustCreateRelicWithInput(t, repository, string(exported), "--from-file", "-")
	again := exportRelic(t, repository, second.Relic.ID)
	copied, err := core.DecodeRelicDocument(again)
	if err != nil {
		t.Fatalf("DecodeRelicDocument(second export): %v", err)
	}
	assertSameRelicContent(t, document, copied)
	if *copied.Parts[0].Body.Text != body {
		t.Fatalf("the copied body = %q, want %q", *copied.Parts[0].Body.Text, body)
	}
}

func mustCreateRelicWithInput(t *testing.T, repository, input string, args ...string) core.RelicMutationResult {
	t.Helper()
	var created core.RelicMutationResult
	mustRelicWithInput(t, repository, input, &created, append([]string{"create"}, args...)...)
	return created
}

func assertSameRelicContent(t *testing.T, want, got core.RelicDocument) {
	t.Helper()
	if got.Title != want.Title || got.Type != want.Type || mustJSON(t, got.Properties) != mustJSON(t, want.Properties) {
		t.Fatalf("relic = %q %q %s, want %q %q %s",
			got.Title, got.Type, mustJSON(t, got.Properties), want.Title, want.Type, mustJSON(t, want.Properties))
	}
	if len(got.Parts) != len(want.Parts) {
		t.Fatalf("parts = %d, want %d", len(got.Parts), len(want.Parts))
	}
	for index := range want.Parts {
		wantPart, gotPart := want.Parts[index], got.Parts[index]
		if gotPart.Type != wantPart.Type || mustJSON(t, gotPart.Fields) != mustJSON(t, wantPart.Fields) {
			t.Errorf("part %d = %s %s, want %s %s", index, gotPart.Type, mustJSON(t, gotPart.Fields),
				wantPart.Type, mustJSON(t, wantPart.Fields))
		}
		if (gotPart.Body == nil) != (wantPart.Body == nil) {
			t.Fatalf("part %d body = %+v, want %+v", index, gotPart.Body, wantPart.Body)
		}
		if wantPart.Body == nil {
			continue
		}
		if gotPart.Body.Format != wantPart.Body.Format || !bytes.Equal(relicBodyBytes(t, *gotPart.Body), relicBodyBytes(t, *wantPart.Body)) {
			t.Errorf("part %d body = %+v, want %+v", index, gotPart.Body, wantPart.Body)
		}
	}
}

func relicBodyBytes(t *testing.T, body core.RelicDocumentBody) []byte {
	t.Helper()
	switch {
	case body.Text != nil:
		return []byte(*body.Text)
	case body.Base64 != nil:
		content, err := base64.StdEncoding.DecodeString(*body.Base64)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	t.Fatalf("body %+v carries no content", body)
	return nil
}

func TestRelicShowPartAndBody(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, relicTestDocument(t)))
	id, goals, decision := created.Relic.ID, created.Parts[0].ID, created.Parts[2].ID

	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", id+"#"+strings.ToLower(goals[:len(goals)-2]))
	if len(shown.Parts) != 1 || shown.Parts[0].ID != goals {
		t.Fatalf("show <relic>#<part prefix> parts = %+v, want the one part", shown.Parts)
	}
	if body := shown.Parts[0].Body; body == nil || body.Version != goals || body.Text != nil || body.Base64 != nil {
		t.Fatalf("show part body = %+v, want its version and no text", body)
	}
	mustRelic(t, repository, &shown, "show", id+"#"+goals, "--body")
	if body := shown.Parts[0].Body; body == nil || body.Text == nil || *body.Text != "# Goals\n\nKeep it small.\n" {
		t.Fatalf("show part --body = %+v, want its text", body)
	}

	code, stdout, stderr := run(t, repository, "relic", "show", id+"#"+goals, "--body")
	if code != 0 || stderr != "" || stdout != "# Goals\n\nKeep it small.\n" {
		t.Fatalf("show part --body text = (%d, %q, %q), want exactly the body", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "relic", "show", id+"#"+created.Parts[1].ID, "--body")
	if code != 0 || stderr != "" || stdout != string([]byte{0xff, 0x00, 0xfe}) {
		t.Fatalf("show binary part --body text = (%d, %q, %q), want exactly the body", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "relic", "show", id+"#"+decision, "--body")
	if code != 4 || stdout != "" || !strings.Contains(stderr, "has no body") {
		t.Fatalf("show bodiless part --body = (%d, %q, %q), want exit 4", code, stdout, stderr)
	}
}

func TestRelicShowRefusesATaskID(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "A task", "--json")
	if code != 0 {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	mustCreateRelic(t, repository, "A relic", "--type", "spec")
	before := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+task.ID)

	for _, command := range []string{"show", "diff", "delete", "restore", "export"} {
		message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, command, task.ID)
		if !strings.Contains(message, "no relic matches") {
			t.Errorf("relic %s <task> message = %q, want no relic matches", command, message)
		}
	}
	if after := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+task.ID); after != before {
		t.Fatalf("task head moved from %s to %s", before, after)
	}
}

func TestRelicCommandsRefuseBadInvocations(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{},
		{"nope"},
		{"create"},
		{"create", "T"},
		{"create", "--type", "spec"},
		{"show"},
		{"diff"},
		{"delete"},
		{"restore"},
		{"export"},
	} {
		relicErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	message := relicErrorMessage(t, repository, 2, core.CategoryInvocation)
	for _, subcommand := range []string{"create", "list", "show", "diff", "update", "delete", "restore", "export"} {
		if !strings.Contains(message, subcommand) {
			t.Errorf("relic with no subcommand message = %q, want it to name %s", message, subcommand)
		}
	}
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation, "create", "T", "--type", "Spec")
	if !strings.Contains(message, "must be lowercase letters and digits, with single hyphens between words") {
		t.Fatalf("relic create --type Spec message = %q, want the grammar", message)
	}
	if got := refsUnder(t, repository, "refs/workbook/relics/"); got != "" {
		t.Fatalf("refused invocations wrote relics:\n%s", got)
	}
}

func TestRelicPrefixResolution(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	first := mustCreateRelic(t, repository, "First", "--type", "spec")
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", strings.ToLower(first.Relic.ID[:len(first.Relic.ID)-3]))
	if shown.ID != first.Relic.ID {
		t.Fatalf("show <lower-case prefix> = %q, want %q", shown.ID, first.Relic.ID)
	}
	mustCreateRelic(t, repository, "Second", "--type", "spec")
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "show", "WB-R-")
	if !strings.Contains(message, `relic prefix "WB-R-" matches 2 relics`) || !strings.Contains(message, "use more characters") {
		t.Fatalf("show WB-R- message = %q, want it to count the matches and say to use more characters", message)
	}
}

func TestRelicHelp(t *testing.T) {
	t.Parallel()
	output := assertHelpOutput(t, []string{"help", "relic"}, "Usage: workbook relic <command> [options]")
	for _, subcommand := range []string{"create", "list", "show", "diff", "update", "delete", "restore", "export"} {
		if !strings.Contains(output, "\n  "+subcommand+" ") {
			t.Errorf("relic help = %q, want subcommand %s", output, subcommand)
		}
	}
	assertHelpOutput(t, []string{"relic", "show", "--help"},
		"Usage: workbook relic show <relic>[#<part>] [--body] [--removed] [--history [--limit <n> | --all]] [--json]")
	assertHelpOutput(t, []string{"relic", "diff", "--help"},
		"Usage: workbook relic diff <relic>[#<part>] [<version> [<version>]] [--context <n>|all] [--json]")
	global := assertHelpOutput(t, []string{"help"}, "relic")
	// The rendered help pads each command name into a column; the usage text
	// printed beside an unknown command does not.
	if !regexp.MustCompile(`\n  relic +<command> \[options\]\n`).MatchString(global) {
		t.Fatalf("global help = %q, want the relic line", global)
	}
	if !strings.Contains(usage, "  relic <command> [options]\n") {
		t.Fatalf("usage = %q, want the relic line", usage)
	}
}

func TestRelicTextOutputIsTerminalSafe(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	hostile := "red \x1b[31malert\nsecond line"
	document := []byte(`{"format":"workbook.relic","version":1,"title":` + mustJSON(t, hostile) +
		`,"type":"spec","schema":null,"retired":false,"deleted":false,"properties":{"note":` + mustJSON(t, hostile) +
		`},"parts":[{"type":"section","fields":{"heading":` + mustJSON(t, hostile) +
		`},"body":{"format":"md","bytes":0,"text":` + mustJSON(t, hostile+"\n") + `}}],"links":[]}`)
	created := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, document))

	for _, args := range [][]string{
		{"relic", "list"},
		{"relic", "show", created.Relic.ID},
		{"relic", "show", created.Relic.ID, "--body"},
		{"relic", "create", hostile, "--type", "spec"},
	} {
		code, stdout, stderr := run(t, repository, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s = (%d, %q, %q)", strings.Join(args, " "), code, stdout, stderr)
		}
		if strings.ContainsRune(stdout, '\x1b') {
			t.Errorf("%s printed an escape byte: %q", strings.Join(args, " "), stdout)
		}
		if strings.Contains(stdout, "alert\nsecond") {
			t.Errorf("%s printed a stored newline inside one value: %q", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stdout, "red [31malert second line") {
			t.Errorf("%s = %q, want the title on one line", strings.Join(args, " "), stdout)
		}
	}
}

func TestRelicIsAuthoredByTheActingUser(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	const actor = "alice@example.test"
	code, stdout, stderr := runAs(t, repository, actor, "relic", "create", "Authored", "--type", "spec", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("relic create as %s = (%d, %q, %q)", actor, code, stdout, stderr)
	}
	var created core.RelicMutationResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "relic create").Data, &created); err != nil {
		t.Fatal(err)
	}
	if created.Relic.Author != actor {
		t.Fatalf("author = %q, want %q", created.Relic.Author, actor)
	}
	var pack core.RelicOperationPack
	packJSON := gitOutput(t, repository, "cat-file", "blob", created.Relic.Head+":operation.json")
	if err := json.Unmarshal([]byte(packJSON), &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Actor.ID != actor {
		t.Fatalf("create pack actor = %q, want %q", pack.Actor.ID, actor)
	}

	// A change by someone else is recorded under their name and leaves the
	// relic's author as it was.
	const editor = "bob@example.test"
	code, stdout, stderr = runAs(t, repository, editor, "relic", "update", created.Relic.ID, "--title", "Edited", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("relic update as %s = (%d, %q, %q)", editor, code, stdout, stderr)
	}
	var updated core.RelicMutationResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "relic update").Data, &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Changed || updated.Relic.Author != actor {
		t.Fatalf("relic after an update as %s = %+v, want a change that leaves the author %q", editor, updated.Relic, actor)
	}
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", created.Relic.ID)
	if shown.Author != actor || shown.Title != "Edited" {
		t.Fatalf("show after an update as %s = author %q title %q, want author %q", editor, shown.Author, shown.Title, actor)
	}
	packJSON = gitOutput(t, repository, "cat-file", "blob", updated.Relic.Head+":operation.json")
	pack = core.RelicOperationPack{}
	if err := json.Unmarshal([]byte(packJSON), &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Actor.ID != editor {
		t.Fatalf("update pack actor = %q, want %q", pack.Actor.ID, editor)
	}
}

// relic list --all marks a deleted relic with a last column of its own, as
// relic show --removed marks a removed part. A live relic's line is the same
// four columns with or without --all.
func TestRelicListMarksDeletedRelics(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	live := mustCreateRelic(t, repository, "Kept", "--type", "spec").Relic.ID
	deleted := mustCreateRelic(t, repository, "Dropped", "--type", "plan").Relic.ID
	mustRelic(t, repository, nil, "delete", deleted)

	liveLine := live + "\tspec\t0 parts\tKept\n"
	code, stdout, stderr := run(t, repository, "relic", "list")
	if code != 0 || stderr != "" || stdout != liveLine {
		t.Fatalf("relic list = (%d, %q, %q), want only %q", code, stdout, stderr, liveLine)
	}
	// Relics list in ID order, and two made in the same millisecond may sort
	// either way.
	want := liveLine + deleted + "\tplan\t0 parts\tDropped\tdeleted\n"
	if deleted < live {
		want = deleted + "\tplan\t0 parts\tDropped\tdeleted\n" + liveLine
	}
	code, stdout, stderr = run(t, repository, "relic", "list", "--all")
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("relic list --all = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
}

// docSection returns one section of a document, with whitespace collapsed:
// from the line that is exactly heading to the next heading of the same or a
// higher level, or to the end. It is empty when the document has no such
// heading.
func docSection(document, heading string) string {
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	var section []string
	inside, fenced := false, false
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "#") {
			hashes := len(line) - len(strings.TrimLeft(line, "#"))
			switch {
			case line == heading:
				inside = true
			case inside && hashes <= level:
				return strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
			}
		}
		if inside {
			section = append(section, line)
		}
	}
	return strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
}

// Relics are a stored format nobody can discover from a command's output: the
// ref, the address form, the base rule and the limits have to be written down,
// and so do how relics are shared and what a relic conflict leaves behind.
// Each document is held to its relic section, because the task sections
// already say "500" and "1 MiB".
func TestReferenceDocumentsSharedRelics(t *testing.T) {
	t.Parallel()
	reference := docSection(repositoryDoc(t, "docs", "reference.md"), "## Relics")
	if reference == "" {
		t.Fatal("docs/reference.md has no \"## Relics\" section")
	}
	for _, required := range []string{
		"refs/workbook/relics/",
		"<relic-id>#<part-id>",
		"--base",
		"stale-write",
		"workbook.relic",
		// How relics are shared, and what a conflict reports.
		"--no-sync", "relicConflict", "relic-over-limit", "git cat-file blob", "body", "tombstone",
		// The other local changes a conflict did not apply, and how to see each.
		"`unapplied`", ":operation.json",
		// That a total binds before the limit on one value.
		"a value of 65,536 bytes is already over this total",
		"a value of 16,384 bytes is already over this total",
		// The limits, as the table writes them.
		"500", "100", "16", "256", "64 KiB", "16 KiB", "1 MiB", "2,000", "4,000", "16 MiB",
		// What relic update's help says about where a relic option may be typed.
		"always act on the relic",
		// What relic list's help says about the order.
		"Relics are listed in ID order",
		// The warning for a machine that still has an older Workbook on it.
		"Upgrade every Workbook on the machine before creating relics.",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}
	if strings.Contains(reference, "not shared") {
		t.Error("the reference's relic section still says relics are not shared")
	}
	// Every limit that is a size is in the table by its exact number of bytes,
	// taken from the constant the commands enforce, and each row says what it
	// limits.
	for _, limit := range []struct {
		row   string
		value int
		unit  string
	}{
		{"| Title |", core.MaxRelicTitleBytes, " bytes"},
		{"| Relic type, part type |", core.MaxRelicTypeBytes, " bytes"},
		{"| Relic type, part type |", core.MaxRelicPartTypeBytes, " bytes"},
		{"| Property name, field name |", core.MaxRelicNameBytes, " bytes"},
		{"| Body format label |", core.MaxRelicBodyFormatBytes, " bytes"},
		{"| Part position |", core.MaxRelicPartPositionBytes, " bytes"},
		{"| One property value |", core.MaxRelicPropertyValueBytes, " bytes"},
		{"| One field value |", core.MaxRelicFieldValueBytes, " bytes"},
		{"| Nesting inside one value |", core.MaxRelicValueDepth, " levels"},
		{"| Body |", core.MaxRelicBodyBytes, " bytes"},
		{"| Properties together |", core.MaxRelicPropertiesBytes, " bytes"},
		{"| Fields of one part together |", core.MaxRelicPartFieldsBytes, " bytes"},
		{"| Live parts |", core.MaxRelicLiveParts, ""},
		{"| Parts |", core.MaxRelicParts, ""},
		{"| Live bodies |", core.MaxRelicLiveBodyBytes, " bytes"},
	} {
		want := limit.row + " " + groupedDigits(limit.value) + limit.unit
		if !strings.Contains(reference, want) {
			t.Errorf("the reference's limit table has no row beginning %q", want)
		}
	}
	// The size --from-file reads is a limit on one command's input.
	for _, required := range []string{groupedDigits(maxRelicDocumentBytes) + " bytes (64 MiB)", "32 levels"} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}

	architecture := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Relic refs")
	if architecture == "" {
		t.Fatal("docs/architecture.md has no \"### Relic refs\" section")
	}
	// The one member order an operation is stored in is the order of the
	// struct that is marshaled, so the document is held to the struct.
	var members []string
	operation := reflect.TypeOf(core.RelicOperation{})
	for index := 0; index < operation.NumField(); index++ {
		name, _, _ := strings.Cut(operation.Field(index).Tag.Get("json"), ",")
		members = append(members, "`"+name+"`")
	}
	architecture = strings.Join(strings.Fields(architecture), " ")
	for _, required := range []string{
		"refs/workbook/relics/", "links.json", "parts/",
		"always written in one order, " + strings.Join(members, ", ") + ",",
		// Where another clone's relics are kept, where a conflict parks a tip,
		// and the refspec that fills the first.
		"refs/workbook/remotes/", "refs/workbook/reconciled-relics/",
		"+refs/workbook/relics/*:refs/workbook/remotes/<remote>/relics/*",
	} {
		if !strings.Contains(architecture, required) {
			t.Errorf("the architecture's relic section is missing %q", required)
		}
	}
	if versions := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Mixed Workbook versions"); !strings.Contains(versions, "relic") {
		t.Error("the architecture's \"### Mixed Workbook versions\" section does not say what an older Workbook does with relics")
	}

	// The relic index: rebuild counts relics, and the architecture says what
	// the index holds and that a body's bytes stay in Git.
	if strings.Contains(repositoryDoc(t, "docs", "reference.md"), "`workbook rebuild` reads none") {
		t.Error("the reference still says workbook rebuild reads no relic")
	}
	if !strings.Contains(reference, "relicCount") || !strings.Contains(reference, "Rebuilt") {
		t.Error("the reference's relic section does not show what workbook rebuild reports about relics")
	}
	projection := docSection(repositoryDoc(t, "docs", "architecture.md"), "## SQLite projection")
	for _, required := range []string{"relic_parts", "body_object", "never copied"} {
		if !strings.Contains(projection, required) {
			t.Errorf("the architecture's SQLite projection section is missing %q", required)
		}
	}

	// Links to tasks: the options, the members a reader meets, and the two
	// operations and the table that hold them.
	if strings.Contains(reference, "Always an empty list") {
		t.Error("the reference still says a relic's links are always an empty list")
	}
	for _, required := range []string{"--link", "--unlink", "--from", "--rel", "--linked-to", "targetRemoved", "linkedBy", "`relics`"} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}
	whole := strings.Join(strings.Fields(repositoryDoc(t, "docs", "architecture.md")), " ")
	for _, required := range []string{"link.add", "link.remove", "relic_links"} {
		if !strings.Contains(whole, required) {
			t.Errorf("the architecture is missing %q", required)
		}
	}

	// Relics for agents: a long document belongs in a relic, the guidelines
	// teach agents to use them, and the README and the reference say so.
	fullReference := strings.Join(strings.Fields(repositoryDoc(t, "docs", "reference.md")), " ")
	if strings.Contains(fullReference, "belongs in the repository with the task linking to it") {
		t.Error("the reference still says a document belongs in the repository with the task linking to it")
	}
	if !strings.Contains(fullReference, "belongs in a relic") {
		t.Error("the reference does not say a document belongs in a relic")
	}
	if !strings.Contains(reference, "### Relics for agents") {
		t.Error("the reference's relic section has no \"### Relics for agents\" subsection")
	}
	forAgents := docSection(repositoryDoc(t, "docs", "reference.md"), "### Relics for agents")
	for _, required := range []string{".workbook/guidelines.md", "workbook docs update", "--base", "relic list --linked-to"} {
		if !strings.Contains(forAgents, required) {
			t.Errorf("the reference's \"### Relics for agents\" is missing %q", required)
		}
	}
	readme := strings.Join(strings.Fields(repositoryDoc(t, "README.md")), " ")
	for _, required := range []string{"`workbook relic show", "`.workbook/guidelines.md`"} {
		if !strings.Contains(readme, required) {
			t.Errorf("the README's relic text is missing %q", required)
		}
	}
	for _, required := range []string{"git ls-tree -r -t", "parts/<part-id>/body", "040000 tree"} {
		if !strings.Contains(architecture, required) {
			t.Errorf("the architecture's relic section is missing the commit-and-tree example's %q", required)
		}
	}
}

// The relic benchmarks decided that bodies are not cached and that part
// positions are never renumbered, and confirmed the limits. Those outcomes
// are behaviour a user relies on, so the reference states them, and the
// architecture keeps the measurement they were decided on.
func TestDocsRecordTheRelicBenchmarkDecisions(t *testing.T) {
	t.Parallel()
	reference := docSection(repositoryDoc(t, "docs", "reference.md"), "## Relics")
	for _, required := range []string{
		"scripts/benchmark.sh --relics",
		"Bodies are not cached",
		"Workbook does not renumber part positions.",
		"place it somewhere else, or move a neighboring part first",
		"confirmed as starting limits",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}
	benchmarks := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Relic benchmarks")
	if benchmarks == "" {
		t.Fatal("docs/architecture.md has no \"### Relic benchmarks\" section")
	}
	for _, required := range []string{
		"scripts/benchmark.sh --relics",
		"--relic-scale smoke",
		"`relic-first-read-fresh-clone`",
		"1,389",
		"git gc",
	} {
		if !strings.Contains(benchmarks, required) {
			t.Errorf("the architecture's relic benchmark section is missing %q", required)
		}
	}
	if projection := docSection(repositoryDoc(t, "docs", "architecture.md"), "## SQLite projection"); strings.Contains(projection, "not measured yet") {
		t.Error("the architecture still says the first relic pass on a fresh clone is not measured yet")
	}
	performance := docSection(repositoryDoc(t, "README.md"), "## Performance")
	if !strings.Contains(performance, "scripts/benchmark.sh --relics") {
		t.Error("the README's performance section does not mention scripts/benchmark.sh --relics")
	}
}

// History and diffs read what is already stored, so the reference states how a
// version is named and what the JSON members mean, and the architecture states
// where the history comes from and what is measured.
func TestDocsDocumentRelicHistoryAndDiffs(t *testing.T) {
	t.Parallel()
	reference := docSection(repositoryDoc(t, "docs", "reference.md"), "## Relics")
	for _, required := range []string{
		"### History and diffs", "relic diff", "--history", "--context", "bodyVersions", "applied",
		"approximate", "noNewline", "fromStart",
		"the operation's members", "not a Git commit", "1,000,000", "2,000 inserted plus deleted lines",
		`\ No newline at end of file`,
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}
	if strings.Contains(reference, "no history or diff commands") {
		t.Error("the reference still says there are no history or diff commands")
	}
	architecture := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Relic refs")
	for _, required := range []string{"relic_operations", "relic diff", "point"} {
		if !strings.Contains(architecture, required) {
			t.Errorf("the architecture's relic section is missing %q", required)
		}
	}
	projection := docSection(repositoryDoc(t, "docs", "architecture.md"), "## SQLite projection")
	for _, required := range []string{"history from `relic_operations`", "from Git otherwise"} {
		if !strings.Contains(projection, required) {
			t.Errorf("the architecture's SQLite projection section is missing %q", required)
		}
	}
	benchmarks := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Relic benchmarks")
	for _, required := range []string{
		"`relic-history-100`", "`relic-diff-latest-100kib`", "`relic-diff-first-last-100kib`", "not yet measured",
	} {
		if !strings.Contains(benchmarks, required) {
			t.Errorf("the architecture's relic benchmark section is missing %q", required)
		}
	}
	if readme := strings.Join(strings.Fields(repositoryDoc(t, "README.md")), " "); !strings.Contains(readme, "`workbook relic diff") {
		t.Error("the README's relic text does not name workbook relic diff")
	}
}

// Links to relics and parts add a stored form, members a reader meets in JSON
// and a promise about what a broken target does, so the reference states each,
// the architecture states how they are stored and indexed, and no document says
// a relic links only to tasks.
func TestDocsDocumentRelicLinks(t *testing.T) {
	t.Parallel()
	reference := repositoryDoc(t, "docs", "reference.md")
	links := docSection(reference, "### Links")
	if links == "" {
		t.Fatal("docs/reference.md has no \"### Links\" section")
	}
	for _, required := range []string{
		"--link <task|relic[#part]>", "--pointer <json object>", "--linked-to <task|relic[#part]>",
		"`seen`", "`pointer`", "`targetState`", "`targetRemoved`",
		"`deleted`", "`removed`", "`missing`", "`retired`", "(removed on <date>)",
		"`linkedFrom`", "`toPart`", "`fromType`", "`linkedBy`", "512 bytes",
		"workbook relic diff <target> <seen>", "(not in this clone)",
		"a link cannot point at what it starts from", "is in another project",
		"[When a relic is checked](#when-a-relic-is-checked)",
	} {
		if !strings.Contains(links, required) {
			t.Errorf("the reference's \"### Links\" is missing %q", required)
		}
	}
	checked := docSection(reference, "#### When a relic is checked")
	if !strings.Contains(checked, "[Links](#links)") {
		t.Error("the reference's \"#### When a relic is checked\" does not point to the links section")
	}

	architecture := repositoryDoc(t, "docs", "architecture.md")
	refs := docSection(architecture, "### Relic refs")
	for _, required := range []string{"`to.relic`", "`to.part`", "`seen`", "`pointer`", "nothing is written on the target"} {
		if !strings.Contains(refs, required) {
			t.Errorf("the architecture's relic section is missing %q", required)
		}
	}
	projection := docSection(architecture, "## SQLite projection")
	for _, required := range []string{"`seen`", "`pointer`", "schema version 13", "one query"} {
		if !strings.Contains(projection, required) {
			t.Errorf("the architecture's SQLite projection section is missing %q", required)
		}
	}
	benchmarks := docSection(architecture, "### Relic benchmarks")
	for _, scenario := range []string{"relic-linked-to-part-hot", "relic-show-linked-part-hot"} {
		if row := "| `" + scenario + "` | not yet measured | 200 ms |"; !strings.Contains(benchmarks, row) {
			t.Errorf("the architecture's relic benchmark table is missing the row %q", row)
		}
	}

	readme := repositoryDoc(t, "README.md")
	changelog := repositoryDoc(t, "CHANGELOG.md")
	for name, document := range map[string]string{"README.md": readme, "CHANGELOG.md": changelog} {
		if !strings.Contains(strings.Join(strings.Fields(document), " "), "--link <task|relic[#part]>") {
			t.Errorf("%s does not show a link to a relic or part", name)
		}
	}
	for name, document := range map[string]string{
		"docs/reference.md": reference, "docs/architecture.md": architecture, "README.md": readme, "CHANGELOG.md": changelog,
	} {
		collapsed := strings.Join(strings.Fields(document), " ")
		for _, stale := range []string{
			"links only to tasks", "#links-to-tasks", "a relic ID are refused as targets",
			"relic links only to tasks", "schema version 11",
		} {
			if strings.Contains(collapsed, stale) {
				t.Errorf("%s still says %q", name, stale)
			}
		}
	}
}

// Retiring and compacting relics add commands, flags, stored members, warning
// codes and refusals a reader meets, so the documents are held to the names
// the code uses.
func TestDocsDocumentRelicRetirementAndCompaction(t *testing.T) {
	t.Parallel()
	document := repositoryDoc(t, "docs", "reference.md")
	relics := docSection(document, "## Relics")
	retiring := docSection(document, "### Retiring a relic")
	compacting := docSection(document, "### Compacting relics")
	after := docSection(document, "#### After a compaction")
	require := func(name, section string, required ...string) {
		t.Helper()
		if section == "" {
			t.Fatalf("%s is missing", name)
		}
		for _, text := range required {
			if !strings.Contains(section, text) {
				t.Errorf("%s is missing %q", name, text)
			}
		}
	}
	// member is the quoted JSON name the struct gives a field.
	member := func(value any, field string) string {
		t.Helper()
		found, ok := reflect.TypeOf(value).FieldByName(field)
		if !ok {
			t.Fatalf("%T has no field %s", value, field)
		}
		name, _, _ := strings.Cut(found.Tag.Get("json"), ",")
		return "`" + name + "`"
	}

	require(`the reference's "### Retiring a relic"`, retiring,
		"workbook relic retire <relic>", "workbook relic unretire <relic>", "`--retired`", "`--all`",
		"`"+string(core.RelicOperationRetire)+"`", "`"+string(core.RelicOperationUnretire)+"`",
		"Retired is a mark, not a lock.", "No change: <relic-id>")
	require(`the reference's "### Compacting relics"`, compacting,
		"workbook compact --relics", "--retired-only", "--deleted-only", "--dry-run", "--no-backup", "--no-sync",
		"`"+core.WarningRelicContentDropped+"`", "`"+core.WarningRelicCompactionDropped+"`",
		"--relics cannot be combined with --since, --done-only, --keep-attachments or --adopt",
		"--retired-only requires --relics", "--retired-only and --deleted-only cannot be combined",
		compaction.ReasonAlreadyCompacted, compaction.ReasonNotPushed, compaction.ReasonRelicConflictParked,
		compaction.ReasonNewerWriter, compaction.ReasonUnreadable, compaction.ReasonChanged,
		compaction.ReasonTooManyChanges, compactRelicNotice,
		"--force-with-lease=refs/workbook/relics/<relic-id>:<expected>", "`compaction-published`",
		"`sync.pushed`", "no `sync.pushRelic`", "A rerun does not publish", "cannot combine",
		member(compactRelicsSummary{}, "DryRun"), member(compactRelicsSummary{}, "Skipped"),
		member(compactedRelic{}, "DroppedParts"), member(compactedRelic{}, "DroppedBytes"),
		member(core.RelicPartDrop{}, "RemovedAt"), member(core.RelicPartDrop{}, "DroppedAt"))
	require(`the reference's "#### After a compaction"`, after,
		member(core.RelicHistory{}, "Compacted"), member(core.RelicDiff{}, "Compacted"),
		"`compacted: true`", member(core.RelicHistoryCompaction{}, "RunID"),
		member(core.RelicHistoryCompaction{}, "Boundary"), "was dropped when it was compacted on",
		"has no change to part <part-id> since it was compacted on", "The kept body version names the checkpoint.",
		"`not-found` (exit `4`)")
	require("the reference's relic section", relics,
		member(core.RelicSummary{}, "Compacted"), "`parts[].dropped`", "`retired: true`",
		"`"+string(core.RelicConflictDropped)+"`", "(removed on <date>)", "`compaction-published`",
		"`relic retire`")
	for _, stale := range []string{"There is no retiring", "| `retired` | Always `false`. |", "`retired` is reserved"} {
		if strings.Contains(relics, stale) {
			t.Errorf("the reference's relic section still says %q", stale)
		}
	}

	architecture := repositoryDoc(t, "docs", "architecture.md")
	require(`the architecture's "#### Compacted relics"`, docSection(architecture, "#### Compacted relics"),
		"`"+string(core.RelicOperationCheckpoint)+"`", "R1", "R7", "--force-with-lease=refs/workbook/relics/<relic-id>:",
		"`"+core.WarningRelicCompactionDropped+"`", "compactedFrom", "move-tasks")
	refs := docSection(architecture, "### Relic refs")
	var summary []string
	checkpoint := reflect.TypeOf(core.RelicCheckpointData{})
	for index := 0; index < checkpoint.NumField(); index++ {
		name, _, _ := strings.Cut(checkpoint.Field(index).Tag.Get("json"), ",")
		summary = append(summary, "`"+name+"`")
	}
	require("the architecture's relic section", refs,
		"`checkpoint`: "+strings.Join(summary, ", "), "`relic.compacted`", "`dropped`",
		"`relic.retire`, `relic.unretire`", "generation 7")
	require(`the architecture's "## SQLite projection"`, docSection(architecture, "## SQLite projection"),
		"`compacted_run`", "`compacted_at`", "`compacted_boundary`", "`compacted_since`",
		"`dropped_removed_at`", "`dropped_at`", "`RefreshRelics`")

	for name, text := range map[string]string{
		"README.md":    repositoryDoc(t, "README.md"),
		"CHANGELOG.md": repositoryDoc(t, "CHANGELOG.md"),
		"AGENTS.md":    repositoryDoc(t, "AGENTS.md"),
	} {
		require(name, strings.Join(strings.Fields(text), " "), "compact --relics")
	}
}

// A schema is a stored format and a promise about what a save is held to, so
// the reference states the file, the words, the checking points and the limits,
// and the architecture states how a version is stored.
func TestDocsDocumentRelicSchemas(t *testing.T) {
	t.Parallel()
	document := repositoryDoc(t, "docs", "reference.md")
	reference := docSection(document, "## Relics")
	schemas := docSection(document, "### Relic schemas")
	if schemas == "" {
		t.Fatal("docs/reference.md has no \"### Relic schemas\" section")
	}
	for _, required := range []string{
		"workbook schema list", "workbook schema show", "workbook schema put",
		"workbook relic migrate", "--behind", "`conformance`", "`schemaProblem`", "`behind`",
		"`open`", "`conforms`", "`not-conforming`", "`unknown-schema`",
		`{"type":"spec","id":"`, "relic-schema-renumbered", "not a security boundary",
		// The words a rule may use.
		"`type`", "`properties`", "`required`", "`additionalProperties`", "`items`",
		"`minItems`", "`maxItems`", "`minLength`", "`maxLength`", "`minimum`", "`maximum`",
		"`enum`", "`const`", "`$ref`", "`title`", "`description`", "`$defs`",
		// The envelope, the part rule and the reference forms.
		"`parts`", "`links`", "`bodyFormats`", "`#/$defs/<name>`", "`<type>#/$defs/<name>`",
		// The checking points, and what is never checked.
		"relic create", "relic update", "relic migrate", "No read",
		"names a schema version for", "workbook sync",
		"the schema is too costly to check against this value",
		// A link's pointer is the one part of a link that is checked.
		"`link pointer: at <JSON pointer>: <what is wrong>`", "the pointer of each link added with `--pointer`",
		groupedDigits(core.MaxRelicSchemaBytes) + " bytes (64 KiB)",
		groupedDigits(core.MaxRelicSchemaVersionsPerType),
		groupedDigits(core.MaxRelicSchemaVersions),
		strconv.Itoa(core.MaxRelicSchemaRefDepth) + " deep",
	} {
		if !strings.Contains(schemas, required) {
			t.Errorf("the reference's \"### Relic schemas\" is missing %q", required)
		}
	}
	for _, stale := range []string{"| `schema` | Always `null`. |", "there are no schemas", "There are no schemas",
		"No relic's link is checked"} {
		if strings.Contains(reference, stale) {
			t.Errorf("the reference's relic section still says %q", stale)
		}
	}
	if !strings.Contains(reference, "`relic migrate`") {
		t.Error("the reference's relic section does not name relic migrate among the results")
	}

	// The example schema file is one the checker accepts.
	_, after, found := strings.Cut(document, "\n#### The schema file\n")
	if !found {
		t.Fatal("docs/reference.md has no \"#### The schema file\" section")
	}
	_, after, found = strings.Cut(after, "```json\n")
	example, _, closed := strings.Cut(after, "\n```")
	if !found || !closed {
		t.Fatal("\"#### The schema file\" has no json example")
	}
	if _, _, err := core.CompileRelicSchema("spec", []byte(example), nil); err != nil {
		t.Errorf("the reference's example schema is refused: %v", err)
	}

	architecture := strings.Join(strings.Fields(repositoryDoc(t, "docs", "architecture.md")), " ")
	for _, required := range []string{
		"schema.put", "relic-schema-", "relic.migrate", "schema version 13", "workbook.relic-schema",
		"relic-schema-renumbered",
	} {
		if !strings.Contains(architecture, required) {
			t.Errorf("the architecture is missing %q", required)
		}
	}
	if strings.Contains(architecture, "`schema` is always `null`") {
		t.Error("the architecture still says a relic's schema is always null")
	}
	readme := strings.Join(strings.Fields(repositoryDoc(t, "README.md")), " ")
	for _, required := range []string{"`workbook schema put", "`workbook relic migrate"} {
		if !strings.Contains(readme, required) {
			t.Errorf("the README's relic text is missing %q", required)
		}
	}
}

// groupedDigits writes a count as the reference does: digits, with a comma
// before each group of three.
func groupedDigits(number int) string {
	digits := strconv.Itoa(number)
	for at := len(digits) - 3; at > 0; at -= 3 {
		digits = digits[:at] + "," + digits[at:]
	}
	return digits
}

// One relic that cannot be read fails relic list as a whole, and the error
// says which relic it was, as it does for every command that reads that relic.
func TestAnUnreadableRelicIsNamedByEveryCommandThatReadsIt(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := openRelicFixture(t, repository)
	healthy := fixture.threeCommitRelic(t, 1)
	root := fixture.createRelic(t, 2, []byte("the first text\n"))
	relicID := root.Operation.RelicID
	pack := nextRelicPack(root, core.RelicOperation{ID: relicTestID(22), Type: core.RelicOperationTitleSet, Title: "A new title"})
	record, err := core.ApplyRelic(&root.Record, pack)
	if err != nil {
		t.Fatalf("ApplyRelic() error = %v", err)
	}
	// The honest state with one space after it: every member is right and the
	// bytes are not the ones Workbook writes.
	state := []byte(strings.TrimSuffix(string(encodedRelicDocument(t, record.State)), "\n") + " \n")
	forged := fixture.forgeRelicCommit(t, root.Head, encodedRelicDocument(t, pack), state,
		gitOutput(t, repository, "rev-parse", root.Head+":parts"))
	gitInput(t, repository, nil, "update-ref", relicRef(relicID), forged, root.Head)

	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("another text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	part := root.Record.State.Relic.Parts[0].ID
	for name, args := range map[string][]string{
		"list":             {"list"},
		"list --all":       {"list", "--all"},
		"show":             {"show", relicID},
		"show a part":      {"show", relicID + "#" + part},
		"export":           {"export", relicID},
		"update the title": {"update", relicID, "--title", "Another title"},
		"update a body":    {"update", relicID, "--part", part, "--body-file", body, "--base", part},
		"delete":           {"delete", relicID},
	} {
		message := relicErrorMessage(t, repository, 7, core.CategoryCorruptData, args...)
		if !strings.Contains(message, "relic "+relicID+": ") || !strings.Contains(message, "not canonical") {
			t.Fatalf("relic %s: message %q does not name relic %s and what is wrong with it", name, message, relicID)
		}
		if strings.Contains(message, healthy.Operation.RelicID) {
			t.Fatalf("relic %s: message %q names the healthy relic", name, message)
		}
	}
	if head := relicHeadOf(t, repository, relicID); head != forged {
		t.Fatalf("a refused command moved the relic from %s to %s", forged, head)
	}
	if shown := shownRelic(t, repository, healthy.Operation.RelicID); shown.ID != healthy.Operation.RelicID {
		t.Fatalf("relic show of the healthy relic = %#v", shown)
	}
}

// endlessRelicInput is a standard input that never ends: every read fills the
// buffer it is given. It counts what was taken from it, and fails once a
// megabyte more than the limit has been taken, so a reader with no bound fails
// this test instead of exhausting the machine.
type endlessRelicInput struct{ taken int64 }

func (input *endlessRelicInput) Read(buffer []byte) (int, error) {
	if input.taken > maxRelicDocumentBytes+1024*1024 {
		return 0, errors.New("the input was read past any bound")
	}
	for index := range buffer {
		buffer[index] = ' '
	}
	input.taken += int64(len(buffer))
	return len(buffer), nil
}

// relic create --from-file reads its document through a limit, so input that
// never ends is refused after a bounded read instead of filling memory.
func TestRelicCreateFromFileReadsABoundedDocument(t *testing.T) {
	t.Parallel()
	if maxRelicDocumentBytes != 64*1024*1024 {
		t.Fatalf("maxRelicDocumentBytes = %d, want 64 MiB", maxRelicDocumentBytes)
	}
	repository := initializedRepository(t)
	refused := func(label string, code int, stdout, stderr string) {
		t.Helper()
		if code != 5 || stdout != "" {
			t.Fatalf("%s = (%d, %q, %q), want exit 5", label, code, stdout, stderr)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "")
		for _, want := range []string{"67108864 bytes", "relic document"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%s: error %q does not hold %q", label, stderr, want)
			}
		}
		if len(stderr) > 400 {
			t.Fatalf("%s: the error is %d bytes long; it must not repeat the document", label, len(stderr))
		}
	}

	endless := &endlessRelicInput{}
	code, stdout, stderr := runWithInput(t, repository, endless, "relic", "create", "--from-file", "-", "--json")
	refused("an endless standard input", code, stdout, stderr)
	// One byte past the limit is enough to know, and is all that is read.
	if endless.taken != maxRelicDocumentBytes+1 {
		t.Fatalf("an endless standard input: %d bytes were read, want %d", endless.taken, maxRelicDocumentBytes+1)
	}

	// A document that is valid in every other way, one byte over and exactly
	// at the limit: JSON allows white space after the value.
	document := relicTestDocument(t)
	padded := func(size int) []byte {
		return append(append(make([]byte, 0, size), document...), bytes.Repeat([]byte{' '}, size-len(document))...)
	}
	path := writeRelicDocumentFile(t, padded(maxRelicDocumentBytes+1))
	code, stdout, stderr = run(t, repository, "relic", "create", "--from-file", path, "--json")
	refused("a file one byte over", code, stdout, stderr)
	code, stdout, stderr = runWithInput(t, repository, bytes.NewReader(padded(maxRelicDocumentBytes+1)),
		"relic", "create", "--from-file", "-", "--json")
	refused("standard input one byte over", code, stdout, stderr)
	if code, stdout, _ := run(t, repository, "relic", "list", "--json"); code != 0 || !strings.Contains(stdout, `"data":[]`) {
		t.Fatalf("relic list after the refusals = (%d, %q), want no relic", code, stdout)
	}

	if err := os.WriteFile(path, padded(maxRelicDocumentBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	created := mustCreateRelic(t, repository, "--from-file", path)
	if created.Relic.Title != "Archive plan" {
		t.Fatalf("a document exactly at the limit created %q", created.Relic.Title)
	}
}
