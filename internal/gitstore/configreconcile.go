package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// configDivergence names how one clone's ledger sits against origin's.
type configDivergence string

const (
	configHeadsEqual       configDivergence = "equal"
	configHeadsLocalAhead  configDivergence = "local-ahead"
	configHeadsRemoteAhead configDivergence = "remote-ahead"
	configHeadsDiverged    configDivergence = "diverged"
	// configHeadsUnrelated is the shape only a lazily seeded singleton can
	// reach: two clones each wrote a genesis root while neither could see the
	// other, so the two histories share no commit at all. It is not a conflict
	// inside one history and cannot be replayed as one.
	configHeadsUnrelated configDivergence = "unrelated"
)

// configReconcileOutcome reports one replay of the local ledger onto origin's.
type configReconcileOutcome struct {
	// Head is the canonical value the caller must move the ref to: origin's tip
	// when every local pack was a no-op or the first one conflicted, and
	// otherwise the last replayed commit.
	Head string
	// Parked is the orphaned local tip, retained so the intent a conflict
	// dropped can still be read.
	Parked    string
	ParkedRef string
	Replayed  int
	Skipped   int
	// AdoptedRoot reports that the two ledgers had different genesis roots and
	// this replay adopted origin's.
	AdoptedRoot bool
	State       core.ConfigStateDocument
	Conflicts   []core.ConfigConflict
}

// classifyConfigHeads decides how the two ledger tips relate, with one Git
// ancestry walk, and returns the parent graph that walk produced so the replay
// can find the shared base without a second one.
//
// It reuses the task path's parseParentGraph, graphReaches and commitChain
// deliberately: a configuration ledger is a single-parent chain of immutable
// operation commits, which is the same shape a task ref holds, and a second
// implementation of "which of these two commits contains the other" is a second
// chance to answer it differently.
func (r *Repository) classifyConfigHeads(
	ctx context.Context,
	localHead, remoteHead string,
) (configDivergence, map[string][]string, error) {
	if localHead == remoteHead {
		return configHeadsEqual, nil, nil
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return "", nil, err
	}
	var input bytes.Buffer
	fmt.Fprintln(&input, localHead)
	fmt.Fprintln(&input, remoteHead)
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return "", nil, core.Wrap(core.CategoryCorruptData, "cannot classify configuration ledger heads", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return "", nil, err
	}
	for objectID, parents := range graph {
		if err := r.validateFullObjectID(objectID); err != nil {
			return "", nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
		}
		for _, parent := range parents {
			if err := r.validateFullObjectID(parent); err != nil {
				return "", nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
			}
		}
	}
	if err := validateCompleteParentGraph(graph); err != nil {
		return "", nil, err
	}
	if _, found := graph[localHead]; !found {
		return "", nil, core.Errorf(core.CategoryCorruptData, "Git parent graph omitted the local configuration head")
	}
	if _, found := graph[remoteHead]; !found {
		return "", nil, core.Errorf(core.CategoryCorruptData, "Git parent graph omitted origin's configuration head")
	}
	switch {
	case graphReaches(graph, remoteHead, localHead):
		return configHeadsRemoteAhead, graph, nil
	case graphReaches(graph, localHead, remoteHead):
		return configHeadsLocalAhead, graph, nil
	}
	_, _, related, err := localOnlyConfigCommits(graph, localHead, remoteHead)
	if err != nil {
		return "", nil, err
	}
	if !related {
		return configHeadsUnrelated, graph, nil
	}
	return configHeadsDiverged, graph, nil
}

// localOnlyConfigCommits returns the local commits origin does not have, oldest
// first, and reports whether the two chains meet at all. Both histories are
// single-parent chains, which is what lets one linear walk answer this without a
// Git process.
//
// Sharing no commit is not corruption here, which is the one place the
// configuration ledger differs from a task ref. A task ref is created by
// exactly one clone and copied everywhere else, so two unrelated task histories
// mean something is wrong. The configuration ledger is seeded lazily by
// whichever clone first changes a status, so two roots is an ordinary race
// between two offline people and has a resolution.
func localOnlyConfigCommits(graph map[string][]string, localHead, remoteHead string) (string, []string, bool, error) {
	remote, err := commitChain(graph, remoteHead)
	if err != nil {
		return "", nil, false, core.Wrap(core.CategoryCorruptData, "cannot walk origin's configuration history", err)
	}
	shared := make(map[string]struct{}, len(remote))
	for _, objectID := range remote {
		shared[objectID] = struct{}{}
	}
	local, err := commitChain(graph, localHead)
	if err != nil {
		return "", nil, false, core.Wrap(core.CategoryCorruptData, "cannot walk the local configuration history", err)
	}
	for index, objectID := range local {
		if _, found := shared[objectID]; !found {
			continue
		}
		return objectID, reverseCommits(local[:index]), true, nil
	}
	return "", nil, false, nil
}

