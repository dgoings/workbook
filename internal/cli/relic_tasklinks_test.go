package cli

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// shownTask runs `workbook show <task> --json` and decodes it.
func shownTask(t *testing.T, repository, taskID string) core.TaskDetail {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", taskID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show %s = (%d, %q, %q)", taskID, code, stdout, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

// linkedToTask runs `relic list --linked-to <task> --json` with options.
func linkedToTask(t *testing.T, repository, taskID string, options ...string) []core.RelicSummary {
	t.Helper()
	var summaries []core.RelicSummary
	mustRelic(t, repository, &summaries, append([]string{"list", "--linked-to", taskID}, options...)...)
	return summaries
}

func relicLinkIDs(document core.RelicDocument) []string {
	ids := []string{}
	for _, link := range document.Links {
		ids = append(ids, link.ID)
	}
	return ids
}

func taskRelicLinkIDs(detail core.TaskDetail) []string {
	ids := []string{}
	for _, link := range detail.Relics {
		ids = append(ids, link.Link)
	}
	return ids
}

func TestLinkAddedInOneCloneIsVisibleFromTheTaskInAnother(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	task := decodeMutationTask(t, mustRun(t, first, "create", "Implement compaction", "--json"), "create")
	relicID := mustCreateRelic(t, first, "Archive plan", "--type", "spec").Relic.ID
	partID := addRelicParts(t, first, relicID, "section")[0]
	linked := mustUpdateRelic(t, first, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by")
	if !linked.Changed {
		t.Fatalf("relic update --link = %+v", linked)
	}
	linkID := shownRelic(t, first, relicID).Links[0].ID

	mustRun(t, second, "fetch")
	detail := shownTask(t, second, task.ID)
	want := []core.TaskRelicLink{{Relic: relicID, Title: "Archive plan", Type: "spec", Part: partID, PartType: "section", Link: linkID, Rel: "implemented-by"}}
	if mustJSON(t, detail.Relics) != mustJSON(t, want) {
		t.Fatalf("show relics = %s, want %s", mustJSON(t, detail.Relics), mustJSON(t, want))
	}
	summaries := linkedToTask(t, second, task.ID[:len(task.ID)-3])
	if len(summaries) != 1 || summaries[0].ID != relicID ||
		mustJSON(t, summaries[0].LinkedBy) != mustJSON(t, []core.RelicLinkMatch{{Link: linkID, Part: partID, PartType: "section", Rel: "implemented-by"}}) {
		t.Fatalf("relic list --linked-to = %s", mustJSON(t, summaries))
	}

	text := mustRun(t, second, "show", task.ID)
	if want := "\nRelics:\n\t" + relicID + "#" + partID + "\timplemented-by\tArchive plan\n"; !strings.HasSuffix(text, want) {
		t.Fatalf("show text = %q, want it to end with %q", text, want)
	}
	listed := mustRun(t, second, "relic", "list", "--linked-to", task.ID)
	if want := "\t" + linkID + "\t" + partID + "\timplemented-by\n"; !strings.Contains(listed, want) || !strings.HasPrefix(listed, relicID+"\t") {
		t.Fatalf("relic list --linked-to text = %q, want a relic line and %q", listed, want)
	}
}

func TestConcurrentLinkChangesFromTwoClonesAllSurvive(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	task := decodeMutationTask(t, mustRun(t, first, "create", "Implement compaction", "--json"), "create")
	relicID := mustCreateRelic(t, first, "Archive plan", "--type", "spec").Relic.ID
	mustUpdateRelic(t, first, relicID, "--link", task.ID, "--rel", "relates-to", "--link", task.ID, "--rel", "implemented-by")
	shared := relicLinkIDs(shownRelic(t, first, relicID))
	if len(shared) != 2 {
		t.Fatalf("links = %q, want two", shared)
	}
	mustRun(t, second, "fetch")
	if got := relicLinkIDs(shownRelic(t, second, relicID)); !slices.Equal(got, shared) {
		t.Fatalf("second clone links = %q, want %q", got, shared)
	}

	mustUpdateRelic(t, first, relicID, "--no-sync", "--link", task.ID, "--rel", "decided-by", "--unlink", shared[0])
	mustUpdateRelic(t, second, relicID, "--no-sync", "--link", task.ID, "--rel", "tested-by", "--unlink", shared[1])
	for range 2 {
		mustRun(t, first, "sync")
		mustRun(t, second, "sync")
	}
	want := relicLinkIDs(shownRelic(t, first, relicID))
	if len(want) != 2 || slices.Contains(want, shared[0]) || slices.Contains(want, shared[1]) {
		t.Fatalf("links after both syncs = %q, want the two new ones", want)
	}
	rels := map[string]bool{}
	for _, link := range shownRelic(t, first, relicID).Links {
		rels[link.Rel] = true
	}
	if !rels["decided-by"] || !rels["tested-by"] {
		t.Fatalf("links after both syncs have relationships %v", rels)
	}
	for name, repository := range map[string]string{"first": first, "second": second} {
		if got := relicLinkIDs(shownRelic(t, repository, relicID)); !slices.Equal(got, want) {
			t.Fatalf("%s clone links = %q, want %q", name, got, want)
		}
		if got := taskRelicLinkIDs(shownTask(t, repository, task.ID)); !slices.Equal(got, want) {
			t.Fatalf("%s clone show relics = %q, want %q", name, got, want)
		}
		assertRelicHistoriesValid(t, repository, 1)
	}
}

func TestLinkToADeletedTaskFailsNoCommand(t *testing.T) {
	t.Parallel()
	repository, _ := cliSyncRepositories(t)
	task := decodeMutationTask(t, mustRun(t, repository, "create", "Implement compaction", "--json"), "create")
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--rel", "implemented-by")
	mustRun(t, repository, "delete", task.ID)
	for _, command := range [][]string{
		{"relic", "show", relicID},
		{"relic", "show", relicID, "--json"},
		{"relic", "export", relicID},
		{"relic", "list", "--linked-to", task.ID},
		{"relic", "list", "--linked-to", task.ID, "--json"},
		{"show", task.ID},
		{"show", task.ID, "--json"},
		{"sync"},
		{"validate"},
	} {
		mustRun(t, repository, command...)
	}
	if shown := shownRelic(t, repository, relicID); len(shown.Links) != 1 || !shown.Links[0].TargetRemoved {
		t.Fatalf("relic show links = %+v, want the link marked removed", shown.Links)
	}
	if links := shownTask(t, repository, task.ID).Relics; len(links) != 1 {
		t.Fatalf("show of the deleted task relics = %+v, want its one link", links)
	}
}

func TestShowOfATaskWithNoLinksIsUnchanged(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	unlinked := cliCreateTask(t, repository, "Unlinked")
	other := cliCreateTask(t, repository, "Linked")
	outputs := func() []string {
		return []string{mustRun(t, repository, "show", unlinked.ID), mustRun(t, repository, "show", unlinked.ID, "--json")}
	}
	before := outputs()
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, relicID, "--link", other.ID, "--rel", "relates-to")
	after := outputs()
	assertSameOutputs(t, "after another task gained a link", after, before)
	if strings.Contains(after[1], `"relics"`) || strings.Contains(after[0], "Relics:") {
		t.Fatalf("show of an unlinked task = %q", after)
	}
	if code, _, stderr := run(t, repository, "show", unlinked.ID); code != 0 || stderr != "" {
		t.Fatalf("show = (%d, %q)", code, stderr)
	}
}

func TestRelicListLinkedToCombinesWithTypeAndAll(t *testing.T) {
	t.Parallel()
	repository, task, spec, partID := linkedRelicRepository(t)
	plan := mustCreateRelic(t, repository, "Rollout", "--type", "plan", "--no-sync").Relic.ID
	gone := mustCreateRelic(t, repository, "Old plan", "--type", "plan", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, spec, "--link", task.ID, "--rel", "relates-to", "--link", task.ID, "--from", partID, "--rel", "implemented-by")
	mustUpdateRelic(t, repository, plan, "--link", task.ID, "--rel", "relates-to")
	mustUpdateRelic(t, repository, gone, "--link", task.ID, "--rel", "relates-to")
	mustRelic(t, repository, nil, "delete", gone)

	ids := func(summaries []core.RelicSummary) []string {
		got := []string{}
		for _, summary := range summaries {
			got = append(got, summary.ID)
		}
		return got
	}
	if got := ids(linkedToTask(t, repository, task.ID)); !slices.Equal(got, []string{spec, plan}) {
		t.Fatalf("--linked-to = %q", got)
	}
	if got := ids(linkedToTask(t, repository, task.ID, "--type", "plan")); !slices.Equal(got, []string{plan}) {
		t.Fatalf("--linked-to --type plan = %q", got)
	}
	if got := ids(linkedToTask(t, repository, task.ID, "--all")); !slices.Equal(got, []string{spec, plan, gone}) {
		t.Fatalf("--linked-to --all = %q", got)
	}
	if summaries := linkedToTask(t, repository, task.ID); len(summaries[0].LinkedBy) != 2 {
		t.Fatalf("spec linkedBy = %+v, want both links", summaries[0].LinkedBy)
	}
	mustUpdateRelic(t, repository, spec, "--remove-part", partID)
	if summaries := linkedToTask(t, repository, task.ID); len(summaries[0].LinkedBy) != 1 {
		t.Fatalf("spec linkedBy after removing the part = %+v, want the whole-relic link", summaries[0].LinkedBy)
	}
	if message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "list", "--linked-to", "WB-7ZZZ"); !strings.Contains(message, "no task matches") {
		t.Fatalf("--linked-to <unknown> message = %q", message)
	}
}

