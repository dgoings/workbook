package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the statuses section of the configuration route does, and what it
// refuses to do.
//
// It is one of two sections on a page of its own at /config, reached from the
// board and served on a hard load like every other route here. It is also the
// one writer in this client that is outside the optimistic mutation queue, so
// most of what is pinned here is about the two rules that follow from that: a
// change waits for the queue to be empty before it goes — including for intents
// started on the board before the reader navigated — and a stale write is where
// the change stops. The rest is request shape — the head the page read has to be
// the head it names — and the standing invariant the whole board rests on: no
// vocabulary change rebuilds a column under the reader, on either side of a
// navigation.

// administrableHandler is a board with the four vocabulary mutations, which is
// what `workbook serve` builds and the only kind whose configuration route
// exists at all. The
// mutations themselves are never reached: the client tests answer the routes
// from the fake fetch, and what the routes do with a request is
// vocabulary_mutation_test.go's subject.
func administrableHandler(vocabulary core.Vocabulary, head string, tasks []core.Task) http.Handler {
	unreached := func() (VocabularyMutation, error) { return VocabularyMutation{}, nil }
	return NewHandler(Options{
		Vocabulary: staticVocabulary(vocabulary, head),
		List:       func(context.Context) ([]core.Task, error) { return tasks, nil },
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
	})
}

// panelFetchHarness replaces the fake fetch with one that records what the page
// sends and answers the vocabulary routes from values the test sets.
//
// The answers are the server's own documents, encoded by the same builders the
// routes use, so a client that reads a member the server does not serve fails
// here rather than in a browser.
const panelFetchHarness = `
const vocabularyCalls = [];
const displayCalls = [];
// What GET /api/vocabulary answers, what the next mutation answers, what the
// next save of the board settings answers, and a gate a task write waits on so
// a test can hold the optimistic queue open.
let vocabularyRead = null;
let vocabularyAnswer = null;
let displayAnswer = null;
let taskWriteGate = null;
globalThis.fetch = async (url, options = {}) => {
  const method = (options.method || "GET").toUpperCase();
  const call = { url, method, options, headers: options.headers || {},
    body: options.body === undefined ? null : JSON.parse(options.body) };
  fetchCalls.push(call);
  if (url.startsWith("/api/vocabulary")) {
    vocabularyCalls.push(call);
    if (method === "GET") return { ok: true, json: async () => vocabularyRead };
    if (!vocabularyAnswer) throw new Error("the panel sent " + method + " " + url + " with no answer prepared");
    const answer = vocabularyAnswer;
    return { ok: answer.ok !== false, json: async () => answer.body };
  }
  if (url === "/api/display") {
    displayCalls.push(call);
    if (!displayAnswer) throw new Error("the page sent " + method + " " + url + " with no answer prepared");
    const answer = displayAnswer;
    return { ok: answer.ok !== false, json: async () => answer.body };
  }
  if (method !== "GET") {
    if (taskWriteGate) await taskWriteGate;
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: taskDocument.tasks[0] }) };
  }
  return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
};
// Lets every promise that is ready settle. It is a real timer rather than the
// window one the harness fakes, so it runs after the microtasks a fetch chain
// queues and before anything the page schedules.
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
// Follows a link the way a reader does, through the click the client intercepts
// rather than by calling navigate() behind its back.
async function follow(anchor) {
  await documentEventListeners.click({ target: anchor, button: 0, preventDefault() {} });
  await settle();
}
// Walks to the statuses page from wherever the reader is, by the link the
// server renders into the header, and waits for the read the route starts.
async function openStatuses() {
  const link = new TestElement("a");
  link.href = window.location.origin + "/config";
  await follow(link);
}
// The board settings form the configuration route draws, and the value in one
// of its fields.
function displayForm() {
  const form = findElement(displayPanelBody, (element) => hasDataKey(element, "displayForm"));
  if (!form) throw new Error("the configuration page drew no board settings form");
  return form;
}
function displayField(member) {
  const field = findElement(displayPanelBody, (element) => element.dataset.displayField === member);
  if (!field) throw new Error("the board settings form has no " + member + " field");
  return field;
}
async function saveDisplay() {
  await displayForm().eventListeners.submit({ preventDefault() {} });
  await settle();
}
// The heading the route draws over a section, by the id the panel points at.
function sectionHeadingText(id) {
  const heading = findElement(main, (element) => element.attributes && element.attributes.id === id);
  return heading ? heading.textContent : null;
}
// Walks back to the board by the page's own Back link.
async function returnToBoard() {
  const back = findElement(main, (element) => element.tagName === "A" && element.className === "board-link");
  if (!back) throw new Error("the statuses page offers no way back to the board");
  await follow(back);
}
// The route shell the statuses page is drawn in, or null when main is drawing
// something else.
function statusesRoute() {
  const section = findElement(main, (element) => hasClassToken(element, "task-route--admin"));
  return section && section.contains(vocabularyPanel) ? section : null;
}
function panelAdd() {
  const form = findElement(vocabularyPanelBody, (element) => hasDataKey(element, "vocabularyAdd"));
  if (!form) throw new Error("the panel drew no add-a-status form");
  return form;
}
function panelForm(status, key) {
  const form = findElement(vocabularyPanelBody, (element) => element.dataset[key] === status);
  if (!form) throw new Error("the panel drew no " + key + " form for " + status);
  return form;
}
async function submitPanelForm(form) {
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
}
`

// runPanelClient renders an administrable board for a project with these
// statuses and executes its client script against the fake DOM and the
// recording fetch. The reader starts on the board, which is where the link to
// the statuses page is.
func runPanelClient(t *testing.T, purpose string, vocabulary core.Vocabulary, head string, tasks []core.Task, body string) {
	t.Helper()
	runStatusesClient(t, purpose, "/", "", vocabulary, head, tasks, body)
}

// runStatusesClient is the same with the entry point spelled out: `path` is the
// address the reader arrived at, which a deep-link test sets to /config, and
// `prelude` is script the harness runs before the client's, which is how a page
// served without the statuses markup is put in front of it.
func runStatusesClient(
	t *testing.T,
	purpose, path, prelude string,
	vocabulary core.Vocabulary,
	head string,
	tasks []core.Task,
	body string,
) {
	runClientOverHandler(t, administrableHandler(vocabulary, head, tasks),
		purpose, path, prelude, vocabulary, head, tasks, body)
}

