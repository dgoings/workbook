package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// SetAsideReport is what this clone's parked tips hold that the task histories
// do not, by task, and the parked tips that could not be read at all.
type SetAsideReport struct {
	// Edits holds, by task, the edits a reconciliation set aside that the
	// task does not carry, in the order they were made.
	Edits map[string][]core.SetAsideEdit
	// Unreadable names each parked tip that was skipped because it cannot be
	// read as a set-aside edit of its task. Nothing it holds is reported, and
	// it hides nothing else.
	Unreadable []UnreadableParkedRef
}

// UnreadableParkedRef is a parked tip SetAsideEdits skipped, and why.
type UnreadableParkedRef struct {
	TaskID string
	Ref    string
	Commit string
	Reason string
}

// parkedVerdict is what one parked tip holds against one task tip.
type parkedVerdict struct {
	objectID string
	tip      string
	// lasting reports a verdict that holds for every later tip of the task as
	// well, so it is not worked out again when the task moves: every edit the
	// tip holds is settled for good, or the tip cannot be read whatever the
	// task does.
	lasting bool
	edits   []core.SetAsideEdit
	problem string
}

// settled reports a parked tip that holds nothing the task lacks: every edit
// in it was replayed or is carried by the task. Only such a tip is ever
// retired without a person saying so.
func (verdict parkedVerdict) settled() bool {
	return verdict.problem == "" && len(verdict.edits) == 0
}

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
// process that parked them and needs no record of its own. When an edit counts
// as settled, and so stops being reported, is setAsideVerdict's to say.
//
// A parked tip that cannot be read as one of its task's set-aside edits — its
// commit is missing, is not a Workbook commit of that task, or shares no
// history with the task — is skipped and named in Unreadable. It hides nothing
// else: every other task and parked tip is reported as if it were not there.
//
// Each parked tip's answer is remembered against the parked commit and the
// task tip, the two immutable things it is a function of, and one whose every
// edit is settled for good is remembered against the parked commit alone. So
// a board polling every second pays for a parked tip when it or its task
// moves, and an answer it has to work out is worked out for every parked tip
// at once, with a fixed number of Git processes however many there are.
func (r *Repository) SetAsideEdits(ctx context.Context, config core.ProjectConfig, heads map[string]string) (SetAsideReport, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return SetAsideReport{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return SetAsideReport{}, err
	}
	parked, err := r.parkedListing(ctx, config, heads)
	if err != nil {
		return SetAsideReport{}, err
	}
	if len(parked) == 0 {
		return SetAsideReport{}, nil
	}

	verdicts, err := r.parkedVerdicts(ctx, config, parked, heads)
	if err != nil {
		return SetAsideReport{}, err
	}
	report := SetAsideReport{Edits: make(map[string][]core.SetAsideEdit)}
	for _, ref := range parked {
		verdict, found := verdicts[ref.name]
		if !found {
			continue
		}
		if verdict.problem != "" {
			report.Unreadable = append(report.Unreadable, UnreadableParkedRef{
				TaskID: ref.taskID, Ref: ref.name, Commit: ref.objectID, Reason: verdict.problem,
			})
			continue
		}
		if len(verdict.edits) > 0 {
			report.Edits[ref.taskID] = append(report.Edits[ref.taskID], verdict.edits...)
		}
	}
	return report, nil
}

// parkedListingReuse is how long SetAsideEdits answers from the parked refs it
// last listed while nothing says they changed.
const parkedListingReuse = 10 * time.Second

// parkedListingMemo is the parked refs SetAsideEdits last listed, sorted, and
// what they were listed against.
type parkedListingMemo struct {
	refs       []parkedRef
	heads      uint64
	generation uint64
	at         time.Time
}

