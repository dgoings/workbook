package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// Schemas the conformance tests save: spec 1 lets a relic have an owner, and
// spec 2 requires one.
const (
	conformanceSpecOne = `{"properties":{"type":"object","properties":{"owner":{"type":"string"}}}}`
	conformanceSpecTwo = `{"properties":{"type":"object","required":["owner"],"properties":{"owner":{"type":"string"}}}}`
)

// putIndexSchema saves a schema that references no other type.
func putIndexSchema(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, relicType, source string) core.RelicSchemaVersion {
	t.Helper()
	document, _, err := core.CompileRelicSchema(relicType, []byte(source), func(string) (core.RelicSchemaVersion, *core.RelicSchema, bool, error) {
		return core.RelicSchemaVersion{}, nil, false, nil
	})
	if err != nil {
		t.Fatalf("compile %s: %v", relicType, err)
	}
	version, _, err := repository.WriteRelicSchema(context.Background(), config, core.CryptoULIDSource{}, document, "")
	if err != nil {
		t.Fatalf("WriteRelicSchema(%s) error = %v", relicType, err)
	}
	return version
}

// extraSchemaStore is the repository's schema store with one more version
// listed, which the repository's own listing lacks: a relic written under it
// names a version every other reader finds unknown.
type extraSchemaStore struct {
	repository *gitstore.Repository
	extra      core.RelicSchemaVersion
}

func (s extraSchemaStore) RelicSchemaListing(ctx context.Context, config core.ProjectConfig) (core.RelicSchemaListing, error) {
	listing, err := s.repository.RelicSchemaListing(ctx, config)
	listing.Versions = append(listing.Versions, s.extra)
	return listing, err
}

func (s extraSchemaStore) ReadRelicSchema(ctx context.Context, config core.ProjectConfig, version core.RelicSchemaVersion) (core.RelicSchemaDocument, *core.RelicSchema, error) {
	if version.ID == s.extra.ID {
		document, schema, err := core.CompileRelicSchema(version.Type, []byte(`{}`), nil)
		return document, schema, err
	}
	return s.repository.ReadRelicSchema(ctx, config, version)
}

// conformanceFixture writes one relic in each conformance state and returns
// their IDs: open, conforms, not-conforming and unknown-schema.
func conformanceFixture(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) (open, conforms, broken, unknown string) {
	t.Helper()
	ctx := context.Background()
	putIndexSchema(t, repository, config, "spec", conformanceSpecOne)
	putIndexSchema(t, repository, config, "spec", conformanceSpecTwo)
	service := relicServiceForIndex(repository, config, 3000)
	service.Schemas = repository
	create := func(title, relicType string, properties map[string]json.RawMessage) string {
		t.Helper()
		created, err := service.Create(ctx, core.RelicCreateInput{Title: title, Type: relicType, Properties: properties,
			Parts: []core.RelicPartInput{{Type: "section", Fields: map[string]json.RawMessage{"heading": relicIndexJSON(t, title)}}}})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", title, err)
		}
		return created.Relic.ID
	}
	owned := map[string]json.RawMessage{"owner": relicIndexJSON(t, "ana")}
	open = create("Open", "note", nil)
	conforms = create("Conforms", "spec", owned)
	broken = create("Broken", "spec", owned)
	// An update not checked against the schema, as two valid edits meeting
	// in a sync leave the relic, clears the required owner.
	unchecked := service
	unchecked.Schemas = nil
	if _, err := unchecked.Update(ctx, broken, []core.RelicEdit{{Kind: core.RelicEditClearProperty, Name: "owner"}}); err != nil {
		t.Fatalf("clear owner: %v", err)
	}
	withExtra := service
	withExtra.Schemas = extraSchemaStore{repository: repository, extra: core.RelicSchemaVersion{
		Type: "spec", Version: 3, ID: relicIndexTestID(3999), Bytes: 2, Blob: strings.Repeat("0", 40),
	}}
	created, err := withExtra.Create(ctx, core.RelicCreateInput{Title: "Unknown", Type: "spec", Schema: relicIndexTestID(3999)})
	if err != nil {
		t.Fatalf("Create(unknown) error = %v", err)
	}
	return open, conforms, broken, created.Relic.ID
}