// runClientOverHandler is the same again for a board built some other way — the
// one wired for the board settings as well as for the statuses, which is what
// `workbook serve` builds and what the configuration route's second section
// needs to be served at all.
func runClientOverHandler(
	t *testing.T,
	handler http.Handler,
	purpose, path, prelude string,
	vocabulary core.Vocabulary,
	head string,
	tasks []core.Task,
	body string,
) {
	t.Helper()
	node := requireNode(t)
	response := request(t, handler, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d; body = %s", path, response.Code, http.StatusOK, response.Body.String())
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: head,
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarnessWith(path, string(document), vocabulary, head) +
		panelFetchHarness + prelude + script + `
setTimeout(async () => {
` + body + `
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// withoutStatusAdministration is what a board built without the four vocabulary
// mutations serves: none of the statuses route's markup at all. The client
// script is the same on every board and asks for each part by name, so taking
// the answers away is exactly what such a page does to it.
const withoutStatusAdministration = `
const servedQuerySelector = document.querySelector.bind(document);
document.querySelector = (selector) =>
  selector.startsWith("[data-vocabulary-panel") ? null : servedQuerySelector(selector);
`

// withoutDisplaySettings is what a board built without the display writer
// serves: the statuses section and none of the board settings markup. The client
// script is the same on every board and asks for each part by name, so taking
// the answers away is exactly what such a page does to it.
const withoutDisplaySettings = `
const servedDisplayQuerySelector = document.querySelector.bind(document);
document.querySelector = (selector) =>
  selector.startsWith("[data-display-panel") ? null : servedDisplayQuerySelector(selector);
`

// panelVocabularyJSON is what GET /api/vocabulary answers, built by the server's
// own builder.
func panelVocabularyJSON(t *testing.T, vocabulary core.Vocabulary, head string) string {
	t.Helper()
	return string(mustJSON(t, vocabularyDocument(VocabularyState{Vocabulary: vocabulary, Head: head})))
}

// panelMutationJSON is what a vocabulary mutation answers.
func panelMutationJSON(
	t *testing.T,
	vocabulary core.Vocabulary,
	head string,
	tasks VocabularyTaskCounts,
	warnings []core.Warning,
) string {
	t.Helper()
	return string(mustJSON(t, VocabularyMutationDocument{
		Format:     "workbook.vocabulary-mutation",
		Version:    1,
		Vocabulary: vocabularyDocument(VocabularyState{Vocabulary: vocabulary, Head: head}),
		Tasks:      tasks,
		Warnings:   warnings,
	}))
}

// panelStaleWriteJSON is the 409 a change composed against a head somebody else
// has moved past is answered with, carrying the statuses as they now stand.
func panelStaleWriteJSON(t *testing.T, vocabulary core.Vocabulary, head, message string) string {
	t.Helper()
	current := vocabularyDocument(VocabularyState{Vocabulary: vocabulary, Head: head})
	return string(mustJSON(t, VocabularyErrorDocument{
		Format:     "workbook.error",
		Version:    1,
		Error:      ErrorBody{Category: core.CategoryStaleWrite, Message: message},
		Vocabulary: &current,
	}))
}

// panelRefusalJSON is any other refusal: the ordinary error envelope, with no
// vocabulary to re-render.
func panelRefusalJSON(t *testing.T, category core.Category, message string) string {
	t.Helper()
	return string(mustJSON(t, ErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error:   ErrorBody{Category: category, Message: message},
	}))
}

// panelRenamedVocabulary is the fixture vocabulary as another clone left it: one
// column relabelled and one added, which is a document the page's own columns
// cannot be mistaken for.
func panelRenamedVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Cold Storage", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "queued", Label: "Queued Up", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "triage", Label: "Triage", Rank: "5/2", Tags: []core.StatusTag{}},
			{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		[]core.StatusAlias{{From: "done", To: "shipped"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// panelRenamedQueuedVocabulary is what a rename answers with: the status is
// under its new name and its new label, and the old name forwards to it.
func panelRenamedQueuedVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Icebox", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "waiting", Label: "Waiting On", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		[]core.StatusAlias{{From: "done", To: "shipped"}, {From: "queued", To: "waiting"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// The way in is a link in the board's chrome, beside the other two routes this
// page has, and it is there for a project that configured its own statuses
// exactly as it is for one that never did.
//
// It is a link rather than a button because it goes to a page: the client
// intercepts the click and renders the route, and a reader without the script —
// or with a middle-click — gets the same page from the server.
func TestHandlerBoardLinksToTheStatusesRoute(t *testing.T) {
	for name, vocabulary := range map[string]core.Vocabulary{
		"default": core.DefaultVocabulary(),
		"custom":  handlerVocabulary(t),
	} {
		body := boardMarkup(t, administrableBoardPage(t, vocabulary))
		link := elementTag(t, body, `href="/config"`)
		for _, attribute := range []string{`<a`, `class="header-link"`} {
			if !strings.Contains(link, attribute) {
				t.Errorf("%s vocabulary drew a statuses entry point %q, which does not carry %q", name, link, attribute)
			}
		}
		// It is not hidden, because it does not depend on the script: the
		// address it names is served.
		if strings.Contains(link, "hidden") {
			t.Errorf("%s vocabulary ships the statuses link hidden: %q", name, link)
		}
		page := elementTag(t, body, "data-vocabulary-panel ")
		for _, attribute := range []string{
			`<div`,
			`class="admin"`,
			// Shipped hidden and outside main: the render for the route is what
			// mounts it, so a page whose script never ran draws the board it
			// was served rather than an administration surface below it.
			`hidden`,
			// It takes focus when a rebuild drops the control the reader
			// pressed, so it has to be focusable without being tabbable — and
			// named, since a screen reader announces nothing for a generic
			// element and a generic role may hold no name at all.
			`tabindex="-1"`,
			`role="group"`,
			`aria-labelledby="statuses-title"`,
			// The roles a status may carry are the server's answer, rendered
			// here because the client must not hold a second copy of them.
			`data-status-tags="default done next"`,
		} {
			if !strings.Contains(page, attribute) {
				t.Errorf("%s vocabulary drew a statuses page %q, which does not carry %q", name, page, attribute)
			}
		}
		if at := strings.Index(body, "</main>"); at < 0 || at > strings.Index(body, "data-vocabulary-panel ") {
			t.Errorf("%s vocabulary rendered the statuses page inside main, which the board occupies", name)
		}
		// The served page is a shell: the list inside it is the client's, drawn
		// from what the server answers rather than from the columns on the page.
		if !strings.Contains(body, `data-vocabulary-panel-body`) {
			t.Errorf("%s vocabulary drew no mount for the statuses list", name)
		}
	}
}

// A board built without the four mutations has no statuses route at all: no
// link, no markup, and an address that is answered as a page that does not
// exist rather than as a page whose every control would be refused.
//
// All four rather than any, because the page is one surface: a board carrying
// three of them would draw controls that look alike and fail differently.
func TestHandlerBoardWithoutVocabularyMutationsOffersNoStatusesRoute(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	for name, options := range map[string]Options{
		"no mutations": {
			Vocabulary: staticVocabulary(vocabulary, "head-1"),
			List:       func(context.Context) ([]core.Task, error) { return nil, nil },
		},
		"one mutation": {
			Vocabulary: staticVocabulary(vocabulary, "head-1"),
			List:       func(context.Context) ([]core.Task, error) { return nil, nil },
			AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
				return VocabularyMutation{}, nil
			},
		},
	} {
		response := request(t, NewHandler(options), http.MethodGet, "/")
		if response.Code != http.StatusOK {
			t.Fatalf("%s: GET / status = %d, want %d", name, response.Code, http.StatusOK)
		}
		body := response.Body.String()
		// The markup alone: the script is the same on every board and asks for
		// the page's parts by name whether or not they are there, which is
		// exactly what makes it safe to serve to a board without them.
		markup := boardMarkup(t, body)
		for _, marker := range []string{
			`href="/config"`,
			"data-vocabulary-panel data-status-tags",
			"data-vocabulary-panel-body",
		} {
			if strings.Contains(markup, marker) {
				t.Errorf("%s: the page carries %q for a board that cannot change its statuses", name, marker)
			}
		}
		// And the address is not a page on this board. A 404 rather than a
		// board: a reader who bookmarked /config against a board that can
		// administer its statuses is told this one cannot, rather than handed
		// columns under a title promising a page that is not there. The address
		// this route used to answer, /statuses, is not answered at all any more
		// — on this board or on the administrable one — because a redirect would
		// be a second name for a page that has one.
		missing := request(t, NewHandler(options), http.MethodGet, "/config")
		if missing.Code != http.StatusNotFound {
			t.Errorf("%s: GET /config status = %d, want %d", name, missing.Code, http.StatusNotFound)
		}
		// The board itself is untouched: it still draws its columns and still
		// says which vocabulary it drew them from.
		if !strings.Contains(body, `data-vocabulary-head="head-1"`) {
			t.Errorf("%s: the board stopped reporting the vocabulary head", name)
		}
	}
}

// Rendering the statuses route reads the project's statuses rather than drawing
// the ones the page happens to be showing.
//
// The page carries a token and a label per column and nothing else, and its head
// may be minutes old by the time anyone walks to this route. A change composed
// against what the page remembers is the stale write it would rather not have to
// report, so it asks — on every entry to the route, not once.
func TestClientStatusesRouteReadsTheProjectsStatusesOnEntry(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "opening the statuses page", vocabulary, "head-1", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, panelRenamedVocabulary(t), "head-9")+`;
  if (vocabularyPanel.hidden !== true) throw new Error("the statuses page was mounted before anyone walked to it");
  if (statusesRoute()) throw new Error("the board route drew the statuses page");
  await openStatuses();

  if (window.location.href.indexOf("/config") < 0) throw new Error("the link did not go to /config: " + window.location.href);
  if (historyPaths[historyPaths.length - 1] !== "/config") throw new Error("the walk pushed " + JSON.stringify(historyPaths));
  if (document.title !== "Configuration · Workbook") throw new Error("the page is titled " + JSON.stringify(document.title));
  if (!statusesRoute()) throw new Error("the route drew no statuses page into main");
  if (vocabularyPanel.hidden !== false) throw new Error("the route left its own body hidden");
  if (main.children.length !== 1 || main.children[0] !== statusesRoute()) {
    throw new Error("main is drawing the board and the statuses page at once");
  }
  if (vocabularyCalls.length !== 1 || vocabularyCalls[0].method !== "GET" || vocabularyCalls[0].url !== "/api/vocabulary") {
    throw new Error("the route asked for " + JSON.stringify(vocabularyCalls.map((call) => call.method + " " + call.url)));
  }
  // The statuses it lists are the ones the server just answered with, in the
  // server's order — not the four columns this page was rendered with.
  const listed = panelStatuses();
  if (listed.join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the panel listed " + listed.join(","));
  }
  const row = panelRow("icebox");
  if (row.textContent.indexOf("Cold Storage") < 0) throw new Error("the row does not carry the label the server answered with: " + row.textContent);
  if (row.textContent.indexOf("icebox") < 0) throw new Error("the row does not name the status token: " + row.textContent);
  const tagged = panelRow("queued");
  const tags = findElements(tagged, (element) => Boolean(element.dataset.statusTag)).map((chip) => chip.dataset.statusTag);
  if (tags.join(",") !== "default,next") throw new Error("the row reports the tags " + tags.join(","));

  // Walking back to the board and returning asks again, because another clone
  // may have changed the statuses in between. The list it was drawing goes with
  // the visit rather than standing until the next one replaces it.
  await returnToBoard();
  if (statusesRoute()) throw new Error("Back left the statuses page in main");
  if (vocabularyPanel.hidden !== true) throw new Error("Back left the statuses page mounted");
  if (panelStatuses().length !== 0) throw new Error("the page kept a list nobody is looking at");
  await openStatuses();
  if (vocabularyCalls.length !== 2) throw new Error("returning to the route asked " + vocabularyCalls.length + " times in total");

  // The read is often the first thing on the page to notice that another clone
  // moved the ledger — this page drew its columns from head-1 — so the standing
  // notice goes up on the strength of it, without a poll and without a column
  // being rebuilt.
  if (vocabularyNotice.hidden !== false) throw new Error("a vocabulary read past this page's own head raised no notice");

  // And a change composed here names the head the panel read rather than the
  // one the page was served with.
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-10", VocabularyTaskCounts{}, nil)+` };
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "paused";
  name.eventListeners.input();
  await submitPanelForm(form);
  const wrote = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (!wrote || wrote.body.expectedHead !== "head-9") {
    throw new Error("the change named the head " + JSON.stringify(wrote && wrote.body.expectedHead) +
      ", want the one the panel read rather than the one the page was rendered from");
  }
`)
}

// A change carries the head the panel read, and the board it is drawn over is
// left exactly as it is.
//
// Both halves are the point. The head is what makes a status change refusable
// rather than last-writer-wins, and the panel names the one it actually read —
// not the one the page was served with. And the columns behind the panel are
// not rebuilt to show the change: every card node on the board is holding
// somebody's work in flight, so the standing notice offers the reload and the
// reader picks the moment.
func TestClientStatusesPageAddsAStatusAgainstTheHeadItRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
		clientPlacementTask("WB-01J0000000000000000000B202", "Queued", core.Status("queued"), core.PriorityHigh),
	}
	runPanelClient(t, "adding a status", vocabulary, "head-7", tasks, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, []core.Warning{
		{Code: "docs-refresh-incomplete", Message: "the generated guidelines are out of date; run workbook docs update"},
	})+` };
  await openStatuses();

  // Nothing has changed yet, and the board has been told nothing.
  if (vocabularyNotice.hidden !== true) throw new Error("opening the panel told the board its columns were out of date");
  const held = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  if (held.length !== 2) throw new Error("the board did not draw both cards");
  held.forEach((node, index) => { node.__witness = "card-" + index; });
  const columnsBefore = boardLists.map((list) => list.dataset.status).join(",");

  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  const label = findElement(form, (element) => element.id === "status-new-label");
  const placement = findElement(form, (element) => element.id === "status-new-placement");
  const submit = panelControl(form, "Add status");
  if (submit.disabled !== true) throw new Error("Add status was offered with no name typed into it");
  name.value = "triage";
  name.eventListeners.input();
  if (submit.disabled !== false) throw new Error("Add status stayed disabled after a name was typed");
  label.value = "Triage";
  findElement(form, (element) => element.id === "status-new-tag-next").checked = true;
  chooseOption(placement, "after:queued");
  await submitPanelForm(form);

  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("the panel sent " + sent.length + " changes for one gesture");
  const wrote = sent[0];
  if (wrote.method !== "POST" || wrote.url !== "/api/vocabulary/statuses") {
    throw new Error("the panel sent " + wrote.method + " " + wrote.url);
  }
  if (wrote.headers["Content-Type"] !== "application/json") {
    throw new Error("the change did not name its media type: " + JSON.stringify(wrote.headers));
  }
  const want = { status: "triage", label: "Triage", tags: ["next"], after: "queued", expectedHead: "head-7" };
  if (JSON.stringify(wrote.body) !== JSON.stringify(want)) {
    throw new Error("the change sent " + JSON.stringify(wrote.body) + ", want " + JSON.stringify(want));
  }

  // The panel re-renders itself from the answer, wholesale.
  if (panelStatuses().join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the panel is drawing " + panelStatuses().join(",") + " after the change");
  }
  if (panelRow("icebox").textContent.indexOf("Cold Storage") < 0) {
    throw new Error("the panel kept a label the answer replaced");
  }
  const said = panelMessages();
  if (said.length !== 2) throw new Error("the panel said " + JSON.stringify(said));
  if (said[0].indexOf("triage") < 0) throw new Error("the panel did not report the change: " + said[0]);
  if (said[1].indexOf("workbook docs update") < 0) throw new Error("the server's warning was swallowed: " + JSON.stringify(said));
  const warned = findElements(vocabularyPanelStatus, (element) => hasDataKey(element, "panelWarning"));
  if (warned.length !== 1) throw new Error("the warning was not marked as one");

  // The board is untouched, and told to say so.
  if (vocabularyNotice.hidden !== false) throw new Error("the board was not told its columns are out of date");
  if (boardLists.map((list) => list.dataset.status).join(",") !== columnsBefore) {
    throw new Error("the panel rebuilt the board's columns");
  }
  const after = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  held.forEach((node, index) => {
    if (after[index] !== node || after[index].__witness !== "card-" + index) {
      throw new Error("card " + index + " was rebuilt by a status change");
    }
  });

  // The next change names the head this one produced.
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-9", VocabularyTaskCounts{}, nil)+` };
  const again = panelAdd();
  const nextName = findElement(again, (element) => element.id === "status-new-name");
  if (nextName.value !== "") throw new Error("the rebuilt add form is still holding " + JSON.stringify(nextName.value));
  nextName.value = "paused";
  nextName.eventListeners.input();
  await submitPanelForm(again);
  const second = vocabularyCalls.filter((call) => call.method !== "GET")[1];
  if (!second || second.body.expectedHead !== "head-8") {
    throw new Error("the second change named the head " + JSON.stringify(second && second.body.expectedHead));
  }
`)
}

// A project whose configuration ledger has never been seeded is administrable,
// and the empty head it reads is a head: it is sent, as the empty string, rather
// than withheld as if the panel had not looked.
func TestClientStatusesPageSendsTheEmptyHeadOfAnUnseededProject(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "administering an unseeded project", vocabulary, "", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-1", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);

  const wrote = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (!wrote) throw new Error("the unseeded project refused to send a change at all");
  if (!("expectedHead" in wrote.body)) throw new Error("the change named no head: " + JSON.stringify(wrote.body));
  if (wrote.body.expectedHead !== "") throw new Error("the change named the head " + JSON.stringify(wrote.body.expectedHead));
`)
}

