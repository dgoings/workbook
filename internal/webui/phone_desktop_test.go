package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The phone layout is a set of rules under one width query and a few lines
// of script, and above that width the board must be the board it was. These
// tests hold that line from both sides: at every width above the breakpoint,
// and at one pixel past it, the page is measured, the phone rules are taken
// out of the stylesheet, and the page is measured again, and the two must be
// the same to the pixel. A rule written outside the query, or a query that
// reaches too far, changes the first measurement and not the second.

// phoneBreakpoint is the widest viewport, in CSS pixels, that gets the phone
// layout. It is the number in the stylesheet's query, and the tests check
// both sides of it.
const phoneBreakpoint = 560

// layoutCaptureScript measures where the page's main regions are on the
// route it was opened at, once the client has drawn it: the header, the
// filter row, main, the board, every column and its header and first card,
// and on a task's page the route's header, editor, sidebar, actions and
// panels. Each is its rectangle and its display, keyed by a name that says
// which element it is.
//
// When strip is true it measures twice, the second time with every rule
// under the phone width query removed from the page's stylesheets, and
// answers both.
func layoutCaptureScript(strip bool) string {
	return pageGeometryHelpers + `
(async () => {
  const creating = location.pathname === "/tasks/new";
  const onTask = !creating && location.pathname.startsWith("/tasks/");
  await waitFor(() => creating ? document.querySelector("#task-title")
    : onTask ? document.querySelector(".task-sidebar") && document.querySelector("[data-change-list]") && document.querySelector(".comment")
    : document.querySelector(".task-card .task-id-copy") && document.querySelector("[data-updated-text]").textContent !== "Loaded from this checkout", "the route to be drawn");
  await new Promise((resolve) => setTimeout(resolve, 300));
  const measure = () => {
    const regions = {};
    const note = (name, element) => {
      if (!element) { regions[name] = null; return; }
      const rect = element.getBoundingClientRect();
      const style = getComputedStyle(element);
      regions[name] = [Math.round(rect.left * 100) / 100, Math.round(rect.top * 100) / 100, Math.round(rect.width * 100) / 100, Math.round(rect.height * 100) / 100, style.display, style.position, style.overflowX + "/" + style.overflowY].join(" ");
    };
    note("html", document.documentElement);
    note("body", document.body);
    note("header", document.querySelector(".app-header"));
    note("header-lead", document.querySelector(".app-header__lead"));
    note("header-settings", document.querySelector(".header-settings"));
    document.querySelectorAll(".header-link, .nav-switch").forEach((element, index) => note("header-control-" + index, element));
    note("filter-row", document.querySelector("[data-filter-row]"));
    document.querySelectorAll(".filter-row > *, .filter-chooser__button").forEach((element, index) => note("filter-" + index, element));
    note("main", document.querySelector("main"));
    note("board-view", document.querySelector("[data-board-view]"));
    note("status-tabs", document.querySelector("[data-status-tabs]"));
    note("board", document.querySelector("[data-board]"));
    document.querySelectorAll(".column").forEach((column, index) => {
      note("column-" + index, column);
      note("column-" + index + "-header", column.querySelector(".column__header"));
      note("column-" + index + "-new", column.querySelector(".new-task-link"));
      note("column-" + index + "-list", column.querySelector(".task-list"));
      const card = column.querySelector(".task-card");
      note("column-" + index + "-card", card);
      if (card) {
        note("column-" + index + "-card-title", card.querySelector("h3 a"));
        note("column-" + index + "-card-copy", card.querySelector(".task-id-copy"));
      }
    });
    note("unknown", document.querySelector("[data-unknown-section]"));
    [".task-route", ".task-route__header", ".task-route__close", ".task-layout", ".task-editor", ".task-sidebar", ".task-actions",
      ".save-button", "#task-title", "#task-status", "#task-priority", "#task-labels", ".description-read", ".task-panel--comments",
      ".comment", ".comment-add textarea", ".task-panel--assignments", ".assignment-add", ".task-panel--attachments", ".task-history",
      ".change-row", ".task-relationships"].forEach((selector) => note(selector, document.querySelector(selector)));
    regions.cardsDraggable = [...document.querySelectorAll(".column .task-card")].every((card) => card.draggable);
    regions.scroll = [document.documentElement.scrollWidth, document.documentElement.clientWidth, document.documentElement.scrollHeight].join(" ");
    return regions;
  };
  const shown = measure();
  if (!` + fmt.Sprint(strip) + `) return { shown };
  let removed = 0;
  for (const sheet of document.styleSheets) {
    for (let index = sheet.cssRules.length - 1; index >= 0; index--) {
      const rule = sheet.cssRules[index];
      if (rule instanceof CSSMediaRule && rule.conditionText.includes("max-width: ` + fmt.Sprint(phoneBreakpoint) + `px")) {
        sheet.deleteRule(index);
        removed++;
      }
    }
  }
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  return { shown, stripped: measure(), removed };
})()
`
}

// The routes a desktop reader meets that the phone layout also styles.
var phoneLayoutRoutes = []struct{ name, path string }{
	{"board", "/"},
	{"task", "/tasks/" + phoneTaskID},
	{"new", "/tasks/new?status=doing"},
}

type layoutCapture struct {
	Shown    map[string]any
	Stripped map[string]any
	Removed  int
}

// Above the breakpoint, every route the phone layout styles is laid out
// exactly as it is with the phone rules taken away, and the cards still drag.
// The widths are a portrait tablet, the two desktop windows the board is
// measured at elsewhere, and the first width past the breakpoint.
func TestBrowserPhoneRulesStayBelowTheBreakpoint(t *testing.T) {
	t.Parallel()
	dump := os.Getenv("WORKBOOK_TEST_LAYOUT_DUMP")
	for _, size := range []struct{ width, height int }{{phoneBreakpoint + 1, 900}, {768, 1024}, {1280, 800}, {1440, 900}} {
		for _, route := range phoneLayoutRoutes {
			t.Run(fmt.Sprintf("%s-%dx%d", route.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				var capture layoutCapture
				openInBrowser(t, newPhoneBoard().handler(t), browserPage{
					Width: size.width, Height: size.height, Path: route.path, Script: layoutCaptureScript(true),
				}, &capture)
				if dump != "" {
					encoded, err := json.MarshalIndent(capture.Shown, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					name := filepath.Join(dump, fmt.Sprintf("%s-%dx%d.json", route.name, size.width, size.height))
					if err := os.WriteFile(name, encoded, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if capture.Removed == 0 {
					t.Fatalf("the page has no rule under max-width: %dpx to take away, so this checked nothing", phoneBreakpoint)
				}
				if !reflect.DeepEqual(capture.Shown, capture.Stripped) {
					var differences []string
					for name, shown := range capture.Shown {
						if stripped := capture.Stripped[name]; !reflect.DeepEqual(shown, stripped) {
							differences = append(differences, fmt.Sprintf("%s: %v with the phone rules, %v without", name, shown, stripped))
						}
					}
					t.Errorf("the phone rules change the page at %dx%d:\n%s", size.width, size.height, strings.Join(differences, "\n"))
				}
				if capture.Shown["cardsDraggable"] != true {
					t.Errorf("a card on the board does not drag at %dx%d", size.width, size.height)
				}
				if tabs, _ := capture.Shown["status-tabs"].(string); tabs != "" && !strings.Contains(tabs, " none ") {
					t.Errorf("the phone's status row is drawn at %dx%d: %s", size.width, size.height, tabs)
				}
			})
		}
	}
}
