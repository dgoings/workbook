package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// FillRelicConflictBodies reads each body a conflict names, refuses one over
// the body limit from its size alone, and still returns every body it could
// read, leaving the caller's conflicts as they were.
func TestFillRelicConflictBodiesRefusesAnOversizeBody(t *testing.T) {
	t.Parallel()
	repo, _, _ := syncRepositoriesWithObjectFormat(t, "sha1")
	blob := func(content string) string {
		t.Helper()
		return syncGitInput(t, repo.Root, []byte(content), "hash-object", "-w", "--stdin")
	}
	base, ours := blob("base\n"), blob("\xff\xfe")
	oversize := blob(strings.Repeat("x", core.MaxRelicBodyBytes+1))
	missing := strings.Repeat("0", len(base))
	conflicts := []core.RelicConflict{
		{RelicID: "WB-R-A", Type: core.RelicConflictBody, Part: "P", Body: &core.RelicBodyConflict{
			Base:   &core.RelicBodyVersion{Object: base},
			Ours:   core.RelicBodyVersion{Object: ours},
			Theirs: core.RelicBodyVersion{Object: oversize},
		}},
		{RelicID: "WB-R-B", Type: core.RelicConflictBody, Part: "Q", Body: &core.RelicBodyConflict{
			Ours:   core.RelicBodyVersion{Object: missing},
			Theirs: core.RelicBodyVersion{Object: base},
		}},
		{RelicID: "WB-R-C", Type: core.RelicConflictTombstone},
	}

	filled, err := repo.FillRelicConflictBodies(context.Background(), conflicts)
	if core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("error = %v, want corrupt data", err)
	}
	message := err.Error()
	for _, want := range []string{
		"relic WB-R-A: part P: the theirs body is 1048577 bytes, over the 1048576 byte limit for a body",
		"relic WB-R-B: part Q: the ours body " + missing + " is missing",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error = %q, want %q", message, want)
		}
	}
	if strings.Contains(message, "xxxx") {
		t.Errorf("error echoes the oversize body: %.200s", message)
	}
	first := filled[0].Body
	if first.Base.Text == nil || *first.Base.Text != "base\n" || first.Ours.Base64 == nil || *first.Ours.Base64 != "//4=" ||
		first.Theirs.Text != nil || first.Theirs.Base64 != nil {
		t.Fatalf("filled = %+v / %+v / %+v, want base text, ours base64 and theirs empty", *first.Base, first.Ours, first.Theirs)
	}
	if second := filled[1].Body; second.Theirs.Text == nil || second.Ours.Text != nil {
		t.Fatalf("filled = %+v, want only theirs read", second)
	}
	if conflicts[0].Body.Base.Text != nil || conflicts[1].Body.Theirs.Text != nil {
		t.Fatal("FillRelicConflictBodies changed the conflicts it was given")
	}
}
