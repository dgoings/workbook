package gitstore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// taskPushOutcome is what one publication of task refs did.
type taskPushOutcome struct {
	// Tasks has one outcome per candidate.
	Tasks map[string]SyncTaskResult
	// Duplicated counts the candidates withheld because their history
	// records one operation ID twice. Each one's outcome is SyncInvalid.
	Duplicated int
	// Stranded is set when the remote, listed again after it accepted a task
	// ref, turned out to be marked as the place the tasks moved away from:
	// the edit went to a place nobody reads any more (strandedPublication).
	Stranded error
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
//
// Every publication of task refs goes through here, which is what makes the
// two checks around the push hold for every path. Before it, a candidate whose
// history records one operation ID twice is withheld: such a history projects
// to nothing anybody can trust, and it must not reach any remote whatever
// wrote it (duplicatedOperations). After it, the remote's identity ref is
// listed again, so an edit accepted by a place the tasks have just left is
// reported rather than lost from view (strandedPublication).
func (r *Repository) pushTaskHeads(ctx context.Context, config core.ProjectConfig, candidates []pushCandidate) (taskPushOutcome, error) {
	outcome := taskPushOutcome{Tasks: make(map[string]SyncTaskResult, len(candidates))}
	if r.readOnlyRemote() {
		return outcome, readOnlyRemotePublication(r.remote(), r.configuredRemote())
	}
	duplicated, err := r.duplicatedOperations(ctx, config, candidates)
	if err != nil {
		return outcome, err
	}
	kept := candidates[:0:0]
	for _, candidate := range candidates {
		if detail, found := duplicated[candidate.TaskID]; found {
			outcome.Tasks[candidate.TaskID] = SyncTaskResult{TaskID: candidate.TaskID, Status: SyncInvalid, Detail: detail}
			outcome.Duplicated++
			continue
		}
		kept = append(kept, candidate)
	}
	if len(kept) == 0 {
		return outcome, nil
	}

	expected := make(map[string]string, len(kept))
	forced := make(map[string]bool)
	args := []string{"push", "--porcelain"}
	for _, candidate := range kept {
		refName := taskRefPrefix + candidate.TaskID
		expected[refName] = candidate.TaskID
		if candidate.Lease != nil {
			forced[refName] = true
			args = append(args, "--force-with-lease="+refName+":"+candidate.Lease.ExpectedRemote)
		}
	}
	args = append(args, r.remote())
	for _, candidate := range kept {
		args = append(args, candidate.Head+":"+taskRefPrefix+candidate.TaskID)
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
	pushed, err := parsePushPorcelain(r.remote(), push.stdout, expected, forced, gitCommandResultError(push))
	if err != nil {
		return outcome, err
	}
	for _, candidate := range kept {
		if candidate.Lease != nil && pushed[candidate.TaskID].Status == SyncCompactionPublished {
			_ = r.advanceTrackingRef(ctx, candidate.TaskID, candidate.Lease.ExpectedRemote, candidate.Lease.Local)
		}
	}
	var accepted []string
	for taskID, item := range pushed {
		outcome.Tasks[taskID] = item
		if item.Status == SyncPublished || item.Status == SyncCompactionPublished {
			accepted = append(accepted, taskID)
		}
	}
	if len(accepted) > 0 {
		sort.Strings(accepted)
		outcome.Stranded = r.strandedPublication(ctx, accepted)
	}
	return outcome, nil
}

// duplicatedOperations reads each candidate's history and names the ones that
// record an operation ID more than once, with the two commits that do.
//
// Workbook never writes such a history on purpose: a fresh operation gets a
// fresh ID, and a replay skips an operation the history it builds on already
// holds (taskReplay.next). This is the check that does not depend on every
// writer having got that right, at the one place every history passes on its
// way to a remote. It reads only what this clone holds; a chain that stops
// early, as a shallow boundary makes it, is checked as far as it goes.
//
// It costs one operation read for the whole publication, whatever the number
// of candidates: one existence probe, one parent walk and one batch of
// operation blobs.
func (r *Repository) duplicatedOperations(ctx context.Context, config core.ProjectConfig, candidates []pushCandidate) (map[string]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	requests := make([]TaskHistoryRequest, 0, len(candidates))
	for _, candidate := range candidates {
		requests = append(requests, TaskHistoryRequest{Head: TaskHead{TaskID: candidate.TaskID, ObjectID: candidate.Head}})
	}
	results, err := r.ReadTaskOperations(ctx, config, requests)
	if err != nil {
		return nil, err
	}
	duplicated := make(map[string]string)
	for _, result := range results {
		if detail, found := repeatedOperation(result.Commits); found {
			duplicated[result.TaskID] = detail
		}
	}
	return duplicated, nil
}

// repeatedOperation finds the first operation ID a chain records twice.
func repeatedOperation(chain []OperationCommit) (string, bool) {
	seen := make(map[string]string)
	for _, commit := range chain {
		for _, operation := range commit.Operation.Operations {
			if first, repeated := seen[operation.ID]; repeated {
				return fmt.Sprintf("operation %s is recorded by commit %s and again by commit %s, "+
					"so this history is not published; run workbook validate", operation.ID, first, commit.ObjectID), true
			}
			seen[operation.ID] = commit.ObjectID
		}
	}
	return "", false
}

// strandedPublication lists the remote's identity ref again after it accepted
// task refs, and reports them as stranded when it now carries a tasks-moved
// marker.
//
// Plain Git has no compare-and-swap across two refs, so nothing can stop a
// task ref landing on the old place in the moment between this checkout's last
// look at the identity ref and the move marking it. What can be done is to
// look again afterwards. The edit itself is safe: it stays on this checkout's
// own task ref and goes to the new place after setup, and `workbook move-tasks`
// run again carries it from the old place. What must not happen is that the
// command says nothing, so this returns the refusal the command reports.
//
// The check costs one listing of one ref, and only after a push that changed
// something on the remote. A listing that fails says nothing either way and is
// not reported: the push itself just reached the remote.
func (r *Repository) strandedPublication(ctx context.Context, taskIDs []string) error {
	remote := r.remote()
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", remote, identityRef)
	if err != nil {
		return nil
	}
	head, err := parseRemoteIdentityHead(listing)
	if err != nil || head == "" {
		return nil
	}
	local, found, err := r.memoizedIdentityRecord()
	if err != nil || (found && local.Head == head) {
		return nil
	}
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", remote, r.identityFetchRefspec()); err != nil {
		return nil
	}
	record, found, err := r.readIdentityRef(ctx, r.remoteIdentityRef())
	if err != nil || !found || record.Moved == nil {
		return nil
	}
	r.metadataMu.Lock()
	moved := *record.Moved
	r.originMoved = &moved
	r.metadataMu.Unlock()
	return r.strandedRefusal(ctx, remote, moved, taskIDs)
}

// strandedRefusal is the report for task refs a remote accepted just as it was
// marked as moved.
func (r *Repository) strandedRefusal(ctx context.Context, remote string, moved TasksMoved, taskIDs []string) error {
	command := "workbook setup"
	next := "pull the commit that sets tasks-remote in " + configPath + " and run workbook setup, which publishes it there"
	if tasksOnly, err := r.TasksOnly(ctx); err == nil && tasksOnly {
		command = "workbook setup --tasks-only " + moved.URL
		next = "set up a new folder with " + command + " and serve the board from there"
	}
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemotePublishedAfterMove,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
			Command: command,
			Tasks:   append([]string(nil), taskIDs...),
		},
		"%s accepted %s just as this project's tasks moved from there to %s (remote %q), so nobody reads it there any more; "+
			"the change is kept in this checkout: %s; and whoever moved the tasks runs workbook move-tasks again, "+
			"which carries it from %s",
		remote, strings.Join(taskIDs, ", "), moved.URL, moved.Remote, next, remote)
}

