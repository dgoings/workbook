package webui

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
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

// Restoring a refused draft renders a New Task form over whatever route the
// user is standing on, and a route render replaces that node wholesale. With
// Create more on, the route they are standing on is usually the next task: the
// form re-arms, they start typing, and the refusal of the last one arrives
// while they do. Restoring the refused draft must not cost them the one they
// are in the middle of — that would move the loss the report exists to prevent
// onto the next task instead of preventing it.
func TestHandlerClientRestoringADraftKeepsTheFormItReplaces(t *testing.T) {
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

  const toggle = findElement(main, (element) => element.id === "task-create-more");
  toggle.checked = true;
  toggle.eventListeners.change();
  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "Refused first";
  await form.eventListeners.submit({ preventDefault() {} });

  // The form has re-armed and the next task is half typed into it when the
  // refusal of the last one is read.
  findElement(main, (element) => element.id === "task-title").value = "Still being typed";
  findElement(main, (element) => element.id === "task-description").value = "Half a description";
  findElement(main, (element) => element.id === "task-labels").value = "web, later";
  findElement(createNotice, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft")
    .eventListeners.click();

  const restored = findElement(main, (element) => element.id === "task-title");
  if (!restored || restored.value !== "Refused first") {
    throw new Error("Restore draft did not open the refused draft");
  }
  const reports = findElements(createNotice, (element) => hasClassToken(element, "notice__report"));
  if (reports.length !== 1) {
    throw new Error("restoring one draft left " + reports.length + " reports, want the displaced draft's");
  }
  if (createNotice.hidden) throw new Error("the displaced draft was reported into a hidden notice");
  const kept = reports.find((report) => report.textContent.includes("Still being typed"));
  if (!kept) {
    throw new Error("Restore draft destroyed the form being typed into: " + createNotice.textContent);
  }
  findElement(kept, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft")
    .eventListeners.click();
  const back = findElement(main, (element) => element.id === "task-title");
  if (!back || back.value !== "Still being typed") {
    throw new Error("the displaced draft did not come back");
  }
  if (findElement(main, (element) => element.id === "task-description").value !== "Half a description" ||
      findElement(main, (element) => element.id === "task-labels").value !== "web, later") {
    throw new Error("the displaced draft came back missing what was typed into it");
  }
  if (document.activeElement !== back) throw new Error("a restored draft did not take the caret");
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute displaced draft preservation: %v\n%s", err, output)
	}
}

// A refused create takes the card the caret was standing on with it, and the
// report it leaves behind holds the only copy of the draft. Focus has to follow
// the draft: a keyboard user who is dropped on the document body has to tab in
// from the top to reach the one control that can get their task back.
func TestHandlerClientRefusedCreateHandsTheCaretToTheReport(t *testing.T) {
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

  // Create more off, so the save lands on the board and the caret is handed to
  // the card the create is drawing.
  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "Refused under the caret";
  const settled = form.eventListeners.submit({ preventDefault() {} });
  const standIn = boardLists.flatMap((list) => list.querySelectorAll(".task-card"))[0];
  if (!standIn || document.activeElement !== standIn) {
    throw new Error("the optimistic landing did not put the caret on the card it drew");
  }
  await settled;

  const restore = findElement(createNotice, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Restore draft");
  if (!restore) throw new Error("a refused create offered no way back to the draft");
  if (document.activeElement !== restore) {
    throw new Error("a refused create dropped the caret instead of handing it to the draft's report");
  }
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute refused create focus: %v\n%s", err, output)
	}
}

// The board can carry a create before its POST answers: the server commits the
// task and then publishes it, and the read side is not held for either. A poll
// that lands in that window returns the created task while the client still has
// no ID for it, and drawing the stand-in beside it shows the task twice and
// counts the column one too high.
func TestHandlerClientPollThatOutrunsACreateRetiresItsStandIn(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000C3", "Outrun task", core.StatusReady, core.PriorityMedium)
	created.Description = "Committed before the POST answered."
	created.Labels = []string{"later", "web"}
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
        releasePost = () => resolve(` + taskMutationJSON(refreshed, "") + `);
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
  // Typed the way a person types them, so the match has to survive the
  // normalization the server applies on the way in.
  findElement(form, (element) => element.id === "task-labels").value = "web, later, web";
  const settled = form.eventListeners.submit({ preventDefault() {} });

  // The task is committed and readable; the POST is still open.
  activeDocument = ` + refreshed + `;
  await intervalCallback();

  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const cards = ready.querySelectorAll(".task-card");
  if (cards.length !== 1) {
    throw new Error("a poll that outran the create drew " + cards.length + " cards, want 1");
  }
  if (cards[0].dataset.taskId !== ` + strconv.Quote(created.ID) + `) {
    throw new Error("the durable task did not replace the card standing in for it");
  }
  const readyCount = boardCounts.find((element) => element.dataset.count === "ready");
  if (readyCount.textContent !== "1") {
    throw new Error("the Ready count = " + readyCount.textContent + ", want the task counted once");
  }
  if (document.activeElement !== cards[0]) {
    throw new Error("retiring the stand-in dropped the caret instead of handing it to the durable card");
  }

  releasePost();
  await settled;
  const settledCards = ready.querySelectorAll(".task-card");
  if (settledCards.length !== 1 || settledCards[0].dataset.taskId !== ` + strconv.Quote(created.ID) + `) {
    throw new Error("the create's response redrew a card the board already carried");
  }
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute poll that outran a create: %v\n%s", err, output)
	}
}

