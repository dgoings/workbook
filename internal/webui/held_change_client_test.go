package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the page does with a change the server asked it to send later.
//
// The board never answers that way itself; a proxy in front of it may, with a
// 429 or a 503 that carries Retry-After. What is pinned here: a change that
// cannot apply twice is kept, drawn as made, and sent again after the wait the
// answer named, under a notice and a warning before the tab is left; one still
// refused after its resends is reported in a plain sentence; and a create, a
// change that names no head, an answer without Retry-After and every read are
// sent exactly as often as they always were.
//
// The waits are the page's own window timers, which the harness holds rather
// than runs, so each test decides when a wait is over and nothing here depends
// on how fast it runs.

const heldChangeTaskID = "WB-01J0000000000000000000HC01"

// heldChangeServer answers writes from a list the test sets, records them, and
// fires the waits the page schedules on the test's word.
const heldChangeServer = `
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
// What nginx answers past a limit_req: an HTML page, not JSON.
const proxyPage = "<html><head><title>429 Too Many Requests</title></head><body>Too Many Requests</body></html>";
function waitAnswer(status, retryAfter) {
  return {
    ok: false, status,
    headers: { get: (name) => (name.toLowerCase() === "retry-after" ? retryAfter : null) },
    json: async () => JSON.parse(proxyPage)
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
// A board with a watcher, so the sync switch is live from the first read.
const syncAnswer = (mode) => ({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({
  format: "workbook.sync", version: 1, sync: { mode, watcher: true } }) });
globalThis.fetch = async (url, options = {}) => {
  if (url === "/api/sync" && (options.method || "GET") === "GET") return syncAnswer("inline");
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
function pendingWaits() {
  return windowTimeouts.filter((timer) => !timer.canceled && !timer.ran);
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
function saveStatus() {
  const status = findElement(main, (element) => element.attributes.role === "status" && hasDataKey(element, "saveStatus"));
  if (!status) throw new Error("the task form has no save status");
  return status.textContent;
}
`

// heldChangeTasks is the one task these programs drive, as the board first
// reads it and as the server holds it once a change has landed.
func heldChangeTasks(head string) (core.Task, core.Task) {
	task := clientPlacementTask(heldChangeTaskID, "Held task", core.StatusReady, core.PriorityMedium)
	task.Description = "Original."
	task.Head = head
	confirmed := task
	confirmed.Status = core.StatusInProgress
	confirmed.Head = "head-2"
	return task, confirmed
}

func heldChangeProgram(t *testing.T, path string, task, confirmed core.Task, body string) string {
	t.Helper()
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", path, response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	return clientDOMHarness(path, tasksDocumentJSON(t, tasks)) +
		"const taskID = " + strconv.Quote(task.ID) + ";\n" +
		"const confirmedTask = " + string(mustJSON(t, confirmed)) + ";\n" +
		"const confirmedDocument = " + tasksDocumentJSON(t, []core.Task{confirmed}) + ";\n" +
		heldChangeServer + script + `
setTimeout(async () => {
  await settle();
` + body + `
}, 0);
`
}

