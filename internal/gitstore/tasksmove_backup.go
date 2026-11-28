package gitstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// A move back can find, at the place the tasks return to, an older copy of a
// task that was compacted by hand at the place they are leaving. The
// checkpoint reaches it only by replacing that copy: a non-fast-forward push
// of that one task ref, under a lease on exactly the tip this run read. That
// is the compaction exception AGENTS.md allows, used a second, narrower way:
// only for a task already compacted by hand, and only when moving the tasks
// back onto a stale copy of it.
//
// Before the first such push in a run, the move writes a Git bundle of every
// tip it is about to make unreachable there, with the same manifest, default
// location and flags `workbook compact` uses, so the commits replaced at the
// returned-to place, a teammate's late edit among them, are kept somewhere a
// person can find them. Every operation they hold is also present afterwards
// at the returned-to place, carried onto the checkpoint or set aside and
// reported, so the bundle is a recovery copy, not the only record.
//
//	returned-to place, before:   refs/workbook/tasks/T ──> E (late edit) ── C ── … ── create
//	place left, compacted:       refs/workbook/tasks/T ──> K (checkpoint, lists C … create)
//	returned-to place, after:    refs/workbook/tasks/T ──> E' (E replayed) ── K
//	bundle in the mover's clone: refs/workbook/remotes/<place>/tasks/T ──> E, and the manifest
//
// A lease that finds the tip changed is never pushed over: the run reads the
// place again, writes a new bundle of what it holds now, and tries again.
type moveBackup struct {
	remote       string
	noBackup     bool
	backup       string
	runID        string
	flags        []string
	now          func() time.Time
	commonGitDir string
	// bundles are the paths written, in order.
	bundles []string
	// replaced names the tasks whose copy at remote a leased push replaced.
	replaced map[string]struct{}
	// stale says a leased push found the tip it was taken on changed, or a
	// bundle found the tracking ref moved, so the synchronization is run
	// again.
	stale bool
	// setAside is the edits made at remote, to tasks deleted and compacted
	// at the place the tasks leave, that the run set aside, by task.
	setAside map[string]moveSetAsideAt
}

// moveSetAsideAt is one task whose edits made at the place the tasks return
// to were set aside, because it was deleted and compacted at the place they
// leave.
type moveSetAsideAt struct {
	taskID, title string
	operations    []string
	parkedRef     string
	conflict      *core.Conflict
}

// noteMoveSetAside records a set-aside edit for the move's report. A later
// synchronization in the same run that parks the task again, because the
// copy changed before the push, replaces the entry, so each task is reported
// once, under the newest parked ref.
func (r *Repository) noteMoveSetAside(backup *moveBackup, item moveSetAsideAt) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if backup.setAside == nil {
		backup.setAside = map[string]moveSetAsideAt{}
	}
	backup.setAside[item.taskID] = item
}

// takeMoveSetAside returns, sorted by task, what the run set aside at the
// place the tasks return to so far.
func (r *Repository) takeMoveSetAside() []moveSetAsideAt {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if r.moveBackup == nil {
		return nil
	}
	items := make([]moveSetAsideAt, 0, len(r.moveBackup.setAside))
	for _, item := range r.moveBackup.setAside {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].taskID < items[j].taskID })
	return items
}

// errReplacedTipMoved is a bundle that found a tip it was to hold changed
// since the lease was taken on it.
var errReplacedTipMoved = errors.New("a replaced tip moved before it was bundled")

