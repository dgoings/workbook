package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// compactedOnOriginDetailFor is the fallback detail for the task
// compactedOnOriginTask leaves stuck, with its one unpushed change.
func compactedOnOriginDetailFor(taskID string) string {
	return `"Kept on the old history" was compacted on origin; you have 1 unpushed changes on the old history. ` +
		`Run "workbook compact --adopt ` + taskID + `" to retry, ` +
		`or add --discard-local to take origin's version after saving yours to a bundle.`
}

func cannotCombineDetailFor(title, taskID string) string {
	return `"` + title + `" changed on origin after you compacted it, and the two can't be combined. ` +
		`Run "workbook compact --adopt ` + taskID + ` --discard-local" to take origin's version ` +
		`after saving yours to a bundle; then compact it again.`
}

// adoptJSON runs compact --adopt with --json, which must succeed, and decodes
// its result.
func adoptJSON(t *testing.T, repository string, args ...string) (compactAdoptResult, compactEnvelope) {
	t.Helper()
	stdout := compactRun(t, repository, append(append([]string{"compact", "--adopt"}, args...), "--json")...)
	var result compactAdoptResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "compact").Data, &result); err != nil {
		t.Fatalf("decode adopt result: %v; output = %q", err, stdout)
	}
	return result, decodeCompactEnvelope(t, stdout)
}

// fetchOutcome runs `sync --json` in repository and returns the fetch phase's
// line for taskID, requiring the run to exit 0.
func syncFetchOutcome(t *testing.T, repository, taskID string) gitstore.SyncTaskResult {
	t.Helper()
	stdout := compactRun(t, repository, "sync", "--json")
	var result gitstore.SyncRunResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &result); err != nil {
		t.Fatalf("decode sync: %v; output = %q", err, stdout)
	}
	for _, item := range result.Fetch.Tasks {
		if item.TaskID == taskID {
			return item
		}
	}
	t.Fatalf("sync fetch tasks = %+v, want a line for %s", result.Fetch.Tasks, taskID)
	return gitstore.SyncTaskResult{}
}

// bundleHeads lists the refs a bundle holds, by name.
func bundleHeads(t *testing.T, repository, bundle string) map[string]string {
	t.Helper()
	heads := map[string]string{}
	for _, line := range strings.Split(cliGitOutput(t, repository, "bundle", "list-heads", bundle), "\n") {
		if objectID, name, found := strings.Cut(line, " "); found {
			heads[name] = objectID
		}
	}
	return heads
}

func TestCompactAdoptRejectsOtherModes(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Not stuck")
	const combined = "--adopt cannot be combined with --done-only, --deleted-only, --since, --keep-attachments or --dry-run"
	for _, args := range [][]string{
		{"--adopt", task.ID, "--done-only"},
		{"--adopt", task.ID, "--done-only", "30d"},
		{"--adopt", task.ID, "--deleted-only"},
		{"--deleted-only", "7d", "--adopt", task.ID},
		{"--adopt", task.ID, "--since", "7d"},
		{"--adopt", task.ID, "--keep-attachments"},
		{"--adopt", task.ID, "--dry-run"},
		{"--adopt", task.ID, "--discard-local", "--dry-run"},
	} {
		assertCompactRefused(t, repository, combined, args...)
	}
	assertCompactRefused(t, repository, "--adopt cannot be combined with --no-sync", "--adopt", task.ID, "--no-sync")
	assertCompactRefused(t, repository, "--discard-local requires --adopt", "--discard-local")
	assertCompactRefused(t, repository, "--discard-local requires --adopt", "--discard-local", "--no-backup")
	assertCompactRefused(t, repository, "--no-backup and --backup cannot be combined",
		"--adopt", task.ID, "--discard-local", "--no-backup", "--backup", t.TempDir())
}

// A task in neither stuck state has nothing to adopt, and saying so is an
// invocation error rather than a silent success.
func TestCompactAdoptWithNothingStuckIsAnInvocationError(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Nothing to adopt")
	before := taskRefs(t, first)
	code, stdout, stderr := run(t, first, "compact", "--adopt", task.ID, "--json")
	if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "probe")) || stdout != "" {
		t.Fatalf("compact --adopt = code %d, stdout %q; want the invocation code", code, stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, "task "+task.ID+" has no compaction to adopt")
	if after := taskRefs(t, first); !reflect.DeepEqual(after, before) {
		t.Fatalf("task refs = %v, want unchanged %v", after, before)
	}
	assertNoArchiveDirectory(t, first)
}

