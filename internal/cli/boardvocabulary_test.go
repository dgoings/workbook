package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// What the board's status administration routes do, through the real wiring.
//
// The handler-level tests in internal/webui prove the envelopes; these prove the
// thing that matters most about this feature — that the board is a second
// surface over the status verb family rather than a second implementation of it.
// Every refusal below is asserted as the sentence `workbook status` produces,
// and the shared ledger is read back through the CLI.

// A rename through the board is a rename: the CLI sees it, the old value
// forwards to the new one, and the ledger records the pack `status rename`
// records rather than something the board invented.
func TestBoardRenamesAStatusThroughTheSharedLedger(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	document := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/in-progress",
		`{"name":"doing","expectedHead":`+quoteJSON(before.Head)+`}`)
	if document.Vocabulary.Head == before.Head {
		t.Fatalf("vocabulary head = %q, want a head past %q", document.Vocabulary.Head, before.Head)
	}
	if !containsStatus(document.Vocabulary.Statuses, "doing") || containsStatus(document.Vocabulary.Statuses, "in-progress") {
		t.Fatalf("statuses after the rename = %s, want doing and no in-progress",
			statusNames(document.Vocabulary.Statuses))
	}

	// The CLI reads the same ledger, which is the whole point of the exercise.
	code, stdout, stderr := run(t, repository, "status", "list", "--json")
	if code != 0 {
		t.Fatalf("status list code = %d, want 0; stderr = %q", code, stderr)
	}
	var listed statusListResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "status list").Data, &listed); err != nil {
		t.Fatalf("decode status list: %v", err)
	}
	if listed.Head != document.Vocabulary.Head {
		t.Fatalf("status list head = %q, want the head the board reported %q", listed.Head, document.Vocabulary.Head)
	}
	renamed := false
	for _, view := range listed.Statuses {
		if view.Status == "doing" {
			renamed = true
			// The label was derived from the old name, so the rename re-derives
			// it: the CLI's own derived-label rule, reached through the board.
			if view.Label != "Doing" {
				t.Fatalf("renamed status label = %q, want the re-derived %q", view.Label, "Doing")
			}
		}
	}
	if !renamed {
		t.Fatalf("status list = %#v, want the status the board renamed", listed.Statuses)
	}
	forwarded := false
	for _, retired := range listed.Retired {
		if retired.Status == "in-progress" && retired.Becomes == "doing" &&
			retired.Operation == core.ConfigStatusRename {
			forwarded = true
		}
	}
	if !forwarded {
		t.Fatalf("retired values = %#v, want in-progress forwarding to doing", listed.Retired)
	}

	// The pack is the verb's pack: a rename, then the relabel the derived-label
	// rule asks for. Reconciliation classifies that shape by name, so a board
	// that invented its own would be a board whose changes a teammate's clone
	// could not fold.
	operations := configPackOperations(t, repository, document.Vocabulary.Head)
	if len(operations) != 2 ||
		operations[0].Type != core.ConfigStatusRename || operations[0].From != "in-progress" || operations[0].To != "doing" ||
		operations[1].Type != core.ConfigStatusRelabel || operations[1].Status != "doing" {
		t.Fatalf("recorded operations = %#v, want the rename-then-relabel pack the verb records", operations)
	}
	if subject := gitOutput(t, repository, "log", "-1", "--format=%s", document.Vocabulary.Head); subject != "workbook: rename status in-progress to doing" {
		t.Fatalf("ledger commit subject = %q, want the verb's own subject", subject)
	}
}

// A status added through the board lands where the board asked for it, carries
// the tags it was given, and takes the default tag off whoever held it — the
// exclusivity rule, enforced by the same authoring gate the CLI writes through.
func TestBoardAddsAPositionedTaggedStatus(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)
	previousDefault := before.Default

	document := boardVocabularyMutation(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses",
		`{"status":"triage","label":"Triage","tags":["default"],"before":"`+string(before.Statuses[0].Status)+`",`+
			`"expectedHead":`+quoteJSON(before.Head)+`}`)
	if got := document.Vocabulary.Statuses[0].Status; got != "triage" {
		t.Fatalf("first column = %q, want the status placed before every other", got)
	}
	if document.Vocabulary.Default != "triage" {
		t.Fatalf("default status = %q, want triage", document.Vocabulary.Default)
	}
	for _, definition := range document.Vocabulary.Statuses {
		if definition.Status == previousDefault && definition.HasTag(core.StatusTagDefault) {
			t.Fatalf("status %q still carries the default tag; exactly one status may hold it", previousDefault)
		}
	}
	// A change that moved no tasks still says so, so a client reads the same
	// member from every mutation.
	if document.Tasks.Affected != 0 || document.Tasks.ClaimableAfter != 0 {
		t.Fatalf("task counts = %#v, want zeroes for a change that moved nothing", document.Tasks)
	}
}

