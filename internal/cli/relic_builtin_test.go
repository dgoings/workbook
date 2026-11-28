package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// builtInSpecFile is a spec document that fits workbook:spec:1.
func builtInSpecFile(t *testing.T) string {
	t.Helper()
	return writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"Archive compaction","type":"spec",`+
		`"properties":{"status":"draft"},"parts":[`+
		`{"type":"section","fields":{"title":"Goals"},"body":{"format":"md","text":"Keep history small."}},`+
		`{"type":"decision","fields":{"title":"Where do checkpoints live?","chosen":"In the relic ref","status":"decided"}},`+
		`{"type":"open-question","fields":{"title":"How often?"}}]}`))
}

// builtInPlanFile is a plan document that fits workbook:plan:1.
func builtInPlanFile(t *testing.T) string {
	t.Helper()
	return writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"Build compaction","type":"plan",`+
		`"properties":{"status":"draft"},"parts":[`+
		`{"type":"section","fields":{"title":"Context"}},`+
		`{"type":"step","fields":{"title":"Task 1"}},`+
		`{"type":"step","fields":{"title":"Task 2","needs":["Task 1"]}}]}`))
}

func schemaListOutput(t *testing.T, repository string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "schema", "list", "--json")
	if code != 0 {
		t.Fatalf("schema list = (%d, %q, %q)", code, stdout, stderr)
	}
	return stdout
}

func requireOnBuiltIn(t *testing.T, label string, shown core.RelicDocument, id, state string) {
	t.Helper()
	if shown.Schema == nil || *shown.Schema != (core.RelicSchemaRef{Type: shown.Type, ID: id, BuiltIn: true}) ||
		shown.State != state {
		t.Fatalf("%s: schema %+v, conformance %+v; want %s on %s", label, shown.Schema, shown.RelicConformance, state, id)
	}
}

func TestBrandNewProjectWritesSpecAndPlan(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			before := schemaListOutput(t, repository)
			configBefore := gitOutput(t, repository, "rev-parse", "--verify", "--quiet", "refs/workbook/config")

			for _, test := range []struct{ file, id string }{
				{builtInSpecFile(t), "workbook:spec:1"},
				{builtInPlanFile(t), "workbook:plan:1"},
			} {
				created := mustCreateRelic(t, repository, "--from-file", test.file)
				if created.Relic.Schema == nil || !created.Relic.Schema.BuiltIn || created.Relic.Schema.ID != test.id {
					t.Fatalf("created relic names %+v, want %s", created.Relic.Schema, test.id)
				}
				var shown core.RelicDocument
				mustRelic(t, repository, &shown, "show", created.Relic.ID)
				requireOnBuiltIn(t, "relic show", shown, test.id, core.RelicConformanceConforms)
			}
			if after := schemaListOutput(t, repository); after != before {
				t.Errorf("schema list changed with the creates:\nbefore %s\nafter  %s", before, after)
			}
			if after := gitOutput(t, repository, "rev-parse", "--verify", "--quiet", "refs/workbook/config"); after != configBefore {
				t.Errorf("the creates moved the settings from %s to %s", configBefore, after)
			}
			if behind := relicListOf(t, repository, "--behind"); len(behind) != 0 {
				t.Errorf("relics on the built-ins are behind: %s", mustJSON(t, behind))
			}
		})
	}
}

// writeOpenRelicFromDocument writes the document as a relic that names no
// schema, as a build before the built-ins wrote it.
func writeOpenRelicFromDocument(t *testing.T, repository, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := core.DecodeRelicDocument(source)
	if err != nil {
		t.Fatalf("DecodeRelicDocument(%s) error = %v", path, err)
	}
	input, err := core.RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatalf("RelicCreateInputFromDocument(%s) error = %v", path, err)
	}
	fixture := openRelicFixture(t, repository)
	service := core.RelicService{Config: fixture.config, Reader: fixture.repo, Writer: fixture.repo,
		IDs: core.CryptoULIDSource{}, Actor: relicTestActor}
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	if created.Relic.Schema != nil {
		t.Fatalf("the relic written open names %+v", created.Relic.Schema)
	}
	return created.Relic.ID
}

func TestUpgradedProjectAdoptsTheDefaultSpec(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := writeOpenRelicFromDocument(t, repository, filepath.Join("..", "core", "testdata", "design-relic-shape.json"))

	behind := relicListOf(t, repository, "--behind")
	if len(behind) != 1 || behind[0].ID != relicID || behind[0].Schema != nil || behind[0].State != core.RelicConformanceOpen {
		t.Fatalf("--behind = %s, want the open spec", mustJSON(t, behind))
	}

	var migrated core.RelicMutationResult
	mustRelic(t, repository, &migrated, "migrate", relicID, "--to", "spec")
	if !migrated.Changed || migrated.Relic.Schema == nil ||
		*migrated.Relic.Schema != (core.RelicSchemaRef{Type: "spec", ID: "workbook:spec:1", BuiltIn: true}) ||
		migrated.Relic.RelicConformance == nil || migrated.Relic.State != core.RelicConformanceConforms {
		t.Fatalf("migrate --to spec = %s, want a change to workbook:spec:1 that conforms", mustJSON(t, migrated))
	}
	head := relicHeadOf(t, repository, relicID)
	var again core.RelicMutationResult
	mustRelic(t, repository, &again, "migrate", relicID, "--to", "spec")
	if again.Changed || relicHeadOf(t, repository, relicID) != head {
		t.Fatalf("a second migrate = %s, want nothing written", mustJSON(t, again))
	}
	if behind := relicListOf(t, repository, "--behind"); len(behind) != 0 {
		t.Fatalf("--behind after adopting the default = %s", mustJSON(t, behind))
	}
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", relicID)
	requireOnBuiltIn(t, "relic show after adopting", shown, "workbook:spec:1", core.RelicConformanceConforms)
}

