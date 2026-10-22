package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// requestIfNoneMatch is a GET that carries the tag a previous poll was given,
// which is the only thing the conditional route reads from the request beyond
// what an unconditional one does.
func requestIfNoneMatch(t *testing.T, handler http.Handler, target, tag string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("If-None-Match", tag)
	handler.ServeHTTP(response, request)
	return response
}

// firstPoll reads the tasks document unconditionally and returns its tag, after
// checking the headers every successful poll must carry.
func firstPoll(t *testing.T, handler http.Handler, target string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	response := request(t, handler, http.MethodGet, target)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d; body = %s", target, response.Code, http.StatusOK, response.Body.String())
	}
	tag := response.Header().Get("ETag")
	if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		t.Fatalf("GET %s ETag = %q, want a non-empty strong quoted tag", target, tag)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("GET %s Cache-Control = %q, want no-cache", target, got)
	}
	return response, tag
}

// The tag is the digest of the bytes the poll was sent, so every input that
// shapes the document reaches it and nothing else does. The first poll is the
// same document it always was; a repeat naming that tag is answered with the
// tag alone.
func TestTasksPollAnswersARepeatedTagWithNotModified(t *testing.T) {
	t.Parallel()
	tasks := boardTasks()[:2]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	first, tag := firstPoll(t, handler, "/api/tasks")
	if got := first.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("GET /api/tasks Content-Type = %q, want application/json; charset=utf-8", got)
	}
	digest := sha256.Sum256(first.Body.Bytes())
	if want := `"` + hex.EncodeToString(digest[:]) + `"`; tag != want {
		t.Errorf("GET /api/tasks ETag = %s, want the body's sha256 %s", tag, want)
	}
	var document TasksDocument
	if err := json.Unmarshal(first.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode tasks document: %v; body = %s", err, first.Body.String())
	}
	if document.Format != "workbook.tasks" || document.Version != 1 || !reflect.DeepEqual(document.Tasks, tasks) {
		t.Fatalf("tasks document = %#v, want workbook.tasks v1 with %#v", document, tasks)
	}

	repeat := requestIfNoneMatch(t, handler, "/api/tasks", tag)
	if repeat.Code != http.StatusNotModified {
		t.Fatalf("repeat poll status = %d, want %d", repeat.Code, http.StatusNotModified)
	}
	if repeat.Body.Len() != 0 {
		t.Errorf("repeat poll body = %q, want empty", repeat.Body.String())
	}
	if got := repeat.Header().Get("ETag"); got != tag {
		t.Errorf("repeat poll ETag = %q, want %q", got, tag)
	}
	if got := repeat.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("repeat poll Cache-Control = %q, want no-cache", got)
	}
}

// If-None-Match is a list compared weakly, and `*` names any representation
// the route has, so each of these is the poll's own tag and none of them is
// worth resending the document for. A tag the route did not give is.
func TestTasksPollMatchesIfNoneMatchTheWayHTTPDoes(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks()[:2], nil })
	_, tag := firstPoll(t, handler, "/api/tasks")

	for name, header := range map[string]string{
		"list":     `"stale", ` + tag + `, "other"`,
		"weak":     "W/" + tag,
		"wildcard": "*",
	} {
		response := requestIfNoneMatch(t, handler, "/api/tasks", header)
		if response.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s (%s) status = %d, want %d", name, header, response.Code, http.StatusNotModified)
		}
		if response.Body.Len() != 0 {
			t.Errorf("If-None-Match %s body = %q, want empty", name, response.Body.String())
		}
		if got := response.Header().Get("ETag"); got != tag {
			t.Errorf("If-None-Match %s ETag = %q, want %q", name, got, tag)
		}
	}

	miss := requestIfNoneMatch(t, handler, "/api/tasks", `"not-this-one"`)
	if miss.Code != http.StatusOK || miss.Body.Len() == 0 {
		t.Fatalf("non-matching If-None-Match status = %d with %d body bytes, want %d with a body", miss.Code, miss.Body.Len(), http.StatusOK)
	}
	if got := miss.Header().Get("ETag"); got != tag {
		t.Errorf("non-matching If-None-Match ETag = %q, want %q", got, tag)
	}
}

