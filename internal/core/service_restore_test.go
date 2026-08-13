package core

import (
	"context"
	"testing"
)

// A restore that names no destination writes exactly the pack it has always
// written. The bytes are pinned rather than the operation list because the pack
// is the durable record every clone folds: a second operation, a changed field,
// or a different commit subject would alter what `workbook restore` has meant
// since the verb existed. Naming a destination is an addition, and this is what
// holds it to being one.
func TestRestoreWithoutADestinationWritesThePackItAlwaysHas(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7D3"}})

	if _, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{}); err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("RestoreMutation() wrote %d packs, want %d", got, want)
	}
	encoded, err := EncodeDocument(store.writes[0].pack)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	const want = `{"format":"workbook.operation-pack","version":1,` +
		`"projectId":"01K0M6B8A4FTT8C39MXXYTW7D0","taskId":"WB-01K0M6B8A4FTT8C39MXXYTW7D1",` +
		`"historyGeneration":"01K0M6B8A4FTT8C39MXXYTW7D9","actor":{"id":"developer@example.com"},` +
		`"logicalClock":2,"wallTime":"2026-07-23T15:00:00Z",` +
		`"operations":[{"id":"01K0M6B8A4FTT8C39MXXYTW7D3","type":"task.restore"}]}` + "\n"
	if got := string(encoded); got != want {
		t.Fatalf("restore pack = %s, want %s", got, want)
	}
	if got, want := store.writes[0].reason, "restore task"; got != want {
		t.Fatalf("restore commit subject = %q, want %q", got, want)
	}
}

// The destination rides in the same pack as the restore, so the task is never
// briefly visible in the column it was deleted from and a reader sees one
// change rather than two.
func TestRestoreIntoAStatusWritesOnePackCarryingBoth(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
	}})

	result, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: StatusInProgress})
	if err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("RestoreMutation() wrote %d packs, want %d", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskRestore},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E2", Type: OperationFieldSet, Field: "status", Value: "in-progress"},
	})
	if result.Task.Deleted {
		t.Fatal("RestoreMutation() task is still tombstoned")
	}
	if got, want := result.Task.Status, StatusInProgress; got != want {
		t.Fatalf("RestoreMutation() status = %q, want %q", got, want)
	}
	if result.StatusCorrected != nil {
		t.Fatalf("StatusCorrected = %#v, want none for a destination the caller chose", result.StatusCorrected)
	}
}

// Restoring into the status the task already stores is a bare restore with a
// destination spelled out, and writes the same pack as one. Place skips a
// status operation it does not need for the same reason: a pack should record
// the change, not the request.
func TestRestoreIntoTheStoredStatusWritesNoStatusOperation(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}})

	if _, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: StatusBacklog}); err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskRestore},
	})
}

// Restoring a task into the status its stale token already resolves to writes
// the settlement and reports it as a correction rather than as a move, which is
// exactly what Place does with the same request.
func TestRestoreIntoAResolvedStatusReportsASettlement(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Deleted", Status: "shipped", Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(snapshot)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
	}}, customVocabulary(t))

	result, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: "released"})
	if err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	if result.StatusCorrected == nil {
		t.Fatal("RestoreMutation() reported no status correction, want one")
	}
	if got, want := *result.StatusCorrected, (StatusCorrection{From: "shipped", To: "released"}); got != want {
		t.Fatalf("StatusCorrected = %#v, want %#v", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskRestore},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E2", Type: OperationFieldSet, Field: "status", Value: "released"},
	})

	// A genuine move out of the stale token is not a settlement, however stale
	// the token it started from.
	moved := vocabularyServiceUnderTest(newMemoryTaskStore(snapshot), &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E3",
		"01K0M6B8A4FTT8C39MXXYTW7E4",
	}}, customVocabulary(t))
	result, err = moved.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: "triage"})
	if err != nil {
		t.Fatalf("RestoreMutation(triage) error = %v", err)
	}
	if result.StatusCorrected != nil {
		t.Fatalf("StatusCorrected = %#v, want none", result.StatusCorrected)
	}
}

