package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The restore route gained a body, and the whole point of its being optional is
// that a client which never learned about it keeps working. A request with no
// body at all is the bare verb, and reaches the service with nothing set.
func TestHandlerRestoresWithoutABody(t *testing.T) {
	restored := boardTasks()[1]
	var gotID string
	var gotInput core.RestoreInput
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Restore: func(_ context.Context, id string, input core.RestoreInput) (core.MutationResult, error) {
			gotID = id
			gotInput = input
			return core.MutationResult{Task: restored}, nil
		},
	})

	response := request(t, handler, http.MethodPost, "/api/tasks/"+restored.ID+"/restore")
	if response.Code != http.StatusOK {
		t.Fatalf("POST restore status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if gotID != restored.ID || gotInput != (core.RestoreInput{}) {
		t.Fatalf("restore callback = %q/%#v, want %q and no destination", gotID, gotInput, restored.ID)
	}
	assertTaskMutationDocument(t, response, restored)
}

func TestHandlerRestoresIntoAStatus(t *testing.T) {
	restored := boardTasks()[1]
	var gotInput core.RestoreInput
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Restore: func(_ context.Context, _ string, input core.RestoreInput) (core.MutationResult, error) {
			gotInput = input
			return core.MutationResult{Task: restored}, nil
		},
	})

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+restored.ID+"/restore",
		`{"status":"in-progress","after":"WB-01J00000000000000000000003","expectedHead":"headsha"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST restore status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	want := core.RestoreInput{Into: core.StatusInProgress, After: "WB-01J00000000000000000000003", ExpectedHead: "headsha"}
	if gotInput != want {
		t.Fatalf("restore callback input = %#v, want %#v", gotInput, want)
	}
	assertTaskMutationDocument(t, response, restored)
}

// A body that is present is held to exactly what the other mutation routes
// demand of theirs. Absent and malformed are different requests, and only the
// first one is the bare verb.
func TestHandlerRejectsMalformedRestoreBodies(t *testing.T) {
	const taskID = "WB-01J00000000000000000000001"
	tests := map[string]string{
		"truncated":      `{"status":`,
		"unknown member": `{"rank":"1/1"}`,
		"a second value": `{"status":"ready"}{"status":"done"}`,
		"not an object":  `"ready"`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			handler := NewHandler(Options{
				List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
				Create:       unexpectedTaskCreate(t),
				Update:       unexpectedTaskUpdate(t),
				UpdateStatus: unexpectedStatusUpdate(t),
				Restore: func(context.Context, string, core.RestoreInput) (core.MutationResult, error) {
					t.Fatal("restore reached the service with a malformed body")
					return core.MutationResult{}, nil
				},
			})

			response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+taskID+"/restore", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("POST restore status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode restore error: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.error" || document.Version != 1 ||
				document.Error.Category != core.CategoryInvocation {
				t.Fatalf("restore error document = %#v, want workbook.error v1 invocation", document)
			}
		})
	}
}

// A restore refused as stale is reported as the conflict it is, so a board that
// queued the intent can tell "somebody got there first" from "this request was
// wrong".
func TestHandlerReportsARefusedRestore(t *testing.T) {
	const taskID = "WB-01J00000000000000000000001"
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Restore: func(context.Context, string, core.RestoreInput) (core.MutationResult, error) {
			return core.MutationResult{}, core.Errorf(core.CategoryStaleWrite,
				"task %s has changed since abc; reload and try again", taskID)
		},
	})

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+taskID+"/restore",
		`{"status":"ready","expectedHead":"abc"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("POST restore status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode restore error: %v; body = %s", err, response.Body.String())
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("restore error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
}

// Delete carries an expected head the same optional way, because a drag into
// the deleted column is a queued intent like any other and has to be refusable
// when the card it names has moved on.
func TestHandlerDeletesWithAnOptionalExpectedHead(t *testing.T) {
	deleted := boardTasks()[0]
	deleted.Deleted = true
	var gotInput core.DeleteInput
	handler := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Delete: func(_ context.Context, _ string, input core.DeleteInput) (core.MutationResult, error) {
			gotInput = input
			return core.MutationResult{Task: deleted}, nil
		},
	})

	response := request(t, handler, http.MethodDelete, "/api/tasks/"+deleted.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if gotInput != (core.DeleteInput{}) {
		t.Fatalf("delete callback input = %#v, want nothing set", gotInput)
	}

	response = requestJSON(t, handler, http.MethodDelete, "/api/tasks/"+deleted.ID, `{"expectedHead":"headsha"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE with a head status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got, want := gotInput, (core.DeleteInput{ExpectedHead: "headsha"}); got != want {
		t.Fatalf("delete callback input = %#v, want %#v", got, want)
	}

	refusing := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Delete: func(context.Context, string, core.DeleteInput) (core.MutationResult, error) {
			t.Fatal("delete reached the service with a malformed body")
			return core.MutationResult{}, nil
		},
	})
	response = requestJSON(t, refusing, http.MethodDelete, "/api/tasks/"+deleted.ID, `{"head":"headsha"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("DELETE with an unknown member status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