// A task that changed changes the document, so the tag the page holds no longer
// names what the board is and the poll is sent the new one.
func TestTasksPollResendsAfterATaskChanges(t *testing.T) {
	t.Parallel()
	tasks := boardTasks()[:2]
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	_, before := firstPoll(t, handler, "/api/tasks")

	changed := append([]core.Task(nil), tasks...)
	changed[0].Title = "Retitled while the page was polling"
	tasks = changed

	response := requestIfNoneMatch(t, handler, "/api/tasks", before)
	if response.Code != http.StatusOK {
		t.Fatalf("poll after a task change status = %d, want %d", response.Code, http.StatusOK)
	}
	after := response.Header().Get("ETag")
	if after == "" || after == before {
		t.Fatalf("ETag after a task change = %q, want one different from %q", after, before)
	}
	var document TasksDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode tasks document: %v; body = %s", err, response.Body.String())
	}
	if document.Tasks[0].Title != "Retitled while the page was polling" {
		t.Errorf("poll after a task change carried title %q, want the new one", document.Tasks[0].Title)
	}
}

// The columns the cards are drawn into are part of the document: a moved
// ledger head or a different set of statuses is a different board even when no
// task changed, and the page learns of it through the same poll.
func TestTasksPollResendsAfterTheVocabularyChanges(t *testing.T) {
	t.Parallel()
	original := handlerVocabulary(t)
	shape := original.Document()
	reshaped, err := core.NewVocabulary(
		append(append([]core.StatusDefinition(nil), shape.Statuses...),
			core.StatusDefinition{Status: "parked", Label: "Parked", Rank: "4/1", Tags: []core.StatusTag{}}),
		shape.Aliases,
		shape.Retired,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	state := VocabularyState{Vocabulary: original, Head: "head-1"}
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) { return state, nil },
		List:       func(context.Context) ([]core.Task, error) { return boardTasks()[:2], nil },
	})

	_, original1 := firstPoll(t, handler, "/api/tasks")

	state = VocabularyState{Vocabulary: original, Head: "head-2"}
	moved := requestIfNoneMatch(t, handler, "/api/tasks", original1)
	if moved.Code != http.StatusOK {
		t.Fatalf("poll after the head moved status = %d, want %d", moved.Code, http.StatusOK)
	}
	movedTag := moved.Header().Get("ETag")
	if movedTag == "" || movedTag == original1 {
		t.Fatalf("ETag after the head moved = %q, want one different from %q", movedTag, original1)
	}

	state = VocabularyState{Vocabulary: reshaped, Head: "head-2"}
	reshapedResponse := requestIfNoneMatch(t, handler, "/api/tasks", movedTag)
	if reshapedResponse.Code != http.StatusOK {
		t.Fatalf("poll after the statuses changed status = %d, want %d", reshapedResponse.Code, http.StatusOK)
	}
	if got := reshapedResponse.Header().Get("ETag"); got == "" || got == movedTag || got == original1 {
		t.Fatalf("ETag after the statuses changed = %q, want one different from %q and %q", got, movedTag, original1)
	}
}

