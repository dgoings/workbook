package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board on a phone, checked the way a phone's own browser opens it: a
// mobile viewport, a phone's user agent, and real touch input — taps, strokes
// and typing handed to the browser rather than events a script made up. The
// sizes are a narrow phone, a common one and a wide one.
var phoneSizes = []struct{ width, height int }{{360, 740}, {390, 844}, {430, 932}}

// phoneHelpers is prepended to every phone page script.
//
// tap scrolls a control into view and touches its middle. type and enter
// type into whatever has focus. sideways reports how far the page scrolls
// sideways. smallTargets lists every control a thumb has to hit inside root
// that is smaller than 44 by 44 CSS pixels, or reaches past the screen's
// edge, and largestShortfall is the same list's sizes; a link inside the text
// of a description or a comment is read rather than aimed at and is left out,
// and a check box, radio button or file input is measured by the label a
// reader actually touches.
const phoneHelpers = pageGeometryHelpers + `
const settle = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const tap = async (element) => {
  if (!element) throw new Error("nothing to tap");
  element.scrollIntoView({ block: "center", inline: "nearest" });
  await settle();
  const rect = element.getBoundingClientRect();
  await touchInput({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 });
  await settle();
};
const type = (text) => touchInput({ text });
const enter = () => touchInput({ key: "Enter" });
const sideways = () => document.documentElement.scrollWidth - document.documentElement.clientWidth;
const describe = (element) => element.tagName.toLowerCase() + (element.className && typeof element.className === "string" ? "." + element.className.trim().split(/\s+/).join(".") : "") +
  (element.textContent ? " " + JSON.stringify(element.textContent.trim().slice(0, 24)) : "");
const controls = (root) => {
  const found = [];
  const selector = 'a[href], button, select, input, textarea, [role="switch"], summary';
  for (const element of root.querySelectorAll(selector)) {
    if (element.closest(".skip-link")) continue;
    if (element.matches("a") && element.closest(".markdown")) continue;
    let target = element;
    if (element.matches('input[type="checkbox"], input[type="radio"], input[type="file"]')) {
      // Measured by the largest of its labels: a file input has a caption and
      // a button-like label, and the button is what a thumb is aimed at.
      const labels = [element.closest("label"), ...(element.id ? document.querySelectorAll('label[for="' + element.id + '"]') : [])].filter(Boolean);
      const area = (label) => { const rect = label.getBoundingClientRect(); return Math.min(rect.width, rect.height); };
      target = labels.sort((left, right) => area(right) - area(left))[0] || null;
      if (!target) continue;
    }
    const rect = target.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) continue;
    if (getComputedStyle(target).visibility !== "visible") continue;
    if (!found.includes(target)) found.push(target);
  }
  return found;
};
const smallTargets = (root) => controls(root || document).flatMap((target) => {
  const rect = target.getBoundingClientRect();
  const problems = [];
  if (rect.width < 43.99 || rect.height < 43.99) problems.push(describe(target) + " is " + rect.width.toFixed(1) + "x" + rect.height.toFixed(1));
  if (rect.left < -0.5 || rect.right > document.documentElement.clientWidth + 0.5) {
    if (!target.closest("[data-status-tabs]")) problems.push(describe(target) + " reaches from " + rect.left.toFixed(1) + " to " + rect.right.toFixed(1));
  }
  return problems;
});
const smallest = (root) => controls(root || document).reduce((least, target) => {
  const rect = target.getBoundingClientRect();
  return Math.min(least, rect.width, rect.height);
}, Infinity);
`

// phoneWatch runs in every document a phone page loads, before the board's
// own script: it collects anything the page's security policy refused and any
// error the page's script threw, so a test can say there were none.
const phoneWatch = `
window.__refused = [];
window.__errors = [];
document.addEventListener("securitypolicyviolation", (event) => window.__refused.push(event.violatedDirective + " " + event.blockedURI));
window.addEventListener("error", (event) => window.__errors.push(String(event.message)));
window.addEventListener("unhandledrejection", (event) => window.__errors.push(String(event.reason && event.reason.message || event.reason)));
`

// phoneShot names a screenshot for this page when WORKBOOK_TEST_SCREENSHOT_DIR
// asks for them.
func phoneShot(name string, width, height int) string {
	directory := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	if directory == "" {
		return ""
	}
	return filepath.Join(directory, fmt.Sprintf("phone-%s-%dx%d.png", name, width, height))
}

