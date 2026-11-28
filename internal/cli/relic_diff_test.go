package cli

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// rebuiltBodies rebuilds both texts of a one-part diff shown with all context.
func rebuiltBodies(t *testing.T, diff core.RelicDiff) (from, to string) {
	t.Helper()
	if len(diff.Parts) != 1 || diff.Parts[0].Body == nil || !diff.Parts[0].Body.Text || len(diff.Parts[0].Body.Hunks) != 1 {
		t.Fatalf("diff parts = %+v, want one part with one text hunk", diff.Parts)
	}
	var before, after strings.Builder
	for _, line := range diff.Parts[0].Body.Hunks[0].Lines {
		text := line.Text
		if !line.NoNewline {
			text += "\n"
		}
		if line.Kind != core.DiffInsert {
			before.WriteString(text)
		}
		if line.Kind != core.DiffDelete {
			after.WriteString(text)
		}
	}
	return before.String(), after.String()
}

func relicDiffOf(t *testing.T, repository string, args ...string) core.RelicDiff {
	t.Helper()
	var diff core.RelicDiff
	mustRelic(t, repository, &diff, append([]string{"diff"}, args...)...)
	return diff
}

func TestRelicDiffAnyTwoVersionsOfAPart(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	texts := []string{
		"alpha\nbeta\ngamma\n",
		"alpha\nBETA\ngamma\ndelta",
		"",
		"windows\r\nlines\r\n",
		"alpha\nbeta\n\ngamma\n",
	}
	relicID, partID, versions := savedRelicPart(t, repository, texts...)
	for index, version := range versions {
		if index == len(versions)-1 {
			if got := showPartBody(t, repository, relicID, partID); got != texts[index] {
				t.Fatalf("body = %q, want %q", got, texts[index])
			}
		}
		for other, target := range versions {
			diff := relicDiffOf(t, repository, relicID+"#"+partID, version, target, "--context", "all")
			if diff.Relic != relicID || diff.Part != partID || diff.From == nil || diff.From.Version != version || diff.To.Version != target {
				t.Fatalf("diff %d %d = %+v", index, other, diff)
			}
			if index == other {
				if len(diff.Parts) != 0 {
					t.Errorf("diff of a version with itself lists %+v", diff.Parts)
				}
				continue
			}
			if from, to := rebuiltBodies(t, diff); from != texts[index] || to != texts[other] {
				t.Errorf("diff %d %d rebuilds %q → %q, want %q → %q", index, other, from, to, texts[index], texts[other])
			}
		}
	}
}

func TestRelicDiffDefaultsAndOneVersion(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	alone := mustCreateRelic(t, repository, "Alone", "--type", "paper").Relic.ID
	code, stdout, stderr := run(t, repository, "relic", "diff", alone, "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"from":null`) {
		t.Fatalf("diff of a one-commit relic = (%d, %q, %q), want from null", code, stdout, stderr)
	}
	diff := relicDiffOf(t, repository, alone)
	if diff.From != nil || len(diff.Changes) != 2 || diff.Changes[0].Name != "type" || diff.Changes[0].Kind != core.ChangeAdded ||
		diff.Changes[1].Name != "title" || diff.Changes[1].Kind != core.ChangeAdded {
		t.Fatalf("diff = %+v, want the type and the title added", diff)
	}

	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
	mustRelic(t, repository, nil, "update", relicID, "--title", "Retitled")
	diff = relicDiffOf(t, repository, relicID)
	if len(diff.Parts) != 0 || len(diff.Changes) != 1 || diff.Changes[0].Name != "title" || diff.From == nil || diff.From.Version != versions[1] {
		t.Fatalf("whole diff with no version = %+v, want the title change from the last save", diff)
	}
	diff = relicDiffOf(t, repository, relicID+"#"+partID, "--context", "all")
	if from, to := rebuiltBodies(t, diff); from != "one\n" || to != "two\n" || diff.From.Version != versions[0] || diff.To.Version != versions[1] {
		t.Fatalf("part diff with no version = %+v, want the last save", diff)
	}
	diff = relicDiffOf(t, repository, relicID+"#"+partID, strings.ToLower(versions[0]), "--context", "all")
	if from, to := rebuiltBodies(t, diff); from != "one\n" || to != "two\n" || diff.To.Version == versions[1] {
		t.Fatalf("part diff with one version = %+v, want it against the head", diff)
	}
}

