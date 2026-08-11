package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// SyncConfigStatus reports what one run did about the project's configuration
// ledger.
type SyncConfigStatus string

const (
	// SyncConfigUnchanged reports that this clone and origin already agree.
	SyncConfigUnchanged SyncConfigStatus = "unchanged"
	// SyncConfigCreated reports that this clone gained a ledger it did not have.
	SyncConfigCreated SyncConfigStatus = "created"
	// SyncConfigFastForwarded reports that origin's ledger contained this
	// clone's, so the local ref moved onto it.
	SyncConfigFastForwarded SyncConfigStatus = "fast-forwarded"
	// SyncConfigLocalAhead reports configuration this clone holds and origin
	// does not.
	SyncConfigLocalAhead SyncConfigStatus = "local-ahead"
	// SyncConfigReconciled reports local-only configuration replayed onto the
	// fetched tip, leaving the canonical ref a descendant of origin's.
	SyncConfigReconciled SyncConfigStatus = "reconciled"
	// SyncConfigConflicted reports a replay that stopped on concurrent intent
	// Workbook will not decide. The conflict list carries the detail.
	SyncConfigConflicted SyncConfigStatus = "conflicted"
	// SyncConfigPublished reports that origin gained this clone's ledger.
	SyncConfigPublished SyncConfigStatus = "published"
	// SyncConfigInvalid reports a ledger tip this clone could not read or fold.
	SyncConfigInvalid SyncConfigStatus = "invalid"
)

// SyncConfigResult is the configuration stage's account of one run.
//
// It is carried as a pointer and omitted entirely when the stage had nothing to
// say, so a synchronization of a project with no ledger — which is every
// project until somebody changes a status — emits exactly the JSON it emitted
// before this stage existed.
type SyncConfigResult struct {
	Status SyncConfigStatus `json:"status"`
	Detail string           `json:"detail,omitempty"`
	Head   string           `json:"head,omitempty"`
	// Ignored names refs origin holds under the configuration ref's name that
	// this version does not read. Like the identity and task namespaces, this
	// is a report and never an instruction: the names belong to origin, and a
	// clone states what it skipped rather than refusing to run.
	Ignored []string `json:"ignoredRefs,omitempty"`
	// Unpublished reports that origin still has no copy of this clone's ledger
	// after this run tried to give it one. Publication continues in that state
	// by design, so this is the flag that keeps the decision from being
	// invisible.
	Unpublished bool `json:"unpublished,omitempty"`
	// Conflicts travels with the result but is not part of its JSON. Callers
	// lift it to the result envelope's configuration conflict member, so one
	// command reports one list whatever mix of phases produced it.
	Conflicts []core.ConfigConflict `json:"-"`
}

// Warning returns what a command should tell the user about this configuration
// outcome, if anything. It mirrors SyncIdentityResult.Warning: a run that could
// not publish has something to say whatever else went well, and so does a run
// that changed nothing and still skipped a ref origin holds.
func (result SyncConfigResult) Warning() (string, bool) {
	if result.Detail == "" {
		return "", false
	}
	if result.Unpublished || result.Status == SyncConfigUnchanged {
		return result.Detail, true
	}
	return "", false
}

// configStageOutcome pairs the stage's report with what it means for the rest
// of the run.
//
// The split is the whole ordering decision. Identity is fatal because every
// task document names a project, so replaying task history against the wrong
// one is the failure the identity stage exists to make impossible. The
// configuration ledger is not like that: it decides what the columns are
// called, and a clone that cannot fold it still holds every task, still reads
// them under core.LegacyVocabulary, and still has every reason to synchronize
// them. Refusing to sync tasks over a configuration conflict would mean one
// disputed status rename could stop a team's work from moving at all.
//
// So exactly one configuration outcome stops the run: a ledger that names a
// different project. That is not a configuration disagreement but the same
// swapped-remote condition the identity stage refuses, observed on a second
// ref, and continuing past it would write one project's operations into
// another's history. Everything else is recorded, task synchronization
// proceeds, and the failure is reported once the tasks are done.
type configStageOutcome struct {
	Result *SyncConfigResult
	// Fatal stops the run before any task is looked at.
	Fatal error
	// Deferred is returned after the task work finishes.
	Deferred error
}