// beginMoveBackup installs the backup for a move to remote.
func (r *Repository) beginMoveBackup(remote string, options MoveTasksOptions) error {
	runID := options.RunID
	if runID == "" {
		id, err := core.CryptoULIDSource{}.New()
		if err != nil {
			return err
		}
		runID = id
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.moveBackup = &moveBackup{
		remote:       remote,
		noBackup:     options.NoBackup,
		backup:       options.Backup,
		runID:        runID,
		flags:        append([]string(nil), options.Flags...),
		now:          now,
		commonGitDir: r.CommonGitDir,
		replaced:     map[string]struct{}{},
	}
	return nil
}

// endMoveBackup removes the backup and returns what it recorded.
func (r *Repository) endMoveBackup() *moveBackup {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	backup := r.moveBackup
	r.moveBackup = nil
	return backup
}

// activeMoveBackup is the backup for a push to remote, or nil.
func (r *Repository) activeMoveBackup(remote string) *moveBackup {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if r.moveBackup == nil || r.moveBackup.remote != remote {
		return nil
	}
	return r.moveBackup
}

// takeStaleLease reports, and clears, whether a leased push since the last
// call found its tip changed.
func (r *Repository) takeStaleLease() bool {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if r.moveBackup == nil {
		return false
	}
	stale := r.moveBackup.stale
	r.moveBackup.stale = false
	return stale
}

// backUpReplacedTips writes the bundle of the tips leases replace at the
// backup's remote, before the push that replaces them. The tips are read
// from that remote's tracking refs, which hold what this run read there;
// one that moved since is errReplacedTipMoved, and the push is not made.
//
// A lease is a task's or a relic's (leasedPush.RelicID). A relic copy is held
// in the bundle under its relic tracking name, as a task copy is under its
// task tracking name, and the manifest lists it under relics. A relic has no
// parked tip to bundle with it: parking one keeps it in this checkout, and
// the push replaces only the copy at the remote.
func (r *Repository) backUpReplacedTips(ctx context.Context, config core.ProjectConfig, backup *moveBackup, leases []leasedPush) error {
	if backup.noBackup {
		return nil
	}
	var taskLeases, relicLeases []leasedPush
	for _, lease := range leases {
		if lease.RelicID != "" {
			relicLeases = append(relicLeases, lease)
		} else {
			taskLeases = append(taskLeases, lease)
		}
	}
	manifest := ArchiveManifest{
		ProjectID: config.ProjectID,
		RunID:     backup.runID,
		CreatedAt: backup.now(),
		Flags:     append([]string{"move-tasks"}, backup.flags...),
	}
	if len(backup.bundles) > 0 {
		manifest.RunID = fmt.Sprintf("%s-%d", backup.runID, len(backup.bundles)+1)
	}
	tasks, err := r.replacedTaskCopies(ctx, config, backup, taskLeases)
	if err != nil {
		return err
	}
	manifest.Tasks = tasks
	relics, err := r.replacedRelicCopies(ctx, config, backup, relicLeases)
	if err != nil {
		return err
	}
	manifest.Relics = relics
	path, err := ResolveArchivePath(backup.commonGitDir, manifest.RunID, backup.backup)
	if err != nil {
		return err
	}
	if len(backup.bundles) > 0 && backup.backup != "" && path == backup.bundles[0] {
		// --backup named a file: later bundles in the run go beside it.
		extension := filepath.Ext(path)
		path = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(path, extension), len(backup.bundles)+1, extension)
	}
	if path, err = filepath.Abs(path); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
	}
	if err := r.writeMoveBundle(ctx, config, path, manifest, backup.remote); err != nil {
		var moved *ArchiveTasksMovedError
		if errors.As(err, &moved) {
			return errReplacedTipMoved
		}
		return err
	}
	backup.bundles = append(backup.bundles, path)
	return nil
}

