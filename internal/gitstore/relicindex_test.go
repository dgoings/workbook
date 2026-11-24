package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// indexFixtureRelic writes relic number with every part shape the index
// holds: a body, no body, an empty body and a binary body, with the bodiless
// part then removed. It returns the tip.
func indexFixtureRelic(t *testing.T, repo *Repository, config core.ProjectConfig, number int) core.RelicSnapshot {
	t.Helper()
	root := writeRelicRootNumbered(t, repo, config, number,
		relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte(fmt.Sprintf("# Goals of %d\n", number))),
		relicTestPartNumbered(2).withField("status", "open"),
		relicTestPartNumbered(3).withBody("txt", []byte{}),
		relicTestPartNumbered(4).withBody("", []byte{0xff, 0x00, byte(number)}),
	)
	return writeRelicChild(t, repo, config, root, nil,
		relicPartChange(700000+number, core.RelicOperationPartRemove, relicTestID(2)))
}

func indexRequest(snapshot core.RelicSnapshot) RelicIndexRequest {
	return RelicIndexRequest{Head: core.RelicHead{RelicID: snapshot.Operation.RelicID, ObjectID: snapshot.Head}}
}

func TestReadIndexedRelicsMatchesGetRelic(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := indexFixtureRelic(t, repo, config, 1)
		relicID := tip.Operation.RelicID

		read, errs := repo.ReadIndexedRelics(ctx, config, []RelicIndexRequest{indexRequest(tip)})
		if len(read) != 1 || len(errs) != 1 || errs[0] != nil {
			t.Fatalf("ReadIndexedRelics() = %d relics, errors %v", len(read), errs)
		}
		want, err := repo.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() error = %v", err)
		}
		assertSameRelicSnapshot(t, read[0].Snapshot, want)

		parts := tip.Record.State.Relic.Parts
		if len(read[0].Parts) != len(parts) || len(read[0].Fields) != len(parts) {
			t.Fatalf("read %d part objects and %d fields, want %d of each", len(read[0].Parts), len(read[0].Fields), len(parts))
		}
		for _, part := range parts {
			objects, found := read[0].Parts[part.ID]
			if !found {
				t.Fatalf("part %s has no objects", part.ID)
			}
			prefix := tip.Head + ":parts/" + part.ID
			if objects.Tree != gitOutput(t, repo, "rev-parse", prefix) ||
				objects.Fields != gitOutput(t, repo, "rev-parse", prefix+"/fields.json") {
				t.Fatalf("part %s objects = %+v, want the commit's", part.ID, objects)
			}
			if part.Body == nil {
				if objects.Body != "" {
					t.Fatalf("bodiless part %s has body object %s", part.ID, objects.Body)
				}
			} else if objects.Body != gitOutput(t, repo, "rev-parse", prefix+"/body") || objects.BodyBytes != part.Body.Bytes {
				t.Fatalf("part %s body = %s of %d bytes, want the commit's", part.ID, objects.Body, objects.BodyBytes)
			}
			if got, want := read[0].Fields[part.ID], relicBlob(t, repo, prefix+"/fields.json"); !bytes.Equal(got, want) {
				t.Fatalf("part %s fields = %q, want %q", part.ID, got, want)
			}
		}
	})
}

func TestReadIndexedRelicsSkipsKnownFields(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	tip := indexFixtureRelic(t, repo, config, 1)
	request := indexRequest(tip)
	request.KnownFields = map[string]struct{}{}
	for _, part := range tip.Record.State.Relic.Parts {
		request.KnownFields[gitOutput(t, repo, "rev-parse", tip.Head+":parts/"+part.ID+"/fields.json")] = struct{}{}
	}

	requests := observeRelicObjects(repo)
	read, errs := repo.ReadIndexedRelics(ctx, config, []RelicIndexRequest{request})
	repo.relicObjectObserver = nil
	if errs[0] != nil {
		t.Fatalf("ReadIndexedRelics() error = %v", errs[0])
	}
	if len(read[0].Fields) != 0 {
		t.Fatalf("Fields = %v, want none read", read[0].Fields)
	}
	for _, asked := range *requests {
		if _, known := request.KnownFields[asked]; known {
			t.Fatalf("ReadIndexedRelics() asked for known fields object %s", asked)
		}
	}
}

