package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// relicFixturePart is one part of a relic folded outside the service.
type relicFixturePart struct {
	id string
	// position is where the part sits; empty places it by its index, at 1/1,
	// 2/1 and so on.
	position string
	// body is the body the state claims for the part. No text is stored.
	body    *RelicBodyChange
	removed bool
}

func relicFixtureID(number int) string {
	return NewRelicID(serviceTestConfig.Key, relicTestID(700000+number))
}

// relicFixture folds a relic outside the service, so it can hold what the
// service would refuse to build: a root pack that adds the parts and, when any
// is removed, a second pack that removes those.
func relicFixture(t *testing.T, number int, parts []relicFixturePart) RelicSnapshot {
	t.Helper()
	relicID := relicFixtureID(number)
	generation := relicTestID(710000 + number)
	operations := []RelicOperation{{
		ID:    relicTestID(720000 + number),
		Type:  RelicOperationCreate,
		Relic: &RelicCreateData{Title: fmt.Sprintf("Relic %d", number), Type: "spec", CreatedAt: relicServiceTestTime},
	}}
	var removes []RelicOperation
	for index, part := range parts {
		position := part.position
		if position == "" {
			position = fmt.Sprintf("%d/1", index+1)
		}
		operation := RelicOperation{ID: part.id, Type: RelicOperationPartAdd, PartType: "section", Position: position}
		if part.body != nil {
			body := *part.body
			operation.Body = &body
		}
		operations = append(operations, operation)
		if part.removed {
			removes = append(removes, RelicOperation{ID: relicTestID(800000 + index), Type: RelicOperationPartRemove, Part: part.id})
		}
	}
	pack := NewRelicOperationPack(serviceTestConfig.ProjectID, relicID, generation, relicServiceTestActor, 1,
		relicServiceTestTime, operations)
	record, err := ApplyRelic(nil, pack)
	if err != nil {
		t.Fatalf("ApplyRelic(root %d): %v", number, err)
	}
	if len(removes) != 0 {
		pack = NewRelicOperationPack(serviceTestConfig.ProjectID, relicID, generation, relicServiceTestActor, 2,
			relicServiceTestTime.Add(time.Minute), removes)
		if record, err = ApplyRelic(&record, pack); err != nil {
			t.Fatalf("ApplyRelic(removes %d): %v", number, err)
		}
	}
	return RelicSnapshot{Head: fmt.Sprintf("fixture-%d", number), Operation: pack, Record: record}
}

// relicFixtureParts returns count parts with IDs numbered from 1, the first
// removed of them removed, each claiming a body of bodyBytes bytes unless
// bodyBytes is negative.
func relicFixtureParts(count, removed int, bodyBytes int64) []relicFixturePart {
	parts := make([]relicFixturePart, count)
	for index := range parts {
		parts[index] = relicFixturePart{id: relicTestID(index + 1), removed: index < removed}
		if bodyBytes >= 0 {
			parts[index].body = &RelicBodyChange{Bytes: bodyBytes}
		}
	}
	return parts
}

// relicFixtureService stores a fixture as relic 1 and returns a service over
// it whose IDs are numbered from 900000.
func relicFixtureService(t *testing.T, parts []relicFixturePart) (*memoryRelicStore, RelicService, string) {
	t.Helper()
	store := newMemoryRelicStore()
	store.put(relicFixture(t, 1, parts))
	return store, newRelicTestService(store, relicServiceIDs(900000, 100)), relicFixtureID(1)
}

// relicCreatedForUpdate creates the relic of relicCreateInputForTest through
// the service, which is the store's first write, and returns the IDs of its
// parts: goals has a heading and an md body, decision has neither, and note
// has an empty body.
func relicCreatedForUpdate(t *testing.T) (store *memoryRelicStore, service RelicService, relicID, goals, decision, note string) {
	t.Helper()
	store = newMemoryRelicStore()
	service = newRelicTestService(store, relicServiceIDs(1, 100))
	created, err := service.Create(context.Background(), relicCreateInputForTest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store, service, created.Relic.ID, created.Parts[0].ID, created.Parts[1].ID, created.Parts[2].ID
}

func requireRelicWrites(t *testing.T, label string, store *memoryRelicStore, want int) {
	t.Helper()
	if len(store.writes) != want {
		t.Fatalf("%s: store saw %d writes, want %d", label, len(store.writes), want)
	}
}

// requireRelicWritten asserts an update changed the relic with exactly one
// more write, and returns that write.
func requireRelicWritten(t *testing.T, label string, store *memoryRelicStore, want int, result RelicMutationResult, err error) memoryRelicWrite {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	requireRelicWrites(t, label, store, want)
	if !result.Changed {
		t.Fatalf("%s: changed = false", label)
	}
	return store.writes[want-1]
}

func relicOperationTypes(pack RelicOperationPack) []RelicOperationType {
	types := make([]RelicOperationType, len(pack.Operations))
	for index, operation := range pack.Operations {
		types[index] = operation.Type
	}
	return types
}

func relicDocumentPartIDs(parts []RelicDocumentPart) []string {
	ids := make([]string, len(parts))
	for index, part := range parts {
		ids[index] = part.ID
	}
	return ids
}

// relicSeveralEdits is a title, a property, a new part, a field on another
// part and a move.
func relicSeveralEdits(goals, note string) []RelicEdit {
	return []RelicEdit{
		{Kind: RelicEditTitle, Title: "  A new title "},
		{Kind: RelicEditSetProperty, Name: "status", Value: json.RawMessage(` "done"`)},
		{
			Kind:     RelicEditAddPart,
			PartType: "risk",
			Fields:   []RelicFieldEdit{{Name: "level", Value: json.RawMessage(`2`)}},
			Body:     &RelicBodyInput{Format: "md", Content: []byte("risky\n")},
			After:    strings.ToLower(goals),
		},
		{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "owner", Value: relicRawString("dylan")}}},
		{Kind: RelicEditMovePart, Part: note, Before: goals},
	}
}

func TestRelicUpdateRecordsSeveralChangesAsOneCommit(t *testing.T) {
	store, service, relicID, goals, decision, note := relicCreatedForUpdate(t)
	ctx := context.Background()
	ids := relicServiceIDs(1, 100)
	created := store.writes[0]

	result, err := service.Update(ctx, strings.ToLower(relicID), relicSeveralEdits(goals, note))
	write := requireRelicWritten(t, "update", store, 2, result, err)

	if write.parent == nil || write.parent.Head != "head-1" {
		t.Fatalf("update parent = %+v, want the created relic", write.parent)
	}
	if write.reason != "update relic "+relicID {
		t.Errorf("reason = %q", write.reason)
	}
	pack := write.pack
	if pack.LogicalClock != 2 || pack.RelicID != relicID || pack.HistoryGeneration != created.pack.HistoryGeneration ||
		pack.Actor.ID != relicServiceTestActor || !pack.WallTime.Equal(relicServiceTestTime) {
		t.Errorf("pack clock, relic, generation, actor, time = %d, %s, %s, %s, %s",
			pack.LogicalClock, pack.RelicID, pack.HistoryGeneration, pack.Actor.ID, pack.WallTime)
	}
	wantTypes := []RelicOperationType{
		RelicOperationTitleSet, RelicOperationPropertySet, RelicOperationPartAdd,
		RelicOperationPartFieldSet, RelicOperationPartMove,
	}
	if got := relicOperationTypes(pack); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("operations = %v, want %v", got, wantTypes)
	}
	for index, operation := range pack.Operations {
		if operation.ID != ids[8+index] {
			t.Errorf("operation %d ID = %s, want %s", index, operation.ID, ids[8+index])
		}
	}
	title, property, added, field, move := pack.Operations[0], pack.Operations[1], pack.Operations[2], pack.Operations[3], pack.Operations[4]
	if title.Title != "A new title" {
		t.Errorf("title.set = %q", title.Title)
	}
	if property.Name != "status" || string(property.Value) != `"done"` {
		t.Errorf("property.set = %s %s", property.Name, property.Value)
	}
	if added.PartType != "risk" || added.Position != "3/2" || added.Body == nil || added.Body.Format != "md" ||
		added.Body.Bytes != 6 || added.Body.Base != "" || added.Part != "" {
		t.Errorf("part.add = %+v, body %+v", added, added.Body)
	}
	if field.Part != goals || field.Name != "owner" || string(field.Value) != `"dylan"` {
		t.Errorf("part.field.set = %+v", field)
	}
	if move.Part != note || move.Position != "1/2" {
		t.Errorf("part.move = %+v", move)
	}

	// A new part's fields and body, and an edited part's whole new fields, go
	// in the write's content. A part that only moved has none.
	if len(write.content) != 2 {
		t.Fatalf("content names %d parts, want 2: %+v", len(write.content), write.content)
	}
	if got := write.content[added.ID]; !got.HasBody || string(got.Body) != "risky\n" ||
		!reflect.DeepEqual(got.Fields, map[string]json.RawMessage{"level": json.RawMessage(`2`)}) {
		t.Errorf("new part content = %+v", got)
	}
	wantGoalsFields := map[string]json.RawMessage{"heading": relicRawString("Goals"), "owner": relicRawString("dylan")}
	if got := write.content[goals]; got.HasBody || !reflect.DeepEqual(got.Fields, wantGoalsFields) {
		t.Errorf("edited part content = %+v", got)
	}

	if result.Relic.ID != relicID || result.Relic.Head != "head-2" || result.Relic.Title != "A new title" ||
		result.Relic.Parts != 4 || !result.Relic.UpdatedAt.Equal(relicServiceTestTime) {
		t.Errorf("result relic = %+v", result.Relic)
	}
	if got, want := relicDocumentPartIDs(result.Parts), []string{added.ID, goals}; !reflect.DeepEqual(got, want) {
		t.Fatalf("result parts = %v, want the added and the edited part %v", got, want)
	}
	if body := result.Parts[0].Body; body == nil || body.Version != added.ID || body.Text != nil || body.Base64 != nil {
		t.Errorf("result added part body = %+v", body)
	}
	if !reflect.DeepEqual(result.Parts[1].Fields, wantGoalsFields) {
		t.Errorf("result edited part fields = %s", result.Parts[1].Fields)
	}

	shown, err := service.Show(ctx, relicID, RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got, want := relicDocumentPartIDs(shown.Parts), []string{note, goals, added.ID, decision}; !reflect.DeepEqual(got, want) {
		t.Errorf("part order = %v, want %v", got, want)
	}
	if shown.Title != "A new title" || string(shown.Properties["status"]) != `"done"` ||
		string(shown.Properties["owner"]) != `{"name":"dylan"}` {
		t.Errorf("shown title, properties = %q, %s", shown.Title, shown.Properties)
	}
	if body := shown.Parts[1].Body; body == nil || body.Text == nil || *body.Text != "# Goals\n" || body.Version != goals {
		t.Errorf("the edited part's body = %+v, want it untouched", body)
	}
	if body := shown.Parts[2].Body; body == nil || body.Text == nil || *body.Text != "risky\n" {
		t.Errorf("the added part's body = %+v", body)
	}
}