// parkedListing returns every parked ref, sorted by task and index.
//
// Listing them is the one cost a repeated read cannot otherwise avoid, and
// with many loose refs it is most of what a poll costs, so the listing is
// reused while all of these hold: the task tips the caller shows are the ones
// it was listed against, this process has created or removed no parked ref
// since, and it is under parkedListingReuse old. A parked ref is created only
// by a fetch, which moves the task off the tip it parks, so a new one always
// arrives with a changed tip; this process's own dismissals and pruning count
// as changes; and anything else, such as a ref removed by hand or by another
// process, is seen within parkedListingReuse.
func (r *Repository) parkedListing(ctx context.Context, config core.ProjectConfig, heads map[string]string) ([]parkedRef, error) {
	fingerprint := headsFingerprint(heads)
	r.setAsideMu.Lock()
	memo, generation := r.parkedListed, r.parkedGeneration
	r.setAsideMu.Unlock()
	if memo != nil && memo.heads == fingerprint && memo.generation == generation && time.Since(memo.at) < parkedListingReuse {
		return memo.refs, nil
	}
	listed := time.Now()
	parked, err := r.listParkedRefs(ctx, config, reconciledRefPrefix)
	if err != nil {
		return nil, err
	}
	sortParkedRefs(parked)
	r.forgetVanishedVerdicts(parked)
	r.setAsideMu.Lock()
	if r.parkedGeneration == generation {
		r.parkedListed = &parkedListingMemo{refs: parked, heads: fingerprint, generation: generation, at: listed}
	}
	r.setAsideMu.Unlock()
	return parked, nil
}

// parkedRefsChanged records that this process created or removed parked refs,
// so the next SetAsideEdits lists them again.
func (r *Repository) parkedRefsChanged() {
	r.setAsideMu.Lock()
	r.parkedGeneration++
	r.setAsideMu.Unlock()
}

// headsFingerprint summarizes a set of task tips, so two calls can tell
// cheaply whether they were handed the same ones.
func headsFingerprint(heads map[string]string) uint64 {
	ids := make([]string, 0, len(heads))
	for id := range heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hash := fnv.New64a()
	for _, id := range ids {
		hash.Write([]byte(id))
		hash.Write([]byte{0})
		hash.Write([]byte(heads[id]))
		hash.Write([]byte{0})
	}
	return hash.Sum64()
}

func sortParkedRefs(parked []parkedRef) {
	sort.Slice(parked, func(i, j int) bool {
		if parked[i].taskID != parked[j].taskID {
			return parked[i].taskID < parked[j].taskID
		}
		return parked[i].index < parked[j].index
	})
}

// forgetVanishedVerdicts drops every remembered answer whose parked ref is no
// longer listed, or now names another commit, so the memo holds one entry per
// parked ref that exists and never more.
func (r *Repository) forgetVanishedVerdicts(listed []parkedRef) {
	present := make(map[string]string, len(listed))
	for _, ref := range listed {
		present[ref.name] = ref.objectID
	}
	r.setAsideMu.Lock()
	defer r.setAsideMu.Unlock()
	for name, verdict := range r.setAsideMemo {
		if objectID, found := present[name]; !found || objectID != verdict.objectID {
			delete(r.setAsideMemo, name)
		}
	}
}

// forgetVerdicts drops the remembered answers of parked refs this process has
// just deleted.
func (r *Repository) forgetVerdicts(names []string) {
	if len(names) == 0 {
		return
	}
	r.setAsideMu.Lock()
	defer r.setAsideMu.Unlock()
	for _, name := range names {
		delete(r.setAsideMemo, name)
	}
	r.parkedGeneration++
}

// verdictRequest asks what one parked tip holds against its task's tip.
type verdictRequest struct {
	ref parkedRef
	tip string
}

