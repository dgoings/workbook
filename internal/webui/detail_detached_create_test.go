package webui

import (
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A create can outlive the form it was typed into, exactly the way a save can:
// the reader stops waiting on a slow request and goes back to the board, and the
// POST is still open when the node this form reports into leaves the document.
//
// The handler used to return without a word in that case — the same silence PR
// #73 took out of the save path and deliberately left in this one. It is worse
// here, because what disappears with it is not only the outcome but the draft:
// the reader walked away believing a task was being created, nothing on screen
// ever contradicted them, and everything they typed went with the node.
//
// Only a create that staged a relationship can reach that state. One that staged
// none leaves the form the moment Save is pressed and reports through the
// optimistic path from wherever the reader landed; one that staged relationships
// waits for the ID the server has not assigned yet, and that wait is the window.

const (
	detachedCreatePrerequisiteID = "WB-01J0000000000000000000DC01"
	detachedCreateCreatedID      = "WB-01J0000000000000000000DC02"
)

const (
	detachedCreateTitle       = "Slow to create"
	detachedCreateDescription = "A paragraph the reader waited on and then stopped waiting on."
	detachedCreateLabel       = "recovery"
)

// heldDetachedCreate types a draft, stages the relationship that makes the
// create wait for the server, and submits it with the POST held open. It leaves
// `creating` pending and `releaseCreate` armed with `resolution`, so a caller can
// take the reader somewhere else before the server answers.
func heldDetachedCreate(prerequisiteID, prerequisiteTitle, resolution string) string {
	return `
  const prerequisiteID = ` + strconv.Quote(prerequisiteID) + `;
  const boardFetch = globalThis.fetch;
  let releaseCreate = null;
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/tasks" && options.method === "POST") {
      fetchCalls.push({ url, options });
      return new Promise((resolve) => { releaseCreate = () => resolve(` + resolution + `); });
    }
    return boardFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  if (!form) throw new Error("the New Task form did not render");
  findElement(main, (element) => element.id === "task-title").value = ` +
		strconv.Quote(detachedCreateTitle) + `;
  findElement(main, (element) => element.id === "task-description").value = ` +
		strconv.Quote(detachedCreateDescription) + `;
  const labels = findElement(main, (element) => element.id === "task-labels");
  labels.value = ` + strconv.Quote(detachedCreateLabel) + `;
  labels.eventListeners.keydown({ key: "Enter", preventDefault() {} });

  // A staged relationship is what makes this create wait for the server instead
  // of leaving the form the moment Save is pressed.
  const dependsGroup = findElement(main, (element) => element.textContent === "Depends On").parentElement;
  const combobox = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  combobox.value = ` + strconv.Quote(prerequisiteTitle) + `;
  combobox.eventListeners.input();
  findElement(dependsGroup, (element) => element.attributes.role === "option" &&
    element.dataset.candidateId === prerequisiteID).eventListeners.click();
  await findElement(dependsGroup, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();

  const creating = form.eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();
  if (!releaseCreate) throw new Error("the create never reached the server");
`
}

// leaveForTheBoard is the reader giving up on the request and taking the Back
// link, which is what takes the node this create reports into out of the
// document.
const leaveForTheBoard = `
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  if (main.firstElementChild !== boardView) {
    throw new Error("the reader did not end up on the board");
  }
`

// The reader types a task, stages a prerequisite for it, saves, gives up on the
// slow request and goes back to the board. The create is refused while they are
// standing there. They learn about it where they are, rather than believing a
// task was made.
func TestHandlerClientDetachedCreateReportsItsRefusal(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask(detachedCreatePrerequisiteID, "Prerequisite", core.StatusDone, core.PriorityHigh)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	board := tasksDocumentJSON(t, []core.Task{prerequisite})

	program := clientDOMHarness("/tasks/new?status=ready", board) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + heldDetachedCreate(prerequisite.ID, prerequisite.Title, `{ ok: false, json: async () => ({
    format: "workbook.error", version: 1,
    error: { category: "validation", message: "title is not valid" }
  }) }`) + leaveForTheBoard + `
  releaseCreate();
  await creating;

  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the detached create reported nothing anywhere: " + JSON.stringify(notice.textContent));
  }
  const copy = findElement(reports[0], (element) => element.tagName === "P").textContent;
  if (!copy.includes(` + strconv.Quote(detachedCreateTitle) + `)) {
    throw new Error("the report does not name the draft it is about: " + JSON.stringify(copy));
  }
  if (!copy.includes("not saved")) {
    throw new Error("the report does not say the create did not happen: " + JSON.stringify(copy));
  }
  if (!copy.includes("still open when you left the form")) {
    throw new Error("the report does not say the create outlived the form: " + JSON.stringify(copy));
  }
  // The server's sentence is a Go error: lower case and unpunctuated. Spliced
  // between two sentences of this client's own it runs into the one after it,
  // so it is quoted at the end, where the reader can stop.
  if (!copy.endsWith("title is not valid")) {
    throw new Error("the server's message is not the last word of the report: " + JSON.stringify(copy));
  }
  if (reports[0].dataset.kind !== "error") {
    throw new Error("a refusal was reported as something other than an error: " + JSON.stringify(reports[0].dataset.kind));
  }
  // Nothing was created, so there is no task to offer a way back to. The one
  // control this report carries is the one that gets the draft back.
  if (findElement(reports[0], (element) => element.tagName === "A")) {
    throw new Error("the report offers a route to a task the server refused to make");
  }
  const drawn = boardLists.flatMap((list) => list.querySelectorAll(".task-card"))
    .filter((card) => card.textContent.includes(` + strconv.Quote(detachedCreateTitle) + `));
  if (drawn.length !== 0) {
    throw new Error("a refused create left a card standing on the board");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached create refusal behavior: %v\n%s", err, output)
	}
}

// The report is now the only copy of what was typed, so it holds the draft the
// way a refused create's report always has: one control puts every field back
// into a New Task form. The relationships staged beside those fields do not come
// back with them, and the report says so rather than letting the reader save a
// task that has quietly lost its prerequisite.
func TestHandlerClientDetachedCreateKeepsTheDraftRecoverable(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask(detachedCreatePrerequisiteID, "Prerequisite", core.StatusDone, core.PriorityHigh)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	board := tasksDocumentJSON(t, []core.Task{prerequisite})

	program := clientDOMHarness("/tasks/new?status=ready", board) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + heldDetachedCreate(prerequisite.ID, prerequisite.Title, `{ ok: false, json: async () => ({
    format: "workbook.error", version: 1,
    error: { category: "validation", message: "title is not valid" }
  }) }`) + leaveForTheBoard + `
  releaseCreate();
  await creating;

  const restore = findElement(notice, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft");
  if (!restore) throw new Error("the detached create's report offers no way back to the draft");
  restore.eventListeners.click();

  const restored = findElement(main, (element) => element.tagName === "FORM");
  if (!restored || restored === form) throw new Error("Restore draft did not open a New Task form");
  const restoredTitle = findElement(restored, (element) => element.id === "task-title");
  const restoredDescription = findElement(restored, (element) => element.id === "task-description");
  const restoredLabels = findElements(restored, (element) =>
    Object.hasOwn(element.dataset, "label")).map((chiclet) => chiclet.dataset.label);
  if (restoredTitle.value !== ` + strconv.Quote(detachedCreateTitle) + ` ||
      restoredDescription.value !== ` + strconv.Quote(detachedCreateDescription) + ` ||
      JSON.stringify(restoredLabels) !== ` + strconv.Quote(`["`+detachedCreateLabel+`"]`) + `) {
    throw new Error("Restore draft lost what the detached create was carrying");
  }
  const feedback = findElement(restored, (element) =>
    element.className === "form-status" && element.textContent !== "");
  if (!feedback) throw new Error("the restored form does not say why it is back");
  if (!feedback.textContent.endsWith("title is not valid")) {
    throw new Error("the restored form does not end on why the create was refused: " +
      JSON.stringify(feedback.textContent));
  }
  // The sidebar came back empty, because the only machinery that hands values
  // back hands the fields and not the edges. A form that said nothing about it
  // would invite a save of a task quietly missing its prerequisite.
  const restoredRows = findElements(restored, (element) => Object.hasOwn(element.dataset, "relationshipId"));
  if (restoredRows.length !== 0) {
    throw new Error("the restored draft claimed relationships this form does not hold");
  }
  if (!feedback.textContent.includes("relationships staged with this draft")) {
    throw new Error("the restored form does not say the staged relationships are gone: " +
      JSON.stringify(feedback.textContent));
  }
  if (!notice.hidden) throw new Error("Restore draft left the failure report standing");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached create draft recovery: %v\n%s", err, output)
	}
}

// The other half of a detached outcome is the create that lands. The task it made
// arrives on the board through the refresh every create already performs, so
// there is nothing to announce — but the route the reader deliberately went to
// while waiting is theirs, and a create finishing behind them must not take it
// away, exactly as a landed save must not.
func TestHandlerClientDetachedCreateLeavesTheRouteTheReaderChose(t *testing.T) {
	node := requireNode(t)
	prerequisite := clientPlacementTask(detachedCreatePrerequisiteID, "Prerequisite", core.StatusDone, core.PriorityHigh)
	created := clientPlacementTask(detachedCreateCreatedID, detachedCreateTitle, core.StatusReady, core.PriorityMedium)
	created.Description = detachedCreateDescription
	created.Labels = []string{detachedCreateLabel}
	created.Dependencies = []string{prerequisite.ID}
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	board := tasksDocumentJSON(t, []core.Task{prerequisite})
	refreshed := tasksDocumentJSON(t, []core.Task{created, prerequisite})

	program := clientDOMHarness("/tasks/new?status=ready", board) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + heldDetachedCreate(prerequisite.ID, prerequisite.Title, `(() => {
    taskResponse = `+refreshed+`;
    return { ok: true, json: async () => (`+taskMutationJSON(refreshed, "")+`) };
  })()`) + `
  // Rather than wait, the reader opens the prerequisite they staged, to read it.
  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(prerequisiteID);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const reading = main.firstElementChild;

  releaseCreate();
  await creating;

  if (main.firstElementChild !== reading) {
    throw new Error("the landed create replaced the route the reader had gone to");
  }
  const heading = findElement(main, (element) => element.id === "task-form-title");
  if (heading.textContent !== ` + strconv.Quote(prerequisite.Title) + `) {
    throw new Error("the reader was moved off the task they opened: " + JSON.stringify(heading.textContent));
  }
  const wantPath = "/tasks/" + encodeURIComponent(prerequisiteID);
  if (historyPaths.length !== 1 || historyPaths[0] !== wantPath) {
    throw new Error("the landed create pushed a route of its own: " + JSON.stringify(historyPaths));
  }
  if (historyReplacements.length !== 0) {
    throw new Error("the landed create re-armed a New Task form over the reader's route: " +
      JSON.stringify(historyReplacements));
  }
  if (window.location.href !== new URL(wantPath, window.location.href).href) {
    throw new Error("the landed create moved the reader's address: " + JSON.stringify(window.location.href));
  }
  if (!notice.hidden) {
    throw new Error("an accepted create was reported as news: " + JSON.stringify(notice.textContent));
  }

  // Nothing was announced because nothing needed to be: the refresh the create
  // already performs is what puts the new task on the board the reader goes
  // back to.
  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const card = boardLists.flatMap((list) => list.querySelectorAll(".task-card"))
    .find((node) => node.dataset.taskId === ` + strconv.Quote(created.ID) + `);
  if (!card) throw new Error("the created task never reached the board");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute detached landed create behavior: %v\n%s", err, output)
	}
}
