package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var (
	relicCheckpointRetireOp   = relicTestID(7001)
	relicCheckpointRemoveOp   = relicTestID(7002)
	relicCheckpointOp         = relicTestID(7010)
	relicCheckpointGeneration = relicTestID(7011)
	relicCheckpointRun        = relicTestID(7012)
	relicCheckpointLink       = relicTestID(7013)
	relicCheckpointLinkedTask = "WB-" + relicTestID(7014)
	relicCheckpointTip        = strings.Repeat("c3", 20)
	relicCheckpointRemovedAt  = relicDocTime.Add(time.Minute)
	relicCheckpointSince      = relicDocTime.Add(2 * time.Minute)
	relicCheckpointWall       = relicDocTime.Add(24 * time.Hour)
)

// relicCheckpointForTest is a valid checkpoint root for the golden relic after
// a link from part A, the removal of part B and a retirement (or, for the
// deleted boundary, a deletion): the pack and the record stored beside it.
// Every call returns fresh values, so a case may change them freely.
func relicCheckpointForTest(boundary string) (RelicOperationPack, RelicRecord) {
	relic := goldenRelicState().Relic
	relic.UpdatedAt = relicCheckpointSince
	switch boundary {
	case RelicBoundaryRetired:
		relic.Retired = true
		relic.Parts[1] = RelicPart{ID: relicDocPartB, Type: "decision", Position: "2/1", Removed: true,
			Dropped: &RelicPartDrop{RemovedAt: relicCheckpointRemovedAt, DroppedAt: relicCheckpointWall}}
	case RelicBoundaryDeleted:
		relic.Deleted = true
		relic.Properties = map[string]json.RawMessage{}
		relic.Parts = []RelicPart{
			{ID: relicDocPartA, Type: "section", Position: "1/1", Removed: true,
				Dropped: &RelicPartDrop{RemovedAt: relicCheckpointSince, DroppedAt: relicCheckpointWall}},
			{ID: relicDocPartB, Type: "decision", Position: "2/1", Removed: true,
				Dropped: &RelicPartDrop{RemovedAt: relicCheckpointRemovedAt, DroppedAt: relicCheckpointWall}},
		}
	}
	relic.Compacted = &RelicCompaction{RunID: relicCheckpointRun, At: relicCheckpointWall, Boundary: boundary,
		Since: relicCheckpointSince}
	compactedFrom := relicCheckpointTip
	state := RelicStateDocument{
		Format:       relicStateFormat,
		Version:      documentVersion,
		MinReader:    RelicCheckpointMinReader,
		ProjectID:    relicDocProject,
		RelicID:      relicDocRelic,
		History:      History{Generation: relicCheckpointGeneration, CompactedFrom: &compactedFrom},
		LogicalClock: 4,
		Relic:        relic,
	}
	links := EmptyRelicLinks()
	links.Links = []RelicLink{{ID: relicCheckpointLink, From: relicDocPartA,
		To: RelicLinkTarget{Task: relicCheckpointLinkedTask}, Rel: "implemented-by"}}
	pack := NewRelicOperationPack(relicDocProject, relicDocRelic, relicCheckpointGeneration, relicDocActor, 4,
		relicCheckpointWall, []RelicOperation{{ID: relicCheckpointOp, Type: RelicOperationCheckpoint,
			Checkpoint: &RelicCheckpointData{
				RunID:              relicCheckpointRun,
				CompactedFrom:      relicCheckpointTip,
				PreviousGeneration: relicDocGeneration,
				Boundary:           boundary,
				Since:              relicCheckpointSince,
				CompactedCommits:   4,
				FirstWallTime:      relicDocTime,
				LastWallTime:       relicCheckpointSince,
				CompactedOperations: []string{relicDocCreateOp, relicDocPropertyOp, relicDocPartA, relicDocPartB,
					relicCheckpointRetireOp, relicCheckpointRemoveOp, relicCheckpointLink},
			}}})
	return pack, RelicRecord{State: state, Links: links}
}

func checkpointSummaryOf(pack *RelicOperationPack) *RelicCheckpointData {
	return pack.Operations[0].Checkpoint
}

