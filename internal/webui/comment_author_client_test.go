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
const harnessFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") === "GET" && url === "/api/tasks/" + encodeURIComponent(` + strconv.Quote(threadPageTaskID) + `)) {
    detailReads += 1;
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
