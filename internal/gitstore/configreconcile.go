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
// ledgers have different genesis roots, oldest first and without the root, and
// the root it dropped.
//
// Adopting origin's root rather than trying to merge the two is the whole
// resolution, and the reason is a division of labour rather than a tiebreak.
// A genesis root is bookkeeping: it says "this project's configuration started
// from this vocabulary". The operations after it are intent — somebody added,
// renamed or removed a status — and intent must survive. Origin won the
// publication race, so its root is the project's root, and this clone's
// operations are replayed onto it.
//
// That division holds only while the two roots make the same statement, and
// they no longer always do. This comment used to assert that a lazily seeded
// root is always core.LegacyVocabulary; `workbook setup` now writes
// core.DefaultVocabulary when it mints a project, so a root can carry either,
// and adopting across a difference is not bookkeeping at all — it redefines
// the project. A column can vanish with tasks in it, or appear in a project
// that never had one. The caller therefore compares the two roots and refuses
// to adopt across a difference, which is why this returns the dropped root
// rather than discarding it silently.
func unrelatedLocalConfigCommits(graph map[string][]string, localHead string) ([]string, string, error) {
	local, err := commitChain(graph, localHead)
	if err != nil {
		return nil, "", core.Wrap(core.CategoryCorruptData, "cannot walk the local configuration history", err)
	}
	if len(local) == 0 {
		return nil, "", core.Errorf(core.CategoryCorruptData, "the local configuration ledger has no commits")
	}
	// The oldest commit is this clone's own genesis. It is dropped rather than
	// replayed: config.genesis requires no parent, so replaying one onto
	// origin's root would be refused by the fold.
	return reverseCommits(local[:len(local)-1]), local[len(local)-1], nil
}

// configRootOf returns the oldest commit in a ledger, which is its genesis root.
func configRootOf(graph map[string][]string, head string) (string, error) {
	chain, err := commitChain(graph, head)
	if err != nil {
		return "", core.Wrap(core.CategoryCorruptData, "cannot walk a configuration history", err)
	}
	if len(chain) == 0 {
		return "", core.Errorf(core.CategoryCorruptData, "a configuration ledger has no commits")
	}
	return chain[len(chain)-1], nil
}

