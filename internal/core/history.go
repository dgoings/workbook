package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

// DefaultChangeLimit is the number of most recent changes a change log shows
// when the caller asks for no particular number.
const DefaultChangeLimit = 10

// HistoryEntry is one recorded operation pack together with the commit object
// that holds it. Commit is the durable address of the entry: reconciliation
// rewrites logical clocks and therefore object IDs, but a commit an earlier
// listing named usually stays reachable through a parked ref.
type HistoryEntry struct {
	Commit    string
	Parent    string
	Operation OperationPack
}

// HistoryTruncation names the commit a read could not get past. A truncated
// history is still worth showing, so readers return the valid prefix and this
// boundary rather than failing.
type HistoryTruncation struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
	// Category names why the read stopped, when the reason is something other
	// than corruption. It exists because a replay that stops at a pack written
	// by a newer Workbook is not a broken history, and every caller that turns
	// a truncation back into an error would otherwise have to say it was.
	//
	// Omitted when empty, which is the ordinary case, so a truncation reported
	// before this member existed reads exactly as it did.
	Category Category `json:"category,omitempty"`
}

// TaskHistory is one task's operation packs ordered by the parent chain, oldest
// first. The order is deliberately the chain's, not wall time's: replay
// preserves an operation's wall time while rewriting its logical clock, so
// after a reconciliation the two orders legitimately disagree.
type TaskHistory struct {
	Entries   []HistoryEntry
	Truncated *HistoryTruncation
}

// TaskHistorySource reads recorded operation packs for one task.
type TaskHistorySource interface {
	// TaskHistory returns the chain ending at the task's current head.
	TaskHistory(context.Context, ProjectConfig, string) (TaskHistory, error)
	// CommitHistory returns the chain ending at one named commit object, which
	// may be a pre-replay tip outside the canonical task ref.
	CommitHistory(context.Context, ProjectConfig, string, string) (TaskHistory, error)
}

// ChangeKind names how one field moved.
type ChangeKind string

const (
	// ChangeSet reports a scalar field replaced from one value with another.
	ChangeSet ChangeKind = "set"
	// ChangeReordered reports a rank change. A rank is opaque, so its literal
	// value tells a reader nothing and is deliberately omitted.
	ChangeReordered ChangeKind = "reordered"
	ChangeAdded     ChangeKind = "added"
	ChangeRemoved   ChangeKind = "removed"
	ChangeCreated   ChangeKind = "created"
	ChangeDeleted   ChangeKind = "deleted"
	ChangeRestored  ChangeKind = "restored"
)

// FieldChange renders one field in its own terms.
type FieldChange struct {
	Field string     `json:"field"`
	Kind  ChangeKind `json:"kind"`
	From  string     `json:"from,omitempty"`
	To    string     `json:"to,omitempty"`
	// Diff carries a word-level comparison for the description, the one field
	// whose old and new values are too large to read side by side.
	Diff []DiffSpan `json:"diff,omitempty"`
}

// Change is one operation pack rendered as one row. Entries are per pack rather
// than per operation because actor, wall time, and logical clock are all
// pack-level, and a pack touching several fields is one thing that happened.
type Change struct {
	Commit       string        `json:"commit"`
	Actor        string        `json:"actor"`
	WallTime     time.Time     `json:"wallTime"`
	LogicalClock uint64        `json:"logicalClock"`
	Summary      string        `json:"summary"`
	Fields       []FieldChange `json:"fields"`
}

// ChangeLog is the rendered history view: a window onto the chain plus enough
// context to say what the window left out.
type ChangeLog struct {
	Total     int                `json:"total"`
	Showing   int                `json:"showing"`
	Changes   []Change           `json:"changes"`
	Truncated *HistoryTruncation `json:"truncated,omitempty"`
}

// Comparison is a field-level diff between two points in one task's history,
// in the order the caller named them.
type Comparison struct {
	From   string        `json:"from"`
	To     string        `json:"to"`
	Fields []FieldChange `json:"fields"`
}

// ComparePoints names the two commits a comparison runs between. They are never
// sorted: operation ULIDs sort by authoring time and no longer track chain
// position once a task has been reconciled, so only the caller's order is
// meaningful.
type ComparePoints struct {
	From string
	To   string
}