// reconcileConfig converges this clone's configuration ledger with origin's.
//
// It costs no network round trip: origin's ledger rides the same fetch that
// brings the task and identity refs, and everything else here reads local refs.
// The steady state — both refs at the same commit, or neither ref existing —
// reads no objects at all and reports nothing.
func (r *Repository) reconcileConfig(ctx context.Context, config core.ProjectConfig) configStageOutcome {
	listing, err := r.listConfigRefs(ctx, configRef, remoteConfigRef)
	if err != nil {
		return configStageOutcome{
			Result:   &SyncConfigResult{Status: SyncConfigInvalid, Detail: err.Error()},
			Deferred: err,
		}
	}
	localHead, localFound := listing.Heads[configRef]
	remoteHead, remoteFound := listing.Heads[remoteConfigRef]
	r.rememberConfigObservation(localHead, remoteHead)

	outcome := r.reconcileObservedConfig(ctx, config, localHead, localFound, remoteHead, remoteFound)
	if len(listing.Ignored) > 0 {
		if outcome.Result == nil {
			outcome.Result = &SyncConfigResult{Status: SyncConfigUnchanged, Head: localHead}
		}
		outcome.Result.Ignored = listing.Ignored
		if outcome.Result.Detail == "" {
			outcome.Result.Detail = fmt.Sprintf("origin holds %d ref(s) under %s that this version does not read",
				len(listing.Ignored), configRef)
		}
	}
	if outcome.Result != nil &&
		outcome.Result.Status == SyncConfigUnchanged &&
		outcome.Result.Detail == "" &&
		!outcome.Result.Unpublished &&
		len(outcome.Result.Ignored) == 0 {
		// Nothing happened and there is nothing to say, so nothing is reported.
		// Callers omit the member entirely, which keeps the output of a project
		// whose configuration is settled byte-identical to what it was before
		// this stage existed — the same rule the identity stage follows, and
		// for the same reason: every caller parsing that output relies on it.
		outcome.Result = nil
	}
	return outcome
}

func (r *Repository) reconcileObservedConfig(
	ctx context.Context,
	config core.ProjectConfig,
	localHead string,
	localFound bool,
	remoteHead string,
	remoteFound bool,
) configStageOutcome {
	switch {
	case !localFound && !remoteFound:
		// No project has a ledger until somebody changes a status. Nothing
		// happened and there is nothing to say, so nothing is reported and the
		// run's output is byte-identical to what it was before this stage.
		return configStageOutcome{}
	case !remoteFound:
		return configStageOutcome{Result: &SyncConfigResult{Status: SyncConfigLocalAhead, Head: localHead}}
	}

	remote, err := r.readConfigRecordAt(ctx, config, remoteConfigRef, remoteHead)
	if err != nil {
		return r.configStageFailure(err)
	}
	if remote.State.ProjectID != config.ProjectID {
		err := core.Errorf(core.CategoryCorruptData,
			"origin's %s belongs to project %s, but this repository is project %s; "+
				"check `git remote -v` before synchronizing again",
			configRef, remote.State.ProjectID, config.ProjectID)
		return configStageOutcome{
			Result: &SyncConfigResult{Status: SyncConfigInvalid, Detail: err.Error(), Head: localHead},
			Fatal:  err,
		}
	}
	if !localFound {
		if err := r.createRefWithReason(ctx, configRef, remoteHead, configRefLogReason); err != nil {
			return r.configStageFailure(core.Wrap(core.CategoryStaleWrite,
				"cannot create the local Workbook configuration ledger", err))
		}
		r.rememberConfigObservation(remoteHead, remoteHead)
		r.replaceVocabulary(remote.State.Vocabulary(), remoteHead)
		return configStageOutcome{Result: &SyncConfigResult{
			Status: SyncConfigCreated,
			Head:   remoteHead,
			Detail: "adopted origin's project configuration",
		}}
	}
	if localHead == remoteHead {
		return configStageOutcome{Result: &SyncConfigResult{Status: SyncConfigUnchanged, Head: localHead}}
	}

	relationship, graph, err := r.classifyConfigHeads(ctx, localHead, remoteHead)
	if err != nil {
		return r.configStageFailure(err)
	}
	switch relationship {
	case configHeadsLocalAhead:
		return configStageOutcome{Result: &SyncConfigResult{Status: SyncConfigLocalAhead, Head: localHead}}
	case configHeadsRemoteAhead:
		if err := r.replaceConfigRef(ctx, remoteHead, localHead, nil); err != nil {
			return r.configStageFailure(err)
		}
		r.rememberConfigObservation(remoteHead, remoteHead)
		r.replaceVocabulary(remote.State.Vocabulary(), remoteHead)
		return configStageOutcome{Result: &SyncConfigResult{
			Status: SyncConfigFastForwarded,
			Head:   remoteHead,
			Detail: "fast-forwarded to origin's project configuration",
		}}
	}

	local, err := r.readConfigRecordAt(ctx, config, configRef, localHead)
	if err != nil {
		return r.configStageFailure(err)
	}
	outcome, err := r.reconcileConfigLedger(ctx, config, graph, local, remote, relationship == configHeadsUnrelated)
	if err != nil {
		return r.configStageFailure(err)
	}
	if err := r.replaceConfigRef(ctx, outcome.Head, localHead, &outcome); err != nil {
		return r.configStageFailure(err)
	}
	r.rememberConfigObservation(outcome.Head, remoteHead)
	r.replaceVocabulary(outcome.State.Vocabulary(), outcome.Head)
	return configStageOutcome{Result: reconciledConfigResult(outcome), Deferred: configReplayError(outcome)}
}

