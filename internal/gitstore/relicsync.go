package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// A fetch carries relics beside tasks. Origin's relic refs arrive in the
// tracking namespace, refs/workbook/remotes/<remote>/relics/, by the same
// `git fetch` that carries the task refs, and only that fetch writes there.
// The relic stage then runs after the task refs have moved:
//
//	refs/workbook/relics/<id>                     this clone's relic
//	refs/workbook/remotes/<remote>/relics/<id>    origin's, as just fetched
//	refs/workbook/reconciled-relics/<id>/<n>      a local tip a replay stopped on
//
// One local ref listing over the first two namespaces decides which relics
// changed: a relic whose two refs name the same object did not, and nothing of
// it is read. Each changed relic's tips are checked as a single-relic read
// checks them, a relic whose fetched tip descends from its local one is fast
// forwarded, and a relic whose two tips diverged has its local packs replayed
// onto the fetched tip. Every move goes into one compare-and-swap transaction
// of its own, after the task transaction, so nothing a relic does can change
// what happened to a task.

// relicParkedRefPrefix holds a relic's old local tip when replaying its
// changes onto origin's stopped on a conflict, as <relic-id>/<n> with n
// counting from 1. It is outside refs/workbook/relics/, so no relic history
// walk takes a parked tip for a relic, and nothing prunes it: it holds the
// only copy of the text that was not applied.
const relicParkedRefPrefix = "refs/workbook/reconciled-relics/"

// relicFetchReflogMessage opens the reflog message of the relic transaction.
// The remote's name follows it.
const relicFetchReflogMessage = "workbook: fetch relics from "

// SyncRelicResult is what a synchronization did to one relic. A relic that did
// not change is not listed.
type SyncRelicResult struct {
	RelicID string     `json:"relicId"`
	Status  SyncStatus `json:"status"`
	Detail  string     `json:"detail,omitempty"`
}

// relicFetchState is what the relic stage leaves for the push phase: every
// relic's canonical and tracking tip once the stage is over, and the relics
// it must not publish because the stage left them on a tip origin should not
// be given.
type relicFetchState struct {
	Local, Tracking map[string]string
	Withheld        map[string]struct{}
}

// relicStage is what the relic stage reports. Invalid and Newer hold the
// detail of each relic that failed its check or could not be replayed, and of
// each whose replay waits on an upgrade. Err is set when the stage itself
// failed, in which case no relic ref moved.
type relicStage struct {
	Results   []SyncRelicResult
	Conflicts []core.RelicConflict
	Warnings  []core.Warning
	Ignored   []IgnoredRef
	Invalid   []string
	Newer     []string
	Err       error
}

// corruptError, newerWriterError, conflictError and otherError are the
// stage's failures, one per place in the order a fetch reports its errors:
// each directly after the task error of the same category, and a failure of
// another category after them all.
func (stage relicStage) corruptError() error {
	if stage.Err != nil && core.CategoryOf(stage.Err) == core.CategoryCorruptData {
		return stage.Err
	}
	return relicDetailsError(core.CategoryCorruptData, stage.Invalid, "could not be synchronized")
}

func (stage relicStage) newerWriterError() error {
	return relicDetailsError(core.CategoryNewerWriter, stage.Newer, "wait on a newer workbook")
}

func (stage relicStage) conflictError() error {
	if len(stage.Conflicts) == 0 {
		return nil
	}
	return core.RelicConflictError(stage.Conflicts)
}

func (stage relicStage) otherError() error {
	if stage.Err != nil && core.CategoryOf(stage.Err) != core.CategoryCorruptData {
		return stage.Err
	}
	return nil
}

// relicDetailsError makes one error of details, each of which already names
// its relic.
func relicDetailsError(category core.Category, details []string, what string) error {
	switch len(details) {
	case 0:
		return nil
	case 1:
		return core.Errorf(category, "%s", details[0])
	default:
		return core.Errorf(category, "%d relics %s: %s", len(details), what, strings.Join(details, "; "))
	}
}

// relicDetail is err's message as a relic result's detail, which starts by
// naming the relic.
func relicDetail(relicID string, err error) string {
	message := err.Error()
	if strings.HasPrefix(message, "relic "+relicID+":") {
		return message
	}
	return "relic " + relicID + ": " + message
}

