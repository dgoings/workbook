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

// What the board says about a project's task-ID keys, and what its two key
// routes do with a request.
//
// The keys are the third section of the same configuration ledger the statuses
// and the priorities live in, so almost everything here is those sections'
// shape: one read on entry, the same head discipline, the same wholesale
// adoption of whatever a change answers with, the same refusals in the verb
// family's own words. What is pinned here is what a key is that a status and a
// priority are not.
//
//   - A key is added and then only ever changes state. There is no rename, no
//     removal and no reorder, so there are two routes rather than six, and the
//     one per-key route carries exactly one of three intents.
//   - A key change costs no tasks. Every task ID ever minted keeps the key it
//     was minted under, so the mutation envelope prices nothing — and carries no
//     `tasks` member at all rather than a permanent zero.
//   - Being current is not a state. The stored document names the current key
//     once, so the view marks it with a member of its own beside the
//     active/retired word.

// projectKeys is a project that has moved its key: WB retired, NEW minting, and
// SPARE active but not current.
//
// It is deliberately hostile to every shortcut a reader of this document could
// take. The current key is neither the first member nor the only active one,
// the founding key is the retired one, and add order is not state order — so
// anything deriving "which key mints tasks" from a position fails here rather
// than in a project nobody tested with.
func projectKeys(t *testing.T) core.KeySet {
	t.Helper()
	keys, err := core.NewKeySet(core.KeyDocument{
		Keys: []core.KeyDefinition{
			{Key: "WB", Retired: true},
			{Key: "NEW"},
			{Key: "SPARE"},
		},
		Current: "NEW",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	return keys
}

// widenedKeys is that project with one more active key, for the shape test.
func widenedKeys(t *testing.T) core.KeySet {
	t.Helper()
	keys, err := core.NewKeySet(core.KeyDocument{
		Keys: []core.KeyDefinition{
			{Key: "WB", Retired: true},
			{Key: "NEW"},
			{Key: "SPARE"},
			{Key: "THIRD"},
		},
		Current: "NEW",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	return keys
}

// recordedKeyMutations captures what each route handed its capability, so a
// test can assert that the body reached it in the shape the contract promises.
type recordedKeyMutations struct {
	addition *VocabularyKeyAddition
	edited   string
	edit     *VocabularyKeyEdit
	calls    int
}

// keyMutationHandler builds a board whose two key mutations record their input
// and answer with the given result.
func keyMutationHandler(
	t *testing.T,
	recorded *recordedKeyMutations,
	mutation VocabularyKeyMutation,
	err error,
) http.Handler {
	t.Helper()
	answer := func() (VocabularyKeyMutation, error) {
		recorded.calls++
		return mutation, err
	}
	return NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: handlerVocabulary(t),
				Head:       "head-current",
				Priorities: projectPriorities(t),
				Keys:       projectKeys(t),
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
		AddKey: func(_ context.Context, addition VocabularyKeyAddition) (VocabularyKeyMutation, error) {
			recorded.addition = &addition
			return answer()
		},
		EditKey: func(_ context.Context, key string, change VocabularyKeyEdit) (VocabularyKeyMutation, error) {
			recorded.edited, recorded.edit = key, &change
			return answer()
		},
	})
}

// keyMutationResult is what a successful capability answers with: a
// configuration that is deliberately not the one the resolver reports, in every
// section, so a route that answered from the resolver rather than from the
// change would fail.
func keyMutationResult(t *testing.T) VocabularyKeyMutation {
	t.Helper()
	return VocabularyKeyMutation{
		State: VocabularyState{
			Vocabulary: handlerVocabulary(t),
			Head:       "head-written",
			Priorities: projectPriorities(t),
			Keys:       widenedKeys(t),
		},
	}
}

// keyRoutes is every key change, with a body the route accepts. One table
// drives the envelope, the head, the staleness and the unwired-board tests, so
// a route added without joining all four is a route one of them fails on.
func keyRoutes(head string) []struct {
	name   string
	method string
	target string
	body   string
} {
	quoted := `"expectedHead":` + quotedJSON(head)
	return []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/keys",
			body: `{"key":"THIRD",` + quoted + `}`},
		{name: "current", method: http.MethodPatch, target: "/api/vocabulary/keys/SPARE",
			body: `{"current":true,` + quoted + `}`},
	}
}

