package gitstore

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/dgoings/workbook/internal/core"
)

// Relic schema versions live in the configuration ledger. Each schema.put
// names a blob, the stored schema document, and the commit that adds the
// operation carries that blob in its tree as `relic-schema-<operation-id>`:
//
//	refs/workbook/config  →  commit K7 (parent K6)
//	  tree: operation.json             [schema.put 01MA…S]
//	        state.json                 (lists every version, reads no blob)
//	        relic-schema-01MA…S        blob: the stored schema document
//
// The checkpoint lists each version's type, number, ID, size and blob, so the
// settings every command reads never read a schema. A schema is read only
// when a relic is judged against it, and a compiled schema is kept per blob
// for the life of the Repository: a blob never changes.

const relicSchemaEntryPrefix = "relic-schema-"

func relicSchemaTreeName(operationID string) string {
	return relicSchemaEntryPrefix + operationID
}

// relicSchemaTreeEntries maps each tree entry a pack's schema.puts need onto
// the blob it must hold.
func relicSchemaTreeEntries(pack core.ConfigOperationPack) map[string]string {
	entries := map[string]string{}
	for _, operation := range pack.Operations {
		if operation.Type == core.ConfigRelicSchemaPut && operation.RelicSchema != nil {
			entries[relicSchemaTreeName(operation.ID)] = operation.RelicSchema.Blob
		}
	}
	return entries
}

func relicSchemaTreeEntryList(pack core.ConfigOperationPack) []attachmentTreeEntry {
	entries := relicSchemaTreeEntries(pack)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]attachmentTreeEntry, 0, len(names))
	for _, name := range names {
		list = append(list, attachmentTreeEntry{name: name, objectID: entries[name]})
	}
	return list
}

type cachedRelicSchema struct {
	document core.RelicSchemaDocument
	schema   *core.RelicSchema
}

func (r *Repository) cachedRelicSchema(blob string) (cachedRelicSchema, bool) {
	r.relicSchemaMu.Lock()
	defer r.relicSchemaMu.Unlock()
	cached, ok := r.relicSchemaCache[blob]
	return cached, ok
}

func (r *Repository) rememberRelicSchema(blob string, cached cachedRelicSchema) {
	r.relicSchemaMu.Lock()
	defer r.relicSchemaMu.Unlock()
	if r.relicSchemaCache == nil {
		r.relicSchemaCache = map[string]cachedRelicSchema{}
	}
	r.relicSchemaCache[blob] = cached
}

// RelicSchemaListing returns every saved relic schema version from the
// configuration ledger's tip. It reads the checkpoint and no schema; a project
// with no ledger has an empty listing.
func (r *Repository) RelicSchemaListing(ctx context.Context, config core.ProjectConfig) (core.RelicSchemaListing, error) {
	listing, err := r.listConfigRefs(ctx, configRef)
	if err != nil {
		return core.RelicSchemaListing{}, unreadableConfigLedger(err)
	}
	head, found := listing.Heads[configRef]
	if !found {
		return core.RelicSchemaListing{}, nil
	}
	if decoded, found := r.decodedConfigAt(head, config.Key); found {
		return decoded.schemas, nil
	}
	record, err := r.readConfigRecordAt(ctx, config, configRef, head)
	if err != nil {
		return core.RelicSchemaListing{}, unreadableConfigLedger(err)
	}
	decoded := decodedConfigOf(record.State, config.Key)
	r.rememberDecodedConfig(head, decoded)
	return decoded.schemas, nil
}

func describeRelicSchemaVersion(version core.RelicSchemaVersion) string {
	return fmt.Sprintf("the schema for %s version %d", version.Type, version.Version)
}

