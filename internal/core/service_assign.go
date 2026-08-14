package core

import (
	"context"
	"strings"
)

// AssignmentChange is one assignment intent riding on an update.
//
// Two shapes: an addition, which is a value or an empty one meaning the acting
// identity, and a withdrawal, which is the same with Remove. It is a member of
// UpdateInput rather than the input of a write of its own for the reason
// CommentChange is: `update X --status in-progress --assign self` has to be one
// pack, one commit and one refusal, and the only way to guarantee that is for
// every operation to be built into the same pack.
type AssignmentChange struct {
	// To is the assignment as principal[/label]. An empty value means the
	// acting identity, which is the self-assignment the claim path is built on:
	// an agent that assigns itself never has to know how its own identity is
	// spelled, and cannot get it wrong.
	To string
	// Remove turns the change into a withdrawal.
	Remove bool
	// OnlyIfUnheld refuses the whole update when the task already carries an
	// assignment under another principal, instead of recording this one beside
	// theirs.
	//
	// This is the claim behavior, and it is a property of the verb rather than
	// of the data: nothing in the fold enforces it, no lease is taken, and a
	// caller that does not ask for it records an assignment beside anybody
	// else's exactly as before. An orchestrator deliberately pairing two agents
	// on one task simply leaves it unset, which is also what --force does.
	//
	// The gate is decided from the parent this mutation already read, so the
	// check and the append see one task rather than two reads a race can slip
	// between. What it cannot cover is the window between this write and the
	// push that publishes it; that race ends in the both-assigned state, which
	// the design calls a spike and a meaningful outcome, and the result's
	// Others is how the caller hears about it.
	//
	// It never fires on an assignment the task already carries: re-adding what
	// is already there writes nothing, and refusing a no-op would tell an agent
	// to go away from work it already holds.
	OnlyIfUnheld bool
}

// AssignInput names who a task is being assigned to. It is the single-intent
// door on to the same machinery `update --assign` uses.
type AssignInput struct {
	// To is the assignee as principal[/label], with an empty value meaning the
	// acting identity; see AssignmentChange.To.
	To string
	// OnlyIfUnheld carries the meaning it has on AssignmentChange.
	OnlyIfUnheld bool
	// ExpectedHead carries the same meaning it does on UpdateInput.
	ExpectedHead string
}

// UnassignInput names the assignment being withdrawn. Its zero From is the
// acting identity, the same way AssignInput's To is — an agent releases what it
// holds without spelling itself out.
type UnassignInput struct {
	From         string
	ExpectedHead string
}

// AssignMutation records an assignment on a task.
//
// Assignment is additive and always overrideable by addition: this never
// removes anybody's assignment, and it refuses because somebody else holds the
// task only when the caller asked it to with AssignInput.OnlyIfUnheld. Two
// agents forcing their way to the same work therefore terminate in the
// both-assigned state — a spike, which is a meaningful outcome — instead of
// removing each other's claims forever.
func (s Service) AssignMutation(ctx context.Context, idOrPrefix string, input AssignInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Assignments:  []AssignmentChange{{To: input.To, OnlyIfUnheld: input.OnlyIfUnheld}},
	})
}

// UnassignMutation withdraws an assignment, if this actor may.
//
// The authority check is the first of the removal rule's two layers, and the
// one a person meets. It refuses before anything is written, names who may
// remove the assignment, and leaves the task exactly as it was. The second
// layer is in Apply, which folds a foreign removal away even when it was never
// offered to this function — see applyAssignRemove.
func (s Service) UnassignMutation(ctx context.Context, idOrPrefix string, input UnassignInput) (MutationResult, error) {
	return s.UpdateMutation(ctx, idOrPrefix, UpdateInput{
		ExpectedHead: input.ExpectedHead,
		Assignments:  []AssignmentChange{{To: input.From, Remove: true}},
	})
}

