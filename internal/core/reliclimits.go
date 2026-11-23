package core

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits on what a relic stores. They are part of the storage format: they can
// be raised later and never lowered.
const (
	MaxRelicTitleBytes        = 500
	MaxRelicTypeBytes         = 100
	MaxRelicPartTypeBytes     = 100
	MaxRelicBodyFormatBytes   = 16
	MaxRelicPartPositionBytes = 256
	MaxRelicPropertiesBytes   = 64 * 1024
	MaxRelicPartFieldsBytes   = 16 * 1024
	MaxRelicBodyBytes         = 1024 * 1024
	MaxRelicLiveParts         = 2000
	MaxRelicParts             = 4000
	MaxRelicLiveBodyBytes     = 16 * 1024 * 1024
)

var (
	relicTypePattern       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	relicBodyFormatPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
)

func relicSizeError(what string, size, limit int) error {
	return Errorf(CategoryValidation, "%s is %d bytes; the limit is %d bytes", what, size, limit)
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
func ValidateRelicName(kind, name string) error {
	if name == "" {
		return Errorf(CategoryValidation, "a %s name must not be empty", kind)
	}
	if !utf8.ValidString(name) {
		return Errorf(CategoryValidation, "a %s name must be valid UTF-8", kind)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '=' {
			return Errorf(CategoryValidation,
				"a %s name must not contain '=' or control characters", kind)
		}
	}
	return nil
}

// CanonicalRelicValue decodes exactly one JSON value and re-encodes it in the
// stored form: numbers keep their written text, object members are sorted, and
// a repeated member keeps its last value.
func CanonicalRelicValue(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, Errorf(CategoryValidation, "the value is not valid JSON: %s", err.Error())
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, Errorf(CategoryValidation, "the value must be exactly one JSON value")
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, Wrap(CategoryValidation, "the value cannot be encoded", err)
	}
	return json.RawMessage(bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))), nil
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
