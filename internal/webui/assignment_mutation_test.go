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

// Making and withdrawing an assignment from the board.
//
// The display half of this lives next door in assignment_display_test.go and is
// unchanged: what a card draws, what the section prints, and the fact that
// `PATCH /api/tasks/{id}` still carries no assignment intent. This file is about
// the two routes that do carry one, and about the single question that decides
// whether the page offers them at all — whether this board has an identity to
// record an assignment against.
//
// That identity is the checkout's, not the browser's. `workbook serve` is bound
// to one worktree and writes as the same `user.email` the command line in that
// worktree writes as, so an assignment made here is attributable in exactly the
// way one made from `workbook update --assign` is. A board built without it
// draws no control, which is the board every test in the neighbouring file
// builds.

const (
	assignableTaskID = "WB-01J0000000000000000000AS11"
	boardIdentity    = "dylan@example.com"
)

// assignableBoard is a board wired the way `workbook serve` wires one: both
// assignment mutations and the identity they are recorded against. The two
// closures report what they were handed so a test can say what the route sent
// rather than only what it answered.
type assignableBoard struct {
	assigned   []core.AssignInput
	unassigned []core.UnassignInput
	task       core.Task
}

func (board *assignableBoard) options(t *testing.T, tasks []core.Task) Options {
	t.Helper()
	return Options{
		Identity: boardIdentity,
		List:     func(context.Context) ([]core.Task, error) { return tasks, nil },
		Update:   unexpectedTaskUpdate(t),
		Assign: func(_ context.Context, id string, input core.AssignInput) (core.MutationResult, error) {
			if id != board.task.ID {
				t.Fatalf("the assign route addressed task %q, want %q", id, board.task.ID)
			}
			board.assigned = append(board.assigned, input)
			return core.MutationResult{Task: board.task}, nil
		},
		Unassign: func(_ context.Context, id string, input core.UnassignInput) (core.MutationResult, error) {
			if id != board.task.ID {
				t.Fatalf("the unassign route addressed task %q, want %q", id, board.task.ID)
			}
			board.unassigned = append(board.unassigned, input)
			return core.MutationResult{Task: board.task}, nil
		},
	}
}

// heldTask is one task carrying three assignments: one this board's identity
// holds, one it recorded against somebody else, and one it has nothing to do
// with. The three are exactly the removal rule's three cases.
func heldTask() core.Task {
	task := clientPlacementTask(assignableTaskID, "Held task", core.StatusReady, core.PriorityHigh)
	task.Head = "head-1"
	task.Assignments = []core.Assignment{
		{Principal: boardIdentity, Label: "impl-1", Creator: boardIdentity, CreatedAt: assignmentStamp},
		{Principal: "sam@example.com", Creator: boardIdentity, CreatedAt: assignmentStamp},
		{Principal: "stranger@example.com", Creator: "stranger@example.com", CreatedAt: assignmentStamp},
	}
	return task
}

func assignmentsPath(taskID string) string {
	return "/api/tasks/" + taskID + "/assignments"
}

