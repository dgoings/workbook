package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// compactRelicsForTest runs the real relic compaction over every retired and
// deleted relic of the repository, dated two days from now so that a stub's
// removal date and its drop date differ.
func compactRelicsForTest(t *testing.T, repository string) compaction.RelicResult {
	t.Helper()
	ctx := context.Background()
	fixture := openRelicFixture(t, repository)
	store, release, err := openProjection(ctx, fixture.repo, fixture.config)
	if err != nil {
		t.Fatalf("openProjection() error = %v", err)
	}
	defer release()
	deps := compaction.RelicDeps{
		Repository: fixture.repo,
		Config:     fixture.config,
		Relics: core.RelicService{
			Config: fixture.config, Reader: fixture.repo, Writer: fixture.repo,
			IDs: core.CryptoULIDSource{}, Actor: relicTestActor,
		},
		Index: store,
	}
	options := compaction.RelicOptions{
		Criteria: compaction.RelicCriteria{Retired: true, Deleted: true},
		Backup:   "-",
		Now:      time.Now().UTC().Add(48 * time.Hour),
	}
	plan, err := compaction.PrepareRelics(ctx, deps, options)
	if err != nil {
		t.Fatalf("PrepareRelics() error = %v", err)
	}
	result, err := compaction.ExecuteRelics(ctx, deps, plan, options)
	if err != nil || len(result.Compacted) == 0 || len(result.Skipped) != 0 {
		t.Fatalf("ExecuteRelics() = %+v, %v", result, err)
	}
	return result
}

func dateOf(when time.Time) string {
	return when.UTC().Format("2006-01-02")
}

// fromIndexAndGit runs each command with the index as it stands, rebuilt after
// its file is deleted, and with the index impossible to build, so that Git
// answers, requires the three to agree, and returns the outputs.
func fromIndexAndGit(t *testing.T, repository string, commands ...[]string) []string {
	t.Helper()
	outputs := func() []string {
		got := make([]string, len(commands))
		for index, command := range commands {
			code, stdout, stderr := run(t, repository, command...)
			if code != 0 {
				t.Fatalf("%s = (%d, %q, %q)", strings.Join(command, " "), code, stdout, stderr)
			}
			got[index] = stdout
		}
		return got
	}
	cache := relicCachePath(t, repository)
	warm := outputs()
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, "after the index was deleted", outputs(), warm)
	// Only relic reads answer from Git when the index cannot be built; a task
	// command needs its index.
	var relicOnly [][]string
	var want []string
	for index, command := range commands {
		if command[0] == "relic" {
			relicOnly, want = append(relicOnly, command), append(want, warm[index])
		}
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	all := commands
	commands = relicOnly
	assertSameOutputs(t, "from Git", outputs(), want)
	commands = all
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	return warm
}

