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

// The reasons only a relic run reports. The others it shares with a task run:
// ReasonAlreadyCompacted, ReasonNotPushed, ReasonNewerWriter, ReasonUnreadable
// and ReasonChanged.
const (
	// ReasonRelicConflictParked is a relic with a parked tip under
	// refs/workbook/reconciled-relics/. The tip holds a person's unapplied
	// change and nothing else shows it, so the run neither compacts the relic
	// nor deletes the tip.
	ReasonRelicConflictParked = "has a relic conflict set aside"
	// ReasonTooManyChanges is a relic whose checkpoint would list more
	// operations than one stored object holds.
	ReasonTooManyChanges = "lists too many changes for one checkpoint"
)

// RelicCriteria chooses which relics a run compacts: retired ones, deleted
// ones, or both. A relic that is deleted counts as deleted whether or not it
// is also retired, so with only Retired it is never selected.
type RelicCriteria struct{ Retired, Deleted bool }

// RelicCandidate is one relic a run will compact, as PrepareRelics observed
// it.
type RelicCandidate struct {
	RelicID, Title string
	// Boundary is core.RelicBoundaryRetired or core.RelicBoundaryDeleted.
	Boundary string
	// Since is when the relic last became retired or deleted, by Boundary.
	Since time.Time
	// Tip is the head the run replaces, read at Tip.Head. ExecuteRelics moves
	// the ref only while it still names this commit.
	Tip core.RelicSnapshot
	// Commits is the number of commits the checkpoint replaces.
	Commits int
	// DroppedParts are the parts the checkpoint turns into stubs, and
	// DroppedBytes the size of their fields and bodies at the tip. A stub an
	// earlier run made has nothing left to drop.
	DroppedParts int
	DroppedBytes int64
	// EstimatedSavedBytes is what Git can reclaim once it prunes, never below
	// zero.
	EstimatedSavedBytes int64

	// history is what the checkpoint summarizes, read once at Tip.Head. The
	// ref move is a compare-and-swap on that head, so it is still the history
	// a checkpoint replaces when the run writes it.
	history relicHistory
}

// relicHistory is what CompactedRelicHistory reported for a candidate's tip.
type relicHistory struct {
	ids         []string
	commits     int
	first, last time.Time
	removedAt   map[string]time.Time
}

