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
