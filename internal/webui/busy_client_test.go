package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The page's side of the server's cap on how much work it does at once.
//
// A refusal from the cap is a 503 with Retry-After, sent before the server has
// started on the request. What is pinned here is what the page does with one:
// a change is kept, drawn as made, and sent again after the wait the server
// asked for, with a notice saying why it has not been confirmed; a change still
// refused after its retries is reported like any other refusal; and the
// once-a-second refresh steps back rather than knocking every second, and
// never piles a read onto one that has not come back.
//
// The waits are the page's own window timers, which the harness holds rather
// than runs, so each test decides when a wait is over and nothing here depends
// on how fast it runs.

const busyClientTaskID = "WB-01J0000000000000000000BZ01"

// busyClientServer answers writes from a script the test sets and records
// them, and fires the waits the page schedules on the test's word.
const busyClientServer = `
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
function busyAnswer(retryAfter = "2") {
  return {
    ok: false, status: 503,
    headers: { get: (name) => (name.toLowerCase() === "retry-after" ? retryAfter : null) },
    json: async () => ({ format: "workbook.error", version: 1,
      error: { category: "operational", message: "The board is busy: it is already working on the 16 requests it takes at once, so this one was not started. Try again in a moment." } })
  };
}
// What a proxy in front of a shared board answers past a person's limit.
function limitedAnswer(retryAfter) {
  return {
    ok: false, status: 429,
    headers: { get: (name) => (name.toLowerCase() === "retry-after" ? retryAfter : null) },
    json: async () => { throw new SyntaxError("Unexpected token '<'"); }
  };
}
// What leaving the page would do now: whether a listener stops to ask.
function leavingAsks() {
  const listener = windowEventListeners.beforeunload;
  if (!listener) return false;
  let prevented = false;
  const event = { returnValue: undefined, preventDefault() { prevented = true; } };
  listener(event);
  return prevented && event.returnValue === "";
}
const writes = [];
let writeAnswers = [];
const harnessWriteFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") === "GET") return harnessWriteFetch(url, options);
  fetchCalls.push({ url, options });
  writes.push({ url, method: options.method, body: options.body });
  const next = writeAnswers.shift();
  if (next) return next();
  return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({
    format: "workbook.task-mutation", version: 1, task: confirmedTask }) };
};
// The page's next scheduled wait, once it has scheduled one.
async function nextWait() {
  for (let turn = 0; turn < 200; turn += 1) {
    const due = windowTimeouts.find((timer) => !timer.canceled && !timer.ran);
    if (due) return due;
    await settle();
  }
  throw new Error("the page never scheduled a wait");
}
async function endWait(timer) {
  timer.ran = true;
  timer.callback();
  await settle();
}
function dragTo(status) {
  const from = boardLists.find((list) => list.querySelectorAll(".task-card").some((item) => item.dataset.taskId === taskID));
  const card = from.querySelectorAll(".task-card").find((item) => item.dataset.taskId === taskID);
  const target = boardLists.find((list) => list.dataset.status === status);
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  return documentEventListeners.drop({ target, clientY: 1, dataTransfer, preventDefault() {} });
}
function cardIn(status) {
  const list = boardLists.find((candidate) => candidate.dataset.status === status);
  return list.querySelectorAll(".task-card").some((item) => item.dataset.taskId === taskID);
}
`

