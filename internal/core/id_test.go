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

func TestValidateTaskIDRejectsNonCanonicalULIDBody(t *testing.T) {
	for name, taskID := range map[string]string{
		"lowercase":  "WB-01k0m6b8a4ftt8c39mxxytw7c2",
		"mixed case": "WB-01K0m6B8A4FTT8C39MXXYTW7C2",
	} {
		t.Run(name, func(t *testing.T) {
			if got := CategoryOf(ValidateTaskID("WB", taskID)); got != CategoryValidation {
				t.Fatalf("ValidateTaskID(%q) category = %q, want %q", taskID, got, CategoryValidation)
			}
		})
	}
}

// PlausibleTaskID is the gate in front of advice to delete a ref from a shared
// remote, so it must keep saying yes to the two names that a stranger's junk is
// easily mistaken for: a task written under this project's key in an ID format
// this build predates, and a task of a second project sharing the namespace.
func TestPlausibleTaskIDAcceptsNamesThisBuildCannotOwn(t *testing.T) {
	const id = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"

	for name, candidate := range map[string]string{
		"canonical ID":                     id,
		"non-canonical body":               "WB-01k0m6b8a4ftt8c39mxxytw7c2",
		"unparsable body under our key":    "WB-NEXT-FORMAT",
		"empty body under our key":         "WB-",
		"nested under our key":             id + "/attachment",
		"peeled under our key":             id + "^{}",
		"another project's key":            "OPS-01K0M6B8A4FTT8C39MXXYTW7C2",
		"another project's lowercase body": "OPS-01k0m6b8a4ftt8c39mxxytw7c2",
		"nested under another key":         "OPS-01K0M6B8A4FTT8C39MXXYTW7C2/attachment",
		// A peeled name is judged by the task it points at under any key. It
		// answered for this project's key and not another's while the suffix
		// counted as part of the ID body, which made a second project's history
		// the one shape the gate would have offered for deletion.
		"peeled under another key": "OPS-01K0M6B8A4FTT8C39MXXYTW7C2^{}",
	} {
		t.Run(name, func(t *testing.T) {
			if !PlausibleTaskID("WB", candidate) {
				t.Fatalf("PlausibleTaskID(%q) = false, want true", candidate)
			}
		})
	}
}

// Only a name that carries neither this project's key prefix nor the
// <KEY>-<ULID> shape names nobody's task, and only such a name may be offered
// for deletion.
func TestPlausibleTaskIDRejectsNamesNoProjectCanOwn(t *testing.T) {
	for name, candidate := range map[string]string{
		"empty":                             "",
		"bare word":                         "EVIL",
		"nested bare word":                  "team/EVIL",
		"key with no body":                  "OPS-",
		"short body":                        "OPS-01K0M6B8A4FTT8C39MXXYTW7C",
		"long body":                         "OPS-01K0M6B8A4FTT8C39MXXYTW7C22",
		"lowercase key":                     "ops-01K0M6B8A4FTT8C39MXXYTW7C2",
		"body outside Crockford's alphabet": "OPS-01K0M6B8A4FTT8C39MXXYTW7CI",
		"our key without its separator":     "WB01K0M6B8A4FTT8C39MXXYTW7C2",
	} {
		t.Run(name, func(t *testing.T) {
			if PlausibleTaskID("WB", candidate) {
				t.Fatalf("PlausibleTaskID(%q) = true, want false", candidate)
			}
		})
	}
}

// A key this build cannot use disables only the rule that depends on it. The
// name is still judged against every valid project key's shape, because a
// misconfigured clone must not start recommending deletions.
func TestPlausibleTaskIDToleratesAnInvalidProjectKey(t *testing.T) {
	if !PlausibleTaskID("bad key", "OPS-01K0M6B8A4FTT8C39MXXYTW7C2") {
		t.Fatalf("PlausibleTaskID() = false for a well-shaped foreign task ID, want true")
	}
	if PlausibleTaskID("bad key", "bad key-EVIL") {
		t.Fatalf("PlausibleTaskID() = true for an invalid key's own name, want false")
	}
}
