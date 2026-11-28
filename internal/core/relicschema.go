package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A relic schema says what one relic type holds: its properties, its part
// types with their fields and bodies, and its links. The file a project saves
// is a Workbook envelope whose members are title, description, properties,
// parts, links and $defs. The rules inside it are JSON Schema, limited to a
// closed list of words; any other word, at any depth, is refused when the
// schema is saved. The list can grow and never shrink.
//
// A saved version is stored as a workbook.relic-schema document. A reference
// to another relic type is pinned to that type's newest version when the
// schema is saved, so a stored version never changes meaning.

const (
	RelicSchemaDocumentFormat  = "workbook.relic-schema"
	RelicSchemaDocumentVersion = 1
)

// RelicSchemaVersion is one saved version as the project settings list it:
// its type, its number among that type's versions, the ID of the operation
// that saved it, the stored document's size and the document's object ID.
type RelicSchemaVersion struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ID      string `json:"id"`
	Bytes   int64  `json:"bytes"`
	Blob    string `json:"blob"`
}

// RelicSchemaDocument is a stored schema version. References maps each other
// relic type the schema refers to onto the ID of the version it is pinned to.
// Schema is the saved file in canonical form.
type RelicSchemaDocument struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	Type       string            `json:"type"`
	References map[string]string `json:"references,omitempty"`
	Schema     json.RawMessage   `json:"schema"`
}

// RelicSchemaLookup returns the newest version of another relic type and its
// compiled schema, or false when the type has none.
type RelicSchemaLookup func(relicType string) (RelicSchemaVersion, *RelicSchema, bool, error)

// RelicSchemaPinned returns the compiled schema of the version with this ID,
// as a stored document's references name it.
type RelicSchemaPinned func(id string) (*RelicSchema, error)

// RelicSchema is a compiled schema version, ready to judge relics. It is never
// changed after it is compiled, so it can be shared.
type RelicSchema struct {
	relicType     string
	title         string
	description   string
	properties    *schemaNode
	parts         map[string]*relicPartRule // nil: any part type
	relationships []string
	pointer       *schemaNode
	defs          map[string]*schemaNode
}

// Type is the relic type the schema is for.
func (s *RelicSchema) Type() string { return s.relicType }

const (
	relicPartBodyRequired = "required"
	relicPartBodyOptional = "optional"
	relicPartBodyNone     = "none"
)

type relicPartRule struct {
	description string
	fields      *schemaNode // nil: any fields
	body        string
	bodyFormats []string // nil: any format
}

// schemaNode is one compiled JSON Schema object.
type schemaNode struct {
	types           []string
	properties      map[string]*schemaNode
	required        []string
	additionalFalse bool
	items           *schemaNode
	minItems        int64 // -1 when absent, as for the other three counts
	maxItems        int64
	minLength       int64
	maxLength       int64
	minimum         *relicNumber
	maximum         *relicNumber
	minimumText     string
	maximumText     string
	enum            []string // canonical JSON of each allowed value, for messages
	enumValues      []any    // each allowed value, decoded, for comparing
	hasConst        bool
	constValue      string
	constParsed     any
	title           string
	description     string

	ref        *schemaNode // the resolved $ref, nil when there is none
	refText    string
	refPointer string
	refOwner   string // the $defs entry this node sits in, "" outside $defs
	refLocal   string // the local definition a #/$defs ref names
	depth      int    // the longest run of refs from here, -1 until computed
}

var relicSchemaTypeNames = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

var relicSchemaDefNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// schemaPathError is a problem with a schema file at one place in it, a JSON
// Pointer.
type schemaPathError struct {
	path    string
	message string
}

func (e *schemaPathError) Error() string {
	if e.path == "" {
		return e.message
	}
	return e.path + ": " + e.message
}

func schemaFail(path, format string, args ...any) error {
	return &schemaPathError{path: path, message: fmt.Sprintf(format, args...)}
}

func escapeSchemaPointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func schemaChild(path, token string) string {
	return path + "/" + escapeSchemaPointer(token)
}

func sortedSchemaKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// schemaResolve returns the compiled schema of another relic type a
// reference names, or nil when the type has none.
type schemaResolve func(relicType string) (*RelicSchema, error)

type schemaParser struct {
	relicType  string
	currentDef string
	refs       []*schemaNode
}

// CompileRelicSchema parses a schema file for relicType, refuses anything the
// closed list of words does not allow, pins each reference to another type to
// that type's newest version, and returns the document to store with the
// compiled schema. A refusal is a validation error of the form
// "schema for <type>: <path>: <message>", the path a JSON Pointer into the
// file. newest may be nil when no type has a schema.
func CompileRelicSchema(relicType string, source []byte, newest RelicSchemaLookup) (RelicSchemaDocument, *RelicSchema, error) {
	if err := ValidateRelicType(relicType); err != nil {
		return RelicSchemaDocument{}, nil, err
	}
	refuse := func(err error) error {
		var pathError *schemaPathError
		if errors.As(err, &pathError) {
			return Errorf(CategoryValidation, "schema for %s: %s", relicType, pathError.Error())
		}
		return err
	}
	canonical, err := CanonicalRelicValue(source)
	if err != nil {
		return RelicSchemaDocument{}, nil, Errorf(CategoryValidation, "schema for %s: %s", relicType, err.Error())
	}
	pins := map[string]string{}
	found := map[string]*RelicSchema{}
	resolve := func(other string) (*RelicSchema, error) {
		if schema, ok := found[other]; ok {
			return schema, nil
		}
		if newest == nil {
			return nil, nil
		}
		version, schema, ok, err := newest(other)
		if err != nil {
			return nil, err
		}
		if !ok || schema == nil {
			found[other] = nil
			return nil, nil
		}
		found[other] = schema
		pins[other] = version.ID
		return schema, nil
	}
	schema, err := compileRelicSchemaSource(relicType, canonical, resolve)
	if err != nil {
		return RelicSchemaDocument{}, nil, refuse(err)
	}
	document := RelicSchemaDocument{
		Format:  RelicSchemaDocumentFormat,
		Version: RelicSchemaDocumentVersion,
		Type:    relicType,
		Schema:  canonical,
	}
	if len(pins) > 0 {
		document.References = pins
	}
	encoded, err := EncodeDocument(document)
	if err != nil {
		return RelicSchemaDocument{}, nil, err
	}
	if len(encoded) > MaxRelicSchemaBytes {
		return RelicSchemaDocument{}, nil, Errorf(CategoryValidation,
			"schema for %s: the stored schema is %d bytes; the limit is %d bytes", relicType, len(encoded), MaxRelicSchemaBytes)
	}
	return document, schema, nil
}

// DecodeRelicSchemaDocument reads a stored schema document strictly. Anything
// wrong with it, its size included, is corrupt data.
func DecodeRelicSchemaDocument(data []byte) (RelicSchemaDocument, error) {
	if len(data) > MaxRelicSchemaBytes {
		return RelicSchemaDocument{}, corrupt("the stored schema is %d bytes; the limit is %d bytes", len(data), MaxRelicSchemaBytes)
	}
	var document RelicSchemaDocument
	if err := decodeOneJSON(data, &document); err != nil {
		return RelicSchemaDocument{}, err
	}
	if err := validateRelicSchemaDocument(document); err != nil {
		return RelicSchemaDocument{}, err
	}
	return document, nil
}

func validateRelicSchemaDocument(document RelicSchemaDocument) error {
	if document.Format != RelicSchemaDocumentFormat || document.Version != RelicSchemaDocumentVersion {
		return corrupt("unsupported relic schema document %q version %d", document.Format, document.Version)
	}
	if err := ValidateRelicType(document.Type); err != nil {
		return corrupt("the relic schema document's type: %s", err.Error())
	}
	for other, id := range document.References {
		if err := ValidateRelicType(other); err != nil || other == document.Type {
			return corrupt("the relic schema document refers to type %q", other)
		}
		if err := validateOperationID(id); err != nil {
			return err
		}
	}
	if len(document.Schema) == 0 {
		return corrupt("the relic schema document has no schema")
	}
	return nil
}

