package cli

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func relicHistoryOf(t *testing.T, repository, address string, options ...string) core.RelicHistory {
	t.Helper()
	var history core.RelicHistory
	mustRelic(t, repository, &history, append([]string{"show", address, "--history"}, options...)...)
	return history
}

// historyOperationIDs lists the operation IDs of a history, change by change.
func historyOperationIDs(history core.RelicHistory) []string {
	var ids []string
	for _, change := range history.Changes {
		for _, operation := range change.Operations {
			ids = append(ids, operation.ID)
		}
	}
	return ids
}

// savedRelicPart creates a relic with one part and saves its body once for each
// further text, returning the relic's ID, the part's ID and every body version
// in order.
func savedRelicPart(t *testing.T, repository string, texts ...string) (relicID, partID string, versions []string) {
	t.Helper()
	relicID = mustCreateRelic(t, repository, "Edited", "--type", "paper").Relic.ID
	added := mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "body.md", texts[0]))
	partID = added.Parts[0].ID
	versions = []string{added.Parts[0].Body.Version}
	for _, text := range texts[1:] {
		saved := mustUpdateRelic(t, repository, relicID, "--part", partID,
			"--body-file", writeRelicBodyFile(t, "body.md", text), "--base", versions[len(versions)-1])
		versions = append(versions, saved.Parts[0].Body.Version)
	}
	return relicID, partID, versions
}

func TestRelicShowHistoryJSON(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n", "three\n")
	mustRelic(t, repository, nil, "update", relicID, "--title", "Edited, revised")

	history := relicHistoryOf(t, repository, relicID)
	if history.Relic.ID != relicID || history.Part != "" || history.Total != 5 || history.Showing != 5 ||
		len(history.Changes) != 5 || history.BodyVersions != nil || history.Truncated != nil {
		t.Fatalf("history = %+v", history)
	}
	var types []string
	for _, change := range history.Changes {
		if change.Actor != relicTestActor || change.Commit == "" || change.WallTime.IsZero() {
			t.Fatalf("change = %+v, want its author, commit and time", change)
		}
		types = append(types, change.Summary)
	}
	want := []string{"relic.create", "part.add", "part.body.set", "part.body.set", "title.set"}
	if !slices.Equal(types, want) {
		t.Fatalf("summaries = %q, want %q", types, want)
	}
	ids := historyOperationIDs(history)
	if len(ids) != 5 || !slices.Equal(ids[1:4], versions) {
		t.Fatalf("operation IDs = %q, want the three versions %q in the middle", ids, versions)
	}

	part := relicHistoryOf(t, repository, relicID+"#"+partID)
	if part.Part != partID || part.Total != 3 || !slices.Equal(historyOperationIDs(part), versions) {
		t.Fatalf("part history = %+v, want the three saves", part)
	}
	var listed []string
	for index, version := range part.BodyVersions {
		listed = append(listed, version.Version)
		if version.Current != (index == len(part.BodyVersions)-1) {
			t.Errorf("body version %d current = %v", index, version.Current)
		}
	}
	if !slices.Equal(listed, versions) {
		t.Fatalf("bodyVersions = %q, want %q", listed, versions)
	}
}

