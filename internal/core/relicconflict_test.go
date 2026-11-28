package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRelicBodyConflictDetailSaysWhyItWasNotMerged(t *testing.T) {
	conflict, texts := relicBodyConflictForTest()
	conflict.Body.Reason = RelicMergeOverlap
	conflict.Body.Overlaps = []LineMergeOverlap{{BaseStart: 4, BaseLines: 2}, {BaseStart: 12, BaseLines: 1}, {BaseStart: 20, BaseLines: 0}}
	detail := RelicConflictDetail(conflict)
	for _, want := range []string{
		"the edits overlap at base lines 4-5, 12, after 20",
		"git cat-file blob " + conflict.Body.Base.Object + " > base.md",
		"git cat-file blob " + conflict.ParkedRef + ":parts/" + relicDocPartA + "/body > mine.md",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not hold %q:\n%s", want, detail)
		}
	}
	for _, text := range texts {
		if strings.Contains(detail, strings.TrimSpace(text)) {
			t.Errorf("the detail holds body text %q", text)
		}
	}
	for reason, words := range map[RelicMergeReason]string{
		RelicMergeTooManyChanges: "too many lines",
		RelicMergeTooLarge:       "byte limit",
		RelicMergeNotText:        "not UTF-8",
		RelicMergeFormat:         "format labels",
		RelicMergeNoBase:         "no body",
		RelicMergeBaseDropped:    "compaction",
		RelicMergeUnmergeable:    "do not follow",
	} {
		conflict.Body.Reason, conflict.Body.Overlaps = reason, nil
		detail := RelicConflictDetail(conflict)
		if !strings.Contains(detail, "; it cannot be merged: ") || !strings.Contains(detail, words) || strings.ContainsAny(detail, "\r\n") {
			t.Errorf("%s: the detail does not say why in one line:\n%s", reason, detail)
		}
	}
}

func TestRelicBodyConflictJSONCarriesTheReason(t *testing.T) {
	conflict, _ := relicBodyConflictForTest()
	conflict.Body.Reason = RelicMergeOverlap
	conflict.Body.Overlaps = []LineMergeOverlap{{BaseStart: 3, BaseLines: 2}}
	encoded, err := json.Marshal(conflict.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"reason":"overlap","overlaps":[{"baseStart":3,"baseLines":2}]`) {
		t.Fatalf("body conflict JSON = %s, want the reason and overlaps", encoded)
	}
	conflict.Body.Reason, conflict.Body.Overlaps = RelicMergeFormat, nil
	encoded, _ = json.Marshal(conflict.Body)
	if !strings.Contains(string(encoded), `"reason":"format"`) || strings.Contains(string(encoded), "overlaps") {
		t.Fatalf("body conflict JSON = %s, want the reason and no overlaps", encoded)
	}
}

func TestRelicBodyMergedWarning(t *testing.T) {
	merge := RelicOperation{ID: relicTestID(450), Type: RelicOperationPartBodyMerge, Part: relicDocPartA,
		Body:  &RelicBodyChange{Format: "md", Bytes: 3, Base: relicTestID(451)},
		Merge: &RelicBodyMerge{Ancestor: relicTestID(452), Other: relicTestID(453), Method: RelicLineMergeMethod}}
	warning := RelicBodyMergedWarning(relicDocRelic, "", merge)
	want := "relic " + relicDocRelic + ": part " + relicDocPartA + ": the body was saved here from version " + relicTestID(452) +
		" and on origin as version " + relicTestID(451) + "; the two edits changed different lines and were merged as version " +
		relicTestID(450) + "; `workbook relic diff " + relicDocRelic + "#" + relicDocPartA + "` shows the merge"
	if warning.Code != WarningRelicBodyMerged || warning.Message != want {
		t.Fatalf("warning = %+v, want %s: %s", warning, WarningRelicBodyMerged, want)
	}
}