// LoadRelicSchema decodes and compiles a stored version. pinned returns each
// version the document's references name. A document over the size limit, of
// another type, or that does not compile is corrupt data naming the version.
func LoadRelicSchema(version RelicSchemaVersion, stored []byte, pinned RelicSchemaPinned) (*RelicSchema, error) {
	name := fmt.Sprintf("the schema for %s version %d", version.Type, version.Version)
	if len(stored) > MaxRelicSchemaBytes {
		return nil, corrupt("%s is %d bytes; the limit is %d bytes", name, len(stored), MaxRelicSchemaBytes)
	}
	document, err := DecodeRelicSchemaDocument(stored)
	if err != nil {
		return nil, Wrap(CategoryCorruptData, name, err)
	}
	if document.Type != version.Type {
		return nil, corrupt("%s holds a schema for %s", name, document.Type)
	}
	canonical, err := CanonicalRelicValue(document.Schema)
	if err != nil || !bytes.Equal(canonical, document.Schema) {
		return nil, corrupt("%s is not stored in canonical form", name)
	}
	used := map[string]bool{}
	resolve := func(other string) (*RelicSchema, error) {
		id, ok := document.References[other]
		if !ok {
			return nil, schemaFail("", "the schema refers to type %s without pinning a version of it", other)
		}
		if pinned == nil {
			return nil, schemaFail("", "no pinned version of %s can be read", other)
		}
		schema, err := pinned(id)
		if err != nil {
			return nil, err
		}
		if schema == nil || schema.relicType != other {
			return nil, schemaFail("", "the version pinned for %s is not a schema for %s", other, other)
		}
		used[other] = true
		return schema, nil
	}
	schema, err := compileRelicSchemaSource(version.Type, document.Schema, resolve)
	if err != nil {
		var pathError *schemaPathError
		if errors.As(err, &pathError) {
			return nil, corrupt("%s: %s", name, pathError.Error())
		}
		return nil, err
	}
	for other := range document.References {
		if !used[other] {
			return nil, corrupt("%s pins a version of %s it does not refer to", name, other)
		}
	}
	return schema, nil
}

// RelicSchemaRefusal is the validation error for a relic save that does not
// fit the version it names.
func RelicSchemaRefusal(version RelicSchemaVersion, problem RelicSchemaProblem) error {
	return Errorf(CategoryValidation, "relic does not fit schema %s: %s", version.Name(), problem.String())
}

// compileRelicSchemaSource compiles canonical schema text. A problem with the
// text is a *schemaPathError; any other error comes from resolve.
func compileRelicSchemaSource(relicType string, canonical []byte, resolve schemaResolve) (*RelicSchema, error) {
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, schemaFail("", "the schema is not valid JSON: %s", err.Error())
	}
	parser := &schemaParser{relicType: relicType}
	schema, err := parser.envelope(value)
	if err != nil {
		return nil, err
	}
	if err := parser.link(schema, resolve); err != nil {
		return nil, err
	}
	if err := checkSchemaLoops(schema, parser.refs); err != nil {
		return nil, err
	}
	for _, node := range parser.refs {
		if steps := 1 + node.ref.refDepth(); steps > MaxRelicSchemaRefDepth {
			return nil, schemaFail(node.refPointer,
				"the reference follows %d references in a row; the limit is %d", steps, MaxRelicSchemaRefDepth)
		}
	}
	// Work out every node's depth now, so a compiled schema is never written
	// to again and can be shared between goroutines.
	schema.eachRoot(func(node *schemaNode) { node.refDepth() })
	return schema, nil
}