// describeRootVocabulary renders a genesis vocabulary for a conflict line: how
// many statuses it started from and which, in order.
//
// Naming them is what makes the report actionable. "Two different vocabularies"
// sends somebody to read two commits by hand; "backlog, ready, blocked, …
// against backlog, ready, …" is the difference itself.
func describeRootVocabulary(document core.VocabularyDocument) string {
	names := make([]string, 0, len(document.Statuses))
	for _, definition := range document.Statuses {
		names = append(names, string(definition.Status))
	}
	return fmt.Sprintf("%d status(es): %s", len(names), strings.Join(names, ", "))
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
	// Same refusal the task replay makes, for the same reason and with the same
	// consequence: the local ledger ref is left exactly where it is, holding
	// every local operation, and origin's tip waits in the tracking namespace
	// until a build that can fold it arrives. A fast-forward is unaffected —
	// adopting origin's ledger wholesale needs no fold — so a clone that has
	// authored nothing locally keeps synchronizing normally.
	if remote.State.RequiresNewerReader() || local.State.RequiresNewerReader() {
		return configReconcileOutcome{}, core.Errorf(core.CategoryNewerWriter,
			"this project's local configuration changes were not replayed: origin's configuration was "+
				"written by a newer workbook; upgrade workbook to publish them. They are unchanged on %s.",
			configRef)
	}
	outcome := configReconcileOutcome{
		Head:        remote.Head,
		Parked:      local.Head,
		State:       remote.State,
		AdoptedRoot: unrelated,
	}

	var localOnly []string
	var localRoot string
	var err error
	if unrelated {
		localOnly, localRoot, err = unrelatedLocalConfigCommits(graph, local.Head)
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
	if unrelated {
		conflict, err := r.classifyConfigRoots(ctx, config, graph, localRoot, remote.Head, outcome.ParkedRef)
		if err != nil {
			return configReconcileOutcome{}, err
		}
		if conflict != nil {
			// The ledger still becomes origin's — a clone cannot serve two
			// roots — but nothing local is replayed onto it and the whole local
			// tip is parked, so every operation the local root carried is still
			// readable at the parked ref while somebody decides.
			outcome.Conflicts = []core.ConfigConflict{*conflict}
			return outcome, nil
		}
	}
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

// classifyConfigRoots reports the one thing that must stop an adoption: the two
// unrelated ledgers were started from different vocabularies.
//
// Equal roots keep the behavior this path has always had — adopt origin's root,
// replay the local operations onto it, lose nothing anybody chose. The
// comparison is what makes that safe, where it used to rest on an assumption
// about how roots were written; `workbook setup` mints one vocabulary and the
// lazy seed records another, so the assumption no longer holds and only the
// comparison can tell the two situations apart.
//
// It reads two commits, and only on the unrelated path, which a project reaches
// at most once in its life.
func (r *Repository) classifyConfigRoots(
	ctx context.Context,
	config core.ProjectConfig,
	graph map[string][]string,
	localRoot string,
	remoteHead string,
	parkedRef string,
) (*core.ConfigConflict, error) {
	remoteRoot, err := configRootOf(graph, remoteHead)
	if err != nil {
		return nil, err
	}
	localRecords, err := r.readConfigRecords(ctx, config, configRef, []string{localRoot})
	if err != nil {
		return nil, err
	}
	remoteRecords, err := r.readConfigRecords(ctx, config, remoteConfigRef, []string{remoteRoot})
	if err != nil {
		return nil, err
	}
	ours := localRecords[0].State.Config.Vocabulary
	theirs := remoteRecords[0].State.Config.Vocabulary
	if reflect.DeepEqual(ours, theirs) {
		return nil, nil
	}
	return &core.ConfigConflict{
		Type:   core.ConfigConflictRootVocabulary,
		Ours:   describeRootVocabulary(ours),
		Theirs: describeRootVocabulary(theirs),
		Detail: fmt.Sprintf(
			"this project's configuration was started twice from different vocabularies — %s here, %s on origin. "+
				"Origin's is this project's now and nothing local was replayed onto it; the local ledger is kept at %s. "+
				"Reapply what it recorded with `workbook status`, and add back any status origin's root does not "+
				"define before tasks stored under it stop resolving",
			describeRootVocabulary(ours), describeRootVocabulary(theirs), parkedRef),
	}, nil
}

// markPackSubjects records the statuses a pack brings into existence, so an
// operation later in the same pack can name one of them.
//
// A pack is atomic and its operations are ordered, so the honest reading of
// operation N+1 is against a vocabulary that already has operation N's effect.
// This projects the one effect classification asks about — which tokens exist —
// rather than advancing the whole view, and that is deliberate. Advancing would
// mean folding each operation into a vocabulary document here, which is a second
// implementation of ApplyConfig's rules living beside the fold rather than in
// it; classifyConfigAdd compares labels, ranks and tag sets exactly, so a
// projection that normalized any of them differently would invent conflicts
// instead of finding them. Existence is the whole question the pending set
// answers, and it is answerable without reproducing the fold.
//
// Two operation types create a token: an add names one outright, and a rename
// moves an existing status onto a new one. `workbook status rename` records
// exactly the second followed by a relabel of the new token whenever the display
// label follows the machine value, which is the default for every derived label
// — so an ordinary rename is a pack whose second operation names a status only
// its first operation created.
func markPackSubjects(view configView, operations []core.ConfigOperation) {
	for _, operation := range operations {
		switch operation.Type {
		case core.ConfigStatusAdd:
			view.pending[operation.Name] = struct{}{}
		case core.ConfigStatusRename:
			view.pending[operation.To] = struct{}{}
		}
	}
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
	view := newConfigView(replay.parent.State.Config)
	markPackSubjects(view, local.Operation.Operations)
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
	// display is the parent's resolved display settings. It is carried as the
	// resolved value rather than the stored pointer because every question asked
	// of it is "what does origin say this setting is", and an unconfigured
	// setting and an absent section are the same answer.
	display core.DisplaySettings
	// pending are the statuses the pack being classified brings into existence
	// itself. The view is built once per pack, from the vocabulary the pack
	// starts from, so without this an operation editing a status an earlier
	// operation in the same pack created would read as an edit to a status
	// nobody defines.
	pending map[core.Status]struct{}
}

// newConfigView takes the whole configuration rather than the vocabulary alone,
// because classification is now a question about two sections. Passing the
// vocabulary and letting the display arrive some other way would let the two
// come from different reads of the ledger, which is precisely the skew the
// replay cannot afford: the parent is one commit, and the view is what that one
// commit said.
func newConfigView(config core.ConfigData) configView {
	document := config.Vocabulary
	view := configView{
		live:    make(map[core.Status]core.StatusDefinition, len(document.Statuses)),
		aliases: make(map[core.Status]core.Status, len(document.Aliases)),
		retired: make(map[core.Status]core.Status, len(document.Retired)),
		display: core.ResolveDisplaySettings(config.Display),
		pending: make(map[core.Status]struct{}),
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
	case core.ConfigDisplaySet, core.ConfigDisplayUnset:
		return classifyConfigDisplay(view, operation)
	default:
		return nil
	}
}

// classifyConfigDisplay reports two clones deciding the same display setting
// differently.
//
// The section's three values are independent, so the question is asked of one
// setting at a time: what does the fetched history say this setting is, and what
// would this operation make it? A pair that agrees — both sides chose the same
// name, both cleared the same color, or origin never touched the setting at all
// — converges, and recording it again would say nothing. A pair that disagrees
// is a decision, because the fold keeps whichever applied first and after a
// reconciliation that is always upstream's: the local value is the one that
// would vanish without anybody being told.
//
// A set against an unconfigured setting is not a disagreement even though the
// two sides differ. Origin expressed no intent about it, so there is nothing to
// weigh the local one against, and the fold records it — which is the same rule
// classifyConfigAdd applies to a status origin does not define.
func classifyConfigDisplay(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	theirs, known := view.display.Value(operation.Setting)
	if !known {
		// A setting name this build does not know reached the classifier, which
		// the operation document check already refuses. Saying nothing about it
		// is the only honest answer: this cannot compare two values when it does
		// not know where either is stored.
		return nil
	}
	ours := ""
	if operation.Type == core.ConfigDisplaySet {
		ours = operation.Value
	}
	if theirs == "" || ours == theirs {
		return nil
	}
	return &core.ConfigConflict{
		Type:   core.ConfigConflictDisplaySetting,
		Ours:   ours,
		Theirs: theirs,
		Detail: describeDisplayConflict(operation.Setting, ours, theirs),
	}
}

// describeDisplayConflict says which setting disagrees and what the two sides
// made of it, in one line, because the conflict union has no member to put a
// setting name in — it names a status, and a display setting is not one.
func describeDisplayConflict(setting, ours, theirs string) string {
	if ours == "" {
		return fmt.Sprintf(
			"%s was cleared here and set to %s on origin, so origin's value stands; clear it again to keep the local intent",
			setting, theirs)
	}
	return fmt.Sprintf(
		"%s was set to %s here and to %s on origin, so origin's value stands; set it again to keep the local intent",
		setting, ours, theirs)
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
		// Origin never had it. Reachable only after a root adoption, and a
		// discarded rename is as much a lost decision as a discarded relabel;
		// see classifyConfigSubject.
		return undefinedSubjectConflict(view, operation.From, operation.Type)
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
			// Origin never had it; see classifyConfigSubject.
			return undefinedSubjectConflict(view, operation.Status, operation.Type)
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
//
// Two things can go wrong with them. The fetched history may have removed the
// status underneath: the fold refuses to chase a retirement, precisely so the
// edit does not land on the innocent status the tasks were forwarded into, and
// the dropped edit is reported.
//
// Or the fetched history may never have had the status at all. That used to be
// unreachable — the two ledgers shared a base, or shared a root that said the
// same thing — and returning nil for it read as "a no-op nobody needs to decide
// about". It is reachable now that a root can be minted from one vocabulary and
// seeded from another, and nil is the wrong answer: the fold applies the edit to
// nothing, the result equals the parent, and the replay counts the operation as
// already applied upstream. Somebody's deliberate change is discarded and the
// report says it landed. An edit to a status the adopted history does not define
// is a decision, so it is one here.
func classifyConfigSubject(view configView, operation core.ConfigOperation) *core.ConfigConflict {
	if operation.Status == "" {
		return nil
	}
	if _, live := view.resolveSubject(operation.Status); live {
		return nil
	}
	if destination, retired := view.retirementOf(operation.Status); retired {
		return &core.ConfigConflict{
			Type:   core.ConfigConflictStatusRetired,
			Status: operation.Status,
			Theirs: string(destination),
			Detail: fmt.Sprintf("origin removed this status into %s, so the local %s was dropped",
				destination, operation.Type),
		}
	}
	return undefinedSubjectConflict(view, operation.Status, operation.Type)
}

// undefinedSubjectConflict reports a local operation whose subject the fetched
// history neither defines nor has ever retired.
//
// A subject the local ledger itself defines in the same pack is not undefined:
// the pack is classified against the vocabulary it starts from, so an add and an
// edit recorded together would otherwise report the author's own status as
// missing.
func undefinedSubjectConflict(
	view configView,
	subject core.Status,
	operationType core.ConfigOperationType,
) *core.ConfigConflict {
	if _, pending := view.pending[subject]; pending {
		return nil
	}
	return &core.ConfigConflict{
		Type:   core.ConfigConflictStatusDefinition,
		Status: subject,
		Ours:   fmt.Sprintf("a local %s of this status", operationType),
		Theirs: "not defined",
		Detail: fmt.Sprintf(
			"origin's configuration does not define this status and never retired it, so the local %s changed "+
				"nothing; define it again with `workbook status add` before reapplying the change",
			operationType),
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
	afterView := newConfigView(core.ConfigData{Vocabulary: after})
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
