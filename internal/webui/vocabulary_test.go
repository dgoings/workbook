package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the board's client does with a project's own statuses.
//
// The page no longer carries a status list; it carries one labelled column per
// status and one attribute naming the default, and the script reads both back
// out of the DOM. These tests drive that script against a vocabulary that
// shares nothing with the built-in six, so anything still reaching for a fixed
// table fails here rather than in a project nobody tested with.

// runVocabularyClient renders a page for a project with these statuses,
// executes its client script against a fake DOM built the same way, and runs
// body with the route already rendered.
func runVocabularyClient(t *testing.T, purpose, url string, vocabulary core.Vocabulary, head string, tasks []core.Task, body string) {
	t.Helper()
	node := requireNode(t)
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(vocabulary, head),
		List:       func(context.Context) ([]core.Task, error) { return tasks, nil },
	})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: head,
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarnessWith(url, string(document), vocabulary, head) + script + `
setTimeout(async () => {
` + body + `
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// The form's status select offers the project's statuses, under the project's
// labels, in the project's order — because it is built from the columns the
// server rendered rather than from a list the client keeps. A form offering a
// status the project does not define is a save that fails after the reader has
// filled it in.
func TestHandlerClientBuildsTheStatusSelectFromTheRenderedColumns(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	runVocabularyClient(t, "status select options", "/tasks/new", vocabulary, "head-1", []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-status");
  if (!control) throw new Error("the New Task form has no status select");
  const options = control.children.map((option) => [option.value, option.textContent]);
  const want = `+string(mustJSON(t, statusPairs(handlerVocabulary(t))))+`;
  if (JSON.stringify(options) !== JSON.stringify(want)) {
    throw new Error("status options = " + JSON.stringify(options) + ", want " + JSON.stringify(want));
  }
`)
}

// A new task with no status named lands in the status the project tagged
// default, which the server rendered into the page. It used to land in
// "backlog", which is a status a project need not define at all.
func TestHandlerClientNewTaskDefaultsToTheDefaultTaggedStatus(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	// The default is deliberately not the first column, so a client that
	// substituted "the first one" would still fail.
	if got := vocabulary.Definitions()[0].Status; got == vocabulary.Default() {
		t.Fatalf("fixture default %q is the first column, which makes the assertion vacuous", got)
	}
	runVocabularyClient(t, "new task default status", "/tasks/new", vocabulary, "head-1", []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-status");
  if (!control) throw new Error("the New Task form has no status select");
  if (control.value !== "queued") {
    throw new Error("a new task with no status named would be created in " + JSON.stringify(control.value));
  }
  if (typeof boardView.dataset.defaultStatus !== "string" || boardView.dataset.defaultStatus !== "queued") {
    throw new Error("the page did not carry the project's default status");
  }
`)
	// A status the project does not define is not honoured either, and the
	// fallback is the same one.
	runVocabularyClient(t, "new task with a foreign status", "/tasks/new?status=backlog", vocabulary, "head-1", []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-status");
  if (control.value !== "queued") {
    throw new Error("a foreign ?status= was honoured as " + JSON.stringify(control.value));
  }
`)
	// One the project does define is honoured.
	runVocabularyClient(t, "new task with a project status", "/tasks/new?status=icebox", vocabulary, "head-1", []core.Task{}, `
  const control = findElement(main, (element) => element.id === "task-status");
  if (control.value !== "icebox") {
    throw new Error("a project status was not honoured: " + JSON.stringify(control.value));
  }
`)
}

func statusPairs(vocabulary core.Vocabulary) [][2]string {
	definitions := vocabulary.Definitions()
	pairs := make([][2]string, len(definitions))
	for index, definition := range definitions {
		pairs[index] = [2]string{string(definition.Status), definition.Label}
	}
	return pairs
}

