package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the client does with the board's filters: reads them out of the address,
// writes them back into it, narrows the columns it draws, and says how much of
// each column the narrowing left. Everything here runs the served script against
// the fake DOM, because the filtering is the client's and the address is the only
// place it is written down.

const (
	filterAuditID  = "WB-01J0000000000000000000FF01"
	filterPerfID   = "WB-01J0000000000000000000FF02"
	filterQueueID  = "WB-01J0000000000000000000FF03"
	filterBacklogA = "WB-01J0000000000000000000FF04"
	filterBacklogB = "WB-01J0000000000000000000FF05"
)

// boardFilterTasks is three Ready tasks chosen so that each filter in the row
// decides a different one of them: the first passes every filter, the second
// fails only the search, and the third fails only the priority — and it fails it
// while matching the search in its description, which is the half of the
// predicate a search over titles alone would miss.
func boardFilterTasks() []core.Task {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	audit.Description = "Walk every checkpoint."
	audit.Labels = []string{"a,b"}
	perf := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityHigh)
	perf.Rank = "2/1"
	perf.Labels = []string{"a,b"}
	queue := clientPlacementTask(filterQueueID, "Rebuild the queue", core.StatusReady, core.PriorityLow)
	queue.Rank = "3/1"
	queue.Labels = []string{"a,b"}
	queue.Description = "An audit of the drain order."
	return []core.Task{audit, perf, queue}
}

// boardFiltersClientScript renders the board and returns its script, which is
// what every test here executes.
func boardFiltersClientScript(t *testing.T) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	return renderedClientScript(t, response.Body.String())
}

func runBoardFiltersClient(t *testing.T, purpose, program string) string {
	t.Helper()
	node := requireNode(t)
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
	return string(output)
}

// A filtered board is an address, so a filtered address is a board: everything
// the row can hold is read back out of the query, every control in the row states
// it, and the columns draw only what passes.
func TestHandlerClientReadsFiltersFromTheAddress(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?q=audit&priority=high&label=a%2Cb&key=WB&deleted=1", tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  if (filterSearch.value !== "audit") {
    throw new Error("the search box reads " + JSON.stringify(filterSearch.value) + ", want the address's own term");
  }
  if (filterClear.hidden) throw new Error("a filtered board offers no way to clear the filters");
  // Clear drops every narrowing and keeps the Deleted column: that switch is the
  // one control in the row whose state is a column rather than a narrowing.
  if (filterClear.href !== "/?deleted=1") {
    throw new Error("Clear filters points at " + filterClear.href + ", want the same board unfiltered");
  }
  if (!boardCard(` + strconv.Quote(filterAuditID) + `)) {
    throw new Error("the board drew no card for the task that passes every filter");
  }
  if (boardCard(` + strconv.Quote(filterPerfID) + `)) {
    throw new Error("the board drew a card the search does not match");
  }
  if (boardCard(` + strconv.Quote(filterQueueID) + `)) {
    throw new Error("the board drew a card the priority chooser does not match");
  }
}, 0);
`
	runBoardFiltersClient(t, "filters read from the address", program)
}

// Typing narrows the board by writing the address, which is what makes a narrowed
// board a link. It replaces the entry rather than pushing one, so Back reaches
// where the reader came from rather than walking their keystrokes backwards.
func TestHandlerClientSearchWritesTheAddressWithReplace(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  filterSearch.value = "Perf";
  filterSearch.eventListeners.input({ target: filterSearch });
  if (historyReplacements.at(-1) !== "/?q=Perf") {
    throw new Error("typing wrote " + JSON.stringify(historyReplacements));
  }
  if (historyPaths.length !== 0) {
    throw new Error("typing filled Back with keystrokes: " + JSON.stringify(historyPaths));
  }
  if (!boardCard(` + strconv.Quote(filterPerfID) + `)) throw new Error("the search hid the card it matches");
  if (boardCard(` + strconv.Quote(filterAuditID) + `)) throw new Error("the search left a card it does not match");
  if (boardCard(` + strconv.Quote(filterQueueID) + `)) throw new Error("the search left a card it does not match");

  filterSearch.value = "";
  filterSearch.eventListeners.input({ target: filterSearch });
  if (historyReplacements.at(-1) !== "/") {
    throw new Error("emptying the search wrote " + JSON.stringify(historyReplacements));
  }
  if (!filterClear.hidden) throw new Error("an unfiltered board still offers to clear its filters");
  [` + strconv.Quote(filterAuditID) + `, ` + strconv.Quote(filterPerfID) + `, ` + strconv.Quote(filterQueueID) + `]
    .forEach((id) => { if (!boardCard(id)) throw new Error("emptying the search left " + id + " hidden"); });
}, 0);
`
	runBoardFiltersClient(t, "the search writing the address", program)
}

