package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// ErrRelicCheckpointTooLarge is BuildRelicCheckpoint's refusal of a checkpoint
// whose operation pack would be larger than one stored object may be, because
// it lists too many replaced operations. Nothing was written. The relic is
// left as it is, and a compaction run reports it as a skip.
var ErrRelicCheckpointTooLarge = errors.New("the checkpoint would list too many changes for one stored object")

// relicCheckpointPackLimit is the largest encoded checkpoint pack
// BuildRelicCheckpoint writes: the ceiling every reader puts on one object. A
// test lowers it to reach the refusal without a history of that size.
var relicCheckpointPackLimit = MaxObjectBytes

// RelicCheckpointInput is everything one relic's compaction decides before
// its checkpoint is written.
type RelicCheckpointInput struct {
	RelicID string
	// Tip is the head the checkpoint replaces, as read at Tip.Head.
	Tip core.RelicSnapshot
	// RunID names the compaction run.
	RunID string
	// Generation is the new history generation the checkpoint opens.
	Generation string
	// OperationID is the checkpoint operation's own ID.
	OperationID string
	Actor       string
	// Boundary is core.RelicBoundaryRetired or core.RelicBoundaryDeleted, and
	// must be what the tip is.
	Boundary string
	// Since is when the relic last became retired or deleted, by Boundary.
	Since time.Time
	// Now is when the run happens: the checkpoint's wall time, the
	// compaction's date and every new stub's droppedAt.
	Now time.Time
	// RemovedAt is when each removed part's removal last took effect, as
	// CompactedRelicHistory reports it. A part the checkpoint makes a stub
	// that is live at the tip, which only a deleted relic has, takes Since.
	RemovedAt map[string]time.Time
	// Summary carries the counts, times and operation IDs
	// CompactedRelicHistory reports. BuildRelicCheckpoint fills RunID,
	// CompactedFrom, PreviousGeneration, Boundary and Since itself.
	Summary core.RelicCheckpointData
}

