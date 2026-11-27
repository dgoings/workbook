package core

import (
	"context"
	"encoding/json"
	"fmt"
)

// RelicSchemaStore is what the relic service reads schema versions through:
// the listing from the project settings, which reads no schema, and one
// version's stored document compiled.
type RelicSchemaStore interface {
	RelicSchemaListing(context.Context, ProjectConfig) (RelicSchemaListing, error)
	ReadRelicSchema(context.Context, ProjectConfig, RelicSchemaVersion) (RelicSchemaDocument, *RelicSchema, error)
}

// The states a relic is judged to be in against the schema version it names.
const (
	// RelicConformanceOpen is a relic that names no schema version.
	RelicConformanceOpen = "open"
	// RelicConformanceConforms is a relic that fits the version it names.
	RelicConformanceConforms = "conforms"
	// RelicConformanceNotConforming is a relic that does not fit the version
	// it names, as two valid edits that meet can leave it.
	RelicConformanceNotConforming = "not-conforming"
	// RelicConformanceUnknownSchema is a relic that names a version this
	// clone's settings do not list, or one whose stored document cannot be
	// read, so it cannot be judged.
	RelicConformanceUnknownSchema = "unknown-schema"
)

// RelicConformance is how a relic stands against the schema version it
// names. Problem is the first thing that does not fit, for a relic that is
// not conforming.
type RelicConformance struct {
	State   string `json:"conformance"`
	Problem string `json:"schemaProblem,omitempty"`
}

// JudgeRelic judges a whole relic against the version it names. fields holds
// each live part's fields by part ID. Reading a relic never refuses it for not
// fitting, so this reports rather than refuses: a version the listing lacks,
// or one load reports as corrupt data, judges unknown-schema. Any other load
// error is returned.
func JudgeRelic(
	relic RelicData,
	fields map[string]map[string]json.RawMessage,
	listing RelicSchemaListing,
	load func(RelicSchemaVersion) (*RelicSchema, error),
) (RelicConformance, error) {
	if relic.Schema == "" {
		return RelicConformance{State: RelicConformanceOpen}, nil
	}
	version, known := listing.ByID(relic.Schema)
	if !known {
		return RelicConformance{State: RelicConformanceUnknownSchema}, nil
	}
	schema, err := load(version)
	if err != nil {
		if CategoryOf(err) == CategoryCorruptData {
			return RelicConformance{State: RelicConformanceUnknownSchema}, nil
		}
		return RelicConformance{}, err
	}
	if problem := schema.CheckRelic(relic, fields); problem != nil {
		return RelicConformance{State: RelicConformanceNotConforming, Problem: problem.String()}, nil
	}
	return RelicConformance{State: RelicConformanceConforms}, nil
}

// schemaListing reads the project's schema versions. With no schema store,
// every type is open.
func (s RelicService) schemaListing(ctx context.Context) (RelicSchemaListing, error) {
	if s.Schemas == nil {
		return RelicSchemaListing{}, nil
	}
	return s.Schemas.RelicSchemaListing(ctx, s.Config)
}

func (s RelicService) loadSchema(ctx context.Context) func(RelicSchemaVersion) (*RelicSchema, error) {
	return func(version RelicSchemaVersion) (*RelicSchema, error) {
		_, schema, err := s.Schemas.ReadRelicSchema(ctx, s.Config, version)
		return schema, err
	}
}

// relicSummaryUnder is the relic's summary with the version it names numbered
// from the listing.
func relicSummaryUnder(snapshot RelicSnapshot, listing RelicSchemaListing) RelicSummary {
	summary := relicSummaryOf(snapshot)
	summary.Schema = ResolveRelicSchemaRef(snapshot.Record.State.Relic, listing)
	return summary
}

// relicMutationUnder is relicMutationOf with the version the relic names
// numbered from the listing.
func relicMutationUnder(snapshot RelicSnapshot, changed bool, listing RelicSchemaListing) RelicMutationResult {
	result := relicMutationOf(snapshot, changed)
	result.Relic.Schema = ResolveRelicSchemaRef(snapshot.Record.State.Relic, listing)
	return result
}

// schemaForCreate is the version a new relic of the type is written under:
// the one given, which must be a version of that type this clone has, or else
// the type's newest. Both are zero for a type with no schema.
func (s RelicService) schemaForCreate(ctx context.Context, relicType, given string) (RelicSchemaListing, RelicSchemaVersion, *RelicSchema, error) {
	listing, err := s.schemaListing(ctx)
	if err != nil {
		return RelicSchemaListing{}, RelicSchemaVersion{}, nil, err
	}
	var version RelicSchemaVersion
	if given == "" {
		newest, has := listing.Newest(relicType)
		if !has {
			return listing, RelicSchemaVersion{}, nil, nil
		}
		version = newest
	} else {
		named, known := listing.ByID(given)
		if !known {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryStaleWrite,
				"the document names schema version %s, which this clone has not synced; run `workbook sync` and try again", given)
		}
		if named.Type != relicType {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryValidation,
				"schema version %s is version %d of %s, not of %s", given, named.Version, named.Type, relicType)
		}
		version = named
	}
	_, schema, err := s.Schemas.ReadRelicSchema(ctx, s.Config, version)
	if err != nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	return listing, version, schema, nil
}

