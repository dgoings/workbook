package gitstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// MoveTasksResult reports one run of `workbook move-tasks`.
type MoveTasksResult struct {
	// From is the remote the tasks were copied from and that is now marked.
	From core.TasksRemoteTarget `json:"from"`
	// To is the project's tasks remote, which now holds them.
	To core.TasksRemoteTarget `json:"to"`
	// Remote reports what this run did about the tasks remote in this
	// checkout: "added" from the settings, or "kept" as it was.
	Remote string `json:"remote"`
	// Marked is "marked" when this run marked the old place, and "already"
	// when an earlier run had.
	Marked string `json:"marked"`
	// Tasks is the number of task refs the old place holds.
	Tasks int `json:"tasks"`
	// Identical counts those the new place holds at exactly the same commit.
	// Carried counts the rest: the new place holds every one of their
	// operations, under other commits, because it was edited there after the
	// copy or because an edit published to the old place was carried onto
	// its history. Every task ref is one or the other, or the run fails.
	Identical int `json:"identical"`
	Carried   int `json:"carried"`
	// Stragglers reports the operations this run found at the old place that
	// the new place lacked and carried across, and who made them. It is
	// absent when there were none.
	Stragglers *MoveStragglers `json:"stragglers,omitempty"`
	// SetAside names the tasks whose operations from the old place could not
	// be applied onto the new place's history because they conflict with it.
	// They are parked in this checkout for a person to look at and are not
	// tried again by a later run here.
	SetAside []MoveSetAside `json:"setAside,omitempty"`
	// Config says what became of the old place's configuration ledger:
	// "unchanged" when this checkout already held it, "carried" when this run
	// took it, and "not-carried" when it holds a change this checkout's own
	// ledger has diverged from, which a move does not combine.
	Config string `json:"config"`
	// Copy and Recopy are the two synchronizations with the new place, before
	// and after the old place was marked.
	Copy   SyncRunResult `json:"copy"`
	Recopy SyncRunResult `json:"recopy"`
	// Verified is set once the new place has been shown to hold every
	// operation the old place does, or to have it set aside here.
	Verified bool `json:"verified"`
}

// MoveStragglers is what one run carried by operation identity.
type MoveStragglers struct {
	Operations int      `json:"operations"`
	Tasks      []string `json:"tasks"`
	Actors     []string `json:"actors"`
}

// MoveSetAside is one task whose operations from the old place were set aside.
type MoveSetAside struct {
	TaskID     string   `json:"taskId"`
	Title      string   `json:"title"`
	Operations []string `json:"operations"`
	// ParkedRef holds the old place's history for the task, when this run
	// parked it. A task set aside by an earlier run has none here.
	ParkedRef string `json:"parkedRef,omitempty"`
}

// MoveTasksOptions are the choices a person makes for one move.
type MoveTasksOptions struct {
	// From names the remote the tasks are in now; origin when empty.
	From string
	// AllowBranches accepts a destination that holds branches.
	AllowBranches bool
}

// switchRemote points this handle at name for the next synchronization and
// forgets everything it established about the remote it pointed at before:
// whether that remote agreed about the identity, and whether it was marked.
func (r *Repository) switchRemote(name string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.remoteOverride = name
	r.originIdentity = originIdentityUnknown
	r.originMoved = nil
	r.identityReport = nil
}

