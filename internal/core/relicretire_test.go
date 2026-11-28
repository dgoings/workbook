package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRelicRetireAndUnretireWriteOneOperationEach(t *testing.T) {
	store, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	ctx := context.Background()

	writes := len(store.writes)
	retired, err := service.Retire(ctx, relicID)
	write := requireRelicWritten(t, "retire", store, writes+1, retired, err)
	if got := relicOperationTypes(write.pack); len(got) != 1 || got[0] != RelicOperationRetire {
		t.Fatalf("retire operations = %v", got)
	}
	if write.reason != "retire relic "+relicID || !retired.Relic.Retired {
		t.Fatalf("retire = %q %+v", write.reason, retired.Relic)
	}
	again, err := service.Retire(ctx, relicID)
	if err != nil || again.Changed || !again.Relic.Retired || len(store.writes) != writes+1 {
		t.Fatalf("retire again = %+v, %v after %d writes", again, err, len(store.writes))
	}

	unretired, err := service.Unretire(ctx, relicID)
	write = requireRelicWritten(t, "unretire", store, writes+2, unretired, err)
	if got := relicOperationTypes(write.pack); len(got) != 1 || got[0] != RelicOperationUnretire {
		t.Fatalf("unretire operations = %v", got)
	}
	if write.reason != "unretire relic "+relicID || unretired.Relic.Retired {
		t.Fatalf("unretire = %q %+v", write.reason, unretired.Relic)
	}
	again, err = service.Unretire(ctx, relicID)
	if err != nil || again.Changed || len(store.writes) != writes+2 {
		t.Fatalf("unretire again = %+v, %v after %d writes", again, err, len(store.writes))
	}
}

func TestRelicRetireRefusesADeletedRelic(t *testing.T) {
	store, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	ctx := context.Background()
	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatal(err)
	}
	writes := len(store.writes)
	for label, change := range map[string]func(context.Context, string) (RelicMutationResult, error){
		"retire": service.Retire, "unretire": service.Unretire,
	} {
		_, err := change(ctx, relicID)
		requireRelicCategory(t, label, err, CategoryValidation,
			"relic "+relicID+" is deleted; restore it first with \"workbook relic restore "+relicID+"\"")
	}
	requireRelicWrites(t, "a refused retire", store, writes)
}

func TestRelicListLeavesOutRetiredUnlessAsked(t *testing.T) {
	_, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	ctx := context.Background()
	if _, err := service.Retire(ctx, relicID); err != nil {
		t.Fatal(err)
	}
	for filter, want := range map[RelicListFilter]int{
		{}:                     0,
		{IncludeDeleted: true}: 0,
		{IncludeRetired: true}: 1,
		{IncludeRetired: true, IncludeDeleted: true}: 1,
	} {
		listed, err := service.List(ctx, filter)
		if err != nil || len(listed) != want {
			t.Fatalf("List(%+v) = %+v, %v; want %d", filter, listed, err, want)
		}
	}
	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatal(err)
	}
	if listed, err := service.List(ctx, RelicListFilter{IncludeRetired: true}); err != nil || len(listed) != 0 {
		t.Fatalf("a deleted retired relic is listed with IncludeRetired alone: %+v, %v", listed, err)
	}
}

func TestRelicLinkRemovedAt(t *testing.T) {
	since := time.Date(2026, time.November, 20, 8, 0, 0, 0, time.UTC)
	removed := since.Add(-time.Hour)
	drop := &RelicPartDrop{RemovedAt: removed, DroppedAt: since.Add(time.Hour)}
	deleted := &RelicCompaction{Boundary: RelicBoundaryDeleted, Since: since}
	for label, test := range map[string]struct {
		relic RelicSummary
		drop  *RelicPartDrop
		want  *time.Time
	}{
		"live relic":                {relic: RelicSummary{}},
		"deleted, not compacted":    {relic: RelicSummary{Deleted: true}},
		"compacted deleted":         {relic: RelicSummary{Deleted: true, Compacted: deleted}, want: &since},
		"restored after compaction": {relic: RelicSummary{Compacted: deleted}},
		"a stub":                    {relic: RelicSummary{}, drop: drop, want: &removed},
		"a stub of a deleted relic": {relic: RelicSummary{Deleted: true, Compacted: deleted}, drop: drop, want: &removed},
		"compacted at retired, then deleted": {relic: RelicSummary{Deleted: true,
			Compacted: &RelicCompaction{Boundary: RelicBoundaryRetired, Since: since}}},
	} {
		got := RelicLinkRemovedAt(test.relic, test.drop)
		if (got == nil) != (test.want == nil) || got != nil && !got.Equal(*test.want) {
			t.Errorf("%s: RelicLinkRemovedAt = %v, want %v", label, got, test.want)
		}
	}
}

func TestRelicCreateInputLeavesOutStubs(t *testing.T) {
	at := time.Date(2026, time.November, 20, 8, 0, 0, 0, time.UTC)
	document := RelicDocument{
		Format: RelicDocumentFormat, Version: 1, Title: "Copied", Type: "spec", Retired: true, Deleted: true,
		Compacted: &RelicCompaction{RunID: relicTestID(1), At: at, Boundary: RelicBoundaryRetired, Since: at},
		Parts: []RelicDocumentPart{
			{Type: "note", Removed: true, Dropped: &RelicPartDrop{RemovedAt: at, DroppedAt: at}},
			{Type: "section"},
		},
	}
	input, err := RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Parts) != 1 || input.Parts[0].Type != "section" {
		t.Fatalf("parts = %+v, want only the one with content", input.Parts)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRelicDocument(encoded)
	if err != nil || decoded.Compacted == nil || decoded.Parts[0].Dropped == nil {
		t.Fatalf("DecodeRelicDocument = %+v, %v; want compacted and dropped read back", decoded, err)
	}
}