// parkedVerdicts answers, by parked ref name, what each parked tip whose task
// tips names holds against that tip: from the memo where the inputs are the
// ones it was worked out for, and otherwise all at once.
func (r *Repository) parkedVerdicts(ctx context.Context, config core.ProjectConfig, refs []parkedRef, tips map[string]string) (map[string]parkedVerdict, error) {
	verdicts := make(map[string]parkedVerdict, len(refs))
	var misses []verdictRequest
	r.setAsideMu.Lock()
	for _, ref := range refs {
		tip := tips[ref.taskID]
		if tip == "" {
			continue
		}
		remembered, found := r.setAsideMemo[ref.name]
		if found && remembered.objectID == ref.objectID && (remembered.lasting || remembered.tip == tip) {
			verdicts[ref.name] = remembered
			continue
		}
		misses = append(misses, verdictRequest{ref: ref, tip: tip})
	}
	r.setAsideMu.Unlock()
	if len(misses) == 0 {
		return verdicts, nil
	}

	computed, err := r.computeVerdicts(ctx, config, misses)
	if err != nil {
		return nil, err
	}
	r.setAsideMu.Lock()
	if r.setAsideMemo == nil {
		r.setAsideMemo = make(map[string]parkedVerdict)
	}
	for _, miss := range misses {
		verdict := computed[miss.ref.name]
		verdicts[miss.ref.name] = verdict
		r.setAsideMemo[miss.ref.name] = verdict
	}
	r.setAsideMu.Unlock()
	return verdicts, nil
}

// verdictPlan is one request's two sides once the commit graph is known.
type verdictPlan struct {
	request verdictRequest
	// base is the newest commit both sides share, and empty when they share
	// none because the task's history was compacted since the tip was parked.
	base string
	// parkedOnly and tipOnly are the commits only one side has, oldest first.
	// tipOnly ends with the task's tip.
	parkedOnly []string
	tipOnly    []string
}