func TestReadIndexedRelicsUsesAFixedNumberOfGitProcesses(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	requests := make([]RelicIndexRequest, 25)
	for index := range requests {
		requests[index] = indexRequest(indexFixtureRelic(t, repo, config, index+1))
	}
	// The first read learns what a repository learns once.
	if _, errs := repo.ReadIndexedRelics(ctx, config, requests[:1]); errs[0] != nil {
		t.Fatalf("ReadIndexedRelics() error = %v", errs[0])
	}

	commands := observeCommands(repo)
	count := func(requests []RelicIndexRequest) int {
		t.Helper()
		*commands = nil
		_, errs := repo.ReadIndexedRelics(ctx, config, requests)
		for index, err := range errs {
			if err != nil {
				t.Fatalf("ReadIndexedRelics() error for %d = %v", index, err)
			}
		}
		return len(*commands)
	}
	one, many := count(requests[:1]), count(requests)
	repo.commandObserver = nil
	if one != many || many > 6 {
		t.Fatalf("reading 1 relic ran %d Git commands and reading 25 ran %d, want the same and at most 6", one, many)
	}
}

func TestReadIndexedRelicsIsolatesADamagedRelic(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	first := indexFixtureRelic(t, repo, config, 1)
	second := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withField("status", "open"))
	third := indexFixtureRelic(t, repo, config, 3)

	// A title change whose stored state keeps the old title.
	pack := relicChildPack(second, relicTitleSet(700100, "A new title"))
	damaged := forgeRelicCommit(t, repo, second.Head,
		encodedRelicDocument(t, pack), encodedRelicDocument(t, second.Record.State), forgedPartsOf(t, repo, second.Head))

	requests := []RelicIndexRequest{
		indexRequest(first),
		{Head: core.RelicHead{RelicID: second.Operation.RelicID, ObjectID: damaged}},
		indexRequest(third),
	}
	read, errs := repo.ReadIndexedRelics(ctx, config, requests)
	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("the sound relics failed: %v, %v", errs[0], errs[2])
	}
	assertSameRelicSnapshot(t, read[0].Snapshot, first)
	assertSameRelicSnapshot(t, read[2].Snapshot, third)
	assertRelicCategory(t, errs[1], core.CategoryCorruptData, second.Operation.RelicID)
	if read[1].Snapshot.Head != "" || read[1].Parts != nil || read[1].Fields != nil {
		t.Fatalf("the damaged relic was returned all the same: %+v", read[1])
	}
}

func TestReadIndexedRelicsReportsANewerWriter(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	sound := indexFixtureRelic(t, repo, config, 1)
	root := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withBody("md", []byte("a body\n")))
	newer := forgeNewerRelicTip(t, repo, config, root)

	read, errs := repo.ReadIndexedRelics(ctx, config, []RelicIndexRequest{
		indexRequest(sound),
		{Head: core.RelicHead{RelicID: root.Operation.RelicID, ObjectID: newer}},
	})
	if errs[0] != nil {
		t.Fatalf("the sound relic failed: %v", errs[0])
	}
	assertSameRelicSnapshot(t, read[0].Snapshot, sound)
	assertRelicCategory(t, errs[1], core.CategoryNewerWriter, root.Operation.RelicID)
}

