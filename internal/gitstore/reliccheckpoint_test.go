package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicChainOperationIDs is every operation ID on the chain ending at head,
// sorted.
func relicChainOperationIDs(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, head string) []string {
	t.Helper()
	chain, err := repo.RelicChain(context.Background(), config, core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil || chain.Truncated != nil {
		t.Fatalf("RelicChain(%s) = %#v, %v", relicID, chain.Truncated, err)
	}
	var ids []string
	for _, commit := range chain.Commits {
		for _, operation := range commit.Operation.Operations {
			ids = append(ids, operation.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// relicRootWallTime is the wall time of the root of the chain ending at head.
func relicRootWallTime(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, head string) time.Time {
	t.Helper()
	chain, err := repo.RelicChain(context.Background(), config, core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil || len(chain.Commits) == 0 {
		t.Fatalf("RelicChain(%s) = %d commits, %v", relicID, len(chain.Commits), err)
	}
	return chain.Commits[0].Operation.WallTime
}

// relicCommitWallTimes is each commit's wall time on the chain ending at head,
// by the ID of the first operation the commit records.
func relicCommitWallTimes(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, head string) map[string]time.Time {
	t.Helper()
	chain, err := repo.RelicChain(context.Background(), config, core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil {
		t.Fatalf("RelicChain(%s) error = %v", relicID, err)
	}
	walls := map[string]time.Time{}
	for _, commit := range chain.Commits {
		walls[commit.Operation.Operations[0].ID] = commit.Operation.WallTime
	}
	return walls
}

func TestBuildRelicCheckpointRetired(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		relicID := tip.Record.State.RelicID
		walls := relicCommitWallTimes(t, repo, config, relicID, tip.Head)
		base := 710000 + 100

		ids, commits, first, last, removedAt, since, err := repo.CompactedRelicHistory(ctx, config, tip.Head)
		if err != nil {
			t.Fatalf("CompactedRelicHistory() error = %v", err)
		}
		if want := relicChainOperationIDs(t, repo, config, relicID, tip.Head); !slices.Equal(ids, want) {
			t.Fatalf("ids = %q, want every operation of the chain sorted, %q", ids, want)
		}
		if root := relicRootWallTime(t, repo, config, relicID, tip.Head); commits != 5 || !first.Equal(root) ||
			!last.Equal(tip.Operation.WallTime) {
			t.Fatalf("commits, first, last = %d, %s, %s; want 5, %s, %s", commits, first, last, root, tip.Operation.WallTime)
		}
		if got, want := removedAt[relicTestID(2)], walls[relicTestID(base+3)]; !got.Equal(want) || len(removedAt) != 1 {
			t.Fatalf("removedAt = %v, want only part 2 at %s", removedAt, want)
		}
		if got, want := since[core.RelicBoundaryRetired], walls[relicTestID(base+5)]; !got.Equal(want) || len(since) != 1 {
			t.Fatalf("since = %v, want only retired at %s", since, want)
		}

		checkpoint := buildRelicCheckpointOf(t, repo, config, tip, core.RelicBoundaryRetired, 1)
		if got := readRelicTip(t, repo, config, relicID).Head; got != tip.Head {
			t.Fatalf("BuildRelicCheckpoint moved the ref to %s", got)
		}
		if err := repo.CompactRelicRef(ctx, config, relicID, tip.Head, checkpoint.Head); err != nil {
			t.Fatalf("CompactRelicRef() error = %v", err)
		}
		assertSameRelicSnapshot(t, readRelicTip(t, repo, config, relicID), checkpoint)
		assertRelicHistoryValid(t, repo, config, relicID, checkpoint.Head, 1)

		summary, _ := checkpoint.Operation.Checkpoint()
		if !slices.Equal(summary.CompactedOperations, ids) || summary.CompactedFrom != tip.Head ||
			summary.PreviousGeneration != tip.Record.State.History.Generation || summary.CompactedCommits != 5 {
			t.Fatalf("summary = %+v", summary)
		}
		relic := checkpoint.Record.State.Relic
		if relic.Compacted == nil || relic.Compacted.Boundary != core.RelicBoundaryRetired ||
			!relic.Compacted.Since.Equal(since[core.RelicBoundaryRetired]) {
			t.Fatalf("compacted = %+v", relic.Compacted)
		}
		if string(relic.Properties["outcome"]) != `"superseded"` || !relic.UpdatedAt.Equal(tip.Record.State.Relic.UpdatedAt) {
			t.Fatalf("relic = %+v, want the properties and updatedAt kept", relic)
		}
		if got := checkpoint.Record.State.LogicalClock; got != tip.Record.State.LogicalClock+1 {
			t.Fatalf("clock = %d, want the tip's plus one", got)
		}
		live, _ := relic.Part(relicTestID(1))
		stub, _ := relic.Part(relicTestID(2))
		if live.Dropped != nil || live.Removed {
			t.Fatalf("live part = %+v, want it kept", live)
		}
		if got, want := relicSubtree(t, repo, checkpoint.Head, live.ID), relicSubtree(t, repo, tip.Head, live.ID); got != want {
			t.Fatalf("live part subtree = %s, want the tip's %s", got, want)
		}
		if stub.Dropped == nil || !stub.Dropped.RemovedAt.Equal(removedAt[stub.ID]) || stub.Body != nil {
			t.Fatalf("stub = %+v, want removed at %s", stub, removedAt[stub.ID])
		}
		if !slices.Equal(relicLinkIDsAt(t, repo, checkpoint.Head), relicLinkIDsAt(t, repo, tip.Head)) {
			t.Fatalf("links changed")
		}
	})
}

func TestBuildRelicCheckpointDeleted(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 2, true)
		relicID := tip.Record.State.RelicID
		walls := relicCommitWallTimes(t, repo, config, relicID, tip.Head)
		base := 710000 + 200
		deletedAt := walls[relicTestID(base+5)]

		_, _, _, _, removedAt, since, err := repo.CompactedRelicHistory(ctx, config, tip.Head)
		if err != nil {
			t.Fatalf("CompactedRelicHistory() error = %v", err)
		}
		if !since[core.RelicBoundaryDeleted].Equal(deletedAt) {
			t.Fatalf("since = %v, want deleted at %s", since, deletedAt)
		}
		checkpoint := buildRelicCheckpointOf(t, repo, config, tip, core.RelicBoundaryDeleted, 1)
		if err := repo.CompactRelicRef(ctx, config, relicID, tip.Head, checkpoint.Head); err != nil {
			t.Fatalf("CompactRelicRef() error = %v", err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, checkpoint.Head, 1)

		relic := checkpoint.Record.State.Relic
		if len(relic.Properties) != 0 || relic.Title != tip.Record.State.Relic.Title || !relic.Deleted {
			t.Fatalf("relic = %+v, want no properties and the title kept", relic)
		}
		for _, part := range relic.Parts {
			if part.Dropped == nil || !part.Removed {
				t.Fatalf("part %s = %+v, want a stub", part.ID, part)
			}
		}
		// Part 1 was live when the relic was deleted; part 2 had been removed.
		if part, _ := relic.Part(relicTestID(1)); !part.Dropped.RemovedAt.Equal(deletedAt) {
			t.Fatalf("part 1 removedAt = %s, want the deletion's %s", part.Dropped.RemovedAt, deletedAt)
		}
		if part, _ := relic.Part(relicTestID(2)); !part.Dropped.RemovedAt.Equal(removedAt[relicTestID(2)]) {
			t.Fatalf("part 2 removedAt = %s, want %s", part.Dropped.RemovedAt, removedAt[relicTestID(2)])
		}
		if len(checkpoint.Record.Links.Links) != 2 {
			t.Fatalf("links = %+v, want both kept", checkpoint.Record.Links.Links)
		}
	})
}

// The dates come from what the fold applied: a repeated retire, a removal of
// a part already removed, and a retire after an unretire.
func TestCompactedRelicHistoryDatesFromTheFold(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := writeRelicRootNumbered(t, repo, config, 3, relicTestPartNumbered(1), relicTestPartNumbered(2), relicTestPartNumbered(3))
		retire := func(n int) core.RelicOperation {
			return core.RelicOperation{ID: relicTestID(n), Type: core.RelicOperationRetire}
		}
		tip = writeRelicChild(t, repo, config, tip, nil, retire(720001))
		tip = writeRelicChild(t, repo, config, tip, nil, retire(720002))
		tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(720003, core.RelicOperationPartRemove, relicTestID(1)))
		removed1 := tip.Operation.WallTime
		tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(720004, core.RelicOperationPartRemove, relicTestID(1)))
		tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(720005, core.RelicOperationPartRemove, relicTestID(2)))
		tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(720006, core.RelicOperationPartRestore, relicTestID(2)))
		tip = writeRelicChild(t, repo, config, tip, nil,
			core.RelicOperation{ID: relicTestID(720007), Type: core.RelicOperationUnretire})
		tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(720008, core.RelicOperationPartRemove, relicTestID(2)))
		removed2 := tip.Operation.WallTime
		tip = writeRelicChild(t, repo, config, tip, nil, retire(720009))
		retiredAt := tip.Operation.WallTime
		tip = writeRelicChild(t, repo, config, tip, nil, retire(720010))

		_, commits, _, _, removedAt, since, err := repo.CompactedRelicHistory(ctx, config, tip.Head)
		if err != nil {
			t.Fatalf("CompactedRelicHistory() error = %v", err)
		}
		if commits != 11 {
			t.Fatalf("commits = %d, want 11", commits)
		}
		if !since[core.RelicBoundaryRetired].Equal(retiredAt) {
			t.Fatalf("since = %v, want retired at %s", since, retiredAt)
		}
		if _, deleted := since[core.RelicBoundaryDeleted]; deleted {
			t.Fatalf("since = %v, want no deletion date for a live relic", since)
		}
		want := map[string]time.Time{relicTestID(1): removed1, relicTestID(2): removed2}
		if !maps.EqualFunc(removedAt, want, time.Time.Equal) {
			t.Fatalf("removedAt = %v, want %v", removedAt, want)
		}
	})
}

