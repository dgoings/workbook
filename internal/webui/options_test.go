package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// TestNewHandlerRoutesEveryOptionToItsOwnRoute is the whole option-to-route map
// in one place. Delete and Restore share a signature, as do Depend and Free, so
// a transposed pair compiles; every capability here records its own name, and a
// route that reaches the wrong field names the wrong one.
func TestNewHandlerRoutesEveryOptionToItsOwnRoute(t *testing.T) {
	task := boardTasks()[0]
	var called []string
	record := func(name string) core.MutationResult {
		called = append(called, name)
		return core.MutationResult{Task: task}
	}
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			called = append(called, "List")
			return []core.Task{task}, nil
		},
		Create: func(context.Context, core.CreateInput) (core.MutationResult, error) {
			return record("Create"), nil
		},
		Update: func(context.Context, string, core.UpdateInput) (core.MutationResult, error) {
			return record("Update"), nil
		},
		UpdateStatus: func(context.Context, string, core.Status, string) (core.MutationResult, error) {
			return record("UpdateStatus"), nil
		},
		Position: func(context.Context, string, core.PlaceInput) (core.MutationResult, error) {
			return record("Position"), nil
		},
		Delete: func(context.Context, string, core.DeleteInput) (core.MutationResult, error) {
			return record("Delete"), nil
		},
		Restore: func(context.Context, string, core.RestoreInput) (core.MutationResult, error) {
			return record("Restore"), nil
		},
		Depend: func(context.Context, string, string) (core.MutationResult, error) {
			return record("Depend"), nil
		},
		Free: func(context.Context, string, string) (core.MutationResult, error) {
			return record("Free"), nil
		},
		History: func(context.Context, string) (core.TaskDetail, error) {
			called = append(called, "History")
			return historyDetail(), nil
		},
		SyncState: func(context.Context) SyncState {
			called = append(called, "SyncState")
			return SyncState{Mode: SyncModeDeferred, Watcher: true}
		},
		SetSyncMode: func(context.Context, string) (SyncState, error) {
			called = append(called, "SetSyncMode")
			return SyncState{Mode: SyncModeInline}, nil
		},
		SetDisplay: func(context.Context, DisplayChange) (DisplayMutation, error) {
			called = append(called, "SetDisplay")
			return DisplayMutation{}, nil
		},
	})

	dependencyPath := "/api/tasks/" + task.ID + "/dependencies/" + boardTasks()[1].ID
	tests := []struct {
		option string
		method string
		target string
		body   string
	}{
		{option: "List", method: http.MethodGet, target: "/api/tasks"},
		{option: "Create", method: http.MethodPost, target: "/api/tasks", body: `{"title":"New"}`},
		{option: "Update", method: http.MethodPatch, target: "/api/tasks/" + task.ID, body: `{"title":"Renamed"}`},
		{option: "UpdateStatus", method: http.MethodPatch, target: "/api/tasks/" + task.ID + "/status", body: `{"status":"ready"}`},
		{option: "Position", method: http.MethodPatch, target: "/api/tasks/" + task.ID + "/position", body: `{"status":"ready"}`},
		{option: "Delete", method: http.MethodDelete, target: "/api/tasks/" + task.ID},
		{option: "Restore", method: http.MethodPost, target: "/api/tasks/" + task.ID + "/restore"},
		{option: "Depend", method: http.MethodPut, target: dependencyPath},
		{option: "Free", method: http.MethodDelete, target: dependencyPath},
		{option: "History", method: http.MethodGet, target: "/api/tasks/" + task.ID + "/history"},
		{option: "SyncState", method: http.MethodGet, target: "/api/sync"},
		{option: "SetSyncMode", method: http.MethodPut, target: "/api/sync", body: `{"mode":"inline"}`},
		{option: "SetDisplay", method: http.MethodPatch, target: "/api/display", body: `{"expectedHead":""}`},
	}
	for _, test := range tests {
		t.Run(test.option, func(t *testing.T) {
			called = nil
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s = %d, want %d; body = %s", test.method, test.target, response.Code, http.StatusOK, response.Body.String())
			}
			if want := []string{test.option}; !reflect.DeepEqual(called, want) {
				t.Fatalf("%s %s called %v, want %v", test.method, test.target, called, want)
			}
		})
	}
}

// A capability the board was not given is reported rather than assumed, so a
// zero Options value still answers every route. Every mutating route belongs in
// this table: an unguarded one does not return a 500, it panics, and a panic is
// a torn-down connection and a server stack trace rather than the error
// document the rest of the surface returns.
func TestNewHandlerReportsUnconfiguredCapabilities(t *testing.T) {
	task := boardTasks()[0]
	dependencyPath := "/api/tasks/" + task.ID + "/dependencies/" + boardTasks()[1].ID
	handler := NewHandler(Options{List: func(context.Context) ([]core.Task, error) { return nil, nil }})
	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "create", method: http.MethodPost, target: "/api/tasks", body: `{"title":"New"}`},
		{name: "update", method: http.MethodPatch, target: "/api/tasks/" + task.ID, body: `{"title":"Renamed"}`},
		{name: "status", method: http.MethodPatch, target: "/api/tasks/" + task.ID + "/status", body: `{"status":"ready"}`},
		{name: "position", method: http.MethodPatch, target: "/api/tasks/" + task.ID + "/position", body: `{"status":"ready"}`},
		{name: "delete", method: http.MethodDelete, target: "/api/tasks/" + task.ID},
		{name: "restore", method: http.MethodPost, target: "/api/tasks/" + task.ID + "/restore"},
		{name: "depend", method: http.MethodPut, target: dependencyPath},
		{name: "free", method: http.MethodDelete, target: dependencyPath},
		{name: "history", method: http.MethodGet, target: "/api/tasks/" + task.ID + "/history"},
		{name: "sync state", method: http.MethodGet, target: "/api/sync"},
		{name: "sync mode", method: http.MethodPut, target: "/api/sync", body: `{"mode":"inline"}`},
		{name: "display settings", method: http.MethodPatch, target: "/api/display", body: `{"expectedHead":""}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s = %d, want %d; body = %s", test.method, test.target, response.Code, http.StatusInternalServerError, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode %s %s error: %v; body = %s", test.method, test.target, err, response.Body.String())
			}
			if !strings.HasSuffix(document.Error.Message, "is not configured") {
				t.Fatalf("%s %s error = %#v, want an unconfigured-capability report", test.method, test.target, document.Error)
			}
		})
	}
}

// Listing was the one capability the positional constructor made mandatory by
// signature. A named field can simply be left out, so the routes that read
// tasks have to report a missing lister the way every other route reports a
// missing capability, rather than panicking on the first request.
func TestNewHandlerReportsUnconfiguredLister(t *testing.T) {
	handler := NewHandler(Options{})
	for _, target := range []string{"/", "/api/tasks"} {
		response := request(t, handler, http.MethodGet, target)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s = %d, want %d; body = %s", target, response.Code, http.StatusInternalServerError, response.Body.String())
		}
		var document ErrorDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode GET %s error: %v; body = %s", target, err, response.Body.String())
		}
		if document.Error.Message != "task listing is not configured" {
			t.Fatalf("GET %s error = %#v, want an unconfigured-listing report", target, document.Error)
		}
	}
}
