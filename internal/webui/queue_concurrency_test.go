package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// One task's intents are serialized; two tasks' are not. The serialization test
// beside this one proves the first half by watching a single card, and would
// pass just as well over a queue that serialized the whole board — which would
// make a slow write to one card stall every other card on it.
//
// This watches two cards instead: both writes have to be open at once.
func TestHandlerClientDrainsDifferentTasksConcurrently(t *testing.T) {
	node := requireNode(t)
	first := clientPlacementTask("WB-01J00000000000000000000301", "First", core.StatusReady, core.PriorityMedium)
	first.Head = "head-first"
	second := clientPlacementTask("WB-01J00000000000000000000302", "Second", core.StatusReady, core.PriorityMedium)
	second.Head = "head-second"
	tasks := []core.Task{first, second}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	script := renderedClientScript(t, response.Body.String())
	document, err := json.Marshal(TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	if err != nil {
		t.Fatal(err)
	}

	program := clientDOMHarness("/", string(document)) + script + `
setTimeout(async () => {
  const ready = boardLists.find((list) => list.dataset.status === "ready");
  const inProgress = boardLists.find((list) => list.dataset.status === "in-progress");
  const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

  let open = 0;
  let maxOpen = 0;
  const releases = [];
  globalThis.fetch = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    if ((options.method || "GET") !== "GET") {
      open += 1;
      maxOpen = Math.max(maxOpen, open);
      return new Promise((resolve) => {
        releases.push(() => {
          open -= 1;
          resolve({ ok: true, json: async () => ({
            format: "workbook.task-mutation", version: 1,
            task: Object.assign({}, JSON.parse(options.body).expectedHead === "head-first"
              ? ` + string(mustJSON(t, first)) + `
              : ` + string(mustJSON(t, second)) + `, { status: "in-progress", head: "head-next" }) }) });
        });
      });
    }
    return { ok: true, json: async () => (` + string(document) + `) };
  };

  const drag = (taskID) => {
    const card = boardCard(taskID);
    card.rect = { top: 0, bottom: 80 };
    documentEventListeners.dragstart({ target: card, dataTransfer });
    return documentEventListeners.drop({ target: inProgress, clientY: 1, dataTransfer, preventDefault() {} });
  };
  const drops = [drag(` + strconv.Quote(first.ID) + `), drag(` + strconv.Quote(second.ID) + `)];
  // Let both sends reach the fetch above before either is answered.
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (maxOpen !== 2) {
    throw new Error("two tasks' writes did not overlap; max concurrent writes was " + maxOpen);
  }
  releases.forEach((release) => release());
  await Promise.all(drops);
  const written = fetchCalls.filter(({ options }) => (options.method || "GET") !== "GET");
  if (written.length !== 2) throw new Error("two drags sent " + written.length + " writes");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute concurrent task queue behavior: %v\n%s", err, output)
	}
}
