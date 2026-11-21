package cli

import (
	"strings"
	"testing"
)

// An ordinary rename survives a teammate publishing something else, which is the
// coupling gitstore's replay fixtures can only assume.
//
// `workbook status rename` records two operations whenever the display label
// follows the machine value — the rename, then a relabel of the token the rename
// just created — and that is the default for every derived label. Nothing on the
// replay path may read the second operation as an edit to a status nobody
// defines, and nothing but this test notices if the verb stops emitting the shape
// gitstore's fixtures reproduce by hand.
func TestStatusRenameReplaysAfterATeammatePublishes(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	mustRunStatus(t, first, "status", "add", "triage", "--after", "backlog")
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = code %d; stderr = %q", code, stderr)
	}

	// The teammate renames the column offline. Its label was derived, so the
	// rename carries a relabel of the new value with it.
	rename := cliStatusMutation(t, second, "status rename",
		"status", "rename", "triage", "intake", "--no-sync", "--json")
	if rename.Change.Label == nil || rename.Change.Label.To != "Intake" {
		t.Fatalf("rename label = %#v, want the derived Intake that makes this a two-operation pack",
			rename.Change.Label)
	}

	// Somebody else publishes anything at all, so the rename has to replay.
	mustRunStatus(t, first, "status", "label", "done", "Delivered")

	code, stdout, stderr := run(t, second, "fetch")
	if code != 0 {
		t.Fatalf("fetch after a rename = code %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "Config conflict") || strings.Contains(stdout, "Config conflict") {
		t.Fatalf("an ordinary rename conflicted:\n%s\n%s", stdout, stderr)
	}

	document := cliStatusList(t, second)
	names := make([]string, 0, len(document.Statuses))
	label := ""
	for _, status := range document.Statuses {
		names = append(names, status.Status)
		if status.Status == "intake" {
			label = status.Label
		}
	}
	if !equalStrings(names, []string{"backlog", "intake", "ready", "blocked", "in-progress", "in-review", "done"}) {
		t.Fatalf("statuses = %v, want the rename replayed in place", names)
	}
	if label != "Intake" {
		t.Fatalf("intake label = %q, want the relabel from the same pack to have landed", label)
	}
	// The forwarding pointer survives the replay, so a task stored under the old
	// value still reads into the new column.
	if len(document.Retired) != 1 || document.Retired[0].Status != "triage" ||
		document.Retired[0].Becomes != "intake" || document.Retired[0].Operation != "status.rename" {
		t.Fatalf("retired = %#v, want triage forwarding to intake", document.Retired)
	}
	// And the teammate's own change is still there, so the replay landed on top
	// of it rather than instead of it.
	for _, status := range document.Statuses {
		if status.Status == "done" && status.Label != "Delivered" {
			t.Fatalf("done label = %q, want the fetched change preserved", status.Label)
		}
	}
}