func TestRelicDiffWholeRelicJSON(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Build it")
	relicID := mustCreateRelic(t, repository, "Whole", "--type", "paper").Relic.ID
	var parts []string
	for _, name := range []string{"a", "b", "c"} {
		parts = append(parts, mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
			"--field", "heading="+name).Parts[0].ID)
	}
	updated := mustUpdateRelic(t, repository, relicID,
		"--add-part", "--part-type", "decision", "--body-file", writeRelicBodyFile(t, "d.md", "decided\n"),
		"--remove-part", parts[1],
		"--move-part", parts[2], "--before", parts[0],
		"--set", "owner=board",
		"--link", task.ID, "--rel", "implemented-by")
	added := updated.Parts[0].ID

	diff := relicDiffOf(t, repository, relicID)
	byID := map[string]core.RelicPartDiff{}
	for _, part := range diff.Parts {
		byID[part.ID] = part
	}
	if len(diff.Parts) != 3 || byID[added].Kind != core.ChangeAdded || byID[added].Body == nil || byID[added].Body.From != nil ||
		byID[parts[1]].Kind != core.ChangeRemoved || byID[parts[1]].Moved != nil ||
		byID[parts[2]].Kind != core.RelicPartChanged || byID[parts[2]].Moved == nil ||
		byID[parts[2]].Moved.FromIndex != 2 || byID[parts[2]].Moved.ToIndex != 0 {
		t.Fatalf("parts = %+v", diff.Parts)
	}
	if len(diff.Properties) != 1 || diff.Properties[0].Name != "owner" || diff.Properties[0].Kind != core.ChangeAdded ||
		string(diff.Properties[0].To) != `"board"` {
		t.Fatalf("properties = %+v", diff.Properties)
	}
	if diff.Links == nil || len(diff.Links.Added) != 1 || len(diff.Links.Removed) != 0 ||
		diff.Links.Added[0].To.Task != task.ID || diff.Links.Added[0].Rel != "implemented-by" {
		t.Fatalf("links = %+v", diff.Links)
	}
}

func TestRelicDiffTextOfAWholeRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Build it")
	relicID := mustCreateRelic(t, repository, "Before", "--type", "paper").Relic.ID
	var parts []string
	for _, name := range []string{"a", "b"} {
		parts = append(parts, mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
			"--field", "heading="+name).Parts[0].ID)
	}
	mustRelic(t, repository, nil, "update", relicID, "--set", "old=1", "--set", "kept=x",
		"--link", task.ID, "--rel", "relates-to")
	linked := shownRelic(t, repository, relicID).Links[0].ID
	start := relicHistoryOf(t, repository, relicID, "--limit", "1").Changes[0].Operations[0].ID

	mustRelic(t, repository, nil, "update", relicID, "--title", "After", "--clear", "old", "--set", "kept=y", "--set", "new=z",
		"--move-part", parts[1], "--before", parts[0], "--part", parts[0], "--field", "heading=A", "--field", "extra=1",
		"--unlink", linked, "--link", task.ID, "--rel", "implemented-by")
	mustRelic(t, repository, nil, "delete", relicID)
	added := shownRelic(t, repository, relicID).Links[0].ID
	diff := relicDiffOf(t, repository, relicID, start)

	code, stdout, stderr := run(t, repository, "relic", "diff", relicID, start)
	if code != 0 || stderr != "" {
		t.Fatalf("diff = (%d, %q, %q)", code, stdout, stderr)
	}
	want := "Comparing:\t" + start + "\t→\t" + diff.To.Version + "\n" +
		"Title:\tBefore → After\n" +
		"Deleted:\tfalse → true\n" +
		"Property kept:\tx → y\n" +
		"Property new:\t+z\n" +
		"Property old:\t-1\n" +
		"Part " + parts[1] + "\tsection\tchanged\tmoved 1 → 0\n" +
		"Part " + parts[0] + "\tsection\tchanged\n" +
		"\tField extra:\t+1\n" +
		"\tField heading:\ta → A\n" +
		"Links:\n" +
		"\t+" + added + "\trelic\timplemented-by\t" + task.ID + "\n" +
		"\t-" + linked + "\trelic\trelates-to\t" + task.ID + "\n"
	if stdout != want {
		t.Fatalf("text =\n%q\nwant\n%q", stdout, want)
	}

	code, stdout, stderr = run(t, repository, "relic", "diff", relicID, start, start)
	if want := "Comparing:\t" + start + "\t→\t" + start + "\nThese two versions hold the same relic.\n"; code != 0 || stderr != "" || stdout != want {
		t.Fatalf("diff of a version with itself = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
}

func TestRelicDiffLeavesOutAPartAddedAndRemovedInside(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := mustCreateRelic(t, repository, "Brief", "--type", "paper").Relic.ID
	created := relicHistoryOf(t, repository, relicID).Changes[0].Operations[0].ID
	partID := mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
		"--field", "heading=Q").Parts[0].ID
	mustRelic(t, repository, nil, "update", relicID, "--remove-part", partID)

	if diff := relicDiffOf(t, repository, relicID, created); len(diff.Parts) != 0 {
		t.Errorf("whole diff parts = %+v, want none", diff.Parts)
	}
	for address, noun := range map[string]string{relicID: "relic", relicID + "#" + partID: "part"} {
		diff := relicDiffOf(t, repository, address, created)
		code, stdout, stderr := run(t, repository, "relic", "diff", address, created)
		want := "Comparing:\t" + created + "\t→\t" + diff.To.Version + "\nThese two versions hold the same " + noun + ".\n"
		if code != 0 || stderr != "" || stdout != want {
			t.Errorf("diff %s = (%d, %q, %q), want %q", address, code, stdout, stderr, want)
		}
	}
}

