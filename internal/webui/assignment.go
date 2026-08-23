package webui

import (
	"context"
	"net/http"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The two assignment mutations a board may be built with. Each takes core's own
// single-intent input, the way the thread mutations do, so this package decides
// nothing about what an assignment means: it decodes a request into the input
// `workbook update --assign` fills in from its flags, and both surfaces reach
// the same planner and the same refusals.
type (
	TaskAssigner   func(context.Context, string, core.AssignInput) (core.MutationResult, error)
	TaskUnassigner func(context.Context, string, core.UnassignInput) (core.MutationResult, error)
)

// assignTaskRequest and unassignTaskRequest are the two bodies.
//
// They name the assignment as one value — principal[/label] — rather than as two
// members, because that is the token core parses, the token `workbook show`
// prints, and the token a reader types. Splitting it here would put a second
// copy of core's grammar in this package, and a copy that disagreed about where
// the first slash falls would address a different assignment than the one the
// reader pointed at.
//
// An empty value is this board's own identity, which is what `--assign self`
// means and what the form's placeholder offers. It is passed through empty
// rather than filled in here so that core.Service.assignee resolves the actor
// once: an assignment's creator is half the removal rule's evidence, and a
// second place that decided who was acting could record one nobody may withdraw.
type assignTaskRequest struct {
	To           string `json:"to"`
	ExpectedHead string `json:"expectedHead"`
}

type unassignTaskRequest struct {
	From         string `json:"from"`
	ExpectedHead string `json:"expectedHead"`
}

// assignIdentity is the identity this board would record an assignment against,
// and empty for a board that can record none.
//
// All three of the capability's parts rather than any, for the reason
// Administrable asks for all four of the vocabulary mutations: the section is
// one surface, and a board that could add an assignment but not withdraw one
// would draw two controls that look alike and fail differently. The identity is
// the third part because it is not optional either — an assignment records its
// creator, so a checkout with no configured `user.email` has nothing to write
// one against and the service refuses before anything is staged.
//
// It is the single value the page and the routes both read. The page renders it
// into an attribute the script reads, and taskPresentation derives each row's
// removal rule from it, so a board that draws a withdrawal is a board whose
// service would accept one.
func (handler *handler) assignIdentity() string {
	if handler.Assign == nil || handler.Unassign == nil {
		return ""
	}
	return handler.Identity
}

func (handler *handler) addTaskAssignment(writer http.ResponseWriter, request *http.Request) {
	if handler.Assign == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "assignment is not configured"))
		return
	}
	var body assignTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode assignment", err))
		return
	}
	// OnlyIfUnheld is deliberately never set. The claim gate belongs to an agent
	// selecting work it has not seen; a person assigning from this page is
	// looking at the section that names everybody who already holds the task, so
	// refusing the write would refuse a decision they made with the evidence in
	// front of them. Assignment is additive, exactly as it is on the command
	// line without --force.
	result, err := handler.Assign(request.Context(), taskCollectionID(request, taskAssignmentsPathID), core.AssignInput{
		To:           body.To,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeAssignmentMutation(writer, result)
}

func (handler *handler) removeTaskAssignment(writer http.ResponseWriter, request *http.Request) {
	if handler.Unassign == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "assignment withdrawal is not configured"))
		return
	}
	var body unassignTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode assignment withdrawal", err))
		return
	}
	result, err := handler.Unassign(request.Context(), taskCollectionID(request, taskAssignmentsPathID), core.UnassignInput{
		From:         body.From,
		ExpectedHead: body.ExpectedHead,
	})
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeAssignmentMutation(writer, result)
}

// writeAssignmentMutation answers with the changed task and the section the page
// draws from it.
//
// The section rides along because it is derived rather than read: an assignment
// row's staleness hint is presentation.AssignedAgo's wording and its withdrawal
// is core's removal rule, and neither is anywhere in core.Task. Without it the
// page would either wait a poll tick to show a reader their own assignment or
// compose those two answers itself — which is the second copy of a rule the
// whole presentation contract exists to prevent.
// A withdrawal that leaves nobody holding the task answers `[]` rather than
// leaving the member out: see TaskMutationDocument.Assignments for why the
// difference between "no assignments" and "not about assignments" has to survive
// the wire.
func (handler *handler) writeAssignmentMutation(writer http.ResponseWriter, result core.MutationResult) {
	section := assignmentPresentation(result.Task.Assignments, time.Now(), handler.assignIdentity())
	if section == nil {
		section = []AssignmentPresentation{}
	}
	writeJSON(writer, http.StatusOK, TaskMutationDocument{
		Format:      "workbook.task-mutation",
		Version:     1,
		Task:        result.Task,
		Warnings:    result.Warnings,
		Assignments: &section,
	})
}

// taskAssignmentsPathID reads the task an assignment route addresses.
//
// The collection takes both verbs at one address rather than addressing an
// assignment by name in the path, because an assignment's name contains a
// slash: `dylan@example.com/impl-1` is one value with a separator core owns, and
// a path segment holding it would have to survive a decode this package already
// treats as a malformed request everywhere else. So the value travels in the
// body, where it is exactly the token somebody typed.
func taskAssignmentsPathID(path string) string {
	return taskCollectionPathID(path, "/assignments")
}
