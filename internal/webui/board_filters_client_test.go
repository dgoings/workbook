package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
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

// boardFilterChooserHelpers is how a test reads a chooser: its parts, the options
// it is listing, what each one says, and the two gestures a reader has — opening a
// menu and ticking an option. The options are read out of the menu rather than off
// any record the client keeps, because what a reader can act on is what is in the
// document.
const boardFilterChooserHelpers = `
function filterChooser(group) {
  const root = filterChooserRoots.find((element) => element.dataset.filterChooser === group);
  if (!root) throw new Error("the filter row has no " + group + " chooser");
  return {
    root,
    button: root.querySelector("[data-filter-chooser-button]"),
    // The badge and the number inside it. The badge also holds a word only a
    // screen reader hears, so its own textContent is the two together and the
    // count is read off the span the client writes.
    badge: root.querySelector("[data-filter-chooser-badge]"),
    count: root.querySelector("[data-filter-chooser-count]"),
    menu: root.querySelector("[data-filter-chooser-menu]"),
    // The parts only a menu that narrows itself has: the box, the line it says
    // when the box has hidden every row, and the container the rows are drawn
    // into. The first two are null on the choosers the page serves without them.
    search: root.querySelector("[data-filter-chooser-search]"),
    noMatch: root.querySelector("[data-filter-chooser-nomatch]"),
    optionsList: root.querySelector("[data-filter-chooser-options]")
  };
}
// One chooser's options, in the order it lists them: the value a tick writes, the
// name a reader sees, the count beside it, whether it is ticked, whether it is
// drawn as an option that would leave nothing, and the box the tick is raised on.
function chooserRows(group) {
  const menu = filterChooser(group).menu;
  return menu.querySelectorAll("[data-filter-option]").map((input) => {
    const label = input.parentElement;
    const spans = label.children.filter((child) => child.tagName === "SPAN");
    const count = spans.find((span) => hasClassToken(span, "count"));
    const name = spans.find((span) => span !== count);
    return {
      value: input.value,
      name: name ? name.textContent : "",
      count: count ? count.textContent : "",
      checked: input.checked === true,
      none: hasClassToken(label, "filter-option--none"),
      // Whether the menu's own box has narrowed this row away. Rows are hidden
      // rather than removed, so the list is the whole list whatever is typed and
      // the keyed rebuild above has nothing to do with the narrowing.
      hidden: label.hidden === true,
      input,
      label
    };
  });
}
// The rows a reader can see, by name, which is what a claim about a narrowing is
// a claim about.
function visibleChooserNames(group) {
  return chooserRows(group).filter((row) => !row.hidden).map((row) => row.name);
}
// A reader typing into a menu's own box: the value, then the input event the
// browser raises. Nothing else — what is typed here is menu state, so a
// keystroke that reached the address would be the defect.
function typeIntoChooserSearch(group, text) {
  const search = filterChooser(group).search;
  if (!search) throw new Error("the " + group + " menu has no search box");
  search.value = text;
  search.eventListeners.input({ target: search });
  return search;
}
// Escape pressed inside that box, raised the way the browser raises it: the box's
// own listener, and then the document unless the box stopped it there.
function escapeChooserSearch(group) {
  const search = filterChooser(group).search;
  if (!search) throw new Error("the " + group + " menu has no search box");
  return search.keydown({ key: "Escape" });
}
// What a menu reads as, one option per entry, which is what a test states when it
// is making a claim about the whole list rather than about one option in it.
function chooserReading(group) {
  return chooserRows(group).map((row) => row.name + " " + row.count);
}
function chooserRow(group, value) {
  const row = chooserRows(group).find((candidate) => candidate.value === value);
  if (!row) {
    throw new Error("the " + group + " chooser lists no " + JSON.stringify(value) + ", only " +
      JSON.stringify(chooserRows(group).map((candidate) => candidate.value)));
  }
  return row;
}
// A reader ticking an option: the browser flips the box, raises change, and lets
// the click reach the document — where the row decides whether the click was
// inside an open menu or outside every one of them. All three, because a tick
// that only raised change would never exercise the menu staying open.
function tickOption(group, value) {
  const row = chooserRow(group, value);
  row.input.checked = !row.input.checked;
  row.input.eventListeners.change({ target: row.input });
  documentEventListeners.click({
    target: row.input, button: 0, preventDefault() {}, stopPropagation() {}
  });
  return row.input;
}
// A reader clicking a chooser's button, which is the click the browser raises: the
// button's own listener, and then the document, where the row closes every other
// menu.
function openChooserMenu(group) {
  const chooser = filterChooser(group);
  chooser.button.click();
  return chooser;
}
function chooserIsOpen(group) {
  const chooser = filterChooser(group);
  return chooser.menu.hidden === false && chooser.button.getAttribute("aria-expanded") === "true";
}
// A click on something that is no part of any chooser, raised at the document the
// way a browser raises it.
function clickOutsideChoosers(element) {
  return documentEventListeners.click({
    target: element, button: 0, preventDefault() {}, stopPropagation() {}
  });
}
`

