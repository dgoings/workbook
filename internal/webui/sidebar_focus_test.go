package webui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Where the keyboard focus lands after a write in the task sidebar, pressed
// with real keys in a real browser against a board that applies the write.
// Every sidebar list is rebuilt from what a write answers, and the buttons in
// it are disabled while the write is in flight, so the control a keyboard
// reader pressed is gone or blurred by the time the answer is drawn; left to
// the browser, the caret falls to <body> and the reader is sent back to the
// top of the page. The rule is the one the chosen-file × already kept: after
// a removal, the × now in the removed row's place, else the one before it,
// else the section's own input; after an add, that input; and after a write
// that was refused, the control that was pressed.
//
// Each case is read twice: once the redraw has settled, and again after two
// more of the board's once-a-second polls, which must not take the caret back
// off wherever it was put.

const (
	focusTaskID      = "WB-01J0000000000000000000FC00"
	focusFirstID     = "WB-01J0000000000000000000FC01"
	focusSecondID    = "WB-01J0000000000000000000FC02"
	focusBlockedID   = "WB-01J0000000000000000000FC03"
	focusCandidateID = "WB-01J0000000000000000000FC04"
	focusCreatedID   = "WB-01J0000000000000000000FC05"
)

const (
	focusFirstTitle     = "Write the first prerequisite"
	focusSecondTitle    = "Write the second prerequisite"
	focusBlockedTitle   = "Ship what this task holds up"
	focusCandidateTitle = "Pick this task to add"
)

// focusBoard is a board whose sidebar writes really happen: a dependency
// added or freed, an assignment withdrawn, a link attached or an attachment
// removed is applied to the task it names, and every read after it sees the
// result. With refuse set, every write is refused instead; with refuseLinks
// set, only the dependency and assignment writes are, which is what a create
// whose follow-up writes failed hands to the new task's page.
type focusBoard struct {
	mu          sync.Mutex
	tasks       []core.Task
	head        int
	refuse      bool
	refuseLinks bool
	next        int
}

// newFocusBoard builds the board. With single set, the task holds exactly one
// row in each list, so removing it leaves the list empty.
func newFocusBoard(single, refuse bool) *focusBoard {
	target := clientPlacementTask(focusTaskID, "Keep the caret where it belongs", core.StatusInProgress, core.PriorityHigh)
	target.Head = "head-0"
	first := clientPlacementTask(focusFirstID, focusFirstTitle, core.StatusReady, core.PriorityMedium)
	second := clientPlacementTask(focusSecondID, focusSecondTitle, core.StatusReady, core.PriorityLow)
	blocked := clientPlacementTask(focusBlockedID, focusBlockedTitle, core.StatusBacklog, core.PriorityLow)
	candidate := clientPlacementTask(focusCandidateID, focusCandidateTitle, core.StatusReady, core.PriorityMedium)
	target.Dependencies = []string{focusFirstID, focusSecondID}
	blocked.Dependencies = []string{focusTaskID}
	target.Assignments = []core.Assignment{
		{Principal: boardIdentity, Label: "impl-1", Creator: boardIdentity, CreatedAt: assignmentStamp},
		{Principal: boardIdentity, Label: "impl-2", Creator: boardIdentity, CreatedAt: assignmentStamp},
	}
	target.Attachments = []core.Attachment{
		{ID: "att-one", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/one", Label: "One",
		}},
		{ID: "att-two", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/two", Label: "Two",
		}},
		{ID: "att-file", Author: boardIdentity, AddedAt: assignmentStamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentFile, Name: "notes.txt", Media: "text/plain", Size: 120, Blob: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	if single {
		target.Dependencies = target.Dependencies[:1]
		blocked.Dependencies = nil
		target.Assignments = target.Assignments[:1]
		target.Attachments = target.Attachments[:1]
	}
	return &focusBoard{tasks: []core.Task{target, first, second, blocked, candidate}, refuse: refuse}
}

// write applies change to the task id names and answers with the result.
func (board *focusBoard) write(id string, change func(*core.Task)) (core.MutationResult, error) {
	return board.writeRefusing(id, false, change)
}

// link is write for the dependency and assignment routes.
func (board *focusBoard) link(id string, change func(*core.Task)) (core.MutationResult, error) {
	return board.writeRefusing(id, true, change)
}

