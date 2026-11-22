package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// compactionStaleDetail is what a push says when the lease on a checkpoint
// went stale: origin moved since this clone looked, and Git refused the push
// without touching origin. Git's own text ("[rejected] (stale info)")
// describes the mechanism, not what happened to the compaction.
func compactionStaleDetail(remote string) string {
	return remote + " changed since the compaction; the next sync reconciles it"
}

// compactionRefusedDetail is what a push says when origin refused a leased
// checkpoint for any other reason: a server-side hook, a ruleset, or a host
// that refuses rewrites whatever Git calls it. Git's reason stays in
// parentheses, since it is the only account of origin's policy. Nothing
// settles this from this clone except undoing the compaction, which the text
// names: every later sync and push would meet the same refusal.
func compactionRefusedDetail(remote, taskID, reason string) string {
	return fmt.Sprintf(`%s refused the compacted checkpoint (%s); the compacted task stays local. `+
		`Run "workbook compact --adopt %s --discard-local" to undo the compaction on this clone.`,
		remote, reason, taskID)
}

// unrelatedHistoryDetail reports a local checkpoint whose task origin holds a
// history it shares nothing with. Publishing would replace that history, so
// the checkpoint stays local.
func unrelatedHistoryDetail(remote string) string {
	return remote + " has an unrelated history for this task; it was not published"
}

// compactedOnOriginDetail is what fetch says about a task origin compacted
// whose local-only changes it could not replay onto the checkpoint.
func compactedOnOriginDetail(remote, title, taskID string, unpushed int) string {
	changes := fmt.Sprintf("%d unpushed changes", unpushed)
	if unpushed == 1 {
		changes = "1 unpushed change"
	}
	return fmt.Sprintf(`"%s" was compacted on %s; you have %s on the old history. `+
		`Run "workbook compact --adopt %s" to retry, `+
		`or add --discard-local to take %s's version after saving yours to a bundle.`,
		title, remote, changes, taskID, remote)
}

// cannotCombineDetail is what fetch says about a task this clone deleted on
// its checkpoint while origin changed the old history: nothing applies to a
// deleted task, so origin's change cannot win the way an edit's does.
func cannotCombineDetail(remote, title, taskID string) string {
	return fmt.Sprintf(`"%s" changed on %s after you compacted it, and the two can't be combined. `+
		`Run "workbook compact --adopt %s --discard-local" to take %s's version `+
		`after saving yours to a bundle; then compact it again.`,
		title, remote, taskID, remote)
}

// leasedPush describes one ref pushed as a deliberate non-fast-forward.
type leasedPush struct{ TaskID, Local, ExpectedRemote string }

// compactionLease decides whether publishing local over origin's remoteHead is
// the deliberate non-fast-forward a compaction makes.
//
// It is one when local's history begins at a checkpoint (its state carries the
// compactedFrom the checkpoint recorded), origin's tip is not already on that
// history, and origin's tip belongs to the older generation the checkpoint
// fully accounts for: every operation on origin's chain is in the checkpoint's
// list or on local's chain, which is the rule the projection uses to accept the
// same move (CompactionAccountsFor). The lease is on origin's tip as observed,
// so the push succeeds only while origin still holds exactly the history this
// clone has proven it holds. compactedFrom itself is the simplest case; origin
// behind it, or ahead of it with changes fetch has since replayed onto the
// checkpoint, are the others. When origin has moved again, the lease fails as
// stale and Git refuses the push without touching origin.
//
// Every other case is an ordinary push and gets no lease: a task that was never
// compacted, one origin does not hold yet, a compacted one whose checkpoint
// origin already has (local extends it by a fast-forward, or a teammate did
// and Git refuses an ordinary non-fast-forward), and an origin tip this clone
// does not hold or cannot prove it accounts for, which Git also refuses as an
// ordinary non-fast-forward until a fetch reconciles it.
func (r *Repository) compactionLease(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	local core.Snapshot,
	remoteHead string,
) (leasedPush, bool, error) {
	compactedFrom := local.State.History.CompactedFrom
	if compactedFrom == nil || remoteHead == "" || remoteHead == local.Head {
		return leasedPush{}, false, nil
	}
	lease := leasedPush{TaskID: taskID, Local: local.Head, ExpectedRemote: remoteHead}
	if remoteHead == *compactedFrom {
		return lease, true, nil
	}
	published, err := r.onLocalHistory(ctx, local.Head, remoteHead)
	if err != nil {
		return leasedPush{}, false, err
	}
	if published {
		return leasedPush{}, false, nil
	}
	// The accounting is also what turns away the two tips that are not this
	// clone's to lease over. One it does not hold, or holds only in part,
	// proves nothing. One in local's own generation that is not on local
	// history is a teammate's extension of the published checkpoint, and
	// holds their change, which local does not have.
	accounted, err := r.CompactionAccountsFor(ctx, config,
		core.Snapshot{Head: remoteHead}, TaskHead{TaskID: taskID, ObjectID: local.Head})
	if err != nil || !accounted {
		return leasedPush{}, false, err
	}
	return lease, true, nil
}

// compactedOnOriginOnly reports whether the tip the tracking ref names for
// taskID belongs to a compacted history that local is not on: origin holds a
// checkpoint, or work built on one, in a generation this clone's ref is not in.
// That is the state adoption's fallback leaves a task in.
func (r *Repository) compactedOnOriginOnly(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	local core.Snapshot,
) (bool, error) {
	tracking, err := r.TrackingTaskHeads(ctx, config)
	if err != nil {
		return false, err
	}
	remoteHead := tracking[taskID]
	if remoteHead == "" {
		return false, nil
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: remoteHead}})
	if err != nil {
		return false, err
	}
	if len(tips) != 1 || tips[0].Err != nil {
		return false, nil
	}
	remote := tips[0].Snapshot
	return remote.State.History.CompactedFrom != nil &&
		remote.Operation.HistoryGeneration != local.Operation.HistoryGeneration, nil
}

