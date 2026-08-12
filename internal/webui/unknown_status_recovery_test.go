package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the board offers a reader whose task holds a status this project cannot
// resolve.
//
// The region those tasks sit in is a display, not a seventh column: nothing
// drops into it, because there is no status such a drop would name. What its
// cards now do is drag *out*, which is the whole recovery — the write a drop
// sends names a status the project defines, so it is an ordinary status change
// and the mutation boundary has nothing to refuse.

// strandedStatus is a status no fixture vocabulary defines and no alias
// forwards, so a task holding it lands in the unknown-status region.
const strandedStatus = core.Status("ghost")

// The region's copy is the only place a reader is told why their task is here
// and what to do about it, so both halves are pinned.
//
// Two causes, because a card lands here either genuinely — a status nothing
// forwards — or temporarily, while an open page draws columns the project has
// moved on from; naming only the first would tell a reader their work is
// stranded when a reload would file it. Two ways out, because dragging is the
// quick one and the form is the one a keyboard reaches. And nothing that reads
// as "this task is stuck", which is what the copy used to imply back when the
// cards could not be dragged.
func TestHandlerUnknownStatusRegionCopyNamesBothCausesAndBothWaysOut(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		// The two causes.
		"hold a status this project does not define and nothing forwards to one",
		"statuses have changed since this page was opened",
		"offers the reload that files them",
		// The two ways out, and the one direction that stays closed.
		"drag one onto any column to file it there",
		"open it and edit it like any other task",
		"Nothing drops back into this region",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the unknown-status region copy does not say %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "only naming a status the project does not define is refused") {
		t.Error("the region copy still describes the cards as unmovable")
	}
}

// A card in the unknown-status region drags into a column, and that is an
// ordinary status change: the same PATCH every other drop sends, and the same
// card node afterwards.
//
// Node identity is the protected invariant here as everywhere on this board. A
// stranded card is one a reader may have opened, staged a change against, or
// left an unread refusal on, and filing it must not be the one move that throws
// all three away. So this asserts the node, not just the status.
func TestHandlerClientDragsACardOutOfTheUnknownStatusRegion(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	stranded := clientPlacementTask("WB-01J0000000000000000000A101", "Written elsewhere", strandedStatus, core.PriorityMedium)
	stranded.Head = "head-a"
	settled := stranded
	settled.Status = core.Status("shipped")
	settled.Head = "head-b"
	served := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{settled}, Presentation: presentationForTasks([]core.Task{settled}),
	})

	runVocabularyClient(t, "drag out of the unknown-status region", "/", vocabulary, "head-1", []core.Task{stranded}, `
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  if (!shipped) throw new Error("the board rendered no Shipped column");
  const card = boardCard(`+strconv.Quote(stranded.ID)+`);
  if (!card) throw new Error("the board drew no card for the stranded task");
  if (card.parentElement !== boardUnknownList) throw new Error("the stranded card is not in the unknown-status region");
  if (boardUnknownSection.dataset.visible !== "true") throw new Error("the region holding a card stayed hidden");
  if (card.draggable !== true) throw new Error("the stranded card offers no drag, so there is no way out of the region");
  if (card.getAttribute("aria-label") !== "Move task Written elsewhere out of the unrecognized status ghost") {
    throw new Error("the card does not say what dragging it would do: " + JSON.stringify(card.getAttribute("aria-label")));
  }
  // A property no renderer writes, so finding it afterwards proves the node
  // survived rather than that an equal one was built.
  card.__witness = "stranded";
  const columnsBefore = boardLists.map((list) => list.dataset.status).join(",");

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  await documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });

  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the drag out of the region sent nothing");
  if (!wrote.url.endsWith("/position")) throw new Error("the drop sent " + wrote.url);
  const body = JSON.parse(wrote.options.body);
  // An ordinary status change: the destination column, the head the card was
  // drawn from, and nothing about the status it is leaving — there is no anchor
  // to name in a bucket this card was never in.
  if (body.status !== "shipped") throw new Error("the drop proposed " + JSON.stringify(body.status));
  if (body.expectedHead !== "head-a") throw new Error("the drop did not carry the head it was drawn from: " + JSON.stringify(body.expectedHead));
  if (body.before || body.after) throw new Error("the drop named an anchor: " + JSON.stringify(body));

  // The server agrees, and the poll that reports it moves the node rather than
  // replacing it.
  taskResponse = `+string(served)+`;
  await intervalCallback();
  const filed = boardCard(`+strconv.Quote(stranded.ID)+`);
  if (!filed) throw new Error("the filed card vanished from the board");
  if (filed !== card || filed.__witness !== "stranded") throw new Error("filing the card rebuilt it instead of moving it");
  if (filed.parentElement !== shipped) throw new Error("the filed card is not in the column it was dropped on");
  if (filed.getAttribute("aria-label") !== "Move task Written elsewhere from shipped") {
    throw new Error("the filed card still reads as stranded: " + JSON.stringify(filed.getAttribute("aria-label")));
  }
  if (boardUnknownSection.dataset.visible !== "false") throw new Error("the emptied region stayed on screen");
  if (boardUnknownCount.textContent !== "0") throw new Error("the region counts " + boardUnknownCount.textContent + ", want 0");
  if (boardLists.map((list) => list.dataset.status).join(",") !== columnsBefore) {
    throw new Error("filing a stranded card rebuilt the columns");
  }
`)
}