func (board *focusBoard) writeRefusing(id string, isLink bool, change func(*core.Task)) (core.MutationResult, error) {
	board.mu.Lock()
	defer board.mu.Unlock()
	if board.refuse || (isLink && board.refuseLinks) {
		return core.MutationResult{}, core.Errorf(core.CategoryValidation, "refused in this test")
	}
	for index := range board.tasks {
		if board.tasks[index].ID != id {
			continue
		}
		change(&board.tasks[index])
		board.head++
		board.tasks[index].Head = "head-" + strconv.Itoa(board.head)
		return core.MutationResult{Task: copyFocusTask(board.tasks[index])}, nil
	}
	return core.MutationResult{}, core.Errorf(core.CategoryNotFound, "no task %s", id)
}

// copyFocusTask copies the slices a write changes, so a task handed to the
// handler is never changed under it by a later write.
func copyFocusTask(task core.Task) core.Task {
	task.Dependencies = slices.Clone(task.Dependencies)
	task.Assignments = slices.Clone(task.Assignments)
	task.Attachments = slices.Clone(task.Attachments)
	return task
}

func (board *focusBoard) list() []core.Task {
	board.mu.Lock()
	defer board.mu.Unlock()
	tasks := make([]core.Task, len(board.tasks))
	for index, task := range board.tasks {
		tasks[index] = copyFocusTask(task)
	}
	return tasks
}

func (board *focusBoard) handler(t *testing.T) http.Handler {
	t.Helper()
	return NewHandler(Options{
		Identity: boardIdentity,
		List:     func(context.Context) ([]core.Task, error) { return board.list(), nil },
		Update:   unexpectedTaskUpdate(t),
		Create: func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
			board.mu.Lock()
			defer board.mu.Unlock()
			created := clientPlacementTask(focusCreatedID, input.Title, core.StatusReady, core.PriorityMedium)
			created.Head = "head-created"
			board.tasks = append(board.tasks, created)
			return core.MutationResult{Task: copyFocusTask(created)}, nil
		},
		History: func(_ context.Context, id string) (core.TaskDetail, error) {
			detail := historyDetail()
			for _, task := range board.list() {
				if task.ID == id {
					detail.Task = task
					return detail, nil
				}
			}
			return core.TaskDetail{}, core.Errorf(core.CategoryNotFound, "no task %s", id)
		},
		Depend: func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			return board.link(id, func(task *core.Task) { task.Dependencies = append(task.Dependencies, dependency) })
		},
		Free: func(_ context.Context, id, dependency string) (core.MutationResult, error) {
			return board.link(id, func(task *core.Task) {
				task.Dependencies = slices.DeleteFunc(task.Dependencies, func(held string) bool { return held == dependency })
			})
		},
		Assign: func(_ context.Context, id string, input core.AssignInput) (core.MutationResult, error) {
			return board.link(id, func(task *core.Task) {
				principal, label := input.To, ""
				for index := 0; index < len(input.To); index++ {
					if input.To[index] == '/' {
						principal, label = input.To[:index], input.To[index+1:]
						break
					}
				}
				task.Assignments = append(task.Assignments, core.Assignment{Principal: principal, Label: label, Creator: boardIdentity, CreatedAt: assignmentStamp})
			})
		},
		Unassign: func(_ context.Context, id string, input core.UnassignInput) (core.MutationResult, error) {
			return board.link(id, func(task *core.Task) {
				task.Assignments = slices.DeleteFunc(task.Assignments, func(held core.Assignment) bool { return held.Value() == input.From })
			})
		},
		AddAttachment: func(_ context.Context, id string, input core.AttachmentAddInput) (core.MutationResult, error) {
			return board.write(id, func(task *core.Task) {
				board.next++
				task.Attachments = append(task.Attachments, core.Attachment{
					ID: fmt.Sprintf("att-added-%d", board.next), Author: boardIdentity, AddedAt: assignmentStamp,
					AttachmentData: core.AttachmentData{Kind: core.AttachmentLink, URL: input.URL, Label: input.Label},
				})
			})
		},
		RemoveAttachment: func(_ context.Context, id string, input core.AttachmentRemoveInput) (core.MutationResult, error) {
			return board.write(id, func(task *core.Task) {
				task.Attachments = slices.DeleteFunc(task.Attachments, func(held core.Attachment) bool { return held.ID == input.AttachmentID })
			})
		},
	})
}

