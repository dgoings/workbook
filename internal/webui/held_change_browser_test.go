package webui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a reader sees, in a real browser, when a proxy in front of the board
// asks the page to send their change later. The Node harness pins the page's
// logic; this pins that the notice is on the screen and legible at desktop and
// phone sizes, that the browser is asked to warn before the tab is left while
// the change waits and stops asking once it is sent, that what the reader types
// meanwhile survives the save landing, and that a change refused through every
// resend ends in a plain sentence rather than a JSON parse error.
//
// With WORKBOOK_TEST_SCREENSHOT_DIR set, the waiting notice is photographed at
// each size.

const heldBrowserTaskID = "WB-01J0000000000000000000HC11"

// heldChangeResends is HELD_CHANGE_RESENDS in mutations.js.
const heldChangeResends = 3

// limitingProxy stands in front of the board the way nginx's limit_req does:
// it answers the first `refusals` saves of the task with 429, a Retry-After,
// and an HTML page, without passing them on, and hands everything else to the
// board.
type limitingProxy struct {
	board    http.Handler
	refusals int

	mutex   sync.Mutex
	refused int
	saved   []core.UpdateInput
}

func (proxy *limitingProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPatch && request.URL.Path == "/api/tasks/"+heldBrowserTaskID {
		proxy.mutex.Lock()
		refuse := proxy.refused < proxy.refusals
		if refuse {
			proxy.refused++
		}
		proxy.mutex.Unlock()
		if refuse {
			writer.Header().Set("Content-Type", "text/html")
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(writer, "<html>\r\n<head><title>429 Too Many Requests</title></head>\r\n<body>\r\n<center><h1>429 Too Many Requests</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n")
			return
		}
	}
	proxy.board.ServeHTTP(writer, request)
}

func heldBrowserBoard(t *testing.T, refusals int) *limitingProxy {
	t.Helper()
	task := clientPlacementTask(heldBrowserTaskID, "Held in a browser", core.StatusReady, core.PriorityMedium)
	task.Description = "Original."
	task.Head = "head-1"
	proxy := &limitingProxy{refusals: refusals}
	var current = task
	proxy.board = NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			proxy.mutex.Lock()
			defer proxy.mutex.Unlock()
			return []core.Task{current}, nil
		},
		Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
			proxy.mutex.Lock()
			defer proxy.mutex.Unlock()
			if input.Title != nil {
				current.Title = *input.Title
			}
			if input.Description != nil {
				current.Description = *input.Description
			}
			current.Head = fmt.Sprintf("head-%d", len(proxy.saved)+2)
			proxy.saved = append(proxy.saved, input)
			return core.MutationResult{Task: current}, nil
		},
	})
	return proxy
}

// heldBrowserScript edits the title, saves, and reports what the page drew
// while the save waited, then types into the description meanwhile and
// reports what the page drew once the save was settled one way or the other.
const heldBrowserScript = `(async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const until = async (test, what) => {
    for (let index = 0; index < 600; index += 1) { if (test()) return; await sleep(25); }
    throw new Error("the page never " + what);
  };
  const box = (element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, left: rect.left, width: rect.width, height: rect.height };
  };
  // Whether leaving now would be stopped to ask: a cancelable beforeunload
  // the page cancels.
  const leaving = () => {
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    return event.defaultPrevented;
  };
  await until(() => document.querySelector("#task-title"), "drew the task form");
  const title = document.querySelector("#task-title");
  title.value = "Renamed behind a proxy";
  title.dispatchEvent(new Event("input", { bubbles: true }));
  const leaveWarnedBefore = leaving();
  // Where a reader is when they press Save: scrolled to it, which on a phone
  // is well below the notice at the top of the page.
  document.querySelector(".save-button").scrollIntoView({ block: "center" });
  await sleep(50);
  document.querySelector(".save-button").click();
  const notice = document.querySelector("[data-held-change-notice]");
  await until(() => !notice.hidden && notice.getBoundingClientRect().height > 0, "showed the held-change notice");
  const paragraph = notice.querySelector("p");
  const save = document.querySelector(".save-button");
  const said = document.querySelector("[data-save-status]");
  await until(() => said.textContent.trim() !== "", "said the save is held beside Save");
  // The held line's ink and what it stands on, in each scheme: a held save
  // is pending, not failed, so it is set in the board's pending ink — the
  // held-change notice's own — and never in the form's error ink.
  const inks = (scheme) => {
    const root = document.documentElement;
    const before = root.getAttribute("data-scheme");
    root.setAttribute("data-scheme", scheme);
    const probe = document.createElement("span");
    said.parentElement.append(probe);
    probe.style.color = "var(--wb-warning-ink)";
    const pending = getComputedStyle(probe).color;
    probe.style.color = "var(--wb-danger-ink)";
    const error = getComputedStyle(probe).color;
    probe.remove();
    let ground = "";
    for (let node = said; node && !ground; node = node.parentElement) {
      const color = getComputedStyle(node).backgroundColor;
      if (color && color !== "transparent" && !/^rgba\(.*,\s*0\)$/.test(color)) ground = color;
    }
    const result = { color: getComputedStyle(said).color, ground, pending, error };
    if (before === null) root.removeAttribute("data-scheme"); else root.setAttribute("data-scheme", before);
    return result;
  };
  const waiting = {
    inks: { light: inks("light"), dark: inks("dark") },
    leaveWarned: leaving(),
    notice: box(notice),
    noticeText: notice.textContent.trim(),
    fontSize: parseFloat(getComputedStyle(paragraph).fontSize),
    lineHeight: parseFloat(getComputedStyle(paragraph).lineHeight),
    saveDisabled: save.disabled,
    save: box(save),
    statusText: said.textContent.trim(),
    status: box(said),
    scrollWidth: document.documentElement.scrollWidth,
  };
  // The reader goes on editing the title while the save waits.
  title.value = "Renamed behind a proxy, and then some";
  title.dispatchEvent(new Event("input", { bubbles: true }));
  /*while-waiting*/
  await until(() => notice.hidden && !said.textContent.includes("held in this tab"), "settled the save");
  await sleep(300);
  const status = document.querySelector("[data-save-status]");
  return {
    viewport: { width: innerWidth, height: innerHeight },
    leaveWarnedBefore,
    waiting,
    noticeDisplayAfter: getComputedStyle(notice).display,
    leaveWarnedAfter: leaving(),
    path: location.pathname,
    titleAfter: document.querySelector("#task-title") ? document.querySelector("#task-title").value : null,
    statusText: status ? status.textContent.trim() : "",
    statusTone: status ? status.getAttribute("data-tone") : null,
    status: status ? box(status) : null,
    saveDisabledAfter: document.querySelector(".save-button") ? document.querySelector(".save-button").disabled : null,
  };
})()`

