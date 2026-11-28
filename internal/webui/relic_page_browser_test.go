package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The relic list and the relic page, in a real browser, over fakes.

const (
	pageRelicID   = "WB-R-01M8EVZ9BQR7GNFYPYHSVT6FRC"
	pageRelicPlan = "WB-R-01M8EW0000PLAN000000000001"
	pageRelicOld  = "WB-R-01M8EW0000RETIRED000000002"
	pageRelicGone = "WB-R-01M8EW0000DELETED000000003"
	pagePartMD    = "01M8EVZ9BQ20NDMR0F09K3CG01"
	pagePartHTML  = "01M8EVZ9BQ20NDMR0F09K3HT02"
	pagePartBin   = "01M8EVZ9BQ20NDMR0F09K3CG03"
	pageRelicTask = "WB-01J0000000000000000000PH01"
)

var pageRelicStamp = time.Date(2026, time.November, 20, 9, 30, 0, 0, time.UTC)

// relicPageFake answers the three relic reads a board's relic pages need, and
// counts every part read: how many of each, and how many at once.
type relicPageFake struct {
	mu          sync.Mutex
	summaries   []core.RelicSummary
	documents   []core.RelicDocument
	bodies      map[string]core.RelicDocumentBody
	largeBody   *string
	listErr     error
	partErrs    map[string]error
	partDelay   time.Duration
	partReads   map[string]int
	inFlight    int
	maxInFlight int
}

func (fake *relicPageFake) document(id string) (core.RelicDocument, error) {
	var found []core.RelicDocument
	for _, document := range fake.documents {
		if strings.HasPrefix(document.ID, id) {
			found = append(found, document)
		}
	}
	if len(found) != 1 {
		return core.RelicDocument{}, core.Errorf(core.CategoryNotFound, "relic %s not found", id)
	}
	return found[0], nil
}

// apply gives options the fake's three reads.
func (fake *relicPageFake) apply(options *Options) {
	options.ListRelics = func(context.Context) ([]core.RelicSummary, error) {
		return slices.Clone(fake.summaries), fake.listErr
	}
	options.ShowRelic = func(_ context.Context, id string, _ bool) (core.RelicDocument, error) {
		return fake.document(id)
	}
	options.RelicPart = func(_ context.Context, relicID, partID string) (core.RelicPartView, error) {
		fake.mu.Lock()
		if fake.partReads == nil {
			fake.partReads = map[string]int{}
		}
		fake.partReads[partID]++
		fake.inFlight++
		fake.maxInFlight = max(fake.maxInFlight, fake.inFlight)
		fake.mu.Unlock()
		defer func() {
			fake.mu.Lock()
			fake.inFlight--
			fake.mu.Unlock()
		}()
		if fake.partDelay > 0 {
			time.Sleep(fake.partDelay)
		}
		document, err := fake.document(relicID)
		if err != nil {
			return core.RelicPartView{}, err
		}
		if err := fake.partErrs[partID]; err != nil {
			return core.RelicPartView{}, err
		}
		for _, part := range document.Parts {
			if part.ID != partID {
				continue
			}
			if fake.largeBody != nil {
				part.Body = &core.RelicDocumentBody{Format: "md", Version: "v-" + partID, Bytes: int64(len(*fake.largeBody)), Text: fake.largeBody}
			} else if body, ok := fake.bodies[partID]; ok {
				part.Body = &body
			}
			return core.RelicPartView{Relic: document.ID, Head: document.Head, Part: part}, nil
		}
		return core.RelicPartView{}, core.Errorf(core.CategoryNotFound, "relic %s has no part %s", document.ID, partID)
	}
}

func (fake *relicPageFake) reads() (map[string]int, int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return maps.Clone(fake.partReads), fake.maxInFlight
}

// handler is the phone board's tasks with this fake's relics.
func (fake *relicPageFake) handler(t *testing.T) http.Handler {
	t.Helper()
	options := newPhoneBoard().options(t)
	fake.apply(&options)
	return NewHandler(options)
}