// A destination is a status a person is choosing, so it is refused exactly
// where every other chosen status is: a name the project removed through its
// ledger is not somewhere a task may be restored to, and the refusal reads the
// same as `workbook update --status` naming it.
func TestRestoreRefusesADestinationTheProjectDoesNotDefine(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Deleted", Status: "triage", Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(snapshot)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	// `blocked` was removed into `triage`, so it resolves for a task that still
	// stores it and is still no place to put one deliberately.
	_, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: "blocked"})
	if err == nil {
		t.Fatal("RestoreMutation() error = nil, want the removed status refused")
	}
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("RestoreMutation() category = %q, want %q; error = %v", got, want, err)
	}
	if got, want := err.Error(), `invalid task status "blocked"`; got != want {
		t.Fatalf("RestoreMutation() error = %q, want %q", got, want)
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("RestoreMutation() wrote %d packs, want none", got)
	}
}

// The anchor rules are Place's, because a restore with an anchor is a placement
// that happens to start from a tombstone. Each of these leaves the task
// tombstoned: a refusal must not restore a task somewhere nobody asked for.
func TestRestoreValidatesItsAnchors(t *testing.T) {
	const restoredID = "WB-01K0M6B8A4FTT8C39MXXYTW7F1"
	const anchorID = "WB-01K0M6B8A4FTT8C39MXXYTW7F2"
	const elsewhereID = "WB-01K0M6B8A4FTT8C39MXXYTW7F3"
	const tombstonedAnchorID = "WB-01K0M6B8A4FTT8C39MXXYTW7F4"
	const otherPriorityID = "WB-01K0M6B8A4FTT8C39MXXYTW7F5"

	tests := map[string]struct {
		input RestoreInput
		want  string
	}{
		"two directions": {
			input: RestoreInput{Into: StatusReady, Before: anchorID, After: anchorID},
			want:  "restore accepts at most one anchor direction",
		},
		"anchor without a destination": {
			input: RestoreInput{Before: anchorID},
			want:  "restore anchors require a destination status",
		},
		"tombstoned anchor": {
			input: RestoreInput{Into: StatusReady, Before: tombstonedAnchorID},
			want:  "restore anchor must be an active different task in the destination status and priority bucket",
		},
		"the task itself": {
			input: RestoreInput{Into: StatusReady, Before: restoredID},
			want:  "restore anchor must be an active different task in the destination status and priority bucket",
		},
		"anchor in another column": {
			input: RestoreInput{Into: StatusReady, Before: elsewhereID},
			want:  "restore anchor must be an active different task in the destination status and priority bucket",
		},
		"anchor at another priority": {
			input: RestoreInput{Into: StatusReady, After: otherPriorityID},
			want:  "restore anchor must be an active different task in the destination status and priority bucket",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newMemoryTaskStore(
				serviceSnapshot(restoredID, TaskData{
					Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
				}),
				serviceSnapshot(anchorID, TaskData{
					Title: "Anchor", Status: StatusReady, Priority: PriorityMedium, Rank: "2/1",
				}),
				serviceSnapshot(elsewhereID, TaskData{
					Title: "Another column", Status: StatusDone, Priority: PriorityMedium, Rank: "3/1",
				}),
				serviceSnapshot(tombstonedAnchorID, TaskData{
					Title: "Also deleted", Status: StatusReady, Priority: PriorityMedium, Rank: "4/1", Deleted: true,
				}),
				serviceSnapshot(otherPriorityID, TaskData{
					Title: "Same column, other lane", Status: StatusReady, Priority: PriorityHigh, Rank: "5/1",
				}),
			)
			service := serviceUnderTest(store, &sequenceIDSource{})

			_, err := service.RestoreMutation(context.Background(), restoredID, test.input)
			if err == nil {
				t.Fatal("RestoreMutation() error = nil, want a refusal")
			}
			if got, want := CategoryOf(err), CategoryValidation; got != want {
				t.Fatalf("RestoreMutation() category = %q, want %q; error = %v", got, want, err)
			}
			if got := err.Error(); got != test.want {
				t.Fatalf("RestoreMutation() error = %q, want %q", got, test.want)
			}
			if got := len(store.writes); got != 0 {
				t.Fatalf("RestoreMutation() wrote %d packs, want none", got)
			}
		})
	}
}

