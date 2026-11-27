package cli

import (
	"strings"
	"testing"
)

// A project's first schema puts a kind of change in its settings that v0.6.1
// does not know. That release still reads the settings, and is told to
// upgrade before it changes them.

// TestReleasedV061ReadsSettingsWithSchemasButRefusesChange plays the v0.6.1
// binary against an origin whose settings hold a schema.
func TestReleasedV061ReadsSettingsWithSchemasButRefusesChange(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the v0.6.1 release; skipped in -short mode")
	}
	t.Parallel()
	binary := v0061Binary(t)

	writer, older := cliSyncRepositories(t)
	if code, _, stderr := run(t, writer, "setup"); code != 0 {
		t.Fatalf("writer setup code = %d; stderr = %q", code, stderr)
	}
	mustPutSchema(t, writer, "spec", specSchemaOne)
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}
	originConfig := func() string {
		t.Helper()
		return cliGitOutput(t, writer, "ls-remote", "origin", "refs/workbook/config")
	}
	published := originConfig()
	if published == "" || !strings.HasPrefix(published, gitOutput(t, writer, "rev-parse", "refs/workbook/config")) {
		t.Fatalf("origin's configuration ledger = %q, want the writer's tip", published)
	}

	for _, args := range [][]string{{"setup"}, {"sync"}, {"status", "list", "--json"}, {"list", "--json"}} {
		if code, stdout, stderr := runBinary(t, binary, older, args...); code != 0 {
			t.Fatalf("v0.6.1 %s = (%d, %q, %q)", strings.Join(args, " "), code, stdout, stderr)
		}
	}
	code, stdout, stderr := runBinary(t, binary, older, "status", "add", "review")
	if code == 0 || !strings.Contains(stdout+stderr, "written by a newer workbook; upgrade workbook") {
		t.Fatalf("v0.6.1 status add = (%d, %q, %q), want the newer-writer refusal", code, stdout, stderr)
	}
	if got := originConfig(); got != published {
		t.Fatalf("origin's configuration ledger changed from %q to %q", published, got)
	}
}
