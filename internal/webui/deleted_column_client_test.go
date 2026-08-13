package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// What the client does with the Deleted column: shows it, hides it, fills it
// from the same poll that fills every other column, and moves cards across its
// edge in both directions through the one optimistic queue every other board
// change already goes through.

// deletedColumnClientPage renders the board and returns its script, which is
// what every test here executes against the fake DOM.
func deletedColumnClientScript(t *testing.T) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	return renderedClientScript(t, response.Body.String())
}

// runDeletedColumnClient executes one client program and returns everything it
// printed, so a test can both assert inside the page and hand a structured
// answer back to Go.
func runDeletedColumnClient(t *testing.T, purpose, program string) string {
	t.Helper()
	node := requireNode(t)
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
	return string(output)
}

// reportedTracks pulls the board-track reports a client program printed. Each
// is the opening tag of every direct child of the board at one moment, which is
// exactly what boardChildren reads off the served page — so the same contract
// function judges both.
func reportedTracks(t *testing.T, output, label string) []string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		prefix := "TRACKS " + label + " "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var children []string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &children); err != nil {
			t.Fatalf("decode %s board tracks: %v", label, err)
		}
		return children
	}
	t.Fatalf("the client printed no board tracks for %s:\n%s", label, output)
	return nil
}

// reportTracks is the client-side half: it prints the board's children as the
// opening tags the served-page helper reads, so Go can judge both with one
// contract.
const reportTracks = `
function reportTracks(label) {
  console.log("TRACKS " + label + " " + JSON.stringify(
    boardElement.children.map((child) => '<section class="' + child.className + '">')));
}
`

