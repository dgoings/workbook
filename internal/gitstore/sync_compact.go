package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// The two ways origin can refuse a leased checkpoint, in the words the person
// who ran the push has to act on. Git's own text ("[rejected] (stale info)",
// "[remote rejected] (non-fast-forward)") describes the mechanism, not what
// happened to the compaction.
const (
	// It names no Git setting: receive.denyNonFastForwards covers refs/heads/
	// only, so it is never why origin refuses a task ref.
	compactionDeniedDetail = "origin refuses non-fast-forward updates to Workbook refs " +
		"(for example, a server-side hook or ruleset); the compacted task stays local until that changes"
	compactionStaleDetail = "origin changed since the compaction; the next sync reconciles it"
)

// unrelatedHistoryDetail reports a local checkpoint whose task origin holds a
// history it shares nothing with. Publishing would replace that history, so
// the checkpoint stays local.
const unrelatedHistoryDetail = "origin has an unrelated history for this task; it was not published"

// compactedOnOriginDetail is what fetch says about a task origin compacted
// whose local-only changes it could not replay onto the checkpoint.
func compactedOnOriginDetail(title, taskID string, unpushed int) string {
	return fmt.Sprintf(`"%s" was compacted on origin; you have %d unpushed changes on the old history. `+
		`Run "workbook compact --adopt %s" to retry, `+
		`or add --discard-local to take origin's version after saving yours to a bundle.`,
		title, unpushed, taskID)
}

