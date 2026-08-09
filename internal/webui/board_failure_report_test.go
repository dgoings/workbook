package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A refused board change used to be reported on the stale banner, which every
// successful poll clears. The board polls once a second, so the report was
// erased inside a second whether or not anyone had looked at it — and on a
// shared board, a conflict nobody reads is a conflict that did not happen.
//
// These tests drive the captured interval callback rather than only asserting
// right after the drain settles, because the poll is what used to destroy the
// report and a test that never ticks it cannot see that.

const (
	failureReportTaskID      = "WB-01J0000000000000000000FA01"
	failureReportBystanderID = "WB-01J0000000000000000000FA02"
	failureReportLiftedID    = "WB-01J0000000000000000000FA03"
	failureReportPollTicks   = 3
)

// A conflict is reported on the card it concerns, on that card alone, and it
// survives every poll after it. Only the reader takes it away, and dismissing
// it leaves the caret on the card rather than on the document body.
func TestHandlerClientKeepsARefusedChangeReportedOnItsCardAcrossPolls(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(failureReportTaskID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	// A second card, untouched throughout: a report that appeared on it would be
	// a board-level banner wearing a card's clothes.
	bystander := clientPlacementTask(failureReportBystanderID, "Untouched", core.StatusReady, core.PriorityLow)
	bystander.Head = "head-b"
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	tasks := []core.Task{moved, bystander}
	refreshedTasks := []core.Task{elsewhere, bystander}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	refreshed := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks: refreshedTasks, Presentation: presentationForTasks(refreshedTasks),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      // The board this clone is holding is already behind, so the refresh the
      // refusal forces reads the version the other writer left.
      taskResponse = ` + string(refreshed) + `;
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
      }) };
    }
    return boardFetch(url, options);
  };

  const dragged = boardCard(` + strconv.Quote(moved.ID) + `);
  dragged.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });

  const reported = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
  if (!reported.includes("changed elsewhere")) {
    throw new Error("the conflict was not reported on the card: " + JSON.stringify(reported));
  }
  if (stale.dataset.visible === "true") {
    throw new Error("the conflict was written to the banner every successful poll clears");
  }
  if (!ready.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the refused move did not roll back");
  }
  if (cardFailureMessage(boardCard(` + strconv.Quote(bystander.ID) + `)) !== "") {
    throw new Error("the report was drawn on a card the refusal had nothing to do with");
  }

  // The poll the report has to survive. It is the same callback the page hands
  // to setInterval, driven here rather than left uncalled.
  for (let tick = 1; tick <= ` + strconv.Itoa(failureReportPollTicks) + `; tick++) {
    await intervalCallback();
    const surviving = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
    if (surviving !== reported) {
      throw new Error("poll " + tick + " changed the report: " + JSON.stringify(surviving));
    }
    if (cardFailureMessage(boardCard(` + strconv.Quote(bystander.ID) + `)) !== "") {
      throw new Error("poll " + tick + " spread the report to an untouched card");
    }
  }
  // The poll really did run and really did redraw the board from the server.
  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  if (!card.textContent.includes("Renamed elsewhere")) {
    throw new Error("the polls never landed, so surviving them proves nothing");
  }

  const dismiss = cardFailureDismiss(card);
  if (!dismiss) throw new Error("the report offered no way to dismiss it");
  dismiss.focus();
  dismiss.eventListeners.click();
  if (cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)) !== "") {
    throw new Error("Dismiss left the report standing");
  }
  if (document.activeElement !== boardCard(` + strconv.Quote(moved.ID) + `)) {
    throw new Error("dismissing the report dropped the caret instead of leaving it on the card");
  }
  await intervalCallback();
  if (cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)) !== "") {
    throw new Error("a dismissed report came back on the next poll");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute persistent card failure report behavior: %v\n%s", err, output)
	}
}

// A report belongs to a card, and the likeliest reason a change is refused is
// also a reason the card is about to go: another clone deleted the task. The
// report moves to the notice rather than leaving with the card, because a
// report nobody can read is the defect this whole change exists to remove.
func TestHandlerClientLiftsARefusedChangeReportToTheNoticeWhenItsCardLeavesTheBoard(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(failureReportLiftedID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	emptied := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}, Presentation: []TaskPresentation{},
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "not-found", message: "no task has that ID" }
      }) };
    }
    return boardFetch(url, options);
  };

  const dragged = boardCard(` + strconv.Quote(moved.ID) + `);
  dragged.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });

  if (!cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)).includes("Task update failed")) {
    throw new Error("the refusal was not reported on the card it concerned");
  }
  if (!notice.hidden) throw new Error("a report with a card to sit on was lifted to the notice anyway");

  // The other clone's deletion arrives, and the card the report was drawn on
  // goes with it.
  taskResponse = ` + string(emptied) + `;
  await intervalCallback();

  if (boardCard(` + strconv.Quote(moved.ID) + `)) throw new Error("the deleted task is still on the board");
  if (notice.hidden || !notice.textContent.includes("Task update failed")) {
    throw new Error("the report left with its card: " + JSON.stringify(notice.textContent));
  }
  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (reports.length !== 1) {
    throw new Error("the lifted report was not written exactly once: " + reports.length);
  }
  // Lifted, not copied: further polls must not restate it.
  await intervalCallback();
  await intervalCallback();
  if (findElements(notice, (element) => hasClassToken(element, "notice__report")).length !== 1) {
    throw new Error("later polls restated the lifted report");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute lifted failure report behavior: %v\n%s", err, output)
	}
}
