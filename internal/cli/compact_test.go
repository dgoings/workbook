package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/testenv"
)

const compactUpgradeLine = "Every clone of this project must run workbook v0.7.0 or later before compacted tasks are pushed.\n"

const compactGCLine = "Run \"git gc\" to reclaim the space locally; origin cleans up on its own schedule.\n"

// compactFixture is a project holding one task of each kind compaction cares
// about: a Done task with a file attachment, a Deleted task, and a task still
// in the backlog that is never compacted.
type compactFixture struct {
	repository string
	done       core.Task
	deleted    core.Task
	open       core.Task
	attachment []byte
}

func newCompactFixture(t *testing.T) compactFixture {
	t.Helper()
	return newCompactFixtureIn(t, initializedRepository(t))
}

func newCompactFixtureIn(t *testing.T, repository string) compactFixture {
	t.Helper()
	f := compactFixture{repository: repository, attachment: []byte("# Release notes\n")}
	f.done = cliCreateTask(t, repository, "Finished work")
	path := writeAttachmentFile(t, "notes.md", f.attachment)
	compactRun(t, repository, "update", f.done.ID, "--attach-file", path, "--no-sync")
	compactRun(t, repository, "update", f.done.ID, "--status", "done", "--no-sync")
	f.deleted = cliCreateTask(t, repository, "Abandoned work")
	compactRun(t, repository, "delete", f.deleted.ID, "--no-sync")
	f.open = cliCreateTask(t, repository, "Open work")
	return f
}

// compactRun runs a command that must succeed and returns its standard output.
func compactRun(t *testing.T, repository string, args ...string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 {
		t.Fatalf("%s code = %d, want 0; stderr = %q", strings.Join(args, " "), code, stderr)
	}
	return stdout
}

// compactJSON runs compact with --json, which must succeed, and decodes its
// summary.
func compactJSON(t *testing.T, repository string, args ...string) (compactSummary, resultDocument) {
	t.Helper()
	stdout := compactRun(t, repository, append(append([]string{"compact"}, args...), "--json")...)
	document := assertJSONResult(t, stdout, "compact")
	var summary compactSummary
	if err := json.Unmarshal(document.Data, &summary); err != nil {
		t.Fatalf("decode compact summary: %v; output = %q", err, stdout)
	}
	return summary, document
}

// taskRefs maps every task ref to the commit it names.
func taskRefs(t *testing.T, repository string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	for _, line := range strings.Split(gitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/workbook/tasks/"), "\n") {
		if name, objectID, found := strings.Cut(line, " "); found {
			refs[strings.TrimPrefix(name, "refs/workbook/tasks/")] = objectID
		}
	}
	return refs
}

// archiveDirectory is where a run writes its bundle by default. Git reports
// its directory with symbolic links resolved, as on macOS's /var, so the
// expected path is resolved the same way.
func archiveDirectory(repository string) string {
	if resolved, err := filepath.EvalSymlinks(repository); err == nil {
		repository = resolved
	}
	return filepath.Join(repository, ".git", "workbook-archive")
}

func assertNoArchiveDirectory(t *testing.T, repository string) {
	t.Helper()
	if _, err := os.Stat(archiveDirectory(repository)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want no archive directory", archiveDirectory(repository), err)
	}
}

