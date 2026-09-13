package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// What the board's priority administration routes do, through the real wiring.
//
// The handler-level tests in internal/webui prove the envelopes; these prove
// the thing that matters most about this half of the feature — that the board
// is a second surface over the priority verb family rather than a second
// implementation of it, and that nothing between the planner and the client
// flattens a refusal somebody has to act on. The refusals below are asserted as
// whole sentences, and the shared ledger is read back through the CLI.

// A recolor through the board is a recolor: the CLI sees it, the ledger moves,
// and the answer prices the change in the one term a priority change has.
func TestBoardRecolorsAPriorityThroughTheSharedLedger(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	body, status := boardRequest(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/priorities/high/color",
		`{"color":"#b42318","expectedHead":`+quoteJSON(before.Head)+`}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH color = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	var document webui.VocabularyPriorityMutationDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, body)
	}
	if document.Format != "workbook.priority-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned priority document", document)
	}
	if document.Vocabulary.Head == before.Head || document.Vocabulary.Head == "" {
		t.Fatalf("head = %q, want a head past %q", document.Vocabulary.Head, before.Head)
	}
	// The answer carries both halves of the configuration, because the client
	// adopts what it is handed: an answer that left the statuses zero would tell
	// a board that this project uses the built-in six.
	if len(document.Vocabulary.Statuses) != 5 {
		t.Fatalf("statuses in a priority answer = %d, want this project's five", len(document.Vocabulary.Statuses))
	}
	recolored := ""
	for _, definition := range document.Vocabulary.Priorities.Priorities {
		if definition.Priority == "high" {
			recolored = definition.Color
		}
	}
	if recolored != "#b42318" {
		t.Fatalf("high's color in the answer = %q, want the one the change recorded", recolored)
	}
	// The member a priority change cannot answer is not in the bytes. See
	// webui.VocabularyPriorityTaskCounts: nothing about a priority decides
	// whether a task is eligible for `workbook next`, so an answer carrying
	// `claimableAfter` would carry a permanent zero.
	if strings.Contains(string(body), "claimableAfter") {
		t.Fatalf("priority answer carries claimableAfter: %s", body)
	}

	// The CLI reads the same ledger, which is the whole point of the exercise.
	for _, priority := range cliPriorityList(t, repository).Priorities {
		if priority.Priority == "high" && priority.Color != "#b42318" {
			t.Fatalf("priority list reports high as %q, want the board's color", priority.Color)
		}
	}
}

// A rename and the default role are two requests, the second composed against
// the head the first answered with. It is an asymmetry with the statuses, where
// one form is one change, and it is the shape a panel has to be written to: the
// role is one operation the fold transfers, and no planner renames and
// transfers in one pack.
func TestBoardTakesARenameAndTheDefaultRoleAsTwoChainedChanges(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	renamed := boardPriorityChange(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/priorities/low",
		`{"name":"someday","label":"Someday","expectedHead":`+quoteJSON(head)+`}`)
	if renamed.Vocabulary.Priorities.Default == "someday" {
		t.Fatal("the rename took the default role with it; the two are separate changes")
	}

	// The second request names the head the first produced, which is exactly
	// what a panel's Save has to carry forward.
	tagged := boardPriorityChange(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/priorities/someday/default",
		`{"expectedHead":`+quoteJSON(renamed.Vocabulary.Head)+`}`)
	if tagged.Vocabulary.Priorities.Default != "someday" {
		t.Fatalf("default after the second change = %q, want someday",
			tagged.Vocabulary.Priorities.Default)
	}

	// And the CLI agrees about both halves of what the panel did.
	listed := cliPriorityList(t, repository)
	if listed.Default != "someday" {
		t.Fatalf("priority list default = %q, want someday", listed.Default)
	}
}

// Every refusal reaches the client in the priority verbs' own words.
//
// These are the sentences a person acts on, and a generic body is a regression
// even where the status code is right. Four of them are load-bearing: a Save
// pressed with no edits, a removal with nowhere to forward to, a recolor that
// would record nothing, and a change composed against a configuration somebody
// else has moved. The first priority write on a project backfills the built-in
// three and stamps a marker that parks every teammate on an older build, so a
// change that changes nothing must be refused rather than recorded — and the
// refusal has to read as an explanation.
func TestBoardRefusesPriorityChanges(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantError  string
	}{
		{
			name: "a token that is not a token", method: http.MethodPost, path: "/api/vocabulary/priorities",
			body:       `{"priority":"Not A Token","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority "Not A Token" must be lowercase letters and digits separated by single hyphens`,
		},
		{
			name: "a priority the project already defines", method: http.MethodPost, path: "/api/vocabulary/priorities",
			body:       `{"priority":"high","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `this project already defines priority "high"`,
		},
		{
			name: "a placement naming both neighbors", method: http.MethodPost, path: "/api/vocabulary/priorities",
			body:       `{"priority":"urgent","before":"high","after":"low","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "a priority is placed before or after another priority, not both",
		},
		{
			name: "a priority nothing resolves", method: http.MethodPatch, path: "/api/vocabulary/priorities/nowhere",
			body:       `{"name":"somewhere","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no priority "nowhere" in this project; the priorities are: high, medium, low`,
		},
		{
			name: "an edit that sets nothing", method: http.MethodPatch, path: "/api/vocabulary/priorities/high",
			body:       `{"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority "high" was given nothing to change`,
		},
		{
			name: "an edit that repeats what is stored", method: http.MethodPatch, path: "/api/vocabulary/priorities/high",
			body:       `{"name":"high","label":"High","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority "high" already has that label`,
		},
		{
			name: "a removal with nowhere to forward to", method: http.MethodDelete, path: "/api/vocabulary/priorities/high",
			body:       `{"into":"","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError: "removing a priority requires naming where its tasks belong; " +
				"this project's priorities are: high, medium, low",
		},
		{
			name: "a removal into itself", method: http.MethodDelete, path: "/api/vocabulary/priorities/high",
			body:       `{"into":"high","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority delete cannot forward "high" into itself; name where its tasks belong`,
		},
		{
			name: "a removal into a priority that is not there", method: http.MethodDelete, path: "/api/vocabulary/priorities/high",
			body:       `{"into":"nowhere","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no priority "nowhere" in this project; the priorities are: high, medium, low`,
		},
		{
			name: "a move naming neither neighbor", method: http.MethodPatch, path: "/api/vocabulary/priorities/high/position",
			body:       `{"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "moving a priority requires naming the priority it goes before or after",
		},
		{
			name: "a move naming both", method: http.MethodPatch, path: "/api/vocabulary/priorities/high/position",
			body:       `{"before":"medium","after":"low","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "a priority moves before or after another priority, not both",
		},
		{
			name: "the role the priority already holds", method: http.MethodPatch, path: "/api/vocabulary/priorities/medium/default",
			body:       `{"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority "medium" already carries the "default" tag`,
		},
		{
			name: "a color that is not a color", method: http.MethodPatch, path: "/api/vocabulary/priorities/high/color",
			body:       `{"color":"puce","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `color "puce" must be six hexadecimal digits behind a hash, as in #1a7f4b`,
		},
		{
			name: "a clearing of ink that is not there", method: http.MethodPatch, path: "/api/vocabulary/priorities/high/color",
			body:       `{"color":"","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `priority "high" has no color to clear`,
		},
		// From here down the sentences are this surface's own: they are about
		// the request rather than about the vocabulary, and a message naming
		// --into or --before would be wrong in an HTTP error.
		{
			name: "a change with no head", method: http.MethodPatch, path: "/api/vocabulary/priorities/high",
			body:       `{"label":"Urgent"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "expectedHead is required; it names the vocabulary this change was composed against",
		},
		{
			name: "a recolor that names no color", method: http.MethodPatch, path: "/api/vocabulary/priorities/high/color",
			body:       `{"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "a priority color change must name a color; send an empty one to clear it",
		},
		{
			name: "a change against a head that has moved", method: http.MethodPatch, path: "/api/vocabulary/priorities/high",
			body:       `{"label":"Urgent","expectedHead":"0000000000000000000000000000000000000000"}`,
			wantStatus: http.StatusConflict,
			wantError: "this project's priorities have changed since 0000000000000000000000000000000000000000; " +
				"reload and try again",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, status := boardRequest(t, test.method, "http://"+addr+test.path, test.body)
			if status != test.wantStatus {
				t.Fatalf("%s %s = %d, want %d; body = %s", test.method, test.path, status, test.wantStatus, body)
			}
			var document webui.VocabularyErrorDocument
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, body)
			}
			if document.Format != "workbook.error" || document.Version != 1 {
				t.Fatalf("error envelope = %#v, want workbook.error v1", document)
			}
			if document.Error.Message != test.wantError {
				t.Fatalf("error message = %q, want %q", document.Error.Message, test.wantError)
			}
		})
	}

	// Nothing above was recorded: a refused change leaves the ledger where it
	// was, which is what makes the retry safe — and, here, what keeps a Save
	// pressed with no edits from stamping the marker that parks a team.
	if after := boardVocabularyDocument(t, addr).Head; after != head {
		t.Fatalf("configuration head moved to %q from %q; a refused change wrote something", after, head)
	}
}

// A project cannot be left with no priorities, and the board refuses the last
// removal in the words the verb refuses it with — naming the command that makes
// the removal possible.
func TestBoardRefusesToRemoveTheLastPriority(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	head := boardVocabularyDocument(t, addr).Head
	for _, removed := range []string{"high", "low"} {
		mutation := boardPriorityChange(t, http.MethodDelete,
			"http://"+addr+"/api/vocabulary/priorities/"+removed,
			`{"into":"medium","expectedHead":`+quoteJSON(head)+`}`)
		head = mutation.Vocabulary.Head
	}

	body, status := boardRequest(t, http.MethodDelete,
		"http://"+addr+"/api/vocabulary/priorities/medium",
		`{"into":"medium","expectedHead":`+quoteJSON(head)+`}`)
	if status != http.StatusBadRequest {
		t.Fatalf("removing the last priority = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
	var document webui.VocabularyErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	want := `priority delete cannot remove "medium"; it is this project's only priority, and every task has to be ` +
		"at one; add another first: workbook priority add <priority>"
	if document.Error.Message != want {
		t.Fatalf("error message = %q, want %q", document.Error.Message, want)
	}
	if after := boardVocabularyDocument(t, addr).Head; after != head {
		t.Fatalf("configuration head moved to %q from %q; the refused removal wrote something", after, head)
	}
}

// The refusal a client can act on carries what it needs to act: a stale write
// reports 409, the stale-write category, and the configuration the reader is
// actually looking at — priorities included, which is the half that made
// attaching it worth doing here.
func TestBoardAnswersAStalePriorityWriteWithTheCurrentConfiguration(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	stale := boardVocabularyDocument(t, addr).Head

	// Somebody else changes a priority while the panel is open.
	if code, _, stderr := run(t, repository, "priority", "add", "urgent", "--before", "high", "--no-sync"); code != 0 {
		t.Fatalf("priority add = code %d; stderr = %q", code, stderr)
	}
	current := boardVocabularyDocument(t, addr)
	if current.Head == stale {
		t.Fatal("the fixture did not move the configuration head")
	}

	body, status := boardRequest(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/priorities/high/color",
		`{"color":"#b42318","expectedHead":`+quoteJSON(stale)+`}`)
	if status != http.StatusConflict {
		t.Fatalf("stale recolor = %d, want %d; body = %s", status, http.StatusConflict, body)
	}
	var document webui.VocabularyErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
	if document.Error.Message != "this project's priorities have changed since "+stale+"; reload and try again" {
		t.Fatalf("error message = %q, want the priority half's own wording", document.Error.Message)
	}
	if document.Vocabulary == nil {
		t.Fatal("a stale priority write answered without the configuration to adopt")
	}
	if document.Vocabulary.Head != current.Head {
		t.Fatalf("refusal head = %q, want the current %q", document.Vocabulary.Head, current.Head)
	}
	names := make([]string, 0, 4)
	for _, definition := range document.Vocabulary.Priorities.Priorities {
		names = append(names, string(definition.Priority))
	}
	if strings.Join(names, ",") != "urgent,high,medium,low" {
		t.Fatalf("refusal priorities = %v, want the ones the teammate left behind", names)
	}
}

// boardPriorityChange makes one priority change through the board and decodes
// the answer, failing the test on anything but a recorded change.
func boardPriorityChange(t *testing.T, method, url, body string) webui.VocabularyPriorityMutationDocument {
	t.Helper()
	contents, status := boardRequest(t, method, url, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d, want %d; body = %s", method, url, status, http.StatusOK, contents)
	}
	var document webui.VocabularyPriorityMutationDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, contents)
	}
	if document.Format != "workbook.priority-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned priority document", document)
	}
	return document
}
