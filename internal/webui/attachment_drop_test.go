package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Files dropped onto the attachment controls, as a browser runs them.
//
// Two things are being pinned here and they pull in opposite directions. A file
// dragged in from the desktop has to be taken by the attachment panel and by
// nothing else; a card dragged across the board has to be taken by the board and
// by nothing else. Everything below is one or the other, and the two tests that
// cross the streams are the ones that matter most.

// dropHarness is the browser drag machinery the fake DOM does not have: the
// transfers a file drag and a card drag carry, and an event shaped like the one
// a browser dispatches.
const dropHarness = `
// A file drag. items is what a browser gives for a real drop and is where a
// folder is told from a file; files is the older list, and both are present on
// a real DataTransfer.
function fileTransfer(files, folders = 0) {
  const items = files.map((file) => ({
    kind: "file",
    getAsFile: () => file,
    webkitGetAsEntry: () => ({ isDirectory: false })
  }));
  for (let index = 0; index < folders; index += 1) {
    items.push({
      kind: "file",
      getAsFile: () => null,
      webkitGetAsEntry: () => ({ isDirectory: true })
    });
  }
  return { types: ["Files"], items, files, dropEffect: "", effectAllowed: "", setData() {} };
}
// A file drag from a browser that hands over no item list at all, so the older
// files list is the only thing to read.
function legacyFileTransfer(files) {
  return { types: ["Files"], files, dropEffect: "", effectAllowed: "", setData() {} };
}
// The board's own drag: the page put text/plain on it and no Files anywhere.
function cardTransfer() {
  return { types: ["text/plain"], dropEffect: "", effectAllowed: "", setData() {} };
}
function dropZone() {
  const zone = findElement(main, (element) => hasDataKey(element, "attachmentDrop"));
  if (!zone) throw new Error("this page draws no attachment drop zone");
  // A box for the cursor to be inside or outside of. Without one every
  // coordinate reads as the origin, which is inside a zero-sized rect.
  zone.rect = { top: 100, right: 400, bottom: 600, left: 100, width: 300 };
  return zone;
}
function dragEventOn(zone, transfer, extra) {
  const event = {
    target: zone,
    dataTransfer: transfer,
    // Inside the zone's box unless a test says otherwise.
    clientX: 200,
    clientY: 200,
    relatedTarget: null,
    prevented: 0,
    preventDefault() { event.prevented += 1; }
  };
  return Object.assign(event, extra || {});
}
function dropState(zone) { return zone.dataset.dropState || ""; }
// The fake DOM records window timers rather than running them, so a test about
// one has to run it. Only the zone's departure grace is run here; everything
// else this page schedules is somebody else's business.
const dropDepartureGrace = 250;
const dropIdleGrace = 3000;
function pendingDepartureTimers(delay = dropDepartureGrace) {
  return windowTimeouts.filter((timer) =>
    !timer.canceled && !timer.ran && timer.delay === delay);
}
function runDepartureTimers(delay = dropDepartureGrace) {
  const due = pendingDepartureTimers(delay);
  due.forEach((timer) => { timer.ran = true; timer.callback(); });
  return due.length;
}
// A cancelled or abandoned external drag, as every browser reports it: a leave
// that names nothing, with the cursor still inside the zone. No dragend follows
// it, because the file came from outside the browser, and no drop follows it
// either.
function cancelShapedLeave(zone, transfer) {
  return dragEventOn(zone, transfer, { relatedTarget: null, clientX: 200, clientY: 200 });
}
`

