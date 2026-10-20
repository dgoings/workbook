package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board's side of the conditional poll.
//
// The server answers every read of /api/tasks with a tag naming the document it
// sent, and answers 304 with no body when the read names the document it still
// holds. What is pinned here is what the page does with that: it names only the
// document its model actually holds, it forgets the name the moment the model
// stops being that document, and a 304 is a tick that synchronized rather than
// one that failed — the board stays drawn exactly as it is.

const (
	conditionalPollTaskID       = "WB-01J0000000000000000000CP01"
	conditionalPollDependencyID = "WB-01J0000000000000000000CP02"
)

// conditionalPollServer is the fake server every test here talks to, installed
// before the page's script so the read the page boots with goes through it too.
//
// Each read is recorded with the validator it carried, and answered by
// `serve`. The default answers the way the real route does: the document the
// URL names under the tag the test assigned it, or 304 when the read named that
// tag. `taggedAnswer` and `notModifiedAnswer` build the two shapes, and a 304
// counts the calls to its json() so a test can say no body was ever parsed.
// Writes fall through to the harness, which confirms them.
//
// The page also gets a sync indicator here and a clock the test holds, because
// the harness serves neither and "the tick counted as a sync" is a claim about
// both.
const conditionalPollServer = `
const updatedTextElement = new TestElement("span");
const syncDotElement = new TestElement("span");
const harnessQuerySelector = document.querySelector;
document.querySelector = function (selector) {
  if (selector === "[data-updated-text]") return updatedTextElement;
  if (selector === "[data-sync-dot]") return syncDotElement;
  return harnessQuerySelector.call(this, selector);
};
let clock = 1000000;
Date.now = () => clock;

const reads = [];
const answers = [];
let taskTag = "\"a\"";
let deletedTag = "\"d\"";
let includedTag = "\"i\"";
let readsFail = false;
function answerHeaders(etag) {
  return { get: (name) => (name.toLowerCase() === "etag" ? etag : null) };
}
function taggedAnswer(document, etag) {
  return { ok: true, status: 200, headers: answerHeaders(etag), json: async () => document };
}
function notModifiedAnswer(etag) {
  const answer = {
    ok: false, status: 304, headers: answerHeaders(etag), jsonCalls: 0,
    json: async () => { answer.jsonCalls += 1; throw new SyntaxError("Unexpected end of JSON input"); }
  };
  return answer;
}
function honestAnswer(url, tag) {
  let document = taskResponse;
  let etag = taskTag;
  if (url === "/api/tasks?deleted=true") { document = deletedTaskResponse; etag = deletedTag; }
  if (url === "/api/tasks?deleted=include") { document = includedTaskResponse || taskResponse; etag = includedTag; }
  return tag !== null && tag === etag ? notModifiedAnswer(etag) : taggedAnswer(document, etag);
}
let serve = honestAnswer;
const harnessFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") !== "GET") return harnessFetch(url, options);
  fetchCalls.push({ url, options });
  const headers = options.headers || {};
  const tag = Object.hasOwn(headers, "If-None-Match") ? headers["If-None-Match"] : null;
  reads.push({ url, tag });
  if (readsFail) throw new TypeError("fetch failed");
  const answer = await serve(url, tag);
  answers.push(answer);
  return answer;
};
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
function lastRead(url) {
  const matching = reads.filter((read) => read.url === url);
  return matching[matching.length - 1];
}
`

// conditionalPollProgram renders the page at path over tasks and runs body once
// the boot read has had its turn.
func conditionalPollProgram(t *testing.T, path string, tasks []core.Task, prelude, body string) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	return clientDOMHarness(path, tasksDocumentJSON(t, tasks)) + conditionalPollServer + prelude + script + `
setTimeout(async () => {
  await settle();
` + body + `
}, 0);
`
}

