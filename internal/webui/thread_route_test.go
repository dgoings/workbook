package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The board's comments and attachments: five mutations that go through core's
// single-intent doors, and one read that hands somebody else's bytes back to a
// browser. The read is where the security lives, and most of this file is about
// it.

const (
	threadRouteTaskID    = "WB-01K0M6B8A4FTT8C39MXXYTW7C3"
	threadRouteComment   = "01K0M6B8A4FTT8C39MXXYTWC01"
	threadRouteAttachOne = "01K0M6B8A4FTT8C39MXXYTWA01"
	threadRouteAttachTwo = "01K0M6B8A4FTT8C39MXXYTWA02"
)

// threadTask is a task with a thread on it, which is what every one of these
// mutations answers with: the whole task, so a client renders the result of a
// change through the code that rendered the page.
func threadRouteTask() core.Task {
	stamp := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	return core.Task{
		ID:        threadRouteTaskID,
		ProjectID: "01J00000000000000000000000",
		Head:      "head-2",
		TaskData: core.TaskData{
			Title:        "Commented task",
			Status:       core.StatusReady,
			Priority:     core.PriorityMedium,
			Rank:         "1/1",
			Labels:       []string{},
			Dependencies: []string{},
			CreatedAt:    stamp,
			UpdatedAt:    stamp,
			Comments: []core.Comment{
				{ID: threadRouteComment, Author: "author@example.com", Body: "shipped", CreatedAt: stamp},
			},
			Attachments: []core.Attachment{
				{ID: threadRouteAttachOne, Author: "author@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
					Name: "trace.log", Kind: core.AttachmentFile, Media: "text/plain", Size: 11, Blob: strings.Repeat("a", 40),
				}},
				{ID: threadRouteAttachTwo, Author: "author@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
					Kind: core.AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
				}},
			},
		},
	}
}

// threadRouteHandler wires every thread capability to a recorder, so a test can
// state what one request reached the service with.
type threadRouteCalls struct {
	commentAdd       []core.CommentAddInput
	commentEdit      []core.CommentEditInput
	commentRemove    []core.CommentRemoveInput
	attachmentAdd    []core.AttachmentAddInput
	attachmentRemove []core.AttachmentRemoveInput
	taskIDs          []string
	found            []core.Attachment
	content          []byte
	contentErr       error
	findErr          error
}

func threadRouteHandler(t *testing.T, calls *threadRouteCalls) http.Handler {
	t.Helper()
	task := threadRouteTask()
	result := func() (core.MutationResult, error) { return core.MutationResult{Task: task}, nil }
	return NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		AddComment: func(_ context.Context, id string, input core.CommentAddInput) (core.MutationResult, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			calls.commentAdd = append(calls.commentAdd, input)
			return result()
		},
		EditComment: func(_ context.Context, id string, input core.CommentEditInput) (core.MutationResult, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			calls.commentEdit = append(calls.commentEdit, input)
			return result()
		},
		RemoveComment: func(_ context.Context, id string, input core.CommentRemoveInput) (core.MutationResult, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			calls.commentRemove = append(calls.commentRemove, input)
			return result()
		},
		AddAttachment: func(_ context.Context, id string, input core.AttachmentAddInput) (core.MutationResult, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			calls.attachmentAdd = append(calls.attachmentAdd, input)
			return result()
		},
		RemoveAttachment: func(_ context.Context, id string, input core.AttachmentRemoveInput) (core.MutationResult, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			calls.attachmentRemove = append(calls.attachmentRemove, input)
			return result()
		},
		Attachment: func(_ context.Context, id, attachmentID string) (core.Attachment, error) {
			calls.taskIDs = append(calls.taskIDs, id)
			if calls.findErr != nil {
				return core.Attachment{}, calls.findErr
			}
			for _, attachment := range task.Attachments {
				if attachment.ID == attachmentID {
					return attachment, nil
				}
			}
			return core.Attachment{}, core.Errorf(core.CategoryNotFound,
				"task %s has no attachment %s", id, attachmentID)
		},
		AttachmentContent: func(_ context.Context, attachment core.Attachment) ([]byte, error) {
			calls.found = append(calls.found, attachment)
			if calls.contentErr != nil {
				return nil, calls.contentErr
			}
			if calls.content != nil {
				return calls.content, nil
			}
			return []byte("hello world"), nil
		},
	})
}

