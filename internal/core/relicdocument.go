package core

import (
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode/utf8"
)

// RelicDocumentFormat names the document relic show and export write and
// create --from-file reads.
const RelicDocumentFormat = "workbook.relic"

// RelicDocumentBody is a part's body in a relic document. Text carries a body
// that is valid UTF-8 and Base64 any other; both are absent when the body was
// not asked for. Format, Version and Bytes describe the stored body.
type RelicDocumentBody struct {
	Format  string  `json:"format,omitempty"`
	Version string  `json:"version,omitempty"`
	Bytes   int64   `json:"bytes"`
	Text    *string `json:"text,omitempty"`
	Base64  *string `json:"base64,omitempty"`
}

// RelicDocumentPart is one part in a relic document.
type RelicDocumentPart struct {
	ID       string                     `json:"id,omitempty"`
	Type     string                     `json:"type"`
	Position string                     `json:"position,omitempty"`
	Removed  bool                       `json:"removed,omitempty"`
	Fields   map[string]json.RawMessage `json:"fields"`
	Body     *RelicDocumentBody         `json:"body,omitempty"`
}

// RelicDocument is a relic as show and export present it, and what create
// --from-file reads. Show leaves Format and Version unset; export sets them.
type RelicDocument struct {
	Format     string                     `json:"format"`
	Version    int                        `json:"version"`
	ID         string                     `json:"id,omitempty"`
	Head       string                     `json:"head,omitempty"`
	Title      string                     `json:"title"`
	Type       string                     `json:"type"`
	Schema     json.RawMessage            `json:"schema"`
	Author     string                     `json:"author,omitempty"`
	Retired    bool                       `json:"retired"`
	Deleted    bool                       `json:"deleted"`
	CreatedAt  *time.Time                 `json:"createdAt,omitempty"`
	UpdatedAt  *time.Time                 `json:"updatedAt,omitempty"`
	Properties map[string]json.RawMessage `json:"properties"`
	Parts      []RelicDocumentPart        `json:"parts"`
	Links      []json.RawMessage          `json:"links"`
}

// DecodeRelicDocument reads exactly one relic document, refusing a member this
// build does not know. What the document says is checked when it is turned
// into a create; see RelicCreateInputFromDocument.
func DecodeRelicDocument(data []byte) (RelicDocument, error) {
	var document RelicDocument
	if err := decodeOneJSON(data, &document); err != nil {
		return RelicDocument{}, Errorf(CategoryValidation, "the relic document cannot be read: %s", err.Error())
	}
	return document, nil
}

// RelicCreateInputFromDocument turns a relic document into a create. The new
// relic gets new IDs, so the document's ID, head, author, retired and deleted
// flags, dates, part IDs, positions and removed flags, and body versions and
// sizes are ignored; parts are placed in document order, every one live.
func RelicCreateInputFromDocument(document RelicDocument) (RelicCreateInput, error) {
	if document.Format != RelicDocumentFormat {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"the document's format is %q, not %q", document.Format, RelicDocumentFormat)
	}
	if document.Version != documentVersion {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"the relic document is version %d; this version of workbook reads version %d", document.Version, documentVersion)
	}
	if len(document.Schema) != 0 && string(document.Schema) != "null" {
		return RelicCreateInput{}, Errorf(CategoryValidation, "this version of workbook cannot create a relic that names a schema")
	}
	if len(document.Links) != 0 {
		return RelicCreateInput{}, Errorf(CategoryValidation, "this version of workbook cannot create a relic with links")
	}
	input := RelicCreateInput{
		Title:      document.Title,
		Type:       document.Type,
		Properties: document.Properties,
		Parts:      make([]RelicPartInput, 0, len(document.Parts)),
	}
	for index, part := range document.Parts {
		partInput := RelicPartInput{Type: part.Type, Fields: part.Fields}
		if part.Body != nil {
			content, err := relicDocumentBodyContent(index, *part.Body)
			if err != nil {
				return RelicCreateInput{}, err
			}
			partInput.Body = &RelicBodyInput{Format: part.Body.Format, Content: content}
		}
		input.Parts = append(input.Parts, partInput)
	}
	return input, nil
}

// relicDocumentBodyContent returns the bytes a document body carries, in
// exactly one of text and base64. index is the part's place in the document.
func relicDocumentBodyContent(index int, body RelicDocumentBody) ([]byte, error) {
	switch {
	case body.Text != nil && body.Base64 != nil:
		return nil, Errorf(CategoryValidation,
			"part %d of the document has a body with both text and base64; give exactly one", index+1)
	case body.Text != nil:
		return []byte(*body.Text), nil
	case body.Base64 != nil:
		content, err := base64.StdEncoding.DecodeString(*body.Base64)
		if err != nil {
			return nil, Errorf(CategoryValidation, "part %d of the document has a body whose base64 cannot be read: %s",
				index+1, err.Error())
		}
		return content, nil
	default:
		return nil, Errorf(CategoryValidation,
			"part %d of the document has a body with neither text nor base64; give exactly one", index+1)
	}
}

// newRelicDocument presents a relic and the given parts, which are in the
// order they are to appear, with content holding each part's fields and, when
// bodies were read, its body.
func newRelicDocument(snapshot RelicSnapshot, parts []RelicPart, content map[string]RelicPartContent) RelicDocument {
	relic := snapshot.Record.State.Relic
	createdAt, updatedAt := relic.CreatedAt, relic.UpdatedAt
	properties := make(map[string]json.RawMessage, len(relic.Properties))
	for name, value := range relic.Properties {
		properties[name] = value
	}
	document := RelicDocument{
		ID:         snapshot.Record.State.RelicID,
		Head:       snapshot.Head,
		Title:      relic.Title,
		Type:       relic.Type,
		Schema:     json.RawMessage("null"),
		Author:     relic.Author,
		Retired:    relic.Retired,
		Deleted:    relic.Deleted,
		CreatedAt:  &createdAt,
		UpdatedAt:  &updatedAt,
		Properties: properties,
		Parts:      make([]RelicDocumentPart, 0, len(parts)),
		Links:      []json.RawMessage{},
	}
	for _, part := range parts {
		document.Parts = append(document.Parts, newRelicDocumentPart(part, content[part.ID]))
	}
	return document
}

// newRelicDocumentPart presents one part with content's fields. The body's
// text is included only when content holds a body.
func newRelicDocumentPart(part RelicPart, content RelicPartContent) RelicDocumentPart {
	copied := make(map[string]json.RawMessage, len(content.Fields))
	for name, value := range content.Fields {
		copied[name] = value
	}
	presented := RelicDocumentPart{
		ID:       part.ID,
		Type:     part.Type,
		Position: part.Position,
		Removed:  part.Removed,
		Fields:   copied,
	}
	if part.Body != nil {
		body := &RelicDocumentBody{Format: part.Body.Format, Version: part.Body.Version, Bytes: part.Body.Bytes}
		if content.HasBody {
			if utf8.Valid(content.Body) {
				text := string(content.Body)
				body.Text = &text
			} else {
				encoded := base64.StdEncoding.EncodeToString(content.Body)
				body.Base64 = &encoded
			}
		}
		presented.Body = body
	}
	return presented
}