// Every priority the project defines is offered, in the project's own order, and
// each says how many cards it would leave. Ticking one is a navigation, so a
// filtered board is a link the way a searched one is — and the tick survives the
// poll a second later, because the menus are drawn in place rather than rebuilt.
func TestHandlerClientPriorityChooserListsEveryPriorityWithCounts(t *testing.T) {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	perf := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityHigh)
	perf.Rank = "2/1"
	queue := clientPlacementTask(filterQueueID, "Rebuild the queue", core.StatusReady, core.PriorityLow)
	queue.Rank = "3/1"
	tasks := []core.Task{audit, perf, queue}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  // The project's order, which is the order the board sorts by, rather than the
  // order the counts would put them in.
  const reading = chooserReading("priority").join(", ");
  if (reading !== "High 2, Medium 0, Low 1") {
    throw new Error("the Priority chooser reads " + JSON.stringify(reading));
  }
  // A priority no card holds is still offered and still tickable; it is drawn as
  // the option it is, so ticking it is a reader's choice rather than a surprise.
  if (!chooserRow("priority", "medium").none) {
    throw new Error("an option that would leave nothing is drawn as one that would leave something");
  }
  if (chooserRow("priority", "high").none) {
    throw new Error("an option that would leave two cards is drawn as one that would leave none");
  }
  const chooser = filterChooser("priority");
  if (!chooser.badge.hidden) throw new Error("a chooser with nothing ticked is counting something");

  // Ticked from inside an open menu, which is where a reader ticks: the tick is a
  // navigation and the render it causes must leave the list they are working
  // through standing, or a second tick costs them another click on the button.
  openChooserMenu("priority");
  tickOption("priority", "low");
  if (!chooserIsOpen("priority")) {
    throw new Error("ticking an option closed the menu the reader was working through");
  }
  // Pushed rather than replaced: a filtered board is one the reader chose, and
  // Back should undo the choice.
  if (historyPaths.at(-1) !== "/?priority=low") {
    throw new Error("ticking an option wrote " + JSON.stringify(historyPaths));
  }
  if (chooser.badge.hidden || chooser.count.textContent !== "1") {
    throw new Error("the badge reads " + JSON.stringify(chooser.count.textContent) +
      " and hidden=" + chooser.badge.hidden);
  }
  // What a screen reader is handed: the number and the word that says what it
  // counts, rather than a bare digit beside a caption.
  if (chooser.badge.textContent !== "1 selected") {
    throw new Error("the badge announces " + JSON.stringify(chooser.badge.textContent));
  }
  if (!filterChooser("priority").root.classList.contains("filter-chooser--active")) {
    throw new Error("a chooser holding a tick is drawn as one holding none");
  }
  if (!boardCard(` + strconv.Quote(filterQueueID) + `)) throw new Error("the ticked priority hid its own card");
  [` + strconv.Quote(filterAuditID) + `, ` + strconv.Quote(filterPerfID) + `].forEach((id) => {
    if (boardCard(id)) throw new Error("the board still draws " + id + " under another priority");
  });
  if (!chooserRow("priority", "low").checked) {
    throw new Error("the option the reader ticked came back unticked from its own navigation");
  }

  // A second later the poll redraws the board. The tick is the address's, so it
  // has to still be there.
  await intervalCallback();
  if (!chooserRow("priority", "low").checked) {
    throw new Error("the poll untidied the reader's tick");
  }
  if (chooserReading("priority").join(", ") !== "High 2, Medium 0, Low 1") {
    throw new Error("the poll changed what the chooser lists: " + chooserReading("priority").join(", "));
  }
}, 0);
`
	runBoardFiltersClient(t, "the priority chooser", program)
}

// An option's count is a count against every other filter, not against the whole
// board: it answers "how many cards would this leave me", which is the only
// reading a reader can act on. A label only a filtered-out card carries says zero
// and is drawn as the option it is.
func TestHandlerClientLabelChooserCountsAgainstTheOtherFilters(t *testing.T) {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	audit.Labels = []string{"ledger"}
	queue := clientPlacementTask(filterQueueID, "Audit the queue", core.StatusReady, core.PriorityHigh)
	queue.Rank = "2/1"
	queue.Labels = []string{"queue"}
	perf := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityLow)
	perf.Rank = "3/1"
	perf.Labels = []string{"perf"}
	notes := clientPlacementTask(filterBacklogA, "Rewrite the notes", core.StatusBacklog, core.PriorityLow)
	notes.Labels = []string{"ledger", "stale"}
	tasks := []core.Task{audit, queue, perf, notes}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?q=audit", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  // Every label on the board, sorted, because nobody declares a label: the only
  // list there is is the one the cards carry.
  const reading = chooserReading("label").join(", ");
  // "ledger" is on two cards and the search leaves one of them, which is the
  // whole point of counting against the other filters.
  if (reading !== "ledger 1, perf 0, queue 1, stale 0") {
    throw new Error("the Labels chooser reads " + JSON.stringify(reading));
  }
  ["perf", "stale"].forEach((value) => {
    if (!chooserRow("label", value).none) {
      throw new Error(value + " would leave nothing and is drawn as though it would leave something");
    }
  });
  ["ledger", "queue"].forEach((value) => {
    if (chooserRow("label", value).none) {
      throw new Error(value + " would leave a card and is drawn as though it would leave none");
    }
  });
  // Ticking the honest zero is allowed, and the board says so rather than looking
  // like a control that did nothing.
  tickOption("label", "perf");
  if (historyPaths.at(-1) !== "/?q=audit&label=perf") {
    throw new Error("ticking a label wrote " + JSON.stringify(historyPaths));
  }
  const readyEmpty = findElement(
    boardLists.find((list) => list.dataset.status === "ready"),
    (element) => hasDataKey(element, "filterEmpty"));
  if (!readyEmpty || readyEmpty.hidden) {
    throw new Error("ticking an option that leaves nothing left the column looking simply empty");
  }
}, 0);
`
	runBoardFiltersClient(t, "the label chooser's counts", program)
}

// The Key chooser is the one control in the row that a project can have nothing to
// say with: most projects mint under one key, and a chooser offering one
// alternative is a control with nothing to choose. A project with two offers both,
// retired keys included and said to be retired — their tasks exist and the chooser
// is how a reader reaches them.
func TestHandlerClientKeyChooserHidesWithOneKey(t *testing.T) {
	first := clientPlacementTask("AB-01J0000000000000000000FF11", "Audit the ledger", core.StatusReady, core.PriorityHigh)
	second := clientPlacementTask("CD-01J0000000000000000000FF12", "Rebuild the queue", core.StatusReady, core.PriorityLow)
	second.Rank = "2/1"
	// A task whose ID spells its key in lower case, which is not what Workbook
	// mints but is what a hand-edited store or an older clone can hold.
	lowerCased := clientPlacementTask("cd-01J0000000000000000000FF13", "Drain the queue", core.StatusReady, core.PriorityLow)
	lowerCased.Rank = "3/1"
	tasks := []core.Task{first, second}
	script := boardFiltersClientScript(t)

	one := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) +
		keyChooserPrelude(t, oneKeyProject()) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  if (!filterChooser("key").root.hidden) {
    throw new Error("a project minting under one key was offered a choice of keys");
  }
}, 0);
`
	runBoardFiltersClient(t, "the key chooser over a one-key project", one)

	keys, err := core.NewKeySet(core.KeyDocument{
		Keys:    []core.KeyDefinition{{Key: "AB"}, {Key: "CD", Retired: true}},
		Current: "AB",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	two := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) +
		keyChooserPrelude(t, keys) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  if (filterChooser("key").root.hidden) {
    throw new Error("a project with two keys was offered no choice between them");
  }
  const reading = chooserReading("key").join(", ");
  if (reading !== "AB 1, CD (retired) 1") {
    throw new Error("the Key chooser reads " + JSON.stringify(reading));
  }
  // A retired key still filters: it mints nothing, and everything it ever minted
  // is still on the board.
  tickOption("key", "CD");
  if (historyPaths.at(-1) !== "/?key=CD") {
    throw new Error("ticking a key wrote " + JSON.stringify(historyPaths));
  }
  if (!boardCard("CD-01J0000000000000000000FF12")) {
    throw new Error("the retired key hid the card it minted");
  }
  if (boardCard("AB-01J0000000000000000000FF11")) {
    throw new Error("the board still draws a card minted under another key");
  }

  // A key is upper-case everywhere it is written, so an address typed by hand in
  // lower case is the same board rather than an empty one.
  returnTo("/?key=cd");
  if (!boardCard("CD-01J0000000000000000000FF12")) {
    throw new Error("/?key=cd drew nothing, so a hand-typed key filters everything away");
  }
  if (boardCard("AB-01J0000000000000000000FF11")) {
    throw new Error("/?key=cd narrowed nothing");
  }
  if (!chooserRow("key", "CD").checked) {
    throw new Error("/?key=cd left the chooser's own option unticked");
  }
  // The other half of the same folding: a task whose ID was not written in upper
  // case is still a task under that key. The two readings are compared, so they
  // have to be folded the same way or the folding is only half done.
  taskResponse = ` + tasksDocumentJSON(t, []core.Task{lowerCased, first}) + `;
  await intervalCallback();
  if (!boardCard(` + strconv.Quote(lowerCased.ID) + `)) {
    throw new Error("/?key=CD drew no card for a task whose ID spells the key in lower case");
  }
}, 0);
`
	runBoardFiltersClient(t, "the key chooser over a project with a retired key", two)
}

// keyChooserPrelude restates the two key attributes the server renders, which is
// the only way a test says what keys a project has: the shared harness publishes
// what a board built with no key resolver publishes — none at all.
func keyChooserPrelude(t *testing.T, keys core.KeySet) string {
	t.Helper()
	return `
boardView.dataset.keys = ` + strconv.Quote(pageKeys(keys)) + `;
boardView.dataset.currentKey = ` + strconv.Quote(keys.Current()) + `;
`
}

