package webui

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestHandlerClientNewTaskSaveReturnsToTheBoard(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000B1", "Created task", core.StatusReady, core.PriorityMedium)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	refreshed := tasksDocumentJSON(t, []core.Task{created})

	program := clientDOMHarness("/tasks/new?status=ready", tasksDocumentJSON(t, nil)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + createFetchStub(taskMutationJSON(refreshed, ""), refreshed) + `
  const toggle = findElement(main, (element) => element.id === "task-create-more");
  if (!toggle) throw new Error("the New Task form does not offer a Create more toggle");
  if (toggle.checked) throw new Error("Create more is on before the user asks for it");

  findElement(main, (element) => element.id === "task-title").value = ` + strconv.Quote(created.Title) + `;
  const form = findElement(main, (element) => element.tagName === "FORM");
  // A keyboard user submits from the Save button, so that is where the caret
  // stands when the save begins and the form is destroyed under it.
  findElement(form, (element) => hasClassToken(element, "save-button")).focus();
  await form.eventListeners.submit({ preventDefault() {} });

  if (createCalls !== 1) throw new Error("Save did not create exactly one task");
  if (historyPaths.length !== 1 || historyPaths[0] !== "/") {
    throw new Error("Save navigation = " + JSON.stringify(historyPaths) + ", want [\"/\"]");
  }
  if (historyReplacements.length !== 0) {
    throw new Error("Save replaced a history entry instead of pushing the board");
  }
  if (main.firstElementChild !== boardView) throw new Error("Save did not land on the board");
  if (findElement(main, (element) => element.id === "task-title")) {
    throw new Error("the saved task's form is still on screen");
  }

  // The board landing has the same blur the re-armed form does: the save
  // destroyed the node the user was standing on. Landing with nothing focused
  // makes a keyboard user tab from the top of the document, and tells a screen
  // reader nothing about the create having worked. The new card carries both.
  const createdCard = boardLists
    .map((list) => list.querySelectorAll(".task-card")
      .find((node) => node.dataset.taskId === ` + strconv.Quote(created.ID) + `))
    .find(Boolean);
  if (!createdCard) throw new Error("the board landing does not carry the created card");
  if (document.activeElement !== createdCard) {
    throw new Error("the board landing dropped the caret instead of handing it to the created card");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute New Task save navigation: %v\n%s", err, output)
	}
}

func TestHandlerClientCreateMoreKeepsACleanNewTaskForm(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000B2", "First task", core.StatusReady, core.PriorityMedium)
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	refreshed := tasksDocumentJSON(t, []core.Task{created})

	program := clientDOMHarness("/tasks/new?status=ready", tasksDocumentJSON(t, nil)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + createFetchStub(taskMutationJSON(refreshed, ""), refreshed) + `
  const toggle = findElement(main, (element) => element.id === "task-create-more");
  if (!toggle) throw new Error("the New Task form does not offer a Create more toggle");
  toggle.checked = true;
  toggle.eventListeners.change();

  const firstForm = findElement(main, (element) => element.tagName === "FORM");
  findElement(firstForm, (element) => element.id === "task-title").value = "First task";
  findElement(firstForm, (element) => element.id === "task-description").value = "First description";
  findElement(firstForm, (element) => element.id === "task-labels").value = "web, docs";
  await firstForm.eventListeners.submit({ preventDefault() {} });

  if (createCalls !== 1) throw new Error("Save did not create exactly one task");
  if (main.firstElementChild === boardView) throw new Error("Create more returned the user to the board");
  if (historyPaths.length !== 0) {
    throw new Error("Create more pushed " + JSON.stringify(historyPaths) + " instead of replacing the spent form");
  }
  if (historyReplacements.length !== 1 || historyReplacements[0] !== "/tasks/new?status=ready") {
    throw new Error("Create more landing = " + JSON.stringify(historyReplacements));
  }

  const secondForm = findElement(main, (element) => element.tagName === "FORM");
  if (!secondForm || secondForm === firstForm) {
    throw new Error("Create more re-presented the form that was just saved");
  }
  const title = findElement(secondForm, (element) => element.id === "task-title");
  const description = findElement(secondForm, (element) => element.id === "task-description");
  const labels = findElement(secondForm, (element) => element.id === "task-labels");
  const status = findElement(secondForm, (element) => element.id === "task-status");
  if (title.value !== "" || description.value !== "" || labels.value !== "") {
    throw new Error("Create more re-presented the saved task's values");
  }
  if (status.value !== "ready") {
    throw new Error("Create more forgot the column the last task was filed under");
  }
  const secondToggle = findElement(secondForm, (element) => element.id === "task-create-more");
  if (!secondToggle || !secondToggle.checked) {
    throw new Error("Create more did not survive the save it caused");
  }
  // The save destroyed the node the user was on, which blurs it. Re-arming a
  // form for rapid entry and dropping focus to the body would make every task
  // after the first start with a reach for the mouse.
  if (document.activeElement !== title) {
    throw new Error("Create more did not put the caret in the re-armed title");
  }

  title.value = "Second task";
  await secondForm.eventListeners.submit({ preventDefault() {} });
  if (createCalls !== 2) throw new Error("the clean form did not save a second task");
  if (historyReplacements.length !== 2 || historyPaths.length !== 0) {
    throw new Error("the second save did not re-arm the form");
  }
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute Create more save behavior: %v\n%s", err, output)
	}
}

