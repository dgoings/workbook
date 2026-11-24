package gitstore

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicStep is one pack a test history records: its operations and the
// content that goes beside them. The fields of an existing part the pack
// edits are filled in from the history it is written on.
type relicStep struct {
	content    map[string]core.RelicPartContent
	operations []core.RelicOperation
}

func relicOps(operations ...core.RelicOperation) relicStep {
	return relicStep{operations: operations}
}

func (step relicStep) withBody(partID string, body []byte) relicStep {
	content := make(map[string]core.RelicPartContent, len(step.content)+1)
	for id, stored := range step.content {
		content[id] = stored
	}
	content[partID] = core.RelicPartContent{Body: body, HasBody: true}
	step.content = content
	return step
}

// relicHistoryFrom writes steps one after another on top of from and returns
// the last commit. The relic's ref is left at that commit.
func relicHistoryFrom(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	from core.RelicSnapshot,
	steps ...relicStep,
) core.RelicSnapshot {
	t.Helper()
	pointRelicRef(t, repo, from.Record.State.RelicID, from.Head)
	tip := from
	for _, step := range steps {
		content := make(map[string]core.RelicPartContent, len(step.content))
		for id, stored := range step.content {
			content[id] = stored
		}
		for partID := range relicPartsWithFieldOperations(core.RelicOperationPack{Operations: step.operations}) {
			if _, existed := tip.Record.State.Relic.Part(partID); !existed {
				continue
			}
			stored := content[partID]
			stored.Fields = relicFieldsAfter(t, repo, config, tip, partID, step.operations...)
			content[partID] = stored
		}
		tip = writeRelicChild(t, repo, config, tip, content, step.operations...)
	}
	return tip
}

func relicTombstone(operationNumber int) core.RelicOperation {
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationTombstone}
}

func relicBodySetWithFormat(operationNumber int, partID, base, format string, body []byte) core.RelicOperation {
	operation := relicBodySet(operationNumber, partID, base, body)
	operation.Body.Format = format
	return operation
}

// replayDiverged classifies the two tips, which must have diverged, replays
// the local one onto the remote one, and checks that no ref moved.
func replayDiverged(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, local, remote string) relicReconcileOutcome {
	t.Helper()
	ctx := context.Background()
	pointRelicRef(t, repo, relicID, local)
	request := relicReconcileRequest{RelicID: relicID, Local: local, Remote: remote}
	ahead, behind, diverged, graph, err := repo.classifyRelicHeads(ctx, []relicReconcileRequest{request})
	if err != nil {
		t.Fatalf("classifyRelicHeads() error = %v", err)
	}
	if len(ahead) != 0 || len(behind) != 0 || len(diverged) != 1 || diverged[0] != request {
		t.Fatalf("classifyRelicHeads() = ahead %v, behind %v, diverged %v; want the request diverged", ahead, behind, diverged)
	}
	outcomes := repo.reconcileDivergentRelics(ctx, config, diverged, graph)
	if len(outcomes) != 1 || outcomes[0].RelicID != relicID {
		t.Fatalf("reconcileDivergentRelics() = %+v, want one outcome for %s", outcomes, relicID)
	}
	if got := gitOutput(t, repo, "rev-parse", relicRef(relicID)); got != local {
		t.Fatalf("the relic ref moved to %s during the replay, want %s", got, local)
	}
	return outcomes[0]
}

func replayDivergedSnapshots(t *testing.T, repo *Repository, config core.ProjectConfig, local, remote core.RelicSnapshot) relicReconcileOutcome {
	t.Helper()
	return replayDiverged(t, repo, config, local.Record.State.RelicID, local.Head, remote.Head)
}

func requireReplayed(t *testing.T, outcome relicReconcileOutcome) {
	t.Helper()
	if outcome.Err != nil {
		t.Fatalf("replay error = %v", outcome.Err)
	}
	if outcome.Next == "" {
		t.Fatalf("replay returned no next tip: %+v", outcome)
	}
}

