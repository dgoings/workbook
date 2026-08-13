package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
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
	marked := strings.Replace(stored, `"version":1,`,
		fmt.Sprintf(`"version":1,"minReader":%d,`, futureGeneration), 1)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"task":{`, `"comments":[{"body":"written by a newer workbook"}],"task":{`, 1)
	if marked == stored {
		t.Fatal("the checkpoint substitutions matched nothing; the stored document changed shape")
	}

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
	marked := strings.Replace(stored, `"version":1,`,
		fmt.Sprintf(`"version":1,"minReader":%d,`, futureGeneration), 1)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"config":{`, `"templates":[{"name":"bug"}],"config":{`, 1)
	if marked == stored {
		t.Fatal("the ledger substitutions matched nothing; the stored document changed shape")
	}

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