// A menu is a menu: one at a time, closed by a click anywhere else, and closed by
// Escape with the caret handed back to the button that opened it. A reader who
// opened one and changed their mind is never left with a list they cannot dismiss.
func TestHandlerClientOnlyOneChooserIsOpenAndOutsideClickCloses(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  if (chooserIsOpen("priority") || chooserIsOpen("label")) {
    throw new Error("the row opened a menu nobody asked for");
  }
  openChooserMenu("priority");
  if (!chooserIsOpen("priority")) throw new Error("the Priority button opened no menu");

  openChooserMenu("label");
  if (!chooserIsOpen("label")) throw new Error("the Labels button opened no menu");
  if (chooserIsOpen("priority")) {
    throw new Error("two menus are open at once, so one is standing over the board with nothing pointing at it");
  }

  await clickOutsideChoosers(main);
  if (chooserIsOpen("label") || chooserIsOpen("priority")) {
    throw new Error("a click on the board left a menu open");
  }

  // Escape closes the one that is open and hands the caret back, so a reader who
  // opened the menu from the keyboard is left where they were.
  const chooser = openChooserMenu("priority");
  chooser.button.focus();
  documentEventListeners.keydown({ key: "Escape", preventDefault() {} });
  if (chooserIsOpen("priority")) throw new Error("Escape left the menu open");
  if (document.activeElement !== chooser.button) {
    throw new Error("Escape dropped the caret rather than handing it back to the button");
  }
}, 0);
`
	runBoardFiltersClient(t, "one menu at a time", program)
}

// The menus are drawn in place because the board redraws every second. A reader
// working down an open list must not have it rebuilt under their finger: the tick
// they are on keeps the caret, and the menu stays open.
func TestHandlerClientChooserOptionsSurviveAPollWithFocus(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = openChooserMenu("priority");
  const box = chooserRow("priority", "medium").input;
  box.focus();
  if (document.activeElement !== box) throw new Error("the harness could not focus a chooser option");

  await intervalCallback();
  await intervalCallback();
  if (document.activeElement !== box) {
    throw new Error("the poll took the caret out of the option the reader was on");
  }
  if (chooserRow("priority", "medium").input !== box) {
    throw new Error("the poll replaced the option node, so the caret it kept is on a node nobody can see");
  }
  if (!chooserIsOpen("priority")) throw new Error("the poll closed the open menu");
}, 0);
`
	runBoardFiltersClient(t, "an open menu across two polls", program)
}

// A menu goes with the row. Leaving the board hides the row, and an open menu
// hidden with it is a menu nothing can close: Back would bring the board back with
// a list standing over it that the reader never opened, and until then the page
// would be claiming Escape on a route where the relationship combobox wants it.
func TestHandlerClientClosesAnOpenChooserWhenTheRouteLeavesTheBoard(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = openChooserMenu("priority");
  if (!chooserIsOpen("priority")) throw new Error("the Priority button opened no menu");

  returnTo("/tasks/" + ` + strconv.Quote(filterAuditID) + `);
  if (!filterRow.hidden) throw new Error("a task's own page still offers the filter row");
  if (chooserIsOpen("priority")) {
    throw new Error("leaving the board left a menu open behind the hidden row");
  }
  if (chooser.button.getAttribute("aria-expanded") !== "false") {
    throw new Error("the button still says its menu is expanded: " +
      chooser.button.getAttribute("aria-expanded"));
  }
  // Escape belongs to whatever is on screen now. What decides is the row being
  // hidden, not whether a menu happens to be open — so the menu is forced open
  // here, which is the state the guard exists for: with the row off screen there
  // is nothing of ours to dismiss, and claiming Escape would take it from the
  // relationship combobox, which closes its listbox with it.
  chooser.menu.hidden = false;
  const escape = { key: "Escape", prevented: false, preventDefault() { this.prevented = true; } };
  documentEventListeners.keydown(escape);
  if (escape.prevented) {
    throw new Error("the hidden filter row claimed Escape on a route that draws no columns");
  }
  chooser.menu.hidden = true;

  returnTo("/");
  if (filterRow.hidden) throw new Error("coming back to the board did not reveal the filter row");
  if (chooserIsOpen("priority")) {
    throw new Error("the board came back with a menu open that the reader never reopened");
  }
}, 0);
`
	runBoardFiltersClient(t, "an open chooser across a route change", program)
}

// A group with nothing to list says so. Nobody declares a label, so a board whose
// cards carry none has an empty Labels menu — and an empty menu reads as a menu
// that failed to draw, which is the one thing it must not be mistaken for.
func TestHandlerClientLabelChooserSaysSoWhenNothingIsLabelled(t *testing.T) {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	queue := clientPlacementTask(filterQueueID, "Rebuild the queue", core.StatusReady, core.PriorityLow)
	queue.Rank = "2/1"
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, []core.Task{audit, queue})) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  if (chooserRows("label").length !== 0) {
    throw new Error("the Labels chooser offered " + chooserRows("label").length + " labels over a board carrying none");
  }
  // Read off the container the rows are drawn into rather than off the menu: the
  // menu also holds the box that narrows the rows and the line that says the box
  // has matched nothing, and neither is a row.
  const chooser = filterChooser("label");
  if (chooser.optionsList.children.length !== 1) {
    throw new Error("the empty Labels menu holds " + chooser.optionsList.children.length +
      " lines, want the one that explains it");
  }
  const line = chooser.optionsList.children[0];
  if (!hasClassToken(line, "filter-option--none") || line.textContent !== "No labels") {
    throw new Error("the empty Labels menu says " + JSON.stringify(line.textContent) +
      " in " + JSON.stringify(line.className));
  }
  // "No labels" and "No labels match" are different sentences, and a board with
  // no labels at all is the first of them: nothing was typed, so nothing has been
  // narrowed away.
  if (!chooser.noMatch.hidden) {
    throw new Error("a board carrying no labels is told its search matched nothing");
  }
  // The Priority chooser is not empty over the same board: the priorities are the
  // project's, so they are listed whether or not a card holds one.
  if (chooserRows("priority").length === 0) {
    throw new Error("the Priority chooser emptied itself over a board with labels missing");
  }
}, 0);
`
	runBoardFiltersClient(t, "the Labels chooser over an unlabelled board", program)
}

// Back over a search empties the box as well as the board. The render the typing
// itself causes leaves the box alone — it would move the caret to the end of what
// it assigned — and every other render writes it, which is what keeps the box from
// standing over a board it is no longer narrowing.
func TestHandlerClientBackFromASearchEmptiesTheSearchBox(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) + script + `
setTimeout(async () => {
  await intervalCallback();
  // The caret is in the box, which is the case the old guard could not tell apart
  // from a render caused by the typing.
  filterSearch.focus();
  filterSearch.value = "x";
  filterSearch.eventListeners.input({ target: filterSearch });
  if (historyReplacements.at(-1) !== "/?q=x") {
    throw new Error("typing wrote " + JSON.stringify(historyReplacements));
  }
  if (filterSearch.value !== "x") throw new Error("the render caused by the typing rewrote what was typed");

  returnTo("/");
  if (filterSearch.value !== "") {
    throw new Error("Back left " + JSON.stringify(filterSearch.value) + " in a box that is narrowing nothing");
  }
  if (!filterClear.hidden) throw new Error("Back left the board offering to clear filters it no longer holds");
  [` + strconv.Quote(filterAuditID) + `, ` + strconv.Quote(filterPerfID) + `, ` + strconv.Quote(filterQueueID) + `]
    .forEach((id) => { if (!boardCard(id)) throw new Error("Back left " + id + " hidden"); });
}, 0);
`
	runBoardFiltersClient(t, "Back out of a search", program)
}

