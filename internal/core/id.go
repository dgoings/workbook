package core

import (
	"crypto/rand"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

type IDSource interface {
	New() (string, error)
}

type IDSourceFunc func() (string, error)

func (f IDSourceFunc) New() (string, error) {
	return f()
}

type CryptoULIDSource struct {
	Now     func() time.Time
	Entropy io.Reader
}

func (s CryptoULIDSource) New() (string, error) {
	now := s.Now
	if now == nil {
		now = time.Now
	}

	entropy := s.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}

	id, err := ulid.New(ulid.Timestamp(now().UTC()), entropy)
	if err != nil {
		return "", Wrap(CategoryOperational, "cannot generate ULID", err)
	}
	return id.String(), nil
}

func ValidateProjectKey(key string) error {
	if !projectKeyPattern.MatchString(key) {
		return Errorf(CategoryValidation, "project key %q must match %s", key, projectKeyPattern)
	}
	return nil
}

// ulidShapePattern matches a canonical ULID body's length and alphabet without
// decoding it, and without insisting on the canonical uppercase form. It is
// deliberately looser than ulid.ParseStrict: it answers "could another
// Workbook have written this", where accepting one name too many costs a piece
// of advice and rejecting one too few costs somebody's history.
var ulidShapePattern = regexp.MustCompile(`(?i)^[0-9A-HJKMNP-TV-Z]{26}$`)

// PlausibleTaskID reports whether name could be some Workbook's task ID even
// though ValidateTaskID rejects it for key. Two names qualify: one carrying
// this project's own key prefix, which a version writing an ID format this
// build predates would produce, and one shaped like <KEY>-<ULID> under any
// valid project key, which a second project sharing a remote's task namespace
// produces. A name nested under either is judged by the segment it hangs from,
// so a child ref is as protected as its parent.
//
// It exists to gate destructive advice, never to widen what Workbook reads as a
// task: a true answer means only "do not offer to delete this". Every name that
// fails both rules belongs to no project's ID format and can be named as
// removable; ValidateTaskID remains the sole authority on what is a task ID.
func PlausibleTaskID(key, name string) bool {
	if ValidateProjectKey(key) == nil && strings.HasPrefix(name, key+"-") {
		return true
	}
	head, _, _ := strings.Cut(name, "/")
	foreignKey, body, separated := strings.Cut(head, "-")
	if !separated {
		return false
	}
	return ValidateProjectKey(foreignKey) == nil && ulidShapePattern.MatchString(body)
}

func ValidateTaskID(key, taskID string) error {
	if err := ValidateProjectKey(key); err != nil {
		return err
	}

	prefix := key + "-"
	if !strings.HasPrefix(taskID, prefix) {
		return Errorf(CategoryValidation, "task ID %q must begin with %q", taskID, prefix)
	}
	suffix := strings.TrimPrefix(taskID, prefix)
	parsed, err := ulid.ParseStrict(suffix)
	if err != nil {
		return Wrap(CategoryValidation, "task ID must contain a canonical ULID", err)
	}
	if parsed.String() != suffix {
		return Errorf(CategoryValidation, "task ID must contain a canonical uppercase ULID")
	}
	return nil
}