func TestRelicShowHistoryWindowAndRefusals(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, _, _ := savedRelicPart(t, repository, "one\n")
	for change := range 10 {
		mustRelic(t, repository, nil, "update", relicID, "--title", "Edited "+strconv.Itoa(change))
	}

	for _, test := range []struct {
		options []string
		showing int
	}{
		{nil, core.DefaultChangeLimit},
		{[]string{"--limit", "1"}, 1},
		{[]string{"--all"}, 12},
	} {
		history := relicHistoryOf(t, repository, relicID, test.options...)
		if history.Total != 12 || history.Showing != test.showing || len(history.Changes) != test.showing {
			t.Fatalf("%q: total %d showing %d changes %d, want 12 and %d", test.options, history.Total, history.Showing, len(history.Changes), test.showing)
		}
	}

	// The refusals come before anything is read, so a folder that is not a
	// repository gets them too.
	nowhere := t.TempDir()
	for _, test := range []struct {
		args    []string
		message string
	}{
		{[]string{"--history", "--limit", "1", "--all"}, "cannot use --limit with --all"},
		{[]string{"--limit", "1"}, "relic show --limit requires --history"},
		{[]string{"--all"}, "relic show --all requires --history"},
		{[]string{"--history", "--body"}, "relic show --history cannot be combined with --body"},
		{[]string{"--history", "--removed"}, "relic show --history cannot be combined with --removed"},
		{[]string{"--history", "--limit", "0"}, "relic show --limit must be a positive whole number"},
		{[]string{"--history", "--limit="}, "relic show --limit must be a positive whole number"},
		{[]string{"--history", "--limit", "ten"}, "relic show --limit must be a positive whole number"},
	} {
		message := relicErrorMessage(t, nowhere, 2, core.CategoryInvocation, append([]string{"show", relicID}, test.args...)...)
		if message != test.message {
			t.Errorf("%q message = %q, want %q", test.args, message, test.message)
		}
	}
}

func TestRelicShowHistoryText(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
	history := relicHistoryOf(t, repository, relicID+"#"+partID)

	code, stdout, stderr := run(t, repository, "relic", "show", relicID+"#"+partID, "--history")
	if code != 0 || stderr != "" {
		t.Fatalf("show --history = (%d, %q, %q)", code, stdout, stderr)
	}
	stamp := func(index int) string { return history.Changes[index].WallTime.UTC().Format(time.RFC3339) }
	want := relicID + "\tpaper\tEdited\n" +
		"Showing all 2 change(s).\n" +
		stamp(0) + "\t" + relicTestActor + "\tpart.add\n" +
		"\t" + versions[0] + "\tpart.add\t" + partID + "\n" +
		stamp(1) + "\t" + relicTestActor + "\tpart.body.set\n" +
		"\t" + versions[1] + "\tpart.body.set\t" + partID + "\n" +
		"Body versions:\n" +
		"\t" + versions[0] + "\t" + stamp(0) + "\t" + relicTestActor + "\tmd\t4 bytes\n" +
		"\t" + versions[1] + "\t" + stamp(1) + "\t" + relicTestActor + "\tmd\t4 bytes\tcurrent\n"
	if stdout != want {
		t.Fatalf("text =\n%q\nwant\n%q", stdout, want)
	}

	code, stdout, stderr = run(t, repository, "relic", "show", relicID, "--history", "--limit", "1")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Showing 1 most recent changes out of 3.\n") ||
		strings.Contains(stdout, "Body versions:") {
		t.Fatalf("show --history --limit 1 = (%d, %q, %q)", code, stdout, stderr)
	}
}

// A part.add and a link.add from a part name no part member; their text lines
// still show the part.
func TestRelicShowHistoryTextNamesThePartAddedOrLinkedFrom(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Build it")
	relicID, partID, versions := savedRelicPart(t, repository, "one\n")
	mustRelic(t, repository, nil, "update", relicID, "--link", task.ID, "--rel", "relates-to", "--from", partID)
	mustRelic(t, repository, nil, "update", relicID, "--link", task.ID, "--rel", "implemented-by")
	history := relicHistoryOf(t, repository, relicID)
	ids := historyOperationIDs(history)
	if len(ids) != 4 || ids[1] != versions[0] {
		t.Fatalf("operation IDs = %q", ids)
	}

	code, stdout, stderr := run(t, repository, "relic", "show", relicID, "--history")
	if code != 0 || stderr != "" {
		t.Fatalf("show --history = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, line := range []string{
		"\t" + ids[0] + "\trelic.create\n",
		"\t" + ids[1] + "\tpart.add\t" + partID + "\n",
		"\t" + ids[2] + "\tlink.add\t" + partID + "\n",
		"\t" + ids[3] + "\tlink.add\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("text = %q, want the line %q", stdout, line)
		}
	}
}

