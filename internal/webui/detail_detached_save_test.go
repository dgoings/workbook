package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A save from the detail form can outlive the form it was made in: the reader
// stops waiting on a slow request and goes back to the board, or a restore
// renders another form over the route, and the request is still open when the
// node it reports into leaves the document.
//
// The handler used to return without a word in exactly that case, which is the
// worst possible moment to say nothing: the reader walked away believing a save
// was on its way, and nothing on screen ever contradicted them. The outcome
// follows them instead — to the notice, which outlives every route, and onto
// the task's own form for the next time it is opened.

const (
	detachedSaveRefusedTaskID = "WB-01J0000000000000000000DS01"
	detachedSaveStaleTaskID   = "WB-01J0000000000000000000DS02"
	detachedSaveLandedTaskID  = "WB-01J0000000000000000000DS03"
	detachedSaveOtherTaskID   = "WB-01J0000000000000000000DS04"
	detachedSaveDeletedTaskID = "WB-01J0000000000000000000DS05"
)

// The reader saves, gives up on a slow request and takes the Back link to the
// board. The save is refused while they are standing there. They learn about it
// on the board, and the task's form says the same thing when they open it.
func TestHandlerClientDetachedDetailSaveReportsItsRefusal(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(detachedSaveRefusedTaskID, "Slow to save", core.StatusReady, core.PriorityMedium)
	task.Description = "Original."
	task.Head = "head-1"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
const typed = "A paragraph the reader waited on and then stopped waiting on.";
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  let releaseSave;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseSave = () => resolve({ ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "validation", message: "The server refused this save." }
        }) });
      });
    }
    return boardFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  if (!form || !description) throw new Error("the detail form did not render");
  description.value = typed;
  const saving = form.eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();

  // The reader stops waiting and goes back to the board, taking the node this
  // save reports into out of the document with them.
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  if (main.firstElementChild !== boardView) {
    throw new Error("the reader did not end up on the board");
  }

  releaseSave();
  await saving;

  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the detached save reported nothing anywhere: " + JSON.stringify(notice.textContent));
  }
  const copy = findElement(reports[0], (element) => element.tagName === "P").textContent;
  if (!copy.includes(` + strconv.Quote(task.ID) + `) || !copy.includes("Slow to save")) {
    throw new Error("the report does not name the task it is about: " + JSON.stringify(copy));
  }
  if (!copy.includes("not saved")) {
    throw new Error("the report does not say the save did not happen: " + JSON.stringify(copy));
  }
  // The server's sentence is a Go error: lower case and unpunctuated. Spliced
  // between two sentences of this client's own it runs into the one after it,
  // so it is quoted at the end, where the reader can stop.
  if (!copy.endsWith("The server refused this save.")) {
    throw new Error("the server's message is not the last word of the report: " + JSON.stringify(copy));
  }
  if (reports[0].dataset.kind !== "error") {
    throw new Error("a refusal was reported as something other than an error: " + JSON.stringify(reports[0].dataset.kind));
  }

  // A report read from the board has to offer the way back to the task it is
  // about, the way a created task's warnings do.
  const open = findElement(reports[0], (element) => element.tagName === "A");
  if (!open || open.href !== "/tasks/" + encodeURIComponent(` + strconv.Quote(task.ID) + `)) {
    throw new Error("the report does not offer the task it is about: " + JSON.stringify(open && open.href));
  }
  await documentEventListeners.click({
    target: open, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const message = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!message || !message.textContent.includes("were not saved") ||
      !message.textContent.endsWith("The server refused this save.")) {
    throw new Error("the re-opened form says nothing about the save it lost: " + JSON.stringify(message && message.textContent));
  }
  // Said once. A staged message that survived its own reading would greet the
  // reader again every time they opened the task.
  const reopened = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: reopened, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  await documentEventListeners.click({
    target: open, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const second = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (second.textContent !== "") {
    throw new Error("the staged message was read twice: " + JSON.stringify(second.textContent));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached save refusal behavior: %v\n%s", err, output)
	}
}

// The refusal that detaches most often is the stale write, because it is the one
// the reader is invited to answer by saving again — and the answer is a message
// about a conflict, which is exactly the thing they must not be left guessing
// about. The report names the conflict rather than the raw server sentence about
// heads, and the re-base it would have done has no form left to do it to.
func TestHandlerClientDetachedDetailSaveReportsAConflictAsOne(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(detachedSaveStaleTaskID, "Edited twice", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	elsewhere := task
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks:        []core.Task{elsewhere},
		Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  let releaseSave;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseSave = () => {
          taskResponse = ` + string(truth) + `;
          resolve({ ok: false, json: async () => ({
            format: "workbook.error", version: 1,
            error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
          }) });
        };
      });
    }
    return boardFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  title.value = "Renamed here";
  const saving = form.eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();

  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });

  releaseSave();
  await saving;

  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the detached conflict reported nothing anywhere: " + JSON.stringify(notice.textContent));
  }
  const copy = findElement(reports[0], (element) => element.tagName === "P").textContent;
  if (!copy.includes("not saved")) {
    throw new Error("the report does not say the save did not happen: " + JSON.stringify(copy));
  }
  // Described rather than quoted, and last either way: the reason is the last
  // sentence of the report whether it is this client's or the server's.
  if (!copy.endsWith("The task changed elsewhere, so the save was refused.")) {
    throw new Error("the report does not end on why the save was lost: " + JSON.stringify(copy));
  }
  // The raw refusal names a head, which is a fact about this client's
  // bookkeeping rather than about the reader's task.
  if (copy.includes("head-1")) {
    throw new Error("the report repeated the server's head sentence: " + JSON.stringify(copy));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached conflict behavior: %v\n%s", err, output)
	}
}