// Every thread mutation answers with the whole task, thread included, because a
// client that can draw a task from the poll draws the result of its own change
// with the same code.
func TestThreadMutationsAnswerWithTheWholeTask(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"comment add", http.MethodPost, "/api/tasks/" + threadRouteTaskID + "/comments", `{"body":"shipped","expectedHead":"head-1"}`},
		{"comment edit", http.MethodPatch, "/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, `{"body":"shipped twice","expectedHead":"head-1"}`},
		{"comment remove", http.MethodDelete, "/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, `{"expectedHead":"head-1"}`},
		{"attachment add", http.MethodPost, "/api/tasks/" + threadRouteTaskID + "/attachments", `{"kind":"link","url":"https://example.test/pr","expectedHead":"head-1"}`},
		{"attachment remove", http.MethodDelete, "/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, `{"expectedHead":"head-1"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s status = %d, want %d; body = %s", test.method, test.target, response.Code, http.StatusOK, response.Body.String())
			}
			var document TaskMutationDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.task-mutation" || document.Version != 1 {
				t.Fatalf("mutation document = %#v", document)
			}
			if len(document.Task.Comments) != 1 || document.Task.Comments[0].ID != threadRouteComment {
				t.Fatalf("answer carries no thread: %#v", document.Task.Comments)
			}
			if len(document.Task.Attachments) != 2 {
				t.Fatalf("answer carries no attachment list: %#v", document.Task.Attachments)
			}
		})
	}
	for _, id := range calls.taskIDs {
		if id != threadRouteTaskID {
			t.Fatalf("a mutation addressed task %q, want %q", id, threadRouteTaskID)
		}
	}
}

// The head a change was composed against travels in the body of every one of
// them, exactly as it does on the task routes these sit beside.
func TestThreadMutationsCarryTheHeadTheyWereComposedAgainst(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/comments", `{"body":"  shipped  ","expectedHead":"head-1"}`)
	requestJSON(t, handler, http.MethodPatch, "/api/tasks/"+threadRouteTaskID+"/comments/"+threadRouteComment, `{"body":"reworded","expectedHead":"head-2"}`)
	requestJSON(t, handler, http.MethodDelete, "/api/tasks/"+threadRouteTaskID+"/comments/"+threadRouteComment, `{"expectedHead":"head-3"}`)
	requestJSON(t, handler, http.MethodDelete, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne, `{"expectedHead":"head-4"}`)

	if len(calls.commentAdd) != 1 || calls.commentAdd[0].ExpectedHead != "head-1" {
		t.Fatalf("comment add = %#v", calls.commentAdd)
	}
	// The body reaches core exactly as it was sent: trimming a comment is core's
	// decision and it makes it once, at the mutation boundary, so that what is
	// stored is what the boundary decided to store.
	if calls.commentAdd[0].Body != "  shipped  " {
		t.Fatalf("comment body = %q, want it untouched by this package", calls.commentAdd[0].Body)
	}
	if len(calls.commentEdit) != 1 || calls.commentEdit[0].CommentID != threadRouteComment ||
		calls.commentEdit[0].Body != "reworded" || calls.commentEdit[0].ExpectedHead != "head-2" {
		t.Fatalf("comment edit = %#v", calls.commentEdit)
	}
	if len(calls.commentRemove) != 1 || calls.commentRemove[0].CommentID != threadRouteComment ||
		calls.commentRemove[0].ExpectedHead != "head-3" {
		t.Fatalf("comment remove = %#v", calls.commentRemove)
	}
	if len(calls.attachmentRemove) != 1 || calls.attachmentRemove[0].AttachmentID != threadRouteAttachOne ||
		calls.attachmentRemove[0].ExpectedHead != "head-4" {
		t.Fatalf("attachment remove = %#v", calls.attachmentRemove)
	}
}

// A removal may arrive with no body at all, the way a task deletion may: every
// member is optional, so the bare verb is a legal request and keeps meaning
// what it meant.
func TestThreadRemovalsAcceptABareVerb(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	for _, target := range []string{
		"/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment,
		"/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne,
	} {
		response := requestJSON(t, handler, http.MethodDelete, target, "")
		if response.Code != http.StatusOK {
			t.Fatalf("DELETE %s status = %d, want %d; body = %s", target, response.Code, http.StatusOK, response.Body.String())
		}
	}
	if len(calls.commentRemove) != 1 || calls.commentRemove[0].ExpectedHead != "" {
		t.Fatalf("comment remove = %#v", calls.commentRemove)
	}
	if len(calls.attachmentRemove) != 1 || calls.attachmentRemove[0].ExpectedHead != "" {
		t.Fatalf("attachment remove = %#v", calls.attachmentRemove)
	}
}

