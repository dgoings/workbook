package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The board's notices on a phone, with two showing at once, produced the way
// a reader produces them: a proxy in front of the board asks the page to send
// one task's save later, which raises the notice that the change is held, and
// a save of another task is refused after the reader has left its page, which
// raises the notice that the save failed, with its Open task and Dismiss.
// Every notice showing is drawn whole and apart from the others, each of
// their controls can be reached and pressed, the notices together never take
// more than a third of the screen — a phone with its keyboard up included —
// without scrolling inside themselves, and a control brought into view by
// focus or by a script is never left underneath them.

// heldPhoneBoard is the phone board behind a proxy that answers every save of
// phoneTaskID with 429 and a Retry-After long enough to outlast the test, and
// a board whose save of phoneWideID is refused after a pause long enough for
// the reader to leave its page.
func heldPhoneBoard(t *testing.T) http.Handler {
	t.Helper()
	board := newPhoneBoard()
	options := board.options(t)
	update := options.Update
	options.Update = func(ctx context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
		if id == phoneWideID {
			time.Sleep(1500 * time.Millisecond)
			return core.MutationResult{}, errors.New("the disk is full")
		}
		return update(ctx, id, input)
	}
	handler := NewHandler(options)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch && request.URL.Path == "/api/tasks/"+phoneTaskID {
			writer.Header().Set("Content-Type", "text/html")
			writer.Header().Set("Retry-After", "120")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(writer, "<html><body><h1>429 Too Many Requests</h1></body></html>")
			return
		}
		handler.ServeHTTP(writer, request)
	})
}

type phoneNoticeBox struct{ Top, Bottom float64 }

type phoneNoticeReading struct {
	Unfinished string
	Viewport   float64
	// Box is the notices' box, Notices each notice showing in it, and
	// Overlaps any two of them drawn over one another.
	Box      phoneNoticeBox
	Notices  map[string]phoneNoticeBox
	Overlaps []string
	// Scrolls is whether the box scrolls inside itself, and Unreachable
	// every control in it that could not be brought fully into view and hit
	// at its middle.
	Scrolls     bool
	Controls    []string
	Unreachable []string
	// Padding is the page's scroll-padding-bottom while the notices show.
	Padding string
	// The task page's Save with the held notice up: where it is after focus
	// takes it from under the notices, and after a script scrolls it to the
	// bottom edge, and whether a tap at its middle lands on it each time.
	FocusSave     phoneNoticeBox
	FocusBox      phoneNoticeBox
	FocusHit      bool
	EndSave       phoneNoticeBox
	EndBox        phoneNoticeBox
	EndHit        bool
	SaveTapped    bool
	Dismissed     bool
	Sideways      float64
	Refused       []string
	Errors        []string
	FailureText   string
	HeldText      string
	SaveUnderAtGo bool
	// PageRoom is how far the page could scroll with the notices up, and
	// SwipeScrolled how far two strokes of a finger on the notices moved it.
	PageRoom      float64
	SwipeScrolled float64
}

