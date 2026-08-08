package webui

import (
	"os/exec"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A create that stages no relationships leaves the form the moment Save is
// pressed and draws the task from what was typed, so the perceived cost of a
// create is a render rather than a round trip. The card the board stands up in
// the meantime is not a task yet: it carries no ID to copy, no detail route to
// open, and no drag, because none of those exist until the server answers.
func TestHandlerClientRendersACreatedTaskBeforeItsResponse(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000C1", "Instant task", core.StatusReady, core.PriorityMedium)
	created.Description = "Typed before the server heard about it."
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	empty := tasksDocumentJSON(t, nil)
	refreshed := tasksDocumentJSON(t, []core.Task{created})

	program := clientDOMHarness("/tasks/new?status=ready", empty) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  let activeDocument = ` + empty + `;
  let releasePost;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => new Promise((resolve) => {
        releasePost = () => {
          activeDocument = ` + refreshed + `;
          resolve(` + taskMutationJSON(refreshed, "") + `);
        };
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      return { ok: true, json: async () => activeDocument };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = ` + strconv.Quote(created.Title) + `;
  findElement(form, (element) => element.id === "task-description").value = ` + strconv.Quote(created.Description) + `;
  // A keyboard user submits from Save, so that is the node the save destroys.
  findElement(form, (element) => hasClassToken(element, "save-button")).focus();
  const settled = form.eventListeners.submit({ preventDefault() {} });

  if (main.firstElementChild !== boardView) {
    throw new Error("Save waited for the server before leaving the form");
  }
  if (historyPaths.length !== 1 || historyPaths[0] !== "/") {
    throw new Error("Save navigation = " + JSON.stringify(historyPaths) + ", want [\"/\"]");
  }
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const optimistic = ready.querySelectorAll(".task-card")
    .filter((node) => node.dataset.pendingCreate === "true");
  if (optimistic.length !== 1) {
    throw new Error("the board drew " + optimistic.length + " unconfirmed cards, want 1");
  }
  const card = optimistic[0];
  if (!card.textContent.includes(` + strconv.Quote(created.Title) + `) ||
      !card.textContent.includes(` + strconv.Quote(created.Description) + `)) {
    throw new Error("the unconfirmed card does not carry what was typed");
  }
  if (document.activeElement !== card) {
    throw new Error("the optimistic landing dropped the caret instead of handing it to the new card");
  }
  if (card.draggable) throw new Error("a task the server has not accepted yet offered a drag");
  if (findElement(card, (element) => element.tagName === "A")) {
    throw new Error("the unconfirmed card links to a task route that does not exist");
  }
  if (findElement(card, (element) =>
      Object.prototype.hasOwnProperty.call(element.dataset, "copyTaskId"))) {
    throw new Error("the unconfirmed card offers an ID nothing can be copied from");
  }
  const readyCount = boardCounts.find((element) => element.dataset.count === "ready");
  if (readyCount.textContent !== "1") {
    throw new Error("the Ready count = " + readyCount.textContent + ", want the unconfirmed task counted");
  }

  // A poll replaces the whole model while the create is still open. The card
  // has to survive it exactly as a pending board intent does.
  await intervalCallback();
  if (!ready.querySelectorAll(".task-card").some((node) => node.dataset.pendingCreate === "true")) {
    throw new Error("a poll erased the unconfirmed card while the create was open");
  }

  releasePost();
  await settled;
  const cards = ready.querySelectorAll(".task-card");
  if (cards.some((node) => node.dataset.pendingCreate === "true")) {
    throw new Error("the unconfirmed card outlived the task it stood in for");
  }
  const durable = cards.find((node) => node.dataset.taskId === ` + strconv.Quote(created.ID) + `);
  if (!durable) throw new Error("the durable task did not replace the unconfirmed card");
  if (document.activeElement !== durable) {
    throw new Error("the durable card did not take the caret from the card it replaced");
  }
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute optimistic task creation: %v\n%s", err, output)
	}
}

// A refused create has nowhere to report to: the form that would have shown the
// message is gone, and the board's stale banner is cleared by the next poll a
// second later. So the failure is reported where it survives polls, it says
// what was refused, and everything typed is one click from being back in the
// form it was typed into.
func TestHandlerClientRefusedCreateKeepsTheDraftRecoverable(t *testing.T) {
	node := requireNode(t)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	empty := tasksDocumentJSON(t, nil)

	program := clientDOMHarness("/tasks/new?status=ready", empty) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  let createCalls = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      return { ok: false, json: async () => ({
        format: "workbook.error",
        version: 1,
        error: { category: "validation", message: "title is not valid" }
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      return { ok: true, json: async () => (` + empty + `) };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(form, (element) => element.id === "task-title");
  const description = findElement(form, (element) => element.id === "task-description");
  const status = findElement(form, (element) => element.id === "task-status");
  const priority = findElement(form, (element) => element.id === "task-priority");
  const labels = findElement(form, (element) => element.id === "task-labels");
  title.value = "Unsaved title";
  description.value = "Unsaved description";
  status.children.forEach((option) => { option.selected = option.value === "in-review"; });
  priority.children.forEach((option) => { option.selected = option.value === "high"; });
  labels.value = "review, recovery";
  await form.eventListeners.submit({ preventDefault() {} });

  if (createCalls !== 1) throw new Error("Save did not attempt exactly one create");
  if (main.firstElementChild !== boardView) throw new Error("Save did not land on the board");
  const drawn = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  if (drawn.length !== 0) throw new Error("a refused create left a card standing on the board");
  if (createNotice.hidden) throw new Error("a refused create reported nothing");
  if (!createNotice.textContent.includes("title is not valid") ||
      !createNotice.textContent.includes("Unsaved title")) {
    throw new Error("the failure report does not name the task or the reason: " + createNotice.textContent);
  }

  // The stale banner is cleared by every successful poll, which is why the
  // report does not live there.
  await intervalCallback();
  if (createNotice.hidden || !createNotice.textContent.includes("title is not valid")) {
    throw new Error("a poll erased the create failure report");
  }

  const restore = findElement(createNotice, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft");
  if (!restore) throw new Error("the failure report offers no way back to the draft");
  restore.eventListeners.click();

  const restored = findElement(main, (element) => element.tagName === "FORM");
  if (!restored || restored === form) throw new Error("Restore draft did not open a New Task form");
  const restoredTitle = findElement(restored, (element) => element.id === "task-title");
  const restoredDescription = findElement(restored, (element) => element.id === "task-description");
  const restoredStatus = findElement(restored, (element) => element.id === "task-status");
  const restoredPriority = findElement(restored, (element) => element.id === "task-priority");
  const restoredLabels = findElement(restored, (element) => element.id === "task-labels");
  if (restoredTitle.value !== "Unsaved title" ||
      restoredDescription.value !== "Unsaved description" ||
      restoredStatus.value !== "in-review" ||
      restoredPriority.value !== "high" ||
      restoredLabels.value !== "review, recovery") {
    throw new Error("Restore draft lost what the refused create was carrying");
  }
  const feedback = findElement(restored, (element) =>
    element.className === "form-status" && element.textContent.includes("title is not valid"));
  if (!feedback) throw new Error("the restored form does not say why it is back");
  if (!createNotice.hidden) throw new Error("Restore draft left the failure report standing");
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute refused optimistic creation: %v\n%s", err, output)
	}
}

// A report can be holding the only copy of a task that was refused, so a later
// create adds its own report rather than replacing the one standing.
func TestHandlerClientRefusedCreatesEachKeepTheirOwnDraft(t *testing.T) {
	node := requireNode(t)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	empty := tasksDocumentJSON(t, nil)

	program := clientDOMHarness("/tasks/new?status=ready", empty) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: false, json: async () => ({
        format: "workbook.error",
        version: 1,
        error: { category: "operational", message: "the repository is locked" }
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      return { ok: true, json: async () => (` + empty + `) };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };

  // Create more keeps the user on a form, which is how two creates are refused
  // without either draft ever having been retyped.
  const toggle = findElement(main, (element) => element.id === "task-create-more");
  toggle.checked = true;
  toggle.eventListeners.change();
  const save = async (title) => {
    const form = findElement(main, (element) => element.tagName === "FORM");
    findElement(form, (element) => element.id === "task-title").value = title;
    await form.eventListeners.submit({ preventDefault() {} });
  };
  await save("First refused");
  await save("Second refused");

  const reports = findElements(createNotice, (element) =>
    hasClassToken(element, "notice__report"));
  if (reports.length !== 2) {
    throw new Error("two refused creates left " + reports.length + " reports, want 2");
  }
  const first = reports.find((report) => report.textContent.includes("First refused"));
  if (!first) throw new Error("the second refusal replaced the first one's draft");
  findElement(first, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft")
    .eventListeners.click();
  const restored = findElement(main, (element) => element.id === "task-title");
  if (!restored || restored.value !== "First refused") {
    throw new Error("Restore draft opened the wrong draft");
  }
  const left = findElements(createNotice, (element) => hasClassToken(element, "notice__report"));
  if (left.length !== 1 || !left[0].textContent.includes("Second refused")) {
    throw new Error("restoring one draft disturbed the other report");
  }
  if (createNotice.hidden) throw new Error("a standing report was hidden with the one restored");
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute stacked create refusals: %v\n%s", err, output)
	}
}

// A create that has a warning to report used to open the task to deliver it,
// which an instant save cannot do: the user is already somewhere else, often
// typing the next task. The warning is reported where it waits for them, with
// the task one click away.
func TestHandlerClientOptimisticCreateReportsWarningsWhereTheUserStands(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000C2", "Reported task", core.StatusReady, core.PriorityMedium)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	refreshed := tasksDocumentJSON(t, []core.Task{created})
	mutation := taskMutationJSON(refreshed, "Task creation projection needs repair.")

	program := clientDOMHarness("/tasks/new?status=ready", tasksDocumentJSON(t, nil)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + createFetchStub(mutation, refreshed) + `
  const toggle = findElement(main, (element) => element.id === "task-create-more");
  toggle.checked = true;
  toggle.eventListeners.change();
  findElement(main, (element) => element.id === "task-title").value = ` + strconv.Quote(created.Title) + `;
  await findElement(main, (element) => element.tagName === "FORM")
    .eventListeners.submit({ preventDefault() {} });

  if (historyPaths.length !== 0) {
    throw new Error("a reported create moved the user to " + JSON.stringify(historyPaths));
  }
  if (historyReplacements.length !== 1 || historyReplacements[0] !== "/tasks/new?status=ready") {
    throw new Error("a reported create did not re-arm the New Task form");
  }
  const rearmed = findElement(main, (element) => element.id === "task-title");
  if (!rearmed || rearmed.value !== "") throw new Error("the re-armed form is not clean");
  if (document.activeElement !== rearmed) throw new Error("the re-armed form lost the caret");
  if (createNotice.hidden ||
      !createNotice.textContent.includes("Task creation projection needs repair.")) {
    throw new Error("the create's warning was dropped: " + createNotice.textContent);
  }
  const open = findElement(createNotice, (element) =>
    element.tagName === "A" && element.href === "/tasks/" + encodeURIComponent(` + strconv.Quote(created.ID) + `));
  if (!open) throw new Error("the warning report does not open the task it is about");
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute reported optimistic creation: %v\n%s", err, output)
	}
}