// A part whose position string changed without changing its place among the
// live parts gets no moved note.
func TestRelicDiffTextNotesOnlyAChangedPlace(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := mustCreateRelic(t, repository, "Places", "--type", "paper").Relic.ID
	var parts []string
	for range 2 {
		parts = append(parts, mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section").Parts[0].ID)
	}
	start := relicHistoryOf(t, repository, relicID, "--limit", "1").Changes[0].Operations[0].ID
	mustRelic(t, repository, nil, "update", relicID, "--move-part", parts[0], "--after", parts[1])
	mustRelic(t, repository, nil, "update", relicID, "--move-part", parts[1], "--after", parts[0])

	// Each part went to the end in turn, so both hold new positions in the
	// same order.
	diff := relicDiffOf(t, repository, relicID, start)
	if len(diff.Parts) != 2 {
		t.Fatalf("parts = %+v, want both moved in place", diff.Parts)
	}
	for index, part := range diff.Parts {
		if part.ID != parts[index] || part.Moved == nil || part.Moved.From == part.Moved.To ||
			part.Moved.FromIndex != index || part.Moved.ToIndex != index {
			t.Fatalf("part %d = %+v, moved %+v; want it moved in place", index, part, part.Moved)
		}
	}
	code, stdout, stderr := run(t, repository, "relic", "diff", relicID, start)
	want := "Comparing:\t" + start + "\t→\t" + diff.To.Version + "\n" +
		"Part " + parts[0] + "\tsection\tchanged\n" +
		"Part " + parts[1] + "\tsection\tchanged\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("diff = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
}

func TestRelicDiffRefusesACommitID(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
	commit := relicHeadOf(t, repository, relicID)
	for _, version := range []string{commit, "ZZZZZZ"} {
		message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "diff", relicID, version)
		want := fmt.Sprintf("relic %s has no change %q in its history; a version is a change ID from \"workbook relic show %s --history\", not a Git commit",
			relicID, version, relicID)
		if message != want {
			t.Errorf("diff %s message = %q, want %q", version, message, want)
		}
	}
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "diff", relicID, versions[0][:2])
	if !strings.Contains(message, fmt.Sprintf("change prefix %q matches", versions[0][:2])) || !strings.Contains(message, "use more characters") {
		t.Errorf("ambiguous prefix message = %q", message)
	}
	relicErrorMessage(t, repository, 5, core.CategoryValidation, "diff", relicID, "")

	// These are refused before anything is read, so a folder that is not a
	// repository gets them too.
	nowhere := t.TempDir()
	code, stdout, stderr := run(t, nowhere, "relic", "diff", relicID, versions[0], versions[1], versions[0])
	if code != 2 || stdout != "" || !strings.Contains(stderr, "relic diff takes at most two versions") {
		t.Errorf("three versions = (%d, %q, %q), want exit 2", code, stdout, stderr)
	}
	for _, context := range []string{"-1", "two", "1.5", "", "ALL", "1000001", "9223372036854775807", "99999999999999999999"} {
		message := relicErrorMessage(t, nowhere, 2, core.CategoryInvocation, "diff", relicID, "--context="+context)
		if want := `relic diff --context must be a whole number or "all"`; message != want {
			t.Errorf("--context %q message = %q, want %q", context, message, want)
		}
	}
	relicErrorMessage(t, nowhere, 2, core.CategoryInvocation, "diff")
	for _, context := range []string{"0", "1000000", "all"} {
		relicDiffOf(t, repository, relicID+"#"+partID, "--context", context)
	}
}

