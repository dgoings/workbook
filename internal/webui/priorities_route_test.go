package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the priorities section of the configuration route does, and what it
// refuses to do.
//
// It is the statuses section's sibling: the same read on entry, the same head
// discipline, the same busy/quiescence protocol, the same wholesale adoption of
// whatever a change answers with. What is pinned here is what a priority is
// that a status is not.
//
//   - A priority has one role, and the role is given rather than held. There is
//     no operation that clears it, so the priority that carries it is drawn
//     carrying it and has nothing to offer.
//   - Each change is its own route against its own head, so a Save that renames
//     a priority AND takes the default is two requests, in order, the second
//     against the head the first answered with and addressed at the NEW name.
//     It can half-land, and this panel reports that rather than inventing a
//     compensating write.
//   - And the one this section is written around: what a read answers is the
//     EFFECTIVE vocabulary. A project that has configured no priorities is
//     answered with the built-in three, because that is what the board has to
//     draw either way, and nothing in the document says which it is. So the
//     panel never sends back what it read.

// prioritiesAdministrableHandler is a board wired for the statuses, the
// priorities and the board settings, which is what `workbook serve` builds and
// the only kind whose configuration route carries all three sections. The
// mutations are never reached: the client tests answer the routes from the fake
// fetch, and what the routes do with a request is priority_mutation_test.go's
// subject.
func prioritiesAdministrableHandler(
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
	tasks []core.Task,
) http.Handler {
	return NewHandler(prioritiesAdministrableOptions(vocabulary, priorities, head, tasks))
}

// prioritiesAdministrableOptions is that board's wiring, held out so a test can
// withhold one capability from it and ask what the configuration page does then.
func prioritiesAdministrableOptions(
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
	tasks []core.Task,
) Options {
	unreachedStatus := func() (VocabularyMutation, error) { return VocabularyMutation{}, nil }
	unreached := func() (VocabularyPriorityMutation, error) { return VocabularyPriorityMutation{}, nil }
	return Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{Vocabulary: vocabulary, Head: head, Priorities: priorities}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return unreachedStatus()
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return unreachedStatus()
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return unreachedStatus()
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return unreachedStatus()
		},
		SetDisplay: func(context.Context, DisplayChange) (DisplayMutation, error) {
			return DisplayMutation{}, nil
		},
		AddPriority: func(context.Context, VocabularyPriorityAddition) (VocabularyPriorityMutation, error) {
			return unreached()
		},
		EditPriority: func(context.Context, core.Priority, VocabularyPriorityEdit) (VocabularyPriorityMutation, error) {
			return unreached()
		},
		RemovePriority: func(context.Context, core.Priority, VocabularyPriorityRemoval) (VocabularyPriorityMutation, error) {
			return unreached()
		},
		MovePriority: func(context.Context, core.Priority, VocabularyPriorityMove) (VocabularyPriorityMutation, error) {
			return unreached()
		},
		SetDefaultPriority: func(context.Context, core.Priority, VocabularyPriorityDefault) (VocabularyPriorityMutation, error) {
			return unreached()
		},
		RecolorPriority: func(context.Context, core.Priority, VocabularyPriorityRecolor) (VocabularyPriorityMutation, error) {
			return unreached()
		},
	}
}

// priorityFetchHarness answers the six priority routes from a queue, so one test
// can drive a chain whose second request is answered differently from its first
// — which is the whole of the rename-plus-default case. Everything else falls
// through to the statuses harness this runs on top of.
const priorityFetchHarness = `
const priorityCalls = [];
const priorityAnswers = [];
const beneathPriorityFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  const method = (options.method || "GET").toUpperCase();
  if (url.startsWith("/api/vocabulary/priorities")) {
    const call = { url, method, headers: options.headers || {},
      body: options.body === undefined ? null : JSON.parse(options.body) };
    priorityCalls.push(call);
    fetchCalls.push(call);
    const answer = priorityAnswers.shift();
    if (!answer) throw new Error("the panel sent " + method + " " + url + " with no answer prepared");
    return { ok: answer.ok !== false, json: async () => answer.body };
  }
  return beneathPriorityFetch(url, options);
};
function priorityAdd() {
  const form = findElement(priorityPanelBody, (element) => hasDataKey(element, "priorityAdd"));
  if (!form) throw new Error("the panel drew no add-a-priority form");
  return form;
}
function priorityForm(priority, key) {
  const form = findElement(priorityPanelBody, (element) => element.dataset[key] === priority);
  if (!form) throw new Error("the panel drew no " + key + " form for " + priority);
  return form;
}
// Opens a row's form the way a reader does, through the control on the row.
async function openPriorityForm(priority, caption) {
  const control = panelControl(priorityRow(priority), caption);
  if (!control) throw new Error("the row for " + priority + " offers no " + caption);
  await control.eventListeners.click();
  await settle();
}
async function submitPriorityForm(form) {
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
}
`

// runPriorityPanelClient renders a board wired for all three configuration sections
// and executes its client script. The reader starts on the board, which is where
// the link to the configuration page is.
//
// The board behind the page is drawn from the same priorities the panel is
// about. The shared DOM harness states the built-in three, which is what a board
// served without a priority vocabulary publishes and what every test that is not
// about priorities wants; every test in this file is about them, and a board
// drawing a different set from the one its handler serves would answer questions
// about what the page is already showing by accident. So the three attributes
// the server renders from the project's priorities — the priorities themselves,
// the default, and the digest of what the page is drawing — are restated here
// from the vocabulary this handler was built with.
func runPriorityPanelClient(
	t *testing.T,
	purpose string,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
	tasks []core.Task,
	body string,
) {
	t.Helper()
	state := VocabularyState{Vocabulary: vocabulary, Head: head, Priorities: priorities}
	prelude := priorityFetchHarness + `
boardView.dataset.priorities = ` + strconv.Quote(pagePriorities(priorities)) + `;
boardView.dataset.defaultPriority = ` + strconv.Quote(string(priorities.Default())) + `;
boardView.dataset.vocabularyShape = ` + strconv.Quote(vocabularyShape(state)) + `;
`
	runClientOverHandler(t, prioritiesAdministrableHandler(vocabulary, priorities, head, tasks),
		purpose, "/", prelude, vocabulary, head, tasks, body)
}