func TestStaleSaveIsListedAsNotApplied(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := openRelicFixture(t, repository)
	root := fixture.createRelic(t, 1, []byte("the first text\n"))
	relicID, partID := root.Record.State.RelicID, root.Record.State.Relic.Parts[0].ID
	second := fixture.setRelicBody(t, root, 12, partID, []byte("the second text\n"))
	const other = "bob@example.test"
	text := []byte("Bob's text, from the first version\n")
	pack := nextRelicPack(second, relicBodySetOperation(13, partID, partID, text))
	pack.Actor.ID = other
	fixture.write(t, &second, pack, map[string]core.RelicPartContent{partID: {Body: text, HasBody: true}})
	stale := relicTestID(13)

	code, stdout, stderr := run(t, repository, "relic", "show", relicID, "--history", "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"applied":false`) {
		t.Fatalf("show --history = (%d, %q, %q)", code, stdout, stderr)
	}
	history := relicHistoryOf(t, repository, relicID+"#"+partID)
	last := history.Changes[len(history.Changes)-1]
	if last.Actor != other || len(last.Operations) != 1 || last.Operations[0].ID != stale ||
		last.Operations[0].Applied == nil || *last.Operations[0].Applied {
		t.Fatalf("last change = %+v, want the stale save under %s, not applied", last, other)
	}
	for _, version := range history.BodyVersions {
		if version.Version == stale {
			t.Fatalf("bodyVersions = %+v, want no stale save", history.BodyVersions)
		}
	}
	if got := showPartBody(t, repository, relicID, partID); got != "the second text\n" {
		t.Fatalf("body = %q, want it unchanged", got)
	}
	for _, versions := range [][]string{{stale}, {relicTestID(12), stale}} {
		var diff core.RelicDiff
		mustRelic(t, repository, &diff, append([]string{"diff", relicID + "#" + partID}, versions...)...)
		if len(diff.Parts) != 0 {
			t.Errorf("diff %q parts = %+v, want no body change", versions, diff.Parts)
		}
	}
	code, stdout, stderr = run(t, repository, "relic", "show", relicID, "--history")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "\t"+stale+"\tpart.body.set\t"+partID+" (not applied)\n") {
		t.Fatalf("show --history = (%d, %q, %q), want the stale save marked", code, stdout, stderr)
	}
}

func TestHistoryFromTwoClonesShowsEveryChangeOnce(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	relicID := mustCreateRelic(t, first, "Shared", "--type", "paper").Relic.ID
	addPart := func(text string) core.RelicDocumentPart {
		updated := mustUpdateRelic(t, first, relicID, "--add-part", "--part-type", "section",
			"--body-file", writeRelicBodyFile(t, "body.md", text))
		return core.RelicDocumentPart{ID: updated.Parts[0].ID, Body: &core.RelicDocumentBody{Version: updated.Parts[0].Body.Version}}
	}
	one, two := addPart("one\n"), addPart("two\n")
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	save := func(repository string, part core.RelicDocumentPart, text string) string {
		saved := mustUpdateRelic(t, repository, relicID, "--part", part.ID,
			"--body-file", writeRelicBodyFile(t, "body.md", text), "--base", part.Body.Version, "--no-sync")
		return saved.Parts[0].Body.Version
	}
	want := []string{
		one.ID, two.ID,
		save(first, one, "one, from the first clone\n"),
		save(second, two, "two, from the second clone\n"),
	}
	for _, repository := range []string{first, second, first, second} {
		if code, stdout, stderr := run(t, repository, "sync"); code != 0 {
			t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
		}
	}
	ours := relicHistoryOf(t, first, relicID, "--all")
	theirs := relicHistoryOf(t, second, relicID, "--all")
	if !reflect.DeepEqual(ours.Changes, theirs.Changes) {
		t.Fatalf("the clones list different changes:\n%+v\n%+v", ours.Changes, theirs.Changes)
	}
	ids := historyOperationIDs(ours)
	if len(ids) != 5 {
		t.Fatalf("operation IDs = %q, want the create and four saves", ids)
	}
	for _, id := range want {
		if count := len(slices.DeleteFunc(slices.Clone(ids), func(other string) bool { return other != id })); count != 1 {
			t.Errorf("operation %s is listed %d times in %q", id, count, ids)
		}
	}
}

