package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// moveFixture is a project on origin with two clones and published tasks, and
// an empty bare repository for its tasks to move to. first is the clone that
// chose the new remote; second is a teammate still on the old settings.
type moveFixture struct {
	first, second *Repository
	// config is first's project after it chose the new remote, and stale is
	// the project as second still reads it.
	config, stale core.ProjectConfig
	origin, tasks string
	ids           []string
}

func newMoveFixture(t *testing.T, objectFormat string) moveFixture {
	t.Helper()
	ctx := context.Background()
	var first, second *Repository
	var config core.ProjectConfig
	if objectFormat == testrepo.FormatSHA1 {
		first, second, config = syncRepositories(t)
	} else {
		first, second, config = syncRepositoriesWithObjectFormat(t, objectFormat)
	}
	fixture := moveFixture{first: first, second: second, config: config, stale: config}
	fixture.origin = syncGit(t, first.Root, "remote", "get-url", "origin")
	for _, title := range []string{"Shared before the move", "Also shared before the move"} {
		fixture.ids = append(fixture.ids, createSyncTask(t, first, config, title).ID)
	}
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	fixture.tasks = filepath.Join(t.TempDir(), "tasks.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", "--object-format="+objectFormat, fixture.tasks)
	syncGit(t, fixture.tasks, "config", "receive.autogc", "false")
	syncGit(t, fixture.tasks, "config", "gc.auto", "0")

	updated, err := first.SetProjectTasksRemote(ctx, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
	if err != nil {
		t.Fatal(err)
	}
	fixture.config = updated
	return fixture
}

// refListing is every ref under prefix in a bare repository, name and commit.
func refListing(t *testing.T, bare, prefix string) string {
	t.Helper()
	return syncGit(t, bare, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
}

var errMoveInterrupted = errors.New("interrupted")

// A move stopped before any of its steps, and resumed by running it again,
// leaves the new place holding exactly the refs the old place holds — every
// task ref and the configuration ledger, at the same commits, so every
// operation keeps its ID — and the old place marked. Until the mark, the
// teammate still on the old settings publishes to the old place and that work
// is carried across by the resumed run; from the mark on, the teammate is
// refused before recording anything.
func TestMoveTasksResumesAfterAnInterruptionAtEveryStep(t *testing.T) {
	t.Parallel()
	steps := []string{"read", "copy", "mark", "reread", "recopy", "verify"}
	for index, step := range steps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)

			fixture.first.moveStepHook = func(at string) error {
				if at == step {
					return errMoveInterrupted
				}
				return nil
			}
			if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
				t.Fatalf("MoveTasks() error = %v, want the interruption at %s", err, step)
			}
			fixture.first.moveStepHook = nil

			marked := index > 2
			// The teammate on the old settings edits a task and tries to
			// publish it to the old place.
			updateSyncTask(t, fixture.second, fixture.stale, fixture.ids[0], "Edited by the teammate during the move")
			_, pushErr := fixture.second.Sync(ctx, fixture.stale)
			if marked {
				refusal, found := core.TasksRemoteRefusalOf(pushErr)
				if !found || refusal.Reason != core.TasksRemoteMoved || refusal.MovedTo == nil ||
					refusal.MovedTo.URL != fixture.tasks {
					t.Fatalf("teammate sync after the mark = %v, want the tasks-moved refusal naming %s", pushErr, fixture.tasks)
				}
				if core.CategoryOf(pushErr) != core.CategoryNotInitialized {
					t.Fatalf("refusal category = %q, want not-initialized", core.CategoryOf(pushErr))
				}
			} else if pushErr != nil {
				t.Fatalf("teammate sync before the mark = %v, want it published to the old place", pushErr)
			}

			result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
			if err != nil {
				t.Fatalf("resumed MoveTasks() = %v", err)
			}
			if result.Tasks != len(fixture.ids) || result.Identical != len(fixture.ids) || result.Carried != 0 {
				t.Fatalf("resumed result = %+v, want %d identical tasks", result, len(fixture.ids))
			}
			wantMarked := "marked"
			if marked {
				wantMarked = "already"
			}
			if result.Marked != wantMarked {
				t.Fatalf("resumed Marked = %q, want %q", result.Marked, wantMarked)
			}

			assertSameRefs(t, fixture)
			if !marked {
				// The edit the teammate published before the mark reached the
				// new place.
				head := syncGit(t, fixture.origin, "rev-parse", taskRefPrefix+fixture.ids[0])
				if got := syncGit(t, fixture.tasks, "rev-parse", taskRefPrefix+fixture.ids[0]); got != head {
					t.Fatalf("tasks holds %s at %s, want the teammate's edit %s", fixture.ids[0], got, head)
				}
			}
			assertOriginMarked(t, fixture)

			if marked {
				// The teammate pulls the settings and sets up again; the
				// edit the refusal kept is published to the new place.
				if _, err := fixture.second.SetProjectTasksRemote(ctx, fixture.config.TasksRemote); err != nil {
					t.Fatal(err)
				}
				teammate, err := Open(ctx, fixture.second.Root)
				if err != nil {
					t.Fatal(err)
				}
				config, err := teammate.LoadConfig()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := teammate.EnsureTasksRemote(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := teammate.Sync(ctx, config); err != nil {
					t.Fatalf("teammate sync after setup = %v", err)
				}
				local := syncGit(t, teammate.Root, "rev-parse", taskRefPrefix+fixture.ids[0])
				if got := syncGit(t, fixture.tasks, "rev-parse", taskRefPrefix+fixture.ids[0]); got != local {
					t.Fatalf("tasks holds %s at %s, want the teammate's kept edit %s", fixture.ids[0], got, local)
				}
				if err := teammate.CheckTasksRemote(ctx); err != nil {
					t.Fatalf("CheckTasksRemote after setup = %v", err)
				}
			}
			assertVerifyNamesWhatTheNewPlaceLacks(t, fixture)
		})
	}
}

