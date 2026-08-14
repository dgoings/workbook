package core

import (
	"context"
	"strings"
)

// AssignInput names who a task is being assigned to.
type AssignInput struct {
	// To is the assignee as principal[/label]. An empty value means the acting
	// identity, which is the self-assignment the claim path is built on: an
	// agent that assigns itself never has to know how its own identity is
	// spelled, and cannot get it wrong.
	To string
	// ExpectedHead carries the same meaning it does on UpdateInput.
	ExpectedHead string
}

// UnassignInput names the assignment being withdrawn. Its zero To is the acting
// identity, the same way AssignInput's is — an agent releases what it holds
// without spelling itself out.
type UnassignInput struct {
	From         string
	ExpectedHead string
}

// AssignResult is a mutation result plus what the assignment landed beside.
//
// The two extra members exist so a caller can explain the outcome without
// reading the task a second time. Claiming a task synchronously means
// fetch, check, append, push in one stroke, and the check is this: an agent
// that assigns itself a task somebody else already holds has to be told so it
// can pick another one, and the warning has to describe the assignments that
// were actually there when the pack was written rather than whatever a second
// read finds a moment later.
type AssignResult struct {
	MutationResult
	// Others are the assignments the task already carried under a different
	// principal, in stored order. It is populated whether or not the assignment
	// was recorded, because "you now share this task" and "you already share
	// this task" are the same warning.
	Others []Assignment `json:"others,omitempty"`
	// Already reports that the task already carried this exact assignment, so
	// nothing was written. Adding an assignment twice is idempotent by design;
	// this is how a caller tells that from a fresh claim.
	Already bool `json:"already,omitempty"`
}

// AssignMutation records an assignment on a task.
//
// Assignment is additive and always overrideable by addition: this never
// removes anybody's assignment, and it never refuses because somebody else
// holds the task. Two agents racing for the same work therefore terminate in
// the both-assigned state — a spike, which is a meaningful outcome — instead of
// removing each other's claims forever.
func (s Service) AssignMutation(ctx context.Context, idOrPrefix string, input AssignInput) (AssignResult, error) {
	value, err := s.assignee(input.To)
	if err != nil {
		return AssignResult{}, err
	}
	if err := ValidateAssigneeAuthoring(value); err != nil {
		return AssignResult{}, err
	}
	principal, label, err := SplitAssignmentValue(value)
	if err != nil {
		return AssignResult{}, err
	}
	// The acting identity becomes the assignment's creator, so it is bounded
	// here rather than only inside the fold. The fold does bound it — a stored
	// assignment with an unbounded creator is not a document this build will
	// write — but it reports the failure as corrupt data, which is the right
	// verdict for a document read off a ref and an incomprehensible one for
	// somebody whose `user.email` is simply too long.
	if len(s.Actor) > MaxAssignmentPrincipalBytes {
		return AssignResult{}, Errorf(
			CategoryValidation,
			"this repository's identity is %d bytes and must not exceed %d to record an assignment",
			len(s.Actor), MaxAssignmentPrincipalBytes,
		)
	}

	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return AssignResult{}, err
	}
	if err := requireExpectedHead(parent, input.ExpectedHead); err != nil {
		return AssignResult{}, err
	}
	if parent.State.Task.Deleted {
		return AssignResult{}, Errorf(CategoryValidation, "cannot assign a tombstoned task")
	}

	others := AssignmentsHeldByOthers(parent.State.Task.Assignments, principal)
	if _, found := findAssignment(parent.State.Task.Assignments, principal, label); found {
		return AssignResult{
			MutationResult: MutationResult{Task: s.Project(parent)},
			Others:         others,
			Already:        true,
		}, nil
	}
	// The ceiling is asked here and nowhere else; see MaxAssignmentCount for
	// why a fold must never ask it.
	if len(parent.State.Task.Assignments)+1 > MaxAssignmentCount {
		return AssignResult{}, Errorf(
			CategoryValidation,
			"task %s already has %d assignments and must not exceed %d",
			parent.State.TaskID, len(parent.State.Task.Assignments), MaxAssignmentCount,
		)
	}

	// One pack, two operations when the task's stored status also needs
	// settling — which is the composition every other mutation uses, and the
	// reason an assignment operation is an ordinary member of a pack rather
	// than a write of its own. A verb that changes a status and an assignee
	// together joins them here and gets one history entry and one refusal
	// surface.
	operations, corrected := s.settle(
		[]Operation{{Type: OperationAssignAdd, Value: value}},
		parent.State.Task,
	)
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return AssignResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, assignCommitSubject(parent.State.TaskID, "assign", value))
	if err != nil {
		return AssignResult{}, err
	}
	result.StatusCorrected = corrected
	return AssignResult{MutationResult: result, Others: others}, nil
}