func TestVersionNotedBeforeASyncStillResolves(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	relicID, partID, versions := savedRelicPart(t, first, "the shared text\n")
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	mustUpdateRelic(t, first, relicID, "--title", "Moved on")
	const saved = "the second clone's text\nwith a second line, and no final newline"
	noted := mustUpdateRelic(t, second, relicID, "--part", partID,
		"--body-file", writeRelicBodyFile(t, "body.md", saved), "--base", versions[0], "--no-sync").Parts[0].Body.Version
	before := relicHistoryOf(t, second, relicID+"#"+partID).Changes
	if code, stdout, stderr := run(t, second, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	after := relicHistoryOf(t, second, relicID+"#"+partID).Changes
	if before[len(before)-1].Commit == after[len(after)-1].Commit {
		t.Fatalf("the sync did not replay the save: it is still at %s", after[len(after)-1].Commit)
	}
	if got := showPartBody(t, second, relicID, partID); got != saved {
		t.Fatalf("body = %q, want the saved text", got)
	}
	diff := relicDiffOf(t, second, relicID+"#"+partID, versions[0], noted, "--context", "all")
	if _, to := rebuiltBodies(t, diff); to != saved {
		t.Fatalf("diff to the noted version rebuilds %q, want %q", to, saved)
	}
	mustUpdateRelic(t, second, relicID, "--part", partID,
		"--body-file", writeRelicBodyFile(t, "body.md", "something later\n"), "--base", noted)
	diff = relicDiffOf(t, second, relicID+"#"+partID, noted, "--context", "all")
	if from, to := rebuiltBodies(t, diff); from != saved || to != "something later\n" || diff.From.Version != noted {
		t.Fatalf("diff from the noted version rebuilds %q → %q (from %+v)", from, to, diff.From)
	}
}

func TestRelicHistoryIsTheSameAfterTheIndexIsDeleted(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n", "three\n")
			mustRelic(t, repository, nil, "update", relicID, "--title", "Revised <&>", "--set", "owner=board")
			commands := [][]string{
				{"relic", "show", relicID, "--history", "--all", "--json"},
				{"relic", "show", relicID + "#" + partID, "--history", "--all", "--json"},
				{"relic", "diff", relicID, "--json"},
				{"relic", "diff", relicID + "#" + partID, versions[0], versions[2], "--json"},
			}
			outputs := func() []string {
				var outputs []string
				for _, command := range commands {
					code, stdout, stderr := run(t, repository, command...)
					if code != 0 || stderr != "" {
						t.Fatalf("%s = (%d, %q, %q)", strings.Join(command, " "), code, stdout, stderr)
					}
					outputs = append(outputs, stdout)
				}
				return outputs
			}
			want := outputs()
			if err := os.Remove(relicCachePath(t, repository)); err != nil {
				t.Fatalf("remove the cache: %v", err)
			}
			assertSameOutputs(t, "after the cache was deleted", outputs(), want)
		})
	}
}

// compactRetiredRelics compacts every retired and deleted relic of a
// repository with no remote, through workbook compact.
func compactRetiredRelics(t *testing.T, repository string) {
	t.Helper()
	compactRun(t, repository, "compact", "--relics", "--no-sync")
}

