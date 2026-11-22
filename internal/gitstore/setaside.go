package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// maxSetAsideMemo bounds the memo SetAsideEdits keeps. An entry is keyed by
// two commits, so a fresh parked tip or a task that moved simply misses; the
// bound only keeps a server that runs for months from holding every pair it
// ever answered.
const maxSetAsideMemo = 256

// SetAsideEdits reports, by task, the edits this clone's parked tips hold that
// the task's history does not: what a reconciliation set aside when it stopped
// on a conflict (see reconcileDivergentTasks).
//
// heads names each task's tip as the caller is showing it, which is what each
// parked tip is compared with. A caller drawing tasks from the projection
// passes the heads it drew, so what it reports set aside always agrees with
// the task beside it, even in the moment after a fetch has moved a ref and
// before the projection has caught up. A task heads does not name is skipped.
//
// It reads the parked refs and nothing else, so the answer outlives the
// process that parked them and needs no record of its own. A parked tip is
// compared with the task's current tip. A commit only the parked side has is
// an edit the history lacks unless every one of its operations is on the
// current side too — reconciliation keeps operation IDs when it replays —
// and an edit whose effect the task has carried since is not reported either
// (see setAside). Every other one is reported, in the order it was made.
//
// A clone with no parked refs answers with one ref listing. The answer for a
// parked tip and a task tip is a function of the two commits, so it is
// remembered and a board polling every second pays for each pair once.
func (r *Repository) SetAsideEdits(ctx context.Context, config core.ProjectConfig, heads map[string]string) (map[string][]core.SetAsideEdit, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	parked, err := r.listParkedRefs(ctx, config, reconciledRefPrefix)
	if err != nil || len(parked) == 0 {
		return nil, err
	}
	sort.Slice(parked, func(i, j int) bool {
		if parked[i].taskID != parked[j].taskID {
			return parked[i].taskID < parked[j].taskID
		}
		return parked[i].index < parked[j].index
	})

	result := make(map[string][]core.SetAsideEdit)
	for _, ref := range parked {
		tip := heads[ref.taskID]
		if tip == "" {
			continue
		}
		edits, err := r.parkedSetAside(ctx, config, ref, tip)
		if err != nil {
			return nil, err
		}
		if len(edits) > 0 {
			result[ref.taskID] = append(result[ref.taskID], edits...)
		}
	}
	return result, nil
}

// parkedSetAside answers SetAsideEdits for one parked tip against the task's
// current tip, from the memo when it can.
func (r *Repository) parkedSetAside(ctx context.Context, config core.ProjectConfig, ref parkedRef, tip string) ([]core.SetAsideEdit, error) {
	key := ref.name + "\x00" + ref.objectID + "\x00" + tip
	r.setAsideMu.Lock()
	edits, remembered := r.setAsideMemo[key]
	r.setAsideMu.Unlock()
	if remembered {
		return edits, nil
	}

	parkedOnly, err := r.revList(ctx, "--reverse", ref.objectID, "^"+tip)
	if err != nil {
		return nil, err
	}
	if len(parkedOnly) > 0 {
		tipOnly, err := r.revList(ctx, tip, "^"+ref.objectID)
		if err != nil {
			return nil, err
		}
		heads := make([]TaskHead, 0, 1+len(parkedOnly)+len(tipOnly))
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: tip})
		for _, commit := range parkedOnly {
			heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: commit})
		}
		for _, commit := range tipOnly {
			heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: commit})
		}
		read, err := r.ReadTaskHeads(ctx, config, heads)
		if err != nil {
			return nil, err
		}
		// Every state the task has had since the two sides parted, the
		// current one first.
		states := []core.StateDocument{read[0].State}
		recorded := make(map[string]struct{})
		for _, snapshot := range read[1+len(parkedOnly):] {
			states = append(states, snapshot.State)
			for _, operation := range snapshot.Operation.Operations {
				recorded[operation.ID] = struct{}{}
			}
		}
		for _, snapshot := range read[1 : 1+len(parkedOnly)] {
			if !setAside(states, snapshot.Operation, recorded) {
				continue
			}
			edits = append(edits, core.SetAsideEdit{
				ParkedRef:  ref.name,
				Parked:     ref.objectID,
				Commit:     snapshot.Head,
				Actor:      snapshot.Operation.Actor.ID,
				WallTime:   snapshot.Operation.WallTime,
				Operations: append([]core.Operation(nil), snapshot.Operation.Operations...),
			})
		}
	}

	r.setAsideMu.Lock()
	if r.setAsideMemo == nil || len(r.setAsideMemo) >= maxSetAsideMemo {
		r.setAsideMemo = make(map[string][]core.SetAsideEdit)
	}
	r.setAsideMemo[key] = edits
	r.setAsideMu.Unlock()
	return edits, nil
}

