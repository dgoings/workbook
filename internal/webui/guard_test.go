package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// guardedHandler wraps a full board handler whose list and create callbacks
// count their calls, so a test can prove a rejected request never reached the
// service layer.
func guardedHandler(t *testing.T, boundAddr string) (http.Handler, *int, *int) {
	t.Helper()
	lists := new(int)
	creates := new(int)
	created := boardTasks()[0]
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) {
			*lists++
			return boardTasks(), nil
		},
		func(context.Context, core.CreateInput) (core.MutationResult, error) {
			*creates++
			return core.MutationResult{Task: created}, nil
		},
		unexpectedTaskUpdate(t),
		unexpectedStatusUpdate(t),
	)
	return GuardSameOrigin(handler, boundAddr), lists, creates
}

func guardedRequest(t *testing.T, handler http.Handler, method, target, host, origin, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Host = host
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertGuardRejection(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	assertSecurityHeaders(t, response.Result())
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode rejection body: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryValidation {
		t.Fatalf("rejection envelope = %#v, want workbook.error v1 with category %q", document, core.CategoryValidation)
	}
	if document.Error.Message == "" {
		t.Fatal("rejection message is empty")
	}
}

func TestGuardRejectsForeignHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{name: "rebound DNS name on a GET", host: "evil.example:7331"},
		{name: "rebound DNS name without a port", host: "evil.example"},
		{name: "loopback with the wrong port", host: "127.0.0.1:9999"},
		{name: "loopback without a port", host: "127.0.0.1"},
		{name: "localhost subdomain trick", host: "localhost.evil.example:7331"},
		{name: "empty host", host: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, _ := guardedHandler(t, "127.0.0.1:7331")
			response := guardedRequest(t, handler, http.MethodGet, "/api/tasks", test.host, "", "", "")
			assertGuardRejection(t, response, http.StatusForbidden)
			if *lists != 0 {
				t.Fatalf("task lister ran %d time(s) for a foreign Host", *lists)
			}
		})
	}
}

func TestGuardAllowsTheBoundLoopbackHostAndItsAliases(t *testing.T) {
	tests := []struct {
		name      string
		boundAddr string
		host      string
	}{
		{name: "bound address", boundAddr: "127.0.0.1:7331", host: "127.0.0.1:7331"},
		{name: "localhost alias", boundAddr: "127.0.0.1:7331", host: "localhost:7331"},
		{name: "case-insensitive localhost", boundAddr: "127.0.0.1:7331", host: "LocalHost:7331"},
		{name: "IPv6 loopback alias", boundAddr: "127.0.0.1:7331", host: "[::1]:7331"},
		{name: "IPv6 bound address", boundAddr: "[::1]:7331", host: "[::1]:7331"},
		{name: "IPv4 alias of an IPv6 bind", boundAddr: "[::1]:7331", host: "127.0.0.1:7331"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, _ := guardedHandler(t, test.boundAddr)
			response := guardedRequest(t, handler, http.MethodGet, "/api/tasks", test.host, "", "", "")
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			if *lists != 1 {
				t.Fatalf("task lister ran %d time(s), want 1", *lists)
			}
		})
	}
}

func TestGuardRejectsForeignOrigins(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		origin string
		body   string
	}{
		{name: "cross-origin create", method: http.MethodPost, target: "/api/tasks", origin: "https://evil.example", body: `{"title":"injected"}`},
		{name: "same host on the wrong scheme", method: http.MethodPost, target: "/api/tasks", origin: "https://127.0.0.1:7331", body: `{"title":"injected"}`},
		{name: "foreign origin on the board port", method: http.MethodPost, target: "/api/tasks", origin: "http://evil.example:7331", body: `{"title":"injected"}`},
		{name: "opaque null origin", method: http.MethodPost, target: "/api/tasks", origin: "null", body: `{"title":"injected"}`},
		{name: "cross-origin read", method: http.MethodGet, target: "/api/tasks", origin: "http://evil.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, creates := guardedHandler(t, "127.0.0.1:7331")
			response := guardedRequest(t, handler, test.method, test.target, "127.0.0.1:7331", test.origin, "application/json", test.body)
			assertGuardRejection(t, response, http.StatusForbidden)
			if *lists != 0 || *creates != 0 {
				t.Fatalf("service callbacks ran (%d lists, %d creates) for a foreign Origin", *lists, *creates)
			}
		})
	}
}

func TestGuardAllowsTheBoardsOwnOrigin(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:7331", "http://localhost:7331"} {
		t.Run(origin, func(t *testing.T) {
			handler, _, creates := guardedHandler(t, "127.0.0.1:7331")
			response := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "127.0.0.1:7331", origin, "application/json", `{"title":"own origin"}`)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			if *creates != 1 {
				t.Fatalf("task creator ran %d time(s), want 1", *creates)
			}
		})
	}
}

