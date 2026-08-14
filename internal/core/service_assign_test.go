package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	serviceActor = "developer@example.com"
	// serviceAssignTaskID is the one task every case in this file assigns.
	serviceAssignTaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7E1"
)

// assignService is a service whose acting identity can be varied, which is what
// every removal-authority case turns on.
func assignService(store *memoryTaskStore, actor string, ids ...string) Service {
	service := serviceUnderTest(store, &sequenceIDSource{values: ids})
	service.Actor = actor
	return service
}

// assignedSnapshot is a stored task carrying assignments.
func assignedSnapshot(id string, assignments ...Assignment) Snapshot {
	snapshot := serviceSnapshot(id, TaskData{
		Title: "Assignable", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	snapshot.State.Task.Assignments = assignments
	return snapshot
}

func heldBy(principal, label, creator string) Assignment {
	return Assignment{
		Principal: principal, Label: label, Creator: creator,
		CreatedAt: serviceTestNow.Add(-time.Hour),
	}
}

// A bare assign self-assigns, which is the shape an agent uses: it never has to
// spell its own identity and so cannot get it wrong.
func TestAssignMutationDefaultsToTheActingIdentity(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: operationID1, Type: OperationAssignAdd, Value: serviceActor},
	})
	want := []Assignment{{Principal: serviceActor, Creator: serviceActor, CreatedAt: serviceTestNow}}
	if !reflect.DeepEqual(result.Task.Assignments, want) {
		t.Fatalf("assignments = %#v, want %#v", result.Task.Assignments, want)
	}
	if result.Already {
		t.Fatal("Already = true, want false on a fresh assignment")
	}
	if result.Others != nil {
		t.Fatalf("Others = %#v, want none", result.Others)
	}
}

// The claim path's whole warning, answered by the mutation that observed the
// parent rather than by a second read.
func TestAssignMutationReportsTheAssignmentsAlreadyHeldByOthers(t *testing.T) {
	held := heldBy("sam@example.com", "", "sam@example.com")
	mine := heldBy(serviceActor, "impl-1", serviceActor)
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, mine, held))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{To: serviceActor + "/impl-2"})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	if got, want := assignmentValues(result.Others), []string{"sam@example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Others = %#v, want %#v; the caller's own agents are not others", got, want)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want 1; assignment is additive and never refuses", len(store.writes))
	}
}

// Adding an assignment that is already there writes nothing and says so.
func TestAssignMutationIsIdempotent(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy(serviceActor, "impl-1", "sam@example.com")))
	service := assignService(store, serviceActor)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{To: serviceActor + "/impl-1"})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	if !result.Already {
		t.Fatal("Already = false, want true")
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none for an assignment that already exists", len(store.writes))
	}
}

// The boundary judges the identity somebody typed, which the fold deliberately
// does not.
func TestAssignMutationRefusesAnImplausiblePrincipal(t *testing.T) {
	for _, value := range []string{"dylan", "dylan@localhost", "dylan@example.com/", "dylan example@example.com"} {
		t.Run(value, func(t *testing.T) {
			store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
			service := assignService(store, serviceActor)
			_, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{To: value})
			if err == nil {
				t.Fatalf("AssignMutation(%q) error = nil, want a refusal", value)
			}
			if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("category = %q, want %q", got, CategoryValidation)
			}
			if len(store.writes) != 0 {
				t.Fatalf("writes = %d, want none", len(store.writes))
			}
		})
	}
}

