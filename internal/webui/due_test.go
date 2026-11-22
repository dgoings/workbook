package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// datedTask is a task due on the given date, projected the way core projects
// one: the date reported, and the state core judged it in.
func datedTask(id, title string, status core.Status, due string, state core.DueState) core.Task {
	task := clientPlacementTask(id, title, status, core.PriorityMedium)
	task.Head = "head-" + id
	task.Due = due
	if due != "" {
		reported := due
		task.ReportedDue = &reported
	}
	task.Overdue = state == core.DueStateOverdue
	task.DueSoon = state == core.DueStateSoon
	return task
}

const (
	dueOverdueID = "WB-01J00000000000000000000D01"
	dueSoonID    = "WB-01J00000000000000000000D02"
	dueLaterID   = "WB-01J00000000000000000000D03"
	dueDoneID    = "WB-01J00000000000000000000D04"
	dueNoneID    = "WB-01J00000000000000000000D05"
	dueEdgeID    = "WB-01J00000000000000000000D06"
)

// dueTasks are dated around the machine's own today, which is the reader's
// today for a test whose browser runs on the same machine.
func dueTasks() []core.Task {
	now := time.Now()
	day := func(offset int) string { return core.LocalDate(now.AddDate(0, 0, offset)) }
	return []core.Task{
		datedTask(dueOverdueID, "Late", core.StatusReady, day(-1), core.DueStateOverdue),
		datedTask(dueSoonID, "Today", core.StatusReady, day(0), core.DueStateSoon),
		datedTask(dueEdgeID, "Edge of soon", core.StatusReady, day(core.DueSoonDays), core.DueStateSoon),
		datedTask(dueLaterID, "Later", core.StatusBacklog, day(core.DueSoonDays+1), core.DueStateNone),
		datedTask(dueDoneID, "Shipped late", core.StatusDone, day(-5), core.DueStateNone),
		datedTask(dueNoneID, "Undated", core.StatusBacklog, "", core.DueStateNone),
	}
}

