package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
)

// A build from the future is the one thing these tests cannot install, so they
// forge its output instead.
//
// Everything below writes real Git objects with plumbing — a commit whose
// operation.json declares a writer-format generation this build does not have,
// carries an operation type it has never heard of, and whose state.json carries
// a member it cannot decode. That is exactly the shape the comments and
// attachments story will produce, and it is the only shape worth testing: a
// forged pack that merely claims the marker without using it would pass for
// reasons that will not hold when the real one arrives.
const futureGeneration = core.SupportedFormatGeneration + 1

// markGeneration sets a stored document's writer-format marker: it replaces the
// marker the document already carries, or inserts one after the envelope
// version if it carries none. It is the same helper, in the same shape, as
// internal/gitstore's markGeneration; the two packages cannot share a test file,
// so they share a shape instead.
//
// Replace-if-present is the whole point. The insert-only form this replaced was
// written when a configuration checkpoint carried no marker of its own. Now that
// every ledger starts from a genesis stamped with one, a bare insert produced a
// document with two minReader members, and Go's decoder takes the last — so the
// forgery quietly claimed the generation it was trying to exceed, the
// newer-writer path was never selected, and three tests read a working mechanism
// as corruption.
var storedMarkerPattern = regexp.MustCompile(`"minReader":\d+`)

// Generation zero is forged explicitly, as `"minReader":0`, rather than by
// leaving the member out. The two are different documents to a reader — absence
// is the canonical spelling and an explicit zero is one this build refuses —
// and a forge that silently returned the document untouched for zero would
// produce an unforged document, failing whatever test used it with a shape
// complaint rather than the refusal it was checking for.
func markGeneration(document string, generation int) string {
	marker := fmt.Sprintf(`"minReader":%d`, generation)
	if storedMarkerPattern.MatchString(document) {
		return storedMarkerPattern.ReplaceAllString(document, marker)
	}
	return strings.Replace(document, `"version":1,`, `"version":1,`+marker+`,`, 1)
}

// assertOneMarker fails loudly when a forged document carries anything but one
// writer-format marker. A silent duplicate is what made the last shape change
// read as a production bug; this guard is what makes the next one honest.
func assertOneMarker(t *testing.T, what, document string) {
	t.Helper()
	if count := strings.Count(document, `"minReader"`); count != 1 {
		t.Fatalf("%s carries %d minReader members, want exactly 1: %s", what, count, document)
	}
}

