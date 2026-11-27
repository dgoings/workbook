package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// requestRecorder keeps every request that reached the server, in order.
type requestRecorder struct {
	mu       sync.Mutex
	requests []string
}

func (recorder *requestRecorder) record(request *http.Request) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.requests = append(recorder.requests, request.Method+" "+request.URL.RequestURI())
}

func (recorder *requestRecorder) seen() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.requests)
}

// guardedRecordedBoard is the board as serve runs it, behind the same-origin
// guard bound to the address the request arrived at, with every request
// recorded before the guard sees it.
func guardedRecordedBoard(options Options, recorder *requestRecorder) http.Handler {
	inner := NewHandler(options)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.record(request)
		bound := request.Context().Value(http.LocalAddrContextKey).(net.Addr).String()
		GuardSameOrigin(inner, bound).ServeHTTP(writer, request)
	})
}

// htmlBodyBoard is a board whose every part read answers one html body.
func htmlBodyBoard(t *testing.T, html string) Options {
	t.Helper()
	options := guardedOptions(t, new(int), new(int))
	relics := relicRouteOptions(&relicRouteCalls{}, nil)
	options.ListRelics, options.ShowRelic = relics.ListRelics, relics.ShowRelic
	options.RelicPart = func(context.Context, string, string) (core.RelicPartView, error) {
		return core.RelicPartView{Relic: routeRelicID, Head: "abc", Part: core.RelicDocumentPart{
			ID: routePartID, Type: "section",
			Body: &core.RelicDocumentBody{Format: "html", Version: "v1", Bytes: int64(len(html)), Text: &html},
		}}, nil
	}
	return options
}

