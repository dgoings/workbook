package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The board at two desktop sizes, held to a recording of how it is laid out.
// TestBrowserPhoneRulesStayBelowTheBreakpoint compares a page with the phone
// rules and the same page with them taken out, which cannot see a rule meant
// for a phone that was written outside the phone query: it is in both pages.
// This compares the page with what it was when the recording was made, so any
// change to the desktop layout, from anywhere, fails it until the recording
// is made again on purpose.
//
// What is recorded is the layout's specification rather than its geometry:
// for every element the page draws, in document order, its tag and classes,
// whether it can be dragged, and the computed values of the properties that
// lay it out — display, position and its offsets, the flex and grid
// properties, overflow, width, height and their limits, padding, margin and
// border widths (the one value read as drawn, which is 0 for a side with no
// border style), box sizing, and, where they differ from the parent's, font
// size, weight, line height and white space. Computed values, read through the
// Typed OM (computedStyleMap), are what the stylesheet says once lengths are
// in pixels: `auto` stays `auto` and a percentage stays a percentage, so none
// of it depends on the fonts a machine has, which differ between the Ubuntu,
// macOS and Windows machines this runs on. Rectangles, which do depend on
// them, are not recorded.
//
// So this catches a changed display, position, flex or grid setup, overflow,
// size or limit, padding, margin or border, a font size, an element drawn or
// no longer drawn, and a card that stops dragging. It does not catch a
// change of color or decoration, a change that only moves text (letter
// spacing, alignment, a different font), or a change that only some text
// content would show.
//
// To record it again after changing the desktop layout on purpose:
//
//	WORKBOOK_TEST_RECORD_DESKTOP_LAYOUT=1 go test ./internal/webui -run TestBrowserDesktopLayoutMatchesTheRecording
//
// and read the diff of testdata/desktop-layout before committing it. The
// recording in this commit was made from main before the phone layout
// existed, and this branch's page matches it line for line.

// desktopLayoutScript lists every element the page draws, one line each.
const desktopLayoutScript = pageGeometryHelpers + `
(async () => {
  const creating = location.pathname === "/tasks/new";
  const onTask = !creating && location.pathname.startsWith("/tasks/");
  await waitFor(() => creating ? document.querySelector("#task-title")
    : onTask ? document.querySelector(".task-sidebar") && document.querySelector("[data-change-list]") && document.querySelector(".comment")
    : document.querySelector(".task-card .task-id-copy") && document.querySelector("[data-updated-text]").textContent !== "Loaded from this checkout", "the route to be drawn");
  await new Promise((resolve) => setTimeout(resolve, 300));
  // Each property, with the value it is left out at because nearly every
  // element has it.
  const laid = {
    "position": "static", "top": "auto", "right": "auto", "bottom": "auto", "left": "auto", "float": "none",
    "flex-direction": "row", "flex-wrap": "nowrap", "flex-grow": "0", "flex-shrink": "1", "flex-basis": "auto", "order": "0",
    "align-items": "normal", "align-self": "auto", "justify-content": "normal", "justify-self": "auto",
    "grid-template-columns": "none", "grid-template-rows": "none", "grid-auto-flow": "row", "grid-auto-columns": "auto", "grid-auto-rows": "auto",
    "grid-column-start": "auto", "grid-column-end": "auto", "grid-row-start": "auto", "grid-row-end": "auto",
    "row-gap": "normal", "column-gap": "normal", "overflow-x": "visible", "overflow-y": "visible",
    "width": "auto", "height": "auto", "min-width": "auto", "min-height": "auto", "max-width": "none", "max-height": "none",
    "padding-top": "0px", "padding-right": "0px", "padding-bottom": "0px", "padding-left": "0px",
    "margin-top": "0px", "margin-right": "0px", "margin-bottom": "0px", "margin-left": "0px",
    "box-sizing": "content-box", "z-index": "auto",
  };
  // Inherited, so written only where an element changes them.
  const inherited = ["font-size", "font-weight", "line-height", "white-space", "visibility"];
  const value = (map, property) => { const found = map.get(property); return found ? found.toString() : ""; };
  const lines = [];
  const walk = (element, depth, parentMap) => {
    const map = element.computedStyleMap();
    const drawn = element.getClientRects().length > 0;
    if (drawn) {
      const name = element.tagName.toLowerCase() + (typeof element.className === "string" && element.className.trim() ? "." + element.className.trim().split(/\s+/).join(".") : "");
      const facts = ["display:" + value(map, "display")];
      for (const [property, usual] of Object.entries(laid)) {
        const shown = value(map, property);
        if (shown !== usual) facts.push(property + ":" + shown);
      }
      // A border's width as drawn, which is 0 where the side has no border
      // style: the Typed OM reports the width the side would have.
      const style = getComputedStyle(element);
      for (const side of ["top", "right", "bottom", "left"]) {
        const width = style.getPropertyValue("border-" + side + "-width");
        if (width !== "0px") facts.push("border-" + side + "-width:" + width);
      }
      for (const property of inherited) {
        const shown = value(map, property);
        if (!parentMap || shown !== value(parentMap, property)) facts.push(property + ":" + shown);
      }
      if (element.draggable) facts.push("draggable");
      lines.push(" ".repeat(depth) + name + " " + facts.join(" "));
    }
    for (const child of element.children) walk(child, drawn ? depth + 1 : depth, drawn ? map : parentMap);
  };
  walk(document.body, 0, null);
  return lines.join("\n") + "\n";
})()
`

