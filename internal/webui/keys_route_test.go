package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the keys section of the configuration route does, and what the create
// form does with the keys it reads.
//
// It is the statuses section's third sibling: the same read on entry, the same
// head discipline, the same busy/quiescence protocol, the same wholesale
// adoption of whatever a change answers with, the same refusal quoted in its own
// live region. What is pinned here is what a key is that a status and a priority
// are not.
//
//   - There is no order to choose. A key has no rank and the fold only ever
//     appends, so the list is add order and the section offers no reorder, no
//     drag, no Up and no Down.
//   - A key is added and then only ever changes state. A row draws "Make
//     current", "Retire" or "Reactivate" according to what the key is, and the
//     current key draws its badge and nothing else — there is no operation that
//     takes the minting away from a key without giving it to another.
//   - And the one the create form is written around: the keys are deliberately
//     NOT in the vocabulary shape, so no board is ever asked to reload for a key
//     change. The chooser therefore reads the keys at the moment it is drawn,
//     out of the last vocabulary this page received, rather than a set captured
//     when the page loaded.

// keysAdministrableHandler is a board wired for the statuses, the priorities,
// the board settings and the two key mutations, which is what `workbook serve`
// builds and the only kind whose configuration route carries a keys section. The
// mutations are never reached: the client tests answer the routes from the fake
// fetch, and what the routes do with a request is keys_test.go's subject.
func keysAdministrableHandler(
	t *testing.T,
	vocabulary core.Vocabulary,
	keys core.KeySet,
	head string,
	tasks []core.Task,
) http.Handler {
	t.Helper()
	return NewHandler(keysAdministrableOptions(vocabulary, keys, head, tasks))
}

// keysAdministrableOptions is that board's wiring, held out so a test can
// withhold one key capability from it and ask what the configuration page does
// then.
//
// The priorities are the built-in three, which is what a project that has
// configured none is served and what the shared DOM harness publishes: every
// test in this file is about keys, and a board drawing priorities its own
// handler does not serve would raise the vocabulary notice over a change nobody
// made.
func keysAdministrableOptions(
	vocabulary core.Vocabulary,
	keys core.KeySet,
	head string,
	tasks []core.Task,
) Options {
	options := prioritiesAdministrableOptions(vocabulary, core.PriorityVocabulary{}, head, tasks)
	options.Vocabulary = func(context.Context) (VocabularyState, error) {
		return VocabularyState{Vocabulary: vocabulary, Head: head, Keys: keys}, nil
	}
	options.AddKey = func(context.Context, VocabularyKeyAddition) (VocabularyKeyMutation, error) {
		return VocabularyKeyMutation{}, nil
	}
	options.EditKey = func(context.Context, string, VocabularyKeyEdit) (VocabularyKeyMutation, error) {
		return VocabularyKeyMutation{}, nil
	}
	return options
}

// keyFetchHarness answers the two key routes from a queue, so one test can drive
// a pair of changes whose second answer differs from its first. Everything else
// falls through to the statuses harness this runs on top of — including GET
// /api/vocabulary, which is the one read all four sections come out of.
const keyFetchHarness = `
const keyCalls = [];
const keyAnswers = [];
const beneathKeyFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  const method = (options.method || "GET").toUpperCase();
  if (url.startsWith("/api/vocabulary/keys")) {
    const call = { url, method, headers: options.headers || {},
      body: options.body === undefined ? null : JSON.parse(options.body) };
    keyCalls.push(call);
    fetchCalls.push(call);
    const answer = keyAnswers.shift();
    if (!answer) throw new Error("the section sent " + method + " " + url + " with no answer prepared");
    return { ok: answer.ok !== false, json: async () => answer.body };
  }
  return beneathKeyFetch(url, options);
};
// The add-a-key form, and a row's control found the way a reader finds it: by
// the caption it draws or the sentence it announces.
function keyAdd() {
  const form = findElement(keyPanelBody, (element) => hasDataKey(element, "keyAdd"));
  if (!form) throw new Error("the section drew no add-a-key form");
  return form;
}
async function submitKeyForm(form) {
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
}
// Presses a control on a key's row, from the control itself, so a rebuild that
// drops it is a rebuild that dropped the node the press started on.
async function pressKeyControl(key, caption) {
  const control = panelControl(keyPanelRow(key), caption);
  if (!control) throw new Error("the row for " + key + " offers no " + caption);
  control.focus();
  await control.eventListeners.click();
  await settle();
}
// Every control a key's row offers, by caption, so a test can say what a row
// draws rather than only what it does not.
function keyRowControls(key) {
  return findElements(keyPanelRow(key), (element) => element.tagName === "BUTTON")
    .map((control) => control.textContent);
}
// The create form's key chooser, or null on a form that draws none.
function newTaskKeySelect(form) {
  return findElement(form, (element) => element.id === "task-key");
}
`

