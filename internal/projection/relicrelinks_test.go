package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// relicTargetRelics writes a relic X of three parts and three relics linking
// to it: A from the whole relic to X with a pointer, and to a task with a
// pointer; B from its first part to X#P1; C from the whole relic to X#P2.
// It returns X, X's part IDs, the linking relics and the task.
func relicTargetRelics(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) (string, []string, []string, string) {
	t.Helper()
	service := relicServiceForIndex(repository, config, 2000)
	task := linkIndexTask(t, repository, config, 3)
	x := createIndexRelic(t, service, "Target", 3)
	parts := []string{relicPartIDOf(t, repository, config, x, 0), relicPartIDOf(t, repository, config, x, 1), relicPartIDOf(t, repository, config, x, 2)}
	var linking []string
	for _, title := range []string{"A", "B", "C"} {
		linking = append(linking, createIndexRelic(t, service, title, 1))
	}
	linkRelic(t, service, linking[0],
		core.RelicEdit{Kind: core.RelicEditAddLink, Target: x, Rel: "supersedes", Pointer: json.RawMessage(`{"quote":"all of it"}`)},
		core.RelicEdit{Kind: core.RelicEditAddLink, Target: task, Rel: "implemented-by", Pointer: json.RawMessage(`{"at":"00:00:01"}`)})
	linkRelic(t, service, linking[1], core.RelicEdit{Kind: core.RelicEditAddLink, Target: x + "#" + parts[1],
		From: relicPartIDOf(t, repository, config, linking[1], 0), Rel: "decided-in"})
	linkRelic(t, service, linking[2], core.RelicEdit{Kind: core.RelicEditAddLink, Target: x + "#" + parts[2], Rel: "changes"})
	return x, parts, linking, task
}

// relicTargetRowsFromGit is what relic_links must hold for every link of
// every relic, whatever it points at, as Git stores it.
func relicTargetRowsFromGit(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) string {
	t.Helper()
	ctx := context.Background()
	heads, err := repository.ListRelicHeads(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	for _, head := range heads {
		snapshot, err := repository.GetRelic(ctx, config, head.RelicID)
		if err != nil {
			t.Fatal(err)
		}
		for _, link := range snapshot.Record.Links.Links {
			kind, to := "task", link.To.Task
			if link.To.Relic != "" {
				kind, to = "relic", link.To.Relic
			}
			fmt.Fprintf(&rows, "%s %s %q %s %s %q %s %q %q\n",
				head.RelicID, link.ID, link.From, kind, to, link.To.Part, link.Rel, link.Seen, string(link.Pointer))
		}
	}
	return rows.String()
}

func relicTargetRowsInIndex(t *testing.T, store *Store) string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT relic_id, link_id, from_part, to_kind, to_id, to_part, rel, seen, pointer FROM relic_links ORDER BY relic_id, link_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var dump strings.Builder
	for rows.Next() {
		var relicID, linkID, from, kind, to, toPart, rel, seen, pointer string
		if err := rows.Scan(&relicID, &linkID, &from, &kind, &to, &toPart, &rel, &seen, &pointer); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&dump, "%s %s %q %s %s %q %s %q %q\n", relicID, linkID, from, kind, to, toPart, rel, seen, pointer)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return dump.String()
}

