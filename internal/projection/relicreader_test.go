package projection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// relicReaderFixture writes every relic shape the index has to answer for and
// returns their IDs: a deleted relic, one of 40 parts moved out of creation
// order, and one with a removed part, a bodiless part, an empty body, a binary
// body, nested property and field values and a body format other than md.
func relicReaderFixture(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) []string {
	t.Helper()
	ctx := context.Background()
	service := relicServiceForIndex(repository, config, 1000)

	deleted := createIndexRelic(t, service, "Deleted", 2)
	if _, err := service.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}

	large := createIndexRelic(t, service, "Large", 40)
	snapshot, err := repository.GetRelic(ctx, config, large)
	if err != nil {
		t.Fatal(err)
	}
	parts := snapshot.Record.State.Relic.Parts
	var moves []core.RelicEdit
	for index := 0; index < 10; index++ {
		moves = append(moves, core.RelicEdit{Kind: core.RelicEditMovePart, Part: parts[index].ID, After: parts[39-index].ID})
	}
	if _, err := service.Update(ctx, large, moves); err != nil {
		t.Fatalf("move parts: %v", err)
	}

	shapes, err := service.Create(ctx, core.RelicCreateInput{
		Title: "Shapes", Type: "spec",
		Properties: map[string]json.RawMessage{
			"meta":  json.RawMessage(`{"z":1,"a":[true,null,{"deep":"x"}]}`),
			"count": json.RawMessage(`1.5`),
		},
		Parts: []core.RelicPartInput{
			{Type: "section", Fields: map[string]json.RawMessage{"heading": relicIndexJSON(t, "Goals"), "nested": json.RawMessage(`{"b":[1,2],"a":{}}`)},
				Body: &core.RelicBodyInput{Format: "md", Content: []byte("# Goals\n")}},
			{Type: "note", Fields: map[string]json.RawMessage{"kind": relicIndexJSON(t, "aside")}},
			{Type: "empty", Body: &core.RelicBodyInput{Format: "txt", Content: []byte{}}},
			{Type: "binary", Body: &core.RelicBodyInput{Content: []byte{0xff, 0x00, 0xfe}}},
			{Type: "code", Body: &core.RelicBodyInput{Format: "go", Content: []byte("package main\n")}},
			{Type: "gone", Body: &core.RelicBodyInput{Format: "md", Content: []byte("removed\n")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gone := shapes.Parts[5].ID
	// Links from the whole relic, from a live part and from the part removed
	// below, to a live task and to one deleted afterwards.
	live, deletedTask := linkIndexTask(t, repository, config, 1), linkIndexTask(t, repository, config, 2)
	if _, err := service.Update(ctx, shapes.Relic.ID, []core.RelicEdit{
		{Kind: core.RelicEditAddLink, Task: live, Rel: "relates-to"},
		{Kind: core.RelicEditAddLink, Task: deletedTask, From: shapes.Parts[0].ID, Rel: "implemented-by"},
		{Kind: core.RelicEditAddLink, Task: live, From: gone, Rel: "decided-by"},
	}); err != nil {
		t.Fatal(err)
	}
	deleteIndexTask(t, repository, config, deletedTask)
	if _, err := service.Update(ctx, shapes.Relic.ID, []core.RelicEdit{{Kind: core.RelicEditRemovePart, Part: gone}}); err != nil {
		t.Fatal(err)
	}
	return []string{deleted, large, shapes.Relic.ID}
}

// relicAnswers runs every read the relic commands make through a fresh
// service on reader, and returns each answer, or its error, as text.
func relicAnswers(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, reader func() core.RelicReader, ids []string) []string {
	t.Helper()
	ctx := context.Background()
	service := func() core.RelicService {
		service := relicServiceForIndex(repository, config, 900000)
		service.Reader = reader()
		return service
	}
	encode := func(label string, value any, err error) string {
		if err != nil {
			return fmt.Sprintf("%s: error %s %s", label, core.CategoryOf(err), err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return label + ": " + string(encoded)
	}
	var answers []string
	for _, all := range []bool{false, true} {
		listed, err := service().List(ctx, core.RelicListFilter{IncludeDeleted: all})
		answers = append(answers, encode(fmt.Sprintf("list all=%t", all), listed, err))
	}
	for _, relicID := range ids {
		for _, options := range []core.RelicShowOptions{{}, {Bodies: true}, {Removed: true}, {Bodies: true, Removed: true}} {
			document, err := service().Show(ctx, relicID, options)
			answers = append(answers, encode(fmt.Sprintf("show %s %+v", relicID, options), document, err))
		}
		exported, err := service().Export(ctx, relicID)
		answers = append(answers, encode("export "+relicID, exported, err))
		snapshot, err := repository.GetRelic(ctx, config, relicID)
		if err != nil {
			answers = append(answers, encode("get "+relicID, nil, err))
			continue
		}
		for _, part := range snapshot.Record.State.Relic.Parts {
			for _, bodies := range []bool{false, true} {
				address := relicID + "#" + part.ID
				document, err := service().Show(ctx, address, core.RelicShowOptions{Bodies: bodies})
				answers = append(answers, encode(fmt.Sprintf("show %s bodies=%t", address, bodies), document, err))
			}
		}
	}
	return answers
}

func assertSameAnswers(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d answers, want %d", label, len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s: answer %d =\n%s\nwant\n%s", label, index, got[index], want[index])
		}
	}
}

func TestIndexAnswersEqualGitAnswers(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ids := relicReaderFixture(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		fromGit := relicAnswers(t, repository, config, func() core.RelicReader { return repository }, ids)

		if err := os.Remove(store.CachePath()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		cold := relicAnswers(t, repository, config, func() core.RelicReader { return store.RelicReader() }, ids)
		assertSameAnswers(t, "cold", cold, fromGit)
		warm := relicAnswers(t, repository, config, func() core.RelicReader { return store.RelicReader() }, ids)
		assertSameAnswers(t, "warm", warm, fromGit)
		tasks, err := repository.List(context.Background(), config)
		if err != nil || len(tasks) != 2 {
			t.Fatalf("tasks = %d, %v; want the fixture's two", len(tasks), err)
		}
		for _, task := range tasks {
			taskID := task.State.TaskID
			if got, want := mustLookupJSON(t, relicLinksTo(t, store, config, taskID)), lookupFromGit(t, repository, config, taskID); got != want {
				t.Fatalf("RelicLinksTo(%s) =\n%s\nwant\n%s", taskID, got, want)
			}
		}
		if got := countRows(t, store, `SELECT COUNT(*) FROM relics`); got != len(ids) {
			t.Fatalf("the index holds %d relics, want %d", got, len(ids))
		}
	})
}

func TestWarmRelicShowReadsNoRelicObjects(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	ids := relicReaderFixture(t, repository, config)
	source := newCountingRelicSource(repository)
	store := openRelicStore(t, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	show := func(options core.RelicShowOptions) {
		t.Helper()
		service := relicServiceForIndex(repository, config, 900000)
		service.Reader = store.RelicReader()
		if _, err := service.Show(ctx, ids[2], options); err != nil {
			t.Fatalf("Show() error = %v", err)
		}
	}
	show(core.RelicShowOptions{})

	source.reset()
	show(core.RelicShowOptions{})
	if source.listCalls != 1 || source.relicCalls() != 1 {
		t.Fatalf("a warm show made %d relic calls, want one ListRelicHeads", source.relicCalls())
	}
	source.reset()
	show(core.RelicShowOptions{Bodies: true, Removed: true})
	if source.listCalls != 1 || source.bodyCalls != 1 || source.relicCalls() != 2 {
		t.Fatalf("a warm show with bodies made %d relic calls (%d bodies), want a listing and one body read",
			source.relicCalls(), source.bodyCalls)
	}
}

func TestDamagedAndNewerRelicsReadAsTheyDoFromGit(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	service := relicServiceForIndex(repository, config, 1000)
	createIndexRelic(t, service, "Sound", 2)
	damaged := createIndexRelic(t, service, "Damaged", 2)
	updateIndexRelicTitle(t, service, damaged, "Changed")
	newer := createIndexRelic(t, service, "Newer", 2)
	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if _, err := store.RebuildWithRelics(context.Background()); err != nil {
		t.Fatal(err)
	}
	damageRelic(t, repository, damaged)
	makeNewerRelic(t, repository, config, newer)

	ids := []string{damaged, newer}
	fromGit := relicAnswers(t, repository, config, func() core.RelicReader { return repository }, ids)
	assertSameAnswers(t, "index", relicAnswers(t, repository, config, func() core.RelicReader { return store.RelicReader() }, ids), fromGit)
	assertSameAnswers(t, "again", relicAnswers(t, repository, config, func() core.RelicReader { return store.RelicReader() }, ids), fromGit)
	var refused bool
	for _, answer := range fromGit {
		if bytes.Contains([]byte(answer), []byte("error "+core.CategoryCorruptData)) {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("no read of the damaged relic was refused: %q", fromGit)
	}
}

func TestRelicReaderAnswersFromGitWhenTheRowMoves(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	ctx := context.Background()
	service := relicServiceForIndex(repository, config, 1000)
	relicID := createIndexRelic(t, service, "Moving", 3)
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	store := openRelicStore(t, repositorySource{repository: repository}, config, cachePath)
	other := openRelicStore(t, repositorySource{repository: repository}, config, cachePath)
	runRelicPass(t, store)

	updateIndexRelicTitle(t, service, relicID, "Listed")
	listed := relicRefHeads(t, repository, config)[relicID]
	// Between this pass's Git read and its write, another change lands and a
	// second store indexes it.
	store.beforeRelicWrite = func() {
		store.beforeRelicWrite = nil
		updateIndexRelicTitle(t, service, relicID, "Newer")
		runRelicPass(t, other)
	}
	reading := service
	reading.Reader = store.RelicReader()
	document, err := reading.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if document.Head != listed || document.Title != "Listed" {
		t.Fatalf("Show() = %s %q, want the listed head %s", document.Head, document.Title, listed)
	}
	newer := relicRefHeads(t, repository, config)[relicID]
	if got := relicRowHeads(t, store)[relicID]; got != newer {
		t.Fatalf("row head = %s, want the newer %s the other store wrote", got, newer)
	}
	// The same reader keeps answering from the pass it ran.
	summaries, err := reading.List(ctx, core.RelicListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Head != listed {
		t.Fatalf("List() = %+v, want the listed head %s", summaries, listed)
	}
}

// damageTaskTip points a task's ref at a blob, so a task rebuild fails.
func damageTaskTip(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
	t.Helper()
	created := createTask(t, repository, config, "Damaged task")
	blob := relicIndexGit(t, repository, []byte("not a commit\n"), "hash-object", "-w", "--stdin")
	relicIndexGit(t, repository, nil, "update-ref", "refs/workbook/tasks/"+created.ID, blob)
}

// A relic read on a cache that cannot be built, here because a task tip is
// damaged, answers from Git exactly as a read without an index does.
func TestRelicReadsAnswerFromGitWhenTheIndexCannotBeBuilt(t *testing.T) {
	t.Parallel()
	forBothRelicIndexFormats(t, func(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig) {
		ids := relicReaderFixture(t, repository, config)
		damaged := createIndexRelic(t, relicServiceForIndex(repository, config, 5000), "Damaged", 2)
		updateIndexRelicTitle(t, relicServiceForIndex(repository, config, 6000), damaged, "Changed")
		damageRelic(t, repository, damaged)
		ids = append(ids, damaged)
		damageTaskTip(t, repository, config)
		store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
		if _, err := store.Rebuild(context.Background()); err == nil {
			t.Fatal("Rebuild() succeeded; the task damage did not take")
		}
		if err := os.Remove(store.CachePath()); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}

		fromGit := relicAnswers(t, repository, config, func() core.RelicReader { return repository }, ids)
		assertSameAnswers(t, "index", relicAnswers(t, repository, config, func() core.RelicReader { return store.RelicReader() }, ids), fromGit)

		var refused bool
		for _, answer := range fromGit {
			refused = refused || strings.Contains(answer, "error "+string(core.CategoryCorruptData))
		}
		if !refused {
			t.Fatalf("no read of the damaged relic was refused: %q", fromGit)
		}
		for _, relicID := range ids {
			got, gotErr := store.RelicReader().GetRelic(context.Background(), config, relicID)
			want, wantErr := repository.GetRelic(context.Background(), config, relicID)
			if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || got.Head != want.Head {
				t.Fatalf("GetRelic(%s) = %s, %v; want %s, %v", relicID, got.Head, gotErr, want.Head, wantErr)
			}
		}
	})
}

// cancellingTaskSource cancels the read it serves when the task rebuild reads
// task tips, as a command interrupted while its index is being built is.
type cancellingTaskSource struct {
	*countingRelicSource
	cancel context.CancelFunc
}

func (s cancellingTaskSource) ReadTaskHeads(ctx context.Context, config core.ProjectConfig, heads []gitstore.TaskHead) ([]core.Snapshot, error) {
	s.cancel()
	return nil, ctx.Err()
}

func TestCancelledRelicReadIsNotAnsweredFromGit(t *testing.T) {
	t.Parallel()
	repository, config := initializeWorkbook(t)
	createIndexRelic(t, relicServiceForIndex(repository, config, 1000), "Relic", 2)
	createTask(t, repository, config, "A task")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counting := newCountingRelicSource(repository)
	store := openRelicStore(t, cancellingTaskSource{countingRelicSource: counting, cancel: cancel}, config,
		filepath.Join(t.TempDir(), "cache.sqlite"))

	_, err := store.RelicReader().ListRelicHeads(ctx, config)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListRelicHeads() error = %v, want the context's", err)
	}
	if calls := counting.relicCalls(); calls != 0 {
		t.Fatalf("a cancelled read made %d relic calls, want none", calls)
	}
}
