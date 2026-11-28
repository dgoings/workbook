package core

import "sort"

// The relic schema section of the project settings.
//
// A schema.put saves one version of one relic type's schema. The stored schema
// document is a blob in the tree of the commit that adds it, and the
// checkpoint lists only each version's type, number, ID, size and blob, so
// reading statuses, priorities or keys reads no schema.
//
// A version's identity is the ID of the schema.put that saved it, and a relic
// names that ID. Its number is its position among its type's versions in
// ledger order, so two concurrent saves for one type both survive a replay:
// the one replayed is numbered after origin's, and every relic still names
// exactly one version.

// RelicSchemaChange is what a schema.put records: the version's type, the
// stored document's size and the document's object ID.
type RelicSchemaChange struct {
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
	Blob  string `json:"blob"`
}

// RelicSchemaListing is every saved schema version, sorted by type and then
// by version number. BuiltIn holds this build's built-in versions beside them
// once WithBuiltIns adds them; it is never stored.
type RelicSchemaListing struct {
	Versions []RelicSchemaVersion `json:"versions"`
	BuiltIn  []RelicSchemaVersion `json:"-"`
}

// RelicSchemas reads the checkpoint's schema listing. A checkpoint with none
// reads as the empty listing.
func (state ConfigStateDocument) RelicSchemas() RelicSchemaListing {
	if state.Config.Schemas == nil {
		return RelicSchemaListing{}
	}
	return RelicSchemaListing{Versions: append([]RelicSchemaVersion(nil), state.Config.Schemas.Versions...)}
}

// Newest returns the newest version of a type: the project's newest, or the
// newest built-in when the project has none of its own.
func (listing RelicSchemaListing) Newest(relicType string) (RelicSchemaVersion, bool) {
	if newest, found := newestRelicSchemaVersion(listing.Versions, relicType); found {
		return newest, true
	}
	return newestRelicSchemaVersion(listing.BuiltIn, relicType)
}

func newestRelicSchemaVersion(versions []RelicSchemaVersion, relicType string) (RelicSchemaVersion, bool) {
	var newest RelicSchemaVersion
	found := false
	for _, version := range versions {
		if version.Type == relicType && (!found || version.Version > newest.Version) {
			newest, found = version, true
		}
	}
	return newest, found
}

// ByID returns the version a schema.put with this ID saved, or the built-in
// with this name.
func (listing RelicSchemaListing) ByID(id string) (RelicSchemaVersion, bool) {
	for _, versions := range [][]RelicSchemaVersion{listing.Versions, listing.BuiltIn} {
		for _, version := range versions {
			if version.ID == id {
				return version, true
			}
		}
	}
	return RelicSchemaVersion{}, false
}

// Number returns version number version of a type the project saved.
func (listing RelicSchemaListing) Number(relicType string, version int) (RelicSchemaVersion, bool) {
	for _, candidate := range listing.Versions {
		if candidate.Type == relicType && candidate.Version == version {
			return candidate, true
		}
	}
	return RelicSchemaVersion{}, false
}

// Types returns the types with at least one version the project saved,
// sorted.
func (listing RelicSchemaListing) Types() []string {
	var types []string
	seen := map[string]bool{}
	for _, version := range listing.Versions {
		if !seen[version.Type] {
			seen[version.Type] = true
			types = append(types, version.Type)
		}
	}
	sort.Strings(types)
	return types
}

// TouchesRelicSchemas reports whether an operation type belongs to the schema
// section.
func (operationType ConfigOperationType) TouchesRelicSchemas() bool {
	return operationType == ConfigRelicSchemaPut
}

func validateRelicSchemaChange(change RelicSchemaChange) error {
	if err := ValidateRelicType(change.Type); err != nil {
		return Wrap(CategoryCorruptData, "schema.put names an invalid relic type", err)
	}
	if change.Bytes < 1 || change.Bytes > MaxRelicSchemaBytes {
		return corrupt("schema.put records a %d-byte schema; a stored schema is 1 to %d bytes", change.Bytes, MaxRelicSchemaBytes)
	}
	if !objectIDPattern.MatchString(change.Blob) {
		return corrupt("schema.put names an invalid object ID")
	}
	return nil
}

