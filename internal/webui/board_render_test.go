package webui

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board renders once a second forever. Node identity across those renders is
// the property every one of these tests is really about: a card node that
// survives a poll keeps its keyboard focus, its drag state, and the browser's
// scroll anchor, and one that is destroyed and rebuilt loses all three. The fake
// DOM has no layout engine and cannot see the scroll drift, but node identity is
// a DOM-level fact and needs none.

const (
	reconcileAlphaID   = "WB-01J0000000000000000000AA01"
	reconcileBravoID   = "WB-01J0000000000000000000BB02"
	reconcileCharlieID = "WB-01J0000000000000000000CC03"
	reconcileDeltaID   = "WB-01J0000000000000000000DD04"
)

func reconcileBoardTasks() []core.Task {
	alpha := clientPlacementTask(reconcileAlphaID, "Alpha", core.StatusReady, core.PriorityHigh)
	alpha.Description = "Draw the first card."
	alpha.Labels = []string{"web"}
	bravo := clientPlacementTask(reconcileBravoID, "Bravo", core.StatusReady, core.PriorityMedium)
	bravo.Rank = "2/1"
	charlie := clientPlacementTask(reconcileCharlieID, "Charlie", core.StatusReady, core.PriorityLow)
	charlie.Rank = "3/1"
	return []core.Task{alpha, bravo, charlie}
}

// runBoardClient renders the board page, executes its client script against the
// fake DOM, and then runs body with the board already painted.
func runBoardClient(t *testing.T, purpose string, tasks []core.Task, body string) {
	t.Helper()
	runBoardClientWithSetup(t, purpose, tasks, "", body)
}

// runBoardClientWithSetup is runBoardClient with JavaScript that runs against
// the fake DOM before the client script does, which is where a test states what
// the browser already held when the page loaded.
func runBoardClientWithSetup(t *testing.T, purpose string, tasks []core.Task, setup, body string) {
	t.Helper()
	runBoardClientAt(t, purpose, "/", tasks, setup, body)
}

// runBoardClientAt is runBoardClientWithSetup for a board opened at some URL
// other than the board itself, which is how a test states what the header does
// on a route that is not the board.
func runBoardClientAt(t *testing.T, purpose, url string, tasks []core.Task, setup, body string) {
	t.Helper()
	node := requireNode(t)
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarness(url, string(document)) + setup + script + boardReconcilePrelude + `
setTimeout(async () => {
` + body + `
}, 0);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// The presentation for a task the board already knows is reused verbatim, so a
// poll that changes nothing really does change nothing.
const boardReconcilePrelude = `
const initialTasks = taskDocument.tasks;
const taskByID = (id) => initialTasks.find((task) => task.id === id);
const serve = (tasks) => {
  taskResponse = {
    format: "workbook.tasks",
    version: 1,
    tasks,
    presentation: tasks.map((task) =>
      taskDocument.presentation.find((view) => view.taskId === task.id) ||
      { taskId: task.id, idPrefix: task.id, dependenciesComplete: 0, dependenciesTotal: 0, waitingOnDependencies: false })
  };
};
const listFor = (status) => boardLists.find((list) => list.dataset.status === status);
const cardsIn = (list) => list.querySelectorAll(".task-card");
const cardIn = (list, id) => cardsIn(list).find((node) => node.dataset.taskId === id);
const idsIn = (list) => cardsIn(list).map((node) => node.dataset.taskId);
const boardCard = (id) => boardLists.map((list) => cardIn(list, id)).find(Boolean);
`

func TestHandlerClientKeepsUnchangedCardNodesAcrossAPoll(t *testing.T) {
	runBoardClient(t, "unchanged board reconciliation", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const held = cardsIn(ready);
  if (held.length !== 3) throw new Error("board did not render three Ready cards: " + held.length);

  let moves = 0;
  let removals = 0;
  const insert = ready.insertBefore.bind(ready);
  ready.insertBefore = (child, reference) => { moves += 1; return insert(child, reference); };
  held.forEach((node) => {
    const detach = node.remove.bind(node);
    node.remove = () => { removals += 1; return detach(); };
  });

  await intervalCallback();

  const after = cardsIn(ready);
  if (after.length !== held.length) throw new Error("a poll changed the Ready card count: " + after.length);
  held.forEach((node, index) => {
    if (after[index] !== node) throw new Error("a poll replaced the unchanged card " + node.dataset.taskId);
    if (node.parentElement !== ready) throw new Error("a poll detached the unchanged card " + node.dataset.taskId);
  });
  if (moves !== 0 || removals !== 0) {
    throw new Error("a poll that changed nothing still moved " + moves + " and removed " + removals + " cards");
  }
`)
}