// The reader asks for the column and the board grows one; they ask again and it
// goes. Nothing else moves either way — not a column, not a card — because a
// card node is where their work in flight lives, and a toggle that rebuilt the
// board to add one column would destroy all of it.
func TestHandlerClientTogglesTheDeletedColumnWithoutTouchingTheBoard(t *testing.T) {
	tasks := deletedColumnTasks()
	active := []core.Task{tasks[0]}
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, active)) + script + reportTracks + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await intervalCallback();
  if (deletedColumn()) throw new Error("the board drew the Deleted column before anyone asked for it");
  if (deletedToggle.hidden) throw new Error("the board did not reveal the Deleted column's toggle");
  if (deletedToggle.textContent !== "Deleted tasks: hidden" || deletedToggle.href !== "/?deleted=1") {
    throw new Error("the toggle does not offer the column: " + deletedToggle.textContent + " " + deletedToggle.href);
  }
  reportTracks("hidden");

  // Everything the toggle must not touch, held by identity.
  const columns = [...boardElement.children];
  const held = boardLists.map((list) => list.querySelectorAll(".task-card"));
  const activeCard = boardCard(` + strconv.Quote(tasks[0].ID) + `);
  if (!activeCard) throw new Error("the board drew no card for the active task");

  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  if (historyPaths.length !== 1 || historyPaths[0] !== "/?deleted=1") {
    throw new Error("the toggle navigated to " + JSON.stringify(historyPaths));
  }
  const column = deletedColumn();
  if (!column) throw new Error("asking for the column drew none");
  if (boardElement.children[boardElement.children.length - 1] !== column) {
    throw new Error("the Deleted column is not the board's last track");
  }
  columns.forEach((node, index) => {
    if (boardElement.children[index] !== node) throw new Error("showing the column replaced board track " + index);
  });
  boardLists.forEach((list, index) => {
    const after = list.querySelectorAll(".task-card");
    if (after.length !== held[index].length) throw new Error("showing the column changed a column's card count");
    after.forEach((node, at) => {
      if (node !== held[index][at]) throw new Error("showing the column replaced a card node");
    });
  });
  if (deletedToggle.textContent !== "Deleted tasks: shown" || deletedToggle.href !== "/") {
    throw new Error("the toggle does not offer to hide the column: " + deletedToggle.textContent + " " + deletedToggle.href);
  }
  reportTracks("shown");

  // The column is fed by the same poll as every other column.
  await intervalCallback();
  const drawn = deletedCards().map((card) => card.dataset.taskId);
  // Most recently deleted first.
  const wantOrder = [` + strconv.Quote(tasks[2].ID) + `, ` + strconv.Quote(tasks[1].ID) + `];
  if (JSON.stringify(drawn) !== JSON.stringify(wantOrder)) {
    throw new Error("the Deleted column drew " + JSON.stringify(drawn) + ", want " + JSON.stringify(wantOrder));
  }
  const count = findElement(column, (element) => hasDataKey(element, "deletedCount"));
  if (!count || count.textContent !== "2") throw new Error("the Deleted column counts " + (count && count.textContent));
  // The heading's text is also its accessible name, and a status column's is
  // "Ready 3" rather than "Ready3" because the server writes a space between the
  // label and the count. This one has to read the same way.
  const heading = findElement(column, (element) => element.className === "column__heading");
  if (!heading || heading.textContent !== "Deleted 2") {
    throw new Error("the Deleted column's heading reads " + JSON.stringify(heading && heading.textContent) + ", want \"Deleted 2\"");
  }
  const empty = findElement(column, (element) => hasDataKey(element, "deletedEmpty"));
  if (!empty || !empty.hidden) throw new Error("a column with cards in it still says it is empty");
  if (empty.textContent !== "No deleted tasks.") throw new Error("the empty state reads " + empty.textContent);
  // A deleted card offers Restore and no detail link; an active one is the
  // other way round.
  const deletedCard = boardCard(` + strconv.Quote(tasks[2].ID) + `);
  const restore = findElement(deletedCard, (element) => hasDataKey(element, "restoreTask"));
  if (!restore || restore.hidden) throw new Error("a deleted card offers no Restore control");
  if (findElement(deletedCard, (element) => element.tagName === "A" && element.href)) {
    throw new Error("a deleted card links to a detail page that would refuse every control on it");
  }
  const activeRestore = findElement(activeCard, (element) => hasDataKey(element, "restoreTask"));
  if (!activeRestore || !activeRestore.hidden) throw new Error("an active card offers Restore");

  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  if (historyPaths.length !== 2 || historyPaths[1] !== "/") {
    throw new Error("hiding the column navigated to " + JSON.stringify(historyPaths));
  }
  if (deletedColumn()) throw new Error("hiding the column left it on the board");
  columns.forEach((node, index) => {
    if (boardElement.children[index] !== node) throw new Error("hiding the column replaced board track " + index);
  });
  if (boardCard(` + strconv.Quote(tasks[0].ID) + `) !== activeCard) throw new Error("hiding the column rebuilt an active card");
  reportTracks("hidden-again");

  // Back returns to the address that shows it, and the client renders from the
  // popstate exactly as it does from a click.
  returnTo("/?deleted=1");
  if (!deletedColumn()) throw new Error("Back did not restore the column");
  returnTo("/");
  if (deletedColumn()) throw new Error("Back did not take the column away again");
}, 0);
`
	output := runDeletedColumnClient(t, "deleted column toggle behavior", program)
	assertBoardTracks(t, "the client with the column hidden", reportedTracks(t, output, "hidden"), len(core.LegacyVocabulary().Definitions()), false)
	assertBoardTracks(t, "the client with the column shown", reportedTracks(t, output, "shown"), len(core.LegacyVocabulary().Definitions()), true)
	assertBoardTracks(t, "the client after hiding the column", reportedTracks(t, output, "hidden-again"), len(core.LegacyVocabulary().Definitions()), false)
}

// A hard load of the address that shows the column shows it, and the first poll
// the page makes is already the one that asks for the deleted tasks.
func TestHandlerClientOpensTheDeletedColumnOnAHardLoad(t *testing.T) {
	tasks := deletedColumnTasks()
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  const column = deletedColumn();
  if (!column) throw new Error("a hard load of the deleted address drew no column");
  const empty = findElement(column, (element) => hasDataKey(element, "deletedEmpty"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const reads = fetchCalls.filter(({ url }) => url.startsWith("/api/tasks") && !url.includes("/restore"));
  if (reads.length === 0) throw new Error("the page made no read at all");
  for (const { url } of reads) {
    if (url !== "/api/tasks?deleted=include") {
      throw new Error("a poll for a board showing the column asked " + url);
    }
  }
  if (deletedCards().length !== 2) throw new Error("the first poll did not fill the column");

  // Emptied by the server, the column says so — once a poll that asked has
  // landed.
  includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{tasks[0]}) + `;
  await intervalCallback();
  if (deletedCards().length !== 0) throw new Error("the column kept cards the server no longer holds");
  if (empty.hidden) throw new Error("an empty column says nothing");
}, 0);
`
	runDeletedColumnClient(t, "deleted column hard load behavior", program)
}

// "No deleted tasks." is a claim about what the server holds, and a column shown
// over a model that was read without them has not asked yet. The gap is real and
// it is a whole render wide: showing the column draws it immediately, from the
// tasks the last active-only poll left, which name no tombstones at all — so a
// column that spoke from that would say it was empty and then fill a moment
// later, over a board that never was.
//
// This is the test the hard-load one above cannot be: there, nothing has loaded
// when the column appears, so the renderer has not run and the empty state is
// still hidden from construction whatever it would have decided.
func TestHandlerClientWithholdsTheEmptyStateUntilAPollHasAsked(t *testing.T) {
	tasks := deletedColumnTasks()
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  // A board that has read the server once, without the deleted tasks.
  await intervalCallback();
  // The card proves the board loaded, which is what makes the render below run
  // at all: a toggle over an unloaded board draws nothing and decides nothing.
  if (!boardCard(` + strconv.Quote(tasks[0].ID) + `)) {
    throw new Error("the board never loaded, so the render under test never runs");
  }

  // The read the toggle starts is held open, so what the column says is what it
  // says before any answer about the deleted tasks has arrived.
  let releaseInclude = null;
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if (url === "/api/tasks?deleted=include") {
      await new Promise((resolve) => { releaseInclude = resolve; });
    }
    return boardFetch(url, options);
  };

  // Not awaited: what is under test is the frame the toggle paints, and the
  // click's own promise does not settle until the held read does.
  documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  const column = deletedColumn();
  if (!column) throw new Error("the toggle drew no column");
  const empty = findElement(column, (element) => hasDataKey(element, "deletedEmpty"));
  if (deletedCards().length !== 0) {
    throw new Error("the column already held cards, so it had nothing to be silent about");
  }
  if (!empty.hidden) {
    throw new Error("the column said it was empty before a poll had asked for the deleted tasks");
  }

  // The answer arrives and it holds two. Had the column spoken a moment ago it
  // would have been wrong about a board it had not read.
  releaseInclude();
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (deletedCards().length !== 2) throw new Error("the held read did not fill the column");
  if (!empty.hidden) throw new Error("a column with cards in it still says it is empty");

  // And once a poll that asked has answered with none, it says so.
  includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{tasks[0]}) + `;
  await intervalCallback();
  if (deletedCards().length !== 0) throw new Error("the column kept cards the server no longer holds");
  if (empty.hidden) throw new Error("a column the server says is empty says nothing");
}, 0);
`
	runDeletedColumnClient(t, "deleted column empty-state timing", program)
}

// A tombstone is drawn in the Deleted column or it is drawn nowhere. It is
// never drawn in a status column, and the moment that could happen is the one
// right after the reader hides the column: the model still holds every deleted
// task the last poll read, and each of them is filed under a status a column
// does name. A renderer that only asked "is there a Deleted column to put this
// in" and then fell through to the status grouping would file them all among
// the live work, and the next poll — up to a second later — would take them
// away again.
func TestHandlerClientDrawsNoTombstoneInAStatusColumnWhileTheColumnIsHidden(t *testing.T) {
	tasks := deletedColumnTasks()
	script := deletedColumnClientScript(t)
	// Both tombstones are filed under a status this board draws a column for, so
	// a fall-through has somewhere to land. Without that the test could pass over
	// a renderer that strands them instead.
	for _, task := range tasks[1:] {
		if task.Status != core.StatusReady {
			t.Fatalf("fixture tombstone %s is filed under %q, which the test needs to be a real column", task.ID, task.Status)
		}
	}

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
const tombstones = [` + strconv.Quote(tasks[1].ID) + `, ` + strconv.Quote(tasks[2].ID) + `];
// Every task ID drawn in a column the board takes drops into, which is the set a
// tombstone must never appear in.
function idsInStatusColumns() {
  return boardLists.flatMap((list) => list.querySelectorAll(".task-card").map((card) => card.dataset.taskId));
}
function assertNoTombstonesAreFiled(when) {
  const filed = idsInStatusColumns().filter((id) => tombstones.includes(id));
  if (filed.length !== 0) throw new Error("a tombstone was drawn in a status column " + when + ": " + JSON.stringify(filed));
  const stranded = boardUnknownList.querySelectorAll(".task-card")
    .map((card) => card.dataset.taskId).filter((id) => tombstones.includes(id));
  if (stranded.length !== 0) throw new Error("a tombstone was drawn in the unknown-status region " + when + ": " + JSON.stringify(stranded));
}
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (deletedCards().length !== 2) throw new Error("the column did not fill, so there is nothing to hide");
  assertNoTombstonesAreFiled("while the column was shown");

  // Hidden, with the model still holding both tombstones: the poll that stops
  // asking for them has not run yet, and this is the frame that matters.
  await documentEventListeners.click({ target: deletedToggle, button: 0, preventDefault() {} });
  if (deletedColumn()) throw new Error("the toggle left the column on the board");
  assertNoTombstonesAreFiled("in the frame after the column was hidden");
  tombstones.forEach((id) => {
    if (boardCard(id)) throw new Error("a hidden tombstone is still drawn somewhere: " + id);
  });

  // And still not, once the poll that no longer asks has replaced the model.
  await intervalCallback();
  assertNoTombstonesAreFiled("after a poll that did not ask for them");
}, 0);
`
	runDeletedColumnClient(t, "hidden tombstone rendering", program)
}