const phoneNoticeScript = phoneHelpers + `
(async () => {
  const reading = { Viewport: innerHeight, Notices: {}, Overlaps: [], Controls: [], Unreachable: [] };
  const rect = (element) => { const r = element.getBoundingClientRect(); return { Top: r.top, Bottom: r.bottom }; };
  const box = document.querySelector("[data-notices]");
  const held = document.querySelector("[data-held-change-notice]");
  const failure = document.querySelector("[data-notice]");
  // A tap at an element's middle reaches it rather than something drawn
  // over it.
  const hits = (element) => {
    const r = element.getBoundingClientRect();
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return Boolean(at && (at === element || element.contains(at)));
  };
  try {
    // A held save of the first task.
    await waitFor(() => document.querySelector("#task-title") && document.querySelector("[data-change-list]"), "the first task's page");
    await settle();
    await tap(document.querySelector("#task-title"));
    await type(" again");
    await tap(document.querySelector(".task-actions .save-button"));
    await waitFor(() => !held.hidden && held.getBoundingClientRect().height > 0, "the held-change notice");

    // The second task, opened from its card on the board.
    await tap(document.querySelector(".task-route__close"));
    await waitFor(() => location.pathname === "/" && document.querySelector('.column[data-phone-current] .task-card[data-task-id="` + phoneWideID + `"] h3 a'), "the board");
    await tap(document.querySelector('.column[data-phone-current] .task-card[data-task-id="` + phoneWideID + `"] h3 a'));
    await waitFor(() => location.pathname === "/tasks/` + phoneWideID + `" && document.querySelector("#task-title") && document.querySelector(".task-actions .save-button"), "the second task's page");
    await settle();
    await tap(document.querySelector("#task-title"));
    await type(" renamed");
    const save = document.querySelector(".task-actions .save-button");

    // Save, left under the notices, is taken out from under them by focus.
    const boxTop = () => box.getBoundingClientRect().top;
    window.scrollTo(0, Math.max(0, save.getBoundingClientRect().bottom + scrollY - innerHeight + 4));
    await settle();
    reading.SaveUnderAtGo = save.getBoundingClientRect().bottom > boxTop() + 0.5;
    document.activeElement.blur();
    save.focus();
    await settle();
    reading.FocusSave = rect(save); reading.FocusBox = rect(box); reading.FocusHit = hits(save);
    // And by a script that scrolls it to the bottom edge.
    window.scrollTo(0, 0);
    await settle();
    save.scrollIntoView({ block: "end" });
    await settle();
    reading.EndSave = rect(save); reading.EndBox = rect(box); reading.EndHit = hits(save);
    reading.Padding = getComputedStyle(document.documentElement).scrollPaddingBottom;

    // Tapped where it is, it sends the save; the reader leaves before the
    // answer, which is a refusal.
    const sent = () => save.disabled || Boolean(document.querySelector("[data-save-status]") && document.querySelector("[data-save-status]").textContent.trim() !== "");
    const r = save.getBoundingClientRect();
    await touchInput({ x: r.left + r.width / 2, y: r.top + r.height / 2 });
    for (let waited = 0; waited < 2000 && !sent(); waited += 50) await pause(50);
    reading.SaveTapped = sent();
    // A tap that missed is reported, and the save sent another way, so the
    // rest of what is checked still is.
    if (!reading.SaveTapped) save.click();
    await tap(document.querySelector(".task-route__close"));
    await waitFor(() => !failure.hidden && failure.textContent.includes("was not saved"), "the failure notice");
    await pause(300);
    await settle();

    // Both notices, together.
    reading.FailureText = failure.textContent.trim();
    reading.HeldText = held.textContent.trim();
    reading.Box = rect(box);
    reading.Scrolls = box.scrollHeight > box.clientHeight + 0.5 && ["auto", "scroll"].includes(getComputedStyle(box).overflowY);
    reading.Padding = getComputedStyle(document.documentElement).scrollPaddingBottom;
    const shown = [...box.querySelectorAll(".notice")].filter((notice) => !notice.hidden);
    for (const notice of shown) reading.Notices[notice.hasAttribute("data-notice") ? "failure" : notice.hasAttribute("data-held-change-notice") ? "held" : "vocabulary"] = rect(notice);
    for (let i = 0; i < shown.length; i++) for (let j = i + 1; j < shown.length; j++) {
      const a = shown[i].getBoundingClientRect(), b = shown[j].getBoundingClientRect();
      if (a.bottom > b.top + 0.5 && b.bottom > a.top + 0.5) reading.Overlaps.push(i + " and " + j);
    }
    // Every control in the box, brought into view inside it, is whole on the
    // screen and takes a tap at its middle.
    for (const control of box.querySelectorAll("a[href], button")) {
      if (control.getBoundingClientRect().height === 0) continue;
      reading.Controls.push(describe(control));
      control.scrollIntoView({ block: "nearest" });
      await settle();
      const at = control.getBoundingClientRect();
      if (at.top < -0.5 || at.bottom > innerHeight + 0.5 || !hits(control)) reading.Unreachable.push(describe(control) + " at " + at.top.toFixed(1) + "-" + at.bottom.toFixed(1));
    }
    reading.Sideways = sideways();
    // A finger on the notices still scrolls the page: once, which may only
    // scroll the box to its end, and again.
    window.scrollTo(0, 0);
    box.scrollTop = 0;
    await settle();
    reading.PageRoom = document.documentElement.scrollHeight - innerHeight;
    for (let stroke = 0; stroke < 2; stroke++) {
      const on = box.getBoundingClientRect();
      await touchInput({ x: on.left + on.width / 2, y: on.top + Math.min(on.height / 2, 40), dy: -Math.min(200, on.top - 20), steps: 12 });
      await pause(400);
    }
    reading.SwipeScrolled = scrollY;
    window.scrollTo(0, 0);
    await settle();
    const dismiss = [...failure.querySelectorAll("button")].find((button) => button.textContent === "Dismiss");
    await tap(dismiss);
    reading.Dismissed = failure.hidden;
  } catch (error) {
    reading.Unfinished = String(error && error.message || error);
  }
  reading.Refused = window.__refused;
  reading.Errors = window.__errors;
  return reading;
})()
`

