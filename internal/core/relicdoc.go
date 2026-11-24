package core

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"time"
)

const (
	relicOperationPackFormat = "workbook.relic-operation-pack"
	relicStateFormat         = "workbook.relic-state"
	relicLinksFormat         = "workbook.relic-links"
	relicPartFieldsFormat    = "workbook.relic-part-fields"
)

// RelicMinReader is the writer-format generation every relic operation type
// declares. See SupportedFormatGeneration for what generation six means.
const RelicMinReader = 6

type RelicOperationType string

const (
	RelicOperationCreate         RelicOperationType = "relic.create"
	RelicOperationTitleSet       RelicOperationType = "title.set"
	RelicOperationTombstone      RelicOperationType = "relic.tombstone"
	RelicOperationRestore        RelicOperationType = "relic.restore"
	RelicOperationPropertySet    RelicOperationType = "property.set"
	RelicOperationPropertyClear  RelicOperationType = "property.clear"
	RelicOperationPartAdd        RelicOperationType = "part.add"
	RelicOperationPartFieldSet   RelicOperationType = "part.field.set"
	RelicOperationPartFieldClear RelicOperationType = "part.field.clear"
	RelicOperationPartBodySet    RelicOperationType = "part.body.set"
	RelicOperationPartMove       RelicOperationType = "part.move"
	RelicOperationPartRemove     RelicOperationType = "part.remove"
	RelicOperationPartRestore    RelicOperationType = "part.restore"
)

// RelicCreateData is what relic.create records about the new relic.
type RelicCreateData struct {
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
}

// RelicBodyChange describes a body that part.add or part.body.set stores in the
// commit's tree: its label and size, never its text. Base is the body version
// a part.body.set started from, left out when the part had no body.
type RelicBodyChange struct {
	Format string `json:"format,omitempty"`
	Bytes  int64  `json:"bytes"`
	Base   string `json:"base,omitempty"`
}

// RelicOperation is one recorded change to a relic. Which members it carries
// depends on its type; see relicOperationShapes. A part.add creates the part
// whose ID is the operation's own ID.
type RelicOperation struct {
	ID       string             `json:"id"`
	Type     RelicOperationType `json:"type"`
	Relic    *RelicCreateData   `json:"relic,omitempty"`
	Title    string             `json:"title,omitempty"`
	Name     string             `json:"name,omitempty"`
	Value    json.RawMessage    `json:"value,omitempty"`
	Part     string             `json:"part,omitempty"`
	PartType string             `json:"partType,omitempty"`
	Position string             `json:"position,omitempty"`
	Body     *RelicBodyChange   `json:"body,omitempty"`
}

// RelicOperationPack is a relic commit's operation.json.
type RelicOperationPack struct {
	Format            string           `json:"format"`
	Version           int              `json:"version"`
	MinReader         int              `json:"minReader,omitempty"`
	ProjectID         string           `json:"projectId"`
	RelicID           string           `json:"relicId"`
	HistoryGeneration string           `json:"historyGeneration"`
	Actor             Actor            `json:"actor"`
	LogicalClock      uint64           `json:"logicalClock"`
	WallTime          time.Time        `json:"wallTime"`
	Operations        []RelicOperation `json:"operations"`
}

// RelicBody is a part's current body as the state lists it. Version is the ID
// of the operation that saved it.
type RelicBody struct {
	Format  string `json:"format,omitempty"`
	Version string `json:"version"`
	Bytes   int64  `json:"bytes"`
}

// RelicPart is one part as the state lists it, live or removed.
type RelicPart struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Position string     `json:"position"`
	Removed  bool       `json:"removed,omitempty"`
	Body     *RelicBody `json:"body,omitempty"`
}

