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
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			*lists++
			return boardTasks(), nil
		},
		Create: func(context.Context, core.CreateInput) (core.MutationResult, error) {
			*creates++
			return core.MutationResult{Task: created}, nil
		},
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
	})
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
	inner := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Position:     unexpectedTaskPosition(t),
		Depend: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
		Free: func(context.Context, string, string) (core.MutationResult, error) {
			dependencyCalls++
			return core.MutationResult{Task: dependent}, nil
		},
	})
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

func TestGuardPinsHostOnExplicitNonLoopbackBind(t *testing.T) {
	// A bind to one address knows exactly which address reaches it, so a Host
	// that is not that address is refused the same way a foreign Host on a
	// loopback bind is. A rebound name that resolves to the board is the attack
	// this closes: the browser sends the attacker's name, not the board's
	// address.
	tests := []struct {
		name      string
		boundAddr string
		host      string
	}{
		{name: "rebound DNS name", boundAddr: "192.168.1.5:7331", host: "evil.example:7331"},
		{name: "name that resolves to the bind", boundAddr: "192.168.1.5:7331", host: "board.internal:7331"},
		{name: "another address on the board port", boundAddr: "192.168.1.5:7331", host: "192.168.1.6:7331"},
		{name: "loopback is not the bound address", boundAddr: "192.168.1.5:7331", host: "127.0.0.1:7331"},
		{name: "bound address on the wrong port", boundAddr: "192.168.1.5:7331", host: "192.168.1.5:9999"},
		{name: "bound address without a port", boundAddr: "192.168.1.5:7331", host: "192.168.1.5"},
		{name: "foreign name against an IPv6 bind", boundAddr: "[2001:db8::5]:7331", host: "evil.example:7331"},
		{name: "another address against an IPv6 bind", boundAddr: "[2001:db8::5]:7331", host: "[2001:db8::6]:7331"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, creates := guardedHandler(t, test.boundAddr)

			read := guardedRequest(t, handler, http.MethodGet, "/api/tasks", test.host, "", "", "")
			assertGuardRejection(t, read, http.StatusForbidden)

			write := guardedRequest(t, handler, http.MethodPost, "/api/tasks", test.host, "http://"+test.host, "application/json", `{"title":"injected"}`)
			assertGuardRejection(t, write, http.StatusForbidden)

			if *lists != 0 || *creates != 0 {
				t.Fatalf("service callbacks ran (%d lists, %d creates) for Host %q against a bind at %q", *lists, *creates, test.host, test.boundAddr)
			}
		})
	}
}

func TestGuardAllowsTheBoundExplicitAddress(t *testing.T) {
	// The bound address is compared as an address, so the spellings a browser
	// or a listener may choose for the same one are the same host.
	tests := []struct {
		name      string
		boundAddr string
		host      string
	}{
		{name: "bound address", boundAddr: "192.168.1.5:7331", host: "192.168.1.5:7331"},
		{name: "fully qualified trailing dot", boundAddr: "192.168.1.5:7331", host: "192.168.1.5.:7331"},
		{name: "IPv4-mapped IPv6 spelling", boundAddr: "192.168.1.5:7331", host: "[::ffff:192.168.1.5]:7331"},
		{name: "IPv6 bound address", boundAddr: "[2001:db8::5]:7331", host: "[2001:db8::5]:7331"},
		{name: "expanded IPv6 spelling", boundAddr: "[2001:db8::5]:7331", host: "[2001:0db8:0000:0000:0000:0000:0000:0005]:7331"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, creates := guardedHandler(t, test.boundAddr)

			read := guardedRequest(t, handler, http.MethodGet, "/api/tasks", test.host, "", "", "")
			if read.Code != http.StatusOK || *lists != 1 {
				t.Fatalf("status/lists = %d/%d, want %d/1; body = %s", read.Code, *lists, http.StatusOK, read.Body.String())
			}

			write := guardedRequest(t, handler, http.MethodPost, "/api/tasks", test.host, "http://"+test.host, "application/json", `{"title":"self"}`)
			if write.Code != http.StatusOK || *creates != 1 {
				t.Fatalf("self-origin status/creates = %d/%d, want %d/1; body = %s", write.Code, *creates, http.StatusOK, write.Body.String())
			}
		})
	}
}

func TestGuardRejectsForeignOriginsOnExplicitNonLoopbackBind(t *testing.T) {
	// The Host is pinned, so the Origin is compared with the bound address too
	// rather than with whatever the browser addressed.
	for _, origin := range []string{"http://evil.example:7331", "https://192.168.1.5:7331", "http://192.168.1.6:7331", "null"} {
		t.Run(origin, func(t *testing.T) {
			handler, lists, creates := guardedHandler(t, "192.168.1.5:7331")
			response := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "192.168.1.5:7331", origin, "application/json", `{"title":"injected"}`)
			assertGuardRejection(t, response, http.StatusForbidden)
			if *lists != 0 || *creates != 0 {
				t.Fatalf("service callbacks ran (%d lists, %d creates) for Origin %q", *lists, *creates, origin)
			}
		})
	}
}

func TestGuardOnWildcardBindPinsOnlyThePort(t *testing.T) {
	// A wildcard bind answers every address this machine has, under every name
	// that resolves to one of them, so there is no host to pin: the Host check
	// falls back to the port and the Origin check to repeating the authority the
	// browser addressed. That gap is documented in README.md's guard section and
	// named by the warning `serve` prints, and this test holds the two together
	// — including the drive-by it still admits, so closing it is a visible
	// change rather than a silent one.
	for _, boundAddr := range []string{"0.0.0.0:7331", "[::]:7331"} {
		t.Run(boundAddr, func(t *testing.T) {
			handler, lists, creates := guardedHandler(t, boundAddr)

			lan := guardedRequest(t, handler, http.MethodGet, "/api/tasks", "192.168.1.5:7331", "", "", "")
			if lan.Code != http.StatusOK || *lists != 1 {
				t.Fatalf("LAN host status/lists = %d/%d, want %d/1; body = %s", lan.Code, *lists, http.StatusOK, lan.Body.String())
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

			// The residual exposure: a page that rebinds its own name to this
			// machine addresses the board under that name, and the name matches
			// itself in both checks.
			reboundRead := guardedRequest(t, handler, http.MethodGet, "/api/tasks", "evil.example:7331", "", "", "")
			if reboundRead.Code != http.StatusOK || *lists != 2 {
				t.Fatalf("rebound-host status/lists = %d/%d, want %d/2; a wildcard bind cannot pin a host, so document the change if this now refuses", reboundRead.Code, *lists, http.StatusOK)
			}
			reboundWrite := guardedRequest(t, handler, http.MethodPost, "/api/tasks", "evil.example:7331", "http://evil.example:7331", "application/json", `{"title":"drive-by"}`)
			if reboundWrite.Code != http.StatusOK || *creates != 2 {
				t.Fatalf("rebound-origin status/creates = %d/%d, want %d/2; a wildcard bind cannot pin a host, so document the change if this now refuses", reboundWrite.Code, *creates, http.StatusOK)
			}
		})
	}
}