// The replay that fell back still conflicts, so the retry returns the same
// fallback and moves nothing.
func TestCompactAdoptRetryThatStillConflictsReturnsTheFallback(t *testing.T) {
	t.Parallel()
	_, second, task := compactedOnOriginTask(t)
	if item := syncFetchOutcome(t, second, task.ID); item.Status != gitstore.SyncCompactedOnOrigin {
		t.Fatalf("fetch outcome = %+v, want %s", item, gitstore.SyncCompactedOnOrigin)
	}
	before := taskRefs(t, second)[task.ID]
	code, _, stderr := run(t, second, "compact", "--adopt", task.ID, "--json")
	if code != core.ExitCode(core.Errorf(core.CategoryConflict, "probe")) {
		t.Fatalf("compact --adopt code = %d, want the conflict code; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryConflict, compactedOnOriginDetailFor(task.ID))
	if after := taskRefs(t, second)[task.ID]; after != before {
		t.Fatalf("local ref = %s, want it left on the old history at %s", after, before)
	}
	assertNoArchiveDirectory(t, second)
}

// Once origin's text agrees with the local change, the replay that fell back
// runs, and --adopt takes the checkpoint with the local change on top of it and
// publishes it. A mutation cannot settle the conflict from this side, because
// mutations refuse the stuck task, so it is settled on origin.
func TestAdoptCompactionRetrySucceedsAfterResolvingConflict(t *testing.T) {
	t.Parallel()
	first, second, task := compactedOnOriginTask(t)
	if item := syncFetchOutcome(t, second, task.ID); item.Status != gitstore.SyncCompactedOnOrigin {
		t.Fatalf("fetch outcome = %+v, want %s", item, gitstore.SyncCompactedOnOrigin)
	}
	compactRun(t, first, "update", task.ID, "--description", "Our text")
	resolved := remoteTaskRefValue(t, first, task.ID)

	result, envelope := adoptJSON(t, second, task.ID)
	if result.TaskID != task.ID || result.Status != gitstore.SyncAdoptedReplayed || result.Bundle != "" {
		t.Fatalf("adopt result = %+v, want %s %s with no bundle", result, task.ID, gitstore.SyncAdoptedReplayed)
	}
	local := taskRefs(t, second)[task.ID]
	if result.Head != local {
		t.Fatalf("adopt head = %s, want the local ref %s", result.Head, local)
	}
	if parent := cliGitOutput(t, second, "rev-parse", "refs/workbook/tasks/"+task.ID+"^"); parent != resolved {
		t.Fatalf("replayed change's parent = %s, want origin's tip %s", parent, resolved)
	}
	if got := remoteTaskRefValue(t, second, task.ID); got != local {
		t.Fatalf("origin holds %s, want the adopted tip %s published", got, local)
	}
	if envelope.Sync.Status != syncStatusCompleted || envelope.Sync.Fetch == nil {
		t.Fatalf("sync = %+v, want a completed fetch and push", envelope.Sync)
	}
	if shown := showTask(t, second, task.ID); shown.Description != "Our text" || shown.Status != core.StatusDone {
		t.Fatalf("shown task = %+v, want the agreed description on the checkpoint", shown)
	}
	if item := syncFetchOutcome(t, second, task.ID); item.Status != gitstore.SyncUnchanged {
		t.Fatalf("next fetch outcome = %+v, want %s", item, gitstore.SyncUnchanged)
	}
}

// --discard-local writes the local history, parked refs included, to a
// verified bundle at the default path first, then takes origin's checkpoint
// and keeps nothing of the old history in the clone.
func TestAdoptCompactionDiscardLocalBundlesFirst(t *testing.T) {
	t.Parallel()
	first, second, task := compactedOnOriginTask(t)
	syncFetchOutcome(t, second, task.ID)
	old := taskRefs(t, second)[task.ID]
	parked := "refs/workbook/reconciled/" + task.ID + "/0"
	cliGit(t, second, "update-ref", parked, old)
	checkpoint := remoteTaskRefValue(t, first, task.ID)

	result, envelope := adoptJSON(t, second, task.ID, "--discard-local")
	if result.TaskID != task.ID || result.Status != gitstore.SyncDiscardedLocal || result.Head != checkpoint {
		t.Fatalf("adopt result = %+v, want %s %s at origin's checkpoint %s", result, task.ID, gitstore.SyncDiscardedLocal, checkpoint)
	}
	if envelope.Sync.Status != syncStatusCompleted || envelope.Sync.Fetch == nil {
		t.Fatalf("sync = %+v, want a completed fetch", envelope.Sync)
	}
	directory := archiveDirectory(second)
	if filepath.Dir(result.Bundle) != directory || !strings.HasSuffix(result.Bundle, "-"+task.ID+"-local.bundle") {
		t.Fatalf("bundle = %q, want <run>-%s-local.bundle in %s", result.Bundle, task.ID, directory)
	}
	if result.Detail != "took origin's version; the local history is saved in "+result.Bundle {
		t.Fatalf("detail = %q, want it to name the bundle", result.Detail)
	}
	cliGit(t, second, "bundle", "verify", "--quiet", result.Bundle)
	heads := bundleHeads(t, second, result.Bundle)
	if heads["refs/workbook/tasks/"+task.ID] != old || heads[parked] != old {
		t.Fatalf("bundle heads = %v, want the old tip %s under the task ref and the parked ref", heads, old)
	}
	if got := taskRefs(t, second)[task.ID]; got != checkpoint {
		t.Fatalf("local ref = %s, want origin's checkpoint %s", got, checkpoint)
	}
	if refs := cliGitOutput(t, second, "for-each-ref", "refs/workbook/reconciled/"+task.ID+"/"); refs != "" {
		t.Fatalf("parked refs = %q, want none", refs)
	}
	if reflog := cliGitOutput(t, second, "reflog", "show", "--format=%H", "refs/workbook/tasks/"+task.ID); strings.Contains(reflog, old) {
		t.Fatalf("task reflog %q still names the old tip %s", reflog, old)
	}
	if shown := showTask(t, second, task.ID); shown.Description != "Their text" {
		t.Fatalf("shown description = %q, want origin's text", shown.Description)
	}
	if item := syncFetchOutcome(t, second, task.ID); item.Status != gitstore.SyncUnchanged {
		t.Fatalf("next fetch outcome = %+v, want %s", item, gitstore.SyncUnchanged)
	}
}

// The text output is one line naming the bundle, or saying none was written.
func TestCompactAdoptDiscardLocalTextOutput(t *testing.T) {
	t.Parallel()
	backup := t.TempDir()
	for _, args := range [][]string{{"--backup", backup}, {"--no-backup"}} {
		_, second, task := compactedOnOriginTask(t)
		stdout := compactRun(t, second, append([]string{"compact", "--adopt", task.ID, "--discard-local"}, args...)...)
		line, _, _ := strings.Cut(stdout, "\n")
		if args[0] == "--no-backup" {
			want := "Took origin's version of " + task.ID + "; the local history was discarded without a backup."
			if line != want {
				t.Fatalf("first line = %q, want %q", line, want)
			}
			assertNoArchiveDirectory(t, second)
			continue
		}
		prefix := "Took origin's version of " + task.ID + "; the local history is saved in " + backup + string(filepath.Separator)
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "-"+task.ID+"-local.bundle.") {
			t.Fatalf("first line = %q, want %q<run>-%s-local.bundle.", line, prefix, task.ID)
		}
		bundle := strings.TrimSuffix(strings.TrimPrefix(line, "Took origin's version of "+task.ID+"; the local history is saved in "), ".")
		if _, err := os.Stat(bundle); err != nil {
			t.Fatalf("bundle %s: %v", bundle, err)
		}
	}
}

// deletedAfterCompaction leaves first holding a task it compacted and then
// deleted, unpublished, while second edited the old history and published:
// origin's change cannot be combined with the deletion.
func deletedAfterCompaction(t *testing.T) (first, second string, task core.Task, local string) {
	t.Helper()
	first, second = cliSyncRepositories(t)
	task = publishedDoneTask(t, first, "Deleted after compaction")
	compactRun(t, second, "sync")
	compactRun(t, first, "compact", "--no-sync", "--no-backup")
	compactRun(t, first, "delete", task.ID, "--no-sync")
	local = taskRefs(t, first)[task.ID]
	compactRun(t, second, "update", task.ID, "--title", "Their title")
	return first, second, task, local
}

// A task this clone compacted and deleted, while origin edited the old
// history, is taken back to origin's version by --discard-local: the bundle
// holds the local checkpoint chain, the ref moves back to the old generation,
// the projection shows origin's task without a rebuild, and the next sync has
// nothing to say.
func TestCompactAdoptDiscardLocalTakesOriginsOlderHistory(t *testing.T) {
	t.Parallel()
	first, second, task, local := deletedAfterCompaction(t)
	theirs := remoteTaskRefValue(t, second, task.ID)
	code, _, stderr := run(t, first, "sync")
	if code != core.ExitCode(core.Errorf(core.CategoryConflict, "probe")) {
		t.Fatalf("sync code = %d, want the conflict code; stderr = %q", code, stderr)
	}

	// Retrying cannot combine them either.
	code, _, stderr = run(t, first, "compact", "--adopt", task.ID, "--json")
	if code != core.ExitCode(core.Errorf(core.CategoryConflict, "probe")) {
		t.Fatalf("compact --adopt code = %d, want the conflict code; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryConflict, cannotCombineDetailFor(task.Title, task.ID))
	if got := taskRefs(t, first)[task.ID]; got != local {
		t.Fatalf("local ref after the retry = %s, want it left at %s", got, local)
	}

	result, _ := adoptJSON(t, first, task.ID, "--discard-local")
	if result.Status != gitstore.SyncDiscardedLocal || result.Head != theirs {
		t.Fatalf("adopt result = %+v, want %s at origin's tip %s", result, gitstore.SyncDiscardedLocal, theirs)
	}
	if heads := bundleHeads(t, first, result.Bundle); heads["refs/workbook/tasks/"+task.ID] != local {
		t.Fatalf("bundle heads = %v, want the local checkpoint tip %s", heads, local)
	}
	if got := taskRefs(t, first)[task.ID]; got != theirs {
		t.Fatalf("local ref = %s, want origin's tip %s", got, theirs)
	}
	shown := showTask(t, first, task.ID)
	if shown.Title != "Their title" || shown.Deleted {
		t.Fatalf("shown task = %+v, want origin's title and not deleted", shown)
	}
	stdout := compactRun(t, first, "sync", "--json")
	var synced gitstore.SyncRunResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &synced); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	for _, phase := range []gitstore.SyncResult{synced.Fetch, synced.Push} {
		for _, item := range phase.Tasks {
			if item.TaskID == task.ID && item.Status != gitstore.SyncUnchanged && item.Status != gitstore.SyncUpToDate {
				t.Fatalf("next sync reported %+v, want it quiet", item)
			}
		}
	}
}

// Every mutation of a task kept on the old history is refused with the
// fallback's own words, after a fetch, with --no-sync, and beside a watcher
// that did the fetching.
func TestMutationRefusedWhileCompactedOnOrigin(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fetch", "no-sync", "watcher"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			_, second, task := compactedOnOriginTask(t)
			args := []string{"update", task.ID, "--title", "Refused", "--json"}
			switch mode {
			case "no-sync":
				compactRun(t, second, "sync")
				args = append(args, "--no-sync")
			case "watcher":
				startCLIWatcher(t, second, "1h")
			}
			before := taskRefs(t, second)[task.ID]
			code, _, stderr := run(t, second, args...)
			if code != core.ExitCode(core.Errorf(core.CategoryConflict, "probe")) {
				t.Fatalf("update code = %d, want the conflict code; stderr = %q", code, stderr)
			}
			assertJSONError(t, stderr, core.CategoryConflict, compactedOnOriginDetailFor(task.ID))
			if after := taskRefs(t, second)[task.ID]; after != before {
				t.Fatalf("local ref = %s, want it unchanged at %s", after, before)
			}
		})
	}
}