// computeVerdicts works out what each parked tip holds, with a fixed number of
// Git processes: one to learn which commits exist, one for the commit graph
// of every parked tip and task tip, and one batch read of every commit either
// side holds alone. A parked tip whose history cannot be walked makes the
// shared walk fail, and only then is each walked alone, so one bad tip costs
// its own processes and never another's answer.
func (r *Repository) computeVerdicts(ctx context.Context, config core.ProjectConfig, requests []verdictRequest) (map[string]parkedVerdict, error) {
	verdicts := make(map[string]parkedVerdict, len(requests))
	problem := func(request verdictRequest, lasting bool, format string, args ...any) {
		verdicts[request.ref.name] = parkedVerdict{
			objectID: request.ref.objectID, tip: request.tip, lasting: lasting, problem: fmt.Sprintf(format, args...),
		}
	}

	objects := make([]string, 0, 2*len(requests))
	for _, request := range requests {
		objects = append(objects, request.ref.objectID, request.tip)
	}
	kinds, err := r.objectKinds(ctx, objects)
	if err != nil {
		return nil, err
	}
	walkable := make([]verdictRequest, 0, len(requests))
	for _, request := range requests {
		switch kind := kinds[request.ref.objectID]; {
		case kind == "":
			problem(request, true, "its commit %s is not in this clone", request.ref.objectID)
		case kind != "commit":
			problem(request, true, "it points at a %s, not a commit", kind)
		case kinds[request.tip] != "commit":
			problem(request, false, "the task's tip %s cannot be read", request.tip)
		default:
			walkable = append(walkable, request)
		}
	}

	starts := make([]string, 0, 2*len(walkable))
	for _, request := range walkable {
		starts = append(starts, request.ref.objectID, request.tip)
	}
	graph, err := r.parentGraph(ctx, starts)
	if err != nil {
		graph = make(map[string][]string)
		alone := walkable[:0:0]
		for _, request := range walkable {
			own, err := r.parentGraph(ctx, []string{request.ref.objectID, request.tip})
			if err != nil {
				problem(request, false, "its history cannot be walked: %v", err)
				continue
			}
			for commit, parents := range own {
				graph[commit] = parents
			}
			alone = append(alone, request)
		}
		walkable = alone
	}

	plans := make([]verdictPlan, 0, len(walkable))
	for _, request := range walkable {
		parkedChain, err := commitChain(graph, request.ref.objectID)
		if err != nil {
			problem(request, true, "its history is not a Workbook history: %v", err)
			continue
		}
		tipChain, err := commitChain(graph, request.tip)
		if err != nil {
			problem(request, false, "the task's history cannot be walked: %v", err)
			continue
		}
		onTip := make(map[string]int, len(tipChain))
		for position, commit := range tipChain {
			onTip[commit] = position
		}
		plan := verdictPlan{request: request}
		shared := len(parkedChain)
		for position, commit := range parkedChain {
			if _, found := onTip[commit]; found {
				shared = position
				plan.base = commit
				break
			}
		}
		if shared == 0 {
			// The parked tip is in the task's own history, so it holds
			// nothing the task lacks. That is not for good: a caller showing
			// a projection that has not caught up with the fetch that parked
			// it passes the very tip the fetch moved the task off, and the
			// answer for the tip it moved to is a different one.
			verdicts[request.ref.name] = parkedVerdict{objectID: request.ref.objectID, tip: request.tip}
			continue
		}
		tipEnd := len(tipChain)
		if plan.base != "" {
			tipEnd = onTip[plan.base]
		}
		plan.parkedOnly = oldestFirst(parkedChain[:shared])
		plan.tipOnly = oldestFirst(tipChain[:tipEnd])
		plans = append(plans, plan)
	}

	// One batch read of every commit any plan needs. The same commit may be
	// needed by several parked tips of one task, and is read once.
	type readKey struct{ taskID, objectID string }
	positions := make(map[readKey]int)
	var heads []TaskHead
	need := func(taskID, objectID string) {
		key := readKey{taskID, objectID}
		if _, found := positions[key]; found {
			return
		}
		positions[key] = len(heads)
		heads = append(heads, TaskHead{TaskID: taskID, ObjectID: objectID})
	}
	for _, plan := range plans {
		taskID := plan.request.ref.taskID
		if plan.base != "" {
			need(taskID, plan.base)
		}
		for _, commit := range plan.parkedOnly {
			need(taskID, commit)
		}
		for _, commit := range plan.tipOnly {
			need(taskID, commit)
		}
	}
	read, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}
	snapshots := func(taskID string, commits []string) ([]core.Snapshot, error) {
		result := make([]core.Snapshot, 0, len(commits))
		for _, commit := range commits {
			entry := read[positions[readKey{taskID, commit}]]
			if entry.Err != nil {
				return nil, fmt.Errorf("commit %s: %w", commit, entry.Err)
			}
			result = append(result, entry.Snapshot)
		}
		return result, nil
	}

	for _, plan := range plans {
		taskID := plan.request.ref.taskID
		parked, err := snapshots(taskID, plan.parkedOnly)
		if err != nil {
			problem(plan.request, true, "it does not hold a readable edit of task %s: %v", taskID, err)
			continue
		}
		tip, err := snapshots(taskID, plan.tipOnly)
		if err != nil {
			problem(plan.request, false, "the task's history cannot be read: %v", err)
			continue
		}
		var base *core.Snapshot
		if plan.base != "" {
			shared, err := snapshots(taskID, []string{plan.base})
			if err != nil {
				problem(plan.request, false, "the history it shares with the task cannot be read: %v", err)
				continue
			}
			base = &shared[0]
		} else if !compactedFrom(tip[0], parked[0]) {
			problem(plan.request, false, "it shares no history with task %s", taskID)
			continue
		}
		verdicts[plan.request.ref.name] = setAsideVerdict(plan.request, base, parked, tip)
	}
	return verdicts, nil
}