// assertReplayedHistory asserts that next is a valid relic history in which
// no operation ID appears in two commits of its first-parent chain.
func assertReplayedHistory(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, next string) {
	t.Helper()
	commits := strings.Fields(gitOutput(t, repo, "rev-list", "--first-parent", next))
	assertRelicHistoryValid(t, repo, config, relicID, next, len(commits))
	usedIn := map[string]string{}
	for _, commit := range commits {
		pack, err := core.DecodeRelicOperationPack(relicBlob(t, repo, commit+":operation.json"))
		if err != nil {
			t.Fatalf("DecodeRelicOperationPack(%s) error = %v", commit, err)
		}
		for _, operation := range pack.Operations {
			if first, used := usedIn[operation.ID]; used {
				t.Fatalf("operation %s is recorded in commit %s and again in commit %s", operation.ID, first, commit)
			}
			usedIn[operation.ID] = commit
		}
	}
}

func relicCommitAt(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, commit string) RelicCommit {
	t.Helper()
	read, err := repo.ReadRelicCommit(context.Background(), config, relicID, commit)
	if err != nil {
		t.Fatalf("ReadRelicCommit(%s) error = %v", commit, err)
	}
	return read
}

func relicFieldsAt(t *testing.T, repo *Repository, commit, partID string) map[string]json.RawMessage {
	t.Helper()
	document, err := core.DecodeRelicFieldsDocument(relicBlob(t, repo, commit+":parts/"+partID+"/fields.json"))
	if err != nil {
		t.Fatalf("DecodeRelicFieldsDocument(%s, %s) error = %v", commit, partID, err)
	}
	return document.Fields
}

func assertRelicFieldValue(t *testing.T, fields map[string]json.RawMessage, name, want string) {
	t.Helper()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := fields[name]; !ok || string(got) != string(encoded) {
		t.Fatalf("field %q = %s (present %v), want %s", name, got, ok, encoded)
	}
}

// replayedHistoryScenario has the local side add a part with a body and set
// field a on part p, and the remote side set field b on p and then save part
// q's body.
type replayedHistoryScenario struct {
	root, local, remote core.RelicSnapshot
	p, q, added         string
}

func writeReplayedHistoryScenario(t *testing.T, repo *Repository, config core.ProjectConfig) replayedHistoryScenario {
	t.Helper()
	p := relicTestPartNumbered(1).withField("heading", "Goals")
	q := relicTestPartNumbered(2).withBody("md", []byte("first notes\n"))
	root := writeRelicRoot(t, repo, config, p, q)
	added := relicTestID(3)
	addedBody := []byte("a part added here\n")
	local := relicHistoryFrom(t, repo, config, root,
		relicOps(relicPartAdd(added, "3/1", addedBody), relicFieldSet(101, p.id, "a", "from here")).withBody(added, addedBody))
	second := []byte("second notes\n")
	remote := relicHistoryFrom(t, repo, config, root,
		relicOps(relicFieldSet(201, p.id, "b", "from there")),
		relicOps(relicBodySet(202, q.id, q.id, second)).withBody(q.id, second))
	return replayedHistoryScenario{root: root, local: local, remote: remote, p: p.id, q: q.id, added: added}
}