// assertVerifyNamesWhatTheNewPlaceLacks takes from the new place, just before
// a run verifies it, the whole of the second task and, when the first task's
// newest operation is one the old place holds too, that operation, and
// requires the run to fail naming them; then a rerun puts them back and
// verifies. A move whose verification passed whatever the new place held
// fails here.
func assertVerifyNamesWhatTheNewPlaceLacks(t *testing.T, fixture moveFixture) {
	t.Helper()
	ctx := context.Background()
	rewound, deleted := taskRefPrefix+fixture.ids[0], taskRefPrefix+fixture.ids[1]
	tip := syncGit(t, fixture.tasks, "rev-parse", rewound)
	rewind := tip == syncGit(t, fixture.origin, "rev-parse", rewound) &&
		len(strings.Fields(syncGit(t, fixture.tasks, "rev-list", "--parents", "-n1", tip))) == 2
	var lost string
	fixture.first.moveStepHook = func(at string) error {
		if at != "verify" {
			return nil
		}
		syncGit(t, fixture.tasks, "update-ref", "-d", deleted)
		if !rewind {
			return nil
		}
		var pack struct {
			Operations []struct {
				ID string `json:"id"`
			} `json:"operations"`
		}
		if err := json.Unmarshal([]byte(syncGit(t, fixture.tasks, "cat-file", "blob", tip+":operation.json")), &pack); err != nil {
			t.Fatal(err)
		}
		lost = pack.Operations[0].ID
		syncGit(t, fixture.tasks, "update-ref", rewound, tip+"^", tip)
		return nil
	}
	_, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	fixture.first.moveStepHook = nil
	if err == nil || core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("MoveTasks() with operations missing at the new place = %v, want the verification to fail", err)
	}
	wants := []string{fixture.ids[1] + " (absent)"}
	if rewind {
		wants = append(wants, fixture.ids[0]+" (lacks operation(s) "+lost+")")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("verification error %q does not name %q", err, want)
		}
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if err != nil || !result.Verified || result.Tasks != len(fixture.ids) {
		t.Fatalf("MoveTasks() after the loss = (%+v, %v), want it put back and verified", result, err)
	}
	if got := syncGit(t, fixture.tasks, "rev-parse", rewound); got != tip {
		t.Fatalf("tasks holds %s at %s after the rerun, want %s back", rewound, got, tip)
	}
	if got := syncGit(t, fixture.tasks, "rev-parse", deleted); got != syncGit(t, fixture.origin, "rev-parse", deleted) {
		t.Fatalf("tasks holds %s at %s after the rerun, want origin's commit", deleted, got)
	}
}

