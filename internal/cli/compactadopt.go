package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// compactAdoptRequest is what `compact --adopt` was asked, once runCompact has
// refused every flag it cannot be combined with.
type compactAdoptRequest struct {
	target       string
	discardLocal bool
	noBackup     bool
	backup       string
	flags        []string
	jsonMode     bool
}

// compactAdoptResult is the data member of `compact --adopt --json`.
//
// Status is one of gitstore's per-task statuses: adopted and adopted-replayed
// when origin's checkpoint was taken (by this command's fetch or its retry),
// reconciled or conflicted when origin's changes replayed onto this clone's
// checkpoint, and discarded-local for --discard-local. Head is the tip the
// task's ref holds afterwards. Bundle is the local history's backup, written
// only by --discard-local without --no-backup.
type compactAdoptResult struct {
	TaskID string              `json:"taskId"`
	Status gitstore.SyncStatus `json:"status"`
	Detail string              `json:"detail,omitempty"`
	Head   string              `json:"head"`
	Bundle string              `json:"bundle,omitempty"`
}

// runCompactAdopt settles one task a compaction left stuck. It always fetches
// inline first, whatever the auto-sync setting and whether a watcher runs:
// what it settles against is origin's current tip, and the fetch is also the
// first retry, since fetch attempts the same adoption every time it runs.
func runCompactAdopt(ctx context.Context, request compactAdoptRequest, cwd string, stdout, stderr io.Writer) error {
	session, err := openTaskSession(ctx, cwd, false, true, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	session.deferred = false
	session.report.Enabled = true
	session.report.Detail = ""
	if !session.repository.HasTasksRemote(ctx) {
		return core.Errorf(core.CategoryValidation, "compact --adopt needs an %s remote to fetch from", session.repository.TasksRemoteName())
	}
	if err := session.fetchBefore(ctx); err != nil {
		return err
	}
	if session.report.Status == syncStatusFailed {
		return core.Errorf(core.CategoryOperational, "compact --adopt needs origin's current tip, but %s", session.report.Detail)
	}
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}
	taskID, err := session.resolveTaskID(ctx, request.target)
	if err != nil {
		return err
	}

	result := compactAdoptResult{TaskID: taskID}
	var warnings []core.Warning
	if adopted, found := fetchedAdoption(session.fetched, taskID); found {
		// The fetch's own attempt took origin's checkpoint.
		result.Status, result.Detail = adopted.Status, adopted.Detail
	} else {
		adoption, err := adoptStuckTask(ctx, session, taskID, request)
		if err != nil {
			return err
		}
		result.Status, result.Detail, result.Bundle = adoption.Result.Status, adoption.Result.Detail, adoption.Bundle
		switch {
		case adoption.Result.Status == gitstore.SyncDiscardedLocal:
			if warning, failed := settleDiscardProjection(ctx, session, adoption); failed {
				warnings = append(warnings, warning)
			}
		case adoption.Result.Status.LeavesPreviousHistory():
			// The retry took origin's checkpoint, which the projection proves
			// against the old history while it is still here.
			if _, err := session.store.Get(ctx, session.config, taskID); err != nil {
				warnings = append(warnings, unsettledWarning(err))
			}
		}
	}
	warnings = append(warnings, session.unsettledWarnings()...)
	if head, found := taskRefHead(ctx, session, taskID); found {
		result.Head = head
	}
	// A discard leaves the ref on exactly the tip origin holds, so there is
	// nothing to publish; anything else replayed changes origin lacks.
	if result.Status != gitstore.SyncDiscardedLocal {
		session.publish(ctx, taskID)
	}
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the task was adopted locally, but " + session.report.Detail,
		})
	}
	session.compactedOnOrigin = withoutTask(session.compactedOnOrigin, taskID)
	if warning, found := session.compactedOnOriginWarning(""); found {
		warnings = append(warnings, warning)
	}
	conflicts := conflictsWithoutTask(session.conflicts, taskID)

	if request.jsonMode {
		writeSyncedResult(stdout, "compact", result, &session.report, conflicts, warnings)
		return nil
	}
	fmt.Fprintln(stdout, compactAdoptLine(result))
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
	return nil
}

