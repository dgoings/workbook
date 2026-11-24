package gitstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

var errResetInterrupted = errors.New("interrupted")

// bundleHeads is every ref a bundle holds, with the manifest left out.
func bundleHeads(t *testing.T, directory, bundle string) map[string]string {
	t.Helper()
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(syncGit(t, directory, "bundle", "list-heads", bundle)), "\n") {
		if objectID, name, found := strings.Cut(line, " "); found && name != archiveManifestRef {
			heads[name] = objectID
		}
	}
	return heads
}

// workbookRefs is every ref under refs/workbook/ a repository holds, and
// otherRefs every ref outside it.
func workbookRefs(t *testing.T, directory string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(syncGit(t, directory, "for-each-ref", "--format=%(refname) %(objectname)")), "\n") {
		if name, objectID, found := strings.Cut(line, " "); found && strings.HasPrefix(name, workbookRefRoot) {
			refs[name] = objectID
		}
	}
	return refs
}

func otherRefs(t *testing.T, directory string) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(syncGit(t, directory, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if line != "" && !strings.HasPrefix(line, workbookRefRoot) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// A checkout's reset stopped after its bundle, or after its refs went and
// before its caches did, is finished by running it again. The first run's
// bundle holds every ref the checkout had, and nothing outside refs/workbook/
// moves.
func TestResetLocalResumesAfterAnInterruptionAtEveryStep(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"bundle", "refs"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, _, config := syncRepositories(t)
			createSyncTask(t, first, config, "Published")
			if _, err := first.Sync(ctx, config); err != nil {
				t.Fatal(err)
			}
			createSyncTask(t, first, config, "Here only")
			before, outside := workbookRefs(t, first.Root), otherRefs(t, first.Root)
			if err := os.MkdirAll(filepath.Join(first.CommonGitDir, "workbook"), 0o755); err != nil {
				t.Fatal(err)
			}

			plan, err := first.PlanLocalReset(ctx, "origin")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Unpublished) != 1 {
				t.Fatalf("unpublished = %+v, want the one task made here only", plan.Unpublished)
			}
			first.resetStepHook = func(at string) error {
				if at == step {
					return errResetInterrupted
				}
				return nil
			}
			interrupted, err := first.ResetLocal(ctx, config.ProjectID, plan, ResetOptions{})
			if !errors.Is(err, errResetInterrupted) {
				t.Fatalf("ResetLocal() error = %v, want the interruption after %s", err, step)
			}
			first.resetStepHook = nil
			if got := bundleHeads(t, first.Root, interrupted.Bundle); len(got) != len(before) {
				t.Fatalf("the interrupted run's bundle holds %v, want %v", got, before)
			}
			for name, objectID := range before {
				if got := bundleHeads(t, first.Root, interrupted.Bundle)[name]; got != objectID {
					t.Fatalf("the interrupted run's bundle holds %s at %q, want %s", name, got, objectID)
				}
			}

			plan, err = first.PlanLocalReset(ctx, "origin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.ResetLocal(ctx, config.ProjectID, plan, ResetOptions{}); err != nil {
				t.Fatalf("resumed ResetLocal() = %v", err)
			}
			if got := workbookRefs(t, first.Root); len(got) != 0 {
				t.Fatalf("refs left after the resumed reset: %v", got)
			}
			if _, err := os.Stat(filepath.Join(first.CommonGitDir, "workbook")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the cache directory is still there: %v", err)
			}
			if got := otherRefs(t, first.Root); got != outside {
				t.Fatalf("refs outside refs/workbook/ changed:\n%s\nwant\n%s", got, outside)
			}
		})
	}
}

