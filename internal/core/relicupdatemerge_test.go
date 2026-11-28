package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// The texts of a part's body the merge tests save: v1, then v2 from v1 with
// line 2 changed. A save from v1 changing line 5 merges onto v2; one changing
// line 2 again overlaps it.
const (
	mergeV1Text      = "one\ntwo\nthree\nfour\nfive\nsix\n"
	mergeV2Text      = "one\nTWO\nthree\nfour\nfive\nsix\n"
	mergeLine5Text   = "one\ntwo\nthree\nfour\nFIVE\nsix\n"
	mergeMergedText  = "one\nTWO\nthree\nfour\nFIVE\nsix\n"
	mergeOverlapText = "one\nZWEI\nthree\nfour\nfive\nsix\n"
)

// relicWithTwoVersions creates relicCreatedForUpdate's relic in a store that
// keeps its history, and saves the goals part's body twice: v1, then v2 from
// v1. The service reads history from the store.
func relicWithTwoVersions(t *testing.T) (store *memoryRelicStore, service RelicService, relicID, goals, v1, v2 string) {
	t.Helper()
	store, service, relicID, goals, _, _ = relicCreatedForUpdate(t)
	service.HistoryReader = store
	v1 = saveGoalsBody(t, service, relicID, goals, goals, mergeV1Text)
	v2 = saveGoalsBody(t, service, relicID, goals, v1, mergeV2Text)
	return store, service, relicID, goals, v1, v2
}

func saveGoalsBody(t *testing.T, service RelicService, relicID, part, base, text string) string {
	t.Helper()
	result, err := service.Update(context.Background(), relicID, []RelicEdit{{
		Kind: RelicEditPart, Part: part, Base: base, Body: &RelicBodyInput{Format: "md", Content: []byte(text)},
	}})
	if err != nil {
		t.Fatalf("save from %s: %v", base, err)
	}
	return result.Parts[0].Body.Version
}

func saveBodyFrom(service RelicService, relicID, part, base, text string, more ...RelicEdit) (RelicMutationResult, error) {
	edits := append(append([]RelicEdit(nil), more...), RelicEdit{
		Kind: RelicEditPart, Part: part, Base: base, Body: &RelicBodyInput{Content: []byte(text)}, BodyFile: "goals.md",
	})
	return service.Update(context.Background(), relicID, edits)
}

func TestUpdateFromAnOlderVersionMerges(t *testing.T) {
	store, service, relicID, goals, v1, v2 := relicWithTwoVersions(t)
	ctx := context.Background()

	result, err := saveBodyFrom(service, relicID, goals, v1, mergeLine5Text)
	write := requireRelicWritten(t, "save from v1", store, 4, result, err)

	pack := write.pack
	if pack.MinReader != RelicMergeMinReader {
		t.Errorf("pack minReader = %d, want %d", pack.MinReader, RelicMergeMinReader)
	}
	if got := relicOperationTypes(pack); !reflect.DeepEqual(got, []RelicOperationType{RelicOperationPartBodySet, RelicOperationPartBodyMerge}) {
		t.Fatalf("operations = %v, want part.body.set then part.body.merge", got)
	}
	save, merge := pack.Operations[0], pack.Operations[1]
	if save.Part != goals || *save.Body != (RelicBodyChange{Format: "md", Bytes: int64(len(mergeLine5Text)), Base: v1}) {
		t.Errorf("part.body.set = %+v, body %+v; want the file's bytes saved from %s", save, save.Body, v1)
	}
	wantID := RelicMergeOperationID(relicID, goals, save.ID, v2, RelicLineMergeMethod, pack.WallTime)
	if merge.ID != wantID || merge.Part != goals ||
		*merge.Body != (RelicBodyChange{Format: "md", Bytes: int64(len(mergeMergedText)), Base: v2}) ||
		*merge.Merge != (RelicBodyMerge{Ancestor: v1, Other: save.ID, Method: RelicLineMergeMethod}) {
		t.Errorf("part.body.merge = %+v, body %+v, merge %+v; want %s onto %s", merge, merge.Body, merge.Merge, wantID, v2)
	}
	content := write.content[goals]
	if !content.HasBody || string(content.Body) != mergeMergedText || string(content.MergeInput) != mergeLine5Text {
		t.Errorf("content = body %q, merge input %q; want the merged text and the file's bytes", content.Body, content.MergeInput)
	}
	part := requirePartForTest(t, write.record.State.Relic, goals)
	if part.Body == nil || part.Body.Version != merge.ID {
		t.Errorf("part body = %+v, want version %s", part.Body, merge.ID)
	}
	if len(result.Parts) != 1 || result.Parts[0].Body == nil || result.Parts[0].Body.Version != merge.ID {
		t.Errorf("result parts = %+v, want the part at the merge", result.Parts)
	}
	wantWarning := Warning{Code: WarningRelicBodyMerged, Message: "relic " + relicID + ": part " + goals +
		": this save started from version " + v1 + ", and the body has since been saved as version " + v2 +
		"; the two edits changed different lines and were merged as version " + merge.ID +
		"; `workbook relic diff " + relicID + "#" + goals + "` shows the merge"}
	if !reflect.DeepEqual(result.Warnings, []Warning{wantWarning}) {
		t.Errorf("warnings = %+v, want %+v", result.Warnings, wantWarning)
	}

	shown, err := service.Show(ctx, relicID+"#"+goals, RelicShowOptions{Bodies: true})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if body := shown.Parts[0].Body; body.Text == nil || *body.Text != mergeMergedText {
		t.Errorf("shown body = %+v, want the merged text", body)
	}
	history, err := service.History(ctx, relicID+"#"+goals, RelicHistoryOptions{All: true})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	last := history.Changes[len(history.Changes)-1]
	if len(last.Operations) != 2 || last.Operations[0].Applied == nil || *last.Operations[0].Applied ||
		last.Operations[1].Applied == nil || !*last.Operations[1].Applied {
		t.Errorf("history's last change = %+v, want the save not applied and the merge applied", last.Operations)
	}
	if current := history.BodyVersions[len(history.BodyVersions)-1]; current.Version != merge.ID || !current.Current {
		t.Errorf("current body version = %+v, want the merge", current)
	}

	// A version may be given in any letter case, as the current one may.
	result, err = saveBodyFrom(service, relicID, goals, lowerASCII(v2), "ONE\nTWO\nthree\nfour\nfive\nsix\n")
	write = requireRelicWritten(t, "save from v2 in lower case", store, 5, result, err)
	if got := relicOperationTypes(write.pack); !reflect.DeepEqual(got, []RelicOperationType{RelicOperationPartBodySet, RelicOperationPartBodyMerge}) {
		t.Fatalf("operations from v2 = %v, want a merge onto the first merge", got)
	}
	if got := string(write.content[goals].Body); got != "ONE\nTWO\nthree\nfour\nFIVE\nsix\n" {
		t.Errorf("body after the second merge = %q", got)
	}
}

