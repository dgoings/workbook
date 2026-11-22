package webui

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The column a phone comes back to. It is the one it was last looking at on
// this project's board, its button is slid into view in the row — at once,
// without moving the page — and it is the column the phone draws from its
// first frame, chosen before any card of the board has arrived. Another
// project served at the same address keeps a column of its own.

type phoneRestoreReading struct {
	Pressed     []string
	Shown       []string
	RowScrolled float64
	TabLeft     float64
	TabRight    float64
	RowLeft     float64
	RowRight    float64
	PageScroll  float64
	// Kept is what the browser holds under window.__keptKey, when a test
	// names one.
	Kept    string
	Refused []string
	Errors  []string
}

// phoneRestoreScript reads the board a phone came back to: which column is
// pressed and shown, where the pressed button is in the row, and how far the
// page is scrolled.
const phoneRestoreScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card .task-id-copy") && document.querySelector("[data-updated-text]").textContent !== "Loaded from this checkout", "the board to load");
  await pause(300);
  await settle();
  const row = document.querySelector("[data-status-tabs]");
  const tabs = [...row.querySelectorAll(".status-tab")];
  const key = (tab) => tab.hasAttribute("data-status-tab-deleted") ? "deleted" : tab.dataset.statusTab;
  const pressed = tabs.filter((tab) => tab.getAttribute("aria-pressed") === "true");
  const reading = {
    Pressed: pressed.map(key),
    Shown: [...document.querySelectorAll(".column")].filter((column) => getComputedStyle(column).display !== "none")
      .map((column) => column.querySelector("[data-status]") ? column.querySelector("[data-status]").dataset.status : "deleted"),
    RowScrolled: row.scrollLeft,
    PageScroll: window.scrollY,
    Kept: (() => { if (!window.__keptKey) return ""; try { return localStorage.getItem(window.__keptKey) || ""; } catch (_) { return "unreadable"; } })(),
    Refused: window.__refused,
    Errors: window.__errors,
  };
  if (pressed.length) {
    const tab = pressed[0].getBoundingClientRect(), edge = row.getBoundingClientRect();
    Object.assign(reading, { TabLeft: tab.left, TabRight: tab.right, RowLeft: edge.left, RowRight: edge.right });
  }
  return reading;
})()
`

// phoneChooseScript taps the named column's button in the row, as a reader
// does, and leaves the page at its top.
func phoneChooseScript(column string) string {
	return phoneHelpers + `(async () => {
  const selector = ` + strconv.Quote(column) + ` === "deleted" ? "[data-status-tab-deleted]" : '[data-status-tab="` + column + `"]';
  await waitFor(() => document.querySelector(selector), "the button for ` + column + `");
  await tap(document.querySelector(selector));
  await pause(600);
  window.scrollTo(0, 0);
})()`
}

// A phone that chose the column at the far end of a row wider than its
// screen comes back to that column with its button slid into view, and the
// page where it was, at the top. The Deleted column is the last of all and
// the board draws it itself, after the page has loaded, so it is put back
// and slid to as well.
func TestBrowserPhoneBoardSlidesARememberedColumnIntoView(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{360, 740}, {430, 932}} {
		for _, column := range []struct{ name, path string }{{"archived", "/"}, {"deleted", "/?deleted=1"}} {
			t.Run(fmt.Sprintf("%s-%dx%d", column.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				var reading phoneRestoreReading
				openInBrowser(t, newPhoneBoardWith(sevenPhoneStatuses).handler(t), browserPage{
					Width: size.width, Height: size.height, Phone: true, Path: column.path,
					BeforeLoad: phoneWatch, Setup: phoneChooseScript(column.name), Reload: true, Script: phoneRestoreScript,
				}, &reading)
				if !slices.Equal(reading.Pressed, []string{column.name}) || !slices.Equal(reading.Shown, []string{column.name}) {
					t.Fatalf("the phone came back with %v pressed and %v shown, want %s", reading.Pressed, reading.Shown, column.name)
				}
				if reading.RowScrolled <= 0 || reading.TabLeft < reading.RowLeft-0.5 || reading.TabRight > reading.RowRight+0.5 {
					t.Errorf("the pressed button spans %.1f to %.1f in a row from %.1f to %.1f scrolled by %.1f: it was not slid into view",
						reading.TabLeft, reading.TabRight, reading.RowLeft, reading.RowRight, reading.RowScrolled)
				}
				if reading.PageScroll != 0 {
					t.Errorf("putting the column back scrolled the page by %.1fpx", reading.PageScroll)
				}
				if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
					t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
				}
			})
		}
	}
}

type phoneFirstFrameReading struct {
	// Cards is, for each card as it arrived in the document, the column it
	// arrived in and whether that column was drawn.
	Cards []struct {
		Column string
		Shown  bool
	}
	// Pressed and RowScrolled are the row as the first card arrived.
	Pressed     []string
	RowScrolled float64
	Final       []string
	Refused     []string
	Errors      []string
}

// phoneFirstFrameWatch records, from the page's first line, each card as it
// arrives in the document: which column it is in and whether that column is
// drawn at that moment, which is before the board's own script has run. The
// row is read as the first card arrives.
const phoneFirstFrameWatch = `
window.__cards = [];
new MutationObserver((records) => {
  for (const record of records) for (const node of record.addedNodes) {
    if (!(node instanceof Element)) continue;
    const cards = node.matches(".task-card") ? [node] : [...node.querySelectorAll(".task-card")];
    for (const card of cards) {
      const column = card.closest(".column");
      if (!window.__row) {
        const row = document.querySelector("[data-status-tabs]");
        window.__row = { pressed: [...row.querySelectorAll('[aria-pressed="true"]')].map((tab) => tab.dataset.statusTab), scrolled: row.scrollLeft };
      }
      window.__cards.push({ Column: column ? column.dataset.phoneColumn : "", Shown: column ? getComputedStyle(column).display !== "none" : false });
    }
  }
}).observe(document, { childList: true, subtree: true });
`

const phoneFirstFrameScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card .task-id-copy"), "the board to load");
  await settle();
  return {
    Cards: window.__cards,
    Pressed: window.__row ? window.__row.pressed : [],
    RowScrolled: window.__row ? window.__row.scrolled : 0,
    Final: [...document.querySelectorAll(".column")].filter((column) => getComputedStyle(column).display !== "none").map((column) => column.dataset.phoneColumn),
    Refused: window.__refused,
    Errors: window.__errors,
  };
})()
`