// A remote's reset stopped after its bundle, or after its deletions and
// before this checkout's tracking refs were pruned, is finished by running it
// again: the remote ends with no Workbook ref, this checkout tracks none of
// them, and nothing outside refs/workbook/ moves at either.
func TestResetRemoteResumesAfterAnInterruptionAtEveryStep(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"bundle", "push"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, _, config := syncRepositories(t)
			createSyncTask(t, first, config, "Published")
			if _, err := first.Sync(ctx, config); err != nil {
				t.Fatal(err)
			}
			origin := syncGit(t, first.Root, "remote", "get-url", "origin")
			originOutside, outside := otherRefs(t, origin), otherRefs(t, first.Root)
			held := workbookRefs(t, origin)

			plan, err := first.PlanRemoteReset(ctx, "origin")
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Live() || plan.Tasks != 1 || len(plan.Refs) != len(held) {
				t.Fatalf("plan = %+v, want every one of %v", plan, held)
			}
			first.resetStepHook = func(at string) error {
				if at == step {
					return errResetInterrupted
				}
				return nil
			}
			if _, err := first.ResetRemote(ctx, config.ProjectID, plan, ResetOptions{}); !errors.Is(err, errResetInterrupted) {
				t.Fatalf("ResetRemote() error = %v, want the interruption after %s", err, step)
			}
			first.resetStepHook = nil
			if step == "bundle" && len(workbookRefs(t, origin)) != len(held) {
				t.Fatalf("an interruption before the push changed origin: %v", workbookRefs(t, origin))
			}

			plan, err = first.PlanRemoteReset(ctx, "origin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.ResetRemote(ctx, config.ProjectID, plan, ResetOptions{}); err != nil {
				t.Fatalf("resumed ResetRemote() = %v", err)
			}
			if got := workbookRefs(t, origin); len(got) != 0 {
				t.Fatalf("origin still holds %v", got)
			}
			for name := range workbookRefs(t, first.Root) {
				if strings.HasPrefix(name, remoteTrackingPrefix("origin")) {
					t.Fatalf("this checkout still tracks %s", name)
				}
			}
			if got := otherRefs(t, origin); got != originOutside {
				t.Fatalf("origin's refs outside refs/workbook/ changed:\n%s\nwant\n%s", got, originOutside)
			}
			if got := otherRefs(t, first.Root); got != outside {
				t.Fatalf("refs outside refs/workbook/ changed:\n%s\nwant\n%s", got, outside)
			}
		})
	}
}