// A card in a column the project invented drags and drops like any other,
// because the client's idea of a droppable column is the set of columns the
// server rendered. Nothing on this path names a status the client was born
// knowing.
func TestHandlerClientDropsIntoAColumnTheProjectInvented(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	task := clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium)
	task.Head = "head-a"
	runVocabularyClient(t, "drop into a custom column", "/", vocabulary, "head-1", []core.Task{task}, `
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  if (!shipped) throw new Error("the board rendered no Shipped column");
  const dragged = boardCard(`+strconv.Quote(task.ID)+`);
  if (!dragged) throw new Error("the board drew no card for the icebox task");
  if (dragged.draggable !== true) throw new Error("a card in a project status was rendered undraggable");

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  dragged.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: dragged, dataTransfer });
  await documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: dragged });

  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the drop into a project column sent nothing");
  if (!wrote.url.endsWith("/position")) throw new Error("the drop sent " + wrote.url);
  const body = JSON.parse(wrote.options.body);
  if (body.status !== "shipped") throw new Error("the drop proposed " + JSON.stringify(body.status));
`)
}

// A vocabulary change under an open page is announced and nothing else: the
// board keeps the columns it was served with, and every card node the reader is
// looking at is the same node afterwards.
//
// Node identity is the protected invariant, not a nicety. An open edit form, a
// change staged against a head, and a refusal banner nobody has read yet all
// hang off a card node; rebuilding the columns to show a new one would take all
// three away to show a column the reader may not care about. So the page says
// it is out of date and offers a reload, and the reader picks the moment.
func TestHandlerClientAnnouncesAVocabularyChangeWithoutRebuildingTheBoard(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Frozen", core.Status("icebox"), core.PriorityMedium),
		clientPlacementTask("WB-01J0000000000000000000B202", "Queued", core.Status("queued"), core.PriorityHigh),
	}
	served := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-2",
		Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	runVocabularyClient(t, "mid-session vocabulary change", "/", vocabulary, "head-1", tasks, `
  const held = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  if (held.length !== 2) throw new Error("the board did not draw both cards: " + held.length);
  // A property no renderer writes, so finding it afterwards proves the node
  // survived rather than that an equal one was built.
  held.forEach((node, index) => { node.__witness = "card-" + index; });
  const columnsBefore = boardLists.map((list) => list.dataset.status).join(",");

  if (vocabularyNotice.hidden !== true) throw new Error("the notice was showing before anything changed");
  // A poll carrying the head the page was served with says nothing.
  await intervalCallback();
  if (vocabularyNotice.hidden !== true) throw new Error("an unchanged vocabulary raised the notice");

  taskResponse = `+string(served)+`;
  await intervalCallback();

  if (vocabularyNotice.hidden !== false) throw new Error("a changed vocabulary raised no notice");
  const after = boardLists.flatMap((list) => list.querySelectorAll(".task-card"));
  if (after.length !== held.length) throw new Error("the board changed its card count: " + after.length);
  held.forEach((node, index) => {
    if (after[index] !== node) throw new Error("the notice rebuilt card " + index);
    if (after[index].__witness !== "card-" + index) throw new Error("card " + index + " was replaced by an equal-looking one");
  });
  if (boardLists.map((list) => list.dataset.status).join(",") !== columnsBefore) {
    throw new Error("the columns were rebuilt live");
  }

  // Said once, not once per poll.
  vocabularyNotice.hidden = true;
  await intervalCallback();
  if (vocabularyNotice.hidden !== true) throw new Error("the notice came back after the reader dismissed it");

  // And the control does what it says.
  vocabularyReload.eventListeners.click();
  if (reloadCalls !== 1) throw new Error("the reload control reloaded " + reloadCalls + " times");
`)
}

// The client script must not name a status at all.
//
// Every status it once knew is a status a project is free not to define, so a
// literal surviving anywhere in the script is a second answer to a question the
// server has already answered on the page — and the one place a project that
// customized its statuses would find the old six looking back.
func TestClientScriptNamesNoStatusOfItsOwn(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	for _, definition := range core.DefaultVocabulary().Definitions() {
		if literal := strconv.Quote(string(definition.Status)); strings.Contains(script, literal) {
			t.Errorf("the client script names the built-in status %s, which a project need not define", literal)
		}
	}
}