// relicRefUpdate is one move of the relic transaction. Expected is the tip
// the ref must still name, or empty for a ref that must not exist yet.
type relicRefUpdate struct {
	RelicID, Next, Expected string
	ParkedRef, ParkedHead   string
}

// syncFetchedRelics is the relic stage of a fetch. See the comment at the top
// of this file. It never fails the fetch: what it reports is folded into the
// fetch's result and error by the caller.
func (r *Repository) syncFetchedRelics(ctx context.Context, config core.ProjectConfig) (relicFetchState, relicStage) {
	state := relicFetchState{Local: map[string]string{}, Tracking: map[string]string{}, Withheld: map[string]struct{}{}}
	var stage relicStage
	// A stage that failed leaves the push phase nothing to publish: what it
	// read may be partial, and the next sync repairs what it did not do.
	fail := func(err error) (relicFetchState, relicStage) {
		stage.Err = err
		state.Local = map[string]string{}
		return state, stage
	}

	keys, err := r.keySet(ctx, config)
	if err != nil {
		return fail(err)
	}
	trackingPrefix := r.remoteRelicRefPrefix()
	records, err := r.relicRefRecords(ctx, relicRefPrefix, trackingPrefix)
	if err != nil {
		return fail(err)
	}
	for _, record := range records {
		if tracked, isTracking := strings.CutPrefix(record.name, trackingPrefix); isTracking {
			if reason := unreadableTrackedRelic(keys, tracked, record); reason != "" {
				stage.Ignored = append(stage.Ignored, ignoredRelicRef(tracked, reason))
				continue
			}
			if err := r.validateFullObjectID(record.objectID); err != nil {
				return fail(core.Wrap(core.CategoryCorruptData, "tracking relic ref object ID is invalid", err))
			}
			state.Tracking[tracked] = record.objectID
			continue
		}
		head, err := canonicalRelicHead(keys, record)
		if err != nil {
			return fail(err)
		}
		if err := r.validateFullObjectID(head.ObjectID); err != nil {
			return fail(core.Wrap(core.CategoryCorruptData, "relic ref object ID is invalid", err))
		}
		state.Local[head.RelicID] = head.ObjectID
	}

	var changed []string
	for relicID, remote := range state.Tracking {
		if local, found := state.Local[relicID]; !found || local != remote {
			changed = append(changed, relicID)
		}
	}
	if len(changed) == 0 {
		return state, stage
	}
	sort.Strings(changed)

	withhold := func(relicID string) { state.Withheld[relicID] = struct{}{} }
	invalid := func(relicID string, err error) {
		detail := relicDetail(relicID, err)
		stage.Results = append(stage.Results, SyncRelicResult{RelicID: relicID, Status: SyncInvalid, Detail: detail})
		stage.Invalid = append(stage.Invalid, detail)
		withhold(relicID)
	}
	needsUpgrade := func(relicID string) {
		detail := "relic " + relicID + ": " + r.remote() + "'s history of it was written by a newer workbook; " +
			"upgrade workbook to replay the local changes onto it"
		stage.Results = append(stage.Results, SyncRelicResult{RelicID: relicID, Status: SyncNeedsUpgrade, Detail: detail})
		stage.Newer = append(stage.Newer, detail)
		withhold(relicID)
	}

	// Every fetched tip that will become or underlie a local ref is checked,
	// and so is the local tip it meets, so a local ref this build cannot read
	// is reported and left where it is rather than built on.
	heads := make([]core.RelicHead, 0, 2*len(changed))
	for _, relicID := range changed {
		heads = append(heads, core.RelicHead{RelicID: relicID, ObjectID: state.Tracking[relicID]})
	}
	localAt := make(map[string]int, len(changed))
	for _, relicID := range changed {
		if local, found := state.Local[relicID]; found {
			localAt[relicID] = len(heads)
			heads = append(heads, core.RelicHead{RelicID: relicID, ObjectID: local})
		}
	}
	_, errs := r.verifyRelicTips(ctx, config, heads)

	var updates []relicRefUpdate
	planned := map[string]SyncRelicResult{}
	var pairs []relicReconcileRequest
	newerFetched := map[string]bool{}
	for index, relicID := range changed {
		if err := errs[index]; err != nil {
			if projectID, foreign := foreignProjectOf(err); foreign {
				stage.Ignored = append(stage.Ignored, ignoredForeignRelicRef(config, relicID, projectID))
				withhold(relicID)
				continue
			}
			if core.CategoryOf(err) != core.CategoryNewerWriter {
				invalid(relicID, err)
				continue
			}
			// A newer writer's tip is taken unchecked when nothing has to be
			// replayed onto it, as a newer task's tip is.
			newerFetched[relicID] = true
		}
		remote := state.Tracking[relicID]
		at, found := localAt[relicID]
		if !found {
			updates = append(updates, relicRefUpdate{RelicID: relicID, Next: remote})
			planned[relicID] = SyncRelicResult{RelicID: relicID, Status: SyncCreated}
			continue
		}
		if err := errs[at]; err != nil && core.CategoryOf(err) != core.CategoryNewerWriter {
			invalid(relicID, err)
			continue
		}
		pairs = append(pairs, relicReconcileRequest{RelicID: relicID, Local: state.Local[relicID], Remote: remote})
	}

	_, behind, diverged, graph, err := r.classifyRelicHeads(ctx, pairs)
	if err != nil {
		return fail(err)
	}
	for _, pair := range behind {
		updates = append(updates, relicRefUpdate{RelicID: pair.RelicID, Next: pair.Remote, Expected: pair.Local})
		planned[pair.RelicID] = SyncRelicResult{RelicID: pair.RelicID, Status: SyncFastForwarded}
	}
	replays := diverged[:0:0]
	for _, pair := range diverged {
		if newerFetched[pair.RelicID] {
			needsUpgrade(pair.RelicID)
			continue
		}
		replays = append(replays, pair)
	}
	var conflicts []core.RelicConflict
	var warnings []core.Warning
	for index, outcome := range r.reconcileDivergentRelics(ctx, config, replays, graph) {
		relicID := outcome.RelicID
		if outcome.Err != nil {
			if core.CategoryOf(outcome.Err) == core.CategoryNewerWriter {
				needsUpgrade(relicID)
			} else {
				invalid(relicID, outcome.Err)
			}
			continue
		}
		update := relicRefUpdate{RelicID: relicID, Next: outcome.Next, Expected: replays[index].Local}
		warnings = append(warnings, outcome.Warnings...)
		if outcome.Conflict != nil {
			conflict := *outcome.Conflict
			conflict.Remote = r.remote()
			update.ParkedHead = outcome.ParkedHead
			conflicts = append(conflicts, conflict)
			planned[relicID] = SyncRelicResult{RelicID: relicID, Status: SyncConflicted}
		} else if outcome.Replayed == 0 && outcome.Next == replays[index].Remote {
			// Every local change was already on origin or changed nothing, so
			// the ref simply moves to origin's tip.
			planned[relicID] = SyncRelicResult{RelicID: relicID, Status: SyncFastForwarded}
		} else {
			planned[relicID] = SyncRelicResult{
				RelicID: relicID,
				Status:  SyncReconciled,
				Detail: fmt.Sprintf("relic %s: replayed %d local change(s); %d already on %s or changing nothing",
					relicID, outcome.Replayed, outcome.Skipped, r.remote()),
			}
		}
		updates = append(updates, update)
	}

	if len(conflicts) > 0 {
		if err := r.nameParkedRelicRefs(ctx, updates, conflicts); err != nil {
			for _, update := range updates {
				withhold(update.RelicID)
			}
			return fail(err)
		}
		for _, conflict := range conflicts {
			result := planned[conflict.RelicID]
			result.Detail = core.RelicConflictDetail(conflict)
			planned[conflict.RelicID] = result
		}
	}
	if err := r.moveFetchedRelicRefs(ctx, updates); err != nil {
		for _, update := range updates {
			withhold(update.RelicID)
		}
		return fail(err)
	}

	for _, update := range updates {
		state.Local[update.RelicID] = update.Next
		stage.Results = append(stage.Results, planned[update.RelicID])
	}
	sort.Slice(stage.Results, func(i, j int) bool { return stage.Results[i].RelicID < stage.Results[j].RelicID })
	stage.Conflicts, stage.Warnings = conflicts, warnings
	return state, stage
}