// The create notice shares a fixed-height flex column with the routed region,
// and a report leaves it only when someone clicks Dismiss. A locked repository
// with Create more on adds one per save, so without a cap the notice grows
// until the board or the form beneath it has no height left — and the page
// itself never scrolls, so there is nothing to scroll back to it with. The cap
// is style, so it is asserted as style: no script restores it.
func TestHandlerCreateNoticeCannotCrowdOutTheRoute(t *testing.T) {
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return nil, nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
	)
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	rule := styleRule(t, response.Body.String(), ".notice {")
	for _, declaration := range []string{"max-height", "overflow"} {
		if !strings.Contains(rule, declaration) {
			t.Errorf("the create notice rule %q declares no %s, so a run of reports can fill the viewport", rule, declaration)
		}
	}
}

// styleRule returns the declaration block the page's stylesheet gives a
// selector, so a test can assert on one rule rather than on the whole sheet.
func styleRule(t *testing.T, body, selector string) string {
	t.Helper()
	start := strings.Index(body, selector)
	if start < 0 {
		t.Fatalf("the rendered page declares no %q rule", selector)
	}
	end := strings.Index(body[start:], "}")
	if end < 0 {
		t.Fatalf("the %q rule is never closed", selector)
	}
	return body[start : start+end+1]
}

// Matching a create's values back to a task retires the card standing in for
// it, so a task the board already carried must not be able to claim one. Saving
// a second copy of a task that already exists is an ordinary thing to do, and
// the copy being written deserves the same card as any other create until the
// server answers for it.
func TestHandlerClientStandInSurvivesATaskThatAlreadyMatchedIt(t *testing.T) {
	node := requireNode(t)
	existing := clientPlacementTask("WB-01J000000000000000000000D1", "Duplicate", core.StatusReady, core.PriorityMedium)
	created := clientPlacementTask("WB-01J000000000000000000000D2", "Duplicate", core.StatusReady, core.PriorityMedium)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	standing := tasksDocumentJSON(t, []core.Task{existing})
	both := tasksDocumentJSON(t, []core.Task{existing, created})

	program := clientDOMHarness("/tasks/new?status=ready", standing) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  let activeDocument = ` + standing + `;
  let releasePost;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => new Promise((resolve) => {
        releasePost = () => {
          activeDocument = ` + both + `;
          resolve(` + taskMutationJSON(tasksDocumentJSON(t, []core.Task{created}), "") + `);
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
  const settled = form.eventListeners.submit({ preventDefault() {} });

  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const drawn = ready.querySelectorAll(".task-card");
  if (drawn.length !== 2 || !drawn.some((node) => node.dataset.pendingCreate === "true")) {
    throw new Error("a create that matched a standing task drew " + drawn.length + " cards, want the task and its stand-in");
  }
  // A poll re-reads the board the create was typed against; the standing task
  // is still not the one being made.
  await intervalCallback();
  const polled = ready.querySelectorAll(".task-card");
  if (polled.length !== 2 || !polled.some((node) => node.dataset.pendingCreate === "true")) {
    throw new Error("a poll retired the stand-in against the task it merely matched");
  }

  releasePost();
  await settled;
  const cards = ready.querySelectorAll(".task-card");
  if (cards.length !== 2 || cards.some((node) => node.dataset.pendingCreate === "true")) {
    throw new Error("the durable create did not replace its own stand-in");
  }
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute create matching a standing task: %v\n%s", err, output)
	}
}
