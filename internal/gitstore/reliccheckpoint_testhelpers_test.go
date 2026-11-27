package gitstore

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// relicCheckpointRunAfter is how long after the tip's own wall time
// writeRelicCheckpoint dates its run.
const relicCheckpointRunAfter = time.Hour

// writeRelicCheckpoint compacts the relic tip is the head of into one
// parentless history.checkpoint at boundary, written with Git plumbing as the
// real compaction will write it, and moves the relic's ref from tip to it with
// a compare-and-swap. It is for tests that need a compacted relic before the
// real checkpoint builder exists, and it fails the test unless the root it
// wrote passes this build's own verification, a read and a whole-history
// validation both.
//
// What it keeps follows the plan's boundaries: at retired, every live part's
// subtree object and the properties; at deleted, no properties. Removed parts
// (all parts, at deleted) become stubs whose subtree is the empty fields.json,
// and a stub kept from an earlier checkpoint keeps its dates. The run, the
// generation and the checkpoint's operation ID are derived from the tip's
// clock, so two checkpoints of one relic differ.
func writeRelicCheckpoint(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	tip core.RelicSnapshot,
	boundary string,
) core.RelicSnapshot {
	t.Helper()
	ctx := context.Background()
	relicID := tip.Record.State.RelicID
	chain, err := repo.RelicChain(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: tip.Head})
	if err != nil {
		t.Fatalf("writeRelicCheckpoint: RelicChain(%s) error = %v", relicID, err)
	}
	if chain.Truncated != nil || len(chain.Commits) == 0 {
		t.Fatalf("writeRelicCheckpoint: the history of %s cannot be read in full: %#v", relicID, chain.Truncated)
	}

	// Walk the history for what the summary and the stubs record. A chain
	// rooted at an earlier checkpoint starts from what that root kept.
	history := relicCheckpointHistoryOf(t, repo, config, relicID, chain)

	before := tip.Record.State
	wall := tip.Operation.WallTime.Add(relicCheckpointRunAfter)
	clock := before.LogicalClock + 1
	generation := relicTestID(970000 + int(clock))
	run := relicTestID(980000 + int(clock))
	operationID := relicTestID(990000 + int(clock))

	relic := before.Relic
	relic.Parts = slices.Clone(before.Relic.Parts)
	var since time.Time
	switch boundary {
	case core.RelicBoundaryRetired:
		if !relic.Retired || relic.Deleted {
			t.Fatalf("writeRelicCheckpoint: relic %s is not retired and live, so it cannot be compacted at %s",
				relicID, boundary)
		}
		since = history.retiredAt
	case core.RelicBoundaryDeleted:
		if !relic.Deleted {
			t.Fatalf("writeRelicCheckpoint: relic %s is not deleted, so it cannot be compacted at %s", relicID, boundary)
		}
		since = history.deletedAt
		relic.Properties = map[string]json.RawMessage{}
	default:
		t.Fatalf("writeRelicCheckpoint: boundary %q is unsupported", boundary)
	}
	for index, part := range relic.Parts {
		if part.Dropped != nil || (boundary == core.RelicBoundaryRetired && !part.Removed) {
			continue
		}
		removedAt := since
		if part.Removed {
			removedAt = history.removedAt[part.ID]
		}
		if removedAt.IsZero() {
			t.Fatalf("writeRelicCheckpoint: no commit of %s removes part %s", relicID, part.ID)
		}
		part.Removed, part.Body = true, nil
		part.Dropped = &core.RelicPartDrop{RemovedAt: removedAt, DroppedAt: wall}
		relic.Parts[index] = part
	}
	relic.Compacted = &core.RelicCompaction{RunID: run, At: wall, Boundary: boundary, Since: since}

	compactedFrom := tip.Head
	state := before
	state.MinReader = core.RelicCheckpointMinReader
	state.History = core.History{Generation: generation, CompactedFrom: &compactedFrom}
	state.LogicalClock = clock
	state.Relic = relic

	compacted := slices.Sorted(maps.Keys(history.operations))
	pack := core.NewRelicOperationPack(config.ProjectID, relicID, generation, tip.Operation.Actor.ID, clock, wall,
		[]core.RelicOperation{{ID: operationID, Type: core.RelicOperationCheckpoint, Checkpoint: &core.RelicCheckpointData{
			RunID:               run,
			CompactedFrom:       tip.Head,
			PreviousGeneration:  before.History.Generation,
			Boundary:            boundary,
			Since:               since,
			CompactedCommits:    history.commits,
			FirstWallTime:       history.firstWallTime,
			LastWallTime:        tip.Operation.WallTime,
			CompactedOperations: compacted,
		}}})
	record := core.RelicRecord{State: state, Links: tip.Record.Links}

	// The tree: live parts keep the subtree object the tip stores them in, and
	// every stub holds only the empty fields.json.
	entries := []string{
		"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, pack)) + "\toperation.json",
		"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, record.State)) + "\tstate.json",
		"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, record.Links)) + "\tlinks.json",
	}
	if len(relic.Parts) > 0 {
		kept := map[string]string{}
		for _, line := range relicLines(gitOutput(t, repo, "ls-tree", tip.Head+":parts")) {
			fields := strings.Fields(line)
			kept[fields[len(fields)-1]] = fields[2]
		}
		stub := forgeRelicTree(t, repo, []string{
			"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, core.NewRelicFields(nil))) + "\tfields.json",
		})
		subtrees := make([]string, 0, len(relic.Parts))
		for _, part := range relic.Parts {
			subtree := kept[part.ID]
			if part.Dropped != nil {
				subtree = stub
			}
			subtrees = append(subtrees, "040000 tree "+subtree+"\t"+part.ID)
		}
		entries = append(entries, "040000 tree "+forgeRelicTree(t, repo, subtrees)+"\tparts")
	}
	head := forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries))
	gitOutput(t, repo, "update-ref", relicRef(relicID), head, tip.Head)

	written := core.RelicSnapshot{Head: head, Operation: pack, Record: record}
	read, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("writeRelicCheckpoint: GetRelic(%s) of the checkpoint error = %v", relicID, err)
	}
	assertSameRelicSnapshot(t, read, written)
	assertRelicHistoryValid(t, repo, config, relicID, head, 1)
	return written
}

