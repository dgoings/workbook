package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// CheckpointInput is everything one task's compaction decides before its
// checkpoint is written.
type CheckpointInput struct {
	TaskID string
	// Tip is the head the checkpoint replaces.
	Tip core.Snapshot
	// RunID names the compaction run, and is what a dropped file's tombstone
	// records.
	RunID string
	// Generation is the new history generation the checkpoint opens.
	Generation string
	// OperationID is the checkpoint operation's own ID.
	OperationID string
	Actor       string
	Now         time.Time
	// KeepAttachments carries every live file's bytes into the checkpoint tree
	// instead of dropping them.
	KeepAttachments bool
	// Summary is filled by the caller with the counts, times and operation IDs
	// CompactedHistory reports, and with when the task became Done or Deleted.
	// BuildCheckpoint fills CompactedFrom, PreviousGeneration, Attachments and
	// RunID itself, from the tip and the rest of the input.
	Summary core.CheckpointData
}

// BuildCheckpoint writes the parentless checkpoint commit that replaces
// input.Tip's history, and returns it as a snapshot. It writes objects only and
// moves no ref: publishing the checkpoint is the caller's compare-and-swap.
// input.Tip must be the snapshot read at input.Tip.Head; the checkpoint trusts
// its state and does not reread it.
//
// The checkpoint carries the tip's task unchanged except for its file
// attachments. By default each live file is marked dropped by this run and its
// bytes are left behind; with KeepAttachments each live file's existing blob is
// put in the checkpoint tree, and a blob this clone does not hold is a
// not-found error, because a checkpoint must never claim bytes it cannot
// reach. A file an earlier run dropped stays dropped either way.
func (r *Repository) BuildCheckpoint(ctx context.Context, config core.ProjectConfig, input CheckpointInput) (core.Snapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.validateFullObjectID(input.Tip.Head); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "checkpoint tip must be a full Git object ID", err)
	}
	if input.Tip.State.TaskID != input.TaskID {
		return core.Snapshot{}, core.Errorf(core.CategoryValidation,
			"checkpoint tip is task %s, not %s", input.Tip.State.TaskID, input.TaskID)
	}

	// The drop time is stored in UTC, so the date every surface prints from it
	// is the same whether it reads the first ten characters or formats it.
	now := input.Now.UTC()
	// The task is the tip's own canonical value. Copying it field by field
	// would turn its empty lists into nil ones, which the checkpoint's
	// canonical check refuses.
	task := input.Tip.State.Task
	attachments := core.CheckpointAttachmentsKept
	if !input.KeepAttachments {
		attachments = core.CheckpointAttachmentsDropped
		_, _, task.Attachments = core.DropFileAttachments(task.Attachments, input.RunID, now)
	}

	summary := input.Summary
	summary.RunID = input.RunID
	summary.CompactedFrom = input.Tip.Head
	summary.PreviousGeneration = input.Tip.State.History.Generation
	summary.Attachments = attachments
	summary.CompactedOperations = append([]string(nil), input.Summary.CompactedOperations...)

	pack := core.NewOperationPack(
		input.Tip.State.ProjectID, input.TaskID, input.Generation, input.Actor,
		input.Tip.State.LogicalClock+1, now,
		[]core.Operation{{
			ID:         input.OperationID,
			Type:       core.OperationHistoryCheckpoint,
			Task:       &task,
			Checkpoint: &summary,
		}},
	)
	state, err := core.Apply(nil, pack)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := core.ValidateCheckpoint(nil, pack, state); err != nil {
		return core.Snapshot{}, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := validateWriteIdentity(config, keys, pack, state); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.requireKeptAttachmentBlobs(ctx, input.TaskID, task.Attachments); err != nil {
		return core.Snapshot{}, err
	}

	head, err := r.writeTaskObjects(ctx, "", pack, state, "compact")
	if err != nil {
		return core.Snapshot{}, err
	}
	return core.Snapshot{Head: head, Operation: pack, State: state}, nil
}

// CompactTaskRef compare-and-swaps refs/workbook/tasks/<id> from expected to
// checkpoint, deletes every refs/workbook/reconciled/<id>/* in the same
// update-ref transaction, then expires the task ref's reflog. Returns
// CategoryStaleWrite when the ref moved, and then changes nothing.
//
// After it returns nil, nothing Workbook keeps in this clone reaches the old
// history, so `git gc` can prune it once it is older than gc.pruneExpire.
func (r *Repository) CompactTaskRef(ctx context.Context, config core.ProjectConfig, taskID, expected, checkpoint string) error {
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return err
	}
	if err := keys.RequireOwned(taskID); err != nil {
		return core.Wrap(core.CategoryValidation, "compacted task ID is invalid", err)
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

	ref := taskRefPrefix + taskID
	if err := r.rejectSymbolicTaskRef(ctx, ref); err != nil {
		return err
	}
	parked, err := r.listParkedRefs(ctx, config, reconciledRefPrefix+taskID+"/")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(parked))
	for _, entry := range parked {
		if !validParkedRefName(taskID, entry.name) {
			return core.Errorf(core.CategoryCorruptData, "parked ref %q does not belong to task %s", entry.name, taskID)
		}
		names = append(names, entry.name)
	}
	if err := r.commitTaskRefUpdate(ctx, ref, checkpoint, expected, names, "compact"); err != nil {
		return err
	}
	return r.ExpireTaskReflog(ctx, taskID)
}