// A reset stopped before its backup began, or killed while writing it, which
// leaves the manifest ref the backup sets and a half-written temporary bundle
// beside the file it was writing, is finished by the first run after it. That
// run's bundle holds every ref the checkout had, the leftover manifest ref is
// neither bundled as data nor allowed to fail the bundle's own check, and the
// temporary file is not taken for a backup.
func TestResetLocalFinishesOnTheFirstRunAfterAnInterruptedBackup(t *testing.T) {
	t.Parallel()
	for _, interruption := range []string{"before the bundle", "during the bundle at --backup", "during the bundle at the default place"} {
		t.Run(interruption, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			first, _, config := syncRepositories(t)
			createSyncTask(t, first, config, "Published")
			if _, err := first.Sync(ctx, config); err != nil {
				t.Fatal(err)
			}
			createSyncTask(t, first, config, "Here only")
			before, outside := workbookRefs(t, first.Root), otherRefs(t, first.Root)
			options := ResetOptions{}
			var temporary string
			switch interruption {
			case "before the bundle":
				plan, err := first.PlanLocalReset(ctx, "origin")
				if err != nil {
					t.Fatal(err)
				}
				first.resetStepHook = func(at string) error {
					if at == "start" {
						return errResetInterrupted
					}
					return nil
				}
				if _, err := first.ResetLocal(ctx, config.ProjectID, plan, options); !errors.Is(err, errResetInterrupted) {
					t.Fatalf("ResetLocal() error = %v, want the interruption before the bundle", err)
				}
				first.resetStepHook = nil
				if got := workbookRefs(t, first.Root); len(got) != len(before) {
					t.Fatalf("an interruption before the bundle changed refs: %v", got)
				}
			default:
				// What a process killed between setting the manifest ref and
				// publishing the bundle leaves: the ref, and the temporary
				// file half written.
				manifest, err := first.writeArchiveManifest(ctx, normalizedArchiveManifest(ArchiveManifest{
					ProjectID: config.ProjectID, RunID: "01INTERRUPTEDRUN0000000000", Flags: []string{"reset"},
				}))
				if err != nil {
					t.Fatal(err)
				}
				if err := first.setArchiveManifestRef(ctx, manifest); err != nil {
					t.Fatal(err)
				}
				if interruption == "during the bundle at --backup" {
					options.Backup = filepath.Join(t.TempDir(), "kept.bundle")
					temporary = options.Backup + ".tmp"
				} else {
					temporary = DefaultArchivePath(first.CommonGitDir, "01INTERRUPTEDRUN0000000000") + ".tmp"
				}
				if err := os.MkdirAll(filepath.Dir(temporary), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(temporary, []byte("# v2 git bundle\nhalf"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			plan, err := first.PlanLocalReset(ctx, "origin")
			if err != nil {
				t.Fatal(err)
			}
			result, err := first.ResetLocal(ctx, config.ProjectID, plan, options)
			if err != nil {
				t.Fatalf("the first ResetLocal() after the interruption = %v", err)
			}
			if result.Bundle == "" || result.Bundle == temporary {
				t.Fatalf("bundle = %q, want a new one, not the temporary file %q", result.Bundle, temporary)
			}
			syncGit(t, first.Root, "bundle", "verify", result.Bundle)
			want := map[string]string{}
			for name, objectID := range before {
				if name != archiveManifestRef {
					want[name] = objectID
				}
			}
			if got := bundleHeads(t, first.Root, result.Bundle); len(got) != len(want) {
				t.Fatalf("the bundle holds %v, want %v", got, want)
			}
			for name, objectID := range want {
				if got := bundleHeads(t, first.Root, result.Bundle)[name]; got != objectID {
					t.Fatalf("the bundle holds %s at %q, want %s", name, got, objectID)
				}
			}
			if got := workbookRefs(t, first.Root); len(got) != 0 {
				t.Fatalf("refs left after the reset: %v", got)
			}
			if got := otherRefs(t, first.Root); got != outside {
				t.Fatalf("refs outside refs/workbook/ changed:\n%s\nwant\n%s", got, outside)
			}
		})
	}
}

// A --backup inside a folder a reset of this checkout removes files from is
// refused before anything is done, however it is spelled: a file or a folder,
// through a symbolic link, or not yet existing. The default place is outside
// those folders in an ordinary checkout, a linked worktree, whose Git
// directory is the main checkout's, and a tasks-only folder.
func TestResetRefusesABackupInsideAFolderItRemovesFilesFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	syncGit(t, first.Root, "worktree", "add", "--detach", "--quiet", linkedDir, "HEAD")
	linked, err := Open(ctx, linkedDir)
	if err != nil {
		t.Fatal(err)
	}
	origin := strings.TrimSpace(syncGit(t, first.Root, "remote", "get-url", "origin"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	tasksOnlyDir := t.TempDir()
	if _, err := PrepareTasksOnlyFolder(ctx, tasksOnlyDir, origin, config.Key); err != nil {
		t.Fatal(err)
	}
	tasksOnly, err := Open(ctx, tasksOnlyDir)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(outside, "link")
	if err := os.Symlink(filepath.Join(first.Root, ".workbook"), link); err != nil {
		t.Fatal(err)
	}
	for _, backup := range []string{
		filepath.Join(first.Root, ".workbook", "mybackup.bundle"),
		filepath.Join(first.Root, ".workbook"),
		filepath.Join(first.Root, ".workbook", "new", "folder", "x.bundle"),
		filepath.Join(first.CommonGitDir, "workbook"),
		filepath.Join(first.CommonGitDir, "workbook", "mybackup.bundle"),
		filepath.Join(link, "mybackup.bundle"),
		link,
	} {
		if err := first.CheckResetBackup(backup); err == nil || core.CategoryOf(err) != core.CategoryInvocation ||
			!strings.Contains(err.Error(), backup) {
			t.Errorf("CheckResetBackup(%s) = %v, want an invocation refusal naming it", backup, err)
		}
		if _, err := first.PrepareResetBackup(ResetOptions{Backup: backup}); err == nil {
			t.Errorf("PrepareResetBackup(%s) accepted it", backup)
		}
	}
	if err := linked.CheckResetBackup(filepath.Join(first.CommonGitDir, "workbook")); err == nil {
		t.Error("a linked worktree accepted the shared Git directory's workbook/ folder")
	}
	if err := linked.CheckResetBackup(filepath.Join(linkedDir, ".workbook", "x.bundle")); err == nil {
		t.Error("a linked worktree accepted its own .workbook/ folder")
	}
	for _, accepted := range []string{filepath.Join(outside, "elsewhere.bundle"), outside,
		filepath.Join(first.CommonGitDir, "workbook-archive")} {
		if err := first.CheckResetBackup(accepted); err != nil {
			t.Errorf("CheckResetBackup(%q) = %v, want it accepted", accepted, err)
		}
	}
	for name, repository := range map[string]*Repository{"checkout": first, "linked worktree": linked, "tasks-only folder": tasksOnly} {
		options, err := repository.PrepareResetBackup(ResetOptions{})
		if err != nil {
			t.Fatalf("%s: PrepareResetBackup() = %v", name, err)
		}
		for _, folder := range append(repository.resetFolders(), first.resetFolders()...) {
			if pathInside(options.Backup, folder) {
				t.Errorf("%s: the default bundle %s is inside %s", name, options.Backup, folder)
			}
		}
	}
}

// Nothing publishes a relic yet, so a checkout whose tasks remote holds every
// task, and which also holds a relic, holds work that exists only here: the
// relic, named by its ID and title, with every commit and operation it has.
func TestResetCountsALocalRelicAsWorkThatExistsOnlyHere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	createSyncTask(t, first, config, "Published")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	if plan, err := first.PlanLocalReset(ctx, "origin"); err != nil || plan.Holds() {
		t.Fatalf("PlanLocalReset() before the relic = %+v, %v; want nothing held only here", plan, err)
	}
	root := writeRelicRoot(t, first, config, relicTestPartNumbered(1))
	tip := writeRelicChild(t, first, config, root, nil, relicTitleSet(2, "Archive plan, revised"))
	relicID := tip.Record.State.RelicID

	plan, err := first.PlanLocalReset(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Holds() || plan.Unchecked != "" || len(plan.Unpublished) != 1 {
		t.Fatalf("PlanLocalReset() = %+v, want the relic as the one unpublished ref", plan)
	}
	got := plan.Unpublished[0]
	want := UnpublishedRef{
		Ref:        relicRef(relicID),
		RelicID:    relicID,
		Title:      "Archive plan, revised",
		New:        true,
		Commits:    2,
		Operations: []string{relicTestID(1), relicTestID(2), relicTestID(800001)},
	}
	if got.Ref != want.Ref || got.RelicID != want.RelicID || got.TaskID != "" || got.Title != want.Title ||
		got.New != want.New || got.Commits != want.Commits || strings.Join(got.Operations, ",") != strings.Join(want.Operations, ",") {
		t.Fatalf("unpublished = %+v, want %+v", got, want)
	}
}

// A reset never reports a bundle that is not there: one that vanishes before
// the reset finishes fails it, naming the path.
func TestResetLocalFailsWhenItsBundleIsGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	createSyncTask(t, first, config, "Here only")
	plan, err := first.PlanLocalReset(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	options := ResetOptions{Backup: filepath.Join(t.TempDir(), "gone.bundle")}
	first.resetStepHook = func(at string) error {
		if at == "refs" {
			return os.Remove(options.Backup)
		}
		return nil
	}
	if _, err := first.ResetLocal(ctx, config.ProjectID, plan, options); err == nil || !strings.Contains(err.Error(), options.Backup) {
		t.Fatalf("ResetLocal() with its bundle gone = %v, want an error naming %s", err, options.Backup)
	}
}

// forgeRelicTipWithoutAnOperation puts one hand-made commit on top of a relic
// whose tree holds no operation.json, and points the relic's ref at it. Every
// commit Workbook wrote for the relic is still reachable from it.
func forgeRelicTipWithoutAnOperation(t *testing.T, repo *Repository, tip core.RelicSnapshot) string {
	t.Helper()
	ref := relicRef(tip.Record.State.RelicID)
	saved := syncGit(t, repo.Root, "rev-parse", tip.Head+"^{tree}")
	tree := syncGitInput(t, repo.Root, []byte("040000 tree "+saved+"\tsaved\n"), "mktree")
	forged := syncGit(t, repo.Root, "commit-tree", tree, "-p", tip.Head, "-m", "hand-made")
	syncGit(t, repo.Root, "update-ref", ref, forged, tip.Head)
	return forged
}

// A relic ref whose newest commit is not a Workbook commit is damaged, and its
// object is still a commit Git can bundle with the whole history under it. A
// relic exists nowhere else, so the reset keeps it like any other ref: it is
// work that exists only here, the bundle holds it at the same object, and the
// restore command fetches it back.
func TestResetBundlesADamagedRelicRefWhoseObjectIsACommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	createSyncTask(t, first, config, "Published")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	root := writeRelicRoot(t, first, config, relicTestPartNumbered(1))
	tip := writeRelicChild(t, first, config, root, nil, relicTitleSet(2, "Archive plan, revised"))
	relicID := tip.Record.State.RelicID
	forged := forgeRelicTipWithoutAnOperation(t, first, tip)

	plan, err := first.PlanLocalReset(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	var listed *ResetRef
	for index := range plan.Refs {
		if plan.Refs[index].Ref == relicRef(relicID) {
			listed = &plan.Refs[index]
		}
	}
	if listed == nil || listed.ObjectID != forged || listed.Damaged == "" || !listed.Bundleable {
		t.Fatalf("the relic ref is listed as %+v, want it damaged at %s and bundleable", listed, forged)
	}
	if !plan.Holds() || len(plan.Unpublished) != 1 {
		t.Fatalf("PlanLocalReset() = %+v, want the damaged relic as the one unpublished ref", plan)
	}
	got := plan.Unpublished[0]
	if got.Ref != relicRef(relicID) || got.RelicID != relicID || !got.New || got.Commits != 3 ||
		strings.Join(got.Operations, ",") != strings.Join([]string{relicTestID(1), relicTestID(2), relicTestID(800001)}, ",") {
		t.Fatalf("unpublished = %+v, want the relic with its three commits and the operations of the two Workbook wrote", got)
	}

	result, err := first.ResetLocal(ctx, config.ProjectID, plan, ResetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if heads := bundleHeads(t, first.Root, result.Bundle); heads[relicRef(relicID)] != forged {
		t.Fatalf("the bundle holds %v, want %s at %s", heads, relicRef(relicID), forged)
	}
	if command := first.RestoreCommand(result.Bundle, plan.Refs); !strings.Contains(command, relicRefPrefix+"*:"+relicRefPrefix+"*") {
		t.Fatalf("RestoreCommand() = %s, want it to fetch the relic refs", command)
	}
	if got := workbookRefs(t, first.Root); len(got) != 0 {
		t.Fatalf("refs left after the reset: %v", got)
	}
	fresh := t.TempDir()
	syncGit(t, fresh, "init", "--quiet")
	syncGit(t, fresh, "fetch", "--quiet", result.Bundle, relicRefPrefix+"*:"+relicRefPrefix+"*")
	if got := syncGit(t, fresh, "rev-parse", "--verify", relicRef(relicID)); got != forged {
		t.Fatalf("the bundle restores %s at %q, want %s", relicRef(relicID), got, forged)
	}
	if got := syncGit(t, fresh, "rev-parse", "--verify", relicRef(relicID)+"~1"); got != tip.Head {
		t.Fatalf("the restored ref's parent is %q, want the relic's last Workbook commit %s", got, tip.Head)
	}
}

// A relic ref that is symbolic is damaged too, and the commit it resolves to
// is kept the same way. One whose object is not a commit cannot be bundled:
// it is the one kind of relic ref a reset removes with nothing kept.
func TestResetBundlesEveryRelicRefThatResolvesToACommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	root := writeRelicRoot(t, first, config, relicTestPartNumbered(1))
	symbolic := relicRef(relicTestRelicID(71))
	notACommit := relicRef(relicTestRelicID(72))
	syncGit(t, first.Root, "symbolic-ref", symbolic, relicRef(root.Record.State.RelicID))
	blob := syncGitInput(t, first.Root, []byte("not a commit\n"), "hash-object", "-w", "--stdin")
	syncGit(t, first.Root, "update-ref", notACommit, blob)

	plan, err := first.PlanLocalReset(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]ResetRef{}
	for _, ref := range plan.Refs {
		listed[ref.Ref] = ref
	}
	if ref := listed[symbolic]; ref.Damaged == "" || !ref.Bundleable {
		t.Fatalf("the symbolic relic ref is listed as %+v, want it damaged and bundleable", ref)
	}
	if ref := listed[notACommit]; ref.Damaged != "it names a blob, not a commit" || ref.Bundleable {
		t.Fatalf("the relic ref naming a blob is listed as %+v, want it damaged and not bundleable", ref)
	}
	unpublished := map[string]bool{}
	for _, item := range plan.Unpublished {
		unpublished[item.Ref] = true
	}
	if !unpublished[symbolic] || unpublished[notACommit] {
		t.Fatalf("unpublished = %+v, want the symbolic relic ref and not the one naming a blob", plan.Unpublished)
	}

	result, err := first.ResetLocal(ctx, config.ProjectID, plan, ResetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	heads := bundleHeads(t, first.Root, result.Bundle)
	if heads[symbolic] != root.Head {
		t.Fatalf("the bundle holds %v, want %s at %s", heads, symbolic, root.Head)
	}
	if _, bundled := heads[notACommit]; bundled {
		t.Fatalf("the bundle holds %s, which names a blob", notACommit)
	}
	if got := workbookRefs(t, first.Root); len(got) != 0 {
		t.Fatalf("refs left after the reset: %v", got)
	}
}
