package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// relicBodyOptions is a board that reads relics and answers every part read
// with body, recording what it was asked for in asked.
func relicBodyOptions(asked *[]string, body *core.RelicDocumentBody) Options {
	options := relicRouteOptions(&relicRouteCalls{}, nil)
	options.RelicPart = func(_ context.Context, relicID, partID string) (core.RelicPartView, error) {
		*asked = append(*asked, relicID+" "+partID)
		return core.RelicPartView{Relic: routeRelicID, Head: "abc", Part: core.RelicDocumentPart{
			ID: routePartID, Type: "section", Body: body,
		}}, nil
	}
	return options
}

func relicBodyPath(relicID, partID string) string {
	return "/relics/" + relicID + "/parts/" + partID + "/body"
}

// Pinned byte for byte, so a change to what an opened body may do is a change
// a reviewer sees.
func TestBodySecurityPolicyIsPinned(t *testing.T) {
	t.Parallel()
	const want = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
	if bodySecurityPolicy != want {
		t.Fatalf("bodySecurityPolicy = %q, want %q", bodySecurityPolicy, want)
	}
}

func TestRelicBodyRouteHeaders(t *testing.T) {
	t.Parallel()
	text := func(value string) *string { return &value }
	binary := []byte{0xff, 0xfe, 0x00, 0x01, 'x'}
	encoded := base64.StdEncoding.EncodeToString(binary)
	tests := []struct {
		name        string
		body        core.RelicDocumentBody
		media       string
		disposition string
		content     []byte
	}{
		{
			name:  "html",
			body:  core.RelicDocumentBody{Format: "html", Version: "v-html", Bytes: 21, Text: text("<p>drawn as a page</p>")},
			media: "text/html; charset=utf-8", disposition: "inline", content: []byte("<p>drawn as a page</p>"),
		},
		{
			name:  "htm",
			body:  core.RelicDocumentBody{Format: "htm", Version: "v-htm", Bytes: 3, Text: text("<b>")},
			media: "text/html; charset=utf-8", disposition: "inline", content: []byte("<b>"),
		},
		{
			name:  "HTML label in another case",
			body:  core.RelicDocumentBody{Format: "HTML", Version: "v-upper", Bytes: 4, Text: text("<i>x")},
			media: "text/html; charset=utf-8", disposition: "inline", content: []byte("<i>x"),
		},
		{
			name:  "markdown is text, not markup",
			body:  core.RelicDocumentBody{Format: "md", Version: "v-md", Bytes: 30, Text: text("# Goals\n<script>alert(1)</script>")},
			media: "text/plain; charset=utf-8", disposition: "inline", content: []byte("# Goals\n<script>alert(1)</script>"),
		},
		{
			name:  "text with no label",
			body:  core.RelicDocumentBody{Version: "v-plain", Bytes: 5, Text: text("plain")},
			media: "text/plain; charset=utf-8", disposition: "inline", content: []byte("plain"),
		},
		{
			name:  "bytes that are not UTF-8",
			body:  core.RelicDocumentBody{Format: "png", Version: "v-bin", Bytes: int64(len(binary)), Base64: &encoded},
			media: "application/octet-stream", disposition: contentDisposition("attachment", routePartID+".png"), content: binary,
		},
		{
			name:  "bytes with no label",
			body:  core.RelicDocumentBody{Version: "v-bin2", Bytes: int64(len(binary)), Base64: &encoded},
			media: "application/octet-stream", disposition: contentDisposition("attachment", routePartID+".bin"), content: binary,
		},
		{
			// The label is somebody's text: a quote or a control in it reaches
			// the header only as contentDisposition formats it.
			name:  "bytes whose label holds a quote and a control",
			body:  core.RelicDocumentBody{Format: "a\"b\nc", Version: "v-odd", Bytes: int64(len(binary)), Base64: &encoded},
			media: "application/octet-stream", disposition: contentDisposition("attachment", routePartID+".a\"b\nc"), content: binary,
		},
		{
			name:  "bytes whose label holds path separators",
			body:  core.RelicDocumentBody{Format: "../x\\y", Version: "v-path", Bytes: int64(len(binary)), Base64: &encoded},
			media: "application/octet-stream", disposition: contentDisposition("attachment", routePartID+"..._x_y"), content: binary,
		},
		{
			// A label of html does not make bytes a page.
			name:  "bytes labelled html",
			body:  core.RelicDocumentBody{Format: "html", Version: "v-binhtml", Bytes: int64(len(binary)), Base64: &encoded},
			media: "application/octet-stream", disposition: contentDisposition("attachment", routePartID+".html"), content: binary,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var asked []string
			body := test.body
			handler := NewHandler(relicBodyOptions(&asked, &body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, relicBodyPath("WB-R-01M8", "01M8"), nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
			}
			header := response.Header()
			for name, want := range map[string]string{
				"Content-Type":            test.media,
				"Content-Disposition":     test.disposition,
				"Content-Security-Policy": bodySecurityPolicy,
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
				"ETag":                    `"` + test.body.Version + `"`,
			} {
				if got := header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if got := header.Values("Content-Security-Policy"); len(got) != 1 {
				t.Errorf("Content-Security-Policy is sent %d times: %q", len(got), got)
			}
			if got := response.Body.Bytes(); string(got) != string(test.content) {
				t.Errorf("body = %q, want %q", got, test.content)
			}
			if len(asked) != 1 || asked[0] != "WB-R-01M8 01M8" {
				t.Errorf("the part read was asked for %q, want the path's relic and part", asked)
			}
		})
	}
}

func TestRelicBodyDeliveryKeepsTypeAndDispositionTogether(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		format      string
		content     string
		isText      bool
		media       string
		disposition string
	}{
		{"html", "<p>", true, "text/html; charset=utf-8", "inline"},
		{" HTM ", "<p>", true, "text/html; charset=utf-8", "inline"},
		{"md", "<p>", true, "text/plain; charset=utf-8", "inline"},
		{"svg", "<svg onload=alert(1)>", true, "text/plain; charset=utf-8", "inline"},
		{"", "x", true, "text/plain; charset=utf-8", "inline"},
		// Text that is not UTF-8 is not served as text, whatever claims it is.
		{"html", "\xff\xfe", true, "application/octet-stream", "attachment"},
		{"html", "<p>", false, "application/octet-stream", "attachment"},
	} {
		media, disposition := relicBodyDelivery(test.format, []byte(test.content), test.isText)
		if media != test.media || disposition != test.disposition {
			t.Errorf("relicBodyDelivery(%q, %q, %v) = %q, %q; want %q, %q",
				test.format, test.content, test.isText, media, disposition, test.media, test.disposition)
		}
	}
}

