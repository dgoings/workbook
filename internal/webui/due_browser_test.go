package webui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The due marks in a real browser, at the two desktop sizes the board is laid
// out for and at every phone size: the cards' lines are drawn inside their
// cards with the word in them and a color that differs from an undated
// card's, and the task page's date field is on the page, reachable, not
// covered, large enough to touch on a phone, and saves what is chosen in it.

var dueBrowserSizes = []struct {
	width, height int
	phone         bool
}{{1440, 900, false}, {1280, 800, false}, {360, 740, true}, {390, 844, true}, {430, 932, true}}

type dueCardReading struct {
	Lines []struct {
		ID, State, Text, Color, Background string
		Width, Height, FontSize            float64
		Inside, Visible                    bool
	}
	Sideways float64
	Errors   []string
}

const dueCardScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector(".task-card .task-card__due"), "a card's due line");
  await settle();
  const lines = [];
  for (const line of document.querySelectorAll(".task-card__due")) {
    // Every card's line is read for what it says, including the cards of a
    // column a phone is not showing; only a line on screen is measured.
    const rect = line.getBoundingClientRect();
    const card = line.closest(".task-card").getBoundingClientRect();
    const style = getComputedStyle(line);
    lines.push({
      ID: line.closest(".task-card").dataset.taskId, State: line.dataset.due, Text: line.textContent,
      Color: style.color, Background: style.backgroundColor,
      Width: rect.width, Height: rect.height, FontSize: parseFloat(style.fontSize),
      Inside: rect.left >= card.left - 0.5 && rect.right <= card.right + 0.5 && rect.top >= card.top - 0.5 && rect.bottom <= card.bottom + 0.5,
      Visible: rect.width > 0 && rect.height > 0,
    });
  }
  return { Lines: lines, Sideways: sideways(), Errors: window.__errors };
})()
`

type dueTaskPageReading struct {
	Width, Height, FontSize float64
	Reachable, Focused      bool
	Mark, MarkState         string
	MarkWidth, MarkHeight   float64
	Saved                   bool
	Sideways                float64
	Errors                  []string
}

const dueTaskPageScript = phoneHelpers + `
(async () => {
  await waitFor(() => document.querySelector("#task-due"), "the date field");
  await settle();
  const field = document.querySelector("#task-due");
  field.scrollIntoView({ block: "center", inline: "nearest" });
  await settle();
  const rect = field.getBoundingClientRect();
  const atCenter = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
  field.focus();
  const focused = document.activeElement === field;
  const fontSize = parseFloat(getComputedStyle(field).fontSize);
  const mark = document.querySelector("#task-due-state");
  const markRect = mark.getBoundingClientRect();
  const markText = mark.hidden ? "" : mark.textContent;
  const markState = mark.dataset.dueState;
  field.value = "2031-03-04";
  field.dispatchEvent(new Event("input", { bubbles: true }));
  field.dispatchEvent(new Event("change", { bubbles: true }));
  document.querySelector(".save-button").click();
  let saved = false;
  try { await waitFor(() => window.__dueSaved, "the save"); saved = true; } catch (error) {}
  return {
    Width: rect.width, Height: rect.height, FontSize: fontSize,
    Reachable: atCenter === field || field.contains(atCenter), Focused: focused,
    Mark: markText, MarkState: markState,
    MarkWidth: markRect.width, MarkHeight: markRect.height,
    Saved: saved, Sideways: sideways(), Errors: window.__errors,
  };
})()
`

// dueSaveWatch marks the page once a PATCH has answered, so the script can
// wait for the save rather than for a guess at how long one takes.
const dueSaveWatch = phoneWatch + `
const dueFetch = window.fetch;
window.fetch = async (...args) => {
  const response = await dueFetch(...args);
  const options = args[1] || {};
  if (options.method === "PATCH") window.__dueSaved = true;
  return response;
};
`

func dueShot(name string, width, height int) string {
	directory := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	if directory == "" {
		return ""
	}
	return filepath.Join(directory, fmt.Sprintf("due-%s-%dx%d.png", name, width, height))
}

func TestBrowserDueMarksAreDrawnInWordsInsideTheirCards(t *testing.T) {
	t.Parallel()
	for _, size := range dueBrowserSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			tasks := dueTasks()
			handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
			var reading dueCardReading
			openInBrowser(t, handler, browserPage{
				Width: size.width, Height: size.height, Phone: size.phone, Path: "/",
				BeforeLoad: phoneWatch, Script: dueCardScript, Screenshot: dueShot("board", size.width, size.height),
			}, &reading)
			if len(reading.Errors) > 0 {
				t.Fatalf("the page threw %v", reading.Errors)
			}
			if reading.Sideways > 0 {
				t.Errorf("the board scrolls sideways by %.1fpx", reading.Sideways)
			}
			// What each card says is judged from its date and the browser's
			// today, whatever the server's flags claimed: two of the tasks
			// carry flags that contradict their dates.
			wantStates := map[string]string{
				dueOverdueID: "overdue", dueSoonID: "due-soon", dueEdgeID: "due-soon",
				dueLaterID: "", dueDoneID: "", dueClaimedOverdueID: "", dueClaimedNoneID: "overdue",
			}
			colors := map[string]string{}
			for _, line := range reading.Lines {
				want, known := wantStates[line.ID]
				if !known {
					t.Errorf("an unexpected card %s draws a due line %q", line.ID, line.Text)
				}
				delete(wantStates, line.ID)
				word := map[string]string{"overdue": "Overdue ", "due-soon": "Due soon ", "": "Due "}[want]
				if line.State != want || !strings.HasPrefix(line.Text, word) {
					t.Errorf("%s's due line reads %q (state %q), want state %q and text starting %q", line.ID, line.Text, line.State, want, word)
				}
				if !line.Visible {
					continue
				}
				t.Logf("%s %q: %.1fx%.1f at %.1fpx, %s on %s", line.ID, line.Text, line.Width, line.Height, line.FontSize, line.Color, line.Background)
				if !line.Inside {
					t.Errorf("%s's due line reaches outside its card", line.ID)
				}
				// The size of the card's other notice, the set-aside flag
				// (.72rem), and above its labels' (.64rem).
				if line.FontSize < 11.49 {
					t.Errorf("%s's due line is set in %.1fpx, smaller than the card's set-aside notice", line.ID, line.FontSize)
				}
				colors[line.State] = line.Color + " on " + line.Background
			}
			if len(wantStates) > 0 {
				t.Errorf("no due line drawn for %v", wantStates)
			}
			// The phone shows one column; the board opens on one holding an
			// overdue and a due-soon task, so both marks are there to compare.
			if colors["overdue"] == "" || colors["due-soon"] == "" {
				t.Fatalf("lines drawn = %+v, want an overdue and a due-soon one on screen", reading.Lines)
			}
			if plain, ok := colors[""]; ok && (plain == colors["overdue"] || plain == colors["due-soon"]) {
				t.Errorf("a marked line is colored like a plain one: %v", colors)
			}
			if colors["overdue"] == colors["due-soon"] {
				t.Errorf("overdue and due soon are colored alike: %v", colors)
			}
		})
	}
}

func TestBrowserTaskPageDateFieldIsReachableAndSaves(t *testing.T) {
	t.Parallel()
	for _, size := range dueBrowserSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			tasks := dueTasks()
			var mutex sync.Mutex
			var saved []string
			handler := NewHandler(Options{
				List:   func(context.Context) ([]core.Task, error) { return tasks, nil },
				Create: unexpectedTaskCreate(t),
				Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
					mutex.Lock()
					defer mutex.Unlock()
					if input.Due != nil {
						saved = append(saved, *input.Due)
					}
					return core.MutationResult{Task: tasks[0]}, nil
				},
				UpdateStatus: unexpectedStatusUpdate(t),
			})
			var reading dueTaskPageReading
			openInBrowser(t, handler, browserPage{
				Width: size.width, Height: size.height, Phone: size.phone, Path: "/tasks/" + dueOverdueID,
				BeforeLoad: dueSaveWatch, Script: dueTaskPageScript, Screenshot: dueShot("task", size.width, size.height),
			}, &reading)
			t.Logf("date field %.1fx%.1f at %.1fpx; mark %q %.1fx%.1f", reading.Width, reading.Height, reading.FontSize, reading.Mark, reading.MarkWidth, reading.MarkHeight)
			if len(reading.Errors) > 0 {
				t.Fatalf("the page threw %v", reading.Errors)
			}
			if !reading.Reachable || !reading.Focused {
				t.Errorf("the date field is reachable %t, takes focus %t", reading.Reachable, reading.Focused)
			}
			if reading.Mark != "Overdue" || reading.MarkState != "overdue" || reading.MarkHeight == 0 {
				t.Errorf("the date's state reads %q (%q), %.1fpx tall", reading.Mark, reading.MarkState, reading.MarkHeight)
			}
			if size.phone && (reading.Height < 43.99 || reading.FontSize < 15.99) {
				t.Errorf("on a phone the date field is %.1fpx tall in %.1fpx text, want at least 44 and 16", reading.Height, reading.FontSize)
			}
			if reading.Sideways > 0 {
				t.Errorf("the task page scrolls sideways by %.1fpx", reading.Sideways)
			}
			mutex.Lock()
			defer mutex.Unlock()
			if !reading.Saved || len(saved) != 1 || saved[0] != "2031-03-04" {
				t.Errorf("the save sent due %v (page saw it land: %t), want 2031-03-04", saved, reading.Saved)
			}
		})
	}
}

// dueMidnightWatch runs before the page's own script: it holds the page's
// clock, so the test can carry the reader's date past local midnight, and it
// records how each poll of the board was answered.
const dueMidnightWatch = phoneWatch + `
(() => {
  const RealDate = Date;
  let shift = 0;
  window.Date = class extends RealDate {
    constructor(...args) {
      if (args.length) super(...args);
      else { super(); this.setTime(this.getTime() + shift); }
    }
    static now() { return RealDate.now() + shift; }
  };
  window.__runPastMidnight = () => {
    const now = new window.Date();
    const next = new RealDate(now.getFullYear(), now.getMonth(), now.getDate() + 1, 0, 1);
    shift += next.getTime() - now.getTime();
  };
  window.__polls = [];
  const pollFetch = window.fetch;
  window.fetch = async (...args) => {
    const response = await pollFetch(...args);
    const options = args[1] || {};
    if ((options.method || "GET") === "GET" && String(args[0]) === "/api/tasks") window.__polls.push(response.status);
    return response;
  };
})();
`

type dueMidnightReading struct {
	Before, After       string
	PollsAfter          []int
	PollsBeforeMidnight int
	Errors              []string
}

// dueMidnightScript reads one mark before and after the page's clock passes
// midnight. read names the mark; the page is left alone for three ticks after
// the clock moves, and every poll in that time must be a 304, so a redraw can
// only have come from the page's own judgment.
func dueMidnightScript(read string) string {
	return phoneHelpers + `