func TestReverseLookupsSurviveDeletingTheIndex(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			tasks := []core.Task{cliCreateTask(t, repository, "First"), cliCreateTask(t, repository, "Second")}
			relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
			partID := addRelicParts(t, repository, relicID, "section")[0]
			other := mustCreateRelic(t, repository, "Rollout", "--type", "plan", "--no-sync").Relic.ID
			mustUpdateRelic(t, repository, relicID, "--link", tasks[0].ID, "--rel", "relates-to",
				"--link", tasks[1].ID, "--from", partID, "--rel", "implemented-by")
			mustUpdateRelic(t, repository, other, "--link", tasks[0].ID, "--rel", "relates-to")
			outputs := func() []string {
				var got []string
				for _, task := range tasks {
					got = append(got, mustRun(t, repository, "show", task.ID, "--json"),
						mustRun(t, repository, "relic", "list", "--linked-to", task.ID, "--json"))
				}
				return got
			}
			want := outputs()
			if !strings.Contains(want[0], `"relics"`) || !strings.Contains(want[1], `"linkedBy"`) {
				t.Fatalf("outputs = %q, want links listed", want)
			}
			if err := os.Remove(relicCachePath(t, repository)); err != nil {
				t.Fatal(err)
			}
			assertSameOutputs(t, "after deleting the index", outputs(), want)
			mustRun(t, repository, "rebuild")
			assertSameOutputs(t, "after rebuild", outputs(), want)
		})
	}
}

// A service whose tasks were not read from a store has no relic index to ask,
// so show says the links are not listed rather than leaving them out silently.
func TestTaskRelicLinksWarnsWhenTheReaderIsNotAStore(t *testing.T) {
	t.Parallel()
	links, warnings, err := taskRelicLinks(context.Background(), core.Service{}, "WB-1")
	if err != nil || links != nil {
		t.Fatalf("taskRelicLinks() = (%v, %v, %v), want no links and no error", links, warnings, err)
	}
	want := []core.Warning{{
		Code:    core.WarningRelicLinksUnread,
		Message: "relics cannot be read, so their links are not listed: the task was not read from the local index",
	}}
	if !slices.Equal(warnings, want) {
		t.Fatalf("taskRelicLinks() warnings = %#v, want %#v", warnings, want)
	}
}
