package core

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on what a relic stores. They are part of the storage format: they can
// be raised later and never lowered.
//
// A limit on one stored item is checked when the item is saved and again when
// it is read: the title, a type name, a body format label, a position, one
// property value, one field value, how deep a value nests, a property or field
// name, one body and a link's relationship name. A count or a total is checked
// only when a change makes it grow, and never when a relic is read, so that no
// sequence of changes that were each allowed can add up to a relic nothing
// will read: the live parts, the parts counting removed ones, the bodies of
// the live parts together, all of a relic's properties together, all of one
// part's fields together and the live links.
const (
	MaxRelicTitleBytes        = 500
	MaxRelicTypeBytes         = 100
	MaxRelicPartTypeBytes     = 100
	MaxRelicBodyFormatBytes   = 16
	MaxRelicPartPositionBytes = 256
	// MaxRelicPropertyValueBytes and MaxRelicFieldValueBytes bound one stored
	// value, measured as its canonical bytes: the form CanonicalRelicValue
	// gives, escapes included.
	MaxRelicPropertyValueBytes = 64 * 1024
	MaxRelicFieldValueBytes    = 16 * 1024
	// MaxRelicPropertiesBytes is a total: the encoded properties object of one
	// relic. MaxRelicPartFieldsBytes is a total: one part's fields.json.
	MaxRelicPropertiesBytes = 64 * 1024
	MaxRelicPartFieldsBytes = 16 * 1024
	MaxRelicBodyBytes       = 1024 * 1024
	MaxRelicLiveParts       = 2000
	MaxRelicParts           = 4000
	MaxRelicLiveBodyBytes   = 16 * 1024 * 1024
	// MaxRelicNameBytes bounds one property name and one field name.
	MaxRelicNameBytes = 100
	// MaxRelicValueDepth bounds how deep one property or field value nests
	// arrays and objects: a scalar is depth 0 and [1] is depth 1.
	MaxRelicValueDepth = 32
	// MaxRelicRelationBytes bounds a link's relationship name, checked on
	// save and on read. MaxRelicLinks is a count: a relic's live links,
	// checked only when a change makes it grow.
	MaxRelicRelationBytes = 100
	MaxRelicLinks         = 2000
)

var (
	relicTypePattern       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	relicBodyFormatPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
)

func relicSizeError(what string, size, limit int) error {
	return Errorf(CategoryValidation, "%s is %d bytes; the limit is %d bytes", what, size, limit)
}

// relicTotalSizeError is relicSizeError for several things measured together,
// as in "the relic's properties are 70000 bytes".
func relicTotalSizeError(what string, size, limit int) error {
	return Errorf(CategoryValidation, "%s are %d bytes; the limit is %d bytes", what, size, limit)
}

// ValidateRelicTitle trims a title and checks it, returning the trimmed form.
func ValidateRelicTitle(title string) (string, error) {
	if !utf8.ValidString(title) {
		return "", Errorf(CategoryValidation, "the relic title must be valid UTF-8")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", Errorf(CategoryValidation, "the relic title must not be blank")
	}
	if len(title) > MaxRelicTitleBytes {
		return "", relicSizeError("the relic title", len(title), MaxRelicTitleBytes)
	}
	return title, nil
}

func validateRelicToken(what, name string, limit int) error {
	if len(name) > limit {
		return relicSizeError("the "+what, len(name), limit)
	}
	if !relicTypePattern.MatchString(name) {
		return Errorf(CategoryValidation,
			"the %s %q must be lowercase letters and digits, with single hyphens between words", what, name)
	}
	return nil
}

// ValidateRelicType checks a relic type name.
func ValidateRelicType(name string) error {
	return validateRelicToken("relic type", name, MaxRelicTypeBytes)
}

// ValidateRelicPartType checks a part type name.
func ValidateRelicPartType(name string) error {
	return validateRelicToken("part type", name, MaxRelicPartTypeBytes)
}

// ValidateRelicBodyFormat checks a body format label. The empty label means no
// format and is valid.
func ValidateRelicBodyFormat(label string) error {
	if label == "" {
		return nil
	}
	if len(label) > MaxRelicBodyFormatBytes {
		return relicSizeError("the body format", len(label), MaxRelicBodyFormatBytes)
	}
	if !relicBodyFormatPattern.MatchString(label) {
		return Errorf(CategoryValidation,
			"the body format %q must be lowercase letters, digits, '.', '+' or '-', starting with a letter or digit", label)
	}
	return nil
}

