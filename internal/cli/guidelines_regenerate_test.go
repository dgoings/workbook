package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// The guidelines state a project's statuses, so a status change that left them
// alone would ship a generated file that is wrong from the moment it is
// committed. Every mutating verb rewrites them, and the envelope says so.
func TestStatusChangesRegenerateTheGuidelines(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)

	rename := cliStatusMutation(t, repository, "status rename",
		"status", "rename", "ready", "todo", "--label", "Next Up", "--json")

	if rename.Docs == nil || len(rename.Docs.Artifacts) != 1 {
		t.Fatalf("rename docs = %#v, want the guidelines alone", rename.Docs)
	}
	artifact := rename.Docs.Artifacts[0]
	if artifact.Path != agentdocs.GuidelinesPath || artifact.State != string(agentdocs.StateStale) || !artifact.Written {
		t.Fatalf("rename docs artifact = %#v, want a stale %s rewritten", artifact, agentdocs.GuidelinesPath)
	}

	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	for _, want := range []string{
		"| 2 | `todo` | Next Up | `next` |",
		"`workbook next` claims from `todo`.",
		"New tasks land in `backlog`.",
		"A dependency is satisfied once it reaches `done`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q after the rename:\n%s", want, guidelines)
		}
	}
	if strings.Contains(guidelines, "`ready`") {
		t.Errorf("guidelines still document the old status:\n%s", guidelines)
	}
	// Regenerated means current, not merely rewritten: `workbook docs status`
	// has to agree that nothing is left to do.
	if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Errorf("docs status after a status change = code %d, %q; want everything current", code, stdout)
	}

	// A second verb, a second rewrite, and the tag legend follows the tags
	// rather than the names.
	tagged := cliStatusMutation(t, repository, "status tag",
		"status", "tag", "in-review", "--tag", "done", "--json")
	if tagged.Docs == nil || !tagged.Docs.Artifacts[0].Written {
		t.Fatalf("tag docs = %#v, want the guidelines rewritten", tagged.Docs)
	}
	guidelines = readProjectFile(t, repository, agentdocs.GuidelinesPath)
	for _, want := range []string{
		"| 4 | `in-review` | In Review | `done` |",
		"A dependency is satisfied once it reaches `in-review` or `done`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q after the tag:\n%s", want, guidelines)
		}
	}

	// The text surface reports the file it rewrote, the same way setup does.
	code, stdout, stderr := run(t, repository, "status", "add", "triage", "--after", "backlog")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\twritten") {
		t.Errorf("status add text = %q, want the regenerated guidelines line", stdout)
	}
}

// A generated file somebody edited is never overwritten, and a status change is
// not the place to start. The change is already recorded and published when the
// refresh runs, so the refusal is reported beside a success rather than turned
// into a failure that would leave the ledger ahead of the exit code.
func TestStatusChangeReportsGuidelinesItWillNotOverwrite(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	edited := strings.Replace(
		readProjectFile(t, repository, agentdocs.GuidelinesPath),
		"# Workbook guidelines", "# Workbook guidelines\n\nOur own preamble.", 1)
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, edited)

	code, stdout, stderr := run(t, repository, "status", "add", "triage", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "status add")

	var document statusMutationDocument
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode status add: %v; output = %s", err, stdout)
	}
	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateModified) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want a modified file left alone", artifact)
	}
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningDocsRefresh {
		t.Fatalf("warnings = %#v, want one %q", envelope.Warnings, core.WarningDocsRefresh)
	}
	if !strings.Contains(envelope.Warnings[0].Message, "workbook docs update --force") {
		t.Fatalf("warning = %q, want the command that overwrites it", envelope.Warnings[0].Message)
	}

	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != edited {
		t.Fatalf("guidelines were overwritten:\n%s", got)
	}
	// The status change itself landed. A documentation refusal is not a
	// configuration refusal.
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done", "triage",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}

	// The same refusal reaches a text-mode caller on stderr, with the change on
	// stdout, and still exits 0.
	code, stdout, stderr = run(t, repository, "status", "add", "archived")
	if code != 0 {
		t.Fatalf("status add = code %d; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\tunchanged") {
		t.Errorf("status add text = %q, want the untouched guidelines reported", stdout)
	}
	if !strings.Contains(stderr, "workbook docs update --force") {
		t.Errorf("status add stderr = %q, want the warning", stderr)
	}
}