// compactedFrom reports whether root, the first commit of the task's history,
// is a checkpoint that replaced the history parkedRoot begins: the task was
// compacted after the tip was parked. Anything else that shares no commit with
// the task is not one of its histories at all.
func compactedFrom(root, parkedRoot core.Snapshot) bool {
	checkpoint, found := root.Operation.Checkpoint()
	if !found {
		return false
	}
	compacted := operationIDSet(checkpoint.CompactedOperations)
	for _, operation := range parkedRoot.Operation.Operations {
		if _, found := compacted[operation.ID]; !found {
			return false
		}
	}
	return true
}

// setAsideVerdict decides which of a parked tip's edits the task lacks.
//
// An edit is settled, and stops being reported, when one of these holds:
//
//   - Every operation in it is in the task's history: it was replayed.
//     Reconciliation keeps operation IDs when it replays, and a compaction's
//     checkpoint lists every ID it replaced.
//   - Applied as a replay would apply it, it changes nothing in a state the
//     task has had since the edit was set aside: the task carries it. The
//     task's current state always counts. So do the states from the one
//     origin held when the edit was parked onward, when the parked tip names
//     that state: reconciliation replays the edits before a conflict onto
//     origin's tip, so the first replayed commit's parent is it. An edit
//     carried by one of those is settled for good, however the field changes
//     afterwards. Origin's own states from before then never count, so an
//     edit whose value origin held once and changed again before the
//     conflict stays reported.
//   - It adds a comment, and the task now holds a comment by the same person
//     with the same text that was not there before the two sides parted: the
//     text was copied back in, which gives it a new ID. Removing that copy
//     brings the set-aside comment back.
//
// Every other edit is reported. One that cannot be applied at all — the task
// was deleted, or its history compacted, since — is reported, since nothing
// says its effect is there.
func setAsideVerdict(request verdictRequest, base *core.Snapshot, parked, tip []core.Snapshot) parkedVerdict {
	verdict := parkedVerdict{objectID: request.ref.objectID, tip: request.tip, lasting: true}
	recorded := make(map[string]struct{})
	for _, snapshot := range tip {
		for _, operation := range snapshot.Operation.Operations {
			recorded[operation.ID] = struct{}{}
		}
		if checkpoint, found := snapshot.Operation.Checkpoint(); found {
			for _, id := range checkpoint.CompactedOperations {
				recorded[id] = struct{}{}
			}
		}
	}
	parkedOperations := make(map[string]struct{})
	for _, snapshot := range parked {
		for _, operation := range snapshot.Operation.Operations {
			parkedOperations[operation.ID] = struct{}{}
		}
	}

	// The task's tip is the last commit only it holds, or, when it holds
	// none, the commit the parked tip was built on.
	var current core.StateDocument
	if len(tip) > 0 {
		current = tip[len(tip)-1].State
	} else {
		current = base.State
	}
	since := []core.StateDocument{current}
	sinceLasts := false
	if base != nil {
		for position, snapshot := range tip {
			if !holdsOnly(snapshot.Operation, parkedOperations) {
				continue
			}
			from := base.State
			if position > 0 {
				from = tip[position-1].State
			}
			since = []core.StateDocument{from}
			for _, later := range tip[position:] {
				since = append(since, later.State)
			}
			sinceLasts = true
			break
		}
	}
	var before map[string]struct{}
	if base != nil {
		before = make(map[string]struct{}, len(base.State.Task.Comments))
		for _, comment := range base.State.Task.Comments {
			before[comment.ID] = struct{}{}
		}
	}

	for _, snapshot := range parked {
		if holdsOnly(snapshot.Operation, recorded) {
			continue
		}
		pack, copied := withoutCopiedComments(snapshot.Operation, current.Task, before)
		if len(pack.Operations) == 0 {
			verdict.lasting = false
			continue
		}
		if carriedBy(since, pack) {
			if copied || !sinceLasts {
				verdict.lasting = false
			}
			continue
		}
		verdict.lasting = false
		verdict.edits = append(verdict.edits, core.SetAsideEdit{
			ParkedRef:  request.ref.name,
			Parked:     request.ref.objectID,
			Commit:     snapshot.Head,
			Actor:      snapshot.Operation.Actor.ID,
			WallTime:   snapshot.Operation.WallTime,
			Operations: append([]core.Operation(nil), snapshot.Operation.Operations...),
		})
	}
	return verdict
}

