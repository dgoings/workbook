package webui

import (
	"context"
	"net/http"
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
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// The presentation for a task the board already knows is reused verbatim, so a
// poll that changes nothing really does change nothing. Looking a card up by ID
// wherever it currently sits is the harness's own boardCard, which searches the
// unknown-status region as well as the six columns.
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

// Alpha waits on prerequisites, so its card carries all three of the sections
// applyCard() rebuilds — a description, labels, and dependency progress — and a
// card that is redrawn needlessly has all three to lose.
const reconcileAlphaDependencies = `
taskDocument.presentation = taskDocument.presentation.map((view) => view.taskId !== "` + reconcileAlphaID + `"
  ? view
  : Object.assign({}, view, { dependenciesTotal: 2, dependenciesComplete: 1, waitingOnDependencies: true }));
`

// The test above watches the column, and proves a poll neither moves nor detaches
// an unchanged card. It cannot see inside one. applyCard() rebuilds a card's
// description, labels and dependency progress from scratch and rewrites its
// title, its priority and its attributes without touching either the column or
// the article, so a card that lost `+"`if (parts.signature === signature)`"+` would churn
// its whole contents on every poll and still satisfy every assertion there — the
// reader's selection inside a description would be dropped once a second. This
// test watches one card from the inside instead: it holds the card's sections,
// counts every write the card takes, and then changes the task to prove the
// instruments can see a write at all.
func TestHandlerClientWritesNothingInsideAnUnchangedCardAcrossAPoll(t *testing.T) {
	runBoardClientWithSetup(t, "unchanged card contents", reconcileBoardTasks(), reconcileAlphaDependencies, `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  if (!alpha) throw new Error("board did not render the Alpha card");
  const description = findElement(alpha, (element) => element.tagName === "P");
  const labels = findElement(alpha, (element) => element.className === "labels");
  const progress = findElement(alpha, (element) => hasDataKey(element, "dependencyProgress"));
  if (!description || !labels || !progress) {
    throw new Error("the card rendered no description, labels or dependency progress to hold on to");
  }
  const sections = [...alpha.children];

  // Every write the card can take: a section detached or added, an attribute or
  // data attribute rewritten, and any text or class written anywhere inside it.
  let removals = 0;
  let additions = 0;
  const writes = [];
  sections.forEach((node) => {
    const detach = node.remove.bind(node);
    node.remove = () => { removals += 1; return detach(); };
  });
  const append = alpha.append.bind(alpha);
  alpha.append = (...children) => { additions += 1; return append(...children); };
  const setAttribute = alpha.setAttribute.bind(alpha);
  alpha.setAttribute = (name, value) => { writes.push("@" + name); return setAttribute(name, value); };
  alpha.dataset = new Proxy(alpha.dataset, {
    set(target, key, value) { writes.push("data-" + String(key)); target[key] = value; return true; }
  });
  findElements(alpha, () => true).forEach((element, index) => {
    const label = element.tagName + "[" + index + "]";
    const written = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "textContent");
    Object.defineProperty(element, "textContent", {
      configurable: true,
      get() { return written.get.call(element); },
      set(value) { writes.push(label + ".textContent"); written.set.call(element, value); }
    });
    let className = element.className;
    Object.defineProperty(element, "className", {
      configurable: true,
      get() { return className; },
      set(value) { writes.push(label + ".className"); className = value; }
    });
  });

  await intervalCallback();

  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("a poll rebuilt the unchanged card");
  const after = [...alpha.children];
  if (after.length !== sections.length) throw new Error("a poll changed the unchanged card's section count: " + after.length);
  sections.forEach((node, index) => {
    if (after[index] !== node) throw new Error("a poll replaced section " + index + " of the unchanged card");
  });
  if (findElement(alpha, (element) => element.tagName === "P") !== description) throw new Error("a poll rebuilt the unchanged card's description");
  if (findElement(alpha, (element) => element.className === "labels") !== labels) throw new Error("a poll rebuilt the unchanged card's labels");
  if (findElement(alpha, (element) => hasDataKey(element, "dependencyProgress")) !== progress) {
    throw new Error("a poll rebuilt the unchanged card's dependency progress");
  }
  if (removals !== 0 || additions !== 0) {
    throw new Error("a poll that changed nothing still removed " + removals + " and added " + additions + " card sections");
  }
  if (writes.length !== 0) throw new Error("a poll that changed nothing still wrote to the card: " + writes.join(", "));

  // The watch is worth nothing if it cannot see a write, so a poll that really
  // does change the task has to trip every instrument the unchanged poll left
  // silent. This is also the churn the signature spares every other card.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { title: "Alpha renamed" })));
  await intervalCallback();
  if (cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("a changed task was rebuilt instead of updated in place");
  if (description.parentElement) throw new Error("a changed card kept its old description attached");
  if (findElement(alpha, (element) => element.tagName === "P") === description) throw new Error("a changed card kept its old description node");
  if (removals === 0 || additions === 0) throw new Error("a changed card did not rebuild its sections");
  if (!writes.length) throw new Error("the write watch saw nothing when the card really changed");
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