// NewOperationPack assembles a durable operation pack from separately stored
// parts. The projection stores operations alone and rebuilds their pack, so the
// format and version constants live in one place.
func NewOperationPack(
	projectID, taskID, historyGeneration, actor string,
	logicalClock uint64,
	wallTime time.Time,
	operations []Operation,
) OperationPack {
	return OperationPack{
		Format:  operationPackFormat,
		Version: documentVersion,
		// The generation is derived from the operations rather than passed in,
		// so no caller can forget it and none can overstate it. A build that
		// ships a new operation type declares its requirement once, in
		// operationMinReader, and every pack that carries the type gets the
		// marker here.
		MinReader:         PackMinReader(operations),
		ProjectID:         projectID,
		TaskID:            taskID,
		HistoryGeneration: historyGeneration,
		Actor:             Actor{ID: actor},
		LogicalClock:      logicalClock,
		WallTime:          wallTime,
		Operations:        operations,
	}
}

// ReplayStep is one entry together with the state on either side of it.
type ReplayStep struct {
	Entry  HistoryEntry
	Before *TaskData
	After  TaskData
}

// ReplayHistory applies every entry from the root. State is reconstructed
// rather than stored per row, so a chain that a reconciliation rewrote needs no
// checkpoint invalidation. An entry that cannot be applied truncates the replay
// softly: the valid prefix is returned with the boundary named.
func ReplayHistory(projectKey string, history TaskHistory) ([]ReplayStep, *HistoryTruncation) {
	steps := make([]ReplayStep, 0, len(history.Entries))
	var parent *StateDocument
	for _, entry := range history.Entries {
		state, err := Apply(parent, entry.Operation, projectKey)
		if err != nil {
			truncation := &HistoryTruncation{
				Commit:  entry.Commit,
				Message: "cannot replay this operation: " + err.Error(),
			}
			// A stop at a pack written by a newer Workbook keeps its category,
			// so a caller that has to turn this boundary back into an error
			// does not have to call a sound history broken.
			if category := CategoryOf(err); category == CategoryNewerWriter {
				truncation.Category = category
			}
			return steps, truncation
		}
		step := ReplayStep{Entry: entry, After: state.Task}
		if parent != nil {
			before := parent.Task
			step.Before = &before
		}
		steps = append(steps, step)
		next := state
		parent = &next
	}
	return steps, history.Truncated
}

// StateAt replays a whole chain and returns the task as it stood at its end.
func StateAt(projectKey string, history TaskHistory) (TaskData, error) {
	steps, truncation := ReplayHistory(projectKey, history)
	if truncation != nil {
		category := truncation.Category
		if category == "" {
			category = CategoryCorruptData
		}
		return TaskData{}, Errorf(
			category,
			"cannot reconstruct task state at commit %s: %s",
			truncation.Commit,
			truncation.Message,
		)
	}
	if len(steps) == 0 {
		return TaskData{}, Errorf(CategoryCorruptData, "task history contains no operations")
	}
	return steps[len(steps)-1].After, nil
}

// BuildChangeLog renders a task's chain, keeping the most recent changes.
// Ordering follows the parent chain and wall times are printed as attribution
// only, so timestamps that read out of order after a reconciliation are shown
// as they are rather than reordered.
func BuildChangeLog(projectKey string, history TaskHistory, limit int, all bool) ChangeLog {
	steps, truncation := ReplayHistory(projectKey, history)
	changes := make([]Change, 0, len(steps))
	for _, step := range steps {
		changes = append(changes, describeStep(step))
	}
	log := ChangeLog{Total: len(changes), Truncated: truncation}
	if !all {
		if limit <= 0 {
			limit = DefaultChangeLimit
		}
		if len(changes) > limit {
			changes = changes[len(changes)-limit:]
		}
	}
	log.Changes = changes
	log.Showing = len(changes)
	return log
}