// configuredPriorities is a project that named its own: three of them, in its own
// order, with the default on the middle one and one of them already colored.
// None of the built-in three's tokens survive except low, which is there so a
// test cannot pass by accident on a substituted vocabulary.
func configuredPriorities(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// renamedPriorities is what a rename answers with: urgent is now "critical" and
// relabeled, and the old name forwards to it.
func renamedPriorities(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "critical", Label: "Critical", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, []core.PriorityAlias{{From: "urgent", To: "critical"}}, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// renamedPrioritiesWithTheDefaultMoved is what the second request of the chain
// answers with when it lands: the rename stands and the role has moved onto it.
func renamedPrioritiesWithTheDefaultMoved(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "critical", Label: "Critical", Rank: "1/1", Tags: []core.PriorityTag{core.PriorityTagDefault}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, []core.PriorityAlias{{From: "urgent", To: "critical"}}, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// priorityVocabularyJSON is what GET /api/vocabulary answers for a project with
// these statuses and these priorities, built by the server's own builder.
func priorityVocabularyJSON(
	t *testing.T,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
) string {
	t.Helper()
	return string(mustJSON(t, vocabularyDocument(VocabularyState{
		Vocabulary: vocabulary, Head: head, Priorities: priorities,
	})))
}

// priorityMutationJSON is what one of the six priority routes answers.
func priorityMutationJSON(
	t *testing.T,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head string,
	tasks VocabularyPriorityTaskCounts,
	warnings []core.Warning,
) string {
	t.Helper()
	return string(mustJSON(t, VocabularyPriorityMutationDocument{
		Format:  "workbook.priority-mutation",
		Version: 1,
		Vocabulary: vocabularyDocument(VocabularyState{
			Vocabulary: vocabulary, Head: head, Priorities: priorities,
		}),
		Tasks:    tasks,
		Warnings: warnings,
	}))
}

// priorityStaleWriteJSON is the 409 a change composed against a head somebody
// else has moved past is answered with, carrying the priorities as they stand.
func priorityStaleWriteJSON(
	t *testing.T,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	head, message string,
) string {
	t.Helper()
	current := vocabularyDocument(VocabularyState{
		Vocabulary: vocabulary, Head: head, Priorities: priorities,
	})
	return string(mustJSON(t, VocabularyErrorDocument{
		Format:     "workbook.error",
		Version:    1,
		Error:      ErrorBody{Category: core.CategoryStaleWrite, Message: message},
		Vocabulary: &current,
	}))
}

// The served shell of the priorities section, which is the whole of the client's
// capability gate. It is matched as markup rather than as the bare attribute
// name, because the client script asks for the element by that name on every
// board and so carries the string whether or not the section was served.
const priorityPanelMarkup = `<div class="admin" data-priority-panel`

// A board wired for the priority mutations carries the section; one that is not
// carries no markup for it at all, so the client has nothing to draw and draws
// nothing, rather than a heading over a section that could only be refused.
func TestHandlerConfigCarriesThePrioritiesSectionOnlyWhenItCanChangeThem(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)

	served := request(t, prioritiesAdministrableHandler(vocabulary, priorities, "head-1", nil), http.MethodGet, "/config")
	if served.Code != http.StatusOK {
		t.Fatalf("GET /config status = %d, want %d", served.Code, http.StatusOK)
	}
	if body := served.Body.String(); !strings.Contains(body, priorityPanelMarkup) {
		t.Error("a board wired for the priority mutations served no priorities section")
	}

	withoutPriorities := request(t, administrableHandler(vocabulary, "head-1", nil), http.MethodGet, "/config")
	if withoutPriorities.Code != http.StatusOK {
		t.Fatalf("GET /config status = %d, want %d", withoutPriorities.Code, http.StatusOK)
	}
	if body := withoutPriorities.Body.String(); strings.Contains(body, priorityPanelMarkup) {
		t.Error("a board that cannot change its priorities served the priorities section anyway")
	}
}

// The client script must not name a priority role either.
//
// It is the same rule that keeps it from naming a status tag, and here the two
// rules are the same assertion twice over: the sets are separate and share the
// word `default`, so a script spelling the priority role would be a script
// spelling a status tag as well. The roles come from the section's own
// attribute, which the server writes from core's list.
func TestClientScriptNamesNoPriorityRoleOfItsOwn(t *testing.T) {
	response := request(t,
		prioritiesAdministrableHandler(handlerVocabulary(t), configuredPriorities(t), "head-1", nil),
		http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	for _, tag := range core.PriorityTags() {
		if literal := `"` + string(tag) + `"`; strings.Contains(script, literal) {
			t.Errorf("the client script names the priority role %s, which is the vocabulary's to name", literal)
		}
	}
	if !strings.Contains(script, "dataset.priorityTags") {
		t.Error("the client script no longer reads the roles the server rendered")
	}
}

// Entering the route reads the priorities from the server and draws them, and
// leaving takes the list with it so the next visit reads again. It is the same
// read the statuses come out of, because the two are sections of one ledger and
// a second read could be answered from either side of a change.
func TestClientPrioritiesSectionReadsTheProjectsPrioritiesOnEntry(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPriorityPanelClient(t, "opening the priorities section", vocabulary, configuredPriorities(t), "head-1", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, configuredPriorities(t), "head-9")+`;
  if (priorityPanel.hidden !== true) throw new Error("the priorities section was mounted before anyone walked to it");
  await openStatuses();

  if (priorityPanel.hidden !== false) throw new Error("the route left the priorities section hidden");
  if (!statusesRoute().contains(priorityPanel)) throw new Error("the route drew no priorities section into the page");
  if (sectionHeadingText("priorities-title") !== "Priorities") {
    throw new Error("the section is headed " + JSON.stringify(sectionHeadingText("priorities-title")));
  }
  // One read for both sections, not two.
  if (vocabularyCalls.length !== 1) {
    throw new Error("entering the route asked " + vocabularyCalls.length + " times for the configuration");
  }
  const listed = panelPriorities();
  if (listed.join(",") !== "urgent,soon,low") throw new Error("the section listed " + listed.join(","));
  const row = priorityRow("urgent");
  if (row.textContent.indexOf("Drop everything") < 0) throw new Error("the row does not carry the label: " + row.textContent);
  if (row.textContent.indexOf("urgent") < 0) throw new Error("the row does not name the token: " + row.textContent);
  // The role is drawn on the priority the server says holds it, and on no other.
  const roles = panelPriorities().map((priority) => {
    const chips = findElements(priorityRow(priority), (element) => Boolean(element.dataset.priorityTag));
    return priority + ":" + chips.map((chip) => chip.dataset.priorityTag).join("+");
  });
  if (roles.join(",") !== "urgent:,soon:default,low:") throw new Error("the roles drawn are " + roles.join(","));

  // Leaving takes the list with it, and returning reads again.
  await returnToBoard();
  if (priorityPanel.hidden !== true) throw new Error("Back left the priorities section mounted");
  if (panelPriorities().length !== 0) throw new Error("the page kept a list nobody is looking at");
  await openStatuses();
  if (vocabularyCalls.length !== 2) throw new Error("returning read " + vocabularyCalls.length + " times in total");
  if (panelPriorities().join(",") !== "urgent,soon,low") throw new Error("the returning visit drew " + panelPriorities().join(","));
`)
}

// THE TRAP. What the page and the read carry is the effective vocabulary, so a
// project that configured no priorities is answered with the built-in three and
// the client cannot tell which it is looking at. Drawing them must therefore
// cost nothing: no priority write goes out for opening the section, and none
// goes out for saving a field that has nothing to do with them.
//
// The first priority write on a project records the built-ins as a decision it
// never made and stamps the marker that parks every teammate on an older build,
// in exchange for changing nothing.
func TestClientPrioritiesSectionWritesNothingForAProjectThatConfiguredNone(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	unconfigured := core.PriorityVocabulary{}
	runPriorityPanelClient(t, "opening the priorities section on an unconfigured project", vocabulary, unconfigured, "head-1", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, unconfigured, "head-1")+`;
  displayAnswer = { body: `+displayMutationJSON(t, VocabularyState{Vocabulary: vocabulary, Head: "head-2", Priorities: unconfigured, Display: core.DisplaySettings{Name: "Atlas"}})+` };
  await openStatuses();

  // The built-in three are drawn, because they are what the board draws.
  if (panelPriorities().join(",") !== "high,medium,low") {
    throw new Error("the section drew " + panelPriorities().join(","));
  }
  if (priorityCalls.length !== 0) {
    throw new Error("drawing the priorities sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }

  // Saving an unrelated field. The whole configuration is one ledger, and this
  // is the write that would carry a priorities section with it if the panel
  // round-tripped what it read.
  displayField("name").value = "Atlas";
  await saveDisplay();
  if (displayCalls.length !== 1) throw new Error("the save sent " + displayCalls.length + " requests");
  const sent = displayCalls[0].body;
  if (Object.prototype.hasOwnProperty.call(sent, "priorities") ||
      Object.prototype.hasOwnProperty.call(sent, "priority")) {
    throw new Error("the save carried the priorities it read: " + JSON.stringify(sent));
  }
  if (priorityCalls.length !== 0) {
    throw new Error("saving an unrelated field wrote " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }

  // And a Save on a row nobody edited is finished rather than sent: an edit that
  // changes nothing is refused by the writer, deliberately, because recording it
  // would cost the project exactly what the paragraph above describes.
  await openPriorityForm("medium", "Edit Medium");
  await submitPriorityForm(priorityForm("medium", "priorityEdit"));
  if (priorityCalls.length !== 0) {
    throw new Error("an unedited Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("Medium") < 0) {
    throw new Error("the section said " + JSON.stringify(said) + " about a Save with nothing in it");
  }
`)
}

// Adding a priority names one placement, against the head the section read, and
// the section redraws itself from the answer wholesale.
func TestClientPrioritiesSectionAddsAPriorityAgainstTheHeadItRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	added, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: "whenever", Label: "Whenever", Rank: "5/2", Tags: []core.PriorityTag{}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	runPriorityPanelClient(t, "adding a priority", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, added, "head-8", VocabularyPriorityTaskCounts{}, []core.Warning{
		{Code: "docs-refresh-incomplete", Message: "the generated guidelines are out of date; run workbook docs update"},
	})+` });
  await openStatuses();

  const form = priorityAdd();
  const name = findElement(form, (element) => element.id === "priority-new-name");
  const label = findElement(form, (element) => element.id === "priority-new-label");
  const placement = findElement(form, (element) => element.id === "priority-new-placement");
  const submit = panelControl(form, "Add priority");
  if (submit.disabled !== true) throw new Error("Add priority was offered with no name typed into it");
  name.value = "whenever";
  name.eventListeners.input();
  if (submit.disabled !== false) throw new Error("Add priority stayed disabled after a name was typed");
  label.value = "Whenever";
  chooseOption(placement, "after:soon");
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) throw new Error("the section sent " + priorityCalls.length + " changes for one gesture");
  const wrote = priorityCalls[0];
  if (wrote.method !== "POST" || wrote.url !== "/api/vocabulary/priorities") {
    throw new Error("the section sent " + wrote.method + " " + wrote.url);
  }
  if (wrote.headers["Content-Type"] !== "application/json") {
    throw new Error("the change did not name its media type: " + JSON.stringify(wrote.headers));
  }
  const want = { priority: "whenever", label: "Whenever", after: "soon", expectedHead: "head-7" };
  if (JSON.stringify(wrote.body) !== JSON.stringify(want)) {
    throw new Error("the change sent " + JSON.stringify(wrote.body) + ", want " + JSON.stringify(want));
  }
  if (panelPriorities().join(",") !== "urgent,soon,whenever,low") {
    throw new Error("the section is drawing " + panelPriorities().join(",") + " after the change");
  }
  const said = priorityMessages();
  if (said.length !== 2) throw new Error("the section said " + JSON.stringify(said));
  if (said[0].indexOf("whenever") < 0) throw new Error("the section did not report the change: " + said[0]);
  if (said[1].indexOf("workbook docs update") < 0) throw new Error("the warning was swallowed: " + JSON.stringify(said));
  // The board is told its cards are drawn against an older configuration, and
  // not one of its columns is rebuilt.
  if (vocabularyNotice.hidden !== false) throw new Error("the board was not told its priorities are out of date");
`)
}