// replacedTaskCopies is the manifest entry of each task copy leases replace,
// or errReplacedTipMoved when its tracking ref no longer holds the tip leased.
func (r *Repository) replacedTaskCopies(ctx context.Context, config core.ProjectConfig, backup *moveBackup, leases []leasedPush) ([]ArchiveTask, error) {
	if len(leases) == 0 {
		return nil, nil
	}
	prefix := remoteTaskRefPrefixFor(backup.remote)
	oldHeads := make([]TaskHead, 0, len(leases))
	newHeads := make([]TaskHead, 0, len(leases))
	for _, lease := range leases {
		oldHeads = append(oldHeads, TaskHead{TaskID: lease.TaskID, ObjectID: lease.ExpectedRemote})
		newHeads = append(newHeads, TaskHead{TaskID: lease.TaskID, ObjectID: lease.Local})
	}
	oldTips, err := r.readTaskHeadsPartial(ctx, config, oldHeads)
	if err != nil {
		return nil, err
	}
	newTips, err := r.readTaskHeadsPartial(ctx, config, newHeads)
	if err != nil {
		return nil, err
	}
	oldChains, err := r.completeTaskChains(ctx, config, oldHeads)
	if err != nil {
		return nil, err
	}
	tasks := make([]ArchiveTask, 0, len(leases))
	for index, lease := range leases {
		if oldTips[index].Err != nil {
			return nil, wrapKeepingCategory("cannot read the copy of task "+lease.TaskID+" that "+backup.remote+" holds, to back it up", oldTips[index].Err)
		}
		task := ArchiveTask{
			TaskID:        lease.TaskID,
			Title:         oldTips[index].Snapshot.State.Task.Title,
			OldTip:        lease.ExpectedRemote,
			OldGeneration: oldTips[index].Snapshot.Operation.HistoryGeneration,
			Commits:       len(oldChains[index]),
		}
		if newTips[index].Err == nil {
			task.NewGeneration = newTips[index].Snapshot.Operation.HistoryGeneration
		}
		if err := r.requireTrackingAt(ctx, prefix+lease.TaskID, lease.ExpectedRemote); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// replacedRelicCopies is replacedTaskCopies for relic leases.
func (r *Repository) replacedRelicCopies(ctx context.Context, config core.ProjectConfig, backup *moveBackup, leases []leasedPush) ([]ArchiveRelic, error) {
	if len(leases) == 0 {
		return nil, nil
	}
	prefix := relicTrackingRefPrefixFor(backup.remote)
	heads := make([]core.RelicHead, 0, 2*len(leases))
	for _, lease := range leases {
		heads = append(heads,
			core.RelicHead{RelicID: lease.RelicID, ObjectID: lease.ExpectedRemote},
			core.RelicHead{RelicID: lease.RelicID, ObjectID: lease.Local})
	}
	read, errs := r.readRelicCommitDocuments(ctx, config, heads)
	relics := make([]ArchiveRelic, 0, len(leases))
	for index, lease := range leases {
		if err := errs[2*index]; err != nil {
			return nil, wrapKeepingCategory("cannot read the copy of relic "+lease.RelicID+" that "+backup.remote+" holds, to back it up", err)
		}
		count, err := r.Git(ctx, nil, "rev-list", "--count", lease.ExpectedRemote)
		if err != nil {
			return nil, err
		}
		commits, err := strconv.Atoi(strings.TrimSpace(string(count)))
		if err != nil {
			return nil, core.Wrap(core.CategoryOperational, "cannot count the commits of relic "+lease.RelicID, err)
		}
		old := read[2*index]
		relic := ArchiveRelic{
			RelicID:       lease.RelicID,
			Title:         old.Record.State.Relic.Title,
			OldTip:        lease.ExpectedRemote,
			OldGeneration: old.Operation.HistoryGeneration,
			Commits:       commits,
		}
		if errs[2*index+1] == nil {
			relic.NewGeneration = read[2*index+1].Operation.HistoryGeneration
		}
		if err := r.requireTrackingAt(ctx, prefix+lease.RelicID, lease.ExpectedRemote); err != nil {
			return nil, err
		}
		relics = append(relics, relic)
	}
	return relics, nil
}

// requireTrackingAt is errReplacedTipMoved unless the tracking ref holds tip.
func (r *Repository) requireTrackingAt(ctx context.Context, ref, tip string) error {
	current, err := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || strings.TrimSpace(string(current)) != tip {
		return errReplacedTipMoved
	}
	return nil
}

// writeMoveBundle is writeArchiveBundle for a move's backup, which may hold
// relic copies beside task copies: each task's old tip under remote's task
// tracking name with its parked refs, each relic's under remote's relic
// tracking name.
func (r *Repository) writeMoveBundle(ctx context.Context, config core.ProjectConfig, path string, manifest ArchiveManifest, remote string) error {
	if len(manifest.Relics) == 0 {
		return r.writeArchiveBundle(ctx, config, path, manifest, remoteTaskRefPrefixFor(remote))
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return err
	}
	manifest, err := r.canonicalArchiveManifest(ctx, config, manifest)
	if err != nil {
		return err
	}
	parked, err := r.archiveParkedRefs(ctx, config, manifest.Tasks)
	if err != nil {
		return err
	}
	refs := make([]bundledRef, 0, len(manifest.Tasks)+len(manifest.Relics)+len(parked))
	for _, task := range manifest.Tasks {
		refs = append(refs, bundledRef{name: remoteTaskRefPrefixFor(remote) + task.TaskID, expected: task.OldTip, taskID: task.TaskID})
	}
	for _, relic := range manifest.Relics {
		refs = append(refs, bundledRef{name: relicTrackingRefPrefixFor(remote) + relic.RelicID, expected: relic.OldTip, relicID: relic.RelicID})
	}
	for _, name := range parked {
		refs = append(refs, bundledRef{name: name})
	}
	return r.writeRefsBundle(ctx, path, manifest, refs)
}

// backUpRelicLeases is what a relic push does, during a move, between taking
// its leases (leaseRelicCheckpoints) and pushing (pushRelicHeads): it writes
// the move's bundle of every relic copy a lease replaces at the remote the
// move returns to, before the push, as publishTaskHeads does for tasks. With
// no move under way for this remote, no lease, or --no-backup, it returns
// candidates unchanged. A copy that moved since its lease was taken is never
// pushed over: every leased candidate is left out of the push and reported
// rejected, and the move synchronizes again (syncDestination), which reads
// the place afresh, bundles what it holds now and leases on that.
func (r *Repository) backUpRelicLeases(
	ctx context.Context,
	config core.ProjectConfig,
	candidates []relicPushCandidate,
) ([]relicPushCandidate, map[string]SyncRelicResult, error) {
	backup := r.activeMoveBackup(r.remote())
	if backup == nil {
		return candidates, nil, nil
	}
	var leases []leasedPush
	for _, candidate := range candidates {
		if candidate.Lease != "" {
			leases = append(leases, leasedPush{RelicID: candidate.RelicID, Local: candidate.Head, ExpectedRemote: candidate.Lease})
		}
	}
	if len(leases) == 0 {
		return candidates, nil, nil
	}
	err := r.backUpReplacedTips(ctx, config, backup, leases)
	if err == nil {
		if r.leasedPushHook != nil {
			r.leasedPushHook()
		}
		return candidates, nil, nil
	}
	if !errors.Is(err, errReplacedTipMoved) {
		return nil, nil, err
	}
	r.metadataMu.Lock()
	backup.stale = true
	r.metadataMu.Unlock()
	unleased := candidates[:0:0]
	rejected := map[string]SyncRelicResult{}
	for _, candidate := range candidates {
		if candidate.Lease == "" {
			unleased = append(unleased, candidate)
			continue
		}
		rejected[candidate.RelicID] = SyncRelicResult{RelicID: candidate.RelicID, Status: SyncRejected,
			Detail: fmt.Sprintf("relic %s: %s", candidate.RelicID, compactionStaleDetail(r.remote()))}
	}
	return unleased, rejected, nil
}
