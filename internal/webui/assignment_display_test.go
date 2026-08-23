package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// What the board says about who holds a task, on the cards and on the task page.
//
// Every case here is about one of four things: that the chip a card draws and the
// hint the section prints are the server's own derivations rather than a second
// copy of them in JavaScript; that a task nobody holds draws exactly what it drew
// before assignments existed, node for node; that an assignment made from the CLI
// arrives within a poll tick without moving anything the reader is holding; and
// that a principal somebody else asserted is drawn as text whatever it contains.
//
// Nothing here writes, and every board built here is one that cannot: these are
// read-only boards, wired with a lister and no assignment mutations, which is
// what the display half of this feature has to keep doing unchanged now that a
// board wired for them draws a form. Making one is assignment_mutation_test.go's
// subject.

const (
	assignedTaskID   = "WB-01J0000000000000000000AS01"
	unassignedTaskID = "WB-01J0000000000000000000AS02"
	crowdedTaskID    = "WB-01J0000000000000000000AS03"
)

// assignmentStamp is the wall time every assignment in this file was recorded
// at: three days before the test runs, so the staleness hint is a hint a reader
// would act on and reads the same on every clock. A fixed calendar date would
// have said "3 days ago" the week it was written and something else every week
// after, which is a test that stops asserting the wording it was written for.
var assignmentStamp = time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)

func heldAssignment(principal, label string) core.Assignment {
	return core.Assignment{Principal: principal, Label: label, Creator: principal, CreatedAt: assignmentStamp}
}

// assignedBoardTasks is a board with one held task, one nobody holds, and one
// held by more agents than a card has room for.
func assignedBoardTasks() []core.Task {
	held := clientPlacementTask(assignedTaskID, "Held task", core.StatusReady, core.PriorityHigh)
	held.Assignments = []core.Assignment{
		heldAssignment("dylan@example.com", "impl-1"),
		heldAssignment("teammate@example.com", ""),
	}
	free := clientPlacementTask(unassignedTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Rank = "2/1"
	crowded := clientPlacementTask(crowdedTaskID, "Crowded task", core.StatusReady, core.PriorityLow)
	crowded.Rank = "3/1"
	crowded.Assignments = []core.Assignment{
		heldAssignment("a@example.com", "one"),
		heldAssignment("b@example.com", "two"),
		heldAssignment("c@example.com", "three"),
		heldAssignment("d@example.com", "four"),
		heldAssignment("e@example.com", "five"),
	}
	return []core.Task{held, free, crowded}
}

// The chip is the short form the terminal board prints — the local part of the
// address and the agent label — and it is derived here rather than by the client,
// so the two boards and `workbook show` cannot drift.
func TestTasksDocumentCarriesTheAssignmentsACardDraws(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return assignedBoardTasks(), nil })
	response := request(t, handler, http.MethodGet, "/api/tasks")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
	}
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the tasks document: %v", err)
	}
	views := make(map[string]TaskPresentation, len(document.Presentation))
	for _, view := range document.Presentation {
		views[view.TaskID] = view
	}

	held := views[assignedTaskID]
	if got, want := strings.Join(held.AssignmentChips, ","), "dylan/impl-1,teammate"; got != want {
		t.Fatalf("the held task's chips = %q, want %q", got, want)
	}
	if held.MoreAssignments != 0 {
		t.Fatalf("a two-assignment card hid %d of them", held.MoreAssignments)
	}
	if len(held.Assignments) != 2 {
		t.Fatalf("the held task's assignment list = %d entries, want 2", len(held.Assignments))
	}
	if held.Assignments[0].Principal != "dylan@example.com" || held.Assignments[0].Label != "impl-1" {
		t.Fatalf("the first assignment = %+v", held.Assignments[0])
	}
	// The whole address, not the chip: the section is where a value somebody
	// would type into --unassign is legible in full.
	if held.Assignments[1].Principal != "teammate@example.com" || held.Assignments[1].Label != "" {
		t.Fatalf("the second assignment = %+v", held.Assignments[1])
	}
	if !held.Assignments[0].CreatedAt.Equal(assignmentStamp) {
		t.Fatalf("the recorded time = %v, want %v", held.Assignments[0].CreatedAt, assignmentStamp)
	}
	// The CLI's own wording, from presentation.AssignedAgo, computed against the
	// clock the route read rather than against anything this client holds.
	if held.Assignments[0].Ago != "assigned 3 days ago" {
		t.Fatalf("the staleness hint = %q, want %q", held.Assignments[0].Ago, "assigned 3 days ago")
	}

	// A task nobody holds carries none of the three members, so the document a
	// board polls once a second is byte-for-byte what it was before this shipped.
	free := views[unassignedTaskID]
	if free.AssignmentChips != nil || free.MoreAssignments != 0 || free.Assignments != nil {
		t.Fatalf("an unheld task carried assignment presentation: %+v", free)
	}
	encoded, err := json.Marshal(free)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"assignmentChips", "moreAssignments", "assignments"} {
		if strings.Contains(string(encoded), member) {
			t.Fatalf("an unheld task's view names %s: %s", member, encoded)
		}
	}
}