func compactedTaskIDs(summary compactSummary) []string {
	ids := make([]string, 0, len(summary.Tasks))
	for _, task := range summary.Tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

// assertParentless fails unless the task's ref names a commit with no parent,
// which is what a checkpoint is.
func assertParentless(t *testing.T, repository, taskID string) {
	t.Helper()
	fields := strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", "refs/workbook/tasks/"+taskID))
	if len(fields) != 1 {
		t.Fatalf("%s head topology = %q, want a parentless checkpoint", taskID, fields)
	}
}

// assertTotalsAddUp checks that the totals are the sum of the listed tasks.
func assertTotalsAddUp(t *testing.T, summary compactSummary) {
	t.Helper()
	var want compactTotals
	for _, task := range summary.Tasks {
		switch task.Kind {
		case compaction.KindDone:
			want.Done++
		case compaction.KindDeleted:
			want.Deleted++
		}
		want.Commits += task.Commits
		want.AttachmentsDropped += task.Attachments.Dropped
		want.AttachmentsKept += task.Attachments.Kept
		want.DroppedBytes += task.Attachments.DroppedBytes
		want.KeptBytes += task.Attachments.KeptBytes
		want.EstimatedSavedBytes += task.EstimatedSavedBytes
	}
	if summary.Totals != want {
		t.Fatalf("totals = %+v, want the sum of the tasks %+v", summary.Totals, want)
	}
}

func TestCompactDryRunReportsAndChangesNothing(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)

	summary, document := compactJSON(t, f.repository, "--dry-run", "--no-sync")
	if !summary.DryRun || summary.RunID == "" || summary.Bundle != "" {
		t.Fatalf("summary = %+v, want a dry run with a run ID and no bundle", summary)
	}
	if got, want := compactedTaskIDs(summary), sortedIDs(f.done.ID, f.deleted.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks = %v, want %v", got, want)
	}
	assertTotalsAddUp(t, summary)
	if summary.Totals.Done != 1 || summary.Totals.Deleted != 1 || summary.Totals.AttachmentsDropped != 1 ||
		summary.Totals.DroppedBytes != int64(len(f.attachment)) || summary.Totals.EstimatedSavedBytes <= 0 {
		t.Fatalf("totals = %+v, want one done, one deleted, the %d-byte attachment dropped and a positive estimate",
			summary.Totals, len(f.attachment))
	}
	for _, task := range summary.Tasks {
		if task.Commits < 2 || task.Title == "" {
			t.Fatalf("task = %+v, want its title and every commit it replaces", task)
		}
	}
	if len(summary.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", summary.Skipped)
	}
	if document.Warnings != nil {
		t.Fatalf("warnings = %+v, want none", document.Warnings)
	}

	stdout := compactRun(t, f.repository, "compact", "--dry-run", "--no-sync")
	for _, want := range []string{
		compactUpgradeLine,
		"Estimated space saved: ",
		" (applies once Git cleans up unreachable objects)\n",
		"  Finished work  done  ",
		"  Abandoned work  deleted  ",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run output = %q, want it to contain %q", stdout, want)
		}
	}
	for _, unwanted := range []string{"Backup:", compactGCLine} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("dry-run output = %q, want no %q", stdout, unwanted)
		}
	}

	if after := taskRefs(t, f.repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task refs after a dry run = %v, want unchanged %v", after, before)
	}
	assertNoArchiveDirectory(t, f.repository)
}

func TestCompactDryRunWithNothingEligibleWritesNothing(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Still open")
	before := taskRefs(t, repository)

	for _, args := range [][]string{{"--dry-run", "--no-sync"}, {"--no-sync"}} {
		summary, _ := compactJSON(t, repository, args...)
		if summary.Tasks == nil || len(summary.Tasks) != 0 || summary.Skipped == nil || len(summary.Skipped) != 0 {
			t.Fatalf("compact %v summary = %+v, want empty task and skip lists", args, summary)
		}
		if summary.Totals != (compactTotals{}) || summary.Bundle != "" {
			t.Fatalf("compact %v summary = %+v, want zero totals and no bundle", args, summary)
		}
	}
	if after := taskRefs(t, repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task refs = %v, want unchanged %v", after, before)
	}
	assertNoArchiveDirectory(t, repository)
}

func TestCompactDefaultCompactsDoneAndDeleted(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)

	summary, _ := compactJSON(t, f.repository, "--no-sync")
	if summary.DryRun {
		t.Fatal("summary reports a dry run")
	}
	if got, want := compactedTaskIDs(summary), sortedIDs(f.done.ID, f.deleted.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks = %v, want %v", got, want)
	}
	assertTotalsAddUp(t, summary)
	if want := filepath.Join(archiveDirectory(f.repository), summary.RunID+".bundle"); summary.Bundle != want {
		t.Fatalf("bundle = %q, want the default path %q", summary.Bundle, want)
	}
	if _, err := os.Stat(summary.Bundle); err != nil {
		t.Fatalf("Stat(bundle) error = %v", err)
	}

	after := taskRefs(t, f.repository)
	for _, id := range []string{f.done.ID, f.deleted.ID} {
		if after[id] == before[id] {
			t.Fatalf("%s did not move", id)
		}
		assertParentless(t, f.repository, id)
	}
	if after[f.open.ID] != before[f.open.ID] {
		t.Fatalf("open task moved from %s to %s", before[f.open.ID], after[f.open.ID])
	}

	shown := showTask(t, f.repository, f.done.ID)
	if shown.Title != "Finished work" || shown.Status != core.StatusDone || len(shown.Attachments) != 1 {
		t.Fatalf("compacted task = %+v, want its title, status and attachment row", shown)
	}
	attachment := shown.Attachments[0]
	if attachment.Dropped == nil || time.Since(attachment.Dropped.At).Abs() > time.Hour {
		t.Fatalf("attachment = %+v, want it dropped by this run", attachment)
	}
	day := attachment.Dropped.At.Format("2006-01-02")

	text := compactRun(t, f.repository, "show", f.done.ID)
	if want := "notes.md (removed by compaction on " + day + ")"; !strings.Contains(text, want) {
		t.Fatalf("show output = %q, want %q", text, want)
	}
	code, stdout, stderr := run(t, f.repository, "show", f.done.ID, "--get-attachment", attachment.ID)
	if code != core.ExitCode(core.Errorf(core.CategoryNotFound, "probe")) || stdout != "" {
		t.Fatalf("--get-attachment = code %d, stdout %q; want the not-found code and no bytes", code, stdout)
	}
	if want := `workbook: attachment "notes.md" was removed when the task was compacted on ` + day + "\n"; stderr != want {
		t.Fatalf("--get-attachment stderr = %q, want %q", stderr, want)
	}
	if deleted := compactRun(t, f.repository, "list", "--all", "--json"); !strings.Contains(deleted, f.deleted.ID) {
		t.Fatalf("list --all = %q, want the compacted deleted task still listed", deleted)
	}
}

