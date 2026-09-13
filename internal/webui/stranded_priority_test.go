package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the task form does with a priority this project's vocabulary cannot
// resolve.
//
// A stored priority can resolve to nothing — a teammate on a newer build
// authors a priority, files a task under it, and this build has never heard of
// it. The select is built from the priorities the server published, so it has
// no option for that value, and a select with no selected option shows its
// first one. That is not a display problem: the form's save sends every field
// whose control disagrees with the task, so the reader's next save carries a
// priority they never chose. The status half of the form solved this with a
// disabled placeholder that names the value the server holds and sends nothing;
// these tests are the priority half of it.

// strandedPriority is a priority projectPriorities does not define and nothing
// resolves, so a task holding it opens a form that cannot offer it.
const strandedPriority = core.Priority("critical")

const strandedPriorityTaskID = "WB-01J0000000000000000000F707"

// strandedPriorityTask is a task filed under a priority this build cannot
// resolve, the way a teammate's newer build would have filed it.
func strandedPriorityTask() core.Task {
	task := clientPlacementTask(strandedPriorityTaskID, "Filed by a teammate", core.StatusReady, strandedPriority)
	task.Head = "head-a"
	return task
}

// The form names the priority the server holds instead of showing one the
// reader did not choose.
//
// The placeholder asks for one of this project's priorities, because there is
// no canonical set: the priorities are the project's, and the ones this select
// offers are the ones the server published on the page. The select is also the
// one place on the board where a priority is read as a name rather than as a
// token, which is what makes a sentence the right thing to put here.
func TestHandlerClientNamesAPriorityTheProjectCannotResolve(t *testing.T) {
	task := strandedPriorityTask()
	runPriorityClient(t, "a task at an unresolvable priority", "/tasks/"+task.ID, projectPriorities(t), []core.Task{task}, `
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the task form has no priority select");
  if (control.value !== "") {
    throw new Error("the form shows the priority as " + JSON.stringify(control.value) +
      ", which is a priority nobody chose for this task");
  }
  const placeholder = control.firstElementChild;
  if (!placeholder || !placeholder.disabled) {
    throw new Error("the select offers no placeholder, so it falls to " + JSON.stringify(control.children[0].value));
  }
  if (!placeholder.textContent.includes(`+strconv.Quote(string(strandedPriority))+`)) {
    throw new Error("the placeholder does not name the priority the server holds: " + JSON.stringify(placeholder.textContent));
  }
  if (!placeholder.textContent.includes("this project's priorities")) {
    throw new Error("the placeholder does not ask for one of this project's priorities: " + JSON.stringify(placeholder.textContent));
  }
  const offered = control.children.filter((option) => option.value !== "").map((option) => option.value);
  const want = ["urgent", "high", "soon", "low"];
  if (JSON.stringify(offered) !== JSON.stringify(want)) {
    throw new Error("the project's own priorities did not survive the placeholder: " + JSON.stringify(offered));
  }
`)
}

// Saving that form without touching the priority leaves the task at the
// priority it holds.
//
// This is the harm the placeholder exists to prevent, so it is asserted against
// what the save stores rather than against what the select displays: the
// request the client actually built is carried back into the server that would
// answer it, and the task is read afterwards. An untouched placeholder sends
// nothing about the priority, so the stored value is the one the teammate wrote.
func TestHandlerClientSavingAStrandedPriorityDoesNotReassignIt(t *testing.T) {
	task := strandedPriorityTask()
	const renamed = "Renamed, and nothing else"
	output := runPriorityClientReporting(t, "save a form at an unresolvable priority", "/tasks/"+task.ID,
		projectPriorities(t), []core.Task{task}, `
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  if (!form || !title) throw new Error("the task form did not render");
  // The one field the reader went near. The priority control is left exactly as
  // the form drew it.
  title.value = `+strconv.Quote(renamed)+`;
  await form.eventListeners.submit({ preventDefault() {} });
  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the save sent nothing");
  console.log("SAVE-BODY " + wrote.options.body);
`)
	body := reportedLine(t, output, "SAVE-BODY ")

	// The store this save lands in, holding the task as the teammate filed it.
	stored := task
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return []core.Task{stored}, nil },
		Update: func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			if id != task.ID {
				t.Fatalf("the save named task %q, want %q", id, task.ID)
			}
			if input.Title != nil {
				stored.Title = *input.Title
			}
			if input.Priority != nil {
				stored.Priority = *input.Priority
			}
			return core.MutationResult{Task: stored}, nil
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+task.ID, body)
	if response.Code != http.StatusOK {
		t.Fatalf("the save was refused (%d), which is not the failure this test is about: %s",
			response.Code, response.Body.String())
	}
	if stored.Priority != strandedPriority {
		t.Errorf("saving the form reassigned the task's priority to %q; it was filed at %q and the reader never touched the control",
			stored.Priority, strandedPriority)
	}
	if stored.Title != renamed {
		t.Errorf("the save did not carry the edit the reader made: title = %q, want %q", stored.Title, renamed)
	}
}

