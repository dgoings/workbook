package core

import (
	"fmt"
	"testing"
	"time"
)

const (
	historyProjectID  = "01K0M65GBZ8F5ZQX0VC1J8H3TP"
	historyTaskID     = "WB-01K0M6B8A4FTT8C39MXXYTW7C1"
	historyGeneration = "01K0M6B8A4FTT8C39MXXYTW7C2"
	historyProjectKey = "WB"
)

var historyOrigin = time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)

func TestBuildChangeLogRendersEachFieldInItsOwnTerms(t *testing.T) {
	// Mutation caught: printing a rank's literal value, or reporting a
	// description change as an old-to-new pair a reader cannot compare.
	history := historyOf(t,
		createOperation("Original title", "First description here."),
		[]Operation{{Type: OperationFieldSet, Field: "status", Value: string(StatusReady)}},
		[]Operation{{Type: OperationFieldSet, Field: "rank", Value: "3/2"}},
		[]Operation{{Type: OperationFieldSet, Field: "description", Value: "First description, revised."}},
		[]Operation{
			{Type: OperationFieldSet, Field: "title", Value: "Renamed title"},
			{Type: OperationFieldSet, Field: "priority", Value: string(PriorityHigh)},
		},
	)

	log := BuildChangeLog(history, 0, true)
	if log.Total != 5 || log.Showing != 5 {
		t.Fatalf("log window = %d of %d, want all five changes", log.Showing, log.Total)
	}

	summaries := make([]string, len(log.Changes))
	for index, change := range log.Changes {
		summaries[index] = change.Summary
	}
	want := []string{
		"created the task",
		"changed status",
		"reordered the task",
		"changed description",
		"changed title and priority",
	}
	for index := range want {
		if summaries[index] != want[index] {
			t.Fatalf("summaries = %#v, want %#v", summaries, want)
		}
	}

	status := log.Changes[1].Fields[0]
	if status.Kind != ChangeSet || status.From != string(StatusBacklog) || status.To != string(StatusReady) {
		t.Fatalf("status change = %#v, want backlog to ready", status)
	}
	rank := log.Changes[2].Fields[0]
	if rank.Kind != ChangeReordered || rank.From != "" || rank.To != "" {
		t.Fatalf("rank change = %#v, want a reordering with no opaque values", rank)
	}
	description := log.Changes[3].Fields[0]
	if description.Kind != ChangeSet || len(description.Diff) == 0 {
		t.Fatalf("description change = %#v, want word-level spans", description)
	}
}

func TestBuildChangeLogKeepsTheChainOrderWhenWallTimesDisagree(t *testing.T) {
	// Mutation caught: sorting entries by wall time, which erases the visible
	// fingerprint replayed work leaves behind.
	history := historyOf(t,
		createOperation("Shared task", ""),
		[]Operation{{Type: OperationFieldSet, Field: "status", Value: string(StatusReady)}},
		[]Operation{{Type: OperationFieldSet, Field: "title", Value: "Renamed by the replay"}},
	)
	// A replayed pack keeps the wall time its author recorded while its logical
	// clock is rewritten, so the newest chain position can be the oldest clock.
	history.Entries[2].Operation.WallTime = historyOrigin.Add(-time.Hour)

	log := BuildChangeLog(history, 0, true)
	if got, want := log.Changes[2].Summary, "changed title"; got != want {
		t.Fatalf("last change = %q, want %q at the end of the chain", got, want)
	}
	if !log.Changes[2].WallTime.Before(log.Changes[1].WallTime) {
		t.Fatal("test fixture no longer reproduces out-of-order wall times")
	}
	for index := 1; index < len(log.Changes); index++ {
		if log.Changes[index].LogicalClock != log.Changes[index-1].LogicalClock+1 {
			t.Fatalf("logical clocks = %#v, want the chain order", log.Changes)
		}
	}
}