// MoveTasks copies this project's tasks from the remote options.From names
// (origin when empty) to the tasks remote its settings name, marks the old
// place so a checkout that has not picked up the settings is refused there,
// and proves that the new place holds every operation the old place does.
//
// Every step is safe to repeat, so an interrupted run is finished by running
// it again, and so is a run made at any later time:
//
//  1. read the old place into its tracking refs, and nothing else;
//  2. synchronize with the new place, carry onto this checkout's tasks every
//     operation the old place holds that they lack, and synchronize again,
//     which publishes them there;
//  3. mark the old place: one fast-forward of its refs/workbook/project;
//  4. and 5. read the old place again and carry what was published there
//     before the mark, or since;
//  6. compare, task by task, the operation IDs the two places hold.
//
// Carrying is by operation identity. A task whose every operation this
// checkout holds is carried, whatever its commits; one the old place holds at
// a descendant of this checkout's tip is fast-forwarded, so its commits are
// the old place's own; and an operation this checkout lacks is applied once
// onto its history by the ordinary replay. A conflict stops that task's
// replay, parks the old place's history beside it, and is reported; a later
// run finds it parked and does not try it again.
//
// The mark cannot be made atomic with the edits that other checkouts publish:
// Git has no compare-and-swap across two refs. A checkout still on the old
// settings that publishes in the moment around the mark is told so by its own
// command (strandedPublication), and running this again carries the edit.
func (r *Repository) MoveTasks(ctx context.Context, config core.ProjectConfig, options MoveTasksOptions) (MoveTasksResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return MoveTasksResult{}, err
	}
	tracked, exists, err := r.readConfig()
	if err != nil {
		return MoveTasksResult{}, err
	}
	if !exists || !tracked.TasksRemote.Chosen() {
		return MoveTasksResult{}, core.Errorf(core.CategoryValidation,
			"this project's settings name no tasks remote to move to; "+
				"run workbook config set tasks-remote <name> --url <address> first")
	}
	to := tracked.TasksRemote
	from := options.From
	if from == "" {
		from, err = r.defaultMoveSource(ctx, to.Name)
		if err != nil {
			return MoveTasksResult{}, err
		}
	}
	if err := core.ValidateTasksRemoteName(from); err != nil {
		return MoveTasksResult{}, core.Wrap(core.CategoryInvocation, "--from must name a Git remote", err)
	}
	if from == to.Name {
		return MoveTasksResult{}, core.Errorf(core.CategoryInvocation,
			"--from names %s, which is already this project's tasks remote", from)
	}
	if !r.hasGitRemote(ctx, from) {
		return MoveTasksResult{}, core.Errorf(core.CategoryNotFound,
			"this checkout has no Git remote named %q to move the tasks from", from)
	}
	setup, err := r.EnsureTasksRemote(ctx)
	if err != nil {
		return MoveTasksResult{}, err
	}
	result := MoveTasksResult{
		From:   core.TasksRemoteTarget{Remote: from, URL: redactURLCredentials(r.gitRemoteURL(ctx, from))},
		To:     core.TasksRemoteTarget{Remote: to.Name, URL: to.URL},
		Remote: setup.Status,
	}
	defer r.switchRemote("")

	// Both places are asked before anything is written, so a run without
	// access to one of them, or pointed at the wrong repository, stops having
	// changed nothing.
	r.switchRemote(to.Name)
	if _, err := r.ProbeTasksRemote(ctx); err != nil {
		return result, r.forgetAddedRemote(ctx, setup, err)
	}
	r.switchRemote(from)
	if _, err := r.ProbeTasksRemote(ctx); err != nil {
		return result, r.forgetAddedRemote(ctx, setup, err)
	}
	if err := r.refuseUnfitDestination(ctx, from, to.Name, options.AllowBranches); err != nil {
		return result, r.forgetAddedRemote(ctx, setup, err)
	}

	carried := newMoveCarry()
	if err := r.moveStep("read"); err != nil {
		return result, err
	}
	if err := r.moveReadSource(ctx, config, from); err != nil {
		return result, err
	}
	// Before the mark, carrying is the copy itself. Once the old place is
	// marked, whatever is carried from it reached it afterwards, or in the
	// moment around the mark, and is reported as such.
	carried.counting = r.observedMove() != nil
	if err := r.moveStep("copy"); err != nil {
		return result, err
	}
	result.Copy, err = r.moveCopy(ctx, config, from, to.Name, carried, &result)
	if err != nil {
		return result, err
	}

	if err := r.moveStep("mark"); err != nil {
		return result, err
	}
	r.switchRemote(from)
	marked, err := r.markTasksMoved(ctx, from, to)
	if err != nil {
		return result, err
	}
	result.Marked = marked

	if err := r.moveStep("reread"); err != nil {
		return result, err
	}
	if err := r.moveReadSource(ctx, config, from); err != nil {
		return result, err
	}
	carried.counting = true
	if err := r.moveStep("recopy"); err != nil {
		return result, err
	}
	result.Recopy, err = r.moveCopy(ctx, config, from, to.Name, carried, &result)
	if err != nil {
		return result, err
	}

	if err := r.moveStep("verify"); err != nil {
		return result, err
	}
	if err := r.verifyMovedTasks(ctx, config, from, to.Name, &result); err != nil {
		return result, err
	}
	result.Verified = true
	result.Stragglers = carried.report()
	if len(carried.conflicts) > 0 {
		return result, setAsideRefusal(from, to.Name, carried.conflicts)
	}
	return result, nil
}

// moveConflict is one task whose edits from the old place were set aside.
type moveConflict struct {
	conflict  core.Conflict
	title     string
	parkedRef string
}

// setAsideRefusal is the exit-8 report of a run that set edits aside: which
// tasks, why each could not be applied, where its text is kept and what the
// person does about it. The old place's edits are the ones that could not
// apply, and the new place's history is what they met.
func setAsideRefusal(from, to string, conflicts []moveConflict) error {
	lines := make([]string, 0, len(conflicts))
	refs := make([]string, 0, len(conflicts))
	for _, item := range conflicts {
		lines = append(lines, fmt.Sprintf("%q (%s): %s", item.title, item.conflict.TaskID, moveConflictReason(item.conflict, from, to)))
		refs = append(refs, item.parkedRef)
	}
	return core.Errorf(core.CategoryConflict,
		"the move is finished, but edits made at %s to %d task(s) could not be applied at %s, "+
			"because each conflicts with what happened to its task there: %s. "+
			"They are set aside, not lost: each one's text is kept in this clone under %s, "+
			"and workbook serve run from this clone shows it on its task as a set-aside edit; "+
			"apply what you want of it by hand. Running workbook move-tasks again from this clone does not report them again",
		from, len(conflicts), to, strings.Join(lines, "; "), strings.Join(refs, ", "))
}