// A card dropped into a column the filters had emptied lands there, and the line
// that was explaining the emptiness goes with it. The dragged card matches the
// search, so the board draws it where it was put: shownTasks filters what the
// server has answered for, and a card that still passes is still drawn.
func TestHandlerClientDropsACardIntoAFilterEmptiedColumn(t *testing.T) {
	keeper := clientPlacementTask(filterAuditID, "Keep the audit", core.StatusReady, core.PriorityHigh)
	keeper.Head = "head-keeper"
	sweep := clientPlacementTask(filterPerfID, "Sweep the floor", core.StatusInProgress, core.PriorityHigh)
	sweep.Head = "head-sweep"
	moved := keeper
	moved.Status = core.StatusInProgress
	moved.Head = "head-moved"
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?q=keep", tasksDocumentJSON(t, []core.Task{keeper, sweep})) + script + `
setTimeout(async () => {
  await intervalCallback();
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const emptied = findElement(inProgress, (element) => hasDataKey(element, "filterEmpty"));
  if (!emptied || emptied.hidden) {
    throw new Error("the search did not leave the destination column explaining itself");
  }
  if (inProgress.children.length !== 1 || inProgress.children[0] !== emptied) {
    throw new Error("the destination column holds more than the line that explains it");
  }

  const writes = [];
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      writes.push({ url, method: options.method, body: JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, moved)) + ` }) };
    }
    return { ok: true, json: async () => taskResponse };
  };

  const card = boardCard(` + strconv.Quote(filterAuditID) + `);
  if (!card) throw new Error("the search hid the card the test is about to drag");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const dropped = documentEventListeners.drop({
    target: inProgress, clientY: 10, dataTransfer, preventDefault() {}
  });
  await Promise.resolve();

  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(filterAuditID) + `)) {
    throw new Error("the card did not land in the column it was dropped on");
  }
  if (!emptied.hidden) {
    throw new Error("the column says it has no matches while drawing the card just dropped into it");
  }
  await dropped;
  if (writes.length !== 1 || writes[0].method !== "PATCH") {
    throw new Error("the drop sent " + JSON.stringify(writes));
  }
  if (writes[0].body.status !== "in-progress") {
    throw new Error("the drop named the status " + JSON.stringify(writes[0].body.status));
  }
  // And the column it left, which the search had left holding one card, now holds
  // none of its own — so it says nothing rather than claiming a match it lost.
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  if (ready.querySelectorAll(".task-card").length !== 0) {
    throw new Error("the card is still drawn in the column it was dragged out of");
  }
  const readyEmpty = findElement(ready, (element) => hasDataKey(element, "filterEmpty"));
  if (readyEmpty && !readyEmpty.hidden) {
    throw new Error("the column the card left says the filters emptied it, which is not what emptied it");
  }
}, 0);
`
	runBoardFiltersClient(t, "a drop into a filter-emptied column", program)
}

// A ticked value stays in its menu even when the board stops offering it.
//
// The reproduction a browser found: tick a label only a deleted task carries, then
// hide the Deleted column. The label leaves the board with the card that carried
// it, so the menu stopped listing it — and the filter was still in the address,
// still counted by the badge, and still emptying every column, with no row left to
// untick and nothing on the page saying what was narrowing it.
func TestHandlerClientKeepsATickedLabelTheBoardStoppedDrawing(t *testing.T) {
	active := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	active.Labels = []string{"ledger"}
	doomed := clientPlacementTask(filterQueueID, "Rebuild the queue", core.StatusReady, core.PriorityHigh)
	doomed.Rank = "2/1"
	doomed.Labels = []string{"doomed"}
	doomed.Deleted = true
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{active})) + script + boardFilterChooserHelpers + `
includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{active, doomed}) + `;
setTimeout(async () => {
  await intervalCallback();
  if (!deletedColumn()) throw new Error("the deleted address drew no Deleted column to carry the tombstone");
  // The label is on the board only because that column is drawing the one card
  // that has it, which is what makes this the case the menus used to lose.
  if (chooserRow("label", "doomed").count !== "1") {
    throw new Error("the label the tombstone carries counts " +
      JSON.stringify(chooserRow("label", "doomed").count) + ", want \"1\"");
  }
  tickOption("label", "doomed");
  if (historyPaths.at(-1) !== "/?deleted=1&label=doomed") {
    throw new Error("ticking the label wrote " + JSON.stringify(historyPaths.at(-1)));
  }

  // Hiding the column takes the only card carrying that label off the board. The
  // filter is untouched — the switch decides a column and nothing else — so the
  // menu now has to list a value the board is not offering.
  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  if (historyPaths.at(-1) !== "/?label=doomed") {
    throw new Error("hiding the column wrote " + JSON.stringify(historyPaths.at(-1)));
  }
  if (deletedColumn()) throw new Error("hiding the column left it on the board");
  const chooser = filterChooser("label");
  if (chooser.badge.hidden || chooser.count.textContent !== "1") {
    throw new Error("the badge stopped counting a filter the address still carries: hidden=" +
      chooser.badge.hidden + " count=" + JSON.stringify(chooser.count.textContent));
  }
  const row = chooserRow("label", "doomed");
  if (!row.checked) {
    throw new Error("the label the reader ticked is listed unticked, so the badge counts something the menu denies");
  }
  // Zero, honestly: nothing the board is drawing carries it. That is the whole
  // reading a reader needs — the filter is on, and it is what emptied the board.
  if (row.count !== "0") {
    throw new Error("the ticked label counts " + JSON.stringify(row.count) + ", want \"0\"");
  }
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  if (ready.querySelectorAll(".task-card").length !== 0) {
    throw new Error("the filter the address carries is not narrowing the board");
  }

  // And the row is the way back out, which is the point of listing it.
  tickOption("label", "doomed");
  if (historyPaths.at(-1) !== "/") {
    throw new Error("unticking the label wrote " + JSON.stringify(historyPaths.at(-1)));
  }
  if (!filterChooser("label").badge.hidden) {
    throw new Error("unticking the only ticked label left the chooser counting one");
  }
  if (!boardCard(` + strconv.Quote(filterAuditID) + `)) {
    throw new Error("clearing the label did not bring the board back");
  }
}, 0);
`
	runBoardFiltersClient(t, "a ticked label the board stopped drawing", program)
}

// A token the project has never heard of gets a row that names it. An address is
// shareable and hand-editable, so ?priority=bogus happens — a typo, or a priority
// renamed since the link was sent — and an empty board with an empty menu tells
// the reader nothing about which word emptied it.
func TestHandlerClientNamesAnUnknownPriorityInItsChooser(t *testing.T) {
	tasks := boardFilterTasks()
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?priority=bogus", tasksDocumentJSON(t, tasks)) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const row = chooserRow("priority", "bogus");
  if (row.name !== "bogus") {
    throw new Error("the unknown priority is listed as " + JSON.stringify(row.name) +
      ", want the token the address spells");
  }
  if (!row.checked) throw new Error("the priority the address is filtering by is listed unticked");
  if (row.count !== "0") {
    throw new Error("the unknown priority counts " + JSON.stringify(row.count) + ", want \"0\"");
  }
  // After the vocabulary's own options, so the order the project stated is still
  // the order the reader reads down the menu.
  const values = chooserRows("priority").map((candidate) => candidate.value);
  if (values.at(-1) !== "bogus" || values.slice(0, -1).join(",") !== "high,medium,low") {
    throw new Error("the Priority menu lists " + JSON.stringify(values));
  }
  const chooser = filterChooser("priority");
  if (chooser.badge.hidden || chooser.count.textContent !== "1") {
    throw new Error("the badge does not count the filter the address carries");
  }
  if (boardCard(` + strconv.Quote(filterAuditID) + `)) {
    throw new Error("a priority no task holds did not narrow the board");
  }
  tickOption("priority", "bogus");
  if (historyPaths.at(-1) !== "/") {
    throw new Error("unticking the unknown priority wrote " + JSON.stringify(historyPaths.at(-1)));
  }
  if (!boardCard(` + strconv.Quote(filterAuditID) + `)) {
    throw new Error("clearing the unknown priority did not bring the board back");
  }
}, 0);
`
	runBoardFiltersClient(t, "an unknown priority in the address", program)
}

// Every landing on the board is the board the reader came from. Saving a task —
// new or existing — used to go to the bare address and throw away whatever the
// reader had the board narrowed down to, so filing a task from a searched board
// cost them the search.
func TestHandlerClientSavingATaskLandsOnTheFilteredBoard(t *testing.T) {
	existing := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityHigh)
	existing.Head = "head-1"
	saved := existing
	saved.Description = "Swept."
	saved.Head = "head-2"
	created := clientPlacementTask(filterAuditID, "Sweep the ledger", core.StatusReady, core.PriorityMedium)
	created.Rank = "2/1"
	// A create that staged a relationship is the other create: it waits for the
	// server rather than leaving the moment Save is pressed, and it decides its
	// landing in a second place. Both places have to reach the same board.
	second := clientPlacementTask(filterBacklogA, "Sweep the backlog", core.StatusReady, core.PriorityMedium)
	second.Rank = "3/1"
	linked := second
	linked.Dependencies = []string{existing.ID}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?q=sweep", tasksDocumentJSON(t, []core.Task{existing})) + script + `
