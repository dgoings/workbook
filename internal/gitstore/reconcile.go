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

// reconciledRefPrefix holds the orphaned local tips that reconciliation
// replaced. The namespace sits outside taskRefPrefix, so every task-ref
// enumeration — including the one that builds a push refspec — ignores it and
// parked history stays local without a second exclusion rule.
const reconciledRefPrefix = "refs/workbook/reconciled/"

// maxParkedRefsPerTask bounds retained orphaned tips. Parked commits exist so a
// person can recover text a conflict dropped, which is a short-lived need; the
// canonical history is the durable record.
const maxParkedRefsPerTask = 3

// reconcileRequest is one divergent task: a validated local tip that origin
// does not contain, and the validated remote tip it must be replayed onto.
type reconcileRequest struct {
	TaskID string
	Local  core.Snapshot
	Remote core.Snapshot
}

// reconcileOutcome reports one task's replay. Head is the canonical value the
// caller must move the task ref to: the remote tip when every local operation
// was a no-op or the first one conflicted, and otherwise the last replayed
// commit. Conflict and Err are mutually exclusive and both leave Head at the
// furthest point replay reached before stopping.
type reconcileOutcome struct {
	TaskID    string
	Head      string
	Parked    string
	ParkedRef string
	Replayed  int
	Skipped   int
	Snapshot  core.Snapshot
	Conflict  *core.Conflict
	Err       error
}

// reconcileDivergentTasks replays each task's local-only operation packs onto
// its fetched remote tip and returns the resulting canonical head. It writes
// commit objects but never moves a ref: the caller applies every outcome in the
// same compare-and-swap transaction that performs ordinary fetch updates, so an
// interrupted run leaves unreferenced objects rather than a half-reconciled ref.
//
// graph must be the parent graph the head classification already produced, and
// dependencies must be the post-fetch dependency edges of every active task.
func (r *Repository) reconcileDivergentTasks(
	ctx context.Context,
	config core.ProjectConfig,
	graph map[string][]string,
	dependencies map[string][]string,
	requests []reconcileRequest,
) ([]reconcileOutcome, error) {
	outcomes := make([]reconcileOutcome, len(requests))
	if len(requests) == 0 {
		return outcomes, nil
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}

	parked, err := r.nextParkedRefIndexes(ctx, config)
	if err != nil {
		return nil, err
	}

	// Reading every task's base and local-only commits through one batch keeps
	// the Git process count independent of how many tasks diverged.
	var heads []TaskHead
	spans := make([][]string, len(requests))
	for index, request := range requests {
		outcomes[index].TaskID = request.TaskID
		outcomes[index].Head = request.Remote.Head
		outcomes[index].Parked = request.Local.Head
		outcomes[index].Snapshot = request.Remote
		// Replay onto a history this build cannot fold is not hard, it is
		// undefined: the operations would have to be applied to a checkpoint
		// whose rules are unknown here. So it is refused before anything is
		// written, and refused in the direction that loses nothing — the
		// canonical ref is left exactly where it is, holding every local
		// operation, and the fetched tip stays in the tracking namespace until
		// a build that can fold it arrives. Nothing is parked, because nothing
		// was replaced.
		if err := refuseNewerWriterReplay(request); err != nil {
			outcomes[index].Err = err
			continue
		}
		base, localOnly, err := localOnlyCommits(graph, request)
		if err != nil {
			outcomes[index].Err = err
			continue
		}
		spans[index] = append([]string{base}, localOnly...)
		for _, objectID := range spans[index] {
			heads = append(heads, TaskHead{TaskID: request.TaskID, ObjectID: objectID})
		}
	}

	tips, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}
	subjects, err := r.readCommitSubjects(ctx, heads)
	if err != nil {
		return nil, err
	}

	position := 0
	for index, request := range requests {
		span := spans[index]
		if span == nil {
			continue
		}
		read := tips[position : position+len(span)]
		position += len(span)
		if outcomes[index].Err != nil {
			continue
		}
		for _, tip := range read {
			if tip.Err != nil {
				outcomes[index].Err = tip.Err
				break
			}
		}
		if outcomes[index].Err != nil {
			continue
		}

		// Each task checks cycles against the fetched graph plus its own
		// replayed edges. Letting one task's replay feed another's check would
		// make the outcome depend on task order, and closing a cycle across two
		// concurrently changed tasks is already possible through two clean
		// fast-forwards that conflict with nothing.
		replay := taskReplay{
			config:       config,
			taskID:       request.TaskID,
			base:         read[0].Snapshot.State,
			parent:       request.Remote,
			dependencies: taskDependencyGraph(dependencies, request.TaskID, request.Remote.State.Task),
			subjects:     subjects,
		}
		for _, tip := range read[1:] {
			done, err := replay.next(ctx, r, tip.Snapshot)
			if err != nil {
				outcomes[index].Err = err
				break
			}
			if done {
				break
			}
		}
		outcomes[index].Head = replay.parent.Head
		outcomes[index].Snapshot = replay.parent
		outcomes[index].Replayed = replay.replayed
		outcomes[index].Skipped = replay.skipped
		outcomes[index].Conflict = replay.conflict
		outcomes[index].ParkedRef = fmt.Sprintf("%s%s/%d", reconciledRefPrefix, request.TaskID, parked[request.TaskID])
	}
	return outcomes, nil
}

