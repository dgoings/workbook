package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

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

func (r *Repository) remoteRefHead(ctx context.Context, ref string) (string, error) {
	output, err := r.Git(ctx, nil, "ls-remote", "--refs", "origin", ref)
	if err != nil {
		return "", err
	}
	if len(output) == 0 {
		return "", nil
	}
	if output[len(output)-1] != '\n' {
		return "", core.Errorf(core.CategoryOperational, "Git returned an unterminated remote ref record")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != 1 {
		return "", core.Errorf(core.CategoryOperational, "Git returned multiple records for remote ref %q", ref)
	}
	fields := bytes.Fields(lines[0])
	if len(fields) != 2 || string(fields[1]) != ref {
		return "", core.Errorf(core.CategoryOperational, "Git returned an invalid remote ref record")
	}
	return string(fields[0]), nil
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

	fetched, fetchErr := r.Fetch(ctx, config)
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

	pushed, pushErr := r.Push(ctx, config)
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
// namespace, validates every tip, then creates or fast-forwards compatible
// canonical refs with compare-and-swap updates.
func (r *Repository) Fetch(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	result := SyncResult{Remote: "origin", Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}

	refspec := "+" + taskRefPrefix + "*:" + remoteTaskRefPrefix + "*"
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "origin", refspec); err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}

	refs, err := r.listRefs(ctx, remoteTaskRefPrefix)
	if err != nil {
		return failedSyncPhase(result, "fetch failed before completion", err)
	}
	invalid := 0
	for _, remote := range refs {
		item := SyncTaskResult{TaskID: remote.taskID}
		if err := r.validateFetchedHistory(ctx, config, remote); err != nil {
			item.Status = SyncInvalid
			item.Detail = err.Error()
			result.Tasks = append(result.Tasks, item)
			invalid++
			continue
		}

		local, found, err := r.taskRef(ctx, remote.taskID)
		if err != nil {
			return failedSyncPhase(result, "fetch failed before completion", err)
		}
		if !found {
			if err := r.updateCanonicalRef(ctx, remote.taskID, remote.objectID, ""); err != nil {
				return failedSyncPhase(result, "fetch failed before completion", err)
			}
			item.Status = SyncCreated
			result.Tasks = append(result.Tasks, item)
			continue
		}
		if _, err := r.readTip(ctx, config, local.taskID, local.objectID); err != nil {
			return failedSyncPhase(result, "fetch failed before completion", err)
		}
		if local.objectID == remote.objectID {
			item.Status = SyncUnchanged
		} else {
			localBeforeRemote, err := r.isAncestor(ctx, local.objectID, remote.objectID)
			if err != nil {
				return failedSyncPhase(result, "fetch failed before completion", err)
			}
			switch {
			case localBeforeRemote:
				if err := r.updateCanonicalRef(ctx, remote.taskID, remote.objectID, local.objectID); err != nil {
					return failedSyncPhase(result, "fetch failed before completion", err)
				}
				item.Status = SyncFastForwarded
			default:
				remoteBeforeLocal, err := r.isAncestor(ctx, remote.objectID, local.objectID)
				if err != nil {
					return failedSyncPhase(result, "fetch failed before completion", err)
				}
				if remoteBeforeLocal {
					item.Status = SyncLocalAhead
				} else {
					item.Status = SyncDiverged
				}
			}
		}
		result.Tasks = append(result.Tasks, item)
	}
	result.Status = SyncPhaseCompleted
	if invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "%d fetched task ref(s) failed validation", invalid)
	}
	return result, nil
}

func (r *Repository) validateFetchedHistory(
	ctx context.Context,
	config core.ProjectConfig,
	remote taskRefRecord,
) error {
	return r.validateHistory(ctx, config, remote, remoteTaskRefPrefix+remote.taskID)
}

func (r *Repository) validateHistory(
	ctx context.Context,
	config core.ProjectConfig,
	record taskRefRecord,
	refName string,
) error {
	output, err := r.Git(ctx, nil, "rev-list", "--reverse", record.objectID)
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot enumerate task history", err)
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return core.Errorf(core.CategoryCorruptData, "Git returned an invalid task history")
	}
	commits := strings.Fields(string(output))
	if len(commits) == 0 || commits[len(commits)-1] != record.objectID {
		return core.Errorf(core.CategoryCorruptData, "task history does not end at its ref")
	}

	var parent *core.StateDocument
	for _, commit := range commits {
		snapshot, err := r.readTipAtRef(ctx, config, record.taskID, commit, refName)
		if err != nil {
			return err
		}
		if err := core.ValidateCheckpoint(parent, snapshot.Operation, snapshot.State, config.Key); err != nil {
			return err
		}
		state := snapshot.State
		parent = &state
	}
	return nil
}

func (r *Repository) refStillAt(ctx context.Context, taskID, expected string) bool {
	current, found, err := r.taskRef(ctx, taskID)
	return err == nil && found && current.objectID == expected
}

func (r *Repository) listRefs(ctx context.Context, prefix string) ([]taskRefRecord, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated ref record")
	}

	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	refs := make([]taskRefRecord, 0, len(lines))
	for _, line := range lines {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid ref record")
		}
		refName := string(parts[0])
		if !strings.HasPrefix(refName, prefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside %q", prefix)
		}
		taskID := strings.TrimPrefix(refName, prefix)
		if taskID == "" || strings.Contains(taskID, "/") {
			return nil, core.Errorf(core.CategoryCorruptData, "ref %q does not name one task", refName)
		}
		refs = append(refs, taskRefRecord{taskID: taskID, objectID: string(parts[1])})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].taskID < refs[j].taskID })
	return refs, nil
}

func (r *Repository) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := r.Git(ctx, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (r *Repository) updateCanonicalRef(ctx context.Context, taskID, next, expected string) error {
	ref := taskRefPrefix + taskID
	if _, err := r.Git(ctx, nil, "check-ref-format", ref); err != nil {
		return core.Wrap(core.CategoryCorruptData, "fetched task ref is invalid", err)
	}
	if err := r.rejectSymbolicTaskRef(ctx, ref); err != nil {
		return err
	}
	if _, err := r.Git(
		ctx,
		nil,
		"update-ref",
		"--no-deref",
		"--create-reflog",
		"-m",
		"workbook: fetch origin",
		ref,
		next,
		expected,
	); err != nil {
		return core.Wrap(core.CategoryStaleWrite, "task ref changed during fetch", err)
	}
	return nil
}