// Acceptance is one decision and both events ask it.
//
// This is the defect this repo has a name for. A browser takes the accept flag
// from whichever of dragenter and dragover it dispatched last, and Chrome
// dispatches dragenter rather than dragover whenever the element under the
// cursor changes — which inside a panel of rows and inputs is most of the time.
// A zone that answered only dragover would have its acceptance decided by
// whichever event happened to land last, and a file released at the wrong moment
// would be handed to the browser, which navigates the window to it.
func TestHandlerClientAcceptsAFileDragOnBothDragEnterAndDragOver(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();

  for (const which of ["dragenter", "dragover"]) {
    if (typeof zone.eventListeners[which] !== "function") {
      throw new Error("the zone answers no " + which + ", so its acceptance is decided by whichever " +
        "of the two the browser happened to dispatch last");
    }
    zone.dataset.dropState = "";
    const event = dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")]));
    zone.eventListeners[which](event);
    if (event.prevented !== 1) {
      throw new Error(which + " did not accept the drag, so the browser would take the drop");
    }
    if (event.dataTransfer.dropEffect !== "copy") {
      throw new Error(which + " set dropEffect " + JSON.stringify(event.dataTransfer.dropEffect));
    }
    if (dropState(zone) !== "active") {
      throw new Error(which + " did not light the zone: " + JSON.stringify(dropState(zone)));
    }
  }

  // And the two agree about a drag they do not take, which is the other half of
  // one decision: a zone that accepted on one event and refused on the other
  // would be deciding by whichever arrived last.
  for (const which of ["dragenter", "dragover"]) {
    delete zone.dataset.dropState;
    const event = dragEventOn(zone, cardTransfer());
    zone.eventListeners[which](event);
    if (event.prevented !== 0) throw new Error(which + " accepted a drag carrying no files");
    if (dropState(zone) !== "") throw new Error(which + " lit the zone for a card drag");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the drag acceptance parity: %v\n%s", err, output)
	}
}

// A card dragged over the attachment panel is not a file, and the panel leaves
// it entirely alone — no highlight, no preventDefault, nothing swallowed. The
// board's machinery has to see exactly what it saw before this zone existed.
func TestHandlerClientLeavesACardDragAloneOverTheAttachmentZone(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();
  const dropped = dragEventOn(zone, cardTransfer());
  zone.eventListeners.drop(dropped);
  if (dropped.prevented !== 0) {
    throw new Error("the attachment zone swallowed a drop that was not carrying files");
  }
  if (stagedRows().length !== 0) throw new Error("a card drop staged something");
  if (panelStatusText("attachments") !== "") {
    throw new Error("a card drop reported into the attachment panel: " +
      JSON.stringify(panelStatusText("attachments")));
  }
  // A drag with no dataTransfer at all is not a file drag either, and must not
  // throw its way out of a handler the board is relying on.
  const bare = dragEventOn(zone, null);
  zone.eventListeners.dragover(bare);
  zone.eventListeners.drop(bare);
  if (bare.prevented !== 0) throw new Error("a transfer-less drag was accepted");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the card drag over the zone: %v\n%s", err, output)
	}
}

// The other direction, and the one with a card's write on the end of it: a file
// dragged over a board column must not be treated as a card being moved.
//
// activeDrag is usually enough to say so, since this page is the only thing that
// sets it — but a dragend that never arrived leaves it standing, and then a file
// dropped on a column would move a card the reader had finished with. So the
// drag is asked what it is carrying, and this pins the answer against exactly
// that state: a live activeDrag with a file drag on top of it.
func TestHandlerClientLeavesAFileDragAloneOverTheBoard(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask("WB-01J0000000000000000000DR01", "A card", core.StatusReady, core.PriorityMedium)
	tasks := []core.Task{task}
	document := tasksDocumentJSON(t, tasks)
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	program := clientDOMHarness("/", document) + script + `
` + fileHarness + `
` + dropHarness + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const card = ready.querySelectorAll(".task-card")[0];
  if (!card) throw new Error("the board drew no card to drag");

  // A card drag is started and deliberately never ended, which is the state
  // that makes this dangerous.
  documentEventListeners.dragstart({ target: card, dataTransfer: cardTransfer() });

  const over = dragEventOn(ready, fileTransfer([new TestFile("shot.png", 12, "x")]), { target: card });
  documentEventListeners.dragover(over);
  if (over.prevented !== 0) {
    throw new Error("a file dragged over a column was accepted as a card move");
  }
  const entered = dragEventOn(ready, fileTransfer([new TestFile("shot.png", 12, "x")]), { target: card });
  documentEventListeners.dragenter(entered);
  if (entered.prevented !== 0) {
    throw new Error("dragenter accepted a file drag as a card move");
  }

  const dropped = dragEventOn(ready, fileTransfer([new TestFile("shot.png", 12, "x")]), { target: card });
  await documentEventListeners.drop(dropped);
  if (dropped.prevented !== 0) throw new Error("a file dropped on a column was taken as a card move");
  const writes = fetchCalls.filter((call) => (call.options.method || "GET") !== "GET");
  if (writes.length !== 0) {
    throw new Error("a file dropped on the board wrote to a task: " +
      JSON.stringify(writes.map((call) => call.url)));
  }

  // And the card drag itself still works, which is the regression this guards.
  const cardDrop = dragEventOn(ready, cardTransfer(), { target: card, clientY: 10 });
  documentEventListeners.dragover(cardDrop);
  if (cardDrop.prevented === 0) {
    throw new Error("the types check broke the card drag it was meant to leave alone");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the file drag over the board: %v\n%s", err, output)
	}
}

