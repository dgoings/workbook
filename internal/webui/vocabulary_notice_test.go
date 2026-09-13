package webui

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the standing "reload the board" notice is raised on.
//
// It used to be raised on the configuration head moving, which was right while
// nothing a change carried could be drawn without a reload. A color can be:
// every answer now carries the stylesheet the board would have been served, and
// the page swaps it in place. So a reader who set a color watched the board
// change under them and was then told to reload to see it.
//
// The narrowing is by content rather than by authorship, which is what makes it
// right for a teammate's change as well as your own: the notice is raised on
// what the page is *not already drawing* — the columns and the priorities, by
// membership, order and label — and a head that moved without moving any of
// those is recorded in silence.

// coloredBuiltInPriorities is the built-in three with a color on the first,
// which is the whole of what `workbook priority color` changes.
func coloredBuiltInPriorities(t *testing.T, color string) core.PriorityVocabulary {
	t.Helper()
	definitions := core.BuiltInPriorityVocabulary().Definitions()
	definitions[0].Color = color
	priorities, err := core.NewPriorityVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// The digest is a statement about the columns and the priorities and about
// nothing else, which is the whole of the narrowing: a recolor leaves it where
// it was, and every change that moves a column or a priority moves it.
func TestVocabularyShapeFollowsTheColumnsAndThePrioritiesAndNotTheColors(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	base := VocabularyState{Vocabulary: vocabulary, Head: "head-1", Priorities: configuredPriorities(t)}
	shape := vocabularyShape(base)
	if shape == "" {
		t.Fatal("a configured project has no shape at all, so nothing here could be compared")
	}

	unchanged := []struct {
		name  string
		state VocabularyState
	}{{
		name:  "the same configuration at a later head",
		state: VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: configuredPriorities(t)},
	}, {
		name:  "a priority recolored",
		state: VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: recoloredPriorities(t, "#7c3aed")},
	}, {
		name:  "the board renamed and recolored",
		state: VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: configuredPriorities(t), Display: core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"}},
	}}
	for _, test := range unchanged {
		if got := vocabularyShape(test.state); got != shape {
			t.Errorf("%s moves the shape, so the board is told to reload for something it is already drawing", test.name)
		}
	}

	moved := []struct {
		name  string
		state VocabularyState
	}{{
		name:  "a status renamed",
		state: VocabularyState{Vocabulary: panelRenamedVocabulary(t), Head: "head-2", Priorities: configuredPriorities(t)},
	}, {
		name:  "a priority renamed",
		state: VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: renamedPriorities(t)},
	}, {
		name:  "a priority added",
		state: VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: widenedPriorities(t)},
	}}
	for _, test := range moved {
		if got := vocabularyShape(test.state); got == shape {
			t.Errorf("%s leaves the shape where it was, so the board is never told its columns are out of date", test.name)
		}
	}

	// And a project that has recorded no statuses is drawing the legacy six, so
	// its shape is the shape of the six rather than of an empty list — otherwise
	// every such board would be told forever that its columns had moved.
	empty := vocabularyShape(VocabularyState{Head: "head-1"})
	legacy := vocabularyShape(VocabularyState{Vocabulary: core.LegacyVocabulary(), Head: "head-1"})
	if empty != legacy {
		t.Error("a project with no recorded statuses has a different shape from the six it is actually drawing")
	}
}

// runNoticeClient runs a client over a page that is really drawing the
// priorities the panel behind it is about.
//
// The shared harness renders the board's own attributes for a project that
// configured no priorities, which is what every test that is not about them
// wants. These are about them: the question is whether a change matches what the
// page is drawing, and a harness drawing a different set would answer it by
// accident.
func runNoticeClient(
	t *testing.T,
	purpose string,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
	body string,
) {
	t.Helper()
	state := VocabularyState{Vocabulary: vocabulary, Head: head, Priorities: priorities}
	prelude := priorityFetchHarness + `
boardView.dataset.priorities = ` + strconv.Quote(pagePriorities(priorities)) + `;
boardView.dataset.vocabularyShape = ` + strconv.Quote(vocabularyShape(state)) + `;
`
	runClientOverHandler(t, prioritiesAdministrableHandler(vocabulary, priorities, head, nil),
		purpose, "/", prelude, vocabulary, head, nil, body)
}

