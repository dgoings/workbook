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

// What the six priority mutation routes do with a request, and what they say
// about a refusal. What the changes themselves mean is decided by the priority
// verb family and tested through the real wiring in internal/cli, which is also
// where the refusals below are asserted as the sentences the CLI actually
// writes rather than as sentences a test made up.

// recordedPriorityMutations captures what each route handed its capability, so
// a test can assert that the body reached it in the shape the contract
// promises.
type recordedPriorityMutations struct {
	addition   *VocabularyPriorityAddition
	edited     core.Priority
	edit       *VocabularyPriorityEdit
	removed    core.Priority
	removal    *VocabularyPriorityRemoval
	moved      core.Priority
	move       *VocabularyPriorityMove
	defaulted  core.Priority
	setDefault *VocabularyPriorityDefault
	recolored  core.Priority
	recolor    *VocabularyPriorityRecolor
	calls      int
}

// priorityMutationHandler builds a board whose six priority mutations record
// their input and answer with the given result.
func priorityMutationHandler(
	t *testing.T,
	recorded *recordedPriorityMutations,
	mutation VocabularyPriorityMutation,
	err error,
) http.Handler {
	t.Helper()
	answer := func() (VocabularyPriorityMutation, error) {
		recorded.calls++
		return mutation, err
	}
	return NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: handlerVocabulary(t),
				Head:       "head-current",
				Priorities: projectPriorities(t),
			}, nil
		},
		List: func(context.Context) ([]core.Task, error) { return nil, nil },
		AddPriority: func(_ context.Context, addition VocabularyPriorityAddition) (VocabularyPriorityMutation, error) {
			recorded.addition = &addition
			return answer()
		},
		EditPriority: func(_ context.Context, priority core.Priority, edit VocabularyPriorityEdit) (VocabularyPriorityMutation, error) {
			recorded.edited, recorded.edit = priority, &edit
			return answer()
		},
		RemovePriority: func(_ context.Context, priority core.Priority, removal VocabularyPriorityRemoval) (VocabularyPriorityMutation, error) {
			recorded.removed, recorded.removal = priority, &removal
			return answer()
		},
		MovePriority: func(_ context.Context, priority core.Priority, move VocabularyPriorityMove) (VocabularyPriorityMutation, error) {
			recorded.moved, recorded.move = priority, &move
			return answer()
		},
		SetDefaultPriority: func(_ context.Context, priority core.Priority, change VocabularyPriorityDefault) (VocabularyPriorityMutation, error) {
			recorded.defaulted, recorded.setDefault = priority, &change
			return answer()
		},
		RecolorPriority: func(_ context.Context, priority core.Priority, change VocabularyPriorityRecolor) (VocabularyPriorityMutation, error) {
			recorded.recolored, recorded.recolor = priority, &change
			return answer()
		},
	})
}

