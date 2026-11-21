package webui

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The rest of what the board draws from text somebody typed: the browser tab's
// title on every route, the fields a reader sees before choosing to edit
// anything, the name a download is saved under, file names padded out with
// blank-looking characters, the alternative text of an attachment image, a
// card's accessible name, and the directional marks honest right-to-left text
// is written with. attachment_name_display_test.go holds the first round.

// spoofedBoardHarness is a project whose name somebody set, through
// PATCH /api/display, to carry U+202E RIGHT-TO-LEFT OVERRIDE. The page carries
// the name in three attributes and the client titles every route from them.
const spoofedBoardHarness = `
boardView.dataset.projectName = "Atlas\u202efdp.exe";
boardView.dataset.titleSuffix = "Board\u202efdp.exe";
boardView.dataset.defaultProjectName = "Workbook\u202ebrd";
`

// Every route's browser tab title is built from the project's name, on load and
// after a navigation inside the page, and none of them may carry the override
// into the tab — where it would read "Configuration · Boardexe.pdf".
func TestClientTabTitlesDrawTheProjectNameWithoutDirectionOverrides(t *testing.T) {
	t.Parallel()
	vocabulary := handlerVocabulary(t)
	const missing = "WB-01J0000000000000000000NOPE"
	tasks := []core.Task{clientPlacementTask("WB-01J0000000000000000000TT01", "Held", "queued", core.PriorityMedium)}
	checks := `
const overrides = /[\u202a-\u202e\u2066-\u2069]/u;
function titled(where, want) {
  const shown = String(document.title);
  if (overrides.test(shown)) throw new Error(where + " titles the tab " + JSON.stringify(shown) + ", which carries a direction override");
  if (shown !== want) throw new Error(where + " titles the tab " + JSON.stringify(shown) + ", want " + JSON.stringify(want));
}
async function walkTo(path) {
  const link = new TestElement("a");
  link.href = window.location.origin + path;
  await follow(link);
}
`
	runClientOverHandler(t, configurableHandler(vocabulary, "head-1", tasks),
		"titling routes after in-page navigation", "/", spoofedBoardHarness, vocabulary, "head-1", tasks, checks+`
  vocabularyRead = `+configuredVocabularyJSON(t, vocabulary, "head-1")+`;
  await settle();
  titled("the board", "Atlas fdp.exe");
  await openStatuses();
  titled("the configuration page", "Configuration · Board fdp.exe");
  // The settings form's placeholder is the name a cleared field would give
  // the board, and it is drawn text too.
  const placeholder = String(displayField("name").placeholder);
  if (placeholder !== "Workbook brd") throw new Error("the name field's placeholder reads " + JSON.stringify(placeholder));
  await walkTo("/tasks/new");
  titled("the New Task page", "New task · Board fdp.exe");
  await walkTo("/tasks/`+missing+`");
  titled("a task this board does not hold", "Task not found · Board fdp.exe");
  await walkTo("/");
  titled("the board, returned to", "Atlas fdp.exe");
`)
	for _, route := range []struct{ path, want string }{
		{path: "/config", want: "Configuration · Board fdp.exe"},
		{path: "/tasks/new", want: "New task · Board fdp.exe"},
		{path: "/tasks/" + missing, want: "Task not found · Board fdp.exe"},
	} {
		runClientOverHandler(t, configurableHandler(vocabulary, "head-1", tasks),
			"titling "+route.path+" on load", route.path, spoofedBoardHarness, vocabulary, "head-1", tasks, checks+`
  vocabularyRead = `+configuredVocabularyJSON(t, vocabulary, "head-1")+`;
  await settle();
  await settle();
  titled(`+strconv.Quote(route.path+" on load")+`, `+strconv.Quote(route.want)+`);
`)
	}
}