func TestRelicUpdateAppliesAllOrNothing(t *testing.T) {
	store, service, relicID, goals, _, note := relicCreatedForUpdate(t)
	edits := append(relicSeveralEdits(goals, note), RelicEdit{Kind: RelicEditRemovePart, Part: "01M9B"})

	_, err := service.Update(context.Background(), relicID, edits)
	requireRelicCategory(t, "unknown part", err, CategoryNotFound, relicID, `"01M9B"`)
	requireRelicWrites(t, "unknown part", store, 1)
	if got := store.snapshots[relicID]; got.Head != "head-1" || got.Record.State.Relic.Title != "Task archive compaction" {
		t.Errorf("relic after the refused update = %s %q", got.Head, got.Record.State.Relic.Title)
	}
}

func TestRelicUpdateEditsSeeTheOnesBeforeThem(t *testing.T) {
	ctx := context.Background()

	t.Run("remove then restore", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditRemovePart, Part: goals},
			{Kind: RelicEditRestorePart, Part: goals},
		})
		write := requireRelicWritten(t, "remove then restore", store, 2, result, err)
		want := []RelicOperationType{RelicOperationPartRemove, RelicOperationPartRestore}
		if got := relicOperationTypes(write.pack); !reflect.DeepEqual(got, want) {
			t.Errorf("operations = %v, want %v", got, want)
		}
		if part := requirePartForTest(t, write.record.State.Relic, goals); part.Removed {
			t.Errorf("part is removed after remove then restore")
		}
	})

	t.Run("remove then edit", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		_, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditRemovePart, Part: goals},
			{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "owner", Value: relicRawString("dylan")}}},
		})
		requireRelicCategory(t, "remove then edit", err, CategoryValidation,
			"part "+goals+" was removed; restore it first with --restore-part "+goals)
		requireRelicWrites(t, "remove then edit", store, 1)
	})

	t.Run("fields in one edit", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{
			{Name: "owner", Value: relicRawString("dylan")},
			{Name: "heading", Value: json.RawMessage(`{"b": 1, "a": 2}`)},
			{Name: "absent", Clear: true},
		}}})
		write := requireRelicWritten(t, "fields in one edit", store, 2, result, err)
		want := []RelicOperationType{RelicOperationPartFieldSet, RelicOperationPartFieldSet}
		if got := relicOperationTypes(write.pack); !reflect.DeepEqual(got, want) {
			t.Errorf("operations = %v, want %v", got, want)
		}
		wantFields := map[string]json.RawMessage{"heading": json.RawMessage(`{"a":2,"b":1}`), "owner": relicRawString("dylan")}
		if got := write.content[goals].Fields; !reflect.DeepEqual(got, wantFields) {
			t.Errorf("content fields = %s, want %s", got, wantFields)
		}
		if !reflect.DeepEqual(result.Parts[0].Fields, wantFields) {
			t.Errorf("result fields = %s, want %s", result.Parts[0].Fields, wantFields)
		}
	})
}

func TestRelicBodyEditFromAnOutOfDateVersionIsRefused(t *testing.T) {
	store, service, relicID, goals, decision, _ := relicCreatedForUpdate(t)
	ctx := context.Background()
	ids := relicServiceIDs(1, 100)
	save := func(part, base, text string) (RelicMutationResult, error) {
		return service.Update(ctx, relicID, []RelicEdit{{
			Kind: RelicEditPart, Part: part, Base: base, Body: &RelicBodyInput{Format: "txt", Content: []byte(text)},
		}})
	}

	// The first body's version is the part's own ID; saving over it makes c2.
	c1 := goals
	result, err := save(goals, c1, "second text")
	requireRelicWritten(t, "save from c1", store, 2, result, err)
	c2 := ids[8]
	if got := requirePartForTest(t, store.snapshots[relicID].Record.State.Relic, goals).Body.Version; got != c2 {
		t.Fatalf("version after the first save = %s, want %s", got, c2)
	}

	_, err = save(goals, c1, "from the old version")
	requireRelicCategory(t, "base c1", err, CategoryStaleWrite,
		"part "+goals+" body is at version "+c2+", not "+c1+
			`; read it again with "workbook relic show `+relicID+"#"+goals+` --body" and save from that version`)
	requireRelicWrites(t, "base c1", store, 2)

	_, err = save(goals, "", "from no version")
	requireRelicCategory(t, "no base", err, CategoryStaleWrite,
		"part "+goals+" body is at version "+c2+"; pass --base "+c2+" to save over it")
	requireRelicWrites(t, "no base", store, 2)

	result, err = save(goals, c2, "third text")
	write := requireRelicWritten(t, "base c2", store, 3, result, err)
	if len(write.pack.Operations) != 1 {
		t.Fatalf("base c2: operations = %d, want 1", len(write.pack.Operations))
	}
	operation := write.pack.Operations[0]
	if operation.Type != RelicOperationPartBodySet || operation.Part != goals || operation.Body == nil ||
		operation.Body.Base != c2 || operation.Body.Format != "txt" || operation.Body.Bytes != int64(len("third text")) {
		t.Errorf("part.body.set = %+v, body %+v", operation, operation.Body)
	}
	if got := write.content[goals]; !got.HasBody || string(got.Body) != "third text" {
		t.Errorf("content = %+v", got)
	}
	if len(result.Parts) != 1 || result.Parts[0].ID != goals || result.Parts[0].Body == nil ||
		result.Parts[0].Body.Version != operation.ID || result.Parts[0].Body.Text != nil {
		t.Errorf("result parts = %+v, want the part at version %s", result.Parts, operation.ID)
	}
	if got := result.Parts[0].Fields; !reflect.DeepEqual(got, map[string]json.RawMessage{"heading": relicRawString("Goals")}) {
		t.Errorf("result part fields = %s, want the part's stored fields", got)
	}
	shown, err := service.Show(ctx, relicID+"#"+goals, RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if body := shown.Parts[0].Body; body.Text == nil || *body.Text != "third text" || body.Version != operation.ID ||
		string(shown.Parts[0].Fields["heading"]) != `"Goals"` {
		t.Errorf("shown part = %+v, body %+v", shown.Parts[0], body)
	}

	_, err = save(decision, c1, "a first body")
	requireRelicCategory(t, "base without a body", err, CategoryStaleWrite,
		"part "+decision+" has no body, so there is no version "+c1+" to start from")
	requireRelicWrites(t, "base without a body", store, 3)

	result, err = save(decision, "", "a first body")
	write = requireRelicWritten(t, "first body", store, 4, result, err)
	first := write.pack.Operations[0]
	if first.Type != RelicOperationPartBodySet || first.Body.Base != "" {
		t.Errorf("first body operation = %+v, body %+v", first, first.Body)
	}
	if got := requirePartForTest(t, write.record.State.Relic, decision).Body; got == nil || got.Version != first.ID {
		t.Errorf("first body = %+v, want version %s", got, first.ID)
	}

	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditPart, Part: goals, Base: operation.ID}})
	requireRelicCategory(t, "base without a body to save", err, CategoryValidation)
	requireRelicWrites(t, "base without a body to save", store, 4)
}