// assertSameRefs requires the two places to hold the same task refs and the
// same configuration ledger, name for name and commit for commit.
// An edit a teammate on the old settings publishes to the old place while one
// uninterrupted move is under way, after the move read it and before the mark,
// reaches the new place in that same run: the move reads the old place again
// after marking it, so nothing published before the mark is left behind.
func TestMoveTasksCarriesAnEditPublishedBeforeTheMarkInTheSameRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)

	fixture.first.moveStepHook = func(at string) error {
		if at != "mark" {
			return nil
		}
		updateSyncTask(t, fixture.second, fixture.stale, fixture.ids[0], "Edited by the teammate while the move runs")
		if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
			t.Fatalf("teammate sync before the mark = %v, want it published to the old place", err)
		}
		return nil
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if err != nil {
		t.Fatalf("MoveTasks() = %v, want the teammate's edit carried across", err)
	}
	if result.Marked != "marked" || result.Identical != len(fixture.ids) {
		t.Fatalf("MoveTasks() result = %+v, want %d identical tasks and origin marked", result, len(fixture.ids))
	}
	head := syncGit(t, fixture.origin, "rev-parse", taskRefPrefix+fixture.ids[0])
	if got := syncGit(t, fixture.tasks, "rev-parse", taskRefPrefix+fixture.ids[0]); got != head {
		t.Fatalf("tasks holds %s at %s, want the teammate's edit %s", fixture.ids[0], got, head)
	}
	assertSameRefs(t, fixture)
	assertOriginMarked(t, fixture)
}

func assertSameRefs(t *testing.T, fixture moveFixture) {
	t.Helper()
	for _, prefix := range []string{taskRefPrefix, configRef} {
		source, destination := refListing(t, fixture.origin, prefix), refListing(t, fixture.tasks, prefix)
		if source != destination || (prefix == taskRefPrefix && source == "") {
			t.Fatalf("%s differ:\norigin: %s\ntasks:  %s", prefix, source, destination)
		}
	}
}

// assertOriginMarked requires origin's identity ref to be the marker commit:
// a child of the new place's identity commit carrying the same project.json
// and a moved.json naming the new place.
func assertOriginMarked(t *testing.T, fixture moveFixture) {
	t.Helper()
	identity := syncGit(t, fixture.tasks, "rev-parse", identityRef)
	marker := syncGit(t, fixture.origin, "rev-parse", identityRef)
	if marker == identity {
		t.Fatal("origin's identity ref is unmarked")
	}
	if parents := syncGit(t, fixture.origin, "rev-list", "--parents", "-n1", marker); parents != marker+" "+identity {
		t.Fatalf("marker parents = %q, want the identity commit %s", parents, identity)
	}
	if got, want := syncGit(t, fixture.origin, "rev-parse", marker+":"+identityDocumentPath),
		syncGit(t, fixture.tasks, "rev-parse", identity+":"+identityDocumentPath); got != want {
		t.Fatalf("marker project.json = %s, want the identity's %s", got, want)
	}
	document := syncGit(t, fixture.origin, "cat-file", "blob", marker+":"+tasksMovedDocumentPath)
	if !strings.Contains(document, `"format":"workbook.tasks-moved"`) || !strings.Contains(document, `"remote":"tasks"`) ||
		!strings.Contains(document, fixture.tasks) {
		t.Fatalf("moved.json = %s", document)
	}
}