// Restore on a card is a queued intent like every other board change: the card
// leaves the column before the response arrives, the write carries the head the
// card was drawn from, and the request names no destination — a bare restore
// puts the task back where it was deleted from.
func TestHandlerClientRestoresADeletedTaskFromItsCard(t *testing.T) {
	tasks := deletedColumnTasks()
	restored := tasks[2]
	restored.Deleted = false
	restored.Head = "head-restored"
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const card = boardCard(` + strconv.Quote(restored.ID) + `);
  if (!card) throw new Error("the column drew no card to restore");
  const restore = findElement(card, (element) => hasDataKey(element, "restoreTask"));

  let release;
  const writes = [];
  const boardFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      writes.push({ url, method: options.method, body: options.body === undefined ? null : JSON.parse(options.body) });
      return new Promise((resolve) => {
        release = () => resolve({ ok: true, json: async () => ({
          format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, restored)) + ` }) });
      });
    }
    return boardFetch(url, options);
  };

  restore.eventListeners.click();
  await Promise.resolve();
  // The card is out of the column and in the one its status names, before the
  // response has arrived.
  if (deletedCards().some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("the restored card stayed in the Deleted column while the write was open");
  }
  const ready = boardLists.find((list) => list.dataset.status === ` + strconv.Quote(string(restored.Status)) + `);
  if (!ready.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("the restored card did not land in the column its status names");
  }
  // A poll landing mid-write must not take the optimistic paint away.
  await intervalCallback();
  if (!ready.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("a poll reverted the optimistic restore while the write was open");
  }

  if (writes.length !== 1) throw new Error("restore sent " + writes.length + " writes");
  const sent = writes[0];
  if (sent.method !== "POST" || sent.url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(restored.ID) + `) + "/restore") {
    throw new Error("restore sent " + sent.method + " " + sent.url);
  }
  if (JSON.stringify(sent.body) !== JSON.stringify({ expectedHead: ` + strconv.Quote(tasks[2].Head) + ` })) {
    throw new Error("restore named a destination or the wrong head: " + JSON.stringify(sent.body));
  }
  release();
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (!ready.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("the confirmed card did not stay where the restore put it");
  }
}, 0);
`
	runDeletedColumnClient(t, "deleted card restore behavior", program)
}

// deletedDropTasks is a board with two active cards in one column and one
// deleted task, which is the smallest board a drop position can be read from.
func deletedDropTasks() []core.Task {
	stamp := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	base := core.Task{
		ProjectID: "01J00000000000000000000000",
		TaskData: core.TaskData{
			Status:    core.StatusInProgress,
			Priority:  core.PriorityMedium,
			Rank:      "1/1",
			Labels:    []string{},
			CreatedAt: stamp,
			UpdatedAt: stamp,
		},
	}
	first := base
	first.ID = "WB-01J00000000000000000000201"
	first.Title = "First in progress"
	first.Head = "head-first"
	second := base
	second.ID = "WB-01J00000000000000000000202"
	second.Title = "Second in progress"
	second.Head = "head-second"
	removed := base
	removed.ID = "WB-01J00000000000000000000203"
	removed.Title = "Tombstoned"
	removed.Status = core.StatusReady
	removed.Deleted = true
	removed.UpdatedAt = stamp.Add(time.Hour)
	removed.Head = "head-removed"
	return []core.Task{first, second, removed}
}

// Dragging a deleted card onto a column is one queued restore that names where
// it lands: the same drop position the board already turns into a before/after,
// carried on the restore so the return and the placement are one operation pack
// rather than a task briefly visible in the column it was deleted from.
func TestHandlerClientRestoresACardIntoTheColumnItIsDroppedOn(t *testing.T) {
	tasks := deletedDropTasks()
	active := []core.Task{tasks[0], tasks[1]}
	restored := tasks[2]
	restored.Deleted = false
	restored.Status = core.StatusInProgress
	restored.Head = "head-restored"
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, active)) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const cards = inProgress.querySelectorAll(".task-card");
  if (cards.length !== 2) throw new Error("the destination column drew " + cards.length + " cards");
  cards[0].rect = { top: 0, bottom: 80 };
  cards[1].rect = { top: 80, bottom: 160 };
  const card = boardCard(` + strconv.Quote(restored.ID) + `);
  if (!card) throw new Error("the Deleted column drew no card to drag");

  const writes = [];
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      writes.push({ url, method: options.method, body: JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, restored)) + ` }) };
    }
    return { ok: true, json: async () => includedTaskResponse };
  };

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  // Dropped above the second card, which is the position the write has to name.
  const dropped = documentEventListeners.drop({ target: inProgress, clientY: 100, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  if (deletedCards().some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("the dragged card stayed in the Deleted column");
  }
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(restored.ID) + `)) {
    throw new Error("the dragged card did not land in the column it was dropped on");
  }
  await dropped;
  if (writes.length !== 1) throw new Error("the drop sent " + writes.length + " writes: " + JSON.stringify(writes));
  const sent = writes[0];
  if (sent.method !== "POST" || sent.url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(restored.ID) + `) + "/restore") {
    throw new Error("the drop sent " + sent.method + " " + sent.url);
  }
  const want = { status: "in-progress", before: ` + strconv.Quote(tasks[1].ID) + `, expectedHead: ` + strconv.Quote(tasks[2].Head) + ` };
  if (JSON.stringify(sent.body) !== JSON.stringify(want)) {
    throw new Error("the restore body = " + JSON.stringify(sent.body) + ", want " + JSON.stringify(want));
  }
}, 0);
`
	runDeletedColumnClient(t, "restore into a dropped column behavior", program)
}

// Dragging a live card onto the Deleted column is a queued delete, carrying the
// head the card was drawn from so a task another clone has changed since is
// refused rather than tombstoned out from under them.
func TestHandlerClientDeletesACardDroppedOnTheDeletedColumn(t *testing.T) {
	tasks := deletedDropTasks()
	active := []core.Task{tasks[0], tasks[1]}
	removed := tasks[0]
	removed.Deleted = true
	removed.Head = "head-deleted"
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, active)) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const card = boardCard(` + strconv.Quote(tasks[0].ID) + `);
  const list = deletedList();
  if (!card || !list) throw new Error("the board drew no card to delete or no column to drop it on");

  const writes = [];
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      writes.push({ url, method: options.method, body: options.body === undefined ? null : JSON.parse(options.body) });
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: ` + string(mustJSON(t, removed)) + ` }) };
    }
    return { ok: true, json: async () => includedTaskResponse };
  };

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  // The column takes the drop and draws no placement marker: it orders itself
  // by when each task was deleted, so a line between two cards would promise a
  // position it does not keep.
  let prevented = false;
  documentEventListeners.dragover({ target: list, clientY: 1, dataTransfer, preventDefault() { prevented = true; } });
  if (!prevented) throw new Error("the Deleted column refused the drag");
  if (findElement(list, (element) => element.className === "drop-marker")) {
    throw new Error("the Deleted column drew a placement marker for a drop that names no position");
  }
  const dropped = documentEventListeners.drop({ target: list, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  if (!deletedCards().some((item) => item.dataset.taskId === ` + strconv.Quote(tasks[0].ID) + `)) {
    throw new Error("the deleted card did not move into the Deleted column before the response arrived");
  }
  await dropped;
  if (writes.length !== 1) throw new Error("the drop sent " + writes.length + " writes: " + JSON.stringify(writes));
  const sent = writes[0];
  if (sent.method !== "DELETE" || sent.url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(tasks[0].ID) + `)) {
    throw new Error("the drop sent " + sent.method + " " + sent.url);
  }
  if (JSON.stringify(sent.body) !== JSON.stringify({ expectedHead: ` + strconv.Quote(tasks[0].Head) + ` })) {
    throw new Error("the delete body = " + JSON.stringify(sent.body));
  }

  // A card already in the column cannot be deleted again.
  const settled = boardCard(` + strconv.Quote(tasks[0].ID) + `);
  documentEventListeners.dragstart({ target: settled, dataTransfer });
  let secondPrevented = false;
  documentEventListeners.dragover({ target: list, clientY: 1, dataTransfer, preventDefault() { secondPrevented = true; } });
  if (secondPrevented) throw new Error("the column accepted a card it is already holding");
  await documentEventListeners.drop({ target: list, clientY: 1, dataTransfer, preventDefault() {} });
  if (writes.length !== 1) throw new Error("re-deleting a tombstone sent a write");
}, 0);
`
	runDeletedColumnClient(t, "drag to delete behavior", program)
}

// Both new intents are ordinary intents when they are refused: a stale head
// forces a refresh, re-bases the queue onto what that refresh read, rolls the
// card back to what the server holds, and reports on the card it concerns.
func TestHandlerClientRebasesARefusedRestoreAndReportsItOnTheCard(t *testing.T) {
	tasks := deletedColumnTasks()
	moved := tasks[2]
	moved.Head = "head-moved"
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const card = boardCard(` + strconv.Quote(tasks[2].ID) + `);
  const restore = findElement(card, (element) => hasDataKey(element, "restoreTask"));

  const heads = [];
  let refuse = true;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      heads.push(JSON.parse(options.body).expectedHead);
      if (refuse) {
        refuse = false;
        // The refusal is what moves the board on, so the refresh it forces
        // reads the newer head.
        includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{tasks[0], tasks[1], moved}) + `;
        return { ok: false, json: async () => ({
          format: "workbook.error", version: 1,
          error: { category: "stale-write", message: "Another clone changed that task." } }) };
      }
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1,
        task: Object.assign({}, ` + string(mustJSON(t, moved)) + `, { deleted: false }) }) };
    }
    return { ok: true, json: async () => includedTaskResponse };
  };

  await restore.eventListeners.click();
  if (heads.length !== 1 || heads[0] !== ` + strconv.Quote(tasks[2].Head) + `) {
    throw new Error("the refused restore carried " + JSON.stringify(heads));
  }
  // Rolled back: the card is in the column again, showing what the server holds.
  if (!deletedCards().some((item) => item.dataset.taskId === ` + strconv.Quote(tasks[2].ID) + `)) {
    throw new Error("a refused restore left the card out of the Deleted column");
  }
  const message = cardFailureMessage(boardCard(` + strconv.Quote(tasks[2].ID) + `));
  if (!message) throw new Error("a refused restore reported nothing on the card");
  if (!message.includes("changed")) throw new Error("the report does not name the conflict: " + message);

  // The queue re-based, so the reader's next attempt goes out against the head
  // the forced refresh read rather than the one that was refused.
  const again = findElement(boardCard(` + strconv.Quote(tasks[2].ID) + `), (element) => hasDataKey(element, "restoreTask"));
  await again.eventListeners.click();
  if (heads.length !== 2 || heads[1] !== ` + strconv.Quote(moved.Head) + `) {
    throw new Error("the retry did not re-base: " + JSON.stringify(heads));
  }
}, 0);
`
	runDeletedColumnClient(t, "refused restore behavior", program)
}

// A refused delete rolls the card back into the column it came from and says so
// where the reader is looking, exactly as a refused placement does.
func TestHandlerClientRollsBackARefusedDeleteOntoItsColumn(t *testing.T) {
	tasks := deletedDropTasks()
	active := []core.Task{tasks[0], tasks[1]}
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, active)) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const card = boardCard(` + strconv.Quote(tasks[0].ID) + `);
  const list = deletedList();
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return { ok: false, json: async () => ({
        format: "workbook.error", version: 1,
        error: { category: "validation", message: "That task cannot be deleted." } }) };
    }
    return { ok: true, json: async () => includedTaskResponse };
  };

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  await documentEventListeners.drop({ target: list, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });

  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  if (!inProgress.querySelectorAll(".task-card").some((item) => item.dataset.taskId === ` + strconv.Quote(tasks[0].ID) + `)) {
    throw new Error("a refused delete left the card in the Deleted column");
  }
  // The same wording every other refused board change carries, because it is
  // the same queue reporting it.
  const message = cardFailureMessage(boardCard(` + strconv.Quote(tasks[0].ID) + `));
  if (message !== "Task update failed. The card shows the version the server holds.") {
    throw new Error("a refused delete reported " + JSON.stringify(message));
  }
}, 0);
`
	runDeletedColumnClient(t, "refused delete behavior", program)
}