func TestRelicHistoryTextAndJSONAfterCompaction(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
	mustRelic(t, repository, nil, "retire", relicID)
	before := relicHistoryOf(t, repository, relicID, "--all")
	compactRetiredRelics(t, repository)

	history := relicHistoryOf(t, repository, relicID)
	compacted := history.Compacted
	if history.Total != 0 || history.Showing != 0 || compacted == nil {
		t.Fatalf("history = total %d, showing %d, compacted %+v", history.Total, history.Showing, compacted)
	}
	if compacted.Boundary != core.RelicBoundaryRetired || compacted.Changes != before.Total ||
		!compacted.From.Equal(before.Changes[0].WallTime) || !compacted.To.Equal(before.Changes[before.Total-1].WallTime) ||
		compacted.Version == "" || compacted.RunID == "" || compacted.At.IsZero() {
		t.Fatalf("compacted = %+v, want the %d changes before it", compacted, before.Total)
	}
	line := fmt.Sprintf("Compacted on %s: %d earlier changes (%s to %s) were dropped; history starts at change %s.\n",
		dateOf(compacted.At), compacted.Changes, dateOf(compacted.From), dateOf(compacted.To), compacted.Version)
	text := mustRun(t, repository, "relic", "show", relicID, "--history")
	if want := relicID + "\tpaper\tEdited\nShowing all 0 change(s).\n" + line; text != want {
		t.Fatalf("history text = %q, want %q", text, want)
	}
	part := relicHistoryOf(t, repository, relicID+"#"+partID)
	if len(part.BodyVersions) != 1 || part.BodyVersions[0].Version != versions[1] || !part.BodyVersions[0].Compacted ||
		!part.BodyVersions[0].Current || part.BodyVersions[0].Commit != relicHeadOf(t, repository, relicID) {
		t.Fatalf("bodyVersions = %+v, want the kept body", part.BodyVersions)
	}

	mustRelic(t, repository, nil, "unretire", relicID)
	history = relicHistoryOf(t, repository, relicID)
	if history.Total != 1 || history.Showing != 1 || history.Changes[0].Summary != "unretired" || history.Compacted == nil {
		t.Fatalf("history after an edit = %+v", history)
	}
	// The compaction line ends the text, after the changes and body versions.
	text = mustRun(t, repository, "relic", "show", relicID+"#"+partID, "--history")
	if !strings.Contains(text, "Body versions:\n") || !strings.HasSuffix(text, "\n"+line) {
		t.Fatalf("part history text = %q, want it to end %q", text, line)
	}
}