(async () => {
  const read = () => { ` + read + ` };
  await waitFor(() => read(), "the due mark");
  await waitFor(() => window.__polls.includes(304), "a poll answered 304");
  await settle();
  const before = read();
  const pollsBefore = window.__polls.length;
  window.__runPastMidnight();
  await pause(3200);
  await settle();
  return { Before: before, After: read(), PollsAfter: window.__polls.slice(pollsBefore), PollsBeforeMidnight: pollsBefore, Errors: window.__errors };
})()
`
}

// A board, and a task's page, left open across the reader's midnight redraw
// their due marks on the next poll tick, although the server's document has
// not changed and every poll is answered 304: a task due today, due soon until
// midnight, is overdue after it.
func TestBrowserDueMarksAreJudgedAgainAtTheReadersMidnight(t *testing.T) {
	t.Parallel()
	today := dueTasks()[1]
	pages := []struct {
		name, path, read string
		before, after    string
	}{
		{"board", "/", `const card = document.querySelector('.task-card[data-task-id="` + today.ID + `"] .task-card__due'); return card ? card.dataset.due + " " + card.textContent : "";`,
			"due-soon Due soon " + today.Due, "overdue Overdue " + today.Due},
		{"task", "/tasks/" + today.ID, `const mark = document.querySelector("#task-due-state"); return mark ? (mark.hidden ? "hidden" : mark.dataset.dueState + " " + mark.textContent) : "";`,
			"due-soon Due soon", "overdue Overdue"},
	}
	for _, page := range pages {
		for _, size := range []struct {
			width, height int
			phone         bool
		}{{1440, 900, false}, {390, 844, true}} {
			t.Run(fmt.Sprintf("%s/%dx%d", page.name, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				tasks := dueTasks()
				handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
				var reading dueMidnightReading
				openInBrowser(t, handler, browserPage{
					Width: size.width, Height: size.height, Phone: size.phone, Path: page.path,
					BeforeLoad: dueMidnightWatch, Script: dueMidnightScript(page.read),
				}, &reading)
				t.Logf("before %q, after %q; polls after the clock moved %v", reading.Before, reading.After, reading.PollsAfter)
				if len(reading.Errors) > 0 {
					t.Fatalf("the page threw %v", reading.Errors)
				}
				if reading.Before != page.before {
					t.Fatalf("before midnight the mark reads %q, want %q", reading.Before, page.before)
				}
				if len(reading.PollsAfter) == 0 {
					t.Fatal("the page did not poll after the clock moved")
				}
				for _, status := range reading.PollsAfter {
					if status != 304 {
						t.Fatalf("the polls after the clock moved were answered %v, want only 304", reading.PollsAfter)
					}
				}
				if reading.After != page.after {
					t.Errorf("after midnight the mark reads %q, want %q", reading.After, page.after)
				}
			})
		}
	}
}
