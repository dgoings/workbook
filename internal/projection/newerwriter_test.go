package projection

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

const projectionFutureGeneration = core.SupportedFormatGeneration + 1

// writeFutureTaskCommit appends a commit to a task ref that only a newer
// Workbook could have written: an operation pack declaring a generation this
// build cannot fold, carrying an operation type it has never heard of, beside a
// checkpoint carrying a member it cannot decode.
func writeFutureTaskCommit(t *testing.T, repository *gitstore.Repository, taskID string) string {
	t.Helper()
	ref := "refs/workbook/tasks/" + taskID
	head := gitOutput(t, repository, "rev-parse", ref)
	stored := gitOutput(t, repository, "show", head+":state.json")
	state, err := core.DecodeStateDocument([]byte(stored + "\n"))
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}

	operation := fmt.Sprintf(
		`{"format":"workbook.operation-pack","version":1,"minReader":%d,"projectId":%q,"taskId":%q,`+
			`"historyGeneration":%q,"actor":{"id":"future@example.test"},"logicalClock":%d,`+
			`"wallTime":"2027-01-01T00:00:00Z","operations":[{"id":"01KZYHVT1D070XVGT7J0M99QAH",`+
			`"type":"comment.add","body":"written by a newer workbook"}]}`+"\n",
		projectionFutureGeneration, state.ProjectID, state.TaskID, state.History.Generation, state.LogicalClock+1)

	marked := strings.Replace(stored, `"version":1,`,
		fmt.Sprintf(`"version":1,"minReader":%d,`, projectionFutureGeneration), 1)
	marked = strings.Replace(marked,
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock),
		fmt.Sprintf(`"logicalClock":%d,`, state.LogicalClock+1), 1)
	marked = strings.Replace(marked, `"task":{`, `"comments":[{"body":"written by a newer workbook"}],"task":{`, 1)
	if marked == stored {
		t.Fatal("the checkpoint substitutions matched nothing; the stored document changed shape")
	}

	operationBlob := hashObject(t, repository, operation)
	stateBlob := hashObject(t, repository, marked+"\n")
	tree := gitWithInput(t, repository,
		fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob),
		"mktree")
	commit := gitWithInput(t, repository, "workbook: comment on "+taskID, "commit-tree", tree, "-p", head)
	gitOutput(t, repository, "update-ref", ref, commit, head)
	return commit
}

func hashObject(t *testing.T, repository *gitstore.Repository, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write object: %v", err)
	}
	return gitOutput(t, repository, "hash-object", "-w", path)
}

func gitWithInput(t *testing.T, repository *gitstore.Repository, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository.CommonGitDir}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return trimNewline(string(output))
}

// The projection must carry the writer-format watermark, not recompute it.
//
// This is the one place the whole contract can be broken silently. Every read
// and every mutation goes through the projection rather than through Git, and
// the projection rebuilds its snapshots from SQLite columns. A column that is
// not there does not read as missing — it reads as generation zero, which is a
// claim that the task is safe to fold and safe to write on top of. So the
// assertions below deliberately look at the second read and at a freshly opened
// store, where the answer can only have come out of the database.
func TestTheProjectionCarriesTheWatermarkThroughSQLite(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Ordinary title")
	writeFutureTaskCommit(t, repository, created.ID)

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// First read populates the rows from Git.
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(first) error = %v", err)
	}
	// Second read is served from SQLite: the head has not moved, so nothing is
	// re-read from Git and every value below came out of a column.
	projected, err := store.List(ctx, config)
	if err != nil {
		t.Fatalf("List(second) error = %v", err)
	}
	if len(projected) != 1 {
		t.Fatalf("List() returned %d snapshots, want 1", len(projected))
	}
	if !projected[0].State.RequiresNewerReader() {
		t.Fatal("the projected checkpoint lost its watermark; a mutation would build on a history this build cannot fold")
	}
	if got := projected[0].State.MinReader; got != projectionFutureGeneration {
		t.Fatalf("projected watermark = %d, want %d", got, projectionFutureGeneration)
	}
	if got := projected[0].State.Task.Title; got != "Ordinary title" {
		t.Fatalf("projected title = %q, want the checkpoint's; the task must still read", got)
	}

	// The advisory every read surface shows is derived from the projected
	// snapshot, so it has to survive the same round trip.
	service := core.Service{Config: config, Reader: store, Writer: repository, Actor: "test@example.test"}
	if !service.Project(projected[0]).NewerWriter {
		t.Fatal("the projected task does not report a newer writer; every read surface would show it as ordinary")
	}

	// A second store handle reads the same rows without Git having moved,
	// which is the cold-start shape a later command takes.
	reopened, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open(reopened) error = %v", err)
	}
	cold, err := reopened.List(ctx, config)
	if err != nil {
		t.Fatalf("List(reopened) error = %v", err)
	}
	if len(cold) != 1 || !cold[0].State.RequiresNewerReader() {
		t.Fatalf("reopened projection = %#v, want the watermark preserved", cold)
	}
}