// A task's Title field is part of the page a reader reads before touching
// anything, so it shows the title the way the heading above it does. And
// because it does, a save of some other field must not send the field's text
// back: the stored title, override and all, stays byte for byte what it was.
func TestClientTaskTitleFieldShowsTheCleanTitleAndASaveLeavesTheStoredOneAlone(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const stored = "Pay the invoice\u202efdp.exe"
	task := threadPageTask()
	task.Title = stored
	task.Description = "Original."
	program := threadPageProgram(t, []core.Task{task}, `
const sent = [];
const boardFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if ((options.method || "GET") !== "GET") {
    sent.push({ url, method: options.method, body: JSON.parse(options.body) });
    return { ok: true, json: async () => ({ format: "workbook.task-mutation", version: 1, task: taskDocument.tasks[0] }) };
  }
  return boardFetch(url, options);
};
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const title = findElement(main, (element) => element.id === "task-title");
  if (!title) throw new Error("the task page draws no title field");
  if (title.value !== "Pay the invoice fdp.exe") throw new Error("the title field holds " + JSON.stringify(title.value));
  const form = findElement(main, (element) => element.tagName === "FORM");
  const description = findElement(main, (element) => element.id === "task-description");
  description.value = "Edited, and nothing else.";
  await form.eventListeners.submit({ preventDefault() {} });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const saves = sent.filter((call) => call.method === "PATCH");
  if (saves.length !== 1) throw new Error("the save sent " + JSON.stringify(sent));
  console.log(JSON.stringify(saves[0]));
}, 0);
`)
	output, err := nodeCommand(node, program).Output()
	if err != nil {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("execute the title field render: %v\n%s%s", err, output, stderr)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var save struct {
		URL  string          `json:"url"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &save); err != nil {
		t.Fatalf("read the save the page sent: %v\n%s", err, output)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(save.Body, &members); err != nil {
		t.Fatal(err)
	}
	if _, named := members["title"]; named {
		t.Fatalf("a description save sent the title: %s", save.Body)
	}

	// The same body, through the board's own update route, onto the stored
	// task: what is recorded is what the route hands the core, applied.
	held := task
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return []core.Task{held}, nil },
		Update: func(_ context.Context, _ string, input core.UpdateInput) (core.MutationResult, error) {
			if input.Title != nil {
				held.Title = *input.Title
			}
			if input.Description != nil {
				held.Description = *input.Description
			}
			return core.MutationResult{Task: held}, nil
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, save.URL, string(save.Body))
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH %s status = %d; body = %s", save.URL, response.Code, response.Body.String())
	}
	if held.Description != "Edited, and nothing else." {
		t.Fatalf("the save did not land: description = %q", held.Description)
	}
	if held.Title != stored {
		t.Fatalf("stored title = %q after a description save, want it byte for byte %q", held.Title, stored)
	}
}

// The configuration page's project name is a field drawn without an edit mode,
// and the status and priority label fields are one-line fields too. Each shows
// the cleaned text, and a save that changed something else sends the stored
// text back exactly — or, for the vocabularies, does not send the label at all.
func TestClientConfigFieldsShowCleanTextAndSaveTheStoredText(t *testing.T) {
	t.Parallel()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Ice\u202ebox", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "queued", Label: "Queued Up", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	state := VocabularyState{Vocabulary: vocabulary, Head: "head-1", Display: core.DisplaySettings{Name: "Atlas\u202efdp.exe", PrimaryColor: "#1a7f4b"}}
	read := string(mustJSON(t, vocabularyDocument(state)))
	saved := state
	saved.Head = "head-2"
	saved.Display.PrimaryColor = "#123abc"
	runClientOverHandler(t, configurableHandler(vocabulary, "head-1", nil),
		"saving the board settings and a status", "/", "", vocabulary, "head-1", nil, `
  vocabularyRead = `+read+`;
  await openStatuses();
  const name = displayField("name");
  if (name.value !== "Atlas fdp.exe") throw new Error("the project name field holds " + JSON.stringify(name.value));
  displayField("primaryColor").value = "#123abc";
  displayAnswer = { body: `+displayMutationJSON(t, saved)+` };
  await saveDisplay();
  if (displayCalls.length !== 1) throw new Error("the settings save sent " + displayCalls.length + " requests");
  if (displayCalls[0].body.name !== "Atlas\u202efdp.exe") {
    throw new Error("a color save sent the name " + JSON.stringify(displayCalls[0].body.name) + ", not the stored one");
  }

  panelControl(panelRow("icebox"), "Edit Ice box").eventListeners.click();
  const form = panelForm("icebox", "vocabularyEdit");
  const label = findElement(form, (element) => element.id === "status-label-icebox");
  if (label.value !== "Ice box") throw new Error("the status label field holds " + JSON.stringify(label.value));
  findElement(form, (element) => element.id === "status-name-icebox").value = "cold";
  vocabularyAnswer = { body: `+panelMutationJSON(t, vocabulary, "head-3", VocabularyTaskCounts{}, nil)+` };
  await submitPanelForm(form);
  const change = vocabularyCalls.filter((call) => call.method !== "GET")[0];
  if (!change) throw new Error("the status rename was not sent");
  if ("label" in change.body) throw new Error("a rename sent the label " + JSON.stringify(change.body.label));
  if (change.body.name !== "cold") throw new Error("the rename sent " + JSON.stringify(change.body));
`)

	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop\u202eeverything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	base := handlerVocabulary(t)
	runPriorityPanelClient(t, "renaming a priority", base, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, base, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, base, priorities, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();
  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  const label = findElement(form, (element) => element.id === "priority-label-urgent");
  if (label.value !== "Drop everything") throw new Error("the priority label field holds " + JSON.stringify(label.value));
  findElement(form, (element) => element.id === "priority-name-urgent").value = "critical";
  await submitPriorityForm(form);
  if (priorityCalls.length !== 1) throw new Error("the Save sent " + JSON.stringify(priorityCalls.map((call) => call.method + " " + call.url)));
  if ("label" in priorityCalls[0].body) throw new Error("a rename sent the label " + JSON.stringify(priorityCalls[0].body.label));
`)
}

// The name a download is saved under is the server's to clean: it is drawn by
// the browser's download bar and by whatever file manager the file lands in,
// and none of them should be trusted to drop U+202E on the board's behalf. Both
// the plain and the RFC 2231 extended form carry an underscore in its place.
func TestAttachmentDownloadSendsAFileNameWithoutDirectionControls(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, header string }{
		{name: "invoice\u202efdp.exe", header: "attachment; filename=invoice_fdp.exe"},
		{name: "a\x07b.txt", header: "attachment; filename=a_b.txt"},
		{name: "fa\u00e7ture\u2067fdp.exe", header: "attachment; filename*=utf-8''fa%C3%A7ture_fdp.exe"},
		// A directional mark cannot reverse a run and is kept.
		{name: "\u05d0\u200f.txt", header: "attachment; filename*=utf-8''%D7%90%E2%80%8F.txt"},
	} {
		var calls threadRouteCalls
		handler := attachmentHandler(t, &calls, core.Attachment{ID: threadRouteAttachOne, AttachmentData: core.AttachmentData{
			Name: test.name, Kind: core.AttachmentFile, Media: "text/plain", Size: 11, Blob: strings.Repeat("a", 40),
		}})
		response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne)
		got := response.Result().Header.Get("Content-Disposition")
		if got != test.header {
			t.Errorf("name %q: Content-Disposition = %q, want %q", test.name, got, test.header)
		}
		if strings.Contains(got, "%E2%80%AE") || strings.ContainsRune(got, '\u202e') {
			t.Errorf("name %q: Content-Disposition %q still carries the override", test.name, got)
		}
		if _, _, err := mime.ParseMediaType(got); err != nil {
			t.Errorf("name %q: Content-Disposition %q does not parse: %v", test.name, got, err)
		}
	}
}