func TestReplayedRelicHistoryIsValid(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		scenario := writeReplayedHistoryScenario(t, repo, config)
		relicID := scenario.root.Record.State.RelicID
		outcome := replayDivergedSnapshots(t, repo, config, scenario.local, scenario.remote)
		requireReplayed(t, outcome)
		if outcome.Conflict != nil || outcome.Replayed != 1 || outcome.Skipped != 0 || outcome.ParkedHead != "" {
			t.Fatalf("outcome = %+v, want one pack replayed and nothing parked", outcome)
		}
		if parents := relicParents(t, repo, outcome.Next); len(parents) != 1 || parents[0] != scenario.remote.Head {
			t.Fatalf("next tip's parents = %v, want the remote tip %s", parents, scenario.remote.Head)
		}
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)

		fields := relicFieldsAt(t, repo, outcome.Next, scenario.p)
		assertRelicFieldValue(t, fields, "heading", "Goals")
		assertRelicFieldValue(t, fields, "a", "from here")
		assertRelicFieldValue(t, fields, "b", "from there")
		bodyPath := ":parts/" + scenario.added + "/body"
		if got, want := gitOutput(t, repo, "rev-parse", outcome.Next+bodyPath), gitOutput(t, repo, "rev-parse", scenario.local.Head+bodyPath); got != want {
			t.Fatalf("the added part's body is blob %s, want the local commit's %s", got, want)
		}
		qBody := ":parts/" + scenario.q + "/body"
		if got, want := gitOutput(t, repo, "rev-parse", outcome.Next+qBody), gitOutput(t, repo, "rev-parse", scenario.remote.Head+qBody); got != want {
			t.Fatalf("part q's body is blob %s, want the remote's %s", got, want)
		}

		replayed, err := core.DecodeRelicOperationPack(relicBlob(t, repo, outcome.Next+":operation.json"))
		if err != nil {
			t.Fatalf("DecodeRelicOperationPack() error = %v", err)
		}
		if replayed.LogicalClock != scenario.remote.Record.State.LogicalClock+1 {
			t.Fatalf("replayed logical clock = %d, want %d", replayed.LogicalClock, scenario.remote.Record.State.LogicalClock+1)
		}
		original := scenario.local.Operation
		replayed.LogicalClock, original.LogicalClock = 0, 0
		gotJSON, _ := json.Marshal(replayed)
		wantJSON, _ := json.Marshal(original)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("replayed pack = %s, want the local pack %s", gotJSON, wantJSON)
		}
	})
}

func TestReplayReadsNoBodyText(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	scenario := writeReplayedHistoryScenario(t, repo, config)
	relicID := scenario.root.Record.State.RelicID

	requests := observeRelicObjects(repo)
	outcome := replayDivergedSnapshots(t, repo, config, scenario.local, scenario.remote)
	repo.relicObjectObserver = nil
	requireReplayed(t, outcome)

	bodies := map[string]bool{}
	for _, commit := range []string{scenario.root.Head, scenario.local.Head, scenario.remote.Head, outcome.Next} {
		for _, line := range relicLines(gitOutput(t, repo, "ls-tree", "-r", commit, "parts/")) {
			meta, path, _ := strings.Cut(line, "\t")
			if strings.HasSuffix(path, "/body") {
				bodies[strings.Fields(meta)[2]] = true
			}
		}
	}
	if len(bodies) < 3 {
		t.Fatalf("found %d body blobs, want the scenario's three", len(bodies))
	}
	if len(*requests) == 0 {
		t.Fatalf("the replay asked for no relic object")
	}
	for _, request := range *requests {
		if bodies[request] || strings.HasSuffix(request, "/body") {
			t.Fatalf("replaying relic %s asked for body %q; all requests: %q", relicID, request, *requests)
		}
	}
}

func TestReplayStopsOnTheSameBodyAndKeepsBothTexts(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p := relicTestPartNumbered(1).withBody("md", []byte("shared text\n"))
		root := writeRelicRoot(t, repo, config, p)
		ours, theirs := []byte("the text saved here\n"), []byte("the text saved there, longer\n")
		local := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(101, p.id, p.id, ours)).withBody(p.id, ours))
		remote := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(201, p.id, p.id, theirs)).withBody(p.id, theirs))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		conflict := outcome.Conflict
		if conflict == nil || conflict.Type != core.RelicConflictBody || conflict.Part != p.id || conflict.Body == nil || conflict.Body.Base == nil {
			t.Fatalf("conflict = %+v, want a body conflict on part %s with three versions", conflict, p.id)
		}
		bodyPath := ":parts/" + p.id + "/body"
		for _, check := range []struct {
			name    string
			got     core.RelicBodyVersion
			commit  string
			version string
			bytes   int
		}{
			{"base", *conflict.Body.Base, root.Head, p.id, len("shared text\n")},
			{"ours", conflict.Body.Ours, local.Head, relicTestID(101), len(ours)},
			{"theirs", conflict.Body.Theirs, remote.Head, relicTestID(201), len(theirs)},
		} {
			want := core.RelicBodyVersion{
				Version: check.version, Format: "md", Bytes: int64(check.bytes),
				Object: gitOutput(t, repo, "rev-parse", check.commit+bodyPath),
			}
			if check.got.Version != want.Version || check.got.Format != want.Format || check.got.Bytes != want.Bytes ||
				check.got.Object != want.Object || check.got.Text != nil || check.got.Base64 != nil {
				t.Fatalf("%s = %+v, want %+v", check.name, check.got, want)
			}
		}
		if conflict.Unreplayed != 1 || outcome.Replayed != 0 {
			t.Fatalf("unreplayed = %d, replayed = %d; want 1 and 0", conflict.Unreplayed, outcome.Replayed)
		}
		if outcome.Next != remote.Head || outcome.ParkedHead != local.Head {
			t.Fatalf("next = %s, parked = %s; want the remote tip %s and the local tip %s",
				outcome.Next, outcome.ParkedHead, remote.Head, local.Head)
		}
		if got, want := gitOutput(t, repo, "rev-parse", outcome.Next+bodyPath), gitOutput(t, repo, "rev-parse", remote.Head+bodyPath); got != want {
			t.Fatalf("the body at the next tip is %s, want the remote's %s", got, want)
		}
	})
}