func runConditionalPollClient(t *testing.T, purpose, program string) {
	t.Helper()
	node := requireNode(t)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

func conditionalPollTasks() []core.Task {
	task := clientPlacementTask(conditionalPollTaskID, "Polled task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	return []core.Task{task}
}

// A 304 is the server saying the board is already showing what it holds. The
// board stays exactly as it is drawn, no body is read, and the tick counts as a
// sync: the banner a failed poll raised comes down and the age starts over.
func TestClientConditionalPollKeepsTheBoardOnNotModifiedAndMarksItSynchronized(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), "", `
  const id = `+strconv.Quote(conditionalPollTaskID)+`;
  const card = boardCard(id);
  if (!card || !card.textContent.includes("Polled task")) throw new Error("the board never drew the task it booted with");

  // A poll that cannot reach the server, twenty seconds after the last sync.
  readsFail = true;
  clock += 20000;
  await intervalCallback();
  if (stale.dataset.visible !== "true") throw new Error("the failed poll raised no banner");
  if (updatedTextElement.textContent !== "20s ago") {
    throw new Error("the indicator does not report the missed sync: " + JSON.stringify(updatedTextElement.textContent));
  }

  // The next one reaches it, and nothing has changed.
  readsFail = false;
  await intervalCallback();
  const read = lastRead("/api/tasks");
  if (read.tag !== taskTag) throw new Error("the poll named " + JSON.stringify(read.tag) + ", want " + JSON.stringify(taskTag));
  const answer = answers[answers.length - 1];
  if (answer.status !== 304) throw new Error("the poll was not answered 304, so this test proves nothing");
  if (answer.jsonCalls !== 0) throw new Error("the page tried to read a body out of a 304");
  if (boardCard(id) !== card || !card.textContent.includes("Polled task")) {
    throw new Error("the 304 changed the board it should have left alone");
  }
  if (stale.dataset.visible !== "false") throw new Error("a poll the server answered left the banner up");
  if (updatedTextElement.textContent !== "in sync" || syncDotElement.dataset.state !== "fresh") {
    throw new Error("a 304 was not counted as a sync: " + JSON.stringify(updatedTextElement.textContent) +
      " / " + JSON.stringify(syncDotElement.dataset.state));
  }
`)
	runConditionalPollClient(t, "a 304 poll", program)
}

// The page names only a tag it has been given. A read before any tagged answer
// sends no validator, and so does the read after an answer that carried none —
// which is every answer an older server gives. Once a tag arrives, the next
// poll names it.
func TestClientConditionalPollNamesOnlyATagItWasGiven(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), `
// The boot read is answered the way an older server answers: no status, no
// headers, just a body.
serve = (url) => ({ ok: true, json: async () => taskResponse });
`, `
  if (reads.length === 0 || reads[0].tag !== null) {
    throw new Error("the boot read named a tag nobody had given it: " + JSON.stringify(reads[0]));
  }
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== null) {
    throw new Error("an untagged answer left the page naming a tag: " + JSON.stringify(lastRead("/api/tasks")));
  }

  serve = (url, tag) => taggedAnswer(taskResponse, "\"a\"");
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== null) throw new Error("the read that is given the first tag already named one");
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== "\"a\"") {
    throw new Error("the poll after a tagged answer named " + JSON.stringify(lastRead("/api/tasks").tag) + ", want \"a\"");
  }
`)
	runConditionalPollClient(t, "naming only given tags", program)
}

// A read that lost the race never touched the model, so its tag names a
// document the page is not holding. Only the read that was applied records its
// tag: A leaves first and lands last, B is the one drawn, and the next poll
// names B.
func TestClientConditionalPollIgnoresTheTagOfASupersededRead(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), "", `
  let releaseA = null;
  serve = (url, tag) => {
    if (!releaseA) return new Promise((resolve) => { releaseA = () => resolve(taggedAnswer(taskResponse, "\"a\"")); });
    return taggedAnswer(taskResponse, "\"b\"");
  };
  const pollA = intervalCallback();
  await settle();
  if (!releaseA) throw new Error("refresh A never reached the server");
  const pollB = intervalCallback();
  await pollB;
  releaseA();
  await pollA;

  serve = honestAnswer;
  taskTag = "\"b\"";
  await intervalCallback();
  const read = lastRead("/api/tasks");
  if (read.tag !== "\"b\"") {
    throw new Error("the poll after a superseded read named " + JSON.stringify(read.tag) + ", want the applied read's \"b\"");
  }
`)
	runConditionalPollClient(t, "a superseded read's tag", program)
}