func TestCheckpointRootRules(t *testing.T) {
	for _, boundary := range []string{RelicBoundaryRetired, RelicBoundaryDeleted} {
		pack, stored := relicCheckpointForTest(boundary)
		if err := ValidateRelicCheckpointRoot(pack, stored); err != nil {
			t.Fatalf("%s root: ValidateRelicCheckpointRoot = %v", boundary, err)
		}
		if err := ValidateRelicCheckpoint(nil, pack, stored); err != nil {
			t.Fatalf("%s root: ValidateRelicCheckpoint(nil) = %v", boundary, err)
		}
		summary, ok := pack.Checkpoint()
		if !ok || summary.Boundary != boundary || summary.RunID != relicCheckpointRun {
			t.Fatalf("%s root: Checkpoint() = %#v, %v", boundary, summary, ok)
		}
		for label, document := range map[string]any{"pack": pack, "state": stored.State} {
			encoded, err := EncodeDocument(document)
			if err != nil {
				t.Fatalf("%s root: EncodeDocument(%s) = %v", boundary, label, err)
			}
			if !strings.Contains(string(encoded), `"minReader":7,`) {
				t.Errorf("%s root: the %s is not at generation 7: %s", boundary, label, encoded)
			}
		}
		requireRelicStateRoundTrips(t, boundary+" root state", stored.State)
	}

	// Wall clocks are display metadata: a stub kept from an earlier
	// compaction by a clone whose clock ran fast may carry a droppedAt after
	// this checkpoint's wall time, and the root is still valid.
	for _, boundary := range []string{RelicBoundaryRetired, RelicBoundaryDeleted} {
		pack, stored := relicCheckpointForTest(boundary)
		stored.State.Relic.Parts[1].Dropped.DroppedAt = pack.WallTime.Add(time.Hour)
		if err := ValidateRelicCheckpointRoot(pack, stored); err != nil {
			t.Errorf("%s root with a stub dropped after its wall time: ValidateRelicCheckpointRoot = %v", boundary, err)
		}
	}

	retiredPack, _ := relicCheckpointForTest(RelicBoundaryRetired)
	_, err := ApplyRelic(nil, retiredPack)
	requireCorruptData(t, "ApplyRelic(nil, checkpoint)", err)
	if err == nil || !strings.Contains(err.Error(), "a checkpoint is validated against its stored record") {
		t.Errorf("ApplyRelic(nil, checkpoint) = %v, want the stored-record refusal", err)
	}

	type mutation struct {
		boundary string
		change   func(*RelicOperationPack, *RelicRecord)
	}
	retired, deleted := RelicBoundaryRetired, RelicBoundaryDeleted
	cases := map[string]mutation{
		// R1: one history.checkpoint, pack and state at generation 7.
		"R1 two operations": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			p.Operations = append(p.Operations, RelicOperation{ID: relicTestID(7020), Type: RelicOperationRetire})
		}},
		"R1 not a checkpoint": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			*p = NewRelicOperationPack(p.ProjectID, p.RelicID, p.HistoryGeneration, p.Actor.ID, p.LogicalClock, p.WallTime,
				[]RelicOperation{{ID: relicCheckpointOp, Type: RelicOperationRetire}})
		}},
		"R1 state at 6": {retired, func(_ *RelicOperationPack, r *RelicRecord) { r.State.MinReader = RelicMinReader }},
		"R1 pack at 6":  {retired, func(p *RelicOperationPack, _ *RelicRecord) { p.MinReader = RelicMinReader }},
		// R2: generation, compactedFrom and clocks.
		"R2 state in another generation": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.History.Generation = relicTestID(7021)
		}},
		"R2 generation not new": {retired, func(p *RelicOperationPack, r *RelicRecord) {
			p.HistoryGeneration = relicDocGeneration
			r.State.History.Generation = relicDocGeneration
		}},
		"R2 compactedFrom differs": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			other := strings.Repeat("d4", 20)
			r.State.History.CompactedFrom = &other
		}},
		"R2 no compactedFrom": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.History.CompactedFrom = nil
			r.State.Relic.Compacted = nil
		}},
		"R2 clocks differ": {retired, func(_ *RelicOperationPack, r *RelicRecord) { r.State.LogicalClock = 5 }},
		"R2 another relic": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.RelicID = "WB-R-" + relicTestID(7022)
		}},
		// R3: the state's compacted member is the run's.
		"R3 compacted at another time": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Compacted.At = relicCheckpointWall.Add(time.Second)
		}},
		"R3 compacted by another run": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Compacted.RunID = relicTestID(7023)
		}},
		"R3 compacted since differs": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Compacted.Since = relicCheckpointWall
		}},
		// R4: the boundary agrees with the relic.
		"R4 wrong boundary": {retired, func(p *RelicOperationPack, r *RelicRecord) {
			checkpointSummaryOf(p).Boundary = RelicBoundaryDeleted
			r.State.Relic.Compacted.Boundary = RelicBoundaryDeleted
		}},
		"R4 retired root of a live relic": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Retired = false
		}},
		"R4 retired root keeps a removed part's content": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Parts[1].Dropped = nil
		}},
		"R4 deleted root with properties": {deleted, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Properties = map[string]json.RawMessage{"status": json.RawMessage(`"approved"`)}
		}},
		"R4 deleted root keeps a part": {deleted, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Parts[0] = RelicPart{ID: relicDocPartA, Type: "section", Position: "1/1", Removed: true}
		}},
		"R4 deleted root of a live relic": {deleted, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Deleted = false
		}},
		// R5: stubs, and the list of what was replaced.
		"R5 live part with dropped": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Parts[0].Dropped = &RelicPartDrop{RemovedAt: relicCheckpointSince, DroppedAt: relicCheckpointWall}
		}},
		"R5 stub with a body": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.State.Relic.Parts[1].Body = &RelicBody{Format: "md", Version: relicDocPartB, Bytes: 3}
		}},
		"R5 unsorted list": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			list := checkpointSummaryOf(p).CompactedOperations
			list[0], list[1] = list[1], list[0]
		}},
		"R5 duplicate in the list": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			list := checkpointSummaryOf(p).CompactedOperations
			list[1] = list[0]
		}},
		"R5 own ID in the list": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			summary := checkpointSummaryOf(p)
			summary.CompactedOperations = append(summary.CompactedOperations, relicCheckpointOp)
		}},
		"R5 empty list": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			checkpointSummaryOf(p).CompactedOperations = []string{}
		}},
		"R5 list entry not a ULID": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			checkpointSummaryOf(p).CompactedOperations[0] = "op-1"
		}},
		// R6: the links are valid against the state.
		"R6 link from a part the relic does not have": {retired, func(_ *RelicOperationPack, r *RelicRecord) {
			r.Links.Links[0].From = relicTestID(7024)
		}},
		// The summary's own members.
		"summary with an unknown boundary": {retired, func(p *RelicOperationPack, r *RelicRecord) {
			checkpointSummaryOf(p).Boundary = "archived"
			r.State.Relic.Compacted.Boundary = "archived"
		}},
		"summary compactedFrom not an object ID": {retired, func(p *RelicOperationPack, r *RelicRecord) {
			checkpointSummaryOf(p).CompactedFrom = "xyz"
			value := "xyz"
			r.State.History.CompactedFrom = &value
		}},
		"summary with no commits": {retired, func(p *RelicOperationPack, _ *RelicRecord) {
			checkpointSummaryOf(p).CompactedCommits = 0
		}},
		"summary with no since": {retired, func(p *RelicOperationPack, r *RelicRecord) {
			checkpointSummaryOf(p).Since = time.Time{}
			r.State.Relic.Compacted.Since = time.Time{}
		}},
	}
	for label, test := range cases {
		pack, stored := relicCheckpointForTest(test.boundary)
		test.change(&pack, &stored)
		err := ValidateRelicCheckpointRoot(pack, stored)
		requireCorruptData(t, label, err)
		if err == nil {
			t.Errorf("%s: ValidateRelicCheckpointRoot accepted it", label)
		}
	}
}