// unreadableTrackedRelic says why a ref in the tracking namespace is not one
// of this project's relics, or returns "" for one that is. Anyone who can
// push can write any name there, so such a ref is skipped and reported rather
// than failing the fetch.
func unreadableTrackedRelic(keys core.KeySet, relicID string, record relicRefRecord) string {
	switch {
	case relicID == "" || strings.Contains(relicID, "/"):
		return "the ref does not name one relic"
	case core.ValidateRelicIDShape(relicID) != nil:
		return core.ValidateRelicIDShape(relicID).Error()
	case !keys.OwnsRelic(relicID):
		return fmt.Sprintf("relic ID %q carries a project key this project does not have; its keys are: %s",
			relicID, core.KeyNameList(keys))
	case record.symbolicTarget != "":
		return "the ref is symbolic, so it does not name one relic's history"
	default:
		return ""
	}
}

// ignoredForeignRelicRef reports a fetched relic whose documents name another
// project, which is that project's history rather than damage to this one.
func ignoredForeignRelicRef(config core.ProjectConfig, relicID, projectID string) IgnoredRef {
	return ignoredRelicRef(relicID, fmt.Sprintf(
		"relic ID %q carries this project's key, but its documents carry project ID %s rather than this project's %s, "+
			"so the ref belongs to another project sharing this origin",
		relicID, projectID, config.ProjectID))
}

