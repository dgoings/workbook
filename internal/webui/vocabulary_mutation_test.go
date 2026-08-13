package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the four vocabulary mutation routes do with a request, and what they say
// about a refusal. What the changes themselves mean is decided by the status
// verb family and tested through the real wiring in internal/cli.

// recordedVocabularyMutations captures what each route handed its capability, so
// a test can assert that the body reached it in the shape the contract promises.
type recordedVocabularyMutations struct {
	addition *VocabularyStatusAddition
	edited   core.Status
	edit     *VocabularyStatusEdit
	removed  core.Status
	removal  *VocabularyStatusRemoval
	order    *VocabularyOrder
	calls    int
}

// vocabularyMutationHandler builds a board whose four vocabulary mutations
// record their input and answer with the given result.
func vocabularyMutationHandler(
	t *testing.T,
	recorded *recordedVocabularyMutations,
	mutation VocabularyMutation,
	err error,
) http.Handler {
	t.Helper()
	answer := func() (VocabularyMutation, error) {
		recorded.calls++
		return mutation, err
	}
	return NewHandler(Options{
		Vocabulary: staticVocabulary(handlerVocabulary(t), "head-current"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
		AddStatus: func(_ context.Context, addition VocabularyStatusAddition) (VocabularyMutation, error) {
			recorded.addition = &addition
			return answer()
		},
		EditStatus: func(_ context.Context, status core.Status, edit VocabularyStatusEdit) (VocabularyMutation, error) {
			recorded.edited, recorded.edit = status, &edit
			return answer()
		},
		RemoveStatus: func(_ context.Context, status core.Status, removal VocabularyStatusRemoval) (VocabularyMutation, error) {
			recorded.removed, recorded.removal = status, &removal
			return answer()
		},
		ReorderStatus: func(_ context.Context, order VocabularyOrder) (VocabularyMutation, error) {
			recorded.order = &order
			return answer()
		},
	})
}

// vocabularyMutationResult is what a successful capability answers with: a
// vocabulary that is deliberately not the one the resolver reports, so a route
// that answered from the resolver rather than from the change would fail.
func vocabularyMutationResult(t *testing.T) VocabularyMutation {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Icebox", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "queued", Label: "Queued Up", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "landed", Label: "Landed", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		[]core.StatusAlias{{From: "shipped", To: "landed"}},
		[]core.RetiredStatus{{Status: "thawing", Destination: "icebox"}},
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return VocabularyMutation{
		State: VocabularyState{Vocabulary: vocabulary, Head: "head-written"},
		Tasks: VocabularyTaskCounts{Affected: 3, ClaimableAfter: 2},
	}
}

// Every mutation answers with the whole vocabulary, in the shape GET
// /api/vocabulary serves, including the head the client's next change must name.
func TestHandlerVocabularyMutationsAnswerWithTheWholeVocabulary(t *testing.T) {
	result := vocabularyMutationResult(t)
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/statuses",
			body: `{"status":"triage","expectedHead":"head-current"}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/statuses/queued",
			body: `{"label":"Queued","expectedHead":"head-current"}`},
		{name: "remove", method: http.MethodDelete, target: "/api/vocabulary/statuses/queued",
			body: `{"into":"icebox","expectedHead":"head-current"}`},
		{name: "reorder", method: http.MethodPut, target: "/api/vocabulary/order",
			body: `{"statuses":["queued","icebox"],"expectedHead":"head-current"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedVocabularyMutations{}
			handler := vocabularyMutationHandler(t, recorded, result, nil)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusOK, response.Body.String())
			}
			assertSecurityHeaders(t, response.Result())
			var document VocabularyMutationDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.vocabulary-mutation" || document.Version != 1 {
				t.Fatalf("mutation envelope = %#v, want a versioned document", document)
			}
			// The same document the read route serves, from the same builder, so
			// a client renders a change through the code that rendered the page.
			if want := vocabularyDocument(result.State); !reflect.DeepEqual(document.Vocabulary, want) {
				t.Fatalf("mutation vocabulary = %#v, want %#v", document.Vocabulary, want)
			}
			if document.Vocabulary.Head != "head-written" {
				t.Fatalf("head = %q, want the one the change produced", document.Vocabulary.Head)
			}
			if document.Tasks != result.Tasks {
				t.Fatalf("task counts = %#v, want %#v", document.Tasks, result.Tasks)
			}
			if recorded.calls != 1 {
				t.Fatalf("capability was called %d times, want once", recorded.calls)
			}
		})
	}
}