// The board reaches Place and Restore with the same drag, so a request that is
// wrong in two ways at once has to be refused for the same one by both. Restore
// checks membership before its anchors for exactly this reason.
func TestRestoreAndPlaceRefuseAMalformedRequestTheSameWay(t *testing.T) {
	deletedID := "WB-01K0M6B8A4FTT8C39MXXYTW7F1"
	activeID := "WB-01K0M6B8A4FTT8C39MXXYTW7F2"
	store := newMemoryTaskStore(
		serviceSnapshot(deletedID, TaskData{
			Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
		}),
		serviceSnapshot(activeID, TaskData{
			Title: "Active", Status: StatusBacklog, Priority: PriorityMedium, Rank: "2/1",
		}),
	)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, placeErr := service.PlaceMutation(context.Background(), activeID, PlaceInput{
		Status: "nope", Before: activeID, After: deletedID,
	})
	_, restoreErr := service.RestoreMutation(context.Background(), deletedID, RestoreInput{
		Into: "nope", Before: activeID, After: deletedID,
	})
	if placeErr == nil || restoreErr == nil {
		t.Fatalf("PlaceMutation() error = %v and RestoreMutation() error = %v, want both refused", placeErr, restoreErr)
	}
	if got, want := restoreErr.Error(), placeErr.Error(); got != want {
		t.Fatalf("RestoreMutation() error = %q, want Place's %q", got, want)
	}
	if got, want := restoreErr.Error(), `invalid task status "nope"`; got != want {
		t.Fatalf("refusal = %q, want the status refused before the anchors, %q", got, want)
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("the two refusals wrote %d packs, want none", got)
	}
}

// An anchored restore computes its rank the way Move and Place do, in the
// destination bucket, so a card dropped between two others comes back between
// them.
func TestRestoreWithAnAnchorRanksInTheDestinationBucket(t *testing.T) {
	const restoredID = "WB-01K0M6B8A4FTT8C39MXXYTW7F1"
	const anchorID = "WB-01K0M6B8A4FTT8C39MXXYTW7F2"
	const neighborID = "WB-01K0M6B8A4FTT8C39MXXYTW7F3"
	store := newMemoryTaskStore(
		serviceSnapshot(restoredID, TaskData{
			Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "9/1", Deleted: true,
		}),
		serviceSnapshot(anchorID, TaskData{
			Title: "Anchor", Status: StatusReady, Priority: PriorityMedium, Rank: "2/1",
		}),
		serviceSnapshot(neighborID, TaskData{
			Title: "Neighbour", Status: StatusReady, Priority: PriorityMedium, Rank: "6/1",
		}),
	)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
		"01K0M6B8A4FTT8C39MXXYTW7E3",
	}})

	result, err := service.RestoreMutation(context.Background(), restoredID, RestoreInput{
		Into:  StatusReady,
		After: anchorID,
	})
	if err != nil {
		t.Fatalf("RestoreMutation() error = %v", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskRestore},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E2", Type: OperationFieldSet, Field: "status", Value: "ready"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E3", Type: OperationFieldSet, Field: "rank", Value: "4/1"},
	})
	if got, want := result.Task.Rank, "4/1"; got != want {
		t.Fatalf("RestoreMutation() rank = %q, want %q, between the anchor and its neighbour", got, want)
	}
}

