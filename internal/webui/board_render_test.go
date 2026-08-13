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

  // Every write applyCard() can make to a card that already exists: a section
  // detached or added, and — on the card and on every node already inside it, not
  // just the card itself — a text, class, attribute or data attribute rewritten.
  // Descendants have to be watched because applyCard() writes to them directly:
  // it sets the failure region's data-visible without going through the article.
  // The card's draggable flag is watched separately, being the one thing
  // applyCard() writes that is a plain property rather than one of those. Nodes
  // built after this point are new sections, and the addition count sees those.
  let removals = 0;
  let additions = 0;
  const writes = [];
  sections.forEach((node) => {
    const detach = node.remove.bind(node);
    node.remove = () => { removals += 1; return detach(); };
  });
  const append = alpha.append.bind(alpha);
  alpha.append = (...children) => { additions += 1; return append(...children); };
  // A rebuilt section is inserted before the card's Restore control rather than
  // appended past it, because that control is built once with the card and has
  // to stay its last child. Both writes are additions and the watch has to see
  // both, or a card that rebuilt every section would count as unchanged.
  const insert = alpha.insertBefore.bind(alpha);
  alpha.insertBefore = (child, reference) => { additions += 1; return insert(child, reference); };
  let draggable = alpha.draggable;
  Object.defineProperty(alpha, "draggable", {
    configurable: true,
    get() { return draggable; },
    set(value) { writes.push("ARTICLE.draggable"); draggable = value; }
  });
  findElements(alpha, () => true).forEach((element, index) => {
    const label = element.tagName + "[" + index + "]";
    const setAttribute = element.setAttribute.bind(element);
    element.setAttribute = (name, value) => { writes.push(label + "@" + name); return setAttribute(name, value); };
    element.dataset = new Proxy(element.dataset, {
      set(target, key, value) { writes.push(label + ".data-" + String(key)); target[key] = value; return true; }
    });
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
  // Named rather than counted, because these three are the instruments a reader
  // would most reasonably assume are there without checking: the write applyCard()
  // makes to a node inside the card rather than to the card, the card's plain
  // draggable property, and an attribute set through setAttribute().
  if (!writes.some((entry) => entry.endsWith(".data-visible"))) {
    throw new Error("the watch missed the data attribute applyCard() writes inside the card: " + writes.join(", "));
  }
  if (!writes.includes("ARTICLE.draggable")) {
    throw new Error("the watch missed the card's draggable flag: " + writes.join(", "));
  }
  if (!writes.some((entry) => entry.endsWith("@aria-label"))) {
    throw new Error("the watch missed the card's aria-label: " + writes.join(", "));
  }
`)
}

// The guard is a claim about both directions: it must return early when nothing
// the card draws has changed, and it must stop returning early the moment
// something has. The test above covers the first direction, and on its own it
// leaves the second one open — seven of the ten inputs to cardSignature() could
// be deleted with the whole package still green, which is the failure that
// actually loses a reader's data: a description-only, labels-only or
// dependency-progress-only edit made in another clone would never reach the
// board, and nothing would say so.
//
// So this walks the signature one input at a time. Each step changes exactly one
// of them on top of the state the board was last served, which is what makes a
// failure name its own field: a poll that differs in one input redraws the card
// or the guard swallowed that input. The tenth input, the card's failure report,
// is the one a poll cannot deliver — it comes from a refused mutation, and
// board_failure_report_test.go drives it.
func TestHandlerClientRedrawsACardForEverySignatureFieldThatChanges(t *testing.T) {
	runBoardClientWithSetup(t, "changed card signature fields", reconcileBoardTasks(), reconcileAlphaDependencies, `
  const alpha = boardCard(`+strconv.Quote(reconcileAlphaID)+`);
  if (!alpha) throw new Error("board did not render the Alpha card");
  const visibleID = () => {
    const code = findElement(alpha, (element) => element.tagName === "CODE");
    return code ? code.textContent : "";
  };

  // What the board is drawing Alpha from right now. A step edits one field of
  // one of these and serves both again, so consecutive polls differ by exactly
  // the field under test and by nothing else.
  let task = taskByID(`+strconv.Quote(reconcileAlphaID)+`);
  let view = taskDocument.presentation.find((entry) => entry.taskId === `+strconv.Quote(reconcileAlphaID)+`);
  const serveAlpha = () => {
    taskResponse = {
      format: "workbook.tasks",
      version: 1,
      tasks: initialTasks.map((entry) => entry.id === `+strconv.Quote(reconcileAlphaID)+` ? task : entry),
      presentation: taskDocument.presentation.map((entry) =>
        entry.taskId === `+strconv.Quote(reconcileAlphaID)+` ? view : entry)
    };
  };
  const changeTask = (fields) => { task = Object.assign({}, task, fields); serveAlpha(); };
  const changeView = (fields) => { view = Object.assign({}, view, fields); serveAlpha(); };

  const steps = [
    { field: "task.title",
      change: () => changeTask({ title: "Alpha renamed" }),
      drawn: () => alpha.textContent.includes("Alpha renamed") },
    { field: "task.description",
      change: () => changeTask({ description: "Only the description moved." }),
      drawn: () => alpha.textContent.includes("Only the description moved.") },
    { field: "task.priority",
      change: () => changeTask({ priority: "low" }),
      drawn: () => alpha.dataset.priority === "low" && alpha.textContent.includes("low") },
    { field: "task.labels",
      change: () => changeTask({ labels: ["web", "accessibility"] }),
      drawn: () => alpha.textContent.includes("accessibility") },
    // The prefix shortens when the task that forced a longer one goes away.
    { field: "view.idPrefix",
      change: () => changeView({ idPrefix: "WB-01J00000" }),
      drawn: () => visibleID() === "WB-01J00000" && alpha.dataset.idPrefix === "WB-01J00000" },
    { field: "view.dependenciesComplete",
      change: () => changeView({ dependenciesComplete: 2 }),
      drawn: () => alpha.textContent.includes("2 of 2 prerequisites complete") },
    { field: "view.dependenciesTotal",
      change: () => changeView({ dependenciesTotal: 3 }),
      drawn: () => alpha.textContent.includes("2 of 3 prerequisites complete") },
    { field: "view.waitingOnDependencies",
      change: () => changeView({ waitingOnDependencies: false }),
      drawn: () => !alpha.textContent.includes("Waiting on dependencies") },
    { field: "task.status",
      change: () => changeTask({ status: "in-progress" }),
      drawn: () => alpha.parentElement === listFor("in-progress") }
  ];

  for (const step of steps) {
    step.change();
    await intervalCallback();
    if (boardCard(`+strconv.Quote(reconcileAlphaID)+`) !== alpha) {
      throw new Error("changing " + step.field + " rebuilt the card instead of redrawing it");
    }
    if (!step.drawn()) {
      throw new Error("changing " + step.field + " left the card drawn from the old value: " + alpha.textContent);
    }
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

// The test above proves the neighbours of an added or removed card keep their
// nodes. Keeping the node is not the whole cost: reconcile() compares each entry
// with whatever node stands at that index, so a card still attached to a column
// the model has stopped naming pushes every card below it one index down, and
// each of them takes an insertBefore to correct an offset nothing asked for. The
// node survives, but a browser blurs a node it re-inserts, so on a long column
// that is a focusout/focusin once for every reader parked below the card that
// left. This test instruments the column a card leaves — first by changing
// status, then by being deleted — and requires that its neighbours cost nothing,
// while the column that gains the card pays the one move it really needs.
func TestHandlerClientLeavesNeighboursAloneWhenACardLeavesAColumn(t *testing.T) {
	runBoardClient(t, "departed card sweep", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const done = listFor("done");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  const bravo = cardIn(ready, `+strconv.Quote(reconcileBravoID)+`);
  const charlie = cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`);
  if (!alpha || !bravo || !charlie) throw new Error("board did not render the Ready cards");

  let readyMoves = 0;
  let doneMoves = 0;
  const insertReady = ready.insertBefore.bind(ready);
  ready.insertBefore = (child, reference) => { readyMoves += 1; return insertReady(child, reference); };
  const insertDone = done.insertBefore.bind(done);
  done.insertBefore = (child, reference) => { doneMoves += 1; return insertDone(child, reference); };

  // Another clone moves the top card out of the column. Bravo and Charlie did
  // not move relative to the model and must not be touched.
  serve(initialTasks.map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { status: "done" })));
  await intervalCallback();

  if (cardIn(done, `+strconv.Quote(reconcileAlphaID)+`) !== alpha) throw new Error("the departed card was rebuilt in its new column");
  if (cardIn(ready, `+strconv.Quote(reconcileBravoID)+`) !== bravo) throw new Error("a departing card rebuilt the card after it");
  if (cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`) !== charlie) throw new Error("a departing card rebuilt the card below it");
  if (JSON.stringify(idsIn(ready)) !== JSON.stringify([bravo.dataset.taskId, charlie.dataset.taskId])) {
    throw new Error("the surviving cards are out of order: " + idsIn(ready).join(", "));
  }
  if (readyMoves !== 0) throw new Error("a card leaving the column re-inserted " + readyMoves + " of its neighbours");
  // The instrument can see a move: the column that gained the card pays exactly
  // one, so the count above is a fact about the column and not a dead counter.
  if (doneMoves !== 1) throw new Error("the receiving column took " + doneMoves + " moves for one arriving card");

  // A deletion is the same departure without a destination.
  readyMoves = 0;
  serve(initialTasks
    .filter((task) => task.id !== `+strconv.Quote(reconcileBravoID)+`)
    .map((task) => task.id !== `+strconv.Quote(reconcileAlphaID)+` ? task : Object.assign({}, task, { status: "done" })));
  await intervalCallback();

  if (bravo.parentElement) throw new Error("a deleted card stayed attached to the board");
  if (cardIn(ready, `+strconv.Quote(reconcileCharlieID)+`) !== charlie) throw new Error("a deleted card rebuilt the card below it");
  if (readyMoves !== 0) throw new Error("a deleted card re-inserted " + readyMoves + " of its neighbours");
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