func TestRelicUpdatePlacesParts(t *testing.T) {
	ctx := context.Background()
	a, b, c, removed := relicTestID(1), relicTestID(2), relicTestID(3), relicTestID(4)
	fixture := func(t *testing.T) (*memoryRelicStore, RelicService, string) {
		parts := relicFixtureParts(3, 0, -1)
		parts = append(parts, relicFixturePart{id: removed, position: "5/2", removed: true})
		return relicFixtureService(t, parts)
	}
	add := func(before, after string) RelicEdit {
		return RelicEdit{Kind: RelicEditAddPart, PartType: "section", Before: before, After: after}
	}

	for name, test := range map[string]struct {
		edit         RelicEdit
		wantType     RelicOperationType
		wantPosition string
		wantOrder    func(added string) []string
	}{
		"add with no anchor": {add("", ""), RelicOperationPartAdd, "4/1", func(added string) []string { return []string{a, b, c, added} }},
		"add before A":       {add(a, ""), RelicOperationPartAdd, "1/2", func(added string) []string { return []string{added, a, b, c} }},
		"add after A":        {add("", a), RelicOperationPartAdd, "3/2", func(added string) []string { return []string{a, added, b, c} }},
		"move C before A": {RelicEdit{Kind: RelicEditMovePart, Part: c, Before: a}, RelicOperationPartMove, "1/2",
			func(string) []string { return []string{c, a, b} }},
		"move A after C": {RelicEdit{Kind: RelicEditMovePart, Part: a, After: c}, RelicOperationPartMove, "4/1",
			func(string) []string { return []string{b, c, a} }},
	} {
		t.Run(name, func(t *testing.T) {
			store, service, relicID := fixture(t)
			result, err := service.Update(ctx, relicID, []RelicEdit{test.edit})
			write := requireRelicWritten(t, name, store, 1, result, err)
			if len(write.pack.Operations) != 1 {
				t.Fatalf("operations = %d, want 1", len(write.pack.Operations))
			}
			operation := write.pack.Operations[0]
			if operation.Type != test.wantType || operation.Position != test.wantPosition {
				t.Errorf("operation = %s at %s, want %s at %s", operation.Type, operation.Position, test.wantType, test.wantPosition)
			}
			shown, err := service.Show(ctx, relicID, RelicShowOptions{})
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if got, want := relicDocumentPartIDs(shown.Parts), test.wantOrder(operation.ID); !reflect.DeepEqual(got, want) {
				t.Errorf("order = %v, want %v", got, want)
			}
		})
	}

	wasRemoved := "part " + removed + " was removed; restore it first with --restore-part " + removed
	for name, test := range map[string]struct {
		edit      RelicEdit
		category  Category
		fragments []string
	}{
		"add with both anchors":     {add(a, b), CategoryValidation, nil},
		"move with both anchors":    {RelicEdit{Kind: RelicEditMovePart, Part: c, Before: a, After: b}, CategoryValidation, nil},
		"move with no anchor":       {RelicEdit{Kind: RelicEditMovePart, Part: c}, CategoryValidation, nil},
		"move before itself":        {RelicEdit{Kind: RelicEditMovePart, Part: a, Before: a}, CategoryValidation, []string{"itself"}},
		"move after itself":         {RelicEdit{Kind: RelicEditMovePart, Part: a, After: a}, CategoryValidation, []string{"itself"}},
		"add before a removed part": {add(removed, ""), CategoryValidation, []string{wasRemoved}},
		"add after a removed part":  {add("", removed), CategoryValidation, []string{wasRemoved}},
		"move next to a removed part": {RelicEdit{Kind: RelicEditMovePart, Part: a, After: removed},
			CategoryValidation, []string{wasRemoved}},
		"move a removed part": {RelicEdit{Kind: RelicEditMovePart, Part: removed, After: a},
			CategoryValidation, []string{wasRemoved}},
		"edit a removed part": {RelicEdit{Kind: RelicEditPart, Part: removed,
			Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("x")}}}, CategoryValidation, []string{wasRemoved}},
		"add next to an unknown part": {add("01M9B", ""), CategoryNotFound, []string{relicFixtureID(1), `"01M9B"`}},
		"an ambiguous anchor":         {add("01m9a", ""), CategoryValidation, []string{"use more characters"}},
		"an unknown edit":             {RelicEdit{Kind: "rename-part", Part: a}, CategoryValidation, nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, service, relicID := fixture(t)
			_, err := service.Update(ctx, relicID, []RelicEdit{test.edit})
			requireRelicCategory(t, name, err, test.category, test.fragments...)
			requireRelicWrites(t, name, store, 0)
		})
	}

	t.Run("a removed part keeps its place and is no neighbor", func(t *testing.T) {
		between, last := relicTestID(5), relicTestID(6)
		parts := []relicFixturePart{
			{id: a, position: "1/1"},
			{id: between, position: "3/2", removed: true},
			{id: b, position: "2/1"},
			{id: last, position: "7/1", removed: true},
		}
		// Next to a live part, the removed one between it and the next live
		// part is not looked at.
		store, service, relicID := relicFixtureService(t, parts)
		result, err := service.Update(ctx, relicID, []RelicEdit{add("", a)})
		write := requireRelicWritten(t, "add after A", store, 1, result, err)
		if got := write.pack.Operations[0].Position; got != "3/2" {
			t.Errorf("position after A = %s, want 3/2", got)
		}
		// At the end, the new part goes after every part, removed ones too.
		store, service, relicID = relicFixtureService(t, parts)
		result, err = service.Update(ctx, relicID, []RelicEdit{add("", "")})
		write = requireRelicWritten(t, "add at the end", store, 1, result, err)
		if got := write.pack.Operations[0].Position; got != "8/1" {
			t.Errorf("position at the end = %s, want 8/1", got)
		}
	})

	t.Run("parts that share a position", func(t *testing.T) {
		first, second, later := relicTestID(5), relicTestID(6), relicTestID(7)
		store, service, relicID := relicFixtureService(t, []relicFixturePart{
			{id: a, position: "1/1"},
			{id: first, position: "3/2"},
			{id: second, position: "3/2"},
			{id: b, position: "2/1"},
			{id: later, position: "5/1"},
		})
		// A part cannot go between the two unless its ID sorts between theirs.
		_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: later, After: first}})
		requireRelicCategory(t, "move between the two", err, CategoryValidation, "share a position", "move one of them first")
		requireRelicWrites(t, "move between the two", store, 0)

		// Moving one of them next to the other gives it a position of its own,
		// although it is already on that side of it.
		result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: second, After: first}})
		write := requireRelicWritten(t, "move one of the two", store, 1, result, err)
		if got := write.pack.Operations[0].Position; got != "5/3" {
			t.Errorf("position of the moved part = %s, want 5/3", got)
		}
		result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: second, After: first}})
		if err != nil || result.Changed {
			t.Fatalf("the same move again: changed = %v, error = %v", result.Changed, err)
		}
		result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: later, After: first}})
		requireRelicWritten(t, "move between the two once they are apart", store, 2, result, err)
		shown, err := service.Show(ctx, relicID, RelicShowOptions{})
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got, want := relicDocumentPartIDs(shown.Parts), []string{a, first, later, second, b}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}

		// Between two parts at its own position, a part has nowhere else to go
		// that keeps it between them, so asking for that changes nothing.
		store, service, relicID = relicFixtureService(t, []relicFixturePart{
			{id: first, position: "1/1"}, {id: second, position: "1/1"}, {id: later, position: "1/1"},
		})
		result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditMovePart, Part: second, After: first}})
		if err != nil || result.Changed {
			t.Fatalf("move between two parts at the same position: changed = %v, error = %v", result.Changed, err)
		}
		requireRelicWrites(t, "move between two parts at the same position", store, 0)
	})

	t.Run("several placements in one update", func(t *testing.T) {
		store, service, relicID := fixture(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			add(a, ""),
			{Kind: RelicEditMovePart, Part: c, Before: a},
			{Kind: RelicEditRestorePart, Part: removed},
			{Kind: RelicEditMovePart, Part: b, After: removed},
		})
		write := requireRelicWritten(t, "several placements", store, 1, result, err)
		added := write.pack.Operations[0].ID
		shown, err := service.Show(ctx, relicID, RelicShowOptions{})
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		// The new part goes first, then C goes between it and A.
		if got, want := relicDocumentPartIDs(shown.Parts), []string{added, c, a, removed, b}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
}

func TestRelicUpdateRefusesAPlacementPastThePositionLimit(t *testing.T) {
	// (n+1)/n and n/(n-1) are neighbors with nothing simpler between them than
	// (2n+1)/(2n-1). With a 127-digit n each is 255 bytes and that one is 257.
	n := "6" + strings.Repeat("0", 126)
	nPlusOne := "6" + strings.Repeat("0", 125) + "1"
	nMinusOne := "5" + strings.Repeat("9", 126)
	low, high := relicTestID(1), relicTestID(2)
	store, service, relicID := relicFixtureService(t, []relicFixturePart{
		{id: low, position: nPlusOne + "/" + n},
		{id: high, position: n + "/" + nMinusOne},
	})
	ctx := context.Background()

	for name, edit := range map[string]RelicEdit{
		"after the lower part":    {Kind: RelicEditAddPart, PartType: "section", After: low},
		"before the upper part":   {Kind: RelicEditAddPart, PartType: "section", Before: high},
		"together with a retitle": {Kind: RelicEditAddPart, PartType: "section", After: low},
	} {
		edits := []RelicEdit{edit}
		if name == "together with a retitle" {
			edits = append([]RelicEdit{{Kind: RelicEditTitle, Title: "A new title"}}, edits...)
		}
		_, err := service.Update(ctx, relicID, edits)
		requireRelicCategory(t, name, err, CategoryValidation, "257", "256")
		if len(err.Error()) > 300 {
			t.Errorf("%s: the refusal is %d bytes long; it must not carry the position", name, len(err.Error()))
		}
		requireRelicWrites(t, name, store, 0)
	}

	// Somewhere else is still open.
	result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Before: low}})
	write := requireRelicWritten(t, "before the lower part", store, 1, result, err)
	if got := write.pack.Operations[0].Position; got != "1/1" {
		t.Errorf("position before the lower part = %s, want 1/1", got)
	}
}