// A status change waits for the board's own writes to finish, across the
// navigation that led to it.
//
// A pending intent can be carrying the very status a change retires, and it was
// composed against the columns the reader was looking at a moment ago. Walking
// to another route does not empty the queue — the intent outlives the page it
// was made on, as it must — so the move is made on the board here and the change
// is composed after it, which is the order a reader makes them in. The page says
// it is waiting rather than appearing to have ignored the press, and sends once
// the queue is empty.
func TestClientStatusesPageWaitsForPendingBoardChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	task.Head = "head-a"
	runPanelClient(t, "a status change behind a pending intent", vocabulary, "head-7", []core.Task{task}, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };

  // Hold the board's write open, then make one on the board: the queue is now
  // non-empty, and it stays that way while the reader walks to the statuses
  // page.
  let releaseTaskWrite = null;
  taskWriteGate = new Promise((resolve) => { releaseTaskWrite = resolve; });
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  const dragged = boardCard(`+strconv.Quote(task.ID)+`);
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
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  const change = form.eventListeners.submit({ preventDefault() {} });
  await settle();

  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("the status change went out over a board that was still writing");
  }
  const waiting = panelMessages();
  if (waiting.length !== 1 || waiting[0].indexOf("already in flight") < 0) {
    throw new Error("the panel said " + JSON.stringify(waiting) + " while it waited");
  }
  // The controls it disabled stay disabled until the change resolves, so the
  // wait cannot be mistaken for a press that did nothing.
  if (panelControl(form, "Add status").disabled !== true) {
    throw new Error("the panel re-enabled its controls while it was waiting");
  }

  // Walking away and back while it waits is the one way to meet this page with
  // every control disabled and nothing on it that was said before the walk. It
  // says why again rather than presenting a dead grey list.
  await returnToBoard();
  await openStatuses();
  const returned = panelMessages();
  if (returned.length !== 1 || returned[0].indexOf("already in flight") < 0) {
    throw new Error("coming back mid-change said " + JSON.stringify(returned));
  }
  if (panelControl(panelAdd(), "Add status").disabled !== true) {
    throw new Error("coming back mid-change offered controls a change in flight has disabled");
  }

  releaseTaskWrite();
  await dropped;
  await change;
  await settle();

  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("the panel sent " + sent.length + " changes once the board was quiet");
  if (sent[0].body.status !== "triage") throw new Error("the panel sent " + JSON.stringify(sent[0].body));
  // And it went after the board's write, not beside it.
  const order = fetchCalls.filter((call) => call.method === "PATCH" || call.url === "/api/vocabulary/statuses")
    .map((call) => call.url);
  if (order[0] !== "/api/tasks/" + encodeURIComponent(`+strconv.Quote(task.ID)+`) + "/position") {
    throw new Error("the writes went out as " + JSON.stringify(order));
  }
  if (order[order.length - 1] !== "/api/vocabulary/statuses") {
    throw new Error("the writes went out as " + JSON.stringify(order));
  }
  if (panelControl(panelAdd(), "Add status").disabled !== true) {
    throw new Error("the rebuilt add form offers a change with no name typed into it");
  }