// A restore of the task this clone compacted and deleted, while origin edited
// the old history, is refused with that state's words, with and without
// --no-sync.
func TestMutationRefusedWhileOriginChangedADeletedCompaction(t *testing.T) {
	t.Parallel()
	for _, noSync := range []bool{false, true} {
		first, _, task, local := deletedAfterCompaction(t)
		args := []string{"restore", task.ID, "--json"}
		if noSync {
			run(t, first, "sync")
			args = append(args, "--no-sync")
		}
		code, _, stderr := run(t, first, args...)
		if code != core.ExitCode(core.Errorf(core.CategoryConflict, "probe")) {
			t.Fatalf("restore (no-sync %v) code = %d, want the conflict code; stderr = %q", noSync, code, stderr)
		}
		assertJSONError(t, stderr, core.CategoryConflict, cannotCombineDetailFor(task.Title, task.ID))
		if after := taskRefs(t, first)[task.ID]; after != local {
			t.Fatalf("local ref = %s, want it unchanged at %s", after, local)
		}
	}
}

// next never offers a stuck task, and next --claim claims the next one
// instead.
func TestNextClaimSkipsAStuckTask(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	stuck := cliCreateTaskAtPriority(t, first, "Stuck but urgent", "high")
	compactRun(t, first, "update", stuck.ID, "--status", "done", "--no-sync")
	other := cliCreateTask(t, first, "Ordinary work")
	compactRun(t, first, "update", other.ID, "--status", "ready", "--no-sync")
	compactRun(t, first, "push")
	compactRun(t, second, "sync")
	compactRun(t, second, "update", stuck.ID, "--status", "ready", "--description", "Our text", "--no-sync")
	compactRun(t, first, "update", stuck.ID, "--description", "Their text")
	compactRun(t, first, "compact", "--no-backup")

	stdout := compactRun(t, second, "next", "--json")
	if task := decodeMutationTask(t, stdout, "next"); task.ID != other.ID {
		t.Fatalf("next = %s, want %s rather than the stuck %s", task.ID, other.ID, stuck.ID)
	}
	stdout = compactRun(t, second, "next", "--claim", "--json")
	if task := decodeMutationTask(t, stdout, "next"); task.ID != other.ID {
		t.Fatalf("next --claim = %s, want %s rather than the stuck %s", task.ID, other.ID, stuck.ID)
	}
	if item := syncFetchOutcome(t, second, stuck.ID); item.Status != gitstore.SyncCompactedOnOrigin {
		t.Fatalf("stuck task fetch outcome = %+v, want it still %s", item, gitstore.SyncCompactedOnOrigin)
	}
}

