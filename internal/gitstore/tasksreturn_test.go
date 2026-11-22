package gitstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// returnSteps are the steps of a move back to a place the tasks left, in order.
var returnSteps = []string{"read", "copy", "return", "mark", "reread", "recopy", "verify"}

// newBarePlace is an empty bare repository for tasks to move to.
func newBarePlace(t *testing.T, objectFormat, name string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), name+".git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", "--object-format="+objectFormat, bare)
	syncGit(t, bare, "config", "receive.autogc", "false")
	syncGit(t, bare, "config", "gc.auto", "0")
	return bare
}

// checkoutOn opens a fresh clone of the code whose settings name setting, set
// up and synchronized with that place: a teammate on that generation of the
// settings.
func checkoutOn(t *testing.T, origin string, setting core.TasksRemoteSetting) (*Repository, core.ProjectConfig) {
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
	if _, err := reopened.EnsureTasksRemote(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Sync(ctx, config); err != nil {
		t.Fatalf("set up a checkout on %+v: %v", setting, err)
	}
	return reopened, config
}

// pullSettings gives checkout the settings setting names and reopens it, which
// is what pulling the commit that changed them and running setup does.
func pullSettings(t *testing.T, checkout *Repository, setting core.TasksRemoteSetting) (*Repository, core.ProjectConfig) {
	t.Helper()
	ctx := context.Background()
	if _, err := checkout.SetProjectTasksRemote(ctx, setting); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, checkout.Root)
	if err != nil {
		t.Fatal(err)
	}
	config, err := reopened.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.EnsureTasksRemote(ctx); err != nil {
		t.Fatal(err)
	}
	return reopened, config
}

// moveTo points first's settings at setting and moves the tasks there,
// returning first reopened on the new settings.
func moveTo(t *testing.T, first *Repository, setting core.TasksRemoteSetting) (*Repository, core.ProjectConfig) {
	t.Helper()
	reopened, config := pullSettings(t, first, setting)
	if _, err := reopened.MoveTasks(context.Background(), config, MoveTasksOptions{}); err != nil {
		t.Fatalf("move to %+v: %v", setting, err)
	}
	return reopened, config
}

// requireMovedTo requires err to be the tasks-moved refusal naming url as the
// place the tasks are now.
func requireMovedTo(t *testing.T, err error, url string) {
	t.Helper()
	refusal, found := core.TasksRemoteRefusalOf(err)
	if !found || refusal.Reason != core.TasksRemoteMoved || refusal.MovedTo == nil || refusal.MovedTo.URL != url {
		t.Fatalf("error = %v (refusal %+v), want the tasks-moved refusal naming %s", err, refusal, url)
	}
}