// Without an anchor the rank is untouched, even when the destination bucket
// already holds tasks. Place refuses that request, because a placement is a
// caller stating where in a column a task goes; a restore states only which
// column, and lands where its old rank puts it — which is what `update
// --status` has always done.
func TestRestoreWithoutAnAnchorLeavesTheRankAlone(t *testing.T) {
	const restoredID = "WB-01K0M6B8A4FTT8C39MXXYTW7F1"
	const occupantID = "WB-01K0M6B8A4FTT8C39MXXYTW7F2"
	store := newMemoryTaskStore(
		serviceSnapshot(restoredID, TaskData{
			Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "9/1", Deleted: true,
		}),
		serviceSnapshot(occupantID, TaskData{
			Title: "Already there", Status: StatusReady, Priority: PriorityMedium, Rank: "2/1",
		}),
	)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
	}})

	result, err := service.RestoreMutation(context.Background(), restoredID, RestoreInput{Into: StatusReady})
	if err != nil {
		t.Fatalf("RestoreMutation() error = %v, want an anchorless restore into an occupied column accepted", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskRestore},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E2", Type: OperationFieldSet, Field: "status", Value: "ready"},
	})
	if got, want := result.Task.Rank, "9/1"; got != want {
		t.Fatalf("RestoreMutation() rank = %q, want the rank it was tombstoned with, %q", got, want)
	}
}

// A restore is a write like any other, so a client that names the tip it
// rendered is told when that tip has moved. It is honored whether or not the
// restore names a destination: a queued restore is stale for the same reason
// either way.
func TestRestoreHonorsAnExpectedHead(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Deleted", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})

	for name, input := range map[string]RestoreInput{
		"bare":               {ExpectedHead: "0000000000000000000000000000000000000000"},
		"with a destination": {Into: StatusReady, ExpectedHead: "0000000000000000000000000000000000000000"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryTaskStore(snapshot)
			service := serviceUnderTest(store, &sequenceIDSource{})

			_, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, input)
			if got, want := CategoryOf(err), CategoryStaleWrite; got != want {
				t.Fatalf("RestoreMutation() category = %q, want %q; error = %v", got, want, err)
			}
			if got := len(store.writes); got != 0 {
				t.Fatalf("RestoreMutation() wrote %d packs, want none", got)
			}
		})
	}

	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}})
	if _, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{
		ExpectedHead: snapshot.Head,
	}); err != nil {
		t.Fatalf("RestoreMutation() error = %v, want the current tip accepted", err)
	}
}

// A destination does not make an active task restorable, and the refusal is the
// one it always was.
func TestRestoreIntoAStatusStillRefusesAnActiveTask(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Active", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{})

	_, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID, RestoreInput{Into: StatusReady})
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("RestoreMutation() category = %q, want %q; error = %v", got, want, err)
	}
	if got, want := err.Error(), "cannot restore an active task"; got != want {
		t.Fatalf("RestoreMutation() error = %q, want %q", got, want)
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("RestoreMutation() wrote %d packs, want none", got)
	}
}

// A delete may be conditioned on the tip the caller rendered, for the reason
// every other mutation may: a queued intent from a board that has since been
// overtaken should be refused rather than applied to state nobody looked at.
func TestDeleteHonorsAnExpectedHead(t *testing.T) {
	snapshot := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7D1", TaskData{
		Title: "Active", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(snapshot)
	service := serviceUnderTest(store, &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}})

	_, err := service.DeleteMutation(context.Background(), snapshot.State.TaskID, DeleteInput{
		ExpectedHead: "0000000000000000000000000000000000000000",
	})
	if got, want := CategoryOf(err), CategoryStaleWrite; got != want {
		t.Fatalf("DeleteMutation() category = %q, want %q; error = %v", got, want, err)
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("DeleteMutation() wrote %d packs, want none", got)
	}

	if _, err := service.DeleteMutation(context.Background(), snapshot.State.TaskID, DeleteInput{
		ExpectedHead: snapshot.Head,
	}); err != nil {
		t.Fatalf("DeleteMutation() error = %v, want the current tip accepted", err)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationTaskTombstone},
	})
}
