package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the board's status administration panel does, and what it refuses to do.
//
// The panel is the one writer in this client that is outside the optimistic
// mutation queue, so most of what is pinned here is about the two rules that
// follow from that: a change waits for the queue to be empty before it goes,
// and a stale write is where the change stops. The rest is request shape — the
// head the panel read has to be the head it names — and the standing invariant
// the whole board rests on: no vocabulary change rebuilds a column under the
// reader.

// administrableHandler is a board with the four vocabulary mutations, which is
// what `workbook serve` builds and the only kind that draws the panel. The
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

// panelFetchHarness replaces the fake fetch with one that records what the panel
// sends and answers the vocabulary routes from values the test sets.
//
// The answers are the server's own documents, encoded by the same builders the
// routes use, so a client that reads a member the server does not serve fails
// here rather than in a browser.
const panelFetchHarness = `
const vocabularyCalls = [];
// What GET /api/vocabulary answers, what the next mutation answers, and a gate
// a task write waits on so a test can hold the optimistic queue open.
let vocabularyRead = null;
let vocabularyAnswer = null;
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
// Opens the panel the way a reader does, and waits for the read it starts.
async function openPanel() {
  vocabularyPanelToggle.eventListeners.click();
  await settle();
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
// recording fetch.
func runPanelClient(t *testing.T, purpose string, vocabulary core.Vocabulary, head string, tasks []core.Task, body string) {
	t.Helper()
	node := requireNode(t)
	handler := administrableHandler(vocabulary, head, tasks)
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: head,
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarnessWith("/", string(document), vocabulary, head) +
		panelFetchHarness + script + `
setTimeout(async () => {
` + body + `
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

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

// The entry point is on the board and is one word, and it is there for a project
// that configured its own statuses exactly as it is for one that never did.
//
// It is a button rather than a link because it opens a region on this page, and
// it ships hidden: the panel is drawn entirely by the script, so a page whose
// script never ran offers no way into it rather than a dead control.
func TestHandlerBoardCarriesTheStatusPanelEntryPoint(t *testing.T) {
	for name, vocabulary := range map[string]core.Vocabulary{
		"default": core.DefaultVocabulary(),
		"custom":  handlerVocabulary(t),
	} {
		body := boardMarkup(t, administrableBoardPage(t, vocabulary))
		toggle := elementTag(t, body, "data-vocabulary-panel-toggle")
		for _, attribute := range []string{
			`<button`,
			`type="button"`,
			`class="header-link"`,
			// It says what it controls and whether that is open, which is the
			// whole of the state: a name that changed with the state as well
			// would say it twice and in opposite directions.
			`aria-controls="vocabulary-panel"`,
			`aria-expanded="false"`,
			`hidden`,
		} {
			if !strings.Contains(toggle, attribute) {
				t.Errorf("%s vocabulary drew a status entry point %q, which does not carry %q", name, toggle, attribute)
			}
		}
		panel := elementTag(t, body, "data-vocabulary-panel ")
		for _, attribute := range []string{
			`<section`,
			`id="vocabulary-panel"`,
			`aria-labelledby="vocabulary-panel-title"`,
			`hidden`,
			// The roles a status may carry are the server's answer, rendered
			// here because the client must not hold a second copy of them.
			`data-status-tags="default done next"`,
		} {
			if !strings.Contains(panel, attribute) {
				t.Errorf("%s vocabulary drew a status panel %q, which does not carry %q", name, panel, attribute)
			}
		}
		// The panel is a shell: the list inside it is the client's, drawn from
		// what the server answers rather than from the columns on the page.
		if !strings.Contains(body, `data-vocabulary-panel-body`) {
			t.Errorf("%s vocabulary drew no mount for the panel's status list", name)
		}
	}
}

// A board built without the four mutations offers no way into a panel whose
// every control would be answered "this board has no such capability".
//
// All four rather than any, because the panel is one surface: a board carrying
// three of them would draw controls that look alike and fail differently.
func TestHandlerBoardWithoutVocabularyMutationsOffersNoStatusPanel(t *testing.T) {
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
		// the panel's parts by name whether or not they are there, which is
		// exactly what makes it safe to serve to a board without them.
		markup := boardMarkup(t, body)
		for _, marker := range []string{
			"data-vocabulary-panel-toggle",
			`id="vocabulary-panel"`,
			"data-vocabulary-panel-body",
		} {
			if strings.Contains(markup, marker) {
				t.Errorf("%s: the page carries %q for a board that cannot change its statuses", name, marker)
			}
		}
		// The board itself is untouched: it still draws its columns and still
		// says which vocabulary it drew them from.
		if !strings.Contains(body, `data-vocabulary-head="head-1"`) {
			t.Errorf("%s: the board stopped reporting the vocabulary head", name)
		}
	}
}

// Opening the panel reads the project's statuses rather than drawing the ones
// the page happens to be showing.
//
// The page carries a token and a label per column and nothing else, and its head
// may be minutes old by the time anyone opens this. A change composed against
// what the page remembers is the stale write the panel would rather not have to
// report, so it asks — every time it is opened, not once.
func TestClientStatusPanelReadsTheProjectsStatusesWhenItOpens(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "opening the status panel", vocabulary, "head-1", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, panelRenamedVocabulary(t), "head-9")+`;
  if (vocabularyPanel.hidden !== true) throw new Error("the panel was open before anyone asked for it");
  if (vocabularyPanelToggle.hidden !== false) throw new Error("the board route hid the panel's entry point");
  await openPanel();

  if (vocabularyPanel.hidden !== false) throw new Error("the entry point did not open the panel");
  if (vocabularyPanelToggle.getAttribute("aria-expanded") !== "true") throw new Error("the entry point does not report the panel as open");
  if (vocabularyCalls.length !== 1 || vocabularyCalls[0].method !== "GET" || vocabularyCalls[0].url !== "/api/vocabulary") {
    throw new Error("opening the panel asked for " + JSON.stringify(vocabularyCalls.map((call) => call.method + " " + call.url)));
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

  // Closing and opening it again asks again, because another clone may have
  // changed the statuses in between.
  vocabularyPanelClose.eventListeners.click();
  if (vocabularyPanel.hidden !== true) throw new Error("Close left the panel open");
  if (vocabularyPanelToggle.getAttribute("aria-expanded") !== "false") throw new Error("Close left the entry point reporting an open panel");
  await openPanel();
  if (vocabularyCalls.length !== 2) throw new Error("re-opening the panel asked " + vocabularyCalls.length + " times in total");

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
func TestClientStatusPanelAddsAStatusAgainstTheHeadItRead(t *testing.T) {
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
  await openPanel();

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
  const warned = findElements(vocabularyPanelStatus, (element) => hasDataKey(element, "vocabularyPanelWarning"));
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
func TestClientStatusPanelSendsTheEmptyHeadOfAnUnseededProject(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "administering an unseeded project", vocabulary, "", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-1", VocabularyTaskCounts{}, nil)+` };
  await openPanel();
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