// The two halves of the row do not overwrite each other: the Deleted switch
// carries the search across, and Clear carries the Deleted column across. Each
// control changes the one thing it is about and states the rest of the address.
func TestHandlerClientFiltersKeepTheDeletedSwitchAndClearKeepsDeleted(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	shown := clientDOMHarness("/?q=x&deleted=1", tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  if (deletedToggle.href !== "/?q=x") {
    throw new Error("hiding the column would drop the search: " + deletedToggle.href);
  }
  if (filterClear.href !== "/?deleted=1") {
    throw new Error("clearing the filters would drop the column: " + filterClear.href);
  }
}, 0);
`
	runBoardFiltersClient(t, "the Deleted switch over a searched board", shown)

	hidden := clientDOMHarness("/?q=x", tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  if (deletedToggle.href !== "/?deleted=1&q=x") {
    throw new Error("showing the column would drop the search: " + deletedToggle.href);
  }
}, 0);
`
	runBoardFiltersClient(t, "the Deleted switch over a searched board with the column hidden", hidden)
}

// A column says how many of its cards the filters left, and a column the filters
// emptied says so rather than looking like a column with nothing in it. Neither
// reading appears over a board nobody has filtered.
func TestHandlerClientCountsShowVisibleOverTotalAndNoMatches(t *testing.T) {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	perf := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityMedium)
	perf.Rank = "2/1"
	first := clientPlacementTask(filterBacklogA, "Rebuild the queue", core.StatusBacklog, core.PriorityMedium)
	second := clientPlacementTask(filterBacklogB, "Rewrite the notes", core.StatusBacklog, core.PriorityLow)
	second.Rank = "2/1"
	tasks := []core.Task{audit, perf, first, second}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?q=audit", tasksDocumentJSON(t, tasks)) + script + `
const listFor = (status) => boardLists.find((list) => list.dataset.status === status);
const countFor = (status) => boardCounts.find((element) => element.dataset.count === status);
const emptyLineIn = (status) => findElement(listFor(status), (element) => hasDataKey(element, "filterEmpty"));
setTimeout(async () => {
  await intervalCallback();
  const ready = countFor("ready");
  if (ready.textContent !== "1 / 2") {
    throw new Error("the Ready count reads " + JSON.stringify(ready.textContent) + ", want \"1 / 2\"");
  }
  if (!ready.classList.contains("column__count--filtered")) {
    throw new Error("a narrowed count is drawn as an ordinary one: " + ready.className);
  }
  const backlog = countFor("backlog");
  if (backlog.textContent !== "0 / 2") {
    throw new Error("the Backlog count reads " + JSON.stringify(backlog.textContent) + ", want \"0 / 2\"");
  }
  const emptied = emptyLineIn("backlog");
  if (!emptied || emptied.hidden) {
    throw new Error("a column the filters emptied looks like a column with nothing in it");
  }
  if (emptied.textContent !== "No matches.") {
    throw new Error("the emptied column says " + JSON.stringify(emptied.textContent));
  }
  const narrowed = emptyLineIn("ready");
  if (narrowed && !narrowed.hidden) {
    throw new Error("a column the filters left a card in still says it has no matches");
  }

  // The same board with nothing filtered: the counts are counts again and no
  // column is explaining itself.
  returnTo("/");
  if (countFor("ready").textContent !== "2") {
    throw new Error("an unfiltered Ready count reads " + JSON.stringify(countFor("ready").textContent));
  }
  if (countFor("ready").classList.contains("column__count--filtered")) {
    throw new Error("an unfiltered count is still drawn as a narrowed one");
  }
  if (countFor("backlog").textContent !== "2") {
    throw new Error("an unfiltered Backlog count reads " + JSON.stringify(countFor("backlog").textContent));
  }
  if (!emptyLineIn("backlog").hidden) {
    throw new Error("an unfiltered column says it has no matches");
  }
}, 0);
`
	runBoardFiltersClient(t, "counts and the no-matches line", program)
}

// The Deleted column is a column, so the filters narrow it like any other. A
// tombstone the search does not match is not drawn, and the column says how many
// it is holding back.
func TestHandlerClientDeletedCardsAreFilteredToo(t *testing.T) {
	tasks := deletedColumnTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?deleted=1&q=zzz", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{tasks[0], tasks[1]}) + `;
