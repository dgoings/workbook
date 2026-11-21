package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The sidebar's one dependency section: one picker, a toggle saying whether
// the next entry is a Depends On or a Blocks edge, and one list holding both
// kinds, each row tagged in words, Depends On rows first.

// relationshipKindProgramHelpers works the section the way a reader does,
// without going through relationshipGroup: that view chooses a kind on every
// read, and these tests are about what the toggle does when nobody touches it.
const relationshipKindProgramHelpers = `
const sectionPicker = () => {
  const section = relationshipSection(main);
  return {
    input: findElement(section, (element) => element.attributes.role === "combobox"),
    add: findElement(section, (element) => element.tagName === "BUTTON" && element.getAttribute("aria-label") === "Add dependency")
  };
};
const pickAndAdd = async (candidateID, query) => {
  const picker = sectionPicker();
  picker.input.value = query;
  picker.input.eventListeners.input();
  const option = findElement(relationshipSection(main), (element) =>
    element.attributes.role === "option" && element.dataset.candidateId === candidateID);
  if (!option) throw new Error("the picker does not offer " + candidateID);
  option.eventListeners.click();
  if (picker.add.disabled) throw new Error("choosing " + candidateID + " did not enable Add dependency");
  await picker.add.eventListeners.click();
};
const checkedKind = () => relationshipKindRadios(main).filter((radio) => radio.checked).map((radio) => radio.value).join();
// Each row as "kind tag:id", in the order the list draws them. The tag is read
// from the row's text, because the kind has to be said in words.
const listedRows = () => findElements(relationshipSection(main), (element) => hasDataKey(element, "relationshipRow")).map((row) => {
  const tag = findElement(row, (element) => hasClassToken(element, "relationship-row__kind"));
  if (!tag) throw new Error("row " + row.dataset.relationshipId + " carries no kind tag");
  if (!hasClassToken(tag, "relationship-row__kind--" + row.dataset.relationshipKind)) {
    throw new Error("row " + row.dataset.relationshipId + " is tagged " + tag.textContent + " but listed as " + row.dataset.relationshipKind);
  }
  return tag.textContent + ":" + row.dataset.relationshipId;
});
const removeRow = async (id) => {
  const row = findElement(relationshipSection(main), (element) => hasDataKey(element, "relationshipRow") && element.dataset.relationshipId === id);
  const remove = row && findElement(row, (element) => element.tagName === "BUTTON" && /^Remove /.test(element.getAttribute("aria-label") || ""));
  if (!remove) throw new Error("row " + id + " has no Remove");
  await remove.eventListeners.click();
};
`

