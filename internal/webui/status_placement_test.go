package webui

import "testing"

// Where a new status can go, offered once each.
//
// This is TestClientPriorityAddOffersEachPlacementOnce's sibling, and the
// statuses panel had the same doubling for the same reason: a list of n statuses
// has n+1 positions, and naming both neighbours of every one of them spells most
// of those twice. "Before X" is the position "After the one before X" already
// names.
//
// Every option names a column, the end of the list included, for the reason the
// priorities panel gives: an entry shaped unlike the rest reads as a member's
// name to somebody whose project happens to use those words.
//
// The end is the default, which is why the control is asked what it has selected
// rather than only what it offers: the list runs in board order, so the default
// is not the option it reads first.
//
// The count is asserted rather than only the wording, because the count is what
// keeps this from regrowing: an option added for a position that already has one
// fails here rather than reading as a longer list.
func TestClientStatusAddOffersEachPlacementOnce(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "the placements a new status can take", vocabulary, "head-1", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-1")+`;
  await openStatuses();

  const form = panelAdd();
  const placement = findElement(form, (element) => element.id === "status-new-placement");
  if (!placement) throw new Error("the add form offers no placement control");
  const offered = placement.children.map((option) => option.value + " | " + option.textContent);
  // Three statuses, four positions, in board order: before the first column,
  // then after each of the three.
  const want = [
    "before:icebox | Before Icebox",
    "after:icebox | After Icebox",
    "after:queued | After Queued Up",
    "after:shipped | After Shipped"
  ];
  if (offered.length !== 4) {
    throw new Error("the control offers " + offered.length + " options for 4 positions: " + JSON.stringify(offered));
  }
  if (JSON.stringify(offered) !== JSON.stringify(want)) {
    throw new Error("the control offers " + JSON.stringify(offered) + ", want " + JSON.stringify(want));
  }
  const unnamed = placement.children.filter((option) => option.value === "");
  if (unnamed.length !== 0) {
    throw new Error("the control offers an option naming no column: " + JSON.stringify(unnamed.map((o) => o.textContent)));
  }
  const standing = placement.children.filter((option) => option.selected).map((option) => option.value);
  if (JSON.stringify(standing) !== JSON.stringify(["after:shipped"])) {
    throw new Error("the control stands at " + JSON.stringify(standing) + ", want the end of the list");
  }
`)
}
