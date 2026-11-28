package core

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// historyCommitID returns a fake commit ID for the n-th commit of a test
// chain. The chain functions never read it, so its shape does not matter.
func historyCommitID(n int) string {
	return fmt.Sprintf("%040x", n)
}

// historyChain links packs into a chain, oldest first, under fake commit IDs.
func historyChain(packs ...RelicOperationPack) RelicChain {
	chain := RelicChain{RelicID: relicDocRelic, Commits: make([]RelicChainCommit, len(packs))}
	for index, pack := range packs {
		commit := RelicChainCommit{Commit: historyCommitID(index + 1), Operation: pack}
		if index > 0 {
			commit.Parent = historyCommitID(index)
		}
		chain.Commits[index] = commit
	}
	if len(packs) > 0 {
		chain.Head = historyCommitID(len(packs))
	}
	return chain
}

// historyPack is relicFoldPack under another actor.
func historyPack(clock uint64, actor string, operations ...RelicOperation) RelicOperationPack {
	pack := relicFoldPack(clock, operations...)
	pack.Actor = Actor{ID: actor}
	return pack
}

func historyCreate(id string) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationCreate, Relic: &RelicCreateData{
		Title: "History", Type: "spec", CreatedAt: relicDocTime,
	}}
}

func historyPartAdd(id, position string, body *RelicBodyChange) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationPartAdd, PartType: "section", Position: position, Body: body}
}

func historyBodySet(id, part, base string, bytes int64) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationPartBodySet, Part: part,
		Body: &RelicBodyChange{Format: "md", Bytes: bytes, Base: base}}
}

func historyOperationIDs(operations []RelicChangeOperation) []string {
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.ID
	}
	return ids
}

func historyFiveCommits() RelicChain {
	part := relicTestID(11)
	return historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(10)), historyPartAdd(part, "1/1", &RelicBodyChange{Format: "md", Bytes: 3})),
		historyPack(2, "bo@example.com", RelicOperation{ID: relicTestID(12), Type: RelicOperationTitleSet, Title: "Two"}),
		historyPack(3, "ana@example.com", historyBodySet(relicTestID(13), part, part, 4)),
		historyPack(4, "cy@example.com",
			RelicOperation{ID: relicTestID(14), Type: RelicOperationPropertySet, Name: "status", Value: json.RawMessage(`"draft"`)},
			RelicOperation{ID: relicTestID(15), Type: RelicOperationTitleSet, Title: "Four"}),
		historyPack(5, "bo@example.com", RelicOperation{ID: relicTestID(16), Type: RelicOperationPartMove, Part: part, Position: "2/1"}),
	)
}

func TestBuildRelicHistoryListsChangesOldestFirst(t *testing.T) {
	chain := historyFiveCommits()
	summary := RelicSummary{ID: relicDocRelic, Title: "Four"}

	history := BuildRelicHistory(chain, summary, "", 0, false)
	if history.Total != 5 || history.Showing != 5 || len(history.Changes) != 5 {
		t.Fatalf("total %d, showing %d, changes %d; want 5, 5, 5", history.Total, history.Showing, len(history.Changes))
	}
	if history.Relic.ID != relicDocRelic || history.Part != "" || history.BodyVersions != nil || history.Truncated != nil {
		t.Fatalf("history = %+v", history)
	}
	for index, change := range history.Changes {
		pack := chain.Commits[index].Operation
		if change.Commit != chain.Commits[index].Commit || change.Actor != pack.Actor.ID ||
			!change.WallTime.Equal(pack.WallTime) || change.LogicalClock != pack.LogicalClock {
			t.Errorf("change %d = %+v, want commit %s by %s at %s clock %d",
				index, change, chain.Commits[index].Commit, pack.Actor.ID, pack.WallTime, pack.LogicalClock)
		}
		want := make([]string, len(pack.Operations))
		for at, operation := range pack.Operations {
			want[at] = operation.ID
		}
		if got := historyOperationIDs(change.Operations); !reflect.DeepEqual(got, want) {
			t.Errorf("change %d operations = %v, want %v", index, got, want)
		}
	}

	limited := BuildRelicHistory(chain, summary, "", 2, false)
	if limited.Total != 5 || limited.Showing != 2 || len(limited.Changes) != 2 ||
		limited.Changes[0].Commit != historyCommitID(4) || limited.Changes[1].Commit != historyCommitID(5) {
		t.Fatalf("limit 2 = total %d, showing %d, %+v", limited.Total, limited.Showing, limited.Changes)
	}
	all := BuildRelicHistory(chain, summary, "", 2, true)
	if all.Showing != 5 || len(all.Changes) != 5 {
		t.Fatalf("all = showing %d, changes %d", all.Showing, len(all.Changes))
	}

	// Twelve changes: the default window is the last ten.
	packs := []RelicOperationPack{historyPack(1, "ana@example.com", historyCreate(relicTestID(20)))}
	for clock := uint64(2); clock <= 12; clock++ {
		packs = append(packs, historyPack(clock, "ana@example.com",
			RelicOperation{ID: relicTestID(20 + int(clock)), Type: RelicOperationTitleSet, Title: fmt.Sprintf("T%d", clock)}))
	}
	long := BuildRelicHistory(historyChain(packs...), summary, "", 0, false)
	if long.Total != 12 || long.Showing != DefaultChangeLimit || long.Changes[0].Commit != historyCommitID(3) {
		t.Fatalf("default window = total %d, showing %d, first %s", long.Total, long.Showing, long.Changes[0].Commit)
	}
}