// moveConflictReason says why one of the old place's edits could not apply
// onto the new place's history.
func moveConflictReason(conflict core.Conflict, from, to string) string {
	switch conflict.Type {
	case core.ConflictDescription:
		return "its description was changed both at " + from + " and at " + to
	case core.ConflictTombstone:
		edit := "the edit (" + string(conflict.Tombstone.Operation) + ")"
		if conflict.Tombstone.Field != "" {
			edit = "the change to its " + conflict.Tombstone.Field
		}
		return "it was deleted at " + to + ", so " + edit + " made at " + from + " cannot apply to it"
	case core.ConflictDependencyCycle:
		return "the dependency on " + conflict.Dependency.To + " made at " + from + " would close a dependency cycle at " + to
	default:
		return string(conflict.Type)
	}
}

// defaultMoveSource is where the tasks are now, for a move whose --from was
// left out: origin, or, when origin is marked as moved to some other remote,
// the place its marker names, and so on along the markers. That is how a
// second move (origin to tasks, then tasks to third) finds the tasks without
// being told: origin's marker names tasks, and tasks is not marked yet. A
// marker naming the destination itself is a first move run again, whose
// source is still the marked place.
//
// Each hop costs one listing of that remote, and a fetch of its identity ref
// when it changed. A remote that cannot be listed ends the walk there, and the
// probe that follows says what it needs. A marker naming a remote this
// checkout does not have is refused with the --from that would work.
func (r *Repository) defaultMoveSource(ctx context.Context, to string) (string, error) {
	from := core.DefaultTasksRemote
	seen := map[string]bool{}
	for !seen[from] && r.hasGitRemote(ctx, from) {
		seen[from] = true
		moved, err := r.remoteMoveMarker(ctx, from)
		if err != nil || moved == nil || moved.Remote == to {
			return from, nil
		}
		if !r.hasGitRemote(ctx, moved.Remote) {
			return "", core.Errorf(core.CategoryValidation,
				"%s is marked as moved to %s (remote %q), which this checkout does not have, so the tasks are there or further on; "+
					"run workbook move-tasks --from <remote> naming this checkout's remote for %s",
				from, moved.URL, moved.Remote, moved.URL)
		}
		from = moved.Remote
	}
	return from, nil
}

// remoteMoveMarker reads the tasks-moved marker on remote's identity ref,
// asking remote, or nil when it carries none.
func (r *Repository) remoteMoveMarker(ctx context.Context, remote string) (*TasksMoved, error) {
	tracking := remoteIdentityRefFor(remote)
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", remote, identityRef)
	if err != nil {
		return nil, err
	}
	head, err := parseRemoteIdentityHead(listing)
	if err != nil || head == "" {
		return nil, err
	}
	current, _ := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", tracking)
	if strings.TrimSpace(string(current)) != head {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", remote, identityFetchRefspecFor(remote)); err != nil {
			return nil, err
		}
	}
	record, found, err := r.readIdentityRef(ctx, tracking)
	if err != nil || !found {
		return nil, err
	}
	return record.Moved, nil
}

// forgetAddedRemote removes the tasks remote again when this run added it and
// then refused, so a refusal leaves the checkout as it found it.
func (r *Repository) forgetAddedRemote(ctx context.Context, setup *TasksRemoteSetup, refusal error) error {
	if setup != nil && setup.Status == "added" {
		_, _ = r.Git(ctx, nil, "remote", "remove", setup.Name)
		r.forgetTasksRemote()
	}
	return refusal
}

// moveReadSource reads the old place into its tracking refs. Nothing local
// moves: the old place is not the project's tasks remote, so the fetch is a
// read-only one (fetchTracking), and carrying is moveCopy's.
func (r *Repository) moveReadSource(ctx context.Context, config core.ProjectConfig, from string) error {
	r.switchRemote(from)
	_, err := r.Fetch(ctx, config)
	if err != nil {
		return wrapKeepingCategory("cannot read the tasks from "+from, err)
	}
	return nil
}

// moveCopy synchronizes with the new place, carries what the old place holds
// and this checkout lacks, and synchronizes again when that changed anything.
// Synchronizing first is what makes the carry safe to repeat: this checkout
// then holds everything the new place does, so an operation an earlier run or
// another checkout already carried there is found held and not carried again.
func (r *Repository) moveCopy(
	ctx context.Context,
	config core.ProjectConfig,
	from, to string,
	carried *moveCarry,
	result *MoveTasksResult,
) (SyncRunResult, error) {
	r.switchRemote(to)
	run, err := r.Sync(ctx, config)
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		return run, wrapKeepingCategory("cannot copy the tasks to "+to, err)
	}
	changed, err := r.carryFromTracking(ctx, config, from, carried, result)
	if err != nil {
		return run, err
	}
	if !changed {
		return run, nil
	}
	r.switchRemote(to)
	run, err = r.Sync(ctx, config)
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		return run, wrapKeepingCategory("cannot copy the tasks to "+to, err)
	}
	return run, nil
}

