package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
)

// urgentPriorityRow is the "Canonical priorities" row a project that added
// `urgent` must see in its generated guidelines. Every test below looks for
// this exact row rather than the bare word, because the word also appears in
// prose the renderer writes for every project.
const urgentPriorityRow = "| `urgent` | Urgent |"

// configuredPriorityProject is an initialized project that has named a
// priority of its own, which is the whole precondition these tests share: a
// project whose guidelines are only correct if the writer was told what this
// project's priorities are.
func configuredPriorityProject(t *testing.T) string {
	t.Helper()
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "priority", "add", "urgent",
		"--before", "high", "--no-sync"); code != 0 {
		t.Fatalf("priority add urgent = code %d; stderr = %q", code, stderr)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); !strings.Contains(got, urgentPriorityRow) {
		t.Fatalf("the fixture's guidelines do not name the priority it just added:\n%s", got)
	}
	return repository
}

// assertGuidelinesKeepUrgent reports whether the generated guidelines still
// document the project's own priorities rather than the built-in three.
func assertGuidelinesKeepUrgent(t *testing.T, repository, what string) {
	t.Helper()
	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, urgentPriorityRow) {
		t.Fatalf("%s rewrote the guidelines without this project's own priorities:\n%s", what, guidelines)
	}
}

// `workbook docs update` rewrites the guidelines, and the priorities it writes
// have to be the ones this project configured.
//
// Writing the built-in three here is worse than a stale file: `docs status`
// calls the correct file stale, `docs update` replaces the project's own
// priorities with high/medium/low and calls the result current, and the next
// priority or status verb writes them back — a flip-flop that leaves an agent
// reading the file between the two told that `urgent` does not exist.
func TestDocsUpdateKeepsTheProjectsOwnPriorities(t *testing.T) {
	repository := configuredPriorityProject(t)

	// The file a priority verb has just written is not stale, so `docs status`
	// has to agree with the writer that produced it.
	code, stdout, stderr := run(t, repository, "docs", "status")
	if code != 0 {
		t.Fatalf("docs status = code %d; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Fatalf("docs status called this project's own guidelines stale:\n%s", stdout)
	}

	if code, _, stderr := run(t, repository, "docs", "update"); code != 0 {
		t.Fatalf("docs update = code %d; stderr = %q", code, stderr)
	}
	assertGuidelinesKeepUrgent(t, repository, "docs update")

	// And it settles rather than flip-flopping: the file it just wrote is the
	// file it reads back as current.
	code, stdout, stderr = run(t, repository, "docs", "status")
	if code != 0 {
		t.Fatalf("docs status after update = code %d; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Fatalf("docs status called the file docs update just wrote stale:\n%s", stdout)
	}
}

// `workbook docs install` is the same options construction as update, and a
// project that reinstalls its documentation keeps its priorities.
func TestDocsInstallKeepsTheProjectsOwnPriorities(t *testing.T) {
	repository := configuredPriorityProject(t)

	if code, _, stderr := run(t, repository, "docs", "install"); code != 0 {
		t.Fatalf("docs install = code %d; stderr = %q", code, stderr)
	}
	assertGuidelinesKeepUrgent(t, repository, "docs install")
}

// Rerunning `workbook setup` on a configured project installs documentation
// again, and that pass writes the guidelines in full.
func TestSetupKeepsTheProjectsOwnPriorities(t *testing.T) {
	repository := configuredPriorityProject(t)

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("second setup = code %d; stderr = %q", code, stderr)
	}
	assertGuidelinesKeepUrgent(t, repository, "setup")
}

// Setup installs documentation before it fetches, so a clone joining a project
// that named its own priorities writes the built-in three first and corrects
// them from the fetched ledger afterwards. That correction has to carry the
// priorities as well as the statuses: this is somebody who did nothing wrong
// being handed a file that says their project's priorities do not exist.
func TestSetupWritesThePrioritiesTheFetchDelivered(t *testing.T) {
	author, _ := cliSyncRepositories(t)
	if code, _, stderr := run(t, author, "priority", "add", "urgent", "--before", "high"); code != 0 {
		t.Fatalf("priority add urgent = code %d; stderr = %q", code, stderr)
	}

	joining := cliClone(t, gitOutput(t, author, "remote", "get-url", "origin"))
	if code, stdout, stderr := run(t, joining, "setup"); code != 0 {
		t.Fatalf("setup = code %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	assertGuidelinesKeepUrgent(t, joining, "setup in a joining clone")

	// And the clone agrees with itself afterwards, rather than reporting the
	// file it just installed as stale.
	code, stdout, stderr := run(t, joining, "docs", "status")
	if code != 0 {
		t.Fatalf("docs status = code %d; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Fatalf("docs status in a joining clone called the installed guidelines stale:\n%s", stdout)
	}
}

// The board never writes the guidelines; it reads how they compare and reports
// staleness. That comparison renders the document, so it needs this project's
// priorities for the same reason every writer does — otherwise a project that
// named its own priorities is told its guidelines are stale whenever the board
// records a change, including changes that left the statuses exactly as the
// installed file describes them.
func TestBoardDoesNotCallTheProjectsOwnPrioritiesStale(t *testing.T) {
	repository := configuredPriorityProject(t)
	ctx := context.Background()
	board := openBoardVocabulary(t, ctx, repository)
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		t.Fatalf("load the project's configuration: %v", err)
	}

	warnings := staleGuidelinesWarnings(board, state.Vocabulary, state.Priorities)

	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none for guidelines that match this project's configuration", warnings)
	}
}