func textBody(format, text string) core.RelicDocumentBody {
	return core.RelicDocumentBody{Format: format, Version: "v-" + format, Bytes: int64(len(text)), Text: &text}
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// relicListFake is four relics: two live, one retired, one deleted.
func relicListFake() *relicPageFake {
	summary := func(id, kind, title string) core.RelicSummary {
		return core.RelicSummary{ID: id, Head: "h-" + id, Title: title, Type: kind, Author: "writer@example.com",
			CreatedAt: pageRelicStamp, UpdatedAt: pageRelicStamp, Parts: 2}
	}
	retired := summary(pageRelicOld, "spec", "Old spec")
	retired.Retired = true
	deleted := summary(pageRelicGone, "note", "Gone note")
	deleted.Deleted = true
	fake := &relicPageFake{summaries: []core.RelicSummary{
		summary(pageRelicID, "spec", "Compaction spec"), summary(pageRelicPlan, "plan", "Compaction plan"), retired, deleted,
	}}
	for _, item := range fake.summaries {
		fake.documents = append(fake.documents, core.RelicDocument{Format: core.RelicDocumentFormat, Version: 1, ID: item.ID,
			Head: item.Head, Title: item.Title, Type: item.Type, Retired: item.Retired, Deleted: item.Deleted})
	}
	return fake
}

// specRelicFake is one spec relic with a Markdown, an HTML and a binary part.
func specRelicFake(t *testing.T) *relicPageFake {
	t.Helper()
	created, updated := pageRelicStamp, pageRelicStamp.Add(26*time.Hour)
	document := core.RelicDocument{
		Format: core.RelicDocumentFormat, Version: 1, ID: pageRelicID, Head: "head-spec", Title: "Compaction spec", Type: "spec",
		Schema:           &core.RelicSchemaRef{Type: "spec", ID: "01M8SCHEMA00000000000000002", Version: 2},
		RelicConformance: core.RelicConformance{State: core.RelicConformanceConforms},
		Author:           "writer@example.com", Retired: true, CreatedAt: &created, UpdatedAt: &updated,
		Properties: map[string]json.RawMessage{
			"status": rawJSON(t, "draft"), "owners": rawJSON(t, []string{"ana", "bo"}), "estimate": rawJSON(t, 3),
		},
		Parts: []core.RelicDocumentPart{
			{ID: pagePartMD, Type: "section", Fields: map[string]json.RawMessage{"heading": rawJSON(t, "Goals"), "title": rawJSON(t, "not this")},
				Body: &core.RelicDocumentBody{Format: "md", Version: "v1", Bytes: 64}},
			{ID: pagePartHTML, Type: "page", Fields: map[string]json.RawMessage{"title": rawJSON(t, "Rendered page")},
				Body: &core.RelicDocumentBody{Format: "html", Version: "v2", Bytes: 40}},
			{ID: pagePartBin, Type: "blob", Fields: map[string]json.RawMessage{},
				Body: &core.RelicDocumentBody{Format: "bin", Version: "v3", Bytes: 3}},
		},
		Links: []core.RelicDocumentLink{
			{RelicLink: core.RelicLink{ID: "l1", To: core.RelicLinkTarget{Task: pageRelicTask}, Rel: "specifies"}},
		},
	}
	encoded := "AAEC"
	return &relicPageFake{
		documents: []core.RelicDocument{document},
		bodies: map[string]core.RelicDocumentBody{
			pagePartMD:   textBody("md", "# Goals\n\nKeep **history**.\n\n| Field | Kind |\n| --- | --- |\n| title | text |"),
			pagePartHTML: textBody("html", "<h1>Page</h1><script>window.__ran = 1</script>"),
			pagePartBin:  {Format: "bin", Version: "v3", Bytes: 3, Base64: &encoded},
		},
	}
}

// relicPageHelpers is what every relic page script reaches for.
const relicPageHelpers = pageGeometryHelpers + `
const relicView = () => document.querySelector("[data-relic-view]");
const relicState = () => relicView() ? relicView().dataset.relicState : undefined;
const waitForRelicState = (want) => waitFor(() => relicState() === want, "the relic view to be " + want + " (it is " + relicState() + ")");
const bodyStates = () => [...document.querySelectorAll("[data-body-state]")].map((element) => element.dataset.bodyState);
// Brings each part whose body is still waiting into view, the way a reader
// scrolling down the page does, and waits for every body to settle.
const waitForBodies = async () => {
  for (;;) {
    const waiting = [...document.querySelectorAll("[data-body-state=\"waiting\"]")][0];
    if (!waiting) break;
    waiting.closest("[data-relic-part]").scrollIntoView();
    await waitFor(() => waiting.dataset.bodyState !== "waiting" && waiting.dataset.bodyState !== "loading", "a body brought into view to settle");
  }
  await waitFor(() => {
    const states = bodyStates();
    return states.length > 0 && states.every((state) => state !== "waiting" && state !== "loading");
  }, "every body to settle");
};
const partSection = (id) => document.querySelector("[data-relic-part=\"" + id + "\"]");
const factText = (name) => { const fact = document.querySelector("[data-relic-fact=\"" + name + "\"]"); return fact ? fact.textContent.trim() : null; };
`

func TestBrowserRelicListFiltersAndLinks(t *testing.T) {
	t.Parallel()
	handler := relicListFake().handler(t)
	var result struct {
		First, Back []relicListRow
		Planned     struct {
			Address string
			Rows    []relicListRow
		}
		Title string
		Note  string
	}
	openInBrowser(t, handler, browserPage{Width: 1440, Height: 900, Path: "/relics", Script: relicPageHelpers + relicListRows + `
(async () => {
  await waitForRelicState("ready");
  const first = listRows();
  const select = document.querySelector("[data-relic-filter=\"type\"]");
  select.value = "plan";
  select.dispatchEvent(new Event("change", { bubbles: true }));
  await waitFor(() => location.search === "?type=plan", "the address to name the type");
  await waitForRelicState("ready");
  const planned = { address: location.pathname + location.search, rows: listRows() };
  history.back();
  await waitFor(() => location.search === "", "Back to the full list");
  await waitForRelicState("ready");
  return { first, planned, back: listRows(), title: document.title, note: document.querySelector("[data-relic-note]").textContent };
})()`}, &result)
	ids := func(rows []relicListRow) []string {
		var found []string
		for _, row := range rows {
			found = append(found, row.ID)
			if row.Href != "/relics/"+row.ID {
				t.Errorf("row %s links to %q", row.ID, row.Href)
			}
		}
		return found
	}
	if got, want := ids(result.First), []string{pageRelicID, pageRelicPlan}; !slices.Equal(got, want) {
		t.Errorf("default list = %v, want %v", got, want)
	}
	if result.Planned.Address != "/relics?type=plan" || !slices.Equal(ids(result.Planned.Rows), []string{pageRelicPlan}) {
		t.Errorf("type plan = %+v", result.Planned)
	}
	if got := ids(result.Back); !slices.Equal(got, []string{pageRelicID, pageRelicPlan}) {
		t.Errorf("after Back = %v", got)
	}
	if !strings.HasPrefix(result.Title, "Relics · ") {
		t.Errorf("title = %q", result.Title)
	}
	if !strings.Contains(result.Note, "workbook sync") {
		t.Errorf("list note = %q", result.Note)
	}

	var deleted struct{ Rows []relicListRow }
	openInBrowser(t, handler, browserPage{Width: 1440, Height: 900, Path: "/relics?deleted=1", Script: relicPageHelpers + relicListRows + `
(async () => { await waitForRelicState("ready"); return { rows: listRows() }; })()`}, &deleted)
	if got := ids(deleted.Rows); !slices.Equal(got, []string{pageRelicID, pageRelicPlan, pageRelicGone}) {
		t.Fatalf("?deleted=1 lists %v", got)
	}
	if badges := deleted.Rows[2].Badges; !slices.Equal(badges, []string{"Deleted"}) {
		t.Errorf("the deleted relic's badges = %v", badges)
	}
	if badges := deleted.Rows[0].Badges; len(badges) != 0 {
		t.Errorf("a live relic carries badges %v", badges)
	}
}

type relicListRow struct {
	ID, Href, Text string
	Badges         []string
}

const relicListRows = `
const listRows = () => [...document.querySelectorAll("[data-relic-row]")].map((row) => ({
  id: row.dataset.relicRow,
  href: row.querySelector("a").getAttribute("href"),
  text: row.textContent,
  badges: [...row.querySelectorAll("[data-relic-badge]")].map((badge) => badge.textContent),
}));
`

func TestBrowserRelicPageShowsEnforcedFieldsAndParts(t *testing.T) {
	t.Parallel()
	fake := specRelicFake(t)
	var result struct {
		Heading, Note       string
		Facts               map[string]string
		Badges              []string
		Properties          map[string]string
		Parts               []string
		Captions            []string
		MarkdownHeading     string
		TableCells          []string
		HTMLLabel, HTMLText string
		BinaryText          string
		States              map[string]string
		Opens               []struct{ Href, Target, Rel, Text string }
		Links               []struct{ Text, Href string }
		Title               string
		Ran                 bool
	}
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  await waitForBodies();
  const view = relicView();
  const facts = {};
  view.querySelectorAll("[data-relic-fact]").forEach((fact) => { facts[fact.dataset.relicFact] = fact.textContent.trim(); });
  const properties = {};
  view.querySelectorAll("[data-relic-property]").forEach((value) => { properties[value.dataset.relicProperty] = value.textContent; });
  const parts = [...view.querySelectorAll("[data-relic-part]")];
  const states = {};
  parts.forEach((part) => { states[part.dataset.relicPart] = part.querySelector("[data-body-state]").dataset.bodyState; });
  const md = partSection("` + pagePartMD + `").querySelector("[data-body-state]");
  const html = partSection("` + pagePartHTML + `").querySelector("[data-body-state]");
  return {
    heading: view.querySelector("h2").textContent,
    note: view.querySelector("[data-relic-readonly]").textContent,
    facts,
    badges: [...view.querySelectorAll("[data-relic-badge]")].map((badge) => badge.textContent),
    properties,
    parts: parts.map((part) => part.dataset.relicPart),
    captions: parts.map((part) => part.querySelector("h3").textContent),
    markdownHeading: md.querySelector(".markdown h3").textContent,
    tableCells: [...md.querySelectorAll("table th, table td")].map((cell) => cell.textContent),
    htmlLabel: html.querySelector("[data-body-label]").textContent,
    htmlText: html.querySelector("pre").textContent,
    binaryText: partSection("` + pagePartBin + `").querySelector("[data-body-state]").textContent.trim(),
    states,
    opens: [...view.querySelectorAll("a[data-relic-body-open]")].map((link) => ({ href: link.getAttribute("href"), target: link.target, rel: link.rel, text: link.textContent })),
    links: [...view.querySelectorAll("[data-relic-link]")].map((item) => ({ text: item.textContent, href: item.querySelector("a") ? item.querySelector("a").getAttribute("href") : "" })),
    title: document.title,
    ran: window.__ran === 1,
  };
})()`}, &result)
	if result.Heading != "Compaction spec" || !strings.Contains(result.Note, "workbook relic update") {
		t.Errorf("heading %q, note %q", result.Heading, result.Note)
	}
	for name, want := range map[string]string{
		"type": "spec", "author": "writer@example.com", "schema": "spec version 2", "conformance": "conforms",
	} {
		if result.Facts[name] != want {
			t.Errorf("fact %s = %q, want %q", name, result.Facts[name], want)
		}
	}
	if !strings.Contains(result.Facts["id"], pageRelicID) {
		t.Errorf("fact id = %q", result.Facts["id"])
	}
	for _, name := range []string{"created", "updated"} {
		if result.Facts[name] == "" {
			t.Errorf("fact %s is empty", name)
		}
	}
	if result.Facts["created"] == result.Facts["updated"] {
		t.Errorf("created and updated read the same: %q", result.Facts["created"])
	}
	if !slices.Equal(result.Badges, []string{"Retired"}) {
		t.Errorf("badges = %v", result.Badges)
	}
	if result.Properties["status"] != "draft" || result.Properties["estimate"] != "3" ||
		result.Properties["owners"] != "[\n  \"ana\",\n  \"bo\"\n]" {
		t.Errorf("properties = %#v", result.Properties)
	}
	if !slices.Equal(result.Parts, []string{pagePartMD, pagePartHTML, pagePartBin}) {
		t.Errorf("parts = %v", result.Parts)
	}
	if !slices.Equal(result.Captions, []string{"Goals", "Rendered page", "blob"}) {
		t.Errorf("captions = %v", result.Captions)
	}
	if result.MarkdownHeading != "Goals" || !slices.Equal(result.TableCells, []string{"Field", "Kind", "title", "text"}) {
		t.Errorf("markdown heading %q, cells %v", result.MarkdownHeading, result.TableCells)
	}
	if result.HTMLLabel != "HTML, shown as source" || result.HTMLText != "<h1>Page</h1><script>window.__ran = 1</script>" {
		t.Errorf("html body %q %q", result.HTMLLabel, result.HTMLText)
	}
	if result.Ran {
		t.Error("the HTML body's script ran on the board")
	}
	if result.BinaryText != "not text, 3 B" {
		t.Errorf("binary body = %q", result.BinaryText)
	}
	if want := map[string]string{pagePartMD: "drawn", pagePartHTML: "drawn", pagePartBin: "binary"}; fmt.Sprint(result.States) != fmt.Sprint(want) {
		t.Errorf("body states = %v, want %v", result.States, want)
	}
	if len(result.Opens) != 3 {
		t.Fatalf("open links = %+v", result.Opens)
	}
	for index, part := range []string{pagePartMD, pagePartHTML, pagePartBin} {
		open := result.Opens[index]
		if open.Href != "/relics/"+pageRelicID+"/parts/"+part+"/body" || open.Target != "_blank" ||
			!slices.Contains(strings.Fields(open.Rel), "noopener") || open.Text != "Open in new tab" {
			t.Errorf("open link %d = %+v", index, open)
		}
	}
	if len(result.Links) != 1 || result.Links[0].Href != "/tasks/"+pageRelicTask ||
		!strings.Contains(result.Links[0].Text, "specifies") || !strings.Contains(result.Links[0].Text, pageRelicTask) {
		t.Errorf("links = %+v", result.Links)
	}
	if result.Title != "Compaction spec · "+phoneProjectName {
		t.Errorf("document title = %q", result.Title)
	}
}

func TestBrowserRelicPartAddressScrollsToThePart(t *testing.T) {
	t.Parallel()
	fake := specRelicFake(t)
	document := &fake.documents[0]
	// Enough parts ahead of the one addressed that it starts off the screen.
	long := strings.Repeat("A line of the plan that takes room.\n\n", 30)
	var parts []core.RelicDocumentPart
	for index := range 24 {
		id := fmt.Sprintf("01M8EVZ9BQ20NDMR0F09K3CH%02d", index)
		parts = append(parts, core.RelicDocumentPart{ID: id, Type: "section", Fields: map[string]json.RawMessage{},
			Body: &core.RelicDocumentBody{Format: "md", Version: "v", Bytes: int64(len(long))}})
		fake.bodies[id] = textBody("md", long)
	}
	document.Parts = append(parts, document.Parts...)
	var result struct {
		Address string
		Current []string
		Top     float64
		Bottom  float64
		Height  float64
	}
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900,
		Path: "/relics/" + pageRelicID[:12] + "/parts/" + pagePartHTML[:23], Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  const part = partSection("` + pagePartHTML + `");
  await waitFor(() => part.querySelector("[data-body-state]").dataset.bodyState === "drawn", "the part's body");
  const rect = part.getBoundingClientRect();
  return {
    address: location.pathname,
    current: [...document.querySelectorAll("[data-relic-part][aria-current=\"true\"]")].map((element) => element.dataset.relicPart),
    top: rect.top, bottom: rect.bottom, height: innerHeight,
  };
})()`}, &result)
	if want := "/relics/" + pageRelicID + "/parts/" + pagePartHTML; result.Address != want {
		t.Errorf("address = %q, want %q", result.Address, want)
	}
	if !slices.Equal(result.Current, []string{pagePartHTML}) {
		t.Errorf("aria-current parts = %v", result.Current)
	}
	if result.Top < 0 || result.Top >= result.Height {
		t.Errorf("the part's top is at %.0f in a %.0f pixel viewport", result.Top, result.Height)
	}
}