// ExpireTaskReflog drops every reflog entry of refs/workbook/tasks/<id>, so the
// reflog no longer keeps a compacted history reachable. A ref with no reflog
// has nothing to expire, which makes a retried cleanup a no-op.
func (r *Repository) ExpireTaskReflog(ctx context.Context, taskID string) error {
	if err := core.ValidateTaskIDShape(taskID); err != nil {
		return err
	}
	ref := taskRefPrefix + taskID
	if _, err := r.Git(ctx, nil, "reflog", "exists", ref); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return nil
		}
		return core.Wrap(core.CategoryOperational, "cannot determine whether task "+taskID+" has a reflog", err)
	}
	if _, err := r.Git(ctx, nil,
		"reflog", "expire", "--expire=now", "--expire-unreachable=now", ref,
	); err != nil {
		return core.Wrap(core.CategoryOperational, "expire the reflog of task "+taskID, err)
	}
	return nil
}

// requireKeptAttachmentBlobs proves every file a checkpoint keeps is a blob
// this clone holds, with one object probe. Git's mktree would refuse a missing
// object too, but only as an operational failure that names neither the task
// nor the attachment.
func (r *Repository) requireKeptAttachmentBlobs(ctx context.Context, taskID string, attachments []core.Attachment) error {
	kept := keptFileAttachments(attachments)
	if len(kept) == 0 {
		return nil
	}
	var input bytes.Buffer
	for _, attachment := range kept {
		if err := r.validateFullObjectID(attachment.Blob); err != nil {
			return core.Wrap(core.CategoryCorruptData,
				fmt.Sprintf("task %s attachment %s has an invalid blob ID", taskID, attachment.ID), err)
		}
		fmt.Fprintln(&input, attachment.Blob)
	}
	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot probe kept attachment blobs", err)
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return core.Errorf(core.CategoryOperational, "Git returned an unterminated object probe")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != len(kept) {
		return core.Errorf(core.CategoryOperational, "Git probed %d objects, want %d", len(lines), len(kept))
	}
	for index, line := range lines {
		attachment := kept[index]
		fields := bytes.Fields(line)
		switch {
		case len(fields) == 2 && string(fields[0]) == attachment.Blob && string(fields[1]) == "missing":
			return core.Errorf(core.CategoryNotFound,
				"task %s attachment %s: blob %s is not in this clone, so its bytes cannot be kept",
				taskID, attachment.ID, attachment.Blob)
		case len(fields) == 3 && string(fields[0]) == attachment.Blob && string(fields[1]) == "blob":
			continue
		case len(fields) == 3 && string(fields[0]) == attachment.Blob:
			return core.Errorf(core.CategoryCorruptData,
				"task %s attachment %s names object %s, which is not a blob", taskID, attachment.ID, attachment.Blob)
		default:
			return core.Errorf(core.CategoryOperational, "Git returned an unexpected object probe line %q", line)
		}
	}
	return nil
}

