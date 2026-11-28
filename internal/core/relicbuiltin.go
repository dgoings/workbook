package core

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Built-in relic schemas.
//
// Workbook ships a schema for some relic types, the way it ships default
// statuses, so a project has them with nothing to set up. A built-in version
// is named workbook:<type>:<n>: the literal "workbook:", a relic type, ":",
// and a decimal number from 1 with no leading zero. A ULID never holds ":",
// so a built-in name and a project version's ID cannot be confused.
//
// Built-ins are never stored in the project settings. A relic written under
// one names it in its stored schema member, which needs writer-format
// generation 8 (RelicBuiltInSchemaMinReader). A released built-in never
// changes: a later release adds workbook:<type>:<n+1> beside it.

//go:embed relicschemas/*.json
var builtInRelicSchemaFiles embed.FS

// builtInRelicSchemaRegistry lists every shipped built-in, in the order
// BuiltInRelicSchemaVersions returns them, and the embedded file of each.
var builtInRelicSchemaRegistry = []struct {
	relicType string
	version   int
	file      string
}{
	{"spec", 1, "relicschemas/spec-1.json"},
	{"plan", 1, "relicschemas/plan-1.json"},
}

var builtInRelicSchemaIDPattern = regexp.MustCompile(`^workbook:[a-z0-9]+(-[a-z0-9]+)*:[1-9][0-9]*$`)

// IsBuiltInRelicSchemaID reports whether id is a well-formed built-in name,
// whether or not this build ships it.
func IsBuiltInRelicSchemaID(id string) bool {
	if !builtInRelicSchemaIDPattern.MatchString(id) {
		return false
	}
	relicType := id[len("workbook:"):strings.LastIndexByte(id, ':')]
	return ValidateRelicType(relicType) == nil
}

func builtInRelicSchemaID(relicType string, version int) string {
	return "workbook:" + relicType + ":" + strconv.Itoa(version)
}

type builtInRelicSchemaEntry struct {
	version  RelicSchemaVersion
	document RelicSchemaDocument
	schema   *RelicSchema
}

var builtInRelicSchemas = sync.OnceValue(func() []builtInRelicSchemaEntry {
	entries := make([]builtInRelicSchemaEntry, 0, len(builtInRelicSchemaRegistry))
	for _, registered := range builtInRelicSchemaRegistry {
		source, err := builtInRelicSchemaFiles.ReadFile(registered.file)
		if err != nil {
			panic(fmt.Sprintf("built-in relic schema %s: %v", registered.file, err))
		}
		document, schema, err := CompileRelicSchema(registered.relicType, source, nil)
		if err != nil {
			panic(fmt.Sprintf("built-in relic schema %s: %v", registered.file, err))
		}
		encoded, err := EncodeDocument(document)
		if err != nil {
			panic(fmt.Sprintf("built-in relic schema %s: %v", registered.file, err))
		}
		entries = append(entries, builtInRelicSchemaEntry{
			version: RelicSchemaVersion{
				Type:    registered.relicType,
				Version: registered.version,
				ID:      builtInRelicSchemaID(registered.relicType, registered.version),
				Bytes:   int64(len(encoded)),
			},
			document: document,
			schema:   schema,
		})
	}
	return entries
})

// BuiltInRelicSchemaVersions returns every built-in this build ships, sorted
// by type as the registry lists them and then by number.
func BuiltInRelicSchemaVersions() []RelicSchemaVersion {
	entries := builtInRelicSchemas()
	versions := make([]RelicSchemaVersion, len(entries))
	for index, entry := range entries {
		versions[index] = entry.version
	}
	return versions
}

// BuiltInRelicSchema returns the built-in named id, its stored document and
// its compiled schema, or false when this build does not ship it.
func BuiltInRelicSchema(id string) (RelicSchemaVersion, RelicSchemaDocument, *RelicSchema, bool) {
	for _, entry := range builtInRelicSchemas() {
		if entry.version.ID == id {
			return entry.version, entry.document, entry.schema, true
		}
	}
	return RelicSchemaVersion{}, RelicSchemaDocument{}, nil, false
}

// WithBuiltIns returns the listing with this build's built-ins beside the
// project's versions. The built-ins are never encoded.
func (listing RelicSchemaListing) WithBuiltIns() RelicSchemaListing {
	return RelicSchemaListing{
		Versions: append([]RelicSchemaVersion(nil), listing.Versions...),
		BuiltIn:  BuiltInRelicSchemaVersions(),
	}
}

// EffectiveTypes returns the types with a project version or a built-in,
// sorted.
func (listing RelicSchemaListing) EffectiveTypes() []string {
	seen := map[string]bool{}
	var types []string
	for _, versions := range [][]RelicSchemaVersion{listing.Versions, listing.BuiltIn} {
		for _, version := range versions {
			if !seen[version.Type] {
				seen[version.Type] = true
				types = append(types, version.Type)
			}
		}
	}
	sort.Strings(types)
	return types
}

// IsBuiltIn reports whether the version is a built-in rather than one the
// project saved.
func (v RelicSchemaVersion) IsBuiltIn() bool {
	return IsBuiltInRelicSchemaID(v.ID)
}

// Name is how the version is named to people: "spec version 3" for a project
// version, its name for a built-in.
func (v RelicSchemaVersion) Name() string {
	if v.IsBuiltIn() {
		return v.ID
	}
	return fmt.Sprintf("%s version %d", v.Type, v.Version)
}

// ref presents the version as a relic document names it. A built-in carries
// no number.
func (v RelicSchemaVersion) ref() *RelicSchemaRef {
	if v.IsBuiltIn() {
		return &RelicSchemaRef{Type: v.Type, ID: v.ID, BuiltIn: true}
	}
	return &RelicSchemaRef{Type: v.Type, ID: v.ID, Version: v.Version}
}

// validateRelicSchemaID checks a stored schema member: a project version's
// ID, which is a canonical ULID, or a well-formed built-in name.
func validateRelicSchemaID(description, id string) error {
	if IsBuiltInRelicSchemaID(id) {
		return nil
	}
	return validateCanonicalULID(description, id)
}

// relicSchemaMinReader is the generation a stored schema member needs.
func relicSchemaMinReader(id string) int {
	if IsBuiltInRelicSchemaID(id) {
		return RelicBuiltInSchemaMinReader
	}
	return 0
}