func TestRelicRetireAndUnretire(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			id := mustCreateRelic(t, repository, "Old plan", "--type", "plan").Relic.ID
			kept := mustCreateRelic(t, repository, "Current plan", "--type", "plan").Relic.ID
			ref := "refs/workbook/relics/" + id

			if got, want := mustRun(t, repository, "relic", "retire", id, "--no-sync"), id+"\tplan\tOld plan\n"; got != want {
				t.Fatalf("relic retire = %q, want %q", got, want)
			}
			if subject := gitOutput(t, repository, "log", "-1", "--format=%s", ref); !strings.Contains(subject, "retire relic "+id) {
				t.Fatalf("commit subject = %q", subject)
			}
			head := gitOutput(t, repository, "rev-parse", ref)
			if got, want := mustRun(t, repository, "relic", "retire", id, "--no-sync"), "No change: "+id+"\n"; got != want {
				t.Fatalf("relic retire again = %q, want %q", got, want)
			}
			if again := gitOutput(t, repository, "rev-parse", ref); again != head {
				t.Fatalf("a repeated retire moved %s to %s", ref, again)
			}

			if got := listedIDs(relicListOf(t, repository)); !slices.Equal(got, []string{kept}) {
				t.Fatalf("relic list = %q, want the retired relic left out", got)
			}
			for _, option := range []string{"--retired", "--all"} {
				listed := relicListOf(t, repository, option)
				if !slices.Equal(listedIDs(listed), []string{id, kept}) || !listed[0].Retired || listed[1].Retired {
					t.Fatalf("relic list %s = %s", option, mustJSON(t, listed))
				}
				text := mustRun(t, repository, "relic", "list", option)
				if want := id + "\tplan\t0 parts\tOld plan\tretired\n" + kept + "\tplan\t0 parts\tCurrent plan\n"; text != want {
					t.Fatalf("relic list %s = %q, want %q", option, text, want)
				}
			}
			if shown := shownRelic(t, repository, id); !shown.Retired || shown.Compacted != nil {
				t.Fatalf("relic show = %+v", shown)
			}
			text := mustRun(t, repository, "relic", "show", id)
			if !strings.Contains(text, "\nRetired:\tyes\n") || strings.Contains(text, "Compacted:") {
				t.Fatalf("relic show = %q", text)
			}
			listJSON := mustRun(t, repository, "relic", "list", "--retired", "--json")
			if strings.Count(listJSON, `"compacted":null`) != 2 {
				t.Fatalf("relic list --json = %s, want compacted null on both", listJSON)
			}
			fromIndexAndGit(t, repository,
				[]string{"relic", "list"}, []string{"relic", "list", "--retired", "--json"},
				[]string{"relic", "show", id}, []string{"relic", "show", id, "--json"})

			var unretired core.RelicMutationResult
			mustRelic(t, repository, &unretired, "unretire", id, "--no-sync")
			if !unretired.Changed || unretired.Relic.Retired {
				t.Fatalf("relic unretire = %+v", unretired)
			}
			mustRelic(t, repository, &unretired, "unretire", id, "--no-sync")
			if unretired.Changed {
				t.Fatalf("relic unretire again = %+v, want no change", unretired)
			}
			if got := listedIDs(relicListOf(t, repository)); !slices.Equal(got, []string{id, kept}) {
				t.Fatalf("relic list after unretire = %q", got)
			}

			if retired := mustRun(t, repository, "relic", "retire", id, "--no-sync", "--json"); !strings.Contains(retired, `"command":"relic retire"`) ||
				!strings.Contains(retired, `"retired":true`) {
				t.Fatalf("relic retire --json = %s", retired)
			}
		})
	}
}

func TestRetiredRelicCanStillBeEdited(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Implement compaction")
	relicID := mustCreateRelic(t, repository, "Plan", "--type", "spec", "--no-sync").Relic.ID
	mustRelic(t, repository, nil, "retire", relicID)
	mustUpdateRelic(t, repository, relicID, "--title", "Plan, revised", "--add-part", "--part-type", "section")
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--rel", "relates-to")
	mustPutSchema(t, repository, "spec", `{}`)
	var migrated core.RelicMutationResult
	mustRelic(t, repository, &migrated, "migrate", relicID, "--to", "1")
	if !migrated.Changed || !migrated.Relic.Retired {
		t.Fatalf("migrate of a retired relic = %+v", migrated)
	}
	var deleted core.RelicMutationResult
	mustRelic(t, repository, &deleted, "delete", relicID)
	if !deleted.Changed || !deleted.Relic.Deleted || !deleted.Relic.Retired {
		t.Fatalf("delete of a retired relic = %+v", deleted)
	}
	text := mustRun(t, repository, "relic", "list", "--all")
	if !strings.HasSuffix(text, "\tPlan, revised\tretired\tdeleted\n") {
		t.Fatalf("relic list --all = %q, want retired before deleted", text)
	}
	if listed := relicListOf(t, repository, "--retired"); len(listed) != 0 {
		t.Fatalf("relic list --retired = %s, want the deleted one left out", mustJSON(t, listed))
	}
}

func TestRetireRefusesDeletedRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := mustCreateRelic(t, repository, "Plan", "--type", "spec").Relic.ID
	mustRelic(t, repository, nil, "delete", relicID)
	head := gitOutput(t, repository, "rev-parse", "refs/workbook/relics/"+relicID)
	for _, verb := range []string{"retire", "unretire"} {
		message := relicErrorMessage(t, repository, 5, core.CategoryValidation, verb, relicID, "--no-sync")
		if want := "relic " + relicID + " is deleted; restore it first with \"workbook relic restore " + relicID + "\""; message != want {
			t.Fatalf("relic %s message = %q, want %q", verb, message, want)
		}
	}
	if got := gitOutput(t, repository, "rev-parse", "refs/workbook/relics/"+relicID); got != head {
		t.Fatalf("a refused retire moved the ref to %s", got)
	}
}

func TestListBehindLeavesOutRetired(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	old := mustCreateRelic(t, repository, "Old", "--type", "spec").Relic.ID
	current := mustCreateRelic(t, repository, "Current", "--type", "spec").Relic.ID
	mustPutSchema(t, repository, "spec", ownerSchemaOne)
	mustRelic(t, repository, nil, "retire", old)

	if got := listedIDs(relicListOf(t, repository, "--behind")); !slices.Equal(got, []string{current}) {
		t.Fatalf("--behind = %q, want only the relic still in use", got)
	}
	for _, option := range []string{"--retired", "--all"} {
		if got := listedIDs(relicListOf(t, repository, "--behind", option)); !slices.Equal(got, []string{old, current}) {
			t.Fatalf("--behind %s = %q", option, got)
		}
	}
	fromIndexAndGit(t, repository,
		[]string{"relic", "list", "--behind", "--json"}, []string{"relic", "list", "--behind", "--retired", "--json"})
}

func TestShowTaskMarksRetiredRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Implement compaction")
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
	partID := addRelicParts(t, repository, relicID, "section")[0]
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by")
	linkID := shownRelic(t, repository, relicID).Links[0].ID
	if before := mustRun(t, repository, "show", task.ID, "--json"); strings.Contains(before, `"retired"`) {
		t.Fatalf("show before retiring = %s", before)
	}
	mustRelic(t, repository, nil, "retire", relicID)

	detail := shownTask(t, repository, task.ID)
	if len(detail.Relics) != 1 || !detail.Relics[0].Retired || detail.Relics[0].Deleted || detail.Relics[0].RemovedAt != nil {
		t.Fatalf("show relics = %s", mustJSON(t, detail.Relics))
	}
	text := mustRun(t, repository, "show", task.ID)
	if want := "\t" + relicID + "#" + partID + " (retired)\timplemented-by\tArchive plan\n"; !strings.HasSuffix(text, want) {
		t.Fatalf("show text = %q, want it to end with %q", text, want)
	}

	if listed := linkedToTask(t, repository, task.ID); len(listed) != 0 {
		t.Fatalf("--linked-to = %s, want the retired relic left out", mustJSON(t, listed))
	}
	listed := linkedToTask(t, repository, task.ID, "--retired")
	if len(listed) != 1 || !listed[0].Retired || listed[0].Compacted != nil {
		t.Fatalf("--linked-to --retired = %s", mustJSON(t, listed))
	}
	matches := mustRun(t, repository, "relic", "list", "--linked-to", task.ID, "--retired")
	if want := "\t" + linkID + "\t" + partID + "\timplemented-by (retired)\n"; !strings.HasSuffix(matches, want) {
		t.Fatalf("--linked-to --retired = %q, want it to end with %q", matches, want)
	}
	fromIndexAndGit(t, repository,
		[]string{"show", task.ID, "--json"}, []string{"relic", "list", "--linked-to", task.ID, "--retired", "--json"},
		[]string{"relic", "list", "--linked-to", task.ID, "--retired"})
}