// A Save that renames a priority and takes the default is TWO requests, in
// order: the edit, then the role, addressed at the NEW name and composed against
// the head the edit answered with.
func TestClientPrioritiesSectionChainsARenameAndTheDefault(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "renaming a priority and taking the default", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPriorities(t), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPrioritiesWithTheDefaultMoved(t), "head-9", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  const name = findElement(form, (element) => element.id === "priority-name-urgent");
  const label = findElement(form, (element) => element.id === "priority-label-urgent");
  const role = findElement(form, (element) => element.dataset.priorityDefault === "urgent");
  if (!role) throw new Error("the edit form offers no way to make this the default");
  if (role.checked !== false) throw new Error("a priority that does not hold the role was drawn holding it");
  name.value = "critical";
  label.value = "Critical";
  role.checked = true;
  await submitPriorityForm(form);

  if (priorityCalls.length !== 2) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  const edit = priorityCalls[0];
  if (edit.method !== "PATCH" || edit.url !== "/api/vocabulary/priorities/urgent") {
    throw new Error("the edit went to " + edit.method + " " + edit.url);
  }
  const wantEdit = { name: "critical", label: "Critical", expectedHead: "head-7" };
  if (JSON.stringify(edit.body) !== JSON.stringify(wantEdit)) {
    throw new Error("the edit sent " + JSON.stringify(edit.body) + ", want " + JSON.stringify(wantEdit));
  }
  // The NEW name: the old one is a 404 here, because a rename forwards a name
  // for the tasks filed under it and does not answer as an address.
  const role2 = priorityCalls[1];
  if (role2.method !== "PATCH" || role2.url !== "/api/vocabulary/priorities/critical/default") {
    throw new Error("the role change went to " + role2.method + " " + role2.url);
  }
  // And the head the FIRST answered with, not the one the form opened against.
  const wantRole = { expectedHead: "head-8" };
  if (JSON.stringify(role2.body) !== JSON.stringify(wantRole)) {
    throw new Error("the role change sent " + JSON.stringify(role2.body) + ", want " + JSON.stringify(wantRole));
  }

  const listed = panelPriorities();
  if (listed.join(",") !== "critical,soon,low") throw new Error("the section is drawing " + listed.join(","));
  const chips = findElements(priorityRow("critical"), (element) => Boolean(element.dataset.priorityTag));
  if (chips.length !== 1) throw new Error("the renamed priority is not drawn holding the role it took");
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("Critical") < 0) {
    throw new Error("the section said " + JSON.stringify(said) + " about a chain that landed");
  }