// writeFutureTaskCommit appends one commit to a task ref that only a newer
// Workbook could have written, and returns its object ID.
func writeFutureTaskCommit(t *testing.T, repository, taskID string) string {
	t.Helper()
	ref := "refs/workbook/tasks/" + taskID
	head := cliGitOutput(t, repository, "rev-parse", ref)
	state, err := core.DecodeStateDocument([]byte(cliGitOutput(t, repository, "show", head+":state.json") + "\n"))
	if err != nil {
		t.Fatalf("DecodeStateDocument(%s) error = %v", taskID, err)
	}

	operation := fmt.Sprintf(
		`{"format":"workbook.operation-pack","version":1,"minReader":%d,"projectId":%q,"taskId":%q,`+
			`"historyGeneration":%q,"actor":{"id":"future@example.test"},"logicalClock":%d,`+
			`"wallTime":"2027-01-01T00:00:00Z","operations":[{"id":"01KZYHVT1D070XVGT7J0M99QAH",`+
			`"type":"comment.add","body":"written by a newer workbook"}]}`+"\n",
		futureGeneration, state.ProjectID, state.TaskID, state.History.Generation, state.LogicalClock+1)

	stored := cliGitOutput(t, repository, "show", head+":state.json")
	marked := markGeneration(stored, futureGeneration)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"task":{`, `"comments":[{"body":"written by a newer workbook"}],"task":{`, 1)
	if marked == stored {
		t.Fatal("the checkpoint substitutions matched nothing; the stored document changed shape")
	}
	assertOneMarker(t, "the forged task checkpoint", marked)

	operationBlob := hashObject(t, repository, operation)
	stateBlob := hashObject(t, repository, marked+"\n")
	tree := gitWithInput(t, repository, fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		operationBlob, stateBlob), "mktree")
	commit := gitWithInput(t, repository, "workbook: comment on "+taskID,
		"commit-tree", tree, "-p", head)
	cliGit(t, repository, "update-ref", ref, commit, head)
	return commit
}

// writeFutureConfigCommit does the same to the configuration ledger.
func writeFutureConfigCommit(t *testing.T, repository string) string {
	t.Helper()
	const ref = "refs/workbook/config"
	head := cliGitOutput(t, repository, "rev-parse", ref)
	state, err := core.DecodeConfigStateDocument([]byte(cliGitOutput(t, repository, "show", head+":state.json") + "\n"))
	if err != nil {
		t.Fatalf("DecodeConfigStateDocument() error = %v", err)
	}

	operation := fmt.Sprintf(
		`{"format":"workbook.config-operation-pack","version":1,"minReader":%d,"projectId":%q,`+
			`"historyGeneration":%q,"actor":{"id":"future@example.test"},"logicalClock":%d,`+
			`"wallTime":"2027-01-01T00:00:00Z","operations":[{"id":"01KZYHVT1D070XVGT7J0M99QAJ",`+
			`"type":"template.add","template":"bug"}]}`+"\n",
		futureGeneration, state.ProjectID, state.History.Generation, state.LogicalClock+1)

	stored := cliGitOutput(t, repository, "show", head+":state.json")
	marked := markGeneration(stored, futureGeneration)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"config":{`, `"templates":[{"name":"bug"}],"config":{`, 1)
	if marked == stored {
		t.Fatal("the ledger substitutions matched nothing; the stored document changed shape")
	}
	assertOneMarker(t, "the forged configuration checkpoint", marked)

	operationBlob := hashObject(t, repository, operation)
	stateBlob := hashObject(t, repository, marked+"\n")
	tree := gitWithInput(t, repository, fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		operationBlob, stateBlob), "mktree")
	commit := gitWithInput(t, repository, "workbook: add a template", "commit-tree", tree, "-p", head)
	cliGit(t, repository, "update-ref", ref, commit, head)
	return commit
}

func hashObject(t *testing.T, repository, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write object: %v", err)
	}
	return cliGitOutput(t, repository, "hash-object", "-w", path)
}