// runKeyPanelClient renders a board wired for all four configuration sections
// and executes its client script against the fake DOM and the recording fetch.
// `path` is the address the reader arrived at: "/" for the board they walk to
// the configuration page from, "/tasks/new" for the create form.
//
// The two key attributes the server renders are restated from the key set this
// handler was built with, for the reason the priorities section restates its
// three: the shared harness publishes what a board with no key resolver
// publishes — no keys and no current key — and a page disagreeing with the
// handler behind it answers questions about what it happened to be showing.
func runKeyPanelClient(
	t *testing.T,
	purpose, path string,
	keys core.KeySet,
	tasks []core.Task,
	body string,
) {
	t.Helper()
	vocabulary := handlerVocabulary(t)
	const head = "head-1"
	prelude := keyFetchHarness + `
boardView.dataset.keys = ` + strconv.Quote(pageKeys(keys)) + `;
boardView.dataset.currentKey = ` + strconv.Quote(keys.Current()) + `;
`
	runClientOverHandler(t, keysAdministrableHandler(t, vocabulary, keys, head, tasks),
		purpose, path, prelude, vocabulary, head, tasks, body)
}

// keyPanelVocabularyJSON is what GET /api/vocabulary answers for a project with
// these keys, built by the server's own builder.
func keyPanelVocabularyJSON(t *testing.T, keys core.KeySet, head string) string {
	t.Helper()
	return string(mustJSON(t, vocabularyDocument(VocabularyState{
		Vocabulary: handlerVocabulary(t), Head: head, Keys: keys,
	})))
}

// keyPanelMutationJSON is what either key route answers: the whole vocabulary
// document at the new head, and no `tasks` member at all, because a key change
// moves no task.
func keyPanelMutationJSON(t *testing.T, keys core.KeySet, head string, warnings []core.Warning) string {
	t.Helper()
	return string(mustJSON(t, VocabularyKeyMutationDocument{
		Format:  "workbook.key-mutation",
		Version: 1,
		Vocabulary: vocabularyDocument(VocabularyState{
			Vocabulary: handlerVocabulary(t), Head: head, Keys: keys,
		}),
		Warnings: warnings,
	}))
}

