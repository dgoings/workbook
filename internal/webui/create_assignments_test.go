package webui

import (
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Assignments staged on a New Task form, as a browser runs them.
//
// An assignment is recorded on the task's own ref, so like a dependency or an
// attachment it cannot be written before the task exists. The form holds what
// the reader stages and sends it to the assignment route once the create has
// answered with an ID, and a failure is reported where a failed dependency is:
// on the created task's own page, with the row still there to retry.

const createAssignmentTaskID = "WB-01J0000000000000000000CA01"

// createAssignmentProgram builds the Node program for a New Task form on a board
// that can assign, with helpers for the staged assignments panel.
func createAssignmentProgram(t *testing.T, body string) string {
	t.Helper()
	const path = "/tasks/new?status=ready"
	script := newTaskClientScript(t, path)
	created := clientPlacementTask(createAssignmentTaskID, "Task with assignees", core.StatusReady, core.PriorityMedium)
	created.Head = "head-created"
	refreshed := tasksDocumentJSON(t, []core.Task{created})
	// The identity is set before the script reads it, which is the order the
	// browser sees: the server renders the attribute and the script reads it on
	// its first pass.
	return clientDOMHarness(path, tasksDocumentJSON(t, nil)) + `
boardView.dataset.assignIdentity = ` + strconv.Quote(boardIdentity) + `;
` + script + threadPanelHelpers + `
const createdTaskID = ` + strconv.Quote(createAssignmentTaskID) + `;
const refreshedTasks = ` + refreshed + `;
async function openCreateForm() {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const form = findElement(main, (element) => element.tagName === "FORM" && hasClassToken(element, "task-layout"));
  if (!form) throw new Error("the New Task form did not render");
  return form;
}
function assignmentPanel() {
  const panel = panelSection("assignments");
  if (!panel) throw new Error("the form draws no assignments panel");
  return panel;
}
function assignmentField() {
  return findElement(assignmentPanel(), (element) => element.id === "assignment-to");
}
function stageAssignment(value) {
  assignmentField().value = value;
  const form = findElement(assignmentPanel(), (element) => hasDataKey(element, "assignmentForm"));
  return form.eventListeners.submit({ preventDefault() {} });
}
function stagedAssignmentRows() { return panelRows("assignments"); }
function countText() {
  const line = findElement(assignmentPanel(), (element) => hasDataKey(element, "panelCount"));
  return line ? line.textContent : "";
}
function assignmentWrites() {
  return fetchCalls.filter((call) => call.options.method === "POST" &&
    call.url === "/api/tasks/" + encodeURIComponent(createdTaskID) + "/assignments");
}
function createWrites() {
  return fetchCalls.filter((call) => call.options.method === "POST" && call.url === "/api/tasks");
}
// Answers the create, the assignment writes (refusing whatever refuse names),
// and the reads the landing makes.
function stubCreate(refuse = () => "") {
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    const method = options.method || "GET";
    if (url === "/api/tasks" && method === "POST") {
      return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: refreshedTasks.tasks[0] }) };
    }
    if (url === "/api/tasks/" + encodeURIComponent(createdTaskID) + "/assignments" && method === "POST") {
      const refusal = refuse(JSON.parse(options.body));
      if (refusal) {
        return { ok: false, json: async () => ({ format: "workbook.error", version: 1, error: { category: "validation", message: refusal } }) };
      }
      return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: refreshedTasks.tasks[0], assignments: [] }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (url === "/api/tasks" && method === "GET") {
      return { ok: true, json: async () => refreshedTasks };
    }
    throw new Error("unexpected fetch: " + method + " " + url);
  };
}
function typeTitle(form, value) {
  findElement(form, (element) => element.id === "task-title").value = value;
}
` + body
}

// The create form offers the same assignment control the task's own page does,
// and what it is given is held rather than sent: there is no task to assign yet.
// An empty list is the heading and the control with nothing said about it.
func TestHandlerClientStagesAssignmentsOnTheCreateForm(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  const sidebar = findElement(form, (element) => hasClassToken(element, "task-sidebar"));
  if (!sidebar.contains(assignmentPanel())) throw new Error("the staged assignments are not in the sidebar");
  if (stagedAssignmentRows().length !== 0) throw new Error("an untouched form staged an assignment");
  if (countText() !== "") throw new Error("an empty list still counts itself: " + JSON.stringify(countText()));
  const empty = findElement(assignmentPanel(), (element) => hasDataKey(element, "panelEmpty"));
  if (empty && (!empty.hidden || empty.textContent)) throw new Error("an empty list still explains itself: " + JSON.stringify(empty.textContent));
  if (assignmentField().placeholder !== `+strconv.Quote(boardIdentity)+`) {
    throw new Error("the field does not say who a blank assigns: " + JSON.stringify(assignmentField().placeholder));
  }

  await stageAssignment("sam@example.com/impl-2");
  // Blank is this board's own identity, as it is on the task page.
  await stageAssignment("   ");
  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("staging sent a write before the task existed");
  }
  const rows = stagedAssignmentRows();
  if (rows.length !== 2) throw new Error("staged rows = " + rows.length);
  if (!rows[0].textContent.includes("sam@example.com") || !rows[0].textContent.includes("/impl-2")) {
    throw new Error("the first row = " + JSON.stringify(rows[0].textContent));
  }
  if (!rows[1].textContent.includes(`+strconv.Quote(boardIdentity)+`)) {
    throw new Error("a blank assignment does not name who it assigns: " + JSON.stringify(rows[1].textContent));
  }
  if (assignmentField().value !== "") throw new Error("the field kept the value it staged");
  if (countText() !== "2 staged") throw new Error("the count = " + JSON.stringify(countText()));

  // Staging one twice is one assignment, and the reader is told so.
  await stageAssignment("sam@example.com/impl-2");
  if (stagedAssignmentRows().length !== 2) throw new Error("a duplicate was staged twice");
  if (!panelStatusText("assignments").includes("already staged")) {
    throw new Error("the duplicate was not reported: " + JSON.stringify(panelStatusText("assignments")));
  }

  rowControl(stagedAssignmentRows()[0], "Remove").eventListeners.click();
  const left = stagedAssignmentRows();
  if (left.length !== 1 || !left[0].textContent.includes(`+strconv.Quote(boardIdentity)+`)) {
    throw new Error("Remove took out the wrong row: " + left.map((row) => row.textContent).join("|"));
  }
  rowControl(left[0], "Remove").eventListeners.click();
  if (stagedAssignmentRows().length !== 0 || countText() !== "") {
    throw new Error("the emptied list did not return to its empty state");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute staged assignments: %v\n%s", err, output)
	}
}