func TestCompactTextOutputForARealRun(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)

	stdout := compactRun(t, f.repository, "compact", "--no-sync")
	bundles, err := os.ReadDir(archiveDirectory(f.repository))
	if err != nil || len(bundles) != 1 {
		t.Fatalf("archive directory = %v, %v; want one bundle", bundles, err)
	}
	bundle := filepath.Join(archiveDirectory(f.repository), bundles[0].Name())
	short := func(id string) string { return id[:len(f.done.ID)-18] }
	for _, want := range []string{
		short(f.done.ID) + "  Finished work  done  ",
		"1 attachment dropped (16 bytes)\n",
		short(f.deleted.ID) + "  Abandoned work  deleted  ",
		"Compacted 2 tasks (1 done, 1 deleted)",
		"Estimated space saved: ",
		"Backup: " + bundle + "\n",
		compactUpgradeLine,
		compactGCLine,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("compact output = %q, want it to contain %q", stdout, want)
		}
	}
	if !strings.HasSuffix(stdout, compactUpgradeLine+compactGCLine) {
		t.Fatalf("compact output = %q, want it to end with the upgrade line and the gc hint", stdout)
	}
}

// Not parallel: it sets compactNow, which every compact run reads.
func TestCompactDoneOnlyWithDuration(t *testing.T) {
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)
	real := compactNow
	t.Cleanup(func() { compactNow = real })

	compactNow = func() time.Time { return time.Now().Add(20 * 24 * time.Hour) }
	summary, _ := compactJSON(t, f.repository, "--done-only", "30d", "--dry-run", "--no-sync")
	if len(summary.Tasks) != 0 {
		t.Fatalf("20 days on, tasks = %v, want none past a 30d cutoff", compactedTaskIDs(summary))
	}

	compactNow = func() time.Time { return time.Now().Add(40 * 24 * time.Hour) }
	summary, _ = compactJSON(t, f.repository, "--done-only", "--since", "30d", "--dry-run", "--no-sync")
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{f.done.ID}) {
		t.Fatalf("--done-only --since 30d tasks = %v, want [%s]", got, f.done.ID)
	}
	summary, _ = compactJSON(t, f.repository, "--done-only", "30d", "--no-sync")
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{f.done.ID}) {
		t.Fatalf("--done-only 30d tasks = %v, want [%s]", got, f.done.ID)
	}
	after := taskRefs(t, f.repository)
	if after[f.done.ID] == before[f.done.ID] || after[f.deleted.ID] != before[f.deleted.ID] {
		t.Fatalf("refs = %v from %v, want only the done task moved", after, before)
	}
}

func TestCompactDeletedOnly(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)

	summary, _ := compactJSON(t, f.repository, "--deleted-only", "--no-sync")
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{f.deleted.ID}) {
		t.Fatalf("tasks = %v, want [%s]", got, f.deleted.ID)
	}
	if summary.Totals.Done != 0 || summary.Totals.Deleted != 1 {
		t.Fatalf("totals = %+v, want one deleted task", summary.Totals)
	}
	after := taskRefs(t, f.repository)
	if after[f.deleted.ID] == before[f.deleted.ID] || after[f.done.ID] != before[f.done.ID] {
		t.Fatalf("refs = %v from %v, want only the deleted task moved", after, before)
	}
}

func assertCompactRefused(t *testing.T, repository, message string, args ...string) {
	t.Helper()
	before := taskRefs(t, repository)
	code, stdout, stderr := run(t, repository, append(append([]string{"compact"}, args...), "--json")...)
	if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "probe")) || stdout != "" {
		t.Fatalf("compact %v = code %d, stdout %q; want the invocation code", args, code, stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, message)

	code, _, stderr = run(t, repository, append([]string{"compact"}, args...)...)
	if code == 0 {
		t.Fatalf("compact %v succeeded in text mode", args)
	}
	assertHumanError(t, stderr, message)
	if after := taskRefs(t, repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task refs after a refused compact = %v, want unchanged %v", after, before)
	}
	assertNoArchiveDirectory(t, repository)
}