func TestRelicUpdateEnforcesLivePartLimitOnlyWhenAdding(t *testing.T) {
	// 2,002 parts, the first of them removed: 2,001 live.
	store, service, relicID := relicFixtureService(t, relicFixtureParts(MaxRelicLiveParts+2, 1, -1))
	ctx := context.Background()
	removed := relicTestID(1)
	liveParts := func() int { return len(store.snapshots[relicID].Record.State.Relic.LiveParts()) }
	addPart := []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}}
	restorePart := []RelicEdit{{Kind: RelicEditRestorePart, Part: removed}}

	shown, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(shown.Parts) != MaxRelicLiveParts+1 {
		t.Fatalf("shown parts = %d, want %d", len(shown.Parts), MaxRelicLiveParts+1)
	}

	_, err = service.Update(ctx, relicID, addPart)
	requireRelicCategory(t, "add at 2001", err, CategoryValidation, "2002", "2000")
	_, err = service.Update(ctx, relicID, restorePart)
	requireRelicCategory(t, "restore at 2001", err, CategoryValidation, "2002", "2000")
	requireRelicWrites(t, "at 2001", store, 0)

	result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "Still editable"}})
	requireRelicWritten(t, "retitle at 2001", store, 1, result, err)
	// Replacing one part with another adds nothing to the count.
	result, err = service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditRemovePart, Part: relicTestID(2)},
		{Kind: RelicEditAddPart, PartType: "section"},
	})
	requireRelicWritten(t, "swap at 2001", store, 2, result, err)
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: relicTestID(3)}})
	requireRelicWritten(t, "remove at 2001", store, 3, result, err)
	if got := liveParts(); got != MaxRelicLiveParts {
		t.Fatalf("live parts = %d, want %d", got, MaxRelicLiveParts)
	}

	_, err = service.Update(ctx, relicID, addPart)
	requireRelicCategory(t, "add at 2000", err, CategoryValidation, "2001", "2000")
	_, err = service.Update(ctx, relicID, restorePart)
	requireRelicCategory(t, "restore at 2000", err, CategoryValidation, "2001", "2000")
	requireRelicWrites(t, "at 2000", store, 3)

	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: relicTestID(4)}})
	requireRelicWritten(t, "remove at 2000", store, 4, result, err)
	result, err = service.Update(ctx, relicID, addPart)
	requireRelicWritten(t, "add at 1999", store, 5, result, err)
	if got := liveParts(); got != MaxRelicLiveParts || result.Relic.Parts != MaxRelicLiveParts {
		t.Fatalf("live parts = %d, result says %d; want %d", got, result.Relic.Parts, MaxRelicLiveParts)
	}
}

func TestRelicUpdateEnforcesTotalPartLimitOnlyWhenAdding(t *testing.T) {
	// 4,000 parts: 3,000 removed and 1,000 live.
	store, service, relicID := relicFixtureService(t, relicFixtureParts(MaxRelicParts, 3000, -1))
	ctx := context.Background()

	_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}})
	requireRelicCategory(t, "add at 4000", err, CategoryValidation, "4001", "4000")
	requireRelicWrites(t, "add at 4000", store, 0)

	result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRestorePart, Part: relicTestID(1)}})
	requireRelicWritten(t, "restore at 4000", store, 1, result, err)
	if result.Relic.Parts != 1001 {
		t.Errorf("live parts after the restore = %d, want 1001", result.Relic.Parts)
	}
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: relicTestID(3001)}})
	requireRelicWritten(t, "remove at 4000", store, 2, result, err)

	// A relic already over the limit refuses only the add.
	store, service, relicID = relicFixtureService(t, relicFixtureParts(MaxRelicParts+1, 3001, -1))
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}})
	requireRelicCategory(t, "add at 4001", err, CategoryValidation, "4002", "4000")
	requireRelicWrites(t, "add at 4001", store, 0)
	result, err = service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditTitle, Title: "Still editable"},
		{Kind: RelicEditRestorePart, Part: relicTestID(1)},
		{Kind: RelicEditRemovePart, Part: relicTestID(3002)},
	})
	requireRelicWritten(t, "retitle, restore and remove at 4001", store, 1, result, err)

	// One under the limit, a part can still be added.
	store, service, relicID = relicFixtureService(t, relicFixtureParts(MaxRelicParts-1, 3000, -1))
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}})
	requireRelicWritten(t, "add at 3999", store, 1, result, err)
	if got := len(store.snapshots[relicID].Record.State.Relic.Parts); got != MaxRelicParts {
		t.Errorf("parts after the add = %d, want %d", got, MaxRelicParts)
	}
}

func TestRelicUpdateEnforcesBodyTotalOnlyWhenItGrows(t *testing.T) {
	ctx := context.Background()
	full := make([]byte, MaxRelicBodyBytes)
	// saveBody saves a body over the fixture part numbered n, from the version
	// the relic has for it now.
	saveBody := func(store *memoryRelicStore, relicID string, n int, content []byte) []RelicEdit {
		edit := RelicEdit{Kind: RelicEditPart, Part: relicTestID(n), Body: &RelicBodyInput{Content: content}}
		if body := requirePartForTest(t, store.snapshots[relicID].Record.State.Relic, relicTestID(n)).Body; body != nil {
			edit.Base = body.Version
		}
		return []RelicEdit{edit}
	}
	addWithBody := []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Body: &RelicBodyInput{Content: []byte("x")}}}

	// Sixteen live parts of 1 MiB each, which is the total exactly, and a
	// seventeenth with no body.
	parts := relicFixtureParts(16, 0, MaxRelicBodyBytes)
	parts = append(parts, relicFixturePart{id: relicTestID(17)})
	store, service, relicID := relicFixtureService(t, parts)

	result, err := service.Update(ctx, relicID, saveBody(store, relicID, 1, full))
	requireRelicWritten(t, "keep the sum", store, 1, result, err)

	_, err = service.Update(ctx, relicID, saveBody(store, relicID, 17, []byte("x")))
	requireRelicCategory(t, "raise the sum with a first body", err, CategoryValidation, "16777217", "16777216")
	_, err = service.Update(ctx, relicID, addWithBody)
	requireRelicCategory(t, "raise the sum with a new part", err, CategoryValidation, "16777217", "16777216")
	requireRelicWrites(t, "raise the sum", store, 1)

	result, err = service.Update(ctx, relicID, saveBody(store, relicID, 2, []byte("shorter")))
	requireRelicWritten(t, "lower the sum", store, 2, result, err)
	result, err = service.Update(ctx, relicID, saveBody(store, relicID, 17, []byte("x")))
	requireRelicWritten(t, "raise the sum under the limit", store, 3, result, err)

	// Seventeen live parts of 1 MiB each, which is over the total, and a
	// removed eighteenth.
	parts = relicFixtureParts(18, 1, MaxRelicBodyBytes)
	store, service, relicID = relicFixtureService(t, parts)
	shown, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show a relic over the total: %v", err)
	}
	if len(shown.Parts) != 17 {
		t.Fatalf("shown parts = %d, want 17", len(shown.Parts))
	}
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "Still editable"}})
	requireRelicWritten(t, "retitle over the total", store, 1, result, err)
	result, err = service.Update(ctx, relicID, saveBody(store, relicID, 2, full))
	requireRelicWritten(t, "keep the sum over the total", store, 2, result, err)
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddPart, PartType: "section"}})
	requireRelicWritten(t, "add a part with no body over the total", store, 3, result, err)

	_, err = service.Update(ctx, relicID, addWithBody)
	requireRelicCategory(t, "add a body over the total", err, CategoryValidation, "16777216")
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRestorePart, Part: relicTestID(1)}})
	requireRelicCategory(t, "restore a body over the total", err, CategoryValidation, "18874368", "16777216")
	requireRelicWrites(t, "over the total", store, 3)

	result, err = service.Update(ctx, relicID, saveBody(store, relicID, 3, []byte("shorter")))
	requireRelicWritten(t, "lower the sum over the total", store, 4, result, err)
}