// onLocalHistory reports whether commit is reachable from head. A compacted
// history begins at a parentless checkpoint, so the walk covers only the
// commits written since the compaction, and it needs nothing origin holds that
// this clone has not fetched.
func (r *Repository) onLocalHistory(ctx context.Context, head, commit string) (bool, error) {
	output, err := r.Git(ctx, nil, "rev-list", head)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == commit {
			return true, nil
		}
	}
	return false, nil
}

// pushCandidate is one task ref a push path publishes, with its lease when
// compactionLease approved one.
type pushCandidate struct {
	TaskID string
	Head   string
	Lease  *leasedPush
}

// advanceTrackingRef compare-and-swaps a task's tracking ref from expected, the
// tip the lease was taken against, to next. A tracking ref that does not exist
// is created. One holding anything else was moved by a fetch that ran beside
// this push, and is left for the next fetch, which observes origin afresh.
//
// A reflog entry for the old value would keep the compacted history reachable
// just as the ref did, so the update asks for none. That holds only under
// Git's default core.logAllRefUpdates=true, which logs refs/heads/,
// refs/remotes/ and refs/notes/ but not refs/workbook/; a clone set to
// `always` keeps a reflog for the tracking ref, and that reflog holds the old
// history until it expires.
func (r *Repository) advanceTrackingRef(ctx context.Context, taskID, expected, next string) error {
	ref := r.remoteTaskRefPrefix() + taskID
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return err
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	switch current := strings.TrimSpace(string(output)); current {
	case "":
		fmt.Fprintf(&input, "create %s %s\n", ref, next)
	case expected:
		fmt.Fprintf(&input, "update %s %s %s\n", ref, next, expected)
	default:
		return nil
	}
	input.WriteString("prepare\ncommit\n")
	_, err = r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "--stdin")
	return err
}

// leasedRejectionDetail translates remote's refusal of taskID's leased ref.
func leasedRejectionDetail(remote, taskID, detail string) string {
	if strings.Contains(detail, "stale info") {
		return compactionStaleDetail(remote)
	}
	return compactionRefusedDetail(remote, taskID, detail)
}

// awaitsCheckpointPublication reports a fetched pair whose generations differ
// only because this clone compacted the task and has not published the
// checkpoint: origin still holds exactly the tip the checkpoint replaced. The
// push phase publishes it with a lease. Any other generation change is fetch's
// to reconcile.
func awaitsCheckpointPublication(local, remote core.Snapshot) bool {
	compactedFrom := local.State.History.CompactedFrom
	return compactedFrom != nil && *compactedFrom == remote.Head
}

// PushTasks publishes the named task refs to origin in one push, as compact
// does for the checkpoints it wrote. Like PushTask it lists nothing remote:
// each task's tracking ref, which the fetch before the run brought up to date,
// is where origin is expected to be, and a compacted task is leased against
// that tip when it is older history the checkpoint accounts for. Git's own
// rules guard the rest.
//
// Only the named tips are validated, so an unrelated malformed ref cannot
// block publishing them.
func (r *Repository) PushTasks(ctx context.Context, config core.ProjectConfig, taskIDs []string) (SyncResult, error) {
	result := SyncResult{Remote: r.remote(), Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	unique := make(map[string]struct{}, len(taskIDs))
	ordered := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if _, seen := unique[taskID]; !seen {
			unique[taskID] = struct{}{}
			ordered = append(ordered, taskID)
		}
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		result.Status = SyncPhaseCompleted
		return result, nil
	}

	heads := make([]TaskHead, 0, len(ordered))
	for _, taskID := range ordered {
		ref, found, err := r.taskRef(ctx, taskID)
		if err != nil {
			return failedSyncPhase(result, "push failed before completion", err)
		}
		if !found {
			return failedSyncPhase(result, "push failed before completion",
				core.Errorf(core.CategoryNotFound, "task %s has no Workbook ref to publish", taskID))
		}
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	items := make(map[string]SyncTaskResult, len(ordered))
	valid := make(map[string]core.Snapshot, len(tips))
	invalid := 0
	for _, tip := range tips {
		if tip.Err != nil {
			items[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
			invalid++
			continue
		}
		valid[tip.Head.TaskID] = tip.Snapshot
	}

	// The same two settlements PushTask makes before writing a task ref: the
	// project origin holds, and the vocabulary these tasks are written against.
	if err := r.ensureOriginIdentityAgreement(ctx, nil, false); err != nil {
		return failedPushTasks(result, ordered, items, "push stopped before publishing any task ref", err)
	}
	result.Identity, _ = r.IdentityReport()
	published, err := r.publishConfigLedger(ctx)
	if err != nil {
		return failedPushTasks(result, ordered, items, "push failed before completion", err)
	}
	result.Config = published

	var tracking map[string]string
	candidates := make([]pushCandidate, 0, len(valid))
	for _, taskID := range ordered {
		snapshot, ok := valid[taskID]
		if !ok {
			continue
		}
		candidate := pushCandidate{TaskID: taskID, Head: snapshot.Head}
		if snapshot.State.History.CompactedFrom != nil {
			if tracking == nil {
				if tracking, err = r.TrackingTaskHeads(ctx, config); err != nil {
					return failedPushTasks(result, ordered, items, "push failed before completion", err)
				}
			}
			lease, leased, err := r.compactionLease(ctx, config, taskID, snapshot, tracking[taskID])
			if err != nil {
				return failedPushTasks(result, ordered, items, "push failed before completion", err)
			}
			if leased {
				candidate.Lease = &lease
			}
		}
		candidates = append(candidates, candidate)
	}
	var stranded error
	if len(candidates) != 0 {
		pushed, err := r.pushTaskHeads(ctx, config, candidates)
		if err != nil {
			return failedPushTasks(result, ordered, items, "push failed before completion", err)
		}
		for taskID, item := range pushed.Tasks {
			items[taskID] = item
		}
		invalid += pushed.Duplicated
		stranded = pushed.Stranded
	}

	var rejected []SyncTaskResult
	for _, taskID := range ordered {
		if items[taskID].Status == SyncRejected {
			rejected = append(rejected, items[taskID])
		}
		result.Tasks = append(result.Tasks, items[taskID])
	}
	result.Status = SyncPhaseCompleted
	if stranded != nil {
		return result, stranded
	}
	if invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalid)
	}
	if len(rejected) > 0 {
		return result, r.rejectedByRemote(rejected)
	}
	return result, nil
}

