package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// The tests in this file pin the relic pack (relicpack.go): a handle that
// moved a relic ref packs the relic namespaces and nothing else, changes no
// ref's value, runs nothing when no relic ref moved, and never fails for it.
// They look at the repository's files, which only a test may do, to tell a
// loose ref from a packed one.

// looseRefsUnder lists the loose ref files below prefix in repo's common Git
// directory, as ref names.
func looseRefsUnder(t *testing.T, repo *Repository, prefix string) []string {
	t.Helper()
	root := filepath.Join(repo.CommonGitDir, filepath.FromSlash(prefix))
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".lock") {
			return nil
		}
		relative, err := filepath.Rel(repo.CommonGitDir, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// packedRefNames lists the ref names the packed-refs file holds.
func packedRefNames(t *testing.T, repo *Repository) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	contents, err := os.ReadFile(filepath.Join(repo.CommonGitDir, "packed-refs"))
	if os.IsNotExist(err) {
		return names
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		if _, name, found := strings.Cut(line, " "); found {
			names[name] = true
		}
	}
	return names
}

// everyRef is every ref the repository holds with its value, as Git lists
// them.
func everyRef(t *testing.T, repo *Repository) string {
	t.Helper()
	return syncGit(t, repo.Root, "for-each-ref", "--format=%(refname) %(objectname)")
}

// packCommands is the pack-refs commands among a recorded run.
func packCommands(commands [][]string) [][]string {
	var packs [][]string
	for _, command := range commands {
		if len(command) > 0 && command[0] == "pack-refs" {
			packs = append(packs, command)
		}
	}
	return packs
}

// relicsFetchedInto publishes count new relics from first and fetches them
// into second.
func relicsFetchedInto(t *testing.T, first, second *Repository, config core.ProjectConfig, count int) []core.RelicSnapshot {
	t.Helper()
	roots := make([]core.RelicSnapshot, 0, count)
	for n := 1; n <= count; n++ {
		roots = append(roots, writeRelicRootNumbered(t, first, config, n))
	}
	publishRelicRefs(t, first)
	fetchRelics(t, second, config)
	return roots
}