// ReadRelicSchema reads and compiles one saved version, with every version it
// pins. Blobs are read in batches, one per level of pinning, each bounded by
// MaxRelicSchemaBytes. A version whose blob this clone lacks, or whose bytes
// do not match what the settings list, is corrupt data naming the version.
func (r *Repository) ReadRelicSchema(
	ctx context.Context,
	config core.ProjectConfig,
	version core.RelicSchemaVersion,
) (core.RelicSchemaDocument, *core.RelicSchema, error) {
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.RelicSchemaDocument{}, nil, err
	}
	if cached, ok := r.cachedRelicSchema(version.Blob); ok {
		return cached.document, cached.schema, nil
	}
	stored := map[string][]byte{}
	var listing *core.RelicSchemaListing
	listed := func(id string) (core.RelicSchemaVersion, bool, error) {
		if listing == nil {
			found, err := r.RelicSchemaListing(ctx, config)
			if err != nil {
				return core.RelicSchemaVersion{}, false, err
			}
			listing = &found
		}
		version, ok := listing.ByID(id)
		return version, ok, nil
	}
	pending := []core.RelicSchemaVersion{version}
	queued := map[string]bool{version.ID: true}
	for len(pending) > 0 {
		contents, err := r.readRelicSchemaBlobs(ctx, pending)
		if err != nil {
			return core.RelicSchemaDocument{}, nil, err
		}
		var next []core.RelicSchemaVersion
		for _, read := range pending {
			stored[read.ID] = contents[read.ID]
			document, err := core.DecodeRelicSchemaDocument(contents[read.ID])
			if err != nil {
				return core.RelicSchemaDocument{}, nil, core.Wrap(core.CategoryCorruptData, describeRelicSchemaVersion(read), err)
			}
			for _, id := range sortedReferenceIDs(document.References) {
				if queued[id] {
					continue
				}
				pinned, ok, err := listed(id)
				if err != nil {
					return core.RelicSchemaDocument{}, nil, err
				}
				if !ok {
					return core.RelicSchemaDocument{}, nil, core.Errorf(core.CategoryCorruptData,
						"%s pins version %s, which the project settings do not list", describeRelicSchemaVersion(read), id)
				}
				queued[id] = true
				if _, cached := r.cachedRelicSchema(pinned.Blob); !cached {
					next = append(next, pinned)
				}
			}
		}
		pending = next
	}

	loading := map[string]bool{}
	var load func(core.RelicSchemaVersion) (cachedRelicSchema, error)
	load = func(target core.RelicSchemaVersion) (cachedRelicSchema, error) {
		if cached, ok := r.cachedRelicSchema(target.Blob); ok {
			return cached, nil
		}
		if loading[target.ID] {
			return cachedRelicSchema{}, core.Errorf(core.CategoryCorruptData,
				"%s pins a version that pins it back", describeRelicSchemaVersion(target))
		}
		loading[target.ID] = true
		contents := stored[target.ID]
		schema, err := core.LoadRelicSchema(target, contents, func(id string) (*core.RelicSchema, error) {
			pinned, ok, err := listed(id)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, core.Errorf(core.CategoryCorruptData,
					"%s pins version %s, which the project settings do not list", describeRelicSchemaVersion(target), id)
			}
			loaded, err := load(pinned)
			if err != nil {
				return nil, err
			}
			return loaded.schema, nil
		})
		if err != nil {
			return cachedRelicSchema{}, err
		}
		document, err := core.DecodeRelicSchemaDocument(contents)
		if err != nil {
			return cachedRelicSchema{}, core.Wrap(core.CategoryCorruptData, describeRelicSchemaVersion(target), err)
		}
		loaded := cachedRelicSchema{document: document, schema: schema}
		r.rememberRelicSchema(target.Blob, loaded)
		return loaded, nil
	}
	loaded, err := load(version)
	if err != nil {
		return core.RelicSchemaDocument{}, nil, err
	}
	return loaded.document, loaded.schema, nil
}

func sortedReferenceIDs(references map[string]string) []string {
	types := make([]string, 0, len(references))
	for relicType := range references {
		types = append(types, relicType)
	}
	sort.Strings(types)
	ids := make([]string, len(types))
	for index, relicType := range types {
		ids[index] = references[relicType]
	}
	return ids
}