func TestGuardRequiresJSONMediaTypeOnMutations(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "text plain form POST", contentType: "text/plain", body: `{"title":"CSRF TEXT PLAIN PWN"}`},
		{name: "urlencoded form POST", contentType: "application/x-www-form-urlencoded", body: `title=injected`},
		{name: "multipart form POST", contentType: "multipart/form-data; boundary=x", body: "--x--"},
		{name: "missing content type", contentType: "", body: `{"title":"injected"}`},
		{name: "unparseable content type", contentType: ";", body: `{"title":"injected"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _, creates := guardedHandler(t, "127.0.0.1:7331")
			response := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "127.0.0.1:7331", "", test.contentType, test.body)
			assertGuardRejection(t, response, http.StatusUnsupportedMediaType)
			if *creates != 0 {
				t.Fatalf("task creator ran %d time(s) for %q", *creates, test.contentType)
			}
		})
	}
}

func TestGuardAcceptsJSONMediaTypeVariantsAndSparesReads(t *testing.T) {
	handler, lists, creates := guardedHandler(t, "127.0.0.1:7331")

	response := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "127.0.0.1:7331", "", "application/json; charset=utf-8", `{"title":"charset"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST with charset status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if *creates != 1 {
		t.Fatalf("task creator ran %d time(s), want 1", *creates)
	}

	response = guardedRequest(t, handler, http.MethodGet, "/api/tasks", "127.0.0.1:7331", "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET without Content-Type status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if *lists != 1 {
		t.Fatalf("task lister ran %d time(s), want 1", *lists)
	}
}

func TestGuardRequiresJSONMediaTypeOnBodyLessMutations(t *testing.T) {
	dependent := boardTasks()[0]
	prerequisite := boardTasks()[1]
	dependencyCalls := 0
	inner := NewHandlerWithTaskMutations(
		func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
		unexpectedTaskPosition(t), nil, nil,
		func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
		func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
		nil,
	)
	handler := GuardSameOrigin(inner, "127.0.0.1:7331")
	path := "/api/tasks/" + dependent.ID + "/dependencies/" + prerequisite.ID

	bare := guardedRequest(t, handler, http.MethodPut, path, "127.0.0.1:7331", "", "", "")
	assertGuardRejection(t, bare, http.StatusUnsupportedMediaType)
	if dependencyCalls != 0 {
		t.Fatalf("dependency callbacks = %d, want 0 without a media type", dependencyCalls)
	}

	declared := guardedRequest(t, handler, http.MethodPut, path, "127.0.0.1:7331", "", "application/json", "")
	if declared.Code != http.StatusOK {
		t.Fatalf("PUT dependency status = %d, want %d; body = %s", declared.Code, http.StatusOK, declared.Body.String())
	}
	if dependencyCalls != 1 {
		t.Fatalf("dependency callbacks = %d, want 1", dependencyCalls)
	}
}

func TestGuardOnNonLoopbackBindChecksPortAndSelfOrigin(t *testing.T) {
	// A deliberately exposed bind cannot know which names reach it, so the
	// guard pins the port and requires any Origin to name the same authority
	// the browser addressed. The missing authentication is warned about when
	// the listener opens.
	handler, lists, creates := guardedHandler(t, "0.0.0.0:7331")

	allowed := guardedRequest(t, handler, http.MethodGet, "/api/tasks", "192.168.1.5:7331", "", "", "")
	if allowed.Code != http.StatusOK || *lists != 1 {
		t.Fatalf("LAN host status/lists = %d/%d, want %d/1; body = %s", allowed.Code, *lists, http.StatusOK, allowed.Body.String())
	}

	wrongPort := guardedRequest(t, handler, http.MethodGet, "/api/tasks", "192.168.1.5:9999", "", "", "")
	assertGuardRejection(t, wrongPort, http.StatusForbidden)

	selfOrigin := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "192.168.1.5:7331", "http://192.168.1.5:7331", "application/json", `{"title":"self"}`)
	if selfOrigin.Code != http.StatusOK || *creates != 1 {
		t.Fatalf("self-origin status/creates = %d/%d, want %d/1; body = %s", selfOrigin.Code, *creates, http.StatusOK, selfOrigin.Body.String())
	}

	foreignOrigin := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "192.168.1.5:7331", "http://evil.example:7331", "application/json", `{"title":"injected"}`)
	assertGuardRejection(t, foreignOrigin, http.StatusForbidden)
	if *creates != 1 {
		t.Fatalf("task creator ran %d time(s), want the foreign origin rejected", *creates)
	}
}
