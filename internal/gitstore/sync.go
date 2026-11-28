package gitstore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

type SyncStatus string
type SyncPhaseStatus string

const (
	SyncCreated       SyncStatus = "created"
	SyncFastForwarded SyncStatus = "fast-forwarded"
	SyncUnchanged     SyncStatus = "unchanged"
	SyncLocalAhead    SyncStatus = "local-ahead"
	// SyncReconciled reports a task whose local-only operations were replayed
	// onto the fetched tip, leaving the canonical ref a descendant of origin.
	SyncReconciled SyncStatus = "reconciled"
	// SyncConflicted reports a replay that stopped on concurrent intent
	// Workbook will not decide. The envelope's conflict list carries the detail.
	SyncConflicted SyncStatus = "conflicted"
	// SyncNeedsUpgrade reports a divergent task whose local operations were not
	// replayed because origin's history for it was written by a newer Workbook.
	//
	// It is separate from SyncInvalid because nothing about it is invalid. The
	// task fetched, the tracking ref advanced, and the local operations are
	// exactly where they were; what is missing is a build that can fold
	// origin's side of the divergence, which is a fact about this clone.
	SyncNeedsUpgrade SyncStatus = "needs-upgrade"
	SyncInvalid      SyncStatus = "invalid"
	SyncPublished    SyncStatus = "published"
	SyncUpToDate     SyncStatus = "up-to-date"
	SyncRejected     SyncStatus = "rejected"
	SyncLocalChanged SyncStatus = "local-changed"
	// SyncCompactionPublished reports a compacted task whose checkpoint origin
	// accepted as a deliberate non-fast-forward, through a lease on the
	// older-generation tip origin held, which the checkpoint accounts for. See
	// compactionLease.
	SyncCompactionPublished SyncStatus = "compaction-published"
	// SyncAdopted reports a task origin compacted, which this clone took as it
	// stood: every local operation was already in the checkpoint, so the
	// canonical ref moved to it with nothing to replay.
	SyncAdopted SyncStatus = "adopted"
	// SyncAdoptedReplayed reports a task origin compacted, whose local-only
	// operations were replayed onto the checkpoint in its generation.
	SyncAdoptedReplayed SyncStatus = "adopted-replayed"
	// SyncCompactedOnOrigin reports a task origin compacted whose local-only
	// operations could not be replayed onto the checkpoint. The canonical ref
	// stays on the old history, unpublished, until `workbook compact --adopt`.
	SyncCompactedOnOrigin SyncStatus = "compacted-on-origin"
	// SyncDiscardedLocal reports a task `workbook compact --adopt
	// --discard-local` moved to origin's tip, dropping the local history that
	// could not be combined with it, after saving that history to a bundle
	// unless told not to.
	SyncDiscardedLocal SyncStatus = "discarded-local"
	// SyncTracked reports a task read from a remote named with --remote that
	// is not the project's tasks remote. Its tip went into that remote's
	// tracking ref and nowhere else: this checkout's own task ref is exactly
	// where it was, because only `workbook move-tasks` carries tasks from one
	// remote to another (see fetchTracking).
	SyncTracked SyncStatus = "tracked"
)

// LeavesPreviousHistory reports an outcome that moved the canonical ref onto a
// history its previous tip is not on: origin's compaction adopted, with or
// without local operations replayed onto it, or the local history discarded
// for origin's. Nothing keeps that previous tip alive afterwards, neither a
// parked ref nor a reflog entry, so whatever last read it, such as the
// projection, has to read the task again before Git prunes it; once it is
// gone, no advance from it can be proven.
//
// Every other outcome leaves the ref on a descendant of its previous tip, or
// parks the previous tip, which keeps it reachable.
func (s SyncStatus) LeavesPreviousHistory() bool {
	switch s {
	case SyncAdopted, SyncAdoptedReplayed, SyncDiscardedLocal:
		return true
	default:
		return false
	}
}

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

// SyncRunResult carries both phases of one synchronization. Only Fetch reports
// ignored refs: the run's push phase publishes tips fetch already inspected and
// deliberately lists origin's namespace no second time, so a caller reporting a
// whole run reads Fetch.Ignored. The order is the one Git lists refs in, by ref
// name, so one run's report reads the same as the next.
type SyncRunResult struct {
	Remote string     `json:"remote"`
	Fetch  SyncResult `json:"fetch"`
	Push   SyncResult `json:"push"`
	// Identity reports what the identity stage did, and is a pointer so that a
	// run where it did nothing new omits the member entirely. A steady-state
	// synchronization therefore emits exactly the JSON it emitted before this
	// stage existed, which is what every caller parsing that output relies on.
	Identity *SyncIdentityResult `json:"identity,omitempty"`
	// Config reports what the configuration stage did, on the same terms and
	// for the same reason: a project with no ledger, or one whose ledger did
	// not move, omits the member entirely.
	Config *SyncConfigResult `json:"config,omitempty"`
}

// fetchState is the single validated view that Fetch produces for Sync. It is
// deliberately private: callers receive the stable SyncResult while Sync can
// reuse the canonical and tracking tips without inspecting them again.
type fetchState struct {
	Canonical map[string]core.Snapshot
	Tracking  map[string]core.Snapshot
	Outcomes  map[string]SyncTaskResult
	// Withheld names the tasks the push phase must not publish: fetch left
	// each on a tip origin will not take, or must not be given.
	Withheld map[string]struct{}
	Identity *SyncIdentityResult
	Config   *SyncConfigResult
	// Relics is the relic stage's view, for the push phase.
	Relics relicFetchState
}