func TestHandlerClientCreateMoreYieldsToACreateThatNeedsAttention(t *testing.T) {
	node := requireNode(t)
	created := clientPlacementTask("WB-01J000000000000000000000B3", "Created task", core.StatusReady, core.PriorityMedium)
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

  const wantPath = "/tasks/" + encodeURIComponent(` + strconv.Quote(created.ID) + `);
  if (historyPaths.length !== 1 || historyPaths[0] !== wantPath) {
    throw new Error("a create with something to report landed at " + JSON.stringify(historyPaths));
  }
  if (historyReplacements.length !== 0) {
    throw new Error("a create with something to report re-armed the New Task form");
  }
  const feedback = findElement(main, (element) => element.className === "form-status" &&
    element.textContent.includes("Task creation projection needs repair."));
  if (!feedback) throw new Error("the created task's detail did not carry the create's warning");
}, 0);
`
	command := exec.Command(node, "-e", program)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute reported create landing: %v\n%s", err, output)
	}
}

// newTaskClientScript renders the New Task route and returns the client script
// a browser would run for it, so a node harness drives the real save path
// rather than a copy of it.
func newTaskClientScript(t *testing.T, path string) string {
	t.Helper()
	handler := NewHandler(
		func(context.Context) ([]core.Task, error) { return nil, nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t),
	)
	response := request(t, handler, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
	}
	return renderedClientScript(t, response.Body.String())
}

// tasksDocumentJSON encodes a task refresh response. An absent task list is
// encoded as an empty one, because the client reads a null "tasks" member as a
// failed load and never renders the route under test.
func tasksDocumentJSON(t *testing.T, tasks []core.Task) string {
	t.Helper()
	if tasks == nil {
		tasks = []core.Task{}
	}
	return string(mustJSON(t, TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	}))
}

// taskMutationJSON is the create response for the first task of tasksJSON,
// optionally carrying the one warning a caller wants the client to report.
func taskMutationJSON(tasksJSON, warning string) string {
	mutation := `{ format: "workbook.task-mutation", version: 1, task: (` + tasksJSON + `).tasks[0]`
	if warning != "" {
		mutation += `, warnings: [{ code: "projection-update-failed", message: ` + strconv.Quote(warning) + ` }]`
	}
	return mutation + ` }`
}

// createFetchStub answers the three requests one create makes: the POST, the
// active refresh, and the deleted-task refresh that the relationship sidebar
// needs before the client will leave the form.
func createFetchStub(mutationJSON, refreshedTasksJSON string) string {
	return `
  let createCalls = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      return { ok: true, json: async () => (` + mutationJSON + `) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (url === "/api/tasks" && (options.method || "GET") === "GET") {
      return { ok: true, json: async () => (` + refreshedTasksJSON + `) };
    }
    throw new Error("unexpected fetch: " + (options.method || "GET") + " " + url);
  };
`
}