func TestHandlerClientUpdatesAChangedCardInPlace(t *testing.T) {
	runBoardClient(t, "in-place card update", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  const bravo = cardIn(ready, `+strconv.Quote(reconcileBravoID)+`);
  if (!alpha || !bravo) throw new Error("board did not render the Ready cards");

  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, {
    title: "Alpha renamed",
    description: "Redrawn without being rebuilt.",
    priority: "low",
    labels: ["web", "accessibility"]
  })));
  await intervalCallback();

  const redrawn = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  if (redrawn !== alpha) throw new Error("a changed task was rebuilt instead of updated in place");
  if (cardIn(ready, `+strconv.Quote(reconcileBravoID)+`) !== bravo) throw new Error("changing one card rebuilt its neighbour");
  if (!alpha.textContent.includes("Alpha renamed")) throw new Error("the card kept its old title");
  if (!alpha.textContent.includes("Redrawn without being rebuilt.")) throw new Error("the card kept its old description");
  if (!alpha.textContent.includes("accessibility")) throw new Error("the card kept its old labels");
  if (alpha.dataset.priority !== "low") throw new Error("the card kept its old priority: " + alpha.dataset.priority);
  if (alpha.getAttribute("aria-label") !== "Move task Alpha renamed from ready") {
    throw new Error("the card kept its old aria-label: " + alpha.getAttribute("aria-label"));
  }
  const priority = findElement(alpha, (element) => element.className === "priority priority--low");
  if (!priority || priority.textContent !== "low") throw new Error("the card kept its old priority styling");

  // Dropping a field removes its section rather than leaving the old text behind.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, {
    description: "",
    labels: []
  })));
  await intervalCallback();
  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("clearing a field rebuilt the card");
  if (alpha.textContent.includes("Draw the first card.")) throw new Error("the card kept a description the task no longer has");
  if (findElement(alpha, (element) => element.className === "labels")) throw new Error("the card kept labels the task no longer has");
`)
}

func TestHandlerClientAddsAndRemovesCardsWithoutDisturbingTheirNeighbours(t *testing.T) {
	runBoardClient(t, "card addition and removal", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  const bravo = cardIn(ready, `+strconv.Quote(reconcileBravoID)+`);
  const charlie = cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`);
  if (!alpha || !bravo || !charlie) throw new Error("board did not render the Ready cards");

  serve(initialTasks.filter((task) => task.id !== `+strconv.Quote(reconcileBravoID)+`));
  await intervalCallback();
  if (cardIn(ready, `+strconv.Quote(reconcileBravoID)+`)) throw new Error("a removed task kept its card");
  if (bravo.parentElement) throw new Error("a removed card stayed attached to the board");
  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("removing a card rebuilt the card before it");
  if (cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`) !== charlie) throw new Error("removing a card rebuilt the card after it");
  if (JSON.stringify(idsIn(ready)) !== JSON.stringify([alpha.dataset.taskId, charlie.dataset.taskId])) {
    throw new Error("the surviving cards are out of order: " + idsIn(ready).join(", "));
  }

  const delta = Object.assign({}, taskByID(`+strconv.Quote(reconcileCharlieID)+`), {
    id: `+strconv.Quote(reconcileDeltaID)+`,
    title: "Delta",
    rank: "4/1"
  });
  serve([taskByID(`+strconv.Quote(reconcileAlphaID)+`), delta, taskByID(`+strconv.Quote(reconcileCharlieID)+`)]);
  await intervalCallback();
  const added = cardIn(ready, `+strconv.Quote(reconcileDeltaID)+`);
  if (!added) throw new Error("an added task did not get a card");
  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("adding a card rebuilt the card before it");
  if (cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`) !== charlie) throw new Error("adding a card rebuilt the card after it");
  if (JSON.stringify(idsIn(ready)) !== JSON.stringify([alpha.dataset.taskId, added.dataset.taskId, charlie.dataset.taskId])) {
    throw new Error("the added card landed in the wrong place: " + idsIn(ready).join(", "));
  }
  if (boardCounts.find((element) => element.dataset.count === "ready").textContent !== "3") {
    throw new Error("the Ready count did not follow its cards");
  }
