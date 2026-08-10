package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A refused write forces a refresh and re-bases on what it reads. These tests
// drive the case where that refresh does not land — an offline blip, a server
// restart, a `workbook serve` reloading — which leaves the model at the last
// successful poll, holding the very head the server has just refused.
//
// Both callers used to trust it anyway, so the "re-based" head was the refused
// one and the retry it invited was refused identically. The write path is kept
// answering in both tests while the read path fails, because that is what
// separates "the server is gone" from "the server refused this write and the
// board cannot see what it holds instead".

// The third test is the other side of that line: a refresh whose read of the
// board landed and whose *relationship* context was then overtaken. It answers
// "superseded" like a refresh that never read anything, and treating the two
// alike calls a board that is showing the server's version an outage.

const (
	refusedRefreshBoardTaskID  = "WB-01J0000000000000000000RR01"
	refusedRefreshDetailTaskID = "WB-01J0000000000000000000RR02"
	refusedRefreshSupersededID = "WB-01J0000000000000000000RR03"
)

// A stale write whose forced refresh fails leaves the queue holding a head the
// server has already refused. The intents behind the conflict must not be sent
// against it — they would be refused identically, which is the failure the
// re-base exists to prevent — and must not be sent unguarded either, which
// would overwrite the change that caused the refusal. They are refused where
// they stand, the report says the board could not be read, and the queue is
// working again as soon as a poll gets through.
func TestHandlerClientStaleWriteDoesNotRebaseTheQueueOntoAFailedRefresh(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(refusedRefreshBoardTaskID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	confirmed := elsewhere
	confirmed.Status = core.StatusInProgress
	confirmed.Head = "head-3"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	refreshed := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks: []core.Task{elsewhere}, Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const done = boardLists.find((list) => list.dataset.status === "done");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  const heads = [];
  // Writes are answered and reads are not, from the moment the conflict is
  // reported: the refresh the refusal forces is exactly the request that fails.
  let refreshesFail = false;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      const body = JSON.parse(options.body);
      heads.push(body.expectedHead);
      await new Promise((resolve) => setTimeout(resolve, 0));
      // The server holds head-2, so anything sent against the head this board
      // is holding is refused — the intent behind the conflict included, if
      // the queue ever sends it.
      if (body.expectedHead !== "head-2") {
        refreshesFail = true;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since " + body.expectedHead + "; reload and try again" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    if (refreshesFail) {
      fetchCalls.push({ url, options });
      throw new TypeError("fetch failed");
    }
    return boardFetch(url, options);
  };

  const first = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  first.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: first, dataTransfer });
  const firstDrop = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });

  const second = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  second.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: second, dataTransfer });
  const secondDrop = documentEventListeners.drop({ target: done, clientY: 1, dataTransfer, preventDefault() {} });

  await Promise.all([firstDrop, secondDrop]);
  if (heads.length !== 1 || heads[0] !== "head-1") {
    throw new Error("an intent was sent against a head the server had already refused: " + JSON.stringify(heads));
  }
  const firstMutation = fetchCalls.findIndex((call) => (call.options.method || "GET") !== "GET");
  const refreshedAfterConflict = fetchCalls.slice(firstMutation + 1).some((call) =>
    (call.options.method || "GET") === "GET" && call.url === "/api/tasks");
  if (!refreshedAfterConflict) {
    throw new Error("the stale write did not force a refresh");
  }
  if (inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the conflicted intent did not roll back");
  }
  if (done.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("an intent the queue could not send was left standing on the board");
  }
  if (!ready.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the card did not fall back to the last version the board read");
  }
  const reported = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
  if (!reported.includes("changed elsewhere")) {
    throw new Error("the conflict was not reported as such on the card: " + JSON.stringify(reported));
  }
  if (!reported.includes("could not be refreshed")) {
    throw new Error("the card claimed to show a version the board never read: " + JSON.stringify(reported));
  }
  // The banner keeps its own single writer, and a refresh that failed is
  // exactly that writer. The conflict is on the card; the outage is here.
  if (stale.dataset.visible !== "true") {
    throw new Error("the failed refresh did not raise the stale banner");
  }

  // The blip ends. The poll reads the version the forced refresh could not,
  // and the reader's next change is sent against that head rather than against
  // the one the server refused.
  refreshesFail = false;
  taskResponse = ` + string(refreshed) + `;
  await intervalCallback();
  const recovered = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  if (!recovered || !recovered.textContent.includes("Renamed elsewhere")) {
    throw new Error("the board never converged on the version it could not read");
  }
  recovered.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: recovered, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  if (heads.length !== 2 || heads[1] !== "head-2") {
    throw new Error("the change made after the outage was not sent against the recovered head: " + JSON.stringify(heads));
  }
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the change made after the outage did not land");
  }
  if (cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `)) !== "") {
    throw new Error("the reader's next change left the outage report standing");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute stale-write behavior when the forced refresh fails: %v\n%s", err, output)
	}
}

// A refused save whose forced refresh fails must not move the head the form
// proposes: the only head it could move to is the one the last poll read, which
// is not the version the server refused this save for. Nor may it invite the
// retry, because that retry is refused identically. Once a refresh lands, the
// form re-bases and the invitation comes back with it.
//
// The poll before the save is what makes the difference observable: it leaves
// the model holding head-2 while the form still proposes head-1 and the server
// has moved on to head-3, so a form that answered from the model rather than
// from the refresh sends a head of its own invention.
func TestHandlerClientDetailFormDoesNotRebaseARefusedSaveOntoAFailedRefresh(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(refusedRefreshDetailTaskID, "Detail task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "Original."
	renamed := task
	renamed.Title = "Renamed elsewhere"
	renamed.Head = "head-2"
	reprioritized := renamed
	reprioritized.Priority = core.PriorityHigh
	reprioritized.Head = "head-3"
	saved := reprioritized
	saved.Description = "Rewritten while stale."
	saved.Head = "head-4"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	documentJSON := func(held core.Task) string {
		t.Helper()
		return string(mustJSON(t, TasksDocument{
			Format: "workbook.tasks", Version: 1,
			Tasks: []core.Task{held}, Presentation: presentationForTasks([]core.Task{held}),
		}))
	}

	program := clientDOMHarness("/tasks/"+task.ID, documentJSON(task)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const boardFetch = globalThis.fetch;
  const bodies = [];
  let refreshesFail = false;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      const body = JSON.parse(options.body);
      bodies.push(body);
      await new Promise((resolve) => setTimeout(resolve, 0));
      if (body.expectedHead !== "head-3") {
        // The refresh the first refusal forces cannot reach the server. By the
        // second the board is reachable again and reads what the server holds.
        refreshesFail = bodies.length === 1;
        if (bodies.length === 2) taskResponse = ` + documentJSON(reprioritized) + `;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since " + body.expectedHead + "; reload and try again" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, saved)) + `
      }) };
    }
    if (refreshesFail) {
      fetchCalls.push({ url, options });
      throw new TypeError("fetch failed");
    }
    return boardFetch(url, options);
  };

  // A poll lands while the form is open, so the model holds a version newer
  // than the one this form rendered — and still older than the server's.
  taskResponse = ` + documentJSON(renamed) + `;
  await intervalCallback();

  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("the detail form did not render");
  const result = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!result) throw new Error("the detail form has nowhere to report a refusal");
  description.value = "Rewritten while stale.";
  const form = findElement(main, (element) => element.tagName === "FORM");

  await form.eventListeners.submit({ preventDefault() {} });
  if (JSON.stringify(bodies[0]) !== '{"description":"Rewritten while stale.","expectedHead":"head-1"}') {
    throw new Error("the stale save did not carry the rendered head: " + JSON.stringify(bodies[0]));
  }
  if (!result.textContent.includes("changed elsewhere")) {
    throw new Error("the refusal was not reported in the form: " + JSON.stringify(result.textContent));
  }
  if (!result.textContent.includes("could not be loaded")) {
    throw new Error("the form did not say the version to save against was never read: " + JSON.stringify(result.textContent));
  }
  if (result.textContent.includes("save again to apply them to the version the server holds")) {
    throw new Error("the form invited a retry no refresh had re-based: " + JSON.stringify(result.textContent));
  }
  if (description.value !== "Rewritten while stale.") {
    throw new Error("the refusal discarded the user's edits");
  }
  if (historyPaths.length !== 0) {
    throw new Error("a refused save navigated away");
  }
  const save = findElement(main, (element) => element.tagName === "BUTTON" && element.textContent === "Save");
  if (save.disabled) throw new Error("the refusal left Save disabled");

  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 2) {
    throw new Error("the second save did not reach the server; sent " + bodies.length + " mutations");
  }
  if (bodies[1].expectedHead !== "head-1") {
    throw new Error("the form re-based onto a head no refresh confirmed: " + JSON.stringify(bodies[1]));
  }
  if (!result.textContent.includes("save again to apply them to the version the server holds")) {
    throw new Error("the refusal whose refresh landed did not invite the retry: " + JSON.stringify(result.textContent));
  }

  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 3 || bodies[2].expectedHead !== "head-3") {
    throw new Error("the save after the board caught up did not carry the refreshed head: " + JSON.stringify(bodies));
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("the accepted save did not return to the board");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute refused save behavior when the forced refresh fails: %v\n%s", err, output)
	}
}

