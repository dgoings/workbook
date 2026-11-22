package webui

import (
	"net/http"

	"github.com/dgoings/workbook/internal/core"
)

// commentIdentity is the person whose comments this request may edit, and
// empty for a board that edits none.
//
// It is requestPerson gated on the edit route existing, the way assignIdentity
// is gated on the assignment routes: a board that cannot edit a comment must
// not draw an Edit that its own route would answer as unconfigured.
func (handler *handler) commentIdentity(request *http.Request) string {
	if handler.EditComment == nil {
		return ""
	}
	return handler.requestPerson(request)
}

// editableComments names the comments person may edit, in thread order, and
// nil when there are none. core.Comment.EditableBy is the rule; nothing here
// restates it.
func editableComments(comments []core.Comment, person string) []string {
	if person == "" {
		return nil
	}
	var editable []string
	for _, comment := range comments {
		if comment.EditableBy(person) {
			editable = append(editable, comment.ID)
		}
	}
	return editable
}

// writeCommentMutation answers a comment route with the changed task and which
// of its comments the request's person may edit, so the page that just added
// a comment draws its Edit without waiting for the next poll.
func (handler *handler) writeCommentMutation(writer http.ResponseWriter, request *http.Request, result core.MutationResult) {
	editable := editableComments(result.Task.Comments, handler.commentIdentity(request))
	if editable == nil {
		editable = []string{}
	}
	writeJSON(writer, http.StatusOK, TaskMutationDocument{
		Format:           "workbook.task-mutation",
		Version:          1,
		Task:             result.Task,
		Warnings:         handler.disclosed(result.Warnings),
		EditableComments: &editable,
	})
}

// serveTaskDetail answers GET /api/tasks/{id} with the task as `workbook show
// --json` gives it.
//
// Each comment's `editable` is answered here, for the request's person and
// only if this board can edit comments, rather than taken from the reader: the
// detail's own answer is for whoever the service would act as, which on a
// board that cannot edit is still somebody, and an `editable: true` that the
// edit route then refused would be a promise this board does not keep.
func (handler *handler) serveTaskDetail(writer http.ResponseWriter, request *http.Request) {
	if handler.Detail == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task detail is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskPathID(request.URL.Path)
	}
	detail, err := handler.Detail(request.Context(), id)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	person := handler.commentIdentity(request)
	for index := range detail.Comments {
		detail.Comments[index].Editable = detail.Comments[index].EditableBy(person)
	}
	writeJSON(writer, http.StatusOK, TaskDocument{Format: "workbook.task", Version: 1, Task: detail})
}