// The route hands core the assignment somebody typed, with the tip the page was
// looking at, and answers with the task it produced.
func TestAssignRouteRecordsTheAssignmentTheBodyNames(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := requestJSON(t, handler, http.MethodPost, assignmentsPath(assignableTaskID),
		`{"to":"sam@example.com/impl-2","expectedHead":"head-1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST an assignment status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	if len(board.assigned) != 1 {
		t.Fatalf("the route made %d assignments, want 1", len(board.assigned))
	}
	if got, want := board.assigned[0].To, "sam@example.com/impl-2"; got != want {
		t.Fatalf("the assignment value = %q, want %q", got, want)
	}
	if got, want := board.assigned[0].ExpectedHead, "head-1"; got != want {
		t.Fatalf("the expected head = %q, want %q", got, want)
	}
	// Never the claim gate. Assigning from a board is additive, exactly as
	// `workbook update --assign` is: the section the reader is looking at
	// already names everybody who holds the task, so refusing the write would
	// refuse a decision they have already seen the evidence for.
	if board.assigned[0].OnlyIfUnheld {
		t.Fatal("the board's assign asked for the claim gate")
	}
	var document TaskMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the mutation document: %v", err)
	}
	if document.Task.ID != assignableTaskID {
		t.Fatalf("the answer named task %q", document.Task.ID)
	}
	// The section the page draws is the server's derivation, on this answer as
	// on the poll: the panel redraws from what came back rather than composing a
	// staleness hint of its own.
	if document.Assignments == nil || len(*document.Assignments) != 3 {
		t.Fatalf("the answer carried %v assignment views, want 3", document.Assignments)
	}
	if (*document.Assignments)[0].Ago == "" {
		t.Fatal("the answer's assignment view carries no staleness hint")
	}
}

// A withdrawal that leaves nobody holding the task still speaks about
// assignments: the member is `[]` rather than absent, because absent is what
// every mutation that is not about assignments sends, and a page that could not
// tell the two apart would redraw the row it had just removed.
func TestAssignmentRoutesDistinguishNoAssignmentsFromNotAboutAssignments(t *testing.T) {
	free := clientPlacementTask(assignableTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Head = "head-2"
	board := &assignableBoard{task: free}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := requestJSON(t, handler, http.MethodDelete, assignmentsPath(assignableTaskID),
		`{"from":"dylan@example.com/impl-1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE the last assignment status = %d: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"assignments":[]`) {
		t.Fatalf("an emptied task did not say so: %s", response.Body)
	}

	// And an ordinary task update still says nothing about assignments at all.
	options := board.options(t, []core.Task{heldTask()})
	options.Update = func(context.Context, string, core.UpdateInput) (core.MutationResult, error) {
		return core.MutationResult{Task: heldTask()}, nil
	}
	saved := requestJSON(t, NewHandler(options), http.MethodPatch, "/api/tasks/"+assignableTaskID, `{"title":"Renamed"}`)
	var document TaskMutationDocument
	if err := json.Unmarshal(saved.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the save: %v", err)
	}
	// The task's own list rides along as it does on every answer; what is absent
	// is the section, which only the two assignment routes speak about.
	if document.Assignments != nil {
		t.Fatalf("a title save carried an assignment section: %s", saved.Body)
	}
	if len(document.Task.Assignments) != 3 {
		t.Fatalf("a title save dropped the task's own assignments: %s", saved.Body)
	}
}

// An empty value is the board's own identity, which is what the form's
// placeholder offers and what `--assign self` means on the command line. It is
// left empty rather than filled in here, so core resolves the actor once.
func TestAssignRouteLeavesTheAssigneeToCoreWhenTheBodyNamesNobody(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := requestJSON(t, handler, http.MethodPost, assignmentsPath(assignableTaskID), `{"to":""}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST a self-assignment status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	if len(board.assigned) != 1 || board.assigned[0].To != "" {
		t.Fatalf("the route resolved the assignee itself: %+v", board.assigned)
	}
}

// Withdrawal names the assignment, because a task may carry several of this
// identity's agents and withdrawing one must not withdraw the rest.
func TestUnassignRouteWithdrawsTheAssignmentTheBodyNames(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := requestJSON(t, handler, http.MethodDelete, assignmentsPath(assignableTaskID),
		`{"from":"dylan@example.com/impl-1","expectedHead":"head-1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE an assignment status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	if len(board.unassigned) != 1 {
		t.Fatalf("the route made %d withdrawals, want 1", len(board.unassigned))
	}
	if got, want := board.unassigned[0].From, "dylan@example.com/impl-1"; got != want {
		t.Fatalf("the withdrawn value = %q, want %q", got, want)
	}
	if got, want := board.unassigned[0].ExpectedHead, "head-1"; got != want {
		t.Fatalf("the expected head = %q, want %q", got, want)
	}
}

// The address takes those two verbs and no others, and a body it does not
// understand is refused rather than half-read — the rule every other mutation
// on this board is held to.
func TestAssignmentRouteRefusesWhatItDoesNotAccept(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := request(t, handler, http.MethodGet, assignmentsPath(assignableTaskID))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET the assignments route status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got, want := response.Header().Get("Allow"), "POST, DELETE"; got != want {
		t.Fatalf("Allow = %q, want %q", got, want)
	}
	for _, body := range []string{
		`{"assign":"sam@example.com"}`,
		`{"to":"sam@example.com","label":"impl-1"}`,
	} {
		response := requestJSON(t, handler, http.MethodPost, assignmentsPath(assignableTaskID), body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("POST %s status = %d, want %d", body, response.Code, http.StatusBadRequest)
		}
	}
	if len(board.assigned) != 0 || len(board.unassigned) != 0 {
		t.Fatal("a refused request reached the service")
	}
}

// A board built without the two mutations answers the address the way it answers
// every other unwired one, rather than pretending to record something.
func TestAssignmentRoutesReportABoardBuiltWithoutThem(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{heldTask()}, nil })
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		response := requestJSON(t, handler, method, assignmentsPath(assignableTaskID), `{}`)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s on an unwired board status = %d, want %d", method, response.Code, http.StatusInternalServerError)
		}
	}
}