// moveCarry accumulates what a run carried by operation identity.
type moveCarry struct {
	// counting says whether what is carried now is reported as stragglers.
	counting   bool
	operations int
	tasks      map[string]struct{}
	actors     map[string]struct{}
	conflicts  []moveConflict
}

func newMoveCarry() *moveCarry {
	return &moveCarry{tasks: map[string]struct{}{}, actors: map[string]struct{}{}}
}

// add records one carried commit, when what is carried now is reported.
func (carry *moveCarry) add(taskID string, commit OperationCommit) {
	if !carry.counting {
		return
	}
	carry.tasks[taskID] = struct{}{}
	carry.actors[commit.Operation.Actor.ID] = struct{}{}
	carry.operations += len(commit.Operation.Operations)
}

// countTaken records what taking the old place's commits for one task
// carries: every operation on its history from head that this checkout's
// history to local, if any, does not hold.
func (r *Repository) countTaken(ctx context.Context, config core.ProjectConfig, carry *moveCarry, taskID, head, local string) error {
	if !carry.counting {
		return nil
	}
	if local == "" {
		chains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: head}})
		if err != nil {
			return err
		}
		for _, commit := range chains[0] {
			carry.add(taskID, commit)
		}
		return nil
	}
	missing, err := r.operationsNotIn(ctx, config, taskID, head, local, nil)
	if err != nil {
		return err
	}
	for _, commit := range missing {
		carry.add(taskID, commit)
	}
	return nil
}

func (carry *moveCarry) report() *MoveStragglers {
	if carry.operations == 0 {
		return nil
	}
	report := &MoveStragglers{Operations: carry.operations}
	for taskID := range carry.tasks {
		report.Tasks = append(report.Tasks, taskID)
	}
	for actor := range carry.actors {
		report.Actors = append(report.Actors, actor)
	}
	sort.Strings(report.Tasks)
	sort.Strings(report.Actors)
	return report
}

// carryFromTracking brings into this checkout's task refs what the old
// place's tracking refs hold and they lack, in one compare-and-swap
// transaction, and reports whether it moved anything. See MoveTasks for the
// rule; the configuration ledger is carried only by fast-forward (carryConfig).
func (r *Repository) carryFromTracking(
	ctx context.Context,
	config core.ProjectConfig,
	from string,
	carried *moveCarry,
	result *MoveTasksResult,
) (bool, error) {
	configChanged, err := r.carryConfig(ctx, config, from, result)
	if err != nil {
		return false, err
	}
	trackingRefs, _, err := r.listOwnedTaskRefs(ctx, config, remoteTaskRefPrefixFor(from))
	if err != nil {
		return false, err
	}
	canonicalRefs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return false, err
	}
	local := make(map[string]string, len(canonicalRefs))
	for _, ref := range canonicalRefs {
		local[ref.taskID] = ref.objectID
	}
	heads := make([]TaskHead, 0, len(trackingRefs)+len(canonicalRefs))
	for _, ref := range trackingRefs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	for _, ref := range canonicalRefs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return false, err
	}
	source := make(map[string]core.Snapshot, len(trackingRefs))
	canonical := make(map[string]core.Snapshot, len(canonicalRefs))
	for index, tip := range tips {
		if index < len(trackingRefs) {
			if tip.Err != nil {
				return false, core.Wrap(core.CategoryOf(tip.Err),
					fmt.Sprintf("%s holds task %s at a commit this checkout cannot read, so it was not carried", from, tip.Head.TaskID),
					tip.Err)
			}
			source[tip.Head.TaskID] = tip.Snapshot
			continue
		}
		if tip.Err == nil {
			canonical[tip.Head.TaskID] = tip.Snapshot
		}
	}

	dependencies := make(map[string][]string, len(canonical))
	for taskID, snapshot := range canonical {
		if !snapshot.State.Task.Deleted {
			dependencies[taskID] = snapshot.State.Task.Dependencies
		}
	}
	var updates []canonicalRefUpdate
	var pairs []taskHeadPair
	var byIdentity []string
	for _, ref := range trackingRefs {
		remote := source[ref.taskID]
		head, found := local[ref.taskID]
		switch {
		case !found:
			// The old place's own commits, so the new place gets the same
			// commit IDs, not only the same operation IDs.
			updates = append(updates, canonicalRefUpdate{TaskID: ref.taskID, Next: remote.Head})
			if err := r.countTaken(ctx, config, carried, ref.taskID, remote.Head, ""); err != nil {
				return false, err
			}
		case head == remote.Head:
		default:
			mine, readable := canonical[ref.taskID]
			if !readable {
				return false, core.Errorf(core.CategoryCorruptData,
					"this checkout's task ref %s cannot be read, so %s's copy of it was not carried", ref.taskID, from)
			}
			if mine.Operation.HistoryGeneration != remote.Operation.HistoryGeneration {
				byIdentity = append(byIdentity, ref.taskID)
				continue
			}
			pairs = append(pairs, taskHeadPair{TaskID: ref.taskID, Local: mine, Remote: remote})
		}
	}
	relationships, _, err := r.classifyTaskHeadRelationships(ctx, config, pairs)
	if err != nil {
		return false, err
	}
	for _, relationship := range relationships {
		switch relationship.Relationship {
		case taskHeadsRemoteAhead:
			updates = append(updates, canonicalRefUpdate{
				TaskID: relationship.TaskID, Next: source[relationship.TaskID].Head, Expected: local[relationship.TaskID],
			})
			if err := r.countTaken(ctx, config, carried, relationship.TaskID,
				source[relationship.TaskID].Head, local[relationship.TaskID]); err != nil {
				return false, err
			}
		case taskHeadsDiverged:
			byIdentity = append(byIdentity, relationship.TaskID)
		}
	}
	sort.Strings(byIdentity)

	parkedIndexes, err := r.nextParkedRefIndexes(ctx, config)
	if err != nil {
		return false, err
	}
	parked, err := r.parkedTaskHeads(ctx, config)
	if err != nil {
		return false, err
	}
	for _, taskID := range byIdentity {
		update, err := r.carryTask(ctx, config, from, taskID, canonical[taskID], source[taskID], parked[taskID],
			parkedIndexes[taskID], dependencies, carried, result)
		if err != nil {
			return false, err
		}
		if update != nil {
			updates = append(updates, *update)
		}
	}
	if len(updates) == 0 {
		return configChanged, nil
	}
	if err := r.updateCanonicalRefsFromValidated(ctx, config, canonicalRefs, updates); err != nil {
		return false, err
	}
	return true, nil
}