// statusVerbFixtures is one exercise of every mutating status verb, and the
// setup each needs to be a legal change on a fresh project.
//
// It is keyed by verb and checked against the family the help schema lists, so
// a verb added to the family without an entry here fails rather than quietly
// going unexercised. The docs refresh is wired per verb, which is exactly the
// shape that drifts: six verbs regenerating and a seventh silently not is
// invisible to a test that only exercises the ones somebody remembered.
var statusVerbFixtures = map[string]struct {
	setup [][]string
	args  []string
}{
	"add":    {args: []string{"status", "add", "triage", "--json"}},
	"rename": {args: []string{"status", "rename", "ready", "todo", "--json"}},
	"label":  {args: []string{"status", "label", "ready", "Next Up", "--json"}},
	"move":   {args: []string{"status", "move", "ready", "--before", "backlog", "--json"}},
	"tag":    {args: []string{"status", "tag", "in-review", "--tag", "next", "--json"}},
	"untag": {
		// Untagging the only status tagged next is refused outright, so the
		// project gets a second one first.
		setup: [][]string{{"status", "tag", "in-review", "--tag", "next"}},
		args:  []string{"status", "untag", "in-review", "next", "--json"},
	},
	"delete": {args: []string{"status", "delete", "in-review", "--into", "done", "--json"}},
}

// statusReadingVerbs are the two verbs that change nothing and so regenerate
// nothing.
var statusReadingVerbs = map[string]bool{"list": true, "log": true}

func TestEveryMutatingStatusVerbRegeneratesTheGuidelines(t *testing.T) {
	t.Parallel()
	for _, verb := range statusSubcommands() {
		if statusReadingVerbs[verb] {
			continue
		}
		fixture, covered := statusVerbFixtures[verb]
		if !covered {
			t.Errorf("status %s has no fixture here, so nothing checks that it regenerates the guidelines", verb)
			continue
		}
		t.Run(verb, func(t *testing.T) {
			repository := initializedRepository(t)
			for _, command := range fixture.setup {
				mustRunStatus(t, repository, command...)
			}
			before := readProjectFile(t, repository, agentdocs.GuidelinesPath)

			document := cliStatusMutation(t, repository, "status "+verb, fixture.args...)

			if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
				t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
			}
			artifact := document.Docs.Artifacts[0]
			if artifact.Path != agentdocs.GuidelinesPath ||
				artifact.State != string(agentdocs.StateStale) || !artifact.Written {
				t.Fatalf("docs artifact = %#v, want a stale %s rewritten", artifact, agentdocs.GuidelinesPath)
			}
			if readProjectFile(t, repository, agentdocs.GuidelinesPath) == before {
				t.Fatalf("status %s reported a rewrite that changed nothing", verb)
			}
			if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
				strings.Contains(stdout, string(agentdocs.StateStale)) {
				t.Fatalf("docs status after status %s = code %d, %q; want everything current", verb, code, stdout)
			}
		})
	}
}