func busyClientProgram(t *testing.T, body string) string {
	t.Helper()
	task := clientPlacementTask(busyClientTaskID, "Busy task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	confirmed := task
	confirmed.Status = core.StatusInProgress
	confirmed.Head = "head-2"
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	return clientDOMHarness("/", tasksDocumentJSON(t, tasks)) +
		"const taskID = " + strconv.Quote(busyClientTaskID) + ";\n" +
		"const confirmedTask = " + string(mustJSON(t, confirmed)) + ";\n" +
		busyClientServer + script + `
setTimeout(async () => {
  await settle();
` + body + `
  process.stdout.write("\n` + busyClientFinished + `\n");
}, 0);
`
}

// busyClientFinished is printed once a program's checks have all run. Node
// exits cleanly when nothing is left scheduled, so a program stalled on a
// promise nobody settles would otherwise pass without checking anything.
const busyClientFinished = "busy-client-finished"

func runBusyClient(t *testing.T, purpose, program string) {
	t.Helper()
	node := requireNode(t)
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
	if !strings.Contains(string(output), busyClientFinished) {
		t.Fatalf("%s stopped before its checks finished\n%s", purpose, output)
	}
}

func TestClientKeepsAndResendsAChangeTheBoardWasTooBusyToStart(t *testing.T) {
	t.Parallel()
	// Production mutation: a page that reads the cap's 503 as any other
	// refusal rolls the reader's change back and asks them to make it again,
	// for a change the server never even looked at.
	runBusyClient(t, "resend after a busy refusal", busyClientProgram(t, `
  writeAnswers = [() => busyAnswer("2"), () => busyAnswer("2")];
  const dropped = dragTo("in-progress");

  const firstWait = await nextWait();
  if (firstWait.delay !== 2000) throw new Error("the first resend waited " + firstWait.delay + "ms, want the 2s the server asked for");
  if (!cardIn("in-progress")) throw new Error("the card went back while its change was waiting to be sent again");
  if (busyNotice.hidden) throw new Error("nothing told the reader why their change had not been confirmed");
  if (cardFailureMessage(boardCard(taskID)) !== "") throw new Error("a change still being sent was reported as refused");
  await endWait(firstWait);

  const secondWait = await nextWait();
  if (secondWait.delay !== 4000) throw new Error("the second resend waited " + secondWait.delay + "ms, want twice the first");
  await endWait(secondWait);
  await dropped;

  if (writes.length !== 3) throw new Error("the change was sent " + writes.length + " times, want 3");
  if (new Set(writes.map((write) => write.url + " " + write.method + " " + write.body)).size !== 1) {
    throw new Error("a resend was not the same request: " + JSON.stringify(writes));
  }
  if (!cardIn("in-progress")) throw new Error("the confirmed change is not on the board");
  if (!busyNotice.hidden) throw new Error("the busy notice stayed up after the change was confirmed");
  if (cardFailureMessage(boardCard(taskID)) !== "") throw new Error("a confirmed change left a report on its card");
`))
}

func TestClientReportsAChangeTheBoardStayedTooBusyFor(t *testing.T) {
	t.Parallel()
	// Production mutation: retrying a busy refusal forever is an unbounded
	// queue in the page — a reader on a flooded board is never told their
	// change did not go.
	runBusyClient(t, "report after the resends run out", busyClientProgram(t, `
  writeAnswers = [() => busyAnswer(), () => busyAnswer(), () => busyAnswer(), () => busyAnswer(), () => busyAnswer()];
  const dropped = dragTo("in-progress");
  const delays = [];
  for (let index = 0; index < 3; index += 1) {
    const wait = await nextWait();
    delays.push(wait.delay);
    await endWait(wait);
  }
  await dropped;
  if (JSON.stringify(delays) !== JSON.stringify([2000, 4000, 8000])) throw new Error("resend waits = " + JSON.stringify(delays));
  if (writes.length !== 4) throw new Error("the change was sent " + writes.length + " times, want 4: once and three resends");
  if (cardIn("in-progress") || !cardIn("ready")) throw new Error("a change the server never made is still drawn as made");
  const reported = cardFailureMessage(boardCard(taskID));
  if (!reported.includes("busy")) throw new Error("the card does not say the board was busy: " + JSON.stringify(reported));
  if (!busyNotice.hidden) throw new Error("the busy notice stayed up once nothing was waiting");
`))
}

func TestClientDoesNotResendA503TheBoardDidNotSend(t *testing.T) {
	t.Parallel()
	// Production mutation: resending on any 503 resends through a proxy whose
	// 503 says nothing about whether the board made the change.
	runBusyClient(t, "no resend without Retry-After", busyClientProgram(t, `
  writeAnswers = [() => ({ ok: false, status: 503, headers: { get: () => null },
    json: async () => ({ format: "workbook.error", version: 1, error: { category: "operational", message: "upstream unavailable" } }) })];
  await dragTo("in-progress");
  if (writes.length !== 1) throw new Error("a 503 without Retry-After was sent " + writes.length + " times, want once");
  if (!busyNotice.hidden) throw new Error("a proxy's 503 was announced as the board being busy");
`))
}

func TestClientPollStepsBackWhileTheBoardIsBusy(t *testing.T) {
	t.Parallel()
	// Production mutation: a refresh that ignores the cap's refusal asks again
	// every second, which on a flooded board is every open page adding to the
	// flood it is waiting on.
	runBusyClient(t, "poll backoff", busyClientProgram(t, `
  let clock = 1000000;
  Date.now = () => clock;
  const reads = [];
  let readsBusy = true;
  const pageFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET" && url.startsWith("/api/tasks")) {
      reads.push(url);
      if (readsBusy) return busyAnswer("2");
    }
    return pageFetch(url, options);
  };
  await intervalCallback();
  if (reads.length !== 1) throw new Error("the tick sent " + reads.length + " reads, want 1");
  if (stale.dataset.visible !== "true" || !stale.textContent.includes("busy")) {
    throw new Error("a busy refusal was not shown as the board being busy: " + JSON.stringify(stale.textContent));
  }
  // Inside the two seconds the server asked for, no tick reads.
  clock += 1000; await intervalCallback();
  if (reads.length !== 1) throw new Error("the refresh asked again before the wait the server named was over");
  clock += 1000; await intervalCallback();
  if (reads.length !== 2) throw new Error("the refresh did not ask again once the wait was over");
  // Refused again: the wait doubles.
  clock += 3999; await intervalCallback();
  if (reads.length !== 2) throw new Error("a second refusal in a row did not double the wait");
  clock += 1; await intervalCallback();
  if (reads.length !== 3) throw new Error("the refresh did not ask again after the doubled wait");
  // Answered: back on the one-second beat, and the banner comes down.
  readsBusy = false;
  clock += 8000; await intervalCallback();
  if (reads.length !== 4 || stale.dataset.visible === "true") throw new Error("an answered read did not clear the busy banner");
  clock += 1000; await intervalCallback();
  if (reads.length !== 5) throw new Error("an answered read did not put the refresh back on its one-second beat");
`))
}

func TestClientPollSendsNoReadWhileTheLastIsStillOut(t *testing.T) {
	t.Parallel()
	// Production mutation: a tick that reads every second whatever is still
	// out piles reads onto a server that is already slow to answer them.
	runBusyClient(t, "one poll at a time", busyClientProgram(t, `
  const reads = [];
  let hold = null;
  const pageFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET" && url.startsWith("/api/tasks")) {
      reads.push(url);
      if (reads.length === 1) {
        await new Promise((resolve) => { hold = resolve; });
      }
    }
    return pageFetch(url, options);
  };
  const first = intervalCallback();
  await settle();
  intervalCallback(); intervalCallback();
  await settle();
  if (reads.length !== 1) throw new Error("ticks sent " + reads.length + " reads while the first was still out, want 1");
  hold();
  await first;
  await intervalCallback();
  if (reads.length !== 2) throw new Error("the tick after the read came back did not read");
`))
}

func TestClientWarnsBeforeLeavingWhileAChangeWaitsToBeSent(t *testing.T) {
	t.Parallel()
	// Production mutation: a notice that says the change is kept, with nothing
	// stopping the tab from closing, loses a change the reader was told was
	// safe — the review closed the tab seven seconds in and the title never
	// reached the server.
	// The notice says what is true: the change is held in this tab, and what
	// would lose it.
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	page := request(t, handler, http.MethodGet, "/").Body.String()
	notice := page[strings.Index(page, "data-busy-notice"):]
	notice = notice[:strings.Index(notice, "</div>")]
	if !strings.Contains(notice, "held in this tab") || !strings.Contains(notice, "closing or leaving the tab before then loses it") {
		t.Fatalf("the busy notice reads %q, want it to say the change is held in this tab and what loses it", notice)
	}
	runBusyClient(t, "a warning before leaving", busyClientProgram(t, `
  if (leavingAsks()) throw new Error("leaving was stopped to ask before anything was waiting");
  writeAnswers = [() => busyAnswer("2")];
  const dropped = dragTo("in-progress");
  const wait = await nextWait();
  if (!leavingAsks()) throw new Error("leaving the page while a change waited to be sent was not stopped to ask");
  await endWait(wait);
  await dropped;
  if (writes.length !== 2) throw new Error("the change was sent " + writes.length + " times, want 2");
  if (leavingAsks()) throw new Error("leaving is still stopped to ask once the change was confirmed");
  if (windowEventListeners.beforeunload) throw new Error("the warning's listener was left behind");
`))
}

func TestClientResendsAChangeAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: a page that reads a proxy's 429 as a failure tells
	// the reader their change did not go and never sends it, though the proxy
	// said exactly when it would be taken.
	runBusyClient(t, "resend after a 429", busyClientProgram(t, `
  Date.now = () => Date.parse("Sat, 21 Nov 2026 12:00:00 GMT");
  writeAnswers = [() => limitedAnswer("3"), () => limitedAnswer("Sat, 21 Nov 2026 12:00:05 GMT")];
  const dropped = dragTo("in-progress");
  const first = await nextWait();
  if (first.delay !== 3000) throw new Error("a 429 asking for 3 seconds waited " + first.delay + "ms");
  if (busyNotice.hidden || !leavingAsks()) throw new Error("a change waiting on a 429 was not announced and guarded like a busy one");
  if (!cardIn("in-progress")) throw new Error("the card went back while its change waited out a 429");
  await endWait(first);
  // A Retry-After given as a date, five seconds off, doubled for the second
  // refusal in a row.
  const second = await nextWait();
  if (second.delay !== 10000) throw new Error("a 429 naming a date five seconds off waited " + second.delay + "ms on its second refusal, want 10000");
  await endWait(second);
  await dropped;
  if (writes.length !== 3 || !cardIn("in-progress")) throw new Error("the change was not sent again after the 429s: " + writes.length + " writes");
  if (!busyNotice.hidden) throw new Error("the notice stayed up after the change was confirmed");

  // A 429 that does not say when is somebody else's refusal, and the change is
  // reported rather than resent.
  writeAnswers = [() => limitedAnswer(null)];
  await dragTo("ready");
  if (writes.length !== 4) throw new Error("a 429 without Retry-After was sent again");
  if (!busyNotice.hidden) throw new Error("a 429 without Retry-After was announced as a wait");

  // The poll steps back from a 429 with Retry-After the way it does from the
  // board's own refusal.
  let clock = Date.parse("Sat, 21 Nov 2026 12:01:00 GMT");
  Date.now = () => clock;
  const reads = [];
  const pageFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET" && url.startsWith("/api/tasks")) {
      reads.push(url);
      return limitedAnswer("2");
    }
    return pageFetch(url, options);
  };
  await intervalCallback();
  if (stale.dataset.visible !== "true" || !stale.textContent.includes("busy")) {
    throw new Error("a 429 poll was not shown as the board being busy: " + JSON.stringify(stale.textContent));
  }
  clock += 1000; await intervalCallback();
  if (reads.length !== 1) throw new Error("the refresh asked again inside the wait a 429 named");
`))
}

func TestClientWaitsNoLongerThanAMinuteForAResend(t *testing.T) {
	t.Parallel()
	// Production mutation: a Retry-After of an hour, doubled, holds a change
	// in a tab for longer than anyone keeps one open waiting.
	runBusyClient(t, "a bounded wait", busyClientProgram(t, `
  writeAnswers = [() => limitedAnswer("3600")];
  const dropped = dragTo("in-progress");
  const wait = await nextWait();
  if (wait.delay !== 60000) throw new Error("a Retry-After of an hour waited " + wait.delay + "ms, want 60000");
  await endWait(wait);
  await dropped;
`))
}