// A second compaction lists the first's operations, its checkpoint and every
// change since, and the stubs the first made keep their dates.
func TestRecompactionListsBothRuns(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 4, false)
		relicID := tip.Record.State.RelicID
		before := relicChainOperationIDs(t, repo, config, relicID, tip.Head)
		first := relicRootWallTime(t, repo, config, relicID, tip.Head)
		earlier := buildRelicCheckpointOf(t, repo, config, tip, core.RelicBoundaryRetired, 1)
		if err := repo.CompactRelicRef(ctx, config, relicID, tip.Head, earlier.Head); err != nil {
			t.Fatalf("CompactRelicRef() error = %v", err)
		}
		earlierStub, _ := earlier.Record.State.Relic.Part(relicTestID(2))

		edited := writeRelicChild(t, repo, config, earlier, nil, relicPropertySet(730001, "outcome", "kept"))
		deleted := writeRelicChild(t, repo, config, edited, nil,
			core.RelicOperation{ID: relicTestID(730002), Type: core.RelicOperationTombstone})

		ids, commits, again, last, removedAt, since, err := repo.CompactedRelicHistory(ctx, config, deleted.Head)
		if err != nil {
			t.Fatalf("CompactedRelicHistory() error = %v", err)
		}
		earlierID := earlier.Operation.Operations[0].ID
		want := slices.Sorted(slices.Values(append(slices.Clone(before), earlierID, relicTestID(730001), relicTestID(730002))))
		if !slices.Equal(ids, want) {
			t.Fatalf("ids = %q, want %q", ids, want)
		}
		if commits != 7 || !again.Equal(first) || !last.Equal(deleted.Operation.WallTime) {
			t.Fatalf("commits, first, last = %d, %s, %s; want 7, %s, %s", commits, again, last, first, deleted.Operation.WallTime)
		}
		if !removedAt[relicTestID(2)].Equal(earlierStub.Dropped.RemovedAt) {
			t.Fatalf("removedAt = %v, want the stub's %s", removedAt, earlierStub.Dropped.RemovedAt)
		}
		if !since[core.RelicBoundaryDeleted].Equal(deleted.Operation.WallTime) {
			t.Fatalf("since = %v", since)
		}

		later := buildRelicCheckpointOf(t, repo, config, deleted, core.RelicBoundaryDeleted, 2)
		if err := repo.CompactRelicRef(ctx, config, relicID, deleted.Head, later.Head); err != nil {
			t.Fatalf("CompactRelicRef() error = %v", err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, later.Head, 1)
		summary, _ := later.Operation.Checkpoint()
		if slices.Contains(summary.CompactedOperations, later.Operation.Operations[0].ID) {
			t.Fatalf("the list holds the new checkpoint's own ID")
		}
		if !slices.Equal(summary.CompactedOperations, want) {
			t.Fatalf("list = %q, want %q", summary.CompactedOperations, want)
		}
		stub, _ := later.Record.State.Relic.Part(relicTestID(2))
		if *stub.Dropped != *earlierStub.Dropped {
			t.Fatalf("stub = %+v, want the dates it had, %+v", stub.Dropped, earlierStub.Dropped)
		}
		if part, _ := later.Record.State.Relic.Part(relicTestID(1)); !part.Dropped.RemovedAt.Equal(deleted.Operation.WallTime) {
			t.Fatalf("part 1 = %+v, want removed at the deletion", part.Dropped)
		}
	})
}

