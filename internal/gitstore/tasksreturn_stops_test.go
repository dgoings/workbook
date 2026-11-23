package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// freshSetup is a fresh clone of the code on setting running what workbook
// setup runs before it publishes anything: add the tasks remote, probe it,
// check that a move to it finished, and synchronize. It returns the clone
// whatever the outcome, so the caller can go on to record and publish an edit
// there as a person whose setup failed might.
func freshSetup(t *testing.T, origin string, setting core.TasksRemoteSetting) (*Repository, core.ProjectConfig, error) {
	t.Helper()
	ctx := context.Background()
	clone := openSyncClone(t, origin)
	if _, err := clone.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if setting.Chosen() {
		if _, err := clone.SetProjectTasksRemote(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(ctx, clone.Root)
	if err != nil {
		t.Fatal(err)
	}
	config, err := reopened.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	setup, err := reopened.EnsureTasksRemote(ctx)
	if err != nil {
		return reopened, config, err
	}
	if setup != nil {
		if _, err := reopened.ProbeTasksRemote(ctx); err != nil {
			return reopened, config, err
		}
	}
	if setup != nil {
		if err := reopened.CheckMoveDone(ctx); err != nil {
			return reopened, config, err
		}
	}
	reopened.RequireFinishedMove()
	_, err = reopened.Sync(ctx, config)
	// The handle goes on as the commands run after setup do, without it.
	reopened = reopenRepository(t, reopened)
	return reopened, config, err
}

// placeHolds reports whether a bare repository holds a task ref.
func placeHolds(bare, taskID string) bool {
	_, err := plainGit(bare, "rev-parse", "--verify", "--quiet", taskRefPrefix+taskID)
	return err == nil
}

// publishAttempt is one try at publishing a new task from a checkout.
type publishAttempt struct {
	who    string
	taskID string
	err    error
}

// attemptPublish records a new task in checkout and synchronizes it.
func attemptPublish(t *testing.T, who string, checkout *Repository, config core.ProjectConfig) publishAttempt {
	t.Helper()
	task := createSyncTask(t, checkout, config, "Published by "+who)
	_, err := checkout.Sync(context.Background(), config)
	return publishAttempt{who: who, taskID: task.ID, err: err}
}

// acceptingPlaces is the places, by name, that hold at least one of the
// attempts' tasks, with who reached each.
func acceptingPlaces(places map[string]string, attempts []publishAttempt) map[string][]string {
	accepted := map[string][]string{}
	for name, bare := range places {
		for _, attempt := range attempts {
			if placeHolds(bare, attempt.taskID) {
				accepted[name] = append(accepted[name], attempt.who)
			}
		}
	}
	return accepted
}

func describeAttempts(attempts []publishAttempt) string {
	lines := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		outcome := "accepted"
		if attempt.err != nil {
			outcome = attempt.err.Error()
		}
		lines = append(lines, attempt.who+": "+outcome)
	}
	return strings.Join(lines, "\n")
}

// moveBackRoute is one way the tasks travel and come back, with a checkout
// set up on every generation of the settings it passes through.
type moveBackRoute struct {
	name string
	// prepare makes every move but the last, sets up a checkout on each
	// generation of the settings, and returns the mover on the settings of
	// the move back, which it has not started.
	prepare func(t *testing.T, fixture moveFixture) moveBackStart
}

type moveBackStart struct {
	mover       *Repository
	moverConfig core.ProjectConfig
	// generations are checkouts set up before the move back, one per
	// generation of the settings, the mover's own excepted.
	generations []generationCheckout
	// settings are every generation of the settings, for fresh clones.
	settings map[string]core.TasksRemoteSetting
	// places are the bare repositories the tasks can be in, by name.
	places map[string]string
	// final is the place the move back ends at.
	final string
}

type generationCheckout struct {
	name       string
	repository *Repository
	config     core.ProjectConfig
}

func moveBackRoutes() []moveBackRoute {
	return []moveBackRoute{
		{name: "origin-tasks-origin", prepare: func(t *testing.T, fixture moveFixture) moveBackStart {
			tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
			first, _ := moveTo(t, fixture.first, tasks)
			onTasks, onTasksConfig := checkoutOn(t, fixture.origin, tasks)
			mover, moverConfig := pullSettings(t, first, core.TasksRemoteSetting{})
			return moveBackStart{
				mover: mover, moverConfig: moverConfig,
				generations: []generationCheckout{
					{name: "first settings", repository: fixture.second, config: fixture.stale},
					{name: "tasks settings", repository: onTasks, config: onTasksConfig},
				},
				settings: map[string]core.TasksRemoteSetting{"no tasks remote": {}, "tasks": tasks},
				places:   map[string]string{"origin": fixture.origin, "tasks": fixture.tasks},
				final:    "origin",
			}
		}},
		{name: "origin-tasks-third-tasks", prepare: func(t *testing.T, fixture moveFixture) moveBackStart {
			tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
			third := core.TasksRemoteSetting{Name: "third", URL: newBarePlace(t, testrepo.FormatSHA1, "third")}
			first, _ := moveTo(t, fixture.first, tasks)
			onTasks, onTasksConfig := checkoutOn(t, fixture.origin, tasks)
			first, _ = moveTo(t, first, third)
			onThird, onThirdConfig := checkoutOn(t, fixture.origin, third)
			mover, moverConfig := pullSettings(t, first, tasks)
			return moveBackStart{
				mover: mover, moverConfig: moverConfig,
				generations: []generationCheckout{
					{name: "first settings", repository: fixture.second, config: fixture.stale},
					{name: "tasks settings", repository: onTasks, config: onTasksConfig},
					{name: "third settings", repository: onThird, config: onThirdConfig},
				},
				settings: map[string]core.TasksRemoteSetting{"no tasks remote": {}, "tasks": tasks, "third": third},
				places:   map[string]string{"origin": fixture.origin, "tasks": fixture.tasks, "third": third.URL},
				final:    "tasks",
			}
		}},
	}
}

// No stop of a move back, on either route, leaves two places accepting a
// publish. At every stop, a checkout on every generation of the settings and
// a fresh clone on every generation (set up, then recording a new task) each
// try to publish, and at most one place takes any of it. Running the move
// again then finishes it, and the final place holds everything any place
// took.
func TestNoStopOfAMoveBackLeavesTwoPlacesAcceptingAPublish(t *testing.T) {
	t.Parallel()
	for _, route := range moveBackRoutes() {
		for _, step := range returnSteps {
			t.Run(route.name+"/"+step, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fixture := newMoveFixture(t, testrepo.FormatSHA1)
				start := route.prepare(t, fixture)
				start.mover.moveStepHook = func(at string) error {
					if at == step {
						return errMoveInterrupted
					}
					return nil
				}
				if _, err := start.mover.MoveTasks(ctx, start.moverConfig, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
					t.Fatalf("MoveTasks() = %v, want the interruption at %s", err, step)
				}
				start.mover.moveStepHook = nil

				var attempts []publishAttempt
				for _, generation := range start.generations {
					attempts = append(attempts, attemptPublish(t, generation.name, reopenRepository(t, generation.repository), generation.config))
				}
				names := make([]string, 0, len(start.settings))
				for name := range start.settings {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					clone, config, setupErr := freshSetup(t, fixture.origin, start.settings[name])
					attempt := attemptPublish(t, "fresh clone on "+name, clone, config)
					if setupErr != nil && attempt.err == nil {
						attempt.err = fmt.Errorf("setup refused (%v), but the publish after it was accepted", setupErr)
					}
					attempts = append(attempts, attempt)
				}
				accepted := acceptingPlaces(start.places, attempts)
				if len(accepted) > 1 {
					t.Fatalf("stopped at %s, %d places accepted a publish: %v\n%s", step, len(accepted), accepted, describeAttempts(attempts))
				}

				if _, err := start.mover.MoveTasks(ctx, start.moverConfig, MoveTasksOptions{}); err != nil {
					t.Fatalf("resumed MoveTasks() = %v", err)
				}
				for name, bare := range start.places {
					want := "moved:"
					if name == start.final {
						want = "returned"
					}
					if got := identityShape(t, bare); !strings.HasPrefix(got, want) {
						t.Fatalf("%s after the move = %s, want %s", name, got, want)
					}
				}
				for _, attempt := range attempts {
					for name, bare := range start.places {
						if placeHolds(bare, attempt.taskID) && !placeHolds(start.places[start.final], attempt.taskID) {
							t.Fatalf("%s's task reached %s and not %s, where the tasks are", attempt.who, name, start.final)
						}
					}
				}
			})
		}
	}
}