// hostileBody is an HTML body whose script tries everything Review Focus 1
// names, at relative and absolute board addresses. Every address it aims at
// carries via=<attempt>, so a request any of them made is told apart from the
// page's own. Each attempt settles on what the page itself can see: a throw,
// an error event, a null return, or a content-policy violation event. It
// writes the outcomes into #result and only then sets data-done.
const hostileBody = `<!doctype html>
<html><head><title>hostile body</title></head>
<body><pre id="result"></pre>
<script>
(async () => {
  const violations = [];
  const violated = [];
  document.addEventListener("securitypolicyviolation", (event) => {
    violations.push({ directive: event.effectiveDirective, blocked: event.blockedURI });
    for (const waiter of violated.splice(0)) waiter();
  });
  // Resolves once a violation that matches has been reported.
  const violation = (matches) => new Promise((resolve) => {
    const check = () => {
      if (violations.some(matches)) resolve(true);
      else violated.push(check);
    };
    check();
  });
  let unloading = false;
  window.addEventListener("beforeunload", () => { unloading = true; });
  window.addEventListener("pagehide", () => { unloading = true; });

  const absolute = (path) => "http://" + location.host + path;
  const blocked = (how) => ({ outcome: "blocked", how });
  const reached = (how) => ({ outcome: "reached", how });
  const viaEvents = (start, ok, fail) => new Promise((resolve) => {
    try {
      const target = start();
      target[ok] = () => resolve(reached(ok));
      target[fail] = () => resolve(blocked(fail));
    } catch (error) { resolve(blocked("threw " + error.name)); }
  });
  const attempts = {};
  let frames = 0;

  for (const [name, url] of [["relative", "/api/tasks?via=fetch-relative"], ["absolute", absolute("/api/tasks?via=fetch-absolute")]]) {
    attempts["fetch " + name] = fetch(url).then((response) => reached("status " + response.status), (error) => blocked("rejected " + error.name));
    attempts["xhr " + name] = viaEvents(() => {
      const request = new XMLHttpRequest();
      request.open("GET", url.replace("fetch", "xhr"));
      setTimeout(() => request.send(), 0);
      return request;
    }, "onload", "onerror");
    attempts["eventsource " + name] = viaEvents(() => new EventSource(url.replace("fetch", "eventsource")), "onopen", "onerror");
    // sendBeacon answers true once it has queued the request, so what the
    // page sees of a refusal is the violation its own address is named in.
    attempts["beacon " + name] = (async () => {
      const target = url.replace("fetch", "beacon");
      let sent;
      try { sent = navigator.sendBeacon(target, "probe"); } catch (error) { return blocked("threw " + error.name); }
      if (!sent) return blocked("returned false");
      await violation((seen) => seen.directive === "connect-src" && seen.blocked.endsWith(target.slice(target.indexOf("/api/"))));
      return blocked("violation reported");
    })();
    attempts["img " + name] = viaEvents(() => {
      const image = document.createElement("img");
      image.src = url.replace("fetch", "img");
      document.body.append(image);
      return image;
    }, "onload", "onerror");
    attempts["prefetch " + name] = viaEvents(() => {
      const link = document.createElement("link");
      link.rel = "prefetch";
      link.href = url.replace("fetch", "prefetch");
      document.head.append(link);
      return link;
    }, "onload", "onerror");
    attempts["window.open " + name] = Promise.resolve((() => {
      try {
        const opened = window.open(url.replace("/api/tasks", "/").replace("fetch", "open"));
        return opened === null ? blocked("returned null") : reached("returned a window");
      } catch (error) { return blocked("threw " + error.name); }
    })());
    // A frame's violation names only the origin it was refused, so each
    // frame waits for one more frame-src violation than the frames before it.
    attempts["iframe " + name] = (async () => {
      const frame = document.createElement("iframe");
      const ordinal = ++frames;
      frame.src = url.replace("/api/tasks", "/").replace("fetch", "iframe");
      document.body.append(frame);
      await violation(() => violations.filter((seen) => seen.directive === "frame-src").length >= ordinal);
      return blocked("violation reported");
    })();
  }
  attempts["websocket"] = viaEvents(() => new WebSocket("ws://" + location.host + "/api/sync?via=websocket"), "onopen", "onerror");

  // A form POST at the task collection. A sandbox without allow-forms drops
  // the submission silently, so what the page can see is that no navigation
  // followed: the document is still this one when every other attempt has
  // settled, and beforeunload never fired. The recorder is the other half.
  attempts["form post"] = Promise.resolve((() => {
    const form = document.createElement("form");
    form.method = "post";
    form.action = "/api/tasks?via=form";
    const field = document.createElement("input");
    field.name = "title";
    field.value = "injected";
    form.append(field);
    document.body.append(form);
    try { form.submit(); } catch (error) { return blocked("threw " + error.name); }
    return { outcome: "submitted", how: "no event; see navigated" };
  })());
  attempts["localStorage"] = Promise.resolve((() => {
    try { localStorage.setItem("workbook-body-probe", "written"); return reached("set"); } catch (error) { return blocked("threw " + error.name); }
  })());
  attempts["document.cookie"] = Promise.resolve((() => {
    try { document.cookie = "workbook-body-probe=written"; return reached("set: " + document.cookie); } catch (error) { return blocked("threw " + error.name); }
  })());

  // Written as each attempt settles, so a page that never finishes still says
  // which attempts are left pending.
  const outcomes = {};
  const write = () => {
    document.getElementById("result").textContent = JSON.stringify({
      outcomes,
      violations,
      opener: window.opener === null ? "null" : typeof window.opener,
      origin: self.origin,
      href: location.href,
      unloading
    });
  };
  for (const name of Object.keys(attempts)) outcomes[name] = { outcome: "pending", how: "never settled" };
  write();
  await Promise.all(Object.entries(attempts).map(([name, attempt]) => attempt.then((outcome) => { outcomes[name] = outcome; write(); })));
  write();
  document.body.dataset.done = "";
})();
</script>
</body></html>`