// Push publishes validated local Workbook task refs to origin without deletion
// or unconditional force. The one non-fast-forward it makes is a compacted
// task's checkpoint, leased against the older-generation tip origin holds
// (compactionLease). One
// non-atomic publication preserves per-ref outcomes so a rejection does not
// prevent unrelated tasks from publishing.
//
// Relic refs go out after the task refs, in a push of their own, each plain:
// the one listing of origin also names its relic refs, and every local relic
// whose tip origin does not hold is checked and published.
func (r *Repository) Push(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	result := SyncResult{Remote: r.remote(), Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if r.readOnlyRemote() {
		return failedSyncPhase(result, "push refused", readOnlyRemotePublication(r.remote(), r.configuredRemote()))
	}
	refs, err := r.listTaskRefs(ctx)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].taskID < refs[j].taskID })
	relicRecords, err := r.relicRefRecords(ctx, relicRefPrefix)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if len(refs) == 0 && len(relicRecords) == 0 {
		// A project with no task refs can still hold a configuration ledger.
		// Returning here published nothing, which made `workbook push` the one
		// publication path that could silently keep a vocabulary local — and
		// the likeliest project to have one and no tasks is a project being set
		// up, where the columns are exactly what a teammate needs first.
		return r.pushConfigOnly(ctx, result)
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
	valid := make(map[string]core.Snapshot, len(refs))
	invalid := 0
	for _, tip := range partial {
		observed[tip.Head.TaskID] = tip.Head.ObjectID
		if tip.Err != nil {
			items[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
			invalid++
			continue
		}
		valid[tip.Head.TaskID] = tip.Snapshot
	}

	// Both singleton refs ride the listing Push already makes, so establishing
	// that origin is this project, and whether it holds this project's
	// configuration, costs no round trip of its own.
	remoteOutput, err := r.Git(ctx, nil, "ls-remote", "--refs", r.remote(), taskRefPrefix+"*", relicRefPrefix+"*", identityRef, configRef)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	remoteIdentityHead, err := parseRemoteIdentityHead(remoteOutput)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	if err := r.ensureOriginIdentityAgreement(ctx, map[string]string{identityRef: remoteIdentityHead}, false); err != nil {
		return failedPushTransport(result, refs, items, invalid, "push stopped before publishing any task ref", err)
	}
	result.Identity, _ = r.IdentityReport()
	// The vocabulary goes out before the task refs whose statuses it explains.
	// A teammate who fetches a task in a column their clone has never heard of
	// has a worse experience than one whose columns arrive a moment early.
	if err := r.observeRemoteConfigHead(ctx, remoteOutput); err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	if published, err := r.publishConfigLedger(ctx); err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	} else if published != nil {
		result.Config = published
	}
	// The keys are read after the configuration ledger went out, so a key this
	// push just published classifies the refs it published beside it.
	//
	// The memo is dropped first, for the reason the fetch path drops it
	// (sync.go's fetch, above): this handle's memoized set can predate a ledger
	// another handle on the same repository moved, and a push that classified
	// origin's namespace against it would report a teammate's refs as another
	// project's in its own report.
	r.forgetKeySet()
	keys, err := r.keySet(ctx, config)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	remoteHeads, ignored, err := r.parseRemoteTaskHeads(keys, remoteOutput)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	result.Ignored = ignored
	remoteRelics, err := parseRemoteRelicHeads(remoteOutput)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	// A relic ref this clone cannot name stops the relics, not the tasks.
	relicCandidates, relicListErr := localRelicPushCandidates(keys, relicRecords, remoteRelics)
	candidates := make([]pushCandidate, 0, len(valid))
	for _, ref := range refs {
		snapshot, ok := valid[ref.taskID]
		if !ok {
			continue
		}
		if remoteHeads[ref.taskID] == ref.objectID {
			items[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncUpToDate}
			continue
		}
		candidate := pushCandidate{TaskID: ref.taskID, Head: ref.objectID}
		lease, leased, err := r.compactionLease(ctx, config, ref.taskID, snapshot, remoteHeads[ref.taskID])
		if err != nil {
			return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
		}
		if leased {
			candidate.Lease = &lease
		}
		candidates = append(candidates, candidate)
	}
	var stranded error
	if len(candidates) != 0 {
		pushed, err := r.pushTaskHeads(ctx, config, candidates)
		if err != nil {
			return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
		}
		for taskID, item := range pushed.Tasks {
			items[taskID] = item
		}
		invalid += pushed.Duplicated
		stranded = pushed.Stranded
	}

	finalRefs, err := r.listTaskRefs(ctx)
	if err != nil {
		return failedPushTransport(result, refs, items, invalid, "push failed before completion", err)
	}
	final := make(map[string]string, len(finalRefs))
	for _, ref := range finalRefs {
		final[ref.taskID] = ref.objectID
	}
	var rejected []SyncTaskResult
	changed := 0
	for _, ref := range refs {
		item := items[ref.taskID]
		if item.Status == SyncRejected {
			rejected = append(rejected, item)
		} else if publishedSyncStatus(item.Status) && final[ref.taskID] != observed[ref.taskID] {
			item.Status = SyncLocalChanged
			item.Detail = "validated task head was published, but the local ref advanced during push; run workbook push again"
			items[ref.taskID] = item
			changed++
		}
		result.Tasks = append(result.Tasks, items[ref.taskID])
	}
	if stranded != nil {
		result.Status = SyncPhaseCompleted
		return result, stranded
	}
	// Relics go out after the task refs, in a push of their own, so nothing
	// origin does with a relic can change a task's outcome. Every relic tip is
	// checked here: no fetch in this command read them.
	var relics relicPublication
	var relicPushErr error
	if relicListErr == nil {
		relicPushErr = func() error {
			kept, withheld, err := r.withholdRelicsOriginAlreadyJudged(ctx, config, relicCandidates, remoteRelics)
			if err != nil {
				return err
			}
			unchecked := make(map[string]bool, len(kept))
			for _, candidate := range kept {
				unchecked[candidate.RelicID] = true
			}
			if relics, err = r.publishRelics(ctx, config, kept, unchecked, nil); err != nil {
				return err
			}
			for _, outcome := range withheld {
				relics.Results = append(relics.Results, outcome)
				relics.Invalid = append(relics.Invalid, outcome.Detail)
			}
			sort.Slice(relics.Results, func(i, j int) bool { return relics.Results[i].RelicID < relics.Results[j].RelicID })
			result.Relics = relics.Results
			return nil
		}()
	}
	result.Status = SyncPhaseCompleted
	return relicPushFailed(result, relicPushErr, func() error {
		if relics.Stranded != nil {
			return relics.Stranded
		}
		if invalid > 0 {
			return core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalid)
		}
		if relicListErr != nil {
			return relicListErr
		}
		if err := relics.invalidError(); err != nil {
			return err
		}
		if len(rejected) > 0 {
			return r.rejectedByRemote(rejected)
		}
		if err := relics.rejectedError(r.remote()); err != nil {
			return err
		}
		if changed > 0 {
			return core.Errorf(core.CategoryStaleWrite, "%d local task ref(s) changed during push", changed)
		}
		return nil
	}())
}

