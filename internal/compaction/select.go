// Package compaction selects finished tasks and replaces each one's history
// with a single checkpoint: the engine behind `workbook compact`.
//
// It works on the local clone only. Fetching before a run and publishing after
// it are the command's job, so that a run can be tested, and dry-run, without a
// network.
package compaction

import (
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Kind is why a task is eligible: it is Done, or it is Deleted.
type Kind string

const (
	KindDone    Kind = "done"
	KindDeleted Kind = "deleted"
)

// Criteria chooses which finished tasks a run compacts.
type Criteria struct {
	// Done and Deleted pick the kinds. A Deleted task counts as Deleted
	// whatever its status, so with only Done it is never selected.
	Done, Deleted bool
	// Cutoff keeps only the tasks that became Done or Deleted more than this
	// long before Options.Now. Zero keeps every task of the selected kinds.
	Cutoff time.Duration
}

// BecameAt returns when the task entered its current unbroken stretch in
// done-tagged statuses, and when it was last deleted. Each is zero unless
// current, the task as it stands now, is done or deleted respectively.
//
// The stretch is read from history with today's vocabulary: an entry whose
// task is done-tagged after it and was not before it (or that created the
// task) starts it, an entry that leaves a done-tagged status ends it, and
// moving between two done-tagged statuses does neither. A checkpoint root
// stands in for the history it replaced with the times its compaction
// recorded. When no entry started the stretch, because a status became
// done-tagged through the configuration ledger rather than through a task
// operation, the task's most recent status change is used, and then its
// creation time. A deletion is the wall time of the last task.tombstone, with
// the same fallback to creation time. Wall times are display metadata
// everywhere else; here they are only ever compared against a cutoff, and a
// comment on a finished task does not move either answer.
func BecameAt(history core.TaskHistory, vocabulary core.Vocabulary, current core.TaskData) (doneAt, deletedAt time.Time) {
	isDone := func(status core.Status) bool {
		resolved, _ := vocabulary.Resolve(status)
		return vocabulary.IsDone(resolved)
	}
	steps, _ := core.ReplayHistory(history)
	var stretch, statusChanged, deleted time.Time
	for _, step := range steps {
		pack := step.Entry.Operation
		if summary, ok := pack.Checkpoint(); ok && step.Before == nil {
			stretch, deleted = time.Time{}, time.Time{}
			if summary.BecameDoneAt != nil && isDone(step.After.Status) {
				stretch = *summary.BecameDoneAt
			}
			if summary.BecameDeletedAt != nil && step.After.Deleted {
				deleted = *summary.BecameDeletedAt
			}
			continue
		}
		wall := pack.WallTime
		switch after := isDone(step.After.Status); {
		case !after:
			stretch = time.Time{}
		case step.Before == nil || !isDone(step.Before.Status):
			stretch = wall
		}
		if step.Before != nil && step.Before.Status != step.After.Status {
			statusChanged = wall
		}
		for _, operation := range pack.Operations {
			if operation.Type == core.OperationTaskTombstone {
				deleted = wall
			}
		}
	}

	if isDone(current.Status) {
		doneAt = firstSet(stretch, statusChanged, current.CreatedAt)
	}
	if current.Deleted {
		deletedAt = firstSet(deleted, current.CreatedAt)
	}
	return doneAt, deletedAt
}

func firstSet(times ...time.Time) time.Time {
	for _, candidate := range times {
		if !candidate.IsZero() {
			return candidate
		}
	}
	return time.Time{}
}
