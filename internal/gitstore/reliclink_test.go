package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func relicLinkAddOp(operationNumber int, from, task, rel string) core.RelicOperation {
	return core.RelicOperation{
		ID:   relicTestID(operationNumber),
		Type: core.RelicOperationLinkAdd,
		Link: &core.RelicLinkChange{From: from, To: core.RelicLinkTarget{Task: task}, Rel: rel},
	}
}

// relicRelicLinkAddOp links to relic, or to its part when part is not
// empty, with a seen version and the pointer given (none when empty).
func relicRelicLinkAddOp(operationNumber int, from, relic, part, rel, pointer string) core.RelicOperation {
	change := &core.RelicLinkChange{From: from, To: core.RelicLinkTarget{Relic: relic, Part: part}, Rel: rel,
		Seen: relicTestID(800000)}
	if pointer != "" {
		change.Pointer = json.RawMessage(pointer)
	}
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationLinkAdd, Link: change}
}

func relicLinkRelicID(n int) string { return "WB-R-" + relicTestID(750000+n) }

func relicLinkRemoveOp(operationNumber, linkNumber int) core.RelicOperation {
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationLinkRemove, LinkID: relicTestID(linkNumber)}
}

func relicLinkTaskID(n int) string { return "WB-" + relicTestID(700000+n) }

func relicLinkIDsAt(t *testing.T, repo *Repository, commit string) []string {
	t.Helper()
	links, err := core.DecodeRelicLinksDocument(relicBlob(t, repo, commit+":links.json"))
	if err != nil {
		t.Fatalf("DecodeRelicLinksDocument(%s) error = %v", commit, err)
	}
	ids := make([]string, 0, len(links.Links))
	for _, link := range links.Links {
		ids = append(ids, link.ID)
	}
	return ids
}

func TestRelicLinksRoundTripThroughGit(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p, q := relicTestPartNumbered(1), relicTestPartNumbered(2)
		root := writeRelicRoot(t, repo, config, p, q)
		relicID := root.Record.State.RelicID
		linked := writeRelicChild(t, repo, config, root, nil,
			relicLinkAddOp(13, "", relicLinkTaskID(1), "relates-to"),
			relicLinkAddOp(11, p.id, relicLinkTaskID(2), "implemented-by"),
			relicLinkAddOp(12, q.id, relicLinkTaskID(1), "implemented-by"),
			relicRelicLinkAddOp(15, q.id, relicLinkRelicID(1), relicTestID(751000), "decided-in", `{"at":"00:14:32","quote":"q"}`))

		read, err := repo.GetRelic(context.Background(), config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() error = %v", err)
		}
		want := []core.RelicLink{
			{ID: relicTestID(11), From: p.id, To: core.RelicLinkTarget{Task: relicLinkTaskID(2)}, Rel: "implemented-by"},
			{ID: relicTestID(12), From: q.id, To: core.RelicLinkTarget{Task: relicLinkTaskID(1)}, Rel: "implemented-by"},
			{ID: relicTestID(13), To: core.RelicLinkTarget{Task: relicLinkTaskID(1)}, Rel: "relates-to"},
			{ID: relicTestID(15), From: q.id, To: core.RelicLinkTarget{Relic: relicLinkRelicID(1), Part: relicTestID(751000)},
				Rel: "decided-in", Seen: relicTestID(800000), Pointer: json.RawMessage(`{"at":"00:14:32","quote":"q"}`)},
		}
		if !reflect.DeepEqual(read.Record.Links.Links, want) {
			t.Fatalf("GetRelic() links = %#v, want %#v", read.Record.Links.Links, want)
		}
		if read.Head != linked.Head {
			t.Fatalf("GetRelic() head = %s, want %s", read.Head, linked.Head)
		}
		if stored := relicBlob(t, repo, linked.Head+":links.json"); !bytes.Equal(stored, encodedRelicDocument(t, read.Record.Links)) {
			t.Fatalf("links.json = %s, want the encoded document", stored)
		}
		if tip := readRelicTip(t, repo, config, relicID); !reflect.DeepEqual(tip.Record.Links, read.Record.Links) {
			t.Fatalf("ReadRelicTips() links = %#v, want %#v", tip.Record.Links, read.Record.Links)
		}
		// The parts' trees are untouched by link operations.
		if before, after := gitOutput(t, repo, "rev-parse", root.Head+":parts"), gitOutput(t, repo, "rev-parse", linked.Head+":parts"); before != after {
			t.Fatalf("parts tree changed from %s to %s", before, after)
		}
		assertRelicHistoryValid(t, repo, config, relicID, linked.Head, 2)

		unlinked := writeRelicChild(t, repo, config, linked, nil, relicLinkRemoveOp(14, 11))
		if got := relicLinkIDsAt(t, repo, unlinked.Head); !reflect.DeepEqual(got, []string{relicTestID(12), relicTestID(13), relicTestID(15)}) {
			t.Fatalf("links after the remove = %v", got)
		}
		assertRelicHistoryValid(t, repo, config, relicID, unlinked.Head, 3)
	})
}