type phoneBoardReading struct {
	Pressed           []string
	Shown             []string
	ShownLabel        string
	TabCounts         map[string]string
	ColumnCounts      map[string]string
	Sideways          float64
	Small             []string
	Smallest          float64
	Draggable         int
	Drags             int
	ScrolledBy        float64
	RowTopBefore      float64
	RowTopAfter       float64
	RowPosition       string
	CardMovedBy       float64
	Partial           string
	RowStrokeSideways float64
	AfterTap          []string
	AfterTapShown     []string
	TabInRow          bool
	TabLeft           float64
	TabRight          float64
	RowLeft           float64
	RowRight          float64
	RowScrolled       float64
	Remembered        string
	ColumnUnderRow    float64
	MenuSideways      float64
	Refused           []string
	Errors            []string
}

// phoneBoardScript reads the board as a phone first draws it, strokes a card
// with a finger, then taps a column name the row cuts off and reads it
// again.
const phoneBoardScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card .task-id-copy") && document.querySelector("[data-updated-text]").textContent !== "Loaded from this checkout", "the board to load");
  await settle();
  const row = document.querySelector("[data-status-tabs]");
  const tabs = () => [...row.querySelectorAll(".status-tab")];
  const shownColumns = () => [...document.querySelectorAll(".column")].filter((column) => getComputedStyle(column).display !== "none")
    .map((column) => column.querySelector("[data-status]") ? column.querySelector("[data-status]").dataset.status : "deleted");
  const reading = {
    Pressed: tabs().filter((tab) => tab.getAttribute("aria-pressed") === "true").map((tab) => tab.dataset.statusTab),
    Shown: shownColumns(),
    TabCounts: Object.fromEntries(tabs().map((tab) => [tab.dataset.statusTab, tab.querySelector("[data-status-tab-count]").textContent])),
    ColumnCounts: Object.fromEntries([...document.querySelectorAll("[data-count]")].map((count) => [count.dataset.count, count.textContent])),
    Sideways: sideways(),
    Small: smallTargets(document),
    Smallest: smallest(document),
    Draggable: [...document.querySelectorAll(".task-card")].filter((card) => card.draggable).length,
    RowPosition: getComputedStyle(row).position,
  };
  const shown = document.querySelector(".column[data-phone-current]");
  reading.ShownLabel = shown ? shown.querySelector("[data-status]").dataset.statusLabel : "";

  // A finger drawn up a card, starting on its title, scrolls the page and
  // does not drag the card; the row of columns stays where it is.
  let drags = 0;
  document.addEventListener("dragstart", () => { drags++; }, true);
  const card = shown.querySelectorAll(".task-card")[3];
  card.scrollIntoView({ block: "start" });
  window.scrollTo(0, 0);
  await settle();
  const cardTop = card.getBoundingClientRect().top;
  reading.RowTopBefore = row.getBoundingClientRect().top;
  const start = card.querySelector("h3 a").getBoundingClientRect();
  const before = window.scrollY;
  await touchInput({ x: start.left + 10, y: Math.min(start.top + 10, innerHeight - 20), dy: -Math.min(300, innerHeight / 2), steps: 12, press: 600 });
  await pause(400);
  reading.ScrolledBy = window.scrollY - before;
  reading.CardMovedBy = cardTop - card.getBoundingClientRect().top;
  reading.RowTopAfter = row.getBoundingClientRect().top;
  reading.Drags = drags;

  // The first column whose name runs off the right of the row; tapping the
  // part of it that shows shows the column, slides the row so its whole name
  // is in view, and remembers it.
  const rowEdge = row.getBoundingClientRect().right;
  const cutOff = () => tabs().find((tab) => tab.getBoundingClientRect().left < rowEdge - 20 && tab.getBoundingClientRect().right > rowEdge + 0.5);
  // A row whose names all fit or end exactly at its edge is drawn a little
  // way along with a finger first, which the row takes and the page does not.
  for (let stroke = 0; stroke < 4 && !cutOff(); stroke++) {
    const rect = row.getBoundingClientRect();
    await touchInput({ x: rect.right - 30, y: rect.top + rect.height / 2, dx: -50, steps: 6 });
    await pause(300);
  }
  reading.RowStrokeSideways = sideways();
  const last = cutOff();
  reading.Partial = last ? last.dataset.statusTab : "";
  const lastRect = last.getBoundingClientRect();
  await touchInput({ x: (lastRect.left + Math.min(lastRect.right, rowEdge)) / 2, y: lastRect.top + lastRect.height / 2 });
  await pause(800);
  reading.AfterTap = tabs().filter((tab) => tab.getAttribute("aria-pressed") === "true").map((tab) => tab.dataset.statusTab);
  reading.AfterTapShown = shownColumns();
  const tabRect = last.getBoundingClientRect();
  const rowRect = row.getBoundingClientRect();
  reading.TabLeft = tabRect.left; reading.TabRight = tabRect.right; reading.RowLeft = rowRect.left; reading.RowRight = rowRect.right;
  reading.TabInRow = tabRect.left >= rowRect.left - 0.5 && tabRect.right <= rowRect.right + 0.5;
  reading.RowScrolled = row.scrollLeft;
  reading.ColumnUnderRow = document.querySelector(".column[data-phone-current]").getBoundingClientRect().top - rowRect.bottom;
  try { reading.Remembered = localStorage.getItem(document.querySelector("[data-board-view]").dataset.phoneColumnKey) || ""; } catch (_) { reading.Remembered = "unreadable"; }

  // A chooser's menu opened over the row stays inside the screen.
  window.scrollTo(0, 0);
  await settle();
  await tap(document.querySelector('[data-filter-chooser="label"] [data-filter-chooser-button]'));
  reading.MenuSideways = sideways();
  reading.Refused = window.__refused;
  reading.Errors = window.__errors;
  return reading;
})()
`

// On a phone the board is one column at a time, under a row of every
// column's name and count that stays put while the cards scroll. It opens on
// the column `workbook next` draws from — here a status called queued and
// labeled "Up next", because nothing is called ready — and a tap on another
// name shows that column, slides the row to it and remembers it. A finger
// drawn along a card scrolls the page and never drags, no page is wider than
// the screen, and every control is at least 44 pixels each way.
func TestBrowserPhoneBoardShowsOneColumnAtATime(t *testing.T) {
	t.Parallel()
	for _, size := range phoneSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			var reading phoneBoardReading
			openInBrowser(t, newPhoneBoard().handler(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/",
				BeforeLoad: phoneWatch, Script: phoneBoardScript, Screenshot: phoneShot("board", size.width, size.height),
			}, &reading)
			if !slices.Equal(reading.Pressed, []string{"queued"}) || !slices.Equal(reading.Shown, []string{"queued"}) {
				t.Errorf("a first visit opens with %v pressed and %v shown, want queued alone: the column workbook next draws from", reading.Pressed, reading.Shown)
			}
			if reading.ShownLabel != "Up next" {
				t.Errorf("the column shown is labeled %q, want %q", reading.ShownLabel, "Up next")
			}
			if len(reading.TabCounts) != len(phoneStatuses) {
				t.Errorf("the row offers %d columns, want %d: %v", len(reading.TabCounts), len(phoneStatuses), reading.TabCounts)
			}
			for status, count := range reading.ColumnCounts {
				if reading.TabCounts[status] != count {
					t.Errorf("the row counts %q for %s, and its column counts %q", reading.TabCounts[status], status, count)
				}
			}
			if reading.TabCounts["queued"] != "14" {
				t.Errorf("the row counts %q in queued, want 14", reading.TabCounts["queued"])
			}
			if reading.Sideways > 0 || reading.MenuSideways > 0 || reading.RowStrokeSideways > 0 {
				t.Errorf("the board scrolls sideways by %.1fpx, and by %.1fpx with the Labels menu open", reading.Sideways, reading.MenuSideways)
			}
			if len(reading.Small) > 0 {
				t.Errorf("controls a thumb cannot hit on the board:\n%s", strings.Join(reading.Small, "\n"))
			}
			if reading.Draggable != 0 {
				t.Errorf("%d cards are draggable on a phone", reading.Draggable)
			}
			if reading.Drags != 0 || reading.ScrolledBy < 100 || reading.CardMovedBy < 100 {
				t.Errorf("a finger drawn up a card started %d drags and scrolled the page %.1fpx (the card moved %.1fpx), want no drag and a scroll",
					reading.Drags, reading.ScrolledBy, reading.CardMovedBy)
			}
			if reading.RowPosition != "sticky" || reading.RowTopAfter < -0.5 || reading.RowTopAfter > 0.5 {
				t.Errorf("after scrolling, the row of columns is %s at %.1fpx from the top (was %.1fpx), want held at 0", reading.RowPosition, reading.RowTopAfter, reading.RowTopBefore)
			}
			if reading.Partial == "" || !slices.Equal(reading.AfterTap, []string{reading.Partial}) || !slices.Equal(reading.AfterTapShown, []string{reading.Partial}) {
				t.Errorf("tapping %q, the first name the row cuts off, left %v pressed and %v shown", reading.Partial, reading.AfterTap, reading.AfterTapShown)
			}
			if !reading.TabInRow || reading.RowScrolled <= 0 {
				t.Errorf("the tapped button spans %.1f to %.1f in a row from %.1f to %.1f scrolled by %.1f: the row did not slide it into view",
					reading.TabLeft, reading.TabRight, reading.RowLeft, reading.RowRight, reading.RowScrolled)
			}
			if reading.ColumnUnderRow < -0.5 {
				t.Errorf("the column shown starts %.1fpx under the row's bottom edge, hidden behind it", -reading.ColumnUnderRow)
			}
			if reading.Remembered != "status:"+reading.Partial {
				t.Errorf("the phone remembered %q, want status:%s", reading.Remembered, reading.Partial)
			}
			if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
				t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
			}
			t.Logf("smallest control on the board at %dx%d: %.1fpx", size.width, size.height, reading.Smallest)
		})
	}
}

type phoneReturnReading struct {
	Pressed []string
	Shown   []string
	Errors  []string
	Small   []string
	Tapped  []string
}

const phoneReturnScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card .task-id-copy"), "the board to load");
  await settle();
  const tabs = [...document.querySelectorAll("[data-status-tabs] .status-tab")];
  const reading = {
    Pressed: tabs.filter((tab) => tab.getAttribute("aria-pressed") === "true").map((tab) => tab.dataset.statusTab),
    Shown: [...document.querySelectorAll(".column")].filter((column) => getComputedStyle(column).display !== "none").map((column) => column.querySelector("[data-status]").dataset.status),
  };
  await tap(document.querySelector('[data-status-tab="review"]'));
  reading.Tapped = [...document.querySelectorAll(".column")].filter((column) => getComputedStyle(column).display !== "none").map((column) => column.querySelector("[data-status]").dataset.status);
  reading.Errors = window.__errors;
  return reading;
})()
`

