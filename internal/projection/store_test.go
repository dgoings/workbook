package projection

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestStoreRefreshUsesSQLiteUntilATaskHeadChanges(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first, err := store.List(ctx, config)
	if err != nil || len(first) != 1 || first[0].State.Task.Title != "Initial title" {
		t.Fatalf("first projection = %#v, %v", first, err)
	}

	updateTaskTitle(t, repository, config, created.ID, "Changed title")
	second, err := store.List(ctx, config)
	if err != nil || len(second) != 1 || second[0].State.Task.Title != "Changed title" {
		t.Fatalf("incremental projection = %#v, %v", second, err)
	}

	if err := os.Remove(store.CachePath()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := store.List(ctx, config)
	if err != nil || len(rebuilt) != 1 || rebuilt[0].State.Task.Title != "Changed title" {
		t.Fatalf("recreated projection = %#v, %v", rebuilt, err)
	}
	if _, err := os.Stat(store.CachePath()); err != nil {
		t.Fatalf("Stat(recreated cache) error = %v", err)
	}
}

func TestStoreBacksMutationReaderWithCanonicalEmptyCollections(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Initial title")
	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	service := core.Service{
		Config:     config,
		Reader:     store,
		Writer:     repository,
		Projection: store,
		Actor:      "test@example.test",
		Now:        func() time.Time { return time.Date(2026, time.July, 26, 12, 1, 0, 0, time.UTC) },
		IDs: core.IDSourceFunc(func() (string, error) {
			return "01K0M6B8A4FTT8C39MXXYTW7D7", nil
		}),
	}
	title := "Updated title"

	result, err := service.UpdateMutation(ctx, created.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if result.Task.Title != title {
		t.Fatalf("UpdateMutation() title = %q, want %q", result.Task.Title, title)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("UpdateMutation() warnings = %#v, want none", result.Warnings)
	}
}

func TestStoreRefreshReadsOnlyAdvancedHeads(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	source := &countingHeadSource{snapshots: map[string]core.Snapshot{
		first.Head:  first,
		second.Head: second,
	}}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	source.heads = []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}, {TaskID: second.State.TaskID, ObjectID: second.Head}}
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(initial) error = %v", err)
	}
	if got, want := source.batchReadCalls, 1; got != want {
		t.Fatalf("initial ReadTaskHeads calls = %d, want %d", got, want)
	}
	if got, want := len(source.readBatches[0]), 2; got != want {
		t.Fatalf("initial ReadTaskHeads batch size = %d, want %d", got, want)
	}

	source.batchReadCalls = 0
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(unchanged) error = %v", err)
	}
	if got := source.batchReadCalls; got != 0 {
		t.Fatalf("unchanged ReadTaskHeads calls = %d, want 0", got)
	}

	advanced := second
	advanced.Head = "head-3"
	advanced.State.Task.Title = "Second, changed"
	source.snapshots[advanced.Head] = advanced
	source.heads[1].ObjectID = advanced.Head
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh(advanced) error = %v", err)
	}
	if got, want := source.batchReadCalls, 1; got != want {
		t.Fatalf("advanced ReadTaskHeads calls = %d, want %d", got, want)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := snapshots[1].State.Task.Title, "Second, changed"; got != want {
		t.Fatalf("changed title = %q, want %q", got, want)
	}
}

func TestStoreGetInspectsOnlyExactWarmHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	expected := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Warm")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: expected.State.TaskID, ObjectID: expected.Head}},
		snapshots: map[string]core.Snapshot{expected.Head: expected},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	resetSourceCalls(source)

	snapshot, err := store.Get(ctx, config, expected.State.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head != expected.Head {
		t.Fatalf("head = %q, want %q", snapshot.Head, expected.Head)
	}
	if source.listCalls != 0 || source.inspectCalls != 1 || source.batchReadCalls != 0 {
		t.Fatalf("source calls = list %d inspect %d batch %d", source.listCalls, source.inspectCalls, source.batchReadCalls)
	}
}

func TestStoreGetRefreshesOnlyChangedExactHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	expected := testSnapshot(previous.State.TaskID, "head-2", "Expected")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous, expected.Head: expected},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	source.heads[0].ObjectID = expected.Head
	resetSourceCalls(source)

	snapshot, err := store.Get(ctx, config, previous.State.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head != expected.Head || snapshot.State.Task.Title != "Expected" {
		t.Fatalf("snapshot = %#v, want changed head %q", snapshot, expected.Head)
	}
	if source.listCalls != 0 || source.inspectCalls != 1 || source.batchReadCalls != 1 || source.validationCalls != 1 {
		t.Fatalf(
			"source calls = list %d inspect %d batch %d validate %d",
			source.listCalls,
			source.inspectCalls,
			source.batchReadCalls,
			source.validationCalls,
		)
	}
	if got := source.readBatches[0]; !reflect.DeepEqual(got, source.heads) {
		t.Fatalf("batch heads = %#v, want %#v", got, source.heads)
	}
	advance := source.validatedAdvances[0][0]
	if advance.Previous.Head != previous.Head || advance.Current.ObjectID != expected.Head {
		t.Fatalf("validated advance = %#v, want %q -> %q", advance, previous.Head, expected.Head)
	}
}

func TestStoreGetRejectsDisappearedExactHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	cached := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Cached")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: cached.State.TaskID, ObjectID: cached.Head}},
		snapshots: map[string]core.Snapshot{cached.Head: cached},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	source.heads = nil
	resetSourceCalls(source)

	_, err = store.Get(ctx, config, cached.State.TaskID)
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("Get(disappeared) category = %q, want corrupt-data; error = %v", got, err)
	}
	if source.listCalls != 0 || source.inspectCalls != 1 || source.batchReadCalls != 0 {
		t.Fatalf("source calls = list %d inspect %d batch %d", source.listCalls, source.inspectCalls, source.batchReadCalls)
	}
}

