package compaction

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// The reasons a run reports for a task it selected and left alone.
const (
	ReasonAlreadyCompacted = "already compacted"
	ReasonNotPushed        = "has changes not yet on origin"
	ReasonNewerWriter      = "written by a newer workbook"
	ReasonUnreadable       = "history cannot be read in full"
	ReasonChanged          = "changed during the run"
)

// Candidate is one task a run will compact, as Prepare observed it.
type Candidate struct {
	TaskID, Title string
	Kind          Kind
	// Since is when the task became Done or Deleted, by Kind.
	Since time.Time
	// Tip is the head the run replaces, read at Tip.Head. Execute moves the ref
	// only while it still names this commit.
	Tip core.Snapshot
	// Commits is the number of commits the checkpoint replaces.
	Commits int
	// Attachments are live files only: links store nothing, and a file an
	// earlier run dropped has nothing left to drop.
	DroppedAttachments, KeptAttachments int
	DroppedBytes, KeptBytes             int64
	// EstimatedSavedBytes is what Git can reclaim once it prunes, never below
	// zero.
	EstimatedSavedBytes int64

	// doneAt and deletedAt are both of the task's times, which its checkpoint
	// records whatever the kind, so a later run's cutoff needs no history.
	doneAt, deletedAt time.Time
}

// Skip is a selected task a run leaves alone, and why.
type Skip struct{ TaskID, Title, Reason string }

// Plan is what a run will do. A dry run stops at it.
type Plan struct {
	RunID      string
	Candidates []Candidate
	// Skipped lists tasks of the selected kinds, past the cutoff, that the run
	// will not compact. A task inside the cutoff is not a skip; it was simply
	// not selected. The exceptions are ReasonNewerWriter and ReasonUnreadable,
	// reported whatever the cutoff, because their history cannot be replayed
	// to date them.
	Skipped             []Skip
	EstimatedSavedBytes int64
}

// Options are a run's choices. Prepare and Execute take the same value.
type Options struct {
	Criteria        Criteria
	KeepAttachments bool
	// Backup is "" for the default bundle path, "-" for no bundle, and
	// otherwise the --backup value: a directory gets the default file name.
	Backup string
	// Flags are the compact flags as given, recorded in the bundle manifest.
	Flags []string
	// RequirePushed skips any task whose tip origin does not hold. The
	// command sets it when sync is on and origin exists.
	RequirePushed bool
	// Now is what the cutoff is measured from and when the run is recorded as
	// happening. Zero means the current time.
	Now time.Time
}

func (options Options) withNow() Options {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	return options
}

// Result is a finished run: the plan, what it compacted, and what it skipped,
// including tasks that changed after the plan was made.
type Result struct {
	Plan
	Compacted []Candidate
	// BundlePath is the backup bundle's absolute path, or "" when the run
	// wrote none.
	BundlePath string
}

// Projection is the projection cache a run leaves on its checkpoints. It is
// the same store a Service reads and updates through; Refresh is what lets
// one batch take every compacted task at once.
type Projection interface {
	core.ProjectionUpdater
	Refresh(context.Context) error
}

// Deps are what a run reads and writes through.
type Deps struct {
	Repository *gitstore.Repository
	Config     core.ProjectConfig
	// Service lists the tasks, deleted ones included, reads their histories,
	// and supplies the vocabulary, IDs and actor.
	Service core.Service
	// Projection is the cache Execute moves onto the checkpoints, and Execute
	// refuses to run without it. Left behind, the cache could not catch up on
	// its own: once Git prunes the heads it last saw, it can no longer prove the
	// moves are compactions. Prepare does not use it.
	Projection Projection
}

// afterTaskHook runs after each task's ref has moved. It is a test seam for an
// interrupted run, and nil otherwise.
var afterTaskHook func(taskID string) error