// servedTasks is the poll's document as the handler serves it, presentation
// and all, without the vocabulary members a harness built for the legacy
// statuses does not publish.
func servedTasks(t *testing.T, handler http.Handler) string {
	t.Helper()
	var document TasksDocument
	if err := json.Unmarshal(request(t, handler, http.MethodGet, "/api/tasks").Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	return string(mustJSON(t, TasksDocument{Format: document.Format, Version: document.Version, Tasks: document.Tasks, Presentation: document.Presentation}))
}

// The server's card says what the date means in words, beside the date, and a
// card for a task with no date draws no due line at all.
func TestHandlerServersCardMarksDueDatesInWords(t *testing.T) {
	t.Parallel()
	tasks := dueTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	body := request(t, handler, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `data-due-soon-days="3"`) {
		t.Fatal("the page does not publish how far ahead due soon reaches")
	}
	card := func(id string) string {
		start := strings.Index(body, `data-task-id="`+id+`"`)
		if start < 0 {
			t.Fatalf("no card for %s", id)
		}
		end := strings.Index(body[start:], "</article>")
		return body[start : start+end]
	}
	for id, want := range map[string]string{
		dueOverdueID: `task-card__due--overdue" data-due="overdue"><span class="task-card__due-word">Overdue</span> <time datetime="` + tasks[0].Due + `">`,
		dueSoonID:    `task-card__due--soon" data-due="due-soon"><span class="task-card__due-word">Due soon</span>`,
		dueLaterID:   `class="task-card__due" data-due=""><span class="task-card__due-word">Due</span>`,
		dueDoneID:    `class="task-card__due" data-due=""><span class="task-card__due-word">Due</span>`,
	} {
		if got := card(id); !strings.Contains(got, want) {
			t.Errorf("card %s = %s\nwant it to contain %s", id, got, want)
		}
	}
	if strings.Contains(card(dueNoneID), "task-card__due") {
		t.Error("an undated task's card draws a due line")
	}
}

// The poll's presentation names the finished tasks, because which statuses
// are done is the project's to say and the client judges the date itself.
func TestHandlerPresentationNamesFinishedDatedTasks(t *testing.T) {
	t.Parallel()
	tasks := dueTasks()
	deleted := datedTask("WB-01J00000000000000000000D07", "Deleted late", core.StatusReady, "2020-01-01", core.DueStateNone)
	deleted.Deleted = true
	undatedDone := datedTask("WB-01J00000000000000000000D08", "Done undated", core.StatusDone, "", core.DueStateNone)
	tasks = append(tasks, deleted, undatedDone)
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	var document TasksDocument
	// The whole board, deleted tasks included, as the board asks for it when
	// its Deleted column is shown.
	if err := json.Unmarshal(request(t, handler, http.MethodGet, "/api/tasks?deleted=include").Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	finished := map[string]bool{}
	for _, view := range document.Presentation {
		if view.DueFinished {
			finished[view.TaskID] = true
		}
	}
	want := map[string]bool{dueDoneID: true, deleted.ID: true}
	if !reflect.DeepEqual(finished, want) {
		t.Fatalf("dueFinished = %v, want %v", finished, want)
	}
}

// The client draws each card's due line from the reader's own today, as the
// server's card does, and redraws it when the poll brings another date.
func TestHandlerClientJudgesDueDatesAgainstTheReadersToday(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := dueTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	script := renderedClientScript(t, request(t, handler, http.MethodGet, "/").Body.String())
	first := servedTasks(t, handler)
	changed := append([]core.Task(nil), tasks...)
	changed[0] = datedTask(dueOverdueID, "Late", core.StatusReady, "", core.DueStateNone)
	changed[5] = datedTask(dueNoneID, "Undated", core.StatusBacklog, "2020-02-02", core.DueStateOverdue)
	second := servedTasks(t, listHandler(t, func(context.Context) ([]core.Task, error) { return changed, nil }))
	program := clientDOMHarness("/", first) + script + `
async function settle() {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
}
function dueLines() {
  const drawn = {};
  for (const card of findElements(boardElement, (element) => element.tagName === "ARTICLE" && hasDataKey(element, "taskId"))) {
    const line = findElement(card, (element) => hasDataKey(element, "due"));
    drawn[card.dataset.taskId] = line ? [line.dataset.due, line.className, line.textContent] : null;
  }
  return drawn;
}
setTimeout(async () => {
  await settle();
  const drawn = dueLines();
  const expect = (id, state, className, words) => {
    const got = drawn[id];
    if (!got || got[0] !== state || got[1] !== className || !got[2].startsWith(words)) {
      throw new Error(id + " draws " + JSON.stringify(got) + ", want " + JSON.stringify([state, className, words]));
    }
  };
  expect("` + dueOverdueID + `", "overdue", "task-card__due task-card__due--overdue", "Overdue ` + tasks[0].Due + `");
  expect("` + dueSoonID + `", "due-soon", "task-card__due task-card__due--soon", "Due soon ` + tasks[1].Due + `");
  expect("` + dueEdgeID + `", "due-soon", "task-card__due task-card__due--soon", "Due soon");
  expect("` + dueLaterID + `", "", "task-card__due", "Due ` + tasks[3].Due + `");
  expect("` + dueDoneID + `", "", "task-card__due", "Due ` + tasks[4].Due + `");
  if (drawn["` + dueNoneID + `"] !== null) throw new Error("an undated card draws " + JSON.stringify(drawn["` + dueNoneID + `"]));

  taskResponse = ` + second + `;
  await intervalCallback();
  await settle();
  const after = dueLines();
  if (after["` + dueOverdueID + `"] !== null) throw new Error("a cleared date still draws " + JSON.stringify(after["` + dueOverdueID + `"]));
  const dated = after["` + dueNoneID + `"];
  if (!dated || dated[0] !== "overdue" || dated[2] !== "Overdue 2020-02-02") throw new Error("a newly dated card draws " + JSON.stringify(dated));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the cards' due lines: %v\n%s", err, output)
	}
}

// A due date set aside by a sync is drawn like any other scalar field, and a
// cleared one says the date was removed rather than drawing an empty value.
func TestHandlerClientDrawsASetAsideDueDate(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	script := renderedClientScript(t, request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID).Body.String())
	edit := setAsideEdit()
	edit.Operations = []core.Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTWD11", Type: core.OperationFieldSet, Field: "due", Value: "2031-01-02"},
		{ID: "01K0M6B8A4FTT8C39MXXYTWD12", Type: core.OperationFieldSet, Field: "due", Value: ""},
	}
	with := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: setAsidePresentation(tasks, []core.SetAsideEdit{edit})})
	program := clientDOMHarness("/tasks/"+tasks[0].ID, string(with)) + script + threadPanelHelpers + `
setTimeout(async () => {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
  const notice = findElement(main, (element) => hasDataKey(element, "setAside"));
  if (!notice || notice.hidden) throw new Error("the task page shows no set-aside notice");
  const rows = findElements(notice, (element) => element.className === "set-aside__change");
  const drawn = rows.map((row) => row.children.filter((child) => child.className !== "set-aside__copy" && child.className !== "set-aside__note").map((child) => child.textContent));
  const want = [["Due", "2031-01-02"], ["Due date removed"]];
  if (JSON.stringify(drawn) !== JSON.stringify(want)) throw new Error("the notice draws " + JSON.stringify(drawn));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the set-aside due date: %v\n%s", err, output)
	}
}

// The task page holds the date in a date field, says what it means in words
// beside it, sends a changed date, and sends an emptied field as the empty
// string, which removes the date.
func TestHandlerClientTaskPageEditsAndClearsTheDueDate(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := dueTasks()[:1]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	script := renderedClientScript(t, request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID).Body.String())
	document := servedTasks(t, handler)
	program := clientDOMHarness("/tasks/"+tasks[0].ID, document) + script + threadPanelHelpers + `
const sent = [];
const boardFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") !== "GET") {
    sent.push({ url, method: options.method, body: JSON.parse(options.body) });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: taskDocument.tasks[0] }) };
  }
  return boardFetch(url, options);
};
async function settle() {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
}
setTimeout(async () => {
  await settle();
  const due = findElement(main, (element) => element.id === "task-due");
  if (!due || due.type !== "date" || due.name !== "due") throw new Error("the task page draws no date field: " + JSON.stringify(due && [due.type, due.name]));
  if (due.value !== "` + tasks[0].Due + `") throw new Error("the date field holds " + JSON.stringify(due.value));
  const mark = findElement(main, (element) => element.id === "task-due-state");
  if (!mark || mark.hidden || mark.textContent !== "Overdue" || mark.dataset.dueState !== "overdue") {
    throw new Error("the due state reads " + JSON.stringify(mark && [mark.hidden, mark.textContent, mark.dataset.dueState]));
  }
  if (due.getAttribute("aria-describedby") !== "task-due-state") throw new Error("the date field is not described by its state");
  const form = findElement(main, (element) => element.tagName === "FORM");
  due.value = "2031-03-04";
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
  due.value = "";
  await form.eventListeners.submit({ preventDefault() {} });
  await settle();
  const saves = sent.filter((call) => call.method === "PATCH").map((call) => call.body.due);
  console.log(JSON.stringify(saves));
}, 0);
`
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("execute the task page's due field: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if got := lines[len(lines)-1]; got != `["2031-03-04",""]` {
		t.Fatalf("the saves sent due %s, want the new date and then the empty string\n%s", got, output)
	}
}

// The create and update routes hand the date to core as given: a create with
// one sets it, an update with one changes it, an update with the empty string
// removes it, and an update that names no date leaves it alone.
func TestHandlerPassesTheDueDateToCreateAndUpdate(t *testing.T) {
	t.Parallel()
	var created []core.CreateInput
	var updated []core.UpdateInput
	result := core.MutationResult{Task: boardTasks()[0]}
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		Create: func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			created = append(created, input)
			return result, nil
		},
		Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
			updated = append(updated, input)
			return result, nil
		},
		UpdateStatus: unexpectedStatusUpdate(t),
	})
	for _, body := range []string{`{"title":"Dated","due":"2031-01-02"}`, `{"title":"Undated"}`} {
		if response := requestJSON(t, handler, http.MethodPost, "/api/tasks", body); response.Code != http.StatusOK {
			t.Fatalf("POST %s = %d; body = %s", body, response.Code, response.Body.String())
		}
	}
	if len(created) != 2 || created[0].Due != "2031-01-02" || created[1].Due != "" {
		t.Fatalf("create inputs = %#v", created)
	}
	for _, body := range []string{`{"due":"2031-02-03"}`, `{"due":""}`, `{"title":"Renamed"}`} {
		if response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/WB-01J00000000000000000000001", body); response.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d; body = %s", body, response.Code, response.Body.String())
		}
	}
	if len(updated) != 3 || updated[0].Due == nil || *updated[0].Due != "2031-02-03" ||
		updated[1].Due == nil || *updated[1].Due != "" || updated[2].Due != nil {
		t.Fatalf("update inputs = %#v", updated)
	}
}
