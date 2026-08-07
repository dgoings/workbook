// Package testenv distinguishes tests skipped because the environment lacks
// an optional capability, such as a node binary or SHA-256 Git support, from
// tests that are intentionally excluded. Capability skips carry a machine-
// recognizable marker, and an environment that must run the whole suite can
// turn them into failures so coverage cannot shrink silently.
package testenv

import (
	"fmt"
	"os"
	"testing"
)

// RequireCapabilitiesVariable names the environment variable that, when set
// to any non-empty value, turns a missing optional capability into a test
// failure instead of a skip. CI sets it after provisioning every capability
// so a green run proves the full suite executed.
const RequireCapabilitiesVariable = "WORKBOOK_TEST_REQUIRE_CAPABILITIES"

// MissingCapabilityPrefix begins every skip reason recorded by
// MissingCapability, so tooling reading `go test -json` output can tell
// capability skips apart from intentional exclusions.
const MissingCapabilityPrefix = "missing capability: "

// MissingCapability reports that the environment lacks an optional capability
// the calling test needs. By default the test is skipped with a marked
// reason; when RequireCapabilitiesVariable is set the test fails instead,
// because the environment promised every capability would be present.
func MissingCapability(t testing.TB, format string, args ...any) {
	t.Helper()
	reason := fmt.Sprintf(format, args...)
	if os.Getenv(RequireCapabilitiesVariable) != "" {
		t.Fatalf("%s%s (%s is set, so a missing capability fails instead of skipping)",
			MissingCapabilityPrefix, reason, RequireCapabilitiesVariable)
		return
	}
	t.Skipf("%s%s", MissingCapabilityPrefix, reason)
}
