package compaction

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// fixture is a real initialized project with the service and projection a
// compaction run is given, writing at a clock the test sets.
type fixture struct {
	t          *testing.T
	repository *gitstore.Repository
	config     core.ProjectConfig
	store      *projection.Store
	service    core.Service
	now        time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	repository, err := gitstore.Open(ctx, testrepo.New(t))
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, _, err := repository.Init(ctx, "WB", core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("projection.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &fixture{t: t, repository: repository, config: config, store: store, now: time.Now().UTC().Add(-90 * 24 * time.Hour)}
	f.service = core.Service{
		Config:     config,
		Vocabulary: finishedVocabulary(t),
		Reader:     store,
		Writer:     repository,
		Blobs:      repository,
		BlobReads:  repository,
		Projection: store,
		History:    store,
		IDs:        core.CryptoULIDSource{},
		Now:        func() time.Time { return f.now },
		Actor:      "tester@example.test",
	}
	return f
}

func (f *fixture) deps() Deps {
	return Deps{Repository: f.repository, Config: f.config, Service: f.service, Projection: f.store}
}

// advance moves the fixture's clock, which every later write records.
func (f *fixture) advance(by time.Duration) { f.now = f.now.Add(by) }

func (f *fixture) create(title string) string {
	f.t.Helper()
	created, err := f.service.CreateMutation(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		f.t.Fatalf("CreateMutation(%q) error = %v", title, err)
	}
	return created.Task.ID
}

func (f *fixture) status(taskID string, status core.Status) {
	f.t.Helper()
	if _, err := f.service.UpdateMutation(context.Background(), taskID, core.UpdateInput{Status: &status}); err != nil {
		f.t.Fatalf("UpdateMutation(%s, %s) error = %v", taskID, status, err)
	}
}

func (f *fixture) comment(taskID, body string) {
	f.t.Helper()
	if _, err := f.service.CommentAddMutation(context.Background(), taskID, core.CommentAddInput{Body: body}); err != nil {
		f.t.Fatalf("CommentAddMutation(%s) error = %v", taskID, err)
	}
}

func (f *fixture) attach(taskID, name, content string) {
	f.t.Helper()
	if _, err := f.service.AttachmentAddMutation(context.Background(), taskID, core.AttachmentAddInput{
		Kind: core.AttachmentFile, Name: name, Content: []byte(content), Media: "text/plain",
	}); err != nil {
		f.t.Fatalf("AttachmentAddMutation(%s) error = %v", taskID, err)
	}
}

func (f *fixture) delete(taskID string) {
	f.t.Helper()
	if _, err := f.service.DeleteMutation(context.Background(), taskID, core.DeleteInput{}); err != nil {
		f.t.Fatalf("DeleteMutation(%s) error = %v", taskID, err)
	}
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	output, err := f.repository.Git(context.Background(), nil, args...)
	if err != nil {
		f.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func (f *fixture) tip(taskID string) core.Snapshot {
	f.t.Helper()
	snapshot, err := f.repository.Get(context.Background(), f.config, taskID)
	if err != nil {
		f.t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	return snapshot
}

func (f *fixture) prepare(options Options) Plan {
	f.t.Helper()
	if options.Now.IsZero() {
		options.Now = f.now
	}
	plan, err := Prepare(context.Background(), f.deps(), options)
	if err != nil {
		f.t.Fatalf("Prepare() error = %v", err)
	}
	return plan
}

func everything() Criteria { return Criteria{Done: true, Deleted: true} }

func candidateIDs(plan Plan) []string {
	ids := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		ids = append(ids, candidate.TaskID)
	}
	sort.Strings(ids)
	return ids
}

func sortedIDs(ids ...string) []string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return sorted
}

func requireIDs(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func candidateFor(t *testing.T, plan Plan, taskID string) Candidate {
	t.Helper()
	for _, candidate := range plan.Candidates {
		if candidate.TaskID == taskID {
			return candidate
		}
	}
	t.Fatalf("plan has no candidate %s; candidates %v, skipped %v", taskID, candidateIDs(plan), plan.Skipped)
	return Candidate{}
}

func skipFor(t *testing.T, skipped []Skip, taskID string) Skip {
	t.Helper()
	for _, skip := range skipped {
		if skip.TaskID == taskID {
			return skip
		}
	}
	t.Fatalf("no skip for %s in %v", taskID, skipped)
	return Skip{}
}

// compactNow runs a whole compaction of the given criteria with no backup.
func (f *fixture) compactNow(criteria Criteria) Result {
	f.t.Helper()
	options := Options{Criteria: criteria, Backup: "-", Now: f.now}
	plan := f.prepare(options)
	result, err := Execute(context.Background(), f.deps(), plan, options)
	if err != nil {
		f.t.Fatalf("Execute() error = %v", err)
	}
	return result
}

func TestPrepareSelectsDoneAndDeletedOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	backlog := f.create("Still to do")
	done := f.create("Finished")
	f.status(done, core.StatusDone)
	shipped := f.create("Shipped it")
	f.status(shipped, "shipped")
	deleted := f.create("Thrown away")
	f.delete(deleted)

	plan := f.prepare(Options{Criteria: everything()})
	requireIDs(t, "default candidates", candidateIDs(plan), sortedIDs(done, shipped, deleted))
	if len(plan.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none", plan.Skipped)
	}
	if plan.RunID == "" {
		t.Fatalf("plan has no run ID")
	}
	for _, id := range []string{done, shipped} {
		if kind := candidateFor(t, plan, id).Kind; kind != KindDone {
			t.Fatalf("%s kind = %q, want %q", id, kind, KindDone)
		}
	}
	gone := candidateFor(t, plan, deleted)
	if gone.Kind != KindDeleted || gone.Title != "Thrown away" || gone.Commits != 2 {
		t.Fatalf("deleted candidate = %+v, want kind deleted, its title and 2 commits", gone)
	}
	if gone.Tip.Head != f.tip(deleted).Head {
		t.Fatalf("deleted candidate tip = %s, want the task's head %s", gone.Tip.Head, f.tip(deleted).Head)
	}
	if !gone.Since.Equal(f.now) {
		t.Fatalf("deleted candidate since = %v, want the delete's wall time %v", gone.Since, f.now)
	}
	var total int64
	for _, candidate := range plan.Candidates {
		if candidate.EstimatedSavedBytes <= 0 {
			t.Fatalf("candidate %s estimated saving = %d, want a positive estimate", candidate.TaskID, candidate.EstimatedSavedBytes)
		}
		total += candidate.EstimatedSavedBytes
	}
	if plan.EstimatedSavedBytes != total {
		t.Fatalf("plan estimate = %d, want the candidates' sum %d", plan.EstimatedSavedBytes, total)
	}
	for _, id := range candidateIDs(plan) {
		if id == backlog {
			t.Fatalf("backlog task %s was selected", backlog)
		}
	}

	requireIDs(t, "done-only candidates",
		candidateIDs(f.prepare(Options{Criteria: Criteria{Done: true}})), sortedIDs(done, shipped))
	requireIDs(t, "deleted-only candidates",
		candidateIDs(f.prepare(Options{Criteria: Criteria{Deleted: true}})), sortedIDs(deleted))
}

// A Deleted task counts as Deleted whatever its status says, so --done-only
// leaves a deleted Done task alone.
func TestPrepareCountsDeletedDoneTaskAsDeleted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	task := f.create("Done, then deleted")
	f.status(task, core.StatusDone)
	f.delete(task)

	if got := candidateIDs(f.prepare(Options{Criteria: Criteria{Done: true}})); len(got) != 0 {
		t.Fatalf("done-only candidates = %v, want none", got)
	}
	plan := f.prepare(Options{Criteria: everything()})
	if kind := candidateFor(t, plan, task).Kind; kind != KindDeleted {
		t.Fatalf("kind = %q, want %q", kind, KindDeleted)
	}
}

func TestPrepareCutoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	day := 24 * time.Hour
	old := f.create("Done long ago")
	f.status(old, core.StatusDone)
	f.advance(10 * day)
	recent := f.create("Done recently")
	f.status(recent, core.StatusDone)
	deleted := f.create("Deleted recently")
	f.delete(deleted)
	f.advance(9 * day)
	// A comment on a finished task does not restart its clock.
	f.comment(old, "A late note")
	f.advance(day)

	requireIDs(t, "15-day cutoff", candidateIDs(f.prepare(Options{Criteria: Criteria{Done: true, Deleted: true, Cutoff: 15 * day}})),
		sortedIDs(old))
	requireIDs(t, "5-day cutoff", candidateIDs(f.prepare(Options{Criteria: Criteria{Done: true, Deleted: true, Cutoff: 5 * day}})),
		sortedIDs(old, recent, deleted))
	requireIDs(t, "deleted 15-day cutoff", candidateIDs(f.prepare(Options{Criteria: Criteria{Deleted: true, Cutoff: 15 * day}})),
		nil)
	requireIDs(t, "deleted 5-day cutoff", candidateIDs(f.prepare(Options{Criteria: Criteria{Deleted: true, Cutoff: 5 * day}})),
		sortedIDs(deleted))
	// The cutoff is strict: exactly the cutoff ago is not more than it.
	requireIDs(t, "exact cutoff", candidateIDs(f.prepare(Options{Criteria: Criteria{Done: true, Cutoff: 10 * day}})),
		sortedIDs(old))
}

func TestPrepareSkipsUnpushedWhenRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	f.git("init", "--bare", "--quiet", bare)
	f.git("remote", "add", "origin", bare)

	pushed := f.create("Pushed")
	f.status(pushed, core.StatusDone)
	edited := f.create("Edited after the push")
	f.status(edited, core.StatusDone)
	if _, err := f.repository.Sync(ctx, f.config); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	// A push does not move the tracking refs; the fetch every run starts with does.
	if _, err := f.repository.Fetch(ctx, f.config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	f.comment(edited, "Not on origin yet")
	local := f.create("Never pushed")
	f.status(local, core.StatusDone)

	plan := f.prepare(Options{Criteria: everything(), RequirePushed: true})
	requireIDs(t, "candidates", candidateIDs(plan), sortedIDs(pushed))
	for _, id := range []string{edited, local} {
		if reason := skipFor(t, plan.Skipped, id).Reason; reason != ReasonNotPushed {
			t.Fatalf("skip reason for %s = %q, want %q", id, reason, ReasonNotPushed)
		}
	}

	requireIDs(t, "candidates without the push requirement",
		candidateIDs(f.prepare(Options{Criteria: everything()})), sortedIDs(pushed, edited, local))
}

func TestPrepareSkipsAlreadyCompacted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	task := f.create("Compact once")
	f.status(task, core.StatusDone)
	if got := f.compactNow(everything()); len(got.Compacted) != 1 {
		t.Fatalf("first run compacted %d tasks, want 1", len(got.Compacted))
	}

	plan := f.prepare(Options{Criteria: everything()})
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %v, want none", candidateIDs(plan))
	}
	if reason := skipFor(t, plan.Skipped, task).Reason; reason != ReasonAlreadyCompacted {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonAlreadyCompacted)
	}
}