// relicPushFailedDetail opens the report of a relic push that could not run.
const relicPushFailedDetail = "push failed before publishing relics"

// relicPushFailed finishes a push phase whose task refs went out, given the
// error the relic push failed with, if it did, and the error the rest of the
// phase ends with. The relic push runs after the task push, so its failure
// changes nothing the task push did: the phase stays completed, says the
// relics were not published in its detail, and returns the relic failure
// after every task error, in the one error, so neither hides the other. The
// error keeps the category, and so the exit code, of the first error it
// names, and its message names the relic failure's reason in every category.
func relicPushFailed(result SyncResult, relicErr, err error) (SyncResult, error) {
	if relicErr == nil {
		return result, err
	}
	result.Detail = relicPushFailedDetail + ": " + relicErr.Error()
	if err == nil {
		return result, core.Wrap(core.CategoryOf(relicErr), relicPushFailedDetail, relicErr)
	}
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryOperational
	}
	return result, &relicPushError{
		head:  &core.Error{Category: category, Message: err.Error() + "; " + result.Detail},
		relic: relicErr,
	}
}

// relicPushError is a task error and the relic push failure of the same run.
// Its text names both. It reads as the task error's category to errors.As,
// so it exits with the task error's code, and it unwraps to the relic failure,
// so anything that failure carries, a tasks-remote refusal included, is still
// found. A core.Error wrapping the relic failure could not do both: its
// message is all a caller prints for a category other than operational, and
// putting the reason in the message as well would print it twice wherever the
// whole error is printed.
type relicPushError struct {
	head  *core.Error
	relic error
}

func (e *relicPushError) Error() string { return e.head.Message }

func (e *relicPushError) Unwrap() error { return e.relic }

// As answers a request for a *core.Error with the task error's category and
// the whole text, before errors.As reaches the relic failure's own.
func (e *relicPushError) As(target any) bool {
	typed, ok := target.(**core.Error)
	if ok {
		*typed = e.head
	}
	return ok
}

// publishedSyncStatus reports an outcome in which origin now holds the tip the
// push observed.
func publishedSyncStatus(status SyncStatus) bool {
	return status == SyncPublished || status == SyncUpToDate || status == SyncCompactionPublished
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
//
// Before the push it asks the remote which project its identity ref names,
// which costs one listing of one ref; PushTaskAfterFetch is the one caller
// allowed to skip that. After the push it lists that ref again (see
// strandedPublication).
func (r *Repository) PushTask(ctx context.Context, config core.ProjectConfig, taskID string) (SyncTaskResult, error) {
	return r.pushTask(ctx, config, taskID, false)
}

// PushTaskAfterFetch is PushTask for a short-lived command that fetched from
// the tasks remote on this handle moments before, as the CLI's mutation path
// does: that fetch compared the identity refs, so the push asks nothing first.
// A long-lived process never calls it.
func (r *Repository) PushTaskAfterFetch(ctx context.Context, config core.ProjectConfig, taskID string) (SyncTaskResult, error) {
	return r.pushTask(ctx, config, taskID, true)
}

func (r *Repository) pushTask(ctx context.Context, config core.ProjectConfig, taskID string, afterFetch bool) (SyncTaskResult, error) {
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
	// A task ref is this project's history. Publishing one to a remote holding
	// another project would write into a history it does not belong to, so the
	// identity is settled first — for free on the mutation path, where the
	// fetch this command already ran compared the two refs.
	if err := r.ensureOriginIdentityAgreement(ctx, nil, afterFetch); err != nil {
		return result, err
	}
	// The vocabulary this task's status is written against goes out with it.
	// See publishConfigLedger for why a configuration change is more urgent to
	// publish than the task change that prompted it, not less. On this path the
	// decision costs nothing: the fetch this command already ran enumerated
	// both configuration refs.
	if _, err := r.publishConfigLedger(ctx); err != nil {
		return result, err
	}

	// A compacted task is leased against the older-generation tip origin
	// holds. Where origin is expected to be comes from the tracking ref the
	// fetch before
	// this push brought up to date, which is a local read, so an ordinary task
	// still costs nothing beyond the one push.
	candidate := pushCandidate{TaskID: taskID, Head: ref.objectID}
	if tips[0].Snapshot.State.History.CompactedFrom != nil {
		tracking, err := r.TrackingTaskHeads(ctx, config)
		if err != nil {
			return result, err
		}
		lease, leased, err := r.compactionLease(ctx, config, taskID, tips[0].Snapshot, tracking[taskID])
		if err != nil {
			return result, err
		}
		if leased {
			candidate.Lease = &lease
		}
	}
	published, err := r.pushTaskHeads(ctx, config, []pushCandidate{candidate})
	if err != nil {
		return result, err
	}
	result = published.Tasks[taskID]
	if published.Stranded != nil {
		return result, published.Stranded
	}
	if published.Duplicated > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "local task ref %s was not published: %s", taskID, result.Detail)
	}
	if result.Status == SyncRejected && candidate.Lease != nil {
		// A refused lease is not replayed by the next fetch the way an
		// ordinary rejection is; its detail says what happens instead.
		return result, core.Errorf(core.CategoryOperational, "task ref %s was rejected by %s: %s", taskID, r.remote(), result.Detail)
	}
	if result.Status == SyncRejected {
		compacted, err := r.compactedOnOriginOnly(ctx, config, taskID, tips[0].Snapshot)
		if err != nil {
			return result, err
		}
		if compacted {
			// The fetch before this push fell back and kept the task on the
			// old history, and every later fetch does the same, so the
			// sentence below would be untrue.
			return result, core.Errorf(core.CategoryOperational,
				"task ref %s was rejected by %s: the task was compacted on %s and is kept on the "+
					"old history here; run \"workbook compact --adopt %s\"", taskID, r.remote(), r.remote(), taskID)
		}
		// A rejection means origin moved between this command's fetch and its
		// push. The change is already durable locally and the next fetch
		// replays it onto the new tip, so this is an ordinary operational
		// outcome rather than something the caller must act on.
		return result, core.Errorf(core.CategoryOperational,
			"task ref %s was rejected by %s; the next fetch replays it onto %s's tip", taskID, r.remote(), r.remote())
	}
	return result, nil
}

