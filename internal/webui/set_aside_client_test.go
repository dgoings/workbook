package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// setAsideEdit is a description edit the server's sync set aside. Its text
// carries a direction control, which the page draws as a space.
func setAsideEdit() core.SetAsideEdit {
	return core.SetAsideEdit{
		ParkedRef: "refs/workbook/reconciled/" + threadPageTaskID + "/0",
		Parked:    "parked-tip",
		Commit:    "parked-tip",
		Actor:     "alice@client.example",
		WallTime:  time.Date(2026, time.November, 21, 14, 3, 0, 0, time.UTC),
		Operations: []core.Operation{
			{ID: "01K0M6B8A4FTT8C39MXXYTWD00", Type: core.OperationFieldSet, Field: "description", Value: "Alice's‮text"},
		},
	}
}

func setAsidePresentation(tasks []core.Task, edits []core.SetAsideEdit) []TaskPresentation {
	presentation := threadPagePresentation(tasks)
	presentation[0].SetAside = edits
	return presentation
}

// The task page shows what the sync set aside — whose edit, the field and the
// text, cleaned as the board cleans text — between its heading and its form,
// dismisses it by the parked tip it was shown, and follows the poll: the
// notice goes when the presentation stops carrying the edit.
func TestHandlerClientShowsAndDismissesSetAsideEdits(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+tasks[0].ID)
	script := renderedClientScript(t, response.Body.String())
	with := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: setAsidePresentation(tasks, []core.SetAsideEdit{setAsideEdit()})})
	without := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: setAsidePresentation(tasks, nil)})
	program := clientDOMHarness("/tasks/"+tasks[0].ID, string(with)) + script + threadPanelHelpers + `
const sent = [];
const harnessFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") === "DELETE") {
    sent.push({ url, body: JSON.parse(options.body), type: options.headers["Content-Type"] });
    return { ok: true, json: async () => ({ format: "workbook.set-aside-dismissal", version: 1, taskId: ` + strconv.Quote(threadPageTaskID) + ` }) };
  }
  return harnessFetch(url, options);
};
async function settle() {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
}
setTimeout(async () => {
  await settle();
  const notice = findElement(main, (element) => hasDataKey(element, "setAside"));
  if (!notice || notice.hidden) throw new Error("the task page shows no set-aside notice");
  const said = notice.textContent;
  if (!said.includes("1 edit set aside") || !said.includes("alice@client.example") || !said.includes("Description")) {
    throw new Error("the notice says " + JSON.stringify(said));
  }
  const shown = findElement(notice, (element) => element.dataset.setAsideText === "description");
  if (!shown || shown.textContent !== "Alice's text") throw new Error("the set-aside text is " + JSON.stringify(shown && shown.textContent));
  const dismiss = findElement(notice, (element) => element.tagName === "BUTTON" && hasDataKey(element, "setAsideDismiss"));
  if (!dismiss) throw new Error("the notice offers no Dismiss");
  await dismiss.eventListeners.click();
  await settle();
  if (sent.length !== 1 || sent[0].url !== "/api/tasks/" + encodeURIComponent(` + strconv.Quote(threadPageTaskID) + `) + "/set-aside" ||
      sent[0].body.parkedRef !== ` + strconv.Quote(setAsideEdit().ParkedRef) + ` || sent[0].body.parked !== "parked-tip" ||
      sent[0].type !== "application/json") {
    throw new Error("the dismissal sent " + JSON.stringify(sent));
  }
  taskResponse = ` + string(without) + `;
  await intervalCallback();
  await settle();
  if (!notice.hidden) throw new Error("the notice stayed after the poll stopped carrying the edit");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the set-aside notice: %v\n%s", err, output)
	}
}

// The card says its task has edits set aside, as the server's card does, and
// stops saying so when the poll does.
func TestHandlerClientFlagsACardWithSetAsideEdits(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	tasks := []core.Task{threadPageTask()}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	script := renderedClientScript(t, request(t, handler, http.MethodGet, "/").Body.String())
	two := []core.SetAsideEdit{setAsideEdit(), setAsideEdit()}
	with := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: setAsidePresentation(tasks, two)})
	without := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: setAsidePresentation(tasks, nil)})
	program := clientDOMHarness("/", string(with)) + script + `
setTimeout(async () => {
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
  const flag = () => findElement(boardElement, (element) => hasDataKey(element, "setAsideFlag"));
  if (!flag() || flag().textContent !== "2 edits set aside") throw new Error("the card's flag is " + JSON.stringify(flag() && flag().textContent));
  taskResponse = ` + string(without) + `;
  await intervalCallback();
  for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
  if (flag()) throw new Error("the card kept its flag after the poll stopped carrying the edits");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the card's set-aside flag: %v\n%s", err, output)
	}
}

