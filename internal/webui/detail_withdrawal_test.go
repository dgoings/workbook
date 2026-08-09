package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A board intent can be refused while the detail form for its task is open, and
// the form is then displaying an optimistic value the server threw away. The
// withdrawal corrects that value where it stands.
//
// What it must not do is rebuild the form. The reader is very likely mid-word
// in it — a long description is exactly the edit that takes long enough for a
// board intent to fail underneath it — and a rebuilt form loses every keystroke
// and detaches a save in flight along with them.

const (
	withdrawalEditsTaskID    = "WB-01J0000000000000000000WD01"
	withdrawalInFlightTaskID = "WB-01J0000000000000000000WD02"
	withdrawalDepartedTaskID = "WB-01J0000000000000000000WD03"
)

// The reader is typing when their board change is refused. Everything they
// typed stays, in the same node, with the caret where they left it; the fields
// they never touched follow the version the server holds — including the ones a
// concurrent edit moved, not just the one the refused intent projected.
//
// The save afterwards is the proof that the baseline moved with the display: it
// carries the two fields this reader actually changed, against the head that
// now exists, and says nothing about the status the server refused.
func TestHandlerClientWithdrawalKeepsUnsavedDetailEditsAndCorrectsTheRest(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(withdrawalEditsTaskID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Description = "Original."
	moved.Labels = []string{"docs"}
	moved.Head = "head-1"
	// What the server actually holds by the time the refusal lands: another
	// clone renamed the task and labelled it, and the drop never applied.
	elsewhere := moved
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Labels = []string{"docs", "web"}
	elsewhere.Head = "head-2"
	saved := elsewhere
	saved.Head = "head-3"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1,
		Tasks:        []core.Task{elsewhere},
		Presentation: presentationForTasks([]core.Task{elsewhere}),
	})

	program := clientDOMHarness("/", string(document)) + script + `
const typed = "Half a paragraph the reader is still in the middle of.";
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  const bodies = [];
  let releaseIntent;
  let refuseIntent = true;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (refuseIntent) {
        return new Promise((resolve) => {
          releaseIntent = () => {
            taskResponse = ` + string(truth) + `;
            resolve({ ok: false, json: async () => ({
              format: "workbook.error", version: 1,
              error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
            }) });
          };
        });
      }
      bodies.push({ url, body: JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, saved)) + `
      }) };
    }
    return boardFetch(url, options);
  };

  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });

  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  const description = findElement(main, (element) => element.id === "task-description");
  const status = findElement(main, (element) => element.id === "task-status");
  const priority = findElement(main, (element) => element.id === "task-priority");
  if (!form || status.value !== "in-progress") {
    throw new Error("the open form does not project the pending intent: " + JSON.stringify(status && status.value));
  }
  // The reader is mid-edit: a long description and a deliberate priority
  // change. Title, status and labels are left exactly as the form drew them.
  description.value = typed;
  priority.children.forEach((option) => { option.selected = option.value === "high"; });
  description.focus();

  releaseIntent();
  await pending;

  // Read back out of the document rather than off the captured nodes: a
  // rebuilt form leaves the old ones detached and still holding every value
  // they were given, which is the one way this could pass while the reader's
  // work is gone from the screen.
  const shown = {
    form: findElement(main, (element) => element.tagName === "FORM"),
    title: findElement(main, (element) => element.id === "task-title"),
    description: findElement(main, (element) => element.id === "task-description"),
    status: findElement(main, (element) => element.id === "task-status"),
    priority: findElement(main, (element) => element.id === "task-priority")
  };
  if (shown.description.value !== typed) {
    throw new Error("the withdrawal destroyed the unsaved description: " + JSON.stringify(shown.description.value));
  }
  if (shown.priority.value !== "high") {
    throw new Error("the withdrawal reverted an edited field: " + JSON.stringify(shown.priority.value));
  }
  if (shown.status.value !== "ready") {
    throw new Error("the form kept the refused optimistic status: " + JSON.stringify(shown.status.value));
  }
  if (shown.title.value !== "Renamed elsewhere") {
    throw new Error("an untouched field did not follow the server: " + JSON.stringify(shown.title.value));
  }
  if (shown.form !== form || shown.description !== description) {
    throw new Error("the withdrawal rebuilt the form instead of correcting it");
  }
  if (document.activeElement !== description) {
    throw new Error("the withdrawal took the caret out of the field being typed in");
  }
  const chiclets = findElements(main, (element) =>
    Object.hasOwn(element.dataset, "label")).map((chiclet) => chiclet.dataset.label);
  if (JSON.stringify(chiclets) !== '["docs","web"]') {
    throw new Error("an untouched label set did not follow the server: " + JSON.stringify(chiclets));
  }
  const message = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!message.textContent.includes("changed elsewhere") ||
      !message.textContent.includes("your edits are kept")) {
    throw new Error("the form does not say what happened to it: " + JSON.stringify(message.textContent));
  }

  // The baseline moved with the display, so the save that follows sends the
  // two fields this reader changed and nothing else — against the head the
  // refusal's own refresh established.
  refuseIntent = false;
  await form.eventListeners.submit({ preventDefault() {} });
  if (bodies.length !== 1) {
    throw new Error("the save sent " + bodies.length + " mutations");
  }
  if (JSON.stringify(bodies[0].body) !== JSON.stringify({
    description: typed, priority: "high", expectedHead: "head-2"
  })) {
    throw new Error("the save did not carry exactly the reader's edits: " + JSON.stringify(bodies[0].body));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute withdrawal edit-preservation behavior: %v\n%s", err, output)
	}
}