// UnassignMutation withdraws an assignment, if this actor may.
//
// The authority check here is the first of the removal rule's two layers, and
// the one a person meets. It refuses before anything is written, names who may
// remove the assignment, and leaves the task exactly as it was. The second
// layer is in Apply, which folds a foreign removal away even when it was never
// offered to this function — see applyAssignRemove.
func (s Service) UnassignMutation(ctx context.Context, idOrPrefix string, input UnassignInput) (MutationResult, error) {
	value, err := s.assignee(input.From)
	if err != nil {
		return MutationResult{}, err
	}
	// Structure only, deliberately: a removal names an assignment the history
	// already holds rather than authoring an identity, and an assignment
	// recorded by something other than this build is exactly the one somebody
	// most needs to be able to withdraw.
	principal, label, err := SplitAssignmentValue(value)
	if err != nil {
		return MutationResult{}, err
	}

	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if err := requireExpectedHead(parent, input.ExpectedHead); err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot unassign a tombstoned task")
	}

	index, found := findAssignment(parent.State.Task.Assignments, principal, label)
	if !found {
		return MutationResult{}, Errorf(
			CategoryValidation,
			"task %s is not assigned to %s",
			parent.State.TaskID, value,
		)
	}
	assignment := parent.State.Task.Assignments[index]
	if !assignment.RemovableBy(s.Actor) {
		return MutationResult{}, Errorf(CategoryValidation, "%s", foreignRemovalMessage(parent.State.TaskID, assignment))
	}

	operations, corrected := s.settle(
		[]Operation{{Type: OperationAssignRemove, Value: value}},
		parent.State.Task,
	)
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, assignCommitSubject(parent.State.TaskID, "unassign", value))
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

// assignee resolves an assignment value, defaulting to the acting identity.
//
// A blank actor is refused rather than defaulted, and refused here rather than
// deeper: the actor becomes the assignment's creator, which is half of the
// removal rule's evidence, so an assignment recorded with no creator would be
// one nobody could ever be shown as entitled to remove.
func (s Service) assignee(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	if strings.TrimSpace(s.Actor) == "" {
		return "", Errorf(CategoryValidation, "cannot resolve the assignee: this repository has no configured identity")
	}
	return s.Actor, nil
}

// foreignRemovalMessage names everybody entitled to remove an assignment.
//
// It is a refusal a person reads and acts on, so it says who to ask rather than
// only that they are not the one. The two clauses collapse when the assignee
// recorded their own assignment, which is the ordinary case and would otherwise
// name the same address twice.
func foreignRemovalMessage(taskID string, assignment Assignment) string {
	if assignment.Creator == assignment.Principal {
		return "assignment " + assignment.Value() + " on task " + taskID +
			" may be removed only by " + assignment.Principal
	}
	return "assignment " + assignment.Value() + " on task " + taskID +
		" may be removed only by " + assignment.Principal +
		" or by " + assignment.Creator + ", who recorded it"
}

func assignCommitSubject(taskID, verb, value string) string {
	return "workbook: " + verb + " " + taskCommitShortID(taskID) + " " + DisplayLine(value)
}
