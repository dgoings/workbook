package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

// How long an assignment has stood is worded on the page, from the time it was
// recorded and a rule the server sends as data. The rule is
// presentation.AssignedAgo's — the one `workbook show` prints through — so what
// is pinned here is that the page is handed that rule and nothing else, and that
// it reads it the way Go does.

// servedAssignedAgo reads the wording rule out of a page the handler rendered,
// the way the script reads it: one attribute on the board view.
func servedAssignedAgo(t *testing.T, page string) []pageAgeStep {
	t.Helper()
	const open = `data-assigned-ago="`
	start := strings.Index(page, open)
	if start < 0 {
		t.Fatal("the rendered page carries no data-assigned-ago attribute")
	}
	rest := page[start+len(open):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatal("the data-assigned-ago attribute is not closed")
	}
	var steps []pageAgeStep
	if err := json.Unmarshal([]byte(html.UnescapeString(rest[:end])), &steps); err != nil {
		t.Fatalf("decode the served wording rule %q: %v", rest[:end], err)
	}
	return steps
}

// The table the page is served is presentation's own, step for step, in the
// milliseconds a browser's clock counts in. A bound that was not a whole number
// of milliseconds could not be carried exactly, so that is refused here rather
// than rounded somewhere a reader would never see it.
func TestPageServesTheAssignedAgoRule(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, path := range []string{"/", "/tasks/" + assignedTaskID} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		served := servedAssignedAgo(t, response.Body.String())
		rule := presentation.AssignedAgoSteps()
		if len(served) != len(rule) {
			t.Fatalf("GET %s served %d steps, want presentation's %d: %+v", path, len(served), len(rule), served)
		}
		for index, step := range rule {
			for name, value := range map[string]time.Duration{"Below": step.Below, "Unit": step.Unit} {
				if value%time.Millisecond != 0 {
					t.Fatalf("step %d %s = %v is not a whole number of milliseconds", index, name, value)
				}
			}
			want := pageAgeStep{
				Below: step.Below.Milliseconds(),
				Unit:  step.Unit.Milliseconds(),
				One:   step.One,
				Many:  step.Many,
			}
			if served[index] != want {
				t.Fatalf("GET %s step %d = %+v, want %+v", path, index, served[index], want)
			}
		}
	}
}

// The page words an age exactly as Go does, at every boundary and either side of
// it. Each row's phrase is compared with presentation.AssignedAgo asked about
// the same assignment at the same instant, so this fails the day either side
// reads the table differently — or the day the page stops reading it.
func TestClientWordsAssignmentAgesByTheGoRule(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	const ms = time.Millisecond
	ages := []time.Duration{
		-5 * time.Second, 0, ms,
		time.Minute - ms, time.Minute, time.Minute + ms,
		2*time.Minute - ms, 2 * time.Minute, 59*time.Minute + 59*time.Second,
		time.Hour - ms, time.Hour, time.Hour + ms,
		2*time.Hour - ms, 2 * time.Hour,
		24*time.Hour - ms, 24 * time.Hour, 24*time.Hour + ms,
		48*time.Hour - ms, 48 * time.Hour,
		30 * 24 * time.Hour, 400 * 24 * time.Hour,
	}
	task := clientPlacementTask(assignedTaskID, "Aged task", core.StatusReady, core.PriorityMedium)
	want := make([]string, len(ages))
	for index, age := range ages {
		assignment := core.Assignment{
			Principal: fmt.Sprintf("agent%02d@example.com", index),
			Creator:   "lead@example.com",
			CreatedAt: now.Add(-age),
		}
		task.Assignments = append(task.Assignments, assignment)
		want[index] = presentation.AssignedAgo(assignment, now)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	program := assignmentPageProgram(t, []core.Task{task}, `
Date.now = () => `+strconv.FormatInt(now.UnixMilli(), 10)+`;
const want = `+string(encoded)+`;
setTimeout(() => {
  const rows = assignmentRows();
  if (rows.length !== want.length) throw new Error("assignment rows = " + rows.length + ", want " + want.length);
  const wrong = [];
  rows.forEach((row, index) => {
    const meta = assignmentText(row)[1];
    const phrase = meta.slice(meta.lastIndexOf(" · ") + 3);
    if (phrase !== want[index]) wrong.push(index + ": page " + JSON.stringify(phrase) + ", Go " + JSON.stringify(want[index]));
  });
  if (wrong.length) throw new Error("the page and presentation.AssignedAgo disagree:\n" + wrong.join("\n"));
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the wording sweep: %v\n%s", err, output)
	}
}

// An open page's rows go on aging while the board is idle. Every poll is
// answered 304 — nothing about the task changed, so nothing is sent — and the
// phrase still moves from "just now" through minutes, hours and days, on the
// page's own tick, in the rows already drawn.
func TestClientAssignmentWordingAdvancesWhilePollsAnswerNotModified(t *testing.T) {
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
  const phrase = (row) => {
    const meta = row.children[1].textContent;
    return meta.slice(meta.lastIndexOf(" · ") + 3);
  };
  const drawn = rows()[0];
  if (!drawn) throw new Error("the assignment was never drawn");
  if (phrase(drawn) !== "assigned just now") throw new Error("a 30-second-old assignment reads " + JSON.stringify(phrase(drawn)));

  for (const [step, want] of [
    [31000, "assigned 1 minute ago"],
    [60000, "assigned 2 minutes ago"],
    [58 * 60000, "assigned 1 hour ago"],
    [23 * 3600000, "assigned 1 day ago"],
    [24 * 3600000, "assigned 2 days ago"]
  ]) {
    clock += step;
    await intervalCallback();
    const answer = answers[answers.length - 1];
    if (lastRead("/api/tasks").tag !== taskTag || answer.status !== 304) {
      throw new Error("the poll was not answered 304, so this test proves nothing: " + JSON.stringify(lastRead("/api/tasks")));
    }
    if (answer.jsonCalls !== 0) throw new Error("the page read a body out of a 304");
    const current = rows();
    if (current.length !== 1 || current[0] !== drawn) throw new Error("re-wording rebuilt the row it should have written into");
    if (phrase(drawn) !== want) {
      throw new Error("after " + step + "ms more the row reads " + JSON.stringify(phrase(drawn)) + ", want " + JSON.stringify(want));
    }
  }
`)
	runConditionalPollClient(t, "aging behind 304s", program)
}