// unrelatedLocalConfigCommits returns the local commits to replay when the two
// ledgers have different genesis roots, oldest first and without the root.
//
// Adopting origin's root rather than trying to merge the two is the whole
// resolution, and the reason is a division of labour rather than a tiebreak.
// A genesis root is bookkeeping: it says "this project's configuration started
// from this vocabulary", which for a lazily seeded ledger is always
// core.LegacyVocabulary and is therefore the same statement on both sides. The
// operations after it are intent — somebody added, renamed or removed a status
// — and intent must survive. Origin won the publication race, so its root is
// the project's root, and this clone's operations are replayed onto it. Nothing
// is lost that anybody chose.
func unrelatedLocalConfigCommits(graph map[string][]string, localHead string) ([]string, error) {
	local, err := commitChain(graph, localHead)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot walk the local configuration history", err)
	}
	if len(local) == 0 {
		return nil, core.Errorf(core.CategoryCorruptData, "the local configuration ledger has no commits")
	}
	// The oldest commit is this clone's own genesis. It is dropped rather than
	// replayed: config.genesis requires no parent, so replaying one onto
	// origin's root would be refused by the fold, and the vocabulary it carries
	// is the same starting point origin's root already recorded.
	return reverseCommits(local[:len(local)-1]), nil
}

func reverseCommits(newestFirst []string) []string {
	oldestFirst := make([]string, len(newestFirst))
	for offset := range newestFirst {
		oldestFirst[len(newestFirst)-1-offset] = newestFirst[offset]
	}
	return oldestFirst
}

// reconcileConfigLedger replays this clone's local-only configuration packs
// onto origin's tip.
//
// It writes commit objects but never moves a ref: the caller applies the
// outcome in the same compare-and-swap transaction that parks the orphaned
// local tip, so an interrupted run leaves unreferenced objects rather than a
// ledger whose history has been replaced without its predecessor being kept.
func (r *Repository) reconcileConfigLedger(
	ctx context.Context,
	config core.ProjectConfig,
	graph map[string][]string,
	local, remote configRecord,
	unrelated bool,
) (configReconcileOutcome, error) {
	outcome := configReconcileOutcome{
		Head:        remote.Head,
		Parked:      local.Head,
		State:       remote.State,
		AdoptedRoot: unrelated,
	}

	var localOnly []string
	var err error
	if unrelated {
		localOnly, err = unrelatedLocalConfigCommits(graph, local.Head)
	} else {
		var related bool
		_, localOnly, related, err = localOnlyConfigCommits(graph, local.Head, remote.Head)
		if err == nil && !related {
			err = core.Errorf(core.CategoryCorruptData,
				"the local and fetched configuration ledgers share no common commit")
		}
	}
	if err != nil {
		return configReconcileOutcome{}, err
	}
	if len(localOnly) > core.MaxConfigLedgerReplayCommits {
		return configReconcileOutcome{}, core.Errorf(core.CategoryOperational,
			"the local configuration ledger is %d commits ahead of origin, over this clone's replay budget of %d "+
				"(MaxConfigLedgerReplayCommits); the ledger is unchanged, and raising the bound is the only thing that folds it",
			len(localOnly), core.MaxConfigLedgerReplayCommits)
	}
	index, err := r.nextParkedConfigRefIndex(ctx)
	if err != nil {
		return configReconcileOutcome{}, err
	}
	outcome.ParkedRef = fmt.Sprintf("%s%d", parkedConfigRefPrefix, index)
	if len(localOnly) == 0 {
		return outcome, nil
	}

	records, err := r.readConfigRecords(ctx, config, configRef, localOnly)
	if err != nil {
		return configReconcileOutcome{}, err
	}

	replay := configReplay{parent: remote}
	for _, record := range records {
		done, err := replay.next(ctx, r, record)
		if err != nil {
			return configReconcileOutcome{}, err
		}
		if done {
			break
		}
	}
	outcome.Head = replay.parent.Head
	outcome.State = replay.parent.State
	outcome.Replayed = replay.replayed
	outcome.Skipped = replay.skipped
	outcome.Conflicts = replay.conflicts
	return outcome, nil
}

// configReplay carries the state one ledger's replay advances through.
type configReplay struct {
	parent    configRecord
	replayed  int
	skipped   int
	conflicts []core.ConfigConflict
}