// A card is one box in one column, and a task may hold fifty assignments. The row
// is capped and says how many it left out; the task page prints all of them.
func TestTasksDocumentCapsTheChipRowACardDraws(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return assignedBoardTasks(), nil })
	response := request(t, handler, http.MethodGet, "/api/tasks")
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the tasks document: %v", err)
	}
	for _, view := range document.Presentation {
		if view.TaskID != crowdedTaskID {
			continue
		}
		if got, want := strings.Join(view.AssignmentChips, ","), "a/one,b/two,c/three"; got != want {
			t.Fatalf("the crowded task's chips = %q, want %q", got, want)
		}
		if view.MoreAssignments != 2 {
			t.Fatalf("the crowded task hid %d assignments, want 2", view.MoreAssignments)
		}
		if len(view.Assignments) != 5 {
			t.Fatalf("the crowded task's section lists %d assignments, want all 5", len(view.Assignments))
		}
		return
	}
	t.Fatal("the document carried no view for the crowded task")
}

// The cards the server renders into the page carry the same chips the poll will
// carry a second later, because one function produced both.
func TestBoardPageRendersAssigneeChipsOnHeldCardsOnly(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return assignedBoardTasks(), nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	held := cardMarkup(t, body, assignedTaskID)
	if !strings.Contains(held, `<span class="assignee">@dylan/impl-1</span>`) ||
		!strings.Contains(held, `<span class="assignee">@teammate</span>`) {
		t.Fatalf("the held card drew no chip row: %s", held)
	}
	free := cardMarkup(t, body, unassignedTaskID)
	if strings.Contains(free, "assignee") || strings.Contains(free, "data-assignees") {
		t.Fatalf("an unheld card drew a chip row: %s", free)
	}
	crowded := cardMarkup(t, body, crowdedTaskID)
	if !strings.Contains(crowded, `+2 more`) {
		t.Fatalf("the crowded card did not say what it left out: %s", crowded)
	}
	if strings.Contains(crowded, "@d/four") || strings.Contains(crowded, "@e/five") {
		t.Fatalf("the crowded card drew past its cap: %s", crowded)
	}
}

// A principal is somebody else's asserted identity, unverified by design, and the
// page draws whatever it is as text. The server-rendered half is html/template's
// escaping; the client half is the case below it.
func TestBoardPageEscapesHostileAssigneeChips(t *testing.T) {
	hostile := `<img src=x onerror=alert(1)>@example.com`
	task := clientPlacementTask(assignedTaskID, "Held task", core.StatusReady, core.PriorityHigh)
	task.Assignments = []core.Assignment{heldAssignment(hostile, `"><script>alert(1)</script>`)}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })
	response := request(t, handler, http.MethodGet, "/")
	body := response.Body.String()
	if strings.Contains(body, "<img src=x") || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("a hostile assignment reached the page as markup")
	}
	card := cardMarkup(t, body, assignedTaskID)
	if !strings.Contains(card, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("the hostile principal was not drawn as text: %s", card)
	}
}

