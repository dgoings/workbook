package gitstore

import (
	"context"
	"fmt"
	"sort"

	"github.com/dgoings/workbook/internal/core"
)

const remoteTaskRefPrefix = "refs/workbook/remotes/origin/tasks/"

type SyncStatus string
type SyncPhaseStatus string

const (
	SyncCreated       SyncStatus = "created"
	SyncFastForwarded SyncStatus = "fast-forwarded"
	SyncUnchanged     SyncStatus = "unchanged"
	SyncLocalAhead    SyncStatus = "local-ahead"
	SyncDiverged      SyncStatus = "diverged"
	SyncInvalid       SyncStatus = "invalid"
	SyncPublished     SyncStatus = "published"
	SyncUpToDate      SyncStatus = "up-to-date"
	SyncRejected      SyncStatus = "rejected"
	SyncLocalChanged  SyncStatus = "local-changed"
)

const (
	SyncPhaseCompleted SyncPhaseStatus = "completed"
	SyncPhaseFailed    SyncPhaseStatus = "failed"
	SyncPhaseSkipped   SyncPhaseStatus = "skipped"
)

type SyncTaskResult struct {
	TaskID string     `json:"taskId"`
	Status SyncStatus `json:"status"`
	Detail string     `json:"detail,omitempty"`
}

type SyncRunResult struct {
	Remote string     `json:"remote"`
	Fetch  SyncResult `json:"fetch"`
	Push   SyncResult `json:"push"`
}

// fetchState is the single validated view that Fetch produces for Sync. It is
// deliberately private: callers receive the stable SyncResult while Sync can
// reuse the canonical and tracking tips without inspecting them again.
type fetchState struct {
	Canonical map[string]core.Snapshot
	Tracking  map[string]core.Snapshot
	Outcomes  map[string]SyncTaskResult
}

// Push publishes validated local Workbook task refs to origin without force or
// deletion. One non-atomic publication preserves per-ref outcomes so a
// rejection does not prevent unrelated tasks from publishing.
func (r *Repository) Push(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	result := SyncResult{Remote: "origin", Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	refs, err := r.listTaskRefs(ctx)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].taskID < refs[j].taskID })
	if len(refs) == 0 {
		result.Status = SyncPhaseCompleted
		return result, nil
	}

	heads := make([]TaskHead, len(refs))
	for i, ref := range refs {
		heads[i] = TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID}
	}
	partial, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	items := make(map[string]SyncTaskResult, len(refs))
	observed := make(map[string]string, len(refs))
	valid := make(map[string]struct{}, len(refs))
	invalid := 0
	for _, tip := range partial {
		observed[tip.Head.TaskID] = tip.Head.ObjectID
		if tip.Err != nil {
			items[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
			invalid++
			continue
		}
		valid[tip.Head.TaskID] = struct{}{}
	}

	remoteOutput, err := r.Git(ctx, nil, "ls-remote", "--refs", "origin", taskRefPrefix+"*")
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	remoteHeads, err := r.parseRemoteTaskHeads(config, remoteOutput)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	expected := make(map[string]string, len(valid))
	for _, ref := range refs {
		if _, ok := valid[ref.taskID]; !ok {
			continue
		}
		refName := taskRefPrefix + ref.taskID
		if remoteHeads[ref.taskID] == ref.objectID {
			items[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncUpToDate}
			continue
		}
		expected[refName] = ref.taskID
	}
	if len(expected) != 0 {
		args := []string{"push", "--porcelain", "origin"}
		for _, ref := range refs {
			refName := taskRefPrefix + ref.taskID
			if _, candidate := expected[refName]; candidate {
				args = append(args, ref.objectID+":"+refName)
			}
		}
		push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
		pushed, err := parsePushPorcelain(push.stdout, expected, push.err)
		if err != nil {
			return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
		}
		for taskID, item := range pushed {
			items[taskID] = item
		}
	}

	finalRefs, err := r.listTaskRefs(ctx)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	final := make(map[string]string, len(finalRefs))
	for _, ref := range finalRefs {
		final[ref.taskID] = ref.objectID
	}
	rejected := 0
	changed := 0
	for _, ref := range refs {
		item := items[ref.taskID]
		if item.Status == SyncRejected {
			rejected++
		} else if (item.Status == SyncPublished || item.Status == SyncUpToDate) && final[ref.taskID] != observed[ref.taskID] {
			item.Status = SyncLocalChanged
			item.Detail = "validated task head was published, but the local ref advanced during push; run workbook push again"
			items[ref.taskID] = item
			changed++
		}
		result.Tasks = append(result.Tasks, items[ref.taskID])
	}
	result.Status = SyncPhaseCompleted
	if invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalid)
	}
	if rejected > 0 {
		return result, core.Errorf(core.CategoryOperational, "%d task ref(s) were rejected by origin", rejected)
	}
	if changed > 0 {
		return result, core.Errorf(core.CategoryStaleWrite, "%d local task ref(s) changed during push", changed)
	}
	return result, nil
}

