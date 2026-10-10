package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the task form does with a priority this project's vocabulary cannot
// resolve.
//
// A stored priority can resolve to nothing — a teammate on a newer build
// authors a priority, files a task under it, and this build has never heard of
// it. The select is built from the priorities the server published, so it has
// no option for that value, and a select with no selected option shows its
// first one. That is not a display problem: the form's save sends every field
// whose control disagrees with the task, so the reader's next save carries a
// priority they never chose. The status half of the form solved this with a
// disabled placeholder that names the value the server holds and sends nothing;
// these tests are the priority half of it.

// strandedPriority is a priority projectPriorities does not define and nothing
// resolves, so a task holding it opens a form that cannot offer it.
const strandedPriority = core.Priority("critical")

const strandedPriorityTaskID = "WB-01J0000000000000000000F707"

// strandedPriorityTask is a task filed under a priority this build cannot
// resolve, the way a teammate's newer build would have filed it.
func strandedPriorityTask() core.Task {
	task := clientPlacementTask(strandedPriorityTaskID, "Filed by a teammate", core.StatusReady, strandedPriority)
	task.Head = "head-a"
	return task
}

// The form names the priority the server holds instead of showing one the
// reader did not choose.
//
// The value leads. A collapsed select is about 200px of the properties column,
// and what it clips is the end of the text: a placeholder that opened with the
// instruction — "Choose one of this project's priorities (current: critical)" —
// showed the instruction and lost the value, which is the one fact the reader
// cannot recover from anywhere else on the page. The explanation is the half
// the clip may take.
func TestHandlerClientNamesAPriorityTheProjectCannotResolve(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	runPriorityClient(t, "a task at an unresolvable priority", "/tasks/"+task.ID, projectPriorities(t), []core.Task{task}, `
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the task form has no priority select");
  if (control.value !== "") {
    throw new Error("the form shows the priority as " + JSON.stringify(control.value) +
      ", which is a priority nobody chose for this task");
  }
  const placeholder = control.firstElementChild;
  if (!placeholder || !placeholder.disabled) {
    throw new Error("the select offers no placeholder, so it falls to " + JSON.stringify(control.children[0].value));
  }
  if (placeholder.textContent !== `+strconv.Quote(strandedPlaceholder(string(strandedPriority)))+`) {
    throw new Error("the placeholder does not lead with the priority the server holds: " + JSON.stringify(placeholder.textContent));
  }
  const offered = control.children.filter((option) => option.value !== "").map((option) => option.value);
  const want = ["urgent", "high", "soon", "low"];
  if (JSON.stringify(offered) !== JSON.stringify(want)) {
    throw new Error("the project's own priorities did not survive the placeholder: " + JSON.stringify(offered));
  }
`)
}

// Saving that form without touching the priority leaves the task at the
// priority it holds.
//
// This is the harm the placeholder exists to prevent, so it is asserted against
// what the save stores rather than against what the select displays: the
// request the client actually built is carried back into the server that would
// answer it, and the task is read afterwards. An untouched placeholder sends
// nothing about the priority, so the stored value is the one the teammate wrote.
func TestHandlerClientSavingAStrandedPriorityDoesNotReassignIt(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	const renamed = "Renamed, and nothing else"
	output := runPriorityClientReporting(t, "save a form at an unresolvable priority", "/tasks/"+task.ID,
		projectPriorities(t), []core.Task{task}, `
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  if (!form || !title) throw new Error("the task form did not render");
  // The one field the reader went near. The priority control is left exactly as
  // the form drew it.
  title.value = `+strconv.Quote(renamed)+`;
  await form.eventListeners.submit({ preventDefault() {} });
  const wrote = fetchCalls.find((call) => call.options && call.options.method === "PATCH");
  if (!wrote) throw new Error("the save sent nothing");
  console.log("SAVE-BODY " + wrote.options.body);
`)
	body := reportedLine(t, output, "SAVE-BODY ")

	// The store this save lands in, holding the task as the teammate filed it.
	stored := task
	handler := NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) { return []core.Task{stored}, nil },
		Update: func(_ context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			if id != task.ID {
				t.Fatalf("the save named task %q, want %q", id, task.ID)
			}
			if input.Title != nil {
				stored.Title = *input.Title
			}
			if input.Priority != nil {
				stored.Priority = *input.Priority
			}
			return core.MutationResult{Task: stored}, nil
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+task.ID, body)
	if response.Code != http.StatusOK {
		t.Fatalf("the save was refused (%d), which is not the failure this test is about: %s",
			response.Code, response.Body.String())
	}
	if stored.Priority != strandedPriority {
		t.Errorf("saving the form reassigned the task's priority to %q; it was filed at %q and the reader never touched the control",
			stored.Priority, strandedPriority)
	}
	if stored.Title != renamed {
		t.Errorf("the save did not carry the edit the reader made: title = %q, want %q", stored.Title, renamed)
	}
}

