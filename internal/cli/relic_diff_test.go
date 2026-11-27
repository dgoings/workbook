package cli

import (
	"fmt"
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
	alone := mustCreateRelic(t, repository, "Alone", "--type", "spec").Relic.ID
	code, stdout, stderr := run(t, repository, "relic", "diff", alone, "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"from":null`) {
		t.Fatalf("diff of a one-commit relic = (%d, %q, %q), want from null", code, stdout, stderr)
	}
	diff := relicDiffOf(t, repository, alone)
	if diff.From != nil || len(diff.Changes) != 1 || diff.Changes[0].Name != "title" || diff.Changes[0].Kind != core.ChangeAdded {
		t.Fatalf("diff = %+v, want the title added", diff)
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
	relicID := mustCreateRelic(t, repository, "Whole", "--type", "spec").Relic.ID
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
	relicID := mustCreateRelic(t, repository, "Before", "--type", "spec").Relic.ID
	var parts []string
	for _, name := range []string{"a", "b"} {
		parts = append(parts, mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
			"--field", "heading="+name).Parts[0].ID)
	}
	mustRelic(t, repository, nil, "update", relicID, "--set", "old=1", "--set", "kept=x",
		"--link", task.ID, "--rel", "tracked-by")
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
		"\t-" + linked + "\trelic\ttracked-by\t" + task.ID + "\n"
	if stdout != want {
		t.Fatalf("text =\n%q\nwant\n%q", stdout, want)
	}

	code, stdout, stderr = run(t, repository, "relic", "diff", relicID, start, start)
	if want := "Comparing:\t" + start + "\t→\t" + start + "\nThese two versions hold the same relic.\n"; code != 0 || stderr != "" || stdout != want {
		t.Fatalf("diff of a version with itself = (%d, %q, %q), want %q", code, stdout, stderr, want)
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
	relicID := mustCreateRelic(t, repository, "Binary", "--type", "spec").Relic.ID
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
		"\t+new [31mred\n" +
		"\t end\n"
	if stdout != want {
		t.Fatalf("text =\n%q\nwant\n%q", stdout, want)
	}

	// The side before the relic, and a part with a first body.
	alone := mustCreateRelic(t, repository, "Alone", "--type", "spec").Relic.ID
	code, stdout, stderr = run(t, repository, "relic", "diff", alone)
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "Comparing:\t(before the relic)\t→\t") ||
		!strings.Contains(stdout, "\nTitle:\t+Alone\n") {
		t.Fatalf("diff of a one-commit relic = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, repository, "relic", "diff", relicID+"#"+partID, versions[0], versions[0])
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "These two versions hold the same relic.\n") {
		t.Fatalf("diff of a version with itself = (%d, %q, %q)", code, stdout, stderr)
	}
	added := mustUpdateRelic(t, repository, alone, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "body", "x\n")).Parts[0].ID
	code, stdout, stderr = run(t, repository, "relic", "diff", alone)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Part "+added+"\tsection\tadded\n\tBody:\tno body → - 2 bytes\n\t@@ -0,0 +1,1 @@\n\t+x\n") {
		t.Fatalf("diff adding a part = (%d, %q, %q)", code, stdout, stderr)
	}
}
