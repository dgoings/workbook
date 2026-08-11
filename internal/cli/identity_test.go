package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupJoinsOriginIdentityRefWithNoCommittedConfigAnywhere is the bootstrap
// this story exists for. Origin publishes its identity ref but no branch in the
// repository ever gained `.workbook/config.json`, so the older probe — read
// origin's default branch — has nothing to find. Setup must still join the
// project rather than mint a second one.
func TestSetupJoinsOriginIdentityRefWithNoCommittedConfigAnywhere(t *testing.T) {
	_, seed, stale := originAdoptedAfterClone(t)

	// The seed adopts Workbook and shares tasks, but deliberately never commits
	// its configuration to any branch.
	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	seeded := []string{
		cliCreateTask(t, seed, "Shared with nothing committed").ID,
		cliCreateTask(t, seed, "Also shared with nothing committed").ID,
	}
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}
	if gitOutput(t, seed, "status", "--porcelain", "--", ".workbook") == "" {
		t.Fatal("the fixture committed the tracked configuration; this test needs it uncommitted")
	}

	code, stdout, stderr := run(t, stale, "setup")
	if code != 0 {
		t.Fatalf("stale setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got, want := projectID(t, stdout), projectIDOf(t, seed); got != want {
		t.Fatalf("stale checkout adopted project %q, want origin's %q", got, want)
	}
	if got, want := projectIdentitySource(t, stdout), "(adopted from the published project identity)"; got != want {
		t.Fatalf("stale setup identity source = %q, want %q", got, want)
	}
	if code, _, stderr := run(t, stale, "sync"); code != 0 {
		t.Fatalf("stale sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, stale); !sameIDs(got, seeded) {
		t.Fatalf("adopted tasks = %v, want %v", got, seeded)
	}
}

// A checkout whose branch carries no tracked configuration is now usable: the
// identity ref says which project it is. The missing advisory copy is worth one
// line on stderr and nothing more.
func TestCommandsReportMissingTrackedConfigurationOnceAndKeepWorking(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Recorded before the branch switch")
	if err := os.Remove(filepath.Join(repository, ".workbook", "config.json")); err != nil {
		t.Fatalf("remove tracked configuration: %v", err)
	}

	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, task.ID) {
		t.Fatalf("list stdout = %q, want task %s", stdout, task.ID)
	}
	warnings := 0
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if strings.HasPrefix(line, "workbook: warning: ") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("list stderr = %q, want exactly one warning line", stderr)
	}
	if !strings.Contains(stderr, "refs/workbook/project") || !strings.Contains(stderr, ".workbook/config.json") {
		t.Fatalf("list stderr = %q, want it to name the ref and the missing file", stderr)
	}
}

// Every caller that parses sync's JSON reads a fixed envelope. The identity
// stage must add a member only when it has something to report, so a
// steady-state run stays byte-identical to what earlier versions emitted.
func TestSyncJSONOmitsIdentityWhenNothingChanged(t *testing.T) {
	_, seed, _ := originAdoptedAfterClone(t)
	// --no-sync keeps bootstrap local, so the publication origin needs is left
	// for the first explicit sync to make.
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, first, stderr := run(t, seed, "sync", "--json")
	if code != 0 {
		t.Fatalf("first sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(first, `"identity"`) {
		t.Fatalf("first sync JSON = %q, want the one-time identity publication reported", first)
	}

	code, second, stderr := run(t, seed, "sync", "--json")
	if code != 0 {
		t.Fatalf("second sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.Contains(second, `"identity"`) {
		t.Fatalf("second sync JSON = %q, want no identity member once origin agrees", second)
	}
	var envelope struct {
		Data struct {
			Remote string          `json:"remote"`
			Fetch  json.RawMessage `json:"fetch"`
			Push   json.RawMessage `json:"push"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(second), &envelope); err != nil {
		t.Fatalf("decode sync result: %v; output = %q", err, second)
	}
	if envelope.Data.Remote != "origin" || len(envelope.Data.Fetch) == 0 || len(envelope.Data.Push) == 0 {
		t.Fatalf("sync result = %q, want the unchanged remote, fetch and push members", second)
	}
}
