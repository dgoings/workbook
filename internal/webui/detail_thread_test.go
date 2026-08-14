package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The task page's comment thread and attachment list, as a browser runs them.
//
// Every case here is about one of three things: what the page draws from a task
// it was handed, what it sends when somebody changes something, and what it does
// with text somebody else wrote. The third is the reason this file is long.

const (
	threadPageTaskID  = "WB-01J0000000000000000000TH01"
	threadPageOldest  = "01K0M6B8A4FTT8C39MXXYTWC01"
	threadPageMiddle  = "01K0M6B8A4FTT8C39MXXYTWC02"
	threadPageNewest  = "01K0M6B8A4FTT8C39MXXYTWC03"
	threadPageFileID  = "01K0M6B8A4FTT8C39MXXYTWA01"
	threadPageLinkID  = "01K0M6B8A4FTT8C39MXXYTWA02"
	threadPageEvilURL = "01K0M6B8A4FTT8C39MXXYTWA03"
	threadPageRelURL  = "01K0M6B8A4FTT8C39MXXYTWA04"
)

// threadPageTask is one task carrying a thread, an attached file, a link, and a
// link no clone of this build could have authored — which is exactly the sort a
// page has to be able to draw without acting on it.
func threadPageTask() core.Task {
	stamp := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	edited := stamp.Add(time.Hour)
	task := clientPlacementTask(threadPageTaskID, "Commented task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Comments = []core.Comment{
		{ID: threadPageOldest, Author: "first@example.com", Body: "Oldest remark.", CreatedAt: stamp},
		{ID: threadPageMiddle, Author: "second@example.com", Body: "Second remark.", CreatedAt: stamp.Add(time.Minute), EditedAt: &edited},
	}
	task.Attachments = []core.Attachment{
		{ID: threadPageFileID, Author: "first@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Name: "trace.log", Kind: core.AttachmentFile, Media: "text/plain", Size: 2048, Blob: strings.Repeat("a", 40),
		}},
		{ID: threadPageLinkID, Author: "first@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
	}
	return task
}

// threadPageProgram builds the Node program for a task page carrying these
// tasks, with the given body appended.
func threadPageProgram(t *testing.T, tasks []core.Task, body string) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET the task page status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	// The helpers every one of these programs reaches for: the panels, their
	// rows and their controls, found the way a reader finds them.
	const helpers = `
function panelSection(name) {
  return findElement(main, (element) => element.dataset.taskPanel === name);
}
function panelRows(name) {
  const panel = panelSection(name);
  if (!panel) throw new Error("the page has no " + name + " panel");
  const list = findElement(panel, (element) => hasDataKey(element, "panelList"));
  return list.children;
}
function panelStatusText(name) {
  const panel = panelSection(name);
  const line = panel && findElement(panel, (element) => hasDataKey(element, "panelStatus"));
  return line ? line.textContent : "";
}
// The attachment panel draws two forms — one per intent — so a test names the
// one it means rather than taking the first it finds.
function attachmentForm(which) {
  const panel = panelSection("attachments");
  const key = which === "file" ? "attachmentFileForm" : "attachmentLinkForm";
  const form = findElement(panel, (element) => hasDataKey(element, key));
  if (!form) throw new Error("the attachments panel has no " + which + " form");
  return form;
}
function submitForm(form) {
  return form.eventListeners.submit({ preventDefault() {} });
}
function rowControl(row, caption) {
  return findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === caption);
}
// What a comment reads as, whatever elements were built to draw it.
//
// This helper used to assert that a body had no children at all, which was the
// whole of "a comment is text": there was no renderer, so an element under a
// body could only have come from parsing markup. Bodies carry formatting now,
// so the guarantee moved rather than went away — every element under one is
// checked against the renderer's whitelist here, in the same place, so a body
// that drew a <script> or an onerror fails exactly as loudly as it used to.
function commentBodyText(row) {
  const body = findElement(row, (element) => hasDataKey(element, "commentBody"));
  if (!body) return null;
  const findings = markdownViolations(body);
  if (findings.length) throw new Error("a comment body drew what it must not: " + findings.join("; "));
  return body.textContent;
}
`
	return clientDOMHarness("/tasks/"+tasks[0].ID, string(document)) + script + helpers + body
}