// bodyOutcomes is what hostileBody wrote into #result.
type bodyOutcomes struct {
	Outcomes map[string]struct {
		Outcome string `json:"outcome"`
		How     string `json:"how"`
	} `json:"outcomes"`
	Violations []struct {
		Directive string `json:"directive"`
		Blocked   string `json:"blocked"`
	} `json:"violations"`
	Opener    string `json:"opener"`
	Origin    string `json:"origin"`
	Href      string `json:"href"`
	Unloading bool   `json:"unloading"`
}

// readHostileBody waits for the body's script to finish and answers what it
// wrote, with whether the document was still the body by then. A body that
// has not finished within the wait answers what it has, so the failure names
// the attempts still pending rather than only a timeout.
const readHostileBody = pageGeometryHelpers + `
(async () => {
  try {
    await waitFor(() => document.body && document.body.hasAttribute("data-done"), "the body's attempts to settle");
  } catch (_) {}
  const written = document.getElementById("result");
  const result = written && written.textContent ? JSON.parse(written.textContent) : { outcomes: {} };
  result.done = !!document.body && document.body.hasAttribute("data-done");
  result.stillTheBody = location.pathname.endsWith("/body") && document.title === "hostile body";
  return result;
})()`

var hostileAttempts = []string{
	"fetch relative", "fetch absolute", "xhr relative", "xhr absolute",
	"eventsource relative", "eventsource absolute", "beacon relative", "beacon absolute",
	"img relative", "img absolute", "prefetch relative", "prefetch absolute",
	"window.open relative", "window.open absolute", "iframe relative", "iframe absolute",
	"websocket", "form post", "localStorage", "document.cookie",
}