// cardMarkup is the rendered markup of one card, so a test can say what a card
// does and does not contain without matching against the whole page.
func cardMarkup(t *testing.T, body, taskID string) string {
	t.Helper()
	start := strings.Index(body, `data-task-id="`+taskID+`"`)
	if start < 0 {
		t.Fatalf("the page rendered no card for %s", taskID)
	}
	end := strings.Index(body[start:], "</article>")
	if end < 0 {
		t.Fatalf("the card for %s is unterminated", taskID)
	}
	return body[start : start+end]
}

// The client draws the chips it was handed, in the row the server rendered, and
// draws nothing at all on a card nobody holds.
func TestHandlerClientDrawsAssigneeChipsFromTheServersDerivation(t *testing.T) {
	runBoardClient(t, "assignee chips", assignedBoardTasks(), `
  const held = boardCard(`+strconv.Quote(assignedTaskID)+`);
  const row = findElement(held, (element) => hasDataKey(element, "assignees"));
  if (!row) throw new Error("the held card drew no chip row");
  if (row.getAttribute("role") !== "group" || row.getAttribute("aria-label") !== "Assignees") {
    throw new Error("the chip row is not named for a reader who cannot see it");
  }
  const chips = row.children.map((chip) => chip.textContent).join(",");
  if (chips !== "@dylan/impl-1,@teammate") throw new Error("chips = " + JSON.stringify(chips));

  const free = boardCard(`+strconv.Quote(unassignedTaskID)+`);
  if (findElement(free, (element) => hasDataKey(element, "assignees"))) {
    throw new Error("an unheld card drew a chip row");
  }

  const crowded = boardCard(`+strconv.Quote(crowdedTaskID)+`);
  const capped = findElement(crowded, (element) => hasDataKey(element, "assignees"));
  const drawn = capped.children.map((chip) => chip.textContent);
  if (drawn.join(",") !== "@a/one,@b/two,@c/three,+2 more") {
    throw new Error("the capped row = " + JSON.stringify(drawn));
  }
  if (!hasClassToken(capped.children[3], "assignee--more")) {
    throw new Error("the overflow chip is not marked apart from the assignees");
  }
`)
}

// The card a client builds and the card the server rendered are the same card.
// They are built by different code and this is the seam where they could differ:
// the server's chips are html/template's, the client's are text nodes, and a
// reader who hard-loads the board must not watch the chips change a second later.
func TestHandlerClientRedrawsTheServersChipsUnchanged(t *testing.T) {
	tasks := assignedBoardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	served := cardMarkup(t, response.Body.String(), assignedTaskID)
	chips := make([]string, 0, 2)
	for _, fragment := range strings.Split(served, `<span class="assignee">`)[1:] {
		chips = append(chips, fragment[:strings.Index(fragment, "</span>")])
	}
	if len(chips) != 2 {
		t.Fatalf("the served card drew %d chips, want 2", len(chips))
	}
	runBoardClient(t, "chip parity", tasks, `
  const served = `+string(mustJSON(t, chips))+`;
  const row = findElement(boardCard(`+strconv.Quote(assignedTaskID)+`), (element) => hasDataKey(element, "assignees"));
  const drawn = row.children.map((chip) => chip.textContent);
  if (drawn.join(",") !== served.join(",")) {
    throw new Error("the client redrew the served chips as " + JSON.stringify(drawn) + ", not " + JSON.stringify(served));
  }
`)
}

