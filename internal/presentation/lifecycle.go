package presentation

import (
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// LifecycleStop is one status a task stood in, in the order its operation chain
// records. Attribution names the change that entered the status and is absent
// for a stop no recorded change entered.
type LifecycleStop struct {
	Status   core.Status
	Label    string
	Commit   string
	Actor    string
	WallTime *time.Time
	Current  bool
}

// Lifecycle derives a task's status lane from its change log. Status is the
// most common change by a wide margin, and a lane reading ready to in-progress
// to in-review to done communicates a task's life in a way a chronological list
// cannot, so status is rendered as a lane rather than as another flat row type.
//
// The lane follows the parent chain the log is ordered by, and carries wall
// times as attribution only, exactly as the change rows treat them: after a
// reconciliation those timestamps legitimately read out of order, and that
// disagreement is shown rather than sorted away.
//
// No operation records the status a task was created in, because a create pack
// carries the whole task rather than a field change. The earliest status the
// log can name is therefore the "from" side of the first status change, and a
// task whose status never changed leaves its current status as the only stop
// the lane can honestly show.
//
// Labels come from the project's own vocabulary, but the statuses themselves
// are left exactly as the log records them. A lane is history: a stop under a
// name the project has since renamed happened under that name, and forwarding it
// to the new one would rewrite what the reader is being shown. The label for a
// name the project no longer defines is that name.
func Lifecycle(log core.ChangeLog, current core.Status, vocabulary core.Vocabulary) []LifecycleStop {
	vocabulary = boardVocabulary(vocabulary)
	statusLabel := vocabulary.Label
	stops := make([]LifecycleStop, 0, 4)
	for _, change := range log.Changes {
		for _, field := range change.Fields {
			if field.Field != "status" || field.Kind != core.ChangeSet {
				continue
			}
			if len(stops) == 0 && field.From != "" {
				stops = append(stops, openingStop(log, core.Status(field.From), statusLabel))
			}
			wallTime := change.WallTime
			stops = append(stops, LifecycleStop{
				Status:   core.Status(field.To),
				Label:    statusLabel(core.Status(field.To)),
				Commit:   change.Commit,
				Actor:    change.Actor,
				WallTime: &wallTime,
			})
		}
	}
	// A complete chain ends where the task stands now. A truncated read or an
	// empty log does not, and the lane says so with an unattributed stop rather
	// than implying a change nobody recorded.
	if len(stops) == 0 || stops[len(stops)-1].Status != current {
		stops = append(stops, LifecycleStop{Status: current, Label: statusLabel(current)})
	}
	stops[len(stops)-1].Current = true
	return stops
}

// openingStop attributes the status a task was created in to the change that
// created it, and leaves the stop unattributed when the log does not reach back
// that far.
func openingStop(log core.ChangeLog, status core.Status, statusLabel func(core.Status) string) LifecycleStop {
	stop := LifecycleStop{Status: status, Label: statusLabel(status)}
	if len(log.Changes) == 0 || !createsTask(log.Changes[0]) {
		return stop
	}
	first := log.Changes[0]
	wallTime := first.WallTime
	stop.Commit, stop.Actor, stop.WallTime = first.Commit, first.Actor, &wallTime
	return stop
}

func createsTask(change core.Change) bool {
	for _, field := range change.Fields {
		if field.Kind == core.ChangeCreated {
			return true
		}
	}
	return false
}