// identityShape is what a place's identity ref says: "identity" for the
// identity commit, "moved:<url>" for a marker, "returned" for a return.
func identityShape(t *testing.T, bare string) string {
	t.Helper()
	tip := syncGit(t, bare, "rev-parse", identityRef)
	parents := strings.Fields(syncGit(t, bare, "rev-list", "--parents", "-n1", tip))
	if len(parents) == 1 {
		return "identity"
	}
	tree := syncGit(t, bare, "ls-tree", "--name-only", tip)
	if strings.Contains(tree, tasksMovedDocumentPath) {
		document := syncGit(t, bare, "cat-file", "blob", tip+":"+tasksMovedDocumentPath)
		moved, err := decodeTasksMoved([]byte(document + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		return "moved:" + moved.URL
	}
	root := syncGit(t, bare, "rev-list", "--max-parents=0", tip)
	if syncGit(t, bare, "rev-parse", tip+"^{tree}") != syncGit(t, bare, "rev-parse", root+"^{tree}") {
		t.Fatalf("%s's return commit %s does not carry the identity tree", bare, tip)
	}
	if syncGit(t, bare, "log", "-1", "--format=%s", tip) != tasksReturnedCommitMessage {
		t.Fatalf("%s's return commit is not titled %q", bare, tasksReturnedCommitMessage)
	}
	return "returned"
}

// requireSameTaskRefs requires two places to hold the same task refs and the
// same configuration ledger, commit for commit.
func requireSameTaskRefs(t *testing.T, a, b string) {
	t.Helper()
	for _, prefix := range []string{taskRefPrefix, configRef} {
		left, right := refListing(t, a, prefix), refListing(t, b, prefix)
		if left != right || (prefix == taskRefPrefix && left == "") {
			t.Fatalf("%s differ:\n%s: %s\n%s: %s", prefix, a, left, b, right)
		}
	}
}

// Moving the tasks back to origin is the same two commands as moving them
// away: settings that name no tasks remote, then move-tasks. A run stopped
// before any of its steps and run again ends with origin holding every task
// ref the tasks repository holds, origin's identity ref recording the return
// on top of its marker, and the tasks repository marked as moved to origin.
// A teammate still on the first settings is refused only until the return is
// recorded and then publishes the edit it kept; a teammate on the tasks
// repository's settings publishes there until it is marked, and is refused
// and told origin after, and its kept edit reaches origin once it pulls the
// settings.
func TestMoveTasksBackToOriginResumesAfterAnInterruptionAtEveryStep(t *testing.T) {
	t.Parallel()
	for index, step := range returnSteps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			returnToOriginAfterInterruption(t, testrepo.FormatSHA1, index, step)
		})
	}
}

func TestMoveTasksBackToOriginInSHA256Repositories(t *testing.T) {
	t.Parallel()
	returnToOriginAfterInterruption(t, testrepo.FormatSHA256, 2, "return")
}