// ignoredRelicRef reports a ref under the relic namespace that is not one of
// this project's relics. Every such ref is judged as one that may be another
// Workbook's history and is never offered for removal: relic IDs are new, so
// no rule yet tells a name no project can write from one a newer version or
// another project wrote.
func ignoredRelicRef(name, reason string) IgnoredRef {
	return IgnoredRef{Ref: relicRefPrefix + name, Reason: reason, PlausibleTask: true}
}

// nameParkedRelicRefs gives each conflicted relic's update the next free
// parked ref, one greater than the highest that relic already has, and
// records it on the conflict. It lists the parked namespace only for the
// relics that need it.
func (r *Repository) nameParkedRelicRefs(ctx context.Context, updates []relicRefUpdate, conflicts []core.RelicConflict) error {
	patterns := make([]string, len(conflicts))
	for index, conflict := range conflicts {
		patterns[index] = relicParkedRefPrefix + conflict.RelicID + "/"
	}
	records, err := r.relicRefRecords(ctx, patterns...)
	if err != nil {
		return err
	}
	next := map[string]int{}
	for _, record := range records {
		rest := strings.TrimPrefix(record.name, relicParkedRefPrefix)
		relicID, suffix, _ := strings.Cut(rest, "/")
		if index, err := strconv.Atoi(suffix); err == nil && index >= next[relicID] {
			next[relicID] = index + 1
		}
	}
	parked := map[string]string{}
	for _, conflict := range conflicts {
		index := next[conflict.RelicID]
		if index < 1 {
			index = 1
		}
		parked[conflict.RelicID] = fmt.Sprintf("%s%s/%d", relicParkedRefPrefix, conflict.RelicID, index)
	}
	for index := range updates {
		if name, found := parked[updates[index].RelicID]; found {
			updates[index].ParkedRef = name
		}
	}
	for index := range conflicts {
		conflicts[index].ParkedRef = parked[conflicts[index].RelicID]
	}
	return nil
}

// moveFetchedRelicRefs applies every relic move in one transaction, each
// against the tip the stage read. If any ref moved since, none moves, and the
// refusal names a relic that moved.
func (r *Repository) moveFetchedRelicRefs(ctx context.Context, updates []relicRefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	for _, update := range updates {
		if update.ParkedRef != "" {
			fmt.Fprintf(&input, "create %s %s\n", update.ParkedRef, update.ParkedHead)
		}
		ref := relicRefPrefix + update.RelicID
		if update.Expected == "" {
			fmt.Fprintf(&input, "create %s %s\n", ref, update.Next)
			continue
		}
		fmt.Fprintf(&input, "update %s %s %s\n", ref, update.Next, update.Expected)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "--create-reflog",
		"-m", relicFetchReflogMessage+r.remote(), "--stdin"); err != nil {
		return r.relicMovedDuringSync(ctx, updates, err)
	}
	r.noteRelicRefsMoved()
	return nil
}