// relicCheckpointHistory is what writeRelicCheckpoint learns from the history
// it replaces.
type relicCheckpointHistory struct {
	// operations is every operation ID the checkpoint lists.
	operations map[string]struct{}
	commits    int
	// firstWallTime is the first commit's wall time, or the first one an
	// earlier checkpoint recorded.
	firstWallTime time.Time
	// retiredAt and deletedAt are when the relic last became retired and
	// deleted; removedAt is when each part removed at the tip was removed.
	retiredAt, deletedAt time.Time
	removedAt            map[string]time.Time
}

func relicCheckpointHistoryOf(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	relicID string,
	chain core.RelicChain,
) relicCheckpointHistory {
	t.Helper()
	history := relicCheckpointHistory{
		operations:    map[string]struct{}{},
		commits:       len(chain.Commits),
		firstWallTime: chain.Commits[0].Operation.WallTime,
		removedAt:     map[string]time.Time{},
	}
	var retired, deleted bool
	removed := map[string]bool{}
	if summary, ok := chain.Commits[0].Operation.Checkpoint(); ok {
		root, err := repo.ReadRelicCommit(context.Background(), config, relicID, chain.Commits[0].Commit)
		if err != nil {
			t.Fatalf("writeRelicCheckpoint: ReadRelicCommit(%s) error = %v", chain.Commits[0].Commit, err)
		}
		relic := root.Record.State.Relic
		for _, id := range summary.CompactedOperations {
			history.operations[id] = struct{}{}
		}
		history.commits += summary.CompactedCommits
		history.firstWallTime = summary.FirstWallTime
		retired, deleted = relic.Retired, relic.Deleted
		history.retiredAt, history.deletedAt = summary.Since, summary.Since
		for _, part := range relic.Parts {
			removed[part.ID] = part.Removed
			if part.Dropped != nil {
				history.removedAt[part.ID] = part.Dropped.RemovedAt
			}
		}
	}
	for _, commit := range chain.Commits {
		pack := commit.Operation
		for _, operation := range pack.Operations {
			history.operations[operation.ID] = struct{}{}
			switch operation.Type {
			case core.RelicOperationRetire:
				if !retired {
					retired, history.retiredAt = true, pack.WallTime
				}
			case core.RelicOperationUnretire:
				retired = false
			case core.RelicOperationTombstone:
				if !deleted {
					deleted, history.deletedAt = true, pack.WallTime
				}
			case core.RelicOperationRestore:
				deleted = false
			case core.RelicOperationPartRemove:
				if !removed[operation.Part] {
					removed[operation.Part], history.removedAt[operation.Part] = true, pack.WallTime
				}
			case core.RelicOperationPartRestore:
				removed[operation.Part] = false
			}
		}
	}
	return history
}