// The marker never becomes the local identity: a checkout that reads the old
// place keeps its identity ref on the identity commit, and a local identity
// ref moved onto a marker by hand is refused rather than read.
func TestMoveMarkerIsNeverAdoptedLocally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil {
		t.Fatal(err)
	}
	before := syncGit(t, fixture.second.Root, "rev-parse", identityRef)
	if _, err := fixture.second.Fetch(ctx, fixture.stale); err == nil {
		t.Fatal("stale fetch succeeded")
	}
	if after := syncGit(t, fixture.second.Root, "rev-parse", identityRef); after != before {
		t.Fatalf("stale fetch moved the identity ref from %s to %s", before, after)
	}
	marker := syncGit(t, fixture.origin, "rev-parse", identityRef)
	syncGit(t, fixture.second.Root, "update-ref", identityRef, marker)
	reopened, err := Open(ctx, fixture.second.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadConfig(); core.CategoryOf(err) != core.CategoryCorruptData ||
		!strings.Contains(err.Error(), "tasks-moved marker") {
		t.Fatalf("LoadConfig() with a marker as the identity = %v, want corrupt-data naming the marker", err)
	}
}

// A remote named with --remote that is marked can be read and is never
// published to.
func TestMovedRemoteNamedExplicitlyIsReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, fixture.first.Root)
	if err != nil {
		t.Fatal(err)
	}
	config, err := reopened.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.UseRemote(ctx, "origin"); err != nil {
		t.Fatal(err)
	}
	fetched, err := reopened.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(--remote origin) = %v, want a read-only read", err)
	}
	if fetched.Status != SyncPhaseCompleted {
		t.Fatalf("fetch status = %q", fetched.Status)
	}
	before := refListing(t, fixture.origin, "refs/")
	createSyncTask(t, reopened, config, "Not for the old place")
	_, err = reopened.Push(ctx, config)
	if core.CategoryOf(err) != core.CategoryValidation || !strings.Contains(err.Error(), "is not this project's tasks remote") {
		t.Fatalf("Push(--remote origin) = %v, want the validation refusal to publish to another remote", err)
	}
	run, err := reopened.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync(--remote origin) = %v, want a read that publishes nothing", err)
	}
	if run.Identity == nil || run.Identity.Status != SyncIdentityMoved {
		t.Fatalf("Sync identity = %+v, want moved", run.Identity)
	}
	if run.Push.Status != SyncPhaseSkipped {
		t.Fatalf("Sync push = %+v, want skipped", run.Push)
	}
	if after := refListing(t, fixture.origin, "refs/"); after != before {
		t.Fatalf("origin changed after the refused publications:\nbefore %s\nafter  %s", before, after)
	}
}

// A fetch prunes only its own remote's tracking namespace.
func TestFetchPrunesOnlyItsOwnRemotesTrackingRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	if _, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, fixture.first.Root)
	if err != nil {
		t.Fatal(err)
	}
	config, err := reopened.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	gone := fixture.ids[1]
	syncGit(t, fixture.tasks, "update-ref", "-d", taskRefPrefix+gone)
	originTracking := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("origin"))
	if !strings.Contains(originTracking, gone) {
		t.Fatalf("origin's tracking refs lack %s before the fetch: %s", gone, originTracking)
	}
	if _, err := reopened.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	if tracking := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("tasks")); strings.Contains(tracking, gone) {
		t.Fatalf("fetch from tasks kept the pruned %s: %s", gone, tracking)
	}
	if after := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("origin")); after != originTracking {
		t.Fatalf("fetch from tasks changed origin's tracking refs:\nbefore %s\nafter  %s", originTracking, after)
	}

	// And the other way: a fetch from origin prunes origin's namespace only.
	syncGit(t, fixture.origin, "update-ref", "-d", taskRefPrefix+fixture.ids[0])
	tasksTracking := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("tasks"))
	if err := reopened.UseRemote(ctx, "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	if after := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("origin")); strings.Contains(after, fixture.ids[0]) {
		t.Fatalf("fetch from origin kept the pruned %s: %s", fixture.ids[0], after)
	}
	if after := refListing(t, fixture.first.Root, remoteTaskRefPrefixFor("tasks")); after != tasksTracking {
		t.Fatalf("fetch from origin changed tasks' tracking refs:\nbefore %s\nafter  %s", tasksTracking, after)
	}
}