func TestCheckpointOperationShape(t *testing.T) {
	pack, _ := relicCheckpointForTest(RelicBoundaryRetired)
	encoded, err := EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(checkpoint pack) = %v", err)
	}
	want := `"operations":[{"id":"` + relicCheckpointOp + `","type":"history.checkpoint","checkpoint":{"runId":"` +
		relicCheckpointRun + `","compactedFrom":"` + relicCheckpointTip + `","previousGeneration":"` + relicDocGeneration +
		`","boundary":"retired","since":"2026-11-23T10:02:00Z","compactedCommits":4,` +
		`"firstWallTime":"2026-11-23T10:00:00Z","lastWallTime":"2026-11-23T10:02:00Z","compactedOperations":["`
	if !strings.Contains(string(encoded), want) {
		t.Fatalf("checkpoint pack encoding:\n got %s\nwant it to hold %s", encoded, want)
	}
	decoded, err := DecodeRelicOperationPack(encoded)
	if err != nil {
		t.Fatalf("DecodeRelicOperationPack(checkpoint) = %v", err)
	}
	if summary, ok := decoded.Checkpoint(); !ok || len(summary.CompactedOperations) != 7 {
		t.Fatalf("decoded Checkpoint() = %#v, %v", summary, ok)
	}

	// A checkpoint carries only its summary, and the summary is required.
	withTitle := pack
	withTitle.Operations = []RelicOperation{pack.Operations[0]}
	withTitle.Operations[0].Title = "x"
	requireRelicPackRefused(t, "a checkpoint with a title", withTitle)
	bare := pack
	bare.Operations = []RelicOperation{{ID: relicCheckpointOp, Type: RelicOperationCheckpoint}}
	requireRelicPackRefused(t, "a checkpoint without its summary", bare)
	if _, ok := goldenRelicPack().Checkpoint(); ok {
		t.Errorf("the golden create pack reads as a checkpoint")
	}

	_, stored := relicCheckpointForTest(RelicBoundaryRetired)
	encodedState, err := EncodeDocument(stored.State)
	if err != nil {
		t.Fatalf("EncodeDocument(checkpoint state) = %v", err)
	}
	for _, fragment := range []string{
		`"compactedFrom":"` + relicCheckpointTip + `"`,
		`"retired":true,"deleted":false,"compacted":{"runId":"` + relicCheckpointRun +
			`","at":"2026-11-24T10:00:00Z","boundary":"retired","since":"2026-11-23T10:02:00Z"},"createdAt":`,
		`{"id":"` + relicDocPartB + `","type":"decision","position":"2/1","removed":true,` +
			`"dropped":{"removedAt":"2026-11-23T10:01:00Z","droppedAt":"2026-11-24T10:00:00Z"}}`,
	} {
		if !strings.Contains(string(encodedState), fragment) {
			t.Errorf("checkpoint state encoding:\n got %s\nwant it to hold %s", encodedState, fragment)
		}
	}
}