func TestReplayTreatsIdenticalBodiesAsAgreement(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	p := relicTestPartNumbered(1).withBody("md", []byte("shared text\n"))
	root := writeRelicRoot(t, repo, config, p)
	same := []byte("the same new text\n")
	local := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(101, p.id, p.id, same)).withBody(p.id, same))
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(201, p.id, p.id, same)).withBody(p.id, same))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	// The local save changes nothing once the two sides agree, so no commit
	// records it.
	if outcome.Conflict != nil || outcome.ParkedHead != "" || outcome.Skipped != 1 || outcome.Next != remote.Head {
		t.Fatalf("outcome = %+v, want no conflict and the pack skipped", outcome)
	}
	part, _ := relicCommitAt(t, repo, config, root.Record.State.RelicID, outcome.Next).Record.State.Relic.Part(p.id)
	if part.Body == nil || part.Body.Version != relicTestID(201) {
		t.Fatalf("body at the next tip = %+v, want the remote's version %s", part.Body, relicTestID(201))
	}
	assertReplayedHistory(t, repo, config, root.Record.State.RelicID, outcome.Next)
}

func TestReplayStopsAtTheFirstConflictAndKeepsEarlierPacks(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	p := relicTestPartNumbered(1).withBody("md", []byte("shared text\n"))
	root := writeRelicRoot(t, repo, config, p)
	relicID := root.Record.State.RelicID
	ours, theirs := []byte("saved here\n"), []byte("saved there\n")
	local := relicHistoryFrom(t, repo, config, root,
		relicOps(relicTitleSet(101, "The title from here")),
		relicOps(relicBodySet(102, p.id, p.id, ours)).withBody(p.id, ours),
		relicOps(relicPropertySet(103, "owner", "here")))
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(201, p.id, p.id, theirs)).withBody(p.id, theirs))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	if outcome.Conflict == nil || outcome.Conflict.Type != core.RelicConflictBody || outcome.Conflict.Unreplayed != 2 {
		t.Fatalf("conflict = %+v, want a body conflict with two packs unreplayed", outcome.Conflict)
	}
	if outcome.Replayed != 1 || outcome.ParkedHead != local.Head {
		t.Fatalf("replayed = %d, parked = %s; want 1 and the local tip %s", outcome.Replayed, outcome.ParkedHead, local.Head)
	}
	if parents := relicParents(t, repo, outcome.Next); len(parents) != 1 || parents[0] != remote.Head {
		t.Fatalf("next tip's parents = %v, want the remote tip %s", parents, remote.Head)
	}
	next := relicCommitAt(t, repo, config, relicID, outcome.Next)
	if next.Record.State.Relic.Title != "The title from here" {
		t.Fatalf("title at the next tip = %q, want the local one", next.Record.State.Relic.Title)
	}
	if _, set := next.Record.State.Relic.Properties["owner"]; set {
		t.Fatalf("the property after the conflict was replayed: %v", next.Record.State.Relic.Properties)
	}
	if conflict := outcome.Conflict.Body; conflict == nil || conflict.Ours.Version != relicTestID(102) || conflict.Theirs.Version != relicTestID(201) {
		t.Fatalf("body conflict = %+v, want ours %s and theirs %s", conflict, relicTestID(102), relicTestID(201))
	}
	assertReplayedHistory(t, repo, config, relicID, outcome.Next)
}