// Who may withdraw which assignment is core's rule, decided on the server and
// carried in the view, so the button the page draws and the refusal the service
// would give cannot disagree.
func TestAssignmentPresentationSaysWhichAssignmentsThisBoardMayWithdraw(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	response := request(t, handler, http.MethodGet, "/api/tasks")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks status = %d, want %d", response.Code, http.StatusOK)
	}
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the tasks document: %v", err)
	}
	views := document.Presentation[0].Assignments
	if len(views) != 3 {
		t.Fatalf("the held task carried %d assignment views, want 3", len(views))
	}
	// This identity's own, and the one it recorded against somebody else: both
	// removable, which is exactly core.Assignment.RemovableBy's two clauses.
	if !views[0].Removable || !views[1].Removable {
		t.Fatalf("this board may not withdraw what it holds or recorded: %+v", views[:2])
	}
	// A stranger's own assignment is theirs to withdraw, and the page must not
	// offer a control the service would refuse.
	if views[2].Removable {
		t.Fatalf("this board offered to withdraw a stranger's assignment: %+v", views[2])
	}

	// And a board with no identity carries the member on nothing at all, so the
	// document a read-only board polls is byte-for-byte the one it always was.
	readOnly := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{heldTask()}, nil })
	encoded := request(t, readOnly, http.MethodGet, "/api/tasks").Body.String()
	if strings.Contains(encoded, "removable") {
		t.Fatalf("a board that cannot assign published a removal rule: %s", encoded)
	}
}

// The page says which identity it would record an assignment against, because
// the form has to be able to say so before anybody has typed anything — and says
// nothing where there is no identity, which is what keeps the control off a
// board that could only ever be refused.
func TestBoardPageCarriesTheIdentityItAssignsAs(t *testing.T) {
	board := &assignableBoard{task: heldTask()}
	handler := NewHandler(board.options(t, []core.Task{heldTask()}))
	body := request(t, handler, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `data-assign-identity="`+boardIdentity+`"`) {
		t.Fatal("the page does not say which identity it assigns as")
	}
	readOnly := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{heldTask()}, nil })
	readOnlyBody := request(t, readOnly, http.MethodGet, "/").Body.String()
	if !strings.Contains(readOnlyBody, `data-assign-identity=""`) {
		t.Fatal("a board that cannot assign did not say so")
	}
}

// An identity with nothing to write it through is no identity at all: a board
// given the name and neither mutation must draw no control, because every one
// of them would be refused.
func TestABoardWithAnIdentityAndNoMutationsCannotAssign(t *testing.T) {
	handler := NewHandler(Options{
		Identity: boardIdentity,
		List:     func(context.Context) ([]core.Task, error) { return []core.Task{heldTask()}, nil },
	})
	body := request(t, handler, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `data-assign-identity=""`) {
		t.Fatal("a board with no assignment mutations advertised an identity")
	}
}

// assignablePageProgram builds the Node program for a task page on a board that
// can assign, with the helpers the section's tests reach for.
func assignablePageProgram(t *testing.T, board *assignableBoard, tasks []core.Task, body string) string {
	t.Helper()
	handler := NewHandler(board.options(t, tasks))
	response := request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET the task page status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks,
		Presentation: presentationForTasksAs(tasks, boardIdentity),
	})
	helpers := `
boardView.dataset.assignIdentity = ` + strconv.Quote(boardIdentity) + `;
function assignmentsPanel() {
  return findElement(main, (element) => element.dataset.taskPanel === "assignments");
}
function assignmentRows() {
  const panel = assignmentsPanel();
  if (!panel) throw new Error("the page is drawing no assignments section");
  return findElement(panel, (element) => hasDataKey(element, "panelList")).children;
}
function assignmentForm() {
  const panel = assignmentsPanel();
  const form = panel && findElement(panel, (element) => hasDataKey(element, "assignmentForm"));
  if (!form) throw new Error("the assignments section draws no form");
  return form;
}
function assignmentField() {
  return findElement(assignmentForm(), (element) => element.tagName === "INPUT");
}
function submitAssignment() {
  return assignmentForm().eventListeners.submit({ preventDefault() {} });
}
function withdrawControl(row) {
  return findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === "Unassign");
}
function assignmentStatusText() {
  const line = findElement(assignmentsPanel(), (element) => hasDataKey(element, "panelStatus"));
  return line ? line.textContent : "";
}
`
	// The harness sets the identity attribute before the script reads it, which
	// is the same order the browser sees: the server renders the attribute into
	// the page and the script reads it on its first pass.
	return clientDOMHarness("/tasks/"+tasks[0].ID, string(document)) + helpers + script + `
function assignmentAnswer(assignments) {
  return {
    format: "workbook.task-mutation",
    version: 1,
    task: taskDocument.tasks[0],
    assignments
  };
}
` + body
}

