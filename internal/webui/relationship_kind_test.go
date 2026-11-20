package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
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
    add: findElement(section, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency")
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
  const remove = row && findElement(row, (element) => element.tagName === "BUTTON" && element.textContent === "Remove");
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
	current.Dependencies = []string{prerequisite.ID}
	storedBlocked.Dependencies = []string{current.ID}
	initial := []core.Task{storedBlocked, current, prerequisite, firstBlocked, secondBlocked}
	firstAdded := firstBlocked
	firstAdded.Dependencies = []string{current.ID}
	afterFirst := []core.Task{storedBlocked, current, prerequisite, firstAdded, secondBlocked}
	secondAdded := secondBlocked
	secondAdded.Dependencies = []string{current.ID}
	afterSecond := []core.Task{storedBlocked, current, prerequisite, firstAdded, secondAdded}
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
  // With the toggle on Blocks, a task this one already blocks is not offered,
  // and the stored prerequisite is; on Depends On it is the other way round.
  const offered = () => findElements(relationshipSection(main), (element) => element.attributes.role === "option").map((option) => option.dataset.candidateId);
  if (offered().includes(` + strconv.Quote(firstBlocked.ID) + `) || !offered().includes(` + strconv.Quote(prerequisite.ID) + `)) {
    throw new Error("Blocks offers " + JSON.stringify(offered()));
  }
  chooseRelationshipKind(main, "Depends On");
  if (offered().includes(` + strconv.Quote(prerequisite.ID) + `) || !offered().includes(` + strconv.Quote(firstBlocked.ID) + `) ||
      offered().includes(` + strconv.Quote(current.ID) + `)) {
    throw new Error("Depends On offers " + JSON.stringify(offered()));
  }
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page dependency kind: %v\n%s", err, output)
	}
}

// The toggle is a radio group a keyboard operates the way it operates any:
// the arrow keys move the choice and the focus together, the picker follows,
// and the focused option shows a focus ring. Pressed as real keys in a real
// browser, because the arrow-key behavior is the browser's own.
func TestBrowserOperatesTheDependencyKindToggleFromTheKeyboard(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) {
		return []core.Task{clientPlacementTask("WB-01J0000000000000000000KN01", "Neighbour task", core.StatusReady, core.PriorityMedium)}, nil
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
