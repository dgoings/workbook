package historyvalidation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
	_ "modernc.org/sqlite"
)

func TestPrepareMarksNewChangedAndVersionMismatchedHeadsPending(t *testing.T) {
	// Production mutation: removing any new, changed, or validator-version pending transition leaves the corresponding row completed.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	original := []gitstore.TaskHead{
		{TaskID: taskID(1), ObjectID: "head-unchanged"},
		{TaskID: taskID(2), ObjectID: "head-before-change"},
		{TaskID: taskID(3), ObjectID: "head-version-mismatch"},
		{TaskID: taskID(4), ObjectID: "head-now-absent"},
	}
	if _, err := cache.Prepare(ctx, original, false); err != nil {
		t.Fatalf("Prepare(original) error = %v", err)
	}
	for index, head := range original {
		state := canonicalState(t, head.TaskID, generationID(index+1), "original")
		if err := cache.Record(ctx, Completion{
			TaskID:               head.TaskID,
			ObservedHead:         head.ObjectID,
			Status:               StatusValid,
			LastValidCommit:      "commit-" + head.TaskID,
			LastValidGeneration:  generationID(index + 1),
			LastValidState:       state,
			ValidatedCommitIDs:   []string{"commit-" + head.TaskID},
			ValidatedCommitCount: index + 1,
		}); err != nil {
			t.Fatalf("Record(%s) error = %v", head.TaskID, err)
		}
	}
	if _, err := cache.db.ExecContext(ctx,
		`UPDATE task_validation SET validator_version = ? WHERE task_id = ?`,
		ValidatorVersion-1, taskID(3),
	); err != nil {
		t.Fatalf("mark validator version mismatch: %v", err)
	}

	current := []gitstore.TaskHead{
		{TaskID: taskID(1), ObjectID: "head-unchanged"},
		{TaskID: taskID(2), ObjectID: "head-after-change"},
		{TaskID: taskID(3), ObjectID: "head-version-mismatch"},
		{TaskID: taskID(5), ObjectID: "head-new"},
	}
	prepared, err := cache.Prepare(ctx, current, false)
	if err != nil {
		t.Fatalf("Prepare(current) error = %v", err)
	}

	if got := prepared[taskID(1)]; got.Status != StatusValid || got.ObservedHead != "head-unchanged" {
		t.Fatalf("unchanged row = %#v, want unchanged valid completion", got)
	}
	changed := prepared[taskID(2)]
	if changed.Status != StatusPending || changed.ObservedHead != "head-after-change" {
		t.Fatalf("changed row = %#v, want pending at new head", changed)
	}
	if changed.LastValidCommit != "commit-"+taskID(2) ||
		changed.LastValidGeneration != generationID(2) ||
		!bytes.Equal(changed.LastValidState, canonicalState(t, taskID(2), generationID(2), "original")) ||
		changed.ValidatedCommitCount != 2 {
		t.Fatalf("changed row boundary = %#v, want retained valid boundary", changed)
	}
	versionMismatch := prepared[taskID(3)]
	if versionMismatch.Status != StatusPending || versionMismatch.ValidatorVersion != ValidatorVersion {
		t.Fatalf("version-mismatched row = %#v, want current-version pending", versionMismatch)
	}
	if versionMismatch.LastValidCommit != "" || versionMismatch.LastValidGeneration != "" ||
		len(versionMismatch.LastValidState) != 0 || versionMismatch.ValidatedCommitCount != 0 {
		t.Fatalf("version-mismatched row boundary = %#v, want invalidated boundary", versionMismatch)
	}
	added := prepared[taskID(5)]
	if added.Status != StatusPending || added.ObservedHead != "head-new" ||
		added.LastValidCommit != "" || added.LastValidGeneration != "" ||
		len(added.LastValidState) != 0 || added.ValidatedCommitCount != 0 {
		t.Fatalf("new row = %#v, want empty pending row", added)
	}
	if _, found := prepared[taskID(4)]; found {
		t.Fatalf("Prepare() retained absent task %q", taskID(4))
	}
	var absentCount int
	if err := cache.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_validation WHERE task_id = ?`, taskID(4),
	).Scan(&absentCount); err != nil {
		t.Fatalf("query absent task: %v", err)
	}
	if absentCount != 0 {
		t.Fatalf("absent task row count = %d, want 0", absentCount)
	}
}

func TestPrepareFullMarksEveryObservedTaskPendingWithoutDiscardingOldCompletion(t *testing.T) {
	// Production mutation: clearing boundary or commit rows during full preparation makes an interrupted full audit lose its prior completion.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "head-stable"}
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(initial) error = %v", err)
	}
	state := canonicalState(t, head.TaskID, generationID(1), "completed")
	if err := cache.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      "commit-2",
		LastValidGeneration:  generationID(1),
		LastValidState:       state,
		ValidatedCommitIDs:   []string{"commit-1", "commit-2"},
		ValidatedCommitCount: 2,
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	prepared, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, true)
	if err != nil {
		t.Fatalf("Prepare(full) error = %v", err)
	}
	got := prepared[head.TaskID]
	if got.Status != StatusPending || got.ObservedHead != head.ObjectID ||
		got.LastValidCommit != "commit-2" || got.LastValidGeneration != generationID(1) ||
		!bytes.Equal(got.LastValidState, state) || got.ValidatedCommitCount != 2 {
		t.Fatalf("full prepared row = %#v, want pending with prior completion boundary", got)
	}
	if got := validatedCommitIDs(t, cache, head.TaskID); !reflect.DeepEqual(got, []string{"commit-1", "commit-2"}) {
		t.Fatalf("validated commits after full Prepare = %#v, want old completion retained", got)
	}
}

func TestRecordCommitsOneTaskValidOrInvalidResultAtomically(t *testing.T) {
	// Production mutation: omitting one commit insert or committing before the final task update exposes a partial completion.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	heads := []gitstore.TaskHead{
		{TaskID: taskID(1), ObjectID: "valid-head"},
		{TaskID: taskID(2), ObjectID: "invalid-head"},
		{TaskID: taskID(3), ObjectID: "rollback-head"},
		{TaskID: taskID(4), ObjectID: "full-head"},
		{TaskID: taskID(5), ObjectID: "unrelated-head"},
	}
	if _, err := cache.Prepare(ctx, heads, false); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	validState := canonicalState(t, taskID(1), generationID(1), "valid")
	if err := cache.Record(ctx, Completion{
		TaskID:               taskID(1),
		ObservedHead:         "valid-head",
		Status:               StatusValid,
		LastValidCommit:      "valid-commit-2",
		LastValidGeneration:  generationID(1),
		LastValidState:       validState,
		ValidatedCommitIDs:   []string{"valid-commit-1", "valid-commit-2"},
		ValidatedCommitCount: 2,
	}); err != nil {
		t.Fatalf("Record(valid) error = %v", err)
	}
	failure := &Failure{
		TaskID:   taskID(2),
		Commit:   "invalid-commit",
		Category: "corrupt-data",
		Message:  "stored checkpoint differs from computed state",
	}
	invalidState := canonicalState(t, taskID(2), generationID(2), "last valid")
	if err := cache.Record(ctx, Completion{
		TaskID:               taskID(2),
		ObservedHead:         "invalid-head",
		Status:               StatusInvalid,
		LastValidCommit:      "invalid-parent",
		LastValidGeneration:  generationID(2),
		LastValidState:       invalidState,
		ValidatedCommitIDs:   []string{"invalid-parent"},
		ValidatedCommitCount: 1,
		Failure:              failure,
	}); err != nil {
		t.Fatalf("Record(invalid) error = %v", err)
	}

	snapshot, err := cache.Snapshot(ctx, []string{taskID(1), taskID(2)})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot) != 2 {
		t.Fatalf("Snapshot() length = %d, want 2", len(snapshot))
	}
	if got := snapshot[0]; got.Status != StatusValid || got.Failure != nil ||
		got.LastValidCommit != "valid-commit-2" || got.ValidatedCommitCount != 2 ||
		!bytes.Equal(got.LastValidState, validState) {
		t.Fatalf("valid completion = %#v", got)
	}
	if got := snapshot[1]; got.Status != StatusInvalid || !reflect.DeepEqual(got.Failure, failure) ||
		got.LastValidCommit != "invalid-parent" || got.ValidatedCommitCount != 1 ||
		!bytes.Equal(got.LastValidState, invalidState) {
		t.Fatalf("invalid completion = %#v", got)
	}
	if got := validatedCommitRows(t, cache); !reflect.DeepEqual(got, []validatedCommitRow{
		{ValidatorVersion, "invalid-parent", taskID(2), generationID(2)},
		{ValidatorVersion, "valid-commit-1", taskID(1), generationID(1)},
		{ValidatorVersion, "valid-commit-2", taskID(1), generationID(1)},
	}) {
		t.Fatalf("validated commit rows = %#v", got)
	}

	err = cache.Record(ctx, Completion{
		TaskID:               taskID(3),
		ObservedHead:         "rollback-head",
		Status:               StatusValid,
		LastValidCommit:      "duplicate",
		LastValidGeneration:  generationID(3),
		LastValidState:       canonicalState(t, taskID(3), generationID(3), "rollback"),
		ValidatedCommitIDs:   []string{"duplicate", "duplicate"},
		ValidatedCommitCount: 2,
	})
	if err == nil {
		t.Fatal("Record(duplicate commits) error = nil, want transaction rollback")
	}
	rolledBack, err := cache.Snapshot(ctx, []string{taskID(3)})
	if err != nil {
		t.Fatalf("Snapshot(rollback) error = %v", err)
	}
	if len(rolledBack) != 1 || rolledBack[0].Status != StatusPending {
		t.Fatalf("rolled-back task = %#v, want pending", rolledBack)
	}
	if got := validatedCommitIDs(t, cache, taskID(3)); len(got) != 0 {
		t.Fatalf("rolled-back validated commits = %#v, want none", got)
	}

	recordSimpleValid(t, ctx, cache, heads[3], "old-full-commit", generationID(4))
	recordSimpleValid(t, ctx, cache, heads[4], "unrelated-commit", generationID(5))
	if _, err := cache.Prepare(ctx, heads, true); err != nil {
		t.Fatalf("Prepare(full) error = %v", err)
	}
	if err := cache.Record(ctx, Completion{
		TaskID:               taskID(4),
		ObservedHead:         "full-head",
		Status:               StatusValid,
		LastValidCommit:      "new-full-commit",
		LastValidGeneration:  generationID(4),
		LastValidState:       canonicalState(t, taskID(4), generationID(4), "full"),
		ValidatedCommitIDs:   []string{"new-full-commit"},
		ValidatedCommitCount: 1,
		Full:                 true,
	}); err != nil {
		t.Fatalf("Record(full) error = %v", err)
	}
	if got := validatedCommitIDs(t, cache, taskID(4)); !reflect.DeepEqual(got, []string{"new-full-commit"}) {
		t.Fatalf("full replacement commits = %#v, want only new full commit", got)
	}
	if got := validatedCommitIDs(t, cache, taskID(5)); !reflect.DeepEqual(got, []string{"unrelated-commit"}) {
		t.Fatalf("unrelated commits = %#v, want preserved", got)
	}
}

func TestRecordRejectsStaleObservedHead(t *testing.T) {
	// Production mutation: updating without the observed-head and pending predicates lets stale or duplicate work overwrite newer cache state.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	task := taskID(1)
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{{TaskID: task, ObjectID: "old-head"}}, false); err != nil {
		t.Fatalf("Prepare(old) error = %v", err)
	}
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{{TaskID: task, ObjectID: "new-head"}}, false); err != nil {
		t.Fatalf("Prepare(new) error = %v", err)
	}
	stale := Completion{
		TaskID:               task,
		ObservedHead:         "old-head",
		Status:               StatusValid,
		LastValidCommit:      "old-head",
		LastValidGeneration:  generationID(1),
		LastValidState:       canonicalState(t, task, generationID(1), "stale"),
		ValidatedCommitIDs:   []string{"old-head"},
		ValidatedCommitCount: 1,
	}
	if err := cache.Record(ctx, stale); err == nil || !strings.Contains(err.Error(), "observed head") {
		t.Fatalf("Record(stale) error = %v, want observed-head rejection", err)
	}
	got, err := cache.Snapshot(ctx, []string{task})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(got) != 1 || got[0].ObservedHead != "new-head" || got[0].Status != StatusPending {
		t.Fatalf("after stale record = %#v, want new-head pending", got)
	}
	fresh := stale
	fresh.ObservedHead = "new-head"
	fresh.LastValidCommit = "new-head"
	fresh.ValidatedCommitIDs = []string{"new-head"}
	if err := cache.Record(ctx, fresh); err != nil {
		t.Fatalf("Record(fresh) error = %v", err)
	}
	if err := cache.Record(ctx, fresh); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("Record(already complete) error = %v, want pending-row rejection", err)
	}
}

func TestUnchangedInvalidHeadRetainsExactCachedFailure(t *testing.T) {
	// Production mutation: treating an unchanged invalid head as pending discards the exact cached failure and repeats known-failing work.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "invalid-head"}
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(initial) error = %v", err)
	}
	failure := &Failure{
		TaskID:   head.TaskID,
		Commit:   "0123456789abcdef0123456789abcdef01234567",
		Category: "corrupt-data",
		Message:  "logical clock does not advance by one",
	}
	if err := cache.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusInvalid,
		LastValidCommit:      "valid-parent",
		LastValidGeneration:  generationID(1),
		LastValidState:       canonicalState(t, head.TaskID, generationID(1), "boundary"),
		ValidatedCommitIDs:   []string{"valid-parent"},
		ValidatedCommitCount: 1,
		Failure:              failure,
	}); err != nil {
		t.Fatalf("Record(invalid) error = %v", err)
	}

	prepared, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, false)
	if err != nil {
		t.Fatalf("Prepare(unchanged) error = %v", err)
	}
	got := prepared[head.TaskID]
	if got.Status != StatusInvalid || !reflect.DeepEqual(got.Failure, failure) {
		t.Fatalf("unchanged invalid completion = %#v, want exact failure %#v", got, failure)
	}
}

func TestCompletedTasksSurviveCancellationAndPendingTasksResume(t *testing.T) {
	// Production mutation: sharing one transaction across tasks rolls back completed tasks when later validation is cancelled.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	heads := []gitstore.TaskHead{
		{TaskID: taskID(1), ObjectID: "completed-head"},
		{TaskID: taskID(2), ObjectID: "pending-head"},
	}
	if _, err := cache.Prepare(ctx, heads, false); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	recordSimpleValid(t, ctx, cache, heads[0], "completed-commit", generationID(1))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := cache.Record(cancelled, Completion{
		TaskID:               heads[1].TaskID,
		ObservedHead:         heads[1].ObjectID,
		Status:               StatusValid,
		LastValidCommit:      "pending-commit",
		LastValidGeneration:  generationID(2),
		LastValidState:       canonicalState(t, heads[1].TaskID, generationID(2), "pending"),
		ValidatedCommitIDs:   []string{"pending-commit"},
		ValidatedCommitCount: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Record(cancelled) error = %v, want context.Canceled", err)
	}

	resumed, err := cache.Prepare(ctx, heads, false)
	if err != nil {
		t.Fatalf("Prepare(resume) error = %v", err)
	}
	if resumed[heads[0].TaskID].Status != StatusValid {
		t.Fatalf("completed task status = %q, want valid", resumed[heads[0].TaskID].Status)
	}
	if resumed[heads[1].TaskID].Status != StatusPending {
		t.Fatalf("interrupted task status = %q, want pending", resumed[heads[1].TaskID].Status)
	}
	recordSimpleValid(t, ctx, cache, heads[1], "pending-commit", generationID(2))
	final, err := cache.Snapshot(ctx, []string{heads[0].TaskID, heads[1].TaskID})
	if err != nil {
		t.Fatalf("Snapshot(final) error = %v", err)
	}
	if final[0].Status != StatusValid || final[1].Status != StatusValid {
		t.Fatalf("final statuses = %q, %q, want valid, valid", final[0].Status, final[1].Status)
	}
}

func TestOpenCacheRebuildsMissingIncompatibleForeignAndCorruptCaches(t *testing.T) {
	// Production mutation: trusting cache metadata or schema without checking it reuses missing, incompatible, foreign-project, or corrupt state.
	ctx := context.Background()
	config := testConfig()
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "missing"},
		{
			name: "incompatible",
			setup: func(t *testing.T, path string) {
				writeCacheFixture(t, path, "0", config.ProjectID, taskID(1))
			},
		},
		{
			name: "foreign project",
			setup: func(t *testing.T, path string) {
				writeCacheFixture(t, path, schemaVersion, "01K0M6B8A4FTT8C39MXXYTW7ZZ", taskID(1))
			},
		},
		{
			// Production mutation: accepting a cache whose per-task index is
			// absent silently restores the full-table DELETE scan.
			name: "missing per-task index",
			setup: func(t *testing.T, path string) {
				writeCacheFixtureWithIndex(t, path, schemaVersion, config.ProjectID, taskID(1), false)
			},
		},
		{
			name: "corrupt",
			setup: func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commonGitDir := t.TempDir()
			path := filepath.Join(commonGitDir, "workbook", "validation.sqlite")
			if test.setup != nil {
				test.setup(t, path)
			}
			cache, err := OpenCache(ctx, commonGitDir, config)
			if err != nil {
				t.Fatalf("OpenCache() error = %v", err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			if got, want := cache.Path(), path; got != want {
				t.Fatalf("Path() = %q, want %q", got, want)
			}
			var metadata map[string]string
			metadata = queryMetadata(t, cache)
			if !reflect.DeepEqual(metadata, map[string]string{
				"project_id":        config.ProjectID,
				"schema_version":    schemaVersion,
				"reader_generation": strconv.Itoa(readerGeneration),
			}) {
				t.Fatalf("metadata = %#v, want fresh current cache", metadata)
			}
			rows, err := cache.Snapshot(ctx, []string{taskID(1)})
			if err != nil {
				t.Fatalf("Snapshot() error = %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("Snapshot() = %#v, want old rows discarded", rows)
			}
		})
	}
}

func TestOpenCacheUsesCommonGitDirectoryAcrossWorktrees(t *testing.T) {
	// Production mutation: deriving the cache from a worktree root gives linked worktrees separate validation progress.
	ctx := context.Background()
	root := testrepo.New(t)
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README")
	runGit(t, root, "commit", "--quiet", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, root, "worktree", "add", "--detach", "--quiet", linked, "HEAD")
	t.Cleanup(func() {
		command := exec.Command("git", "-C", root, "worktree", "remove", "--force", linked)
		_ = command.Run()
	})
	rootRepository, err := gitstore.Open(ctx, root)
	if err != nil {
		t.Fatalf("gitstore.Open(root) error = %v", err)
	}
	linkedRepository, err := gitstore.Open(ctx, linked)
	if err != nil {
		t.Fatalf("gitstore.Open(linked) error = %v", err)
	}
	if rootRepository.CommonGitDir != linkedRepository.CommonGitDir {
		t.Fatalf("common Git directories differ: %q and %q", rootRepository.CommonGitDir, linkedRepository.CommonGitDir)
	}

	first, err := OpenCache(ctx, rootRepository.CommonGitDir, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(root) error = %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "shared-head"}
	if _, err := first.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(root) error = %v", err)
	}
	second, err := OpenCache(ctx, linkedRepository.CommonGitDir, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(linked) error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.Path() != second.Path() ||
		first.Path() != filepath.Join(rootRepository.CommonGitDir, "workbook", "validation.sqlite") {
		t.Fatalf("cache paths = %q and %q, want shared common-dir path", first.Path(), second.Path())
	}
	snapshot, err := second.Snapshot(ctx, []string{head.TaskID})
	if err != nil {
		t.Fatalf("Snapshot(linked) error = %v", err)
	}
	if len(snapshot) != 1 || snapshot[0].ObservedHead != head.ObjectID || snapshot[0].Status != StatusPending {
		t.Fatalf("linked-worktree snapshot = %#v, want root worktree's pending row", snapshot)
	}
}

func TestOpenCacheSerializesConcurrentRebuildsAcrossWorktrees(t *testing.T) {
	// Production mutation: removing the common-directory initialization lock or its under-lock usability recheck lets a later rename discard progress recorded through the first cache handle.
	ctx := context.Background()
	commonGitDir := t.TempDir()
	cacheDir := filepath.Join(commonGitDir, "workbook")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(
		filepath.Join(cacheDir, "validation.sqlite.lock"),
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("lock validation cache initialization: %v", err)
	}

	type openResult struct {
		cache *Cache
		err   error
	}
	start := make(chan struct{})
	results := make(chan openResult, 2)
	for range 2 {
		go func() {
			<-start
			cache, err := OpenCache(ctx, commonGitDir, testConfig())
			results <- openResult{cache: cache, err: err}
		}()
	}
	close(start)

	var opened []openResult
	premature := false
	select {
	case result := <-results:
		opened = append(opened, result)
		premature = true
	case <-time.After(250 * time.Millisecond):
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("unlock validation cache initialization: %v", err)
	}
	for len(opened) < 2 {
		select {
		case result := <-results:
			opened = append(opened, result)
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent OpenCache calls did not finish after releasing initialization lock")
		}
	}
	for _, result := range opened {
		if result.cache != nil {
			t.Cleanup(func() { _ = result.cache.Close() })
		}
		if result.err != nil {
			t.Fatalf("OpenCache() error = %v", result.err)
		}
	}
	if premature {
		t.Fatal("OpenCache returned while another process held the common-directory initialization lock")
	}

	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "concurrent-head"}
	if _, err := opened[0].cache.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(first handle) error = %v", err)
	}
	recordSimpleValid(t, ctx, opened[0].cache, head, "concurrent-commit", generationID(1))
	snapshot, err := opened[1].cache.Snapshot(ctx, []string{head.TaskID})
	if err != nil {
		t.Fatalf("Snapshot(second handle) error = %v", err)
	}
	if len(snapshot) != 1 ||
		snapshot[0].Status != StatusValid ||
		snapshot[0].LastValidCommit != "concurrent-commit" {
		t.Fatalf("second handle snapshot = %#v, want first handle's recorded completion", snapshot)
	}
}

type validatedCommitRow struct {
	validatorVersion  int
	commitID          string
	taskID            string
	historyGeneration string
}

func openTestCache(t *testing.T, ctx context.Context, config core.ProjectConfig) *Cache {
	t.Helper()
	cache, err := OpenCache(ctx, t.TempDir(), config)
	if err != nil {
		t.Fatalf("OpenCache() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return cache
}

func testConfig() core.ProjectConfig {
	return core.ProjectConfig{
		Format:    "workbook.project",
		Version:   1,
		ProjectID: "01K0M6B8A4FTT8C39MXXYTW7C1",
		Key:       "WB",
	}
}

func taskID(index int) string {
	return []string{
		"",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D1",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D2",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D3",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D4",
		"WB-01K0M6B8A4FTT8C39MXXYTW7D5",
	}[index]
}

func generationID(index int) string {
	return []string{
		"",
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
		"01K0M6B8A4FTT8C39MXXYTW7E3",
		"01K0M6B8A4FTT8C39MXXYTW7E4",
		"01K0M6B8A4FTT8C39MXXYTW7E5",
	}[index]
}

func canonicalState(t *testing.T, taskID, generation, title string) []byte {
	t.Helper()
	createdAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	encoded, err := core.EncodeDocument(core.StateDocument{
		Format:       "workbook.task-state",
		Version:      1,
		ProjectID:    testConfig().ProjectID,
		TaskID:       taskID,
		History:      core.History{Generation: generation},
		LogicalClock: 1,
		Task: core.TaskData{
			Title:        title,
			Status:       core.StatusBacklog,
			Priority:     core.PriorityMedium,
			Labels:       []string{},
			Rank:         "1/1",
			Dependencies: []string{},
			CreatedAt:    createdAt,
			UpdatedAt:    createdAt,
		},
	})
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	return encoded
}

func recordSimpleValid(
	t *testing.T,
	ctx context.Context,
	cache *Cache,
	head gitstore.TaskHead,
	commitID string,
	generation string,
) {
	t.Helper()
	if err := cache.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      commitID,
		LastValidGeneration:  generation,
		LastValidState:       canonicalState(t, head.TaskID, generation, "completed"),
		ValidatedCommitIDs:   []string{commitID},
		ValidatedCommitCount: 1,
	}); err != nil {
		t.Fatalf("Record(%s) error = %v", head.TaskID, err)
	}
}

func validatedCommitIDs(t *testing.T, cache *Cache, taskID string) []string {
	t.Helper()
	rows, err := cache.db.Query(
		`SELECT commit_id FROM validated_commits WHERE validator_version = ? AND task_id = ? ORDER BY commit_id`,
		ValidatorVersion, taskID,
	)
	if err != nil {
		t.Fatalf("query validated commits: %v", err)
	}
	defer rows.Close()
	var commits []string
	for rows.Next() {
		var commit string
		if err := rows.Scan(&commit); err != nil {
			t.Fatalf("scan validated commit: %v", err)
		}
		commits = append(commits, commit)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read validated commits: %v", err)
	}
	return commits
}

func validatedCommitRows(t *testing.T, cache *Cache) []validatedCommitRow {
	t.Helper()
	rows, err := cache.db.Query(`
		SELECT validator_version, commit_id, task_id, history_generation
		FROM validated_commits
		ORDER BY commit_id
	`)
	if err != nil {
		t.Fatalf("query validated commit rows: %v", err)
	}
	defer rows.Close()
	var result []validatedCommitRow
	for rows.Next() {
		var row validatedCommitRow
		if err := rows.Scan(&row.validatorVersion, &row.commitID, &row.taskID, &row.historyGeneration); err != nil {
			t.Fatalf("scan validated commit row: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read validated commit rows: %v", err)
	}
	return result
}

func queryMetadata(t *testing.T, cache *Cache) map[string]string {
	t.Helper()
	rows, err := cache.db.Query(`SELECT key, value FROM validation_meta ORDER BY key`)
	if err != nil {
		t.Fatalf("query validation metadata: %v", err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatalf("scan validation metadata: %v", err)
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read validation metadata: %v", err)
	}
	return result
}

func writeCacheFixture(t *testing.T, path, version, projectID, staleTaskID string) {
	t.Helper()
	writeCacheFixtureWithIndex(t, path, version, projectID, staleTaskID, true)
}

func writeCacheFixtureWithIndex(t *testing.T, path, version, projectID, staleTaskID string, withTaskIndex bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE validation_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE task_validation (
			task_id TEXT PRIMARY KEY,
			observed_head TEXT NOT NULL,
			validator_version INTEGER NOT NULL,
			status TEXT NOT NULL,
			last_valid_commit TEXT NOT NULL,
			last_valid_generation TEXT NOT NULL,
			last_valid_state BLOB NOT NULL,
			validated_commit_count INTEGER NOT NULL,
			failure_commit TEXT NOT NULL,
			failure_category TEXT NOT NULL,
			failure_message TEXT NOT NULL
		);
		CREATE TABLE validated_commits (
			validator_version INTEGER NOT NULL,
			commit_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			history_generation TEXT NOT NULL,
			PRIMARY KEY (validator_version, commit_id)
		);
	`); err != nil {
		t.Fatal(err)
	}
	if withTaskIndex {
		if _, err := db.Exec(
			`CREATE INDEX ` + validatedCommitsByTaskIndex + ` ON validated_commits (validator_version, task_id)`,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO validation_meta (key, value) VALUES ('schema_version', ?), ('project_id', ?)`,
		version, projectID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO task_validation (
			task_id, observed_head, validator_version, status,
			last_valid_commit, last_valid_generation, last_valid_state,
			validated_commit_count, failure_commit, failure_category, failure_message
		) VALUES (?, 'stale-head', 1, 'valid', 'stale-head', 'stale-generation', x'00', 1, '', '', '')
	`, staleTaskID); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

// TestRecordDeletesFullValidationCommitSetThroughATaskIndex pins the index that
// keeps a full run's per-task DELETE from scanning the whole commit table. The
// primary key leads with validator_version, so without a dedicated index every
// task's DELETE visits every row recorded so far and a full run costs O(tasks^2
// x depth) row visits.
func TestRecordDeletesFullValidationCommitSetThroughATaskIndex(t *testing.T) {
	// Production mutation: dropping the index restores the full-table scan that
	// made the task-count axis superlinear.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())

	rows, err := cache.db.QueryContext(ctx,
		`EXPLAIN QUERY PLAN DELETE FROM validated_commits WHERE validator_version = ? AND task_id = ?`,
		ValidatorVersion, taskID(1),
	)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("query plan is empty")
	}
	for _, detail := range plan {
		if strings.Contains(detail, validatedCommitsByTaskIndex) {
			return
		}
	}
	t.Fatalf("DELETE query plan = %#v, want a search using %s", plan, validatedCommitsByTaskIndex)
}