// The thread is drawn oldest first, each comment carrying who wrote it, when,
// and whether it has been edited since.
func TestHandlerClientDrawsTheThreadOldestFirst(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, `
setTimeout(() => {
  const rows = panelRows("comments");
  if (rows.length !== 2) throw new Error("comment rows = " + rows.length);
  if (rows[0].dataset.commentId !== `+strconv.Quote(threadPageOldest)+` ||
      rows[1].dataset.commentId !== `+strconv.Quote(threadPageMiddle)+`) {
    throw new Error("the thread is not in creation order: " + rows.map((row) => row.dataset.commentId).join(","));
  }
  if (commentBodyText(rows[0]) !== "Oldest remark.") {
    throw new Error("the first comment's body = " + JSON.stringify(commentBodyText(rows[0])));
  }
  const attribution = rows[0].children[0].textContent;
  if (!attribution.includes("first@example.com")) {
    throw new Error("a comment does not say who wrote it: " + JSON.stringify(attribution));
  }
  if (attribution.includes("edited")) {
    throw new Error("an unedited comment is marked edited: " + JSON.stringify(attribution));
  }
  if (!rows[1].children[0].textContent.includes("edited")) {
    throw new Error("an edited comment is not marked: " + JSON.stringify(rows[1].children[0].textContent));
  }
  const count = findElement(panelSection("comments"), (element) => hasDataKey(element, "panelCount"));
  if (count.textContent !== "2 comments") throw new Error("count = " + JSON.stringify(count.textContent));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the rendered thread: %v\n%s", err, output)
	}
}

// A comment body is somebody's text and is drawn as text: no markup in it
// becomes an element, and nothing on this page has ever parsed HTML.
func TestHandlerClientDrawsHostileCommentBodiesAsText(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	hostile := `<img src=x onerror="fetch('/api/tasks',{method:'DELETE'})"><script>alert(1)</script>`
	task.Comments = []core.Comment{{
		ID:        threadPageOldest,
		Author:    `<b>author</b>@example.com`,
		Body:      hostile,
		CreatedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
	}}
	task.Attachments = []core.Attachment{
		{ID: threadPageFileID, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Name: `<script>alert(1)</script>.log`, Kind: core.AttachmentFile, Media: "text/plain", Size: 3, Blob: strings.Repeat("a", 40),
		}},
		{ID: threadPageLinkID, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/x", Label: `"><script>alert(1)</script>`,
		}},
	}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
