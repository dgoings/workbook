package webui

import (
	"strconv"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// A change to a task that a proxy passed to the board and still answered 429
// for. The page waits, sends it again, and the resend names a head the first
// attempt moved, so the board refuses it as stale. Reported as it reads, that
// refusal tells the reader a change that landed did not, and the reader who
// makes it again records it twice.
//
// Every reporter of a task change is driven here through a proxy shaped that
// way: the first write reaches the board (the test swaps in what the board
// then holds) and answers 429 with Retry-After: 1; every later write is
// refused as stale. Each is run with the first attempt landed, with it not
// landed (somebody else moved the head instead), and where it matters with the
// board unreadable once the refusal comes.

// taskLandingProxy sits in front of the page's task writes. A test sets
// landing.land to what the board holds once the first attempt arrives, and
// may set landing.accept to an answer for writes after the refused resend.
const taskLandingProxy = `
const landing = { land: () => {}, writes: [], readsFail: false, accept: null };
const landingSettle = () => new Promise((resolve) => setTimeout(resolve, 0));
const beneathLanding = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  const method = (options.method || "GET").toUpperCase();
  if (!String(url).startsWith("/api/tasks")) return beneathLanding(url, options);
  if (method === "GET") {
    if (landing.readsFail) throw new TypeError("Failed to fetch");
    return beneathLanding(url, options);
  }
  landing.writes.push({ url, method, body: options.body === undefined ? null : JSON.parse(options.body) });
  if (landing.writes.length === 1) {
    landing.land();
    return { ok: false, status: 429,
      headers: { get: (name) => (name.toLowerCase() === "retry-after" ? "1" : null) },
      json: async () => JSON.parse("<html><head><title>429 Too Many Requests</title></head></html>") };
  }
  if (landing.accept) return { ok: true, status: 200, headers: { get: () => null }, json: async () => landing.accept };
  return { ok: false, status: 409, headers: { get: () => null }, json: async () => ({
    format: "workbook.error", version: 1,
    error: { category: "stale-write", message: "task changed since head-1; reload and try again" } }) };
};
// Ends the one-second wait the page was told to make, once it has made it,
// and lets what follows run.
async function releaseTaskHold() {
  for (let turn = 0; turn < 400; turn += 1) {
    const wait = windowTimeouts.find((timer) => !timer.canceled && !timer.ran && timer.delay === 1000);
    if (wait) {
      if (heldChangeNotice.hidden) throw new Error("the page held the change without saying so");
      wait.ran = true;
      wait.callback();
      for (let more = 0; more < 40; more += 1) await landingSettle();
      return;
    }
    await landingSettle();
  }
  throw new Error("the page never held the change to send it again");
}
function sentTwice() {
  if (landing.writes.length !== 2) throw new Error("the change was sent " + landing.writes.length + " times, want twice: once and the refused resend");
  if (!landing.writes[0].body || !landing.writes[0].body.expectedHead) throw new Error("the change named no head: " + JSON.stringify(landing.writes[0]));
}
const doubtSaid = "may already have been saved";
`

// landingOutcome is which way the first attempt went.
type landingOutcome string

const (
	firstAttemptLanded landingOutcome = "the first attempt landed"
	firstAttemptAbsent landingOutcome = "somebody else moved the head instead"
)

func TestClientSettlesACardChangeThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	// Production mutation: dropping the held-change check from the card queue
	// reports a move that landed as "This task changed elsewhere" and draws the
	// card back in the column it left.
	task, moved := heldChangeTasks("head-1")
	elsewhere := task
	elsewhere.Head = "head-2"
	elsewhere.Title = "Renamed elsewhere"
	for _, outcome := range []landingOutcome{firstAttemptLanded, firstAttemptAbsent} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			holds := moved
			check := `
  if (!cardIn("in-progress")) throw new Error("a move that landed is not drawn where it landed");
  if (cardFailureMessage(boardCard(taskID)) !== "") throw new Error("a move that landed was reported: " + JSON.stringify(cardFailureMessage(boardCard(taskID))));`
			if outcome == firstAttemptAbsent {
				holds = elsewhere
				check = `
  if (!cardIn("ready")) throw new Error("a move that never landed is still drawn as made");
  const said = cardFailureMessage(boardCard(taskID));
  if (said !== "This task changed elsewhere. The card shows the version the server holds.") throw new Error("a move that never landed reported " + JSON.stringify(said));`
			}
			runHeldChangeClient(t, "a held card move, "+string(outcome), heldChangeProgram(t, "/", task, moved, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, []core.Task{holds})+`;
  landing.land = () => { taskResponse = holds; };
  const dropped = dragTo("in-progress");
  await releaseTaskHold();
  await dropped;
  sentTwice();
`+check+`
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up once nothing was waiting");
`))
		})
	}
	t.Run("the board cannot be read", func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a held card move with the board unreadable", heldChangeProgram(t, "/", task, moved, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, []core.Task{moved})+`;
  landing.land = () => { taskResponse = holds; landing.readsFail = true; };
  const dropped = dragTo("in-progress");
  await releaseTaskHold();
  await dropped;
  sentTwice();
  const said = cardFailureMessage(boardCard(taskID));
  if (!said.includes("It may already have been saved")) throw new Error("a move nobody could check reported " + JSON.stringify(said));
  if (said.includes("changed elsewhere")) throw new Error("a move nobody could check was reported as refused: " + JSON.stringify(said));
`))
	})
}

// deletedLandingProgram is the board with its Deleted column shown, which is
// where a card is dropped to delete it and where Restore is pressed.
func deletedLandingProgram(t *testing.T, active, all []core.Task, body string) string {
	t.Helper()
	return clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, active)) + deletedColumnClientScript(t) + `
includedTaskResponse = ` + tasksDocumentJSON(t, all) + `;
` + taskLandingProxy + `
setTimeout(async () => {
  await landingSettle();
` + body + `
}, 0);
`
}

func TestClientSettlesADeleteAndARestoreThatLandedBeforeTheirResend(t *testing.T) {
	t.Parallel()
	// Production mutation: a delete or restore whose resend was refused after
	// its first attempt landed rolls the card back across the Deleted column's
	// edge and says the task changed elsewhere.
	tasks := deletedDropTasks()
	active := []core.Task{tasks[0], tasks[1]}
	deleted := tasks[0]
	deleted.Deleted = true
	deleted.Head = "head-deleted"
	touched := tasks[0]
	touched.Head = "head-touched"
	touched.Title = "Renamed elsewhere"
	restored := tasks[2]
	restored.Deleted = false
	restored.Head = "head-restored"
	retouched := tasks[2]
	retouched.Head = "head-retouched"
	for _, outcome := range []landingOutcome{firstAttemptLanded, firstAttemptAbsent} {
		t.Run("delete, "+string(outcome), func(t *testing.T) {
			t.Parallel()
			holds, check := []core.Task{deleted, tasks[1], tasks[2]}, `
  if (!deletedCards().some((item) => item.dataset.taskId === id)) throw new Error("a delete that landed is not in the Deleted column");
  if (cardFailureMessage(boardCard(id)) !== "") throw new Error("a delete that landed was reported: " + JSON.stringify(cardFailureMessage(boardCard(id))));`
			if outcome == firstAttemptAbsent {
				holds, check = []core.Task{touched, tasks[1], tasks[2]}, `
  if (deletedCards().some((item) => item.dataset.taskId === id)) throw new Error("a delete that never landed is drawn as made");
  const said = cardFailureMessage(boardCard(id));
  if (said !== "This task changed elsewhere. The card shows the version the server holds.") throw new Error("a delete that never landed reported " + JSON.stringify(said));`
			}
			runHeldChangeClient(t, "a held delete, "+string(outcome), deletedLandingProgram(t, active, tasks, `
  const id = `+strconv.Quote(tasks[0].ID)+`;
  const holds = `+tasksDocumentJSON(t, holds)+`;
  landing.land = () => { includedTaskResponse = holds; };
  const card = boardCard(id);
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const dropped = documentEventListeners.drop({ target: deletedList(), clientY: 1, dataTransfer, preventDefault() {} });
  documentEventListeners.dragend({ target: card });
  await releaseTaskHold();
  await dropped;
  sentTwice();
  if (landing.writes[0].method !== "DELETE") throw new Error("the drop sent " + landing.writes[0].method);
`+check+`
`))
		})
		t.Run("restore, "+string(outcome), func(t *testing.T) {
			t.Parallel()
			holds, check := []core.Task{tasks[0], tasks[1], restored}, `
  if (deletedCards().some((item) => item.dataset.taskId === id)) throw new Error("a restore that landed is still in the Deleted column");
  if (cardFailureMessage(boardCard(id)) !== "") throw new Error("a restore that landed was reported: " + JSON.stringify(cardFailureMessage(boardCard(id))));`
			if outcome == firstAttemptAbsent {
				holds, check = []core.Task{tasks[0], tasks[1], retouched}, `
  if (!deletedCards().some((item) => item.dataset.taskId === id)) throw new Error("a restore that never landed is drawn as made");
  const said = cardFailureMessage(boardCard(id));
  if (said !== "This task changed elsewhere. The card shows the version the server holds.") throw new Error("a restore that never landed reported " + JSON.stringify(said));`
			}
			runHeldChangeClient(t, "a held restore, "+string(outcome), deletedLandingProgram(t, active, tasks, `
  const id = `+strconv.Quote(tasks[2].ID)+`;
  const holds = `+tasksDocumentJSON(t, holds)+`;
  landing.land = () => { includedTaskResponse = holds; };
  const restoring = findElement(boardCard(id), (element) => hasDataKey(element, "restoreTask")).eventListeners.click();
  await releaseTaskHold();
  await restoring;
  sentTwice();
  if (!landing.writes[0].url.endsWith("/restore")) throw new Error("Restore sent " + landing.writes[0].url);
`+check+`
`))
		})
	}
}

// threadLandingProgram is the task page, its comment and attachment panels
// behind the landing proxy.
func threadLandingProgram(t *testing.T, task core.Task, body string) string {
	t.Helper()
	return threadPageProgram(t, []core.Task{task}, fileHarness+taskLandingProxy+`
setTimeout(async () => {
  await landingSettle();
`+body+`
}, 0);
`)
}

func threadDocumentJSON(t *testing.T, tasks []core.Task) string {
	t.Helper()
	return string(mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: threadPagePresentation(tasks)}))
}

// threadChange is one kind of change the task page's panels send.
type threadChange struct {
	name string
	// land is the task the board holds once the change is recorded.
	land func(core.Task) core.Task
	// drive makes the change and leaves its promise in `making`.
	drive string
	// panel is the panel that reports it.
	panel string
	// saved checks what a saved change leaves; kept what an unsaved one does.
	saved, kept string
}

func threadChanges() []threadChange {
	stamp := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	return []threadChange{
		{
			name: "a comment added",
			land: func(task core.Task) core.Task {
				task.Comments = append(append([]core.Comment{}, task.Comments...),
					core.Comment{ID: "01K0M6B8A4FTT8C39MXXYTWC09", Author: "first@example.com", Body: "A remark.", CreatedAt: stamp})
				return task
			},
			drive: `
  const area = findElement(panelSection("comments"), (element) => element.tagName === "TEXTAREA");
  area.value = "A remark.  ";
  const making = submitForm(findElement(panelSection("comments"), (element) => element.tagName === "FORM"));`,
			panel: "comments",
			saved: `
  if (area.value !== "") throw new Error("a comment that landed was left in the box: " + JSON.stringify(area.value));
  if (!panelRows("comments").some((row) => commentBodyText(row) === "A remark.")) throw new Error("the comment that landed is not drawn");`,
			kept: `
  if (area.value !== "A remark.  ") throw new Error("a comment that never landed was taken out of the box: " + JSON.stringify(area.value));`,
		},
		{
			name: "a comment edited",
			land: func(task core.Task) core.Task {
				task.Comments = append([]core.Comment{}, task.Comments...)
				task.Comments[0].Body = "Reworded."
				task.Comments[0].EditedAt = &stamp
				return task
			},
			drive: `
  rowControl(panelRows("comments")[0], "Edit").eventListeners.click();
  findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor")).value = "Reworded.";
  const making = rowControl(panelRows("comments")[0], "Save").eventListeners.click();`,
			panel: "comments",
			saved: `
  if (findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor"))) throw new Error("an edit that landed left its editor open");
  if (commentBodyText(panelRows("comments")[0]) !== "Reworded.") throw new Error("the edit that landed is not drawn");`,
			kept: `
  const editor = findElement(panelRows("comments")[0], (element) => hasDataKey(element, "commentEditor"));
  if (!editor || editor.value !== "Reworded.") throw new Error("an edit that never landed lost what was typed");`,
		},
		{
			name: "a comment removed",
			land: func(task core.Task) core.Task {
				task.Comments = append([]core.Comment{}, task.Comments[1:]...)
				return task
			},
			drive: `
  const making = rowControl(panelRows("comments")[0], "Remove").eventListeners.click();`,
			panel: "comments",
			saved: `
  if (panelRows("comments").length !== 1) throw new Error("a removal that landed left " + panelRows("comments").length + " comments drawn");`,
			kept: `
  if (panelRows("comments").length !== 2) throw new Error("a removal that never landed took a comment off the thread");`,
		},
		{
			name: "a link attached",
			land: func(task core.Task) core.Task {
				task.Attachments = append(append([]core.Attachment{}, task.Attachments...),
					core.Attachment{ID: "01K0M6B8A4FTT8C39MXXYTWA09", Author: "first@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
						Kind: core.AttachmentLink, URL: "https://example.test/build", Label: "Build",
					}})
				return task
			},
			drive: `
  const url = findElement(panelSection("attachments"), (element) => element.id === "attachment-url");
  url.value = "https://example.test/build";
  findElement(panelSection("attachments"), (element) => element.id === "attachment-label").value = " Build ";
  const making = submitForm(attachmentForm("link"));`,
			panel: "attachments",
			saved: `
  if (url.value !== "") throw new Error("a link that landed was left in the form: " + JSON.stringify(url.value));
  if (panelRows("attachments").length !== 3) throw new Error("the link that landed is not drawn");`,
			kept: `
  if (url.value !== "https://example.test/build") throw new Error("a link that never landed was taken out of the form");`,
		},
		{
			name: "a file attached",
			land: func(task core.Task) core.Task {
				task.Attachments = append(append([]core.Attachment{}, task.Attachments...),
					core.Attachment{ID: "01K0M6B8A4FTT8C39MXXYTWA09", Author: "first@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
						Name: "notes.txt", Kind: core.AttachmentFile, Media: "text/plain", Size: 5, Blob: "b",
					}})
				return task
			},
			drive: `
  const chooser = findElement(panelSection("attachments"), (element) => element.id === "attachment-file");
  chooseFiles(chooser, [new TestFile("notes.txt", 5, "notes")]);
  const making = submitForm(attachmentForm("file"));`,
			panel: "attachments",
			saved: `
  if (chooser.value !== "") throw new Error("a file that landed is still chosen: " + JSON.stringify(chooser.value));
  if (panelRows("attachments").length !== 3) throw new Error("the file that landed is not drawn");`,
			kept: `
  if (panelRows("attachments").length !== 2) throw new Error("a file that never landed is drawn");`,
		},
		{
			name: "an attachment removed",
			land: func(task core.Task) core.Task {
				task.Attachments = append([]core.Attachment{}, task.Attachments[1:]...)
				return task
			},
			drive: `
  const making = rowControl(panelRows("attachments")[0], "Remove").eventListeners.click();`,
			panel: "attachments",
			saved: `
  if (panelRows("attachments").length !== 1) throw new Error("a removal that landed left " + panelRows("attachments").length + " attachments drawn");`,
			kept: `
  if (panelRows("attachments").length !== 2) throw new Error("a removal that never landed took an attachment away");`,
		},
	}
}

func TestClientSettlesAThreadChangeThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	// Production mutation: dropping the held-change check from
	// writeThreadChange reports each of these as "was not saved" though the
	// board holds it, and keeps what was typed for the reader to send again.
	task := threadPageTask()
	elsewhere := task
	elsewhere.Head = "head-2"
	elsewhere.Title = "Renamed elsewhere"
	for _, change := range threadChanges() {
		for _, outcome := range []landingOutcome{firstAttemptLanded, firstAttemptAbsent} {
			t.Run(change.name+", "+string(outcome), func(t *testing.T) {
				t.Parallel()
				holds := change.land(task)
				holds.Head = "head-2"
				check := `
  if (panelStatusText(` + strconv.Quote(change.panel) + `) !== "") throw new Error("a change that landed was reported: " + JSON.stringify(panelStatusText(` + strconv.Quote(change.panel) + `)));
` + change.saved
				if outcome == firstAttemptAbsent {
					holds = elsewhere
					check = `
  const said = panelStatusText(` + strconv.Quote(change.panel) + `);
  if (!said.startsWith("That task changed elsewhere, so ") || !said.includes(" was not saved.")) throw new Error("a change that never landed reported " + JSON.stringify(said));
` + change.kept
				}
				runHeldChangeClient(t, change.name+", "+string(outcome), threadLandingProgram(t, task, `
  const holds = `+threadDocumentJSON(t, []core.Task{holds})+`;
  landing.land = () => { taskResponse = holds; };
`+change.drive+`
  await releaseTaskHold();
  await making;
  sentTwice();
`+check+`
  if (!heldChangeNotice.hidden) throw new Error("the notice stayed up once nothing was waiting");
`))
			})
		}
	}
}

func TestClientSaysAThreadChangeMayHaveLandedWhenItCannotLook(t *testing.T) {
	t.Parallel()
	// Production mutation: a comment refused after a resend, with the board
	// unreadable, reported as "was not saved"; or a second press of the same
	// comment, once the board reads again, sent without looking and recorded
	// the comment twice.
	task := threadPageTask()
	added := threadChanges()[0].land(task)
	added.Head = "head-2"
	start := `
  const holds = ` + threadDocumentJSON(t, []core.Task{added}) + `;
  landing.land = () => { taskResponse = holds; landing.readsFail = true; };
  const area = findElement(panelSection("comments"), (element) => element.tagName === "TEXTAREA");
  const form = findElement(panelSection("comments"), (element) => element.tagName === "FORM");
  area.value = "A remark.";
  const making = submitForm(form);
  await releaseTaskHold();
  await making;
  sentTwice();
  const said = panelStatusText("comments");
  if (!said.includes(doubtSaid) || !said.includes("check before making it again")) throw new Error("a comment nobody could check reported " + JSON.stringify(said));
  if (said.includes("was not saved")) throw new Error("a comment nobody could check was reported as not saved: " + JSON.stringify(said));
  if (area.value !== "A remark.") throw new Error("a comment that may not have landed was taken out of the box");
`
	t.Run("pressed again once the board reads, the comment there", func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a doubted comment pressed again", threadLandingProgram(t, task, start+`
  landing.readsFail = false;
  await submitForm(form);
  if (landing.writes.length !== 2) throw new Error("pressing again sent the comment again although the board holds it: " + landing.writes.length + " writes");
  if (area.value !== "" || panelStatusText("comments") !== "") throw new Error("the comment the board holds was not settled as saved");
  if (panelRows("comments").filter((row) => commentBodyText(row) === "A remark.").length !== 1) throw new Error("the comment is not drawn once");
`))
	})
	t.Run("pressed again once the board reads, the comment not there", func(t *testing.T) {
		t.Parallel()
		elsewhere := task
		elsewhere.Head = "head-3"
		runHeldChangeClient(t, "a doubted comment pressed again and sent", threadLandingProgram(t, task, start+`
  taskResponse = `+threadDocumentJSON(t, []core.Task{elsewhere})+`;
  landing.readsFail = false;
  landing.accept = { format: "workbook.task-mutation", version: 1, task: holds.tasks[0] };
  await submitForm(form);
  if (landing.writes.length !== 3) throw new Error("pressing again sent " + (landing.writes.length - 2) + " writes, want one");
  if (landing.writes[2].body.expectedHead !== "head-3") throw new Error("the comment was sent again against " + JSON.stringify(landing.writes[2].body.expectedHead) + ", want the head just read");
  if (area.value !== "") throw new Error("a comment sent and saved was left in the box");
`))
	})
	t.Run("pressed again with the board still unreadable", func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a doubted comment pressed again blind", threadLandingProgram(t, task, start+`
  await submitForm(form);
  if (landing.writes.length !== 2) throw new Error("pressing again with the board unreadable sent the comment blind");
  if (!panelStatusText("comments").includes(doubtSaid)) throw new Error("pressing again blind said " + JSON.stringify(panelStatusText("comments")));
`))
	})
}

func assignableDocumentJSON(t *testing.T, tasks []core.Task) string {
	t.Helper()
	return string(mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: tasks,
		Presentation: presentationForTasksAs(tasks, boardIdentity)}))
}

func TestClientSettlesAnAssignmentThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	// Production mutation: dropping the held-change check from the assignment
	// form or a withdrawal reports either as "was not saved" though the board
	// holds it.
	base := heldTask()
	assigned := base
	assigned.Assignments = append(append([]core.Assignment{}, base.Assignments...),
		core.Assignment{Principal: "someone@example.com", Creator: boardIdentity, CreatedAt: assignmentStamp})
	withdrawn := base
	withdrawn.Assignments = []core.Assignment{base.Assignments[0], base.Assignments[2]}
	elsewhere := base
	elsewhere.Title = "Renamed elsewhere"
	changes := []struct {
		name, drive, saved, kept string
		holds                    core.Task
	}{
		{
			name:  "an assignment made",
			holds: assigned,
			drive: `
  assignmentField().value = "someone@example.com";
  const making = submitAssignment();`,
			saved: `
  if (assignmentField().value !== "") throw new Error("an assignment that landed was left in the field");`,
			kept: `
  if (assignmentField().value !== "someone@example.com") throw new Error("an assignment that never landed was taken out of the field");`,
		},
		{
			name:  "an assignment withdrawn",
			holds: withdrawn,
			drive: `
  const row = assignmentRows().find((item) => item.textContent.includes("sam@example.com"));
  const making = withdrawControl(row).eventListeners.click();`,
			saved: `
  if (assignmentRows().some((item) => item.textContent.includes("sam@example.com"))) throw new Error("a withdrawal that landed left the assignment drawn");`,
			kept: `
  if (!assignmentRows().some((item) => item.textContent.includes("sam@example.com"))) throw new Error("a withdrawal that never landed took the assignment away");`,
		},
	}
	for _, change := range changes {
		for _, outcome := range []landingOutcome{firstAttemptLanded, firstAttemptAbsent} {
			t.Run(change.name+", "+string(outcome), func(t *testing.T) {
				t.Parallel()
				holds := change.holds
				check := `
  if (assignmentStatusText() !== "") throw new Error("a change that landed was reported: " + JSON.stringify(assignmentStatusText()));
` + change.saved
				if outcome == firstAttemptAbsent {
					holds = elsewhere
					check = `
  const said = assignmentStatusText();
  if (!said.startsWith("That task changed elsewhere, so ") || !said.includes(" was not saved.")) throw new Error("a change that never landed reported " + JSON.stringify(said));
` + change.kept
				}
				holds.Head = "head-2"
				board := &assignableBoard{task: base}
				runHeldChangeClient(t, change.name+", "+string(outcome), assignablePageProgram(t, board, []core.Task{base}, taskLandingProxy+`
setTimeout(async () => {
  await landingSettle();
  const holds = `+assignableDocumentJSON(t, []core.Task{holds})+`;
  landing.land = () => { taskResponse = holds; };
`+change.drive+`
  await releaseTaskHold();
  await making;
  sentTwice();
`+check+`
}, 0);
`))
			})
		}
	}
}

func TestClientSettlesATaskFormSaveThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	// Production mutation: dropping the held-change check from the task form
	// says a save that landed met a task changed elsewhere and keeps the form
	// open for a save that would record the same values again.
	task, _ := heldChangeTasks("head-1")
	saved := task
	saved.Title = "First edit"
	saved.Head = "head-2"
	elsewhere := task
	elsewhere.Description = "Changed elsewhere."
	elsewhere.Head = "head-2"
	drive := `
  const form = findElement(main, (element) => element.tagName === "FORM");
  findElement(form, (element) => element.id === "task-title").value = "First edit ";
  const saving = form.eventListeners.submit({ preventDefault() {} });
  await releaseTaskHold();
  await saving;
  sentTwice();
`
	cases := []struct {
		name  string
		holds core.Task
		fail  bool
		check string
	}{
		{name: string(firstAttemptLanded), holds: saved, check: `
  if (!historyPaths.includes("/")) throw new Error("a save that landed did not return to the board: " + JSON.stringify(historyPaths) + " " + JSON.stringify(saveStatus()));`},
		{name: string(firstAttemptAbsent), holds: elsewhere, check: `
  if (historyPaths.includes("/")) throw new Error("a save that never landed left the form");
  if (saveStatus() !== "That task changed elsewhere. Your edits are kept; save again to apply them to the version the server holds.") throw new Error("a save that never landed said " + JSON.stringify(saveStatus()));`},
		{name: "the board cannot be read", holds: saved, fail: true, check: `
  if (historyPaths.includes("/")) throw new Error("a save nobody could check left the form");
  if (!saveStatus().includes(doubtSaid) || saveStatus().includes("changed elsewhere")) throw new Error("a save nobody could check said " + JSON.stringify(saveStatus()));`},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			t.Parallel()
			fail := ""
			if each.fail {
				fail = " landing.readsFail = true;"
			}
			runHeldChangeClient(t, "a held save, "+each.name, heldChangeProgram(t, "/tasks/"+task.ID, task, saved, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, []core.Task{each.holds})+`;
  landing.land = () => { taskResponse = holds;`+fail+` };
`+drive+each.check+`
`))
		})
	}
}

func TestClientDeletesFromTheTaskFormAgainstItsHead(t *testing.T) {
	t.Parallel()
	// Production mutation: a Delete that names no head is never resent, so a
	// proxy's 429 reads as a failed delete; and one that names it, refused
	// after its first attempt landed, says the task changed elsewhere though
	// it is gone.
	task, _ := heldChangeTasks("head-1")
	elsewhere := task
	elsewhere.Title = "Renamed elsewhere"
	elsewhere.Head = "head-2"
	drive := `
  const remove = findElement(main, (element) => element.tagName === "BUTTON" && hasClassToken(element, "delete-button"));
  const deleting = remove.eventListeners.click();
  await releaseTaskHold();
  await deleting;
  sentTwice();
  if (landing.writes[0].method !== "DELETE" || landing.writes[0].body.expectedHead !== "head-1") throw new Error("Delete sent " + JSON.stringify(landing.writes[0]));
`
	t.Run(string(firstAttemptLanded), func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a held delete that landed", heldChangeProgram(t, "/tasks/"+task.ID, task, task, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, nil)+`;
  landing.land = () => { taskResponse = holds; };
`+drive+`
  if (!historyPaths.some((path) => path.startsWith("/?deleted=1"))) throw new Error("a delete that landed did not go to the Deleted column: " + JSON.stringify(historyPaths) + " " + JSON.stringify(saveStatus()));
`))
	})
	t.Run(string(firstAttemptAbsent), func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a held delete that never landed", heldChangeProgram(t, "/tasks/"+task.ID, task, task, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, []core.Task{elsewhere})+`;
  landing.land = () => { taskResponse = holds; };
`+drive+`
  if (historyPaths.length) throw new Error("a delete that never landed left the form: " + JSON.stringify(historyPaths));
  if (saveStatus() !== "That task changed elsewhere, so it was not deleted. Press Delete again to delete the version the server now holds.") throw new Error("a delete that never landed said " + JSON.stringify(saveStatus()));
  landing.accept = { format: "workbook.task-mutation", version: 1, task: Object.assign({}, holds.tasks[0], { deleted: true, head: "head-3" }) };
  await remove.eventListeners.click();
  if (landing.writes.length !== 3 || landing.writes[2].body.expectedHead !== "head-2") throw new Error("Delete pressed again sent " + JSON.stringify(landing.writes.slice(2)));
  if (!historyPaths.some((path) => path.startsWith("/?deleted=1"))) throw new Error("the delete pressed again did not go to the Deleted column");
`))
	})
	t.Run("the board cannot be read", func(t *testing.T) {
		t.Parallel()
		runHeldChangeClient(t, "a held delete nobody could check", heldChangeProgram(t, "/tasks/"+task.ID, task, task, taskLandingProxy+`
  const holds = `+tasksDocumentJSON(t, nil)+`;
  landing.land = () => { taskResponse = holds; landing.readsFail = true; };
`+drive+`
  if (historyPaths.length) throw new Error("a delete nobody could check left the form");
  if (!saveStatus().includes(doubtSaid)) throw new Error("a delete nobody could check said " + JSON.stringify(saveStatus()));
`))
	})
	// And with nothing in the way, Delete still sends once, now with the head.
	runHeldChangeClient(t, "a plain delete", heldChangeProgram(t, "/tasks/"+task.ID, task, task, `
  const remove = findElement(main, (element) => element.tagName === "BUTTON" && hasClassToken(element, "delete-button"));
  await remove.eventListeners.click();
  if (writes.length !== 1 || JSON.parse(writes[0].body).expectedHead !== "head-1") throw new Error("Delete sent " + JSON.stringify(writes));
`))
}