// A poll that re-resolves a card mid-drag makes the drop a no-op, and the
// client has to notice.
//
// This is reachable rather than theoretical, and stranded cards are where it is
// likeliest: the status that strands one arrives from another clone, and so does
// the status that un-strands it. The renderer deliberately leaves a dragged node
// in place, but reconciliation still files it into the column the model now
// names — so the reader finishes a drag whose card has already arrived. Dropping
// it there changes nothing, and a client comparing against the status the drag
// *started* in would send a write anyway: a settlement commit nobody asked for,
// or a stale-head banner over a move that was never made.
//
// The control is the same gesture on a card that started in a column, which has
// always sent nothing.
func TestHandlerClientDoesNotWriteADropOntoTheColumnACardHasAlreadyReached(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	stranded := clientPlacementTask("WB-01J0000000000000000000A101", "Written elsewhere", strandedStatus, core.PriorityMedium)
	stranded.Head = "head-a"
	// The poll that lands mid-drag: another clone defined the status this card
	// was stranded under, and the server now resolves it into Shipped.
	resolved := stranded
	resolved.Status = core.Status("shipped")
	resolved.Head = "head-b"
	served := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{resolved}, Presentation: presentationForTasks([]core.Task{resolved}),
	})

	runVocabularyClient(t, "drop onto a column the card already reached", "/", vocabulary, "head-1", []core.Task{stranded}, `
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  const card = boardCard(`+strconv.Quote(stranded.ID)+`);
  if (card.parentElement !== boardUnknownList) throw new Error("the stranded card did not start in the region");

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });

  // The poll lands while the cursor is still holding the card.
  taskResponse = `+string(served)+`;
  await intervalCallback();
  if (card.parentElement !== shipped) {
    throw new Error("the fixture is stale: reconciliation no longer files a dragged card, so nothing is being tested");
  }

  // Finishing the drag on the column it has already reached asks for nothing.
  await documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });
  if (fetchCalls.some((call) => call.options && (call.options.method || "GET") !== "GET")) {
    throw new Error("a drop onto the column the card had already reached sent a write");
  }
  if (card.parentElement !== shipped) throw new Error("the no-op drop moved the card");

  // And the card is still the reader's own node throughout.
  if (boardCard(`+strconv.Quote(stranded.ID)+`) !== card) throw new Error("the no-op drop rebuilt the card");
`)
}

