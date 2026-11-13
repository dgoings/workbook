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

// leasedPush describes one ref pushed as a deliberate non-fast-forward.
type leasedPush struct{ TaskID, Local, ExpectedRemote string }

// compactionLease decides whether publishing local over origin's remoteHead is
// the deliberate non-fast-forward a compaction makes.
//
// It is one when local's history begins at a checkpoint (its state carries the
// compactedFrom the checkpoint recorded) and origin's tip is not already on
// that history. The lease is always on compactedFrom, the tip the checkpoint
// replaced: the push succeeds only while origin still holds exactly that tip.
// When origin has moved on since, the lease fails as stale and Git refuses the
// push without touching origin; reconciling that move is fetch's job.
//
// Every other case is an ordinary push and gets no lease: a task that was never
// compacted, one origin does not hold yet, and a compacted one whose checkpoint
// origin already has, which local now extends by a fast-forward.
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
	lease := leasedPush{TaskID: taskID, Local: local.Head, ExpectedRemote: *compactedFrom}
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
	return lease, true, nil
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
			_ = r.advanceTrackingRef(ctx, candidate.TaskID, candidate.Lease.Local)
		}
	}
	return pushed, nil
}

// advanceTrackingRef compare-and-swaps a task's tracking ref from the value it
// holds now to next. A reflog entry for the old value would keep the compacted
// history reachable just as the ref did, so the update asks for none. That
// holds only under Git's default core.logAllRefUpdates=true, which logs
// refs/heads/, refs/remotes/ and refs/notes/ but not refs/workbook/; a clone
// set to `always` keeps a reflog for the tracking ref, and that reflog holds
// the old history until it expires.
func (r *Repository) advanceTrackingRef(ctx context.Context, taskID, next string) error {
	ref := remoteTaskRefPrefix + taskID
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return err
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	if current := strings.TrimSpace(string(output)); current == "" {
		fmt.Fprintf(&input, "create %s %s\n", ref, next)
	} else {
		fmt.Fprintf(&input, "update %s %s %s\n", ref, next, current)
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
// the tip its checkpoint replaced. Git's own rules guard the rest.
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