// A compacted task that picked up new changes is eligible again, and its new
// checkpoint replaces the old one together with those changes.
func TestPrepareIncludesRecompactionCandidate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	task := f.create("Reopened")
	f.status(task, core.StatusDone)
	f.compactNow(everything())
	first := f.tip(task)

	f.advance(time.Hour)
	f.status(task, core.StatusReady)
	if got := candidateIDs(f.prepare(Options{Criteria: everything()})); len(got) != 0 {
		t.Fatalf("candidates while reopened = %v, want none", got)
	}
	f.advance(time.Hour)
	f.status(task, core.StatusDone)

	plan := f.prepare(Options{Criteria: everything()})
	candidate := candidateFor(t, plan, task)
	if !candidate.Since.Equal(f.now) {
		t.Fatalf("since = %v, want the second done %v", candidate.Since, f.now)
	}
	result, err := Execute(context.Background(), f.deps(), plan, Options{Criteria: everything(), Backup: "-", Now: f.now})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Compacted) != 1 {
		t.Fatalf("second run compacted %d tasks, want 1", len(result.Compacted))
	}
	second := f.tip(task)
	summary, ok := second.Operation.Checkpoint()
	if !ok {
		t.Fatalf("task tip after recompaction is not a checkpoint")
	}
	if summary.PreviousGeneration != first.State.History.Generation {
		t.Fatalf("previous generation = %s, want the first checkpoint's %s",
			summary.PreviousGeneration, first.State.History.Generation)
	}
	if summary.BecameDoneAt == nil || !summary.BecameDoneAt.Equal(f.now) {
		t.Fatalf("becameDoneAt = %v, want %v", summary.BecameDoneAt, f.now)
	}
}

