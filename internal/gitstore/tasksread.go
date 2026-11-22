package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/dgoings/workbook/internal/core"
)

// A remote named with --remote that is not the project's tasks remote is read
// and never folded in. Its Workbook refs land in its own tracking namespace,
// refs/workbook/remotes/<remote>/, and nothing else in this checkout moves: no
// task ref is fast-forwarded or replayed, nothing is parked, the identity and
// configuration refs stay where they are, and nothing is published to it.
//
// Folding such a remote in is what made two places unsafe to alternate
// between. Their histories for one task share operations under different
// commits once either has replayed the other's, so an ordinary reconciliation
// against the other place replays operations this checkout already holds,
// and every round trip between the two wrote them again. MoveTasks is the one
// thing that carries tasks from one remote to another, and it does so by
// operation identity (see carryFromTracking).

// readOnlyRemote reports a handle pointed at a remote other than the project's
// tasks remote, as --remote and the move's reads of the old place do.
func (r *Repository) readOnlyRemote() bool {
	r.metadataMu.RLock()
	override := r.remoteOverride
	r.metadataMu.RUnlock()
	return override != "" && override != r.configuredRemote()
}

func readOnlyRemoteDetail(remote string) string {
	return fmt.Sprintf("push skipped: %s is not this project's tasks remote, so it was only read into %s; "+
		"workbook move-tasks is what carries tasks from one remote to another", remote, remoteTrackingPrefix(remote))
}

// readOnlyRemotePublication refuses publishing to a remote other than the
// project's tasks remote.
func readOnlyRemotePublication(remote, tasksRemote string) error {
	return core.TasksRemoteError(core.CategoryValidation,
		core.TasksRemoteRefusal{Reason: core.TasksRemoteReadOnly, Remote: remote, Command: "workbook push"},
		"%s is not this project's tasks remote (%s), and nothing is published to another remote: "+
			"workbook fetch and sync --remote %s read it, workbook push publishes to %s, "+
			"and workbook move-tasks carries tasks from one remote to another",
		remote, tasksRemote, remote, tasksRemote)
}

// fetchTracking reads a remote other than the project's tasks remote into its
// tracking namespace and reports what it holds, task by task, against this
// checkout's own refs. The identity is compared and never adopted or
// published, so a remote holding another project is still refused, and a
// remote marked as the place the tasks moved away from is reported as such.
func (r *Repository) fetchTracking(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	remote := r.remote()
	result := SyncResult{Remote: remote, Tasks: []SyncTaskResult{}}
	prefix := r.remoteTaskRefPrefix()
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", remote,
		"+"+taskRefPrefix+"*:"+prefix+"*",
		r.identityFetchRefspec(),
		r.configFetchRefspec(),
	); err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	r.metadataMu.Lock()
	r.trackedIdentity = trackedIdentityHead{}
	r.metadataMu.Unlock()

	identity, err := r.compareTrackedIdentity(ctx)
	result.Identity = identity
	if err != nil {
		return failedSyncPhase(result, "fetch stopped before tasks were read", err)
	}

	keys, err := r.keySet(ctx, config)
	if err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	canonicalRefs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	trackingRefs, ignored, err := r.listOwnedTaskRefs(ctx, config, prefix)
	if err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	result.Ignored = ignored
	local := make(map[string]string, len(canonicalRefs))
	for _, ref := range canonicalRefs {
		local[ref.taskID] = ref.objectID
	}
	heads := make([]TaskHead, 0, len(trackingRefs))
	for _, ref := range trackingRefs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	invalid := 0
	for _, tip := range tips {
		taskID := tip.Head.TaskID
		if tip.Err != nil {
			if projectID, foreign := foreignProjectOf(tip.Err); foreign {
				result.Ignored = append(result.Ignored, r.ignoredForeignProjectRef(keys, config, taskID, projectID))
				continue
			}
			invalid++
			result.Tasks = append(result.Tasks, SyncTaskResult{TaskID: taskID, Status: SyncInvalid, Detail: tip.Err.Error()})
			continue
		}
		if local[taskID] == tip.Head.ObjectID {
			result.Tasks = append(result.Tasks, SyncTaskResult{TaskID: taskID, Status: SyncUnchanged})
			continue
		}
		result.Tasks = append(result.Tasks, SyncTaskResult{
			TaskID: taskID,
			Status: SyncTracked,
			Detail: "read into " + prefix + taskID + "; this checkout's task ref is unchanged",
		})
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].TaskID < result.Tasks[j].TaskID })
	sort.Slice(result.Ignored, func(i, j int) bool { return result.Ignored[i].Ref < result.Ignored[j].Ref })
	result.Status = SyncPhaseCompleted
	if invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "%d fetched task ref(s) failed validation", invalid)
	}
	return result, nil
}

// compareTrackedIdentity compares the identity a read-only fetch brought into
// the tracking namespace with this checkout's own, adopting and publishing
// nothing. It reports a marked remote as moved and records the marker, so a
// publication on the same handle is refused.
func (r *Repository) compareTrackedIdentity(ctx context.Context) (*SyncIdentityResult, error) {
	tracking := r.remoteIdentityRef()
	listing, err := r.listIdentityRefs(ctx, identityRef, tracking)
	if err != nil {
		return nil, err
	}
	localHead, found := listing.Heads[identityRef]
	if !found {
		return nil, core.Errorf(core.CategoryCorruptData, "%s disappeared during synchronization; rerun the command", identityRef)
	}
	remoteHead, found := listing.Heads[tracking]
	if !found || remoteHead == localHead {
		return nil, nil
	}
	local, err := r.identityRecordAt(ctx, identityRef, localHead)
	if err != nil {
		return nil, err
	}
	remote, err := r.identityRecordAt(ctx, tracking, remoteHead)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(remote.Document, local.Document) {
		result := &SyncIdentityResult{
			Status: SyncIdentityMismatched,
			Head:   localHead,
			Detail: fmt.Sprintf("this clone is project %s (key %s); %s is project %s (key %s)",
				local.Identity.ProjectID, local.Identity.Key, r.remote(), remote.Identity.ProjectID, remote.Identity.Key),
		}
		return result, r.originIdentityMismatch(local, remote)
	}
	if remote.Moved == nil {
		return nil, nil
	}
	r.metadataMu.Lock()
	moved := *remote.Moved
	r.originMoved = &moved
	r.metadataMu.Unlock()
	return &SyncIdentityResult{
		Status: SyncIdentityMoved,
		Head:   localHead,
		Detail: fmt.Sprintf("%s is marked as the place this project's tasks moved away from, to %s (remote %q); "+
			"it was read, and nothing is published to it", r.remote(), moved.URL, moved.Remote),
	}, nil
}