// assignmentOperations turns an update's assignment intents into the operations
// that carry them, and reports what they landed beside.
//
// This is the mutation boundary the design puts every assignment refusal at,
// which is deliberately not where the fold puts its own: a value the boundary
// would never have authored is still folded when it arrives from a peer, and a
// removal this refuses is folded away rather than rejected. The difference is
// the one ValidateAssigneeAuthoring draws — somebody is choosing a value here,
// and a person who typed a bad one can be told so.
//
// Others and Already are described on MutationResult, which is where they end
// up. With one addition, which is every invocation the command line produces,
// they reduce to "the assignments somebody else holds" and "you already held
// this one".
func (s Service) assignmentOperations(taskID string, task TaskData, changes []AssignmentChange) ([]Operation, []Assignment, bool, error) {
	if len(changes) == 0 {
		return nil, nil, false, nil
	}
	operations := make([]Operation, 0, len(changes))
	principals := make(map[string]struct{}, len(changes))
	added, existing := 0, 0
	for _, change := range changes {
		value, err := s.assignee(change.To)
		if err != nil {
			return nil, nil, false, err
		}
		if change.Remove {
			operation, err := s.assignRemoveOperation(taskID, task, value)
			if err != nil {
				return nil, nil, false, err
			}
			operations = append(operations, operation)
			continue
		}
		principal, label, err := s.assignAddPrincipal(value)
		if err != nil {
			return nil, nil, false, err
		}
		principals[principal] = struct{}{}
		added++
		if _, found := findAssignment(task.Assignments, principal, label); found {
			existing++
			continue
		}
		if others := AssignmentsHeldByOthers(task.Assignments, principal); change.OnlyIfUnheld && len(others) > 0 {
			return nil, nil, false, Errorf(
				CategoryAssigned,
				"task %s is already assigned to %s",
				taskID, strings.Join(assignmentValues(others), ", "),
			)
		}
		// The ceiling is asked here and nowhere else; see MaxAssignmentCount for
		// why a fold must never ask it. Additions already planned in this pack
		// count against it, because they are as much a part of the task's
		// assignment list as the ones already stored.
		if len(task.Assignments)+len(operations)+1 > MaxAssignmentCount {
			return nil, nil, false, Errorf(
				CategoryValidation,
				"task %s already has %d assignments and must not exceed %d",
				taskID, len(task.Assignments), MaxAssignmentCount,
			)
		}
		operations = append(operations, Operation{Type: OperationAssignAdd, Value: value})
	}
	if added == 0 {
		return operations, nil, false, nil
	}
	others := make([]Assignment, 0, len(task.Assignments))
	for _, assignment := range task.Assignments {
		if _, mine := principals[assignment.Principal]; !mine {
			others = append(others, assignment)
		}
	}
	if len(others) == 0 {
		others = nil
	}
	return operations, others, added == existing, nil
}

// assignAddPrincipal checks an assignment value somebody is authoring and
// returns its parts.
//
// The acting identity is bounded here rather than only inside the fold. The
// fold does bound it — a stored assignment with an unbounded creator is not a
// document this build will write — but it reports the failure as corrupt data,
// which is the right verdict for a document read off a ref and an
// incomprehensible one for somebody whose `user.email` is simply too long.
func (s Service) assignAddPrincipal(value string) (principal, label string, err error) {
	if err := ValidateAssigneeAuthoring(value); err != nil {
		return "", "", err
	}
	principal, label, err = SplitAssignmentValue(value)
	if err != nil {
		return "", "", err
	}
	if len(s.Actor) > MaxAssignmentPrincipalBytes {
		return "", "", Errorf(
			CategoryValidation,
			"this repository's identity is %d bytes and must not exceed %d to record an assignment",
			len(s.Actor), MaxAssignmentPrincipalBytes,
		)
	}
	return principal, label, nil
}

// assignRemoveOperation checks that this actor may withdraw the assignment a
// value names, and builds the operation that does.
//
// The value is checked for structure only, deliberately: a removal names an
// assignment the history already holds rather than authoring an identity, and
// an assignment recorded by something other than this build is exactly the one
// somebody most needs to be able to withdraw.
func (s Service) assignRemoveOperation(taskID string, task TaskData, value string) (Operation, error) {
	principal, label, err := SplitAssignmentValue(value)
	if err != nil {
		return Operation{}, err
	}
	index, found := findAssignment(task.Assignments, principal, label)
	if !found {
		return Operation{}, Errorf(CategoryValidation, "task %s is not assigned to %s", taskID, value)
	}
	if assignment := task.Assignments[index]; !assignment.RemovableBy(s.Actor) {
		return Operation{}, Errorf(CategoryValidation, "%s", foreignRemovalMessage(taskID, assignment))
	}
	return Operation{Type: OperationAssignRemove, Value: value}, nil
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

// assignmentChangeSubjects names the assignments a pack moved, for the commit
// subject. `git log` over a task ref is how somebody reconstructs a claim they
// cannot otherwise explain, so the value is spelled out rather than counted the
// way comments are: there is one of them in practice, and which identity it was
// is the whole content.
func assignmentChangeSubjects(operations []Operation) []string {
	subjects := make([]string, 0, len(operations))
	for _, operation := range operations {
		switch operation.Type {
		case OperationAssignAdd:
			subjects = append(subjects, "assign "+DisplayLine(operation.Value))
		case OperationAssignRemove:
			subjects = append(subjects, "unassign "+DisplayLine(operation.Value))
		}
	}
	return subjects
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