// BuildRelicCheckpoint writes the parentless checkpoint commit that replaces
// input.Tip's history, and returns it as a snapshot. It writes objects only
// and moves no ref: putting the relic on it is CompactRelicRef's
// compare-and-swap. input.Tip must be the snapshot read at input.Tip.Head;
// its stored state is checked against the commit before anything is written.
//
// The checkpoint keeps what the boundary keeps (core.ValidateRelicCheckpointRoot
// holds it to that) and rewrites no content: a live part's subtree is the very
// object the tip stores it in, and a stub's is the canonical empty
// fields.json alone. A stub an earlier checkpoint made keeps its dates. The
// commit is read back and verified as a root before it is returned.
//
// A pack over the single-object ceiling is ErrRelicCheckpointTooLarge, with
// nothing written.
func (r *Repository) BuildRelicCheckpoint(ctx context.Context, config core.ProjectConfig, input RelicCheckpointInput) (core.RelicSnapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.requireOwnedRelic(ctx, config, input.RelicID); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.validateFullObjectID(input.Tip.Head); err != nil {
		return core.RelicSnapshot{}, core.Wrap(core.CategoryValidation, "checkpoint tip must be a full Git object ID", err)
	}
	before := input.Tip.Record.State
	if before.RelicID != input.RelicID {
		return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
			"checkpoint tip is relic %s, not %s", before.RelicID, input.RelicID)
	}

	now := input.Now.UTC()
	relic := before.Relic
	relic.Parts = slices.Clone(before.Relic.Parts)
	switch input.Boundary {
	case core.RelicBoundaryRetired:
		if !relic.Retired || relic.Deleted {
			return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
				"relic %s is not retired and live, so it cannot be compacted as retired", input.RelicID)
		}
	case core.RelicBoundaryDeleted:
		if !relic.Deleted {
			return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
				"relic %s is not deleted, so it cannot be compacted as deleted", input.RelicID)
		}
		relic.Properties = map[string]json.RawMessage{}
	default:
		return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation, "checkpoint boundary %q is unsupported", input.Boundary)
	}
	if relic.Properties == nil {
		relic.Properties = map[string]json.RawMessage{}
	}
	stubs := false
	for index, part := range relic.Parts {
		if part.Dropped != nil {
			stubs = true
			continue
		}
		if input.Boundary == core.RelicBoundaryRetired && !part.Removed {
			continue
		}
		removedAt := input.Since
		if part.Removed {
			removedAt = input.RemovedAt[part.ID]
		}
		if removedAt.IsZero() {
			return core.RelicSnapshot{}, core.Errorf(core.CategoryValidation,
				"relic %s part %s has no removal date for its checkpoint", input.RelicID, part.ID)
		}
		part.Removed, part.Body = true, nil
		part.Dropped = &core.RelicPartDrop{RemovedAt: removedAt.UTC(), DroppedAt: now}
		relic.Parts[index] = part
		stubs = true
	}
	relic.Compacted = &core.RelicCompaction{RunID: input.RunID, At: now, Boundary: input.Boundary, Since: input.Since.UTC()}

	compactedFrom := input.Tip.Head
	state := before
	state.MinReader = core.RelicCheckpointMinReader
	state.History = core.History{Generation: input.Generation, CompactedFrom: &compactedFrom}
	state.LogicalClock = before.LogicalClock + 1
	state.Relic = relic

	summary := input.Summary
	summary.RunID = input.RunID
	summary.CompactedFrom = input.Tip.Head
	summary.PreviousGeneration = before.History.Generation
	summary.Boundary = input.Boundary
	summary.Since = input.Since.UTC()
	summary.CompactedOperations = slices.Clone(input.Summary.CompactedOperations)
	pack := core.NewRelicOperationPack(config.ProjectID, input.RelicID, input.Generation, input.Actor, state.LogicalClock, now,
		[]core.RelicOperation{{ID: input.OperationID, Type: core.RelicOperationCheckpoint, Checkpoint: &summary}})
	record := core.RelicRecord{State: state, Links: input.Tip.Record.Links}
	if err := core.ValidateRelicCheckpointRoot(pack, record); err != nil {
		return core.RelicSnapshot{}, err
	}
	encodedPack, err := core.EncodeDocument(pack)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if len(encodedPack) > relicCheckpointPackLimit {
		return core.RelicSnapshot{}, core.Wrap(core.CategoryValidation, fmt.Sprintf(
			"relic %s: its checkpoint would be %d bytes, past the %d-byte limit for one stored object",
			input.RelicID, len(encodedPack), relicCheckpointPackLimit), ErrRelicCheckpointTooLarge)
	}
	encodedState, err := core.EncodeDocument(record.State)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	encodedLinks, err := core.EncodeDocument(record.Links)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := refuseActorMismatch(ctx, pack.Actor.ID); err != nil {
		return core.RelicSnapshot{}, err
	}

	// The tip's own trees, checked against the state it was read with, so
	// every live subtree carried over is the one that state describes.
	inherited, err := r.readRelicParentTrees(ctx, input.Tip, nil)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	blobs := [][]byte{encodedPack, encodedState, encodedLinks}
	if stubs {
		emptyFields, err := core.EncodeDocument(core.NewRelicFields(nil))
		if err != nil {
			return core.RelicSnapshot{}, err
		}
		blobs = append(blobs, emptyFields)
	}
	objectIDs, err := r.hashRelicBlobs(ctx, blobs)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	top := []relicTreeEntry{
		{mode: regularBlobMode, kind: "blob", objectID: objectIDs[0], name: relicOperationPath},
		{mode: regularBlobMode, kind: "blob", objectID: objectIDs[1], name: relicStatePath},
		{mode: regularBlobMode, kind: "blob", objectID: objectIDs[2], name: relicLinksPath},
	}
	if len(relic.Parts) > 0 {
		stubTree := ""
		if stubs {
			trees, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{{
				{mode: regularBlobMode, kind: "blob", objectID: objectIDs[3], name: relicFieldsPath},
			}})
			if err != nil {
				return core.RelicSnapshot{}, err
			}
			stubTree = trees[0]
		}
		entries := make([]relicTreeEntry, 0, len(relic.Parts))
		for _, part := range relic.Parts {
			subtree := inherited.parts[part.ID]
			if part.Dropped != nil {
				subtree = stubTree
			}
			entries = append(entries, relicTreeEntry{mode: mktreeSubtreeMode, kind: "tree", objectID: subtree, name: part.ID})
		}
		trees, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{entries})
		if err != nil {
			return core.RelicSnapshot{}, err
		}
		top = append(top, relicTreeEntry{mode: mktreeSubtreeMode, kind: "tree", objectID: trees[0], name: relicPartsPath})
	}
	trees, err := r.writeRelicTrees(ctx, [][]relicTreeEntry{top})
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	head, err := r.writeCommit(ctx, trees[0], "", "compact relic "+input.RelicID, pack.Actor.ID, "")
	if err != nil {
		return core.RelicSnapshot{}, err
	}

	// What was written is judged as any reader will judge it: read back, and
	// held to the root rules with every live part's fields read.
	written, err := r.readRelicCommit(ctx, config, input.RelicID, head)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if err := r.VerifyRelicTransition(ctx, nil, written, true); err != nil {
		return core.RelicSnapshot{}, err
	}
	return core.RelicSnapshot{Head: head, Operation: pack, Record: record}, nil
}

