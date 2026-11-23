package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var relicServiceTestTime = time.Date(2026, time.November, 23, 10, 0, 0, 0, time.UTC)

const relicServiceTestActor = "dylan@example.com"

// memoryRelicStore is an in-memory RelicReader and RelicWriter. It checks each
// write against the fold the way the Git store does, and keeps each part's
// content as of the relic's tip.
type memoryRelicStore struct {
	snapshots map[string]RelicSnapshot
	content   map[string]map[string]RelicPartContent
	writes    []memoryRelicWrite
}

type memoryRelicWrite struct {
	parent  *RelicSnapshot
	pack    RelicOperationPack
	record  RelicRecord
	content map[string]RelicPartContent
	reason  string
}

func newMemoryRelicStore() *memoryRelicStore {
	return &memoryRelicStore{
		snapshots: map[string]RelicSnapshot{},
		content:   map[string]map[string]RelicPartContent{},
	}
}

// put stores a snapshot built outside the service, with empty fields for each
// of its parts.
func (s *memoryRelicStore) put(snapshot RelicSnapshot) {
	relicID := snapshot.Record.State.RelicID
	s.snapshots[relicID] = snapshot
	parts := map[string]RelicPartContent{}
	for _, part := range snapshot.Record.State.Relic.Parts {
		parts[part.ID] = RelicPartContent{Fields: map[string]json.RawMessage{}}
	}
	s.content[relicID] = parts
}

func (s *memoryRelicStore) ListRelicHeads(context.Context, ProjectConfig) ([]RelicHead, error) {
	heads := make([]RelicHead, 0, len(s.snapshots))
	for relicID, snapshot := range s.snapshots {
		heads = append(heads, RelicHead{RelicID: relicID, ObjectID: snapshot.Head})
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].RelicID < heads[j].RelicID })
	return heads, nil
}

