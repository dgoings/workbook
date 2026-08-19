package webui

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the configuration route's second section does in a browser.
//
// The statuses half of this page is statuses_route_test.go's subject. What is
// pinned here is what the board settings add to it: that both sections are drawn
// from one read of one document, that a save states the whole configuration
// against the head that read reported, and that a refusal is recovered from the
// way a refused status change is — by adopting what the server says now stands.

// configurableHandler is a board wired for everything the configuration route
// offers, which is what `workbook serve` builds. The mutations are never
// reached: the client tests answer the routes from the fake fetch.
func configurableHandler(vocabulary core.Vocabulary, head string, tasks []core.Task) http.Handler {
	unreached := func() (VocabularyMutation, error) { return VocabularyMutation{}, nil }
	return NewHandler(Options{
		// Named, because the page a client is served is where its own name comes
		// from: an open board keeps the name it was opened with, whatever a later
		// read says, and the notice above main is what offers the reload.
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: vocabulary, Head: head,
				Display: core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"},
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return unreached()
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return unreached()
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return unreached()
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return unreached()
		},
		SetDisplay: func(context.Context, DisplayChange) (DisplayMutation, error) {
			return DisplayMutation{}, nil
		},
	})
}

// namedBoardHarness is what a named project's page carries, as the server
// renders it — the fake DOM is built beside the served page rather than out of
// it, so the two attributes the client titles routes from are stated here. That
// the server does render them is
// TestHandlerDrawsTheProjectsOwnNameAndItsCheckout's claim.
const namedBoardHarness = `
boardView.dataset.projectName = "Atlas";
boardView.dataset.titleSuffix = "Atlas";
`

// runConfigClient renders a fully wired board and executes its client script
// against the fake DOM and the recording fetch, with the reader starting on the
// board.
func runConfigClient(t *testing.T, purpose string, vocabulary core.Vocabulary, head string, body string) {
	t.Helper()
	runClientOverHandler(t, configurableHandler(vocabulary, head, nil),
		purpose, "/", namedBoardHarness, vocabulary, head, nil, body)
}

// configuredVocabularyJSON is what GET /api/vocabulary answers for a project
// that has named itself and chosen a colour: one document, one head, both
// sections of the page.
func configuredVocabularyJSON(t *testing.T, vocabulary core.Vocabulary, head string) string {
	t.Helper()
	return string(mustJSON(t, vocabularyDocument(VocabularyState{
		Vocabulary: vocabulary,
		Head:       head,
		Display:    core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"},
	})))
}

// displayMutationJSON is what a save answers with.
func displayMutationJSON(t *testing.T, head string, settings core.DisplaySettings) string {
	t.Helper()
	return string(mustJSON(t, DisplayMutationDocument{
		Format:  "workbook.display-mutation",
		Version: 1,
		Display: displayDocument(VocabularyState{Head: head, Display: settings}),
	}))
}

// displayStaleWriteJSON is the 409 a save composed against a head somebody else
// has moved past is answered with, carrying the settings as they now stand.
func displayStaleWriteJSON(t *testing.T, head string, settings core.DisplaySettings, message string) string {
	t.Helper()
	current := displayDocument(VocabularyState{Head: head, Display: settings})
	return string(mustJSON(t, DisplayErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error:   ErrorBody{Category: core.CategoryStaleWrite, Message: message},
		Display: &current,
	}))
}

// Both sections come from one read of one document. The settings are not asked
// for separately, because a second read could be answered from either side of a
// change and would offer a Save composed against a configuration nobody saw.
func TestClientConfigRouteDrawsBothSectionsFromOneRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runConfigClient(t, "opening the configuration page", vocabulary, "head-1", `
  vocabularyRead = `+configuredVocabularyJSON(t, panelRenamedVocabulary(t), "head-9")+`;
  if (displayPanel.hidden !== true) throw new Error("the settings section was mounted before anyone walked to it");
  await openStatuses();

  if (document.title !== "Configuration · Atlas") throw new Error("the page is titled " + JSON.stringify(document.title));
  // One read, for both sections.
  if (vocabularyCalls.length !== 1 || vocabularyCalls[0].url !== "/api/vocabulary") {
    throw new Error("the route asked for " + JSON.stringify(vocabularyCalls.map((call) => call.url)));
  }
  if (displayCalls.length !== 0) {
    throw new Error("the settings were read separately: " + JSON.stringify(displayCalls.map((call) => call.url)));
  }
  // Each section under its own heading, and the page's own above them.
  if (sectionHeadingText("config-title") !== "Configuration") {
    throw new Error("the page is headed " + JSON.stringify(sectionHeadingText("config-title")));
  }
  if (sectionHeadingText("statuses-title") !== "Statuses") {
    throw new Error("the statuses section is headed " + JSON.stringify(sectionHeadingText("statuses-title")));
  }
  if (sectionHeadingText("display-title") !== "Board settings") {
    throw new Error("the settings section is headed " + JSON.stringify(sectionHeadingText("display-title")));
  }
  if (!main.contains(displayPanel)) throw new Error("the settings section was not mounted into main");
  // Filled from what the read carried.
  if (displayField("name").value !== "Atlas") throw new Error("the name field holds " + JSON.stringify(displayField("name").value));
  if (displayField("primaryColor").value !== "#1a7f4b") {
    throw new Error("the accent field holds " + JSON.stringify(displayField("primaryColor").value));
  }
  if (displayField("textColor").value !== "") {
    throw new Error("a setting nobody configured is drawn as " + JSON.stringify(displayField("textColor").value));
  }

  // Leaving takes it away with the statuses, so the next visit reads again
  // rather than offering a Save composed against minutes-old values.
  await returnToBoard();
  if (displayPanel.parentElement) throw new Error("the settings section is still hanging off the document");
  if (document.title !== "Atlas") throw new Error("the board is titled " + JSON.stringify(document.title));
`)
}