// sidebarFocusHelpers opens every case's setup and script. It is declared
// inside each one's function rather than at the top level, because the page
// keeps one global scope across the two evaluations.
const sidebarFocusHelpers = pageGeometryHelpers + `
const sidebar = () => document.querySelector(".task-sidebar");
const named = (name) => document.querySelector('[aria-label="' + name + '"]');
const describeFocus = () => {
  const element = document.activeElement;
  if (!element || element === document.body) return "body";
  return element.getAttribute("aria-label") || (element.id ? "#" + element.id : element.tagName + " " + element.textContent.trim());
};
const dependencyRows = () => document.querySelectorAll(".task-sidebar [data-relationship-row]").length;
const assignmentRows = () => document.querySelectorAll('.task-sidebar [data-task-panel="assignments"] .assignment').length;
const attachmentRows = () => document.querySelectorAll('.task-sidebar [data-task-panel="attachments"] .attachment').length;
// Nothing in the sidebar is waiting on a write: every × and every box takes
// input again.
const idle = () => [...sidebar().querySelectorAll("[data-row-remove], input")].every((element) => !element.disabled);
const said = () => [...sidebar().querySelectorAll(".relationship-message, [data-panel-status]")].some((element) => element.textContent.trim());
const frames = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
// Picks a task in the dependency picker the way a pointer does, with the
// kind toggle set first.
const pickDependency = async (direction, title) => {
  document.querySelector("#dependency-kind-" + direction).click();
  const input = document.querySelector("#dependency-input");
  input.focus();
  input.value = title;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  const option = () => [...document.querySelectorAll("#dependency-listbox [role=option]")]
    .find((candidate) => candidate.querySelector("strong").textContent === title);
  await waitFor(option, "the option for " + title);
  option().click();
  await waitFor(() => !document.querySelector("[data-relationship-add]").disabled, "Add to take the pick");
};
const stageDependency = async (direction, title) => {
  const before = dependencyRows();
  await pickDependency(direction, title);
  document.querySelector("[data-relationship-add]").click();
  await waitFor(() => dependencyRows() === before + 1, "the staged dependency");
};
const stageAssignment = async (value) => {
  const before = assignmentRows();
  document.querySelector("#assignment-to").value = value;
  document.querySelector("[data-assignment-form]").requestSubmit();
  await waitFor(() => assignmentRows() === before + 1, "the staged assignment");
};
const stageLink = async (url) => {
  const before = attachmentRows();
  document.querySelector("#attachment-url").value = url;
  document.querySelector("[data-attachment-link-form]").requestSubmit(document.querySelector("[data-attachment-add-link]"));
  await waitFor(() => attachmentRows() === before + 1, "the staged link");
};
`

// sidebarFocusCase is one press and where it must leave the caret.
type sidebarFocusCase struct {
	name string
	// newTask opens the New Task form rather than the task's page.
	newTask bool
	// single gives the task one row in each list; refuse refuses every write.
	single, refuse bool
	// stage runs once the sidebar is drawn, before focus is placed: it fills
	// in what the press needs, such as a picked task or staged rows.
	stage string
	// focus is an expression for the control the key is pressed on.
	focus string
	// done is a condition that holds once the write has been answered and
	// drawn.
	done string
	// moveTo, when set, is an expression for a control the reader moves to
	// while the write is still in flight; the write is held back long enough
	// for that to happen first.
	moveTo string
	// want is where the caret must be, as describeFocus names it.
	want string
}

func runSidebarFocusCase(t *testing.T, test sidebarFocusCase) {
	t.Helper()
	board := newFocusBoard(test.single, test.refuse)
	path := "/tasks/" + focusTaskID
	ready := `dependencyRows() > 0 && document.querySelector('[data-task-panel="assignments"]') && document.querySelector('[data-task-panel="attachments"] .attachment')`
	if test.newTask {
		path = "/tasks/new"
		ready = `document.querySelector('[data-task-panel="assignments"] #assignment-to') && document.querySelector("#attachment-url") && document.querySelector("#dependency-input")`
	}
	slow := ""
	moved := ""
	if test.moveTo != "" {
		slow = `
  const realFetch = window.fetch;
  window.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") await pause(600);
    return realFetch(url, options);
  };`
		moved = `
  await pause(100);
  const elsewhere = ` + test.moveTo + `;
  elsewhere.focus();
  if (document.activeElement !== elsewhere) throw new Error("the reader could not move elsewhere");`
	}
	var result struct {
		After, Later string
		Popup        bool
	}
	openInBrowser(t, board.handler(t), browserPage{
		Width: 1280, Height: 900,
		Path: path,
		Setup: `
(async () => {
` + sidebarFocusHelpers + `
  await waitFor(() => sidebar() && (` + ready + `), "the sidebar");
` + slow + test.stage + `
  const control = ` + test.focus + `;
  if (!control) throw new Error("there is no control to press");
  control.focus();
  if (document.activeElement !== control) throw new Error("the control could not take focus");
  await frames();
})()`,
		Keys: []string{"Enter"},
		Script: `
(async () => {
` + sidebarFocusHelpers + moved + `
  await waitFor(() => (` + test.done + `) && idle(), "the write to be answered and drawn");
  await frames();
  await pause(150);
  const after = describeFocus();
  // Two more of the board's once-a-second polls.
  await pause(2300);
  const listbox = document.querySelector("#dependency-listbox");
  return { after, later: describeFocus(), popup: Boolean(listbox && !listbox.hidden) };
})()`,
	}, &result)
	if result.After != test.want {
		t.Errorf("once the write was drawn, focus is on %q, want %q", result.After, test.want)
	}
	if result.Later != test.want {
		t.Errorf("two polls later, focus is on %q, want %q", result.Later, test.want)
	}
	if result.Popup {
		t.Errorf("handing the caret back opened the dependency picker's list")
	}
}