func runHeldChangeClient(t *testing.T, purpose, program string) {
	t.Helper()
	node := requireNode(t)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

func TestClientResendsAChangeAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: a page that reads a proxy's 429 as any other
	// refusal rolls the reader's change back and asks them to make it again,
	// though the proxy said exactly when it would take it.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "resend after a 429 and a 503", heldChangeProgram(t, "/", task, confirmed, `
  Date.now = () => Date.parse("Sat, 21 Nov 2026 12:00:00 GMT");
  writeAnswers = [() => waitAnswer(429, "3"), () => waitAnswer(503, "Sat, 21 Nov 2026 12:00:05 GMT")];
  const dropped = dragTo("in-progress");

  const first = await nextWait();
  if (first.delay !== 3000) throw new Error("a 429 asking for 3 seconds waited " + first.delay + "ms");
  if (!cardIn("in-progress")) throw new Error("the card went back while its change waited to be sent again");
  if (heldChangeNotice.hidden) throw new Error("nothing told the reader why their change had not been confirmed");
  if (!leavingAsks()) throw new Error("leaving the page while a change waited was not stopped to ask");
  if (cardFailureMessage(boardCard(taskID)) !== "") throw new Error("a change still waiting was reported as refused");
  await endWait(first);

  // A Retry-After given as a date five seconds off, doubled for the second
  // answer in a row that asked the page to wait.
  const second = await nextWait();
  if (second.delay !== 10000) throw new Error("a 503 naming a date five seconds off waited " + second.delay + "ms on the second resend, want 10000");
  await endWait(second);
  await dropped;

  if (writes.length !== 3) throw new Error("the change was sent " + writes.length + " times, want 3");
  if (new Set(writes.map((write) => write.url + " " + write.method + " " + write.body)).size !== 1) {
    throw new Error("a resend was not the same request: " + JSON.stringify(writes));
  }
  if (!JSON.parse(writes[0].body).expectedHead) throw new Error("the resent change named no head: " + writes[0].body);
  if (!cardIn("in-progress")) throw new Error("the confirmed change is not on the board");
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up after the change was confirmed");
  if (leavingAsks()) throw new Error("leaving is still stopped to ask once the change was confirmed");
  if (cardFailureMessage(boardCard(taskID)) !== "") throw new Error("a confirmed change left a report on its card");
`))
}

func TestClientReportsAChangeTheServerKeptAskingToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: resending for as long as the answer says wait is an
	// unbounded queue in the page, and a reader behind a proxy that never lets
	// up is never told their change did not go. And a refusal whose body is an
	// HTML page must not be reported as the page's own parse error.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "report after the resends run out", heldChangeProgram(t, "/", task, confirmed, `
  writeAnswers = [1, 2, 3, 4, 5].map(() => () => waitAnswer(429, "2"));
  const dropped = dragTo("in-progress");
  const delays = [];
  for (let index = 0; index < 3; index += 1) {
    const wait = await nextWait();
    delays.push(wait.delay);
    await endWait(wait);
  }
  await dropped;
  await settle();
  if (JSON.stringify(delays) !== JSON.stringify([2000, 4000, 8000])) throw new Error("resend waits = " + JSON.stringify(delays));
  if (writes.length !== 4) throw new Error("the change was sent " + writes.length + " times, want 4: once and three resends");
  if (cardIn("in-progress") || !cardIn("ready")) throw new Error("a change the server never took is still drawn as made");
  const reported = cardFailureMessage(boardCard(taskID));
  if (!reported.includes("kept asking this page to wait")) throw new Error("the card does not say the server kept asking to wait: " + JSON.stringify(reported));
  if (/JSON|Unexpected token/.test(reported)) throw new Error("the card reports a parse error: " + JSON.stringify(reported));
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up once nothing was waiting");
  if (leavingAsks()) throw new Error("leaving is still stopped to ask once nothing waits");
`))
}

func TestClientSendsOnceAnAnswerThatDoesNotSayWhenToAskAgain(t *testing.T) {
	t.Parallel()
	// Production mutation: resending on any 429 or 503 resends on answers that
	// say nothing about when, or about whether the change went through.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "no resend without a usable Retry-After", heldChangeProgram(t, "/", task, confirmed, `
  for (const [status, retryAfter] of [[503, null], [429, null], [429, "soon"], [502, "2"]]) {
    const before = writes.length;
    writeAnswers = [() => waitAnswer(status, retryAfter)];
    await dragTo("in-progress");
    await settle();
    if (writes.length !== before + 1) {
      throw new Error("a " + status + " with Retry-After " + JSON.stringify(retryAfter) + " was sent " + (writes.length - before) + " times, want once");
    }
    if (pendingWaits().length !== 0) throw new Error("a " + status + " with Retry-After " + JSON.stringify(retryAfter) + " scheduled a resend");
    if (!heldChangeNotice.hidden) throw new Error("a " + status + " with Retry-After " + JSON.stringify(retryAfter) + " was announced as a wait");
    if (!cardIn("ready")) throw new Error("a refused change is still drawn as made");
  }
`))
}

func TestClientWaitsNoLongerThanAMinuteForAResend(t *testing.T) {
	t.Parallel()
	// Production mutation: a Retry-After of an hour, doubled, holds a change
	// in a tab for longer than anyone keeps one open waiting.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "a bounded wait", heldChangeProgram(t, "/", task, confirmed, `
  writeAnswers = [() => waitAnswer(429, "3600"), () => waitAnswer(429, "40")];
  const dropped = dragTo("in-progress");
  const first = await nextWait();
  if (first.delay !== 60000) throw new Error("a Retry-After of an hour waited " + first.delay + "ms, want 60000");
  await endWait(first);
  const second = await nextWait();
  if (second.delay !== 60000) throw new Error("40 seconds doubled waited " + second.delay + "ms, want the 60000 cap");
  await endWait(second);
  await dropped;
  if (writes.length !== 3 || !cardIn("in-progress")) throw new Error("the change was not sent after its bounded waits");
`))
}

func TestClientWarnsBeforeLeavingWhileAChangeWaitsToBeSent(t *testing.T) {
	t.Parallel()
	// Production mutation: a notice that says the change is kept, with nothing
	// stopping the tab from closing, loses a change the reader was told was
	// safe. The notice says what is true: it is held in this tab, and what
	// would lose it.
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	page := request(t, handler, http.MethodGet, "/").Body.String()
	at := strings.Index(page, "<div class=\"notice\" data-held-change-notice")
	if at < 0 {
		t.Fatal("the page has no held-change notice")
	}
	notice := page[at:]
	notice = notice[:strings.Index(notice, "</div>")]
	for _, want := range []string{"held in this tab", "closing or leaving the tab before then loses it"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the held-change notice reads %q, want it to say %q", notice, want)
		}
	}
	for _, unwanted := range []string{"busy with other requests", "cap"} {
		if strings.Contains(notice, unwanted) {
			t.Fatalf("the held-change notice reads %q, which speaks of the board's own limit (%q)", notice, unwanted)
		}
	}
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "a warning before leaving", heldChangeProgram(t, "/", task, confirmed, `
  if (leavingAsks()) throw new Error("leaving was stopped to ask before anything was waiting");
  writeAnswers = [() => waitAnswer(429, "2")];
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

func TestClientNeverResendsAChangeThatCouldApplyTwice(t *testing.T) {
	t.Parallel()
	// Production mutation: a page that resends every change it is asked to
	// wait on resends a create, or a change that names no head, whose first
	// attempt may already have reached the board: the resend would make a
	// second task, or apply the change twice.
	t.Run("a change that names no head", func(t *testing.T) {
		t.Parallel()
		task, confirmed := heldChangeTasks("")
		runHeldChangeClient(t, "a headless change sent once", heldChangeProgram(t, "/", task, confirmed, `
  writeAnswers = [() => waitAnswer(429, "1")];
  await dragTo("in-progress");
  await settle();
  if (writes.length !== 1) throw new Error("a change naming no head was sent " + writes.length + " times, want once");
  if ("expectedHead" in JSON.parse(writes[0].body)) throw new Error("the change was meant to name no head: " + writes[0].body);
  if (pendingWaits().length !== 0 || !heldChangeNotice.hidden) throw new Error("a change naming no head was held to be sent again");
`))
	})
	t.Run("a create", func(t *testing.T) {
		t.Parallel()
		task, confirmed := heldChangeTasks("head-1")
		runHeldChangeClient(t, "a create sent once", heldChangeProgram(t, "/tasks/new?status=ready", task, confirmed, `
  writeAnswers = [() => waitAnswer(429, "1")];
  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "Made once";
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
  const creates = writes.filter((write) => write.url === "/api/tasks" && write.method === "POST");
  if (creates.length !== 1) throw new Error("a create was sent " + creates.length + " times, want once");
  if (pendingWaits().length !== 0 || !heldChangeNotice.hidden) throw new Error("a create was held to be sent again");
  if (/JSON|Unexpected token/.test(notice.textContent)) throw new Error("the refused create reports a parse error: " + JSON.stringify(notice.textContent));
`))
	})
}

func TestClientNeverResendsARead(t *testing.T) {
	t.Parallel()
	// Production mutation: putting the poll's read through the resend would
	// schedule reads of its own beside the once-a-second refresh.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "a read asked to wait", heldChangeProgram(t, "/", task, confirmed, `
  const reads = [];
  const pageFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET" && url.startsWith("/api/tasks")) {
      reads.push(url);
      return waitAnswer(429, "2");
    }
    return pageFetch(url, options);
  };
  await intervalCallback();
  await settle();
  if (reads.length !== 1) throw new Error("one tick sent " + reads.length + " reads, want 1");
  if (pendingWaits().length !== 0) throw new Error("a read the server asked to wait scheduled a resend of its own");
  if (!heldChangeNotice.hidden) throw new Error("a read was announced as a change waiting to be sent");
  await intervalCallback();
  if (reads.length !== 2) throw new Error("the next tick did not read as it always has");