func TestCompactRejectsConflictingCutoffs(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	const message = "give the cutoff once: --since cannot be combined with a --done-only or --deleted-only duration"
	for _, args := range [][]string{
		{"--done-only", "30d", "--since", "7d"},
		{"--done-only", "7d", "--since", "7d"},
		{"--since", "7d", "--deleted-only", "7d"},
		{"--done-only=7d", "--since=7d"},
	} {
		assertCompactRefused(t, f.repository, message, args...)
	}
}

func TestCompactRejectsDoneOnlyWithDeletedOnly(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	const message = "--done-only and --deleted-only cannot be combined"
	assertCompactRefused(t, f.repository, message, "--done-only", "--deleted-only")
	assertCompactRefused(t, f.repository, message, "--deleted-only", "1d", "--done-only", "1d")
}

func TestCompactRejectsNoBackupWithBackup(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	assertCompactRefused(t, f.repository, "--no-backup and --backup cannot be combined",
		"--no-backup", "--backup", t.TempDir())
}

func TestCompactBackupToDirectoryAndFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first := newCompactFixture(t)
	summary, _ := compactJSON(t, first.repository, "--backup", directory, "--no-sync")
	if want := filepath.Join(directory, summary.RunID+".bundle"); summary.Bundle != want {
		t.Fatalf("bundle = %q, want the default name in the directory %q", summary.Bundle, want)
	}
	if _, err := os.Stat(summary.Bundle); err != nil {
		t.Fatalf("Stat(bundle) error = %v", err)
	}
	assertNoArchiveDirectory(t, first.repository)

	file := filepath.Join(t.TempDir(), "before-compaction.bundle")
	second := newCompactFixture(t)
	summary, _ = compactJSON(t, second.repository, "--backup", file, "--no-sync")
	if summary.Bundle != file {
		t.Fatalf("bundle = %q, want the file path as given %q", summary.Bundle, file)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("Stat(bundle) error = %v", err)
	}
	gitOutput(t, second.repository, "bundle", "verify", file)
}

func TestCompactNoBackupWritesNoBundle(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)

	summary, _ := compactJSON(t, f.repository, "--no-backup", "--no-sync")
	if summary.Bundle != "" || len(summary.Tasks) != 2 {
		t.Fatalf("summary = %+v, want two tasks and no bundle", summary)
	}
	if after := taskRefs(t, f.repository); after[f.done.ID] == before[f.done.ID] {
		t.Fatal("done task did not move")
	}
	assertNoArchiveDirectory(t, f.repository)

	other := newCompactFixture(t)
	if stdout := compactRun(t, other.repository, "compact", "--no-backup", "--no-sync"); strings.Contains(stdout, "Backup:") {
		t.Fatalf("compact --no-backup output = %q, want no Backup line", stdout)
	}
}