// refuseNewerWriterReplay reports a divergence this build must not resolve.
//
// It is not what makes the replay safe. core.Apply is: it refuses to fold onto
// a checkpoint carrying a watermark this build cannot meet, so deleting this
// function changes the outcome of a synchronization in no way a test can see —
// the replay still stops, the ref still keeps its local operations, the run
// still exits newer-writer. What this buys is the wording and the moment. The
// refusal happens before any object is written rather than partway through a
// chain, and it says what became of the local work, which is the question
// somebody reading it actually has; Apply can only say that the task was
// written by a newer Workbook.
//
// Either side is checked. Origin's tip carrying the marker is the case the
// contract is about; the local tip carrying it means this clone fetched a newer
// history and then authored on top of it, which the mutation gate already
// refuses. Checking both costs a comparison.
func refuseNewerWriterReplay(request reconcileRequest) error {
	if !request.Remote.State.RequiresNewerReader() && !request.Local.State.RequiresNewerReader() {
		return nil
	}
	return core.Errorf(core.CategoryNewerWriter,
		"task %s has local changes that were not replayed: origin's history for it was written by a "+
			"newer workbook; upgrade workbook to publish them. They are unchanged on this clone's task ref.",
		request.TaskID)
}

// taskReplay carries the state one task's replay advances through. The base
// checkpoint stays fixed because it is the three-way base every description
// comparison is made against.
type taskReplay struct {
	config       core.ProjectConfig
	taskID       string
	base         core.StateDocument
	parent       core.Snapshot
	dependencies map[string][]string
	subjects     map[string]string
	replayed     int
	skipped      int
	conflict     *core.Conflict
}

// next replays one local operation pack. It reports done when replay must stop,
// which happens only for a conflict; every other concurrent change is silent
// last-syncer-wins.
func (replay *taskReplay) next(ctx context.Context, r *Repository, local core.Snapshot) (bool, error) {
	if conflict := replay.classify(local.Operation); conflict != nil {
		replay.conflict = conflict
		return true, nil
	}
	if replayNoOp(replay.parent.State, local.Operation) {
		replay.skipped++
		return false, nil
	}

	// Only the logical clock is rewritten. Actor, wall time, and operation
	// ULIDs are the record of what someone actually did, and rewriting them
	// would make the replayed history a different claim than the original.
	pack := local.Operation
	pack.LogicalClock = replay.parent.State.LogicalClock + 1
	state, err := core.Apply(&replay.parent.State, pack, replay.config.Key)
	if err != nil {
		// A newer-writer refusal keeps its own category rather than being
		// restated as corruption. reconcileDivergentTasks refuses such a
		// divergence before replay begins, so this is defence rather than a
		// live path — but the category is the whole point of the signal, and a
		// wrapper that silently drops it would be a bug nobody could see.
		category := core.CategoryCorruptData
		if core.CategoryOf(err) == core.CategoryNewerWriter {
			category = core.CategoryNewerWriter
		}
		return false, core.Wrap(
			category,
			fmt.Sprintf("cannot replay task %s operation %s onto the fetched tip", replay.taskID, local.Head),
			err,
		)
	}
	if sameTaskState(replay.parent.State.Task, state.Task) {
		replay.skipped++
		return false, nil
	}

	subject := replay.subjects[local.Head]
	if subject == "" {
		subject = "workbook: replay task operation"
	}
	head, err := r.writeTaskObjects(ctx, replay.parent.Head, pack, state, subject)
	if err != nil {
		return false, err
	}
	replay.parent = core.Snapshot{Head: head, Operation: pack, State: state}
	replay.replayed++
	replay.dependencies[replay.taskID] = append([]string(nil), state.Task.Dependencies...)
	return false, nil
}

