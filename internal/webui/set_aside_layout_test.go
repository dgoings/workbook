package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// setAsideBoard serves one task with two sets of edits set aside — a long
// description and a long comment from one conflict, another long comment from
// a second — and dismisses either. It records how many Origin headers each
// request that changes something arrived with, and answers /origin-probe with
// the Origin values of the request itself, so a page can ask what its browser
// sent.
type setAsideBoard struct {
	mu      sync.Mutex
	edits   []core.SetAsideEdit
	origins []int
}

func newSetAsideBoard() *setAsideBoard {
	long := func(what string) string {
		return strings.Repeat("The board's sync kept origin's text, and this paragraph is the "+what+" it set aside. ", 8) +
			"\n\n" + strings.Repeat("unbroken", 24)
	}
	description := setAsideEdit()
	description.Operations[0].Value = long("description")
	remark := setAsideEdit()
	remark.Commit = "parked-tip-2"
	remark.Operations = []core.Operation{{ID: "01K0M6B8A4FTT8C39MXXYTWD01", Type: core.OperationCommentAdd, Body: long("comment")}}
	comment := setAsideEdit()
	comment.ParkedRef = "refs/workbook/reconciled/" + threadPageTaskID + "/1"
	comment.Parked, comment.Commit = "parked-other", "parked-other"
	comment.Actor = "bob@client.example"
	comment.Operations = []core.Operation{{ID: "01K0M6B8A4FTT8C39MXXYTWD02", Type: core.OperationCommentAdd, Body: long("second comment")}}
	return &setAsideBoard{edits: []core.SetAsideEdit{description, remark, comment}}
}

func (board *setAsideBoard) handler(t *testing.T) http.Handler {
	t.Helper()
	task := threadPageTask()
	inner := NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		SetAside: func(context.Context, map[string]string) (map[string][]core.SetAsideEdit, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			if len(board.edits) == 0 {
				return nil, nil
			}
			return map[string][]core.SetAsideEdit{task.ID: append([]core.SetAsideEdit(nil), board.edits...)}, nil
		},
		DismissSetAside: func(_ context.Context, _, parkedRef, parked string) error {
			board.mu.Lock()
			defer board.mu.Unlock()
			kept := board.edits[:0]
			for _, edit := range board.edits {
				if edit.ParkedRef != parkedRef || edit.Parked != parked {
					kept = append(kept, edit)
				}
			}
			board.edits = kept
			return nil
		},
	})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/origin-probe" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(request.Header.Values("Origin"))
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			board.mu.Lock()
			board.origins = append(board.origins, len(request.Header.Values("Origin")))
			board.mu.Unlock()
		}
		inner.ServeHTTP(writer, request)
	})
}