// Prepare selects the tasks a run compacts and estimates what it saves. It
// writes nothing.
func Prepare(ctx context.Context, deps Deps, options Options) (Plan, error) {
	options = options.withNow()
	if deps.Service.IDs == nil {
		return Plan{}, core.Errorf(core.CategoryOperational, "compaction needs an ID source")
	}
	if deps.Service.History == nil {
		return Plan{}, core.Errorf(core.CategoryOperational, "compaction needs a task history source")
	}
	runID, err := deps.Service.IDs.New()
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{RunID: runID, Candidates: []Candidate{}, Skipped: []Skip{}}

	vocabulary := deps.Service.Vocabulary
	if vocabulary.IsZero() {
		vocabulary = core.LegacyVocabulary()
	}
	tasks, err := deps.Service.List(ctx, core.ListFilter{All: true})
	if err != nil {
		return Plan{}, err
	}
	var tracking map[string]string
	if options.RequirePushed {
		if tracking, err = deps.Repository.TrackingTaskHeads(ctx, deps.Config); err != nil {
			return Plan{}, err
		}
	}

	type selected struct {
		task core.Task
		kind Kind
	}
	var eligible []selected
	var heads []gitstore.TaskHead
	for _, task := range tasks {
		var kind Kind
		switch {
		case task.Deleted:
			if !options.Criteria.Deleted {
				continue
			}
			kind = KindDeleted
		case options.Criteria.Done && vocabulary.IsDone(task.Status):
			kind = KindDone
		default:
			continue
		}
		// This build cannot replay a newer writer's history, so it can
		// neither date the task nor summarize what it would drop.
		if task.NewerWriter {
			plan.Skipped = append(plan.Skipped, Skip{TaskID: task.ID, Title: task.Title, Reason: ReasonNewerWriter})
			continue
		}
		eligible = append(eligible, selected{task: task, kind: kind})
		heads = append(heads, gitstore.TaskHead{TaskID: task.ID, ObjectID: task.Head})
	}
	// The tips the run replaces are read from Git, in one batch, rather than
	// trusted from the cache: a checkpoint is built from exactly this state.
	tips, err := deps.Repository.ReadTaskHeads(ctx, deps.Config, heads)
	if err != nil {
		return Plan{}, err
	}

	for index, chosen := range eligible {
		task, tip := chosen.task, tips[index]
		skip := func(reason string) {
			plan.Skipped = append(plan.Skipped, Skip{TaskID: task.ID, Title: task.Title, Reason: reason})
		}
		if tip.State.RequiresNewerReader() {
			skip(ReasonNewerWriter)
			continue
		}
		history, err := deps.Service.History.CommitHistory(ctx, deps.Config, task.ID, tip.Head)
		if err != nil {
			return Plan{}, err
		}
		if _, truncation := core.ReplayHistory(history); truncation != nil {
			skip(ReasonUnreadable)
			continue
		}
		doneAt, deletedAt := BecameAt(history, vocabulary, tip.State.Task)
		since := doneAt
		if chosen.kind == KindDeleted {
			since = deletedAt
		}
		if options.Criteria.Cutoff > 0 && !(options.Now.Sub(since) > options.Criteria.Cutoff) {
			continue
		}
		if _, isCheckpoint := tip.Operation.Checkpoint(); isCheckpoint {
			skip(ReasonAlreadyCompacted)
			continue
		}
		if options.RequirePushed && tracking[task.ID] != tip.Head {
			skip(ReasonNotPushed)
			continue
		}

		candidate := Candidate{
			TaskID:    task.ID,
			Title:     tip.State.Task.Title,
			Kind:      chosen.kind,
			Since:     since,
			Tip:       tip,
			Commits:   len(history.Entries),
			doneAt:    doneAt,
			deletedAt: deletedAt,
		}
		files, bytes, _ := core.DropFileAttachments(tip.State.Task.Attachments, plan.RunID, options.Now)
		if options.KeepAttachments {
			candidate.KeptAttachments, candidate.KeptBytes = files, bytes
		} else {
			candidate.DroppedAttachments, candidate.DroppedBytes = files, bytes
		}
		reclaimable, err := deps.Repository.CompactionReclaimableBytes(ctx, deps.Config, task.ID, tip.Head)
		if err != nil {
			return Plan{}, err
		}
		candidate.EstimatedSavedBytes = max(reclaimable-candidate.KeptBytes, 0)
		plan.EstimatedSavedBytes += candidate.EstimatedSavedBytes
		plan.Candidates = append(plan.Candidates, candidate)
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].TaskID < plan.Candidates[j].TaskID })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].TaskID < plan.Skipped[j].TaskID })
	return plan, nil
}