func failedPushTasks(
	result SyncResult,
	taskIDs []string,
	items map[string]SyncTaskResult,
	detail string,
	err error,
) (SyncResult, error) {
	for _, taskID := range taskIDs {
		if item, found := items[taskID]; found {
			result.Tasks = append(result.Tasks, item)
		}
	}
	return failedSyncPhase(result, detail, err)
}

// generationMismatchDetail is fetch's report on a pair whose generations
// differ for no reason compaction explains.
const generationMismatchDetail = "tracking and canonical tips use different history generations"

// compactedOutcomeKind says which of fetch's tallies a compacted pair joins.
type compactedOutcomeKind int

const (
	compactedSettled compactedOutcomeKind = iota
	// compactedInvalid joins the fetched tips that failed validation.
	compactedInvalid
	// compactedNotReplayed joins the divergent histories that could not be
	// replayed, as an ordinary reconciliation's failure does.
	compactedNotReplayed
	compactedNewerWriter
)

// compactedOutcome is what fetch does with one pair whose generations differ
// because one side was compacted. Update is nil when the canonical ref stays
// where it is.
type compactedOutcome struct {
	TaskID   string
	Result   SyncTaskResult
	Update   *canonicalRefUpdate
	Snapshot core.Snapshot
	Conflict *core.Conflict
	// Withhold keeps the task out of this run's push phase: origin holds a
	// tip the local ref neither descends from nor may be leased over, so a
	// push could only be refused, or would publish over history it has not
	// accounted for.
	Withhold bool
	Kind     compactedOutcomeKind
}

// reconcileCompactedTasks settles every fetched pair whose generations differ
// and at least one of whose histories begins at a checkpoint:
//
//   - origin compacted the task (its chain begins at a checkpoint this clone
//     did not write from origin's generation): adopt origin's checkpoint,
//     replaying onto it whatever local operations it does not already hold.
//     Remote wins even when this clone compacted too.
//   - this clone compacted the task and origin still holds the older
//     generation: replay onto the local checkpoint whatever origin added that
//     the checkpoint does not hold. The push phase then leases against
//     origin's tip.
//
// Like reconcileDivergentTasks it writes commit objects but moves no ref; the
// caller applies each Update in fetch's one transaction.
func (r *Repository) reconcileCompactedTasks(
	ctx context.Context,
	config core.ProjectConfig,
	dependencies map[string][]string,
	requests []reconcileRequest,
) ([]compactedOutcome, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	localHeads := make([]TaskHead, 0, len(requests))
	remoteHeads := make([]TaskHead, 0, len(requests))
	for _, request := range requests {
		localHeads = append(localHeads, TaskHead{TaskID: request.TaskID, ObjectID: request.Local.Head})
		remoteHeads = append(remoteHeads, TaskHead{TaskID: request.TaskID, ObjectID: request.Remote.Head})
	}
	localChains, err := r.completeTaskChains(ctx, config, localHeads)
	if err != nil {
		return nil, err
	}
	remoteChains, err := r.completeTaskChains(ctx, config, remoteHeads)
	if err != nil {
		return nil, err
	}

	parked, err := r.nextParkedRefIndexes(ctx, config)
	if err != nil {
		return nil, err
	}

	outcomes := make([]compactedOutcome, len(requests))
	for index, request := range requests {
		outcome, err := r.reconcileCompactedTask(ctx, config, dependencies, parked[request.TaskID], request,
			localChains[index], remoteChains[index])
		if err != nil {
			return nil, err
		}
		outcomes[index] = outcome
	}
	return outcomes, nil
}

func (r *Repository) reconcileCompactedTask(
	ctx context.Context,
	config core.ProjectConfig,
	dependencies map[string][]string,
	parkedIndex int,
	request reconcileRequest,
	localChain, remoteChain []OperationCommit,
) (compactedOutcome, error) {
	if localChain != nil && remoteChain != nil {
		adopt, onto := compactedSides(localChain, remoteChain, request.Remote.Operation.HistoryGeneration)
		switch {
		case adopt != nil:
			return r.adoptRemoteCheckpoint(ctx, config, dependencies, request, adopt)
		case onto != nil:
			return r.replayOntoLocalCheckpoint(ctx, config, dependencies, parkedIndex, request, onto, remoteChain)
		}
	}
	return compactedOutcome{
		TaskID:   request.TaskID,
		Result:   SyncTaskResult{TaskID: request.TaskID, Status: SyncInvalid, Detail: generationMismatchDetail},
		Snapshot: request.Local,
		Kind:     compactedInvalid,
	}, nil
}

// compactedSides decides which side of a pair whose generations differ was
// compacted. adopt is origin's checkpoint, when origin compacted the task and
// this clone is to take it; onto is this clone's checkpoint, when origin still
// holds the older history and its changes are replayed onto the local one.
// Both are nil when neither chain begins at a checkpoint that explains the
// difference.
//
// A local checkpoint written from origin's generation means origin is the
// older side, whatever its own root is. Fetch and the stuck-task detection
// both ask this, so the two can never disagree about which way a pair goes.
func compactedSides(localChain, remoteChain []OperationCommit, remoteGeneration string) (adopt, onto *core.CheckpointData) {
	localCheckpoint, _ := localChain[0].Operation.Checkpoint()
	remoteCheckpoint, _ := remoteChain[0].Operation.Checkpoint()
	localFromRemote := localCheckpoint != nil && localCheckpoint.PreviousGeneration == remoteGeneration
	switch {
	case remoteCheckpoint != nil && !localFromRemote:
		return remoteCheckpoint, nil
	case localCheckpoint != nil:
		return nil, localCheckpoint
	}
	return nil, nil
}