// A board built without a capability says so, the way every unwired route on
// this board says so, rather than answering an address it cannot serve.
func TestThreadRoutesReportTheCapabilitiesTheyWereNotGiven(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{threadRouteTask()}, nil })

	for _, test := range []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/api/tasks/" + threadRouteTaskID + "/comments", `{"body":"x"}`},
		{http.MethodPatch, "/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, `{"body":"x"}`},
		{http.MethodDelete, "/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, ``},
		{http.MethodPost, "/api/tasks/" + threadRouteTaskID + "/attachments", `{"kind":"link","url":"https://example.test/x"}`},
		{http.MethodDelete, "/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, ``},
		{http.MethodGet, "/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, ``},
	} {
		response := requestJSON(t, handler, test.method, test.target, test.body)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s status = %d, want %d; body = %s", test.method, test.target, response.Code, http.StatusInternalServerError, response.Body.String())
		}
		if got := errorDocumentOf(t, response.Body.Bytes()); got.Category != core.CategoryOperational ||
			!strings.Contains(got.Message, "is not configured") {
			t.Fatalf("%s %s error = %#v", test.method, test.target, got)
		}
	}
}

// A file arrives as base64 and reaches core as bytes. The board names no media
// type: the file name decides through core's table, which is the only rule two
// clones attaching the same file can agree on.
func TestAttachmentUploadDecodesItsBytes(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)
	content := []byte("hello world\x00\xff binary")
	body := `{"kind":"file","name":"trace.log","content":"` + base64.StdEncoding.EncodeToString(content) + `","expectedHead":"head-1"}`

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments", body)
	if response.Code != http.StatusOK {
		t.Fatalf("POST attachments status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if len(calls.attachmentAdd) != 1 {
		t.Fatalf("attachment adds = %d, want one", len(calls.attachmentAdd))
	}
	added := calls.attachmentAdd[0]
	if added.Kind != core.AttachmentFile || added.Name != "trace.log" ||
		string(added.Content) != string(content) || added.Media != "" ||
		added.URL != "" || added.Label != "" || added.ExpectedHead != "head-1" {
		t.Fatalf("attachment add = %#v", added)
	}
}

// A link stores nothing and carries no file members.
func TestAttachmentUploadCarriesALink(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments",
		`{"kind":"link","url":"https://example.test/design","label":"Design doc"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST attachments status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	added := calls.attachmentAdd[0]
	if added.Kind != core.AttachmentLink || added.URL != "https://example.test/design" ||
		added.Label != "Design doc" || added.Name != "" || added.Content != nil {
		t.Fatalf("attachment add = %#v", added)
	}
}

// The two kinds are separate requests, and a body that mixes them is refused
// rather than read as one of them with the rest ignored.
func TestAttachmentUploadRefusesABodyThatIsNeitherKind(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)
	sample := base64.StdEncoding.EncodeToString([]byte("bytes"))

	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"no kind", `{"name":"trace.log","content":"` + sample + `"}`, `must be "file" or "link"`},
		{"unknown kind", `{"kind":"blob","name":"trace.log"}`, `must be "file" or "link"`},
		{"file with a URL", `{"kind":"file","name":"a.txt","content":"` + sample + `","url":"https://example.test/x"}`, "no url or label"},
		{"link with content", `{"kind":"link","url":"https://example.test/x","content":"` + sample + `"}`, "no name, content or media type"},
		{"link with a media type", `{"kind":"link","url":"https://example.test/x","media":"image/png"}`, "no name, content or media type"},
		{"file with no bytes", `{"kind":"file","name":"a.txt"}`, "base64 in content"},
		{"file with bytes that are not base64", `{"kind":"file","name":"a.txt","content":"not base64!"}`, "standard base64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if got := errorDocumentOf(t, response.Body.Bytes()); !strings.Contains(got.Message, test.want) {
				t.Fatalf("error = %#v, want a message containing %q", got, test.want)
			}
		})
	}
	if len(calls.attachmentAdd) != 0 {
		t.Fatalf("a refused upload reached the service %d times", len(calls.attachmentAdd))
	}
}

// The file ceiling is refused before the service is called, so no refused
// attachment can leave a staged Git object behind, and it is refused in core's
// own words and against core's own number.
func TestAttachmentUploadRefusesAFileOverTheCeilingBeforeStaging(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)
	oversized := base64.StdEncoding.EncodeToString(make([]byte, core.MaxAttachmentFileBytes+1))

	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments",
		`{"kind":"file","name":"huge.bin","content":"`+oversized+`"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	body := errorDocumentOf(t, response.Body.Bytes())
	if body.Category != core.CategoryValidation {
		t.Fatalf("category = %q, want %q", body.Category, core.CategoryValidation)
	}
	for _, want := range []string{"huge.bin", strconv.Itoa(core.MaxAttachmentFileBytes), "attach a link instead"} {
		if !strings.Contains(body.Message, want) {
			t.Fatalf("refusal = %q, want it to name %q", body.Message, want)
		}
	}
	if len(calls.attachmentAdd) != 0 {
		t.Fatalf("an over-sized upload reached the service %d times", len(calls.attachmentAdd))
	}
}

// The upload route's body ceiling is the encoded one. A file at core's ceiling
// is four thirds of it once base64'd, so the ordinary request ceiling would
// refuse the largest attachment this build can store — and the enlarged one
// still refuses a body that means to keep going.
func TestAttachmentUploadBodyCeilingAdmitsTheLargestAttachment(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	largest := base64.StdEncoding.EncodeToString(make([]byte, core.MaxAttachmentFileBytes))
	body := `{"kind":"file","name":"largest.bin","content":"` + largest + `"}`
	if len(body) <= MaxRequestBodyBytes {
		t.Fatalf("the largest upload body is %d bytes, which is inside the ordinary ceiling %d; this test proves nothing",
			len(body), MaxRequestBodyBytes)
	}
	response := requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if len(calls.attachmentAdd) != 1 || len(calls.attachmentAdd[0].Content) != core.MaxAttachmentFileBytes {
		t.Fatalf("the largest attachment did not reach the service whole: %d adds", len(calls.attachmentAdd))
	}

	over := `{"kind":"file","name":"a.bin","content":"` + strings.Repeat("A", MaxAttachmentUploadBodyBytes) + `"}`
	response = requestJSON(t, handler, http.MethodPost, "/api/tasks/"+threadRouteTaskID+"/attachments", over)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("over-ceiling status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	refusal := errorDocumentOf(t, response.Body.Bytes())
	if refusal.Category != core.CategoryInvocation ||
		!strings.Contains(refusal.Message, strconv.Itoa(MaxAttachmentUploadBodyBytes)) {
		t.Fatalf("refusal = %#v, want the upload ceiling named", refusal)
	}
	if len(calls.attachmentAdd) != 1 {
		t.Fatalf("an over-ceiling body reached the service")
	}
}

// The ordinary routes keep the ordinary ceiling: widening it for one route must
// not widen it for the board.
func TestTheEnlargedBodyCeilingIsTheUploadRoutesAlone(t *testing.T) {
	for _, test := range []struct {
		path string
		want int64
	}{
		{"/api/tasks", MaxRequestBodyBytes},
		{"/api/tasks/" + threadRouteTaskID, MaxRequestBodyBytes},
		{"/api/tasks/" + threadRouteTaskID + "/comments", MaxRequestBodyBytes},
		{"/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, MaxRequestBodyBytes},
		{"/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, MaxRequestBodyBytes},
		{"/api/vocabulary/statuses", MaxRequestBodyBytes},
		{"/api/tasks/" + threadRouteTaskID + "/attachments", MaxAttachmentUploadBodyBytes},
	} {
		if got := requestBodyLimit(test.path); got != test.want {
			t.Errorf("requestBodyLimit(%q) = %d, want %d", test.path, got, test.want)
		}
	}
}

// The one route that hands back somebody else's bytes. What may be rendered by
// the browser is an allow-list of four raster image types, and everything else
// — every other type core's own table can derive, and every type it cannot —
// is an opaque download.
func TestAttachmentDownloadServesOnlyAllowListedImagesInline(t *testing.T) {
	// Every extension in core's attachment media table, so that a type added
	// there is a type this test asks about. The four images are the whole
	// inline list; the rest are documents, archives and text, every one of
	// which a browser would happily render or execute if it were let.
	names := []string{
		"a.css", "a.csv", "a.diff", "a.gif", "a.gz", "a.jpeg", "a.jpg", "a.json",
		"a.log", "a.md", "a.patch", "a.pdf", "a.png", "a.tar", "a.txt", "a.webp",
		"a.zip", "a.bin", "a.svg", "a.html", "a.xhtml", "a.js",
	}
	for _, name := range names {
		media := core.AttachmentMediaType(name)
		wantInline := media == "image/gif" || media == "image/jpeg" ||
			media == "image/png" || media == "image/webp"
		gotMedia, disposition := attachmentDelivery(media)
		if wantInline {
			if disposition != "inline" || gotMedia != media {
				t.Errorf("%s (%s) served as %q/%q, want inline as itself", name, media, gotMedia, disposition)
			}
			continue
		}
		if disposition != "attachment" || gotMedia != core.DefaultAttachmentMedia {
			t.Errorf("%s (%s) served as %q/%q, want an opaque download", name, media, gotMedia, disposition)
		}
	}
	// Nothing in the allow-list is a type core would refuse to store, and every
	// one of them is a type its table can produce: a list naming a type core
	// cannot even label a file with would be a rule about nothing.
	derivable := map[string]bool{}
	for _, name := range names {
		derivable[core.AttachmentMediaType(name)] = true
	}
	for media := range inlineAttachmentMediaTypes {
		if !derivable[media] {
			t.Errorf("the inline list names %q, which core's table does not derive", media)
		}
		if core.IsScriptableImageMediaType(media) {
			t.Errorf("the inline list names %q, which core refuses to store", media)
		}
	}
}

// The types an attacker would choose, none of which may ever be served inline.
// A prefix test on "image/" passes every one of the SVG spellings; the list
// does not.
func TestAttachmentDownloadNeverServesAScriptableTypeInline(t *testing.T) {
	for _, media := range []string{
		"image/svg+xml", "image/svg", "IMAGE/SVG+XML", " image/svg+xml ",
		"image/svg+xml; charset=utf-8", "text/html", "application/xhtml+xml",
		"application/javascript", "text/javascript", "image/", "image",
		"imagex/png", "image/png+html", "", "application/pdf",
	} {
		gotMedia, disposition := attachmentDelivery(media)
		if disposition != "attachment" || gotMedia != core.DefaultAttachmentMedia {
			t.Errorf("media %q served as %q/%q, want an opaque download", media, gotMedia, disposition)
		}
	}
}

// An attached HTML file downloads rather than rendering, headers and all. This
// is the stored cross-site scripting case the whole route is shaped around: the
// board has no authentication and its own page permits inline script, so a
// document served inline from this origin would be script on it.
func TestAttachmentDownloadServesHostileContentAsADownload(t *testing.T) {
	var calls threadRouteCalls
	page := []byte(`<script>fetch("/api/tasks",{method:"POST"})</script>`)
	calls.content = page
	handler := attachmentHandler(t, &calls, core.Attachment{ID: threadRouteAttachOne, AttachmentData: core.AttachmentData{
		Name: "report.html", Kind: core.AttachmentFile, Media: "text/html", Size: int64(len(page)), Blob: strings.Repeat("a", 40),
	}})

	response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	header := response.Result().Header
	if got := header.Get("Content-Type"); got != core.DefaultAttachmentMedia {
		t.Errorf("Content-Type = %q, want %q", got, core.DefaultAttachmentMedia)
	}
	if got := header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want a download", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := header.Get("Content-Security-Policy"); got != attachmentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, attachmentSecurityPolicy)
	}
	if response.Body.String() != string(page) {
		t.Errorf("body = %q, want the stored bytes unchanged", response.Body.String())
	}
}

// An allow-listed image is served as itself, inline, and still under the
// attachment's own policy and nosniff.
func TestAttachmentDownloadServesAnImageInline(t *testing.T) {
	var calls threadRouteCalls
	calls.content = []byte("\x89PNG\r\n\x1a\n")
	handler := attachmentHandler(t, &calls, core.Attachment{ID: threadRouteAttachOne, AttachmentData: core.AttachmentData{
		Name: "screenshot.png", Kind: core.AttachmentFile, Media: "image/png", Size: 8, Blob: strings.Repeat("a", 40),
	}})

	response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	header := response.Result().Header
	if got := header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline") ||
		!strings.Contains(got, "screenshot.png") {
		t.Errorf("Content-Disposition = %q, want inline naming the file", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff even inline", got)
	}
	if got := header.Get("Content-Security-Policy"); got != attachmentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want the attachment policy", got)
	}
	if got := header.Get("Content-Length"); got != "8" {
		t.Errorf("Content-Length = %q, want 8", got)
	}
}

// An attachment name is text somebody typed: core forbids a path separator and
// a NUL in it and nothing else. A name carrying a quote, a newline or a header
// of its own must not become part of the response's headers.
func TestAttachmentDownloadFormatsAHostileFileName(t *testing.T) {
	for _, name := range []string{
		`"; filename="pwned.html`,
		"report\r\nSet-Cookie: a=b",
		`back\slash".txt`,
		"réponse finale.txt",
		"‮gnp.exe",
		strings.Repeat("n", core.MaxAttachmentNameBytes),
	} {
		var calls threadRouteCalls
		handler := attachmentHandler(t, &calls, core.Attachment{ID: threadRouteAttachOne, AttachmentData: core.AttachmentData{
			Name: name, Kind: core.AttachmentFile, Media: "text/plain", Size: 11, Blob: strings.Repeat("a", 40),
		}})

		response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne)
		if response.Code != http.StatusOK {
			t.Fatalf("name %q: status = %d, want %d", name, response.Code, http.StatusOK)
		}
		disposition := response.Result().Header.Get("Content-Disposition")
		if !strings.HasPrefix(disposition, "attachment") {
			t.Errorf("name %q: Content-Disposition = %q, want a download", name, disposition)
		}
		if strings.ContainsAny(disposition, "\r\n") {
			t.Errorf("name %q: Content-Disposition carries a line break: %q", name, disposition)
		}
		// The header parses back to one disposition carrying one parameter, and
		// that parameter is the name as stored. A name that broke out of its
		// quoting would arrive as a second parameter, as a different
		// disposition, or as a header that does not parse at all.
		parsed, parameters, err := mime.ParseMediaType(disposition)
		if err != nil {
			t.Errorf("name %q: Content-Disposition %q does not parse: %v", name, disposition, err)
			continue
		}
		if parsed != "attachment" || len(parameters) != 1 || parameters["filename"] != name {
			t.Errorf("name %q: Content-Disposition parsed as %q %#v", name, parsed, parameters)
		}
	}
}

// A link holds no bytes. The refusal says so and names the URL, so a client
// that followed this address is told where the thing actually is.
func TestAttachmentDownloadRefusesALinkAndNamesIt(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachTwo)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	body := errorDocumentOf(t, response.Body.Bytes())
	if body.Category != core.CategoryValidation {
		t.Fatalf("category = %q, want %q", body.Category, core.CategoryValidation)
	}
	for _, want := range []string{threadRouteAttachTwo, "https://example.test/design", "is a link"} {
		if !strings.Contains(body.Message, want) {
			t.Fatalf("refusal = %q, want it to name %q", body.Message, want)
		}
	}
	if len(calls.found) != 0 {
		t.Fatalf("a link reached the content reader")
	}
}

// An attachment this task does not have, and an attachment whose bytes this
// clone does not hold, are both the not-found answer — the second is what a
// compacted history will produce.
func TestAttachmentDownloadAnswersMissingAttachmentsAndBlobsAsNotFound(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)
	response := request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/01K0M6B8A4FTT8C39MXXYTWA99")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body.String())
	}

	calls = threadRouteCalls{contentErr: core.Errorf(core.CategoryNotFound, "attachment object %s is not in this clone", strings.Repeat("a", 40))}
	handler = threadRouteHandler(t, &calls)
	response = request(t, handler, http.MethodGet, "/api/tasks/"+threadRouteTaskID+"/attachments/"+threadRouteAttachOne)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing blob status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body.String())
	}
	if got := errorDocumentOf(t, response.Body.Bytes()); got.Category != core.CategoryNotFound {
		t.Fatalf("missing blob error = %#v", got)
	}
	// A refused read answers as JSON under the page's own policy: nothing about
	// it is an attachment, so nothing about it wears the attachment's headers.
	if got := response.Result().Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want the error envelope's", got)
	}
}