setTimeout(() => {
  const rows = panelRows("comments");
  if (commentBodyText(rows[0]) !== `+strconv.Quote(hostile)+`) {
    throw new Error("the hostile body was not drawn verbatim as text: " + JSON.stringify(commentBodyText(rows[0])));
  }
  const attachments = panelRows("attachments");
  const name = findElement(attachments[0], (element) => hasClassToken(element, "attachment__name"));
  const link = findElement(name, (element) => element.tagName === "A");
  if (!link || link.textContent !== `+strconv.Quote(`<script>alert(1)</script>.log`)+`) {
    throw new Error("the attachment name was not drawn as text: " + JSON.stringify(link && link.textContent));
  }
  if (link.children.length !== 0) throw new Error("the attachment name drew child elements");
  const label = findElement(attachments[1], (element) => hasClassToken(element, "attachment__name"));
  if (label.textContent !== `+strconv.Quote(`"><script>alert(1)</script>`)+`) {
    throw new Error("the link label was not drawn as text: " + JSON.stringify(label.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the hostile thread render: %v\n%s", err, output)
	}
}

// Core refuses anything but http and https when somebody attaches a link, and
// that rule is about authoring: a pack from another clone can carry any URL.
// A stored `javascript:` link drawn with an href would be script on this
// board's own origin, one click away, so it is drawn as text instead.
func TestHandlerClientNeverDrawsAnUnsafeLinkAsALink(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	task.Attachments = []core.Attachment{
		{ID: threadPageLinkID, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
		{ID: threadPageEvilURL, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "javascript:fetch('/api/tasks',{method:'POST'})", Label: "Read me",
		}},
		{ID: threadPageFileID, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==", Label: "Notes",
		}},
		// A relative URL is the quiet one. Resolved against this page it would
		// become a link to the board itself — /api/tasks/…/delete, say — drawn
		// under whatever label its author chose, which is a link this board
		// invented rather than one anybody stored.
		{ID: threadPageRelURL, Author: "a@example.com", AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "../../api/tasks", Label: "Relative",
		}},
	}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
setTimeout(() => {
  const rows = panelRows("attachments");
  const anchors = rows.map((row) => {
    const name = findElement(row, (element) => hasClassToken(element, "attachment__name"));
    return findElement(name, (element) => element.tagName === "A");
  });
  if (!anchors[0] || anchors[0].href !== "https://example.test/design") {
    throw new Error("an http link was not drawn as a link: " + JSON.stringify(anchors[0] && anchors[0].href));
  }
  if (anchors[0].rel !== "noopener noreferrer nofollow" || anchors[0].target !== "_blank") {
    throw new Error("a link attachment opens without its guards: " + anchors[0].rel + " " + anchors[0].target);
  }
  if (anchors[1] || anchors[2] || anchors[3]) {
    throw new Error("a javascript:, data: or relative attachment was drawn as a link");
  }
  const relative = findElement(rows[3], (element) => hasClassToken(element, "attachment__name"));
  if (relative.textContent !== "Relative") {
    throw new Error("the relative link is not drawn as its label: " + JSON.stringify(relative.textContent));
  }
  const unsafe = findElement(rows[1], (element) => hasClassToken(element, "attachment__name"));
  if (unsafe.textContent !== "Read me") {
    throw new Error("the refused link is not drawn as its label: " + JSON.stringify(unsafe.textContent));
  }
  // The destination is still shown, as text, because a reader deciding whether
  // to follow a link is entitled to see where it goes.
  const meta = findElement(rows[1], (element) => hasClassToken(element, "attachment__meta"));
  if (!meta.textContent.includes("javascript:")) {
    throw new Error("the refused link's destination is hidden: " + JSON.stringify(meta.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the unsafe link render: %v\n%s", err, output)
	}
}

// A file's row links to the download route, in a tab of its own so the reader
// keeps the task they are reading.
func TestHandlerClientLinksAFileToItsDownloadRoute(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, `
setTimeout(() => {
  const rows = panelRows("attachments");
  const name = findElement(rows[0], (element) => hasClassToken(element, "attachment__name"));
  const link = findElement(name, (element) => element.tagName === "A");
  const want = "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) +
    "/attachments/" + encodeURIComponent(`+strconv.Quote(threadPageFileID)+`);
  if (!link || link.href !== want) {
    throw new Error("the file does not link to its download route: " + JSON.stringify(link && link.href));
  }
  if (link.target !== "_blank") throw new Error("the download replaces the task page");
  const meta = findElement(rows[0], (element) => hasClassToken(element, "attachment__meta"));
  for (const part of ["file", "text/plain", "2 KiB", "first@example.com"]) {
    if (!meta.textContent.includes(part)) {
      throw new Error("the file's line does not state " + part + ": " + JSON.stringify(meta.textContent));
    }
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the attachment render: %v\n%s", err, output)
	}
}

// Adding a comment sends the body and the head this page read, and the panel
// draws the thread the server answered with.
func TestHandlerClientAddsACommentAgainstTheHeadItRead(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	answered.Comments = append(append([]core.Comment(nil), task.Comments...), core.Comment{
		ID: threadPageNewest, Author: "me@example.com", Body: "Newest remark.",
		CreatedAt: time.Date(2026, time.August, 13, 11, 0, 0, 0, time.UTC),
	})
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const panel = panelSection("comments");
  const area = findElement(panel, (element) => element.tagName === "TEXTAREA");
  const form = findElement(panel, (element) => element.tagName === "FORM");
  area.value = "Newest remark.";
  await form.eventListeners.submit({ preventDefault() {} });

  const writes = fetchCalls.filter((call) => (call.options.method || "GET") !== "GET");
  if (writes.length !== 1) throw new Error("writes = " + writes.length);
  if (writes[0].options.method !== "POST" ||
      writes[0].url !== "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) + "/comments") {
    throw new Error("the comment went to " + writes[0].options.method + " " + writes[0].url);
  }
  const sent = JSON.parse(writes[0].options.body);
  if (sent.body !== "Newest remark." || sent.expectedHead !== "head-1") {
    throw new Error("the comment carried " + JSON.stringify(sent));
  }
  if (writes[0].options.headers["Content-Type"] !== "application/json") {
    throw new Error("the comment did not declare JSON");
  }
  if (area.value !== "") throw new Error("the add form kept the comment it sent");
  const rows = panelRows("comments");
  if (rows.length !== 3 || rows[2].dataset.commentId !== `+strconv.Quote(threadPageNewest)+`) {
    throw new Error("the answer's thread was not drawn: " + rows.length);
  }
  if (panelStatusText("comments") !== "") {
    throw new Error("an accepted comment reported something: " + JSON.stringify(panelStatusText("comments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the comment add: %v\n%s", err, output)
	}
}

// A comment change moves the task's head, and the form above it follows: a save
// made afterwards must not be refused as a conflict with a change the reader
// made on the same page seconds earlier.
func TestHandlerClientFormFollowsTheHeadAThreadChangeMoved(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const panel = panelSection("comments");
  const area = findElement(panel, (element) => element.tagName === "TEXTAREA");
  area.value = "A remark.";
  await findElement(panel, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });

  const title = findElement(main, (element) => element.id === "task-title");
  title.value = "Renamed after commenting";
  const form = findElement(main, (element) => element.tagName === "FORM" && hasClassToken(element, "task-layout"));
  await form.eventListeners.submit({ preventDefault() {} });
  const saves = fetchCalls.filter((call) => call.options.method === "PATCH");
  if (saves.length !== 1) throw new Error("saves = " + saves.length);
  const sent = JSON.parse(saves[0].options.body);
  if (sent.expectedHead !== "head-2") {
    throw new Error("the save named the head the comment moved past: " + JSON.stringify(sent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the head adoption: %v\n%s", err, output)
	}
}

// Editing is in place and direct: Edit opens the body in a field, Save sends the
// edit, and Remove acts immediately — the page's other removals do too.
func TestHandlerClientEditsAndRemovesAComment(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	edited := task
	edited.Head = "head-2"
	stamp := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	edited.Comments = []core.Comment{
		{ID: threadPageOldest, Author: "first@example.com", Body: "Reworded.", CreatedAt: stamp, EditedAt: &stamp},
		task.Comments[1],
	}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const edited = `+string(mustJSON(t, edited))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: edited }) };
  };
  const commentPath = "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) +
    "/comments/" + encodeURIComponent(`+strconv.Quote(threadPageOldest)+`);

  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  let row = panelRows("comments")[0];
  const editor = findElement(row, (element) => hasDataKey(element, "commentEditor"));
  if (!editor || editor.value !== "Oldest remark.") {
    throw new Error("Edit did not open the comment's own text: " + JSON.stringify(editor && editor.value));
  }
  if (globalThis.activeElement !== editor) throw new Error("Edit did not put the caret in the field");
  editor.value = "Reworded.";
  await rowControl(row, "Save").eventListeners.click();

  const edits = fetchCalls.filter((call) => call.options.method === "PATCH");
  if (edits.length !== 1 || edits[0].url !== commentPath) {
    throw new Error("the edit went to " + JSON.stringify(edits.map((call) => call.url)));
  }
  const sent = JSON.parse(edits[0].options.body);
  if (sent.body !== "Reworded." || sent.expectedHead !== "head-1") {
    throw new Error("the edit carried " + JSON.stringify(sent));
  }
  row = panelRows("comments")[0];
  if (commentBodyText(row) !== "Reworded.") {
    throw new Error("the row did not return to reading the new body: " + JSON.stringify(commentBodyText(row)));
  }

  // Removal is one press. Nothing on this page confirms a removal, and the
  // change log still holds what the comment said.
  await rowControl(row, "Remove").eventListeners.click();
  const removals = fetchCalls.filter((call) => call.options.method === "DELETE");
  if (removals.length !== 1 || removals[0].url !== commentPath) {
    throw new Error("the removal went to " + JSON.stringify(removals.map((call) => call.url)));
  }
  if (JSON.parse(removals[0].options.body).expectedHead !== "head-2") {
    throw new Error("the removal did not name the head the edit moved to: " + removals[0].options.body);
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the comment edit and removal: %v\n%s", err, output)
	}
}

// A poll lands once a second, and the reader may be halfway through an edit when
// it does. The thread follows the board; the sentence being typed does not move.
func TestHandlerClientKeepsAnOpenCommentEditAcrossAPoll(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	arrived := task
	stamp := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	arrived.Comments = append(append([]core.Comment(nil), task.Comments...), core.Comment{
		ID: threadPageNewest, Author: "teammate@example.com", Body: "Arrived while you were typing.", CreatedAt: stamp,
	})
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const arrived = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{arrived},
		Presentation: presentationForTasks([]core.Task{arrived}),
	}))+`;
setTimeout(async () => {
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  const editor = findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor"));
  editor.value = "Half a sentence";

  taskResponse = arrived;
  await intervalCallback();

  const rows = panelRows("comments");
  if (rows.length !== 3) throw new Error("the teammate's comment did not arrive: " + rows.length);
  if (rows[2].dataset.commentId !== `+strconv.Quote(threadPageNewest)+`) {
    throw new Error("the arrival is not in the thread");
  }
  const stillOpen = findElement(rows[0], (element) => hasDataKey(element, "commentEditor"));
  if (stillOpen !== editor) throw new Error("the poll rebuilt the row being edited");
  if (stillOpen.value !== "Half a sentence") {
    throw new Error("the poll took the edit with it: " + JSON.stringify(stillOpen.value));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the poll during an edit: %v\n%s", err, output)
	}
}

// A comment removed elsewhere while the reader is editing it keeps its row and
// says so, rather than taking the words away mid-sentence.
func TestHandlerClientKeepsAnEditWhoseCommentWasRemovedElsewhere(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	shortened := task
	shortened.Comments = []core.Comment{task.Comments[1]}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const shortened = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{shortened},
		Presentation: presentationForTasks([]core.Task{shortened}),
	}))+`;