// The Restore control is built once with the card and is its last child, which
// is what lets the optional sections be rebuilt around it without ever moving
// it: applyCard inserts each of them before the button rather than appending
// past it. Three comments in the renderer rest on that, so it is asserted here
// against a card that carries all three rebuilt sections — a description, labels
// and dependency progress — before and after a poll that rebuilds every one of
// them.
func TestHandlerClientKeepsTheRestoreControlAsTheCardsLastChild(t *testing.T) {
	task := clientPlacementTask("WB-01J00000000000000000000401", "Rebuilt", core.StatusReady, core.PriorityMedium)
	task.Description = "A description, which applyCard rebuilds on every change."
	task.Labels = []string{"alpha", "beta"}
	task.Head = "head-1"
	renamed := task
	renamed.Title = "Rebuilt and renamed"
	script := deletedColumnClientScript(t)
	// The dependency progress row is drawn from the view rather than the task, so
	// the document is built by hand to carry one.
	viewed := func(tasks []core.Task) string {
		t.Helper()
		return string(mustJSON(t, TasksDocument{
			Format: "workbook.tasks", Version: 1, Tasks: tasks,
			Presentation: []TaskPresentation{{
				TaskID: task.ID, IDPrefix: task.ID,
				DependenciesTotal: 2, DependenciesComplete: 1, WaitingOnDependencies: true,
			}},
		}))
	}

	program := clientDOMHarness("/", viewed([]core.Task{task})) + script + `
setTimeout(async () => {
  await intervalCallback();
  const card = boardCard(` + strconv.Quote(task.ID) + `);
  if (!card) throw new Error("the board drew no card");
  const restore = findElement(card, (element) => hasDataKey(element, "restoreTask"));
  if (!restore) throw new Error("the card carries no Restore control");
  // All three rebuilt sections are present, or the assertion below could hold
  // over a card that has nothing to rebuild around the button.
  const sections = ["P", "labels", "dependencyProgress"].map((marker) => findElement(card, (element) =>
    marker === "P" ? element.tagName === "P" : (element.className === marker || hasDataKey(element, marker))));
  if (sections.some((section) => !section)) {
    throw new Error("the card is missing a rebuilt section, so it cannot witness the order");
  }
  const last = () => card.children[card.children.length - 1];
  if (last() !== restore) {
    throw new Error("the Restore control is not the card's last child: " + last().tagName + "." + last().className);
  }

  // A poll that really does change the task rebuilds every one of those
  // sections. The button must not be pushed off the end by any of them, and it
  // must be the same node it was.
  taskResponse = ` + viewed([]core.Task{renamed}) + `;
  await intervalCallback();
  if (boardCard(` + strconv.Quote(task.ID) + `) !== card) throw new Error("the poll rebuilt the card instead of updating it");
  if (findElement(card, (element) => element.tagName === "H3").textContent !== ` + strconv.Quote(renamed.Title) + `) {
    throw new Error("the poll did not change the card, so nothing was rebuilt around the button");
  }
  if (findElement(card, (element) => hasDataKey(element, "restoreTask")) !== restore) {
    throw new Error("a rebuild replaced the Restore control");
  }
  if (last() !== restore) {
    throw new Error("a rebuilt section was appended past the Restore control: " + last().tagName + "." + last().className);
  }
}, 0);
`
	runDeletedColumnClient(t, "restore control position", program)
}