func TestReadRefusesLinksNamingAMissingPart(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		pack, _ := relicRootPack(config, 1, relicTestPartNumbered(1))
		record := foldRelic(t, nil, pack)
		relicID := pack.RelicID
		links := core.EmptyRelicLinks()
		links.Links = []core.RelicLink{{ID: relicTestID(20), From: relicTestID(99),
			To: core.RelicLinkTarget{Task: relicLinkTaskID(1)}, Rel: "relates-to"}}
		partTree := forgeRelicTree(t, repo, []string{
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, core.NewRelicFields(nil))) + "\tfields.json"})
		tree := forgeRelicTree(t, repo, []string{
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, pack)) + "\toperation.json",
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, record.State)) + "\tstate.json",
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, links)) + "\tlinks.json",
			"040000 tree " + forgeRelicTree(t, repo, []string{"040000 tree " + partTree + "\t" + relicTestID(1)}) + "\tparts",
		})
		commit := forgeRelicCommitOf(t, repo, tree)
		pointRelicRef(t, repo, relicID, commit)

		requireNamedCorrupt := func(label string, err error) {
			t.Helper()
			if core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(err.Error(), relicID) ||
				!strings.Contains(err.Error(), relicTestID(99)) {
				t.Fatalf("%s error = %v, want corrupt data naming the relic and the part", label, err)
			}
		}
		_, err := repo.GetRelic(context.Background(), config, relicID)
		requireNamedCorrupt("GetRelic()", err)
		_, err = repo.ReadRelicTips(context.Background(), config, []core.RelicHead{{RelicID: relicID, ObjectID: commit}})
		requireNamedCorrupt("ReadRelicTips()", err)
		want := "relic " + relicID + ": relic link " + relicTestID(20) + " is from part " + relicTestID(99) +
			", which the relic does not have"
		if err.Error() != want {
			t.Fatalf("ReadRelicTips() error = %q, want %q", err, want)
		}

		// The check that judges a tip's objects names the relic itself, as
		// the checks beside it do, rather than leaving that to its caller.
		var input bytes.Buffer
		requestRelicTipObjects(&input, commit)
		batch, err := repo.startRelicObjectBatch(context.Background(), input.Bytes())
		if err != nil {
			t.Fatalf("startRelicObjectBatch() error = %v", err)
		}
		defer batch.Close()
		objects, err := readRelicTipObjects(batch)
		if err != nil {
			t.Fatalf("readRelicTipObjects() error = %v", err)
		}
		decoded, err := decodeObjectID(commit)
		if err != nil {
			t.Fatalf("decodeObjectID(%s) error = %v", commit, err)
		}
		_, err = validateRelicTipObjects(objects, config, core.RelicHead{RelicID: relicID, ObjectID: commit}, len(decoded))
		if core.CategoryOf(err) != core.CategoryCorruptData || err.Error() != want {
			t.Fatalf("validateRelicTipObjects() error = %v, want corrupt data %q", err, want)
		}
	})
}

// linkedSharedRelic writes a relic with part p whose second commit links L1
// (number 11, the whole relic) and L2 (number 12, from p).
func linkedSharedRelic(t *testing.T, repo *Repository, config core.ProjectConfig) (core.RelicSnapshot, relicTestPart) {
	t.Helper()
	p := relicTestPartNumbered(1)
	root := writeRelicRoot(t, repo, config, p)
	shared := relicHistoryFrom(t, repo, config, root, relicOps(
		relicLinkAddOp(11, "", relicLinkTaskID(1), "relates-to"),
		relicLinkAddOp(12, p.id, relicLinkTaskID(2), "implemented-by")))
	return shared, p
}

func TestReplayKeepsConcurrentLinkAddsAndRemoves(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		shared, p := linkedSharedRelic(t, repo, config)
		relicID := shared.Record.State.RelicID
		local := relicHistoryFrom(t, repo, config, shared, relicOps(
			relicLinkAddOp(13, p.id, relicLinkTaskID(1), "implemented-by"),
			relicLinkRemoveOp(101, 11)))
		remote := relicHistoryFrom(t, repo, config, shared, relicOps(
			relicLinkAddOp(14, "", relicLinkTaskID(1), "implemented-by"),
			relicLinkRemoveOp(201, 11),
			relicLinkRemoveOp(202, 12)))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if outcome.Conflict != nil || outcome.Replayed != 1 || len(outcome.Warnings) != 0 {
			t.Fatalf("outcome = %+v, want one pack replayed with no conflict or warning", outcome)
		}
		if got, want := relicLinkIDsAt(t, repo, outcome.Next), []string{relicTestID(13), relicTestID(14)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("links after the replay = %v, want %v", got, want)
		}
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)
		recorded := map[string]bool{}
		for _, commit := range strings.Fields(gitOutput(t, repo, "rev-list", "--first-parent", outcome.Next)) {
			pack, err := core.DecodeRelicOperationPack(relicBlob(t, repo, commit+":operation.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range pack.Operations {
				recorded[operation.ID] = true
			}
		}
		for _, number := range []int{11, 12, 13, 14, 101, 201, 202} {
			if !recorded[relicTestID(number)] {
				t.Errorf("operation %s is not in the replayed history", relicTestID(number))
			}
		}
	})
}

