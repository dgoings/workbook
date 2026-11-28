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
// each live part's fields by part ID, and links the relic's links as
// links.json lists them. Reading a relic never refuses it for not
// fitting, so this reports rather than refuses: a version the listing lacks,
// or one load reports as corrupt data, judges unknown-schema. Any other load
// error is returned.
func JudgeRelic(
	relic RelicData,
	fields map[string]map[string]json.RawMessage,
	links []RelicLink,
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
	if problem := schema.CheckRelic(relic, fields, links); problem != nil {
		return RelicConformance{State: RelicConformanceNotConforming, Problem: problem.String()}, nil
	}
	return RelicConformance{State: RelicConformanceConforms}, nil
}

// ReadRelicSchemaVersion reads one version a listing names: a built-in from
// this build, which is never stored, and a project version from the store.
// Every read of a version a relic is judged under goes through it, so nothing
// asks the store for a built-in or caches one under its empty blob.
func ReadRelicSchemaVersion(
	ctx context.Context,
	store RelicSchemaStore,
	config ProjectConfig,
	version RelicSchemaVersion,
) (RelicSchemaDocument, *RelicSchema, error) {
	if !version.IsBuiltIn() {
		return store.ReadRelicSchema(ctx, config, version)
	}
	if _, document, schema, shipped := BuiltInRelicSchema(version.ID); shipped {
		return document, schema, nil
	}
	return RelicSchemaDocument{}, nil, Errorf(CategoryNewerWriter,
		"this Workbook does not have the built-in schema %s; upgrade Workbook and try again", version.ID)
}

// schemaListing reads the project's schema versions, with this build's
// built-ins beside them. With no schema store, every type is open, built-in
// types too.
func (s RelicService) schemaListing(ctx context.Context) (RelicSchemaListing, error) {
	if s.Schemas == nil {
		return RelicSchemaListing{}, nil
	}
	listing, err := s.Schemas.RelicSchemaListing(ctx, s.Config)
	if err != nil {
		return RelicSchemaListing{}, err
	}
	return listing.WithBuiltIns(), nil
}

func (s RelicService) loadSchema(ctx context.Context) func(RelicSchemaVersion) (*RelicSchema, error) {
	return func(version RelicSchemaVersion) (*RelicSchema, error) {
		_, schema, err := ReadRelicSchemaVersion(ctx, s.Schemas, s.Config, version)
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
// the type's newest, a project version or a built-in. Both are zero for a type
// with no schema. A built-in name this build does not ship is a newer
// Workbook's, which no sync brings.
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
		if !known && IsBuiltInRelicSchemaID(given) {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryNewerWriter,
				"the document names the built-in schema %s, which this Workbook does not have; upgrade Workbook and try again", given)
		}
		if !known {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryStaleWrite,
				"the document names schema version %s, which this clone has not synced; run `workbook sync` and try again", given)
		}
		if named.Type != relicType && named.IsBuiltIn() {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryValidation,
				"schema %s is a schema for %s, not for %s", given, named.Type, relicType)
		}
		if named.Type != relicType {
			return listing, RelicSchemaVersion{}, nil, Errorf(CategoryValidation,
				"schema version %s is version %d of %s, not of %s", given, named.Version, named.Type, relicType)
		}
		version = named
	}
	_, schema, err := ReadRelicSchemaVersion(ctx, s.Schemas, s.Config, version)
	if err != nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	return listing, version, schema, nil
}

