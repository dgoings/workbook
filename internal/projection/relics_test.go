package projection

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
	"modernc.org/sqlite"
)

const relicIndexActor = "workbook@example.test"

var relicIndexTime = time.Date(2026, time.November, 24, 10, 0, 0, 0, time.UTC)

// relicIndexTestID builds the canonical ULIDs these tests mint relics and
// operations with.
func relicIndexTestID(n int) string {
	return fmt.Sprintf("01M9B%021d", n)
}

// initializeRelicWorkbook returns an initialized project in objectFormat: a
// copy of the package template for SHA-1, and for SHA-256 a repository minted
// as mintWorkbookTemplate mints one.
func initializeRelicWorkbook(t *testing.T, objectFormat string) (*gitstore.Repository, core.ProjectConfig) {
	t.Helper()
	if objectFormat == testrepo.FormatSHA1 {
		return initializeWorkbook(t)
	}
	testrepo.RequireObjectFormat(t, objectFormat)
	dir := t.TempDir()
	if err := testrepo.InitAtWithObjectFormat(dir, objectFormat); err != nil {
		t.Fatalf("init %s repository: %v", objectFormat, err)
	}
	repository, err := gitstore.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, _, err := repository.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		return testConfig().ProjectID, nil
	}))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return repository, config
}

// forBothRelicIndexFormats runs one test body against a SHA-1 and a SHA-256
// project.
func forBothRelicIndexFormats(t *testing.T, run func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig)) {
	t.Helper()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository, config := initializeRelicWorkbook(t, objectFormat)
			run(t, repository, config)
		})
	}
}

// relicServiceForIndex wires a core.RelicService to a repository, minting IDs
// numbered from start, as gitstore's relicServiceFor does.
func relicServiceForIndex(repository *gitstore.Repository, config core.ProjectConfig, start int) core.RelicService {
	var mutex sync.Mutex
	next := start
	return core.RelicService{
		Config: config,
		Reader: repository,
		Writer: repository,
		Tasks:  repository,
		IDs: core.IDSourceFunc(func() (string, error) {
			mutex.Lock()
			defer mutex.Unlock()
			id := relicIndexTestID(next)
			next++
			return id, nil
		}),
		Now:   func() time.Time { return relicIndexTime },
		Actor: relicIndexActor,
	}
}

func relicIndexJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// createIndexRelic creates a relic of the given number of parts, each with a
// field of its own and every third without a body.
func createIndexRelic(t *testing.T, service core.RelicService, title string, parts int) string {
	t.Helper()
	input := core.RelicCreateInput{
		Title: title, Type: "spec",
		Properties: map[string]json.RawMessage{"owner": relicIndexJSON(t, title), "meta": json.RawMessage(`{"z":[1,{"a":null}]}`)},
	}
	for index := range parts {
		part := core.RelicPartInput{
			Type:   "section",
			Fields: map[string]json.RawMessage{"heading": relicIndexJSON(t, fmt.Sprintf("%s section %d", title, index))},
		}
		if index%3 != 2 {
			part.Body = &core.RelicBodyInput{Format: "md", Content: []byte(fmt.Sprintf("# %s %d\n", title, index))}
		}
		input.Parts = append(input.Parts, part)
	}
	result, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", title, err)
	}
	return result.Relic.ID
}

func updateIndexRelicTitle(t *testing.T, service core.RelicService, relicID, title string) {
	t.Helper()
	if _, err := service.Update(context.Background(), relicID, []core.RelicEdit{{Kind: core.RelicEditTitle, Title: title}}); err != nil {
		t.Fatalf("Update(%s) error = %v", relicID, err)
	}
}

func relicIndexGit(t *testing.T, repository *gitstore.Repository, input []byte, args ...string) string {
	t.Helper()
	output, err := repository.Git(context.Background(), input, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSuffix(string(output), "\n")
}

func relicRefOf(relicID string) string { return "refs/workbook/relics/" + relicID }

// countingRelicSource is the repository with a count of each relic call, and
// of what the two batch readers were asked.
type countingRelicSource struct {
	repositorySource
	mu               sync.Mutex
	listCalls        int
	tipCalls         int
	getCalls         int
	partCalls        int
	indexCalls       int
	operationCalls   int
	bodyCalls        int
	chainCalls       int
	indexRequests    [][]gitstore.RelicIndexRequest
	operationRequest [][]gitstore.RelicHistoryRequest
}

func newCountingRelicSource(repository *gitstore.Repository) *countingRelicSource {
	return &countingRelicSource{repositorySource: repositorySource{repository: repository}}
}

func (s *countingRelicSource) count(counter *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*counter++
}

func (s *countingRelicSource) relicCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls + s.tipCalls + s.getCalls + s.partCalls + s.indexCalls + s.operationCalls + s.bodyCalls
}