// classify reports the one situation, if any, in which this pack expresses
// intent that the fetched history already contradicts.
func (replay *taskReplay) classify(pack core.OperationPack) *core.Conflict {
	parent := replay.parent.State.Task
	if parent.Deleted && !replayNoOp(replay.parent.State, pack) {
		blocked := pack.Operations[0]
		for _, operation := range pack.Operations {
			if operation.Type != core.OperationTaskRestore {
				blocked = operation
				break
			}
		}
		return &core.Conflict{
			TaskID: replay.taskID,
			Type:   core.ConflictTombstone,
			Tombstone: &core.TombstoneConflict{
				OperationID: blocked.ID,
				Operation:   blocked.Type,
				Field:       blocked.Field,
				Value:       blocked.Value,
			},
		}
	}

	for _, operation := range pack.Operations {
		switch {
		case operation.Type == core.OperationFieldSet && operation.Field == "description":
			// Upstream leaving the description alone means nothing was
			// concurrently written, and matching values mean both sides already
			// agree. Only genuinely divergent prose needs a person.
			if parent.Description == replay.base.Task.Description || operation.Value == parent.Description {
				continue
			}
			return &core.Conflict{
				TaskID: replay.taskID,
				Type:   core.ConflictDescription,
				Description: &core.DescriptionConflict{
					Base:   replay.base.Task.Description,
					Ours:   operation.Value,
					Theirs: parent.Description,
				},
			}
		case operation.Type == core.OperationSetAdd && operation.Field == "dependencies":
			if hasValue(parent.Dependencies, operation.Value) {
				continue
			}
			path := core.DependencyClosingPath(replay.dependencies, replay.taskID, operation.Value)
			if path == nil {
				continue
			}
			return &core.Conflict{
				TaskID: replay.taskID,
				Type:   core.ConflictDependencyCycle,
				Dependency: &core.DependencyConflict{
					From: replay.taskID,
					To:   operation.Value,
					Path: path,
				},
			}
		}
	}
	return nil
}

// replayNoOp reports packs whose effect the fetched history already contains.
// A tombstone applied to an already-tombstoned task is the common case: both
// clones deleted the same task, and recording it twice would say nothing.
func replayNoOp(parent core.StateDocument, pack core.OperationPack) bool {
	if !parent.Task.Deleted {
		return false
	}
	for _, operation := range pack.Operations {
		if operation.Type != core.OperationTaskTombstone {
			return false
		}
	}
	return true
}

// sameTaskState compares two checkpoints for the operator-visible change that
// decides whether a replayed pack earns a commit. The update timestamp is
// display metadata, so a pack that only restates upstream values is dropped
// rather than recorded as an empty edit.
func sameTaskState(parent, next core.TaskData) bool {
	if parent.Title != next.Title ||
		parent.Description != next.Description ||
		parent.Status != next.Status ||
		parent.Priority != next.Priority ||
		parent.Rank != next.Rank ||
		parent.Deleted != next.Deleted ||
		!parent.CreatedAt.Equal(next.CreatedAt) {
		return false
	}
	return sameStrings(parent.Labels, next.Labels) &&
		sameStrings(parent.Dependencies, next.Dependencies) &&
		core.SameAssignments(parent.Assignments, next.Assignments) &&
		core.SameComments(parent.Comments, next.Comments) &&
		core.SameAttachments(parent.Attachments, next.Attachments)
}

