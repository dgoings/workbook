package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// What a reader sees, in a real browser, when a proxy passes their change to
// the board and still tells the page to send it later. The resend names a
// head the first attempt moved, so the board refuses it as stale; the page
// must read that as the save it is, not as "not saved". The Node harness pins
// every reporter's logic (held_change_landed_*_test.go); this pins two of
// them end to end against a board that really records the change and really
// refuses the resend: a comment on the task page and a status added on the
// configuration page, at desktop and phone sizes.
//
// With WORKBOOK_TEST_SCREENSHOT_DIR set, each settled page is photographed.

const landedBrowserTaskID = "WB-01J0000000000000000000HL11"

// landingBrowserProxy passes the first write to the path it guards on to the
// board, discards the board's answer, and answers 429 with a Retry-After and
// an HTML page instead, the way a proxy that times out a slow upstream does.
type landingBrowserProxy struct {
	board  http.Handler
	method string
	path   string

	mutex   sync.Mutex
	held    bool
	reached int
}

func (proxy *landingBrowserProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == proxy.method && request.URL.Path == proxy.path {
		proxy.mutex.Lock()
		proxy.reached++
		first := !proxy.held
		proxy.held = true
		proxy.mutex.Unlock()
		if first {
			proxy.board.ServeHTTP(httptest.NewRecorder(), request)
			writer.Header().Set("Content-Type", "text/html")
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(writer, "<html><head><title>429 Too Many Requests</title></head><body>Too Many Requests</body></html>")
			return
		}
	}
	proxy.board.ServeHTTP(writer, request)
}

// landedCommentBoard is a board holding one task whose comments are really
// recorded, each against the head the request names.
func landedCommentBoard(t *testing.T) (*landingBrowserProxy, func() core.Task) {
	t.Helper()
	task := clientPlacementTask(landedBrowserTaskID, "Commented behind a proxy", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	var mutex sync.Mutex
	current := task
	proxy := &landingBrowserProxy{method: http.MethodPost, path: "/api/tasks/" + landedBrowserTaskID + "/comments"}
	proxy.board = NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			mutex.Lock()
			defer mutex.Unlock()
			return []core.Task{current}, nil
		},
		Update: unexpectedTaskUpdate(t),
		AddComment: func(_ context.Context, _ string, input core.CommentAddInput) (core.MutationResult, error) {
			mutex.Lock()
			defer mutex.Unlock()
			if input.ExpectedHead != current.Head {
				return core.MutationResult{}, core.Errorf(core.CategoryStaleWrite, "task %s changed since %s", current.ID, input.ExpectedHead)
			}
			current.Comments = append(append([]core.Comment{}, current.Comments...), core.Comment{
				ID: fmt.Sprintf("01K0M6B8A4FTT8C39MXXYTWC%02d", len(current.Comments)+1), Author: "reader@example.com",
				Body: input.Body, CreatedAt: time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC),
			})
			current.Head = fmt.Sprintf("head-%d", len(current.Comments)+1)
			return core.MutationResult{Task: current}, nil
		},
	})
	return proxy, func() core.Task {
		mutex.Lock()
		defer mutex.Unlock()
		return current
	}
}

// landedStatusBoard is a project whose statuses are really added, each against
// the configuration head the request names.
func landedStatusBoard(t *testing.T) (*landingBrowserProxy, func() []core.StatusDefinition) {
	t.Helper()
	var mutex sync.Mutex
	vocabulary := handlerVocabulary(t)
	head := "head-1"
	state := func() VocabularyState { return VocabularyState{Vocabulary: vocabulary, Head: head} }
	unexpected := func(name string) (VocabularyMutation, error) {
		t.Errorf("the configuration page sent a %s nobody made", name)
		return VocabularyMutation{}, core.Errorf(core.CategoryValidation, "unexpected %s", name)
	}
	proxy := &landingBrowserProxy{method: http.MethodPost, path: "/api/vocabulary/statuses"}
	proxy.board = NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			mutex.Lock()
			defer mutex.Unlock()
			return state(), nil
		},
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
		AddStatus: func(_ context.Context, addition VocabularyStatusAddition) (VocabularyMutation, error) {
			mutex.Lock()
			defer mutex.Unlock()
			if addition.ExpectedHead != head {
				return VocabularyMutation{}, core.Errorf(core.CategoryStaleWrite, "this project's configuration changed since %s; reload and try again", addition.ExpectedHead)
			}
			definitions := vocabulary.Definitions()
			label := addition.Label
			if label == "" {
				label = "Triage"
			}
			definitions = append(definitions, core.StatusDefinition{
				Status: addition.Status, Label: label, Rank: fmt.Sprintf("%d/1", len(definitions)+1), Tags: []core.StatusTag{},
			})
			document := vocabulary.Document()
			next, err := core.NewVocabulary(definitions, document.Aliases, document.Retired)
			if err != nil {
				return VocabularyMutation{}, err
			}
			vocabulary, head = next, fmt.Sprintf("head-%d", len(definitions))
			return VocabularyMutation{State: state()}, nil
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return unexpected("status edit")
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return unexpected("status removal")
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return unexpected("status reorder")
		},
	})
	return proxy, func() []core.StatusDefinition {
		mutex.Lock()
		defer mutex.Unlock()
		return vocabulary.Definitions()
	}
}

