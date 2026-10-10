package webui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A tombstone's label says what dragging it does, and still says its priority
// is not the project's.
//
// The chip of a deleted card is marked like any other, so a reader who can see
// it is told. The label is the only thing a screen reader hears on focus, and a
// label that dropped the suffix on the deleted branch left that reader with
// less than the sighted one for the same card.
func TestHandlerClientNamesTheStrandedPriorityOnADeletedCardToo(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	task.Deleted = true
	live := clientPlacementTask("WB-01J0000000000000000000F708", "Filed here", core.StatusReady, "urgent")
	live.Head = "head-a"
	live.Deleted = true
	runPriorityClient(t, "a deleted card at an unresolvable priority", "/?deleted=1", projectPriorities(t), []core.Task{task, live}, `
  const stranded = boardCard(`+strconv.Quote(task.ID)+`);
  const settled = boardCard(`+strconv.Quote(live.ID)+`);
  if (!stranded || !settled) throw new Error("the deleted column did not draw both cards");
  const want = "Restore task Filed by a teammate by moving it to a column, at the unrecognized priority critical";
  if (stranded.getAttribute("aria-label") !== want) {
    throw new Error("the deleted card's label = " + JSON.stringify(stranded.getAttribute("aria-label")) + ", want " + JSON.stringify(want));
  }
  if (settled.getAttribute("aria-label") !== "Restore task Filed here by moving it to a column") {
    throw new Error("a deleted card at a live priority is labeled " + JSON.stringify(settled.getAttribute("aria-label")));
  }
`)
}

// A page that published no priorities at all — an older server — has nothing to
// judge a chip against, so it marks none. The empty set would otherwise call
// every priority stranded.
func TestHandlerClientMarksNoChipWhenThePageNamedNoPriorities(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	script := boardFiltersClientScript(t)
	program := clientDOMHarness("/", tasksDocumentJSON(t, []core.Task{task})) + `
boardView.dataset.priorities = "[]";
` + script + `
setTimeout(async () => {
  await intervalCallback();
  const card = boardCard(` + strconv.Quote(task.ID) + `);
  if (!card) throw new Error("the board did not draw the card");
  const chip = findElement(card, (element) => hasClassToken(element, "priority"));
  if (!chip) throw new Error("the card carries no priority chip");
  if ("priorityUnresolved" in chip.dataset || chip.getAttribute("title") !== null) {
    throw new Error("a page with no priorities marked its chip: " + JSON.stringify([chip.dataset, chip.getAttribute("title")]));
  }
}, 0);
`
	runBoardFiltersClient(t, "a page that published no priorities", program)
}

// The mark's glyph and its padding are part of what says "stranded" and what
// keeps the card's top row one height. The declaration test reads properties,
// so these two are pinned by what they declare.
func TestStrandedPriorityChipKeepsItsGlyphAndRowHeight(t *testing.T) {
	t.Parallel()
	body := priorityInkBoardPage(t, fourPriorityVocabulary(t, ""), nil)
	for _, want := range []string{
		`.priority[data-priority-unresolved]::before { content: "?" / ""; `,
		// The border takes a pixel on every side, so the padding gives it back.
		`padding: calc(.1rem - 1px) calc(.34rem - 1px)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stylesheet lost %s", want)
		}
	}
}