// Not parallel: it lowers relicCheckpointPackLimit, which every build reads.
func TestRelicCheckpointTooLarge(t *testing.T) {
	limit := relicCheckpointPackLimit
	relicCheckpointPackLimit = 512
	t.Cleanup(func() { relicCheckpointPackLimit = limit })
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			repo, config := relicRepository(t, objectFormat)
			tip := relicToCompact(t, repo, config, 1, false)
			_, err := repo.BuildRelicCheckpoint(context.Background(), config,
				relicCheckpointInputOf(t, repo, config, tip, core.RelicBoundaryRetired, 1))
			if !errors.Is(err, ErrRelicCheckpointTooLarge) {
				t.Fatalf("BuildRelicCheckpoint() error = %v, want ErrRelicCheckpointTooLarge", err)
			}
		})
	}
}

func TestBuildRelicCheckpointRefusesTheWrongBoundary(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		live := writeRelicRootNumbered(t, repo, config, 5, relicTestPartNumbered(1))
		input := relicCheckpointInputOf(t, repo, config, live, core.RelicBoundaryRetired, 1)
		if _, err := repo.BuildRelicCheckpoint(context.Background(), config, input); core.CategoryOf(err) != core.CategoryValidation {
			t.Fatalf("BuildRelicCheckpoint(live relic) error = %v, want a validation error", err)
		}
	})
}

