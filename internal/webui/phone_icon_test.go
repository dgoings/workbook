package webui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A phone that saves the board to its home screen asks for an icon and a
// name. These hold what the board answers: the three icons, each the size it
// says it is, kept by the browser for a day and by no shared cache on a
// hosted board; a manifest named for the project that says the shortcut opens
// in the browser; the page linking both; and a security policy that lets the
// manifest load and lets nothing else in.

func phoneHandlers(t *testing.T) (local, hosted http.Handler) {
	t.Helper()
	options := newPhoneBoard().options(t)
	local = NewHandler(options)
	options.Hosted = true
	options.ServerLog = &lockedBuffer{}
	hosted = NewHandler(options)
	return local, hosted
}

func TestHandlerServesTheHomeScreenIcons(t *testing.T) {
	t.Parallel()
	local, hosted := phoneHandlers(t)
	sizes := map[string]int{"/icons/apple-touch-icon.png": 180, "/icons/icon-192.png": 192, "/icons/icon-512.png": 512}
	if len(sizes) != len(iconFiles) {
		t.Fatalf("the board serves %d icons, and this checks %d", len(iconFiles), len(sizes))
	}
	for path, size := range sizes {
		for name, test := range map[string]struct {
			handler http.Handler
			cache   string
		}{
			"local":  {local, "max-age=86400"},
			"hosted": {hosted, "private, max-age=86400"},
		} {
			response := request(t, test.handler, http.MethodGet, path)
			if response.Code != http.StatusOK {
				t.Fatalf("%s GET %s = %d; body = %s", name, path, response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "image/png" {
				t.Errorf("%s GET %s Content-Type = %q, want image/png", name, path, got)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Errorf("%s GET %s Cache-Control = %q, want %q", name, path, got, test.cache)
			}
			if got := response.Header().Get("Content-Security-Policy"); got != securityPolicy {
				t.Errorf("%s GET %s carries the policy %q, want the board's own", name, path, got)
			}
			decoded, err := png.Decode(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatalf("%s GET %s is not a PNG: %v", name, path, err)
			}
			if bounds := decoded.Bounds(); bounds.Dx() != size || bounds.Dy() != size {
				t.Errorf("%s GET %s is %dx%d, want %dx%d", name, path, bounds.Dx(), bounds.Dy(), size, size)
			}
			// iOS fills what is transparent in a touch icon with black, so the
			// one it uses is opaque to the corners.
			if path == "/icons/apple-touch-icon.png" {
				assertOpaque(t, path, decoded)
			}
		}
		if response := request(t, local, http.MethodPost, path); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Errorf("POST %s = %d allowing %q, want 405 allowing GET", path, response.Code, response.Header().Get("Allow"))
		}
	}
	if response := request(t, local, http.MethodGet, "/icons/icon-1024.png"); response.Code != http.StatusNotFound {
		t.Errorf("GET an icon the board does not have = %d, want 404", response.Code)
	}
}

func assertOpaque(t *testing.T, name string, picture image.Image) {
	t.Helper()
	bounds := picture.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := picture.At(x, y).RGBA(); alpha != 0xffff {
				t.Fatalf("%s is not opaque at %d,%d", name, x, y)
			}
		}
	}
}

func TestHandlerServesAManifestNamedForTheProject(t *testing.T) {
	t.Parallel()
	local, hosted := phoneHandlers(t)
	for name, test := range map[string]struct {
		handler http.Handler
		cache   string
	}{
		// The name is the project's and can change, so a browser checks
		// before it reuses one; a hosted board stores nothing that names
		// the project.
		"local":  {local, "no-cache"},
		"hosted": {hosted, "private, no-store"},
	} {
		response := request(t, test.handler, http.MethodGet, manifestPath)
		if response.Code != http.StatusOK {
			t.Fatalf("%s GET %s = %d; body = %s", name, manifestPath, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "application/manifest+json" {
			t.Errorf("%s manifest Content-Type = %q", name, got)
		}
		if got := response.Header().Get("Cache-Control"); got != test.cache {
			t.Errorf("%s manifest Cache-Control = %q, want %q", name, got, test.cache)
		}
		var manifest map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("%s manifest is not JSON: %v; body = %s", name, err, response.Body.String())
		}
		for member, want := range map[string]string{"name": "Atlas", "short_name": "Atlas", "start_url": "/", "scope": "/", "display": "browser"} {
			if manifest[member] != want {
				t.Errorf("%s manifest %s = %v, want %q", name, member, manifest[member], want)
			}
		}
		// Never an app window of its own: nothing that asks for one.
		if _, set := manifest["display_override"]; set {
			t.Errorf("%s manifest sets display_override", name)
		}
		icons, _ := manifest["icons"].([]any)
		var served []string
		for _, icon := range icons {
			entry, _ := icon.(map[string]any)
			source, _ := entry["src"].(string)
			served = append(served, source+" "+entry["sizes"].(string))
			if response := request(t, test.handler, http.MethodGet, source); response.Code != http.StatusOK {
				t.Errorf("%s manifest names %s, which answers %d", name, source, response.Code)
			}
		}
		if want := []string{"/icons/icon-192.png 192x192", "/icons/icon-512.png 512x512"}; !slices.Equal(served, want) {
			t.Errorf("%s manifest icons = %v, want %v", name, served, want)
		}
	}
	if response := request(t, local, http.MethodPost, manifestPath); response.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST %s = %d, want 405", manifestPath, response.Code)
	}
}

