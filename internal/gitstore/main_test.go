package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// Tests that stay serial, and why. Go runs these before the parallel batch,
// so each gets the process to itself while it calls t.Setenv, which panics
// if the calling test or an ancestor has called t.Parallel.
//
//	TestManagedPrePushHookPublishesOnlyOriginAndHonorsRecursionGuard — hooks_test.go
//	TestManagedPrePushHookBlocksPushWhenWorkbookPushFails — hooks_test.go
//	TestOpenSpawnsOneGitProcessForRootAndCommonDir — repository_test.go
//	TestOpenFromLinkedWorktreeSpawnsOneGitProcess — repository_test.go
//	TestGitSeparatesStdoutAndStderrAndSetsReplaceProtection — repository_test.go
//	TestPushBypassesManagedHookRecursion — sync_test.go
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
// and maintenance.auto there — in mintSyncRepositories (sync_test.go), in
// adoptOrigin (adopt_test.go), and directly inside
// TestAdoptOriginProjectTaskRefsWithoutCommittedConfigFails (adopt_test.go)
// — but nothing guards the clones, the plain repositories writeRepository
// builds, or the other bare origins. Writing gc.auto=0,
// maintenance.auto=false, and receive.autogc=false into the global config
// this TestMain installs covers every repository the tests create, not just
// the three that were guarded per-origin. The three existing per-origin
// git config lines stay in place; they are redundant with the global file
// but harmless.
//
// TestMain also mints the two fixture templates, after the environment above
// and before m.Run. What they are and why they are minted here is on
// templateRoot, writeTemplate and syncTemplate.
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

	// The fixture templates are minted here, after the environment above and
	// not on whichever test asks for one first: the mint then runs with the
	// configuration every copy of it will be read under, and a mint that
	// cannot succeed says so once rather than failing every test that copies
	// it.
	templateRoot = filepath.Join(home, "template")
	if err := os.MkdirAll(templateRoot, 0o755); err != nil {
		panic("create template root: " + err.Error())
	}
	if _, _, err := writeTemplate(); err != nil {
		panic("mint write template: " + err.Error())
	}
	if _, err := syncTemplate(); err != nil {
		panic("mint sync template: " + err.Error())
	}

	code := m.Run()

	// Report rather than discard: a directory under this home that a child
	// process left read-only does not come away with os.RemoveAll, and a
	// discarded error there is a leak nobody sees. scripts/main_test.go carries
	// the occurrence that made the case.
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated home %s: %v\n", home, err)
	}
	os.Exit(code)
}

// templateRoot holds the two fixtures this package mints once and every test
// that wants one copies: the initialized project writeRepository hands out, and
// the bare-origin-and-two-clones trio syncRepositories hands out. They live
// under the isolated home directory TestMain creates, so that removing the home
// removes them, and so that they are minted against the same user-global git
// configuration every test runs against.
var templateRoot string

var (
	writeTemplateOnce   sync.Once
	writeTemplateDir    string
	writeTemplateConfig core.ProjectConfig
	writeTemplateErr    error
)

// writeTemplate mints the initialized project writeRepository copies, once for
// the package.
//
// TestMain calls this before m.Run, so the mint happens in a quiet process with
// the environment TestMain has just arranged, and a mint that fails stops the
// package with one message instead of failing each of the 115 tests that copy
// it. The sync.Once and the error every caller still checks are what make that
// ordering an optimization rather than a requirement.
func writeTemplate() (string, core.ProjectConfig, error) {
	writeTemplateOnce.Do(func() {
		dir := filepath.Join(templateRoot, "write")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeTemplateErr = err
			return
		}
		config, err := mintWriteRepository(dir)
		if err != nil {
			writeTemplateErr = err
			return
		}
		writeTemplateDir, writeTemplateConfig = dir, config
	})
	return writeTemplateDir, writeTemplateConfig, writeTemplateErr
}

var (
	syncTemplateOnce  sync.Once
	syncTemplateTrees syncTrees
	syncTemplateErr   error
)

// syncTemplate mints the SHA-1 sync fixture syncRepositories copies, once for
// the package, for the same reasons writeTemplate does.
//
// Only SHA-1 gets a template. SHA-256 is a capability an old Git does not have,
// and a mint that fails here can only panic; the test that wanted SHA-256 has
// to be the one that reports the capability missing, so those callers keep
// minting.
func syncTemplate() (syncTrees, error) {
	syncTemplateOnce.Do(func() {
		root := filepath.Join(templateRoot, "sync")
		if err := os.MkdirAll(root, 0o755); err != nil {
			syncTemplateErr = err
			return
		}
		syncTemplateTrees, syncTemplateErr = mintSyncRepositories(context.Background(), root, testrepo.FormatSHA1)
	})
	return syncTemplateTrees, syncTemplateErr
}