func TestCompactBundleFailureChangesNothing(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	before := taskRefs(t, f.repository)
	existing := filepath.Join(t.TempDir(), "taken.bundle")
	if err := os.WriteFile(existing, []byte("someone else's file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code, stdout, stderr := run(t, f.repository, "compact", "--backup", existing, "--no-sync", "--json")
	if code == 0 || stdout != "" {
		t.Fatalf("compact onto an existing bundle = code %d, stdout %q; want a failure", code, stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		"backup bundle "+existing+" already exists; Workbook never overwrites one")
	if after := taskRefs(t, f.repository); !reflect.DeepEqual(after, before) {
		t.Fatalf("task refs after a failed bundle = %v, want unchanged %v", after, before)
	}
	if content, err := os.ReadFile(existing); err != nil || string(content) != "someone else's file" {
		t.Fatalf("existing file = %q, %v; want it untouched", content, err)
	}
}

func TestCompactKeepAttachments(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)

	summary, _ := compactJSON(t, f.repository, "--keep-attachments", "--no-sync")
	if summary.Totals.AttachmentsKept != 1 || summary.Totals.KeptBytes != int64(len(f.attachment)) ||
		summary.Totals.AttachmentsDropped != 0 {
		t.Fatalf("totals = %+v, want the attachment kept", summary.Totals)
	}
	assertParentless(t, f.repository, f.done.ID)
	attachment := showTask(t, f.repository, f.done.ID).Attachments[0]
	if attachment.Dropped != nil {
		t.Fatalf("attachment = %+v, want it live", attachment)
	}
	if stdout := compactRun(t, f.repository, "show", f.done.ID, "--get-attachment", attachment.ID); stdout != string(f.attachment) {
		t.Fatalf("--get-attachment = %q, want the original bytes %q", stdout, f.attachment)
	}
}

func TestCompactedTaskCanBeReopenedAndEdited(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)
	compactJSON(t, f.repository, "--no-sync")

	compactRun(t, f.repository, "update", f.done.ID, "--status", "ready", "--no-sync")
	compactRun(t, f.repository, "update", f.done.ID, "--title", "Finished work, reopened", "--no-sync")
	shown := showTask(t, f.repository, f.done.ID)
	if shown.Status != core.Status("ready") || shown.Title != "Finished work, reopened" {
		t.Fatalf("reopened task = %+v, want ready with the new title", shown)
	}

	code, stdout, stderr := run(t, f.repository, "validate", "--full", "--json")
	if code != 0 {
		_, text, _ := run(t, f.repository, "validate", "--full")
		t.Fatalf("validate --full = code %d, stderr %q; report:\n%s", code, stderr, text)
	}
	var report historyvalidation.Result
	if err := json.Unmarshal(assertJSONResult(t, stdout, "validate").Data, &report); err != nil {
		t.Fatalf("decode validate: %v", err)
	}
	if report.Invalid != 0 || report.NewerWriter != 0 {
		t.Fatalf("validate = %+v, want no invalid or newer-writer task", report)
	}
}

func TestCompactDoneOnlyAcceptsFlagWithoutValue(t *testing.T) {
	t.Parallel()
	f := newCompactFixture(t)

	// --json directly after --done-only is a flag, not the duration.
	code, stdout, stderr := run(t, f.repository, "compact", "--done-only", "--json", "--no-sync")
	if code != 0 {
		t.Fatalf("compact --done-only --json = code %d; stderr = %q", code, stderr)
	}
	var summary compactSummary
	if err := json.Unmarshal(assertJSONResult(t, stdout, "compact").Data, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{f.done.ID}) {
		t.Fatalf("tasks = %v, want only the done task %s", got, f.done.ID)
	}

	// A word that is not a duration is not taken as one either.
	code, _, stderr = run(t, f.repository, "compact", "--done-only", "soon", "--no-sync", "--json")
	if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "probe")) {
		t.Fatalf("compact --done-only soon = code %d, want the invocation code; stderr %q", code, stderr)
	}
}

func TestCompactRejectsInvalidDurations(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, test := range []struct {
		args    []string
		message string
	}{
		{[]string{"--since", "soon"}, "compact --since must be a duration such as 30d"},
		{[]string{"--since", "0d"}, "compact --since must be positive"},
		{[]string{"--done-only=soon"}, "compact --done-only must be a duration such as 30d"},
		{[]string{"--deleted-only", "--deleted-only"}, "compact accepts --deleted-only once"},
	} {
		code, _, stderr := run(t, repository, append(append([]string{"compact"}, test.args...), "--json")...)
		if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "probe")) {
			t.Fatalf("compact %v = code %d, want the invocation code; stderr %q", test.args, code, stderr)
		}
		assertJSONError(t, stderr, core.CategoryInvocation, test.message)
	}
}

