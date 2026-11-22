package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// serverDetail is the kind of text a failed Git command hands back: the
// server's folder, the remote's address and Git's own words.
const serverDetail = "git push origin refs/workbook/tasks/x failed: exit status 128: fatal: '/srv/boards/acme/missing.git' does not appear to be a git repository"

// lockedBuffer is a server log several requests may write at once.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (log *lockedBuffer) Write(p []byte) (int, error) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return log.buffer.Write(p)
}

func (log *lockedBuffer) String() string {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return log.buffer.String()
}

// hostedOptions is a board whose reads fail with server detail and whose
// creates succeed with a publication warning carrying it, so one handler shows
// both halves of what a hosted board keeps out of its answers.
func hostedOptions(t *testing.T, listErr error) Options {
	t.Helper()
	task := threadRouteTask()
	return Options{
		List: func(context.Context) ([]core.Task, error) {
			if listErr != nil {
				return nil, listErr
			}
			return []core.Task{task}, nil
		},
		Create: func(context.Context, core.CreateInput) (core.MutationResult, error) {
			return core.MutationResult{Task: task, Warnings: []core.Warning{
				{Code: core.WarningAutoSync, Message: "the change was recorded locally, but publishing it failed: " + serverDetail},
				{Code: core.WarningAssignmentShared, Message: "WB-01 is also assigned to sam@example.com"},
			}}, nil
		},
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Attachment: func(_ context.Context, id, attachmentID string) (core.Attachment, error) {
			for _, attachment := range task.Attachments {
				if attachment.ID == attachmentID {
					return attachment, nil
				}
			}
			return core.Attachment{}, core.Errorf(core.CategoryNotFound, "task %s has no attachment %s", id, attachmentID)
		},
		AttachmentContent: func(context.Context, core.Attachment) ([]byte, error) {
			return []byte("hello world"), nil
		},
	}
}

var referencePattern = regexp.MustCompile(`reference ([0-9a-f]{12})\b`)

func TestHostedBoardKeepsServerDetailOutOfAnErrorAndLogsIt(t *testing.T) {
	t.Parallel()
	log := &lockedBuffer{}
	options := hostedOptions(t, core.Wrap(core.CategoryOperational, "list tasks", errors.New(serverDetail)))
	options.Hosted = true
	options.ServerLog = log
	response := requestJSON(t, NewHandler(options), http.MethodGet, "/api/tasks", "")

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, leaked := range []string{"/srv/boards", "missing.git", "fatal:", "git push", "list tasks"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("hosted answer carries %q: %s", leaked, body)
		}
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode: %v; body = %s", err, body)
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryOperational {
		t.Fatalf("envelope = %#v, want workbook.error v1, operational", document)
	}
	match := referencePattern.FindStringSubmatch(document.Error.Message)
	if match == nil || document.Error.Reference != match[1] {
		t.Fatalf("message %q and reference %q, want one sentence naming the reference it carries", document.Error.Message, document.Error.Reference)
	}
	logged := log.String()
	if !strings.Contains(logged, match[1]) || !strings.Contains(logged, serverDetail) || !strings.Contains(logged, "list tasks") {
		t.Fatalf("server log = %q, want the reference %s beside the whole detail", logged, match[1])
	}
}

func TestHostedBoardKeepsCorruptDataDetailOut(t *testing.T) {
	t.Parallel()
	log := &lockedBuffer{}
	options := hostedOptions(t, core.Errorf(core.CategoryCorruptData, "operation blob in /srv/boards/acme/.git is unreadable"))
	options.Hosted = true
	options.ServerLog = log
	response := requestJSON(t, NewHandler(options), http.MethodGet, "/api/tasks", "")
	if strings.Contains(response.Body.String(), "/srv/boards") {
		t.Fatalf("hosted answer carries the server's folder: %s", response.Body.String())
	}
	if !strings.Contains(log.String(), "/srv/boards/acme/.git") {
		t.Fatalf("server log = %q, want the detail", log.String())
	}
}