func returnToOriginAfterInterruption(t *testing.T, objectFormat string, index int, step string) {
	t.Helper()
	ctx := context.Background()
	fixture := newMoveFixture(t, objectFormat)
	tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
	first, config := moveTo(t, fixture.first, tasks)
	if got := identityShape(t, fixture.origin); got != "moved:"+fixture.tasks {
		t.Fatalf("origin after the move = %s", got)
	}

	// The old-settings teammate is refused and keeps its edit; a teammate
	// who set up on the new settings edits at the tasks repository.
	gen0, stale := fixture.second, fixture.stale
	updateSyncTask(t, gen0, stale, fixture.ids[1], "Kept by the old-settings teammate")
	if _, err := gen0.Sync(ctx, stale); err == nil {
		t.Fatal("old-settings teammate published to the place the tasks left")
	}
	gen1, gen1Config := checkoutOn(t, fixture.origin, tasks)
	updateSyncTask(t, first, config, fixture.ids[0], "Edited at the tasks repository")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}

	// Back to origin.
	first, config = pullSettings(t, first, core.TasksRemoteSetting{})
	first.moveStepHook = func(at string) error {
		if at == step {
			return errMoveInterrupted
		}
		return nil
	}
	if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
		t.Fatalf("MoveTasks() error = %v, want the interruption at %s", err, step)
	}
	first.moveStepHook = nil

	returned := index > 2 // origin records the return before "mark"
	marked := index > 3   // the tasks repository is marked before "reread"
	if got, want := identityShape(t, fixture.origin), map[bool]string{true: "returned", false: "moved:" + fixture.tasks}[returned]; got != want {
		t.Fatalf("origin interrupted at %s = %s, want %s", step, got, want)
	}

	// The tasks-repository teammate edits during the interruption.
	updateSyncTask(t, gen1, gen1Config, fixture.ids[1], "Edited on the tasks settings during the move back")
	_, gen1Err := gen1.Sync(ctx, gen1Config)
	if marked {
		requireMovedTo(t, gen1Err, first.gitRemoteURL(ctx, "origin"))
	} else if gen1Err != nil {
		t.Fatalf("tasks-settings teammate before the mark = %v", gen1Err)
	}

	result, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
	if err != nil {
		t.Fatalf("resumed MoveTasks() = %v", err)
	}
	if result.From.Remote != "tasks" || result.To.Remote != core.DefaultTasksRemote || result.Remote != "kept" {
		t.Fatalf("resumed result = %+v", result)
	}
	if want := map[bool]string{true: "already", false: "returned"}[returned]; result.Returned != want {
		t.Fatalf("resumed Returned = %q, want %q", result.Returned, want)
	}
	if want := map[bool]string{true: "already", false: "marked"}[marked]; result.Marked != want {
		t.Fatalf("resumed Marked = %q, want %q", result.Marked, want)
	}
	if got := identityShape(t, fixture.origin); got != "returned" {
		t.Fatalf("origin after the move back = %s", got)
	}
	if got := identityShape(t, fixture.tasks); got != "moved:"+first.gitRemoteURL(ctx, "origin") {
		t.Fatalf("tasks after the move back = %s", got)
	}
	requireSameTaskRefs(t, fixture.origin, fixture.tasks)
	if got := syncGit(t, fixture.origin, "rev-parse", identityRef+"^1^1"); got != syncGit(t, fixture.tasks, "rev-parse", identityRef+"^1") {
		t.Fatalf("origin's chain does not rest on the identity commit both places share: %s", got)
	}
	if local := syncGit(t, first.Root, "rev-parse", identityRef); local != syncGit(t, fixture.tasks, "rev-parse", identityRef+"^1") {
		t.Fatalf("this checkout's identity ref left the identity commit: %s", local)
	}

	// The old-settings teammate is not refused any more, and publishes the
	// edit it kept.
	if err := gen0.CheckTasksRemote(ctx); err != nil {
		t.Fatalf("old-settings teammate after the return = %v", err)
	}
	if _, err := gen0.Sync(ctx, stale); err != nil {
		t.Fatalf("old-settings teammate sync after the return = %v", err)
	}
	if got, want := syncGit(t, fixture.origin, "rev-parse", taskRefPrefix+fixture.ids[1]),
		syncGit(t, gen0.Root, "rev-parse", taskRefPrefix+fixture.ids[1]); got != want {
		t.Fatalf("origin holds %s at %s, want the old-settings teammate's %s", fixture.ids[1], got, want)
	}

	// The tasks-settings teammate is refused, told origin, and publishes
	// what it kept once it pulls the settings.
	_, err = gen1.Sync(ctx, gen1Config)
	requireMovedTo(t, err, first.gitRemoteURL(ctx, "origin"))
	gen1, gen1Config = pullSettings(t, gen1, core.TasksRemoteSetting{})
	if _, err := gen1.Sync(ctx, gen1Config); err != nil {
		t.Fatalf("tasks-settings teammate after pulling the settings = %v", err)
	}
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	for _, checkout := range []*Repository{first, gen0, gen1} {
		if _, err := checkout.Sync(ctx, config); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range fixture.ids {
		snapshot := readSyncTask(t, first, config, id)
		if id == fixture.ids[0] && snapshot.Title != "Edited at the tasks repository" {
			t.Fatalf("%s = %q after the move back", id, snapshot.Title)
		}
	}
	requireEveryOperationOnce(t, first, config, fixture.origin)
}