func TestRelicUpdateRefusesOversizedSingleValues(t *testing.T) {
	ctx := context.Background()
	long := func(size int) json.RawMessage { return relicRawString(strings.Repeat("v", size)) }
	field := func(name string, value json.RawMessage) RelicFieldEdit {
		return RelicFieldEdit{Name: name, Value: value}
	}

	for name, test := range map[string]struct {
		edits     func(goals string) []RelicEdit
		fragments []string
	}{
		"a 501-byte title": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditTitle, Title: strings.Repeat("a", MaxRelicTitleBytes+1)}}
		}, []string{"501", "500"}},
		"a blank title": {func(string) []RelicEdit { return []RelicEdit{{Kind: RelicEditTitle, Title: " \t"}} }, nil},
		"one property over the limit": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditSetProperty, Name: "long", Value: long(MaxRelicPropertiesBytes)}}
		}, []string{"65536"}},
		"properties passing the limit together": {func(string) []RelicEdit {
			return []RelicEdit{
				{Kind: RelicEditSetProperty, Name: "one", Value: long(40 * 1024)},
				{Kind: RelicEditSetProperty, Name: "two", Value: long(40 * 1024)},
			}
		}, []string{"65536"}},
		"one field over the limit": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{field("long", long(MaxRelicPartFieldsBytes))}}}
		}, []string{"16384"}},
		"fields passing the limit together": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{
				field("one", long(9*1024)), field("two", long(9*1024)),
			}}}
		}, []string{"16384"}},
		"a new part's fields over the limit": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Fields: []RelicFieldEdit{field("long", long(MaxRelicPartFieldsBytes))}}}
		}, []string{"16384"}},
		"a body of 1 MiB + 1": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Base: goals, Body: &RelicBodyInput{Content: make([]byte, MaxRelicBodyBytes+1)}}}
		}, []string{"1048577", "1048576"}},
		"a new part's body of 1 MiB + 1": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Body: &RelicBodyInput{Content: make([]byte, MaxRelicBodyBytes+1)}}}
		}, []string{"1048577", "1048576"}},
		"a property name with =": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditSetProperty, Name: "a=b", Value: relicRawString("x")}}
		}, nil},
		"a property to clear with no name": {func(string) []RelicEdit { return []RelicEdit{{Kind: RelicEditClearProperty}} }, nil},
		"a property value that is not JSON": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditSetProperty, Name: "broken", Value: json.RawMessage(`{"a":`)}}
		}, []string{`"broken"`}},
		"a field value that is not JSON": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{field("broken", json.RawMessage(`[1,`))}}}
		}, []string{`"broken"`}},
		"a field with no name": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Clear: true}}}}
		}, nil},
		"a part type Section": {func(string) []RelicEdit { return []RelicEdit{{Kind: RelicEditAddPart, PartType: "Section"}} }, nil},
		"a body format MD": {func(goals string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditPart, Part: goals, Base: goals, Body: &RelicBodyInput{Format: "MD", Content: []byte("x")}}}
		}, nil},
		"a new part's body format MD": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Body: &RelicBodyInput{Format: "MD"}}}
		}, nil},
		"a field to clear on a new part": {func(string) []RelicEdit {
			return []RelicEdit{{Kind: RelicEditAddPart, PartType: "section", Fields: []RelicFieldEdit{{Name: "heading", Clear: true}}}}
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
			// A real change ahead of the refused one must not be written either.
			edits := append([]RelicEdit{{Kind: RelicEditTitle, Title: "A new title"}}, test.edits(goals)...)
			_, err := service.Update(ctx, relicID, edits)
			requireRelicCategory(t, name, err, CategoryValidation, test.fragments...)
			if len(err.Error()) > 300 {
				t.Errorf("the refusal is %d bytes long; it must not carry the value", len(err.Error()))
			}
			requireRelicWrites(t, name, store, 1)
		})
	}

	// A body of exactly 1 MiB is within the limit.
	store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
	result, err := service.Update(ctx, relicID, []RelicEdit{{
		Kind: RelicEditPart, Part: goals, Base: goals, Body: &RelicBodyInput{Content: make([]byte, MaxRelicBodyBytes)},
	}})
	requireRelicWritten(t, "a body of 1 MiB", store, 2, result, err)
	// So is a properties object that a clear in the same update brings back
	// under the limit: only what would be stored is measured.
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditSetProperty, Name: "one", Value: long(40 * 1024)}})
	requireRelicWritten(t, "one large property", store, 3, result, err)
	result, err = service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditSetProperty, Name: "two", Value: long(40 * 1024)},
		{Kind: RelicEditClearProperty, Name: "one"},
	})
	requireRelicWritten(t, "properties under the limit at the end", store, 4, result, err)
}

// relicTitleUnchanged requires the stored relic to keep the title
// relicCreatedForUpdate gave it.
func relicTitleUnchanged(t *testing.T, label string, store *memoryRelicStore, relicID string) {
	t.Helper()
	if got := store.snapshots[relicID].Record.State.Relic.Title; got != "Task archive compaction" {
		t.Errorf("%s: title after the refused update = %q", label, got)
	}
}

func TestRelicUpdateRefusesANameGivenTwice(t *testing.T) {
	ctx := context.Background()
	set := func(name, value string) RelicEdit {
		return RelicEdit{Kind: RelicEditSetProperty, Name: name, Value: relicRawString(value)}
	}
	clear := func(name string) RelicEdit { return RelicEdit{Kind: RelicEditClearProperty, Name: name} }
	setField := func(name, value string) RelicFieldEdit {
		return RelicFieldEdit{Name: name, Value: relicRawString(value)}
	}
	clearField := func(name string) RelicFieldEdit { return RelicFieldEdit{Name: name, Clear: true} }

	for name, test := range map[string]struct {
		edits     func(goals string) []RelicEdit
		fragments func(goals string) []string
	}{
		"a property set and cleared": {
			func(string) []RelicEdit { return []RelicEdit{set("status", "a"), clear("status")} },
			func(string) []string {
				return []string{`this update sets and clears property "status"`, "name a property once in one update"}
			},
		},
		"a property cleared and set": {
			func(string) []RelicEdit { return []RelicEdit{clear("status"), set("status", "a")} },
			func(string) []string { return []string{`this update sets and clears property "status"`} },
		},
		"a property set twice": {
			func(string) []RelicEdit { return []RelicEdit{set("status", "a"), set("status", "b")} },
			func(string) []string {
				return []string{`this update sets property "status" twice`, "name a property once in one update"}
			},
		},
		// The relic has no such property, so each clear alone would change
		// nothing; the refusal is decided from the edits alone.
		"an absent property cleared twice": {
			func(string) []RelicEdit { return []RelicEdit{clear("absent"), clear("absent")} },
			func(string) []string { return []string{`this update clears property "absent" twice`} },
		},
		"a field set and cleared": {
			func(goals string) []RelicEdit {
				return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{setField("heading", "x"), clearField("heading")}}}
			},
			func(goals string) []string {
				return []string{`this update sets and clears field "heading" of part ` + goals, "name a field once for each part in one update"}
			},
		},
		"a field set twice in two edits": {
			func(goals string) []RelicEdit {
				return []RelicEdit{
					{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{setField("heading", "x")}},
					{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{setField("heading", "y")}},
				}
			},
			func(goals string) []string {
				return []string{`this update sets field "heading" of part ` + goals + " twice", "name a field once for each part in one update"}
			},
		},
		"an absent field cleared twice": {
			func(goals string) []RelicEdit {
				return []RelicEdit{{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{clearField("absent"), clearField("absent")}}}
			},
			func(goals string) []string {
				return []string{`this update clears field "absent" of part ` + goals + " twice"}
			},
		},
		// Two prefixes that find the same part name the same part.
		"one part addressed two ways": {
			func(goals string) []RelicEdit {
				return []RelicEdit{
					{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{setField("heading", "x")}},
					{Kind: RelicEditPart, Part: strings.ToLower(goals), Fields: []RelicFieldEdit{clearField("heading")}},
				}
			},
			func(goals string) []string {
				return []string{`this update sets and clears field "heading" of part ` + goals}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
			// A valid change alongside the refused ones must not be written either.
			edits := append([]RelicEdit{{Kind: RelicEditTitle, Title: "A new title"}}, test.edits(goals)...)
			_, err := service.Update(ctx, relicID, edits)
			requireRelicCategory(t, name, err, CategoryValidation, test.fragments(goals)...)
			requireRelicWrites(t, name, store, 1)
			relicTitleUnchanged(t, name, store, relicID)
		})
	}

	t.Run("before the relic is read", func(t *testing.T) {
		store, service, _, _, _, _ := relicCreatedForUpdate(t)
		_, err := service.Update(ctx, "WB-R-7ZZZ", []RelicEdit{set("status", "a"), clear("status")})
		requireRelicCategory(t, "an unknown relic", err, CategoryValidation, `"status"`)
		requireRelicWrites(t, "an unknown relic", store, 1)
	})

	t.Run("an oversized name is not echoed", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		long := strings.Repeat("n", 5000)
		for label, edits := range map[string][]RelicEdit{
			"property": {set(long, "a"), set(long, "b")},
			"field":    {{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{setField(long, "a"), clearField(long)}}},
		} {
			_, err := service.Update(ctx, relicID, edits)
			// A name that long is no name at all, which is said before the
			// update is searched for a name given twice.
			requireRelicCategory(t, label, err, CategoryValidation, "name is 5000 bytes", "the limit is 100 bytes")
			if len(err.Error()) > 300 {
				t.Errorf("%s: the refusal is %d bytes long; it must not carry the name", label, len(err.Error()))
			}
			requireRelicWrites(t, label, store, 1)
		}
	})

	t.Run("a part added and then edited", func(t *testing.T) {
		store, service, relicID := relicFixtureService(t, relicFixtureParts(1, 0, -1))
		// The fixture service mints IDs from 900000, so the new part is that one.
		added := relicTestID(900000)
		_, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditAddPart, PartType: "section", Fields: []RelicFieldEdit{setField("heading", "x")}},
			{Kind: RelicEditPart, Part: added, Fields: []RelicFieldEdit{setField("heading", "y")}},
		})
		requireRelicCategory(t, "a part added and then edited", err, CategoryValidation,
			`this update sets field "heading" of part `+added+" twice")
		requireRelicWrites(t, "a part added and then edited", store, 0)

		_, err = service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditAddPart, PartType: "section", Fields: []RelicFieldEdit{setField("heading", "x"), setField("heading", "y")}},
		})
		requireRelicCategory(t, "a new part's field given twice", err, CategoryValidation, `this update sets field "heading" of part `)
		requireRelicWrites(t, "a new part's field given twice", store, 0)
	})
}