func TestRelicChangeSummaryIsDistinctTypes(t *testing.T) {
	part := relicTestID(31)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(30)), historyPartAdd(part, "1/1", nil)),
		historyPack(2, "ana@example.com",
			RelicOperation{ID: relicTestID(32), Type: RelicOperationPartFieldSet, Part: part, Name: "a", Value: json.RawMessage(`1`)},
			historyBodySet(relicTestID(33), part, "", 2),
			RelicOperation{ID: relicTestID(34), Type: RelicOperationPartFieldSet, Part: part, Name: "b", Value: json.RawMessage(`2`)}),
		historyPack(3, "ana@example.com",
			RelicOperation{ID: relicTestID(35), Type: "part.body.merge", Part: part},
			RelicOperation{ID: relicTestID(36), Type: RelicOperationTitleSet, Title: "New"}),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, "", 0, true)
	for index, want := range []string{
		"relic.create, part.add",
		"part.field.set, part.body.set",
		"part.body.merge, title.set",
	} {
		if got := history.Changes[index].Summary; got != want {
			t.Errorf("change %d summary = %q, want %q", index, got, want)
		}
	}
	if operation := history.Changes[2].Operations[0]; operation.Type != "part.body.merge" || operation.Applied != nil {
		t.Errorf("unknown operation = %+v, want its type and no applied mark", operation)
	}
}

func TestPartHistoryListsOnlyChangesNamingThePart(t *testing.T) {
	partP, partQ := relicTestID(41), relicTestID(42)
	link := relicTestID(46)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(40)),
			historyPartAdd(partP, "1/1", &RelicBodyChange{Format: "md", Bytes: 1}),
			historyPartAdd(partQ, "2/1", nil)),
		historyPack(2, "bo@example.com", RelicOperation{ID: relicTestID(43), Type: RelicOperationTitleSet, Title: "Only title"}),
		historyPack(3, "bo@example.com",
			historyBodySet(relicTestID(44), partQ, "", 5),
			RelicOperation{ID: relicTestID(45), Type: RelicOperationPartMove, Part: partP, Position: "3/1"}),
		historyPack(4, "cy@example.com",
			RelicOperation{ID: link, Type: RelicOperationLinkAdd, Link: &RelicLinkChange{From: partP, To: RelicLinkTarget{Task: "WB-1"}, Rel: "implements"}},
			RelicOperation{ID: relicTestID(47), Type: RelicOperationLinkAdd, Link: &RelicLinkChange{To: RelicLinkTarget{Task: "WB-2"}, Rel: "implements"}}),
		historyPack(5, "cy@example.com", RelicOperation{ID: relicTestID(48), Type: RelicOperationPartRemove, Part: partQ}),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, partP, 0, true)
	if history.Part != partP || history.Total != 3 || history.Showing != 3 {
		t.Fatalf("part history = part %q, total %d, showing %d; want %s, 3, 3", history.Part, history.Total, history.Showing, partP)
	}
	want := []struct {
		commit string
		ids    []string
		sum    string
	}{
		{historyCommitID(1), []string{partP}, "part.add"},
		{historyCommitID(3), []string{relicTestID(45)}, "part.move"},
		{historyCommitID(4), []string{link}, "link.add"},
	}
	for index, change := range history.Changes {
		if change.Commit != want[index].commit || !reflect.DeepEqual(historyOperationIDs(change.Operations), want[index].ids) ||
			change.Summary != want[index].sum {
			t.Errorf("change %d = commit %s, operations %v, summary %q; want %s %v %q", index, change.Commit,
				historyOperationIDs(change.Operations), change.Summary, want[index].commit, want[index].ids, want[index].sum)
		}
	}
	if len(history.BodyVersions) != 1 || history.BodyVersions[0].Version != partP || !history.BodyVersions[0].Current {
		t.Errorf("body versions = %+v, want P's first body as current", history.BodyVersions)
	}
	limited := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, partP, 1, false)
	if limited.Total != 3 || limited.Showing != 1 || limited.Changes[0].Commit != historyCommitID(4) {
		t.Errorf("limited part history = total %d, showing %d", limited.Total, limited.Showing)
	}
	if len(limited.BodyVersions) != 1 {
		t.Errorf("body versions are windowed: %+v", limited.BodyVersions)
	}
}