setTimeout(async () => {
  await intervalCallback();
  if (filterSearch.value !== "sweep") throw new Error("the board did not read the search out of the address");
  const boardFetch = globalThis.fetch;
  let createCalls = 0;
  let nextCreate = ` + taskMutationJSON(tasksDocumentJSON(t, []core.Task{created}), "") + `;
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/tasks" && options.method === "POST") {
      createCalls += 1;
      fetchCalls.push({ url, options });
      return { ok: true, json: async () => nextCreate };
    }
    if (options.method === "PUT" && url.includes("/dependencies/")) {
      fetchCalls.push({ url, options });
      return { ok: true, json: async () => (` + taskMutationJSON(tasksDocumentJSON(t, []core.Task{linked}), "") + `) };
    }
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      return { ok: true, json: async () => (` + taskMutationJSON(tasksDocumentJSON(t, []core.Task{saved}), "") + `) };
    }
    return boardFetch(url, options);
  };

  // A reader opens a task from the board they had narrowed down, edits it, saves.
  const detail = new TestElement("a");
  detail.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(existing.ID) + `);
  await documentEventListeners.click({
    target: detail, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const description = findElement(main, (element) => element.id === "task-description");
  if (!description) throw new Error("the task's detail form did not render");
  description.value = "Swept.";
  await findElement(main, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  if (historyPaths.at(-1) !== "/?q=sweep") {
    throw new Error("the detail save landed on " + JSON.stringify(historyPaths.at(-1)) + ", want the filtered board");
  }
  if (filterSearch.value !== "sweep") throw new Error("the save landed on a board whose search box is empty");

  // And a task filed from that board, which is the other landing.
  taskResponse = ` + tasksDocumentJSON(t, []core.Task{existing, created}) + `;
  const newTask = new TestElement("a");
  newTask.href = "/tasks/new?status=ready";
  await documentEventListeners.click({
    target: newTask, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const title = findElement(main, (element) => element.id === "task-title");
  if (!title) throw new Error("the New Task form did not render");
  title.value = ` + strconv.Quote(created.Title) + `;
  await findElement(main, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  if (createCalls !== 1) throw new Error("Save created " + createCalls + " tasks");
  if (historyPaths.at(-1) !== "/?q=sweep") {
    throw new Error("the create landed on " + JSON.stringify(historyPaths.at(-1)) + ", want the filtered board");
  }
  if (filterSearch.value !== "sweep") throw new Error("the create landed on a board whose search box is empty");
  if (main.firstElementChild !== boardView) throw new Error("the create did not land on the board");

  // And the create that waits for the server, which chooses its landing in a
  // place of its own: one that staged a relationship, so the ID the server
  // assigns is needed before the second write can go.
  nextCreate = ` + taskMutationJSON(tasksDocumentJSON(t, []core.Task{linked}), "") + `;
  taskResponse = ` + tasksDocumentJSON(t, []core.Task{existing, created, linked}) + `;
  const filedAgain = new TestElement("a");
  filedAgain.href = "/tasks/new?status=ready";
  await documentEventListeners.click({
    target: filedAgain, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  findElement(main, (element) => element.id === "task-title").value = ` + strconv.Quote(second.Title) + `;
  const dependsGroup = findElement(main, (element) => element.textContent === "Depends On").parentElement;
  const combobox = findElement(dependsGroup, (element) => element.attributes.role === "combobox");
  combobox.value = ` + strconv.Quote(existing.Title) + `;
  combobox.eventListeners.input();
  findElement(dependsGroup, (element) => element.attributes.role === "option" &&
    element.dataset.candidateId === ` + strconv.Quote(existing.ID) + `).eventListeners.click();
  await findElement(dependsGroup, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Add dependency").eventListeners.click();
  await findElement(main, (element) => element.tagName === "FORM").eventListeners.submit({ preventDefault() {} });
  if (createCalls !== 2) throw new Error("the second Save created " + createCalls + " tasks in total");
  if (historyPaths.at(-1) !== "/?q=sweep") {
    throw new Error("a create that waited for the server landed on " +
      JSON.stringify(historyPaths.at(-1)) + ", want the filtered board");
  }
  if (filterSearch.value !== "sweep") throw new Error("that create landed on a board whose search box is empty");
}, 0);
`
	runBoardFiltersClient(t, "a save landing on the filtered board", program)
}

// Back is the board the reader came from, on every page that offers it. A reader
// who narrowed the board down, opened a task or the configuration page and then
// changed their mind should land where they left rather than on a board they never
// asked for — the same rule Save now follows, applied to the control that means
// "never mind".
func TestHandlerClientBackFromAPageReturnsToTheFilteredBoard(t *testing.T) {
	existing := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityHigh)
	script := boardFiltersClientScript(t)
	const backLinkHelper = `
// Every page that offers a way back writes it the same way: an anchor of class
// board-link reading "Back". A test asks for it the way a reader finds it.
function backLink() {
  return findElement(main, (element) =>
    element.tagName === "A" && element.className === "board-link" && element.textContent === "Back");
}
function visit(href) {
  const link = new TestElement("a");
  link.href = href;
  return documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
}
`

	program := clientDOMHarness("/?q=sweep&priority=high", tasksDocumentJSON(t, []core.Task{existing})) + script + backLinkHelper + `
setTimeout(async () => {
  await intervalCallback();
  const want = "/?q=sweep&priority=high";

  await visit("/tasks/" + encodeURIComponent(` + strconv.Quote(existing.ID) + `));
  const fromDetail = backLink();
  if (!fromDetail) throw new Error("the task's own page offers no way back to the board");
  if (fromDetail.href !== want) {
    throw new Error("Back from a task points at " + JSON.stringify(fromDetail.href) + ", want " + JSON.stringify(want));
  }
  // And it is still that board after the poll redraws the route, because the
  // link is written by the render rather than once.
  await intervalCallback();
  if (backLink().href !== want) {
    throw new Error("a poll rewrote Back as " + JSON.stringify(backLink().href));
  }

  await visit(window.location.origin + "/config");
  const fromConfig = backLink();
  if (!fromConfig) throw new Error("the configuration page offers no way back to the board");
  if (fromConfig.href !== want) {
    throw new Error("Back from the configuration page points at " +
      JSON.stringify(fromConfig.href) + ", want " + JSON.stringify(want));
  }

  // The third Back link is the one a route message carries, which is the only
  // thing a reader who mistyped an address has to act on.
  await visit(window.location.origin + "/nowhere");
  const fromMessage = backLink();
  if (!fromMessage) throw new Error("the not-found message offers no way back to the board");
  if (fromMessage.href !== want) {
    throw new Error("Back from a route message points at " +
      JSON.stringify(fromMessage.href) + ", want " + JSON.stringify(want));
  }
}, 0);
`
	runBoardFiltersClient(t, "Back from a page over a filtered board", program)

	// A page opened by its own address is a page with no board behind it: nothing
	// has read a board address, so the filters are the empty ones the client
	// starts with and Back is the bare board — which is what it has always been.
	direct := clientDOMHarness("/tasks/"+existing.ID, tasksDocumentJSON(t, []core.Task{existing})) + script + backLinkHelper + `
setTimeout(async () => {
  await intervalCallback();
  if (!backLink()) throw new Error("a task page loaded by its own address offers no way back");
  if (backLink().href !== "/") {
    throw new Error("Back from a directly loaded task points at " + JSON.stringify(backLink().href) + ", want \"/\"");
  }
  await visit(window.location.origin + "/config");
  if (backLink().href !== "/") {
    throw new Error("Back from a directly loaded configuration page points at " + JSON.stringify(backLink().href));
  }
}, 0);
`
	runBoardFiltersClient(t, "Back from a page nobody reached through a board", direct)
}