// An assignment made from the CLI arrives on the next poll, on the card it names
// and on no other. The card keeps its node — its focus, its drag state and the
// scroll anchor under it — and a card nobody holds is not written to at all.
func TestHandlerClientAddsAChipRowWithoutRebuildingTheCard(t *testing.T) {
	tasks := assignedBoardTasks()
	assigned := make([]core.Task, len(tasks))
	copy(assigned, tasks)
	assigned[1].Assignments = []core.Assignment{heldAssignment("late@example.com", "spike")}
	runBoardClient(t, "an assignment arriving on a poll", tasks, `
  const arrived = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: assigned, Presentation: presentationForTasks(assigned),
	}))+`;
  const free = boardCard(`+strconv.Quote(unassignedTaskID)+`);
  const held = boardCard(`+strconv.Quote(assignedTaskID)+`);
  const heldRow = findElement(held, (element) => hasDataKey(element, "assignees"));
  const heldSections = held.children.length;
  let heldWrites = 0;
  const write = held.setAttribute.bind(held);
  held.setAttribute = (name, value) => { heldWrites += 1; return write(name, value); };

  taskResponse = arrived;
  await intervalCallback();

  if (boardCard(`+strconv.Quote(unassignedTaskID)+`) !== free) {
    throw new Error("the assigned card was rebuilt rather than redrawn");
  }
  const row = findElement(free, (element) => hasDataKey(element, "assignees"));
  if (!row) throw new Error("the assignment did not arrive on the card");
  if (row.children.map((chip) => chip.textContent).join(",") !== "@late/spike") {
    throw new Error("the arrived chips = " + JSON.stringify(row.children.map((chip) => chip.textContent)));
  }
  // And the card that was already held was left entirely alone.
  if (boardCard(`+strconv.Quote(assignedTaskID)+`) !== held) throw new Error("an untouched card was rebuilt");
  if (findElement(held, (element) => hasDataKey(element, "assignees")) !== heldRow) {
    throw new Error("an untouched card's chip row was rebuilt");
  }
  if (held.children.length !== heldSections || heldWrites !== 0) {
    throw new Error("a poll wrote " + heldWrites + " attributes to a card it did not change");
  }
`)
}

// A card nobody holds is the card this board has always drawn: the same children,
// in the same order, with nothing added and nothing reserved. This is the height
// invariant stated as the only thing a fake DOM can state it as.
func TestHandlerClientLeavesUnheldCardsExactlyAsTheyWere(t *testing.T) {
	free := clientPlacementTask(unassignedTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Description = "Nobody holds this one."
	free.Labels = []string{"web"}
	runBoardClient(t, "an unheld card's shape", []core.Task{free}, `
  const card = boardCard(`+strconv.Quote(unassignedTaskID)+`);
  const shape = card.children.map((child) => child.className || child.tagName).join(",");
  if (shape !== "task-card__meta,H3,task-card__failure,P,labels,restore-button") {
    throw new Error("an unheld card's children = " + JSON.stringify(shape));
  }
  if (findElements(card, (element) => hasClassToken(element, "assignee")).length !== 0) {
    throw new Error("an unheld card reserved room for a chip");
  }
`)
}

// Chips are text nodes, whatever the principal contains.
func TestHandlerClientDrawsHostileAssigneeChipsAsText(t *testing.T) {
	hostile := `<img src=x onerror=alert(1)>@example.com`
	task := clientPlacementTask(assignedTaskID, "Held task", core.StatusReady, core.PriorityHigh)
	task.Assignments = []core.Assignment{heldAssignment(hostile, `"><script>alert(1)</script>`)}
	runBoardClient(t, "hostile chips", []core.Task{task}, `
  const row = findElement(boardCard(`+strconv.Quote(assignedTaskID)+`), (element) => hasDataKey(element, "assignees"));
  const chip = row.children[0];
  if (chip.children.length !== 0) throw new Error("a chip built child elements");
  if (chip.textContent !== `+strconv.Quote(`@<img src=x onerror=alert(1)>/"><script>alert(1)</script>`)+`) {
    throw new Error("the hostile chip was not drawn verbatim as text: " + JSON.stringify(chip.textContent));
  }
`)
}

// assignmentPageProgram builds the Node program for a task page carrying these
// tasks, with the helpers a test of the assignments section reaches for.
func assignmentPageProgram(t *testing.T, tasks []core.Task, body string) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET the task page status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	const helpers = `
function assignmentsPanel() {
  return findElement(main, (element) => element.dataset.taskPanel === "assignments");
}
function assignmentRows() {
  const panel = assignmentsPanel();
  if (!panel) throw new Error("the page is drawing no assignments section");
  return findElement(panel, (element) => hasDataKey(element, "panelList")).children;
}
function assignmentText(row) {
  return row.children.map((line) => line.textContent);
}
`
	return clientDOMHarness("/tasks/"+tasks[0].ID, string(document)) + script + helpers + body
}