// priorityMutationResult is what a successful capability answers with: a
// configuration that is deliberately not the one the resolver reports, in both
// halves, so a route that answered from the resolver rather than from the
// change would fail.
func priorityMutationResult(t *testing.T) VocabularyPriorityMutation {
	t.Helper()
	vocabulary, err := core.NewVocabulary(
		[]core.StatusDefinition{
			{Status: "icebox", Label: "Icebox", Rank: "1/1", Tags: []core.StatusTag{}},
			{Status: "queued", Label: "Queued Up", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}},
			{Status: "landed", Label: "Landed", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return VocabularyPriorityMutation{
		State: VocabularyState{Vocabulary: vocabulary, Head: "head-written", Priorities: priorities},
		Tasks: VocabularyPriorityTaskCounts{Affected: 3},
	}
}

// priorityRoutes is every priority change, with a body the route accepts. One
// table drives the envelope, the head and the staleness tests, so a route that
// is added without joining all three is a route one of them fails on.
func priorityRoutes(head string) []struct {
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
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/priorities",
			body: `{"priority":"blocker",` + quoted + `}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/priorities/soon",
			body: `{"label":"Soon-ish",` + quoted + `}`},
		{name: "remove", method: http.MethodDelete, target: "/api/vocabulary/priorities/soon",
			body: `{"into":"low",` + quoted + `}`},
		{name: "move", method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/position",
			body: `{"before":"urgent",` + quoted + `}`},
		{name: "default", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/default",
			body: `{` + quoted + `}`},
		{name: "color", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/color",
			body: `{"color":"#b42318",` + quoted + `}`},
	}
}

// quotedJSON quotes a head for a JSON body, so a test's literal cannot
// disagree with the encoder about a value with a quote in it.
func quotedJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// Every priority mutation answers with the whole vocabulary — both halves of
// one configuration — in the shape GET /api/vocabulary serves it, including the
// head the client's next change must name.
func TestHandlerPriorityMutationsAnswerWithTheWholeVocabulary(t *testing.T) {
	result := priorityMutationResult(t)
	for _, test := range priorityRoutes("head-current") {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedPriorityMutations{}
			handler := priorityMutationHandler(t, recorded, result, nil)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusOK, response.Body.String())
			}
			assertSecurityHeaders(t, response.Result())
			var document VocabularyPriorityMutationDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
			}
			if document.Format != "workbook.priority-mutation" || document.Version != 1 {
				t.Fatalf("mutation envelope = %#v, want a versioned document", document)
			}
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

// The answer prices a priority change in the one number a priority change has,
// and does not carry the one it does not.
//
// `claimableAfter` says how many tasks became eligible for `workbook next`, and
// eligibility is decided by a status's tags and a task's dependencies: nothing
// about a priority gates it. Reusing the status mutation's envelope would ship
// that member as a permanent zero, which invites a client to branch on it and
// tells a reader that priorities take part in something they do not. This is
// asserted against the bytes rather than against the struct, because a struct
// with the member absent is exactly what a struct with the member present
// decodes into.
func TestHandlerPriorityMutationPricesOnlyWhatAPriorityChangeCosts(t *testing.T) {
	handler := priorityMutationHandler(t, &recordedPriorityMutations{}, priorityMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/color",
		`{"color":"#b42318","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH color = %d; body = %s", response.Code, response.Body.String())
	}
	var document struct {
		Tasks map[string]int `json:"tasks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	want := map[string]int{"affected": 3}
	if !reflect.DeepEqual(document.Tasks, want) {
		t.Fatalf("tasks = %#v, want %#v — a priority change has one count, not two", document.Tasks, want)
	}
	if strings.Contains(response.Body.String(), "claimableAfter") {
		t.Fatalf("priority answer carries claimableAfter: %s", response.Body.String())
	}
}

// Each body reaches its capability as the members the contract names, and an
// omitted member of a priority change is different from an emptied one.
func TestHandlerPriorityMutationsCarryTheirBodies(t *testing.T) {
	result := priorityMutationResult(t)

	t.Run("addition", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/priorities",
			`{"priority":"blocker","label":"Blocker","after":"urgent","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("POST priority = %d; body = %s", response.Code, response.Body.String())
		}
		want := VocabularyPriorityAddition{
			Priority: "blocker", Label: "Blocker", After: "urgent", ExpectedHead: "head-current",
		}
		if !reflect.DeepEqual(*recorded.addition, want) {
			t.Fatalf("addition = %#v, want %#v", *recorded.addition, want)
		}
	})

	t.Run("a change that names one member", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/soon",
			`{"name":"later","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH priority = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.edited != "soon" {
			t.Fatalf("edited priority = %q, want the one the path addressed", recorded.edited)
		}
		if recorded.edit.Name == nil || *recorded.edit.Name != "later" {
			t.Fatalf("edit name = %#v, want later", recorded.edit.Name)
		}
		if recorded.edit.Label != nil {
			t.Fatalf("edit = %#v, want the member it did not name left absent", *recorded.edit)
		}
	})

	t.Run("a change that empties a member", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/soon",
			`{"label":"","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH priority = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.edit.Label == nil || *recorded.edit.Label != "" {
			t.Fatalf("edit label = %#v, want an emptied member rather than an absent one", recorded.edit.Label)
		}
	})

	t.Run("removal", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodDelete, "/api/vocabulary/priorities/drop-everything",
			`{"into":"low","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("DELETE priority = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.removed != "drop-everything" {
			t.Fatalf("removed priority = %q, want the hyphenated one the path addressed", recorded.removed)
		}
		if recorded.removal.Into != "low" || recorded.removal.ExpectedHead != "head-current" {
			t.Fatalf("removal = %#v, want the destination and head the body named", *recorded.removal)
		}
	})

	t.Run("move", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/low/position",
			`{"before":"soon","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH position = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.moved != "low" {
			t.Fatalf("moved priority = %q, want the one the path addressed", recorded.moved)
		}
		want := VocabularyPriorityMove{Before: "soon", ExpectedHead: "head-current"}
		if !reflect.DeepEqual(*recorded.move, want) {
			t.Fatalf("move = %#v, want %#v", *recorded.move, want)
		}
	})

	t.Run("default", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/default",
			`{"expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH default = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.defaulted != "urgent" {
			t.Fatalf("defaulted priority = %q, want the one the path addressed", recorded.defaulted)
		}
		if recorded.setDefault.ExpectedHead != "head-current" {
			t.Fatalf("default = %#v, want the head the body named", *recorded.setDefault)
		}
	})

	t.Run("color", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/color",
			`{"color":" #B42318 ","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH color = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.recolored != "urgent" {
			t.Fatalf("recolored priority = %q, want the one the path addressed", recorded.recolored)
		}
		// Handed on as the reader typed it. Trimming and canonicalizing a color
		// is the verb's reading of what a color is, and the board keeps exactly
		// one of those.
		want := VocabularyPriorityRecolor{Color: " #B42318 ", ExpectedHead: "head-current"}
		if !reflect.DeepEqual(*recorded.recolor, want) {
			t.Fatalf("recolor = %#v, want %#v", *recorded.recolor, want)
		}
	})

	t.Run("a color cleared", func(t *testing.T) {
		recorded := &recordedPriorityMutations{}
		handler := priorityMutationHandler(t, recorded, result, nil)
		response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/color",
			`{"color":"","expectedHead":"head-current"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("PATCH color = %d; body = %s", response.Code, response.Body.String())
		}
		if recorded.recolor.Color != "" {
			t.Fatalf("recolor color = %q, want the clearing the client sent", recorded.recolor.Color)
		}
	})
}

// A recolor that names no color at all is refused rather than read as a
// clearing. Clearing a priority's ink is a decision somebody makes, and an
// absent member is a client that did not make it.
func TestHandlerPriorityRecolorRequiresAColor(t *testing.T) {
	recorded := &recordedPriorityMutations{}
	handler := priorityMutationHandler(t, recorded, priorityMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/color",
		`{"expectedHead":"head-current"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PATCH color = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if !strings.Contains(document.Error.Message, "color") {
		t.Fatalf("error message = %q, want it to name the missing member", document.Error.Message)
	}
	if recorded.calls != 0 {
		t.Fatal("a recolor naming no color reached the capability")
	}
}

// A change that names no head is refused before anything is asked to apply it,
// and the refusal names the member that is missing.
func TestHandlerPriorityMutationsRequireAnExpectedHead(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/priorities", body: `{"priority":"blocker"}`},
		{name: "edit", method: http.MethodPatch, target: "/api/vocabulary/priorities/soon", body: `{"label":"Soon-ish"}`},
		{name: "remove", method: http.MethodDelete, target: "/api/vocabulary/priorities/soon", body: `{"into":"low"}`},
		{name: "move", method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/position", body: `{"before":"urgent"}`},
		{name: "default", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/default", body: `{}`},
		{name: "color", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/color", body: `{"color":"#b42318"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedPriorityMutations{}
			handler := priorityMutationHandler(t, recorded, priorityMutationResult(t), nil)
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
	// truth about what it saw. Every project that has never changed a priority
	// is such a project, so this is the ordinary first priority change rather
	// than an edge case.
	recorded := &recordedPriorityMutations{}
	handler := priorityMutationHandler(t, recorded, priorityMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/priorities",
		`{"priority":"blocker","expectedHead":""}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST with an empty head = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	if recorded.addition.ExpectedHead != "" {
		t.Fatalf("expectedHead = %q, want the empty one the client read", recorded.addition.ExpectedHead)
	}
}

// A stale write is a 409 carrying the stale-write category the client matches
// on, and the configuration it should recompose the change against — priorities
// included, which is the whole point of attaching it here.
func TestHandlerPriorityMutationsReportStaleWritesWithTheCurrentVocabulary(t *testing.T) {
	stale := core.Errorf(core.CategoryStaleWrite,
		"this project's priorities have changed since head-old; reload and try again")
	for _, test := range priorityRoutes("head-old") {
		t.Run(test.name, func(t *testing.T) {
			handler := priorityMutationHandler(t, &recordedPriorityMutations{}, VocabularyPriorityMutation{}, stale)
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
				t.Fatal("a stale priority write answered without the configuration to re-render")
			}
			want := vocabularyDocument(VocabularyState{
				Vocabulary: handlerVocabulary(t),
				Head:       "head-current",
				Priorities: projectPriorities(t),
			})
			if !reflect.DeepEqual(*document.Vocabulary, want) {
				t.Fatalf("refusal configuration = %#v, want the current one %#v", *document.Vocabulary, want)
			}
		})
	}
}

// Every refusal the priority verbs give reaches the client in the verbs' own
// words. A generic body is a regression even where the status code is right:
// each of these sentences tells a person what to do next, and four of them are
// the whole reason a panel's Save can be pressed safely.
//
// The sentences here are the ones internal/cli asserts against the real
// planners; this test is about what the route does to them, which must be
// nothing but choose the status code.
func TestHandlerPriorityRefusalsReachTheClientInTheVerbsOwnWords(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		target     string
		body       string
		err        error
		wantStatus int
	}{
		{
			name:   "a recolor that changes nothing",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/color",
			body: `{"color":"#b42318","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`priority "urgent" already has that color`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a clearing of ink that is not there",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/color",
			body: `{"color":"","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`priority "soon" has no color to clear`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a removal with nowhere to forward to",
			method: http.MethodDelete, target: "/api/vocabulary/priorities/soon",
			body: `{"into":"","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryInvocation,
				"removing a priority requires naming where its tasks belong; "+
					"this project's priorities are: urgent, soon, low"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "the last priority a project has",
			method: http.MethodDelete, target: "/api/vocabulary/priorities/soon",
			body: `{"into":"low","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`priority delete cannot remove "soon"; it is this project's only priority, and every task has to be `+
					"at one; add another first: workbook priority add <priority>"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "an edit that changes nothing",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/soon",
			body: `{"name":"soon","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`priority "soon" was given nothing to change`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a priority this project does not have",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/nowhere",
			body: `{"label":"Nowhere","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryNotFound,
				`no priority "nowhere" in this project; the priorities are: urgent, soon, low`),
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "a placement naming both neighbors",
			method: http.MethodPost, target: "/api/vocabulary/priorities",
			body: `{"priority":"blocker","before":"soon","after":"low","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryInvocation,
				"a priority is placed before or after another priority, not both"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a move naming neither neighbor",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/position",
			body: `{"expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryInvocation,
				"moving a priority requires naming the priority it goes before or after"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a role the priority already holds",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/default",
			body: `{"expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`priority "soon" already carries the "default" tag`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "a color that is not a color",
			method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/color",
			body: `{"color":"puce","expectedHead":"head-current"}`,
			err: core.Errorf(core.CategoryValidation,
				`color "puce" must be a hex value such as #4f46e5`),
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := priorityMutationHandler(t, &recordedPriorityMutations{}, VocabularyPriorityMutation{}, test.err)
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
			// there is nothing for the client to re-render, and a body that grew
			// a member for every refusal would train nobody to read it.
			if document.Vocabulary != nil {
				t.Fatalf("refusal carried a configuration it has no use for: %#v", *document.Vocabulary)
			}
		})
	}
}

// The placement contradiction is the writer's refusal to give, not the route's.
//
// The status route refuses "before and after" itself; this one deliberately
// does not, because the priority writer already refuses it — and it refuses it
// where `workbook priority add` refuses it, in one sentence tested once. A
// second check here would be a second place the sentence could change.
func TestHandlerPriorityPlacementContradictionIsTheWritersToRefuse(t *testing.T) {
	recorded := &recordedPriorityMutations{}
	handler := priorityMutationHandler(t, recorded, priorityMutationResult(t), nil)
	response := requestJSON(t, handler, http.MethodPost, "/api/vocabulary/priorities",
		`{"priority":"blocker","before":"soon","after":"low","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST priority = %d; body = %s", response.Code, response.Body.String())
	}
	if recorded.addition.Before != "soon" || recorded.addition.After != "low" {
		t.Fatalf("addition = %#v, want both anchors handed to the writer", *recorded.addition)
	}
}

// A stale write from a board whose configuration cannot be read is still a
// stale write. The client loses the re-render it would have got, not the
// refusal.
func TestHandlerPriorityStaleWriteSurvivesAnUnreadableVocabulary(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{}, core.Errorf(core.CategoryCorruptData, "cannot read this project's configuration")
		},
		RecolorPriority: func(context.Context, core.Priority, VocabularyPriorityRecolor) (VocabularyPriorityMutation, error) {
			return VocabularyPriorityMutation{}, core.Errorf(core.CategoryStaleWrite,
				"this project's priorities have changed since head-old; reload and try again")
		},
	})
	response := requestJSON(t, handler, http.MethodPatch, "/api/vocabulary/priorities/urgent/color",
		`{"color":"#b42318","expectedHead":"head-old"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("PATCH color = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var document VocabularyErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, response.Body.String())
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
	if document.Vocabulary != nil {
		t.Fatal("an unreadable configuration was reported as a configuration")
	}
}

// A body carrying a member these routes do not have is refused rather than
// silently ignored, exactly as every other mutation body is.
func TestHandlerPriorityMutationsRefuseUnknownMembers(t *testing.T) {
	recorded := &recordedPriorityMutations{}
	handler := priorityMutationHandler(t, recorded, priorityMutationResult(t), nil)
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "add", method: http.MethodPost, target: "/api/vocabulary/priorities",
			body: `{"priority":"blocker","colour":"blue","expectedHead":"head-current"}`},
		{name: "color", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/color",
			body: `{"color":"#b42318","tags":["default"],"expectedHead":"head-current"}`},
		{name: "default", method: http.MethodPatch, target: "/api/vocabulary/priorities/urgent/default",
			body: `{"priority":"urgent","expectedHead":"head-current"}`},
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

// A board built without these capabilities says so rather than pretending, the
// way every route reports a capability it was not given. The six are asked for
// separately because they are six capabilities: a board wired for some of them
// is a board whose panel would draw controls that look alike and fail
// differently.
func TestHandlerWithoutPriorityMutationsReportsThem(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, test := range priorityRoutes("head-current") {
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

// The routes answer their own methods and refuse the rest, naming what they
// allow — including the three per-member addresses, which the method table has
// to know about separately from the priority they hang off.
func TestHandlerPriorityRoutesEnforceTheirMethods(t *testing.T) {
	handler := priorityMutationHandler(t, &recordedPriorityMutations{}, priorityMutationResult(t), nil)
	for _, test := range []struct {
		method string
		target string
		allow  string
	}{
		{method: http.MethodGet, target: "/api/vocabulary/priorities", allow: http.MethodPost},
		{method: http.MethodDelete, target: "/api/vocabulary/priorities", allow: http.MethodPost},
		{method: http.MethodPost, target: "/api/vocabulary/priorities/soon", allow: "PATCH, DELETE"},
		{method: http.MethodPut, target: "/api/vocabulary/priorities/soon/position", allow: http.MethodPatch},
		{method: http.MethodGet, target: "/api/vocabulary/priorities/soon/default", allow: http.MethodPatch},
		{method: http.MethodDelete, target: "/api/vocabulary/priorities/soon/color", allow: http.MethodPatch},
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

// An address these routes do not have is not one of them. A member nobody
// defined is a 404 rather than a method refusal that claims a route exists.
func TestHandlerPriorityRoutesAnswerOnlyTheirOwnAddresses(t *testing.T) {
	handler := priorityMutationHandler(t, &recordedPriorityMutations{}, priorityMutationResult(t), nil)
	for _, test := range []struct {
		method string
		target string
	}{
		{method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/nonsense"},
		{method: http.MethodPatch, target: "/api/vocabulary/priorities/soon/color/extra"},
		// And any other method on an address nobody defined. This is the case
		// the method table has to get right: an arm that matched every member
		// beneath a priority would answer this 405 and name a verb, which tells
		// a reader the address exists.
		{method: http.MethodGet, target: "/api/vocabulary/priorities/soon/nonsense"},
		{method: http.MethodDelete, target: "/api/vocabulary/priorities/soon/nonsense"},
	} {
		response := requestJSON(t, handler, test.method, test.target, `{"expectedHead":"head-current"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want %d; body = %s",
				test.method, test.target, response.Code, http.StatusNotFound, response.Body.String())
		}
	}
}