// readRelicSchemaBlobs reads the stored documents of several versions through
// one batch process, keyed by version ID.
func (r *Repository) readRelicSchemaBlobs(ctx context.Context, versions []core.RelicSchemaVersion) (map[string][]byte, error) {
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, err
	}
	for _, version := range versions {
		if err := r.validateFullObjectID(version.Blob); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, describeRelicSchemaVersion(version)+" names an invalid object ID", err)
		}
	}
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		for _, version := range versions {
			if _, err := fmt.Fprintf(writer, "%s\n", version.Blob); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer batch.Close()
	contents := make(map[string][]byte, len(versions))
	var failure error
	for _, version := range versions {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read relic schemas from Git batch", err)
		}
		if failure != nil {
			continue
		}
		name := describeRelicSchemaVersion(version)
		switch {
		case object.missing:
			failure = core.Errorf(core.CategoryCorruptData, "%s is missing from this clone (object %s)", name, version.Blob)
		case object.refused != nil:
			failure = core.Wrap(core.CategoryCorruptData, name+" cannot be read", object.refused)
		case object.kind != "blob":
			failure = core.Errorf(core.CategoryCorruptData, "%s is not stored as a blob", name)
		case object.size > core.MaxRelicSchemaBytes:
			failure = core.Errorf(core.CategoryCorruptData, "%s is %d bytes; the limit is %d bytes",
				name, object.size, core.MaxRelicSchemaBytes)
		case int64(len(object.contents)) != version.Bytes:
			failure = core.Errorf(core.CategoryCorruptData, "%s is %d bytes, but the project settings list %d",
				name, len(object.contents), version.Bytes)
		default:
			contents[version.ID] = object.contents
		}
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	return contents, nil
}

// WriteRelicSchema saves a compiled schema document as its type's next
// version and returns it. A document identical to the type's newest version
// writes nothing and returns that version with false, so a retry is safe.
func (r *Repository) WriteRelicSchema(
	ctx context.Context,
	config core.ProjectConfig,
	ids core.IDSource,
	document core.RelicSchemaDocument,
	reason string,
) (core.RelicSchemaVersion, bool, error) {
	if ids == nil {
		return core.RelicSchemaVersion{}, false, core.Errorf(core.CategoryOperational, "configuration ID source is required")
	}
	encoded, err := core.EncodeDocument(document)
	if err != nil {
		return core.RelicSchemaVersion{}, false, err
	}
	if len(encoded) > core.MaxRelicSchemaBytes {
		return core.RelicSchemaVersion{}, false, core.Errorf(core.CategoryValidation,
			"schema for %s: the stored schema is %d bytes; the limit is %d bytes",
			document.Type, len(encoded), core.MaxRelicSchemaBytes)
	}
	listing, err := r.RelicSchemaListing(ctx, config)
	if err != nil {
		return core.RelicSchemaVersion{}, false, err
	}
	blob, err := r.writeBlob(ctx, encoded)
	if err != nil {
		return core.RelicSchemaVersion{}, false, err
	}
	if newest, ok := listing.Newest(document.Type); ok && newest.Blob == blob {
		return newest, false, nil
	}
	id, err := ids.New()
	if err != nil {
		return core.RelicSchemaVersion{}, false, core.Wrap(core.CategoryOperational, "cannot generate configuration operation ID", err)
	}
	result, err := r.WriteConfigOperation(ctx, config, ids, []core.ConfigOperation{{
		ID:   id,
		Type: core.ConfigRelicSchemaPut,
		RelicSchema: &core.RelicSchemaChange{
			Type:  document.Type,
			Bytes: int64(len(encoded)),
			Blob:  blob,
		},
	}}, reason)
	if err != nil {
		return core.RelicSchemaVersion{}, false, err
	}
	version, ok := result.State.RelicSchemas().ByID(id)
	if !ok {
		return core.RelicSchemaVersion{}, false, core.Errorf(core.CategoryOperational,
			"the configuration ledger recorded the schema for %s but does not list it", document.Type)
	}
	return version, true, nil
}

// renumberedRelicSchemas reports each schema version saved here whose number a
// replay changed, because a teammate saved a version of the same type first.
func renumberedRelicSchemas(local, replayed core.ConfigStateDocument) []core.Warning {
	after := replayed.RelicSchemas()
	var warnings []core.Warning
	for _, before := range local.RelicSchemas().Versions {
		now, ok := after.ByID(before.ID)
		if !ok || now.Version == before.Version {
			continue
		}
		warnings = append(warnings, core.Warning{
			Code: core.WarningRelicSchemaRenumbered,
			Message: fmt.Sprintf("the schema for %s saved here as version %d is now version %d; a teammate saved version %d first",
				before.Type, before.Version, now.Version, before.Version),
		})
	}
	return warnings
}