func TestReplayOntoADeletedRelicIsATombstoneConflict(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	local := relicHistoryFrom(t, repo, config, root, relicOps(relicTitleSet(101, "The title from here")))
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicTombstone(201)))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	conflict := outcome.Conflict
	if conflict == nil || conflict.Type != core.RelicConflictTombstone || conflict.Tombstone == nil ||
		conflict.Tombstone.OperationID != relicTestID(101) || conflict.Tombstone.Operation != core.RelicOperationTitleSet {
		t.Fatalf("conflict = %+v, want a tombstone conflict naming title.set %s", conflict, relicTestID(101))
	}
	if conflict.Unreplayed != 1 || outcome.Next != remote.Head || outcome.ParkedHead != local.Head {
		t.Fatalf("outcome = %+v, want one pack unreplayed, the remote tip next and the local tip parked", outcome)
	}
}

// requireUnapplied asserts a stopped replay's list of the other local
// operations it did not apply.
func requireUnapplied(t *testing.T, outcome relicReconcileOutcome, want ...core.RelicUnappliedOperation) {
	t.Helper()
	if outcome.Conflict == nil {
		t.Fatalf("outcome = %+v, want a conflict", outcome)
	}
	got := outcome.Conflict.Unapplied
	if len(got) != len(want) {
		t.Fatalf("unapplied = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unapplied = %+v, want %+v", got, want)
		}
	}
}

func unappliedAt(operation core.RelicOperation, commit core.RelicSnapshot) core.RelicUnappliedOperation {
	return core.RelicUnappliedOperation{OperationID: operation.ID, Operation: operation.Type, Commit: commit.Head}
}

func TestABodyConflictNamesTheOtherLocalChanges(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	p := relicTestPartNumbered(1).withBody("md", []byte("shared text\n"))
	root := writeRelicRoot(t, repo, config, p)
	ours, theirs := []byte("saved here\n"), []byte("saved there\n")
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicBodySet(201, p.id, p.id, theirs)).withBody(p.id, theirs))

	t.Run("in the same command", func(t *testing.T) {
		body, property, title := relicBodySet(101, p.id, p.id, ours), relicPropertySet(102, "owner", "here"), relicTitleSet(103, "Lost title")
		local := relicHistoryFrom(t, repo, config, root, relicOps(body, property, title).withBody(p.id, ours))
		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if outcome.Conflict.Type != core.RelicConflictBody || outcome.Conflict.Unreplayed != 1 {
			t.Fatalf("conflict = %+v, want a body conflict with one pack unreplayed", outcome.Conflict)
		}
		requireUnapplied(t, outcome, unappliedAt(property, local), unappliedAt(title, local))
	})

	t.Run("in a later command", func(t *testing.T) {
		body, title := relicBodySet(111, p.id, p.id, ours), relicTitleSet(112, "Lost title")
		first := relicHistoryFrom(t, repo, config, root, relicOps(body).withBody(p.id, ours))
		local := relicHistoryFrom(t, repo, config, first, relicOps(title))
		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if outcome.Conflict.Type != core.RelicConflictBody || outcome.Conflict.Unreplayed != 2 {
			t.Fatalf("conflict = %+v, want a body conflict with two packs unreplayed", outcome.Conflict)
		}
		requireUnapplied(t, outcome, unappliedAt(title, local))
	})

	t.Run("not one the remote already holds", func(t *testing.T) {
		body, title, property := relicBodySet(121, p.id, p.id, ours), relicTitleSet(122, "Lost title"), relicPropertySet(123, "owner", "here")
		first := relicHistoryFrom(t, repo, config, root, relicOps(body, title).withBody(p.id, ours))
		local := relicHistoryFrom(t, repo, config, first, relicOps(property))
		holding := relicHistoryFrom(t, repo, config, remote, relicOps(property))
		outcome := replayDivergedSnapshots(t, repo, config, local, holding)
		requireReplayed(t, outcome)
		requireUnapplied(t, outcome, unappliedAt(title, first))
	})
}