func failedPushTransport(
	result SyncResult,
	refs []taskRefRecord,
	items map[string]SyncTaskResult,
	invalid int,
	detail string,
	err error,
) (SyncResult, error) {
	result.Status = SyncPhaseFailed
	result.Detail = detail
	if err != nil {
		result.Detail += ": " + err.Error()
	}
	for _, ref := range refs {
		item, found := items[ref.taskID]
		if found && item.Status != "" {
			result.Tasks = append(result.Tasks, item)
		}
	}
	if invalid > 0 {
		return result, core.Wrap(
			core.CategoryCorruptData,
			fmt.Sprintf("%d local task ref(s) failed validation before transport completed", invalid),
			err,
		)
	}
	return result, err
}

// PushTask publishes one validated task ref to origin.
//
// It deliberately issues no remote listing. Git's own non-fast-forward rule is
// already the remote race guard, and push porcelain reports the up-to-date
// outcome that the full Push uses ls-remote to discover. That makes the cost of
// publishing one task constant in the number of tasks a project holds.
//
// Only the named tip is validated, so an unrelated malformed ref cannot block
// an unrelated mutation. The full validation sweep remains in Push.
func (r *Repository) PushTask(ctx context.Context, config core.ProjectConfig, taskID string) (SyncTaskResult, error) {
	result := SyncTaskResult{TaskID: taskID}
	if err := r.verifyIdentity(ctx); err != nil {
		return result, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return result, err
	}
	ref, found, err := r.taskRef(ctx, taskID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, core.Errorf(core.CategoryNotFound, "task %s has no Workbook ref to publish", taskID)
	}

	tips, err := r.readTaskHeadsPartial(ctx, config, []TaskHead{{TaskID: ref.taskID, ObjectID: ref.objectID}})
	if err != nil {
		return result, err
	}
	if len(tips) != 1 {
		return result, core.Errorf(core.CategoryOperational, "tip validation returned %d results, want 1", len(tips))
	}
	if tips[0].Err != nil {
		result.Status = SyncInvalid
		result.Detail = tips[0].Err.Error()
		return result, core.Errorf(core.CategoryCorruptData, "local task ref %s failed validation", taskID)
	}

	refName := taskRefPrefix + taskID
	expected := map[string]string{refName: taskID}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", "origin", ref.objectID+":"+refName)
	published, err := parsePushPorcelain(push.stdout, expected, push.err)
	if err != nil {
		return result, err
	}
	result = published[taskID]
	if result.Status == SyncRejected {
		return result, core.Errorf(core.CategoryStaleWrite,
			"task ref %s was rejected by origin; fetch and reconcile before publishing again", taskID)
	}
	return result, nil
}

type SyncResult struct {
	Remote string           `json:"remote"`
	Status SyncPhaseStatus  `json:"status,omitempty"`
	Detail string           `json:"detail,omitempty"`
	Tasks  []SyncTaskResult `json:"tasks"`
}

// Sync performs the POC-safe synchronization sequence: fetch and validate
// origin's Workbook refs, stop if any task history diverged or failed
// validation, then publish local Workbook task refs without touching code refs.
func (r *Repository) Sync(ctx context.Context, config core.ProjectConfig) (SyncRunResult, error) {
	result := SyncRunResult{
		Remote: "origin",
		Fetch:  skippedSyncPhase("fetch not run"),
		Push:   skippedSyncPhase("push not run"),
	}

	state, fetched, fetchErr := r.fetch(ctx, config)
	result.Fetch = fetched
	if fetchErr != nil {
		result.Push = skippedSyncPhase("push skipped because fetch failed")
		return result, fetchErr
	}
	diverged := countSyncStatus(fetched, SyncDiverged)
	if diverged > 0 {
		message := fmt.Sprintf("%d divergent task history(s) require reconciliation before sync can push", diverged)
		result.Push = skippedSyncPhase("push skipped because " + message)
		return result, core.Errorf(core.CategoryStaleWrite, "%s", message)
	}

	pushed, pushErr := r.publishFetched(ctx, config, state)
	result.Push = pushed
	if pushErr != nil {
		return result, pushErr
	}
	return result, nil
}

