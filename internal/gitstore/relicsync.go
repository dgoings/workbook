package gitstore

import (
	"bytes"
	"context"
	"fmt"
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
	fail := func(err error) (relicFetchState, relicStage) {
		stage.Err = err
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
				stage.Ignored = append(stage.Ignored, IgnoredRef{Ref: relicRefPrefix + tracked, Reason: reason, PlausibleTask: true})
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
	return IgnoredRef{
		Ref: relicRefPrefix + relicID,
		Reason: fmt.Sprintf(
			"relic ID %q carries this project's key, but its documents carry project ID %s rather than this project's %s, "+
				"so the ref belongs to another project sharing this origin",
			relicID, projectID, config.ProjectID),
		PlausibleTask: true,
	}
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
	return nil
}

// relicMovedDuringSync is the refusal for a relic transaction Git would not
// commit. It names the first relic whose ref no longer names the tip the stage
// read, or the first relic of the transaction when none can be found.
func (r *Repository) relicMovedDuringSync(ctx context.Context, updates []relicRefUpdate, cause error) error {
	if ctx.Err() != nil {
		return core.Wrap(core.CategoryOperational, "relic refs were not moved", cause)
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