func TestATombstoneConflictNamesTheOtherLocalChanges(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	title, owner, reviewer := relicTitleSet(101, "Lost title"), relicPropertySet(102, "owner", "here"), relicPropertySet(103, "reviewer", "here")
	first := relicHistoryFrom(t, repo, config, root, relicOps(title, owner))
	local := relicHistoryFrom(t, repo, config, first, relicOps(reviewer))
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicTombstone(201)))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	conflict := outcome.Conflict
	if conflict == nil || conflict.Type != core.RelicConflictTombstone || conflict.Tombstone == nil ||
		conflict.Tombstone.OperationID != title.ID || conflict.Unreplayed != 2 {
		t.Fatalf("conflict = %+v, want a tombstone conflict naming %s with two packs unreplayed", conflict, title.ID)
	}
	requireUnapplied(t, outcome, unappliedAt(owner, first), unappliedAt(reviewer, local))
}

func TestReplayingADeletionOverAnEditDeletes(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	relicID := root.Record.State.RelicID
	local := relicHistoryFrom(t, repo, config, root, relicOps(relicTombstone(101)))
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicTitleSet(201, "The title from there")))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	if outcome.Conflict != nil || outcome.Replayed != 1 || outcome.ParkedHead != "" {
		t.Fatalf("outcome = %+v, want the deletion replayed", outcome)
	}
	relic := relicCommitAt(t, repo, config, relicID, outcome.Next).Record.State.Relic
	if !relic.Deleted || relic.Title != "The title from there" {
		t.Fatalf("relic at the next tip = deleted %v, title %q; want deleted holding the remote's title", relic.Deleted, relic.Title)
	}
	assertReplayedHistory(t, repo, config, relicID, outcome.Next)
}

func TestReplayCombinedFieldsOverTheTotalApplyAndWarn(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p := relicTestPartNumbered(1)
		root := writeRelicRoot(t, repo, config, p)
		relicID := root.Record.State.RelicID
		here, there := strings.Repeat("h", 9*1024), strings.Repeat("t", 9*1024)
		local := relicHistoryFrom(t, repo, config, root, relicOps(relicFieldSet(101, p.id, "here", here)))
		remote := relicHistoryFrom(t, repo, config, root, relicOps(relicFieldSet(201, p.id, "there", there)))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if outcome.Conflict != nil || outcome.Replayed != 1 {
			t.Fatalf("outcome = %+v, want the field replayed without a conflict", outcome)
		}
		fields := relicFieldsAt(t, repo, outcome.Next, p.id)
		assertRelicFieldValue(t, fields, "here", here)
		assertRelicFieldValue(t, fields, "there", there)
		size := relicObjectSize(t, repo, outcome.Next+":parts/"+p.id+"/fields.json")
		if size <= core.MaxRelicPartFieldsBytes {
			t.Fatalf("combined fields are %d bytes, want them over %d", size, core.MaxRelicPartFieldsBytes)
		}
		if len(outcome.Warnings) != 1 {
			t.Fatalf("warnings = %+v, want one", outcome.Warnings)
		}
		warning := outcome.Warnings[0]
		for _, want := range []string{"relic " + relicID + ":", "part " + p.id, strconv.FormatInt(size, 10), "16384"} {
			if warning.Code != core.WarningRelicOverLimit || !strings.Contains(warning.Message, want) {
				t.Fatalf("warning = %+v, want code %s naming %q", warning, core.WarningRelicOverLimit, want)
			}
		}
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)
	})
}

func TestReplayNeverRecordsAnOperationTwice(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	relicID := root.Record.State.RelicID
	// The remote already holds the first local pack, under a commit of its own.
	local := relicHistoryFrom(t, repo, config, root,
		relicOps(relicTitleSet(101, "The shared title")),
		relicOps(relicPropertySet(102, "owner", "here")))
	remote := relicHistoryFrom(t, repo, config, root,
		relicOps(relicPropertySet(201, "reviewer", "there")),
		relicOps(relicTitleSet(101, "The shared title")))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	if outcome.Conflict != nil || outcome.Skipped != 1 || outcome.Replayed != 1 {
		t.Fatalf("outcome = %+v, want one pack skipped and one replayed", outcome)
	}
	assertReplayedHistory(t, repo, config, relicID, outcome.Next)

	// The same local commits met again, as a retry would meet them, are all
	// held already, and nothing is written.
	again := replayDiverged(t, repo, config, relicID, local.Head, outcome.Next)
	requireReplayed(t, again)
	if again.Next != outcome.Next || again.Skipped != 2 || again.Replayed != 0 || again.Conflict != nil {
		t.Fatalf("retry outcome = %+v, want both packs skipped and the same tip %s", again, outcome.Next)
	}
}