// A drop stages every file it carried, and each one is asked the same questions
// a chosen file is asked — because it is the same call. The refusal names the
// file that caused it and keeps everything staged before it.
func TestHandlerClientStagesFilesDroppedOnTheCreateForm(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();

  const dropped = dragEventOn(zone, fileTransfer([
    new TestFile("first.log", 1024, "a"),
    new TestFile("second.log", 2048, "b")
  ]));
  await zone.eventListeners.drop(dropped);
  if (dropped.prevented !== 1) throw new Error("the drop was not taken");
  const names = stagedNames();
  if (names.length !== 2 || names[0] !== "first.log" || names[1] !== "second.log") {
    throw new Error("the drop staged " + JSON.stringify(names));
  }
  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("a drop on the create form sent a write before the task existed");
  }

  // The ceilings are the chooser's ceilings, because the door is the chooser's
  // door — and so is the contract. A run stops at the first file it refuses:
  // everything before it is kept, the refused one is named, and everything
  // after it is never looked at. That is not a fan-out and is not meant to be;
  // it is what acceptFiles has always done, and a drop that behaved differently
  // would be a second rule on one door.
  const mixed = dragEventOn(zone, fileTransfer([
    new TestFile("fine.log", 16, "c"),
    new TestFile("enormous.bin", `+strconv.Itoa(core.MaxAttachmentFileBytes+1)+`, "x"),
    new TestFile("never.log", 16, "d")
  ]));
  await zone.eventListeners.drop(mixed);
  const after = stagedNames();
  if (after.length !== 3 || after[2] !== "fine.log") {
    throw new Error("the run did not stop at the refused file: " + JSON.stringify(after));
  }
  if (after.includes("never.log")) {
    throw new Error("a file after the refused one was evaluated: " + JSON.stringify(after));
  }
  const reported = panelStatusText("attachments");
  if (!reported.includes("enormous.bin") || !reported.includes("attach a link instead")) {
    throw new Error("the drop's refusal does not name the file: " + JSON.stringify(reported));
  }
  // The highlight does not survive the drop that consumed it.
  if (dropState(zone) !== "") throw new Error("the zone stayed lit after the drop");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the create-form drop: %v\n%s", err, output)
	}
}

