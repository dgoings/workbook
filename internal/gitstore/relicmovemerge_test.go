package gitstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicStraggler is a relic whose body a teammate saved on line 2 while the
// tasks moved, publishing it to the old place just after the move, and whose
// body the mover then saved on line 5 at the new place.
type relicStraggler struct {
	fixture         moveFixture
	relicID, part   string
	mover, teammate core.RelicService
}

// newRelicStraggler builds a relicStraggler: a relic shared at the old place,
// a teammate's unpublished save, a move, the mover's save published at the new
// place, and then the teammate's save landing at the old place, as a push in
// flight when the old place was marked does.
func newRelicStraggler(t *testing.T, objectFormat string) relicStraggler {
	t.Helper()
	ctx := context.Background()
	fixture := newMoveFixture(t, objectFormat)
	teammate := relicServiceFor(fixture.second, fixture.stale, 200000)
	created, err := teammate.Create(ctx, core.RelicCreateInput{Title: "Plan", Type: "paper", Parts: []core.RelicPartInput{{
		Type: "section", Body: &core.RelicBodyInput{Format: "md", Content: []byte(mergeText())},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	syncClean(t, fixture.second, fixture.stale)
	relicID, part, version := created.Relic.ID, created.Parts[0].ID, created.Parts[0].Body.Version
	ref := relicRef(relicID)
	syncGit(t, fixture.first.Root, "fetch", "--quiet", fixture.origin, ref+":"+ref)
	if _, err := teammate.Update(ctx, relicID, []core.RelicEdit{saveBody(part, version, mergeText(2))}); err != nil {
		t.Fatal(err)
	}

	if result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil || !result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v", result, err)
	}
	mover := relicServiceFor(fixture.first, fixture.config, 100000)
	if _, err := mover.Update(ctx, relicID, []core.RelicEdit{saveBody(part, version, mergeText(5))}); err != nil {
		t.Fatal(err)
	}
	syncClean(t, fixture.first, fixture.config)
	syncGit(t, fixture.second.Root, "push", "--quiet", fixture.origin, ref+":"+ref)
	return relicStraggler{fixture: fixture, relicID: relicID, part: part, mover: mover, teammate: teammate}
}

// A teammate's relic body save that reached the old place just as the tasks
// moved is carried by the next run: the read step merges the mover's own save
// onto it, the copy step replays the teammate's save onto the new place with a
// merge of its own and counts the mover's merged pack as held there, since
// its merge is derived from a save the new place holds. Both edits survive,
// every clone converges, no operation is recorded twice, and a further run
// changes nothing.
func TestMoveTasksCarriesARelicSaveThatRacedTheMove(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := newRelicStraggler(t, objectFormat)
			fixture, ref := s.fixture, relicRef(s.relicID)

			result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
			if err != nil {
				t.Fatalf("MoveTasks() again = %v", err)
			}
			if !result.Verified || result.Relics != 1 {
				t.Fatalf("MoveTasks() again = %+v, want one relic, verified", result)
			}
			tip := syncGit(t, fixture.tasks, "rev-parse", ref)
			if got := relicRefHead(t, fixture.first, s.relicID); got != tip {
				t.Fatalf("the mover's relic is at %s, the new place's at %s", got, tip)
			}
			both := mergeText(2, 5)
			assertShownBody(t, s.mover, s.relicID, s.part, both)
			assertReplayedHistory(t, fixture.first, fixture.config, s.relicID, tip)
			requireRelicMoveVerifiesFromAFreshClone(t, fixture.origin, fixture.tasks, "origin", "tasks")

			second, config := pullSettings(t, fixture.second, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
			syncClean(t, second, config)
			syncClean(t, fixture.first, fixture.config)
			for _, repo := range []*Repository{fixture.first, second} {
				if got := relicRefHead(t, repo, s.relicID); got != tip {
					t.Fatalf("a clone ended on %s, want the new place's %s", got, tip)
				}
			}
			assertShownBody(t, relicServiceFor(second, config, 300000), s.relicID, s.part, both)
			if got := syncGit(t, fixture.tasks, "rev-parse", ref); got != tip {
				t.Fatalf("the new place moved to %s once the clones synced, want %s", got, tip)
			}

			before := syncGit(t, fixture.origin, "rev-parse", ref)
			again, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
			if err != nil || !again.Verified || again.Stragglers != nil || len(again.SetAside) != 0 || len(again.TaskWarnings) != 0 {
				t.Fatalf("MoveTasks() a third time = %+v, %v, want quiet", again, err)
			}
			if got := syncGit(t, fixture.tasks, "rev-parse", ref); got != tip {
				t.Fatalf("the new place moved to %s on a further run, want %s", got, tip)
			}
			if got := syncGit(t, fixture.origin, "rev-parse", ref); got != before {
				t.Fatalf("the old place moved to %s on a further run, want %s", got, before)
			}
			if got := relicRefHead(t, fixture.first, s.relicID); got != tip {
				t.Fatalf("a further run left the mover's relic at %s, want %s", got, tip)
			}
			assertNoParkedRelics(t, fixture.first)
		})
	}
}

// After the straggler is carried, the tasks move back to the old place, which
// holds the teammate's save alone. The move back replays the mover's save onto
// it with a merge and counts the new place's carried pack, the teammate's save
// and its merge, as held there.
func TestMoveTasksBackAfterACarriedRelicBodyMerge(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := newRelicStraggler(t, objectFormat)
			fixture, ref := s.fixture, relicRef(s.relicID)
			if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil {
				t.Fatalf("MoveTasks() again = %v", err)
			}
			carried := syncGit(t, fixture.tasks, "rev-parse", ref)

			first, config := pullSettings(t, fixture.first, core.TasksRemoteSetting{})
			result, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
			if err != nil || !result.Verified {
				t.Fatalf("MoveTasks() back = %+v, %v, want it verified", result, err)
			}
			tip := syncGit(t, fixture.origin, "rev-parse", ref)
			assertShownBody(t, relicServiceFor(first, config, 300000), s.relicID, s.part, mergeText(2, 5))
			assertReplayedHistory(t, first, config, s.relicID, tip)
			requireRelicMoveVerifiesFromAFreshClone(t, fixture.origin, fixture.tasks, "tasks", "origin")

			again, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
			if err != nil || !again.Verified || again.Stragglers != nil || len(again.SetAside) != 0 {
				t.Fatalf("MoveTasks() back again = %+v, %v, want quiet", again, err)
			}
			if got := syncGit(t, fixture.origin, "rev-parse", ref); got != tip {
				t.Fatalf("the old place moved to %s on the second run, want %s", got, tip)
			}
			if got := syncGit(t, fixture.tasks, "rev-parse", ref); got != carried {
				t.Fatalf("the new place moved to %s on the second run, want %s", got, carried)
			}
		})
	}
}