// historyGenerator builds random, valid relic chains for comparing the applied
// marks with the fold.
type historyGenerator struct {
	random *rand.Rand
	next   int
}

func (g *historyGenerator) id() string {
	g.next++
	return relicTestID(1000 + g.next)
}

func (g *historyGenerator) body() *RelicBodyChange {
	return &RelicBodyChange{Format: "md", Bytes: int64(g.random.Intn(100))}
}

// chain returns the packs of one random chain, each valid for the fold.
func (g *historyGenerator) chain(t *testing.T) []RelicOperationPack {
	t.Helper()
	operations := []RelicOperation{historyCreate(g.id())}
	for count := g.random.Intn(3); count >= 0; count-- {
		var body *RelicBodyChange
		if g.random.Intn(2) == 0 {
			body = g.body()
		}
		operations = append(operations, historyPartAdd(g.id(), fmt.Sprintf("%d/1", g.next), body))
	}
	packs := []RelicOperationPack{relicFoldPack(1, operations...)}
	record, err := ApplyRelic(nil, packs[0])
	if err != nil {
		t.Fatalf("ApplyRelic(root): %v", err)
	}
	// Every version a part has had, so a stale base can be one that was real.
	seen := map[string][]string{}
	for _, part := range record.State.Relic.Parts {
		if part.Body != nil {
			seen[part.ID] = append(seen[part.ID], part.Body.Version)
		}
	}

	for clock := uint64(2); clock <= uint64(2+g.random.Intn(6)); clock++ {
		parts := record.State.Relic.Parts
		var pack []RelicOperation
		// pending is the version each part has within the pack being built.
		pending := map[string]string{}
		current := func(part RelicPart) string {
			if version, ok := pending[part.ID]; ok {
				return version
			}
			if part.Body != nil {
				return part.Body.Version
			}
			return ""
		}
		for count := 1 + g.random.Intn(4); count > 0; count-- {
			choice := g.random.Intn(8)
			if len(parts) == 0 {
				choice = 0
			}
			switch choice {
			case 0: // add a part, with or without a body
				var body *RelicBodyChange
				if g.random.Intn(2) == 0 {
					body = g.body()
				}
				pack = append(pack, historyPartAdd(g.id(), fmt.Sprintf("%d/1", g.next+1), body))
			case 1, 2: // save from the current version
				part := parts[g.random.Intn(len(parts))]
				id := g.id()
				pack = append(pack, historyBodySet(id, part.ID, current(part), 7))
				pending[part.ID] = id
			case 3: // save from a stale base
				part := parts[g.random.Intn(len(parts))]
				base := g.id()
				if versions := seen[part.ID]; len(versions) > 0 && g.random.Intn(2) == 0 {
					base = versions[g.random.Intn(len(versions))]
				}
				if base == current(part) {
					base = g.id()
				}
				pack = append(pack, historyBodySet(g.id(), part.ID, base, 7))
			case 4: // two saves to one part in one pack, the second current or stale
				part := parts[g.random.Intn(len(parts))]
				first := g.id()
				pack = append(pack, historyBodySet(first, part.ID, current(part), 7))
				pending[part.ID] = first
				if g.random.Intn(2) == 0 {
					second := g.id()
					pack = append(pack, historyBodySet(second, part.ID, first, 9))
					pending[part.ID] = second
				} else {
					pack = append(pack, historyBodySet(g.id(), part.ID, g.id(), 9))
				}
			case 5: // save to a part the relic does not have
				pack = append(pack, historyBodySet(g.id(), g.id(), "", 3))
			case 6: // remove a part; later saves to it still count
				part := parts[g.random.Intn(len(parts))]
				pack = append(pack, RelicOperation{ID: g.id(), Type: RelicOperationPartRemove, Part: part.ID})
			case 7: // a stale save merged onto the current version, or onto a stale one
				part := parts[g.random.Intn(len(parts))]
				ancestor := g.id()
				if versions := seen[part.ID]; len(versions) > 0 && g.random.Intn(2) == 0 {
					ancestor = versions[g.random.Intn(len(versions))]
				}
				if ancestor == current(part) {
					ancestor = g.id()
				}
				save := g.id()
				pack = append(pack, historyBodySet(save, part.ID, ancestor, 7))
				base := current(part)
				if base == "" || g.random.Intn(3) == 0 {
					base = g.id()
				}
				merge := g.id()
				pack = append(pack, relicMergeOperation(merge, part.ID, base, ancestor, save, 11))
				if base == current(part) {
					pending[part.ID] = merge
				}
			}
		}
		next := relicFoldPack(clock, pack...)
		folded, err := ApplyRelic(&record, next)
		if err != nil {
			t.Fatalf("ApplyRelic(clock %d): %v", clock, err)
		}
		for _, part := range folded.State.Relic.Parts {
			if part.Body != nil {
				seen[part.ID] = append(seen[part.ID], part.Body.Version)
			}
		}
		packs = append(packs, next)
		record = folded
	}
	return packs
}