`)
}

// A stale write is where a status change stops.
//
// The refusal carries the statuses as they now stand, so the panel adopts them
// and says what happened; it does not re-base the change onto the head it just
// learned and send it again. Two people renaming the same column mean two
// different things, and a client that applied one over the other would invent a
// third that neither of them chose.
func TestClientStatusesPageStopsAtAStaleWrite(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a status change refused as stale", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { ok: false, body: `+panelStaleWriteJSON(t, panelRenamedVocabulary(t), "head-8",
		"this project's statuses have changed since head-7; reload and try again")+` };
  await openStatuses();

  panelControl(panelRow("icebox"), "Edit Icebox").eventListeners.click();
  const form = panelForm("icebox", "vocabularyEdit");
  const label = findElement(form, (element) => element.id === "status-label-icebox");
  label.value = "Deep Freeze";
  await submitPanelForm(form);

  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("a refused status change was sent " + sent.length + " times");
  if (vocabularyCalls.filter((call) => call.method === "GET").length !== 1) {
    throw new Error("the refusal was followed by a re-read it did not need");
  }
  const said = panelMessages();
  if (said.length !== 1 || said[0].indexOf("another clone") < 0) {
    throw new Error("the panel said " + JSON.stringify(said) + " about a stale write");
  }
  // The head is this client's own bookkeeping, so the refusal is described
  // rather than quoted.
  if (said[0].indexOf("head-7") >= 0) throw new Error("the panel quoted a sentence about a head at the reader: " + said[0]);
  if (vocabularyPanelStatus.dataset.kind !== "error") throw new Error("the refusal was not reported as one");

  // The statuses it is now showing are the ones the refusal carried, and the
  // form composed against the old ones is gone.
  if (panelStatuses().join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the panel is still drawing " + panelStatuses().join(","));
  }
  if (findElement(vocabularyPanelBody, (element) => hasDataKey(element, "vocabularyEdit"))) {
    throw new Error("the panel kept a form composed against the statuses it just replaced");
  }
  // Its controls work again: a refusal is not a dead end, it is a re-read.
  const retry = panelControl(panelRow("icebox"), "Edit Cold Storage");
  if (!retry) throw new Error("the rebuilt row offers no way to edit it");
  if (retry.disabled !== false) throw new Error("the panel left its controls disabled after a refusal");
  // And the board is told the columns it is drawing are out of date.
  if (vocabularyNotice.hidden !== false) throw new Error("the refusal's vocabulary raised no notice for the board");

  // A change made after the refusal names the head the refusal carried.
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-9", VocabularyTaskCounts{}, nil)+` };
  retry.eventListeners.click();
  const reopened = panelForm("icebox", "vocabularyEdit");
  findElement(reopened, (element) => element.id === "status-label-icebox").value = "Deep Freeze";
  await submitPanelForm(reopened);
  const second = vocabularyCalls.filter((call) => call.method !== "GET")[1];
  if (!second || second.body.expectedHead !== "head-8") {
    throw new Error("the change after the refusal named " + JSON.stringify(second && second.body.expectedHead));
  }
  if (JSON.stringify(second.body) !== JSON.stringify({ label: "Deep Freeze", expectedHead: "head-8" })) {
    throw new Error("the change sent " + JSON.stringify(second.body) + ", want the one member it edited");
  }
`)
}

// A removal names where the tasks go and reports what it moved, in the terms
// `workbook status delete` reports them.
func TestClientStatusesPagePricesARemoval(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "removing a status", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8",
		VocabularyTaskCounts{Affected: 3, ClaimableAfter: 2}, nil)+` };
  await openStatuses();

  panelControl(panelRow("shipped"), "Delete Shipped").eventListeners.click();
  const form = panelForm("shipped", "vocabularyDelete");
  const into = findElement(form, (element) => element.id === "status-into-shipped");
  // The destination is every other status this project has, and nothing else:
  // a column cannot be emptied into itself.
  const offered = into.children.map((option) => option.value);
  if (offered.join(",") !== "icebox,queued") throw new Error("the destinations offered are " + offered.join(","));
  chooseOption(into, "icebox");
  await submitPanelForm(form);

  const wrote = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (wrote.method !== "DELETE" || wrote.url !== "/api/vocabulary/statuses/shipped") {
    throw new Error("the removal sent " + wrote.method + " " + wrote.url);
  }
  if (JSON.stringify(wrote.body) !== JSON.stringify({ into: "icebox", expectedHead: "head-7" })) {
    throw new Error("the removal sent " + JSON.stringify(wrote.body));
  }
  const said = panelMessages()[0];
  if (said.indexOf("3 tasks moved") < 0 || said.indexOf("2 of them claimable") < 0) {
    throw new Error("the removal reported " + JSON.stringify(said));
  }
  // Named by the label the answer carries, since that is what the reader will
  // be looking for on the board.
  if (said.indexOf("Cold Storage") < 0) throw new Error("the removal did not name where the tasks went: " + said);