// carryTask applies onto this checkout's history of one task the operations
// the old place holds for it and this checkout lacks, skipping those already
// set aside here. It returns the canonical ref update to make, if any.
func (r *Repository) carryTask(
	ctx context.Context,
	config core.ProjectConfig,
	from, taskID string,
	mine, theirs core.Snapshot,
	parkedHeads map[string]struct{},
	parkedIndex int,
	dependencies map[string][]string,
	carried *moveCarry,
	result *MoveTasksResult,
) (*canonicalRefUpdate, error) {
	chains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: mine.Head}})
	if err != nil {
		return nil, err
	}
	if chains[0] == nil {
		return nil, core.Errorf(core.CategoryCorruptData,
			"this checkout cannot read the whole history of task %s, so %s's copy of it was not carried", taskID, from)
	}
	var exclude map[string]struct{}
	held := heldOperations{}
	if err := held.recordChain(chains[0]); err != nil {
		return nil, err
	}
	if checkpoint, ok := chains[0][0].Operation.Checkpoint(); ok {
		exclude = operationIDSet(checkpoint.CompactedOperations)
		held.recordIDs(checkpoint.CompactedOperations)
	}
	// An operation the old place holds under an ID this history records is
	// the same operation or a forgery, and only content tells them apart:
	// carrying is by operation identity, so a forgery would otherwise be
	// counted as held and its task reported carried.
	sourceChains, err := r.completeTaskChains(ctx, config, []TaskHead{{TaskID: taskID, ObjectID: theirs.Head}})
	if err != nil {
		return nil, err
	}
	for _, commit := range sourceChains[0] {
		if _, err := held.match(taskID, commit.ObjectID, commit.Operation); err != nil {
			return nil, err
		}
	}
	missing, err := r.operationsNotIn(ctx, config, taskID, theirs.Head, mine.Head, exclude)
	if err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return nil, nil
	}

	// What this checkout parked earlier is set aside, not missing: a person
	// was shown it, and trying it again on every run would show it again as
	// if it were new.
	setAside, err := r.parkedAccount(ctx, config, taskID, parkedHeads)
	if err != nil {
		return nil, err
	}
	var pending []OperationCommit
	var aside []string
	for _, commit := range missing {
		if setAside.covers(commit) {
			for _, operation := range commit.Operation.Operations {
				aside = append(aside, operation.ID)
			}
			continue
		}
		pending = append(pending, commit)
	}
	if len(pending) == 0 {
		// The second copy of a run finds what its first one parked.
		for _, earlier := range result.SetAside {
			if earlier.TaskID == taskID {
				return nil, nil
			}
		}
		result.SetAside = append(result.SetAside, MoveSetAside{TaskID: taskID, Title: mine.State.Task.Title, Operations: aside})
		return nil, nil
	}

	// The replay adds what it writes to held, so whether a pack was carried
	// or found already recorded is decided against a copy taken before it.
	before := make(operationAccount, len(held))
	for id := range held {
		before[id] = struct{}{}
	}
	replay, failure, err := r.replayOperations(ctx, config, taskID, mine, pending, false,
		mine.Operation.HistoryGeneration, dependencies, held)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, wrapKeepingCategory(fmt.Sprintf("cannot carry task %s from %s", taskID, from), failure)
	}
	processed := replay.replayed + replay.skipped
	for _, commit := range pending[:processed] {
		if !before.covers(commit) {
			carried.add(taskID, commit)
		}
	}
	update := &canonicalRefUpdate{TaskID: taskID, Next: replay.parent.Head, Expected: mine.Head}
	if replay.conflict != nil {
		parkedRef := fmt.Sprintf("%s%s/%d", reconciledRefPrefix, taskID, parkedIndex)
		update.ParkedRef = parkedRef
		update.ParkedHead = theirs.Head
		var operations []string
		for _, commit := range pending[processed:] {
			if before.covers(commit) {
				continue
			}
			for _, operation := range commit.Operation.Operations {
				operations = append(operations, operation.ID)
			}
		}
		result.SetAside = append(result.SetAside, MoveSetAside{
			TaskID: taskID, Title: mine.State.Task.Title, Operations: operations, ParkedRef: parkedRef,
		})
		carried.conflicts = append(carried.conflicts, moveConflict{
			conflict: *replay.conflict, title: mine.State.Task.Title, parkedRef: parkedRef,
		})
	}
	if update.Next == update.Expected && update.ParkedRef == "" {
		return nil, nil
	}
	return update, nil
}