func (s *countingRelicSource) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls, s.tipCalls, s.getCalls, s.partCalls = 0, 0, 0, 0
	s.indexCalls, s.operationCalls, s.bodyCalls = 0, 0, 0
	s.indexRequests, s.operationRequest = nil, nil
}

func (s *countingRelicSource) ListRelicHeads(ctx context.Context, config core.ProjectConfig) ([]core.RelicHead, error) {
	s.count(&s.listCalls)
	return s.repositorySource.ListRelicHeads(ctx, config)
}

func (s *countingRelicSource) ReadRelicTips(ctx context.Context, config core.ProjectConfig, heads []core.RelicHead) ([]core.RelicSnapshot, error) {
	s.count(&s.tipCalls)
	return s.repositorySource.ReadRelicTips(ctx, config, heads)
}

func (s *countingRelicSource) GetRelic(ctx context.Context, config core.ProjectConfig, relicID string) (core.RelicSnapshot, error) {
	s.count(&s.getCalls)
	return s.repositorySource.GetRelic(ctx, config, relicID)
}

func (s *countingRelicSource) ReadRelicParts(
	ctx context.Context, config core.ProjectConfig, snapshot core.RelicSnapshot, partIDs []string, bodies bool,
) (map[string]core.RelicPartContent, error) {
	s.count(&s.partCalls)
	return s.repositorySource.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
}

func (s *countingRelicSource) ReadIndexedRelics(
	ctx context.Context, config core.ProjectConfig, requests []gitstore.RelicIndexRequest,
) ([]gitstore.IndexedRelic, []error) {
	s.mu.Lock()
	s.indexCalls++
	s.indexRequests = append(s.indexRequests, requests)
	s.mu.Unlock()
	return s.repositorySource.ReadIndexedRelics(ctx, config, requests)
}

func (s *countingRelicSource) ReadRelicOperations(
	ctx context.Context, config core.ProjectConfig, requests []gitstore.RelicHistoryRequest,
) ([]gitstore.RelicOperationsResult, error) {
	s.mu.Lock()
	s.operationCalls++
	s.operationRequest = append(s.operationRequest, requests)
	s.mu.Unlock()
	return s.repositorySource.ReadRelicOperations(ctx, config, requests)
}

func (s *countingRelicSource) ReadRelicBodies(
	ctx context.Context, config core.ProjectConfig, requests []gitstore.RelicBodyRequest,
) ([][]byte, error) {
	s.count(&s.bodyCalls)
	return s.repositorySource.ReadRelicBodies(ctx, config, requests)
}

// orderedRelicSource lists relic heads in reverse, so a pass meets the relics
// in the opposite order from the repository's.
type orderedRelicSource struct{ repositorySource }

func (s orderedRelicSource) ListRelicHeads(ctx context.Context, config core.ProjectConfig) ([]core.RelicHead, error) {
	heads, err := s.repositorySource.ListRelicHeads(ctx, config)
	slices.Reverse(heads)
	return heads, err
}

func openRelicStore(t *testing.T, source taskHeadSource, config core.ProjectConfig, cachePath string) *Store {
	t.Helper()
	store, err := openStoreForTest(t, context.Background(), source, config, cachePath)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	return store
}

// runRelicPass runs one relic pass as a relic read does.
func runRelicPass(t *testing.T, store *Store) relicPass {
	t.Helper()
	pass, err := tryRelicPass(store)
	if err != nil {
		t.Fatalf("relic pass error = %v", err)
	}
	return pass
}

func tryRelicPass(store *Store) (relicPass, error) {
	var pass relicPass
	err := store.withActiveDatabase(context.Background(), func(ctx context.Context) error {
		var err error
		pass, err = store.refreshRelicsActive(ctx)
		return err
	})
	return pass, err
}

