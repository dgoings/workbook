package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a reader sees, in a real browser, when the board is too busy for their
// change. The Node harness pins the page's logic; this pins that the notice is
// on the screen at a desktop and a phone size, legible, and that the form the
// reader typed into still holds what they typed while the change waits and
// after it is finally refused.

const busyBrowserTaskID = "WB-01J0000000000000000000BZ11"

// busyPageResends is BUSY_RETRIES in mutations.js: how many times the page
// sends a refused change again before it reports the refusal.
const busyPageResends = 3

// busyFront refuses the first `refusals` saves of the task with the cap's own
// answer and hands the rest to the board.
type busyFront struct {
	inner    http.Handler
	refusals int

	mutex   sync.Mutex
	refused int
	saved   []string
}

func (front *busyFront) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPatch && request.URL.Path == "/api/tasks/"+busyBrowserTaskID {
		front.mutex.Lock()
		refuse := front.refused < front.refusals
		if refuse {
			front.refused++
		}
		front.mutex.Unlock()
		if refuse {
			writeBusy(writer, fmt.Sprintf(busyWorkMessage, DefaultMaxRequests))
			return
		}
	}
	front.inner.ServeHTTP(writer, request)
}

func busyBrowserBoard(t *testing.T, refusals int) *busyFront {
	t.Helper()
	task := clientPlacementTask(busyBrowserTaskID, "Busy in a browser", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	front := &busyFront{refusals: refusals}
	front.inner = NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil },
		Update: func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			updated := task
			if input.Title != nil {
				updated.Title = *input.Title
			}
			updated.Head = "head-2"
			front.mutex.Lock()
			front.saved = append(front.saved, updated.Title)
			front.mutex.Unlock()
			return core.MutationResult{Task: updated}, nil
		},
	})
	return front
}

// busyBrowserScript types a new title, saves, and reports what the page drew
// while the save waited and once it was settled one way or the other.
const busyBrowserScript = `(async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const until = async (test, what) => {
    for (let index = 0; index < 600; index += 1) { if (test()) return; await sleep(50); }
    throw new Error("the page never " + what);
  };
  await until(() => document.querySelector("#task-title"), "drew the task form");
  const title = document.querySelector("#task-title");
  title.value = "Renamed while the board was busy";
  title.dispatchEvent(new Event("input", { bubbles: true }));
  document.querySelector(".save-button").click();
  const notice = document.querySelector("[data-busy-notice]");
  await until(() => !notice.hidden, "showed the busy notice");
  const box = notice.getBoundingClientRect();
  const text = notice.querySelector("p");
  // Whether leaving now would be stopped to ask, which is what the page arms
  // while a change waits: a cancelable beforeunload the page cancels.
  const leaving = () => {
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    return event.defaultPrevented;
  };
  const waiting = {
    leaveWarned: leaving(),
    top: box.top, bottom: box.bottom, left: box.left, right: box.right,
    display: getComputedStyle(notice).display,
    fontSize: parseFloat(getComputedStyle(text).fontSize),
    text: notice.textContent.trim(),
    typed: document.querySelector("#task-title").value,
  };
  await until(() => notice.hidden, "took the busy notice down");
  await sleep(200);
  const form = document.querySelector("form");
  return {
    viewport: { width: innerWidth, height: innerHeight },
    scrollWidth: document.documentElement.scrollWidth,
    waiting,
    noticeDisplayAfter: getComputedStyle(notice).display,
    leaveWarnedAfter: leaving(),
    typedAfter: document.querySelector("#task-title") ? document.querySelector("#task-title").value : null,
    formText: form ? form.textContent : "",
  };
})()`

type busyBrowserResult struct {
	Viewport    struct{ Width, Height float64 }
	ScrollWidth float64
	Waiting     struct {
		LeaveWarned              bool
		Top, Bottom, Left, Right float64
		Display                  string
		FontSize                 float64
		Text                     string
		Typed                    string
	}
	NoticeDisplayAfter string
	LeaveWarnedAfter   bool
	TypedAfter         *string
	FormText           string
}

