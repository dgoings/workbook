package cli

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// linkedToRelic runs `relic list --linked-to <address> --json`.
func linkedToRelic(t *testing.T, repository, address string, options ...string) []core.RelicSummary {
	t.Helper()
	var summaries []core.RelicSummary
	mustRelic(t, repository, &summaries, append([]string{"list", "--linked-to", address}, options...)...)
	return summaries
}

func incomingLinkIDs(document core.RelicDocument) []string {
	ids := []string{}
	for _, link := range document.LinkedFrom {
		ids = append(ids, link.Link)
	}
	return ids
}

func linkIDTo(t *testing.T, document core.RelicDocument, target string) string {
	t.Helper()
	for _, link := range document.Links {
		if link.To.Address() == target {
			return link.ID
		}
	}
	t.Fatalf("relic %s has no link to %s: %+v", document.ID, target, document.Links)
	return ""
}

// The story's chain: a transcript's turns, the decisions made in them, the
// spec section the decisions change and the task that implements it, each
// followed from both ends.
func TestRelicChainFollowsBothWays(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Compact by hand")
	transcript := mustCreateRelic(t, repository, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
	turns := addRelicParts(t, repository, transcript, "turn", "turn")
	spec := mustCreateRelic(t, repository, "Archive spec", "--type", "spec", "--no-sync").Relic.ID
	section := addRelicParts(t, repository, spec, "section")[0]
	decisions := mustCreateRelic(t, repository, "Decisions", "--type", "decision-log", "--no-sync").Relic.ID
	decided := addRelicParts(t, repository, decisions, "decision", "decision")
	mustUpdateRelic(t, repository, decisions, "--no-sync",
		"--link", transcript+"#"+turns[0], "--from", decided[0], "--rel", "decided-in", "--pointer", `{"at":"00:01:00"}`,
		"--link", transcript+"#"+turns[1], "--from", decided[1], "--rel", "decided-in", "--pointer", `{"at":"00:02:00"}`,
		"--link", spec+"#"+section, "--from", decided[0], "--rel", "changes",
		"--link", spec+"#"+section, "--from", decided[1], "--rel", "changes")
	mustUpdateRelic(t, repository, spec, "--no-sync", "--link", task.ID, "--from", section, "--rel", "implemented-by")

	shownDecisions := shownRelic(t, repository, decisions)
	if len(shownDecisions.Links) != 4 {
		t.Fatalf("decision links = %+v, want four", shownDecisions.Links)
	}
	toTurn := linkIDTo(t, shownDecisions, transcript+"#"+turns[0])
	turn := shownRelic(t, repository, transcript+"#"+turns[0])
	want := []core.RelicIncomingLink{{Relic: decisions, Title: "Decisions", Type: "decision-log", Link: toTurn,
		From: decided[0], FromType: "decision", ToPart: turns[0], Rel: "decided-in",
		Pointer: []byte(`{"at":"00:01:00"}`)}}
	for _, link := range shownDecisions.Links {
		if link.ID == toTurn {
			want[0].Seen = link.Seen
		}
	}
	if mustJSON(t, turn.LinkedFrom) != mustJSON(t, want) {
		t.Fatalf("linkedFrom of the turn = %s, want %s", mustJSON(t, turn.LinkedFrom), mustJSON(t, want))
	}
	if got := incomingLinkIDs(shownRelic(t, repository, transcript)); len(got) != 2 {
		t.Fatalf("linkedFrom of the transcript = %q, want both turns' links", got)
	}

	linked := linkedToRelic(t, repository, spec+"#"+section)
	if len(linked) != 1 || linked[0].ID != decisions || len(linked[0].LinkedBy) != 2 || linked[0].LinkedBy[0].ToPart != section {
		t.Fatalf("relic list --linked-to the section = %s", mustJSON(t, linked))
	}
	if relics := shownTask(t, repository, task.ID).Relics; len(relics) != 1 || relics[0].Relic != spec || relics[0].Part != section {
		t.Fatalf("show of the task relics = %+v, want the spec section", relics)
	}

	text := mustRun(t, repository, "relic", "show", transcript+"#"+turns[0])
	if line := "\nLinked from:\n\t" + toTurn + "\t" + decisions + "#" + decided[0] + "\tdecided-in\t" + turns[0] + "\t" + `{"at":"00:01:00"}` + "\n"; !strings.Contains(text, line) {
		t.Fatalf("relic show text = %q, want it to hold %q", text, line)
	}
	listed := mustRun(t, repository, "relic", "list", "--linked-to", transcript)
	if line := "\t" + toTurn + "\t" + decided[0] + "\tdecided-in\t" + turns[0] + "\t" + `{"at":"00:01:00"}` + "\n"; !strings.HasPrefix(listed, decisions+"\t") || !strings.Contains(listed, line) {
		t.Fatalf("relic list --linked-to text = %q, want it to hold %q", listed, line)
	}
}

func TestLinkToARelicNotFetchedFailsNoCommand(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	y := mustCreateRelic(t, first, "Decisions", "--type", "decision-log").Relic.ID
	x := mustCreateRelic(t, first, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
	part := mustUpdateRelic(t, first, x, "--no-sync", "--add-part", "--part-type", "turn").Parts[0].ID
	mustUpdateRelic(t, first, y, "--link", x+"#"+part, "--rel", "decided-in")

	mustRun(t, second, "fetch")
	if refs := cliGitOutput(t, second, "for-each-ref", "--format=%(refname)", "refs/workbook/relics/"); strings.Contains(refs, x) {
		t.Fatalf("the second clone holds X: %s", refs)
	}
	shown := shownRelic(t, second, y)
	if len(shown.Links) != 1 || shown.Links[0].TargetState != core.RelicLinkTargetMissing {
		t.Fatalf("relic show Y links = %+v, want the link marked missing", shown.Links)
	}
	if text := mustRun(t, second, "relic", "show", y); !strings.Contains(text, x+"#"+part+" (not in this clone)") {
		t.Fatalf("relic show Y text = %q", text)
	}
	for _, address := range []string{x, x + "#" + part} {
		if linked := linkedToRelic(t, second, address); len(linked) != 1 || linked[0].ID != y {
			t.Fatalf("relic list --linked-to %s = %s, want Y", address, mustJSON(t, linked))
		}
	}
	for _, command := range [][]string{
		{"relic", "show", y, "--history"},
		{"relic", "diff", y},
		{"relic", "export", y},
		{"sync"},
		{"validate"},
		{"rebuild"},
	} {
		mustRun(t, second, command...)
	}

	mustRun(t, first, "push")
	mustRun(t, second, "sync")
	if shown := shownRelic(t, second, y); shown.Links[0].TargetState != "" || shown.Links[0].TargetRemoved {
		t.Fatalf("relic show Y after X arrived = %+v, want the mark gone", shown.Links)
	}
	if got := incomingLinkIDs(shownRelic(t, second, x+"#"+part)); !slices.Equal(got, relicLinkIDs(shown)) {
		t.Fatalf("linkedFrom of X#P = %q, want Y's link %q", got, relicLinkIDs(shown))
	}
}

func TestLinksToRemovedAndDeletedTargetsFailNoCommand(t *testing.T) {
	t.Parallel()
	repository, _ := cliSyncRepositories(t)
	x := mustCreateRelic(t, repository, "Planning call", "--type", "transcript").Relic.ID
	part := addRelicParts(t, repository, x, "turn")[0]
	z := mustCreateRelic(t, repository, "Old spec", "--type", "spec").Relic.ID
	y := mustCreateRelic(t, repository, "Decisions", "--type", "decision-log").Relic.ID
	mustUpdateRelic(t, repository, y, "--link", x+"#"+part, "--rel", "decided-in", "--link", z, "--rel", "supersedes")
	mustUpdateRelic(t, repository, x, "--remove-part", part)
	mustRelic(t, repository, nil, "delete", z)

	for _, command := range [][]string{
		{"relic", "show", y},
		{"relic", "show", y, "--json"},
		{"relic", "export", y},
		{"relic", "list", "--linked-to", x},
		{"relic", "list", "--linked-to", x + "#" + part, "--json"},
		{"relic", "list", "--linked-to", z},
		{"relic", "show", y, "--history"},
		{"relic", "diff", y},
		{"sync"},
		{"validate"},
		{"rebuild"},
	} {
		mustRun(t, repository, command...)
	}
	states := map[string]string{}
	for _, link := range shownRelic(t, repository, y).Links {
		states[link.To.Address()] = link.TargetState
	}
	if states[x+"#"+part] != core.RelicLinkTargetRemoved || states[z] != core.RelicLinkTargetDeleted {
		t.Fatalf("relic show Y target states = %v", states)
	}
	if text := mustRun(t, repository, "relic", "show", y); strings.Count(text, " (removed)") != 2 {
		t.Fatalf("relic show Y text = %q, want both targets marked", text)
	}
	if got := shownRelic(t, repository, x).LinkedFrom; len(got) != 0 {
		t.Fatalf("linkedFrom of X without --removed = %+v, want none", got)
	}
	if got := shownRelic(t, repository, x, "--removed").LinkedFrom; len(got) != 1 || got[0].Relic != y || got[0].ToPart != part {
		t.Fatalf("linkedFrom of X --removed = %+v, want Y's link to the removed part", got)
	}
	if got := shownRelic(t, repository, z).LinkedFrom; len(got) != 1 || got[0].Relic != y {
		t.Fatalf("linkedFrom of the deleted Z = %+v, want Y's link", got)
	}
}

func TestConcurrentRelicLinkChangesFromTwoClonesAllSurvive(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	x := mustCreateRelic(t, first, "Planning call", "--type", "transcript").Relic.ID
	parts := addRelicParts(t, first, x, "turn", "turn")
	y := mustCreateRelic(t, first, "Decisions", "--type", "decision-log").Relic.ID
	mustUpdateRelic(t, first, y, "--link", x+"#"+parts[0], "--rel", "decided-in")
	l1 := relicLinkIDs(shownRelic(t, first, y))[0]
	mustRun(t, second, "fetch")

	mustUpdateRelic(t, first, y, "--no-sync", "--link", x+"#"+parts[1], "--rel", "decided-in", "--pointer", `{"at":"00:02:00"}`, "--unlink", l1)
	l2 := relicLinkIDs(shownRelic(t, first, y))[0]
	mustUpdateRelic(t, second, y, "--no-sync", "--link", x, "--rel", "summarizes")
	var l3 string
	for _, id := range relicLinkIDs(shownRelic(t, second, y)) {
		if id != l1 {
			l3 = id
		}
	}
	mustUpdateRelic(t, second, x, "--no-sync", "--remove-part", parts[0])
	for range 2 {
		mustRun(t, first, "sync")
		mustRun(t, second, "sync")
	}
	want := []string{l2, l3}
	slices.Sort(want)
	for name, repository := range map[string]string{"first": first, "second": second} {
		if got := relicLinkIDs(shownRelic(t, repository, y)); !slices.Equal(got, want) {
			t.Fatalf("%s clone: Y's links = %q, want %q", name, got, want)
		}
		shownX := shownRelic(t, repository, x, "--removed")
		if got := incomingLinkIDs(shownX); !slices.Equal(got, want) {
			t.Fatalf("%s clone: X's linkedFrom = %q, want %q", name, got, want)
		}
		if !shownX.Parts[0].Removed || shownX.Parts[0].ID != parts[0] {
			t.Fatalf("%s clone: X's parts = %+v, want the first removed", name, shownX.Parts)
		}
		assertRelicHistoriesValid(t, repository, 2)
	}
}

func TestRelicReverseLookupsSurviveDeletingTheIndex(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			x := mustCreateRelic(t, repository, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
			part := addRelicParts(t, repository, x, "turn")[0]
			y := mustCreateRelic(t, repository, "Decisions", "--type", "decision-log", "--no-sync").Relic.ID
			z := mustCreateRelic(t, repository, "Summary", "--type", "note", "--no-sync").Relic.ID
			mustUpdateRelic(t, repository, y, "--no-sync", "--link", x+"#"+part, "--rel", "decided-in", "--pointer", `{"at":"00:14:32"}`)
			mustUpdateRelic(t, repository, z, "--no-sync", "--link", x, "--rel", "summarizes")
			outputs := func() []string {
				return []string{
					mustRun(t, repository, "relic", "show", x, "--json"),
					mustRun(t, repository, "relic", "show", x+"#"+part, "--json"),
					mustRun(t, repository, "relic", "list", "--linked-to", x, "--json"),
					mustRun(t, repository, "relic", "show", x),
				}
			}
			want := outputs()
			if !strings.Contains(want[0], `"linkedFrom"`) || !strings.Contains(want[2], `"toPart"`) {
				t.Fatalf("outputs = %q, want incoming links listed", want)
			}
			assertSameOutputs(t, "warm", outputs(), want)
			if err := os.Remove(relicCachePath(t, repository)); err != nil {
				t.Fatal(err)
			}
			assertSameOutputs(t, "after deleting the index", outputs(), want)
			mustRun(t, repository, "rebuild")
			assertSameOutputs(t, "after rebuild", outputs(), want)
		})
	}
}

