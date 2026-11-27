package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

func (s *countingRelicSource) RelicChain(ctx context.Context, config core.ProjectConfig, head core.RelicHead) (core.RelicChain, error) {
	s.mu.Lock()
	s.chainCalls++
	s.mu.Unlock()
	return s.repositorySource.RelicChain(ctx, config, head)
}

func (s *countingRelicSource) chains() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chainCalls
}

// assertIndexChainsEqualGit requires a fresh reader on store to answer every
// relic's chain exactly as the repository does, and returns how many chains
// the reader read from Git once its pass had run. With fromIndex set, it
// requires that to be none.
func assertIndexChainsEqualGit(
	t *testing.T,
	label string,
	store *Store,
	source *countingRelicSource,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	fromIndex bool,
) int {
	t.Helper()
	ctx := context.Background()
	heads, err := repository.ListRelicHeads(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) == 0 {
		t.Fatalf("%s: no relics", label)
	}
	reader := store.RelicReader()
	if _, err := reader.ListRelicHeads(ctx, config); err != nil {
		t.Fatalf("%s: ListRelicHeads() error = %v", label, err)
	}
	source.reset()
	chainsBefore := source.chains()
	for _, head := range heads {
		got, err := reader.RelicChain(ctx, config, head)
		if err != nil {
			t.Fatalf("%s: index RelicChain(%s) error = %v", label, head.RelicID, err)
		}
		want, err := repository.RelicChain(ctx, config, head)
		if err != nil {
			t.Fatalf("%s: Git RelicChain(%s) error = %v", label, head.RelicID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: index chain of %s =\n%+v\nwant Git's\n%+v", label, head.RelicID, got, want)
		}
	}
	fromGit := source.chains() - chainsBefore
	if fromIndex && (source.operationCalls != 0 || fromGit != 0) {
		t.Fatalf("%s: the chains were read from Git (%d operation reads, %d chain reads), want the index",
			label, source.operationCalls, fromGit)
	}
	return fromGit
}

func TestRelicChainFromIndexEqualsGit(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceForIndex(repository, config, 1000)
		first := createIndexRelic(t, service, "First", 3)
		second := createIndexRelic(t, service, "Second", 2)
		source := newCountingRelicSource(repository)
		cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
		store := openRelicStore(t, source, config, cachePath)
		assertIndexChainsEqualGit(t, "created", store, source, repository, config, true)

		updateIndexRelicTitle(t, service, first, "First, <again> & again")
		if _, err := service.Update(ctx, second, []core.RelicEdit{{
			Kind: core.RelicEditSetProperty, Name: "note", Value: json.RawMessage(`"<a&b>"`),
		}}); err != nil {
			t.Fatal(err)
		}
		updateIndexRelicTitle(t, service, first, "First, a third time")
		assertIndexChainsEqualGit(t, "extended", store, source, repository, config, true)
		if got := countRows(t, store, `SELECT COUNT(DISTINCT chain_index) FROM relic_operations WHERE relic_id = ?`, first); got != 3 {
			t.Fatalf("the index holds %d commits of %s, want 3", got, first)
		}

		// A replay moves the head to a sibling of the old one, and the rows
		// are replaced.
		head := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(first))
		sibling := relicIndexGit(t, repository, nil, "commit-tree", head+"^{tree}", "-p", head+"^", "-m", "replayed")
		relicIndexGit(t, repository, nil, "update-ref", relicRefOf(first), sibling)
		assertIndexChainsEqualGit(t, "replayed", store, source, repository, config, true)
		if got := countRows(t, store, `SELECT COUNT(*) FROM relic_operations WHERE commit_id = ?`, head); got != 0 {
			t.Fatalf("%d rows still name the replaced head", got)
		}

		rebuilt := newCountingRelicSource(repository)
		fresh := openRelicStore(t, rebuilt, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		if _, err := fresh.RebuildWithRelics(ctx); err != nil {
			t.Fatal(err)
		}
		assertIndexChainsEqualGit(t, "rebuilt", fresh, rebuilt, repository, config, true)
	})
}

// A warm chain read is the reader's pass and one query.
func TestRelicChainIsOneQuery(t *testing.T) {
	t.Parallel()
	repository, config := initializeRelicWorkbook(t, testrepo.FormatSHA1)
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Counted", 3)
	updateIndexRelicTitle(t, service, relicID, "Counted again")
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

	head := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(relicID))
	source.reset()
	chainsBefore := source.chains()
	queries.Store(0)
	chain, err := store.RelicReader().RelicChain(context.Background(), config, core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Commits) != 2 {
		t.Fatalf("chain has %d commits, want 2", len(chain.Commits))
	}
	if got := queries.Load(); got != pass+check+1 {
		t.Fatalf("a warm RelicChain ran %d queries; its pass runs %d and a cache check %d, so want %d", got, pass, check, pass+check+1)
	}
	if fromGit := source.chains() - chainsBefore; source.operationCalls != 0 || fromGit != 0 {
		t.Fatalf("a warm RelicChain read Git: %d operation reads, %d chain reads", source.operationCalls, fromGit)
	}
}

func TestRelicChainAnswersFromGitWhenRowsAreIncomplete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		damage func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, store *Store, relicID string)
	}{
		{"every row deleted", func(t *testing.T, _ *gitstore.Repository, _ core.ProjectConfig, store *Store, relicID string) {
			runRelicPass(t, store)
			execCache(t, store, `DELETE FROM relic_operations WHERE relic_id = ?`, relicID)
		}},
		{"the middle commit's rows deleted", func(t *testing.T, _ *gitstore.Repository, _ core.ProjectConfig, store *Store, relicID string) {
			runRelicPass(t, store)
			execCache(t, store, `DELETE FROM relic_operations WHERE relic_id = ? AND chain_index = 1`, relicID)
		}},
		{"one operation of a commit deleted", func(t *testing.T, _ *gitstore.Repository, _ core.ProjectConfig, store *Store, relicID string) {
			runRelicPass(t, store)
			execCache(t, store, `DELETE FROM relic_operations WHERE relic_id = ? AND chain_index = 0 AND pack_index = 1`, relicID)
		}},
		{"a damaged relic", func(t *testing.T, repository *gitstore.Repository, _ core.ProjectConfig, _ *Store, relicID string) {
			damageRelic(t, repository, relicID)
		}},
		{"an index that cannot be built", func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, _ *Store, _ string) {
			damageTaskTip(t, repository, config)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, config := initializeRelicWorkbook(t, testrepo.FormatSHA1)
			service := relicServiceForIndex(repository, config, 1000)
			relicID := createIndexRelic(t, service, "Incomplete", 3)
			updateIndexRelicTitle(t, service, relicID, "Incomplete, two")
			updateIndexRelicTitle(t, service, relicID, "Incomplete, three")
			source := newCountingRelicSource(repository)
			store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
			test.damage(t, repository, config, store, relicID)

			if fromGit := assertIndexChainsEqualGit(t, test.name, store, source, repository, config, false); fromGit != 1 {
				t.Fatalf("%d chain reads from Git, want 1", fromGit)
			}
		})
	}
}
