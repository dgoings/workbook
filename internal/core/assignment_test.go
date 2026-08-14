package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	dylan     = "dylan@example.com"
	teammate  = "sam@example.com"
	stranger  = "mallory@example.com"
	assignID1 = "01K0M6B8A4FTT8C39MXXYTW7D1"
	assignID2 = "01K0M6B8A4FTT8C39MXXYTW7D2"
	assignID3 = "01K0M6B8A4FTT8C39MXXYTW7D3"
	assignID4 = "01K0M6B8A4FTT8C39MXXYTW7D4"
)

// assignmentPack builds one pack of assignment operations by an actor.
func assignmentPack(actor string, clock uint64, at time.Time, operations ...Operation) OperationPack {
	return NewOperationPack(projectID, taskID, generationID, actor, clock, at, operations)
}

// assign is one assign.add operation with a distinct ULID per call site.
func assign(id, value string) Operation {
	return Operation{ID: id, Type: OperationAssignAdd, Value: value}
}

func unassign(id, value string) Operation {
	return Operation{ID: id, Type: OperationAssignRemove, Value: value}
}

// createdTask is the state every assignment test folds onto.
func createdTask(t *testing.T) StateDocument {
	t.Helper()
	state, err := Apply(nil, createPack(), "WB")
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	return state
}

// assignedTask is a task carrying one assignment, recorded by whoever holds it.
func assignedTask(t *testing.T, value, actor string) StateDocument {
	t.Helper()
	state := createdTask(t)
	next, err := Apply(&state, assignmentPack(actor, 2, updatedAt, assign(assignID1, value)), "WB")
	if err != nil {
		t.Fatalf("Apply(assign %s by %s) error = %v", value, actor, err)
	}
	return next
}

func assignmentValuesOf(state StateDocument) []string {
	return assignmentValues(state.Task.Assignments)
}

// The fold records an assignment with the pack's actor and wall time, which is
// the whole evidence the removal rule is later decided from.
func TestApplyAssignAddRecordsTheActorAndTheWallTime(t *testing.T) {
	state := assignedTask(t, dylan+"/impl-1", teammate)

	want := []Assignment{{Principal: dylan, Label: "impl-1", Creator: teammate, CreatedAt: updatedAt}}
	if !reflect.DeepEqual(state.Task.Assignments, want) {
		t.Fatalf("assignments = %#v, want %#v", state.Task.Assignments, want)
	}
	if got, want := state.Task.UpdatedAt, updatedAt; !got.Equal(want) {
		t.Fatalf("updatedAt = %s, want %s", got, want)
	}
}