// desktopLayoutSizes are the desktop windows the board is recorded at.
var desktopLayoutSizes = []struct{ width, height int }{{1280, 800}, {1440, 900}}

func desktopLayoutRecording(route string, width, height int) string {
	return filepath.Join("testdata", "desktop-layout", fmt.Sprintf("%s-%dx%d.txt", route, width, height))
}

func TestBrowserDesktopLayoutMatchesTheRecording(t *testing.T) {
	t.Parallel()
	record := os.Getenv("WORKBOOK_TEST_RECORD_DESKTOP_LAYOUT") != ""
	for _, size := range desktopLayoutSizes {
		for _, route := range phoneLayoutRoutes {
			t.Run(fmt.Sprintf("%s-%dx%d", route.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				var layout string
				openInBrowser(t, newPhoneBoard().handler(t), browserPage{
					Width: size.width, Height: size.height, Path: route.path, Script: desktopLayoutScript,
				}, &layout)
				name := desktopLayoutRecording(route.name, size.width, size.height)
				if record {
					if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(layout), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				recorded, err := os.ReadFile(name)
				if err != nil {
					t.Fatalf("read the recorded layout (record it with WORKBOOK_TEST_RECORD_DESKTOP_LAYOUT=1): %v", err)
				}
				if want := strings.ReplaceAll(string(recorded), "\r\n", "\n"); layout != want {
					t.Errorf("%s at %dx%d is not laid out as recorded in %s; first difference:\n%s\nIf the desktop layout changed on purpose, record it again with WORKBOOK_TEST_RECORD_DESKTOP_LAYOUT=1 and read the diff.",
						route.path, size.width, size.height, name, firstLineDifference(want, layout))
				}
			})
		}
	}
}

// firstLineDifference names the first line at which got departs from want,
// with the lines around it.
func firstLineDifference(want, got string) string {
	wanted, gotten := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := 0; index < len(wanted) || index < len(gotten); index++ {
		var w, g string
		if index < len(wanted) {
			w = wanted[index]
		}
		if index < len(gotten) {
			g = gotten[index]
		}
		if w != g {
			return fmt.Sprintf("line %d\n  recorded: %s\n  now:      %s", index+1, w, g)
		}
	}
	return "none"
}