// The same drop on a task that already exists uploads, because that is what
// acceptFiles does on that surface. One write per file, in the order they were
// dropped, to the task the page is showing.
func TestHandlerClientUploadsFilesDroppedOnATaskPage(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	program := threadPageProgram(t, []core.Task{task}, `
`+fileHarness+`
`+dropHarness+`
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const zone = dropZone();
  const dropped = dragEventOn(zone, fileTransfer([
    new TestFile("one.txt", 5, "one"),
    new TestFile("two.txt", 5, "two")
  ]));
  await zone.eventListeners.drop(dropped);

  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 2) {
    throw new Error("dropped files sent " + writes.length + " uploads; panel says " +
      JSON.stringify(panelStatusText("attachments")));
  }
  const sent = writes.map((call) => JSON.parse(call.options.body));
  if (sent[0].name !== "one.txt" || sent[1].name !== "two.txt") {
    throw new Error("the uploads went out of order: " + JSON.stringify(sent.map((body) => body.name)));
  }
  if (sent.some((body) => body.kind !== "file")) throw new Error("a dropped file was not sent as a file");
  if (Buffer.from(sent[0].content, "base64").toString() !== "one") {
    throw new Error("a dropped file's bytes did not survive");
  }
  if (writes.some((call) => call.url !==
      "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) + "/attachments")) {
    throw new Error("a dropped file went to the wrong task");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task-page drop: %v\n%s", err, output)
	}
}

// A drop while the create run is walking the list is refused exactly as a
// mid-run add is, and said out loud — a drop has no disabled button to have
// been stopped by, so silence would read as the files having been taken.
//
// It is still accepted at the browser level while refused at ours, which is the
// only safe combination: a zone that stopped answering the drag would hand the
// drop to the browser, and the browser navigates the window to the file. Over a
// form holding staged files that is the worst outcome available.
func TestHandlerClientRefusesADropWhileTheCreateRunWalksTheList(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  let releaseFirstUpload = null;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      if (!releaseFirstUpload) {
        return { ok: true, json: () => new Promise((resolve) => { releaseFirstUpload = () => resolve(created); }) };
      }
      return { ok: true, json: async () => created };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  const zone = dropZone();
  await zone.eventListeners.drop(dragEventOn(zone, fileTransfer([new TestFile("first.log", 5, "first")])));
  if (stagedRows().length !== 1) throw new Error("the first drop did not stage");

  const settled = form.eventListeners.submit({ preventDefault() {} });
  for (let turn = 0; turn < 200 && !releaseFirstUpload; turn += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!releaseFirstUpload) throw new Error("the upload never opened, so nothing was observed mid-run");

  // The drag is still answered, and says so: refused, not ignored. Asked of
  // both events, because "one shared decision" is only true if the busy state
  // reaches both — and dragenter is the one Chrome dispatches while the rows
  // under the cursor are being redrawn, which is exactly what a run does.
  for (const which of ["dragenter", "dragover"]) {
    delete zone.dataset.dropState;
    const hovering = dragEventOn(zone, fileTransfer([new TestFile("late.log", 5, "late")]));
    zone.eventListeners[which](hovering);
    if (hovering.prevented !== 1) {
      throw new Error(which + " left a drag on a busy zone to the browser, which navigates to the file");
    }
    if (hovering.dataTransfer.dropEffect !== "none") {
      throw new Error(which + " offered to copy while busy: " + JSON.stringify(hovering.dataTransfer.dropEffect));
    }
    if (dropState(zone) !== "refused") {
      throw new Error(which + " did not show the zone refused: " + JSON.stringify(dropState(zone)));
    }
  }

  // The reason is on the screen already, before any drop, because no drop is
  // ever coming: dropEffect "none" makes Blink cancel the drag, so the whole
  // sequence a refusal produces is enter, over, leave. A refusal announced from
  // a drop handler would be a refusal nobody is ever told.
  if (!panelStatusText("attachments").includes("still being sent")) {
    throw new Error("the refusal was not said at decision time: " +
      JSON.stringify(panelStatusText("attachments")));
  }

  // And that is how the gesture actually ends — a leave, no drop, no dragend.
  zone.eventListeners.dragleave(cancelShapedLeave(zone, fileTransfer([new TestFile("late.log", 5, "late")])));
  if (pendingDepartureTimers().length !== 1) {
    throw new Error("an ambiguous leave booked no departure, so nothing will ever clear the zone");
  }
  runDepartureTimers();
  if (dropState(zone) !== "") {
    throw new Error("the zone kept its refusal outline after the drag ended: " + JSON.stringify(dropState(zone)));
  }
  if (panelStatusText("attachments") !== "") {
    throw new Error("the refusal outlived the state it explained: " +
      JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedNames().includes("late.log")) {
    throw new Error("a file refused mid-run was staged anyway");
  }

  // The guard inside the drop handler, asked directly.
  //
  // No browser reaches it while the effect is "none" — that is the whole
  // finding above — so it is kept as a guard rather than as a message, and a
  // guard nothing exercises is a guard that quietly stops working. This is the
  // call a browser does not make.
  //
  // What it uniquely protects is the panel's own line. A file cannot be staged
  // mid-run either way, because acceptFiles refuses while busy; what only this
  // guard stops is everything the handler would otherwise say on the way there
  // — a folder note, or "that drop carried no file" — written over a panel that
  // belongs to the run. So the drop that proves it is one with something to
  // say.
  const before = stagedNames().length;
  const quiet = panelStatusText("attachments");
  const forced = dragEventOn(zone, fileTransfer([new TestFile("forced.log", 5, "forced")], 1));
  await zone.eventListeners.drop(forced);
  if (forced.prevented !== 1) throw new Error("the drop handler handed a busy drop to the browser");
  if (stagedNames().length !== before || stagedNames().includes("forced.log")) {
    throw new Error("a drop delivered mid-run pushed a file into the list the run is walking: " +
      JSON.stringify(stagedNames()));
  }
  if (panelStatusText("attachments") !== quiet) {
    throw new Error("a drop delivered mid-run wrote over the panel the run is using: " +
      JSON.stringify(panelStatusText("attachments")));
  }

  releaseFirstUpload();
  await settled;
  const uploaded = fetchCalls
    .filter((call) => call.options.method === "POST" && call.url !== "/api/tasks")
    .map((call) => JSON.parse(call.options.body).name);
  if (uploaded.join(",") !== "first.log") {
    throw new Error("the run uploaded " + JSON.stringify(uploaded));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the mid-run drop refusal: %v\n%s", err, output)
	}
}

// Child churn inside the zone must not strobe the highlight. A cursor crossing
// from a row to the chooser leaves a child without leaving the zone, and the
// rows underneath are replaced whenever the list changes.
func TestHandlerClientKeepsTheDropHighlightThroughChildChurn(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  if (dropState(zone) !== "active") throw new Error("the zone did not light up");

  // A leave naming somewhere inside the zone is a churn, not a departure. This
  // is the shape Chrome reports.
  const chooser = findElement(zone, (element) => element.id === "attachment-file");
  zone.eventListeners.dragleave(dragEventOn(zone, fileTransfer([]), {
    relatedTarget: chooser, clientX: 200, clientY: 200
  }));
  if (dropState(zone) !== "active") throw new Error("a leave onto a child cleared the highlight");

  // The same churn as Firefox and Safari report it — no relatedTarget at all —
  // told from a departure by where the cursor is.
  zone.eventListeners.dragleave(dragEventOn(zone, fileTransfer([]), {
    relatedTarget: null, clientX: 200, clientY: 200
  }));
  if (dropState(zone) !== "active") {
    throw new Error("a leave with no relatedTarget was read as a departure while the cursor was still inside");
  }

  // The churn Chrome reports with no useful coordinates at all: a leave naming
  // a child, at the origin. This is the case that makes contains() load-bearing
  // — the cursor check disagrees with it and would call this a departure.
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  zone.eventListeners.dragleave(dragEventOn(zone, fileTransfer([]), {
    relatedTarget: chooser, clientX: 0, clientY: 0
  }));
  if (dropState(zone) !== "active") {
    throw new Error("a leave naming a child was read as a departure because its coordinates were the origin");
  }
  if (pendingDepartureTimers().length !== 0) {
    throw new Error("a leave onto a child booked a departure it should have answered outright");
  }

  // A cursor genuinely outside the zone's box is a departure however it is
  // reported, and the highlight goes at once rather than after the grace.
  zone.eventListeners.dragleave(dragEventOn(zone, fileTransfer([]), {
    relatedTarget: null, clientX: 900, clientY: 900
  }));
  if (dropState(zone) !== "") {
    throw new Error("the highlight survived the cursor leaving the zone: " + JSON.stringify(dropState(zone)));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the drop highlight churn: %v\n%s", err, output)
	}
}

// A drag that is cancelled or abandoned leaves the highlight behind unless
// something takes it down, and for a file dragged in from outside the browser
// there is nothing that would: no drop is delivered, and no dragend ever
// reaches this page — the drag belongs to the desktop, not to the document.
//
// The only signal is a dragleave that names nothing with the cursor still
// inside, which is the same shape Firefox and Safari report for ordinary child
// churn. So it is not read as either: it books a departure, and any further
// drag event calls that off. Churn always has a further event — the dragenter
// for whatever the cursor moved onto — and a drag that has ended never does.
func TestHandlerClientClearsTheDropHighlightWhenADragIsAbandoned(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();
  const chooser = findElement(zone, (element) => element.id === "attachment-file");

  // Abandoned: lit, then a cancel-shaped leave and nothing more.
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  if (dropState(zone) !== "active") throw new Error("the zone did not light up");
  zone.eventListeners.dragleave(cancelShapedLeave(zone, fileTransfer([])));
  if (dropState(zone) !== "active") {
    throw new Error("an ambiguous leave was read as a departure outright, which strobes on Firefox and Safari");
  }
  if (runDepartureTimers() !== 1) throw new Error("no departure was booked, so nothing would ever clear it");
  if (dropState(zone) !== "") {
    throw new Error("the highlight outlived a drag that was abandoned: " + JSON.stringify(dropState(zone)));
  }

  // Churn of the same shape is not a departure, because the gesture carries on:
  // the dragenter for whatever the cursor moved onto calls the departure off.
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  zone.eventListeners.dragleave(cancelShapedLeave(zone, fileTransfer([])));
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")]), { target: chooser }));
  if (runDepartureTimers() !== 0) {
    throw new Error("the gesture carried on and the departure was not called off, so the zone would go dark mid-drag");
  }
  if (dropState(zone) !== "active") {
    throw new Error("a churn cleared the highlight: " + JSON.stringify(dropState(zone)));
  }

  // A drop calls it off too, and the drop's own clear is what is left.
  zone.eventListeners.dragleave(cancelShapedLeave(zone, fileTransfer([])));
  await zone.eventListeners.drop(dragEventOn(zone, fileTransfer([new TestFile("kept.log", 32, "k")])));
  if (runDepartureTimers() !== 0) throw new Error("a delivered drop left a departure booked behind it");
  if (dropState(zone) !== "") throw new Error("the drop did not clear the highlight");
  if (!stagedNames().includes("kept.log")) throw new Error("the drop did not stage its file");

  // And the ending that tells this page nothing at all — measured on Chrome
  // 151: a cancelled drag delivers no leave, no drop and no dragend, because
  // the file belongs to the desktop. Only the backstop can clear that, and
  // every drag event pushes it out again so a live gesture never trips it.
  zone.eventListeners.dragenter(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  if (dropState(zone) !== "active") throw new Error("the zone did not light up");
  if (pendingDepartureTimers(dropIdleGrace).length !== 1) {
    throw new Error("no backstop was armed, so a silently cancelled drag would stay lit for good");
  }
  zone.eventListeners.dragover(dragEventOn(zone, fileTransfer([new TestFile("shot.png", 12, "x")])));
  if (pendingDepartureTimers(dropIdleGrace).length !== 1) {
    throw new Error("the backstop was not pushed out by a drag event that proves the gesture is live");
  }
  runDepartureTimers(dropIdleGrace);
  if (dropState(zone) !== "") {
    throw new Error("a drag that ended without telling the page anything stayed lit: " +
      JSON.stringify(dropState(zone)));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the abandoned drag: %v\n%s", err, output)
	}
}

// A folder is not a file whatever the drop calls it, and a drop carrying
// nothing attachable must say so rather than wedge. Neither leaves the zone lit.
func TestHandlerClientRefusesFolderAndEmptyDropsWithoutWedging(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+dropHarness+`
setTimeout(async () => {
  await openCreateForm();
  const zone = dropZone();

  // A folder on its own.
  await zone.eventListeners.drop(dragEventOn(zone, fileTransfer([], 1)));
  if (stagedRows().length !== 0) throw new Error("a folder was staged");
  if (!panelStatusText("attachments").includes("folder")) {
    throw new Error("a folder drop said " + JSON.stringify(panelStatusText("attachments")));
  }
  if (dropState(zone) !== "") throw new Error("the zone stayed lit after a folder drop");

  // A drop carrying nothing at all.
  await zone.eventListeners.drop(dragEventOn(zone, fileTransfer([])));
  if (stagedRows().length !== 0) throw new Error("an empty drop staged something");
  if (!panelStatusText("attachments").includes("no file")) {
    throw new Error("an empty drop said " + JSON.stringify(panelStatusText("attachments")));
  }

  // Files and folders together: the files are taken and the folders are named,
  // because a reader who selected everything in a window is owed both halves.
  await zone.eventListeners.drop(dragEventOn(zone, fileTransfer([new TestFile("kept.log", 64, "k")], 2)));
  if (stagedNames().join(",") !== "kept.log") {
    throw new Error("a mixed drop staged " + JSON.stringify(stagedNames()));
  }
  if (!panelStatusText("attachments").includes("2 folders were skipped")) {
    throw new Error("a mixed drop did not name the folders: " + JSON.stringify(panelStatusText("attachments")));
  }

  // And a browser that hands over no item list at all still attaches its files.
  await zone.eventListeners.drop(dragEventOn(zone, legacyFileTransfer([new TestFile("legacy.log", 32, "l")])));
  if (!stagedNames().includes("legacy.log")) {
    throw new Error("a transfer with no items list attached nothing: " + JSON.stringify(stagedNames()));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the folder and empty drops: %v\n%s", err, output)
	}
}