// U+200E, U+200F and U+061C are marks, not overrides: an invisible strong
// letter that decides which side of a Hebrew or Arabic word its punctuation
// sits on. They cannot reverse a run of letters, so the board draws them as
// written — in the served page and in everything the script draws — and keeps
// replacing the overrides, embeddings and isolates.
func TestBoardKeepsDirectionalMarksAndReplacesOverrides(t *testing.T) {
	t.Parallel()
	const hebrew = "\u05e9\u05dc\u05d5\u05dd!\u200f"
	const arabic = "\u0645\u0631\u062d\u0628\u0627\u061c!"
	const marked = "ltr\u200e(1)"
	for value, want := range map[string]string{
		hebrew:                         hebrew,
		arabic:                         arabic,
		marked:                         marked,
		"a\u202ab\u202bc\u202cd":       "a b c d",
		"a\u202db\u202ec":              "a b c",
		"a\u2066b\u2067c\u2068d\u2069": "a b c d ",
	} {
		if got := withoutDirectionControls(value); got != want {
			t.Errorf("withoutDirectionControls(%q) = %q, want %q", value, got, want)
		}
	}

	node := requireNode(t)
	task := clientPlacementTask("WB-01J0000000000000000000MK01", hebrew, core.StatusReady, core.PriorityMedium)
	task.Labels = []string{arabic}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })
	page := request(t, handler, http.MethodGet, "/").Body.String()
	for _, want := range []string{hebrew, arabic} {
		if !strings.Contains(page, want) {
			t.Errorf("the served board does not draw %q as written", want)
		}
	}
	program := threadPageProgram(t, []core.Task{task}, `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const heading = findElement(main, (element) => element.id === "task-form-title");
  if (heading.textContent !== `+strconv.Quote(hebrew)+`) throw new Error("the heading reads " + JSON.stringify(heading.textContent));
  const title = findElement(main, (element) => element.id === "task-title");
  if (title.value !== `+strconv.Quote(hebrew)+`) throw new Error("the title field holds " + JSON.stringify(title.value));
  if (shownLine(`+strconv.Quote(marked)+`) !== `+strconv.Quote(marked)+`) throw new Error("shownLine dropped a mark");
  if (withoutDirectionControls("a\u202eb\u2066c") !== "a b c") throw new Error("an override or an isolate was drawn");
}, 0);
`)
	program = strings.Replace(program, "function shownLine(", "globalThis.shownLine = shownLine; function shownLine(", 1)
	program = strings.Replace(program, "function withoutDirectionControls(", "globalThis.withoutDirectionControls = withoutDirectionControls; function withoutDirectionControls(", 1)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the directional mark render: %v\n%s", err, output)
	}
}