`)
}

// Everything a status change can be refused for is the vocabulary's decision,
// and the panel puts the vocabulary's own sentence in front of the reader.
//
// A client that checked first would refuse in words of its own — a second,
// worse copy of a rule that lives in one place — and would refuse a change the
// server would have accepted the moment either of them drifted.
func TestClientStatusesPageQuotesARefusalItDidNotMake(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a refused status change", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { ok: false, body: `+panelRefusalJSON(t, core.CategoryValidation,
		`unsupported status tag "blocked"`)+` };
  await openStatuses();

  panelControl(panelRow("queued"), "Edit Queued Up").eventListeners.click();
  const form = panelForm("queued", "vocabularyEdit");
  // Tag arity is the vocabulary's rule: clearing the only default-tagged status
  // is refused there, and the panel offers the box either way.
  const box = findElement(form, (element) => element.id === "status-queued-tag-default");
  if (box.checked !== true) throw new Error("the form does not show the tags the status carries");
  box.checked = false;
  await submitPanelForm(form);

  const said = panelMessages();
  if (said.length !== 1 || said[0] !== 'unsupported status tag "blocked"') {
    throw new Error("the panel reported " + JSON.stringify(said) + " rather than the server's sentence");
  }
  // A refusal that carries no vocabulary changes nothing about what the panel
  // is drawing, and leaves the form the reader is standing in open.
  if (panelStatuses().join(",") !== "icebox,queued,shipped") {
    throw new Error("a refusal replaced the statuses with " + panelStatuses().join(","));
  }
  const standing = panelForm("queued", "vocabularyEdit");
  if (panelControl(standing, "Save changes to Queued Up").disabled !== false) {
    throw new Error("the panel left its controls disabled after a refusal it can be corrected from");
  }
  if (vocabularyNotice.hidden !== true) throw new Error("a refusal told the board its columns had changed");
`)
}

// A reorder is one request per gesture, carrying the whole order.
//
// The server takes the whole order because a drag is one decision; a sequence
// of pairwise moves would be several commits describing it, each of which could
// be refused on its own. Both ways of making the gesture — the drag and the
// controls a keyboard can reach — send exactly the same one request.
func TestClientStatusesPageReordersInOneRequestPerGesture(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "reordering the columns", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  // The first row cannot move up and the last cannot move down, and both say so
  // rather than sending an order the server would refuse.
  if (panelControl(panelRow("icebox"), "Move Icebox earlier").disabled !== true) {
    throw new Error("the first row offers to move earlier than first");
  }
  if (panelControl(panelRow("shipped"), "Move Shipped later").disabled !== true) {
    throw new Error("the last row offers to move later than last");
  }

  panelControl(panelRow("icebox"), "Move Icebox later").eventListeners.click();
  await settle();
  let sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("one move sent " + sent.length + " requests");
  if (sent[0].method !== "PUT" || sent[0].url !== "/api/vocabulary/order") {
    throw new Error("the move sent " + sent[0].method + " " + sent[0].url);
  }
  if (JSON.stringify(sent[0].body) !== JSON.stringify({ statuses: ["queued", "icebox", "shipped"], expectedHead: "head-7" })) {
    throw new Error("the move sent " + JSON.stringify(sent[0].body));
  }
`)
	runPanelClient(t, "dragging a column into place", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  // A row whose form is open is not draggable: selecting the text in an input
  // is a press and a drag, which inside a draggable row is the gesture that
  // reorders the columns. Its Up and Down controls still move it.
  panelControl(panelRow("queued"), "Edit Queued Up").eventListeners.click();
  if (panelRow("queued").draggable !== false) throw new Error("a row being edited is still draggable");
  if (panelRow("icebox").draggable !== true) throw new Error("an ordinary row is not draggable");
  panelControl(panelForm("queued", "vocabularyEdit"), "Stop editing Queued Up").eventListeners.click();

  const dragged = panelRow("shipped");
  const target = panelRow("icebox");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });
  if (dragged.dataset.dragging !== "true") throw new Error("the dragged row does not say it is being dragged");
  target.eventListeners.dragover({ preventDefault() {}, dataTransfer });
  if (target.dataset.dropTarget !== "true") throw new Error("the row under the cursor is not marked");
  await target.eventListeners.drop({ preventDefault() {}, dataTransfer });
  await settle();

  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("one drag sent " + sent.length + " requests");
  // Dropped on the first row, it lands before it: the whole order, once.
  if (JSON.stringify(sent[0].body) !== JSON.stringify({ statuses: ["shipped", "icebox", "queued"], expectedHead: "head-7" })) {
    throw new Error("the drag sent " + JSON.stringify(sent[0].body));
  }
  if (panelStatuses().join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the panel did not re-render from the answer: " + panelStatuses().join(","));
  }
`)
}

// A drop is delivered only where the page said yes, and a browser takes that
// answer from whichever of dragenter and dragover it dispatched last. Blink
// dispatches dragenter rather than dragover whenever the hit-test target under
// the cursor changes, which is what any content moving under a stationary
// cursor causes — the board's autoscroll made exactly that happen and lost
// drops by it, because the board answered only dragover.
//
// Nothing moves under the cursor on this panel today. This closes the class
// rather than a live bug: the fake DOM has no accept flag of its own, so what
// is pinned is the property that keeps it closed — for every target either
// event can land on, the two give the same answer, through one rule.
func TestClientStatusesPageAcceptsAReorderOnEnterAsWellAsOnOver(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "reorder acceptance on enter and over", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const withFiles = { effectAllowed: "", dropEffect: "", setData() {}, types: ["Files"] };
  const answer = (name, row, transfer) => {
    let prevented = false;
    transfer.dropEffect = "";
    row.eventListeners[name]({ target: row, dataTransfer: transfer, preventDefault() { prevented = true; } });
    return prevented + "/" + transfer.dropEffect;
  };

  const dragged = panelRow("shipped");
  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });

  // Every row here is a drop target. The rows below that answer "false/" are
  // the ones a handler asking only "is there a row under the cursor?" would
  // wrongly accept, and they are what tells that question from the one this
  // listener actually has to ask.
  [
    ["another row, with a reorder in flight", panelRow("icebox"), dataTransfer, "true/move"],
    ["the row being dragged, which has nowhere to arrive", dragged, dataTransfer, "false/"],
    ["a row while files are being dragged in", panelRow("icebox"), withFiles, "false/"],
  ].forEach(([what, row, transfer, want]) => {
    const entered = answer("dragenter", row, transfer);
    const over = answer("dragover", row, transfer);
    if (entered !== want) throw new Error("dragenter on " + what + " answered " + entered + ", want " + want);
    if (over !== want) throw new Error("dragover on " + what + " answered " + over + ", want " + want);
  });

  // A row dropped on itself is refused rather than half-handled. The drop is
  // left entirely alone — nothing prevented, no request — and the gesture's own
  // dragend is what clears the drag, as it is for a drag that ends any other
  // way.
  let selfPrevented = false;
  await dragged.eventListeners.drop({ target: dragged, dataTransfer, preventDefault() { selfPrevented = true; } });
  await settle();
  if (selfPrevented) throw new Error("a row dropped on itself was taken as a drop");
  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("a row dropped on itself sent a request");
  }

  // And with no reorder in flight at all — somebody else's gesture passing over
  // the list — both events refuse every row.
  dragged.eventListeners.dragend({ target: dragged });
  ["icebox", "queued", "shipped"].forEach((status) => {
    if (panelRow(status).dataset.dropTarget === "true") {
      throw new Error("dragend left " + status + " marked as a drop target");
    }
  });
  ["icebox", "queued", "shipped"].forEach((status) => {
    const row = panelRow(status);
    const entered = answer("dragenter", row, dataTransfer);
    const over = answer("dragover", row, dataTransfer);
    if (entered !== "false/") throw new Error("dragenter on " + status + " with no reorder in flight answered " + entered);
    if (over !== "false/") throw new Error("dragover on " + status + " with no reorder in flight answered " + over);
  });
  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("answering a drag sent a request");
  }