// A bare principal carries no label, and the label is absent rather than empty.
func TestApplyAssignAddAcceptsAPrincipalWithoutALabel(t *testing.T) {
	state := assignedTask(t, dylan, dylan)

	if got := state.Task.Assignments[0].Label; got != "" {
		t.Fatalf("label = %q, want empty", got)
	}
	if got, want := state.Task.Assignments[0].Value(), dylan; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

// Multi-assignment is the semantics: two principals, and two agents of one
// principal, all coexist.
func TestApplyAssignAddAccumulatesEveryDistinctAssignment(t *testing.T) {
	state := createdTask(t)
	state, err := Apply(&state, assignmentPack(dylan, 2, updatedAt,
		assign(assignID1, dylan+"/impl-1"),
		assign(assignID2, dylan+"/impl-2"),
	), "WB")
	if err != nil {
		t.Fatalf("Apply(two agents) error = %v", err)
	}
	state, err = Apply(&state, assignmentPack(teammate, 3, updatedAt, assign(assignID3, teammate)), "WB")
	if err != nil {
		t.Fatalf("Apply(teammate) error = %v", err)
	}

	want := []string{dylan + "/impl-1", dylan + "/impl-2", teammate}
	if got := assignmentValuesOf(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}
}

// The stored order is the canonical one, whatever order the operations arrived
// in — two clones that fold the same history have to write the same bytes.
func TestAssignmentsAreStoredInCanonicalOrder(t *testing.T) {
	forward := createdTask(t)
	forward, err := Apply(&forward, assignmentPack(dylan, 2, updatedAt,
		assign(assignID1, "zoe@example.com"),
		assign(assignID2, dylan+"/impl-2"),
		assign(assignID3, dylan+"/impl-1"),
		assign(assignID4, "aaron@example.com"),
	), "WB")
	if err != nil {
		t.Fatalf("Apply(forward) error = %v", err)
	}

	want := []string{"aaron@example.com", dylan + "/impl-1", dylan + "/impl-2", "zoe@example.com"}
	if got := assignmentValuesOf(forward); !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}

	// The same four assignments in a different arrival order, and the same
	// bytes out. Determinism here is what stops two clones from disagreeing
	// about a checkpoint they both computed.
	reverse := createdTask(t)
	reverse, err = Apply(&reverse, assignmentPack(dylan, 2, updatedAt,
		assign(assignID4, "aaron@example.com"),
		assign(assignID3, dylan+"/impl-1"),
		assign(assignID2, dylan+"/impl-2"),
		assign(assignID1, "zoe@example.com"),
	), "WB")
	if err != nil {
		t.Fatalf("Apply(reverse) error = %v", err)
	}
	forwardBytes, err := EncodeDocument(forward)
	if err != nil {
		t.Fatalf("EncodeDocument(forward) error = %v", err)
	}
	reverseBytes, err := EncodeDocument(reverse)
	if err != nil {
		t.Fatalf("EncodeDocument(reverse) error = %v", err)
	}
	if string(forwardBytes) != string(reverseBytes) {
		t.Fatalf("checkpoints differ by arrival order:\n%s\n%s", forwardBytes, reverseBytes)
	}
}

// Re-adding an assignment changes nothing, including its attribution. A
// redelivered pack must not rewrite who assigned whom.
func TestApplyAssignAddIsIdempotentAndKeepsTheFirstAttribution(t *testing.T) {
	state := assignedTask(t, dylan+"/impl-1", teammate)
	later := updatedAt.Add(time.Hour)

	state, err := Apply(&state, assignmentPack(stranger, 3, later, assign(assignID2, dylan+"/impl-1")), "WB")
	if err != nil {
		t.Fatalf("Apply(duplicate add) error = %v", err)
	}

	want := []Assignment{{Principal: dylan, Label: "impl-1", Creator: teammate, CreatedAt: updatedAt}}
	if !reflect.DeepEqual(state.Task.Assignments, want) {
		t.Fatalf("assignments = %#v, want %#v; a duplicate add must not re-attribute", state.Task.Assignments, want)
	}
}

// The first removal branch: the assignee-principal, whatever label the
// assignment carries and whoever recorded it.
func TestApplyAssignRemoveHonorsTheAssigneePrincipal(t *testing.T) {
	state := assignedTask(t, dylan+"/impl-1", teammate)

	state, err := Apply(&state, assignmentPack(dylan, 3, updatedAt, unassign(assignID2, dylan+"/impl-1")), "WB")
	if err != nil {
		t.Fatalf("Apply(self removal) error = %v", err)
	}
	if len(state.Task.Assignments) != 0 {
		t.Fatalf("assignments = %#v, want none; the principal may always remove", state.Task.Assignments)
	}
}

// The second removal branch: the actor who recorded it, so a mistaken tag of a
// teammate is undoable by its author.
func TestApplyAssignRemoveHonorsTheCreator(t *testing.T) {
	state := assignedTask(t, teammate+"/review", dylan)

	state, err := Apply(&state, assignmentPack(dylan, 3, updatedAt, unassign(assignID2, teammate+"/review")), "WB")
	if err != nil {
		t.Fatalf("Apply(creator removal) error = %v", err)
	}
	if len(state.Task.Assignments) != 0 {
		t.Fatalf("assignments = %#v, want none; the creator may undo their own tag", state.Task.Assignments)
	}
}