func skippedSyncPhase(detail string) SyncResult {
	return SyncResult{Remote: "origin", Status: SyncPhaseSkipped, Detail: detail, Tasks: []SyncTaskResult{}}
}

func failedSyncPhase(result SyncResult, detail string, err error) (SyncResult, error) {
	result.Status = SyncPhaseFailed
	if err != nil {
		result.Detail = detail + ": " + err.Error()
	} else {
		result.Detail = detail
	}
	return result, err
}

func countSyncStatus(result SyncResult, status SyncStatus) int {
	count := 0
	for _, task := range result.Tasks {
		if task.Status == status {
			count++
		}
	}
	return count
}

// Fetch downloads origin's Workbook task refs into an isolated tracking
// namespace, validates their current tips, then creates or fast-forwards
// compatible canonical refs with one compare-and-swap transaction.
func (r *Repository) Fetch(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	_, result, err := r.fetch(ctx, config)
	return result, err
}

func (r *Repository) fetch(ctx context.Context, config core.ProjectConfig) (fetchState, SyncResult, error) {
	state := fetchState{
		Canonical: make(map[string]core.Snapshot),
		Tracking:  make(map[string]core.Snapshot),
		Outcomes:  make(map[string]SyncTaskResult),
	}
	result := SyncResult{Remote: "origin", Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	refspec := "+" + taskRefPrefix + "*:" + remoteTaskRefPrefix + "*"
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "origin", refspec); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	canonicalRefs, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	trackingRefs, err := r.listOwnedTaskRefs(ctx, config, remoteTaskRefPrefix)
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	heads := make([]TaskHead, 0, len(canonicalRefs)+len(trackingRefs))
	for _, ref := range canonicalRefs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	for _, ref := range trackingRefs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	partial, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	invalidCanonical := 0
	invalidTracking := 0
	invalidCanonicalTasks := make(map[string]struct{})
	for index, tip := range partial {
		if index < len(canonicalRefs) {
			if tip.Err != nil {
				invalidCanonical++
				invalidCanonicalTasks[tip.Head.TaskID] = struct{}{}
				state.Outcomes[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
				continue
			}
			state.Canonical[tip.Head.TaskID] = tip.Snapshot
			continue
		}
		if tip.Err != nil {
			invalidTracking++
			state.Outcomes[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
			continue
		}
		state.Tracking[tip.Head.TaskID] = tip.Snapshot
	}

	pairs := make([]taskHeadPair, 0, len(state.Tracking))
	for _, ref := range trackingRefs {
		remote, valid := state.Tracking[ref.taskID]
		if !valid {
			continue
		}
		if _, invalid := invalidCanonicalTasks[ref.taskID]; invalid {
			continue
		}
		if local, found := state.Canonical[ref.taskID]; found {
			if local.Operation.HistoryGeneration != remote.Operation.HistoryGeneration {
				invalidTracking++
				state.Outcomes[ref.taskID] = SyncTaskResult{
					TaskID: ref.taskID,
					Status: SyncInvalid,
					Detail: "tracking and canonical tips use different history generations",
				}
				delete(state.Tracking, ref.taskID)
				continue
			}
			pairs = append(pairs, taskHeadPair{TaskID: ref.taskID, Local: local, Remote: remote})
		}
	}
	relationships, err := r.classifyTaskHeadRelationships(ctx, config, pairs)
	if err != nil {
		result.Tasks = sortedSyncOutcomes(state.Outcomes)
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	updates := make([]canonicalRefUpdate, 0, len(state.Tracking))
	plannedOutcomes := make(map[string]SyncTaskResult)
	for _, ref := range trackingRefs {
		remote, valid := state.Tracking[ref.taskID]
		if !valid {
			continue
		}
		if _, invalid := invalidCanonicalTasks[ref.taskID]; invalid {
			continue
		}
		local, found := state.Canonical[ref.taskID]
		if !found {
			updates = append(updates, canonicalRefUpdate{TaskID: ref.taskID, Next: remote.Head})
			plannedOutcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncCreated}
			continue
		}
		for _, relationship := range relationships {
			if relationship.TaskID != ref.taskID {
				continue
			}
			switch relationship.Relationship {
			case taskHeadsEqual:
				state.Outcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncUnchanged}
			case taskHeadsRemoteAhead:
				updates = append(updates, canonicalRefUpdate{TaskID: ref.taskID, Next: remote.Head, Expected: local.Head})
				plannedOutcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncFastForwarded}
			case taskHeadsLocalAhead:
				state.Outcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncLocalAhead}
			case taskHeadsDiverged:
				state.Outcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncDiverged}
			}
			break
		}
	}
	if err := r.updateCanonicalRefsFromValidated(ctx, config, canonicalRefs, updates); err != nil {
		result.Tasks = sortedSyncOutcomes(state.Outcomes)
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	for _, update := range updates {
		state.Canonical[update.TaskID] = state.Tracking[update.TaskID]
	}
	for taskID, outcome := range plannedOutcomes {
		state.Outcomes[taskID] = outcome
	}
	result.Tasks = sortedSyncOutcomes(state.Outcomes)
	result.Status = SyncPhaseCompleted
	if invalidCanonical > 0 {
		return state, result, core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalidCanonical)
	}
	if invalidTracking > 0 {
		return state, result, core.Errorf(core.CategoryCorruptData, "%d fetched task ref(s) failed validation", invalidTracking)
	}
	return state, result, nil
}