func lowerASCII(text string) string {
	lowered := []byte(text)
	for index, b := range lowered {
		if b >= 'A' && b <= 'Z' {
			lowered[index] = b + ('a' - 'A')
		}
	}
	return string(lowered)
}

// A merge whose result is the text that stands records nothing: the save
// already holds every edit the file makes.
func TestUpdateFromAnOlderVersionThatAddsNothingWritesNothing(t *testing.T) {
	store, service, relicID, goals, v1, _ := relicWithTwoVersions(t)
	result, err := saveBodyFrom(service, relicID, goals, v1, mergeV2Text)
	if err != nil || result.Changed || len(result.Warnings) != 0 {
		t.Fatalf("save of v2's text from v1 = %+v, %v; want no change", result, err)
	}
	requireRelicWrites(t, "save of v2's text from v1", store, 3)
}

func TestUpdateFromAnOlderVersionOverlappingConflicts(t *testing.T) {
	store, service, relicID, goals, v1, v2 := relicWithTwoVersions(t)
	head := store.snapshots[relicID].Head

	_, err := saveBodyFrom(service, relicID, goals, v1, mergeOverlapText,
		RelicEdit{Kind: RelicEditTitle, Title: "Never recorded"})
	requireRelicCategory(t, "overlapping save", err, CategoryConflict)
	requireRelicWrites(t, "overlapping save", store, 3)
	if store.snapshots[relicID].Head != head {
		t.Fatalf("an overlapping save moved the relic")
	}
	var failure RelicConflictFailure
	if !errors.As(err, &failure) || len(failure.Conflicts) != 1 {
		t.Fatalf("error = %#v, want a RelicConflictFailure with one conflict", err)
	}
	conflict := failure.Conflicts[0]
	if conflict.RelicID != relicID || conflict.Type != RelicConflictBody || conflict.Part != goals ||
		conflict.ParkedRef != "" || conflict.Unreplayed != 0 || conflict.Remote != "" || conflict.Body == nil {
		t.Fatalf("conflict = %+v, want a body conflict of %s#%s and nothing parked", conflict, relicID, goals)
	}
	body := conflict.Body
	if body.Reason != RelicMergeOverlap || !reflect.DeepEqual(body.Overlaps, []LineMergeOverlap{{BaseStart: 2, BaseLines: 1}}) {
		t.Errorf("reason %q, overlaps %+v; want overlap at line 2", body.Reason, body.Overlaps)
	}
	requireConflictBody(t, "base", body.Base, v1, mergeV1Text, true)
	requireConflictBody(t, "ours", &body.Ours, "", mergeOverlapText, false)
	requireConflictBody(t, "theirs", &body.Theirs, v2, mergeV2Text, true)

	want := "relic " + relicID + ": part " + goals + ": this save started from version " + v1 +
		", and the body has since been saved as version " + v2 + "; the edits overlap at base lines 2" +
		"; nothing was saved; the file given still holds this save's text; run `workbook relic show " +
		relicID + "#" + goals + " --body > current.md` to write the text that stands, make the edits again in current.md, " +
		"then run `workbook relic update " + relicID + " --part " + goals + " --body-file current.md --base " + v2 + "`"
	if err.Error() != want || RelicConflictDetail(conflict) != want {
		t.Errorf("message = %q\n  detail = %q\n    want %q", err, RelicConflictDetail(conflict), want)
	}
}

