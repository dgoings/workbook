package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"modernc.org/sqlite"
)

// linkIndexTask creates task number n with IDs of its own.
func linkIndexTask(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, n int) string {
	t.Helper()
	ids := make([]string, 6)
	for index := range ids {
		ids[index] = relicIndexTestID(700000 + n*10 + index)
	}
	return createTaskWithIDs(t, repository, config, fmt.Sprintf("Task %d", n), ids...).ID
}

func linkRelic(t *testing.T, service core.RelicService, relicID string, edits ...core.RelicEdit) {
	t.Helper()
	if _, err := service.Update(context.Background(), relicID, edits); err != nil {
		t.Fatalf("Update(%s) error = %v", relicID, err)
	}
}

func relicPartIDOf(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, relicID string, index int) string {
	t.Helper()
	snapshot, err := repository.GetRelic(context.Background(), config, relicID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Record.State.Relic.Parts[index].ID
}

// linkedRelics writes three relics that link to two tasks: relic 0 from the
// whole relic to task 0 and from its first part to task 1, relic 1 from its
// second part to task 0, and relic 2 from the whole relic to task 1.
func linkedRelics(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) (core.RelicService, []string, []string) {
	t.Helper()
	service := relicServiceForIndex(repository, config, 1000)
	tasks := []string{linkIndexTask(t, repository, config, 1), linkIndexTask(t, repository, config, 2)}
	var relics []string
	for number := range 3 {
		relics = append(relics, createIndexRelic(t, service, fmt.Sprintf("Linked %d", number), 3))
	}
	linkRelic(t, service, relics[0],
		core.RelicEdit{Kind: core.RelicEditAddLink, Target: tasks[0], Rel: "relates-to"},
		core.RelicEdit{Kind: core.RelicEditAddLink, Target: tasks[1], From: relicPartIDOf(t, repository, config, relics[0], 0), Rel: "implemented-by"})
	linkRelic(t, service, relics[1],
		core.RelicEdit{Kind: core.RelicEditAddLink, Target: tasks[0], From: relicPartIDOf(t, repository, config, relics[1], 1), Rel: "decided-in"})
	linkRelic(t, service, relics[2], core.RelicEdit{Kind: core.RelicEditAddLink, Target: tasks[1], Rel: "relates-to"})
	return service, relics, tasks
}

// linkRowsFromGit is what relic_links must hold: one row per live link of
// every relic, as Git stores it.
func linkRowsFromGit(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) string {
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
			fmt.Fprintf(&rows, "%s %s %q task %s %q %s\n", head.RelicID, link.ID, link.From, link.To.Task, "", link.Rel)
		}
	}
	return rows.String()
}

func linkRowsInIndex(t *testing.T, store *Store) string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT relic_id, link_id, from_part, to_kind, to_id, to_part, rel FROM relic_links ORDER BY relic_id, link_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var dump strings.Builder
	for rows.Next() {
		var relicID, linkID, from, kind, to, toPart, rel string
		if err := rows.Scan(&relicID, &linkID, &from, &kind, &to, &toPart, &rel); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&dump, "%s %s %q %s %s %q %s\n", relicID, linkID, from, kind, to, toPart, rel)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return dump.String()
}

// lookupFromGit is the reverse lookup computed from every relic's tip in Git.
func lookupFromGit(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, taskID string) string {
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
		lookup.Sources = append(lookup.Sources, core.RelicLinkSourcesOf(snapshot, core.RelicLinkTarget{Task: taskID})...)
	}
	core.SortRelicLinkSources(lookup.Sources)
	return mustLookupJSON(t, lookup)
}

