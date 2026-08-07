package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// Reading and classifying objects is covered against both object formats, but
// every fetch, push, sync, and reconcile test ran SHA-1 only, so nothing
// exercised the transport, the ref compare-and-swap, or the replay against
// 64-character object IDs. A hard-coded 40-character assumption anywhere on
// that path — a prefix length, a fixed-width parse, a same-length comparison —
// survives the rest of this package untouched.
func TestSyncFetchesReplaysAndConflictsThroughASHA256Origin(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositoriesWithObjectFormat(t, testrepo.FormatSHA256)
	for name, repo := range map[string]*Repository{"first": first, "second": second} {
		if got := syncGit(t, repo.Root, "rev-parse", "--show-object-format"); got != testrepo.FormatSHA256 {
			t.Fatalf("%s clone object format = %q, want %q", name, got, testrepo.FormatSHA256)
		}
	}

	// Push, not a raw refspec: the publishing half of the round trip has to
	// cross the transport in this format too.
	shared := createSyncTask(t, first, config, "SHA-256 shared task")
	pushed, err := first.Push(ctx, config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, pushed)
	}
	assertSyncOutcome(t, pushed, shared.ID, SyncPublished)

	fetched, err := second.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(new task) error = %v; result = %#v", err, fetched)
	}
	assertSyncOutcome(t, fetched, shared.ID, SyncCreated)
	head := refValue(t, second, taskRefPrefix+shared.ID)
	if len(head) != 64 || strings.TrimLeft(head, "0123456789abcdef") != "" {
		t.Fatalf("fetched tip = %q, want a 64-character SHA-256 object ID", head)
	}

	// Divergent local work replays onto the fetched tip rather than being lost
	// or fast-forwarded away, and the two clones agree afterwards.
	updateSyncTask(t, first, config, shared.ID, "Remote title")
	if _, err := first.Push(ctx, config); err != nil {
		t.Fatal(err)
	}
	setSyncTaskPriority(t, second, config, shared.ID, core.PriorityHigh)
	remoteTip := refValue(t, first, taskRefPrefix+shared.ID)

	run, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync(divergent) error = %v; result = %#v", err, run)
	}
	assertSyncOutcome(t, run.Fetch, shared.ID, SyncReconciled)
	assertSyncOutcome(t, run.Push, shared.ID, SyncPublished)
	replayed, err := second.Get(ctx, config, shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.State.Task.Title != "Remote title" || replayed.State.Task.Priority != core.PriorityHigh {
		t.Fatalf("replayed task = %#v, want the remote title and the local priority", replayed.State.Task)
	}
	if !mergeBaseIsAncestor(t, second.Root, remoteTip, replayed.Head) {
		t.Fatalf("replayed tip %s does not descend from the fetched tip %s", replayed.Head, remoteTip)
	}
	if got := remoteRefValue(t, second, taskRefPrefix+shared.ID); got != replayed.Head {
		t.Fatalf("published tip = %q, want the replayed tip %q", got, replayed.Head)
	}

	// A conflict still stops the replay at the fetched tip, so the refusal path
	// reads and writes SHA-256 refs as well as the accepting one.
	setSyncTaskDescription(t, first, config, shared.ID, "Their text")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	setSyncTaskDescription(t, second, config, shared.ID, "Our text")
	conflictTip := refValue(t, first, taskRefPrefix+shared.ID)

	result, err := second.Fetch(ctx, config)
	if core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("Fetch(description conflict) error = %v, want a conflict; result = %#v", err, result)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Type != core.ConflictDescription {
		t.Fatalf("conflicts = %#v, want exactly one description conflict", result.Conflicts)
	}
	if got := refValue(t, second, taskRefPrefix+shared.ID); got != conflictTip {
		t.Fatalf("conflicted tip = %q, want the fetched tip %q", got, conflictTip)
	}
}