func (s *RelicSchema) eachRoot(visit func(*schemaNode)) {
	for _, node := range []*schemaNode{s.properties, s.pointer} {
		if node != nil {
			visit(node)
		}
	}
	for _, rule := range s.parts {
		if rule.fields != nil {
			visit(rule.fields)
		}
	}
	for _, node := range s.defs {
		visit(node)
	}
}

func (p *schemaParser) envelope(value any) (*RelicSchema, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, schemaFail("", "the schema must be a JSON object")
	}
	schema := &RelicSchema{relicType: p.relicType, defs: map[string]*schemaNode{}}
	for _, key := range sortedSchemaKeys(object) {
		path := schemaChild("", key)
		member := object[key]
		var err error
		switch key {
		case "title":
			schema.title, err = schemaString(member, path)
		case "description":
			schema.description, err = schemaString(member, path)
		case "properties":
			schema.properties, err = p.schema(member, path)
		case "parts":
			schema.parts, err = p.parts(member, path)
		case "links":
			err = p.links(schema, member, path)
		case "$defs":
			err = p.defs(schema, member, path)
		default:
			err = schemaFail(path, "%q is not a member of a relic schema; the members are title, description, properties, parts, links and $defs", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return schema, nil
}

func (p *schemaParser) defs(schema *RelicSchema, value any, path string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return schemaFail(path, "$defs must be an object of named schemas")
	}
	for _, name := range sortedSchemaKeys(object) {
		child := schemaChild(path, name)
		if !relicSchemaDefNamePattern.MatchString(name) {
			return schemaFail(child, "a definition name must be 1 to 100 letters, digits, '-' or '_'")
		}
		p.currentDef = name
		node, err := p.schema(object[name], child)
		p.currentDef = ""
		if err != nil {
			return err
		}
		schema.defs[name] = node
	}
	return nil
}

func (p *schemaParser) parts(value any, path string) (map[string]*relicPartRule, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, schemaFail(path, "parts must be an object of part rules keyed by part type")
	}
	rules := make(map[string]*relicPartRule, len(object))
	for _, partType := range sortedSchemaKeys(object) {
		child := schemaChild(path, partType)
		if err := ValidateRelicPartType(partType); err != nil {
			return nil, schemaFail(child, "%s", err.Error())
		}
		rule, err := p.partRule(object[partType], child)
		if err != nil {
			return nil, err
		}
		rules[partType] = rule
	}
	return rules, nil
}

func (p *schemaParser) partRule(value any, path string) (*relicPartRule, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, schemaFail(path, "a part rule must be a JSON object")
	}
	rule := &relicPartRule{body: relicPartBodyOptional}
	for _, key := range sortedSchemaKeys(object) {
		child := schemaChild(path, key)
		member := object[key]
		var err error
		switch key {
		case "description":
			rule.description, err = schemaString(member, child)
		case "fields":
			rule.fields, err = p.schema(member, child)
		case "body":
			body, ok := member.(string)
			if !ok || (body != relicPartBodyRequired && body != relicPartBodyOptional && body != relicPartBodyNone) {
				err = schemaFail(child, `body must be "required", "optional" or "none"`)
			}
			rule.body = body
		case "bodyFormats":
			rule.bodyFormats, err = schemaBodyFormats(member, child)
		default:
			err = schemaFail(child, "%q is not a member of a part rule; the members are description, fields, body and bodyFormats", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return rule, nil
}

func schemaBodyFormats(value any, path string) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, schemaFail(path, "bodyFormats must be a non-empty list of body formats; leave it out to accept any")
	}
	seen := map[string]bool{}
	formats := make([]string, 0, len(list))
	for index, item := range list {
		child := schemaChild(path, fmt.Sprint(index))
		label, ok := item.(string)
		if !ok || label == "" {
			return nil, schemaFail(child, "a body format must be a non-empty string")
		}
		if err := ValidateRelicBodyFormat(label); err != nil {
			return nil, schemaFail(child, "%s", err.Error())
		}
		if seen[label] {
			return nil, schemaFail(child, "the body format %s is listed twice", label)
		}
		seen[label] = true
		formats = append(formats, label)
	}
	return formats, nil
}