// A deleted task's text is drawn through the same escaping every other card
// goes through: it is written as text, never as markup, so a title that looks
// like a tag is a title that looks like a tag.
func TestHandlerClientDrawsHostileDeletedTaskTextAsText(t *testing.T) {
	tasks := deletedColumnTasks()
	hostile := tasks[2]
	hostile.Title = `<script>alert("x")</script>`
	hostile.Description = `</div><img src=x onerror="alert(1)">`
	hostile.Labels = []string{`<b>label</b>`}
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, []core.Task{tasks[0], hostile}) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const card = boardCard(` + strconv.Quote(hostile.ID) + `);
  if (!card) throw new Error("the column drew no card for the hostile task");
  const heading = findElement(card, (element) => element.tagName === "H3");
  if (heading.textContent !== ` + strconv.Quote(hostile.Title) + `) {
    throw new Error("the title was not written as text: " + heading.textContent);
  }
  const description = findElement(card, (element) => element.tagName === "P");
  if (description.textContent !== ` + strconv.Quote(hostile.Description) + `) {
    throw new Error("the description was not written as text: " + description.textContent);
  }
  const label = findElement(card, (element) => element.className === "label");
  if (label.textContent !== ` + strconv.Quote(hostile.Labels[0]) + `) {
    throw new Error("the label was not written as text: " + label.textContent);
  }
  // Nothing anywhere under the card was built from markup: every element there
  // was created by the renderer, so a tag in the text has no element to be.
  const forged = findElements(card, (element) => ["SCRIPT", "IMG", "B"].includes(element.tagName));
  if (forged.length !== 0) throw new Error("hostile text became " + forged.length + " elements");
}, 0);
`
	runDeletedColumnClient(t, "hostile deleted task text behavior", program)
}

// The page the deleted tasks used to have is gone on both sides: the server
// answers the address with a 404, and a client that reaches it anyway calls it
// what it is.
func TestHandlerClientAnswersTheRemovedDeletedRouteAsNotFound(t *testing.T) {
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/deleted", tasksDocumentJSON(t, nil)) + script + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (document.title !== "Page not found · Workbook") {
    throw new Error("the removed route rendered as " + JSON.stringify(document.title));
  }
  if (!deletedToggle.hidden) throw new Error("a route that is not the board revealed the column's toggle");
}, 0);
`
	runDeletedColumnClient(t, "removed deleted route behavior", program)
}

// A deleted task has no detail page. The route answers for the tasks the board
// holds and a tombstone cannot be edited, so a form over one would offer a Save
// the server refuses — which is the answer this address gave before the column
// existed, whether or not the board is now carrying the card.
func TestHandlerClientRefusesADetailRouteForADeletedTask(t *testing.T) {
	tasks := deletedColumnTasks()
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, []core.Task{tasks[0]})) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, tasks) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (!boardCard(` + strconv.Quote(tasks[2].ID) + `)) throw new Error("the column drew no card for the deleted task");
  const link = new TestElement("a");
  link.href = window.location.origin + "/tasks/" + encodeURIComponent(` + strconv.Quote(tasks[2].ID) + `);
  await documentEventListeners.click({ target: link, button: 0, preventDefault() {} });
  if (document.title !== "Task not found · Workbook") {
    throw new Error("a deleted task's address rendered as " + JSON.stringify(document.title));
  }
}, 0);
`
	runDeletedColumnClient(t, "deleted task detail route behavior", program)
}
