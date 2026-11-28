package gitstore

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicChainOf reads relicID's chain at its ref.
func relicChainOf(t *testing.T, repo *Repository, config core.ProjectConfig, relicID string) core.RelicChain {
	t.Helper()
	head := relicRefHead(t, repo, relicID)
	chain, err := repo.RelicChain(context.Background(), config, core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil {
		t.Fatalf("RelicChain(%s) error = %v", relicID, err)
	}
	return chain
}

// relicCurrentBody returns the body version a part holds at the relic's tip.
func relicCurrentBody(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, partID string) string {
	t.Helper()
	snapshot, err := repo.GetRelic(context.Background(), config, relicID)
	if err != nil {
		t.Fatalf("GetRelic(%s) error = %v", relicID, err)
	}
	part, found := snapshot.Record.State.Relic.Part(partID)
	if !found || part.Body == nil {
		t.Fatalf("relic %s has no body for part %s", relicID, partID)
	}
	return part.Body.Version
}

func lastRelicOperationID(commit core.RelicChainCommit) string {
	operations := commit.Operation.Operations
	return operations[len(operations)-1].ID
}

// historyRelic creates a relic of two parts with bodies and one without, and
// then saves the first part's body, sets a field of the second and changes
// the title: four commits.
func historyRelic(t *testing.T, service core.RelicService) (relicID string, parts []string) {
	t.Helper()
	ctx := context.Background()
	created, err := service.Create(ctx, core.RelicCreateInput{
		Title: "History", Type: "paper",
		Parts: []core.RelicPartInput{
			{Type: "section", Body: &core.RelicBodyInput{Format: "md", Content: []byte("one\ntwo\n")}},
			{Type: "section", Body: &core.RelicBodyInput{Format: "md", Content: []byte("second\n")}},
			{Type: "note", Fields: map[string]json.RawMessage{"heading": json.RawMessage(`"<a&b>"`)}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	relicID = created.Relic.ID
	for _, part := range created.Parts {
		parts = append(parts, part.ID)
	}
	edits := [][]core.RelicEdit{
		{{Kind: core.RelicEditPart, Part: parts[0], Base: parts[0], Body: &core.RelicBodyInput{Format: "md", Content: []byte("one\n2\n")}}},
		{{Kind: core.RelicEditPart, Part: parts[1], Fields: []core.RelicFieldEdit{{Name: "heading", Value: json.RawMessage(`"Second"`)}}}},
		{{Kind: core.RelicEditTitle, Title: "History, again"}},
	}
	for _, edit := range edits {
		if _, err := service.Update(ctx, relicID, edit); err != nil {
			t.Fatalf("Update(%+v) error = %v", edit, err)
		}
	}
	return relicID, parts
}

func TestRelicChainReadsTheFirstParentChain(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		relicID, _ := historyRelic(t, relicServiceFor(repo, config, 100000))
		head := relicRefHead(t, repo, relicID)
		chain := relicChainOf(t, repo, config, relicID)
		if chain.RelicID != relicID || chain.Head != head || chain.Truncated != nil {
			t.Fatalf("chain = %s at %s, truncated %+v; want %s at %s, whole", chain.RelicID, chain.Head, chain.Truncated, relicID, head)
		}
		newestFirst := relicLines(gitOutput(t, repo, "rev-list", "--first-parent", head))
		if len(chain.Commits) != 4 || len(newestFirst) != 4 {
			t.Fatalf("chain has %d commits and Git lists %d, want 4", len(chain.Commits), len(newestFirst))
		}
		for index, commit := range chain.Commits {
			if want := newestFirst[len(newestFirst)-1-index]; commit.Commit != want {
				t.Fatalf("chain commit %d = %s, want %s", index, commit.Commit, want)
			}
			wantParent := ""
			if index > 0 {
				wantParent = chain.Commits[index-1].Commit
			}
			if commit.Parent != wantParent {
				t.Fatalf("chain commit %d has parent %q, want %q", index, commit.Parent, wantParent)
			}
			pack, err := core.DecodeRelicOperationPack(relicBlob(t, repo, commit.Commit+":operation.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(commit.Operation, pack) {
				t.Fatalf("chain commit %d pack =\n%+v\nwant\n%+v", index, commit.Operation, pack)
			}
		}
	})
}

func TestReadRelicVersionsReturnsPartObjects(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceFor(repo, config, 100000)
		relicID, parts := historyRelic(t, service)
		chain := relicChainOf(t, repo, config, relicID)
		commits := []string{chain.Commits[0].Commit, chain.Commits[2].Commit}
		versions, err := repo.ReadRelicVersions(ctx, config, relicID, commits)
		if err != nil {
			t.Fatalf("ReadRelicVersions() error = %v", err)
		}
		if len(versions) != len(commits) {
			t.Fatalf("ReadRelicVersions() returned %d versions, want %d", len(versions), len(commits))
		}
		for index, version := range versions {
			commit := commits[index]
			if version.Snapshot.Head != commit || version.Snapshot.Record.State.RelicID != relicID {
				t.Fatalf("version %d is %s of %s, want %s of %s", index,
					version.Snapshot.Head, version.Snapshot.Record.State.RelicID, commit, relicID)
			}
			if !reflect.DeepEqual(version.Snapshot.Operation, chain.Commits[index*2].Operation) {
				t.Fatalf("version %d operation = %+v, want the chain's", index, version.Snapshot.Operation)
			}
			if len(version.Parts) != len(parts) {
				t.Fatalf("version %d has %d parts, want %d", index, len(version.Parts), len(parts))
			}
			for _, partID := range parts {
				want := core.RelicPartObjectIDs{
					Fields: gitOutput(t, repo, "rev-parse", commit+":parts/"+partID+"/fields.json"),
				}
				if partID != parts[2] {
					want.Body = gitOutput(t, repo, "rev-parse", commit+":parts/"+partID+"/body")
				}
				if got := version.Parts[partID]; got != want {
					t.Fatalf("version %d part %s objects = %+v, want %+v", index, partID, got, want)
				}
			}
		}
		if versions[0].Parts[parts[0]].Body == versions[1].Parts[parts[0]].Body {
			t.Fatal("the saved body has the same object in both versions")
		}
		if versions[0].Parts[parts[1]].Fields == versions[1].Parts[parts[1]].Fields {
			t.Fatal("the edited fields have the same object in both versions")
		}

		other, _ := historyRelic(t, relicServiceFor(repo, config, 200000))
		otherHead := relicRefHead(t, repo, other)
		tree := gitOutput(t, repo, "rev-parse", chain.Head+"^{tree}")
		for _, commit := range []string{otherHead, tree} {
			_, err := repo.ReadRelicVersions(ctx, config, relicID, []string{chain.Head, commit})
			assertRelicCategory(t, err, core.CategoryCorruptData, relicID)
		}
	})
}

// writeRelicChildAs is writeRelicChild for an actor and a wall time of its
// own.
func writeRelicChildAs(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	parent core.RelicSnapshot,
	actor string,
	wallTime time.Time,
	content map[string]core.RelicPartContent,
	operations ...core.RelicOperation,
) core.RelicSnapshot {
	t.Helper()
	state := parent.Record.State
	pack := core.NewRelicOperationPack(state.ProjectID, state.RelicID, state.History.Generation, actor,
		state.LogicalClock+1, wallTime, operations)
	record := foldRelic(t, &parent, pack)
	snapshot, err := repo.WriteRelic(context.Background(), config, &parent, pack, record, content, "update relic "+pack.RelicID)
	if err != nil {
		t.Fatalf("WriteRelic(update %s) error = %v", pack.RelicID, err)
	}
	return snapshot
}

func TestRelicHistoryServiceAfterAReplay(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		one, two := relicTestPartNumbered(1), relicTestPartNumbered(2)
		shared := sharedRelic(t, first, second, config, 1,
			one.withBody("md", []byte("one\n")), two.withBody("md", []byte("two\n")))
		relicID := shared.Operation.RelicID
		theirTime := relicTestTime.Add(time.Hour)
		ourTime := relicTestTime.Add(2 * time.Hour)
		theirBody, ourBody := []byte("one, theirs\n"), []byte("two, ours\n")
		theirs := writeRelicChildAs(t, first, config, shared, "them@example.test", theirTime,
			map[string]core.RelicPartContent{one.id: {Body: theirBody, HasBody: true}},
			relicBodySet(700001, one.id, one.id, theirBody))
		ours := writeRelicChildAs(t, second, config, shared, "us@example.test", ourTime,
			map[string]core.RelicPartContent{two.id: {Body: ourBody, HasBody: true}},
			relicBodySet(700002, two.id, two.id, ourBody))
		publishRelicRefs(t, first)
		assertRelicSyncOutcome(t, fetchRelics(t, second, config), relicID, SyncReconciled)
		replayed := relicRefHead(t, second, relicID)
		if replayed == ours.Head || replayed == theirs.Head {
			t.Fatalf("the relic ref is at %s after the replay, want a new commit", replayed)
		}

		service := relicServiceFor(second, config, 100000)
		service.HistoryReader = second
		history, err := service.History(ctx, relicID, core.RelicHistoryOptions{All: true})
		if err != nil {
			t.Fatalf("History() error = %v", err)
		}
		if history.Relic.Head != replayed || history.Total != 3 || history.Showing != 3 || history.Truncated != nil {
			t.Fatalf("history of %s: head %s, total %d, showing %d, truncated %+v; want head %s and 3 changes",
				relicID, history.Relic.Head, history.Total, history.Showing, history.Truncated, replayed)
		}
		seen := map[string]core.RelicChange{}
		for _, change := range history.Changes {
			for _, operation := range change.Operations {
				if _, again := seen[operation.ID]; again {
					t.Fatalf("operation %s is listed twice", operation.ID)
				}
				seen[operation.ID] = change
			}
		}
		want := []string{relicTestID(800001), one.id, two.id, relicTestID(700001), relicTestID(700002)}
		if got := slices.Sorted(func(yield func(string) bool) {
			for id := range seen {
				if !yield(id) {
					return
				}
			}
		}); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
			t.Fatalf("history lists operations %v, want %v", got, want)
		}
		if change := seen[relicTestID(700001)]; change.Actor != "them@example.test" || !change.WallTime.Equal(theirTime) || change.Commit != theirs.Head {
			t.Fatalf("their change = %+v, want by them at %s in %s", change, theirTime, theirs.Head)
		}
		change := seen[relicTestID(700002)]
		if change.Actor != "us@example.test" || !change.WallTime.Equal(ourTime) || change.Commit != replayed {
			t.Fatalf("our replayed change = %+v, want by us at %s in %s", change, ourTime, replayed)
		}
		if applied := change.Operations[0].Applied; applied == nil || !*applied {
			t.Fatalf("our replayed save is not applied: %+v", change.Operations[0])
		}
	})
}