`)
}

// The half-landed chain. The rename answers 200 and the role change is refused:
// the rename stands, the role did not move, and there is no compensating write.
// The section reports what landed in the refusal's own words and sends nothing
// more; the row it was made on has already been redrawn, so reopening its form
// offers the operation that did not land and not the one that did.
func TestClientPrioritiesSectionReportsARenameThatKeptTheDefaultWhereItWas(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "half-landing a rename and a default", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPriorities(t), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  priorityAnswers.push({ ok: false, body: `+priorityStaleWriteJSON(t, vocabulary, renamedPriorities(t), "head-11",
		"this project's priorities have changed since head-8; reload and try again")+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-name-urgent").value = "critical";
  findElement(form, (element) => element.id === "priority-label-urgent").value = "Critical";
  findElement(form, (element) => element.dataset.priorityDefault === "urgent").checked = true;
  await submitPriorityForm(form);

  if (priorityCalls.length !== 2) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  // Nothing else went. There is no write that undoes the rename, and a board
  // authoring a change nobody asked for is worse than one that says what
  // happened.
  const said = priorityMessages();
  if (said.length !== 1) throw new Error("the section said " + JSON.stringify(said));
  if (said[0].indexOf("Critical") < 0) {
    throw new Error("the report does not say the rename landed: " + said[0]);
  }
  if (said[0].indexOf("changed in another clone") < 0) {
    throw new Error("the report does not say why the role did not move: " + said[0]);
  }
  if (priorityPanelStatus.dataset.kind !== "error") {
    throw new Error("a half-landed change was reported as a success");
  }
  // The section is showing the configuration the refusal carried: the rename is
  // recorded and the role is where it was.
  if (panelPriorities().join(",") !== "critical,soon,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }
  const held = findElements(priorityRow("soon"), (element) => Boolean(element.dataset.priorityTag));
  if (held.length !== 1) throw new Error("the role was drawn as having moved when it did not");

  // Making the change again offers only the operation that did not land: the
  // row's form is built from the priorities as they now stand, so the rename is
  // not re-sent — which would be a second and more confusing refusal.
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPrioritiesWithTheDefaultMoved(t), "head-12", VocabularyPriorityTaskCounts{}, nil)+` });
  await openPriorityForm("critical", "Edit Critical");
  const again = priorityForm("critical", "priorityEdit");
  if (findElement(again, (element) => element.id === "priority-name-critical").value !== "critical") {
    throw new Error("the reopened form is still holding the name the rename replaced");
  }
  findElement(again, (element) => element.dataset.priorityDefault === "critical").checked = true;
  await submitPriorityForm(again);
  if (priorityCalls.length !== 3) {
    throw new Error("the retry sent " + JSON.stringify(priorityCalls.slice(2).map((call) => call.method + " " + call.url)));
  }
  const retried = priorityCalls[2];
  if (retried.url !== "/api/vocabulary/priorities/critical/default" || retried.body.expectedHead !== "head-11") {
    throw new Error("the retry sent " + retried.method + " " + retried.url + " " + JSON.stringify(retried.body));
  }
`)
}

// Reordering names one neighbor, because the move route takes one anchor and
// there is no whole-order counterpart for priorities. Up places the priority
// before the one above it; Down places it after the one below.
func TestClientPrioritiesSectionReordersByNamingANeighbor(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	moved, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "soon", Label: "Soon", Rank: "1/2", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	runPriorityPanelClient(t, "reordering the priorities", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, moved, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  // The ends have nowhere to go, exactly as a status row's do.
  if (panelControl(priorityRow("urgent"), "Move Drop everything earlier").disabled !== true) {
    throw new Error("the first priority was offered a way to move earlier");
  }
  if (panelControl(priorityRow("low"), "Move Low later").disabled !== true) {
    throw new Error("the last priority was offered a way to move later");
  }

  await panelControl(priorityRow("soon"), "Move Soon earlier").eventListeners.click();
  await settle();
  if (priorityCalls.length !== 1) throw new Error("one gesture sent " + priorityCalls.length + " requests");
  const wrote = priorityCalls[0];
  if (wrote.method !== "PATCH" || wrote.url !== "/api/vocabulary/priorities/soon/position") {
    throw new Error("the move went to " + wrote.method + " " + wrote.url);
  }
  const want = { before: "urgent", expectedHead: "head-7" };
  if (JSON.stringify(wrote.body) !== JSON.stringify(want)) {
    throw new Error("the move sent " + JSON.stringify(wrote.body) + ", want " + JSON.stringify(want));
  }
  if (panelPriorities().join(",") !== "soon,urgent,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }

  // And the other direction names the neighbor below.
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, priorities, "head-9", VocabularyPriorityTaskCounts{}, nil)+` });
  await panelControl(priorityRow("soon"), "Move Soon later").eventListeners.click();
  await settle();
  const second = priorityCalls[1];
  const wantSecond = { after: "urgent", expectedHead: "head-8" };
  if (JSON.stringify(second.body) !== JSON.stringify(wantSecond)) {
    throw new Error("the second move sent " + JSON.stringify(second.body) + ", want " + JSON.stringify(wantSecond));
  }
`)
}