func TestBrowserPhoneNoticesShowTogetherAndCoverNothing(t *testing.T) {
	t.Parallel()
	sizes := append(append([]struct{ width, height int }{}, phoneSizes...), struct{ width, height int }{360, 400})
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			var reading phoneNoticeReading
			openInBrowser(t, heldPhoneBoard(t), browserPage{
				Width: size.width, Height: size.height, Phone: true, Path: "/tasks/" + phoneTaskID,
				BeforeLoad: phoneWatch, Script: phoneNoticeScript, Screenshot: phoneShot("notices", size.width, size.height),
			}, &reading)
			t.Logf("at %dx%d: notices box %.1f-%.1f, notices %v, scroll-padding-bottom %s; Save after focus %.1f-%.1f (box from %.1f), after scrolling to the end %.1f-%.1f (box from %.1f)",
				size.width, size.height, reading.Box.Top, reading.Box.Bottom, reading.Notices, reading.Padding,
				reading.FocusSave.Top, reading.FocusSave.Bottom, reading.FocusBox.Top, reading.EndSave.Top, reading.EndSave.Bottom, reading.EndBox.Top)
			if reading.Unfinished != "" {
				t.Fatalf("the reader could not finish: %s", reading.Unfinished)
			}
			if _, ok := reading.Notices["failure"]; !ok {
				t.Errorf("the failure notice is not showing: %v", reading.Notices)
			}
			if _, ok := reading.Notices["held"]; !ok {
				t.Errorf("the held-change notice is not showing: %v", reading.Notices)
			}
			if len(reading.Overlaps) > 0 {
				t.Errorf("notices %v are drawn over one another: %v", reading.Overlaps, reading.Notices)
			}
			if !strings.Contains(reading.FailureText, "Open task") || !strings.Contains(reading.FailureText, "Dismiss") {
				t.Errorf("the failure notice reads %q, without its Open task and Dismiss", reading.FailureText)
			}
			if len(reading.Controls) < 2 || len(reading.Unreachable) > 0 {
				t.Errorf("of the notices' controls %v, these cannot be brought into view and tapped: %v", reading.Controls, reading.Unreachable)
			}
			if height := reading.Box.Bottom - reading.Box.Top; height > reading.Viewport/3+1 {
				t.Errorf("the notices take %.1fpx of a %.0fpx screen, more than a third", height, reading.Viewport)
			} else if height > reading.Viewport/3-1 && !reading.Scrolls {
				t.Errorf("the notices fill a third of the screen (%.1fpx) and do not scroll, so what is below cannot be reached", height)
			}
			if reading.Box.Bottom > reading.Viewport+0.5 || reading.Box.Bottom < reading.Viewport-0.5 {
				t.Errorf("the notices end at %.1f on a %.0fpx screen, not at its bottom edge", reading.Box.Bottom, reading.Viewport)
			}
			if !reading.SaveUnderAtGo {
				t.Errorf("Save was never under the notices before focus, so focus proved nothing")
			}
			if reading.FocusSave.Bottom > reading.FocusBox.Top+0.5 || !reading.FocusHit {
				t.Errorf("focused, Save is at %.1f-%.1f under notices from %.1f (a tap there reaches it: %v)", reading.FocusSave.Top, reading.FocusSave.Bottom, reading.FocusBox.Top, reading.FocusHit)
			}
			if reading.EndSave.Bottom > reading.EndBox.Top+0.5 || !reading.EndHit {
				t.Errorf("scrolled to the bottom edge, Save is at %.1f-%.1f under notices from %.1f (a tap there reaches it: %v)", reading.EndSave.Top, reading.EndSave.Bottom, reading.EndBox.Top, reading.EndHit)
			}
			if !reading.SaveTapped {
				t.Errorf("a tap on Save with the held notice up sent nothing")
			}
			if reading.PageRoom > 60 && reading.SwipeScrolled < 20 {
				t.Errorf("two strokes of a finger on the notices scrolled the page by %.1fpx of the %.1fpx it could scroll; the notices swallow the page's scrolling", reading.SwipeScrolled, reading.PageRoom)
			}
			if !reading.Dismissed {
				t.Errorf("a tap on the failure notice's Dismiss left it showing")
			}
			if reading.Sideways > 0 {
				t.Errorf("the board scrolls sideways by %.1fpx with the notices up", reading.Sideways)
			}
			if len(reading.Refused) > 0 || len(reading.Errors) > 0 {
				t.Errorf("the page had things refused by its policy %v, or threw %v", reading.Refused, reading.Errors)
			}
		})
	}
}
