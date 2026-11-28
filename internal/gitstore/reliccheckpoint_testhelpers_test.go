package gitstore

import (
	"context"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// relicCheckpointRunAfter is how long after the tip's own wall time
// writeRelicCheckpoint dates its run.
const relicCheckpointRunAfter = time.Hour

// writeRelicCheckpoint compacts the relic tip is the head of into one
// parentless history.checkpoint at boundary, through the real compaction
// steps: CompactedRelicHistory, BuildRelicCheckpoint and the compare-and-swap
// of CompactRelicRef. It fails the test unless the root passes this build's
// own verification, a read and a whole-history validation both.
//
// The run, the generation and the checkpoint's operation ID are derived from
// the tip's clock, so two checkpoints of one relic differ, and the run is
// dated relicCheckpointRunAfter after the tip.
func writeRelicCheckpoint(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	tip core.RelicSnapshot,
	boundary string,
) core.RelicSnapshot {
	t.Helper()
	return writeRelicCheckpointNumbered(t, repo, config, tip, boundary, 0)
}

// writeRelicCheckpointNumbered is writeRelicCheckpoint with offset added to
// the numbers of the run, the generation and the checkpoint's operation, so
// two clones compacting the same tip write two different checkpoints.
func writeRelicCheckpointNumbered(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	tip core.RelicSnapshot,
	boundary string,
	offset int,
) core.RelicSnapshot {
	t.Helper()
	ctx := context.Background()
	relicID := tip.Record.State.RelicID
	ids, commits, first, last, removedAt, since, err := repo.CompactedRelicHistory(ctx, config, tip.Head)
	if err != nil {
		t.Fatalf("writeRelicCheckpoint: CompactedRelicHistory(%s) error = %v", relicID, err)
	}
	clock := int(tip.Record.State.LogicalClock+1) + offset
	written, err := repo.BuildRelicCheckpoint(ctx, config, RelicCheckpointInput{
		RelicID:     relicID,
		Tip:         tip,
		RunID:       relicTestID(980000 + clock),
		Generation:  relicTestID(970000 + clock),
		OperationID: relicTestID(990000 + clock),
		Actor:       tip.Operation.Actor.ID,
		Boundary:    boundary,
		Since:       since[boundary],
		Now:         tip.Operation.WallTime.Add(relicCheckpointRunAfter),
		RemovedAt:   removedAt,
		Summary: core.RelicCheckpointData{
			CompactedCommits: commits, FirstWallTime: first, LastWallTime: last, CompactedOperations: ids,
		},
	})
	if err != nil {
		t.Fatalf("writeRelicCheckpoint: BuildRelicCheckpoint(%s, %s) error = %v", relicID, boundary, err)
	}
	if err := repo.CompactRelicRef(ctx, config, relicID, tip.Head, written.Head); err != nil {
		t.Fatalf("writeRelicCheckpoint: CompactRelicRef(%s) error = %v", relicID, err)
	}
	read, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("writeRelicCheckpoint: GetRelic(%s) of the checkpoint error = %v", relicID, err)
	}
	assertSameRelicSnapshot(t, read, written)
	assertRelicHistoryValid(t, repo, config, relicID, written.Head, 1)
	return written
}