// The Deleted column switches the board between two documents, and the page
// holds one model. A tag for the other document is never sent: the read the
// toggle starts asks unconditionally, and so does the read that toggles back,
// because the tag the page once had for that address names a model it no
// longer holds. A toggle there and back before any poll asks in full too.
func TestClientConditionalPollForgetsTheTagWhenTheDeletedColumnToggles(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), "", `
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== taskTag) throw new Error("the board poll never named its tag, so this test proves nothing");

  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  await settle();
  const include = lastRead("/api/tasks?deleted=include");
  if (!include) throw new Error("showing the column did not read the deleted tasks");
  if (include.tag !== null) throw new Error("the read for the column named " + JSON.stringify(include.tag));
  await intervalCallback();
  if (lastRead("/api/tasks?deleted=include").tag !== includedTag) {
    throw new Error("the column's own poll did not name its tag");
  }

  // Hidden and shown again before any poll lands. The model was read from the
  // address the column reads, but the switch has said it is no answer to that
  // setting until a poll asks again, so the read the second click starts asks
  // in full rather than being told the model already holds it.
  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  const beforeReshow = reads.length;
  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  await settle();
  const reshown = reads.slice(beforeReshow).find((read) => read.url === "/api/tasks?deleted=include");
  if (!reshown) throw new Error("showing the column again did not read the deleted tasks");
  if (reshown.tag !== null) throw new Error("the read for a column shown again named " + JSON.stringify(reshown.tag));

  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  await settle();
  await intervalCallback();
  const read = reads[reads.length - 1];
  if (read.url !== "/api/tasks") throw new Error("the board did not go back to the active read: " + read.url);
  if (read.tag !== null) {
    throw new Error("the read after hiding the column named " + JSON.stringify(read.tag) + " for a model read with deleted tasks");
  }
`)
	runConditionalPollClient(t, "the deleted column toggle", program)
}

// A confirmed write puts its task into the model directly, so the model is no
// longer the document the tag names. The next poll reads in full.
func TestClientConditionalPollForgetsTheTagWhenAWriteReplacesATask(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), "", `
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== taskTag) throw new Error("the board poll never named its tag, so this test proves nothing");

  const id = `+strconv.Quote(conditionalPollTaskID)+`;
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const card = boardCard(id);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  await documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  if (!fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("the drop sent no write, so nothing replaced the model");
  }

  await intervalCallback();
  const read = lastRead("/api/tasks");
  if (read.tag !== null) {
    throw new Error("the poll after a confirmed write named " + JSON.stringify(read.tag) + " for a model the write had changed");
  }
`)
	runConditionalPollClient(t, "a confirmed write", program)
}

// The relationship sidebar reads the deleted tasks on its own and holds them in
// a model of its own, so it keeps a tag of its own. A 304 there keeps what the
// sidebar was drawn from — the tombstoned prerequisite stays a removable
// "Deleted" row — rather than being read as a failure. It is also the proof
// that a board 304 goes on to the relationship step the way a 200 does.
func TestClientConditionalPollKeepsTheHeldDeletedTasksOnNotModified(t *testing.T) {
	t.Parallel()
	current := clientPlacementTask(conditionalPollTaskID, "Depends on a tombstone", core.StatusReady, core.PriorityMedium)
	current.Head = "head-1"
	current.Dependencies = []string{conditionalPollDependencyID}
	dependency := clientPlacementTask(conditionalPollDependencyID, "Tombstoned prerequisite", core.StatusReady, core.PriorityMedium)
	dependency.Deleted = true
	deleted := string(mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{dependency}}))

	program := conditionalPollProgram(t, "/tasks/"+current.ID, []core.Task{current}, `
deletedTaskResponse = `+deleted+`;
`, `
  await settle();
  const dependencyID = `+strconv.Quote(conditionalPollDependencyID)+`;
  const deletedRow = () => findElement(main, (element) => element.dataset.relationshipId === dependencyID);
  const removable = (row) => row && row.textContent.includes("Deleted") &&
    findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
  if (!removable(deletedRow())) throw new Error("the tombstoned prerequisite was never drawn, so this test proves nothing");
  const first = lastRead("/api/tasks?deleted=true");
  if (!first || first.tag !== null) throw new Error("the sidebar's first read named a tag: " + JSON.stringify(first));

  const before = reads.length;
  await intervalCallback();
  const board = reads.slice(before).find((read) => read.url === "/api/tasks");
  if (!board || board.tag !== taskTag) throw new Error("the board poll did not name its tag: " + JSON.stringify(board));
  const sidebar = reads.slice(before).find((read) => read.url === "/api/tasks?deleted=true");
  if (!sidebar) throw new Error("a board 304 did not go on to refresh the relationship sidebar");
  if (sidebar.tag !== deletedTag) {
    throw new Error("the sidebar's read named " + JSON.stringify(sidebar.tag) + ", want its own tag " + JSON.stringify(deletedTag));
  }
  const notModified = answers.slice(-1)[0];
  if (notModified.status !== 304) throw new Error("the sidebar's read was not answered 304, so this test proves nothing");
  if (notModified.jsonCalls !== 0) throw new Error("the page tried to read a body out of the sidebar's 304");
  if (!removable(deletedRow())) throw new Error("a 304 for the deleted tasks lost the tombstoned prerequisite");
  if (findElement(main, (element) => element.textContent === "Deleted task data could not be loaded.")) {
    throw new Error("a 304 for the deleted tasks was reported as a failure");
  }
`)
	runConditionalPollClient(t, "a 304 for the deleted tasks", program)
}