// A move back stopped between the mark and the return leaves both places
// marked, each naming the other, and is finished by running move-tasks again
// from any checkout, the mover's or one on any generation of the settings,
// set up before the stop or cloned fresh: the run moves the tasks to the
// place that checkout's settings name, and ends with that place alone live
// and holding every task.
func TestAMoveBackStoppedWithBothPlacesMarkedIsFinishedFromAnyCheckout(t *testing.T) {
	t.Parallel()
	for _, route := range moveBackRoutes() {
		for _, finisher := range []string{"mover", "generation 0", "generation 1", "generation 2", "fresh"} {
			t.Run(route.name+"/"+finisher, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fixture := newMoveFixture(t, testrepo.FormatSHA1)
				start := route.prepare(t, fixture)
				start.mover.moveStepHook = func(at string) error {
					if at == "return" {
						return errMoveInterrupted
					}
					return nil
				}
				if _, err := start.mover.MoveTasks(ctx, start.moverConfig, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
					t.Fatalf("MoveTasks() = %v, want the interruption", err)
				}
				for name, bare := range start.places {
					if got := identityShape(t, bare); !strings.HasPrefix(got, "moved:") {
						t.Fatalf("%s while stopped = %s, want marked", name, got)
					}
				}
				var checkout *Repository
				var config core.ProjectConfig
				switch finisher {
				case "mover":
					checkout, config = reopenRepository(t, start.mover), start.moverConfig
				case "fresh":
					// A fresh clone on the mover's settings: its setup is
					// refused, and move-tasks is what the refusal names.
					clone, cloneConfig, err := freshSetup(t, fixture.origin, start.settings[map[string]string{
						"origin-tasks-origin": "no tasks remote", "origin-tasks-third-tasks": "tasks"}[route.name]])
					requireMarkersInACircle(t, err)
					checkout, config = clone, cloneConfig
				default:
					index := int(finisher[len(finisher)-1] - '0')
					if index >= len(start.generations) {
						t.Skip("this route has no such generation")
					}
					generation := start.generations[index]
					checkout, config = reopenRepository(t, generation.repository), generation.config
					_, err := checkout.Sync(ctx, config)
					requireMarkersInACircle(t, err)
				}
				before := map[string]string{}
				for name, bare := range start.places {
					before[name] = refListing(t, bare, "refs/workbook/")
				}
				result, err := moveFollowingAdvice(t, checkout, config)
				if route.name == "origin-tasks-third-tasks" && finisher == "generation 0" {
					// The first settings name origin, which is neither place
					// of the move under way: that is a second move, and it is
					// refused while the markers lead in a circle, changing
					// nothing.
					if core.CategoryOf(err) != core.CategoryConflict || !strings.Contains(err.Error(), "leads back to") {
						t.Fatalf("MoveTasks() to origin while tasks and third name each other = %v, want refused", err)
					}
					for name, bare := range start.places {
						if after := refListing(t, bare, "refs/workbook/"); after != before[name] {
							t.Fatalf("the refused move changed %s", name)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("MoveTasks() from %s = %v", finisher, err)
				}
				live := result.To.Remote
				if live == core.DefaultTasksRemote {
					live = "origin"
				}
				for name, bare := range start.places {
					got := identityShape(t, bare)
					if name == live && got != "returned" {
						t.Fatalf("%s, where %s moved the tasks, = %s, want returned", name, finisher, got)
					}
					if name != live && !strings.HasPrefix(got, "moved:") {
						t.Fatalf("%s after %s finished the move = %s, want marked", name, finisher, got)
					}
				}
				requireSameTaskRefs(t, start.places[live], start.places[result.From.Remote])
				if _, err := checkout.Sync(ctx, config); err != nil {
					t.Fatalf("Sync() from %s after the move = %v", finisher, err)
				}
				requireEveryOperationOnce(t, checkout, config, start.places[live])
			})
		}
	}
}

// moveFollowingAdvice runs move-tasks, and while it is refused for a place
// this checkout has no remote for, adds the remote the refusal names, as it
// says to, and runs it again.
func moveFollowingAdvice(t *testing.T, checkout *Repository, config core.ProjectConfig) (MoveTasksResult, error) {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; ; attempt++ {
		result, err := checkout.MoveTasks(ctx, config, MoveTasksOptions{})
		if err == nil || attempt > 3 {
			return result, err
		}
		_, advice, found := strings.Cut(err.Error(), "add it with git remote add ")
		if !found {
			return result, err
		}
		fields := strings.Fields(advice)
		if len(fields) < 2 {
			t.Fatalf("refusal names no remote to add: %v", err)
		}
		syncGit(t, checkout.Root, "remote", "add", fields[0], fields[1])
		checkout = reopenRepository(t, checkout)
	}
}

// requireOneLivePlace requires exactly one of places to be live, every other
// one marked, and the live one to hold every task in ids. It returns the
// live place's name.
func requireOneLivePlace(t *testing.T, places map[string]string, ids []string) string {
	t.Helper()
	live := ""
	for name, bare := range places {
		if _, err := plainGit(bare, "rev-parse", "--verify", "--quiet", identityRef); err != nil {
			// Never written to: a place no move reached holds nothing.
			if refs := refListing(t, bare, taskRefPrefix); refs != "" {
				t.Fatalf("%s holds task refs and no identity: %s", name, refs)
			}
			continue
		}
		shape := identityShape(t, bare)
		if strings.HasPrefix(shape, "moved:") {
			continue
		}
		if live != "" {
			t.Fatalf("both %s and %s are live", live, name)
		}
		live = name
	}
	if live == "" {
		t.Fatal("no place is live")
	}
	for _, id := range ids {
		if !placeHolds(places[live], id) {
			t.Fatalf("%s, the live place, lacks task %s", live, id)
		}
	}
	return live
}

// A second person's move that starts while a move back is stopped, at any
// step, ends with one place live and every other marked: the second move is
// refused while both places of the first are marked, and otherwise moves the
// tasks from wherever the markers say they are, and the first move run again
// then finishes from there.
func TestASecondMoveDuringAStoppedMoveBackLeavesNoPlaceUnmarked(t *testing.T) {
	t.Parallel()
	for index, step := range returnSteps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)
			tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
			third := core.TasksRemoteSetting{Name: "third", URL: newBarePlace(t, testrepo.FormatSHA1, "third")}
			places := map[string]string{"origin": fixture.origin, "tasks": fixture.tasks, "third": third.URL}
			first, _ := moveTo(t, fixture.first, tasks)
			onTasks, _ := checkoutOn(t, fixture.origin, tasks)
			mover, moverConfig := pullSettings(t, first, core.TasksRemoteSetting{})
			mover.moveStepHook = func(at string) error {
				if at == step {
					return errMoveInterrupted
				}
				return nil
			}
			if _, err := mover.MoveTasks(ctx, moverConfig, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
				t.Fatalf("MoveTasks() = %v, want the interruption", err)
			}
			mover.moveStepHook = nil

			second, secondConfig := pullSettings(t, onTasks, third)
			_, err := moveFollowingAdvice(t, second, secondConfig)
			bothMarked := index > 2 && index <= 5
			switch {
			case bothMarked && (core.CategoryOf(err) != core.CategoryConflict || !strings.Contains(err.Error(), "leads back to")):
				t.Fatalf("second move while both places are marked = %v, want refused", err)
			case !bothMarked && err != nil:
				t.Fatalf("second move = %v", err)
			}
			if _, err := moveFollowingAdvice(t, reopenRepository(t, mover), moverConfig); err != nil {
				t.Fatalf("resumed move back = %v", err)
			}
			if live := requireOneLivePlace(t, places, fixture.ids); live != "origin" {
				t.Fatalf("the move back ended with %s live", live)
			}
		})
	}
}