func (p *schemaParser) links(schema *RelicSchema, value any, path string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return schemaFail(path, "links must be a JSON object")
	}
	for _, key := range sortedSchemaKeys(object) {
		child := schemaChild(path, key)
		member := object[key]
		switch key {
		case "relationships":
			list, ok := member.([]any)
			if !ok {
				return schemaFail(child, "relationships must be a list of relationship names")
			}
			seen := map[string]bool{}
			for index, item := range list {
				itemPath := schemaChild(child, fmt.Sprint(index))
				name, ok := item.(string)
				if !ok {
					return schemaFail(itemPath, "a relationship name must be a string")
				}
				if err := ValidateRelicRelation(name); err != nil {
					return schemaFail(itemPath, "%s", err.Error())
				}
				if seen[name] {
					return schemaFail(itemPath, "the relationship %s is listed twice", name)
				}
				seen[name] = true
				schema.relationships = append(schema.relationships, name)
			}
		case "pointer":
			node, err := p.schema(member, child)
			if err != nil {
				return err
			}
			schema.pointer = node
		default:
			return schemaFail(child, "%q is not a member of links; the members are relationships and pointer", key)
		}
	}
	return nil
}

func schemaString(value any, path string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", schemaFail(path, "the value must be a string")
	}
	return text, nil
}

func schemaCount(value any, path string) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, schemaFail(path, "the value must be a non-negative integer")
	}
	parsed, ok := parseRelicNumber(number.String())
	if !ok || parsed.sign < 0 || !parsed.isInteger() {
		return 0, schemaFail(path, "the value must be a non-negative integer")
	}
	return parsed.clampedInt64(), nil
}

func canonicalSchemaValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (p *schemaParser) schema(value any, path string) (*schemaNode, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, schemaFail(path, "a schema must be a JSON object")
	}
	node := &schemaNode{minItems: -1, maxItems: -1, minLength: -1, maxLength: -1, depth: -1}
	for _, key := range sortedSchemaKeys(object) {
		child := schemaChild(path, key)
		member := object[key]
		var err error
		switch key {
		case "type":
			node.types, err = schemaTypes(member, child)
		case "properties":
			properties, ok := member.(map[string]any)
			if !ok {
				return nil, schemaFail(child, "properties must be an object of named schemas")
			}
			node.properties = make(map[string]*schemaNode, len(properties))
			for _, name := range sortedSchemaKeys(properties) {
				rule, err := p.schema(properties[name], schemaChild(child, name))
				if err != nil {
					return nil, err
				}
				node.properties[name] = rule
			}
		case "required":
			node.required, err = schemaRequired(member, child)
		case "additionalProperties":
			allowed, ok := member.(bool)
			if !ok {
				err = schemaFail(child, "additionalProperties must be true or false")
			}
			node.additionalFalse = !allowed
		case "items":
			if _, isList := member.([]any); isList {
				return nil, schemaFail(child, "items must be one schema, not a list")
			}
			node.items, err = p.schema(member, child)
		case "minItems":
			node.minItems, err = schemaCount(member, child)
		case "maxItems":
			node.maxItems, err = schemaCount(member, child)
		case "minLength":
			node.minLength, err = schemaCount(member, child)
		case "maxLength":
			node.maxLength, err = schemaCount(member, child)
		case "minimum", "maximum":
			number, ok := member.(json.Number)
			var parsed relicNumber
			if ok {
				parsed, ok = parseRelicNumber(number.String())
			}
			if !ok {
				return nil, schemaFail(child, "%s must be a number", key)
			}
			if key == "minimum" {
				node.minimum, node.minimumText = &parsed, number.String()
			} else {
				node.maximum, node.maximumText = &parsed, number.String()
			}
		case "enum":
			list, ok := member.([]any)
			if !ok || len(list) == 0 {
				return nil, schemaFail(child, "enum must be a non-empty list of values")
			}
			node.enum, node.enumValues = make([]string, len(list)), list
			for index, item := range list {
				node.enum[index] = canonicalSchemaValue(item)
			}
		case "const":
			node.hasConst, node.constValue, node.constParsed = true, canonicalSchemaValue(member), member
		case "$ref":
			text, ok := member.(string)
			if !ok {
				return nil, schemaFail(child, "$ref must be a string")
			}
			node.refText, node.refPointer, node.refOwner = text, child, p.currentDef
			p.refs = append(p.refs, node)
		case "title":
			node.title, err = schemaString(member, child)
		case "description":
			node.description, err = schemaString(member, child)
		case "$defs":
			err = schemaFail(child, `the word "$defs" is allowed only at the top of the schema`)
		default:
			err = schemaFail(child, "the word %q is not supported in a relic schema", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return node, nil
}

func schemaTypes(value any, path string) ([]string, error) {
	const message = "type must be one of string, number, integer, boolean, object, array and null, or a non-empty list of them"
	if name, ok := value.(string); ok {
		if !relicSchemaTypeNames[name] {
			return nil, schemaFail(path, message)
		}
		return []string{name}, nil
	}
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, schemaFail(path, message)
	}
	seen := map[string]bool{}
	types := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok || !relicSchemaTypeNames[name] || seen[name] {
			return nil, schemaFail(path, "%s, each named once", message)
		}
		seen[name] = true
		types = append(types, name)
	}
	return types, nil
}