func reconciledConfigResult(outcome configReconcileOutcome) *SyncConfigResult {
	detail := fmt.Sprintf("replayed %d configuration operation(s); %d already applied upstream",
		outcome.Replayed, outcome.Skipped)
	if outcome.AdoptedRoot {
		detail = "adopted origin's configuration root, then " + detail
	}
	if len(outcome.Conflicts) > 0 {
		return &SyncConfigResult{
			Status:    SyncConfigConflicted,
			Head:      outcome.Head,
			Detail:    core.ConfigConflictDetail(outcome.Conflicts[0]),
			Conflicts: outcome.Conflicts,
		}
	}
	return &SyncConfigResult{Status: SyncConfigReconciled, Head: outcome.Head, Detail: detail}
}

func configReplayError(outcome configReconcileOutcome) error {
	if len(outcome.Conflicts) == 0 {
		return nil
	}
	return core.ConfigConflictError(outcome.Conflicts)
}

// configStageFailure records a configuration stage failure without stopping the
// run. The ledger is left exactly where it was, tasks synchronize as they
// always did, and the failure is reported once they have.
func (r *Repository) configStageFailure(err error) configStageOutcome {
	return configStageOutcome{
		Result:   &SyncConfigResult{Status: SyncConfigInvalid, Detail: err.Error()},
		Deferred: err,
	}
}

// replaceConfigRef moves the canonical configuration ref, parking the orphaned
// tip in the same transaction when a reconciliation produced one.
//
// One transaction is what keeps the parked commit from being unreachable even
// momentarily: the ref that names it is created before, not after, the ref that
// used to.
func (r *Repository) replaceConfigRef(ctx context.Context, head, expected string, outcome *configReconcileOutcome) error {
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	if outcome != nil && outcome.ParkedRef != "" && outcome.Head != expected {
		if !validParkedConfigRefName(outcome.ParkedRef) {
			return core.Errorf(core.CategoryCorruptData, "parked configuration ref %q is not a name this clone builds", outcome.ParkedRef)
		}
		if err := r.validateFullObjectID(outcome.Parked); err != nil {
			return core.Wrap(core.CategoryCorruptData, "parked configuration ref target is invalid", err)
		}
		fmt.Fprintf(&input, "create %s %s\n", outcome.ParkedRef, outcome.Parked)
	}
	fmt.Fprintf(&input, "update %s %s %s\n", configRef, head, expected)
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(ctx, input.Bytes(),
		"update-ref", "--no-deref", "--create-reflog", "-m", configRefLogReason, "--stdin"); err != nil {
		return core.Wrap(core.CategoryStaleWrite, "the configuration ledger changed during synchronization", err)
	}
	return nil
}

// rememberConfigObservation records what this command saw of both sides of the
// ledger, so a publication path can decide whether origin is behind without
// asking Git again.
func (r *Repository) rememberConfigObservation(local, remote string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.configLocalKnown = true
	r.configLocalHead = local
	r.configRemoteKnown = true
	r.configRemoteHead = remote
}

func (r *Repository) rememberConfigRemoteHead(head string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.configRemoteKnown = true
	r.configRemoteHead = head
}

// observeRemoteConfigHead reads origin's configuration ref out of a listing the
// caller already made.
//
// Origin's namespace can hold names under the configuration ref, which this
// query cannot match and which are none of a publication's business.
func (r *Repository) observeRemoteConfigHead(ctx context.Context, listing []byte) error {
	head := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(listing), "\n"), "\n") {
		if line == "" {
			continue
		}
		objectID, refName, found := strings.Cut(line, "\t")
		if !found || objectID == "" {
			return core.Errorf(core.CategoryOperational, "Git returned an invalid remote configuration record")
		}
		if refName != configRef {
			continue
		}
		if head != "" {
			return core.Errorf(core.CategoryOperational, "Git returned duplicate remote configuration records")
		}
		head = objectID
	}
	r.rememberConfigRemoteHead(head)
	return nil
}