// next replays one local configuration pack, and reports done when replay must
// stop.
//
// Replay stops at the first conflict, exactly as a task's does. The alternative
// — folding past a status change the fetched history contradicts — would keep
// applying the author's later operations to a vocabulary the author never saw,
// and every one of them would be a guess about what they meant.
func (replay *configReplay) next(ctx context.Context, r *Repository, local configRecord) (bool, error) {
	view := newConfigView(replay.parent.State.Config.Vocabulary)
	for _, operation := range local.Operation.Operations {
		if conflict := classifyConfigOperation(view, operation); conflict != nil {
			replay.conflicts = append(replay.conflicts, *conflict)
			return true, nil
		}
	}

	// Only the logical clock and the history generation are rewritten. Actor,
	// wall time and operation IDs are the record of what somebody actually did.
	// The generation moves because it may have to: adopting origin's root means
	// adopting the generation that root minted, and a pack whose generation
	// disagreed with its parent's would be refused by the fold.
	pack, err := core.NewConfigOperationPack(
		replay.parent.State.ProjectID,
		replay.parent.State.History.Generation,
		local.Operation.Actor.ID,
		replay.parent.State.LogicalClock+1,
		local.Operation.WallTime,
		local.Operation.Operations,
	)
	if err != nil {
		return false, core.Wrap(core.CategoryCorruptData,
			fmt.Sprintf("cannot replay configuration commit %s onto the fetched tip", local.Head), err)
	}
	state, err := core.ApplyConfig(&replay.parent.State, pack)
	if err != nil {
		return false, core.Wrap(core.CategoryCorruptData,
			fmt.Sprintf("cannot replay configuration commit %s onto the fetched tip", local.Head), err)
	}
	// Arity is the one condition that cannot be seen before the fold: it is a
	// property of the result, and ApplyConfig repairs it rather than refusing,
	// picking a status by position because position is what every clone agrees
	// on. The repair is reported here — before any object is written, so a
	// conflicted replay leaves the ledger exactly where the clean part of the
	// replay reached.
	if conflicts := classifyConfigArity(replay.parent.State.Config.Vocabulary, state.Config.Vocabulary, pack); len(conflicts) > 0 {
		replay.conflicts = append(replay.conflicts, conflicts...)
		return true, nil
	}
	if reflect.DeepEqual(replay.parent.State.Config, state.Config) {
		// The fetched history already contains this pack's effect. Recording it
		// again would add a commit that says nothing.
		replay.skipped++
		return false, nil
	}

	head, err := r.writeConfigObjects(ctx, replay.parent.Head, pack, state, configReplaySubject)
	if err != nil {
		return false, err
	}
	replay.parent = configRecord{Head: head, Operation: pack, State: state}
	replay.replayed++
	return false, nil
}

// configView is the parent vocabulary a replayed operation is classified
// against.
//
// It is built from the stored document rather than from core's fold state,
// because classification is a question about two intents and the fold is a
// question about one result. Reading the document also keeps the two walks the
// classification needs — through renames only, and through renames and
// retirements together — distinguishable, which is the distinction every
// configuration conflict turns on.
type configView struct {
	live    map[core.Status]core.StatusDefinition
	aliases map[core.Status]core.Status
	retired map[core.Status]core.Status
}

func newConfigView(document core.VocabularyDocument) configView {
	view := configView{
		live:    make(map[core.Status]core.StatusDefinition, len(document.Statuses)),
		aliases: make(map[core.Status]core.Status, len(document.Aliases)),
		retired: make(map[core.Status]core.Status, len(document.Retired)),
	}
	for _, definition := range document.Statuses {
		view.live[definition.Status] = definition
	}
	for _, alias := range document.Aliases {
		view.aliases[alias.From] = alias.To
	}
	for _, entry := range document.Retired {
		view.retired[entry.Status] = entry.Destination
	}
	return view
}

// resolveSubject walks a status through rename aliases only, and stops at a
// retirement, exactly as the fold's own subject resolution does.
func (view configView) resolveSubject(status core.Status) (core.Status, bool) {
	if _, live := view.live[status]; live {
		return status, true
	}
	seen := make(map[core.Status]struct{}, len(view.aliases))
	current := status
	for range len(view.aliases) + 1 {
		next, aliased := view.aliases[current]
		if !aliased {
			return status, false
		}
		if _, repeated := seen[next]; repeated {
			return status, false
		}
		seen[next] = struct{}{}
		if _, live := view.live[next]; live {
			return next, true
		}
		current = next
	}
	return status, false
}