// Removing a column says what it costs: how many tasks move, and how many of
// them an agent can claim where they land. The CLI reports both, so the panel
// that is about to ask "are you sure" can too.
func TestBoardReportsWhatARemovalMoves(t *testing.T) {
	repository := initializedRepository(t)
	for _, title := range []string{"First", "Second"} {
		code, stdout, stderr := run(t, repository, "create", title, "--status", "in-review", "--json")
		if code != 0 {
			t.Fatalf("create %s = code %d; stdout = %q stderr = %q", title, code, stdout, stderr)
		}
	}
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	document := boardVocabularyMutation(t, http.MethodDelete,
		"http://"+addr+"/api/vocabulary/statuses/in-review",
		`{"into":"ready","expectedHead":`+quoteJSON(before.Head)+`}`)
	if containsStatus(document.Vocabulary.Statuses, "in-review") {
		t.Fatalf("statuses after the removal = %s, want no in-review", statusNames(document.Vocabulary.Statuses))
	}
	if document.Tasks.Affected != 2 {
		t.Fatalf("tasks affected = %d, want the 2 tasks that were in the removed column", document.Tasks.Affected)
	}
	// `ready` is this project's next-tagged status, so both tasks become
	// claimable where they land.
	if document.Tasks.ClaimableAfter != 2 {
		t.Fatalf("claimable after = %d, want 2", document.Tasks.ClaimableAfter)
	}
	retired := false
	for _, entry := range document.Vocabulary.Retired {
		if entry.Status == "in-review" && entry.Destination == "ready" {
			retired = true
		}
	}
	if !retired {
		t.Fatalf("retired values = %#v, want in-review forwarding to ready", document.Vocabulary.Retired)
	}
}

// A drag across the board is one intent: the whole order, applied in one commit.
func TestBoardReordersEveryColumnInOneCommit(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	reversed := make([]string, 0, len(before.Statuses))
	for index := len(before.Statuses) - 1; index >= 0; index-- {
		reversed = append(reversed, string(before.Statuses[index].Status))
	}
	document := boardVocabularyMutation(t, http.MethodPut, "http://"+addr+"/api/vocabulary/order",
		`{"statuses":`+string(mustJSONBytes(t, reversed))+`,"expectedHead":`+quoteJSON(before.Head)+`}`)
	if got := statusNames(document.Vocabulary.Statuses); got != strings.Join(reversed, ",") {
		t.Fatalf("order after the drag = %q, want %q", got, strings.Join(reversed, ","))
	}
	// One commit, carrying a reorder for every status the reversal actually
	// moved — which is every status but the middle one, whose place in a
	// reversed list is the place it was already in.
	operations := configPackOperations(t, repository, document.Vocabulary.Head)
	if len(operations) != len(reversed)-1 {
		t.Fatalf("recorded %d operations, want one reorder per moved status (%d)",
			len(operations), len(reversed)-1)
	}
	for _, operation := range operations {
		if operation.Type != core.ConfigStatusReorder {
			t.Fatalf("recorded operation %#v, want only reorders", operation)
		}
	}

	// The CLI agrees about the order, which is what a teammate's `status list`
	// will show them.
	code, stdout, stderr := run(t, repository, "status", "list", "--json")
	if code != 0 {
		t.Fatalf("status list code = %d; stderr = %q", code, stderr)
	}
	var listed statusListResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "status list").Data, &listed); err != nil {
		t.Fatalf("decode status list: %v", err)
	}
	names := make([]string, len(listed.Statuses))
	for index, view := range listed.Statuses {
		names[index] = string(view.Status)
	}
	if strings.Join(names, ",") != strings.Join(reversed, ",") {
		t.Fatalf("status list order = %q, want %q", strings.Join(names, ","), strings.Join(reversed, ","))
	}
	// The commit's inverse is honest about moving one status back out of many.
	newest := newestStatusLogEntry(t, repository)
	if newest.Inverse == nil {
		t.Fatalf("newest status log entry = %#v, want an inverse", newest)
	}
	if newest.Inverse.Exact {
		t.Fatal("the inverse of a whole-board reorder claims to be exact")
	}
}