// CompactedHistory reports what a checkpoint replacing tip would compact: every
// operation ID in tip's history (sorted and unique), the number of commits it
// holds, and the wall times of the first and last of them.
//
// It walks tip's chain to its root. When that root is itself a checkpoint, the
// history it replaced is known only from its summary, so the answer carries on
// from it: the IDs are its CompactedOperations together with the checkpoint's
// own operation and every operation since; the count is its CompactedCommits
// plus the commits since, the checkpoint itself not being a change; and the
// span starts at its FirstWallTime. Listing the old checkpoint's operation is
// what lets a clone whose chain is rooted at it find every one of its
// operations in the new list.
func (r *Repository) CompactedHistory(
	ctx context.Context,
	config core.ProjectConfig,
	tip string,
) (ids []string, commits int, first, last time.Time, err error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, 0, time.Time{}, time.Time{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, 0, time.Time{}, time.Time{}, err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, 0, time.Time{}, time.Time{}, err
	}
	if err := r.validateFullObjectID(tip); err != nil {
		return nil, 0, time.Time{}, time.Time{}, core.Wrap(
			core.CategoryValidation, "compacted history tip must be a full Git object ID", err)
	}
	// The tip's own pack names its task, which the chain read needs.
	tipPacks, err := r.readOperationPacks(ctx, []TaskHead{{ObjectID: tip}})
	if err != nil {
		return nil, 0, time.Time{}, time.Time{}, err
	}
	if tipPacks[0].err != nil {
		return nil, 0, time.Time{}, time.Time{}, tipPacks[0].err
	}
	taskID := tipPacks[0].pack.TaskID

	results, err := r.ReadTaskOperations(ctx, config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: taskID, ObjectID: tip}},
	})
	if err != nil {
		return nil, 0, time.Time{}, time.Time{}, err
	}
	if len(results) != 1 {
		return nil, 0, time.Time{}, time.Time{}, core.Errorf(core.CategoryCorruptData,
			"task operation read returned %d results, want 1", len(results))
	}
	result := results[0]
	// Compaction discards the history it summarizes, so a summary of part of
	// it would lose the rest for good.
	if result.Truncated != nil {
		return nil, 0, time.Time{}, time.Time{}, core.Wrap(core.CategoryOf(result.Truncated.Err),
			fmt.Sprintf("task %s history cannot be read in full at commit %s", taskID, result.Truncated.Commit),
			result.Truncated.Err)
	}
	if len(result.Commits) == 0 {
		return nil, 0, time.Time{}, time.Time{}, core.Errorf(core.CategoryCorruptData,
			"task %s has no history at %s", taskID, tip)
	}

	seen := make(map[string]struct{})
	add := func(id string) {
		if _, duplicate := seen[id]; duplicate {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	chain := result.Commits
	// The walk stops at any parentless commit and does not revalidate what it
	// reads, so a shallow boundary or a damaged history ends the chain at an
	// ordinary commit. Summarizing from there would drop everything before it.
	if !isRootPack(chain[0].Operation) {
		return nil, 0, time.Time{}, time.Time{}, core.Errorf(core.CategoryCorruptData,
			"task %s history ends at commit %s, which is not a task.create or history.checkpoint root",
			taskID, chain[0].ObjectID)
	}
	first = chain[0].Operation.WallTime
	if previous, ok := chain[0].Operation.Checkpoint(); ok {
		for _, id := range previous.CompactedOperations {
			add(id)
		}
		commits = previous.CompactedCommits
		first = previous.FirstWallTime
		last = previous.LastWallTime
		add(chain[0].Operation.Operations[0].ID)
		chain = chain[1:]
	}
	for _, commit := range chain {
		for _, operation := range commit.Operation.Operations {
			add(operation.ID)
		}
		commits++
		last = commit.Operation.WallTime
	}
	sort.Strings(ids)
	return ids, commits, first, last, nil
}

// CompactionAccountsFor reports whether moving a task from previous to
// current is a compaction that loses nothing: current's chain is rooted at a
// valid checkpoint, and every operation on previous's chain is either in that
// checkpoint's CompactedOperations or on current's chain, as an unpushed
// change replayed onto the checkpoint would be.
//
// It is the one generation change the head-advance and projection guards
// accept. A history this clone cannot read in full on either side proves
// nothing, so it answers false rather than failing: after gc the previous head
// may be gone, and the caller then reports the move as a rewrite, as it does
// any other.
func (r *Repository) CompactionAccountsFor(
	ctx context.Context,
	config core.ProjectConfig,
	previous core.Snapshot,
	current TaskHead,
) (bool, error) {
	taskID := current.TaskID
	// The two chains are read separately because one request set names each
	// task once.
	currentChain, ok, err := r.completeTaskChain(ctx, config, current)
	if err != nil || !ok {
		return false, err
	}
	root := currentChain[0]
	summary, isCheckpoint := root.Operation.Checkpoint()
	if !isCheckpoint {
		return false, nil
	}
	// The operation walk does not revalidate what it reads, and a list in a
	// commit that is not a valid checkpoint vouches for nothing.
	if _, err := r.ReadTaskHeads(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: root.ObjectID}}); err != nil {
		return false, err
	}
	previousChain, ok, err := r.completeTaskChain(ctx, config, TaskHead{TaskID: taskID, ObjectID: previous.Head})
	if err != nil || !ok {
		return false, err
	}

	accounted := make(map[string]struct{}, len(summary.CompactedOperations))
	for _, id := range summary.CompactedOperations {
		accounted[id] = struct{}{}
	}
	for _, commit := range currentChain {
		for _, operation := range commit.Operation.Operations {
			accounted[operation.ID] = struct{}{}
		}
	}
	for _, commit := range previousChain {
		for _, operation := range commit.Operation.Operations {
			if _, found := accounted[operation.ID]; !found {
				return false, nil
			}
		}
	}
	return true, nil
}

// completeTaskChain reads head's chain back to its root. It reports false,
// with no error, when this clone holds only part of it: a missing commit, a
// read that stopped early, or a chain that ends at a commit that is not a
// root, as a shallow boundary does.
func (r *Repository) completeTaskChain(
	ctx context.Context,
	config core.ProjectConfig,
	head TaskHead,
) ([]OperationCommit, bool, error) {
	results, err := r.ReadTaskOperations(ctx, config, []TaskHistoryRequest{{Head: head}})
	if err != nil {
		return nil, false, err
	}
	if len(results) != 1 {
		return nil, false, core.Errorf(core.CategoryCorruptData,
			"task operation read returned %d results, want 1", len(results))
	}
	result := results[0]
	if result.Truncated != nil || len(result.Commits) == 0 || !isRootPack(result.Commits[0].Operation) {
		return nil, false, nil
	}
	return result.Commits, true, nil
}