// A create's stand-in is retired by value when a poll outruns the POST, and by
// the ID the POST answers with otherwise. When the poll that carried the task
// could not match it by value — the server stored something other than what was
// typed — the ID is the only way left, and it arrives when nothing about the
// board has changed: every refresh from then on is a 304 and redraws nothing.
// So the ID has to be drawn the moment it is known, or the task is shown twice
// for as long as the board stays idle.
func TestClientConditionalPollRetiresAStandInTheBoardAlreadyCarries(t *testing.T) {
	t.Parallel()
	created := clientPlacementTask("WB-01J0000000000000000000CP03", "Stored otherwise", core.StatusReady, core.PriorityMedium)
	created.Head = "head-1"
	refreshed := tasksDocumentJSON(t, []core.Task{created})

	program := conditionalPollProgram(t, "/tasks/new?status=ready", nil, "", `
  const id = `+strconv.Quote(created.ID)+`;
  const conditionalFetch = globalThis.fetch;
  let releasePost = null;
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/tasks" && options.method === "POST") {
      fetchCalls.push({ url, options });
      return { ok: true, json: async () => new Promise((resolve) => {
        releasePost = () => resolve(`+taskMutationJSON(refreshed, "")+`);
      }) };
    }
    return conditionalFetch(url, options);
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  if (!form) throw new Error("the create form did not render");
  findElement(form, (element) => element.id === "task-title").value = "Typed one way";
  const settled = form.eventListeners.submit({ preventDefault() {} });
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const cards = () => ready.querySelectorAll(".task-card");

  // The task is committed and readable, under a title other than the one
  // typed, while the POST is still open. The poll draws it and cannot retire
  // the stand-in, because nothing ties the two together yet.
  taskResponse = `+refreshed+`;
  taskTag = "\"b\"";
  await intervalCallback();
  if (cards().length !== 2 || !cards().some((card) => card.dataset.taskId === id)) {
    throw new Error("the setup did not draw the task beside its stand-in, so this test proves nothing: " +
      cards().map((card) => card.dataset.taskId).join(", "));
  }

  releasePost();
  await settled;
  const refresh = answers[answers.length - 1];
  if (!refresh || refresh.status !== 304) throw new Error("the refresh after the create was not a 304, so this test proves nothing");
  await intervalCallback();
  if (answers[answers.length - 1].status !== 304) throw new Error("the idle poll was not a 304");
  const drawn = cards();
  if (drawn.length !== 1 || drawn[0].dataset.taskId !== id) {
    throw new Error("the board drew " + drawn.map((card) => card.dataset.taskId).join(", ") + ", want only " + id);
  }
`)
	runConditionalPollClient(t, "a stand-in the board already carries", program)
}

// conditionalPollDragTasks is a board of two, so the board still has a card to
// draw after the dragged one's task is deleted elsewhere.
func conditionalPollDragTasks() (core.Task, core.Task) {
	dragged := clientPlacementTask("WB-01J0000000000000000000CP04", "Dragged", core.StatusReady, core.PriorityMedium)
	dragged.Head = "head-a"
	kept := clientPlacementTask("WB-01J0000000000000000000CP05", "Kept", core.StatusReady, core.PriorityMedium)
	kept.Head = "head-b"
	return dragged, kept
}

// The renderer leaves a dragged card where it is for as long as the gesture
// lasts, even when a poll during it drops the card's task. Something has to
// collect it when the gesture ends, and it cannot be the next poll: the poll
// that dropped the task already brought the board up to date, so every poll
// after it is a 304 that redraws nothing. A drag cancelled with Escape — a
// dragend and no drop — must not leave the card behind.
func TestClientConditionalPollCollectsADraggedCardWhoseTaskWasDeleted(t *testing.T) {
	t.Parallel()
	dragged, kept := conditionalPollDragTasks()
	program := conditionalPollProgram(t, "/", []core.Task{dragged, kept}, "", `
  const id = `+strconv.Quote(dragged.ID)+`;
  const card = boardCard(id);
  if (!card) throw new Error("the board never drew the card to drag");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });

  // Another clone deletes the task while it is under the cursor.
  taskResponse = `+tasksDocumentJSON(t, []core.Task{kept})+`;
  taskTag = "\"b\"";
  await intervalCallback();
  if (answers[answers.length - 1].status !== 200) throw new Error("the poll during the drag was not a 200, so this test proves nothing");
  if (!card.parentElement) throw new Error("the poll detached the card mid-drag, so this test proves nothing");

  documentEventListeners.dragend({ target: card });
  await intervalCallback();
  await intervalCallback();
  const statuses = answers.slice(-2).map((answer) => answer.status);
  if (statuses.some((status) => status !== 304)) throw new Error("the polls after the drag were " + JSON.stringify(statuses) + ", want 304s");
  if (card.parentElement || boardCard(id)) {
    throw new Error("the dragged card outlived its drag and its task");
  }
  if (!boardCard(`+strconv.Quote(kept.ID)+`)) throw new Error("collecting the dragged card took another one with it");
`)
	runConditionalPollClient(t, "a cancelled drag over a deleted task", program)
}