// partReadCountingReader is a relic reader that records each ReadRelicParts
// call.
type partReadCountingReader struct {
	core.RelicReader
	mu    sync.Mutex
	calls []partRead
}

type partRead struct {
	head   string
	parts  []string
	bodies bool
}

func (r *partReadCountingReader) ReadRelicParts(
	ctx context.Context, config core.ProjectConfig, snapshot core.RelicSnapshot, partIDs []string, bodies bool,
) (map[string]core.RelicPartContent, error) {
	r.mu.Lock()
	r.calls = append(r.calls, partRead{head: snapshot.Head, parts: slices.Clone(partIDs), bodies: bodies})
	r.mu.Unlock()
	return r.RelicReader.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
}

func (r *partReadCountingReader) take() []partRead {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

func TestRelicDiffServiceReadsOnlyChangedParts(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	service := relicServiceFor(repo, config, 100000)
	input := core.RelicCreateInput{Title: "Large", Type: "paper"}
	for index := range 200 {
		input.Parts = append(input.Parts, core.RelicPartInput{
			Type:   "section",
			Fields: map[string]json.RawMessage{"heading": relicJSONString(t, strings.Repeat("h", index%7+1))},
			Body:   &core.RelicBodyInput{Format: "md", Content: []byte(strings.Repeat("line\n", index%5+1))},
		})
	}
	created, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	relicID := created.Relic.ID
	bodyPart, fieldPart := created.Parts[57].ID, created.Parts[58].ID
	counting := &partReadCountingReader{RelicReader: repo}
	reader := service
	reader.Reader, reader.HistoryReader = counting, repo

	assertReads := func(label, partID string, bodies bool, wantHeads []string) {
		t.Helper()
		calls := counting.take()
		if len(calls) != len(wantHeads) {
			t.Fatalf("%s: %d ReadRelicParts calls %+v, want %d", label, len(calls), calls, len(wantHeads))
		}
		for index, call := range calls {
			if call.head != wantHeads[index] || !slices.Equal(call.parts, []string{partID}) || call.bodies != bodies {
				t.Fatalf("%s: call %d = %+v, want part %s at %s with bodies %t", label, index, call, partID, wantHeads[index], bodies)
			}
		}
	}

	if _, err := service.Update(ctx, relicID, []core.RelicEdit{{
		Kind: core.RelicEditPart, Part: bodyPart, Base: relicCurrentBody(t, repo, config, relicID, bodyPart),
		Body: &core.RelicBodyInput{Format: "md", Content: []byte("line\nchanged\n")},
	}}); err != nil {
		t.Fatal(err)
	}
	chain := relicChainOf(t, repo, config, relicID)
	counting.take()
	diff, err := reader.Diff(ctx, relicID, nil, core.RelicDiffOptions{Context: 3})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.Parts) != 1 || diff.Parts[0].ID != bodyPart || diff.Parts[0].Body == nil {
		t.Fatalf("Diff() parts = %+v, want the body of %s", diff.Parts, bodyPart)
	}
	assertReads("body edit", bodyPart, true, []string{chain.Commits[0].Commit, chain.Commits[1].Commit})

	if _, err := service.Update(ctx, relicID, []core.RelicEdit{{
		Kind: core.RelicEditPart, Part: fieldPart, Fields: []core.RelicFieldEdit{{Name: "heading", Value: relicJSONString(t, "New")}},
	}}); err != nil {
		t.Fatal(err)
	}
	chain = relicChainOf(t, repo, config, relicID)
	counting.take()
	diff, err = reader.Diff(ctx, relicID, nil, core.RelicDiffOptions{Context: 3})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(diff.Parts) != 1 || diff.Parts[0].ID != fieldPart || diff.Parts[0].Body != nil || len(diff.Parts[0].Fields) != 1 {
		t.Fatalf("Diff() parts = %+v, want one field of %s", diff.Parts, fieldPart)
	}
	assertReads("field edit", fieldPart, false, []string{chain.Commits[1].Commit, chain.Commits[2].Commit})
}