// validateRelicSchemaListing refuses a listing that is not the canonical one a
// fold produces: sorted by type then version, each type numbered from 1 with
// no gap, every ID distinct, and nil rather than empty when there are none.
// It does not count versions: counts are checked only when one is saved.
func validateRelicSchemaListing(listing *RelicSchemaListing) error {
	if listing == nil {
		return nil
	}
	if len(listing.Versions) == 0 {
		return corrupt("an empty relic schema listing must be absent")
	}
	ids := make(map[string]bool, len(listing.Versions))
	for index, version := range listing.Versions {
		if err := validateOperationID(version.ID); err != nil {
			return err
		}
		if ids[version.ID] {
			return corrupt("the relic schema listing names version %s twice", version.ID)
		}
		ids[version.ID] = true
		if err := validateRelicSchemaChange(RelicSchemaChange{Type: version.Type, Bytes: version.Bytes, Blob: version.Blob}); err != nil {
			return err
		}
		want := 1
		if index > 0 {
			previous := listing.Versions[index-1]
			switch {
			case previous.Type == version.Type:
				want = previous.Version + 1
			case previous.Type > version.Type:
				return corrupt("the relic schema listing is not sorted by type")
			}
		}
		if version.Version != want {
			return corrupt("the relic schema listing numbers %s version %d where %d belongs", version.Type, version.Version, want)
		}
	}
	return nil
}

// configSchemas is the schema listing during a fold.
type configSchemas struct {
	versions []RelicSchemaVersion
	ids      map[string]bool
	counts   map[string]int
}

func newConfigSchemas(listing *RelicSchemaListing) (*configSchemas, error) {
	if err := validateRelicSchemaListing(listing); err != nil {
		return nil, err
	}
	schemas := &configSchemas{ids: map[string]bool{}, counts: map[string]int{}}
	if listing != nil {
		for _, version := range listing.Versions {
			schemas.versions = append(schemas.versions, version)
			schemas.ids[version.ID] = true
			schemas.counts[version.Type]++
		}
	}
	return schemas, nil
}

// apply appends the version a schema.put saves, numbered after its type's
// others. It never refuses on a count: two clones can each save within the
// limit and together pass it, and the settings must stay readable.
func (schemas *configSchemas) apply(operation ConfigOperation) error {
	if operation.Type != ConfigRelicSchemaPut || operation.RelicSchema == nil {
		return corrupt("unsupported configuration operation type %q", operation.Type)
	}
	if schemas.ids[operation.ID] {
		return corrupt("schema.put %s saves a version already listed", operation.ID)
	}
	change := *operation.RelicSchema
	schemas.counts[change.Type]++
	schemas.ids[operation.ID] = true
	schemas.versions = append(schemas.versions, RelicSchemaVersion{
		Type: change.Type, Version: schemas.counts[change.Type], ID: operation.ID, Bytes: change.Bytes, Blob: change.Blob,
	})
	return nil
}

func (schemas *configSchemas) document() *RelicSchemaListing {
	if len(schemas.versions) == 0 {
		return nil
	}
	versions := append([]RelicSchemaVersion(nil), schemas.versions...)
	sort.SliceStable(versions, func(left, right int) bool {
		if versions[left].Type != versions[right].Type {
			return versions[left].Type < versions[right].Type
		}
		return versions[left].Version < versions[right].Version
	})
	return &RelicSchemaListing{Versions: versions}
}

// validateRelicSchemaGrowth refuses a pack that would take a type past
// MaxRelicSchemaVersionsPerType, or the project past MaxRelicSchemaVersions.
// Only growth is refused, so a listing already over a limit after two clones
// met stays writable for everything else.
func validateRelicSchemaGrowth(parent *ConfigStateDocument, after *configSchemas) error {
	before := map[string]int{}
	total := 0
	if parent != nil && parent.Config.Schemas != nil {
		for _, version := range parent.Config.Schemas.Versions {
			before[version.Type]++
			total++
		}
	}
	types := make([]string, 0, len(after.counts))
	for relicType := range after.counts {
		types = append(types, relicType)
	}
	sort.Strings(types)
	for _, relicType := range types {
		count := after.counts[relicType]
		if count > before[relicType] && count > MaxRelicSchemaVersionsPerType {
			return Errorf(CategoryValidation, "the schema for %s would have %d versions; the limit is %d",
				relicType, count, MaxRelicSchemaVersionsPerType)
		}
	}
	if count := len(after.versions); count > total && count > MaxRelicSchemaVersions {
		return Errorf(CategoryValidation, "the project would have %d schema versions; the limit is %d",
			count, MaxRelicSchemaVersions)
	}
	return nil
}
