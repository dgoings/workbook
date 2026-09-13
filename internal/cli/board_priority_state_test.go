package cli

import (
	"encoding/json"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// What a running board knows about this project's priorities, through the real
// wiring.
//
// `serve` runs for hours, and its view of the project's statuses is re-read on
// every request for that reason. Its view of the project's priorities was not:
// it was whatever the process opened with, so a board sorted cards against a
// priority order a teammate had already changed, landed new tasks on a default
// nobody used any more, and refused a priority somebody had added at lunchtime.
// The first two tests below are that staleness.
//
// The third is the trap that comes with fixing it. The moment the board's
// documents carry priorities, every producer of a webui.VocabularyState that
// leaves the field zero answers with the built-in three, because every
// PriorityVocabulary accessor substitutes them for the zero value. The client
// adopts a mutation answer wholesale, so a project that named its own
// priorities would watch them turn back into high/medium/low after saving an
// unrelated status rename. That is why the third test renames a status and then
// asks about priorities: the two have nothing to do with each other, which is
// exactly the point.

// answeredPriorityNames names the priorities a vocabulary mutation answered
// with, read out of the raw JSON rather than through webui.VocabularyDocument.
//
// That is deliberate: this test has to be able to tell an answer that names
// nothing from an answer that names the built-in three, and only the second is
// the bug. Decoding through the typed document would make both of them read as
// an absent field.
func answeredPriorityNames(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Vocabulary struct {
			Priorities struct {
				Priorities []struct {
					Priority string `json:"priority"`
				} `json:"priorities"`
			} `json:"priorities"`
		} `json:"vocabulary"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode vocabulary mutation: %v; body = %s", err, body)
	}
	names := make([]string, 0, len(envelope.Vocabulary.Priorities.Priorities))
	for _, definition := range envelope.Vocabulary.Priorities.Priorities {
		names = append(names, definition.Priority)
	}
	return strings.Join(names, ",")
}

// boardViewElement is the page element the server renders its facts onto, and
// boardPriorityAttribute is the one this stage adds. Both are matched rather
// than parsed because the assertion is about what the page carries, not about
// how a browser would read it.
var (
	boardViewElement       = regexp.MustCompile(`<div class="board-view"[^>]*>`)
	boardPriorityAttribute = regexp.MustCompile(`data-priorities="([^"]*)"`)
)

// pagePriorities reads the priorities the served board rendered into its page.
func pagePriorities(t *testing.T, page []byte) []struct {
	Priority string `json:"priority"`
	Label    string `json:"label"`
	Color    string `json:"color"`
} {
	t.Helper()
	match := boardPriorityAttribute.FindSubmatch(page)
	if match == nil {
		element := boardViewElement.Find(page)
		t.Fatalf("the served board carries no data-priorities attribute; its board view is %s", element)
	}
	var definitions []struct {
		Priority string `json:"priority"`
		Label    string `json:"label"`
		Color    string `json:"color"`
	}
	// The attribute is HTML-escaped by the template, which is what makes it
	// safe to carry JSON in one; the browser unescapes it before the script
	// parses it, and so does this.
	encoded := html.UnescapeString(string(match[1]))
	if err := json.Unmarshal([]byte(encoded), &definitions); err != nil {
		t.Fatalf("decode data-priorities: %v; attribute = %s", err, encoded)
	}
	return definitions
}

// A priority added while the board is running reaches the board, the way a
// status added while it is running already does. The board is started first on
// purpose: this is the refresh path, not the startup read.
func TestBoardPriorityStateFollowsAPriorityAddedAfterTheProcessStarted(t *testing.T) {
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)

	mustRunStatus(t, repository, "priority", "add", "urgent", "--before", "high", "--no-sync")

	body, status := boardRequest(t, http.MethodPost, "http://"+addr+"/api/tasks",
		`{"title":"Rotate the signing key","priority":"urgent"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /api/tasks = %d, want %d; the board is still reading the priorities "+
			"this process opened with. body = %s", status, http.StatusOK, body)
	}
	task := decodeServeMutation(t, body, status)
	if task.Priority != "urgent" {
		t.Fatalf("created task priority = %q, want the priority the request named", task.Priority)
	}
}

// A board page load reports the project's own priorities, so the client draws
// the set the server owns rather than a copy of three names it carried itself.
func TestBoardPriorityStatePageReportsTheProjectsPriorities(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "priority", "add", "urgent",
		"--before", "high", "--label", "Drop everything", "--no-sync")
	addr := startServeBoard(t, repository)

	page, status := boardRequest(t, http.MethodGet, "http://"+addr+"/", "")
	if status != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", status, http.StatusOK)
	}
	definitions := pagePriorities(t, page)
	names := make([]string, len(definitions))
	for index, definition := range definitions {
		names[index] = definition.Priority
	}
	if got, want := strings.Join(names, ","), "urgent,high,medium,low"; got != want {
		t.Fatalf("the page reports priorities %q, want %q", got, want)
	}
	if definitions[0].Label != "Drop everything" {
		t.Fatalf("the page labels %q as %q, want the project's own label",
			definitions[0].Priority, definitions[0].Label)
	}
}

// A status rename is not a priority change, and its answer must not read as
// one. The client adopts the whole vocabulary a mutation answers with, so an
// answer that left the priorities out would redraw this project's four as the
// built-in three — a configuration nobody touched, replaced by a rename of
// something else.
func TestBoardPriorityStateKeepsThePrioritiesThroughAStatusRename(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "priority", "add", "urgent", "--before", "high", "--no-sync")
	addr := startServeBoard(t, repository)
	before := boardVocabularyDocument(t, addr)

	body, status := boardRequest(t, http.MethodPatch,
		"http://"+addr+"/api/vocabulary/statuses/in-progress",
		`{"name":"doing","expectedHead":`+quoteJSON(before.Head)+`}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH a status = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	if got, want := answeredPriorityNames(t, body), "urgent,high,medium,low"; got != want {
		t.Fatalf("the rename answered with priorities %q, want %q", got, want)
	}
}
