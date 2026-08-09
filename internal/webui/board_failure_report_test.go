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
	failureReportSettledID   = "WB-01J0000000000000000000FA04"
	failureReportDeletedID   = "WB-01J0000000000000000000FA05"
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
//
// What moves is not what the card said. The card could point at itself; a
// notice cannot, so the lifted report names the task and says the board no
// longer carries it. It also takes the caret, because the card that was holding
// it has just been removed from the document.
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

  // Re-dragging the card is the expected retry, and the other clone's deletion
  // can arrive in the middle of it. The renderer keeps a dragged node attached
  // rather than pulling it out from under the cursor, so this card is still on
  // the board, still showing its report, and the notice must not say the same
  // thing a second time.
  const again = boardCard(` + strconv.Quote(moved.ID) + `);
  again.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: again, dataTransfer });
  taskResponse = ` + string(emptied) + `;
  await intervalCallback();
  const held = boardCard(` + strconv.Quote(moved.ID) + `);
  if (!held) throw new Error("the dragged card was detached out from under the cursor");
  if (!cardFailureMessage(held).includes("Task update failed")) {
    throw new Error("the dragged card stopped reporting: " + JSON.stringify(cardFailureMessage(held)));
  }
  if (!notice.hidden) {
    throw new Error("the report was stated twice, on the dragged card and in the notice: " + JSON.stringify(notice.textContent));
  }
  documentEventListeners.dragend({ target: again });

  // The caret is on the card when the poll after the drag finally takes it.
  boardCard(` + strconv.Quote(moved.ID) + `).focus();
  await intervalCallback();

  if (boardCard(` + strconv.Quote(moved.ID) + `)) throw new Error("the deleted task is still on the board");
  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the report left with its card: " + JSON.stringify(notice.textContent));
  }
  const lifted = reports[0].textContent;
  for (const wanted of [` + strconv.Quote(moved.ID) + `, ` + strconv.Quote(moved.Title) + `, "no longer on the board"]) {
    if (!lifted.includes(wanted)) {
      throw new Error("the lifted report never says " + JSON.stringify(wanted) + ": " + JSON.stringify(lifted));
    }
  }
  // The card's own wording points at a card that no longer exists.
  if (lifted.includes("The card shows")) {
    throw new Error("the lifted report points at the card the board just removed: " + JSON.stringify(lifted));
  }
  const dismiss = findElements(reports[0], (element) => element.tagName === "BUTTON")
    .find((element) => element.textContent === "Dismiss");
  if (!dismiss) throw new Error("the lifted report offered no way to dismiss it");
  if (document.activeElement !== dismiss) {
    throw new Error("the removed card dropped the caret instead of handing it to the report that outlived it");
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

// The likeliest refusal of all is a task another clone deleted, and a
// `stale-write` refusal forces a refresh before it is reported. That refresh is
// what takes the task out of the model, so a report that waited until then to
// look up what it is about would have nothing left to name. It is read first.
func TestHandlerClientNamesALiftedReportWhoseTaskTheForcedRefreshRemoved(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(failureReportDeletedID, "Deleted elsewhere", core.StatusReady, core.PriorityMedium)
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
      // The refusal and the deletion are the same event, so the refresh this
      // refusal forces is the one that empties the board.
      taskResponse = ` + string(emptied) + `;
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
  await intervalCallback();

  if (boardCard(` + strconv.Quote(moved.ID) + `)) throw new Error("the deleted task is still on the board");
  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the report left with its card: " + JSON.stringify(notice.textContent));
  }
  const lifted = reports[0].textContent;
  for (const wanted of [` + strconv.Quote(moved.ID) + `, ` + strconv.Quote(moved.Title) + `, "changed elsewhere"]) {
    if (!lifted.includes(wanted)) {
      throw new Error("the lifted report never says " + JSON.stringify(wanted) + ": " + JSON.stringify(lifted));
    }
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute lifted report naming behavior: %v\n%s", err, output)
	}
}

// A report is about a conflict, and changing the card again is the reader
// answering it. Keeping the report up afterwards would warn about something
// they have already dealt with — and from the moment the retry is queued the
// card is showing an optimistic value, which is the one thing the report claims
// it is not showing.
//
// The next decision clears it, not the next confirmation: an intent already
// queued when the refusal arrived was never an answer to it, which is why
// TestHandlerClientRollsBackAFailedIntentAndLeavesALaterOneStanding still finds
// the report on a card whose later placement succeeded.
func TestHandlerClientClearsARefusedChangeReportWhenTheCardIsChangedAgain(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(failureReportSettledID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	accepted := elsewhere
	accepted.Status = core.StatusInProgress
	accepted.Head = "head-3"
	tasks := []core.Task{moved}
	refreshedTasks := []core.Task{elsewhere}
	settledTasks := []core.Task{accepted}
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
	settled := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks: settledTasks, Presentation: presentationForTasks(settledTasks),
	})
	confirmed := mustJSON(t, accepted)

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  let refuse = true;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (refuse) {
        taskResponse = ` + string(refreshed) + `;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
        }) };
      }
      taskResponse = ` + string(settled) + `;
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(confirmed) + `
      }) };
    }
    return boardFetch(url, options);
  };

  let dragged = boardCard(` + strconv.Quote(moved.ID) + `);
  dragged.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });
  if (!cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)).includes("changed elsewhere")) {
    throw new Error("the refusal was not reported on the card it concerned");
  }

  // The natural next action: drag it again — the reader's answer to the
  // refusal — and this time the server takes it.
  refuse = false;
  dragged = boardCard(` + strconv.Quote(moved.ID) + `);
  dragged.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });

  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  if (!card || card.parentElement.dataset.status !== "in-progress") {
    throw new Error("the retry was not applied, so clearing the report would prove nothing");
  }
  if (cardFailureMessage(card) !== "") {
    throw new Error("a resolved conflict is still reported: " + JSON.stringify(cardFailureMessage(card)));
  }
  if (!notice.hidden) {
    throw new Error("the cleared report was lifted to the notice: " + JSON.stringify(notice.textContent));
  }
  await intervalCallback();
  if (cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)) !== "") {
    throw new Error("the next poll brought the settled report back");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute settled failure report behavior: %v\n%s", err, output)
	}
}