// The vocabulary document carries this project's keys: which ones there are,
// what each one is, and which mints new tasks.
//
// It is present on every answer rather than omitted when a project has recorded
// nothing about keys, for the reason the priorities are: a project that never
// ran `workbook key` still has a key, and a client handed no member would carry
// a fallback of its own.
func TestVocabularyDocumentCarriesTheProjectKeys(t *testing.T) {
	handler := keyMutationHandler(t, &recordedKeyMutations{}, keyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodGet, "/api/vocabulary", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var document VocabularyDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, response.Body.String())
	}
	if document.Keys.Current != "NEW" {
		t.Fatalf("keys.current = %q, want the key this project mints under", document.Keys.Current)
	}
	want := []KeyView{
		{Key: "WB", State: core.KeyStateRetired},
		{Key: "NEW", State: core.KeyStateActive, Current: true},
		{Key: "SPARE", State: core.KeyStateActive},
	}
	if !reflect.DeepEqual(document.Keys.Keys, want) {
		t.Fatalf("keys.keys = %#v, want %#v — add order, each with its state", document.Keys.Keys, want)
	}

	// The member is there for a project that has recorded nothing, and it is an
	// empty list rather than a null: a client iterating it has something to
	// iterate whatever this project has configured.
	bare := NewHandler(Options{List: func(context.Context) ([]core.Task, error) { return nil, nil }})
	response = requestJSON(t, bare, http.MethodGet, "/api/vocabulary", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d; body = %s", response.Code, response.Body.String())
	}
	var members struct {
		Keys *KeyVocabularyDocument `json:"keys"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &members); err != nil {
		t.Fatalf("decode vocabulary members: %v; body = %s", err, response.Body.String())
	}
	if members.Keys == nil {
		t.Fatalf("a project with no recorded keys was answered without the member: %s", response.Body.String())
	}
	if members.Keys.Keys == nil {
		t.Fatalf("keys.keys = null, want an empty list; body = %s", response.Body.String())
	}
}

// Adding a key answers with the whole configuration, in the shape GET
// /api/vocabulary serves it, so the client renders the result of a change
// through the code that rendered the page — the new head included, which is
// what its next change has to name.
func TestAddVocabularyKeyRecordsAndAnswersWithTheWholeConfiguration(t *testing.T) {
	result := keyMutationResult(t)
	recorded := &recordedKeyMutations{}
	handler := keyMutationHandler(t, recorded, result, nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/keys",
		`{"key":"THIRD","current":true,"expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/vocabulary/keys = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	assertSecurityHeaders(t, response.Result())
	var document VocabularyKeyMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.key-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned document", document)
	}
	if want := vocabularyDocument(result.State); !reflect.DeepEqual(document.Vocabulary, want) {
		t.Fatalf("mutation vocabulary = %#v, want %#v", document.Vocabulary, want)
	}
	if document.Vocabulary.Head != "head-written" {
		t.Fatalf("head = %q, want the one the change produced", document.Vocabulary.Head)
	}
	want := VocabularyKeyAddition{Key: "THIRD", Current: true, ExpectedHead: "head-current"}
	if recorded.addition == nil || !reflect.DeepEqual(*recorded.addition, want) {
		t.Fatalf("addition = %#v, want %#v", recorded.addition, want)
	}
	if recorded.calls != 1 {
		t.Fatalf("capability was called %d times, want once", recorded.calls)
	}
	// A key change moves no task: every ID ever minted keeps the key it was
	// minted under. So this envelope prices nothing rather than carrying a
	// member that would be zero for every key change this project will ever
	// record — which invites a client to branch on it and tells a reader that
	// keys take part in something they do not.
	if strings.Contains(response.Body.String(), `"tasks"`) {
		t.Fatalf("key answer prices a change that costs no tasks: %s", response.Body.String())
	}

	// An addition that says nothing about the current key is the ordinary one,
	// and it reaches the capability as the false it sent.
	recorded = &recordedKeyMutations{}
	handler = keyMutationHandler(t, recorded, result, nil)
	response = requestJSON(t, handler, http.MethodPost, "/api/vocabulary/keys",
		`{"key":"THIRD","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/vocabulary/keys = %d; body = %s", response.Code, response.Body.String())
	}
	if recorded.addition.Current {
		t.Fatalf("addition = %#v, want the current role it did not ask for left alone", *recorded.addition)
	}
}

// A change that names no head is refused before anything is asked to apply it,
// and the refusal names the member that is missing — exactly as a status or
// priority change is, and for the reason vocabularyHead gives.
func TestAddVocabularyKeyRequiresExpectedHead(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/keys", body: `{"key":"THIRD"}`},
		{name: "current", method: http.MethodPatch, target: "/api/vocabulary/keys/SPARE", body: `{"current":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedKeyMutations{}
			handler := keyMutationHandler(t, recorded, keyMutationResult(t), nil)
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

	// An empty head is a head, and every project that has never changed a key
	// reads one: sending it back is the client telling the truth about what it
	// saw. So this is the ordinary first key change rather than an edge case.
	recorded := &recordedKeyMutations{}
	handler := keyMutationHandler(t, recorded, keyMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/keys",
		`{"key":"THIRD","expectedHead":""}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST with an empty head = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	if recorded.addition.ExpectedHead != "" {
		t.Fatalf("expectedHead = %q, want the empty one the client read", recorded.addition.ExpectedHead)
	}
}

// The per-key route carries each of the three things that can happen to a key
// that already exists, and hands the key the path addressed to the capability.
func TestEditVocabularyKeyMakesCurrentRetiresAndReactivates(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want VocabularyKeyEdit
	}{
		{name: "made current", body: `{"current":true,"expectedHead":"head-current"}`,
			want: VocabularyKeyEdit{Current: true, ExpectedHead: "head-current"}},
		{name: "retired", body: `{"retire":true,"expectedHead":"head-current"}`,
			want: VocabularyKeyEdit{Retire: true, ExpectedHead: "head-current"}},
		{name: "reactivated", body: `{"reactivate":true,"expectedHead":"head-current"}`,
			want: VocabularyKeyEdit{Reactivate: true, ExpectedHead: "head-current"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedKeyMutations{}
			handler := keyMutationHandler(t, recorded, keyMutationResult(t), nil)
			response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/keys/SPARE", test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("PATCH key = %d, want %d; body = %s",
					response.Code, http.StatusOK, response.Body.String())
			}
			if recorded.edited != "SPARE" {
				t.Fatalf("edited key = %q, want the one the path addressed", recorded.edited)
			}
			if recorded.edit == nil || !reflect.DeepEqual(*recorded.edit, test.want) {
				t.Fatalf("edit = %#v, want %#v", recorded.edit, test.want)
			}
			var document VocabularyKeyMutationDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.key-mutation" {
				t.Fatalf("mutation envelope = %#v, want the key mutation's own", document)
			}
		})
	}
}

// One request is one intent. No planner reads two of current, retire and
// reactivate at once — they are three different changes to the same key — so
// the route refuses the combination rather than picking one, and refuses a body
// that names none rather than recording nothing.
func TestEditVocabularyKeyRefusesTwoIntentsInOneRequest(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "current and retire", body: `{"current":true,"retire":true,"expectedHead":"head-current"}`},
		{name: "retire and reactivate", body: `{"retire":true,"reactivate":true,"expectedHead":"head-current"}`},
		{name: "all three", body: `{"current":true,"retire":true,"reactivate":true,"expectedHead":"head-current"}`},
		{name: "none of them", body: `{"expectedHead":"head-current"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedKeyMutations{}
			handler := keyMutationHandler(t, recorded, keyMutationResult(t), nil)
			response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/keys/SPARE", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("PATCH key = %d, want %d; body = %s",
					response.Code, http.StatusBadRequest, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryInvocation {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryInvocation)
			}
			for _, word := range []string{"current", "retire", "reactivate"} {
				if !strings.Contains(document.Error.Message, word) {
					t.Fatalf("error message = %q, want it to name the three intents", document.Error.Message)
				}
			}
			if recorded.calls != 0 {
				t.Fatalf("a body naming %s reached the capability", test.name)
			}
		})
	}
}