func TestReadIndexedRelicsReadsNoBody(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	var requests []RelicIndexRequest
	bodies := map[string]struct{}{}
	for number := 1; number <= 5; number++ {
		tip := indexFixtureRelic(t, repo, config, number)
		requests = append(requests, indexRequest(tip))
		for _, part := range tip.Record.State.Relic.Parts {
			if part.Body != nil {
				bodies[gitOutput(t, repo, "rev-parse", tip.Head+":parts/"+part.ID+"/body")] = struct{}{}
			}
		}
	}

	asked := observeRelicObjects(repo)
	_, errs := repo.ReadIndexedRelics(ctx, config, requests)
	repo.relicObjectObserver = nil
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ReadIndexedRelics() error for %d = %v", index, err)
		}
	}
	for _, request := range *asked {
		if _, body := bodies[request]; body || strings.HasSuffix(request, "/body") {
			t.Fatalf("ReadIndexedRelics() asked for body %s", request)
		}
	}
}

func historyRequest(snapshot core.RelicSnapshot, stopAt string) RelicHistoryRequest {
	return RelicHistoryRequest{
		Head:   core.RelicHead{RelicID: snapshot.Operation.RelicID, ObjectID: snapshot.Head},
		StopAt: stopAt,
	}
}

func assertOperationCommits(t *testing.T, got []RelicOperationCommit, want ...core.RelicSnapshot) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("read %d commits, want %d: %+v", len(got), len(want), got)
	}
	for index, snapshot := range want {
		if got[index].ObjectID != snapshot.Head {
			t.Fatalf("commit %d = %s, want %s", index, got[index].ObjectID, snapshot.Head)
		}
		if gotPack, wantPack := encodedRelicDocument(t, got[index].Operation), encodedRelicDocument(t, snapshot.Operation); !bytes.Equal(gotPack, wantPack) {
			t.Fatalf("commit %d pack = %s, want %s", index, gotPack, wantPack)
		}
	}
}

func TestReadRelicOperationsWalksFromStopAt(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1).withField("status", "open"))
		first := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "One"))
		second := writeRelicChild(t, repo, config, first, nil, relicTitleSet(700002, "Two"))
		third := writeRelicChild(t, repo, config, second, nil, relicTitleSet(700003, "Three"))

		results, err := repo.ReadRelicOperations(ctx, config, []RelicHistoryRequest{historyRequest(third, first.Head)})
		if err != nil {
			t.Fatalf("ReadRelicOperations() error = %v", err)
		}
		result := results[0]
		if result.RelicID != root.Operation.RelicID || result.Head != third.Head || !result.BoundaryReached || result.Truncated != nil {
			t.Fatalf("result = %+v, want the boundary reached", result)
		}
		assertOperationCommits(t, result.Commits, second, third)
		if result.Commits[0].Parent != first.Head || result.Commits[1].Parent != second.Head {
			t.Fatalf("parents = %s, %s", result.Commits[0].Parent, result.Commits[1].Parent)
		}

		results, err = repo.ReadRelicOperations(ctx, config, []RelicHistoryRequest{historyRequest(third, "")})
		if err != nil {
			t.Fatalf("ReadRelicOperations() error = %v", err)
		}
		if results[0].BoundaryReached || results[0].Truncated != nil {
			t.Fatalf("result = %+v, want the whole chain", results[0])
		}
		assertOperationCommits(t, results[0].Commits, root, first, second, third)
		if results[0].Commits[0].Parent != "" {
			t.Fatalf("the root has parent %s", results[0].Commits[0].Parent)
		}
	})
}

func TestReadRelicOperationsReportsAReplacedHistory(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1).withField("status", "open"))
	first := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "One"))
	second := writeRelicChild(t, repo, config, first, nil, relicTitleSet(700002, "Two"))
	// A sibling of second: the same tree on first, so second is not an
	// ancestor of it.
	sibling := gitOutput(t, repo, "commit-tree", second.Head+"^{tree}", "-p", first.Head, "-m", "replayed")
	siblingSnapshot := core.RelicSnapshot{Head: sibling, Operation: second.Operation}

	results, err := repo.ReadRelicOperations(ctx, config, []RelicHistoryRequest{{
		Head:   core.RelicHead{RelicID: root.Operation.RelicID, ObjectID: sibling},
		StopAt: second.Head,
	}})
	if err != nil {
		t.Fatalf("ReadRelicOperations() error = %v", err)
	}
	if results[0].BoundaryReached || results[0].Truncated != nil {
		t.Fatalf("result = %+v, want the boundary missed", results[0])
	}
	assertOperationCommits(t, results[0].Commits, root, first, siblingSnapshot)
}