// foldApplied asks the fold itself whether each body save took: the pack cut
// after the save is folded onto the parent, and the save took when its ID is
// then the part's body version.
func foldApplied(t *testing.T, packs []RelicOperationPack) map[string]bool {
	t.Helper()
	applied := map[string]bool{}
	var parent *RelicRecord
	for _, pack := range packs {
		for index, operation := range pack.Operations {
			partID := ""
			switch {
			case operation.Type == RelicOperationPartAdd && operation.Body != nil:
				partID = operation.ID
			case operation.Type == RelicOperationPartBodySet, operation.Type == RelicOperationPartBodyMerge:
				partID = operation.Part
			default:
				continue
			}
			cut := pack
			cut.Operations = pack.Operations[:index+1]
			// A cut before a merge holds none, so it declares less.
			cut.MinReader = relicPackMinReader(cut.Operations)
			record, err := ApplyRelic(parent, cut)
			if err != nil {
				t.Fatalf("ApplyRelic(clock %d cut at %d): %v", pack.LogicalClock, index, err)
			}
			part, ok := record.State.Relic.Part(partID)
			applied[operation.ID] = ok && part.Body != nil && part.Body.Version == operation.ID
		}
		record, err := ApplyRelic(parent, pack)
		if err != nil {
			t.Fatalf("ApplyRelic(clock %d): %v", pack.LogicalClock, err)
		}
		parent = &record
	}
	return applied
}

func TestAppliedMatchesTheFold(t *testing.T) {
	generator := &historyGenerator{random: rand.New(rand.NewSource(20261127))}
	stale, merges, mergesApplied := 0, 0, 0
	for run := 0; run < 200; run++ {
		packs := generator.chain(t)
		want := foldApplied(t, packs)
		got := RelicBodySavesApplied(historyChain(packs...))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: RelicBodySavesApplied = %v, the fold says %v", run, got, want)
		}
		for _, applied := range want {
			if !applied {
				stale++
			}
		}
		for _, pack := range packs {
			for _, operation := range pack.Operations {
				if operation.Type == RelicOperationPartBodyMerge {
					merges++
					if want[operation.ID] {
						mergesApplied++
					}
				}
			}
		}
		history := BuildRelicHistory(historyChain(packs...), RelicSummary{ID: relicDocRelic}, "", 0, true)
		for _, change := range history.Changes {
			for _, operation := range change.Operations {
				expected, saves := want[operation.ID]
				if saves != (operation.Applied != nil) || (saves && *operation.Applied != expected) {
					t.Fatalf("run %d: operation %s applied = %v, want %v (saves %v)", run, operation.ID, operation.Applied, expected, saves)
				}
			}
		}
	}
	if stale == 0 {
		t.Fatal("the generated chains held no stale save")
	}
	if mergesApplied == 0 || mergesApplied == merges {
		t.Fatalf("the generated chains held %d merges, %d applied; want some applied and some not", merges, mergesApplied)
	}
}

