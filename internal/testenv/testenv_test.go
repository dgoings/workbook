package testenv

import (
	"fmt"
	"strings"
	"testing"
)

// outcomeRecorder captures how MissingCapability disposes of a test without
// actually skipping or failing the test that drives it.
type outcomeRecorder struct {
	testing.TB
	helper  bool
	fatal   string
	fataled bool
	skip    string
	skipped bool
}

func (r *outcomeRecorder) Helper() { r.helper = true }

func (r *outcomeRecorder) Fatalf(format string, args ...any) {
	r.fataled = true
	r.fatal = fmt.Sprintf(format, args...)
}

func (r *outcomeRecorder) Skipf(format string, args ...any) {
	r.skipped = true
	r.skip = fmt.Sprintf(format, args...)
}

// Mutation witness: dropping the marker prefix makes capability skips
// indistinguishable from intentional exclusions in `go test -json` output.
func TestMissingCapabilitySkipsWithMarkedReasonByDefault(t *testing.T) {
	t.Setenv(RequireCapabilitiesVariable, "")
	recorder := &outcomeRecorder{TB: t}

	MissingCapability(recorder, "node is required to execute the embedded client behavior")

	if recorder.fataled {
		t.Fatalf("MissingCapability failed the test without %s set: %q", RequireCapabilitiesVariable, recorder.fatal)
	}
	if !recorder.skipped {
		t.Fatal("MissingCapability did not skip the test")
	}
	want := MissingCapabilityPrefix + "node is required to execute the embedded client behavior"
	if recorder.skip != want {
		t.Fatalf("skip reason = %q, want %q", recorder.skip, want)
	}
	if !recorder.helper {
		t.Fatal("MissingCapability did not mark itself as a helper")
	}
}

// Mutation witness: skipping despite the requirement variable restores the
// silent-shrink failure mode CI exists to prevent.
func TestMissingCapabilityFailsWhenCapabilitiesAreRequired(t *testing.T) {
	t.Setenv(RequireCapabilitiesVariable, "1")
	recorder := &outcomeRecorder{TB: t}

	MissingCapability(recorder, "Git does not support SHA-256 repositories")

	if recorder.skipped {
		t.Fatalf("MissingCapability skipped with %s set: %q", RequireCapabilitiesVariable, recorder.skip)
	}
	if !recorder.fataled {
		t.Fatal("MissingCapability did not fail the test")
	}
	if !strings.Contains(recorder.fatal, "Git does not support SHA-256 repositories") {
		t.Fatalf("failure message %q does not name the missing capability", recorder.fatal)
	}
	if !strings.Contains(recorder.fatal, RequireCapabilitiesVariable) {
		t.Fatalf("failure message %q does not explain which variable demanded the capability", recorder.fatal)
	}
}

func TestMissingCapabilityFormatsTheReason(t *testing.T) {
	t.Setenv(RequireCapabilitiesVariable, "")
	recorder := &outcomeRecorder{TB: t}

	MissingCapability(recorder, "probe failed: %v", fmt.Errorf("unknown hash algorithm"))

	want := MissingCapabilityPrefix + "probe failed: unknown hash algorithm"
	if recorder.skip != want {
		t.Fatalf("skip reason = %q, want %q", recorder.skip, want)
	}
}