func TestCompactRelicRefIsACompareAndSwap(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		relicID := tip.Record.State.RelicID
		checkpoint := buildRelicCheckpointOf(t, repo, config, tip, core.RelicBoundaryRetired, 1)
		moved := writeRelicChild(t, repo, config, tip, nil, relicPropertySet(740001, "outcome", "moved"))
		err := repo.CompactRelicRef(ctx, config, relicID, tip.Head, checkpoint.Head)
		if core.CategoryOf(err) != core.CategoryStaleWrite {
			t.Fatalf("CompactRelicRef() after a move error = %v, want a stale write", err)
		}
		if got := readRelicTip(t, repo, config, relicID).Head; got != moved.Head {
			t.Fatalf("ref = %s, want it left at %s", got, moved.Head)
		}
		// A parked tip is never the compaction's to delete.
		parked := relicParkedRefPrefix + relicID + "/0"
		gitOutput(t, repo, "update-ref", parked, tip.Head)
		checkpoint = buildRelicCheckpointOf(t, repo, config, moved, core.RelicBoundaryRetired, 2)
		if err := repo.CompactRelicRef(ctx, config, relicID, moved.Head, checkpoint.Head); err != nil {
			t.Fatalf("CompactRelicRef() error = %v", err)
		}
		if got := gitOutput(t, repo, "rev-parse", parked); got != tip.Head {
			t.Fatalf("parked tip = %q, want it kept at %s", got, tip.Head)
		}
		if reflog := gitOutput(t, repo, "reflog", "show", "--format=%H", relicRef(relicID)); strings.Contains(reflog, moved.Head) {
			t.Fatalf("reflog %q still names the old tip", reflog)
		}
		parkedTips, err := repo.ParkedRelicHeads(ctx, config)
		if err != nil || !slices.Equal(parkedTips[relicID], []string{tip.Head}) {
			t.Fatalf("ParkedRelicHeads() = %v, %v", parkedTips, err)
		}
	})
}

