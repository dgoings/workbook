package webui

import (
	"strconv"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The task page's thread as one reader reads it: an Edit only on their own
// comments, a Remove on every one, and a line for each removed comment naming
// who removed it, read from the task answer.

const threadPageRemoved = "01K0M6B8A4FTT8C39MXXYTWC00"

// threadDetailStub answers GET /api/tasks/<id> with detail and counts how
// often the page asked, passing every other request to the harness. It is
// installed as the page boots, before the first poll's answer draws the
// thread that asks for it.
func threadDetailStub(t *testing.T, detail core.TaskDetail) string {
	t.Helper()
	document := mustJSON(t, TaskDocument{Format: "workbook.task", Version: 1, Task: detail})
	return `
let detailReads = 0;
let detailAnswer = ` + string(document) + `;
let detailGate = null;
const harnessFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") === "GET" && url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(threadPageTaskID) + `)) {
    detailReads += 1;
    // A test that has to act before the answer lands holds it here.
    if (detailGate) await detailGate;
    return { ok: true, json: async () => detailAnswer };
  }
  return harnessFetch(url, options);
};
async function settle() {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
}
`
}

func threadPageDetail(task core.Task) core.TaskDetail {
	detail := core.TaskDetail{Task: task}
	for _, comment := range task.Comments {
		detail.Comments = append(detail.Comments, core.ThreadComment{Comment: comment, Editable: comment.EditableBy(threadPageReader)})
	}
	return detail
}

func TestHandlerClientOffersEditOnlyOnTheReadersOwnComments(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, threadDetailStub(t, threadPageDetail(tasks[0]))+`
setTimeout(async () => {
  await settle();
  const rows = panelRows("comments");
  if (rows.length !== 2) throw new Error("comment rows = " + rows.length);
  if (!rowControl(rows[0], "Edit")) throw new Error("the reader's own comment has no Edit");
  if (rowControl(rows[1], "Edit")) throw new Error("somebody else's comment offers an Edit");
  if (!rowControl(rows[0], "Remove") || !rowControl(rows[1], "Remove")) {
    throw new Error("every comment must offer Remove");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the per-reader edit controls: %v\n%s", err, output)
	}
}

func TestHandlerClientDrawsWhoRemovedACommentWithoutItsText(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	detail := threadPageDetail(task)
	stamp := task.Comments[0].CreatedAt
	detail.RemovedComments = []core.RemovedComment{{
		ID: threadPageRemoved, Author: "second@example.com", CreatedAt: stamp.Add(-time.Hour),
		RemovedBy: "remover@example.com", RemovedAt: stamp.Add(2 * time.Hour),
	}}
	// The middle comment's latest edit was made by somebody other than its
	// author, which the thread has to say.
	detail.Comments[1].EditedBy = "rewriter@example.com"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, detail)+`
setTimeout(async () => {
  await settle();
  if (detailReads !== 1) throw new Error("the task answer was read " + detailReads + " times, want once");
  const rows = panelRows("comments");
  if (rows.length !== 3) throw new Error("rows = " + rows.length + ", want the removed comment among the two live ones");
  const removed = rows[0];
  if (removed.dataset.commentId !== `+strconv.Quote(threadPageRemoved)+` || !hasDataKey(removed, "commentRemoved")) {
    throw new Error("the removed comment is not first, in identifier order");
  }
  const said = removed.textContent;
  if (!said.includes("second@example.com") || !said.includes("Removed by remover@example.com")) {
    throw new Error("the removed comment's line says " + JSON.stringify(said));
  }
  if (findElement(removed, (element) => element.tagName === "BUTTON")) {
    throw new Error("a removed comment offers a control");
  }
  if (findElement(removed, (element) => hasDataKey(element, "commentBody"))) {
    throw new Error("a removed comment draws a body");
  }
  const attribution = findElement(rows[2], (element) => hasClassToken(element, "comment__attribution"));
  if (!attribution || !attribution.textContent.endsWith(" by rewriter@example.com")) {
    throw new Error("an edit by someone other than the author does not say so: " + JSON.stringify(attribution && attribution.textContent));
  }
  const own = findElement(rows[1], (element) => hasClassToken(element, "comment__attribution"));
  if (own.textContent.includes(" by ")) throw new Error("an unedited comment names an editor: " + own.textContent);
  const count = findElement(panelSection("comments"), (element) => hasDataKey(element, "panelCount"));
  if (count.textContent !== "2 comments") throw new Error("count = " + JSON.stringify(count.textContent) + ", want the live comments");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the removed comment's line: %v\n%s", err, output)
	}
}

// The trace is read once per head: a poll that brings the same task asks
// nothing, and one that brings a task whose head moved asks again.
func TestHandlerClientReadsTheTraceAgainOnlyWhenTheHeadMoves(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	moved := task
	moved.Head = "head-2"
	moved.Comments = []core.Comment{task.Comments[1]}
	movedDetail := threadPageDetail(moved)
	movedDetail.RemovedComments = []core.RemovedComment{{
		ID: threadPageOldest, Author: "first@example.com", CreatedAt: task.Comments[0].CreatedAt,
		RemovedBy: "second@example.com", RemovedAt: task.Comments[0].CreatedAt.Add(time.Hour),
	}}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, threadPageDetail(task))+`
const same = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: threadPagePresentation(tasks),
	}))+`;
const movedDocument = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{moved}, Presentation: threadPagePresentation([]core.Task{moved}),
	}))+`;