// A drag reorders too, and it is the same decision the neighbor-naming controls
// make.
//
// The move route takes one anchor rather than a whole order, and that is what a
// drop already is. The rule the statuses read a drop by — a row dropped on
// another takes that row's place, landing after it when it came from above and
// before it when it came from below — names exactly one neighbor and one side,
// which is {after: target} one way and {before: target} the other. So the
// absence of a whole-order counterpart costs this section nothing: a drag sends
// the same one PATCH Up and Down send.
func TestClientPrioritiesSectionReordersByDraggingARow(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	// Dropped on the last row from above, the priority lands after it.
	dropped, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
		{Priority: "urgent", Label: "Drop everything", Rank: "4/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	runPriorityPanelClient(t, "dragging a priority into place", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, dropped, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  // A row whose form is open is not draggable, for the reason a status row's is
  // not: selecting the text in an input is a press and a drag, and inside a
  // draggable row that is the gesture that reorders the list. Up and Down still
  // move it, which is the path a keyboard has always taken.
  await openPriorityForm("urgent", "Edit Drop everything");
  if (priorityRow("urgent").draggable !== false) throw new Error("a priority being edited is still draggable");
  if (priorityRow("soon").draggable !== true) throw new Error("an ordinary priority row is not draggable");
  await panelControl(priorityForm("urgent", "priorityEdit"), "Stop editing Drop everything").eventListeners.click();
  await settle();

  // Downward: dropped on the row below it, it lands after that row.
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const falling = priorityRow("urgent");
  const below = priorityRow("low");
  falling.eventListeners.dragstart({ target: falling, dataTransfer });
  if (falling.dataset.dragging !== "true") throw new Error("the dragged row does not say it is being dragged");
  below.eventListeners.dragover({ target: below, dataTransfer, preventDefault() {} });
  if (below.dataset.dropTarget !== "true") throw new Error("the row under the cursor is not marked");
  await below.eventListeners.drop({ target: below, dataTransfer, preventDefault() {} });
  await settle();

  if (priorityCalls.length !== 1) throw new Error("one drag sent " + priorityCalls.length + " requests");
  const first = priorityCalls[0];
  if (first.method !== "PATCH" || first.url !== "/api/vocabulary/priorities/urgent/position") {
    throw new Error("the drag went to " + first.method + " " + first.url);
  }
  const wantFirst = { after: "low", expectedHead: "head-7" };
  if (JSON.stringify(first.body) !== JSON.stringify(wantFirst)) {
    throw new Error("the downward drag sent " + JSON.stringify(first.body) + ", want " + JSON.stringify(wantFirst));
  }
  if (panelPriorities().join(",") !== "soon,low,urgent") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }

  // Upward: dropped on the row above it, it lands before that row — and with no
  // dragover anywhere in the gesture, because a dragenter is the one word a
  // browser sends once what is under the cursor churns.
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, priorities, "head-9", VocabularyPriorityTaskCounts{}, nil)+` });
  const rising = priorityRow("urgent");
  const above = priorityRow("soon");
  rising.eventListeners.dragstart({ target: rising, dataTransfer });
  let answered = false;
  above.eventListeners.dragenter({ target: above, dataTransfer, preventDefault() { answered = true; } });
  if (!answered) throw new Error("a dragenter over a row the move may land on was not answered");
  if (above.dataset.dropTarget !== "true") throw new Error("a dragenter drew no mark on the row under the cursor");
  await above.eventListeners.drop({ target: above, dataTransfer, preventDefault() {} });
  await settle();

  if (priorityCalls.length !== 2) throw new Error("the second drag sent " + (priorityCalls.length - 1) + " requests");
  const second = priorityCalls[1];
  if (second.url !== "/api/vocabulary/priorities/urgent/position") {
    throw new Error("the upward drag went to " + second.method + " " + second.url);
  }
  const wantSecond = { before: "soon", expectedHead: "head-8" };
  if (JSON.stringify(second.body) !== JSON.stringify(wantSecond)) {
    throw new Error("the upward drag sent " + JSON.stringify(second.body) + ", want " + JSON.stringify(wantSecond));
  }
  if (panelPriorities().join(",") !== "urgent,soon,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }
`)
}