func TestRelicCreateFromFileRefusesLinkedFrom(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	x := mustCreateRelic(t, repository, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
	y := mustCreateRelic(t, repository, "Decisions", "--type", "decision-log", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, y, "--no-sync", "--link", x, "--rel", "summarizes")
	document := assertJSONResult(t, mustRun(t, repository, "relic", "show", x, "--json"), "relic show").Data
	if !strings.Contains(string(document), `"linkedFrom"`) {
		t.Fatalf("relic show = %s, want linkedFrom", document)
	}
	path := writeRelicDocumentFile(t, document)
	message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "create", "--from-file", path, "--no-sync")
	if !strings.Contains(message, "linkedFrom lists other relics' links and is not part of a relic; remove it") {
		t.Fatalf("create --from-file message = %q", message)
	}
}

// F1: a link's pointer, to a task or to a relic, reads the same through a warm
// index, a cold one and with the cache deleted as it does from Git alone.
func TestRelicLinkPointersReadThroughTheIndexEqualGit(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Compact by hand")
	x := mustCreateRelic(t, repository, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
	y := mustCreateRelic(t, repository, "Spec", "--type", "spec", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, y, "--no-sync",
		"--link", task.ID, "--rel", "implemented-by", "--pointer", `{"quote":"build it"}`,
		"--link", x, "--rel", "decided-in", "--pointer", `{"at":"00:14:32"}`)
	// A relic whose only link is to a task, so nothing else sends it to Git.
	w := mustCreateRelic(t, repository, "Plan", "--type", "plan", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, w, "--no-sync", "--link", task.ID, "--rel", "implemented-by", "--pointer", `{"quote":"build it"}`)
	outputs := func() []string {
		var got []string
		for _, relicID := range []string{w, y} {
			got = append(got,
				mustRun(t, repository, "relic", "show", relicID, "--json"),
				mustRun(t, repository, "relic", "show", relicID),
				mustRun(t, repository, "relic", "export", relicID))
		}
		return got
	}
	cache := relicCachePath(t, repository)
	if err := os.Remove(cache); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	fromGit := outputs()
	for index, output := range fromGit {
		if !strings.Contains(output, `"build it"`) || index >= 3 && !strings.Contains(output, `"00:14:32"`) {
			t.Fatalf("from Git alone = %q, want the pointers", output)
		}
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, "cold index", outputs(), fromGit)
	assertSameOutputs(t, "warm index", outputs(), fromGit)
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, "after deleting the cache", outputs(), fromGit)
	assertSameOutputs(t, "warm again", outputs(), fromGit)
}