// holdsOnly reports a pack with operations, every one of them in ids.
func holdsOnly(pack core.OperationPack, ids map[string]struct{}) bool {
	if len(pack.Operations) == 0 {
		return false
	}
	for _, operation := range pack.Operations {
		if _, found := ids[operation.ID]; !found {
			return false
		}
	}
	return true
}

// carriedBy reports whether applying pack, as a replay would apply it, to any
// of states changes nothing the task shows.
func carriedBy(states []core.StateDocument, pack core.OperationPack) bool {
	for _, state := range states {
		attempt := pack
		attempt.LogicalClock = state.LogicalClock + 1
		attempt.HistoryGeneration = state.History.Generation
		next, err := core.Apply(&state, attempt)
		if err == nil && sameTaskState(state.Task, next.Task) {
			return true
		}
	}
	return false
}

// withoutCopiedComments returns pack without the comment additions whose text
// task holds again under a new ID, by the same person, in a comment that was
// not there before the two sides parted (before), and whether it removed any.
// When before is nil, which comments were there is unknown (the task was
// compacted since), and none counts as a copy: an older comment with the same
// text by the same person would otherwise settle one nobody dealt with.
func withoutCopiedComments(pack core.OperationPack, task core.TaskData, before map[string]struct{}) (core.OperationPack, bool) {
	kept := make([]core.Operation, 0, len(pack.Operations))
	for _, operation := range pack.Operations {
		if operation.Type == core.OperationCommentAdd && commentCopied(operation, pack.Actor.ID, task, before) {
			continue
		}
		kept = append(kept, operation)
	}
	copied := len(kept) != len(pack.Operations)
	pack.Operations = kept
	return pack, copied
}

func commentCopied(operation core.Operation, author string, task core.TaskData, before map[string]struct{}) bool {
	if before == nil {
		return false
	}
	for _, comment := range task.Comments {
		if comment.ID == operation.ID || comment.Author != author || comment.Body != operation.Body {
			continue
		}
		if _, existed := before[comment.ID]; existed {
			continue
		}
		return true
	}
	return false
}

func oldestFirst(newestFirst []string) []string {
	result := make([]string, len(newestFirst))
	for index, commit := range newestFirst {
		result[len(newestFirst)-1-index] = commit
	}
	return result
}

// objectKinds returns each object's type, and nothing for one this clone does
// not hold, with one Git process.
func (r *Repository) objectKinds(ctx context.Context, objects []string) (map[string]string, error) {
	kinds := make(map[string]string, len(objects))
	if len(objects) == 0 {
		return kinds, nil
	}
	var input bytes.Buffer
	ordered := make([]string, 0, len(objects))
	for _, objectID := range objects {
		if _, seen := kinds[objectID]; seen {
			continue
		}
		kinds[objectID] = ""
		ordered = append(ordered, objectID)
		fmt.Fprintln(&input, objectID)
	}
	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot check the parked commits", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) != len(ordered) {
		return nil, core.Errorf(core.CategoryCorruptData, "Git answered %d object checks, want %d", len(lines), len(ordered))
	}
	for index, line := range lines {
		name, kind, _ := strings.Cut(line, " ")
		if kind == "missing" || name != ordered[index] {
			continue
		}
		kinds[ordered[index]] = kind
	}
	return kinds, nil
}

// parentGraph returns the parents of every commit reachable from starts.
func (r *Repository) parentGraph(ctx context.Context, starts []string) (map[string][]string, error) {
	if len(starts) == 0 {
		return map[string][]string{}, nil
	}
	var input bytes.Buffer
	for _, start := range starts {
		fmt.Fprintln(&input, start)
	}
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, err
	}
	return parseParentGraph(output)
}