func TestPrepareSkipsNewerWriter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	task := f.create("From the future")
	f.status(task, core.StatusDone)
	writeNewerWriterTip(t, f, task)

	plan := f.prepare(Options{Criteria: everything()})
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %v, want none", candidateIDs(plan))
	}
	if reason := skipFor(t, plan.Skipped, task).Reason; reason != ReasonNewerWriter {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonNewerWriter)
	}
}

func TestExecuteCompactsAndBundles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	done := f.create("Done with a file")
	f.attach(done, "notes.txt", "some notes worth keeping")
	f.status(done, core.StatusDone)
	deleted := f.create("Deleted")
	f.delete(deleted)
	open := f.create("Still open")

	options := Options{Criteria: everything(), Flags: []string{"--since", "0d"}, Now: f.now.Add(time.Hour)}
	plan := f.prepare(options)
	tips := map[string]core.Snapshot{done: f.tip(done), deleted: f.tip(deleted)}
	attached := candidateFor(t, plan, done)
	if attached.DroppedAttachments != 1 || attached.DroppedBytes != int64(len("some notes worth keeping")) || attached.KeptAttachments != 0 {
		t.Fatalf("attachment counts = %+v, want one dropped file of its size", attached)
	}

	result, err := Execute(ctx, f.deps(), plan, options)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requireIDs(t, "compacted", compactedIDs(result), sortedIDs(done, deleted))
	if len(result.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none", result.Skipped)
	}

	for taskID, old := range tips {
		current := f.tip(taskID)
		summary, ok := current.Operation.Checkpoint()
		if !ok {
			t.Fatalf("task %s tip is not a checkpoint", taskID)
		}
		if summary.CompactedFrom != old.Head || summary.RunID != plan.RunID {
			t.Fatalf("task %s checkpoint = %+v, want it to replace %s in run %s", taskID, summary, old.Head, plan.RunID)
		}
		if current.State.Task.Title != old.State.Task.Title || current.State.Task.Status != old.State.Task.Status ||
			current.State.Task.Deleted != old.State.Task.Deleted {
			t.Fatalf("task %s changed: %+v, want %+v", taskID, current.State.Task, old.State.Task)
		}
	}
	if summary, _ := f.tip(deleted).Operation.Checkpoint(); summary.BecameDeletedAt == nil {
		t.Fatalf("deleted task checkpoint records no delete time")
	}
	if attachments := f.tip(done).State.Task.Attachments; len(attachments) != 1 || !attachments[0].IsDropped() {
		t.Fatalf("attachments = %+v, want the file dropped", attachments)
	}
	if f.tip(open).Head == "" {
		t.Fatalf("open task vanished")
	}

	if !filepath.IsAbs(result.BundlePath) {
		t.Fatalf("bundle path %q is not absolute", result.BundlePath)
	}
	if want := gitstore.DefaultArchivePath(f.repository.CommonGitDir, plan.RunID); result.BundlePath != want {
		t.Fatalf("bundle path = %s, want the default %s", result.BundlePath, want)
	}
	f.git("bundle", "verify", "--quiet", result.BundlePath)
	heads := f.git("bundle", "list-heads", result.BundlePath)
	for taskID, old := range tips {
		if !strings.Contains(heads, old.Head+" refs/workbook/tasks/"+taskID) {
			t.Fatalf("bundle heads %q lack task %s at %s", heads, taskID, old.Head)
		}
	}

	// Nothing but the bundle holds the old history now, so pruning it is what
	// a later `git gc` does. The projection must already be on the
	// checkpoints, because it could no longer prove the compaction from the
	// heads it saw before.
	f.git("gc", "--quiet", "--prune=now")
	for taskID, old := range tips {
		if _, err := f.repository.Git(ctx, nil, "cat-file", "-e", old.Head); err == nil {
			t.Fatalf("task %s old tip %s survived gc; the fixture proves nothing", taskID, old.Head)
		}
	}
	listed, err := f.store.List(ctx, f.config)
	if err != nil {
		t.Fatalf("projection List() after gc error = %v", err)
	}
	for _, snapshot := range listed {
		old, compacted := tips[snapshot.State.TaskID]
		if !compacted {
			continue
		}
		if snapshot.Head != f.tip(snapshot.State.TaskID).Head {
			t.Fatalf("projected %s head = %s, want its checkpoint", snapshot.State.TaskID, snapshot.Head)
		}
		if snapshot.State.Task.Title != old.State.Task.Title || snapshot.State.Task.Status != old.State.Task.Status {
			t.Fatalf("projected %s = %+v, want title and status unchanged from %+v",
				snapshot.State.TaskID, snapshot.State.Task, old.State.Task)
		}
	}
}