// A history and a diff of a compacted relic are the same bytes whether the
// index or Git answers: after a compaction, after an edit that follows one, and
// after a second compaction.
func TestHistoryFromIndexEqualsGitAfterCompaction(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		repository := relicRepositoryFor(t, objectFormat)
		type edited struct {
			relic, part string
			versions    []string
		}
		newRetired := func() edited {
			relicID, partID, versions := savedRelicPart(t, repository, "one\n", "two\n")
			addRelicParts(t, repository, relicID, "note")
			mustRelic(t, repository, nil, "retire", relicID)
			return edited{relicID, partID, versions}
		}
		save := func(relic *edited, text string) {
			saved := mustUpdateRelic(t, repository, relic.relic, "--part", relic.part,
				"--body-file", writeRelicBodyFile(t, "body.md", text), "--base", relic.versions[len(relic.versions)-1])
			relic.versions = append(relic.versions, saved.Parts[0].Body.Version)
		}
		compacted, after, twice := newRetired(), newRetired(), newRetired()
		gone := newRetired()
		mustRelic(t, repository, nil, "delete", gone.relic)
		// Fill the index with the whole histories before they are compacted,
		// so a row kept from then would show.
		for _, relic := range []edited{compacted, after, twice, gone} {
			relicHistoryOf(t, repository, relic.relic, "--all")
		}
		compactRetiredRelics(t, repository)
		// Only twice changes between the runs, so only it is compacted again;
		// after is edited once the last run is over.
		save(&twice, "three\n")
		compactRetiredRelics(t, repository)
		save(&twice, "four\n")
		save(&after, "three\n")
		mustRelic(t, repository, nil, "update", after.relic, "--title", "Revised <&>")

		var commands [][]string
		for _, relic := range []edited{compacted, after, twice, gone} {
			part := relic.relic + "#" + relic.part
			commands = append(commands,
				[]string{"relic", "show", relic.relic, "--history", "--all", "--json"},
				[]string{"relic", "show", relic.relic, "--history"},
				[]string{"relic", "show", part, "--history", "--all", "--json"},
				[]string{"relic", "show", part, "--history"},
				[]string{"relic", "diff", relic.relic, "--json"},
				[]string{"relic", "diff", relic.relic},
			)
		}
		commands = append(commands,
			[]string{"relic", "diff", after.relic + "#" + after.part, "--json"},
			[]string{"relic", "diff", twice.relic + "#" + twice.part, twice.versions[len(twice.versions)-1], "--json"},
		)
		outputs := fromIndexAndGit(t, repository, commands...)
		if !strings.Contains(outputs[0], `"compacted":{"version":"`) {
			t.Fatalf("history of the compacted relic = %s", outputs[0])
		}
		// The relics are in the states the comparison is meant to cover.
		for _, test := range []struct {
			relic          edited
			total, changes int
		}{{compacted, 0, 5}, {after, 2, 5}, {twice, 1, 6}} {
			history := relicHistoryOf(t, repository, test.relic.relic, "--all")
			if history.Total != test.total || history.Compacted == nil || history.Compacted.Changes != test.changes {
				t.Fatalf("history = total %d, compacted %+v; want %d above a checkpoint of %d changes",
					history.Total, history.Compacted, test.total, test.changes)
			}
		}
		// The versions the compactions dropped are refused the same way by
		// both, the checkpoint of an earlier run among them.
		refusals := func() []string {
			var got []string
			for _, relic := range []edited{compacted, after, twice} {
				got = append(got, relicErrorMessage(t, repository, 4, core.CategoryNotFound, "diff", relic.relic, relic.versions[0]))
			}
			return got
		}
		want := refusals()
		if err := os.Remove(relicCachePath(t, repository)); err != nil {
			t.Fatal(err)
		}
		assertSameOutputs(t, "refusals after the cache was deleted", refusals(), want)
		if err := os.Remove(relicCachePath(t, repository)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(relicCachePath(t, repository), 0o755); err != nil {
			t.Fatal(err)
		}
		for index, relic := range []edited{compacted, after, twice} {
			code, _, stderr := run(t, repository, "relic", "diff", relic.relic, relic.versions[0])
			if code != 4 || !strings.Contains(stderr, want[index]) {
				t.Fatalf("diff from Git = (%d, %q), want exit 4 and %q", code, stderr, want[index])
			}
		}
	})
}

// mergedRelic is a relic whose part two clones saved from one version, on
// different lines, merged by the second clone's sync.
type mergedRelic struct {
	first, second                    string
	relic, part                      string
	ancestor, theirs, ours, merge    string
	theirsText, oursText, mergedText string
}