// adoptRemoteCheckpoint moves the canonical ref onto origin's checkpoint. The
// local-only operations are those on local history that are neither in the
// checkpoint's list nor on origin's chain; with none, the ref moves to origin's
// tip, and otherwise they are replayed onto it in its generation. The old tip
// is neither parked nor left in the reflog, since either would keep the
// compacted history alive. Parked tips from earlier reconciliations go too,
// except one that still holds an edit the task lacks: that is somebody's work
// that only this clone holds, and it is kept, and shown, until a person
// dismisses it. A replay that cannot run cleanly leaves the ref
// where it is, on the old history, and says how to adopt by hand.
func (r *Repository) adoptRemoteCheckpoint(
	ctx context.Context,
	config core.ProjectConfig,
	dependencies map[string][]string,
	request reconcileRequest,
	checkpoint *core.CheckpointData,
) (compactedOutcome, error) {
	outcome := compactedOutcome{TaskID: request.TaskID, Snapshot: request.Local}
	localOnly, err := r.operationsNotIn(ctx, config, request.TaskID, request.Local.Head, request.Remote.Head,
		operationIDSet(checkpoint.CompactedOperations))
	if err != nil {
		return outcome, err
	}
	update := canonicalRefUpdate{
		TaskID:            request.TaskID,
		Next:              request.Remote.Head,
		Expected:          request.Local.Head,
		NoPark:            true,
		DropSettledParked: true,
	}
	if len(localOnly) == 0 {
		outcome.Update = &update
		outcome.Snapshot = request.Remote
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncAdopted}
		return outcome, nil
	}

	fallBack := func() (compactedOutcome, error) {
		// A person made each change the detail counts; a leftover local
		// checkpoint is bookkeeping, not one of them.
		unpushed := 0
		for _, commit := range localOnly {
			if _, isCheckpoint := commit.Operation.Checkpoint(); !isCheckpoint {
				unpushed++
			}
		}
		outcome.Result = SyncTaskResult{
			TaskID: request.TaskID,
			Status: SyncCompactedOnOrigin,
			Detail: compactedOnOriginDetail(r.remote(), request.Local.State.Task.Title, request.TaskID, unpushed),
		}
		// The task's own state, reported on its line every run, rather
		// than a failed synchronization: everything else fetched and
		// published, and the watcher must stay trustworthy while one task
		// waits for somebody to adopt it by hand.
		outcome.Withhold = true
		return outcome, nil
	}
	// A checkpoint this build cannot fold is a fact about this build, not a
	// replay to retry by hand: the task waits for an upgrade, as a divergent
	// one does, and `compact --adopt` would meet the same refusal.
	needsUpgrade := func(refusal error) (compactedOutcome, error) {
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncNeedsUpgrade, Detail: refusal.Error()}
		outcome.Withhold = true
		outcome.Kind = compactedNewerWriter
		return outcome, nil
	}
	if refusal := refuseNewerWriterReplay(request); refusal != nil {
		return needsUpgrade(refusal)
	}
	replay, failure, err := r.replayOperations(ctx, config, request.TaskID, request.Remote, localOnly, false,
		request.Remote.Operation.HistoryGeneration, dependencies, nil)
	if err != nil {
		return outcome, err
	}
	if failure != nil && core.CategoryOf(failure) == core.CategoryNewerWriter {
		return needsUpgrade(failure)
	}
	if failure != nil || replay.conflict != nil {
		return fallBack()
	}
	update.Next = replay.parent.Head
	outcome.Update = &update
	outcome.Snapshot = replay.parent
	outcome.Result = SyncTaskResult{
		TaskID: request.TaskID,
		Status: SyncAdoptedReplayed,
		Detail: fmt.Sprintf("replayed %d local operation(s) onto %s's checkpoint", replay.replayed, r.remote()),
	}
	return outcome, nil
}

// replayOntoLocalCheckpoint settles a task this clone compacted while origin
// still holds the older generation. Origin's chain must share history with the
// one the checkpoint replaced, which it does exactly when its root operation
// is among those the checkpoint compacted; anything else is an unrelated
// history and is never published over. What origin added that the checkpoint
// does not hold is replayed onto the local tip in the local generation, so the
// canonical ref only moves forward. Origin's tip is then fully accounted for,
// and the push phase leases against it.
//
// A conflict is settled as an ordinary reconcile settles one: origin wins.
// Origin's operation is applied over the local value, replay goes on, the
// pre-sync local tip is parked under parkedIndex so its text can be recovered,
// and the conflict is reported. A tombstone conflict cannot be settled that
// way, since nothing applies to a deleted task, so it leaves the ref where it
// is and keeps the task off origin.
func (r *Repository) replayOntoLocalCheckpoint(
	ctx context.Context,
	config core.ProjectConfig,
	dependencies map[string][]string,
	parkedIndex int,
	request reconcileRequest,
	checkpoint *core.CheckpointData,
	remoteChain []OperationCommit,
) (compactedOutcome, error) {
	outcome := compactedOutcome{TaskID: request.TaskID, Snapshot: request.Local}
	exclude := operationIDSet(checkpoint.CompactedOperations)
	if !newOperationAccount(checkpoint.CompactedOperations, nil).covers(remoteChain[0]) {
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncInvalid, Detail: unrelatedHistoryDetail(r.remote())}
		outcome.Withhold = true
		outcome.Kind = compactedInvalid
		return outcome, nil
	}
	remoteOnly, err := r.operationsNotIn(ctx, config, request.TaskID, request.Remote.Head, request.Local.Head, exclude)
	if err != nil {
		return outcome, err
	}
	if len(remoteOnly) == 0 {
		// Origin is at or behind the tip the checkpoint replaced.
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncLocalAhead}
		return outcome, nil
	}

	replay, failure, err := r.replayOperations(ctx, config, request.TaskID, request.Local, remoteOnly, true,
		request.Local.Operation.HistoryGeneration, dependencies, nil)
	if err != nil {
		return outcome, err
	}
	outcome.Withhold = failure != nil || replay.conflict != nil
	switch {
	case failure != nil:
		outcome.Kind = compactedNotReplayed
		status := SyncInvalid
		if core.CategoryOf(failure) == core.CategoryNewerWriter {
			outcome.Kind = compactedNewerWriter
			status = SyncNeedsUpgrade
		}
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: status, Detail: failure.Error()}
	case replay.conflict != nil:
		// A tombstone conflict. The ref stays where it is, since origin's tip
		// is not accounted for and could not be leased over.
		conflict := fromLocalSide(*replay.conflict)
		outcome.Conflict = &conflict
		outcome.Result = SyncTaskResult{
			TaskID: request.TaskID,
			Status: SyncConflicted,
			Detail: cannotCombineDetail(r.remote(), request.Local.State.Task.Title, request.TaskID),
		}
	case replay.overridden != nil:
		conflict := fromLocalSide(*replay.overridden)
		outcome.Update = &canonicalRefUpdate{
			TaskID:     request.TaskID,
			Next:       replay.parent.Head,
			Expected:   request.Local.Head,
			ParkedRef:  fmt.Sprintf("%s%s/%d", reconciledRefPrefix, request.TaskID, parkedIndex),
			ParkedHead: request.Local.Head,
		}
		outcome.Snapshot = replay.parent
		outcome.Conflict = &conflict
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncConflicted, Detail: core.ConflictDetail(conflict)}
	default:
		outcome.Update = &canonicalRefUpdate{TaskID: request.TaskID, Next: replay.parent.Head, Expected: request.Local.Head}
		outcome.Snapshot = replay.parent
		outcome.Result = SyncTaskResult{
			TaskID: request.TaskID,
			Status: SyncReconciled,
			Detail: fmt.Sprintf("replayed %d operation(s) from %s onto the local checkpoint", replay.replayed, r.remote()),
		}
	}
	return outcome, nil
}