func TestStoreGetRetriesWhenConditionalRefreshLosesRace(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	changed := testSnapshot(previous.State.TaskID, "head-2", "Changed")
	newer := testSnapshot(previous.State.TaskID, "head-3", "Newer")
	initialSource := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous},
	}
	store, err := openStore(ctx, initialSource, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	source := newExactRefreshRaceSource(changed, newer)
	store.source = source
	result := make(chan struct {
		snapshot core.Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, err := store.Get(ctx, config, previous.State.TaskID)
		result <- struct {
			snapshot core.Snapshot
			err      error
		}{snapshot: snapshot, err: err}
	}()
	<-source.changedReadStarted

	applied, err := store.Advance(ctx, config, previous.Head, newer)
	if err != nil || !applied {
		t.Fatalf("Advance(newer) = %t, %v, want true, nil", applied, err)
	}
	close(source.releaseChangedRead)

	got := <-result
	if got.err != nil {
		t.Fatalf("Get() error = %v", got.err)
	}
	if got.snapshot.Head != newer.Head || got.snapshot.State.Task.Title != "Newer" {
		t.Fatalf("Get() = %#v, want concurrently advanced snapshot", got.snapshot)
	}
	if source.inspectCalls != 2 || source.batchReadCalls != 1 {
		t.Fatalf("source calls = inspect %d batch %d, want inspect 2 batch 1", source.inspectCalls, source.batchReadCalls)
	}
}

func TestStoreRefreshBatchesChangedHeads(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	third := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D3", "head-3", "Third")
	source := &countingHeadSource{
		heads: []gitstore.TaskHead{
			{TaskID: first.State.TaskID, ObjectID: first.Head},
			{TaskID: second.State.TaskID, ObjectID: second.Head},
			{TaskID: third.State.TaskID, ObjectID: third.Head},
		},
		snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second, third.Head: third},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	changedFirst := testSnapshot(first.State.TaskID, "head-4", "First changed")
	changedThird := testSnapshot(third.State.TaskID, "head-5", "Third changed")
	source.heads[0].ObjectID = changedFirst.Head
	source.heads[2].ObjectID = changedThird.Head
	source.snapshots[changedFirst.Head] = changedFirst
	source.snapshots[changedThird.Head] = changedThird
	resetSourceCalls(source)

	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if source.listCalls != 1 || source.inspectCalls != 0 || source.batchReadCalls != 1 || source.validationCalls != 1 {
		t.Fatalf(
			"source calls = list %d inspect %d batch %d validate %d",
			source.listCalls,
			source.inspectCalls,
			source.batchReadCalls,
			source.validationCalls,
		)
	}
	wantBatch := []gitstore.TaskHead{source.heads[0], source.heads[2]}
	if got := source.readBatches[0]; !reflect.DeepEqual(got, wantBatch) {
		t.Fatalf("batch heads = %#v, want %#v", got, wantBatch)
	}
	if got := len(source.validatedAdvances[0]); got != 2 {
		t.Fatalf("validated advances = %d, want 2", got)
	}

	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := []string{snapshots[0].State.Task.Title, snapshots[1].State.Task.Title, snapshots[2].State.Task.Title}; !reflect.DeepEqual(got, []string{"First changed", "Second", "Third changed"}) {
		t.Fatalf("titles = %v, want changed consumer snapshots", got)
	}
}

func TestStoreRefreshPreservesConcurrentCreateAdvancedAfterGitEnumeration(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	source := newGitListBarrierSource(repository)
	t.Cleanup(source.release)
	store.source = source
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- store.Refresh(ctx)
	}()
	<-source.listed

	created := createTask(t, repository, config, "Concurrent create")
	written, err := repository.Get(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("Get(canonical create) error = %v", err)
	}
	applied, err := store.Advance(ctx, config, "", written)
	if err != nil || !applied {
		t.Fatalf("Advance(concurrent create) = %t, %v, want true, nil", applied, err)
	}
	source.release()

	if err := <-refreshDone; err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	cached, found, err := store.querySnapshot(ctx, created.ID)
	if err != nil {
		t.Fatalf("querySnapshot() error = %v", err)
	}
	if !found || cached.Head != written.Head || cached.State.Task.Title != "Concurrent create" {
		t.Fatalf("cached snapshot = %#v, found %t, want concurrently created head %q", cached, found, written.Head)
	}
}

func TestStoreRefreshRejectsDisappearedCanonicalRefAndPreservesCache(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Disappeared ref")
	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	ref := "refs/workbook/tasks/" + created.ID
	if _, err := repository.Git(ctx, nil, "update-ref", "-d", ref, created.Head); err != nil {
		t.Fatalf("delete canonical task ref error = %v", err)
	}
	err = store.Refresh(ctx)
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("Refresh(disappeared ref) category = %q, want corrupt-data; error = %v", got, err)
	}
	cached, found, queryErr := store.querySnapshot(ctx, created.ID)
	if queryErr != nil {
		t.Fatalf("querySnapshot() error = %v", queryErr)
	}
	if !found || cached.Head != created.Head || cached.State.Task.Title != "Disappeared ref" {
		t.Fatalf("cached snapshot = %#v, found %t, want preserved head %q", cached, found, created.Head)
	}
}

func TestStoreRefreshRejectsChangedHistoryGeneration(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	changed := testSnapshot(previous.State.TaskID, "head-2", "Changed generation")
	changed.State.History.Generation = "01K0M6B8A4FTT8C39MXXYTW7DA"
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous, changed.Head: changed},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	source.heads[0].ObjectID = changed.Head
	resetSourceCalls(source)

	err = store.Refresh(ctx)
	if got := core.CategoryOf(err); got != core.CategoryCorruptData {
		t.Fatalf("Refresh() category = %q, want corrupt-data; error = %v", got, err)
	}
	if source.validationCalls != 1 || source.batchReadCalls != 1 {
		t.Fatalf("source calls = validate %d batch %d, want 1 each", source.validationCalls, source.batchReadCalls)
	}
	cached, found, err := store.querySnapshot(ctx, previous.State.TaskID)
	if err != nil {
		t.Fatalf("querySnapshot() error = %v", err)
	}
	if !found || cached.Head != previous.Head || cached.State.Task.Title != "Previous" {
		t.Fatalf("cached snapshot = %#v, found %t, want unchanged previous snapshot", cached, found)
	}
}

