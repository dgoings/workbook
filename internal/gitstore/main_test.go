package gitstore

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests that stay serial, and why. Go runs these before the parallel batch,
// so each gets the process to itself while it calls t.Setenv, which panics
// if the calling test or an ancestor has called t.Parallel.
//
//	TestManagedPrePushHookPublishesOnlyOriginAndHonorsRecursionGuard — hooks_test.go:91
//	TestManagedPrePushHookBlocksPushWhenWorkbookPushFails — hooks_test.go:130
//	TestOpenSpawnsOneGitProcessForRootAndCommonDir — repository_test.go:164
//	TestOpenFromLinkedWorktreeSpawnsOneGitProcess — repository_test.go:190
//	TestGitSeparatesStdoutAndStderrAndSetsReplaceProtection — repository_test.go:326
//	TestPushBypassesManagedHookRecursion — sync_test.go:974
//
// The first two and the last set PATH and WORKBOOK_TEST_LOG to put a fake
// workbook ahead of the real one for the managed pre-push hook to resolve.
// The middle two set PATH to install a git-invocation counter and then count
// every git process the whole test binary spawns, so a concurrently running
// test's git would land in the same counter. The fifth sets
// WORKBOOK_ENV_SENTINEL to prove gitEnvironment preserves the ambient
// process environment. Nothing else in the package needs to be serial.
//
// TestMain isolates the user-global and system git configuration that every
// test repository in this package would otherwise read. Two reasons.
//
// First, without a TestMain, testrepo.New and testrepo.InitAt build every
// repository against the developer's real ~/.gitconfig and any system
// gitconfig (for example Xcode's). Measured on a real machine, that config
// carries a credential helper, aliases, init.defaultBranch, and
// push.autosetupremote, none of which this package's tests intend to depend
// on but all of which are live in every repository they create. A per-test
// t.Setenv("GIT_CONFIG_GLOBAL", ...) would normally paper over this, but it
// panics once a test calls t.Parallel, so the environment has to be replaced
// once, in TestMain, before m.Run.
//
// Second, background auto-gc or git maintenance spawned by any of the
// roughly 180 repositories the tests create can outlive its test and race
// t.TempDir's cleanup, which fails with "directory not empty" if a stray
// process still has the directory open. Three sync fixtures already guard
// against this on their own bare origin by setting receive.autogc, gc.auto,
// and maintenance.auto there — in syncRepositoriesWithObjectFormat
// (sync_test.go), in adoptOrigin (adopt_test.go), and directly inside
// TestAdoptOriginProjectTaskRefsWithoutCommittedConfigFails (adopt_test.go)
// — but nothing guards the clones, the plain repositories writeRepository
// builds, or the other bare origins. Writing gc.auto=0,
// maintenance.auto=false, and receive.autogc=false into the global config
// this TestMain installs covers every repository the tests create, not just
// the three that were guarded per-origin. The three existing per-origin
// git config lines stay in place; they are redundant with the global file
// but harmless.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "workbook-gitstore-home")
	if err != nil {
		panic("create isolated home: " + err.Error())
	}

	gitconfig := filepath.Join(home, "gitconfig")
	contents := "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[receive]\n\tautogc = false\n"
	if err := os.WriteFile(gitconfig, []byte(contents), 0o600); err != nil {
		panic("write isolated gitconfig: " + err.Error())
	}

	os.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}
