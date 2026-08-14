package core

import (
	"context"
	"reflect"
	"strconv"
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

// A pack that hands one assignment over to another ends the same side of the
// ceiling it started on, so it is not the ceiling's business. Counting the
// pack's operations instead of its growth refused it.
func TestUpdateMutationAllowsAHandoverAtTheAssignmentCeiling(t *testing.T) {
	existing := make([]Assignment, 0, MaxAssignmentCount)
	for index := 0; index < MaxAssignmentCount; index++ {
		existing = append(existing, heldBy("sam@example.com", "impl-"+strconv.Itoa(100+index), serviceActor))
	}
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, existing...))
	service := assignService(store, serviceActor, operationID1, operationID2)

	result, err := service.UpdateMutation(context.Background(), serviceAssignTaskID, UpdateInput{
		Assignments: []AssignmentChange{
			{To: existing[0].Value(), Remove: true},
			{To: serviceActor},
		},
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v, want a handover at the ceiling to be allowed", err)
	}
	if got := len(result.Task.Assignments); got != MaxAssignmentCount {
		t.Fatalf("assignments = %d, want %d", got, MaxAssignmentCount)
	}

	// Growing past it is still refused, which is the rule the arithmetic exists
	// to enforce.
	_, err = service.UpdateMutation(context.Background(), serviceAssignTaskID, UpdateInput{
		Assignments: []AssignmentChange{{To: "mallory@example.com"}},
	})
	if CategoryOf(err) != CategoryValidation {
		t.Fatalf("category = %q, want %q past the ceiling", CategoryOf(err), CategoryValidation)
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

// The claim gate: asked for, it refuses a task somebody else holds, names them,
// and writes nothing. Nothing written is the promise the exit code carries — an
// agent that meets this holds no assignment and owes nobody a cleanup.
func TestAssignMutationRefusesATaskSomebodyElseHoldsWhenAskedTo(t *testing.T) {
	held := heldBy("sam@example.com", "review", "sam@example.com")
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, held))
	service := assignService(store, serviceActor, operationID1)

	_, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{OnlyIfUnheld: true})
	if err == nil {
		t.Fatal("AssignMutation() error = nil, want a claim refusal")
	}
	if got := CategoryOf(err); got != CategoryAssigned {
		t.Fatalf("category = %q, want %q; a fleet agent branches on this and on nothing else", got, CategoryAssigned)
	}
	for _, wanted := range []string{serviceAssignTaskID, "sam@example.com/review"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Fatalf("refusal = %q, want it to name %q", err.Error(), wanted)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none; a refused claim records nothing", len(store.writes))
	}
}

// The gate is opt-in, and without it the assignment lands beside theirs — which
// is what --force asks for and what pairs two agents on one task deliberately.
func TestAssignMutationRecordsBesideAnotherPrincipalWhenTheGateIsNotAsked(t *testing.T) {
	held := heldBy("sam@example.com", "review", "sam@example.com")
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, held))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID, AssignInput{})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(store.writes))
	}
	if got, want := assignmentValues(result.Others), []string{"sam@example.com/review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Others = %#v, want %#v", got, want)
	}
	if got := assignmentValues(result.Task.Assignments); len(got) != 2 {
		t.Fatalf("assignments = %#v, want both; assignment is additive and removes nothing", got)
	}
}

// The gate and the skip have to agree about what "somebody else's" means. An
// identity that already holds a shared task is offered it by Next, so the gate
// must let it record a second agent of its own — otherwise an agent is offered
// a task it is then refused, forever.
func TestAssignMutationGateLetsAPrincipalThatAlreadyHoldsTheTaskAddAnAgent(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID,
		heldBy(serviceActor, "impl-1", serviceActor),
		heldBy("sam@example.com", "", "sam@example.com"),
	))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID,
		AssignInput{To: serviceActor + "/impl-2", OnlyIfUnheld: true})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v, want a second agent of a holding identity to be let through", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(store.writes))
	}
	if got, want := assignmentValues(result.Others), []string{"sam@example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Others = %#v, want %#v", got, want)
	}
}

// The gate never fires on an assignment the caller already holds. Refusing a
// no-op would send an agent away from work it is already doing.
func TestAssignMutationGateIgnoresAnAssignmentAlreadyHeld(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID,
		heldBy(serviceActor, "impl-1", serviceActor),
		heldBy("sam@example.com", "", "sam@example.com"),
	))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.AssignMutation(context.Background(), serviceAssignTaskID,
		AssignInput{To: serviceActor + "/impl-1", OnlyIfUnheld: true})
	if err != nil {
		t.Fatalf("AssignMutation() error = %v, want the shared task's own holder to be let through", err)
	}
	if !result.Already {
		t.Fatal("Already = false, want true")
	}
	if got, want := assignmentValues(result.Others), []string{"sam@example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Others = %#v, want %#v; the caller still shares the task", got, want)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want none", len(store.writes))
	}
}