func sortedSyncOutcomes(outcomes map[string]SyncTaskResult) []SyncTaskResult {
	taskIDs := make([]string, 0, len(outcomes))
	for taskID := range outcomes {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	items := make([]SyncTaskResult, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		items = append(items, outcomes[taskID])
	}
	return items
}

// publishFetched publishes only the tips already inspected by fetch. Git's
// normal non-fast-forward rule remains the remote race guard; a second remote
// listing would both duplicate work and observe a different synchronization
// boundary.
func (r *Repository) publishFetched(ctx context.Context, config core.ProjectConfig, state fetchState) (SyncResult, error) {
	result := SyncResult{Remote: "origin", Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}

	taskIDs := make([]string, 0, len(state.Canonical))
	for taskID := range state.Canonical {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	items := make(map[string]SyncTaskResult, len(taskIDs))
	observed := make(map[string]string, len(taskIDs))
	expected := make(map[string]string, len(taskIDs))
	for _, taskID := range taskIDs {
		snapshot := state.Canonical[taskID]
		observed[taskID] = snapshot.Head
		tracking, tracked := state.Tracking[taskID]
		if !tracked || tracking.Head != snapshot.Head {
			expected[taskRefPrefix+taskID] = taskID
			continue
		}
		items[taskID] = SyncTaskResult{TaskID: taskID, Status: SyncUpToDate}
	}
	if len(expected) != 0 {
		args := []string{"push", "--porcelain", "origin"}
		for _, taskID := range taskIDs {
			refName := taskRefPrefix + taskID
			if _, candidate := expected[refName]; candidate {
				args = append(args, observed[taskID]+":"+refName)
			}
		}
		push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
		pushed, err := parsePushPorcelain(push.stdout, expected, push.err)
		if err != nil {
			return failedSyncPhase(result, "push failed before completion", err)
		}
		for taskID, item := range pushed {
			items[taskID] = item
		}
	}

	finalRefs, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	final := make(map[string]string, len(finalRefs))
	for _, ref := range finalRefs {
		final[ref.taskID] = ref.objectID
	}
	rejected := 0
	changed := 0
	for _, taskID := range taskIDs {
		item := items[taskID]
		if item.Status == SyncRejected {
			rejected++
		} else if (item.Status == SyncPublished || item.Status == SyncUpToDate) && final[taskID] != observed[taskID] {
			item.Status = SyncLocalChanged
			item.Detail = "validated task head was published, but the local ref advanced during push; run workbook push again"
			items[taskID] = item
			changed++
		}
		result.Tasks = append(result.Tasks, items[taskID])
	}
	result.Status = SyncPhaseCompleted
	if rejected > 0 {
		return result, core.Errorf(core.CategoryOperational, "%d task ref(s) were rejected by origin", rejected)
	}
	if changed > 0 {
		return result, core.Errorf(core.CategoryStaleWrite, "%d local task ref(s) changed during push", changed)
	}
	return result, nil
}
