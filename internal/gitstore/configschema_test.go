package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// schemaLookup is the newest version of each type in repo, as `schema put`
// compiles against it.
func schemaLookup(t *testing.T, repo *Repository, config core.ProjectConfig) core.RelicSchemaLookup {
	t.Helper()
	return func(relicType string) (core.RelicSchemaVersion, *core.RelicSchema, bool, error) {
		listing, err := repo.RelicSchemaListing(context.Background(), config)
		if err != nil {
			return core.RelicSchemaVersion{}, nil, false, err
		}
		version, ok := listing.Newest(relicType)
		if !ok {
			return core.RelicSchemaVersion{}, nil, false, nil
		}
		_, schema, err := repo.ReadRelicSchema(context.Background(), config, version)
		return version, schema, err == nil, err
	}
}

func putSchema(t *testing.T, repo *Repository, config core.ProjectConfig, relicType, source string) (core.RelicSchemaVersion, bool) {
	t.Helper()
	document, _, err := core.CompileRelicSchema(relicType, []byte(source), schemaLookup(t, repo, config))
	if err != nil {
		t.Fatalf("compile %s: %v", relicType, err)
	}
	version, changed, err := repo.WriteRelicSchema(context.Background(), config, core.CryptoULIDSource{}, document, "")
	if err != nil {
		t.Fatalf("WriteRelicSchema(%s) error = %v", relicType, err)
	}
	return version, changed
}

func treeLines(t *testing.T, repo *Repository, commit string) []string {
	t.Helper()
	return strings.Split(gitOutput(t, repo, "ls-tree", commit), "\n")
}

func treeHasEntry(lines []string, objectID, name string) bool {
	for _, line := range lines {
		if strings.HasSuffix(line, " "+objectID+"\t"+name) {
			return true
		}
	}
	return false
}