// The mirror image: a different move started while a first move away from
// origin is stopped at any step. Each ends with one place live, every other
// marked, and every task there.
func TestADifferentMoveDuringAStoppedMoveLeavesNoPlaceUnmarked(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"read", "copy", "mark", "reread", "recopy", "verify"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)
			third := core.TasksRemoteSetting{Name: "third", URL: newBarePlace(t, testrepo.FormatSHA1, "third")}
			places := map[string]string{"origin": fixture.origin, "tasks": fixture.tasks, "third": third.URL}
			mover, moverConfig := pullSettings(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
			mover.moveStepHook = func(at string) error {
				if at == step {
					return errMoveInterrupted
				}
				return nil
			}
			if _, err := mover.MoveTasks(ctx, moverConfig, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
				t.Fatalf("MoveTasks() = %v, want the interruption", err)
			}
			mover.moveStepHook = nil

			second, secondConfig := pullSettings(t, fixture.second, third)
			if _, err := moveFollowingAdvice(t, second, secondConfig); err != nil {
				t.Fatalf("second move = %v", err)
			}
			if _, err := moveFollowingAdvice(t, reopenRepository(t, mover), moverConfig); err != nil {
				t.Fatalf("resumed first move = %v", err)
			}
			if live := requireOneLivePlace(t, places, fixture.ids); live != "tasks" {
				t.Fatalf("the resumed move ended with %s live", live)
			}
		})
	}
}