func TestBodySavesAppliedCountsMerges(t *testing.T) {
	part := relicTestID(9200)
	current, save, merge := relicTestID(9201), relicTestID(9202), relicTestID(9203)
	staleSave, staleMerge := relicTestID(9204), relicTestID(9205)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(9206)), historyPartAdd(part, "1/1", &RelicBodyChange{Format: "md", Bytes: 10})),
		historyPack(2, "ana@example.com", historyBodySet(current, part, part, 20)),
		historyPack(3, "bo@example.com", historyBodySet(save, part, part, 30),
			relicMergeOperation(merge, part, current, part, save, 40)),
		// A merge from a version that no longer stands is not taken.
		historyPack(4, "cy@example.com", historyBodySet(staleSave, part, part, 50),
			relicMergeOperation(staleMerge, part, current, part, staleSave, 60)),
	)
	applied := RelicBodySavesApplied(chain)
	want := map[string]bool{part: true, current: true, save: false, merge: true, staleSave: false, staleMerge: false}
	if !reflect.DeepEqual(applied, want) {
		t.Fatalf("RelicBodySavesApplied = %v, want %v", applied, want)
	}
	if fold := foldApplied(t, []RelicOperationPack{chain.Commits[0].Operation, chain.Commits[1].Operation,
		chain.Commits[2].Operation, chain.Commits[3].Operation}); !reflect.DeepEqual(fold, want) {
		t.Fatalf("the fold says %v, want %v", fold, want)
	}
	if id, saves := savesRelicBody(chain.Commits[2].Operation.Operations[1]); !saves || id != part {
		t.Fatalf("savesRelicBody(merge) = %q, %v; want %s and true", id, saves, part)
	}
}