`))
}

func TestClientResendsASyncModeAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: the sync switch sends its mode without the resend,
	// and a flip a proxy asked to wait on is silently lost.
	task, confirmed := heldChangeTasks("head-1")
	runHeldChangeClient(t, "resend a sync mode", heldChangeProgram(t, "/", task, confirmed, `
  writeAnswers = [() => waitAnswer(503, "2"), () => syncAnswer("deferred")];
  if (syncToggle.hidden) throw new Error("the sync switch never appeared");
  const flipped = syncToggle.eventListeners.click({ preventDefault() {} });
  const wait = await nextWait();
  if (wait.delay !== 2000 || heldChangeNotice.hidden) throw new Error("the mode a proxy asked to wait on was not held");
  await endWait(wait);
  await flipped;
  const puts = writes.filter((write) => write.url === "/api/sync");
  if (puts.length !== 2) throw new Error("the mode was sent " + puts.length + " times, want 2");
  if (syncToggle.attributes["aria-checked"] !== "true") throw new Error("the resent mode is not shown as set");
`))
}

func TestClientReportsARefusalThatIsNotJSONInAPlainSentence(t *testing.T) {
	t.Parallel()
	// Production mutation: parsing every refusal as JSON reports an HTML error
	// page from a proxy as `Unexpected token '<', "<html><hea"... is not valid
	// JSON`, which is the page's own failure and tells the reader nothing.
	task, confirmed := heldChangeTasks("head-1")
	confirmed.Status = task.Status
	runHeldChangeClient(t, "plain sentences for unreadable refusals", heldChangeProgram(t, "/tasks/"+task.ID, task, confirmed, `
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(form, (element) => element.id === "task-title");
  title.value = "Edited title";

  writeAnswers = [1, 2, 3, 4].map(() => () => waitAnswer(429, "1"));
  const saving = form.eventListeners.submit({ preventDefault() {} });
  for (let index = 0; index < 3; index += 1) await endWait(await nextWait());
  await saving;
  if (writes.length !== 4) throw new Error("the save was sent " + writes.length + " times, want 4");
  const waited = saveStatus();
  if (waited !== "The server was still asking this page to wait after 3 resends, so this change was not sent again. Try it again in a moment.") {
    throw new Error("a save refused through every resend reads " + JSON.stringify(waited));
  }

  for (const status of [502, 413]) {
    writeAnswers = [() => waitAnswer(status, null)];
    await form.eventListeners.submit({ preventDefault() {} });
    const said = saveStatus();
    if (said !== "The server answered HTTP " + status + " and gave no reason this page can read.") {
      throw new Error("an HTML " + status + " reads " + JSON.stringify(said));
    }
  }
`))
}