func (s *memoryRelicStore) ReadRelicTips(_ context.Context, _ ProjectConfig, heads []RelicHead) ([]RelicSnapshot, error) {
	snapshots := make([]RelicSnapshot, 0, len(heads))
	for _, head := range heads {
		snapshot, ok := s.snapshots[head.RelicID]
		if !ok || snapshot.Head != head.ObjectID {
			return nil, Errorf(CategoryCorruptData, "relic %s has no tip %s", head.RelicID, head.ObjectID)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *memoryRelicStore) GetRelic(_ context.Context, _ ProjectConfig, relicID string) (RelicSnapshot, error) {
	snapshot, ok := s.snapshots[relicID]
	if !ok {
		return RelicSnapshot{}, Errorf(CategoryNotFound, "relic %q was not found", relicID)
	}
	return snapshot, nil
}

func (s *memoryRelicStore) ReadRelicParts(
	_ context.Context,
	_ ProjectConfig,
	snapshot RelicSnapshot,
	partIDs []string,
	bodies bool,
) (map[string]RelicPartContent, error) {
	relicID := snapshot.Record.State.RelicID
	contents := make(map[string]RelicPartContent, len(partIDs))
	for _, partID := range partIDs {
		stored, ok := s.content[relicID][partID]
		if !ok {
			return nil, Errorf(CategoryNotFound, "relic %s has no part %s", relicID, partID)
		}
		content := RelicPartContent{Fields: stored.Fields}
		if bodies && stored.HasBody {
			content.Body, content.HasBody = stored.Body, true
		}
		contents[partID] = content
	}
	return contents, nil
}

func (s *memoryRelicStore) WriteRelic(
	_ context.Context,
	_ ProjectConfig,
	parent *RelicSnapshot,
	pack RelicOperationPack,
	record RelicRecord,
	content map[string]RelicPartContent,
	reason string,
) (RelicSnapshot, error) {
	current, exists := s.snapshots[pack.RelicID]
	switch {
	case parent == nil && exists, parent != nil && (!exists || current.Head != parent.Head):
		return RelicSnapshot{}, Errorf(CategoryStaleWrite, "relic ref changed concurrently")
	}
	var parentRecord *RelicRecord
	if parent != nil {
		parentRecord = &parent.Record
	}
	if err := ValidateRelicCheckpoint(parentRecord, pack, record); err != nil {
		return RelicSnapshot{}, err
	}
	s.writes = append(s.writes, memoryRelicWrite{parent: parent, pack: pack, record: record, content: content, reason: reason})
	snapshot := RelicSnapshot{Head: fmt.Sprintf("head-%d", len(s.writes)), Operation: pack, Record: record}
	s.snapshots[pack.RelicID] = snapshot
	parts := s.content[pack.RelicID]
	if parts == nil {
		parts = map[string]RelicPartContent{}
	}
	for partID, given := range content {
		if given.Fields == nil {
			given.Fields = map[string]json.RawMessage{}
		}
		parts[partID] = given
	}
	s.content[pack.RelicID] = parts
	return snapshot, nil
}

// relicServiceIDs returns count canonical ULIDs numbered from start.
func relicServiceIDs(start, count int) []string {
	ids := make([]string, count)
	for index := range ids {
		ids[index] = relicTestID(start + index)
	}
	return ids
}

func newRelicTestService(store *memoryRelicStore, ids []string) RelicService {
	return RelicService{
		Config: serviceTestConfig,
		Reader: store,
		Writer: store,
		IDs:    &sequenceIDSource{values: ids},
		Now:    func() time.Time { return relicServiceTestTime },
		Actor:  relicServiceTestActor,
	}
}

func relicRawString(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// relicSnapshotForTest folds a relic outside the service: a root pack with
// parts of the given IDs, then, when removed or deleted is set, a second pack
// that removes the first part or tombstones the relic.
func relicSnapshotForTest(t *testing.T, number int, relicType string, partIDs []string, removeFirst, deleted bool) RelicSnapshot {
	t.Helper()
	relicID := NewRelicID(serviceTestConfig.Key, relicTestID(700000+number))
	generation := relicTestID(710000 + number)
	operations := []RelicOperation{{
		ID:    relicTestID(720000 + number),
		Type:  RelicOperationCreate,
		Relic: &RelicCreateData{Title: fmt.Sprintf("Relic %d", number), Type: relicType, CreatedAt: relicServiceTestTime},
	}}
	for index, partID := range partIDs {
		operations = append(operations, RelicOperation{
			ID: partID, Type: RelicOperationPartAdd, PartType: "section", Position: fmt.Sprintf("%d/1", index+1),
		})
	}
	pack := NewRelicOperationPack(serviceTestConfig.ProjectID, relicID, generation, relicServiceTestActor, 1,
		relicServiceTestTime, operations)
	record, err := ApplyRelic(nil, pack)
	if err != nil {
		t.Fatalf("ApplyRelic(root %d): %v", number, err)
	}
	var follow []RelicOperation
	if removeFirst {
		follow = append(follow, RelicOperation{ID: relicTestID(730000 + number), Type: RelicOperationPartRemove, Part: partIDs[0]})
	}
	if deleted {
		follow = append(follow, RelicOperation{ID: relicTestID(740000 + number), Type: RelicOperationTombstone})
	}
	if len(follow) != 0 {
		pack = NewRelicOperationPack(serviceTestConfig.ProjectID, relicID, generation, relicServiceTestActor, 2,
			relicServiceTestTime.Add(time.Minute), follow)
		if record, err = ApplyRelic(&record, pack); err != nil {
			t.Fatalf("ApplyRelic(child %d): %v", number, err)
		}
	}
	return RelicSnapshot{Head: fmt.Sprintf("fixture-%d", number), Operation: pack, Record: record}
}

func requireRelicCategory(t *testing.T, label string, err error, want Category, fragments ...string) {
	t.Helper()
	if got := CategoryOf(err); got != want {
		t.Fatalf("%s: category = %q, want %q; error = %v", label, got, want, err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("%s: error %q does not mention %q", label, err, fragment)
		}
	}
}

func relicCreateInputForTest() RelicCreateInput {
	return RelicCreateInput{
		Title: "  Task archive compaction ",
		Type:  "spec",
		Properties: map[string]json.RawMessage{
			"status": relicRawString("approved"),
			"owner":  json.RawMessage(`{"name": "dylan"}`),
		},
		Parts: []RelicPartInput{
			{
				Type:   "section",
				Fields: map[string]json.RawMessage{"heading": relicRawString("Goals")},
				Body:   &RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
			},
			{Type: "decision"},
			{Type: "note", Body: &RelicBodyInput{Content: []byte{}}},
		},
	}
}

func TestRelicCreateBuildsOneRootPack(t *testing.T) {
	store := newMemoryRelicStore()
	ids := relicServiceIDs(1, 8)
	service := newRelicTestService(store, ids)

	result, err := service.Create(context.Background(), relicCreateInputForTest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(store.writes))
	}
	write := store.writes[0]
	relicID := serviceTestConfig.Key + "-R-" + ids[0]
	if write.parent != nil {
		t.Errorf("create wrote with a parent")
	}
	pack := write.pack
	if pack.RelicID != relicID || pack.HistoryGeneration != ids[1] || pack.LogicalClock != 1 {
		t.Errorf("pack relic, generation, clock = %s, %s, %d; want %s, %s, 1",
			pack.RelicID, pack.HistoryGeneration, pack.LogicalClock, relicID, ids[1])
	}
	if pack.Actor.ID != relicServiceTestActor || write.record.State.Relic.Author != relicServiceTestActor {
		t.Errorf("actor = %q, author = %q; want %q", pack.Actor.ID, write.record.State.Relic.Author, relicServiceTestActor)
	}
	if write.reason != "create relic "+relicID {
		t.Errorf("reason = %q", write.reason)
	}
	wantTypes := []RelicOperationType{
		RelicOperationCreate, RelicOperationPropertySet, RelicOperationPropertySet,
		RelicOperationPartAdd, RelicOperationPartAdd, RelicOperationPartAdd,
	}
	if len(pack.Operations) != len(wantTypes) {
		t.Fatalf("operations = %d, want %d", len(pack.Operations), len(wantTypes))
	}
	for index, operation := range pack.Operations {
		if operation.Type != wantTypes[index] {
			t.Errorf("operation %d type = %s, want %s", index, operation.Type, wantTypes[index])
		}
		if operation.ID != ids[index+2] {
			t.Errorf("operation %d ID = %s, want %s", index, operation.ID, ids[index+2])
		}
	}
	if create := pack.Operations[0].Relic; create.Title != "Task archive compaction" || create.Type != "spec" ||
		!create.CreatedAt.Equal(relicServiceTestTime) {
		t.Errorf("relic.create = %+v", create)
	}
	if pack.Operations[1].Name != "owner" || pack.Operations[2].Name != "status" {
		t.Errorf("property order = %s, %s; want owner, status", pack.Operations[1].Name, pack.Operations[2].Name)
	}
	if got := string(pack.Operations[1].Value); got != `{"name":"dylan"}` {
		t.Errorf("owner value = %s, want the canonical form", got)
	}
	parts := write.record.State.Relic.Parts
	if len(parts) != 3 {
		t.Fatalf("state parts = %d, want 3", len(parts))
	}
	for index, operation := range pack.Operations[3:] {
		if want := fmt.Sprintf("%d/1", index+1); operation.Position != want {
			t.Errorf("part %d position = %s, want %s", index, operation.Position, want)
		}
		part := requirePartForTest(t, write.record.State.Relic, operation.ID)
		if part.Body != nil && part.Body.Version != operation.ID {
			t.Errorf("part %d body version = %s, want %s", index, part.Body.Version, operation.ID)
		}
	}
	first, second, third := pack.Operations[3], pack.Operations[4], pack.Operations[5]
	if first.Body == nil || first.Body.Format != "md" || first.Body.Bytes != 8 || second.Body != nil ||
		third.Body == nil || third.Body.Bytes != 0 {
		t.Errorf("part.add bodies = %+v, %+v, %+v", first.Body, second.Body, third.Body)
	}
	if got := write.content[first.ID]; !bytes.Equal(got.Body, []byte("# Goals\n")) || !got.HasBody ||
		string(got.Fields["heading"]) != `"Goals"` {
		t.Errorf("first part content = %+v", got)
	}
	if got := write.content[third.ID]; !got.HasBody || len(got.Body) != 0 {
		t.Errorf("empty body content = %+v", got)
	}
	if got := write.content[second.ID]; got.HasBody {
		t.Errorf("part without a body has content %+v", got)
	}

	if !result.Changed || result.Relic.ID != relicID || result.Relic.Head != "head-1" || result.Relic.Parts != 3 {
		t.Errorf("result relic = %+v, changed = %v", result.Relic, result.Changed)
	}
	if len(result.Parts) != 3 || result.Parts[0].ID != first.ID || result.Parts[2].ID != third.ID {
		t.Fatalf("result parts = %+v", result.Parts)
	}
	for _, part := range result.Parts {
		if part.Body != nil && (part.Body.Text != nil || part.Body.Base64 != nil) {
			t.Errorf("result part %s carries body text", part.ID)
		}
	}
	if result.Parts[0].Body == nil || result.Parts[0].Body.Version != first.ID {
		t.Errorf("result part body = %+v", result.Parts[0].Body)
	}

	acting, err := WithActor(context.Background(), "agent@example.com")
	if err != nil {
		t.Fatal(err)
	}
	actingService := newRelicTestService(store, relicServiceIDs(100, 8))
	if _, err := actingService.Create(acting, relicCreateInputForTest()); err != nil {
		t.Fatalf("Create as agent: %v", err)
	}
	if got := store.writes[1].pack.Actor.ID; got != "agent@example.com" {
		t.Errorf("acting create actor = %q, want the context's actor", got)
	}
}

func TestRelicCreateRefusesBadValuesBeforeWriting(t *testing.T) {
	cases := map[string]func(*RelicCreateInput){
		"blank title":      func(input *RelicCreateInput) { input.Title = "  \t" },
		"501-byte title":   func(input *RelicCreateInput) { input.Title = strings.Repeat("a", 501) },
		"type Spec":        func(input *RelicCreateInput) { input.Type = "Spec" },
		"part type 101":    func(input *RelicCreateInput) { input.Parts[1].Type = strings.Repeat("a", 101) },
		"format MD":        func(input *RelicCreateInput) { input.Parts[0].Body.Format = "MD" },
		"property name =":  func(input *RelicCreateInput) { input.Properties["a=b"] = relicRawString("x") },
		"invalid value":    func(input *RelicCreateInput) { input.Properties["broken"] = json.RawMessage(`{"a":`) },
		"invalid field":    func(input *RelicCreateInput) { input.Parts[0].Fields["broken"] = json.RawMessage(`[1,`) },
		"field name blank": func(input *RelicCreateInput) { input.Parts[0].Fields[""] = relicRawString("x") },
		"body 1 MiB + 1":   func(input *RelicCreateInput) { input.Parts[2].Body.Content = make([]byte, MaxRelicBodyBytes+1) },
		"fields over limit": func(input *RelicCreateInput) {
			input.Parts[0].Fields["long"] = relicRawString(strings.Repeat("f", MaxRelicPartFieldsBytes))
		},
		"properties over limit": func(input *RelicCreateInput) {
			input.Properties["long"] = relicRawString(strings.Repeat("p", MaxRelicPropertiesBytes))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store := newMemoryRelicStore()
			input := relicCreateInputForTest()
			mutate(&input)
			_, err := newRelicTestService(store, relicServiceIDs(1, 20)).Create(context.Background(), input)
			requireRelicCategory(t, name, err, CategoryValidation)
			if len(store.writes) != 0 {
				t.Fatalf("store saw %d writes", len(store.writes))
			}
		})
	}
}

func TestRelicCreateEnforcesAddTimeLimits(t *testing.T) {
	manyParts := func(count int) []RelicPartInput {
		parts := make([]RelicPartInput, count)
		for index := range parts {
			parts[index] = RelicPartInput{Type: "section"}
		}
		return parts
	}

	store := newMemoryRelicStore()
	_, err := newRelicTestService(store, relicServiceIDs(1, 2004)).Create(context.Background(),
		RelicCreateInput{Title: "Too many", Type: "spec", Parts: manyParts(MaxRelicLiveParts + 1)})
	requireRelicCategory(t, "2001 parts", err, CategoryValidation, "2001", "2000")
	if len(store.writes) != 0 {
		t.Fatalf("2001 parts: store saw %d writes", len(store.writes))
	}

	result, err := newRelicTestService(store, relicServiceIDs(1, 2003)).Create(context.Background(),
		RelicCreateInput{Title: "Just enough", Type: "spec", Parts: manyParts(MaxRelicLiveParts)})
	if err != nil {
		t.Fatalf("2000 parts: %v", err)
	}
	if len(store.writes) != 1 || result.Relic.Parts != MaxRelicLiveParts {
		t.Fatalf("2000 parts: writes = %d, live parts = %d", len(store.writes), result.Relic.Parts)
	}
	last := store.writes[0].pack.Operations[len(store.writes[0].pack.Operations)-1]
	if last.Position != "2000/1" {
		t.Errorf("last position = %s, want 2000/1", last.Position)
	}

	store = newMemoryRelicStore()
	full := make([]byte, MaxRelicBodyBytes)
	parts := make([]RelicPartInput, 0, 17)
	for range MaxRelicLiveBodyBytes / MaxRelicBodyBytes {
		parts = append(parts, RelicPartInput{Type: "section", Body: &RelicBodyInput{Content: full}})
	}
	parts = append(parts, RelicPartInput{Type: "section", Body: &RelicBodyInput{Content: []byte("x")}})
	_, err = newRelicTestService(store, relicServiceIDs(1, 30)).Create(context.Background(),
		RelicCreateInput{Title: "Too large", Type: "spec", Parts: parts})
	requireRelicCategory(t, "16 MiB + 1", err, CategoryValidation, "16777217", "16777216")
	if len(store.writes) != 0 {
		t.Fatalf("16 MiB + 1: store saw %d writes", len(store.writes))
	}
}

func TestResolveRelicPrefix(t *testing.T) {
	first := NewRelicID("WB", relicTestID(1))
	second := NewRelicID("WB", relicTestID(20))
	heads := []RelicHead{{RelicID: first, ObjectID: "a"}, {RelicID: second, ObjectID: "b"}}

	for _, input := range []string{first, strings.ToLower(first), strings.ToLower(first[:len(first)-1])} {
		head, err := ResolveRelicPrefix(heads, input)
		if err != nil || head != heads[0] {
			t.Errorf("ResolveRelicPrefix(%q) = %+v, %v; want %s", input, head, err, first)
		}
	}
	_, err := ResolveRelicPrefix(heads, "wb-r-")
	requireRelicCategory(t, "ambiguous", err, CategoryValidation, "ambiguous", `"wb-r-"`)
	_, err = ResolveRelicPrefix(heads, "WB-R-01M9B")
	requireRelicCategory(t, "none", err, CategoryNotFound, `no relic matches "WB-R-01M9B"`)
	_, err = ResolveRelicPrefix(heads, "  ")
	requireRelicCategory(t, "blank", err, CategoryValidation)
	taskID := "WB-" + relicTestID(1)
	_, err = ResolveRelicPrefix(heads, taskID)
	requireRelicCategory(t, "task ID", err, CategoryNotFound, "no relic matches")
}

func TestResolvePartPrefix(t *testing.T) {
	first, second := relicTestID(1), relicTestID(20)
	parts := []RelicPart{
		{ID: first, Type: "section", Position: "1/1", Removed: true},
		{ID: second, Type: "section", Position: "2/1"},
	}
	for _, input := range []string{first, strings.ToLower(first), strings.ToLower(first[:len(first)-1])} {
		part, err := ResolvePartPrefix(parts, input)
		if err != nil || part.ID != first {
			t.Errorf("ResolvePartPrefix(%q) = %+v, %v; want removed part %s", input, part, err, first)
		}
	}
	_, err := ResolvePartPrefix(parts, "01m9a")
	requireRelicCategory(t, "ambiguous", err, CategoryValidation, `part prefix "01m9a" matches 2 parts`, "use more characters")
	_, err = ResolvePartPrefix(parts, "01M9B")
	requireRelicCategory(t, "none", err, CategoryNotFound, `matches "01M9B"`)
	_, err = ResolvePartPrefix(parts, "")
	requireRelicCategory(t, "blank", err, CategoryValidation)
	_, err = ResolvePartPrefix(parts, "WB-"+first)
	requireRelicCategory(t, "task ID", err, CategoryNotFound)
}

func TestRelicListFiltersAndOrders(t *testing.T) {
	store := newMemoryRelicStore()
	// Put in an order that is not ID order.
	store.put(relicSnapshotForTest(t, 3, "spec", []string{relicTestID(31)}, false, true))
	store.put(relicSnapshotForTest(t, 1, "spec", []string{relicTestID(11), relicTestID(12)}, true, false))
	store.put(relicSnapshotForTest(t, 2, "plan", nil, false, false))
	service := newRelicTestService(store, nil)
	idOf := func(number int) string { return NewRelicID(serviceTestConfig.Key, relicTestID(700000+number)) }
	ids := func(summaries []RelicSummary) []string {
		out := make([]string, len(summaries))
		for index, summary := range summaries {
			out[index] = summary.ID
		}
		return out
	}

	all, err := service.List(context.Background(), RelicListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := ids(all), []string{idOf(1), idOf(2)}; !reflect.DeepEqual(got, want) {
		t.Errorf("default list = %v, want %v", got, want)
	}
	if first := all[0]; first.Parts != 1 || first.Type != "spec" || first.Title != "Relic 1" ||
		first.Author != relicServiceTestActor || first.Head != "fixture-1" || first.Deleted ||
		!first.CreatedAt.Equal(relicServiceTestTime) || !first.UpdatedAt.Equal(relicServiceTestTime.Add(time.Minute)) {
		t.Errorf("summary = %+v", first)
	}

	specs, err := service.List(context.Background(), RelicListFilter{Type: "spec"})
	if err != nil {
		t.Fatalf("List spec: %v", err)
	}
	if got, want := ids(specs), []string{idOf(1)}; !reflect.DeepEqual(got, want) {
		t.Errorf("spec list = %v, want %v", got, want)
	}

	everything, err := service.List(context.Background(), RelicListFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if got, want := ids(everything), []string{idOf(1), idOf(2), idOf(3)}; !reflect.DeepEqual(got, want) {
		t.Errorf("all list = %v, want %v", got, want)
	}
	if !everything[2].Deleted {
		t.Errorf("deleted relic summary = %+v", everything[2])
	}
}

func relicDocumentForTest() string {
	return `{"format":"workbook.relic","version":1,"id":"WB-R-` + relicTestID(1) + `","head":"abc",` +
		`"title":"Task archive compaction","type":"spec","schema":null,"author":"someone","retired":false,` +
		`"deleted":true,"createdAt":"2026-11-23T10:00:00Z","updatedAt":"2026-11-23T10:00:00Z",` +
		`"properties":{"status":"approved"},` +
		`"parts":[{"id":"` + relicTestID(2) + `","type":"section","position":"7/2","removed":true,` +
		`"fields":{"heading":"Goals"},"body":{"format":"md","version":"` + relicTestID(2) + `","bytes":99,"text":"# Goals\n"}},` +
		`{"type":"blob","fields":{},"body":{"bytes":3,"base64":"//4A"}},` +
		`{"type":"note","fields":{}}],"links":[]}`
}

func TestRelicDocumentRefusals(t *testing.T) {
	document, err := DecodeRelicDocument([]byte(relicDocumentForTest()))
	if err != nil {
		t.Fatalf("DecodeRelicDocument(valid): %v", err)
	}
	input, err := RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatalf("RelicCreateInputFromDocument(valid): %v", err)
	}
	if input.Title != "Task archive compaction" || input.Type != "spec" || string(input.Properties["status"]) != `"approved"` ||
		len(input.Parts) != 3 || input.Parts[0].Body.Format != "md" || string(input.Parts[0].Body.Content) != "# Goals\n" ||
		!bytes.Equal(input.Parts[1].Body.Content, []byte{0xff, 0xfe, 0x00}) || input.Parts[2].Body != nil ||
		string(input.Parts[0].Fields["heading"]) != `"Goals"` {
		t.Errorf("create input = %+v", input)
	}

	replace := func(old, new string) []byte {
		text := relicDocumentForTest()
		if !strings.Contains(text, old) {
			t.Fatalf("fixture does not contain %q", old)
		}
		return []byte(strings.Replace(text, old, new, 1))
	}
	_, err = DecodeRelicDocument(replace(`"links":[]`, `"links":[],"extra":1`))
	requireRelicCategory(t, "unknown member", err, CategoryValidation)
	_, err = DecodeRelicDocument(replace(`"fields":{}}]`, `"fields":{},"extra":1}]`))
	requireRelicCategory(t, "unknown part member", err, CategoryValidation)

	for name, test := range map[string]struct {
		data      []byte
		fragments []string
	}{
		"wrong format": {replace(`"format":"workbook.relic"`, `"format":"workbook.task"`), []string{"workbook.relic"}},
		"version 2":    {replace(`"version":1,"id"`, `"version":2,"id"`), []string{"2"}},
		"schema": {replace(`"schema":null`, `"schema":{"name":"spec"}`),
			[]string{"this version of workbook cannot create a relic that names a schema"}},
		"one link": {replace(`"links":[]`, `"links":[{"to":"WB-1"}]`),
			[]string{"this version of workbook cannot create a relic with links"}},
		"text and base64": {replace(`"base64":"//4A"`, `"base64":"//4A","text":"x"`), []string{"text", "base64"}},
		"neither":         {replace(`,"base64":"//4A"`, ``), []string{"text", "base64"}},
		"bad base64":      {replace(`"base64":"//4A"`, `"base64":"!!"`), []string{"base64"}},
	} {
		document, err := DecodeRelicDocument(test.data)
		if err != nil {
			t.Fatalf("%s: DecodeRelicDocument: %v", name, err)
		}
		_, err = RelicCreateInputFromDocument(document)
		requireRelicCategory(t, name, err, CategoryValidation, test.fragments...)
	}
}