// Once the task holds a priority this project has, the placeholder is gone.
//
// A placeholder naming a priority the task has since left is a worse label than
// none, so it is removed rather than left in the list. This is the path that
// proves it: a form open when a board intent for its task is refused is
// corrected in place rather than rebuilt, against the version the board holds —
// which here is one a teammate has since moved onto a priority this project
// does define. The save afterwards says nothing about the priority either,
// because the baseline moved with the control.
func TestHandlerClientDropsThePriorityPlaceholderOnceTheProjectHasTheValue(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	// What the board holds by the time the refusal lands: the drop never
	// applied, and the priority the task was stranded at is one the project
	// defines now.
	settled := task
	settled.Priority = core.Priority("urgent")
	settled.Head = "head-b"
	truth := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{settled}, Presentation: presentationForTasks([]core.Task{settled}),
	})

	runPriorityClient(t, "a stranded priority the project catches up with", "/",
		projectPriorities(t), []core.Task{task}, `
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  if (!inProgress) throw new Error("the board rendered no In Progress column");

  const boardFetch = globalThis.fetch;
  const bodies = [];
  let releaseIntent;
  let refuseIntent = true;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") {
      fetchCalls.push({ url, options });
      if (refuseIntent) {
        return new Promise((resolve) => {
          releaseIntent = () => {
            taskResponse = `+string(truth)+`;
            resolve({ ok: false, json: async () => ({
              format: "workbook.error", version: 1,
              error: { category: "stale-write", message: "task has changed since head-a; reload and try again" }
            }) });
          };
        });
      }
      bodies.push(JSON.parse(options.body));
      return { ok: true, json: async () => ({
        format: "workbook.task-mutation", version: 1, task: `+string(mustJSON(t, settled))+`
      }) };
    }
    return boardFetch(url, options);
  };

  const card = boardCard(`+strconv.Quote(task.ID)+`);
  if (!card) throw new Error("the board drew no card for the stranded task");
  card.rect = { top: 0, bottom: 80 };
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
  documentEventListeners.dragstart({ target: card, dataTransfer });
  const pending = documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  await Promise.resolve();
  documentEventListeners.dragend({ target: card });

  const link = new TestElement("a");
  link.href = "/tasks/" + encodeURIComponent(`+strconv.Quote(task.ID)+`);
  await documentEventListeners.click({
    target: link, button: 0, defaultPrevented: false,
    metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
    preventDefault() {}
  });
  const control = findElement(main, (element) => element.id === "task-priority");
  if (!control) throw new Error("the task form has no priority select");
  if (!control.firstElementChild || !control.firstElementChild.disabled || control.value !== "") {
    throw new Error("the form did not open on a placeholder, so this test would prove nothing");
  }

  releaseIntent();
  await pending;

  const shown = findElement(main, (element) => element.id === "task-priority");
  if (shown !== control) throw new Error("the correction rebuilt the form instead of correcting it");
  if (shown.value !== "urgent") {
    throw new Error("the field did not adopt the priority the task now holds: " + JSON.stringify(shown.value));
  }
  const leftover = shown.children.filter((option) => option.value === "");
  if (leftover.length !== 0) {
    throw new Error("the placeholder outlived the priority it named: " + JSON.stringify(leftover.map((option) => option.textContent)));
  }

  // The baseline moved with the control, so an untouched priority still sends
  // nothing — including no re-assertion of the value the correction brought in.
  refuseIntent = false;
  const form = findElement(main, (element) => element.tagName === "FORM");
  const title = findElement(main, (element) => element.id === "task-title");
  title.value = "Renamed after the correction";
  await form.eventListeners.submit({ preventDefault() {} });
  if (!bodies.length) throw new Error("the save sent nothing");
  if ("priority" in bodies[0]) {
    throw new Error("the save re-asserted a priority the reader never touched: " + JSON.stringify(bodies[0]));
  }
`)
}

// strandedPlaceholder is what a select says about a value this project's
// vocabulary cannot resolve, status and priority alike.
func strandedPlaceholder(value string) string {
	return "Current: " + value + " (not in this project)"
}