func TestRelicBodyRouteWithoutBody(t *testing.T) {
	t.Parallel()
	var asked []string
	handler := NewHandler(relicBodyOptions(&asked, nil))
	response := requestJSON(t, handler, http.MethodGet, relicBodyPath("WB-R-01M8", "01M8"), "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", response.Code, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode: %v; body = %s", err, response.Body.String())
	}
	want := "part " + routePartID + " of relic " + routeRelicID + " has no body"
	if document.Format != "workbook.error" || document.Error.Category != core.CategoryNotFound || document.Error.Message != want {
		t.Fatalf("answer = %#v, want not-found %q", document, want)
	}
	// An error is the board's own JSON, under the board's policy.
	if got := response.Header().Get("Content-Security-Policy"); got != securityPolicy {
		t.Errorf("Content-Security-Policy = %q on an error, want the board's", got)
	}
}

func TestRelicBodyRouteMapsReadErrors(t *testing.T) {
	t.Parallel()
	options := relicRouteOptions(&relicRouteCalls{}, nil)
	options.RelicPart = func(context.Context, string, string) (core.RelicPartView, error) {
		return core.RelicPartView{}, core.Errorf(core.CategoryNotFound, "relic WB-R-9 not found")
	}
	response := requestJSON(t, NewHandler(options), http.MethodGet, relicBodyPath("WB-R-9", "01M8"), "")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "relic WB-R-9 not found") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