// A forced refresh reads the board, replaces the model with it and clears the
// stale banner, and *then* refreshes the relationship context of whatever
// detail route is open. When that second half is overtaken — an ordinary route
// render is enough, because rendering one drops the controller the refresh is
// holding — the whole refresh answers "superseded", the same word a refresh
// that never reached the server answers with.
//
// The queue must not read that as an outage. The board it is looking at is the
// server's, the banner above it says so, and the head it holds is the one the
// refused write needs. Dropping the intents behind the conflict there would
// re-open the bug this whole change closes, and report an outage over a board
// that is current.
//
// The route change is driven where the reviewer found it: while the relationship
// half of the forced refresh is waiting on its deleted-task read.
func TestHandlerClientStaleWriteRebasesWhenOnlyTheRelationshipContextIsSuperseded(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(refusedRefreshSupersededID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	confirmed := elsewhere
	confirmed.Status = core.StatusDone
	confirmed.Head = "head-3"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	refreshed := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks: []core.Task{elsewhere}, Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const done = boardLists.find((list) => list.dataset.status === "done");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const linkTo = (path) => {
    const link = new TestElement("a");
    link.href = path;
    return {
      target: link, button: 0, defaultPrevented: false,
      metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
      preventDefault() {}
    };
  };
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

  const boardFetch = globalThis.fetch;
  const heads = [];
  let releaseMutation = null;
  let releaseDeletedRead = null;
  // Held only for the read the *forced* refresh makes. The one the detail route
  // makes when it opens has to land, or the route change below would be
  // superseding a refresh that was never waiting on anything.
  let holdDeletedRead = false;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      const body = JSON.parse(options.body);
      heads.push(body.expectedHead);
      if (heads.length === 1) {
        return new Promise((resolve) => {
          releaseMutation = () => {
            // The server has moved to head-2, and the refresh the refusal
            // forces is going to read exactly that.
            taskResponse = ` + string(refreshed) + `;
            resolve({ ok: false, json: async () => ({
              format: "workbook.error", version: 1,
              error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
            }) });
          };
        });
      }
      await settle();
      if (body.expectedHead !== "head-2") {
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "task has changed since " + body.expectedHead + "; reload and try again" }
        }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, confirmed)) + `
      }) };
    }
    if (url === "/api/tasks?deleted=true" && holdDeletedRead) {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseDeletedRead = () => resolve({ ok: true, json: async () => deletedTaskResponse });
      });
    }
    return boardFetch(url, options);
  };

  const first = ready.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  first.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: first, dataTransfer });
  const firstDrop = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await settle();
  if (!releaseMutation) throw new Error("the first intent never reached the server");

  const second = inProgress.querySelectorAll(".task-card").find((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `);
  second.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: second, dataTransfer });
  const secondDrop = documentEventListeners.drop({ target: done, clientY: 1, dataTransfer, preventDefault() {} });

  // The reader opens the task's page while that write is still in flight, which
  // is what gives the forced refresh a relationship context to refresh at all.
  await documentEventListeners.click(linkTo("/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `)));
  await settle();

  holdDeletedRead = true;
  releaseMutation();
  await settle();
  if (!releaseDeletedRead) {
    throw new Error("the forced refresh never reached the relationship half a route change could supersede");
  }
  // The board read has already landed by now: the model and the banner are the
  // server's, and only the relationship half is still waiting.
  if (stale.dataset.visible === "true") {
    throw new Error("the forced refresh did not read the board before its relationship half waited");
  }

  // Back to the board. renderRoute drops the controller the in-flight
  // relationship refresh is holding, so that refresh — and with it the whole
  // forced refresh — answers "superseded".
  await documentEventListeners.click(linkTo("/"));
  releaseDeletedRead();
  await Promise.all([firstDrop, secondDrop]);

  if (heads.length !== 2 || heads[1] !== "head-2") {
    throw new Error("the queue did not re-base on a refresh that had read the board: " + JSON.stringify(heads));
  }
  if (!done.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(moved.ID) + `)) {
    throw new Error("the intent behind the conflict did not land against the refreshed head");
  }
  const reported = cardFailureMessage(boardCard(` + strconv.Quote(moved.ID) + `));
  if (!reported.includes("changed elsewhere")) {
    throw new Error("the conflict was not reported as such on the card: " + JSON.stringify(reported));
  }
  if (reported.includes("could not be refreshed")) {
    throw new Error("an outage was reported over a board showing the server's version: " + JSON.stringify(reported));
  }
  if (stale.dataset.visible === "true") {
    throw new Error("the banner called a board it had just read stale");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute stale-write behavior when only the relationship context is superseded: %v\n%s", err, output)
	}
}
