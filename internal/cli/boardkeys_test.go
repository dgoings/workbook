package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// What the board does with a project's task-ID keys through the real `serve`
// wiring. The handler's own tests hold their own fakes, so only a test through
// this wiring can show that a body reaches a planner, that a refusal is the
// planner's own sentence, and that the set a request is answered from is the
// set on disk now rather than the one this process opened with.

// boardKeyDocument reads the keys off the board's vocabulary route.
func boardKeyDocument(t *testing.T, addr string) webui.KeyVocabularyDocument {
	t.Helper()
	return boardVocabularyDocument(t, addr).Keys
}

// boardKeyChange performs one key change through the board and returns what
// it answered with, failing the test on anything but a recorded change. It is
// named for the change rather than for the mutation type, which this package
// already spells boardKeyMutation.
func boardKeyChange(t *testing.T, method, url, body string) webui.VocabularyKeyMutationDocument {
	t.Helper()
	contents, status := boardRequest(t, method, url, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d, want %d; body = %s", method, url, status, http.StatusOK, contents)
	}
	var document webui.VocabularyKeyMutationDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode key mutation: %v; body = %s", err, contents)
	}
	if document.Format != "workbook.key-mutation" || document.Version != 1 {
		t.Fatalf("key mutation envelope = %#v, want workbook.key-mutation v1", document)
	}
	return document
}

// boardKeyStates renders a key document as "KEY:state" pairs, current marked,
// so a failure prints what the board actually reported rather than a struct
// dump. It is the board's counterpart to key_test.go's keyStates, which asks
// the same question of `workbook key list`.
func boardKeyStates(document webui.KeyVocabularyDocument) string {
	rendered := make([]string, 0, len(document.Keys))
	for _, view := range document.Keys {
		word := string(view.State)
		if view.Current {
			word = "current"
		}
		rendered = append(rendered, view.Key+":"+word)
	}
	return strings.Join(rendered, " ")
}

// The board administers the key section of the ledger through the key verb
// family's own planners: it adds a key, moves the current one, retires one and
// brings it back, and every answer is the whole configuration with the keys as
// they now stand.
func TestBoardAdministersProjectKeysThroughItsRoutes(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	before := boardKeyDocument(t, addr)
	if before.Current != "WB" || boardKeyStates(before) != "WB:current" {
		t.Fatalf("keys = %q, want the founding key alone and current", boardKeyStates(before))
	}
	head := boardVocabularyDocument(t, addr).Head

	added := boardKeyChange(t, http.MethodPost, "http://"+addr+"/api/vocabulary/keys",
		`{"key":"NEW","expectedHead":`+quoteJSON(head)+`}`)
	if got := boardKeyStates(added.Vocabulary.Keys); got != "WB:current NEW:active" {
		t.Fatalf("keys after an add = %q, want the new key active and the founding one still minting", got)
	}
	if added.Vocabulary.Head == head {
		t.Fatal("the key add answered with the head it was composed against")
	}

	current := boardKeyChange(t, http.MethodPatch, "http://"+addr+"/api/vocabulary/keys/NEW",
		`{"current":true,"expectedHead":`+quoteJSON(added.Vocabulary.Head)+`}`)
	if got := boardKeyStates(current.Vocabulary.Keys); got != "WB:active NEW:current" {
		t.Fatalf("keys after making NEW current = %q, want the role moved", got)
	}

	retired := boardKeyChange(t, http.MethodPatch, "http://"+addr+"/api/vocabulary/keys/WB",
		`{"retire":true,"expectedHead":`+quoteJSON(current.Vocabulary.Head)+`}`)
	if got := boardKeyStates(retired.Vocabulary.Keys); got != "WB:retired NEW:current" {
		t.Fatalf("keys after retiring WB = %q, want it retired and still listed", got)
	}

	// Reactivation is the add route's other reading, and it reaches the same
	// planner: a retired key comes back in the place it already had.
	reactivated := boardKeyChange(t, http.MethodPatch, "http://"+addr+"/api/vocabulary/keys/WB",
		`{"reactivate":true,"expectedHead":`+quoteJSON(retired.Vocabulary.Head)+`}`)
	if got := boardKeyStates(reactivated.Vocabulary.Keys); got != "WB:active NEW:current" {
		t.Fatalf("keys after reactivating WB = %q, want it active in the place it had", got)
	}

	// And the CLI sees exactly what the board recorded, because there is one
	// ledger and one set of planners writing it.
	code, stdout, stderr := run(t, repository, "key", "list", "--json")
	if code != 0 {
		t.Fatalf("key list = code %d; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, `"current":"NEW"`) {
		t.Fatalf("key list = %s, want NEW current", stdout)
	}

	// The answer carries the rest of the configuration too, because the client
	// adopts a mutation answer wholesale: a state that left the statuses or the
	// priorities zero would tell a project that named its own that it has the
	// built-in ones.
	if len(reactivated.Vocabulary.Statuses) == 0 || len(reactivated.Vocabulary.Priorities.Priorities) == 0 {
		t.Fatalf("key mutation answered with half a configuration: %#v", reactivated.Vocabulary)
	}
}

// Every key refusal reaches the board's client in the verb family's own words,
// with the status code the category maps to. These are the sentences
// internal/cli asserts against the planners; what is pinned here is that the
// board's wiring reaches those planners rather than a second reading of them.
func TestBoardRefusesKeyChangesInTheVerbsOwnWords(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	for _, test := range []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		want       string
	}{
		{
			name:   "a key this project already mints under",
			method: http.MethodPost, target: "/api/vocabulary/keys",
			body:       `{"key":"WB","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			want:       `project key "WB" is already active and already current, so there is nothing to add`,
		},
		{
			name:   "a key that is not a key",
			method: http.MethodPost, target: "/api/vocabulary/keys",
			body:       `{"key":"wb","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			want:       `project key "wb" must match`,
		},
		{
			name:   "retiring the only active key",
			method: http.MethodPatch, target: "/api/vocabulary/keys/WB",
			body:       `{"retire":true,"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			want:       `is this project's only active key`,
		},
		{
			name:   "a key this project does not have",
			method: http.MethodPatch, target: "/api/vocabulary/keys/NOPE",
			body:       `{"current":true,"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			want:       `no project key "NOPE" in this project`,
		},
		{
			name:   "two intents in one request",
			method: http.MethodPatch, target: "/api/vocabulary/keys/WB",
			body:       `{"current":true,"retire":true,"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			want:       "a key change names exactly one of current, retire or reactivate",
		},
		{
			name:   "a change composed against keys that have moved",
			method: http.MethodPost, target: "/api/vocabulary/keys",
			body:       `{"key":"NEW","expectedHead":"0000000000000000000000000000000000000000"}`,
			wantStatus: http.StatusConflict,
			want:       "reload and try again",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, status := boardRequest(t, test.method, "http://"+addr+test.target, test.body)
			if status != test.wantStatus {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, status, test.wantStatus, body)
			}
			var document webui.VocabularyErrorDocument
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, body)
			}
			if !strings.Contains(document.Error.Message, test.want) {
				t.Fatalf("error message = %q, want it to contain %q", document.Error.Message, test.want)
			}
		})
	}

	// Nothing above was recorded, so the project still has the one key it
	// started with.
	if got := boardKeyStates(boardKeyDocument(t, addr)); got != "WB:current" {
		t.Fatalf("keys after six refusals = %q, want the project left as it was", got)
	}
}

