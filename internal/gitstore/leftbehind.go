package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// LeftBehind reports operations the place the tasks moved away from holds and
// this checkout does not: edits published there in the moment around the
// mark, or by a checkout that kept publishing there some other way.
type LeftBehind struct {
	// Remote is the old place.
	Remote string `json:"remote"`
	// Operations counts the operations, and Tasks names their tasks.
	Operations int      `json:"operations"`
	Tasks      []string `json:"tasks"`
}

// Warning restates the report for a command's warnings.
func (left LeftBehind) Warning(tasksRemote string) core.Warning {
	return core.Warning{
		Code: "tasks-left-behind",
		Message: left.Remote + ", which the tasks moved away from, holds " + plural(left.Operations, "operation") +
			" on " + strings.Join(left.Tasks, ", ") + " that " + tasksRemote +
			" lacks; run workbook move-tasks, which carries each of them once",
	}
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// FindLeftBehind asks origin, in a checkout whose tasks are shared through
// another remote, whether it is marked as the place they moved away from and
// holds operations this checkout lacks, and reports them. It returns nil when
// there is nothing to say, and when it cannot tell: it is a notice, not a
// guard, and a listing that fails says nothing either way.
//
// It costs one listing of origin when the tasks have not moved or nothing
// there changed since this checkout last looked. Otherwise it fetches origin's
// task refs into origin's tracking namespace, which is all reading the old
// place ever writes (see tasksread.go), and reads the histories of the tasks
// whose tips differ from this checkout's, in two batches.
func (r *Repository) FindLeftBehind(ctx context.Context, config core.ProjectConfig) *LeftBehind {
	const old = core.DefaultTasksRemote
	if r.configuredRemote() == old || !r.hasGitRemote(ctx, old) {
		return nil
	}
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", old, identityRef, taskRefPrefix+"*")
	if err != nil {
		return nil
	}
	remoteTips := make(map[string]string)
	identityHead := ""
	for _, line := range strings.Split(string(bytes.TrimSpace(listing)), "\n") {
		objectID, name, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		if name == identityRef {
			identityHead = objectID
		} else if taskID, ok := strings.CutPrefix(name, taskRefPrefix); ok {
			remoteTips[taskID] = objectID
		}
	}
	if identityHead == "" || len(remoteTips) == 0 {
		return nil
	}
	tracking := remoteTaskRefPrefixFor(old)
	trackedTips, err := r.trackingRefHeads(ctx, tracking)
	if err != nil {
		return nil
	}
	trackedIdentity, _ := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", remoteIdentityRefFor(old))
	stale := strings.TrimSpace(string(trackedIdentity)) != identityHead || len(trackedTips) != len(remoteTips)
	for taskID, head := range remoteTips {
		if trackedTips[taskID] != head {
			stale = true
		}
	}
	if stale {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", old,
			"+"+taskRefPrefix+"*:"+tracking+"*", identityFetchRefspecFor(old)); err != nil {
			return nil
		}
	}
	record, found, err := r.readIdentityRef(ctx, remoteIdentityRefFor(old))
	if err != nil || !found || record.Moved == nil {
		return nil
	}

	canonical, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return nil
	}
	local := make(map[string]string, len(canonical))
	for _, ref := range canonical {
		local[ref.taskID] = ref.objectID
	}
	var differing []string
	for taskID, head := range remoteTips {
		if local[taskID] != head {
			differing = append(differing, taskID)
		}
	}
	if len(differing) == 0 {
		return nil
	}
	sort.Strings(differing)
	var theirHeads, ourHeads []TaskHead
	var ourTasks []string
	for _, taskID := range differing {
		theirHeads = append(theirHeads, TaskHead{TaskID: taskID, ObjectID: remoteTips[taskID]})
		if head, found := local[taskID]; found {
			ourHeads = append(ourHeads, TaskHead{TaskID: taskID, ObjectID: head})
			ourTasks = append(ourTasks, taskID)
		}
	}
	theirs, err := r.completeTaskChains(ctx, config, theirHeads)
	if err != nil {
		return nil
	}
	ours, err := r.completeTaskChains(ctx, config, ourHeads)
	if err != nil {
		return nil
	}
	accounts := make(map[string]operationAccount, len(ourTasks))
	for index, taskID := range ourTasks {
		if ours[index] == nil {
			return nil
		}
		var compacted []string
		if checkpoint, ok := ours[index][0].Operation.Checkpoint(); ok {
			compacted = checkpoint.CompactedOperations
		}
		accounts[taskID] = newOperationAccount(compacted, ours[index])
	}
	parked, err := r.parkedTaskHeads(ctx, config)
	if err != nil {
		return nil
	}
	left := &LeftBehind{Remote: old}
	for index, taskID := range differing {
		if theirs[index] == nil {
			continue
		}
		account := accounts[taskID]
		aside, err := r.parkedAccount(ctx, config, taskID, parked[taskID])
		if err != nil {
			return nil
		}
		lacking := 0
		for _, commit := range theirs[index] {
			if account.covers(commit) || aside.covers(commit) {
				continue
			}
			lacking += len(commit.Operation.Operations)
		}
		if lacking > 0 {
			left.Operations += lacking
			left.Tasks = append(left.Tasks, taskID)
		}
	}
	if left.Operations == 0 {
		return nil
	}
	return left
}