// The region takes no drops, from either direction. There is no status a drop
// there would name, so a card dragged onto it is a change nobody can express —
// and the page says so by not offering the target at all rather than by sending
// a write the server would refuse.
func TestHandlerClientTakesNoDropsIntoTheUnknownStatusRegion(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	tasks := []core.Task{
		clientPlacementTask("WB-01J0000000000000000000A101", "Written elsewhere", strandedStatus, core.PriorityMedium),
		clientPlacementTask("WB-01J0000000000000000000B202", "Queued", core.Status("queued"), core.PriorityHigh),
	}
	tasks[0].Head = "head-a"
	tasks[1].Head = "head-b"

	runVocabularyClient(t, "no drops into the unknown-status region", "/", vocabulary, "head-1", tasks, `
  const queued = boardLists.find((list) => list.dataset.status === "queued");
  const stranded = boardCard(`+strconv.Quote(tasks[0].ID)+`);
  const filed = boardCard(`+strconv.Quote(tasks[1].ID)+`);
  if (!stranded || !filed) throw new Error("the board did not draw both cards");

  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  // A card from a column, dragged onto the region.
  let offered = 0;
  filed.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: filed, dataTransfer });
  documentEventListeners.dragover({ target: boardUnknownList, clientY: 1, dataTransfer, preventDefault() { offered += 1; } });
  await documentEventListeners.drop({ target: boardUnknownList, clientY: 1, dataTransfer, preventDefault() { offered += 1; } });
  documentEventListeners.dragend({ target: filed });
  if (offered !== 0) throw new Error("the region offered itself as a drop target " + offered + " time(s)");
  if (filed.parentElement !== queued) throw new Error("a card dropped on the region left its column");

  // And the region's own card, dragged back onto it: still nothing, because the
  // region is not somewhere a task can be put.
  stranded.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: stranded, dataTransfer });
  documentEventListeners.dragover({ target: stranded, clientY: 1, dataTransfer, preventDefault() { offered += 1; } });
  await documentEventListeners.drop({ target: stranded, clientY: 1, dataTransfer, preventDefault() { offered += 1; } });
  documentEventListeners.dragend({ target: stranded });
  if (offered !== 0) throw new Error("the region took a drop from its own card");
  if (stranded.parentElement !== boardUnknownList) throw new Error("the stranded card left the region without a column to go to");

  if (fetchCalls.some((call) => call.options && (call.options.method || "GET") !== "GET")) {
    throw new Error("a drop the region cannot express still sent a write");
  }
`)
}