// A display label is authored by whoever can push, and the managed block's own
// terminator is twenty-one bytes of it. A label carrying that string used to
// truncate the block it was written into: the recorded hash covered the whole
// body and the block read back was the truncated one, so every clone reported a
// file nobody had edited as locally modified, every status change warned about
// it forever, `docs update --force` grew the file by a whole block per run, and
// `workbook setup` exited 5 in every clone — from one label.
func TestAStatusLabelCarryingTheBlockTerminatorDoesNotWedgeTheDocs(t *testing.T) {
	t.Parallel()
	author, _ := cliSyncRepositories(t)
	origin := gitOutput(t, author, "remote", "get-url", "origin")
	if code, _, stderr := run(t, author, "setup"); code != 0 {
		t.Fatalf("setup = code %d; stderr = %q", code, stderr)
	}
	mustRunStatus(t, author, "status", "label", "ready", "Next <!-- workbook:end --> Up")

	// The file Workbook just wrote is one it can still read as its own.
	if code, stdout, _ := run(t, author, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateModified)) {
		t.Fatalf("docs status = code %d, %q; want nothing reported as modified", code, stdout)
	}
	guidelines := readProjectFile(t, author, agentdocs.GuidelinesPath)
	if strings.Count(guidelines, "<!-- workbook:end -->") != 1 {
		t.Fatalf("the guidelines carry %d end markers, want the block's own:\n%s",
			strings.Count(guidelines, "<!-- workbook:end -->"), guidelines)
	}
	if !strings.Contains(guidelines, "Next &lt;!-- workbook:end --> Up") {
		t.Fatalf("the label does not read as what was written:\n%s", guidelines)
	}

	// The next change refreshes it rather than refusing, with nothing to warn
	// about.
	code, stdout, stderr := run(t, author, "status", "add", "triage", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "status add")
	if len(envelope.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", envelope.Warnings)
	}
	var document statusMutationDocument
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode status add: %v", err)
	}
	if document.Docs == nil || document.Docs.Artifacts[0].State != string(agentdocs.StateStale) ||
		!document.Docs.Artifacts[0].Written {
		t.Fatalf("docs = %#v, want a stale rewrite rather than a refusal", document.Docs)
	}

	// A forced refresh settles rather than growing the file by a block.
	lines := strings.Count(readProjectFile(t, author, agentdocs.GuidelinesPath), "\n")
	for range 3 {
		if code, _, stderr := run(t, author, "docs", "update", "--force", "--no-skill"); code != 0 {
			t.Fatalf("docs update --force = code %d; stderr = %q", code, stderr)
		}
	}
	if got := strings.Count(readProjectFile(t, author, agentdocs.GuidelinesPath), "\n"); got != lines {
		t.Fatalf("three forced refreshes took the guidelines from %d lines to %d", lines, got)
	}

	// And a teammate can still bootstrap, which is the failure that reached
	// every clone rather than only the one that authored the label.
	mustRunStatus(t, author, "push")
	joining := cliClone(t, origin)
	if code, _, stderr := run(t, joining, "setup"); code != 0 {
		t.Fatalf("setup in a joining clone = code %d; stderr = %q", code, stderr)
	}
	if code, stdout, _ := run(t, joining, "docs", "status"); code != 0 ||
		strings.Contains(stdout, string(agentdocs.StateModified)) {
		t.Fatalf("docs status in a joining clone = code %d, %q; want nothing modified", code, stdout)
	}
}