// The move works the same in a SHA-256 project.
func TestMoveTasksInSHA256Repositories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA256)
	// A second operation on the first task, so verification has an
	// operation, not only a task, to find missing.
	updateSyncTask(t, fixture.second, fixture.stale, fixture.ids[0], "Edited before the move")
	if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Identical != len(fixture.ids) {
		t.Fatalf("result = %+v", result)
	}
	assertSameRefs(t, fixture)
	assertOriginMarked(t, fixture)
	if marker := syncGit(t, fixture.origin, "rev-parse", identityRef); len(marker) != 64 {
		t.Fatalf("marker %q is not a SHA-256 object ID", marker)
	}
	assertVerifyNamesWhatTheNewPlaceLacks(t, fixture)
}

// moveRelics gives the fixture's old place two relics, written by the teammate
// still on the old settings. The clone that moves holds the first at its root
// only, so the change the teammate then publishes to it, and the whole of the
// second, are ones it has not fetched.
func moveRelics(t *testing.T, fixture moveFixture) []string {
	t.Helper()
	ctx := context.Background()
	first := writeRelicRootNumbered(t, fixture.second, fixture.stale, 1, relicTestPartNumbered(1))
	if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
		t.Fatal(err)
	}
	ref := relicRef(first.Record.State.RelicID)
	syncGit(t, fixture.first.Root, "fetch", "--quiet", fixture.origin, ref+":"+ref)
	writeRelicChild(t, fixture.second, fixture.stale, first, nil, relicTitleSet(610001, "Changed at the old place"))
	second := writeRelicRootNumbered(t, fixture.second, fixture.stale, 2, relicTestPartNumbered(2))
	if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
		t.Fatal(err)
	}
	return []string{first.Record.State.RelicID, second.Record.State.RelicID}
}

// assertSameRelics requires the two places to hold the same relic refs, name
// for name and commit for commit, and at least one.
func assertSameRelics(t *testing.T, fixture moveFixture) {
	t.Helper()
	source, destination := refListing(t, fixture.origin, relicRefPrefix), refListing(t, fixture.tasks, relicRefPrefix)
	if source != destination || source == "" {
		t.Fatalf("relic refs differ:\norigin: %s\ntasks:  %s", source, destination)
	}
}

// Moving the tasks moves the relics: the new place ends holding every relic
// the old place does, at the same commits, including a change this checkout
// had not fetched, and the verification counts them.
func TestMoveTasksCarriesRelics(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, objectFormat)
			moveRelics(t, fixture)
			result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
			if err != nil {
				t.Fatalf("MoveTasks() = %v", err)
			}
			if !result.Verified || result.Relics != 2 || result.RelicsIdentical != 2 || result.RelicsCarried != 0 {
				t.Fatalf("MoveTasks() result = %+v, want two identical relics, verified", result)
			}
			assertSameRelics(t, fixture)
			assertSameRefs(t, fixture)
		})
	}
}