func mustLookupJSON(t *testing.T, lookup core.RelicLinkLookup) string {
	t.Helper()
	if lookup.Sources == nil {
		lookup.Sources = []core.RelicLinkSource{}
	}
	if lookup.Unread == nil {
		lookup.Unread = []string{}
	}
	encoded, err := json.Marshal(lookup)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func relicLinksTo(t *testing.T, store *Store, config core.ProjectConfig, taskID string) core.RelicLinkLookup {
	t.Helper()
	lookup, err := store.RelicReader().RelicLinksTo(context.Background(), config, core.RelicLinkTarget{Task: taskID})
	if err != nil {
		t.Fatalf("RelicLinksTo(%s) error = %v", taskID, err)
	}
	return lookup
}

func assertLinkIndexMatchesGit(t *testing.T, label string, store *Store, repository *gitstore.Repository, config core.ProjectConfig, tasks []string) {
	t.Helper()
	runRelicPass(t, store)
	if got, want := linkRowsInIndex(t, store), linkRowsFromGit(t, repository, config); got != want {
		t.Fatalf("%s: relic_links holds\n%s\nwant\n%s", label, got, want)
	}
	for _, taskID := range tasks {
		if got, want := mustLookupJSON(t, relicLinksTo(t, store, config, taskID)), lookupFromGit(t, repository, config, taskID); got != want {
			t.Fatalf("%s: RelicLinksTo(%s) =\n%s\nwant\n%s", label, taskID, got, want)
		}
	}
}

func TestRelicLinksAreIndexedByBothEnds(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		_, relics, tasks := linkedRelics(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		assertLinkIndexMatchesGit(t, "indexed", store, repository, config, tasks)

		first := relicLinksTo(t, store, config, tasks[0])
		var got []string
		for _, source := range first.Sources {
			got = append(got, fmt.Sprintf("%s %d %s", source.Relic.ID, source.PartOrdinal, source.Link.Rel))
		}
		want := []string{relics[0] + " -1 relates-to", relics[1] + " 1 decided-in"}
		if !slices.Equal(got, want) {
			t.Fatalf("RelicLinksTo(task 0) = %q, want %q", got, want)
		}
		if source := first.Sources[1]; source.PartType != "section" || source.PartRemoved || source.Relic.Parts != 3 {
			t.Fatalf("part source = %+v", source)
		}
		if got := countRows(t, store, `SELECT link_count FROM relics WHERE relic_id = ?`, relics[0]); got != 2 {
			t.Fatalf("link_count = %d, want 2", got)
		}
		if none := relicLinksTo(t, store, config, "WB-"+relicIndexTestID(999999)); len(none.Sources) != 0 || len(none.Unread) != 0 {
			t.Fatalf("RelicLinksTo(no such task) = %+v, want nothing", none)
		}
	})
}

