package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The v0.6.1 release has never heard of relics. It must keep working in a
// clone of an origin that holds them: its fetch refspecs name only tasks,
// project identity and configuration, so relic refs stay out of its clone, and
// nothing it does touches them on origin.

// TestReleasedV061IgnoresRelicsOnOrigin plays the v0.6.1 binary against an
// origin whose relics and tasks this build published.
func TestReleasedV061IgnoresRelicsOnOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the v0.6.1 release; skipped in -short mode")
	}
	t.Parallel()
	binary := v0061Binary(t)

	writer, older := cliSyncRepositories(t)
	first := cliCreateTask(t, writer, "Before the relics")
	relicOne := mustCreateRelic(t, writer, "First relic", "--type", "spec")
	relicTwo := mustCreateRelic(t, writer, "Second relic", "--type", "spec")
	last := cliCreateTask(t, writer, "After the relics")
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}
	relicLines := func(clone string) string {
		t.Helper()
		return cliGitOutput(t, clone, "ls-remote", "origin", "refs/workbook/relics/*")
	}
	originRelics := relicLines(writer)
	if strings.Count(originRelics, "refs/workbook/relics/") != 2 {
		t.Fatalf("origin's relic refs = %q, want both relics", originRelics)
	}
	relicList := func(clone string) string {
		t.Helper()
		code, stdout, stderr := run(t, clone, "relic", "list", "--json")
		if code != 0 {
			t.Fatalf("relic list code = %d; stderr = %q", code, stderr)
		}
		return stdout
	}
	listBefore := relicList(writer)

	if code, stdout, stderr := runBinary(t, binary, older, "setup"); code != 0 {
		t.Fatalf("v0.6.1 setup code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	code, stdout, stderr := runBinary(t, binary, older, "list", "--json")
	if code != 0 {
		t.Fatalf("v0.6.1 list code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	var listed struct {
		Data []core.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("decode v0.6.1 list: %v; stdout = %q", err, stdout)
	}
	listedIDs := map[string]bool{}
	for _, task := range listed.Data {
		listedIDs[task.ID] = true
	}
	if len(listed.Data) != 2 || !listedIDs[first.ID] || !listedIDs[last.ID] {
		t.Fatalf("v0.6.1 list holds %d tasks %v, want exactly %s and %s", len(listed.Data), listedIDs, first.ID, last.ID)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "show", last.ID); code != 0 {
		t.Fatalf("v0.6.1 show code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "update", last.ID, "--title", "Renamed"); code != 0 {
		t.Fatalf("v0.6.1 update code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("v0.6.1 sync code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}

	if refs := cliGitOutput(t, older, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "relics") {
		t.Fatalf("the v0.6.1 clone holds a relic ref:\n%s", refs)
	}
	if got := relicLines(older); got != originRelics {
		t.Fatalf("origin's relic refs changed:\nbefore: %s\nafter:  %s", originRelics, got)
	}

	// This build reads the rename and every relic unchanged.
	if code, _, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, writer, "show", last.ID, "--json")
	if code != 0 {
		t.Fatalf("writer show code = %d; stderr = %q", code, stderr)
	}
	var shown struct {
		Data core.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("decode show: %v; stdout = %q", err, stdout)
	}
	if shown.Data.Title != "Renamed" {
		t.Fatalf("this build shows the task as %q, want Renamed", shown.Data.Title)
	}
	if got := relicList(writer); got != listBefore {
		t.Fatalf("relic list changed:\nbefore: %s\nafter:  %s", listBefore, got)
	}
	for _, id := range []string{relicOne.Relic.ID, relicTwo.Relic.ID} {
		if !strings.Contains(originRelics, id) {
			t.Fatalf("origin's relic refs %q do not name %s", originRelics, id)
		}
	}
}