// Setting a color says nothing to the board, because the board has already
// drawn it: the answer carries the stylesheet and the page swaps it in place.
func TestClientRaisesNoNoticeForAColorTheBoardHasAlreadyDrawn(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runNoticeClient(t, "a color the board is already drawing", vocabulary, priorities, "head-7", `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, recoloredPriorities(t, "#7c3aed"), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  const color = findElement(form, (element) => element.id === "priority-color-urgent");
  color.value = "#7c3aed";
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) throw new Error("the Save sent " + priorityCalls.length + " requests");
  // The board did draw the new ink, which is why there is nothing to announce.
  if (boardPriorityInkStyle.textContent.indexOf("#7c3aed") < 0) {
    throw new Error("the recolor never reached the board, so this test is not about what it says it is");
  }
  if (vocabularyNotice.hidden !== true) {
    throw new Error("the board was told to reload for a color it had already drawn");
  }
`)
}

// Renaming a priority still says so: the cards are drawn under the name the page
// was served with, and the label in every form and message is that name too.
func TestClientStillRaisesANoticeWhenAPriorityIsRenamed(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runNoticeClient(t, "a priority renamed under the board", vocabulary, priorities, "head-7", `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPriorities(t), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-name-urgent").value = "critical";
  await submitPriorityForm(form);

  if (vocabularyNotice.hidden !== false) {
    throw new Error("a renamed priority left the board with no notice that it is drawing the old one");
  }
`)
}

// And so does renaming a status, which is the case the notice was written for:
// the columns on screen were built from the old vocabulary and only a reload
// rebuilds them.
func TestClientStillRaisesANoticeWhenAStatusIsRenamed(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a status renamed under the board", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();
  if (vocabularyNotice.hidden !== true) throw new Error("the notice was up before anything changed");

  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);

  if (vocabularyNotice.hidden !== false) {
    throw new Error("a status change left the board with no notice that its columns are out of date");
  }
`)
}

// A read that lands on a later head with the same statuses and priorities says
// nothing at all. This is the same narrowing seen from the other side: the page
// is current, so the head is recorded and the reader is left alone.
func TestClientRaisesNoNoticeForAHeadThatMovedWithoutTheConfiguration(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a head that moved past an unchanged configuration", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-9")+`;
  await openStatuses();

  if (vocabularyNotice.hidden !== true) {
    throw new Error("a head that moved without moving a column told the board to reload");
  }
`)
}

// The same two answers on the poll, which is how a teammate's change arrives.
//
// The board does not redraw another clone's color — the poll carries tasks, not
// a stylesheet — so this is the case where the page really is drawing something
// older than the server holds, and the decision is still to stay quiet: a color
// is not what a reload is for. Their rename is, and it says so.
func TestClientPollAnnouncesATeammatesRenameAndNotTheirRecolor(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
	}
	recolored := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-2",
		VocabularyShape: vocabularyShape(VocabularyState{
			Vocabulary: vocabulary, Head: "head-2", Priorities: coloredBuiltInPriorities(t, "#7c3aed"),
		}),
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	renamed := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-3",
		VocabularyShape: vocabularyShape(VocabularyState{
			Vocabulary: panelRenamedVocabulary(t), Head: "head-3",
		}),
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	runVocabularyClient(t, "a teammate's recolor and rename on the poll", "/", vocabulary, "head-1", tasks, `
  taskResponse = `+string(recolored)+`;
  await intervalCallback();
  if (vocabularyNotice.hidden !== true) {
    throw new Error("a poll carrying another clone's recolor told the reader to reload");
  }

  taskResponse = `+string(renamed)+`;
  await intervalCallback();
  if (vocabularyNotice.hidden !== false) {
    throw new Error("a poll carrying another clone's rename raised no notice");
  }
`)
}

// A page served by a build that carries no shape, or a poll answered by one,
// keeps the behavior it had: the head moved, and nothing here can say whether
// what moved is on screen, so the notice goes up.
func TestClientAnnouncesAMovedHeadItCannotCompare(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
	}
	served := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-2",
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	runVocabularyClient(t, "a poll with no shape on it", "/", vocabulary, "head-1", tasks, `
  taskResponse = `+string(served)+`;
  await intervalCallback();
  if (vocabularyNotice.hidden !== false) {
    throw new Error("a head that moved with nothing to compare it against was passed over in silence");
  }
`)
}

// The board page states the shape it was rendered from, which is the reading the
// client compares everything against.
func TestHandlerBoardPageStatesTheShapeItWasDrawnFrom(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	handler := prioritiesAdministrableHandler(vocabulary, priorities, "head-7", nil)
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", response.Code, http.StatusOK)
	}
	want := vocabularyShape(VocabularyState{Vocabulary: vocabulary, Head: "head-7", Priorities: priorities})
	if !strings.Contains(response.Body.String(), `data-vocabulary-shape="`+want+`"`) {
		t.Error("the board page does not state the configuration it was drawn from")
	}
}