func TestRelicDiffServicePoints(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	service := relicServiceFor(repo, config, 100000)
	service.HistoryReader = repo
	relicID, parts := historyRelic(t, service)
	chain := relicChainOf(t, repo, config, relicID)
	root, saved, fielded, titled := chain.Commits[0], chain.Commits[1], chain.Commits[2], chain.Commits[3]

	point := func(commit core.RelicChainCommit, version string) *core.RelicDiffPoint {
		return &core.RelicDiffPoint{
			Version: version, Commit: commit.Commit, Actor: commit.Operation.Actor.ID, WallTime: commit.Operation.WallTime,
		}
	}
	createID := root.Operation.Operations[0].ID
	cases := []struct {
		name     string
		address  string
		versions []string
		from, to *core.RelicDiffPoint
	}{
		{"latest change", relicID, nil, point(fielded, lastRelicOperationID(fielded)), point(titled, lastRelicOperationID(titled))},
		{"latest change of a part", relicID + "#" + parts[0], nil, point(root, lastRelicOperationID(root)), point(saved, lastRelicOperationID(saved))},
		{"one version", relicID, []string{createID}, point(root, createID), point(titled, lastRelicOperationID(titled))},
		{"a lowercase prefix", relicID, []string{strings.ToLower(lastRelicOperationID(saved))}, point(saved, lastRelicOperationID(saved)), point(titled, lastRelicOperationID(titled))},
		{"two versions newest first", relicID, []string{lastRelicOperationID(titled), parts[1]}, point(titled, lastRelicOperationID(titled)), point(root, parts[1])},
	}
	for _, test := range cases {
		diff, err := service.Diff(ctx, test.address, test.versions, core.RelicDiffOptions{Context: 3})
		if err != nil {
			t.Fatalf("%s: Diff() error = %v", test.name, err)
		}
		if diff.Relic != relicID || !reflect.DeepEqual(diff.From, test.from) || diff.To != *test.to {
			t.Fatalf("%s: Diff() compares %+v to %+v, want %+v to %+v", test.name, diff.From, diff.To, test.from, test.to)
		}
	}

	// A relic of one commit is compared with the side before it.
	single, err := service.Create(ctx, core.RelicCreateInput{Title: "Single", Type: "paper"})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := service.Diff(ctx, single.Relic.ID, nil, core.RelicDiffOptions{Context: 3})
	if err != nil {
		t.Fatalf("Diff(one commit) error = %v", err)
	}
	if diff.From != nil || diff.To.Commit != single.Relic.Head || len(diff.Changes) == 0 {
		t.Fatalf("Diff(one commit) = %+v, want from null to %s with the title added", diff, single.Relic.Head)
	}

	_, err = service.Diff(ctx, relicID, []string{createID, createID, createID}, core.RelicDiffOptions{})
	assertRelicCategory(t, err, core.CategoryInvocation)
	singleCreate := relicChainOf(t, repo, config, single.Relic.ID).Commits[0].Operation.Operations[0].ID
	_, err = service.Diff(ctx, relicID, []string{singleCreate}, core.RelicDiffOptions{})
	assertRelicCategory(t, err, core.CategoryNotFound, relicID, "not a Git commit")
	_, err = service.Diff(ctx, relicID, []string{titled.Commit}, core.RelicDiffOptions{})
	assertRelicCategory(t, err, core.CategoryNotFound, relicID)

	noHistory := relicServiceFor(repo, config, 300000)
	_, err = noHistory.Diff(ctx, relicID, nil, core.RelicDiffOptions{})
	assertRelicCategory(t, err, core.CategoryOperational, "no history reader")
	_, err = noHistory.History(ctx, relicID, core.RelicHistoryOptions{})
	assertRelicCategory(t, err, core.CategoryOperational, "no history reader")
}
