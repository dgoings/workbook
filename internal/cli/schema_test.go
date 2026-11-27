package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// The two spec schemas the command tests save: the first allows a status of
// draft or approved, the second also requires one.
const (
	specSchemaOne = `{
  "title": "Spec",
  "properties": {"type": "object", "properties": {"status": {"enum": ["draft", "approved"]}}}
}`
	specSchemaTwo = `{
  "title": "Spec",
  "properties": {"type": "object", "required": ["status"], "properties": {"status": {"enum": ["draft", "approved"]}}}
}`
)

type schemaVersionData struct {
	Type       string            `json:"type"`
	Version    int               `json:"version"`
	ID         string            `json:"id"`
	Bytes      int64             `json:"bytes"`
	References map[string]string `json:"references"`
	Schema     json.RawMessage   `json:"schema"`
	Changed    bool              `json:"changed"`
}

type schemaListData struct {
	Type     string `json:"type"`
	Version  int    `json:"version"`
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
	ID       string `json:"id"`
}

func writeSchemaFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// mustSchema runs a schema command with --json, requires it to succeed
// quietly, and decodes its data into target.
func mustSchema(t *testing.T, repository string, target any, args ...string) {
	t.Helper()
	full := append([]string{"schema"}, append(args, "--json")...)
	code, stdout, stderr := run(t, repository, full...)
	if code != 0 || stderr != "" {
		t.Fatalf("%s = (%d, %q, %q)", strings.Join(full, " "), code, stdout, stderr)
	}
	result := assertJSONResult(t, stdout, "schema "+args[0])
	if target != nil {
		if err := json.Unmarshal(result.Data, target); err != nil {
			t.Fatalf("decode %s: %v; data = %s", strings.Join(full, " "), err, result.Data)
		}
	}
}

func mustPutSchema(t *testing.T, repository, relicType, content string, args ...string) schemaVersionData {
	t.Helper()
	var put schemaVersionData
	mustSchema(t, repository, &put, append([]string{"put", relicType, writeSchemaFile(t, content)}, args...)...)
	return put
}

func schemaErrorMessage(t *testing.T, repository string, want int, category core.Category, args ...string) string {
	t.Helper()
	full := append([]string{"schema"}, append(args, "--json")...)
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

func TestSchemaPutShowList(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)

			var empty []schemaListData
			mustSchema(t, repository, &empty, "list")
			if empty == nil || len(empty) != 0 {
				t.Fatalf("schema list of no schemas = %#v, want an empty list", empty)
			}

			first := mustPutSchema(t, repository, "spec", specSchemaOne)
			second := mustPutSchema(t, repository, "spec", specSchemaTwo)
			if !first.Changed || first.Version != 1 || !second.Changed || second.Version != 2 || first.ID == second.ID {
				t.Fatalf("puts = %+v, %+v; want versions 1 and 2, both changes", first, second)
			}
			again := mustPutSchema(t, repository, "spec", specSchemaTwo)
			if again.Changed || again.Version != 2 || again.ID != second.ID {
				t.Fatalf("repeated put = %+v, want no change at version 2", again)
			}

			var listed []schemaListData
			mustSchema(t, repository, &listed, "list")
			want := schemaListData{Type: "spec", Version: 2, Versions: 2, Bytes: second.Bytes, ID: second.ID}
			if len(listed) != 1 || listed[0] != want {
				t.Fatalf("schema list = %+v, want %+v", listed, want)
			}

			var shown schemaVersionData
			mustSchema(t, repository, &shown, "show", "spec", "--version", "1")
			canonical, err := core.CanonicalRelicValue([]byte(specSchemaOne))
			if err != nil {
				t.Fatal(err)
			}
			if shown.Type != "spec" || shown.Version != 1 || shown.ID != first.ID || shown.Bytes != first.Bytes ||
				string(shown.Schema) != string(canonical) || shown.References == nil || len(shown.References) != 0 {
				t.Fatalf("schema show spec --version 1 = %+v, want the first file canonicalized: %s", shown, canonical)
			}
			var newest schemaVersionData
			mustSchema(t, repository, &newest, "show", "spec")
			if newest.Version != 2 || newest.ID != second.ID {
				t.Fatalf("schema show spec = %+v, want version 2", newest)
			}

			// A reference to another type is pinned and listed.
			plan := mustPutSchema(t, repository, "plan", `{"$defs": {"step": {"type": "object"}}}`)
			mustPutSchema(t, repository, "design", `{"properties": {"$ref": "plan#/$defs/step"}}`)
			var design schemaVersionData
			mustSchema(t, repository, &design, "show", "design")
			if len(design.References) != 1 || design.References["plan"] != plan.ID {
				t.Fatalf("design references = %v, want plan pinned to %s", design.References, plan.ID)
			}
		})
	}
}