// The board opens where the phone was last looking: a phone that tapped
// Doing and comes back finds Doing. A remembered column the project no longer
// has falls back to the column `workbook next` draws from, and so does a
// phone whose browser will not keep anything at all — which still switches
// columns, it only forgets.
func TestBrowserPhoneBoardReturnsToTheLastColumnViewed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		beforeLoad string
		setup      string
		want       string
	}{
		{name: "remembered", setup: phoneHelpers + `(async () => {
  await waitFor(() => document.querySelector('[data-status-tab="doing"]'), "the row");
  await tap(document.querySelector('[data-status-tab="doing"]'));
})()`, want: "doing"},
		{name: "gone", beforeLoad: rememberPhoneColumn(phoneProjectName, "status:ready"), want: "queued"},
		{name: "deleted column not shown", beforeLoad: rememberPhoneColumn(phoneProjectName, "deleted"), want: "queued"},
		{name: "no storage", beforeLoad: `Object.defineProperty(window, "localStorage", { configurable: true, get() { throw new DOMException("The operation is insecure.", "SecurityError"); } });`, setup: phoneHelpers + `(async () => {
  await waitFor(() => document.querySelector('[data-status-tab="doing"]'), "the row");
  await tap(document.querySelector('[data-status-tab="doing"]'));
})()`, want: "queued"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var reading phoneReturnReading
			openInBrowser(t, newPhoneBoard().handler(t), browserPage{
				Width: 390, Height: 844, Phone: true, Path: "/",
				BeforeLoad: phoneWatch + test.beforeLoad, Setup: test.setup, Reload: test.setup != "",
				Script: phoneReturnScript,
			}, &reading)
			if !slices.Equal(reading.Pressed, []string{test.want}) || !slices.Equal(reading.Shown, []string{test.want}) {
				t.Errorf("the board opened with %v pressed and %v shown, want %s", reading.Pressed, reading.Shown, test.want)
			}
			if !slices.Equal(reading.Tapped, []string{"review"}) {
				t.Errorf("tapping In review then shows %v", reading.Tapped)
			}
			if len(reading.Errors) > 0 {
				t.Errorf("the page threw %v", reading.Errors)
			}
		})
	}
}

