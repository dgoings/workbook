package webui

import (
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Files pasted onto the attachment controls, as a browser runs them.
//
// A paste is the drop's sibling and goes through the same door — acceptFiles()
// — so most of what is pinned here is that it does: the same ceilings, the same
// mid-run refusal, the same folder note. What is only true of a paste is where
// it is heard and what it is called, and those are the two halves this file
// spends most of its length on.

// pasteHarness is the clipboard machinery the fake DOM does not have: the
// transfer a paste carries, and an event shaped like the one a browser
// dispatches at whatever has focus.
const pasteHarness = `
// A clipboard, in the shape a paste hands one over. It is a DataTransfer, the
// same object a drag carries, holding one item per flavour this copy has.
//
// The file items carry no webkitGetAsEntry: the entry API belongs to drag and
// drop, and no engine offers it on a pasted item, so a page that asked for it
// unguarded would throw on every paste.
function clipboardTransfer(files, options = {}) {
  const folders = options.folders || 0;
  const types = [];
  const items = [];
  if (options.text) { types.push("text/plain"); items.push({ kind: "string", type: "text/plain" }); }
  if (options.html) { types.push("text/html"); items.push({ kind: "string", type: "text/html" }); }
  if (files.length || folders) types.push("Files");
  files.forEach((file) => items.push({ kind: "file", type: file.type || "", getAsFile: () => file }));
  for (let index = 0; index < folders; index += 1) {
    // A folder copied in a file manager. This one does answer the entry API,
    // because a page that only skipped folders it was told about would attach
    // the rest of them and fail on the read.
    items.push({
      kind: "file",
      type: "",
      getAsFile: () => null,
      webkitGetAsEntry: () => ({ isDirectory: true })
    });
  }
  return { types, items, files };
}
// A copy that is only text — a URL, a sentence, a task ID — which is what most
// pastes on this page are.
function textClipboard(text) {
  return clipboardTransfer([], { text });
}
// A clipboard from a browser that hands over no item list at all.
function legacyClipboard(files) {
  return { types: ["Files"], files };
}
// The screenshot itself: bytes with a media type and no name, because nobody
// named it.
function screenshot(size = 4096, contents = "png-bytes") {
  return new TestFile("", size, contents, "image/png");
}
function pasteEventOn(target, clipboard) {
  const event = {
    target,
    clipboardData: clipboard,
    prevented: 0,
    preventDefault() { event.prevented += 1; }
  };
  return event;
}
// A paste, dispatched where a browser dispatches it: at the document, having
// started at whatever had focus.
async function pasteOn(target, clipboard) {
  const event = pasteEventOn(target, clipboard);
  await documentEventListeners.paste(event);
  return event;
}
const generatedPasteName = /^pasted-\d{8}-\d{6}(-\d+)?\.[a-z0-9]+$/;
`

// A screenshot pasted onto a New Task form is staged, exactly as one dropped on
// it is: same door, same list, same ceilings. What is different is its name —
// the clipboard supplied none — so the page supplies one that says when it
// arrived rather than leaving a row with nothing to call it.
func TestHandlerClientStagesAnImagePastedOntoTheCreateForm(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+pasteHarness+`
setTimeout(async () => {
  await openCreateForm();

  const pasted = await pasteOn(main, clipboardTransfer([screenshot()]));
  if (pasted.prevented !== 1) {
    throw new Error("the paste was left to the browser, which inserts the file wherever the caret is");
  }
  const names = stagedNames();
  if (names.length !== 1) throw new Error("the paste staged " + JSON.stringify(names));
  if (!generatedPasteName.test(names[0])) {
    throw new Error("a nameless pasted image was staged as " + JSON.stringify(names[0]));
  }
  if (!names[0].endsWith(".png")) {
    throw new Error("the generated name lost the format the clipboard did state: " + JSON.stringify(names[0]));
  }
  // Staged rather than sent, because that is what this surface's acceptFiles
  // does and a paste has no rules of its own.
  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("a paste on the create form sent a write before the task existed");
  }
  if (panelCountText("attachments") !== "1 staged · 4 KiB of files") {
    throw new Error("the pasted file was weighed as " + JSON.stringify(panelCountText("attachments")));
  }

  // A file copied out of a folder arrives named, and keeps the name it arrived
  // with: the generated one is for what nobody named.
  await pasteOn(main, clipboardTransfer([new TestFile("notes.txt", 64, "n", "text/plain")]));
  if (stagedNames()[1] !== "notes.txt") {
    throw new Error("a named file was renamed by the paste: " + JSON.stringify(stagedNames()));
  }

  // And the placeholder every engine invents for a clipboard image is treated
  // as the absence of a name, which is what it is: three screenshots would
  // otherwise be three rows called image.png.
  await pasteOn(main, clipboardTransfer([new TestFile("image.png", 128, "i", "image/png")]));
  const placeheld = stagedNames()[2];
  if (!generatedPasteName.test(placeheld)) {
    throw new Error("the engines' placeholder was staged as " + JSON.stringify(placeheld));
  }

  // Two nameless images in one paste happen at one moment, so the stamp alone
  // would call them the same thing.
  await pasteOn(main, clipboardTransfer([screenshot(16, "a"), screenshot(16, "b")]));
  const pair = stagedNames().slice(3);
  if (pair.length !== 2 || pair[0] === pair[1]) {
    throw new Error("one paste named two files the same: " + JSON.stringify(pair));
  }

  // A copy that is only text is not this page's business at all, wherever the
  // caret is: the reader is pasting into something.
  const words = await pasteOn(main, textClipboard("WB-01J0000000000000000000CR01"));
  if (words.prevented !== 0) throw new Error("a text paste was swallowed by the attachment panel");
  if (stagedNames().length !== 5) {
    throw new Error("a text paste changed the staged list: " + JSON.stringify(stagedNames()));
  }

  // And a browser that hands over no item list still attaches what it carried.
  await pasteOn(main, legacyClipboard([new TestFile("legacy.log", 32, "l")]));
  if (!stagedNames().includes("legacy.log")) {
    throw new Error("a clipboard with no items list attached nothing: " + JSON.stringify(stagedNames()));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the create-form paste: %v\n%s", err, output)
	}
}

// The ceilings a pasted file is asked are the chooser's ceilings, because it is
// the chooser's call — and the refusal names the file by the name the list would
// have shown it under, which for a screenshot is the generated one.
func TestHandlerClientRefusesAnOversizedPasteBeforeReadingIt(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+pasteHarness+`
setTimeout(async () => {
  await openCreateForm();

  await pasteOn(main, clipboardTransfer([screenshot(`+strconv.Itoa(core.MaxAttachmentFileBytes+1)+`)]));
  const reported = panelStatusText("attachments");
  if (!reported.includes("pasted-") || !reported.includes("attach a link instead")) {
    throw new Error("the refusal does not name the pasted file: " + JSON.stringify(reported));
  }
  if (!reported.includes("`+strconv.Itoa(core.MaxAttachmentFileBytes)+` bytes")) {
    throw new Error("the refusal did not state core's own ceiling: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== 0) throw new Error("an over-sized paste was staged anyway");
  if (readCalls.length !== 0) throw new Error("an over-sized paste was read anyway: " + readCalls.join(","));

  // A folder pasted is a folder skipped, said in the words a dropped one is
  // said in — and a paste carrying nothing attachable says so rather than
  // leaving the reader to wonder whether it worked.
  await pasteOn(main, clipboardTransfer([], { folders: 2 }));
  if (stagedRows().length !== 0) throw new Error("a pasted folder was staged");
  if (!panelStatusText("attachments").includes("2 folders were skipped")) {
    throw new Error("a pasted folder said " + JSON.stringify(panelStatusText("attachments")));
  }
  await pasteOn(main, clipboardTransfer([new TestFile("kept.log", 64, "k")], { folders: 1 }));
  if (stagedNames().join(",") !== "kept.log") {
    throw new Error("a mixed paste staged " + JSON.stringify(stagedNames()));
  }
  if (!panelStatusText("attachments").includes("One folder was skipped")) {
    throw new Error("a mixed paste did not name the folder: " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the oversized paste refusal: %v\n%s", err, output)
	}
}

// A paste that carries text as well as a file belongs to the caret.
//
// A clipboard holds one thing in several flavours: a range of spreadsheet cells
// is text and a picture of itself, and a copied region of a web page is markup
// and a picture of itself. A page that attached every paste carrying an image
// would answer Cmd+V in the description by attaching a PNG of the words the
// reader meant to type — silently, because the field would stay empty.
func TestHandlerClientLeavesATextAndImagePasteToTheFieldItLandedIn(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+pasteHarness+`
setTimeout(async () => {
  const form = await openCreateForm();
  const description = findElement(form, (element) => element.id === "task-description");
  if (!description) throw new Error("the New Task form draws no description field");

  const spreadsheet = clipboardTransfer([screenshot()], { text: "one\ttwo", html: "<table>" });
  const typed = await pasteOn(description, spreadsheet);
  if (typed.prevented !== 0) {
    throw new Error("a paste of text and a picture of it was taken from the field the caret was in");
  }
  if (stagedRows().length !== 0) {
    throw new Error("a spreadsheet paste attached a picture of itself: " + JSON.stringify(stagedNames()));
  }

  // The same clipboard, pasted with the caret nowhere that takes text, is the
  // reader asking for the image: there is nothing else it could mean.
  const aimed = await pasteOn(main, spreadsheet);
  if (aimed.prevented !== 1) throw new Error("a paste outside any field was left to the browser");
  if (stagedRows().length !== 1) {
    throw new Error("a paste outside any field attached nothing: " + JSON.stringify(stagedNames()));
  }

  // And a screenshot carries no text flavour, so the caret being in a field
  // does not make it the field's: there is nothing there for the field to take.
  const shot = await pasteOn(description, clipboardTransfer([screenshot(2048, "s")]));
  if (shot.prevented !== 1) {
    throw new Error("a screenshot pasted into the description was handed to the browser instead of attached");
  }
  if (stagedRows().length !== 2) {
    throw new Error("a screenshot pasted into the description staged " + JSON.stringify(stagedNames()));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the text-and-image paste: %v\n%s", err, output)
	}
}

// The same paste on a task that already exists uploads, because that is what
// acceptFiles does on that surface. The bytes are the clipboard's; only the name
// is this page's, and it reaches the server as the attachment's name.
func TestHandlerClientUploadsAnImagePastedOntoATaskPage(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	program := threadPageProgram(t, []core.Task{task}, `
`+fileHarness+`
`+pasteHarness+`
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };

  const pasted = await pasteOn(main, clipboardTransfer([screenshot(3, "png")]));
  if (pasted.prevented !== 1) throw new Error("the paste was left to the browser");

  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 1) {
    throw new Error("a pasted file sent " + writes.length + " uploads; panel says " +
      JSON.stringify(panelStatusText("attachments")));
  }
  if (writes[0].url !== "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) + "/attachments") {
    throw new Error("a pasted file went to " + writes[0].url);
  }
  const sent = JSON.parse(writes[0].options.body);
  if (sent.kind !== "file") throw new Error("a pasted image was not sent as a file");
  if (!generatedPasteName.test(sent.name)) {
    throw new Error("the upload was named " + JSON.stringify(sent.name));
  }
  // The name is new and the bytes are not: naming a file must not lose what it
  // was carrying.
  if (Buffer.from(sent.content, "base64").toString() !== "png") {
    throw new Error("a pasted file's bytes did not survive being named");
  }
  // A media type is written into shared history, so the board never guesses
  // one: the name decides it through the table Workbook keeps, exactly as it
  // does for a chosen or dropped file.
  if (sent.media !== undefined) throw new Error("the paste sent a media type of its own");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task-page paste: %v\n%s", err, output)
	}
}

// A paste while the create run is walking the list is refused exactly as a drop
// and a mid-run add are, and said out loud: a paste has no outline to turn red
// and no disabled button to have been stopped by, so silence would read as the
// file having been taken.
func TestHandlerClientRefusesAPasteWhileTheCreateRunWalksTheList(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
`+pasteHarness+`
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
  await pasteOn(main, clipboardTransfer([new TestFile("first.log", 5, "first")]));
  if (stagedRows().length !== 1) throw new Error("the first paste did not stage");

  const settled = form.eventListeners.submit({ preventDefault() {} });
  for (let turn = 0; turn < 200 && !releaseFirstUpload; turn += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!releaseFirstUpload) throw new Error("the upload never opened, so nothing was observed mid-run");

  const late = await pasteOn(main, clipboardTransfer([screenshot(16, "late")]));
  if (late.prevented !== 1) {
    throw new Error("a refused paste was handed to the browser, which inserts the file into the form");
  }
  if (!panelStatusText("attachments").includes("still being sent")) {
    throw new Error("the refusal was not said: " + JSON.stringify(panelStatusText("attachments")));
  }
  if (!panelStatusText("attachments").includes("paste again")) {
    throw new Error("the refusal names the wrong gesture: " + JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedRows().length !== 1) {
    throw new Error("a paste mid-run pushed a file into the list the run is walking: " +
      JSON.stringify(stagedNames()));
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
		t.Fatalf("execute the mid-run paste refusal: %v\n%s", err, output)
	}
}

// The one listener is at the document, so the surface it acts on has to be the
// one on screen. A reader who leaves the form and pastes on the board is pasting
// at nothing: the panel that would have taken it is gone, and a page that still
// held it would be attaching files to a form nobody can see.
func TestHandlerClientTakesNoPasteOnceTheAttachmentPanelIsGone(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
`+pasteHarness+`
setTimeout(async () => {
  await openCreateForm();
  await pasteOn(main, clipboardTransfer([screenshot()]));
  if (stagedRows().length !== 1) throw new Error("the form did not take a paste to begin with");

  // The Board link in the header, followed the way a reader follows it.
  const board = document.createElement("a");
  board.href = "/";
  await documentEventListeners.click({ target: board, button: 0, preventDefault() {}, stopPropagation() {} });
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (main.firstElementChild !== boardView) throw new Error("the Board link did not land on the board");

  const orphaned = await pasteOn(main, clipboardTransfer([screenshot(64, "b")]));
  if (orphaned.prevented !== 0) {
    throw new Error("a paste on the board was answered by a panel that is no longer on screen");
  }
  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("a paste on the board wrote to a task: " +
      JSON.stringify(fetchCalls.filter((call) => (call.options.method || "GET") !== "GET").map((call) => call.url)));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the paste after leaving the form: %v\n%s", err, output)
	}
}