// The remembered column is the one drawn from the first frame: by the time
// the first card of the board is in the document — before the board's own
// script, which is at the end of the page, has run — that column's button is
// pressed and slid into view, and every card arrives already in the column
// that will be drawn or in one that will not, so no frame shows the column
// `workbook next` draws from first.
func TestBrowserPhoneBoardChoosesTheRememberedColumnBeforeItsCards(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{360, 740}, {430, 932}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			var reading phoneFirstFrameReading
			openInBrowser(t, newPhoneBoardWith(sevenPhoneStatuses).handler(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/",
				BeforeLoad: phoneWatch + rememberPhoneColumn(phoneProjectName, "status:archived") + phoneFirstFrameWatch,
				Script:     phoneFirstFrameScript,
			}, &reading)
			if len(reading.Cards) == 0 {
				t.Fatal("no card was seen arriving, so this checked nothing")
			}
			if !slices.Equal(reading.Pressed, []string{"archived"}) || reading.RowScrolled <= 0 {
				t.Errorf("as the first card arrived, the row pressed %v and was scrolled by %.1f, want archived slid into view", reading.Pressed, reading.RowScrolled)
			}
			for index, card := range reading.Cards {
				if card.Shown != (card.Column == "archived") {
					t.Errorf("card %d arrived in %s, which was drawn: %v; only archived should be", index, card.Column, card.Shown)
				}
			}
			if !slices.Equal(reading.Final, []string{"archived"}) {
				t.Errorf("once loaded the phone shows %v, want archived", reading.Final)
			}
			if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
				t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
			}
		})
	}
}

// projectSwitch serves one project's board and then, once asked at
// /__switch, another's, at the same address: the way a reader meets two
// local boards, each `workbook serve` on the same default port in turn.
type projectSwitch struct {
	first, second http.Handler
	switched      atomic.Bool
}

func (serve *projectSwitch) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/__switch" {
		serve.switched.Store(true)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if serve.switched.Load() {
		serve.second.ServeHTTP(writer, request)
		return
	}
	serve.first.ServeHTTP(writer, request)
}

// A column remembered on one project's board is that project's: the next
// project served at the same address opens on its own `next` column, and the
// first project's column is still kept for it. The same project served again
// — renamed nothing, keyed the same — opens where it was left, which is what
// shows the switch itself is not what forgot it.
func TestBrowserPhoneBoardKeepsAColumnPerProject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, project, key string
		want               string
	}{
		{name: "another project", project: "Borealis", key: "BX", want: "queued"},
		{name: "another name", project: "Borealis", want: "queued"},
		{name: "another key", project: phoneProjectName, key: "BX", want: "queued"},
		{name: "the same project", project: phoneProjectName, want: "doing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			second := newPhoneBoard()
			second.project, second.key = test.project, test.key
			serve := &projectSwitch{first: newPhoneBoard().handler(t), second: second.handler(t)}
			var reading phoneRestoreReading
			firstKey := phoneColumnStorageKey(core.KeySet{}, phoneProjectName)
			openInBrowser(t, serve, browserPage{
				Width: 390, Height: 844, Phone: true, Path: "/",
				BeforeLoad: phoneWatch + "window.__keptKey = " + strconv.Quote(firstKey) + ";",
				Setup:      phoneChooseScript("doing") + `.then(() => fetch("/__switch"))`,
				Reload:     true,
				Script:     phoneRestoreScript,
			}, &reading)
			if !slices.Equal(reading.Pressed, []string{test.want}) || !slices.Equal(reading.Shown, []string{test.want}) {
				t.Errorf("the second board opened with %v pressed and %v shown, want %s", reading.Pressed, reading.Shown, test.want)
			}
			if reading.Kept != "status:doing" {
				t.Errorf("the first project's column is kept as %q, want status:doing", reading.Kept)
			}
			if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
				t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
			}
		})
	}
}