// A board that cannot assign stages nothing: the create form has no control a
// task's own page would not offer.
func TestHandlerClientOffersNoStagedAssignmentsOnABoardThatCannotAssign(t *testing.T) {
	t.Parallel()
	runTaskFormClient(t, "no staged assignments without an identity", `
  if (findElement(form, (element) => element.dataset.taskPanel === "assignments")) {
    throw new Error("a board with no identity offered to stage an assignment");
  }
`)
}

// Saving sends the create first and then each staged assignment, in order, to
// the task it made — with the value the reader typed, blank meaning this board's
// identity, exactly what the task page's control sends.
func TestHandlerClientAssignsStagedAssigneesAfterTheCreate(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  stubCreate();
  typeTitle(form, "Task with assignees");
  await stageAssignment("sam@example.com");
  await stageAssignment("");
  await form.eventListeners.submit({ preventDefault() {} });

  if (createWrites().length !== 1) throw new Error("creates = " + createWrites().length);
  const writes = assignmentWrites();
  if (writes.length !== 2) throw new Error("assignment writes = " + writes.length);
  const create = fetchCalls.indexOf(createWrites()[0]);
  if (writes.some((call) => fetchCalls.indexOf(call) < create)) {
    throw new Error("an assignment was sent before the task existed");
  }
  const bodies = writes.map((call) => JSON.parse(call.options.body));
  if (bodies[0].to !== "sam@example.com" || bodies[1].to !== "") {
    throw new Error("the writes carried " + JSON.stringify(bodies));
  }
  if (JSON.parse(createWrites()[0].options.body).assignments !== undefined) {
    throw new Error("the create itself carried the assignments");
  }
  // Nothing to report, so the create lands where one with nothing staged does.
  if (historyPaths.length !== 1 || historyPaths[0] !== "/") {
    throw new Error("the landing = " + JSON.stringify(historyPaths));
  }
  if (main.firstElementChild !== boardView) throw new Error("the create did not land on the board");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute staged assignment create: %v\n%s", err, output)
	}
}