// relicConformanceRows is each relic row's schema, conformance and problem.
func relicConformanceRows(t *testing.T, store *Store) map[string][3]string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT relic_id, schema, conformance, schema_problem FROM relics`)
	if err != nil {
		t.Fatalf("query conformance: %v", err)
	}
	defer rows.Close()
	found := map[string][3]string{}
	for rows.Next() {
		var relicID string
		var row [3]string
		if err := rows.Scan(&relicID, &row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		found[relicID] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

// listedJSON is relic list's answer, with deleted relics, through reader.
func listedJSON(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, reader core.RelicReader) string {
	t.Helper()
	service := relicServiceForIndex(repository, config, 900000)
	service.Reader, service.Schemas = reader, repository
	listed, err := service.List(context.Background(), core.RelicListFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	return string(relicIndexJSON(t, listed))
}

func TestIndexStoresConformance(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		open, conforms, broken, unknown := conformanceFixture(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		runRelicPass(t, store)

		listing, err := repository.RelicSchemaListing(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		specTwo, _ := listing.Number("spec", 2)
		problem := core.RelicSchemaRefusal(specTwo, *mustCheck(t, repository, config, specTwo))
		wantProblem := strings.TrimPrefix(problem.Error(), "relic does not fit schema spec version 2: ")
		rows := relicConformanceRows(t, store)
		want := map[string][3]string{
			open:     {"", core.RelicConformanceOpen, ""},
			conforms: {specTwo.ID, core.RelicConformanceConforms, ""},
			broken:   {specTwo.ID, core.RelicConformanceNotConforming, wantProblem},
			unknown:  {relicIndexTestID(3999), core.RelicConformanceUnknownSchema, ""},
		}
		for relicID, wanted := range want {
			if rows[relicID] != wanted {
				t.Errorf("row %s = %q, want %q", relicID, rows[relicID], wanted)
			}
		}
		if !strings.Contains(wantProblem, `property "owner"`) || !strings.HasSuffix(wantProblem, "(required)") {
			t.Errorf("problem = %q, want the owner required", wantProblem)
		}
		if dump := dumpRelicTables(t, store); !strings.Contains(dump, `conformance="not-conforming" schema_problem=`) {
			t.Errorf("dumpRelicTables leaves out the conformance columns:\n%s", dump)
		}
		if got, wantList := listedJSON(t, repository, config, store.RelicReader()), listedJSON(t, repository, config, repository); got != wantList {
			t.Errorf("index list =\n%s\nGit list =\n%s", got, wantList)
		}
	})
}

// mustCheck is the first problem spec version 2 finds with a relic that has
// no owner.
func mustCheck(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, version core.RelicSchemaVersion) *core.RelicSchemaProblem {
	t.Helper()
	_, schema, err := repository.ReadRelicSchema(context.Background(), config, version)
	if err != nil {
		t.Fatal(err)
	}
	problem := schema.CheckRelic(core.RelicData{Properties: map[string]json.RawMessage{}}, nil)
	if problem == nil {
		t.Fatal("a relic with no owner fits spec version 2")
	}
	return problem
}

// hidingSchemaSource is the counting source whose listing leaves out the
// versions in hidden, as a clone that has a relic but not yet the settings
// that list its version.
type hidingSchemaSource struct {
	*countingRelicSource
	hidden map[string]bool
}

func (s *hidingSchemaSource) RelicSchemaListing(ctx context.Context, config core.ProjectConfig) (core.RelicSchemaListing, error) {
	listing, err := s.countingRelicSource.RelicSchemaListing(ctx, config)
	kept := listing.Versions[:0:0]
	for _, version := range listing.Versions {
		if !s.hidden[version.ID] {
			kept = append(kept, version)
		}
	}
	listing.Versions = kept
	return listing, err
}

func TestUnknownSchemaRowIsRejudgedAfterSync(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	version := putIndexSchema(t, repository, config, "spec", conformanceSpecTwo)
	service := relicServiceForIndex(repository, config, 4000)
	service.Schemas = repository
	created, err := service.Create(ctx, core.RelicCreateInput{Title: "Later", Type: "spec",
		Properties: map[string]json.RawMessage{"owner": relicIndexJSON(t, "ana")}})
	if err != nil {
		t.Fatal(err)
	}
	other := createIndexRelicOfType(t, relicServiceForIndex(repository, config, 4500), "spec", "Other", 1)
	relicID := created.Relic.ID

	source := &hidingSchemaSource{countingRelicSource: newCountingRelicSource(repository), hidden: map[string]bool{version.ID: true}}
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	before := relicConformanceRows(t, store)
	if got := before[relicID]; got[1] != core.RelicConformanceUnknownSchema {
		t.Fatalf("row before the settings arrive = %q, want unknown-schema", got)
	}
	head := countRows(t, store, `SELECT COUNT(*) FROM relics WHERE relic_id = ? AND head = ?`, relicID, created.Relic.Head)
	if head != 1 {
		t.Fatalf("row does not hold the relic's head")
	}

	source.hidden = map[string]bool{}
	source.reset()
	runRelicPass(t, store)
	after := relicConformanceRows(t, store)
	if got := after[relicID]; got != [3]string{version.ID, core.RelicConformanceConforms, ""} {
		t.Fatalf("row after the settings arrive = %q, want conforms", got)
	}
	if after[other] != before[other] {
		t.Errorf("the other relic's row moved: %q → %q", before[other], after[other])
	}
	if countRows(t, store, `SELECT COUNT(*) FROM relics WHERE relic_id = ? AND head = ?`, relicID, created.Relic.Head) != 1 {
		t.Fatal("re-judging moved the row's head")
	}
	if len(source.indexRequests) != 1 || len(source.indexRequests[0]) != 1 || source.indexRequests[0][0].Head.RelicID != relicID {
		t.Fatalf("the second pass read %+v from Git, want only %s", source.indexRequests, relicID)
	}

	// With every row judged, a third pass reads no relic.
	source.reset()
	runRelicPass(t, store)
	if source.indexCalls != 0 {
		t.Fatalf("a pass over judged rows read %+v", source.indexRequests)
	}
}

// A pass for a task command does not judge: a changed relic that names a
// schema is written as not yet judged, and the next relic read judges it.
func TestTaskPassLeavesJudgingToTheNextRelicRead(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	version := putIndexSchema(t, repository, config, "spec", conformanceSpecTwo)
	service := relicServiceForIndex(repository, config, 5000)
	service.Schemas = repository
	created, err := service.Create(ctx, core.RelicCreateInput{Title: "Linked", Type: "spec",
		Properties: map[string]json.RawMessage{"owner": relicIndexJSON(t, "ana")}})
	if err != nil {
		t.Fatal(err)
	}
	// Created with no schema store, so it names no version and is open.
	open := createIndexRelicOfType(t, relicServiceForIndex(repository, config, 6000), "spec", "Open", 1)
	source := &hidingSchemaSource{countingRelicSource: newCountingRelicSource(repository)}
	listings := &countingListingSource{hidingSchemaSource: source}
	store := openRelicStore(t, listings, config, filepath.Join(t.TempDir(), "cache.sqlite"))

	if _, err := store.RelicReaderForTasks().RelicLinksTo(ctx, config, core.RelicLinkTarget{Task: "WB-01M9B000000000000000000001"}); err != nil {
		t.Fatalf("RelicLinksTo() error = %v", err)
	}
	if listings.listings != 0 || listings.schemaReads != 0 {
		t.Fatalf("a task pass read the schema listing %d times and %d schemas", listings.listings, listings.schemaReads)
	}
	rows := relicConformanceRows(t, store)
	if rows[created.Relic.ID] != [3]string{version.ID, "", ""} || rows[open][1] != core.RelicConformanceOpen {
		t.Fatalf("rows after a task pass = %q", rows)
	}
	if got, want := listedJSON(t, repository, config, store.RelicReader()), listedJSON(t, repository, config, repository); got != want {
		t.Fatalf("index list =\n%s\nGit list =\n%s", got, want)
	}
	if rows := relicConformanceRows(t, store); rows[created.Relic.ID][1] != core.RelicConformanceConforms {
		t.Fatalf("row after a relic read = %q, want conforms", rows[created.Relic.ID])
	}
}

// countingListingSource counts the schema listings and documents read.
type countingListingSource struct {
	*hidingSchemaSource
	listings, schemaReads int
}

func (s *countingListingSource) RelicSchemaListing(ctx context.Context, config core.ProjectConfig) (core.RelicSchemaListing, error) {
	s.listings++
	return s.hidingSchemaSource.RelicSchemaListing(ctx, config)
}

func (s *countingListingSource) ReadRelicSchema(ctx context.Context, config core.ProjectConfig, version core.RelicSchemaVersion) (core.RelicSchemaDocument, *core.RelicSchema, error) {
	s.schemaReads++
	return s.hidingSchemaSource.ReadRelicSchema(ctx, config, version)
}

// A project with no schema reads no schema on a relic pass.
func TestRelicPassWithNoSchemaReadsNone(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	createIndexRelic(t, relicServiceForIndex(repository, config, 7000), "Plain", 2)
	listings := &countingListingSource{hidingSchemaSource: &hidingSchemaSource{countingRelicSource: newCountingRelicSource(repository)}}
	store := openRelicStore(t, listings, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	if listings.schemaReads != 0 {
		t.Fatalf("a pass read %d schemas in a project with none", listings.schemaReads)
	}
	if rows := relicConformanceRows(t, store); len(rows) != 1 {
		t.Fatalf("rows = %q", rows)
	}
	for _, row := range relicConformanceRows(t, store) {
		if row != [3]string{"", core.RelicConformanceOpen, ""} {
			t.Fatalf("row = %q, want open", row)
		}
	}
}

func TestSchemaVersionTenCacheIsReplaced(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A cache as version 10 wrote it: no conformance columns.
	old, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schema+`;
		ALTER TABLE relics DROP COLUMN conformance; ALTER TABLE relics DROP COLUMN schema_problem;
		INSERT INTO projection_meta (key, value) VALUES ('schema_version', '10'), ('project_id', ?)`, config.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openForTest(t, ctx, repository, config)
	if err != nil {
		t.Fatal(err)
	}
	if store.cacheUsable(ctx) {
		t.Fatal("a version 10 cache is usable")
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM projection_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || schemaVersion != "13" {
		t.Fatalf("schema version = %s (built %s), want 13", version, schemaVersion)
	}
	countRows(t, store, `SELECT COUNT(conformance) + COUNT(schema_problem) FROM relics`)
}

// Conformance survives deleting the cache and rebuilding it: the relics
// list the same before, after the cache is deleted, and after a rebuild.
func TestConformanceSurvivesDeletingTheIndex(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		conformanceFixture(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		want := listedJSON(t, repository, config, repository)
		if got := listedJSON(t, repository, config, store.RelicReader()); got != want {
			t.Fatalf("warm list =\n%s\nwant\n%s", got, want)
		}
		if err := os.Remove(store.CachePath()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if got := listedJSON(t, repository, config, store.RelicReader()); got != want {
			t.Fatalf("list after deleting the cache =\n%s\nwant\n%s", got, want)
		}
		if _, err := store.RebuildWithRelics(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := listedJSON(t, repository, config, store.RelicReader()); got != want {
			t.Fatalf("list after a rebuild =\n%s\nwant\n%s", got, want)
		}
		for _, state := range []string{"open", "conforms", "not-conforming", "unknown-schema"} {
			if !strings.Contains(want, `"conformance":"`+state+`"`) {
				t.Errorf("the list has no relic %s:\n%s", state, want)
			}
		}
	})
}