`)
}

// The gesture the class is about: a reorder whose only word over the target row
// is a dragenter. That is what a browser sends once the thing under the cursor
// churns, and a panel that answered only dragover would take the drop nowhere.
func TestClientStatusesPageTakesAReorderReportedOnlyByDragenter(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a reorder reported only by dragenter", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const dragged = panelRow("shipped");
  const target = panelRow("icebox");
  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });

  // No dragover anywhere in this gesture.
  let prevented = false;
  target.eventListeners.dragenter({ target, dataTransfer, preventDefault() { prevented = true; } });
  if (!prevented) throw new Error("a dragenter over a row the reorder may land on was not answered");
  if (target.dataset.dropTarget !== "true") throw new Error("a dragenter drew no mark on the row under the cursor");

  await target.eventListeners.drop({ target, dataTransfer, preventDefault() {} });
  await settle();

  const sent = vocabularyCalls.filter((call) => call.method !== "GET");
  if (sent.length !== 1) throw new Error("a dragenter-only reorder sent " + sent.length + " requests");
  if (sent[0].method !== "PUT" || sent[0].url !== "/api/vocabulary/order") {
    throw new Error("the reorder sent " + sent[0].method + " " + sent[0].url);
  }
  if (JSON.stringify(sent[0].body) !== JSON.stringify({ statuses: ["shipped", "icebox", "queued"], expectedHead: "head-7" })) {
    throw new Error("the reorder sent " + JSON.stringify(sent[0].body));
  }
`)
}

// The mark saying where the row will land has to survive the leave that comes
// with every enter.
//
// A browser pairs a dragleave with each dragenter it sends — the board's own
// measurement in this file is ten dragenters, ten dragleaves and no dragover at
// all — so a handler that cleared on any leave would erase, on each churn tick,
// the mark the matching enter had just drawn. And it does not take churn: a row
// is a name, some chips and four buttons, so a cursor crossing any of them
// fires dragenter on the child and dragleave on the row.
//
// Two rules cover the browsers between them. relatedTarget settles it on
// Chrome; Firefox and Safari send none on a dragleave, and there the cursor's
// own coordinates have to.
func TestClientStatusesPageKeepsTheDropMarkThroughTheLeaveThatFollowsEachEnter(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "the leave that follows each enter", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const dragged = panelRow("shipped");
  const target = panelRow("icebox");
  // The geometry a browser would have computed for the row under the cursor.
  target.rect = { left: 100, right: 500, top: 200, bottom: 260, width: 400 };
  const inside = { clientX: 300, clientY: 230 };
  const outside = { clientX: 300, clientY: 400 };
  const child = target.children[0];
  if (!child) throw new Error("the row drew no children for a cursor to cross");

  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });
  const mark = () => {
    target.eventListeners.dragenter({ target, dataTransfer, ...inside, preventDefault() {} });
    if (target.dataset.dropTarget !== "true") throw new Error("the row under the cursor was not marked");
  };

  // Chrome's shape: the leave names the child the cursor moved onto.
  mark();
  target.eventListeners.dragleave({ target, relatedTarget: child, ...inside });
  if (target.dataset.dropTarget !== "true") {
    throw new Error("a leave naming a child of the row erased the row's own mark");
  }
  // The same shape with nothing to go on but the name — no coordinates at all,
  // which is what the two rules are for: either one alone leaves a browser
  // unanswered, and this is the half only relatedTarget can answer.
  target.eventListeners.dragleave({ target, relatedTarget: child });
  if (target.dataset.dropTarget !== "true") {
    throw new Error("a leave naming a child, with no coordinates to fall back on, erased the mark");
  }

  // Firefox and Safari send no relatedTarget at all, and there the cursor's
  // position is the only thing that can tell a churn leave from a departure.
  target.eventListeners.dragleave({ target, relatedTarget: null, ...inside });
  if (target.dataset.dropTarget !== "true") {
    throw new Error("a leave with no relatedTarget and the cursor still inside erased the mark");
  }

  // The whole churn tick, ten times over, with no dragover anywhere: enter,
  // leave, enter, leave. The mark has to be there at the end of it.
  for (let tick = 0; tick < 10; tick += 1) {
    target.eventListeners.dragenter({ target, dataTransfer, ...inside, preventDefault() {} });
    target.eventListeners.dragleave({ target, relatedTarget: null, ...inside });
  }
  if (target.dataset.dropTarget !== "true") {
    throw new Error("ten churn ticks left the reader with no mark at all");
  }

  // And a real departure still clears it: the cursor has gone somewhere else,
  // and nothing about this row is being promised any more.
  target.eventListeners.dragleave({ target, relatedTarget: null, ...outside });
  if (target.dataset.dropTarget === "true") {
    throw new Error("carrying the cursor off the row left its mark behind");
  }
  // As does a departure a browser names, with the cursor's own position
  // unavailable — a leave carrying neither is read as the departure it most
  // likely is.
  mark();
  target.eventListeners.dragleave({ target, relatedTarget: panelRow("queued") });
  if (target.dataset.dropTarget === "true") {
    throw new Error("a leave naming another row left the mark behind");
  }
  dragged.eventListeners.dragend({ target: dragged });
`)
}

// The mark is written when it changes and not otherwise. Rewriting the same
// attribute on every drag event is a mutation under a drag cursor for no
// change, and mutations under a drag cursor are what make a browser re-run its
// hit test and dispatch more of the churn this whole change is about.
func TestClientStatusesPageWritesTheDropMarkOnlyWhenItMoves(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "the drop mark is written only when it moves", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const dragged = panelRow("shipped");
  const target = panelRow("icebox");
  const other = panelRow("queued");
  target.rect = { left: 100, right: 500, top: 200, bottom: 260, width: 400 };
  const inside = { clientX: 300, clientY: 230 };

  // Count what the page writes to the attribute rather than what it ends up
  // saying, because the whole point is the writing.
  let written = 0;
  const raw = target.dataset;
  target.dataset = new Proxy(raw, {
    set(store, key, value) { if (key === "dropTarget") written += 1; store[key] = value; return true; },
    deleteProperty(store, key) { delete store[key]; return true; }
  });

  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });
  // A stationary cursor, twenty drag events, one row: the mark moves once.
  for (let tick = 0; tick < 10; tick += 1) {
    target.eventListeners.dragenter({ target, dataTransfer, ...inside, preventDefault() {} });
    target.eventListeners.dragover({ target, dataTransfer, ...inside, preventDefault() {} });
  }
  if (target.dataset.dropTarget !== "true") throw new Error("the row under the cursor is not marked");
  if (written !== 1) throw new Error("twenty drag events over one row wrote the mark " + written + " times, want 1");

  // Moving to another row and back writes it again, because now it has moved.
  other.eventListeners.dragover({ target: other, dataTransfer, clientX: 300, clientY: 500, preventDefault() {} });
  if (other.dataset.dropTarget !== "true") throw new Error("the second row was not marked");
  target.eventListeners.dragover({ target, dataTransfer, ...inside, preventDefault() {} });
  if (written !== 2) throw new Error("coming back to the first row wrote the mark " + written + " times in total, want 2");

  dragged.eventListeners.dragend({ target: dragged });
`)
}

// A file dragged over the status list is not a reorder, and the panel must
// neither take it nor swallow it. The flag that says "I will take this" is the
// same flag that stops the drop reaching anything else on the page, so a panel
// that accepted a file drag would take a reader's file and do nothing with it.
func TestClientStatusesPageRefusesAFileDraggedOverTheList(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a file dragged over the status list", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const withFiles = { effectAllowed: "", dropEffect: "", setData() {}, types: ["Files"] };
  const dragged = panelRow("shipped");
  const target = panelRow("icebox");
  // The worst case rather than the easy one: a reorder the page still believes
  // is live — a gesture whose dragend never arrived — and a file arriving over
  // the list on top of it. Without the file test this drop would be read as a
  // reorder and would move a column the reader never touched.
  dragged.eventListeners.dragstart({ target: dragged, dataTransfer });

  let prevented = false;
  target.eventListeners.dragenter({ target, dataTransfer: withFiles, preventDefault() { prevented = true; } });
  target.eventListeners.dragover({ target, dataTransfer: withFiles, preventDefault() { prevented = true; } });
  if (prevented) throw new Error("the panel offered to take a file drag");
  if (target.dataset.dropTarget === "true") throw new Error("a file drag marked a row as a drop target");

  await target.eventListeners.drop({ target, dataTransfer: withFiles, preventDefault() { prevented = true; } });
  await settle();
  if (prevented) throw new Error("the panel took the drop of a file");
  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("a file dropped on the status list sent a vocabulary request");
  }
  if (panelStatuses().join(",") !== "icebox,queued,shipped") {
    throw new Error("a file dropped on the status list reordered the columns: " + panelStatuses().join(","));
  }