// movedTask is one ref a run moved, which the projection must follow.
type movedTask struct {
	taskID     string
	oldTip     string
	checkpoint core.Snapshot
}

// Execute compacts plan's candidates: it writes and verifies the backup bundle
// unless options.Backup is "-", then for each task builds its checkpoint and
// compare-and-swaps the ref from the observed tip. A task whose ref moved since
// Prepare is skipped with ReasonChanged. For each task Prepare found already
// compacted it also expires any reflog entries a run that stopped early left
// behind. Last, it moves the projection onto the checkpoints.
//
// deps.Projection is required, and its absence is refused before anything is
// written. A bundle failure returns before any ref moves. Any later failure returns the
// Result so far with the error: the tasks already compacted stay compacted,
// the bundle is complete, and running again finishes the rest.
func Execute(ctx context.Context, deps Deps, plan Plan, options Options) (Result, error) {
	options = options.withNow()
	result := Result{Plan: plan, Compacted: []Candidate{}}
	if deps.Projection == nil {
		return result, core.Errorf(core.CategoryInvocation,
			"compaction needs the projection cache, so it can move it onto the checkpoints before Git prunes what it last saw")
	}
	result.Skipped = append([]Skip{}, plan.Skipped...)

	generations := make([]string, len(plan.Candidates))
	for index := range plan.Candidates {
		generation, err := deps.Service.IDs.New()
		if err != nil {
			return result, err
		}
		generations[index] = generation
	}
	if len(plan.Candidates) > 0 && options.Backup != "-" {
		path, err := writeBundle(ctx, deps, plan, options, generations)
		if err != nil {
			return result, err
		}
		result.BundlePath = path
	}

	var moved []movedTask
	finish := func(err error) (Result, error) {
		if settleErr := settleProjection(ctx, deps, moved); settleErr != nil {
			err = errors.Join(err, settleErr)
		}
		return result, err
	}
	for index, candidate := range plan.Candidates {
		checkpoint, err := compactOne(ctx, deps, plan, options, candidate, generations[index])
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			result.Skipped = append(result.Skipped, Skip{TaskID: candidate.TaskID, Title: candidate.Title, Reason: ReasonChanged})
			continue
		}
		if err != nil {
			return finish(err)
		}
		result.Compacted = append(result.Compacted, candidate)
		moved = append(moved, movedTask{taskID: candidate.TaskID, oldTip: candidate.Tip.Head, checkpoint: checkpoint})
		if afterTaskHook != nil {
			if err := afterTaskHook(candidate.TaskID); err != nil {
				return finish(err)
			}
		}
	}

	// A run that stopped between a ref move and its reflog expiry leaves the
	// task already compacted, so this rerun is the one that lets the reflog's
	// hold on the old history go. Its parked refs are left alone: the ref move
	// deleted them in the same transaction, so any parked ref there now came
	// from a later reconcile, holds work no bundle has, and is not this run's
	// to drop.
	for _, skipped := range plan.Skipped {
		if skipped.Reason != ReasonAlreadyCompacted {
			continue
		}
		if err := deps.Repository.ExpireTaskReflog(ctx, skipped.TaskID); err != nil {
			return finish(err)
		}
	}
	return finish(nil)
}