func gitWithInput(t *testing.T, repository, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// The whole contract, over a real bare remote and two real clones.
//
// One clone plays the future: it pushes a task history and a configuration
// ledger that declare a generation the other cannot fold. The other clone is
// this build, and everything asserted below is what it must do — serve what it
// can, refuse what it must, keep synchronizing, and lose nothing.
func TestANewerWritersHistoryIsServedRefusedAndNeverWedged(t *testing.T) {
	local, future := cliSyncRepositories(t)

	fetched := cliCreateTask(t, local, "Fetched from the future")
	diverged := cliCreateTask(t, local, "Diverged from the future")
	untouched := cliCreateTask(t, local, "Ordinary task")
	if code, _, stderr := run(t, local, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}

	writeFutureTaskCommit(t, future, fetched.ID)
	writeFutureTaskCommit(t, future, diverged.ID)
	cliGit(t, future, "push", "--quiet", "origin",
		"refs/workbook/tasks/"+fetched.ID, "refs/workbook/tasks/"+diverged.ID)

	// One local, unpublished change on the task origin has since moved past.
	// This is the hard case: replaying it means folding a history this build
	// cannot read.
	cliUpdateTitle(t, local, diverged.ID, "Renamed locally")
	localDivergedHead := cliGitOutput(t, local, "rev-parse", "refs/workbook/tasks/"+diverged.ID)

	code, stdout, stderr := run(t, local, "sync", "--json")
	if code != 9 {
		t.Fatalf("sync code = %d, want 9 (newer-writer); stdout = %q stderr = %q", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	if !strings.Contains(stderr, "newer workbook") {
		t.Fatalf("sync error = %q, want it to name a newer workbook", stderr)
	}

	// Refs advanced where they could. The fetched task is at origin's tip, the
	// ordinary task published, and the divergent task still holds its local
	// operation.
	remoteFetchedHead := cliGitOutput(t, local, "rev-parse", "refs/workbook/remotes/origin/tasks/"+fetched.ID)
	if got := cliGitOutput(t, local, "rev-parse", "refs/workbook/tasks/"+fetched.ID); got != remoteFetchedHead {
		t.Fatalf("fetched task ref = %q, want origin's tip %q", got, remoteFetchedHead)
	}
	if got := cliGitOutput(t, local, "rev-parse", "refs/workbook/tasks/"+diverged.ID); got != localDivergedHead {
		t.Fatalf("divergent task ref = %q, want the local head %q; local work must be preserved", got, localDivergedHead)
	}
	if got := cliGitOutput(t, future, "ls-remote", "origin", "refs/workbook/tasks/"+untouched.ID); !strings.Contains(got, untouched.ID) {
		t.Fatalf("origin does not hold the ordinary task: %q", got)
	}

	// Reads serve the newer task from its checkpoint, and say so.
	code, stdout, stderr = run(t, local, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "list")
	var listed []core.Task
	if err := json.Unmarshal(envelope.Data, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	newer := 0
	for _, task := range listed {
		if task.ID == fetched.ID {
			if !task.NewerWriter {
				t.Fatal("the fetched task does not report a newer writer")
			}
			if task.Title != fetched.Title {
				t.Fatalf("fetched task title = %q, want the checkpoint's %q", task.Title, fetched.Title)
			}
			newer++
		}
	}
	if newer != 1 {
		t.Fatalf("list returned %d newer-writer tasks, want 1; output = %q", newer, stdout)
	}
	assertNewerWriterWarning(t, envelope.Warnings, fetched.ID)

	// The board and show carry the same advisory, and the board still renders.
	code, stdout, stderr = run(t, local, "board", "--json")
	if code != 0 {
		t.Fatalf("board code = %d, want 0; stderr = %q", code, stderr)
	}
	assertNewerWriterWarning(t, assertJSONResult(t, stdout, "board").Warnings, fetched.ID)
	code, stdout, stderr = run(t, local, "show", fetched.ID, "--json")
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	assertNewerWriterWarning(t, assertJSONResult(t, stdout, "show").Warnings, fetched.ID)

	// Mutating it is refused, by name, with the upgrade message.
	code, _, stderr = run(t, local, "update", fetched.ID, "--title", "Mine now", "--no-sync", "--json")
	if code != 9 {
		t.Fatalf("update code = %d, want 9; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	for _, want := range []string{fetched.ID, "newer workbook", "upgrade workbook"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("update error = %q, want it to contain %q", stderr, want)
		}
	}
	for _, forbidden := range []string{"corrupt", "damaged", "unreadable"} {
		if strings.Contains(stderr, forbidden) {
			t.Fatalf("update error = %q, want it not to imply damage with %q", stderr, forbidden)
		}
	}

	// Every other task is unaffected.
	if code, _, stderr := run(t, local, "update", untouched.ID, "--title", "Still mine", "--no-sync"); code != 0 {
		t.Fatalf("ordinary update code = %d, want 0; stderr = %q", code, stderr)
	}

	// validate reports it as newer-writer, scoped to the task, and not as
	// corrupt data.
	code, stdout, stderr = run(t, local, "validate", "--json")
	if code != 9 {
		t.Fatalf("validate code = %d, want 9; stderr = %q", code, stderr)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode validate: %v", err)
	}
	if report.NewerWriter != 1 || report.Invalid != 0 {
		t.Fatalf("validate reported %d newer-writer and %d invalid, want 1 and 0", report.NewerWriter, report.Invalid)
	}
	found := false
	for _, failure := range report.Failures {
		if failure.TaskID != fetched.ID {
			continue
		}
		found = true
		if core.Category(failure.Category) != core.CategoryNewerWriter {
			t.Fatalf("validate failure category = %q, want %q", failure.Category, core.CategoryNewerWriter)
		}
	}
	if !found {
		t.Fatalf("validate did not name %s among its failures: %+v", fetched.ID, report.Failures)
	}

	// A second sync says the same thing and changes nothing: the refusal is
	// stable rather than a state the next run works past.
	code, _, stderr = run(t, local, "sync", "--json")
	if code != 9 {
		t.Fatalf("second sync code = %d, want 9; stderr = %q", code, stderr)
	}
	if got := cliGitOutput(t, local, "rev-parse", "refs/workbook/tasks/"+diverged.ID); got != localDivergedHead {
		t.Fatalf("divergent task ref moved to %q on the second sync", got)
	}
}

// The configuration ledger answers the same way, and vocabulary resolution
// keeps working from the checkpoint while it does.
func TestANewerWritersConfigurationLedgerIsResolvedAndRefused(t *testing.T) {
	local, future := cliSyncRepositories(t)
	task := cliCreateTask(t, local, "A task to file")
	if code, _, stderr := run(t, local, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}
	// The ledger is seeded lazily, so the future clone has to change a status
	// before it has a configuration history to write into.
	if code, _, stderr := run(t, future, "status", "label", "backlog", "Inbox", "--no-docs"); code != 0 {
		t.Fatalf("seeding the ledger code = %d; stderr = %q", code, stderr)
	}

	writeFutureConfigCommit(t, future)
	cliGit(t, future, "push", "--quiet", "origin", "refs/workbook/config")

	if code, _, stderr := run(t, local, "fetch"); code != 0 {
		t.Fatalf("fetch code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := cliGitOutput(t, local, "rev-parse", "refs/workbook/config"); got != cliGitOutput(t, local, "rev-parse", "refs/workbook/remotes/origin/config") {
		t.Fatal("the configuration ref did not fast-forward to origin's newer ledger")
	}

	// Resolution still works: the board's columns come from the checkpoint.
	code, stdout, stderr := run(t, local, "status", "list", "--json")
	if code != 0 {
		t.Fatalf("status list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "in-progress") {
		t.Fatalf("status list = %q, want the checkpoint's vocabulary", stdout)
	}
	if code, _, stderr := run(t, local, "update", task.ID, "--status", "in-progress", "--no-sync"); code != 0 {
		t.Fatalf("filing a task under a resolved status code = %d, want 0; stderr = %q", code, stderr)
	}

	// Changing the configuration is refused with the upgrade message.
	code, _, stderr = run(t, local, "status", "add", "triage", "--label", "Triage", "--no-sync", "--json")
	if code != 9 {
		t.Fatalf("status add code = %d, want 9; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	for _, want := range []string{"newer workbook", "upgrade workbook"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("status add error = %q, want it to contain %q", stderr, want)
		}
	}

	// validate reports the ledger distinctly, and not as corrupt data.
	code, stdout, stderr = run(t, local, "validate", "--json")
	if code != 9 {
		t.Fatalf("validate code = %d, want 9; stderr = %q", code, stderr)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode validate: %v", err)
	}
	if report.Config == nil || report.Config.Failure == nil {
		t.Fatalf("validate reported no ledger failure: %+v", report.Config)
	}
	if core.Category(report.Config.Failure.Category) != core.CategoryNewerWriter {
		t.Fatalf("ledger failure category = %q, want %q", report.Config.Failure.Category, core.CategoryNewerWriter)
	}
	if report.Invalid != 0 {
		t.Fatalf("validate counted %d invalid task(s); the tasks are sound", report.Invalid)
	}
}

// A configuration divergence against a newer ledger is refused, and the local
// operations stay exactly where they are.
//
// This is the ledger's version of the hard case: replaying a local status
// change onto origin's tip would mean folding a configuration history this
// build cannot read. Refusing costs the clone nothing it had — its own ledger
// is untouched and its own statuses keep working — and publishing is what
// waits for the upgrade.
func TestANewerWritersConfigurationDivergenceIsRefusedAndPreserved(t *testing.T) {
	local, future := cliSyncRepositories(t)
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "status", "label", "backlog", "Inbox", "--no-docs"); code != 0 {
		t.Fatalf("seeding the ledger code = %d; stderr = %q", code, stderr)
	}
	writeFutureConfigCommit(t, future)
	cliGit(t, future, "push", "--quiet", "origin", "refs/workbook/config")

	// The local clone records its own status change without ever seeing
	// origin's ledger.
	if code, _, stderr := run(t, local, "status", "label", "ready", "Up Next", "--no-sync", "--no-docs"); code != 0 {
		t.Fatalf("local status label code = %d; stderr = %q", code, stderr)
	}
	localConfigHead := cliGitOutput(t, local, "rev-parse", "refs/workbook/config")

	code, _, stderr := run(t, local, "sync", "--json")
	if code != 9 {
		t.Fatalf("sync code = %d, want 9 (newer-writer); stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	if got := cliGitOutput(t, local, "rev-parse", "refs/workbook/config"); got != localConfigHead {
		t.Fatalf("local ledger moved to %q, want it left at %q with its unpublished change", got, localConfigHead)
	}

	code, stdout, stderr := run(t, local, "status", "list", "--json")
	if code != 0 {
		t.Fatalf("status list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Up Next") {
		t.Fatalf("status list = %q, want the local clone's own label to have survived", stdout)
	}
}

// The refusal is reported once, and the push does not repeat it in the language
// of a network failure.
//
// The ledger is not published over a newer origin for the same reason the task
// refs are not: this clone's ref is not a descendant of origin's, so the push
// can only be rejected. Reporting that rejection would follow "needs upgrade"
// with "could not publish refs/workbook/config … failed to push some refs",
// which describes the same refusal twice and blames the wrong thing the second
// time.
func TestANewerWritersLedgerIsNotRepublishedOverOrigin(t *testing.T) {
	local, future := cliSyncRepositories(t)
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "status", "label", "backlog", "Inbox", "--no-docs"); code != 0 {
		t.Fatalf("seeding the ledger code = %d; stderr = %q", code, stderr)
	}
	writeFutureConfigCommit(t, future)
	cliGit(t, future, "push", "--quiet", "origin", "refs/workbook/config")

	if code, _, stderr := run(t, local, "status", "label", "ready", "Up Next", "--no-sync", "--no-docs"); code != 0 {
		t.Fatalf("local status label code = %d; stderr = %q", code, stderr)
	}
	originConfigHead := cliGitOutput(t, future, "ls-remote", "origin", "refs/workbook/config")

	code, stdout, stderr := run(t, local, "sync", "--json")
	if code != 9 {
		t.Fatalf("sync code = %d, want 9; stderr = %q", code, stderr)
	}
	// One statement about the ledger, in the words of the refusal.
	if !strings.Contains(stdout, string(gitstore.SyncConfigNeedsUpgrade)) {
		t.Fatalf("sync report = %q, want the ledger reported as %q", stdout, gitstore.SyncConfigNeedsUpgrade)
	}
	for _, forbidden := range []string{"could not publish", "failed to push"} {
		if strings.Contains(stdout, forbidden) || strings.Contains(stderr, forbidden) {
			t.Fatalf("sync reported %q as well as the refusal; the push must not repeat it\nstdout = %q\nstderr = %q",
				forbidden, stdout, stderr)
		}
	}
	// Origin's ledger is untouched, which is the other half of "no push was
	// attempted".
	if got := cliGitOutput(t, future, "ls-remote", "origin", "refs/workbook/config"); got != originConfigHead {
		t.Fatalf("origin's ledger moved to %q, want it left at %q", got, originConfigHead)
	}
}

// A divergent task whose origin history needs a newer Workbook is withheld from
// the push, and this pins the withholding rather than its consequences.
//
// Removing the skip leaves every other assertion in this file passing: the ref
// still holds the local operations and the run still exits 9. What changes is
// that origin rejects a push nobody should have made, and the run says so in a
// second, transport-flavored error.
func TestANewerWritersDivergentTaskIsNotPushed(t *testing.T) {
	local, future := cliSyncRepositories(t)
	diverged := cliCreateTask(t, local, "Diverged from the future")
	if code, _, stderr := run(t, local, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}
	writeFutureTaskCommit(t, future, diverged.ID)
	cliGit(t, future, "push", "--quiet", "origin", "refs/workbook/tasks/"+diverged.ID)
	cliUpdateTitle(t, local, diverged.ID, "Renamed locally")

	code, stdout, stderr := run(t, local, "sync", "--json")
	if code != 9 {
		t.Fatalf("sync code = %d, want 9; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, string(gitstore.SyncNeedsUpgrade)) {
		t.Fatalf("sync report = %q, want the task reported as %q", stdout, gitstore.SyncNeedsUpgrade)
	}
	if strings.Contains(stdout, string(gitstore.SyncRejected)) {
		t.Fatalf("sync report = %q, want no rejected push; the divergent task must be withheld", stdout)
	}
	for _, forbidden := range []string{"rejected by origin", "failed to push"} {
		if strings.Contains(stdout, forbidden) || strings.Contains(stderr, forbidden) {
			t.Fatalf("sync reported %q; the push must not be attempted\nstdout = %q\nstderr = %q",
				forbidden, stdout, stderr)
		}
	}
}

// The status strings are contract: a caller filters on them, and collapsing
// either into "invalid" would tell a script the repository is damaged.
func TestTheNeedsUpgradeStatusStringsAreWhatCallersRead(t *testing.T) {
	if got := string(gitstore.SyncNeedsUpgrade); got != "needs-upgrade" {
		t.Fatalf("SyncNeedsUpgrade = %q, want %q", got, "needs-upgrade")
	}
	if got := string(gitstore.SyncConfigNeedsUpgrade); got != "needs-upgrade" {
		t.Fatalf("SyncConfigNeedsUpgrade = %q, want %q", got, "needs-upgrade")
	}
	for _, invalid := range []string{string(gitstore.SyncInvalid), string(gitstore.SyncConfigInvalid)} {
		if invalid == "needs-upgrade" {
			t.Fatal("the needs-upgrade status collapsed into the invalid one")
		}
	}
}

// next and next --limit carry the same newer-writer advisory the read
// commands do, for the task(s) they offer.
//
// This reuses the fixture TestANewerWritersHistoryIsServedRefusedAndNeverWedged
// stages: a task written by a future clone, synced into this build. Here
// neither task carries a local, unpublished change — there is nothing to fold
// against the newer history — so this is the fixture's "fetched" case, not its
// "diverged" one, and sync fast-forwards cleanly rather than refusing.
func TestNextReportsATaskWrittenByANewerWorkbook(t *testing.T) {
	local, future := cliSyncRepositories(t)

	// Higher priority, so next picks it first regardless of ID order.
	first := createOrderingTask(t, local, "Chosen by next, written by the future", "high")
	second := createOrderingTask(t, local, "Runner-up, written by this build", "medium")
	if code, _, stderr := run(t, local, "sync"); code != 0 {
		t.Fatalf("initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, future, "sync"); code != 0 {
		t.Fatalf("future clone sync code = %d; stderr = %q", code, stderr)
	}

	writeFutureTaskCommit(t, future, first.ID)
	cliGit(t, future, "push", "--quiet", "origin", "refs/workbook/tasks/"+first.ID)

	// Local has no unpublished change on either task, so this sync only needs
	// to fast-forward the first task's ref to origin's newer-writer tip — it
	// does not need to fold anything, and it succeeds (exit 0), unlike the
	// "diverged" case in TestANewerWritersHistoryIsServedRefusedAndNeverWedged
	// where a local mutation forces a fold against the newer history and the
	// run exits 9.
	if code, _, stderr := run(t, local, "sync"); code != 0 {
		t.Fatalf("sync code = %d, want 0 (no local divergence to fold); stderr = %q", code, stderr)
	}

	// Plain next: the newer-writer task is the only one eligible ahead of its
	// neighbor by priority, and it carries the advisory.
	code, stdout, stderr := run(t, local, "next", "--json", "--no-sync")
	if code != 0 {
		t.Fatalf("next code = %d, want 0; stderr = %q", code, stderr)
	}
	envelope := assertJSONResult(t, stdout, "next")
	var task core.Task
	if err := json.Unmarshal(envelope.Data, &task); err != nil {
		t.Fatalf("decode next task: %v", err)
	}
	if task.ID != first.ID {
		t.Fatalf("next chose %s, want %s", task.ID, first.ID)
	}
	if !task.NewerWriter {
		t.Fatal("next's task does not report a newer writer")
	}
	assertNewerWriterWarning(t, envelope.Warnings, first.ID)

	code, _, stderr = run(t, local, "next", "--no-sync")
	if code != 0 {
		t.Fatalf("next (text) code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "newer workbook") {
		t.Fatalf("next stderr = %q, want it to name a newer workbook", stderr)
	}

	// next --limit: the advisory follows the specific task it names, not its
	// neighbor, which this build wrote.
	code, stdout, stderr = run(t, local, "next", "--limit", "2", "--json", "--no-sync")
	if code != 0 {
		t.Fatalf("next --limit code = %d, want 0; stderr = %q", code, stderr)
	}
	envelope = assertJSONResult(t, stdout, "next")
	var document struct {
		Tasks    []core.Task `json:"tasks"`
		Eligible int         `json:"eligible"`
	}
	if err := json.Unmarshal(envelope.Data, &document); err != nil {
		t.Fatalf("decode next --limit document: %v", err)
	}
	if len(document.Tasks) != 2 {
		t.Fatalf("next --limit offered %d tasks, want 2", len(document.Tasks))
	}
	if document.Tasks[0].ID != first.ID || !document.Tasks[0].NewerWriter {
		t.Fatalf("first offered task = %+v, want %s with NewerWriter set", document.Tasks[0], first.ID)
	}
	if document.Tasks[1].ID != second.ID || document.Tasks[1].NewerWriter {
		t.Fatalf("second offered task = %+v, want %s without NewerWriter", document.Tasks[1], second.ID)
	}
	assertNewerWriterWarning(t, envelope.Warnings, first.ID)
	for _, warning := range envelope.Warnings {
		if warning.Code == core.WarningNewerWriter && strings.Contains(warning.Message, second.ID) {
			t.Fatalf("advisory = %q, want it not to name %s, which this build wrote", warning.Message, second.ID)
		}
	}
}

func assertNewerWriterWarning(t *testing.T, warnings []core.Warning, taskID string) {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code != core.WarningNewerWriter {
			continue
		}
		if !strings.Contains(warning.Message, taskID) {
			t.Fatalf("advisory = %q, want it to name %s", warning.Message, taskID)
		}
		if !strings.Contains(warning.Message, "newer workbook") {
			t.Fatalf("advisory = %q, want it to name a newer workbook", warning.Message)
		}
		return
	}
	t.Fatalf("no newer-writer advisory among %+v", warnings)
}