// requireOwnedRelic refuses an ID that is not one of this project's relics.
func (r *Repository) requireOwnedRelic(ctx context.Context, config core.ProjectConfig, relicID string) error {
	if err := core.ValidateRelicIDShape(relicID); err != nil {
		return err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return err
	}
	if !keys.OwnsRelic(relicID) {
		return core.Errorf(core.CategoryValidation,
			"relic ID %q carries a project key this project does not have; its keys are: %s",
			relicID, core.KeyNameList(keys))
	}
	return nil
}

// CompactRelicRef compare-and-swaps refs/workbook/relics/<id> from expected to
// checkpoint, then expires the ref's reflog. It returns CategoryStaleWrite,
// having changed nothing, when the ref no longer points at expected.
//
// Unlike a task's, a relic's parked tips under refs/workbook/reconciled-relics/
// are never deleted here: they hold text nothing else shows, and a run skips a
// relic that has any. After it returns nil, nothing Workbook keeps in this
// clone reaches the old history, apart from the tracking ref until the
// checkpoint is published.
func (r *Repository) CompactRelicRef(ctx context.Context, config core.ProjectConfig, relicID, expected, checkpoint string) error {
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if err := r.requireOwnedRelic(ctx, config, relicID); err != nil {
		return core.Wrap(core.CategoryValidation, "compacted relic ID is invalid", err)
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return err
	}
	if err := r.validateFullObjectID(expected); err != nil {
		return core.Wrap(core.CategoryValidation, "compaction must name the full tip it replaces", err)
	}
	if err := r.validateFullObjectID(checkpoint); err != nil {
		return core.Wrap(core.CategoryValidation, "checkpoint must be a full Git object ID", err)
	}
	if err := r.moveRelicRef(ctx, relicRefPrefix+relicID, checkpoint, expected, "compact relic "+relicID); err != nil {
		return err
	}
	return r.ExpireRelicReflog(ctx, relicID)
}

// ExpireRelicReflog drops every reflog entry of refs/workbook/relics/<id>, so
// the reflog no longer keeps a compacted history reachable. A ref with no
// reflog has nothing to expire, which makes a retried cleanup a no-op.
func (r *Repository) ExpireRelicReflog(ctx context.Context, relicID string) error {
	if err := core.ValidateRelicIDShape(relicID); err != nil {
		return err
	}
	ref := relicRefPrefix + relicID
	if _, err := r.Git(ctx, nil, "reflog", "exists", ref); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return nil
		}
		return core.Wrap(core.CategoryOperational, "cannot determine whether relic "+relicID+" has a reflog", err)
	}
	if _, err := r.Git(ctx, nil,
		"reflog", "expire", "--expire=now", "--expire-unreachable=now", ref,
	); err != nil {
		return core.Wrap(core.CategoryOperational, "expire the reflog of relic "+relicID, err)
	}
	return nil
}

// TrackingRelicHeads returns, by relic ID, the commit each relic's tracking ref
// names: where this clone last saw origin hold it. A tracking ref whose name
// is not one of this project's relics is left out.
func (r *Repository) TrackingRelicHeads(ctx context.Context, config core.ProjectConfig) (map[string]string, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return nil, err
	}
	prefix := r.remoteRelicRefPrefix()
	records, err := r.relicRefRecords(ctx, prefix)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string, len(records))
	for _, record := range records {
		relicID := strings.TrimPrefix(record.name, prefix)
		if record.symbolicTarget != "" || core.ValidateRelicIDShape(relicID) != nil || !keys.OwnsRelic(relicID) {
			continue
		}
		heads[relicID] = record.objectID
	}
	return heads, nil
}