// A board is open for hours and a teammate's `workbook key add` reaches this
// checkout while it is. The key set is therefore resolved per request rather
// than once at startup, and this is the test that distinguishes the two:
// nothing is restarted, a request has already been served — so a snapshot would
// have been taken and memoized — and then both halves of the board have to
// move, the keys it reports and the keys its writes mint under.
//
// The second half is the one worth naming. The resolver alone would let the
// board offer a key chooser the service then refused to mint under, because the
// service's own membership check reads the set it was built with.
func TestBoardMintsUnderAKeyAddedWhileItWasRunning(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	before := boardKeyDocument(t, addr)
	if before.Current != "WB" {
		t.Fatalf("keys = %q, want the founding key current", boardKeyStates(before))
	}

	// Another process moves the project's key, exactly as a teammate's pull
	// would.
	if code, _, stderr := run(t, repository, "key", "add", "NEW", "--current", "--no-sync"); code != 0 {
		t.Fatalf("key add NEW --current = code %d; stderr = %q", code, stderr)
	}

	after := boardKeyDocument(t, addr)
	if after.Current != "NEW" {
		t.Fatalf("keys = %q, want NEW current; the board is holding a snapshot", boardKeyStates(after))
	}

	// And the write boundary agrees with what the board reports: a task created
	// through the board mints under the key the project moved to.
	created, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks",
		`{"title":"Filed after the key moved","description":"","status":"backlog","priority":"medium","labels":[]}`)
	task := decodeServeMutation(t, created, status)
	if !strings.HasPrefix(task.ID, "NEW-") {
		t.Fatalf("created task ID = %q, want it minted under the key added while serve was running", task.ID)
	}

	// The retired-or-unknown refusal comes from the same fresh set: a key this
	// project does not have is refused rather than minted under.
	body, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks",
		`{"title":"Filed under nothing","key":"NOPE"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create under an unknown key = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
	// The refusal names the active keys, and it names the pair this project has
	// now rather than the one key serve opened with — which is the same freshness
	// the mint above shows, said by the service's own sentence.
	var refusal webui.ErrorDocument
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	want := `no project key "NOPE" in this project; its active keys are: WB, NEW`
	if refusal.Error.Message != want {
		t.Fatalf("refusal = %q, want the service's own sentence %q", refusal.Error.Message, want)
	}
}

// The create route mints under any active key the client chooses, which is what
// the create form's chooser sends. The board and the CLI agree about where the
// task went, because one service answered both.
func TestBoardCreatesATaskUnderAChosenKey(t *testing.T) {
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "key", "add", "NEW", "--no-sync"); code != 0 {
		t.Fatalf("key add NEW = code %d; stderr = %q", code, stderr)
	}
	addr := startServeBoard(t, repository)

	created, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks",
		`{"title":"Filed under the other key","key":"NEW"}`)
	task := decodeServeMutation(t, created, status)
	if !strings.HasPrefix(task.ID, "NEW-") {
		t.Fatalf("created task ID = %q, want it minted under the chosen key", task.ID)
	}
	code, stdout, stderr := run(t, repository, "list", "--key", "NEW", "--json")
	if code != 0 {
		t.Fatalf("list --key NEW = code %d; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, task.ID) {
		t.Fatalf("list --key NEW = %s, want the task the board minted", stdout)
	}
}

// A key change through the board leaves the generated guidelines describing a
// key the project has moved off, and says so rather than writing the file: the
// board does not write files, for the reason boardVocabulary.apply gives, and
// the next key verb or `workbook docs update` settles it.
func TestBoardReportsTheStaleGuidelinesAfterAKeyChange(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head
	before := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(before, "`WB-`") {
		t.Fatalf("the generated guidelines do not name the founding key:\n%s", before)
	}

	document := boardKeyChange(t, http.MethodPost, "http://"+addr+"/api/vocabulary/keys",
		`{"key":"NEW","current":true,"expectedHead":`+quoteJSON(head)+`}`)
	if document.Vocabulary.Keys.Current != "NEW" {
		t.Fatalf("keys = %q, want NEW minting", boardKeyStates(document.Vocabulary.Keys))
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != before {
		t.Fatal("the board rewrote the generated guidelines from an HTTP request")
	}
	warned := false
	for _, warning := range document.Warnings {
		if warning.Code == core.WarningDocsRefresh && strings.Contains(warning.Message, agentdocs.GuidelinesPath) {
			warned = true
			if !strings.Contains(warning.Message, "workbook docs update") {
				t.Fatalf("docs warning = %q, want the command that refreshes the file", warning.Message)
			}
		}
	}
	if !warned {
		t.Fatalf("warnings = %#v, want one naming the guidelines the board did not rewrite", document.Warnings)
	}

	// And the next CLI key verb settles the file, which is the correct-on-touch
	// rule this decision leans on.
	if code, _, stderr := run(t, repository, "key", "add", "THIRD", "--no-sync"); code != 0 {
		t.Fatalf("key add THIRD = code %d; stderr = %q", code, stderr)
	}
	settled := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(settled, "`NEW-`") {
		t.Fatalf("the CLI did not settle the guidelines the board left stale:\n%s", settled)
	}
}

// Every board write into the configuration ledger answers with this project's
// keys, not only the key routes.
//
// The client adopts a mutation answer wholesale. A status or priority or
// display write that left the keys zero would not say "this answer is about
// statuses", it would say "this project has no keys" — and the create form's
// chooser would empty out and its default would vanish on the strength of a
// column rename. That is the hazard the display settings already learned the
// hard way; see boardDisplay.set's own note.
func TestBoardWritesAnswerWithTheProjectsKeys(t *testing.T) {
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "key", "add", "NEW", "--current", "--no-sync"); code != 0 {
		t.Fatalf("key add NEW --current = code %d; stderr = %q", code, stderr)
	}
	addr := startServeBoard(t, repository)

	head := boardVocabularyDocument(t, addr).Head
	renamed := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/in-progress",
		`{"name":"doing","expectedHead":`+quoteJSON(head)+`}`)
	if got := boardKeyStates(renamed.Vocabulary.Keys); got != "WB:active NEW:current" {
		t.Fatalf("a status rename answered with keys %q, want this project's own", got)
	}

	priorities := boardPriorityChange(t, http.MethodPost,
		"http://"+addr+"/api/vocabulary/priorities",
		`{"priority":"blocker","expectedHead":`+quoteJSON(renamed.Vocabulary.Head)+`}`)
	if got := boardKeyStates(priorities.Vocabulary.Keys); got != "WB:active NEW:current" {
		t.Fatalf("a priority add answered with keys %q, want this project's own", got)
	}

	// The display save carries no vocabulary at all, so it cannot lose the keys
	// that way — but it carries the digest of the whole configuration, and the
	// keys are in that. A save that read its state without them would answer
	// with a shape naming no keys, and the page would raise its reload notice
	// for a change nobody made.
	save := boardDisplaySave(t, addr, `{"name":"Atlas","expectedHead":`+quoteJSON(priorities.Vocabulary.Head)+`}`)
	after := boardVocabularyDocument(t, addr)
	if save.Shape != after.Shape {
		t.Fatalf("display save shape = %q, want the configuration's own %q", save.Shape, after.Shape)
	}
	if got := boardKeyStates(after.Keys); got != "WB:active NEW:current" {
		t.Fatalf("keys after three writes = %q, want this project's own", got)
	}
}
