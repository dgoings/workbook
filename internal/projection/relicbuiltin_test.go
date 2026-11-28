package projection

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// migrateIndexRelic records a relic.migrate to schema, written as a teammate's
// build would have written it, with nothing checked.
func migrateIndexRelic(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, relicID string, number int, relicType, schema string) {
	t.Helper()
	changeIndexRelic(t, repository, config, relicID, core.RelicOperation{
		ID: relicIndexTestID(number), Type: core.RelicOperationMigrate, RelicType: relicType, Schema: schema,
	})
}

// builtInIndexFixture writes a spec that fits workbook:spec:1, a plan on
// workbook:plan:1 whose step's needs is a string, a spec naming the built-in
// workbook:spec:9 this build does not ship, and a note on a project version.
func builtInIndexFixture(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) (spec, plan, unknown, note string) {
	t.Helper()
	ctx := context.Background()
	putIndexSchema(t, repository, config, "note", `{}`)
	service := relicServiceForIndex(repository, config, 5000)
	service.Schemas = repository
	create := func(service core.RelicService, title, relicType, partType, fieldName string, field json.RawMessage) string {
		t.Helper()
		created, err := service.Create(ctx, core.RelicCreateInput{Title: title, Type: relicType,
			Properties: map[string]json.RawMessage{"status": relicIndexJSON(t, "draft")},
			Parts: []core.RelicPartInput{{Type: partType,
				Fields: map[string]json.RawMessage{"title": relicIndexJSON(t, title), fieldName: field}}}})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", title, err)
		}
		return created.Relic.ID
	}
	spec = create(service, "Spec", "spec", "section", "kind", relicIndexJSON(t, "intro"))
	unchecked := service
	unchecked.Schemas = nil
	plan = create(unchecked, "Plan", "plan", "step", "needs", relicIndexJSON(t, "Task 1"))
	migrateIndexRelic(t, repository, config, plan, 5900, "plan", "workbook:plan:1")
	unknown = create(unchecked, "Unknown", "spec", "section", "kind", relicIndexJSON(t, "intro"))
	migrateIndexRelic(t, repository, config, unknown, 5901, "spec", "workbook:spec:9")
	note = create(service, "Note", "note", "section", "kind", relicIndexJSON(t, "intro"))
	return spec, plan, unknown, note
}

// One pass judges a spec and a plan, both of which have an empty blob, each
// under its own built-in: no cache keyed by blob may hand one the other's.
func TestOnePassJudgesSpecAndPlanUnderTheirOwnBuiltIns(t *testing.T) {
	t.Parallel()
	repository, config := initializeRelicWorkbook(t, "sha1")
	spec, plan, _, _ := builtInIndexFixture(t, repository, config)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)

	rows := relicConformanceRows(t, store)
	if rows[spec] != [3]string{"workbook:spec:1", core.RelicConformanceConforms, ""} {
		t.Errorf("spec row = %q, want it to conform to workbook:spec:1", rows[spec])
	}
	planRow := rows[plan]
	if planRow[0] != "workbook:plan:1" || planRow[1] != core.RelicConformanceNotConforming ||
		!strings.Contains(planRow[2], "(step)") || !strings.Contains(planRow[2], "needs") || !strings.HasSuffix(planRow[2], "(type)") {
		t.Errorf("plan row = %q, want not-conforming naming the step's needs (type)", planRow)
	}
}

// relic list from a cold index, a warm one, Git alone and after the cache is
// deleted answers the same for relics on a built-in, an unknown built-in and
// a project version, and so do the summaries of the relics linking to one.
func TestIndexAnswersEqualGitForBuiltIns(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ctx := context.Background()
		spec, plan, unknown, note := builtInIndexFixture(t, repository, config)
		target := createIndexRelic(t, relicServiceForIndex(repository, config, 6000), "Target", 0)
		linker := relicServiceForIndex(repository, config, 6100)
		for _, relicID := range []string{spec, plan, unknown, note} {
			linkRelic(t, linker, relicID, core.RelicEdit{Kind: core.RelicEditAddLink, Target: target, Rel: "relates-to"})
		}

		gitList := listedJSON(t, repository, config, repository)
		for _, want := range []string{
			`"schema":{"type":"spec","id":"workbook:spec:1","builtIn":true},"conformance":"conforms"`,
			`"schema":{"type":"plan","id":"workbook:plan:1","builtIn":true},"conformance":"not-conforming"`,
			`"schema":{"type":"spec","id":"workbook:spec:9","builtIn":true},"conformance":"unknown-schema"`,
			`"schema":{"type":"note","id":"`,
		} {
			if !strings.Contains(gitList, want) {
				t.Errorf("Git list lacks %s:\n%s", want, gitList)
			}
		}
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		linked := func(label string, schemas core.RelicSchemaStore) string {
			t.Helper()
			service := relicServiceForIndex(repository, config, 900000)
			service.Reader, service.Links, service.Schemas = store.RelicReader(), store.RelicReader(), schemas
			summaries, _, err := service.LinkedTo(ctx, target, core.RelicListFilter{})
			if err != nil {
				t.Fatalf("%s: LinkedTo() error = %v", label, err)
			}
			for index := range summaries {
				summaries[index].LinkedBy = nil
			}
			encoded := string(relicIndexJSON(t, summaries))
			for _, id := range []string{"workbook:spec:1", "workbook:plan:1", "workbook:spec:9"} {
				if !strings.Contains(encoded, `"id":"`+id+`","builtIn":true`) {
					t.Errorf("%s: linked summaries do not mark %s built in:\n%s", label, id, encoded)
				}
			}
			return encoded
		}

		if got := listedJSON(t, repository, config, store.RelicReader()); got != gitList {
			t.Errorf("cold index list =\n%s\nGit list =\n%s", got, gitList)
		}
		if got := listedJSON(t, repository, config, store.RelicReader()); got != gitList {
			t.Errorf("warm index list =\n%s\nGit list =\n%s", got, gitList)
		}
		judged := linked("judged", repository)
		linked("with no schema store", nil)
		if err := os.Remove(store.CachePath()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if got := listedJSON(t, repository, config, store.RelicReader()); got != gitList {
			t.Errorf("list after deleting the cache =\n%s\nGit list =\n%s", got, gitList)
		}
		if got := linked("after deleting the cache", repository); got != judged {
			t.Errorf("linked after deleting the cache =\n%s\nbefore =\n%s", got, judged)
		}
	})
}
