package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// specRelicFile is a relic create --from-file document for a spec with one
// status property.
func specRelicFile(t *testing.T, status string) string {
	t.Helper()
	return writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"T","type":"spec",`+
		`"properties":{"status":"`+status+`"}}`))
}

func TestRelicSaveRefusedByItsSchema(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustPutSchema(t, repository, "spec", specSchemaTwo)

	message := relicErrorMessage(t, repository, 5, core.CategoryValidation,
		"create", "--from-file", specRelicFile(t, "wip"))
	if !strings.HasPrefix(message, "relic does not fit schema spec version 1: ") || !strings.HasSuffix(message, "(enum)") {
		t.Fatalf("create message = %q, want the schema refusal ending (enum)", message)
	}
	if got := refsUnder(t, repository, "refs/workbook/relics/"); got != "" {
		t.Fatalf("a refused create wrote a relic:\n%s", got)
	}

	created := mustCreateRelic(t, repository, "--from-file", specRelicFile(t, "draft"))
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", created.Relic.ID, "--set", "status=wip")
	if !strings.HasSuffix(message, "(enum)") {
		t.Fatalf("update --set message = %q, want it to end (enum)", message)
	}
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", created.Relic.ID, "--clear", "status")
	if !strings.HasSuffix(message, "(required)") {
		t.Fatalf("update --clear message = %q, want it to end (required)", message)
	}
	// The version the relic names is judged when it is shown.
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", created.Relic.ID)
	if shown.State != core.RelicConformanceConforms || shown.Schema == nil || shown.Schema.Version != 1 {
		t.Fatalf("show = %+v %+v, want a conforming relic under version 1", shown.RelicConformance, shown.Schema)
	}
}

// relic create --from-file refuses a document naming a version this clone
// does not have.
func TestRelicCreateFromADocumentNamingAnUnknownSchema(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	document := `{"format":"workbook.relic","version":1,"title":"T","type":"spec",` +
		`"schema":{"type":"spec","id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","version":1}}`
	message := relicErrorMessage(t, repository, 6, core.CategoryStaleWrite,
		"create", "--from-file", writeRelicDocumentFile(t, []byte(document)))
	if !strings.HasPrefix(message, "the document names schema version 01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Fatalf("message = %q, want it to say the document names the version", message)
	}
}

func TestRelicMigrateCommand(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	one := mustPutSchema(t, repository, "spec", specSchemaOne)
	created := mustCreateRelic(t, repository, "T", "--type", "spec")
	id := created.Relic.ID
	two := mustPutSchema(t, repository, "spec", specSchemaTwo)

	relicErrorMessage(t, repository, 2, core.CategoryInvocation, "migrate")
	message := relicErrorMessage(t, repository, 2, core.CategoryInvocation, "migrate", id)
	if !strings.Contains(message, "--to") {
		t.Fatalf("migrate without --to = %q, want it to ask for --to", message)
	}
	relicErrorMessage(t, repository, 2, core.CategoryInvocation, "migrate", id, "--to", "")
	relicErrorMessage(t, repository, 4, core.CategoryNotFound, "migrate", id, "--to", "9")
	message = relicErrorMessage(t, repository, 5, core.CategoryValidation, "migrate", id, "--to", "2")
	if !strings.Contains(message, "relic does not fit schema spec version 2") {
		t.Fatalf("migrate refusal = %q", message)
	}

	mustUpdateRelic(t, repository, id, "--set", "status=draft")
	var migrated core.RelicMutationResult
	mustRelic(t, repository, &migrated, "migrate", id, "--to", "2")
	if !migrated.Changed || migrated.Relic.Schema == nil || migrated.Relic.Schema.ID != two.ID {
		t.Fatalf("migrate = %+v, want a change naming %s", migrated, two.ID)
	}
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", id)
	if shown.Schema == nil || *shown.Schema != (core.RelicSchemaRef{Type: "spec", ID: two.ID, Version: 2}) {
		t.Fatalf("show after migrate = %+v, want version 2", shown.Schema)
	}
	code, stdout, stderr := run(t, repository, "relic", "migrate", id, "--to", "2")
	if code != 0 || !strings.HasPrefix(stdout, "No change: "+id+"\n") {
		t.Fatalf("repeated migrate = (%d, %q, %q), want No change", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "relic", "migrate", id, "--to", "spec@1", "--no-sync")
	if code != 0 || !strings.HasPrefix(stdout, id+"\tspec\tT\n") {
		t.Fatalf("migrate to spec@1 = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, _ = run(t, repository, "relic", "show", id)
	if code != 0 || !strings.Contains(stdout, "\nSchema:\tspec version 1\n") || strings.Contains(stdout, "Does not fit") {
		t.Fatalf("show = (%d, %q), want the schema line for version 1 (%s)", code, stdout, one.ID)
	}
	mustRelic(t, repository, &migrated, "migrate", id, "--to", "note")
	if migrated.Relic.Schema != nil || migrated.Relic.Type != "note" {
		t.Fatalf("migrate to an open type = %+v", migrated.Relic)
	}
	code, stdout, _ = run(t, repository, "relic", "show", id)
	if code != 0 || !strings.Contains(stdout, "\nSchema:\topen\n") {
		t.Fatalf("show = (%d, %q), want Schema: open", code, stdout)
	}
}

// relic show says which version a relic names, or that this clone does not
// have it, and what does not fit.
func TestRelicShowTextNamesTheSchema(t *testing.T) {
	t.Parallel()
	for name, shown := range map[string]struct {
		document core.RelicDocument
		want     string
	}{
		"unknown": {
			core.RelicDocument{
				Schema:           &core.RelicSchemaRef{Type: "spec", ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
				RelicConformance: core.RelicConformance{State: core.RelicConformanceUnknownSchema},
			},
			"Schema:\tunknown version 01ARZ3NDEKTSV4RRFFQ69G5FAV\n",
		},
		"not conforming": {
			core.RelicDocument{
				Schema: &core.RelicSchemaRef{Type: "spec", ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Version: 3},
				RelicConformance: core.RelicConformance{
					State: core.RelicConformanceNotConforming, Problem: "property \"a\nb\": bad (enum)",
				},
			},
			"Schema:\tspec version 3\nDoes not fit:\tproperty \"a b\": bad (enum)\n",
		},
	} {
		var output bytes.Buffer
		writeRelicDocument(&output, shown.document, false)
		if !strings.Contains(output.String(), shown.want) {
			t.Errorf("%s: output = %q, want %q", name, output.String(), shown.want)
		}
	}
}