// Origin to tasks, tasks to third, and third back to tasks, each with the two
// commands, interrupted anywhere and run again. A checkout on any earlier
// generation of the settings is told where the tasks are now, however many
// places its own remote's marker is from there: origin's marker names tasks,
// and on the way back third's names tasks too.
func TestMoveTasksThroughAThirdPlaceAndBackResumesAtEveryStep(t *testing.T) {
	t.Parallel()
	for index, step := range returnSteps {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)
			tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
			third := core.TasksRemoteSetting{Name: "third", URL: newBarePlace(t, testrepo.FormatSHA1, "third")}
			first, _ := moveTo(t, fixture.first, tasks)
			gen1, gen1Config := checkoutOn(t, fixture.origin, tasks)
			first, config := moveTo(t, first, third)
			gen2, gen2Config := checkoutOn(t, fixture.origin, third)

			// Two moves on, the first settings are told the third place.
			gen0, stale := fixture.second, fixture.stale
			updateSyncTask(t, gen0, stale, fixture.ids[0], "Kept on the first settings")
			_, err := gen0.Sync(ctx, stale)
			requireMovedTo(t, err, third.URL)

			updateSyncTask(t, first, config, fixture.ids[1], "Edited at the third place")
			if _, err := first.Sync(ctx, config); err != nil {
				t.Fatal(err)
			}

			first, config = pullSettings(t, first, tasks)
			first.moveStepHook = func(at string) error {
				if at == step {
					return errMoveInterrupted
				}
				return nil
			}
			if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); !errors.Is(err, errMoveInterrupted) {
				t.Fatalf("MoveTasks() error = %v, want the interruption at %s", err, step)
			}
			first.moveStepHook = nil
			if index > 3 {
				updateSyncTask(t, gen2, gen2Config, fixture.ids[0], "Kept on the third settings")
				_, err := gen2.Sync(ctx, gen2Config)
				requireMovedTo(t, err, tasks.URL)
			}

			result, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
			if err != nil {
				t.Fatalf("resumed MoveTasks() = %v", err)
			}
			if result.From.Remote != "third" || result.To.Remote != "tasks" {
				t.Fatalf("resumed result = %+v", result)
			}
			if got := identityShape(t, fixture.tasks); got != "returned" {
				t.Fatalf("tasks = %s", got)
			}
			if got := identityShape(t, third.URL); got != "moved:"+tasks.URL {
				t.Fatalf("third = %s", got)
			}
			if got := identityShape(t, fixture.origin); got != "moved:"+tasks.URL {
				t.Fatalf("origin = %s", got)
			}
			requireSameTaskRefs(t, fixture.tasks, third.URL)

			// Every earlier generation of the settings is told tasks, by the
			// next command it runs.
			gen0, gen2 = reopenRepository(t, gen0), reopenRepository(t, gen2)
			_, err = gen0.Sync(ctx, stale)
			requireMovedTo(t, err, tasks.URL)
			_, err = gen2.Sync(ctx, gen2Config)
			requireMovedTo(t, err, tasks.URL)
			// The second generation names the place the tasks are now, so it
			// just works, and the edits the others kept arrive once they
			// pull the settings.
			if _, err := gen1.Sync(ctx, gen1Config); err != nil {
				t.Fatalf("second-settings teammate after the return = %v", err)
			}
			for _, stuck := range []*Repository{gen0, gen2} {
				reopened, settings := pullSettings(t, stuck, tasks)
				if _, err := reopened.Sync(ctx, settings); err != nil {
					t.Fatalf("teammate after pulling the settings = %v", err)
				}
			}
			if _, err := first.Sync(ctx, config); err != nil {
				t.Fatal(err)
			}
			requireEveryOperationOnce(t, first, config, fixture.tasks)
		})
	}
}

