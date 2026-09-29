package scripts_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests that stay serial, and why. There are none: this package has no
// t.Setenv, no os.Setenv, no os.Chdir, no t.Chdir, and no package-level
// mutable state (`grep -rn 't\.Setenv|os\.Setenv|os\.Unsetenv|os\.Chdir|t\.Chdir|os\.Getwd' scripts/*_test.go`
// finds nothing). Every test that needs a different PATH, HOME, GOCACHE,
// WORKBOOK_REPO, or FAKE_GH_ROOT sets it on its own exec.Cmd.Env, which is
// unaffected by another test running at the same time.
//
// One subtest group stays in order for a different reason:
//
//	TestDeleteReleaseTagRejectsUnsafeVersions — delete_release_tag_test.go
//
// Its four subtests share the parent's single clone and the parent asserts on
// that clone's tags after the loop. Parallel subtests would let that
// assertion run before they finish, which would make the whole test vacuous
// rather than racy, so it is not a t.Parallel() candidate at all.
//
// TestMain isolates the user-global and system git configuration that every
// fixture repository in this package would otherwise read. Two reasons.
//
// First, the fixtures in this package commit, tag, and push through `git
// commit`, `git tag`, and `git push` — real plumbing that reads the
// developer's real ~/.gitconfig and any system gitconfig. Nothing in this
// package intends to depend on that config, but core.hooksPath could redirect
// a hook one of the fixtures installs, commit.gpgsign could try to sign a
// fixture commit with a key the machine does not have, and core.fsmonitor
// could start a daemon that outlives the test and races t.TempDir's cleanup.
// A per-test t.Setenv would normally paper over this, but it panics once a
// test calls t.Parallel, so the environment has to be replaced once, in
// TestMain, before m.Run.
//
// Second, background auto-gc or git maintenance spawned by receive-pack on a
// bare remote can outlive its test and race t.TempDir's cleanup, which fails
// with "directory not empty" if a stray process still has the directory
// open. newTapRepository (publish_release_test.go) already guards its own
// bare remote against this with three git config lines. newReleaseRepository
// (cut_release_test.go), newTagDeletionRepository
// (delete_release_tag_test.go), and ciWorkflowGateRepository
// (ci_workflow_test.go) push to or commit into repositories with no such
// guard. Writing gc.auto=0, maintenance.auto=false, and receive.autogc=false
// into the global config this TestMain installs covers every repository any
// test creates, not just the one that was guarded per-fixture.
// newTapRepository's own three lines stay in place; they are redundant with
// the global file but harmless.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "workbook-scripts-home")
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

// isolatedGitConfigValues names the two environment entries that make a git
// invocation honor the isolated configuration TestMain installs above. Every
// exec.Cmd in this package either leaves Env nil or builds it from
// os.Environ(), so it inherits GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM
// automatically, except the two call sites that construct an environment
// from scratch (runWithPATH in check_ci_capabilities_test.go and
// buildEnvironment in install_default_test.go); they append this slice so
// the scripts they run still see the isolated git configuration rather than
// the developer's real one.
func isolatedGitConfigValues() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.Getenv("GIT_CONFIG_GLOBAL"),
		"GIT_CONFIG_NOSYSTEM=" + os.Getenv("GIT_CONFIG_NOSYSTEM"),
	}
}
