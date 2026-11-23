package gitstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
func (r *Repository) backUpReplacedTips(ctx context.Context, config core.ProjectConfig, backup *moveBackup, leases []leasedPush) error {
	if backup.noBackup {
		return nil
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
		return err
	}
	newTips, err := r.readTaskHeadsPartial(ctx, config, newHeads)
	if err != nil {
		return err
	}
	oldChains, err := r.completeTaskChains(ctx, config, oldHeads)
	if err != nil {
		return err
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
	for index, lease := range leases {
		if oldTips[index].Err != nil {
			return wrapKeepingCategory("cannot read the copy of task "+lease.TaskID+" that "+backup.remote+" holds, to back it up", oldTips[index].Err)
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
		current, err := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", prefix+lease.TaskID)
		if err != nil || strings.TrimSpace(string(current)) != lease.ExpectedRemote {
			return errReplacedTipMoved
		}
		manifest.Tasks = append(manifest.Tasks, task)
	}
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
	if err := r.writeArchiveBundle(ctx, config, path, manifest, prefix); err != nil {
		var moved *ArchiveTasksMovedError
		if errors.As(err, &moved) {
			return errReplacedTipMoved
		}
		return err
	}
	backup.bundles = append(backup.bundles, path)
	return nil
}