func TestRelicUpdateNamesThatDoNotCollide(t *testing.T) {
	ctx := context.Background()

	t.Run("the same field on two parts", func(t *testing.T) {
		store, service, relicID, goals, decision, _ := relicCreatedForUpdate(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("One")}}},
			{Kind: RelicEditPart, Part: decision, Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("Two")}}},
		})
		write := requireRelicWritten(t, "the same field on two parts", store, 2, result, err)
		if got := write.content[goals].Fields["heading"]; string(got) != `"One"` {
			t.Errorf("goals heading = %s", got)
		}
		if got := write.content[decision].Fields["heading"]; string(got) != `"Two"` {
			t.Errorf("decision heading = %s", got)
		}
	})

	t.Run("a property and a field of one name", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditSetProperty, Name: "heading", Value: relicRawString("p")},
			{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("f")}}},
		})
		write := requireRelicWritten(t, "a property and a field of one name", store, 2, result, err)
		if got := write.record.State.Relic.Properties["heading"]; string(got) != `"p"` {
			t.Errorf("property heading = %s", got)
		}
		if got := write.content[goals].Fields["heading"]; string(got) != `"f"` {
			t.Errorf("field heading = %s", got)
		}
	})

	t.Run("several edits of one part naming different fields", func(t *testing.T) {
		store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "owner", Value: relicRawString("dylan")}}},
			{Kind: RelicEditPart, Part: strings.ToLower(goals), Fields: []RelicFieldEdit{{Name: "heading", Clear: true}}},
		})
		write := requireRelicWritten(t, "several edits of one part", store, 2, result, err)
		want := map[string]json.RawMessage{"owner": relicRawString("dylan")}
		if got := write.content[goals].Fields; !reflect.DeepEqual(got, want) {
			t.Errorf("fields = %s, want %s", got, want)
		}
	})

	t.Run("a part added and then edited in another field", func(t *testing.T) {
		store, service, relicID := relicFixtureService(t, relicFixtureParts(1, 0, -1))
		added := relicTestID(900000)
		result, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditAddPart, PartType: "section", Fields: []RelicFieldEdit{{Name: "heading", Value: relicRawString("x")}}},
			{Kind: RelicEditPart, Part: added, Fields: []RelicFieldEdit{{Name: "owner", Value: relicRawString("y")}}},
		})
		write := requireRelicWritten(t, "a part added and then edited", store, 1, result, err)
		want := map[string]json.RawMessage{"heading": relicRawString("x"), "owner": relicRawString("y")}
		if got := write.content[added].Fields; !reflect.DeepEqual(got, want) {
			t.Errorf("fields = %s, want %s", got, want)
		}
	})
}

func TestRelicUpdateThatChangesNothingWritesNothing(t *testing.T) {
	ctx := context.Background()
	store, service, relicID, goals, decision, note := relicCreatedForUpdate(t)
	result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: note}})
	requireRelicWritten(t, "remove a part", store, 2, result, err)

	sameField := []RelicFieldEdit{{Name: "heading", Value: json.RawMessage(` "Goals" `)}}
	noOps := map[string][]RelicEdit{
		"no edits":                              nil,
		"the same title":                        {{Kind: RelicEditTitle, Title: "  Task archive compaction  "}},
		"the same property value":               {{Kind: RelicEditSetProperty, Name: "owner", Value: json.RawMessage(`{ "name" : "dylan" }`)}},
		"clearing an absent property":           {{Kind: RelicEditClearProperty, Name: "absent"}},
		"the same field value":                  {{Kind: RelicEditPart, Part: goals, Fields: sameField}},
		"clearing an absent field":              {{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "absent", Clear: true}}}},
		"an edit with nothing in it":            {{Kind: RelicEditPart, Part: decision}},
		"removing a removed part":               {{Kind: RelicEditRemovePart, Part: note}},
		"restoring a live part":                 {{Kind: RelicEditRestorePart, Part: goals}},
		"moving a part after the one before it": {{Kind: RelicEditMovePart, Part: decision, After: goals}},
		"moving a part before the one after it": {{Kind: RelicEditMovePart, Part: goals, Before: decision}},
	}
	var everyNoOp []RelicEdit
	for name, edits := range noOps {
		result, err := service.Update(ctx, relicID, edits)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Changed {
			t.Errorf("%s: changed = true", name)
		}
		if result.Relic.ID != relicID || result.Relic.Head != "head-2" || result.Relic.Parts != 2 {
			t.Errorf("%s: result relic = %+v, want the relic as it stands", name, result.Relic)
		}
		if result.Parts == nil || len(result.Parts) != 0 {
			t.Errorf("%s: result parts = %#v, want an empty list", name, result.Parts)
		}
		requireRelicWrites(t, name, store, 2)
		everyNoOp = append(everyNoOp, edits...)
	}

	result, err = service.Update(ctx, relicID, everyNoOp)
	if err != nil || result.Changed {
		t.Fatalf("every no-op at once: changed = %v, error = %v", result.Changed, err)
	}
	requireRelicWrites(t, "every no-op at once", store, 2)

	// A part that sits where it is asked to go stays there even when its
	// position is not the one a fresh placement would choose.
	fixtureStore, fixtureService, fixtureID := relicFixtureService(t, []relicFixturePart{
		{id: relicTestID(1), position: "1/1"}, {id: relicTestID(2), position: "7/4"}, {id: relicTestID(3), position: "3/1"},
	})
	for name, edit := range map[string]RelicEdit{
		"after":  {Kind: RelicEditMovePart, Part: relicTestID(2), After: relicTestID(1)},
		"before": {Kind: RelicEditMovePart, Part: relicTestID(2), Before: relicTestID(3)},
	} {
		result, err := fixtureService.Update(ctx, fixtureID, []RelicEdit{edit})
		if err != nil || result.Changed {
			t.Fatalf("move %s where it is: changed = %v, error = %v", name, result.Changed, err)
		}
		requireRelicWrites(t, "move "+name+" where it is", fixtureStore, 0)
	}

	mixed := append([]RelicEdit{{Kind: RelicEditSetProperty, Name: "status", Value: relicRawString("done")}}, everyNoOp...)
	result, err = service.Update(ctx, relicID, mixed)
	write := requireRelicWritten(t, "one real edit among no-ops", store, 3, result, err)
	if got := relicOperationTypes(write.pack); !reflect.DeepEqual(got, []RelicOperationType{RelicOperationPropertySet}) {
		t.Errorf("operations = %v, want one property.set", got)
	}
	if len(write.content) != 0 || len(result.Parts) != 0 {
		t.Errorf("content = %+v, result parts = %+v; want none", write.content, result.Parts)
	}
}

func TestRelicDeleteAndRestoreWriteOneOperationEach(t *testing.T) {
	ctx := context.Background()
	store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
	retitle := []RelicEdit{{Kind: RelicEditTitle, Title: "A new title"}}

	restored, err := service.Restore(ctx, relicID)
	if err != nil || restored.Changed || restored.Relic.ID != relicID || restored.Relic.Deleted {
		t.Fatalf("Restore of a live relic = %+v, %v; want no change", restored, err)
	}
	requireRelicWrites(t, "restore a live relic", store, 1)

	deleted, err := service.Delete(ctx, strings.ToLower(relicID))
	write := requireRelicWritten(t, "delete", store, 2, deleted, err)
	if write.reason != "delete relic "+relicID || write.pack.LogicalClock != 2 ||
		!reflect.DeepEqual(relicOperationTypes(write.pack), []RelicOperationType{RelicOperationTombstone}) {
		t.Errorf("delete wrote %v as %q at clock %d", relicOperationTypes(write.pack), write.reason, write.pack.LogicalClock)
	}
	if !deleted.Relic.Deleted || deleted.Relic.Head != "head-2" || deleted.Relic.Parts != 3 || len(deleted.Parts) != 0 {
		t.Errorf("delete result = %+v", deleted)
	}

	again, err := service.Delete(ctx, relicID)
	if err != nil || again.Changed || !again.Relic.Deleted || again.Relic.Head != "head-2" {
		t.Fatalf("Delete of a deleted relic = %+v, %v; want no change", again, err)
	}
	_, err = service.Update(ctx, relicID, retitle)
	requireRelicCategory(t, "update a deleted relic", err, CategoryValidation,
		"relic "+relicID+` is deleted; restore it first with "workbook relic restore `+relicID+`"`)
	_, err = service.Update(ctx, relicID, nil)
	requireRelicCategory(t, "update a deleted relic with no edits", err, CategoryValidation, "is deleted")
	requireRelicWrites(t, "a deleted relic", store, 2)

	restored, err = service.Restore(ctx, relicID)
	write = requireRelicWritten(t, "restore", store, 3, restored, err)
	if write.reason != "restore relic "+relicID || write.pack.LogicalClock != 3 ||
		!reflect.DeepEqual(relicOperationTypes(write.pack), []RelicOperationType{RelicOperationRestore}) {
		t.Errorf("restore wrote %v as %q at clock %d", relicOperationTypes(write.pack), write.reason, write.pack.LogicalClock)
	}
	if restored.Relic.Deleted || restored.Relic.Parts != 3 {
		t.Errorf("restore result = %+v", restored.Relic)
	}

	result, err := service.Update(ctx, relicID, retitle)
	requireRelicWritten(t, "update after the restore", store, 4, result, err)
	shown, err := service.Show(ctx, relicID+"#"+goals, RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if body := shown.Parts[0].Body; shown.Title != "A new title" || body == nil || body.Text == nil || *body.Text != "# Goals\n" {
		t.Errorf("shown after delete and restore = %q, body %+v", shown.Title, body)
	}

	_, err = service.Delete(ctx, "WB-R-01M9B")
	requireRelicCategory(t, "delete an unknown relic", err, CategoryNotFound, "no relic matches")
	_, err = service.Restore(ctx, "WB-"+relicTestID(1))
	requireRelicCategory(t, "restore a task ID", err, CategoryNotFound, "no relic matches")
	_, err = service.Update(ctx, "WB-R-01M9B", retitle)
	requireRelicCategory(t, "update an unknown relic", err, CategoryNotFound, "no relic matches")
	requireRelicWrites(t, "unknown relics", store, 4)
}

// staleRelicWriter refuses every write the way the store refuses one whose
// relic changed after it was read.
type staleRelicWriter struct{ calls int }

func (w *staleRelicWriter) WriteRelic(
	context.Context, ProjectConfig, *RelicSnapshot, RelicOperationPack, RelicRecord, map[string]RelicPartContent, string,
) (RelicSnapshot, error) {
	w.calls++
	return RelicSnapshot{}, Errorf(CategoryStaleWrite, "relic ref changed concurrently")
}

func TestRelicChangesReturnAStaleWriteWithoutRetrying(t *testing.T) {
	ctx := context.Background()
	store, service, relicID, goals, _, _ := relicCreatedForUpdate(t)
	writer := &staleRelicWriter{}
	service.Writer = writer

	_, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditTitle, Title: "A new title"},
		{Kind: RelicEditPart, Part: goals, Base: goals, Body: &RelicBodyInput{Content: []byte("x")}},
	})
	requireRelicCategory(t, "update", err, CategoryStaleWrite, "changed concurrently")
	_, err = service.Delete(ctx, relicID)
	requireRelicCategory(t, "delete", err, CategoryStaleWrite, "changed concurrently")
	if writer.calls != 2 {
		t.Errorf("the writer was asked %d times, want once for each change", writer.calls)
	}
	requireRelicWrites(t, "stale writes", store, 1)
}