func TestAFetchThatBringsRelicsPacksOnlyTheRelicNamespaces(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	// A loose branch, a loose tag and a loose ref elsewhere under
	// refs/workbook/ are not the pack's to touch.
	head := syncGit(t, second.Root, "rev-parse", "HEAD")
	syncGit(t, second.Root, "branch", "kept-loose")
	syncGit(t, second.Root, "tag", "kept-loose-tag")
	syncGit(t, second.Root, "update-ref", "refs/workbook/probes/kept-loose", head)

	relicsFetchedInto(t, first, second, config, 30)
	loose := looseRefsUnder(t, second, relicRefPrefix)
	if len(loose) != 30 {
		t.Fatalf("before the pack %d relic refs are loose, want 30: %v", len(loose), loose)
	}
	before := everyRef(t, second)

	commands := observeCommands(second)
	second.PackMovedRelicRefs(ctx)
	second.commandObserver = nil
	packs := packCommands(*commands)
	if len(packs) != 1 {
		t.Fatalf("PackMovedRelicRefs ran %d pack-refs, want 1: %q", len(packs), *commands)
	}
	for _, arg := range packs[0] {
		if arg == "--all" {
			t.Fatalf("the pack named --all: %q", packs[0])
		}
	}

	if after := everyRef(t, second); after != before {
		t.Fatalf("the pack changed what Git lists:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, prefix := range []string{relicRefPrefix, relicTrackingRefPrefixFor("origin")} {
		if got := looseRefsUnder(t, second, prefix); len(got) != 0 {
			t.Fatalf("%d refs under %s are still loose: %v", len(got), prefix, got)
		}
	}
	packed := packedRefNames(t, second)
	for _, root := range relicRefNames(t, second, relicRefPrefix) {
		if !packed[root] {
			t.Fatalf("%s is not in packed-refs", root)
		}
	}
	for _, ref := range []string{"refs/heads/kept-loose", "refs/tags/kept-loose-tag", "refs/workbook/probes/kept-loose"} {
		if packed[ref] {
			t.Fatalf("%s was packed", ref)
		}
		if len(looseRefsUnder(t, second, ref)) != 1 {
			t.Fatalf("%s is no longer a loose ref", ref)
		}
	}
	for ref := range packed {
		if !strings.HasPrefix(ref, "refs/workbook/") && !strings.HasPrefix(ref, "refs/remotes/") &&
			!strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		if strings.HasPrefix(ref, "refs/workbook/") && !strings.Contains(ref, "relics/") {
			t.Fatalf("%s, a Workbook ref outside the relic namespaces, was packed", ref)
		}
	}

	// The move was packed; the next call has nothing to do.
	commands = observeCommands(second)
	second.PackMovedRelicRefs(ctx)
	second.commandObserver = nil
	if len(*commands) != 0 {
		t.Fatalf("a second PackMovedRelicRefs ran %q, want nothing", *commands)
	}
}

// A handle that moved no relic ref runs no Git process for the pack: neither
// a fetch that found every relic unchanged nor a task command's fetch.
func TestNoRelicMoveMeansNoPack(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	relicsFetchedInto(t, first, second, config, 3)

	reopened, err := Open(ctx, second.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	fetchRelics(t, reopened, config)
	if _, err := reopened.FetchTasks(ctx, config); err != nil {
		t.Fatal(err)
	}
	commands := observeCommands(reopened)
	reopened.PackMovedRelicRefs(ctx)
	if len(*commands) != 0 {
		t.Fatalf("PackMovedRelicRefs after fetches that moved no relic ran %q", *commands)
	}
}

// A relic whose ref is packed still reads, takes a compare-and-swap update,
// synchronizes both ways, and is deleted by a reset with its parked tip.
func TestAPackedRelicKeepsWorking(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	roots := relicsFetchedInto(t, first, second, config, 3)
	second.PackMovedRelicRefs(ctx)
	relicID := roots[0].Operation.RelicID
	if len(looseRefsUnder(t, second, relicRefPrefix)) != 0 {
		t.Fatal("the relic refs were not packed")
	}

	tip := readRelicTip(t, second, config, relicID)
	if tip.Head != roots[0].Head {
		t.Fatalf("the packed relic reads at %s, want %s", tip.Head, roots[0].Head)
	}
	// A write against a stale parent is refused although the ref is packed.
	child := writeRelicChild(t, second, config, tip, nil, relicTitleSet(700001, "Renamed after the pack"))
	stale := relicChildPack(tip, relicTitleSet(700002, "Lost race"))
	if _, err := second.WriteRelic(ctx, config, &tip, stale, foldRelic(t, &tip, stale), nil, "update relic"); core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("WriteRelic on a stale parent error = %v, want a stale write", err)
	}
	if got := relicRefHead(t, second, relicID); got != child.Head {
		t.Fatalf("relic ref = %s, want the child %s", got, child.Head)
	}
	if result, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() error = %v; result = %+v", err, result)
	}
	second.PackMovedRelicRefs(ctx)
	fetched := fetchRelics(t, first, config)
	assertRelicSyncOutcome(t, fetched, relicID, SyncFastForwarded)
	if got := relicRefHead(t, first, relicID); got != child.Head {
		t.Fatalf("first holds the relic at %s, want %s", got, child.Head)
	}

	parked := parkRelicTip(t, second, relicID, 1, roots[0].Head)
	second.noteRelicRefsMoved()
	second.PackMovedRelicRefs(ctx)
	if !packedRefNames(t, second)[parked] {
		t.Fatalf("the parked tip %s was not packed", parked)
	}
	plan, err := second.PlanLocalReset(ctx, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ResetLocal(ctx, config.ProjectID, plan, ResetOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := workbookRefs(t, second.Root); len(got) != 0 {
		t.Fatalf("refs left after the reset: %v", got)
	}
	for ref := range packedRefNames(t, second) {
		if strings.HasPrefix(ref, "refs/workbook/") {
			t.Fatalf("packed-refs still names %s after the reset", ref)
		}
	}
}

// fakeGit writes a git that answers pack-refs with the given script line and
// runs every other command with the real git.
func fakeGit(t *testing.T, packRefs string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = pack-refs ]; then\n    " + packRefs +
		"\n  fi\ndone\nexec '" + real + "' \"$@\"\n"
	testenv.WriteExecutable(t, path, []byte(script))
	return path
}

// A Git that refuses --include, as every Git before the option did, packs
// nothing and is not asked again by the same handle. A pack that fails for any
// other reason is retried by the next move.
func TestARefusedOrFailedPackChangesNothing(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		script   string
		askAgain bool
	}{
		"refuses --include": {
			script: "echo \"error: unknown option \\`include'\" >&2; echo 'usage: git pack-refs [--all] [--no-prune]' >&2; exit 129",
		},
		"fails": {
			script:   "echo \"fatal: Unable to create 'packed-refs.lock': File exists.\" >&2; exit 128",
			askAgain: true,
		},
	} {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second, config := syncRepositories(t)
			ctx := context.Background()
			second.gitPath = fakeGit(t, test.script)
			relicsFetchedInto(t, first, second, config, 3)
			before := everyRef(t, second)
			commands := observeCommands(second)
			second.PackMovedRelicRefs(ctx)
			if len(packCommands(*commands)) != 1 {
				t.Fatalf("ran %q, want one pack-refs", *commands)
			}
			if after := everyRef(t, second); after != before {
				t.Fatalf("refs changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if got := looseRefsUnder(t, second, relicRefPrefix); len(got) != 3 {
				t.Fatalf("loose relic refs = %v, want the three left loose", got)
			}

			second.noteRelicRefsMoved()
			*commands = nil
			second.PackMovedRelicRefs(ctx)
			if got := len(packCommands(*commands)); (got == 1) != test.askAgain {
				t.Fatalf("after a %s pack the next move ran %q", name, *commands)
			}
		})
	}
}

// A cancelled context runs nothing.
func TestACancelledPackRunsNothing(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	relicsFetchedInto(t, first, second, config, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	commands := observeCommands(second)
	second.PackMovedRelicRefs(ctx)
	if len(*commands) != 0 {
		t.Fatalf("a cancelled pack ran %q", *commands)
	}
}
