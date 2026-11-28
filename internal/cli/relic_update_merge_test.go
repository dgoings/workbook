package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// The texts the merge tests save as a part's body: v1, then v2 from v1 with
// line 2 changed. A save from v1 that changes line 5 merges onto v2; one that
// changes line 2 again overlaps it.
const (
	updateMergeV1      = "one\ntwo\nthree\nfour\nfive\nsix\n"
	updateMergeV2      = "one\nTWO\nthree\nfour\nfive\nsix\n"
	updateMergeLine5   = "one\ntwo\nthree\nfour\nFIVE\nsix\n"
	updateMergeMerged  = "one\nTWO\nthree\nfour\nFIVE\nsix\n"
	updateMergeOverlap = "one\nZWEI\nthree\nfour\nfive\nsix\n"
)

// relicPartAtV2 creates a relic whose one section part has body v1, then v2
// saved from v1, and returns the relic, the part and both versions.
func relicPartAtV2(t *testing.T, repository string) (id, part, v1, v2 string) {
	t.Helper()
	id = mustCreateRelic(t, repository, "Merge plan", "--type", "paper").Relic.ID
	part = mustUpdateRelic(t, repository, id, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "v1.md", updateMergeV1)).Parts[0].ID
	v1 = part
	v2 = mustUpdateRelic(t, repository, id, "--part", part, "--body-file", writeRelicBodyFile(t, "v2.md", updateMergeV2),
		"--base", v1).Parts[0].Body.Version
	return id, part, v1, v2
}

func TestRelicUpdateFromAnOlderVersionMerges(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			id, part, v1, v2 := relicPartAtV2(t, repository)
			file := writeRelicBodyFile(t, "mine.md", updateMergeLine5)

			code, stdout, stderr := run(t, repository, "relic", "update", id, "--part", part, "--body-file", file,
				"--base", v1, "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("relic update from v1 = (%d, %q, %q), want exit 0", code, stdout, stderr)
			}
			envelope := decodeRelicSyncEnvelope(t, stdout, "relic update")
			var updated core.RelicMutationResult
			if err := json.Unmarshal(envelope.Data, &updated); err != nil {
				t.Fatal(err)
			}
			merge := updated.Parts[0].Body.Version
			if len(envelope.Warnings) != 1 || envelope.Warnings[0].Code != core.WarningRelicBodyMerged ||
				envelope.Warnings[0].Message != "relic "+id+": part "+part+": this save started from version "+v1+
					", and the body has since been saved as version "+v2+
					"; the two edits changed different lines and were merged as version "+merge+
					"; `workbook relic diff "+id+"#"+part+"` shows the merge" {
				t.Errorf("warnings = %+v, want relic-body-merged for %s", envelope.Warnings, merge)
			}
			if text := relicBodyText(t, repository, id, part); text != updateMergeMerged {
				t.Fatalf("body = %q, want the merged text", text)
			}
			head := relicHeadOf(t, repository, id)
			if input := cliGitOutput(t, repository, "cat-file", "blob", head+":merges/"+merge); input+"\n" != updateMergeLine5 {
				t.Errorf("merges/%s = %q, want the file's bytes", merge, input)
			}

			var history core.RelicHistory
			mustRelic(t, repository, &history, "show", id+"#"+part, "--history")
			last := history.Changes[len(history.Changes)-1]
			if last.Summary != "part.body.set, part.body.merge" || len(last.Operations) != 2 ||
				*last.Operations[0].Applied || !*last.Operations[1].Applied || last.Operations[1].ID != merge {
				t.Errorf("last change = %+v, want the save not applied and the merge applied", last)
			}
			assertRelicHistoriesValid(t, repository, 1)

			// Text output says the same on standard error.
			again := writeRelicBodyFile(t, "again.md", "ONE\nTWO\nthree\nfour\nfive\nsix\n")
			code, stdout, stderr = run(t, repository, "relic", "update", id, "--part", part, "--body-file", again, "--base", v2)
			if code != 0 || !strings.HasPrefix(stderr, "workbook: warning: relic "+id+": part "+part+
				": this save started from version "+v2+", and the body has since been saved as version "+merge) {
				t.Fatalf("relic update from v2 = (%d, %q, %q), want exit 0 and the merge warning", code, stdout, stderr)
			}
			if text := relicBodyText(t, repository, id, part); text != "ONE\nTWO\nthree\nfour\nFIVE\nsix\n" {
				t.Fatalf("body after the second merge = %q", text)
			}
		})
	}
}