// A relative --backup value names a file relative to where the command ran,
// and the result reports where that is.
func TestExecuteReportsAbsoluteBundlePath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	task := f.create("Done")
	f.status(task, core.StatusDone)
	want := filepath.Join(t.TempDir(), "relative.bundle")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	relative, err := filepath.Rel(workingDirectory, want)
	if err != nil {
		t.Fatalf("Rel() error = %v", err)
	}

	options := Options{Criteria: everything(), Backup: relative, Now: f.now}
	result, err := Execute(context.Background(), f.deps(), f.prepare(options), options)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.BundlePath != want {
		t.Fatalf("bundle path = %q, want the absolute %q", result.BundlePath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("Stat(bundle) error = %v", err)
	}
}

func compactedIDs(result Result) []string {
	ids := make([]string, 0, len(result.Compacted))
	for _, candidate := range result.Compacted {
		ids = append(ids, candidate.TaskID)
	}
	sort.Strings(ids)
	return ids
}

func TestExecuteBundleFailureChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	done := f.create("Done")
	f.status(done, core.StatusDone)
	deleted := f.create("Deleted")
	f.delete(deleted)

	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if probe, err := os.CreateTemp(locked, "probe"); err == nil {
		probe.Close()
		testenv.MissingCapability(t, "a read-only directory is writable here (running as root?)")
	}

	options := Options{Criteria: everything(), Backup: locked, Now: f.now}
	plan := f.prepare(options)
	tips := map[string]string{done: f.tip(done).Head, deleted: f.tip(deleted).Head}
	result, err := Execute(context.Background(), f.deps(), plan, options)
	if err == nil {
		t.Fatalf("Execute() into a read-only directory succeeded")
	}
	if len(result.Compacted) != 0 {
		t.Fatalf("Compacted = %v, want none", compactedIDs(result))
	}
	for taskID, head := range tips {
		if got := f.tip(taskID).Head; got != head {
			t.Fatalf("task %s moved to %s after a failed bundle, want %s", taskID, got, head)
		}
	}
	entries, readErr := os.ReadDir(locked)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("backup directory holds %d entries, want none", len(entries))
	}
}

