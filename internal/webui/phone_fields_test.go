package webui

import (
	"fmt"
	"strings"
	"testing"
)

// A phone's browser zooms the page in when a field whose text is smaller than
// 16 CSS pixels takes focus — iPhone Safari does, and leaves the page zoomed,
// and so scrollable sideways, after the field lets go. So at the phone width
// every field a reader can type into or choose from is set in at least 16
// pixels: every input, select, textarea and editable region in the document,
// whether it is on screen yet or not, with the board's filter menus open. The
// rule that does it names the elements rather than any class, so a field
// added later is covered without anybody remembering to.

type phoneFieldReading struct {
	Fields   int
	Small    []string
	Sideways float64
	Errors   []string
}

// phoneFieldScript opens every filter menu the page has, then measures every
// field in the document.
const phoneFieldScript = phoneHelpers + `
(async () => {
  const creating = location.pathname === "/tasks/new";
  const onTask = !creating && location.pathname.startsWith("/tasks/");
  await waitFor(() => creating ? document.querySelector("#task-title")
    : onTask ? document.querySelector(".task-sidebar") && document.querySelector("[data-change-list]") && document.querySelector(".comment")
    : document.querySelector(".task-card .task-id-copy"), "the page to be drawn");
  await settle();
  for (const button of document.querySelectorAll('[aria-expanded="false"][data-filter-chooser-button]')) {
    if (button.getBoundingClientRect().height === 0) continue;
    button.click();
    await settle();
  }
  const fields = [...document.querySelectorAll('input, select, textarea, [contenteditable]:not([contenteditable="false"])')]
    .filter((field) => !field.matches('input[type="hidden"]'));
  const name = (field) => field.tagName.toLowerCase() + (field.id ? "#" + field.id : "") +
    (field.className && typeof field.className === "string" && field.className.trim() ? "." + field.className.trim().split(/\s+/).join(".") : "") +
    (field.getAttribute("aria-label") ? " " + JSON.stringify(field.getAttribute("aria-label")) : "") +
    (field.type ? " [" + field.type + "]" : "");
  return {
    Fields: fields.length,
    Small: fields.filter((field) => parseFloat(getComputedStyle(field).fontSize) < 15.99)
      .map((field) => name(field) + " is " + getComputedStyle(field).fontSize),
    Sideways: sideways(),
    Errors: window.__errors,
  };
})()
`

func TestBrowserPhoneFieldsAreLargeEnoughNotToZoom(t *testing.T) {
	t.Parallel()
	for _, size := range phoneSizes {
		for _, route := range phoneLayoutRoutes {
			t.Run(fmt.Sprintf("%s-%dx%d", route.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				var reading phoneFieldReading
				openInBrowser(t, newPhoneBoard().handler(t), browserPage{
					Width: size.width, Height: size.height, Phone: true, Path: route.path,
					BeforeLoad: phoneWatch, Script: phoneFieldScript,
				}, &reading)
				if reading.Fields < 3 {
					t.Fatalf("found %d fields on %s, so this checked nothing", reading.Fields, route.path)
				}
				if len(reading.Small) > 0 {
					t.Errorf("fields on %s set smaller than 16px, which a phone zooms in on when they take focus:\n%s", route.path, strings.Join(reading.Small, "\n"))
				}
				if reading.Sideways > 0 {
					t.Errorf("%s scrolls sideways by %.1fpx with its menus open", route.path, reading.Sideways)
				}
				if len(reading.Errors) > 0 {
					t.Errorf("the page threw %v", reading.Errors)
				}
				t.Logf("%d fields on %s at %dx%d", reading.Fields, route.path, size.width, size.height)
			})
		}
	}
}

type phoneWidePageReading struct {
	Title    string
	Sideways float64
	Errors   []string
}

// The task whose title has no break in it, opened on its own page: the title
// is the page's heading there, at its largest, and it wraps where it must
// rather than making the page wider than the screen.
func TestBrowserPhoneLongTitleTaskPageFitsTheScreen(t *testing.T) {
	t.Parallel()
	for _, size := range phoneSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			var reading phoneWidePageReading
			openInBrowser(t, newPhoneBoard().handler(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/tasks/" + phoneWideID,
				BeforeLoad: phoneWatch, Screenshot: phoneShot("wide-title", size.width, size.height),
				Script: phoneHelpers + `(async () => {
  await waitFor(() => document.querySelector(".task-route__header h2") && document.querySelector(".task-sidebar") && document.querySelector("[data-change-list]"), "the task page");
  await settle();
  return { Title: document.querySelector(".task-route__header h2").textContent.trim(), Sideways: sideways(), Errors: window.__errors };
})()`,
			}, &reading)
			if !strings.Contains(reading.Title, "Averyverylongtitle") {
				t.Fatalf("the page's heading is %q, not the long title", reading.Title)
			}
			if reading.Sideways > 0 {
				t.Errorf("the long-titled task's page scrolls sideways by %.1fpx", reading.Sideways)
			}
			if len(reading.Errors) > 0 {
				t.Errorf("the page threw %v", reading.Errors)
			}
		})
	}
}