// writeBundle writes and verifies the run's backup bundle and returns its
// absolute path.
func writeBundle(ctx context.Context, deps Deps, plan Plan, options Options, generations []string) (string, error) {
	path, err := gitstore.ResolveArchivePath(deps.Repository.CommonGitDir, plan.RunID, options.Backup)
	if err != nil {
		return "", err
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	manifest := gitstore.ArchiveManifest{
		ProjectID: deps.Config.ProjectID,
		RunID:     plan.RunID,
		CreatedAt: options.Now,
		Flags:     options.Flags,
		Tasks:     make([]gitstore.ArchiveTask, 0, len(plan.Candidates)),
	}
	for index, candidate := range plan.Candidates {
		manifest.Tasks = append(manifest.Tasks, gitstore.ArchiveTask{
			TaskID:        candidate.TaskID,
			Title:         candidate.Title,
			OldTip:        candidate.Tip.Head,
			OldGeneration: candidate.Tip.State.History.Generation,
			NewGeneration: generations[index],
			Commits:       candidate.Commits,
		})
	}
	if err := deps.Repository.WriteArchiveBundle(ctx, deps.Config, path, manifest); err != nil {
		return "", err
	}
	return path, nil
}

// compactOne builds one task's checkpoint and moves its ref there. It returns
// CategoryStaleWrite, having moved nothing, when the ref is no longer at the
// tip Prepare observed.
func compactOne(
	ctx context.Context,
	deps Deps,
	plan Plan,
	options Options,
	candidate Candidate,
	generation string,
) (core.Snapshot, error) {
	ids, commits, first, last, err := deps.Repository.CompactedHistory(ctx, deps.Config, candidate.Tip.Head)
	if err != nil {
		return core.Snapshot{}, err
	}
	operationID, err := deps.Service.IDs.New()
	if err != nil {
		return core.Snapshot{}, err
	}
	summary := core.CheckpointData{
		CompactedCommits:    commits,
		FirstWallTime:       first,
		LastWallTime:        last,
		BecameDoneAt:        optionalTime(candidate.doneAt),
		BecameDeletedAt:     optionalTime(candidate.deletedAt),
		CompactedOperations: ids,
	}
	checkpoint, err := deps.Repository.BuildCheckpoint(ctx, deps.Config, gitstore.CheckpointInput{
		TaskID:          candidate.TaskID,
		Tip:             candidate.Tip,
		RunID:           plan.RunID,
		Generation:      generation,
		OperationID:     operationID,
		Actor:           deps.Service.Actor,
		Now:             options.Now,
		KeepAttachments: options.KeepAttachments,
		Summary:         summary,
	})
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := deps.Repository.CompactTaskRef(ctx, deps.Config, candidate.TaskID, candidate.Tip.Head, checkpoint.Head); err != nil {
		return core.Snapshot{}, err
	}
	return checkpoint, nil
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// settleProjection leaves the projection on every checkpoint the run wrote
// before Execute returns. Git may later prune the heads the projection last
// saw, and then it could no longer prove these moves are compactions.
//
// One refresh takes every compacted task in one batch, proving each move
// against the head the projection held. A task it did not land on its
// checkpoint, because the refresh failed or the row names some other head, is
// invalidated and read afresh by a second refresh.
func settleProjection(ctx context.Context, deps Deps, moved []movedTask) error {
	if len(moved) == 0 {
		return nil
	}
	refreshErr := deps.Projection.Refresh(ctx)
	invalidated := false
	for _, task := range moved {
		advanced, err := deps.Projection.Advance(ctx, deps.Config, task.oldTip, task.checkpoint)
		if err != nil {
			return err
		}
		if advanced {
			continue
		}
		if err := deps.Projection.Invalidate(ctx, deps.Config, task.taskID, task.oldTip, task.checkpoint.Head); err != nil {
			return err
		}
		invalidated = true
	}
	if refreshErr == nil && !invalidated {
		return nil
	}
	return deps.Projection.Refresh(ctx)
}