// ValidateRelicName checks a property or field name. kind is "property" or
// "field" and only shapes the message.
//
// A name is at most MaxRelicNameBytes of valid UTF-8, not empty, without '=',
// a control character or a format character, and with no white space at either
// end. A control character is one in Unicode's category Cc: U+0000 to U+001F,
// U+007F, and U+0080 to U+009F. A format character is one in category Cf, such
// as U+200B, U+FEFF, U+202E and U+00AD. White space is Unicode's White_Space property: two names
// that differ only by a space nobody can see would be two properties. White
// space inside a name is the name's own, unless it is a control character, as
// a tab is. The
// name is not required to be in a Unicode normalization form; nothing here
// compares names except byte for byte. A name over the limit is reported by
// its size, never repeated.
func ValidateRelicName(kind, name string) error {
	if name == "" {
		return Errorf(CategoryValidation, "a %s name must not be empty", kind)
	}
	if len(name) > MaxRelicNameBytes {
		return relicSizeError("a "+kind+" name", len(name), MaxRelicNameBytes)
	}
	if !utf8.ValidString(name) {
		return Errorf(CategoryValidation, "a %s name must be valid UTF-8", kind)
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '=' {
			return Errorf(CategoryValidation,
				"a %s name must not contain '=' or control characters", kind)
		}
		// A format character (category Cf, such as U+200B, U+FEFF, U+202E
		// or U+00AD) is not seen, so two names that differ by one look the
		// same, and a bidirectional one reorders what is shown after it.
		if unicode.Is(unicode.Cf, r) {
			return Errorf(CategoryValidation,
				"a %s name must not contain invisible format characters, such as U+%04X", kind, r)
		}
	}
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	if unicode.Is(unicode.White_Space, first) || unicode.Is(unicode.White_Space, last) {
		return Errorf(CategoryValidation, "a %s name must not begin or end with white space", kind)
	}
	return nil
}

// checkRelicValueDepth refuses a value that nests deeper than the limit.
func checkRelicValueDepth(depth int) error {
	if depth > MaxRelicValueDepth {
		return Errorf(CategoryValidation,
			"the value nests arrays and objects %d levels deep; the limit is %d levels", depth, MaxRelicValueDepth)
	}
	return nil
}

// checkRelicValueSize refuses one property or field value over the limit for
// one value. kind is "property" or "field", and size is the value's canonical
// bytes.
func checkRelicValueSize(kind string, size int) error {
	limit := MaxRelicPropertyValueBytes
	if kind == "field" {
		limit = MaxRelicFieldValueBytes
	}
	if size > limit {
		return relicSizeError("a "+kind+" value", size, limit)
	}
	return nil
}

// CanonicalRelicValue decodes exactly one JSON value and re-encodes it in the
// stored form: numbers keep their written text and object members are sorted.
// The encoding is exactly json.Marshal's, including its HTML escaping (a "<",
// ">" or "&" in a string is written as its six-byte \u escape). Stored values
// are embedded as json.RawMessage in documents that core.EncodeDocument writes
// with json.Marshal, which escapes a RawMessage again, so a form that left
// them bare would differ from the bytes read back from storage.
//
// A value that nests deeper than MaxRelicValueDepth is refused first, by a
// count that does not recurse, so no depth reaches the decoder or the encoder.
// The same pass over the bytes finds text the decoder would replace instead of
// refusing, and checkRelicValueText refuses it, along with an object that
// names a member twice: the value stored is the value given, or nothing is.
func CanonicalRelicValue(raw []byte) (json.RawMessage, error) {
	scan := scanRelicJSONText(raw)
	if err := checkRelicValueDepth(scan.depth); err != nil {
		return nil, err
	}
	if err := checkRelicValueText(raw, scan); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, Errorf(CategoryValidation, "the value is not valid JSON: %s", err.Error())
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, Errorf(CategoryValidation, "the value must be exactly one JSON value")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, Wrap(CategoryValidation, "the value cannot be encoded", err)
	}
	return json.RawMessage(encoded), nil
}

// BodyFormatFromFileName derives a body format label from a file name's ending:
// lowercased, without its dot, empty when there is no ending.
func BodyFormatFromFileName(name string) (string, error) {
	label := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if err := ValidateRelicBodyFormat(label); err != nil {
		return "", Errorf(CategoryValidation,
			"the file ending of %q cannot be used as a body format (%s); pass --body-format", name, err.Error())
	}
	return label, nil
}
