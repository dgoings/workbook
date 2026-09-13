package webui

import "testing"

// Where a new priority can go, offered once each.
//
// A list of n priorities has n+1 positions: above the most urgent, and below
// each of them. The control used to offer an "Above" and a "Below" for every
// priority, which is 2n options for n+1 places — every "Above X" is the same
// position as "Below the one before X".
//
// Every option names a priority, the bottom of the list included. A standing
// "Least urgent" was the one entry shaped unlike the rest, and it read as a
// member's name to anybody whose project has a priority called Urgent — the
// first name people reach for. "Below Low" is the same place and cannot be
// mistaken for one.
//
// The bottom is the default, which is why the control is asked what it has
// selected rather than only what it offers: the list runs in the order the rows
// are drawn, so the default is not the option it reads first.
//
// The count is asserted rather than only the wording, because the count is what
// keeps this from regrowing: an option added for a position that already has one
// fails here rather than reading as a longer list.
func TestClientPriorityAddOffersEachPlacementOnce(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "the placements a new priority can take", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  await openStatuses();

  const form = priorityAdd();
  const placement = findElement(form, (element) => element.id === "priority-new-placement");
  if (!placement) throw new Error("the add form offers no placement control");
  const offered = placement.children.map((option) => option.value + " | " + option.textContent);
  // Three priorities, four positions, in the order the rows are drawn: above
  // the most urgent, then below each of the three.
  const want = [
    "before:urgent | Above Drop everything",
    "after:urgent | Below Drop everything",
    "after:soon | Below Soon",
    "after:low | Below Low"
  ];
  if (offered.length !== 4) {
    throw new Error("the control offers " + offered.length + " options for 4 positions: " + JSON.stringify(offered));
  }
  if (JSON.stringify(offered) !== JSON.stringify(want)) {
    throw new Error("the control offers " + JSON.stringify(offered) + ", want " + JSON.stringify(want));
  }
  // Nothing is offered that does not name a priority: a standing "Least urgent"
  // would read as one to a project that has a priority called Urgent.
  const unnamed = placement.children.filter((option) => option.value === "");
  if (unnamed.length !== 0) {
    throw new Error("the control offers an option naming no priority: " + JSON.stringify(unnamed.map((o) => o.textContent)));
  }
  // And the bottom of the list is what stands, not the first option read.
  const standing = placement.children.filter((option) => option.selected).map((option) => option.value);
  if (JSON.stringify(standing) !== JSON.stringify(["after:low"])) {
    throw new Error("the control stands at " + JSON.stringify(standing) + ", want the bottom of the list");
  }
`)
}