func TestBrowserPlacesFocusAfterSidebarWritesOnTheTaskPage(t *testing.T) {
	t.Parallel()
	removeDependency := "Remove dependency on "
	for _, test := range []sidebarFocusCase{
		{
			name:  "depends-on x takes the next row's x",
			focus: `named("` + removeDependency + focusFirstTitle + `")`,
			done:  `dependencyRows() === 2`,
			want:  removeDependency + focusSecondTitle,
		},
		{
			name:  "blocks x on the last row takes the one before it",
			focus: `named("Remove blocked task ` + focusBlockedTitle + `")`,
			done:  `dependencyRows() === 2`,
			want:  removeDependency + focusSecondTitle,
		},
		{
			name: "the only dependency's x takes the picker", single: true,
			focus: `named("` + removeDependency + focusFirstTitle + `")`,
			done:  `dependencyRows() === 0`,
			want:  "#dependency-input",
		},
		{
			name:  "assignment x takes the next row's x",
			focus: `named("Unassign ` + boardIdentity + `/impl-1")`,
			done:  `assignmentRows() === 1`,
			want:  "Unassign " + boardIdentity + "/impl-2",
		},
		{
			name: "the only assignment's x takes the assign box", single: true,
			focus: `named("Unassign ` + boardIdentity + `/impl-1")`,
			done:  `assignmentRows() === 0`,
			want:  "#assignment-to",
		},
		{
			name:  "attachment x takes the next row's x",
			focus: `named("Remove One")`,
			done:  `attachmentRows() === 2`,
			want:  "Remove Two",
		},
		{
			name:  "attachment x on the last row takes the one before it",
			focus: `named("Remove notes.txt")`,
			done:  `attachmentRows() === 2`,
			want:  "Remove Two",
		},
		{
			name: "the only attachment's x takes the URL box", single: true,
			focus: `named("Remove One")`,
			done:  `attachmentRows() === 0`,
			want:  "#attachment-url",
		},
		{
			name:  "adding a dependency returns to the picker",
			stage: `await pickDependency("depends-on", "` + focusCandidateTitle + `");`,
			focus: `document.querySelector("[data-relationship-add]")`,
			done:  `dependencyRows() === 4`,
			want:  "#dependency-input",
		},
		{
			name:  "attaching a link returns to the URL box",
			stage: `document.querySelector("#attachment-url").value = "https://example.test/three";`,
			focus: `document.querySelector("[data-attachment-add-link]")`,
			done:  `attachmentRows() === 4`,
			want:  "#attachment-url",
		},
		{
			name:   "a caret moved elsewhere during a removal stays there",
			focus:  `named("` + removeDependency + focusFirstTitle + `")`,
			moveTo: `document.querySelector("#comment-body")`,
			done:   `dependencyRows() === 2`,
			want:   "#comment-body",
		},
		{
			name:   "a caret moved elsewhere while a link is sent stays there",
			stage:  `document.querySelector("#attachment-url").value = "https://example.test/three";`,
			focus:  `document.querySelector("[data-attachment-add-link]")`,
			moveTo: `document.querySelector("#comment-body")`,
			done:   `attachmentRows() === 4`,
			want:   "#comment-body",
		},
		{
			name: "a refused dependency removal keeps its x", refuse: true,
			focus: `named("` + removeDependency + focusFirstTitle + `")`,
			done:  `said()`,
			want:  removeDependency + focusFirstTitle,
		},
		{
			name: "a refused withdrawal keeps its x", refuse: true,
			focus: `named("Unassign ` + boardIdentity + `/impl-1")`,
			done:  `said()`,
			want:  "Unassign " + boardIdentity + "/impl-1",
		},
		{
			name: "a refused attachment removal keeps its x", refuse: true,
			focus: `named("Remove One")`,
			done:  `said()`,
			want:  "Remove One",
		},
		{
			name: "a refused dependency add keeps Add", refuse: true,
			stage: `await pickDependency("depends-on", "` + focusCandidateTitle + `");`,
			focus: `document.querySelector("[data-relationship-add]")`,
			done:  `said()`,
			want:  "Add dependency",
		},
		{
			name: "a refused link keeps Attach", refuse: true,
			stage: `document.querySelector("#attachment-url").value = "https://example.test/three";`,
			focus: `document.querySelector("[data-attachment-add-link]")`,
			done:  `said()`,
			want:  "Attach link",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runSidebarFocusCase(t, test)
		})
	}
}

