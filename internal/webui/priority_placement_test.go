package webui

import "testing"

// Where a new priority can go, offered once each.
//
// A list of n priorities has n+1 positions: above the most urgent, and below
// each of them. The control used to offer an "Above" and a "Below" for every
// priority, which is 2n options for n+1 places — every "Above X" is the same
// position as "Below the one before X", and the default option, "Least urgent",
// is the same position as "Below the least urgent" a second time.
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
  // Three priorities, four positions: the end, above the most urgent, and below
  // each of the two that something can sit below without being the end again.
  const want = [
    " | Least urgent",
    "before:urgent | Above Drop everything",
    "after:urgent | Below Drop everything",
    "after:soon | Below Soon"
  ];
  if (offered.length !== 4) {
    throw new Error("the control offers " + offered.length + " options for 4 positions: " + JSON.stringify(offered));
  }
  if (JSON.stringify(offered) !== JSON.stringify(want)) {
    throw new Error("the control offers " + JSON.stringify(offered) + ", want " + JSON.stringify(want));
  }
`)
}