func TestReplayKeepsTheStoredBodyFormatWhenComparing(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	p := relicTestPartNumbered(1).withBody("txt", []byte("shared text\n"))
	root := writeRelicRoot(t, repo, config, p)
	same := []byte("the same new text\n")
	// The local save inherits the stored label, which the service writes into
	// the operation; the remote one asked for md.
	local := relicHistoryFrom(t, repo, config, root,
		relicOps(relicBodySetWithFormat(101, p.id, p.id, "txt", same)).withBody(p.id, same))
	remote := relicHistoryFrom(t, repo, config, root,
		relicOps(relicBodySetWithFormat(201, p.id, p.id, "md", same)).withBody(p.id, same))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	if outcome.Conflict == nil || outcome.Conflict.Type != core.RelicConflictBody || outcome.Conflict.Part != p.id {
		t.Fatalf("conflict = %+v, want a body conflict on part %s", outcome.Conflict, p.id)
	}
	if body := outcome.Conflict.Body; body.Ours.Object != body.Theirs.Object || body.Ours.Format != "txt" || body.Theirs.Format != "md" {
		t.Fatalf("body conflict = %+v, want the same blob under txt and md", body)
	}
}

func TestReplayKeepsARemovedPartRemovedAndWarns(t *testing.T) {
	t.Parallel()
	for _, direction := range []struct {
		name          string
		local, remote core.RelicOperation
		field, value  string
	}{
		{
			name:  "removed there and edited here",
			local: relicFieldSet(101, relicTestID(1), "status", "edited here"), remote: relicPartChange(201, core.RelicOperationPartRemove, relicTestID(1)),
			field: "status", value: "edited here",
		},
		{
			name:  "edited there and removed here",
			local: relicPartChange(101, core.RelicOperationPartRemove, relicTestID(1)), remote: relicFieldSet(201, relicTestID(1), "status", "edited there"),
			field: "status", value: "edited there",
		},
	} {
		t.Run(direction.name, func(t *testing.T) {
			t.Parallel()
			repo, config := relicRepository(t, testrepo.FormatSHA1)
			p := relicTestPartNumbered(1).withField("heading", "Goals")
			root := writeRelicRoot(t, repo, config, p)
			relicID := root.Record.State.RelicID
			local := relicHistoryFrom(t, repo, config, root, relicOps(direction.local))
			remote := relicHistoryFrom(t, repo, config, root, relicOps(direction.remote))

			outcome := replayDivergedSnapshots(t, repo, config, local, remote)
			requireReplayed(t, outcome)
			if outcome.Conflict != nil || outcome.Replayed != 1 {
				t.Fatalf("outcome = %+v, want the local pack replayed", outcome)
			}
			part, _ := relicCommitAt(t, repo, config, relicID, outcome.Next).Record.State.Relic.Part(p.id)
			if !part.Removed {
				t.Fatalf("part %s at the next tip = %+v, want it removed", p.id, part)
			}
			assertRelicFieldValue(t, relicFieldsAt(t, repo, outcome.Next, p.id), direction.field, direction.value)
			if len(outcome.Warnings) != 1 || outcome.Warnings[0].Code != core.WarningRelicEditOnRemovedPart ||
				!strings.Contains(outcome.Warnings[0].Message, "part "+p.id) {
				t.Fatalf("warnings = %+v, want one %s naming part %s", outcome.Warnings, core.WarningRelicEditOnRemovedPart, p.id)
			}
			assertReplayedHistory(t, repo, config, relicID, outcome.Next)
		})
	}
}