// On a New Task form the toggle starts on Depends On and stays where it is
// put: two Blocks entries in a row are one choice and two picks. The list puts
// Depends On first whatever order the entries were made in, tags each row,
// and removing a staged row of either kind writes nothing.
func TestHandlerClientStagesBothDependencyKindsInOneTaggedList(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	firstBlocked := clientPlacementTask("WB-01J0000000000000000000KB01", "First blocked", core.StatusBacklog, core.PriorityLow)
	secondBlocked := clientPlacementTask("WB-01J0000000000000000000KB02", "Second blocked", core.StatusReady, core.PriorityMedium)
	prerequisite := clientPlacementTask("WB-01J0000000000000000000KD01", "Prerequisite", core.StatusDone, core.PriorityHigh)
	tasks := []core.Task{firstBlocked, secondBlocked, prerequisite}
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
	program := clientDOMHarness("/tasks/new", string(document)) + script + relationshipKindProgramHelpers + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  fetchCalls.splice(0);
  if (checkedKind() !== "depends-on") throw new Error("the toggle opens on " + JSON.stringify(checkedKind()) + ", want Depends On");
  if (sectionPicker().input.placeholder !== "Prerequisite") throw new Error("the picker does not open asking for a prerequisite");

  chooseRelationshipKind(main, "Blocks");
  if (sectionPicker().input.placeholder !== "Task to block") throw new Error("choosing Blocks did not change what the picker asks for");
  await pickAndAdd(` + strconv.Quote(firstBlocked.ID) + `, "First");
  if (checkedKind() !== "blocks") throw new Error("the toggle left Blocks after an add");
  if (sectionPicker().input.value !== "") throw new Error("the picker kept the added task's title");
  // No second choice: the toggle is still on Blocks.
  await pickAndAdd(` + strconv.Quote(secondBlocked.ID) + `, "Second");
  chooseRelationshipKind(main, "Depends On");
  await pickAndAdd(` + strconv.Quote(prerequisite.ID) + `, "Prerequisite");

  const want = [
    "Depends On:` + prerequisite.ID + `",
    "Blocks:` + firstBlocked.ID + `",
    "Blocks:` + secondBlocked.ID + `"
  ];
  if (JSON.stringify(listedRows()) !== JSON.stringify(want)) {
    throw new Error("the list = " + JSON.stringify(listedRows()) + ", want " + JSON.stringify(want));
  }
  if (findElements(relationshipSection(main), (element) => hasDataKey(element, "relationshipDraft")).length !== 3) {
    throw new Error("the staged rows are not drawn as drafts");
  }
  if (findElement(relationshipSection(main), (element) => element.textContent === "No dependencies.")) {
    throw new Error("the list still says it is empty");
  }

  await removeRow(` + strconv.Quote(firstBlocked.ID) + `);
  await removeRow(` + strconv.Quote(prerequisite.ID) + `);
  if (JSON.stringify(listedRows()) !== JSON.stringify(["Blocks:` + secondBlocked.ID + `"])) {
    throw new Error("removing a staged row of each kind left " + JSON.stringify(listedRows()));
  }
  await removeRow(` + strconv.Quote(secondBlocked.ID) + `);
  const empty = findElements(relationshipSection(main), (element) =>
    hasClassToken(element, "relationship-row__note") && element.textContent === "No dependencies.");
  if (listedRows().length !== 0 || empty.length !== 1) throw new Error("an emptied list does not say so once");
  if (fetchCalls.some((call) => (call.options.method || "GET") !== "GET")) {
    throw new Error("staging and unstaging wrote before the task existed");
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the staged dependency list: %v\n%s", err, output)
	}
}