// relicMovedDuringSync is the refusal for a relic transaction Git would not
// commit. It names the first relic whose ref no longer names the tip the stage
// read, or the first relic of the transaction when none can be found.
func (r *Repository) relicMovedDuringSync(ctx context.Context, updates []relicRefUpdate, cause error) error {
	if ctx.Err() != nil {
		// Git's own text names lock files and paths; the cancel is the cause.
		return core.Wrap(core.CategoryOperational, "relic refs were not moved, because the command was cancelled", ctx.Err())
	}
	relicID := updates[0].RelicID
	if records, err := r.relicRefRecords(ctx, relicRefPrefix); err == nil {
		current := make(map[string]string, len(records))
		for _, record := range records {
			current[strings.TrimPrefix(record.name, relicRefPrefix)] = record.objectID
		}
		for _, update := range updates {
			if current[update.RelicID] != update.Expected {
				relicID = update.RelicID
				break
			}
		}
	}
	return core.Errorf(core.CategoryStaleWrite,
		"relic %s: changed while it was being synchronized; run the command again", relicID)
}

// A push carries relics beside tasks, in a `git push` of its own that runs
// after the task push, so a relic origin refuses can never change what
// happened to a task. Every refspec is plain: no relic ref is ever forced,
// leased or deleted, so Git's own non-fast-forward rule is the only race
// guard, and a refused relic is replayed onto origin's tip by the next fetch.

// relicPushCandidate is one relic ref to publish at Head.
type relicPushCandidate struct {
	RelicID, Head string
}

// pushRelicHeads publishes candidates in one push and returns one outcome per
// relic, read from Git's porcelain: published, up-to-date or rejected. It does
// not check the remote afterwards; the caller lists the identity ref again
// for the relics origin accepted (strandedPublication).
func (r *Repository) pushRelicHeads(ctx context.Context, candidates []relicPushCandidate) (map[string]SyncRelicResult, error) {
	outcomes := make(map[string]SyncRelicResult, len(candidates))
	if len(candidates) == 0 {
		return outcomes, nil
	}
	if r.readOnlyRemote() {
		return nil, readOnlyRemotePublication(r.remote(), r.configuredRemote())
	}
	expected := make(map[string]string, len(candidates))
	args := []string{"push", "--porcelain", r.remote()}
	for _, candidate := range candidates {
		refName := relicRefPrefix + candidate.RelicID
		expected[refName] = candidate.RelicID
		args = append(args, candidate.Head+":"+refName)
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
	pushed, err := parsePushPorcelain(r.remote(), push.stdout, expected, nil, gitCommandResultError(push))
	if err != nil {
		return nil, err
	}
	for relicID, item := range pushed {
		outcome := SyncRelicResult{RelicID: relicID, Status: item.Status}
		if item.Status == SyncRejected {
			outcome.Detail = fmt.Sprintf("relic %s: %s refused it %s; the next sync replays any local changes onto %s's tip",
				relicID, r.remote(), item.Detail, r.remote())
		}
		outcomes[relicID] = outcome
	}
	return outcomes, nil
}

// relicPublication is what one publication of relic refs did.
type relicPublication struct {
	// Results has one entry per relic that was a candidate, by relic ID.
	Results []SyncRelicResult
	// Invalid and Rejected hold the detail of each relic not published
	// because it failed its check, and of each origin refused.
	Invalid, Rejected []string
	// Stranded is set when origin, listed again after it accepted relic refs,
	// turned out to be the place the tasks moved away from.
	Stranded error
}

func (p relicPublication) invalidError() error {
	return relicDetailsError(core.CategoryCorruptData, p.Invalid, "were not published")
}

func (p relicPublication) rejectedError(remote string) error {
	return relicDetailsError(core.CategoryOperational, p.Rejected, "were refused by "+remote)
}

// publishRelics checks the candidates named in unchecked as a single-relic
// read does, and publishes every candidate that passes in one push. A tip
// that fails is reported invalid and is not published. A tip a newer Workbook
// wrote is published unchecked: it is history this clone holds and cannot
// judge, and publishing it rewrites nothing. The error is for a publication
// that could not run.
func (r *Repository) publishRelics(
	ctx context.Context,
	config core.ProjectConfig,
	candidates []relicPushCandidate,
	unchecked map[string]bool,
) (relicPublication, error) {
	var publication relicPublication
	if len(candidates) == 0 {
		return publication, nil
	}
	results := make(map[string]SyncRelicResult, len(candidates))
	var heads []core.RelicHead
	for _, candidate := range candidates {
		if unchecked[candidate.RelicID] {
			heads = append(heads, core.RelicHead{RelicID: candidate.RelicID, ObjectID: candidate.Head})
		}
	}
	if len(heads) > 0 {
		_, errs := r.verifyRelicTips(ctx, config, heads)
		for index, head := range heads {
			if err := errs[index]; err != nil && core.CategoryOf(err) != core.CategoryNewerWriter {
				detail := relicDetail(head.RelicID, err)
				results[head.RelicID] = SyncRelicResult{RelicID: head.RelicID, Status: SyncInvalid, Detail: detail}
			}
		}
	}
	kept := candidates[:0:0]
	for _, candidate := range candidates {
		if _, invalid := results[candidate.RelicID]; !invalid {
			kept = append(kept, candidate)
		}
	}
	pushed, err := r.pushRelicHeads(ctx, kept)
	if err != nil {
		return publication, err
	}
	var accepted []string
	for relicID, outcome := range pushed {
		results[relicID] = outcome
		if outcome.Status == SyncPublished {
			accepted = append(accepted, relicID)
		}
	}
	for _, candidate := range candidates {
		outcome := results[candidate.RelicID]
		publication.Results = append(publication.Results, outcome)
		switch outcome.Status {
		case SyncInvalid:
			publication.Invalid = append(publication.Invalid, outcome.Detail)
		case SyncRejected:
			publication.Rejected = append(publication.Rejected, outcome.Detail)
		}
	}
	sort.Slice(publication.Results, func(i, j int) bool {
		return publication.Results[i].RelicID < publication.Results[j].RelicID
	})
	if len(accepted) > 0 {
		sort.Strings(accepted)
		publication.Stranded = r.strandedRelicPublication(ctx, accepted)
	}
	return publication, nil
}

// localRelicPushCandidates reads this clone's relic refs, as listed, and
// returns the ones whose tip is not the one origin holds, sorted by relic ID.
func localRelicPushCandidates(keys core.KeySet, records []relicRefRecord, remote map[string]string) ([]relicPushCandidate, error) {
	candidates := make([]relicPushCandidate, 0, len(records))
	for _, record := range records {
		head, err := canonicalRelicHead(keys, record)
		if err != nil {
			return nil, err
		}
		if remote[head.RelicID] == head.ObjectID {
			continue
		}
		candidates = append(candidates, relicPushCandidate{RelicID: head.RelicID, Head: head.ObjectID})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RelicID < candidates[j].RelicID })
	return candidates, nil
}