setTimeout(async () => {
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  const editor = findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor"));
  editor.value = "Words worth keeping";

  taskResponse = shortened;
  await intervalCallback();

  const rows = panelRows("comments");
  const kept = rows.find((row) => row.dataset.commentId === `+strconv.Quote(threadPageOldest)+`);
  if (!kept) throw new Error("the row being edited was taken away");
  // And it is where it was. Appended to the end it would have jumped down the
  // page under the caret of somebody mid-sentence.
  if (rows[0] !== kept) {
    throw new Error("the kept row moved to position " + rows.indexOf(kept));
  }
  const stillOpen = findElement(kept, (element) => hasDataKey(element, "commentEditor"));
  if (!stillOpen || stillOpen.value !== "Words worth keeping") {
    throw new Error("the edit did not survive: " + JSON.stringify(stillOpen && stillOpen.value));
  }
  const note = findElement(kept, (element) => hasClassToken(element, "comment__note"));
  if (!note || !note.textContent.includes("removed elsewhere")) {
    throw new Error("the row does not say what happened: " + JSON.stringify(note && note.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the removal during an edit: %v\n%s", err, output)
	}
}

// A refused change is reported in the panel it was made in, and a stale head is
// re-based so the retry is made against the version that exists.
func TestHandlerClientReportsARefusedThreadChangeInPlace(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	moved := task
	moved.Head = "head-9"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const moved = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{moved},
		Presentation: presentationForTasks([]core.Task{moved}),
	}))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    taskResponse = moved;
    return { ok: false, json: async () => ({
      format: "workbook.error", version: 1,
      error: { category: "stale-write", message: "task WB-… has moved on" }
    }) };
  };
  const panel = panelSection("comments");
  const area = findElement(panel, (element) => element.tagName === "TEXTAREA");
  area.value = "A remark.";
  await findElement(panel, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });

  const reported = panelStatusText("comments");
  if (!reported.includes("changed elsewhere") || !reported.includes("try again")) {
    throw new Error("the refusal was not reported in the panel: " + JSON.stringify(reported));
  }
  // The text stays where it was typed: nothing else on this page throws away
  // what a refusal protected.
  if (area.value !== "A remark.") throw new Error("the refused comment was discarded: " + JSON.stringify(area.value));

  // And the next attempt names the head the refusal re-based onto.
  await findElement(panel, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 2) throw new Error("writes = " + writes.length);
  if (JSON.parse(writes[1].options.body).expectedHead !== "head-9") {
    throw new Error("the retry did not re-base: " + writes[1].options.body);
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the refused thread change: %v\n%s", err, output)
	}
}