// Setup installs documentation before it fetches, so that an unreachable origin
// still leaves a clone with documentation. A clone joining a project that
// configured its statuses would therefore write the built-in six and read them
// as the truth, which is the one moment a fresh clone is most likely to believe
// them.
func TestSetupWritesTheGuidelinesTheFetchDelivered(t *testing.T) {
	t.Parallel()
	author, _ := cliSyncRepositories(t)
	mustRunStatus(t, author, "status", "rename", "ready", "todo", "--label", "Next Up")

	joining := cliClone(t, gitOutput(t, author, "remote", "get-url", "origin"))
	code, stdout, stderr := run(t, joining, "setup")
	if code != 0 {
		t.Fatalf("setup = code %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}

	guidelines := readProjectFile(t, joining, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, "| 2 | `todo` | Next Up | `next` |") {
		t.Fatalf("guidelines do not describe the fetched statuses:\n%s", guidelines)
	}
	if strings.Contains(guidelines, "`ready`") {
		t.Fatalf("guidelines describe the statuses this clone had before its fetch:\n%s", guidelines)
	}
	// One line per managed file, whichever pass wrote it.
	if got := strings.Count(stdout, "Docs:\t"+agentdocs.GuidelinesPath); got != 1 {
		t.Fatalf("setup reported the guidelines %d times:\n%s", got, stdout)
	}
	if code, out, _ := run(t, joining, "docs", "status"); code != 0 ||
		strings.Contains(out, string(agentdocs.StateStale)) {
		t.Fatalf("docs status after setup = code %d, %q; want everything current", code, out)
	}
}

// A project that has no guidelines file said so, with `workbook setup
// --no-docs` or `workbook docs remove`. A status change refreshes generated
// documentation and never installs it, so that decision survives.
func TestStatusChangeDoesNotInstallGuidelinesAProjectDeclined(t *testing.T) {
	t.Parallel()
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup --no-docs = code %d; stderr = %q", code, stderr)
	}

	document := cliStatusMutation(t, repository, "status add", "status", "add", "triage", "--json")

	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateAbsent) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want an absent file left absent", artifact)
	}
	path := filepath.Join(repository, filepath.FromSlash(agentdocs.GuidelinesPath))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the file still absent", agentdocs.GuidelinesPath, err)
	}

	// The text surface says which of the two it is. "unchanged" beside the path
	// would read as "already current", which is the opposite of the truth.
	code, stdout, stderr := run(t, repository, "status", "add", "archived")
	if code != 0 || stderr != "" {
		t.Fatalf("status add = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\t"+agentdocs.GuidelinesPath+"\tnot installed") {
		t.Errorf("status add text = %q, want the guidelines reported as not installed", stdout)
	}
}

// A file somebody wrote at the guidelines path themselves carries no managed
// block, and a status change appending one would install documentation the
// project never asked for — the same decision as having no file at all, made a
// different way.
func TestStatusChangeLeavesAGuidelinesFileWithNoManagedBlockAlone(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "docs", "remove", "--no-skill"); code != 0 {
		t.Fatalf("docs remove = code %d; stderr = %q", code, stderr)
	}
	handWritten := "# Our own notes\n\nWorkbook wrote none of this.\n"
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, handWritten)

	document := cliStatusMutation(t, repository, "status add", "status", "add", "triage", "--json")

	if document.Docs == nil || len(document.Docs.Artifacts) != 1 {
		t.Fatalf("docs = %#v, want the guidelines reported", document.Docs)
	}
	if artifact := document.Docs.Artifacts[0]; artifact.State != string(agentdocs.StateAbsent) || artifact.Written {
		t.Fatalf("docs artifact = %#v, want a blockless file left alone", artifact)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != handWritten {
		t.Fatalf("the hand-written file gained a managed block:\n%s", got)
	}
}

// --no-docs is the escape hatch, and it is the same word `workbook setup`
// already uses for the same decision.
func TestStatusChangeSkipsTheGuidelinesOnRequest(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	before := readProjectFile(t, repository, agentdocs.GuidelinesPath)

	document := cliStatusMutation(t, repository, "status add",
		"status", "add", "triage", "--no-docs", "--json")
	if document.Docs != nil {
		t.Fatalf("docs = %#v, want nothing reported for a skipped refresh", document.Docs)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != before {
		t.Fatalf("guidelines were regenerated despite --no-docs:\n%s", got)
	}

	code, stdout, stderr := run(t, repository, "status", "add", "archived", "--no-docs")
	if code != 0 || stderr != "" {
		t.Fatalf("status add --no-docs = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tdocs:\tskipped") {
		t.Errorf("status add --no-docs text = %q, want the skipped line", stdout)
	}
	// The skipped refresh leaves work behind, and `workbook docs` is where it is
	// found. Nothing else has to notice.
	if code, stdout, _ := run(t, repository, "docs", "status"); code != 0 ||
		!strings.Contains(stdout, string(agentdocs.StateStale)) {
		t.Errorf("docs status = code %d, %q; want the guidelines reported stale", code, stdout)
	}
}