// The section names who holds the task, which agent of theirs holds it, when that
// was recorded, and how long ago — the four things `workbook show` prints.
func TestHandlerClientDrawsTheAssignmentsSection(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{assignedBoardTasks()[0]}
	program := assignmentPageProgram(t, tasks, `
setTimeout(() => {
  const rows = assignmentRows();
  if (rows.length !== 2) throw new Error("assignment rows = " + rows.length);
  const first = assignmentText(rows[0]);
  if (first[0] !== "dylan@example.com/impl-1") {
    throw new Error("the first row does not name the value: " + JSON.stringify(first[0]));
  }
  // The whole address, not the card's chip: this is where the value somebody
  // types into --unassign is legible.
  if (assignmentText(rows[1])[0] !== "teammate@example.com") {
    throw new Error("a labelless assignment is not drawn whole: " + JSON.stringify(assignmentText(rows[1])[0]));
  }
  if (!first[1].endsWith("assigned 3 days ago")) {
    throw new Error("the row carries no staleness hint: " + JSON.stringify(first[1]));
  }
  const count = findElement(assignmentsPanel(), (element) => hasDataKey(element, "panelCount"));
  if (count.textContent !== "2 assignments") throw new Error("count = " + JSON.stringify(count.textContent));
  // Read-only: this board was built with no assignment mutations and no
  // identity to record one against, so the section offers no control at all.
  if (findElements(assignmentsPanel(), (element) => element.tagName === "BUTTON" || element.tagName === "FORM" || element.tagName === "INPUT").length) {
    throw new Error("the assignments section drew a control");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the assignments section: %v\n%s", err, output)
	}
}

// A task nobody holds draws no section at all — not a heading over an empty list.
// An assign made from the CLI puts one there within a poll tick, in its place
// above the attachments, and an unassign takes it away again; through all of it
// the form above keeps every word the reader has typed into it.
func TestHandlerClientFollowsAssignmentsThroughThePoll(t *testing.T) {
	node := requireNode(t)
	free := clientPlacementTask(assignedTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Head = "head-1"
	assigned := free
	assigned.Assignments = []core.Assignment{heldAssignment("dylan@example.com", "impl-1")}
	program := assignmentPageProgram(t, []core.Task{free}, `
const assigned = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{assigned},
		Presentation: presentationForTasks([]core.Task{assigned}),
	}))+`;
const released = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{free},
		Presentation: presentationForTasks([]core.Task{free}),
	}))+`;
setTimeout(async () => {
  if (assignmentsPanel()) throw new Error("an unheld task drew an assignments section");
  const title = findElement(main, (element) => element.tagName === "INPUT" && element.name === "title");
  title.value = "Half an edit";

  taskResponse = assigned;
  await intervalCallback();

  const panel = assignmentsPanel();
  if (!panel) throw new Error("the assignment did not arrive on the open page");
  if (assignmentRows().length !== 1) throw new Error("rows = " + assignmentRows().length);
  // Above the attachments, which is where it was built to sit.
  const sections = panel.parentElement.children;
  const attachments = sections.find((element) => element.dataset.taskPanel === "attachments");
  if (sections.indexOf(panel) !== sections.indexOf(attachments) - 1) {
    throw new Error("the section did not arrive above the attachments");
  }
  if (title.value !== "Half an edit") {
    throw new Error("the poll took the reader's edit with it: " + JSON.stringify(title.value));
  }

  taskResponse = released;
  await intervalCallback();

  if (assignmentsPanel()) throw new Error("the unassign left an empty section behind");
  if (title.value !== "Half an edit") throw new Error("the second poll took the edit with it");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the assignment poll: %v\n%s", err, output)
	}
}

