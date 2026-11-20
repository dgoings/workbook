package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// How one dependency row arranges itself. A relationship row is a two-column
// grid: what there is to read on the left, what there is to press on the right.
// Both of those are built by the client, so the containers are asserted through
// the Node harness; the rules that place them are asserted against the served
// stylesheet, because a fake DOM has no layout engine to read a track width out
// of.
//
// The bug these guard is what happens when the row's parts are loose. The only
// child with a declared column was the Remove button, so every other part was
// dealt into the grid by auto-placement and the arrangement came out of how
// many parts that branch happened to append rather than out of any rule. A row
// with no Remove button left column two — an auto track — unclaimed, so
// row-major auto-placement dealt it the metadata and the note, the track was
// maximized to those sentences before the minmax(0, 1fr) title column was given
// anything, and the title column resolved to 0px. With overflow-wrap: anywhere
// on the heading, the title came out one character per line. That row — a
// deleted task in Blocks — is no longer drawn at all, but a row beside a button
// spilled too once it had a fourth part: a draft whose save was refused dealt
// its error note into column two, so the two items stay.

// runRelationshipRowClient renders the New Task page, executes its client
// script against the fake DOM, and runs body with relationshipRow reachable.
//
// The row builder is a local of the script's IIFE, so the harness hoists it the
// way TestHandlerClientSidebarAccessibilityAndMobileOrder does. Calling it
// directly is the point: a row's shape depends on which branch built it, and
// these shapes are otherwise only reachable by arranging a different
// tombstone-and-draft scenario to get at each one.
func runRelationshipRowClient(t *testing.T, purpose, body string) {
	t.Helper()
	node := requireNode(t)
	tasks := []core.Task{clientPlacementTask("WB-01J0000000000000000000FE01", "Neighboring task", core.StatusReady, core.PriorityMedium)}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	script = strings.Replace(script, "function relationshipRow(", "globalThis.relationshipRow = function relationshipRow(", 1)
	script = strings.Replace(script, "function taskRelationships(", "globalThis.taskRelationships = function taskRelationships(", 1)
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
setTimeout(() => {
  if (typeof relationshipRow !== "function") throw new Error("the client no longer builds relationship rows");
  // The relationship shape the dependency snapshot builds: an id and the task
  // on the other end, or null when no task by that id is known. Every row it
  // builds is removable: Blocks lists only live tasks, and a Depends On edge is
  // recorded on this task whatever became of the other one.
  const liveTask = taskDocument.tasks[0];
  const deletedTask = { ...liveTask, deleted: true };
  const textParts = (row) => findElements(row, (element) =>
    element.tagName === "H4" || element.tagName === "P" || element.tagName === "STRONG");
  const item = (row, name) => findElement(row, (element) => classTokens(element).includes(name));
` + body + `
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// Every row has something to press, and every shape is held to the same two
// items on its first line. A deleted prerequisite adds a badge, which fit in
// column one even before the fix; a failed draft adds a badge, the failure
// text and a Retry button, and those go on a third item that spans the card
// under the other two — the failure text is the part that used to land in
// column two, and Retry beside the × narrowed the title the same way.
func TestHandlerClientKeepsRelationshipControlsOutOfTheTextColumn(t *testing.T) {
	t.Parallel()
	runRelationshipRowClient(t, "relationship row layout", `
  const shapes = [
    ["a plain row", relationshipRow({ id: liveTask.id, task: liveTask }, () => {}), false],
    // A prerequisite that has been deleted. The edge is recorded on this task,
    // so it stays removable even though the task on the other end is gone.
    ["a Depends On row for a deleted prerequisite", relationshipRow({ id: deletedTask.id, task: deletedTask }, () => {}), false],
    // A prerequisite no task answers to at all.
    ["a Depends On row for a missing prerequisite", relationshipRow({ id: "WB-01J0000000000000000000FE02", task: null }, () => {}), false],
    // The second shape that was already broken before anyone reported it: a
    // draft whose save was refused carries a badge and the failure text, and
    // offers Retry as well as Remove.
    ["a draft row carrying a save failure", relationshipRow({ id: liveTask.id, task: liveTask, error: "the save was refused" }, () => {}, true, () => {}), true]
  ];
  shapes.forEach(([label, row, expectRetry]) => {
    const want = expectRetry ? 3 : 2;
    if (row.children.length !== want) {
      throw new Error(label + " deals " + row.children.length + " items into the row's own grid, not " + want);
    }
    const column = item(row, "relationship-row__text");
    const controls = item(row, "relationship-row__controls");
    const failure = item(row, "relationship-row__failure");
    if (!column || !controls) throw new Error(label + " is missing its text column or its controls");
    if (row.children[0] !== column || row.children[1] !== controls) {
      throw new Error(label + " no longer reads left to right as text then controls");
    }
    if (expectRetry !== Boolean(failure) || (failure && row.children[2] !== failure)) {
      throw new Error(label + " does not put its draft state on a line of its own after the controls");
    }
    if (textParts(row).some((part) => !column.contains(part) && !(failure && failure.contains(part)))) {
      throw new Error(label + " leaves a text part loose in the row's grid");
    }
    if (controls.children.length !== 1) {
      throw new Error(label + " draws " + controls.children.length + " controls beside its title, not the × alone");
    }
    // The row keeps its identity through the wrapping: the list finds its rows
    // by this attribute, and the compact class is what makes a row a sidebar
    // row.
    if (row.dataset.relationshipRow === undefined || !row.dataset.relationshipId ||
        !classTokens(row).includes("relationship-row--compact")) {
      throw new Error(label + " lost the row's identity in the wrapping");
    }
    // The buttons are what the group disables while a write is in flight and
    // what every other relationship test presses, so the row still names them.
    if (!row.removeButton || row.removeButton.parentElement !== controls) {
      throw new Error(label + " does not hold its Remove button in the controls");
    }
    // The red × that every sidebar row uses, named for what it removes.
    if (!classTokens(row.removeButton).includes("row-remove") || row.removeButton.type !== "button" ||
        !/^Remove (dependency on|blocked task) /.test(row.removeButton.getAttribute("aria-label") || "") ||
        row.removeButton.title !== row.removeButton.getAttribute("aria-label") ||
        !row.removeButton.eventListeners.click) {
      throw new Error(label + " lost the working Remove button itself");
    }
    if (expectRetry) {
      // "Not saved", then why, then Retry: the order they are read in.
      const state = item(failure, "relationship-state");
      const note = item(failure, "relationship-row__note");
      if (!row.retryButton || row.retryButton.parentElement !== failure) {
        throw new Error(label + " does not hold its Retry button on its failure line");
      }
      if (!state || state.textContent !== "Not saved" || !note || note.textContent !== "the save was refused") {
        throw new Error(label + " does not report the failure on its own line: " + JSON.stringify(failure.textContent));
      }
      const order = failure.children;
      if (!(order.indexOf(state) < order.indexOf(note) && order.indexOf(note) < order.indexOf(row.retryButton))) {
        throw new Error(label + " does not read Not saved, the reason, then Retry");
      }
      if (item(column, "relationship-row__note") || item(column, "relationship-state")) {
        throw new Error(label + " still reports its failure inside the title's column");
      }
    } else {
      if (row.retryButton) throw new Error(label + " grew a Retry button it has nothing to retry");
      // Nothing is refused, so there is nothing to explain. The note a deleted
      // Blocks task used to carry belongs to a row that is no longer drawn.
      if (item(column, "relationship-row__note")) {
        throw new Error(label + " explains a refusal that did not happen: " + JSON.stringify(column.textContent));
      }
    }
  });
  const prerequisite = shapes[1][1];
  const badge = item(prerequisite, "relationship-state");
  if (!badge || badge.textContent !== "Deleted") {
    throw new Error("a deleted prerequisite no longer says it is deleted: " + JSON.stringify(prerequisite.textContent));
  }
`)

	body := newTaskPage(t)
	// What places the structure above. The fix itself is structural — the item
	// count is what the test through the harness pins — so these hold the
	// stack to the line gap every sidebar card uses. min-width: 0 is pinned
	// because it is served, not because it rescues the title: the title
	// column's min sizing function is a fixed 0 already, so no item minimum can
	// reach it. It is there for the day someone writes the template as
	// `1fr auto`, where that minimum would start to count.
	for _, fragment := range []string{
		`.relationship-row__text { display: grid; gap: var(--sb-card-line-gap); min-width: 0; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("relationship row styling does not contain %q", fragment)
		}
	}
	// The span existed to reserve the two rows the loose heading and metadata
	// filled. With the text in one item there is no second row to span, and
	// spanning one anyway leaves a phantom empty grid row under every row. The
	// Remove button places nothing at all now — the controls around it do — so
	// this looks for a grid placement on it rather than for the one rule text
	// it used to carry, which would go stale the moment anyone reformatted it.
	if strings.Contains(body, `.relationship-remove { align-self: center; grid-column: 2; grid-row: 1 / span 2; }`) {
		t.Error("the Remove button still reserves two grid rows of the relationship row")
	}
	// Named properties rather than a bare "grid-", which would both miss the
	// `grid:` shorthand and fire on any unrelated custom property that happens
	// to spell those five characters.
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, ".relationship-remove") {
			continue
		}
		for _, placement := range []string{"grid-column", "grid-row", "grid-area", "grid-template", "grid:"} {
			if strings.Contains(line, placement) {
				t.Errorf("the Remove button still places itself in the relationship row's grid, with %s: %q", placement, strings.TrimSpace(line))
			}
		}
	}
	// Declared rather than emergent: the controls name the column the Remove
	// button used to claim for itself, and sit on the title's line; a draft's
	// state spans both columns on the line under them.
	if !strings.Contains(body, `.relationship-row__controls { display: flex; align-items: center; align-self: start; gap: var(--sb-inline-gap); grid-column: 2; }`) {
		t.Error("relationship row styling does not place the controls in the row's second column")
	}
	if !strings.Contains(body, `.relationship-row__failure { display: grid; grid-column: 1 / -1;`) {
		t.Error("relationship row styling does not give a draft's state the card's whole width")
	}
}

// A dependency list skips a redraw when nothing it draws has changed, so a
// poll leaves its rows — and the × under a keyboard reader's caret — standing.
// What it compares has to include everything a row draws. A draft's error is
// drawn as the note under "Not saved", so a refusal arriving, or a retry
// clearing one, must redraw the row even when nothing else about it moved.
func TestHandlerClientRedrawsADependencyDraftWhenItsErrorChanges(t *testing.T) {
	t.Parallel()
	runRelationshipRowClient(t, "a draft's error redraw", `
  const draftState = { dependsOn: new Map(), blocks: new Map() };
  const entry = { id: liveTask.id, task: { id: liveTask.id, title: liveTask.title, status: liveTask.status, priority: liveTask.priority, deleted: false } };
  draftState.dependsOn.set(liveTask.id, entry);
  const controller = taskRelationships("", draftState);
  const note = () => {
    const row = controller.list.children[0];
    if (!row || row.dataset.relationshipRow === undefined) throw new Error("the draft drew no row");
    const found = item(row, "relationship-row__note");
    return found ? found.textContent : "";
  };
  if (note() !== "") throw new Error("a draft nobody tried to save explains a refusal: " + JSON.stringify(note()));
  entry.error = "the save was refused";
  controller.update();
  if (note() !== "the save was refused") {
    throw new Error("a refusal arriving left the row undrawn: its note reads " + JSON.stringify(note()));
  }
  entry.error = "";
  controller.update();
  if (note() !== "") throw new Error("a cleared refusal is still drawn: " + JSON.stringify(note()));
`)
}

// A skipped redraw keeps the rows on screen, so it has to keep the buttons
// the controller disables and re-enables with them. Holding on to the buttons
// of the rows it built and threw away instead, a poll that landed while a
// removal was in flight left the × on screen disabled for good once the
// write had finished: setBusy(false) re-enabled buttons nobody can see.
func TestHandlerClientKeepsTheDrawnRemoveButtonsAcrossAnUnchangedRedraw(t *testing.T) {
	t.Parallel()
	runRelationshipRowClient(t, "the kept remove buttons", `
  const draftState = { dependsOn: new Map(), blocks: new Map() };
  draftState.dependsOn.set(liveTask.id, { id: liveTask.id, task: { id: liveTask.id, title: liveTask.title, status: liveTask.status, priority: liveTask.priority, deleted: false } });
  const controller = taskRelationships("", draftState);
  const row = controller.list.children[0];
  const remove = row && row.removeButton;
  if (!remove) throw new Error("the draft drew no ×");
  controller.setBusy(true);
  if (!remove.disabled) throw new Error("a write in flight left the × enabled");
  // A poll that changed nothing about the rows.
  controller.update();
  if (controller.list.children[0] !== row) throw new Error("an unchanged poll rebuilt the row");
  if (!remove.disabled) throw new Error("an unchanged poll re-enabled the × while the write was in flight");
  controller.setBusy(false);
  if (remove.disabled) throw new Error("the × on screen stayed disabled after the write finished");
`)
}