// resolve walks a status through both chains to the live status it now means.
func (view configView) resolve(status core.Status) (core.Status, bool) {
	if _, live := view.live[status]; live {
		return status, true
	}
	seen := make(map[core.Status]struct{}, len(view.aliases)+len(view.retired))
	current := status
	for range len(view.aliases) + len(view.retired) + 1 {
		next, forwarded := view.forwarded(current)
		if !forwarded {
			return status, false
		}
		if _, repeated := seen[next]; repeated {
			return status, false
		}
		seen[next] = struct{}{}
		if _, live := view.live[next]; live {
			return next, true
		}
		current = next
	}
	return status, false
}

func (view configView) forwarded(status core.Status) (core.Status, bool) {
	if to, aliased := view.aliases[status]; aliased {
		return to, true
	}
	to, retired := view.retired[status]
	return to, retired
}

// retirementOf reports the destination the fetched history forwarded a status
// into, following renames on the way. A status that is live, or unknown, has
// none.
func (view configView) retirementOf(status core.Status) (core.Status, bool) {
	seen := make(map[core.Status]struct{}, len(view.aliases)+len(view.retired))
	current := status
	for range len(view.aliases) + len(view.retired) + 1 {
		if _, live := view.live[current]; live {
			return "", false
		}
		if destination, retired := view.retired[current]; retired {
			return destination, true
		}
		next, aliased := view.aliases[current]
		if !aliased {
			return "", false
		}
		if _, repeated := seen[next]; repeated {
			return "", false
		}
		seen[next] = struct{}{}
		current = next
	}
	return "", false
}

// classifyConfigOperation reports the one situation, if any, in which a local
// operation expresses intent the fetched history already contradicts.
//
// It returns nil for every operation the fold converges on, including the ones
// it converges on by doing nothing: an operation whose subject the fetched
// history never had is a no-op nobody needs to decide about. What earns a
// conflict is a pair of intents that both happened and disagree.
func classifyConfigOperation(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	switch operation.Type {
	case core.ConfigStatusAdd:
		return classifyConfigAdd(view, operation)
	case core.ConfigStatusRename:
		return classifyConfigRename(view, operation)
	case core.ConfigStatusRemove:
		return classifyConfigRemove(view, operation)
	case core.ConfigStatusRelabel, core.ConfigStatusReorder, core.ConfigStatusTag, core.ConfigStatusUntag:
		return classifyConfigSubject(view, operation)
	default:
		return nil
	}
}

// classifyConfigAdd reports two clones defining the same status differently.
// The fold keeps whichever applied first, which after a reconciliation is
// always upstream's, so the local definition is the one that would vanish
// silently.
func classifyConfigAdd(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	definition, live := view.live[operation.Name]
	if !live {
		return nil
	}
	if definition.Label == operation.Label &&
		definition.Rank == operation.Rank &&
		sameStatusTags(definition.Tags, operation.Tags) {
		return nil
	}
	return &core.ConfigConflict{
		Type:   core.ConfigConflictStatusDefinition,
		Status: operation.Name,
		Ours:   describeStatusDefinition(operation.Label, operation.Rank, operation.Tags),
		Theirs: describeStatusDefinition(definition.Label, definition.Rank, definition.Tags),
	}
}

func classifyConfigRename(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	subject, live := view.resolveSubject(operation.From)
	if !live {
		if destination, retired := view.retirementOf(operation.From); retired {
			return &core.ConfigConflict{
				Type:   core.ConfigConflictStatusRetired,
				Status: operation.From,
				Ours:   string(operation.To),
				Theirs: string(destination),
				Detail: fmt.Sprintf(
					"this status was renamed to %s here and removed into %s on origin, so the rename was dropped",
					operation.To, destination),
			}
		}
		return nil
	}
	if subject == operation.To {
		// Both sides renamed it to the same token, in either order.
		return nil
	}
	if subject != operation.From {
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusRename,
			Status: operation.From,
			Ours:   string(operation.To),
			Theirs: string(subject),
		}
	}
	if _, taken := view.live[operation.To]; taken {
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusRename,
			Status: operation.From,
			Ours:   string(operation.To),
			Theirs: string(operation.To),
			Detail: fmt.Sprintf(
				"renaming this status to %s would collide with the status origin already defines under that name, so the rename was dropped",
				operation.To),
		}
	}
	return nil
}

