package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// What the board's display settings do through the real wiring, and the one
// thing this surface must never do.
//
// A display operation stamps generation two into a project's configuration
// checkpoint, and that mark is permanent: from the moment it lands, every clone
// running an older Workbook can read the project and can no longer change its
// configuration. A save that recorded nothing but still authored a pack would
// spend that on every teammate for the life of the project in exchange for a
// commit that says nothing. So the rule the command line enforces with exit 5 —
// no operation without a change — is enforced here by writing nothing at all,
// and the tests below check the ref rather than the answer.

// boardDisplayDocument reads the settings the board reports, out of the same
// document its statuses come from.
func boardDisplayDocument(t *testing.T, addr string) webui.DisplayDocument {
	t.Helper()
	document := boardVocabularyDocument(t, addr)
	if document.Display == nil {
		return webui.DisplayDocument{Format: "workbook.display", Version: 1, Head: document.Head}
	}
	if document.Display.Head != document.Head {
		t.Fatalf("display head = %q, statuses head = %q; one read has one head",
			document.Display.Head, document.Head)
	}
	return *document.Display
}

// boardDisplaySave performs one save and returns the document it answered with,
// failing the test on anything but a success.
func boardDisplaySave(t *testing.T, addr, body string) webui.DisplayMutationDocument {
	t.Helper()
	contents, status := boardRequest(t, http.MethodPatch, "http://"+addr+"/api/display", body)
	if status != http.StatusOK {
		t.Fatalf("PATCH /api/display = %d, want %d; body = %s", status, http.StatusOK, contents)
	}
	var document webui.DisplayMutationDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode display mutation: %v; body = %s", err, contents)
	}
	if document.Format != "workbook.display-mutation" || document.Version != 1 {
		t.Fatalf("mutation envelope = %#v, want a versioned document", document)
	}
	return document
}

// configHead reads the configuration ledger's tip, or the empty string for a
// project that has none.
func configHead(t *testing.T, repository string) string {
	t.Helper()
	code, stdout, _ := run(t, repository, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show code = %d, want 0", code)
	}
	var shown struct {
		Display struct {
			Head string `json:"head"`
		} `json:"display"`
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "config show").Data, &shown); err != nil {
		t.Fatalf("decode config show: %v", err)
	}
	return shown.Display.Head
}

// A save records what the reader changed, and the command line reads back the
// same values out of the same ledger.
func TestBoardRecordsDisplaySettingsThroughTheSharedLedger(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardDisplayDocument(t, addr)

	document := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"#1A7F4B","textColor":"","expectedHead":`+quoteJSON(before.Head)+`}`)
	if document.Display.Head == before.Head {
		t.Fatalf("head = %q, want a head past %q", document.Display.Head, before.Head)
	}
	// Canonicalized at this boundary, exactly as `workbook config set` does it:
	// the ledger stores one spelling of a colour.
	if document.Display.Name != "Atlas" || document.Display.PrimaryColor != "#1a7f4b" {
		t.Fatalf("display = %#v, want the canonical values", document.Display)
	}
	if document.Display.TextColor != "" {
		t.Fatalf("text color = %q, want the setting nobody configured", document.Display.TextColor)
	}

	code, stdout, stderr := run(t, repository, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show code = %d, want 0; stderr = %q", code, stderr)
	}
	shown := string(assertJSONResult(t, stdout, "config show").Data)
	for _, want := range []string{`"Atlas"`, `"#1a7f4b"`} {
		if !strings.Contains(shown, want) {
			t.Errorf("config show data = %s, want it to carry %s", shown, want)
		}
	}

	// One pack, and only the settings that moved are in it.
	operations := configPackOperations(t, repository, document.Display.Head)
	if len(operations) != 2 {
		t.Fatalf("the save recorded %d operations, want the two settings it changed: %#v", len(operations), operations)
	}
	for _, operation := range operations {
		if operation.Type != core.ConfigDisplaySet {
			t.Fatalf("operation = %#v, want a display.set", operation)
		}
	}
	// And the board draws itself out of it: the name is the page's heading and
	// the colour reaches the stylesheet as an override.
	page, status := boardRequest(t, http.MethodGet, "http://"+addr+"/", "")
	if status != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", status, http.StatusOK)
	}
	for _, want := range []string{"<h1>Atlas</h1>", "<title>Atlas</title>", "--wb-primary: #1a7f4b;"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the served board does not carry %q", want)
		}
	}
	if strings.Contains(string(page), "ZgotmplZ") {
		t.Error("the theme was filtered rather than emitted")
	}
}

// The rule this whole surface turns on: a save that changes nothing writes
// nothing.
//
// It is checked against the ref rather than against the answer, because the cost
// of getting it wrong is not a wrong answer — it is a commit whose generation
// marker parks every older clone on this project forever. The answer is a
// success, because the reader asked for a configuration and that is the
// configuration they now have.
func TestBoardSaveThatChangesNothingMovesNoRef(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	// An unconfigured project, saved empty: three settings nobody configured,
	// cleared. `config unset` refuses each of these outright with exit 5.
	unseeded := boardDisplayDocument(t, addr)
	before := configHead(t, repository)
	empty := boardDisplaySave(t, addr,
		`{"name":"","primaryColor":"","textColor":"","expectedHead":`+quoteJSON(unseeded.Head)+`}`)
	if empty.Display.Head != unseeded.Head {
		t.Fatalf("an empty save moved the head to %q from %q", empty.Display.Head, unseeded.Head)
	}
	if after := configHead(t, repository); after != before {
		t.Fatalf("the configuration ledger moved from %q to %q on a save that changed nothing", before, after)
	}

	// Now configure it, and save the very same values again — including the
	// colour in the other case, which canonicalizes to what is already stored.
	saved := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"#1a7f4b","textColor":"","expectedHead":`+quoteJSON(unseeded.Head)+`}`)
	configured := configHead(t, repository)
	if configured == before {
		t.Fatal("the first real save recorded nothing, so this test pins nothing")
	}

	again := boardDisplaySave(t, addr,
		`{"name":"  Atlas  ","primaryColor":"#1A7F4B","textColor":"","expectedHead":`+quoteJSON(saved.Display.Head)+`}`)
	if again.Display.Head != saved.Display.Head {
		t.Fatalf("re-saving the stored values moved the head to %q from %q",
			again.Display.Head, saved.Display.Head)
	}
	if again.Display.Name != "Atlas" || again.Display.PrimaryColor != "#1a7f4b" {
		t.Fatalf("re-saving answered %#v, want the configuration as it stands", again.Display)
	}
	if after := configHead(t, repository); after != configured {
		t.Fatalf("the configuration ledger moved from %q to %q on a save that changed nothing", configured, after)
	}
}