// A file too large for the ceiling is refused before it is read, encoded or
// sent, and the refusal names the ceiling the server holds. The server enforces
// it too; this is the reader being told now rather than a megabyte later.
//
// A file one byte over the ceiling is also the case where a rounded unit says
// nothing: "1 MiB and must not exceed 1 MiB" reads as a refusal of something
// allowed, so a pair the unit cannot separate is given in bytes — which is what
// the server's own refusal says. A file that is plainly larger keeps the unit.
func TestHandlerClientRefusesAnOversizedUploadBeforeSendingIt(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, `
`+fileHarness+`
setTimeout(async () => {
  const before = fetchCalls.length;
  const panel = panelSection("attachments");
  const chooser = findElement(panel, (element) => element.id === "attachment-file");
  chooser.files = [new TestFile("enormous.bin", `+strconv.Itoa(core.MaxAttachmentFileBytes+1)+`, "x")];
  await submitForm(attachmentForm("file"));

  const reported = panelStatusText("attachments");
  if (!reported.includes("enormous.bin") || !reported.includes("attach a link instead")) {
    throw new Error("the refusal does not name the file: " + JSON.stringify(reported));
  }
  // Both sides in bytes, because both round to the same unit.
  if (!reported.includes("`+strconv.Itoa(core.MaxAttachmentFileBytes+1)+` bytes") ||
      !reported.includes("`+strconv.Itoa(core.MaxAttachmentFileBytes)+` bytes")) {
    throw new Error("a one-byte overrun was described in units that cannot tell it apart: " + JSON.stringify(reported));
  }
  if (fetchCalls.length !== before) throw new Error("an over-sized file was sent anyway");
  if (readCalls.length !== 0) throw new Error("an over-sized file was read anyway");

  // A file the unit can separate keeps the unit, which is what a reader can
  // actually judge a download by.
  chooser.files = [new TestFile("enormous.bin", `+strconv.Itoa(core.MaxAttachmentFileBytes*9)+`, "x")];
  await submitForm(attachmentForm("file"));
  const second = panelStatusText("attachments");
  if (!second.includes("9 MiB") || !second.includes("1 MiB")) {
    throw new Error("a plainly larger file was not described in units: " + JSON.stringify(second));
  }
  if (fetchCalls.length !== before) throw new Error("an over-sized file was sent anyway");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the oversized upload: %v\n%s", err, output)
	}
}

// Choosing a file and pressing Return attaches the file.
//
// It used to attach nothing: both controls sat in one form, so Return submitted
// the link half — "A link needs a URL", no request, and the file the reader had
// chosen still sitting there. Each control is its own form now, so Return in
// either one submits the intent it belongs to.
func TestHandlerClientAttachesTheFileWhenTheFileFormIsSubmitted(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
`+fileHarness+`
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const file = attachmentForm("file");
  const link = attachmentForm("link");
  if (file === link) throw new Error("the two adders are still one form");

  const chooser = findElement(panelSection("attachments"), (element) => element.id === "attachment-file");
  chooser.files = [new TestFile("notes.txt", 5, "notes")];
  await submitForm(file);

  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 1) {
    throw new Error("submitting the file form sent " + writes.length + " requests; panel says " +
      JSON.stringify(panelStatusText("attachments")));
  }
  const sent = JSON.parse(writes[0].options.body);
  if (sent.kind !== "file" || sent.name !== "notes.txt") {
    throw new Error("the file form sent " + JSON.stringify(sent));
  }
  if (panelStatusText("attachments") !== "") {
    throw new Error("an accepted upload reported something: " + JSON.stringify(panelStatusText("attachments")));
  }

  // The link form, meanwhile, still refuses to send an empty URL and says so —
  // which is the answer that used to swallow a file upload.
  await submitForm(link);
  if (!panelStatusText("attachments").includes("needs a URL")) {
    throw new Error("the link form said " + JSON.stringify(panelStatusText("attachments")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the file form submit: %v\n%s", err, output)
	}
}

// An acceptable file is sent as base64 in JSON, with no media type: the name
// decides that, through the table core keeps, because a media type is written
// into shared history and the browser's guess differs from machine to machine.
func TestHandlerClientUploadsAFileAsBase64JSON(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
`+fileHarness+`
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const panel = panelSection("attachments");
  const chooser = findElement(panel, (element) => element.id === "attachment-file");
  chooser.files = [new TestFile("screenshot.svg", 5, "hello", "image/svg+xml")];
  await submitForm(attachmentForm("file"));

  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 1 ||
      writes[0].url !== "/api/tasks/" + encodeURIComponent(`+strconv.Quote(threadPageTaskID)+`) + "/attachments") {
    throw new Error("the upload went to " + JSON.stringify(writes.map((call) => call.url)));
  }
  if (writes[0].options.headers["Content-Type"] !== "application/json") {
    throw new Error("the upload did not declare JSON, which the same-origin guard requires");
  }
  const sent = JSON.parse(writes[0].options.body);
  if (sent.kind !== "file" || sent.name !== "screenshot.svg" || sent.expectedHead !== "head-1") {
    throw new Error("the upload carried " + JSON.stringify(sent));
  }
  if ("media" in sent) {
    throw new Error("the upload passed the browser's media guess into shared history: " + JSON.stringify(sent.media));
  }
  if (Buffer.from(sent.content, "base64").toString() !== "hello") {
    throw new Error("the upload's bytes did not survive: " + JSON.stringify(sent.content));
  }
  if (chooser.value !== "") throw new Error("the chooser kept the file it sent");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the upload: %v\n%s", err, output)
	}
}

// A link is attached from the same panel, and the fields it was typed into are
// cleared only once the server has taken it.
func TestHandlerClientAttachesALink(t *testing.T) {
	node := requireNode(t)
	task := threadPageTask()
	answered := task
	answered.Head = "head-2"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, `
const answered = `+string(mustJSON(t, answered))+`;
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: answered }) };
  };
  const panel = panelSection("attachments");
  const url = findElement(panel, (element) => element.id === "attachment-url");
  const label = findElement(panel, (element) => element.id === "attachment-label");
  url.value = "  https://example.test/pr/12  ";
  label.value = " The pull request ";
  const form = attachmentForm("link");
  // The browser must not vet this field: constraint validation cancels the
  // submit, and a cancelled submit is a click this page could not report on.
  // The harness has no constraint validation to run — it calls the submit
  // listener directly — so what is pinned here is the property a browser reads,
  // which is the whole of the fix.
  if (form.noValidate !== true) throw new Error("the link form still lets the browser vet the URL");
  await submitForm(form);

  const writes = fetchCalls.filter((call) => call.options.method === "POST");
  if (writes.length !== 1) throw new Error("writes = " + writes.length);
  const sent = JSON.parse(writes[0].options.body);
  if (sent.kind !== "link" || sent.url !== "https://example.test/pr/12" ||
      sent.label !== "The pull request" || sent.expectedHead !== "head-1") {
    throw new Error("the link carried " + JSON.stringify(sent));
  }
  if ("content" in sent || "name" in sent) throw new Error("a link carried file members: " + JSON.stringify(sent));
  if (url.value !== "" || label.value !== "") throw new Error("the link fields were not cleared");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the link attach: %v\n%s", err, output)
	}
}

