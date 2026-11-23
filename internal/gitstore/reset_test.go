package gitstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