// A project with one key is offered no Key chooser — there is nothing to choose
// between — but a ticked key overrides that. /?key=ZZ on a one-key project is an
// address someone can reach (a typo, a link from a project that has since dropped
// a key), and hiding the chooser there hid the only row that said what was
// emptying the board: the union in chooserOptions built the row and the hidden
// chooser then made it unreachable.
func TestHandlerClientKeyChooserStaysVisibleWhileAKeyIsTicked(t *testing.T) {
	only := clientPlacementTask("WB-01J0000000000000000000FF21", "Audit the ledger", core.StatusReady, core.PriorityHigh)
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?key=ZZ", tasksDocumentJSON(t, []core.Task{only})) +
		keyChooserPrelude(t, oneKeyProject()) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = filterChooser("key");
  if (chooser.root.hidden) {
    throw new Error("the Key chooser is hidden while a key is ticked, so the filter cannot be reached");
  }
  const row = chooserRow("key", "ZZ");
  if (!row.checked) throw new Error("the key the address is filtering by is listed unticked");
  if (row.name !== "ZZ") {
    throw new Error("the unknown key is listed as " + JSON.stringify(row.name) + ", want the token the address spells");
  }
  if (row.count !== "0") {
    throw new Error("the unknown key counts " + JSON.stringify(row.count) + ", want \"0\"");
  }
  if (chooser.badge.hidden || chooser.count.textContent !== "1") {
    throw new Error("the badge does not count the key the address carries: hidden=" +
      chooser.badge.hidden + " count=" + JSON.stringify(chooser.count.textContent));
  }
  if (boardCard(` + strconv.Quote(only.ID) + `)) {
    throw new Error("a key nothing was minted under did not narrow the board");
  }

  // Unticking it is what the row is for, and with nothing ticked the project is
  // back to having nothing to choose between.
  tickOption("key", "ZZ");
  if (historyPaths.at(-1) !== "/") {
    throw new Error("unticking the key wrote " + JSON.stringify(historyPaths.at(-1)));
  }
  if (!filterChooser("key").root.hidden) {
    throw new Error("a one-key project with nothing ticked is still offered a choice of keys");
  }
  if (!boardCard(` + strconv.Quote(only.ID) + `)) {
    throw new Error("clearing the key did not bring the board back");
  }
}, 0);
`
	runBoardFiltersClient(t, "the key chooser with an unknown key ticked", program)
}

// boardLabelMenuTasks is three Ready tasks carrying six labels between them,
// which is what the box inside the Labels menu is for: nobody declares a label,
// so that list is as long as a project's cards make it and the other two menus
// never are. Three of the six contain "au" and three do not, and one of the three
// spells it with a capital, so a narrowing that folded nothing would be caught.
func boardLabelMenuTasks() []core.Task {
	audit := clientPlacementTask(filterAuditID, "Audit the ledger", core.StatusReady, core.PriorityHigh)
	audit.Labels = []string{"Audit", "author"}
	perf := clientPlacementTask(filterPerfID, "Perf sweep", core.StatusReady, core.PriorityHigh)
	perf.Rank = "2/1"
	perf.Labels = []string{"perf", "gauge"}
	queue := clientPlacementTask(filterQueueID, "Rebuild the queue", core.StatusReady, core.PriorityLow)
	queue.Rank = "3/1"
	queue.Labels = []string{"queue", "release"}
	return []core.Task{audit, perf, queue}
}

// The whole list of labels this board carries, in the order the menu lists them,
// which is the reading every claim about a narrowing is made against.
const boardLabelMenuWholeList = "Audit, author, gauge, perf, queue, release"

// The box at the top of the Labels menu narrows its rows as the reader types. It
// is menu state and not board state: the caret lands in it when the menu opens,
// the narrowing is case-insensitive on the same rule the board's own search uses,
// and nothing about it reaches the address — a filtered board is a link, and what
// somebody typed to find a label in a menu is no part of the board they found.
func TestHandlerClientLabelMenuNarrowsItsRowsAsTheReaderTypes(t *testing.T) {
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, boardLabelMenuTasks())) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  // Only the Labels menu has a box. The other two list a vocabulary the project
  // states, which is short by construction, and a box over three priorities is a
  // control asking to be used instead of read.
  ["priority", "key"].forEach((group) => {
    if (filterChooser(group).search) throw new Error("the " + group + " menu carries a search box of its own");
  });
  const chooser = openChooserMenu("label");
  if (!chooser.search) throw new Error("the Labels menu carries no search box");
  // Opened with the caret in it, so a reader who came to find one label out of
  // six types rather than reads.
  if (document.activeElement !== chooser.search) {
    throw new Error("opening the Labels menu left the caret out of its box");
  }
  if (visibleChooserNames("label").join(", ") !== ` + strconv.Quote(boardLabelMenuWholeList) + `) {
    throw new Error("the menu opened narrowed: " + visibleChooserNames("label").join(", "));
  }
  // Where the address stands before anything is typed. Nothing below may move it.
  const pushed = historyPaths.length;
  const replaced = historyReplacements.length;

  typeIntoChooserSearch("label", "au");
  if (visibleChooserNames("label").join(", ") !== "Audit, author, gauge") {
    throw new Error("typing au left " + visibleChooserNames("label").join(", "));
  }
  // Hidden, not removed: the list is the whole list whatever is typed, so the
  // keyed rebuild the poll relies on has nothing to do with the narrowing.
  if (chooserRows("label").length !== 6) {
    throw new Error("the narrowing removed rows rather than hiding them: " + chooserRows("label").length + " left");
  }
  if (!chooser.noMatch.hidden) throw new Error("a search that matched three rows says it matched none");

  typeIntoChooserSearch("label", "zzz");
  if (visibleChooserNames("label").length !== 0) {
    throw new Error("zzz left " + visibleChooserNames("label").join(", ") + " showing");
  }
  // A menu with every row hidden says which of the two empty menus it is: a
  // search that matched nothing, not a board carrying no labels.
  if (chooser.noMatch.hidden) throw new Error("a search that matched nothing left the menu blank");
  if (chooser.noMatch.textContent !== "No labels match") {
    throw new Error("the line reads " + JSON.stringify(chooser.noMatch.textContent));
  }

  typeIntoChooserSearch("label", "");
  if (visibleChooserNames("label").join(", ") !== ` + strconv.Quote(boardLabelMenuWholeList) + `) {
    throw new Error("clearing the box left " + visibleChooserNames("label").join(", "));
  }
  if (!chooser.noMatch.hidden) throw new Error("an empty box left the no-match line standing");

  // The whole of the claim that this is menu state: three narrowings and a
  // clearing, and the address is where it was.
  if (historyPaths.length !== pushed || historyReplacements.length !== replaced) {
    throw new Error("typing into a menu wrote the address: " +
      JSON.stringify(historyPaths) + " " + JSON.stringify(historyReplacements));
  }
}, 0);
`
	runBoardFiltersClient(t, "the Labels menu's own search", program)
}