func schemaRequired(value any, path string) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, schemaFail(path, "required must be a list of names")
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, schemaFail(path, "required must be a list of names")
		}
		if seen[name] {
			return nil, schemaFail(path, "required lists %q twice", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

const relicSchemaDefsPrefix = "#/$defs/"

// link resolves every $ref the parser met, in the order it met them.
func (p *schemaParser) link(schema *RelicSchema, resolve schemaResolve) error {
	for _, node := range p.refs {
		ref := node.refText
		index := strings.Index(ref, relicSchemaDefsPrefix)
		if index < 0 {
			return schemaFail(node.refPointer,
				"the reference %q is outside the project; a reference is #/$defs/<name> or <type>#/$defs/<name>", ref)
		}
		otherType, name := ref[:index], ref[index+len(relicSchemaDefsPrefix):]
		if !relicSchemaDefNamePattern.MatchString(name) {
			return schemaFail(node.refPointer,
				"the reference %q must name one definition, as #/$defs/<name> or <type>#/$defs/<name>", ref)
		}
		if otherType == "" {
			target, ok := schema.defs[name]
			if !ok {
				return schemaFail(node.refPointer, "the reference %q names no definition in this schema", ref)
			}
			node.ref, node.refLocal = target, name
			continue
		}
		if err := ValidateRelicType(otherType); err != nil {
			return schemaFail(node.refPointer,
				"the reference %q is outside the project; a reference is #/$defs/<name> or <type>#/$defs/<name>", ref)
		}
		if otherType == p.relicType {
			return schemaFail(node.refPointer,
				"the reference %q names the type being saved; refer to its own definitions as #/$defs/%s", ref, name)
		}
		other, err := resolve(otherType)
		if err != nil {
			return err
		}
		if other == nil {
			return schemaFail(node.refPointer, "the reference %q names type %s, which has no schema", ref, otherType)
		}
		target, ok := other.defs[name]
		if !ok {
			return schemaFail(node.refPointer, "the reference %q names no definition in the schema for %s", ref, otherType)
		}
		node.ref = target
	}
	return nil
}

// checkSchemaLoops refuses references between this schema's own definitions
// that loop. A reference to another type cannot loop: the version it is pinned
// to was saved before this one.
func checkSchemaLoops(schema *RelicSchema, refs []*schemaNode) error {
	type edge struct{ target, pointer string }
	edges := map[string][]edge{}
	for _, node := range refs {
		if node.refOwner != "" && node.refLocal != "" {
			edges[node.refOwner] = append(edges[node.refOwner], edge{node.refLocal, node.refPointer})
		}
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(name string) error
	visit = func(name string) error {
		state[name] = visiting
		stack = append(stack, name)
		for _, next := range edges[name] {
			switch state[next.target] {
			case visiting:
				start := 0
				for index, entry := range stack {
					if entry == next.target {
						start = index
					}
				}
				loop := make([]string, 0, len(stack)-start+1)
				for _, entry := range append(append([]string{}, stack[start:]...), next.target) {
					loop = append(loop, relicSchemaDefsPrefix+entry)
				}
				return schemaFail(next.pointer, "the references loop: %s", strings.Join(loop, " → "))
			case unvisited:
				if err := visit(next.target); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	names := make([]string, 0, len(schema.defs))
	for name := range schema.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if state[name] == unvisited {
			if err := visit(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// refDepth is the longest run of references followed from this node or
// anything inside it. It is called only once loops are refused.
func (n *schemaNode) refDepth() int {
	if n.depth >= 0 {
		return n.depth
	}
	deepest := 0
	if n.ref != nil {
		deepest = 1 + n.ref.refDepth()
	}
	for _, rule := range n.properties {
		deepest = max(deepest, rule.refDepth())
	}
	if n.items != nil {
		deepest = max(deepest, n.items.refDepth())
	}
	n.depth = deepest
	return deepest
}

// RelicSchemaSummary is what the agent guidelines say about a type: its
// title and description, its required properties with their allowed values
// when an enum lists them, and its part types.
type RelicSchemaSummary struct {
	Title       string
	Description string
	Required    []RelicSchemaPropertySummary
	Parts       []RelicSchemaPartSummary
}

type RelicSchemaPropertySummary struct {
	Name    string
	Allowed []string
}

type RelicSchemaPartSummary struct {
	Type           string
	Description    string
	RequiredFields []string
	Body           string
	BodyFormats    []string
}

// Summary describes the schema for people and agents.
func (s *RelicSchema) Summary() RelicSchemaSummary {
	summary := RelicSchemaSummary{Title: s.title, Description: s.description}
	if s.properties != nil {
		chain := s.properties.chain()
		for _, name := range requiredNames(chain) {
			property := RelicSchemaPropertySummary{Name: name}
			for _, node := range chain {
				if rule, ok := node.properties[name]; ok && property.Allowed == nil {
					property.Allowed = allowedValues(rule)
				}
			}
			summary.Required = append(summary.Required, property)
		}
	}
	types := make([]string, 0, len(s.parts))
	for partType := range s.parts {
		types = append(types, partType)
	}
	sort.Strings(types)
	for _, partType := range types {
		rule := s.parts[partType]
		part := RelicSchemaPartSummary{Type: partType, Description: rule.description, Body: rule.body}
		if rule.fields != nil {
			part.RequiredFields = requiredNames(rule.fields.chain())
		}
		if rule.bodyFormats != nil {
			part.BodyFormats = append([]string(nil), rule.bodyFormats...)
		}
		summary.Parts = append(summary.Parts, part)
	}
	return summary
}

func requiredNames(chain []*schemaNode) []string {
	seen := map[string]bool{}
	var names []string
	for _, node := range chain {
		for _, name := range node.required {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func allowedValues(rule *schemaNode) []string {
	for _, node := range rule.chain() {
		if node.enum == nil {
			continue
		}
		allowed := make([]string, len(node.enum))
		for index, value := range node.enum {
			var text string
			if json.Unmarshal([]byte(value), &text) == nil {
				allowed[index] = text
			} else {
				allowed[index] = value
			}
		}
		return allowed
	}
	return nil
}
