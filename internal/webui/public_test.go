package webui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestParsePublicURLAcceptsAnHTTPSOriginOrALocalHTTPOne(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw    string
		origin string
	}{
		{"https://tasks.example.com", "https://tasks.example.com"},
		{"https://tasks.example.com/", "https://tasks.example.com"},
		{"https://Tasks.Example.com", "https://tasks.example.com"},
		{"https://tasks.example.com:8443", "https://tasks.example.com:8443"},
		{"https://tasks.example.com:443", "https://tasks.example.com"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://127.0.0.1:9000/", "http://127.0.0.1:9000"},
		{"http://[::1]:9000", "http://[::1]:9000"},
		{"https://xn--bcher-kva.example", "https://xn--bcher-kva.example"},
		{"https://my-board.example.com.", "https://my-board.example.com"},
		{"https://203.0.113.7", "https://203.0.113.7"},
		{"https://[2001:db8::1]:8443", "https://[2001:db8::1]:8443"},
		{"https://tasks.example.com:65535", "https://tasks.example.com:65535"},
	}
	for _, test := range tests {
		public, err := ParsePublicURL(test.raw)
		if err != nil {
			t.Errorf("ParsePublicURL(%q) error = %v, want accepted", test.raw, err)
			continue
		}
		if got := public.Origin(); got != test.origin {
			t.Errorf("ParsePublicURL(%q).Origin() = %q, want %q", test.raw, got, test.origin)
		}
	}
}

func TestParsePublicURLRefusesAnythingButAnOrigin(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"",
		"tasks.example.com",
		"//tasks.example.com",
		"https://",
		"https:///board",
		"http://tasks.example.com",
		"http://10.0.0.5:7331",
		"ftp://tasks.example.com",
		"https://tasks.example.com/board",
		"https://tasks.example.com//",
		"https://tasks.example.com?team=a",
		"https://tasks.example.com/?",
		"https://tasks.example.com#top",
		"https://sam@tasks.example.com",
		"https://sam:secret@tasks.example.com",
		"https://tasks.example.com:0",
		"https://tasks.example.com:99999",
		"https://tasks.example.com:https",
		"mailto:sam@example.com",
		" https://tasks.example.com",
		// A host no browser can send, and a port no browser writes that
		// way: each would start a board that refuses every request.
		"https://*.example.com",
		"https://tasks_example.com",
		"https://-tasks.example.com",
		"https://tasks-.example.com",
		"https://tasks..example.com",
		"https://bücher.example",
		"https://" + strings.Repeat("a", 64) + ".example.com",
		"https://" + strings.Repeat("abcdefghi.", 26) + "com",
		"https://10.0.0.010",
		"https://127.1",
		"https://tasks.example.com:0443",
		"https://tasks.example.com:00",
		"https://tasks.example.com:+443",
	} {
		_, err := ParsePublicURL(raw)
		if err == nil {
			t.Errorf("ParsePublicURL(%q) accepted, want refused", raw)
			continue
		}
		if core.CategoryOf(err) != core.CategoryInvocation {
			t.Errorf("ParsePublicURL(%q) category = %q, want %q", raw, core.CategoryOf(err), core.CategoryInvocation)
		}
		message := err.Error()
		if !strings.Contains(message, "https://") || !strings.Contains(message, "path") ||
			!strings.Contains(message, "letters, digits and hyphens") || !strings.Contains(message, "no leading zero") {
			t.Errorf("ParsePublicURL(%q) error = %q, want a sentence saying what is accepted", raw, message)
		}
	}
}

// publicGuardedHandler is guardedHandler for a board that also answers at a
// public address.
func publicGuardedHandler(t *testing.T, boundAddr, publicURL string) (http.Handler, *int, *int) {
	t.Helper()
	public, err := ParsePublicURL(publicURL)
	if err != nil {
		t.Fatal(err)
	}
	lists, creates := new(int), new(int)
	handler := NewHandler(guardedOptions(t, lists, creates))
	return GuardSameOriginAt(handler, boundAddr, &public), lists, creates
}

func TestGuardAcceptsThePublicAddressBesideTheBoundOne(t *testing.T) {
	t.Parallel()
	const bound = "127.0.0.1:7331"
	tests := []struct {
		name   string
		host   string
		origin string
	}{
		{"public host, no origin", "tasks.example.com", ""},
		{"public host with its default port", "tasks.example.com:443", ""},
		{"public host in another case", "Tasks.Example.COM", ""},
		{"public host with a root dot", "tasks.example.com.", ""},
		{"public host and public origin", "tasks.example.com", "https://tasks.example.com"},
		{"proxy rewrote the host to the bound one", "127.0.0.1:7331", "https://tasks.example.com"},
		{"bound host and bound origin", "127.0.0.1:7331", "http://127.0.0.1:7331"},
		{"loopback name for the bound host", "localhost:7331", "http://localhost:7331"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, lists, _ := publicGuardedHandler(t, bound, "https://tasks.example.com")
			response := guardedRequest(t, handler, http.MethodGet, "/api/tasks", test.host, test.origin, "", "")
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
			}
			if *lists != 1 {
				t.Fatalf("list calls = %d, want 1", *lists)
			}
			handler, _, creates := publicGuardedHandler(t, bound, "https://tasks.example.com")
			response = guardedRequest(t, handler, http.MethodPost, "/api/tasks", test.host, test.origin, "application/json", `{"title":"Through the proxy"}`)
			if response.Code != http.StatusOK || *creates != 1 {
				t.Fatalf("create status = %d with %d creates, want 200 and 1; body = %s", response.Code, *creates, response.Body.String())
			}
		})
	}
}

func TestGuardRefusesOtherSitesAtThePublicAddress(t *testing.T) {
	t.Parallel()
	const bound = "127.0.0.1:7331"
	tests := []struct {
		name   string
		host   string
		origin string
	}{
		{"foreign host", "evil.example", ""},
		{"public host on another port", "tasks.example.com:8443", ""},
		{"public name as a subdomain", "tasks.example.com.evil.example", ""},
		{"foreign origin at the public host", "tasks.example.com", "https://evil.example"},
		{"foreign origin at the bound host", "127.0.0.1:7331", "https://evil.example"},
		{"public name over plain http", "tasks.example.com", "http://tasks.example.com"},
		{"public origin on another port", "tasks.example.com", "https://tasks.example.com:8443"},
		{"sandboxed frame", "tasks.example.com", "null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				handler, lists, creates := publicGuardedHandler(t, bound, "https://tasks.example.com")
				response := guardedRequest(t, handler, method, "/api/tasks", test.host, test.origin, "application/json", `{"title":"Drive-by"}`)
				assertGuardRejection(t, response, http.StatusForbidden)
				if *lists != 0 || *creates != 0 {
					t.Fatalf("%s reached the service: %d lists, %d creates", method, *lists, *creates)
				}
				if !strings.Contains(response.Body.String(), "https://tasks.example.com") {
					t.Fatalf("%s refusal = %s, want it to name the public address", method, response.Body.String())
				}
			}
		})
	}
}

func TestGuardWithoutAPublicAddressStillRefusesItsName(t *testing.T) {
	t.Parallel()
	handler, lists, _ := guardedHandler(t, "127.0.0.1:7331")
	response := guardedRequest(t, handler, http.MethodGet, "/api/tasks", "tasks.example.com", "https://tasks.example.com", "", "")
	assertGuardRejection(t, response, http.StatusForbidden)
	if *lists != 0 {
		t.Fatalf("list calls = %d, want 0", *lists)
	}
}