func TestReplaySkipsOperationsTheOtherSideAlreadyHolds(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	operations := []core.RelicOperation{relicTitleSet(101, "The shared title"), relicPropertySet(102, "owner", "here")}
	local := relicHistoryFrom(t, repo, config, root, relicOps(operations...))
	remote := relicHistoryFrom(t, repo, config, root,
		relicOps(relicPropertySet(201, "reviewer", "there")),
		relicOps(operations...))

	outcome := replayDivergedSnapshots(t, repo, config, local, remote)
	requireReplayed(t, outcome)
	if outcome.Skipped != 1 || outcome.Replayed != 0 || outcome.Next != remote.Head || outcome.ParkedHead != "" || outcome.Conflict != nil {
		t.Fatalf("outcome = %+v, want the pack skipped and the remote tip %s next", outcome, remote.Head)
	}
}

func TestAReplayThatCannotFoldMovesNothing(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	relicID := root.Record.State.RelicID
	// No writer restores a live relic in the same pack as an edit. The state
	// beside the forged pack is the edit's alone.
	title := relicTitleSet(101, "A forged title")
	forged := relicChildPack(root, core.RelicOperation{ID: relicTestID(102), Type: core.RelicOperationRestore}, title)
	record := foldRelic(t, &root, relicChildPack(root, title))
	local := forgeRelicCommit(t, repo, root.Head, encodedRelicDocument(t, forged), encodedRelicDocument(t, record.State), nil)
	remote := relicHistoryFrom(t, repo, config, root, relicOps(relicPropertySet(201, "reviewer", "there")))

	outcome := replayDiverged(t, repo, config, relicID, local, remote.Head)
	if outcome.Err == nil || !strings.HasPrefix(outcome.Err.Error(), "relic "+relicID) {
		t.Fatalf("outcome = %+v, want an error naming relic %s", outcome, relicID)
	}
	if outcome.Next != "" || outcome.ParkedHead != "" || outcome.Conflict != nil {
		t.Fatalf("outcome = %+v, want nothing to move", outcome)
	}
}

func TestClassifyRelicHeadsSortsEachPair(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	var pairs []relicReconcileRequest
	for number := 1; number <= 4; number++ {
		root := writeRelicRootNumbered(t, repo, config, number)
		relicID := root.Record.State.RelicID
		older := relicHistoryFrom(t, repo, config, root, relicOps(relicTitleSet(100+number, "older")))
		switch number {
		case 1: // equal
			pairs = append(pairs, relicReconcileRequest{RelicID: relicID, Local: older.Head, Remote: older.Head})
		case 2: // local ahead
			newer := relicHistoryFrom(t, repo, config, older, relicOps(relicTitleSet(200+number, "newer")))
			pairs = append(pairs, relicReconcileRequest{RelicID: relicID, Local: newer.Head, Remote: older.Head})
		case 3: // remote ahead
			newer := relicHistoryFrom(t, repo, config, older, relicOps(relicTitleSet(200+number, "newer")))
			pairs = append(pairs, relicReconcileRequest{RelicID: relicID, Local: older.Head, Remote: newer.Head})
		case 4: // diverged
			other := relicHistoryFrom(t, repo, config, root, relicOps(relicTitleSet(300+number, "other")))
			pairs = append(pairs, relicReconcileRequest{RelicID: relicID, Local: older.Head, Remote: other.Head})
		}
	}
	commands := observeCommands(repo)
	ahead, behind, diverged, graph, err := repo.classifyRelicHeads(ctx, pairs)
	ran := *commands
	repo.commandObserver = nil
	if err != nil {
		t.Fatalf("classifyRelicHeads() error = %v", err)
	}
	if len(ahead) != 1 || ahead[0] != pairs[1] || len(behind) != 1 || behind[0] != pairs[2] || len(diverged) != 1 || diverged[0] != pairs[3] {
		t.Fatalf("classifyRelicHeads() = ahead %v, behind %v, diverged %v", ahead, behind, diverged)
	}
	if countCommand(ran, "rev-list", "--parents", "--stdin") != 1 {
		t.Fatalf("classifyRelicHeads() ran %q, want one rev-list --parents --stdin", ran)
	}
	if _, found := graph[pairs[3].Local]; !found {
		t.Fatalf("the graph lacks the diverged local tip")
	}
}