// withholdRelicsOriginAlreadyJudged drops from a plain push's candidates the
// relics whose push origin would refuse for a reason the last fetch already
// saw: origin's tip, as listed, is the tip that fetch left in the tracking
// ref, and either the local tip is an ancestor of it, so there is nothing to
// publish, or the two diverged and origin's tip fails its check, so the next
// sync withholds the relic rather than replaying onto it. The second kind is
// returned as an invalid result that says it was not pushed. A tip origin
// holds that this clone never fetched is left to the push, whose refusal the
// next sync acts on.
func (r *Repository) withholdRelicsOriginAlreadyJudged(
	ctx context.Context,
	config core.ProjectConfig,
	candidates []relicPushCandidate,
	remote map[string]string,
) ([]relicPushCandidate, []SyncRelicResult, error) {
	listed := false
	for _, candidate := range candidates {
		listed = listed || remote[candidate.RelicID] != ""
	}
	if !listed {
		// Nothing origin holds to compare with, so no read of the tracking refs.
		return candidates, nil, nil
	}
	trackingPrefix := r.remoteRelicRefPrefix()
	records, err := r.relicRefRecords(ctx, trackingPrefix)
	if err != nil {
		return nil, nil, err
	}
	tracking := make(map[string]string, len(records))
	for _, record := range records {
		tracking[strings.TrimPrefix(record.name, trackingPrefix)] = record.objectID
	}
	var pairs []relicReconcileRequest
	for _, candidate := range candidates {
		if head := remote[candidate.RelicID]; head != "" && tracking[candidate.RelicID] == head {
			pairs = append(pairs, relicReconcileRequest{RelicID: candidate.RelicID, Local: candidate.Head, Remote: head})
		}
	}
	if len(pairs) == 0 {
		return candidates, nil, nil
	}
	_, behind, diverged, _, err := r.classifyRelicHeads(ctx, pairs)
	if err != nil {
		return nil, nil, err
	}
	dropped := make(map[string]bool, len(behind)+len(diverged))
	for _, pair := range behind {
		dropped[pair.RelicID] = true
	}
	var withheld []SyncRelicResult
	if len(diverged) > 0 {
		heads := make([]core.RelicHead, len(diverged))
		for index, pair := range diverged {
			heads[index] = core.RelicHead{RelicID: pair.RelicID, ObjectID: pair.Remote}
		}
		_, errs := r.verifyRelicTips(ctx, config, heads)
		for index, pair := range diverged {
			err := errs[index]
			if err == nil || core.CategoryOf(err) == core.CategoryNewerWriter {
				continue
			}
			dropped[pair.RelicID] = true
			reason := strings.TrimPrefix(relicDetail(pair.RelicID, err), "relic "+pair.RelicID+": ")
			withheld = append(withheld, SyncRelicResult{
				RelicID: pair.RelicID,
				Status:  SyncInvalid,
				Detail: fmt.Sprintf("relic %s: not pushed, because %s's tip of it fails its check and the next sync withholds it: %s",
					pair.RelicID, r.remote(), reason),
			})
		}
	}
	kept := candidates[:0:0]
	for _, candidate := range candidates {
		if !dropped[candidate.RelicID] {
			kept = append(kept, candidate)
		}
	}
	return kept, withheld, nil
}

