package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Schemas the conformance tests save: owner version 1 lets a spec have an
// owner, and version 2 requires one.
const (
	ownerSchemaOne = `{"properties":{"type":"object","properties":{"owner":{"type":"string"}}}}`
	ownerSchemaTwo = `{"properties":{"type":"object","required":["owner"],"properties":{"owner":{"type":"string"}}}}`
)

func ownedSpecFile(t *testing.T) string {
	t.Helper()
	return writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"Owned","type":"spec",`+
		`"properties":{"owner":"ana"}}`))
}

func relicListOf(t *testing.T, repository string, args ...string) []core.RelicSummary {
	t.Helper()
	var listed []core.RelicSummary
	mustRelic(t, repository, &listed, append([]string{"list"}, args...)...)
	return listed
}

func listedIDs(summaries []core.RelicSummary) []string {
	ids := []string{}
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	return ids
}

func syncedClone(t *testing.T, clone string) {
	t.Helper()
	if code, stdout, stderr := run(t, clone, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
}

// One clone clears an optional property while the other migrates the relic
// to a version that requires it. Both edits were valid, both apply, and the
// relic they leave is readable and flagged in both clones.
func TestMigrateAndClearMeetingFlagTheRelic(t *testing.T) {
	t.Parallel()
	first, second := schemaSharedClones(t)
	mustPutSchema(t, first, "spec", ownerSchemaOne)
	relicID := mustCreateRelic(t, first, "--from-file", ownedSpecFile(t)).Relic.ID
	mustPutSchema(t, first, "spec", ownerSchemaTwo)
	syncedClone(t, second)

	mustRelic(t, first, nil, "update", relicID, "--clear", "owner", "--no-sync")
	mustRelic(t, second, nil, "migrate", relicID, "--to", "2", "--no-sync")
	syncedClone(t, first)
	syncedClone(t, second)
	syncedClone(t, first)

	for name, clone := range map[string]string{"first": first, "second": second} {
		code, stdout, stderr := run(t, clone, "relic", "show", relicID, "--json")
		if code != 0 {
			t.Fatalf("%s clone's relic show = (%d, %q, %q)", name, code, stdout, stderr)
		}
		var shown core.RelicDocument
		mustRelic(t, clone, &shown, "show", relicID)
		if shown.State != core.RelicConformanceNotConforming || !strings.HasPrefix(shown.Problem, `property "owner": `) ||
			!strings.HasSuffix(shown.Problem, "(required)") || shown.Schema == nil || shown.Schema.Version != 2 {
			t.Fatalf("%s clone shows %+v under %+v, want not conforming for the owner", name, shown.RelicConformance, shown.Schema)
		}
		listed := relicListOf(t, clone)
		if len(listed) != 1 || listed[0].RelicConformance == nil || *listed[0].RelicConformance != shown.RelicConformance {
			t.Fatalf("%s clone lists %s, want %+v", name, mustJSON(t, listed), shown.RelicConformance)
		}
	}
}

// A clone that holds a relic but not yet the settings listing its version
// reads the relic, refuses to update it, and judges it once it syncs.
func TestRelicNamingAnUnknownVersionSyncsAndReads(t *testing.T) {
	t.Parallel()
	first, second := schemaSharedClones(t)
	before := gitOutput(t, second, "rev-parse", "refs/workbook/config")
	mustPutSchema(t, first, "spec", ownerSchemaTwo)
	relicID := mustCreateRelic(t, first, "--from-file", ownedSpecFile(t)).Relic.ID
	syncedClone(t, second)
	gitOutput(t, second, "update-ref", "refs/workbook/config", before)

	var shown core.RelicDocument
	mustRelic(t, second, &shown, "show", relicID)
	if shown.State != core.RelicConformanceUnknownSchema || shown.Schema == nil || shown.Schema.Version != 0 {
		t.Fatalf("show before the settings arrive = %+v %+v, want an unknown version", shown.RelicConformance, shown.Schema)
	}
	listed := relicListOf(t, second)
	if len(listed) != 1 || listed[0].RelicConformance == nil || listed[0].State != core.RelicConformanceUnknownSchema {
		t.Fatalf("list before the settings arrive = %s", mustJSON(t, listed))
	}
	if behind := relicListOf(t, second, "--behind"); len(behind) != 0 {
		t.Fatalf("--behind lists a relic naming an unknown version: %s", mustJSON(t, behind))
	}
	relicErrorMessage(t, second, 6, core.CategoryStaleWrite, "update", relicID, "--title", "Renamed", "--no-sync")

	syncedClone(t, second)
	mustRelic(t, second, &shown, "show", relicID)
	if shown.State != core.RelicConformanceConforms || shown.Schema == nil || shown.Schema.Version != 1 {
		t.Fatalf("show after sync = %+v %+v, want conforms under version 1", shown.RelicConformance, shown.Schema)
	}
	if listed := relicListOf(t, second); len(listed) != 1 || listed[0].State != core.RelicConformanceConforms {
		t.Fatalf("list after sync = %s, want conforms", mustJSON(t, listed))
	}
}

func TestRelicListBehind(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	none := mustCreateRelic(t, repository, "Before any schema", "--type", "spec").Relic.ID
	planBefore := mustCreateRelic(t, repository, "Plan before its schema", "--type", "plan").Relic.ID
	mustPutSchema(t, repository, "spec", ownerSchemaOne)
	onOne := mustCreateRelic(t, repository, "On one", "--type", "spec").Relic.ID
	mustPutSchema(t, repository, "spec", ownerSchemaTwo)
	newest := mustCreateRelic(t, repository, "--from-file", ownedSpecFile(t)).Relic.ID
	mustPutSchema(t, repository, "plan", `{}`)
	planNewest := mustCreateRelic(t, repository, "Plan", "--type", "plan").Relic.ID
	mustCreateRelic(t, repository, "Open", "--type", "note")

	if got, want := listedIDs(relicListOf(t, repository, "--behind")), []string{none, planBefore, onOne}; mustJSON(t, got) != mustJSON(t, want) {
		t.Fatalf("--behind = %v, want %v", got, want)
	}
	if got := listedIDs(relicListOf(t, repository, "--behind", "--type", "plan")); mustJSON(t, got) != mustJSON(t, []string{planBefore}) {
		t.Fatalf("--behind --type plan = %v, want %s", got, planBefore)
	}
	code, stdout, stderr := run(t, repository, "relic", "list", "--behind", "--json")
	if code != 0 || strings.Count(stdout, `"behind":true`) != 3 {
		t.Fatalf("--behind --json = (%d, %q, %q), want three relics marked behind", code, stdout, stderr)
	}
	if all := mustRun(t, repository, "relic", "list", "--json"); strings.Count(all, `"behind":true`) != 3 || strings.Contains(all, `"behind":false`) {
		t.Fatalf("relic list --json = %s, want behind only on the three", all)
	}

	mustRelic(t, repository, nil, "delete", onOne)
	if got := listedIDs(relicListOf(t, repository, "--behind")); mustJSON(t, got) != mustJSON(t, []string{none, planBefore}) {
		t.Fatalf("--behind after a delete = %v", got)
	}
	if got := listedIDs(relicListOf(t, repository, "--behind", "--all")); mustJSON(t, got) != mustJSON(t, []string{none, planBefore, onOne}) {
		t.Fatalf("--behind --all = %v", got)
	}

	// --linked-to combines with --behind, and numbers each schema version.
	task := decodeMutationTask(t, mustRun(t, repository, "create", "Linked", "--json"), "create")
	for _, relicID := range []string{none, newest, planNewest} {
		mustRelic(t, repository, nil, "update", relicID, "--link", task.ID, "--rel", "relates-to")
	}
	linked := linkedToTask(t, repository, task.ID)
	if len(linked) != 3 || linked[1].ID != newest || linked[1].Schema == nil || linked[1].Schema.Version != 2 ||
		linked[1].RelicConformance == nil || linked[1].State != core.RelicConformanceConforms {
		t.Fatalf("--linked-to = %s, want the newest spec relic numbered 2 and conforming", mustJSON(t, linked))
	}
	if got := listedIDs(linkedToTask(t, repository, task.ID, "--behind")); mustJSON(t, got) != mustJSON(t, []string{none}) {
		t.Fatalf("--linked-to --behind = %v, want %s", got, none)
	}
}

// The relic list, conformance and behind included, is the same from a warm
// index, after the index is deleted, and after a rebuild.
func TestConformanceSurvivesDeletingTheIndexFile(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustCreateRelic(t, repository, "Before any schema", "--type", "spec")
	mustPutSchema(t, repository, "spec", ownerSchemaTwo)
	mustCreateRelic(t, repository, "--from-file", ownedSpecFile(t))
	mustCreateRelic(t, repository, "Open", "--type", "note")
	want := mustRun(t, repository, "relic", "list", "--all", "--json")
	if !strings.Contains(want, `"conformance":"conforms"`) || !strings.Contains(want, `"conformance":"open"`) {
		t.Fatalf("list = %s", want)
	}
	if got := mustRun(t, repository, "relic", "list", "--all", "--json"); got != want {
		t.Fatalf("warm list = %s, want %s", got, want)
	}
	if err := os.Remove(relicCachePath(t, repository)); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, repository, "relic", "list", "--all", "--json"); got != want {
		t.Fatalf("list after deleting the index = %s, want %s", got, want)
	}
	mustRun(t, repository, "rebuild")
	if got := mustRun(t, repository, "relic", "list", "--all", "--json"); got != want {
		t.Fatalf("list after a rebuild = %s, want %s", got, want)
	}
}