// A drop this section refuses is left alone entirely: not marked, not
// prevented, and above all not turned into a move.
//
// The flag that says "I will take this" is the same flag that stops the drop
// reaching whatever else on the page wants it, so a section that accepted a
// file drag would take a reader's file and do nothing with it. And a drag this
// section believes is live — a gesture whose dragend never arrived — is exactly
// how a file dropped here would otherwise become a reorder of a priority nobody
// touched. The statuses' rows are asked the same question by their own rule, so
// neither list answers for the other's gesture.
func TestClientPrioritiesSectionLeavesARefusedDropAlone(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "a drop the priorities refuse", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const withFiles = { effectAllowed: "", dropEffect: "", setData() {}, types: ["Files"] };
  const answer = (name, row, transfer) => {
    let prevented = false;
    transfer.dropEffect = "";
    row.eventListeners[name]({ target: row, dataTransfer: transfer, preventDefault() { prevented = true; } });
    return prevented + "/" + transfer.dropEffect;
  };

  const dragged = priorityRow("urgent");
  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });

  // Every priority row is a drop target. The rows below that answer "false/"
  // are the ones a handler asking the easier question — "is there a row here?"
  // — would wrongly accept.
  [
    ["another row, with a move in flight", priorityRow("soon"), dataTransfer, "true/move"],
    ["the row being dragged, which has nowhere to arrive", dragged, dataTransfer, "false/"],
    ["a row while files are being dragged in", priorityRow("soon"), withFiles, "false/"],
    ["a status row, which this gesture is no business of", panelRow("icebox"), dataTransfer, "false/"],
  ].forEach(([what, row, transfer, want]) => {
    const entered = answer("dragenter", row, transfer);
    const over = answer("dragover", row, transfer);
    if (entered !== want) throw new Error("dragenter on " + what + " answered " + entered + ", want " + want);
    if (over !== want) throw new Error("dragover on " + what + " answered " + over + ", want " + want);
  });

  // A file dropped over a move the page still believes is live.
  let prevented = false;
  const soon = priorityRow("soon");
  await soon.eventListeners.drop({ target: soon, dataTransfer: withFiles, preventDefault() { prevented = true; } });
  await settle();
  if (prevented) throw new Error("the section took the drop of a file");

  // A row dropped on itself is refused rather than half-handled, and the
  // gesture's own dragend is what clears the drag.
  await dragged.eventListeners.drop({ target: dragged, dataTransfer, preventDefault() { prevented = true; } });
  await settle();
  if (prevented) throw new Error("a row dropped on itself was taken as a move");

  dragged.eventListeners.dragend({ target: dragged });
  ["urgent", "soon", "low"].forEach((priority) => {
    if (priorityRow(priority).dataset.dropTarget === "true") {
      throw new Error("dragend left " + priority + " marked as a drop target");
    }
  });

  // And with no move in flight at all — somebody else's gesture passing over
  // the list, a status row's drag among them — every row refuses.
  ["urgent", "soon", "low"].forEach((priority) => {
    const row = priorityRow(priority);
    const entered = answer("dragenter", row, dataTransfer);
    const over = answer("dragover", row, dataTransfer);
    if (entered !== "false/") throw new Error("dragenter on " + priority + " with no move in flight answered " + entered);
    if (over !== "false/") throw new Error("dragover on " + priority + " with no move in flight answered " + over);
  });

  const column = panelRow("shipped");
  column.eventListeners.dragstart({ target: column, dataTransfer });
  const landing = priorityRow("soon");
  if (answer("dragenter", landing, dataTransfer) !== "false/") {
    throw new Error("a column being dragged was offered a priority row to land on");
  }
  await landing.eventListeners.drop({ target: landing, dataTransfer, preventDefault() { prevented = true; } });
  await settle();
  if (prevented) throw new Error("a column dropped on a priority row was taken as a move");
  column.eventListeners.dragend({ target: column });

  // Nothing was sent, by either section, and the order stands as it was read.
  if (priorityCalls.length !== 0) {
    throw new Error("a refused drop sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("a refused drop sent a statuses request");
  }
  if (panelPriorities().join(",") !== "urgent,soon,low") {
    throw new Error("a refused drop reordered the priorities: " + panelPriorities().join(","));
  }
`)
}

// Moving the role on its own is one request and carries a head and nothing else:
// the priority is the address and the role is the route. The priority that holds
// it has nothing to offer, because there is no operation that clears the role.
func TestClientPrioritiesSectionMovesTheDefaultOnItsOwn(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	moved, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{core.PriorityTagDefault}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	runPriorityPanelClient(t, "moving the default", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, moved, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  // The priority that already carries the role is drawn carrying it and is not
  // offered a way to give up something nothing can take.
  await openPriorityForm("soon", "Edit Soon");
  const holder = findElement(priorityForm("soon", "priorityEdit"), (element) => element.dataset.priorityDefault === "soon");
  if (holder.checked !== true) throw new Error("the priority holding the role was not drawn holding it");
  if (holder.disabled !== true) throw new Error("the holder was offered a way to clear a role nothing clears");
  await openPriorityForm("soon", "Edit Soon");

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.dataset.priorityDefault === "urgent").checked = true;
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) throw new Error("the Save sent " + priorityCalls.length + " requests");
  const wrote = priorityCalls[0];
  if (wrote.method !== "PATCH" || wrote.url !== "/api/vocabulary/priorities/urgent/default") {
    throw new Error("the role change went to " + wrote.method + " " + wrote.url);
  }
  if (JSON.stringify(wrote.body) !== JSON.stringify({ expectedHead: "head-7" })) {
    throw new Error("the role change sent " + JSON.stringify(wrote.body));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("Drop everything") < 0) {
    throw new Error("the section said " + JSON.stringify(said));
  }
`)
}

// Removing a priority names where its tasks belong and never guesses, and the
// report prices the removal in the one term a priority removal has: the tasks
// that moved. Not in claimability — that is a status property, and the number
// would be zero forever.
func TestClientPrioritiesSectionRemovesAPriorityIntoAnother(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	shorter, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, []core.PriorityAlias{{From: "urgent", To: "soon"}}, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	runPriorityPanelClient(t, "removing a priority", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, shorter, "head-8", VocabularyPriorityTaskCounts{Affected: 3}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Delete Drop everything");
  const form = priorityForm("urgent", "priorityDelete");
  const into = findElement(form, (element) => element.id === "priority-into-urgent");
  const offered = into.children.map((option) => option.value);
  if (offered.join(",") !== "soon,low") throw new Error("the destinations offered are " + offered.join(","));
  chooseOption(into, "soon");
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) throw new Error("the removal sent " + priorityCalls.length + " requests");
  const wrote = priorityCalls[0];
  if (wrote.method !== "DELETE" || wrote.url !== "/api/vocabulary/priorities/urgent") {
    throw new Error("the removal went to " + wrote.method + " " + wrote.url);
  }
  if (JSON.stringify(wrote.body) !== JSON.stringify({ into: "soon", expectedHead: "head-7" })) {
    throw new Error("the removal sent " + JSON.stringify(wrote.body));
  }
  const said = priorityMessages();
  if (said.length !== 1) throw new Error("the section said " + JSON.stringify(said));
  if (said[0].indexOf("3 tasks moved to Soon") < 0) {
    throw new Error("the removal was not priced in the tasks that moved: " + said[0]);
  }
  if (said[0].indexOf("claimable") >= 0) {
    throw new Error("the removal was priced in claimability, which is not a priority's business: " + said[0]);
  }
  if (panelPriorities().join(",") !== "soon,low") throw new Error("the section is drawing " + panelPriorities().join(","));
`)
}

// A refusal that is not a conflict is the reader's own to read, so it is quoted
// exactly as the command would have printed it — including the one this section
// is built to produce: a Save with nothing in it that reached the server anyway.
func TestClientPrioritiesSectionQuotesARefusalItDidNotMake(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "quoting a refused priority change", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ ok: false, body: `+panelRefusalJSON(t, core.CategoryInvocation,
		`priority "urgent" already has that label`)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-label-urgent").value = "Drop everything now";
  await submitPriorityForm(form);

  const said = priorityMessages();
  if (said.length !== 1 || said[0] !== 'priority "urgent" already has that label') {
    throw new Error("the section said " + JSON.stringify(said) + " instead of what the server said");
  }
  if (priorityPanelStatus.dataset.kind !== "error") throw new Error("a refusal was reported as a success");
  // Nothing was adopted: the section is still drawing what it read.
  if (panelPriorities().join(",") !== "urgent,soon,low") throw new Error("the section is drawing " + panelPriorities().join(","));
  // And the statuses section beside it was not made to speak for a change that
  // has nothing to do with it.
  if (panelMessages().length !== 0) throw new Error("the statuses section said " + JSON.stringify(panelMessages()));
`)
}

// A priority change waits for the board's own writes to settle, for the reason a
// status change does: a pending intent was composed against this configuration,
// and moving the ledger tip out from under it would have it refused as a stale
// write nothing was actually wrong with. While it waits, both other sections'
// controls are disabled too — one ledger, one tip.
func TestClientPrioritiesSectionWaitsForPendingBoardChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
	}
	runPriorityPanelClient(t, "waiting for the board", vocabulary, priorities, "head-7", tasks, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, renamedPriorities(t), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  // Hold the board's write open with a drop, then walk to the configuration
  // page: the queue is non-empty and stays that way.
  let releaseTaskWrite = null;
  taskWriteGate = new Promise((resolve) => { releaseTaskWrite = resolve; });
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  const dragged = boardCard("WB-01J0000000000000000000A101");
  dragged.rect = { top: 0, bottom: 80 };
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  const dropped = documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });
  await settle();
  if (fetchCalls.filter((call) => call.method === "PATCH").length !== 1) {
    throw new Error("the board write did not go out");
  }

  await openStatuses();
  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-name-urgent").value = "critical";
  const sending = form.eventListeners.submit({ preventDefault() {} });
  await settle();
  if (priorityCalls.length !== 0) throw new Error("the change went while a task write was in flight");
  const waiting = priorityMessages();
  if (waiting.length !== 1 || waiting[0].indexOf("already in flight") < 0) {
    throw new Error("the section did not say it was waiting: " + JSON.stringify(waiting));
  }
  // Every section's controls, not only this one's.
  if (panelControl(priorityRow("soon"), "Move Soon earlier").disabled !== true) {
    throw new Error("the priorities section kept its controls live while a change was in flight");
  }
  if (panelControl(panelRow("icebox"), "Edit Icebox").disabled !== true) {
    throw new Error("a priority change left the statuses section's controls live");
  }
  releaseTaskWrite();
  await dropped;
  await sending;
  await settle();
  if (priorityCalls.length !== 1) throw new Error("the change did not go once the board settled");
  if (priorityCalls[0].body.expectedHead !== "head-7") {
    throw new Error("the change named the head " + JSON.stringify(priorityCalls[0].body.expectedHead));
  }
`)
}