func execCache(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// dumpRelicTables prints every relic table, every column, ordered by primary
// key.
func dumpRelicTables(t *testing.T, store *Store) string {
	t.Helper()
	var dump strings.Builder
	for _, query := range []string{
		`SELECT * FROM relics ORDER BY relic_id`,
		`SELECT * FROM relic_properties ORDER BY relic_id, name`,
		`SELECT * FROM relic_parts ORDER BY relic_id, part_id`,
		`SELECT * FROM relic_operations ORDER BY relic_id, chain_index, pack_index`,
		`SELECT * FROM relic_links ORDER BY relic_id, link_id`,
	} {
		fmt.Fprintf(&dump, "%s\n", query)
		dumpRows(t, store.db, &dump, query)
	}
	return dump.String()
}

func dumpRows(t *testing.T, db *sql.DB, dump *strings.Builder, query string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for index, value := range values {
			if value.Valid {
				fmt.Fprintf(dump, "%s=%q ", columns[index], value.String)
			} else {
				fmt.Fprintf(dump, "%s=NULL ", columns[index])
			}
		}
		dump.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func relicRowHeads(t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT relic_id, head FROM relics`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	heads := map[string]string{}
	for rows.Next() {
		var relicID, head string
		if err := rows.Scan(&relicID, &head); err != nil {
			t.Fatal(err)
		}
		heads[relicID] = head
	}
	return heads
}

func relicRefHeads(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) map[string]string {
	t.Helper()
	heads, err := repository.ListRelicHeads(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, head := range heads {
		byID[head.RelicID] = head.ObjectID
	}
	return byID
}

func countRows(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

// forgeRelicTipOn points relicID's ref at a commit on base whose tree is
// base's with operation.json and state.json replaced. Nothing checks it.
func forgeRelicTipOn(t *testing.T, repository *gitstore.Repository, relicID, base string, operationJSON, stateJSON []byte) string {
	t.Helper()
	blob := func(contents []byte) string {
		return relicIndexGit(t, repository, contents, "hash-object", "-w", "--stdin")
	}
	var entries []string
	for _, entry := range strings.Split(relicIndexGit(t, repository, nil, "ls-tree", base), "\n") {
		switch {
		case strings.HasSuffix(entry, "\toperation.json"):
			entry = "100644 blob " + blob(operationJSON) + "\toperation.json"
		case strings.HasSuffix(entry, "\tstate.json"):
			entry = "100644 blob " + blob(stateJSON) + "\tstate.json"
		}
		entries = append(entries, entry)
	}
	tree := relicIndexGit(t, repository, []byte(strings.Join(entries, "\n")+"\n"), "mktree")
	commit := relicIndexGit(t, repository, nil, "commit-tree", tree, "-p", base, "-m", "forged")
	relicIndexGit(t, repository, nil, "update-ref", relicRefOf(relicID), commit)
	return commit
}

// damageRelic gives relicID, which has one update on its root, a tip whose
// title change is not in its stored state.
func damageRelic(t *testing.T, repository *gitstore.Repository, relicID string) {
	t.Helper()
	tip := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(relicID))
	root := tip + "^"
	forgeRelicTipOn(t, repository, relicID, relicIndexGit(t, repository, nil, "rev-parse", root),
		[]byte(relicIndexGit(t, repository, nil, "cat-file", "blob", tip+":operation.json")+"\n"),
		[]byte(relicIndexGit(t, repository, nil, "cat-file", "blob", root+":state.json")+"\n"))
}

// makeNewerRelic puts on relicID's tip what a later Workbook writes when it
// retires the relic, marked generation 7.
func makeNewerRelic(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, relicID string) {
	t.Helper()
	tip := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(relicID))
	state := relicIndexGit(t, repository, nil, "cat-file", "blob", tip+":state.json") + "\n"
	var parsed struct {
		LogicalClock int `json:"logicalClock"`
		History      struct {
			Generation string `json:"generation"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(state), &parsed); err != nil {
		t.Fatal(err)
	}
	clock := parsed.LogicalClock
	for _, replacement := range [][2]string{
		{`"minReader":6`, `"minReader":7`},
		{fmt.Sprintf(`"logicalClock":%d`, clock), fmt.Sprintf(`"logicalClock":%d`, clock+1)},
		{`"retired":false`, `"retired":true`},
	} {
		if !strings.Contains(state, replacement[0]) {
			t.Fatalf("state holds no %s", replacement[0])
		}
		state = strings.Replace(state, replacement[0], replacement[1], 1)
	}
	operation := `{"format":"workbook.relic-operation-pack","version":1,"minReader":7,` +
		`"projectId":"` + config.ProjectID + `","relicId":"` + relicID + `",` +
		`"historyGeneration":"` + parsed.History.Generation + `","actor":{"id":"` + relicIndexActor + `"},` +
		fmt.Sprintf(`"logicalClock":%d,"wallTime":"2026-11-24T10:01:00Z",`, clock+1) +
		`"operations":[{"id":"` + relicIndexTestID(990001) + `","type":"relic.retire"}]}` + "\n"
	forgeRelicTipOn(t, repository, relicID, tip, []byte(operation), []byte(state))
}

func TestRelicPassIndexesEveryRelic(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceForIndex(repository, config, 1000)
		ids := []string{
			createIndexRelic(t, service, "Alpha", 3),
			createIndexRelic(t, service, "Beta", 0),
			createIndexRelic(t, service, "Gamma", 5),
		}
		updateIndexRelicTitle(t, service, ids[2], "Gamma again")
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))

		pass := runRelicPass(t, store)
		if len(pass.heads) != 3 || len(pass.fromGit) != 0 || len(pass.failed) != 0 {
			t.Fatalf("pass = %d heads, %d from Git, %d failed", len(pass.heads), len(pass.fromGit), len(pass.failed))
		}
		refs := relicRefHeads(t, repository, config)
		if rows := relicRowHeads(t, store); fmt.Sprint(rows) != fmt.Sprint(refs) {
			t.Fatalf("row heads = %v, want the refs %v", rows, refs)
		}
		for _, relicID := range ids {
			snapshot, err := repository.GetRelic(ctx, config, relicID)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := store.db.QueryContext(ctx,
				`SELECT part_id, ordinal, body_object FROM relic_parts WHERE relic_id = ? ORDER BY ordinal`, relicID)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for rows.Next() {
				var partID string
				var ordinal int
				var body sql.NullString
				if err := rows.Scan(&partID, &ordinal, &body); err != nil {
					t.Fatal(err)
				}
				got = append(got, fmt.Sprintf("%d %s %s", ordinal, partID, body.String))
			}
			rows.Close()
			var want []string
			for ordinal, part := range snapshot.Record.State.Relic.Parts {
				body := ""
				if part.Body != nil {
					body = relicIndexGit(t, repository, nil, "rev-parse", snapshot.Head+":parts/"+part.ID+"/body")
				}
				want = append(want, fmt.Sprintf("%d %s %s", ordinal, part.ID, body))
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("parts of %s = %q, want %q", relicID, got, want)
			}

			chains, err := repository.ReadRelicOperations(ctx, config, []gitstore.RelicHistoryRequest{
				{Head: core.RelicHead{RelicID: relicID, ObjectID: snapshot.Head}},
			})
			if err != nil {
				t.Fatal(err)
			}
			operations := 0
			for _, commit := range chains[0].Commits {
				operations += len(commit.Operation.Operations)
			}
			if got := countRows(t, store, `SELECT COUNT(*) FROM relic_operations WHERE relic_id = ?`, relicID); got != operations {
				t.Fatalf("%s has %d operation rows, want %d", relicID, got, operations)
			}
		}
	})
}