func TestBuildChangeLogWindowsTheMostRecentChanges(t *testing.T) {
	// Mutation caught: silently truncating without reporting the total, or
	// keeping the oldest entries instead of the most recent ones.
	packs := [][]Operation{createOperation("Original title", "")}
	for index := range 20 {
		packs = append(packs, []Operation{
			{Type: OperationFieldSet, Field: "title", Value: fmt.Sprintf("Title %d", index)},
		})
	}
	history := historyOf(t, packs...)

	defaulted := BuildChangeLog(history, 0, false)
	if defaulted.Showing != DefaultChangeLimit || defaulted.Total != 21 {
		t.Fatalf("default window = %d of %d, want %d of 21", defaulted.Showing, defaulted.Total, DefaultChangeLimit)
	}
	if got, want := defaulted.Changes[defaulted.Showing-1].Fields[0].To, "Title 19"; got != want {
		t.Fatalf("newest windowed change = %q, want %q", got, want)
	}

	limited := BuildChangeLog(history, 3, false)
	if limited.Showing != 3 || limited.Total != 21 {
		t.Fatalf("limited window = %d of %d, want 3 of 21", limited.Showing, limited.Total)
	}

	all := BuildChangeLog(history, 3, true)
	if all.Showing != 21 || all.Total != 21 {
		t.Fatalf("unlimited window = %d of %d, want 21 of 21", all.Showing, all.Total)
	}
}

func TestBuildChangeLogTruncatesSoftlyWhenAnOperationCannotBeApplied(t *testing.T) {
	// Mutation caught: failing the whole view instead of showing the valid
	// prefix and naming where it stopped.
	history := historyOf(t,
		createOperation("Original title", ""),
		[]Operation{{Type: OperationFieldSet, Field: "status", Value: string(StatusReady)}},
		[]Operation{{Type: OperationFieldSet, Field: "title", Value: "Never applied"}},
	)
	history.Entries[2].Operation.LogicalClock = 99

	log := BuildChangeLog(history, 0, true)
	if log.Showing != 2 || log.Total != 2 {
		t.Fatalf("log = %d of %d, want the two-entry valid prefix", log.Showing, log.Total)
	}
	if log.Truncated == nil || log.Truncated.Commit != history.Entries[2].Commit {
		t.Fatalf("truncation = %#v, want the boundary commit named", log.Truncated)
	}
}

func TestCompareTasksReportsEveryFieldDifference(t *testing.T) {
	// Mutation caught: comparing only scalars, or losing collection membership.
	from := TaskData{
		Title: "Before", Description: "One two three", Status: StatusBacklog, Priority: PriorityLow,
		Rank: "1/1", Labels: []string{"kept", "dropped"}, Dependencies: []string{},
	}
	to := TaskData{
		Title: "After", Description: "One four three", Status: StatusDone, Priority: PriorityHigh,
		Rank: "3/2", Labels: []string{"kept", "added"}, Dependencies: []string{historyTaskID},
	}

	fields := CompareTasks(from, to)
	got := make(map[string]FieldChange, len(fields))
	for _, change := range fields {
		got[change.Field+":"+string(change.Kind)+":"+change.From+change.To] = change
	}
	for _, key := range []string{
		"title:set:BeforeAfter",
		"status:set:backlogdone",
		"priority:set:lowhigh",
		"rank:reordered:",
		"labels:removed:dropped",
		"labels:added:added",
		"dependencies:added:" + historyTaskID,
	} {
		if _, found := got[key]; !found {
			t.Fatalf("comparison = %#v, want a %q entry", fields, key)
		}
	}
	description, found := got["description:set:One two threeOne four three"]
	if !found || len(description.Diff) == 0 {
		t.Fatalf("comparison = %#v, want a description entry carrying word spans", fields)
	}
	if _, unexpected := got["labels:added:kept"]; unexpected {
		t.Fatalf("comparison = %#v, want unchanged labels omitted", fields)
	}
}

func TestCompareTasksReportsNothingForIdenticalStates(t *testing.T) {
	// Mutation caught: reporting a difference between a state and itself.
	task := TaskData{
		Title: "Same", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1",
		Labels: []string{"one"}, Dependencies: []string{},
	}
	if fields := CompareTasks(task, task); len(fields) != 0 {
		t.Fatalf("comparison = %#v, want no differences", fields)
	}
}

func createOperation(title, description string) []Operation {
	task := TaskData{
		Title: title, Description: description, Status: StatusBacklog, Priority: PriorityMedium,
		Labels: []string{}, Rank: "1/1", Dependencies: []string{},
		CreatedAt: historyOrigin, UpdatedAt: historyOrigin,
	}
	return []Operation{{Type: OperationTaskCreate, Task: &task}}
}

