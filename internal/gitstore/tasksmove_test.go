package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
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