func TestSchemaVersionRoundTripsThroughGit(t *testing.T) {
	forBothSyncFormats(t, func(t *testing.T, repo, _ *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		empty, err := repo.RelicSchemaListing(ctx, config)
		if err != nil || len(empty.Versions) != 0 {
			t.Fatalf("listing with no ledger = %+v, %v", empty, err)
		}
		plan, changed := putSchema(t, repo, config, "plan", `{"$defs":{"step":{"type":"string"}}}`)
		if !changed || plan.Version != 1 {
			t.Fatalf("plan = %+v, changed %t", plan, changed)
		}
		first, _ := putSchema(t, repo, config, "spec", `{"properties":{"required":["status"],"properties":{"next":{"$ref":"plan#/$defs/step"}}}}`)
		second, _ := putSchema(t, repo, config, "spec", `{"properties":{"required":["owner"]}}`)
		if first.Version != 1 || second.Version != 2 || first.ID == second.ID {
			t.Fatalf("versions = %+v, %+v", first, second)
		}
		if len(second.Blob) != len(refValue(t, repo, configRef)) {
			t.Errorf("blob %q is not a full object ID of this repository's format", second.Blob)
		}

		tip := refValue(t, repo, configRef)
		lines := treeLines(t, repo, tip)
		if !treeHasEntry(lines, second.Blob, "relic-schema-"+second.ID) || len(lines) != 3 {
			t.Errorf("tip tree = %v, want operation.json, state.json and relic-schema-%s", lines, second.ID)
		}
		if parent := treeLines(t, repo, tip+"^"); !treeHasEntry(parent, first.Blob, "relic-schema-"+first.ID) {
			t.Errorf("the first version's commit tree = %v", parent)
		}

		listing, err := openSyncCloneAt(t, repo.Root).RelicSchemaListing(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		if len(listing.Versions) != 3 || listing.Versions[1] != first || listing.Versions[2] != second {
			t.Fatalf("listing = %+v", listing.Versions)
		}

		fresh := openSyncCloneAt(t, repo.Root)
		document, schema, err := fresh.ReadRelicSchema(ctx, config, first)
		if err != nil {
			t.Fatalf("ReadRelicSchema() error = %v", err)
		}
		if document.Type != "spec" || document.References["plan"] != plan.ID {
			t.Errorf("document = %+v", document)
		}
		if problem := schema.CheckPropertyClear("status"); problem == nil {
			t.Error("spec version 1 lost its required status")
		}
		if problem := schema.CheckProperty("next", []byte(`3`)); problem == nil || problem.Keyword != "type" {
			t.Errorf("the pinned plan rule = %v", problem)
		}
		_, cached, err := fresh.ReadRelicSchema(ctx, config, first)
		if err != nil || cached != schema {
			t.Errorf("a second read compiled again: %v", err)
		}

		var parent *core.ConfigStateDocument
		for _, commit := range configChain(t, repo, config) {
			state := commit.State
			if err := core.ValidateConfigCheckpoint(parent, commit.Operation, state); err != nil {
				t.Errorf("checkpoint %s: %v", commit.ObjectID, err)
			}
			parent = &state
		}
	})
}

func TestConfigTreeEntriesMatchSchemaPuts(t *testing.T) {
	forBothSyncFormats(t, func(t *testing.T, repo, _ *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		version, _ := putSchema(t, repo, config, "spec", `{"properties":{"required":["status"]}}`)
		putTip := refValue(t, repo, configRef)
		writeConfig(t, repo, config, relabelOperation("ready", "Up next"))
		plainTip := refValue(t, repo, configRef)
		name := "relic-schema-" + version.ID
		other := forgeRelicBlob(t, repo, []byte("not the schema\n"))

		forged := map[string]string{
			"an entry no put names": forgeRelicCommitOf(t, repo,
				forgeRelicTree(t, repo, append(treeLines(t, repo, plainTip), "100644 blob "+version.Blob+"\trelic-schema-01MA0000000000000000000000")),
				putTip),
			"a put without its entry": forgeRelicCommitOf(t, repo,
				forgeRelicTree(t, repo, replaceRelicTreeEntry(t, treeLines(t, repo, putTip), name, "")),
				putTip+"^"),
			"an entry with another blob": forgeRelicCommitOf(t, repo,
				forgeRelicTree(t, repo, replaceRelicTreeEntry(t, treeLines(t, repo, putTip), name, "100644 blob "+other+"\t"+name)),
				putTip+"^"),
			"an entry that is a tree": forgeRelicCommitOf(t, repo,
				forgeRelicTree(t, repo, replaceRelicTreeEntry(t, treeLines(t, repo, putTip), name,
					"040000 tree "+gitOutput(t, repo, "rev-parse", putTip+"^{tree}")+"\t"+name)),
				putTip+"^"),
		}
		for label, commit := range forged {
			gitOutput(t, repo, "update-ref", configRef, commit)
			_, err := openSyncCloneAt(t, repo.Root).LoadVocabularyState(ctx, config)
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Errorf("%s: LoadVocabularyState() = %v, want corrupt data", label, err)
			}
		}
		gitOutput(t, repo, "update-ref", configRef, plainTip)
		if _, err := openSyncCloneAt(t, repo.Root).LoadVocabularyState(ctx, config); err != nil {
			t.Errorf("the real tip: %v", err)
		}
	})
}

func TestReadingSettingsReadsNoSchemaBlob(t *testing.T) {
	forBothSyncFormats(t, func(t *testing.T, repo, _ *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		first, _ := putSchema(t, repo, config, "spec", `{"properties":{"required":["status"]}}`)
		second, _ := putSchema(t, repo, config, "plan", `{"properties":{"required":["status"]}}`)
		for _, blob := range []string{first.Blob, second.Blob} {
			path := gitOutput(t, repo, "rev-parse", "--git-path", "objects/"+blob[:2]+"/"+blob[2:])
			if !filepath.IsAbs(path) {
				path = filepath.Join(repo.Root, path)
			}
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove the loose schema blob: %v", err)
			}
		}

		fresh := openSyncCloneAt(t, repo.Root)
		state, err := fresh.LoadVocabularyState(ctx, config)
		if err != nil {
			t.Fatalf("LoadVocabularyState() error = %v", err)
		}
		if state.Priorities.Default() == "" || len(state.Keys.Active()) == 0 {
			t.Errorf("state = %+v", state)
		}
		if _, err := fresh.keySet(ctx, config); err != nil {
			t.Errorf("keySet() error = %v", err)
		}
		listing, err := fresh.RelicSchemaListing(ctx, config)
		if err != nil || len(listing.Versions) != 2 {
			t.Errorf("RelicSchemaListing() = %+v, %v", listing, err)
		}
		_, _, err = fresh.ReadRelicSchema(ctx, config, first)
		if core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(err.Error(), "spec version 1") {
			t.Errorf("ReadRelicSchema() = %v, want corrupt data naming spec version 1", err)
		}
	})
}

func syncConfigResultOf(result SyncRunResult) *SyncConfigResult {
	if result.Config != nil {
		return result.Config
	}
	return result.Fetch.Config
}