// F2: a relic holding links to relics is indexed once, so a warm workbook
// show of a task starts as many Git processes with such a relic in the
// project as without one.
func TestWarmTaskShowCostsTheSameWithRelicLinks(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	project := func(relicLink bool) (string, string) {
		repository := initializedRepository(t)
		task := cliCreateTask(t, repository, "Compact by hand")
		x := mustCreateRelic(t, repository, "Planning call", "--type", "transcript", "--no-sync").Relic.ID
		y := mustCreateRelic(t, repository, "Spec", "--type", "spec", "--no-sync").Relic.ID
		args := []string{"--no-sync", "--link", task.ID, "--rel", "implemented-by"}
		if relicLink {
			args = append(args, "--link", x, "--rel", "decided-in")
		} else {
			args = append(args, "--link", task.ID, "--rel", "relates-to")
		}
		mustUpdateRelic(t, repository, y, args...)
		return repository, task.ID
	}
	cost := func(repository, taskID string) gitCost {
		for range 2 {
			tracedCommand(t, binary, repository, nil, "show", taskID)
		}
		warm, _ := tracedCommand(t, binary, repository, nil, "show", taskID)
		return warm
	}
	without, withoutTask := project(false)
	with, withTask := project(true)
	withoutCost, withCost := cost(without, withoutTask), cost(with, withTask)
	if withCost.processes != withoutCost.processes {
		extra, missing := commandDifference(withCost.commands, withoutCost.commands)
		t.Fatalf("a warm show of a task starts %d Git processes with a relic link and %d without; it runs %v more and %v fewer",
			withCost.processes, withoutCost.processes, extra, missing)
	}
}