// With sync on and an origin, compact fetches first and leaves alone any task
// whose tip origin does not hold.
func TestCompactWithSyncFetchesFirstAndSkipsUnpushed(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	pushed := cliCreateTask(t, first, "Pushed and done")
	compactRun(t, first, "update", pushed.ID, "--status", "done", "--no-sync")
	compactRun(t, first, "push")
	local := cliCreateTask(t, first, "Done but never pushed")
	compactRun(t, first, "update", local.ID, "--status", "done", "--no-sync")
	before := taskRefs(t, first)

	stdout := compactRun(t, first, "compact", "--json")
	document := assertJSONResult(t, stdout, "compact")
	var summary compactSummary
	if err := json.Unmarshal(document.Data, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{pushed.ID}) {
		t.Fatalf("tasks = %v, want only the pushed task %s", got, pushed.ID)
	}
	want := []compaction.Skip{{TaskID: local.ID, Title: "Done but never pushed", Reason: compaction.ReasonNotPushed}}
	if !reflect.DeepEqual(summary.Skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", summary.Skipped, want)
	}
	var envelope struct {
		Sync *syncReport `json:"sync"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Sync == nil || !envelope.Sync.Enabled || envelope.Sync.Fetch == nil || envelope.Sync.Status != syncStatusCompleted {
		t.Fatalf("sync = %+v, want an enabled, completed fetch", envelope.Sync)
	}
	after := taskRefs(t, first)
	if after[pushed.ID] == before[pushed.ID] || after[local.ID] != before[local.ID] {
		t.Fatalf("refs = %v from %v, want only the pushed task moved", after, before)
	}

	text := compactRun(t, first, "compact", "--dry-run")
	if want := local.ID[:len(local.ID)-18] + "  Done but never pushed  " + compaction.ReasonNotPushed + "\n"; !strings.Contains(text, want) {
		t.Fatalf("dry-run output = %q, want the skipped task %q", text, want)
	}
}

// A compaction whose refs moved but whose projection cache could not follow is
// reported as done, with the warning that names `workbook rebuild`.
//
// Not parallel: it sets compactExecute, which every compact run reads.
func TestCompactReportsProjectionFailureAsAWarning(t *testing.T) {
	f := newCompactFixture(t)
	real := compactExecute
	t.Cleanup(func() { compactExecute = real })
	compactExecute = func(ctx context.Context, deps compaction.Deps, plan compaction.Plan, options compaction.Options) (compaction.Result, error) {
		result, err := real(ctx, deps, plan, options)
		if err != nil {
			return result, err
		}
		return result, &compaction.ProjectionError{Err: errors.New("cache advance broke")}
	}

	summary, document := compactJSON(t, f.repository, "--no-sync")
	if len(summary.Tasks) != 2 {
		t.Fatalf("tasks = %v, want both compacted", compactedTaskIDs(summary))
	}
	if len(document.Warnings) != 1 || document.Warnings[0].Code != core.WarningProjectionUpdate ||
		!strings.Contains(document.Warnings[0].Message, "workbook rebuild") ||
		!strings.Contains(document.Warnings[0].Message, "cache advance broke") {
		t.Fatalf("warnings = %+v, want one projection-update warning naming workbook rebuild", document.Warnings)
	}

	other := newCompactFixture(t)
	code, stdout, stderr := run(t, other.repository, "compact", "--no-sync")
	if code != 0 || !strings.Contains(stdout, compactGCLine) {
		t.Fatalf("compact = code %d, stdout %q; want the normal summary", code, stdout)
	}
	if !strings.HasPrefix(stderr, "workbook: warning: ") || !strings.Contains(stderr, "workbook rebuild") {
		t.Fatalf("stderr = %q, want the projection warning", stderr)
	}
}

func sortedIDs(ids ...string) []string {
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	return sorted
}

// compactEnvelope decodes the parts of compact's JSON result the publishing
// tests read.
type compactEnvelope struct {
	Sync           *syncReport           `json:"sync"`
	ConfigConflict []core.ConfigConflict `json:"configConflict"`
	Warnings       []core.Warning        `json:"warnings"`
}

func decodeCompactEnvelope(t *testing.T, stdout string) compactEnvelope {
	t.Helper()
	var envelope compactEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode envelope: %v; output = %q", err, stdout)
	}
	if envelope.Sync == nil {
		t.Fatalf("compact result carried no sync member; output = %q", stdout)
	}
	return envelope
}

// publishedDoneTask creates a task in repository, marks it Done, and pushes it,
// so compact with sync on has a task origin already holds.
func publishedDoneTask(t *testing.T, repository, title string) core.Task {
	t.Helper()
	task := cliCreateTask(t, repository, title)
	compactRun(t, repository, "update", task.ID, "--status", "done", "--no-sync")
	compactRun(t, repository, "push")
	return task
}

// With sync on, compact publishes each checkpoint it wrote.
func TestCompactCLIPublishesWhenSyncOn(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Published then compacted")

	stdout := compactRun(t, first, "compact", "--json")
	summary := compactSummary{}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "compact").Data, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{task.ID}) {
		t.Fatalf("tasks = %v, want %s", got, task.ID)
	}
	assertParentless(t, first, task.ID)
	local := taskRefs(t, first)[task.ID]
	if got := remoteTaskRefValue(t, first, task.ID); got != local {
		t.Fatalf("origin holds %s, want the local checkpoint %s", got, local)
	}
	envelope := decodeCompactEnvelope(t, stdout)
	if envelope.Sync.Status != syncStatusCompleted || envelope.Sync.Pushed == nil {
		t.Fatalf("sync = %+v, want a completed run with a push", envelope.Sync)
	}
	want := []gitstore.SyncTaskResult{{TaskID: task.ID, Status: gitstore.SyncCompactionPublished}}
	if !reflect.DeepEqual(envelope.Sync.Pushed.Tasks, want) {
		t.Fatalf("sync.pushed.tasks = %+v, want %+v", envelope.Sync.Pushed.Tasks, want)
	}
	if got := cliGitOutput(t, first, "rev-parse", "refs/workbook/remotes/origin/tasks/"+task.ID); got != local {
		t.Fatalf("tracking ref = %s, want the checkpoint %s", got, local)
	}

	// A run with nothing left to compact publishes nothing.
	stdout = compactRun(t, first, "compact", "--json")
	if envelope := decodeCompactEnvelope(t, stdout); envelope.Sync.Pushed != nil {
		t.Fatalf("sync.pushed = %+v, want none when nothing was compacted", envelope.Sync.Pushed)
	}
}

// An origin that refuses the checkpoint keeps it local, and compact says why,
// with origin's reason, without failing the run. The refusal comes from an
// update hook: Git's receive.denyNonFastForwards guards refs/heads/ only, so it
// never refuses a task ref.
func TestCompactCLIReportsOriginRefusingTheLease(t *testing.T) {
	t.Parallel()
	for _, jsonMode := range []bool{true, false} {
		first, _ := cliSyncRepositories(t)
		task := publishedDoneTask(t, first, "Refused checkpoint")
		published := remoteTaskRefValue(t, first, task.ID)
		hooks := filepath.Join(cliGitOutput(t, first, "config", "remote.origin.url"), "hooks")
		if err := os.MkdirAll(hooks, 0o755); err != nil {
			t.Fatalf("create %s: %v", hooks, err)
		}
		testenv.WriteExecutable(t, filepath.Join(hooks, "update"), []byte(
			"#!/bin/sh\ncase \"$1\" in refs/workbook/tasks/*) exit 1;; esac\nexit 0\n"))

		args := []string{"compact"}
		if jsonMode {
			args = append(args, "--json")
		}
		code, stdout, stderr := run(t, first, args...)
		if code != 0 {
			t.Fatalf("compact json=%v = code %d, stderr %q; want the run to succeed", jsonMode, code, stderr)
		}
		assertParentless(t, first, task.ID)
		if got := remoteTaskRefValue(t, first, task.ID); got != published {
			t.Fatalf("origin holds %s, want it unchanged at %s", got, published)
		}
		const detail = "[remote rejected] (hook declined)"
		if !jsonMode {
			if !strings.Contains(stdout, "Sync:\tfailed") || !strings.Contains(stderr, detail) {
				t.Fatalf("text output = %q, stderr %q; want a failed sync and a warning naming %s", stdout, stderr, detail)
			}
			continue
		}
		envelope := decodeCompactEnvelope(t, stdout)
		if envelope.Sync.Status != syncStatusFailed || envelope.Sync.Pushed == nil {
			t.Fatalf("sync = %+v, want a failed push", envelope.Sync)
		}
		if len(envelope.Sync.Pushed.Tasks) != 1 || envelope.Sync.Pushed.Tasks[0].Status != gitstore.SyncRejected ||
			!strings.Contains(envelope.Sync.Pushed.Tasks[0].Detail, detail) {
			t.Fatalf("sync.pushed.tasks = %+v, want one rejection naming %s", envelope.Sync.Pushed.Tasks, detail)
		}
		if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningAutoSync ||
			!strings.Contains(envelope.Warnings[0].Message, detail) {
			t.Fatalf("warnings = %+v, want one auto-sync warning naming %s", envelope.Warnings, detail)
		}
	}
}

// When the fetch before the run fails, the run still compacts from this clone's
// last view of origin, and publishing is skipped. The warning says the
// checkpoints stayed local; a dry run, which wrote none, does not.
func TestCompactCLIWarnsCheckpointsStayLocalWhenTheFetchFails(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Compacted while offline")
	compactRun(t, first, "fetch")
	cliGit(t, first, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	const stayLocal = "; the compacted tasks stay local (unpublished) until the next successful sync"

	stdout := compactRun(t, first, "compact", "--dry-run", "--json")
	document := assertJSONResult(t, stdout, "compact")
	if len(document.Warnings) != 1 || strings.Contains(document.Warnings[0].Message, stayLocal) {
		t.Fatalf("dry-run warnings = %+v, want the fetch warning without %q", document.Warnings, stayLocal)
	}

	stdout = compactRun(t, first, "compact", "--json")
	summary := compactSummary{}
	document = assertJSONResult(t, stdout, "compact")
	if err := json.Unmarshal(document.Data, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if got := compactedTaskIDs(summary); !reflect.DeepEqual(got, []string{task.ID}) {
		t.Fatalf("tasks = %v, want %s compacted from the last view of origin", got, task.ID)
	}
	envelope := decodeCompactEnvelope(t, stdout)
	if envelope.Sync.Status != syncStatusFailed || envelope.Sync.Pushed != nil {
		t.Fatalf("sync = %+v, want a failed fetch and no push", envelope.Sync)
	}
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningAutoSync ||
		!strings.HasPrefix(envelope.Warnings[0].Message, "the run used this clone's last view of origin, because fetch failed: ") ||
		!strings.HasSuffix(envelope.Warnings[0].Message, stayLocal) {
		t.Fatalf("warnings = %+v, want the fetch failure ending %q", envelope.Warnings, stayLocal)
	}
}

// A live watcher is handed the publication, as a mutation hands it a change,
// and its synchronization publishes the checkpoint. The watcher's interval is
// an hour, so origin holds the checkpoint only if the nudge delivered it.
func TestCompactCLIDefersPublicationToALiveWatcher(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Compacted beside a watcher")
	startCLIWatcher(t, first, "1h")

	stdout := compactRun(t, first, "compact", "--json")
	envelope := decodeCompactEnvelope(t, stdout)
	if envelope.Sync.Status != syncStatusDeferred || envelope.Sync.Pushed != nil {
		t.Fatalf("sync = %+v, want the publication handed to the watcher", envelope.Sync)
	}
	assertParentless(t, first, task.ID)
	waitForPublishedTip(t, first, task.ID)
}

// The fetch before the run can meet a configuration disagreement it cannot
// decide. compact reports it as every other synchronizing command does.
func TestCompactCLIReportsConfigConflictsFromItsFetch(t *testing.T) {
	t.Parallel()
	// The fetch that meets the dispute is the one that reports it, so each
	// output mode gets a dispute of its own.
	disputed := func(t *testing.T) string {
		t.Helper()
		first, second := cliSyncRepositories(t)
		publishedDoneTask(t, first, "Compacted during a rename dispute")
		compactRun(t, second, "sync")
		compactRun(t, second, "status", "rename", "in-progress", "active", "--no-sync", "--no-docs")
		compactRun(t, second, "sync")
		compactRun(t, first, "status", "rename", "in-progress", "doing", "--no-sync", "--no-docs")
		return first
	}

	stdout := compactRun(t, disputed(t), "compact", "--dry-run", "--json")
	envelope := decodeCompactEnvelope(t, stdout)
	if len(envelope.ConfigConflict) == 0 {
		t.Fatalf("configConflict = %+v, want the rename dispute; output = %q", envelope.ConfigConflict, stdout)
	}
	text := compactRun(t, disputed(t), "compact", "--dry-run")
	if !strings.Contains(text, "Config conflict:\t") {
		t.Fatalf("text output = %q, want the configuration conflict", text)
	}
}

// A clone whose task a teammate compacted takes the checkpoint on `workbook
// sync`, replaying its own unpushed edit, and reads the result back through the
// projection with no rebuild, although the projection last saw the old
// history.
func TestSyncAdoptsATeammatesCompactionIntoTheProjection(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	task := publishedDoneTask(t, first, "Compacted by a teammate")
	compactRun(t, second, "sync")
	compactRun(t, second, "update", task.ID, "--description", "Written offline", "--no-sync")
	if shown := showTask(t, second, task.ID); shown.Description != "Written offline" {
		t.Fatalf("description before the sync = %q, want the offline edit", shown.Description)
	}
	compactRun(t, first, "compact", "--no-backup")
	checkpoint := remoteTaskRefValue(t, first, task.ID)

	stdout := compactRun(t, second, "sync", "--json")
	var result gitstore.SyncRunResult
	if err := json.Unmarshal(assertJSONResult(t, stdout, "sync").Data, &result); err != nil {
		t.Fatalf("decode sync: %v; output = %q", err, stdout)
	}
	var fetched *gitstore.SyncTaskResult
	for index := range result.Fetch.Tasks {
		if result.Fetch.Tasks[index].TaskID == task.ID {
			fetched = &result.Fetch.Tasks[index]
		}
	}
	if fetched == nil || fetched.Status != gitstore.SyncAdoptedReplayed {
		t.Fatalf("fetch tasks = %+v, want %s %s", result.Fetch.Tasks, task.ID, gitstore.SyncAdoptedReplayed)
	}
	if parent := cliGitOutput(t, second, "rev-parse", "refs/workbook/tasks/"+task.ID+"^"); parent != checkpoint {
		t.Fatalf("replayed edit's parent = %s, want the teammate's checkpoint %s", parent, checkpoint)
	}

	shown := showTask(t, second, task.ID)
	if shown.Title != "Compacted by a teammate" || shown.Status != core.StatusDone || shown.Description != "Written offline" {
		t.Fatalf("shown task = %+v, want the checkpoint's title and status with the offline description", shown)
	}
}
