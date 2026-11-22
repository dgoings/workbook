package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The paths below each draw somebody's typed text a way no other test reaches,
// so each is held to the board's rule for drawn text on its own: a direction
// override becomes a space, and a file name's blank-looking fillers read as
// one space. Each was found with the cleaning missing from it, or with nothing
// to show the cleaning was there.

// A board intent refused while its task's page is open re-reads the form
// against the task the board now holds, and a field the reader has not been
// near follows it. The title field is drawn text like the heading above it,
// so it follows the cleaned title, not the stored one with its override.
func TestClientTaskFormRefreshShowsTheCleanTitle(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	moved := clientPlacementTask("WB-01J0000000000000000000CT01", "Plain title", core.StatusReady, core.PriorityMedium)
	moved.Head = "head-1"
	renamed := moved
	renamed.Title = "Renamed\u202eelsewhere"
	renamed.Head = "head-2"
	tasks := []core.Task{moved}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	refreshed := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: []core.Task{renamed}, Presentation: presentationForTasks([]core.Task{renamed})})

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  const boardFetch = globalThis.fetch;
  let releaseIntent;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      return new Promise((resolve) => {
        releaseIntent = () => {
          // Somebody renamed the task: the refresh the refusal forces reads it.
          taskResponse = ` + string(refreshed) + `;
          resolve({ ok: false, json: async () => ({
            format: "workbook.error", version: 1,
            error: { category: "stale-write", message: "task has changed since head-1; reload and try again" }
          }) });
        };
      });
    }
    return boardFetch(url, options);
  };
  const card = boardCard(` + strconv.Quote(moved.ID) + `);
  card.rect = { top: 0, bottom: 80 };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });
  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(` + strconv.Quote(moved.ID) + `);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  if (!form || !title || title.value !== "Plain title") throw new Error("the task page did not open on the task: " + JSON.stringify(title && title.value));
  releaseIntent();
  await pending;
  if (findElement(main, (element) => element.tagName === "FORM") !== form) throw new Error("the refusal rebuilt the form instead of correcting it");
  if (title.value.includes("\u202e")) throw new Error("the refreshed title field draws the override: " + JSON.stringify(title.value));
  if (title.value !== "Renamed elsewhere") throw new Error("the refreshed title field holds " + JSON.stringify(title.value) + ", want the renamed title cleaned");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the refused intent's refresh: %v\n%s", err, output)
	}
}

// A row's × is named for what the row names, and a relationship row hands it
// the other task's title as stored. The name is an accessible name and a
// tooltip, both drawn text, so the button cleans it itself.
func TestClientRowRemoveButtonNamesTheRowWithoutDirectionOverrides(t *testing.T) {
	t.Parallel()
	runRelationshipRowClient(t, "the remove button's name", `
  const overridden = { ...liveTask, title: "invoice‮fdp.exe" };
  const row = relationshipRow({ id: overridden.id, task: overridden }, () => {});
  const remove = row.removeButton;
  if (!remove) throw new Error("the relationship row draws no ×");
  for (const [where, shown] of [["its accessible name", remove.getAttribute("aria-label") || ""], ["its tooltip", remove.title || ""]]) {
    if (shown.includes("‮")) throw new Error("the × carries the override in " + where + ": " + JSON.stringify(shown));
    if (!shown.endsWith("invoice fdp.exe")) throw new Error("the × names the row as " + JSON.stringify(shown) + " in " + where);
  }
`)
}

// U+180E MONGOLIAN VOWEL SEPARATOR draws as nothing at all and is no space to
// shownLine, so a run of it could hold a file's extension apart from its name
// the way the Hangul fillers could. It reads as a space in a drawn file name.
func TestClientFileNameReadsAMongolianVowelSeparatorAsASpace(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const paddedID = "01K0M6B8A4FTT8C39MXXYTWA41"
	padded := "invoice.pdf" + strings.Repeat("᠎", 40) + ".exe"
	task := threadPageTask()
	task.Attachments = append(task.Attachments, core.Attachment{
		ID: paddedID, Author: "first@example.com",
		AttachmentData: core.AttachmentData{
			Name: padded, Kind: core.AttachmentFile, Media: "application/octet-stream", Size: 2, Blob: strings.Repeat("f", 40),
		},
	})
	program := threadPageProgram(t, []core.Task{task}, `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const row = [...panelRows("attachments")].find((candidate) => candidate.dataset.attachmentId === `+strconv.Quote(paddedID)+`);
  if (!row) throw new Error("the attachment list has no row for the file");
  const link = findElement(row, (element) => element.tagName === "A");
  const shown = link.textContent;
  if (shown.includes("᠎")) throw new Error("the row draws U+180E: " + JSON.stringify(shown));
  if (!shown.includes("invoice.pdf .exe")) throw new Error("the row reads " + JSON.stringify(shown) + ", want the extension beside the name");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the separator-padded file name render: %v\n%s", err, output)
	}
}

// Picking a task in the dependency picker writes its title into the box. The
// box is drawn text, so it holds the title cleaned, as the option the reader
// picked showed it, and not the stored title with its override.
func TestClientDependencyPickerShowsThePickedTitleCleaned(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	current := threadPageTask()
	candidate := clientPlacementTask("WB-01J0000000000000000000PK01", "Pay the invoice‮fdp.exe", core.StatusReady, core.PriorityMedium)
	tasks := []core.Task{current, candidate}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/tasks/"+current.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("GET the task page status = %d", response.Code)
	}
	script := renderedClientScript(t, response.Body.String())
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: threadPagePresentation(tasks)})
	program := clientDOMHarness("/tasks/"+current.ID, string(document)) + script + threadPanelHelpers + `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const input = findElement(main, (element) => element.id === "dependency-input");
  if (!input) throw new Error("the task page draws no dependency picker");
  input.value = "invoice";
  input.eventListeners.input({ target: input });
  const option = findElement(main, (element) => element.dataset && element.dataset.candidateId === ` + strconv.Quote(candidate.ID) + `);
  if (!option) throw new Error("the picker offers no option for the candidate");
  option.eventListeners.click({ preventDefault() {} });
  if (input.value.includes("‮")) throw new Error("the picked title draws the override: " + JSON.stringify(input.value));
  if (input.value !== "Pay the invoice fdp.exe") throw new Error("the box holds " + JSON.stringify(input.value) + " after the pick");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the dependency pick: %v\n%s", err, output)
	}
}
