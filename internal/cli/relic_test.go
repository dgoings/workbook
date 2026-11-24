package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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

	for _, command := range []string{"show", "delete", "restore", "export"} {
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
		{"delete"},
		{"restore"},
		{"export"},
	} {
		relicErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	message := relicErrorMessage(t, repository, 2, core.CategoryInvocation)
	for _, subcommand := range []string{"create", "list", "show", "update", "delete", "restore", "export"} {
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
	if !strings.Contains(message, "ambiguous") {
		t.Fatalf("show WB-R- message = %q, want ambiguous", message)
	}
}

func TestRelicHelp(t *testing.T) {
	t.Parallel()
	output := assertHelpOutput(t, []string{"help", "relic"}, "Usage: workbook relic <command> [options]")
	for _, subcommand := range []string{"create", "list", "show", "update", "delete", "restore", "export"} {
		if !strings.Contains(output, "\n  "+subcommand+" ") {
			t.Errorf("relic help = %q, want subcommand %s", output, subcommand)
		}
	}
	assertHelpOutput(t, []string{"relic", "show", "--help"},
		"Usage: workbook relic show <relic>[#<part>] [--body] [--removed] [--json]")
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
// and so does the fact that nothing shares a relic yet. Each document is held
// to its relic section, because the task sections already say "500" and "1 MiB".
func TestReferenceDocumentsLocalRelics(t *testing.T) {
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
		"not shared",
		"workbook.relic",
		// The limits, as the table writes them.
		"500", "100", "16", "256", "64 KiB", "16 KiB", "1 MiB", "2,000", "4,000", "16 MiB",
		// What relic update's help says about where a relic option may be typed.
		"always act on the relic",
	} {
		if !strings.Contains(reference, required) {
			t.Errorf("the reference's relic section is missing %q", required)
		}
	}

	architecture := docSection(repositoryDoc(t, "docs", "architecture.md"), "### Relic refs")
	if architecture == "" {
		t.Fatal("docs/architecture.md has no \"### Relic refs\" section")
	}
	for _, required := range []string{"refs/workbook/relics/", "links.json", "parts/"} {
		if !strings.Contains(architecture, required) {
			t.Errorf("the architecture's relic section is missing %q", required)
		}
	}
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