type heldBrowserBox struct{ Top, Left, Width, Height float64 }

// heldBrowserInk is the held line's computed color in one scheme, the
// background it stands on, and the two inks it could be set in.
type heldBrowserInk struct{ Color, Ground, Pending, Error string }

// computedHex is a computed CSS color, as a browser reports it ("rgb(r, g,
// b)"), written as #rrggbb for contrastRatio.
func computedHex(t *testing.T, color string) string {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(strings.Replace(color, "rgba(", "rgb(", 1), "rgb(%d, %d, %d", &r, &g, &b); err != nil {
		t.Fatalf("the browser reported a color %q this cannot read: %v", color, err)
	}
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

type heldBrowserResult struct {
	Viewport          struct{ Width, Height float64 }
	LeaveWarnedBefore bool
	Waiting           struct {
		LeaveWarned          bool
		Notice               heldBrowserBox
		NoticeText           string
		FontSize, LineHeight float64
		SaveDisabled         bool
		Save                 heldBrowserBox
		StatusText           string
		Status               heldBrowserBox
		ScrollWidth          float64
		Inks                 struct{ Light, Dark heldBrowserInk }
	}
	NoticeDisplayAfter string
	LeaveWarnedAfter   bool
	Path               string
	TitleAfter         *string
	StatusText         string
	StatusTone         *string
	Status             *heldBrowserBox
	SaveDisabledAfter  *bool
}

func TestBrowserHoldsAChangeAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	shots := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	for _, outcome := range []struct {
		name     string
		refusals int
	}{
		// One refusal: the save waits a second, is sent again, and lands.
		{"lands", 1},
		// Every send refused: the save is reported once the resends run out.
		{"refused", 1 + heldChangeResends},
	} {
		for _, size := range []struct{ width, height int }{{1440, 900}, {1280, 800}, {390, 844}} {
			t.Run(fmt.Sprintf("%s at %dx%d", outcome.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				proxy := heldBrowserBoard(t, outcome.refusals)
				screenshot := ""
				if shots != "" {
					screenshot = filepath.Join(shots, fmt.Sprintf("held-change-%s-%dx%d.png", outcome.name, size.width, size.height))
				}
				var result heldBrowserResult
				openInBrowser(t, proxy, browserPage{
					Width: size.width, Height: size.height,
					Path:       "/tasks/" + heldBrowserTaskID,
					Script:     heldBrowserScript,
					Screenshot: screenshot,
				}, &result)
				if shots != "" && outcome.name == "lands" {
					// The page as it stands while the change waits, which the
					// run above has moved past by the time it is photographed.
					var waiting struct {
						Waiting struct{ Notice heldBrowserBox }
					}
					openInBrowser(t, heldBrowserBoard(t, 1+heldChangeResends), browserPage{
						Width: size.width, Height: size.height,
						Path:       "/tasks/" + heldBrowserTaskID,
						Script:     strings.Replace(heldBrowserScript, "/*while-waiting*/", "return { waiting };", 1),
						Screenshot: filepath.Join(shots, fmt.Sprintf("held-change-waiting-%dx%d.png", size.width, size.height)),
					}, &waiting)
				}
				t.Logf("%s at %dx%d: %+v", outcome.name, size.width, size.height, result)

				waiting := result.Waiting
				if result.LeaveWarnedBefore {
					t.Error("leaving was stopped to ask before anything waited")
				}
				if !waiting.LeaveWarned {
					t.Error("leaving was not stopped to ask while the change waited")
				}
				if !strings.Contains(waiting.NoticeText, "held in this tab") {
					t.Errorf("the notice reads %q", waiting.NoticeText)
				}
				notice := waiting.Notice
				if notice.Left < 0 || notice.Left+notice.Width > result.Viewport.Width+0.5 || notice.Top < 0 || notice.Top+notice.Height > result.Viewport.Height {
					t.Errorf("the notice at %+v is not inside the %vx%v viewport", notice, result.Viewport.Width, result.Viewport.Height)
				}
				// 0.85rem, the size of every notice the board shows.
				if waiting.FontSize < 13 {
					t.Errorf("the notice is set in %vpx, too small to read", waiting.FontSize)
				}
				// Beside Save, where the reader is, it says the same thing: on a
				// phone the notice is a scroll away.
				if !strings.Contains(waiting.StatusText, "held in this tab") {
					t.Errorf("the form says %q beside Save while the save waits", waiting.StatusText)
				}
				for scheme, ink := range map[string]heldBrowserInk{"light": waiting.Inks.Light, "dark": waiting.Inks.Dark} {
					if ink.Color != ink.Pending || ink.Color == ink.Error {
						t.Errorf("in the %s scheme the held line is set in %s, want the pending ink %s rather than the error ink %s", scheme, ink.Color, ink.Pending, ink.Error)
					}
					if ratio := contrastRatio(t, computedHex(t, ink.Color), computedHex(t, ink.Ground)); ratio < 4.5 {
						t.Errorf("in the %s scheme the held line's %s on %s has contrast %.2f, under 4.5", scheme, ink.Color, ink.Ground, ratio)
					} else {
						t.Logf("%s scheme: held line %s on %s, contrast %.2f", scheme, ink.Color, ink.Ground, ratio)
					}
				}
				status := waiting.Status
				if status.Top < 0 || status.Top+status.Height > result.Viewport.Height || status.Left < 0 || status.Left+status.Width > result.Viewport.Width+0.5 {
					t.Errorf("the form's status at %+v is not on screen in the %vx%v viewport", status, result.Viewport.Width, result.Viewport.Height)
				}
				if waiting.ScrollWidth > result.Viewport.Width {
					t.Errorf("the page scrolls sideways while the notice is up: %v wide in a %v viewport", waiting.ScrollWidth, result.Viewport.Width)
				}
				if !waiting.SaveDisabled {
					t.Error("Save stayed live while its change waited")
				}
				if result.NoticeDisplayAfter != "none" {
					t.Errorf("the notice is still drawn (display %q) once nothing waits", result.NoticeDisplayAfter)
				}
				if result.LeaveWarnedAfter {
					t.Error("leaving is still stopped to ask once nothing waits")
				}
				if result.Path != "/tasks/"+heldBrowserTaskID {
					t.Errorf("the page left the task form for %s", result.Path)
				}
				if result.TitleAfter == nil || *result.TitleAfter != "Renamed behind a proxy, and then some" {
					t.Errorf("what was typed while the save waited reads %v", result.TitleAfter)
				}
				if result.SaveDisabledAfter == nil || *result.SaveDisabledAfter {
					t.Error("Save is not live again once the save settled")
				}
				// What the save ends in is the form's own word, not the held
				// line's pending ink carried past it.
				if result.StatusTone != nil {
					t.Errorf("the form's status kept the tone %q once the save settled", *result.StatusTone)
				}
				proxy.mutex.Lock()
				refused, saved := proxy.refused, proxy.saved
				proxy.mutex.Unlock()
				if refused != outcome.refusals {
					t.Errorf("the proxy refused %d sends, want %d", refused, outcome.refusals)
				}
				switch outcome.name {
				case "lands":
					if len(saved) != 1 || saved[0].Title == nil || *saved[0].Title != "Renamed behind a proxy" {
						t.Errorf("the board recorded %d saves, want the first title once", len(saved))
					}
					if !strings.Contains(result.StatusText, "not saved yet") {
						t.Errorf("the form says %q, want it to say what is not saved yet", result.StatusText)
					}
				case "refused":
					if len(saved) != 0 {
						t.Errorf("the board recorded %d saves the proxy never passed on", len(saved))
					}
					want := "The server was still asking this page to wait after 3 resends, so this change was not sent again. Try it again in a moment."
					if result.StatusText != want {
						t.Errorf("the form says %q, want %q", result.StatusText, want)
					}
				}
			})
		}
	}
}