// A move whose copy to the new place stops because a relic's replay was
// refused says which relic and why, as the task side does: here the new place
// holds a commit reusing the ID of this checkout's unpublished relic change
// with other content.
func TestMoveTasksNamesTheRelicItCouldNotCopyAndWhy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relicID := moveRelics(t, fixture)[0]
	ref := relicRef(relicID)
	syncGit(t, fixture.first.Root, "fetch", "--quiet", fixture.origin, ref+":"+ref)
	if result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil || !result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v", result, err)
	}
	tip := readRelicTip(t, fixture.first, fixture.config, relicID)
	writeRelicChild(t, fixture.first, fixture.config, tip, nil, relicTitleSet(620001, "Mine"))
	forged := writeRelicChild(t, fixture.second, fixture.stale, readRelicTip(t, fixture.second, fixture.stale, relicID), nil,
		relicTitleSet(620001, "Forged"))
	syncGit(t, fixture.second.Root, "push", "--quiet", fixture.tasks, forged.Head+":"+ref)

	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryCorruptData || result.Verified {
		t.Fatalf("MoveTasks() = %+v, %v (category %q), want a corrupt-data refusal", result, err, core.CategoryOf(err))
	}
	for _, want := range []string{"cannot copy the tasks to tasks", "tasks: relic " + relicID + ": operation " + relicTestID(620001) + " is held on the remote with other content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("MoveTasks() error = %q, want it to contain %q", err, want)
		}
		if said := outermostMessage(t, err); !strings.Contains(said, want) {
			t.Fatalf("MoveTasks() outermost message = %q, want it to contain %q: a command prints no more than this", said, want)
		}
	}
}

// requireRelicMoveVerifiesFromAFreshClone runs the move's verification, of a
// move from one place to the other, in a fresh clone of origin that also knows
// tasks. Only a checkout holding nothing parked shows that the destination
// itself holds every relic operation the source holds.
func requireRelicMoveVerifiesFromAFreshClone(t *testing.T, origin, tasks, from, to string) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fresh")
	syncGit(t, t.TempDir(), "clone", "--quiet", origin, root)
	syncGit(t, root, "remote", "add", "tasks", tasks)
	fresh, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	config, err := fresh.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	var result MoveTasksResult
	shortfall := &moveShortfall{}
	if err := fresh.verifyMovedTasks(ctx, config, from, to, &result, shortfall); err != nil {
		t.Fatalf("verifyMovedTasks() = %v", err)
	}
	if err := fresh.verifyMovedRelics(ctx, config, from, to, &result, shortfall); err != nil {
		t.Fatalf("verifyMovedRelics() = %v", err)
	}
	if err := shortfall.refusal(from, to); err != nil {
		t.Fatalf("verification from a fresh clone = %v", err)
	}
	if result.Relics == 0 {
		t.Fatalf("the verification from a fresh clone counted no relic: %+v", result)
	}
}