func TestTrackingRelicHeadsAndReclaimableBytes(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		relicID := tip.Record.State.RelicID
		heads, err := repo.TrackingRelicHeads(ctx, config)
		if err != nil || len(heads) != 0 {
			t.Fatalf("TrackingRelicHeads() = %v, %v; want none", heads, err)
		}
		gitOutput(t, repo, "update-ref", repo.remoteRelicRefPrefix()+relicID, tip.Head)
		if heads, err = repo.TrackingRelicHeads(ctx, config); err != nil || heads[relicID] != tip.Head {
			t.Fatalf("TrackingRelicHeads() = %v, %v; want %s", heads, err, tip.Head)
		}
		reclaimable, err := repo.RelicCompactionReclaimableBytes(ctx, config, relicID, tip.Head)
		if err != nil || reclaimable <= 0 {
			t.Fatalf("RelicCompactionReclaimableBytes() = %d, %v; want some", reclaimable, err)
		}
		// Another ref holding the whole history leaves nothing to reclaim but
		// the state the checkpoint stands in for.
		gitOutput(t, repo, "update-ref", "refs/heads/keep", tip.Head)
		held, err := repo.RelicCompactionReclaimableBytes(ctx, config, relicID, tip.Head)
		if err != nil || held >= 0 {
			t.Fatalf("RelicCompactionReclaimableBytes() with the history held = %d, %v; want below zero", held, err)
		}
	})
}

func TestRelicBundleRestoresOldHistory(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repo, config := relicRepository(t, objectFormat)
			ctx := context.Background()
			retired := relicToCompact(t, repo, config, 1, false)
			deleted := relicToCompact(t, repo, config, 2, true)
			manifest := ArchiveManifest{
				ProjectID: config.ProjectID,
				RunID:     relicTestID(990001),
				CreatedAt: relicTestTime,
				Flags:     []string{"--relics"},
				Relics: []ArchiveRelic{
					relicArchiveEntry(retired, 5),
					relicArchiveEntry(deleted, 5),
				},
			}
			path := filepath.Join(t.TempDir(), "relics.bundle")
			if err := repo.WriteRelicArchiveBundle(ctx, config, path, manifest); err != nil {
				t.Fatalf("WriteRelicArchiveBundle() error = %v", err)
			}
			assertNoArchiveManifestRef(t, repo)

			fresh := testrepo.New(t, testrepo.WithObjectFormat(objectFormat))
			syncGit(t, fresh, "fetch", "--quiet", path, "refs/workbook/*:refs/workbook/*")
			for _, tip := range []core.RelicSnapshot{retired, deleted} {
				relicID := tip.Record.State.RelicID
				if got := strings.TrimSpace(syncGit(t, fresh, "rev-parse", relicRef(relicID))); got != tip.Head {
					t.Fatalf("bundle holds %s at %s, want %s", relicID, got, tip.Head)
				}
			}
			stored := syncGit(t, fresh, "show", archiveManifestRef+":"+archiveManifestFile)
			var read struct {
				Tasks  []ArchiveTask  `json:"tasks"`
				Relics []ArchiveRelic `json:"relics"`
			}
			if err := json.Unmarshal([]byte(stored), &read); err != nil {
				t.Fatalf("manifest %q: %v", stored, err)
			}
			if len(read.Tasks) != 0 || !slices.Equal(read.Relics, manifest.Relics) {
				t.Fatalf("manifest = %+v, want relics %+v", read, manifest.Relics)
			}
			if !strings.Contains(stored, `"relicId": "`+retired.Record.State.RelicID+`"`) {
				t.Fatalf("manifest %q does not name relicId", stored)
			}
		})
	}
}

