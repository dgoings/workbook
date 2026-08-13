package historyvalidation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// withReaderGeneration plays the upgrade this coupling exists for. The constant
// it shadows is a build-time fact, and a test cannot install a second build, so
// the variable is moved and restored around the second open.
func withReaderGeneration(t *testing.T, generation int) {
	t.Helper()
	previous := readerGeneration
	readerGeneration = generation
	t.Cleanup(func() { readerGeneration = previous })
}

// A newer-writer verdict must not survive the upgrade that answers it.
//
// This is the whole failure in one test. A build that folds generation zero
// records the refusal against a task head; the user upgrades to a build that
// folds generation one; and nothing about the task moved, so the row's key —
// head plus validator version — still matches. Without the reading build's
// generation in the cache, `workbook validate` serves that refusal back as a
// cache hit and keeps demanding an upgrade that has already happened, while
// every mutation it refused now succeeds.
//
// The assertion is that the second open does not find the row at all: a cache
// computed under other rules is discarded, so the verdict is re-derived rather
// than believed.
func TestANewerWriterVerdictDoesNotSurviveTheUpgradeThatAnswersIt(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "commit-newer-writer"}

	before, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(before upgrade) error = %v", err)
	}
	if _, err := before.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(before upgrade) error = %v", err)
	}
	if err := before.Record(ctx, Completion{
		TaskID:       head.TaskID,
		ObservedHead: head.ObjectID,
		Status:       StatusInvalid,
		Failure: &Failure{
			TaskID:   head.TaskID,
			Commit:   head.ObjectID,
			Category: string(core.CategoryNewerWriter),
			Message:  "task was written by a newer workbook; upgrade workbook to change it",
		},
		LastValidState: []byte{},
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	// The same build reads its own verdict back, which is the behavior the
	// cache exists for and must keep.
	sameBuild, err := before.Prepare(ctx, []gitstore.TaskHead{head}, false)
	if err != nil {
		t.Fatalf("Prepare(same build) error = %v", err)
	}
	if got := sameBuild[head.TaskID].Status; got != StatusInvalid {
		t.Fatalf("same-build status = %q, want %q; the cache must still serve its own verdict", got, StatusInvalid)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("Close(before upgrade) error = %v", err)
	}

	// The upgrade. Nothing about the task moved.
	withReaderGeneration(t, core.SupportedFormatGeneration+1)

	after, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(after upgrade) error = %v", err)
	}
	t.Cleanup(func() { _ = after.Close() })
	prepared, err := after.Prepare(ctx, []gitstore.TaskHead{head}, false)
	if err != nil {
		t.Fatalf("Prepare(after upgrade) error = %v", err)
	}
	entry := prepared[head.TaskID]
	if entry.Status != StatusPending {
		t.Fatalf("after-upgrade status = %q, want %q; the upgraded build must re-derive the verdict rather than "+
			"serve the refusal the older build cached", entry.Status, StatusPending)
	}
	if entry.Failure != nil {
		t.Fatalf("after-upgrade failure = %+v, want none carried across the upgrade", entry.Failure)
	}
	if after.Path() != filepath.Join(directory, "workbook", cacheFilename) {
		t.Fatalf("cache path = %q, want the shared one; the rebuild must replace the cache in place", after.Path())
	}
}

// The same guard points the other way. A `valid` verdict is a claim that the
// whole chain folded, and a build that folds fewer generations cannot inherit
// it: the chain it would refuse is exactly the chain the newer build passed.
func TestAValidVerdictDoesNotSurviveADowngrade(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	head := gitstore.TaskHead{TaskID: taskID(2), ObjectID: "commit-valid"}

	withReaderGeneration(t, core.SupportedFormatGeneration+1)
	newer, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(newer build) error = %v", err)
	}
	if _, err := newer.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(newer build) error = %v", err)
	}
	if err := newer.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      head.ObjectID,
		LastValidGeneration:  generationID(1),
		LastValidState:       canonicalState(t, head.TaskID, generationID(1), "folded by a newer build"),
		ValidatedCommitIDs:   []string{head.ObjectID},
		ValidatedCommitCount: 1,
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := newer.Close(); err != nil {
		t.Fatalf("Close(newer build) error = %v", err)
	}

	readerGeneration = core.SupportedFormatGeneration
	older, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(older build) error = %v", err)
	}
	t.Cleanup(func() { _ = older.Close() })
	prepared, err := older.Prepare(ctx, []gitstore.TaskHead{head}, false)
	if err != nil {
		t.Fatalf("Prepare(older build) error = %v", err)
	}
	if got := prepared[head.TaskID].Status; got != StatusPending {
		t.Fatalf("downgraded status = %q, want %q; a verdict folded under rules this build lacks must not be inherited",
			got, StatusPending)
	}
}

// An unchanged generation keeps the cache, which is what stops the guard from
// becoming a rebuild on every run.
func TestTheCacheSurvivesWhenTheReaderGenerationIsUnchanged(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	head := gitstore.TaskHead{TaskID: taskID(3), ObjectID: "commit-stable"}

	first, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(first) error = %v", err)
	}
	if _, err := first.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}
	if err := first.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      head.ObjectID,
		LastValidGeneration:  generationID(1),
		LastValidState:       canonicalState(t, head.TaskID, generationID(1), "stable"),
		ValidatedCommitIDs:   []string{head.ObjectID},
		ValidatedCommitCount: 1,
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	second, err := OpenCache(ctx, directory, testConfig())
	if err != nil {
		t.Fatalf("OpenCache(second) error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	prepared, err := second.Prepare(ctx, []gitstore.TaskHead{head}, false)
	if err != nil {
		t.Fatalf("Prepare(second) error = %v", err)
	}
	if got := prepared[head.TaskID].Status; got != StatusValid {
		t.Fatalf("second-run status = %q, want %q; an unchanged generation must not discard the cache", got, StatusValid)
	}
}