// countingQueries reopens store's database through a driver that counts
// queries, and returns the counter.
func countingQueries(t *testing.T, store *Store) *atomic.Int64 {
	t.Helper()
	var queries atomic.Int64
	driverName := fmt.Sprintf("workbook-counting-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, &queryCountingDriver{inner: &sqlite.Driver{}, queries: &queries})
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, store.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	store.db = db
	return &queries
}

func TestRelicLinksToIsOneQuery(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	_, _, tasks := linkedRelics(t, repository, config)
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	queries := countingQueries(t, store)

	queries.Store(0)
	runRelicPass(t, store)
	pass := queries.Load()
	// Each use of the active cache checks it first, and the lookup's query is
	// a second use after the pass.
	queries.Store(0)
	if err := store.withActiveDatabase(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check := queries.Load()

	source.reset()
	queries.Store(0)
	lookup := relicLinksTo(t, store, config, tasks[0])
	if len(lookup.Sources) != 2 {
		t.Fatalf("RelicLinksTo = %+v, want two sources", lookup)
	}
	if got := queries.Load(); got != pass+check+1 {
		t.Fatalf("a warm RelicLinksTo ran %d queries; its pass runs %d and a cache check %d, so want %d",
			got, pass, check, pass+check+1)
	}
	if source.listCalls != 1 || source.relicCalls() != 1 {
		t.Fatalf("a warm RelicLinksTo made %d relic calls, want one ListRelicHeads", source.relicCalls())
	}
}

func TestRelicLinkRowsFollowEachChange(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	service, relics, tasks := linkedRelics(t, repository, config)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	assertLinkIndexMatchesGit(t, "start", store, repository, config, tasks)

	snapshot, err := repository.GetRelic(ctx, config, relics[0])
	if err != nil {
		t.Fatal(err)
	}
	linkRelic(t, service, relics[0], core.RelicEdit{Kind: core.RelicEditRemoveLink, Link: snapshot.Record.Links.Links[0].ID})
	assertLinkIndexMatchesGit(t, "after an unlink", store, repository, config, tasks)

	linkRelic(t, service, relics[1], core.RelicEdit{Kind: core.RelicEditRemovePart, Part: relicPartIDOf(t, repository, config, relics[1], 1)})
	assertLinkIndexMatchesGit(t, "after a part removal", store, repository, config, tasks)
	if source := relicLinksTo(t, store, config, tasks[0]).Sources; len(source) != 1 || !source[0].PartRemoved {
		t.Fatalf("sources after a part removal = %+v, want the one link from the removed part", source)
	}

	if _, err := service.Delete(ctx, relics[2]); err != nil {
		t.Fatal(err)
	}
	assertLinkIndexMatchesGit(t, "after a relic delete", store, repository, config, tasks)
	if source := relicLinksTo(t, store, config, tasks[1]).Sources; len(source) != 2 || !source[1].Relic.Deleted {
		t.Fatalf("sources after a relic delete = %+v, want the deleted relic's link marked", source)
	}

	before := dumpRelicTables(t, store)
	execCache(t, store, `DELETE FROM relic_links WHERE relic_id = ?`, relics[0])
	assertLinkIndexMatchesGit(t, "after a link row is lost", store, repository, config, tasks)
	if got := dumpRelicTables(t, store); got != before {
		t.Fatalf("after the repair the index holds\n%s\nwant\n%s", got, before)
	}
}

func TestDamagedRelicLinksAreReportedUnread(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service, relics, tasks := linkedRelics(t, repository, config)
	updateIndexRelicTitle(t, service, relics[1], "Changed")
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if _, err := store.RebuildWithRelics(context.Background()); err != nil {
		t.Fatal(err)
	}
	damageRelic(t, repository, relics[1])
	for _, attempt := range []string{"first", "again"} {
		lookup := relicLinksTo(t, store, config, tasks[0])
		if !slices.Equal(lookup.Unread, []string{relics[1]}) {
			t.Fatalf("%s: Unread = %q, want %q", attempt, lookup.Unread, relics[1])
		}
		if len(lookup.Sources) != 1 || lookup.Sources[0].Relic.ID != relics[0] {
			t.Fatalf("%s: Sources = %+v, want the valid relic's one link", attempt, lookup.Sources)
		}
	}
}

// With no usable index, a reverse lookup reads every relic from Git, and a
// relic Git refuses is unread there too.
func TestRelicLinksToAnswersFromGitWithNoUsableIndex(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service, relics, tasks := linkedRelics(t, repository, config)
	updateIndexRelicTitle(t, service, relics[1], "Changed")
	var want core.RelicLinkLookup
	if err := json.Unmarshal([]byte(lookupFromGit(t, repository, config, tasks[1])), &want); err != nil {
		t.Fatal(err)
	}
	// The damaged relic cannot be read, so it is unread for every task.
	want.Unread = []string{relics[1]}
	damageRelic(t, repository, relics[1])
	damageTaskTip(t, repository, config)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err := os.Remove(store.CachePath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if got := mustLookupJSON(t, relicLinksTo(t, store, config, tasks[1])); got != mustLookupJSON(t, want) {
		t.Fatalf("RelicLinksTo with no usable index =\n%s\nwant\n%s", got, mustLookupJSON(t, want))
	}
	lookup := relicLinksTo(t, store, config, tasks[0])
	if !slices.Equal(lookup.Unread, []string{relics[1]}) || len(lookup.Sources) != 1 {
		t.Fatalf("RelicLinksTo(task 0) with no usable index = %+v", lookup)
	}
}

// deleteIndexTask tombstones a task through the repository.
func deleteIndexTask(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, taskID string) {
	t.Helper()
	var next atomic.Int64
	service := core.Service{
		Config: config, Reader: repository, Writer: repository, Actor: "test@example.test",
		Now: func() time.Time { return relicIndexTime },
		IDs: core.IDSourceFunc(func() (string, error) {
			return relicIndexTestID(800000 + int(next.Add(1))), nil
		}),
	}
	if _, err := service.DeleteMutation(context.Background(), taskID, core.DeleteInput{}); err != nil {
		t.Fatalf("DeleteMutation(%s) error = %v", taskID, err)
	}
}

// relicLinkMutations links and unlinks the relics of relicIndexMutations, one
// change each.
func relicLinkMutations(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, service core.RelicService, ids []string) []func() {
	t.Helper()
	task := linkIndexTask(t, repository, config, 9)
	links := func(relicID string) []core.RelicLink {
		t.Helper()
		snapshot, err := repository.GetRelic(context.Background(), config, relicID)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Record.Links.Links
	}
	return []func(){
		func() {
			linkRelic(t, service, ids[1], core.RelicEdit{Kind: core.RelicEditAddLink, Target: task, Rel: "relates-to"})
		},
		func() {
			linkRelic(t, service, ids[2], core.RelicEdit{Kind: core.RelicEditAddLink, Target: task,
				From: relicPartIDOf(t, repository, config, ids[2], 1), Rel: "implemented-by"})
		},
		func() {
			linkRelic(t, service, ids[1], core.RelicEdit{Kind: core.RelicEditAddLink, Target: task,
				From: relicPartIDOf(t, repository, config, ids[1], 0), Rel: "decided-in"})
		},
		func() {
			linkRelic(t, service, ids[1], core.RelicEdit{Kind: core.RelicEditRemoveLink, Link: links(ids[1])[0].ID})
		},
	}
}

// A link row of a compacted relic, from a stub or from the relic, carries the
// compaction and the drop as Git does, from the index alone.
func TestRelicLinksToCompactedRelicsComeFromTheIndex(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		retired, _ := compactionIndexFixture(t, repository, config)
		compacted := compactIndexRelic(t, repository, config, retired, core.RelicBoundaryRetired)
		task := compacted.Record.Links.Links[0].To.Task
		// A relic never compacted links from a part to the same task.
		service := relicServiceForIndex(repository, config, 80000)
		plain := createIndexRelic(t, service, "Never compacted", 1)
		linkRelic(t, service, plain, core.RelicEdit{
			Kind: core.RelicEditAddLink, Target: task, From: relicPartIDOf(t, repository, config, plain, 0), Rel: "relates-to",
		})
		source := newCountingRelicSource(repository)
		store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		assertLinkIndexMatchesGit(t, "compacted", store, repository, config, []string{task})

		source.reset()
		lookup := relicLinksTo(t, store, config, task)
		if source.listCalls != 1 || source.relicCalls() != 1 {
			t.Fatalf("a warm RelicLinksTo made %d relic calls, want one ListRelicHeads", source.relicCalls())
		}
		var whole, stubbed, never int
		for _, found := range lookup.Sources {
			switch {
			case found.Relic.ID == plain && found.Relic.Compacted == nil && found.Dropped == nil:
				never++
			case found.Relic.ID == retired && found.Relic.Compacted != nil && found.Link.From == "" && found.Dropped == nil:
				whole++
			case found.Relic.ID == retired && found.Relic.Compacted != nil && found.Link.From != "" && found.Dropped != nil:
				stubbed++
			}
		}
		if len(lookup.Sources) != 3 || whole != 1 || stubbed != 1 || never != 1 {
			t.Fatalf("RelicLinksTo = %+v, want the relic's link, the stub's and the live part's, each with its own members", lookup.Sources)
		}
	})
}