func describeStep(step ReplayStep) Change {
	pack := step.Entry.Operation
	change := Change{
		Commit:       step.Entry.Commit,
		Actor:        pack.Actor.ID,
		WallTime:     pack.WallTime,
		LogicalClock: pack.LogicalClock,
	}
	before := TaskData{}
	if step.Before != nil {
		before = *step.Before
	}
	for _, operation := range pack.Operations {
		change.Fields = append(change.Fields, describeOperation(operation, before, step.After)...)
	}
	sortFieldChanges(change.Fields)
	change.Summary = summarizeFields(change.Fields)
	return change
}

func describeOperation(operation Operation, before, after TaskData) []FieldChange {
	switch operation.Type {
	case OperationTaskCreate:
		return []FieldChange{{Field: "task", Kind: ChangeCreated, To: after.Title}}
	case OperationTaskTombstone:
		return []FieldChange{{Field: "task", Kind: ChangeDeleted}}
	case OperationTaskRestore:
		return []FieldChange{{Field: "task", Kind: ChangeRestored}}
	case OperationSetAdd:
		return []FieldChange{{Field: operation.Field, Kind: ChangeAdded, To: operation.Value}}
	case OperationSetRemove:
		return []FieldChange{{Field: operation.Field, Kind: ChangeRemoved, From: operation.Value}}
	case OperationAssignAdd, OperationAssignRemove:
		return describeAssignment(operation, before, after)
	case OperationFieldSet:
		return []FieldChange{scalarChange(operation.Field, fieldValue(before, operation.Field), operation.Value)}
	default:
		return nil
	}
}

// describeAssignment renders an assignment operation by what it did, not by
// what it asked for.
//
// Every other operation type is unconditional: a set.add adds. An assignment
// operation is not, and the change log has to say so. An add of an assignment
// the task already carried changes nothing, and a removal by somebody the
// removal rule does not entitle changes nothing — that is the fold's whole
// point, and a row reading "removed dylan@example.com" beside a task that is
// still assigned to dylan@example.com would be the log lying about the one
// thing this design most needs to be legible.
//
// So the operation is compared against the states on either side of its pack,
// and a no-op contributes no field change at all. The pack still appears in the
// log, attributed and timestamped, summarized as having recorded no visible
// change — which is exactly what happened.
//
// What is compared is the whole record, not merely whether the value is there.
// A pack that removes an assignment and re-adds it — which nothing this build's
// boundary writes, but which a composed pack could — leaves the same value in
// place while replacing its creator and its creation time. The creation time is
// the clock a staleness display is computed from, so a log that called that
// "nothing" would quietly reset the one number somebody was reading. Comparing
// records reports it honestly, as a removal and an addition, which is what the
// two operations did.
func describeAssignment(operation Operation, before, after TaskData) []FieldChange {
	principal, label, err := SplitAssignmentValue(operation.Value)
	if err != nil {
		return nil
	}
	beforeIndex, heldBefore := findAssignment(before.Assignments, principal, label)
	afterIndex, heldAfter := findAssignment(after.Assignments, principal, label)
	unchanged := heldBefore && heldAfter &&
		sameAssignmentRecord(before.Assignments[beforeIndex], after.Assignments[afterIndex])
	switch {
	case operation.Type == OperationAssignAdd && heldAfter && !unchanged:
		return []FieldChange{{Field: "assignments", Kind: ChangeAdded, To: operation.Value}}
	case operation.Type == OperationAssignRemove && heldBefore && !unchanged:
		return []FieldChange{{Field: "assignments", Kind: ChangeRemoved, From: operation.Value}}
	default:
		return nil
	}
}

// scalarChange renders one scalar field the way that field reads. Title,
// status, and priority read as old to new; a rank reads as a reordering
// because its literal value is opaque; a description needs a real diff.
func scalarChange(field, from, to string) FieldChange {
	switch field {
	case "rank":
		return FieldChange{Field: field, Kind: ChangeReordered}
	case "description":
		return FieldChange{Field: field, Kind: ChangeSet, From: from, To: to, Diff: WordDiff(from, to)}
	default:
		return FieldChange{Field: field, Kind: ChangeSet, From: from, To: to}
	}
}

func fieldValue(task TaskData, field string) string {
	switch field {
	case "title":
		return task.Title
	case "description":
		return task.Description
	case "status":
		return string(task.Status)
	case "priority":
		return string(task.Priority)
	case "rank":
		return task.Rank
	default:
		return ""
	}
}