// A relic change a teammate on the old settings publishes to the old place
// while one move is under way, before the mark, reaches the new place in that
// same run.
func TestMoveTasksCarriesARelicPublishedBeforeTheMarkInTheSameRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relics := moveRelics(t, fixture)
	fixture.first.moveStepHook = func(at string) error {
		if at != "mark" {
			return nil
		}
		tip, err := fixture.second.GetRelic(ctx, fixture.stale, relics[0])
		if err != nil {
			t.Fatal(err)
		}
		writeRelicChild(t, fixture.second, fixture.stale, tip, nil, relicTitleSet(610002, "Changed while the move runs"))
		if _, err := fixture.second.Sync(ctx, fixture.stale); err != nil {
			t.Fatalf("teammate sync before the mark = %v, want it published to the old place", err)
		}
		return nil
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if err != nil {
		t.Fatalf("MoveTasks() = %v, want the teammate's relic change carried across", err)
	}
	if !result.Verified || result.Relics != 2 || result.RelicsIdentical != 2 {
		t.Fatalf("MoveTasks() result = %+v, want two identical relics", result)
	}
	head := syncGit(t, fixture.origin, "rev-parse", relicRef(relics[0]))
	if got := syncGit(t, fixture.tasks, "rev-parse", relicRef(relics[0])); got != head {
		t.Fatalf("tasks holds %s at %s, want the teammate's change %s", relics[0], got, head)
	}
	assertSameRelics(t, fixture)
	assertOriginMarked(t, fixture)
}

// A new place that lacks a relic the old place holds fails the verification,
// naming the relic, and a rerun puts it back.
func TestMoveVerificationNamesARelicTheNewPlaceLacks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relics := moveRelics(t, fixture)
	fixture.first.moveStepHook = func(at string) error {
		if at == "verify" {
			syncGit(t, fixture.tasks, "update-ref", "-d", relicRef(relics[1]))
		}
		return nil
	}
	result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	fixture.first.moveStepHook = nil
	want := "tasks does not hold everything origin holds: relic " + relics[1] + " (lacks 1 change(s)); run workbook move-tasks again"
	if core.CategoryOf(err) != core.CategoryConflict || err == nil || !strings.Contains(err.Error(), want) || result.Verified {
		t.Fatalf("MoveTasks() with a relic missing at the new place = (%+v, %v), want the verification to fail with %q", result, err, want)
	}
	result, err = fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
	if err != nil || !result.Verified || result.Relics != 2 {
		t.Fatalf("MoveTasks() after the loss = (%+v, %v), want it put back and verified", result, err)
	}
	assertSameRelics(t, fixture)
}

// notThisProjectsRefs are refs under the task and relic namespaces whose names
// are not one of this project's task or relic IDs: another key's task and
// relic, names that fit no project, and nested names, each pointing at one of
// this project's commits so that only its name tells it apart.
func notThisProjectsRefs(fixture moveFixture, relicID string) map[string]string {
	_, taskBody, _ := strings.Cut(fixture.ids[0], "-")
	_, relicBody, _ := core.ParseRelicID(relicID)
	return map[string]string{
		taskRef("OTHER-" + taskBody):                  taskRef(fixture.ids[0]),
		taskRef("notes"):                              taskRef(fixture.ids[0]),
		taskRef("nested/" + fixture.ids[0]):           taskRef(fixture.ids[0]),
		relicRef(core.NewRelicID("OTHER", relicBody)): relicRef(relicID),
		relicRef("not-a-relic"):                       relicRef(relicID),
		relicRef("nested/" + relicID):                 relicRef(relicID),
	}
}

// writeNotThisProjectsRefs writes notThisProjectsRefs at the old place and
// returns them by name, at the commit each points at.
func writeNotThisProjectsRefs(t *testing.T, fixture moveFixture, relicID string) map[string]string {
	t.Helper()
	written := map[string]string{}
	for name, target := range notThisProjectsRefs(fixture, relicID) {
		tip := syncGit(t, fixture.origin, "rev-parse", target)
		syncGit(t, fixture.origin, "update-ref", name, tip)
		written[name] = tip
	}
	return written
}

// requireHeldAt requires bare to hold every ref in refs at its commit.
func requireHeldAt(t *testing.T, bare string, refs map[string]string) {
	t.Helper()
	for name, tip := range refs {
		if got := refListing(t, bare, name); got != name+" "+tip {
			t.Fatalf("%s holds %q for %s, want it at %s", bare, got, name, tip)
		}
	}
}