func TestStoreAdvanceConditionallyUpdatesSnapshot(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	expected := testSnapshot(previous.State.TaskID, "head-2", "Expected")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous, expected.Head: expected},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	applied, err := store.Advance(ctx, config, previous.Head, expected)
	if err != nil || !applied {
		t.Fatalf("Advance() = %t, %v, want true, nil", applied, err)
	}
	source.heads[0].ObjectID = expected.Head
	resetSourceCalls(source)
	got, err := store.Get(ctx, config, previous.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Head != expected.Head || got.State.Task.Title != "Expected" {
		t.Fatalf("Get() = %#v, want advanced snapshot", got)
	}
}

func TestStoreAdvanceRejectsChangedHistoryGeneration(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	changed := testSnapshot(previous.State.TaskID, "head-2", "Changed generation")
	changed.State.History.Generation = "01K0M6B8A4FTT8C39MXXYTW7DA"
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	applied, err := store.Advance(ctx, config, previous.Head, changed)
	if err != nil || applied {
		t.Fatalf("Advance(changed generation) = %t, %v, want false, nil", applied, err)
	}
	cached, found, err := store.querySnapshot(ctx, previous.State.TaskID)
	if err != nil {
		t.Fatalf("querySnapshot() error = %v", err)
	}
	if !found || cached.Head != previous.Head ||
		cached.State.History.Generation != previous.State.History.Generation ||
		cached.State.Task.Title != "Previous" {
		t.Fatalf("cached snapshot = %#v, found %t, want unchanged previous snapshot", cached, found)
	}
}

func TestStoreAdvanceAcceptsAlreadyAdvancedSnapshot(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	expected := testSnapshot(previous.State.TaskID, "head-2", "Expected")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: expected.State.TaskID, ObjectID: expected.Head}},
		snapshots: map[string]core.Snapshot{expected.Head: expected},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	applied, err := store.Advance(ctx, config, previous.Head, expected)
	if err != nil || !applied {
		t.Fatalf("Advance(already advanced) = %t, %v, want true, nil", applied, err)
	}
	got, err := store.Get(ctx, config, expected.State.TaskID)
	if err != nil || got.Head != expected.Head {
		t.Fatalf("Get() = %#v, %v, want head %q", got, err, expected.Head)
	}
}

func TestStoreAdvanceDoesNotRegressNewerSnapshot(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	stale := testSnapshot(previous.State.TaskID, "head-2", "Stale")
	newer := testSnapshot(previous.State.TaskID, "head-3", "Newer")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: newer.State.TaskID, ObjectID: newer.Head}},
		snapshots: map[string]core.Snapshot{newer.Head: newer},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	applied, err := store.Advance(ctx, config, previous.Head, stale)
	if err != nil || applied {
		t.Fatalf("Advance(stale) = %t, %v, want false, nil", applied, err)
	}
	got, err := store.Get(ctx, config, newer.State.TaskID)
	if err != nil || got.Head != newer.Head || got.State.Task.Title != "Newer" {
		t.Fatalf("Get() = %#v, %v, want newer snapshot", got, err)
	}
}

func TestStoreInvalidateOnlyDeletesExpectedOrWrittenHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	previous.State.Task.Labels = []string{"cache"}
	previous.State.Task.Dependencies = []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D9"}
	written := testSnapshot(previous.State.TaskID, "head-2", "Written")
	newer := testSnapshot(previous.State.TaskID, "head-3", "Newer")

	for _, test := range []struct {
		name       string
		cached     core.Snapshot
		wantAbsent bool
	}{
		{name: "expected parent", cached: previous, wantAbsent: true},
		{name: "written head", cached: written, wantAbsent: true},
		{name: "newer head", cached: newer, wantAbsent: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &countingHeadSource{
				heads:     []gitstore.TaskHead{{TaskID: test.cached.State.TaskID, ObjectID: test.cached.Head}},
				snapshots: map[string]core.Snapshot{test.cached.Head: test.cached},
			}
			store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
			if err != nil {
				t.Fatalf("openStore() error = %v", err)
			}
			if _, err := store.Rebuild(ctx); err != nil {
				t.Fatalf("Rebuild() error = %v", err)
			}

			if err := store.Invalidate(ctx, config, previous.State.TaskID, previous.Head, written.Head); err != nil {
				t.Fatalf("Invalidate() error = %v", err)
			}
			if test.wantAbsent {
				source.heads = nil
				_, err := store.Get(ctx, config, previous.State.TaskID)
				if got := core.CategoryOf(err); got != core.CategoryNotFound {
					t.Fatalf("Get() category = %q, want not-found; error = %v", got, err)
				}
				source.heads = []gitstore.TaskHead{{TaskID: written.State.TaskID, ObjectID: written.Head}}
				source.snapshots[written.Head] = written
				got, err := store.Get(ctx, config, written.State.TaskID)
				if err != nil || got.Head != written.Head {
					t.Fatalf("Get(recreated) = %#v, %v, want written snapshot", got, err)
				}
				return
			}
			got, err := store.Get(ctx, config, newer.State.TaskID)
			if err != nil || got.Head != newer.Head || got.State.Task.Title != "Newer" {
				t.Fatalf("Get() = %#v, %v, want preserved newer snapshot", got, err)
			}
		})
	}
}