// recoloredPriorities is configuredPriorities with urgent drawn in `color`. An
// empty one is the priority with nothing stored, which is what clearing leaves:
// there is no stored default to go back to, so the board derives an ink from
// the position instead.
func recoloredPriorities(t *testing.T, color string) core.PriorityVocabulary {
	t.Helper()
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: color},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// The section draws a color control now, so the recolor joins the capabilities
// its gate counts. A board wired for the other five but not for this one would
// serve a form with a field on it that could only ever answer "this board has no
// such capability", which is the thing the gate exists to prevent.
func TestHandlerConfigWithholdsThePrioritiesSectionFromABoardThatCannotRecolor(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	options := prioritiesAdministrableOptions(vocabulary, configuredPriorities(t), "head-1", nil)
	options.RecolorPriority = nil

	served := request(t, NewHandler(options), http.MethodGet, "/config")
	if served.Code != http.StatusOK {
		t.Fatalf("GET /config status = %d, want %d", served.Code, http.StatusOK)
	}
	if body := served.Body.String(); strings.Contains(body, priorityPanelMarkup) {
		t.Error("a board that cannot recolor a priority served the priorities section anyway")
	}
}

// Choosing what color a priority is drawn in, which is what this section was
// built toward.
//
// The field is the change and the well is the way into it, exactly as the board
// settings form's two color fields work: picking writes into the field, and the
// Save reads the field. The color is its own route against its own head, so a
// Save that only recolors is one request.
func TestClientPrioritiesSectionSetsThePriorityColor(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "recoloring a priority", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, recoloredPriorities(t, "#7c3aed"), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  const color = findElement(form, (element) => element.id === "priority-color-urgent");
  if (!color) throw new Error("the edit form offers no way to change the color");
  if (color.value !== "#b42318") throw new Error("the color field opened on " + JSON.stringify(color.value));
  const well = findElement(form, (element) => element.dataset.displayWell === "priority-color-urgent");
  if (!well) throw new Error("the color field has no well beside it");
  if (well.type !== "color") throw new Error("the well is a " + well.type + " input rather than a color input");
  if (well.value !== "#b42318") throw new Error("the well opened on " + JSON.stringify(well.value));
  // Picking writes into the field, which stays what the Save reads.
  well.value = "#7c3aed";
  well.eventListeners.input();
  if (color.value !== "#7c3aed") throw new Error("picking left the field at " + JSON.stringify(color.value));
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  const recolor = priorityCalls[0];
  if (recolor.method !== "PATCH" || recolor.url !== "/api/vocabulary/priorities/urgent/color") {
    throw new Error("the recolor went to " + recolor.method + " " + recolor.url);
  }
  const want = { color: "#7c3aed", expectedHead: "head-7" };
  if (JSON.stringify(recolor.body) !== JSON.stringify(want)) {
    throw new Error("the recolor sent " + JSON.stringify(recolor.body) + ", want " + JSON.stringify(want));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("Drop everything") < 0) {
    throw new Error("the section said " + JSON.stringify(said) + " about a recolor that landed");
  }
  // Re-opening the row offers the color the answer carried, not the one the
  // form was opened with — the row was redrawn from the adopted document.
  await openPriorityForm("urgent", "Edit Drop everything");
  const again = priorityForm("urgent", "priorityEdit");
  if (findElement(again, (element) => element.id === "priority-color-urgent").value !== "#7c3aed") {
    throw new Error("the re-opened form is still holding the color the recolor replaced");
  }
  // The board's per-priority ink is still a stylesheet rendered for the
  // priorities the page was served with — but it is no longer only the server
  // that renders it. The recolor's answer carries the stylesheet the server
  // would have served now, and the page swaps it in place without touching a
  // card, so the ink under the columns the reader left behind is already the new
  // one. The standing notice is for what only a reload can redraw, which a color
  // is not, so a recolor says nothing.
  if (boardPriorityInkStyle.textContent.indexOf("#7c3aed") < 0) {
    throw new Error("the recolor never reached the board's ink, so the silence below proves nothing");
  }
  if (vocabularyNotice.hidden !== true) {
    throw new Error("a recolor told the reader to reload for ink the page had already swapped in");
  }
`)
}

// Clearing takes the priority back to the color its position derives. The route
// requires the member and reads an empty one as the clearing, so an emptied
// field is sent rather than withheld — a Save that dropped the member would be
// refused for naming no color at all.
func TestClientPrioritiesSectionClearsAColorBackToTheOneItsPositionDerives(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "clearing a priority's color", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, recoloredPriorities(t, ""), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-color-urgent").value = "";
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  const cleared = priorityCalls[0];
  if (cleared.url !== "/api/vocabulary/priorities/urgent/color") {
    throw new Error("the clearing went to " + cleared.method + " " + cleared.url);
  }
  const want = { color: "", expectedHead: "head-7" };
  if (JSON.stringify(cleared.body) !== JSON.stringify(want)) {
    throw new Error("the clearing sent " + JSON.stringify(cleared.body) + ", want " + JSON.stringify(want));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("position") < 0) {
    throw new Error("the section said " + JSON.stringify(said) + " about a color it cleared");
  }
  // The re-opened row holds nothing, and its well is back at the control's own
  // black default rather than at a color of the board's: the derived ink is the
  // stylesheet's, and this script keeps no copy of it.
  await openPriorityForm("urgent", "Edit Drop everything");
  const again = priorityForm("urgent", "priorityEdit");
  if (findElement(again, (element) => element.id === "priority-color-urgent").value !== "") {
    throw new Error("the re-opened form is still holding the color that was cleared");
  }
  if (findElement(again, (element) => element.dataset.displayWell === "priority-color-urgent").value !== "#000000") {
    throw new Error("the well of a cleared color opened on a color of the board's own");
  }
`)
}