func TestConcurrentSchemaPutsBothSurviveReplay(t *testing.T) {
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		putSchema(t, first, config, "plan", `{"title":"Plan"}`)
		if _, err := first.Sync(ctx, config); err != nil {
			t.Fatalf("Sync(first) error = %v", err)
		}
		if _, err := second.Fetch(ctx, config); err != nil {
			t.Fatalf("Fetch(second) error = %v", err)
		}

		theirs, _ := putSchema(t, first, config, "spec", `{"title":"Theirs"}`)
		if _, err := first.Sync(ctx, config); err != nil {
			t.Fatalf("Sync(first) error = %v", err)
		}
		ours, _ := putSchema(t, second, config, "spec", `{"title":"Ours"}`)
		if theirs.Version != 1 || ours.Version != 1 {
			t.Fatalf("before sync: theirs %d, ours %d", theirs.Version, ours.Version)
		}

		result, err := openSyncCloneAt(t, second.Root).Sync(ctx, config)
		if err != nil {
			t.Fatalf("Sync(second) error = %v; result = %#v", err, result)
		}
		configResult := syncConfigResultOf(result)
		if configResult == nil || len(configResult.Warnings) != 1 {
			t.Fatalf("config result = %#v, want one warning", configResult)
		}
		warning := configResult.Warnings[0]
		wantMessage := "the schema for spec saved here as version 1 is now version 2; a teammate saved version 1 first"
		if warning.Code != core.WarningRelicSchemaRenumbered || warning.Message != wantMessage {
			t.Errorf("warning = %+v", warning)
		}

		replayed := refValue(t, second, configRef)
		if !treeHasEntry(treeLines(t, second, replayed), ours.Blob, "relic-schema-"+ours.ID) {
			t.Errorf("the replayed commit's tree lacks its schema blob: %v", treeLines(t, second, replayed))
		}
		if _, err := first.Fetch(ctx, config); err != nil {
			t.Fatalf("Fetch(first) error = %v", err)
		}
		for label, repo := range map[string]*Repository{"first": first, "second": second} {
			listing, err := openSyncCloneAt(t, repo.Root).RelicSchemaListing(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			one, ok1 := listing.Number("spec", 1)
			two, ok2 := listing.Number("spec", 2)
			if !ok1 || !ok2 || one.ID != theirs.ID || two.ID != ours.ID {
				t.Errorf("%s: listing = %+v", label, listing.Versions)
			}
			if _, _, err := openSyncCloneAt(t, repo.Root).ReadRelicSchema(ctx, config, two); err != nil {
				t.Errorf("%s: read the replayed version: %v", label, err)
			}
		}

		third, _ := putSchema(t, openSyncCloneAt(t, second.Root), config, "spec", `{"title":"Third"}`)
		if third.Version != 3 {
			t.Errorf("the next version after sync = %d, want 3", third.Version)
		}
		if _, err := openSyncCloneAt(t, second.Root).Sync(ctx, config); err != nil {
			t.Fatal(err)
		}
		if _, err := first.Fetch(ctx, config); err != nil {
			t.Fatal(err)
		}
		listing, err := openSyncCloneAt(t, first.Root).RelicSchemaListing(ctx, config)
		if newest, _ := listing.Newest("spec"); err != nil || newest.Version != 3 || newest.ID != third.ID {
			t.Errorf("first's newest spec = %+v, %v", newest, err)
		}
	})
}

func TestIdenticalSchemaPutWritesNothing(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	source := `{"properties":{"required":["status"]}}`
	version, changed := putSchema(t, repo, config, "spec", source)
	if !changed {
		t.Fatal("the first put wrote nothing")
	}
	commits := len(configChain(t, repo, config))
	again, changed := putSchema(t, repo, config, "spec", source)
	if changed || again != version {
		t.Errorf("the same document again = %+v, changed %t; want %+v unchanged", again, changed, version)
	}
	if got := len(configChain(t, repo, config)); got != commits {
		t.Errorf("ledger grew from %d to %d commits", commits, got)
	}
	other, changed := putSchema(t, repo, config, "spec", `{"properties":{"required":["owner"]}}`)
	if !changed || other.Version != 2 {
		t.Errorf("a different document = %+v, changed %t", other, changed)
	}
	back, changed := putSchema(t, repo, config, "spec", source)
	if !changed || back.Version != 3 {
		t.Errorf("going back to version 1's document = %+v, changed %t; want a new version 3", back, changed)
	}
}
