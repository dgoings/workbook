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

// RelicMinReader is the writer-format generation the relic operation types of
// the first relic stories declare. See SupportedFormatGeneration for what
// generation six means; RelicCheckpointMinReader is seven.
const RelicMinReader = 6

// RelicBuiltInSchemaMinReader is the writer-format generation a relic pack or
// state declares when it names a built-in schema in a schema member. See
// SupportedFormatGeneration for what generation eight means.
const RelicBuiltInSchemaMinReader = 8

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
	// RelicOperationPartBodyMerge saves a body that combines a stale save
	// with the version that stood; see RelicBodyMerge.
	RelicOperationPartBodyMerge RelicOperationType = "part.body.merge"
	RelicOperationPartMove      RelicOperationType = "part.move"
	RelicOperationPartRemove    RelicOperationType = "part.remove"
	RelicOperationPartRestore   RelicOperationType = "part.restore"
	RelicOperationMigrate       RelicOperationType = "relic.migrate"
	RelicOperationRetire        RelicOperationType = "relic.retire"
	RelicOperationUnretire      RelicOperationType = "relic.unretire"
	// RelicOperationCheckpoint is the root of a compacted relic: the sole
	// operation of a parentless commit, carrying a summary of the history it
	// replaced and none of the relic's content.
	RelicOperationCheckpoint RelicOperationType = "history.checkpoint"
)

// RelicCreateData is what relic.create records about the new relic. Schema
// is the ID of the schema version the relic is written under, left out when
// its type has none.
type RelicCreateData struct {
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	Schema    string    `json:"schema,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// RelicBodyChange describes a body that part.add, part.body.set or
// part.body.merge stores in the commit's tree: its label and size, never its
// text. Base is the body version a part.body.set started from, left out when
// the part had no body, and the version a part.body.merge is applied onto.
type RelicBodyChange struct {
	Format string `json:"format,omitempty"`
	Bytes  int64  `json:"bytes"`
	Base   string `json:"base,omitempty"`
}

// RelicOperation is one recorded change to a relic. Which members it carries
// depends on its type; see relicOperationShapes. A part.add creates the part
// whose ID is the operation's own ID. A relic.migrate names the relic's new
// type in RelicType and the schema version it moves to in Schema, which is
// left out when the new type has no schema.
type RelicOperation struct {
	ID        string             `json:"id"`
	Type      RelicOperationType `json:"type"`
	Relic     *RelicCreateData   `json:"relic,omitempty"`
	Title     string             `json:"title,omitempty"`
	Name      string             `json:"name,omitempty"`
	Value     json.RawMessage    `json:"value,omitempty"`
	Part      string             `json:"part,omitempty"`
	PartType  string             `json:"partType,omitempty"`
	Position  string             `json:"position,omitempty"`
	Body      *RelicBodyChange   `json:"body,omitempty"`
	Link      *RelicLinkChange   `json:"link,omitempty"`
	LinkID    string             `json:"linkId,omitempty"`
	RelicType string             `json:"relicType,omitempty"`
	Schema    string             `json:"schema,omitempty"`
	// Checkpoint is the summary a history.checkpoint carries.
	Checkpoint *RelicCheckpointData `json:"checkpoint,omitempty"`
	// Merge is what a part.body.merge merged.
	Merge *RelicBodyMerge `json:"merge,omitempty"`
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
	ID       string `json:"id"`
	Type     string `json:"type"`
	Position string `json:"position"`
	Removed  bool   `json:"removed,omitempty"`
	// Dropped marks a stub: a removed part whose content a checkpoint dropped.
	// A stub has no body, and its fields.json is the empty one.
	Dropped *RelicPartDrop `json:"dropped,omitempty"`
	Body    *RelicBody     `json:"body,omitempty"`
}

// RelicData is the folded relic a state document carries. Schema is the ID of
// the schema version the relic names, empty for an open relic and written as
// null then (see MarshalJSON). Compacted is set once the relic's history
// starts at a checkpoint, and is left out of the document until then.
// Properties and Parts are never nil, and Parts is ordered by position, then
// ID.
type RelicData struct {
	Title      string                     `json:"title"`
	Type       string                     `json:"type"`
	Schema     string                     `json:"schema"`
	Author     string                     `json:"author"`
	Retired    bool                       `json:"retired"`
	Deleted    bool                       `json:"deleted"`
	Compacted  *RelicCompaction           `json:"compacted,omitempty"`
	CreatedAt  time.Time                  `json:"createdAt"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
	Properties map[string]json.RawMessage `json:"properties"`
	Parts      []RelicPart                `json:"parts"`
}

