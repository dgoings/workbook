package presentation

import (
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestLifecycleReadsTheChainFromCreationToTheCurrentStatus(t *testing.T) {
	created := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	log := core.ChangeLog{
		Total:   4,
		Showing: 4,
		Changes: []core.Change{
			{
				Commit: "aaa", Actor: "dylan", WallTime: created,
				Fields: []core.FieldChange{{Field: "task", Kind: core.ChangeCreated, To: "Write it down"}},
			},
			{
				Commit: "bbb", Actor: "dylan", WallTime: created.Add(time.Hour),
				Fields: []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "backlog", To: "ready"}},
			},
			{
				Commit: "ccc", Actor: "agent", WallTime: created.Add(2 * time.Hour),
				Fields: []core.FieldChange{
					{Field: "title", Kind: core.ChangeSet, From: "Write it down", To: "Write it up"},
					{Field: "status", Kind: core.ChangeSet, From: "ready", To: "in-progress"},
				},
			},
			{
				Commit: "ddd", Actor: "agent", WallTime: created.Add(3 * time.Hour),
				Fields: []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "in-progress", To: "in-review"}},
			},
		},
	}

	stops := Lifecycle(log, core.StatusInReview, core.DefaultVocabulary())

	want := []struct {
		status core.Status
		label  string
		commit string
		actor  string
	}{
		{core.StatusBacklog, "Backlog", "aaa", "dylan"},
		{core.StatusReady, "Ready", "bbb", "dylan"},
		{core.StatusInProgress, "In Progress", "ccc", "agent"},
		{core.StatusInReview, "In Review", "ddd", "agent"},
	}
	if len(stops) != len(want) {
		t.Fatalf("lifecycle stops = %#v, want %d stops", stops, len(want))
	}
	for index, expected := range want {
		got := stops[index]
		if got.Status != expected.status || got.Label != expected.label ||
			got.Commit != expected.commit || got.Actor != expected.actor {
			t.Fatalf("stop %d = %#v, want %#v", index, got, expected)
		}
		if got.WallTime == nil {
			t.Fatalf("stop %d lost its attribution time", index)
		}
	}
	if !stops[len(stops)-1].Current {
		t.Fatal("the last stop is not marked current")
	}
	for _, stop := range stops[:len(stops)-1] {
		if stop.Current {
			t.Fatalf("stop %q is marked current before the end of the chain", stop.Status)
		}
	}
	if got, want := *stops[0].WallTime, created; !got.Equal(want) {
		t.Fatalf("opening stop time = %s, want the creating change's %s", got, want)
	}
}

func TestLifecycleShowsOneUnattributedStopWhenStatusNeverChanged(t *testing.T) {
	log := core.ChangeLog{
		Total:   1,
		Showing: 1,
		Changes: []core.Change{{
			Commit: "aaa", Actor: "dylan", WallTime: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
			Fields: []core.FieldChange{{Field: "task", Kind: core.ChangeCreated, To: "Write it down"}},
		}},
	}

	stops := Lifecycle(log, core.StatusBacklog, core.DefaultVocabulary())

	if len(stops) != 1 {
		t.Fatalf("lifecycle stops = %#v, want exactly one", stops)
	}
	// No operation records the status a task was created in, so the lane names
	// the current status without claiming a change entered it.
	if stops[0].Status != core.StatusBacklog || stops[0].Label != "Backlog" || !stops[0].Current {
		t.Fatalf("only stop = %#v, want the current backlog status", stops[0])
	}
	if stops[0].Commit != "" || stops[0].Actor != "" || stops[0].WallTime != nil {
		t.Fatalf("only stop = %#v, want no attribution", stops[0])
	}
}

func TestLifecycleClosesOnTheCurrentStatusWhenTheChainDoesNotReachIt(t *testing.T) {
	log := core.ChangeLog{
		Total:   1,
		Showing: 1,
		Changes: []core.Change{{
			Commit: "bbb", Actor: "dylan", WallTime: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
			Fields: []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "backlog", To: "ready"}},
		}},
		Truncated: &core.HistoryTruncation{Commit: "ccc", Message: "cannot read this operation"},
	}

	stops := Lifecycle(log, core.StatusDone, core.DefaultVocabulary())

	if len(stops) != 3 {
		t.Fatalf("lifecycle stops = %#v, want backlog, ready, and the current done", stops)
	}
	// The truncated read never names the change that reached done, so the lane
	// shows where the task stands without inventing attribution for it.
	if stops[0].Commit != "" || stops[0].WallTime != nil {
		t.Fatalf("opening stop = %#v, want no attribution when the log does not start at creation", stops[0])
	}
	last := stops[2]
	if last.Status != core.StatusDone || !last.Current || last.Commit != "" || last.WallTime != nil {
		t.Fatalf("closing stop = %#v, want an unattributed current done stop", last)
	}
}

func TestLifecycleKeepsAStatusThisBuildDoesNotKnow(t *testing.T) {
	log := core.ChangeLog{
		Total:   1,
		Showing: 1,
		Changes: []core.Change{{
			Commit: "bbb", Actor: "dylan", WallTime: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
			Fields: []core.FieldChange{{Field: "status", Kind: core.ChangeSet, From: "ready", To: "shipped"}},
		}},
	}

	stops := Lifecycle(log, core.Status("shipped"), core.DefaultVocabulary())

	if len(stops) != 2 {
		t.Fatalf("lifecycle stops = %#v, want two", stops)
	}
	if stops[1].Status != core.Status("shipped") || stops[1].Label != "shipped" {
		t.Fatalf("unknown status stop = %#v, want the raw value as its label", stops[1])
	}
}