// Each body reaches its capability as the members the contract names, and an
// omitted member of a status change is different from an emptied one.
func TestHandlerVocabularyMutationsCarryTheirBodies(t *testing.T) {
	result := vocabularyMutationResult(t)

	t.Run("addition", func(t *testing.T) {
		recorded := &recordedVocabularyMutations{}
		handler := vocabularyMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
			`{"status":"triage","label":"Triage","tags":["default","next"],"after":"icebox","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("POST status = %d; body = %s", response.Code, response.Body.String())
		}
		want := VocabularyStatusAddition{
			Status: "triage", Label: "Triage", Tags: []string{"default", "next"},
			After: "icebox", ExpectedHead: "head-current",
		}
		if !reflect.DeepEqual(*recorded.addition, want) {
			t.Fatalf("addition = %#v, want %#v", *recorded.addition, want)
		}
	})

	t.Run("a change that names one member", func(t *testing.T) {
		recorded := &recordedVocabularyMutations{}
		handler := vocabularyMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/statuses/queued",
			`{"name":"waiting","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.edited != "queued" {
			t.Fatalf("edited status = %q, want the one the path addressed", recorded.edited)
		}
		if recorded.edit.Name == nil || *recorded.edit.Name != "waiting" {
			t.Fatalf("edit name = %#v, want waiting", recorded.edit.Name)
		}
		if recorded.edit.Label != nil || recorded.edit.Tags != nil {
			t.Fatalf("edit = %#v, want the members it did not name left absent", *recorded.edit)
		}
	})

	t.Run("a change that empties a member", func(t *testing.T) {
		recorded := &recordedVocabularyMutations{}
		handler := vocabularyMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/statuses/queued",
			`{"label":"","tags":[],"expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.edit.Label == nil || *recorded.edit.Label != "" {
			t.Fatalf("edit label = %#v, want an emptied member rather than an absent one", recorded.edit.Label)
		}
		if recorded.edit.Tags == nil || len(*recorded.edit.Tags) != 0 {
			t.Fatalf("edit tags = %#v, want an emptied set rather than an absent one", recorded.edit.Tags)
		}
	})

	t.Run("removal", func(t *testing.T) {
		recorded := &recordedVocabularyMutations{}
		handler := vocabularyMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodDelete, "/api/vocabulary/statuses/in-review",
			`{"into":"icebox","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("DELETE status = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.removed != "in-review" {
			t.Fatalf("removed status = %q, want the hyphenated one the path addressed", recorded.removed)
		}
		if recorded.removal.Into != "icebox" || recorded.removal.ExpectedHead != "head-current" {
			t.Fatalf("removal = %#v, want the destination and head the body named", *recorded.removal)
		}
	})

	t.Run("order", func(t *testing.T) {
		recorded := &recordedVocabularyMutations{}
		handler := vocabularyMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPut, "/api/vocabulary/order",
			`{"statuses":["landed","queued","icebox"],"expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PUT order = %d; body = %s", response.Code, response.Body.String())
		}
		want := []core.Status{"landed", "queued", "icebox"}
		if !reflect.DeepEqual(recorded.order.Statuses, want) {
			t.Fatalf("order = %#v, want %#v", recorded.order.Statuses, want)
		}
	})
}

// A change that names no head is refused before anything is asked to apply it,
// and the refusal names the member that is missing.
func TestHandlerVocabularyMutationsRequireAnExpectedHead(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/statuses", body: `{"status":"triage"}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/statuses/queued", body: `{"label":"Queued"}`},
		{name: "remove", method: http.MethodDelete, target: "/api/vocabulary/statuses/queued", body: `{"into":"icebox"}`},
		{name: "reorder", method: http.MethodPut, target: "/api/vocabulary/order", body: `{"statuses":["queued"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedVocabularyMutations{}
			handler := vocabularyMutationHandler(t, recorded, vocabularyMutationResult(t), nil)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusBadRequest, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryValidation {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryValidation)
			}
			if !strings.Contains(document.Error.Message, "expectedHead") {
				t.Fatalf("error message = %q, want it to name the missing member", document.Error.Message)
			}
			if recorded.calls != 0 {
				t.Fatal("a change with no head reached the capability")
			}
		})
	}

	// An empty head is a head: it is what a project whose configuration ledger
	// has never been seeded reads, and sending it back is the client telling the
	// truth about what it saw.
	recorded := &recordedVocabularyMutations{}
	handler := vocabularyMutationHandler(t, recorded, vocabularyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":""}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST with an empty head = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	if recorded.addition.ExpectedHead != "" {
		t.Fatalf("expectedHead = %q, want the empty one the client read", recorded.addition.ExpectedHead)
	}
}

// A stale write is a 409 carrying the stale-write category the client's queue
// matches on, and the statuses it should recompose the change against.
func TestHandlerVocabularyMutationsReportStaleWritesWithTheCurrentVocabulary(t *testing.T) {
	stale := core.Errorf(core.CategoryStaleWrite,
		"this project's statuses have changed since head-old; reload and try again")
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/statuses",
			body: `{"status":"triage","expectedHead":"head-old"}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/statuses/queued",
			body: `{"label":"Queued","expectedHead":"head-old"}`},
		{name: "remove", method: http.MethodDelete, target: "/api/vocabulary/statuses/queued",
			body: `{"into":"icebox","expectedHead":"head-old"}`},
		{name: "reorder", method: http.MethodPut, target: "/api/vocabulary/order",
			body: `{"statuses":["queued","icebox"],"expectedHead":"head-old"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := vocabularyMutationHandler(t, &recordedVocabularyMutations{}, VocabularyMutation{}, stale)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusConflict {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusConflict, response.Body.String())
			}
			assertSecurityHeaders(t, response.Result())
			var document VocabularyErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			// The envelope is the ordinary one, so a client that only knows how
			// to read errors reads this one.
			if document.Format != "workbook.error" || document.Version != 1 {
				t.Fatalf("error envelope = %#v, want workbook.error v1", document)
			}
			if document.Error.Category != core.CategoryStaleWrite || document.Error.Message != stale.Error() {
				t.Fatalf("error body = %#v, want the stale write as it was reported", document.Error)
			}
			if document.Vocabulary == nil {
				t.Fatal("a stale write answered without the vocabulary to re-render")
			}
			if want := vocabularyDocument(VocabularyState{Vocabulary: handlerVocabulary(t), Head: "head-current"}); !reflect.DeepEqual(*document.Vocabulary, want) {
				t.Fatalf("refusal vocabulary = %#v, want the current one %#v", *document.Vocabulary, want)
			}
		})
	}
}

// A refusal that is not about staleness carries no vocabulary: there is nothing
// for the client to re-render, and a body that grew a member for every refusal
// would train nobody to read it.
func TestHandlerVocabularyMutationsMapRefusalsToStatuses(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "validation", err: core.Errorf(core.CategoryValidation, `this project already defines status "queued"`), wantStatus: http.StatusBadRequest},
		{name: "unknown status", err: core.Errorf(core.CategoryNotFound, `no status "nowhere" in this project`), wantStatus: http.StatusNotFound},
		{name: "invocation", err: core.Errorf(core.CategoryInvocation, "a status is placed before or after another status, not both"), wantStatus: http.StatusBadRequest},
		{name: "conflict", err: core.Errorf(core.CategoryConflict, "the configuration ledger disagrees"), wantStatus: http.StatusConflict},
		{name: "corrupt data", err: core.Errorf(core.CategoryCorruptData, "the ledger will not decode"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := vocabularyMutationHandler(t, &recordedVocabularyMutations{}, VocabularyMutation{}, test.err)
			response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
				`{"status":"queued","expectedHead":"head-current"}`)
			if response.Code != test.wantStatus {
				t.Fatalf("POST status = %d, want %d; body = %s",
					response.Code, test.wantStatus, response.Body.String())
			}
			var document VocabularyErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryOf(test.err) {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryOf(test.err))
			}
			if document.Vocabulary != nil {
				t.Fatalf("refusal carried a vocabulary it has no use for: %#v", *document.Vocabulary)
			}
		})
	}
}

// A stale write from a board whose vocabulary cannot be read is still a stale
// write. The client loses the re-render it would have got, not the refusal.
func TestHandlerVocabularyStaleWriteSurvivesAnUnreadableVocabulary(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{}, core.Errorf(core.CategoryCorruptData, "cannot read this project's status configuration")
		},
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return VocabularyMutation{}, core.Errorf(core.CategoryStaleWrite, "statuses have changed; reload and try again")
		},
	})
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":"head-old"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("POST status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var document VocabularyErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
	if document.Vocabulary != nil {
		t.Fatal("an unreadable vocabulary was reported as a vocabulary")
	}
}

// A status change that sets nothing is refused rather than recorded as a commit
// that did nothing.
func TestHandlerVocabularyEditRequiresSomethingToChange(t *testing.T) {
	recorded := &recordedVocabularyMutations{}
	handler := vocabularyMutationHandler(t, recorded, vocabularyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/statuses/queued",
		`{"expectedHead":"head-current"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if recorded.calls != 0 {
		t.Fatal("a change that sets nothing reached the capability")
	}
}

// A placement that names both neighbours is a request nobody could have meant.
func TestHandlerVocabularyAdditionTakesOneNeighbour(t *testing.T) {
	recorded := &recordedVocabularyMutations{}
	handler := vocabularyMutationHandler(t, recorded, vocabularyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
		`{"status":"triage","before":"queued","after":"icebox","expectedHead":"head-current"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if recorded.calls != 0 {
		t.Fatal("a placement naming both neighbours reached the capability")
	}
}

// A body carrying a member these routes do not have is refused rather than
// silently ignored, exactly as every other mutation body is.
func TestHandlerVocabularyMutationsRefuseUnknownMembers(t *testing.T) {
	recorded := &recordedVocabularyMutations{}
	handler := vocabularyMutationHandler(t, recorded, vocabularyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/statuses",
		`{"status":"triage","colour":"blue","expectedHead":"head-current"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if recorded.calls != 0 {
		t.Fatal("a body with an unknown member reached the capability")
	}
}

// A board built without these capabilities says so rather than pretending, the
// way every route reports a capability it was not given.
func TestHandlerWithoutVocabularyMutationsReportsThem(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, test := range []struct {
		method string
		target string
	}{
		{method: http.MethodPost, target: "/api/vocabulary/statuses"},
		{method: http.MethodPatch, target: "/api/vocabulary/statuses/queued"},
		{method: http.MethodDelete, target: "/api/vocabulary/statuses/queued"},
		{method: http.MethodPut, target: "/api/vocabulary/order"},
	} {
		response := requestJSON(t, handler, test.method, test.target, `{"expectedHead":"head-current"}`)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s = %d, want %d; body = %s",
				test.method, test.target, response.Code, http.StatusInternalServerError, response.Body.String())
		}
		var document ErrorDocument
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
		}
		if document.Error.Category != core.CategoryOperational {
			t.Fatalf("%s %s category = %q, want %q",
				test.method, test.target, document.Error.Category, core.CategoryOperational)
		}
	}
}

// The routes answer their own methods and refuse the rest, naming what they
// allow.
func TestHandlerVocabularyRoutesEnforceTheirMethods(t *testing.T) {
	handler := vocabularyMutationHandler(t, &recordedVocabularyMutations{}, vocabularyMutationResult(t), nil)
	for _, test := range []struct {
		method string
		target string
		allow  string
	}{
		{method: http.MethodGet, target: "/api/vocabulary/statuses", allow: http.MethodPost},
		{method: http.MethodDelete, target: "/api/vocabulary/statuses", allow: http.MethodPost},
		{method: http.MethodPost, target: "/api/vocabulary/statuses/queued", allow: "PATCH, DELETE"},
		{method: http.MethodGet, target: "/api/vocabulary/order", allow: http.MethodPut},
		{method: http.MethodPost, target: "/api/vocabulary", allow: http.MethodGet},
	} {
		response := requestJSON(t, handler, test.method, test.target, `{}`)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want %d; body = %s",
				test.method, test.target, response.Code, http.StatusMethodNotAllowed, response.Body.String())
		}
		if got := response.Header().Get("Allow"); got != test.allow {
			t.Fatalf("%s %s Allow = %q, want %q", test.method, test.target, got, test.allow)
		}
	}
}

// The read route is untouched by any of this: a board with the four
// capabilities still serves the same document it always did.
func TestHandlerServesTheVocabularyThroughTheSameDocument(t *testing.T) {
	handler := vocabularyMutationHandler(t, &recordedVocabularyMutations{}, vocabularyMutationResult(t), nil)
	response := request(t, handler, http.MethodGet, "/api/vocabulary")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d, want %d", response.Code, http.StatusOK)
	}
	var document VocabularyDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, response.Body.String())
	}
	if want := vocabularyDocument(VocabularyState{Vocabulary: handlerVocabulary(t), Head: "head-current"}); !reflect.DeepEqual(document, want) {
		t.Fatalf("vocabulary document = %#v, want %#v", document, want)
	}
}