// A save states the whole configuration against the head the page read, and the
// head it answers with becomes the head the next change on this page names —
// there is one ledger, so a saved name moves the tip a status change has to
// declare.
func TestClientConfigPageSavesEverySettingAgainstTheHeadItRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	renamed := panelRenamedVocabulary(t)
	runConfigClient(t, "saving the board settings", vocabulary, "head-1", `
  vocabularyRead = `+configuredVocabularyJSON(t, renamed, "head-9")+`;
  await openStatuses();

  displayField("name").value = "  Beta  ";
  displayField("primaryColor").value = "";
  displayField("textColor").value = "#3b2a1a";
  displayAnswer = { body: `+displayMutationJSON(t, "head-10", core.DisplaySettings{Name: "Beta", TextColor: "#3b2a1a"})+` };
  await saveDisplay();

  if (displayCalls.length !== 1) throw new Error("the save sent " + displayCalls.length + " requests");
  const wrote = displayCalls[0];
  if (wrote.method !== "PATCH" || wrote.url !== "/api/display") {
    throw new Error("the save sent " + wrote.method + " " + wrote.url);
  }
  // The whole configuration, trimmed, and the head it was composed against.
  const sent = wrote.body;
  if (sent.name !== "Beta" || sent.primaryColor !== "" || sent.textColor !== "#3b2a1a") {
    throw new Error("the save sent " + JSON.stringify(sent));
  }
  if (sent.expectedHead !== "head-9") throw new Error("the save named head " + JSON.stringify(sent.expectedHead));
  if (Object.keys(sent).length !== 4) throw new Error("the save sent " + JSON.stringify(Object.keys(sent)));

  // Re-rendered from the answer rather than from what was typed.
  if (displayField("name").value !== "Beta" || displayField("primaryColor").value !== "") {
    throw new Error("the form was not re-drawn from the answer: " + JSON.stringify(displayField("name").value));
  }
  if (displayPanelStatus.textContent.indexOf("Beta") < 0) {
    throw new Error("the save said " + JSON.stringify(displayPanelStatus.textContent));
  }

  // One ledger, one tip: the board is told its own page is out of date, and the
  // next status change names the head the save produced rather than the head the
  // read reported.
  if (vocabularyNotice.hidden) throw new Error("a change to the configuration raised no notice on the board");
  vocabularyAnswer = { body: `+panelMutationJSON(t, renamed, "head-11", VocabularyTaskCounts{}, nil)+` };
  const addForm = panelAdd();
  const newName = findElement(addForm, (element) => element.id === "status-new-name");
  newName.value = "triage";
  newName.eventListeners.input();
  await submitPanelForm(addForm);
  const change = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (!change) throw new Error("the statuses section sent nothing");
  if (change.body.expectedHead !== "head-10") {
    throw new Error("the status change named head " + JSON.stringify(change.body.expectedHead));
  }
`)
}