func TestRelicPassReadsOnlyChangedRelics(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	var ids []string
	for number := range 10 {
		ids = append(ids, createIndexRelic(t, service, fmt.Sprintf("Relic %d", number), 2))
	}
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)

	updateIndexRelicTitle(t, service, ids[4], "Changed")
	source.reset()
	runRelicPass(t, store)
	if source.indexCalls != 1 || len(source.indexRequests[0]) != 1 || source.indexRequests[0][0].Head.RelicID != ids[4] {
		t.Fatalf("ReadIndexedRelics was called %d times with %+v, want once for %s", source.indexCalls, source.indexRequests, ids[4])
	}
	if source.operationCalls != 1 || len(source.operationRequest[0]) != 1 || source.operationRequest[0][0].Head.RelicID != ids[4] {
		t.Fatalf("ReadRelicOperations was called %d times with %+v, want once for %s", source.operationCalls, source.operationRequest, ids[4])
	}

	source.reset()
	runRelicPass(t, store)
	if source.listCalls != 1 || source.relicCalls() != 1 {
		t.Fatalf("a pass with nothing changed made %d relic calls, want one ListRelicHeads", source.relicCalls())
	}
}

func TestRelicPassReusesUnchangedFields(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Large", 30)
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)

	snapshot, err := repository.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatal(err)
	}
	edited := snapshot.Record.State.Relic.Parts[0]
	if _, err := service.Update(ctx, relicID, []core.RelicEdit{{
		Kind: core.RelicEditPart, Part: edited.ID, Base: edited.Body.Version,
		Body: &core.RelicBodyInput{Format: "md", Content: []byte("a new body\n")},
	}}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	source.reset()
	runRelicPass(t, store)
	if source.indexCalls != 1 {
		t.Fatalf("ReadIndexedRelics was called %d times, want once", source.indexCalls)
	}
	known := source.indexRequests[0][0].KnownFields
	for _, part := range snapshot.Record.State.Relic.Parts[1:] {
		object := relicIndexGit(t, repository, nil, "rev-parse", snapshot.Head+":parts/"+part.ID+"/fields.json")
		if _, found := known[object]; !found {
			t.Fatalf("KnownFields lacks the fields of unchanged part %s: %v", part.ID, known)
		}
	}
	if len(known) < 29 {
		t.Fatalf("KnownFields holds %d objects, want the other 29 parts' at least", len(known))
	}
}