// hostileText is in every string a teammate writes: markup, a script, a
// javascript: address, a relative address and a direction override.
const hostileText = `<img src=/hostile-img onerror="window.__hostile=1"><script>window.__hostile=2</script> javascript:window.__hostile=3 ` + "\u202e" + `desrever`

// hostileBeforeLoad watches the page from its first line: script errors, any
// attribute set whose name starts with on, and any node added carrying one.
const hostileBeforeLoad = `
window.__relicWatch = { errors: [], events: [], violations: [] };
window.addEventListener("error", (event) => window.__relicWatch.errors.push(String(event.message)));
window.addEventListener("securitypolicyviolation", (event) => window.__relicWatch.violations.push(event.blockedURI));
const relicWatchSet = Element.prototype.setAttribute;
Element.prototype.setAttribute = function (name, value) {
  if (/^on/i.test(String(name))) window.__relicWatch.events.push(String(name));
  return relicWatchSet.call(this, name, value);
};
const relicWatchElement = (node) => {
  if (!node || node.nodeType !== 1) return;
  for (const attribute of node.attributes) if (/^on/i.test(attribute.name)) window.__relicWatch.events.push(node.tagName + " " + attribute.name);
  for (const child of node.children) relicWatchElement(child);
};
new MutationObserver((records) => {
  for (const record of records) {
    if (record.type === "attributes" && /^on/i.test(record.attributeName)) window.__relicWatch.events.push(record.attributeName);
    record.addedNodes.forEach(relicWatchElement);
  }
}).observe(document, { subtree: true, childList: true, attributes: true });
`