// The status and priority placeholders say it the same way.
//
// The two vocabularies are deliberately symmetric, and the two placeholders
// are one sentence in two places. Fixing the clip in one and not the other
// left the board describing the same condition two different ways, so both are
// asserted from one form holding a task stranded on both.
func TestHandlerClientWordsBothStrandedPlaceholdersAlike(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	task.Status = strandedStatus
	runPriorityClient(t, "a task stranded on both vocabularies", "/tasks/"+task.ID, projectPriorities(t), []core.Task{task}, `
  const said = {};
  for (const name of ["status", "priority"]) {
    const control = findElement(main, (element) => element.id === "task-" + name);
    if (!control) throw new Error("the task form has no " + name + " select");
    const placeholder = control.firstElementChild;
    if (!placeholder || !placeholder.disabled || control.value !== "") {
      throw new Error("the " + name + " select did not open on a placeholder");
    }
    said[name] = placeholder.textContent;
  }
  const want = { status: `+strconv.Quote(strandedPlaceholder(string(strandedStatus)))+`, priority: `+strconv.Quote(strandedPlaceholder(string(strandedPriority)))+` };
  if (JSON.stringify(said) !== JSON.stringify(want)) {
    throw new Error("the placeholders read " + JSON.stringify(said) + ", want " + JSON.stringify(want));
  }
`)
}

// A card says when its priority is not one of this project's.
//
// The chip draws the token, and before this it drew a stranded token exactly
// as it drew a live one: CRITICAL read as a priority this project has. Statuses
// have a whole region for the same condition; a priority has no column to be
// moved out of, so the chip itself is marked — a data attribute the stylesheet
// draws in the warning family, a tooltip, and the card's own label, which is
// where a screen reader is told about the status case too.
//
// It follows the task rather than the first paint: a poll that settles the
// priority clears the mark on the same node, and one that strands it again
// puts it back.
func TestHandlerClientMarksAPriorityChipTheProjectCannotResolve(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	live := clientPlacementTask("WB-01J0000000000000000000F708", "Filed here", core.StatusReady, "urgent")
	live.Head = "head-a"
	settled := task
	settled.Priority = core.Priority("urgent")
	settled.Head = "head-b"
	settledDocument := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{settled, live}, Presentation: presentationForTasks([]core.Task{settled, live}),
	})
	strandedDocument := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, VocabularyHead: "head-1",
		Tasks: []core.Task{task, live}, Presentation: presentationForTasks([]core.Task{task, live}),
	})
	runPriorityClient(t, "a card at an unresolvable priority", "/", projectPriorities(t), []core.Task{task, live}, `
  const chipOf = (card) => findElement(card, (element) => hasClassToken(element, "priority"));
  const expectMarked = (card, label) => {
    const chip = chipOf(card);
    if (!chip) throw new Error("the card carries no priority chip");
    if (chip.textContent !== `+strconv.Quote(string(strandedPriority))+`) {
      throw new Error("the stranded chip stopped drawing its token: " + JSON.stringify(chip.textContent));
    }
    if (!("priorityUnresolved" in chip.dataset)) {
      throw new Error("the chip of a priority this project does not define is drawn like a live one: " + JSON.stringify(chip.dataset));
    }
    if (chip.getAttribute("title") !== "Priority critical is not one of this project's") {
      throw new Error("the stranded chip explains nothing on hover: " + JSON.stringify(chip.getAttribute("title")));
    }
    if (card.getAttribute("aria-label") !== label) {
      throw new Error("the card's label = " + JSON.stringify(card.getAttribute("aria-label")) + ", want " + JSON.stringify(label));
    }
  };
  const expectLive = (card, label) => {
    const chip = chipOf(card);
    if (!chip) throw new Error("the card carries no priority chip");
    if ("priorityUnresolved" in chip.dataset || chip.getAttribute("title") !== null) {
      throw new Error("a live priority's chip is marked as stranded: " + JSON.stringify([chip.dataset, chip.getAttribute("title")]));
    }
    if (card.getAttribute("aria-label") !== label) {
      throw new Error("the card's label = " + JSON.stringify(card.getAttribute("aria-label")) + ", want " + JSON.stringify(label));
    }
  };

  const card = boardCard(`+strconv.Quote(task.ID)+`);
  const neighbor = boardCard(`+strconv.Quote(live.ID)+`);
  if (!card || !neighbor) throw new Error("the board did not draw both cards");
  expectMarked(card, "Move task Filed by a teammate from ready, at the unrecognized priority critical");
  expectLive(neighbor, "Move task Filed here from ready");

  card.__witness = "stranded";
  taskResponse = `+string(settledDocument)+`;
  await intervalCallback();
  const settledCard = boardCard(`+strconv.Quote(task.ID)+`);
  if (settledCard !== card || settledCard.__witness !== "stranded") throw new Error("settling the priority rebuilt the card");
  expectLive(settledCard, "Move task Filed by a teammate from ready");

  taskResponse = `+string(strandedDocument)+`;
  await intervalCallback();
  expectMarked(boardCard(`+strconv.Quote(task.ID)+`), "Move task Filed by a teammate from ready, at the unrecognized priority critical");
`)
}