// The server's own card carries the same flag, so a page that loads with
// edits set aside says so before the first poll lands, and the poll's
// presentation names them.
func TestHandlerShowsSetAsideEditsOnTheServersCardAndInThePresentation(t *testing.T) {
	t.Parallel()
	tasks := boardTasks()
	edit := setAsideEdit()
	var heads map[string]string
	options := Options{
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
		SetAside: func(_ context.Context, given map[string]string) (map[string][]core.SetAsideEdit, error) {
			heads = given
			return map[string][]core.SetAsideEdit{tasks[0].ID: {edit}}, nil
		},
	}
	handler := NewHandler(options)
	page := request(t, handler, http.MethodGet, "/")
	body := page.Body.String()
	card := body[strings.Index(body, `data-task-id="`+tasks[0].ID+`"`):]
	card = card[:strings.Index(card, "</article>")]
	if !strings.Contains(card, `<div class="task-card__set-aside" data-set-aside-flag>1 edit set aside</div>`) {
		t.Fatalf("the server's card has no set-aside flag: %s", card)
	}
	other := body[strings.Index(body, `data-task-id="`+tasks[1].ID+`"`):]
	if other = other[:strings.Index(other, "</article>")]; strings.Contains(other, "set-aside") {
		t.Fatalf("a task with nothing set aside is flagged: %s", other)
	}

	document := decodeTasksDocument(t, handler, "/api/tasks")
	if heads[tasks[0].ID] != tasks[0].Head {
		t.Fatalf("the reader was given heads %v, want each listed task's", heads)
	}
	for _, view := range document.Presentation {
		want := 0
		if view.TaskID == tasks[0].ID {
			want = 1
		}
		if len(view.SetAside) != want {
			t.Fatalf("presentation of %s carries %#v, want %d edits", view.TaskID, view.SetAside, want)
		}
	}
	raw := request(t, handler, http.MethodGet, "/api/tasks").Body.String()
	if strings.Count(raw, `"setAside"`) != 1 || !strings.Contains(raw, `"parkedRef":"`+edit.ParkedRef+`"`) {
		t.Fatalf("the task data names setAside other than once: %s", raw)
	}
}

// A board built without a reader draws no flag and no member, which is every
// board built before this; the dismissal route answers as unconfigured.
func TestHandlerWithoutASetAsideReaderShowsNothing(t *testing.T) {
	t.Parallel()
	tasks := boardTasks()
	handler := NewHandler(Options{List: func(context.Context) ([]core.Task, error) { return tasks, nil }})
	if body := request(t, handler, http.MethodGet, "/api/tasks").Body.String(); strings.Contains(body, "setAside") {
		t.Fatalf("task data names setAside: %s", body)
	}
	if body := request(t, handler, http.MethodGet, "/").Body.String(); strings.Contains(body, "data-set-aside-flag") {
		t.Fatal("the page flags a card")
	}
	response := requestJSON(t, handler, http.MethodDelete, "/api/tasks/"+tasks[0].ID+"/set-aside", `{"parkedRef":"x","parked":"y"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("dismissal on a board without one = %d; body = %s", response.Code, response.Body.String())
	}
}

// The dismissal route takes the parked tip it was shown, answers in its own
// document, refuses a body that does not name one, answers the method
// question for its address, and logs who dismissed what.
func TestHandlerDismissesSetAsideEditsByTheTipShown(t *testing.T) {
	t.Parallel()
	tasks := boardTasks()
	var got []string
	log := &strings.Builder{}
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return tasks, nil },
		DismissSetAside: func(_ context.Context, taskID, parkedRef, parked string) error {
			got = append(got, taskID, parkedRef, parked)
			if parked == "moved" {
				return core.Errorf(core.CategoryStaleWrite, "the set-aside edits changed")
			}
			return nil
		},
		Identity:  "server@example.com",
		ServerLog: log,
	})
	path := "/api/tasks/" + tasks[0].ID + "/set-aside"
	response := requestJSON(t, handler, http.MethodDelete, path, `{"parkedRef":"refs/workbook/reconciled/`+tasks[0].ID+`/0","parked":"tip"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"format":"workbook.set-aside-dismissal"`) {
		t.Fatalf("dismissal = %d; body = %s", response.Code, response.Body.String())
	}
	if strings.Join(got, " ") != tasks[0].ID+" refs/workbook/reconciled/"+tasks[0].ID+"/0 tip" {
		t.Fatalf("dismissed %v", got)
	}
	if !strings.Contains(log.String(), "server@example.com dismissed the set-aside edits of "+tasks[0].ID) || !strings.Contains(log.String(), "commit tip") {
		t.Fatalf("server log = %q", log.String())
	}
	if response := requestJSON(t, handler, http.MethodDelete, path, `{"parkedRef":"r","parked":"moved"}`); response.Code != http.StatusConflict {
		t.Fatalf("a moved tip = %d; body = %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{}`, `{"parkedRef":"r"}`, `{"parked":"p"}`, `{"parkedRef":"r","parked":"p","extra":1}`} {
		if response := requestJSON(t, handler, http.MethodDelete, path, body); response.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d; want 400", body, response.Code)
		}
	}
	response = requestJSON(t, handler, http.MethodGet, path, ``)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodDelete {
		t.Fatalf("GET %s = %d, Allow %q", path, response.Code, response.Header().Get("Allow"))
	}
}