// landedBrowserHelpers waits on the page and measures what it drew.
const landedBrowserHelpers = `
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const until = async (test, what) => {
    for (let index = 0; index < 600; index += 1) { if (test()) return; await sleep(25); }
    throw new Error("the page never " + what);
  };
  const box = (element) => {
    const rect = element.getBoundingClientRect();
    return { top: rect.top, left: rect.left, width: rect.width, height: rect.height };
  };
  const notice = document.querySelector("[data-held-change-notice]");
  const held = async () => {
    await until(() => !notice.hidden && notice.getBoundingClientRect().height > 0, "showed the held-change notice");
    await until(() => notice.hidden, "settled the held change");
    await sleep(300);
  };
`

const landedCommentScript = `(async () => {` + landedBrowserHelpers + `
  await until(() => document.querySelector('[data-task-panel="comments"] textarea'), "drew the comment box");
  const panel = document.querySelector('[data-task-panel="comments"]');
  const area = panel.querySelector("textarea");
  area.scrollIntoView({ block: "center" });
  area.value = "Recorded once, behind a proxy.";
  area.dispatchEvent(new Event("input", { bubbles: true }));
  panel.querySelector("form").requestSubmit();
  await held();
  const status = panel.querySelector("[data-panel-status]");
  const rows = [...panel.querySelector("[data-panel-list]").children];
  const row = rows[rows.length - 1];
  return {
    viewport: { width: innerWidth, height: innerHeight },
    boxValue: area.value,
    statusText: status ? status.textContent.trim() : "",
    comments: rows.map((item) => (item.querySelector("[data-comment-body]") || item).textContent.trim()),
    row: row ? box(row) : null,
    noticeDisplay: getComputedStyle(notice).display,
    scrollWidth: document.documentElement.scrollWidth,
  };
})()`

const landedStatusScript = `(async () => {` + landedBrowserHelpers + `
  await until(() => document.querySelector("#status-new-name"), "drew the add-a-status form");
  const name = document.querySelector("#status-new-name");
  name.scrollIntoView({ block: "center" });
  name.value = "triage";
  name.dispatchEvent(new Event("input", { bubbles: true }));
  name.form.requestSubmit();
  await held();
  const status = document.querySelector("[data-vocabulary-panel-status]");
  const row = document.querySelector('[data-vocabulary-status="triage"]');
  return {
    viewport: { width: innerWidth, height: innerHeight },
    boxValue: document.querySelector("#status-new-name") ? document.querySelector("#status-new-name").value : null,
    statusText: status.textContent.trim(),
    status: box(status),
    row: row ? box(row) : null,
    noticeDisplay: getComputedStyle(notice).display,
    scrollWidth: document.documentElement.scrollWidth,
  };
})()`

type landedBrowserResult struct {
	Viewport      struct{ Width, Height float64 }
	BoxValue      *string
	StatusText    string
	Comments      []string
	Status        *heldBrowserBox
	Row           *heldBrowserBox
	NoticeDisplay string
	ScrollWidth   float64
}