// compactedDate renders t the way the compacted line does, written out here
// independently of writeChangeLog.
func compactedDate(t time.Time, now time.Time) string {
	t = t.Local()
	if t.Year() != now.Year() {
		return t.Format("Jan 2, 2006")
	}
	return t.Format("Jan 2")
}

// show --history on a compacted task lists the changes since the checkpoint,
// not the checkpoint itself, then the line the checkpoint's summary gives.
func TestShowHistoryShowsCompactedLine(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Five commits")
	compactRun(t, repository, "update", task.ID, "--title", "Five commits, revised", "--no-sync")
	compactRun(t, repository, "update", task.ID, "--description", "Some words", "--no-sync")
	compactRun(t, repository, "update", task.ID, "--priority", "high", "--no-sync")
	compactRun(t, repository, "update", task.ID, "--status", "done", "--no-sync")
	compactRun(t, repository, "compact", "--no-sync", "--no-backup")
	compactRun(t, repository, "update", task.ID, "--title", "After the checkpoint", "--no-sync")

	stdout := compactRun(t, repository, "show", task.ID, "--history", "--json")
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	history := detail.History
	if history == nil || history.Compacted == nil {
		t.Fatalf("history = %+v, want a compacted summary", history)
	}
	if history.Compacted.Changes != 5 || history.Total != 1 || len(history.Changes) != 1 ||
		history.Changes[0].Summary != "changed title" {
		t.Fatalf("history = %+v (compacted %+v), want the one edit and 5 compacted changes", history, *history.Compacted)
	}
	var raw struct {
		Data struct {
			History struct {
				Compacted map[string]json.RawMessage `json:"compacted"`
			} `json:"history"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("decode raw show: %v", err)
	}
	for _, name := range []string{"changes", "from", "to", "on"} {
		if _, found := raw.Data.History.Compacted[name]; !found {
			t.Fatalf("compacted = %v, want a %q member", raw.Data.History.Compacted, name)
		}
	}

	now := time.Now()
	compacted := history.Compacted
	want := "5 earlier changes from " + compactedDate(compacted.From, now) + " to " + compactedDate(compacted.To, now) +
		" were compacted on " + compactedDate(compacted.On, now) + "."
	text := compactRun(t, repository, "show", task.ID, "--history")
	lines := strings.Split(text, "\n")
	found := false
	for _, line := range lines {
		found = found || line == want
	}
	if !found || !strings.Contains(text, "Showing all 1 change(s).") || strings.Count(text, "\tchanged title\n") != 1 {
		t.Fatalf("show --history =\n%s\nwant one change and the line %q", text, want)
	}
}

// A date outside the current year carries its year.
func TestChangeLogCompactedLineAddsTheYear(t *testing.T) {
	t.Parallel()
	now := time.Now()
	from := time.Date(now.Year()-2, time.March, 4, 12, 0, 0, 0, time.Local)
	to := time.Date(now.Year()-1, time.July, 3, 12, 0, 0, 0, time.Local)
	var output bytes.Buffer
	writeChangeLog(&output, core.ChangeLog{
		Changes:   []core.Change{},
		Compacted: &core.CompactedSummary{Changes: 12, From: from, To: to, On: now},
	})
	want := "12 earlier changes from Mar 4, " + from.Format("2006") + " to Jul 3, " + to.Format("2006") +
		" were compacted on " + now.Format("Jan 2") + ".\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("change log =\n%s\nwant %q", output.String(), want)
	}
}