// A place's markers that lead in a circle are reported as such, to a
// checkout refused there, rather than followed for ever; and the move run
// from a clone whose settings name one of them settles it.
func TestMovedMarkersInACircleAreReportedAndAMoveSettlesThem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	tasks := core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks}
	first, config := moveTo(t, fixture.first, tasks)
	// Somebody marks the tasks repository as moved back to origin without
	// recording the return there: two moves at once could leave this.
	if _, err := first.markTasksMoved(ctx, "tasks",
		core.TasksRemoteSetting{Name: core.DefaultTasksRemote, URL: first.gitRemoteURL(ctx, core.DefaultTasksRemote)}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.second.Sync(ctx, fixture.stale)
	refusal, found := core.TasksRemoteRefusalOf(err)
	if !found || refusal.Reason != core.TasksRemoteMoved || refusal.Command != "workbook move-tasks" ||
		!strings.Contains(err.Error(), "leads back to a place already passed") {
		t.Fatalf("refusal in a circle = %v (%+v)", err, refusal)
	}
	if _, err := first.MoveTasks(ctx, config, MoveTasksOptions{}); err != nil {
		t.Fatalf("MoveTasks() from the tasks settings = %v", err)
	}
	if got := identityShape(t, fixture.tasks); got != "returned" {
		t.Fatalf("tasks after settling = %s", got)
	}
	reopened, err := Open(ctx, fixture.second.Root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.Sync(ctx, fixture.stale)
	requireMovedTo(t, err, fixture.tasks)
}

// A move back is safe whatever became of the copies the returned-to place
// kept: left as they were, edited since at the other place, or removed by
// hand. Each ends with the two places holding the same task refs.
func TestMoveTasksBackWhateverBecameOfTheOldCopies(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"untouched", "stale", "gone"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMoveFixture(t, testrepo.FormatSHA1)
			first, config := moveTo(t, fixture.first, core.TasksRemoteSetting{Name: "tasks", URL: fixture.tasks})
			switch variant {
			case "stale":
				updateSyncTask(t, first, config, fixture.ids[0], "Edited after the move")
				createSyncTask(t, first, config, "Created after the move")
				if _, err := first.Sync(ctx, config); err != nil {
					t.Fatal(err)
				}
			case "gone":
				for _, id := range fixture.ids {
					syncGit(t, fixture.origin, "update-ref", "-d", taskRefPrefix+id)
				}
				syncGit(t, fixture.origin, "update-ref", "-d", configRef)
			}
			first, config = pullSettings(t, first, core.TasksRemoteSetting{})
			result, err := first.MoveTasks(ctx, config, MoveTasksOptions{})
			if err != nil {
				t.Fatalf("MoveTasks() back = %v", err)
			}
			if !result.Verified || result.Returned != "returned" || result.Marked != "marked" {
				t.Fatalf("result = %+v", result)
			}
			requireSameTaskRefs(t, fixture.origin, fixture.tasks)
		})
	}
}

// A project that never chose a tasks remote has nothing to move: move-tasks
// says so as it always did.
func TestMoveTasksWithNoTasksRemoteAndNoMoveIsRefusedAsBefore(t *testing.T) {
	t.Parallel()
	fixture := newMoveFixture(t, testrepo.FormatSHA1)
	_, err := fixture.second.MoveTasks(context.Background(), fixture.stale, MoveTasksOptions{})
	if core.CategoryOf(err) != core.CategoryValidation || !strings.Contains(err.Error(), "name no tasks remote to move to") {
		t.Fatalf("MoveTasks() = %v", err)
	}
}

