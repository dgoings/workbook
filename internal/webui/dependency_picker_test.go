package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board polls about once a second, and every poll hands the dependency
// picker its candidates again. A reader who has moved the highlight down the
// open list with the arrow keys must find it where they left it after a poll
// that changed nothing, so Enter picks the task they were looking at. Pressed
// as real keys in a real browser, and read after the page has seen two more
// polls answered.
func TestBrowserKeepsThePickerHighlightAcrossPolls(t *testing.T) {
	t.Parallel()
	tasks := []core.Task{}
	for index := 1; index <= 4; index++ {
		tasks = append(tasks, clientPlacementTask(
			fmt.Sprintf("WB-01J0000000000000000000HL%02d", index),
			fmt.Sprintf("Highlight candidate %d", index),
			core.StatusReady, core.PriorityMedium))
	}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	var result struct {
		Before, After, Descendant string
		Polls                     int
		Open                      bool
	}
	openInBrowser(t, handler, browserPage{
		Width: 1280, Height: 900, Path: "/tasks/new",
		Setup: pageGeometryHelpers + `
(async () => {
  window.__polls = 0;
  const realFetch = window.fetch;
  window.fetch = async (url, options = {}) => {
    const response = await realFetch(url, options);
    if ((options.method || "GET") === "GET" && String(url).startsWith("/api/tasks")) window.__polls++;
    return response;
  };
  await waitFor(() => document.querySelector("#dependency-input"), "the dependency picker");
  const input = document.querySelector("#dependency-input");
  input.focus();
  // A press on the box opens the list whether or not the focus event fired.
  input.click();
  await waitFor(() => !document.querySelector("#dependency-listbox").hidden, "the picker's list to open");
})()`,
		Keys: []string{"ArrowDown", "ArrowDown"},
		Script: `
(async () => {
  const active = () => {
    const option = document.querySelector("#dependency-listbox .relationship-option.is-active");
    return option ? option.dataset.candidateId : "";
  };
  const before = active();
  const polls = window.__polls;
  const deadline = Date.now() + 10000;
  // Two polls answered, then a beat for the page to draw what they brought.
  while (window.__polls < polls + 2 && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 50));
  await new Promise((resolve) => setTimeout(resolve, 200));
  const input = document.querySelector("#dependency-input");
  const descendant = input.getAttribute("aria-activedescendant") || "";
  const named = descendant ? document.getElementById(descendant) : null;
  return {
    before,
    after: active(),
    descendant: named ? named.dataset.candidateId : "",
    polls: window.__polls - polls,
    open: !document.querySelector("#dependency-listbox").hidden
  };
})()`,
	}, &result)
	if result.Before != tasks[1].ID {
		t.Fatalf("two ArrowDowns highlighted %q, want the second candidate %q", result.Before, tasks[1].ID)
	}
	if result.Polls < 2 {
		t.Fatalf("the page answered %d polls while the test waited, so this proves nothing", result.Polls)
	}
	if !result.Open {
		t.Fatalf("the picker's list closed during the polls")
	}
	if result.After != result.Before {
		t.Errorf("after %d polls the highlight is on %q, want it still on %q", result.Polls, result.After, result.Before)
	}
	if result.Descendant != result.Before {
		t.Errorf("after %d polls the input's active descendant names %q, want %q", result.Polls, result.Descendant, result.Before)
	}
}

// dependencyPickerOffered names what the open picker lists, in its order.
const dependencyPickerOffered = `
const offered = () => findElements(relationshipSection(main), (element) =>
  element.attributes.role === "option").map((option) => option.dataset.candidateId).sort();
const expectOffered = (kind, want, why) => {
  chooseRelationshipKind(main, kind);
  const got = offered();
  const sorted = [...want].sort();
  if (JSON.stringify(got) !== JSON.stringify(sorted)) {
    throw new Error(why + ": " + kind + " offers " + JSON.stringify(got) + ", want " + JSON.stringify(sorted));
  }
};
`