func TestRelicUpdateFromAnOlderVersionOverlappingConflicts(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	id, part, v1, v2 := relicPartAtV2(t, repository)
	head := relicHeadOf(t, repository, id)
	file := writeRelicBodyFile(t, "mine.md", updateMergeOverlap)

	code, stdout, stderr := run(t, repository, "relic", "update", id, "--title", "Never recorded",
		"--part", part, "--body-file", file, "--base", v1, "--json")
	if code != 8 {
		t.Fatalf("relic update from v1 = (%d, %q, %q), want exit 8", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryConflict, "")
	envelope := decodeRelicSyncEnvelope(t, stdout, "relic update")
	if len(envelope.RelicConflict) != 1 {
		t.Fatalf("relicConflict = %+v, want one", envelope.RelicConflict)
	}
	conflict := envelope.RelicConflict[0]
	body := conflict.Body
	if conflict.RelicID != id || conflict.Type != core.RelicConflictBody || conflict.Part != part ||
		conflict.ParkedRef != "" || body == nil || body.Reason != core.RelicMergeOverlap ||
		len(body.Overlaps) != 1 || body.Overlaps[0] != (core.LineMergeOverlap{BaseStart: 2, BaseLines: 1}) {
		t.Fatalf("conflict = %+v, body %+v; want an overlap at line 2 and nothing parked", conflict, body)
	}
	if body.Base == nil || body.Base.Version != v1 || body.Base.Text == nil || *body.Base.Text != updateMergeV1 ||
		cliGitOutput(t, repository, "cat-file", "blob", body.Base.Object)+"\n" != updateMergeV1 {
		t.Errorf("base = %+v, want v1 and its text", body.Base)
	}
	if body.Ours.Version != "" || body.Ours.Object != "" || body.Ours.Text == nil || *body.Ours.Text != updateMergeOverlap {
		t.Errorf("ours = %+v, want the file, never saved", body.Ours)
	}
	if body.Theirs.Version != v2 || body.Theirs.Text == nil || *body.Theirs.Text != updateMergeV2 {
		t.Errorf("theirs = %+v, want v2 and its text", body.Theirs)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("an overlapping save moved the relic from %s to %s", head, after)
	}
	if title := shownRelic(t, repository, id).Title; title != "Merge plan" {
		t.Fatalf("title = %q, want it unchanged", title)
	}

	// Text output lists the conflict as sync does, and fails with its detail.
	code, stdout, stderr = run(t, repository, "relic", "update", id, "--part", part, "--body-file", file, "--base", v1)
	if code != 8 || !strings.Contains(stdout, "Relic conflict:\t"+id+"\tbody\trelic "+id+": part "+part+": this save started from version "+v1) ||
		!strings.Contains(stderr, "the edits overlap at base lines 2; nothing was saved") {
		t.Fatalf("relic update text = (%d, %q, %q), want the conflict listed", code, stdout, stderr)
	}
	if after := relicHeadOf(t, repository, id); after != head {
		t.Fatalf("an overlapping save moved the relic")
	}
}

// A save from an older version reads the part's chain through the index: with
// the index up to date, it walks no history in Git.
func TestRelicUpdateFromAnOlderVersionReadsTheChainFromTheIndex(t *testing.T) {
	repository := initializedRepository(t)
	id, part, v1, _ := relicPartAtV2(t, repository)
	// Bring the index up to the relic refs.
	mustRelic(t, repository, nil, "show", id+"#"+part, "--history")

	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	cursor := &gitTraceCursor{path: trace}
	file := writeRelicBodyFile(t, "mine.md", updateMergeLine5)
	code, stdout, stderr := run(t, repository, "relic", "update", id, "--part", part, "--body-file", file,
		"--base", v1, "--no-sync", "--json")
	cost := cursor.next(t)
	os.Unsetenv("GIT_TRACE2_EVENT")
	if code != 0 {
		t.Fatalf("relic update from v1 = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	if cost.processes == 0 {
		t.Fatal("the trace recorded no Git process")
	}
	for _, line := range cost.lines {
		if strings.Contains(line, " rev-list") {
			t.Errorf("a merging update with a fresh index ran %q", line)
		}
	}
	if text := relicBodyText(t, repository, id, part); text != updateMergeMerged {
		t.Fatalf("body = %q, want the merged text", text)
	}
}