// CheckRemoteNotMoved reads, without asking the remote anything, whether this
// checkout's copy of the tasks remote's identity ref carries the tasks-moved
// marker, and returns the moved refusal when it does.
//
// It is for a command that is about to record a change and hand its
// publication to a watcher. The watcher fetches the remote's identity ref into
// that copy on every round, so a marker published to the remote reaches the
// copy within one watcher interval, and this check refuses from then on; the
// watcher itself is refused publishing there and logs it once. Asking the
// remote instead would put a network round trip back on the path a watcher
// exists to keep it off.
//
// reread reads the ref again, for a long-lived handle such as a board's;
// otherwise the listing this command made when it opened the project serves,
// which costs nothing more (CheckTasksRemote read the same ref then).
func (r *Repository) CheckRemoteNotMoved(ctx context.Context, reread bool) error {
	if moved := r.observedMove(); moved != nil {
		return r.remoteMarkedMoved(ctx, *moved, true)
	}
	if reread {
		r.metadataMu.Lock()
		r.trackedIdentity = trackedIdentityHead{}
		r.metadataMu.Unlock()
	}
	remote := r.remote()
	head, found, err := r.trackingIdentityHead(ctx, remote)
	if err != nil || !found {
		return nil
	}
	moved, found, err := r.trackedMoveMarker(ctx, remote, head)
	if err != nil || !found {
		return nil
	}
	return r.remoteMarkedMoved(ctx, moved, true)
}