// A poll that changes nothing about who holds the task writes nothing into the
// section, so a reader's selection inside a row survives the second.
func TestHandlerClientWritesNothingIntoAnUnchangedAssignmentsSection(t *testing.T) {
	node := requireNode(t)
	tasks := []core.Task{assignedBoardTasks()[0]}
	program := assignmentPageProgram(t, tasks, `
const again = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{assignedBoardTasks()[0]},
		Presentation: presentationForTasks([]core.Task{assignedBoardTasks()[0]}),
	}))+`;
setTimeout(async () => {
  const panel = assignmentsPanel();
  const rows = assignmentRows();
  const first = rows[0];

  taskResponse = again;
  await intervalCallback();

  if (assignmentsPanel() !== panel) throw new Error("a poll rebuilt the section");
  if (assignmentRows()[0] !== first) throw new Error("a poll rebuilt a row it did not change");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the unchanged assignments poll: %v\n%s", err, output)
	}
}

// A principal and a label reach the section as text, exactly as they reach a chip.
func TestHandlerClientDrawsHostileAssignmentsAsText(t *testing.T) {
	node := requireNode(t)
	hostile := `<img src=x onerror=alert(1)>@example.com`
	task := clientPlacementTask(assignedTaskID, "Held task", core.StatusReady, core.PriorityHigh)
	task.Assignments = []core.Assignment{heldAssignment(hostile, `"><script>alert(1)</script>`)}
	program := assignmentPageProgram(t, []core.Task{task}, `
setTimeout(() => {
  const row = assignmentRows()[0];
  const who = row.children[0];
  if (elementsUnder(who).some((element) => element.tagName !== "SPAN")) {
    throw new Error("an assignment row built something other than the two spans it draws");
  }
  if (who.textContent !== `+strconv.Quote(`<img src=x onerror=alert(1)>@example.com/"><script>alert(1)</script>`)+`) {
    throw new Error("the hostile assignment was not drawn verbatim as text: " + JSON.stringify(who.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the hostile assignments section: %v\n%s", err, output)
	}
}

// The standing check that the task update endpoint carries no assignment intent.
//
// It is not a claim that this board cannot assign — it can, through two routes
// of its own — but a claim about this route. PATCH /api/tasks/{id} maps its
// request onto core.UpdateInput field by field for exactly this class of
// accident: core's update input carries assignment changes, and a member added
// to the service input must not become part of this API by inheritance. An
// assignment made here would ride an unrelated field save, and a save is what
// this endpoint is for. This is that mapping asserted from the outside.
func TestUpdateEndpointCarriesNoAssignmentIntent(t *testing.T) {
	for _, body := range []string{
		`{"title":"Renamed","assignments":[{"add":"someone@example.com"}]}`,
		`{"assign":"someone@example.com"}`,
		`{"unassign":"someone@example.com"}`,
	} {
		var reached bool
		handler := NewHandler(Options{
			List: func(context.Context) ([]core.Task, error) { return assignedBoardTasks(), nil },
			Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
				reached = true
				if len(input.Assignments) != 0 {
					t.Fatalf("the web update carried %d assignment changes into core", len(input.Assignments))
				}
				return core.MutationResult{Task: assignedBoardTasks()[0]}, nil
			},
		})
		response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+assignedTaskID, body)
		// Refused rather than quietly dropped. The endpoint decodes with unknown
		// members disallowed, so a client that tried to assign through it is told
		// so instead of watching a title change and an assignment vanish.
		if response.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s status = %d, want %d", body, response.Code, http.StatusBadRequest)
		}
		if reached {
			t.Fatalf("PATCH %s reached the service", body)
		}
	}
	// And the route still updates what it is for, so the refusals above are about
	// the assignment members rather than about the endpoint.
	var title string
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return assignedBoardTasks(), nil },
		Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
			if input.Title != nil {
				title = *input.Title
			}
			return core.MutationResult{Task: assignedBoardTasks()[0]}, nil
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+assignedTaskID, `{"title":"Renamed"}`)
	if response.Code != http.StatusOK || title != "Renamed" {
		t.Fatalf("an ordinary update status = %d, title = %q", response.Code, title)
	}
}