// checkRelicCreateSchema judges a whole new relic: its properties, then each
// part in order, named by its place in the create.
func checkRelicCreateSchema(schema *RelicSchema, properties map[string]json.RawMessage, parts []relicCreatePart) *RelicSchemaProblem {
	if problem := schema.CheckRelic(RelicData{Properties: properties}, nil); problem != nil {
		return problem
	}
	for index, part := range parts {
		judged := RelicPart{Type: part.input.Type}
		if part.input.Body != nil {
			judged.Body = &RelicBody{Format: part.input.Body.Format, Bytes: int64(len(part.input.Body.Content))}
		}
		if problem := schema.CheckPart(fmt.Sprintf("part %d", index+1), judged, part.fields); problem != nil {
			return problem
		}
	}
	return nil
}

// schemaForUpdate is the version an update of the relic is checked under:
// the one it names. A relic naming a version this clone has not synced cannot
// be checked, so it cannot be updated until the clone syncs.
func (s RelicService) schemaForUpdate(ctx context.Context, relic RelicData, relicID string) (RelicSchemaListing, RelicSchemaVersion, *RelicSchema, error) {
	listing, err := s.schemaListing(ctx)
	if err != nil || relic.Schema == "" || s.Schemas == nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	version, known := listing.ByID(relic.Schema)
	if !known {
		return listing, RelicSchemaVersion{}, nil, Errorf(CategoryStaleWrite,
			"relic %s names a schema version for %s this clone has not synced; run `workbook sync` and try again",
			relicID, relic.Type)
	}
	_, schema, err := s.Schemas.ReadRelicSchema(ctx, s.Config, version)
	if err != nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	return listing, version, schema, nil
}

// checkSchema judges the pieces the update saves, in the order its edits
// saved them: each property set or cleared, and each part added, edited or
// restored, judged whole as the update leaves it. A title, a move, a removal
// and a link are not judged, nor is a part the update leaves removed.
func (u *relicUpdate) checkSchema(ctx context.Context, version RelicSchemaVersion, schema *RelicSchema) error {
	if schema == nil {
		return nil
	}
	judged := map[string]bool{}
	for _, operation := range u.operations {
		var problem *RelicSchemaProblem
		partID := ""
		switch operation.Type {
		case RelicOperationPropertySet:
			problem = schema.CheckProperty(operation.Name, operation.Value)
		case RelicOperationPropertyClear:
			problem = schema.CheckPropertyClear(operation.Name)
		case RelicOperationPartAdd:
			partID = operation.ID
		case RelicOperationPartFieldSet, RelicOperationPartFieldClear, RelicOperationPartBodySet, RelicOperationPartRestore:
			partID = operation.Part
		}
		if partID != "" && !judged[partID] {
			judged[partID] = true
			part, exists := u.relic.Part(partID)
			if exists && !part.Removed {
				if err := u.loadFields(ctx, partID); err != nil {
					return err
				}
				problem = schema.CheckPart("part "+partID, part, u.fieldsNow(partID))
			}
		}
		if problem != nil {
			return RelicSchemaRefusal(version, *problem)
		}
	}
	return nil
}

// presentSchema numbers the version the document's relic names and judges the
// whole relic. content holds the parts the document already read; the fields
// of any other live part are read once, and only when there is a schema to
// judge them by.
func (s RelicService) presentSchema(ctx context.Context, document *RelicDocument, snapshot RelicSnapshot, content map[string]RelicPartContent) error {
	relic := snapshot.Record.State.Relic
	listing, err := s.schemaListing(ctx)
	if err != nil {
		return err
	}
	document.Schema = ResolveRelicSchemaRef(relic, listing)
	if _, known := listing.ByID(relic.Schema); !known || s.Schemas == nil {
		document.RelicConformance, err = JudgeRelic(relic, nil, listing, nil)
		return err
	}
	fields := make(map[string]map[string]json.RawMessage, len(relic.Parts))
	var missing []string
	for _, part := range relic.LiveParts() {
		if read, ok := content[part.ID]; ok {
			fields[part.ID] = read.Fields
		} else {
			missing = append(missing, part.ID)
		}
	}
	if len(missing) != 0 {
		read, err := s.Reader.ReadRelicParts(ctx, s.Config, snapshot, missing, false)
		if err != nil {
			return err
		}
		for _, partID := range missing {
			fields[partID] = read[partID].Fields
		}
	}
	document.RelicConformance, err = JudgeRelic(relic, fields, listing, s.loadSchema(ctx))
	return err
}