// RelicData is the folded relic a state document carries. Schema is always
// nil (written as null) and Retired always false in this story; Properties and
// Parts are never nil, and Parts is ordered by position, then ID.
type RelicData struct {
	Title      string                     `json:"title"`
	Type       string                     `json:"type"`
	Schema     json.RawMessage            `json:"schema"`
	Author     string                     `json:"author"`
	Retired    bool                       `json:"retired"`
	Deleted    bool                       `json:"deleted"`
	CreatedAt  time.Time                  `json:"createdAt"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
	Properties map[string]json.RawMessage `json:"properties"`
	Parts      []RelicPart                `json:"parts"`
}

// RelicStateDocument is a relic commit's state.json.
type RelicStateDocument struct {
	Format       string    `json:"format"`
	Version      int       `json:"version"`
	MinReader    int       `json:"minReader,omitempty"`
	ProjectID    string    `json:"projectId"`
	RelicID      string    `json:"relicId"`
	History      History   `json:"history"`
	LogicalClock uint64    `json:"logicalClock"`
	Relic        RelicData `json:"relic"`
}

// RelicLinksDocument is a relic commit's links.json. Links is never nil, and
// is always empty in this story.
type RelicLinksDocument struct {
	Format  string            `json:"format"`
	Version int               `json:"version"`
	Links   []json.RawMessage `json:"links"`
}

// RelicFieldsDocument is a part's parts/<part-id>/fields.json. Fields is never
// nil.
type RelicFieldsDocument struct {
	Format  string                     `json:"format"`
	Version int                        `json:"version"`
	Fields  map[string]json.RawMessage `json:"fields"`
}

// RelicRecord is what one relic commit stores beside its operation.
type RelicRecord struct {
	State RelicStateDocument
	Links RelicLinksDocument
}

// RelicHead names a relic's ref tip.
type RelicHead struct{ RelicID, ObjectID string }

// RelicSnapshot is one relic commit read back: its object ID, its operation
// and the record beside it.
type RelicSnapshot struct {
	Head      string
	Operation RelicOperationPack
	Record    RelicRecord
}

// RelicPartContent is one part's content, in or out: what its fields.json and
// body blobs hold. HasBody tells an empty body from no body.
//
// On a write, a nil Fields leaves an existing part's fields.json as it is and
// gives a new part an empty one.
type RelicPartContent struct {
	Fields  map[string]json.RawMessage
	Body    []byte
	HasBody bool
}

// relicOperationMinReader declares the generation each relic operation type
// needs. Every type this story writes is generation six; a later story's types
// take higher numbers.
var relicOperationMinReader = map[RelicOperationType]int{
	RelicOperationCreate:         RelicMinReader,
	RelicOperationTitleSet:       RelicMinReader,
	RelicOperationTombstone:      RelicMinReader,
	RelicOperationRestore:        RelicMinReader,
	RelicOperationPropertySet:    RelicMinReader,
	RelicOperationPropertyClear:  RelicMinReader,
	RelicOperationPartAdd:        RelicMinReader,
	RelicOperationPartFieldSet:   RelicMinReader,
	RelicOperationPartFieldClear: RelicMinReader,
	RelicOperationPartBodySet:    RelicMinReader,
	RelicOperationPartMove:       RelicMinReader,
	RelicOperationPartRemove:     RelicMinReader,
	RelicOperationPartRestore:    RelicMinReader,
}

// NewRelicOperationPack builds a pack whose generation is derived from its
// operations, so no caller can forget the marker.
func NewRelicOperationPack(
	projectID, relicID, generation, actor string,
	clock uint64,
	wallTime time.Time,
	operations []RelicOperation,
) RelicOperationPack {
	minReader := 0
	for _, operation := range operations {
		if required := relicOperationMinReader[operation.Type]; required > minReader {
			minReader = required
		}
	}
	return RelicOperationPack{
		Format:            relicOperationPackFormat,
		Version:           documentVersion,
		MinReader:         minReader,
		ProjectID:         projectID,
		RelicID:           relicID,
		HistoryGeneration: generation,
		Actor:             Actor{ID: actor},
		LogicalClock:      clock,
		WallTime:          wallTime,
		Operations:        operations,
	}
}

// EmptyRelicLinks is the links document every relic commit stores in this
// story.
func EmptyRelicLinks() RelicLinksDocument {
	return RelicLinksDocument{Format: relicLinksFormat, Version: documentVersion, Links: []json.RawMessage{}}
}

// NewRelicFields wraps a part's fields in their document. A nil map becomes the
// empty one.
func NewRelicFields(fields map[string]json.RawMessage) RelicFieldsDocument {
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	return RelicFieldsDocument{Format: relicPartFieldsFormat, Version: documentVersion, Fields: fields}
}

// RequiresNewerReader reports a relic pack this build must not fold.
func (pack RelicOperationPack) RequiresNewerReader() bool {
	return pack.MinReader > SupportedFormatGeneration
}

// RequiresNewerReader reports a relic state this build must not fold past.
func (state RelicStateDocument) RequiresNewerReader() bool {
	return state.MinReader > SupportedFormatGeneration
}

// LiveParts returns the parts that are not removed, in stored order.
func (data RelicData) LiveParts() []RelicPart {
	live := make([]RelicPart, 0, len(data.Parts))
	for _, part := range data.Parts {
		if !part.Removed {
			live = append(live, part)
		}
	}
	return live
}

// Part finds a part by ID, live or removed.
func (data RelicData) Part(id string) (RelicPart, bool) {
	for _, part := range data.Parts {
		if part.ID == id {
			return part, true
		}
	}
	return RelicPart{}, false
}

// newerWriterRelic is the refusal for one relic written by a newer Workbook.
func newerWriterRelic(relicID string) error {
	return newerWriter("relic %s was written by a newer workbook; upgrade workbook to change it", relicID)
}

// DecodeRelicOperationPack strictly decodes a relic pack, unless the pack
// declares a generation this build cannot fold: then it is read leniently and
// returned marked, so a reader can name the relic and every fold refuses it.
func DecodeRelicOperationPack(data []byte) (RelicOperationPack, error) {
	var pack RelicOperationPack
	if err := decodeOneJSON(data, &pack); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return RelicOperationPack{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &pack); lenientErr != nil {
			return RelicOperationPack{}, newerWriter(
				"a relic operation pack written by a newer workbook could not be read; upgrade workbook")
		}
		pack.MinReader = generation
	}
	if pack.RequiresNewerReader() {
		if pack.Format != relicOperationPackFormat {
			return RelicOperationPack{}, corrupt("unsupported relic operation pack format %q", pack.Format)
		}
		if strings.TrimSpace(pack.RelicID) == "" {
			return RelicOperationPack{}, newerWriter(
				"a relic operation pack written by a newer workbook names no relic; upgrade workbook")
		}
		if len(pack.Operations) == 0 {
			return RelicOperationPack{}, newerWriterRelic(pack.RelicID)
		}
		return pack, nil
	}
	if err := validateRelicOperationPackDocument(pack); err != nil {
		return RelicOperationPack{}, err
	}
	return pack, nil
}

// DecodeRelicStateDocument strictly decodes a relic state, with the same
// exception the pack makes for a newer generation. A newer state is normalized
// rather than checked, so a reader can still show the relic.
func DecodeRelicStateDocument(data []byte) (RelicStateDocument, error) {
	var state RelicStateDocument
	if err := decodeOneJSON(data, &state); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return RelicStateDocument{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &state); lenientErr != nil {
			return RelicStateDocument{}, newerWriter(
				"a relic state written by a newer workbook could not be read; upgrade workbook")
		}
		state.MinReader = generation
	}
	state.Relic.Schema = normalizeRelicSchema(state.Relic.Schema)
	if state.RequiresNewerReader() {
		if state.Format != relicStateFormat {
			return RelicStateDocument{}, corrupt("unsupported relic state format %q", state.Format)
		}
		if strings.TrimSpace(state.RelicID) == "" {
			return RelicStateDocument{}, newerWriter(
				"a relic state written by a newer workbook names no relic; upgrade workbook")
		}
		if _, _, ok := ParseRelicID(state.RelicID); !ok {
			return RelicStateDocument{}, newerWriterRelic(state.RelicID)
		}
		if state.Relic.Properties == nil {
			state.Relic.Properties = map[string]json.RawMessage{}
		}
		if state.Relic.Parts == nil {
			state.Relic.Parts = []RelicPart{}
		}
		return state, nil
	}
	if err := validateRelicStateDocument(state); err != nil {
		return RelicStateDocument{}, err
	}
	return state, nil
}

// DecodeRelicLinksDocument strictly decodes a relic's links.json.
func DecodeRelicLinksDocument(data []byte) (RelicLinksDocument, error) {
	var links RelicLinksDocument
	if err := decodeOneJSON(data, &links); err != nil {
		return RelicLinksDocument{}, err
	}
	if err := validateRelicLinksDocument(links); err != nil {
		return RelicLinksDocument{}, err
	}
	return links, nil
}

// DecodeRelicFieldsDocument strictly decodes a part's fields.json. Each value
// is held to the limit for one field value. How large the fields are together
// is a total, checked only when a change makes it grow, so it is not checked
// here; whoever read the blob from storage bounded its size.
func DecodeRelicFieldsDocument(data []byte) (RelicFieldsDocument, error) {
	var fields RelicFieldsDocument
	if err := decodeOneJSON(data, &fields); err != nil {
		return RelicFieldsDocument{}, err
	}
	if err := validateRelicFieldsDocument(fields); err != nil {
		return RelicFieldsDocument{}, err
	}
	return fields, nil
}

// normalizeRelicSchema reads a stored null schema as absent, which is how a
// state built in memory holds it.
func normalizeRelicSchema(schema json.RawMessage) json.RawMessage {
	if string(schema) == "null" {
		return nil
	}
	return schema
}

// validateRelicMarker checks a relic document's generation: above this build's
// it was written by a newer Workbook, and anything but six is corrupt.
func validateRelicMarker(document string, minReader int, relicID string) error {
	if minReader > SupportedFormatGeneration {
		// This is also what stops EncodeDocument from writing a newer document
		// back out with the members it could not decode silently dropped.
		return newerWriterRelic(relicID)
	}
	if minReader != RelicMinReader {
		return corrupt("%s minimum reader generation %d is invalid; a relic declares %d",
			document, minReader, RelicMinReader)
	}
	return nil
}

func validateRelicOperationPackDocument(pack RelicOperationPack) error {
	if pack.Format != relicOperationPackFormat {
		return corrupt("unsupported relic operation pack format %q", pack.Format)
	}
	if pack.Version != documentVersion {
		return corrupt("unsupported relic operation pack version %d", pack.Version)
	}
	if err := validateRelicMarker("relic operation pack", pack.MinReader, pack.RelicID); err != nil {
		return err
	}
	if err := validateCanonicalULID("relic operation pack project ID", pack.ProjectID); err != nil {
		return err
	}
	if err := ValidateRelicIDShape(pack.RelicID); err != nil {
		return Wrap(CategoryCorruptData, "relic operation pack names an invalid relic", err)
	}
	if err := validateCanonicalULID("relic operation pack history generation", pack.HistoryGeneration); err != nil {
		return err
	}
	if strings.TrimSpace(pack.Actor.ID) == "" {
		return corrupt("relic operation pack actor ID must not be blank")
	}
	if pack.LogicalClock == 0 {
		return corrupt("relic operation pack logical clock must be positive")
	}
	if pack.WallTime.IsZero() {
		return corrupt("relic operation pack wall time must be present")
	}
	if len(pack.Operations) == 0 {
		return corrupt("relic operation pack must contain at least one operation")
	}
	seen := make(map[string]struct{}, len(pack.Operations))
	for _, operation := range pack.Operations {
		if err := validateRelicOperationDocument(operation); err != nil {
			return err
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			return corrupt("relic operation pack contains duplicate operation ID %q", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	return nil
}

// relicOperationMembers names the members, besides id and type, that a relic
// operation type carries. Anything outside its list must be absent, which is
// what stops a future build's operation from being folded as an older one.
type relicOperationMembers struct {
	relic, title, name, value, part, partType, position, body bool
}

var relicOperationShapes = map[RelicOperationType]relicOperationMembers{
	RelicOperationCreate:         {relic: true},
	RelicOperationTitleSet:       {title: true},
	RelicOperationTombstone:      {},
	RelicOperationRestore:        {},
	RelicOperationPropertySet:    {name: true, value: true},
	RelicOperationPropertyClear:  {name: true},
	RelicOperationPartAdd:        {partType: true, position: true, body: true},
	RelicOperationPartFieldSet:   {part: true, name: true, value: true},
	RelicOperationPartFieldClear: {part: true, name: true},
	RelicOperationPartBodySet:    {part: true, body: true},
	RelicOperationPartMove:       {part: true, position: true},
	RelicOperationPartRemove:     {part: true},
	RelicOperationPartRestore:    {part: true},
}

func validateRelicOperationDocument(operation RelicOperation) error {
	if err := validateOperationID(operation.ID); err != nil {
		return err
	}
	shape, known := relicOperationShapes[operation.Type]
	if !known {
		return corrupt("unsupported relic operation type %q", operation.Type)
	}
	present := relicOperationMembers{
		relic:    operation.Relic != nil,
		title:    operation.Title != "",
		name:     operation.Name != "",
		value:    len(operation.Value) != 0,
		part:     operation.Part != "",
		partType: operation.PartType != "",
		position: operation.Position != "",
		body:     operation.Body != nil,
	}
	// part.add is the one type with an optional member: a part may be added
	// without a body.
	if operation.Type == RelicOperationPartAdd {
		present.body = true
	}
	if present != shape {
		return corrupt("%s carries the wrong members", operation.Type)
	}

	if operation.Relic != nil {
		if err := validateStoredRelicTitle(operation.Relic.Title); err != nil {
			return Wrap(CategoryCorruptData, "relic.create carries an invalid title", err)
		}
		if err := ValidateRelicType(operation.Relic.Type); err != nil {
			return Wrap(CategoryCorruptData, "relic.create carries an invalid type", err)
		}
		if operation.Relic.CreatedAt.IsZero() {
			return corrupt("relic.create creation time must be present")
		}
	}
	if operation.Title != "" {
		if err := validateStoredRelicTitle(operation.Title); err != nil {
			return Wrap(CategoryCorruptData, "title.set carries an invalid title", err)
		}
	}
	if operation.Name != "" {
		kind := "property"
		if operation.Part != "" {
			kind = "field"
		}
		if err := ValidateRelicName(kind, operation.Name); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an invalid "+kind, err)
		}
	}
	if len(operation.Value) != 0 {
		kind := "property"
		if operation.Part != "" {
			kind = "field"
		}
		if err := validateStoredRelicValue(kind, operation.Value); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid value", err)
		}
	}
	if operation.Part != "" {
		if err := ValidatePartID(operation.Part); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an invalid part", err)
		}
	}
	if operation.PartType != "" {
		if err := ValidateRelicPartType(operation.PartType); err != nil {
			return Wrap(CategoryCorruptData, "part.add carries an invalid part type", err)
		}
	}
	if operation.Position != "" {
		if _, err := ParsePartPosition(operation.Position); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid position", err)
		}
	}
	if operation.Body != nil {
		if err := ValidateRelicBodyFormat(operation.Body.Format); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid body format", err)
		}
		if err := validateStoredRelicBodySize(operation.Body.Bytes); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid body", err)
		}
		if operation.Body.Base != "" {
			// Only an edit has a version it started from.
			if operation.Type != RelicOperationPartBodySet {
				return corrupt("%s carries the wrong members", operation.Type)
			}
			if err := validateCanonicalULID("part.body.set base", operation.Body.Base); err != nil {
				return err
			}
			// The edit's own ID is the version it saves, so naming itself as
			// the version it started from would relabel a body without a new
			// version.
			if operation.Body.Base == operation.ID {
				return corrupt("part.body.set %s names itself as its base", operation.ID)
			}
		}
	}
	return nil
}

// validateStoredRelicTitle requires a title in the form it is stored: valid,
// and already trimmed.
func validateStoredRelicTitle(title string) error {
	trimmed, err := ValidateRelicTitle(title)
	if err != nil {
		return err
	}
	if trimmed != title {
		return Errorf(CategoryValidation, "the relic title must be stored trimmed")
	}
	return nil
}

// validateStoredRelicValue requires a property or field value within the
// limit for one value and in its canonical form, which is exactly
// CanonicalRelicValue's output. kind is "property" or "field". The size is
// judged first, from the stored bytes alone.
func validateStoredRelicValue(kind string, value json.RawMessage) error {
	if err := checkRelicValueSize(kind, len(value)); err != nil {
		return err
	}
	canonical, err := CanonicalRelicValue(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, value) {
		return Errorf(CategoryValidation, "the value is not stored in canonical form")
	}
	return nil
}

func validateStoredRelicBodySize(size int64) error {
	if size < 0 {
		return Errorf(CategoryValidation, "a body size must not be negative")
	}
	if size > MaxRelicBodyBytes {
		return Errorf(CategoryValidation, "a body is %d bytes; the limit is %d bytes", size, MaxRelicBodyBytes)
	}
	return nil
}

// validateRelicValues checks a properties or fields object: every name
// valid, every value canonical and within the limit for one value. It never
// checks how many there are or how large they are together, which are totals.
func validateRelicValues(kind string, values map[string]json.RawMessage) error {
	for name, value := range values {
		if err := ValidateRelicName(kind, name); err != nil {
			return Wrap(CategoryCorruptData, "a stored "+kind+" name is invalid", err)
		}
		if err := validateStoredRelicValue(kind, value); err != nil {
			return Wrap(CategoryCorruptData, "a stored "+kind+" value is invalid", err)
		}
	}
	return nil
}

func validateRelicStateDocument(state RelicStateDocument) error {
	if state.Format != relicStateFormat {
		return corrupt("unsupported relic state format %q", state.Format)
	}
	if state.Version != documentVersion {
		return corrupt("unsupported relic state version %d", state.Version)
	}
	if err := validateRelicMarker("relic state", state.MinReader, state.RelicID); err != nil {
		return err
	}
	if err := validateCanonicalULID("relic state project ID", state.ProjectID); err != nil {
		return err
	}
	if err := ValidateRelicIDShape(state.RelicID); err != nil {
		return Wrap(CategoryCorruptData, "relic state names an invalid relic", err)
	}
	if err := validateCanonicalULID("relic state history generation", state.History.Generation); err != nil {
		return err
	}
	if state.History.CompactedFrom != nil {
		return corrupt("relic state compaction metadata is unsupported")
	}
	if state.LogicalClock == 0 {
		return corrupt("relic state logical clock must be positive")
	}
	return validateRelicData(state.Relic)
}

func validateRelicData(relic RelicData) error {
	if err := validateStoredRelicTitle(relic.Title); err != nil {
		return Wrap(CategoryCorruptData, "relic state carries an invalid title", err)
	}
	if err := ValidateRelicType(relic.Type); err != nil {
		return Wrap(CategoryCorruptData, "relic state carries an invalid type", err)
	}
	if len(relic.Schema) != 0 && string(relic.Schema) != "null" {
		return corrupt("relic state schema is unsupported")
	}
	if strings.TrimSpace(relic.Author) == "" {
		return corrupt("relic state author must not be blank")
	}
	if relic.Retired {
		return corrupt("relic state retirement is unsupported")
	}
	if relic.CreatedAt.IsZero() || relic.UpdatedAt.IsZero() {
		return corrupt("relic state creation and update times must be present")
	}
	if relic.Properties == nil {
		return corrupt("relic state properties must be an object")
	}
	if err := validateRelicValues("property", relic.Properties); err != nil {
		return err
	}
	if relic.Parts == nil {
		return corrupt("relic state parts must be a list")
	}
	return validateRelicParts(relic.Parts)
}

// validateRelicParts checks each part and that the list is ordered by
// position, then ID, with no ID twice. It never checks how many parts there
// are or how large their bodies are together.
func validateRelicParts(parts []RelicPart) error {
	seen := make(map[string]struct{}, len(parts))
	var previous *big.Rat
	for index, part := range parts {
		if err := ValidatePartID(part.ID); err != nil {
			return Wrap(CategoryCorruptData, "relic state lists an invalid part", err)
		}
		if _, duplicate := seen[part.ID]; duplicate {
			return corrupt("relic state lists part %s twice", part.ID)
		}
		seen[part.ID] = struct{}{}
		if err := ValidateRelicPartType(part.Type); err != nil {
			return Wrap(CategoryCorruptData, "relic state part "+part.ID+" has an invalid type", err)
		}
		position, err := ParsePartPosition(part.Position)
		if err != nil {
			return Wrap(CategoryCorruptData, "relic state part "+part.ID+" has an invalid position", err)
		}
		if previous != nil {
			order := previous.Cmp(position)
			if order > 0 || (order == 0 && parts[index-1].ID > part.ID) {
				return corrupt("relic state parts are not ordered by position, then ID")
			}
		}
		previous = position
		if part.Body != nil {
			if err := ValidateRelicBodyFormat(part.Body.Format); err != nil {
				return Wrap(CategoryCorruptData, "relic state part "+part.ID+" has an invalid body format", err)
			}
			if err := validateCanonicalULID("relic state body version", part.Body.Version); err != nil {
				return err
			}
			if err := validateStoredRelicBodySize(part.Body.Bytes); err != nil {
				return Wrap(CategoryCorruptData, "relic state part "+part.ID+" has an invalid body", err)
			}
		}
	}
	return nil
}

func validateRelicLinksDocument(links RelicLinksDocument) error {
	if links.Format != relicLinksFormat {
		return corrupt("unsupported relic links format %q", links.Format)
	}
	if links.Version != documentVersion {
		return corrupt("unsupported relic links version %d", links.Version)
	}
	if links.Links == nil {
		return corrupt("relic links must be a list")
	}
	if len(links.Links) != 0 {
		return corrupt("relic links are unsupported; a relic stores no links yet")
	}
	return nil
}

func validateRelicFieldsDocument(fields RelicFieldsDocument) error {
	if fields.Format != relicPartFieldsFormat {
		return corrupt("unsupported relic part fields format %q", fields.Format)
	}
	if fields.Version != documentVersion {
		return corrupt("unsupported relic part fields version %d", fields.Version)
	}
	if fields.Fields == nil {
		return corrupt("relic part fields must be an object")
	}
	return validateRelicValues("field", fields.Fields)
}