// On a task's page the list draws what is stored, Depends On first, and the
// toggle stays on Blocks through a write and the refresh after it, so a second
// Blocks entry is a second pick and each one is written to the task it names.
func TestHandlerClientKeepsTheDependencyKindAcrossTaskPageAdds(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000KC00", "Current task", core.StatusReady, core.PriorityMedium)
	current.Head = "head-1"
	prerequisite := clientPlacementTask("WB-01J0000000000000000000KD01", "Stored prerequisite", core.StatusDone, core.PriorityHigh)
	storedBlocked := clientPlacementTask("WB-01J0000000000000000000KB00", "Stored blocked task", core.StatusBacklog, core.PriorityLow)
	firstBlocked := clientPlacementTask("WB-01J0000000000000000000KB01", "First new blocked", core.StatusBacklog, core.PriorityLow)
	secondBlocked := clientPlacementTask("WB-01J0000000000000000000KB02", "Second new blocked", core.StatusReady, core.PriorityMedium)
	unrelated := clientPlacementTask("WB-01J0000000000000000000KU01", "Unrelated task", core.StatusReady, core.PriorityMedium)
	current.Dependencies = []string{prerequisite.ID}
	storedBlocked.Dependencies = []string{current.ID}
	initial := []core.Task{storedBlocked, current, prerequisite, firstBlocked, secondBlocked, unrelated}
	firstAdded := firstBlocked
	firstAdded.Dependencies = []string{current.ID}
	afterFirst := []core.Task{storedBlocked, current, prerequisite, firstAdded, secondBlocked, unrelated}
	secondAdded := secondBlocked
	secondAdded.Dependencies = []string{current.ID}
	afterSecond := []core.Task{storedBlocked, current, prerequisite, firstAdded, secondAdded, unrelated}
	documentJSON := func(tasks []core.Task) string {
		t.Helper()
		document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	emptyDeleted, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{}})
	if err != nil {
		t.Fatal(err)
	}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return initial, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	program := clientDOMHarness("/tasks/"+current.ID, documentJSON(initial)) + script + relationshipKindProgramHelpers + `
deletedTaskResponse = ` + string(emptyDeleted) + `;
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
  // The stored Blocks row comes first in the board's order, and is still
  // listed after the Depends On row.
  const before = ["Depends On:` + prerequisite.ID + `", "Blocks:` + storedBlocked.ID + `"];
  if (JSON.stringify(listedRows()) !== JSON.stringify(before)) {
    throw new Error("the stored edges are listed as " + JSON.stringify(listedRows()) + ", want " + JSON.stringify(before));
  }
  fetchCalls.splice(0);
  chooseRelationshipKind(main, "Blocks");
  const afterFirst = ` + documentJSON(afterFirst) + `;
  nextTask = afterFirst.tasks[3];
  taskResponse = afterFirst;
  await pickAndAdd(` + strconv.Quote(firstBlocked.ID) + `, "First new");
  if (checkedKind() !== "blocks") throw new Error("the toggle left Blocks after a write");
  const afterSecond = ` + documentJSON(afterSecond) + `;
  nextTask = afterSecond.tasks[4];
  taskResponse = afterSecond;
  await pickAndAdd(` + strconv.Quote(secondBlocked.ID) + `, "Second new");
  const writes = fetchCalls.filter((call) => call.options.method === "PUT").map((call) => call.url);
  const wantWrites = [
    "/api/tasks/` + firstBlocked.ID + `/dependencies/` + current.ID + `",
    "/api/tasks/` + secondBlocked.ID + `/dependencies/` + current.ID + `"
  ];
  if (JSON.stringify(writes) !== JSON.stringify(wantWrites)) {
    throw new Error("the Blocks adds wrote " + JSON.stringify(writes) + ", want each edge on the task it names: " + JSON.stringify(wantWrites));
  }
  const after = [
    "Depends On:` + prerequisite.ID + `",
    "Blocks:` + storedBlocked.ID + `",
    "Blocks:` + firstBlocked.ID + `",
    "Blocks:` + secondBlocked.ID + `"
  ];
  if (JSON.stringify(listedRows()) !== JSON.stringify(after)) {
    throw new Error("after two Blocks adds the list is " + JSON.stringify(listedRows()) + ", want " + JSON.stringify(after));
  }
  // Neither kind offers a task already on either side of this one: under
  // Blocks a blocked task is already there and the stored prerequisite would
  // close a cycle, and under Depends On it is the other way around. Only the
  // unrelated task is left to offer, and never this task itself.
  const offered = () => findElements(relationshipSection(main), (element) => element.attributes.role === "option").map((option) => option.dataset.candidateId);
  if (JSON.stringify(offered()) !== JSON.stringify([` + strconv.Quote(unrelated.ID) + `])) {
    throw new Error("Blocks offers " + JSON.stringify(offered()));
  }
  chooseRelationshipKind(main, "Depends On");
  if (JSON.stringify(offered()) !== JSON.stringify([` + strconv.Quote(unrelated.ID) + `])) {
    throw new Error("Depends On offers " + JSON.stringify(offered()));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page dependency kind: %v\n%s", err, output)
	}
}

// A task picked under one kind and not offered under the other is let go of
// when the toggle moves: the box empties and Add stays off, rather than the
// box still showing a title that Add will not take. A pick both kinds offer
// survives the move. The task Blocks offers and Depends On does not is one
// that already depends on this task through another: a second path to it is
// no cycle, but depending on it would be.
func TestHandlerClientDropsAPickTheNewDependencyKindExcludes(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	current := clientPlacementTask("WB-01J0000000000000000000KS00", "Current task", core.StatusReady, core.PriorityMedium)
	current.Head = "head-1"
	blocked := clientPlacementTask("WB-01J0000000000000000000KS01", "Stored blocked task", core.StatusDone, core.PriorityHigh)
	further := clientPlacementTask("WB-01J0000000000000000000KS03", "Further blocked task", core.StatusReady, core.PriorityHigh)
	unrelated := clientPlacementTask("WB-01J0000000000000000000KS02", "Unrelated task", core.StatusBacklog, core.PriorityLow)
	blocked.Dependencies = []string{current.ID}
	further.Dependencies = []string{blocked.ID}
	tasks := []core.Task{current, blocked, further, unrelated}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/<id> status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	if err != nil {
		t.Fatal(err)
	}
	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + relationshipKindProgramHelpers + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const pick = (candidateID, query) => {
    const picker = sectionPicker();
    picker.input.value = query;
    picker.input.eventListeners.input();
    const option = findElement(relationshipSection(main), (element) =>
      element.attributes.role === "option" && element.dataset.candidateId === candidateID);
    if (!option) throw new Error("the picker does not offer " + candidateID);
    option.eventListeners.click();
    if (picker.add.disabled) throw new Error("choosing " + candidateID + " did not enable Add dependency");
  };
  const selected = () => findElements(relationshipSection(main), (element) =>
    element.attributes.role === "option" && element.attributes["aria-selected"] === "true").map((option) => option.dataset.candidateId);

  // Blocks offers the further blocked task; Depends On does not.
  chooseRelationshipKind(main, "Blocks");
  pick(` + strconv.Quote(further.ID) + `, "Further");
  chooseRelationshipKind(main, "Depends On");
  const picker = sectionPicker();
  if (picker.input.value !== "") {
    throw new Error("after the toggle moved to Depends On the box still reads " + JSON.stringify(picker.input.value));
  }
  if (!picker.add.disabled) throw new Error("Add dependency is on with nothing picked");
  if (selected().length !== 0) throw new Error("the picker still marks " + JSON.stringify(selected()) + " as chosen");

  // A task both kinds offer stays picked.
  pick(` + strconv.Quote(unrelated.ID) + `, "Unrelated");
  chooseRelationshipKind(main, "Blocks");
  if (sectionPicker().input.value !== "Unrelated task" || sectionPicker().add.disabled) {
    throw new Error("a pick both kinds offer was dropped by the toggle: box " + JSON.stringify(sectionPicker().input.value));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the kind toggle over a pick: %v\n%s", err, output)
	}
}

// The toggle is a radio group a keyboard operates the way it operates any:
// the arrow keys move the choice and the focus together, the picker follows,
// and the focused option shows a focus ring. Pressed as real keys in a real
// browser, because the arrow-key behavior is the browser's own.
func TestBrowserOperatesTheDependencyKindToggleFromTheKeyboard(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) {
		return []core.Task{clientPlacementTask("WB-01J0000000000000000000KN01", "Neighbor task", core.StatusReady, core.PriorityMedium)}, nil
	})
	var result struct{ Checked, Focused, Placeholder, Outline string }
	report := `
    const radios = [...document.querySelectorAll(".task-relationships input[type=radio]")];
    const checked = radios.find((radio) => radio.checked);
    const label = checked && document.querySelector('label[for="' + checked.id + '"]');
    const outline = label ? getComputedStyle(label) : null;
    return {
      checked: checked ? checked.value : "",
      focused: document.activeElement && document.activeElement.id,
      placeholder: document.querySelector(".task-relationships [role=combobox]").placeholder,
      outline: outline ? outline.outlineStyle + " " + outline.outlineWidth : ""
    };`
	openInBrowser(t, handler, browserPage{
		Width: 1280, Height: 800, Path: "/tasks/new",
		Setup: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.querySelector("#dependency-kind-depends-on"), "the dependency toggle");
  // Where Tab lands on the group: its checked radio.
  document.querySelector("#dependency-kind-depends-on").focus();
})()`,
		Keys:   []string{"ArrowRight"},
		Script: `(() => {` + report + `})()`,
	}, &result)
	if result.Checked != "blocks" || result.Focused != "dependency-kind-blocks" {
		t.Fatalf("ArrowRight on Depends On left %q checked and %q focused, want Blocks for both", result.Checked, result.Focused)
	}
	if result.Placeholder != "Task to block" {
		t.Errorf("after ArrowRight the picker asks for %q, want %q", result.Placeholder, "Task to block")
	}
	if result.Outline == "" || result.Outline[:4] == "none" {
		t.Errorf("the focused option draws no focus ring: outline %q", result.Outline)
	}

	var back struct{ Checked, Focused, Placeholder string }
	openInBrowser(t, handler, browserPage{
		Width: 1280, Height: 800, Path: "/tasks/new",
		Setup: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.querySelector("#dependency-kind-depends-on"), "the dependency toggle");
  document.querySelector("#dependency-kind-depends-on").focus();
})()`,
		Keys:   []string{"ArrowRight", "ArrowLeft"},
		Script: `(() => {` + report + `})()`,
	}, &back)
	if back.Checked != "depends-on" || back.Focused != "dependency-kind-depends-on" || back.Placeholder != "Prerequisite" {
		t.Errorf("ArrowRight then ArrowLeft left %+v, want Depends On checked and focused, asking for a prerequisite", back)
	}
}

// writeRecorder passes requests to the board and keeps every one that is not
// a read, so a test can say that a key press wrote nothing.
type writeRecorder struct {
	next   http.Handler
	mu     sync.Mutex
	writes []string
}

func (recorder *writeRecorder) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		recorder.mu.Lock()
		recorder.writes = append(recorder.writes, request.Method+" "+request.URL.Path)
		recorder.mu.Unlock()
	}
	recorder.next.ServeHTTP(response, request)
}

func (recorder *writeRecorder) recorded() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.writes...)
}

// Enter on an "Add as" option chooses it and does nothing else. The radios
// sit inside the task form, where Chrome treats Enter as a request to submit
// it: on New Task that created the half-filled task, and on a task's page it
// saved the edit and left. Pressed as a real key in a real browser, because
// the submission is the browser's own.
func TestBrowserEnterOnTheDependencyKindChoosesItWithoutSubmitting(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/tasks/new", "/tasks/" + longSidebarTaskID} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			recorder := &writeRecorder{next: longSidebarBoard(t, "Long sidebar task")}
			var result struct {
				Path, Checked, Placeholder string
				Submitted                  bool
			}
			openInBrowser(t, recorder, browserPage{
				Width: 1280, Height: 800, Path: path,
				Setup: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.querySelector("#dependency-kind-blocks") && document.querySelector("#task-title"), "the task form and its dependency toggle");
  // An edit, so a submitted form has something to write.
  const title = document.querySelector("#task-title");
  title.value = "Half-filled title";
  title.dispatchEvent(new Event("input", { bubbles: true }));
  window.__submitted = false;
  document.querySelector("form.task-layout").addEventListener("submit", () => { window.__submitted = true; }, true);
  document.querySelector("#dependency-kind-blocks").focus();
})()`,
				Keys: []string{"Enter"},
				Script: `(async () => {
  // Long enough for a submitted form to have written and navigated.
  await new Promise((resolve) => setTimeout(resolve, 750));
  const checked = document.querySelector(".task-relationships input[type=radio]:checked");
  const picker = document.querySelector(".task-relationships [role=combobox]");
  return {
    path: location.pathname,
    submitted: window.__submitted === true,
    checked: checked ? checked.value : "",
    placeholder: picker ? picker.placeholder : ""
  };
})()`,
			}, &result)
			if result.Submitted {
				t.Errorf("Enter on the Blocks option submitted the task form")
			}
			if writes := recorder.recorded(); len(writes) != 0 {
				t.Errorf("Enter on the Blocks option wrote %v", writes)
			}
			if result.Path != path {
				t.Errorf("Enter on the Blocks option moved the page to %q, want it on %q", result.Path, path)
			}
			if result.Checked != "blocks" || result.Placeholder != "Task to block" {
				t.Errorf("Enter on the Blocks option left %q checked and the picker asking for %q, want Blocks and %q",
					result.Checked, result.Placeholder, "Task to block")
			}
		})
	}
}