// The notice a set-aside edit draws on the task page fits every screen the
// board is read on: it sits above the form, its text wraps rather than
// widening the page, its list scrolls rather than pushing the form away, the
// list is the one thing in it that scrolls — every text is drawn whole within
// it, and the last Dismiss can be scrolled to — and its buttons are a
// finger's height. Dismissing one set takes it away and
// leaves the other. The card on the board says the same in one line inside
// its own width.
//
// The same page answers the Origin question the guard relies on: the
// dismissal arrives with one Origin header, and a page that asks fetch for a
// second, or for a different one, is not obeyed, because Origin is a header
// name fetch forbids a page to set.
func TestBrowserShowsSetAsideEditsOnEveryScreen(t *testing.T) {
	t.Parallel()
	shots := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	for _, size := range []struct{ width, height int }{{1440, 900}, {1280, 800}, {390, 844}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			shot := func(name string) string {
				if shots == "" {
					return ""
				}
				return filepath.Join(shots, fmt.Sprintf("set-aside-%s-%dx%d.png", name, size.width, size.height))
			}

			board := newSetAsideBoard()
			var page struct {
				Heading                           string
				Visible, AboveForm, PageFits      bool
				NoticeLeft, NoticeRight, Viewport float64
				NoticeHeight, ListHeight, FormTop float64
				TextWraps                         bool
				TextWidth, TextClient             float64
				SmallestButton                    float64
				ButtonsInside                     bool
				Groups, GroupsAfter               int
				HeadingAfter                      string
				ProbeOrigins                      []string
				Copy                              string
				Scrollers                         int
				ListScroll                        float64
				TextHeights                       []float64
				TextsWhole, LastDismissReachable  bool
			}
			openInBrowser(t, board.handler(t), browserPage{
				Width: size.width, Height: size.height,
				Path: "/tasks/" + threadPageTaskID,
				Script: `
(async () => {
  const frames = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const waitFor = async (check, what) => {
    for (let turn = 0; turn < 200; turn++) { if (check()) return; await new Promise((resolve) => setTimeout(resolve, 25)); }
    throw new Error("timed out waiting for " + what);
  };
  const notice = () => document.querySelector("[data-set-aside]");
  await waitFor(() => notice() && !notice().hidden && document.querySelectorAll(".set-aside__group").length === 2, "the set-aside notice");
  await frames();
  const region = notice();
  const box = region.getBoundingClientRect();
  const form = document.querySelector("form.task-layout");
  const list = region.querySelector(".set-aside__list");
  const text = region.querySelector("[data-set-aside-text]");
  const buttons = Array.from(region.querySelectorAll("button"));
  const result = {
    heading: region.querySelector("h3").textContent,
    visible: getComputedStyle(region).display !== "none" && box.height > 0,
    aboveForm: !!form && box.bottom <= form.getBoundingClientRect().top + 0.5,
    pageFits: document.documentElement.scrollWidth <= innerWidth,
    noticeLeft: box.left, noticeRight: box.right, viewport: innerWidth,
    noticeHeight: box.height, listHeight: list.getBoundingClientRect().height,
    formTop: form ? form.getBoundingClientRect().top : -1,
    // The block's own width against the list it sits in: a block that does
    // not wrap grows to its longest line and the list clips it.
    textWraps: text.getBoundingClientRect().right <= list.getBoundingClientRect().right + 0.5 && text.scrollWidth <= text.clientWidth + 1,
    textWidth: text.getBoundingClientRect().width, textClient: list.getBoundingClientRect().width,
    smallestButton: Math.min(...buttons.map((button) => button.getBoundingClientRect().height)),
    buttonsInside: buttons.every((button) => { const b = button.getBoundingClientRect(); return b.left >= box.left - 0.5 && b.right <= box.right + 0.5; }),
    groups: region.querySelectorAll(".set-aside__group").length,
    // Everything in the notice that scrolls its own content: the list, and
    // nothing inside it.
    scrollers: [region, ...region.querySelectorAll("*")].filter((element) => {
      const overflow = getComputedStyle(element).overflowY;
      return (overflow === "auto" || overflow === "scroll") && element.scrollHeight > element.clientHeight + 1;
    }).length,
    listScroll: list.scrollHeight,
    textHeights: Array.from(region.querySelectorAll("[data-set-aside-text]")).map((block) => block.getBoundingClientRect().height),
    textsWhole: Array.from(region.querySelectorAll("[data-set-aside-text]")).every((block) => block.scrollHeight <= block.clientHeight + 1)
  };
  list.scrollTop = list.scrollHeight;
  await frames();
  const last = Array.from(region.querySelectorAll("[data-set-aside-dismiss]")).pop().getBoundingClientRect();
  const shown = list.getBoundingClientRect();
  result.lastDismissReachable = last.top >= shown.top - 0.5 && last.bottom <= shown.bottom + 0.5;
  list.scrollTop = 0;
  await frames();
  const probe = await fetch("/origin-probe", { method: "POST", headers: { "Origin": "https://elsewhere.example" }, body: "{}" });
  result.probeOrigins = await probe.json();
  region.querySelector("[data-set-aside-dismiss]").click();
  await waitFor(() => region.querySelectorAll(".set-aside__group").length === 1, "the dismissed edits to go");
  result.groupsAfter = region.querySelectorAll(".set-aside__group").length;
  result.headingAfter = region.querySelector("h3").textContent;
  result.copy = region.querySelector(".set-aside__edit strong").textContent;
  return result;
})()`,
				Screenshot: shot("task"),
			}, &page)

			if !page.Visible || page.Heading != "3 edits set aside" || page.Groups != 2 {
				t.Fatalf("notice = %+v, want three edits in two sets shown", page)
			}
			if !page.AboveForm {
				t.Errorf("the notice ends below the form's top (%.0f)", page.FormTop)
			}
			if !page.PageFits || page.NoticeLeft < 0 || page.NoticeRight > page.Viewport+0.5 {
				t.Errorf("the notice spans %.0f–%.0f of a %.0f-wide screen, or widens the page", page.NoticeLeft, page.NoticeRight, page.Viewport)
			}
			if !page.TextWraps {
				t.Errorf("the set-aside text is %.0f wide in a %.0f-wide list, want it wrapped within it", page.TextWidth, page.TextClient)
			}
			if limit := float64(size.height) * 0.35; page.ListHeight > limit+1 && page.ListHeight > 16*16+1 {
				t.Errorf("the list of set-aside edits is %.0f tall, want it scrolling within %.0f", page.ListHeight, limit)
			}
			if page.Scrollers != 1 || !page.TextsWhole {
				t.Errorf("%d regions of the notice scroll (want the list alone), and every text drawn whole = %t; text heights %v",
					page.Scrollers, page.TextsWhole, page.TextHeights)
			}
			if !page.LastDismissReachable {
				t.Errorf("scrolled to its end, the list does not show the last Dismiss")
			}
			if page.FormTop > float64(size.height) {
				t.Errorf("the form starts at %.0f, below a %d-tall screen", page.FormTop, size.height)
			}
			if page.SmallestButton < 32 || !page.ButtonsInside {
				t.Errorf("the notice's smallest button is %.0f tall (want 32) or a button leaves the notice", page.SmallestButton)
			}
			if page.GroupsAfter != 1 || page.HeadingAfter != "1 edit set aside" || page.Copy != "bob@client.example" {
				t.Errorf("after dismissing alice's edits the notice reads %q with %d sets, first by %q", page.HeadingAfter, page.GroupsAfter, page.Copy)
			}
			if len(page.ProbeOrigins) != 1 || page.ProbeOrigins[0] == "https://elsewhere.example" {
				t.Errorf("a page asking fetch for another Origin sent %q, want the browser's one", page.ProbeOrigins)
			}
			board.mu.Lock()
			origins := append([]int(nil), board.origins...)
			board.mu.Unlock()
			for _, count := range origins {
				if count != 1 {
					t.Errorf("a request that changes something arrived with %d Origin headers, want one: %v", count, origins)
				}
			}
			t.Logf("%dx%d: notice %.0f–%.0f × %.0f tall, list %.0f showing %.0f of content, texts %v tall, %d scrolling region(s), form top %.0f, smallest button %.0f, text %.0f in list %.0f, origins per write %v, probe %q",
				size.width, size.height, page.NoticeLeft, page.NoticeRight, page.NoticeHeight, page.ListHeight, page.ListScroll, page.TextHeights, page.Scrollers, page.FormTop, page.SmallestButton, page.TextWidth, page.TextClient, origins, page.ProbeOrigins)

			var card struct {
				Flag                            string
				Visible, Inside, PageFits       bool
				FlagHeight, FlagLeft, FlagRight float64
				CardLeft, CardRight             float64
			}
			openInBrowser(t, newSetAsideBoard().handler(t), browserPage{
				Width: size.width, Height: size.height,
				Path: "/",
				Script: `
(async () => {
  for (let turn = 0; turn < 200 && !document.querySelector("[data-set-aside-flag]"); turn++) await new Promise((resolve) => setTimeout(resolve, 25));
  if (!document.querySelector("[data-set-aside-flag]")) return { flag: "" };
  document.querySelector("[data-set-aside-flag]").scrollIntoView({ block: "center" });
  // The first poll may draw the card again; the flag is measured as it
  // stands once that has landed, not as the element first found.
  await new Promise((resolve) => setTimeout(resolve, 1500));
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const flag = document.querySelector("[data-set-aside-flag]");
  flag.scrollIntoView({ block: "center" });
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const box = flag.getBoundingClientRect();
  const cardBox = flag.closest(".task-card").getBoundingClientRect();
  return {
    flag: flag.textContent,
    visible: getComputedStyle(flag).display !== "none" && box.height > 0,
    inside: box.left >= cardBox.left - 0.5 && box.right <= cardBox.right + 0.5,
    pageFits: document.documentElement.scrollWidth <= innerWidth,
    flagHeight: box.height, flagLeft: box.left, flagRight: box.right, cardLeft: cardBox.left, cardRight: cardBox.right
  };
})()`,
				Screenshot: shot("card"),
			}, &card)
			if card.Flag != "3 edits set aside" || !card.Visible || !card.Inside || !card.PageFits {
				t.Errorf("card flag = %+v, want \"3 edits set aside\" shown inside its card", card)
			}
			t.Logf("%dx%d: card flag %q %.0f–%.0f in card %.0f–%.0f, %.0f tall", size.width, size.height, card.Flag, card.FlagLeft, card.FlagRight, card.CardLeft, card.CardRight, card.FlagHeight)
		})
	}
}