// Each address answers the methods it has and refuses the rest by name, which
// is what the board's method table is for.
func TestThreadRoutesStateTheMethodsTheyAnswer(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	for _, test := range []struct {
		target  string
		refused string
		allow   string
	}{
		{"/api/tasks/" + threadRouteTaskID + "/comments", http.MethodGet, http.MethodPost},
		{"/api/tasks/" + threadRouteTaskID + "/comments", http.MethodDelete, http.MethodPost},
		{"/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, http.MethodPost, http.MethodPatch + ", " + http.MethodDelete},
		{"/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment, http.MethodGet, http.MethodPatch + ", " + http.MethodDelete},
		{"/api/tasks/" + threadRouteTaskID + "/attachments", http.MethodGet, http.MethodPost},
		{"/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, http.MethodPost, http.MethodGet + ", " + http.MethodDelete},
		{"/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne, http.MethodPatch, http.MethodGet + ", " + http.MethodDelete},
	} {
		response := requestJSON(t, handler, test.refused, test.target, "")
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status = %d, want %d", test.refused, test.target, response.Code, http.StatusMethodNotAllowed)
		}
		if got := response.Result().Header.Get("Allow"); got != test.allow {
			t.Errorf("%s %s Allow = %q, want %q", test.refused, test.target, got, test.allow)
		}
	}
}

// A path that is neither shape is not one of these routes.
func TestThreadRoutesDoNotAnswerADeeperPath(t *testing.T) {
	var calls threadRouteCalls
	handler := threadRouteHandler(t, &calls)

	for _, target := range []string{
		"/api/tasks/" + threadRouteTaskID + "/comments/" + threadRouteComment + "/replies",
		"/api/tasks/" + threadRouteTaskID + "/attachments/" + threadRouteAttachOne + "/bytes",
	} {
		response := request(t, handler, http.MethodGet, target)
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s status = %d, want it unanswered", target, response.Code)
		}
	}
	// The degenerate segments a path can be built with name nothing, and are
	// asked of the reader directly: a request carrying one is rewritten by the
	// mux before a route ever sees it, and the fallback these routes keep for a
	// request built without the mux is what has to refuse them.
	for _, path := range []string{
		"/api/tasks//comments/" + threadRouteComment,
		"/api/tasks/" + threadRouteTaskID + "/comments/",
		"/api/tasks/./comments/" + threadRouteComment,
		"/api/tasks/" + threadRouteTaskID + "/comments/..",
		"/api/tasks/" + threadRouteTaskID + "/attachments/",
	} {
		if _, _, ok := taskCommentPathIDs(path); ok {
			t.Errorf("taskCommentPathIDs(%q) read a comment out of a path that names none", path)
		}
	}
	if _, _, ok := taskCommentPathIDs("/api/tasks/" + threadRouteTaskID + "/dependencies/x"); ok {
		t.Error("a dependency path reads as a comment path")
	}
	if _, _, ok := taskAttachmentPathIDs("/api/tasks/" + threadRouteTaskID + "/comments/x"); ok {
		t.Error("a comment path reads as an attachment path")
	}
	if taskCommentsPathID("/api/tasks//comments") != "" || taskAttachmentsPathID("/api/tasks//attachments") != "" {
		t.Error("a collection path with no task reads as one")
	}
}

// attachmentHandler builds a board whose finder answers with one particular
// attachment, which is how a test states an attachment no ordinary fixture
// would hold — one labelled as HTML, or named with a header of its own.
func attachmentHandler(t *testing.T, calls *threadRouteCalls, attachment core.Attachment) http.Handler {
	t.Helper()
	task := threadRouteTask()
	return NewHandler(Options{
		List:         func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil },
		Create:       unexpectedTaskCreate(t),
		Update:       unexpectedTaskUpdate(t),
		UpdateStatus: unexpectedStatusUpdate(t),
		Attachment: func(context.Context, string, string) (core.Attachment, error) {
			return attachment, nil
		},
		AttachmentContent: func(_ context.Context, found core.Attachment) ([]byte, error) {
			calls.found = append(calls.found, found)
			return calls.content, nil
		},
	})
}

func errorDocumentOf(t *testing.T, body []byte) ErrorBody {
	t.Helper()
	var document ErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	if document.Format != "workbook.error" || document.Version != 1 {
		t.Fatalf("error document = %#v", document)
	}
	return document.Error
}
