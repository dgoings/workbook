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
	Dropped  *RelicPartDrop             `json:"dropped,omitempty"`
	Fields   map[string]json.RawMessage `json:"fields"`
	Body     *RelicDocumentBody         `json:"body,omitempty"`
}

// RelicDocument is a relic as show and export present it, and what create
// --from-file reads. Show and export both name the format and its version.
type RelicDocument struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	ID      string          `json:"id,omitempty"`
	Head    string          `json:"head,omitempty"`
	Title   string          `json:"title"`
	Type    string          `json:"type"`
	Schema  *RelicSchemaRef `json:"schema"`
	RelicConformance
	Author     string                     `json:"author,omitempty"`
	Retired    bool                       `json:"retired"`
	Deleted    bool                       `json:"deleted"`
	Compacted  *RelicCompaction           `json:"compacted,omitempty"`
	CreatedAt  *time.Time                 `json:"createdAt,omitempty"`
	UpdatedAt  *time.Time                 `json:"updatedAt,omitempty"`
	Properties map[string]json.RawMessage `json:"properties"`
	Parts      []RelicDocumentPart        `json:"parts"`
	Links      []RelicDocumentLink        `json:"links"`
	// LinkedFrom lists other relics' links to this relic or its parts. Only
	// show sets it; it is not part of the relic, so export leaves it out and
	// create --from-file refuses it.
	LinkedFrom []RelicIncomingLink `json:"linkedFrom,omitempty"`
	// Warnings is what a read has to say beside the document, such as links
	// whose targets could not be checked. It is not part of the document.
	Warnings []Warning `json:"-"`
}

// RelicSchemaRef names the schema version a relic is written under, as a
// relic document and a relic summary present it. Version is the version's
// number, left out when this clone's settings do not list the ID. A relic that
// names no schema has no RelicSchemaRef, which is written as null.
type RelicSchemaRef struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}

// ResolveRelicSchemaRef presents the schema version the relic names, with its
// type and number from the listing when the listing has it, and the relic's
// own type and no number when it does not. It is nil for an open relic.
func ResolveRelicSchemaRef(relic RelicData, listing RelicSchemaListing) *RelicSchemaRef {
	if relic.Schema == "" {
		return nil
	}
	if version, known := listing.ByID(relic.Schema); known {
		return &RelicSchemaRef{Type: version.Type, ID: version.ID, Version: version.Version}
	}
	return &RelicSchemaRef{Type: relic.Type, ID: relic.Schema}
}

// DecodeRelicDocument reads exactly one relic document. It refuses a member
// this build does not know, with names matched exactly, a member named twice
// in one object at any depth, and text that is not valid UTF-8 or holds a \u
// escape for half of a surrogate pair: each is something the decoder would
// read as a document the text never was. What the document says is checked
// when it is turned into a create; see RelicCreateInputFromDocument.
func DecodeRelicDocument(data []byte) (RelicDocument, error) {
	if err := checkRelicDocumentText(data); err != nil {
		return RelicDocument{}, err
	}
	var document RelicDocument
	if err := decodeOneJSON(data, &document); err != nil {
		return RelicDocument{}, Errorf(CategoryValidation, "the relic document cannot be read: %s", err.Error())
	}
	return document, nil
}

// RelicCreateInputFromDocument turns a relic document into a create. The new
// relic gets new IDs, so the document's ID, head, author, retired and deleted
// flags, compaction, dates, part IDs, positions and removed flags, and body
// versions and sizes are ignored; parts are placed in document order, every
// one live. A part that carries dropped is left out: it has no content.
func RelicCreateInputFromDocument(document RelicDocument) (RelicCreateInput, error) {
	if document.Format != RelicDocumentFormat {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"the document's format is %q, not %q", document.Format, RelicDocumentFormat)
	}
	if document.Version != documentVersion {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"the relic document is version %d; this version of workbook reads version %d", document.Version, documentVersion)
	}
	schema := ""
	if document.Schema != nil {
		// The ID names the version; its type and number are presentation, as
		// the document's dates are, and the create checks the ID against the
		// relic's type.
		if !isCanonicalULID(document.Schema.ID) {
			return RelicCreateInput{}, Errorf(CategoryValidation,
				"the document's schema names version %q, which is not a schema version ID; give its \"id\", or null for the type's newest version",
				document.Schema.ID)
		}
		schema = document.Schema.ID
	}
	if len(document.LinkedFrom) != 0 {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"linkedFrom lists other relics' links and is not part of a relic; remove it")
	}
	if len(document.Links) != 0 {
		return RelicCreateInput{}, Errorf(CategoryValidation,
			"this version of workbook cannot create a relic with links; empty the document's \"links\" list, create the relic, then add each link with \"workbook relic update <relic> --link <task> --rel <rel>\"")
	}
	input := RelicCreateInput{
		Title:      document.Title,
		Type:       document.Type,
		Schema:     schema,
		Properties: document.Properties,
		Parts:      make([]RelicPartInput, 0, len(document.Parts)),
	}
	for index, part := range document.Parts {
		if part.Dropped != nil {
			continue
		}
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
		Format:     RelicDocumentFormat,
		Version:    documentVersion,
		ID:         snapshot.Record.State.RelicID,
		Head:       snapshot.Head,
		Title:      relic.Title,
		Type:       relic.Type,
		Schema:     ResolveRelicSchemaRef(relic, RelicSchemaListing{}),
		Author:     relic.Author,
		Retired:    relic.Retired,
		Deleted:    relic.Deleted,
		Compacted:  relic.Compacted,
		CreatedAt:  &createdAt,
		UpdatedAt:  &updatedAt,
		Properties: properties,
		Parts:      make([]RelicDocumentPart, 0, len(parts)),
		Links:      []RelicDocumentLink{},
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
		Dropped:  part.Dropped,
		Fields:   copied,
	}
	if part.Body != nil {
		body := &RelicDocumentBody{Format: part.Body.Format, Version: part.Body.Version, Bytes: part.Body.Bytes}
		if content.HasBody {
			body.Text, body.Base64 = relicBodyTextOrBase64(content.Body)
		}
		presented.Body = body
	}
	return presented
}

// relicBodyTextOrBase64 presents a body's bytes for JSON, as exactly one of
// the two: the text when the bytes are valid UTF-8, which an empty body is,
// and their base64 when they are not. A relic document and a relic conflict
// both carry a body this way.
func relicBodyTextOrBase64(content []byte) (text, encoded *string) {
	if utf8.Valid(content) {
		value := string(content)
		return &value, nil
	}
	value := base64.StdEncoding.EncodeToString(content)
	return nil, &value
}