// An assignment the server refuses after the create is reported the way a
// refused dependency is: the task exists, so the reader lands on its page, told
// what did not save, with the row still there carrying the reason and a Retry.
func TestHandlerClientReportsAStagedAssignmentTheCreateCouldNotSave(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  stubCreate((body) => body.to === "dylan" ? "assignment principal \"dylan\" must be an email address" : "");
  typeTitle(form, "Task with assignees");
  await stageAssignment("sam@example.com");
  await stageAssignment("dylan");
  await form.eventListeners.submit({ preventDefault() {} });

  if (assignmentWrites().length !== 2) throw new Error("assignment writes = " + assignmentWrites().length);
  const wantPath = "/tasks/" + encodeURIComponent(createdTaskID);
  if (historyPaths[historyPaths.length - 1] !== wantPath) {
    throw new Error("a create with a failed assignment landed at " + JSON.stringify(historyPaths));
  }
  const feedback = findElement(main, (element) => hasDataKey(element, "saveStatus"));
  if (!feedback || !feedback.textContent.includes("Task created, but some assignments were not saved.")) {
    throw new Error("the task page did not say what was lost: " + JSON.stringify(feedback && feedback.textContent));
  }
  const drafts = stagedAssignmentRows().filter((row) => hasDataKey(row, "assignmentDraft"));
  if (drafts.length !== 1 || !drafts[0].textContent.includes("dylan")) {
    throw new Error("the failed assignment is not on the task page: " + stagedAssignmentRows().map((row) => row.textContent).join("|"));
  }
  if (!drafts[0].textContent.includes("must be an email address") || !drafts[0].textContent.includes("Not saved")) {
    throw new Error("the failed row does not say why: " + JSON.stringify(drafts[0].textContent));
  }

  // Retried against the same task, naming the tip the page holds; a success
  // takes the row away.
  stubCreate();
  const before = assignmentWrites().length;
  await rowControl(drafts[0], "Retry").eventListeners.click();
  const retried = assignmentWrites().slice(before);
  if (retried.length !== 1) throw new Error("Retry sent " + retried.length + " writes");
  const body = JSON.parse(retried[0].options.body);
  if (body.to !== "dylan" || body.expectedHead !== "head-created") {
    throw new Error("Retry sent " + JSON.stringify(body));
  }
  if (stagedAssignmentRows().some((row) => hasDataKey(row, "assignmentDraft"))) {
    throw new Error("a retried assignment is still drawn as unsaved");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute failed staged assignment: %v\n%s", err, output)
	}
}

// A failed row the reader decides against is removed without a write, the way
// a failed dependency draft is.
func TestHandlerClientRemovesAFailedStagedAssignmentWithoutAWrite(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  stubCreate(() => "refused");
  typeTitle(form, "Task with assignees");
  await stageAssignment("sam@example.com");
  await form.eventListeners.submit({ preventDefault() {} });
  const drafts = stagedAssignmentRows().filter((row) => hasDataKey(row, "assignmentDraft"));
  if (drafts.length !== 1) throw new Error("the failed assignment is not on the task page");
  const before = fetchCalls.length;
  rowControl(drafts[0], "Remove").eventListeners.click();
  if (fetchCalls.slice(before).some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("removing an unsaved row sent a write");
  }
  if (stagedAssignmentRows().length !== 0) throw new Error("the row was not removed");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute removing a failed staged assignment: %v\n%s", err, output)
	}
}

