package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// An assignment row on the task page says when the assignment was recorded and
// nothing about how long ago that was. The relative phrase — "assigned 5
// minutes ago" — used to be worded by the page from a rule the server rendered
// into it, and re-worded on every tick; it had no other job (nothing warned on
// a stale assignment, and nothing read the phrase back), so it went with the
// line it was on. `workbook show` still prints it, from presentation.AssignedAgo.

// The page is served no wording rule, because nothing on it words an age.
func TestPageServesNoAssignedAgoRule(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, path := range []string{"/", "/tasks/" + assignedTaskID} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		if strings.Contains(response.Body.String(), "data-assigned-ago") {
			t.Fatalf("GET %s still renders the assigned-ago rule into the page", path)
		}
	}
}

// Every row's metadata is the recorded time, exactly as the page formats a
// recorded time anywhere else, at every age from the future to a year back —
// no " · " and no "ago" after it.
func TestClientShowsAssignmentTimesWithoutARelativeAge(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	ages := []time.Duration{
		-5 * time.Second, 0, 30 * time.Second, time.Minute, 59 * time.Minute,
		time.Hour, 23 * time.Hour, 24 * time.Hour, 48 * time.Hour, 400 * 24 * time.Hour,
	}
	task := clientPlacementTask(assignedTaskID, "Aged task", core.StatusReady, core.PriorityMedium)
	stamps := make([]string, len(ages))
	for index, age := range ages {
		created := now.Add(-age)
		task.Assignments = append(task.Assignments, core.Assignment{
			Principal: "agent" + strconv.Itoa(index) + "@example.com",
			Creator:   "lead@example.com",
			CreatedAt: created,
		})
		stamps[index] = created.Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(stamps)
	if err != nil {
		t.Fatal(err)
	}
	program := assignmentPageProgram(t, []core.Task{task}, `
Date.now = () => `+strconv.FormatInt(now.UnixMilli(), 10)+`;
const stamps = `+string(encoded)+`;
setTimeout(() => {
  const rows = assignmentRows();
  if (rows.length !== stamps.length) throw new Error("assignment rows = " + rows.length + ", want " + stamps.length);
  const wrong = [];
  rows.forEach((row, index) => {
    const meta = assignmentText(row)[1];
    const want = new Date(stamps[index]).toLocaleString();
    if (meta !== want) wrong.push(index + ": " + JSON.stringify(meta) + ", want " + JSON.stringify(want));
    if (/\bago\b|just now/.test(row.textContent)) wrong.push(index + " still words an age: " + JSON.stringify(row.textContent));
  });
  if (wrong.length) throw new Error("assignment rows are not their recorded times alone:\n" + wrong.join("\n"));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the assignment time check: %v\n%s", err, output)
	}
}

// An open page's rows stay exactly as drawn while the board is idle. Every poll
// is answered 304 — nothing about the task changed, so nothing is sent — and a
// day of ticks later the row is the same node with the same words: nothing on
// the page moves with the clock any more, so nothing rewrites it.
func TestClientAssignmentRowsStayAsDrawnWhilePollsAnswerNotModified(t *testing.T) {
	t.Parallel()
	// The harness's clock starts here, in milliseconds.
	const clockStart = 1000000
	task := clientPlacementTask(conditionalPollTaskID, "Freshly claimed", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Assignments = []core.Assignment{{
		Principal: "agent@example.com", Label: "impl-1", Creator: "agent@example.com",
		CreatedAt: time.UnixMilli(clockStart - 30_000).UTC(),
	}}
	program := conditionalPollProgram(t, "/tasks/"+task.ID, []core.Task{task}, "", `
  if (clock !== `+strconv.Itoa(clockStart)+`) throw new Error("the harness clock moved; this test's stamps assume it starts at `+strconv.Itoa(clockStart)+`");
  const rows = () => {
    const panel = findElement(main, (element) => element.dataset.taskPanel === "assignments");
    if (!panel) throw new Error("the page is drawing no assignments section");
    return findElement(panel, (element) => hasDataKey(element, "panelList")).children;
  };
  const drawn = rows()[0];
  if (!drawn) throw new Error("the assignment was never drawn");
  const meta = drawn.children[1];
  const words = meta.textContent;
  if (words !== new Date(`+strconv.Itoa(clockStart-30_000)+`).toLocaleString()) {
    throw new Error("the row's metadata is not its recorded time: " + JSON.stringify(words));
  }

  for (const step of [31000, 60000, 58 * 60000, 23 * 3600000, 24 * 3600000]) {
    clock += step;
    await intervalCallback();
    const answer = answers[answers.length - 1];
    if (lastRead("/api/tasks").tag !== taskTag || answer.status !== 304) {
      throw new Error("the poll was not answered 304, so this test proves nothing: " + JSON.stringify(lastRead("/api/tasks")));
    }
    if (answer.jsonCalls !== 0) throw new Error("the page read a body out of a 304");
    const current = rows();
    if (current.length !== 1 || current[0] !== drawn || current[0].children[1] !== meta) {
      throw new Error("an idle tick rebuilt the row");
    }
    if (meta.textContent !== words) {
      throw new Error("after " + step + "ms more the row reads " + JSON.stringify(meta.textContent) + ", want " + JSON.stringify(words));
    }
  }
`)
	runConditionalPollClient(t, "rows idle behind 304s", program)
}