func checkHostileOutcomes(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var result struct {
		bodyOutcomes
		Done         bool `json:"done"`
		StillTheBody bool `json:"stillTheBody"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode the body's outcomes %s: %v", raw, err)
	}
	t.Logf("body outcomes: %s", raw)
	if !result.Done {
		t.Errorf("the body's script did not finish: an attempt never settled, or the document was replaced")
	}
	for _, name := range hostileAttempts {
		outcome, ok := result.Outcomes[name]
		switch {
		case !ok:
			t.Errorf("%s: no outcome reported", name)
		case name == "form post":
			if outcome.Outcome != "blocked" && (!result.StillTheBody || result.Unloading) {
				t.Errorf("form post: the document navigated away (still the body %v, unloading %v)", result.StillTheBody, result.Unloading)
			}
		case outcome.Outcome != "blocked":
			t.Errorf("%s: %s (%s), want blocked", name, outcome.Outcome, outcome.How)
		}
	}
	if len(result.Outcomes) != len(hostileAttempts) {
		t.Errorf("the body reported %d outcomes, want %d", len(result.Outcomes), len(hostileAttempts))
	}
	if result.Opener != "null" {
		t.Errorf("window.opener = %s, want null", result.Opener)
	}
	if result.Origin != "null" {
		t.Errorf("origin = %q, want the opaque \"null\"", result.Origin)
	}
	if !result.StillTheBody || result.Unloading {
		t.Errorf("the body document was replaced or began to unload (still the body %v, unloading %v)", result.StillTheBody, result.Unloading)
	}
}

// A body opened by itself runs its scripts, and they reach nothing: not the
// board's API by any request a page can make, not its storage or cookies, not
// a window or a frame. The page sees each attempt fail and the server sees no
// request but the body's own.
func TestBrowserOpenedBodyCannotReachTheBoard(t *testing.T) {
	t.Parallel()
	bodyPath := relicBodyPath(routeRelicID, routePartID)

	t.Run("opened directly", func(t *testing.T) {
		t.Parallel()
		recorder := &requestRecorder{}
		var raw json.RawMessage
		openInBrowser(t, guardedRecordedBoard(htmlBodyBoard(t, hostileBody), recorder), browserPage{
			Width: 800, Height: 600, Path: bodyPath, Script: readHostileBody,
		}, &raw)
		checkHostileOutcomes(t, raw)
		if seen := recorder.seen(); !slices.Equal(seen, []string{"GET " + bodyPath}) {
			t.Errorf("the server saw %q, want only the body's own request", seen)
		}
	})

	t.Run("opened from the board", func(t *testing.T) {
		t.Parallel()
		recorder := &requestRecorder{}
		var answer struct {
			Page   map[string]any  `json:"page"`
			Opened json.RawMessage `json:"opened"`
			Then   map[string]any  `json:"then"`
		}
		openInBrowser(t, guardedRecordedBoard(htmlBodyBoard(t, hostileBody), recorder), browserPage{
			Width: 1280, Height: 800, Path: "/",
			// The link the relic page draws for a body (Decision 10).
			Script: `(() => {
  const link = document.createElement("a");
  link.href = ` + jsString(bodyPath) + `;
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.textContent = "Open in new tab";
  document.body.append(link);
  const opened = window.open(link.href, "_blank", "noopener");
  return { returned: opened === null ? "null" : typeof opened };
})()`,
			Opened: &openedPage{
				URL:    bodyPath,
				Script: readHostileBody,
				Then: `({
  storage: localStorage.getItem("workbook-body-probe"),
  cookie: document.cookie.includes("workbook-body-probe")
})`,
			},
		}, &answer)
		if answer.Page["returned"] != "null" {
			t.Errorf("window.open with noopener returned %v to the board, want null", answer.Page["returned"])
		}
		checkHostileOutcomes(t, answer.Opened)
		if answer.Then["storage"] != nil || answer.Then["cookie"] != false {
			t.Errorf("the board's storage after the body ran = %v, want no probe key and no probe cookie", answer.Then)
		}
		seen := recorder.seen()
		bodies := 0
		for _, request := range seen {
			if strings.Contains(request, "via=") || strings.HasPrefix(request, "POST ") {
				t.Errorf("the server saw %q, a request the body made", request)
			}
			if request == "GET "+bodyPath {
				bodies++
			}
		}
		if bodies != 1 {
			t.Errorf("the body was requested %d times, want once; the server saw %q", bodies, seen)
		}
	})
}

// An HTML body is drawn as a page: its inline style applies, its data: image
// loads and its script runs.
func TestBrowserOpenedHTMLBodyIsDrawnAsAPage(t *testing.T) {
	t.Parallel()
	const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	page := `<!doctype html><html><head><style>h1 { color: rgb(1, 2, 3); }</style></head>
<body><h1>Drawn</h1><img id="pixel" alt="pixel" src="` + pixel + `">
<script>
const image = document.getElementById("pixel");
const finish = (state) => { document.body.dataset.image = state; document.body.dataset.done = ""; };
if (image.complete && image.naturalWidth > 0) finish("loaded");
else { image.onload = () => finish("loaded"); image.onerror = () => finish("error"); }
</script></body></html>`
	recorder := &requestRecorder{}
	var result struct {
		Color string `json:"color"`
		Image string `json:"image"`
		Width int    `json:"width"`
	}
	openInBrowser(t, guardedRecordedBoard(htmlBodyBoard(t, page), recorder), browserPage{
		Width: 800, Height: 600, Path: relicBodyPath(routeRelicID, routePartID),
		Script: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.body && document.body.hasAttribute("data-done"), "the body's script to run");
  return {
    color: getComputedStyle(document.querySelector("h1")).color,
    image: document.body.dataset.image,
    width: document.getElementById("pixel").naturalWidth
  };
})()`,
	}, &result)
	if result.Color != "rgb(1, 2, 3)" {
		t.Errorf("the body's own style did not apply: color = %q", result.Color)
	}
	if result.Image != "loaded" || result.Width != 1 {
		t.Errorf("the body's data: image = %q, width %d, want loaded at 1", result.Image, result.Width)
	}
	if seen := recorder.seen(); len(seen) != 1 {
		t.Errorf("the server saw %q, want only the body", seen)
	}
}

func jsString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