func TestRelicPassReplacesOperationsAfterAReplay(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Replayed", 2)
	updateIndexRelicTitle(t, service, relicID, "One")
	updateIndexRelicTitle(t, service, relicID, "Two")
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)

	head := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(relicID))
	sibling := relicIndexGit(t, repository, nil, "commit-tree", head+"^{tree}", "-p", head+"^", "-m", "replayed")
	relicIndexGit(t, repository, nil, "update-ref", relicRefOf(relicID), sibling)
	pass := runRelicPass(t, store)
	if len(pass.failed) != 0 {
		// The sibling has the same documents as the head it replaced, so it
		// verifies as that head did.
		t.Fatalf("the replayed relic failed: %v", pass.failed)
	}

	fresh := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if _, err := fresh.RebuildWithRelics(context.Background()); err != nil {
		t.Fatalf("RebuildWithRelics() error = %v", err)
	}
	if got, want := dumpRelicTables(t, store), dumpRelicTables(t, fresh); got != want {
		t.Fatalf("after a replay the index holds\n%s\nwant a rebuild's\n%s", got, want)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM relic_operations WHERE commit_id = ?`, head); got != 0 {
		t.Fatalf("%d operation rows still name the replaced head", got)
	}
}

// relicIndexMutations makes twelve changes to the relics in ids, of every
// kind a relic update records.
func relicIndexMutations(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, service core.RelicService, ids []string) []func() {
	t.Helper()
	ctx := context.Background()
	part := func(relicID string, index int) core.RelicPart {
		t.Helper()
		snapshot, err := repository.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Record.State.Relic.Parts[index]
	}
	update := func(relicID string, edits ...core.RelicEdit) {
		t.Helper()
		if _, err := service.Update(ctx, relicID, edits); err != nil {
			t.Fatalf("Update(%s) error = %v", relicID, err)
		}
	}
	return []func(){
		func() { update(ids[0], core.RelicEdit{Kind: core.RelicEditTitle, Title: "Retitled"}) },
		func() {
			update(ids[1], core.RelicEdit{Kind: core.RelicEditSetProperty, Name: "status", Value: relicIndexJSON(t, "open")})
		},
		func() { update(ids[1], core.RelicEdit{Kind: core.RelicEditClearProperty, Name: "owner"}) },
		func() {
			update(ids[2], core.RelicEdit{Kind: core.RelicEditAddPart, PartType: "note",
				Fields: []core.RelicFieldEdit{{Name: "kind", Value: relicIndexJSON(t, "aside")}}})
		},
		func() {
			p := part(ids[2], 0)
			update(ids[2], core.RelicEdit{Kind: core.RelicEditPart, Part: p.ID, Base: p.Body.Version,
				Body: &core.RelicBodyInput{Format: "txt", Content: []byte{}}})
		},
		func() {
			update(ids[3], core.RelicEdit{Kind: core.RelicEditPart, Part: part(ids[3], 1).ID,
				Fields: []core.RelicFieldEdit{{Name: "heading", Clear: true}, {Name: "level", Value: json.RawMessage(`2`)}}})
		},
		func() {
			update(ids[3], core.RelicEdit{Kind: core.RelicEditMovePart, Part: part(ids[3], 0).ID, After: part(ids[3], 2).ID})
		},
		func() { update(ids[4], core.RelicEdit{Kind: core.RelicEditRemovePart, Part: part(ids[4], 1).ID}) },
		func() { update(ids[4], core.RelicEdit{Kind: core.RelicEditRestorePart, Part: part(ids[4], 1).ID}) },
		func() {
			if _, err := service.Delete(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if _, err := service.Restore(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			update(ids[2], core.RelicEdit{Kind: core.RelicEditAddPart, PartType: "binary",
				Body: &core.RelicBodyInput{Content: []byte{0xff, 0x00, 0xfe}}})
		},
	}
}

func TestRelicIndexContentsDoNotDependOnReadOrder(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceForIndex(repository, config, 1000)
		var ids []string
		for number := range 5 {
			ids = append(ids, createIndexRelic(t, service, fmt.Sprintf("Relic %d", number), 3))
		}
		stepwise := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		runRelicPass(t, stepwise)
		mutations := relicIndexMutations(t, repository, config, service, ids)
		if len(mutations) != 12 {
			t.Fatalf("%d mutations, want 12", len(mutations))
		}
		for _, mutate := range mutations {
			mutate()
			runRelicPass(t, stepwise)
		}
		for _, mutate := range relicLinkMutations(t, repository, config, service, ids) {
			mutate()
			runRelicPass(t, stepwise)
		}

		reversed := openRelicStore(t, orderedRelicSource{repositorySource{repository: repository}}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		runRelicPass(t, reversed)
		forward := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		runRelicPass(t, forward)
		rebuilt := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		if _, err := rebuilt.RebuildWithRelics(ctx); err != nil {
			t.Fatalf("RebuildWithRelics() error = %v", err)
		}

		want := dumpRelicTables(t, forward)
		for name, store := range map[string]*Store{"reversed": reversed, "stepwise": stepwise, "rebuilt": rebuilt} {
			if got := dumpRelicTables(t, store); got != want {
				t.Fatalf("%s index holds\n%s\nwant\n%s", name, got, want)
			}
		}
	})
}

func TestRelicRowWithMissingPartsIsReadAgain(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Parts", 4)
	createIndexRelic(t, service, "Other", 2)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	before := dumpRelicTables(t, store)

	execCache(t, store, `DELETE FROM relic_parts WHERE relic_id = ? AND ordinal = 2`, relicID)
	runRelicPass(t, store)
	if got := dumpRelicTables(t, store); got != before {
		t.Fatalf("after the pass the index holds\n%s\nwant\n%s", got, before)
	}
}

func TestRelicIndexRepairsItselfAfterAnInterruptedPass(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Interrupted", 3)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	before := dumpRelicTables(t, store)

	updateIndexRelicTitle(t, service, relicID, "Changed")
	interrupted := errors.New("the process stopped")
	store.beforeRelicCommit = func() error { return interrupted }
	if _, err := tryRelicPass(store); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted pass error = %v, want %v", err, interrupted)
	}
	if got := dumpRelicTables(t, store); got != before {
		t.Fatalf("an interrupted pass left\n%s\nwant\n%s", got, before)
	}

	store.beforeRelicCommit = nil
	runRelicPass(t, store)
	if got := relicRowHeads(t, store)[relicID]; got != relicRefHeads(t, repository, config)[relicID] {
		t.Fatalf("row head = %s, want the ref's", got)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM relics WHERE title = 'Changed'`); got != 1 {
		t.Fatalf("%d rows hold the new title, want 1", got)
	}
}

