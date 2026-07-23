package core

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

func TestIDSourceFuncCallsFunction(t *testing.T) {
	want := errors.New("entropy unavailable")
	source := IDSourceFunc(func() (string, error) {
		return "", want
	})

	got, err := source.New()
	if got != "" {
		t.Fatalf("New() ID = %q, want empty", got)
	}
	if !errors.Is(err, want) {
		t.Fatalf("New() error = %v, want %v", err, want)
	}
}

func TestCryptoULIDSourceUsesInjectedClockAndEntropy(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	source := CryptoULIDSource{
		Now:     func() time.Time { return now },
		Entropy: bytes.NewReader(make([]byte, 10)),
	}

	got, err := source.New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	parsed, err := ulid.ParseStrict(got)
	if err != nil {
		t.Fatalf("New() ID %q is not a strict ULID: %v", got, err)
	}
	if gotTime := time.UnixMilli(int64(parsed.Time())); !gotTime.Equal(now.UTC()) {
		t.Fatalf("New() timestamp = %s, want %s", gotTime, now.UTC())
	}
}

func TestValidateProjectKey(t *testing.T) {
	valid := []string{"WB", "A1", "WORKBOOK10"}
	for _, key := range valid {
		t.Run("valid/"+key, func(t *testing.T) {
			if err := ValidateProjectKey(key); err != nil {
				t.Fatalf("ValidateProjectKey(%q) error = %v", key, err)
			}
		})
	}

	invalid := []string{"", "A", "1A", "wb", "WORKBOOK123"}
	for _, key := range invalid {
		t.Run("invalid/"+key, func(t *testing.T) {
			if got := CategoryOf(ValidateProjectKey(key)); got != CategoryValidation {
				t.Fatalf("ValidateProjectKey(%q) category = %q, want %q", key, got, CategoryValidation)
			}
		})
	}
}

func TestValidateTaskID(t *testing.T) {
	const id = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"

	if err := ValidateTaskID("WB", id); err != nil {
		t.Fatalf("ValidateTaskID() canonical ID error = %v", err)
	}

	for name, candidate := range map[string]string{
		"lowercase":         strings.ToLower(id),
		"wrong key":         "OTHER-01K0M6B8A4FTT8C39MXXYTW7C2",
		"missing separator": "WB01K0M6B8A4FTT8C39MXXYTW7C2",
		"invalid ULID":      "WB-01K0M6B8A4FTT8C39MXXYTW7C!",
	} {
		t.Run(name, func(t *testing.T) {
			if got := CategoryOf(ValidateTaskID("WB", candidate)); got != CategoryValidation {
				t.Fatalf("ValidateTaskID(%q) category = %q, want %q", candidate, got, CategoryValidation)
			}
		})
	}
}