// parkedAccount is the operations this checkout holds parked for one task.
func (r *Repository) parkedAccount(ctx context.Context, config core.ProjectConfig, taskID string, heads map[string]struct{}) (operationAccount, error) {
	account := operationAccount{}
	if len(heads) == 0 {
		return account, nil
	}
	requests := make([]TaskHistoryRequest, 0, len(heads))
	for head := range heads {
		requests = append(requests, TaskHistoryRequest{Head: TaskHead{TaskID: taskID, ObjectID: head}})
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].Head.ObjectID < requests[j].Head.ObjectID })
	// One request per call: the reader takes each task once.
	for _, request := range requests {
		results, err := r.ReadTaskOperations(ctx, config, []TaskHistoryRequest{request})
		if err != nil {
			return nil, err
		}
		for _, commit := range results[0].Commits {
			for _, operation := range commit.Operation.Operations {
				account[operation.ID] = struct{}{}
			}
		}
	}
	return account, nil
}

// carryConfig takes the old place's configuration ledger when it is ahead of
// this checkout's, by fast-forward only. A ledger replay between two places
// would have the same trouble a task replay had (see tasksread.go), and the
// ledger has no replay by operation identity, so a divergence is reported
// rather than combined.
func (r *Repository) carryConfig(ctx context.Context, config core.ProjectConfig, from string, result *MoveTasksResult) (bool, error) {
	tracking := remoteConfigRefFor(from)
	listing, err := r.listConfigRefs(ctx, configRef, tracking)
	if err != nil {
		return false, err
	}
	localHead, localFound := listing.Heads[configRef]
	remoteHead, remoteFound := listing.Heads[tracking]
	switch {
	case !remoteFound || localHead == remoteHead:
		if result.Config == "" {
			result.Config = "unchanged"
		}
		return false, nil
	case localFound:
		if _, err := r.Git(ctx, nil, "merge-base", "--is-ancestor", remoteHead, localHead); err == nil {
			if result.Config == "" {
				result.Config = "unchanged"
			}
			return false, nil
		}
		if _, err := r.Git(ctx, nil, "merge-base", "--is-ancestor", localHead, remoteHead); err != nil {
			result.Config = "not-carried"
			return false, nil
		}
	}
	// The ordinary configuration stage, pointed at the old place, creates or
	// fast-forwards the ledger with all its checks; the cases above are the
	// only ones it would replay in, and they never reach it.
	r.switchRemote(from)
	outcome := r.reconcileConfig(ctx, config)
	if outcome.Fatal != nil {
		return false, outcome.Fatal
	}
	if outcome.Deferred != nil {
		return false, outcome.Deferred
	}
	result.Config = "carried"
	return true, nil
}