// An assignment's creator is half the removal rule's evidence, so a clone with
// no configured identity is refused rather than defaulted into recording an
// assignment nobody could ever be shown as entitled to remove.
func TestAssignMutationRefusesWhenTheCloneHasNoIdentity(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
	service := assignService(store, "")

	_, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{})
	if err == nil {
		t.Fatal("AssignMutation() error = nil, want a refusal with no identity")
	}
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q", got, CategoryValidation)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// The acting identity is bounded where somebody can read the reason, because
// it is about to be written into the record as the assignment's creator.
func TestAssignMutationRefusesAnOversizedActingIdentity(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
	service := assignService(store, strings.Repeat("a", MaxAssignmentPrincipalBytes)+"@example.com")

	_, err := service.AssignMutation(context.Background(), serviceAssignTaskID,
		AssignInput{To: "sam@example.com"})
	if err == nil {
		t.Fatal("AssignMutation() error = nil, want a refusal")
	}
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; an over-long identity is not corrupt data", got, CategoryValidation)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// The count ceiling lives here and only here.
func TestAssignMutationRefusesPastTheAssignmentCeiling(t *testing.T) {
	existing := make([]Assignment, 0, MaxAssignmentCount)
	for index := 0; index < MaxAssignmentCount; index++ {
		existing = append(existing, heldBy("sam@example.com", "impl-"+string(rune('a'+index)), "sam@example.com"))
	}
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, existing...))
	service := assignService(store, serviceActor)

	_, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{})
	if err == nil {
		t.Fatal("AssignMutation() error = nil, want a refusal at the ceiling")
	}
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q", got, CategoryValidation)
	}
}

// Layer one of the removal rule: the mutation boundary refuses a foreign
// removal, names who may make it, and writes nothing.
func TestUnassignMutationRefusesAForeignRemovalAndNamesWhoMay(t *testing.T) {
	assignment := heldBy("dylan@example.com", "impl-1", "sam@example.com")
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, assignment))
	service := assignService(store, "mallory@example.com")

	_, err := service.UnassignMutation(context.Background(), serviceAssignTaskID,
		UnassignInput{From: "dylan@example.com/impl-1"})
	if err == nil {
		t.Fatal("UnassignMutation() error = nil, want a refusal")
	}
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q", got, CategoryValidation)
	}
	for _, wanted := range []string{"dylan@example.com/impl-1", serviceAssignTaskID, "dylan@example.com", "sam@example.com"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Fatalf("refusal = %q, want it to name %q", err.Error(), wanted)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// The refusal names one address when the assignee recorded their own
// assignment, which is the ordinary case.
func TestUnassignMutationRefusalNamesOneAddressWhenTheAssigneeRecordedIt(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy("dylan@example.com", "", "dylan@example.com")))
	service := assignService(store, "mallory@example.com")

	_, err := service.UnassignMutation(context.Background(), serviceAssignTaskID, UnassignInput{From: "dylan@example.com"})
	if err == nil {
		t.Fatal("UnassignMutation() error = nil, want a refusal")
	}
	if strings.Count(err.Error(), "dylan@example.com") != 2 {
		t.Fatalf("refusal = %q, want the address named once as the assignment and once as the remover", err.Error())
	}
	if strings.Contains(err.Error(), "who recorded it") {
		t.Fatalf("refusal = %q, want no second clause when the assignee recorded it", err.Error())
	}
}

// Both authority branches succeed at the boundary.
func TestUnassignMutationAcceptsThePrincipalAndTheCreator(t *testing.T) {
	for name, actor := range map[string]string{
		"the principal": "dylan@example.com",
		"the creator":   "sam@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID,
				heldBy("dylan@example.com", "impl-1", "sam@example.com")))
			service := assignService(store, actor, operationID1)

			result, err := service.UnassignMutation(context.Background(), serviceAssignTaskID,
				UnassignInput{From: "dylan@example.com/impl-1"})
			if err != nil {
				t.Fatalf("UnassignMutation() error = %v", err)
			}
			assertOperations(t, store.writes[0].pack.Operations, []Operation{
				{ID: operationID1, Type: OperationAssignRemove, Value: "dylan@example.com/impl-1"},
			})
			if len(result.Task.Assignments) != 0 {
				t.Fatalf("assignments = %#v, want none", result.Task.Assignments)
			}
		})
	}
}