`)
}

func TestHandlerClientReordersAColumnByMovingItsExistingNodes(t *testing.T) {
	runBoardClient(t, "column reordering", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  const bravo = cardIn(ready, `+strconv.Quote(reconcileBravoID)+`);
  const charlie = cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`);

  serve([taskByID(`+strconv.Quote(reconcileCharlieID)+`), taskByID(`+strconv.Quote(reconcileAlphaID)+`), taskByID(`+strconv.Quote(reconcileBravoID)+`)]);
  await intervalCallback();

  const reordered = cardsIn(ready);
  if (reordered.length !== 3) throw new Error("reordering changed the Ready card count: " + reordered.length);
  if (reordered[0] !== charlie || reordered[1] !== alpha || reordered[2] !== bravo) {
    throw new Error("a reorder rebuilt cards instead of moving the ones already rendered");
  }

  // A status change moves the node between columns rather than destroying it in
  // one and building a stranger in the other.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { status: "in-progress" })));
  await intervalCallback();
  const inProgress = listFor("in-progress");
  if (cardIn(inProgress, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("a status change rebuilt the moved card");
  if (alpha.parentElement !== inProgress) throw new Error("the moved card did not land in its new column");
  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`)) throw new Error("the moved card stayed in its old column");
`)
}

func TestHandlerClientKeepsCardFocusAcrossAPoll(t *testing.T) {
	runBoardClient(t, "card focus retention", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  alpha.focus();
  if (document.activeElement !== alpha) throw new Error("the card could not take focus");

  await intervalCallback();
  if (document.activeElement !== alpha) throw new Error("a poll destroyed the focused card");
  if (alpha.parentElement !== ready) throw new Error("a poll detached the focused card");

  // Even a poll that has to move the focused card keeps focus on it, so the
  // guarantee does not rest on the diff being perfect.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { status: "done" })));
  await intervalCallback();
  const focused = document.activeElement;
  if (!focused || focused.dataset.taskId !== `+strconv.Quote(reconcileAlphaID)+`) {
    throw new Error("a poll that moved the focused card dropped its focus");
  }
  if (focused.parentElement !== listFor("done")) throw new Error("focus did not follow the card into its new column");
`)
}

func TestHandlerClientLeavesTheDraggedCardConnectedAcrossAPoll(t *testing.T) {
	runBoardClient(t, "drag survival across a poll", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: alpha, dataTransfer });
  if (!alpha.classList.contains("is-dragging")) throw new Error("dragstart did not mark the card");

  await intervalCallback();
  if (!alpha.parentElement) throw new Error("a poll during a drag detached the dragged card");
  if (!alpha.classList.contains("is-dragging")) throw new Error("a poll during a drag cleared the drag state");
  if (boardCard(`+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("a poll during a drag rebuilt the dragged card");

  // Another clone moves the task mid-drag. The node follows the model, but it is
  // the same node and it is still being dragged.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { status: "blocked" })));
  await intervalCallback();
  if (boardCard(`+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("a mid-drag status change rebuilt the dragged card");
  if (alpha.parentElement !== listFor("blocked")) throw new Error("the dragged card did not follow its task");
  if (!alpha.classList.contains("is-dragging")) throw new Error("a mid-drag status change cleared the drag state");

  // Another clone deletes it mid-drag. The board stops naming it, but detaching
  // the node the browser is dragging is exactly what breaks a drag apart.
  serve(initialTasks.filter((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+`));
  await intervalCallback();
  if (!alpha.parentElement) throw new Error("a mid-drag deletion detached the dragged card");
  if (!alpha.classList.contains("is-dragging")) throw new Error("a mid-drag deletion cleared the drag state");

  documentEventListeners.dragend({ target: alpha });
  await intervalCallback();
  if (alpha.parentElement) throw new Error("the dragged card outlived its drag and its task");
`)
}