func TestBodyVersionsAreTheAppliedSaves(t *testing.T) {
	part := relicTestID(51)
	second, third, fourth := relicTestID(52), relicTestID(53), relicTestID(54)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(50)), historyPartAdd(part, "1/1", &RelicBodyChange{Format: "md", Bytes: 10})),
		historyPack(2, "bo@example.com", historyBodySet(second, part, part, 20)),
		historyPack(3, "cy@example.com", historyBodySet(third, part, part, 30)),
		historyPack(4, "ana@example.com", historyBodySet(fourth, part, second, 40)),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, part, 0, false)
	want := []RelicHistoryBodyVersion{
		{Version: part, Format: "md", Bytes: 10, Actor: "ana@example.com", WallTime: chain.Commits[0].Operation.WallTime, Commit: historyCommitID(1)},
		{Version: second, Base: part, Format: "md", Bytes: 20, Actor: "bo@example.com", WallTime: chain.Commits[1].Operation.WallTime, Commit: historyCommitID(2)},
		{Version: fourth, Base: second, Format: "md", Bytes: 40, Actor: "ana@example.com", WallTime: chain.Commits[3].Operation.WallTime, Commit: historyCommitID(4), Current: true},
	}
	if !reflect.DeepEqual(history.BodyVersions, want) {
		t.Fatalf("body versions:\n got %+v\nwant %+v", history.BodyVersions, want)
	}
	staleChange := history.Changes[2]
	if staleChange.Actor != "cy@example.com" || len(staleChange.Operations) != 1 ||
		staleChange.Operations[0].ID != third || staleChange.Operations[0].Applied == nil || *staleChange.Operations[0].Applied {
		t.Fatalf("stale change = %+v, want %s by cy not applied", staleChange, third)
	}
	encoded, err := json.Marshal(staleChange)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"applied":false`) {
		t.Errorf("stale change JSON %s lacks \"applied\":false", encoded)
	}
}

func TestHistoryStopsAtANewerWritersPack(t *testing.T) {
	newer := historyPack(3, "ana@example.com", RelicOperation{ID: relicTestID(63), Type: RelicOperationTitleSet, Title: "Later"})
	newer.MinReader = 99
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(60))),
		historyPack(2, "ana@example.com", RelicOperation{ID: relicTestID(62), Type: RelicOperationTitleSet, Title: "Two"}),
		newer,
		historyPack(4, "ana@example.com", RelicOperation{ID: relicTestID(64), Type: RelicOperationTitleSet, Title: "Four"}),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, "", 0, true)
	if history.Total != 2 || len(history.Changes) != 2 {
		t.Fatalf("total %d, changes %d; want 2 and 2", history.Total, len(history.Changes))
	}
	if history.Truncated == nil || history.Truncated.Commit != historyCommitID(3) ||
		history.Truncated.Category != CategoryNewerWriter || history.Truncated.Message == "" {
		t.Fatalf("truncated = %+v, want newer-writer at %s", history.Truncated, historyCommitID(3))
	}

	// A read that stopped early keeps the reader's boundary.
	short := historyChain(chain.Commits[0].Operation, chain.Commits[1].Operation)
	short.Truncated = &HistoryTruncation{Commit: historyCommitID(3), Message: "cannot read"}
	if got := BuildRelicHistory(short, RelicSummary{}, "", 0, true); got.Total != 2 || got.Truncated != short.Truncated {
		t.Fatalf("reader truncation = %+v", got.Truncated)
	}
}

func TestResolveRelicVersion(t *testing.T) {
	const (
		create = "01KA0000000000000000000000"
		addP   = "01KA0000000000000000000001"
		save   = "01KB7Q00000000000000000000"
		title  = "01KC0000000000000000000000"
		move   = "01KC0000000000000000000009"
	)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(create), historyPartAdd(addP, "1/1", &RelicBodyChange{Bytes: 1})),
		historyPack(2, "ana@example.com", historyBodySet(save, addP, addP, 2)),
		historyPack(3, "ana@example.com",
			RelicOperation{ID: title, Type: RelicOperationTitleSet, Title: "T"},
			RelicOperation{ID: move, Type: RelicOperationPartMove, Part: addP, Position: "2/1"}),
	)
	for _, test := range []struct {
		prefix  string
		index   int
		version string
	}{
		{save, 1, save},
		{strings.ToLower(save), 1, save},
		{"01kb", 1, save},
		{create, 0, create},
		{addP, 0, addP},
		{title, 2, title},
		{move, 2, move},
		{"01KC", 2, move},
	} {
		point, err := ResolveRelicVersion(chain, relicDocRelic, test.prefix)
		if err != nil {
			t.Errorf("ResolveRelicVersion(%q): %v", test.prefix, err)
			continue
		}
		if point.Index != test.index || point.Commit.Commit != historyCommitID(test.index+1) || point.Version != test.version {
			t.Errorf("ResolveRelicVersion(%q) = index %d, commit %s, version %s; want %d, %s, %s",
				test.prefix, point.Index, point.Commit.Commit, point.Version, test.index, historyCommitID(test.index+1), test.version)
		}
	}

	for _, missing := range []string{"01KZ0000000000000000000000", strings.Repeat("ab12", 10)} {
		_, err := ResolveRelicVersion(chain, relicDocRelic, missing)
		want := fmt.Sprintf("relic %s has no change %q in its history; a version is a change ID from \"workbook relic show %s --history\", not a Git commit",
			relicDocRelic, missing, relicDocRelic)
		if CategoryOf(err) != CategoryNotFound || err.Error() != want {
			t.Errorf("ResolveRelicVersion(%q) = %v (%s), want not found %q", missing, err, CategoryOf(err), want)
		}
	}
	_, err := ResolveRelicVersion(chain, relicDocRelic, "01k")
	if want := fmt.Sprintf("change prefix %q matches 3 changes of %s; use more characters", "01k", relicDocRelic); CategoryOf(err) != CategoryValidation || err == nil || err.Error() != want {
		t.Errorf("ambiguous = %v (%s), want %q", err, CategoryOf(err), want)
	}
	if _, err := ResolveRelicVersion(chain, relicDocRelic, " "); CategoryOf(err) != CategoryValidation {
		t.Errorf("empty = %v, want validation", err)
	}
}

func TestLatestRelicPoints(t *testing.T) {
	partP, partQ := relicTestID(71), relicTestID(72)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(70)), historyPartAdd(partP, "1/1", nil), historyPartAdd(partQ, "2/1", nil)),
		historyPack(2, "ana@example.com", historyBodySet(relicTestID(73), partP, "", 1)),
		historyPack(3, "ana@example.com",
			RelicOperation{ID: relicTestID(74), Type: RelicOperationTitleSet, Title: "T"},
			historyBodySet(relicTestID(75), partQ, "", 1),
			RelicOperation{ID: relicTestID(76), Type: RelicOperationTitleSet, Title: "U"}),
		historyPack(4, "ana@example.com", RelicOperation{ID: relicTestID(77), Type: RelicOperationTitleSet, Title: "V"}),
	)
	from, to, err := LatestRelicPoints(chain, "")
	if err != nil || to.Index != 3 || to.Version != relicTestID(77) || to.Commit.Commit != historyCommitID(4) ||
		from.Index != 2 || from.Version != relicTestID(76) || from.Commit.Commit != historyCommitID(3) {
		t.Fatalf("whole relic = %+v → %+v, %v", from, to, err)
	}
	from, to, err = LatestRelicPoints(chain, partQ)
	if err != nil || to.Index != 2 || to.Version != relicTestID(76) || from.Index != 1 || from.Version != relicTestID(73) {
		t.Fatalf("part Q = %+v → %+v, %v", from, to, err)
	}
	from, to, err = LatestRelicPoints(chain, partP)
	if err != nil || to.Index != 1 || from.Index != 0 || from.Version != partQ {
		t.Fatalf("part P = %+v → %+v, %v", from, to, err)
	}
	if _, _, err := LatestRelicPoints(chain, relicTestID(99)); CategoryOf(err) != CategoryNotFound ||
		err.Error() != fmt.Sprintf("relic %s has no change to part %s", relicDocRelic, relicTestID(99)) {
		t.Fatalf("unnamed part = %v", err)
	}

	single := historyChain(chain.Commits[0].Operation)
	from, to, err = LatestRelicPoints(single, "")
	if err != nil || to.Index != 0 || from.Index != -1 || from.Version != "" || !reflect.DeepEqual(from.Commit, RelicChainCommit{}) {
		t.Fatalf("one commit = %+v → %+v, %v", from, to, err)
	}
	from, _, err = LatestRelicPoints(single, partP)
	if err != nil || from.Index != -1 {
		t.Fatalf("one commit, part = %+v, %v", from, err)
	}
}

func TestRelicHistoryJSONMembers(t *testing.T) {
	part := relicTestID(81)
	chain := historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(80)), historyPartAdd(part, "1/1", &RelicBodyChange{Format: "md", Bytes: 3})),
		historyPack(2, "bo@example.com", historyBodySet(relicTestID(82), part, relicTestID(99), 4)),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, part, 0, false)
	history.Truncated = &HistoryTruncation{Commit: "c", Message: "m"}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "history", top, "relic", "part", "total", "showing", "changes", "bodyVersions", "truncated")

	var changes []map[string]json.RawMessage
	if err := json.Unmarshal(top["changes"], &changes); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "change", changes[0], "commit", "actor", "wallTime", "logicalClock", "summary", "operations")
	var operations []map[string]json.RawMessage
	if err := json.Unmarshal(changes[0]["operations"], &operations); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "part.add", operations[0], "id", "type", "partType", "position", "body", "applied")
	if string(operations[0]["applied"]) != "true" {
		t.Errorf("part.add applied = %s, want true", operations[0]["applied"])
	}
	if err := json.Unmarshal(changes[1]["operations"], &operations); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "part.body.set", operations[0], "id", "type", "part", "body", "applied")
	if string(operations[0]["applied"]) != "false" {
		t.Errorf("stale part.body.set applied = %s, want false", operations[0]["applied"])
	}

	var versions []map[string]json.RawMessage
	if err := json.Unmarshal(top["bodyVersions"], &versions); err != nil {
		t.Fatal(err)
	}
	requireMembers(t, "body version", versions[0], "version", "format", "bytes", "actor", "wallTime", "commit", "current")

	// A title change carries no applied mark, and nothing is null.
	titled := BuildRelicHistory(historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(83))),
	), RelicSummary{ID: relicDocRelic}, "", 0, false)
	encoded, _ = json.Marshal(titled)
	if strings.Contains(string(encoded), "applied") || strings.Contains(string(encoded), "bodyVersions") ||
		strings.Contains(string(encoded), `"part"`) {
		t.Errorf("whole-relic history JSON %s carries part members", encoded)
	}

	empty := BuildRelicHistory(RelicChain{RelicID: relicDocRelic}, RelicSummary{ID: relicDocRelic}, part, 0, false)
	encoded, _ = json.Marshal(empty)
	if !strings.Contains(string(encoded), `"changes":[]`) {
		t.Errorf("empty history JSON %s lacks \"changes\":[]", encoded)
	}
	bare := historyChain(historyPack(1, "ana@example.com"))
	encoded, _ = json.Marshal(BuildRelicHistory(bare, RelicSummary{ID: relicDocRelic}, "", 0, false).Changes[0])
	if !strings.Contains(string(encoded), `"operations":[]`) {
		t.Errorf("empty change JSON %s lacks \"operations\":[]", encoded)
	}
}

func requireMembers(t *testing.T, what string, members map[string]json.RawMessage, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := members[name]; !ok {
			t.Errorf("%s JSON lacks %q: %v", what, name, keysOf(members))
		}
	}
}

func keysOf(members map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	return keys
}

// historyMergeChain is a chain whose third commit is a replayed stale save with
// the merge it was given: Bo's save from the first body, not taken, then its
// merge onto Ana's save.
func historyMergeChain() (chain RelicChain, part, theirs, save, merge string) {
	part, theirs, save, merge = relicTestID(9300), relicTestID(9301), relicTestID(9302), relicTestID(9303)
	chain = historyChain(
		historyPack(1, "ana@example.com", historyCreate(relicTestID(9304)), historyPartAdd(part, "1/1", &RelicBodyChange{Format: "md", Bytes: 10})),
		historyPack(2, "ana@example.com", historyBodySet(theirs, part, part, 20)),
		historyPack(3, "bo@example.com", historyBodySet(save, part, part, 30),
			relicMergeOperation(merge, part, theirs, part, save, 40)),
	)
	return chain, part, theirs, save, merge
}

func TestHistoryListsAMergeWithItsInputs(t *testing.T) {
	chain, part, theirs, save, merge := historyMergeChain()
	want := &RelicBodyMerge{Ancestor: part, Other: save, Method: RelicLineMergeMethod}
	for _, partID := range []string{part, ""} {
		history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, partID, 0, true)
		change := history.Changes[len(history.Changes)-1]
		if change.Summary != "part.body.set, part.body.merge" || len(change.Operations) != 2 {
			t.Fatalf("part %q: last change = %+v, want the save and the merge", partID, change)
		}
		saved, merged := change.Operations[0], change.Operations[1]
		if saved.Applied == nil || *saved.Applied || saved.Merge != nil {
			t.Errorf("part %q: save = %+v, want it not applied", partID, saved)
		}
		if merged.Applied == nil || !*merged.Applied || !reflect.DeepEqual(merged.Merge, want) || merged.Body.Base != theirs {
			t.Errorf("part %q: merge = %+v, want it applied onto %s with %+v", partID, merged, theirs, want)
		}
	}

	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, part, 0, false)
	if len(history.BodyVersions) != 3 {
		t.Fatalf("body versions = %+v, want the first, Ana's and the merge", history.BodyVersions)
	}
	last := history.BodyVersions[2]
	if last.Version != merge || last.Base != theirs || !last.Current || !reflect.DeepEqual(last.Merge, want) {
		t.Fatalf("last body version = %+v, want the merge with %+v", last, want)
	}
	for _, version := range history.BodyVersions[:2] {
		if version.Merge != nil {
			t.Errorf("body version %+v is marked as a merge", version)
		}
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	member := `"merge":{"ancestor":"` + part + `","other":"` + save + `","method":"` + RelicLineMergeMethod + `"}`
	if count := strings.Count(string(encoded), member); count != 2 {
		t.Fatalf("history JSON holds %s %d times, want on the operation and the body version: %s", member, count, encoded)
	}
}

// A merged commit replayed again holds two merges of one part, the second
// merging in the first.
func TestHistoryListsBothMergesOfAReplayedMerge(t *testing.T) {
	chain, part, theirs, save, first := historyMergeChain()
	newer, second := relicTestID(9310), relicTestID(9311)
	chain = historyChain(
		chain.Commits[0].Operation, chain.Commits[1].Operation,
		historyPack(3, "cy@example.com", historyBodySet(newer, part, theirs, 25)),
		historyPack(4, "bo@example.com", historyBodySet(save, part, part, 30),
			relicMergeOperation(first, part, theirs, part, save, 40),
			relicMergeOperation(second, part, newer, theirs, first, 45)),
	)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, part, 0, true)
	operations := history.Changes[len(history.Changes)-1].Operations
	if len(operations) != 3 || operations[1].ID != first || operations[2].ID != second ||
		*operations[1].Applied || !*operations[2].Applied || operations[2].Merge.Other != first {
		t.Fatalf("operations = %+v, want both merges, the second applied", operations)
	}
	last := history.BodyVersions[len(history.BodyVersions)-1]
	if last.Version != second || last.Merge == nil || last.Merge.Other != first {
		t.Fatalf("last body version = %+v, want the second merge", last)
	}
	for _, version := range history.BodyVersions {
		if version.Version == first {
			t.Fatalf("body versions = %+v, want no version for the merge that was not applied", history.BodyVersions)
		}
	}
}

func TestHistoryOfAMergeFromANewerBuildTruncates(t *testing.T) {
	chain, part, _, _, merge := historyMergeChain()
	newer := historyPack(4, "cy@example.com", historyBodySet(relicTestID(9320), part, merge, 50))
	newer.MinReader = 10
	chain = historyChain(chain.Commits[0].Operation, chain.Commits[1].Operation, chain.Commits[2].Operation, newer)
	history := BuildRelicHistory(chain, RelicSummary{ID: relicDocRelic}, part, 0, true)
	if history.Total != 3 || history.Truncated == nil || history.Truncated.Category != CategoryNewerWriter ||
		history.Truncated.Commit != historyCommitID(4) {
		t.Fatalf("history = total %d, truncated %+v; want three changes and the newer writer's", history.Total, history.Truncated)
	}
	last := history.BodyVersions[len(history.BodyVersions)-1]
	if last.Version != merge || !last.Current || last.Merge == nil {
		t.Fatalf("last body version = %+v, want the merge", last)
	}
}