func TestReplayOfALinkFromAPartRemovedElsewhereWarns(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p := relicTestPartNumbered(1)
		root := writeRelicRoot(t, repo, config, p)
		relicID := root.Record.State.RelicID
		local := relicHistoryFrom(t, repo, config, root, relicOps(relicLinkAddOp(13, p.id, relicLinkTaskID(1), "implemented-by")))
		remote := relicHistoryFrom(t, repo, config, root, relicOps(relicPartChange(201, core.RelicOperationPartRemove, p.id)))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if got := relicLinkIDsAt(t, repo, outcome.Next); !reflect.DeepEqual(got, []string{relicTestID(13)}) {
			t.Fatalf("links after the replay = %v, want the link from the removed part", got)
		}
		replayed := relicCommitAt(t, repo, config, relicID, outcome.Next)
		if part, ok := replayed.Record.State.Relic.Part(p.id); !ok || !part.Removed {
			t.Fatalf("part %s = %+v (present %v), want it still removed", p.id, part, ok)
		}
		if len(outcome.Warnings) != 1 || outcome.Warnings[0].Code != core.WarningRelicEditOnRemovedPart ||
			!strings.Contains(outcome.Warnings[0].Message, "part "+p.id) {
			t.Fatalf("warnings = %#v, want one relic-edit-on-removed-part naming %s", outcome.Warnings, p.id)
		}
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)
	})
}

func TestReplayKeepsConcurrentRelicLinkChanges(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p := relicTestPartNumbered(1)
		x, p1, p2 := relicLinkRelicID(2), relicTestID(752001), relicTestID(752002)
		root := writeRelicRoot(t, repo, config, p)
		relicID := root.Record.State.RelicID
		shared := relicHistoryFrom(t, repo, config, root, relicOps(relicRelicLinkAddOp(11, p.id, x, p1, "decided-in", "")))
		local := relicHistoryFrom(t, repo, config, shared, relicOps(
			relicRelicLinkAddOp(12, p.id, x, p2, "decided-in", `{"at":"00:00:02"}`),
			relicLinkRemoveOp(101, 11)))
		remote := relicHistoryFrom(t, repo, config, shared, relicOps(
			relicRelicLinkAddOp(13, "", x, "", "supersedes", ""),
			relicLinkRemoveOp(201, 11)))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		links, err := core.DecodeRelicLinksDocument(relicBlob(t, repo, outcome.Next+":links.json"))
		if err != nil {
			t.Fatal(err)
		}
		want := []core.RelicLink{
			{ID: relicTestID(12), From: p.id, To: core.RelicLinkTarget{Relic: x, Part: p2}, Rel: "decided-in",
				Seen: relicTestID(800000), Pointer: json.RawMessage(`{"at":"00:00:02"}`)},
			{ID: relicTestID(13), To: core.RelicLinkTarget{Relic: x}, Rel: "supersedes", Seen: relicTestID(800000)},
		}
		if !reflect.DeepEqual(links.Links, want) {
			t.Fatalf("links after the replay = %#v, want %#v", links.Links, want)
		}
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)
	})
}

func TestReadRefusesAForgedRelicLink(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		pack, _ := relicRootPack(config, 1, relicTestPartNumbered(1))
		record := foldRelic(t, nil, pack)
		relicID := pack.RelicID
		links := []byte(`{"format":"workbook.relic-links","version":1,"links":[{"id":"` + relicTestID(20) +
			`","to":{"relic":"` + relicLinkRelicID(3) + `"},"rel":"relates-to","seen":"` + relicTestID(21) +
			`","pointer":{"b":1,"a":2}}]}` + "\n")
		partTree := forgeRelicTree(t, repo, []string{
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, core.NewRelicFields(nil))) + "\tfields.json"})
		tree := forgeRelicTree(t, repo, []string{
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, pack)) + "\toperation.json",
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, record.State)) + "\tstate.json",
			"100644 blob " + forgeRelicBlob(t, repo, links) + "\tlinks.json",
			"040000 tree " + forgeRelicTree(t, repo, []string{"040000 tree " + partTree + "\t" + relicTestID(1)}) + "\tparts",
		})
		commit := forgeRelicCommitOf(t, repo, tree)
		pointRelicRef(t, repo, relicID, commit)

		for label, read := range map[string]func() error{
			"GetRelic()": func() error {
				_, err := repo.GetRelic(context.Background(), config, relicID)
				return err
			},
			"ReadRelicTips()": func() error {
				_, err := repo.ReadRelicTips(context.Background(), config, []core.RelicHead{{RelicID: relicID, ObjectID: commit}})
				return err
			},
		} {
			if err := read(); core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(err.Error(), relicID) ||
				!strings.Contains(err.Error(), "pointer") {
				t.Errorf("%s error = %v, want corrupt data naming the relic and the pointer", label, err)
			}
		}
	})
}