// String is what a run logs: the measured boxes, not pointers to them.
func (result landedBrowserResult) String() string {
	describe := func(at *heldBrowserBox) string {
		if at == nil {
			return "none"
		}
		return fmt.Sprintf("%.0fx%.0f at (%.0f, %.0f)", at.Width, at.Height, at.Left, at.Top)
	}
	field := "none"
	if result.BoxValue != nil {
		field = fmt.Sprintf("%q", *result.BoxValue)
	}
	return fmt.Sprintf("viewport %.0fx%.0f, field %s, status %q at %s, saved row %s, notice display %s, scroll width %.0f",
		result.Viewport.Width, result.Viewport.Height, field, result.StatusText, describe(result.Status),
		describe(result.Row), result.NoticeDisplay, result.ScrollWidth)
}

func TestBrowserSettlesAChangeThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	shots := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	for _, size := range []struct{ width, height int }{{1440, 900}, {390, 844}} {
		t.Run(fmt.Sprintf("a comment at %dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			proxy, current := landedCommentBoard(t)
			var result landedBrowserResult
			openInBrowser(t, proxy, browserPage{
				Width: size.width, Height: size.height,
				Path:       "/tasks/" + landedBrowserTaskID,
				Script:     landedCommentScript,
				Screenshot: landedScreenshot(shots, "comment", size.width, size.height),
			}, &result)
			t.Logf("a comment at %dx%d: %s", size.width, size.height, result)
			landedBrowserCommon(t, proxy, result)
			if result.BoxValue == nil {
				t.Error("the comment box is gone")
			} else if *result.BoxValue != "" {
				t.Errorf("the comment box still holds %q once the comment landed", *result.BoxValue)
			}
			if len(result.Comments) != 1 || result.Comments[0] != "Recorded once, behind a proxy." {
				t.Errorf("the thread draws %q, want the one comment", result.Comments)
			}
			if task := current(); len(task.Comments) != 1 {
				t.Errorf("the board recorded %d comments, want 1", len(task.Comments))
			}
		})
		t.Run(fmt.Sprintf("a status at %dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			proxy, current := landedStatusBoard(t)
			var result landedBrowserResult
			openInBrowser(t, proxy, browserPage{
				Width: size.width, Height: size.height,
				Path:       "/config",
				Script:     landedStatusScript,
				Screenshot: landedScreenshot(shots, "status", size.width, size.height),
			}, &result)
			t.Logf("a status at %dx%d: %s", size.width, size.height, result)
			landedBrowserCommon(t, proxy, result)
			if result.StatusText != "Added triage." {
				t.Errorf("the statuses section says %q, want %q", result.StatusText, "Added triage.")
			}
			if result.BoxValue != nil && *result.BoxValue != "" {
				t.Errorf("the add-a-status form still holds %q once the status landed", *result.BoxValue)
			}
			added := 0
			for _, definition := range current() {
				if definition.Status == "triage" {
					added++
				}
			}
			if added != 1 {
				t.Errorf("the board holds triage %d times, want once", added)
			}
		})
	}
}

func landedScreenshot(dir, name string, width, height int) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("held-change-landed-%s-%dx%d.png", name, width, height))
}

// landedBrowserCommon is what both pages owe the reader once the change is
// settled: nothing says it failed, the notice is down, the saved row is on
// screen, and nothing scrolls sideways.
func landedBrowserCommon(t *testing.T, proxy *landingBrowserProxy, result landedBrowserResult) {
	t.Helper()
	proxy.mutex.Lock()
	reached := proxy.reached
	proxy.mutex.Unlock()
	if reached != 2 {
		t.Errorf("the change reached the board %d times, want 2: the first attempt and the refused resend", reached)
	}
	for _, said := range []string{"not saved", "not applied", "changed elsewhere", "changed in another clone", "may already"} {
		if strings.Contains(strings.ToLower(result.StatusText), said) {
			t.Errorf("the page says %q of a change that landed", result.StatusText)
		}
	}
	if result.NoticeDisplay != "none" {
		t.Errorf("the held-change notice is still drawn (display %q)", result.NoticeDisplay)
	}
	if row := result.Row; row == nil {
		t.Error("the change that landed is not drawn")
	} else if row.Left < 0 || row.Left+row.Width > result.Viewport.Width+0.5 || row.Width <= 0 {
		t.Errorf("the change that landed is drawn at %+v, outside the %vx%v viewport", *row, result.Viewport.Width, result.Viewport.Height)
	}
	if result.ScrollWidth > result.Viewport.Width {
		t.Errorf("the page scrolls sideways: %v wide in a %v viewport", result.ScrollWidth, result.Viewport.Width)
	}
}