// Without the relic reads the address is unknown, as /config is to a board
// without a vocabulary (Decision 4).
func TestRelicBodyRouteAbsentWithoutRelicReads(t *testing.T) {
	t.Parallel()
	for name, options := range map[string]Options{
		"no relic options": {},
		"no part read": func() Options {
			options := relicRouteOptions(&relicRouteCalls{}, nil)
			options.RelicPart = nil
			return options
		}(),
	} {
		response := httptest.NewRecorder()
		NewHandler(options).ServeHTTP(response, httptest.NewRequest(http.MethodGet, relicBodyPath("WB-R-01", "01M8"), nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, response.Code)
		}
	}
}

func TestRelicBodyRouteRefusesOtherMethods(t *testing.T) {
	t.Parallel()
	var asked []string
	text := "x"
	handler := NewHandler(relicBodyOptions(&asked, &core.RelicDocumentBody{Version: "v", Bytes: 1, Text: &text}))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(method, relicBodyPath("WB-R-01", "01M8"), strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s = %d Allow %q, want 405 Allow GET", method, response.Code, response.Header().Get("Allow"))
		}
	}
	if len(asked) != 0 {
		t.Errorf("a refused method read the part: %q", asked)
	}
}

func TestMalformedRelicBodyPaths(t *testing.T) {
	t.Parallel()
	var asked []string
	text := "x"
	handler := NewHandler(relicBodyOptions(&asked, &core.RelicDocumentBody{Version: "v", Bytes: 1, Text: &text}))
	for _, path := range []string{
		"/relics//parts/p/body",
		"/relics/a/parts//body",
		"/relics/./parts/p/body",
		"/relics/../parts/p/body",
		"/relics/a/parts/./body",
		"/relics/a/parts/../body",
		"/relics/%2E%2E/parts/p/body",
		"/relics/a/parts/%2E/body",
		"/relics/a/./parts/p/body",
		"/relics/a/parts/x/../p/body",
		"/relics/a/parts/p/body/x/..",
		"/relics/a/parts/p/body/.",
		"/relics/a/parts/p/body/",
		"/relics/a/parts/p/body/x",
		"/relics/a/parts/p/q/body",
		"/relics/a%23b/parts/p/body",
		"/relics/a/parts/p%23q/body",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d (Location %q), want 404", method, path, response.Code, response.Header().Get("Location"))
			}
		}
	}
	if len(asked) != 0 {
		t.Errorf("a malformed path read a part: %q", asked)
	}
}

// A request a sandboxed document makes carries Origin: null. The guard refuses
// it on every route a body could aim at, reads included; the same request with
// no Origin, as a browser sends a plain navigation, is let through.
func TestGuardRefusesANullOriginOnRelicRoutes(t *testing.T) {
	t.Parallel()
	const bound = "127.0.0.1:7331"
	var asked []string
	text := "x"
	options := relicBodyOptions(&asked, &core.RelicDocumentBody{Version: "v", Bytes: 1, Text: &text})
	lists := 0
	options.List = func(context.Context) ([]core.Task, error) { lists++; return boardTasks(), nil }
	handler := GuardSameOrigin(NewHandler(options), bound)
	for _, target := range []string{"/api/relics", "/api/tasks", relicBodyPath("WB-R-01", "01M8")} {
		refused := guardedRequest(t, handler, http.MethodGet, target, bound, "null", "", "")
		assertGuardRejection(t, refused, http.StatusForbidden)
		if lists != 0 || len(asked) != 0 {
			t.Fatalf("GET %s with Origin: null reached the board (%d lists, %q parts)", target, lists, asked)
		}
	}
	for _, target := range []string{"/api/relics", "/api/tasks", relicBodyPath("WB-R-01", "01M8")} {
		if response := guardedRequest(t, handler, http.MethodGet, target, bound, "", "", ""); response.Code != http.StatusOK {
			t.Errorf("GET %s with no Origin = %d, want 200; body = %s", target, response.Code, response.Body.String())
		}
	}
}