// adoptStuckTask runs the retry, or the discard with its bundle, through
// AdoptCompaction.
func adoptStuckTask(
	ctx context.Context,
	session *taskSession,
	taskID string,
	request compactAdoptRequest,
) (gitstore.CompactionAdoption, error) {
	options := gitstore.AdoptOptions{DiscardLocal: request.discardLocal, Flags: request.flags, Now: compactionNow()}
	if request.discardLocal {
		runID, err := session.service.IDs.New()
		if err != nil {
			return gitstore.CompactionAdoption{}, err
		}
		options.RunID = runID
		if !request.noBackup {
			backup := request.backup
			if backup == "-" {
				// As for compact: typed as a path, "-" is a file of that name.
				backup = "./-"
			}
			path, err := gitstore.ResolveArchivePath(session.repository.CommonGitDir, runID+"-"+taskID+"-local", backup)
			if err != nil {
				return gitstore.CompactionAdoption{}, err
			}
			if path, err = filepath.Abs(path); err != nil {
				return gitstore.CompactionAdoption{}, core.Wrap(core.CategoryOperational, "cannot resolve backup path", err)
			}
			options.BundlePath = path
		}
		// The move a discard makes is one the projection's guards would
		// call a rewrite, so the row is dropped before it, as well as after:
		// a run that stops in between leaves a cache that reads the task
		// afresh rather than one that needs a rebuild.
		if snapshot, err := session.store.Get(ctx, session.config, taskID); err == nil {
			_ = session.store.Invalidate(ctx, session.config, taskID, snapshot.Head, "")
		}
	}
	return session.repository.AdoptCompaction(ctx, session.config, taskID, options)
}

// settleDiscardProjection moves the projection onto the tip a discard took,
// which its own refresh would refuse: dropping the row and reading the task
// afresh is the one way to take a ref that moved to a history it cannot prove
// holds the old one. A failure is a warning, since the refs are right and the
// cache can be rebuilt from them.
func settleDiscardProjection(ctx context.Context, session *taskSession, adoption gitstore.CompactionAdoption) (core.Warning, bool) {
	taskID := adoption.Result.TaskID
	err := session.store.Invalidate(ctx, session.config, taskID, adoption.Previous, adoption.Head)
	if err == nil {
		_, err = session.store.Get(ctx, session.config, taskID)
	}
	if err == nil {
		return core.Warning{}, false
	}
	return core.Warning{
		Code: core.WarningProjectionUpdate,
		Message: "origin's version was taken, but the SQLite cache could not be updated; run " +
			"`workbook rebuild`: " + err.Error(),
	}, true
}

// fetchedAdoption reports the fetch's outcome for taskID when that fetch
// already took origin's checkpoint.
func fetchedAdoption(fetched *gitstore.SyncResult, taskID string) (gitstore.SyncTaskResult, bool) {
	if fetched == nil {
		return gitstore.SyncTaskResult{}, false
	}
	for _, item := range fetched.Tasks {
		if item.TaskID == taskID && (item.Status == gitstore.SyncAdopted || item.Status == gitstore.SyncAdoptedReplayed) {
			return item, true
		}
	}
	return gitstore.SyncTaskResult{}, false
}

func taskRefHead(ctx context.Context, session *taskSession, taskID string) (string, bool) {
	head, found, err := session.repository.InspectTaskHead(ctx, session.config, taskID)
	if err != nil || !found {
		return "", false
	}
	return head.ObjectID, true
}

func compactAdoptLine(result compactAdoptResult) string {
	switch result.Status {
	case gitstore.SyncDiscardedLocal:
		if result.Bundle == "" {
			return fmt.Sprintf("Took origin's version of %s; the local history was discarded without a backup.", result.TaskID)
		}
		return fmt.Sprintf("Took origin's version of %s; the local history is saved in %s.", result.TaskID, result.Bundle)
	case gitstore.SyncAdopted:
		return fmt.Sprintf("Adopted origin's compaction of %s.", result.TaskID)
	case gitstore.SyncAdoptedReplayed:
		return fmt.Sprintf("Adopted origin's compaction of %s; %s.", result.TaskID, result.Detail)
	default:
		line := fmt.Sprintf("Settled %s: %s", result.TaskID, result.Status)
		if result.Detail != "" {
			line += "; " + result.Detail
		}
		return line + "."
	}
}

func withoutTask(taskIDs []string, taskID string) []string {
	kept := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		if id != taskID {
			kept = append(kept, id)
		}
	}
	return kept
}

func conflictsWithoutTask(conflicts []core.Conflict, taskID string) []core.Conflict {
	var kept []core.Conflict
	for _, conflict := range conflicts {
		if conflict.TaskID != taskID {
			kept = append(kept, conflict)
		}
	}
	return kept
}