// Every refusal the board gives is the refusal the verb gives, because both ask
// the same function. The messages below are quoted from the CLI's own tests, and
// a divergence in either surface fails here.
func TestBoardRefusesStatusChangesInTheCLIsWords(t *testing.T) {
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
			name: "a token that is not a token", method: http.MethodPost, path: "/api/vocabulary/statuses",
			body:       `{"status":"Not A Token","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `status "Not A Token" must be lowercase letters and digits separated by single hyphens`,
		},
		{
			name: "a placement against a status that is not there", method: http.MethodPost, path: "/api/vocabulary/statuses",
			body:       `{"status":"triage","after":"nowhere","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no status "nowhere" in this project; the statuses are: backlog, ready, in-progress, in-review, done`,
		},
		{
			name: "a status the project already defines", method: http.MethodPost, path: "/api/vocabulary/statuses",
			body:       `{"status":"ready","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `this project already defines status "ready"`,
		},
		{
			name: "a tag that does not exist", method: http.MethodPost, path: "/api/vocabulary/statuses",
			body:       `{"status":"triage","tags":["urgent"],"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `unsupported status tag "urgent"; the tags are: default, done, next`,
		},
		{
			name: "a status nothing resolves", method: http.MethodPatch, path: "/api/vocabulary/statuses/nowhere",
			body:       `{"name":"somewhere","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no status "nowhere" in this project; the statuses are: backlog, ready, in-progress, in-review, done`,
		},
		{
			name: "a removal with nowhere to forward to", method: http.MethodDelete, path: "/api/vocabulary/statuses/ready",
			body:       `{"into":"","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError: "removing a status requires naming where its tasks belong; " +
				"this project's statuses are: backlog, ready, in-progress, in-review, done",
		},
		{
			name: "a removal into itself", method: http.MethodDelete, path: "/api/vocabulary/statuses/ready",
			body:       `{"into":"ready","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `status delete cannot forward "ready" into itself; name where its tasks belong`,
		},
		{
			name: "a removal into a status that is not there", method: http.MethodDelete, path: "/api/vocabulary/statuses/ready",
			body:       `{"into":"nowhere","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no status "nowhere" in this project; the statuses are: backlog, ready, in-progress, in-review, done`,
		},
		{
			name: "an order that names some of the statuses", method: http.MethodPut, path: "/api/vocabulary/order",
			body:       `{"statuses":["ready","backlog"],"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError: "the order names 2 of this project's 5 statuses; name each of them exactly once: " +
				"backlog, ready, in-progress, in-review, done",
		},
		{
			name: "an order that names one twice", method: http.MethodPut, path: "/api/vocabulary/order",
			body: `{"statuses":["ready","ready","backlog","in-progress","in-review","done"],` +
				`"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `the order names status "ready" twice; name each of this project's statuses exactly once`,
		},
		{
			name: "an order that changes nothing", method: http.MethodPut, path: "/api/vocabulary/order",
			body: `{"statuses":["backlog","ready","in-progress","in-review","done"],` +
				`"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "this project's statuses are already in that order",
		},
		{
			name: "an order naming a status this project does not have", method: http.MethodPut, path: "/api/vocabulary/order",
			body: `{"statuses":["backlog","ready","in-progress","in-review","done","nowhere"],` +
				`"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusNotFound,
			wantError:  `no status "nowhere" in this project; the statuses are: backlog, ready, in-progress, in-review, done`,
		},
		{
			name: "a change with no head", method: http.MethodPatch, path: "/api/vocabulary/statuses/ready",
			body:       `{"label":"Next Up"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "expectedHead is required; it names the vocabulary this change was composed against",
		},
		{
			name: "a change against a head that has moved", method: http.MethodPatch, path: "/api/vocabulary/statuses/ready",
			body:       `{"label":"Next Up","expectedHead":"0000000000000000000000000000000000000000"}`,
			wantStatus: http.StatusConflict,
			wantError: "this project's statuses have changed since 0000000000000000000000000000000000000000; " +
				"reload and try again",
		},
		{
			name: "a change that changes nothing", method: http.MethodPatch, path: "/api/vocabulary/statuses/ready",
			body:       `{"label":"Ready","expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  `status "ready" already has that label`,
		},
		{
			name: "a change that sets nothing", method: http.MethodPatch, path: "/api/vocabulary/statuses/ready",
			body:       `{"expectedHead":` + quoteJSON(head) + `}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "a status change must set at least one of name, label or tags",
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
	// was, which is what makes the retry safe.
	if after := boardVocabularyDocument(t, addr).Head; after != head {
		t.Fatalf("vocabulary head moved to %q from %q; a refused change wrote something", after, head)
	}
}

// The refusal a client can act on carries what it needs to act: a stale write
// reports 409, the stale-write category the queue matches on, and the statuses
// the reader is actually looking at.
func TestBoardAnswersAStaleVocabularyWriteWithTheCurrentOne(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	stale := boardVocabularyDocument(t, addr).Head

	// Somebody else changes a status while the panel is open.
	if code, _, stderr := run(t, repository, "status", "add", "icebox", "--no-sync"); code != 0 {
		t.Fatalf("status add = code %d; stderr = %q", code, stderr)
	}
	current := boardVocabularyDocument(t, addr)
	if current.Head == stale {
		t.Fatal("the fixture did not move the vocabulary head")
	}

	body, status := boardRequest(t, http.MethodPatch, "http://"+addr+"/api/vocabulary/statuses/ready",
		`{"label":"Next Up","expectedHead":`+quoteJSON(stale)+`}`)
	if status != http.StatusConflict {
		t.Fatalf("stale change = %d, want %d; body = %s", status, http.StatusConflict, body)
	}
	var document webui.VocabularyErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
	}
	if document.Vocabulary == nil {
		t.Fatal("a stale vocabulary write answered without the vocabulary the client should re-render")
	}
	if document.Vocabulary.Head != current.Head {
		t.Fatalf("refusal carried head %q, want the current %q", document.Vocabulary.Head, current.Head)
	}
	if !containsStatus(document.Vocabulary.Statuses, "icebox") {
		t.Fatalf("refusal carried statuses %s, want the ones the reader has to look at now",
			statusNames(document.Vocabulary.Statuses))
	}
	// And nothing was rebased onto the change the reader had not seen.
	for _, definition := range document.Vocabulary.Statuses {
		if definition.Status == "ready" && definition.Label != "Ready" {
			t.Fatalf("status ready is labelled %q; the refused change was applied anyway", definition.Label)
		}
	}
}

// Two changes racing for the same ledger: one of them writes, the other is
// refused as a stale write, and the ledger holds exactly one new commit. There
// is no torn state to hold because the ledger's own compare-and-swap is what
// settles the race, exactly as it does for two CLI processes.
func TestBoardSerializesRacingVocabularyChanges(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	type outcome struct {
		status int
		body   []byte
		err    error
	}
	outcomes := make([]outcome, 2)
	bodies := []string{
		`{"status":"icebox","expectedHead":` + quoteJSON(before.Head) + `}`,
		`{"status":"triage","expectedHead":` + quoteJSON(before.Head) + `}`,
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := range outcomes {
		group.Add(1)
		// The request is made without the test helper, because a helper that
		// fails the test from a worker goroutine is a helper calling FailNow off
		// the test's own goroutine. Failures are carried back and reported below.
		go func() {
			defer group.Done()
			<-start
			request, err := http.NewRequest(http.MethodPost,
				"http://"+addr+"/api/vocabulary/statuses", strings.NewReader(bodies[index]))
			if err != nil {
				outcomes[index] = outcome{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				outcomes[index] = outcome{err: err}
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			outcomes[index] = outcome{status: response.StatusCode, body: body, err: err}
		}()
	}
	close(start)
	group.Wait()
	for _, result := range outcomes {
		if result.err != nil {
			t.Fatalf("racing change: %v", result.err)
		}
	}

	accepted, refused := 0, 0
	for _, result := range outcomes {
		switch result.status {
		case http.StatusOK:
			accepted++
		case http.StatusConflict:
			refused++
			var document webui.VocabularyErrorDocument
			if err := json.Unmarshal(result.body, &document); err != nil {
				t.Fatalf("decode refusal: %v; body = %s", err, result.body)
			}
			if document.Error.Category != core.CategoryStaleWrite {
				t.Fatalf("refusal category = %q, want %q", document.Error.Category, core.CategoryStaleWrite)
			}
		default:
			t.Fatalf("racing change = %d; body = %s", result.status, result.body)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("racing changes = %d accepted and %d refused, want one of each", accepted, refused)
	}

	// One commit, one new status, and a ledger that still validates.
	after := boardVocabularyDocument(t, addr)
	if len(after.Statuses) != len(before.Statuses)+1 {
		t.Fatalf("statuses = %s, want exactly one more than before", statusNames(after.Statuses))
	}
	if code, _, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("validate code = %d after racing changes; stderr = %q", code, stderr)
	}
}

// The board writes the ledger and never the working tree.
//
// That is a decision rather than an oversight, and this is the test that pins
// it: a server may be answering while somebody rebases the checkout it lives in,
// and rewriting a tracked file on an HTTP request is not its business. The
// change is still recorded, and the reader is told that the generated file now
// describes statuses this project no longer has.
func TestBoardLeavesTheWorkingTreeAloneAndSaysSo(t *testing.T) {
	repository := initializedRepository(t)
	guidelines := filepath.Join(repository, agentdocs.GuidelinesPath)
	generated, err := os.ReadFile(guidelines)
	if err != nil {
		t.Fatalf("read the generated guidelines: %v", err)
	}
	if !strings.Contains(string(generated), "in-progress") {
		t.Fatalf("the fixture's guidelines do not name the status this test renames:\n%s", generated)
	}
	// A working tree nobody should write into: a detached HEAD with an
	// uncommitted edit in it, which is what a rebase looks like from outside.
	gitOutput(t, repository, "add", "--all")
	gitOutput(t, repository, "commit", "--quiet", "--message", "the checkout somebody is working in")
	gitOutput(t, repository, "checkout", "--detach")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("half an edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := gitOutput(t, repository, "status", "--porcelain")
	if dirty == "" {
		t.Fatal("the fixture's working tree is clean, so it cannot tell a write from a no-op")
	}

	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head
	document := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/in-progress",
		`{"name":"doing","expectedHead":`+quoteJSON(head)+`}`)

	after, err := os.ReadFile(guidelines)
	if err != nil {
		t.Fatalf("read the generated guidelines: %v", err)
	}
	if string(after) != string(generated) {
		t.Fatal("the board rewrote the generated guidelines from an HTTP request")
	}
	if got := gitOutput(t, repository, "status", "--porcelain"); got != dirty {
		t.Fatalf("working tree = %q, want the %q it was left in", got, dirty)
	}
	// The change is recorded regardless, and the staleness is reported rather
	// than hidden.
	if !containsStatus(document.Vocabulary.Statuses, "doing") {
		t.Fatalf("statuses = %s, want the rename to have been recorded", statusNames(document.Vocabulary.Statuses))
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

	// And the next CLI status verb settles the file, which is the correct-on-
	// touch rule this decision leans on.
	if code, _, stderr := run(t, repository, "status", "label", "doing", "Doing Now", "--no-sync"); code != 0 {
		t.Fatalf("status label = code %d; stderr = %q", code, stderr)
	}
	settled, err := os.ReadFile(guidelines)
	if err != nil {
		t.Fatalf("read the generated guidelines: %v", err)
	}
	if strings.Contains(string(settled), "in-progress") {
		t.Fatalf("the CLI did not settle the guidelines the board left stale:\n%s", settled)
	}
}

// A project that predates the configuration ledger has no head, reports none,
// and can still be administered: the client sends back the nothing it read, and
// the first change seeds the ledger — which is exactly what a status verb does
// for the same project.
func TestBoardAdministersAProjectWithNoLedgerYet(t *testing.T) {
	repository := preLedgerRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)
	if before.Head != "" {
		t.Fatalf("vocabulary head = %q, want none for a project with no ledger", before.Head)
	}

	document := boardVocabularyMutation(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses",
		`{"status":"icebox","expectedHead":""}`)
	if document.Vocabulary.Head == "" {
		t.Fatal("the first change did not seed a configuration ledger")
	}
	if !containsStatus(document.Vocabulary.Statuses, "icebox") {
		t.Fatalf("statuses = %s, want the added one", statusNames(document.Vocabulary.Statuses))
	}
	// Omitting the member is still refused, because a client that says nothing
	// about what it read is not the same as one that read nothing.
	body, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses", `{"status":"thawing"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("change with no expectedHead = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
}

// The panel edits a status as one form, so a rename, a relabel and a tag set
// arrive together and are recorded as one commit against one head.
func TestBoardEditsNameLabelAndTagsInOneChange(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	document := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/in-review",
		`{"name":"checking","label":"Checking","tags":["next"],"expectedHead":`+quoteJSON(head)+`}`)
	found := false
	for _, definition := range document.Vocabulary.Statuses {
		if definition.Status != "checking" {
			continue
		}
		found = true
		if definition.Label != "Checking" {
			t.Fatalf("label = %q, want Checking", definition.Label)
		}
		if len(definition.Tags) != 1 || definition.Tags[0] != core.StatusTagNext {
			t.Fatalf("tags = %v, want only next", definition.Tags)
		}
	}
	if !found {
		t.Fatalf("statuses = %s, want the renamed one", statusNames(document.Vocabulary.Statuses))
	}

	// One commit, and the rename is still the rename-shaped pack reconciliation
	// classifies, with the tag operations naming the value the pack renamed to.
	operations := configPackOperations(t, repository, document.Vocabulary.Head)
	if len(operations) != 3 ||
		operations[0].Type != core.ConfigStatusRename ||
		operations[1].Type != core.ConfigStatusRelabel ||
		operations[2].Type != core.ConfigStatusTag || operations[2].Status != "checking" {
		t.Fatalf("recorded operations = %#v, want rename, relabel and tag against the new name", operations)
	}
	// The inverse of a commit that also moved tags does not claim to restore
	// them.
	newest := newestStatusLogEntry(t, repository)
	if newest.Inverse == nil {
		t.Fatalf("newest status log entry = %#v, want an inverse", newest)
	}
	if newest.Inverse.Exact {
		t.Fatal("the inverse of a rename that also retagged claims to be exact")
	}
	if !strings.Contains(newest.Inverse.Note, "tags") {
		t.Fatalf("inverse note = %q, want it to name the tags it does not restore", newest.Inverse.Note)
	}
}

// A panel form sends every field it has, so a change that repeats what a status
// already says is not a mistake — it is the client saying leave that one alone.
// A change where nothing at all differs is still refused, because a commit that
// records nothing is a commit nobody can read.
func TestBoardTakesAFormThatRepeatsWhatItDoesNotChange(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	document := boardVocabularyMutation(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/ready",
		`{"name":"ready","label":"Up Next","tags":["next"],"expectedHead":`+quoteJSON(head)+`}`)
	for _, definition := range document.Vocabulary.Statuses {
		if definition.Status == "ready" && definition.Label != "Up Next" {
			t.Fatalf("label = %q, want the one the form changed", definition.Label)
		}
	}
	// Only the label moved, so only the relabel was recorded.
	operations := configPackOperations(t, repository, document.Vocabulary.Head)
	if len(operations) != 1 || operations[0].Type != core.ConfigStatusRelabel {
		t.Fatalf("recorded operations = %#v, want the relabel alone", operations)
	}

	body, status := boardRequest(t, http.MethodPatch, "http://"+addr+"/api/vocabulary/statuses/ready",
		`{"name":"ready","label":"Up Next","tags":["next"],"expectedHead":`+
			quoteJSON(document.Vocabulary.Head)+`}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a form that changes nothing = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
	var refusal webui.VocabularyErrorDocument
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	if refusal.Error.Message != `status "ready" already reads exactly that way` {
		t.Fatalf("error message = %q, want the whole-form refusal", refusal.Error.Message)
	}
}

// Arity is a property of the result, and the authoring gate the board writes
// through is the same one the verbs write through: a project cannot be left
// without the status a `workbook next` needs.
func TestBoardRefusesAChangeThatWouldLeaveTheProjectUnusable(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	head := boardVocabularyDocument(t, addr).Head

	body, status := boardRequest(t, http.MethodDelete, "http://"+addr+"/api/vocabulary/statuses/backlog",
		`{"into":"ready","expectedHead":`+quoteJSON(head)+`}`)
	if status != http.StatusBadRequest {
		t.Fatalf("removing the default status = %d, want %d; body = %s", status, http.StatusBadRequest, body)
	}
	var document webui.VocabularyErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, body)
	}
	// The verb's own words, from core's authoring gate.
	if !strings.Contains(document.Error.Message, "default") {
		t.Fatalf("error message = %q, want the arity refusal core states", document.Error.Message)
	}
	if after := boardVocabularyDocument(t, addr).Head; after != head {
		t.Fatalf("vocabulary head moved to %q; a refused change wrote something", after)
	}
}

// The routes answer the methods they have and refuse the ones they do not,
// which is what the board's own method gate is for.
func TestBoardVocabularyRoutesEnforceTheirMethods(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	for _, test := range []struct {
		path  string
		wrong string
		allow string
	}{
		{path: "/api/vocabulary/statuses", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/vocabulary/statuses/ready", wrong: http.MethodPost, allow: "PATCH, DELETE"},
		{path: "/api/vocabulary/order", wrong: http.MethodPost, allow: http.MethodPut},
	} {
		request, err := http.NewRequest(test.wrong, "http://"+addr+test.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want %d", test.wrong, test.path, response.StatusCode, http.StatusMethodNotAllowed)
		}
		if got := response.Header.Get("Allow"); got != test.allow {
			t.Fatalf("%s %s Allow = %q, want %q", test.wrong, test.path, got, test.allow)
		}
	}
}

// boardVocabularyMutation performs one vocabulary change and returns the
// document it answered with, failing the test on anything but a success.
func boardVocabularyMutation(t *testing.T, method, url, body string) webui.VocabularyMutationDocument {
	t.Helper()
	contents, status := boardRequest(t, method, url, body)
	if status != http.StatusOK {
		t.Fatalf("%s %s = %d, want %d; body = %s", method, url, status, http.StatusOK, contents)
	}
	var document webui.VocabularyMutationDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode vocabulary mutation: %v; body = %s", err, contents)
	}
	if document.Format != "workbook.vocabulary-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned document", document)
	}
	if document.Vocabulary.Format != "workbook.vocabulary" || document.Vocabulary.Version != 1 {
		t.Fatalf("mutation vocabulary = %#v, want the document GET /api/vocabulary serves", document.Vocabulary)
	}
	return document
}

// configPackOperations reads the operations one configuration commit recorded.
func configPackOperations(t *testing.T, repository, head string) []core.ConfigOperation {
	t.Helper()
	var pack core.ConfigOperationPack
	if err := json.Unmarshal([]byte(gitOutput(t, repository, "show", head+":operation.json")), &pack); err != nil {
		t.Fatalf("decode configuration pack at %s: %v", head, err)
	}
	return pack.Operations
}

// newestStatusLogEntry reads what `workbook status log` says about the change
// that just happened. The log runs oldest first, so the newest change is the
// last entry rather than the first.
func newestStatusLogEntry(t *testing.T, repository string) statusLogEntry {
	t.Helper()
	code, stdout, stderr := run(t, repository, "status", "log", "--json")
	if code != 0 {
		t.Fatalf("status log code = %d; stderr = %q", code, stderr)
	}
	var result statusLogResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "status log").Data, &result); err != nil {
		t.Fatalf("decode status log: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("status log recorded no entries")
	}
	return result.Entries[len(result.Entries)-1]
}

func containsStatus(definitions []core.StatusDefinition, want core.Status) bool {
	for _, definition := range definitions {
		if definition.Status == want {
			return true
		}
	}
	return false
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