func TestStaleRelicRowIsRefreshed(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Stale", 3)
	updateIndexRelicTitle(t, service, relicID, "Current")
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	before := dumpRelicTables(t, store)

	parent := relicIndexGit(t, repository, nil, "rev-parse", relicRefOf(relicID)+"^")
	execCache(t, store, `UPDATE relics SET head = ? WHERE relic_id = ?`, parent, relicID)
	runRelicPass(t, store)
	if got := dumpRelicTables(t, store); got != before {
		t.Fatalf("after the pass the index holds\n%s\nwant\n%s", got, before)
	}
}

func TestARelicThatCannotBeIndexedHasNoRow(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	valid := createIndexRelic(t, service, "Valid", 2)
	damaged := createIndexRelic(t, service, "Damaged", 2)
	newer := createIndexRelic(t, service, "Newer", 2)
	updateIndexRelicTitle(t, service, damaged, "Changed")
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)

	damageRelic(t, repository, damaged)
	makeNewerRelic(t, repository, config, newer)
	pass := runRelicPass(t, store)
	if rows := relicRowHeads(t, store); len(rows) != 1 || rows[valid] == "" {
		t.Fatalf("rows = %v, want only %s", rows, valid)
	}
	if core.CategoryOf(pass.failed[damaged]) != core.CategoryCorruptData || core.CategoryOf(pass.failed[newer]) != core.CategoryNewerWriter {
		t.Fatalf("failed = %v, want the damaged relic corrupt and the newer one newer", pass.failed)
	}
	for _, table := range []string{"relic_properties", "relic_parts", "relic_operations"} {
		if got := countRows(t, store, `SELECT COUNT(*) FROM `+table+` WHERE relic_id IN (?, ?)`, damaged, newer); got != 0 {
			t.Fatalf("%s holds %d rows of relics that cannot be indexed", table, got)
		}
	}

	source.reset()
	runRelicPass(t, store)
	if source.indexCalls != 1 {
		t.Fatalf("ReadIndexedRelics was called %d times, want once", source.indexCalls)
	}
	var asked []string
	for _, request := range source.indexRequests[0] {
		asked = append(asked, request.Head.RelicID)
	}
	slices.Sort(asked)
	want := []string{damaged, newer}
	slices.Sort(want)
	if !slices.Equal(asked, want) {
		t.Fatalf("the second pass asked for %v, want %v", asked, want)
	}
}