func newMergedRelic(t *testing.T, objectFormat string) mergedRelic {
	t.Helper()
	const shared = "one\ntwo\nthree\nfour\nfive\nsix\n"
	m := mergedRelic{
		theirsText: strings.Replace(shared, "two", "TWO", 1),
		oursText:   strings.Replace(shared, "five", "FIVE", 1),
		mergedText: strings.Replace(strings.Replace(shared, "two", "TWO", 1), "five", "FIVE", 1),
	}
	m.first, m.second = relicSyncPairIn(t, objectFormat)
	var versions []string
	m.relic, m.part, versions = savedRelicPart(t, m.first, shared)
	m.ancestor = versions[0]
	succeed := func(repository, command string) {
		t.Helper()
		if code, stdout, stderr := run(t, repository, command); code != 0 {
			t.Fatalf("%s = (%d, %q, %q)", command, code, stdout, stderr)
		}
	}
	succeed(m.second, "fetch")
	save := func(repository, text string, options ...string) string {
		t.Helper()
		return mustUpdateRelic(t, repository, m.relic, append([]string{"--part", m.part,
			"--body-file", writeRelicBodyFile(t, "body.md", text), "--base", m.ancestor}, options...)...).Parts[0].Body.Version
	}
	m.theirs = save(m.first, m.theirsText)
	m.ours = save(m.second, m.oursText, "--no-sync")
	succeed(m.second, "sync")
	body := relicHistoryOf(t, m.second, m.relic+"#"+m.part).BodyVersions
	m.merge = body[len(body)-1].Version
	if got := showPartBody(t, m.second, m.relic, m.part); got != m.mergedText || m.merge == m.theirs || m.merge == m.ours {
		t.Fatalf("after the sync the body is %q at version %s, want the merge", got, m.merge)
	}
	return m
}

func TestRelicHistoryShowsAMerge(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		m := newMergedRelic(t, objectFormat)
		want := &core.RelicBodyMerge{Ancestor: m.ancestor, Other: m.ours, Method: core.RelicLineMergeMethod}
		history := relicHistoryOf(t, m.second, m.relic+"#"+m.part)
		change := history.Changes[len(history.Changes)-1]
		if len(change.Operations) != 2 || change.Operations[0].ID != m.ours || *change.Operations[0].Applied ||
			change.Operations[1].ID != m.merge || !*change.Operations[1].Applied ||
			!reflect.DeepEqual(change.Operations[1].Merge, want) {
			t.Fatalf("last change = %+v, want the save and the merge with %+v", change, want)
		}
		last := history.BodyVersions[len(history.BodyVersions)-1]
		if last.Base != m.theirs || !reflect.DeepEqual(last.Merge, want) {
			t.Fatalf("last body version = %+v, want the merge onto %s", last, m.theirs)
		}

		text := mustRun(t, m.second, "relic", "show", m.relic+"#"+m.part, "--history")
		for _, line := range []string{
			"\tpart.body.set, part.body.merge\n",
			"\t" + m.ours + "\tpart.body.set\t" + m.part + " (not applied)\n",
			"\t" + m.merge + "\tpart.body.merge\t" + m.part + "\tmerged " + m.ours + " onto " + m.theirs + " from " + m.ancestor + "\n",
			fmt.Sprintf("\t%d bytes\tcurrent\tmerged\n", len(m.mergedText)),
		} {
			if !strings.Contains(text, line) {
				t.Errorf("history text = %q, want it to hold %q", text, line)
			}
		}
		if count := strings.Count(text, "\tmerged\n"); count != 1 {
			t.Errorf("history text = %q, marks %d body versions as merged", text, count)
		}
	})
}

// A history and a diff of a merged relic are the same bytes whether the index
// or Git answers.
func TestHistoryFromIndexEqualsGitWithAMerge(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		m := newMergedRelic(t, objectFormat)
		part := m.relic + "#" + m.part
		outputs := fromIndexAndGit(t, m.second,
			[]string{"relic", "show", m.relic, "--history", "--all", "--json"},
			[]string{"relic", "show", part, "--history", "--all", "--json"},
			[]string{"relic", "show", part, "--history"},
			[]string{"relic", "diff", part, "--json"},
			[]string{"relic", "diff", part},
			[]string{"relic", "diff", m.relic, m.ancestor, "--json"},
		)
		for _, index := range []int{0, 1, 3, 5} {
			if !strings.Contains(outputs[index], `"ancestor":"`+m.ancestor+`","other":"`+m.ours+`"`) {
				t.Errorf("output %d = %s, want the merge's inputs", index, outputs[index])
			}
		}
	})
}
