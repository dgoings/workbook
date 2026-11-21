package webui

import (
	"context"
	"fmt"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board polls about once a second, and every poll hands the dependency
// picker its candidates again. A reader who has moved the highlight down the
// open list with the arrow keys must find it where they left it after a poll
// that changed nothing, so Enter picks the task they were looking at. Pressed
// as real keys in a real browser, and read after the page has seen two more
// polls answered.
func TestBrowserKeepsThePickerHighlightAcrossPolls(t *testing.T) {
	t.Parallel()
	tasks := []core.Task{}
	for index := 1; index <= 4; index++ {
		tasks = append(tasks, clientPlacementTask(
			fmt.Sprintf("WB-01J0000000000000000000HL%02d", index),
			fmt.Sprintf("Highlight candidate %d", index),
			core.StatusReady, core.PriorityMedium))
	}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	var result struct {
		Before, After, Descendant string
		Polls                     int
		Open                      bool
	}
	openInBrowser(t, handler, browserPage{
		Width: 1280, Height: 900, Path: "/tasks/new",
		Setup: pageGeometryHelpers + `
(async () => {
  window.__polls = 0;
  const realFetch = window.fetch;
  window.fetch = async (url, options = {}) => {
    const response = await realFetch(url, options);
    if ((options.method || "GET") === "GET" && String(url).startsWith("/api/tasks")) window.__polls++;
    return response;
  };
  await waitFor(() => document.querySelector("#dependency-input"), "the dependency picker");
  const input = document.querySelector("#dependency-input");
  input.focus();
  // A press on the box opens the list whether or not the focus event fired.
  input.click();
  await waitFor(() => !document.querySelector("#dependency-listbox").hidden, "the picker's list to open");
})()`,
		Keys: []string{"ArrowDown", "ArrowDown"},
		Script: `
(async () => {
  const active = () => {
    const option = document.querySelector("#dependency-listbox .relationship-option.is-active");
    return option ? option.dataset.candidateId : "";
  };
  const before = active();
  const polls = window.__polls;
  const deadline = Date.now() + 10000;
  // Two polls answered, then a beat for the page to draw what they brought.
  while (window.__polls < polls + 2 && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 50));
  await new Promise((resolve) => setTimeout(resolve, 200));
  const input = document.querySelector("#dependency-input");
  const descendant = input.getAttribute("aria-activedescendant") || "";
  const named = descendant ? document.getElementById(descendant) : null;
  return {
    before,
    after: active(),
    descendant: named ? named.dataset.candidateId : "",
    polls: window.__polls - polls,
    open: !document.querySelector("#dependency-listbox").hidden
  };
})()`,
	}, &result)
	if result.Before != tasks[1].ID {
		t.Fatalf("two ArrowDowns highlighted %q, want the second candidate %q", result.Before, tasks[1].ID)
	}
	if result.Polls < 2 {
		t.Fatalf("the page answered %d polls while the test waited, so this proves nothing", result.Polls)
	}
	if !result.Open {
		t.Fatalf("the picker's list closed during the polls")
	}
	if result.After != result.Before {
		t.Errorf("after %d polls the highlight is on %q, want it still on %q", result.Polls, result.After, result.Before)
	}
	if result.Descendant != result.Before {
		t.Errorf("after %d polls the input's active descendant names %q, want %q", result.Polls, result.Descendant, result.Before)
	}
}