func TestShowRelicsMarks(t *testing.T) {
	t.Parallel()
	removed := time.Date(2026, time.November, 20, 8, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	writeShowRelics(&out, []core.TaskRelicLink{
		{Relic: "A", Rel: "r", Title: "t", Retired: true},
		{Relic: "B", Rel: "r", Title: "t", Retired: true, Deleted: true},
		{Relic: "C", Part: "P", Rel: "r", Title: "t", Deleted: true, RemovedAt: &removed},
		{Relic: "D", Rel: "r", Title: "t"},
	})
	want := "\nRelics:\n\tA (retired)\tr\tt\n\tB (deleted)\tr\tt\n\tC#P (removed on 2026-11-20)\tr\tt\n\tD\tr\tt\n"
	if out.String() != want {
		t.Fatalf("writeShowRelics = %q, want %q", out.String(), want)
	}
}

// compactedRetiredRelic writes a relic of two parts, the second removed with a
// field and a body, retires it and compacts it. It returns the relic, its live
// part and its stub.
func compactedRetiredRelic(t *testing.T, repository string) (relicID, live, stub string) {
	t.Helper()
	relicID = mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
	parts := addRelicParts(t, repository, relicID, "section", "note")
	live, stub = parts[0], parts[1]
	mustUpdateRelic(t, repository, relicID, "--part", stub, "--field", "owner=ana",
		"--body-file", writeRelicBodyFile(t, "note.md", "dropped text\n"))
	mustUpdateRelic(t, repository, relicID, "--remove-part", stub)
	mustRelic(t, repository, nil, "retire", relicID)
	compactRelicsForTest(t, repository)
	return relicID, live, stub
}

func TestStubRefusals(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			task := cliCreateTask(t, repository, "Implement compaction")
			relicID, live, stub := compactedRetiredRelic(t, repository)
			other := mustCreateRelic(t, repository, "Other", "--type", "spec", "--no-sync").Relic.ID

			shown := shownRelic(t, repository, relicID, "--removed")
			if shown.Compacted == nil || shown.Compacted.Boundary != core.RelicBoundaryRetired ||
				len(shown.Parts) != 2 || shown.Parts[1].Dropped == nil || shown.Parts[0].Dropped != nil {
				t.Fatalf("relic show --removed = %s", mustJSON(t, shown))
			}
			drop := *shown.Parts[1].Dropped
			if dateOf(drop.RemovedAt) == dateOf(drop.DroppedAt) {
				t.Fatalf("the fixture's dates do not differ: %+v", drop)
			}
			want := "part " + stub + " of relic " + relicID + " was removed on " + dateOf(drop.RemovedAt) +
				"; its content was dropped when the relic was compacted on " + dateOf(drop.DroppedAt) +
				", and only the backup bundle written then holds it"
			head := gitOutput(t, repository, "rev-parse", "refs/workbook/relics/"+relicID)
			for _, args := range [][]string{
				{"update", relicID, "--restore-part", stub},
				{"update", relicID, "--part", stub, "--field", "owner=ben"},
				{"update", relicID, "--move-part", stub, "--before", live},
				{"update", relicID, "--link", task.ID, "--from", stub, "--rel", "relates-to"},
				{"update", relicID, "--move-part", live, "--before", stub},
				{"update", other, "--link", relicID + "#" + stub, "--rel", "relates-to"},
			} {
				message := relicErrorMessage(t, repository, 5, core.CategoryValidation, append(args, "--no-sync")...)
				if message != want {
					t.Errorf("relic %s message = %q, want %q", strings.Join(args, " "), message, want)
				}
			}
			if got := gitOutput(t, repository, "rev-parse", "refs/workbook/relics/"+relicID); got != head {
				t.Fatalf("a refused change moved the ref to %s", got)
			}
			var removedAgain core.RelicMutationResult
			mustRelic(t, repository, &removedAgain, "update", relicID, "--remove-part", stub, "--no-sync")
			if removedAgain.Changed {
				t.Fatalf("removing a stub = %+v, want no change", removedAgain)
			}

			text := mustRun(t, repository, "relic", "show", relicID, "--removed")
			for _, line := range []string{
				"\nRetired:\tyes\n",
				"\nCompacted:\t" + dateOf(shown.Compacted.At) + " (retired)\n",
				"\n#" + stub + "\tnote\tremoved on " + dateOf(drop.RemovedAt) + ", content dropped " + dateOf(drop.DroppedAt) + "\n",
				"\n#" + live + "\tsection\tno body\n",
			} {
				if !strings.Contains(text, line) {
					t.Errorf("relic show --removed = %q, want %q", text, line)
				}
			}
			listed := mustRun(t, repository, "relic", "list", "--retired", "--json")
			if !strings.Contains(listed, `"compacted":{"runId":`) {
				t.Fatalf("relic list --retired --json = %s", listed)
			}
			fromIndexAndGit(t, repository,
				[]string{"relic", "list", "--retired", "--json"},
				[]string{"relic", "show", relicID, "--removed", "--json"},
				[]string{"relic", "show", relicID, "--removed"},
				[]string{"relic", "show", relicID + "#" + stub, "--json"},
				[]string{"relic", "export", relicID})
		})
	}
}