// operationsNotIn returns, oldest first, the commits on head's chain holding an
// operation that is neither in exclude nor on target's chain (target walked to
// its root). A checkpoint counts as held when everything it compacted is.
//
// A checkpoint at the root of head's chain that is not held summarizes history
// only head's side ever had, as when this clone compacted a later tip than
// origin did. Its operations survive only in the history it replaced, so the
// walk continues from its compactedFrom while this clone still holds that
// history; when it does not, the checkpoint itself is returned, and replaying
// it fails.
func (r *Repository) operationsNotIn(
	ctx context.Context,
	config core.ProjectConfig,
	taskID, head, target string,
	exclude map[string]struct{},
) ([]OperationCommit, error) {
	read := func(objectID string) ([]OperationCommit, error) {
		chains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: objectID}})
		if err != nil {
			return nil, err
		}
		return chains[0], nil
	}
	targetChain, err := read(target)
	if err != nil {
		return nil, err
	}
	headChain, err := read(head)
	if err != nil {
		return nil, err
	}
	if targetChain == nil || headChain == nil {
		return nil, core.Errorf(core.CategoryCorruptData, "task %s history cannot be read in full", taskID)
	}
	ids := make([]string, 0, len(exclude))
	for id := range exclude {
		ids = append(ids, id)
	}
	accounted := newOperationAccount(ids, targetChain)

	// Each step reads strictly older history, and a well-formed one ends at a
	// task.create; the bound only guards a history that loops.
	for steps := 0; steps < 64; steps++ {
		checkpoint, isCheckpoint := headChain[0].Operation.Checkpoint()
		if !isCheckpoint || accounted.covers(headChain[0]) {
			break
		}
		replaced, err := read(checkpoint.CompactedFrom)
		if err != nil {
			return nil, err
		}
		if replaced == nil {
			break
		}
		headChain = append(replaced, headChain[1:]...)
	}

	var missing []OperationCommit
	for _, commit := range headChain {
		if !accounted.covers(commit) {
			missing = append(missing, commit)
		}
	}
	return missing, nil
}

// fromLocalSide restates a conflict met replaying origin's operations onto
// this clone's history the way every other conflict is reported: ours is this
// clone's value and theirs is origin's. Replay found them the other way round.
func fromLocalSide(conflict core.Conflict) core.Conflict {
	if conflict.Description != nil {
		swapped := *conflict.Description
		swapped.Ours, swapped.Theirs = swapped.Theirs, swapped.Ours
		conflict.Description = &swapped
	}
	return conflict
}

func operationIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// replayOperations replays commits, oldest first, onto onto in generation. The
// three-way base is the commit before the first of them on its own history.
// originWins says the commits are origin's, so a conflict applies them over
// onto's value rather than stopping (see taskReplay.originWins).
// failure reports why the replay could not run cleanly: a commit with no
// history before it, one that does not validate, or one that cannot be applied
// onto onto. err is reserved for this clone failing to read its own objects.
func (r *Repository) replayOperations(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	onto core.Snapshot,
	commits []OperationCommit,
	originWins bool,
	generation string,
	dependencies map[string][]string,
	held operationAccount,
) (replay taskReplay, failure error, err error) {
	if commits[0].Parent == "" {
		return replay, core.Errorf(core.CategoryCorruptData,
			"task %s operation %s has no history before it to replay from", taskID, commits[0].ObjectID), nil
	}
	heads := make([]TaskHead, 0, len(commits)+1)
	heads = append(heads, TaskHead{TaskID: taskID, ObjectID: commits[0].Parent})
	for _, commit := range commits {
		heads = append(heads, TaskHead{TaskID: taskID, ObjectID: commit.ObjectID})
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return replay, nil, err
	}
	subjects, err := r.readCommitSubjects(ctx, heads[1:])
	if err != nil {
		return replay, nil, err
	}
	for _, tip := range tips {
		if tip.Err != nil {
			return replay, tip.Err, nil
		}
	}

	replay = taskReplay{
		config:       config,
		taskID:       taskID,
		base:         tips[0].Snapshot.State,
		parent:       onto,
		dependencies: taskDependencyGraph(dependencies, taskID, onto.State.Task),
		subjects:     subjects,
		generation:   generation,
		originWins:   originWins,
		held:         held,
	}
	for _, tip := range tips[1:] {
		done, err := replay.next(ctx, r, tip.Snapshot)
		if err != nil {
			return replay, err, nil
		}
		if done {
			break
		}
	}
	return replay, nil, nil
}