// The decision resolution 4 left open: a color field holding nothing but spaces
// is the clearing the writer would make of it, not a refusal this client
// invents.
//
// The writer trims before it reads, so "   " on a colored priority already
// clears it and answers 200; a panel that refused it here would be refusing in
// words of its own, which is the one thing this section does not do. What the
// field does decide for itself is whether the Save is a change at all, and it
// decides that on the trimmed value — so spaces typed into the empty field of a
// priority that has no color are not a clearing of nothing, and send nothing.
func TestClientPrioritiesSectionTreatsABlankColorFieldAsTheClearTheWriterMakesOfIt(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "blanking a priority's color", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, recoloredPriorities(t, ""), "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-color-urgent").value = "   ";
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  if (priorityCalls[0].url !== "/api/vocabulary/priorities/urgent/color") {
    throw new Error("the Save went to " + priorityCalls[0].url);
  }
  if (panelPriorities().join(",") !== "urgent,soon,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }

  // And the same spaces in the field of a priority that has no color are no
  // change at all: there is nothing to clear, and the first priority write on a
  // project costs it the compatibility marker, so a Save that changes nothing
  // must not become one.
  await openPriorityForm("urgent", "Edit Drop everything");
  const again = priorityForm("urgent", "priorityEdit");
  findElement(again, (element) => element.id === "priority-color-urgent").value = "  ";
  await submitPriorityForm(again);
  if (priorityCalls.length !== 1) {
    throw new Error("a blank field over a priority with no color sent " +
      JSON.stringify(priorityCalls.slice(1).map((call) => call.method + " " + call.url)));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0].indexOf("Nothing about") !== 0) {
    throw new Error("the section said " + JSON.stringify(said) + " about a Save with nothing in it");
  }
`)
}

// Setting the color a priority already has is refused, and the refusal is the
// reader's own to read.
//
// The client does not route around it. The first priority write on a project
// backfills the built-in three and stamps a compatibility marker that parks
// every teammate on an older build, so a change that changes nothing must not
// cost a team that — the 400 is the writer protecting them, and it reaches the
// page in the writer's own sentence rather than as a failure this client worded.
//
// It is reachable because the field is compared as it was typed: the writer
// lowercases before it compares, this panel does not canonicalize before it
// sends, and so a differently-cased spelling of the stored color is a change to
// the form and no change to the ledger.
func TestClientPrioritiesSectionQuotesTheRefusalOfAColorAPriorityAlreadyHas(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "re-sending the color a priority has", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ ok: false, body: `+panelRefusalJSON(t, core.CategoryInvocation,
		`priority "urgent" already has that color`)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-color-urgent").value = "#B42318";
  await submitPriorityForm(form);

  if (priorityCalls.length !== 1) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  // As typed. Nothing here canonicalizes: what a color is belongs to the verb
  // family that validates it.
  if (priorityCalls[0].body.color !== "#B42318") {
    throw new Error("the recolor sent " + JSON.stringify(priorityCalls[0].body.color));
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0] !== 'priority "urgent" already has that color') {
    throw new Error("the section said " + JSON.stringify(said) + " instead of what the server said");
  }
  if (panelPriorities().join(",") !== "urgent,soon,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }
`)
}

// A color that is not one is refused before anything is written.
//
// That is what the recolor being the first of this Save's requests buys. The
// color field is the only free-form value in this form whose shape the writer
// checks, and a Save carrying a typo alongside a rename would otherwise record
// the rename and then be refused — a half-landed change, with no compensating
// write, over a mistyped hex code. Sending the color first means a typo costs
// the reader a second press and nothing else.
//
// The sentence is the writer's, because nothing here knows what a color is.
func TestClientPrioritiesSectionWritesNothingWhenTheColorIsMalformed(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	runPriorityPanelClient(t, "a malformed color beside a rename", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ ok: false, body: `+panelRefusalJSON(t, core.CategoryInvocation,
		`color "puce" must be six hexadecimal digits behind a hash, as in #1a7f4b`)+` });
  await openStatuses();

  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  findElement(form, (element) => element.id === "priority-name-urgent").value = "critical";
  findElement(form, (element) => element.id === "priority-label-urgent").value = "Critical";
  findElement(form, (element) => element.dataset.priorityDefault === "urgent").checked = true;
  findElement(form, (element) => element.id === "priority-color-urgent").value = "puce";
  await submitPriorityForm(form);

  // One request, and it is the color. The rename and the role never went.
  if (priorityCalls.length !== 1) {
    throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  }
  if (priorityCalls[0].url !== "/api/vocabulary/priorities/urgent/color") {
    throw new Error("the first request of the Save was " + priorityCalls[0].method + " " + priorityCalls[0].url);
  }
  const said = priorityMessages();
  if (said.length !== 1 || said[0] !== 'color "puce" must be six hexadecimal digits behind a hash, as in #1a7f4b') {
    throw new Error("the section said " + JSON.stringify(said) + " instead of what the server said");
  }
  if (priorityPanelStatus.dataset.kind !== "error") throw new Error("a refusal was reported as a success");
  // Nothing moved: the ledger was not written to, and the section is drawing
  // what it read.
  if (panelPriorities().join(",") !== "urgent,soon,low") {
    throw new Error("the section is drawing " + panelPriorities().join(","));
  }
  const held = findElements(priorityRow("soon"), (element) => Boolean(element.dataset.priorityTag));
  if (held.length !== 1) throw new Error("the role moved on a Save that was refused");
`)
}