// relicViewTags is every element the relic views draw.
var relicViewTags = []string{
	"SECTION", "HEADER", "H2", "H3", "H4", "H5", "H6", "P", "DL", "DT", "DD", "DIV", "SPAN", "A", "BUTTON", "CODE", "PRE",
	"TIME", "UL", "OL", "LI", "NAV", "FORM", "LABEL", "SELECT", "OPTION", "INPUT", "STRONG", "EM", "BLOCKQUOTE", "BR",
	"TABLE", "THEAD", "TBODY", "TR", "TH", "TD",
}

func hostileRelicFake(t *testing.T) *relicPageFake {
	t.Helper()
	hostile := rawJSON(t, hostileText)
	markdown := "# " + hostileText + "\n\n" + hostileText + "\n\n[click](javascript:window.__hostile=4) [near](/hostile-relative) " +
		"![remote](https://hostile.test/i.png) ![local](/hostile-img2) <a href=/hostile-a onclick=alert(1)>a</a>\n\n" +
		"| <script>window.__hostile=5</script> |\n| --- |\n| " + hostileText + " |"
	document := core.RelicDocument{
		Format: core.RelicDocumentFormat, Version: 1, ID: pageRelicID, Head: "head-hostile", Title: hostileText, Type: hostileText,
		Schema:           &core.RelicSchemaRef{Type: hostileText, ID: "01M8SCHEMA00000000000000002", Version: 1},
		RelicConformance: core.RelicConformance{State: core.RelicConformanceNotConforming, Problem: hostileText},
		Author:           hostileText,
		Properties:       map[string]json.RawMessage{hostileText: hostile, "list": rawJSON(t, []string{hostileText})},
		Parts: []core.RelicDocumentPart{
			{ID: pagePartMD, Type: hostileText, Fields: map[string]json.RawMessage{"heading": hostile, hostileText: hostile},
				Body: &core.RelicDocumentBody{Format: "md", Version: "v1", Bytes: int64(len(markdown))}},
			{ID: pagePartHTML, Type: hostileText, Fields: map[string]json.RawMessage{},
				Body: &core.RelicDocumentBody{Format: "html", Version: "v2", Bytes: 1}},
			{ID: pagePartBin, Type: "text", Fields: map[string]json.RawMessage{"title": hostile},
				Body: &core.RelicDocumentBody{Format: hostileText, Version: "v3", Bytes: 1}},
		},
		Links: []core.RelicDocumentLink{
			{RelicLink: core.RelicLink{ID: "l1", To: core.RelicLinkTarget{Task: pageRelicTask}, Rel: hostileText}},
			{RelicLink: core.RelicLink{ID: "l2", From: pagePartMD, To: core.RelicLinkTarget{Relic: "javascript:window.__hostile=6", Part: hostileText}, Rel: hostileText}},
		},
	}
	return &relicPageFake{
		summaries: []core.RelicSummary{{ID: pageRelicID, Head: "head-hostile", Title: hostileText, Type: hostileText, Author: hostileText,
			CreatedAt: pageRelicStamp, UpdatedAt: pageRelicStamp, Parts: 3}},
		documents: []core.RelicDocument{document},
		bodies: map[string]core.RelicDocumentBody{
			pagePartMD:   textBody("md", markdown),
			pagePartHTML: textBody("html", hostileText+`<iframe src="/hostile-frame"></iframe>`),
			pagePartBin:  textBody(hostileText, hostileText),
		},
	}
}