func sameStrings(left, right []string) bool {
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

// taskDependencyGraph copies the fetched edges so one task's replay can update
// its own entry without changing what another task is checked against.
func taskDependencyGraph(fetched map[string][]string, taskID string, task core.TaskData) map[string][]string {
	graph := make(map[string][]string, len(fetched)+1)
	for id, edges := range fetched {
		graph[id] = edges
	}
	if task.Deleted {
		delete(graph, taskID)
		return graph
	}
	graph[taskID] = append([]string(nil), task.Dependencies...)
	return graph
}

func hasValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// localOnlyCommits returns the shared base commit and the local commits that
// origin does not have, oldest first. Both histories are single-parent chains,
// which is what lets one linear walk answer this without a Git process.
func localOnlyCommits(graph map[string][]string, request reconcileRequest) (string, []string, error) {
	remote, err := commitChain(graph, request.Remote.Head)
	if err != nil {
		return "", nil, core.Wrap(core.CategoryCorruptData,
			fmt.Sprintf("cannot walk fetched history for task %s", request.TaskID), err)
	}
	shared := make(map[string]struct{}, len(remote))
	for _, objectID := range remote {
		shared[objectID] = struct{}{}
	}

	local, err := commitChain(graph, request.Local.Head)
	if err != nil {
		return "", nil, core.Wrap(core.CategoryCorruptData,
			fmt.Sprintf("cannot walk local history for task %s", request.TaskID), err)
	}
	for index, objectID := range local {
		if _, found := shared[objectID]; !found {
			continue
		}
		newestFirst := local[:index]
		oldestFirst := make([]string, len(newestFirst))
		for offset := range newestFirst {
			oldestFirst[len(newestFirst)-1-offset] = newestFirst[offset]
		}
		return objectID, oldestFirst, nil
	}
	return "", nil, core.Errorf(
		core.CategoryCorruptData,
		"task %s local and fetched histories share no common commit",
		request.TaskID,
	)
}

// commitChain walks one single-parent history newest first. A commit with more
// than one parent is rejected here for the same reason the tip and history
// readers reject it: Workbook never writes one, so encountering one means the
// ref is not a Workbook history.
func commitChain(graph map[string][]string, head string) ([]string, error) {
	chain := make([]string, 0, len(graph))
	seen := make(map[string]struct{}, len(graph))
	for current := head; ; {
		if _, repeated := seen[current]; repeated {
			return nil, core.Errorf(core.CategoryCorruptData, "history contains a parent cycle at commit %q", current)
		}
		seen[current] = struct{}{}
		chain = append(chain, current)
		parents, found := graph[current]
		if !found {
			return nil, core.Errorf(core.CategoryCorruptData, "parent graph omitted commit %q", current)
		}
		if len(parents) > 1 {
			return nil, core.Errorf(core.CategoryCorruptData, "commit %q has more than one parent", current)
		}
		if len(parents) == 0 {
			return chain, nil
		}
		current = parents[0]
	}
}

// readCommitSubjects returns each commit's message so a replayed commit keeps
// the description its author wrote. Messages are presentation only; every
// authoritative value is parsed from the operation blob.
func (r *Repository) readCommitSubjects(ctx context.Context, heads []TaskHead) (map[string]string, error) {
	subjects := make(map[string]string, len(heads))
	if len(heads) == 0 {
		return subjects, nil
	}
	var input bytes.Buffer
	ordered := make([]string, 0, len(heads))
	for _, head := range heads {
		if _, seen := subjects[head.ObjectID]; seen {
			continue
		}
		subjects[head.ObjectID] = ""
		ordered = append(ordered, head.ObjectID)
		fmt.Fprintln(&input, head.ObjectID)
	}

	// Streamed rather than buffered, so one commit is resident at a time and the
	// per-object ceiling bounds this read instead of merely describing memory
	// already spent.
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(input.Bytes())
		return err
	})
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for _, objectID := range ordered {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read task commit messages", err)
		}
		if object.refused != nil {
			return nil, object.refused
		}
		if object.missing || object.kind != "commit" || object.objectID != objectID {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unexpected object for commit %q", objectID)
		}
		headerEnd := bytes.Index(object.contents, []byte("\n\n"))
		if headerEnd < 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "task commit %q has no header terminator", objectID)
		}
		subjects[objectID] = strings.TrimRight(string(object.contents[headerEnd+2:]), "\n")
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return subjects, nil
}

// validParkedRefName reports whether a ref name is one this clone could have
// constructed for the named task. The ref-update transaction checks it because
// a name reaching Git from a wrong task would delete or create the wrong ref.
func validParkedRefName(taskID, name string) bool {
	suffix, found := strings.CutPrefix(name, reconciledRefPrefix+taskID+"/")
	if !found {
		return false
	}
	index, err := strconv.Atoi(suffix)
	return err == nil && index >= 0 && strconv.Itoa(index) == suffix
}

type parkedRef struct {
	taskID   string
	index    int
	name     string
	objectID string
}

