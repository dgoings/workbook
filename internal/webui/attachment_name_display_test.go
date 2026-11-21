package webui

import (
	"encoding/json"
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
	const spoofed = "invoice‮fdp.exe"
	const honest = "invoice fdp.exe"

	task := threadPageTask()
	task.Description = "See ![](attachment:" + spoofedID + ")"
	task.Attachments = append(task.Attachments, core.Attachment{
		ID: spoofedID, Author: "first@example.com", AddedAt: time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC),
		AttachmentData: core.AttachmentData{
			Name: spoofed, Kind: core.AttachmentFile, Media: "application/octet-stream", Size: 2, Blob: strings.Repeat("b", 40),
		},
	})
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
const honest = `+strconv.Quote(honest)+`;
const overrides = /[‪-‮⁦-⁩‎‏؜]/u;
function check(where, shown) {
  if (overrides.test(shown)) throw new Error(where + " draws a direction override: " + JSON.stringify(shown));
  if (!shown.includes(honest)) throw new Error(where + " = " + JSON.stringify(shown) + ", want it to read " + JSON.stringify(honest));
}
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  const row = [...panelRows("attachments")].find((candidate) => candidate.dataset.attachmentId === spoofedID);
  if (!row) throw new Error("the attachment list has no row for the file");
  const link = findElement(row, (element) => element.tagName === "A");
  if (!link || !link.href.endsWith("/attachments/" + spoofedID)) throw new Error("the row no longer links the file");
  check("the attachment row", link.textContent);
  check("the row's remove control", rowControl(row, "Remove").getAttribute("aria-label"));

  const reference = findElements(main, (element) => element.tagName === "A" && String(element.href || "").endsWith("/attachments/" + spoofedID))
    .find((element) => element !== link);
  if (!reference) throw new Error("the description's reference to the file is not drawn as a link");
  check("the description's reference", reference.textContent);

  const change = findElement(main, (element) => element.dataset && element.dataset.field === "attachment");
  if (!change) throw new Error("the history draws no attachment change");
  check("the history entry", change.textContent);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the attachment name render: %v\n%s", err, output)
	}
}