type phoneTaskReading struct {
	Unfinished    string
	Sideways      []float64
	Small         []string
	Smallest      float64
	HistoryRows   int
	HistoryInside bool
	Saved         string
	Refused       []string
	Errors        []string
}

// phoneTaskScript works a task's page with a finger: it changes the status,
// the priority and the labels and saves, assigns the task to the reader,
// comments, and reads the history, measuring the page at each step.
const phoneTaskScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-sidebar") && document.querySelector("[data-change-list]") && document.querySelector(".comment") && document.querySelector("#assignment-to"), "the task page");
  await settle();
  const reading = { Sideways: [sideways()], Small: smallTargets(document), Smallest: smallest(document) };
  // A step that cannot be finished — a control off the edge of a page wider
  // than the screen is one a tap misses — ends the run with what was
  // measured so far, so the test reports the width as well as the step.
  try {
  const choose = async (select, value) => {
    await tap(select);
    select.value = value;
    select.dispatchEvent(new Event("input", { bubbles: true }));
    select.dispatchEvent(new Event("change", { bubbles: true }));
    select.blur();
  };
  await choose(document.querySelector("#task-status"), "doing");
  await choose(document.querySelector("#task-priority"), "low");
  await tap(document.querySelector("#task-labels"));
  await type("on-the-go");
  await enter();
  reading.Sideways.push(sideways());
  await tap(document.querySelector(".task-actions .save-button"));
  const read = async () => (await (await fetch("/api/tasks/` + phoneTaskID + `")).json()).task;
  await waitFor(() => { read().then((task) => { window.__task = task; }); return window.__task && window.__task.status === "doing"; }, "the save to land");
  reading.Saved = window.__task.status + " " + window.__task.priority + " " + window.__task.labels.join(",");
  // Saving returns to the board, where the task is now in Doing: the reader
  // taps Doing in the row and opens the task again from its card.
  await waitFor(() => location.pathname === "/", "the board after saving");
  await tap(document.querySelector('[data-status-tab="doing"]'));
  await waitFor(() => document.querySelector('.column[data-phone-current] .task-card[data-task-id="` + phoneTaskID + `"] h3 a'), "the card in Doing");
  await tap(document.querySelector('.column[data-phone-current] .task-card[data-task-id="` + phoneTaskID + `"] h3 a'));
  await waitFor(() => document.querySelector(".assignment-add button[type=submit]") && document.querySelector("[data-change-list]"), "the task page again");
  reading.Sideways.push(sideways());

  // Assigned to the reader: the field left empty is this board's identity.
  await tap(document.querySelector(".assignment-add button[type=submit]"));
  await waitFor(() => [...document.querySelectorAll(".assignment__principal")].some((who) => who.textContent === "` + phoneIdentity + `"), "the assignment to be drawn");
  reading.Sideways.push(sideways());

  await tap(document.querySelector("#comment-body"));
  await type("Read and moved along from a phone.");
  await tap(document.querySelector(".comment-add button[type=submit]"));
  await waitFor(() => [...document.querySelectorAll(".comment__body")].some((body) => body.textContent.includes("from a phone")), "the comment to be drawn");
  reading.Sideways.push(sideways());

  const history = document.querySelector(".task-history");
  history.scrollIntoView({ block: "start" });
  await settle();
  reading.HistoryRows = document.querySelectorAll(".change-row").length;
  reading.HistoryInside = [...history.querySelectorAll("*")].every((element) => element.closest("pre") || element.getBoundingClientRect().right <= document.documentElement.clientWidth + 0.5);
  const toggle = history.querySelector(".change-row__summary");
  if (toggle) await tap(toggle);
  reading.Sideways.push(sideways());
  reading.Small = reading.Small.concat(smallTargets(document).filter((problem) => !reading.Small.includes(problem)));
  reading.Smallest = Math.min(reading.Smallest, smallest(document));
  } catch (error) {
    reading.Unfinished = String(error && error.message || error);
  }
  reading.Refused = window.__refused;
  reading.Errors = window.__errors;
  return reading;
})()
`

// A task's page on a phone: a reader changes the status, the priority and
// the labels with a finger and saves, assigns the task to themselves,
// comments, and reads the history. Each change reaches the board, no step
// leaves the page wider than the screen — not the title with no break in it,
// the long label, the long name, the wide code blocks or the long attachment
// name — and every control is at least 44 pixels each way.
func TestBrowserPhoneTaskPageTakesEveryChangeByTouch(t *testing.T) {
	t.Parallel()
	for _, size := range phoneSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			board := newPhoneBoard()
			var reading phoneTaskReading
			openInBrowser(t, board.handler(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/tasks/" + phoneTaskID,
				BeforeLoad: phoneWatch, Script: phoneTaskScript, Screenshot: phoneShot("task", size.width, size.height),
			}, &reading)
			for step, over := range reading.Sideways {
				if over > 0 {
					t.Errorf("step %d of the task page scrolls sideways by %.1fpx", step, over)
				}
			}
			if reading.Unfinished != "" {
				t.Errorf("the reader could not finish on the task page: %s", reading.Unfinished)
			}
			if len(reading.Small) > 0 {
				t.Errorf("controls a thumb cannot hit on the task page:\n%s", strings.Join(reading.Small, "\n"))
			}
			task := board.task(phoneTaskID)
			if task.Status != "doing" || task.Priority != core.PriorityLow || !slices.Contains(task.Labels, "on-the-go") {
				t.Errorf("after saving, the task is %s at %s with labels %v (the page read %q)", task.Status, task.Priority, task.Labels, reading.Saved)
			}
			if !slices.ContainsFunc(task.Assignments, func(assignment core.Assignment) bool { return assignment.Principal == phoneIdentity }) {
				t.Errorf("the task was not assigned to the reader: %v", task.Assignments)
			}
			if !slices.ContainsFunc(task.Comments, func(comment core.Comment) bool { return comment.Body == "Read and moved along from a phone." }) {
				t.Errorf("the comment did not land: %v", task.Comments)
			}
			if reading.HistoryRows != 2 || !reading.HistoryInside {
				t.Errorf("the history shows %d rows, inside the screen: %v", reading.HistoryRows, reading.HistoryInside)
			}
			if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
				t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
			}
			t.Logf("smallest control on the task page at %dx%d: %.1fpx", size.width, size.height, reading.Smallest)
		})
	}
}

type phoneCreateReading struct {
	Unfinished string
	Path       string
	Sideways   []float64
	Small      []string
	Smallest   float64
	Errors     []string
}

const phoneCreateScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".column[data-phone-current] .new-task-link"), "the board");
  await tap(document.querySelector('[data-status-tab="review"]'));
  await tap(document.querySelector(".column[data-phone-current] .new-task-link"));
  await waitFor(() => document.querySelector("#task-title"), "the new task form");
  await settle();
  const reading = { Path: location.pathname + location.search, Sideways: [sideways()], Small: smallTargets(document), Smallest: smallest(document) };
  try {
  await tap(document.querySelector("#task-title"));
  await type("Captured on a phone");
  await tap(document.querySelector(".task-actions .save-button"));
  await waitFor(() => location.pathname === "/" || /\/tasks\/WB-/.test(location.pathname) || document.querySelector("[data-open-created-task]"), "the task to be created");
  await pause(300);
  reading.Sideways.push(sideways());
  } catch (error) {
    reading.Unfinished = String(error && error.message || error);
  }
  reading.Errors = window.__errors;
  return reading;
})()
`