func TestReadRelicOperationsTruncatesAtAnUnreadablePack(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1).withField("status", "open"))
	first := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "One"))
	broken := forgeRelicCommit(t, repo, first.Head, []byte("not json\n"),
		encodedRelicDocument(t, first.Record.State), forgedPartsOf(t, repo, first.Head))

	results, err := repo.ReadRelicOperations(ctx, config, []RelicHistoryRequest{{
		Head: core.RelicHead{RelicID: root.Operation.RelicID, ObjectID: broken},
	}})
	if err != nil {
		t.Fatalf("ReadRelicOperations() error = %v", err)
	}
	result := results[0]
	if result.Truncated == nil || result.Truncated.Commit != broken || result.Truncated.TaskID != root.Operation.RelicID {
		t.Fatalf("Truncated = %+v, want commit %s of relic %s", result.Truncated, broken, root.Operation.RelicID)
	}
	assertRelicCategory(t, result.Truncated.Err, core.CategoryCorruptData, root.Operation.RelicID)
	assertOperationCommits(t, result.Commits, root, first)
}

func TestReadRelicBodiesReadsByObjectID(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := indexFixtureRelic(t, repo, config, 1)
		relicID := tip.Operation.RelicID
		var requests []RelicBodyRequest
		var want [][]byte
		for _, part := range tip.Record.State.Relic.Parts {
			if part.Body == nil {
				continue
			}
			path := tip.Head + ":parts/" + part.ID + "/body"
			requests = append(requests, RelicBodyRequest{
				RelicID: relicID, PartID: part.ID, ObjectID: gitOutput(t, repo, "rev-parse", path), Bytes: part.Body.Bytes,
			})
			want = append(want, relicBlob(t, repo, path))
		}

		asked := observeRelicObjects(repo)
		bodies, err := repo.ReadRelicBodies(ctx, config, requests)
		repo.relicObjectObserver = nil
		if err != nil {
			t.Fatalf("ReadRelicBodies() error = %v", err)
		}
		if len(bodies) != len(want) {
			t.Fatalf("read %d bodies, want %d", len(bodies), len(want))
		}
		for index := range want {
			if !bytes.Equal(bodies[index], want[index]) || bodies[index] == nil {
				t.Fatalf("body %d = %q, want %q", index, bodies[index], want[index])
			}
		}
		if len(*asked) != len(requests) {
			t.Fatalf("asked for %q, want the %d body objects", *asked, len(requests))
		}
		for index, request := range requests {
			if (*asked)[index] != request.ObjectID {
				t.Fatalf("request %d = %q, want the bare object ID %s", index, (*asked)[index], request.ObjectID)
			}
		}

		partID := requests[0].PartID
		for name, request := range map[string]RelicBodyRequest{
			"a wrong size":  {RelicID: relicID, PartID: partID, ObjectID: requests[0].ObjectID, Bytes: requests[0].Bytes + 1},
			"a tree":        {RelicID: relicID, PartID: partID, ObjectID: gitOutput(t, repo, "rev-parse", tip.Head+"^{tree}"), Bytes: 1},
			"an unknown ID": {RelicID: relicID, PartID: partID, ObjectID: strings.Repeat("0", len(tip.Head)-1) + "1", Bytes: 1},
		} {
			_, err := repo.ReadRelicBodies(ctx, config, []RelicBodyRequest{requests[1], request})
			if err == nil {
				t.Fatalf("%s: ReadRelicBodies() succeeded", name)
			}
			assertRelicCategory(t, err, core.CategoryCorruptData, relicID, partID)
		}
	})
}
