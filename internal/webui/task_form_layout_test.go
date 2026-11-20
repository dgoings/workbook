package webui

import (
	"context"
	"encoding/json"
	"fmt"
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
	t.Parallel()
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

// "Create more" says what the next Save does, so it sits on Save's own line,
// right after it, with its words before its box. The way out of a New Task
// form is Cancel, in the corner a task's Delete stands in.
func TestHandlerClientPlacesCreateMoreBesideTheSaveButton(t *testing.T) {
	t.Parallel()
	runTaskFormClient(t, "Create more placement", `
  const footer = findElement(form, (element) => classTokens(element).includes("task-actions"));
  const actionBar = footer && findElement(footer, (element) => classTokens(element).includes("form-actions"));
  const primary = actionBar && findElement(actionBar, (element) => classTokens(element).includes("form-primary-actions"));
  const save = primary && findElement(primary, (element) =>
    element.tagName === "BUTTON" && element.textContent === "Save");
  if (!footer || !actionBar || !primary || !save) throw new Error("the New Task footer lost its action bar");

  const toggle = findElement(form, (element) => element.id === "task-create-more");
  if (!toggle) throw new Error("the New Task form does not offer a Create more toggle");
  const wrapper = toggle.parentElement;
  if (!wrapper || wrapper.tagName !== "LABEL" || !classTokens(wrapper).includes("create-more")) {
    throw new Error("the Create more checkbox lost the label that names it");
  }
  if (wrapper.parentElement !== primary) throw new Error("Create more is not on Save's line");
  if (primary.children.indexOf(wrapper) !== primary.children.indexOf(save) + 1) {
    throw new Error("Create more does not follow the Save button it governs");
  }
  // The text first and the box to the right of it.
  if (wrapper.children.length !== 2 || wrapper.children[0].textContent !== "Create more" ||
      wrapper.children[1] !== toggle) {
    throw new Error("Create more does not put its words before its checkbox");
  }
  // Cancel, apart from the primary group and after it.
  const cancel = findElement(actionBar, (element) => element.tagName === "A" && element.textContent === "Cancel");
  if (!cancel || primary.contains(cancel)) throw new Error("Cancel is not apart from Save");
  const cancelGroup = cancel.parentElement;
  if (!classTokens(cancelGroup).includes("form-cancel") || cancelGroup.parentElement !== actionBar ||
      actionBar.children.indexOf(cancelGroup) <= actionBar.children.indexOf(primary)) {
    throw new Error("Cancel is not in the action bar's far corner");
  }
  if (findElement(form, (element) => element.tagName === "A" && element.textContent === "Back")) {
    throw new Error("the New Task form still offers Back");
  }
  // The footer is its feedback line and the one action bar.
  const status = findElement(footer, (element) => classTokens(element).includes("form-status"));
  if (footer.children.length !== 2 || footer.children[0] !== status || footer.children[1] !== actionBar) {
    throw new Error("the footer is not its feedback line over its action bar");
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
		`.form-danger, .form-cancel { margin-left: auto; }`,
		// The blank feedback line still stands in the document while empty, so
		// it can still announce, but it spends no height saying nothing.
		`.form-status:empty { min-height: 0; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("task footer styling does not contain %q", fragment)
		}
	}
	for _, stale := range []string{
		`padding: .85rem 1.15rem 1.15rem`,
		`.create-more { display: inline-flex; align-items: center; justify-self: start;`,
	} {
		if strings.Contains(body, stale) {
			t.Errorf("task footer styling still contains %q", stale)
		}
	}
}

// Depends On and Blocks are two thirds of the sidebar, and most of that was
// air: a caption on a row of its own repeating the heading above it, and two
// permanently reserved lines per group for messages that are almost never
// there. What each group still owes the reader is unchanged — a name for its
// combobox, and live regions that exist before they have anything to announce.
func TestHandlerClientCompactsTheRelationshipGroups(t *testing.T) {
	t.Parallel()
	runTaskFormClient(t, "relationship group compaction", `
  const sidebar = findElement(form, (element) => classTokens(element).includes("task-sidebar"));
  const groups = findElements(sidebar, (element) => classTokens(element).includes("relationship-group"));
  if (groups.length !== 2) throw new Error("the sidebar does not render both relationship groups: " + groups.length);

  const captions = [];
  groups.forEach((group) => {
    const heading = group.children[0];
    if (!heading || heading.tagName !== "H3") throw new Error("a relationship group lost its heading");
    const name = heading.textContent;
    const editor = findElement(group, (element) => classTokens(element).includes("relationship-editor"));
    const caption = editor && findElement(editor, (element) => element.tagName === "LABEL");
    const input = editor && findElement(editor, (element) => element.attributes.role === "combobox");
    if (!editor || !caption || !input) throw new Error(name + " lost its combobox");
    if (caption.htmlFor !== input.id) throw new Error(name + " no longer names its combobox");
    if (!classTokens(caption).includes("relationship-editor__label")) {
      throw new Error(name + " still spends a row of the sidebar on its caption");
    }
    if (!caption.textContent) throw new Error(name + " combobox has no accessible name");
    // The placeholder is what a sighted reader has left to go on, so it has to
    // be part of the name the control answers to: a speech user saying "click
    // prerequisite" reaches a box that says "Prerequisite" only while the two
    // strings still overlap.
    if (!input.placeholder) throw new Error(name + " combobox says nothing inside the box");
    if (!caption.textContent.toLowerCase().includes(input.placeholder.toLowerCase())) {
      throw new Error(name + " placeholder " + JSON.stringify(input.placeholder) +
        " is not part of its accessible name " + JSON.stringify(caption.textContent));
    }
    if (!findElement(editor, (element) => element.tagName === "BUTTON" && element.textContent === "Add dependency")) {
      throw new Error(name + " lost its Add dependency button");
    }
    // Both live regions are still in the document while empty. Only the height
    // they reserved is gone, and that is the stylesheet's business: a region
    // created in the same frame as its first message never announces it.
    const empty = findElement(editor, (element) => classTokens(element).includes("relationship-empty"));
    const message = findElement(group, (element) => classTokens(element).includes("relationship-message"));
    for (const region of [empty, message]) {
      if (!region || region.attributes.role !== "status" || region.attributes["aria-live"] !== "polite") {
        throw new Error(name + " no longer keeps its announcements in the document");
      }
      if (region.textContent !== "") throw new Error(name + " opens with a message it has no reason to have");
    }
    captions.push(caption.textContent);
  });
  if (captions[0] === captions[1]) throw new Error("both groups ask for the same thing: " + captions[0]);
`)
	body := newTaskPage(t)
	for _, fragment := range []string{
		`.relationship-editor__label { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }`,
		`.relationship-empty:empty, .relationship-message:empty { min-height: 0; margin: 0; }`,
		// "No matching tasks." is written on every keystroke that matches
		// nothing, so the combobox the reader is typing in keeps the row it
		// would appear on rather than growing one under their cursor.
		`.relationship-editor:focus-within .relationship-empty:empty { min-height: 1rem; }`,
		`.task-relationships { display: grid; gap: .7rem; padding: .7rem 0 0; }`,
		`.relationship-group { border-top: 1px solid var(--wb-hairline); padding-top: .7rem; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("relationship group styling does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `.relationship-editor label { grid-column: 1 / -1;`) {
		t.Error("the combobox caption still claims a row of its own")
	}
}

// sidebarOrderCheck asserts the sidebar holds, in order, the properties, the
// relationship groups, the assignments and the attachments — the order a New
// Task form and a task's page share.
const sidebarOrderCheck = `
function sidebarSections(form) {
  const sidebar = findElement(form, (element) => classTokens(element).includes("task-sidebar"));
  if (!sidebar) throw new Error("the form has no sidebar");
  const properties = findElement(sidebar, (element) => classTokens(element).includes("task-properties"));
  const relationships = findElement(sidebar, (element) => classTokens(element).includes("task-relationships"));
  const assignments = findElement(sidebar, (element) => element.dataset.taskPanel === "assignments");
  const attachments = findElement(sidebar, (element) => element.dataset.taskPanel === "attachments");
  if (!properties || !relationships || !assignments || !attachments) {
    throw new Error("the sidebar is missing a section: properties=" + Boolean(properties) +
      " relationships=" + Boolean(relationships) + " assignments=" + Boolean(assignments) +
      " attachments=" + Boolean(attachments));
  }
  // Each section's position among the sidebar's own children.
  const at = (node) => {
    let child = node;
    while (child.parentElement !== sidebar) child = child.parentElement;
    return sidebar.children.indexOf(child);
  };
  const order = [properties, relationships, assignments, attachments].map(at);
  if (!(order[0] < order[1] && order[1] < order[2] && order[2] < order[3])) {
    throw new Error("the sidebar order is " + JSON.stringify(order) +
      ", want properties, relationships, assignments, attachments");
  }
  return sidebar;
}
`

// A New Task form's sidebar is the task page's sidebar: the dependency inputs,
// then who to assign, then what to attach.
func TestHandlerClientOrdersTheNewTaskSidebar(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, sidebarOrderCheck+`
setTimeout(async () => {
  const form = await openCreateForm();
  sidebarSections(form);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the New Task sidebar order: %v\n%s", err, output)
	}
}

// A task's page lays its sidebar out as the New Task form does, and below the
// header and the form there is only the comment thread and the change log.
func TestHandlerClientOrdersTheTaskPageSidebar(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, sidebarOrderCheck+`
setTimeout(() => {
  const route = main.firstElementChild;
  const form = findElement(route, (element) => element.tagName === "FORM" && classTokens(element).includes("task-layout"));
  sidebarSections(form);
  // The header and the form are one block, the part held to the window, and
  // everything else follows it.
  const screen = route.children[0];
  if (!classTokens(screen).includes("task-route__screen") || form.parentElement !== screen ||
      !findElement(screen, (element) => element.tagName === "HEADER")) {
    throw new Error("the header and the form are not the route's first block");
  }
  const below = route.children.slice(1);
  if (below.length !== 2 || below[0].dataset.taskPanel !== "comments" ||
      !classTokens(below[1]).includes("task-history")) {
    throw new Error("below the form = " + JSON.stringify(below.map((node) =>
      node.dataset.taskPanel || node.className)) + ", want the comments and the history");
  }
  const footer = findElement(form, (element) => classTokens(element).includes("task-actions"));
  if (form.children[form.children.length - 1] !== footer) {
    throw new Error("the Save row is not the last thing in the form");
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page sidebar order: %v\n%s", err, output)
	}
}

// closeCheck clicks the header's × the way a reader does and asserts it left
// the way Back does: onto the board the reader came from, with one history
// entry pushed. The board is filtered first, so the address the form leaves for
// carries a query: with no filters set, the × and Back would both lead to "/"
// whether or not the × was given Back's address.
const closeCheck = `
const filteredBoard = "/?q=held";
async function followLink(address) {
  const link = document.createElement("a");
  link.href = address;
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() { this.defaultPrevented = true; }
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
}
// Leaves this page for the filtered board and comes back to it, which is how a
// reader opens a form from a narrowed board.
async function arriveFromFilteredBoard() {
  const at = new URL(window.location.href);
  const here = at.pathname + at.search;
  await followLink(filteredBoard);
  if (main.firstElementChild !== boardView) throw new Error("the filtered board did not render");
  await followLink(here);
  if (main.firstElementChild === boardView) throw new Error("the form did not render again after the board");
}
async function closeFromHeader() {
  const route = main.firstElementChild;
  const header = findElement(route, (element) => element.tagName === "HEADER");
  const close = header && findElement(header, (element) => hasDataKey(element, "taskClose"));
  if (!close || close.tagName !== "A") throw new Error("the header has no close link");
  if (close.getAttribute("aria-label") !== "Close" || close.textContent !== "×") {
    throw new Error("the close is not a × named Close");
  }
  if (header.children[header.children.length - 1] !== close) throw new Error("the × is not at the header's right");
  const leave = findElement(route, (element) => element.tagName === "A" && hasClassToken(element, "board-link") &&
    (element.textContent === "Back" || element.textContent === "Cancel"));
  if (!leave) throw new Error("the form has no Back or Cancel");
  if (leave.href !== filteredBoard) {
    throw new Error(leave.textContent + " leads to " + JSON.stringify(leave.href) + ", want the filtered board " + filteredBoard);
  }
  if (close.href !== leave.href) {
    throw new Error("the × leads to " + JSON.stringify(close.href) + ", not where " + leave.textContent + " goes: " + JSON.stringify(leave.href));
  }
  const pushed = historyPaths.length;
  await documentEventListeners.click({
    target: close, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() { this.defaultPrevented = true; }
  });
  if (main.firstElementChild !== boardView) throw new Error("the × did not return to the board");
  if (historyPaths.length !== pushed + 1) throw new Error("the × did not navigate the way Back does");
  if (historyPaths[historyPaths.length - 1] !== leave.href) {
    throw new Error("the × landed on " + JSON.stringify(historyPaths[historyPaths.length - 1]) + ", not on " + JSON.stringify(leave.href));
  }
}
`

// The New Task header is a compact band naming the page, with the × that
// closes it. The heading is still the region's name.
func TestHandlerClientDrawsACompactNewTaskHeaderThatCloses(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAssignmentProgram(t, closeCheck+`
setTimeout(async () => {
  await openCreateForm();
  const route = main.firstElementChild;
  const header = findElement(route, (element) => element.tagName === "HEADER");
  if (!classTokens(header).includes("task-route__header--compact")) throw new Error("the New Task header is not the compact band");
  const heading = findElement(header, (element) => element.tagName === "H2");
  if (!heading || heading.textContent !== "New task" || heading.id !== "task-form-title") {
    throw new Error("the New Task heading = " + JSON.stringify(heading && heading.textContent));
  }
  if (route.getAttribute("aria-labelledby") !== heading.id) throw new Error("the route is not named by its heading");
  if (route.textContent.includes("Add work to the board")) throw new Error("the old headline is still drawn");
  if (findElement(header, (element) => hasClassToken(element, "eyebrow"))) throw new Error("the band still has an eyebrow");
  await arriveFromFilteredBoard();
  await closeFromHeader();
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the New Task header: %v\n%s", err, output)
	}
}

// A task's page keeps its header and gains the same ×.
func TestHandlerClientClosesTheTaskPageFromItsHeader(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	board := &assignableBoard{task: heldTask()}
	program := assignablePageProgram(t, board, []core.Task{heldTask()}, closeCheck+`
setTimeout(async () => {
  const route = main.firstElementChild;
  const heading = findElement(route, (element) => element.id === "task-form-title");
  if (!heading || heading.textContent !== "Held task") throw new Error("the task page lost its title heading");
  if (route.getAttribute("aria-labelledby") !== "task-form-title") throw new Error("the route is not named by its heading");
  await arriveFromFilteredBoard();
  await closeFromHeader();
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page close: %v\n%s", err, output)
	}
}

// longSidebarTaskID is the task the layout tests open: it depends on five
// tasks, five depend on it, three people hold it and four links are attached,
// which is a sidebar longer than any window these tests draw it in.
const longSidebarTaskID = "WB-01J0000000000000000000GE00"

// longSidebarBoard serves a board whose task longSidebarTaskID carries title
// and a sidebar that overflows, with comments and a change log below the form.
func longSidebarBoard(t *testing.T, title string) http.Handler {
	t.Helper()
	target := clientPlacementTask(longSidebarTaskID, title, core.StatusInProgress, core.PriorityHigh)
	target.Head = "head-1"
	target.Description = "## Summary\n\nThe description of a task whose sidebar runs long.\n\n- one\n- two\n- three"
	target.Assignments = []core.Assignment{
		{Principal: boardIdentity, Label: "impl-1", Creator: boardIdentity, CreatedAt: assignmentStamp},
		{Principal: "sam@example.com", Creator: boardIdentity, CreatedAt: assignmentStamp},
		{Principal: "stranger@example.com", Creator: "stranger@example.com", CreatedAt: assignmentStamp},
	}
	for index := 1; index <= 4; index++ {
		target.Attachments = append(target.Attachments, core.Attachment{
			ID: fmt.Sprintf("att-%d", index), Author: boardIdentity, AddedAt: assignmentStamp,
			AttachmentData: core.AttachmentData{Kind: core.AttachmentLink, URL: fmt.Sprintf("https://example.com/design/%d", index), Label: fmt.Sprintf("Design note %d", index)},
		})
	}
	target.Comments = []core.Comment{{ID: "comment-1", Author: boardIdentity, Body: "A comment below the form.", CreatedAt: assignmentStamp}}
	tasks := []core.Task{}
	for index := 1; index <= 5; index++ {
		prerequisite := clientPlacementTask(fmt.Sprintf("WB-01J0000000000000000000GP%02d", index), fmt.Sprintf("Prerequisite task %d", index), core.StatusDone, core.PriorityMedium)
		target.Dependencies = append(target.Dependencies, prerequisite.ID)
		blocked := clientPlacementTask(fmt.Sprintf("WB-01J0000000000000000000GB%02d", index), fmt.Sprintf("Blocked task %d", index), core.StatusReady, core.PriorityLow)
		blocked.Dependencies = []string{longSidebarTaskID}
		tasks = append(tasks, prerequisite, blocked)
	}
	tasks = append([]core.Task{target}, tasks...)
	board := &assignableBoard{task: target}
	options := board.options(t, tasks)
	options.History = func(context.Context, string) (core.TaskDetail, error) {
		detail := historyDetail()
		detail.Task = target
		return detail, nil
	}
	return NewHandler(options)
}

// taskPageGeometry is what the page reports about a task's page: where the
// parts of the form landed with the page at its top, and what scrolling the
// sidebar to its end showed.
type taskPageGeometry struct {
	Viewport struct{ Width, Height float64 }
	Box      map[string]struct{ Top, Bottom, Height float64 }
	// TitleLines is how many lines the task's title was drawn on.
	TitleLines int
	// The sidebar as a scroller.
	SidebarScrollHeight, SidebarClientHeight, SidebarScrolledTo float64
	SidebarOverflowY                                            string
	// The sidebar's last section once the sidebar is scrolled to its end. It
	// can be taller than the sidebar, so it is its end that has to be in view.
	LastSectionAfterScroll struct{ Top, Bottom float64 }
	SidebarAfterScroll     struct{ Top, Bottom float64 }
	// main, the page's own scroller, and where the thread and the change log
	// are once main is scrolled to them.
	MainScrollHeight, MainClientHeight  float64
	CommentsReachable, HistoryReachable bool
}

// taskPageGeometryScript waits for a task's page to finish drawing its
// sidebar, then measures it.
const taskPageGeometryScript = pageGeometryHelpers + `
(async () => {
  const main = document.querySelector("main");
  const ready = () => {
    const sidebar = main.querySelector(".task-route--history .task-sidebar");
    return sidebar &&
      sidebar.querySelector('[data-task-panel="attachments"]') &&
      sidebar.querySelector('[data-task-panel="assignments"]') &&
      sidebar.querySelectorAll("[data-relationship-row]").length >= 10 &&
      main.querySelector('[data-task-panel="comments"]') &&
      main.querySelector(".task-history");
  };
  await waitFor(ready, "the task page and its sidebar");
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const route = main.firstElementChild;
  const form = route.querySelector("form.task-layout");
  const sidebar = form.querySelector(".task-sidebar");
  const heading = route.querySelector("#task-form-title");
  const parts = {
    appHeader: document.querySelector(".app-header"),
    main,
    routeHeader: route.querySelector(".task-route__header"),
    form,
    description: form.querySelector(".field--description"),
    sidebar,
    save: form.querySelector(".save-button"),
    footer: form.querySelector(".task-actions")
  };
  const boxes = {};
  for (const [name, element] of Object.entries(parts)) {
    if (!element) throw new Error("the page has no " + name);
    boxes[name] = box(element);
  }
  // The title's lines are the distinct tops of the line boxes its text sits in.
  const range = document.createRange();
  range.selectNodeContents(heading);
  const lineTops = new Set([...range.getClientRects()].map((rect) => Math.round(rect.top)));
  const scrollHeight = sidebar.scrollHeight;
  const clientHeight = sidebar.clientHeight;
  const overflowY = getComputedStyle(sidebar).overflowY;
  sidebar.scrollTop = sidebar.scrollHeight;
  const last = sidebar.lastElementChild;
  const geometry = {
    viewport: { width: innerWidth, height: innerHeight },
    box: boxes,
    titleLines: lineTops.size,
    sidebarScrollHeight: scrollHeight,
    sidebarClientHeight: clientHeight,
    sidebarScrolledTo: sidebar.scrollTop,
    sidebarOverflowY: overflowY,
    lastSectionAfterScroll: box(last),
    sidebarAfterScroll: box(sidebar),
    mainScrollHeight: main.scrollHeight,
    mainClientHeight: main.clientHeight
  };
  // Below the form, main scrolls to the thread and then to the change log.
  const reachable = (element) => {
    element.scrollIntoView({ block: "start" });
    const rect = element.getBoundingClientRect();
    return rect.top >= 0 && rect.top < innerHeight;
  };
  geometry.commentsReachable = reachable(route.querySelector('[data-task-panel="comments"]'));
  geometry.historyReachable = reachable(route.querySelector(".task-history"));
  return geometry;
})()
`

// measureTaskPage opens the long-sidebar task at one viewport.
func measureTaskPage(t *testing.T, title string, width, height int) taskPageGeometry {
	t.Helper()
	var geometry taskPageGeometry
	openInBrowser(t, longSidebarBoard(t, title), browserPage{
		Width: width, Height: height,
		Path:   "/tasks/" + longSidebarTaskID,
		Script: taskPageGeometryScript,
	}, &geometry)
	if geometry.Viewport.Width != float64(width) || geometry.Viewport.Height != float64(height) {
		t.Fatalf("the page was drawn at %vx%v, want %dx%d", geometry.Viewport.Width, geometry.Viewport.Height, width, height)
	}
	return geometry
}

// longWrappingTitle is a title long enough to wrap onto several lines at
// every width the layout tests draw.
const longWrappingTitle = "Hold the task page's form to the window it is read in, so the description and the Save row stay on the screen while a sidebar holding dependencies, assignments and attachments scrolls inside it"

// A task's page holds its form to the window the way a New Task form is held:
// the description and the Save row are on the screen, and a sidebar longer
// than the window scrolls inside the form, down to its last section. The
// thread and the change log follow underneath, and the page scrolls to them.
//
// The height comes from the layout rather than an estimate of the chrome:
// calc(100dvh - 16rem) put Save below the fold once the app header stacked or
// the title wrapped. So each viewport is measured with a one-line title and
// with one that wraps, and 820px is a width the app header stacks at.
func TestBrowserHoldsTheTaskPageFormToTheWindow(t *testing.T) {
	t.Parallel()
	for _, viewport := range []struct{ width, height int }{
		{1440, 900}, {1280, 720}, {1024, 768}, {820, 900},
	} {
		for _, title := range []struct{ name, text string }{
			{"short title", "Long sidebar task"},
			{"wrapped title", longWrappingTitle},
		} {
			t.Run(fmt.Sprintf("%dx%d %s", viewport.width, viewport.height, title.name), func(t *testing.T) {
				t.Parallel()
				geometry := measureTaskPage(t, title.text, viewport.width, viewport.height)
				if title.text == longWrappingTitle && geometry.TitleLines < 2 {
					t.Fatalf("the long title was drawn on %d line, so this case does not test a wrapped title", geometry.TitleLines)
				}
				bottom := geometry.Viewport.Height
				for _, part := range []string{"description", "save", "footer", "sidebar"} {
					got := geometry.Box[part]
					if got.Top < geometry.Box["main"].Top || got.Bottom > bottom {
						t.Errorf("the %s spans %.0f..%.0f, outside the visible %.0f..%.0f (title on %d lines, app header %.0fpx)",
							part, got.Top, got.Bottom, geometry.Box["main"].Top, bottom, geometry.TitleLines, geometry.Box["appHeader"].Height)
					}
				}
				if geometry.SidebarOverflowY != "auto" && geometry.SidebarOverflowY != "scroll" {
					t.Errorf("the sidebar's overflow-y is %q, so it is not the scroller", geometry.SidebarOverflowY)
				}
				if geometry.SidebarScrollHeight <= geometry.SidebarClientHeight {
					t.Errorf("the sidebar is %.0fpx of content in %.0fpx: it does not overflow, so this does not test a long sidebar",
						geometry.SidebarScrollHeight, geometry.SidebarClientHeight)
				}
				if geometry.SidebarScrolledTo <= 0 {
					t.Errorf("scrolling the sidebar to its end moved it %.0fpx", geometry.SidebarScrolledTo)
				}
				last, sidebar := geometry.LastSectionAfterScroll, geometry.SidebarAfterScroll
				if last.Bottom > sidebar.Bottom+1 || last.Bottom < sidebar.Top || last.Bottom > bottom {
					t.Errorf("scrolled to its end, the sidebar's last section spans %.0f..%.0f in a sidebar at %.0f..%.0f",
						last.Top, last.Bottom, sidebar.Top, sidebar.Bottom)
				}
				if geometry.MainScrollHeight <= geometry.MainClientHeight {
					t.Errorf("main does not scroll (%.0f in %.0f), so nothing follows the form", geometry.MainScrollHeight, geometry.MainClientHeight)
				}
				if !geometry.CommentsReachable || !geometry.HistoryReachable {
					t.Errorf("below the form, the comments reachable = %v and the change log reachable = %v",
						geometry.CommentsReachable, geometry.HistoryReachable)
				}
			})
		}
	}
}

// A short or a narrow window keeps the one scrolling page: the form is not
// held to a window that has no room for it, the sidebar is not a scroller of
// its own, and every part of it is reached by scrolling the page.
func TestBrowserLetsASmallWindowScrollTheTaskPageAsOnePage(t *testing.T) {
	t.Parallel()
	for _, viewport := range []struct{ width, height int }{{1280, 640}, {390, 844}} {
		t.Run(fmt.Sprintf("%dx%d", viewport.width, viewport.height), func(t *testing.T) {
			t.Parallel()
			geometry := measureTaskPage(t, longWrappingTitle, viewport.width, viewport.height)
			if geometry.SidebarScrollHeight > geometry.SidebarClientHeight+1 {
				t.Errorf("the sidebar is a %.0fpx scroller of %.0fpx of content in a window too small for one",
					geometry.SidebarClientHeight, geometry.SidebarScrollHeight)
			}
			if geometry.MainScrollHeight <= geometry.MainClientHeight {
				t.Errorf("the page does not scroll (%.0f in %.0f)", geometry.MainScrollHeight, geometry.MainClientHeight)
			}
			if !geometry.CommentsReachable || !geometry.HistoryReachable {
				t.Errorf("the comments reachable = %v and the change log reachable = %v",
					geometry.CommentsReachable, geometry.HistoryReachable)
			}
		})
	}
}