// ParkedRelicHeads returns, by relic ID, every parked tip under
// refs/workbook/reconciled-relics/: a local tip a replay stopped on, which
// holds a person's unapplied change.
func (r *Repository) ParkedRelicHeads(ctx context.Context, config core.ProjectConfig) (map[string][]string, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	return r.parkedRelicTips(ctx)
}

// RelicCompactionReclaimableBytes estimates the bytes compacting relicID at
// tip lets Git reclaim: the on-disk size of every object reachable from tip and
// from no other ref, less what the checkpoint keeps (the subtrees of the parts
// a retired checkpoint carries over) and less the size of tip's state.json,
// which stands in for the checkpoint itself. It may be negative.
//
// The relic's own ref and its tracking ref are left out of "every other ref":
// the run moves the first, and the second follows once the checkpoint is
// published. Its parked tips are not, because a run never deletes them. It is
// an estimate: Git frees nothing until it prunes.
func (r *Repository) RelicCompactionReclaimableBytes(ctx context.Context, config core.ProjectConfig, relicID, tip string) (int64, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return 0, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return 0, err
	}
	if err := r.requireOwnedRelic(ctx, config, relicID); err != nil {
		return 0, err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return 0, err
	}
	if err := r.validateFullObjectID(tip); err != nil {
		return 0, core.Wrap(core.CategoryValidation, "compaction estimate must name a full tip", err)
	}
	commits, errs := r.readRelicCommitDocuments(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: tip}})
	if errs[0] != nil {
		return 0, errs[0]
	}
	relic := commits[0].Record.State.Relic
	args := []string{"rev-list", "--objects", tip}
	if !relic.Deleted {
		for _, part := range relic.Parts {
			if !part.Removed {
				args = append(args, "^"+tip+":"+relicPartsPath+"/"+part.ID)
			}
		}
	}
	args = append(args, "--not",
		"--exclude="+relicRefPrefix+relicID,
		"--exclude="+r.remoteRelicRefPrefix()+relicID,
		"--all",
	)
	output, err := r.Git(ctx, nil, args...)
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "cannot list the history of relic "+relicID, err)
	}
	usage, err := r.objectDiskUsage(ctx, output)
	if err != nil {
		return 0, err
	}
	output, err = r.Git(ctx, nil, "cat-file", "-s", tip+":"+relicStatePath)
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "cannot measure the state of relic "+relicID, err)
	}
	state, err := parseByteCount(output)
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "Git returned an invalid object size", err)
	}
	return usage - state, nil
}