// A status change waits for the board's own writes to finish.
//
// A pending intent can be carrying the very status a change retires, and it was
// composed against the columns on screen. The panel says it is waiting rather
// than appearing to have ignored the press, and sends once the queue is empty.
func TestClientStatusPanelWaitsForPendingBoardChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	task.Head = "head-a"
	runPanelClient(t, "a status change behind a pending intent", vocabulary, "head-7", []core.Task{task}, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openPanel();

  // Hold the board's write open, then make one: the queue is now non-empty.
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
func TestClientStatusPanelStopsAtAStaleWrite(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a status change refused as stale", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { ok: false, body: `+panelStaleWriteJSON(t, panelRenamedVocabulary(t), "head-8",
		"this project's statuses have changed since head-7; reload and try again")+` };
  await openPanel();

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
func TestClientStatusPanelPricesARemoval(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "removing a status", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8",
		VocabularyTaskCounts{Affected: 3, ClaimableAfter: 2}, nil)+` };
  await openPanel();

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
func TestClientStatusPanelQuotesARefusalItDidNotMake(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "a refused status change", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { ok: false, body: `+panelRefusalJSON(t, core.CategoryValidation,
		`unsupported status tag "blocked"`)+` };
  await openPanel();

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
func TestClientStatusPanelReordersInOneRequestPerGesture(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "reordering the columns", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openPanel();

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
  await openPanel();

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

// The panel's stylesheet keeps the page's one column-width contract and adds no
// second one of its own.
func TestHandlerStatusPanelIsStyledWithinThePagesLayout(t *testing.T) {
	body := administrableBoardPage(t, handlerVocabulary(t))
	rule := declarationBlock(t, body, ".admin {")
	for _, fragment := range []string{
		// It shares the viewport-height flex column with main and the notices,
		// so it can run out of room rather than out of page.
		"flex: 0 1 auto",
		"max-height: 50vh",
		"overflow: auto",
	} {
		if !strings.Contains(rule, fragment) {
			t.Errorf("the panel's rule %q does not contain %q", rule, fragment)
		}
	}
	if !strings.Contains(body, ".admin[hidden] { display: none; }") {
		t.Error("the panel's display rule does not defeat the hidden attribute it ships with")
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
func TestClientStatusPanelRefusesAnAnswerItCannotRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "an unreadable mutation answer", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: { format: "workbook.tasks", version: 1 } };
  await openPanel();
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
func TestClientStatusPanelEditsOnlyWhatChanged(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "editing one member of a status", vocabulary, "head-7", nil, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  vocabularyAnswer = { body: `+panelMutationJSON(t, panelRenamedQueuedVocabulary(t), "head-8", VocabularyTaskCounts{}, nil)+` };
  await openPanel();

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
func TestClientStatusPanelReportsAVocabularyItCannotRead(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runPanelClient(t, "an unreadable vocabulary", vocabulary, "head-7", nil, `
  vocabularyRead = { format: "workbook.error", version: 1,
    error: { category: "corrupt-data", message: "cannot read this project's status configuration" } };
  await openPanel();
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

// The panel travels with the board, because the columns it administers are the
// board's. It is closed on the way out rather than left standing over a task's
// own page.
func TestClientStatusPanelTravelsWithTheBoardRoute(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	runPanelClient(t, "leaving the board with the panel open", vocabulary, "head-7", []core.Task{task}, `
  vocabularyRead = `+panelVocabularyJSON(t, vocabulary, "head-7")+`;
  await openPanel();
  if (vocabularyPanel.hidden !== false) throw new Error("the panel did not open");

  // Following a card's link is what leaves the board, and the route render it
  // performs is what closes the panel.
  const link = new TestElement("a");
  link.href = window.location.origin + "/tasks/" + encodeURIComponent(`+strconv.Quote(task.ID)+`);
  await documentEventListeners.click({ target: link, button: 0, preventDefault() {} });
  await settle();
  if (window.location.href.indexOf("/tasks/") < 0) throw new Error("the link did not leave the board: " + window.location.href);
  if (vocabularyPanel.hidden !== true) throw new Error("the panel stayed open over a task's page");
  if (vocabularyPanelToggle.hidden !== true) throw new Error("the entry point stayed on a route it does nothing on");
`)
}