// targetLookupFromGit is the reverse lookup to target computed from every
// relic's tip in Git.
func targetLookupFromGit(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, target core.RelicLinkTarget) string {
	t.Helper()
	ctx := context.Background()
	heads, err := repository.ListRelicHeads(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	lookup := core.RelicLinkLookup{}
	for _, head := range heads {
		snapshot, err := repository.GetRelic(ctx, config, head.RelicID)
		if err != nil {
			t.Fatal(err)
		}
		lookup.Sources = append(lookup.Sources, core.RelicLinkSourcesOf(snapshot, target)...)
	}
	core.SortRelicLinkSources(lookup.Sources)
	return mustLookupJSON(t, lookup)
}

func relicTargetLinksTo(t *testing.T, reader RelicIndex, config core.ProjectConfig, target core.RelicLinkTarget) core.RelicLinkLookup {
	t.Helper()
	lookup, err := reader.RelicLinksTo(context.Background(), config, target)
	if err != nil {
		t.Fatalf("RelicLinksTo(%s) error = %v", target.Address(), err)
	}
	return lookup
}

func TestRelicTargetsAreIndexed(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ctx := context.Background()
		x, parts, linking, task := relicTargetRelics(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		runRelicPass(t, store)
		if got, want := relicTargetRowsInIndex(t, store), relicTargetRowsFromGit(t, repository, config); got != want {
			t.Fatalf("relic_links holds\n%s\nwant\n%s", got, want)
		}
		targets := []core.RelicLinkTarget{{Relic: x}, {Relic: x, Part: parts[0]}, {Relic: x, Part: parts[1]}, {Relic: x, Part: parts[2]}, {Task: task}}
		for _, target := range targets {
			got := mustLookupJSON(t, relicTargetLinksTo(t, store.RelicReader(), config, target))
			if want := targetLookupFromGit(t, repository, config, target); got != want {
				t.Fatalf("RelicLinksTo(%s) =\n%s\nwant\n%s", target.Address(), got, want)
			}
		}
		if got := relicTargetLinksTo(t, store.RelicReader(), config, core.RelicLinkTarget{Relic: x}).Sources; len(got) != 3 {
			t.Fatalf("RelicLinksTo(X) = %+v, want three sources", got)
		}
		one := relicTargetLinksTo(t, store.RelicReader(), config, core.RelicLinkTarget{Relic: x, Part: parts[1]}).Sources
		if len(one) != 1 || one[0].Relic.ID != linking[1] || one[0].Link.To != (core.RelicLinkTarget{Relic: x, Part: parts[1]}) ||
			one[0].PartOrdinal != 0 || one[0].Link.Seen == "" {
			t.Fatalf("RelicLinksTo(X#P1) = %+v, want B's one link from its part", one)
		}
		if none := relicTargetLinksTo(t, store.RelicReader(), config, core.RelicLinkTarget{Relic: x, Part: parts[0]}); len(none.Sources) != 0 {
			t.Fatalf("RelicLinksTo(X#P0) = %+v, want nothing", none)
		}
		// An indexed snapshot holds every link as Git does, pointers and all.
		reader := store.RelicReader()
		for _, relicID := range append([]string{x}, linking...) {
			indexed, err := reader.GetRelic(ctx, config, relicID)
			if err != nil {
				t.Fatal(err)
			}
			fromGit, err := repository.GetRelic(ctx, config, relicID)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := mustJSONText(t, indexed.Record.Links), mustJSONText(t, fromGit.Record.Links); got != want {
				t.Fatalf("indexed links of %s = %s, want %s", relicID, got, want)
			}
		}
	})
}