func TestQuerySnapshotRemainsConsistentAcrossConcurrentMutation(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	previous.State.Task.Labels = []string{"old-label"}
	previous.State.Task.Dependencies = []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D9"}
	updated := testSnapshot(previous.State.TaskID, "head-2", "Updated")
	updated.State.Task.Labels = []string{"new-label"}
	updated.State.Task.Dependencies = []string{"WB-01K0M6B8A4FTT8C39MXXYTW7DA"}

	for _, test := range []struct {
		name   string
		read   func(*Store) (core.Snapshot, bool, error)
		mutate func(*Store) error
	}{
		{
			name: "conditional advance",
			read: func(store *Store) (core.Snapshot, bool, error) {
				return store.querySnapshot(ctx, previous.State.TaskID)
			},
			mutate: func(store *Store) error {
				applied, err := store.Advance(ctx, config, previous.Head, updated)
				if err == nil && !applied {
					return errors.New("Advance() was not applied")
				}
				return err
			},
		},
		{
			name: "conditional invalidation through Get",
			read: func(store *Store) (core.Snapshot, bool, error) {
				snapshot, err := store.Get(ctx, config, previous.State.TaskID)
				return snapshot, err == nil, err
			},
			mutate: func(store *Store) error {
				return store.Invalidate(ctx, config, previous.State.TaskID, previous.Head, updated.Head)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &countingHeadSource{
				heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
				snapshots: map[string]core.Snapshot{previous.Head: previous},
			}
			store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
			if err != nil {
				t.Fatalf("openStore() error = %v", err)
			}
			if _, err := store.Rebuild(ctx); err != nil {
				t.Fatalf("Rebuild() error = %v", err)
			}
			blocker := installSnapshotCollectionBlocker(t, store)

			result := make(chan struct {
				snapshot core.Snapshot
				found    bool
				err      error
			}, 1)
			go func() {
				snapshot, found, err := test.read(store)
				result <- struct {
					snapshot core.Snapshot
					found    bool
					err      error
				}{snapshot: snapshot, found: found, err: err}
			}()
			<-blocker.collectionReadStarted

			if err := test.mutate(store); err != nil {
				t.Fatalf("concurrent mutation error = %v", err)
			}
			blocker.release()

			got := <-result
			if got.err != nil {
				t.Fatalf("snapshot read error = %v", got.err)
			}
			if !got.found {
				t.Fatal("snapshot read found = false, want the coherent pre-mutation snapshot")
			}
			if got.snapshot.Head != previous.Head ||
				got.snapshot.State.Task.Title != "Previous" ||
				!reflect.DeepEqual(got.snapshot.State.Task.Labels, []string{"old-label"}) ||
				!reflect.DeepEqual(got.snapshot.State.Task.Dependencies, []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D9"}) {
				t.Fatalf("snapshot read = %#v, want complete pre-mutation snapshot", got.snapshot)
			}
		})
	}
}

func TestConcurrentIndependentTaskAdvancement(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	advancedFirst := testSnapshot(first.State.TaskID, "head-3", "First advanced")
	advancedSecond := testSnapshot(second.State.TaskID, "head-4", "Second advanced")
	source := &countingHeadSource{
		heads: []gitstore.TaskHead{
			{TaskID: first.State.TaskID, ObjectID: first.Head},
			{TaskID: second.State.TaskID, ObjectID: second.Head},
		},
		snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, update := range []struct {
		parent   string
		snapshot core.Snapshot
	}{
		{parent: first.Head, snapshot: advancedFirst},
		{parent: second.Head, snapshot: advancedSecond},
	} {
		update := update
		go func() {
			<-start
			applied, err := store.Advance(ctx, config, update.parent, update.snapshot)
			if err == nil && !applied {
				err = fmt.Errorf("Advance(%s) was not applied", update.snapshot.State.TaskID)
			}
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	source.heads[0].ObjectID = advancedFirst.Head
	source.heads[1].ObjectID = advancedSecond.Head
	source.snapshots[advancedFirst.Head] = advancedFirst
	source.snapshots[advancedSecond.Head] = advancedSecond
	for _, expected := range []core.Snapshot{advancedFirst, advancedSecond} {
		got, err := store.Get(ctx, config, expected.State.TaskID)
		if err != nil || got.Head != expected.Head || got.State.Task.Title != expected.State.Task.Title {
			t.Fatalf("Get(%s) = %#v, %v, want %#v", expected.State.TaskID, got, err, expected)
		}
	}
}

func TestConcurrentRefreshCannotRegressProjectedHead(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot(first.State.TaskID, "head-2", "Second")
	third := testSnapshot(first.State.TaskID, "head-3", "Third")
	initialSource := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}},
		snapshots: map[string]core.Snapshot{first.Head: first},
	}
	store, err := openStore(ctx, initialSource, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	source := newConcurrentRefreshSource(second, third)
	store.source = source
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.Refresh(ctx)
	}()
	<-source.secondReadStarted

	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- store.Refresh(ctx)
	}()
	thirdCompleted := false
	select {
	case <-source.thirdHeadListed:
		if err := <-thirdDone; err != nil {
			t.Fatalf("Refresh(third) error = %v", err)
		}
		thirdCompleted = true
	case <-time.After(250 * time.Millisecond):
	}
	close(source.releaseSecondRead)
	if err := <-firstDone; err != nil {
		t.Fatalf("Refresh(second) error = %v", err)
	}
	if !thirdCompleted {
		err := <-thirdDone
		if err != nil {
			t.Fatalf("Refresh(third) error = %v", err)
		}
	}

	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != third.Head || snapshots[0].State.Task.Title != "Third" {
		t.Fatalf("final cache = %#v, want newest head %q", snapshots, third.Head)
	}
}

func TestStoreConcurrentReadsAndRebuilds(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Concurrent")
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	const workers = 8
	const iterations = 12
	errors := make(chan error, workers*iterations)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if (worker+iteration)%3 == 0 {
					_, err := store.Rebuild(ctx)
					errors <- err
					continue
				}
				snapshots, err := store.List(ctx, config)
				if err == nil && (len(snapshots) != 1 || snapshots[0].Head != snapshot.Head) {
					err = fmt.Errorf("List() = %#v, want head %q", snapshots, snapshot.Head)
				}
				errors <- err
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent cache operation error = %v", err)
		}
	}
}

