package core

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
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

func TestCryptoULIDSourceClassifiesEntropyFailureAsOperational(t *testing.T) {
	cause := errors.New("entropy unavailable")
	source := CryptoULIDSource{
		Now:     func() time.Time { return time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC) },
		Entropy: iotest.ErrReader(cause),
	}

	_, err := source.New()
	if got, want := CategoryOf(err), CategoryOperational; got != want {
		t.Fatalf("New() category = %q, want %q; error = %v", got, want, err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("New() error = %v, want cause %v", err, cause)
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

func TestValidateTaskIDShapeRefusesEveryNameThatIsNotATaskID(t *testing.T) {
	const id = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"

	if err := ValidateTaskIDShape(id); err != nil {
		t.Fatalf("ValidateTaskIDShape() canonical ID error = %v", err)
	}
	if key, body, ok := ParseTaskID(id); !ok || key != "WB" || body != "01K0M6B8A4FTT8C39MXXYTW7C2" {
		t.Fatalf("ParseTaskID() = (%q, %q, %v), want the ID split in two", key, body, ok)
	}

	// A key this project does not have is not on this list: that is ownership,
	// which KeySet.Owns answers, and a name under another project's key is a
	// well-formed task ID.
	for name, candidate := range map[string]string{
		"lowercase":         strings.ToLower(id),
		"lowercase key":     "wb-01K0M6B8A4FTT8C39MXXYTW7C2",
		"missing separator": "WB01K0M6B8A4FTT8C39MXXYTW7C2",
		"invalid ULID":      "WB-01K0M6B8A4FTT8C39MXXYTW7C!",
		"lowercase body":    "WB-01k0m6b8a4ftt8c39mxxytw7c2",
		"mixed-case body":   "WB-01K0m6B8A4FTT8C39MXXYTW7C2",
		"no body":           "WB-",
		"empty":             "",
	} {
		t.Run(name, func(t *testing.T) {
			if got := CategoryOf(ValidateTaskIDShape(candidate)); got != CategoryValidation {
				t.Fatalf("ValidateTaskIDShape(%q) category = %q, want %q", candidate, got, CategoryValidation)
			}
			if _, _, ok := ParseTaskID(candidate); ok {
				t.Fatalf("ParseTaskID(%q) = true, want false", candidate)
			}
		})
	}
	if err := ValidateTaskIDShape("OTHER-01K0M6B8A4FTT8C39MXXYTW7C2"); err != nil {
		t.Fatalf("ValidateTaskIDShape(another project's key) = %v, want nil: shape is not ownership", err)
	}
}
