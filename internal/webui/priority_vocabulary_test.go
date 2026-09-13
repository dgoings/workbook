package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the board's client does with a project's own priorities.
//
// The page carries this project's priorities in an attribute — token, label,
// role, in configured order — and the script has to read all three facts back
// out of it, exactly as it reads this project's statuses out of the columns the
// server rendered. These tests drive that script against a vocabulary that
// shares two tokens with the built-in three and disagrees with it about
// everything else, so anything still reaching for high/medium/low fails here
// rather than in a project nobody tested with.

// projectPriorities is a project that renamed, added and dropped priorities:
// `urgent` above the built-in three, `medium` gone, `soon` in its place and
// carrying the default role, and `urgent` under a label a person chose.
//
// It is deliberately hostile to every hard-coding the client used to hold. The
// default is neither "medium" nor the first member, the order cannot be derived
// from the built-in three, and the label of the priority a project is most
// likely to add is nothing like its token.
func projectPriorities(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	vocabulary, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}},
		{Priority: core.PriorityHigh, Label: "High", Rank: "2/1", Tags: []core.PriorityTag{}},
		{Priority: "soon", Label: "Soon", Rank: "3/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "4/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// priorityBoardHandler serves a board for a project with these priorities and
// the statuses a project that has configured none is using.
func priorityBoardHandler(priorities core.PriorityVocabulary, tasks []core.Task) http.Handler {
	return NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: core.LegacyVocabulary(),
				Head:       "head-1",
				Priorities: priorities,
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
	})
}

// runPriorityClient renders a page for a project with these priorities,
// executes its client script against a fake DOM carrying the same attribute the
// server rendered, and runs body with the route already rendered.
//
// The attribute is written with pagePriorities — the encoder the page itself
// uses — rather than with a literal, so a client test cannot drift from what
// the server publishes. It is set after the shared harness rather than through
// it because the harness answers for a project that configured no priorities at
// all, which is what every test that is not about them wants.
func runPriorityClient(t *testing.T, purpose, url string, priorities core.PriorityVocabulary, tasks []core.Task, body string) {
	t.Helper()
	runPriorityClientReporting(t, purpose, url, priorities, tasks, body)
}

// runPriorityClientReporting is runPriorityClient, handing back what the script
// printed. A test that has to answer a request the client built — rather than
// only inspect what it drew — reads the request off this output and puts it
// through the server in Go.
func runPriorityClientReporting(t *testing.T, purpose, url string, priorities core.PriorityVocabulary, tasks []core.Task, body string) string {
	t.Helper()
	node := requireNode(t)
	handler := priorityBoardHandler(priorities, tasks)
	response := request(t, handler, http.MethodGet, url)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d; body = %s", url, response.Code, http.StatusOK, response.Body.String())
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarnessWith(url, string(document), core.LegacyVocabulary(), "head-1") + `
boardView.dataset.priorities = ` + strconv.Quote(pagePriorities(priorities)) + `;
boardView.dataset.defaultPriority = ` + strconv.Quote(string(priorities.Default())) + `;
` + script + `
setTimeout(async () => {
` + body + `
}, 0);
`
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
	return string(output)
}

// priorityPairs is what the form's select has to offer: this project's
// priorities, under this project's labels, in this project's order.
func priorityPairs(priorities core.PriorityVocabulary) [][2]string {
	definitions := priorities.EffectiveDocument().Priorities
	pairs := make([][2]string, len(definitions))
	for index, definition := range definitions {
		pairs[index] = [2]string{string(definition.Priority), definition.Label}
	}
	return pairs
}

// The form's priority select offers the project's priorities, under the
// project's labels, in the project's order — because it is built from the
// priorities the server published on the page rather than from a list the
// client keeps. A form that cannot offer a priority is a priority nobody can
// assign from the board, which is the shipped defect this fixes.
func TestHandlerClientBuildsThePrioritySelectFromTheProjectsPriorities(t *testing.T) {
	priorities := projectPriorities(t)
	runPriorityClient(t, "priority select options", "/tasks/new", priorities, []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the New Task form has no priority select");
  const options = control.children.map((option) => [option.value, option.textContent]);
  const want = `+string(mustJSON(t, priorityPairs(priorities)))+`;
  if (JSON.stringify(options) !== JSON.stringify(want)) {
    throw new Error("priority options = " + JSON.stringify(options) + ", want " + JSON.stringify(want));
  }
`)
}

// A task already at a priority the old build could not offer opens with that
// priority selected. A form that silently displayed a neighbour would turn a
// reader's unrelated save into a priority change they never made.
func TestHandlerClientShowsTheTasksOwnPriorityInTheForm(t *testing.T) {
	task := clientPlacementTask("WB-01J0000000000000000000C303", "Fire", core.StatusReady, "urgent")
	task.Head = "head-a"
	runPriorityClient(t, "a task at a project priority", "/tasks/"+task.ID, projectPriorities(t), []core.Task{task}, `
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the task form has no priority select");
  if (control.value !== "urgent") {
    throw new Error("the form shows the priority as " + JSON.stringify(control.value));
  }
`)
}

// A new task with no priority named lands on the priority the project tagged
// default, which the server rendered into the page beside the default status.
// It used to land on "medium", which is a priority a project need not define at
// all — and this project does not.
func TestHandlerClientNewTaskDefaultsToTheDefaultTaggedPriority(t *testing.T) {
	priorities := projectPriorities(t)
	definitions := priorities.EffectiveDocument().Priorities
	if got := definitions[0].Priority; got == priorities.Default() {
		t.Fatalf("fixture default %q is the first priority, which makes the assertion vacuous", got)
	}
	if priorities.Has(core.PriorityMedium) {
		t.Fatal("fixture defines medium, so a client still hard-coding it would pass")
	}
	runPriorityClient(t, "new task default priority", "/tasks/new", priorities, []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the New Task form has no priority select");
  if (control.value !== "soon") {
    throw new Error("a new task with no priority named would be created at " + JSON.stringify(control.value));
  }
  if (boardView.dataset.defaultPriority !== "soon") {
    throw new Error("the page did not carry the project's default priority");
  }
`)
	// And the page really does carry it, rather than the harness having been
	// told it: the attribute is the server's answer, on the served board.
	served := request(t, priorityBoardHandler(priorities, nil), http.MethodGet, "/")
	if !strings.Contains(served.Body.String(), `data-default-priority="soon"`) {
		t.Error("the served board does not name the project's default priority")
	}
}

