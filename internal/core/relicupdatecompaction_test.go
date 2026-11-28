package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A save from a version a compaction dropped is a conflict with reason
// base-dropped: the text it started from is gone, so it cannot be merged. A
// save from the version the checkpoint kept, which still stands at the
// checkpoint, merges like any other.
func TestUpdateFromACompactedVersionIsBaseDropped(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	ctx := context.Background()
	relicID, bodied, _ := f.create()
	second := f.save(relicID, bodied, bodied, "one\ntwo\nthree\nfour\n")
	kept := f.save(relicID, bodied, second, "one\nTWO\nthree\nfour\n")
	f.retire(relicID)
	f.compact(relicID, core.RelicBoundaryRetired)
	fourth := f.save(relicID, bodied, kept, "one\nTWO\nthree\nFOUR\n")
	before, err := f.repository.GetRelic(ctx, f.config, relicID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditPart, Part: bodied, Base: second,
		Body: &core.RelicBodyInput{Format: "md", Content: []byte("ONE\ntwo\nthree\nfour\n")}}})
	var failure core.RelicConflictFailure
	if core.CategoryOf(err) != core.CategoryConflict || !errors.As(err, &failure) || len(failure.Conflicts) != 1 {
		t.Fatalf("save from a dropped version: error = %v (%s), want one relic conflict", err, core.CategoryOf(err))
	}
	conflict := failure.Conflicts[0]
	body := conflict.Body
	if conflict.Type != core.RelicConflictBody || conflict.Part != bodied || body == nil ||
		body.Reason != core.RelicMergeBaseDropped || body.Base != nil || body.Overlaps != nil {
		t.Fatalf("conflict = %+v, body %+v; want base-dropped with no base", conflict, body)
	}
	if body.Ours.Version != "" || body.Ours.Text == nil || *body.Ours.Text != "ONE\ntwo\nthree\nfour\n" ||
		body.Theirs.Version != fourth || body.Theirs.Text == nil || *body.Theirs.Text != "one\nTWO\nthree\nFOUR\n" ||
		body.Theirs.Object == "" {
		t.Errorf("ours %+v, theirs %+v; want the file and the text that stands", body.Ours, body.Theirs)
	}
	want := "relic " + relicID + ": part " + bodied + ": this save started from a version that was dropped when the relic was compacted" +
		", so it cannot be merged onto version " + fourth + "; nothing was saved; the file given still holds this save's text; run `workbook relic show " +
		relicID + "#" + bodied + " --body > current.md` to write the text that stands, make the edits again in current.md, " +
		"then run `workbook relic update " + relicID + " --part " + bodied + " --body-file current.md --base " + fourth + "`"
	if err.Error() != want {
		t.Errorf("message = %q\n    want %q", err, want)
	}
	if after, err := f.repository.GetRelic(ctx, f.config, relicID); err != nil || after.Head != before.Head {
		t.Fatalf("the refused save moved the relic: %v", err)
	}

	result, err := f.service.Update(ctx, relicID, []core.RelicEdit{{Kind: core.RelicEditPart, Part: bodied, Base: kept,
		Body: &core.RelicBodyInput{Format: "md", Content: []byte("ONE\nTWO\nthree\nfour\n")}}})
	if err != nil {
		t.Fatalf("save from the kept version: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != core.WarningRelicBodyMerged {
		t.Errorf("warnings = %+v, want the merge", result.Warnings)
	}
	shown, err := f.service.Show(ctx, relicID+"#"+bodied, core.RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatal(err)
	}
	if text := shown.Parts[0].Body.Text; text == nil || *text != "ONE\nTWO\nthree\nFOUR\n" {
		t.Errorf("body after merging from the kept version = %v", text)
	}
}