// CompactedRelicHistory reports what a checkpoint replacing tip would compact:
// every operation ID in tip's history (sorted and unique), the number of
// commits it holds, the wall times of the first and last of them, when each
// part removed at tip was removed, and, by boundary, when the relic last
// became retired and deleted (each present only when tip is).
//
// The dates are taken from what the fold did, not from which operations were
// recorded: the history is folded commit by commit, and a date is the wall
// time of the commit whose fold turned the relic retired or deleted, or the
// part removed, the last time it did. A repeated retire, a removal of a part
// already removed, or any operation the fold ignored moves no date.
//
// It walks tip's first-parent chain once. When the chain is rooted at an
// earlier checkpoint, that checkpoint's list is merged into the answer with
// its own operation ID, its commit count and first wall time carry on (the
// checkpoint itself not being a change), and its stubs' removal dates and its
// since are kept. A retired mark carried by a checkpoint made at the deleted
// boundary is dated by that checkpoint's since, the deletion, which is the
// latest the retirement can have happened.
//
// A history this clone cannot read in full is CategoryCorruptData, or
// CategoryNewerWriter for one a newer Workbook wrote: compaction drops what it
// summarizes, so a summary of part of it would lose the rest.
func (r *Repository) CompactedRelicHistory(
	ctx context.Context,
	config core.ProjectConfig,
	tip string,
) (ids []string, commits int, first, last time.Time, removedAt map[string]time.Time, since map[string]time.Time, err error) {
	fail := func(err error) ([]string, int, time.Time, time.Time, map[string]time.Time, map[string]time.Time, error) {
		return nil, 0, time.Time{}, time.Time{}, nil, nil, err
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return fail(err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return fail(err)
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return fail(err)
	}
	if err := r.validateFullObjectID(tip); err != nil {
		return fail(core.Wrap(core.CategoryValidation, "compacted history tip must be a full Git object ID", err))
	}
	// The tip's own pack names its relic, which the chain read needs.
	output, err := r.Git(ctx, nil, "cat-file", "blob", tip+":"+relicOperationPath)
	if err != nil {
		return fail(core.Wrap(core.CategoryCorruptData, "cannot read the operation of relic commit "+tip, err))
	}
	tipPack, err := core.DecodeRelicOperationPack(output)
	if err != nil {
		return fail(err)
	}
	relicID := tipPack.RelicID
	results, err := r.ReadRelicOperations(ctx, config, []RelicHistoryRequest{{Head: core.RelicHead{RelicID: relicID, ObjectID: tip}}})
	if err != nil {
		return fail(err)
	}
	if len(results) != 1 {
		return fail(core.Errorf(core.CategoryCorruptData, "relic operation read returned %d results, want 1", len(results)))
	}
	result := results[0]
	if result.Truncated != nil {
		category := core.CategoryCorruptData
		if core.CategoryOf(result.Truncated.Err) == core.CategoryNewerWriter {
			category = core.CategoryNewerWriter
		}
		return fail(core.Wrap(category,
			fmt.Sprintf("relic %s history cannot be read in full at commit %s", relicID, result.Truncated.Commit),
			result.Truncated.Err))
	}
	chain := result.Commits
	if len(chain) == 0 || chain[0].Parent != "" || !isRelicRootPack(chain[0].Operation) {
		return fail(core.Errorf(core.CategoryCorruptData,
			"relic %s history does not reach a relic.create or history.checkpoint root", relicID))
	}

	seen := make(map[string]struct{})
	add := func(id string) {
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	removedAt = map[string]time.Time{}
	var retiredAt, deletedAt time.Time
	var state *core.RelicRecord
	first = chain[0].Operation.WallTime
	if previous, ok := chain[0].Operation.Checkpoint(); ok {
		root, err := r.readRelicCommit(ctx, config, relicID, chain[0].ObjectID)
		if err != nil {
			return fail(err)
		}
		if root.needsNewerReader() {
			return fail(newerRelicHistory(relicID))
		}
		for _, id := range previous.CompactedOperations {
			add(id)
		}
		add(chain[0].Operation.Operations[0].ID)
		commits, first, last = previous.CompactedCommits, previous.FirstWallTime, previous.LastWallTime
		relic := root.Record.State.Relic
		if relic.Retired {
			retiredAt = previous.Since
		}
		if relic.Deleted {
			deletedAt = previous.Since
		}
		for _, part := range relic.Parts {
			if part.Dropped != nil {
				removedAt[part.ID] = part.Dropped.RemovedAt
			}
		}
		state = &root.Record
		chain = chain[1:]
	}
	for _, commit := range chain {
		pack := commit.Operation
		next, err := core.ApplyRelic(state, pack)
		if err != nil {
			return fail(namedRelicError(relicID, err))
		}
		var was core.RelicData
		if state != nil {
			was = state.State.Relic
		}
		now := next.State.Relic
		if now.Retired && !was.Retired {
			retiredAt = pack.WallTime
		}
		if now.Deleted && !was.Deleted {
			deletedAt = pack.WallTime
		}
		for _, part := range now.Parts {
			before, existed := was.Part(part.ID)
			switch {
			case !part.Removed:
				delete(removedAt, part.ID)
			case part.Dropped == nil && (!existed || !before.Removed):
				removedAt[part.ID] = pack.WallTime
			}
		}
		for _, operation := range pack.Operations {
			add(operation.ID)
		}
		commits++
		last = pack.WallTime
		state = &next
	}
	slices.Sort(ids)
	since = map[string]time.Time{}
	relic := state.State.Relic
	if relic.Retired && !retiredAt.IsZero() {
		since[core.RelicBoundaryRetired] = retiredAt
	}
	if relic.Deleted && !deletedAt.IsZero() {
		since[core.RelicBoundaryDeleted] = deletedAt
	}
	return ids, commits, first, last, removedAt, since, nil
}

// isRelicRootPack reports whether pack can open a relic's history: one that
// opens with relic.create, or a history.checkpoint alone.
func isRelicRootPack(pack core.RelicOperationPack) bool {
	if len(pack.Operations) == 0 {
		return false
	}
	switch pack.Operations[0].Type {
	case core.RelicOperationCreate:
		return true
	case core.RelicOperationCheckpoint:
		return len(pack.Operations) == 1
	}
	return false
}