// parkedTaskHeads returns the orphaned tips this clone retains, grouped by task.
func (r *Repository) parkedTaskHeads(ctx context.Context, config core.ProjectConfig) (map[string]map[string]struct{}, error) {
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]map[string]struct{}, len(refs))
	for _, ref := range refs {
		if heads[ref.taskID] == nil {
			heads[ref.taskID] = make(map[string]struct{}, 1)
		}
		heads[ref.taskID][ref.objectID] = struct{}{}
	}
	return heads, nil
}

// nextParkedRefIndexes returns the index each task's next parked ref should
// use, one greater than the highest already present.
func (r *Repository) nextParkedRefIndexes(ctx context.Context, config core.ProjectConfig) (map[string]int, error) {
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix)
	if err != nil {
		return nil, err
	}
	next := make(map[string]int, len(refs))
	for _, ref := range refs {
		if ref.index >= next[ref.taskID] {
			next[ref.taskID] = ref.index + 1
		}
	}
	return next, nil
}

// PruneParkedRefs retires orphaned tips past the retention bound across every
// task and reports how many refs it deleted.
//
// Pruning inside a mutation bounds retention only for tasks this clone still
// mutates. A clone that fetches and reconciles but never mutates a task again
// keeps every tip that task ever orphaned, so the sweep has to be able to stand
// alone. Running it after a fetch does not contradict that fetch's refusal to
// prune: retention counted from the post-fetch state always ranks the tip the
// fetch just parked among the newest, so the recoverable work survives the
// command that orphaned it.
func (r *Repository) PruneParkedRefs(ctx context.Context, config core.ProjectConfig) (int, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return 0, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return 0, err
	}
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix)
	if err != nil {
		return 0, err
	}

	byTask := make(map[string][]parkedRef, len(refs))
	for _, ref := range refs {
		byTask[ref.taskID] = append(byTask[ref.taskID], ref)
	}
	taskIDs := make([]string, 0, len(byTask))
	for taskID := range byTask {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)

	names := make([]string, 0, len(refs))
	for _, taskID := range taskIDs {
		group := byTask[taskID]
		if len(group) <= maxParkedRefsPerTask {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].index < group[j].index })
		for _, ref := range group[:len(group)-maxParkedRefsPerTask] {
			names = append(names, ref.name)
		}
	}
	if len(names) == 0 {
		return 0, nil
	}

	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	for _, name := range names {
		fmt.Fprintf(&input, "delete %s\n", name)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(
		ctx,
		input.Bytes(),
		"update-ref", "--no-deref", "-m", "workbook: prune parked refs", "--stdin",
	); err != nil {
		return 0, err
	}
	return len(names), nil
}

// prunableParkedRefs returns the parked refs for one task that exceed the
// retention bound, oldest first.
func (r *Repository) prunableParkedRefs(ctx context.Context, config core.ProjectConfig, taskID string) ([]string, error) {
	refs, err := r.listParkedRefs(ctx, config, reconciledRefPrefix+taskID+"/")
	if err != nil {
		return nil, err
	}
	if len(refs) <= maxParkedRefsPerTask {
		return nil, nil
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].index < refs[j].index })
	names := make([]string, 0, len(refs)-maxParkedRefsPerTask)
	for _, ref := range refs[:len(refs)-maxParkedRefsPerTask] {
		names = append(names, ref.name)
	}
	return names, nil
}

// listParkedRefs enumerates the local reconciliation namespace. Entries that do
// not name a task and an index are ignored rather than reported: these refs are
// disposable local bookkeeping, and a stray hand-written ref must not be able to
// stop synchronization.
func (r *Repository) listParkedRefs(ctx context.Context, config core.ProjectConfig, prefix string) ([]parkedRef, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated ref record")
	}

	refs := make([]parkedRef, 0, bytes.Count(contents, []byte{'\n'}))
	for _, line := range bytes.Split(contents[:len(contents)-1], []byte{'\n'}) {
		name, objectID, split := strings.Cut(string(line), "\x00")
		if !split || objectID == "" {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid ref record")
		}
		if !strings.HasPrefix(name, reconciledRefPrefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside %q", reconciledRefPrefix)
		}
		taskID, suffix, found := strings.Cut(strings.TrimPrefix(name, reconciledRefPrefix), "/")
		if !found || core.ValidateTaskID(config.Key, taskID) != nil {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index < 0 || strconv.Itoa(index) != suffix {
			continue
		}
		refs = append(refs, parkedRef{taskID: taskID, index: index, name: name, objectID: objectID})
	}
	return refs, nil
}