func TestRelicChangesAreRecordedUnderTheActingUser(t *testing.T) {
	store, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	acting, err := WithActor(context.Background(), "agent@example.com")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Update(acting, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "A new title"}})
	requireRelicWritten(t, "update", store, 2, result, err)
	result, err = service.Delete(acting, relicID)
	requireRelicWritten(t, "delete", store, 3, result, err)
	result, err = service.Restore(acting, relicID)
	requireRelicWritten(t, "restore", store, 4, result, err)
	for index, write := range store.writes[1:] {
		if write.pack.Actor.ID != "agent@example.com" {
			t.Errorf("change %d was recorded under %q, want the acting user", index+1, write.pack.Actor.ID)
		}
	}
	// The relic's author is whoever created it.
	if result.Relic.Author != relicServiceTestActor {
		t.Errorf("author = %q, want %q", result.Relic.Author, relicServiceTestActor)
	}
}

func TestRelicChangesRefuseANewerWritersRelic(t *testing.T) {
	ctx := context.Background()
	for name, deleted := range map[string]bool{"live": false, "deleted": true} {
		store := newMemoryRelicStore()
		snapshot := relicSnapshotForTest(t, 1, "spec", []string{relicTestID(11)}, false, deleted)
		snapshot.Record.State.MinReader = SupportedFormatGeneration + 1
		store.put(snapshot)
		service := newRelicTestService(store, relicServiceIDs(900000, 10))
		relicID := relicFixtureID(1)

		// Even a change that would write nothing is refused: this build cannot
		// tell what the relic holds.
		for label, change := range map[string]func() (RelicMutationResult, error){
			"update": func() (RelicMutationResult, error) {
				return service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditTitle, Title: "x"}})
			},
			"update, no edits": func() (RelicMutationResult, error) { return service.Update(ctx, relicID, nil) },
			"delete":           func() (RelicMutationResult, error) { return service.Delete(ctx, relicID) },
			"restore":          func() (RelicMutationResult, error) { return service.Restore(ctx, relicID) },
		} {
			_, err := change()
			requireRelicCategory(t, name+" "+label, err, CategoryNewerWriter, relicID, "newer workbook")
		}
		requireRelicWrites(t, name, store, 0)
	}
}

func TestRelicUpdateStoresWhatItReports(t *testing.T) {
	// The fold of the written pack is the relic the service worked on, and a
	// body saved over another keeps the part's other content.
	store, service, relicID, goals, decision, note := relicCreatedForUpdate(t)
	ctx := context.Background()
	result, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditPart, Part: note, Base: note, Body: &RelicBodyInput{Format: "md", Content: []byte{0xff, 0x00}},
			Fields: []RelicFieldEdit{{Name: "kind", Value: relicRawString("aside")}}},
		{Kind: RelicEditClearProperty, Name: "status"},
		{Kind: RelicEditPart, Part: goals, Fields: []RelicFieldEdit{{Name: "heading", Clear: true}}},
		{Kind: RelicEditRemovePart, Part: decision},
	})
	write := requireRelicWritten(t, "update", store, 2, result, err)
	want := []RelicOperationType{
		RelicOperationPartFieldSet, RelicOperationPartBodySet, RelicOperationPropertyClear,
		RelicOperationPartFieldClear, RelicOperationPartRemove,
	}
	if got := relicOperationTypes(write.pack); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if got := write.content[goals]; got.HasBody || got.Fields == nil || len(got.Fields) != 0 {
		t.Errorf("content of the part whose only field was cleared = %+v, want empty fields", got)
	}
	if got := write.content[note]; !got.HasBody || !bytes.Equal(got.Body, []byte{0xff, 0x00}) ||
		string(got.Fields["kind"]) != `"aside"` {
		t.Errorf("content of the part given a field and a body = %+v", got)
	}
	if _, given := write.content[decision]; given {
		t.Errorf("content was given for a part that was only removed")
	}
	if got, wantIDs := relicDocumentPartIDs(result.Parts), []string{note, goals}; !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("result parts = %v, want %v", got, wantIDs)
	}
	shown, err := service.Show(ctx, relicID, RelicShowOptions{Bodies: true, Removed: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got, wantIDs := relicDocumentPartIDs(shown.Parts), []string{goals, decision, note}; !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("shown parts = %v, want %v", got, wantIDs)
	}
	if body := shown.Parts[0].Body; len(shown.Parts[0].Fields) != 0 || body.Text == nil || *body.Text != "# Goals\n" {
		t.Errorf("goals = %+v, body %+v", shown.Parts[0], body)
	}
	if !shown.Parts[1].Removed || shown.Parts[2].Body.Base64 == nil || shown.Parts[2].Body.Format != "md" {
		t.Errorf("decision removed = %v, note body = %+v", shown.Parts[1].Removed, shown.Parts[2].Body)
	}
	if _, present := shown.Properties["status"]; present {
		t.Errorf("status is still set: %s", shown.Properties)
	}
}

// relicOverItsValueTotals stores a relic folded outside the service whose
// properties together, and whose first part's fields together, are past their
// totals: properties a and b of 40,000 bytes and c of 10,000, and fields a and
// b of 9,000 bytes and c of 1,000. Every one of those values is within the
// limit for one value, so the relic is what several allowed edits add up to.
// Its first part has a body of four bytes at version relicTestID(1).
func relicOverItsValueTotals(t *testing.T) (*memoryRelicStore, RelicService, string, string) {
	t.Helper()
	filler := func(size int) json.RawMessage { return relicRawString(strings.Repeat("v", size-2)) }
	snapshot := relicFixture(t, 1, relicFixtureParts(2, 0, 4))
	state := snapshot.Record.State
	for index, property := range []struct {
		name string
		size int
	}{{"a", 40000}, {"b", 40000}, {"c", 10000}} {
		pack := NewRelicOperationPack(state.ProjectID, state.RelicID, state.History.Generation, relicServiceTestActor,
			snapshot.Record.State.LogicalClock+1, relicServiceTestTime.Add(time.Duration(index+1)*time.Minute),
			[]RelicOperation{{ID: relicTestID(750000 + index), Type: RelicOperationPropertySet, Name: property.name, Value: filler(property.size)}})
		record, err := ApplyRelic(&snapshot.Record, pack)
		if err != nil {
			t.Fatalf("ApplyRelic(property %s): %v", property.name, err)
		}
		snapshot = RelicSnapshot{Head: snapshot.Head, Operation: pack, Record: record}
	}
	store := newMemoryRelicStore()
	store.put(snapshot)
	partID := relicTestID(1)
	content := store.content[state.RelicID][partID]
	content.Fields = map[string]json.RawMessage{"a": filler(9000), "b": filler(9000), "c": filler(1000)}
	content.Body, content.HasBody = []byte("text"), true
	store.content[state.RelicID][partID] = content

	if size, _ := json.Marshal(snapshot.Record.State.Relic.Properties); len(size) <= MaxRelicPropertiesBytes {
		t.Fatalf("the fixture's properties are %d bytes, want them past %d", len(size), MaxRelicPropertiesBytes)
	}
	if size, _ := json.Marshal(NewRelicFields(content.Fields)); len(size)+1 <= MaxRelicPartFieldsBytes {
		t.Fatalf("the fixture's fields are %d bytes, want them past %d", len(size)+1, MaxRelicPartFieldsBytes)
	}
	return store, newRelicTestService(store, relicServiceIDs(900000, 100)), state.RelicID, partID
}