// RelicSkip is a selected relic a run leaves alone, and why. It is part of the
// compact command's JSON summary.
type RelicSkip struct {
	RelicID string `json:"relicId"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

// RelicPlan is what a relic run will do. A dry run stops at it.
type RelicPlan struct {
	RunID      string
	Candidates []RelicCandidate
	// Skipped lists the relics of the selected kinds the run will not
	// compact, in relic ID order.
	Skipped             []RelicSkip
	EstimatedSavedBytes int64
}

// RelicOptions are a relic run's choices. PrepareRelics and ExecuteRelics take
// the same value.
type RelicOptions struct {
	Criteria RelicCriteria
	// Backup is "" for the default bundle path, "-" for no bundle, and
	// otherwise the --backup value: a directory gets the default file name.
	Backup string
	// Flags are the compact flags as given, recorded in the bundle manifest.
	Flags []string
	// RequirePushed skips any relic whose tip is not its tracking ref: a
	// change origin did not hold when this clone last fetched. The command
	// sets it whenever a tasks remote is configured, without sync too.
	RequirePushed bool
	// Now is when the run is recorded as happening. Zero means the current
	// time.
	Now time.Time
}

func (options RelicOptions) withNow() RelicOptions {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	return options
}

// RelicResult is a finished relic run: the plan, what it compacted, and what
// it skipped, including relics that changed after the plan was made.
type RelicResult struct {
	RelicPlan
	Compacted []RelicCandidate
	// BundlePath is the backup bundle's absolute path, or "" when the run
	// wrote none.
	BundlePath string
}

// RelicIndexRefresher is the relic index a run brings up to the moved refs.
// The index re-reads any relic whose head changed, so one refresh after the
// ref moves is all a run needs; until it runs, a read answers from Git.
type RelicIndexRefresher interface {
	RefreshRelics(context.Context) error
}

// RelicDeps are what a relic run reads and writes through.
type RelicDeps struct {
	Repository *gitstore.Repository
	Config     core.ProjectConfig
	// Relics supplies the IDs and the actor. The relics themselves are read
	// from Git, never from the index: a checkpoint is built from exactly the
	// tip the ref names.
	Relics core.RelicService
	// Index is refreshed once the refs have moved, and ExecuteRelics refuses
	// to run without it. PrepareRelics does not use it.
	Index RelicIndexRefresher
}

// afterRelicHook runs after each relic's ref has moved. It is a test seam for
// an interrupted run, and nil otherwise.
var afterRelicHook func(relicID string) error

// beforeRelicBundleRetryHook runs before a relic run writes its bundle the
// second time. It is a test seam, and nil otherwise.
var beforeRelicBundleRetryHook func()

// buildRelicCheckpoint writes one relic's checkpoint. It is the repository's
// builder; a test replaces it to reach a refusal no small history causes.
var buildRelicCheckpoint = (*gitstore.Repository).BuildRelicCheckpoint

// PrepareRelics selects the relics a run compacts and estimates what it saves.
// It writes nothing. Every relic is read from Git, in one batch for the tips.
func PrepareRelics(ctx context.Context, deps RelicDeps, options RelicOptions) (RelicPlan, error) {
	options = options.withNow()
	if deps.Relics.IDs == nil {
		return RelicPlan{}, core.Errorf(core.CategoryOperational, "compaction needs an ID source")
	}
	runID, err := deps.Relics.IDs.New()
	if err != nil {
		return RelicPlan{}, err
	}
	plan := RelicPlan{RunID: runID, Candidates: []RelicCandidate{}, Skipped: []RelicSkip{}}

	heads, err := deps.Repository.ListRelicHeads(ctx, deps.Config)
	if err != nil {
		return RelicPlan{}, err
	}
	tips, err := deps.Repository.ReadRelicTips(ctx, deps.Config, heads)
	if err != nil {
		return RelicPlan{}, err
	}
	parked, err := deps.Repository.ParkedRelicHeads(ctx, deps.Config)
	if err != nil {
		return RelicPlan{}, err
	}
	var tracking map[string]string
	if options.RequirePushed {
		if tracking, err = deps.Repository.TrackingRelicHeads(ctx, deps.Config); err != nil {
			return RelicPlan{}, err
		}
	}

	for index, tip := range tips {
		relicID := heads[index].RelicID
		relic := tip.Record.State.Relic
		var boundary string
		switch {
		case relic.Deleted:
			if !options.Criteria.Deleted {
				continue
			}
			boundary = core.RelicBoundaryDeleted
		case relic.Retired && options.Criteria.Retired:
			boundary = core.RelicBoundaryRetired
		default:
			continue
		}
		skip := func(reason string) {
			plan.Skipped = append(plan.Skipped, RelicSkip{RelicID: relicID, Title: relic.Title, Reason: reason})
		}
		// The cheap checks first: none of them reads the history.
		if tip.Operation.RequiresNewerReader() || tip.Record.State.RequiresNewerReader() {
			skip(ReasonNewerWriter)
			continue
		}
		if _, isCheckpoint := tip.Operation.Checkpoint(); isCheckpoint {
			skip(ReasonAlreadyCompacted)
			continue
		}
		if len(parked[relicID]) > 0 {
			skip(ReasonRelicConflictParked)
			continue
		}
		if options.RequirePushed && tracking[relicID] != tip.Head {
			skip(ReasonNotPushed)
			continue
		}

		ids, commits, first, last, removedAt, since, err := deps.Repository.CompactedRelicHistory(ctx, deps.Config, tip.Head)
		switch core.CategoryOf(err) {
		case "":
		case core.CategoryCorruptData, core.CategoryNotFound:
			skip(ReasonUnreadable)
			continue
		case core.CategoryNewerWriter:
			skip(ReasonNewerWriter)
			continue
		default:
			return RelicPlan{}, err
		}
		if since[boundary].IsZero() {
			skip(ReasonUnreadable)
			continue
		}
		candidate := RelicCandidate{
			RelicID:  relicID,
			Title:    relic.Title,
			Boundary: boundary,
			Since:    since[boundary],
			Tip:      tip,
			Commits:  commits,
			history:  relicHistory{ids: ids, commits: commits, first: first, last: last, removedAt: removedAt},
		}
		if err := measureDroppedParts(ctx, deps, &candidate); err != nil {
			return RelicPlan{}, err
		}
		reclaimable, err := deps.Repository.RelicCompactionReclaimableBytes(ctx, deps.Config, relicID, tip.Head)
		if err != nil {
			return RelicPlan{}, err
		}
		candidate.EstimatedSavedBytes = max(reclaimable, 0)
		plan.EstimatedSavedBytes += candidate.EstimatedSavedBytes
		plan.Candidates = append(plan.Candidates, candidate)
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].RelicID < plan.Candidates[j].RelicID })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].RelicID < plan.Skipped[j].RelicID })
	return plan, nil
}

// measureDroppedParts counts the parts candidate's checkpoint turns into stubs
// and the bytes of their fields and bodies at the tip.
func measureDroppedParts(ctx context.Context, deps RelicDeps, candidate *RelicCandidate) error {
	var dropped []string
	for _, part := range candidate.Tip.Record.State.Relic.Parts {
		if part.Dropped != nil {
			continue
		}
		if candidate.Boundary == core.RelicBoundaryDeleted || part.Removed {
			dropped = append(dropped, part.ID)
		}
	}
	candidate.DroppedParts = len(dropped)
	if len(dropped) == 0 {
		return nil
	}
	commit, err := deps.Repository.ReadRelicCommit(ctx, deps.Config, candidate.RelicID, candidate.Tip.Head)
	if err != nil {
		return err
	}
	for _, partID := range dropped {
		objects := commit.Parts[partID]
		candidate.DroppedBytes += objects.FieldsBytes + objects.BodyBytes
	}
	return nil
}

// ExecuteRelics compacts plan's candidates, in the order Execute compacts
// tasks:
//
//  1. It draws a new generation for every candidate.
//  2. Unless options.Backup is "-", it writes and verifies the backup bundle
//     of every candidate's old tip. A relic whose ref moved since PrepareRelics
//     is skipped with ReasonChanged and the rest are bundled once more. Any
//     bundle failure, that second write included, returns before a ref moves.
//  3. For each candidate it builds the checkpoint and compare-and-swaps the
//     ref from the observed tip, which also expires the ref's reflog. A ref
//     that moved is skipped with ReasonChanged, and a checkpoint too large
//     for one object with ReasonTooManyChanges; neither moves anything.
//  4. For each relic PrepareRelics found already compacted, it expires the
//     reflog an earlier run that stopped after its ref move may have left.
//  5. Once any ref has moved, it refreshes the relic index once.
//
// deps.Index is required, and its absence is refused before anything is
// written. A failure in step 3 or 4 returns the result so far with the error:
// the relics already compacted stay compacted, the bundle is complete, and
// running again finishes the rest. A run that finished everything but the
// index refresh returns a *ProjectionError.
func ExecuteRelics(ctx context.Context, deps RelicDeps, plan RelicPlan, options RelicOptions) (RelicResult, error) {
	options = options.withNow()
	result := RelicResult{RelicPlan: plan, Compacted: []RelicCandidate{}}
	if deps.Index == nil {
		return result, core.Errorf(core.CategoryInvocation,
			"relic compaction needs the relic index, so it can follow the checkpoints")
	}
	if deps.Relics.IDs == nil {
		return result, core.Errorf(core.CategoryOperational, "compaction needs an ID source")
	}
	result.Skipped = append([]RelicSkip{}, plan.Skipped...)

	generations := make([]string, len(plan.Candidates))
	for index := range plan.Candidates {
		generation, err := deps.Relics.IDs.New()
		if err != nil {
			return result, err
		}
		generations[index] = generation
	}
	candidates := plan.Candidates
	if len(candidates) > 0 && options.Backup != "-" {
		path, err := writeRelicBundle(ctx, deps, plan.RunID, options, candidates, generations)
		var changed *gitstore.ArchiveTasksMovedError
		if errors.As(err, &changed) {
			var skipped []RelicSkip
			candidates, generations, skipped = withoutChangedRelics(candidates, generations, changed.RelicIDs)
			path, err = "", nil
			if len(candidates) > 0 {
				if beforeRelicBundleRetryHook != nil {
					beforeRelicBundleRetryHook()
				}
				path, err = writeRelicBundle(ctx, deps, plan.RunID, options, candidates, generations)
			}
			if err == nil {
				result.Skipped = append(result.Skipped, skipped...)
			}
		}
		if err != nil {
			return result, err
		}
		result.BundlePath = path
	}

	moved := false
	finish := func(err error) (RelicResult, error) {
		sort.Slice(result.Skipped, func(i, j int) bool { return result.Skipped[i].RelicID < result.Skipped[j].RelicID })
		if !moved {
			return result, err
		}
		refreshErr := deps.Index.RefreshRelics(ctx)
		switch {
		case refreshErr == nil:
			return result, err
		case err == nil:
			return result, &ProjectionError{Err: refreshErr, Relics: true}
		default:
			return result, errors.Join(err, refreshErr)
		}
	}
	actor := deps.Relics.Actor
	if asserted, ok := core.ActorFrom(ctx); ok {
		actor = asserted
	}
	for index, candidate := range candidates {
		err := compactRelic(ctx, deps, plan.RunID, options, candidate, generations[index], actor)
		skip := func(reason string) {
			result.Skipped = append(result.Skipped, RelicSkip{RelicID: candidate.RelicID, Title: candidate.Title, Reason: reason})
		}
		switch {
		case err == nil:
		case errors.Is(err, gitstore.ErrRelicCheckpointTooLarge):
			skip(ReasonTooManyChanges)
			continue
		case core.CategoryOf(err) == core.CategoryStaleWrite:
			skip(ReasonChanged)
			continue
		default:
			return finish(err)
		}
		moved = true
		result.Compacted = append(result.Compacted, candidate)
		if afterRelicHook != nil {
			if err := afterRelicHook(candidate.RelicID); err != nil {
				return finish(err)
			}
		}
	}

	// A run that stopped between a ref move and its reflog expiry leaves the
	// relic already compacted, so this rerun is the one that lets the
	// reflog's hold on the old history go.
	for _, skipped := range plan.Skipped {
		if skipped.Reason != ReasonAlreadyCompacted {
			continue
		}
		if err := deps.Repository.ExpireRelicReflog(ctx, skipped.RelicID); err != nil {
			return finish(err)
		}
	}
	return finish(nil)
}

// withoutChangedRelics drops the candidates named in changed, with their
// generations, and reports each as a ReasonChanged skip.
func withoutChangedRelics(
	candidates []RelicCandidate,
	generations []string,
	changed []string,
) ([]RelicCandidate, []string, []RelicSkip) {
	drop := make(map[string]struct{}, len(changed))
	for _, relicID := range changed {
		drop[relicID] = struct{}{}
	}
	keptCandidates := make([]RelicCandidate, 0, len(candidates))
	keptGenerations := make([]string, 0, len(generations))
	var skipped []RelicSkip
	for index, candidate := range candidates {
		if _, found := drop[candidate.RelicID]; found {
			skipped = append(skipped, RelicSkip{RelicID: candidate.RelicID, Title: candidate.Title, Reason: ReasonChanged})
			continue
		}
		keptCandidates = append(keptCandidates, candidate)
		keptGenerations = append(keptGenerations, generations[index])
	}
	return keptCandidates, keptGenerations, skipped
}

// writeRelicBundle writes and verifies the backup bundle for candidates and
// returns its absolute path.
func writeRelicBundle(
	ctx context.Context,
	deps RelicDeps,
	runID string,
	options RelicOptions,
	candidates []RelicCandidate,
	generations []string,
) (string, error) {
	path, err := gitstore.ResolveArchivePath(deps.Repository.CommonGitDir, runID, options.Backup)
	if err != nil {
		return "", err
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	manifest := gitstore.ArchiveManifest{
		ProjectID: deps.Config.ProjectID,
		RunID:     runID,
		CreatedAt: options.Now,
		Flags:     options.Flags,
		Tasks:     []gitstore.ArchiveTask{},
		Relics:    make([]gitstore.ArchiveRelic, 0, len(candidates)),
	}
	for index, candidate := range candidates {
		manifest.Relics = append(manifest.Relics, gitstore.ArchiveRelic{
			RelicID:       candidate.RelicID,
			Title:         candidate.Title,
			OldTip:        candidate.Tip.Head,
			OldGeneration: candidate.Tip.Record.State.History.Generation,
			NewGeneration: generations[index],
			Commits:       candidate.Commits,
		})
	}
	if err := deps.Repository.WriteRelicArchiveBundle(ctx, deps.Config, path, manifest); err != nil {
		return "", err
	}
	return path, nil
}

// compactRelic builds one relic's checkpoint and moves its ref there. It
// returns CategoryStaleWrite, having moved nothing, when the ref is no longer
// at the tip PrepareRelics observed, and ErrRelicCheckpointTooLarge, having
// written nothing, for a checkpoint too large for one object.
func compactRelic(
	ctx context.Context,
	deps RelicDeps,
	runID string,
	options RelicOptions,
	candidate RelicCandidate,
	generation, actor string,
) error {
	operationID, err := deps.Relics.IDs.New()
	if err != nil {
		return err
	}
	history := candidate.history
	checkpoint, err := buildRelicCheckpoint(deps.Repository, ctx, deps.Config, gitstore.RelicCheckpointInput{
		RelicID:     candidate.RelicID,
		Tip:         candidate.Tip,
		RunID:       runID,
		Generation:  generation,
		OperationID: operationID,
		Actor:       actor,
		Boundary:    candidate.Boundary,
		Since:       candidate.Since,
		Now:         options.Now,
		RemovedAt:   history.removedAt,
		Summary: core.RelicCheckpointData{
			CompactedCommits:    history.commits,
			FirstWallTime:       history.first,
			LastWallTime:        history.last,
			CompactedOperations: history.ids,
		},
	})
	if err != nil {
		return err
	}
	return deps.Repository.CompactRelicRef(ctx, deps.Config, candidate.RelicID, candidate.Tip.Head, checkpoint.Head)
}