// Review Focus 2: nothing a teammate wrote becomes markup on the board page.
func TestBrowserRelicPageDrawsHostileContentAsText(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/relics/" + pageRelicID, "/relics"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			fake := hostileRelicFake(t)
			options := newPhoneBoard().options(t)
			fake.apply(&options)
			recorder := &requestRecorder{}
			tags, err := json.Marshal(relicViewTags)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Watch struct {
					Errors, Events, Violations []string
				}
				Hostile    any
				Strays     []string
				Attributes []string
				Anchors    []string
				Resources  []string
				Text       string
				Title      string
			}
			openInBrowser(t, guardedRecordedBoard(options, recorder), browserPage{Width: 1440, Height: 900, Path: path,
				BeforeLoad: hostileBeforeLoad, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  if (location.pathname !== "/relics") await waitForBodies();
  const allowed = ` + string(tags) + `;
  const view = relicView();
  const elements = [...view.querySelectorAll("*")];
  return {
    watch: window.__relicWatch,
    hostile: window.__hostile === undefined ? null : window.__hostile,
    strays: elements.filter((element) => !allowed.includes(element.tagName)).map((element) => element.tagName),
    attributes: elements.flatMap((element) => [...element.attributes].map((attribute) => attribute.name)).filter((name) => /^on/i.test(name)),
    anchors: [...view.querySelectorAll("a")].map((anchor) => anchor.getAttribute("href")),
    resources: performance.getEntriesByType("resource").map((entry) => entry.name),
    text: view.textContent,
    title: document.title,
  };
})()`}, &result)
			if len(result.Watch.Errors) != 0 || len(result.Watch.Events) != 0 || len(result.Watch.Violations) != 0 || result.Hostile != nil {
				t.Errorf("a hook fired: %+v, window.__hostile = %v", result.Watch, result.Hostile)
			}
			if len(result.Strays) != 0 {
				t.Errorf("the relic view drew elements outside its list: %v", result.Strays)
			}
			if len(result.Attributes) != 0 {
				t.Errorf("the relic view carries event attributes: %v", result.Attributes)
			}
			for _, href := range result.Anchors {
				if !strings.HasPrefix(href, "/relics/") && !strings.HasPrefix(href, "/tasks/") {
					t.Errorf("the relic view links to %q", href)
				}
			}
			for _, resource := range result.Resources {
				if strings.Contains(resource, "hostile") {
					t.Errorf("the page fetched %s", resource)
				}
			}
			for _, seen := range recorder.seen() {
				target := strings.TrimPrefix(seen, "GET ")
				allowed := target == path || strings.HasPrefix(target, "/api/relics") ||
					strings.HasPrefix(target, "/api/tasks") || strings.HasPrefix(target, "/api/sync") || target == manifestPath
				if _, icon := iconFiles[target]; icon {
					allowed = true
				}
				if !allowed || strings.Contains(target, "hostile") {
					t.Errorf("a request left the page: %s", seen)
				}
			}
			shown := strings.ReplaceAll(hostileText, "\u202e", " ")
			if !strings.Contains(result.Text, shown) {
				t.Errorf("the hostile text is not drawn as text:\n%s", result.Text)
			}
			if strings.Contains(result.Text, "\u202e") || strings.Contains(result.Title, "\u202e") {
				t.Error("a direction override was drawn")
			}
		})
	}
}

// Review Focus 3: a 2,000-part relic with 1 MiB bodies loads only what is near
// the screen, four at a time, each once.
//
// It is not parallel: its page keeps one browser busy for many seconds, and
// run beside the package's other browsers it both timed out under load and
// starved them. Alone, it runs before the parallel tests resume.
func TestBrowserLargeRelicLoadsBodiesAsTheyNearTheScreen(t *testing.T) {
	large := "# A large body\n\n" + strings.Repeat("word ", (1<<20)/5)
	document := core.RelicDocument{Format: core.RelicDocumentFormat, Version: 1, ID: pageRelicID, Head: "head-large",
		Title: "Large relic", Type: "transcript"}
	for index := range 2000 {
		document.Parts = append(document.Parts, core.RelicDocumentPart{ID: fmt.Sprintf("01M8EVZ9BQ20NDMR0F09K%05d", index), Type: "turn",
			Fields: map[string]json.RawMessage{"heading": rawJSON(t, fmt.Sprintf("Turn %d", index))},
			Body:   &core.RelicDocumentBody{Format: "md", Version: "v", Bytes: int64(len(large))}})
	}
	fake := &relicPageFake{documents: []core.RelicDocument{document}, largeBody: &large, partDelay: 40 * time.Millisecond}
	last := document.Parts[len(document.Parts)-1].ID
	var result struct {
		AtReady, AfterNarrowing, AfterScroll int
		LastState                            string
		Sections                             int
	}
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  await waitFor(() => relicView().dataset.bodiesPending === "0" && bodyStates().includes("drawn"), "the bodies near the screen to load");
  const requested = () => performance.getEntriesByType("resource").filter((entry) => entry.name.includes("/parts/")).length;
  const atReady = requested();
  // What the narrow layout does when a window shrinks or a phone turns: main
  // stops scrolling and the document scrolls instead. A script cannot resize
  // its own window, so the rules the narrow layout applies are set here.
  document.body.style.height = "auto";
  document.body.style.overflow = "visible";
  document.querySelector("main").style.flex = "0 0 auto";
  document.querySelector("main").style.overflow = "visible";
  window.dispatchEvent(new Event("resize"));
  await new Promise((resolve) => setTimeout(resolve, 500));
  await waitFor(() => relicView().dataset.bodiesPending === "0", "the queue to empty after the layout narrowed");
  const afterNarrowing = requested();
  const lastPart = partSection("` + last + `");
  lastPart.scrollIntoView();
  await waitFor(() => lastPart.querySelector("[data-body-state]").dataset.bodyState === "drawn", "the last part's body");
  await waitFor(() => relicView().dataset.bodiesPending === "0", "the queue to empty");
  return { atReady, afterNarrowing, afterScroll: requested(), lastState: lastPart.querySelector("[data-body-state]").dataset.bodyState, sections: document.querySelectorAll("[data-relic-part]").length };
})()`}, &result)
	reads, most := fake.reads()
	t.Logf("bodies read before scrolling %d, after the layout narrowed %d, after scrolling %d, at most %d at once",
		result.AtReady, result.AfterNarrowing, result.AfterScroll, most)
	if result.Sections != 2000 {
		t.Errorf("the page drew %d part sections, want 2000", result.Sections)
	}
	if result.AtReady == 0 || result.AtReady >= 40 {
		t.Errorf("the page asked for %d bodies before scrolling, want between 1 and 39", result.AtReady)
	}
	if result.AfterNarrowing >= 40 {
		t.Errorf("the page had asked for %d bodies once the document scrolled instead of main, want under 40", result.AfterNarrowing)
	}
	if most > 4 {
		t.Errorf("%d body reads were in flight at once, want at most 4", most)
	}
	if reads[last] != 1 || result.LastState != "drawn" {
		t.Errorf("the last part was read %d times and is %q", reads[last], result.LastState)
	}
	total := 0
	for part, count := range reads {
		total += count
		if count != 1 {
			t.Errorf("part %s was read %d times", part, count)
		}
	}
	if total != result.AfterScroll {
		t.Errorf("the server saw %d part reads and the page made %d", total, result.AfterScroll)
	}
}