// refuseUnfitDestination refuses a destination that is the same repository
// as the old place, however its address is spelled, and then one that holds
// branches, unless the person said they mean it.
//
// Sameness is decided by what the two hold, not by their addresses: a ref
// with a name nobody else could pick is written to the destination and looked
// for at the old place, then removed. A different repository cannot show it,
// so there is no false refusal for a fresh tasks repository or for one
// somebody filled beforehand, and the same repository always does, through any
// spelling, link or case of its address. It costs three round trips, and
// needs the push access to the destination that the move needs anyway.
func (r *Repository) refuseUnfitDestination(ctx context.Context, from, to string, allowBranches bool) error {
	local, found, err := r.readIdentityRef(ctx, identityRef)
	if err != nil {
		return err
	}
	if !found {
		return core.Errorf(core.CategoryCorruptData, "%s is missing; rerun workbook setup", identityRef)
	}
	nonce, err := newProbeName()
	if err != nil {
		return err
	}
	probe := sameRepositoryProbePrefix + nonce
	write := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", to, local.Head+":"+probe)
	if write.err != nil {
		return core.Wrap(core.CategoryOperational,
			fmt.Sprintf("cannot write to %s, which the move needs: moving the tasks there needs push access to it", to),
			gitCommandResultError(write))
	}
	listing, listErr := r.Git(ctx, nil, "ls-remote", "--refs", from, probe)
	remove := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, "push", "--porcelain", to, ":"+probe)
	if listErr != nil {
		return core.Wrap(core.CategoryOperational, "cannot list "+from+" to tell it apart from "+to, listErr)
	}
	if len(listedRefNames(listing)) > 0 {
		return sameRepositoryRefusal(to, redactURLCredentials(r.gitRemoteURL(ctx, to)),
			from, redactURLCredentials(r.gitRemoteURL(ctx, from)))
	}
	if remove.err != nil {
		return core.Wrap(core.CategoryOperational,
			fmt.Sprintf("cannot remove %s from %s after writing it to tell the two repositories apart; remove it by hand", probe, to),
			gitCommandResultError(remove))
	}
	branches, err := r.Git(ctx, nil, "ls-remote", "--heads", to)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot list "+to+"'s branches", err)
	}
	if names := listedRefNames(branches); len(names) > 0 && !allowBranches {
		shown := names
		if len(shown) > 3 {
			shown = append(append([]string(nil), shown[:3]...), fmt.Sprintf("and %d more", len(names)-3))
		}
		return core.Errorf(core.CategoryValidation,
			"%s (%s) holds branches (%s), and a repository for tasks holds only Workbook refs; "+
				"check that its address is the tasks repository's, or pass --allow-branches if it is",
			to, redactURLCredentials(r.gitRemoteURL(ctx, to)), strings.Join(shown, ", "))
	}
	return nil
}

// sameRepositoryProbePrefix holds the refs refuseUnfitDestination writes and
// removes. Nothing reads this namespace.
const sameRepositoryProbePrefix = "refs/workbook/probes/"

func listedRefNames(listing []byte) []string {
	var names []string
	for _, line := range strings.Split(string(listing), "\n") {
		if _, name, found := strings.Cut(line, "\t"); found {
			names = append(names, name)
		}
	}
	return names
}

// markTasksMoved publishes the marker on from's identity ref. The marker
// commit is deterministic, so a repeated run pushes the same commit and Git
// reports it up to date.
func (r *Repository) markTasksMoved(ctx context.Context, from string, to core.TasksRemoteSetting) (string, error) {
	local, found, err := r.readIdentityRef(ctx, identityRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", core.Errorf(core.CategoryCorruptData, "%s is missing; rerun workbook setup", identityRef)
	}
	moved := TasksMoved{
		Format:    tasksMovedFormat,
		Version:   tasksMovedVersion,
		ProjectID: local.Identity.ProjectID,
		Remote:    to.Name,
		URL:       to.URL,
	}
	marker, err := r.writeTasksMovedCommit(ctx, local, moved)
	if err != nil {
		return "", err
	}
	tracking := remoteIdentityRefFor(from)
	listing, err := r.listIdentityRefs(ctx, tracking)
	if err != nil {
		return "", err
	}
	if listing.Heads[tracking] == marker {
		return "already", nil
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", from, marker+":"+identityRef)
	if push.err != nil {
		return "", core.Wrap(core.CategoryOperational,
			fmt.Sprintf("cannot mark %s as the place the tasks moved away from; the tasks are copied to %s, "+
				"and running workbook move-tasks again finishes the move", from, to.Name),
			gitCommandResultError(push))
	}
	if _, err := r.Git(ctx, nil, "update-ref", "--no-deref", tracking, marker); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot record the published marker", err)
	}
	return "marked", nil
}