// All of a relic's properties together, and all of one part's fields
// together, are totals: a change is refused only when it leaves a total over
// its limit and larger than it was. A relic already over one, as two edits
// made apart can leave it, keeps every change that does not grow it, so it
// can be brought back under. One value over the limit for one value is never
// stored.
func TestRelicUpdateChecksPropertyAndFieldTotalsOnlyWhenTheyGrow(t *testing.T) {
	ctx := context.Background()
	filler := func(size int) json.RawMessage { return relicRawString(strings.Repeat("w", size-2)) }
	setProperty := func(name string, size int) RelicEdit {
		return RelicEdit{Kind: RelicEditSetProperty, Name: name, Value: filler(size)}
	}
	setField := func(part, name string, size int) RelicEdit {
		return RelicEdit{Kind: RelicEditPart, Part: part, Fields: []RelicFieldEdit{{Name: name, Value: filler(size)}}}
	}

	store, service, relicID, partID := relicOverItsValueTotals(t)
	if _, err := service.Show(ctx, relicID, RelicShowOptions{Bodies: true}); err != nil {
		t.Fatalf("Show a relic over both totals: %v", err)
	}

	allowed := []struct {
		label string
		edits []RelicEdit
	}{
		{"change the title", []RelicEdit{{Kind: RelicEditTitle, Title: "Still editable"}}},
		{"save a body", []RelicEdit{{Kind: RelicEditPart, Part: partID, Base: partID, Body: &RelicBodyInput{Content: []byte("new text")}}}},
		{"set a property to a value of the same size", []RelicEdit{setProperty("c", 10000)}},
		{"set a property to a smaller value", []RelicEdit{setProperty("c", 5000)}},
		{"set a field to a value of the same size", []RelicEdit{setField(partID, "c", 1000)}},
		{"set a field to a smaller value", []RelicEdit{setField(partID, "c", 500)}},
		{"swap a property for a smaller one", []RelicEdit{{Kind: RelicEditClearProperty, Name: "c"}, setProperty("d", 4000)}},
		{"clear a property", []RelicEdit{{Kind: RelicEditClearProperty, Name: "d"}}},
		{"clear a field", []RelicEdit{{Kind: RelicEditPart, Part: partID, Fields: []RelicFieldEdit{{Name: "c", Clear: true}}}}},
	}
	for index, change := range allowed {
		result, err := service.Update(ctx, relicID, change.edits)
		requireRelicWritten(t, change.label, store, index+1, result, err)
	}
	// The relic is still over both totals after all of that.
	written := len(allowed)
	if size, _ := json.Marshal(store.snapshots[relicID].Record.State.Relic.Properties); len(size) <= MaxRelicPropertiesBytes {
		t.Fatalf("the properties are %d bytes after the allowed changes, want them still past the total", len(size))
	}

	refused := []struct {
		label     string
		edits     []RelicEdit
		fragments []string
	}{
		{"add a property", []RelicEdit{setProperty("e", 10)}, []string{"properties", "65536"}},
		{"set a property to a larger value", []RelicEdit{setProperty("a", 40001)}, []string{"properties", "65536"}},
		{"add a field", []RelicEdit{setField(partID, "e", 10)}, []string{"fields", "16384"}},
		{"set a field to a larger value", []RelicEdit{setField(partID, "a", 9001)}, []string{"fields", "16384"}},
		{"one property value past the limit for one value, with the total shrinking",
			[]RelicEdit{{Kind: RelicEditClearProperty, Name: "a"}, setProperty("b", MaxRelicPropertyValueBytes+1)},
			[]string{"65537", "65536"}},
		{"one field value past the limit for one value, with the total shrinking",
			[]RelicEdit{{Kind: RelicEditPart, Part: partID, Fields: []RelicFieldEdit{
				{Name: "a", Clear: true}, {Name: "b", Value: filler(MaxRelicFieldValueBytes + 1)},
			}}},
			[]string{"16385", "16384"}},
	}
	for _, change := range refused {
		_, err := service.Update(ctx, relicID, change.edits)
		requireRelicCategory(t, change.label, err, CategoryValidation, change.fragments...)
		if len(err.Error()) > 300 {
			t.Errorf("%s: the refusal is %d bytes long; it must not carry the value", change.label, len(err.Error()))
		}
		requireRelicWrites(t, change.label, store, written)
	}

	// Clearing enough brings the relic back under, and it can grow again.
	result, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditClearProperty, Name: "a"}})
	requireRelicWritten(t, "clear a large property", store, written+1, result, err)
	result, err = service.Update(ctx, relicID, []RelicEdit{setProperty("e", 10)})
	requireRelicWritten(t, "add a property under the total", store, written+2, result, err)
}

// A value at the limit for one value is refused on create when it takes the
// total past its limit, and one past it is refused for itself.
func TestRelicCreateRefusesOneValuePastItsLimit(t *testing.T) {
	for name, mutate := range map[string]func(*RelicCreateInput){
		"property": func(input *RelicCreateInput) {
			input.Properties = map[string]json.RawMessage{"long": relicRawString(strings.Repeat("p", MaxRelicPropertyValueBytes-1))}
		},
		"field": func(input *RelicCreateInput) {
			input.Parts[0].Fields = map[string]json.RawMessage{"long": relicRawString(strings.Repeat("f", MaxRelicFieldValueBytes-1))}
		},
	} {
		store := newMemoryRelicStore()
		input := relicCreateInputForTest()
		mutate(&input)
		_, err := newRelicTestService(store, relicServiceIDs(1, 20)).Create(context.Background(), input)
		requireRelicCategory(t, name, err, CategoryValidation, "a "+name+" value", "the limit is")
		if len(err.Error()) > 300 || len(store.writes) != 0 {
			t.Fatalf("%s: refusal of %d bytes, %d writes", name, len(err.Error()), len(store.writes))
		}
	}
}

// A save over a body that names no format keeps the label the body has. The
// file the text came from names the label only of a part's first body, so the
// name of a scratch file never decides how a stored body is drawn.
func TestRelicBodySaveKeepsItsFormatLabel(t *testing.T) {
	store, service, relicID, goals, decision, note := relicCreatedForUpdate(t)
	ctx := context.Background()
	writes := 1
	label := func(part string) string {
		t.Helper()
		body := requirePartForTest(t, store.snapshots[relicID].Record.State.Relic, part).Body
		if body == nil {
			t.Fatalf("part %s has no body", part)
		}
		return body.Format
	}
	save := func(what, part string, edit RelicEdit) {
		t.Helper()
		edit.Kind, edit.Part = RelicEditPart, part
		if body := requirePartForTest(t, store.snapshots[relicID].Record.State.Relic, part).Body; body != nil {
			edit.Base = body.Version
		}
		result, err := service.Update(ctx, relicID, []RelicEdit{edit})
		writes++
		write := requireRelicWritten(t, what, store, writes, result, err)
		// The label kept is the label recorded: a body edit states the format
		// it saves.
		if operation := write.pack.Operations[0]; operation.Type != RelicOperationPartBodySet ||
			operation.Body.Format != label(part) {
			t.Fatalf("%s: operation = %+v with body %+v, want a body edit recording label %q",
				what, operation, operation.Body, label(part))
		}
	}

	if got := label(goals); got != "md" {
		t.Fatalf("the fixture's goals body is labeled %q, want md", got)
	}
	for _, file := range []string{"tmp.QdhVaHD4", "edit", "body.txt", "Notes.HTML", "draft.my notes"} {
		save("a save from "+file, goals, RelicEdit{BodyFile: file, Body: &RelicBodyInput{Content: []byte("from " + file)}})
		if got := label(goals); got != "md" {
			t.Fatalf("a save from %s with no format left the label %q, want md", file, got)
		}
	}
	// A format read from the file's name is not a format asked for, whatever
	// Body.Format holds beside it.
	save("a save with a file and a stray format", goals,
		RelicEdit{BodyFile: "body.txt", Body: &RelicBodyInput{Format: "txt", Content: []byte("stray")}})
	if got := label(goals); got != "md" {
		t.Fatalf("a save naming a file left the label %q, want md", got)
	}

	save("a save that asks for html", goals, RelicEdit{Body: &RelicBodyInput{Format: "html", Content: []byte("<p>")}})
	if got := label(goals); got != "html" {
		t.Fatalf("a save that asks for html left the label %q", got)
	}
	save("a save that asks for no label", goals, RelicEdit{Body: &RelicBodyInput{Content: []byte("plain")}})
	if got := label(goals); got != "" {
		t.Fatalf("a save that asks for no label left the label %q", got)
	}
	// No label is a label to keep, too.
	save("a save over an unlabeled body", goals, RelicEdit{BodyFile: "notes.md", Body: &RelicBodyInput{Content: []byte("still plain")}})
	if got := label(goals); got != "" {
		t.Fatalf("a save from notes.md over an unlabeled body left the label %q, want none", got)
	}
	// The note's body is empty and unlabeled; it is a body all the same.
	save("a save over an empty body", note, RelicEdit{BodyFile: "notes.md", Body: &RelicBodyInput{Content: []byte("text")}})
	if got := label(note); got != "" {
		t.Fatalf("a save from notes.md over an empty unlabeled body left the label %q, want none", got)
	}

	// A part with no body yet takes its first label from the file's ending.
	_, err := service.Update(ctx, relicID, []RelicEdit{{
		Kind: RelicEditPart, Part: decision, BodyFile: "draft.my notes", Body: &RelicBodyInput{Content: []byte("x")},
	}})
	requireRelicCategory(t, "a first body from a file whose ending is no label", err, CategoryValidation, "--body-format", "draft.my notes")
	requireRelicWrites(t, "a first body from a file whose ending is no label", store, writes)
	save("a first body from Notes.MD", decision, RelicEdit{BodyFile: "Notes.MD", Body: &RelicBodyInput{Content: []byte("# Why\n")}})
	if got := label(decision); got != "md" {
		t.Fatalf("a first body saved from Notes.MD is labeled %q, want md", got)
	}
}
