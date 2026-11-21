package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
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

// TrackingTaskHeads returns, by task ID, the commit each task's tracking ref
// refs/workbook/remotes/origin/tasks/<id> names: where this clone last saw
// origin hold it. A tracking ref whose name is not one of this project's tasks
// is left out, as every reader of that namespace leaves it out.
func (r *Repository) TrackingTaskHeads(ctx context.Context, config core.ProjectConfig) (map[string]string, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	refs, _, err := r.listOwnedTaskRefs(ctx, config, remoteTaskRefPrefix)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string, len(refs))
	for _, ref := range refs {
		heads[ref.taskID] = ref.objectID
	}
	return heads, nil
}

// CompactionReclaimableBytes estimates the bytes compacting taskID at tip lets
// Git reclaim: the on-disk size of every object reachable from tip and the
// task's parked refs and from no other ref, less the size of tip's state.json,
// which stands in for the checkpoint the run writes. It may be negative for a
// history too small to outweigh its checkpoint.
//
// The task's own ref, its tracking ref and its parked refs are left out of
// "every other ref": a run moves the first, deletes the last, and the tracking
// ref follows once the push lands, so none of them holds the history after the
// run. It is an estimate in both directions: objects shared with another task
// are not counted, and Git frees nothing until it prunes.
func (r *Repository) CompactionReclaimableBytes(ctx context.Context, config core.ProjectConfig, taskID, tip string) (int64, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return 0, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return 0, err
	}
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return 0, err
	}
	if err := keys.RequireOwned(taskID); err != nil {
		return 0, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return 0, err
	}
	if err := r.validateFullObjectID(tip); err != nil {
		return 0, core.Wrap(core.CategoryValidation, "compaction estimate must name a full tip", err)
	}
	parkedPrefix := reconciledRefPrefix + taskID + "/"
	parked, err := r.listParkedRefs(ctx, config, parkedPrefix)
	if err != nil {
		return 0, err
	}
	// rev-list lists the objects and cat-file sizes them, rather than one
	// `rev-list --disk-usage`, which needs Git 2.31: a dry run must work on
	// every Git this build otherwise runs on.
	args := []string{"rev-list", "--objects", tip}
	for _, entry := range parked {
		args = append(args, entry.objectID)
	}
	args = append(args, "--not",
		"--exclude="+taskRefPrefix+taskID,
		"--exclude="+remoteTaskRefPrefix+taskID,
		"--exclude="+parkedPrefix+"*",
		"--all",
	)
	output, err := r.Git(ctx, nil, args...)
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "cannot list the history of task "+taskID, err)
	}
	usage, err := r.objectDiskUsage(ctx, output)
	if err != nil {
		return 0, err
	}
	output, err = r.Git(ctx, nil, "cat-file", "-s", tip+":state.json")
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "cannot measure the state of task "+taskID, err)
	}
	state, err := parseByteCount(output)
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "Git returned an invalid object size", err)
	}
	return usage - state, nil
}

// objectDiskUsage sums the on-disk size of every object `rev-list --objects`
// listed, with one cat-file batch. Each listed line is an object ID, followed
// by a path for a tree or blob, and only the ID is sent on.
func (r *Repository) objectDiskUsage(ctx context.Context, listed []byte) (int64, error) {
	var input bytes.Buffer
	count := 0
	for _, line := range bytes.Split(listed, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if err := r.validateFullObjectID(string(fields[0])); err != nil {
			return 0, core.Wrap(core.CategoryOperational, "Git listed an invalid object", err)
		}
		input.Write(fields[0])
		input.WriteByte('\n')
		count++
	}
	if count == 0 {
		return 0, nil
	}
	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch-check=%(objectname) %(objectsize:disk)")
	if err != nil {
		return 0, core.Wrap(core.CategoryOperational, "cannot measure listed objects", err)
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return 0, core.Errorf(core.CategoryOperational, "Git returned an unterminated object size listing")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != count {
		return 0, core.Errorf(core.CategoryOperational, "Git sized %d objects, want %d", len(lines), count)
	}
	var total int64
	for _, line := range lines {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			return 0, core.Errorf(core.CategoryOperational, "Git returned an unexpected object size line %q", line)
		}
		size, err := strconv.ParseInt(string(fields[1]), 10, 64)
		if err != nil || size < 0 {
			return 0, core.Errorf(core.CategoryOperational, "Git returned an unexpected object size line %q", line)
		}
		total += size
	}
	return total, nil
}

func parseByteCount(output []byte) (int64, error) {
	line, err := gitSingleLine(output)
	if err != nil {
		return 0, err
	}
	count, err := strconv.ParseInt(line, 10, 64)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("%q is not a byte count", line)
	}
	return count, nil
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

// CompactionAccountsFor reports whether moving one task from previous to
// current is a compaction that loses nothing. It is provenCompactions for a
// single advance; see there for the rule.
func (r *Repository) CompactionAccountsFor(
	ctx context.Context,
	config core.ProjectConfig,
	previous core.Snapshot,
	current TaskHead,
) (bool, error) {
	proven, err := r.provenCompactions(ctx, config, []HeadAdvance{{Previous: previous, Current: current}})
	if err != nil {
		return false, err
	}
	_, found := proven[current.TaskID]
	return found, nil
}

