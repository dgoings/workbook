package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// How the shared task form arranges what it holds. The structure is built by
// the client, so it is asserted through the Node harness; the rules that place
// what the client built are asserted against the served stylesheet, because a
// fake DOM has no layout engine to read them with.

// newTaskPage renders the New Task shell and returns its HTML.
func newTaskPage(t *testing.T) string {
	t.Helper()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// runTaskFormClient renders the New Task page, executes its client script
// against the fake DOM, and runs body with the form already on screen.
func runTaskFormClient(t *testing.T, purpose, body string) {
	t.Helper()
	node := requireNode(t)
	tasks := []core.Task{clientPlacementTask("WB-01J0000000000000000000FF01", "Neighbour task", core.StatusReady, core.PriorityMedium)}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/new")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /tasks/new status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/tasks/new", string(document)) + script + `
setTimeout(async () => {
  const form = findElement(main, (element) => element.tagName === "FORM");
  if (!form) throw new Error("the New Task form did not render");
` + body + `
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute %s: %v\n%s", purpose, err, output)
	}
}

// The Labels control is a column, not a single input: the chiclets, the hint,
// and the announcement region stack under the box being typed in. A caption
// centred against all of that names nothing, so the field carries a hook that
// puts it on the input's line.
func TestHandlerClientTopAlignsTheLabelsCaption(t *testing.T) {
	runTaskFormClient(t, "Labels caption alignment", `
  const labelsInput = findElement(form, (element) => element.id === "task-labels");
  if (!labelsInput) throw new Error("the form has no Labels input");
  const field = findElement(form, (element) => classTokens(element).includes("field--labels"));
  if (!field) throw new Error("the Labels field carries no top-alignment hook");
  if (!field.contains(labelsInput)) throw new Error("the hook is on a field that does not hold the Labels input");
  const caption = field.children[0];
  if (!caption || caption.tagName !== "LABEL" || caption.htmlFor !== "task-labels") {
    throw new Error("the Labels caption is not the field's first child pointing at its input");
  }
  // The additive parts still follow the input rather than preceding it, which
  // is what makes aligning the caption with the input's line the right answer.
  const region = field.children[1];
  if (!region || region.children[0] !== labelsInput) {
    throw new Error("the Labels input is no longer the first thing in its control");
  }
  const chiclets = region.children[1];
  if (!chiclets || chiclets.tagName !== "UL") throw new Error("the label chiclets do not follow the input");
`)
	body := newTaskPage(t)
	for _, fragment := range []string{
		`.task-properties .field--labels { align-items: start; }`,
		`.task-properties .field--labels > label { padding-top: .65rem; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("Labels field styling does not contain %q", fragment)
		}
	}
}

// "Create more" says what the next Save does, so it belongs in the reader's
// path to that button rather than in the opposite corner of the footer, level
// with a Delete it has nothing to do with.
func TestHandlerClientPlacesCreateMoreAboveTheSaveButton(t *testing.T) {
	runTaskFormClient(t, "Create more placement", `
  const footer = findElement(form, (element) => classTokens(element).includes("task-actions"));
  const actionBar = footer && findElement(footer, (element) => classTokens(element).includes("form-actions"));
  const save = actionBar && findElement(actionBar, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Save");
  if (!footer || !actionBar || !save) throw new Error("the New Task footer lost its action bar");

  const toggle = findElement(form, (element) => element.id === "task-create-more");
  if (!toggle) throw new Error("the New Task form does not offer a Create more toggle");
  const wrapper = toggle.parentElement;
  if (!wrapper || !classTokens(wrapper).includes("create-more")) {
    throw new Error("the Create more checkbox lost the label that names it");
  }
  if (wrapper.parentElement !== footer) {
    throw new Error("Create more is not a row of the footer");
  }
  if (actionBar.contains(wrapper)) {
    throw new Error("Create more is still inside the action bar beside Save");
  }
  if (footer.children.indexOf(wrapper) >= footer.children.indexOf(actionBar)) {
    throw new Error("Create more is drawn below the Save button it governs");
  }
  // The feedback line still leads the footer: a save that has something to say
  // says it above everything the reader could press next.
  const status = findElement(footer, (element) => classTokens(element).includes("form-status"));
  if (footer.children.indexOf(status) !== 0) {
    throw new Error("the footer no longer opens with its feedback line");
  }
  // Changing it is still what arms the next save.
  toggle.checked = true;
  toggle.eventListeners.change();
  const rearmed = findElement(main, (element) => element.id === "task-create-more");
  if (!rearmed.checked) throw new Error("the relocated toggle stopped recording the choice");
`)
	body := newTaskPage(t)
	for _, fragment := range []string{
		// Slimmer than the page section it was padded like: the footer holds one
		// line of feedback and one row of buttons.
		`.task-actions { display: grid; grid-column: 1 / -1; gap: .4rem; padding: .5rem 1.15rem .6rem;`,
		`justify-self: start`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("task footer styling does not contain %q", fragment)
		}
	}
	for _, stale := range []string{
		`padding: .85rem 1.15rem 1.15rem`,
		`.create-more { display: inline-flex; align-items: center; gap: .4rem; margin-left: auto`,
	} {
		if strings.Contains(body, stale) {
			t.Errorf("task footer styling still contains %q", stale)
		}
	}
}