// A run that stops after its first task leaves that task compacted, the rest
// untouched, and the first task's reflog and parked refs possibly still
// holding its old history. The rerun finishes the rest and lets go of both.
//
// Not parallel: it sets afterTaskHook, which every Execute reads.
func TestExecuteInterruptedThenRerun(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ids := []string{f.create("First"), f.create("Second"), f.create("Third")}
	for _, id := range ids {
		f.status(id, core.StatusDone)
	}
	options := Options{Criteria: everything(), Backup: "-", Now: f.now}
	plan := f.prepare(options)
	tips := make(map[string]string, len(ids))
	for _, id := range ids {
		tips[id] = f.tip(id).Head
	}

	interruption := errors.New("interrupted")
	var interrupted string
	afterTaskHook = func(taskID string) error {
		interrupted = taskID
		// What a crash between the ref move and its cleanup could leave: a
		// reflog naming the old tip, and a parked ref holding it.
		checkpoint := f.tip(taskID).Head
		ref := "refs/workbook/tasks/" + taskID
		f.git("update-ref", "--create-reflog", "-m", "test: back", ref, tips[taskID], checkpoint)
		f.git("update-ref", "--create-reflog", "-m", "test: forward", ref, checkpoint, tips[taskID])
		f.git("update-ref", "refs/workbook/reconciled/"+taskID+"/0", tips[taskID])
		return interruption
	}
	t.Cleanup(func() { afterTaskHook = nil })

	result, err := Execute(ctx, f.deps(), plan, options)
	if !errors.Is(err, interruption) {
		t.Fatalf("Execute() error = %v, want the interruption", err)
	}
	afterTaskHook = nil
	requireIDs(t, "compacted before the interruption", compactedIDs(result), []string{interrupted})
	for _, id := range ids {
		_, isCheckpoint := f.tip(id).Operation.Checkpoint()
		if id == interrupted && !isCheckpoint {
			t.Fatalf("interrupted task %s is not compacted", id)
		}
		if id != interrupted && f.tip(id).Head != tips[id] {
			t.Fatalf("task %s moved before the run reached it", id)
		}
	}
	if reflog := f.git("reflog", "show", "--format=%H", "refs/workbook/tasks/"+interrupted); !strings.Contains(reflog, tips[interrupted]) {
		t.Fatalf("fixture reflog %q does not name the old tip", reflog)
	}

	plan = f.prepare(options)
	if reason := skipFor(t, plan.Skipped, interrupted).Reason; reason != ReasonAlreadyCompacted {
		t.Fatalf("rerun skip reason = %q, want %q", reason, ReasonAlreadyCompacted)
	}
	result, err = Execute(ctx, f.deps(), plan, options)
	if err != nil {
		t.Fatalf("rerun Execute() error = %v", err)
	}
	var rest []string
	for _, id := range ids {
		if id != interrupted {
			rest = append(rest, id)
		}
	}
	requireIDs(t, "compacted by the rerun", compactedIDs(result), sortedIDs(rest...))
	for _, id := range ids {
		if _, ok := f.tip(id).Operation.Checkpoint(); !ok {
			t.Fatalf("task %s is not compacted after the rerun", id)
		}
	}
	if reflog := f.git("reflog", "show", "--format=%H", "refs/workbook/tasks/"+interrupted); strings.Contains(reflog, tips[interrupted]) {
		t.Fatalf("rerun left the reflog naming the old tip:\n%s", reflog)
	}
	if parked := f.git("for-each-ref", "refs/workbook/reconciled/"+interrupted+"/"); parked != "" {
		t.Fatalf("rerun left parked refs:\n%s", parked)
	}
}