type SyncResult struct {
	Remote string          `json:"remote"`
	Status SyncPhaseStatus `json:"status,omitempty"`
	Detail string          `json:"detail,omitempty"`
	// Ignored names the refs under origin's task namespace that this version
	// does not recognize as naming one task. They are skipped so a single stray
	// ref cannot deny synchronization, and reported so a user can see what this
	// clone is not reading. A report is not an instruction to remove anything:
	// shared task history is append-only, and an entry whose PlausibleTask is
	// set names history some other Workbook can read. Only an entry no
	// project's ID format could have produced may be offered for removal.
	Ignored []IgnoredRef     `json:"ignoredRefs,omitempty"`
	Tasks   []SyncTaskResult `json:"tasks"`
	// Identity reports what this phase had to establish about origin's project
	// before publishing anything, and is omitted when there was nothing to
	// establish. Publication refuses a remote holding another project, so the
	// one outcome a caller must never miss is the opposite: origin accepting
	// task refs while refusing the identity ref beside them.
	Identity *SyncIdentityResult `json:"identity,omitempty"`
	// Config reports what this phase had to establish about origin's copy of
	// the project configuration, and is omitted when there was nothing to
	// establish.
	Config *SyncConfigResult `json:"config,omitempty"`
	// Conflicts travels with the phase that produced it but is not part of its
	// JSON. Callers lift it to the result envelope's single conflict member so
	// one command reports one list, whatever mix of phases produced it.
	Conflicts []core.Conflict `json:"-"`
	// ConfigConflicts travels the same way, in its own list. It is separate
	// because a task conflict names a task and a configuration conflict names a
	// status; merging them would give every consumer of one a member that can
	// never be populated for it.
	ConfigConflicts []core.ConfigConflict `json:"-"`
	// Relics lists the relics this phase changed or could not change. A
	// relic that did not change is not listed, so a phase that changed none
	// emits the JSON it emitted before relics were synchronized.
	Relics []SyncRelicResult `json:"relics,omitempty"`
	// RelicConflicts and RelicWarnings travel like Conflicts: callers lift
	// them to the result envelope.
	RelicConflicts []core.RelicConflict `json:"-"`
	RelicWarnings  []core.Warning       `json:"-"`
	// TaskWarnings travels like Conflicts too: one description-merged
	// warning for each description merge a replay in this phase recorded
	// and landed, in task ID order. Callers lift it to the result envelope.
	TaskWarnings []core.Warning `json:"-"`
}

// Sync fetches and validates origin's Workbook refs, replays any divergent
// local history onto the fetched tip, then publishes local Workbook task refs
// without touching code refs.
//
// Reconciliation and publication are per task. One task that needs a decision
// leaves every other task fetched, replayed, and published, matching the
// per-ref outcomes push already retains. That extends to a fetch that reports
// per-task failures: it still ran to completion and advanced every ref it
// could, so publication follows. Only a fetch that failed before completion
// skips the push phase, because there is then no validated view to publish.
func (r *Repository) Sync(ctx context.Context, config core.ProjectConfig) (SyncRunResult, error) {
	result := SyncRunResult{
		Remote: r.remote(),
		Fetch:  r.skippedSyncPhase("fetch not run"),
		Push:   r.skippedSyncPhase("push not run"),
	}

	state, fetched, fetchErr := r.fetch(ctx, config, true, true, true)
	result.Fetch = fetched
	result.Identity = state.Identity
	result.Config = state.Config
	if fetchErr != nil && fetched.Status != SyncPhaseCompleted {
		result.Push = r.skippedSyncPhase("push skipped because fetch failed")
		return result, fetchErr
	}
	if r.readOnlyRemote() {
		result.Push = r.skippedSyncPhase(readOnlyRemoteDetail(r.remote()))
		return result, fetchErr
	}

	pushed, pushErr := r.publishFetched(ctx, config, state)
	result.Push = pushed
	result.Config = mergeConfigPublication(state.Config, pushed.Config)
	if fetchErr != nil {
		return result, fetchErr
	}
	return result, pushErr
}