func mustJSONText(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRelicTargetLookupIsOneQuery(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	x, parts, _, _ := relicTargetRelics(t, repository, config)
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	queries := countingQueries(t, store)

	queries.Store(0)
	runRelicPass(t, store)
	pass := queries.Load()
	queries.Store(0)
	if err := store.withActiveDatabase(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check := queries.Load()

	source.reset()
	queries.Store(0)
	lookup := relicTargetLinksTo(t, store.RelicReader(), config, core.RelicLinkTarget{Relic: x, Part: parts[1]})
	if len(lookup.Sources) != 1 {
		t.Fatalf("RelicLinksTo(X#P1) = %+v, want one source", lookup)
	}
	if got := queries.Load(); got != pass+check+1 {
		t.Fatalf("a warm RelicLinksTo(X#P1) ran %d queries; its pass runs %d and a cache check %d, so want %d",
			got, pass, check, pass+check+1)
	}
	if source.listCalls != 1 || source.relicCalls() != 1 {
		t.Fatalf("a warm RelicLinksTo(X#P1) made %d relic calls, want one ListRelicHeads", source.relicCalls())
	}
}

func TestRelicTargetLookupFromGitEqualsTheIndex(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	x, parts, _, task := relicTargetRelics(t, repository, config)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	for _, target := range []core.RelicLinkTarget{{Relic: x}, {Relic: x, Part: parts[1]}, {Relic: x, Part: parts[2]}, {Task: task}, {Relic: "WB-R-" + relicIndexTestID(999999)}} {
		indexed := mustLookupJSON(t, relicTargetLinksTo(t, store.RelicReader(), config, target))
		reader := &relicIndexReader{store: store, judging: true}
		fromGit, err := reader.relicLinksFromGit(ctx, config, target)
		if err != nil {
			t.Fatal(err)
		}
		if got := mustLookupJSON(t, fromGit); got != indexed {
			t.Fatalf("RelicLinksTo(%s) from Git =\n%s\nfrom the index\n%s", target.Address(), got, indexed)
		}
	}
}

// A version 11 cache has relic_links rows with no seen or pointer, and none
// for a link to a relic, so it is discarded and Git read again.
func TestPreviousSchemaVersionCacheIsReplaced(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	relicTargetRelics(t, repository, config)
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schema+`;
		ALTER TABLE relic_links DROP COLUMN seen; ALTER TABLE relic_links DROP COLUMN pointer;
		INSERT INTO projection_meta (key, value) VALUES ('schema_version', '11'), ('project_id', ?)`, config.ProjectID); err != nil {
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
		t.Fatal("a version 11 cache is usable")
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
	runRelicPass(t, store)
	if got, want := relicTargetRowsInIndex(t, store), relicTargetRowsFromGit(t, repository, config); got != want {
		t.Fatalf("relic_links after the replacement holds\n%s\nwant\n%s", got, want)
	}
}

// relicWriteCounter adds triggers to store's cache that count every row
// written to or removed from a relic table, and returns a function that reads
// the count.
func relicWriteCounter(t *testing.T, store *Store) func() int {
	t.Helper()
	statements := []string{`CREATE TABLE test_relic_writes (n INTEGER NOT NULL)`}
	for _, table := range []string{"relics", "relic_properties", "relic_parts", "relic_operations", "relic_links"} {
		for _, event := range []string{"INSERT", "DELETE", "UPDATE"} {
			statements = append(statements, fmt.Sprintf(
				`CREATE TRIGGER test_%s_%s AFTER %s ON %s BEGIN INSERT INTO test_relic_writes (n) VALUES (1); END`,
				table, strings.ToLower(event), event, table))
		}
	}
	for _, statement := range statements {
		execCache(t, store, statement)
	}
	return func() int {
		return countRows(t, store, `SELECT COUNT(*) FROM test_relic_writes`)
	}
}

// F2: a relic that holds links to relics, once indexed, is not indexed again:
// a second pass reads no relic from Git and writes no row, through a relic
// read and through the pass a task command runs.
func TestIndexedRelicLinksAreNotIndexedAgain(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	x, _, linking, task := relicTargetRelics(t, repository, config)
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	writes := relicWriteCounter(t, store)

	for _, step := range []struct {
		name string
		read func() error
	}{
		{"a relic read", func() error {
			reader := store.RelicReader()
			for _, relicID := range append([]string{x}, linking...) {
				if _, err := reader.GetRelic(ctx, config, relicID); err != nil {
					return err
				}
			}
			_, err := reader.RelicLinksTo(ctx, config, core.RelicLinkTarget{Relic: x})
			return err
		}},
		{"a task command's pass", func() error {
			_, err := store.RelicReaderForTasks().RelicLinksTo(ctx, config, core.RelicLinkTarget{Task: task})
			return err
		}},
	} {
		source.reset()
		before := writes()
		if err := step.read(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		source.mu.Lock()
		indexCalls, operationCalls, getCalls := source.indexCalls, source.operationCalls, source.getCalls
		source.mu.Unlock()
		if indexCalls != 0 || operationCalls != 0 || getCalls != 0 {
			t.Fatalf("%s read relics from Git: %d index reads, %d operation reads, %d relic reads, want none",
				step.name, indexCalls, operationCalls, getCalls)
		}
		if got := writes() - before; got != 0 {
			t.Fatalf("%s wrote %d relic rows, want none", step.name, got)
		}
	}
}