// fetchedRelicPushCandidates returns the relics a sync's push phase
// publishes: each whose local tip is not the tip just fetched, unless the
// fetch withheld it. A relic the fetch reported invalid or waiting on an
// upgrade is withheld; a conflicted one is not, because its ref is a clean
// descendant of origin's tip. unchecked names the candidates the fetch never
// read, which are the relics origin does not hold.
func fetchedRelicPushCandidates(state relicFetchState) ([]relicPushCandidate, map[string]bool) {
	var candidates []relicPushCandidate
	unchecked := map[string]bool{}
	for relicID, local := range state.Local {
		if _, withheld := state.Withheld[relicID]; withheld {
			continue
		}
		tracking, tracked := state.Tracking[relicID]
		if tracked && tracking == local {
			continue
		}
		if !tracked {
			unchecked[relicID] = true
		}
		candidates = append(candidates, relicPushCandidate{RelicID: relicID, Head: local})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RelicID < candidates[j].RelicID })
	return candidates, unchecked
}

// PushRelic publishes one relic ref to origin, as PushTask publishes one task
// ref: the tip alone is checked, origin's identity is settled and the
// configuration ledger goes out first, and origin's identity ref is listed
// again after it accepts the relic. It issues no listing of origin's relics;
// Git's non-fast-forward rule is the race guard, and its porcelain reports a
// relic origin already holds as up-to-date.
func (r *Repository) PushRelic(ctx context.Context, config core.ProjectConfig, relicID string) (SyncRelicResult, error) {
	return r.pushRelic(ctx, config, relicID, false)
}

// PushRelicAfterFetch is PushRelic for a short-lived command that fetched from
// the tasks remote on this handle moments before, as PushTaskAfterFetch is.
func (r *Repository) PushRelicAfterFetch(ctx context.Context, config core.ProjectConfig, relicID string) (SyncRelicResult, error) {
	return r.pushRelic(ctx, config, relicID, true)
}

