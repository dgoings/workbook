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
// on the heading, the title came out one character per line.

// runRelationshipRowClient renders the New Task page, executes its client
// script against the fake DOM, and runs body with relationshipRow reachable.
//
// The row builder is a local of the script's IIFE, so the harness hoists it the
// way TestHandlerClientSidebarAccessibilityAndMobileOrder does. Calling it
// directly is the point: a row's shape depends on which branch built it, and
// these four shapes are otherwise only reachable by arranging four different
// tombstone-and-draft scenarios to get at one element each.
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
  // The relationship shape the dependency snapshot builds: an id, the task on
  // the other end, and whether this task's record owns the edge. A Depends On
  // row is always removable; a Blocks row is removable only while the task on
  // the other end is alive.
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

// A Blocks row whose task is deleted is the row with the most to read and
// nothing to press: heading, metadata, the Deleted badge, and the note saying
// why it cannot be changed here. That is the shape the screenshot was of, so it
// is the shape asserted hardest — one grid item, holding all four parts.
func TestHandlerClientKeepsAReadOnlyRelationshipRowInOneColumn(t *testing.T) {
	runRelationshipRowClient(t, "read-only relationship row layout", `
  const readOnly = relationshipRow({ id: deletedTask.id, task: deletedTask, removable: false }, () => {});
  if (readOnly.children.length !== 1) {
    throw new Error("a read-only relationship row deals " + readOnly.children.length +
      " items into the row's own grid; the row's second column is for controls and this row has none");
  }
  const column = readOnly.children[0];
  if (!classTokens(column).includes("relationship-row__text")) {
    throw new Error("the read-only row's one item is not the text column: " + JSON.stringify(column.className));
  }
  // Each of these used to be a grid item of the row, and the last two are the
  // ones auto-placement dealt into column two, where they starved the title.
  const heading = findElement(column, (element) => element.tagName === "H4");
  const metadata = item(column, "relationship-row__metadata");
  const badge = item(column, "relationship-state");
  const note = item(column, "relationship-row__note");
  if (!heading || !metadata || !badge || !note) {
    throw new Error("the read-only row's text column is missing a part: " + JSON.stringify(column.textContent));
  }
  if (badge.textContent !== "Deleted") {
    throw new Error("the deleted badge no longer says so: " + JSON.stringify(badge.textContent));
  }
  if (textParts(readOnly).some((part) => !column.contains(part))) {
    throw new Error("a text part of the read-only row still stands loose in the row's grid");
  }
  // The row keeps its identity through the wrapping: the list finds its rows by
  // this attribute, and the compact class is what makes a row a sidebar row.
  if (readOnly.dataset.relationshipRow === undefined || readOnly.dataset.relationshipId !== deletedTask.id ||
      !classTokens(readOnly).includes("relationship-row--compact")) {
    throw new Error("wrapping the text cost the row its identity");
  }
`)

	body := newTaskPage(t)
	// What places the structure above. The fix itself is structural — the item
	// count is what the test through the harness pins — so these only hold the
	// stack to the gaps the row used to set on its own parts. min-width: 0 is
	// pinned because it is served, not because it rescues the title: the title
	// column's min sizing function is a fixed 0 already, so no item minimum can
	// reach it. It is there for the day someone writes the template as
	// `1fr auto`, where that minimum would start to count.
	for _, fragment := range []string{
		// The stack, and the tighter gap the compact row already sets on its
		// own items — the text column has to split them the same way, or a
		// sidebar row would be spaced like a full-width one.
		`.relationship-row__text { display: grid; gap: .35rem; min-width: 0; }`,
		`.relationship-row--compact .relationship-row__text { gap: .16rem; }`,
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
}

// The three rows that do have something to press. Each of them spilled a part
// into column two as well: a deleted-but-removable row has a badge, and a
// failed draft has a badge, the failure text and a Retry button alongside
// Remove. All three are held to the same two items.
func TestHandlerClientKeepsRelationshipControlsOutOfTheTextColumn(t *testing.T) {
	runRelationshipRowClient(t, "removable relationship row layout", `
  const shapes = [
    ["a plain removable row", relationshipRow({ id: liveTask.id, task: liveTask, removable: true }, () => {}), false],
    // A prerequisite that has been deleted. The edge is recorded on this task,
    // so it stays removable even though the task on the other end is gone.
    ["a Depends On row for a deleted prerequisite", relationshipRow({ id: deletedTask.id, task: deletedTask, removable: true }, () => {}), false],
    // The second shape that was already broken before anyone reported it: a
    // draft whose save was refused carries a badge and the failure text, and
    // offers Retry as well as Remove.
    ["a draft row carrying a save failure", relationshipRow({ id: liveTask.id, task: liveTask, error: "the save was refused" }, () => {}, true, () => {}), true]
  ];
  shapes.forEach(([label, row, expectRetry]) => {
    if (row.children.length > 2) {
      throw new Error(label + " deals " + row.children.length + " items into the row's own grid, not two");
    }
    const column = item(row, "relationship-row__text");
    const controls = item(row, "relationship-row__controls");
    if (!column || !controls) throw new Error(label + " is missing its text column or its controls");
    if (row.children[0] !== column || row.children[1] !== controls) {
      throw new Error(label + " no longer reads left to right as text then controls");
    }
    if (textParts(row).some((part) => !column.contains(part))) {
      throw new Error(label + " leaves a text part loose in the row's grid");
    }
    // The buttons are what the group disables while a write is in flight and
    // what every other relationship test presses, so the row still names them.
    if (!row.removeButton || row.removeButton.parentElement !== controls) {
      throw new Error(label + " does not hold its Remove button in the controls");
    }
    if (row.removeButton.textContent !== "Remove" || row.removeButton.type !== "button" ||
        !row.removeButton.eventListeners.click) {
      throw new Error(label + " lost the working Remove button itself");
    }
    if (expectRetry) {
      if (!row.retryButton || row.retryButton.parentElement !== controls) {
        throw new Error(label + " does not hold its Retry button in the controls");
      }
      if (controls.children.indexOf(row.retryButton) >= controls.children.indexOf(row.removeButton)) {
        throw new Error(label + " draws Retry below the Remove button it used to precede");
      }
      // The failure text is the part that was dealt into column two and sized
      // that track to a whole sentence.
      const note = item(column, "relationship-row__note");
      if (!note || note.textContent !== "the save was refused") {
        throw new Error(label + " does not report the failure inside its text column");
      }
    } else if (row.retryButton) {
      throw new Error(label + " grew a Retry button it has nothing to retry");
    }
  });
`)

	body := newTaskPage(t)
	// Declared rather than emergent: the controls name the column the Remove
	// button used to claim for itself, and stack because a failed draft offers
	// two buttons. Centered against the text beside them, as the lone Remove
	// button was.
	if !strings.Contains(body, `.relationship-row__controls { display: grid; align-self: center; gap: .35rem; grid-column: 2; }`) {
		t.Error("relationship row styling does not place the controls in the row's second column")
	}
}

// The note on a read-only row was a refusal with nobody in it. "deleted tasks
// cannot be changed" states a rule without saying whose record the rule is
// about or what would lift it, which is why this row was reported as one that
// "can't be removed". A Blocks row is an edge stored on the other task's
// record, so the note names that task, the move that reopens it, and where the
// reader can make that move — the last part matters because a deleted task's
// heading is not a link and its detail route is refused, so the only Restore
// they can reach is the one on its card in the board's Deleted column.
func TestHandlerClientSaysWhoOwnsAReadOnlyRelationship(t *testing.T) {
	runRelationshipRowClient(t, "read-only relationship wording", `
  const readOnly = relationshipRow({ id: deletedTask.id, task: deletedTask, removable: false }, () => {});
  const note = item(readOnly, "relationship-row__note");
  if (!note) throw new Error("a read-only relationship row says nothing about why");
  if (note.textContent !== "This dependency is stored on the deleted task. Restore that task from the board's Deleted column to change it.") {
    throw new Error("the read-only note does not name the record it is about: " + JSON.stringify(note.textContent));
  }

  // The mirror case must not pick the note up. This task's own record owns the
  // edge to a deleted prerequisite, so Workbook can still drop it, and the row
  // has a Remove button rather than an explanation.
  const prerequisite = relationshipRow({ id: deletedTask.id, task: deletedTask, removable: true }, () => {});
  if (!prerequisite.removeButton || !prerequisite.removeButton.eventListeners.click) {
    throw new Error("a Depends On row for a deleted prerequisite is no longer removable");
  }
  if (item(prerequisite, "relationship-row__note")) {
    throw new Error("a removable row explains a refusal that did not happen");
  }
`)
}