func TestBrowserTellsTheReaderTheBoardIsBusyAndKeepsTheirChange(t *testing.T) {
	t.Parallel()
	for _, viewport := range []struct{ width, height int }{{1440, 900}, {390, 844}} {
		for _, test := range []struct {
			name     string
			refusals int
		}{
			// Refused once: the save waits two seconds and goes through.
			{name: "sent again", refusals: 1},
			// Refused every time: after three resends the form says so and
			// still holds the title the reader typed.
			{name: "still busy", refusals: 1 + busyPageResends},
		} {
			t.Run(fmt.Sprintf("%dx%d %s", viewport.width, viewport.height, test.name), func(t *testing.T) {
				t.Parallel()
				front := busyBrowserBoard(t, test.refusals)
				var result busyBrowserResult
				openInBrowser(t, front, browserPage{
					Width: viewport.width, Height: viewport.height,
					Path:   "/tasks/" + busyBrowserTaskID,
					Script: busyBrowserScript,
				}, &result)
				t.Logf("%dx%d %s: notice %+v; after: display %s, typed %v", viewport.width, viewport.height, test.name, result.Waiting, result.NoticeDisplayAfter, result.TypedAfter)
				if result.Viewport.Width != float64(viewport.width) {
					t.Fatalf("the page was drawn %v wide, want %d", result.Viewport.Width, viewport.width)
				}
				if result.ScrollWidth > result.Viewport.Width {
					t.Fatalf("the page scrolls sideways: %v wide in a %v viewport", result.ScrollWidth, result.Viewport.Width)
				}
				waiting := result.Waiting
				if waiting.Display == "none" || waiting.Top < 0 || waiting.Bottom > result.Viewport.Height || waiting.Left < 0 || waiting.Right > result.Viewport.Width {
					t.Fatalf("the busy notice is not on the screen while the change waits: %+v in %+v", waiting, result.Viewport)
				}
				if waiting.Bottom-waiting.Top < 20 || waiting.FontSize < 12 {
					t.Fatalf("the busy notice is %v tall in %vpx type, want a readable line", waiting.Bottom-waiting.Top, waiting.FontSize)
				}
				if !strings.Contains(waiting.Text, "busy") || !strings.Contains(waiting.Text, "held in this tab") ||
					!strings.Contains(waiting.Text, "closing or leaving the tab before then loses it") {
					t.Fatalf("the busy notice says %q, want it to say the board is busy, the change is held in this tab, and what would lose it", waiting.Text)
				}
				if !waiting.LeaveWarned {
					t.Fatal("leaving the page while a change waited to be sent was not stopped to ask")
				}
				if result.LeaveWarnedAfter {
					t.Fatal("leaving the page once nothing was waiting is still stopped to ask")
				}
				if waiting.Typed != "Renamed while the board was busy" {
					t.Fatalf("the title field held %q while the change waited, want what was typed", waiting.Typed)
				}
				if result.NoticeDisplayAfter != "none" {
					t.Fatalf("the busy notice is still drawn (%s) once nothing is waiting", result.NoticeDisplayAfter)
				}
				front.mutex.Lock()
				saved := append([]string(nil), front.saved...)
				front.mutex.Unlock()
				if test.refusals == 1 {
					if len(saved) != 1 || saved[0] != "Renamed while the board was busy" {
						t.Fatalf("the board recorded %q, want the one resent save", saved)
					}
					return
				}
				if len(saved) != 0 {
					t.Fatalf("the board recorded %q for a save it refused every time", saved)
				}
				if result.TypedAfter == nil || *result.TypedAfter != "Renamed while the board was busy" {
					t.Fatalf("after the refusal the title field holds %v, want what was typed", result.TypedAfter)
				}
				if !strings.Contains(result.FormText, "busy") {
					t.Fatalf("the form does not say the board was busy: %q", result.FormText)
				}
			})
		}
	}
}
