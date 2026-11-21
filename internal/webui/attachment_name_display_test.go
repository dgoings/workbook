package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// An attachment's name is text somebody else typed, and on a shared board it
// is read by somebody who did not type it and decides from it whether to open
// the file. U+202E RIGHT-TO-LEFT OVERRIDE draws the rest of a name backwards,
// so a file named "invoice<U+202E>fdp.exe" reads as "invoiceexe.pdf" while it
// downloads as an .exe. The command line already draws these names through
// core.DisplayLine, which turns control and bidi-control runes into spaces;
// every place the board draws a file's name has to do the same, and still link
// the same file.
func TestHandlerClientDrawsAttachmentNamesWithoutDirectionOverrides(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const spoofedID = "01K0M6B8A4FTT8C39MXXYTWA09"
	const droppedID = "01K0M6B8A4FTT8C39MXXYTWA10"
	const linkID = "01K0M6B8A4FTT8C39MXXYTWA11"
	const spoofed = "invoice\u202efdp.exe"
	const honest = "invoice fdp.exe"

	task := threadPageTask()
	task.Title = "Pay the invoice\u202efdp.exe"
	// The first reference has no text of its own; the second gives the file a
	// name of its own choosing, which is how the reviewer bypassed the first
	// fix; the third's text carries an override itself, as does the link.
	task.Description = "See ![](attachment:" + spoofedID + ")\n\n" +
		"Also ![invoice.pdf](attachment:" + spoofedID + ")\n\n" +
		"And ![quarterly\u202efdp.exe](attachment:" + spoofedID + ")\n\n" +
		"Then [read\u202efdp.exe](https://example.test/notes)"
	task.Attachments = append(task.Attachments,
		core.Attachment{
			ID: spoofedID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
			AttachmentData: core.AttachmentData{
				Name: spoofed, Kind: core.AttachmentFile, Media: "application/octet-stream", Size: 2, Blob: strings.Repeat("b", 40),
			},
		},
		core.Attachment{
			ID: droppedID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
			AttachmentData: core.AttachmentData{
				Name: "receipt\u202efdp.exe", Kind: core.AttachmentFile, Media: "application/octet-stream", Size: 2, Blob: strings.Repeat("c", 40),
			},
			Dropped: &core.AttachmentDrop{RunID: "run", At: time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)},
		},
		core.Attachment{
			// The core refuses an override in a new link's address, but a stored
			// one — written before that rule, or by another clone — still draws.
			ID: linkID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
			AttachmentData: core.AttachmentData{
				Kind: core.AttachmentLink, URL: "https://evil.example/\u202efdp.exe", Label: "",
			},
		},
	)
	history, err := json.Marshal(map[string]any{
		"format": "workbook.task-history", "version": 1, "taskId": task.ID, "lifecycle": []any{},
		"history": map[string]any{"total": 1, "showing": 1, "changes": []any{map[string]any{
			"commit": "abc", "actor": "first@example.com", "wallTime": "2026-08-13T09:00:00Z", "logicalClock": 1,
			"summary": "changed attachment",
			"fields":  []any{map[string]any{"field": "attachment", "kind": "added", "to": spoofed}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	program := threadPageProgram(t, []core.Task{task}, `
const historyDocument = `+string(history)+`;
const boardFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if (String(url).endsWith("/history")) return { ok: true, json: async () => historyDocument };
  return boardFetch(url, options);
};
const spoofedID = `+strconv.Quote(spoofedID)+`;
const droppedID = `+strconv.Quote(droppedID)+`;
const linkID = `+strconv.Quote(linkID)+`;
const honest = `+strconv.Quote(honest)+`;
const overrides = /\p{Bidi_Control}/u;
function clean(where, shown) {
  if (overrides.test(shown)) throw new Error(where + " draws a direction override: " + JSON.stringify(shown));
}
function check(where, shown, want) {
  clean(where, shown);
  if (!shown.includes(want || honest)) throw new Error(where + " = " + JSON.stringify(shown) + ", want it to read " + JSON.stringify(want || honest));
}
function rowFor(id) {
  const row = [...panelRows("attachments")].find((candidate) => candidate.dataset.attachmentId === id);
  if (!row) throw new Error("the attachment list has no row for " + id);
  return row;
}
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const row = rowFor(spoofedID);
  const link = findElement(row, (element) => element.tagName === "A");
  if (!link || !link.href.endsWith("/attachments/" + spoofedID)) throw new Error("the row no longer links the file");
  check("the attachment row", link.textContent);
  check("the row's remove control", rowControl(row, "Remove").getAttribute("aria-label"));

  // A file compaction removed is drawn as text, by the same rule.
  const dropped = rowFor(droppedID);
  check("the removed file's row", dropped.textContent, "receipt fdp.exe");

  // A link: its row, and the tooltip that says where it goes.
  const linkRow = rowFor(linkID);
  const linkAnchor = findElement(linkRow, (element) => element.tagName === "A");
  if (!linkAnchor) throw new Error("the link attachment is not drawn as a link");
  check("the link attachment's row", linkAnchor.textContent, "https://evil.example/ fdp.exe");
  check("the link attachment's tooltip", String(linkAnchor.title), "https://evil.example/ fdp.exe");

  // Every reference in the description to the file reads as its real name:
  // whatever the author wrote is at most a caption beside it.
  const references = findElements(main, (element) => element.tagName === "A" && String(element.href || "").endsWith("/attachments/" + spoofedID))
    .filter((element) => element !== link);
  if (references.length !== 3) throw new Error("the description draws " + references.length + " references to the file, want 3");
  references.forEach((reference, at) => {
    if (reference.textContent !== honest) {
      throw new Error("reference " + at + " reads " + JSON.stringify(reference.textContent) + ", want the file's real name " + JSON.stringify(honest));
    }
  });
  const description = findElement(main, (element) => element.getAttribute && element.getAttribute("aria-label") === "Description");
  if (!description) throw new Error("the page draws no description");
  check("the description's caption", description.textContent, "(invoice.pdf)");
  check("the description's override caption", description.textContent, "(quarterly fdp.exe)");
  check("the description's link text", description.textContent, "read fdp.exe");

  check("the task's heading", findElement(main, (element) => element.tagName === "H1" || element.tagName === "H2").textContent, "Pay the invoice fdp.exe");
  check("the document title", String(document.title), "Pay the invoice fdp.exe");

  const change = findElement(main, (element) => element.dataset && element.dataset.field === "attachment");
  if (!change) throw new Error("the history draws no attachment change");
  check("the history entry", change.textContent);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the attachment name render: %v\n%s", err, output)
	}
}

// The page the server renders before its script runs draws a card's title, its
// labels and assignees, a column's label and the project's name: all of it text
// somebody typed, and none of it may carry a direction override into the page.
func TestHandlerPageDrawsTypedTextWithoutDirectionOverrides(t *testing.T) {
	t.Parallel()
	task := clientPlacementTask("WB-01J0000000000000000000BD01", "Pay the invoice\u202efdp.exe", core.StatusReady, core.PriorityMedium)
	task.Description = "Due\u202etoday\nsecond line"
	task.Labels = []string{"fin\u202eance"}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return []core.Task{task}, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	page := response.Body.String()
	if strings.ContainsRune(page, '\u202e') {
		at := strings.IndexRune(page, '\u202e')
		t.Fatalf("the board page carries a direction override: %q", page[max(0, at-80):min(len(page), at+40)])
	}
	if !strings.Contains(page, "Pay the invoice fdp.exe") {
		t.Fatal("the board page does not draw the task's title")
	}
}

// form-action is one of the directives that does not fall back to default-src,
// so the page's policy must name it or a form planted in the page could post
// what a reader types anywhere.
func TestHandlerPagePolicyKeepsFormSubmissionsOnTheBoard(t *testing.T) {
	t.Parallel()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	for _, path := range []string{"/", "/tasks/new"} {
		response := request(t, handler, http.MethodGet, path)
		policy := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "form-action 'self'") {
			t.Errorf("GET %s Content-Security-Policy = %q, want form-action 'self'", path, policy)
		}
	}
}