// THE FOLD. A removal by somebody the rule does not entitle is recorded and
// changes nothing — no error, no conflict, and the assignment still standing.
func TestApplyAssignRemoveFoldsAForeignRemovalToANoOp(t *testing.T) {
	state := assignedTask(t, dylan+"/impl-1", dylan)
	before := copyTaskData(state.Task)

	after, err := Apply(&state, assignmentPack(stranger, 3, updatedAt.Add(time.Minute),
		unassign(assignID2, dylan+"/impl-1")), "WB")
	if err != nil {
		t.Fatalf("Apply(foreign removal) error = %v; a foreign removal must fold, not fail", err)
	}
	if !SameAssignments(before.Assignments, after.Task.Assignments) {
		t.Fatalf("assignments = %#v, want %#v unchanged", after.Task.Assignments, before.Assignments)
	}
	// The pack is recorded: the clock advanced and the operation is in history.
	if got, want := after.LogicalClock, uint64(3); got != want {
		t.Fatalf("logical clock = %d, want %d; the operation must still be recorded", got, want)
	}
}

// The rule keys on the principal's identity, not on the agent label, so an
// orchestrator sweeps up after a fleet member whose agent no longer exists.
func TestApplyAssignRemoveIgnoresTheAgentLabelWhenDecidingAuthority(t *testing.T) {
	state := createdTask(t)
	state, err := Apply(&state, assignmentPack(dylan, 2, updatedAt,
		assign(assignID1, dylan+"/impl-1"),
		assign(assignID2, dylan+"/impl-2"),
	), "WB")
	if err != nil {
		t.Fatalf("Apply(fleet) error = %v", err)
	}

	state, err = Apply(&state, assignmentPack(dylan, 3, updatedAt,
		unassign(assignID3, dylan+"/impl-1"),
		unassign(assignID4, dylan+"/impl-2"),
	), "WB")
	if err != nil {
		t.Fatalf("Apply(sweep) error = %v", err)
	}
	if len(state.Task.Assignments) != 0 {
		t.Fatalf("assignments = %#v, want none after the sweep", state.Task.Assignments)
	}
}

// Removing something that is not there folds to nothing. Two clones removing
// the same assignment is ordinary, and the second replay has to fold.
func TestApplyAssignRemoveToleratesAnAbsentAssignment(t *testing.T) {
	state := assignedTask(t, dylan, dylan)

	after, err := Apply(&state, assignmentPack(dylan, 3, updatedAt, unassign(assignID2, teammate)), "WB")
	if err != nil {
		t.Fatalf("Apply(absent removal) error = %v", err)
	}
	if got, want := assignmentValuesOf(after), []string{dylan}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}
}

