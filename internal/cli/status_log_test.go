package cli

import (
	"fmt"
	"strings"
	"testing"
)

// The log is the same window `show --history` renders, over a different
// history: oldest first, the ten most recent by default, and every entry
// carrying the command that reverses it.
//
// One entry is one commit, which is the unit `show --history` counts and the
// unit a person recognizes: one command they ran. It is also what lets the
// total stay exact while the read is bounded by the window.
func TestStatusLogMirrorsShowHistoryWindowing(t *testing.T) {
	t.Parallel()
	// The ledgerless report needs a project with no ledger, and a mint writes
	// one, so the fixture is the pre-ledger shape a real upgrade lands on.
	repository := preLedgerRepository(t)
	if code, stdout, stderr := run(t, repository, "status", "log"); code != 0 || stderr != "" {
		t.Fatalf("status log on a ledgerless project = code %d, stderr %q", code, stderr)
	} else if !strings.Contains(stdout, "No status change is recorded") {
		t.Fatalf("status log = %q, want the ledgerless report", stdout)
	}

	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog")
	for index := range 11 {
		mustRunStatus(t, repository, "status", "label", "triage", fmt.Sprintf("Triage %d", index))
	}

	document := cliStatusLog(t, repository)
	if document.Total != 13 {
		t.Fatalf("total = %d, want the genesis, the add, and eleven relabels", document.Total)
	}
	if document.Showing != 10 {
		t.Fatalf("showing = %d, want the default window of ten", document.Showing)
	}
	if document.Entries[0].Summary != `labelled status triage "Triage 1"` {
		t.Fatalf("first entry = %#v, want the oldest change in the window", document.Entries[0])
	}
	last := document.Entries[len(document.Entries)-1]
	if last.Summary != `labelled status triage "Triage 10"` {
		t.Fatalf("last entry = %#v, want the newest change", last)
	}
	if last.Inverse == nil || last.Inverse.Command != `workbook status label triage "Triage 9"` || !last.Inverse.Exact {
		t.Fatalf("last inverse = %#v, want the label it replaced", last.Inverse)
	}
	if last.Commit == "" || last.OperationID == "" || last.Actor == "" || last.WallTime.IsZero() {
		t.Fatalf("entry = %#v, want commit, operation ID, actor and wall time", last)
	}

	windowed := cliStatusLog(t, repository, "--limit", "2")
	if windowed.Showing != 2 || windowed.Total != 13 {
		t.Fatalf("limited log = %d of %d, want 2 of 13", windowed.Showing, windowed.Total)
	}
	// The window bounds the read, and the oldest entry in it still carries an
	// inverse — which is only possible if the commit before the window was read
	// for its checkpoint.
	if windowed.Entries[0].Inverse == nil {
		t.Fatalf("windowed log = %#v, want the oldest entry's inverse read against its parent", windowed.Entries[0])
	}
	if windowed.Entries[0].Inverse.Command != `workbook status label triage "Triage 8"` {
		t.Fatalf("windowed inverse = %q, want the label the change replaced", windowed.Entries[0].Inverse.Command)
	}

	every := cliStatusLog(t, repository, "--all")
	if every.Showing != 13 {
		t.Fatalf("--all showing = %d, want every change", every.Showing)
	}
	if every.Entries[0].Operation != "config.genesis" || every.Entries[0].Inverse != nil {
		t.Fatalf("first entry = %#v, want the genesis with no inverse", every.Entries[0])
	}

	code, stdout, stderr := run(t, repository, "status", "log", "--limit", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("status log text = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "Showing 3 most recent changes out of 13.") {
		t.Fatalf("status log text = %q, want the window header", stdout)
	}
	if !strings.Contains(stdout, "\tinverse:\tworkbook status label triage") {
		t.Fatalf("status log text = %q, want an inverse line", stdout)
	}

	// `--limit=` sets the flag to an empty value, which used to slip past the
	// conflict check and let --all win in silence.
	for _, args := range [][]string{
		{"status", "log", "--limit=", "--all"},
		{"status", "log", "--limit", "2", "--all"},
	} {
		code, _, stderr := run(t, repository, args...)
		if code != 2 || !strings.Contains(stderr, "cannot use --limit with --all") {
			t.Fatalf("%v = code %d, stderr %q; want the conflict refused", args, code, stderr)
		}
	}
}

// One command is one entry, however many operations it recorded. A rename that
// also moves the label writes two operations into one commit, and the entry
// reports the command — with the inverse that undoes all of it.
func TestStatusLogReportsOneEntryPerRecordedCommand(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")

	document := cliStatusLog(t, repository, "--all")
	if document.Total != 2 {
		t.Fatalf("total = %d, want the genesis and the rename", document.Total)
	}
	rename := document.Entries[1]
	if rename.Operation != "status.rename" {
		t.Fatalf("entry operation = %q, want the rename", rename.Operation)
	}
	if rename.Summary != "renamed status ready to queued (+1 more change(s) in this commit)" {
		t.Fatalf("summary = %q, want the command with the rest of its commit counted", rename.Summary)
	}
	// The same count, reachable without parsing the sentence that states it.
	if rename.Collapsed != 1 {
		t.Fatalf("collapsed = %d, want the relabel this commit also recorded", rename.Collapsed)
	}
	if document.Entries[0].Collapsed != 0 {
		t.Fatalf("genesis collapsed = %d, want nothing beyond the seeding itself", document.Entries[0].Collapsed)
	}
	if rename.Inverse == nil || rename.Inverse.Command != "workbook status rename queued ready --label Ready" {
		t.Fatalf("inverse = %#v, want the rename and the label it moved", rename.Inverse)
	}
	if rename.OperationID == "" {
		t.Fatal("entry carries no operation ID")
	}
}