// settledParkedRefs returns the parked refs past the retention bound: for each
// task, the settled ones older than the newest maxParkedRefsPerTask settled
// ones, oldest first. A parked ref that holds an edit the task lacks, or that
// cannot be read, is never among them, however many a task has, so nothing is
// retired that somebody has not seen dealt with. tips names each task's tip.
func (r *Repository) settledParkedRefs(ctx context.Context, config core.ProjectConfig, refs []parkedRef, tips map[string]string) ([]parkedRef, error) {
	byTask := make(map[string][]parkedRef)
	for _, ref := range refs {
		byTask[ref.taskID] = append(byTask[ref.taskID], ref)
	}
	var candidates []parkedRef
	for _, group := range byTask {
		// A task with no more parked refs than the bound has no more settled
		// ones either, and costs nothing to keep.
		if len(group) > maxParkedRefsPerTask {
			candidates = append(candidates, group...)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	verdicts, err := r.parkedVerdicts(ctx, config, candidates, tips)
	if err != nil {
		return nil, err
	}
	taskIDs := make([]string, 0, len(byTask))
	for taskID := range byTask {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	var retired []parkedRef
	for _, taskID := range taskIDs {
		var settled []parkedRef
		for _, ref := range byTask[taskID] {
			if verdict, found := verdicts[ref.name]; found && verdict.settled() {
				settled = append(settled, ref)
			}
		}
		if len(settled) <= maxParkedRefsPerTask {
			continue
		}
		sort.Slice(settled, func(i, j int) bool { return settled[i].index < settled[j].index })
		retired = append(retired, settled[:len(settled)-maxParkedRefsPerTask]...)
	}
	return retired, nil
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
// It is the one way a parked ref holding an edit the task lacks goes: nothing
// retires one automatically (see settledParkedRefs). The ref is removed only
// while it still points at parked, the commit the person was shown, so a
// dismissal never removes an edit nobody saw. A ref that is not there, because
// somebody dismissed it first, is not-found; one that names another commit is
// a stale write. The commits are left in the object store for Git's own
// garbage collection.
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
	gone := core.Errorf(core.CategoryNotFound, "task %s has no set-aside edits at %s; they may have been dismissed already", taskID, parkedRefName)
	moved := core.Errorf(core.CategoryStaleWrite,
		"the set-aside edits of task %s changed since they were shown; look at them again before dismissing them", taskID)
	current, found, err := r.parkedRefTarget(ctx, config, taskID, parkedRefName)
	if err != nil {
		return err
	}
	if !found {
		return gone
	}
	if current != parked {
		return moved
	}
	var input bytes.Buffer
	fmt.Fprintf(&input, "start\noption no-deref\ndelete %s %s\nprepare\ncommit\n", parkedRefName, parked)
	if _, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "-m", "workbook: dismiss set-aside edits", "--stdin"); err != nil {
		// Somebody else changed the ref between the look and the delete.
		// Which of the two answers they get depends on what they did.
		current, found, lookErr := r.parkedRefTarget(ctx, config, taskID, parkedRefName)
		switch {
		case lookErr != nil:
			return core.Wrap(core.CategoryStaleWrite, fmt.Sprintf("the set-aside edits of task %s changed since they were shown", taskID), err)
		case !found:
			return gone
		case current != parked:
			return moved
		}
		return err
	}
	r.forgetVerdicts([]string{parkedRefName})
	return nil
}

// parkedRefTarget returns the commit one of a task's parked refs names, and
// whether it is there.
func (r *Repository) parkedRefTarget(ctx context.Context, config core.ProjectConfig, taskID, name string) (string, bool, error) {
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix+taskID+"/")
	if err != nil {
		return "", false, err
	}
	for _, ref := range refs {
		if ref.name == name {
			return ref.objectID, true, nil
		}
	}
	return "", false, nil
}
