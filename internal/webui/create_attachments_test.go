package webui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Attachments staged on a New Task form, as a browser runs them.
//
// The whole of this file is about one thing an attachment cannot do: exist
// before its task. The bytes live in the commit that adds them, on the task's
// own ref, so a create form has to hold what it is given and send it afterwards
// — and every case below is about what happens between those two moments.

const createAttachmentTaskID = "WB-01J0000000000000000000CR01"

// createAttachmentProgram builds the Node program for a New Task form with the
// staged-attachment helpers a test drives it through.
func createAttachmentProgram(t *testing.T, body string) string {
	t.Helper()
	const path = "/tasks/new?status=ready"
	script := newTaskClientScript(t, path)
	return clientDOMHarness(path, tasksDocumentJSON(t, nil)) + script + threadPanelHelpers + fileHarness + `
// The New Task form, once the first refresh has let the route render it. Until
// then the route is the loading message, which is not a form at all.
async function openCreateForm() {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const form = findElement(main, (element) => element.tagName === "FORM" && hasClassToken(element, "task-layout"));
  if (!form) throw new Error("the New Task form did not render");
  return form;
}
function stagedRows() { return panelRows("attachments"); }
// What a staged row is called, which is the first line of it.
function stagedNames() {
  return stagedRows().map((row) => row.children[0].textContent);
}
function stagedFailure(row) {
  const line = findElement(row, (element) => hasDataKey(element, "attachmentFailure"));
  return line ? line.textContent : "";
}
function panelCountText(name) {
  const panel = panelSection(name);
  const line = panel && findElement(panel, (element) => hasDataKey(element, "panelCount"));
  return line ? line.textContent : "";
}
function stageFiles(files) {
  const chooser = findElement(panelSection("attachments"), (element) => element.id === "attachment-file");
  chooser.files = files;
  return submitForm(attachmentForm("file"));
}
function stageLink(destination, label = "") {
  const panel = panelSection("attachments");
  findElement(panel, (element) => element.id === "attachment-url").value = destination;
  findElement(panel, (element) => element.id === "attachment-label").value = label;
  return submitForm(attachmentForm("link"));
}
function saveButton() {
  return findElement(main, (element) => hasClassToken(element, "save-button"));
}
function openTaskLink() {
  return findElement(main, (element) => hasDataKey(element, "openCreatedTask"));
}
function attachmentWrites() {
  return fetchCalls.filter((call) => call.options.method === "POST" &&
    call.url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(createAttachmentTaskID) + `) + "/attachments");
}
function createWrites() {
  return fetchCalls.filter((call) => call.options.method === "POST" && call.url === "/api/tasks");
}
function typeTitle(form, value) {
  findElement(form, (element) => element.id === "task-title").value = value;
}
` + body
}

// createdTaskJSON is the task a create in this file answers with, and the board
// the refresh after it reads back.
func createdTaskJSON(t *testing.T) (string, string) {
	t.Helper()
	created := clientPlacementTask(createAttachmentTaskID, "Task with attachments", core.StatusReady, core.PriorityMedium)
	created.Head = "head-created"
	tasks := tasksDocumentJSON(t, []core.Task{created})
	return taskMutationJSON(tasks, ""), tasks
}

// Files and links chosen on a New Task form are held, not sent: there is no ref
// to send them to. The list says what is staged and what it weighs, and a row
// can be taken back out before the task is ever created.
func TestHandlerClientStagesAttachmentsOnTheCreateForm(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  if (panelCountText("attachments") !== "Nothing staged") {
    throw new Error("an empty staging list said " + JSON.stringify(panelCountText("attachments")));
  }
  // Two at once, which is what a chooser that takes more than one file hands
  // back and what a drop will hand the same entry point later.
  await stageFiles([new TestFile("trace.log", 2048, "trace"), new TestFile("shot.png", 4096, "png")]);
  await stageLink("https://example.test/design", "Design doc");

  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("staging sent a write before the task existed");
  }
  if (readCalls.length !== 0) {
    throw new Error("a staged file was read before there was anywhere to send it: " + readCalls.join(","));
  }
  const names = stagedNames();
  if (names.length !== 3 || names[0] !== "trace.log" || names[1] !== "shot.png" || names[2] !== "Design doc") {
    throw new Error("the staged list = " + JSON.stringify(names));
  }
  // A link stores nothing, so it is counted as a row and not as bytes.
  if (panelCountText("attachments") !== "3 staged · 6 KiB of files") {
    throw new Error("the staged count = " + JSON.stringify(panelCountText("attachments")));
  }
  const rows = stagedRows();
  if (!rows[0].children[1].textContent.includes("2 KiB")) {
    throw new Error("a staged file does not say what it weighs: " + rows[0].children[1].textContent);
  }
  if (!rows[2].children[1].textContent.includes("https://example.test/design")) {
    throw new Error("a staged link does not say where it goes: " + rows[2].children[1].textContent);
  }
  // A staged row offers nothing to follow: a file has no route to download from
  // and a link is a destination this page has not stored.
  if (findElement(panelSection("attachments"), (element) => element.tagName === "A")) {
    throw new Error("a staged row linked to something that does not exist yet");
  }

  rowControl(rows[1], "Remove").eventListeners.click();
  const left = stagedNames();
  if (left.length !== 2 || left[0] !== "trace.log" || left[1] !== "Design doc") {
    throw new Error("Remove took out the wrong row: " + JSON.stringify(left));
  }
  if (panelCountText("attachments") !== "2 staged · 2 KiB of files") {
    throw new Error("the count did not follow the removal: " + JSON.stringify(panelCountText("attachments")));
  }
  rowControl(stagedRows()[0], "Remove").eventListeners.click();
  rowControl(stagedRows()[0], "Remove").eventListeners.click();
  if (stagedRows().length !== 0) throw new Error("the list did not empty");
  if (panelCountText("attachments") !== "Nothing staged") {
    throw new Error("an emptied list said " + JSON.stringify(panelCountText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute staging on the create form: %v\n%s", err, output)
	}
}

// A file too large to attach is refused as it is staged, before it is read and
// long before a task exists to attach it to — which is the whole point of asking
// now: the server's refusal would arrive after the create.
func TestHandlerClientRefusesAnOversizedFileAsItIsStaged(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  await stageFiles([new TestFile("enormous.bin", `+strconv.Itoa(core.MaxAttachmentFileBytes+1)+`, "x")]);

  const reported = panelStatusText("attachments");
  if (!reported.includes("enormous.bin") || !reported.includes("attach a link instead")) {
    throw new Error("the refusal does not name the file: " + JSON.stringify(reported));
  }
  // Both sides in bytes, because both round to the same unit.
  if (!reported.includes("`+strconv.Itoa(core.MaxAttachmentFileBytes+1)+` bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxAttachmentFileBytes)+` bytes")) {
    throw new Error("a one-byte overrun was described in units that cannot tell it apart: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== 0) throw new Error("an over-sized file was staged anyway");
  if (readCalls.length !== 0) throw new Error("an over-sized file was read anyway");

  // An empty file is refused for a different reason and says so.
  await stageFiles([new TestFile("empty.log", 0, "")]);
  if (!panelStatusText("attachments").includes("empty.log is empty")) {
    throw new Error("an empty file was described as " + JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedRows().length !== 0) throw new Error("an empty file was staged anyway");

  // The refusal stops the run at the file that caused it and keeps everything
  // chosen before it, which is what makes choosing several at once safe.
  await stageFiles([
    new TestFile("first.log", 1024, "a"),
    new TestFile("enormous.bin", `+strconv.Itoa(core.MaxAttachmentFileBytes+1)+`, "x"),
    new TestFile("third.log", 1024, "c")
  ]);
  const names = stagedNames();
  if (names.length !== 1 || names[0] !== "first.log") {
    throw new Error("a refusal mid-run left " + JSON.stringify(names));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the oversized staging refusal: %v\n%s", err, output)
	}
}

// Ten files a task may each hold are eleven files it may not hold together. The
// total ceiling is core's and the form asks it of what is already staged, which
// is the only place it can be asked at all before the task exists.
func TestHandlerClientRefusesStagingPastTheTotalCeiling(t *testing.T) {
	node := requireNode(t)
	allowed := core.MaxLiveAttachmentBytes / core.MaxAttachmentFileBytes
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  const each = `+strconv.Itoa(core.MaxAttachmentFileBytes)+`;
  const fits = [];
  for (let index = 0; index < `+strconv.Itoa(allowed)+`; index += 1) {
    fits.push(new TestFile("shot-" + index + ".png", each, "x"));
  }
  await stageFiles(fits);
  if (stagedRows().length !== `+strconv.Itoa(allowed)+`) {
    throw new Error("a task's worth of files did not all stage: " + stagedRows().length);
  }
  if (panelStatusText("attachments") !== "") {
    throw new Error("staging exactly what fits reported " + JSON.stringify(panelStatusText("attachments")));
  }

  // One byte more than the task may hold, refused by the total rather than by
  // the per-file ceiling — which this file is well under.
  await stageFiles([new TestFile("one-more.png", 1, "x")]);
  const reported = panelStatusText("attachments");
  if (!reported.includes("one-more.png") || !reported.includes("attach a link instead")) {
    throw new Error("the total ceiling's refusal does not name the file: " + JSON.stringify(reported));
  }
  if (!reported.includes("`+strconv.Itoa(core.MaxLiveAttachmentBytes+1)+` bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxLiveAttachmentBytes)+` bytes")) {
    throw new Error("the total was not stated against the ceiling: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== `+strconv.Itoa(allowed)+`) {
    throw new Error("the refused file was staged, or took others with it: " + stagedRows().length);
  }
  // A link is not bytes, so the ceiling has nothing to say about one.
  await stageLink("https://example.test/build-log");
  if (stagedRows().length !== `+strconv.Itoa(allowed+1)+`) {
    throw new Error("a link was refused by a ceiling on files: " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the total ceiling refusal: %v\n%s", err, output)
	}
}

// A name too long for core is refused as it is staged, and the ceiling is in
// bytes rather than in characters.
//
// This is the refusal a naive `length` would never make: 85 Japanese characters
// are 255 bytes of stored name and 85 of anything a reader counts, so the file
// that trips this is one that looks entirely ordinary. Left to the server it is
// the exact outcome the pre-checks exist to prevent — a task created and an
// upload refused after it.
func TestHandlerClientRefusesAFileNameTooLongInBytes(t *testing.T) {
	node := requireNode(t)
	// Three bytes a character in UTF-8, so this is well over the ceiling while
	// being far under it by any count of characters.
	overlong := strings.Repeat("あ", core.MaxAttachmentNameBytes/3+1) + ".png"
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  const name = `+strconv.Quote(overlong)+`;
  const bytes = new TextEncoder().encode(name).length;
  if (bytes <= `+strconv.Itoa(core.MaxAttachmentNameBytes)+`) {
    throw new Error("the test's own name is not over the ceiling: " + bytes + " bytes");
  }
  if (name.length > `+strconv.Itoa(core.MaxAttachmentNameBytes)+`) {
    throw new Error("the test's name is over the ceiling by character count too, so it proves nothing");
  }
  await stageFiles([new TestFile(name, 1024, "x")]);

  const reported = panelStatusText("attachments");
  if (!reported.includes("rename it")) {
    throw new Error("an over-long name was answered with " + JSON.stringify(reported));
  }
  if (!reported.includes(bytes + " bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxAttachmentNameBytes)+`")) {
    throw new Error("the refusal does not state the count against the ceiling: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== 0) throw new Error("a file with an unstorable name was staged");
  if (readCalls.length !== 0) throw new Error("a file with an unstorable name was read");

  // A name that is long in characters and inside the ceiling in bytes is fine,
  // which is the half of this rule a byte count is needed to get right.
  await stageFiles([new TestFile("a".repeat(`+strconv.Itoa(core.MaxAttachmentNameBytes)+`), 1024, "x")]);
  if (stagedRows().length !== 1) {
    throw new Error("a name exactly at the ceiling was refused: " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the over-long name refusal: %v\n%s", err, output)
	}
}

// A link core will never store is refused as it is staged. The task page leaves
// this to the server deliberately, because there its refusal costs one request;
// here it would cost a task made and an attachment missing.
func TestHandlerClientRefusesAStagedLinkThatIsNotHTTP(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  await stageLink("javascript:alert(1)", "Looks harmless");
  if (!panelStatusText("attachments").includes("http or https")) {
    throw new Error("a script URL was answered with " + JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedRows().length !== 0) throw new Error("a script URL was staged");

  await stageLink("");
  if (!panelStatusText("attachments").includes("needs a URL")) {
    throw new Error("an empty URL was answered with " + JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedRows().length !== 0) throw new Error("an empty URL was staged");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the staged link refusal: %v\n%s", err, output)
	}
}

// A link label too long for core is refused as it is staged, and the ceiling is
// in bytes rather than in characters.
//
// The same discrimination the file-name check needs: a 67-character Japanese
// label is over a 200-byte ceiling and comfortably under it by any count a
// reader — or a naive `length` — would make. Left to the server it is a task
// created and a link missing, over text somebody pasted.
func TestHandlerClientRefusesALinkLabelTooLongInBytes(t *testing.T) {
	node := requireNode(t)
	overlong := strings.Repeat("あ", core.MaxAttachmentLabelBytes/3+1)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  const label = `+strconv.Quote(overlong)+`;
  const bytes = new TextEncoder().encode(label).length;
  if (bytes <= `+strconv.Itoa(core.MaxAttachmentLabelBytes)+`) {
    throw new Error("the test's own label is not over the ceiling: " + bytes + " bytes");
  }
  if (label.length > `+strconv.Itoa(core.MaxAttachmentLabelBytes)+`) {
    throw new Error("the test's label is over by character count too, so it proves nothing");
  }
  await stageLink("https://example.test/design", label);

  const reported = panelStatusText("attachments");
  if (!reported.includes("label")) {
    throw new Error("an over-long label was answered with " + JSON.stringify(reported));
  }
  if (!reported.includes(bytes + " bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxAttachmentLabelBytes)+`")) {
    throw new Error("the refusal does not state the count against the ceiling: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== 0) throw new Error("a link with an unstorable label was staged");

  // A label exactly at the ceiling is storable and stages.
  await stageLink("https://example.test/design", "b".repeat(`+strconv.Itoa(core.MaxAttachmentLabelBytes)+`));
  if (stagedRows().length !== 1) {
    throw new Error("a label at the ceiling was refused: " + JSON.stringify(panelStatusText("attachments")));
  }
  // And a link with no label at all is never measured for one.
  await stageLink("https://example.test/plain");
  if (stagedRows().length !== 2) {
    throw new Error("an unlabelled link was refused: " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the over-long label refusal: %v\n%s", err, output)
	}
}

// A URL too long for core is refused as it is staged, in bytes, and before its
// scheme is looked at — which is the order core asks in, so a URL that is both
// too long and not http hears the same first answer here that it would there.
func TestHandlerClientRefusesALinkURLTooLongInBytes(t *testing.T) {
	node := requireNode(t)
	// Multibyte path segment, so the ceiling is passed by bytes while the
	// character count stays under it.
	overlong := "https://example.test/" + strings.Repeat("あ", core.MaxAttachmentURLBytes/3+1)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  await openCreateForm();
  const destination = `+strconv.Quote(overlong)+`;
  const bytes = new TextEncoder().encode(destination).length;
  if (bytes <= `+strconv.Itoa(core.MaxAttachmentURLBytes)+`) {
    throw new Error("the test's own URL is not over the ceiling: " + bytes + " bytes");
  }
  if (destination.length > `+strconv.Itoa(core.MaxAttachmentURLBytes)+`) {
    throw new Error("the test's URL is over by character count too, so it proves nothing");
  }
  await stageLink(destination);

  const reported = panelStatusText("attachments");
  if (!reported.includes("URL")) {
    throw new Error("an over-long URL was answered with " + JSON.stringify(reported));
  }
  if (!reported.includes(bytes + " bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxAttachmentURLBytes)+`")) {
    throw new Error("the refusal does not state the count against the ceiling: " + JSON.stringify(reported));
  }
  if (stagedRows().length !== 0) throw new Error("a link with an unstorable URL was staged");

  // Length before scheme, which is core's order: a URL that fails both is told
  // about its length first.
  const longAndUnsafe = "javascript:" + "あ".repeat(`+strconv.Itoa(core.MaxAttachmentURLBytes)+`);
  await stageLink(longAndUnsafe);
  if (!panelStatusText("attachments").includes("URL is")) {
    throw new Error("a URL that fails both rules was answered out of order: " +
      JSON.stringify(panelStatusText("attachments")));
  }
  if (stagedRows().length !== 0) throw new Error("a script URL was staged");

  // A URL at the ceiling is storable and stages.
  const atCeiling = "https://example.test/" +
    "c".repeat(`+strconv.Itoa(core.MaxAttachmentURLBytes)+` - "https://example.test/".length);
  if (new TextEncoder().encode(atCeiling).length !== `+strconv.Itoa(core.MaxAttachmentURLBytes)+`) {
    throw new Error("the boundary URL is not exactly at the ceiling");
  }
  await stageLink(atCeiling);
  if (stagedRows().length !== 1) {
    throw new Error("a URL at the ceiling was refused: " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the over-long URL refusal: %v\n%s", err, output)
	}
}

// The create, then one upload per staged attachment, in the order they were
// staged and every one of them addressed to the task the server just named.
//
// A create carrying attachments does not take the instant landing: the uploads
// need an ID the server has not assigned yet, and the list that would have to
// report their failures leaves with the form.
func TestHandlerClientAttachesStagedAttachmentsAfterTheCreate(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  await stageFiles([new TestFile("trace.log", 5, "trace")]);
  await stageLink("https://example.test/design", "Design doc");

  const settled = form.eventListeners.submit({ preventDefault() {} });
  // The form is still on screen while the uploads are open. A create with
  // nothing staged leaves before the POST is answered; this one cannot.
  if (main.firstElementChild === boardView) {
    throw new Error("a create carrying attachments took the instant landing");
  }
  await settled;

  if (createWrites().length !== 1) throw new Error("creates = " + createWrites().length);
  const uploads = attachmentWrites();
  if (uploads.length !== 2) {
    throw new Error("uploads = " + uploads.length + "; the form says " +
      JSON.stringify(findElement(main, (element) => hasDataKey(element, "saveStatus")) || ""));
  }
  // Ordered as they were staged, so a reader gets back the list they built.
  const first = JSON.parse(uploads[0].options.body);
  const second = JSON.parse(uploads[1].options.body);
  if (first.kind !== "file" || first.name !== "trace.log") {
    throw new Error("the first upload carried " + JSON.stringify(first));
  }
  if (Buffer.from(first.content, "base64").toString() !== "trace") {
    throw new Error("the staged file's bytes did not survive: " + JSON.stringify(first.content));
  }
  // No media type: the name decides it, through the table core keeps.
  if ("media" in first) throw new Error("an upload passed the browser's media guess along");
  if (second.kind !== "link" || second.url !== "https://example.test/design" || second.label !== "Design doc") {
    throw new Error("the second upload carried " + JSON.stringify(second));
  }
  if (uploads.some((call) => call.options.headers["Content-Type"] !== "application/json")) {
    throw new Error("an upload did not declare JSON, which the same-origin guard requires");
  }
  // Every write went to the task the create returned, and the create itself is
  // the only thing that was ever sent to /api/tasks.
  if (fetchCalls.some((call) => call.options.method === "POST" &&
      call.url !== "/api/tasks" && !call.url.includes(encodeURIComponent(`+strconv.Quote(createAttachmentTaskID)+`)))) {
    throw new Error("a write went somewhere other than the task that was created");
  }

  // A create that got everything it staged lands where any other create does.
  if (main.firstElementChild !== boardView) {
    throw new Error("a finished create did not land on the board");
  }
  if (historyPaths[historyPaths.length - 1] !== "/") {
    throw new Error("the landing = " + JSON.stringify(historyPaths));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the staged attachment uploads: %v\n%s", err, output)
	}
}

// The staged list cannot change while the run is walking it.
//
// persistAttachmentDrafts reads the list once, at entry, so anything staged
// after that is invisible to it — and invisible in the worst possible way: the
// row sits on the screen saying "3 staged", uploads nothing, fails nothing, and
// a run with no failures navigates. The File would leave with the node and this
// client would not have said one word about it. So the panel is frozen for the
// whole run, exactly as the relationship sidebar beside it is.
func TestHandlerClientFreezesTheStagedListWhileTheCreateRunWalksIt(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  let releaseFirstUpload = null;
  let uploads = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      uploads += 1;
      // The first upload is held open, which is the window a reader has to
      // touch the list while the run is inside it.
      if (uploads === 1) {
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
  await stageFiles([new TestFile("first.log", 5, "first"), new TestFile("second.log", 6, "second")]);
  if (stagedNames().length !== 2) throw new Error("the two files did not stage");

  const settled = form.eventListeners.submit({ preventDefault() {} });
  for (let turn = 0; turn < 200 && !releaseFirstUpload; turn += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!releaseFirstUpload) throw new Error("the first upload never opened, so nothing was observed mid-run");

  // Every control the panel is drawing is disabled, which is what a browser
  // honours.
  const live = findElements(panelSection("attachments"), (element) => element.tagName === "BUTTON")
    .filter((button) => !button.disabled);
  if (live.length !== 0) {
    throw new Error("controls stayed live while the run walked the list: " +
      JSON.stringify(live.map((button) => button.textContent)));
  }

  // And a file staged anyway does not join a list the run has already read.
  await stageFiles([new TestFile("late.log", 9, "late")]);
  const during = stagedNames();
  if (during.length !== 2 || during.includes("late.log")) {
    throw new Error("a file staged mid-run joined the list: " + JSON.stringify(during));
  }
  // A link is the same list and the same answer.
  await stageLink("https://example.test/late");
  if (stagedNames().length !== 2) {
    throw new Error("a link staged mid-run joined the list: " + JSON.stringify(stagedNames()));
  }

  releaseFirstUpload();
  await settled;

  if (uploads !== 2) throw new Error("uploads = " + uploads + ", want the two that were staged");
  const sentNames = fetchCalls
    .filter((call) => call.options.method === "POST" && call.url !== "/api/tasks")
    .map((call) => JSON.parse(call.options.body).name);
  if (sentNames.join(",") !== "first.log,second.log") {
    throw new Error("the run sent " + JSON.stringify(sentNames));
  }
  // The landing is the proof that the silent case is gone: a run that quietly
  // skipped a staged file would arrive here with nothing to report either.
  if (main.firstElementChild !== boardView) throw new Error("the finished create did not land on the board");
  if (!notice.hidden) throw new Error("a create that lost nothing reported a loss: " + notice.textContent);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the frozen staged list: %v\n%s", err, output)
	}
}

// Removing a row while the run is walking the list does not take it out from
// under the walk. The row would leave the screen and be attached anyway, which
// is a reader watching this client do the opposite of what they asked.
func TestHandlerClientRefusesToRemoveAStagedRowMidRun(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  let releaseFirstUpload = null;
  let uploads = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      uploads += 1;
      if (uploads === 1) {
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
  await stageFiles([new TestFile("first.log", 5, "first"), new TestFile("second.log", 6, "second")]);
  const settled = form.eventListeners.submit({ preventDefault() {} });
  for (let turn = 0; turn < 200 && !releaseFirstUpload; turn += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  if (!releaseFirstUpload) throw new Error("the first upload never opened, so nothing was observed mid-run");

  const second = stagedRows()[1];
  const remove = rowControl(second, "Remove");
  if (!remove.disabled) throw new Error("the Remove on a row the run has not reached yet is still live");
  // Pressed anyway, which a disabled attribute alone would not stop if the
  // press arrived some other way.
  remove.eventListeners.click();
  if (stagedNames().length !== 2) {
    throw new Error("a row was taken out from under the walk: " + JSON.stringify(stagedNames()));
  }

  releaseFirstUpload();
  await settled;
  // The row that could not be removed was attached, which is the honest
  // outcome: it was already on its way when the reader asked.
  const sentNames = fetchCalls
    .filter((call) => call.options.method === "POST" && call.url !== "/api/tasks")
    .map((call) => JSON.parse(call.options.body).name);
  if (sentNames.join(",") !== "first.log,second.log") {
    throw new Error("the run sent " + JSON.stringify(sentNames));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the mid-run removal refusal: %v\n%s", err, output)
	}
}

// A refused create attaches nothing. There is no task to attach to, so nothing
// is read, nothing is sent, and everything staged is still staged — including
// the Save that means "create", because no task was made to retry against.
func TestHandlerClientRefusedCreateUploadsNothing(t *testing.T) {
	node := requireNode(t)
	program := createAttachmentProgram(t, `
setTimeout(async () => {
  const form = await openCreateForm();
  let creates = 0;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      creates += 1;
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "validation", message: "title is not valid" }
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    if (options.method === "POST") throw new Error("an attachment was uploaded without a task");
    return { ok: true, json: async () => (`+`{ format: "workbook.tasks", version: 1, tasks: [], presentation: [] }`+`) };
  };

  typeTitle(form, "Doomed");
  await stageFiles([new TestFile("trace.log", 5, "trace")]);
  await stageLink("https://example.test/design");
  await form.eventListeners.submit({ preventDefault() {} });

  if (creates !== 1) throw new Error("creates = " + creates);
  if (readCalls.length !== 0) throw new Error("a staged file was read for a task that was never made");
  if (fetchCalls.some((call) => call.options.method === "POST" && call.url !== "/api/tasks")) {
    throw new Error("a refused create uploaded something anyway");
  }
  if (main.firstElementChild === boardView) throw new Error("a refused create left the form");
  const said = findElement(main, (element) => hasDataKey(element, "saveStatus")).textContent;
  if (!said.includes("title is not valid")) throw new Error("the refusal was not reported: " + JSON.stringify(said));
  if (stagedNames().length !== 2) throw new Error("a refused create discarded what was staged: " + JSON.stringify(stagedNames()));
  // Save still means create, because there is no task to attach to.
  if (saveButton().textContent !== "Save") {
    throw new Error("a refused create re-captioned Save to " + JSON.stringify(saveButton().textContent));
  }
  if (saveButton().disabled) throw new Error("a refused create left Save disabled");
  // And the list is the reader's again: the run froze it, and a run that made
  // no task has to hand it back or the draft cannot be changed before a retry.
  const frozen = findElements(panelSection("attachments"), (element) => element.tagName === "BUTTON")
    .filter((button) => button.disabled);
  if (frozen.length !== 0) {
    throw new Error("a refused create left the staged list frozen: " +
      JSON.stringify(frozen.map((button) => button.textContent)));
  }
  await form.eventListeners.submit({ preventDefault() {} });
  if (creates !== 2) throw new Error("saving again after a refused create attempted " + creates + " creates");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the refused create: %v\n%s", err, output)
	}
}

// The create succeeded and one of its attachments did not.
//
// This is the case the whole design is arranged around. The task exists and is
// named; the attachment that failed says why, on its own row, and is still
// staged; the ones that landed are gone from the list; and the form stays,
// because a File cannot be handed back through a notice the way a typed draft
// can. Every staged item is attempted, so a reader gets a reason for each rather
// than one reason and a queue of untried ones.
func TestHandlerClientReportsTheAttachmentsACreateCouldNotAttach(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      // The middle one fails. The one after it is still attempted.
      const sent = JSON.parse(options.body);
      if (sent.name === "middle.log") {
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "validation", message: "attachment name is not valid" }
        }) };
      }
      return { ok: true, json: async () => created };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  await stageFiles([
    new TestFile("first.log", 5, "first"),
    new TestFile("middle.log", 6, "middle"),
    new TestFile("last.log", 4, "last")
  ]);
  await form.eventListeners.submit({ preventDefault() {} });

  if (createWrites().length !== 1) throw new Error("creates = " + createWrites().length);
  const uploads = attachmentWrites();
  if (uploads.length !== 3) {
    throw new Error("a failure stopped the run: uploads = " + uploads.length);
  }

  // Nothing navigated. The files that did not land are only here.
  if (main.firstElementChild === boardView) {
    throw new Error("a create with outstanding attachments left the form that holds them");
  }
  const said = findElement(main, (element) => hasDataKey(element, "saveStatus")).textContent;
  if (!said.includes(`+strconv.Quote(createAttachmentTaskID)+`) || !said.includes("was created")) {
    throw new Error("the report does not say the task exists: " + JSON.stringify(said));
  }
  if (!said.includes("1 attachment was not attached")) {
    throw new Error("the report does not count what failed: " + JSON.stringify(said));
  }

  // Exactly the failure is left, carrying exactly its reason.
  const left = stagedNames();
  if (left.length !== 1 || left[0] !== "middle.log") {
    throw new Error("the staged list after a partial failure = " + JSON.stringify(left));
  }
  if (!stagedFailure(stagedRows()[0]).includes("attachment name is not valid")) {
    throw new Error("the failed row does not say why: " + JSON.stringify(stagedFailure(stagedRows()[0])));
  }

  // Save is the retry now, and there is a way to the task without one.
  if (saveButton().textContent !== "Retry attachments") {
    throw new Error("Save says " + JSON.stringify(saveButton().textContent));
  }
  if (saveButton().disabled) throw new Error("the retry control is disabled");
  // The list is handed back too, or the reader cannot remove what they have
  // given up on or add what they meant to bring.
  const stillFrozen = findElements(panelSection("attachments"), (element) => element.tagName === "BUTTON")
    .filter((button) => button.disabled);
  if (stillFrozen.length !== 0) {
    throw new Error("the outstanding list stayed frozen: " +
      JSON.stringify(stillFrozen.map((button) => button.textContent)));
  }
  const open = openTaskLink();
  if (!open || open.hidden || open.href !== "/tasks/" + encodeURIComponent(`+strconv.Quote(createAttachmentTaskID)+`)) {
    throw new Error("there is no way from this form to the task it made");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the partial attachment failure: %v\n%s", err, output)
	}
}

// Leaving the form while it is still holding attachments is the one way those
// files can be lost, so it is the one thing this client will not let happen
// quietly. The notice names the task, names what did not attach, and points at
// the only place it can still be attached — it cannot offer the files back,
// because a File leaves with the node holding it, which is exactly why it has
// to say so instead.
func TestHandlerClientAnnouncesAttachmentsAbandonedWithTheForm(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "operational", message: "the ref moved under that write" }
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  await stageFiles([new TestFile("trace.log", 5, "trace")]);
  await stageLink("https://example.test/design", "Design doc");
  await form.eventListeners.submit({ preventDefault() {} });
  if (stagedNames().length !== 2) throw new Error("the refused uploads did not stay staged");
  if (!notice.hidden) throw new Error("a form that is still holding the files announced a loss");

  // Back to the board, which is the render that destroys the form.
  returnTo("/");
  if (main.firstElementChild !== boardView) throw new Error("Back did not leave the form");
  if (notice.hidden) throw new Error("leaving the form said nothing about the files it took");
  const said = notice.textContent;
  if (!said.includes(`+strconv.Quote(createAttachmentTaskID)+`) || !said.includes("was created")) {
    throw new Error("the notice does not name the task: " + JSON.stringify(said));
  }
  if (!said.includes("trace.log") || !said.includes("Design doc")) {
    throw new Error("the notice does not name what was lost: " + JSON.stringify(said));
  }
  if (!findElement(notice, (element) => element.tagName === "A" &&
      element.href === "/tasks/" + encodeURIComponent(`+strconv.Quote(createAttachmentTaskID)+`))) {
    throw new Error("the notice does not point at the task the files belong to");
  }
  // Said once. A second render is not a second loss.
  const before = notice.textContent;
  returnTo("/tasks/new");
  returnTo("/");
  if (notice.textContent !== before) {
    throw new Error("the loss was announced twice: " + JSON.stringify(notice.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the abandoned attachment notice: %v\n%s", err, output)
	}
}

// A reader who removes the rows that failed has decided against them, so
// leaving afterwards announces nothing. The departure report is about a list,
// and it follows that list rather than the moment the list once had.
func TestHandlerClientAnnouncesNoLossWhenTheFailedRowsAreRemoved(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "operational", message: "the ref moved under that write" }
      }) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  await stageFiles([new TestFile("trace.log", 5, "trace")]);
  await stageLink("https://example.test/design", "Design doc");
  await form.eventListeners.submit({ preventDefault() {} });
  if (stagedNames().length !== 2) throw new Error("the refused uploads did not stay staged");

  // One row back: something is still owed, so a departure would still say so.
  rowControl(stagedRows()[0], "Remove").eventListeners.click();
  if (stagedNames().length !== 1) throw new Error("Remove did not take a row out");

  // And the last one: nothing is owed now.
  rowControl(stagedRows()[0], "Remove").eventListeners.click();
  if (stagedRows().length !== 0) throw new Error("the list did not empty");
  returnTo("/");
  if (main.firstElementChild !== boardView) throw new Error("Back did not leave the form");
  if (!notice.hidden) {
    throw new Error("leaving after removing every failed row announced a loss: " +
      JSON.stringify(notice.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the abandoned attachment notice: %v\n%s", err, output)
	}
}

// The retry sends what is left to the task that already exists, and never makes
// a second one. That is the reason the form holds an ID rather than re-entering
// the create path: a reader pressing Save twice must end up with one task.
func TestHandlerClientRetriesOutstandingAttachmentsAgainstTheSameTask(t *testing.T) {
	node := requireNode(t)
	mutation, refreshed := createdTaskJSON(t)
	program := createAttachmentProgram(t, `
const created = `+mutation+`;
const refreshed = `+refreshed+`;
setTimeout(async () => {
  const form = await openCreateForm();
  let refuseUploads = true;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => created };
    }
    if (options.method === "POST") {
      if (refuseUploads) {
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "validation", message: "the ref moved under that write" }
        }) };
      }
      return { ok: true, json: async () => created };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => refreshed };
  };

  typeTitle(form, "Task with attachments");
  await stageFiles([new TestFile("trace.log", 5, "trace")]);
  await stageLink("https://example.test/design");
  await form.eventListeners.submit({ preventDefault() {} });

  if (stagedNames().length !== 2) throw new Error("the refused uploads did not stay staged");
  const afterCreate = attachmentWrites().length;
  if (afterCreate !== 2) throw new Error("uploads attempted = " + afterCreate);

  // A retry that is refused again changes nothing but the reason on each row.
  await form.eventListeners.submit({ preventDefault() {} });
  if (createWrites().length !== 1) {
    throw new Error("a retry made another task: creates = " + createWrites().length);
  }
  if (attachmentWrites().length !== afterCreate + 2) {
    throw new Error("a refused retry did not re-send both: " + attachmentWrites().length);
  }
  if (stagedNames().length !== 2) throw new Error("a refused retry lost something");

  // And one that lands sends each item once, keeps the same task, and leaves
  // for the page that can prove the attachments are there.
  // Two retries at once send one round of writes, not two. A disabled submit
  // button does not stop a Return pressed in a field, so the run guards itself.
  const overlapping = attachmentWrites().length;
  const first = form.eventListeners.submit({ preventDefault() {} });
  const second = form.eventListeners.submit({ preventDefault() {} });
  await Promise.all([first, second]);
  if (attachmentWrites().length !== overlapping + 2) {
    throw new Error("overlapping retries sent " + (attachmentWrites().length - overlapping) + " writes, want 2");
  }

  refuseUploads = false;
  await form.eventListeners.submit({ preventDefault() {} });
  if (createWrites().length !== 1) {
    throw new Error("the successful retry made another task: creates = " + createWrites().length);
  }
  if (attachmentWrites().length !== overlapping + 4) {
    throw new Error("the successful retry sent " + (attachmentWrites().length - overlapping - 2) + " writes, want 2");
  }
  if (stagedRows().length !== 0) throw new Error("what landed is still staged");
  const wanted = "/tasks/" + encodeURIComponent(`+strconv.Quote(createAttachmentTaskID)+`);
  if (historyPaths[historyPaths.length - 1] !== wanted) {
    throw new Error("the retry landed on " + JSON.stringify(historyPaths));
  }
  // Every write of every attempt named the same task.
  const strays = fetchCalls.filter((call) => call.options.method === "POST" &&
    call.url !== "/api/tasks" && !call.url.includes(encodeURIComponent(`+strconv.Quote(createAttachmentTaskID)+`)));
  if (strays.length !== 0) throw new Error("a retry attached to another task: " + JSON.stringify(strays.map((call) => call.url)));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the outstanding attachment retry: %v\n%s", err, output)
	}
}
