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
		Inside                             bool
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
    const rect = line.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) continue;
    const card = line.closest(".task-card").getBoundingClientRect();
    const style = getComputedStyle(line);
    lines.push({
      ID: line.closest(".task-card").dataset.taskId, State: line.dataset.due, Text: line.textContent,
      Color: style.color, Background: style.backgroundColor,
      Width: rect.width, Height: rect.height, FontSize: parseFloat(style.fontSize),
      Inside: rect.left >= card.left - 0.5 && rect.right <= card.right + 0.5 && rect.top >= card.top - 0.5 && rect.bottom <= card.bottom + 0.5,
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
			colors := map[string]string{}
			for _, line := range reading.Lines {
				t.Logf("%s %q: %.1fx%.1f at %.1fpx, %s on %s", line.ID, line.Text, line.Width, line.Height, line.FontSize, line.Color, line.Background)
				if !line.Inside {
					t.Errorf("%s's due line reaches outside its card", line.ID)
				}
				word := map[string]string{"overdue": "Overdue ", "due-soon": "Due soon ", "": "Due "}[line.State]
				if !strings.HasPrefix(line.Text, word) {
					t.Errorf("%s's due line reads %q, want it to start %q", line.ID, line.Text, word)
				}
				// The size of the card's other notice, the set-aside flag
				// (.72rem), and above its labels' (.64rem).
				if line.FontSize < 11.49 {
					t.Errorf("%s's due line is set in %.1fpx, smaller than the card's set-aside notice", line.ID, line.FontSize)
				}
				colors[line.State] = line.Color + " on " + line.Background
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