// A ticked row is shown however the box is narrowed. A filter can always be unset
// from where it was set: a reader who types something the ticked label does not
// match must not be left with a badge saying "1 selected", empty columns, and no
// row to untick — which is the same defect the union in chooserOptions exists to
// prevent, arriving by another road.
func TestHandlerClientLabelMenuAlwaysShowsATickedRow(t *testing.T) {
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/?label=perf", tasksDocumentJSON(t, boardLabelMenuTasks())) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = openChooserMenu("label");
  typeIntoChooserSearch("label", "zzz");
  const perf = chooserRow("label", "perf");
  if (perf.hidden) throw new Error("a search nothing matches hid the row the reader had ticked");
  if (!perf.checked) throw new Error("the ticked row came back unticked");
  if (visibleChooserNames("label").join(", ") !== "perf") {
    throw new Error("the narrowed menu shows " + visibleChooserNames("label").join(", "));
  }
  // A row is showing, so the menu has matched something to offer: the no-match
  // line would be arguing with the row above it.
  if (!chooser.noMatch.hidden) {
    throw new Error("the menu says it matched nothing over a row it is still showing");
  }
}, 0);
`
	runBoardFiltersClient(t, "a ticked row under a narrowing", program)
}

// A menu that closes forgets what was typed into it, by every path a menu closes
// by. Reopening the Labels menu offers the labels the board has rather than the
// three that a search the reader has since forgotten left standing.
func TestHandlerClientLabelMenuSearchClearsWhenTheMenuCloses(t *testing.T) {
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, boardLabelMenuTasks())) + script + boardFilterChooserHelpers + `
function assertMenuForgot(path) {
  const chooser = filterChooser("label");
  if (chooser.search.value !== "") {
    throw new Error("closing by " + path + " left " + JSON.stringify(chooser.search.value) + " in the box");
  }
  const narrowed = chooserRows("label").filter((row) => row.hidden).map((row) => row.name);
  if (narrowed.length !== 0) {
    throw new Error("closing by " + path + " left rows narrowed away: " + JSON.stringify(narrowed));
  }
  if (!chooser.noMatch.hidden) {
    throw new Error("closing by " + path + " left the no-match line standing");
  }
}
setTimeout(async () => {
  await intervalCallback();

  // The button that opened it, clicked again.
  openChooserMenu("label");
  typeIntoChooserSearch("label", "au");
  openChooserMenu("label");
  if (chooserIsOpen("label")) throw new Error("a second click on the button left the menu open");
  assertMenuForgot("the button");

  // A click on the board, which is how a reader who has changed their mind
  // dismisses any menu on this page.
  openChooserMenu("label");
  typeIntoChooserSearch("label", "au");
  await clickOutsideChoosers(main);
  if (chooserIsOpen("label")) throw new Error("a click on the board left the menu open");
  assertMenuForgot("a click outside");

  // A route that draws no columns. The row goes with the board and the menu goes
  // with the row, so what was typed into it goes too — otherwise Back brings the
  // board back with a narrowing nobody asked for standing in a menu.
  openChooserMenu("label");
  typeIntoChooserSearch("label", "au");
  returnTo("/tasks/" + ` + strconv.Quote(filterAuditID) + `);
  if (chooserIsOpen("label")) throw new Error("leaving the board left the menu open");
  assertMenuForgot("a route change");

  returnTo("/");
  await intervalCallback();
  const chooser = openChooserMenu("label");
  if (chooser.search.value !== "") {
    throw new Error("the board came back with " + JSON.stringify(chooser.search.value) + " in the menu's box");
  }
  if (visibleChooserNames("label").join(", ") !== ` + strconv.Quote(boardLabelMenuWholeList) + `) {
    throw new Error("the reopened menu shows " + visibleChooserNames("label").join(", "));
  }
}, 0);
`
	runBoardFiltersClient(t, "a menu that closes forgetting its search", program)
}

// Escape empties the box first and closes the menu second, which is the order of
// what the reader is undoing: the narrowing they typed, then the menu they opened.
// The box claims the first press and keeps it from the document; the second press
// finds an empty box, is let through, and reaches the handler that closes the menu
// and hands the caret back to the button.
func TestHandlerClientEscapeInTheLabelSearchClearsThenCloses(t *testing.T) {
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, boardLabelMenuTasks())) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = openChooserMenu("label");
  typeIntoChooserSearch("label", "au");
  if (visibleChooserNames("label").length !== 3) {
    throw new Error("the harness could not narrow the menu: " + visibleChooserNames("label").join(", "));
  }

  escapeChooserSearch("label");
  if (chooser.search.value !== "") {
    throw new Error("the first Escape left " + JSON.stringify(chooser.search.value) + " in the box");
  }
  if (!chooserIsOpen("label")) {
    throw new Error("the first Escape closed the menu as well as emptying the box");
  }
  if (visibleChooserNames("label").join(", ") !== ` + strconv.Quote(boardLabelMenuWholeList) + `) {
    throw new Error("the first Escape emptied the box and left the rows narrowed: " +
      visibleChooserNames("label").join(", "));
  }

  escapeChooserSearch("label");
  if (chooserIsOpen("label")) throw new Error("the second Escape left the menu open");
  if (document.activeElement !== chooser.button) {
    throw new Error("the second Escape dropped the caret rather than handing it back to the button");
  }
}, 0);
`
	runBoardFiltersClient(t, "Escape inside the Labels menu's box", program)
}

// The narrowing survives the poll, because it is applied where the rows are drawn
// rather than where the keystroke is heard. A reader who typed three letters and
// paused must not watch the menu fill back up a second later — and the box keeps
// the caret, because it is a sibling of the rows rather than one of them, so the
// render that rebuilds every row does not touch it.
//
// The poll that matters here is the one that changes what the group offers. A poll
// that changes nothing keeps the row nodes and would keep a narrowing written into
// them whatever the render did; a card arriving with a label nobody had rebuilds
// every row, and rows built a moment ago know nothing about what was typed before
// they existed. So this walks both, and says which is which.
func TestHandlerClientLabelMenuKeepsItsNarrowingAcrossAPoll(t *testing.T) {
	arrival := clientPlacementTask(filterBacklogA, "Gate the tokens", core.StatusReady, core.PriorityHigh)
	arrival.Rank = "4/1"
	arrival.Labels = []string{"auth", "stale"}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, boardLabelMenuTasks())) + script + boardFilterChooserHelpers + `
setTimeout(async () => {
  await intervalCallback();
  const chooser = openChooserMenu("label");
  typeIntoChooserSearch("label", "au");
  const gauge = chooserRow("label", "gauge").label;

  // A poll over the same cards: the rows are the rows, and the narrowing stands.
  await intervalCallback();
  if (chooserRow("label", "gauge").label !== gauge) {
    throw new Error("a poll that changed nothing rebuilt the menu's rows");
  }
  if (visibleChooserNames("label").join(", ") !== "Audit, author, gauge") {
    throw new Error("a poll widened the narrowing: " + visibleChooserNames("label").join(", "));
  }

  // A card arriving with a label nobody had. The option set has changed, so every
  // row is a new node — and the narrowing is the reader's, not the nodes'.
  taskResponse = ` + tasksDocumentJSON(t, append(boardLabelMenuTasks(), arrival)) + `;
  await intervalCallback();
  if (chooserRows("label").length !== 8) {
    throw new Error("the arriving labels left the menu listing " + chooserRows("label").length + ", want 8");
  }
  if (chooserRow("label", "gauge").label === gauge) {
    throw new Error("the changed option set did not rebuild the rows, so this poll is not the one the test is about");
  }
  if (visibleChooserNames("label").join(", ") !== "Audit, auth, author, gauge") {
    throw new Error("the rebuilt rows came back unnarrowed: " + visibleChooserNames("label").join(", "));
  }

  if (chooser.search.value !== "au") {
    throw new Error("the poll wrote the box: " + JSON.stringify(chooser.search.value));
  }
  if (filterChooser("label").search !== chooser.search) {
    throw new Error("the poll replaced the box, so the caret it kept is on a node nobody can see");
  }
  if (document.activeElement !== chooser.search) {
    throw new Error("the poll took the caret out of the box the reader was typing into");
  }
  if (!chooserIsOpen("label")) throw new Error("the poll closed the open menu");
}, 0);
`
	runBoardFiltersClient(t, "a narrowed menu across two polls", program)
}