// A route change retires a drag no dragend will end, and the board it leaves
// behind is re-attached on the way back without being redrawn. So the card the
// gesture was holding has to be collected when the gesture is retired, or the
// board the reader returns to still draws a task the server deleted.
func TestClientConditionalPollCollectsADraggedCardWhenARouteChangeEndsTheDrag(t *testing.T) {
	t.Parallel()
	dragged, kept := conditionalPollDragTasks()
	program := conditionalPollProgram(t, "/", []core.Task{dragged, kept}, "", `
  const id = `+strconv.Quote(dragged.ID)+`;
  const card = boardCard(id);
  if (!card) throw new Error("the board never drew the card to drag");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });

  taskResponse = `+tasksDocumentJSON(t, []core.Task{kept})+`;
  taskTag = "\"b\"";
  await intervalCallback();
  if (!card.parentElement) throw new Error("the poll detached the card mid-drag, so this test proves nothing");

  // Away to the other task's page with the gesture still open, and back.
  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(`+strconv.Quote(kept.ID)+`);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  await settle();
  await intervalCallback();
  returnTo("/");
  await intervalCallback();
  if (answers.filter((answer) => answer.status === 304).length === 0) {
    throw new Error("no poll after the route change was a 304, so this test proves nothing");
  }
  if (main.firstElementChild !== boardView) throw new Error("the board was not re-attached");
  if (card.parentElement || boardCard(id)) {
    throw new Error("the board the reader returned to still draws the dragged card of a deleted task");
  }
`)
	runConditionalPollClient(t, "a route change during a drag over a deleted task", program)
}

// A 304 vouches only for a document the read named. Answering one to a read
// that named nothing — here, a loaded board whose last answer carried no tag —
// is a broken answer, and the board says so rather than counting it a sync.
func TestClientConditionalPollTreatsAnUnrequestedNotModifiedAsAFailure(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/", conditionalPollTasks(), `
serve = (url) => ({ ok: true, json: async () => taskResponse });
`, `
  if (!boardCard(`+strconv.Quote(conditionalPollTaskID)+`)) throw new Error("the board never loaded");
  serve = () => notModifiedAnswer("\"z\"");
  clock += 20000;
  await intervalCallback();
  if (lastRead("/api/tasks").tag !== null) throw new Error("the read named a tag, so this test proves nothing");
  if (stale.dataset.visible !== "true") throw new Error("a 304 nobody asked for was taken as the board");
  if (updatedTextElement.textContent === "in sync") throw new Error("a 304 nobody asked for was counted as a sync");
  if (!boardCard(`+strconv.Quote(conditionalPollTaskID)+`)) throw new Error("the failure took the last board away");
`)
	runConditionalPollClient(t, "an unrequested board 304", program)
}

// The same for the relationship sidebar's read of the deleted tasks: a 304 to
// its first read, which named nothing, is a failure to load them.
func TestClientConditionalPollTreatsAnUnrequestedDeletedNotModifiedAsAFailure(t *testing.T) {
	t.Parallel()
	program := conditionalPollProgram(t, "/tasks/"+conditionalPollTaskID, conditionalPollTasks(), `
serve = (url, tag) => url === "/api/tasks?deleted=true" ? notModifiedAnswer("\"z\"") : honestAnswer(url, tag);
`, `
  await settle();
  const read = lastRead("/api/tasks?deleted=true");
  if (!read || read.tag !== null) throw new Error("the sidebar's read named a tag, so this test proves nothing: " + JSON.stringify(read));
  if (!findElement(main, (element) => element.textContent === "Deleted task data could not be loaded.")) {
    throw new Error("a 304 nobody asked for was taken as the deleted tasks");
  }
`)
	runConditionalPollClient(t, "an unrequested deleted-tasks 304", program)
}
