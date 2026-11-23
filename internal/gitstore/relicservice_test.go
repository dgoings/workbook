package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicServiceFor wires a core.RelicService to a real repository, minting IDs
// numbered from start.
func relicServiceFor(repo *Repository, config core.ProjectConfig, start int) core.RelicService {
	var mutex sync.Mutex
	next := start
	return core.RelicService{
		Config: config,
		Reader: repo,
		Writer: repo,
		IDs: core.IDSourceFunc(func() (string, error) {
			mutex.Lock()
			defer mutex.Unlock()
			id := relicTestID(next)
			next++
			return id, nil
		}),
		Now:   func() time.Time { return relicTestTime },
		Actor: relicTestActor,
	}
}

func relicJSONString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRelicShowReturnsFieldsAndBodiesOnRequest(t *testing.T) {
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	service := relicServiceFor(repo, config, 100000)
	created, err := service.Create(ctx, core.RelicCreateInput{
		Title: "Task archive compaction",
		Type:  "spec",
		Parts: []core.RelicPartInput{
			{
				Type:   "section",
				Fields: map[string]json.RawMessage{"heading": relicJSONString(t, "Goals")},
				Body:   &core.RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
			},
			{Type: "decision", Fields: map[string]json.RawMessage{"outcome": relicJSONString(t, "accepted")}},
			{Type: "note", Body: &core.RelicBodyInput{Content: []byte("removed text")}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	relicID := created.Relic.ID
	goals, decision, note := created.Parts[0].ID, created.Parts[1].ID, created.Parts[2].ID
	tip, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic: %v", err)
	}
	writeRelicChild(t, repo, config, tip, nil,
		core.RelicOperation{ID: relicTestID(200000), Type: core.RelicOperationPartRemove, Part: note})

	plain, err := service.Show(ctx, relicID, core.RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if plain.ID != relicID || plain.Title != "Task archive compaction" || plain.Type != "spec" ||
		plain.Author != relicTestActor || plain.Head == "" || plain.CreatedAt == nil || plain.UpdatedAt == nil {
		t.Errorf("show header = %+v", plain)
	}
	encoded, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"format":"workbook.relic","version":1,`, `"schema":null`, `"links":[]`, `"properties":{}`} {
		if !bytes.Contains(encoded, []byte(fragment)) {
			t.Errorf("show JSON %s lacks %s", encoded, fragment)
		}
	}
	if len(plain.Parts) != 2 || plain.Parts[0].ID != goals || plain.Parts[1].ID != decision {
		t.Fatalf("show parts = %+v, want the two live parts", plain.Parts)
	}
	body := plain.Parts[0].Body
	if body == nil || body.Format != "md" || body.Version != goals || body.Bytes != 8 || body.Text != nil || body.Base64 != nil {
		t.Errorf("show body without Bodies = %+v", body)
	}
	if got := string(plain.Parts[0].Fields["heading"]); got != `"Goals"` {
		t.Errorf("heading = %s", got)
	}
	if plain.Parts[1].Body != nil || string(plain.Parts[1].Fields["outcome"]) != `"accepted"` {
		t.Errorf("decision part = %+v", plain.Parts[1])
	}

	withBodies, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show with bodies: %v", err)
	}
	if text := withBodies.Parts[0].Body.Text; text == nil || *text != "# Goals\n" || withBodies.Parts[0].Body.Base64 != nil {
		t.Errorf("show body with Bodies = %+v", withBodies.Parts[0].Body)
	}

	one, err := service.Show(ctx, strings.ToLower(relicID)+"#"+strings.ToLower(goals), core.RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show part: %v", err)
	}
	if len(one.Parts) != 1 || one.Parts[0].ID != goals || one.ID != relicID {
		t.Errorf("show part = %+v", one.Parts)
	}

	withRemoved, err := service.Show(ctx, relicID, core.RelicShowOptions{Removed: true})
	if err != nil {
		t.Fatalf("Show removed: %v", err)
	}
	if len(withRemoved.Parts) != 3 || withRemoved.Parts[2].ID != note || !withRemoved.Parts[2].Removed {
		t.Errorf("show with Removed = %+v", withRemoved.Parts)
	}

	removed, err := service.Show(ctx, relicID+"#"+note, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show removed part: %v", err)
	}
	if len(removed.Parts) != 1 || !removed.Parts[0].Removed || removed.Parts[0].Body == nil ||
		removed.Parts[0].Body.Text == nil || *removed.Parts[0].Body.Text != "removed text" {
		t.Errorf("show removed part = %+v", removed.Parts)
	}

	_, err = service.Show(ctx, relicID+"#01M9B", core.RelicShowOptions{})
	assertRelicCategory(t, err, core.CategoryNotFound, relicID, `"01M9B"`)
	_, err = service.Show(ctx, "WB-"+relicTestID(100000), core.RelicShowOptions{})
	assertRelicCategory(t, err, core.CategoryNotFound, "no relic matches")
}

func TestRelicExportRoundTripsEmptyAndBinaryBodies(t *testing.T) {
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceFor(repo, config, 100000)
		binary := []byte{0xff, 0xfe, 0x00}
		created, err := service.Create(ctx, core.RelicCreateInput{
			Title: "Task archive compaction",
			Type:  "spec",
			Properties: map[string]json.RawMessage{
				"summary": relicJSONString(t, "a <b> & c"),
				"count":   json.RawMessage(`1.0`),
				"meta":    json.RawMessage(`{"z": 1, "a": [true, null]}`),
			},
			Parts: []core.RelicPartInput{
				{
					Type:   "section",
					Fields: map[string]json.RawMessage{"heading": relicJSONString(t, "<Goals> & more"), "level": json.RawMessage(`2`)},
					Body:   &core.RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
				},
				{Type: "empty", Body: &core.RelicBodyInput{Format: "txt", Content: []byte{}}},
				{Type: "binary", Body: &core.RelicBodyInput{Content: binary}},
				{Type: "note", Fields: map[string]json.RawMessage{"kind": relicJSONString(t, "aside")}},
			},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		first, err := service.Export(ctx, created.Relic.ID)
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		if first.Format != core.RelicDocumentFormat || first.Version != 1 {
			t.Errorf("export format, version = %q, %d", first.Format, first.Version)
		}
		if got := string(first.Properties["summary"]); got != "\"a \\u003cb\\u003e \\u0026 c\"" {
			t.Errorf("exported summary = %s, want the canonical form", got)
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := core.DecodeRelicDocument(encoded)
		if err != nil {
			t.Fatalf("DecodeRelicDocument: %v", err)
		}
		input, err := core.RelicCreateInputFromDocument(decoded)
		if err != nil {
			t.Fatalf("RelicCreateInputFromDocument: %v", err)
		}
		again, err := service.Create(ctx, input)
		if err != nil {
			t.Fatalf("Create from document: %v", err)
		}
		second, err := service.Export(ctx, again.Relic.ID)
		if err != nil {
			t.Fatalf("Export again: %v", err)
		}

		if first.ID == second.ID {
			t.Fatalf("both relics have ID %s", first.ID)
		}
		if first.Title != second.Title || first.Type != second.Type {
			t.Errorf("title, type = %q %q, then %q %q", first.Title, first.Type, second.Title, second.Type)
		}
		if !reflect.DeepEqual(first.Properties, second.Properties) {
			t.Errorf("properties = %s, then %s", first.Properties, second.Properties)
		}
		if len(first.Parts) != 4 || len(second.Parts) != 4 {
			t.Fatalf("parts = %d, then %d; want 4", len(first.Parts), len(second.Parts))
		}
		wantBodies := [][]byte{[]byte("# Goals\n"), {}, binary, nil}
		for index := range first.Parts {
			a, b := first.Parts[index], second.Parts[index]
			if a.Type != b.Type || !reflect.DeepEqual(a.Fields, b.Fields) {
				t.Errorf("part %d = %s %s, then %s %s", index, a.Type, a.Fields, b.Type, b.Fields)
			}
			for label, part := range map[string]core.RelicDocumentPart{"first": a, "second": b} {
				got := relicDocumentBodyBytes(t, part.Body)
				if (got == nil) != (wantBodies[index] == nil) || !bytes.Equal(got, wantBodies[index]) {
					t.Errorf("%s export part %d body = %v, want %v", label, index, got, wantBodies[index])
				}
			}
			if (a.Body == nil) != (b.Body == nil) || (a.Body != nil && a.Body.Format != b.Body.Format) {
				t.Errorf("part %d body = %+v, then %+v", index, a.Body, b.Body)
			}
		}
		if got := string(first.Parts[0].Fields["heading"]); got != "\"\\u003cGoals\\u003e \\u0026 more\"" {
			t.Errorf("exported heading = %s, want the canonical form", got)
		}
		for label, document := range map[string]core.RelicDocument{"first": first, "second": second} {
			body := document.Parts[2].Body
			if body.Base64 == nil || body.Text != nil {
				t.Errorf("%s binary body = %+v, want base64", label, body)
			}
			if empty := document.Parts[1].Body; empty.Text == nil || *empty.Text != "" || empty.Bytes != 0 {
				t.Errorf("%s empty body = %+v, want empty text", label, empty)
			}
		}
	})
}

func relicDocumentBodyBytes(t *testing.T, body *core.RelicDocumentBody) []byte {
	t.Helper()
	if body == nil {
		return nil
	}
	document := core.RelicDocument{
		Format: core.RelicDocumentFormat, Version: 1, Title: "x", Type: "x",
		Parts: []core.RelicDocumentPart{{Type: "x", Body: body}},
	}
	input, err := core.RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatalf("body %+v cannot be read: %v", body, err)
	}
	content := input.Parts[0].Body.Content
	if content == nil {
		content = []byte{}
	}
	return content
}

// relicRefHead returns the commit a relic's ref points at.
func relicRefHead(t *testing.T, repo *Repository, relicID string) string {
	t.Helper()
	return gitOutput(t, repo, "rev-parse", "--verify", relicRef(relicID))
}

func TestRelicRemoveAndRestorePart(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	service := relicServiceFor(repo, config, 100000)
	created, err := service.Create(ctx, core.RelicCreateInput{
		Title: "Task archive compaction",
		Type:  "spec",
		Parts: []core.RelicPartInput{
			{
				Type:   "section",
				Fields: map[string]json.RawMessage{"heading": relicJSONString(t, "Goals")},
				Body:   &core.RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
			},
			{
				Type:   "decision",
				Fields: map[string]json.RawMessage{"outcome": relicJSONString(t, "accepted"), "votes": json.RawMessage(`3`)},
				Body:   &core.RelicBodyInput{Format: "txt", Content: []byte("we do it")},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	relicID := created.Relic.ID
	goals, decision := created.Parts[0].ID, created.Parts[1].ID
	before, err := service.Show(ctx, relicID+"#"+decision, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show before: %v", err)
	}

	removed, err := service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditRemovePart, Part: strings.ToLower(decision)}})
	if err != nil || !removed.Changed || removed.Relic.Parts != 1 {
		t.Fatalf("remove = %+v, %v; want one live part left", removed, err)
	}
	shown, err := service.Show(ctx, relicID, core.RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(shown.Parts) != 1 || shown.Parts[0].ID != goals {
		t.Fatalf("show after the remove = %+v, want only the live part", shown.Parts)
	}
	withRemoved, err := service.Show(ctx, relicID, core.RelicShowOptions{Removed: true})
	if err != nil {
		t.Fatalf("Show removed: %v", err)
	}
	if len(withRemoved.Parts) != 2 || withRemoved.Parts[1].ID != decision || !withRemoved.Parts[1].Removed ||
		!reflect.DeepEqual(withRemoved.Parts[1].Fields, before.Parts[0].Fields) {
		t.Fatalf("show with removed = %+v, want the removed part with its fields", withRemoved.Parts)
	}

	again, err := service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditRemovePart, Part: decision}})
	if err != nil || again.Changed || again.Relic.Head != removed.Relic.Head {
		t.Fatalf("removing a removed part = %+v, %v; want no change", again, err)
	}
	if head := relicRefHead(t, repo, relicID); head != removed.Relic.Head {
		t.Fatalf("ref = %s after a remove that changed nothing, want %s", head, removed.Relic.Head)
	}

	restored, err := service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditRestorePart, Part: decision}})
	if err != nil || !restored.Changed || restored.Relic.Parts != 2 {
		t.Fatalf("restore = %+v, %v; want two live parts", restored, err)
	}
	after, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show after: %v", err)
	}
	if len(after.Parts) != 2 || after.Parts[0].ID != goals {
		t.Fatalf("show after the restore = %+v", after.Parts)
	}
	// The same ID, fields, body text and version, at the same place.
	if !reflect.DeepEqual(after.Parts[1], before.Parts[0]) {
		t.Errorf("restored part = %+v, body %+v; want %+v, body %+v",
			after.Parts[1], after.Parts[1].Body, before.Parts[0], before.Parts[0].Body)
	}
	if body := after.Parts[1].Body; body == nil || body.Text == nil || *body.Text != "we do it" || body.Version != decision {
		t.Errorf("restored body = %+v", body)
	}
	assertRelicHistoryValid(t, repo, config, relicID, restored.Relic.Head, 3)
}

func TestRelicDeleteAndRestore(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceFor(repo, config, 100000)
		created, err := service.Create(ctx, core.RelicCreateInput{
			Title:      "Task archive compaction",
			Type:       "spec",
			Properties: map[string]json.RawMessage{"status": relicJSONString(t, "draft")},
			Parts: []core.RelicPartInput{
				{
					Type:   "section",
					Fields: map[string]json.RawMessage{"heading": relicJSONString(t, "Goals")},
					Body:   &core.RelicBodyInput{Format: "md", Content: []byte("# Goals\n")},
				},
				{Type: "binary", Body: &core.RelicBodyInput{Content: []byte{0xff, 0xfe, 0x00}}},
				{Type: "note", Fields: map[string]json.RawMessage{"kind": relicJSONString(t, "aside")}},
			},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		relicID := created.Relic.ID
		goals := created.Parts[0].ID
		updated, err := service.Update(ctx, relicID, []core.RelicEdit{
			{Kind: core.RelicEditSetProperty, Name: "status", Value: relicJSONString(t, "approved")},
			{
				Kind: core.RelicEditPart, Part: goals, Base: goals,
				Body:   &core.RelicBodyInput{Format: "md", Content: []byte("# Goals, revised\n")},
				Fields: []core.RelicFieldEdit{{Name: "level", Value: json.RawMessage(`2`)}},
			},
		})
		if err != nil || !updated.Changed {
			t.Fatalf("Update = %+v, %v", updated, err)
		}
		before, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
		if err != nil {
			t.Fatalf("Show before: %v", err)
		}

		deleted, err := service.Delete(ctx, relicID)
		if err != nil || !deleted.Changed || !deleted.Relic.Deleted {
			t.Fatalf("Delete = %+v, %v", deleted, err)
		}
		if head := relicRefHead(t, repo, relicID); head != deleted.Relic.Head {
			t.Fatalf("ref = %s after the delete, want %s", head, deleted.Relic.Head)
		}
		listed, err := service.List(ctx, core.RelicListFilter{})
		if err != nil || len(listed) != 0 {
			t.Fatalf("List after the delete = %+v, %v; want none", listed, err)
		}
		everything, err := service.List(ctx, core.RelicListFilter{IncludeDeleted: true})
		if err != nil || len(everything) != 1 || everything[0].ID != relicID || !everything[0].Deleted {
			t.Fatalf("List of everything after the delete = %+v, %v", everything, err)
		}
		shown, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
		if err != nil {
			t.Fatalf("Show a deleted relic: %v", err)
		}
		if !shown.Deleted || !reflect.DeepEqual(shown.Parts, before.Parts) {
			t.Errorf("deleted relic shown = deleted %v, parts %+v", shown.Deleted, shown.Parts)
		}
		_, err = service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditTitle, Title: "A new title"}})
		assertRelicCategory(t, err, core.CategoryValidation,
			"relic "+relicID+` is deleted; restore it first with "workbook relic restore `+relicID+`"`)
		again, err := service.Delete(ctx, relicID)
		if err != nil || again.Changed || again.Relic.Head != deleted.Relic.Head {
			t.Fatalf("Delete of a deleted relic = %+v, %v; want no change", again, err)
		}
		if head := relicRefHead(t, repo, relicID); head != deleted.Relic.Head {
			t.Fatalf("ref = %s after changes that were refused or changed nothing, want %s", head, deleted.Relic.Head)
		}

		restored, err := service.Restore(ctx, relicID)
		if err != nil || !restored.Changed || restored.Relic.Deleted || restored.Relic.Parts != 3 {
			t.Fatalf("Restore = %+v, %v", restored, err)
		}
		after, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true})
		if err != nil {
			t.Fatalf("Show after: %v", err)
		}
		if after.Deleted || after.Title != before.Title || !reflect.DeepEqual(after.Properties, before.Properties) {
			t.Errorf("restored relic = deleted %v, %q, %s", after.Deleted, after.Title, after.Properties)
		}
		if !reflect.DeepEqual(after.Parts, before.Parts) {
			t.Errorf("restored parts = %+v, want %+v", after.Parts, before.Parts)
		}
		if text := after.Parts[0].Body.Text; text == nil || *text != "# Goals, revised\n" {
			t.Errorf("restored body = %+v", after.Parts[0].Body)
		}
		if !bytes.Equal(relicDocumentBodyBytes(t, after.Parts[1].Body), []byte{0xff, 0xfe, 0x00}) {
			t.Errorf("restored binary body = %+v", after.Parts[1].Body)
		}
		unchanged, err := service.Restore(ctx, relicID)
		if err != nil || unchanged.Changed || unchanged.Relic.Head != restored.Relic.Head {
			t.Fatalf("Restore of a live relic = %+v, %v; want no change", unchanged, err)
		}
		listed, err = service.List(ctx, core.RelicListFilter{})
		if err != nil || len(listed) != 1 || listed[0].Head != restored.Relic.Head {
			t.Fatalf("List after the restore = %+v, %v", listed, err)
		}

		// Create, update, delete, restore.
		if head := relicRefHead(t, repo, relicID); head != restored.Relic.Head {
			t.Fatalf("ref = %s, want %s", head, restored.Relic.Head)
		}
		assertRelicHistoryValid(t, repo, config, relicID, restored.Relic.Head, 4)
	})
}

// relicModelPart is what a test expects one part to hold.
type relicModelPart struct {
	id, partType    string
	fields          map[string]json.RawMessage
	body            []byte
	hasBody         bool
	format, version string
	removed         bool
}

// assertRelicMatchesModel asserts that show returns exactly the model's parts
// in the model's order: every part with its content when removed ones are
// asked for, and the live ones alone otherwise, which is read a second time
// only when a part is removed.
func assertRelicMatchesModel(
	t *testing.T,
	label string,
	service core.RelicService,
	relicID string,
	model []*relicModelPart,
	deleted bool,
) {
	t.Helper()
	ctx := context.Background()
	shown, err := service.Show(ctx, relicID, core.RelicShowOptions{Bodies: true, Removed: true})
	if err != nil {
		t.Fatalf("%s: Show: %v", label, err)
	}
	if shown.Deleted != deleted {
		t.Fatalf("%s: deleted = %v, want %v", label, shown.Deleted, deleted)
	}
	if len(shown.Parts) != len(model) {
		t.Fatalf("%s: show returned %d parts, want %d", label, len(shown.Parts), len(model))
	}
	var live []string
	for index, want := range model {
		got := shown.Parts[index]
		if got.ID != want.id || got.Type != want.partType || got.Removed != want.removed {
			t.Fatalf("%s: part %d = %s %s removed %v, want %s %s removed %v",
				label, index, got.ID, got.Type, got.Removed, want.id, want.partType, want.removed)
		}
		if !reflect.DeepEqual(got.Fields, want.fields) {
			t.Fatalf("%s: part %s fields = %s, want %s", label, want.id, got.Fields, want.fields)
		}
		if (got.Body != nil) != want.hasBody {
			t.Fatalf("%s: part %s body = %+v, want a body: %v", label, want.id, got.Body, want.hasBody)
		}
		if want.hasBody {
			if got.Body.Version != want.version || got.Body.Format != want.format || got.Body.Bytes != int64(len(want.body)) {
				t.Fatalf("%s: part %s body = %+v, want version %s, format %q, %d bytes",
					label, want.id, got.Body, want.version, want.format, len(want.body))
			}
			if text := relicDocumentBodyBytes(t, got.Body); !bytes.Equal(text, want.body) {
				t.Fatalf("%s: part %s body text = %q, want %q", label, want.id, text, want.body)
			}
		}
		if !want.removed {
			live = append(live, want.id)
		}
	}
	if len(live) == len(model) {
		return
	}
	plain, err := service.Show(ctx, relicID, core.RelicShowOptions{})
	if err != nil {
		t.Fatalf("%s: Show live parts: %v", label, err)
	}
	shownLive := make([]string, len(plain.Parts))
	for index, part := range plain.Parts {
		shownLive[index] = part.ID
	}
	if !reflect.DeepEqual(shownLive, live) {
		t.Fatalf("%s: live parts = %v, want %v", label, shownLive, live)
	}
}

func TestRelicManyPartLifecycle(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		service := relicServiceFor(repo, config, 100000)
		const partCount = 200

		input := core.RelicCreateInput{Title: "A long plan", Type: "plan"}
		model := make([]*relicModelPart, partCount)
		for index := range model {
			part := &relicModelPart{
				partType: "section",
				fields: map[string]json.RawMessage{
					"heading": relicJSONString(t, fmt.Sprintf("Part %d", index)),
					"number":  json.RawMessage(fmt.Sprint(index)),
				},
			}
			partInput := core.RelicPartInput{Type: part.partType, Fields: part.fields}
			// Every tenth part starts without a body.
			if index%10 != 9 {
				part.body, part.hasBody, part.format = []byte(fmt.Sprintf("body %d\n", index)), true, "md"
				partInput.Body = &core.RelicBodyInput{Format: part.format, Content: part.body}
			}
			model[index] = part
			input.Parts = append(input.Parts, partInput)
		}
		created, err := service.Create(ctx, input)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		relicID := created.Relic.ID
		for index, part := range created.Parts {
			model[index].id = part.ID
			if model[index].hasBody {
				model[index].version = part.ID
			}
		}
		commits := 1
		byID := func(id string) *relicModelPart {
			for _, part := range model {
				if part.id == id {
					return part
				}
			}
			t.Fatalf("the model has no part %s", id)
			return nil
		}
		update := func(label string, edits ...core.RelicEdit) core.RelicMutationResult {
			t.Helper()
			result, err := service.Update(ctx, relicID, edits)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if result.Changed {
				commits++
			}
			return result
		}
		assertRelicMatchesModel(t, "create", service, relicID, model, false)

		// Twenty body saves, one commit each, each from the version the part is
		// at: fifteen parts, five of them saved a second time. Part 49
		// has no body yet, so its save is a first body with no base. Every other
		// save also sets a field and clears one.
		for save := range 20 {
			part := model[(save%15)*7]
			edit := core.RelicEdit{
				Kind: core.RelicEditPart, Part: part.id, Base: part.version,
				Body: &core.RelicBodyInput{Format: "txt", Content: []byte(fmt.Sprintf("save %d of part %s\n", save, part.id))},
			}
			fields := maps.Clone(part.fields)
			if save%2 == 0 {
				edit.Fields = []core.RelicFieldEdit{
					{Name: "saves", Value: json.RawMessage(fmt.Sprint(save))},
					{Name: "number", Clear: true},
				}
				fields["saves"] = json.RawMessage(fmt.Sprint(save))
				delete(fields, "number")
			}
			label := fmt.Sprintf("body save %d", save)
			result := update(label, edit)
			if !result.Changed || len(result.Parts) != 1 || result.Parts[0].Body == nil {
				t.Fatalf("%s: result = %+v", label, result)
			}
			version := result.Parts[0].Body.Version
			if version == part.version || version == "" {
				t.Fatalf("%s: version = %q, was %q; want a new one", label, version, part.version)
			}
			part.fields, part.body, part.hasBody, part.format, part.version = fields, edit.Body.Content, true, "txt", version
		}
		assertRelicMatchesModel(t, "body saves", service, relicID, model, false)
		// A save from a version that has since been replaced is refused.
		stale := model[0]
		_, err = service.Update(ctx, relicID, []core.RelicEdit{{
			Kind: core.RelicEditPart, Part: stale.id, Base: stale.id, Body: &core.RelicBodyInput{Content: []byte("stale")},
		}})
		assertRelicCategory(t, err, core.CategoryStaleWrite, stale.version, stale.id)

		// Thirty moves: five as a commit each, then twenty-five in one commit,
		// where each is placed against what the ones before it left.
		// Each takes the part at one place in the model to just before or just
		// after the part at another.
		move := func(step int) core.RelicEdit {
			from := (step*37 + 5) % partCount
			to := (step*53 + 11) % partCount
			if to == from {
				to = (to + 1) % partCount
			}
			moved, anchor := model[from], model[to]
			edit := core.RelicEdit{Kind: core.RelicEditMovePart, Part: moved.id}
			model = slices.Delete(model, from, from+1)
			at := slices.Index(model, anchor)
			if step%2 == 0 {
				edit.Before = anchor.id
			} else {
				edit.After = anchor.id
				at++
			}
			model = slices.Insert(model, at, moved)
			return edit
		}
		for step := range 5 {
			label := fmt.Sprintf("move %d", step)
			if result := update(label, move(step)); !result.Changed {
				t.Fatalf("%s: changed nothing", label)
			}
		}
		assertRelicMatchesModel(t, "five moves", service, relicID, model, false)
		var moves []core.RelicEdit
		for step := 5; step < 30; step++ {
			moves = append(moves, move(step))
		}
		if result := update("twenty-five moves at once", moves...); !result.Changed {
			t.Fatal("twenty-five moves at once changed nothing")
		}
		assertRelicMatchesModel(t, "twenty-five moves at once", service, relicID, model, false)

		// Ten removes in one commit, then five of those restored in another. A
		// removed part keeps its place, so the model's order stands.
		var removes, restores []core.RelicEdit
		for index := range 10 {
			part := model[index*20+3]
			part.removed = true
			removes = append(removes, core.RelicEdit{Kind: core.RelicEditRemovePart, Part: part.id})
			if index%2 == 0 {
				restores = append(restores, core.RelicEdit{Kind: core.RelicEditRestorePart, Part: part.id})
			}
		}
		if result := update("ten removes", removes...); result.Relic.Parts != partCount-10 {
			t.Fatalf("live parts after ten removes = %d, want %d", result.Relic.Parts, partCount-10)
		}
		assertRelicMatchesModel(t, "ten removes", service, relicID, model, false)
		for _, restore := range restores {
			byID(restore.Part).removed = false
		}
		if result := update("five restores", restores...); result.Relic.Parts != partCount-5 {
			t.Fatalf("live parts after five restores = %d, want %d", result.Relic.Parts, partCount-5)
		}
		assertRelicMatchesModel(t, "five restores", service, relicID, model, false)

		deleted, err := service.Delete(ctx, relicID)
		if err != nil || !deleted.Changed {
			t.Fatalf("Delete = %+v, %v", deleted, err)
		}
		commits++
		assertRelicMatchesModel(t, "delete", service, relicID, model, true)
		restored, err := service.Restore(ctx, relicID)
		if err != nil || !restored.Changed {
			t.Fatalf("Restore = %+v, %v", restored, err)
		}
		commits++
		assertRelicMatchesModel(t, "restore", service, relicID, model, false)

		// One create, twenty saves, five moves and one of twenty-five, a remove
		// of parts, a restore of parts, a delete and a restore.
		if commits != 31 {
			t.Fatalf("the test made %d commits, want 31", commits)
		}
		assertRelicHistoryValid(t, repo, config, relicID, restored.Relic.Head, commits)
	})
}

func TestRelicUpdateRefusesANewerWritersRelic(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("a body\n"))
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID
	forged := forgeNewerRelicTip(t, repo, config, root)
	service := relicServiceFor(repo, config, 100000)

	// The relic can still be read.
	shown, err := service.Show(ctx, relicID, core.RelicShowOptions{})
	if err != nil || shown.Head != forged || !shown.Retired {
		t.Fatalf("Show = head %s, retired %v, %v; want the newer writer's commit %s", shown.Head, shown.Retired, err, forged)
	}

	for label, change := range map[string]func() (core.RelicMutationResult, error){
		"a retitle": func() (core.RelicMutationResult, error) {
			return service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditTitle, Title: "A title from the older build"}})
		},
		"a body save": func() (core.RelicMutationResult, error) {
			return service.Update(ctx, relicID, []core.RelicEdit{{
				Kind: core.RelicEditPart, Part: part.id, Base: part.id, Body: &core.RelicBodyInput{Content: []byte("x")},
			}})
		},
		"an update with no edits": func() (core.RelicMutationResult, error) { return service.Update(ctx, relicID, nil) },
		"a delete":                func() (core.RelicMutationResult, error) { return service.Delete(ctx, relicID) },
		"a restore":               func() (core.RelicMutationResult, error) { return service.Restore(ctx, relicID) },
	} {
		_, err := change()
		if core.CategoryOf(err) != core.CategoryNewerWriter || !strings.Contains(err.Error(), relicID) {
			t.Fatalf("%s: error = %v, want a newer-writer refusal naming %s", label, err, relicID)
		}
		if head := relicRefHead(t, repo, relicID); head != forged {
			t.Fatalf("%s: ref = %s, want it unchanged at %s", label, head, forged)
		}
	}
}