func TestRelicBundleDetectsMovedRelic(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		writeRelicChild(t, repo, config, tip, nil, relicPropertySet(740001, "outcome", "moved"))
		manifest := ArchiveManifest{
			ProjectID: config.ProjectID, RunID: relicTestID(990002), CreatedAt: relicTestTime,
			Relics: []ArchiveRelic{relicArchiveEntry(tip, 5)},
		}
		path := filepath.Join(t.TempDir(), "moved.bundle")
		err := repo.WriteRelicArchiveBundle(ctx, config, path, manifest)
		var moved *ArchiveTasksMovedError
		if !errors.As(err, &moved) || !slices.Equal(moved.RelicIDs, []string{tip.Record.State.RelicID}) || len(moved.TaskIDs) != 0 {
			t.Fatalf("WriteRelicArchiveBundle() error = %v, want the moved relic named", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a bundle was left at %s", path)
		}
		// A task archive never carries relics.
		if err := repo.WriteArchiveBundle(ctx, config, path, manifest); core.CategoryOf(err) != core.CategoryValidation {
			t.Fatalf("WriteArchiveBundle(relics) error = %v, want a validation error", err)
		}
	})
}

func relicArchiveEntry(tip core.RelicSnapshot, commits int) ArchiveRelic {
	return ArchiveRelic{
		RelicID:       tip.Record.State.RelicID,
		Title:         tip.Record.State.Relic.Title,
		OldTip:        tip.Head,
		OldGeneration: tip.Record.State.History.Generation,
		NewGeneration: relicTestID(960000 + int(tip.Record.State.LogicalClock)),
		Commits:       commits,
	}
}

// relicCheckpointInputOf is the input a compaction run gives the builder for
// tip, with IDs from run.
func relicCheckpointInputOf(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	tip core.RelicSnapshot,
	boundary string,
	run int,
) RelicCheckpointInput {
	t.Helper()
	ids, commits, first, last, removedAt, since, err := repo.CompactedRelicHistory(context.Background(), config, tip.Head)
	if err != nil {
		t.Fatalf("CompactedRelicHistory() error = %v", err)
	}
	return RelicCheckpointInput{
		RelicID:     tip.Record.State.RelicID,
		Tip:         tip,
		RunID:       relicTestID(981000 + run),
		Generation:  relicTestID(971000 + run),
		OperationID: relicTestID(991000 + run),
		Actor:       relicTestActor,
		Boundary:    boundary,
		Since:       since[boundary],
		Now:         tip.Operation.WallTime.Add(relicCheckpointRunAfter),
		RemovedAt:   removedAt,
		Summary: core.RelicCheckpointData{
			CompactedCommits: commits, FirstWallTime: first, LastWallTime: last, CompactedOperations: ids,
		},
	}
}

func buildRelicCheckpointOf(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	tip core.RelicSnapshot,
	boundary string,
	run int,
) core.RelicSnapshot {
	t.Helper()
	checkpoint, err := repo.BuildRelicCheckpoint(context.Background(), config, relicCheckpointInputOf(t, repo, config, tip, boundary, run))
	if err != nil {
		t.Fatalf("BuildRelicCheckpoint() error = %v", err)
	}
	return checkpoint
}