func TestClientKeepsWhatWasTypedWhileASaveWaited(t *testing.T) {
	t.Parallel()
	// Production mutation: the save that lands after a wait leaves for the
	// board as every accepted save does, and the edit the reader typed while it
	// waited goes with the form, unsaved and unmentioned.
	task, confirmed := heldChangeTasks("head-1")
	confirmed.Status = task.Status
	confirmed.Title = "First edit"
	runHeldChangeClient(t, "edits typed while a save waited", heldChangeProgram(t, "/tasks/"+task.ID, task, confirmed, `
  const form = findElement(main, (element) => element.tagName === "FORM");
  const section = main.firstElementChild;
  const title = findElement(form, (element) => element.id === "task-title");
  const description = findElement(form, (element) => element.id === "task-description");
  const save = findElement(form, (element) => hasClassToken(element, "save-button"));
  title.value = "First edit";

  writeAnswers = [() => waitAnswer(429, "2")];
  const saving = form.eventListeners.submit({ preventDefault() {} });
  const wait = await nextWait();
  if (!save.disabled) throw new Error("Save stayed live while its change waited");
  // Said beside Save too, where the reader is: on a phone the notice is a
  // scroll away from it.
  if (!saveStatus().includes("held in this tab")) throw new Error("the form says " + JSON.stringify(saveStatus()) + " beside Save while the save waits");
  // The reader keeps working while the save waits.
  description.value = "Typed while the save waited.";
  taskResponse = confirmedDocument;
  await endWait(wait);
  await saving;

  if (historyPaths.includes("/") || main.firstElementChild !== section) {
    throw new Error("the form left for the board and took the unsaved edit with it: " + JSON.stringify(historyPaths));
  }
  if (description.value !== "Typed while the save waited.") throw new Error("the edit typed meanwhile was replaced with " + JSON.stringify(description.value));
  if (title.value !== "First edit") throw new Error("the saved title reads " + JSON.stringify(title.value));
  if (!saveStatus().includes("not saved yet")) throw new Error("the form does not say what is still unsaved: " + JSON.stringify(saveStatus()));
  if (save.disabled) throw new Error("Save is still off, so what was typed cannot be sent");

  await form.eventListeners.submit({ preventDefault() {} });
  const second = JSON.parse(writes[writes.length - 1].body);
  if (JSON.stringify(second) !== JSON.stringify({ description: "Typed while the save waited.", expectedHead: "head-2" })) {
    throw new Error("the next save sent " + JSON.stringify(second) + ", want only the edit typed meanwhile against the landed head");
  }
`))
	// And a save nobody typed past leaves for the board as it always has.
	runHeldChangeClient(t, "a held save with nothing typed after it", heldChangeProgram(t, "/tasks/"+task.ID, task, confirmed, `
  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "First edit";
  writeAnswers = [() => waitAnswer(429, "2")];
  const saving = form.eventListeners.submit({ preventDefault() {} });
  taskResponse = confirmedDocument;
  await endWait(await nextWait());
  await saving;
  if (!historyPaths.includes("/")) throw new Error("a held save with nothing typed after it did not return to the board: " + JSON.stringify(historyPaths));
`))
}

func TestClientResendsAConfigurationChangeAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: the configuration page sends its changes without
	// the resend, and a status a proxy asked to wait on is reported as not
	// sent, though the proxy said when it would take it.
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "resend a status change", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  let refused = 0;
  const panelFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if (url.startsWith("/api/vocabulary") && (options.method || "GET") !== "GET" && refused === 0) {
      refused += 1;
      vocabularyCalls.push({ url, method: options.method, body: JSON.parse(options.body) });
      return { ok: false, status: 429, headers: { get: (name) => (name.toLowerCase() === "retry-after" ? "2" : null) },
        json: async () => JSON.parse("<html><head><title>429 Too Many Requests</title></head></html>") };
    }
    return panelFetch(url, options);
  };
  await openStatuses();
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  const submitted = submitPanelForm(form);
  let wait = null;
  for (let turn = 0; turn < 200 && !wait; turn += 1) {
    wait = windowTimeouts.find((timer) => !timer.canceled && !timer.ran && timer.delay === 2000) || null;
    if (!wait) await settle();
  }
  if (!wait) throw new Error("the status change a proxy asked to wait on was not held to be sent again");
  if (heldChangeNotice.hidden) throw new Error("nothing said the status change was waiting");
  wait.ran = true;
  wait.callback();
  await submitted;
  await settle();
  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 2 || JSON.stringify(sent[0].body) !== JSON.stringify(sent[1].body)) {
    throw new Error("the status change was sent as " + JSON.stringify(sent) + ", want the same change twice");
  }
  if (panelStatuses().indexOf("triage") < 0) throw new Error("the resent status change is not drawn: " + panelStatuses().join(","));
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up after the status change landed");
`)
}

func TestClientResendsBoardSettingsAProxyAskedToWaitFor(t *testing.T) {
	t.Parallel()
	// Production mutation: the board settings are sent without the resend,
	// and a save a proxy asked to wait on is reported as not sent.
	vocabulary := handlerVocabulary(t)
	state := VocabularyState{Vocabulary: vocabulary, Head: "head-1", Display: core.DisplaySettings{Name: "Atlas"}}
	saved := state
	saved.Head = "head-2"
	saved.Display.PrimaryColor = "#123abc"
	runClientOverHandler(t, configurableHandler(vocabulary, "head-1", nil),
		"resend the board settings", "/", "", vocabulary, "head-1", nil, `
  vocabularyRead = `+string(mustJSON(t, vocabularyDocument(state)))+`;
  displayAnswer = { body: `+displayMutationJSON(t, saved)+` };
  let refused = 0;
  const panelFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/display" && refused === 0) {
      refused += 1;
      displayCalls.push({ url, method: options.method, body: JSON.parse(options.body) });
      return { ok: false, status: 503, headers: { get: (name) => (name.toLowerCase() === "retry-after" ? "2" : null) },
        json: async () => JSON.parse("<html><head><title>503 Service Unavailable</title></head></html>") };
    }
    return panelFetch(url, options);
  };
  await openStatuses();
  displayField("primaryColor").value = "#123abc";
  const saving = saveDisplay();
  let wait = null;
  for (let turn = 0; turn < 200 && !wait; turn += 1) {
    wait = windowTimeouts.find((timer) => !timer.canceled && !timer.ran && timer.delay === 2000) || null;
    if (!wait) await settle();
  }
  if (!wait) throw new Error("the settings a proxy asked to wait on were not held to be sent again");
  if (heldChangeNotice.hidden) throw new Error("nothing said the settings were waiting");
  wait.ran = true;
  wait.callback();
  await saving;
  await settle();
  if (displayCalls.length !== 2 || JSON.stringify(displayCalls[0].body) !== JSON.stringify(displayCalls[1].body)) {
    throw new Error("the settings were sent as " + JSON.stringify(displayCalls) + ", want the same save twice");
  }
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up after the settings landed");
`)
}

func TestClientReportsAConfigurationRefusalThatIsNotJSONInAPlainSentence(t *testing.T) {
	t.Parallel()
	// Production mutation: the configuration page's refusal of an HTML
	// answer said only that the change was not applied, though the reference
	// says every surface names the status the server answered.
	vocabulary := handlerVocabulary(t)
	state := VocabularyState{Vocabulary: vocabulary, Head: "head-1", Display: core.DisplaySettings{Name: "Atlas"}}
	runClientOverHandler(t, configurableHandler(vocabulary, "head-1", nil),
		"plain sentences for unreadable configuration refusals", "/", "", vocabulary, "head-1", nil, `
  vocabularyRead = `+string(mustJSON(t, vocabularyDocument(state)))+`;
  const panelFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((url.startsWith("/api/vocabulary") || url === "/api/display") && (options.method || "GET") !== "GET") {
      return { ok: false, status: 502, headers: { get: () => null },
        json: async () => JSON.parse("<html><head><title>502 Bad Gateway</title></head></html>") };
    }
    return panelFetch(url, options);
  };
  await openStatuses();
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);
  const said = panelMessages().join(" ");
  if (!said.includes("The server answered HTTP 502 and gave no reason this page can read.")) {
    throw new Error("an HTML 502 to a status change reads " + JSON.stringify(said));
  }
  displayField("primaryColor").value = "#123abc";
  await saveDisplay();
  await settle();
  const settings = displayPanelStatus.textContent;
  if (!settings.includes("These board settings were not applied. The server answered HTTP 502 and gave no reason this page can read.")) {
    throw new Error("an HTML 502 to the board settings reads " + JSON.stringify(settings));
  }
  if (/JSON|Unexpected token/.test(said + settings)) throw new Error("a refusal reports a parse error: " + JSON.stringify(said + settings));
`)
}