// historyOf builds a chain whose commits, operation IDs, and clocks follow the
// parent order, so a test can name a position without hard-coding an object ID.
func historyOf(t *testing.T, packs ...[]Operation) TaskHistory {
	t.Helper()
	history := TaskHistory{Entries: make([]HistoryEntry, 0, len(packs))}
	for index, operations := range packs {
		for operation := range operations {
			operations[operation].ID = historyULID(index*10 + operation)
		}
		parent := ""
		if index > 0 {
			parent = history.Entries[index-1].Commit
		}
		history.Entries = append(history.Entries, HistoryEntry{
			Commit: fmt.Sprintf("%040x", index+1),
			Parent: parent,
			Operation: NewOperationPack(
				historyProjectID, historyTaskID, historyGeneration, "tester@example.test",
				uint64(index+1), historyOrigin.Add(time.Duration(index)*time.Minute), operations,
			),
		})
	}
	return history
}

func historyULID(sequence int) string {
	return fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05X", sequence)
}

func TestDescribeStepOfAMergedPack(t *testing.T) {
	// Mutation caught: describing a merged pack by the local edit, whose text is
	// not the one the task holds, or by one row per description operation.
	base, theirs, ours := "one\ntwo\nthree\n", "one\nTWO\nthree\n", "one\ntwo\nTHREE\n"
	merged := "one\nTWO\nTHREE\n"
	mergeOf := func(other, value, ancestor, current string) Operation {
		return Operation{Type: OperationDescriptionMerge, Value: value, Merge: &DescriptionMerge{
			Other: other, Ancestor: ancestor, Current: current, Method: DescriptionLineMergeMethod,
		}}
	}
	// historyOf numbers pack 2's operations from 20.
	setID, mergeID, secondID := historyULID(20), historyULID(21), historyULID(22)

	history := historyOf(t,
		createOperation("Merged", base),
		[]Operation{{Type: OperationFieldSet, Field: "description", Value: theirs}},
		[]Operation{
			{Type: OperationFieldSet, Field: "description", Value: ours},
			mergeOf(setID, merged, base, theirs),
		},
	)
	change := BuildChangeLog(history, 0, true).Changes[2]
	if len(change.Fields) != 1 || change.Summary != "changed description" {
		t.Fatalf("change = %q %#v, want one description change", change.Summary, change.Fields)
	}
	field := change.Fields[0]
	if field.Field != "description" || field.Kind != ChangeSet || field.From != theirs || field.To != merged || len(field.Diff) == 0 {
		t.Fatalf("field = %#v, want the description set from the text before the pack to the merged one", field)
	}
	want := FieldMerge{Operation: mergeID, Method: DescriptionLineMergeMethod, Ancestor: base, Other: ours, Current: theirs}
	if field.Merge == nil || *field.Merge != want {
		t.Fatalf("merge = %#v, want %#v", field.Merge, want)
	}

	// Replayed again before it was published, the pack holds a second merge,
	// which names the first. The row is described by the last one.
	newer := "ONE\ntwo\nthree\n"
	final := "ONE\nTWO\nTHREE\n"
	history = historyOf(t,
		createOperation("Merged", base),
		[]Operation{{Type: OperationFieldSet, Field: "description", Value: newer}},
		[]Operation{
			{Type: OperationFieldSet, Field: "description", Value: ours},
			mergeOf(setID, merged, base, theirs),
			mergeOf(mergeID, final, theirs, newer),
			{Type: OperationFieldSet, Field: "title", Value: "Renamed"},
		},
	)
	change = BuildChangeLog(history, 0, true).Changes[2]
	if len(change.Fields) != 2 || change.Fields[0].Field != "title" || change.Fields[0].Merge != nil {
		t.Fatalf("fields = %#v, want the title and one description change", change.Fields)
	}
	field = change.Fields[1]
	want = FieldMerge{Operation: secondID, Method: DescriptionLineMergeMethod, Ancestor: theirs, Other: merged, Current: newer}
	if field.Field != "description" || field.From != newer || field.To != final || field.Merge == nil || *field.Merge != want {
		t.Fatalf("field = %#v %#v, want the second merge", field, field.Merge)
	}
}