// The IDs the search's own tests use. They are written out rather than taken from
// boardFilterTasks because those five share their first ten characters, and a
// prefix that names more than one task cannot show that a pasted ID names one.
const (
	filterNotarizeID = "WB-01M2JEC8CBKB5S0THJNSF15WMV"
	filterDrainID    = "WB-01M2QRS1CBKB5S0THJNSF15WMV"
	filterWebhookID  = "WB-01M2TUV2CBKB5S0THJNSF15WMV"
	filterRetiredID  = "ZZ-01M2XYZ3CBKB5S0THJNSF15WMV"
)

// Pasting a task's ID into the search box finds that one card, which is what was
// asked for after the row was first tested. The rest of this is what keeps that
// from being a search which matches too much: a fragment out of the middle of a
// ULID finds nothing, and the key on its own is a word like any other.
func TestHandlerClientSearchFindsATaskByIDPrefix(t *testing.T) {
	notarize := clientPlacementTask(filterNotarizeID, "Notarize macOS builds", core.StatusReady, core.PriorityHigh)
	notarize.Description = "Staple the ticket."
	drain := clientPlacementTask(filterDrainID, "Drain the queue", core.StatusReady, core.PriorityLow)
	drain.Rank = "2/1"
	// The one task whose text holds the key, so that typing the key is answered by
	// a title rather than by every card on the board.
	webhook := clientPlacementTask(filterWebhookID, "Retry the wb webhook", core.StatusReady, core.PriorityLow)
	webhook.Rank = "3/1"
	tasks := []core.Task{notarize, drain, webhook}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) +
		keyChooserPrelude(t, oneKeyProject()) + script + `
const listFor = (status) => boardLists.find((list) => list.dataset.status === status);
const emptyLineIn = (status) => findElement(listFor(status), (element) => hasDataKey(element, "filterEmpty"));
const type = (value) => {
  filterSearch.value = value;
  filterSearch.eventListeners.input({ target: filterSearch });
};
const drawn = () => [` + strconv.Quote(filterNotarizeID) + `, ` + strconv.Quote(filterDrainID) + `, ` +
		strconv.Quote(filterWebhookID) + `].filter((id) => boardCard(id));
setTimeout(async () => {
  await intervalCallback();
  if (drawn().length !== 3) throw new Error("the unfiltered board drew " + drawn().join(", "));

  // Ten characters of the ID, in the case a paste from a terminal or another
  // board may well arrive in.
  type("wb-01m2jec");
  if (drawn().join(", ") !== ` + strconv.Quote(filterNotarizeID) + `) {
    throw new Error("the start of an ID drew " + drawn().join(", ") + ", want only the task it names");
  }

  // The whole of it names the same one task.
  type(` + strconv.Quote(strings.ToLower(filterNotarizeID)) + `);
  if (drawn().join(", ") !== ` + strconv.Quote(filterNotarizeID) + `) {
    throw new Error("a whole pasted ID drew " + drawn().join(", ") + ", want only the task it names");
  }

  // Eight characters out of the middle of that same ULID. An ID is matched from
  // its beginning, so this names nothing at all — and the column says so rather
  // than looking like a column with nothing in it.
  type(` + strconv.Quote(strings.ToLower(filterNotarizeID[8:16])) + `);
  if (drawn().length !== 0) {
    throw new Error("a fragment from inside a ULID drew " + drawn().join(", "));
  }
  const emptied = emptyLineIn("ready");
  if (!emptied || emptied.hidden || emptied.textContent !== "No matches.") {
    throw new Error("the emptied column says " + JSON.stringify(emptied && emptied.textContent));
  }

  // The key alone is a word. It finds the card whose title says it and leaves the
  // other two, rather than every card the project has ever minted.
  type("WB");
  if (drawn().join(", ") !== ` + strconv.Quote(filterWebhookID) + `) {
    throw new Error("the key alone drew " + drawn().join(", ") + ", want only the card whose text holds it");
  }
  // And a key with a dash after it is no more of a prefix than the key was.
  type("WB-");
  if (drawn().length !== 0) {
    throw new Error("a key and a dash drew " + drawn().join(", ") + ", want nothing: it names no ID");
  }
}, 0);
`
	runBoardFiltersClient(t, "the search finding a task by the start of its ID", program)
}

// A retired key's tasks are still on the board, so a pasted ID under one still
// finds its card. The keys the search reads are every key the project has, which
// is the same list the Key chooser draws its rows from.
func TestHandlerClientSearchFindsATaskUnderARetiredKeyByIDPrefix(t *testing.T) {
	notarize := clientPlacementTask(filterNotarizeID, "Notarize macOS builds", core.StatusReady, core.PriorityHigh)
	retired := clientPlacementTask(filterRetiredID, "Drain the queue", core.StatusReady, core.PriorityLow)
	retired.Rank = "2/1"
	tasks := []core.Task{notarize, retired}
	keys, err := core.NewKeySet(core.KeyDocument{
		Keys:    []core.KeyDefinition{{Key: "WB"}, {Key: "ZZ", Retired: true}},
		Current: "WB",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	script := boardFiltersClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, tasks)) +
		keyChooserPrelude(t, keys) + script + `
const type = (value) => {
  filterSearch.value = value;
  filterSearch.eventListeners.input({ target: filterSearch });
};
setTimeout(async () => {
  await intervalCallback();
  type("zz-01");
  if (!boardCard(` + strconv.Quote(filterRetiredID) + `)) {
    throw new Error("the start of an ID under a retired key drew no card, so its tasks cannot be pasted for");
  }
  if (boardCard(` + strconv.Quote(filterNotarizeID) + `)) {
    throw new Error("a prefix under one key drew a card minted under another");
  }
  // The active key's own prefix still answers for its own task, which is what
  // says the retired key was added to the list rather than put in place of it.
  type("wb-01m2");
  if (!boardCard(` + strconv.Quote(filterNotarizeID) + `)) {
    throw new Error("the active key's prefix drew no card");
  }
  if (boardCard(` + strconv.Quote(filterRetiredID) + `)) {
    throw new Error("the active key's prefix drew the retired key's card");
  }
}, 0);
`
	runBoardFiltersClient(t, "the search over a project with a retired key", program)
}
