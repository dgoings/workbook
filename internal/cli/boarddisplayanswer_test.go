package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// What a vocabulary change answers with, and the data loss that followed from
// answering with half of it.
//
// A status or priority change answers with the whole configuration document,
// because the client adopts what it is handed: the statuses panel, the priority
// panel and the board settings form are all redrawn from one answer. The board
// settings form is the half that had no producer filling it. A change recorded
// the statuses and the priorities it had just written and left the display
// settings zero, so the answer said this project has configured no name and no
// colors — the client believed it, blanked the three fields, and the reader's
// next Save recorded `cleared project-name` over a board they had named.
//
// The values are not recoverable from the form once it has been blanked, and
// nothing about the sequence looks like a failure: the change succeeded, the
// save succeeded, and the name is simply gone. So the fix is that the answer is
// complete, and these tests are about what the answer carries rather than about
// what the client does with it — the third one is the loss end to end, asserted
// against the ledger, because the ledger is what the reader lost.

// configuredBoard names a board and picks its colors through the board's own
// settings form, and answers with the head that save produced.
func configuredBoard(t *testing.T, addr string) webui.DisplayMutationDocument {
	t.Helper()
	before := boardVocabularyDocument(t, addr)
	saved := boardDisplaySave(t, addr,
		`{"name":"Scratch Board","primaryColor":"#1a7f4b","textColor":"#3b2a1a","expectedHead":`+
			quoteJSON(before.Head)+`}`)
	if saved.Display.Name != "Scratch Board" {
		t.Fatalf("the board was not named: %#v", saved.Display)
	}
	return saved
}

// decodeAnsweredVocabulary reads the vocabulary document out of any mutation
// answer, whichever of the two envelopes it arrived in. Both carry the document
// GET /api/vocabulary serves under the same member, which is what lets one
// client adopt either.
func decodeAnsweredVocabulary(t *testing.T, body []byte) webui.VocabularyDocument {
	t.Helper()
	var envelope struct {
		Vocabulary webui.VocabularyDocument `json:"vocabulary"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode mutation answer: %v; body = %s", err, body)
	}
	if envelope.Vocabulary.Format != "workbook.vocabulary" || envelope.Vocabulary.Version != 1 {
		t.Fatalf("answered vocabulary = %#v, want the document GET /api/vocabulary serves",
			envelope.Vocabulary)
	}
	return envelope.Vocabulary
}

// assertAnsweredDisplay checks that one mutation's vocabulary document carries
// the settings the project actually has, at the head the change produced.
func assertAnsweredDisplay(t *testing.T, document webui.VocabularyDocument) {
	t.Helper()
	if document.Display == nil {
		t.Fatalf("the change answered with no display settings, which reads as a board "+
			"that has configured none; document head = %q", document.Head)
	}
	if document.Display.Name != "Scratch Board" ||
		document.Display.PrimaryColor != "#1a7f4b" ||
		document.Display.TextColor != "#3b2a1a" {
		t.Fatalf("answered display = %#v, want the settings this project has", *document.Display)
	}
	// One commit, one head: the settings a change answers with are the settings
	// that change left behind, not the ones the session opened with.
	if document.Display.Head != document.Head {
		t.Fatalf("display head = %q, statuses head = %q; one answer has one head",
			document.Display.Head, document.Head)
	}
	if document.Display.Theme == "" {
		t.Fatalf("answered display carries no theme, so the board would be redrawn blank: %#v",
			*document.Display)
	}
}

// A priority change answers with the project's display settings, because the
// client redraws the board settings form out of the same document it redraws
// the priority panel from.
func TestBoardPriorityChangeAnswersWithTheDisplaySettings(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	saved := configuredBoard(t, addr)

	// The gesture the reviewer pressed: Up on a priority row, which is a move
	// placing it before its neighbor.
	moved := boardPriorityChange(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/priorities/medium/position",
		`{"before":"high","expectedHead":`+quoteJSON(saved.Display.Head)+`}`)
	assertAnsweredDisplay(t, moved.Vocabulary)
}

// A status change answers with them too. It always was the same hole — the
// statuses reached it first and for longer — and a board settings form blanked
// by a column rename loses exactly as much as one blanked by a priority move.
func TestBoardStatusChangeAnswersWithTheDisplaySettings(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	saved := configuredBoard(t, addr)

	added := boardVocabularyMutation(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":`+quoteJSON(saved.Display.Head)+`}`)
	assertAnsweredDisplay(t, added.Vocabulary)
}

// The loss itself, in the order a reader meets it: name the board, change the
// vocabulary, then save the settings form without touching it.
//
// The save sends what the client's form holds, and what the form holds is what
// the last answer told it — that is the whole mechanism, so the test sends
// exactly that rather than the values it wishes the form had. An answer that
// carried no display settings blanks the three fields, and this save then
// records the blanks. The assertion is against the ledger and not against the
// answer, because a reader who lost their board's name lost it there.
func TestBoardVocabularyChangeLeavesTheSavedDisplaySettingsStanding(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   func(head string) string
	}{
		{
			name:   "a priority moved up",
			method: http.MethodPatch,
			path:   "/api/vocabulary/priorities/medium/position",
			body:   func(head string) string { return `{"before":"high","expectedHead":` + quoteJSON(head) + `}` },
		},
		{
			name:   "a status added",
			method: http.MethodPost,
			path:   "/api/vocabulary/statuses",
			body:   func(head string) string { return `{"status":"triage","expectedHead":` + quoteJSON(head) + `}` },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := initializedRepository(t)
			addr := startServeBoard(t, repository)
			saved := configuredBoard(t, addr)

			contents, status := boardRequest(t, test.method,
				"http://"+addr+test.path, test.body(saved.Display.Head))
			if status != http.StatusOK {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.path, status, http.StatusOK, contents)
			}
			answered := decodeAnsweredVocabulary(t, contents)

			// The form as the client now holds it: the settings the answer
			// carried, and nothing at all where it carried none.
			form := webui.DisplayDocument{}
			if answered.Display != nil {
				form = *answered.Display
			}
			boardDisplaySave(t, addr,
				`{"name":`+quoteJSON(form.Name)+
					`,"primaryColor":`+quoteJSON(form.PrimaryColor)+
					`,"textColor":`+quoteJSON(form.TextColor)+
					`,"expectedHead":`+quoteJSON(answered.Head)+`}`)

			shown := decodeConfigShow(t, mustRun(t, repository, "config", "show", "--json"))
			for _, want := range []struct {
				setting string
				value   string
			}{
				{core.DisplayProjectName, "Scratch Board"},
				{core.DisplayPrimaryColor, "#1a7f4b"},
				{core.DisplayTextColor, "#3b2a1a"},
			} {
				view := settingView(t, shown, want.setting)
				if view.Value != want.value || view.Source != "configured" {
					t.Errorf("%s after the change and an untouched save = %#v, want %q as this project configured it",
						want.setting, view, want.value)
				}
			}
		})
	}
}