// A board built without the key capabilities says so rather than pretending,
// the way every route reports a capability it was not given.
func TestKeyRoutesReportAnUnwiredBoard(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, test := range keyRoutes("head-current") {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusInternalServerError, response.Body.String())
			}
			var document ErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryOperational {
				t.Fatalf("category = %q, want %q", document.Error.Category, core.CategoryOperational)
			}
		})
	}
}

// The create route mints a task under a key the client names, and under the
// project's current key when it names none — which is what every client that
// predates a project having several keys sends.
func TestCreateTaskAcceptsAKey(t *testing.T) {
	created := core.Task{
		ID:       "SPARE-01J00000000000000000000009",
		TaskData: core.TaskData{Title: "Filed under another key"},
	}
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "a key the client chose",
			body: `{"title":"Filed under another key","key":"SPARE"}`, want: "SPARE"},
		{name: "no key at all",
			body: `{"title":"Filed under another key"}`, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var recorded core.CreateInput
			handler := NewHandler(Options{
				List: func(context.Context) ([]core.Task, error) { return nil, nil },
				Create: func(_ context.Context, input core.CreateInput) (core.MutationResult, error) {
					recorded = input
					return core.MutationResult{Task: created}, nil
				},
			})
			response := requestJSON(t, handler, http.MethodPost, "/api/tasks", test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("POST /api/tasks = %d, want %d; body = %s",
					response.Code, http.StatusOK, response.Body.String())
			}
			if recorded.Key != test.want {
				t.Fatalf("create input key = %q, want %q", recorded.Key, test.want)
			}
			if recorded.Title != "Filed under another key" {
				t.Fatalf("create input = %#v, want the title it carried too", recorded)
			}
		})
	}

	// And the body is still held to exactly what it accepts: a member these
	// routes do not have is refused rather than silently ignored.
	handler := NewHandler(Options{
		List:   func(context.Context) ([]core.Task, error) { return nil, nil },
		Create: unexpectedTaskCreate(t),
	})
	response := requestJSON(t, handler, http.MethodPost, "/api/tasks",
		`{"title":"Filed under another key","keys":["SPARE"]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST with an unknown member = %d, want %d; body = %s",
			response.Code, http.StatusBadRequest, response.Body.String())
	}
}

// The digest that rides on the task poll says nothing about the keys, and that
// is a decision rather than an omission.
//
// What the standing reload notice protects is the card nodes: rebuilding the
// columns to show a new status or a new priority would destroy a reader's open
// form, a change staged against a head, and a refusal they have not read.
// Adding a key changes no column and no priority, so nothing on the page has to
// be rebuilt to show it — the page picks a new key up from the answer it
// already re-renders from. A digest that moved for one would ask every open
// board to reload for something no card is drawn from.
func TestVocabularyShapeIgnoresKeys(t *testing.T) {
	base := VocabularyState{
		Vocabulary: handlerVocabulary(t),
		Head:       "head-1",
		Priorities: projectPriorities(t),
		Keys:       projectKeys(t),
	}
	shape := vocabularyShape(base)
	if shape == "" {
		t.Fatal("a configured project has no shape at all, so nothing here could be compared")
	}

	retired, err := core.NewKeySet(core.KeyDocument{
		Keys: []core.KeyDefinition{
			{Key: "WB", Retired: true},
			{Key: "NEW"},
			{Key: "SPARE", Retired: true},
		},
		Current: "NEW",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	moved, err := core.NewKeySet(core.KeyDocument{
		Keys: []core.KeyDefinition{
			{Key: "WB", Retired: true},
			{Key: "NEW"},
			{Key: "SPARE"},
		},
		Current: "SPARE",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	for _, test := range []struct {
		name string
		keys core.KeySet
	}{
		{name: "a key added", keys: widenedKeys(t)},
		{name: "a key retired", keys: retired},
		{name: "the current key moved", keys: moved},
		{name: "no keys read at all", keys: core.KeySet{}},
	} {
		changed := base
		changed.Head = "head-2"
		changed.Keys = test.keys
		if got := vocabularyShape(changed); got != shape {
			t.Errorf("%s moves the shape, so every open board is told to reload for a change no card is drawn from", test.name)
		}
	}

	// And the columns and the priorities still move it, so this test cannot
	// pass by digesting nothing at all.
	widened := base
	widened.Priorities = widenedPriorities(t)
	if vocabularyShape(widened) == shape {
		t.Fatal("a priority added leaves the shape where it was")
	}
}

// A stale key write is a 409 carrying the stale-write category the client
// matches on, and the configuration it should recompose the change against —
// keys included, which is the whole point of attaching it here.
func TestKeyMutationsReportStaleWritesWithTheCurrentConfiguration(t *testing.T) {
	stale := core.Errorf(core.CategoryStaleWrite,
		"this project's keys have changed since head-old; reload and try again")
	for _, test := range keyRoutes("head-old") {
		t.Run(test.name, func(t *testing.T) {
			handler := keyMutationHandler(t, &recordedKeyMutations{}, VocabularyKeyMutation{}, stale)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusConflict {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusConflict, response.Body.String())
			}
			var document VocabularyErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.error" || document.Version != 1 {
				t.Fatalf("error envelope = %#v, want workbook.error v1", document)
			}
			if document.Error.Category != core.CategoryStaleWrite || document.Error.Message != stale.Error() {
				t.Fatalf("error body = %#v, want the stale write as it was reported", document.Error)
			}
			if document.Vocabulary == nil {
				t.Fatal("a stale key write answered without the configuration to re-render")
			}
			if document.Vocabulary.Keys.Current != "NEW" {
				t.Fatalf("refusal keys = %#v, want the project's current ones", document.Vocabulary.Keys)
			}
		})
	}
}

// Every refusal the key verbs give reaches the client in the verbs' own words.
// The sentences here are the ones internal/cli asserts against the real
// planners; this test is about what the route does to them, which must be
// nothing but choose the status code.
func TestKeyRefusalsReachTheClientInTheVerbsOwnWords(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		target     string
		body       string
		err        error
		wantStatus int
	}{
		{
			name:   "a key this project already mints under",
			method: http.MethodPost, target: "/api/vocabulary/keys",
			body: `{"key":"NEW","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`project key "NEW" is already active and already current, so there is nothing to add`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a key that is not a key",
			method: http.MethodPost, target: "/api/vocabulary/keys",
			body: `{"key":"wb","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`project key "wb" must match ^[A-Z][A-Z0-9]{1,9}$`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "retiring the current key",
			method: http.MethodPatch, target: "/api/vocabulary/keys/NEW",
			body: `{"retire":true,"expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`project key "NEW" is this project's current key, so new tasks would have nowhere to go; `+
					"make another key current first: workbook key current <key>"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "minting under a retired key",
			method: http.MethodPatch, target: "/api/vocabulary/keys/WB",
			body: `{"current":true,"expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`project key "WB" is retired; bring it back first: workbook key add WB`),
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := keyMutationHandler(t, &recordedKeyMutations{}, VocabularyKeyMutation{}, test.err)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != test.wantStatus {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, test.wantStatus, response.Body.String())
			}
			var document VocabularyErrorDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
			}
			if document.Error.Category != core.CategoryOf(test.err) {
				t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryOf(test.err))
			}
			if document.Error.Message != test.err.Error() {
				t.Fatalf("error message = %q, want the verb's own words %q", document.Error.Message, test.err.Error())
			}
			// A refusal that is not about staleness carries no configuration:
			// there is nothing for the client to re-render.
			if document.Vocabulary != nil {
				t.Fatalf("refusal carried a configuration it has no use for: %#v", *document.Vocabulary)
			}
		})
	}
}

// A body carrying a member these routes do not have is refused rather than
// silently ignored, exactly as every other mutation body is.
func TestKeyMutationsRefuseUnknownMembers(t *testing.T) {
	recorded := &recordedKeyMutations{}
	handler := keyMutationHandler(t, recorded, keyMutationResult(t), nil)
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/keys",
			body: `{"key":"THIRD","label":"Third","expectedHead":"head-current"}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/keys/SPARE",
			body: `{"current":true,"key":"SPARE","expectedHead":"head-current"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
	if recorded.calls != 0 {
		t.Fatal("a body with an unknown member reached the capability")
	}
}

// The two addresses answer their own methods and refuse the rest, naming what
// they allow. There is no DELETE on a key: a key that has ever minted a task is
// a permanent name, and retirement is what the per-key PATCH carries.
func TestKeyRoutesEnforceTheirMethods(t *testing.T) {
	handler := keyMutationHandler(t, &recordedKeyMutations{}, keyMutationResult(t), nil)
	for _, test := range []struct {
		method string
		target string
		allow  string
	}{
		{method: http.MethodGet, target: "/api/vocabulary/keys", allow: http.MethodPost},
		{method: http.MethodPatch, target: "/api/vocabulary/keys", allow: http.MethodPost},
		{method: http.MethodPost, target: "/api/vocabulary/keys/SPARE", allow: http.MethodPatch},
		{method: http.MethodDelete, target: "/api/vocabulary/keys/SPARE", allow: http.MethodPatch},
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

// The page carries this project's keys and the one that mints new tasks, for
// the reason it carries the priorities: the client needs both before it has
// fetched anything, and must not guess.
func TestPageKeysCarryEveryKeyWithItsState(t *testing.T) {
	encoded := pageKeys(projectKeys(t))
	var published []KeyView
	if err := json.Unmarshal([]byte(encoded), &published); err != nil {
		t.Fatalf("decode the page's keys: %v; attribute = %s", err, encoded)
	}
	want := []KeyView{
		{Key: "WB", State: core.KeyStateRetired},
		{Key: "NEW", State: core.KeyStateActive, Current: true},
		{Key: "SPARE", State: core.KeyStateActive},
	}
	if !reflect.DeepEqual(published, want) {
		t.Fatalf("page keys = %#v, want %#v", published, want)
	}
	// A project this board cannot read keys for carries an empty list rather
	// than a null, so the attribute is always something the client can parse.
	if got := pageKeys(core.KeySet{}); got != "[]" {
		t.Fatalf("page keys for an unconfigured project = %s, want []", got)
	}
}

// The keys section of the configuration page is served only to a board that can
// administer keys, and both capabilities are required: a board wired for one of
// them would draw controls that look alike and fail differently.
func TestKeysAdministrableRequiresBothCapabilities(t *testing.T) {
	adder := func(context.Context, VocabularyKeyAddition) (VocabularyKeyMutation, error) {
		return VocabularyKeyMutation{}, nil
	}
	editor := func(context.Context, string, VocabularyKeyEdit) (VocabularyKeyMutation, error) {
		return VocabularyKeyMutation{}, nil
	}
	administrable := Options{
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
	}
	for _, test := range []struct {
		name  string
		build func(Options) Options
		want  bool
	}{
		{name: "both", want: true, build: func(options Options) Options {
			options.AddKey, options.EditKey = adder, editor
			return options
		}},
		{name: "only the adder", want: false, build: func(options Options) Options {
			options.AddKey = adder
			return options
		}},
		{name: "only the editor", want: false, build: func(options Options) Options {
			options.EditKey = editor
			return options
		}},
		{name: "neither", want: false, build: func(options Options) Options { return options }},
	} {
		t.Run(test.name, func(t *testing.T) {
			board := &handler{Options: test.build(administrable)}
			if got := board.keysAdministrable(); got != test.want {
				t.Fatalf("keysAdministrable() = %t, want %t", got, test.want)
			}
		})
	}

	// And it still requires the statuses, because /config answers 404 without
	// them: a section served onto a page nobody can reach is never seen.
	board := &handler{Options: Options{AddKey: adder, EditKey: editor}}
	if board.keysAdministrable() {
		t.Fatal("a board that cannot administer its statuses offered a keys section on a page it answers 404 for")
	}
}