// A save that changes one of three records one operation, not three. The other
// two are the same values, and re-recording them would be two operations that
// say nothing and one marker nobody needed.
func TestBoardSaveRecordsOnlyWhatMoved(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	unseeded := boardDisplayDocument(t, addr)

	first := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"#1a7f4b","textColor":"#3b2a1a","expectedHead":`+quoteJSON(unseeded.Head)+`}`)
	second := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"#7f1a4b","textColor":"#3b2a1a","expectedHead":`+quoteJSON(first.Display.Head)+`}`)

	operations := configPackOperations(t, repository, second.Display.Head)
	want := []core.ConfigOperation{{Type: core.ConfigDisplaySet, Setting: core.DisplayPrimaryColor, Value: "#7f1a4b"}}
	if len(operations) != 1 || operations[0].Type != want[0].Type ||
		operations[0].Setting != want[0].Setting || operations[0].Value != want[0].Value {
		t.Fatalf("the save recorded %#v, want only %#v", operations, want)
	}

	// And clearing one of them clears exactly that one.
	third := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"#7f1a4b","textColor":"","expectedHead":`+quoteJSON(second.Display.Head)+`}`)
	cleared := configPackOperations(t, repository, third.Display.Head)
	if len(cleared) != 1 || cleared[0].Type != core.ConfigDisplayUnset || cleared[0].Setting != core.DisplayTextColor {
		t.Fatalf("clearing the text colour recorded %#v, want one display.unset for it", cleared)
	}
	if third.Display.TextColor != "" || third.Display.Name != "Atlas" {
		t.Fatalf("display after the clearing = %#v, want the name kept and the colour gone", third.Display)
	}
}

// A value the ledger could not store is refused where the reader can still be
// told the rule, in the words the command line refuses it in, and nothing is
// recorded — including the settings of the same save that were fine.
func TestBoardRefusesADisplayValueTheLedgerCannotStore(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardDisplayDocument(t, addr)
	ledgerBefore := configHead(t, repository)

	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "a colour that is not one",
			body: `{"name":"Atlas","primaryColor":"chartreuse","textColor":""}`,
			want: "six hexadecimal digits",
		},
		{
			name: "a colour with no hash",
			body: `{"name":"Atlas","primaryColor":"1a7f4b","textColor":""}`,
			want: "six hexadecimal digits",
		},
		{
			name: "a name longer than the ceiling",
			body: `{"name":"` + strings.Repeat("A", core.MaxProjectNameBytes+1) + `","primaryColor":"","textColor":""}`,
			want: "100",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.TrimSuffix(test.body, "}") + `,"expectedHead":` + quoteJSON(before.Head) + `}`
			contents, status := boardRequest(t, http.MethodPatch, "http://"+addr+"/api/display", body)
			if status != http.StatusBadRequest {
				t.Fatalf("PATCH = %d, want %d; body = %s", status, http.StatusBadRequest, contents)
			}
			var document webui.DisplayErrorDocument
			if err := json.Unmarshal(contents, &document); err != nil {
				t.Fatalf("decode error document: %v; body = %s", err, contents)
			}
			if document.Error.Category != core.CategoryValidation ||
				!strings.Contains(document.Error.Message, test.want) {
				t.Fatalf("error = %#v, want a validation failure quoting %q", document.Error, test.want)
			}
			if document.Display != nil {
				t.Fatalf("a validation refusal carried settings it has no use for: %#v", *document.Display)
			}
		})
	}
	// A refused save recorded nothing at all, including the settings of the same
	// save that were fine.
	if after := configHead(t, repository); after != ledgerBefore {
		t.Fatalf("a refused save moved the ledger from %q to %q", ledgerBefore, after)
	}

	// A field holding nothing but space is the empty field it looks like, not a
	// refusal: that is what an empty field means everywhere else on this form,
	// and the form trims what it sends anyway.
	blank := boardDisplaySave(t, addr,
		`{"name":"   ","primaryColor":"  ","textColor":"","expectedHead":`+quoteJSON(before.Head)+`}`)
	if blank.Display.Name != "" || blank.Display.Head != before.Head {
		t.Fatalf("a save of blank fields = %#v, want the unconfigured project it already was", blank.Display)
	}
	if after := configHead(t, repository); after != ledgerBefore {
		t.Fatalf("a save of blank fields moved the ledger from %q to %q", ledgerBefore, after)
	}
}

// A save composed against a configuration that has since moved is refused, and
// the refusal carries the configuration as it now stands so the page can adopt
// it without a refetch. The copy names the configuration rather than the
// statuses, because either half of one ledger may be what moved.
func TestBoardAnswersAStaleDisplaySaveWithTheCurrentSettings(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardDisplayDocument(t, addr)

	// Somebody else configures the project from the command line.
	code, _, stderr := run(t, repository, "config", "set", "project-name", "Beta", "--no-sync")
	if code != 0 {
		t.Fatalf("config set code = %d, want 0; stderr = %q", code, stderr)
	}

	contents, status := boardRequest(t, http.MethodPatch, "http://"+addr+"/api/display",
		`{"name":"Atlas","primaryColor":"","textColor":"","expectedHead":`+quoteJSON(before.Head)+`}`)
	if status != http.StatusConflict {
		t.Fatalf("PATCH = %d, want %d; body = %s", status, http.StatusConflict, contents)
	}
	var document webui.DisplayErrorDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode error document: %v; body = %s", err, contents)
	}
	if document.Error.Category != core.CategoryStaleWrite {
		t.Fatalf("error = %#v, want a stale write", document.Error)
	}
	if !strings.Contains(document.Error.Message, "configuration") ||
		strings.Contains(document.Error.Message, "statuses") {
		t.Fatalf("error message = %q, want it to name the configuration rather than the statuses",
			document.Error.Message)
	}
	if document.Display == nil {
		t.Fatal("the refusal carried no settings for the page to adopt")
	}
	if document.Display.Name != "Beta" {
		t.Fatalf("refusal settings = %#v, want the configuration that superseded this save", *document.Display)
	}
	if document.Display.Head == before.Head {
		t.Fatal("the refusal carried the head the save was composed against")
	}
}

// A status change and a display save share one ledger and one tip, so a status
// change made in between is exactly as much a reason to refuse a save as another
// save would be — and a save moves the head the next status change must name.
func TestBoardDisplayAndStatusChangesShareOneLedgerTip(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	saved := boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"","textColor":"","expectedHead":`+quoteJSON(before.Head)+`}`)

	// A status change against the head the board was opened with is now stale.
	contents, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":`+quoteJSON(before.Head)+`}`)
	if status != http.StatusConflict {
		t.Fatalf("POST a status against the old head = %d, want %d; body = %s",
			status, http.StatusConflict, contents)
	}
	// Against the head the save produced, it lands.
	document := boardVocabularyMutation(t, http.MethodPost, "http://"+addr+"/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":`+quoteJSON(saved.Display.Head)+`}`)
	if !containsStatus(document.Vocabulary.Statuses, "triage") {
		t.Fatalf("statuses = %s, want the one the change added", statusNames(document.Vocabulary.Statuses))
	}
	// And the statuses document still carries the name, at the head the status
	// change produced.
	after := boardDisplayDocument(t, addr)
	if after.Name != "Atlas" || after.Head != document.Vocabulary.Head {
		t.Fatalf("display after the status change = %#v, want the name at the new head", after)
	}
}