// The other half of a detached outcome is the save that lands. An accepted save
// is reported by the board showing it, the way every accepted save in this
// client is, so there is nothing to say — but the route the reader deliberately
// went to is theirs, and returning them to the board would take it away from
// them a second or two after they chose it.
func TestHandlerClientDetachedDetailSaveLeavesTheRouteTheReaderChose(t *testing.T) {
	node := requireNode(t)
	saving := clientPlacementTask(detachedSaveLandedTaskID, "Slow to save", core.StatusReady, core.PriorityMedium)
	saving.Head = "head-1"
	other := clientPlacementTask(detachedSaveOtherTaskID, "Read while waiting", core.StatusReady, core.PriorityMedium)
	other.Head = "head-9"
	saved := saving
	saved.Description = "Landed after the reader left."
	saved.Head = "head-2"
	tasks := []core.Task{saving, other}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+saving.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/tasks/"+saving.ID, string(document)) + script + `
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  let releaseSave;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseSave = () => resolve({ ok: true, json: async () => ({
          format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, saved)) + `
        }) });
      });
    }
    return boardFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  description.value = "Landed after the reader left.";
  const pending = form.eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();

  // Rather than wait, the reader opens the other task they came here to read.
  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(other.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const reading = main.firstElementChild;

  releaseSave();
  await pending;

  if (main.firstElementChild !== reading) {
    throw new Error("the landed save replaced the route the reader had gone to");
  }
  const heading = findElement(main, (element) => element.id === "task-form-title");
  if (heading.textContent !== "Read while waiting") {
    throw new Error("the reader was moved off the task they opened: " + JSON.stringify(heading.textContent));
  }
  if (window.location.href !== new URL("/tasks/" + encodeURIComponent(` + strconv.Quote(other.ID) + `), window.location.href).href) {
    throw new Error("the landed save pushed a route of its own: " + JSON.stringify(window.location.href));
  }
  if (!notice.hidden) {
    throw new Error("an accepted save was reported as news: " + JSON.stringify(notice.textContent));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached landed save behavior: %v\n%s", err, output)
	}
}

// The report offers the way back to the task it is about, which is only an offer
// when there is a task to go back to. A `stale-write` refusal forces a refresh
// before the form is given up on, and when that refresh reads the board and no
// longer finds the task, the offer leads to "Task not found" — the one page this
// client goes out of its way not to drop a reader on. The proof is in hand at
// exactly that moment, so the link goes and the report says why.
//
// The reader leaves while the forced refresh is still open, because that is the
// only path on which a detached report knows what a refresh found.
func TestHandlerClientDetachedDetailSaveDoesNotOfferADeletedTask(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(detachedSaveDeletedTaskID, "Deleted while saving", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "Original."
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/tasks/"+task.ID)
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	emptied := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}, Presentation: []TaskPresentation{},
	})

	program := clientDOMHarness("/tasks/"+task.ID, string(document)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
  const boardFetch = globalThis.fetch;
  // Held only for the read the *forced* refresh makes: the reader has to be
  // able to leave while it is open, which is what leaves this save reporting
  // from outside the form with a refresh result in hand.
  let releaseBoardRead = null;
  let holdBoardRead = false;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
      }) };
    }
    if (url === "/api/tasks" && holdBoardRead) {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseBoardRead = () => {
          // The refusal and the deletion are the same event: this is the read
          // that proves the task is gone rather than merely changed.
          taskResponse = ` + string(emptied) + `;
          resolve({ ok: true, json: async () => taskResponse });
        };
      });
    }
    return boardFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  if (!form || !description) throw new Error("the detail form did not render");
  description.value = "A paragraph written into a task another clone deleted.";
  holdBoardRead = true;
  const saving = form.eventListeners.submit({ preventDefault() {} });
  await settle();
  if (!releaseBoardRead) {
    throw new Error("the refusal never forced the refresh this report reads");
  }

  // The reader gives up on the refusal's refresh and goes back to the board,
  // taking the node this save reports into out of the document with them.
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  if (main.firstElementChild !== boardView) {
    throw new Error("the reader did not end up on the board");
  }
  releaseBoardRead();
  await saving;

  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the detached save reported nothing anywhere: " + JSON.stringify(notice.textContent));
  }
  const copy = findElement(reports[0], (element) => element.tagName === "P").textContent;
  if (!copy.includes(` + strconv.Quote(task.ID) + `) || !copy.includes("not saved")) {
    throw new Error("the report does not say which save did not happen: " + JSON.stringify(copy));
  }
  if (reports[0].dataset.kind !== "error") {
    throw new Error("a refusal was reported as something other than an error: " + JSON.stringify(reports[0].dataset.kind));
  }
  // The offer is the whole point: there is nothing behind it.
  const open = findElement(reports[0], (element) => element.tagName === "A");
  if (open) {
    throw new Error("the report offered a task the refresh had proved deleted: " + JSON.stringify(open.href));
  }
  if (!copy.includes("nothing left to open")) {
    throw new Error("the report drops the offer without saying why: " + JSON.stringify(copy));
  }
  // Described rather than quoted, and last either way — and what it describes is
  // the deletion this refresh read, not the vaguer conflict the raw category
  // would have reported.
  if (!copy.endsWith("The task was deleted elsewhere, which is why the save was refused.")) {
    throw new Error("the report does not end on why the save was lost: " + JSON.stringify(copy));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached save behavior when the refresh finds the task deleted: %v\n%s", err, output)
	}
}
