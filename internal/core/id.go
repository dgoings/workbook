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
		return "", err
	}
	return id.String(), nil
}

func ValidateProjectKey(key string) error {
	if !projectKeyPattern.MatchString(key) {
		return Errorf(CategoryValidation, "project key %q must match %s", key, projectKeyPattern)
	}
	return nil
}

func ValidateTaskID(key, taskID string) error {
	if err := ValidateProjectKey(key); err != nil {
		return err
	}

	prefix := key + "-"
	if !strings.HasPrefix(taskID, prefix) {
		return Errorf(CategoryValidation, "task ID %q must begin with %q", taskID, prefix)
	}
	if _, err := ulid.ParseStrict(strings.TrimPrefix(taskID, prefix)); err != nil {
		return Wrap(CategoryValidation, "task ID must contain a canonical ULID", err)
	}
	return nil
}