// checkRelicCreateSchema judges a whole new relic: its properties, then each
// part in order, named by its place in the create.
func checkRelicCreateSchema(schema *RelicSchema, properties map[string]json.RawMessage, parts []relicCreatePart) *RelicSchemaProblem {
	if problem := schema.CheckRelic(RelicData{Properties: properties}, nil, nil); problem != nil {
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
// be checked, so it cannot be updated until the clone syncs; one naming a
// built-in this build does not ship cannot be updated until Workbook is
// upgraded, which no sync helps.
func (s RelicService) schemaForUpdate(ctx context.Context, relic RelicData, relicID string) (RelicSchemaListing, RelicSchemaVersion, *RelicSchema, error) {
	listing, err := s.schemaListing(ctx)
	if err != nil || relic.Schema == "" || s.Schemas == nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	version, known := listing.ByID(relic.Schema)
	if !known && IsBuiltInRelicSchemaID(relic.Schema) {
		return listing, RelicSchemaVersion{}, nil, Errorf(CategoryNewerWriter,
			"relic %s names the built-in schema %s, which this Workbook does not have; upgrade Workbook and try again",
			relicID, relic.Schema)
	}
	if !known {
		return listing, RelicSchemaVersion{}, nil, Errorf(CategoryStaleWrite,
			"relic %s names a schema version for %s this clone has not synced; run `workbook sync` and try again",
			relicID, relic.Type)
	}
	_, schema, err := ReadRelicSchemaVersion(ctx, s.Schemas, s.Config, version)
	if err != nil {
		return listing, RelicSchemaVersion{}, nil, err
	}
	return listing, version, schema, nil
}

// checkSchema judges the pieces the update saves, in the order its edits
// saved them: each property set or cleared, each part added, edited or
// restored, judged whole as the update leaves it, and each link added, its
// relationship against the defaults and the schema's links.relationships and
// its pointer against the schema's links.pointer rule. A title, a move, a
// removal, a link's target, and an unlink are not judged, nor is a part the
// update leaves removed. A link re-added that the relic already holds records
// no operation, so it is not judged.
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
		case RelicOperationPartFieldSet, RelicOperationPartFieldClear, RelicOperationPartBodySet, RelicOperationPartBodyMerge,
			RelicOperationPartRestore:
			partID = operation.Part
		case RelicOperationLinkAdd:
			added := operation.Link
			problem = schema.CheckLink("link", RelicLink{From: added.From, To: added.To, Rel: added.Rel, Seen: added.Seen, Pointer: added.Pointer})
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
// whole relic. content holds the parts the document already read.
func (s RelicService) presentSchema(ctx context.Context, document *RelicDocument, snapshot RelicSnapshot, content map[string]RelicPartContent) error {
	listing, err := s.schemaListing(ctx)
	if err != nil {
		return err
	}
	document.Schema = ResolveRelicSchemaRef(snapshot.Record.State.Relic, listing)
	document.RelicConformance, err = s.judgeSnapshot(ctx, snapshot, listing, content)
	return err
}

// judgeSnapshot judges a whole relic read from Git. content holds parts the
// caller already read; the fields of any other live part are read once, and
// only when the relic names a version the listing has, so a relic that is open
// or names an unknown version reads no part and no schema.
func (s RelicService) judgeSnapshot(ctx context.Context, snapshot RelicSnapshot, listing RelicSchemaListing, content map[string]RelicPartContent) (RelicConformance, error) {
	relic := snapshot.Record.State.Relic
	if _, known := listing.ByID(relic.Schema); !known || s.Schemas == nil {
		return JudgeRelic(relic, nil, nil, listing, nil)
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
			return RelicConformance{}, err
		}
		for _, partID := range missing {
			fields[partID] = read[partID].Fields
		}
	}
	return JudgeRelic(relic, fields, snapshot.Record.Links.Links, listing, s.loadSchema(ctx))
}

// listedConformance is how a listed relic stands: the judgment the index
// stored with the snapshot when it agrees with this listing on whether the
// version is known, and otherwise judged from Git, so a list from the index
// and one from Git answer the same.
func (s RelicService) listedConformance(ctx context.Context, snapshot RelicSnapshot, listing RelicSchemaListing) (RelicConformance, error) {
	if stored := snapshot.Conformance; stored != nil && conformanceAgrees(*stored, snapshot.Record.State.Relic.Schema, listing) {
		return *stored, nil
	}
	return s.judgeSnapshot(ctx, snapshot, listing, nil)
}

// conformanceAgrees reports whether a stored judgment can stand under the
// listing: an open relic is open, and a relic naming a version is judged
// unknown exactly when the listing lacks the version. A version, once
// listed, never changes, so a judgment against it stands.
func conformanceAgrees(stored RelicConformance, schema string, listing RelicSchemaListing) bool {
	if schema == "" {
		return stored.State == RelicConformanceOpen
	}
	if stored.State == RelicConformanceOpen || stored.State == "" {
		return false
	}
	_, known := listing.ByID(schema)
	return known == (stored.State != RelicConformanceUnknownSchema)
}

// RelicBehind reports whether a relic is behind its type's newest schema
// version, a project version or a built-in: the type has one, and the relic
// names none or another known version of its type. Versions are compared by
// ID, not number, since a built-in and a project version can share a number:
// a relic on a built-in is behind once the project saves its own. A relic of
// a type with no schema, or naming a version the listing lacks, is not
// behind.
func RelicBehind(relic RelicData, listing RelicSchemaListing) bool {
	newest, has := listing.Newest(relic.Type)
	if !has {
		return false
	}
	if relic.Schema == "" {
		return true
	}
	named, known := listing.ByID(relic.Schema)
	return known && named.Type == relic.Type && named.ID != newest.ID
}

// judgedAs is the conformance a save that has just checked the whole relic
// reports: conforms under a schema, open under none.
func judgedAs(checked bool) *RelicConformance {
	if checked {
		return &RelicConformance{State: RelicConformanceConforms}
	}
	return &RelicConformance{State: RelicConformanceOpen}
}

// numberLinkedSchema numbers the version a linked relic's summary names from
// the listing, as list does, on a copy of the summary's reference, which the
// link index may share. It reads no schema.
func numberLinkedSchema(summary *RelicSummary, listing RelicSchemaListing) {
	if summary.Schema == nil {
		return
	}
	if version, known := listing.ByID(summary.Schema.ID); known {
		summary.Schema = version.ref()
	}
}