// verifyMovedTasks lists both places afresh and requires the new place to
// hold, for every task the old place holds, every one of its operation IDs,
// or to have it set aside in this checkout. Equal commits are counted without
// reading anything: a commit ID names the whole history below it. Everything
// else is compared by operation ID, because a task carried by replay holds
// the same operations under other commits. A history at the new place that
// records an operation twice fails the run too.
func (r *Repository) verifyMovedTasks(ctx context.Context, config core.ProjectConfig, from, to string, result *MoveTasksResult) error {
	for _, remote := range []string{from, to} {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", remote,
			"+"+taskRefPrefix+"*:"+remoteTaskRefPrefixFor(remote)+"*"); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot list the task refs "+remote+" holds", err)
		}
	}
	source, err := r.trackingRefHeads(ctx, remoteTaskRefPrefixFor(from))
	if err != nil {
		return err
	}
	destination, err := r.trackingRefHeads(ctx, remoteTaskRefPrefixFor(to))
	if err != nil {
		return err
	}
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	sort.Strings(names)
	result.Tasks = len(source)
	result.Identical, result.Carried = 0, 0

	var differing []string
	var missing []string
	for _, name := range names {
		have, found := destination[name]
		switch {
		case !found:
			missing = append(missing, name+" (absent)")
		case have == source[name]:
			result.Identical++
		default:
			differing = append(differing, name)
		}
	}
	if len(differing) > 0 {
		sourceHeads := make([]TaskHead, 0, len(differing))
		destinationHeads := make([]TaskHead, 0, len(differing))
		for _, name := range differing {
			sourceHeads = append(sourceHeads, TaskHead{TaskID: name, ObjectID: source[name]})
			destinationHeads = append(destinationHeads, TaskHead{TaskID: name, ObjectID: destination[name]})
		}
		sourceChains, err := r.completeTaskChains(ctx, config, sourceHeads)
		if err != nil {
			return err
		}
		destinationChains, err := r.completeTaskChains(ctx, config, destinationHeads)
		if err != nil {
			return err
		}
		parked, err := r.parkedTaskHeads(ctx, config)
		if err != nil {
			return err
		}
		for index, name := range differing {
			theirs, ours := sourceChains[index], destinationChains[index]
			if theirs == nil || ours == nil {
				missing = append(missing, name+" (a history this checkout cannot read in full)")
				continue
			}
			if detail, repeated := repeatedOperation(ours); repeated {
				missing = append(missing, name+" ("+detail+")")
				continue
			}
			var compacted []string
			if checkpoint, ok := ours[0].Operation.Checkpoint(); ok {
				compacted = checkpoint.CompactedOperations
			}
			account := newOperationAccount(compacted, ours)
			// Held by ID is not enough: the same ID with other content is a
			// forgery on one side, and counting it as held would report the
			// task carried while the two places disagree about it.
			held := heldOperations{}
			if err := held.recordChain(ours); err != nil {
				return err
			}
			held.recordIDs(compacted)
			aside, err := r.parkedAccount(ctx, config, name, parked[name])
			if err != nil {
				return err
			}
			var lacking []string
			for _, commit := range theirs {
				if _, err := held.match(name, commit.ObjectID, commit.Operation); err != nil {
					return wrapKeepingCategory(fmt.Sprintf("%s and %s disagree about task %s", from, to, name), err)
				}
				if account.covers(commit) || aside.covers(commit) {
					continue
				}
				for _, operation := range commit.Operation.Operations {
					lacking = append(lacking, operation.ID)
				}
			}
			if len(lacking) > 0 {
				missing = append(missing, fmt.Sprintf("%s (lacks operation(s) %s)", name, strings.Join(lacking, ", ")))
				continue
			}
			result.Carried++
		}
	}
	if len(missing) > 0 {
		return core.Errorf(core.CategoryConflict,
			"%s does not hold everything %s holds: %s; run workbook move-tasks again",
			to, from, strings.Join(missing, "; "))
	}
	return nil
}

// trackingRefHeads lists one tracking namespace by task ID, without reading
// any object.
func (r *Repository) trackingRefHeads(ctx context.Context, prefix string) (map[string]string, error) {
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string)
	for _, line := range strings.Split(string(bytes.TrimSpace(output)), "\n") {
		objectID, refName, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		heads[strings.TrimPrefix(refName, prefix)] = objectID
	}
	return heads, nil
}

// CheckMoveDone refuses setup in a checkout whose project chose a tasks remote
// while origin, the code's remote, still holds task refs and is not marked as
// moved: the person who chose the remote has not run move-tasks, or it was
// interrupted before the mark, and joining now would publish to one place
// while teammates publish to the other.
//
// It is best effort. A checkout that cannot reach origin, or a project that
// shares its tasks through origin, is not checked.
func (r *Repository) CheckMoveDone(ctx context.Context) error {
	tracked, exists, err := r.readConfig()
	if err != nil || !exists || !tracked.TasksRemote.Chosen() {
		return err
	}
	from := core.DefaultTasksRemote
	if !r.hasGitRemote(ctx, from) {
		return nil
	}
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", from, identityRef, taskRefPrefix+"*")
	if err != nil {
		return nil
	}
	tasks := false
	for _, line := range strings.Split(string(listing), "\n") {
		if _, refName, found := strings.Cut(line, "\t"); found && strings.HasPrefix(refName, taskRefPrefix) {
			tasks = true
			break
		}
	}
	if !tasks {
		return nil
	}
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", from,
		identityFetchRefspecFor(from)); err != nil {
		return nil
	}
	record, found, err := r.readIdentityRef(ctx, remoteIdentityRefFor(from))
	if err != nil {
		return err
	}
	if found && record.Moved != nil {
		return nil
	}
	to := tracked.TasksRemote
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteNotMoved,
			Remote:  to.Name,
			URL:     to.URL,
			MovedTo: &core.TasksRemoteTarget{Remote: to.Name, URL: to.URL},
			Command: "workbook move-tasks",
		},
		"this project's settings say its tasks are in %s (remote %q), but origin still holds them and is not marked as moved, "+
			"so the move has not finished; whoever set tasks-remote runs workbook move-tasks, and this checkout runs workbook setup after that",
		to.URL, to.Name)
}

// wrapKeepingCategory adds context to err without changing its exit code.
func wrapKeepingCategory(message string, err error) error {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryOperational
	}
	return core.Wrap(category, message, err)
}

func (r *Repository) moveStep(step string) error {
	if r.moveStepHook == nil {
		return nil
	}
	return r.moveStepHook(step)
}

// newProbeName returns a ref name component nobody else could pick.
func newProbeName() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot draw a random name", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}