// A stale write is where a save stops. The settings it carries are the current
// ones and the page adopts them, and the copy names the configuration rather
// than the statuses — either half of one ledger may be what moved.
func TestClientConfigPageStopsAtAStaleDisplaySave(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runConfigClient(t, "a stale save of the board settings", vocabulary, "head-1", `
  vocabularyRead = `+configuredVocabularyJSON(t, panelRenamedVocabulary(t), "head-9")+`;
  await openStatuses();

  displayField("name").value = "Beta";
  displayAnswer = { ok: false, body: `+displayStaleWriteJSON(t, "head-12",
		core.DisplaySettings{Name: "Gamma", PrimaryColor: "#7f1a4b"},
		"this project's configuration has changed since head-9; reload and try again")+` };
  await saveDisplay();

  // Adopted whole: no refetch, no re-base, no retry.
  if (displayCalls.length !== 1) throw new Error("the page retried: " + displayCalls.length + " requests");
  if (displayField("name").value !== "Gamma" || displayField("primaryColor").value !== "#7f1a4b") {
    throw new Error("the form still holds " + JSON.stringify(displayField("name").value));
  }
  const said = displayPanelStatus.textContent;
  if (said.indexOf("configuration changed in another clone") < 0) {
    throw new Error("the refusal was reported as " + JSON.stringify(said));
  }
  if (said.indexOf("statuses") >= 0) {
    throw new Error("the refusal named the statuses: " + JSON.stringify(said));
  }
  // The controls are usable again, so the reader can look and save again.
  if (displayField("name").disabled) throw new Error("a refused save left the form disabled");
`)
}

// A save waits for the board's own writes to settle, for the reason a status
// change does: a pending intent was composed against this configuration, and
// moving the tip out from under it would have it refused as a stale write
// nothing was actually wrong with.
func TestClientConfigPageWaitsForPendingBoardChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	task.Head = "head-a"
	runClientOverHandler(t, configurableHandler(vocabulary, "head-7", []core.Task{task}),
		"a save behind a board write", "/", "", vocabulary, "head-7", []core.Task{task}, `
  vocabularyRead = `+configuredVocabularyJSON(t, vocabulary, "head-7")+`;

  // Hold the board's write open, then make one on the board: the queue is now
  // non-empty, and it stays that way while the reader walks to the page.
  let releaseTaskWrite = null;
  taskWriteGate = new Promise((resolve) => { releaseTaskWrite = resolve; });
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  const dragged = boardCard(`+strconv.Quote(task.ID)+`);
  dragged.rect = { top: 0, bottom: 80 };
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });
  await settle();
  if (fetchCalls.filter((call) => call.method === "PATCH" && call.url !== "/api/display").length !== 1) {
    throw new Error("the board write did not go out");
  }

  await openStatuses();
  displayField("name").value = "Beta";
  displayAnswer = { body: `+displayMutationJSON(t, "head-10", core.DisplaySettings{Name: "Beta"})+` };
  const saving = displayForm().eventListeners.submit({ preventDefault() {} });
  await settle();

  if (displayCalls.length !== 0) {
    throw new Error("the save went out over a board that was still writing");
  }
  if (displayPanelStatus.textContent.indexOf("already in flight") < 0) {
    throw new Error("the page did not say it was waiting: " + JSON.stringify(displayPanelStatus.textContent));
  }
  // Its controls are disabled while it waits, and so are the statuses' — one
  // ledger, one tip, so neither section may be changed while the other is
  // changing it.
  if (displayField("name").disabled !== true) throw new Error("the form was usable while a save waited");
  if (panelControl(panelRow("icebox"), "Edit Icebox").disabled !== true) {
    throw new Error("a save in flight left the statuses controls usable");
  }

  releaseTaskWrite();
  await saving;
  await settle();
  if (displayCalls.length !== 1) throw new Error("the save never went: " + displayCalls.length);
  if (displayField("name").value !== "Beta") throw new Error("the form was not re-drawn from the answer");
  if (panelControl(panelRow("icebox"), "Edit Icebox").disabled !== false) {
    throw new Error("the statuses controls stayed disabled after the save finished");
  }
`)
}

// A board that can administer its statuses and not its display settings is
// served no settings section, and the route is the page it always was rather
// than a script that fails on markup it was not given.
func TestClientConfigRouteWorksWithoutTheSettingsSection(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runStatusesClient(t, "a configuration page with no board settings", "/", withoutDisplaySettings,
		vocabulary, "head-1", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, panelRenamedVocabulary(t), "head-9")+`;
  await openStatuses();

  if (!statusesRoute()) throw new Error("the route drew no configuration page");
  if (findElement(main, (element) => element === displayPanel)) {
    throw new Error("a board that cannot write its settings drew the section anyway");
  }
  if (sectionHeadingText("display-title")) throw new Error("the page drew a heading over a section it has not got");
  if (sectionHeadingText("statuses-title") !== "Statuses") {
    throw new Error("the statuses section is headed " + JSON.stringify(sectionHeadingText("statuses-title")));
  }
  if (panelStatuses().join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the statuses list is " + panelStatuses().join(","));
  }
`)
}