func TestBrowserPlacesFocusAfterSidebarWritesOnTheNewTaskForm(t *testing.T) {
	t.Parallel()
	stageTwoDependencies := `
  await stageDependency("depends-on", "` + focusFirstTitle + `");
  await stageDependency("blocks", "` + focusSecondTitle + `");`
	stageTwoAssignments := `
  await stageAssignment("sam@example.com");
  await stageAssignment("kim@example.com");`
	stageTwoLinks := `
  await stageLink("https://example.test/one");
  await stageLink("https://example.test/two");`
	for _, test := range []sidebarFocusCase{
		{
			name:  "staged depends-on x takes the next row's x",
			stage: stageTwoDependencies,
			focus: `named("Remove dependency on ` + focusFirstTitle + `")`,
			done:  `dependencyRows() === 1`,
			want:  "Remove blocked task " + focusSecondTitle,
		},
		{
			name:  "staged blocks x on the last row takes the one before it",
			stage: stageTwoDependencies,
			focus: `named("Remove blocked task ` + focusSecondTitle + `")`,
			done:  `dependencyRows() === 1`,
			want:  "Remove dependency on " + focusFirstTitle,
		},
		{
			name:  "the only staged dependency's x takes the picker",
			stage: `await stageDependency("depends-on", "` + focusFirstTitle + `");`,
			focus: `named("Remove dependency on ` + focusFirstTitle + `")`,
			done:  `dependencyRows() === 0`,
			want:  "#dependency-input",
		},
		{
			name:  "staged assignment x takes the next row's x",
			stage: stageTwoAssignments,
			focus: `named("Remove sam@example.com")`,
			done:  `assignmentRows() === 1`,
			want:  "Remove kim@example.com",
		},
		{
			name:  "the only staged assignment's x takes the assign box",
			stage: `await stageAssignment("sam@example.com");`,
			focus: `named("Remove sam@example.com")`,
			done:  `assignmentRows() === 0`,
			want:  "#assignment-to",
		},
		{
			name:  "staged attachment x on the last row takes the one before it",
			stage: stageTwoLinks,
			focus: `named("Remove https://example.test/two")`,
			done:  `attachmentRows() === 1`,
			want:  "Remove https://example.test/one",
		},
		{
			name:  "the only staged attachment's x takes the URL box",
			stage: `await stageLink("https://example.test/one");`,
			focus: `named("Remove https://example.test/one")`,
			done:  `attachmentRows() === 0`,
			want:  "#attachment-url",
		},
		{
			name:  "staging a dependency returns to the picker",
			stage: `await pickDependency("depends-on", "` + focusCandidateTitle + `");`,
			focus: `document.querySelector("[data-relationship-add]")`,
			done:  `dependencyRows() === 1`,
			want:  "#dependency-input",
		},
		{
			name:  "staging a link returns to the URL box",
			stage: `document.querySelector("#attachment-url").value = "https://example.test/three";`,
			focus: `document.querySelector("[data-attachment-add-link]")`,
			done:  `attachmentRows() === 1`,
			want:  "#attachment-url",
		},
		{
			name:  "a refused link keeps Add",
			stage: `document.querySelector("#attachment-url").value = "ftp://example.test/three";`,
			focus: `document.querySelector("[data-attachment-add-link]")`,
			done:  `said()`,
			want:  "Add link",
		},
	} {
		test.newTask = true
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runSidebarFocusCase(t, test)
		})
	}
}