// A mirror of the code repository made after it was marked carries the
// marker, and is not therefore a place the tasks lived: move-tasks to it is
// refused for its branches, with the mirror warning, unless --allow-branches
// is passed, and nothing is written there. Origin itself, this checkout's
// code repository, is accepted without it.
func TestMoveTasksBackToAMirrorOfTheMarkedOriginNeedsAllowBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	first, _ := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
	mirror := t.TempDir() + "/mirror.git"
	syncGit(t, t.TempDir(), "clone", "--mirror", "--quiet", fixture.origin, mirror)
	if refs := refListing(t, mirror, "refs/heads/"); refs == "" {
		t.Fatal("the mirror holds no branches")
	}
	before := refListing(t, mirror, "refs/")
	mover, config := pullSettings(t, first, core.TasksRemoteSetting{Name: "mirror", URL: mirror})
	_, err := mover.MoveTasks(ctx, config, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryValidation || !strings.Contains(err.Error(), "holds branches") ||
		!strings.Contains(err.Error(), "Never pass it for a mirror") {
		t.Fatalf("MoveTasks() to a mirror of the marked origin = %v, want the branches refusal", err)
	}
	if after := refListing(t, mirror, "refs/"); after != before {
		t.Fatalf("the refused move wrote to the mirror:\n%s\n%s", before, after)
	}
	if got := identityShape(t, fixture.tasks); got != "identity" {
		t.Fatalf("tasks after the refused move = %s, want left live", got)
	}

	// Origin, which holds the branches this checkout's code comes from, is
	// accepted without the flag.
	mover, config = pullSettings(t, mover, core.TasksRemoteSetting{})
	if _, err := mover.MoveTasks(ctx, config, MoveTasksOptions{}); err != nil {
		t.Fatalf("MoveTasks() back to origin = %v", err)
	}
	if got := identityShape(t, fixture.origin); got != "returned" {
		t.Fatalf("origin after the move back = %s", got)
	}
}