func TestBrowserRelicPagesReportFailures(t *testing.T) {
	t.Parallel()
	t.Run("a damaged relic stops the list", func(t *testing.T) {
		t.Parallel()
		fake := relicListFake()
		fake.listErr = core.Errorf(core.CategoryCorruptData, "relic WB-R-01M8EW0000DAMAGED000000009 cannot be read: its state is not valid JSON; run workbook validate")
		var result struct{ State, Notice string }
		openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics", Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("error");
  return { state: relicState(), notice: relicView().querySelector("[data-relic-notice]").textContent };
})()`}, &result)
		if !strings.Contains(result.Notice, "WB-R-01M8EW0000DAMAGED000000009") || !strings.Contains(result.Notice, "workbook validate") {
			t.Errorf("notice = %q", result.Notice)
		}
	})
	t.Run("an unknown relic", func(t *testing.T) {
		t.Parallel()
		var result struct{ Notice, Title string }
		openInBrowser(t, relicListFake().handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/WB-R-NOPE", Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("not-found");
  return { notice: relicView().querySelector("[data-relic-notice]").textContent, title: document.title };
})()`}, &result)
		if !strings.Contains(result.Notice, "No relic WB-R-NOPE in this checkout") {
			t.Errorf("notice = %q", result.Notice)
		}
	})
	t.Run("a part whose body cannot be read", func(t *testing.T) {
		t.Parallel()
		fake := specRelicFake(t)
		fake.partErrs = map[string]error{pagePartHTML: core.Errorf(core.CategoryCorruptData, "part %s of relic %s cannot be read", pagePartHTML, pageRelicID)}
		var result struct {
			State  string
			States map[string]string
			Error  string
		}
		openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  await waitForBodies();
  const states = {};
  document.querySelectorAll("[data-relic-part]").forEach((part) => { states[part.dataset.relicPart] = part.querySelector("[data-body-state]").dataset.bodyState; });
  return { state: relicState(), states, error: partSection("` + pagePartHTML + `").querySelector("[data-body-state]").textContent };
})()`}, &result)
		if want := map[string]string{pagePartMD: "drawn", pagePartHTML: "error", pagePartBin: "binary"}; fmt.Sprint(result.States) != fmt.Sprint(want) {
			t.Errorf("body states = %v, want %v", result.States, want)
		}
		if !strings.Contains(result.Error, "cannot be read") {
			t.Errorf("the failed body says %q", result.Error)
		}
	})
}

func TestBrowserRelicPagesFitPhoneAndDesktop(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		width, height int
		phone         bool
	}{{390, 844, true}, {1440, 900, false}} {
		for _, path := range []string{"/relics", "/relics/" + pageRelicID} {
			t.Run(fmt.Sprintf("%s-%dx%d", path, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				fake := specRelicFake(t)
				fake.summaries = relicListFake().summaries
				fake.bodies[pagePartHTML] = textBody("html", "<p>"+strings.Repeat("x", 5000)+"</p>")
				var result struct {
					ScrollWidth, InnerWidth int
					Source                  struct{ ScrollWidth, ClientWidth, Right int }
					Unnamed                 []string
					ShownHidden             []string
					Lead, BoardLead         string
				}
				openInBrowser(t, fake.handler(t), browserPage{Width: size.width, Height: size.height, Phone: size.phone, Path: path, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  if (location.pathname !== "/relics") await waitForBodies();
  const pre = document.querySelector("[data-body-state] pre");
  const name = (element) => (element.getAttribute("aria-label") || "").trim() ||
    (element.getAttribute("aria-labelledby") ? element.getAttribute("aria-labelledby").split(/\s+/).map((id) => (document.getElementById(id) || {}).textContent || "").join(" ").trim() : "") ||
    (element.labels && element.labels.length ? [...element.labels].map((label) => label.textContent).join(" ").trim() : "") ||
    element.textContent.trim();
  const unnamed = [...relicView().querySelectorAll("a, button, select, input")].filter((element) => !name(element)).map((element) => element.outerHTML.slice(0, 120));
  const shownHidden = [...document.querySelectorAll("[hidden]")].filter((element) => getComputedStyle(element).display !== "none").map((element) => element.outerHTML.slice(0, 120));
  const lead = () => { const copy = document.querySelector(".app-header__lead").cloneNode(true); copy.querySelectorAll("[aria-current]").forEach((element) => element.removeAttribute("aria-current")); return copy.outerHTML; };
  const result = {
    scrollWidth: document.documentElement.scrollWidth, innerWidth,
    source: pre ? { scrollWidth: pre.scrollWidth, clientWidth: pre.clientWidth, right: Math.round(pre.getBoundingClientRect().right) } : { scrollWidth: 0, clientWidth: 0, right: 0 },
    unnamed, shownHidden, lead: lead(),
  };
  document.querySelector("a.header-link[href=\"/\"]").click();
  await waitFor(() => location.pathname === "/" && !document.querySelector("[data-filter-row]").hidden, "the board");
  result.boardLead = lead();
  return result;
})()`}, &result)
				if result.ScrollWidth > result.InnerWidth {
					t.Errorf("the page scrolls sideways: %d > %d", result.ScrollWidth, result.InnerWidth)
				}
				if path != "/relics" {
					if result.Source.ScrollWidth <= result.Source.ClientWidth || result.Source.Right > result.InnerWidth {
						t.Errorf("the HTML source does not scroll inside its box: %+v in %d", result.Source, result.InnerWidth)
					}
				}
				if len(result.Unnamed) != 0 {
					t.Errorf("controls without an accessible name: %v", result.Unnamed)
				}
				if len(result.ShownHidden) != 0 {
					t.Errorf("hidden elements are drawn: %v", result.ShownHidden)
				}
				if result.Lead != result.BoardLead {
					t.Errorf("the header differs from the board's:\n%s\nboard:\n%s", result.Lead, result.BoardLead)
				}
			})
		}
	}
}