func TestListUsesBoundedSQLQueriesForManyTasks(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	taskIDs := []string{
		"WB-01K0M6B8A4FTT8C39MXXYTW7D1",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D2",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D3",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D4",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D5",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D6",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D7",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D8",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D9",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DA",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DB",
		"WB-01K0M6B8A4FTT8C39MXXYTW7DC",
	}
	source := &staticHeadSource{snapshots: make(map[string]core.Snapshot, len(taskIDs))}
	for index, taskID := range taskIDs {
		head := fmt.Sprintf("head-%02d", index)
		snapshot := testSnapshot(taskID, head, fmt.Sprintf("Task %02d", index))
		snapshot.State.Task.Labels = []string{"zeta", "alpha"}
		snapshot.State.Task.Dependencies = []string{taskIDs[1], taskIDs[0]}
		source.heads = append(source.heads, gitstore.TaskHead{TaskID: taskID, ObjectID: head})
		source.snapshots[head] = snapshot
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	var queries atomic.Int64
	driverName := fmt.Sprintf("workbook-counting-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, &queryCountingDriver{inner: &sqlite.Driver{}, queries: &queries})
	if err := store.db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store.db, err = sql.Open(driverName, store.cachePath)
	if err != nil {
		t.Fatalf("Open(counting driver) error = %v", err)
	}

	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != len(taskIDs) {
		t.Fatalf("List() count = %d, want %d", len(snapshots), len(taskIDs))
	}
	if got, want := snapshots[0].State.Task.Labels, []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if got, want := snapshots[0].State.Task.Dependencies, []string{taskIDs[0], taskIDs[1]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if got := queries.Load(); got > 10 {
		t.Fatalf("unchanged List() SQL queries = %d for %d tasks, want at most 10", got, len(taskIDs))
	}
}

func TestProjectionPreservesCanonicalTimestampOffsets(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	offset := time.FixedZone("canonical-offset", -(4*60+30)*60)
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Offset")
	snapshot.State.Task.CreatedAt = time.Date(2026, time.July, 26, 12, 34, 56, 789, offset)
	snapshot.State.Task.UpdatedAt = time.Date(2026, time.July, 26, 13, 45, 1, 987, offset)
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	got, err := store.Get(ctx, config, snapshot.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State.Task.CreatedAt.Format(time.RFC3339Nano) != snapshot.State.Task.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("createdAt = %q, want preserved %q", got.State.Task.CreatedAt.Format(time.RFC3339Nano), snapshot.State.Task.CreatedAt.Format(time.RFC3339Nano))
	}
	if got.State.Task.UpdatedAt.Format(time.RFC3339Nano) != snapshot.State.Task.UpdatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("updatedAt = %q, want preserved %q", got.State.Task.UpdatedAt.Format(time.RFC3339Nano), snapshot.State.Task.UpdatedAt.Format(time.RFC3339Nano))
	}
}

func TestProjectionCacheErrorsSuggestRebuildWithoutMaskingGitCorruption(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	snapshot := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Cache error")
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		snapshots: map[string]core.Snapshot{snapshot.Head: snapshot},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE task_labels`); err != nil {
		t.Fatalf("DROP TABLE error = %v", err)
	}
	if _, err := store.querySnapshots(ctx); core.CategoryOf(err) != core.CategoryOperational || !strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("cache error = %v (%q), want operational error with rebuild guidance", err, core.CategoryOf(err))
	}

	canonicalError := core.Errorf(core.CategoryCorruptData, "canonical Git checkpoint is corrupt")
	corruptSource := &staticHeadSource{
		heads:   []gitstore.TaskHead{{TaskID: snapshot.State.TaskID, ObjectID: snapshot.Head}},
		readErr: canonicalError,
	}
	corruptStore, err := openStore(ctx, corruptSource, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore(corrupt source) error = %v", err)
	}
	err = corruptStore.Refresh(ctx)
	if core.CategoryOf(err) != core.CategoryCorruptData || !errors.Is(err, canonicalError) {
		t.Fatalf("Refresh(corrupt Git) error = %v (%q), want original corrupt-data error", err, core.CategoryOf(err))
	}
	if strings.Contains(err.Error(), "workbook rebuild") {
		t.Fatalf("Refresh(corrupt Git) error = %v, must not suggest rebuilding cache", err)
	}
}

func TestStoreRebuildsMalformedOrWrongProjectDatabase(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}

	for _, contents := range [][]byte{
		[]byte("not a sqlite database"),
		validProjectionDatabase(t, "2", config.ProjectID),
		validProjectionDatabase(t, schemaVersion, config.ProjectID+"X"),
	} {
		if err := os.WriteFile(store.CachePath(), contents, 0o600); err != nil {
			t.Fatalf("WriteFile(cache) error = %v", err)
		}
		snapshots, err := store.List(ctx, config)
		if err != nil {
			t.Fatalf("List(recovery) error = %v", err)
		}
		if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
			t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
		}
	}
}

func TestOpenRebuildsMalformedCacheFromCanonicalGit(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := os.WriteFile(cachePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("WriteFile(cache) error = %v", err)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
		t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
	}
}

func TestStoreRebuildsMetadataMatchingDatabaseMissingRequiredTable(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Canonical task")
	cachePath := filepath.Join(repository.CommonGitDir, "workbook", cacheFilename)
	if err := writeIncompleteProjectionDatabase(cachePath, config.ProjectID); err != nil {
		t.Fatalf("writeIncompleteProjectionDatabase() error = %v", err)
	}

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].State.TaskID != created.ID || snapshots[0].State.Task.Title != "Canonical task" {
		t.Fatalf("recovered snapshots = %#v, want canonical task", snapshots)
	}
}

func TestRebuildLeavesPreviousDatabaseWhenReplacementFails(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	previous := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Previous")
	replacement := testSnapshot(previous.State.TaskID, "head-2", "Replacement")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: previous.State.TaskID, ObjectID: previous.Head}},
		snapshots: map[string]core.Snapshot{previous.Head: previous, replacement.Head: replacement},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}

	source.heads[0].ObjectID = replacement.Head
	store.rename = func(string, string) error { return errors.New("rename failed") }
	if _, err := store.Rebuild(ctx); err == nil {
		t.Fatal("Rebuild(replacement) error = nil, want failure")
	}

	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() after failed rebuild error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != previous.Head || snapshots[0].State.Task.Title != "Previous" {
		t.Fatalf("cache after failed rebuild = %#v, want previous projected snapshot", snapshots)
	}
}

func TestStoreWritesToTheCacheAnotherProcessReplaced(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot(first.State.TaskID, "head-2", "Second")
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	newSource := func() *countingHeadSource {
		return &countingHeadSource{
			heads:     []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}},
			snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second},
		}
	}

	source := newSource()
	store, err := openStore(ctx, source, config, cachePath)
	if err != nil {
		t.Fatalf("openStore(long lived) error = %v", err)
	}
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(initial) error = %v", err)
	}
	// Read once so the pool holds a live connection to this inode. A long-lived
	// process always does; without it sql.Open's laziness would rebind by
	// accident on the next query and hide the defect.
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}

	// Another process rebuilds the same cache, renaming a new database over the
	// file this store holds open. The long-lived store must notice and rebind.
	replacement, err := openStore(ctx, newSource(), config, cachePath)
	if err != nil {
		t.Fatalf("openStore(replacement) error = %v", err)
	}
	if _, err := replacement.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(replacement) error = %v", err)
	}

	source.heads[0].ObjectID = second.Head
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(after replacement) error = %v", err)
	}

	reader, err := openStore(ctx, newSource(), config, cachePath)
	if err != nil {
		t.Fatalf("openStore(reader) error = %v", err)
	}
	snapshots, err := reader.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != second.Head {
		t.Fatalf("cache at path = %#v, want the refreshed head %q", snapshots, second.Head)
	}
}

func TestStoreStillRebuildsWhenTheCacheIsDeleted(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	only := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "Only")
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	source := &countingHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: only.State.TaskID, ObjectID: only.Head}},
		snapshots: map[string]core.Snapshot{only.Head: only},
	}
	store, err := openStore(ctx, source, config, cachePath)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(initial) error = %v", err)
	}

	if err := os.Remove(cachePath); err != nil {
		t.Fatalf("Remove(cache) error = %v", err)
	}

	snapshots, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List(after deletion) error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].State.Task.Title != "Only" {
		t.Fatalf("snapshots after deletion = %#v, want the canonical task rebuilt", snapshots)
	}
}

func TestRebuildRetriesOnceWhenHeadsChangeDuringBuild(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	second := testSnapshot(first.State.TaskID, "head-2", "Second")
	source := &headSequenceSource{
		headSets: [][]gitstore.TaskHead{
			{{TaskID: first.State.TaskID, ObjectID: first.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
			{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		},
		snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second},
	}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	count, err := store.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Rebuild() count = %d, want 1", count)
	}
	snapshots, err := store.querySnapshots(ctx)
	if err != nil {
		t.Fatalf("querySnapshots() error = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Head != second.Head || snapshots[0].State.Task.Title != "Second" {
		t.Fatalf("installed cache = %#v, want second task head", snapshots)
	}

	third := testSnapshot(first.State.TaskID, "head-3", "Third")
	source.headSets = [][]gitstore.TaskHead{
		{{TaskID: first.State.TaskID, ObjectID: first.Head}},
		{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		{{TaskID: second.State.TaskID, ObjectID: second.Head}},
		{{TaskID: third.State.TaskID, ObjectID: third.Head}},
	}
	source.index = 0
	source.snapshots[third.Head] = third
	if _, err := store.Rebuild(ctx); core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("Rebuild() category = %q, want operational; error = %v", core.CategoryOf(err), err)
	}
}

func TestStoreQueries(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	first := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-1", "First")
	first.State.Task.Labels = []string{"alpha", "zeta"}
	first.State.Task.Dependencies = []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D2"}
	second := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-2", "Second")
	source := &countingHeadSource{heads: []gitstore.TaskHead{{TaskID: first.State.TaskID, ObjectID: first.Head}, {TaskID: second.State.TaskID, ObjectID: second.Head}}, snapshots: map[string]core.Snapshot{first.Head: first, second.Head: second}}
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}

	got, err := store.Get(ctx, config, first.State.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(got.State.Task.Labels, []string{"alpha", "zeta"}) || !reflect.DeepEqual(got.State.Task.Dependencies, []string{"WB-01K0M6B8A4FTT8C39MXXYTW7D2"}) {
		t.Fatalf("Get() collections = %#v", got.State.Task)
	}
	resolved, err := store.Resolve(ctx, config, "wb-01k0m6b8a4ftt8c39mxxytw7d1")
	if err != nil || resolved != first.State.TaskID {
		t.Fatalf("Resolve(case-insensitive) = %q, %v", resolved, err)
	}
	_, err = store.Resolve(ctx, config, "WB-")
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Resolve(ambiguous) category = %q, want validation; error = %v", got, err)
	}
	_, err = store.Get(ctx, config, "WB-01K0M6B8A4FTT8C39MXXYTW7D3")
	if got := core.CategoryOf(err); got != core.CategoryNotFound {
		t.Fatalf("Get(missing) category = %q, want not-found; error = %v", got, err)
	}
}

// emptyTaskOperations satisfies the operation-chain half of taskHeadSource for
// fakes whose tests exercise task heads alone. Returning no commits leaves the
// projection with no operation rows for those tasks, which is exactly the state
// the bounded Git fallback exists to answer from.
type emptyTaskOperations struct{}

func (emptyTaskOperations) ReadTaskOperations(
	_ context.Context,
	_ core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	results := make([]gitstore.TaskOperationsResult, len(requests))
	for index, request := range requests {
		results[index] = gitstore.TaskOperationsResult{
			TaskID:          request.Head.TaskID,
			Head:            request.Head.ObjectID,
			BoundaryReached: request.StopAt != "",
		}
	}
	return results, nil
}

type countingHeadSource struct {
	emptyTaskOperations
	heads             []gitstore.TaskHead
	snapshots         map[string]core.Snapshot
	listCalls         int
	inspectCalls      int
	batchReadCalls    int
	validationCalls   int
	readBatches       [][]gitstore.TaskHead
	validatedAdvances [][]gitstore.HeadAdvance
	err               error
	readErr           error
	validationErr     error
}

type headSequenceSource struct {
	emptyTaskOperations
	headSets  [][]gitstore.TaskHead
	snapshots map[string]core.Snapshot
	index     int
}

type concurrentRefreshSource struct {
	emptyTaskOperations
	mu                sync.Mutex
	snapshots         map[string]core.Snapshot
	headCalls         int
	secondReadStarted chan struct{}
	releaseSecondRead chan struct{}
	thirdHeadListed   chan struct{}
}

type exactRefreshRaceSource struct {
	emptyTaskOperations
	mu                 sync.Mutex
	changed            core.Snapshot
	newer              core.Snapshot
	inspectCalls       int
	batchReadCalls     int
	changedReadStarted chan struct{}
	releaseChangedRead chan struct{}
}

type gitListBarrierSource struct {
	repository  *gitstore.Repository
	listed      chan struct{}
	releaseList chan struct{}
	listOnce    sync.Once
	releaseOnce sync.Once
}

type staticHeadSource struct {
	emptyTaskOperations
	heads     []gitstore.TaskHead
	snapshots map[string]core.Snapshot
	readErr   error
}

type queryCountingDriver struct {
	inner   driver.Driver
	queries *atomic.Int64
}

type queryCountingConn struct {
	driver.Conn
	queries *atomic.Int64
}

type snapshotCollectionBlocker struct {
	collectionReadStarted chan struct{}
	releaseCollectionRead chan struct{}
	startOnce             sync.Once
	releaseOnce           sync.Once
}

type snapshotBlockingDriver struct {
	inner   driver.Driver
	blocker *snapshotCollectionBlocker
}

type snapshotBlockingConn struct {
	driver.Conn
	blocker *snapshotCollectionBlocker
}

var countingDriverSequence atomic.Int64

func newConcurrentRefreshSource(second, third core.Snapshot) *concurrentRefreshSource {
	return &concurrentRefreshSource{
		snapshots:         map[string]core.Snapshot{second.Head: second, third.Head: third},
		secondReadStarted: make(chan struct{}),
		releaseSecondRead: make(chan struct{}),
		thirdHeadListed:   make(chan struct{}),
	}
}

func newExactRefreshRaceSource(changed, newer core.Snapshot) *exactRefreshRaceSource {
	return &exactRefreshRaceSource{
		changed:            changed,
		newer:              newer,
		changedReadStarted: make(chan struct{}),
		releaseChangedRead: make(chan struct{}),
	}
}

func (s *gitListBarrierSource) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
) ([]gitstore.TaskOperationsResult, error) {
	return s.repository.ReadTaskOperations(ctx, config, requests)
}

func newGitListBarrierSource(repository *gitstore.Repository) *gitListBarrierSource {
	return &gitListBarrierSource{
		repository:  repository,
		listed:      make(chan struct{}),
		releaseList: make(chan struct{}),
	}
}

func (s *gitListBarrierSource) release() {
	s.releaseOnce.Do(func() {
		close(s.releaseList)
	})
}

func (s *gitListBarrierSource) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]gitstore.TaskHead, error) {
	heads, err := s.repository.ListTaskHeads(ctx, config)
	s.listOnce.Do(func() {
		close(s.listed)
		<-s.releaseList
	})
	return heads, err
}

func (s *gitListBarrierSource) InspectTaskHead(ctx context.Context, config core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	return s.repository.InspectTaskHead(ctx, config, taskID)
}

func (s *gitListBarrierSource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	return s.repository.ReadTaskHeads(ctx, config, heads)
}

func (s *gitListBarrierSource) ValidateTaskHeadAdvances(ctx context.Context, config core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	return s.repository.ValidateTaskHeadAdvances(ctx, config, advances)
}

func installSnapshotCollectionBlocker(t *testing.T, store *Store) *snapshotCollectionBlocker {
	t.Helper()
	if _, err := store.db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("enable WAL mode error = %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close projection database error = %v", err)
	}

	blocker := &snapshotCollectionBlocker{
		collectionReadStarted: make(chan struct{}),
		releaseCollectionRead: make(chan struct{}),
	}
	driverName := fmt.Sprintf("workbook-snapshot-blocking-sqlite-%d", countingDriverSequence.Add(1))
	sql.Register(driverName, &snapshotBlockingDriver{inner: &sqlite.Driver{}, blocker: blocker})
	var err error
	store.db, err = sql.Open(
		driverName,
		store.cachePath+"?_pragma=busy_timeout%285000%29&_txlock=immediate",
	)
	if err != nil {
		t.Fatalf("open synchronized projection database error = %v", err)
	}
	if err := store.db.Ping(); err != nil {
		t.Fatalf("ping synchronized projection database error = %v", err)
	}
	t.Cleanup(func() {
		blocker.release()
		_ = store.db.Close()
	})
	return blocker
}

func (b *snapshotCollectionBlocker) release() {
	b.releaseOnce.Do(func() {
		close(b.releaseCollectionRead)
	})
}

func (s *exactRefreshRaceSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return nil, errors.New("unexpected global task-head listing")
}

func (s *exactRefreshRaceSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inspectCalls++
	snapshot := s.changed
	if s.inspectCalls > 1 {
		snapshot = s.newer
	}
	return gitstore.TaskHead{TaskID: taskID, ObjectID: snapshot.Head}, true, nil
}

func (s *exactRefreshRaceSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	s.mu.Lock()
	s.batchReadCalls++
	s.mu.Unlock()
	if len(heads) != 1 || heads[0].ObjectID != s.changed.Head {
		return nil, fmt.Errorf("unexpected exact read batch %#v", heads)
	}
	close(s.changedReadStarted)
	<-s.releaseChangedRead
	return []core.Snapshot{s.changed}, nil
}

func (s *exactRefreshRaceSource) ValidateTaskHeadAdvances(_ context.Context, _ core.ProjectConfig, _ []gitstore.HeadAdvance) error {
	return nil
}

func (s *concurrentRefreshSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headCalls++
	head := s.snapshots["head-2"]
	if s.headCalls > 1 {
		head = s.snapshots["head-3"]
		if s.headCalls == 2 {
			close(s.thirdHeadListed)
		}
	}
	return []gitstore.TaskHead{{TaskID: head.State.TaskID, ObjectID: head.Head}}, nil
}

func (s *concurrentRefreshSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.snapshots["head-3"]
	if head.State.TaskID != taskID {
		return gitstore.TaskHead{}, false, nil
	}
	return gitstore.TaskHead{TaskID: taskID, ObjectID: head.Head}, true, nil
}

func (s *concurrentRefreshSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		if head.ObjectID == "head-2" {
			close(s.secondReadStarted)
			<-s.releaseSecondRead
		}
		snapshots = append(snapshots, s.snapshots[head.ObjectID])
	}
	return snapshots, nil
}

func (s *concurrentRefreshSource) ValidateTaskHeadAdvances(_ context.Context, _ core.ProjectConfig, _ []gitstore.HeadAdvance) error {
	return nil
}

func (s *staticHeadSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	return append([]gitstore.TaskHead(nil), s.heads...), nil
}

func (s *staticHeadSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	for _, head := range s.heads {
		if head.TaskID == taskID {
			return head, true, nil
		}
	}
	return gitstore.TaskHead{}, false, nil
}

func (s *staticHeadSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, found := s.snapshots[head.ObjectID]
		if !found {
			return nil, errors.New("missing test snapshot")
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *staticHeadSource) ValidateTaskHeadAdvances(_ context.Context, _ core.ProjectConfig, _ []gitstore.HeadAdvance) error {
	return nil
}

func (d *queryCountingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &queryCountingConn{Conn: connection, queries: d.queries}, nil
}

func (c *queryCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *queryCountingConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (d *snapshotBlockingDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &snapshotBlockingConn{Conn: connection, blocker: d.blocker}, nil
}

func (c *snapshotBlockingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SELECT label FROM task_labels WHERE task_id") {
		c.blocker.startOnce.Do(func() {
			close(c.blocker.collectionReadStarted)
		})
		select {
		case <-c.blocker.releaseCollectionRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *snapshotBlockingConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (s *headSequenceSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	if len(s.headSets) == 0 {
		return nil, nil
	}
	index := s.index
	if index >= len(s.headSets) {
		index = len(s.headSets) - 1
	}
	s.index++
	return append([]gitstore.TaskHead(nil), s.headSets[index]...), nil
}

func (s *headSequenceSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	if len(s.headSets) == 0 {
		return gitstore.TaskHead{}, false, nil
	}
	index := s.index
	if index >= len(s.headSets) {
		index = len(s.headSets) - 1
	}
	for _, head := range s.headSets[index] {
		if head.TaskID == taskID {
			return head, true, nil
		}
	}
	return gitstore.TaskHead{}, false, nil
}

func (s *headSequenceSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, found := s.snapshots[head.ObjectID]
		if !found {
			return nil, errors.New("missing test snapshot")
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *headSequenceSource) ValidateTaskHeadAdvances(_ context.Context, _ core.ProjectConfig, _ []gitstore.HeadAdvance) error {
	return nil
}

func (s *countingHeadSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	s.listCalls++
	return append([]gitstore.TaskHead(nil), s.heads...), s.err
}

func (s *countingHeadSource) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	s.inspectCalls++
	for _, head := range s.heads {
		if head.TaskID == taskID {
			return head, true, nil
		}
	}
	return gitstore.TaskHead{}, false, nil
}

func (s *countingHeadSource) ReadTaskHeads(_ context.Context, _ core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	s.batchReadCalls++
	s.readBatches = append(s.readBatches, append([]gitstore.TaskHead(nil), heads...))
	if s.readErr != nil {
		return nil, s.readErr
	}
	snapshots := make([]core.Snapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, found := s.snapshots[head.ObjectID]
		if !found {
			return nil, errors.New("missing test snapshot")
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *countingHeadSource) ValidateTaskHeadAdvances(_ context.Context, _ core.ProjectConfig, advances []gitstore.HeadAdvance) error {
	s.validationCalls++
	s.validatedAdvances = append(s.validatedAdvances, append([]gitstore.HeadAdvance(nil), advances...))
	return s.validationErr
}

func resetSourceCalls(source *countingHeadSource) {
	source.listCalls = 0
	source.inspectCalls = 0
	source.batchReadCalls = 0
	source.validationCalls = 0
	source.readBatches = nil
	source.validatedAdvances = nil
}

func testConfig() core.ProjectConfig {
	return core.ProjectConfig{Format: "workbook.project", Version: 1, ProjectID: "01K0M6B8A4FTT8C39MXXYTW7D0", Key: "WB"}
}

func testSnapshot(taskID, head, title string) core.Snapshot {
	createdAt := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	return core.Snapshot{
		Head: head,
		State: core.StateDocument{
			Format: "workbook.task-state", Version: 1, ProjectID: testConfig().ProjectID, TaskID: taskID,
			History: core.History{Generation: "01K0M6B8A4FTT8C39MXXYTW7D9"}, LogicalClock: 1,
			Task: core.TaskData{Title: title, Status: core.StatusBacklog, Priority: core.PriorityMedium, Rank: "1/1", CreatedAt: createdAt, UpdatedAt: createdAt},
		},
	}
}

func initializeWorkbook(t *testing.T, root string) (*gitstore.Repository, core.ProjectConfig) {
	t.Helper()
	repository, err := gitstore.Open(context.Background(), root)
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

func createTask(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, title string) core.Task {
	t.Helper()
	ids := []string{"01K0M6B8A4FTT8C39MXXYTW7D1", "01K0M6B8A4FTT8C39MXXYTW7D9", "01K0M6B8A4FTT8C39MXXYTW7D8"}
	index := 0
	service := core.Service{Config: config, Reader: repository, Writer: repository, Actor: "test@example.test", Now: func() time.Time { return time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC) }, IDs: core.IDSourceFunc(func() (string, error) {
		value := ids[index]
		index++
		return value, nil
	})}
	result, err := service.CreateMutation(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	return result.Task
}

func updateTaskTitle(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, taskID, title string) {
	t.Helper()
	service := core.Service{Config: config, Reader: repository, Writer: repository, Actor: "test@example.test", Now: func() time.Time { return time.Date(2026, time.July, 26, 12, 1, 0, 0, time.UTC) }, IDs: core.IDSourceFunc(func() (string, error) {
		return "01K0M6B8A4FTT8C39MXXYTW7D7", nil
	})}
	if _, err := service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Title: &title}); err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
}

func validProjectionDatabase(t *testing.T, version, projectID string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projection.sqlite")
	if err := writeDatabaseMetadata(path, version, projectID); err != nil {
		t.Fatalf("writeDatabaseMetadata() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(foreign cache) error = %v", err)
	}
	return contents
}

func writeDatabaseMetadata(path, version, projectID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, version, projectID)
	return err
}

func writeIncompleteProjectionDatabase(path, projectID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO projection_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`, schemaVersion, projectID)
	return err
}