// Every honest reader replaying the same bytes reaches the same task. This is
// the property that makes the rule a data-model contract rather than a
// courtesy of the mutation boundary: the decision reads only the pack's actor
// and the assignment's own record.
func TestTheRemovalRuleIsDecidedOnlyFromTheHistory(t *testing.T) {
	packs := []OperationPack{
		createPack(),
		assignmentPack(teammate, 2, updatedAt, assign(assignID1, dylan+"/impl-1")),
		assignmentPack(stranger, 3, updatedAt.Add(time.Minute), unassign(assignID2, dylan+"/impl-1")),
		assignmentPack(dylan, 4, updatedAt.Add(2*time.Minute), assign(assignID3, teammate)),
	}
	fold := func() []byte {
		var parent *StateDocument
		for _, pack := range packs {
			state, err := Apply(parent, pack, "WB")
			if err != nil {
				t.Fatalf("Apply(%s) error = %v", pack.Operations[0].Type, err)
			}
			next := state
			parent = &next
		}
		encoded, err := EncodeDocument(*parent)
		if err != nil {
			t.Fatalf("EncodeDocument() error = %v", err)
		}
		return encoded
	}

	first, second := fold(), fold()
	if string(first) != string(second) {
		t.Fatalf("two folds of one history disagree:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), `"principal":"`+dylan+`"`) {
		t.Fatalf("the foreign removal took effect: %s", first)
	}
	if !strings.Contains(string(first), `"principal":"`+teammate+`"`) {
		t.Fatalf("the later assignment is missing: %s", first)
	}
}

// A task nobody assigned writes no assignments member at all, which is the
// whole of the byte-compatibility promise.
func TestATaskWithNoAssignmentsWritesNoAssignmentsMember(t *testing.T) {
	state := createdTask(t)
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if strings.Contains(string(encoded), "assignments") {
		t.Fatalf("checkpoint mentions assignments: %s", encoded)
	}
}

// A pack carrying an assignment operation declares generation one; one carrying
// none declares nothing. That per-type derivation is what keeps an older clone
// folding every task nobody assigned.
func TestAssignmentPacksCarryTheWriterFormatMarkerAndOthersDoNot(t *testing.T) {
	assigning := assignmentPack(dylan, 2, updatedAt, assign(assignID1, dylan))
	if got, want := assigning.MinReader, 1; got != want {
		t.Fatalf("assign pack minReader = %d, want %d", got, want)
	}
	if got, want := PackMinReader(createPack().Operations), 0; got != want {
		t.Fatalf("create pack minReader = %d, want %d", got, want)
	}
	removing := assignmentPack(dylan, 2, updatedAt, unassign(assignID1, dylan))
	if got, want := removing.MinReader, 1; got != want {
		t.Fatalf("unassign pack minReader = %d, want %d", got, want)
	}
	// The marker is on the encoded document, where an older reader finds it.
	encoded, err := EncodeDocument(assigning)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"minReader":1,`) {
		t.Fatalf("encoded pack carries no marker: %s", encoded)
	}
}

// The checkpoint carries the watermark forward, so a later ordinary pack does
// not take the requirement back.
func TestAnAssignmentPutsAPermanentWatermarkOnTheCheckpoint(t *testing.T) {
	state := assignedTask(t, dylan, dylan)
	if got, want := state.MinReader, 1; got != want {
		t.Fatalf("checkpoint minReader = %d, want %d", got, want)
	}
	title := "Renamed"
	ordinary := NewOperationPack(projectID, taskID, generationID, dylan, 3, updatedAt,
		[]Operation{{ID: assignID2, Type: OperationFieldSet, Field: "title", Value: title}})
	if got, want := ordinary.MinReader, 0; got != want {
		t.Fatalf("ordinary pack minReader = %d, want %d", got, want)
	}
	next, err := Apply(&state, ordinary, "WB")
	if err != nil {
		t.Fatalf("Apply(ordinary) error = %v", err)
	}
	if got, want := next.MinReader, 1; got != want {
		t.Fatalf("checkpoint minReader after an ordinary pack = %d, want %d", got, want)
	}
}

// Assignments never ride in a task.create. The create declares generation zero,
// so smuggling one in its task data would produce a pack an older clone accepts
// the header of and then calls corrupt — the exact outcome the writer-format
// contract exists to prevent.
func TestApplyRefusesATaskCreateCarryingAssignments(t *testing.T) {
	pack := createPack()
	task := copyTaskData(*pack.Operations[0].Task)
	task.Assignments = []Assignment{{Principal: dylan, Creator: dylan, CreatedAt: createdAt}}
	pack.Operations[0].Task = &task

	if _, err := Apply(nil, pack, "WB"); err == nil {
		t.Fatal("Apply(create with assignments) error = nil, want a refusal")
	} else if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("category = %q, want %q", got, CategoryCorruptData)
	}
}

func TestApplyRejectsMalformedAssignmentOperations(t *testing.T) {
	for name, operation := range map[string]Operation{
		"blank value":       {ID: assignID1, Type: OperationAssignAdd, Value: ""},
		"embedded space":    {ID: assignID1, Type: OperationAssignAdd, Value: "dylan example@example.com"},
		"newline":           {ID: assignID1, Type: OperationAssignAdd, Value: dylan + "/impl\n1"},
		"empty principal":   {ID: assignID1, Type: OperationAssignAdd, Value: "/impl-1"},
		"empty label":       {ID: assignID1, Type: OperationAssignAdd, Value: dylan + "/"},
		"carries a field":   {ID: assignID1, Type: OperationAssignAdd, Field: "assignments", Value: dylan},
		"carries task data": {ID: assignID1, Type: OperationAssignAdd, Task: &TaskData{}, Value: dylan},
		"oversized":         {ID: assignID1, Type: OperationAssignAdd, Value: strings.Repeat("a", MaxAssignmentBytes) + "@example.com"},
		"remove blank":      {ID: assignID1, Type: OperationAssignRemove, Value: ""},
	} {
		t.Run(name, func(t *testing.T) {
			state := createdTask(t)
			_, err := Apply(&state, assignmentPack(dylan, 2, updatedAt, operation), "WB")
			if err == nil {
				t.Fatalf("Apply(%#v) error = nil, want a refusal", operation)
			}
			if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("category = %q, want %q", got, CategoryCorruptData)
			}
		})
	}
}

// The fold accepts an identity it would never have authored. A principal that
// does not look like an email address is a teammate's business, not this
// clone's, and refusing one during replay would turn a fetched history into
// corrupt data.
func TestApplyAcceptsAPrincipalTheBoundaryWouldRefuse(t *testing.T) {
	state := createdTask(t)
	state, err := Apply(&state, assignmentPack(dylan, 2, updatedAt, assign(assignID1, "dylan")), "WB")
	if err != nil {
		t.Fatalf("Apply(bare principal) error = %v; replay must not judge an identity", err)
	}
	if got, want := assignmentValuesOf(state), []string{"dylan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}
	if err := ValidateAssigneeAuthoring("dylan"); err == nil {
		t.Fatal("ValidateAssigneeAuthoring(\"dylan\") error = nil, want a boundary refusal")
	} else if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("boundary category = %q, want %q", got, CategoryValidation)
	}
}

func TestValidateAssigneeAuthoringBoundsAndShapesTheValue(t *testing.T) {
	for name, testCase := range map[string]struct {
		value string
		valid bool
	}{
		"principal":                {value: dylan, valid: true},
		"principal and label":      {value: dylan + "/impl-1", valid: true},
		"label with a slash":       {value: dylan + "/fleet/impl-1", valid: true},
		"label with punctuation":   {value: dylan + "/spike#3", valid: true},
		"label at the ceiling":     {value: dylan + "/" + strings.Repeat("a", MaxAssignmentLabelBytes), valid: true},
		"no at sign":               {value: "dylan"},
		"no domain dot":            {value: "dylan@localhost"},
		"blank":                    {value: ""},
		"blank label":              {value: dylan + "/"},
		"space":                    {value: "dylan example@example.com"},
		"two at signs":             {value: "dylan@@example.com"},
		"label over the ceiling":   {value: dylan + "/" + strings.Repeat("a", MaxAssignmentLabelBytes+1)},
		"principal over the limit": {value: strings.Repeat("a", MaxAssignmentPrincipalBytes) + "@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateAssigneeAuthoring(testCase.value)
			if testCase.valid {
				if err != nil {
					t.Fatalf("ValidateAssigneeAuthoring(%q) error = %v, want nil", testCase.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateAssigneeAuthoring(%q) error = nil, want a refusal", testCase.value)
			}
			if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("category = %q, want %q", got, CategoryValidation)
			}
		})
	}
}

// The change log reports what an assignment operation did, not what it asked
// for. A row reading "removed dylan@example.com" beside a task that is still
// assigned to dylan@example.com would be the log lying about the one thing this
// design most needs to be legible.
func TestTheChangeLogReportsAssignmentOperationsByTheirEffect(t *testing.T) {
	history := TaskHistory{Entries: []HistoryEntry{
		{Commit: "c1", Operation: createPack()},
		{Commit: "c2", Operation: assignmentPack(dylan, 2, updatedAt, assign(assignID1, dylan+"/impl-1"))},
		{Commit: "c3", Operation: assignmentPack(stranger, 3, updatedAt.Add(time.Minute),
			unassign(assignID2, dylan+"/impl-1"))},
		{Commit: "c4", Operation: assignmentPack(teammate, 4, updatedAt.Add(2*time.Minute),
			assign(assignID3, dylan+"/impl-1"))},
		{Commit: "c5", Operation: assignmentPack(dylan, 5, updatedAt.Add(3*time.Minute),
			unassign(assignID4, dylan+"/impl-1"))},
	}}

	log := BuildChangeLog("WB", history, 0, true)
	if log.Truncated != nil {
		t.Fatalf("change log truncated at %#v; every entry must replay", log.Truncated)
	}
	if got, want := len(log.Changes), 5; got != want {
		t.Fatalf("changes = %d, want %d; every pack is a row whether or not it did anything", got, want)
	}

	assignment := log.Changes[1]
	if len(assignment.Fields) != 1 || assignment.Fields[0].Field != "assignments" ||
		assignment.Fields[0].Kind != ChangeAdded || assignment.Fields[0].To != dylan+"/impl-1" {
		t.Fatalf("the assignment row = %#v, want one added assignment", assignment.Fields)
	}

	// The foreign removal, and the duplicate add after it, both changed nothing.
	for _, index := range []int{2, 3} {
		if got := log.Changes[index].Fields; len(got) != 0 {
			t.Fatalf("row %d fields = %#v, want none for an operation that changed nothing", index, got)
		}
		if got, want := log.Changes[index].Summary, "recorded no visible change"; got != want {
			t.Fatalf("row %d summary = %q, want %q", index, got, want)
		}
	}
	// The actor is still recorded, so a no-op is visible as something somebody
	// attempted rather than erased from the history.
	if got, want := log.Changes[2].Actor, stranger; got != want {
		t.Fatalf("no-op row actor = %q, want %q", got, want)
	}

	removal := log.Changes[4]
	if len(removal.Fields) != 1 || removal.Fields[0].Kind != ChangeRemoved ||
		removal.Fields[0].From != dylan+"/impl-1" {
		t.Fatalf("the removal row = %#v, want one removed assignment", removal.Fields)
	}
}

// A comparison between two points reports assignments the way it reports every
// other collection.
func TestCompareTasksReportsAssignmentDifferences(t *testing.T) {
	before := TaskData{Assignments: []Assignment{
		{Principal: dylan, Label: "impl-1", Creator: dylan, CreatedAt: createdAt},
	}}
	after := TaskData{Assignments: []Assignment{
		{Principal: teammate, Creator: teammate, CreatedAt: updatedAt},
	}}

	fields := CompareTasks(before, after)
	want := []FieldChange{
		{Field: "assignments", Kind: ChangeRemoved, From: dylan + "/impl-1"},
		{Field: "assignments", Kind: ChangeAdded, To: teammate},
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("CompareTasks() = %#v, want %#v", fields, want)
	}
	if got, want := FieldLabel("assignments"), "Assignments"; got != want {
		t.Fatalf("FieldLabel(assignments) = %q, want %q", got, want)
	}
}

// A rejected value is never echoed once it is past the ceiling, because that
// would spend exactly the memory the ceiling withholds.
func TestAnOversizedAssignmentIsNotQuotedBack(t *testing.T) {
	oversized := strings.Repeat("a", MaxAssignmentBytes+1) + "@example.com"
	err := ValidateAssigneeAuthoring(oversized)
	if err == nil {
		t.Fatal("ValidateAssigneeAuthoring(oversized) error = nil, want a refusal")
	}
	if strings.Contains(err.Error(), strings.Repeat("a", 64)) {
		t.Fatalf("the refusal quoted the rejected value: %q", err.Error())
	}
}

// A checkpoint whose assignments are out of order, duplicated, or missing the
// evidence the removal rule needs is not canonical.
func TestNormalizeTaskRejectsNoncanonicalAssignments(t *testing.T) {
	base := func() TaskData {
		return TaskData{
			Title: "Task", Status: StatusBacklog, Priority: PriorityMedium,
			Rank: "1/1", CreatedAt: createdAt, UpdatedAt: createdAt,
		}
	}
	for name, assignments := range map[string][]Assignment{
		"duplicate": {
			{Principal: dylan, Label: "impl-1", Creator: dylan, CreatedAt: createdAt},
			{Principal: dylan, Label: "impl-1", Creator: teammate, CreatedAt: updatedAt},
		},
		"no creator":       {{Principal: dylan, CreatedAt: createdAt}},
		"no creation time": {{Principal: dylan, Creator: dylan}},
		"blank principal":  {{Creator: dylan, CreatedAt: createdAt}},
	} {
		t.Run(name, func(t *testing.T) {
			task := base()
			task.Assignments = assignments
			if _, err := NormalizeTask("WB", task); err == nil {
				t.Fatalf("NormalizeTask(%#v) error = nil, want a refusal", assignments)
			}
		})
	}

	t.Run("out of order sorts", func(t *testing.T) {
		task := base()
		task.Assignments = []Assignment{
			{Principal: teammate, Creator: teammate, CreatedAt: createdAt},
			{Principal: dylan, Label: "b", Creator: dylan, CreatedAt: createdAt},
			{Principal: dylan, Label: "a", Creator: dylan, CreatedAt: createdAt},
		}
		normalized, err := NormalizeTask("WB", task)
		if err != nil {
			t.Fatalf("NormalizeTask() error = %v", err)
		}
		want := []string{dylan + "/a", dylan + "/b", teammate}
		if got := assignmentValues(normalized.Assignments); !reflect.DeepEqual(got, want) {
			t.Fatalf("assignments = %#v, want %#v", got, want)
		}
	})
}

// The fold does not police how many assignments a task has. Two clones each
// adding one can carry a task past the boundary's ceiling without either
// operation being anything but ordinary, and a fold that failed on the count
// would make that pair of acts a task no clone could ever read again.
func TestTheFoldDoesNotEnforceTheAssignmentCeiling(t *testing.T) {
	task := TaskData{
		Title: "Task", Status: StatusBacklog, Priority: PriorityMedium,
		Rank: "1/1", CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	for index := 0; index <= MaxAssignmentCount; index++ {
		task.Assignments = append(task.Assignments, Assignment{
			Principal: dylan,
			Label:     "impl-" + string(rune('a'+index%26)) + string(rune('a'+index/26)),
			Creator:   dylan,
			CreatedAt: createdAt,
		})
	}
	if _, err := NormalizeTask("WB", task); err != nil {
		t.Fatalf("NormalizeTask(over the ceiling) error = %v; the fold must not enforce a count", err)
	}
}

func TestAssignmentsHeldByOthersExcludesThePrincipalsOwnAgents(t *testing.T) {
	assignments := []Assignment{
		{Principal: dylan, Label: "impl-1", Creator: dylan, CreatedAt: createdAt},
		{Principal: dylan, Label: "impl-2", Creator: dylan, CreatedAt: createdAt},
		{Principal: teammate, Creator: teammate, CreatedAt: createdAt},
	}
	others := AssignmentsHeldByOthers(assignments, dylan)
	if got, want := assignmentValues(others), []string{teammate}; !reflect.DeepEqual(got, want) {
		t.Fatalf("others = %#v, want %#v", got, want)
	}
	if got := AssignmentsHeldByOthers(assignments[:2], dylan); got != nil {
		t.Fatalf("others = %#v, want nil when the principal holds them all", got)
	}
}