`)
}

// administrableBoardPage renders an administrable board for a project with these
// statuses.
func administrableBoardPage(t *testing.T, vocabulary core.Vocabulary) string {
	t.Helper()
	response := request(t, administrableHandler(vocabulary, "head-1", nil), http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// boardMarkup is the served page without its client script, which is what an
// assertion about what the page draws has to be made against: the script names
// every element it looks for, present or not.
func boardMarkup(t *testing.T, body string) string {
	t.Helper()
	at := strings.LastIndex(body, "<script>")
	if at < 0 {
		t.Fatal("the rendered page has no client script, so it is not the page")
	}
	return body[:at]
}

// elementTag returns the whole opening tag of the element carrying an attribute,
// so an assertion can say what the element is rather than only that the page
// mentions the attribute somewhere.
func elementTag(t *testing.T, body, attribute string) string {
	t.Helper()
	at := strings.Index(body, attribute)
	if at < 0 {
		t.Fatalf("the page carries no element with %q", attribute)
	}
	start := strings.LastIndexByte(body[:at], '<')
	end := strings.IndexByte(body[at:], '>')
	if start < 0 || end < 0 {
		t.Fatalf("the element carrying %q has no tag around it", attribute)
	}
	return body[start : at+end+1]
}

// The client script must not name a status tag either.
//
// It is the same rule that keeps it from naming a status, and here it has teeth
// of its own: `done` is a tag in this vocabulary and a status name in most
// projects, so a script that spelled the tag would also be a script that names a
// status. The tags come from the panel's own attribute, which the server writes
// from core's list.
func TestClientScriptNamesNoStatusTagOfItsOwn(t *testing.T) {
	response := request(t, administrableHandler(handlerVocabulary(t), "head-1", nil), http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	for _, tag := range core.StatusTags() {
		if literal := strconv.Quote(string(tag)); strings.Contains(script, literal) {
			t.Errorf("the client script names the status tag %s, which is the server's to name", literal)
		}
	}
	// And it still reads them from the page, so the panel's forms have them.
	if !strings.Contains(script, "dataset.statusTags") {
		t.Error("the client script no longer reads the tags the server rendered")
	}
}

// The statuses page is styled as a route rather than as a region in the board's
// chrome, and nothing caps the height it may take.
//
// This is the bug the route exists to fix, pinned as a rule. As a disclosure in
// the viewport-height flex column the page shared its height with the header and
// the board, and on an ordinary desktop window what was left was a sliver too
// short to open a form in. The shell it is drawn in grows with its content and
// main is what scrolls, which is what every other route here does.
//
// Where the rule is written is part of the rule. `.task-route--admin` and
// `.task-route` are equally specific, so the shell only grows if it is written
// after the `height: 100%; min-height: 0` it overrides. Written before it, as it
// was, the declarations are inert and the route's rows spill out of the card it
// is drawn in — which is a page that renders wrongly while every assertion about
// its declarations passes.
func TestHandlerStatusesPageIsStyledAsARoute(t *testing.T) {
	body := administrableBoardPage(t, handlerVocabulary(t))
	shell := declarationBlock(t, body, ".task-route--admin {")
	for _, fragment := range []string{"height: auto", "min-height: 100%"} {
		if !strings.Contains(shell, fragment) {
			t.Errorf("the statuses route's shell rule %q does not contain %q", shell, fragment)
		}
	}
	base := strings.Index(body, ".task-route {")
	modifier := strings.Index(body, ".task-route--admin {")
	if base < 0 || modifier < 0 {
		t.Fatalf("the page has no .task-route rule (%d) or no .task-route--admin rule (%d)", base, modifier)
	}
	if modifier < base {
		t.Errorf("the .task-route--admin rule is written at %d, before the .task-route rule at %d, "+
			"where it is as specific as the height it means to override and therefore does nothing",
			modifier, base)
	}
	rule := declarationBlock(t, body, ".admin {")
	for _, fragment := range []string{"flex:", "max-height", "overflow"} {
		if strings.Contains(rule, fragment) {
			t.Errorf("the statuses page's rule %q still carries %q, which is what starved it in the board's chrome", rule, fragment)
		}
	}
	if !strings.Contains(body, ".admin[hidden] { display: none; }") {
		t.Error("the statuses page's display rule does not defeat the hidden attribute it ships with")
	}
}

// declarationBlock returns the declaration block of one CSS rule on a rendered
// page, selector included.
func declarationBlock(t *testing.T, body, selector string) string {
	t.Helper()
	start := strings.Index(body, selector)
	if start < 0 {
		t.Fatalf("the rendered page has no %q rule", selector)
	}
	rest := body[start:]
	end := strings.IndexByte(rest, '}')
	if end < 0 {
		t.Fatalf("the %q rule is unterminated", selector)
	}
	return rest[:end+1]
}

// A mutation answer that is not the document these routes promise is a failure,
// not a change: the panel says so and keeps drawing what it had.
func TestClientStatusesPageRefusesAnAnswerItCannotRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "an unreadable mutation answer", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: { format: "workbook.tasks", version: 1 } };
  await openStatuses();
  panelControl(panelRow("icebox"), "Edit Icebox").eventListeners.click();
  const form = panelForm("icebox", "vocabularyEdit");
  findElement(form, (element) => element.id === "status-label-icebox").value = "Deep Freeze";
  await submitPanelForm(form);

  const said = panelMessages();
  if (said.length !== 1 || said[0].length === 0) throw new Error("the panel said " + JSON.stringify(said));
  if (vocabularyPanelStatus.dataset.kind !== "error") throw new Error("an unreadable answer was reported as a change");
  if (panelStatuses().join(",") !== "icebox,queued,shipped") {
    throw new Error("an unreadable answer replaced the statuses with " + panelStatuses().join(","));
  }
  if (vocabularyNotice.hidden !== true) throw new Error("an unreadable answer told the board its columns had changed");
`)
}