// CompareTasks reports every field-level difference between two reconstructed
// task states, in the order the caller supplied them.
func CompareTasks(from, to TaskData) []FieldChange {
	fields := make([]FieldChange, 0, 8)
	if from.Deleted != to.Deleted {
		kind := ChangeRestored
		if to.Deleted {
			kind = ChangeDeleted
		}
		fields = append(fields, FieldChange{Field: "task", Kind: kind})
	}
	for _, scalar := range []struct{ field, from, to string }{
		{"title", from.Title, to.Title},
		{"status", string(from.Status), string(to.Status)},
		{"priority", string(from.Priority), string(to.Priority)},
		{"description", from.Description, to.Description},
		{"rank", from.Rank, to.Rank},
	} {
		if scalar.from != scalar.to {
			fields = append(fields, scalarChange(scalar.field, scalar.from, scalar.to))
		}
	}
	for _, collection := range []struct {
		field    string
		from, to []string
	}{
		{field: "labels", from: from.Labels, to: to.Labels},
		{field: "dependencies", from: from.Dependencies, to: to.Dependencies},
	} {
		for _, removed := range setDifference(collection.from, collection.to) {
			fields = append(fields, FieldChange{Field: collection.field, Kind: ChangeRemoved, From: removed})
		}
		for _, added := range setDifference(collection.to, collection.from) {
			fields = append(fields, FieldChange{Field: collection.field, Kind: ChangeAdded, To: added})
		}
	}
	// Assignments compare by their values, which is their identity. Creator and
	// creation time are attribution and cannot differ between two states that
	// hold the same assignment, because the fold never rewrites either.
	for _, removed := range setDifference(assignmentValues(from.Assignments), assignmentValues(to.Assignments)) {
		fields = append(fields, FieldChange{Field: "assignments", Kind: ChangeRemoved, From: removed})
	}
	for _, added := range setDifference(assignmentValues(to.Assignments), assignmentValues(from.Assignments)) {
		fields = append(fields, FieldChange{Field: "assignments", Kind: ChangeAdded, To: added})
	}
	sortFieldChanges(fields)
	return fields
}

func assignmentValues(assignments []Assignment) []string {
	values := make([]string, len(assignments))
	for index, assignment := range assignments {
		values[index] = assignment.Value()
	}
	return values
}

// fieldDisplayOrder keeps a pack's fields in one reading order whatever order
// the operations happen to be recorded in.
var fieldDisplayOrder = map[string]int{
	"task": 0, "title": 1, "status": 2, "priority": 3,
	"description": 4, "rank": 5, "labels": 6, "dependencies": 7,
	"assignments": 8,
}

func sortFieldChanges(fields []FieldChange) {
	sort.SliceStable(fields, func(i, j int) bool {
		left, leftKnown := fieldDisplayOrder[fields[i].Field]
		right, rightKnown := fieldDisplayOrder[fields[j].Field]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if left != right {
			return left < right
		}
		return false
	})
}

// FieldLabel returns the display label for a projected field name.
func FieldLabel(field string) string {
	switch field {
	case "task":
		return "Task"
	case "title":
		return "Title"
	case "status":
		return "Status"
	case "priority":
		return "Priority"
	case "description":
		return "Description"
	case "rank":
		return "Rank"
	case "labels":
		return "Labels"
	case "dependencies":
		return "Dependencies"
	case "assignments":
		return "Assignments"
	default:
		return field
	}
}

func summarizeFields(fields []FieldChange) string {
	for _, change := range fields {
		switch change.Kind {
		case ChangeCreated:
			return "created the task"
		case ChangeDeleted:
			return "deleted the task"
		case ChangeRestored:
			return "restored the task"
		}
	}
	names := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, change := range fields {
		if _, duplicate := seen[change.Field]; duplicate {
			continue
		}
		seen[change.Field] = struct{}{}
		names = append(names, change.Field)
	}
	if len(names) == 0 {
		return "recorded no visible change"
	}
	if len(names) == 1 && names[0] == "rank" {
		return "reordered the task"
	}
	return "changed " + joinNames(names)
}

func joinNames(values []string) string {
	switch len(values) {
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", and " + values[len(values)-1]
	}
}