// StuckKind names one of the two states a compaction can leave a task in that
// no synchronization settles by itself. Either way the canonical ref holds
// history origin will not take, fetch withholds it from every push, and only
// `workbook compact --adopt` moves it.
type StuckKind string

const (
	// StuckCompactedOnOrigin is a task origin compacted whose local-only
	// changes would not replay onto the checkpoint. The ref stays on the old
	// history (SyncCompactedOnOrigin).
	StuckCompactedOnOrigin StuckKind = "compacted-on-origin"
	// StuckCannotCombine is a task this clone compacted and deleted while
	// origin changed the old history. Nothing applies to a deleted task, so
	// origin's changes cannot replay onto the local checkpoint the way an edit
	// does (the tombstone conflict replayOntoLocalCheckpoint reports).
	StuckCannotCombine StuckKind = "cannot-combine"
)

// StuckCompaction is one task in a stuck state, with the detail fetch reports
// for it, which is also what a command refusing to touch it says, and the
// category that refusal carries: a conflict, or CategoryNewerWriter for a
// checkpoint this build cannot fold, which only an upgrade settles.
type StuckCompaction struct {
	TaskID   string
	Kind     StuckKind
	Category core.Category
	Detail   string

	local, remote core.Snapshot
	// checkpoint is origin's for StuckCompactedOnOrigin and this clone's for
	// StuckCannotCombine.
	checkpoint  *core.CheckpointData
	localChain  []OperationCommit
	remoteChain []OperationCommit
}

// StuckCompactions finds the stuck tasks among taskIDs, or among every task
// when taskIDs is nil, from local refs alone: each canonical ref against its
// tracking ref. It costs two ref listings and one tip read for the tasks whose
// two refs differ; only a pair whose generations differ is read further.
//
// Reading refs rather than the last fetch's outcome is what makes the answer
// hold with --no-sync, beside a live watcher that did the fetching, and right
// after a fetch alike. It relies on fetch always attempting the settlement:
// fetch moves a tracking ref and tries to adopt or replay in the same run, so a
// pair still in one of these shapes is one that attempt could not settle.
func (r *Repository) StuckCompactions(ctx context.Context, config core.ProjectConfig, taskIDs []string) (map[string]StuckCompaction, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	var wanted map[string]struct{}
	if taskIDs != nil {
		wanted = operationIDSet(taskIDs)
	}
	canonical, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return nil, err
	}
	trackingRefs, _, err := r.listOwnedTaskRefs(ctx, config, r.remoteTaskRefPrefix())
	if err != nil {
		return nil, err
	}
	tracking := make(map[string]string, len(trackingRefs))
	for _, ref := range trackingRefs {
		tracking[ref.taskID] = ref.objectID
	}
	var heads []TaskHead
	for _, ref := range canonical {
		if _, ok := wanted[ref.taskID]; wanted != nil && !ok {
			continue
		}
		remote := tracking[ref.taskID]
		if remote == "" || remote == ref.objectID {
			continue
		}
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID}, TaskHead{TaskID: ref.taskID, ObjectID: remote})
	}
	stuck := make(map[string]StuckCompaction)
	if len(heads) == 0 {
		return stuck, nil
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}
	for index := 0; index+1 < len(tips); index += 2 {
		local, remote := tips[index], tips[index+1]
		if local.Err != nil || remote.Err != nil {
			continue
		}
		entry, found, err := r.classifyStuck(ctx, config, local.Head.TaskID, local.Snapshot, remote.Snapshot)
		if err != nil {
			return nil, err
		}
		if found {
			stuck[entry.TaskID] = entry
		}
	}
	return stuck, nil
}

// classifyStuck decides whether one canonical and tracking pair is stuck, by
// the rules reconcileCompactedTask settles the same pair with.
func (r *Repository) classifyStuck(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	local, remote core.Snapshot,
) (StuckCompaction, bool, error) {
	if local.Operation.HistoryGeneration == remote.Operation.HistoryGeneration ||
		awaitsCheckpointPublication(local, remote) ||
		(local.State.History.CompactedFrom == nil && remote.State.History.CompactedFrom == nil) {
		return StuckCompaction{}, false, nil
	}
	localChains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: local.Head}})
	if err != nil {
		return StuckCompaction{}, false, err
	}
	remoteChains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: remote.Head}})
	if err != nil {
		return StuckCompaction{}, false, err
	}
	if localChains[0] == nil || remoteChains[0] == nil {
		return StuckCompaction{}, false, nil
	}
	entry := StuckCompaction{
		TaskID:      taskID,
		local:       local,
		remote:      remote,
		localChain:  localChains[0],
		remoteChain: remoteChains[0],
	}
	adopt, onto := compactedSides(entry.localChain, entry.remoteChain, remote.Operation.HistoryGeneration)
	switch {
	case adopt != nil:
		localOnly, err := r.operationsNotIn(ctx, config, taskID, local.Head, remote.Head,
			operationIDSet(adopt.CompactedOperations))
		if err != nil || len(localOnly) == 0 {
			return StuckCompaction{}, false, err
		}
		unpushed := 0
		for _, commit := range localOnly {
			if _, isCheckpoint := commit.Operation.Checkpoint(); !isCheckpoint {
				unpushed++
			}
		}
		entry.Kind = StuckCompactedOnOrigin
		entry.checkpoint = adopt
		entry.Category = core.CategoryConflict
		entry.Detail = compactedOnOriginDetail(r.remote(), local.State.Task.Title, taskID, unpushed)
		// What fetch reported instead for a checkpoint this build cannot
		// fold (adoptRemoteCheckpoint): an upgrade, not --adopt, moves it.
		if refusal := refuseNewerWriterReplay(reconcileRequest{TaskID: taskID, Local: local, Remote: remote}); refusal != nil {
			entry.Category = core.CategoryNewerWriter
			entry.Detail = refusal.Error()
		}
		return entry, true, nil
	case onto != nil:
		// Only a deleted local task makes origin's changes impossible to
		// replay: any other conflict on the local checkpoint is settled
		// origin-wins. A history unrelated to the checkpoint is a different
		// refusal, and so is an origin that added nothing.
		if !local.State.Task.Deleted || !newOperationAccount(onto.CompactedOperations, nil).covers(entry.remoteChain[0]) {
			return StuckCompaction{}, false, nil
		}
		remoteOnly, err := r.operationsNotIn(ctx, config, taskID, remote.Head, local.Head,
			operationIDSet(onto.CompactedOperations))
		if err != nil || len(remoteOnly) == 0 {
			return StuckCompaction{}, false, err
		}
		entry.Kind = StuckCannotCombine
		entry.checkpoint = onto
		entry.Category = core.CategoryConflict
		entry.Detail = cannotCombineDetail(r.remote(), local.State.Task.Title, taskID)
		return entry, true, nil
	}
	return StuckCompaction{}, false, nil
}