func TestSchemaPutReadsStandardInput(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	code, stdout, stderr := runWithInput(t, repository, strings.NewReader(specSchemaOne), "schema", "put", "spec", "-", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("schema put spec - = (%d, %q, %q)", code, stdout, stderr)
	}
	assertJSONResult(t, stdout, "schema put")
	// A refusal after "-" is still reported as JSON.
	code, stdout, stderr = runWithInput(t, repository, strings.NewReader(`{"nope": 1}`), "schema", "put", "spec", "-", "--json")
	if code != 5 || stdout != "" {
		t.Fatalf("refused schema put spec - = (%d, %q, %q), want exit 5", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
}

func TestSchemaPutRefusals(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustPutSchema(t, repository, "spec", specSchemaOne)
	tip := gitOutput(t, repository, "rev-parse", "refs/workbook/config")
	for name, refused := range map[string]struct{ content, want string }{
		"unsupported word": {`{"properties": {"properties": {"a": {"pattern": "x"}}}}`, "schema for spec: /properties/properties/a/pattern: "},
		"outside ref":      {`{"properties": {"$ref": "https://example.test/schema"}}`, "schema for spec: /properties/$ref: "},
		"loop":             {`{"$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}, "properties": {"$ref": "#/$defs/a"}}`, "schema for spec: /$defs/"},
		"over the limit":   {`{"description": "` + strings.Repeat("x", core.MaxRelicSchemaBytes) + `"}`, "65536 bytes"},
	} {
		message := schemaErrorMessage(t, repository, 5, core.CategoryValidation, "put", "spec", writeSchemaFile(t, refused.content))
		if !strings.Contains(message, refused.want) {
			t.Errorf("%s: message = %q, want it to contain %q", name, message, refused.want)
		}
	}
	schemaErrorMessage(t, repository, 5, core.CategoryValidation, "put", "Spec", writeSchemaFile(t, specSchemaOne))
	schemaErrorMessage(t, repository, 4, core.CategoryNotFound, "put", "spec", filepath.Join(t.TempDir(), "absent.json"))
	if got := gitOutput(t, repository, "rev-parse", "refs/workbook/config"); got != tip {
		t.Fatalf("refused puts moved the configuration ledger from %s to %s", tip, got)
	}
}

func TestSchemaCommandRefusals(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, args := range [][]string{
		{},
		{"nope"},
		{"show"},
		{"put"},
		{"put", "spec"},
		{"show", "spec", "--version", "0"},
		{"show", "spec", "--version", "two"},
	} {
		schemaErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	message := schemaErrorMessage(t, repository, 2, core.CategoryInvocation)
	if !strings.Contains(message, "list, show, put") {
		t.Errorf("schema with no subcommand = %q, want it to name the subcommands", message)
	}
	if message := schemaErrorMessage(t, repository, 4, core.CategoryNotFound, "show", "note"); message != "type note has no schema" {
		t.Errorf("schema show of a type with none = %q", message)
	}
	mustPutSchema(t, repository, "spec", specSchemaOne)
	if message := schemaErrorMessage(t, repository, 4, core.CategoryNotFound, "show", "spec", "--version", "9"); message != "type spec has no schema version 9" {
		t.Errorf("schema show --version 9 = %q", message)
	}
}

func TestSchemaTextOutput(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	path := writeSchemaFile(t, specSchemaOne)
	code, stdout, stderr := run(t, repository, "schema", "put", "spec", path)
	if code != 0 || !strings.HasPrefix(stdout, "spec\tversion 1\t") {
		t.Fatalf("schema put = (%d, %q, %q), want the saved version's line", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "schema", "put", "spec", path)
	if code != 0 || !strings.HasPrefix(stdout, "No change: spec version 1") {
		t.Fatalf("repeated schema put = (%d, %q, %q), want No change", code, stdout, stderr)
	}
	var shown schemaVersionData
	mustSchema(t, repository, &shown, "show", "spec")

	code, stdout, stderr = run(t, repository, "schema", "list")
	want := "spec\tversion 1\t1 version\t" + itoa(shown.Bytes) + " bytes\n"
	if code != 0 || stdout != want || stderr != "" {
		t.Fatalf("schema list = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
	mustPutSchema(t, repository, "spec", specSchemaTwo)
	code, stdout, _ = run(t, repository, "schema", "list")
	if code != 0 || !strings.HasPrefix(stdout, "spec\tversion 2\t2 versions\t") {
		t.Fatalf("schema list = (%d, %q), want 2 versions", code, stdout)
	}

	code, stdout, stderr = run(t, repository, "schema", "show", "spec", "--version", "1")
	header := "spec\tversion 1\t" + shown.ID + "\t" + itoa(shown.Bytes) + " bytes\n"
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, header+"  {\n") ||
		!strings.Contains(stdout, "\n      \"properties\": {\n") || !strings.HasSuffix(stdout, "\n  }\n") {
		t.Fatalf("schema show = (%d, %q, %q), want %q and the schema indented", code, stdout, stderr, header)
	}
}

func itoa(number int64) string {
	encoded, _ := json.Marshal(number)
	return string(encoded)
}

// A schema's strings are written by anyone who can push, so no control
// character of theirs reaches a terminal.
func TestSchemaShowIsTerminalSafe(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustPutSchema(t, repository, "spec", "{\"title\": \"a\u009b31mred\"}")
	code, stdout, stderr := run(t, repository, "schema", "show", "spec")
	if code != 0 || stderr != "" || strings.ContainsRune(stdout, 0x9b) {
		t.Fatalf("schema show = (%d, %q, %q), want no control character", code, stdout, stderr)
	}
}

func TestSchemaPutRefreshesTheGuidelines(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	guidelines := func() string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(repository, ".workbook", "guidelines.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	// A status the guidelines do not know about yet makes them stale.
	if code, _, stderr := run(t, repository, "status", "add", "icebox", "--no-docs"); code != 0 {
		t.Fatalf("status add = (%d, %q)", code, stderr)
	}
	mustPutSchema(t, repository, "spec", specSchemaOne, "--no-docs")
	if strings.Contains(guidelines(), "icebox") {
		t.Fatal("schema put --no-docs regenerated the guidelines")
	}
	mustPutSchema(t, repository, "spec", specSchemaTwo)
	if !strings.Contains(guidelines(), "icebox") {
		t.Fatal("schema put did not regenerate the guidelines")
	}
}

func TestSchemaHelp(t *testing.T) {
	t.Parallel()
	output := assertHelpOutput(t, []string{"help", "schema"}, "Usage: workbook schema <command> [options]")
	for _, subcommand := range []string{"list", "show", "put"} {
		if !strings.Contains(output, "\n  "+subcommand+" ") {
			t.Errorf("schema help = %q, want subcommand %s", output, subcommand)
		}
	}
	assertHelpOutput(t, []string{"schema", "put", "--help"},
		"Usage: workbook schema put <type> <file|-> [--no-sync] [--no-docs] [--json]")
	assertHelpOutput(t, []string{"relic", "migrate", "--help"},
		"Usage: workbook relic migrate <relic> --to <target> [--no-sync] [--json]")
	if !strings.Contains(usage, "  schema <command> [options]\n") {
		t.Fatalf("usage = %q, want the schema line", usage)
	}
}

// Two clones save a schema for one type while apart. Both versions survive,
// the one saved second is renumbered, and each relic still names the version
// it was written under.
func TestTwoClonesSavingOneTypeKeepBothVersions(t *testing.T) {
	t.Parallel()
	first, second := schemaSharedClones(t)

	putOne := mustPutSchema(t, first, "spec", specSchemaOne, "--no-sync")
	relicOne := mustCreateRelic(t, first, "First", "--type", "spec", "--no-sync")
	putTwo := mustPutSchema(t, second, "spec", specSchemaTwo, "--no-sync")
	relicTwo := mustCreateRelic(t, second, "--from-file", specRelicFile(t, "draft"), "--no-sync")

	syncClone := func(clone string) (string, string) {
		t.Helper()
		code, stdout, stderr := run(t, clone, "sync")
		if code != 0 {
			t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
		}
		return stdout, stderr
	}
	syncClone(first)
	_, stderr := syncClone(second)
	warning := "workbook: warning: the schema for spec saved here as version 1 is now version 2; a teammate saved version 1 first"
	if !strings.Contains(stderr, warning) {
		t.Fatalf("second clone's first sync stderr = %q, want %q", stderr, warning)
	}
	syncClone(first)
	syncClone(second)

	for name, clone := range map[string]string{"first": first, "second": second} {
		var listed []schemaListData
		mustSchema(t, clone, &listed, "list")
		if len(listed) != 1 || listed[0].Versions != 2 || listed[0].ID != putTwo.ID {
			t.Fatalf("%s clone's schema list = %+v, want two spec versions, %s newest", name, listed, putTwo.ID)
		}
		for relic, want := range map[string]core.RelicSchemaRef{
			relicOne.Relic.ID: {Type: "spec", ID: putOne.ID, Version: 1},
			relicTwo.Relic.ID: {Type: "spec", ID: putTwo.ID, Version: 2},
		} {
			var shown core.RelicDocument
			mustRelic(t, clone, &shown, "show", relic)
			if shown.Schema == nil || *shown.Schema != want {
				t.Fatalf("%s clone shows %s under %+v, want %+v", name, relic, shown.Schema, want)
			}
		}
	}
}

// schemaSharedClones returns two set-up clones sharing one configuration
// ledger through origin.
func schemaSharedClones(t *testing.T) (string, string) {
	t.Helper()
	first, second := cliSyncRepositories(t)
	for _, clone := range []string{first, second} {
		if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
			t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
		}
	}
	if a, b := gitOutput(t, first, "rev-parse", "refs/workbook/config"), gitOutput(t, second, "rev-parse", "refs/workbook/config"); a != b {
		t.Fatalf("the clones' configuration ledgers differ after setup: %s, %s", a, b)
	}
	return first, second
}

// Every command that fetches says when the replay renumbered a version saved
// here, in its own warnings.
func TestSchemaRenumberingWarnsInEveryFetchingCommand(t *testing.T) {
	t.Parallel()
	for name, command := range map[string][]string{
		"fetch":        {"fetch"},
		"sync":         {"sync"},
		"relic create": {"relic", "create", "Note", "--type", "note"},
		"create":       {"create", "A task"},
		"status add":   {"status", "add", "icebox"},
		"schema put":   {"schema", "put", "plan", "-"},
		"setup":        {"setup"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second := schemaSharedClones(t)
			mustPutSchema(t, second, "spec", specSchemaTwo, "--no-sync")
			mustPutSchema(t, first, "spec", specSchemaOne)
			code, stdout, stderr := runWithInput(t, second, strings.NewReader("{}"), append(command, "--json")...)
			if code != 0 {
				t.Fatalf("%s = (%d, %q, %q)", name, code, stdout, stderr)
			}
			var envelope struct {
				Warnings []core.Warning `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, warning := range envelope.Warnings {
				if warning.Code == core.WarningRelicSchemaRenumbered {
					found++
				}
			}
			if found != 1 {
				t.Fatalf("%s warnings = %+v, want one %s", name, envelope.Warnings, core.WarningRelicSchemaRenumbered)
			}
		})
	}
}

func TestStatusesWorkWithSchemaBlobsMissing(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	put := mustPutSchema(t, repository, "spec", specSchemaOne)
	blob := gitOutput(t, repository, "rev-parse", "refs/workbook/config:relic-schema-"+put.ID)
	path := gitOutput(t, repository, "rev-parse", "--git-path", "objects/"+blob[:2]+"/"+blob[2:])
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository, path)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the schema blob: %v", err)
	}
	for _, args := range [][]string{
		{"status", "list", "--json"},
		{"list", "--json"},
		{"create", "Still works", "--json"},
		{"schema", "list", "--json"},
		// A relic of a type with no schema reads no schema.
		{"relic", "create", "Note", "--type", "note", "--json"},
		{"relic", "list", "--json"},
	} {
		if code, stdout, stderr := run(t, repository, args...); code != 0 {
			t.Fatalf("%s = (%d, %q, %q)", strings.Join(args, " "), code, stdout, stderr)
		}
	}
	schemaErrorMessage(t, repository, 7, core.CategoryCorruptData, "show", "spec")
}

// A relic command's schema listing is the one decoded with the settings the
// command read, so asking it costs no Git work.
func TestRelicServiceListsSchemasFromTheDecodedSettings(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	put := mustPutSchema(t, repository, "spec", specSchemaOne)
	service, _, err := openRelicServiceFor(t.Context(), repository, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if service.Schemas == nil {
		t.Fatal("the relic service has no schema store")
	}
	gitOutput(t, repository, "update-ref", "-d", "refs/workbook/config")
	listing, err := service.Schemas.RelicSchemaListing(t.Context(), service.Config)
	if err != nil {
		t.Fatal(err)
	}
	if version, ok := listing.Newest("spec"); !ok || version.ID != put.ID {
		t.Fatalf("listing = %+v, want %s from the settings already decoded", listing, put.ID)
	}
}
