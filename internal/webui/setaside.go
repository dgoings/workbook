package webui

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dgoings/workbook/internal/core"
)

// SetAsideReader reads, by task, the edits this server's clone made that a
// reconciliation set aside on a conflict and did not replay (see
// core.SetAsideEdit). It is read from the clone's parked refs on every call,
// so what it reports survives a restart of the server. heads is each listed
// task's head, which the parked tips are compared with, so the answer agrees
// with the tasks it is drawn beside.
type SetAsideReader func(ctx context.Context, heads map[string]string) (map[string][]core.SetAsideEdit, error)

// SetAsideDismisser removes the parked tip a task's set-aside edits are held
// in, while it still points at the commit the person was shown.
type SetAsideDismisser func(ctx context.Context, taskID, parkedRef, parked string) error

// dismissSetAsideRequest names the parked tip a person dismissed: the ref and
// the commit it held when the page was drawn, from the set-aside edit's own
// parkedRef and parked members.
type dismissSetAsideRequest struct {
	ParkedRef string `json:"parkedRef"`
	Parked    string `json:"parked"`
}

// SetAsideDismissalDocument answers a dismissal. It carries no task, because
// a dismissal changes no task: the edits it removes were never in the task's
// history.
type SetAsideDismissalDocument struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	TaskID    string `json:"taskId"`
	ParkedRef string `json:"parkedRef"`
}

func taskSetAsidePathID(path string) string {
	return taskCollectionPathID(path, "/set-aside")
}

// setAsideEdits is the reader's answer for the tasks listed, or nothing for a
// board built without one.
func (handler *handler) setAsideEdits(request *http.Request, tasks []core.Task) (map[string][]core.SetAsideEdit, error) {
	if handler.SetAside == nil {
		return nil, nil
	}
	heads := make(map[string]string, len(tasks))
	for _, task := range tasks {
		heads[task.ID] = task.Head
	}
	return handler.SetAside(request.Context(), heads)
}

// dismissSetAside answers DELETE /api/tasks/{id}/set-aside.
//
// Anyone who can open the board can dismiss, as anyone can remove a comment:
// the edits are shown to everybody, so whoever deals with them may say so.
// The server's log records who dismissed what, and names the commit, which
// stays in the clone's object store until Git collects it.
func (handler *handler) dismissSetAside(writer http.ResponseWriter, request *http.Request) {
	if handler.DismissSetAside == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "dismissing set-aside edits is not configured"))
		return
	}
	var body dismissSetAsideRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode set-aside dismissal", err))
		return
	}
	if body.ParkedRef == "" || body.Parked == "" {
		handler.writeError(writer, core.Errorf(core.CategoryValidation, "a dismissal names the set-aside edits' parkedRef and parked commit"))
		return
	}
	taskID := taskCollectionID(request, taskSetAsidePathID)
	if err := handler.DismissSetAside(request.Context(), taskID, body.ParkedRef, body.Parked); err != nil {
		handler.writeError(writer, err)
		return
	}
	if handler.ServerLog != nil {
		person := handler.requestPerson(request)
		if person == "" {
			person = "someone"
		}
		_, _ = fmt.Fprintf(handler.ServerLog, "workbook serve: %s dismissed the set-aside edits of %s held at %s (commit %s)\n",
			person, taskID, body.ParkedRef, body.Parked)
	}
	writeJSON(writer, http.StatusOK, SetAsideDismissalDocument{
		Format:    "workbook.set-aside-dismissal",
		Version:   1,
		TaskID:    taskID,
		ParkedRef: body.ParkedRef,
	})
}