// Removing what is not there is a mistake where somebody can see it, and a
// tolerated no-op only once it is history — see the fold.
func TestUnassignMutationRefusesAnAssignmentThatIsNotThere(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy("dylan@example.com", "", "dylan@example.com")))
	service := assignService(store, "dylan@example.com")

	_, err := service.UnassignMutation(context.Background(), serviceAssignTaskID,
		UnassignInput{From: "dylan@example.com/impl-1"})
	if err == nil {
		t.Fatal("UnassignMutation() error = nil, want a refusal")
	}
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q", got, CategoryValidation)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// A removal names an assignment the history already holds, so it is checked for
// structure and not for plausibility: an assignment somebody else's tooling
// recorded is exactly the one most in need of being withdrawable.
func TestUnassignMutationRemovesAPrincipalTheBoundaryWouldNotHaveAuthored(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy("dylan", "", "sam@example.com")))
	service := assignService(store, "sam@example.com", operationID1)

	if _, err := service.UnassignMutation(context.Background(), serviceAssignTaskID, UnassignInput{From: "dylan"}); err != nil {
		t.Fatalf("UnassignMutation() error = %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(store.writes))
	}
}

func TestAssignMutationsRefuseATombstonedTask(t *testing.T) {
	snapshot := assignedSnapshot(serviceAssignTaskID, heldBy(serviceActor, "", serviceActor))
	snapshot.State.Task.Deleted = true
	store := newMemoryTaskStore(snapshot)
	service := assignService(store, serviceActor)

	if _, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{}); err == nil {
		t.Fatal("AssignMutation() error = nil, want a refusal on a tombstoned task")
	} else if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("assign category = %q, want %q", got, CategoryValidation)
	}
	if _, err := service.UnassignMutation(context.Background(), serviceAssignTaskID, UnassignInput{}); err == nil {
		t.Fatal("UnassignMutation() error = nil, want a refusal on a tombstoned task")
	} else if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("unassign category = %q, want %q", got, CategoryValidation)
	}
}

func TestAssignMutationsHonorTheExpectedHead(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy(serviceActor, "", serviceActor)))
	service := assignService(store, serviceActor)

	if _, err := service.AssignMutation(context.Background(), serviceAssignTaskID,
		AssignInput{To: "sam@example.com", ExpectedHead: "stale"}); CategoryOf(err) != CategoryStaleWrite {
		t.Fatalf("AssignMutation() category = %q, want %q", CategoryOf(err), CategoryStaleWrite)
	}
	if _, err := service.UnassignMutation(context.Background(), serviceAssignTaskID,
		UnassignInput{ExpectedHead: "stale"}); CategoryOf(err) != CategoryStaleWrite {
		t.Fatalf("UnassignMutation() category = %q, want %q", CategoryOf(err), CategoryStaleWrite)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// An assignment rides in the same pack as the correct-on-touch status
// settlement, which is what "composable with other update operations" means in
// practice: one history entry, one refusal surface.
func TestAssignMutationSettlesAStaleStoredStatusInTheSamePack(t *testing.T) {
	snapshot := serviceSnapshot(serviceAssignTaskID, TaskData{
		Title: "Assignable", Status: Status("shipped"), Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(snapshot)
	service := assignService(store, serviceActor, operationID1, operationID2)
	service.Vocabulary = customVocabulary(t)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: operationID1, Type: OperationAssignAdd, Value: serviceActor},
		{ID: operationID2, Type: OperationFieldSet, Field: "status", Value: "released"},
	})
	if result.StatusCorrected == nil || result.StatusCorrected.To != Status("released") {
		t.Fatalf("StatusCorrected = %#v, want a settlement to released", result.StatusCorrected)
	}
}

// The commit subject says which assignment moved, because `git log` over a task
// ref is how somebody reconstructs a claim they cannot otherwise explain.
func TestAssignMutationCommitSubjectsNameTheAssignment(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
	service := assignService(store, serviceActor, operationID1)
	if _, err := service.AssignMutation(context.Background(), serviceAssignTaskID,
		AssignInput{To: "sam@example.com/review"}); err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	if got := store.writes[0].reason; !strings.Contains(got, "assign ") || !strings.Contains(got, "sam@example.com/review") {
		t.Fatalf("commit subject = %q, want it to name the assignment", got)
	}
}