// recordConfigReport keeps what a publication path did about the ledger, for a
// caller whose own result is task shaped.
//
// Publication is reached through Push and PushTask, whose results say nothing
// about configuration. Discarding the outcome would make the one case that most
// needs saying — origin refusing the configuration ref while accepting the task
// refs whose statuses it explains — silent on every channel.
func (r *Repository) recordConfigReport(result *SyncConfigResult) {
	if result == nil {
		return
	}
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.configReport = result
}

// ConfigReport returns what publication established about origin's copy of the
// configuration ledger in this command, if it had to establish anything.
func (r *Repository) ConfigReport() (*SyncConfigResult, bool) {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if r.configReport == nil {
		return nil, false
	}
	report := *r.configReport
	return &report, true
}

// publishConfigLedger publishes this clone's configuration ledger when origin
// is behind it.
//
// THE PUBLICATION INVARIANT. A vocabulary change is more important to publish
// than a task change, not less: a task ref says one task moved to `todo`, and
// the configuration ref is the only thing that says what `todo` is. Publishing
// the task without it leaves every teammate rendering a column they do not
// have, and leaves the correct-on-touch rule with nothing to correct towards.
// So configuration publication rides every path that publishes a task —
// `workbook push`, `workbook sync`, and the targeted push every automatically
// synchronizing mutation makes — rather than waiting for a full sync.
//
// A refusal by origin is reported and the run continues, which is the carve-out
// the identity ref already established. The ledger is durable locally, the next
// fetch replays it onto whatever origin holds by then, and refusing to publish
// tasks over a remote this clone cannot write one extra ref to would take a
// working repository away from a user who cannot fix the remote.
func (r *Repository) publishConfigLedger(ctx context.Context) (*SyncConfigResult, error) {
	head, publish, err := r.configPublicationCandidate(ctx)
	if err != nil {
		return nil, err
	}
	if !publish {
		return nil, nil
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", "origin", head+":"+configRef)
	if push.err == nil {
		r.rememberConfigRemoteHead(head)
		result := &SyncConfigResult{
			Status: SyncConfigPublished,
			Head:   head,
			Detail: "published " + configRef + " to origin",
		}
		r.recordConfigReport(result)
		return result, nil
	}
	result := &SyncConfigResult{
		Status:      SyncConfigLocalAhead,
		Head:        head,
		Unpublished: true,
		Detail: fmt.Sprintf("could not publish %s to origin: %s",
			configRef, oneLine(gitCommandResultError(push).Error())),
	}
	r.recordConfigReport(result)
	return result, nil
}

// configPublicationCandidate reports the ledger head origin is missing.
//
// It prefers what this command already observed. Every automatically
// synchronizing mutation fetches first, and that fetch's stage enumerated both
// refs, so the ordinary publication path costs no Git process at all. A caller
// that publishes without having fetched asks once.
func (r *Repository) configPublicationCandidate(ctx context.Context) (string, bool, error) {
	r.metadataMu.RLock()
	localKnown, local := r.configLocalKnown, r.configLocalHead
	remoteKnown, remote := r.configRemoteKnown, r.configRemoteHead
	r.metadataMu.RUnlock()
	if !localKnown || !remoteKnown {
		listing, err := r.listConfigRefs(ctx, configRef, remoteConfigRef)
		if err != nil {
			return "", false, err
		}
		if !localKnown {
			local = listing.Heads[configRef]
		}
		if !remoteKnown {
			remote = listing.Heads[remoteConfigRef]
		}
		r.rememberConfigObservation(local, remote)
	}
	if local == "" || local == remote {
		return "", false, nil
	}
	return local, true, nil
}

// mergeConfigPublication folds a publication outcome into the stage's report.
//
// Publication upgrades the status only when the stage left nothing unresolved.
// A run that reconciled a divergence and then published has published as its
// last word; a run that stopped on a conflict has not stopped being conflicted
// because some earlier operations reached origin.
func mergeConfigPublication(stage, published *SyncConfigResult) *SyncConfigResult {
	if published == nil {
		return stage
	}
	if stage == nil {
		return published
	}
	merged := *stage
	switch stage.Status {
	case SyncConfigConflicted, SyncConfigInvalid:
		if published.Detail != "" {
			merged.Detail = joinConfigDetails(merged.Detail, published.Detail)
		}
		merged.Unpublished = published.Unpublished
		return &merged
	}
	merged.Status = published.Status
	merged.Head = published.Head
	merged.Unpublished = published.Unpublished
	merged.Detail = joinConfigDetails(stage.Detail, published.Detail)
	return &merged
}

func joinConfigDetails(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "; " + second
	}
}
