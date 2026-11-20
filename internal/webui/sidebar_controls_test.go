package webui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The sidebar's rows and add controls, as a reader and a screen reader meet
// them: every row's remove control is one red × named for what it removes, a
// row says no more than it needs to (no task ID, no relative age, no repeated
// URL), and the controls under the rows are a box and a short button on one
// line. The geometry is measured in sidebar_rhythm_layout_test.go; this file
// pins what the client builds.

// sidebarControlsBlockedID is a task that depends on rhythmTaskID, so the
// task's page lists it under Blocks.
const sidebarControlsBlockedID = "WB-01J0000000000000000000RH02"

// sidebarControlsTasks is the rhythm task with a labeled link besides its
// unlabeled one and a task it blocks, so every kind of row and every way of
// naming one is drawn.
func sidebarControlsTasks() []core.Task {
	target := clientPlacementTask(rhythmTaskID, "Polish the task sidebar", core.StatusInProgress, core.PriorityHigh)
	target.Head = "head-1"
	prerequisite := clientPlacementTask("WB-01J0000000000000000000RH01", rhythmPrerequisiteTitle, core.StatusReady, core.PriorityMedium)
	blocked := clientPlacementTask(sidebarControlsBlockedID, "Ship the polished sidebar", core.StatusBacklog, core.PriorityLow)
	blocked.Dependencies = []string{rhythmTaskID}
	target.Dependencies = []string{prerequisite.ID}
	target.Assignments = []core.Assignment{
		{Principal: boardIdentity, Label: "impl-1", Creator: boardIdentity, CreatedAt: assignmentStamp},
	}
	target.Attachments = []core.Attachment{
		{ID: "att-link", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://ersc.io/blog/what-comes-after-git",
		}},
		{ID: "att-labeled", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
		{ID: "att-file", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentFile, Name: "screenshot.png", Media: "image/png", Size: 48213, Blob: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	return []core.Task{target, prerequisite, blocked}
}

// sidebarControlsProgram builds a task page program on a board that can
// assign, with the panel and file helpers the other sidebar tests use.
func sidebarControlsProgram(t *testing.T, body string) string {
	t.Helper()
	tasks := sidebarControlsTasks()
	board := &assignableBoard{task: tasks[0]}
	return assignablePageProgram(t, board, tasks, threadPanelHelpers+fileHarness+`
const prerequisiteID = `+strconv.Quote(tasks[1].ID)+`;
const prerequisiteTitle = `+strconv.Quote(tasks[1].Title)+`;
const blockedID = `+strconv.Quote(sidebarControlsBlockedID)+`;
const blockedTitle = `+strconv.Quote(tasks[2].Title)+`;
function sidebar() {
  const aside = findElement(main, (element) => hasClassToken(element, "task-sidebar"));
  if (!aside) throw new Error("the task page draws no sidebar");
  return aside;
}
function removeButtons(root) {
  return findElements(root, (element) => hasDataKey(element, "rowRemove"));
}
function dependencyRow(id) {
  const row = findElement(sidebar(), (element) => hasDataKey(element, "relationshipRow") && element.dataset.relationshipId === id);
  if (!row) throw new Error("the sidebar lists no dependency row for " + id);
  return row;
}
function attachmentRow(id) {
  const row = findElement(panelSection("attachments"), (element) => element.dataset.attachmentId === id);
  if (!row) throw new Error("the attachments panel has no row for " + id);
  return row;
}
// Every × the same control: a real button, the shared class, a name and a
// matching tooltip, and nothing that takes it out of the tab order.
function assertRemoveButton(button, name) {
  if (!button) throw new Error("no remove button named " + JSON.stringify(name));
  if (button.tagName !== "BUTTON" || button.type !== "button") throw new Error(name + " is not a plain button");
  if (!hasClassToken(button, "row-remove")) throw new Error(name + " is not the shared red ×");
  if (button.textContent !== "×") throw new Error(name + " reads " + JSON.stringify(button.textContent));
  if (button.getAttribute("aria-label") !== name) {
    throw new Error("a remove button is named " + JSON.stringify(button.getAttribute("aria-label")) + ", want " + JSON.stringify(name));
  }
  if (button.title !== name) throw new Error(name + "'s tooltip reads " + JSON.stringify(button.title));
  if (button.getAttribute("tabindex") === "-1") throw new Error(name + " is out of the tab order");
  if (!button.eventListeners.click) throw new Error(name + " does nothing when pressed");
}
`+body)
}

// Every × in the sidebar is named for what it removes, and each one is the
// same keyboard-reachable button.
func TestHandlerClientNamesEverySidebarRemoveButton(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := sidebarControlsProgram(t, `
setTimeout(() => {
  const names = removeButtons(sidebar()).map((button) => button.getAttribute("aria-label")).sort();
  const want = [
    "Remove Design doc",
    "Remove blocked task " + blockedTitle,
    "Remove dependency on " + prerequisiteTitle,
    "Remove https://ersc.io/blog/what-comes-after-git",
    "Remove screenshot.png",
    "Unassign dylan@example.com/impl-1"
  ].sort();
  if (JSON.stringify(names) !== JSON.stringify(want)) {
    throw new Error("the sidebar's remove buttons are named " + JSON.stringify(names) + ", want " + JSON.stringify(want));
  }
  for (const button of removeButtons(sidebar())) assertRemoveButton(button, button.getAttribute("aria-label"));
  // No row keeps a text "Remove" or "Unassign" button beside its ×.
  const worded = findElements(sidebar(), (element) => element.tagName === "BUTTON" &&
    (element.textContent === "Remove" || element.textContent === "Unassign"));
  if (worded.length) throw new Error("the sidebar still draws worded remove buttons: " + worded.length);

  // A chosen file's × is the same control, named for the file.
  const chooser = findElement(panelSection("attachments"), (element) => element.id === "attachment-file");
  chooseFiles(chooser, [new TestFile("notes.txt", 5, "hello")]);
  const pending = findElement(panelSection("attachments"), (element) => hasDataKey(element, "attachmentPending"));
  const pendingRemoves = removeButtons(pending);
  if (pendingRemoves.length !== 1) throw new Error("a chosen file drew " + pendingRemoves.length + " remove buttons");
  assertRemoveButton(pendingRemoves[0], "Remove notes.txt");

  // The add buttons read one word and are named for what they add.
  const add = findElement(sidebar(), (element) => hasDataKey(element, "relationshipAdd"));
  if (!add || add.textContent !== "Add" || add.getAttribute("aria-label") !== "Add dependency") {
    throw new Error("the dependency picker's button reads " + JSON.stringify(add && add.textContent) + " named " + JSON.stringify(add && add.getAttribute("aria-label")));
  }
  const attach = findElement(sidebar(), (element) => hasDataKey(element, "attachmentAddLink"));
  if (!attach || attach.textContent !== "Attach" || attach.getAttribute("aria-label") !== "Attach link" || attach.type !== "submit") {
    throw new Error("the link form's button reads " + JSON.stringify(attach && attach.textContent) + " named " + JSON.stringify(attach && attach.getAttribute("aria-label")));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the remove button names: %v\n%s", err, output)
	}
}

// Each × does its row's removal when a keyboard activates it — which is a
// click on a focused button — and sends the request that removes that row.
func TestHandlerClientRemovesRowsFromTheirKeyboardActivatedButtons(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := sidebarControlsProgram(t, `
setTimeout(async () => {
  const sent = [];
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") === "GET") return { ok: true, json: async () => taskDocument };
    sent.push({ url: String(url), method: options.method, body: options.body ? JSON.parse(options.body) : null });
    return { ok: false, status: 500, json: async () => ({ error: "refused in this test" }) };
  };
  const press = async (button) => {
    button.focus();
    if (document.activeElement !== button) throw new Error("the × could not take focus");
    button.click();
    await new Promise((resolve) => setTimeout(resolve, 0));
  };
  await press(findElement(dependencyRow(prerequisiteID), (element) => hasDataKey(element, "rowRemove")));
  await press(findElement(assignmentRows()[0], (element) => hasDataKey(element, "rowRemove")));
  await press(findElement(attachmentRow("att-file"), (element) => hasDataKey(element, "rowRemove")));
  const methods = sent.map((request) => request.method + " " + request.url);
  const wantDependency = "/api/tasks/" + encodeURIComponent(`+strconv.Quote(rhythmTaskID)+`) + "/dependencies/" + encodeURIComponent(prerequisiteID);
  if (!methods.some((line) => line.startsWith("DELETE ") && line.includes("/dependencies"))) {
    throw new Error("the dependency × sent no removal: " + JSON.stringify(methods) + " (expected one like " + wantDependency + ")");
  }
  const unassign = sent.find((request) => request.method === "DELETE" && request.url.endsWith("/assignments"));
  if (!unassign || unassign.body.from !== "dylan@example.com/impl-1") {
    throw new Error("the Unassign × sent " + JSON.stringify(unassign));
  }
  if (!methods.includes("DELETE /api/tasks/" + encodeURIComponent(`+strconv.Quote(rhythmTaskID)+`) + "/attachments/att-file")) {
    throw new Error("the attachment × sent no removal: " + JSON.stringify(methods));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the keyboard removals: %v\n%s", err, output)
	}
}

// A row says what a reader scanning it needs and no more: a dependency gives
// its status and priority but not its ID, which is the title's tooltip; an
// assignment gives when it was recorded and not how long ago; a link's URL is
// its name's tooltip and not a second time in its metadata.
func TestHandlerClientKeepsSidebarRowsToWhatTheyNeedToSay(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := sidebarControlsProgram(t, `
setTimeout(() => {
  for (const [id, title] of [[prerequisiteID, prerequisiteTitle], [blockedID, blockedTitle]]) {
    const row = dependencyRow(id);
    const metadata = findElement(row, (element) => hasClassToken(element, "relationship-row__metadata"));
    if (!metadata || row.textContent.includes(id)) {
      throw new Error("the dependency row for " + id + " prints its ID: " + JSON.stringify(row.textContent));
    }
    if (!/^[a-z-]+ · [a-z-]+$/.test(metadata.textContent)) {
      throw new Error("the dependency row's metadata is not its status and priority: " + JSON.stringify(metadata.textContent));
    }
    const link = findElement(row, (element) => element.tagName === "A");
    if (!link || link.textContent !== title || link.title !== id) {
      throw new Error("the dependency title does not carry its ID as a tooltip: " + JSON.stringify(link && link.title));
    }
  }

  const assignment = assignmentRows()[0];
  if (/ago\b|just now/.test(assignment.textContent)) {
    throw new Error("the assignment row words a relative age: " + JSON.stringify(assignment.textContent));
  }

  for (const [id, name, url] of [
    ["att-link", "https://ersc.io/blog/what-comes-after-git", "https://ersc.io/blog/what-comes-after-git"],
    ["att-labeled", "Design doc", "https://example.test/design"]
  ]) {
    const row = attachmentRow(id);
    const link = findElement(row, (element) => element.tagName === "A");
    if (!link || link.textContent !== name || link.title !== url) {
      throw new Error(id + " is named " + JSON.stringify(link && link.textContent) + " with tooltip " + JSON.stringify(link && link.title));
    }
    const meta = findElement(row, (element) => hasClassToken(element, "attachment__meta"));
    if (meta.textContent.includes(url) || meta.title.includes(url)) {
      throw new Error(id + "'s metadata repeats its URL: " + JSON.stringify(meta.textContent));
    }
    if (!meta.textContent.startsWith("link · ")) throw new Error(id + "'s metadata reads " + JSON.stringify(meta.textContent));
  }
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the row contents: %v\n%s", err, output)
	}
}

// The assignment form is a caption and a box with Assign on its line, with no
// paragraph of help under it: the placeholder names the identity a blank box
// assigns, on the task page and on a New Task form alike.
func TestHandlerClientDrawsTheAssignFormWithoutHelperText(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	check := `
  const form = findElement(assignmentPanel(), (element) => hasDataKey(element, "assignmentForm"));
  if (!form) throw new Error("no assignment form");
  if (form.textContent.includes("Leave blank") || form.textContent.includes("/label") ||
      findElement(form, (element) => hasClassToken(element, "assignment-add__hint"))) {
    throw new Error("the assignment form still carries its helper text: " + JSON.stringify(form.textContent));
  }
  const field = findElement(form, (element) => element.id === "assignment-to");
  if (!field || field.placeholder !== ` + strconv.Quote(boardIdentity) + `) {
    throw new Error("the field does not show the identity a blank box assigns: " + JSON.stringify(field && field.placeholder));
  }
  if (field.getAttribute("aria-describedby")) throw new Error("the field still points at a description");
  const line = field.parentElement;
  const submit = findElement(form, (element) => element.tagName === "BUTTON" && element.type === "submit");
  if (!hasClassToken(line, "sidebar-inline") || !submit || submit.parentElement !== line || submit.textContent !== "Assign") {
    throw new Error("Assign does not share the box's line");
  }
`
	pageProgram := sidebarControlsProgram(t, `
function assignmentPanel() { return assignmentsPanel(); }
setTimeout(() => {`+check+`}, 0);
`)
	if output, err := nodeCommand(node, pageProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page's assign form: %v\n%s", err, output)
	}
	formProgram := createAssignmentProgram(t, `
(async () => {
  await openCreateForm();`+check+`
})().catch((error) => { console.error(error); process.exit(1); });
`)
	if output, err := nodeCommand(node, formProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the New Task form's assign form: %v\n%s", err, output)
	}
}

// The link form puts Attach on the URL's line and gives the optional label
// the line under them, in that order in the document as on screen, on the
// task page and on a New Task form alike.
func TestHandlerClientPutsAttachOnTheLinkURLsLine(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	check := `
  const form = findElement(panelSection("attachments"), (element) => hasDataKey(element, "attachmentLinkForm"));
  if (!form) throw new Error("no link form");
  const url = findElement(form, (element) => element.id === "attachment-url");
  const label = findElement(form, (element) => element.id === "attachment-label");
  const attach = findElement(form, (element) => hasDataKey(element, "attachmentAddLink"));
  if (!url || !label || !attach) throw new Error("the link form is missing its URL, label or Attach");
  const line = url.parentElement;
  if (!hasClassToken(line, "sidebar-inline") || line.parentElement !== form) {
    throw new Error("the URL is not on an inline line of its own form");
  }
  if (line.children.length !== 2 || line.children[0] !== url || line.children[1] !== attach) {
    throw new Error("the URL's line holds " + line.children.map((child) => child.id || child.textContent).join(", ") + ", want the URL then Attach");
  }
  if (label.parentElement !== form) throw new Error("the optional label does not have a line of its own");
  const order = form.children;
  if (order.indexOf(line) < 0 || order.indexOf(label) !== order.indexOf(line) + 1) {
    throw new Error("the optional label does not follow the URL's line");
  }
  if (attach.type !== "submit") throw new Error("Attach does not submit the form Enter submits");
`
	pageProgram := sidebarControlsProgram(t, `
setTimeout(() => {`+check+`}, 0);
`)
	if output, err := nodeCommand(node, pageProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page's link form: %v\n%s", err, output)
	}
	formProgram := createAssignmentProgram(t, `
(async () => {
  await openCreateForm();`+check+`
})().catch((error) => { console.error(error); process.exit(1); });
`)
	if output, err := nodeCommand(node, formProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the New Task form's link form: %v\n%s", err, output)
	}
}

// Enter in either box of the link form attaches, pressed as a real key in a
// real browser: the browser's implicit submission is what runs, so the
// button's new place must not have taken it away from the label box.
func TestBrowserAttachesALinkOnEnterInEitherBox(t *testing.T) {
	t.Parallel()
	for _, box := range []string{"attachment-url", "attachment-label"} {
		t.Run(box, func(t *testing.T) {
			t.Parallel()
			var sent []string
			openInBrowser(t, rhythmBoard(t), browserPage{
				Width: 1280, Height: 800,
				Path: "/tasks/" + rhythmTaskID,
				Setup: pageGeometryHelpers + rhythmSidebarReady + `
(async () => {
  await waitFor(sidebarReady, "the sidebar");
  window.sentWrites = [];
  const realFetch = window.fetch;
  window.fetch = (url, options = {}) => {
    const method = options.method || "GET";
    if (method !== "GET") {
      window.sentWrites.push(method + " " + new URL(String(url), location.href).pathname + " " + (options.body || ""));
      return new Promise(() => {});
    }
    return realFetch(url, options);
  };
  document.querySelector("#attachment-url").value = "https://example.test/spec";
  document.querySelector("#attachment-label").value = "Spec";
  document.querySelector("#` + box + `").focus();
})()`,
				Keys:   []string{"Enter"},
				Script: `new Promise((resolve) => setTimeout(() => resolve(window.sentWrites), 100))`,
			}, &sent)
			found := false
			for _, line := range sent {
				if strings.HasPrefix(line, "POST ") && strings.Contains(line, "/attachments") &&
					strings.Contains(line, "https://example.test/spec") && strings.Contains(line, "Spec") {
					found = true
				}
			}
			if !found {
				encoded, _ := json.Marshal(sent)
				t.Fatalf("Enter in #%s sent %s, want a POST of the link to /attachments", box, encoded)
			}
		})
	}
}

// The file chooser is a button-styled label for the real file input: a press
// on it opens that input's picker, which a label does for the control it
// names, and the input itself stays the focusable, named control. Choosing
// hands the line over to the pending list, as before.
func TestHandlerClientDrawsTheFileChooserAsAButtonForTheRealInput(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	check := `
  const panel = panelSection("attachments");
  const form = attachmentForm("file");
  const input = findElement(form, (element) => element.id === "attachment-file");
  const choose = findElement(form, (element) => hasDataKey(element, "attachmentChoose"));
  if (!input || input.tagName !== "INPUT" || input.type !== "file") throw new Error("the form has no real file input");
  if (input.hidden || input.getAttribute("tabindex") === "-1" || input.getAttribute("aria-hidden")) {
    throw new Error("the file input was taken out of reach rather than drawn as nothing");
  }
  if (!choose || choose.tagName !== "LABEL" || choose.htmlFor !== input.id) {
    throw new Error("the chooser is not a label for the real input, so a press would not open it");
  }
  if (!hasClassToken(choose, "thread-button") || choose.textContent !== wantChoose) {
    throw new Error("the chooser is not a secondary button reading " + JSON.stringify(wantChoose) + ": " + JSON.stringify(choose.textContent));
  }
  if (Boolean(input.multiple) !== wantMultiple) throw new Error("the input's multiple selection changed: " + input.multiple);
  const caption = findElements(form, (element) => element.tagName === "LABEL" && (element.htmlFor || element.getAttribute("for")) === input.id && element !== choose);
  if (caption.length !== 1 || caption[0].textContent !== "Attach a file") throw new Error("the input lost its caption");
  chooseFiles(input, [new TestFile("notes.txt", 5, "hello")]);
  if (!choose.hidden || !input.hidden) throw new Error("a choice left the chooser drawn beside the pending list");
  const remove = findElement(panel, (element) => hasDataKey(element, "rowRemove") && element.getAttribute("aria-label") === "Remove notes.txt");
  remove.click();
  if (choose.hidden || input.hidden) throw new Error("taking the last file back out did not bring the chooser back");
  if (document.activeElement !== input) throw new Error("taking the last file back out left the caret off the chooser");
`
	pageProgram := sidebarControlsProgram(t, `
const wantChoose = "Choose file";
const wantMultiple = false;
setTimeout(() => {`+check+`}, 0);
`)
	if output, err := nodeCommand(node, pageProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the task page's chooser: %v\n%s", err, output)
	}
	formProgram := createAttachmentProgram(t, `
const wantChoose = "Choose files";
const wantMultiple = true;
(async () => {
  await openCreateForm();`+check+`
})().catch((error) => { console.error(error); process.exit(1); });
`)
	if output, err := nodeCommand(node, formProgram).CombinedOutput(); err != nil {
		t.Fatalf("execute the New Task form's chooser: %v\n%s", err, output)
	}
}

// The staged rows on a New Task form name their × the same way, and a staged
// link's URL is its tooltip rather than its metadata.
func TestHandlerClientNamesTheStagedRowsRemoveButtons(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := createAttachmentProgram(t, `
(async () => {
  await openCreateForm();
  await stageLink("https://example.test/design", "Design doc");
  await stageLink("https://example.test/plain");
  await stageFiles([new TestFile("notes.txt", 5, "hello")]);
  const names = stagedRows().map((row) => {
    const button = findElement(row, (element) => hasDataKey(element, "rowRemove"));
    return button && button.getAttribute("aria-label") + "|" + button.title;
  });
  const want = ["Remove Design doc|Remove Design doc", "Remove https://example.test/plain|Remove https://example.test/plain", "Remove notes.txt|Remove notes.txt"];
  if (JSON.stringify(names) !== JSON.stringify(want)) throw new Error("staged rows' × are named " + JSON.stringify(names));
  const first = stagedRows()[0];
  if (first.children[0].title !== "https://example.test/design" || first.children[1].textContent !== "link") {
    throw new Error("a staged link reads " + JSON.stringify(first.children[1].textContent) + " with tooltip " + JSON.stringify(first.children[0].title));
  }
})().catch((error) => { console.error(error); process.exit(1); });
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the staged names: %v\n%s", err, output)
	}
}

// A real keyboard, in a real browser: Tab-reachable and activated by Enter and
// by Space, each × sends its row's removal.
func TestBrowserActivatesTheRemoveButtonsFromTheKeyboard(t *testing.T) {
	t.Parallel()
	for _, press := range []struct{ name, selector, key, want string }{
		{"dependency by Enter", `[data-relationship-row] [data-row-remove]`, "Enter", "DELETE /dependencies"},
		{"assignment by Space", `[data-task-panel="assignments"] [data-row-remove]`, "Space", "DELETE /assignments"},
		{"attachment by Enter", `[data-attachment-id="att-file"] [data-row-remove]`, "Enter", "DELETE /attachments/att-file"},
	} {
		t.Run(press.name, func(t *testing.T) {
			t.Parallel()
			var sent []string
			var focused string
			openInBrowser(t, rhythmBoard(t), browserPage{
				Width: 1280, Height: 800,
				Path: "/tasks/" + rhythmTaskID,
				Setup: pageGeometryHelpers + rhythmSidebarReady + `
(async () => {
  await waitFor(sidebarReady, "the sidebar");
  window.sentWrites = [];
  const realFetch = window.fetch;
  window.fetch = (url, options = {}) => {
    const method = options.method || "GET";
    if (method !== "GET") {
      window.sentWrites.push(method + " " + new URL(String(url), location.href).pathname);
      return new Promise(() => {});
    }
    return realFetch(url, options);
  };
  const button = document.querySelector(` + strconv.Quote(press.selector) + `);
  // Reach it the way a keyboard does: from the control before it, by Tab.
  const focusable = [...document.querySelectorAll("a[href], button, input, select, textarea")].filter((element) => !element.disabled && element.offsetParent !== null);
  focusable[focusable.indexOf(button) - 1].focus();
})()`,
				Keys:   []string{"Tab", press.key},
				Script: `new Promise((resolve) => setTimeout(() => resolve({ focused: document.activeElement && document.activeElement.getAttribute("aria-label"), sent: window.sentWrites }), 100))`,
			}, &struct {
				Sent    *[]string
				Focused *string
			}{&sent, &focused})
			wantMethod, wantPath, _ := strings.Cut(press.want, " ")
			found := false
			for _, line := range sent {
				method, path, _ := strings.Cut(line, " ")
				if method == wantMethod && strings.Contains(path, wantPath) {
					found = true
				}
			}
			if !found {
				encoded, _ := json.Marshal(sent)
				t.Fatalf("pressing %s on the focused × (%q) sent %s, want a %s", press.key, focused, encoded, press.want)
			}
		})
	}
}

// A focused × keeps the caret across the board's polls. The page polls every
// second, and a list rebuilt on each poll with nothing changed destroys the
// button under a keyboard reader's caret before they can press it: the
// dependency list did exactly that until it learned to skip an unchanged
// redraw, as the assignments section already did.
func TestBrowserKeepsAFocusedRemoveButtonAcrossPolls(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ name, selector string }{
		{"dependency", `[data-relationship-row] [data-row-remove]`},
		{"assignment", `[data-task-panel="assignments"] [data-row-remove]`},
		{"attachment", `[data-attachment-id="att-file"] [data-row-remove]`},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			var kept struct {
				Focused, Connected bool
				Active             string
			}
			openInBrowser(t, rhythmBoard(t), browserPage{
				Width: 1280, Height: 800,
				Path: "/tasks/" + rhythmTaskID,
				Script: pageGeometryHelpers + rhythmSidebarReady + `
(async () => {
  await waitFor(sidebarReady, "the sidebar");
  const button = document.querySelector(` + strconv.Quote(row.selector) + `);
  button.focus();
  if (document.activeElement !== button) throw new Error("the × could not take focus");
  // Three polls' worth.
  await new Promise((resolve) => setTimeout(resolve, 3200));
  const active = document.activeElement;
  return { focused: active === button, connected: button.isConnected, active: active.tagName + " " + (active.getAttribute("aria-label") || "") };
})()`,
			}, &kept)
			if !kept.Focused || !kept.Connected {
				t.Fatalf("after three idle polls the focused × was replaced (still in the page: %v); focus is on %q", kept.Connected, kept.Active)
			}
		})
	}
}

// Two rules the sidebar's controls depend on that only a layout engine can
// see. The file chooser is a label drawn as a button over a clipped file
// input, so the input — which is what Tab reaches — draws no focus ring of
// its own on screen: the label has to draw it. And the labels editor's
// status line is a live region that stands empty almost always, so in the
// sidebar it takes no room until it has something to say.
func TestBrowserDrawsTheChooserFocusRingAndCollapsesTheEmptyLabelStatus(t *testing.T) {
	t.Parallel()
	var drawn struct {
		Focused                    string
		OutlineStyle, OutlineWidth string
		StatusHeight               float64
		StatusText                 string
	}
	openInBrowser(t, rhythmBoard(t), browserPage{
		Width: 1280, Height: 800,
		Path: "/tasks/" + rhythmTaskID,
		Setup: `
(async () => {
` + pageGeometryHelpers + rhythmSidebarReady + `
  await waitFor(sidebarReady, "the sidebar");
  // Reach the file input the way a keyboard does: from the control before
  // it, by Tab, so the browser decides it is a keyboard focus.
  const file = document.querySelector("#attachment-file");
  const focusable = [...document.querySelectorAll("a[href], button, input, select, textarea")]
    .filter((element) => !element.disabled && (element.offsetParent !== null || element === file));
  focusable[focusable.indexOf(file) - 1].focus();
})()`,
		Keys: []string{"Tab"},
		Script: `
(async () => {
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const choose = document.querySelector("[data-attachment-choose]");
  const ring = getComputedStyle(choose);
  const status = document.querySelector(".task-sidebar .label-editor__status");
  if (!status) throw new Error("the sidebar's labels editor has no status line");
  return {
    focused: document.activeElement.id,
    outlineStyle: ring.outlineStyle,
    outlineWidth: ring.outlineWidth,
    statusHeight: status.getBoundingClientRect().height,
    statusText: status.textContent
  };
})()`,
	}, &drawn)
	if drawn.Focused != "attachment-file" {
		t.Fatalf("Tab landed on %q, not the file input", drawn.Focused)
	}
	if drawn.OutlineStyle != "solid" || drawn.OutlineWidth != "3px" {
		t.Errorf("with the file input focused, Choose file draws outline %s %s, want a solid 3px ring", drawn.OutlineStyle, drawn.OutlineWidth)
	}
	if drawn.StatusText != "" {
		t.Fatalf("the labels editor's status line says %q, so its empty height cannot be measured", drawn.StatusText)
	}
	if drawn.StatusHeight != 0 {
		t.Errorf("the empty labels status line is %.1fpx tall in the sidebar, want 0", drawn.StatusHeight)
	}
}