// A change that outlives the page it was made on reports where the reader ended
// up, exactly as a detached save does: the panel it would have reported into is
// gone, and an outcome nobody is told is a change the reader believes happened.
func TestHandlerClientReportsAThreadChangeThatOutlivesItsPage(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, `
setTimeout(async () => {
  const boardFetch = globalThis.fetch;
  let release;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    fetchCalls.push({ url, options });
    return new Promise((resolve) => {
      release = () => resolve({ ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "validation", message: "The server refused this comment." }
      }) });
    });
  };
  const panel = panelSection("comments");
  const area = findElement(panel, (element) => element.tagName === "TEXTAREA");
  area.value = "A remark nobody waited for.";
  const sending = findElement(panel, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  await Promise.resolve();

  const back = findElement(main, (element) => element.tagName === "A" && element.textContent === "Back");
  await documentEventListeners.click({
    target: back, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  if (main.firstElementChild !== boardView) throw new Error("the reader did not end up on the board");

  release();
  await sending;

  const reports = findElements(notice, (element) => hasClassToken(element, "notice__report"));
  if (notice.hidden || reports.length !== 1) {
    throw new Error("the detached comment reported nothing: " + JSON.stringify(notice.textContent));
  }
  const copy = findElement(reports[0], (element) => element.tagName === "P").textContent;
  if (!copy.includes("Commented task") || !copy.includes("your comment was not saved")) {
    throw new Error("the report does not say what happened: " + JSON.stringify(copy));
  }
  if (!copy.endsWith("The server refused this comment.")) {
    throw new Error("the server's own sentence is not the last word: " + JSON.stringify(copy));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the detached thread change: %v\n%s", err, output)
	}
}

// The board's cards are untouched by any of this. A comment count or an
// attachment indicator on a card is not in this design, and a card that grew one
// would be a card that changes size under a poll.
func TestHandlerBoardCardsSayNothingAboutTheThread(t *testing.T) {
	tasks := []core.Task{threadPageTask()}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	card := response.Body.String()
	start := strings.Index(card, `<article class="task-card"`)
	end := strings.Index(card, "</article>")
	if start < 0 || end < start {
		t.Fatal("GET / rendered no card")
	}
	card = card[start:end]
	for _, absent := range []string{"Oldest remark", "trace.log", "Design doc", "attachment", "data-comment"} {
		if strings.Contains(strings.ToLower(card), strings.ToLower(absent)) {
			t.Errorf("a board card mentions %q: %s", absent, card)
		}
	}
}

// The page has no path that parses HTML at all. Everything the client draws is
// built as nodes and written through textContent, which is what makes every
// hostile-content case above a property of the page rather than of one renderer.
func TestHandlerClientScriptHasNoHTMLSink(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{threadPageTask()}, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+threadPageTaskID)
	script := renderedClientScript(t, response.Body.String())
	for _, sink := range []string{".innerHTML", ".outerHTML", ".insertAdjacentHTML", "document.write", "createContextualFragment"} {
		if strings.Contains(script, sink) {
			t.Errorf("the client script reaches for %s", sink)
		}
	}
}

// fileHarness is the browser file machinery the fake DOM does not have: a File
// with a name and a size, and the reader that turns one into the data URL the
// upload path slices its base64 out of. readCalls is what proves a refused file
// was never read.
const fileHarness = `
const readCalls = [];
class TestFile {
  constructor(name, size, contents, type = "") {
    this.name = name;
    this.size = size;
    this.type = type;
    this.contents = contents;
  }
}
globalThis.FileReader = class {
  readAsDataURL(file) {
    readCalls.push(file.name);
    this.result = "data:" + (file.type || "application/octet-stream") + ";base64," +
      Buffer.from(file.contents).toString("base64");
    Promise.resolve().then(() => this.onload());
  }
};
`