func classifyConfigRemove(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	subject, live := view.resolveSubject(operation.Status)
	if !live {
		destination, retired := view.retirementOf(operation.Status)
		if !retired {
			return nil
		}
		ours, oursLive := view.resolve(operation.Destination)
		theirs, theirsLive := view.resolve(destination)
		if oursLive && theirsLive && ours == theirs {
			// Both sides removed it into the same place. Recording that twice
			// says nothing.
			return nil
		}
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusRetired,
			Status: operation.Status,
			Ours:   string(operation.Destination),
			Theirs: string(destination),
		}
	}
	if len(view.live) == 1 {
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusArity,
			Status: subject,
			Detail: "removing this status would leave the project with no statuses at all, so the removal was dropped",
		}
	}
	destination, resolved := view.resolve(operation.Destination)
	if !resolved {
		return nil
	}
	if destination == subject {
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusRetired,
			Status: operation.Status,
			Ours:   string(operation.Destination),
			Theirs: string(subject),
			Detail: fmt.Sprintf(
				"origin already removed %s into this status, so removing this one into %s would forward it to itself",
				operation.Destination, operation.Destination),
		}
	}
	return nil
}

// classifyConfigSubject covers the operations that only edit a status in place.
// The one thing that can go wrong with them is that the fetched history removed
// the status underneath: the fold refuses to chase a retirement, precisely so
// the edit does not land on the innocent status the tasks were forwarded into,
// and the dropped edit is what gets reported here.
func classifyConfigSubject(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	if operation.Status == "" {
		return nil
	}
	if _, live := view.resolveSubject(operation.Status); live {
		return nil
	}
	destination, retired := view.retirementOf(operation.Status)
	if !retired {
		return nil
	}
	return &core.ConfigConflict{
		Type:   core.ConfigConflictStatusRetired,
		Status: operation.Status,
		Theirs: string(destination),
		Detail: fmt.Sprintf("origin removed this status into %s, so the local %s was dropped",
			destination, operation.Type),
	}
}

// classifyConfigArity reports the roles ApplyConfig had to repair because
// replaying this pack emptied one.
//
// It reads the repair out of the result rather than predicting it, because the
// repair is the fold's own rule and a second implementation of it here would be
// a second answer. A status carrying a role after the fold that neither carried
// it before nor was given it by this pack can only have been chosen by
// normalization, by position, and nobody asked for that.
//
// A status the pack renamed keeps its roles under the new token, so the roles
// held before the fold are compared through the result's own forwarding chains.
// One case is deliberately silent: a removal whose destination inherits the
// removed status's last role reads as the destination taking over the column,
// which is what a person would expect, and reporting it would make every
// ordinary removal of the done column a conflict.
func classifyConfigArity(before, after core.VocabularyDocument, pack core.ConfigOperationPack) []core.ConfigConflict {
	afterView := newConfigView(after)
	conflicts := make([]core.ConfigConflict, 0, len(statusRoleTags))
	for _, tag := range statusRoleTags {
		allowed := make(map[core.Status]struct{})
		for _, definition := range before.Statuses {
			if !hasStatusTag(definition.Tags, tag) {
				continue
			}
			if resolved, live := afterView.resolve(definition.Status); live {
				allowed[resolved] = struct{}{}
			}
		}
		for _, operation := range pack.Operations {
			switch operation.Type {
			case core.ConfigStatusTag:
				if operation.Tag == tag {
					if resolved, live := afterView.resolve(operation.Status); live {
						allowed[resolved] = struct{}{}
					}
				}
			case core.ConfigStatusAdd:
				if hasStatusTag(operation.Tags, tag) {
					allowed[operation.Name] = struct{}{}
				}
			}
		}
		gained := make([]core.Status, 0, 1)
		for _, definition := range after.Statuses {
			if !hasStatusTag(definition.Tags, tag) {
				continue
			}
			if _, expected := allowed[definition.Status]; !expected {
				gained = append(gained, definition.Status)
			}
		}
		sort.Slice(gained, func(left, right int) bool { return gained[left] < gained[right] })
		for _, status := range gained {
			conflicts = append(conflicts, core.ConfigConflict{
				Type:   core.ConfigConflictStatusArity,
				Status: status,
				Theirs: string(tag),
				Detail: fmt.Sprintf(
					"replaying this change left no status tagged %s, so the fold tagged %s by position",
					tag, status),
			})
		}
	}
	return conflicts
}

// statusRoleTags is the set of roles arity repair can move. It mirrors core's
// own tag set; a tag added there without being added here would simply not be
// reported, never mis-reported.
var statusRoleTags = [...]core.StatusTag{core.StatusTagDefault, core.StatusTagDone, core.StatusTagNext}

func hasStatusTag(tags []core.StatusTag, wanted core.StatusTag) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

func sameStatusTags(left, right []core.StatusTag) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func describeStatusDefinition(label, rank string, tags []core.StatusTag) string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, string(tag))
	}
	described := fmt.Sprintf("%q at rank %s", label, rank)
	if len(names) > 0 {
		described += " tagged " + strings.Join(names, ",")
	}
	return described
}