func TestRemovedRelicRefDropsItsRows(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	kept := createIndexRelic(t, service, "Kept", 2)
	removed := createIndexRelic(t, service, "Removed", 2)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	keptBefore := countRows(t, store, `SELECT COUNT(*) FROM relic_parts WHERE relic_id = ?`, kept)

	relicIndexGit(t, repository, nil, "update-ref", "-d", relicRefOf(removed))
	runRelicPass(t, store)
	for _, table := range []string{"relics", "relic_properties", "relic_parts", "relic_operations"} {
		if got := countRows(t, store, `SELECT COUNT(*) FROM `+table+` WHERE relic_id = ?`, removed); got != 0 {
			t.Fatalf("%s still holds %d rows of the removed relic", table, got)
		}
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM relic_parts WHERE relic_id = ?`, kept); got != keptBefore || keptBefore == 0 {
		t.Fatalf("the kept relic has %d part rows, had %d", got, keptBefore)
	}
}

// relicIsAncestor reports whether ancestor is an ancestor of descendant, or
// the same commit.
func relicIsAncestor(repository *gitstore.Repository, ancestor, descendant string) bool {
	_, err := repository.Git(context.Background(), nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func TestConcurrentRelicPassesNeverRegressARow(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	var ids []string
	for number := range 3 {
		ids = append(ids, createIndexRelic(t, service, fmt.Sprintf("Raced %d", number), 2))
	}
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	stores := make([]*Store, 4)
	for index := range stores {
		stores[index] = openRelicStore(t, repositorySource{repository: repository}, config, cachePath)
	}

	var stop atomic.Bool
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		// Set however the writer ends, so the readers always stop.
		defer stop.Store(true)
		for round := 0; round < 15; round++ {
			for _, relicID := range ids {
				if _, err := service.Update(context.Background(), relicID, []core.RelicEdit{
					{Kind: core.RelicEditTitle, Title: fmt.Sprintf("Round %d", round)},
				}); err != nil {
					t.Errorf("Update(%s) error = %v", relicID, err)
					return
				}
			}
		}
	}()

	var readers sync.WaitGroup
	for _, store := range stores {
		readers.Add(1)
		go func() {
			defer readers.Done()
			seen := map[string]string{}
			for !stop.Load() {
				if _, err := tryRelicPass(store); err != nil {
					t.Errorf("relic pass error = %v", err)
					return
				}
				rows, err := store.db.QueryContext(context.Background(), `SELECT relic_id, head FROM relics`)
				if err != nil {
					t.Errorf("query rows: %v", err)
					return
				}
				current := map[string]string{}
				for rows.Next() {
					var relicID, head string
					if err := rows.Scan(&relicID, &head); err != nil {
						t.Errorf("scan: %v", err)
					}
					current[relicID] = head
				}
				rows.Close()
				for relicID, head := range current {
					if earlier := seen[relicID]; earlier != "" && earlier != head && relicIsAncestor(repository, head, earlier) {
						t.Errorf("relic %s row went back from %s to %s", relicID, earlier, head)
					}
					seen[relicID] = head
				}
			}
		}()
	}
	writer.Wait()
	readers.Wait()
	if t.Failed() {
		return
	}
	runRelicPass(t, stores[0])
	if rows, refs := relicRowHeads(t, stores[0]), relicRefHeads(t, repository, config); fmt.Sprint(rows) != fmt.Sprint(refs) {
		t.Fatalf("row heads = %v, want the refs %v", rows, refs)
	}
}

func TestRebuildWithRelicsCountsBothKinds(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	createTask(t, repository, config, "First task")
	next := 500000
	taskService := core.Service{Config: config, Reader: repository, Writer: repository, Actor: "test@example.test",
		Now: func() time.Time { return relicIndexTime }, IDs: core.IDSourceFunc(func() (string, error) {
			next++
			return relicIndexTestID(next), nil
		})}
	if _, err := taskService.CreateMutation(context.Background(), core.CreateInput{Title: "Second task"}); err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	service := relicServiceForIndex(repository, config, 1000)
	for number := range 3 {
		createIndexRelic(t, service, fmt.Sprintf("Relic %d", number), 2)
	}

	rebuilt := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	counts, err := rebuilt.RebuildWithRelics(context.Background())
	if err != nil {
		t.Fatalf("RebuildWithRelics() error = %v", err)
	}
	if counts != (RebuildCounts{Tasks: 2, Relics: 3}) {
		t.Fatalf("RebuildWithRelics() = %+v, want 2 tasks and 3 relics", counts)
	}
	incremental := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, incremental)
	if got, want := dumpRelicTables(t, rebuilt), dumpRelicTables(t, incremental); got != want {
		t.Fatalf("a rebuild holds\n%s\nwant an incremental pass's\n%s", got, want)
	}
}

func TestTaskPathsTouchNoRelicState(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	task := createTask(t, repository, config, "A task")
	service := relicServiceForIndex(repository, config, 1000)
	for number := range 10 {
		createIndexRelic(t, service, fmt.Sprintf("Relic %d", number), 2)
	}
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))

	if err := store.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, config, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, config, task.ID[:len(task.ID)-4]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.CachePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := source.relicCalls(); calls != 0 {
		t.Fatalf("task paths made %d relic calls, want none", calls)
	}

	countListQueries := func() int64 {
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
		if _, err := store.List(ctx, config); err != nil {
			t.Fatal(err)
		}
		return queries.Load()
	}
	empty := countListQueries()
	if got := countRows(t, store, `SELECT COUNT(*) FROM relics`); got != 0 {
		t.Fatalf("a task rebuild indexed %d relics", got)
	}
	runRelicPass(t, store)
	if got := countRows(t, store, `SELECT COUNT(*) FROM relics`); got != 10 {
		t.Fatalf("the relic pass indexed %d relics, want 10", got)
	}
	if indexed := countListQueries(); indexed != empty {
		t.Fatalf("a task List ran %d queries with ten relics indexed and %d with none", indexed, empty)
	}
}

func TestRelicIndexHoldsNoBodyText(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	marker := hex.EncodeToString(random)
	service := relicServiceForIndex(repository, config, 1000)
	if _, err := service.Create(ctx, core.RelicCreateInput{
		Title: "Secret", Type: "spec",
		Parts: []core.RelicPartInput{
			{Type: "section", Body: &core.RelicBodyInput{Format: "md", Content: []byte("before " + marker + " after\n")}},
			{Type: "section", Body: &core.RelicBodyInput{Format: "txt", Content: []byte(marker)}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if _, err := store.RebuildWithRelics(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM relic_parts WHERE body_object IS NOT NULL`); got != 2 {
		t.Fatalf("%d parts have a body object, want 2", got)
	}

	tables, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tables.Close()
	for _, table := range names {
		var dump strings.Builder
		dumpRows(t, store.db, &dump, `SELECT * FROM `+table)
		if strings.Contains(dump.String(), marker) {
			t.Fatalf("table %s holds body text", table)
		}
	}
	contents, err := os.ReadFile(store.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte(marker)) {
		t.Fatalf("the cache file holds body text")
	}
}

func TestSchemaVersionNineCacheIsReplaced(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A cache as version 9 wrote it: the relic tables, with no link count and
	// no link table.
	old, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schema+`;
		DROP TABLE relic_links; ALTER TABLE relics DROP COLUMN link_count;
		INSERT INTO projection_meta (key, value) VALUES ('schema_version', '9'), ('project_id', ?)`, config.ProjectID); err != nil {
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
		t.Fatal("a version 9 cache is usable")
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM projection_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || schemaVersion != "10" {
		t.Fatalf("schema version = %s (built %s), want 10", version, schemaVersion)
	}
	countRows(t, store, `SELECT COUNT(link_count) FROM relics`)
	for _, table := range []string{"relics", "relic_properties", "relic_parts", "relic_operations", "relic_links"} {
		countRows(t, store, `SELECT COUNT(*) FROM `+table)
	}
}
