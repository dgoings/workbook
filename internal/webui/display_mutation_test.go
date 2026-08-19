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

// What PATCH /api/display does with a request, and what it says about a refusal.
// What a save means — which settings are recorded and which are refused as
// changing nothing — is decided by the capability behind it and tested through
// the real wiring in internal/cli.

// recordedDisplayChange captures what the route handed its capability.
type recordedDisplayChange struct {
	change *DisplayChange
	calls  int
}

// displayMutationHandler builds a board whose display writer records its input
// and answers with the given result.
func displayMutationHandler(
	t *testing.T,
	recorded *recordedDisplayChange,
	mutation DisplayMutation,
	err error,
) http.Handler {
	t.Helper()
	return NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: handlerVocabulary(t),
				Head:       "head-current",
				Display:    core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"},
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
		SetDisplay: func(_ context.Context, change DisplayChange) (DisplayMutation, error) {
			recorded.change, recorded.calls = &change, recorded.calls+1
			return mutation, err
		},
	})
}

// displayMutationResult is what a successful save answers with: settings that
// are deliberately not the ones the resolver reports, so a route that answered
// from the resolver rather than from the change would fail.
func displayMutationResult() DisplayMutation {
	return DisplayMutation{
		State: VocabularyState{
			Head:    "head-written",
			Display: core.DisplaySettings{Name: "Beta", PrimaryColor: "#7f1a4b", TextColor: "#3b2a1a"},
		},
	}
}

// displaySettingsBoardPage renders a board wired for everything the
// configuration route offers, which is what `workbook serve` builds.
func displaySettingsBoardPage(t *testing.T, tasks []core.Task) string {
	t.Helper()
	unreached := func() (VocabularyMutation, error) { return VocabularyMutation{}, nil }
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(core.DefaultVocabulary(), "head-1"),
		List:       func(context.Context) ([]core.Task, error) { return tasks, nil },
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return unreached()
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return unreached()
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return unreached()
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return unreached()
		},
		SetDisplay: func(context.Context, DisplayChange) (DisplayMutation, error) {
			return DisplayMutation{}, nil
		},
	})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// A save answers with the whole document, including the head its next change
