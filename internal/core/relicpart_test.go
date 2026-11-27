package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

// partReadCountingReader records what a read asks of the reader.
type partReadCountingReader struct {
	*memoryRelicStore
	gets      int
	partReads [][]string
	bodies    []bool
}

func (r *partReadCountingReader) GetRelic(ctx context.Context, config ProjectConfig, relicID string) (RelicSnapshot, error) {
	r.gets++
	return r.memoryRelicStore.GetRelic(ctx, config, relicID)
}

func (r *partReadCountingReader) ReadRelicTips(context.Context, ProjectConfig, []RelicHead) ([]RelicSnapshot, error) {
	return nil, Errorf(CategoryOperational, "ShowPart read every relic's tip")
}

func (r *partReadCountingReader) ReadRelicParts(
	ctx context.Context, config ProjectConfig, snapshot RelicSnapshot, partIDs []string, bodies bool,
) (map[string]RelicPartContent, error) {
	r.partReads = append(r.partReads, append([]string(nil), partIDs...))
	r.bodies = append(r.bodies, bodies)
	return r.memoryRelicStore.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
}

// unreadableTasks and unreadableSchemas panic on any call: their interfaces
// are nil, so a read that reaches one of them fails the test.
type unreadableTasks struct{ TaskReader }

type unreadableSchemas struct{ RelicSchemaStore }

func TestShowPartReadsOnePartAndNothingElse(t *testing.T) {
	store, _, service, versions := newSchemaTestService(t)
	ctx := context.Background()
	binary := []byte{0xff, 0xfe, 0x00}
	created, err := service.Create(ctx, RelicCreateInput{
		Title:      "Compaction",
		Type:       "spec",
		Schema:     versions.spec1.ID,
		Properties: map[string]json.RawMessage{"status": relicRawString("draft")},
		Parts: []RelicPartInput{
			{
				Type:   "section",
				Fields: map[string]json.RawMessage{"heading": relicRawString("Goals")},
				Body:   &RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
			},
			{
				Type:   "section",
				Fields: map[string]json.RawMessage{"heading": relicRawString("Data")},
				Body:   &RelicBodyInput{Format: "bin", Content: binary},
			},
			{Type: "section", Fields: map[string]json.RawMessage{"heading": relicRawString("Empty")}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	relicID := created.Relic.ID
	first, second, third := created.Parts[0].ID, created.Parts[1].ID, created.Parts[2].ID
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: third}}); err != nil {
		t.Fatalf("remove part: %v", err)
	}
	// A link to a task, which a show of the part's relic would look up.
	snapshot := store.snapshots[relicID]
	snapshot.Record.Links.Links = append(snapshot.Record.Links.Links, RelicLink{
		ID: relicTestID(7301), From: first, To: RelicLinkTarget{Task: "WB-" + relicTestID(7302)}, Rel: "relates-to",
	})
	store.snapshots[relicID] = snapshot

	reader := &partReadCountingReader{memoryRelicStore: store}
	service.Reader = reader
	service.Tasks = unreadableTasks{}
	service.Schemas = unreadableSchemas{}
	read := func(address, wantPart string) RelicPartView {
		t.Helper()
		reader.gets, reader.partReads, reader.bodies = 0, nil, nil
		view, err := service.ShowPart(ctx, address)
		if err != nil {
			t.Fatalf("ShowPart(%q): %v", address, err)
		}
		if view.Relic != relicID || view.Head != snapshot.Head || view.Part.ID != wantPart {
			t.Fatalf("ShowPart(%q) = relic %q head %q part %q, want %q, %q, %q",
				address, view.Relic, view.Head, view.Part.ID, relicID, snapshot.Head, wantPart)
		}
		if reader.gets != 1 || !reflect.DeepEqual(reader.partReads, [][]string{{wantPart}}) ||
			!reflect.DeepEqual(reader.bodies, []bool{true}) {
			t.Fatalf("ShowPart(%q) made %d relic reads and part reads %v with bodies %v, want one of each for %s",
				address, reader.gets, reader.partReads, reader.bodies, wantPart)
		}
		return view
	}

	// A prefix of each names the relic and the part. The IDs the test source
	// mints differ only near the end, so drop one character.
	view := read(relicID[:len(relicID)-1]+"#"+first, first)
	if view.Part.Type != "section" || string(view.Part.Fields["heading"]) != `"Goals"` {
		t.Errorf("part = %#v, want the Goals section", view.Part)
	}
	if body := view.Part.Body; body == nil || body.Format != "md" || body.Bytes != 8 || body.Version != first ||
		body.Text == nil || *body.Text != "# Goals\n" || body.Base64 != nil {
		t.Errorf("body = %#v, want the md text", view.Part.Body)
	}

	view = read(relicID+"#"+second, second)
	if body := view.Part.Body; body == nil || body.Text != nil || body.Base64 == nil ||
		*body.Base64 != base64.StdEncoding.EncodeToString(binary) {
		t.Errorf("binary body = %#v, want base64", view.Part.Body)
	}

	view = read(relicID+"#"+third, third)
	if !view.Part.Removed || view.Part.Body != nil || string(view.Part.Fields["heading"]) != `"Empty"` {
		t.Errorf("removed part = %#v, want it read, without a body", view.Part)
	}
}

func TestShowPartResolvesAPartPrefix(t *testing.T) {
	store := newMemoryRelicStore()
	partID := relicTestID(11)
	snapshot := relicSnapshotForTest(t, 1, "spec", []string{partID}, false, false)
	store.put(snapshot)
	service := newRelicTestService(store, nil)

	view, err := service.ShowPart(context.Background(), snapshot.Record.State.RelicID+"#"+partID[:len(partID)-2])
	if err != nil || view.Part.ID != partID {
		t.Fatalf("ShowPart by part prefix = %#v, %v; want part %s", view, err, partID)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"relic", "head", "part"} {
		if _, ok := members[name]; !ok {
			t.Errorf("view JSON %s has no %q member", encoded, name)
		}
	}
}

func TestShowPartRefusesWhatNamesNoPart(t *testing.T) {
	store := newMemoryRelicStore()
	partID := relicTestID(11)
	snapshot := relicSnapshotForTest(t, 1, "spec", []string{partID}, false, false)
	store.put(snapshot)
	service := newRelicTestService(store, nil)
	ctx := context.Background()
	relicID := snapshot.Record.State.RelicID

	_, err := service.ShowPart(ctx, relicID)
	requireRelicCategory(t, "no part", err, CategoryInvocation, "a part address is <relic>#<part>")
	_, err = service.ShowPart(ctx, relicID+"#")
	requireRelicCategory(t, "empty part", err, CategoryValidation)
	_, err = service.ShowPart(ctx, relicID+"#"+relicTestID(99))
	requireRelicCategory(t, "unknown part", err, CategoryNotFound, relicID)
	_, err = service.ShowPart(ctx, "WB-R-NOPE#"+partID)
	requireRelicCategory(t, "unknown relic", err, CategoryNotFound)
}