func TestRestoreCompactedDeletedRelicWarns(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Implement compaction")
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
	parts := addRelicParts(t, repository, relicID, "section", "note")
	mustUpdateRelic(t, repository, relicID, "--set", "outcome=kept",
		"--link", task.ID, "--rel", "relates-to", "--link", task.ID, "--from", parts[0], "--rel", "implemented-by")
	other := mustCreateRelic(t, repository, "Rollout", "--type", "plan", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, other, "--link", relicID, "--rel", "relates-to", "--link", relicID+"#"+parts[1], "--rel", "relates-to")
	links := shownRelic(t, repository, relicID).Links
	mustRelic(t, repository, nil, "delete", relicID)
	compacted := compactRelicsForTest(t, repository)
	since := compacted.Compacted[0].Since

	var whole, part core.RelicLink
	shownOther := shownRelic(t, repository, other)
	for _, link := range shownOther.Links {
		if link.To.Part == "" {
			whole = link.RelicLink
		} else {
			part = link.RelicLink
		}
	}
	shown := shownRelic(t, repository, relicID, "--removed")
	if shown.Compacted == nil || shown.Compacted.Boundary != core.RelicBoundaryDeleted {
		t.Fatalf("relic show of the compacted relic = %s", mustJSON(t, shown))
	}
	partRemoved := shown.Parts[1].Dropped.RemovedAt
	outgoing := mustRun(t, repository, "relic", "show", other)
	for _, want := range []string{
		"\t" + whole.ID + "\trelic\trelates-to\t" + relicID + " (removed on " + dateOf(since) + ")\n",
		"\t" + part.ID + "\trelic\trelates-to\t" + relicID + "#" + parts[1] + " (removed on " + dateOf(partRemoved) + ")\n",
	} {
		if !strings.Contains(outgoing, want) {
			t.Errorf("relic show of the linking relic = %q, want %q", outgoing, want)
		}
	}
	matches := mustRun(t, repository, "relic", "list", "--linked-to", task.ID, "--all")
	if !strings.Contains(matches, "\trelic\trelates-to (removed on "+dateOf(since)+")\n") {
		t.Errorf("--linked-to --all = %q, want the removal date", matches)
	}
	listed := linkedToTask(t, repository, task.ID, "--all")
	if len(listed) != 1 || listed[0].Compacted == nil || !listed[0].Compacted.Since.Equal(since) {
		t.Fatalf("--linked-to --all --json = %s", mustJSON(t, listed))
	}
	if hidden := shownTask(t, repository, task.ID).Relics; len(hidden) != 0 {
		t.Fatalf("show of the task = %s, want a deleted relic left out", mustJSON(t, hidden))
	}
	fromIndexAndGit(t, repository,
		[]string{"relic", "list", "--all", "--json"},
		[]string{"relic", "list", "--linked-to", task.ID, "--all", "--json"},
		[]string{"relic", "list", "--linked-to", task.ID, "--all"},
		[]string{"relic", "show", other},
		[]string{"relic", "show", relicID, "--removed", "--json"})

	code, stdout, stderr := run(t, repository, "relic", "restore", relicID, "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("relic restore = (%d, %q, %q)", code, stdout, stderr)
	}
	var envelope struct {
		Data     core.RelicMutationResult `json:"data"`
		Warnings []core.Warning           `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	wantWarning := core.Warning{
		Code: "relic-content-dropped",
		Message: "relic " + relicID + " is back, but its content was dropped when it was compacted on " +
			dateOf(shown.Compacted.At) + "; only its title, type, author, dates and links remain",
	}
	if !envelope.Data.Changed || envelope.Data.Relic.Deleted || envelope.Data.Relic.Parts != 0 ||
		!slices.Contains(envelope.Warnings, wantWarning) {
		t.Fatalf("relic restore = %s, want warning %+v", stdout, wantWarning)
	}
	restored := shownRelic(t, repository, relicID)
	if restored.Deleted || len(restored.Parts) != 0 || len(restored.Properties) != 0 ||
		mustJSON(t, restored.Links) == "[]" || restored.Links[0].ID != links[0].ID {
		t.Fatalf("restored relic = %s", mustJSON(t, restored))
	}
	if kept := shownRelic(t, repository, relicID, "--removed"); len(kept.Links) != len(links) {
		t.Fatalf("restored links = %s, want %d", mustJSON(t, kept.Links), len(links))
	}

	// With new content, a later restore has nothing to warn about.
	addRelicParts(t, repository, relicID, "section")
	mustRelic(t, repository, nil, "delete", relicID)
	var again core.RelicMutationResult
	mustRelic(t, repository, &again, "restore", relicID)
	if !again.Changed || len(again.Warnings) != 0 {
		t.Fatalf("restore of a relic with new content = %+v, want no warning", again)
	}
}

func TestRestoreCompactedDeletedRelicWarnsInText(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec", "--no-sync").Relic.ID
	mustRelic(t, repository, nil, "delete", relicID)
	compactRelicsForTest(t, repository)
	code, stdout, stderr := run(t, repository, "relic", "restore", relicID, "--no-sync")
	if code != 0 || !strings.HasPrefix(stdout, relicID+"\tspec\tArchive plan\n") ||
		!strings.Contains(stderr, "only its title, type, author, dates and links remain") {
		t.Fatalf("relic restore = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestCreateFromFileSkipsDroppedParts(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	relicID, live, stub := compactedRetiredRelic(t, repository)
	exported := mustRun(t, repository, "relic", "export", relicID)
	var document core.RelicDocument
	if err := json.Unmarshal([]byte(exported), &document); err != nil {
		t.Fatal(err)
	}
	if document.Compacted == nil || !slices.Equal(relicPartIDs(document.Parts), []string{live, stub}) ||
		document.Parts[1].Dropped == nil || !document.Parts[1].Removed {
		t.Fatalf("relic export = %s, want compacted and the stub with its dates", exported)
	}
	copied := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, []byte(exported)), "--no-sync")
	shown := shownRelic(t, repository, copied.Relic.ID, "--removed")
	if shown.Compacted != nil || shown.Retired || len(shown.Parts) != 1 || shown.Parts[0].Type != "section" {
		t.Fatalf("copy of a compacted relic = %s", mustJSON(t, shown))
	}
}