func TestRelicDiffTextOfABinaryBody(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	first, second := "\xff\xfeSECRETONE", "\xff\x00SECRETTWO, longer"
	relicID := mustCreateRelic(t, repository, "Binary", "--type", "paper").Relic.ID
	added := mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "body.bin", first))
	partID, base := added.Parts[0].ID, added.Parts[0].Body.Version
	version := mustUpdateRelic(t, repository, relicID, "--part", partID,
		"--body-file", writeRelicBodyFile(t, "body.bin", second), "--base", base).Parts[0].Body.Version

	code, stdout, stderr := run(t, repository, "relic", "diff", relicID+"#"+partID, "--context", "all")
	want := "Comparing:\t" + base + "\t→\t" + version + "\n" +
		"Part " + partID + "\tsection\tchanged\n" +
		fmt.Sprintf("\tBody:\tbin %d bytes → bin %d bytes\tnot text\n", len(first), len(second))
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("diff = (%d, %q, %q), want %q", code, stdout, stderr, want)
	}
}

func TestRelicDiffTextOutput(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "keep\nold\nend\n", "keep\nnew \x1b[31mred\nend\n")

	code, stdout, stderr := run(t, repository, "relic", "diff", relicID+"#"+partID)
	if code != 0 || stderr != "" {
		t.Fatalf("diff = (%d, %q, %q)", code, stdout, stderr)
	}
	want := "Comparing:\t" + versions[0] + "\t→\t" + versions[1] + "\n" +
		"Part " + partID + "\tsection\tchanged\n" +
		"\tBody:\tmd 13 bytes → md 22 bytes\n" +
		"\t@@ -1,3 +1,3 @@\n" +
		"\t keep\n" +
		"\t-old\n" +
		"\t+new  [31mred\n" +
		"\t end\n"
	if stdout != want {
		t.Fatalf("text =\n%q\nwant\n%q", stdout, want)
	}

	// The side before the relic, and a part with a first body.
	alone := mustCreateRelic(t, repository, "Alone", "--type", "paper").Relic.ID
	code, stdout, stderr = run(t, repository, "relic", "diff", alone)
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "Comparing:\t(before the relic)\t→\t") ||
		!strings.Contains(stdout, "\nTitle:\t+Alone\n") {
		t.Fatalf("diff of a one-commit relic = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "relic", "diff", relicID+"#"+partID, versions[0], versions[0])
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "These two versions hold the same part.\n") {
		t.Fatalf("diff of a version with itself = (%d, %q, %q)", code, stdout, stderr)
	}
	added := mustUpdateRelic(t, repository, alone, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "body", "x\n")).Parts[0].ID
	code, stdout, stderr = run(t, repository, "relic", "diff", alone)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Part "+added+"\tsection\tadded\n\tBody:\tno body → - 2 bytes\n\t@@ -0,0 +1,1 @@\n\t+x\n") {
		t.Fatalf("diff adding a part = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestRelicDiffTextKeepsIndentation(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, _ := savedRelicPart(t, repository,
		"root:\n  two\n  tab\n", "root:\n    two\n\ttab\n")

	code, stdout, stderr := run(t, repository, "relic", "diff", relicID+"#"+partID)
	want := "\t@@ -1,3 +1,3 @@\n" +
		"\t root:\n" +
		"\t-  two\n" +
		"\t-  tab\n" +
		"\t+    two\n" +
		"\t+\ttab\n"
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, want) {
		t.Fatalf("diff = (%d, %q, %q), want it to end %q", code, stdout, stderr, want)
	}
}