// provenCompactions returns the IDs of the tasks among advances whose move is
// a compaction that loses nothing: the current head's chain is rooted at a
// valid checkpoint, and every operation on the previous head's chain is
// either in that checkpoint's CompactedOperations or on the current chain, as
// an unpushed change replayed onto the checkpoint would be. advances must name
// each task once.
//
// It is the one generation change the head-advance and projection guards
// accept. A history this clone cannot read in full on either side proves
// nothing, so that task is simply left out rather than failing the call: after
// gc the previous head may be gone, and the caller then reports the move as a
// rewrite, as it does any other.
//
// The cost does not grow in Git processes with the number of tasks, because a
// bulk compaction moves hundreds of heads at once: one operation read for every
// current head, one validated read of every checkpoint root, and one operation
// read for the previous heads that are still candidates.
func (r *Repository) provenCompactions(
	ctx context.Context,
	config core.ProjectConfig,
	advances []HeadAdvance,
) (map[string]struct{}, error) {
	proven := make(map[string]struct{})
	if len(advances) == 0 {
		return proven, nil
	}
	currentHeads := make([]TaskHead, 0, len(advances))
	for _, advance := range advances {
		currentHeads = append(currentHeads, advance.Current)
	}
	currentChains, err := r.completeTaskChains(ctx, config, currentHeads)
	if err != nil {
		return nil, err
	}

	var candidates []int
	var roots []TaskHead
	for index, chain := range currentChains {
		if chain == nil {
			continue
		}
		if _, isCheckpoint := chain[0].Operation.Checkpoint(); !isCheckpoint {
			continue
		}
		candidates = append(candidates, index)
		roots = append(roots, TaskHead{TaskID: advances[index].Current.TaskID, ObjectID: chain[0].ObjectID})
	}
	if len(candidates) == 0 {
		return proven, nil
	}
	// The operation walk does not revalidate what it reads, and a list in a
	// commit that is not a valid checkpoint vouches for nothing.
	if _, err := r.ReadTaskHeads(ctx, config, roots); err != nil {
		return nil, err
	}

	previousHeads := make([]TaskHead, 0, len(candidates))
	for _, index := range candidates {
		previousHeads = append(previousHeads, TaskHead{
			TaskID:   advances[index].Current.TaskID,
			ObjectID: advances[index].Previous.Head,
		})
	}
	previousChains, err := r.completeTaskChains(ctx, config, previousHeads)
	if err != nil {
		return nil, err
	}
	for position, index := range candidates {
		previousChain := previousChains[position]
		if previousChain == nil {
			continue
		}
		if chainAccountsFor(currentChains[index], previousChain) {
			proven[advances[index].Current.TaskID] = struct{}{}
		}
	}
	return proven, nil
}

// chainAccountsFor reports whether current, a chain rooted at a checkpoint,
// holds or summarizes every operation on previous.
//
// A checkpoint on previous that current was not built on is accounted for by
// what it compacted rather than by its own ID. That is the case when two
// clones compacted the same tip and one adopts the other's checkpoint: the
// checkpoint it drops was never a change, so nothing is lost as long as every
// operation it summarized is still accounted for.
func chainAccountsFor(current, previous []OperationCommit) bool {
	summary, _ := current[0].Operation.Checkpoint()
	accounted := newOperationAccount(summary.CompactedOperations, current)
	for _, commit := range previous {
		if !accounted.covers(commit) {
			return false
		}
	}
	return true
}

// operationAccount is the set of operation IDs one history holds or
// summarizes. It is the single rule for whether an operation is already
// somewhere: the projection and head-advance guards ask it through
// chainAccountsFor, and fetch asks it to find what is left to replay.
type operationAccount map[string]struct{}

// newOperationAccount accounts for the listed IDs, typically a checkpoint's
// CompactedOperations, and every operation on chain.
func newOperationAccount(ids []string, chain []OperationCommit) operationAccount {
	accounted := make(operationAccount, len(ids)+len(chain))
	for _, id := range ids {
		accounted[id] = struct{}{}
	}
	for _, commit := range chain {
		for _, operation := range commit.Operation.Operations {
			accounted[operation.ID] = struct{}{}
		}
	}
	return accounted
}

// covers reports whether every operation commit holds is accounted for. A
// checkpoint is also covered when everything it compacted is, whatever its own
// ID: that is a checkpoint of history the account already holds, as when two
// clones compacted the same tip, and it was never a change of its own.
func (accounted operationAccount) covers(commit OperationCommit) bool {
	for _, operation := range commit.Operation.Operations {
		if _, found := accounted[operation.ID]; found {
			continue
		}
		if checkpoint, ok := commit.Operation.Checkpoint(); ok && accounted.coversAll(checkpoint.CompactedOperations) {
			continue
		}
		return false
	}
	return true
}

func (accounted operationAccount) coversAll(ids []string) bool {
	for _, id := range ids {
		if _, found := accounted[id]; !found {
			return false
		}
	}
	return true
}

// completeTaskChains reads each head's chain back to its root in one
// operation read. A chain is nil when this clone holds only part of it: a
// missing commit, a read that stopped early, or a chain that ends at a commit
// that is not a root, as a shallow boundary does. heads must name each task
// once.
func (r *Repository) completeTaskChains(
	ctx context.Context,
	config core.ProjectConfig,
	heads []TaskHead,
) ([][]OperationCommit, error) {
	requests := make([]TaskHistoryRequest, 0, len(heads))
	for _, head := range heads {
		requests = append(requests, TaskHistoryRequest{Head: head})
	}
	results, err := r.ReadTaskOperations(ctx, config, requests)
	if err != nil {
		return nil, err
	}
	if len(results) != len(requests) {
		return nil, core.Errorf(core.CategoryCorruptData,
			"task operation read returned %d results, want %d", len(results), len(requests))
	}
	chains := make([][]OperationCommit, len(results))
	for index, result := range results {
		if result.Truncated != nil || len(result.Commits) == 0 || !isRootPack(result.Commits[0].Operation) {
			continue
		}
		chains[index] = result.Commits
	}
	return chains, nil
}
