package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/gitstore"
)

// A parked ref the board cannot read is named in the server's log the first
// time a listing meets it, with its reason, and not again on every poll; a
// ref that comes back holding another commit, or failing for another reason,
// is named again.
func TestUnreadableParkedRefIsLoggedOncePerCommitAndReason(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	log := &unreadableParkedLog{out: &out}
	bad := gitstore.UnreadableParkedRef{
		TaskID: "WB-1", Ref: "refs/workbook/reconciled/WB-1/0", Commit: "abc", Reason: "its commit abc is not in this clone",
	}
	other := gitstore.UnreadableParkedRef{
		TaskID: "WB-2", Ref: "refs/workbook/reconciled/WB-2/1", Commit: "def", Reason: "it shares no history with task WB-2",
	}
	log.note([]gitstore.UnreadableParkedRef{bad, other})
	log.note([]gitstore.UnreadableParkedRef{bad, other})
	log.note(nil)
	moved := bad
	moved.Commit, moved.Reason = "fed", "it points at a tree, not a commit"
	log.note([]gitstore.UnreadableParkedRef{moved, bad})

	want := []string{
		"workbook serve: skipped parked ref refs/workbook/reconciled/WB-1/0 (commit abc) of WB-1, which cannot be shown as set-aside edits: its commit abc is not in this clone",
		"workbook serve: skipped parked ref refs/workbook/reconciled/WB-2/1 (commit def) of WB-2, which cannot be shown as set-aside edits: it shares no history with task WB-2",
		"workbook serve: skipped parked ref refs/workbook/reconciled/WB-1/0 (commit fed) of WB-1, which cannot be shown as set-aside edits: it points at a tree, not a commit",
	}
	if got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
