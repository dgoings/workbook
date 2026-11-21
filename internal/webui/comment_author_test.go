package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Which comments a reader may edit is a question about the reader, answered on
// the server for the person on the request's context, exactly as which
// assignments they may remove is.

const (
	authorBobComment   = "01K0M6B8A4FTT8C39MXXYTWB01"
	authorAliceComment = "01K0M6B8A4FTT8C39MXXYTWB02"
	removedBobComment  = "01K0M6B8A4FTT8C39MXXYTWB00"
)

func authoredBoardTask() core.Task {
	task := threadRouteTask()
	stamp := task.CreatedAt
	task.Comments = []core.Comment{
		{ID: authorBobComment, Author: "bob@example.com", Body: "bob's", CreatedAt: stamp},
		{ID: authorAliceComment, Author: "alice@example.com", Body: "alice's", CreatedAt: stamp},
	}
	return task
}

func authoredBoardHandler(t *testing.T, editable bool) http.Handler {
	t.Helper()
	task := authoredBoardTask()
	result := func() (core.MutationResult, error) { return core.MutationResult{Task: task}, nil }
	options := Options{
		List:         func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Identity:     "server@example.test",
		AddComment: func(context.Context, string, core.CommentAddInput) (core.MutationResult, error) {
			return result()
		},
		RemoveComment: func(context.Context, string, core.CommentRemoveInput) (core.MutationResult, error) {
			return result()
		},
		// The reader answers for somebody of its own — everything editable —
		// so a route that passed its answer through would be caught.
		Detail: func(_ context.Context, id string) (core.TaskDetail, error) {
			removedAt := time.Date(2026, time.August, 13, 13, 0, 0, 0, time.UTC)
			detail := core.TaskDetail{Task: task}
			for _, comment := range task.Comments {
				detail.Comments = append(detail.Comments, core.ThreadComment{Comment: comment, Editable: true})
			}
			detail.RemovedComments = []core.RemovedComment{{
				ID: removedBobComment, Author: "bob@example.com", CreatedAt: task.CreatedAt,
				RemovedBy: "alice@example.com", RemovedAt: removedAt,
			}}
			return detail, nil
		},
	}
	if editable {
		options.EditComment = func(context.Context, string, core.CommentEditInput) (core.MutationResult, error) {
			return result()
		}
	}
	return NewHandler(options)
}

// requestAs makes one request whose context names person, the way serve's
// actingAs puts each request's person on it.
func requestAs(t *testing.T, handler http.Handler, method, path, body, person string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if person != "" {
		ctx, err := core.WithActor(request.Context(), person)
		if err != nil {
			t.Fatal(err)
		}
		request = request.WithContext(ctx)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestThePollNamesTheCommentsTheRequestsPersonMayEdit(t *testing.T) {
	t.Parallel()
	handler := authoredBoardHandler(t, true)
	for person, want := range map[string][]string{
		"bob@example.com":     {authorBobComment},
		"alice@example.com":   {authorAliceComment},
		"carol@example.com":   nil,
		"" /* the board's */ : nil,
	} {
		response := requestAs(t, handler, http.MethodGet, "/api/tasks", "", person)
		if response.Code != http.StatusOK {
			t.Fatalf("poll as %q = %d; body = %s", person, response.Code, response.Body)
		}
		var document TasksDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if got := document.Presentation[0].EditableComments; !reflect.DeepEqual(got, want) {
			t.Fatalf("editable as %q = %v, want %v", person, got, want)
		}
	}
}

func TestABoardThatCannotEditCommentsOffersNoEdit(t *testing.T) {
	t.Parallel()
	handler := authoredBoardHandler(t, false)
	response := requestAs(t, handler, http.MethodGet, "/api/tasks", "", "bob@example.com")
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if got := document.Presentation[0].EditableComments; got != nil {
		t.Fatalf("editable on a board without the edit route = %v, want none", got)
	}

	response = requestAs(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID, "", "bob@example.com")
	var detail TaskDocument
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	for _, comment := range detail.Task.Comments {
		if comment.Editable {
			t.Fatalf("task answer on a board without the edit route = %#v, want nothing editable", detail.Task.Comments)
		}
	}
}

func TestTheTaskAnswerCarriesTheReadersThreadAndItsRemovals(t *testing.T) {
	t.Parallel()
	handler := authoredBoardHandler(t, true)
	response := requestAs(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID, "", "alice@example.com")
	if response.Code != http.StatusOK {
		t.Fatalf("task answer = %d; body = %s", response.Code, response.Body)
	}
	var document TaskDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Format != "workbook.task" || document.Version != 1 {
		t.Fatalf("envelope = %q v%d", document.Format, document.Version)
	}
	editable := map[string]bool{}
	for _, comment := range document.Task.Comments {
		editable[comment.ID] = comment.Editable
	}
	if want := map[string]bool{authorBobComment: false, authorAliceComment: true}; !reflect.DeepEqual(editable, want) {
		t.Fatalf("editable as alice = %v, want %v", editable, want)
	}
	if len(document.Task.RemovedComments) != 1 || document.Task.RemovedComments[0].RemovedBy != "alice@example.com" {
		t.Fatalf("removed = %#v, want the removal by alice", document.Task.RemovedComments)
	}
}

func TestTheTaskAnswerReportsAnUnconfiguredReader(t *testing.T) {
	t.Parallel()
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return nil, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
	response := requestAs(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID, "", "")
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "task detail is not configured") {
		t.Fatalf("unconfigured task answer = %d; body = %s", response.Code, response.Body)
	}
}

// Each comment route's answer says which comments the request's person may
// edit, so a page that has just added a comment draws its Edit at once.
func TestCommentRoutesAnswerWithTheReadersEditableComments(t *testing.T) {
	t.Parallel()
	handler := authoredBoardHandler(t, true)
	for _, testCase := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/tasks/" + threadRouteTaskID + "/comments", `{"body":"another"}`},
		{http.MethodPatch, "/api/tasks/" + threadRouteTaskID + "/comments/" + authorBobComment, `{"body":"revised"}`},
		{http.MethodDelete, "/api/tasks/" + threadRouteTaskID + "/comments/" + authorAliceComment, ""},
	} {
		response := requestAs(t, handler, testCase.method, testCase.path, testCase.body, "bob@example.com")
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s = %d; body = %s", testCase.method, testCase.path, response.Code, response.Body)
		}
		var document TaskMutationDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if document.EditableComments == nil || !reflect.DeepEqual(*document.EditableComments, []string{authorBobComment}) {
			t.Fatalf("%s %s editableComments = %v, want bob's alone", testCase.method, testCase.path, document.EditableComments)
		}
	}
}
