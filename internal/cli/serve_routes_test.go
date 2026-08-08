package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// runServe hands the handler twelve function literals. They are named fields
// now rather than a positional list, which makes a transposed pair visible, but
// naming cannot make it impossible: Delete and Restore still share a signature,
// so a swap between those two fields compiles, and every handler-level test
// keeps passing against its own injected fakes. Only a test through the real
// wiring fails. These are those tests for delete, restore, and history; the
// dependency pair is covered by TestRunServeMutatesDependenciesThroughWebRoutes.
func TestRunServeDeletesAndRestoresThroughWebRoutes(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Deleted through the board", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	addr := startServeBoard(t, repository)

	body, status := boardRequest(t, http.MethodDelete, "http://"+addr+"/api/tasks/"+task.ID, "")
	deleted := decodeServeMutation(t, body, status)
	if !deleted.Deleted || deleted.ID != task.ID {
		t.Fatalf("DELETE /api/tasks/%s returned %#v, want that task tombstoned", task.ID, deleted)
	}
	if head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+task.ID); head == task.Head {
		t.Fatal("DELETE did not advance the task ref")
	}
	assertTaskDeleted(t, repository, task.ID, true)

	// The board reads deleted tasks through the same list route it renders
	// /deleted from, so the tombstone has to be visible there and gone from the
	// active board.
	if ids := boardTaskIDs(t, "http://"+addr+"/api/tasks"); contains(ids, task.ID) {
		t.Fatalf("active board still lists tombstoned task %s: %v", task.ID, ids)
	}
	if ids := boardTaskIDs(t, "http://"+addr+"/api/tasks?deleted=true"); !contains(ids, task.ID) {
		t.Fatalf("deleted board does not list tombstoned task %s: %v", task.ID, ids)
	}

	body, status = boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks/"+task.ID+"/restore", "")
	restored := decodeServeMutation(t, body, status)
	if restored.Deleted || restored.ID != task.ID {
		t.Fatalf("POST /api/tasks/%s/restore returned %#v, want that task active", task.ID, restored)
	}
	assertTaskDeleted(t, repository, task.ID, false)
	if ids := boardTaskIDs(t, "http://"+addr+"/api/tasks"); !contains(ids, task.ID) {
		t.Fatalf("active board does not list restored task %s: %v", task.ID, ids)
	}
}

// The board's history route is wired to read the whole chain rather than the
// ten-change window `workbook show --history` defaults to, because the detail
// view derives a status lane that reaches back to the task's creation. Only a
// task with more than ten changes can tell the two wirings apart.
func TestRunServeReadsWholeTaskHistoryThroughWebRoute(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "History through the board", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	const updates = 12
	for index := range updates {
		title := fmt.Sprintf("History through the board %d", index)
		if code, _, stderr := run(t, repository, "update", task.ID, "--title", title); code != 0 {
			t.Fatalf("update %d code = %d, want 0; stderr = %q", index, code, stderr)
		}
	}
	// Status changes drive the lifecycle lane the detail view leads with.
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "in-progress"); code != 0 {
		t.Fatalf("status update code = %d, want 0; stderr = %q", code, stderr)
	}
	addr := startServeBoard(t, repository)

	body, status := boardRequest(t, http.MethodGet, "http://"+addr+"/api/tasks/"+task.ID+"/history", "")
	if status != http.StatusOK {
		t.Fatalf("GET history = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	var document webui.TaskHistoryDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode history document: %v; body = %s", err, body)
	}
	if document.Format != "workbook.task-history" || document.Version != 1 || document.TaskID != task.ID {
		t.Fatalf("history envelope = %#v, want a versioned document for %s", document, task.ID)
	}
	wantTotal := updates + 2
	if document.History.Total != wantTotal || document.History.Showing != wantTotal || document.History.Truncated != nil {
		t.Fatalf("history window = %d of %d (truncated %#v), want the whole chain of %d",
			document.History.Showing, document.History.Total, document.History.Truncated, wantTotal)
	}
	if len(document.Lifecycle) == 0 {
		t.Fatalf("history lifecycle = %#v, want the status lane the detail view leads with", document.Lifecycle)
	}
	if document.History.Changes[0].Commit == "" {
		t.Fatalf("history changes = %#v, want commit object IDs", document.History.Changes[0])
	}
}

// startServeBoard runs the real serve command on an ephemeral loopback port and
// stops it when the test ends. Port zero matters: another suite may be running
// on this machine, and a fixed port would make the two collide.
func startServeBoard(t *testing.T, repository string) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runServe() error = %v; stderr = %q", err, serveStderr.String())
		}
		if serveStdout.Len() != 0 {
			t.Errorf("serve stdout = %q, want empty", serveStdout.String())
		}
	})
	waitForHTTP(t, "http://"+addr+"/healthz")
	return addr
}

// boardRequest speaks to the board the way its own page does: every mutation
// declares JSON, because the same-origin guard refuses one that does not.
func boardRequest(t *testing.T, method, url, body string) ([]byte, int) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	contents, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return contents, response.StatusCode
}

func decodeServeMutation(t *testing.T, body []byte, status int) core.Task {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("board mutation = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	var document struct {
		Format string    `json:"format"`
		Task   core.Task `json:"task"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode board mutation: %v; body = %s", err, body)
	}
	if document.Format != "workbook.task-mutation" {
		t.Fatalf("board mutation format = %q, want workbook.task-mutation", document.Format)
	}
	return document.Task
}

func boardTaskIDs(t *testing.T, url string) []string {
	t.Helper()
	body, status := boardRequest(t, http.MethodGet, url, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d; body = %s", url, status, http.StatusOK, body)
	}
	var document struct {
		Tasks []core.Task `json:"tasks"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode task list: %v; body = %s", err, body)
	}
	ids := make([]string, len(document.Tasks))
	for index, task := range document.Tasks {
		ids[index] = task.ID
	}
	return ids
}

func assertTaskDeleted(t *testing.T, repository, taskID string, want bool) {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", taskID, "--json")
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &task); err != nil {
		t.Fatalf("decode shown task: %v", err)
	}
	if task.Deleted != want {
		t.Fatalf("stored task %s deleted = %t, want %t", taskID, task.Deleted, want)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
