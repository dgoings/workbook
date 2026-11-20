package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// A row whose save failed keeps its title the card's width. A create that
// could not record a dependency or an assignment hands those to the new
// task's page as failed rows, each saying "Not saved", why, and offering
// Retry. Those used to sit beside the × on the title's line, and the title was
// left the narrow column between them: the reason wrapped a word to a line
// and a long title ran to five. Now the × alone is on the title's line, and the
// state, the reason and Retry are on lines of their own under it, across the
// card.
//
// Measured in a real browser on a page reached the way a reader reaches it:
// a New Task form with a dependency and an assignment staged, created on a
// board that refuses both follow-up writes. With WORKBOOK_TEST_SCREENSHOT_DIR
// set, the sidebar is also photographed in the light and dark palettes.
func TestBrowserLaysAFailedRowsStateOutUnderItsTitle(t *testing.T) {
	t.Parallel()
	shots := os.Getenv("WORKBOOK_TEST_SCREENSHOT_DIR")
	for _, scheme := range []string{"light", "dark"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			board := newFocusBoard(false, false)
			board.refuseLinks = true
			screenshot := ""
			if shots != "" {
				screenshot = filepath.Join(shots, "failed-rows-"+scheme+".png")
			}
			var rows map[string]struct {
				TitleShare, FailureShare      float64
				RemoveOnTitleLine, StateBelow bool
				State                         string
			}
			openInBrowser(t, board.handler(t), browserPage{
				Width: 1280, Height: 900,
				Path: "/tasks/new",
				Setup: `
(async () => {
` + sidebarFocusHelpers + `
  document.documentElement.dataset.scheme = "` + scheme + `";
  await waitFor(() => sidebar() && document.querySelector("#dependency-input") && document.querySelector("#assignment-to"), "the New Task form");
  document.querySelector("#task-title").value = "A task whose links were refused";
  await stageDependency("depends-on", "` + focusBlockedTitle + `");
  await stageAssignment("sam@example.com/impl-9");
  document.querySelector("form.task-layout").requestSubmit();
})()`,
				Script: `
(async () => {
` + sidebarFocusHelpers + `
  await waitFor(() => location.pathname === "/tasks/` + focusCreatedID + `" &&
    document.querySelector("[data-relationship-row] .relationship-retry") &&
    document.querySelector('[data-task-panel="assignments"] [data-assignment-draft] button:not([data-row-remove])'),
    "the created task's page with its failed rows");
  await frames();
  const inner = (card) => {
    const style = getComputedStyle(card);
    return card.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
  };
  // The state line is found through its Retry, so a layout that put Retry
  // anywhere else is measured where it put it.
  const measure = (card, title, retry) => {
    const failure = retry.parentElement;
    const remove = card.querySelector("[data-row-remove]");
    const titleBox = title.getBoundingClientRect();
    const failureBox = failure.getBoundingClientRect();
    const removeBox = remove.getBoundingClientRect();
    return {
      titleShare: titleBox.width / inner(card),
      failureShare: failureBox.width / inner(card),
      removeOnTitleLine: removeBox.top < titleBox.bottom && removeBox.bottom > titleBox.top,
      stateBelow: failureBox.top >= titleBox.bottom - 0.5,
      state: failure.textContent
    };
  };
  const dependency = document.querySelector("[data-relationship-row][data-relationship-draft]");
  const assignment = document.querySelector('[data-task-panel="assignments"] [data-assignment-draft]');
  assignment.scrollIntoView({ block: "center" });
  await frames();
  return {
    dependency: measure(dependency, dependency.querySelector(".relationship-row__text"), dependency.querySelector(".relationship-retry")),
    assignment: measure(assignment, assignment.querySelector(".assignment__who"), assignment.querySelector("button:not([data-row-remove])"))
  };
})()`,
				Screenshot: screenshot,
			}, &rows)
			for _, name := range []string{"dependency", "assignment"} {
				row, found := rows[name]
				if !found {
					t.Fatalf("the page measured no failed %s row", name)
				}
				if row.TitleShare < 0.8 {
					t.Errorf("a failed %s row's title takes %.0f%% of its card's width, want at least 80%%", name, row.TitleShare*100)
				}
				if row.FailureShare < 0.95 {
					t.Errorf("a failed %s row's state takes %.0f%% of its card's width, want the whole line", name, row.FailureShare*100)
				}
				if !row.RemoveOnTitleLine {
					t.Errorf("a failed %s row's × is not on its title's line", name)
				}
				if !row.StateBelow {
					t.Errorf("a failed %s row's state %q is not under its title", name, row.State)
				}
			}
		})
	}
}
