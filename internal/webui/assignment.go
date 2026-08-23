package webui

import (
	"context"
	"net/http"
	"strings"
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
	return strings.TrimSpace(handler.Identity)
}

// assignmentRefusal is why this board can record no assignment, and nil when it
// can.
//
// It asks assignIdentity's question rather than one of its own, so the route and
// the page cannot disagree. A guard that only checked its own closure would
// leave a board wired with one mutation drawing no control and still accepting
// the write behind it: a surface the page deliberately hides is not a surface
// the server should answer. The identity counts for the same reason it counts
// there — an assignment records its creator, and a board with nothing to record
// one against would stage a write nobody could be held to or withdraw.
//
// The category is operational because nothing about the request is wrong: this
// deployment cannot answer, and no rewording of the body will change that.
func (handler *handler) assignmentRefusal(what string) error {
	if handler.assignIdentity() != "" {
		return nil
	}
	if handler.Assign == nil || handler.Unassign == nil {
		return core.Errorf(core.CategoryOperational, "%s is not configured", what)
	}
	return core.Errorf(core.CategoryOperational, "%s needs a configured user.email to record it against", what)
}

func (handler *handler) addTaskAssignment(writer http.ResponseWriter, request *http.Request) {
	if err := handler.assignmentRefusal("assignment"); err != nil {
		handler.writeError(writer, err)
		return
	}
	var body assignTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode assignment", err))
		return
	}
	// OnlyIfUnheld is deliberately never set, and this is the one place the two
	// surfaces diverge. `workbook update --assign` sets it unless --force is
	// given, so the command line refuses a task somebody else holds; this board
	// behaves the way `--assign --force` does, never the way bare `--assign`
	// does. The claim gate belongs to an agent selecting work it has not seen; a
	// person assigning from this page is looking at the section that names
	// everybody who already holds the task, so refusing the write would refuse a
	// decision they made with the evidence in front of them. The divergence is
	// documented in docs/reference.md beside the routes, because a reader whose
	// habits come from the CLI would otherwise expect the refusal.
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
	if err := handler.assignmentRefusal("assignment withdrawal"); err != nil {
		handler.writeError(writer, err)
		return
	}
	// No body at all is the bare verb, exactly as it is on the two comment
	// removals: core.UnassignInput reads an empty From as the acting identity,
	// so `DELETE /api/tasks/{id}/assignments` with nothing in it releases what
	// this checkout holds. The page always sends the value, because it is
	// withdrawing a row a reader pointed at rather than whatever happens to be
	// theirs; an agent releasing its own claim has nothing to spell out.
	var body unassignTaskRequest
	if err := decodeOptionalRequest(request.Body, &body); err != nil {
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