// A move concerns this project's refs only. A ref under the task or relic
// namespace at the old place whose name is not one of this project's task or
// relic IDs is left where it is: the move verifies, does not count it, does
// not carry it, and its error-free result names nothing about it, run after
// run.
func TestMoveVerificationLeavesRefsThatAreNotThisProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relics := moveRelics(t, fixture)
	left := writeNotThisProjectsRefs(t, fixture, relics[0])
	for round := 0; round < 2; round++ {
		result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
		if err != nil || !result.Verified || result.Tasks != len(fixture.ids) || result.Relics != len(relics) {
			t.Fatalf("MoveTasks() run %d with refs that are not this project's at the old place = (%+v, %v), "+
				"want %d tasks and %d relics verified", round, result, err, len(fixture.ids), len(relics))
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for name := range left {
			if strings.Contains(string(encoded), strings.TrimPrefix(strings.TrimPrefix(name, taskRefPrefix), relicRefPrefix)) {
				t.Fatalf("MoveTasks() run %d result names %s: %s", round, name, encoded)
			}
			if got := refListing(t, fixture.tasks, name); got != "" {
				t.Fatalf("tasks holds %s after the move: %s", name, got)
			}
		}
		requireHeldAt(t, fixture.origin, left)
	}
}

// One of this project's own refs that the new place lacks still leaves the
// move unverified when refs that are not this project's sit beside it, and
// the refusal names only the one that is.
func TestMoveVerificationNamesOnlyThisProjectsRefTheNewPlaceLacks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relics := moveRelics(t, fixture)
	left := writeNotThisProjectsRefs(t, fixture, relics[0])
	for _, lost := range []string{taskRef(fixture.ids[1]), relicRef(relics[1])} {
		fixture.first.moveStepHook = func(at string) error {
			if at == "verify" {
				syncGit(t, fixture.tasks, "update-ref", "-d", lost)
			}
			return nil
		}
		result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{})
		fixture.first.moveStepHook = nil
		want := "tasks does not hold everything origin holds: " + fixture.ids[1] + " (absent); run workbook move-tasks again"
		if lost == relicRef(relics[1]) {
			want = "tasks does not hold everything origin holds: relic " + relics[1] + " (lacks 1 change(s)); run workbook move-tasks again"
		}
		if core.CategoryOf(err) != core.CategoryConflict || err == nil || err.Error() != want || result.Verified {
			t.Fatalf("MoveTasks() with %s missing at the new place = (%+v, %v), want the verification to fail with exactly %q",
				lost, result, err, want)
		}
		requireHeldAt(t, fixture.origin, left)
	}
	if result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil || !result.Verified {
		t.Fatalf("MoveTasks() after the losses = (%+v, %v), want them put back and verified", result, err)
	}
}

