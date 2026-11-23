package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RelicReader is what the relic service reads relics through.
type RelicReader interface {
	ListRelicHeads(context.Context, ProjectConfig) ([]RelicHead, error)
	ReadRelicTips(context.Context, ProjectConfig, []RelicHead) ([]RelicSnapshot, error)
	GetRelic(context.Context, ProjectConfig, string) (RelicSnapshot, error)
	ReadRelicParts(context.Context, ProjectConfig, RelicSnapshot, []string, bool) (map[string]RelicPartContent, error)
}

// RelicWriter is what the relic service writes relics through.
type RelicWriter interface {
	WriteRelic(context.Context, ProjectConfig, *RelicSnapshot, RelicOperationPack, RelicRecord, map[string]RelicPartContent, string) (RelicSnapshot, error)
}

// RelicService creates, reads and changes relics. Keys, Now and Actor are read
// the way Service reads them: a zero Keys is the founding key, a nil Now is
// the clock, and an actor on the context wins over Actor.
type RelicService struct {
	Config ProjectConfig
	Keys   KeySet
	Reader RelicReader
	Writer RelicWriter
	IDs    IDSource
	Now    func() time.Time
	Actor  string
}

// RelicSummary is one relic as relic list shows it. Parts counts live parts.
type RelicSummary struct {
	ID        string    `json:"id"`
	Head      string    `json:"head"`
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	Author    string    `json:"author"`
	Retired   bool      `json:"retired"`
	Deleted   bool      `json:"deleted"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Parts     int       `json:"parts"`
}

// RelicMutationResult is what a command that changes a relic reports. Parts
// holds the parts the command added or edited, in command order, without
// body text.
type RelicMutationResult struct {
	Relic    RelicSummary        `json:"relic"`
	Parts    []RelicDocumentPart `json:"parts"`
	Changed  bool                `json:"changed"`
	Warnings []Warning           `json:"warnings,omitempty"`
}

// RelicBodyInput is a body to save: its format label and its exact bytes.
type RelicBodyInput struct {
	Format  string
	Content []byte
}

// RelicPartInput is a part to add. A nil Body adds a part without a body.
type RelicPartInput struct {
	Type   string
	Fields map[string]json.RawMessage
	Body   *RelicBodyInput
}

// RelicCreateInput is a relic to create, with its parts in order.
type RelicCreateInput struct {
	Title, Type string
	Properties  map[string]json.RawMessage
	Parts       []RelicPartInput
}

// RelicListFilter narrows relic list. An empty Type matches every type.
type RelicListFilter struct {
	Type           string
	IncludeDeleted bool
}

// RelicShowOptions says what relic show reads besides fields: bodies, and
// removed parts when no one part is addressed.
type RelicShowOptions struct{ Bodies, Removed bool }

func (s RelicService) keys() KeySet {
	if s.Keys.IsZero() {
		return FoundingKeySet(s.Config.Key)
	}
	return s.Keys
}

func (s RelicService) actorFor(ctx context.Context) string {
	if actor, asserted := ActorFrom(ctx); asserted {
		return actor
	}
	return s.Actor
}

func (s RelicService) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

// newID mints one canonical ULID that is not in seen, and adds it.
func (s RelicService) newID(seen map[string]struct{}) (string, error) {
	if s.IDs == nil {
		return "", Errorf(CategoryOperational, "ID source is required")
	}
	id, err := s.IDs.New()
	if err != nil {
		return "", Wrap(CategoryOperational, "cannot generate ID", err)
	}
	if err := validateGeneratedULID(id); err != nil {
		return "", err
	}
	if _, exists := seen[id]; exists {
		return "", Errorf(CategoryValidation, "ID source returned duplicate ULID %q", id)
	}
	seen[id] = struct{}{}
	return id, nil
}

// newIDs mints count distinct canonical ULIDs.
func (s RelicService) newIDs(count int) ([]string, error) {
	ids := make([]string, count)
	seen := make(map[string]struct{}, count)
	for index := range ids {
		id, err := s.newID(seen)
		if err != nil {
			return nil, err
		}
		ids[index] = id
	}
	return ids, nil
}

// ResolveRelicPrefix finds the one relic whose ID is idOrPrefix or starts
// with it, ignoring case.
func ResolveRelicPrefix(heads []RelicHead, idOrPrefix string) (RelicHead, error) {
	if strings.TrimSpace(idOrPrefix) == "" {
		return RelicHead{}, Errorf(CategoryValidation, "give a relic ID, or a prefix of one")
	}
	wanted := strings.ToUpper(idOrPrefix)
	var matches []RelicHead
	for _, head := range heads {
		id := strings.ToUpper(head.RelicID)
		if id == wanted {
			return head, nil
		}
		if strings.HasPrefix(id, wanted) {
			matches = append(matches, head)
		}
	}
	switch len(matches) {
	case 0:
		return RelicHead{}, Errorf(CategoryNotFound, "no relic matches %q", idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return RelicHead{}, Errorf(CategoryValidation, "relic ID prefix %q is ambiguous", idOrPrefix)
	}
}

// ResolvePartPrefix finds the one part, live or removed, whose ID is prefix or
// starts with it, ignoring case. Its messages call the relic "the relic"; the
// service's own lookups name it.
func ResolvePartPrefix(parts []RelicPart, prefix string) (RelicPart, error) {
	return resolvePartPrefix("the relic", parts, prefix)
}

func resolvePartPrefix(relic string, parts []RelicPart, prefix string) (RelicPart, error) {
	if strings.TrimSpace(prefix) == "" {
		return RelicPart{}, Errorf(CategoryValidation, "give a part ID, or a prefix of one")
	}
	wanted := strings.ToUpper(prefix)
	var matches []RelicPart
	for _, part := range parts {
		id := strings.ToUpper(part.ID)
		if id == wanted {
			return part, nil
		}
		if strings.HasPrefix(id, wanted) {
			matches = append(matches, part)
		}
	}
	switch len(matches) {
	case 0:
		return RelicPart{}, Errorf(CategoryNotFound, "no part of %s matches %q", relic, prefix)
	case 1:
		return matches[0], nil
	default:
		return RelicPart{}, Errorf(CategoryValidation,
			"part prefix %q matches %d parts of %s; use more characters", prefix, len(matches), relic)
	}
}

// canonicalRelicValue checks one property or field and returns its value in
// its stored form. kind is "property" or "field".
func canonicalRelicValue(kind, name string, value json.RawMessage) (json.RawMessage, error) {
	if err := ValidateRelicName(kind, name); err != nil {
		return nil, err
	}
	stored, err := CanonicalRelicValue(value)
	if err != nil {
		return nil, Errorf(CategoryValidation, "the %s %q: %s", kind, name, err.Error())
	}
	return stored, nil
}

// canonicalRelicValues checks a properties or fields object and returns it
// with every value in its stored form. kind is "property" or "field".
func canonicalRelicValues(kind string, values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	canonical := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		stored, err := canonicalRelicValue(kind, name, value)
		if err != nil {
			return nil, err
		}
		canonical[name] = stored
	}
	return canonical, nil
}

// checkRelicPropertiesSize refuses a properties object over its limit.
func checkRelicPropertiesSize(properties map[string]json.RawMessage) error {
	encoded, err := json.Marshal(properties)
	if err != nil {
		return Wrap(CategoryValidation, "the relic's properties cannot be encoded", err)
	}
	if len(encoded) > MaxRelicPropertiesBytes {
		return relicSizeError("the relic's properties", len(encoded), MaxRelicPropertiesBytes)
	}
	return nil
}

// checkRelicFieldsSize refuses a part's fields whose fields.json would be over
// its limit. what names the part, as in "part 3".
func checkRelicFieldsSize(what string, fields map[string]json.RawMessage) error {
	measured, err := json.Marshal(NewRelicFields(fields))
	if err != nil {
		return Wrap(CategoryValidation, fmt.Sprintf("the fields of %s cannot be encoded", what), err)
	}
	// The limit is on the stored fields.json, which ends with a newline.
	if size := len(measured) + 1; size > MaxRelicPartFieldsBytes {
		return relicSizeError("the fields of "+what, size, MaxRelicPartFieldsBytes)
	}
	return nil
}

// checkRelicBodyInput checks a body to save: its format label and its size.
// what names the part, as in "part 3".
func checkRelicBodyInput(what string, body RelicBodyInput) error {
	if err := ValidateRelicBodyFormat(body.Format); err != nil {
		return Errorf(CategoryValidation, "%s: %s", what, err.Error())
	}
	if size := len(body.Content); size > MaxRelicBodyBytes {
		return relicSizeError("the body of "+what, size, MaxRelicBodyBytes)
	}
	return nil
}

// relicCreatePart is one part of a create after its values are checked.
type relicCreatePart struct {
	input  RelicPartInput
	fields map[string]json.RawMessage
}

// checkRelicCreate checks every value a create stores, and the limits a create
// can pass, before anything is minted or written.
func checkRelicCreate(input RelicCreateInput) (string, map[string]json.RawMessage, []relicCreatePart, error) {
	title, err := ValidateRelicTitle(input.Title)
	if err != nil {
		return "", nil, nil, err
	}
	if err := ValidateRelicType(input.Type); err != nil {
		return "", nil, nil, err
	}
	properties, err := canonicalRelicValues("property", input.Properties)
	if err != nil {
		return "", nil, nil, err
	}
	if err := checkRelicPropertiesSize(properties); err != nil {
		return "", nil, nil, err
	}

	// A new relic starts from nothing, so every part it has is one it adds.
	created := relicPartTotals{live: len(input.Parts), all: len(input.Parts)}
	parts := make([]relicCreatePart, len(input.Parts))
	for index, part := range input.Parts {
		what := fmt.Sprintf("part %d", index+1)
		if err := ValidateRelicPartType(part.Type); err != nil {
			return "", nil, nil, Errorf(CategoryValidation, "%s: %s", what, err.Error())
		}
		fields, err := canonicalRelicValues("field", part.Fields)
		if err != nil {
			return "", nil, nil, Errorf(CategoryValidation, "%s: %s", what, err.Error())
		}
		if err := checkRelicFieldsSize(what, fields); err != nil {
			return "", nil, nil, err
		}
		if part.Body != nil {
			if err := checkRelicBodyInput(what, *part.Body); err != nil {
				return "", nil, nil, err
			}
			created.liveBodyBytes += int64(len(part.Body.Content))
		}
		parts[index] = relicCreatePart{input: part, fields: fields}
	}
	if err := checkRelicGrowth(relicPartTotals{}, created); err != nil {
		return "", nil, nil, err
	}
	return title, properties, parts, nil
}

// Create writes a new relic as one commit: relic.create, a property.set per
// property in name order, and a part.add per part in input order at positions
// 1/1, 2/1, and so on. Every value is checked before anything is written.
func (s RelicService) Create(ctx context.Context, input RelicCreateInput) (RelicMutationResult, error) {
	title, properties, parts, err := checkRelicCreate(input)
	if err != nil {
		return RelicMutationResult{}, err
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	ids, err := s.newIDs(3 + len(names) + len(parts))
	if err != nil {
		return RelicMutationResult{}, err
	}
	relicID := NewRelicID(s.keys().Current(), ids[0])
	generation, operationIDs := ids[1], ids[2:]
	now := s.now()

	operations := make([]RelicOperation, 0, len(operationIDs))
	operations = append(operations, RelicOperation{
		ID:    operationIDs[0],
		Type:  RelicOperationCreate,
		Relic: &RelicCreateData{Title: title, Type: input.Type, CreatedAt: now},
	})
	for index, name := range names {
		operations = append(operations, RelicOperation{
			ID: operationIDs[1+index], Type: RelicOperationPropertySet, Name: name, Value: properties[name],
		})
	}
	partIDs := operationIDs[1+len(names):]
	content := make(map[string]RelicPartContent, len(parts))
	for index, part := range parts {
		operation := RelicOperation{
			ID:       partIDs[index],
			Type:     RelicOperationPartAdd,
			PartType: part.input.Type,
			Position: fmt.Sprintf("%d/1", index+1),
		}
		partContent := RelicPartContent{Fields: part.fields}
		if body := part.input.Body; body != nil {
			operation.Body = &RelicBodyChange{Format: body.Format, Bytes: int64(len(body.Content))}
			partContent.Body, partContent.HasBody = body.Content, true
		}
		operations = append(operations, operation)
		content[operation.ID] = partContent
	}

	pack := NewRelicOperationPack(s.Config.ProjectID, relicID, generation, s.actorFor(ctx), 1, now, operations)
	record, err := ApplyRelic(nil, pack)
	if err != nil {
		return RelicMutationResult{}, err
	}
	written, err := s.Writer.WriteRelic(ctx, s.Config, nil, pack, record, content, "create relic "+relicID)
	if err != nil {
		return RelicMutationResult{}, err
	}

	result := RelicMutationResult{
		Relic:   relicSummaryOf(written),
		Parts:   make([]RelicDocumentPart, 0, len(partIDs)),
		Changed: true,
	}
	for index, partID := range partIDs {
		part, _ := written.Record.State.Relic.Part(partID)
		result.Parts = append(result.Parts, newRelicDocumentPart(part, RelicPartContent{Fields: parts[index].fields}))
	}
	return result, nil
}

// List returns relic summaries in relic ID order, which is creation order. It
// reads each relic's tip without checking it against its parent.
func (s RelicService) List(ctx context.Context, filter RelicListFilter) ([]RelicSummary, error) {
	heads, err := s.Reader.ListRelicHeads(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	snapshots, err := s.Reader.ReadRelicTips(ctx, s.Config, heads)
	if err != nil {
		return nil, err
	}
	summaries := make([]RelicSummary, 0, len(snapshots))
	for _, snapshot := range snapshots {
		relic := snapshot.Record.State.Relic
		if relic.Deleted && !filter.IncludeDeleted {
			continue
		}
		if filter.Type != "" && relic.Type != filter.Type {
			continue
		}
		summaries = append(summaries, relicSummaryOf(snapshot))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].ID < summaries[j].ID })
	return summaries, nil
}

// Show returns a relic addressed as <relic> or <relic>#<part>, either of them
// a prefix. Without a part it returns the live parts in order, and the removed
// ones too when options.Removed; with one it returns that part, removed or
// not. Bodies are read only when options.Bodies.
func (s RelicService) Show(ctx context.Context, address string, options RelicShowOptions) (RelicDocument, error) {
	relicPrefix, partPrefix, hasPart, err := ParsePartAddress(address)
	if err != nil {
		return RelicDocument{}, err
	}
	snapshot, err := s.getRelic(ctx, relicPrefix)
	if err != nil {
		return RelicDocument{}, err
	}
	relic := snapshot.Record.State.Relic
	var parts []RelicPart
	switch {
	case hasPart:
		part, err := resolvePartPrefix(snapshot.Record.State.RelicID, relic.Parts, partPrefix)
		if err != nil {
			return RelicDocument{}, err
		}
		parts = []RelicPart{part}
	case options.Removed:
		parts = relic.Parts
	default:
		parts = relic.LiveParts()
	}
	return s.relicDocument(ctx, snapshot, parts, options.Bodies)
}

// Export returns the whole relic with its bodies and live parts, as the
// workbook.relic document create --from-file reads.
func (s RelicService) Export(ctx context.Context, idOrPrefix string) (RelicDocument, error) {
	snapshot, err := s.getRelic(ctx, idOrPrefix)
	if err != nil {
		return RelicDocument{}, err
	}
	return s.relicDocument(ctx, snapshot, snapshot.Record.State.Relic.LiveParts(), true)
}

// getRelic resolves an ID or prefix and reads that relic, checked against its
// parent.
func (s RelicService) getRelic(ctx context.Context, idOrPrefix string) (RelicSnapshot, error) {
	heads, err := s.Reader.ListRelicHeads(ctx, s.Config)
	if err != nil {
		return RelicSnapshot{}, err
	}
	head, err := ResolveRelicPrefix(heads, idOrPrefix)
	if err != nil {
		return RelicSnapshot{}, err
	}
	return s.Reader.GetRelic(ctx, s.Config, head.RelicID)
}

func (s RelicService) relicDocument(ctx context.Context, snapshot RelicSnapshot, parts []RelicPart, bodies bool) (RelicDocument, error) {
	partIDs := make([]string, len(parts))
	for index, part := range parts {
		partIDs[index] = part.ID
	}
	content, err := s.Reader.ReadRelicParts(ctx, s.Config, snapshot, partIDs, bodies)
	if err != nil {
		return RelicDocument{}, err
	}
	return newRelicDocument(snapshot, parts, content), nil
}

func relicSummaryOf(snapshot RelicSnapshot) RelicSummary {
	relic := snapshot.Record.State.Relic
	return RelicSummary{
		ID:        snapshot.Record.State.RelicID,
		Head:      snapshot.Head,
		Title:     relic.Title,
		Type:      relic.Type,
		Author:    relic.Author,
		Retired:   relic.Retired,
		Deleted:   relic.Deleted,
		CreatedAt: relic.CreatedAt,
		UpdatedAt: relic.UpdatedAt,
		Parts:     len(relic.LiveParts()),
	}
}
