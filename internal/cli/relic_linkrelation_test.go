package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// linkRelicDirectly writes link.adds onto the relic's tip as a teammate's
// build could have written them, with no relationship checked: a link named
// rel to each target, a task ID or a relic ID. It returns the links' IDs.
func linkRelicDirectly(t *testing.T, repository, relicID string, operationNumber int, links ...[2]string) []string {
	t.Helper()
	ctx := context.Background()
	fixture := openRelicFixture(t, repository)
	tip, err := fixture.repo.GetRelic(ctx, fixture.config, relicID)
	if err != nil {
		t.Fatal(err)
	}
	var operations []core.RelicOperation
	var ids []string
	for index, link := range links {
		rel, address := link[0], link[1]
		change := core.RelicLinkChange{Rel: rel}
		if core.IsRelicAddress(address) {
			target, err := fixture.repo.GetRelic(ctx, fixture.config, address)
			if err != nil {
				t.Fatal(err)
			}
			seen := target.Operation.Operations
			change.To, change.Seen = core.RelicLinkTarget{Relic: address}, seen[len(seen)-1].ID
		} else {
			change.To = core.RelicLinkTarget{Task: address}
		}
		id := relicTestID(operationNumber + index)
		ids = append(ids, id)
		operations = append(operations, core.RelicOperation{ID: id, Type: core.RelicOperationLinkAdd, Link: &change})
	}
	fixture.write(t, &tip, nextRelicPack(tip, operations...), nil)
	return ids
}

// Links made before relationship names were checked, named blocks and r,
// stay readable: shown, listed, fetched, synced and validated with exit 0.
// An open relic holding them stays open; one under a schema does not
// conform, naming the first.
func TestOldLinkNamesStayReadable(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			if objectFormat == testrepo.FormatSHA256 {
				testrepo.RequireObjectFormat(t, objectFormat)
			}
			project := codeProject(t, objectFormat)
			first := project.seed
			second := cloneCode(t, project)
			if code, stdout, stderr := run(t, second, "setup"); code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}

			task := cliCreateTask(t, first, "Build it")
			open := mustCreateRelic(t, first, "Notes", "--type", "paper", "--no-sync").Relic.ID
			spec := mustCreateRelic(t, first, "--from-file", builtInSpecFile(t), "--no-sync").Relic.ID
			linkRelicDirectly(t, first, open, 990001, [2]string{"blocks", task.ID}, [2]string{"r", spec})
			specLinks := linkRelicDirectly(t, first, spec, 990011, [2]string{"blocks", task.ID}, [2]string{"r", open})
			syncedClone(t, first)
			for _, command := range []string{"fetch", "sync"} {
				if code, stdout, stderr := run(t, second, command); code != 0 {
					t.Fatalf("%s = (%d, %q, %q)", command, code, stdout, stderr)
				}
			}

			for name, clone := range map[string]string{"first": first, "second": second} {
				for _, relicID := range []string{open, spec} {
					if got, want := relicHeadOf(t, clone, relicID), relicHeadOf(t, first, relicID); got != want {
						t.Fatalf("%s clone's %s is at %s, want %s", name, relicID, got, want)
					}
				}
				var shownOpen, shownSpec core.RelicDocument
				mustRelic(t, clone, &shownOpen, "show", open)
				mustRelic(t, clone, &shownSpec, "show", spec)
				for label, shown := range map[string]core.RelicDocument{"open": shownOpen, "spec": shownSpec} {
					if len(shown.Links) != 2 || shown.Links[0].Rel != "blocks" || shown.Links[1].Rel != "r" {
						t.Fatalf("%s clone's %s relic shows links %+v, want blocks and r", name, label, shown.Links)
					}
				}
				if shownOpen.State != core.RelicConformanceOpen {
					t.Errorf("%s clone's open relic = %+v, want open", name, shownOpen.RelicConformance)
				}
				if want := "link " + specLinks[0] + ` relationship "blocks": the relationship must be one of `; shownSpec.State != core.RelicConformanceNotConforming ||
					!strings.HasPrefix(shownSpec.Problem, want) {
					t.Errorf("%s clone's spec = %+v, want not-conforming at %q", name, shownSpec.RelicConformance, want)
				}
				listed := relicListOf(t, clone)
				if len(listed) != 2 {
					t.Fatalf("%s clone lists %s, want both relics", name, mustJSON(t, listed))
				}
				for _, command := range [][]string{{"validate"}, {"show", task.ID, "--json"}} {
					if code, stdout, stderr := run(t, clone, command...); code != 0 {
						t.Fatalf("%s clone's %v = (%d, %q, %q)", name, command, code, stdout, stderr)
					}
				}
			}

			// Adding the same link again writes nothing; a new link still needs
			// a listed name.
			if again := mustUpdateRelic(t, second, open, "--link", task.ID, "--rel", "blocks", "--no-sync"); again.Changed {
				t.Fatal("re-adding the blocks link changed the relic")
			}
			other := cliCreateTask(t, second, "Build more")
			relicErrorMessage(t, second, 5, core.CategoryValidation, "update", open, "--link", other.ID, "--rel", "blocks", "--no-sync")
			mustUpdateRelic(t, second, open, "--link", other.ID, "--rel", "relates-to", "--no-sync")
		})
	}
}

// workbook show of a task judges no relic and no link: a task linked from a
// relic under a built-in, which also holds a link no schema lists, costs as
// many Git processes as one linked from an open relic.
func TestTaskCommandsJudgeNoLinks(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	project := func(builtIn bool) (string, string) {
		repository := initializedRepository(t)
		task := cliCreateTask(t, repository, "Build it")
		file := builtInSpecFile(t)
		if !builtIn {
			file = openPaperFile(t)
		}
		relic := mustCreateRelic(t, repository, "--from-file", file, "--no-sync").Relic
		if named := relic.Schema != nil && relic.Schema.BuiltIn; named != builtIn {
			t.Fatalf("relic schema = %+v, want built in %v", relic.Schema, builtIn)
		}
		mustUpdateRelic(t, repository, relic.ID, "--no-sync", "--link", task.ID, "--rel", "implemented-by")
		linkRelicDirectly(t, repository, relic.ID, 990101, [2]string{"blocks", task.ID})
		return repository, task.ID
	}
	cost := func(repository, taskID string) gitCost {
		for range 2 {
			tracedCommand(t, binary, repository, nil, "show", taskID)
		}
		warm, _ := tracedCommand(t, binary, repository, nil, "show", taskID)
		return warm
	}
	open, openTask := project(false)
	builtIn, builtInTask := project(true)
	openCost, builtInCost := cost(open, openTask), cost(builtIn, builtInTask)
	if builtInCost.processes != openCost.processes {
		extra, missing := commandDifference(builtInCost.commands, openCost.commands)
		t.Fatalf("a warm show of a task starts %d Git processes linked from a relic on a built-in and %d from an open one; it runs %v more and %v fewer",
			builtInCost.processes, openCost.processes, extra, missing)
	}
}

// openPaperFile is builtInSpecFile as a paper, a type with no schema.
func openPaperFile(t *testing.T) string {
	t.Helper()
	return writeRelicDocumentFile(t, []byte(`{"format":"workbook.relic","version":1,"title":"Archive compaction","type":"paper",`+
		`"properties":{"status":"draft"},"parts":[`+
		`{"type":"section","fields":{"title":"Goals"},"body":{"format":"md","text":"Keep history small."}},`+
		`{"type":"decision","fields":{"title":"Where do checkpoints live?","chosen":"In the relic ref","status":"decided"}},`+
		`{"type":"open-question","fields":{"title":"How often?"}}]}`))
}