func TestRelicDiffTextMarksAMissingFinalNewline(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "keep\nend", "keep\nend\n")
	marker := "\t\\ No newline at end of file\n"

	code, stdout, stderr := run(t, repository, "relic", "diff", relicID+"#"+partID)
	want := "\t@@ -1,2 +1,2 @@\n\t keep\n\t-end\n" + marker + "\t+end\n"
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, want) {
		t.Fatalf("diff = (%d, %q, %q), want it to end %q", code, stdout, stderr, want)
	}

	// Removing it again marks the added line, and a context line that has none
	// on either side is marked once.
	code, stdout, stderr = run(t, repository, "relic", "diff", relicID+"#"+partID, versions[1], versions[0])
	want = "\t@@ -1,2 +1,2 @@\n\t keep\n\t-end\n\t+end\n" + marker
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, want) {
		t.Fatalf("reverse diff = (%d, %q, %q), want it to end %q", code, stdout, stderr, want)
	}
	relicID, partID, _ = savedRelicPart(t, repository, "old\nlast", "new\nlast")
	code, stdout, stderr = run(t, repository, "relic", "diff", relicID+"#"+partID)
	want = "\t@@ -1,2 +1,2 @@\n\t-old\n\t+new\n\t last\n" + marker
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, want) {
		t.Fatalf("diff with an unterminated context line = (%d, %q, %q), want it to end %q", code, stdout, stderr, want)
	}
}

// relic migrate can change a relic's type, and a whole-relic diff lists that
// change first among the relic-level ones.
func TestRelicDiffShowsATypeChange(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := mustCreateRelic(t, repository, "Notes", "--type", "note").Relic.ID
	mustRelic(t, repository, nil, "migrate", relicID, "--to", "memo")
	diff := relicDiffOf(t, repository, relicID)
	if len(diff.Changes) != 1 || diff.Changes[0].Name != "type" || diff.Changes[0].Kind != core.ChangeSet ||
		string(diff.Changes[0].From) != `"note"` || string(diff.Changes[0].To) != `"memo"` {
		t.Fatalf("diff changes = %+v, want the type set from note to memo", diff.Changes)
	}
	code, stdout, stderr := run(t, repository, "relic", "diff", relicID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "\nType:\tnote → memo\n") {
		t.Fatalf("diff text = (%d, %q, %q), want a Type line", code, stdout, stderr)
	}
}

func TestRelicDiffTextShowsRelicLinkTargets(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	x := mustCreateRelic(t, repository, "Transcript", "--type", "transcript").Relic.ID
	turn := addRelicParts(t, repository, x, "turn")[0]
	y := mustCreateRelic(t, repository, "Decisions", "--type", "decisions").Relic.ID
	start := relicHistoryOf(t, repository, y, "--limit", "1").Changes[0].Operations[0].ID
	mustRelic(t, repository, nil, "update", y, "--link", x+"#"+turn, "--rel", "decided-in", "--pointer", `{"at":"1"}`,
		"--link", x, "--rel", "relates-to")
	links := map[string]string{}
	for _, link := range shownRelic(t, repository, y).Links {
		links[link.To.Address()] = link.ID
	}
	code, stdout, stderr := run(t, repository, "relic", "diff", y, start)
	if code != 0 || stderr != "" {
		t.Fatalf("diff = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, line := range []string{
		"\t+" + links[x+"#"+turn] + "\trelic\tdecided-in\t" + x + "#" + turn + "\t" + `{"at":"1"}` + "\n",
		"\t+" + links[x] + "\trelic\trelates-to\t" + x + "\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Fatalf("diff text = %q, want it to hold %q", stdout, line)
		}
	}
}

func TestRelicDiffOfACompactedRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
	note := addRelicParts(t, repository, relicID, "note")[0]
	mustRelic(t, repository, nil, "retire", relicID)
	compactRetiredRelics(t, repository)
	compacted := relicHistoryOf(t, repository, relicID).Compacted
	if compacted == nil {
		t.Fatal("the relic was not compacted")
	}
	point := compacted.Version

	diff := relicDiffOf(t, repository, relicID)
	if diff.From == nil || diff.From.Version != point || diff.To.Version != point || diff.Compacted == nil ||
		*diff.Compacted != *compacted || diff.Parts != nil || diff.Changes != nil {
		t.Fatalf("diff right after compaction = %+v", diff)
	}
	if got, want := mustRun(t, repository, "relic", "diff", relicID), "Comparing:\t"+point+"\t→\t"+point+"\nThese two versions hold the same relic.\n"; got != want {
		t.Fatalf("diff text = %q, want %q", got, want)
	}
	dropped := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "diff", relicID+"#"+partID, versions[0])
	if want := fmt.Sprintf("change %s of relic %s was dropped when it was compacted on %s; history and diffs reach back only to change %s",
		versions[0], relicID, dateOf(compacted.At), point); dropped != want {
		t.Errorf("diff %s message = %q, want %q", versions[0], dropped, want)
	}
	// The body the checkpoint kept is listed under the ID of its save, and
	// that ID names the checkpoint: nothing has changed since.
	kept := relicDiffOf(t, repository, relicID+"#"+partID, versions[1])
	if kept.From == nil || kept.From.Version != versions[1] || kept.From.Commit != diff.From.Commit || kept.Parts != nil {
		t.Errorf("diff from the kept body version = %+v, want the checkpoint and no change", kept)
	}
	message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "diff", relicID+"#"+note)
	if want := fmt.Sprintf("relic %s has no change to part %s since it was compacted on %s", relicID, note, dateOf(compacted.At)); message != want {
		t.Errorf("part diff message = %q, want %q", message, want)
	}

	mustRelic(t, repository, nil, "unretire", relicID)
	diff = relicDiffOf(t, repository, relicID)
	if diff.From == nil || diff.From.Version != point || len(diff.Changes) != 1 || diff.Changes[0].Name != "retired" ||
		string(diff.Changes[0].From) != "true" || string(diff.Changes[0].To) != "false" {
		t.Fatalf("diff after unretire = %+v", diff)
	}
	if text := mustRun(t, repository, "relic", "diff", relicID); !strings.Contains(text, "Retired:\ttrue → false\n") {
		t.Fatalf("diff text = %q", text)
	}
}

func TestRelicDiffShowsAMerge(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		m := newMergedRelic(t, objectFormat)
		part := m.relic + "#" + m.part
		want := &core.RelicBodyDiffMerge{Version: m.merge, Base: m.theirs,
			RelicBodyMerge: core.RelicBodyMerge{Ancestor: m.ancestor, Other: m.ours, Method: core.RelicLineMergeMethod}}

		diff := relicDiffOf(t, m.second, part, "--context", "all")
		from, to := rebuiltBodies(t, diff)
		if from != m.theirsText || to != m.mergedText || !reflect.DeepEqual(diff.Parts[0].Body.Merge, want) {
			t.Fatalf("diff = %q → %q with merge %+v, want the merge %+v", from, to, diff.Parts[0].Body.Merge, want)
		}
		diff = relicDiffOf(t, m.second, part, m.ancestor, m.merge, "--context", "all")
		if from, to = rebuiltBodies(t, diff); from != "one\ntwo\nthree\nfour\nfive\nsix\n" || to != m.mergedText ||
			!reflect.DeepEqual(diff.Parts[0].Body.Merge, want) {
			t.Fatalf("diff from the ancestor = %q → %q with merge %+v", from, to, diff.Parts[0].Body.Merge)
		}

		text := mustRun(t, m.second, "relic", "diff", part)
		line := fmt.Sprintf("\tBody:\tmd %d bytes → md %d bytes\n\tMerged:\t%s onto %s from %s\n\t@@ ",
			len(m.theirsText), len(m.mergedText), m.ours, m.theirs, m.ancestor)
		if !strings.Contains(text, line) {
			t.Fatalf("diff text = %q, want %q", text, line)
		}
		if text = mustRun(t, m.second, "relic", "diff", part, m.ancestor, m.theirs); strings.Contains(text, "Merged:") {
			t.Fatalf("diff to a save no merge made = %q", text)
		}
	})
}