// A file name can push its extension out of sight with characters that draw as
// blank space but are not spaces: seventy-eight U+3164 HANGUL FILLER between
// "invoice.pdf" and ".exe" drew as the one, blank lines, and the other far
// away. In a drawn file name each blank-looking filler is a space, and a run of
// them collapses to one, so the extension sits beside the name — in the row,
// in a reference, and in the history. The stored name is untouched.
func TestHandlerClientDrawsFileNamesWithoutBlankFillers(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const paddedID = "01K0M6B8A4FTT8C39MXXYTWA21"
	padded := "invoice.pdf" + strings.Repeat("\u3164", 78) + "\u115f\u1160\uffa0\u2800\u3000\u2003.exe"
	const want = "invoice.pdf .exe"
	task := threadPageTask()
	task.Description = "See ![](attachment:" + paddedID + ")"
	task.Attachments = append(task.Attachments, core.Attachment{
		ID: paddedID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
		AttachmentData: core.AttachmentData{
			Name: padded, Kind: core.AttachmentFile, Media: "application/octet-stream", Size: 2, Blob: strings.Repeat("d", 40),
		},
	})
	history, err := json.Marshal(map[string]any{
		"format": "workbook.task-history", "version": 1, "taskId": task.ID, "lifecycle": []any{},
		"history": map[string]any{"total": 1, "showing": 1, "changes": []any{map[string]any{
			"commit": "abc", "actor": "first@example.com", "wallTime": "2026-08-13T09:00:00Z", "logicalClock": 1,
			"summary": "changed attachment",
			"fields":  []any{map[string]any{"field": "attachment", "kind": "added", "to": padded}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	program := threadPageProgram(t, []core.Task{task}, `
const historyDocument = `+string(history)+`;
const boardFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if (String(url).endsWith("/history")) return { ok: true, json: async () => historyDocument };
  return boardFetch(url, options);
};
const want = `+strconv.Quote(want)+`;
const fillers = /[\u115f\u1160\u2800\u3164\uffa0\u3000\u2003]/u;
function check(where, shown) {
  if (fillers.test(shown)) throw new Error(where + " draws a blank filler: " + JSON.stringify(shown));
  if (!shown.includes(want)) throw new Error(where + " = " + JSON.stringify(shown) + ", want it to read " + JSON.stringify(want));
}
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const row = [...panelRows("attachments")].find((candidate) => candidate.dataset.attachmentId === `+strconv.Quote(paddedID)+`);
  if (!row) throw new Error("the attachment list has no row for the file");
  const link = findElement(row, (element) => element.tagName === "A");
  check("the attachment row", link.textContent);
  check("the row's remove control", rowControl(row, "Remove").getAttribute("aria-label"));
  const reference = findElements(main, (element) => element.tagName === "A" && String(element.href || "").endsWith("/attachments/" + `+strconv.Quote(paddedID)+`))
    .find((element) => element !== link);
  if (!reference) throw new Error("the description draws no reference to the file");
  check("the reference", reference.textContent);
  const change = findElement(main, (element) => element.dataset && element.dataset.field === "attachment");
  if (!change) throw new Error("the history draws no attachment change");
  check("the history entry", change.textContent);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the padded file name render: %v\n%s", err, output)
	}
}

// An attachment image's alternative text is what a browser draws when the file
// does not decode as an image, so it names the file by its real name, with
// what the author wrote beside it in parentheses — the rule a file reference
// follows. An author who wrote "invoice.pdf" over a file named
// "invoice<U+202E>fdp.exe" gets "invoice fdp.exe (invoice.pdf)".
func TestHandlerClientNamesAnAttachmentImageByItsFile(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const imageID = "01K0M6B8A4FTT8C39MXXYTWA31"
	task := threadPageTask()
	task.Description = "![invoice.pdf](attachment:" + imageID + ")\n\n![](attachment:" + imageID + ")"
	task.Attachments = append(task.Attachments, core.Attachment{
		ID: imageID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
		AttachmentData: core.AttachmentData{
			Name: "invoice\u202efdp.exe", Kind: core.AttachmentFile, Media: "image/png", Size: 2, Blob: strings.Repeat("e", 40),
		},
	})
	program := threadPageProgram(t, []core.Task{task}, `
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const images = findElements(main, (element) => element.tagName === "IMG" && String(element.src || "").endsWith(`+strconv.Quote("/attachments/"+imageID)+`));
  if (images.length !== 2) throw new Error("the description draws " + images.length + " images, want 2");
  if (images[0].alt !== "invoice fdp.exe (invoice.pdf)") throw new Error("the captioned image's alt is " + JSON.stringify(images[0].alt));
  if (images[1].alt !== "invoice fdp.exe") throw new Error("the uncaptioned image's alt is " + JSON.stringify(images[1].alt));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the attachment image render: %v\n%s", err, output)
	}
}

// A card's accessible name is cleaned by the server's template, and the card
// the client redraws must say the same: the screen reader hears the card's
// label, not its heading.
func TestHandlerClientRedrawsACardsAccessibleNameWithoutDirectionOverrides(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	live := clientPlacementTask("WB-01J0000000000000000000AL01", "Pay the invoice\u202efdp.exe", core.StatusReady, core.PriorityMedium)
	gone := clientPlacementTask("WB-01J0000000000000000000AL02", "Old\u202eexe.pdf", core.StatusReady, core.PriorityMedium)
	gone.Deleted = true
	tasks := []core.Task{live, gone}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	script := renderedClientScript(t, request(t, handler, http.MethodGet, "/").Body.String())
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks)})
	program := clientDOMHarness("/?deleted=1", string(document)) + script + `
const overrides = /[\u202a-\u202e\u2066-\u2069]/u;
setTimeout(async () => {
  // The poll redraws every card the client holds, which is the drawing the
  // server's template does not reach.
  await intervalCallback();
  const live = boardCard("WB-01J0000000000000000000AL01");
  if (!live) throw new Error("the client drew no card for the live task");
  const label = live.getAttribute("aria-label");
  if (overrides.test(label) || !label.includes("Pay the invoice fdp.exe")) throw new Error("the live card is named " + JSON.stringify(label));
  const gone = boardCard("WB-01J0000000000000000000AL02");
  if (!gone) throw new Error("the client drew no card for the deleted task");
  const named = [gone.getAttribute("aria-label"), ...findElements(gone, (element) => element.tagName === "BUTTON").map((button) => button.getAttribute("aria-label") || "")];
  named.forEach((name) => {
    if (overrides.test(name)) throw new Error("the deleted card carries the accessible name " + JSON.stringify(name));
  });
  if (!named.some((name) => name.includes("Restore task Old exe.pdf"))) throw new Error("the deleted card's names are " + JSON.stringify(named));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the card redraw: %v\n%s", err, output)
	}
}

// What an attachment's row names it by, asked of the helper itself: the shared
// text() drawing drops direction controls on its own, so a test that only read
// the row would pass with the label's own cleaning taken out. A control
// character in a link's label is a space, and a run of them one space.
func TestAttachmentShownNameCleansALinksLabel(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	program := threadPageProgram(t, []core.Task{threadPageTask()}, `
setTimeout(() => {
  const cases = [
    [{ kind: "link", label: "read\u0007\u0007me\u001b[31m", url: "https://example.test/" }, "read me [31m"],
    [{ kind: "link", label: "line\none", url: "https://example.test/" }, "line one"],
    [{ kind: "link", label: "", url: "https://example.test/\u0007x" }, "https://example.test/ x"],
    [{ kind: "file", name: "a\u0007b.txt" }, "a b.txt"],
  ];
  cases.forEach(([attachment, want]) => {
    const got = attachmentShownName(attachment);
    if (got !== want) throw new Error("attachmentShownName(" + JSON.stringify(attachment) + ") = " + JSON.stringify(got) + ", want " + JSON.stringify(want));
  });
}, 0);
`)
	program = strings.Replace(program, "function attachmentShownName(", "globalThis.attachmentShownName = attachmentShownName; function attachmentShownName(", 1)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the attachment name helper: %v\n%s", err, output)
	}
}