// The composition the whole arrangement exists for: a status and an assignment
// given together are one pack, so an agent that takes work up records taking it
// and starting it as one change that cannot half-succeed.
func TestUpdateMutationCarriesAnAssignmentInTheSamePackAsAStatus(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID))
	service := assignService(store, serviceActor, operationID1, operationID2)
	inProgress := StatusInProgress

	result, err := service.UpdateMutation(context.Background(), serviceAssignTaskID, UpdateInput{
		Status:      &inProgress,
		Assignments: []AssignmentChange{{OnlyIfUnheld: true}},
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want exactly one pack", len(store.writes))
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: operationID1, Type: OperationFieldSet, Field: "status", Value: string(inProgress)},
		{ID: operationID2, Type: OperationAssignAdd, Value: serviceActor},
	})
	if result.Task.Status != inProgress || len(result.Task.Assignments) != 1 {
		t.Fatalf("task = %#v, want the status moved and the assignment recorded", result.Task)
	}
	if got := store.writes[0].reason; !strings.Contains(got, "status ") || !strings.Contains(got, "assign "+serviceActor) {
		t.Fatalf("commit subject = %q, want it to name both changes", got)
	}
}

// An update that changes nothing but re-states an assignment already held is the
// one empty update that is not a mistake.
func TestUpdateMutationReportsAnAssignmentAlreadyHeldRatherThanAnEmptyUpdate(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy(serviceActor, "", serviceActor)))
	service := assignService(store, serviceActor, operationID1)

	result, err := service.UpdateMutation(context.Background(), serviceAssignTaskID, UpdateInput{
		Assignments: []AssignmentChange{{To: serviceActor}},
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v, want a quiet no-op", err)
	}
	if !result.Already || len(store.writes) != 0 {
		t.Fatalf("Already = %t with %d writes, want an idempotent no-op", result.Already, len(store.writes))
	}
	if result.Task.ID != serviceAssignTaskID {
		t.Fatalf("task = %#v, want the unchanged task back", result.Task)
	}
}

// A withdrawal rides the same pack, and the removal rule still decides it.
func TestUpdateMutationCarriesAWithdrawalInTheSamePack(t *testing.T) {
	store := newMemoryTaskStore(assignedSnapshot(serviceAssignTaskID, heldBy(serviceActor, "impl-1", serviceActor)))
	service := assignService(store, serviceActor, operationID1, operationID2)
	title := "Handed back"

	result, err := service.UpdateMutation(context.Background(), serviceAssignTaskID, UpdateInput{
		Title:       &title,
		Assignments: []AssignmentChange{{To: serviceActor + "/impl-1", Remove: true}},
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: operationID1, Type: OperationFieldSet, Field: "title", Value: title},
		{ID: operationID2, Type: OperationAssignRemove, Value: serviceActor + "/impl-1"},
	})
	if len(result.Task.Assignments) != 0 || result.Others != nil {
		t.Fatalf("task = %#v with Others %#v, want the assignment gone and nobody else named", result.Task, result.Others)
	}
}

// The skip `workbook next` runs on by default: work another principal is
// responsible for is work that is being done.
func TestNextSkipsTasksHeldByAnotherPrincipal(t *testing.T) {
	held := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E2", TaskData{
		Title: "Theirs", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1",
	})
	held.State.Task.Assignments = []Assignment{heldBy("sam@example.com", "impl-1", "sam@example.com")}
	mine := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E3", TaskData{
		Title: "Mine", Status: StatusReady, Priority: PriorityHigh, Rank: "2/1",
	})
	mine.State.Task.Assignments = []Assignment{heldBy(serviceActor, "impl-2", serviceActor)}
	store := newMemoryTaskStore(held, mine)
	service := assignService(store, serviceActor)

	selected, err := service.Next(context.Background(), NextOptions{})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selected == nil || selected.ID != mine.State.TaskID {
		t.Fatalf("Next() = %#v, want the task this identity already holds rather than the one somebody else does", selected)
	}

	selected, err = service.Next(context.Background(), NextOptions{IncludeHeldByOthers: true})
	if err != nil {
		t.Fatalf("Next(--any) error = %v", err)
	}
	if selected == nil || selected.ID != held.State.TaskID {
		t.Fatalf("Next(--any) = %#v, want the highest-ranked eligible task whoever holds it", selected)
	}

	// A service with no acting identity has no self to tell others apart from,
	// so it skips nothing rather than silently answering that there is no work.
	anonymous := assignService(store, "")
	selected, err = anonymous.Next(context.Background(), NextOptions{})
	if err != nil {
		t.Fatalf("Next() with no identity error = %v", err)
	}
	if selected == nil || selected.ID != held.State.TaskID {
		t.Fatalf("Next() with no identity = %#v, want the whole eligible set", selected)
	}
}

// The spike's own claimants keep being offered their work. A task two identities
// deliberately share is skipped by neither of them — the alternative is that the
// pairing the design calls a meaningful outcome leaves both agents told there is
// nothing to do.
func TestNextOffersATaskThisIdentityShares(t *testing.T) {
	shared := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7E4", TaskData{
		Title: "Spiked together", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1",
	})
	shared.State.Task.Assignments = []Assignment{
		heldBy(serviceActor, "impl-1", serviceActor),
		heldBy("sam@example.com", "", "sam@example.com"),
	}
	store := newMemoryTaskStore(shared)

	for _, actor := range []string{serviceActor, "sam@example.com"} {
		selected, err := assignService(store, actor).Next(context.Background(), NextOptions{})
		if err != nil {
			t.Fatalf("Next() as %s error = %v", actor, err)
		}
		if selected == nil || selected.ID != shared.State.TaskID {
			t.Fatalf("Next() as %s = %#v, want the task this identity shares", actor, selected)
		}
	}

	// A third identity is not offered it: it is held, and not by them.
	if selected, err := assignService(store, "mallory@example.com").Next(context.Background(), NextOptions{}); err != nil ||
		selected != nil {
		t.Fatalf("Next() as a stranger = %#v (error %v), want nothing", selected, err)
	}
}