// The page names the manifest and both kinds of icon, and says nothing that
// would make a saved shortcut open as an app of its own on either kind of
// phone.
func TestHandlerPageLinksTheHomeScreenIcons(t *testing.T) {
	t.Parallel()
	local, _ := phoneHandlers(t)
	response := request(t, local, http.MethodGet, "/")
	head, _, found := strings.Cut(response.Body.String(), "</head>")
	if !found {
		t.Fatal("the page has no head")
	}
	for _, link := range []string{
		`<link rel="manifest" href="/manifest.webmanifest" crossorigin="use-credentials">`,
		`<link rel="icon" type="image/png" sizes="192x192" href="/icons/icon-192.png">`,
		`<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png">`,
		`<meta name="apple-mobile-web-app-title" content="Atlas">`,
	} {
		if !strings.Contains(head, link) {
			t.Errorf("the page's head does not hold %s", link)
		}
	}
	for _, standalone := range []string{"apple-mobile-web-app-capable", "mobile-web-app-capable"} {
		if strings.Contains(head, standalone) {
			t.Errorf("the page's head says %s, which opens a saved shortcut as an app of its own", standalone)
		}
	}
	// A manifest is fetched under manifest-src, which falls back to the
	// policy's default of nothing; the board's own origin is the one place it
	// may come from, and the rest of the policy is as it was.
	if !strings.Contains(securityPolicy, "; manifest-src 'self';") {
		t.Errorf("the policy %q does not let the board's own manifest load", securityPolicy)
	}
	if strings.Contains(securityPolicy, "*") || strings.Contains(securityPolicy, "data:") || strings.Contains(securityPolicy, "http") {
		t.Errorf("the policy %q names somewhere beyond the board", securityPolicy)
	}
}

type manifestReading struct {
	Page struct {
		Refused []string
		Errors  []string
		Icons   []string
	}
	Manifest struct {
		URL    string
		Errors []struct{ Message string }
		Data   string
	}
}

// In a phone's browser, under the page's own security policy, the manifest
// the page names loads and parses, every icon the page or the manifest names
// answers as a PNG, and the policy refuses nothing on the way.
func TestBrowserPhoneLoadsTheManifestAndIcons(t *testing.T) {
	t.Parallel()
	var reading manifestReading
	openInBrowser(t, newPhoneBoard().handler(t), browserPage{
		Width: 390, Height: 844, Path: "/", Phone: true, Manifest: true, BeforeLoad: phoneWatch,
		Script: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card"), "the board");
  const manifest = await (await fetch(document.querySelector('link[rel="manifest"]').href, { credentials: "include" })).json();
  const sources = [...document.querySelectorAll('link[rel="icon"], link[rel="apple-touch-icon"]')].map((link) => link.href)
    .concat(manifest.icons.map((icon) => new URL(icon.src, location.href).href));
  const icons = [];
  for (const source of sources) {
    const response = await fetch(source);
    icons.push(new URL(source).pathname + " " + response.status + " " + response.headers.get("Content-Type"));
  }
  await new Promise((resolve) => setTimeout(resolve, 200));
  return { refused: window.__refused, errors: window.__errors, icons };
})()`,
	}, &reading)
	if len(reading.Page.Refused) > 0 || len(reading.Page.Errors) > 0 {
		t.Errorf("the page was refused %v and threw %v", reading.Page.Refused, reading.Page.Errors)
	}
	if !strings.HasSuffix(reading.Manifest.URL, manifestPath) {
		t.Errorf("the browser found the manifest at %q, want %s", reading.Manifest.URL, manifestPath)
	}
	if len(reading.Manifest.Errors) > 0 {
		t.Errorf("the browser read the manifest with errors %+v", reading.Manifest.Errors)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(reading.Manifest.Data), &manifest); err != nil || manifest["name"] != "Atlas" || manifest["display"] != "browser" {
		t.Errorf("the browser loaded the manifest %q (%v), want one named Atlas that opens in the browser", reading.Manifest.Data, err)
	}
	want := []string{
		"/icons/icon-192.png 200 image/png",
		"/icons/apple-touch-icon.png 200 image/png",
		"/icons/icon-192.png 200 image/png",
		"/icons/icon-512.png 200 image/png",
	}
	if !slices.Equal(reading.Page.Icons, want) {
		t.Errorf("the icons answered %v, want %v", reading.Page.Icons, want)
	}
}
