package cli

import (
	"strings"
	"testing"
)

// storedWorkbookDocuments returns every operation.json and state.json this
// repository holds, across every task ref and the configuration ledger.
//
// It walks the commits rather than the tips because the durable interface is
// every document a clone will ever fetch, not just the newest one.
func storedWorkbookDocuments(t *testing.T, repository string) map[string]string {
	t.Helper()
	refs := strings.Fields(gitOutput(t, repository, "for-each-ref", "--format=%(refname)",
		"refs/workbook/tasks/", "refs/workbook/config"))
	if len(refs) == 0 {
		t.Fatal("the repository holds no Workbook refs to inspect")
	}
	documents := make(map[string]string)
	for _, ref := range refs {
		for _, commit := range strings.Fields(gitOutput(t, repository, "rev-list", ref)) {
			for _, name := range []string{"operation.json", "state.json"} {
				documents[commit+":"+name] = gitOutput(t, repository, "show", commit+":"+name)
			}
		}
	}
	return documents
}

// No document this build writes carries the writer-format marker.
//
// The marker names the minimum reader generation a pack needs, and a pack that
// needs nothing beyond this generation says nothing: the member is absent, and
// absence means generation zero. That is what keeps the marker free. A task ref
// and a configuration ledger are append-only shared history, so a member that
// appeared in every document this build wrote would change the bytes every
// clone already holds — and there would be no way to take it back.
//
// This test exists so the claim is checked against real Git objects produced by
// real commands rather than against a struct tag. It covers every task
// mutation verb and every configuration verb, because the marker is per
// operation type and a table with one entry set wrong would show up here and
// nowhere else.
func TestNoDocumentThisBuildWritesCarriesAWriterFormatMarker(t *testing.T) {
	repository := initializedRepository(t)

	commands := [][]string{
		{"create", "First task", "--json"},
		{"create", "Second task", "--json"},
		{"status", "add", "awaiting-review", "--label", "Awaiting Review", "--json"},
		{"status", "rename", "ready", "todo", "--json"},
		{"status", "label", "todo", "Up Next", "--json"},
		{"status", "move", "todo", "--after", "in-progress", "--json"},
		{"status", "tag", "in-progress", "--tag", "next", "--json"},
		{"status", "untag", "in-progress", "next", "--json"},
		{"status", "delete", "awaiting-review", "--into", "in-review", "--json"},
	}
	for _, command := range commands {
		if code, _, stderr := run(t, repository, command...); code != 0 {
			t.Fatalf("%v code = %d, want 0; stderr = %q", command, code, stderr)
		}
	}

	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	var ids []string
	for _, id := range strings.Split(stdout, `"id":"`)[1:] {
		ids = append(ids, id[:strings.Index(id, `"`)])
	}
	if len(ids) != 2 {
		t.Fatalf("list returned %d task IDs, want 2; output = %q", len(ids), stdout)
	}
	for _, command := range [][]string{
		{"update", ids[0], "--title", "Renamed", "--description", "Prose", "--json"},
		{"update", ids[0], "--label", "storage", "--json"},
		{"move", ids[0], "--after", ids[1], "--json"},
		{"update", ids[0], "--status", "in-progress", "--json"},
		{"depend", ids[0], ids[1], "--json"},
		{"free", ids[0], ids[1], "--json"},
		{"delete", ids[1], "--json"},
		{"restore", ids[1], "--json"},
	} {
		if code, _, stderr := run(t, repository, command...); code != 0 {
			t.Fatalf("%v code = %d, want 0; stderr = %q", command, code, stderr)
		}
	}

	documents := storedWorkbookDocuments(t, repository)
	if len(documents) < 20 {
		t.Fatalf("inspected %d documents, want the whole history", len(documents))
	}
	for name, contents := range documents {
		if strings.Contains(contents, "minReader") {
			t.Fatalf("%s carries a writer-format marker: %s", name, contents)
		}
	}
}