// Create more re-arms a clean form, and clean includes the assignments: the
// next task starts with nobody staged.
func TestHandlerClientCreateMoreClearsStagedAssignments(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  stubCreate();
  const toggle = findElement(main, (element) => element.id === "task-create-more");
  toggle.checked = true;
  toggle.eventListeners.change();
  typeTitle(form, "Task with assignees");
  await stageAssignment("sam@example.com");
  await form.eventListeners.submit({ preventDefault() {} });

  if (assignmentWrites().length !== 1) throw new Error("assignment writes = " + assignmentWrites().length);
  if (historyReplacements.length !== 1 || historyReplacements[0] !== "/tasks/new?status=ready") {
    throw new Error("Create more landing = " + JSON.stringify(historyReplacements));
  }
  const next = findElement(main, (element) => element.tagName === "FORM" && hasClassToken(element, "task-layout"));
  if (!next || next === form) throw new Error("Create more did not re-arm the form");
  if (stagedAssignmentRows().length !== 0) throw new Error("the re-armed form still stages an assignment");
  if (assignmentField().value !== "") throw new Error("the re-armed field kept a value");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute Create more with staged assignments: %v\n%s", err, output)
	}
}

// When an attachment fails too, the form stays to retry it, as it always has —
// and says the assignments did not save either, handing them to the task's page
// where Open task leads.
func TestHandlerClientNamesFailedAssignmentsWhileHoldingForAttachments(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, fileHarness+`
setTimeout(async () => {
  const form = await openCreateForm();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    const method = options.method || "GET";
    if (url === "/api/tasks" && method === "POST") {
      return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: refreshedTasks.tasks[0] }) };
    }
    if (method === "POST" && url.endsWith("/assignments")) {
      return { ok: false, json: async () => ({ format: "workbook.error", version: 1, error: { category: "validation", message: "refused assignment" } }) };
    }
    if (method === "POST" && url.endsWith("/attachments")) {
      return { ok: false, json: async () => ({ format: "workbook.error", version: 1, error: { category: "validation", message: "refused attachment" } }) };
    }
    if (url === "/api/tasks?deleted=true") return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    if (url === "/api/tasks" && method === "GET") return { ok: true, json: async () => refreshedTasks };
    throw new Error("unexpected fetch: " + method + " " + url);
  };
  typeTitle(form, "Task with assignees");
  await stageAssignment("sam@example.com");
  const chooser = findElement(panelSection("attachments"), (element) => element.id === "attachment-file");
  chooseFiles(chooser, [new TestFile("trace.log", 20, "trace")]);
  await submitForm(attachmentForm("file"));
  await form.eventListeners.submit({ preventDefault() {} });

  if (main.firstElementChild.tagName !== "SECTION" || !findElement(main, (element) => element === form)) {
    throw new Error("a create owing attachments left its form");
  }
  const said = findElement(main, (element) => hasDataKey(element, "saveStatus")).textContent;
  if (!said.includes("Some assignments were not saved either")) {
    throw new Error("the held form does not name the failed assignments: " + JSON.stringify(said));
  }
  if (stagedAssignmentRows().length !== 0) throw new Error("the spent form still stages the assignments");

  const open = findElement(main, (element) => hasDataKey(element, "openCreatedTask"));
  await documentEventListeners.click({
    target: open, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() { this.defaultPrevented = true; }
  });
  const drafts = stagedAssignmentRows().filter((row) => hasDataKey(row, "assignmentDraft"));
  if (drafts.length !== 1 || !drafts[0].textContent.includes("refused assignment")) {
    throw new Error("the task page does not hold the failed assignment: " + stagedAssignmentRows().map((row) => row.textContent).join("|"));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute failed assignments held with attachments: %v\n%s", err, output)
	}
}