// reopenRepository opens checkout afresh, as the next command run there does.
func reopenRepository(t *testing.T, checkout *Repository) *Repository {
	t.Helper()
	reopened, err := Open(context.Background(), checkout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	return reopened
}

// requireEveryOperationOnce fails when any task history in bare records an
// operation ID twice. checkout has just synchronized with bare and holds its
// task refs at the same commits, which is checked first.
func requireEveryOperationOnce(t *testing.T, checkout *Repository, config core.ProjectConfig, bare string) {
	t.Helper()
	ctx := context.Background()
	refs, _, err := checkout.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if local, remote := refListing(t, checkout.Root, taskRefPrefix), refListing(t, bare, taskRefPrefix); local != remote {
		t.Fatalf("checkout and %s hold different task refs:\n%s\n%s", bare, local, remote)
	}
	heads := make([]TaskHead, 0, len(refs))
	for _, ref := range refs {
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	chains, err := checkout.completeTaskChains(ctx, config, heads)
	if err != nil {
		t.Fatal(err)
	}
	for index, chain := range chains {
		if detail, repeated := repeatedOperation(chain); repeated {
			t.Fatalf("%s at %s: %s", heads[index].TaskID, bare, detail)
		}
	}
}

// The identity ref of a place the tasks moved through reads as one of three
// shapes by its tip alone: the identity commit (live, never left), a marker
// (left, and where to), or a return (live again). Every move and return is
// one commit on the one before, alternating, all on the identity commit, and
// any other history is refused as corrupt.
func TestIdentityHistoryOfMovesAndReturns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, _ := syncRepositories(t)
	identity, found, err := repository.readIdentityRef(ctx, identityRef)
	if err != nil || !found {
		t.Fatalf("identity = %v, %v", found, err)
	}
	marker := func(base identityRecord, remote string) string {
		t.Helper()
		commit, err := repository.writeTasksMovedCommit(ctx, base, TasksMoved{
			Format: tasksMovedFormat, Version: tasksMovedVersion, ProjectID: identity.Identity.ProjectID,
			Remote: remote, URL: "/srv/" + remote + ".git",
		})
		if err != nil {
			t.Fatal(err)
		}
		return commit
	}
	read := func(head string) (identityRecord, error) {
		t.Helper()
		const ref = "refs/workbook/remotes/somewhere/project"
		syncGit(t, repository.Root, "update-ref", ref, head)
		record, _, err := repository.readIdentityRef(ctx, ref)
		return record, err
	}
	mustRead := func(head string) identityRecord {
		t.Helper()
		record, err := read(head)
		if err != nil {
			t.Fatalf("read %s: %v", head, err)
		}
		if record.Root != identity.Head {
			t.Fatalf("root of %s = %s, want the identity commit %s", head, record.Root, identity.Head)
		}
		return record
	}

	moved := marker(identity, "tasks")
	left := mustRead(moved)
	if left.Moved == nil || left.Moved.Remote != "tasks" || left.Departed == nil || left.Returned() {
		t.Fatalf("a marker reads as %+v", left)
	}
	returnedHead, err := repository.writeTasksReturnedCommit(ctx, left)
	if err != nil {
		t.Fatal(err)
	}
	back := mustRead(returnedHead)
	if back.Moved != nil || !back.Returned() || back.Departed == nil || back.Departed.Remote != "tasks" {
		t.Fatalf("a return reads as %+v", back)
	}
	again := mustRead(marker(back, "third"))
	if again.Moved == nil || again.Moved.Remote != "third" || again.Returned() {
		t.Fatalf("a marker on a return reads as %+v", again)
	}
	if never := mustRead(identity.Head); never.Moved != nil || never.Departed != nil || never.Returned() {
		t.Fatalf("the identity commit reads as %+v", never)
	}
	// The same return written twice is the same commit.
	if twice, err := repository.writeTasksReturnedCommit(ctx, left); err != nil || twice != returnedHead {
		t.Fatalf("a second return = %s, %v; want %s", twice, err, returnedHead)
	}

	commitTree := func(tree string, parents ...string) string {
		t.Helper()
		args := []string{"commit-tree", tree, "-m", "hand-made"}
		for _, parent := range parents {
			args = append(args, "-p", parent)
		}
		return syncGit(t, repository.Root, args...)
	}
	identityTree := syncGit(t, repository.Root, "rev-parse", identity.Head+"^{tree}")
	for name, head := range map[string]string{
		"a return where nothing left":          commitTree(identityTree, identity.Head),
		"a marker on a marker":                 marker(left, "third"),
		"a return on a return":                 commitTree(identityTree, returnedHead),
		"a merge of a marker and the identity": commitTree(identityTree, moved, identity.Head),
	} {
		if _, err := read(head); core.CategoryOf(err) != core.CategoryCorruptData {
			t.Errorf("%s read as %v, want corrupt-data", name, err)
		}
	}

	// Only a remote's copy carries any of it.
	syncGit(t, repository.Root, "update-ref", identityRef, returnedHead)
	reopened, err := Open(ctx, repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadConfig(); core.CategoryOf(err) != core.CategoryCorruptData || !strings.Contains(err.Error(), identity.Head) {
		t.Fatalf("LoadConfig() with a return as the identity = %v, want corrupt-data naming the identity commit", err)
	}
}
