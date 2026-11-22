package projection

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// datedTestSnapshot is testSnapshot carrying a due date, with the watermark a
// history that recorded one has.
func datedTestSnapshot(taskID, head, title, due string) core.Snapshot {
	snapshot := testSnapshot(taskID, head, title)
	snapshot.State.Task.Due = due
	snapshot.State.MinReader = core.SupportedFormatGeneration
	return snapshot
}

func TestDeletingTheCacheAndRebuildingReproducesTheDueDate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := testConfig()
	dated := datedTestSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-dated", "Dated", "2026-12-01")
	undated := testSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D2", "head-undated", "Undated")
	source := &staticHeadSource{
		heads: []gitstore.TaskHead{
			{TaskID: dated.State.TaskID, ObjectID: dated.Head},
			{TaskID: undated.State.TaskID, ObjectID: undated.Head},
		},
		snapshots: map[string]core.Snapshot{dated.Head: dated, undated.Head: undated},
	}
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	for round := 0; round < 2; round++ {
		store, err := openStoreForTest(t, ctx, source, config, cachePath)
		if err != nil {
			t.Fatalf("round %d: openStore() error = %v", round, err)
		}
		if _, err := store.Rebuild(ctx); err != nil {
			t.Fatalf("round %d: Rebuild() error = %v", round, err)
		}
		if got := mustGet(t, store, config, dated.State.TaskID); got.State.Task.Due != "2026-12-01" || got.State.MinReader != dated.State.MinReader {
			t.Fatalf("round %d: projected due = %q, minReader %d", round, got.State.Task.Due, got.State.MinReader)
		}
		if got := mustGet(t, store, config, undated.State.TaskID); got.State.Task.Due != "" {
			t.Fatalf("round %d: an undated task projected due %q", round, got.State.Task.Due)
		}
		snapshots, err := store.List(ctx, config)
		if err != nil || len(snapshots) != 2 || snapshots[0].State.Task.Due != "2026-12-01" {
			t.Fatalf("round %d: List() = %#v, %v", round, snapshots, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("round %d: Close() error = %v", round, err)
		}
		// The second round starts from nothing: the cache is deleted outright,
		// and everything above has to come back from the source alone.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(cachePath + suffix); err != nil && !os.IsNotExist(err) {
				t.Fatalf("delete cache: %v", err)
			}
		}
	}
}

// previousSchema is version 7's tasks table, the last one with no due column:
// what a cache written by the release before due dates holds on disk.
const previousSchema = `
CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  history_generation TEXT NOT NULL, compacted_from TEXT NOT NULL,
  logical_clock INTEGER NOT NULL, min_reader INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL,
  priority TEXT NOT NULL, rank TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, deleted INTEGER NOT NULL,
  thread TEXT NOT NULL
);`

func TestACacheFromBeforeTheDueColumnIsReplacedWithoutAManualStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := testConfig()
	dated := datedTestSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", "head-dated", "Dated", "2026-12-01")
	source := &staticHeadSource{
		heads:     []gitstore.TaskHead{{TaskID: dated.State.TaskID, ObjectID: dated.Head}},
		snapshots: map[string]core.Snapshot{dated.Head: dated},
	}
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	old, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatalf("open the old cache: %v", err)
	}
	for _, statement := range strings.Split(previousSchema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := old.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create the old schema: %v", err)
		}
	}
	// A row for the task as the old build cached it: no date, because the old
	// build had nowhere to put one, at the head the source still serves.
	if _, err := old.ExecContext(ctx, `INSERT INTO projection_meta (key, value) VALUES ('schema_version', '7'), ('project_id', ?)`, config.ProjectID); err != nil {
		t.Fatalf("stamp the old cache: %v", err)
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO tasks VALUES (?, ?, ?, ?, '', 1, 0, 'Dated', '', 'backlog', 'medium', '1/1', ?, ?, 0, '')`,
		dated.State.TaskID, dated.Head, config.ProjectID, dated.State.History.Generation,
		dated.State.Task.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		dated.State.Task.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
		t.Fatalf("write the old row: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the old cache: %v", err)
	}

	store, err := openStoreForTest(t, ctx, source, config, cachePath)
	if err != nil {
		t.Fatalf("openStore() over the old cache error = %v", err)
	}
	got := mustGet(t, store, config, dated.State.TaskID)
	if got.State.Task.Due != "2026-12-01" {
		t.Fatalf("due = %q after opening a cache with no due column; it must be read from Git again", got.State.Task.Due)
	}
}