// Once the task holds a priority this project has, the placeholder is gone.
//
// A placeholder naming a priority the task has since left is a worse label than
// none, so it is removed rather than left in the list. This is the path that
// proves it: a form open when a board intent for its task is refused is
// corrected in place rather than rebuilt, against the version the board holds —
// which here is one a teammate has since moved onto a priority this project
// does define. The save afterwards says nothing about the priority either,
// because the baseline moved with the control.
func TestHandlerClientDropsThePriorityPlaceholderOnceTheProjectHasTheValue(t *testing.T) {
	task := strandedPriorityTask()
	// What the board holds by the time the refusal lands: the drop never
	// applied, and the priority the task was stranded at is one the project
	// defines now.
	settled := task
	settled.Priority = core.Priority("urgent")
	settled.Head = "head-b"
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{settled}, Presentation: presentationForTasks([]core.Task{settled}),
	})

	runPriorityClient(t, "a stranded priority the project catches up with", "/",
		projectPriorities(t), []core.Task{task}, `
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  if (!inProgress) throw new Error("the board rendered no In Progress column");

  const boardFetch = globalThis.fetch;
  const bodies = [];
  let releaseIntent;
  let refuseIntent = true;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (refuseIntent) {
        return new Promise((resolve) => {
          releaseIntent = () => {
            taskResponse = `+string(truth)+`;
            resolve({ ok: false, json: async () => ({
              format: "workbook.error", version: 1,
              error: { category: "stale-write", message: "task has changed since head-a; reload and try again" }
            }) });
          };
        });
      }
      bodies.push(JSON.parse(options.body));
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: `+string(mustJSON(t, settled))+`
      }) };
    }
    return boardFetch(url, options);
  };

  const card = boardCard(`+strconv.Quote(task.ID)+`);
  if (!card) throw new Error("the board drew no card for the stranded task");
  card.rect = { top: 0, bottom: 80 };
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(`+strconv.Quote(task.ID)+`);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the task form has no priority select");
  if (!control.firstElementChild || !control.firstElementChild.disabled || control.value !== "") {
    throw new Error("the form did not open on a placeholder, so this test would prove nothing");
  }

  releaseIntent();
  await pending;

  const shown = findElement(main, (element) => element.id === "task-priority");
  if (shown !== control) throw new Error("the correction rebuilt the form instead of correcting it");
  if (shown.value !== "urgent") {
    throw new Error("the field did not adopt the priority the task now holds: " + JSON.stringify(shown.value));
  }
  const leftover = shown.children.filter((option) => option.value === "");
  if (leftover.length !== 0) {
    throw new Error("the placeholder outlived the priority it named: " + JSON.stringify(leftover.map((option) => option.textContent)));
  }

  // The baseline moved with the control, so an untouched priority still sends
  // nothing — including no re-assertion of the value the correction brought in.
  refuseIntent = false;
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  title.value = "Renamed after the correction";
  await form.eventListeners.submit({ preventDefault() {} });
  if (!bodies.length) throw new Error("the save sent nothing");
  if ("priority" in bodies[0]) {
    throw new Error("the save re-asserted a priority the reader never touched: " + JSON.stringify(bodies[0]));
  }
`)
}

// reportedLine pulls the one line the client script printed with this prefix,
// so a request the client built can be answered by the server in Go.
func reportedLine(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if reported, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			if !json.Valid([]byte(reported)) {
				t.Fatalf("the client reported %s%s, which is not a request body", prefix, reported)
			}
			return reported
		}
	}
	t.Fatalf("the client script printed no %q line:\n%s", prefix, output)
	return ""
}