setTimeout(async () => {
  await intervalCallback();
  const column = deletedColumn();
  if (!column) throw new Error("the deleted address drew no column to filter");
  if (deletedCards().length !== 0) {
    throw new Error("the column drew " + deletedCards().length + " tombstones the search does not match");
  }
  const count = findElement(column, (element) => hasDataKey(element, "deletedCount"));
  if (!count || count.textContent !== "0 / 1") {
    throw new Error("the Deleted count reads " + JSON.stringify(count && count.textContent) + ", want \"0 / 1\"");
  }
  const emptied = findElement(column, (element) => hasDataKey(element, "filterEmpty"));
  if (!emptied || emptied.hidden) throw new Error("the filtered Deleted column says nothing about why it is empty");
  // "No deleted tasks." would be the wrong answer: the server holds one.
  const nothing = findElement(column, (element) => hasDataKey(element, "deletedEmpty"));
  if (!nothing || !nothing.hidden) {
    throw new Error("the column claims the server holds no deleted tasks while the filters are what emptied it");
  }
}, 0);
`
	runBoardFiltersClient(t, "filters over the Deleted column", program)
}

// A card the reader has just made is never filtered away. The create's stand-in
// is the only thing drawing that task, and a search typed over it must not take
// it off the board under them.
func TestHandlerClientPendingCreationIsNeverFiltered(t *testing.T) {
	script := newTaskClientScript(t, "/tasks/new?status=ready")
	empty := tasksDocumentJSON(t, nil)

	program := clientDOMHarness("/tasks/new?status=ready", empty) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    // The create is never answered, so the stand-in is the only card there is.
    if (url === "/api/tasks" && options.method === "POST") {
      return { ok: true, json: async () => new Promise(() => {}) };
    }
    if (url === "/api/tasks?deleted=true") {
      return { ok: true, json: async () => ({ format: "workbook.tasks", version: 1, tasks: [] }) };
    }
    return { ok: true, json: async () => (` + empty + `) };
  };

  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "Instant task";
  form.eventListeners.submit({ preventDefault() {} });
  if (main.firstElementChild !== boardView) throw new Error("Save waited for the server before leaving the form");
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const standIns = () => ready.querySelectorAll(".task-card").filter((node) => node.dataset.pendingCreate === "true");
  if (standIns().length !== 1) throw new Error("the board drew " + standIns().length + " stand-ins, want 1");

  filterSearch.value = "zzz";
  filterSearch.eventListeners.input({ target: filterSearch });
  if (historyReplacements.at(-1) !== "/?q=zzz") {
    throw new Error("the search wrote " + JSON.stringify(historyReplacements));
  }
  if (standIns().length !== 1) {
    throw new Error("a search filtered away the card the reader had just made");
  }
}, 0);
`
	runBoardFiltersClient(t, "a stand-in under an active search", program)
}

// The row acts on the columns, and a task's own page draws none. It travels with
// the board the way the Descriptions setting does, and it is the row that goes
// rather than each control in it.
func TestHandlerClientHidesTheFilterRowOffTheBoard(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/tasks/"+filterAuditID, tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  if (!filterRow.hidden) throw new Error("a task's own page offered the board's filter row");
  if (deletedToggle.hidden) {
    throw new Error("the switch hides itself as well as the row, so revealing the row would not reveal it");
  }
  returnTo("/");
  if (filterRow.hidden) throw new Error("coming back to the board did not reveal the filter row");
}, 0);
`
	runBoardFiltersClient(t, "the filter row off the board", program)
}