// AdoptOptions are what `workbook compact --adopt` asks of AdoptCompaction.
type AdoptOptions struct {
	// DiscardLocal takes origin's tip instead of retrying the replay, dropping
	// the local history that could not be combined with it.
	DiscardLocal bool
	// BundlePath is where DiscardLocal saves the local history first. Empty
	// writes no bundle.
	BundlePath string
	// RunID, Flags and Now go into the bundle's manifest.
	RunID string
	Flags []string
	Now   time.Time
}

// CompactionAdoption is what AdoptCompaction did to one task.
type CompactionAdoption struct {
	Result SyncTaskResult
	// Previous is the tip the canonical ref held when the command read it, and
	// Head the tip it holds now. They are equal when nothing moved.
	Previous string
	Head     string
	// Bundle is the bundle written before a discard, or "".
	Bundle string
}

// AdoptCompaction settles one stuck task (see StuckCompactions) by hand. The
// caller fetches first, so the tracking ref is origin's current tip.
//
// Without DiscardLocal it retries what fetch tried: adopting origin's
// checkpoint with the local-only changes replayed onto it, or replaying
// origin's changes onto this clone's checkpoint. A retry that still cannot run
// moves nothing and returns the same guided detail as a CategoryConflict.
//
// With DiscardLocal it writes the local history, parked refs included, to
// BundlePath (unless that is empty), then compare-and-swaps the canonical ref
// from the tip it read to origin's tip, dropping parked refs and expiring the
// reflog, as an adoption does. For a task this clone compacted that move goes
// back to the older generation, which no head-advance guard accepts as a
// compaction: it is an explicit discard, so the caller settles the projection
// itself (Invalidate, then a fresh read).
//
// DiscardLocal also undoes a compaction this clone made that origin has not
// taken (unpublishedCompaction), in the same way: the checkpoint chain goes
// into the bundle and the ref goes back to the tip origin holds. Without
// DiscardLocal such a task is a CategoryValidation error that says to add it.
//
// A task in none of these states is a CategoryValidation error: the command
// was well formed, and the task is in the wrong state for it.
func (r *Repository) AdoptCompaction(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	options AdoptOptions,
) (CompactionAdoption, error) {
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return CompactionAdoption{}, err
	}
	if err := keys.RequireOwned(taskID); err != nil {
		return CompactionAdoption{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	stuck, err := r.StuckCompactions(ctx, config, []string{taskID})
	if err != nil {
		return CompactionAdoption{}, err
	}
	entry, found := stuck[taskID]
	if !found {
		pending, unpublished, err := r.unpublishedCompaction(ctx, config, taskID)
		if err != nil {
			return CompactionAdoption{}, err
		}
		switch {
		case !unpublished:
			return CompactionAdoption{}, core.Errorf(core.CategoryValidation, "task %s has no compaction to adopt", taskID)
		case !options.DiscardLocal:
			// There is nothing to retry: the compaction is this clone's own,
			// and origin has not refused anything fetch could settle.
			return CompactionAdoption{}, core.Errorf(core.CategoryValidation,
				"task %s has a local compaction that is not published; add --discard-local to undo it after saving it to a bundle",
				taskID)
		}
		return r.discardLocalHistory(ctx, config, pending, options)
	}
	// A checkpoint this build cannot read is not one it may take either, by
	// retry or by discard: only an upgrade settles it. Refused before anything
	// is written, in fetch's own words.
	if entry.Category == core.CategoryNewerWriter {
		return CompactionAdoption{}, core.Errorf(core.CategoryNewerWriter, "%s", entry.Detail)
	}
	if options.DiscardLocal {
		return r.discardLocalHistory(ctx, config, entry, options)
	}
	return r.retryAdoption(ctx, config, entry)
}

// unpublishedCompaction finds a compaction of taskID this clone made and origin
// has not taken: the canonical ref holds a checkpoint chain, and the tracking
// ref holds the older history it accounts for, which is exactly the tip the
// push phase would lease against (compactionLease). That is the state a
// --no-sync run leaves, and the one an origin that refuses the leased push
// leaves for good. It is not stuck in StuckCompactions' sense: the task stays
// editable, and the next push retries the lease. Discarding it is the way back
// when that push will never succeed.
//
// The pair comes back as a StuckCompaction so discardLocalHistory can take it
// as it takes a stuck one: local is the checkpoint chain, remote the tip it
// replaced or one it accounts for.
func (r *Repository) unpublishedCompaction(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
) (StuckCompaction, bool, error) {
	ref, found, err := r.taskRef(ctx, taskID)
	if err != nil || !found {
		return StuckCompaction{}, false, err
	}
	tracking, err := r.TrackingTaskHeads(ctx, config)
	if err != nil {
		return StuckCompaction{}, false, err
	}
	remoteHead := tracking[taskID]
	if remoteHead == "" || remoteHead == ref.objectID {
		return StuckCompaction{}, false, nil
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, []TaskHead{
		{TaskID: taskID, ObjectID: ref.objectID},
		{TaskID: taskID, ObjectID: remoteHead},
	})
	if err != nil {
		return StuckCompaction{}, false, err
	}
	if len(tips) != 2 || tips[0].Err != nil || tips[1].Err != nil {
		return StuckCompaction{}, false, nil
	}
	local, remote := tips[0].Snapshot, tips[1].Snapshot
	if local.Operation.HistoryGeneration == remote.Operation.HistoryGeneration {
		return StuckCompaction{}, false, nil
	}
	if _, leased, err := r.compactionLease(ctx, config, taskID, local, remoteHead); err != nil || !leased {
		return StuckCompaction{}, false, err
	}
	chains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: local.Head}})
	if err != nil {
		return StuckCompaction{}, false, err
	}
	return StuckCompaction{TaskID: taskID, local: local, remote: remote, localChain: chains[0]}, true, nil
}

