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

	if _, err := service.RestoreMutation(context.Background(), snapshot.State.TaskID); err != nil {
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