// A phone creates a task from the column it is showing: the column's New
// Task link opens the form for that status, the form fits the screen and
// every control on it is a thumb's size, and the task lands in that status.
func TestBrowserPhoneCreatesATask(t *testing.T) {
	t.Parallel()
	for _, size := range phoneSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			board := newPhoneBoard()
			var reading phoneCreateReading
			openInBrowser(t, board.handler(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/",
				BeforeLoad: phoneWatch, Script: phoneCreateScript, Screenshot: phoneShot("created", size.width, size.height),
			}, &reading)
			if reading.Path != "/tasks/new?status=review" {
				t.Errorf("the column's New Task link opened %q", reading.Path)
			}
			for step, over := range reading.Sideways {
				if over > 0 {
					t.Errorf("step %d of creating a task scrolls sideways by %.1fpx", step, over)
				}
			}
			if reading.Unfinished != "" {
				t.Errorf("the reader could not finish creating a task: %s", reading.Unfinished)
			}
			if len(reading.Small) > 0 {
				t.Errorf("controls a thumb cannot hit on the new task form:\n%s", strings.Join(reading.Small, "\n"))
			}
			created := board.task("WB-01J0000000000000000000PN01")
			if created.Title != "Captured on a phone" || created.Status != "review" {
				t.Errorf("the task created is %q in %q, want %q in review", created.Title, created.Status, "Captured on a phone")
			}
			if len(reading.Errors) > 0 {
				t.Errorf("the page threw %v", reading.Errors)
			}
			t.Logf("smallest control on the new task form at %dx%d: %.1fpx", size.width, size.height, reading.Smallest)
		})
	}
}