// retryAdoption runs fetch's settlement of a stuck pair once more and applies
// it when it now succeeds.
func (r *Repository) retryAdoption(ctx context.Context, config core.ProjectConfig, entry StuckCompaction) (CompactionAdoption, error) {
	adoption := CompactionAdoption{Previous: entry.local.Head, Head: entry.local.Head}
	request := reconcileRequest{TaskID: entry.TaskID, Local: entry.local, Remote: entry.remote}
	dependencies, err := r.adoptionDependencies(ctx, config, request)
	if err != nil {
		return adoption, err
	}
	var outcome compactedOutcome
	switch entry.Kind {
	case StuckCompactedOnOrigin:
		outcome, err = r.adoptRemoteCheckpoint(ctx, config, dependencies, request, entry.checkpoint)
	default:
		parked, parkErr := r.nextParkedRefIndexes(ctx, config)
		if parkErr != nil {
			return adoption, parkErr
		}
		outcome, err = r.replayOntoLocalCheckpoint(ctx, config, dependencies, parked[entry.TaskID], request,
			entry.checkpoint, entry.remoteChain)
	}
	if err != nil {
		return adoption, err
	}
	if outcome.Update == nil {
		switch outcome.Result.Status {
		case SyncNeedsUpgrade:
			return adoption, core.Errorf(core.CategoryNewerWriter, "%s", outcome.Result.Detail)
		case SyncInvalid:
			return adoption, core.Errorf(core.CategoryCorruptData, "%s", outcome.Result.Detail)
		}
		return adoption, core.Errorf(core.CategoryConflict, "%s", entry.Detail)
	}
	if err := r.updateCanonicalRefs(ctx, config, []canonicalRefUpdate{*outcome.Update}); err != nil {
		return adoption, err
	}
	if outcome.Update.NoPark {
		// As after fetch's own adoption: the refs are right, and only
		// reclaiming the space waits on a failure here.
		_ = r.ExpireTaskReflog(ctx, entry.TaskID)
	}
	adoption.Result = outcome.Result
	adoption.Head = outcome.Update.Next
	return adoption, nil
}

// discardLocalHistory saves the local history to a bundle and moves the
// canonical ref to origin's tip.
func (r *Repository) discardLocalHistory(
	ctx context.Context,
	config core.ProjectConfig,
	entry StuckCompaction,
	options AdoptOptions,
) (CompactionAdoption, error) {
	adoption := CompactionAdoption{Previous: entry.local.Head, Head: entry.local.Head}
	if options.BundlePath != "" {
		manifest := ArchiveManifest{
			ProjectID: config.ProjectID,
			RunID:     options.RunID,
			CreatedAt: options.Now,
			Flags:     options.Flags,
			Tasks: []ArchiveTask{{
				TaskID:        entry.TaskID,
				Title:         entry.local.State.Task.Title,
				OldTip:        entry.local.Head,
				OldGeneration: entry.local.Operation.HistoryGeneration,
				NewGeneration: entry.remote.Operation.HistoryGeneration,
				Commits:       len(entry.localChain),
			}},
		}
		// WriteArchiveBundle refuses a ref that moved since it was read, so
		// the bundle holds exactly the tip the move below swaps from.
		if err := r.WriteArchiveBundle(ctx, config, options.BundlePath, manifest); err != nil {
			return adoption, err
		}
		adoption.Bundle = options.BundlePath
	}
	update := canonicalRefUpdate{
		TaskID:     entry.TaskID,
		Next:       entry.remote.Head,
		Expected:   entry.local.Head,
		NoPark:     true,
		DropParked: true,
	}
	if err := r.updateCanonicalRefs(ctx, config, []canonicalRefUpdate{update}); err != nil {
		return adoption, err
	}
	_ = r.ExpireTaskReflog(ctx, entry.TaskID)
	adoption.Head = entry.remote.Head
	detail := "took " + r.remote() + "'s version without saving the local history"
	if adoption.Bundle != "" {
		detail = "took " + r.remote() + "'s version; the local history is saved in " + adoption.Bundle
	}
	adoption.Result = SyncTaskResult{TaskID: entry.TaskID, Status: SyncDiscardedLocal, Detail: detail}
	return adoption, nil
}

// adoptionDependencies is fetchedDependencies for one settlement outside a
// fetch: every active task's edges from its canonical tip, with origin's for
// the task being settled.
func (r *Repository) adoptionDependencies(
	ctx context.Context,
	config core.ProjectConfig,
	request reconcileRequest,
) (map[string][]string, error) {
	refs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return nil, err
	}
	heads := make([]TaskHead, 0, len(refs))
	for _, ref := range refs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}
	state := fetchState{Canonical: make(map[string]core.Snapshot, len(tips))}
	for _, tip := range tips {
		if tip.Err == nil {
			state.Canonical[tip.Head.TaskID] = tip.Snapshot
		}
	}
	return fetchedDependencies(state, nil, []reconcileRequest{request}), nil
}