// recordReturnByHand writes the return on a marked place without marking the
// place the tasks are in, which is the state an interrupted move back made
// before it marked first: both places live.
func recordReturnByHand(t *testing.T, checkout *Repository, remote, bare string) {
	t.Helper()
	ctx := context.Background()
	record, found, err := checkout.remoteIdentityRecord(ctx, remote)
	if err != nil || !found || record.Moved == nil {
		t.Fatalf("%s's identity = %+v, %v, %v; want marked", remote, record, found, err)
	}
	commit, err := checkout.writeTasksReturnedCommit(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	syncGit(t, checkout.Root, "push", "--quiet", bare, commit+":"+identityRef)
}

// Setup refuses a place the tasks came back to while the place they came
// back from still holds them unmarked, whether the settings name no tasks
// remote (origin is the place) or name one; and a hop this clone has no
// remote for is read at the address its marker records, or, when that cannot
// be read either, refuses with what to do rather than passing.
func TestSetupRefusesAPlaceTheTasksCameBackToWhileTheOtherIsLive(t *testing.T) {
	t.Parallel()
	t.Run("no tasks remote", func(t *testing.T) {
		t.Parallel()
		fixture := newMoveFixture(t, testrepo.FormatSHA1)
		first, _ := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
		recordReturnByHand(t, first, core.DefaultTasksRemote, fixture.origin)
		_, _, err := freshSetup(t, fixture.origin, core.TasksRemoteSetting{})
		refusal, found := core.TasksRemoteRefusalOf(err)
		if !found || refusal.Reason != core.TasksRemoteNotMoved || refusal.Command != "workbook move-tasks" ||
			!strings.Contains(err.Error(), "tasks ("+fixture.tasks+") still holds them") {
			t.Fatalf("setup on a returned origin while tasks is live = %v (%+v)", err, refusal)
		}
		before := refListing(t, fixture.origin, "refs/workbook/")
		if _, _, err := freshSetup(t, fixture.origin, core.TasksRemoteSetting{}); err == nil {
			t.Fatal("a second setup passed")
		}
		if after := refListing(t, fixture.origin, "refs/workbook/"); after != before {
			t.Fatalf("a refused setup published to origin:\n%s\n%s", before, after)
		}
	})
	for _, variant := range []string{"readable", "unreachable"} {
		t.Run("hop without a remote/"+variant, func(t *testing.T) {
			t.Parallel()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)
			tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
			thirdBare := newBarePlace(t, testrepo.FormatSHA1, "third")
			first, _ := moveTo(t, fixture.first, tasks)
			first, _ = moveTo(t, first, core.TasksRemoteSetting{Name: "third", URL: thirdBare})
			recordReturnByHand(t, first, "tasks", fixture.tasks)
			if variant == "unreachable" {
				// Third is gone: nothing answers at the address its marker
				// records.
				if err := os.RemoveAll(thirdBare); err != nil {
					t.Fatal(err)
				}
			}
			// A fresh clone on the tasks settings has origin and tasks, and no
			// remote named third.
			_, _, err := freshSetup(t, fixture.origin, tasks)
			if variant == "readable" {
				refusal, found := core.TasksRemoteRefusalOf(err)
				if !found || refusal.Reason != core.TasksRemoteNotMoved || !strings.Contains(err.Error(), "third ("+thirdBare+") still holds them") {
					t.Fatalf("setup on tasks while third is live = %v (%+v)", err, refusal)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "cannot read third ("+thirdBare+")") ||
				!strings.Contains(err.Error(), "workbook setup --no-sync") {
				t.Fatalf("setup on tasks while third cannot be read = %v, want refused with what to do", err)
			}
		})
	}
}

// CheckMoveDone, called directly, refuses settings that name a tasks remote
// the tasks never left while origin still holds them unmarked: before
// move-tasks ran, and after a run stopped before the mark. Once origin is
// marked as moved there it passes, whether or not the run went on to finish,
// and a tasks remote marked as moved itself is not its check.
func TestCheckMoveDoneRefusesUntilThePlaceLeftIsMarked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
	mover, config := pullSettings(t, fixture.first, tasks)
	requireNotMoved := func(when string) {
		t.Helper()
		err := reopenRepository(t, mover).CheckMoveDone(ctx)
		refusal, found := core.TasksRemoteRefusalOf(err)
		if !found || refusal.Reason != core.TasksRemoteNotMoved || refusal.Command != "workbook move-tasks" ||
			refusal.Remote != "tasks" || !strings.Contains(err.Error(), "origin ("+fixture.origin+") still holds them") {
			t.Fatalf("CheckMoveDone() %s = %v (%+v), want the not-moved refusal naming origin", when, err, refusal)
		}
	}
	requireNotMoved("before move-tasks ran")

	for _, step := range []string{"copy", "reread"} {
		mover.moveStepHook = func(at string) error {
			if at == step {
				return errMoveInterrupted
			}
			return nil
		}
		if _, err := mover.MoveTasks(ctx, config, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
			t.Fatalf("MoveTasks() stopped at %s = %v, want the interruption", step, err)
		}
		mover.moveStepHook = nil
		if step == "copy" {
			requireNotMoved("after a move stopped before the mark")
			continue
		}
		if err := reopenRepository(t, mover).CheckMoveDone(ctx); err != nil {
			t.Fatalf("CheckMoveDone() after a move stopped past the mark = %v", err)
		}
	}
	if _, err := mover.MoveTasks(ctx, config, MoveTasksOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reopenRepository(t, mover).CheckMoveDone(ctx); err != nil {
		t.Fatalf("CheckMoveDone() after the move = %v", err)
	}

	// The tasks go back to origin, and a checkout still on the tasks
	// settings finds tasks marked: every command refuses that with where the
	// tasks are, so CheckMoveDone leaves it alone.
	back, _ := moveTo(t, mover, core.TasksRemoteSetting{})
	stale, _ := pullSettings(t, back, tasks)
	if err := stale.CheckMoveDone(ctx); err != nil {
		t.Fatalf("CheckMoveDone() on a tasks remote marked as moved = %v", err)
	}
}

// From a move back stopped with both places marked, each naming the other, a
// checkout whose settings name a third place is refused by move-tasks, told
// that the move between origin and tasks is unfinished and to finish it
// first, with no --from to try; and --from naming either marked place is
// refused before anything is written, so the third place is left with no
// identity and no tasks.
func TestAMoveFromAStoppedMoveBackWritesNothingToAThirdPlace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
	third := core.TasksRemoteSetting{Name: "third", URL: newBarePlace(t, testrepo.FormatSHA1, "third")}
	first, _ := moveTo(t, fixture.first, tasks)
	onTasks, _ := checkoutOn(t, fixture.origin, tasks)
	mover, moverConfig := pullSettings(t, first, core.TasksRemoteSetting{})
	mover.moveStepHook = func(at string) error {
		if at == "return" {
			return errMoveInterrupted
		}
		return nil
	}
	if _, err := mover.MoveTasks(ctx, moverConfig, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
		t.Fatalf("MoveTasks() = %v, want the interruption", err)
	}

	second, secondConfig := pullSettings(t, onTasks, third)
	_, err := second.MoveTasks(ctx, secondConfig, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryConflict ||
		!strings.Contains(err.Error(), "a move between origin and tasks is unfinished") ||
		!strings.Contains(err.Error(), "Finish it first with workbook move-tasks") ||
		strings.Contains(err.Error(), "--from") {
		t.Fatalf("move to third = %v, want the unfinished move named and no --from", err)
	}
	for _, from := range []string{"origin", "tasks"} {
		_, err := reopenRepository(t, second).MoveTasks(ctx, secondConfig, MoveTasksOptions{From: from})
		if core.CategoryOf(err) != core.CategoryConflict || !strings.Contains(err.Error(), from+" is marked as moved to") {
			t.Fatalf("move to third --from %s = %v, want refused for its marker", from, err)
		}
	}
	if _, err := plainGit(third.URL, "rev-parse", "--verify", "--quiet", identityRef); err == nil {
		t.Fatalf("third holds an identity ref")
	}
	if refs := refListing(t, third.URL, "refs/"); refs != "" {
		t.Fatalf("third holds refs: %s", refs)
	}
}
