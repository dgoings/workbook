package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
)

// A build from before description merges meets a task this build merged. It
// shows it from its stored state, refuses to change it, and reports it as a
// newer writer's rather than as damage. A clone of its own holding an edit from
// before the merge cannot replay it, and keeps it: its sync asks for an
// upgrade and moves neither its ref nor origin's. This build then replays that
// edit onto the merge.
func TestPreDescriptionMergeBuildRefusesAMergedTask(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a pre-merge build; skipped in -short mode")
	}
	t.Parallel()
	binary := preMergeBinary(t, core.DescriptionMergeMinReader-1)
	writer, other := cliSyncRepositories(t)
	older := cliClone(t, cliGitOutput(t, writer, "remote", "get-url", "origin"))

	const base = "one\ntwo\nthree\nfour\nfive\n"
	const merged = "ONE\ntwo\nthree\nfour\nFIVE\n"
	code, stdout, stderr := run(t, writer, "create", "Merged task", "--description", base, "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	taskID := decodeMutationTask(t, stdout, "create").ID
	for _, step := range []struct{ repository, command string }{{writer, "push"}, {other, "fetch"}} {
		if code, stdout, stderr := run(t, step.repository, step.command); code != 0 {
			t.Fatalf("%s = (%d, %q, %q)", step.command, code, stdout, stderr)
		}
	}
	// The older clone takes the task as it stands before the merge and saves a
	// title it does not publish.
	if code, stdout, stderr := runBinary(t, binary, older, "setup"); code != 0 {
		t.Fatalf("older setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "update", taskID, "--title", "Mine locally", "--no-sync"); code != 0 {
		t.Fatalf("older update = (%d, %q, %q)", code, stdout, stderr)
	}
	held := localTaskRef(t, older, taskID)

	// Edits of different lines, merged by the second one's sync.
	cliUpdateDescription(t, writer, taskID, strings.Replace(base, "one", "ONE", 1))
	cliUpdateDescription(t, other, taskID, strings.Replace(base, "five", "FIVE", 1))
	if code, stdout, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, other, "sync", "--json")
	if code != 0 {
		t.Fatalf("merging sync = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	envelope := assertJSONResult(t, stdout, "sync")
	var synced gitstore.SyncRunResult
	if err := json.Unmarshal(envelope.Data, &synced); err != nil {
		t.Fatalf("decode merging sync: %v", err)
	}
	assertCLISyncStatus(t, synced.Fetch, taskID, gitstore.SyncReconciled)
	mergeWarnings := 0
	for _, warning := range envelope.Warnings {
		if warning.Code == core.WarningDescriptionMerged {
			mergeWarnings++
		}
	}
	if mergeWarnings != 1 {
		t.Fatalf("merging sync warnings = %+v, want one %s", envelope.Warnings, core.WarningDescriptionMerged)
	}
	tip := remoteTaskRefValue(t, other, taskID)
	if tip != localTaskRef(t, other, taskID) {
		t.Fatalf("origin holds %s, want the merged %s", tip, localTaskRef(t, other, taskID))
	}
	if pack := gitOutput(t, other, "show", tip+":operation.json"); !strings.Contains(pack, `"type":"description.merge"`) {
		t.Fatalf("operation.json of the merge records none: %s", pack)
	}
	for _, name := range []string{"operation.json", "state.json"} {
		if document := gitOutput(t, other, "show", tip+":"+name); !strings.Contains(document, `"minReader":10`) {
			t.Fatalf("%s of the merge carries no generation-ten marker: %s", name, document)
		}
	}

	// The clone holding its own edit cannot replay it onto the merge.
	for attempt := range 2 {
		code, stdout, stderr = runBinary(t, binary, older, "sync", "--json")
		if report := stdout + stderr; code != 9 ||
			!strings.Contains(report, `"taskId":"`+taskID+`","status":"needs-upgrade"`) {
			t.Fatalf("older sync %d = (%d, %q, %q), want exit 9 and %s as needs-upgrade", attempt+1, code, stdout, stderr, taskID)
		}
		if got := localTaskRef(t, older, taskID); got != held {
			t.Fatalf("older sync %d moved its task from %s to %s", attempt+1, held, got)
		}
		if got := remoteTaskRefValue(t, older, taskID); got != tip {
			t.Fatalf("older sync %d moved origin's task from %s to %s", attempt+1, tip, got)
		}
	}

	// A clone with no edit of its own takes the merge, shows it and refuses to
	// change it.
	fresh := cliClone(t, cliGitOutput(t, writer, "remote", "get-url", "origin"))
	if code, stdout, stderr := runBinary(t, binary, fresh, "setup"); code != 0 {
		t.Fatalf("older setup of a fresh clone = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := localTaskRef(t, fresh, taskID); got != tip {
		t.Fatalf("older fresh clone holds %s, want the merge %s", got, tip)
	}
	code, stdout, stderr = runBinary(t, binary, fresh, "show", taskID, "--json")
	if code != 0 {
		t.Fatalf("older show = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	shownEnvelope := assertJSONResult(t, stdout, "show")
	var shown core.Task
	if err := json.Unmarshal(shownEnvelope.Data, &shown); err != nil {
		t.Fatalf("decode older show: %v", err)
	}
	if shown.Description != merged || !shown.NewerWriter || shown.Title != "Merged task" {
		t.Fatalf("older show = %+v, want the merged description marked newerWriter", shown)
	}
	assertNewerWriterWarning(t, shownEnvelope.Warnings, taskID)

	code, stdout, stderr = runBinary(t, binary, fresh, "update", taskID, "--title", "Changed by an older build")
	if code != 9 || !strings.Contains(stderr, "was written by a newer workbook; upgrade workbook") {
		t.Fatalf("older update = (%d, %q, %q), want exit 9", code, stdout, stderr)
	}
	code, stdout, stderr = runBinary(t, binary, fresh, "validate", "--json")
	if code != 9 {
		t.Fatalf("older validate = (%d, %q, %q), want exit 9", code, stdout, stderr)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode older validate: %v", err)
	}
	if report.NewerWriter != 1 || report.Invalid != 0 || strings.Contains(stdout+stderr, string(core.CategoryCorruptData)) {
		t.Fatalf("older validate = (%q, %q), want one newer-writer task and nothing corrupt", stdout, stderr)
	}
	if got := localTaskRef(t, fresh, taskID); got != tip {
		t.Fatalf("a refused update moved the task from %s to %s", tip, got)
	}
	if got := remoteTaskRefValue(t, fresh, taskID); got != tip {
		t.Fatalf("origin's task moved from %s to %s", tip, got)
	}

	// Upgraded, the clone that was left behind replays its title onto the merge.
	code, stdout, stderr = run(t, older, "sync", "--json")
	if code != 0 {
		t.Fatalf("sync after the upgrade = (%d, %q, %q)", code, stdout, stderr)
	}
	got := cliShowTask(t, older, taskID)
	if got.Title != "Mine locally" || got.Description != merged || got.NewerWriter {
		t.Fatalf("task after the upgrade = %+v, want the older build's title on the merged description", got)
	}
	replayed := localTaskRef(t, older, taskID)
	if published := remoteTaskRefValue(t, older, taskID); published != replayed || replayed == tip {
		t.Fatalf("origin holds %s after the upgraded sync, want the replayed %s", published, replayed)
	}
	if parent := gitOutput(t, older, "rev-parse", replayed+"^"); parent != tip {
		t.Fatalf("the replayed title's parent = %s, want the merge %s", parent, tip)
	}
}