// Review Focus 3: one clone saves its own spec schema and publishes it while
// another, not yet synced, writes a spec under the built-in. After both sync,
// the spec keeps its built-in, conforms, and is behind; nothing is refused.
func TestTakeoverMeetsAnOfflineBuiltInRelic(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			if objectFormat == testrepo.FormatSHA256 {
				testrepo.RequireObjectFormat(t, testrepo.FormatSHA256)
			}
			project := codeProject(t, objectFormat)
			first := project.seed
			second := cloneCode(t, project)
			if code, stdout, stderr := run(t, second, "setup"); code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}

			mustPutSchema(t, first, "spec", `{"properties":{"type":"object","properties":{"status":{"type":"string"}}}}`)
			syncedClone(t, first)
			offline := mustCreateRelic(t, second, "--from-file", builtInSpecFile(t), "--no-sync").Relic
			if offline.Schema == nil || offline.Schema.ID != "workbook:spec:1" {
				t.Fatalf("the offline spec names %+v, want workbook:spec:1", offline.Schema)
			}
			syncedClone(t, second)
			syncedClone(t, first)

			for name, clone := range map[string]string{"first": first, "second": second} {
				var shown core.RelicDocument
				mustRelic(t, clone, &shown, "show", offline.ID)
				requireOnBuiltIn(t, name+" clone's relic show", shown, "workbook:spec:1", core.RelicConformanceConforms)
				behind := relicListOf(t, clone, "--behind")
				if len(behind) != 1 || behind[0].ID != offline.ID {
					t.Fatalf("%s clone's --behind = %s, want the spec on the built-in", name, mustJSON(t, behind))
				}
				newer := mustCreateRelic(t, clone, "Newer "+name, "--type", "spec").Relic
				if newer.Schema == nil || newer.Schema.BuiltIn || newer.Schema.Version != 1 {
					t.Fatalf("%s clone's new spec names %+v, want the project's version 1", name, newer.Schema)
				}
			}
			mustRelic(t, second, nil, "update", offline.ID, "--set", "status=approved")
		})
	}
}

func TestNewerWriterRelicNamingABuiltInSyncsUnchanged(t *testing.T) {
	t.Parallel()
	first, second := schemaSharedClones(t)
	created := mustCreateRelic(t, first, "--from-file", builtInSpecFile(t)).Relic
	syncedClone(t, second)

	fixture := openRelicFixture(t, first)
	tip, err := fixture.repo.GetRelic(context.Background(), fixture.config, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tip.Record.State.MinReader != core.RelicBuiltInSchemaMinReader {
		t.Fatalf("a relic on a built-in declares generation %d, want %d", tip.Record.State.MinReader, core.RelicBuiltInSchemaMinReader)
	}
	forged := fixture.forgeNewerRelicCommit(t, tip, 700001)
	cliGit(t, first, "push", "--quiet", "origin", relicRef(created.ID))

	for _, step := range []struct{ clone, command string }{
		{second, "fetch"}, {second, "sync"}, {first, "fetch"}, {first, "sync"}, {second, "sync"},
	} {
		if code, stdout, stderr := run(t, step.clone, step.command); code != 0 {
			t.Fatalf("%s = (%d, %q, %q)", step.command, code, stdout, stderr)
		}
	}
	for name, clone := range map[string]string{"first": first, "second": second} {
		if head := relicHeadOf(t, clone, created.ID); head != forged {
			t.Fatalf("%s clone's relic is at %s, want the newer writer's %s", name, head, forged)
		}
		if head := originRelicHead(t, clone, created.ID); head != forged {
			t.Fatalf("origin's relic is at %s, want %s", head, forged)
		}
		// A newer writer's relic is shown as any other newer writer's relic
		// is: from its stored state, under the built-in it names.
		var shown core.RelicDocument
		mustRelic(t, clone, &shown, "show", created.ID)
		if shown.Head != forged || shown.Schema == nil || shown.Schema.ID != "workbook:spec:1" || !shown.Schema.BuiltIn {
			t.Fatalf("%s clone's relic show = head %s, schema %+v; want the newer writer's tip on workbook:spec:1", name, shown.Head, shown.Schema)
		}
		relicErrorMessage(t, clone, 9, core.CategoryNewerWriter, "update", created.ID, "--title", "Renamed", "--no-sync")
	}
}