const movedDetail = `+string(mustJSON(t, TaskDocument{Format: "workbook.task", Version: 1, Task: movedDetail}))+`;
setTimeout(async () => {
  await settle();
  if (detailReads !== 1) throw new Error("first reads = " + detailReads);
  taskResponse = same;
  await intervalCallback();
  await settle();
  if (detailReads !== 1) throw new Error("a poll that moved nothing read the task again: " + detailReads);

  detailAnswer = movedDetail;
  taskResponse = movedDocument;
  await intervalCallback();
  await settle();
  if (detailReads !== 2) throw new Error("a moved head did not read the task again: " + detailReads);
  const rows = panelRows("comments");
  if (rows.length !== 2 || rows[0].dataset.commentId !== `+strconv.Quote(threadPageOldest)+` || !hasDataKey(rows[0], "commentRemoved")) {
    throw new Error("the comment removed elsewhere is not drawn as removed in its place");
  }
  if (!rows[0].textContent.includes("Removed by second@example.com")) {
    throw new Error("the removal says " + JSON.stringify(rows[0].textContent));
  }
  if (rowControl(rows[1], "Edit")) throw new Error("the remaining comment is not the reader's, and offers an Edit");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the trace re-read: %v\n%s", err, output)
	}
}

// A comment route's answer says which comments the reader may edit, so the
// comment the reader just added carries its Edit at once rather than a poll
// later.
func TestHandlerClientOffersEditOnACommentTheReaderJustAdded(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	added := task
	added.Head = "head-2"
	added.Comments = append(append([]core.Comment(nil), task.Comments...), core.Comment{
		ID: threadPageNewest, Author: threadPageReader, Body: "Mine, just now.", CreatedAt: task.Comments[1].CreatedAt.Add(time.Hour),
	})
	editable := editableComments(added.Comments, threadPageReader)
	answer := mustJSON(t, TaskMutationDocument{Format: "workbook.task-mutation", Version: 1, Task: added, EditableComments: &editable})
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, threadPageDetail(task))+`
setTimeout(async () => {
  await settle();
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    return { ok: true, json: async () => (`+string(answer)+`) };
  };
  const panel = panelSection("comments");
  const area = findElement(panel, (element) => element.tagName === "TEXTAREA");
  area.value = "Mine, just now.";
  await findElement(panel, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  const rows = panelRows("comments");
  if (rows.length !== 3 || !rowControl(rows[2], "Edit")) {
    throw new Error("the reader's new comment has no Edit");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the new comment's Edit: %v\n%s", err, output)
	}
}

// A compacted task's checkpoint keeps an edit's time and not its editor. The
// task answer says so, and the thread says it in words rather than drawing
// the comment as edited by nobody in particular, which reads as its author.
func TestHandlerClientSaysWhenAnEditsEditorIsNotRecorded(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	detail := threadPageDetail(task)
	detail.Comments[1].EditorNotRecorded = true
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, detail)+`
setTimeout(async () => {
  await settle();
  const rows = panelRows("comments");
  const attribution = findElement(rows[1], (element) => hasClassToken(element, "comment__attribution"));
  const said = attribution && attribution.textContent;
  if (!said || !said.endsWith(" · editor not recorded") || said.includes(" by ")) {
    throw new Error("an edit whose editor the history lost reads " + JSON.stringify(said));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the unrecorded editor: %v\n%s", err, output)
	}
}

// An edit form opened before the task answer lands picks up who edited the
// comment when it does, and what the reader typed stays exactly as it was.
func TestHandlerClientAnOpenEditFormLearnsWhoEditedTheComment(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	edited := task.Comments[0].CreatedAt.Add(30 * time.Minute)
	task.Comments[0].EditedAt = &edited
	detail := threadPageDetail(task)
	detail.Comments[0].EditedBy = "rewriter@example.com"
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, detail)+`
let release;
detailGate = new Promise((resolve) => { release = resolve; });
setTimeout(async () => {
  await settle();
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  const row = panelRows("comments")[0];
  const editor = findElement(row, (element) => hasDataKey(element, "commentEditor"));
  editor.value = "Half a sentence";
  if (document.activeElement !== editor) throw new Error("Edit did not focus the draft");
  const before = findElement(row, (element) => hasClassToken(element, "comment__attribution")).textContent;
  if (before.includes(" by ")) throw new Error("the form named an editor before the answer said one: " + before);

  release();
  await settle();
  const after = panelRows("comments")[0];
  if (after !== row) throw new Error("the open form was rebuilt");
  const said = findElement(after, (element) => hasClassToken(element, "comment__attribution")).textContent;
  if (!said.endsWith(" by rewriter@example.com")) {
    throw new Error("the open form's attribution stayed " + JSON.stringify(said));
  }
  const still = findElement(after, (element) => hasDataKey(element, "commentEditor"));
  if (still !== editor || still.value !== "Half a sentence") {
    throw new Error("the draft was disturbed: " + JSON.stringify(still && still.value));
  }
  // Redrawing the thread moves the open row, and a browser blurs what leaves
  // the document; the reader keeps typing where they were.
  if (document.activeElement !== editor) {
    throw new Error("the redraw took focus from the draft: " + (document.activeElement && document.activeElement.tagName));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the late attribution: %v\n%s", err, output)
	}
}

// A comment removed underneath an open edit form: the form says it was
// removed and by whom, keeps the draft to copy, and neither its note nor a
// refused save invites a retry that can never succeed. Save goes; Cancel
// stays.
func TestHandlerClientSaysAnEditCannotBeSavedOnceItsCommentIsRemoved(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	task := threadPageTask()
	gone := task
	gone.Head = "head-2"
	gone.Comments = []core.Comment{task.Comments[1]}
	goneDetail := threadPageDetail(gone)
	goneDetail.RemovedComments = []core.RemovedComment{{
		ID: threadPageOldest, Author: threadPageReader, CreatedAt: task.Comments[0].CreatedAt,
		RemovedBy: "remover@example.com", RemovedAt: task.Comments[0].CreatedAt.Add(time.Hour),
	}}
	tasks := []core.Task{task}
	program := threadPageProgram(t, tasks, threadDetailStub(t, threadPageDetail(task))+`
const goneDocument = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{gone}, Presentation: threadPagePresentation([]core.Task{gone}),
	}))+`;
const goneDetail = `+string(mustJSON(t, TaskDocument{Format: "workbook.task", Version: 1, Task: goneDetail}))+`;
function noRetry(said, where) {
  if (/try again|retry|would be refused/i.test(said)) throw new Error(where + " invites a retry: " + JSON.stringify(said));
}
setTimeout(async () => {
  await settle();
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  const editor = findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor"));
  editor.value = "Words worth keeping";

  // The save races the removal: the server refuses it as stale, and the board
  // it re-reads no longer holds the comment.
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    taskResponse = goneDocument;
    detailAnswer = goneDetail;
    return { ok: false, json: async () => ({
      format: "workbook.error", version: 1,
      error: { category: "stale-write", message: "task WB-… has moved on" }
    }) };
  };
  // The task answer that names the remover is held until the refusal has
  // been reported, so the refusal is first said without a name and then
  // again with one.
  let release;
  detailGate = new Promise((resolve) => { release = resolve; });
  await rowControl(panelRows("comments")[0], "Save").eventListeners.click();
  await settle();
  const early = panelStatusText("comments");
  if (!early.startsWith("This comment was removed, so this edit cannot be saved.")) {
    throw new Error("the refused save, before the history answered, reports " + JSON.stringify(early));
  }
  noRetry(early, "the refused save");
  release();
  await settle();

  const reported = panelStatusText("comments");
  if (!reported.includes("This comment was removed by remover@example.com") || !reported.includes("copy")) {
    throw new Error("the refused save reports " + JSON.stringify(reported));
  }
  noRetry(reported, "the refused save");
  const kept = panelRows("comments").find((row) => row.dataset.commentId === `+strconv.Quote(threadPageOldest)+`);
  if (!kept || !hasDataKey(kept, "commentEditing")) throw new Error("the form was taken away");
  const note = findElement(kept, (element) => hasClassToken(element, "comment__note")).textContent;
  if (!note.includes("removed by remover@example.com") || !note.includes("cannot be saved")) {
    throw new Error("the form's note says " + JSON.stringify(note));
  }
  noRetry(note, "the form's note");
  const still = findElement(kept, (element) => hasDataKey(element, "commentEditor"));
  if (still.value !== "Words worth keeping") throw new Error("the draft is gone: " + JSON.stringify(still.value));
  const save = rowControl(kept, "Save");
  if (save && !save.hidden) throw new Error("the form still offers a Save that cannot succeed");
  const cancel = rowControl(kept, "Cancel");
  if (!cancel || cancel.hidden || cancel.disabled) throw new Error("the form cannot be closed");
  cancel.eventListeners.click();
  const closed = panelRows("comments").find((row) => row.dataset.commentId === `+strconv.Quote(threadPageOldest)+`);
  if (!closed || !hasDataKey(closed, "commentRemoved")) throw new Error("closing the form did not leave the removal's line");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the edit of a removed comment: %v\n%s", err, output)
	}
}

// The server can be first to know: a save answered "no such comment" before
// any poll has shown the removal says the comment was removed, not the bare
// refusal, and still invites no retry.
func TestHandlerClientSaysARefusedSaveOfAMissingCommentWasRemoved(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	program := threadPageProgram(t, tasks, threadDetailStub(t, threadPageDetail(tasks[0]))+`
setTimeout(async () => {
  await settle();
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor")).value = "Words worth keeping";
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return boardFetch(url, options);
    return { ok: false, json: async () => ({
      format: "workbook.error", version: 1,
      error: { category: "not-found", message: "task has no comment `+threadPageOldest+`" }
    }) };
  };
  await rowControl(panelRows("comments")[0], "Save").eventListeners.click();
  const reported = panelStatusText("comments");
  if (!reported.startsWith("This comment was removed") || /try again/i.test(reported)) {
    throw new Error("the refused save reports " + JSON.stringify(reported));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the save of a missing comment: %v\n%s", err, output)
	}
}