// Every relic page address is the board's page, and none is served by a board
// that cannot read relics.
func TestRelicRoutesAreBoardShells(t *testing.T) {
	t.Parallel()
	paths := []string{
		"/relics", "/relics?type=spec&deleted=1", "/relics/" + pageRelicID, "/relics/" + pageRelicID + "/parts/" + pagePartMD,
		"/relics/" + pageRelicID + "/history", "/relics/" + pageRelicID + "/diff",
	}
	reading := specRelicFake(t).handler(t)
	options := newPhoneBoard().options(t)
	plain := NewHandler(options)
	for _, path := range paths {
		response := request(t, reading, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, response.Code)
			continue
		}
		body := response.Body.String()
		for _, want := range []string{`<a class="header-link" href="/relics">Relics</a>`, `<section class="relic-view" data-relic-view`, `data-board-view`} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s does not carry %s", path, want)
			}
		}
		if response := request(t, plain, http.MethodGet, path); response.Code != http.StatusNotFound {
			t.Errorf("GET %s on a board without relics = %d, want 404", path, response.Code)
		}
	}
	board := request(t, plain, http.MethodGet, "/").Body.String()
	if strings.Contains(board, `href="/relics"`) || strings.Contains(board, `<section class="relic-view"`) {
		t.Error("a board without relics offers them")
	}
	// The link sits after Board and before Config.
	unreached := func() (VocabularyMutation, error) { return VocabularyMutation{}, nil }
	options.AddStatus = func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) { return unreached() }
	options.EditStatus = func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
		return unreached()
	}
	options.RemoveStatus = func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
		return unreached()
	}
	options.ReorderStatus = func(context.Context, VocabularyOrder) (VocabularyMutation, error) { return unreached() }
	specRelicFake(t).apply(&options)
	header := headerElement(t, request(t, NewHandler(options), http.MethodGet, "/").Body.String())
	boardAt, relics, config := strings.Index(header, `href="/"`), strings.Index(header, `href="/relics"`), strings.Index(header, `href="/config"`)
	if boardAt < 0 || relics < boardAt || config < relics {
		t.Errorf("the Relics link is not between Board and Config: %s", header)
	}
}