func TestHostedBoardPassesARequestsOwnRefusalThrough(t *testing.T) {
	t.Parallel()
	log := &lockedBuffer{}
	options := hostedOptions(t, core.Errorf(core.CategoryValidation, "title must not be blank"))
	options.Hosted = true
	options.ServerLog = log
	response := requestJSON(t, NewHandler(options), http.MethodGet, "/api/tasks", "")
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Error.Message != "title must not be blank" || document.Error.Reference != "" {
		t.Fatalf("validation refusal = %#v, want its own words and no reference", document.Error)
	}
	if log.String() != "" {
		t.Fatalf("server log = %q, want nothing for a refusal the answer already explains", log.String())
	}
}

func TestHostedBoardKeepsServerDetailOutOfAWarning(t *testing.T) {
	t.Parallel()
	log := &lockedBuffer{}
	options := hostedOptions(t, nil)
	options.Hosted = true
	options.ServerLog = log
	response := requestJSON(t, NewHandler(options), http.MethodPost, "/api/tasks", `{"title":"Ship it"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	var document TaskMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Warnings) != 2 {
		t.Fatalf("warnings = %#v, want both kept", document.Warnings)
	}
	publication := document.Warnings[0]
	if publication.Code != core.WarningAutoSync || strings.Contains(publication.Message, "/srv") ||
		strings.Contains(publication.Message, "fatal") || !strings.Contains(publication.Message, "recorded") {
		t.Fatalf("publication warning = %#v, want its code and a short sentence", publication)
	}
	match := referencePattern.FindStringSubmatch(publication.Message)
	if match == nil {
		t.Fatalf("publication warning %q names no reference", publication.Message)
	}
	if document.Warnings[1].Message != "WB-01 is also assigned to sam@example.com" {
		t.Fatalf("assignment warning = %#v, want its own words", document.Warnings[1])
	}
	if logged := log.String(); !strings.Contains(logged, match[1]) || !strings.Contains(logged, serverDetail) {
		t.Fatalf("server log = %q, want reference %s and the detail", logged, match[1])
	}
}

func TestLocalBoardAnswersWithTheWholeDetail(t *testing.T) {
	t.Parallel()
	failure := core.Wrap(core.CategoryOperational, "list tasks", errors.New(serverDetail))
	response := requestJSON(t, NewHandler(hostedOptions(t, failure)), http.MethodGet, "/api/tasks", "")
	want, err := json.Marshal(ErrorDocument{Format: "workbook.error", Version: 1, Error: ErrorBody{Category: core.CategoryOperational, Message: failure.Error()}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(response.Body.String(), "\n"); got != string(want) {
		t.Fatalf("local answer = %s, want %s", got, want)
	}

	response = requestJSON(t, NewHandler(hostedOptions(t, nil)), http.MethodPost, "/api/tasks", `{"title":"Ship it"}`)
	if !strings.Contains(response.Body.String(), "missing.git") {
		t.Fatalf("local warning lost its detail: %s", response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("local mutation Cache-Control = %q, want none", got)
	}
}

func TestHostedBoardAnswersArePrivateAndNotStored(t *testing.T) {
	t.Parallel()
	options := hostedOptions(t, nil)
	options.Hosted = true
	options.ServerLog = &lockedBuffer{}
	hosted := NewHandler(options)
	local := NewHandler(hostedOptions(t, nil))
	task := threadRouteTask()
	attachment := "/api/tasks/" + task.ID + "/attachments/" + threadRouteAttachOne
	for _, test := range []struct {
		target    string
		localWant string
	}{
		{"/", ""},
		{"/tasks/" + task.ID, ""},
		{"/tasks/new", ""},
		{"/api/tasks", "no-cache"},
		{attachment, ""},
		{"/healthz", ""},
	} {
		response := httptest.NewRecorder()
		hosted.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("hosted GET %s = %d; body = %s", test.target, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("hosted GET %s Cache-Control = %q, want private, no-store", test.target, got)
		}
		response = httptest.NewRecorder()
		local.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
		if got := response.Header().Get("Cache-Control"); got != test.localWant {
			t.Errorf("local GET %s Cache-Control = %q, want %q as before", test.target, got, test.localWant)
		}
	}
	// The poll's conditional answer keeps working under the hosted header: the
	// page sends the tag itself, so nothing needs a stored copy.
	first := httptest.NewRecorder()
	hosted.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	again := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	again.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	hosted.ServeHTTP(second, again)
	if second.Code != http.StatusNotModified || second.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("hosted revalidation = %d with %q, want 304 and private, no-store", second.Code, second.Header().Get("Cache-Control"))
	}
}