// A card dragged into a column that holds no card of its own priority lands in
// its own priority band, and the band is the one the project configured. The
// client used to place it against a fixed high/medium/low order, so a card at a
// priority outside those three sorted against nothing and fell to the bottom of
// the column whatever its rank said.
func TestHandlerClientPlacesADraggedCardByTheConfiguredPriorityOrder(t *testing.T) {
	dragged := clientPlacementTask("WB-01J0000000000000000000D404", "Fire", core.StatusBacklog, "urgent")
	dragged.Head = "head-a"
	high := clientPlacementTask("WB-01J0000000000000000000D505", "Important", core.StatusReady, core.PriorityHigh)
	low := clientPlacementTask("WB-01J0000000000000000000D606", "Whenever", core.StatusReady, core.PriorityLow)
	tasks := []core.Task{dragged, high, low}
	runPriorityClient(t, "drag into a configured priority band", "/", projectPriorities(t), tasks, `
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  if (!ready) throw new Error("the board rendered no Ready column");
  const card = boardCard(`+strconv.Quote(dragged.ID)+`);
  const highCard = boardCard(`+strconv.Quote(high.ID)+`);
  const lowCard = boardCard(`+strconv.Quote(low.ID)+`);
  if (!card || !highCard || !lowCard) throw new Error("the board did not draw all three cards");
  card.rect = { top: 0, bottom: 80 };
  highCard.rect = { top: 0, bottom: 80 };
  lowCard.rect = { top: 100, bottom: 180 };

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  // Released well below both cards, so nothing but the priority order decides
  // where it lands.
  documentEventListeners.dragover({ target: ready, clientY: 1000, dataTransfer, preventDefault() {} });

  // Urgent sits above high in this project's order, so the line shows the card
  // landing above the high card however far down the column it was released.
  const markerIndex = ready.children.findIndex((item) => item.className === "drop-marker");
  const highIndex = ready.children.indexOf(highCard);
  if (markerIndex < 0) throw new Error("the drag over a project column showed no placement at all");
  if (markerIndex !== highIndex - 1) {
    throw new Error("the urgent card was placed at " + markerIndex + ", want the band above high at " + (highIndex - 1));
  }

  await documentEventListeners.drop({ target: ready, clientY: 1000, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });
  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the drop sent nothing");
  if (JSON.parse(wrote.options.body).status !== "ready") {
    throw new Error("the drop proposed " + wrote.options.body);
  }
`)
}

// The priority chip on a card is the priority's token, not its label, and both
// halves of the board agree about it.
//
// The chip is a compact uppercase element in the card's metadata row, beside
// the ID prefix a person copies into a command, and the token is what
// `--priority` takes. A label is prose a project chose and may be a sentence;
// the select and the configuration page are where it is read. What must never
// happen is the two render paths disagreeing — a card drawn by the server and
// the same card redrawn by a poll would then flip between two names.
func TestBoardDrawsThePriorityChipAsItsToken(t *testing.T) {
	task := clientPlacementTask("WB-01J0000000000000000000E505", "Fire", core.StatusReady, "urgent")
	task.Head = "head-a"
	handler := priorityBoardHandler(projectPriorities(t), []core.Task{task})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, `<span class="priority priority--urgent">urgent</span>`) {
		t.Error("the server-rendered card does not draw the priority chip as its token")
	}

	runPriorityClient(t, "client-rendered priority chip", "/", projectPriorities(t), []core.Task{task}, `
  const card = boardCard(`+strconv.Quote(task.ID)+`);
  if (!card) throw new Error("the board drew no card");
  const chip = findElement(card, (element) => hasClassToken(element, "priority"));
  if (!chip) throw new Error("the card carries no priority chip");
  if (chip.textContent !== "urgent") {
    throw new Error("the client draws the priority chip as " + JSON.stringify(chip.textContent));
  }
  if (!hasClassToken(chip, "priority--urgent")) {
    throw new Error("the chip carries no class the served ink can reach: " + chip.className);
  }
`)
}

// The client script must not name a priority at all.
//
// Every priority it once knew is a priority a project is free not to define, so
// a literal surviving anywhere in the script is a second answer to a question
// the server has already answered on the page — and the one place a project
// that renamed its priorities would find the old three looking back.
func TestClientScriptNamesNoPriorityOfItsOwn(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	for _, definition := range (core.PriorityVocabulary{}).EffectiveDocument().Priorities {
		if literal := strconv.Quote(string(definition.Priority)); strings.Contains(script, literal) {
			t.Errorf("the client script names the priority %s, which a project need not define", literal)
		}
	}
}