// A save can be in flight when the board intent behind it is refused. Rebuilding
// the form detaches that save: the node it reports into is gone, its handler
// returns without a word, and the reader is told their board change failed and
// nothing at all about the save they were waiting on.
func TestHandlerClientWithdrawalLeavesAnInFlightDetailSaveAttached(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(withdrawalInFlightTaskID, "Moved", core.StatusReady, core.PriorityMedium)
	moved.Description = "Original."
	moved.Head = "head-1"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  let releaseIntent;
  let releaseSave;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (url.endsWith("/position")) {
        return new Promise((resolve) => {
          releaseIntent = () => resolve({ ok: false, json: async () => ({
            format: "workbook.error", version: 1,
            error: { category: "not-found", message: "no task has that ID" }
          }) });
        });
      }
      return new Promise((resolve) => {
        releaseSave = () => resolve({ ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "validation", message: "The server refused this save." }
        }) });
      });
    }
    return boardFetch(url, options);
  };

  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });

  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  description.value = "Saved while the board change was still in flight.";
  const saving = form.eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();

  // The board change is refused while the save is still open.
  releaseIntent();
  await pending;
  releaseSave();
  await saving;

  const message = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (message.textContent !== "The server refused this save.") {
    throw new Error("the in-flight save reported nothing when it landed: " + JSON.stringify(message.textContent));
  }
  if (description.value !== "Saved while the board change was still in flight.") {
    throw new Error("the withdrawal destroyed the text the open save was carrying");
  }
  const save = findElement(main, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Save");
  if (save.disabled) {
    throw new Error("the form was left unsavable after the save it reported failed");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute withdrawal in-flight save behavior: %v\n%s", err, output)
	}
}

// The likeliest refusal of all is a task another clone deleted, and the refresh
// a `stale-write` forces is what takes it out of the model. There is then
// nothing to correct the form against — but the reader's unsaved text is still
// theirs, and is the last copy of it anywhere. The form stays, says the board no
// longer carries the task, and survives the polls that follow.
func TestHandlerClientWithdrawalKeepsTheFormWhenTheTaskLeavesTheBoard(t *testing.T) {
	node := requireNode(t)
	moved := clientPlacementTask(withdrawalDepartedTaskID, "Moved", core.StatusReady, core.PriorityMedium)
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
const typed = "Notes nobody else has a copy of.";
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  const boardFetch = globalThis.fetch;
  let releaseIntent;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => {
        releaseIntent = () => {
          // The refusal and the deletion are the same event, so the refresh it
          // forces is the one that empties the board.
          taskResponse = ` + string(emptied) + `;
          resolve({ ok: false, json: async () => ({
            format: "workbook.error", version: 1,
            error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
          }) });
        };
      });
    }
    return boardFetch(url, options);
  };

  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  description.value = typed;

  releaseIntent();
  await pending;
  await intervalCallback();

  const shown = findElement(main, (element) => element.id === "task-description");
  if (!shown || shown.value !== typed) {
    throw new Error("the unsaved text left with the task: " + JSON.stringify(shown && shown.value));
  }
  if (findElement(main, (element) => element.tagName === "FORM") !== form || shown !== description) {
    throw new Error("a departed task replaced the form the reader was typing in");
  }
  const message = findElement(main, (element) => Object.hasOwn(element.dataset, "saveStatus"));
  if (!message.textContent.includes("no longer carries it") ||
      !message.textContent.includes("Your edits are kept")) {
    throw new Error("the form does not say why nothing moved: " + JSON.stringify(message.textContent));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute withdrawal departed-task behavior: %v\n%s", err, output)
	}
}