// movedKeys is the fixture project after SPARE has taken the minting: WB is
// still retired, NEW is still active, and the current key has moved.
func movedKeys(t *testing.T) core.KeySet {
	t.Helper()
	keys, err := core.NewKeySet(core.KeyDocument{
		Keys: []core.KeyDefinition{
			{Key: "WB", Retired: true},
			{Key: "NEW"},
			{Key: "SPARE"},
		},
		Current: "SPARE",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	return keys
}

// oneKeyProject is a project that has never added a key: the founding key
// alone, active and current, which is what every project has until somebody
// runs `workbook key add`.
func oneKeyProject() core.KeySet {
	return core.FoundingKeySet("WB")
}

// The served shell of the keys section, matched as markup rather than as the
// bare attribute name, because the client script asks for the element by that
// name on every board and so carries the string whether or not it was served.
const keyPanelMarkup = `<div class="admin" data-key-panel`

// A board wired for both key mutations carries the section; one that is not
// carries no markup for it at all, so the client has nothing to draw and draws
// nothing, rather than a heading over controls that could only be refused.
//
// Both capabilities rather than either: the section is one surface, and a board
// that could add a key but never retire one would draw rows whose controls fail
// differently from the form above them.
func TestHandlerConfigCarriesTheKeysSectionOnlyWhenItCanChangeThem(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	keys := projectKeys(t)
	full := keysAdministrableOptions(vocabulary, keys, "head-1", nil)

	withoutAdd := keysAdministrableOptions(vocabulary, keys, "head-1", nil)
	withoutAdd.AddKey = nil
	withoutEdit := keysAdministrableOptions(vocabulary, keys, "head-1", nil)
	withoutEdit.EditKey = nil

	for name, expected := range map[string]struct {
		options Options
		carried bool
	}{
		"both mutations": {full, true},
		"no add":         {withoutAdd, false},
		"no edit":        {withoutEdit, false},
	} {
		response := request(t, NewHandler(expected.options), http.MethodGet, "/config")
		if response.Code != http.StatusOK {
			t.Fatalf("%s: GET /config status = %d, want %d", name, response.Code, http.StatusOK)
		}
		body := response.Body.String()
		if strings.Contains(body, keyPanelMarkup) != expected.carried {
			t.Errorf("%s: /config carries the keys section = %t, want %t",
				name, strings.Contains(body, keyPanelMarkup), expected.carried)
		}
		if !expected.carried {
			continue
		}
		for _, attribute := range []string{
			// Shipped hidden and outside main, like its three siblings: the
			// render for the route is what mounts it.
			`hidden`,
			// Focusable without being tabbable, and named, because a rebuild
			// parks focus on it and a generic role can hold no name.
			`tabindex="-1"`,
			`role="group"`,
			`aria-labelledby="keys-title"`,
			`data-key-panel-status`,
			`data-key-panel-body`,
		} {
			if !strings.Contains(body, attribute) {
				t.Errorf("%s: the keys section does not carry %q", name, attribute)
			}
		}
	}
}

// Every board publishes its keys and its current key, administrable or not: the
// create form's chooser reads them out of the page before anything has been
// fetched, and a board nobody can administer still mints tasks.
func TestHandlerBoardPublishesTheProjectsKeys(t *testing.T) {
	keys := projectKeys(t)
	response := request(t, keysAdministrableHandler(t, handlerVocabulary(t), keys, "head-1", nil),
		http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		`data-current-key="NEW"`,
		"&#34;key&#34;:&#34;SPARE&#34;",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the board does not publish %q", fragment)
		}
	}
}

// The section lists every key this project has, in add order, saying what each
// one is — and offers each row exactly the changes that key can undergo.
//
// Retired keys are listed rather than hidden. A retired key is still this
// project's key: every task ID minted under it keeps it, because a task ID is a
// permanent name, so a reader looking at this list has to be able to see it
// saying so and to bring it back.
func TestClientKeysSectionListsEveryKeyWithItsState(t *testing.T) {
	runKeyPanelClient(t, "listing this project's keys", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  const listed = panelKeys();
  if (listed.join(",") !== "WB,NEW,SPARE") {
    throw new Error("the keys section listed " + JSON.stringify(listed) + ", want add order WB,NEW,SPARE");
  }
  if (keyRowState("WB") !== "retired" || keyRowState("NEW") !== "active" || keyRowState("SPARE") !== "active") {
    throw new Error("the rows do not draw the states the document carries");
  }
  if (!keyRowIsCurrent("NEW")) throw new Error("the current key draws no badge saying so");
  if (keyRowIsCurrent("WB") || keyRowIsCurrent("SPARE")) {
    throw new Error("a key that is not current drew the current badge");
  }
  // The current key has nothing to offer: there is no operation that takes the
  // minting off a key without handing it to another, and retiring it would
  // leave a new task nowhere to go.
  if (keyRowControls("NEW").length !== 0) {
    throw new Error("the current key offered " + JSON.stringify(keyRowControls("NEW")) + ", want nothing but its badge");
  }
  if (keyRowControls("SPARE").join(",") !== "Make current,Retire") {
    throw new Error("the non-current active key offered " + JSON.stringify(keyRowControls("SPARE")));
  }
  if (keyRowControls("WB").join(",") !== "Reactivate") {
    throw new Error("the retired key offered " + JSON.stringify(keyRowControls("WB")));
  }
  // There is no order to choose here, so there is nothing that offers one.
  for (const absent of ["Up", "Down", "Edit", "Delete", "Remove"]) {
    if (keyRowControls("SPARE").includes(absent)) {
      throw new Error("the keys section offers a " + absent + " control; a key has no rank and is never renamed or deleted");
    }
  }
  const row = keyPanelRow("SPARE");
  if (row.draggable) throw new Error("a key row is draggable; add order is history, not an arrangement");
`)
}

// Making another key current is one PATCH naming one intent, against the head
// the section read, and the answer is adopted whole — so the badge moves without
// the page re-reading anything.
//
// And the control the reader pressed is one of the nodes the rebuild drops, so
// focus is caught on the section itself rather than left on the document body.
func TestClientKeysSectionMakesAnotherKeyCurrent(t *testing.T) {
	runKeyPanelClient(t, "moving the minting to another key", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  keyAnswers.push({ body: `+keyPanelMutationJSON(t, movedKeys(t), "head-2", nil)+` });
  await pressKeyControl("SPARE", "Make current");

  if (keyCalls.length !== 1) {
    throw new Error("the section sent " + keyCalls.length + " key requests, want 1");
  }
  const sent = keyCalls[0];
  if (sent.method !== "PATCH" || sent.url !== "/api/vocabulary/keys/SPARE") {
    throw new Error("Make current sent " + sent.method + " " + sent.url);
  }
  if (sent.body.current !== true || "retire" in sent.body || "reactivate" in sent.body) {
    throw new Error("Make current named more than one intent: " + JSON.stringify(sent.body));
  }
  if (sent.body.expectedHead !== "head-1") {
    throw new Error("the change named head " + JSON.stringify(sent.body.expectedHead) + ", want the head it read");
  }
  if (sent.headers["Content-Type"] !== "application/json") {
    throw new Error("the change did not name its media type");
  }

  if (!keyRowIsCurrent("SPARE") || keyRowIsCurrent("NEW")) {
    throw new Error("the answer did not move the current badge");
  }
  if (keyRowControls("SPARE").length !== 0) {
    throw new Error("the key that took the minting still offers controls");
  }
  if (keyRowControls("NEW").join(",") !== "Make current,Retire") {
    throw new Error("the key that lost the minting was not given its controls back");
  }
  if (document.activeElement !== keyPanel) {
    throw new Error("the rebuild dropped the caret instead of parking it on the section");
  }
  // The next change names the head this answer carried, which is the whole of
  // the head discipline: no refetch between two changes.
  keyAnswers.push({ body: `+keyPanelMutationJSON(t, movedKeys(t), "head-3", nil)+` });
  await pressKeyControl("NEW", "Make current");
  if (keyCalls[1].body.expectedHead !== "head-2") {
    throw new Error("the second change named head " + JSON.stringify(keyCalls[1].body.expectedHead));
  }
`)
}

// Retiring a key and bringing one back are the other two intents the per-key
// route carries, and each is one request naming one of them.
func TestClientKeysSectionRetiresAndReactivatesAKey(t *testing.T) {
	retired := func(t *testing.T) core.KeySet {
		t.Helper()
		keys, err := core.NewKeySet(core.KeyDocument{
			Keys: []core.KeyDefinition{
				{Key: "WB", Retired: true},
				{Key: "NEW"},
				{Key: "SPARE", Retired: true},
			},
			Current: "NEW",
		})
		if err != nil {
			t.Fatalf("NewKeySet() error = %v", err)
		}
		return keys
	}
	revived := func(t *testing.T) core.KeySet {
		t.Helper()
		keys, err := core.NewKeySet(core.KeyDocument{
			Keys: []core.KeyDefinition{
				{Key: "WB"},
				{Key: "NEW"},
				{Key: "SPARE", Retired: true},
			},
			Current: "NEW",
		})
		if err != nil {
			t.Fatalf("NewKeySet() error = %v", err)
		}
		return keys
	}
	runKeyPanelClient(t, "retiring a key and bringing one back", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  keyAnswers.push({ body: `+keyPanelMutationJSON(t, retired(t), "head-2", nil)+` });
  await pressKeyControl("SPARE", "Retire");
  if (keyCalls[0].method !== "PATCH" || keyCalls[0].url !== "/api/vocabulary/keys/SPARE" ||
      keyCalls[0].body.retire !== true || "current" in keyCalls[0].body) {
    throw new Error("Retire sent " + keyCalls[0].method + " " + keyCalls[0].url + " " + JSON.stringify(keyCalls[0].body));
  }
  if (keyRowState("SPARE") !== "retired") throw new Error("the answer did not retire the row");
  if (keyRowControls("SPARE").join(",") !== "Reactivate") {
    throw new Error("a retired row offered " + JSON.stringify(keyRowControls("SPARE")));
  }
  // The list is still add order. A retired key does not move to the bottom,
  // because the order is the history rather than an arrangement.
  if (panelKeys().join(",") !== "WB,NEW,SPARE") {
    throw new Error("retiring a key reordered the list: " + JSON.stringify(panelKeys()));
  }

  keyAnswers.push({ body: `+keyPanelMutationJSON(t, revived(t), "head-3", nil)+` });
  await pressKeyControl("WB", "Reactivate");
  if (keyCalls[1].body.reactivate !== true || keyCalls[1].body.expectedHead !== "head-2") {
    throw new Error("Reactivate sent " + JSON.stringify(keyCalls[1].body));
  }
  if (keyRowState("WB") !== "active") throw new Error("the answer did not bring the key back");
  if (keyRowControls("WB").join(",") !== "Make current,Retire") {
    throw new Error("a reactivated row offered " + JSON.stringify(keyRowControls("WB")));
  }
`)
}

// Adding a key is one POST, and "make it current" is a setting on it rather
// than a second request: that is what `workbook key add --current` is, and two
// requests would be two ledger commits for one decision.
func TestClientKeysSectionAddsAKeyAndCanMakeItCurrent(t *testing.T) {
	widened := func(t *testing.T) core.KeySet {
		t.Helper()
		keys, err := core.NewKeySet(core.KeyDocument{
			Keys: []core.KeyDefinition{
				{Key: "WB", Retired: true},
				{Key: "NEW"},
				{Key: "SPARE"},
				{Key: "THIRD"},
			},
			Current: "THIRD",
		})
		if err != nil {
			t.Fatalf("NewKeySet() error = %v", err)
		}
		return keys
	}
	runKeyPanelClient(t, "adding a key", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  const form = keyAdd();
  const name = findElement(form, (element) => element.id === "key-new-name");
  const makeCurrent = findElement(form, (element) => element.id === "key-new-current");
  if (!name || !makeCurrent) throw new Error("the add form has no name field and current switch");
  const add = panelControl(form, "Add key");
  if (!add) throw new Error("the add form offers no Add key control");
  if (!add.disabled) throw new Error("Add key is offered with no name typed into it");

  name.value = "THIRD";
  await name.eventListeners.input();
  if (add.disabled) throw new Error("Add key stayed blocked with a name typed into it");
  makeCurrent.checked = true;
  keyAnswers.push({ body: `+keyPanelMutationJSON(t, widened(t), "head-2", nil)+` });
  await submitKeyForm(form);

  if (keyCalls.length !== 1) throw new Error("the add sent " + keyCalls.length + " requests, want 1");
  const sent = keyCalls[0];
  if (sent.method !== "POST" || sent.url !== "/api/vocabulary/keys") {
    throw new Error("the add sent " + sent.method + " " + sent.url);
  }
  if (sent.body.key !== "THIRD" || sent.body.current !== true || sent.body.expectedHead !== "head-1") {
    throw new Error("the add sent " + JSON.stringify(sent.body));
  }
  if (panelKeys().join(",") !== "WB,NEW,SPARE,THIRD") {
    throw new Error("the answer was not adopted: " + JSON.stringify(panelKeys()));
  }
  if (!keyRowIsCurrent("THIRD")) throw new Error("the added key did not take the minting");
  if (!keyMessages().some((line) => line.includes("THIRD"))) {
    throw new Error("the section said " + JSON.stringify(keyMessages()) + " about the key it added");
  }
`)
}

// A refusal is quoted exactly as the command would have printed it, in this
// section's own live region — which is where the statuses' refusals go, and why
// each section has one: a refused key change must not blank a label somebody is
// typing into a status row.
func TestClientKeysSectionQuotesARefusalItDidNotMake(t *testing.T) {
	const refusal = `project key "SPARE" is this project's only active key, and a project must keep one ` +
		`to mint new tasks under; add another first: workbook key add <key>`
	runKeyPanelClient(t, "quoting a refused key change", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  keyAnswers.push({ ok: false, body: `+panelRefusalJSON(t, core.CategoryValidation, refusal)+` });
  await pressKeyControl("SPARE", "Retire");

  if (!keyMessages().includes(`+strconv.Quote(refusal)+`)) {
    throw new Error("the section said " + JSON.stringify(keyMessages()) + ", want the server's own sentence");
  }
  if (keyPanelStatus.dataset.kind !== "error") {
    throw new Error("the refusal was not reported as one");
  }
  // The other sections are untouched: a refused key change is news about the
  // keys and about nothing else.
  if (panelMessages().length !== 0 || priorityMessages().length !== 0) {
    throw new Error("a refused key change wrote into another section's live region");
  }
  // And the keys are left exactly as they stand. An ordinary refusal carries no
  // vocabulary, so there is nothing to adopt and nothing to redraw.
  if (keyRowState("SPARE") !== "active" || !keyRowIsCurrent("NEW")) {
    throw new Error("a refused change moved the list anyway");
  }
  if (keyPanelRow("SPARE").children[1].children.some((control) => control.disabled)) {
    throw new Error("the row's controls were left disabled after the refusal");
  }
`)
}

// A project that has never added a key draws the create form it always drew. A
// select with one option asks a question nobody can answer differently, so there
// is no chooser at all — and the create sends no key, which is what every client
// predating several keys sends and what the server reads as the current one.
func TestClientNewTaskFormOffersNoKeyChooserForAProjectWithOneKey(t *testing.T) {
	task := clientPlacementTask("WB-01J0000000000000000000FF01", "Neighbour task", core.StatusReady, core.PriorityMedium)
	runKeyPanelClient(t, "a one-key project's create form", "/tasks/new", oneKeyProject(), []core.Task{task}, `
  await settle();
  const form = findElement(main, (element) => element.tagName === "FORM");
  if (!form) throw new Error("the New Task form did not render");
  if (newTaskKeySelect(form)) {
    throw new Error("a project with one key drew a key chooser");
  }
  if (findElement(form, (element) => element.textContent === "Key")) {
    throw new Error("a project with one key drew a Key caption over nothing");
  }
  findElement(form, (element) => element.id === "task-title").value = "Filed under the only key";
  const settled = form.eventListeners.submit({ preventDefault() {} });
  await settled;
  const created = fetchCalls.find((call) => call.url === "/api/tasks" && call.method === "POST");
  if (!created) throw new Error("the create was never sent");
  if ("key" in created.body) {
    throw new Error("a form with no chooser sent a key anyway: " + JSON.stringify(created.body));
  }
`)
}

// With more than one key able to mint, the form offers the choice — defaulting
// to the current key, listing the active keys only, and sending the key only
// when the reader picked another one.
//
// Retired keys are not offered. A retired key mints nothing, so listing it would
// be offering a choice the service refuses in core's own words.
func TestClientNewTaskFormChoosesAKeyAndSendsItOnCreate(t *testing.T) {
	task := clientPlacementTask("WB-01J0000000000000000000FF01", "Neighbour task", core.StatusReady, core.PriorityMedium)
	runKeyPanelClient(t, "choosing a key on create", "/tasks/new", projectKeys(t), []core.Task{task}, `
  await settle();
  const form = findElement(main, (element) => element.tagName === "FORM");
  const chooser = newTaskKeySelect(form);
  if (!chooser) throw new Error("a project with two active keys drew no key chooser");
  const offered = chooser.children.map((option) => option.value);
  if (offered.join(",") !== "NEW,SPARE") {
    throw new Error("the chooser offered " + JSON.stringify(offered) + ", want the active keys only");
  }
  const standing = chooser.children.find((option) => option.selected);
  if (!standing || standing.value !== "NEW") {
    throw new Error("the chooser stands at " + (standing && standing.value) + ", want the current key");
  }
  const caption = findElement(form, (element) => element.tagName === "LABEL" && element.htmlFor === "task-key");
  if (!caption || caption.textContent !== "Key") {
    throw new Error("the chooser carries no caption pointing at it");
  }

  findElement(form, (element) => element.id === "task-title").value = "Filed under SPARE";
  chooseOption(chooser, "SPARE");
  const settled = form.eventListeners.submit({ preventDefault() {} });
  await settled;
  const created = fetchCalls.find((call) => call.url === "/api/tasks" && call.method === "POST");
  if (!created) throw new Error("the create was never sent");
  if (created.body.key !== "SPARE") {
    throw new Error("the create sent " + JSON.stringify(created.body.key) + ", want the key the reader chose");
  }
`)
}

// The chooser left standing at the current key sends nothing, so a project with
// several keys still sends the create every older client sends.
func TestClientNewTaskFormSendsNoKeyWhenTheReaderTookTheDefault(t *testing.T) {
	task := clientPlacementTask("WB-01J0000000000000000000FF01", "Neighbour task", core.StatusReady, core.PriorityMedium)
	runKeyPanelClient(t, "creating under the current key", "/tasks/new", projectKeys(t), []core.Task{task}, `
  await settle();
  const form = findElement(main, (element) => element.tagName === "FORM");
  if (!newTaskKeySelect(form)) throw new Error("a project with two active keys drew no key chooser");
  findElement(form, (element) => element.id === "task-title").value = "Filed under the current key";
  const settled = form.eventListeners.submit({ preventDefault() {} });
  await settled;
  const created = fetchCalls.find((call) => call.url === "/api/tasks" && call.method === "POST");
  if ("key" in created.body) {
    throw new Error("taking the default sent a key anyway: " + JSON.stringify(created.body));
  }
`)
}

// The chooser reads the keys this page last heard about, not the ones it was
// served with.
//
// This is the one place the keys section's design shows in the create form. A
// key change moves no column and no priority, so it deliberately does not move
// the vocabulary shape and no board is asked to reload for one — which means the
// attribute the page loaded with is the only copy of the keys anything else
// would have. So every vocabulary this page receives replaces them, and the next
// form opened offers what the project now has. Nothing polls for it.
func TestClientNewTaskChooserReadsTheKeysThePageLastHeardAbout(t *testing.T) {
	added := func(t *testing.T) core.KeySet {
		t.Helper()
		keys, err := core.NewKeySet(core.KeyDocument{
			Keys: []core.KeyDefinition{
				{Key: "WB", Retired: true},
				{Key: "NEW"},
				{Key: "SPARE"},
				{Key: "THIRD"},
			},
			Current: "THIRD",
		})
		if err != nil {
			t.Fatalf("NewKeySet() error = %v", err)
		}
		return keys
	}
	task := clientPlacementTask("WB-01J0000000000000000000FF01", "Neighbour task", core.StatusReady, core.PriorityMedium)
	runKeyPanelClient(t, "a key added while the page was open", "/", projectKeys(t), []core.Task{task}, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
  await openStatuses();
  keyAnswers.push({ body: `+keyPanelMutationJSON(t, added(t), "head-2", nil)+` });
  const form = keyAdd();
  findElement(form, (element) => element.id === "key-new-name").value = "THIRD";
  findElement(form, (element) => element.id === "key-new-current").checked = true;
  await submitKeyForm(form);

  // Back to the board and on to a new task, by the links a reader follows.
  await returnToBoard();
  const link = new TestElement("a");
  link.href = window.location.origin + "/tasks/new";
  await follow(link);
  await settle();
  const created = findElement(main, (element) => element.tagName === "FORM");
  const chooser = newTaskKeySelect(created);
  if (!chooser) throw new Error("the create form drew no chooser after a key was added");
  const offered = chooser.children.map((option) => option.value);
  if (offered.join(",") !== "NEW,SPARE,THIRD") {
    throw new Error("the chooser offered " + JSON.stringify(offered) + ", want the keys the page last heard about");
  }
  const standing = chooser.children.find((option) => option.selected);
  if (!standing || standing.value !== "THIRD") {
    throw new Error("the chooser stands at " + (standing && standing.value) + ", want the current key as it now is");
  }
`)
}

// The client holds no reading of what a key may be called. The grammar is
// core.ValidateProjectKey's, the route answers a name that is not one in that
// sentence, and a script that refused first would refuse in words of its own.
func TestClientScriptNamesNoKeyGrammarOfItsOwn(t *testing.T) {
	response := request(t, keysAdministrableHandler(t, handlerVocabulary(t), projectKeys(t), "head-1", nil),
		http.MethodGet, "/config")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /config status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	for _, spelling := range []string{
		core.ProjectKeyPattern(),
		"A-Z0-9",
	} {
		if strings.Contains(script, spelling) {
			t.Errorf("the client script spells the key grammar %q, which core owns", spelling)
		}
	}
}