func (r *Repository) skippedSyncPhase(detail string) SyncResult {
	return SyncResult{Remote: r.remote(), Status: SyncPhaseSkipped, Detail: detail, Tasks: []SyncTaskResult{}}
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

// Fetch downloads origin's Workbook refs into an isolated tracking namespace,
// validates their current tips, then creates or fast-forwards compatible
// canonical refs with one compare-and-swap transaction.
//
// It runs the identity stage but never publishes: a download is not the moment
// to write to origin. Sync, which already has a publication phase, is.
func (r *Repository) Fetch(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	_, result, err := r.fetch(ctx, config, false, false, true)
	return result, err
}

// FetchTasks is Fetch without the relics, for the synchronization a task
// command runs before its change. It names the refspecs a fetch named before
// relics existed, so its Git work does not grow with the number of relics
// origin or this checkout holds: it fetches no relic ref, moves none, parks
// none, and reports no relic. The relic commands, fetch, sync and setup carry
// relics instead.
func (r *Repository) FetchTasks(ctx context.Context, config core.ProjectConfig) (SyncResult, error) {
	_, result, err := r.fetch(ctx, config, false, false, false)
	return result, err
}

// fetch runs the fetch phase. relics adds the relic refspec to the one
// `git fetch` and runs the relic stage after the task refs have moved.
func (r *Repository) fetch(
	ctx context.Context,
	config core.ProjectConfig,
	publishIdentity bool,
	announceIdentity bool,
	relics bool,
) (fetchState, SyncResult, error) {
	state := fetchState{
		Canonical: make(map[string]core.Snapshot),
		Tracking:  make(map[string]core.Snapshot),
		Outcomes:  make(map[string]SyncTaskResult),
		Withheld:  make(map[string]struct{}),
	}
	result := SyncResult{Remote: r.remote(), Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	if r.readOnlyRemote() {
		result, err := r.fetchTracking(ctx, config)
		state.Identity = result.Identity
		return state, result, err
	}

	// One fetch carries all four namespaces, or the first three for a task
	// command. Each of the two singleton refs is worth nothing if learning it
	// costs a round trip of its own on every synchronization, and a separate
	// fetch could also observe a different moment than the tasks it is supposed
	// to describe. Both singleton refspecs are globs, because Git fails a whole
	// fetch when an explicitly named source ref is missing and most origins
	// have neither ref yet. --prune applies only to the refspecs named, so a
	// fetch without the relic refspec leaves the relic tracking refs alone.
	fetchArgs := []string{"fetch", "--no-tags", "--prune", "--no-auto-maintenance", r.remote(),
		"+" + taskRefPrefix + "*:" + r.remoteTaskRefPrefix() + "*",
		r.identityFetchRefspec(),
		r.configFetchRefspec(),
	}
	if relics {
		fetchArgs = append(fetchArgs, relicFetchRefspecFor(r.remote()))
	}
	if _, err := r.Git(ctx, nil, fetchArgs...); err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	r.metadataMu.Lock()
	r.trackedIdentity = trackedIdentityHead{}
	r.metadataMu.Unlock()

	// Identity is settled before any task is looked at: every task document
	// names a project, and there is no point validating them against a project
	// this clone might not belong to.
	identityResult, identityErr := r.reconcileIdentity(ctx, publishIdentity, announceIdentity)
	state.Identity = identityResult
	if identityErr != nil {
		result, identityErr = failedSyncPhase(result, "fetch stopped before tasks were synchronized", identityErr)
		return state, result, identityErr
	}

	// Configuration comes next, between identity and tasks. It is downstream of
	// identity because a ledger naming another project is the same swapped
	// remote the identity stage refuses, and upstream of tasks because a task's
	// projected status is read through the vocabulary this stage settles. Unlike
	// identity it does not gate the tasks: see configStageOutcome for why one
	// disputed status rename must not stop a team's work from synchronizing.
	configOutcome := r.reconcileConfig(ctx, config)
	state.Config = configOutcome.Result
	result.Config = configOutcome.Result
	if configOutcome.Result != nil {
		result.ConfigConflicts = configOutcome.Result.Conflicts
	}
	if configOutcome.Fatal != nil {
		var fatal error
		result, fatal = failedSyncPhase(result, "fetch stopped before tasks were synchronized", configOutcome.Fatal)
		return state, result, fatal
	}

	// The key set is loaded here, between the configuration stage and the task
	// refs, and that ordering is the whole of this feature's fetch contract: a
	// push that delivers `key.add NEW` and a task under NEW arrives as one
	// fetch, and the classification below has to use the set as of the update
	// this run just applied. Loading it earlier — at the top of the function,
	// or lazily inside the listings — would classify that task against the set
	// this clone opened with and report a teammate's work as another project's
	// ref until somebody fetched a second time.
	//
	// The reload is explicit rather than inherited from the configuration
	// stage's own memo drop, because that drop happens only when the stage
	// moves the ledger. A ledger that moved out of band — another handle on
	// this repository fetched it, a second command or the watcher beside an
	// ordinary one — leaves this stage with nothing to do and the memo
	// untouched, and a long-lived handle would then classify a teammate's new
	// key against the set it opened with for as long as it lived. Reading the
	// ledger again here makes the ordering a property of this function rather
	// than of what the stage happened to have to do.
	r.forgetKeySet()
	keys, err := r.keySet(ctx, config)
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	canonicalRefs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	trackingRefs, ignored, err := r.listOwnedTaskRefs(ctx, config, r.remoteTaskRefPrefix())
	if err != nil {
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	// The mirror was just pruned against origin, so this is exactly the set of
	// unrecognized names origin holds right now. Recording it before the phases
	// that can fail keeps the report available whatever happens next.
	result.Ignored = ignored

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
	// Collected rather than appended straight to the report, so the whole
	// listing can be put back in ref-name order once, after the pass.
	var foreignIgnored []IgnoredRef
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
			// A fetched tip whose documents name another project is the one
			// refusal here that says nothing about this repository. Origin's
			// task namespace is shared, this project has the key the name
			// carries — somebody added it — and what arrived is intact history
			// belonging to whoever owns that project ID. Counting it as a
			// failed validation made a mistaken `key add` permanent: every
			// synchronization from then on exited nonzero, and `key retire` did
			// not undo it, because a retired key is still one of this project's
			// keys and its refs are still read. So it joins the ignored refs,
			// which is the report that already exists for a name on origin this
			// clone will not read, and the run completes.
			if projectID, foreign := foreignProjectOf(tip.Err); foreign {
				foreignIgnored = append(foreignIgnored, r.ignoredForeignProjectRef(keys, config, tip.Head.TaskID, projectID))
				continue
			}
			invalidTracking++
			state.Outcomes[tip.Head.TaskID] = SyncTaskResult{TaskID: tip.Head.TaskID, Status: SyncInvalid, Detail: tip.Err.Error()}
			continue
		}
		state.Tracking[tip.Head.TaskID] = tip.Snapshot
	}
	if len(foreignIgnored) > 0 {
		// Reported beside the names the listing above could not read at all,
		// in the one order this report promises: by ref name, so one run's
		// report reads the same as the next whichever pass found an entry.
		result.Ignored = append(result.Ignored, foreignIgnored...)
		sort.Slice(result.Ignored, func(i, j int) bool { return result.Ignored[i].Ref < result.Ignored[j].Ref })
	}

	pairs := make([]taskHeadPair, 0, len(state.Tracking))
	var compacted []reconcileRequest
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
				// This clone compacted the task and origin still holds the
				// tip the checkpoint replaced: local work origin lacks, which
				// the push phase publishes with a lease. The tracking tip stays
				// in the state, because that lease is taken against it.
				if awaitsCheckpointPublication(local, remote) {
					state.Outcomes[ref.taskID] = SyncTaskResult{TaskID: ref.taskID, Status: SyncLocalAhead}
					continue
				}
				// One side was compacted: origin's checkpoint to adopt, or
				// origin's older history to replay onto this clone's. Either
				// way the ancestry classification below, which requires one
				// generation, never sees the pair.
				if local.State.History.CompactedFrom != nil || remote.State.History.CompactedFrom != nil {
					compacted = append(compacted, reconcileRequest{TaskID: ref.taskID, Local: local, Remote: remote})
					continue
				}
				invalidTracking++
				state.Outcomes[ref.taskID] = SyncTaskResult{
					TaskID: ref.taskID,
					Status: SyncInvalid,
					Detail: generationMismatchDetail,
				}
				delete(state.Tracking, ref.taskID)
				continue
			}
			pairs = append(pairs, taskHeadPair{TaskID: ref.taskID, Local: local, Remote: remote})
		}
	}
	relationships, graph, err := r.classifyTaskHeadRelationships(ctx, config, pairs)
	if err != nil {
		result.Tasks = sortedSyncOutcomes(state.Outcomes)
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}

	updates := make([]canonicalRefUpdate, 0, len(state.Tracking))
	plannedOutcomes := make(map[string]SyncTaskResult)
	diverged := make([]reconcileRequest, 0, len(pairs))
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
				diverged = append(diverged, reconcileRequest{TaskID: ref.taskID, Local: local, Remote: remote})
			}
			break
		}
	}

	// A compacted pair contributes origin's edges, like a divergent one: its
	// replay is what decides whether a local edge survives.
	dependencies := fetchedDependencies(state, updates, append(append([]reconcileRequest(nil), diverged...), compacted...))
	reconciled, err := r.reconcileDivergentTasks(ctx, config, graph, dependencies, diverged)
	if err != nil {
		result.Tasks = sortedSyncOutcomes(state.Outcomes)
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	compactedOutcomes, err := r.reconcileCompactedTasks(ctx, config, dependencies, compacted)
	if err != nil {
		result.Tasks = sortedSyncOutcomes(state.Outcomes)
		result, err = failedSyncPhase(result, "fetch failed before completion", err)
		return state, result, err
	}
	invalidReconciled := 0
	newerReconciled := 0
	for _, outcome := range compactedOutcomes {
		if outcome.Update != nil {
			updates = append(updates, *outcome.Update)
			plannedOutcomes[outcome.TaskID] = outcome.Result
		} else {
			state.Outcomes[outcome.TaskID] = outcome.Result
		}
		if outcome.Conflict != nil {
			result.Conflicts = append(result.Conflicts, *outcome.Conflict)
		}
		if outcome.Withhold {
			state.Withheld[outcome.TaskID] = struct{}{}
		}
		switch outcome.Kind {
		case compactedInvalid:
			invalidTracking++
			delete(state.Tracking, outcome.TaskID)
		case compactedNotReplayed:
			invalidReconciled++
		case compactedNewerWriter:
			newerReconciled++
		}
	}
	for index, outcome := range reconciled {
		if outcome.Err != nil {
			status := SyncInvalid
			if core.CategoryOf(outcome.Err) == core.CategoryNewerWriter {
				status = SyncNeedsUpgrade
				newerReconciled++
			} else {
				invalidReconciled++
			}
			state.Outcomes[outcome.TaskID] = SyncTaskResult{
				TaskID: outcome.TaskID,
				Status: status,
				Detail: outcome.Err.Error(),
			}
			continue
		}
		updates = append(updates, canonicalRefUpdate{
			TaskID:     outcome.TaskID,
			Next:       outcome.Head,
			Expected:   diverged[index].Local.Head,
			ParkedRef:  outcome.ParkedRef,
			ParkedHead: outcome.Parked,
		})
		plannedOutcomes[outcome.TaskID] = reconciledSyncOutcome(outcome)
		if outcome.Conflict != nil {
			result.Conflicts = append(result.Conflicts, *outcome.Conflict)
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
	for _, outcome := range reconciled {
		if outcome.Err == nil {
			state.Canonical[outcome.TaskID] = outcome.Snapshot
		}
	}
	result.TaskWarnings = r.descriptionMergedWarnings(reconciled, compactedOutcomes)
	for _, outcome := range compactedOutcomes {
		if outcome.Update == nil {
			continue
		}
		state.Canonical[outcome.TaskID] = outcome.Snapshot
		// The transaction logged the move, naming the old tip, and that entry
		// would keep the compacted history alive. A failure to expire it is
		// left alone: the refs are right, and only reclaiming space waits.
		if outcome.Update.NoPark {
			_ = r.ExpireTaskReflog(ctx, outcome.TaskID)
		}
	}
	for taskID, outcome := range plannedOutcomes {
		state.Outcomes[taskID] = outcome
	}
	result.Tasks = sortedSyncOutcomes(state.Outcomes)
	result.Status = SyncPhaseCompleted

	// Relics come after every task ref has moved, in a transaction of their
	// own, so nothing a relic does can change a task's outcome. Whatever the
	// stage reports, the phase stays completed. A fetch without relics runs no
	// stage, and the zero stage reports nothing.
	var relicStage relicStage
	if relics {
		state.Relics, relicStage = r.syncFetchedRelics(ctx, config)
	}
	result.Relics = relicStage.Results
	result.RelicConflicts = relicStage.Conflicts
	result.RelicWarnings = relicStage.Warnings
	if len(relicStage.Ignored) > 0 {
		result.Ignored = append(result.Ignored, relicStage.Ignored...)
		sort.Slice(result.Ignored, func(i, j int) bool { return result.Ignored[i].Ref < result.Ignored[j].Ref })
	}

	// Each relic error comes directly after the task error of its category.
	if invalidCanonical > 0 {
		return state, result, core.Errorf(core.CategoryCorruptData, "%d local task ref(s) failed validation", invalidCanonical)
	}
	if invalidTracking > 0 {
		return state, result, core.Errorf(core.CategoryCorruptData, "%d fetched task ref(s) failed validation", invalidTracking)
	}
	if invalidReconciled > 0 {
		return state, result, core.Errorf(core.CategoryCorruptData, "%d divergent task history(s) could not be replayed", invalidReconciled)
	}
	if err := relicStage.corruptError(); err != nil {
		return state, result, err
	}
	// Reported after corruption and before conflicts, which is the order of how
	// much they say about the repository. Every other task in this run fetched,
	// replayed and published; these are waiting on an upgrade, and their local
	// operations are still on their refs.
	if newerReconciled > 0 {
		return state, result, core.Errorf(core.CategoryNewerWriter,
			"%d divergent task history(s) were written by a newer workbook; "+
				"upgrade workbook to replay the local changes onto them",
			newerReconciled)
	}
	if err := relicStage.newerWriterError(); err != nil {
		return state, result, err
	}
	if len(result.Conflicts) > 0 {
		return state, result, core.ConflictError(result.Conflicts)
	}
	if err := relicStage.conflictError(); err != nil {
		return state, result, err
	}
	// A relic ref that moved while the stage ran has no task error of its
	// category: a task transaction that fails ends the fetch before this.
	if err := relicStage.otherError(); err != nil {
		return state, result, err
	}
	// The configuration stage's failure is reported last, after every task has
	// been fetched, replayed and recorded. That ordering is the decision: the
	// tasks are the project's history and their synchronization is never held
	// hostage to a status rename two people disagree about, but the
	// disagreement still has to reach the caller's exit status.
	if configOutcome.Deferred != nil {
		return state, result, configOutcome.Deferred
	}
	return state, result, nil
}

// descriptionMergedWarnings reports every description merge the replays of a
// fetch landed, once the ref transaction moved the refs onto them: one
// warning per merge, in task ID order and, within a task, oldest first.
func (r *Repository) descriptionMergedWarnings(reconciled []reconcileOutcome, compacted []compactedOutcome) []core.Warning {
	type landed struct {
		taskID string
		merges int
	}
	var tasks []landed
	for _, outcome := range reconciled {
		if outcome.Err == nil && len(outcome.Merges) > 0 {
			tasks = append(tasks, landed{outcome.TaskID, len(outcome.Merges)})
		}
	}
	for _, outcome := range compacted {
		if outcome.Update != nil && len(outcome.Merges) > 0 {
			tasks = append(tasks, landed{outcome.TaskID, len(outcome.Merges)})
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].taskID < tasks[j].taskID })
	var warnings []core.Warning
	for _, task := range tasks {
		for range task.merges {
			warnings = append(warnings, core.DescriptionMergedWarning(task.taskID, r.remote()))
		}
	}
	return warnings
}

func reconciledSyncOutcome(outcome reconcileOutcome) SyncTaskResult {
	if outcome.Conflict != nil {
		return SyncTaskResult{
			TaskID: outcome.TaskID,
			Status: SyncConflicted,
			Detail: core.ConflictDetail(*outcome.Conflict),
		}
	}
	return SyncTaskResult{
		TaskID: outcome.TaskID,
		Status: SyncReconciled,
		Detail: fmt.Sprintf("replayed %d local operation(s); %d already applied upstream", outcome.Replayed, outcome.Skipped),
	}
}

// fetchedDependencies builds the dependency edges every active task will hold
// once this fetch lands. Divergent tasks contribute origin's edges rather than
// their local ones: replay is what decides whether a local edge survives, and
// checking it against an edge it might drop would invent a cycle.
func fetchedDependencies(state fetchState, updates []canonicalRefUpdate, diverged []reconcileRequest) map[string][]string {
	fetched := make(map[string]core.Snapshot, len(state.Canonical))
	for taskID, snapshot := range state.Canonical {
		fetched[taskID] = snapshot
	}
	for _, update := range updates {
		if tracking, found := state.Tracking[update.TaskID]; found {
			fetched[update.TaskID] = tracking
		}
	}
	for _, request := range diverged {
		fetched[request.TaskID] = request.Remote
	}

	dependencies := make(map[string][]string, len(fetched))
	for taskID, snapshot := range fetched {
		if snapshot.State.Task.Deleted {
			continue
		}
		dependencies[taskID] = snapshot.State.Task.Dependencies
	}
	return dependencies
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
// normal non-fast-forward rule remains the remote race guard, and a compacted
// task's lease is that guard for its checkpoint; a second remote
// listing would both duplicate work and observe a different synchronization
// boundary. It therefore reports no ignored refs: the fetch phase of the same
// run already named every one origin holds, and repeating them would say the
// same thing twice about the same namespace.
func (r *Repository) publishFetched(ctx context.Context, config core.ProjectConfig, state fetchState) (SyncResult, error) {
	result := SyncResult{Remote: r.remote(), Tasks: []SyncTaskResult{}}
	if err := r.verifyIdentity(ctx); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	// A remote marked as the place the tasks moved away from can be read and
	// nothing more: the fetch phase of this run read it because --remote named
	// it, and nothing is published to it.
	if moved := r.observedMove(); moved != nil {
		return failedSyncPhase(result, "push refused", movedRemotePublication(r.remote(), *moved))
	}
	// The configuration ledger is published before the task refs it explains,
	// from the observation the fetch phase of this same run already made.
	published, err := r.publishConfigLedger(ctx)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	result.Config = published

	// Every canonical tip is published, including one whose replay stopped
	// partway. Publishing is about what this clone durably holds, not about
	// whether more was intended: the operations that did replay are ordinary
	// history, and withholding them would make sync disagree with push about
	// the same refs while stranding work the caller already recorded.
	//
	// The one tip that is not published is a divergent task whose origin
	// history needs a newer Workbook. It is not withheld work: origin already
	// holds a tip this clone's ref is not a descendant of, so pushing it can
	// only be rejected, and reporting a rejection would describe the fetch
	// phase's refusal a second time in a word that suggests something went
	// wrong with the network.
	//
	// Fetch withholds the others it settled that way: a task origin compacted
	// that is still on the old history, and a local checkpoint over a history
	// it does not account for, or shares nothing with.
	taskIDs := make([]string, 0, len(state.Canonical))
	for taskID := range state.Canonical {
		if state.Outcomes[taskID].Status == SyncNeedsUpgrade {
			continue
		}
		if _, withheld := state.Withheld[taskID]; withheld {
			continue
		}
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	items := make(map[string]SyncTaskResult, len(taskIDs))
	observed := make(map[string]string, len(taskIDs))
	candidates := make([]pushCandidate, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		snapshot := state.Canonical[taskID]
		observed[taskID] = snapshot.Head
		tracking, tracked := state.Tracking[taskID]
		if tracked && tracking.Head == snapshot.Head {
			items[taskID] = SyncTaskResult{TaskID: taskID, Status: SyncUpToDate}
			continue
		}
		// A checkpoint fetch left local-ahead of origin's older-generation
		// tip, or reconciled onto, is published as a leased non-fast-forward,
		// against the tracking tip this same run observed.
		candidate := pushCandidate{TaskID: taskID, Head: snapshot.Head}
		if tracked {
			lease, leased, err := r.compactionLease(ctx, config, taskID, snapshot, tracking.Head)
			if err != nil {
				return failedSyncPhase(result, "push failed before completion", err)
			}
			if leased {
				candidate.Lease = &lease
			}
		}
		candidates = append(candidates, candidate)
	}
	var stranded error
	duplicated := 0
	if len(candidates) != 0 {
		pushed, err := r.pushTaskHeads(ctx, config, candidates)
		if err != nil {
			return failedSyncPhase(result, "push failed before completion", err)
		}
		for taskID, item := range pushed.Tasks {
			items[taskID] = item
		}
		stranded, duplicated = pushed.Stranded, pushed.Duplicated
	}

	finalRefs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		return failedSyncPhase(result, "push failed before completion", err)
	}
	final := make(map[string]string, len(finalRefs))
	for _, ref := range finalRefs {
		final[ref.taskID] = ref.objectID
	}
	var rejected []SyncTaskResult
	changed := 0
	for _, taskID := range taskIDs {
		item := items[taskID]
		if item.Status == SyncRejected {
			rejected = append(rejected, item)
		} else if publishedSyncStatus(item.Status) && final[taskID] != observed[taskID] {
			item.Status = SyncLocalChanged
			item.Detail = "validated task head was published, but the local ref advanced during push; run workbook push again"
			items[taskID] = item
			changed++
		}
		result.Tasks = append(result.Tasks, items[taskID])
	}
	if stranded != nil {
		result.Status = SyncPhaseCompleted
		return result, stranded
	}
	// Relics go out after the task refs, in a push of their own. Fetch read
	// every relic origin holds that changed; only a relic origin does not hold
	// is checked here.
	relicCandidates, unchecked := fetchedRelicPushCandidates(state.Relics)
	relics, relicPushErr := r.publishRelics(ctx, config, relicCandidates, unchecked, state.Relics.Tracking)
	result.Relics = relics.Results
	result.Status = SyncPhaseCompleted
	return relicPushFailed(result, relicPushErr, func() error {
		if relics.Stranded != nil {
			return relics.Stranded
		}
		if duplicated > 0 {
			return core.Errorf(core.CategoryCorruptData,
				"%d local task ref(s) record an operation twice and were not published", duplicated)
		}
		if err := relics.invalidError(); err != nil {
			return err
		}
		if len(rejected) > 0 {
			return r.rejectedByRemote(rejected)
		}
		if err := relics.rejectedError(r.remote()); err != nil {
			return err
		}
		if changed > 0 {
			return core.Errorf(core.CategoryStaleWrite, "%d local task ref(s) changed during push", changed)
		}
		return nil
	}())
}

// rejectedByRemote is the error for a push origin refused some task refs of.
// It names the first few with Git's own reason for each, as push --porcelain
// reports it — "[rejected] (fetch first)" when somebody else published first,
// "[remote rejected] (hook declined)" when a server hook refused it — so the
// one line a server logs says which it was. Git's reason is its summary, not
// what a hook printed: that text reaches only Git's own output.
func (r *Repository) rejectedByRemote(rejected []SyncTaskResult) error {
	const named = 3
	reasons := make([]string, 0, named)
	for _, item := range rejected {
		if len(reasons) == named {
			break
		}
		reason := item.TaskID
		if item.Detail != "" {
			reason += " " + item.Detail
		}
		reasons = append(reasons, reason)
	}
	message := fmt.Sprintf("%d task ref(s) were rejected by %s: %s", len(rejected), r.remote(), strings.Join(reasons, "; "))
	if more := len(rejected) - len(reasons); more > 0 {
		message += fmt.Sprintf("; and %d more", more)
	}
	return core.Errorf(core.CategoryOperational, "%s", message)
}