// The form sends the value somebody typed to the assignment route, and the
// section redraws from what came back rather than from a guess.
func TestHandlerClientAssignsFromTheTaskPage(t *testing.T) {
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, `
setTimeout(async () => {
  const sent = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    sent.push({ url, options });
    return { ok: true, json: async () => assignmentAnswer([
      { principal: "someone@example.com", createdAt: new Date().toISOString(), ago: "assigned just now", removable: true }
    ]) };
  };
  assignmentField().value = "someone@example.com";
  await submitAssignment();
  if (sent.length !== 1) throw new Error("the form sent " + sent.length + " requests");
  if (sent[0].url !== `+strconv.Quote(assignmentsPath(assignableTaskID))+`) {
    throw new Error("the form posted to " + sent[0].url);
  }
  if (sent[0].options.method !== "POST") throw new Error("the form used " + sent[0].options.method);
  const body = JSON.parse(sent[0].options.body);
  if (body.to !== "someone@example.com") throw new Error("the body carried " + JSON.stringify(body));
  if (body.expectedHead !== "head-1") throw new Error("the body named no tip: " + JSON.stringify(body));
  const rows = assignmentRows();
  if (rows.length !== 1 || !rows[0].textContent.includes("someone@example.com")) {
    throw new Error("the section did not redraw from the answer: " + rows.map((row) => row.textContent).join("|"));
  }
  if (assignmentField().value !== "") throw new Error("the form kept the value it sent");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the assign form: %v\n%s", err, output)
	}
}

// A blank field is this board's own identity, which is what the placeholder says
// and what the empty value means to core. The client sends the empty value
// rather than filling the address in, so one place resolves it.
func TestHandlerClientAssignsTheBoardsIdentityFromAnEmptyField(t *testing.T) {
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, `
setTimeout(async () => {
  const sent = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    sent.push({ url, options });
    return { ok: true, json: async () => assignmentAnswer([]) };
  };
  if (assignmentField().placeholder !== `+strconv.Quote(boardIdentity)+`) {
    throw new Error("the field does not say who it would assign: " + JSON.stringify(assignmentField().placeholder));
  }
  assignmentField().value = "   ";
  await submitAssignment();
  if (sent.length !== 1) throw new Error("a blank field sent " + sent.length + " requests");
  if (JSON.parse(sent[0].options.body).to !== "") {
    throw new Error("the client resolved the identity itself: " + sent[0].options.body);
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the blank assign: %v\n%s", err, output)
	}
}

// A row carries a withdrawal only where the server said this board may make one,
// and the withdrawal names the whole value — principal and agent label — because
// that is the assignment core is asked to remove.
func TestHandlerClientWithdrawsOnlyTheAssignmentsItMay(t *testing.T) {
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, `
setTimeout(async () => {
  const rows = assignmentRows();
  if (rows.length !== 3) throw new Error("assignment rows = " + rows.length);
  if (!withdrawControl(rows[0]) || !withdrawControl(rows[1])) {
    throw new Error("a removable assignment drew no withdrawal");
  }
  if (withdrawControl(rows[2])) throw new Error("a stranger's assignment drew a withdrawal");
  const sent = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    sent.push({ url, options });
    return { ok: true, json: async () => assignmentAnswer([]) };
  };
  await withdrawControl(rows[0]).eventListeners.click();
  if (sent.length !== 1) throw new Error("the withdrawal sent " + sent.length + " requests");
  if (sent[0].options.method !== "DELETE") throw new Error("the withdrawal used " + sent[0].options.method);
  const body = JSON.parse(sent[0].options.body);
  if (body.from !== "dylan@example.com/impl-1") throw new Error("the withdrawal named " + JSON.stringify(body));
  if (assignmentRows().length !== 0) throw new Error("the section did not redraw from the answer");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the withdrawal: %v\n%s", err, output)
	}
}

// A task nobody holds draws the section anyway on a board that can assign, which
// is the whole difference this story makes: the read-only board draws nothing
// there, because there is nothing to read and nothing to do.
func TestHandlerClientOffersTheFormOnATaskNobodyHolds(t *testing.T) {
	node := requireNode(t)
	free := clientPlacementTask(assignableTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Head = "head-1"
	board := &assignableBoard{task: free}
	program := assignablePageProgram(t, board, []core.Task{free}, `
setTimeout(() => {
  const panel = assignmentsPanel();
  if (!panel) throw new Error("a board that can assign drew no assignments section");
  if (assignmentRows().length !== 0) throw new Error("an unheld task drew a row");
  assignmentForm();
  const count = findElement(panel, (element) => hasDataKey(element, "panelCount"));
  if (count.textContent !== "0 assignments") throw new Error("count = " + JSON.stringify(count.textContent));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the unheld task page: %v\n%s", err, output)
	}
}

// A refusal is said in the panel the change was made in, and the value the
// reader typed is kept so they can correct it rather than retype it.
func TestHandlerClientReportsARefusedAssignmentInThePanel(t *testing.T) {
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, `
setTimeout(async () => {
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    return {
      ok: false,
      json: async () => ({
        format: "workbook.error",
        version: 1,
        error: { category: "validation", message: "assignment principal \"dylan\" must be an email address, optionally followed by /label" }
      })
    };
  };
  assignmentField().value = "dylan";
  await submitAssignment();
  if (!assignmentStatusText().includes("must be an email address")) {
    throw new Error("the refusal was not reported: " + JSON.stringify(assignmentStatusText()));
  }
  if (assignmentField().value !== "dylan") throw new Error("a refused assignment took the reader's value with it");
  if (assignmentRows().length !== 3) throw new Error("a refusal changed the section");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the refused assignment: %v\n%s", err, output)
	}
}

// Assigning from a board is additive rather than a claim, so an assignment that
// lands beside somebody else's is recorded — and said out loud, in the panel it
// was made in, with the warning the command line prints for the same outcome.
func TestHandlerClientReportsASharedAssignment(t *testing.T) {
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, `
setTimeout(async () => {
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    return {
      ok: true,
      json: async () => Object.assign(assignmentAnswer([]), {
        warnings: [{ code: "assignment-shared", message: "task `+assignableTaskID+` is also assigned to sam@example.com" }]
      })
    };
  };
  assignmentField().value = "someone@example.com";
  await submitAssignment();
  const said = assignmentStatusText();
  if (!said.includes("also assigned to sam@example.com")) {
    throw new Error("the sharing was not reported: " + JSON.stringify(said));
  }
  if (!said.includes("Saved durably")) throw new Error("the warning did not read as a success: " + JSON.stringify(said));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the shared assignment: %v\n%s", err, output)
	}
}

// The section still follows the poll on a board that can assign: an assignment
// made from the CLI arrives here within a tick, and it does not take the value
// somebody is halfway through typing into the form above it.
func TestHandlerClientFollowsThePollWhileTheFormIsOpen(t *testing.T) {
	node := requireNode(t)
	free := clientPlacementTask(assignableTaskID, "Free task", core.StatusReady, core.PriorityMedium)
	free.Head = "head-1"
	assigned := free
	assigned.Assignments = []core.Assignment{
		{Principal: "late@example.com", Label: "spike", Creator: "late@example.com", CreatedAt: assignmentStamp},
	}
	board := &assignableBoard{task: free}
	program := assignablePageProgram(t, board, []core.Task{free}, `
const arrived = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{assigned},
		Presentation: presentationForTasksAs([]core.Task{assigned}, boardIdentity),
	}))+`;
setTimeout(async () => {
  const field = assignmentField();
  field.value = "half@an.edit";

  taskResponse = arrived;
  await intervalCallback();

  const rows = assignmentRows();
  if (rows.length !== 1 || !rows[0].textContent.includes("late@example.com")) {
    throw new Error("the poll did not bring the assignment: " + rows.map((row) => row.textContent).join("|"));
  }
  if (withdrawControl(rows[0])) throw new Error("a stranger's assignment drew a withdrawal after a poll");
  if (assignmentField().value !== "half@an.edit") throw new Error("the poll took the reader's value with it");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the assignable poll: %v\n%s", err, output)
	}
}

// presentationForTasksAs is presentationForTasks for a board that assigns as
// this identity, so a test can hand the page the removal rule the server would
// have derived rather than one it invented.
func presentationForTasksAs(tasks []core.Task, actor string) []TaskPresentation {
	views := make([]TaskPresentation, len(tasks))
	now := time.Now()
	for i, task := range tasks {
		row := assignmentRow(task.Assignments)
		views[i] = TaskPresentation{
			TaskID:          task.ID,
			IDPrefix:        task.ID,
			AssignmentChips: row.Chips,
			MoreAssignments: row.More,
			Assignments:     assignmentPresentation(task.Assignments, now, actor),
		}
	}
	return views
}