func (r *Repository) pushRelic(ctx context.Context, config core.ProjectConfig, relicID string, afterFetch bool) (SyncRelicResult, error) {
	result := SyncRelicResult{RelicID: relicID}
	if err := r.verifyIdentity(ctx); err != nil {
		return result, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return result, err
	}
	if err := core.ValidateRelicIDShape(relicID); err != nil {
		return result, core.Wrap(core.CategoryValidation, "relic "+relicID+": the ID is invalid", err)
	}
	head, err := r.relicRefValue(ctx, relicRefPrefix+relicID)
	if err != nil {
		return result, err
	}
	if head == "" {
		return result, core.Errorf(core.CategoryNotFound, "relic %s: there is no Workbook ref to publish", relicID)
	}
	_, errs := r.verifyRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: head}})
	if err := errs[0]; err != nil && core.CategoryOf(err) != core.CategoryNewerWriter {
		result.Status, result.Detail = SyncInvalid, relicDetail(relicID, err)
		return result, core.Errorf(core.CategoryCorruptData, "%s; it was not published", result.Detail)
	}
	if err := r.ensureOriginIdentityAgreement(ctx, nil, afterFetch); err != nil {
		return result, err
	}
	if _, err := r.publishConfigLedger(ctx); err != nil {
		return result, err
	}
	pushed, err := r.pushRelicHeads(ctx, []relicPushCandidate{{RelicID: relicID, Head: head}})
	if err != nil {
		return result, err
	}
	result = pushed[relicID]
	if result.Status == SyncPublished {
		if err := r.strandedRelicPublication(ctx, []string{relicID}); err != nil {
			return result, err
		}
	}
	if result.Status == SyncRejected {
		return result, core.Errorf(core.CategoryOperational, "%s", result.Detail)
	}
	return result, nil
}

// FillRelicConflictBodies returns conflicts with the content of every body
// version a body conflict names, read from the blob its Object names: the
// text when the bytes are UTF-8 and their base64 when they are not, as
// core.SetRelicBodyVersionContent fills them. A synchronization never opens a
// body; this is the one read of them, made only to print the report, in one
// `cat-file --batch`.
//
// Each blob is refused above core.MaxRelicBodyBytes from its batch header,
// before anything is allocated for it. A version that is refused, missing or
// not a blob keeps no content and is named in the error, which says every
// such version; the conflicts returned beside it hold all the content that
// could be read, so a caller can still report them. The conflicts passed in
// are not changed.
func (r *Repository) FillRelicConflictBodies(ctx context.Context, conflicts []core.RelicConflict) ([]core.RelicConflict, error) {
	filled := append([]core.RelicConflict(nil), conflicts...)
	type request struct {
		relicID, part, side string
		version             *core.RelicBodyVersion
	}
	var requests []request
	var problems []string
	for index := range filled {
		conflict := &filled[index]
		if conflict.Body == nil {
			continue
		}
		body := *conflict.Body
		conflict.Body = &body
		if body.Base != nil {
			base := *body.Base
			body.Base = &base
			requests = append(requests, request{conflict.RelicID, conflict.Part, "base", body.Base})
		}
		requests = append(requests,
			request{conflict.RelicID, conflict.Part, "ours", &body.Ours},
			request{conflict.RelicID, conflict.Part, "theirs", &body.Theirs})
	}
	var valid []request
	var input bytes.Buffer
	for _, item := range requests {
		if _, err := decodeObjectID(item.version.Object); err != nil {
			problems = append(problems, fmt.Sprintf("relic %s: part %s: the %s body names no valid Git object",
				item.relicID, item.part, item.side))
			continue
		}
		valid = append(valid, item)
		input.WriteString(item.version.Object)
		input.WriteByte('\n')
	}
	if len(valid) > 0 {
		batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
			_, err := writer.Write(input.Bytes())
			return err
		})
		if err != nil {
			return filled, err
		}
		defer batch.Close()
		for _, item := range valid {
			object, err := readBatchObjectWithin(batch.Reader(), core.MaxRelicBodyBytes)
			if err != nil {
				return filled, batch.ReadFailure("cannot read relic bodies from Git batch", err)
			}
			switch {
			case object.missing:
				problems = append(problems, fmt.Sprintf("relic %s: part %s: the %s body %s is missing",
					item.relicID, item.part, item.side, item.version.Object))
			case object.refused != nil:
				problems = append(problems, fmt.Sprintf("relic %s: part %s: the %s body is %d bytes, over the %d byte limit for a body",
					item.relicID, item.part, item.side, object.size, core.MaxRelicBodyBytes))
			case object.kind != "blob":
				problems = append(problems, fmt.Sprintf("relic %s: part %s: the %s body %s is a %s, not a blob",
					item.relicID, item.part, item.side, item.version.Object, object.kind))
			default:
				core.SetRelicBodyVersionContent(item.version, object.contents)
			}
		}
		if err := batch.Finish(); err != nil {
			return filled, err
		}
	}
	if len(problems) > 0 {
		return filled, core.Errorf(core.CategoryCorruptData, "%s", strings.Join(problems, "; "))
	}
	return filled, nil
}