// The three answers to `deleted=` are three documents, so each has its own tag
// and none of them stands in for another: a page that switched its Deleted
// column on must not be told the board it has not read is the one it holds.
func TestTasksPollTagsEachDeletedVariantSeparately(t *testing.T) {
	t.Parallel()
	active := boardTasks()[0]
	deleted := boardTasks()[1]
	deleted.Deleted = true
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{active, deleted}, nil })

	targets := []string{"/api/tasks", "/api/tasks?deleted=true", "/api/tasks?deleted=include"}
	tags := make(map[string]string, len(targets))
	seen := make(map[string]string, len(targets))
	for _, target := range targets {
		_, tag := firstPoll(t, handler, target)
		if other, duplicate := seen[tag]; duplicate {
			t.Fatalf("GET %s and GET %s share ETag %s, want distinct tags", target, other, tag)
		}
		seen[tag] = target
		tags[target] = tag
	}

	for _, from := range targets {
		for _, to := range targets {
			if from == to {
				continue
			}
			response := requestIfNoneMatch(t, handler, to, tags[from])
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Errorf("GET %s with the tag of %s status = %d with %d body bytes, want %d with a body", to, from, response.Code, response.Body.Len(), http.StatusOK)
			}
			if got := response.Header().Get("ETag"); got != tags[to] {
				t.Errorf("GET %s with the tag of %s ETag = %q, want %q", to, from, got, tags[to])
			}
		}
	}
}

// A failed read has no representation to tag. It keeps the status and error
// document it always had, and a page holding a tag is told of the failure rather
// than that nothing changed.
func TestTasksPollErrorCarriesNoTag(t *testing.T) {
	t.Parallel()
	failing := listHandler(t, func(context.Context) ([]core.Task, error) {
		return nil, core.Errorf(core.CategoryCorruptData, "cannot read the task refs")
	})

	for _, header := range []string{"", "*", `"anything"`} {
		response := requestIfNoneMatch(t, failing, "/api/tasks", header)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("failed listing (If-None-Match %q) status = %d, want %d", header, response.Code, http.StatusInternalServerError)
		}
		if got := response.Header().Get("ETag"); got != "" {
			t.Errorf("failed listing (If-None-Match %q) ETag = %q, want none", header, got)
		}
		var document ErrorDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
		}
		if document.Format != "workbook.error" || document.Error.Category != core.CategoryCorruptData {
			t.Errorf("failed listing document = %#v, want a workbook.error of category %s", document, core.CategoryCorruptData)
		}
	}
}

// The document carries nothing that changes on the clock. An assignment's row
// says how long ago it was recorded, and that phrase moves on its own — from
// "just now" to "1 minute ago" here — but the page words it from the recorded
// time and a rule that does not move, so an idle board holding a fresh
// assignment is told nothing changed on every poll, either side of the
// boundary.
//
// It waits for the real clock rather than injecting one, because what is being
// proven is that no clock reaches the document: a route that read one anywhere
// would be the route this test catches.
func TestTasksPollTagHoldsStillAcrossAnAssignmentAgeBoundary(t *testing.T) {
	t.Parallel()
	recorded := time.Now().Add(-time.Minute + 1500*time.Millisecond)
	task := boardTasks()[0]
	task.Assignments = []core.Assignment{
		{Principal: "agent@example.com", Label: "impl-1", Creator: "agent@example.com", CreatedAt: recorded},
	}
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })

	_, tag := firstPoll(t, handler, "/api/tasks")
	if time.Since(recorded) >= time.Minute {
		t.Fatal("the first poll landed after the boundary it was meant to precede, so this run proves nothing")
	}
	if repeat := requestIfNoneMatch(t, handler, "/api/tasks", tag); repeat.Code != http.StatusNotModified {
		t.Fatalf("an idle poll before the boundary status = %d, want %d", repeat.Code, http.StatusNotModified)
	}

	time.Sleep(time.Until(recorded.Add(time.Minute + 100*time.Millisecond)))
	response := requestIfNoneMatch(t, handler, "/api/tasks", tag)
	if response.Code != http.StatusNotModified {
		t.Fatalf("an idle poll after \"assigned just now\" became \"assigned 1 minute ago\" status = %d, want %d; tag %q became %q",
			response.Code, http.StatusNotModified, tag, response.Header().Get("ETag"))
	}
	if got := response.Header().Get("ETag"); got != tag {
		t.Fatalf("ETag across the boundary = %q, want %q", got, tag)
	}
}