// A task whose ref moves after it was selected is skipped and reported, and
// the others are still compacted.
func TestExecuteSkipsTaskChangedDuringRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	moved := f.create("Moves under the run")
	f.status(moved, core.StatusDone)
	steady := f.create("Steady")
	f.status(steady, core.StatusDone)
	options := Options{Criteria: everything(), Backup: "-", Now: f.now}
	plan := f.prepare(options)
	f.comment(moved, "Arrived after selection")

	result, err := Execute(context.Background(), f.deps(), plan, options)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requireIDs(t, "compacted", compactedIDs(result), []string{steady})
	if reason := skipFor(t, result.Skipped, moved).Reason; reason != ReasonChanged {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonChanged)
	}
	if _, ok := f.tip(moved).Operation.Checkpoint(); ok {
		t.Fatalf("task changed during the run was compacted anyway")
	}
}

// writeNewerWriterTip appends a commit to a task ref that only a newer
// Workbook could have written: an operation pack declaring a generation this
// build cannot fold beside a checkpoint carrying a member it cannot decode.
func writeNewerWriterTip(t *testing.T, f *fixture, taskID string) {
	t.Helper()
	ctx := context.Background()
	future := core.SupportedFormatGeneration + 1
	ref := "refs/workbook/tasks/" + taskID
	head := f.git("rev-parse", ref)
	stored := f.git("show", head+":state.json")
	state, err := core.DecodeStateDocument([]byte(stored + "\n"))
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}
	operation := fmt.Sprintf(
		`{"format":"workbook.operation-pack","version":1,"minReader":%d,"projectId":%q,"taskId":%q,`+
			`"historyGeneration":%q,"actor":{"id":"future@example.test"},"logicalClock":%d,`+
			`"wallTime":"2027-01-01T00:00:00Z","operations":[{"id":"01KZYHVT1D070XVGT7J0M99QAH",`+
			`"type":"comment.add","body":"written by a newer workbook"}]}`+"\n",
		future, state.ProjectID, state.TaskID, state.History.Generation, state.LogicalClock+1)
	marked := strings.Replace(stored, `"version":1,`, fmt.Sprintf(`"version":1,"minReader":%d,`, future), 1)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"task":{`, `"comments":[{"body":"written by a newer workbook"}],"task":{`, 1)
	if marked == stored {
		t.Fatal("the checkpoint substitutions matched nothing; the stored document changed shape")
	}
	write := func(input string, args ...string) string {
		output, err := f.repository.Git(ctx, []byte(input), args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	operationBlob := write(operation, "hash-object", "-w", "--stdin")
	stateBlob := write(marked+"\n", "hash-object", "-w", "--stdin")
	tree := write(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob), "mktree")
	commit := write("workbook: comment on "+taskID, "commit-tree", tree, "-p", head)
	f.git("update-ref", ref, commit, head)
}