func requireConflictBody(t *testing.T, label string, got *RelicBodyVersion, version, text string, stored bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: no body", label)
	}
	if got.Version != version || got.Format != "md" || got.Bytes != int64(len(text)) ||
		got.Text == nil || *got.Text != text || got.Base64 != nil || (got.Object != "") != stored {
		t.Errorf("%s = %+v (text %v), want version %q, %q, stored %v", label, got, got.Text, version, text, stored)
	}
}

func TestUpdateFromAnUnknownVersionIsStaleWrite(t *testing.T) {
	store, service, relicID, goals, _, v2 := relicWithTwoVersions(t)
	_, _, _, _, decision, note := relicCreatedForUpdate(t)
	for _, base := range []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", // a version no part ever had
		note,                         // another part's version
		decision,                     // an ID that is no body version
	} {
		_, err := saveBodyFrom(service, relicID, goals, base, mergeLine5Text)
		requireRelicCategory(t, "base "+base, err, CategoryStaleWrite)
		if want := "part " + goals + " body is at version " + v2 + ", not " + base +
			`; read it again with "workbook relic show ` + relicID + "#" + goals + ` --body" and save from that version`; err.Error() != want {
			t.Errorf("base %s: message = %q, want %q", base, err, want)
		}
	}
	_, err := saveBodyFrom(service, relicID, goals, "not a version", mergeLine5Text)
	requireRelicCategory(t, "a base that is no ID", err, CategoryStaleWrite, `not "not a version"`)
	requireRelicWrites(t, "unknown bases", store, 3)
}

func TestUpdateWithoutBaseStillRefused(t *testing.T) {
	store, service, relicID, goals, _, v2 := relicWithTwoVersions(t)
	_, err := saveBodyFrom(service, relicID, goals, "", mergeLine5Text)
	requireRelicCategory(t, "no base", err, CategoryStaleWrite)
	if want := "part " + goals + " body is at version " + v2 + "; pass --base " + v2 + " to save over it"; err.Error() != want {
		t.Errorf("message = %q, want %q", err, want)
	}
	requireRelicWrites(t, "no base", store, 3)
}

// The merged body is judged against the schema the relic names, as any saved
// body is: here the schema came to allow only txt bodies for a section after
// the relic's md body was saved, so the merge, which keeps md, does not fit.
func TestMergedUpdateIsJudgedAgainstTheSchema(t *testing.T) {
	store, service, relicID, goals, v1, _ := relicWithTwoVersions(t)
	schemas := newMemorySchemaStore()
	version := schemas.put(t, "paper", `{"parts":{"section":{"bodyFormats":["txt"]},"decision":{},"note":{}}}`)
	snapshot := store.snapshots[relicID]
	snapshot.Record.State.Relic.Schema = version.ID
	store.snapshots[relicID] = snapshot
	service.Schemas = schemas

	_, err := saveBodyFrom(service, relicID, goals, v1, mergeLine5Text)
	requireRelicCategory(t, "merged save", err, CategoryValidation,
		"relic does not fit schema paper version 1: part "+goals+" (section) body", "(bodyFormats)")
	requireRelicWrites(t, "merged save", store, 3)
}

// With no history reader, a save from an older version is refused as it was
// before merges, and nothing is read.
func TestUpdateFromAnOlderVersionWithoutHistoryIsStaleWrite(t *testing.T) {
	store, service, relicID, goals, v1, v2 := relicWithTwoVersions(t)
	service.HistoryReader = nil
	_, err := saveBodyFrom(service, relicID, goals, v1, mergeLine5Text)
	requireRelicCategory(t, "no history", err, CategoryStaleWrite, "body is at version "+v2+", not "+v1)
	requireRelicWrites(t, "no history", store, 3)
}