// The server's first paint marks the same chip the client would.
//
// The client rebuilds every server-drawn card once, so a card that disagreed
// would flip from live to stranded under the reader on the first poll. A live
// priority's chip is left exactly as it was drawn before any of this.
func TestBoardMarksAStrandedPriorityChipOnTheServerToo(t *testing.T) {
	t.Parallel()
	task := strandedPriorityTask()
	live := clientPlacementTask("WB-01J0000000000000000000F708", "Filed here", core.StatusReady, "urgent")
	response := request(t, priorityBoardHandler(projectPriorities(t), []core.Task{task, live}), http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<span class="priority priority--critical" data-priority-unresolved title="Priority critical is not one of this project's">critical</span>`,
		`aria-label="Move task Filed by a teammate from ready, at the unrecognized priority critical"`,
		`<span class="priority priority--urgent">urgent</span>`,
		`aria-label="Move task Filed here from ready"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the served board does not contain %s", want)
		}
	}
}

// What the mark looks like: the board's warning family, the same one the
// unknown-status region is drawn in, and nothing a priority is drawn in.
//
// A priority is a colored word or a filled chip, and no priority has a border,
// so the dashed one is what says this chip is not one of them. The ink is
// measured against the surface it sits on in both schemes rather than trusted,
// because the warning ink is the one these readings move most between them.
func TestStrandedPriorityChipIsDrawnInTheWarningFamily(t *testing.T) {
	t.Parallel()
	body := priorityInkBoardPage(t, fourPriorityVocabulary(t, ""), nil)
	const selector = ".priority[data-priority-unresolved] {"
	at := strings.Index(body, selector)
	if at < 0 {
		t.Fatalf("the stylesheet draws no stranded priority chip (%s)", selector)
	}
	rule := body[at+len(selector):]
	rule = rule[:strings.Index(rule, "}")]
	declarations := map[string]string{}
	for _, declaration := range strings.Split(rule, ";") {
		if property, value, found := strings.Cut(declaration, ":"); found {
			declarations[strings.TrimSpace(property)] = strings.TrimSpace(value)
		}
	}
	want := map[string]string{
		"color":      "var(--wb-warning-ink)",
		"background": "var(--wb-warning-surface)",
		"border":     "1px dashed var(--wb-warning)",
	}
	for property, value := range want {
		if declarations[property] != value {
			t.Errorf("the stranded chip sets %s: %q, want %q", property, declarations[property], value)
		}
	}
	if strings.Contains(rule, "--wb-priority") {
		t.Errorf("the stranded chip borrows a priority's ink: %s", rule)
	}
	for _, scheme := range []string{"light", "dark"} {
		palette := schemePalette(scheme)
		ink := resolveColor(t, "var(--wb-warning-ink)", palette)
		surface := resolveColor(t, "var(--wb-warning-surface)", palette)
		if ratio := contrastRatio(t, ink, surface); ratio < chipContrastBar {
			t.Errorf("the stranded chip draws %s on %s at %.2f:1 in %s, under %.1f:1", ink, surface, ratio, scheme, chipContrastBar)
		}
		edge := resolveColor(t, "var(--wb-warning)", palette)
		card := palette["--wb-surface"]
		if ratio := contrastRatio(t, edge, card); ratio < chipSeparation {
			t.Errorf("the stranded chip's border %s sits on the %s card %s at %.2f:1, under %.1f:1", edge, scheme, card, ratio, chipSeparation)
		}
	}
}

// reportedLine pulls the one line the client script printed with this prefix,
// so a request the client built can be answered by the server in Go.
func reportedLine(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if reported, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			if !json.Valid([]byte(reported)) {
				t.Fatalf("the client reported %s%s, which is not a request body", prefix, reported)
			}
			return reported
		}
	}
	t.Fatalf("the client script printed no %q line:\n%s", prefix, output)
	return ""
}