// An edit sends the members it changed and nothing else, and a form that changed
// nothing sends nothing at all.
//
// A member is sent to be set, so re-asserting a label nobody edited is
// indistinguishable from editing it: a rename carrying the current label would
// overwrite a relabel that landed beside it. The server refuses a change with
// nothing in it, correctly, but an untouched form is finished rather than
// broken, so it says so instead of collecting that refusal.
func TestClientStatusesPageEditsOnlyWhatChanged(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "editing one member of a status", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedQueuedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openStatuses();

  panelControl(panelRow("queued"), "Edit Queued Up").eventListeners.click();
  let form = panelForm("queued", "vocabularyEdit");
  await submitPanelForm(form);
  if (vocabularyCalls.filter((call) => call.method !== "GET").length !== 0) {
    throw new Error("a form nobody edited sent a change");
  }
  const said = panelMessages();
  if (said.length !== 1 || said[0].indexOf("Nothing") < 0) throw new Error("the panel said " + JSON.stringify(said));

  // A name is required: emptying it is not a rename anyone could have meant.
  const name = findElement(form, (element) => element.id === "status-name-queued");
  const save = panelControl(form, "Save changes to Queued Up");
  name.value = "";
  name.eventListeners.input();
  if (save.disabled !== true) throw new Error("an empty name was offered as a rename");
  name.value = "waiting";
  name.eventListeners.input();
  if (save.disabled !== false) throw new Error("a typed name left the form unsavable");
  await submitPanelForm(form);

  const wrote = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (wrote.method !== "PATCH" || wrote.url !== "/api/vocabulary/statuses/queued") {
    throw new Error("the edit sent " + wrote.method + " " + wrote.url);
  }
  if (JSON.stringify(wrote.body) !== JSON.stringify({ name: "waiting", expectedHead: "head-7" })) {
    throw new Error("the edit sent " + JSON.stringify(wrote.body) + ", want the one member it changed");
  }
  // A rename is reported under the name the project now has. The label this
  // form opened with is the one name that is gone, so naming it would tell the
  // reader their rename had happened to something else — and the answer carries
  // the new one, since a rename derives a label the reader never typed.
  const reported = panelMessages();
  if (reported.length !== 1 || reported[0] !== "Updated Waiting On.") {
    throw new Error("the rename reported " + JSON.stringify(reported));
  }
  if (panelStatuses().join(",") !== "icebox,waiting,shipped") {
    throw new Error("the panel is drawing " + panelStatuses().join(",") + " after the rename");
  }
`)
}

// A panel opened for a project whose statuses cannot be read says so, and offers
// no controls that would compose a change against nothing.
func TestClientStatusesPageReportsAVocabularyItCannotRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "an unreadable vocabulary", vocabulary, "head-7", nil, `
  vocabularyRead = { format: "workbook.error", version: 1,
    error: { category: "corrupt-data", message: "cannot read this project's status configuration" } };
  await openStatuses();
  const said = panelMessages();
  if (said.length !== 1 || said[0] !== "cannot read this project's status configuration") {
    throw new Error("the panel said " + JSON.stringify(said));
  }
  if (panelStatuses().length !== 0) throw new Error("the panel listed statuses it never read");
  if (!findElement(vocabularyPanelBody, (element) => hasDataKey(element, "vocabularyPanelEmpty"))) {
    throw new Error("the panel drew no explanation in place of the list");
  }
  if (findElement(vocabularyPanelBody, (element) => hasDataKey(element, "vocabularyAdd"))) {
    throw new Error("the panel offered to add a status to a vocabulary it could not read");
  }
`)
}

// The statuses page is a page like the others: it is what main is drawing while
// the reader is on it, and it goes when they walk anywhere else — a task's own
// page as readily as the board.
func TestClientStatusesRouteIsOneRouteAmongTheOthers(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	runPanelClient(t, "leaving the statuses page for a task", vocabulary, "head-7", []core.Task{task}, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  await openStatuses();
  if (!statusesRoute()) throw new Error("the route drew no statuses page");

  // Following a card's link is a route away from it, and the render it performs
  // is what puts the page away.
  const link = new TestElement("a");
  link.href = window.location.origin + "/tasks/" + encodeURIComponent(`+strconv.Quote(task.ID)+`);
  await follow(link);
  if (window.location.href.indexOf("/tasks/") < 0) throw new Error("the link did not leave the statuses page: " + window.location.href);
  if (statusesRoute()) throw new Error("the statuses page stayed in main over a task's page");
  if (vocabularyPanel.hidden !== true) throw new Error("the statuses page stayed mounted over a task's page");
  if (vocabularyPanel.parentElement) throw new Error("the statuses page is still hanging off the document");
`)
}

// A hard load of /statuses is the page, drawn from the address rather than from
// a click. It is the same document the board is served from — the server answers
// every page route with it — so a bookmark, a reload and a middle-click all land
// on the statuses page rather than on the board.
func TestClientStatusesRouteRendersOnADirectLoad(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	// The answer is prepared before the client script runs, because a hard load
	// reads the statuses in its first render rather than waiting to be asked.
	runStatusesClient(t, "a hard load of the statuses page", "/config",
		"vocabularyRead = "+panelVocabularyJSON(t, panelRenamedVocabulary(t), "head-7")+";\n",
		vocabulary, "head-7", []core.Task{task}, `
  await settle();
  if (!statusesRoute()) throw new Error("the first render drew no statuses page into main");
  if (document.title !== "Configuration · Workbook") throw new Error("the page is titled " + JSON.stringify(document.title));
  if (vocabularyCalls.length !== 1 || vocabularyCalls[0].url !== "/api/vocabulary") {
    throw new Error("the load asked for " + JSON.stringify(vocabularyCalls.map((call) => call.url)));
  }
  if (panelStatuses().join(",") !== "icebox,queued,triage,shipped") {
    throw new Error("the page listed " + panelStatuses().join(","));
  }
  // The board is not drawn under it, and walking back is what draws it: the
  // columns the server rendered, in main, with the card it served.
  if (findElement(main, (element) => element === boardView)) throw new Error("the board was drawn under the statuses page");
  await returnToBoard();
  if (main.children.length !== 1 || main.children[0] !== boardView) throw new Error("Back drew something other than the board");
  if (document.title !== "Workbook board") throw new Error("the board is titled " + JSON.stringify(document.title));
  if (!boardCard(`+strconv.Quote(task.ID)+`)) throw new Error("the board came back without its card");
  if (historyPaths[historyPaths.length - 1] !== "/") throw new Error("Back pushed " + JSON.stringify(historyPaths));
`)
}

// A page served without the statuses markup has no statuses route, and says so
// in the words every address this client does not have is answered in.
//
// It is the client half of the server's 404. A board built without the four
// vocabulary mutations is served no link, no page body and no capability, so
// drawing the route from the address alone would put a shell in front of a
// reader with nothing the client could fill it from.
func TestClientStatusesRouteIsNotARouteWithoutTheMarkup(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runStatusesClient(t, "a statuses address on a board that cannot administer them",
		"/config", withoutStatusAdministration, vocabulary, "head-7", nil, `
  await settle();
  if (document.title !== "Page not found · Workbook") throw new Error("the page is titled " + JSON.stringify(document.title));
  if (vocabularyCalls.length !== 0) {
    throw new Error("a board that cannot change its statuses read them anyway: " + JSON.stringify(vocabularyCalls.map((call) => call.url)));
  }
  const drawn = main.children[0];
  if (!drawn || drawn.textContent.indexOf("does not exist") < 0) {
    throw new Error("main is drawing " + JSON.stringify(drawn && drawn.textContent));
  }
`)
}

// What the board is showing when the reader comes back from a change they made.
//
// The columns are the ones they left, down to the node: main re-attaches the
// board rather than rebuilding it, so every card is the card it was, holding
// whatever it was holding. The standing notice is what says the statuses have
// moved on, and it says so from outside main, which is why it is already up
// while the reader is still on the statuses page and still up when they return.
// Reloading is the reader's to choose, as it is for a change another clone made.
func TestClientBoardKeepsItsColumnsAcrossAStatusChange(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
		clientPlacementTask("WB-01J0000000000000000000B202", "Queued", core.Status("queued"), core.PriorityHigh),
	}
	runPanelClient(t, "returning to the board after a status change", vocabulary, "head-7", tasks, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  const held = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  if (held.length !== 2) throw new Error("the board did not draw both cards");
  held.forEach((node, index) => { node.__witness = "card-" + index; });
  const columnsBefore = boardLists.map((list) => list.dataset.status).join(",");

  await openStatuses();
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);
  // The notice is readable from here: it sits outside main, so it is not a
  // thing the reader has to go back to the board to be told.
  if (vocabularyNotice.hidden !== false) throw new Error("the change raised no notice while the reader was still on the page");

  await returnToBoard();
  if (main.children.length !== 1 || main.children[0] !== boardView) throw new Error("Back drew something other than the board");
  if (vocabularyNotice.hidden !== false) throw new Error("returning to the board took the notice down");
  if (boardLists.map((list) => list.dataset.status).join(",") !== columnsBefore) {
    throw new Error("returning to the board rebuilt its columns as " + boardLists.map((list) => list.dataset.status).join(","));
  }
  const after = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  held.forEach((node, index) => {
    if (after[index] !== node || after[index].__witness !== "card-" + index) {
      throw new Error("card " + index + " was rebuilt by a walk to the statuses page and back");
    }
  });
  // And the reload the notice offers is still the reader's to press.
  if (reloadCalls !== 0) throw new Error("something reloaded the board without being asked");
  vocabularyReload.eventListeners.click();
  if (reloadCalls !== 1) throw new Error("the notice's reload did nothing");
`)
}