// cannotCombineDetail is what fetch says about a task this clone deleted on
// its checkpoint while origin changed the old history: nothing applies to a
// deleted task, so origin's change cannot win the way an edit's does.
func cannotCombineDetail(title, taskID string) string {
	return fmt.Sprintf(`"%s" changed on origin after you compacted it, and the two can't be combined. `+
		`Run "workbook compact --adopt %s --discard-local" to take origin's version `+
		`after saving yours to a bundle; then compact it again.`,
		title, taskID)
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

// pushTaskHeads publishes candidates in one non-atomic push and returns one
// outcome per task. Every refspec is plain. A leased ref is also named in
// --force-with-lease=<ref>:<expected>, which is what lets Git move it backward,
// and only while origin still holds expected.
//
// The refspec must not carry `+`. A forcing refspec overrides the lease: Git
// (2.54 checked) then updates the ref whatever origin holds, which is exactly
// the unconditional force-push this path exists to avoid.
//
// After origin accepts a leased ref, its tracking ref is moved to the
// checkpoint. No other push path touches tracking refs, but a stale one here
// would keep the compacted history reachable in this clone. A failure to move
// it is left for the next fetch, which overwrites tracking refs anyway.
func (r *Repository) pushTaskHeads(ctx context.Context, candidates []pushCandidate) (map[string]SyncTaskResult, error) {
	expected := make(map[string]string, len(candidates))
	forced := make(map[string]bool)
	args := []string{"push", "--porcelain"}
	for _, candidate := range candidates {
		refName := taskRefPrefix + candidate.TaskID
		expected[refName] = candidate.TaskID
		if candidate.Lease != nil {
			forced[refName] = true
			args = append(args, "--force-with-lease="+refName+":"+candidate.Lease.ExpectedRemote)
		}
	}
	args = append(args, "origin")
	for _, candidate := range candidates {
		args = append(args, candidate.Head+":"+taskRefPrefix+candidate.TaskID)
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
	pushed, err := parsePushPorcelain(push.stdout, expected, forced, push.err)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.Lease != nil && pushed[candidate.TaskID].Status == SyncCompactionPublished {
			_ = r.advanceTrackingRef(ctx, candidate.TaskID, candidate.Lease.ExpectedRemote, candidate.Lease.Local)
		}
	}
	return pushed, nil
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
	ref := remoteTaskRefPrefix + taskID
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

// leasedRejectionDetail translates Git's refusal of a leased ref.
func leasedRejectionDetail(detail string) string {
	switch {
	case strings.Contains(detail, "stale info"):
		return compactionStaleDetail
	case strings.Contains(detail, "non-fast-forward"):
		return compactionDeniedDetail
	default:
		return detail
	}
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
	result := SyncResult{Remote: "origin", Tasks: []SyncTaskResult{}}
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
	if err := r.ensureOriginIdentityAgreement(ctx, nil); err != nil {
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
	if len(candidates) != 0 {
		pushed, err := r.pushTaskHeads(ctx, candidates)
		if err != nil {
			return failedPushTasks(result, ordered, items, "push failed before completion", err)
		}
		for taskID, item := range pushed {
			items[taskID] = item
		}
	}

	rejected := 0
	for _, taskID := range ordered {
		if items[taskID].Status == SyncRejected {
			rejected++
		}
		result.Tasks = append(result.Tasks, items[taskID])
	}
	result.Status = SyncPhaseCompleted
	if invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalid)
	}
	if rejected > 0 {
		return result, core.Errorf(core.CategoryOperational, "%d task ref(s) were rejected by origin", rejected)
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
		localCheckpoint, _ := localChain[0].Operation.Checkpoint()
		remoteCheckpoint, _ := remoteChain[0].Operation.Checkpoint()
		// A local checkpoint written from origin's generation means origin is
		// the older side, whatever its own root is.
		localFromRemote := localCheckpoint != nil &&
			localCheckpoint.PreviousGeneration == request.Remote.Operation.HistoryGeneration
		switch {
		case remoteCheckpoint != nil && !localFromRemote:
			return r.adoptRemoteCheckpoint(ctx, config, dependencies, request, remoteCheckpoint)
		case localCheckpoint != nil:
			return r.replayOntoLocalCheckpoint(ctx, config, dependencies, parkedIndex, request, localCheckpoint, remoteChain)
		}
	}
	return compactedOutcome{
		TaskID:   request.TaskID,
		Result:   SyncTaskResult{TaskID: request.TaskID, Status: SyncInvalid, Detail: generationMismatchDetail},
		Snapshot: request.Local,
		Kind:     compactedInvalid,
	}, nil
}

// adoptRemoteCheckpoint moves the canonical ref onto origin's checkpoint. The
// local-only operations are those on local history that are neither in the
// checkpoint's list nor on origin's chain; with none, the ref moves to origin's
// tip, and otherwise they are replayed onto it in its generation. The old tip
// is neither parked nor left in the reflog, since either would keep the
// compacted history alive. A replay that cannot run cleanly leaves the ref
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
		TaskID:     request.TaskID,
		Next:       request.Remote.Head,
		Expected:   request.Local.Head,
		NoPark:     true,
		DropParked: true,
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
			Detail: compactedOnOriginDetail(request.Local.State.Task.Title, request.TaskID, unpushed),
		}
		// The task's own state, reported on its line every run, rather
		// than a failed synchronization: everything else fetched and
		// published, and the watcher must stay trustworthy while one task
		// waits for somebody to adopt it by hand.
		outcome.Withhold = true
		return outcome, nil
	}
	if refuseNewerWriterReplay(request) != nil {
		return fallBack()
	}
	replay, failure, err := r.replayOperations(ctx, config, request.TaskID, request.Remote, localOnly, false,
		request.Remote.Operation.HistoryGeneration, dependencies)
	if err != nil {
		return outcome, err
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
		Detail: fmt.Sprintf("replayed %d local operation(s) onto origin's checkpoint", replay.replayed),
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
		outcome.Result = SyncTaskResult{TaskID: request.TaskID, Status: SyncInvalid, Detail: unrelatedHistoryDetail}
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
		request.Local.Operation.HistoryGeneration, dependencies)
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
			Detail: cannotCombineDetail(request.Local.State.Task.Title, request.TaskID),
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
			Detail: fmt.Sprintf("replayed %d operation(s) from origin onto the local checkpoint", replay.replayed),
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