// has to name.
func TestHandlerDisplayMutationAnswersWithTheWholeConfiguration(t *testing.T) {
	recorded := &recordedDisplayChange{}
	result := displayMutationResult()
	handler := displayMutationHandler(t, recorded, result, nil)

	response := requestJSON(t, handler, http.MethodPatch, "/api/display",
		`{"name":"Beta","primaryColor":"#7f1a4b","textColor":"#3b2a1a","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/display = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertSecurityHeaders(t, response.Result())
	var document DisplayMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.display-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned document", document)
	}
	if want := displayDocument(result.State); !reflect.DeepEqual(document.Display, want) {
		t.Fatalf("mutation display = %#v, want %#v", document.Display, want)
	}
	if document.Display.Head != "head-written" {
		t.Fatalf("head = %q, want the one the change produced", document.Display.Head)
	}
	if recorded.calls != 1 {
		t.Fatalf("capability was called %d times, want once", recorded.calls)
	}
}

// The body reaches the capability as the whole configuration, which is what
// makes an emptied field a cleared setting rather than a member nobody sent.
func TestHandlerDisplayMutationCarriesTheWholeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want DisplayChange
	}{
		{
			name: "every setting",
			body: `{"name":"Beta","primaryColor":"#7f1a4b","textColor":"#3b2a1a","expectedHead":"head-current"}`,
			want: DisplayChange{Name: "Beta", PrimaryColor: "#7f1a4b", TextColor: "#3b2a1a", ExpectedHead: "head-current"},
		},
		{
			name: "an emptied field is a cleared setting",
			body: `{"name":"Beta","primaryColor":"","textColor":"","expectedHead":"head-current"}`,
			want: DisplayChange{Name: "Beta", ExpectedHead: "head-current"},
		},
		{
			// The body states the whole configuration, so a member it does not
			// name is a setting that is not configured. There is no partial save
			// and there must not appear to be one.
			name: "an omitted field is the same as an emptied one",
			body: `{"name":"Beta","expectedHead":"head-current"}`,
			want: DisplayChange{Name: "Beta", ExpectedHead: "head-current"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedDisplayChange{}
			handler := displayMutationHandler(t, recorded, displayMutationResult(), nil)
			response := requestJSON(t, handler, http.MethodPatch, "/api/display", test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("PATCH = %d; body = %s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(*recorded.change, test.want) {
				t.Fatalf("change = %#v, want %#v", *recorded.change, test.want)
			}
		})
	}
}

// A save that names no head is refused before anything is asked to apply it,
// and an empty head is a head: it is what a project whose configuration ledger
// has never been seeded reads.
func TestHandlerDisplayMutationRequiresAnExpectedHead(t *testing.T) {
	recorded := &recordedDisplayChange{}
	handler := displayMutationHandler(t, recorded, displayMutationResult(), nil)
	response := requestJSON(t, handler, http.MethodPatch, "/api/display", `{"name":"Beta"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with no head = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
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
		t.Fatal("a save with no head reached the capability")
	}

	empty := &recordedDisplayChange{}
	seeded := displayMutationHandler(t, empty, displayMutationResult(), nil)
	answer := requestJSON(t, seeded, http.MethodPatch, "/api/display", `{"name":"Beta","expectedHead":""}`)
	if answer.Code != http.StatusOK {
		t.Fatalf("PATCH with an empty head = %d, want %d; body = %s", answer.Code, http.StatusOK, answer.Body.String())
	}
	if empty.change.ExpectedHead != "" {
		t.Fatalf("expectedHead = %q, want the empty one the client read", empty.change.ExpectedHead)
	}
}

// A stale write is a 409 carrying the settings the save should be recomposed
// against, so the client adopts them without a refetch.
func TestHandlerDisplayMutationReportsStaleWritesWithTheCurrentSettings(t *testing.T) {
	stale := core.Errorf(core.CategoryStaleWrite,
		"this project's configuration has changed since head-old; reload and try again")
	handler := displayMutationHandler(t, &recordedDisplayChange{}, DisplayMutation{}, stale)

	response := requestJSON(t, handler, http.MethodPatch, "/api/display",
		`{"name":"Beta","expectedHead":"head-old"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("PATCH = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	assertSecurityHeaders(t, response.Result())
	var document DisplayErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	// The envelope is the ordinary one, so a client that only knows how to read
	// errors reads this one.
	if document.Format != "workbook.error" || document.Version != 1 {
		t.Fatalf("error envelope = %#v, want workbook.error v1", document)
	}
	if document.Error.Category != core.CategoryStaleWrite || document.Error.Message != stale.Error() {
		t.Fatalf("error body = %#v, want the stale write as it was reported", document.Error)
	}
	if document.Display == nil {
		t.Fatal("a stale write answered without the settings to re-render")
	}
	want := displayDocument(VocabularyState{
		Head:    "head-current",
		Display: core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"},
	})
	if !reflect.DeepEqual(*document.Display, want) {
		t.Fatalf("refusal settings = %#v, want the current ones %#v", *document.Display, want)
	}
}

// A refusal that is not about staleness carries no settings: there is nothing
// for the client to re-render, and a body that grew a member for every refusal
// would train nobody to read it.
func TestHandlerDisplayMutationMapsRefusalsToStatuses(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "validation", err: core.Errorf(core.CategoryValidation, `color "chartreuse" must be six hexadecimal digits`), wantStatus: http.StatusBadRequest},
		{name: "invocation", err: core.Errorf(core.CategoryInvocation, "a display setting is one of three"), wantStatus: http.StatusBadRequest},
		{name: "conflict", err: core.Errorf(core.CategoryConflict, "the configuration ledger disagrees"), wantStatus: http.StatusConflict},
		{name: "corrupt data", err: core.Errorf(core.CategoryCorruptData, "the ledger will not decode"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := displayMutationHandler(t, &recordedDisplayChange{}, DisplayMutation{}, test.err)
			response := requestJSON(t, handler, http.MethodPatch, "/api/display",
				`{"name":"Beta","expectedHead":"head-current"}`)
			if response.Code != test.wantStatus {
				t.Fatalf("PATCH = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			var document DisplayErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryOf(test.err) {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryOf(test.err))
			}
			if document.Display != nil {
				t.Fatalf("refusal carried settings it has no use for: %#v", *document.Display)
			}
		})
	}
}

// A stale write from a board whose configuration cannot be read is still a stale
// write. The client loses the re-render it would have got, not the refusal.
func TestHandlerDisplayStaleWriteSurvivesAnUnreadableConfiguration(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{}, core.Errorf(core.CategoryCorruptData, "cannot read this project's configuration")
		},
		SetDisplay: func(context.Context, DisplayChange) (DisplayMutation, error) {
			return DisplayMutation{}, core.Errorf(core.CategoryStaleWrite, "the configuration has changed; reload and try again")
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, "/api/display", `{"name":"Beta","expectedHead":"head-old"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("PATCH = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var document DisplayErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
	if document.Display != nil {
		t.Fatal("an unreadable configuration was reported as a configuration")
	}
}

// A body carrying a member this route does not have is refused rather than
// silently ignored, exactly as every other mutation body is.
func TestHandlerDisplayMutationRefusesUnknownMembers(t *testing.T) {
	recorded := &recordedDisplayChange{}
	handler := displayMutationHandler(t, recorded, displayMutationResult(), nil)
	response := requestJSON(t, handler, http.MethodPatch, "/api/display",
		`{"name":"Beta","colour":"blue","expectedHead":"head-current"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if recorded.calls != 0 {
		t.Fatal("a body with an unknown member reached the capability")
	}
}

// A board built without the capability says so rather than pretending.
func TestHandlerWithoutTheDisplayWriterReportsIt(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := requestJSON(t, handler, http.MethodPatch, "/api/display", `{"expectedHead":"head-current"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH = %d, want %d; body = %s", response.Code, http.StatusInternalServerError, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Error.Category != core.CategoryOperational {
		t.Fatalf("category = %q, want %q", document.Error.Category, core.CategoryOperational)
	}
}

// The route answers its own method and refuses the rest, naming what it allows.
// The gate is a switch of its own beside the mux, and a route missing from it is
// a route whose 405 silently becomes a 404 — so it is stated here.
func TestHandlerDisplayRouteEnforcesItsMethod(t *testing.T) {
	handler := displayMutationHandler(t, &recordedDisplayChange{}, displayMutationResult(), nil)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := requestJSON(t, handler, method, "/api/display", `{}`)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/display = %d, want %d; body = %s",
				method, response.Code, http.StatusMethodNotAllowed, response.Body.String())
		}
		if got := response.Header().Get("Allow"); got != http.MethodPatch {
			t.Fatalf("%s /api/display Allow = %q, want %q", method, got, http.MethodPatch)
		}
	}
}

// The address the configuration page used to answer is an ordinary unknown path
// now. Nothing forwards it: a redirect would be a second name for a page that
// has one, and the client reads its routes out of the address.
func TestHandlerNoLongerAnswersTheOldStatusesAddress(t *testing.T) {
	handler := administrableHandler(core.DefaultVocabulary(), "head-1", nil)
	response := request(t, handler, http.MethodGet, "/statuses")
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /statuses = %d, want %d", response.Code, http.StatusNotFound)
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("GET /statuses redirected to %q", location)
	}
}

// The statuses and the settings are read from one commit, so they travel in one
// document. A project that has configured nothing carries no display member at
// all, which is what keeps every answer this route gave before display settings
// existed the answer it gives now.
func TestHandlerVocabularyDocumentCarriesTheDisplaySettingsAtTheSameHead(t *testing.T) {
	configured := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: handlerVocabulary(t), Head: "head-9",
				Display: core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"},
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
	})
	response := request(t, configured, http.MethodGet, "/api/vocabulary")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d, want %d", response.Code, http.StatusOK)
	}
	var document VocabularyDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, response.Body.String())
	}
	if document.Display == nil {
		t.Fatal("a configured project's statuses came without its display settings")
	}
	if document.Display.Name != "Atlas" || document.Display.PrimaryColor != "#1a7f4b" || document.Display.TextColor != "" {
		t.Fatalf("display = %#v, want the settings the state carried", *document.Display)
	}
	if document.Display.Head != document.Head {
		t.Fatalf("display head = %q, statuses head = %q; one read has one head",
			document.Display.Head, document.Head)
	}

	unconfigured := NewHandler(Options{
		Vocabulary: staticVocabulary(handlerVocabulary(t), "head-9"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
	})
	plain := request(t, unconfigured, http.MethodGet, "/api/vocabulary")
	if strings.Contains(plain.Body.String(), "display") {
		t.Fatalf("an unconfigured project's document mentions a display: %s", plain.Body.String())
	}
}
