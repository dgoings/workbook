package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
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
	for _, fragment := range []string{`"schema":null`, `"links":[]`, `"properties":{}`} {
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