// A mutation resolved through the projection is refused.
//
// Without the stored watermark this write succeeds and lands a generation-zero
// checkpoint on top of a generation-one history — the silent wrong write, and
// the reason the column exists.
func TestAMutationThroughTheProjectionIsRefusedOnANewerHistory(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Ordinary title")
	writeFutureTaskCommit(t, repository, created.ID)

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	head := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+created.ID)
	service := core.Service{
		Config: config,
		Reader: store,
		Writer: repository,
		Actor:  "test@example.test",
		Now:    func() time.Time { return time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC) },
		IDs:    core.IDSourceFunc(func() (string, error) { return "01K0M6B8A4FTT8C39MXXYTW7D7", nil }),
	}
	title := "Mine now"
	_, err = service.UpdateMutation(ctx, created.ID, core.UpdateInput{Title: &title})
	if err == nil {
		t.Fatal("the mutation succeeded; it wrote a generation-zero checkpoint onto a newer history")
	}
	if core.CategoryOf(err) != core.CategoryNewerWriter {
		t.Fatalf("category = %q, want %q; error = %v", core.CategoryOf(err), core.CategoryNewerWriter, err)
	}
	if !strings.Contains(err.Error(), created.ID) {
		t.Fatalf("error = %q, want it to name %s", err, created.ID)
	}
	if got := gitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+created.ID); got != head {
		t.Fatalf("task ref moved to %q; a refused mutation must write nothing", got)
	}
}

// A cache written before the marker existed is discarded, not read.
//
// Such a cache has no column for the watermark, so every row it holds would
// answer generation zero — not "unknown", but the one claim that must never be
// invented. Two independent guards stop it: the schema version in the metadata,
// and the column probe that opens every table this build needs. This test pins
// the behavior rather than either guard, so removing one still leaves the
// promise checked; removing both fails here.
func TestACacheWrittenBeforeTheMarkerIsDiscarded(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Ordinary title")
	writeFutureTaskCommit(t, repository, created.ID)

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List(populate) error = %v", err)
	}
	// The shape a build that predates the marker leaves behind: the same tables
	// minus the watermark columns, stamped with the schema version that build
	// wrote.
	writePreMarkerCache(t, store.CachePath(), config.ProjectID)

	reopened, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open(stale cache) error = %v", err)
	}
	projected, err := reopened.List(ctx, config)
	if err != nil {
		t.Fatalf("List(stale cache) error = %v", err)
	}
	if len(projected) != 1 {
		t.Fatalf("List() returned %d snapshots, want 1", len(projected))
	}
	if !projected[0].State.RequiresNewerReader() {
		t.Fatal("a cache written before the marker was read as authoritative; every row in it claims generation zero")
	}
}

// writePreMarkerCache replaces the projection cache with the schema this
// package shipped before the writer-format marker existed.
func writePreMarkerCache(t *testing.T, path, projectID string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s%s: %v", path, suffix, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open pre-marker cache: %v", err)
	}
	defer db.Close()
	const priorSchema = `
CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  history_generation TEXT NOT NULL, logical_clock INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL,
  priority TEXT NOT NULL, rank TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, deleted INTEGER NOT NULL
);
CREATE TABLE task_labels (
  task_id TEXT NOT NULL, label TEXT NOT NULL, PRIMARY KEY (task_id, label)
);
CREATE TABLE task_dependencies (
  task_id TEXT NOT NULL, dependency_id TEXT NOT NULL,
  PRIMARY KEY (task_id, dependency_id)
);
CREATE TABLE operations (
  operation_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, commit_id TEXT NOT NULL,
  chain_index INTEGER NOT NULL, pack_index INTEGER NOT NULL,
  logical_clock INTEGER NOT NULL, history_generation TEXT NOT NULL,
  actor TEXT NOT NULL, wall_time TEXT NOT NULL, type TEXT NOT NULL,
  field TEXT NOT NULL, value TEXT NOT NULL, task_data TEXT NOT NULL
);`
	if _, err := db.Exec(priorSchema); err != nil {
		t.Fatalf("create pre-marker schema: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO projection_meta (key, value) VALUES ('schema_version', '2'), ('project_id', ?)`,
		projectID,
	); err != nil {
		t.Fatalf("stamp pre-marker metadata: %v", err)
	}
}

// A pack's own declared generation survives the operation rows too, so a replay
// rebuilt from the projection refuses where a replay from Git would.
func TestAProjectedPackKeepsItsDeclaredGeneration(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Ordinary title")
	writeFutureTaskCommit(t, repository, created.ID)

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.List(ctx, config); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	if len(history.Entries) == 0 {
		t.Fatal("TaskHistory() returned no entries")
	}
	last := history.Entries[len(history.Entries)-1]
	if !last.Operation.RequiresNewerReader() {
		t.Fatalf("replayed pack minReader = %d, want %d; the declared generation must not be recomputed "+
			"from operation types this build does not know", last.Operation.MinReader, projectionFutureGeneration)
	}

	// Replaying that chain stops at the newer pack, and says so in the right
	// words rather than calling the history corrupt.
	_, truncation := core.ReplayHistory(config.Key, history)
	if truncation == nil {
		t.Fatal("the replay folded a pack this build cannot read")
	}
	if truncation.Category != core.CategoryNewerWriter {
		t.Fatalf("truncation category = %q, want %q; message = %q",
			truncation.Category, core.CategoryNewerWriter, truncation.Message)
	}
}