// The clean-up after a move plans to delete this project's task and relic
// refs at the place left, and every other Workbook ref there but its marker,
// and leaves the refs that are not this project's out of the plan: they are
// not deleted, not bundled, not counted, and not named as kept. When the host
// refuses a deletion, what the refusal says remains is only what the plan was
// to delete.
func TestMoveCleanUpPlanLeavesRefsThatAreNotThisProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	relics := moveRelics(t, fixture)
	left := writeNotThisProjectsRefs(t, fixture, relics[0])
	if result, err := fixture.first.MoveTasks(ctx, fixture.config, MoveTasksOptions{}); err != nil || !result.Verified {
		t.Fatalf("MoveTasks() = (%+v, %v)", result, err)
	}
	plan, err := fixture.first.PlanMoveCleanUp(ctx, fixture.config, "origin", "workbook move-tasks --clean-up")
	if err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, ref := range plan.Refs {
		planned = append(planned, ref.Ref)
	}
	sort.Strings(planned)
	// This fixture's project has no configuration ledger, so its task and
	// relic refs are all the plan deletes.
	var want []string
	for _, relicID := range relics {
		want = append(want, relicRef(relicID))
	}
	for _, id := range fixture.ids {
		want = append(want, taskRef(id))
	}
	sort.Strings(want)
	if !reflect.DeepEqual(planned, want) || plan.Tasks != len(fixture.ids) ||
		!reflect.DeepEqual(plan.Kept, []string{identityRef}) || plan.Live() {
		t.Fatalf("PlanMoveCleanUp() = %+v, refs %v; want refs %v, %d tasks, the marker kept", plan, planned, want, len(fixture.ids))
	}

	testenv.WriteExecutable(t, filepath.Join(fixture.origin, "hooks", "update"), []byte("#!/bin/sh\n"+
		"case \"$3\" in *[!0]*) exit 0 ;; esac\n"+
		"echo \"rule: no deletions\" >&2\n"+
		"exit 1\n"))
	_, err = fixture.first.ResetRemote(ctx, fixture.config.ProjectID, plan, ResetOptions{NoBackup: true})
	var refusal *ResetRefusalError
	if !errors.As(err, &refusal) || refusal.Refusal.Reason != ResetDeletionRefused || !reflect.DeepEqual(refusal.Refusal.Remaining, want) {
		t.Fatalf("ResetRemote() with deletions refused = %v, want a refusal naming only %v remaining", err, want)
	}
	for name := range left {
		if strings.Contains(err.Error(), name) {
			t.Fatalf("ResetRemote() refusal names %s: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(fixture.origin, "hooks", "update")); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.first.ResetRemote(ctx, fixture.config.ProjectID, plan, ResetOptions{NoBackup: true})
	if err != nil || !reflect.DeepEqual(sorted(result.Removed), want) {
		t.Fatalf("ResetRemote() = (%+v, %v), want %v removed", result, err, want)
	}
	requireHeldAt(t, fixture.origin, left)
	var remaining []string
	for _, line := range strings.Split(refListing(t, fixture.origin, "refs/workbook/"), "\n") {
		name, _, _ := strings.Cut(line, " ")
		remaining = append(remaining, name)
	}
	wantRemaining := []string{identityRef}
	for name := range left {
		wantRemaining = append(wantRemaining, name)
	}
	if sort.Strings(remaining); !reflect.DeepEqual(remaining, sorted(wantRemaining)) {
		t.Fatalf("origin holds %v after the clean-up, want %v", remaining, sorted(wantRemaining))
	}
}

func sorted(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return values
}

// A shortfall under keys only the old place's configuration has names each
// key once, in order, and the commands that get it here: key add, and key
// retire again for one the old place retired.
func TestMoveVerificationErrorNamesKeysOnlyTheOldPlaceHas(t *testing.T) {
	t.Parallel()
	err := moveVerificationError("origin", "tasks", []string{"CC-1 (absent)", "relic EE-R-1 (lacks 1 change(s))"},
		[]unseenKey{{key: "CC"}, {key: "EE", retired: true}})
	want := "tasks does not hold everything origin holds: CC-1 (absent); relic EE-R-1 (lacks 1 change(s)); " +
		"origin's configuration has project keys CC, EE, which this checkout's does not because the two configurations have diverged, " +
		"so nothing under them was carried; run workbook key add CC, then workbook key add EE, then workbook key retire EE here, " +
		"then workbook move-tasks again"
	if err == nil || err.Error() != want || core.ExitCode(err) != 8 {
		t.Fatalf("moveVerificationError = %v (exit %d), want %q", err, core.ExitCode(err), want)
	}
	if err := moveVerificationError("origin", "tasks", []string{"AA-1 (absent)"}, nil); err == nil ||
		err.Error() != "tasks does not hold everything origin holds: AA-1 (absent); run workbook move-tasks again" {
		t.Fatalf("moveVerificationError with no unseen key = %v", err)
	}
}