// relicDataJSON is RelicData as it is written: the same members in the same
// order, with an empty Schema written as null. A null schema decodes into
// RelicData as the empty string, so only encoding needs it.
type relicDataJSON struct {
	Title      string                     `json:"title"`
	Type       string                     `json:"type"`
	Schema     *string                    `json:"schema"`
	Author     string                     `json:"author"`
	Retired    bool                       `json:"retired"`
	Deleted    bool                       `json:"deleted"`
	Compacted  *RelicCompaction           `json:"compacted,omitempty"`
	CreatedAt  time.Time                  `json:"createdAt"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
	Properties map[string]json.RawMessage `json:"properties"`
	Parts      []RelicPart                `json:"parts"`
}

// MarshalJSON writes the relic with an open relic's schema as null, so the
// state of a relic that names no schema keeps the bytes it always had.
func (data RelicData) MarshalJSON() ([]byte, error) {
	written := relicDataJSON{
		Title:      data.Title,
		Type:       data.Type,
		Author:     data.Author,
		Retired:    data.Retired,
		Deleted:    data.Deleted,
		Compacted:  data.Compacted,
		CreatedAt:  data.CreatedAt,
		UpdatedAt:  data.UpdatedAt,
		Properties: data.Properties,
		Parts:      data.Parts,
	}
	if data.Schema != "" {
		schema := data.Schema
		written.Schema = &schema
	}
	return json.Marshal(written)
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

// RelicLinksDocument is a relic commit's links.json: the relic's live links,
// ordered by ID. Links is never nil. A removed link is kept only in history.
type RelicLinksDocument struct {
	Format  string      `json:"format"`
	Version int         `json:"version"`
	Links   []RelicLink `json:"links"`
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
	// Conformance is how the relic stands against the schema version it
	// names, as the local index judged it when it indexed this head. It is
	// not stored in Git, and only the index fills it; nil means not judged.
	Conformance *RelicConformance
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
// needs. The first relic stories' types are generation six; retiring and
// compaction are seven, and a later story's types take higher numbers.
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
	RelicOperationLinkAdd:        RelicMinReader,
	RelicOperationLinkRemove:     RelicMinReader,
	RelicOperationMigrate:        RelicMinReader,
	RelicOperationRetire:         RelicCheckpointMinReader,
	RelicOperationUnretire:       RelicCheckpointMinReader,
	RelicOperationCheckpoint:     RelicCheckpointMinReader,
	RelicOperationPartBodyMerge:  RelicMergeMinReader,
}

// relicPackMinReader is the generation a pack of these operations declares:
// the largest any of them needs. A type's generation comes from
// relicOperationMinReader, and a relic.create or relic.migrate that names a
// built-in schema needs RelicBuiltInSchemaMinReader as well.
func relicPackMinReader(operations []RelicOperation) int {
	minReader := 0
	for _, operation := range operations {
		required := relicOperationMinReader[operation.Type]
		schema := operation.Schema
		if operation.Type == RelicOperationCreate && operation.Relic != nil {
			schema = operation.Relic.Schema
		}
		if named := relicSchemaMinReader(schema); named > required {
			required = named
		}
		if required > minReader {
			minReader = required
		}
	}
	return minReader
}

// NewRelicOperationPack builds a pack whose generation is derived from its
// operations, so no caller can forget the marker.
func NewRelicOperationPack(
	projectID, relicID, generation, actor string,
	clock uint64,
	wallTime time.Time,
	operations []RelicOperation,
) RelicOperationPack {
	return RelicOperationPack{
		Format:            relicOperationPackFormat,
		Version:           documentVersion,
		MinReader:         relicPackMinReader(operations),
		ProjectID:         projectID,
		RelicID:           relicID,
		HistoryGeneration: generation,
		Actor:             Actor{ID: actor},
		LogicalClock:      clock,
		WallTime:          wallTime,
		Operations:        operations,
	}
}

// EmptyRelicLinks is the links document of a relic with no links.
func EmptyRelicLinks() RelicLinksDocument {
	return RelicLinksDocument{Format: relicLinksFormat, Version: documentVersion, Links: []RelicLink{}}
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

// validateRelicMarker checks a relic document's generation: above this build's
// it was written by a newer Workbook, and anything but six, seven, eight or
// nine is corrupt.
func validateRelicMarker(document string, minReader int, relicID string) error {
	if minReader > SupportedFormatGeneration {
		// This is also what stops EncodeDocument from writing a newer document
		// back out with the members it could not decode silently dropped.
		return newerWriterRelic(relicID)
	}
	switch minReader {
	case RelicMinReader, RelicCheckpointMinReader, RelicBuiltInSchemaMinReader, RelicMergeMinReader:
	default:
		return corrupt("%s minimum reader generation %d is invalid; a relic declares %d, %d, %d or %d",
			document, minReader, RelicMinReader, RelicCheckpointMinReader, RelicBuiltInSchemaMinReader, RelicMergeMinReader)
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
	if err := validateRelicTimeZone("relic operation pack wall time", pack.WallTime); err != nil {
		return err
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
	if err := validateRelicMergeOthers(pack.Operations); err != nil {
		return err
	}
	// A pack declares exactly the generation its operations need, so a retire
	// cannot reach a generation-six build marked as something it can fold, nor
	// a built-in name a generation-seven build.
	if required := relicPackMinReader(pack.Operations); pack.MinReader != required {
		return corrupt("relic operation pack minimum reader generation %d is not %d, the largest its operations need",
			pack.MinReader, required)
	}
	return nil
}

// relicOperationMembers names the members, besides id and type, that a relic
// operation type carries. Anything outside its list must be absent, which is
// what stops a future build's operation from being folded as an older one.
type relicOperationMembers struct {
	relic, title, name, value, part, partType, position, body, link, linkID, relicType, schema, checkpoint, merge bool
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
	RelicOperationPartBodyMerge:  {part: true, body: true, merge: true},
	RelicOperationPartMove:       {part: true, position: true},
	RelicOperationPartRemove:     {part: true},
	RelicOperationPartRestore:    {part: true},
	RelicOperationLinkAdd:        {link: true},
	RelicOperationLinkRemove:     {linkID: true},
	RelicOperationMigrate:        {relicType: true, schema: true},
	RelicOperationRetire:         {},
	RelicOperationUnretire:       {},
	RelicOperationCheckpoint:     {checkpoint: true},
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
		relic:      operation.Relic != nil,
		title:      operation.Title != "",
		name:       operation.Name != "",
		value:      len(operation.Value) != 0,
		part:       operation.Part != "",
		partType:   operation.PartType != "",
		position:   operation.Position != "",
		body:       operation.Body != nil,
		link:       operation.Link != nil,
		linkID:     operation.LinkID != "",
		relicType:  operation.RelicType != "",
		schema:     operation.Schema != "",
		checkpoint: operation.Checkpoint != nil,
		merge:      operation.Merge != nil,
	}
	// part.add and relic.migrate are the types with an optional member: a part
	// may be added without a body, and a relic may move to a type with no
	// schema.
	switch operation.Type {
	case RelicOperationPartAdd:
		present.body = true
	case RelicOperationMigrate:
		present.schema = true
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
		if operation.Relic.Schema != "" {
			if err := validateRelicSchemaID("relic.create schema", operation.Relic.Schema); err != nil {
				return err
			}
		}
		if operation.Relic.CreatedAt.IsZero() {
			return corrupt("relic.create creation time must be present")
		}
		if err := validateRelicTimeZone("relic.create creation time", operation.Relic.CreatedAt); err != nil {
			return err
		}
	}
	if operation.RelicType != "" {
		if err := ValidateRelicType(operation.RelicType); err != nil {
			return Wrap(CategoryCorruptData, "relic.migrate carries an invalid type", err)
		}
	}
	if operation.Schema != "" {
		if err := validateRelicSchemaID("relic.migrate schema", operation.Schema); err != nil {
			return err
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
	if operation.Link != nil {
		if err := validateRelicLinkChange("link.add", *operation.Link); err != nil {
			return err
		}
	}
	if operation.LinkID != "" {
		if err := validateCanonicalULID("link.remove link ID", operation.LinkID); err != nil {
			return err
		}
	}
	if operation.Checkpoint != nil {
		if err := validateRelicCheckpointSummary(operation.ID, *operation.Checkpoint); err != nil {
			return err
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
			if operation.Type != RelicOperationPartBodySet && operation.Type != RelicOperationPartBodyMerge {
				return corrupt("%s carries the wrong members", operation.Type)
			}
			if err := validateCanonicalULID(string(operation.Type)+" base", operation.Body.Base); err != nil {
				return err
			}
			// The edit's own ID is the version it saves, so naming itself as
			// the version it started from would relabel a body without a new
			// version.
			if operation.Body.Base == operation.ID {
				return corrupt("%s %s names itself as its base", operation.Type, operation.ID)
			}
		}
	}
	if operation.Type == RelicOperationPartBodyMerge {
		// A merge is always applied onto a version: the one that stood when
		// the stale save met it.
		if operation.Body.Base == "" {
			return corrupt("part.body.merge %s names no version it is applied onto", operation.ID)
		}
		if err := validateRelicBodyMerge(operation); err != nil {
			return err
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

// validateRelicTimeZone accepts a stored time with any RFC 3339 offset. The
// writer records UTC, but a project may store its relics in a zone of its own,
// and Go reads a time with an offset and writes it back in the same bytes, so
// the offset survives folding, replay, the index and every re-encoding. A
// stored document is canonical only while it is its own re-encoding, and a
// zero offset has one form there: Go writes +00:00 and -00:00 back as Z. A
// zero offset in any other location is what those decode to, and is refused
// here with that reason rather than later as bytes that do not match.
func validateRelicTimeZone(what string, stored time.Time) error {
	if _, offset := stored.Zone(); offset == 0 && stored.Location() != time.UTC {
		return corrupt("%s is not canonical: a zero offset is written Z, not +00:00 or -00:00", what)
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
		return Errorf(CategoryValidation, "the value is not in canonical form")
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
			return storedRelicValueError(kind, "name", err)
		}
		if err := validateStoredRelicValue(kind, value); err != nil {
			return storedRelicValueError(kind, "value", err)
		}
	}
	return nil
}

// storedRelicValueError reports a stored name or value that breaks a rule, as
// corrupt data. what is "name" or "value". The rule's own message opens with
// its subject, "a property name" or "the value", so the subject is said once,
// as in "a stored property name must not be empty", and not once here and
// again there.
func storedRelicValueError(kind, what string, err error) error {
	message := err.Error()
	for _, subject := range []string{"a " + kind + " " + what + " ", "the " + what + " "} {
		if rest, found := strings.CutPrefix(message, subject); found {
			return corrupt("a stored %s %s %s", kind, what, rest)
		}
	}
	return Wrap(CategoryCorruptData, "a stored "+kind+" "+what+" is invalid", err)
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
	// Not quoted, for the reason validateStateEnvelope gives.
	if state.History.CompactedFrom != nil && !objectIDPattern.MatchString(*state.History.CompactedFrom) {
		return corrupt("relic state history compactedFrom must be a Git object ID")
	}
	if state.LogicalClock == 0 {
		return corrupt("relic state logical clock must be positive")
	}
	return validateRelicData(state.Relic, state.History, state.MinReader)
}

// validateRelicData checks the relic a state carries. history and minReader
// are the state's, which the members that retiring and compaction brought
// depend on.
func validateRelicData(relic RelicData, history History, minReader int) error {
	if err := validateStoredRelicTitle(relic.Title); err != nil {
		return Wrap(CategoryCorruptData, "relic state carries an invalid title", err)
	}
	if err := ValidateRelicType(relic.Type); err != nil {
		return Wrap(CategoryCorruptData, "relic state carries an invalid type", err)
	}
	if relic.Schema != "" {
		if err := validateRelicSchemaID("relic state schema", relic.Schema); err != nil {
			return err
		}
	}
	// A generation-seven build validates the member as a ULID, so a state
	// naming a built-in must say it needs eight.
	if required := relicSchemaMinReader(relic.Schema); minReader < required {
		return corrupt("a relic state naming the built-in schema %s must declare minimum reader generation %d",
			relic.Schema, required)
	}
	if strings.TrimSpace(relic.Author) == "" {
		return corrupt("relic state author must not be blank")
	}
	// A generation-six build refuses both members, so a state carrying them
	// must say it needs seven.
	if relic.Retired && minReader < RelicCheckpointMinReader {
		return corrupt("a retired relic state must declare minimum reader generation %d", RelicCheckpointMinReader)
	}
	if relic.Compacted != nil && minReader < RelicCheckpointMinReader {
		return corrupt("a compacted relic state must declare minimum reader generation %d", RelicCheckpointMinReader)
	}
	// The checkpoint records both, and every state after it carries both.
	if (relic.Compacted == nil) != (history.CompactedFrom == nil) {
		return corrupt("relic state compacted and history compactedFrom must be present together")
	}
	if relic.Compacted != nil {
		if err := validateRelicCompaction(*relic.Compacted); err != nil {
			return err
		}
	}
	if relic.CreatedAt.IsZero() || relic.UpdatedAt.IsZero() {
		return corrupt("relic state creation and update times must be present")
	}
	if err := validateRelicTimeZone("relic state creation time", relic.CreatedAt); err != nil {
		return err
	}
	if err := validateRelicTimeZone("relic state update time", relic.UpdatedAt); err != nil {
		return err
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
	if err := validateRelicParts(relic.Parts); err != nil {
		return err
	}
	// Only a checkpoint makes a stub, so a relic never compacted has none.
	if relic.Compacted == nil {
		for _, part := range relic.Parts {
			if part.Dropped != nil {
				return corrupt("relic state part %s is a stub, but the relic was never compacted", part.ID)
			}
		}
	}
	return nil
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
		if part.Dropped != nil {
			if err := validateRelicPartDrop(part); err != nil {
				return err
			}
		}
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
	return validateRelicLinkList(links.Links)
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