// A task another clone deleted mid-drag must not take the board down with it.
//
// The write is confirmed against a task the poll before it had already removed,
// so the confirmation puts the task back into the model while the presentation
// that poll left behind does not name it. The renderer read `undefined.idPrefix`
// and threw, and because that happens inside the render every poll performs, the
// board froze on its last frame — over a card that was about to disappear
// anyway. `projectPresentation` now fills the gap instead.
//
// Both entry paths are covered. A column card is the pre-existing one; a
// stranded card is the one this change opens, and it is the likelier of the two,
// because the clone most apt to be rewriting a task is the clone whose status
// change stranded it here in the first place.
func TestHandlerClientSurvivesATaskDeletedMidDrag(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	bystander := clientPlacementTask("WB-01J0000000000000000000B202", "Bystander", core.Status("icebox"), core.PriorityLow)
	bystander.Head = "head-b"
	// What the poll carries once the other clone's delete lands: the dragged
	// task is gone from the tasks and from the presentation alike.
	served := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{bystander}, Presentation: presentationForTasks([]core.Task{bystander}),
	})

	for _, dragged := range []struct {
		name   string
		status core.Status
	}{
		{"from a column", core.Status("queued")},
		{"from the unknown-status region", strandedStatus},
	} {
		t.Run(dragged.name, func(t *testing.T) {
			doomed := clientPlacementTask("WB-01J0000000000000000000A101", "Doomed", dragged.status, core.PriorityMedium)
			doomed.Head = "head-a"
			runVocabularyClient(t, "delete during a drag "+dragged.name, "/", vocabulary, "head-1",
				[]core.Task{doomed, bystander}, `
  const shipped = boardLists.find((list) => list.dataset.status === "shipped");
  const card = boardCard(`+strconv.Quote(doomed.ID)+`);
  if (!card) throw new Error("the board drew no card for the doomed task");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });

  // The delete lands while the cursor is still holding the card.
  taskResponse = `+string(served)+`;
  await intervalCallback();

  // The drop confirms against a task the board has already forgotten. This is
  // where the renderer used to throw.
  await documentEventListeners.drop({ target: shipped, clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });

  // The board is still rendering, which is the whole claim: the poll that
  // follows draws, and it takes the deleted card away.
  await intervalCallback();
  if (boardCard(`+strconv.Quote(doomed.ID)+`)) throw new Error("the deleted task is still on the board");
  const survivor = boardCard(`+strconv.Quote(bystander.ID)+`);
  if (!survivor) throw new Error("the board lost a card the delete did not concern");
  if (survivor.parentElement !== boardLists.find((list) => list.dataset.status === "icebox")) {
    throw new Error("the surviving card left its column");
  }
  // And it keeps rendering, rather than having thrown once and stopped.
  await intervalCallback();
  await intervalCallback();
  if (!boardCard(`+strconv.Quote(bystander.ID)+`)) throw new Error("a later poll stopped drawing the board");
`)
		})
	}
}

// The other way out of the region is the task's own form, and it still works.
// The status a stranded task holds is one this page has no option for, so the
// field grows a disabled placeholder naming it; choosing one of the project's
// statuses then saves like any other edit.
//
// This is the half that was already true before cards dragged out, and it has
// to stay true: the drag is the quick way, not the only way, and a reader who
// opened the task rather than dragging it must not find a dead form.
func TestHandlerClientSavesAStatusChosenForAStrandedTask(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	stranded := clientPlacementTask("WB-01J0000000000000000000A101", "Written elsewhere", strandedStatus, core.PriorityMedium)
	stranded.Head = "head-a"

	runVocabularyClient(t, "save a status chosen for a stranded task", "/tasks/"+stranded.ID, vocabulary, "head-1", []core.Task{stranded}, `
  const control = findElement(main, (element) => element.id === "task-status");
  if (!control) throw new Error("the stranded task's form has no status select");
  const placeholder = control.firstElementChild;
  if (!placeholder.disabled || placeholder.value !== "") {
    throw new Error("the field does not lead with a disabled placeholder: " + JSON.stringify(placeholder.textContent));
  }
  if (!placeholder.textContent.includes("ghost")) {
    throw new Error("the placeholder does not name the status the server holds: " + JSON.stringify(placeholder.textContent));
  }
  if (control.value !== "") throw new Error("the field pre-selected a status nobody chose: " + JSON.stringify(control.value));

  // The project's own statuses are the choices, and choosing one saves.
  const chosen = control.children.find((option) => option.value === "shipped");
  if (!chosen) throw new Error("the field offers no Shipped option");
  control.children.forEach((option) => { option.selected = false; });
  chosen.selected = true;

  const form = findElement(main, (element) => element.tagName === "FORM");
  await form.eventListeners.submit({ preventDefault() {} });
  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the save sent nothing");
  const body = JSON.parse(wrote.options.body);
  // Exactly the field the reader changed: re-asserting a title nobody touched
  // would revert a teammate's edit that landed in between.
  if (JSON.stringify(body) !== JSON.stringify({ status: "shipped", expectedHead: "head-a" })) {
    throw new Error("the save did not carry exactly the chosen status: " + JSON.stringify(body));
  }
`)
}