// A state that names a compaction must name the tip it was compacted from, and
// the reverse; a stub needs a compacted relic.
func TestCheckpointStateMembersComeTogether(t *testing.T) {
	_, stored := relicCheckpointForTest(RelicBoundaryRetired)
	noCompacted := stored.State
	noCompacted.Relic = copyRelicData(stored.State.Relic)
	noCompacted.Relic.Compacted = nil
	requireRelicStateRefused(t, "compactedFrom without compacted", noCompacted)

	_, stored = relicCheckpointForTest(RelicBoundaryRetired)
	noFrom := stored.State
	noFrom.History.CompactedFrom = nil
	requireRelicStateRefused(t, "compacted without compactedFrom", noFrom)

	stub := goldenRelicState()
	stub.MinReader = RelicCheckpointMinReader
	stub.Relic.Parts[1].Removed = true
	stub.Relic.Parts[1].Dropped = &RelicPartDrop{RemovedAt: relicDocTime, DroppedAt: relicDocTime}
	requireRelicStateRefused(t, "a stub in a relic never compacted", stub)

	compacted := goldenRelicState()
	compacted.Relic.Compacted = &RelicCompaction{RunID: relicCheckpointRun, At: relicCheckpointWall,
		Boundary: RelicBoundaryRetired, Since: relicCheckpointSince}
	from := relicCheckpointTip
	compacted.History.CompactedFrom = &from
	requireRelicStateRefused(t, "compacted at generation 6", compacted)
}