// The picker does not offer a task whose edge the server would refuse as a
// cycle. The server refuses an edge from A to B whenever B already reaches A
// through live tasks' dependencies, however long the path, so the picker
// leaves out the same tasks: under Depends On, every task that already
// depends on this one, directly or through others; under Blocks, every task
// this one already depends on, directly or through others. A task that is
// only reachable the other way round is still offered: a second path to a
// prerequisite is not a cycle. A deleted task breaks a path, as it does on
// the server. Removing the row that made the path brings its tasks back.
func TestHandlerClientPickerLeavesOutTasksThatWouldCloseACycle(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000CY00", "Current task", core.StatusReady, core.PriorityMedium)
	current.Head = "head-1"
	prerequisite := clientPlacementTask("WB-01J0000000000000000000CY01", "Direct prerequisite", core.StatusReady, core.PriorityMedium)
	deeper := clientPlacementTask("WB-01J0000000000000000000CY02", "Prerequisite of the prerequisite", core.StatusReady, core.PriorityMedium)
	blocked := clientPlacementTask("WB-01J0000000000000000000CY03", "Directly blocked", core.StatusReady, core.PriorityMedium)
	further := clientPlacementTask("WB-01J0000000000000000000CY04", "Blocked through the blocked task", core.StatusReady, core.PriorityMedium)
	unrelated := clientPlacementTask("WB-01J0000000000000000000CY05", "Unrelated task", core.StatusReady, core.PriorityMedium)
	pastDeleted := clientPlacementTask("WB-01J0000000000000000000CY06", "Blocked only through a deleted task", core.StatusReady, core.PriorityMedium)
	deleted := clientPlacementTask("WB-01J0000000000000000000CY07", "Deleted task", core.StatusReady, core.PriorityMedium)
	deleted.Deleted = true
	current.Dependencies = []string{prerequisite.ID}
	prerequisite.Dependencies = []string{deeper.ID}
	blocked.Dependencies = []string{current.ID}
	further.Dependencies = []string{blocked.ID}
	deleted.Dependencies = []string{current.ID}
	pastDeleted.Dependencies = []string{deleted.ID}
	initial := []core.Task{current, prerequisite, deeper, blocked, further, unrelated, pastDeleted}
	freed := current
	freed.Dependencies = nil
	afterFree := []core.Task{freed, prerequisite, deeper, blocked, further, unrelated, pastDeleted}
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initial, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	quote := strconv.Quote
	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initial)) + script + relationshipKindProgramHelpers + dependencyPickerOffered + `
deletedTaskResponse = ` + documentJSON([]core.Task{deleted}) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  let nextTask = null;
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: nextTask }) };
    }
    return { ok: true, json: async () => url === "/api/tasks?deleted=true" ? deletedTaskResponse : taskResponse };
  };
  expectOffered("Depends On", [` + quote(deeper.ID) + `, ` + quote(unrelated.ID) + `, ` + quote(pastDeleted.ID) + `],
    "a task that depends on this one, directly or through another, would close a cycle");
  expectOffered("Blocks", [` + quote(further.ID) + `, ` + quote(unrelated.ID) + `, ` + quote(pastDeleted.ID) + `],
    "a task this one depends on, directly or through another, would close a cycle");
  // Free the stored prerequisite: the path to both prerequisites goes with it.
  const afterFree = ` + documentJSON(afterFree) + `;
  nextTask = afterFree.tasks[0];
  taskResponse = afterFree;
  await removeRow(` + quote(prerequisite.ID) + `);
  expectOffered("Blocks", [` + quote(prerequisite.ID) + `, ` + quote(deeper.ID) + `, ` + quote(further.ID) + `, ` + quote(unrelated.ID) + `, ` + quote(pastDeleted.ID) + `],
    "with the prerequisite freed, nothing this task depends on is left to close a cycle");
  expectOffered("Depends On", [` + quote(prerequisite.ID) + `, ` + quote(deeper.ID) + `, ` + quote(unrelated.ID) + `, ` + quote(pastDeleted.ID) + `],
    "freeing a prerequisite does not change what depends on this task");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the cycle-free picker on a task page: %v\n%s", err, output)
	}
}

// On New Task the edges are only staged, and the picker reads them as edges:
// a task staged under Blocks is not offered under Depends On, nor is anything
// that already depends on it, and the mirror for a staged Depends On. Taking
// the staged row off brings them back.
func TestHandlerClientNewTaskPickerLeavesOutTasksAStagedEdgeWouldCycle(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	toBlock := clientPlacementTask("WB-01J0000000000000000000CN01", "Staged as blocked", core.StatusReady, core.PriorityMedium)
	onBlocked := clientPlacementTask("WB-01J0000000000000000000CN02", "Depends on the staged blocked task", core.StatusReady, core.PriorityMedium)
	toDepend := clientPlacementTask("WB-01J0000000000000000000CN03", "Staged as prerequisite", core.StatusReady, core.PriorityMedium)
	underDepend := clientPlacementTask("WB-01J0000000000000000000CN04", "Prerequisite of the staged prerequisite", core.StatusReady, core.PriorityMedium)
	onBlocked.Dependencies = []string{toBlock.ID}
	toDepend.Dependencies = []string{underDepend.ID}
	tasks := []core.Task{toBlock, onBlocked, toDepend, underDepend}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}
	quote := strconv.Quote
	all := quote(toBlock.ID) + `, ` + quote(onBlocked.ID) + `, ` + quote(toDepend.ID) + `, ` + quote(underDepend.ID)
	program := clientDOMHarness("/tasks/new", string(document)) + script + relationshipKindProgramHelpers + dependencyPickerOffered + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  expectOffered("Depends On", [` + all + `], "with nothing staged every task is offered");
  chooseRelationshipKind(main, "Blocks");
  await pickAndAdd(` + quote(toBlock.ID) + `, "Staged as blocked");
  expectOffered("Depends On", [` + quote(toDepend.ID) + `, ` + quote(underDepend.ID) + `],
    "a staged blocked task, and what depends on it, would close a cycle");
  await pickAndAdd(` + quote(toDepend.ID) + `, "Staged as prerequisite");
  expectOffered("Blocks", [` + quote(onBlocked.ID) + `],
    "a staged prerequisite, and what it depends on, would close a cycle");
  await removeRow(` + quote(toBlock.ID) + `);
  expectOffered("Depends On", [` + quote(toBlock.ID) + `, ` + quote(onBlocked.ID) + `, ` + quote(underDepend.ID) + `],
    "taking off the staged blocked row brings it and what depends on it back");
  await removeRow(` + quote(toDepend.ID) + `);
  expectOffered("Blocks", [` + all + `], "with nothing staged again every task is offered");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the cycle-free picker on New Task: %v\n%s", err, output)
	}
}
