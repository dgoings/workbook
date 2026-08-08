package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestMutatingRoutesRejectAnOversizedRequestBody(t *testing.T) {
	// Production mutation: handing request.Body straight to a JSON decoder lets
	// any client on the loopback interface stream an unbounded value into this
	// process before a single field is validated.
	//
	// Each body is complete, well-formed JSON, so a route that does not bound it
	// decodes successfully and calls its mutation — which the unexpected-*
	// callbacks below fail on. That is what separates rejecting the body from
	// merely failing to parse a truncated one.
	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "create", method: http.MethodPost, target: "/api/tasks", body: oversizedJSONBody("title")},
		{name: "update", method: http.MethodPatch, target: "/api/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7C3", body: oversizedJSONBody("description")},
		{name: "status", method: http.MethodPatch, target: "/api/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7C3/status", body: oversizedJSONBody("status")},
		{name: "position", method: http.MethodPatch, target: "/api/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7C3/position", body: oversizedJSONBody("before")},
		{name: "sync mode", method: http.MethodPut, target: "/api/sync", body: oversizedJSONBody("mode")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandlerWithSyncControl(
				func(context.Context) ([]core.Task, error) { return nil, nil },
				unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
				unexpectedTaskPosition(t), nil, nil, nil, nil, nil,
				func(context.Context) SyncState { return SyncState{Mode: SyncModeInline} },
				func(context.Context, string) (SyncState, error) {
					t.Fatal("unexpected publication mode change")
					return SyncState{}, nil
				},
			)

			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want %d; body = %s", test.method, test.target, response.Code, http.StatusBadRequest, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if document.Error.Category != core.CategoryInvocation {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryInvocation)
			}
			// Only the outermost message reaches the client, so a body stopped
			// by the ceiling has to say so rather than report the route's
			// generic decode context.
			if !strings.Contains(document.Error.Message, strconv.Itoa(MaxRequestBodyBytes)) {
				t.Fatalf("error message = %q, want the request body ceiling named", document.Error.Message)
			}
		})
	}
}

func TestMutatingRoutesAcceptABodyUnderTheCeiling(t *testing.T) {
	// Production mutation: a ceiling set below what the board itself sends would
	// break saving an ordinary task through the UI.
	var created core.CreateInput
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return nil, nil },
		func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			created = input
			return core.MutationResult{Task: core.Task{ID: "WB-01K0M6B8A4FTT8C39MXXYTW7C3"}}, nil
		},
		unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
	)

	description := strings.Repeat("d", core.MaxDescriptionBytes)
	body, err := json.Marshal(map[string]any{"title": "Task", "description": description})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, handler, http.MethodPost, "/api/tasks", string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if len(created.Description) != core.MaxDescriptionBytes {
		t.Fatalf("created description = %d bytes, want %d", len(created.Description), core.MaxDescriptionBytes)
	}
}

func TestBoardServerBoundsHowLongAConnectionMayIdle(t *testing.T) {
	// Production mutation: an http.Server with no timeouts holds a goroutine and
	// a file descriptor per stalled connection until the process exits.
	server := newBoardServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want a positive bound", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout = %v, want a positive bound", server.IdleTimeout)
	}
	if server.ReadTimeout <= 0 {
		t.Fatalf("ReadTimeout = %v, want a positive bound", server.ReadTimeout)
	}
	if server.ReadHeaderTimeout > server.ReadTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want no more than ReadTimeout %v", server.ReadHeaderTimeout, server.ReadTimeout)
	}
	// A write deadline is deliberately absent: a mutation may wait on an inline
	// push to origin, which has no bound this package can name honestly.
	if server.WriteTimeout != time.Duration(0) {
		t.Fatalf("WriteTimeout = %v, want none", server.WriteTimeout)
	}
}

func oversizedJSONBody(field string) string {
	return `{"` + field + `":"` + strings.Repeat("x", MaxRequestBodyBytes) + `"}`
}