// setAside reports whether a parked pack is an edit the task's history lacks.
// A pack whose every operation is in the history was replayed. One that is not
// is applied, as a replay would have applied it, to each state the task has
// had since the two sides parted, and counts only if none of them already
// carried its effect: a replay that skipped it as a no-op, or a person who
// copied the text back in, settles it for good, however the field changes
// afterwards. A pack that cannot be applied at all — the task was deleted, or
// its history was compacted since — counts, since nothing says its effect is
// there.
func setAside(states []core.StateDocument, pack core.OperationPack, recorded map[string]struct{}) bool {
	replayed := true
	for _, operation := range pack.Operations {
		if _, found := recorded[operation.ID]; !found {
			replayed = false
			break
		}
	}
	if replayed {
		return false
	}
	for _, state := range states {
		attempt := pack
		attempt.LogicalClock = state.LogicalClock + 1
		attempt.HistoryGeneration = state.History.Generation
		next, err := core.Apply(&state, attempt)
		if err == nil && sameTaskState(state.Task, next.Task) {
			return false
		}
	}
	return true
}

// revList returns the commits git rev-list names for args, one per line.
func (r *Repository) revList(ctx context.Context, args ...string) ([]string, error) {
	output, err := r.Git(ctx, nil, append([]string{"rev-list"}, args...)...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSuffix(string(output), "\n")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// DismissSetAside removes the parked tip that holds a task's set-aside edits,
// once a person has seen them and said they are dealt with.
//
// It is the one way, beside the retention bound and compaction, that a parked
// ref goes, and it is never automatic: a set-aside edit is reported until the
// task carries it or somebody dismisses it. The ref is removed only while it
// still points at parked, the commit the person was shown, so a dismissal
// never removes an edit nobody saw. The commits are left in the object store
// for Git's own garbage collection, as retention leaves them.
func (r *Repository) DismissSetAside(ctx context.Context, config core.ProjectConfig, taskID, parkedRefName, parked string) error {
	if err := core.ValidateTaskIDShape(taskID); err != nil {
		return core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if !strings.HasPrefix(parkedRefName, reconciledRefPrefix+taskID+"/") {
		return core.Errorf(core.CategoryValidation, "%q does not hold set-aside edits of task %s", parkedRefName, taskID)
	}
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix+taskID+"/")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.name != parkedRefName {
			continue
		}
		if ref.objectID != parked {
			return core.Errorf(core.CategoryStaleWrite,
				"the set-aside edits of task %s changed since they were shown; look at them again before dismissing them", taskID)
		}
		var input bytes.Buffer
		fmt.Fprintf(&input, "start\noption no-deref\ndelete %s %s\nprepare\ncommit\n", ref.name, ref.objectID)
		if _, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "-m", "workbook: dismiss set-aside edits", "--stdin"); err != nil {
			return core.Wrap(core.CategoryStaleWrite,
				fmt.Sprintf("the set-aside edits of task %s changed since they were shown", taskID), err)
		}
		return nil
	}
	return core.Errorf(core.CategoryNotFound, "task %s has no set-aside edits at %s; they may have been dismissed already", taskID, parkedRefName)
}
