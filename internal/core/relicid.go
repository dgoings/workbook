package core

import (
	"strings"

	"github.com/oklog/ulid/v2"
)

// relicIDMarker separates a relic ID's project key from its ULID. A task ID
// body is a bare ULID, whose first character is never R, so the two shapes can
// never be mistaken for each other.
const relicIDMarker = "R-"

// NewRelicID joins a project key and a ULID body into a relic ID.
func NewRelicID(key, ulidBody string) string {
	return key + "-" + relicIDMarker + ulidBody
}

// ParseRelicID splits a relic ID into its project key and ULID body, and
// reports whether it is shaped like a relic ID.
func ParseRelicID(relicID string) (key, body string, ok bool) {
	key, rest, separated := strings.Cut(relicID, "-")
	if !separated || ValidateProjectKey(key) != nil {
		return "", "", false
	}
	body, marked := strings.CutPrefix(rest, relicIDMarker)
	if !marked || !isCanonicalULID(body) {
		return "", "", false
	}
	return key, body, true
}

// ValidateRelicIDShape is ParseRelicID with a message.
func ValidateRelicIDShape(relicID string) error {
	if _, _, ok := ParseRelicID(relicID); !ok {
		return Errorf(CategoryValidation, "relic ID %q must be <KEY>-R-<ULID>", relicID)
	}
	return nil
}

// ValidatePartID requires a canonical uppercase ULID, which is the ID of the
// operation that added the part.
func ValidatePartID(partID string) error {
	if !isCanonicalULID(partID) {
		return Errorf(CategoryValidation, "part ID %q must be a canonical uppercase ULID", partID)
	}
	return nil
}

// ParsePartAddress splits <relic>#<part> at the first '#'. An address without
// '#' names a relic alone. Neither half is checked for shape here.
func ParsePartAddress(address string) (relic, part string, hasPart bool, err error) {
	relic, part, hasPart = strings.Cut(address, "#")
	if relic == "" || (hasPart && (part == "" || strings.Contains(part, "#"))) {
		return "", "", false, Errorf(CategoryValidation,
			"address %q must be <relic-id> or <relic-id>#<part-id>", address)
	}
	return relic, part, hasPart, nil
}

func isCanonicalULID(value string) bool {
	parsed, err := ulid.ParseStrict(value)
	return err == nil && parsed.String() == value
}
